package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
)

const expirySlug = "bitcoin-above-120000-at-observation"

type directExpiryRead struct {
	started, end time.Time
}

// The first response establishes one canonical end. Creation can be held on
// that response, or a successful create can be followed by a held ID read of
// the same immutable contract. Entry and release channels prove the response
// crosses the end, rather than relying on a sleep to start the request.
func directExpiryGamma(t *testing.T, e *apptest.Env, holdCreate bool) (<-chan directExpiryRead, func()) {
	t.Helper()
	entered := make(chan directExpiryRead, 1)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	var mu sync.Mutex
	var end time.Time
	reads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/markets" || r.URL.Query().Get("slug") != expirySlug && r.URL.Query().Get("id") != directID {
			http.Error(w, "Unexpected Gamma request", http.StatusBadRequest)
			return
		}
		mu.Lock()
		started := time.Now().UTC()
		if reads == 0 {
			lead := 250 * time.Millisecond
			if !holdCreate {
				// Enough time for the initial create and checker query, while
				// keeping the held read well inside the canonical five seconds.
				lead = time.Second
			}
			end = started.Add(lead)
		}
		reads++
		ordinal, boundary := reads, end
		mu.Unlock()
		if holdCreate || ordinal > 1 {
			select {
			case entered <- directExpiryRead{started: started, end: boundary}:
			case <-r.Context().Done():
				return
			}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		p := 0.3
		if ordinal > 1 {
			p = 0.8
		}
		observation := boundary.Format(time.RFC3339Nano)
		leaf := map[string]any{
			"id": directID, "slug": expirySlug, "conditionId": directCond,
			"question":         fmt.Sprintf("Will Bitcoin be strictly above $120,000 at %s?", observation),
			"description":      fmt.Sprintf("Resolve Yes if the Binance BTC/USDT price is strictly above $120,000 at %s; otherwise No.", observation),
			"resolutionSource": directSource, "endDate": observation,
			"active": true, "closed": false, "archived": false,
			"acceptingOrders": true, "enableOrderBook": true, "outcomes": `["Yes","No"]`,
			"outcomePrices": fmt.Sprintf(`["%g","%g"]`, p, 1-p), "volumeNum": 250000.0,
			"events": []map[string]any{{"id": "7101", "slug": "bitcoin-at-observation", "title": "Bitcoin at observation", "active": true, "closed": false}},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]any{leaf})
	}))
	t.Cleanup(func() {
		unblock()
		srv.Close()
	})
	e.PolymarketBase = srv.URL
	return entered, unblock
}

func releaseDirectReadAfterExpiry(t *testing.T, ctx context.Context, entered <-chan directExpiryRead, release func()) {
	t.Helper()
	var read directExpiryRead
	select {
	case read = <-entered:
	case <-ctx.Done():
		t.Fatalf("canonical request did not reach its response barrier: %v", ctx.Err())
	}
	if !read.started.Before(read.end) {
		t.Fatalf("fixture must enter the fresh read before expiry: entered=%s end=%s", read.started, read.end)
	}
	timer := time.NewTimer(time.Until(read.end.Add(20 * time.Millisecond)))
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		t.Fatalf("expired response was not released within the test budget: %v", ctx.Err())
	}
	if !time.Now().After(read.end) {
		t.Fatal("fixture must release the response after the canonical end")
	}
	release()
}

func expiryBody() map[string]any {
	return map[string]any{"trigger": "odds", "marketSlug": expirySlug, "marketId": directID, "op": "above", "price": 0.5}
}

func joinDirectExpiryRead(t *testing.T, cancel context.CancelFunc, release func(), finished <-chan struct{}) {
	t.Helper()
	t.Cleanup(func() {
		cancel()
		release()
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		select {
		case <-finished:
		case <-timer.C:
			t.Error("direct expiry request did not stop during cleanup")
		}
	})
}

func TestDirectOddsCreateRejectsExpiryDuringFreshRead(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	assertDirectFree(t, e)
	entered, release := directExpiryGamma(t, e, true)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	done := make(chan *httptest.ResponseRecorder, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		done <- e.DoCtx(ctx, e.Cookie, "POST", "/api/alerts", expiryBody())
	}()
	joinDirectExpiryRead(t, cancel, release, finished)
	releaseDirectReadAfterExpiry(t, ctx, entered, release)
	select {
	case w := <-done:
		if w.Code != http.StatusBadRequest || apptest.ErrorCode(t, w) != "bad_request" {
			t.Errorf("a leaf expiring during the fresh create read must be rejected: %d %s", w.Code, w.Body)
		}
	case <-ctx.Done():
		t.Fatalf("direct create did not finish within the test budget: %v", ctx.Err())
	}
	var count int
	if err := e.DB.QueryRow(context.Background(), "select count(*) from alerts").Scan(&count); err != nil || count != 0 {
		t.Errorf("expired fresh create must persist no alert: count=%d err=%v", count, err)
	}
}

func TestCheckDirectOddsExpiryDuringFreshReadRemainsDormant(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	assertDirectFree(t, e)
	directClock(t)
	entered, release := directExpiryGamma(t, e, false)
	a := directCreate(t, e, e.Cookie, expiryBody())
	id := a["id"].(string)
	var beforeQuote, beforeLink []byte
	if err := e.DB.QueryRow(ctx, "select market_odds, linked_market from alerts where id=$1", id).Scan(&beforeQuote, &beforeLink); err != nil {
		t.Fatal(err)
	}
	if directObject(t, a["market"])["yesPrice"] != 0.3 {
		t.Fatal("fixture must first store a valid current quote below the trigger")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		done <- Check(ctx, e.Deps)
	}()
	joinDirectExpiryRead(t, cancel, release, finished)
	releaseDirectReadAfterExpiry(t, ctx, entered, release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("an ended target should stay dormant without failing the checker: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("direct checker did not finish within the test budget: %v", ctx.Err())
	}
	stored := load(t, e, id)
	if stored.Status != "active" || stored.FiredAt != nil || stored.FiredPrice != nil || stored.TradeID != nil {
		t.Errorf("a response crossing the leaf end must not fire the direct alert: %+v", stored)
	}
	var afterQuote, afterLink []byte
	if err := e.DB.QueryRow(ctx, "select market_odds, linked_market from alerts where id=$1", id).Scan(&afterQuote, &afterLink); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterQuote, beforeQuote) || !bytes.Equal(afterLink, beforeLink) {
		t.Errorf("an ended response must not reprice or refresh asOf: beforeQuote=%s afterQuote=%s beforeLink=%s afterLink=%s", beforeQuote, afterQuote, beforeLink, afterLink)
	}
	w := e.Do("GET", "/api/notifications", nil)
	items := apptest.JSON[struct{ Items []Notification }](t, w).Items
	if w.Code != http.StatusOK || len(items) != 0 {
		t.Errorf("an ended target must not produce a Bell notification: status=%d items=%+v", w.Code, items)
	}
}

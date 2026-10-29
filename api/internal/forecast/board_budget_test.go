package forecast

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
)

// Each independent leader fits the market HTTP timeout. The category reader
// must still bound its sequential waits behind both shared keys by boardBudget.
func TestBoardBudgetIncludesSharedMarketCacheWaits(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	assetsStarted, assetsRelease, candlesStarted := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var release sync.Once
	unblock := func() { release.Do(func() { close(assetsRelease) }) }
	var candleReads atomic.Int32
	now := time.Now().UTC()
	ticker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/info" {
			http.Error(w, "fixture has no xyz venue", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path == "/api/v3/ticker/24hr" {
			close(assetsStarted)
			select {
			case <-assetsRelease:
			case <-r.Context().Done():
				return
			}
			rows := []map[string]any{}
			for _, a := range market.CryptoUniverse {
				rows = append(rows, map[string]any{"symbol": a.Pair, "lastPrice": "100", "closeTime": time.Now().UnixMilli()})
			}
			json.NewEncoder(w).Encode(rows)
			return
		}
		if candleReads.Add(1) == 1 {
			close(candlesStarted)
		}
		timer := time.NewTimer(9 * time.Second)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-r.Context().Done():
			return
		}
		day := now.Truncate(24 * time.Hour)
		rows := []any{}
		for i := 3; i > 0; i-- {
			rows = append(rows, []any{day.AddDate(0, 0, -i).UnixMilli(), "100", "110", "90", fmt.Sprint(100 + i*i), "10"})
		}
		json.NewEncoder(w).Encode(rows)
	}))
	t.Cleanup(ticker.Close)
	client := testMarketClient(ticker.URL)
	assetDone, candlesDone := make(chan error, 1), make(chan error, 1)
	var leaders sync.WaitGroup
	leaders.Go(func() { _, err := client.Assets(ctx); assetDone <- err })
	t.Cleanup(func() { cancel(); unblock(); leaders.Wait() })
	select {
	case <-assetsStarted:
	case <-ctx.Done():
		t.Fatal("asset leader did not enter upstream")
	}
	leaders.Go(func() {
		timer := time.NewTimer(8 * time.Second)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return
		}
		leaders.Go(func() { _, err := client.Candles(ctx, "BTC", "1d", 300); candlesDone <- err })
		select {
		case <-candlesStarted:
			unblock()
		case <-ctx.Done():
		}
	})
	leaf := canonicalLeaf("900", "Will Bitcoin be above $110 on December 31?", "Complete external rules.")
	leaf["endDate"] = now.Add(48 * time.Hour).Format(time.RFC3339)
	gamma := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{ev("parent", "Parent", leaf["endDate"].(string), 50000, leaf)})
	}))
	t.Cleanup(gamma.Close)
	b := newBoard(&app.Deps{Market: client, PolymarketBase: gamma.URL})
	started := time.Now()
	events, _, _, err := b.read(context.Background(), "crypto")
	elapsed := time.Since(started)
	if elapsed > boardBudget+time.Second || err == nil || len(events) != 0 {
		t.Errorf("expired category read must stop without fresh publication: elapsed=%s budget=%s events=%d err=%v", elapsed, boardBudget, len(events), err)
	}
	if events, _, _, ok := b.lists["crypto"].serve(b.now()); ok || len(events) != 0 {
		t.Errorf("expired fetch published listing: %+v", events)
	}
	for _, done := range []<-chan error{assetDone, candlesDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal("independent healthy leader failed:", err)
			}
		case <-ctx.Done():
			t.Fatal("independent leader did not join")
		}
	}
	leaders.Wait()
	if events, _, _, ok := b.lists["crypto"].serve(b.now()); ok || len(events) != 0 {
		t.Errorf("leader completion published a late listing: %+v", events)
	}
	if bars, err := client.Candles(ctx, "BTC", "1d", 300); err != nil || len(bars) != 3 || candleReads.Load() != 1 {
		t.Fatalf("canceled board waiter damaged healthy candle cache: bars=%d reads=%d err=%v", len(bars), candleReads.Load(), err)
	}
}

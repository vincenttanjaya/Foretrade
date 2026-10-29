package alerts

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
)

const (
	directID     = "8101"
	directSlug   = "bitcoin-above-120000-december-31-2099"
	directEvent  = "bitcoin-above-on-december-31-2099"
	directQ      = "Will Bitcoin be above $120,000 at noon ET on December 31, 2099?"
	directRules  = "This market resolves Yes if Bitcoin is strictly above $120,000 at 12:00 PM ET on December 31, 2099, using the Binance BTC/USDT price; otherwise No."
	directSource = "https://www.binance.com/en/trade/BTC_USDT"
	directEnd    = "2099-12-31T17:00:00Z"
	directCond   = "0x" + "1111111111111111111111111111111111111111111111111111111111111111"
)

// directGamma serves Gamma's leaf lookup and canonical ID reads. Events and
// markets have different slugs: accidentally treating an event as a leaf must
// not pick its first market. Only the external HTTP boundary is replaced.
type directGamma struct {
	mu     sync.Mutex
	market map[string]any
	others map[string]map[string]any // ID keyed, for the fair minute-check queue
	extra  []map[string]any
	hits   map[string]int
	down   bool
	onRead func()
}

func newDirectGamma(t *testing.T, e *apptest.Env) *directGamma {
	t.Helper()
	g := &directGamma{market: map[string]any{
		"id": directID, "slug": directSlug, "conditionId": directCond,
		"question": directQ, "description": directRules, "resolutionSource": directSource,
		"endDate": directEnd, "active": true, "closed": false, "archived": false,
		"acceptingOrders": true, "enableOrderBook": true, "outcomes": `["Yes","No"]`,
		"outcomePrices": `["0.3","0.7"]`, "volumeNum": 250000.0,
		"events": []map[string]any{{"id": "7101", "slug": directEvent, "title": "Bitcoin on December 31", "active": true, "closed": false}},
	}, hits: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		key := r.URL.Path + "?" + r.URL.RawQuery
		g.hits[key]++
		m, extra, down, hook := maps.Clone(g.market), g.extra, g.down, g.onRead
		id, slug := r.URL.Query().Get("id"), r.URL.Query().Get("slug")
		if strings.HasPrefix(r.URL.Path, "/markets/") {
			id = strings.TrimPrefix(r.URL.Path, "/markets/")
		}
		knownID, knownSlug := id == directID, slug == directSlug
		if alternate, ok := g.others[id]; ok {
			m, knownID = maps.Clone(alternate), true
		} else if slug != directSlug && r.URL.Path == "/markets" {
			for _, alternate := range g.others {
				if alternate["slug"] == slug {
					m, knownSlug = maps.Clone(alternate), true
					break
				}
			}
		}
		g.mu.Unlock()
		if hook != nil {
			hook()
		}
		if down {
			http.Error(w, "Gamma unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/markets" && r.URL.Query().Get("slug") != "":
			out := []map[string]any{}
			if knownSlug && m != nil {
				out = append(out, m)
				out = append(out, extra...)
			}
			json.NewEncoder(w).Encode(out)
		case r.URL.Path == "/markets" && knownID:
			out := []map[string]any{}
			if m != nil {
				out = append(out, m)
			}
			json.NewEncoder(w).Encode(out)
		case strings.HasPrefix(r.URL.Path, "/markets/") && knownID:
			if m == nil {
				http.NotFound(w, r)
				return
			}
			json.NewEncoder(w).Encode(m)
		case r.URL.Path == "/events" && r.URL.Query().Get("slug") == directEvent:
			out := []map[string]any{}
			if m != nil {
				out = append(out, map[string]any{"slug": directEvent, "active": true, "closed": false, "markets": []map[string]any{m}})
			}
			json.NewEncoder(w).Encode(out)
		default:
			http.Error(w, "Unexpected Gamma request "+r.URL.String(), http.StatusBadRequest)
		}
	}))
	t.Cleanup(srv.Close)
	e.PolymarketBase = srv.URL
	return g
}

func (g *directGamma) change(f func(map[string]any)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	f(g.market)
}

func (g *directGamma) price(p float64) {
	g.change(func(m map[string]any) {
		m["outcomePrices"] = fmt.Sprintf(`["%g","%g"]`, p, 1-p)
	})
}

func (g *directGamma) reads() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for _, hits := range g.hits {
		n += hits
	}
	clear(g.hits)
	return n
}

func directBody(op string, p float64) map[string]any {
	return map[string]any{"trigger": "odds", "marketSlug": directSlug, "op": op, "price": p}
}

func directCreate(t *testing.T, e *apptest.Env, cookie *http.Cookie, body map[string]any) map[string]any {
	t.Helper()
	w := e.DoAs(cookie, "POST", "/api/alerts", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("direct odds create: %d %s", w.Code, w.Body)
	}
	return apptest.JSON[struct{ Alert map[string]any }](t, w).Alert
}

func directObject(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("expected JSON object, got %#v", v)
	}
	return m
}

func assertDirectSnapshot(t *testing.T, a map[string]any) {
	t.Helper()
	if a["forecastId"] != nil || a["marketId"] != directID || a["marketSlug"] != directSlug || a["symbol"] != "" {
		t.Fatalf("direct canonical target: %#v", a)
	}
	l := directObject(t, a["linkedMarket"])
	for k, want := range map[string]string{
		"marketId": directID, "conditionId": directCond, "marketSlug": directSlug,
		"eventSlug": directEvent, "question": directQ, "description": directRules,
		"resolutionSource": directSource, "endDate": directEnd,
	} {
		if l[k] != want {
			t.Errorf("canonical %s = %#v, want %q", k, l[k], want)
		}
	}
	asOf, _ := l["asOf"].(string)
	when, err := time.Parse(time.RFC3339Nano, asOf)
	if err != nil || time.Since(when) < -time.Second || time.Since(when) > time.Minute {
		t.Errorf("canonical snapshot must have a fresh timestamp: %q (%v)", asOf, err)
	}
	m := directObject(t, a["market"])
	if m["question"] != directQ || m["url"] != "https://polymarket.com/event/"+directEvent || m["outcome"] != nil {
		t.Errorf("direct crowd market: %#v", m)
	}
}

// Alerts bypass the paid forecast path completely, even for an account with
// no AI allowance. Checking a direct alert also cannot place a paper stake.
func assertDirectFree(t *testing.T, e *apptest.Env) {
	t.Helper()
	t.Cleanup(func() {
		var used, trades, positions, forecasts int
		if err := e.DB.QueryRow(ctx, `select
			coalesce((select sum(credits) from ai_usage), 0),
			(select count(*) from trades), (select count(*) from market_positions),
			(select count(*) from forecasts)`).Scan(&used, &trades, &positions, &forecasts); err != nil {
			t.Fatal(err)
		}
		if used != 0 || trades != 0 || positions != 0 || forecasts != 0 || len(e.Bedrock.Requests()) != 0 {
			t.Errorf("direct alerts must be free notify-only: credits=%d trades=%d positions=%d forecasts=%d modelCalls=%d",
				used, trades, positions, forecasts, len(e.Bedrock.Requests()))
		}
	})
}

func directClock(t *testing.T) func(time.Duration) {
	t.Helper()
	oddsMu.Lock()
	oldRepriced, oldWarned, oldPruned := repriced, warned, pruned
	repriced, warned, pruned = map[string]time.Time{}, map[string]time.Time{}, time.Time{}
	oddsMu.Unlock()
	t.Cleanup(func() {
		oddsMu.Lock()
		repriced, warned, pruned = oldRepriced, oldWarned, oldPruned
		oddsMu.Unlock()
	})
	return at(t, time.Now())
}

func TestDirectOddsCreateStoresCanonicalLeafAndIsFree(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	newDirectGamma(t, e)
	assertDirectFree(t, e)
	if _, err := e.DB.Exec(ctx, "update users set credit_limit = 0 where id = $1", e.User); err != nil {
		t.Fatal(err)
	}
	body := directBody("above", 0.5)
	body["marketId"], body["note"] = directID, "  Noon ET crowd odds  "
	a := directCreate(t, e, e.Cookie, body)
	assertDirectSnapshot(t, a)
	if a["action"] != "notify" || a["source"] != "manual" || a["status"] != "active" || a["note"] != "Noon ET crowd odds" || a["notional"] != nil || a["tradeId"] != nil {
		t.Fatalf("direct notify alert: %#v", a)
	}
	if m := directObject(t, a["market"]); m["yesPrice"] != 0.3 {
		t.Fatalf("create uses fresh own Yes price: %#v", m)
	}
	w := e.Do("GET", "/api/alerts?status=active", nil)
	list := apptest.JSON[struct{ Alerts []map[string]any }](t, w).Alerts
	if w.Code != http.StatusOK || len(list) != 1 || list[0]["id"] != a["id"] {
		t.Fatalf("direct alert list: %d %#v", w.Code, list)
	}
	assertDirectSnapshot(t, list[0])
}

func TestDirectOddsTargetValidation(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	g := newDirectGamma(t, e)
	fc := oddsForecast(t, e, e.User, "unrelated", 0.2)
	for _, c := range []struct {
		name string
		edit func(map[string]any)
	}{
		{"neither target", func(b map[string]any) { delete(b, "marketSlug") }},
		{"both targets", func(b map[string]any) { b["forecastId"] = fc }},
		{"id is not a target", func(b map[string]any) { delete(b, "marketSlug"); b["marketId"] = directID }},
		{"event slug is not a leaf", func(b map[string]any) { b["marketSlug"] = directEvent }},
		{"id mismatch", func(b map[string]any) { b["marketId"] = "8102" }},
		{"id is not numeric", func(b map[string]any) { b["marketId"] = "not-an-id" }},
		{"slug traversal", func(b map[string]any) { b["marketSlug"] = "../markets" }},
		{"buy forbidden", func(b map[string]any) { b["action"], b["notional"] = "buy", 100 }},
		{"sell forbidden", func(b map[string]any) { b["action"], b["notional"] = "sell", 100 }},
		{"notional forbidden", func(b map[string]any) { b["notional"] = 100 }},
		{"stop loss forbidden", func(b map[string]any) { b["stopLoss"] = 0.2 }},
		{"take profit forbidden", func(b map[string]any) { b["takeProfit"] = 0.8 }},
		{"op invalid", func(b map[string]any) { b["op"] = "near" }},
		{"level below range", func(b map[string]any) { b["price"] = 0.009 }},
		{"level above range", func(b map[string]any) { b["price"] = 0.991 }},
		{"above already reached", func(b map[string]any) { b["price"] = 0.3 }},
		{"below already reached", func(b map[string]any) { b["op"], b["price"] = "below", 0.3 }},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := directBody("above", 0.5)
			c.edit(b)
			if w := e.Do("POST", "/api/alerts", b); w.Code != http.StatusBadRequest || apptest.ErrorCode(t, w) != "bad_request" {
				t.Fatalf("invalid direct target accepted: %d %s", w.Code, w.Body)
			}
		})
	}
	// The genuine direct target succeeds; blanket rejection cannot satisfy the
	// negative cases. A below alert monitors this leaf's own Yes, not its No.
	a := directCreate(t, e, e.Cookie, directBody("below", 0.2))
	if m := directObject(t, a["market"]); m["yesPrice"] != 0.3 || a["op"] != "below" {
		t.Fatalf("direct below must preserve own Yes: %#v", a)
	}
	if g.reads() == 0 {
		t.Fatal("a direct target was accepted without a fresh canonical Gamma read")
	}
}

func TestDirectOddsRejectsUnsafeCanonicalMarketsBeforeCreating(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(*directGamma)
	}{
		{"closed", func(g *directGamma) { g.change(func(m map[string]any) { m["closed"] = true }) }},
		{"inactive", func(g *directGamma) { g.change(func(m map[string]any) { m["active"] = false }) }},
		{"archived", func(g *directGamma) { g.change(func(m map[string]any) { m["archived"] = true }) }},
		{"paused", func(g *directGamma) { g.change(func(m map[string]any) { m["acceptingOrders"] = false }) }},
		{"ended", func(g *directGamma) { g.change(func(m map[string]any) { m["endDate"] = "2020-01-01T00:00:00Z" }) }},
		{"missing condition", func(g *directGamma) { g.change(func(m map[string]any) { delete(m, "conditionId") }) }},
		{"missing rules", func(g *directGamma) { g.change(func(m map[string]any) { delete(m, "description") }) }},
		{"missing parent", func(g *directGamma) { g.change(func(m map[string]any) { m["events"] = []map[string]any{} }) }},
		{"closed parent", func(g *directGamma) {
			g.change(func(m map[string]any) {
				m["events"] = []map[string]any{{"slug": directEvent, "active": true, "closed": true}}
			})
		}},
		{"non binary", func(g *directGamma) { g.change(func(m map[string]any) { m["outcomes"] = `["Up","Down"]` }) }},
		{"malformed prices", func(g *directGamma) { g.change(func(m map[string]any) { m["outcomePrices"] = `["NaN","0.7"]` }) }},
		{"price out of range", func(g *directGamma) { g.change(func(m map[string]any) { m["outcomePrices"] = `["1.3","-0.3"]` }) }},
		{"wrong slug returned", func(g *directGamma) { g.change(func(m map[string]any) { m["slug"] = "unrelated-market" }) }},
		{"missing", func(g *directGamma) { g.market = nil }},
		{"ambiguous leaf", func(g *directGamma) {
			other := maps.Clone(g.market)
			other["id"], other["conditionId"] = "8102", "0x"+strings.Repeat("2", 64)
			g.extra = []map[string]any{other}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := apptest.New(t, Register, apptest.Options{})
			g := newDirectGamma(t, e)
			assertDirectFree(t, e)
			c.edit(g)
			w := e.Do("POST", "/api/alerts", directBody("above", 0.5))
			if w.Code != http.StatusBadRequest || apptest.ErrorCode(t, w) != "bad_request" {
				t.Fatalf("unsafe Gamma leaf accepted: %d %s", w.Code, w.Body)
			}
			var alerts int
			if err := e.DB.QueryRow(ctx, "select count(*) from alerts").Scan(&alerts); err != nil || alerts != 0 {
				t.Fatalf("unsafe create left %d alerts: %v", alerts, err)
			}
		})
	}
}

func TestDirectOddsCreateRejectsOutageRatherThanUsingStoredOdds(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	g := newDirectGamma(t, e)
	assertDirectFree(t, e)
	directCreate(t, e, e.Cookie, directBody("above", 0.5))
	g.mu.Lock()
	g.down = true
	g.mu.Unlock()
	w := e.Do("POST", "/api/alerts", directBody("above", 0.8))
	if w.Code != http.StatusBadGateway || apptest.ErrorCode(t, w) != "upstream" {
		t.Fatalf("outage must reject a new direct alert: %d %s", w.Code, w.Body)
	}
	var count int
	if err := e.DB.QueryRow(ctx, "select count(*) from alerts").Scan(&count); err != nil || count != 1 {
		t.Fatalf("outage must not create another alert: count=%d err=%v", count, err)
	}
}

func TestCheckDirectOddsSharesFreshIDReadAndFiresOnce(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	g := newDirectGamma(t, e)
	advance := directClock(t)
	assertDirectFree(t, e)
	other := storetest.User(t, e.DB)
	otherCookie := e.Session(other)
	up := directCreate(t, e, e.Cookie, directBody("above", 0.5))
	second := directCreate(t, e, otherCookie, directBody("above", 0.5))
	down := directCreate(t, e, e.Cookie, directBody("below", 0.2))
	g.reads()
	g.price(0.5) // boundary, not strictly beyond it
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			if err := Check(ctx, e.Deps); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	for _, target := range []map[string]any{up, second} {
		if a := load(t, e, target["id"].(string)); a.Status != "fired" || a.FiredPrice == nil || *a.FiredPrice != 0.5 || a.FiredAt == nil || a.TradeID != nil {
			t.Errorf("direct boundary alert: %+v", a)
		}
	}
	if load(t, e, down["id"].(string)).Status != "active" || g.reads() != 1 {
		t.Fatal("three alerts on one canonical ID need one fresh read; the below alert stays active")
	}
	items := apptest.JSON[struct{ Items []Notification }](t, e.Do("GET", "/api/notifications", nil)).Items
	if len(items) != 1 || items[0].ID != "odds_alert:"+up["id"].(string) || items[0].Kind != "odds_alert" ||
		items[0].Link != "/automations?tab=history&alert="+up["id"].(string) || items[0].Symbol != nil ||
		!strings.Contains(items[0].Title, directQ) || items[0].Body != "Now 50%" || strings.Contains(items[0].Body, "desk") {
		t.Fatalf("direct bell must link to the owned alert without inventing a desk read: %+v", items)
	}
	// Other owners' alerts never leak into this user's bell or list.
	otherItems := apptest.JSON[struct{ Items []Notification }](t, e.DoAs(otherCookie, "GET", "/api/notifications", nil)).Items
	if len(otherItems) != 1 || otherItems[0].ID != "odds_alert:"+second["id"].(string) {
		t.Fatalf("direct bell ownership: %+v", otherItems)
	}

	g.price(0.2)
	advance(59 * time.Second)
	if err := Check(ctx, e.Deps); err != nil || load(t, e, down["id"].(string)).Status != "active" || g.reads() != 0 {
		t.Fatalf("direct ID must be throttled for a minute: %v", err)
	}
	advance(time.Second)
	if err := Check(ctx, e.Deps); err != nil {
		t.Fatal(err)
	}
	if a := load(t, e, down["id"].(string)); a.Status != "fired" || a.FiredPrice == nil || *a.FiredPrice != 0.2 || g.reads() != 1 {
		t.Fatalf("direct below boundary: %+v", a)
	}
	fired := load(t, e, up["id"].(string)).FiredAt
	advance(time.Minute)
	if err := Check(ctx, e.Deps); err != nil || g.reads() != 0 || !load(t, e, up["id"].(string)).FiredAt.Equal(*fired) {
		t.Fatalf("a fired direct alert must never fire again: %v", err)
	}
	if n := apptest.JSON[struct{ Items []Notification }](t, e.Do("GET", "/api/notifications", nil)).Items; len(n) != 2 {
		t.Fatalf("one bell item per direct alert: %+v", n)
	}
}

func TestCheckDirectOddsOutageNeverFiresStoredCrossedPrice(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	g := newDirectGamma(t, e)
	advance := directClock(t)
	assertDirectFree(t, e)
	a := directCreate(t, e, e.Cookie, directBody("above", 0.5))
	g.price(0.8)
	// Even an old persisted quote beyond the threshold is not enough: the
	// checker must obtain a fresh successful canonical read before firing.
	if _, err := e.DB.Exec(ctx, `update alerts set market_odds = jsonb_set(market_odds, '{yesPrice}', '0.8') where id = $1`, a["id"]); err != nil {
		t.Fatal(err)
	}
	g.mu.Lock()
	g.down = true
	g.mu.Unlock()
	g.reads()
	if err := Check(ctx, e.Deps); err != nil || load(t, e, a["id"].(string)).Status != "active" || g.reads() != 1 {
		t.Fatalf("Gamma outage must not fire cached direct odds: %v", err)
	}
	if err := Check(ctx, e.Deps); err != nil || g.reads() != 0 {
		t.Fatalf("failed direct reads must also respect minute throttling: %v", err)
	}
	g.mu.Lock()
	g.down = false
	g.mu.Unlock()
	advance(time.Minute)
	if err := Check(ctx, e.Deps); err != nil || load(t, e, a["id"].(string)).Status != "fired" {
		t.Fatalf("fresh recovery should fire at the live own Yes: %v", err)
	}
}

func TestCheckDirectOddsChangedContractRemainsDormant(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(map[string]any)
	}{
		{"closed", func(m map[string]any) { m["closed"] = true }},
		{"paused", func(m map[string]any) { m["acceptingOrders"] = false }},
		{"changed rules", func(m map[string]any) { m["description"] = "Resolve from Coinbase at midnight UTC instead." }},
		{"changed question", func(m map[string]any) { m["question"] = "Will Bitcoin exceed $130,000 instead?" }},
		{"changed source", func(m map[string]any) { m["resolutionSource"] = "https://www.coinbase.com/price/bitcoin" }},
		{"changed end", func(m map[string]any) { m["endDate"] = "2099-12-31T00:00:00Z" }},
		{"changed condition", func(m map[string]any) { m["conditionId"] = "0x" + strings.Repeat("2", 64) }},
		{"changed leaf", func(m map[string]any) { m["slug"] = "different-leaf-market" }},
		{"changed parent", func(m map[string]any) {
			m["events"] = []map[string]any{{"slug": "other-event", "active": true, "closed": false}}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := apptest.New(t, Register, apptest.Options{})
			g := newDirectGamma(t, e)
			advance := directClock(t)
			assertDirectFree(t, e)
			a := directCreate(t, e, e.Cookie, directBody("above", 0.5))
			original := maps.Clone(g.market)
			g.price(0.8)
			g.change(c.edit)
			if err := Check(ctx, e.Deps); err != nil || load(t, e, a["id"].(string)).Status != "active" {
				t.Fatalf("changed canonical contract must be dormant: %v", err)
			}
			list := apptest.JSON[struct{ Alerts []map[string]any }](t, e.Do("GET", "/api/alerts", nil)).Alerts
			if len(list) != 1 {
				t.Fatalf("dormant alert remains visible: %#v", list)
			}
			assertDirectSnapshot(t, list[0])
			g.mu.Lock()
			g.market = original
			g.mu.Unlock()
			g.price(0.8)
			advance(time.Minute)
			if err := Check(ctx, e.Deps); err != nil || load(t, e, a["id"].(string)).Status != "fired" {
				t.Fatalf("only restoration of the pinned contract permits firing: %v", err)
			}
		})
	}
}

func TestCheckDirectOddsMissingMarketRemainsDormant(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	g := newDirectGamma(t, e)
	advance := directClock(t)
	assertDirectFree(t, e)
	a := directCreate(t, e, e.Cookie, directBody("above", 0.5))
	g.mu.Lock()
	original := g.market
	g.market = nil
	g.mu.Unlock()
	if err := Check(ctx, e.Deps); err != nil || load(t, e, a["id"].(string)).Status != "active" {
		t.Fatalf("a missing leaf must not fire from its stored price: %v", err)
	}
	g.mu.Lock()
	g.market = original
	g.mu.Unlock()
	g.price(0.8)
	advance(time.Minute)
	if err := Check(ctx, e.Deps); err != nil || load(t, e, a["id"].(string)).Status != "fired" {
		t.Fatalf("a restored exact leaf should fire: %v", err)
	}
}

func TestCheckDirectOddsSkipsDisabledOwnersBeforeReading(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	g := newDirectGamma(t, e)
	directClock(t)
	assertDirectFree(t, e)
	a := directCreate(t, e, e.Cookie, directBody("above", 0.5))
	if _, err := e.DB.Exec(ctx, "update users set disabled_at = now() where id = $1", e.User); err != nil {
		t.Fatal(err)
	}
	g.price(0.8)
	g.reads()
	if err := Check(ctx, e.Deps); err != nil || load(t, e, a["id"].(string)).Status != "active" || g.reads() != 0 {
		t.Fatalf("disabled owners must not cause reads or firing: %v", err)
	}
	if _, err := e.DB.Exec(ctx, "update users set disabled_at = null where id = $1", e.User); err != nil {
		t.Fatal(err)
	}
	if err := Check(ctx, e.Deps); err != nil || load(t, e, a["id"].(string)).Status != "fired" {
		t.Fatalf("an enabled owner with fresh crossed odds should fire: %v", err)
	}
}

func TestCheckDirectOddsRechecksEnabledOwnerAtClaim(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	g := newDirectGamma(t, e)
	directClock(t)
	assertDirectFree(t, e)
	a := directCreate(t, e, e.Cookie, directBody("above", 0.5))
	g.price(0.8)
	started, release := make(chan struct{}), make(chan struct{})
	var once, startedOnce sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	g.mu.Lock()
	g.onRead = func() { startedOnce.Do(func() { close(started) }); <-release }
	g.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- Check(ctx, e.Deps) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("checker never requested its fresh Gamma price")
	}
	// Disable after the initial enabled-user selection but before the price
	// returns. The claim must enforce enabled ownership again atomically.
	if _, err := e.DB.Exec(ctx, "update users set disabled_at = now() where id = $1", e.User); err != nil {
		unblock()
		t.Fatal(err)
	}
	unblock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("checker did not finish after Gamma returned")
	}
	if got := load(t, e, a["id"].(string)); got.Status != "active" || got.FiredAt != nil || got.FiredPrice != nil {
		t.Fatalf("a user disabled during the read must not be claimed: %+v", got)
	}
}

func TestDirectOddsPreservesForecastOwnershipAndCompletedRequirement(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	g := newGamma(t, e)
	g.set("fed-cut", 0.3)
	other := storetest.User(t, e.DB)
	foreign := oddsForecast(t, e, other, "fed-cut", 0.2)
	archived := oddsForecast(t, e, e.User, "fed-cut", 0.2)
	pending := oddsForecast(t, e, e.User, "fed-cut", 0.2)
	if _, err := e.DB.Exec(ctx, "update forecasts set archived_from_status = status, status = 'archived' where id = $1", archived); err != nil {
		t.Fatal(err)
	}
	if _, err := e.DB.Exec(ctx, "update forecasts set status = 'running', completed_at = null where id = $1", pending); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{foreign, archived, pending} {
		w := e.Do("POST", "/api/alerts", map[string]any{"trigger": "odds", "forecastId": id, "op": "above", "price": 0.5})
		if w.Code != http.StatusBadRequest || apptest.ErrorCode(t, w) != "bad_request" {
			t.Errorf("foreign or incomplete forecast accepted: %d %s", w.Code, w.Body)
		}
	}
	own := oddsForecast(t, e, e.User, "fed-cut", 0.2)
	a := directCreate(t, e, e.Cookie, map[string]any{"trigger": "odds", "forecastId": own, "op": "above", "price": 0.5})
	for _, key := range []string{"marketId", "marketSlug", "linkedMarket"} {
		if _, present := a[key]; !present {
			t.Errorf("ordinary forecast alert omits the nullable %s contract field", key)
		}
	}
	if a["forecastId"] != own || a["marketId"] != nil || a["marketSlug"] != nil || a["linkedMarket"] != nil || directObject(t, a["market"])["yesPrice"] != 0.3 {
		t.Fatalf("ordinary owned forecast alert remains forecast-targeted: %#v", a)
	}
}

func TestCheckDirectOddsFairQueueDoesNotStarveNewMarketIDs(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	g := newDirectGamma(t, e)
	advance := directClock(t)
	assertDirectFree(t, e)
	g.mu.Lock()
	g.others = map[string]map[string]any{}
	for i := range 21 {
		m := maps.Clone(g.market)
		id, slug := fmt.Sprint(9000+i), fmt.Sprintf("direct-market-%02d", i)
		m["id"], m["slug"], m["conditionId"] = id, slug, fmt.Sprintf("0x%064x", 9000+i)
		g.others[id] = m
	}
	g.mu.Unlock()
	var ids []string
	for i := range 21 {
		user := storetest.User(t, e.DB)
		b := directBody("above", 0.9)
		b["marketSlug"] = fmt.Sprintf("direct-market-%02d", i)
		a := directCreate(t, e, e.Session(user), b)
		ids = append(ids, a["id"].(string))
	}
	g.reads()
	for round := range 2 {
		if err := Check(ctx, e.Deps); err != nil {
			t.Fatal(err)
		}
		want := 20
		if round == 1 {
			want = 1
		}
		if reads := g.reads(); reads != want {
			t.Fatalf("fair direct check %d read %d IDs, want %d", round, reads, want)
		}
	}
	// Make every leaf cross. The earlier 20 have waited longest and consume
	// the first batch; the remaining ID is still visited on the next check.
	g.mu.Lock()
	for _, m := range g.others {
		m["outcomePrices"] = `["0.95","0.05"]`
	}
	g.mu.Unlock()
	advance(time.Minute)
	for range 2 {
		if err := Check(ctx, e.Deps); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range ids {
		if a := load(t, e, id); a.Status != "fired" || a.FiredPrice == nil || *a.FiredPrice != 0.95 {
			t.Errorf("direct leaf starved by other market IDs: %+v", a)
		}
	}
}

package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// The mounted HTTP contract, fresh original-rule prefill, private-free list,
// free direct leaf alert and all rejection paths run through the actual server.
func TestIntegrationPredictionBoardDirectOdds(t *testing.T) {
	const id, slug, parent = "5170727", "bitcoin-reaches-150000-october", "bitcoin-october"
	const q = "Will Bitcoin reach $150,000 in October?"
	rules := strings.TrimSpace(strings.Repeat("Use any Binance BTCUSDT 1m High at least $150,000 within the complete October America/New_York calendar month. ", 30))
	var mu sync.Mutex
	changed, down := false, false
	s := boot(t, options{gamma: func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		change, unavailable := changed, down
		mu.Unlock()
		if unavailable {
			http.Error(w, "unavailable", 503)
			return
		}
		criteria := rules
		if change {
			criteria = "Changed midnight UTC daily-close rule."
		}
		m := map[string]any{"id": id, "conditionId": "0x" + strings.Repeat("1", 64), "slug": slug, "question": q, "description": criteria, "resolutionSource": nil, "endDate": "2099-11-01T04:00:00Z", "active": true, "closed": false, "acceptingOrders": true, "outcomes": `["Yes","No"]`, "outcomePrices": `["0.3","0.7"]`, "volumeNum": 50000, "events": []map[string]any{{"slug": parent, "title": "Bitcoin in October", "active": true, "closed": false}}}
		switch r.URL.Path {
		case "/events":
			json.NewEncoder(w).Encode([]map[string]any{{"slug": parent, "title": "Bitcoin in October", "active": true, "closed": false, "endDate": "2099-11-01T04:00:00Z", "markets": []map[string]any{m}}})
		case "/markets":
			if r.URL.Query().Get("id") == id || r.URL.Query().Get("slug") == slug {
				json.NewEncoder(w).Encode([]map[string]any{m})
			} else {
				json.NewEncoder(w).Encode([]any{})
			}
		default:
			t.Errorf("unexpected Gamma endpoint %s", r.URL)
			http.Error(w, "unexpected", 400)
		}
	}})
	c := s.signIn(t)
	board := c.call("GET", "/api/predictions/markets?category=crypto", nil, 200)
	shape(t, board, "", "events asOf stale")
	if len(list(t, board, "events")) != 1 || !isNull(t, board, "events.0.markets.0.estimate") {
		t.Fatalf("touch board: %v", board)
	}
	if _, ok := at(t, board, "events.0.markets.0").(map[string]any)["linkedMarket"]; ok {
		t.Fatal("listing amplified full rules")
	}
	d := c.call("GET", "/api/predictions/markets/"+id+"/draft", nil, 200)
	if str(t, d, "draft.question") != q || str(t, d, "draft.resolutionCriteria") != rules || !isNull(t, d, "draft.priceRule") || str(t, d, "draft.linkedMarket.marketSlug") != slug || str(t, d, "draft.linkedMarket.marketId") != id || !isNull(t, d, "draft.linkedMarket.resolutionSource") {
		t.Fatalf("original touch predicate: %v", d)
	}
	proposal := at(t, d, "draft").(map[string]any)
	proposal["resolutionCriteria"] = "Edited arbitrary rule."
	c.fail("POST", "/api/forecasts", proposal, 400, "bad_request")
	proposal["resolutionCriteria"] = rules
	mu.Lock()
	changed = true
	mu.Unlock()
	c.fail("POST", "/api/forecasts", proposal, 409, "conflict")
	mu.Lock()
	changed = false
	mu.Unlock()
	alert := map[string]any{"trigger": "odds", "marketSlug": slug, "marketId": id, "op": "above", "price": .5}
	created := c.call("POST", "/api/alerts", alert, 201)
	shape(t, created, "alert", alertKeys)
	if !isNull(t, created, "alert.forecastId") || str(t, created, "alert.linkedMarket.description") != rules || num(t, created, "alert.market.yesPrice") != .3 {
		t.Fatalf("direct snapshot: %v", created)
	}
	alert["forecastId"] = someID
	c.fail("POST", "/api/alerts", alert, 400, "bad_request")
	delete(alert, "forecastId")
	alert["marketSlug"] = parent
	c.fail("POST", "/api/alerts", alert, 400, "bad_request")
	alert["marketSlug"] = slug
	mu.Lock()
	down = true
	mu.Unlock()
	c.fail("POST", "/api/alerts", alert, 502, "upstream")
	c.fail("GET", "/api/predictions/markets/"+id+"/draft", nil, 502, "upstream")
	if s.count(t, "select count(*) from forecasts where user_id=$1", c.id) != 0 || s.count(t, "select count(*) from market_positions where user_id=$1", c.id) != 0 || s.count(t, "select count(*) from trades where user_id=$1", c.id) != 0 || num(t, c.call("GET", "/api/me", nil, 200), "credits.used") != 0 {
		t.Fatal("free/rejected actions spent or traded")
	}
	for _, r := range s.bedrock.Requests() {
		if !strings.HasPrefix(r.System, "You are the markets editor") && !strings.HasPrefix(r.System, "You write one-line descriptions for a momentum screen") {
			t.Fatalf("free discovery invoked model: %.80s", r.System)
		}
	}
}

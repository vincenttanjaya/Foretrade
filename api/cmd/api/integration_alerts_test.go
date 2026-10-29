package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/alerts"
	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
)

const (
	alertKeys        = "prompt thesis notifyWhen quotaStreak id symbol trigger op price every at weekday nextRunAt lastRun action notional stopLoss takeProfit note source proposalId status tradeId error firedPrice createdAt firedAt forecastId market marketId marketSlug linkedMarket drawdown currency"
	notificationKeys = "id kind title body symbol link at"
)

// Automations: an alert set over HTTP fires on the next check (run here
// directly instead of waiting for the server's 15 s tick; the claim makes the
// two safe together), a buy alert's fill links back to it, the bell lists
// both and /seen clears the badge, DELETE cancels only active alerts.
func TestIntegrationAlerts(t *testing.T) {
	s := boot(t, options{})
	c := s.signIn(t)
	check := func(pair string, price float64) {
		t.Helper()
		s.binance.SetPrice(pair, price)
		mkt := market.NewClient(s.binance.URL) // a cold cache sees the new price
		if err := alerts.Check(context.Background(), &app.Deps{DB: s.db, Market: mkt, Paper: paper.NewEngine(s.db, mkt)}); err != nil {
			t.Fatal(err)
		}
	}

	up := c.call("POST", "/api/alerts", map[string]any{"symbol": "SOL", "op": "above", "price": 201, "note": "Breakout"}, 201)
	shape(t, up, "", "alert")
	shape(t, up, "alert", alertKeys)
	isTime(t, up, "alert.createdAt")
	if str(t, up, "alert.status") != "active" || str(t, up, "alert.action") != "notify" || !isNull(t, up, "alert.firedAt") {
		t.Fatalf("created %v", up)
	}
	buy := str(t, c.call("POST", "/api/alerts", map[string]any{"symbol": "SOL", "op": "below", "price": 199, "action": "buy", "notional": 1000,
		"stopLoss": 190, "takeProfit": 215, "source": "ticket"}, 201), "alert.id")
	c.fail("POST", "/api/alerts", map[string]any{"symbol": "SOL", "op": "below", "price": 201}, 400, "bad_request")

	check("SOLUSDT", 201)
	l := c.call("GET", "/api/alerts", nil, 200)
	shapeAll(t, l, "alerts", alertKeys)
	fired := at(t, l, "alerts.1") // active first
	if str(t, l, "alerts.0.id") != buy || str(t, fired, "status") != "fired" || num(t, fired, "firedPrice") != 201 {
		t.Fatalf("after the check: %v", l)
	}
	isTime(t, fired, "firedAt")
	n := c.call("GET", "/api/notifications", nil, 200)
	shape(t, n, "", "items unread")
	shapeAll(t, n, "items", notificationKeys)
	if num(t, n, "unread") != 1 || str(t, n, "items.0.kind") != "alert" || str(t, n, "items.0.title") != "SOL rose above $201.00" ||
		str(t, n, "items.0.body") != "Now $201.00 · Breakout" || str(t, n, "items.0.link") != "/automations" {
		t.Fatalf("bell: %v", n)
	}

	check("SOLUSDT", 198.9)
	newest := at(t, c.call("GET", "/api/portfolio/trades?limit=1", nil, 200), "trades.0")
	if str(t, newest, "source") != "alert" || str(t, newest, "ref.kind") != "alert" || str(t, newest, "ref.id") != buy ||
		str(t, newest, "ref.label") != "Alert: SOL below $199.00" || str(t, newest, "note") != "Alert: SOL below $199.00" {
		t.Fatalf("alert fill: %v", newest)
	}
	pos := at(t, c.call("GET", "/api/portfolio", nil, 200), "portfolio.positions.0")
	if num(t, pos, "stopLoss") != 190 || num(t, pos, "takeProfit") != 215 {
		t.Fatalf("position %v", pos)
	}
	if s.count(t, "select count(*) from alerts where id = $1 and status = 'fired' and trade_id = $2", buy, str(t, newest, "id")) != 1 {
		t.Fatal("trade_id not stored")
	}
	n = c.call("GET", "/api/notifications", nil, 200)
	if num(t, n, "unread") != 2 || str(t, n, "items.0.kind") != "alert_order" || str(t, n, "items.0.title") != "Alert order filled · SOL" {
		t.Fatalf("bell after the fill: %v", n)
	}
	c.call("POST", "/api/notifications/seen", "{}", 204)
	if n := c.call("GET", "/api/notifications", nil, 200); num(t, n, "unread") != 0 || len(list(t, n, "items")) != 2 {
		t.Fatalf("after seen: %v", n)
	}

	id := str(t, c.call("POST", "/api/alerts", map[string]any{"symbol": "BTC", "op": "above", "price": 120000}, 201), "alert.id")
	c.call("DELETE", "/api/alerts/"+id, "{}", 204)
	c.fail("DELETE", "/api/alerts/"+id, "{}", 409, "conflict")
	c.fail("DELETE", "/api/alerts/"+buy, "{}", 409, "conflict")
	c.fail("DELETE", "/api/alerts/"+someID, "{}", 404, "not_found")
	s.signIn(t).fail("DELETE", "/api/alerts/"+str(t, up, "alert.id"), "{}", 404, "not_found")
}

// Prediction-market odds triggers: a binary report matched to Polymarket (a
// path-aware Gamma fake) takes an odds alert over HTTP, checked against the
// live price, and one from the agent's card in a forecast handoff; when the
// price crosses, a check (run here directly; the server's own 15 s loop may
// win the claim) fires the crossed one once, the bell links it to the report,
// and the report's market is fresh. The alerts cost 0 credits.
func TestIntegrationOddsAlerts(t *testing.T) {
	const slug = "fed-decision-in-december-2099"
	const mq = "Fed decreases interest rates after the December 2099 meeting?"
	const ask = "Tell me if the market goes above 80%"
	var mu sync.Mutex
	yes, forecastID, briefed := "0.6", "", false
	s := boot(t, options{
		gamma: func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			event := fmt.Sprintf(`{"slug":%q,"markets":[{"question":%q,"outcomes":"[\"Yes\", \"No\"]","outcomePrices":"[\"%s\", \"0.4\"]",
				"endDate":"2099-12-31T12:00:00Z","active":true,"closed":false,"volumeNum":250000}]}`, slug, mq, yes)
			mu.Unlock()
			switch {
			case r.URL.Path == "/public-search":
				fmt.Fprintf(w, `{"events":[%s]}`, event)
			case r.URL.Path == "/events" && r.URL.Query().Get("slug") == slug:
				fmt.Fprintf(w, `[%s]`, event)
			default:
				http.Error(w, "unexpected "+r.URL.String(), http.StatusBadRequest)
			}
		},
		model: func(r llmtest.Request) llmtest.Reply {
			switch {
			case strings.HasPrefix(r.System, "You are the trading desk analyst") && r.Prompt == ask:
				mu.Lock()
				defer mu.Unlock()
				briefed = strings.Contains(r.System, "refer to them as below, never above") &&
					strings.Contains(r.System, fmt.Sprintf(`set_odds_alert with forecast_id %q, op "above", price_pct 70`, forecastID))
				return llmtest.Reply{Text: "The desk is at 70% against the market's 60%. ", Tools: []llmtest.ToolUse{{ID: "o1", Name: "set_odds_alert",
					Input: fmt.Sprintf(`{"forecast_id":%q,"op":"above","price_pct":80,"note":"Above the desk"}`, forecastID)}}}
			case strings.HasPrefix(r.System, "You are the trading desk analyst") && r.AfterTools: // the card came back
				return llmtest.Reply{Text: "Set it on the card below. **View:** the desk leans Yes."}
			case strings.HasPrefix(r.System, queryWriter):
				return noMarketQueries
			case strings.HasPrefix(r.System, "Decide whether one of these prediction markets"):
				return llmtest.Reply{Text: `{"index": 0, "match": "exact"}`}
			case strings.HasPrefix(r.System, "You are the aggregator"):
				return llmtest.Reply{Text: `{"probabilities":[{"outcome":"Yes","p":0.7},{"outcome":"No","p":0.3}],"summary":"We lean Yes.","confidence":"medium"}`}
			case strings.HasPrefix(r.System, "You are the"):
				return llmtest.Reply{Text: `{"probabilities":[{"outcome":"Yes","p":0.7},{"outcome":"No","p":0.3}],"reasoning":"r","keyFactors":["k"],"confidence":"medium"}`}
			}
			t.Errorf("unexpected model call: %.60q", r.System)
			return llmtest.Reply{Status: 400}
		},
	})
	setYes := func(v string) { mu.Lock(); yes = v; mu.Unlock() }
	c := s.signIn(t)
	id := str(t, c.call("POST", "/api/forecasts", map[string]any{"shape": "binary", "question": "Will the Fed cut rates at its December 2099 meeting?",
		"outcomes": []string{"Yes", "No"}, "resolutionDate": "2099-12-31", "category": "macro"}, 201), "forecast.id")
	mu.Lock()
	forecastID = id
	mu.Unlock()
	var f any
	eventually(t, 30*time.Second, "the forecast run", func() bool {
		f = c.call("GET", "/api/forecasts/"+id, nil, 200)
		return str(t, f, "forecast.status") != "running"
	})
	if num(t, f, "forecast.result.market.yesPrice") != 0.6 {
		t.Fatalf("matched: %v", at(t, f, "forecast.result"))
	}
	used := num(t, c.call("GET", "/api/me", nil, 200), "credits.used")

	odds := func(extra map[string]any) map[string]any {
		b := map[string]any{"trigger": "odds", "forecastId": id, "op": "above", "price": 0.7}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}
	for msg, body := range map[string]map[string]any{
		"The market is already at 60%. Pick a level above it.": odds(map[string]any{"price": 0.55}),
		"Odds alerts notify only.":                             odds(map[string]any{"action": "buy", "notional": 100}),
		"Pick a level between 1% and 99%.":                     odds(map[string]any{"price": 70}),
		"Unknown forecast.":                                    odds(map[string]any{"forecastId": someID}),
	} {
		if got := c.call("POST", "/api/alerts", body, 400); str(t, got, "error.code") != "bad_request" || str(t, got, "error.message") != msg {
			t.Errorf("%v: %v, want %q", body, got, msg)
		}
	}
	up := c.call("POST", "/api/alerts", odds(map[string]any{"note": "Desk number"}), 201)
	shape(t, up, "alert", alertKeys)
	shape(t, up, "alert.market", "question url outcome yesPrice asOf")
	if str(t, up, "alert.trigger") != "odds" || str(t, up, "alert.symbol") != "" || str(t, up, "alert.forecastId") != id || num(t, up, "alert.price") != 0.7 ||
		str(t, up, "alert.market.question") != mq || num(t, up, "alert.market.yesPrice") != 0.6 || !isNull(t, up, "alert.market.outcome") ||
		str(t, up, "alert.market.url") != "https://polymarket.com/event/"+slug {
		t.Fatalf("created %v", up)
	}
	upID := str(t, up, "alert.id")

	// The agent, in a chat opened from the report: an odds card, set by the user.
	ev := c.stream(map[string]any{"message": ask, "context": map[string]any{"forecastId": id}})
	var card any
	for _, e := range ev {
		if e.name == "card" && str(t, e.data, "type") == "price_alert" {
			card = at(t, e.data, "data")
		}
	}
	mu.Lock()
	ok := briefed
	mu.Unlock()
	if last := ev[len(ev)-1]; card == nil || last.name != "done" || !ok {
		t.Fatalf("stream: card %v, last %v, briefed %t", card, last, briefed)
	}
	shape(t, card, "", "proposalId symbol trigger forecastId market op price markPrice distancePct every at weekday action notional stopLoss takeProfit note")
	if str(t, card, "trigger") != "odds" || str(t, card, "forecastId") != id || str(t, card, "market.question") != mq || num(t, card, "price") != 0.8 ||
		num(t, card, "markPrice") != 0.6 || str(t, card, "action") != "notify" {
		t.Fatalf("card %v", card)
	}
	set := map[string]any{"trigger": "odds", "forecastId": id, "op": str(t, card, "op"), "price": num(t, card, "price"), "action": "notify",
		"note": str(t, card, "note"), "source": "agent", "proposalId": str(t, card, "proposalId")}
	agentID := str(t, c.call("POST", "/api/alerts", set, 201), "alert.id")
	c.fail("POST", "/api/alerts", set, 409, "conflict")
	used += 2 // the chat message

	// The market moves to 72%: the 70% trigger fires once, the 80% one waits.
	setYes("0.72")
	deps := &app.Deps{DB: s.db, Market: market.NewClient(s.binance.URL), PolymarketBase: os.Getenv("POLYMARKET_BASE")}
	eventually(t, 75*time.Second, "the odds alert", func() bool { // the server's loop may have re-priced at 60% just before: a minute's throttle
		if err := alerts.Check(context.Background(), deps); err != nil {
			t.Fatal(err)
		}
		return s.count(t, "select count(*) from alerts where id = $1 and status = 'fired'", upID) == 1
	})
	l := c.call("GET", "/api/alerts", nil, 200)
	shapeAll(t, l, "alerts", alertKeys)
	if str(t, l, "alerts.0.id") != agentID || str(t, l, "alerts.0.status") != "active" || num(t, l, "alerts.0.market.yesPrice") != 0.72 ||
		str(t, l, "alerts.1.status") != "fired" || num(t, l, "alerts.1.firedPrice") != 0.72 {
		t.Fatalf("after the check: %v", l)
	}
	n := c.call("GET", "/api/notifications", nil, 200)
	shapeAll(t, n, "items", notificationKeys)
	if str(t, n, "items.0.kind") != "odds_alert" || str(t, n, "items.0.id") != "odds_alert:"+upID ||
		str(t, n, "items.0.title") != `Polymarket moved: "`+mq+`" rose above 70%` || str(t, n, "items.0.body") != "Now 72% · desk 70% · Desk number" ||
		str(t, n, "items.0.link") != "/forecasts/"+id || !isNull(t, n, "items.0.symbol") {
		t.Fatalf("bell: %v", n)
	}
	r := c.call("GET", "/api/forecasts/"+id, nil, 200)
	if num(t, r, "forecast.result.market.yesPrice") != 0.72 || !isTime(t, r, "forecast.result.market.asOf").After(isTime(t, f, "forecast.result.market.asOf")) {
		t.Fatalf("the report's market: %v", at(t, r, "forecast.result.market"))
	}
	if got := num(t, c.call("GET", "/api/me", nil, 200), "credits.used"); got != used {
		t.Fatalf("credits used %v, want %v", got, used)
	}
}

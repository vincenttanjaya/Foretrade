package main

import (
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
)

const marketPositionKeys = "id forecastId forecastQuestion question url outcome side price contracts cost maxPayout status payout pnl markPrice createdAt settledAt"

// Prediction Market Copilot on paper: a binary report matched to Polymarket
// re-prices on demand, takes YES/NO stakes from cash (equity unchanged), is
// settled by its resolution (realized P&L, the "prediction" source) and a
// reset deletes the positions. None of it spends credits.
func TestIntegrationMarketPositions(t *testing.T) {
	const slug = "fed-decision-in-december-2099"
	const mq = "Fed decreases interest rates after the December 2099 meeting?"
	var mu sync.Mutex
	yes := "0.6"
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
	done := func(id string) any {
		var f any
		eventually(t, 30*time.Second, "the forecast run", func() bool {
			f = c.call("GET", "/api/forecasts/"+id, nil, 200)
			return str(t, f, "forecast.status") != "running"
		})
		return f
	}
	id := str(t, c.call("POST", "/api/forecasts", map[string]any{"shape": "binary", "question": "Will the Fed cut rates at its December 2099 meeting?",
		"outcomes": []string{"Yes", "No"}, "resolutionDate": "2099-12-31", "category": "macro"}, 201), "forecast.id")
	m := "forecast.result.market."
	f := done(id)
	if str(t, f, "forecast.status") != "completed" || num(t, f, m+"yesPrice") != 0.6 || num(t, f, m+"edgePp") != 10 || str(t, f, m+"url") != "https://polymarket.com/event/"+slug {
		t.Fatalf("matched: %v", at(t, f, "forecast.result"))
	}
	used := num(t, c.call("GET", "/api/me", nil, 200), "credits.used")

	// Fresh odds: the price moved, POST /market reads it from /events.
	setYes("0.65")
	r := c.call("POST", "/api/forecasts/"+id+"/market", map[string]any{}, 200)
	shape(t, r, "forecast", forecastKeys)
	shape(t, r, "forecast.result", resultKeys)
	if num(t, r, m+"yesPrice") != 0.65 || num(t, r, m+"edgePp") != 5 || str(t, r, m+"signal") != "yes" || !isTime(t, r, m+"asOf").After(isTime(t, f, m+"asOf")) {
		t.Fatalf("refreshed: %v", at(t, r, "forecast.result.market"))
	}

	// Buy Yes $100 at 66¢: cash −100, staked 100, equity unchanged.
	start := c.call("GET", "/api/portfolio", nil, 200)
	shape(t, start, "portfolio", portfolioKeys)
	bought := c.call("POST", "/api/forecasts/"+id+"/market-position", map[string]any{"side": "yes", "stake": 100}, 201)
	shape(t, bought, "position", marketPositionKeys)
	shape(t, bought, "portfolio", portfolioKeys)
	p := "position."
	if num(t, bought, p+"price") != 0.66 || !near(num(t, bought, p+"contracts"), 100/0.66) || num(t, bought, p+"cost") != 100 || str(t, bought, p+"outcome") != "Yes" ||
		str(t, bought, p+"status") != "open" || !isNull(t, bought, p+"payout") || !isNull(t, bought, p+"pnl") || num(t, bought, p+"markPrice") != 0.65 ||
		str(t, bought, p+"question") != mq || str(t, bought, p+"forecastId") != id {
		t.Fatalf("bought: %v", bought)
	}
	if !near(num(t, bought, "portfolio.cash"), num(t, start, "portfolio.cash")-100) || num(t, bought, "portfolio.predictionStakes") != 100 ||
		!near(num(t, bought, "portfolio.equity"), num(t, start, "portfolio.equity")) {
		t.Fatalf("portfolio after the stake: %v, before %v", at(t, bought, "portfolio"), at(t, start, "portfolio"))
	}
	if no := c.call("POST", "/api/forecasts/"+id+"/market-position", map[string]any{"side": "no", "stake": 50}, 201); num(t, no, p+"price") != 0.36 {
		t.Fatalf("bought no: %v", no)
	}
	c.fail("POST", "/api/forecasts/"+id+"/market-position", map[string]any{"side": "yes", "stake": 5}, 400, "bad_request")

	// An update supersedes the run: its positions carry over to the lineage, new ones go on the newest run.
	child := str(t, c.call("POST", "/api/forecasts/"+id+"/update", map[string]any{}, 201), "forecast.id")
	done(child)
	used += 5 // the update run; nothing else here charges
	c.fail("POST", "/api/forecasts/"+id+"/market-position", map[string]any{"side": "yes", "stake": 50}, 409, "conflict")
	if l := list(t, c.call("GET", "/api/market-positions?forecastId="+child, nil, 200), "positions"); len(l) != 2 {
		t.Fatalf("the child's lineage: %v", l)
	}

	// Resolve Yes on the newest run: the Yes stake pays $1 a contract, the No stake loses.
	before := c.call("GET", "/api/portfolio", nil, 200)
	c.call("POST", "/api/forecasts/"+child+"/resolve", map[string]any{"outcome": "Yes"}, 200)
	after := c.call("GET", "/api/portfolio", nil, 200)
	payout := 100 / 0.66
	if !near(num(t, after, "portfolio.cash"), num(t, before, "portfolio.cash")+payout) || num(t, after, "portfolio.predictionStakes") != 0 ||
		math.Abs(num(t, after, "portfolio.realizedPnl")-(num(t, before, "portfolio.realizedPnl")+payout-150)) > 1e-6 {
		t.Fatalf("settled: %v, before %v", at(t, after, "portfolio"), at(t, before, "portfolio"))
	}
	positions := c.call("GET", "/api/market-positions", nil, 200)
	shapeAll(t, positions, "positions", marketPositionKeys)
	l := list(t, positions, "positions")
	if len(l) != 2 || str(t, l[0], "status") != "lost" || num(t, l[0], "pnl") != -50 || str(t, l[1], "status") != "won" ||
		math.Abs(num(t, l[1], "payout")-payout) > 1e-9 || isNull(t, l[1], "settledAt") {
		t.Fatalf("positions: %v", l)
	}
	c.fail("POST", "/api/forecasts/"+child+"/market-position", map[string]any{"side": "yes", "stake": 50}, 409, "conflict")
	c.fail("POST", "/api/forecasts/"+child+"/resolve", map[string]any{"outcome": nil}, 409, "conflict")

	attr := c.call("GET", "/api/portfolio/attribution", nil, 200)
	sum, pred := 0.0, any(nil)
	for _, src := range list(t, attr, "sources") {
		sum += num(t, src, "totalPnl")
		if str(t, src, "source") == "prediction" {
			pred = src
		}
	}
	total := num(t, after, "portfolio.totalPnl")
	if pred == nil || num(t, pred, "trades") != 2 || math.Abs(num(t, pred, "realizedPnl")-(payout-150)) > 1e-6 || math.Abs(sum-total) > 0.01 {
		t.Fatalf("attribution %v, total %v", attr, total)
	}
	pts := list(t, c.call("GET", "/api/portfolio/equity?range=1d", nil, 200), "points")
	if math.Abs(num(t, pts[len(pts)-1], "v")-num(t, after, "portfolio.equity")) > 0.01 {
		t.Fatalf("the curve ends at the equity: %v vs %v", pts[len(pts)-1], at(t, after, "portfolio.equity"))
	}
	if got := num(t, c.call("GET", "/api/me", nil, 200), "credits.used"); got != used {
		t.Fatalf("credits used %v, want %v", got, used)
	}

	// A reset deletes the positions.
	reset := c.call("POST", "/api/portfolio/reset", map[string]any{}, 200)
	if num(t, reset, "portfolio.predictionStakes") != 0 || len(list(t, c.call("GET", "/api/market-positions", nil, 200), "positions")) != 0 ||
		s.count(t, "select count(*) from market_positions") != 0 {
		t.Fatalf("reset: %v", reset)
	}
}

package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
)

// Desk vs market by strike (round 6): an ordered price forecast whose buckets
// straddle Polymarket's daily "Bitcoin above ___" ladder comes back with one
// priced row per strike inside the buckets' edges, ascending; POST /market
// re-prices them from the event (0 credits); a paper stake is refused.
func TestIntegrationForecastLadder(t *testing.T) {
	const slug = "bitcoin-above-on-december-31-2099"
	var mu sync.Mutex
	var requests []string
	shift := 0.0 // added to every Yes price: moves the market for the re-price
	s := boot(t, options{
		gamma: func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			requests = append(requests, r.URL.Path+" "+r.URL.Query().Get("slug")+r.URL.Query().Get("q"))
			if r.URL.Path != "/events" || r.URL.Query().Get("slug") != slug {
				io.WriteString(w, map[bool]string{true: `[]`, false: `{"events":[]}`}[r.URL.Path == "/events"])
				return
			}
			var ms []string
			for i, k := range []int{100, 110, 115, 120, 125, 130, 140} { // $100k and $140k sit in the open-ended tails
				ms = append(ms, fmt.Sprintf(`{"question":"Will the price of Bitcoin be above $%d,000 on December 31?","outcomes":"[\"Yes\", \"No\"]",
					"outcomePrices":"[\"%g\", \"%g\"]","endDate":"2099-12-31T17:00:00Z","active":true,"closed":false,"volumeNum":%d}`,
					k, 0.95-0.15*float64(i)+shift, 0.05+0.15*float64(i)-shift, 5000*(i+1)))
			}
			fmt.Fprintf(w, `[{"slug":%q,"title":"Bitcoin above ___ on December 31?","markets":[%s]}]`, slug, strings.Join(ms, ","))
		},
		model: func(r llmtest.Request) llmtest.Reply {
			probs := `[{"outcome":"< $110,000","p":0.2},{"outcome":"$110,000 – $120,000","p":0.3},{"outcome":"$120,000 – $130,000","p":0.3},{"outcome":"≥ $130,000","p":0.2}]`
			switch {
			case strings.HasPrefix(r.System, "You are the aggregator"):
				return llmtest.Reply{Text: `{"probabilities":` + probs + `,"summary":"We centre on $120k.","confidence":"medium"}`}
			case strings.HasPrefix(r.System, "You are the"):
				if strings.Contains(r.Prompt, "M1 ·") {
					t.Errorf("%.40s saw the ladder as M1", r.System)
				}
				return llmtest.Reply{Text: `{"probabilities":` + probs + `,"reasoning":"r","keyFactors":["k"],"confidence":"medium"}`}
			}
			t.Errorf("unexpected model call: %.60q", r.System)
			return llmtest.Reply{Status: 400}
		},
	})
	c := s.signIn(t)
	id := str(t, c.call("POST", "/api/forecasts", map[string]any{"shape": "ordered", "question": "Where will the Binance BTC/USDT daily close be on 2099-12-31?",
		"outcomes": []string{"< $110,000", "$110,000 – $120,000", "$120,000 – $130,000", "≥ $130,000"}, "resolutionDate": "2099-12-31", "category": "crypto"}, 201), "forecast.id")
	var f any
	eventually(t, 30*time.Second, "the forecast run", func() bool {
		f = c.call("GET", "/api/forecasts/"+id, nil, 200)
		return str(t, f, "forecast.status") != "running"
	})
	if str(t, f, "forecast.status") != "completed" || at(t, f, "forecast.result.market") != nil || at(t, f, "forecast.progress.market.matched") != true {
		t.Fatalf("run: %v", at(t, f, "forecast"))
	}
	// Desk P(above): $110k 0.8, $115k 0.65, $120k 0.5, $125k 0.35, $130k 0.2; market 0.8, 0.65, 0.5, 0.35, 0.2 → edge 0.
	rows := list(t, f, "forecast.result.ladder")
	if len(rows) != 5 {
		t.Fatalf("ladder rows: %v", rows)
	}
	shapeAll(t, f, "forecast.result.ladder", marketKeys+" strike")
	for i, k := range []float64{110000, 115000, 120000, 125000, 130000} {
		p := fmt.Sprintf("forecast.result.ladder.%d.", i)
		if num(t, f, p+"strike") != k || num(t, f, p+"edgePp") != 0 || str(t, f, p+"signal") != "wait" || str(t, f, p+"match") != "exact" ||
			str(t, f, p+"url") != "https://polymarket.com/event/"+slug {
			t.Fatalf("row %d: %v", i, rows[i])
		}
	}
	mu.Lock()
	if strings.Join(requests, " | ") != "/events "+slug {
		t.Errorf("Gamma requests %q", requests)
	}
	shift = -0.1 // the crowd marks every strike down 10 points
	mu.Unlock()

	f = c.call("POST", "/api/forecasts/"+id+"/market", map[string]any{}, 200)
	if len(list(t, f, "forecast.result.ladder")) != 5 || num(t, f, "forecast.result.ladder.2.yesPrice") != 0.4 || num(t, f, "forecast.result.ladder.2.edgePp") != 10 ||
		str(t, f, "forecast.result.ladder.2.signal") != "yes" {
		t.Fatalf("re-priced: %v", at(t, f, "forecast.result.ladder"))
	}
	isTime(t, f, "forecast.result.ladder.0.asOf")
	c.fail("POST", "/api/forecasts/"+id+"/market-position", map[string]any{"side": "yes", "stake": 100}, 409, "conflict")
}

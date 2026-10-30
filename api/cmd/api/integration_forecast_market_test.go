package main

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
)

const marketKeys = "source question url yesPrice volumeUsd endDate match edgePp evYes evNo signal thin asOf"

// Desk vs market: a price-rule forecast finds its strike in the Polymarket
// ladder (POLYMARKET_BASE: the dated event by its slug) and comes
// back with the edge, EV and lean; the user's context is stored and shown to
// the panel as S0.
func TestIntegrationForecastMarket(t *testing.T) {
	var mu sync.Mutex
	var queries []string
	s := boot(t, options{
		gamma: func(w http.ResponseWriter, r *http.Request) { // /events?slug= answers a JSON array, /public-search {"events": [...]}
			mu.Lock()
			queries = append(queries, r.URL.Path+" "+r.URL.Query().Get("slug")+r.URL.Query().Get("q"))
			mu.Unlock()
			ladder := ""
			for i, strike := range []string{"115,000", "120,000", "125,000"} {
				ladder += fmt.Sprintf(`%s{"question":"Will the price of Bitcoin be above $%s on December 31?","outcomes":"[\"Yes\", \"No\"]",
					"outcomePrices":"[\"0.%d\", \"0.%d\"]","endDate":"2099-12-31T17:00:00Z","active":true,"closed":false,"volumeNum":%d}`,
					map[bool]string{true: ",", false: ""}[i > 0], strike, 6-2*i, 4+2*i, 1000*(i+1))
			}
			event := fmt.Sprintf(`{"slug":"bitcoin-above-on-december-31-2099","title":"Bitcoin above ___ on December 31?","markets":[%s]}`, ladder)
			if r.URL.Path == "/events" {
				if r.URL.Query().Get("slug") != "bitcoin-above-on-december-31-2099" {
					event = ""
				}
				fmt.Fprintf(w, `[%s]`, event)
				return
			}
			fmt.Fprintf(w, `{"events":[%s]}`, event)
		},
		model: func(r llmtest.Request) llmtest.Reply {
			opinion := `{"probabilities":[{"outcome":"Yes","p":0.5},{"outcome":"No","p":0.5}],"reasoning":"r","keyFactors":["k"],"confidence":"medium"}`
			switch {
			case strings.HasPrefix(r.System, "You are the aggregator"):
				return llmtest.Reply{Text: `{"probabilities":[{"outcome":"Yes","p":0.46},{"outcome":"No","p":0.54}],"summary":"We lean No.","drivers":[],
					"evidence":[],"flags":[],"whatWouldChange":[],"confidence":"medium"}`}
			case strings.HasPrefix(r.System, "You are the"):
				if !strings.Contains(r.Prompt, "S0 · User context (supplied by the user, unverified)") || !strings.Contains(r.Prompt, "<untrusted_web_content>\nOTC desks report heavy demand.") {
					t.Errorf("%.40s prompt lacks S0: %.300s", r.System, r.Prompt)
				}
				return llmtest.Reply{Text: opinion}
			}
			t.Errorf("unexpected model call: %.60q", r.System)
			return llmtest.Reply{Status: 400}
		},
	})
	c := s.signIn(t)
	draft := map[string]any{"shape": "binary", "question": "Will the Binance BTC/USDT daily close on 2099-12-31 be above $120,000?",
		"outcomes": []string{"Yes", "No"}, "resolutionDate": "2099-12-31", "category": "crypto",
		"priceRule": map[string]any{"symbol": "BTC", "op": "above", "threshold": 120000, "mode": "on_date"}}
	draft["context"] = strings.Repeat("x", 4001)
	c.fail("POST", "/api/forecasts", draft, 400, "bad_request")
	draft["context"] = " OTC desks report heavy demand. "
	created := c.call("POST", "/api/forecasts", draft, 201)
	id := str(t, created, "forecast.id")
	if str(t, created, "forecast.context") != "OTC desks report heavy demand." ||
		s.count(t, "select count(*) from forecasts where id = $1 and context = 'OTC desks report heavy demand.'", id) != 1 {
		t.Fatalf("context not stored: %v", created)
	}

	var f any
	eventually(t, 30*time.Second, "the forecast run", func() bool {
		f = c.call("GET", "/api/forecasts/"+id, nil, 200)
		return str(t, f, "forecast.status") != "running"
	})
	shape(t, f, "forecast.result", resultKeys)
	shape(t, f, "forecast.result.market", marketKeys)
	m := "forecast.result.market."
	if str(t, f, "forecast.status") != "completed" || str(t, f, m+"match") != "exact" || num(t, f, m+"yesPrice") != 0.4 ||
		num(t, f, m+"edgePp") != 6 || num(t, f, m+"evYes") != 0.06 || num(t, f, m+"evNo") != -0.06 || str(t, f, m+"signal") != "yes" ||
		num(t, f, m+"volumeUsd") != 2000 || at(t, f, m+"thin") != false || str(t, f, m+"url") != "https://polymarket.com/event/bitcoin-above-on-december-31-2099" ||
		str(t, f, m+"source") != "Polymarket" || !strings.Contains(str(t, f, m+"question"), "$120,000") {
		t.Fatalf("market: %v", at(t, f, "forecast.result"))
	}
	isTime(t, f, m+"asOf")
	mu.Lock()
	defer mu.Unlock()
	if len(queries) != 1 || queries[0] != "/events bitcoin-above-on-december-31-2099" {
		t.Fatalf("Gamma queries %q", queries)
	}
}

// A thin market (under $1,000 traded) never shows an edge; a market that
// settles before the resolution date on the same event is found by keywords
// and judged; either way the quant and the aggregator see it as M1.
func TestIntegrationForecastMarketThinAndEvents(t *testing.T) {
	var mu sync.Mutex
	var queries []string
	market := func(q, end, yes, volume string) string {
		return fmt.Sprintf(`{"question":%q,"outcomes":"[\"Yes\", \"No\"]","outcomePrices":"[\"%s\", \"0.5\"]","endDate":%q,"active":true,"closed":false,"volumeNum":%s}`,
			q, yes, end, volume)
	}
	s := boot(t, options{
		gamma: func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query().Get("q")
			mu.Lock()
			queries = append(queries, q)
			mu.Unlock()
			if r.URL.Path == "/events" { // the dated ladder's slug: nothing there, so the price rule searches
				io.WriteString(w, `[]`)
				return
			}
			switch q {
			case "Bitcoin above":
				fmt.Fprintf(w, `{"events":[{"slug":"bitcoin-above-on-december-31-2099","markets":[%s]}]}`,
					market("Will the price of Bitcoin be above $130,000 on December 31?", "2099-12-31T17:00:00Z", "0.2", "65.7"))
			case "Democrats win control US House November 2098 midterms":
				fmt.Fprintf(w, `{"events":[{"slug":"house-2098","markets":[%s,%s]}]}`,
					market("Will the Democratic Party control the House after the 2098 Midterm elections?", "2098-11-04T12:00:00Z", "0.71", "3400000"),
					market("Will Democrats hold the House on inauguration day 2099?", "2099-01-20T12:00:00Z", "0.7", "200000"))
			default:
				io.WriteString(w, `{"events":[]}`)
			}
		},
		model: func(r llmtest.Request) llmtest.Reply {
			switch {
			case strings.HasPrefix(r.System, queryWriter): // the Polymarket query writer: no queries, so the question's keywords are searched
				return noMarketQueries
			case strings.HasPrefix(r.System, "Decide whether one of these prediction markets"):
				if strings.Contains(r.Prompt, "inauguration") {
					t.Errorf("a market ending > 7 days after the resolution date reached the judge: %s", r.Prompt)
				}
				return llmtest.Reply{Text: `{"index": 0, "match": "close"}`}
			case strings.HasPrefix(r.System, "You are the aggregator"), strings.HasPrefix(r.System, "You are the Market & quant"):
				if !strings.Contains(r.Prompt, "M1 · Polymarket (prediction market, live)") {
					t.Errorf("%.40s prompt lacks M1: %.300s", r.System, r.Prompt)
				}
				if strings.Contains(r.Prompt, "$130,000") != strings.Contains(r.Prompt, "(volume $65.7; thin market: weigh lightly)") {
					t.Errorf("thin M1: %.300s", r.Prompt)
				}
			case strings.HasPrefix(r.System, "You are the"):
				if strings.Contains(r.Prompt, "M1 ·") {
					t.Errorf("%.40s saw M1", r.System)
				}
			default:
				t.Errorf("unexpected model call: %.60q", r.System)
				return llmtest.Reply{Status: 400}
			}
			if strings.HasPrefix(r.System, "You are the aggregator") {
				return llmtest.Reply{Text: `{"probabilities":[{"outcome":"Yes","p":0.55},{"outcome":"No","p":0.45}],"summary":"We lean Yes.","confidence":"medium"}`}
			}
			return llmtest.Reply{Text: `{"probabilities":[{"outcome":"Yes","p":0.5},{"outcome":"No","p":0.5}],"reasoning":"r","keyFactors":["k"],"confidence":"medium"}`}
		},
	})
	c := s.signIn(t)
	run := func(draft map[string]any) any {
		id := str(t, c.call("POST", "/api/forecasts", draft, 201), "forecast.id")
		var f any
		eventually(t, 30*time.Second, "the forecast run", func() bool {
			f = c.call("GET", "/api/forecasts/"+id, nil, 200)
			return str(t, f, "forecast.status") != "running"
		})
		shape(t, f, "forecast.result.market", marketKeys)
		return f
	}
	m := "forecast.result.market."
	f := run(map[string]any{"shape": "binary", "question": "Will the Binance BTC/USDT daily close on 2099-12-31 be above $130,000?",
		"outcomes": []string{"Yes", "No"}, "resolutionDate": "2099-12-31", "category": "crypto",
		"priceRule": map[string]any{"symbol": "BTC", "op": "above", "threshold": 130000, "mode": "on_date"}})
	// The desk's uncited 55% is across 50% from R1 (BTC far above $130,000 is unlikely without drift), so the shrink
	// guard resets it to R1 and the edge is against that final number.
	r1 := num(t, f, "forecast.result.reference.pYes")
	if str(t, f, "forecast.result.guard") != "shrink" || r1 >= 0.5 || num(t, f, "forecast.result.probabilities.0.p") != r1 {
		t.Fatalf("guard: %v", at(t, f, "forecast.result"))
	}
	if at(t, f, m+"thin") != true || str(t, f, m+"signal") != "wait" || num(t, f, m+"edgePp") != math.Round((r1-0.2)*100*10)/10 || num(t, f, m+"volumeUsd") != 65.7 {
		t.Fatalf("thin market: %v", at(t, f, "forecast.result.market"))
	}
	f = run(map[string]any{"shape": "binary", "question": "Will Democrats win control of the US House in the November 2098 midterms?",
		"outcomes": []string{"Yes", "No"}, "resolutionDate": "2098-12-31", "category": "politics"})
	if at(t, f, m+"thin") != false || str(t, f, m+"match") != "close" || num(t, f, m+"yesPrice") != 0.71 || str(t, f, m+"signal") != "no" ||
		num(t, f, m+"edgePp") != -16 || str(t, f, m+"url") != "https://polymarket.com/event/house-2098" {
		t.Fatalf("event market: %v", at(t, f, "forecast.result.market"))
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(queries, " | ") != " | Bitcoin above on December 31 | Bitcoin above | Democrats win control US House November 2098 midterms" {
		t.Fatalf("Gamma queries %q", queries)
	}
}

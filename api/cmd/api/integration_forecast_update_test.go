package main

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
)

// A living forecast: a run completes through the fake model, an update
// re-runs it (5 credits) with the previous estimate shown to the aggregator,
// and both runs read as one probability history; the list shows the newest.
func TestIntegrationForecastUpdate(t *testing.T) {
	var aggregations atomic.Int32
	s := boot(t, options{model: func(r llmtest.Request) llmtest.Reply {
		switch {
		case strings.HasPrefix(r.System, queryWriter): // the Polymarket query writer: no queries, so the question's keywords are searched
			return noMarketQueries
		case strings.HasPrefix(r.System, "You are the aggregator"):
			if aggregations.Add(1) == 1 {
				return llmtest.Reply{Text: `{"probabilities":[{"outcome":"Yes","p":0.28},{"outcome":"No","p":0.72}],"summary":"We lean No.","confidence":"medium"}`}
			}
			if !strings.Contains(r.Prompt, "Previous desk estimate (") || !strings.Contains(r.Prompt, "Yes 28%; No 72%. Its summary: We lean No.") {
				t.Errorf("the update's aggregator lacks the prior: %.400s", r.Prompt)
			}
			return llmtest.Reply{Text: `{"probabilities":[{"outcome":"Yes","p":0.34},{"outcome":"No","p":0.66}],"summary":"We moved up.","confidence":"medium",
				"changeNote":"Fresh ETF inflows moved us up."}`}
		case strings.HasPrefix(r.System, "You are the"):
			if strings.Contains(r.Prompt, "Previous desk estimate") {
				t.Errorf("%.40s saw the prior", r.System)
			}
			return llmtest.Reply{Text: `{"probabilities":[{"outcome":"Yes","p":0.3},{"outcome":"No","p":0.7}],"reasoning":"r","keyFactors":["k"],"confidence":"medium"}`}
		}
		t.Errorf("unexpected model call: %.60q", r.System)
		return llmtest.Reply{Status: 400}
	}})
	c := s.signIn(t)
	done := func(id string) any {
		var f any
		eventually(t, 30*time.Second, "the forecast run", func() bool {
			f = c.call("GET", "/api/forecasts/"+id, nil, 200)
			return str(t, f, "forecast.status") != "running"
		})
		return f
	}
	first := str(t, c.call("POST", "/api/forecasts", map[string]any{"shape": "binary", "question": "Will the Fed cut rates at its December 2099 meeting?",
		"outcomes": []string{"Yes", "No"}, "resolutionDate": "2099-12-31", "category": "macro"}, 201), "forecast.id")
	if f := done(first); len(list(t, f, "forecast.history")) != 1 || !isNull(t, f, "forecast.parentId") {
		t.Fatalf("first run: %v", f)
	}

	up := c.call("POST", "/api/forecasts/"+first+"/update", map[string]any{}, 201)
	shape(t, up, "forecast", forecastKeys)
	id := str(t, up, "forecast.id")
	if str(t, up, "forecast.parentId") != first || str(t, up, "forecast.status") != "running" || num(t, up, "credits.used") != 10 {
		t.Fatalf("update: %v", up)
	}
	f := done(id)
	shape(t, f, "forecast", forecastKeys)
	shape(t, f, "forecast.result", resultKeys+" changeNote")
	shapeAll(t, f, "forecast.history", "id outcome p at")
	h := list(t, f, "forecast.history")
	if str(t, f, "forecast.result.changeNote") != "Fresh ETF inflows moved us up." || len(h) != 2 || str(t, h[0], "id") != first || str(t, h[1], "id") != id ||
		num(t, h[0], "p") != 0.28 || num(t, h[1], "p") != 0.34 || str(t, h[1], "outcome") != "Yes" || isTime(t, h[0], "at").After(isTime(t, h[1], "at")) {
		t.Fatalf("updated run: %v", f)
	}
	l := list(t, c.call("GET", "/api/forecasts", nil, 200), "forecasts")
	if len(l) != 1 || str(t, l[0], "id") != id || str(t, l[0], "parentId") != first {
		t.Fatalf("the list shows the newest run: %v", l)
	}
	c.fail("POST", "/api/forecasts/"+first+"/resolve", map[string]any{"outcome": "nope"}, 400, "bad_request")
	c.call("POST", "/api/forecasts/"+id+"/resolve", map[string]any{"outcome": "No"}, 200)
	c.fail("POST", "/api/forecasts/"+id+"/update", map[string]any{}, 409, "conflict") // resolved
	if n := s.count(t, "select count(*) from forecasts where parent_id = $1", first); n != 1 {
		t.Fatalf("children: %d", n)
	}
}

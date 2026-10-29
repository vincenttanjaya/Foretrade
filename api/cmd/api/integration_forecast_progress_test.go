package main

import (
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
)

const progressKeys = "research specialists aggregator market materials updatedAt"

// A run you can watch: while the specialists think, GET /api/forecasts/{id}
// already shows the research done (queries, sources found and read) and the
// three specialists running; when it completes, every stage is done, the
// Polymarket lookup says it found no match, and the skeptic, whose labels
// did not fit the outcomes, came back through the repair call: three views.
func TestIntegrationForecastProgress(t *testing.T) {
	release := make(chan struct{})
	open := sync.OnceFunc(func() { close(release) })
	defer open() // before boot's cleanups, which wait for the run: never leave the fake blocked when the test stops early
	cats := `{"probabilities":[{"outcome":"Nvidia","p":0.55},{"outcome":"Apple","p":0.25},{"outcome":"Microsoft","p":0.2}],"reasoning":"Caps from S1.","keyFactors":["AI capex"],"confidence":"medium"}`
	s := boot(t, options{brave: true, model: func(r llmtest.Request) llmtest.Reply {
		switch {
		case strings.HasPrefix(r.System, queryWriter):
			return noMarketQueries
		case strings.HasPrefix(r.System, "You are the research assistant"):
			return llmtest.Reply{Text: `{"queries":["Nvidia Apple Microsoft market cap","largest company by market cap"]}`}
		case strings.HasPrefix(r.System, repairWriter):
			return llmtest.Reply{Text: `{"probabilities":[{"outcome":"Nvidia","p":0.5},{"outcome":"Apple","p":0.3},{"outcome":"Microsoft","p":0.2}],"reasoning":"Crowded.","confidence":"low"}`}
		case strings.HasPrefix(r.System, "You are the Narrative & macro skeptic"):
			<-release
			return llmtest.Reply{Text: `{"probabilities":[{"outcome":"NVIDIA Corp","p":0.5},{"outcome":"Apple Inc.","p":0.3},{"outcome":"MSFT","p":0.2}],"reasoning":"Crowded.","confidence":"low"}`}
		case strings.HasPrefix(r.System, "You are the Base-rate statistician"), strings.HasPrefix(r.System, "You are the Market & quant analyst"):
			<-release
			return llmtest.Reply{Text: cats}
		case strings.HasPrefix(r.System, "You are the aggregator"):
			return llmtest.Reply{Text: `{"probabilities":[{"outcome":"Nvidia","p":0.52},{"outcome":"Apple","p":0.28},{"outcome":"Microsoft","p":0.2}],"summary":"We favour Nvidia.","confidence":"medium"}`}
		}
		t.Errorf("unexpected model call: %.60q", r.System)
		return llmtest.Reply{Status: 400}
	}})
	c := s.signIn(t)
	created := c.call("POST", "/api/forecasts", map[string]any{"shape": "categorical", "question": "Which company will be the largest by market cap on 2099-12-31?",
		"outcomes": []string{"Nvidia", "Apple", "Microsoft"}, "resolutionDate": "2099-12-31", "category": "markets"}, 201)
	shape(t, created, "forecast", forecastKeys)
	id := str(t, created, "forecast.id")
	if !isNull(t, created, "forecast.progress") {
		t.Fatalf("a new run has no trace yet: %v", at(t, created, "forecast.progress"))
	}

	// Mid-run: the research is done and on the page while the specialists are held.
	var f any
	eventually(t, 30*time.Second, "the research done and the specialists running", func() bool {
		f = c.call("GET", "/api/forecasts/"+id, nil, 200)
		if isNull(t, f, "forecast.progress") {
			return false
		}
		running := 0
		for _, sp := range list(t, f, "forecast.progress.specialists") {
			if str(t, sp, "status") == "running" {
				running++
			}
		}
		return str(t, f, "forecast.progress.research.status") == "done" && running == 3
	})
	p := "forecast.progress."
	shape(t, f, "forecast.progress", progressKeys)
	shape(t, f, p+"research", "status queries found read mode depth angles searches findings steps")
	shapeAll(t, f, p+"specialists", "role status top")
	shape(t, f, p+"aggregator", "status")
	shape(t, f, p+"market", "status matched")
	isTime(t, f, p+"updatedAt")
	if str(t, f, "forecast.status") != "running" || str(t, f, p+"research.mode") != "live" || num(t, f, p+"research.found") != 3 ||
		num(t, f, p+"research.read") != 3 || len(list(t, f, p+"research.queries")) != 2 || str(t, f, p+"research.queries.0") != "Nvidia Apple Microsoft market cap" ||
		str(t, f, p+"research.depth") != "quick" || num(t, f, p+"research.searches") != 3 || len(list(t, f, p+"research.steps")) != 0 || len(list(t, f, p+"research.angles")) != 0 ||
		str(t, f, p+"specialists.2.role") != "skeptic" || !isNull(t, f, p+"specialists.0.top") || str(t, f, p+"aggregator.status") != "pending" {
		t.Fatalf("mid-run trace: %v", at(t, f, "forecast.progress"))
	}

	open()
	eventually(t, 30*time.Second, "the forecast run", func() bool {
		f = c.call("GET", "/api/forecasts/"+id, nil, 200)
		return str(t, f, "forecast.status") != "running"
	})
	shape(t, f, "forecast", forecastKeys)
	for i := range 3 {
		sp := at(t, f, p+"specialists."+strconv.Itoa(i))
		shape(t, sp, "top", "outcome p")
		if str(t, sp, "status") != "done" || str(t, sp, "top.outcome") != "Nvidia" {
			t.Fatalf("specialist %d: %v", i, sp)
		}
	}
	if str(t, f, "forecast.status") != "completed" || str(t, f, p+"aggregator.status") != "done" || str(t, f, p+"market.status") != "done" ||
		at(t, f, p+"market.matched") != false || num(t, f, p+"specialists.2.top.p") != 0.5 || len(list(t, f, "forecast.result.specialists")) != 3 ||
		str(t, f, "forecast.result.specialists.2.role") != "skeptic" {
		t.Fatalf("final: %v / %v", at(t, f, "forecast.progress"), at(t, f, "forecast.result.specialists"))
	}
	if s.count(t, "select count(*) from forecasts where id = $1 and progress->'aggregator'->>'status' = 'done'", id) != 1 ||
		s.count(t, "select credits from ai_usage where user_id = $1", c.id) != 5 {
		t.Fatal("the trace is stored on the row, and the repair costs nothing extra")
	}
}

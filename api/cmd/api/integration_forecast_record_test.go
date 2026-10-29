package main

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
)

const trackKeys = "n brier baselineBrier anchorBrier logLoss hitRate bins skill brierCi90 anchorDiffCi90 anchorN anchorSkill claim"

// The forecast desk's track record: boot inserts the embedded replay seed,
// the track record scores it, and a user's categorical forecast resolved by
// hand becomes their own record. The aggregator is held until the resolve
// of the still-running forecast has been refused, so the run cannot finish
// first.
func TestIntegrationForecastRecord(t *testing.T) {
	const cats = `{"probabilities":[{"outcome":"Fed","p":0.5},{"outcome":"ECB","p":0.3},{"outcome":"BoE","p":0.2}]`
	release := make(chan struct{})
	open := sync.OnceFunc(func() { close(release) })
	s := boot(t, options{model: func(r llmtest.Request) llmtest.Reply {
		switch {
		case strings.HasPrefix(r.System, queryWriter): // the Polymarket query writer: no queries, so the question's keywords are searched
			return noMarketQueries
		case strings.HasPrefix(r.System, "You are the aggregator"):
			<-release
			return llmtest.Reply{Text: cats + `,"summary":"We lean Fed.","drivers":[],"evidence":[],"flags":[],"whatWouldChange":[],"confidence":"medium"}`}
		case strings.HasPrefix(r.System, "You are the Base-rate"), strings.HasPrefix(r.System, "You are the Market & quant"), strings.HasPrefix(r.System, "You are the Narrative"):
			return llmtest.Reply{Text: cats + `,"reasoning":"Base rates.","keyFactors":["k"],"confidence":"medium"}`}
		}
		t.Errorf("unexpected model call: %.60q", r.System)
		return llmtest.Reply{Status: 400}
	}})
	// Registered after boot, so it runs before the fake Bedrock's Close, which
	// waits for a held call: a test that stops early never hangs there.
	t.Cleanup(open)

	b, err := os.ReadFile("../../internal/forecast/replay_seed.json")
	if err != nil {
		t.Fatal(err)
	}
	var seed []struct{ Outcome *string }
	if err := json.Unmarshal(b, &seed); err != nil {
		t.Fatal(err)
	}
	decided := 0
	for _, r := range seed {
		if r.Outcome != nil {
			decided++
		}
	}
	if n := s.count(t, "select count(*) from forecasts where user_id is null and replay_key is not null and status = 'completed'"); n != len(seed) {
		t.Fatalf("replay rows %d, seed %d", n, len(seed))
	}

	c := s.signIn(t)
	tr := c.call("GET", "/api/forecasts/track-record", nil, 200)
	shape(t, tr, "", "replay mine")
	shape(t, tr, "replay", trackKeys)
	shape(t, tr, "mine", trackKeys)
	shapeAll(t, tr, "replay.bins", "lo hi n meanP observed")
	shape(t, tr, "replay.claim", "status basis minN n skill")
	if str(t, tr, "replay.claim.status") != "too_few" || num(t, tr, "replay.claim.n") != float64(decided) || num(t, tr, "replay.claim.minN") != 50 ||
		!isNull(t, tr, "replay.claim.basis") || !isNull(t, tr, "replay.claim.skill") || num(t, tr, "replay.anchorN") != float64(decided) ||
		str(t, tr, "mine.claim.status") != "too_few" || num(t, tr, "mine.claim.n") != 0 || num(t, tr, "mine.anchorN") != 0 || !isNull(t, tr, "mine.anchorSkill") {
		t.Fatalf("claims: replay %v, mine %v", at(t, tr, "replay.claim"), at(t, tr, "mine.claim"))
	}
	if num(t, tr, "replay.n") != float64(decided) || num(t, tr, "mine.n") != 0 || !isNull(t, tr, "mine.brier") || !isNull(t, tr, "mine.anchorBrier") ||
		len(list(t, tr, "mine.bins")) != 5 || num(t, tr, "mine.bins.4.hi") != 1 {
		t.Fatalf("track record: %v", tr)
	}
	replay := list(t, c.call("GET", "/api/forecasts?scope=replay", nil, 200), "forecasts")
	if len(replay) != len(seed) || len(list(t, c.call("GET", "/api/forecasts", nil, 200), "forecasts")) != 0 {
		t.Fatalf("replay list %d (seed %d), or replay rows in the user's own list", len(replay), len(seed))
	}
	if len(seed) > 0 {
		id := str(t, replay[0], "id")
		f := c.call("GET", "/api/forecasts/"+id, nil, 200)
		shape(t, f, "forecast", forecastKeys)
		shape(t, f, "forecast.replay", "asOf anchorP")
		shape(t, f, "forecast.priceRule", "symbol op threshold mode")
		if at(t, replay[0], "replay") != true || str(t, f, "forecast.resolution.by") != "auto" {
			t.Fatalf("replay row: %v", f)
		}
		c.fail("DELETE", "/api/forecasts/"+id, nil, 404, "not_found")
	}

	created := c.call("POST", "/api/forecasts", map[string]any{"shape": "categorical", "question": "Which central bank cuts rates first in 2099?",
		"outcomes": []string{"Fed", "ECB", "BoE"}, "resolutionDate": "2099-06-30", "category": "macro"}, 201)
	id := str(t, created, "forecast.id")
	if !isNull(t, created, "forecast.resolution") || !isNull(t, created, "forecast.replay") || !isNull(t, created, "forecast.priceRule") {
		t.Fatalf("new forecast: %v", created)
	}
	c.fail("POST", "/api/forecasts/"+id+"/resolve", map[string]any{"outcome": "ECB"}, 409, "conflict") // still running: the aggregator is held
	if f := c.call("GET", "/api/forecasts/"+id, nil, 200); str(t, f, "forecast.status") != "running" || !isNull(t, f, "forecast.resolution") {
		t.Fatalf("the refused resolve changed the running forecast: %v", f)
	}
	open()
	eventually(t, 30*time.Second, "the forecast run", func() bool {
		return str(t, c.call("GET", "/api/forecasts/"+id, nil, 200), "forecast.status") == "completed"
	})
	c.fail("POST", "/api/forecasts/"+id+"/resolve", map[string]any{"outcome": "SNB"}, 400, "bad_request")
	r := c.call("POST", "/api/forecasts/"+id+"/resolve", map[string]any{"outcome": "ECB"}, 200)
	shape(t, r, "forecast.resolution", "outcome resolvedAt by")
	isTime(t, r, "forecast.resolution.resolvedAt")
	if str(t, r, "forecast.resolution.outcome") != "ECB" || str(t, r, "forecast.resolution.by") != "manual" {
		t.Fatalf("resolve: %v", r)
	}
	tr = c.call("GET", "/api/forecasts/track-record", nil, 200)
	if num(t, tr, "mine.n") != 1 || !near(num(t, tr, "mine.brier"), (0.25+0.49+0.04)/3) || num(t, tr, "mine.hitRate") != 0 ||
		num(t, tr, "replay.n") != float64(decided) {
		t.Fatalf("track record after resolving: %v", tr)
	}
	if l := list(t, c.call("GET", "/api/forecasts", nil, 200), "forecasts"); len(l) != 1 || str(t, l[0], "resolvedOutcome") != "ECB" {
		t.Fatalf("summary: %v", l)
	}
}

package forecast

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
)

// traced wraps a stage router: on the first call of each stage it records
// the run's stored trace, so a test sees the progress in the order the
// pipeline wrote it.
func traced(t *testing.T, e **apptest.Env, route func(llmtest.Request) llmtest.Reply) (func(llmtest.Request) llmtest.Reply, func(string) *Progress) {
	var mu sync.Mutex
	seen := map[string]*Progress{}
	stages := map[string]string{"You are the research assistant": "queries", "You are the Base-rate": "base_rate",
		"A forecaster's answer gave probabilities": "repair", "You are the aggregator": "aggregator"}
	return func(r llmtest.Request) llmtest.Reply {
			for prefix, stage := range stages {
				if !strings.HasPrefix(r.System, prefix) {
					continue
				}
				var p *Progress
				if err := (*e).DB.QueryRow(context.Background(), `select progress from forecasts where user_id = $1 and status = 'running'`, (*e).User).Scan(&p); err != nil {
					t.Errorf("%s: %v", stage, err)
				}
				mu.Lock()
				if _, ok := seen[stage]; !ok {
					seen[stage] = p
				}
				mu.Unlock()
			}
			return route(r)
		}, func(stage string) *Progress {
			mu.Lock()
			defer mu.Unlock()
			if seen[stage] == nil {
				t.Fatalf("no trace seen at %s", stage)
			}
			return seen[stage]
		}
}

func statuses(p *Progress) string {
	s := []string{p.Research.Status}
	for _, sp := range p.Specialists {
		s = append(s, sp.Status)
	}
	return strings.Join(append(s, p.Aggregator.Status, p.Market.Status), " ")
}

// A categorical run traces every stage (queries before the search, the
// specialists' numbers, the aggregator, the Polymarket match), and a skeptic
// that answered with the wrong labels is repaired, so the panel keeps all three.
func TestRunProgressAndRepair(t *testing.T) {
	brave := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/web/search" {
			fmt.Fprint(w, `{"web":{"results":[]}}`)
			return
		}
		fmt.Fprintf(w, `{"results":[{"title":"Nvidia market cap tops $5tn (%[1]s)","url":"https://desk.localhost/%[2]s","description":"Chips","page_age":%[3]q,"profile":{"name":"Desk"}}]}`,
			r.URL.Query().Get("q"), strings.ReplaceAll(r.URL.Query().Get("q"), " ", "-"), time.Now().UTC().Add(-5*time.Hour).Format("2006-01-02T15:04:05"))
	}
	cats := func(n, a, m float64) string {
		return fmt.Sprintf(`{"probabilities":[{"outcome":"Nvidia","p":%v},{"outcome":"Apple","p":%v},{"outcome":"Microsoft","p":%v}],"reasoning":"Market caps from S1.","keyFactors":["AI capex"],"confidence":"medium"}`, n, a, m)
	}
	var e *apptest.Env
	stages := panel(t, map[string]llmtest.Reply{
		"queries":   {Text: `{"queries":["Nvidia Apple Microsoft market cap","largest company 2099"]}`},
		"base_rate": {Text: cats(0.5, 0.3, 0.2)},
		"quant":     {Text: cats(0.6, 0.25, 0.15)},
		"skeptic":   {Text: `{"probabilities":[{"outcome":"NVIDIA Corp","p":0.45},{"outcome":"Apple Inc.","p":0.35},{"outcome":"MSFT","p":0.2}],"reasoning":"Crowded.","keyFactors":["Antitrust"],"confidence":"low"}`},
		"repair":    {Text: cats(0.45, 0.35, 0.2)},
		"aggregator": {Text: `{"probabilities":[{"outcome":"Nvidia","p":0.52},{"outcome":"Apple","p":0.3},{"outcome":"Microsoft","p":0.18}],
			"summary":"We favour Nvidia.","confidence":"medium"}`},
	}, false)
	reply, at := traced(t, &e, func(r llmtest.Request) llmtest.Reply {
		switch {
		case strings.HasPrefix(r.System, "Write up to 2 short Polymarket"):
			return noQueries
		case strings.HasPrefix(r.System, "Match each option"):
			return llmtest.Reply{Text: `{"options": {"Nvidia": 0}, "match": "exact"}`}
		}
		return stages(r)
	})
	e = apptest.New(t, Register, apptest.Options{Brave: brave, Reply: reply})
	e.PolymarketBase, _ = gamma(t, map[string][]map[string]any{"largest-company-end-of-december-2099": {
		gammaMarket("Will NVIDIA be the largest company in the world by market cap on December 31?", "2100-01-01T00:00:00Z", 0.85, 1.3e6)}})
	d := Draft{Shape: "categorical", Question: "Which company will be the largest by market cap on 2099-12-31?", Outcomes: []string{"Nvidia", "Apple", "Microsoft"},
		ResolutionDate: "2099-12-31", Category: "markets"}
	f := waitRun(t, e, create(t, e, d))
	if f.Status != "completed" || f.Result == nil {
		t.Fatalf("run: %+v", f)
	}

	// The trace as each stage started (the market lookup runs beside the research, so it may already be done here).
	if p := at("queries"); !strings.HasPrefix(statuses(p), "running pending pending pending pending ") || len(p.Research.Queries) != 0 {
		t.Errorf("at the query writer: %s %q", statuses(p), p.Research.Queries)
	}
	if p := at("base_rate"); p.Research.Status != "done" || *p.Research.Mode != "live" || p.Research.Found != 2 ||
		!slices.Equal(p.Research.Queries, []string{"Nvidia Apple Microsoft market cap", "largest company 2099"}) || p.Specialists[0].Status != "running" || p.Aggregator.Status != "pending" {
		t.Errorf("at the specialists: %s %+v", statuses(p), p.Research)
	}
	if p := at("repair"); p.Specialists[2].Status != "running" {
		t.Errorf("the skeptic is still running during its repair: %s", statuses(p))
	}
	if p := at("aggregator"); statuses(p) != "done done done done running done" || !p.Market.Matched || p.Specialists[0].Top == nil {
		t.Errorf("at the aggregator: %s %+v", statuses(p), p.Market)
	}
	p := f.Progress
	if p == nil || statuses(p) != "done done done done done done" || !p.Market.Matched ||
		*p.Specialists[0].Top != (OutcomeP{"Nvidia", 0.5}) || *p.Specialists[1].Top != (OutcomeP{"Nvidia", 0.6}) || *p.Specialists[2].Top != (OutcomeP{"Nvidia", 0.45}) {
		t.Fatalf("final trace: %+v", p)
	}

	// The repaired skeptic joins the panel; the repair was one low-effort call that saw the outcomes and the answer.
	r := f.Result
	if len(r.Specialists) != 3 || r.Specialists[2].Role != "skeptic" || !near(r.Specialists[2].Probabilities[1].P, 0.35) ||
		slices.ContainsFunc(r.Flags, func(fl Flag) bool { return strings.Contains(fl.Note, "unavailable") }) || r.Market == nil || r.Market.Outcome != "Nvidia" {
		t.Fatalf("panel: %+v flags %+v market %+v", r.Specialists, r.Flags, r.Market)
	}
	repairs := slices.DeleteFunc(e.Bedrock.Requests(), func(q llmtest.Request) bool { return !strings.HasPrefix(q.System, "A forecaster's answer") })
	if len(repairs) != 1 || repairs[0].Effort != "low" || !strings.HasPrefix(repairs[0].Prompt, `Outcomes: ["Nvidia","Apple","Microsoft"]`+"\n\nForecaster answer:\n{") ||
		!strings.Contains(repairs[0].Prompt, `"outcome":"NVIDIA Corp"`) {
		t.Fatalf("repair calls: %+v", repairs)
	}
	if credits(t, e) != 5 {
		t.Fatalf("the repair runs inside the run's 5 credits: %d", credits(t, e))
	}
	for _, q := range e.Bedrock.Requests() {
		if strings.HasPrefix(q.System, "You are the Base-rate") && !strings.Contains(q.System, "Never write that you lack a tool, a data feed or access.") {
			t.Fatal("specialists are told never to claim a missing tool")
		}
		if strings.HasPrefix(q.System, "You are the research assistant") && !strings.Contains(q.System, `"Nvidia Apple Microsoft market cap"`) {
			t.Fatal("the query writer is told to fetch the compared figures")
		}
	}
}

// A word-ticker price question in the draft's canonical form ("Binance
// LINK/USDT") gets LINK's live spot in every specialist's brief, and the
// knowledge note says the run used it.
func TestRunWordTickerSpot(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{Reply: panel(t, map[string]llmtest.Reply{
		"base_rate":  {Text: opinion(0.3, "Base rate.")},
		"quant":      {Text: opinion(0.2, "Lognormal.")},
		"skeptic":    {Text: opinion(0.25, "Catalysts.")},
		"aggregator": {Text: `{"probabilities":[{"outcome":"Yes","p":0.25}],"summary":"We lean No.","confidence":"medium"}`},
	}, false)})
	e.PolymarketBase, _ = gamma(t, nil)
	f := waitRun(t, e, create(t, e, Draft{Shape: "binary", Question: "Will the Binance LINK/USDT daily close on 2099-12-31 be above $30?", Outcomes: yesNo,
		ResolutionCriteria: "Resolves Yes if the Binance LINKUSDT 1d candle that opens at 00:00 UTC on 2099-12-31 closes above $30; otherwise No.",
		ResolutionDate:     "2099-12-31", Category: "crypto", PriceRule: &PriceRule{"LINK", "above", 30, "on_date"}}))
	if f.Status != "completed" || !strings.HasPrefix(f.Result.KnowledgeNote, "Model knowledge (training data) + live Binance spot data as of ") {
		t.Fatalf("run: %s %+v", f.Status, f.Result)
	}
	briefs := 0
	for _, r := range e.Bedrock.Requests() {
		if strings.HasPrefix(r.System, "You are the Base-rate") || strings.HasPrefix(r.System, "You are the Market") || strings.HasPrefix(r.System, "You are the Narrative") {
			if briefs++; !strings.Contains(r.Prompt, "Live Binance spot as of") || !strings.Contains(r.Prompt, "\n- LINK (Chainlink): $20, 24h ") {
				t.Errorf("brief without LINK spot: %.600s", r.Prompt)
			}
		}
	}
	if briefs != 3 {
		t.Fatalf("specialist calls: %d", briefs)
	}
}

// A repair that still misses drops the specialist: two of three, flagged.
// No search and no market: research ends as none and the lookup as no match.
func TestRunFailedRepair(t *testing.T) {
	var e *apptest.Env
	reply, _ := traced(t, &e, panel(t, map[string]llmtest.Reply{
		"base_rate":  {Text: opinion(0.3, "Base rate.")},
		"quant":      {Text: opinion(0.2, "Lognormal.")},
		"skeptic":    {Text: `{"probabilities":[{"outcome":"Maybe","p":1}]}`},
		"repair":     {Text: `{"probabilities":[{"outcome":"Maybe","p":1}]}`},
		"aggregator": {Text: `{"probabilities":[{"outcome":"Yes","p":0.25}],"summary":"We lean No.","confidence":"medium"}`},
	}, false))
	e = apptest.New(t, Register, apptest.Options{Reply: reply})
	e.PolymarketBase, _ = gamma(t, nil)
	f := waitRun(t, e, create(t, e, Draft{Shape: "binary", Question: "Will the Binance BTC/USDT daily close on 2099-12-31 be above $120,000?",
		Outcomes: yesNo, ResolutionDate: "2099-12-31", Category: "crypto", PriceRule: &PriceRule{"BTC", "above", 120000, "on_date"}}))
	r, p := f.Result, f.Progress
	if f.Status != "completed" || len(r.Specialists) != 2 || !slices.ContainsFunc(r.Flags, func(fl Flag) bool {
		return fl.Type == "low_information" && fl.Note == "Narrative & macro skeptic unavailable for this run; the forecast combines 2 of 3 specialists."
	}) {
		t.Fatalf("run: %+v", f)
	}
	if p == nil || statuses(p) != "done done done failed done done" || p.Specialists[2].Top != nil || *p.Specialists[0].Top != (OutcomeP{"Yes", 0.3}) ||
		*p.Research.Mode != "none" || p.Research.Found != 0 || len(p.Research.Queries) != 0 || p.Market.Matched {
		t.Fatalf("trace: %+v", p)
	}
}

// TestRunSchedule pins the run budget: a deep run's panel deadlines follow its
// longer research stage, quick runs and replays keep theirs, and the stage
// leaves the gap round room after a typical round 1.
func TestRunSchedule(t *testing.T) {
	if by, end := schedule("deep"); by != 370*time.Second || end != 8*time.Minute {
		t.Fatalf("deep: %v %v", by, end)
	}
	for _, depth := range []string{"quick", ""} {
		if by, end := schedule(depth); by != 300*time.Second || end != 7*time.Minute {
			t.Fatalf("%q: %v %v", depth, by, end)
		}
	}
	const sweep = 10 * time.Minute   // Recover fails runs older than this
	const round1 = 115 * time.Second // a typical round 1: plan, search, screen, reads, extract
	switch {
	case limits.stage != 230*time.Second:
		t.Fatalf("stage %v", limits.stage)
	case specialistsBy-limits.stage < 140*time.Second, runTimeout-specialistsBy < 110*time.Second:
		t.Fatal("deep panel slices")
	case quickSpecialistsBy-researchTimeout < 140*time.Second, quickRunTimeout-quickSpecialistsBy < 110*time.Second:
		t.Fatal("quick panel slices")
	case runTimeout >= sweep || quickRunTimeout > runTimeout:
		t.Fatal("run timeouts against the recover sweep")
	case limits.stage-round1 < limits.gapMinLeft:
		t.Fatal("no room for the gap round after a typical round 1")
	case limits.gapMinLeft-limits.readTail < 45*time.Second:
		t.Fatal("round 2's gap call, search, screen and reads need >= 45 s before the extract reserve")
	}
}

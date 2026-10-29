package main

import (
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo/yahootest"
)

// The owner's TSM question on its stored report's six ranges (round 9b).
var methodsOutcomes = []string{"< $300", "$300 – $450", "$450 – $600", "$600 – $800", "$800 – $1,100", "≥ $1,100"}

const (
	methodsPlan = `{"angles":[
		{"kind":"drivers","label":"Consensus EPS and guidance","queries":[{"q":"TSMC consensus EPS estimates next fiscal years","type":"web","recency":"year"}]},
		{"kind":"comparables","label":"Semiconductor windows","queries":[{"q":"semiconductor index five year returns history","type":"web","recency":"any"}]}]}`
	// The aggregator keeps the pool, writes the scenarios' words and opens
	// with process talk the server drops.
	methodsAggregate = `{"probabilities":[{"outcome":"< $300","p":0.17},{"outcome":"$300 – $450","p":0.17},{"outcome":"$450 – $600","p":0.15},
		{"outcome":"$600 – $800","p":0.16},{"outcome":"$800 – $1,100","p":0.15},{"outcome":"≥ $1,100","p":0.20}],
		"summary":"We stay close to the ensemble. We expect TSM to compound with earnings rather than with its multiple. The main risk is a capex digestion or a Taiwan shock.",
		"weights":[{"role":"base_rate","weight":0.3,"why":"The reference class."},{"role":"quant","weight":0.45,"why":"Earnings drive the price."},{"role":"skeptic","weight":0.25,"why":"Tails."}],
		"methodNote":"We trusted the fundamentals model for the centre and the history for the width.",
		"scenarios":[{"kind":"base","name":"Compounding foundry leader","thesis":"EPS compounds from F1's FY2027 consensus.","mustBeTrue":["FY2032 EPS near $38 (F1, H1)"]},
			{"kind":"bear","name":"AI capex digestion","thesis":"Capex digestion cuts growth to 3%.","mustBeTrue":["Growth under 5% a year (assumption)"]}],
		"evidence":[{"claim":"TSM's measured daily history since its listing.","sourceId":"H1","source":"x","stance":"neutral","asOf":"training data"}],
		"confidence":"high"}`
	methodsStress = `{"events":[{"name":"Taiwan blockade or conflict","p":0.05,"move_pct":-60,"p_basis":"assumption","move_basis":"assumption"},
		{"name":"AI-capex bust","p":0.10,"move_pct":-35,"p_basis":"assumption","move_basis":"assumption"}]}`
)

// methodsCloses is years of weekday closes from a seeded GBM ending at last:
// synthetic, never a market's price.
func methodsCloses(seed uint64, years, mu, sigma, last float64) []float64 {
	r := rand.New(rand.NewPCG(seed, 3))
	n := int(years * 261)
	logs := make([]float64, n)
	for i := 1; i < n; i++ {
		logs[i] = logs[i-1] + mu/252 + sigma/math.Sqrt(252)*r.NormFloat64()
	}
	closes := make([]float64, n)
	for i, x := range logs {
		closes[i] = math.Round(last*math.Exp(x-logs[n-1])*100) / 100
	}
	return closes
}

// methodsYahoo serves a 29-year TSM listing ending at $485.12, the SOX and
// the Taiwan index the drift is capped by, in dollars at a constant Taiwan
// dollar rate (so its CAGR is the index's own).
func methodsYahoo(y *yahootest.Fake) {
	y.Set(yahootest.Daily("TSM", "USD", methodsCloses(1, 29, 0.10, 0.38, 485.12)...))
	y.Set(yahootest.Daily("^SOX", "USD", methodsCloses(2, 30, 0.09, 0.30, 5200)...))
	twii := methodsCloses(3, 30, 0.05, 0.22, 22000)
	y.Set(yahootest.Daily("^TWII", "TWD", twii...))
	fx := make([]float64, len(twii))
	for i := range fx {
		fx[i] = 0.031
	}
	y.Set(yahootest.Daily("TWDUSD=X", "USD", fx...))
}

// methodsScenarios are the quant's valuation scenarios: EPS cited to F1
// (whose claim states 21.33), the base case's P/E to S2 (which does not
// state 23), the rest labelled.
func methodsScenarios() string {
	y := time.Now().UTC().Year()
	years := fmt.Sprintf(`"eps_year":%d,"exit_year":%d`, y+1, y+6)
	return `{"scenarios":[
		{"name":"AI capex digestion","kind":"bear","p":0.3,"eps":21.33,` + years + `,"eps_basis":"F1","growth_pct":3,"growth_basis":"assumption","pe":14,"pe_basis":"assumption"},
		{"name":"Compounding foundry leader","kind":"base","p":0.5,"eps":21.33,` + years + `,"eps_basis":"F1","growth_pct":12,"growth_basis":"assumption","pe":23,"pe_basis":"S2"},
		{"name":"AI supercycle","kind":"bull","p":0.2,"eps":21.33,` + years + `,"eps_basis":"F1","growth_pct":20,"growth_basis":"model knowledge, as of 2025-06","pe":25,"pe_basis":"assumption"}]}`
}

// methodsMember answers a member: its first call names its tool, the one
// after the tool round gives probs and a stated median.
func methodsMember(r llmtest.Request, tool, input string, probs []float64, median float64) llmtest.Reply {
	if !r.AfterTools && tool != "" {
		return llmtest.Reply{Tools: []llmtest.ToolUse{{ID: tool[:2], Name: tool, Input: input}}}
	}
	return llmtest.Reply{Text: methodsOpinion(probs, median)}
}

func methodsOpinion(probs []float64, median float64) string {
	var ps []string
	for i, o := range methodsOutcomes {
		ps = append(ps, fmt.Sprintf(`{"outcome":%q,"p":%v}`, o, probs[i]))
	}
	return fmt.Sprintf(`{"probabilities":[%s],"median":%v,"reasoning":"From my method's numbers (H1).","keyFactors":["H1"],"confidence":"medium"}`, strings.Join(ps, ","), median)
}

// methodsPanel is how methodsRoute's panel answers: skip, the quant and
// the skeptic answer one distribution without their tools (a duplicate);
// fix, the quant's corrective retry then calls scenario_model; down, Yahoo
// is down (no H1). retries counts the quant's corrective retries.
type methodsPanel struct {
	skip, fix atomic.Bool
	down      bool
	retries   atomic.Int32
}

// methodsRoute answers the deep research stages, the panel and the draft.
func methodsRoute(t *testing.T, pn *methodsPanel) func(llmtest.Request) llmtest.Reply {
	same := []float64{.17, .18, .17, .16, .15, .17}
	return func(r llmtest.Request) llmtest.Reply {
		switch {
		case strings.HasPrefix(r.System, queryWriter):
			return noMarketQueries
		case strings.HasPrefix(r.System, "Plan the research"):
			if !strings.Contains(r.Prompt, `The "drivers" angle must find: consensus EPS`) || strings.Contains(r.System, "consensus EPS and revenue estimates") {
				t.Errorf("the plan hint belongs in the PLAN prompt: %.300s", r.Prompt)
			}
			return llmtest.Reply{Text: methodsPlan}
		case strings.HasPrefix(r.System, "Screen search results"):
			var scores []string
			for _, m := range candidateLine.FindAllStringSubmatch(r.Prompt, -1) {
				scores = append(scores, fmt.Sprintf(`{"id":%q,"score":3}`, m[1]))
			}
			return llmtest.Reply{Text: `{"scores":[` + strings.Join(scores, ",") + `]}`}
		case strings.HasPrefix(r.System, "Extract findings"):
			id := firstSource.FindStringSubmatch(r.Prompt)
			if id == nil {
				return llmtest.Reply{Status: 400}
			}
			return llmtest.Reply{Text: fmt.Sprintf(`{"findings":[{"claim":"Consensus EPS for FY2027 is $21.33 per ADR, per %[1]s.","sourceIds":["%[1]s"],
				"asOf":"2026-09-30","angle":"A1","direction":"higher","strength":"moderate"}]}`, id[1])}
		case strings.HasPrefix(r.System, "Review a forecasting desk's research ledger"):
			return llmtest.Reply{Text: `{"queries":[]}`}
		case strings.HasPrefix(r.System, "You are the Base-rate statistician"):
			if !r.AfterTools && !pn.down && (!strings.Contains(r.Prompt, "\nH1 · Measured history (tool data, safe to cite as H1): TSM (") ||
				!strings.Contains(r.System, "Method for this question: historical simulation")) {
				t.Errorf("the base rate without H1 or its brief: %.200s", r.System)
			}
			probs, median := []float64{.173, .185, .161, .158, .143, .180}, 580.0
			if pn.skip.Load() { // apart from the other two, so only they duplicate each other
				probs, median = []float64{.10, .12, .15, .20, .23, .20}, 750
			}
			return methodsMember(r, "history_stats", `{"symbol":"SOX","horizon_days":1826,"thresholds":[300,450,600,800,1100]}`, probs, median)
		case strings.HasPrefix(r.System, "You are the Market & quant analyst"):
			if !strings.Contains(r.System, "Your job is the fundamentals view") || strings.Contains(r.System, "these tools cannot price it") {
				t.Errorf("the quant's brief is the fundamentals brief: %.300s", r.System)
			}
			if strings.Contains(r.Prompt, "You did not use your method.") {
				pn.retries.Add(1)
				if !strings.Contains(r.Prompt, "Call scenario_model as your brief says") {
					t.Errorf("the retry names the quant's tool: %q", r.Prompt)
				}
				if pn.fix.Load() {
					return methodsMember(r, "scenario_model", methodsScenarios(), []float64{.135, .156, .144, .157, .163, .245}, 677)
				}
				return llmtest.Reply{Text: methodsOpinion(same, 570)}
			}
			if pn.skip.Load() {
				if r.AfterTools {
					return llmtest.Reply{Text: methodsOpinion([]float64{.135, .156, .144, .157, .163, .245}, 677)} // the fixed retry's answer
				}
				return llmtest.Reply{Text: methodsOpinion(same, 570)}
			}
			return methodsMember(r, "scenario_model", methodsScenarios(), []float64{.135, .156, .144, .157, .163, .245}, 677)
		case strings.HasPrefix(r.System, "You are the Narrative & macro skeptic"):
			if pn.skip.Load() {
				return llmtest.Reply{Text: methodsOpinion(same, 570)}
			}
			return methodsMember(r, "stress_mix", methodsStress, []float64{.217, .190, .156, .148, .130, .160}, 536)
		case strings.HasPrefix(r.System, "You are the aggregator"):
			if !strings.Contains(r.Prompt, "\nMethods (computed by the server, in the question's unit):\n") || !strings.Contains(r.System, "How to resolve the views") {
				t.Errorf("the aggregator without the Methods block: %.300s", r.Prompt)
			}
			return llmtest.Reply{Text: methodsAggregate}
		case strings.HasPrefix(r.System, "You are the question editor"):
			if strings.Contains(r.Prompt, "300-450") {
				return methodsDraft("< $300", "$300 – $450", "$450 – $600", "≥ $600")
			}
			if !strings.Contains(r.Prompt, "Live price: TSM (NYSE, Yahoo Finance) $485.12, regular-market price as of ") {
				t.Errorf("the draft quotes the listing: %q", r.Prompt)
			}
			return methodsDraft(methodsOutcomes...)
		}
		t.Errorf("unexpected model call: %.60q", r.System)
		return llmtest.Reply{Status: 400}
	}
}

func methodsDraft(outcomes ...string) llmtest.Reply {
	date := time.Now().UTC().AddDate(5, 0, 0).Format(time.DateOnly)
	return llmtest.Reply{Text: fmt.Sprintf(`{"shape":"ordered","question":"What will be the NYSE closing price of TSM ADR on %s?","outcomes":["%s"],
		"resolutionCriteria":"NYSE official close.","resolutionDate":%q,"category":"markets","priceRule":null}`, date, strings.Join(outcomes, `","`), date)}
}

// methodsQuestion creates the TSM question five years out and waits for its run.
func methodsQuestion(t *testing.T, c *client, note string) any {
	t.Helper()
	date := time.Now().UTC().AddDate(5, 0, 0).Format(time.DateOnly)
	id := str(t, c.call("POST", "/api/forecasts", map[string]any{"shape": "ordered",
		"question": "What will be the NYSE closing price of TSM ADR on " + date + "?" + note, "outcomes": methodsOutcomes,
		"resolutionCriteria": "The NYSE official closing price of TSM on the date.", "resolutionDate": date, "category": "markets"}, 201), "forecast.id")
	var f any
	eventually(t, 90*time.Second, "the forecast run", func() bool {
		f = c.call("GET", "/api/forecasts/"+id, nil, 200)
		return str(t, f, "forecast.status") != "running"
	})
	if str(t, f, "forecast.status") != "completed" {
		t.Fatalf("run: %v", f)
	}
	return f
}

const (
	viewKeys     = "scale unit horizonDays reference median p10 p25 p75 p90 pAbove changePct change annualPct flat headline edges lead basis"
	scenarioKeys = "kind name p modelP lo hi level vsRefPct annualPct inputs thesis mustBeTrue sourceIds"
)

// Round 9b's desk methods end to end on the owner's TSM question: with deep
// research and the methods on, the base rate reads H1 and the SOX history,
// the quant prices EPS × P/E scenarios (its F1 EPS verified, its S2 P/E
// relabelled an assumption), the skeptic prices two events; the result leads
// with the desk view against Yahoo's $485.12, its scenarios carry the tool's
// levels with odds from the final buckets, the summary loses its process
// sentence and H1 links the Yahoo history page. Then the duplicate panel
// (one corrective retry, with and without the tool), the long-horizon draft
// and Yahoo down.
func TestIntegrationForecastMethods(t *testing.T) {
	pn := &methodsPanel{}
	s := boot(t, options{brave: true, deep: true, methods: true, yahoo: methodsYahoo, model: methodsRoute(t, pn)})
	c := s.signIn(t)
	f := methodsQuestion(t, c, "")
	r := "forecast.result."
	shape(t, f, "forecast.result", resultKeys+" view scenarios methods measured")
	shape(t, f, r+"view", viewKeys)
	shape(t, f, r+"view.reference", "value label asOf source")
	shape(t, f, r+"methods", "plan weights weightsFrom pool agreement guard note")
	shapeAll(t, f, r+"methods.weights", "role method weight why")
	shapeAll(t, f, r+"scenarios", scenarioKeys)
	shape(t, f, r+"measured", "symbol name source currency from to sessions volAnnPct cagrPct maxDrawdown window url")
	shape(t, f, r+"specialists.1", "role name probabilities reasoning keyFactors confidence method anchor stats model") // the quant's valuation scenarios
	shape(t, f, r+"specialists.1.stats", "median p10 p90 pAbove statedMedian")
	if str(t, f, r+"view.headline") != "view" || num(t, f, r+"view.reference.value") != 485.12 || str(t, f, r+"view.reference.source") != "Yahoo Finance (delayed)" ||
		!strings.HasPrefix(str(t, f, r+"view.lead"), "Our median is $") || str(t, f, r+"view.basis.kind") != "scenarios" || isNull(t, f, r+"view.pAbove") {
		t.Fatalf("view %v", at(t, f, r+"view"))
	}
	if sum := str(t, f, r+"summary"); strings.Contains(sum, "ensemble") || !strings.HasPrefix(sum, "We expect TSM to compound") {
		t.Fatalf("summary %q", sum)
	}
	for i, want := range []string{"history", "fundamentals", "regime"} {
		p := fmt.Sprintf("%sspecialists.%d.", r, i)
		if str(t, f, p+"method") != want || len(list(t, f, p+"anchor")) != 6 || num(t, f, p+"stats.statedMedian") <= 0 {
			t.Errorf("member %d: %v %v", i, at(t, f, p+"method"), at(t, f, p+"stats"))
		}
	}
	pool := 0.0
	for _, o := range list(t, f, r+"methods.pool") {
		pool += num(t, o, "p")
	}
	if !near(pool, 1) || str(t, f, r+"methods.weightsFrom") != "desk" || str(t, f, r+"methods.plan") != "long_stock" || str(t, f, r+"methods.agreement") == "duplicate" {
		t.Fatalf("methods %v", at(t, f, r+"methods"))
	}
	// The scenarios' levels are the tool's (EPS × growth × P/E), the odds the final buckets'.
	scen, odds := list(t, f, r+"scenarios"), 0.0
	levels := map[string]float64{}
	for _, sc := range scen {
		odds += num(t, sc, "p")
		levels[str(t, sc, "kind")] = num(t, sc, "level")
	}
	base := 21.33 * math.Pow(1.12, 5) * 23
	if len(scen) != 4 || str(t, scen[0], "kind") != "tail" || math.Abs(odds-1) > 1e-6 || math.Abs(levels["base"]-base) > 0.01 || math.Abs(levels["bull"]-21.33*math.Pow(1.2, 5)*25) > 0.01 {
		t.Fatalf("scenarios %v", scen)
	}
	for _, sc := range scen {
		if str(t, sc, "kind") != "base" {
			continue
		}
		in := map[string]any{}
		for _, x := range list(t, sc, "inputs") {
			in[str(t, x, "field")] = x
		}
		if str(t, in["eps"], "basis") != "F1" || at(t, in["eps"], "verified") != true || str(t, in["pe"], "basis") != "assumption" || at(t, in["pe"], "verified") != false ||
			str(t, sc, "thesis") != "EPS compounds from F1's FY2027 consensus." || !slices.Equal(list(t, sc, "sourceIds"), []any{"F1", "H1"}) {
			t.Fatalf("base scenario %v", sc)
		}
	}
	measured := "https://finance.yahoo.com/quote/TSM/history"
	if !strings.HasPrefix(str(t, f, r+"measured.source"), "Yahoo Finance daily closes") || str(t, f, r+"measured.url") != measured {
		t.Fatalf("measured %v", at(t, f, r+"measured"))
	}
	h1 := false
	for _, e := range list(t, f, r+"evidence") {
		h1 = h1 || str(t, e, "url") == measured && strings.HasPrefix(str(t, e, "source"), "Yahoo Finance daily closes")
	}
	for _, fl := range list(t, f, r+"flags") {
		if str(t, fl, "type") == "duplicate" {
			t.Fatalf("no duplicate among three methods: %v", fl)
		}
	}
	if !h1 || s.count(t, "select credits from ai_usage where user_id = $1", c.id) != 5 || pn.retries.Load() != 0 || str(t, f, r+"confidence") != "high" {
		t.Fatalf("H1 evidence %v, %d retries", list(t, f, r+"evidence"), pn.retries.Load())
	}

	// The list row reads the view; the draft cuts a long horizon at multiples of today's price.
	for _, row := range list(t, c.call("GET", "/api/forecasts", nil, 200), "forecasts") {
		shape(t, row, "view", "median p10 p90 pAbove unit flat headline basis")
	}
	d := c.call("POST", "/api/forecasts/draft", map[string]any{"idea": "Where will TSM trade in 5 years?"}, 200)
	if got := list(t, d, "draft.outcomes"); fmt.Sprint(got) != "[< $250 $250 – $360 $360 – $485 $485 – $750 $750 – $1,000 $1,000 – $1,500 ≥ $1,500]" ||
		num(t, d, "reference.value") != 485.12 || str(t, d, "reference.source") != "Yahoo Finance (delayed)" {
		t.Fatalf("draft %v", d)
	}
	d = c.call("POST", "/api/forecasts/draft", map[string]any{"idea": "Will TSM be under 300, 300-450, 450-600 or above 600 in 5 years?"}, 200)
	if got, ref := list(t, d, "draft.outcomes"), d.(map[string]any)["reference"]; len(got) != 4 || got[1] != "$300 – $450" || ref != nil {
		t.Fatalf("the user's ranges: %v", d)
	}

	// A duplicate panel: the quant and the skeptic answer one distribution
	// without their tools while the base rate keeps its H1 anchor. The quant,
	// the first judgment member, is asked once to use scenario_model.
	pn.skip.Store(true)
	pn.fix.Store(true)
	f = methodsQuestion(t, c, " (retry)")
	if pn.retries.Load() != 1 || str(t, f, r+"specialists.1.method") != "fundamentals" || str(t, f, r+"specialists.2.method") != "judgment" ||
		str(t, f, r+"methods.agreement") == "duplicate" {
		t.Fatalf("a retry that uses the method: %d retries, %v", pn.retries.Load(), at(t, f, r+"methods"))
	}
	pn.fix.Store(false)
	f = methodsQuestion(t, c, " (duplicate)")
	dups := 0
	for _, fl := range list(t, f, r+"flags") {
		if str(t, fl, "type") == "duplicate" {
			dups++
			if !strings.HasPrefix(str(t, fl, "note"), "Market & quant analyst and Narrative & macro skeptic gave near-identical numbers without their own method's tool data") {
				t.Fatalf("flag %v", fl)
			}
		}
	}
	if pn.retries.Load() != 2 || dups != 1 || str(t, f, r+"methods.agreement") != "duplicate" || str(t, f, r+"confidence") != "medium" ||
		str(t, f, r+"specialists.1.method") != "judgment" {
		t.Fatalf("a retry that does not help: %d retries, %v %v", pn.retries.Load(), at(t, f, r+"methods"), at(t, f, r+"flags"))
	}
}

// Yahoo down: no live price and no history, so no measured block and no
// reference; every member's tool fails, the run completes, and the view
// gives the ranges only.
func TestIntegrationForecastMethodsYahooDown(t *testing.T) {
	s := boot(t, options{methods: true, yahoo: func(y *yahootest.Fake) {
		methodsYahoo(y)
		y.SetErr("TSM", &yahoo.UnavailableError{Symbol: "TSM", Reason: "HTTP 503"})
	}, model: methodsRoute(t, &methodsPanel{down: true})})
	f := methodsQuestion(t, s.signIn(t), "")
	r := "forecast.result."
	shape(t, f, "forecast.result", resultKeys+" view methods")
	if !isNull(t, f, r+"view.reference") || !isNull(t, f, r+"view.pAbove") || str(t, f, r+"view.basis.kind") != "none" ||
		!strings.HasPrefix(str(t, f, r+"view.lead"), "The desk's 50% range for ") {
		t.Fatalf("view %v", at(t, f, r+"view"))
	}
	for i := range 3 {
		if m := str(t, f, fmt.Sprintf("%sspecialists.%d.method", r, i)); m != "fallback" {
			t.Errorf("member %d: %s", i, m)
		}
	}
}

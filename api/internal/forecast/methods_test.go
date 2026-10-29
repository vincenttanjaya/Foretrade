package forecast

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo/yahootest"
)

// synthCloses is years of weekday closes from a seeded GBM ending at last:
// synthetic, never a market's price.
func synthCloses(seed uint64, years, mu, sigma, last float64) []float64 {
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

// flatCloses is n closes at v: a constant synthetic FX rate, so an index
// converted at it keeps its own CAGR (TSM's drift is capped by TWII in
// dollars, as the outlook card's).
func flatCloses(n int, v float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

// methodsEnv is a desk with the methods on and Yahoo serving a 29-year TSM
// listing ending at $485.12, the SOX and the Taiwan index (all synthetic),
// with the Taiwan dollar's FX history at a constant rate.
func methodsEnv(t *testing.T, route func(llmtest.Request) llmtest.Reply) *apptest.Env {
	e := apptest.New(t, Register, apptest.Options{Reply: route})
	e.ForecastMethods = true
	e.Yahoo.Set(yahootest.Daily("TSM", "USD", synthCloses(1, 29, 0.10, 0.38, 485.12)...))
	e.Yahoo.Set(yahootest.Daily("^SOX", "USD", synthCloses(2, 30, 0.09, 0.30, 5200)...))
	e.Yahoo.Set(yahootest.Daily("^TWII", "TWD", synthCloses(3, 30, 0.05, 0.22, 22000)...))
	e.Yahoo.Set(yahootest.Daily("TWDUSD=X", "USD", flatCloses(30*261, 0.031)...))
	return e
}

// tsmQuestion is the owner's question, resolving five years out on the
// stored report's six ranges.
func tsmQuestion() Draft {
	return Draft{Shape: "ordered", Question: "What will be the NYSE closing price of TSM ADR on " + fiveYears() + "?", Outcomes: tsmOutcomes,
		ResolutionCriteria: "The NYSE official closing price of TSM on the date.", ResolutionDate: fiveYears(), Category: "markets"}
}

func fiveYears() string { return todayUTC().AddDate(5, 0, 0).Format(time.DateOnly) }

// member answers a numeric-frame member: the first call names its tool, the
// one after the tool round answers with probs and a stated median.
func member(r llmtest.Request, tool, input string, probs []float64, median float64) llmtest.Reply {
	if !r.AfterTools && tool != "" {
		return llmtest.Reply{Tools: []llmtest.ToolUse{{ID: tool[:2], Name: tool, Input: input}}}
	}
	return llmtest.Reply{Text: specJSON(probs, median)}
}

func specJSON(probs []float64, median float64) string {
	var ps []string
	for i, o := range tsmOutcomes {
		ps = append(ps, fmt.Sprintf(`{"outcome":%q,"p":%v}`, o, probs[i]))
	}
	return fmt.Sprintf(`{"probabilities":[%s],"median":%v,"reasoning":"From my method's numbers (H1).","keyFactors":["H1"],"confidence":"medium"}`,
		strings.Join(ps, ","), median)
}

// The §6.1 scenarios, EPS × P/E: the years follow the resolution date.
func tsmScenarios() string {
	y := todayUTC().Year()
	years := fmt.Sprintf(`"eps_year":%d,"exit_year":%d`, y+1, y+6)
	return `{"scenarios":[
		{"name":"AI capex digestion","kind":"bear","p":0.3,"eps":21.33,` + years + `,"eps_basis":"S1","growth_pct":3,"growth_basis":"assumption","pe":14,"pe_basis":"assumption"},
		{"name":"Compounding foundry leader","kind":"base","p":0.5,"eps":21.33,` + years + `,"eps_basis":"S1","growth_pct":12,"growth_basis":"assumption","pe":20,"pe_basis":"model knowledge, as of 2025-06"},
		{"name":"AI supercycle","kind":"bull","p":0.2,"eps":21.33,` + years + `,"eps_basis":"S1","growth_pct":20,"growth_basis":"assumption","pe":25,"pe_basis":"assumption"}]}`
}

const tsmStress = `{"events":[{"name":"Taiwan blockade or conflict","p":0.05,"move_pct":-60,"p_basis":"assumption","move_basis":"assumption"},
	{"name":"AI-capex bust","p":0.10,"move_pct":-35,"p_basis":"assumption","move_basis":"assumption"}]}`

const tsmAggregator = `{"probabilities":[{"outcome":"< $300","p":0.17},{"outcome":"$300 – $450","p":0.17},{"outcome":"$450 – $600","p":0.15},
	{"outcome":"$600 – $800","p":0.16},{"outcome":"$800 – $1,100","p":0.15},{"outcome":"≥ $1,100","p":0.20}],
	"summary":"We stay close to the ensemble. We expect TSM to compound with earnings rather than with its multiple. The main risk is a capex digestion or a Taiwan shock.",
	"weights":[{"role":"base_rate","weight":0.3,"why":"The reference class."},{"role":"quant","weight":0.45,"why":"Earnings drive the price."},{"role":"skeptic","weight":0.25,"why":"Tails."}],
	"methodNote":"We trusted the fundamentals model for the centre and the history for the width.",
	"scenarios":[{"kind":"base","name":"Compounding foundry leader","thesis":"EPS compounds near 12% a year (H1, F9).","mustBeTrue":["FY2032 EPS near $38 at about 20x"]},
		{"kind":"tail","name":"Taiwan","thesis":"A blockade halts the fabs.","mustBeTrue":["A blockade before the date (assumption)"]}],
	"flags":[{"type":"duplicate","note":"All three used the same lognormal."}],
	"evidence":[{"claim":"TSM's measured history since its listing.","sourceId":"H1","source":"x","stance":"neutral","asOf":"training data"}],
	"confidence":"high"}`

// tsmRoute answers the TSM run's stages: the base rate reads SOX history,
// the quant runs the valuation scenarios, the skeptic prices two events.
func tsmRoute(t *testing.T, prompts *atomic.Value) func(llmtest.Request) llmtest.Reply {
	return func(r llmtest.Request) llmtest.Reply {
		if prompts != nil && strings.HasPrefix(r.System, "You are the aggregator") {
			prompts.Store(r.Prompt)
		}
		switch {
		case strings.HasPrefix(r.System, "You are the Base-rate statistician"):
			return member(r, "history_stats", `{"symbol":"SOX","horizon_days":1825,"thresholds":[300,450,600,800,1100]}`, []float64{.173, .185, .161, .158, .143, .180}, 580)
		case strings.HasPrefix(r.System, "You are the Market & quant analyst"):
			return member(r, "scenario_model", tsmScenarios(), []float64{.135, .156, .144, .157, .163, .245}, 677)
		case strings.HasPrefix(r.System, "You are the Narrative & macro skeptic"):
			return member(r, "stress_mix", tsmStress, []float64{.217, .190, .156, .148, .130, .160}, 536)
		case strings.HasPrefix(r.System, "You are the aggregator"):
			return llmtest.Reply{Text: tsmAggregator}
		}
		t.Errorf("unexpected model call: %.80s", r.System)
		return llmtest.Reply{Status: 500}
	}
}

// TestNumericRunTSM: the owner's TSM question with the methods on. Each
// member runs its own method's tool and the server records the method and
// its anchor; the result leads with the desk view against today's measured
// $485.12, the scenarios carry the tool's levels with odds from the final
// buckets, the summary loses its process sentence, and H1 links Yahoo.
func TestNumericRunTSM(t *testing.T) {
	var agg atomic.Value
	e := methodsEnv(t, tsmRoute(t, &agg))
	f := waitRun(t, e, create(t, e, tsmQuestion()))
	if f.Status != "completed" || f.Result == nil {
		t.Fatalf("run: %s %+v", f.Status, f.Error)
	}
	r := f.Result
	methods := map[string]string{}
	for _, s := range r.Specialists {
		methods[s.Role] = s.Method
		if len(s.Anchor) != 6 || s.Stats == nil || s.Stats.PAbove == nil || s.Stats.StatedMedian == nil {
			t.Errorf("%s: anchor %v stats %+v", s.Role, s.Anchor, s.Stats)
		}
	}
	if methods["base_rate"] != "history" || methods["quant"] != "fundamentals" || methods["skeptic"] != "regime" {
		t.Fatalf("methods %v", methods)
	}
	q := r.Specialists[1]
	if len(q.Model) != 3 || !within(q.Model[1].Price, 751.8, 0.1) || q.Model[0].Inputs[0].Basis != "assumption" || q.Model[1].Inputs[2].Kind != "model_knowledge" {
		t.Fatalf("valuation model %+v", q.Model)
	}
	v := r.View
	if v == nil || v.Reference == nil || v.Reference.Value != 485.12 || v.Reference.Source != "Yahoo Finance (delayed)" || v.Headline != "view" || v.Derived ||
		!strings.HasPrefix(v.Lead, "Our median is $") || !strings.Contains(v.Lead, "from today's $485") || v.PAbove == nil {
		t.Fatalf("view %+v", v)
	}
	if strings.Contains(r.Summary, "ensemble") || !strings.HasPrefix(r.Summary, "We expect TSM to compound") {
		t.Fatalf("summary %q", r.Summary)
	}
	m := r.Methods
	if m == nil || m.Plan != "long_stock" || m.WeightsFrom != "desk" || m.Agreement == "duplicate" || len(m.Weights) != 3 || len(m.Pool) != 6 ||
		!strings.HasPrefix(m.Note, "We trusted the fundamentals model") {
		t.Fatalf("methods %+v", m)
	}
	sum := 0.0
	for _, p := range m.Pool {
		sum += p.P
	}
	if !within(sum, 1, 1e-9) {
		t.Fatalf("pool sums to %v", sum)
	}
	if len(r.Scenarios) != 4 || r.Scenarios[0].Kind != "tail" || r.Scenarios[0].Name != "Taiwan blockade or conflict" || !within(r.Scenarios[2].Level, 751.8, 0.1) ||
		r.Scenarios[2].Thesis != "EPS compounds near 12% a year (H1, F9)." || !slices.Equal(r.Scenarios[2].SourceIDs, []string{"H1"}) || r.Scenarios[0].Thesis != "A blockade halts the fabs." {
		t.Fatalf("scenarios %+v", r.Scenarios)
	}
	sum = 0
	for _, s := range r.Scenarios {
		sum += s.P
	}
	if !within(sum, 1, 1e-6) {
		t.Fatalf("scenario odds sum to %v", sum)
	}
	if r.Measured == nil || !strings.HasPrefix(r.Measured.Source, "Yahoo Finance daily closes") || r.Measured.URL == nil ||
		*r.Measured.URL != "https://finance.yahoo.com/quote/TSM/history" || r.Measured.Window == nil {
		t.Fatalf("measured %+v", r.Measured)
	}
	if len(r.Evidence) != 1 || r.Evidence[0].URL == nil || *r.Evidence[0].URL != *r.Measured.URL || r.Evidence[0].Source != r.Measured.Source {
		t.Fatalf("H1 evidence %+v", r.Evidence)
	}
	if slices.ContainsFunc(r.Flags, func(f Flag) bool { return f.Type == "duplicate" }) {
		t.Fatalf("the model's lognormal duplicate flag is the server's call: %+v", r.Flags)
	}
	if !strings.Contains(r.KnowledgeNote, " + Yahoo daily history (TSM since ") || strings.Contains(r.KnowledgeNote, "Binance") {
		t.Fatalf("knowledge note %q", r.KnowledgeNote)
	}
	prompt, _ := agg.Load().(string)
	for _, want := range []string{"Methods (computed by the server, in the question's unit):", "· history (H1 blend; history_stats: SOX) · prior weight 0.30",
		"· fundamentals (scenario_model: bear $346 30%, base $752 50%, bull $1,330 20%) · prior weight 0.45", "· regime (stress_mix: Taiwan blockade or conflict 5% -60%;",
		"Prior-weighted pool:", "Measured-history reference (H1 windows of ", " days applied to today's price, each scaled to the capped drift): 80% range $", "H1 · Measured history (tool data, safe to cite as H1): TSM (NYSE: TSM ADS = 5 TWSE 2330 shares).",
		"- TSM (TSMC (Taiwan Semiconductor)): $485.12, NYSE regular-market price via Yahoo Finance (delayed), as of "} {
		if !strings.Contains(prompt, want) {
			t.Errorf("aggregator prompt lacks %q", want)
		}
	}
	for _, req := range e.Bedrock.Requests() {
		if strings.HasPrefix(req.System, "You are the Market & quant") && (strings.Contains(req.System, "these tools cannot price it") || !strings.Contains(req.System, "Your job is the fundamentals view")) {
			t.Fatal("the quant's brief is the fundamentals brief")
		}
		if strings.HasPrefix(req.System, "You are the aggregator") && !strings.Contains(req.System, "How to resolve the views") {
			t.Fatal("the aggregator gets the numeric combine rules")
		}
	}
	b, _ := json.Marshal(f)
	if credits(t, e) != 5 || !strings.Contains(string(b), `"method":"history"`) {
		t.Fatalf("credits %d", credits(t, e))
	}
}

// firstText is the conversation's first user text: the run's brief.
func firstText(r llmtest.Request) string {
	for _, m := range r.Messages {
		for _, b := range m.Content {
			if txt, ok := b["text"].(string); ok && m.Role == "user" {
				return txt
			}
		}
	}
	return ""
}

func retried(r llmtest.Request) bool {
	return strings.Contains(r.Prompt, "You did not use your method.")
}

// TestNumericRunDuplicateRetry: the quant and the skeptic answer the same
// numbers without their tools while the base rate keeps its H1 anchor: a
// duplicate, so the quant (the first judgment member) is asked once more to
// use scenario_model. When it does, its method is fundamentals and the
// duplicate is gone; when it does not, the server flags the two as one view
// and caps the confidence.
func TestNumericRunDuplicateRetry(t *testing.T) {
	same := []float64{.17, .18, .17, .16, .15, .17}
	var retries atomic.Int32
	e := methodsEnv(t, func(r llmtest.Request) llmtest.Reply {
		fixes := strings.Contains(firstText(r), "(fixed)")
		switch {
		case strings.HasPrefix(r.System, "You are the Base-rate statistician"):
			return member(r, "history_stats", `{"symbol":"SOX","horizon_days":1825,"thresholds":[300,450,600,800,1100]}`, []float64{.10, .12, .15, .20, .23, .20}, 750)
		case strings.HasPrefix(r.System, "You are the Market & quant analyst") && retried(r):
			retries.Add(1)
			if !fixes {
				return llmtest.Reply{Text: specJSON(same, 570)}
			}
			return member(r, "scenario_model", tsmScenarios(), []float64{.135, .156, .144, .157, .163, .245}, 677)
		case strings.HasPrefix(r.System, "You are the Market & quant analyst") && r.AfterTools:
			return llmtest.Reply{Text: specJSON([]float64{.135, .156, .144, .157, .163, .245}, 677)}
		case strings.HasPrefix(r.System, "You are the Market & quant analyst"), strings.HasPrefix(r.System, "You are the Narrative & macro skeptic"):
			return llmtest.Reply{Text: specJSON(same, 570)} // no tool call
		case strings.HasPrefix(r.System, "You are the aggregator"):
			return llmtest.Reply{Text: tsmAggregator}
		}
		t.Errorf("unexpected model call: %.80s", r.System)
		return llmtest.Reply{Status: 500}
	})
	fixed := tsmQuestion()
	fixed.Question += " (fixed)"
	f := waitRun(t, e, create(t, e, fixed))
	r := f.Result
	if f.Status != "completed" || retries.Load() != 1 || r.Specialists[1].Method != "fundamentals" || r.Specialists[2].Method != "judgment" ||
		r.Methods.Agreement == "duplicate" || len(r.Specialists[1].Model) != 3 {
		t.Fatalf("a retry that uses the method: %d retries, %+v", retries.Load(), r.Methods)
	}
	for _, fl := range r.Flags {
		if fl.Type == "duplicate" {
			t.Fatalf("no duplicate after the retry: %+v", fl)
		}
	}
	f = waitRun(t, e, create(t, e, tsmQuestion()))
	r = f.Result
	dups := 0
	for _, fl := range r.Flags {
		if fl.Type == "duplicate" {
			dups++
			if !strings.HasPrefix(fl.Note, "Market & quant analyst and Narrative & macro skeptic gave near-identical numbers without their own method's tool data") {
				t.Fatalf("flag %q", fl.Note)
			}
		}
	}
	if retries.Load() != 2 || r.Specialists[1].Method != "judgment" || r.Methods.Agreement != "duplicate" || dups != 1 || r.Confidence != "medium" {
		t.Fatalf("a retry that does not help: %d retries, %s, %+v %+v", retries.Load(), r.Confidence, r.Methods, r.Flags)
	}
	if w := r.Methods.Weights; !within(w[1].Weight+w[2].Weight, 0.45/(0.30+0.45), 1e-9) { // one view at the larger weight
		t.Fatalf("duplicates count once: %+v", w)
	}
}

// TestNumericRunYahooDown: no live price and no history; every tool says
// so, the run completes with no measured block, no reference and a view
// that states no median.
func TestNumericRunYahooDown(t *testing.T) {
	e := methodsEnv(t, tsmRoute(t, nil))
	e.Yahoo.SetErr("TSM", yahooDown("TSM"))
	f := waitRun(t, e, create(t, e, tsmQuestion()))
	r := f.Result
	if f.Status != "completed" || r.Measured != nil || r.View == nil || r.View.Reference != nil || r.View.Basis.Kind != "none" || r.Scenarios != nil ||
		!strings.HasPrefix(r.View.Lead, "The desk's 50% range") {
		t.Fatalf("Yahoo down: %+v %+v", r.View, r.Measured)
	}
	for _, s := range r.Specialists {
		if s.Method != "fallback" || s.Anchor != nil {
			t.Errorf("%s: %s %v", s.Role, s.Method, s.Anchor)
		}
	}
	for _, q := range e.Bedrock.Requests() {
		if strings.HasPrefix(q.System, "You are the Base-rate") && !strings.Contains(q.System, "The subject's own history is unavailable this run") {
			t.Fatal("the base rate is told the history is unavailable")
		}
	}
}

// TestNumericRunCryptoLong: a two-year BTC question. The quant's long
// price_outlook is the volatility-only baseline without LongOutlook's
// uncapped drift case; its method is volatility.
func TestNumericRunCryptoLong(t *testing.T) {
	outs := []string{"< $60,000", "$60,000 – $100,000", "$100,000 – $150,000", "≥ $150,000"}
	ans := func(median float64) string {
		return fmt.Sprintf(`{"probabilities":[{"outcome":%q,"p":0.2},{"outcome":%q,"p":0.3},{"outcome":%q,"p":0.3},{"outcome":%q,"p":0.2}],"median":%v,"reasoning":"x","keyFactors":["x"],"confidence":"low"}`,
			outs[0], outs[1], outs[2], outs[3], median)
	}
	date := todayUTC().AddDate(2, 0, 0).Format(time.DateOnly)
	var outlook atomic.Value
	e := methodsEnv(t, func(r llmtest.Request) llmtest.Reply {
		switch {
		case strings.HasPrefix(r.System, "You are the Market & quant analyst") && !r.AfterTools:
			return llmtest.Reply{Tools: []llmtest.ToolUse{{ID: "po", Name: "price_outlook", Input: `{"symbol":"BTC","horizon_days":730,"thresholds":[60000,100000,150000]}`}}}
		case strings.HasPrefix(r.System, "You are the Market & quant analyst"):
			outlook.Store(string(r.Body))
			return llmtest.Reply{Text: ans(100000)}
		case strings.HasPrefix(r.System, "You are the"):
			return llmtest.Reply{Text: ans(95000)}
		}
		return llmtest.Reply{Status: 500}
	})
	f := waitRun(t, e, create(t, e, Draft{Shape: "ordered", Question: "Where will the Binance BTC/USDT daily close be on " + date + "?", Outcomes: outs,
		ResolutionDate: date, Category: "crypto"}))
	r := f.Result
	if f.Status != "completed" || r.Specialists[1].Method != "volatility" || len(r.Specialists[1].Anchor) != 4 || r.Methods.Plan != "long_crypto" ||
		r.Specialists[0].Method != "history" || r.Measured == nil || r.Measured.URL != nil || r.View.Reference == nil || r.View.Reference.Source != "Binance spot" {
		t.Fatalf("BTC: %+v %+v %+v", r.Specialists, r.Methods, r.Measured)
	}
	body, _ := outlook.Load().(string)
	if !strings.Contains(body, `\"longHorizon\"`) || strings.Contains(body, `\"driftCase\":{`) || !strings.Contains(body, `\"horizonDays\":730`) {
		t.Fatalf("price_outlook result: %.2000s", body)
	}
	if !strings.Contains(r.KnowledgeNote, "live Binance spot data") || !strings.Contains(r.KnowledgeNote, "Binance daily history (BTC since") {
		t.Fatalf("knowledge note %q", r.KnowledgeNote)
	}
}

// TestNumericRunLevel: a question with no instrument (US CPI) is a level
// frame: no tools, no measured block, judgment members, the prior weights
// for levels, and a view that states only its ranges.
func TestNumericRunLevel(t *testing.T) {
	outs := []string{"< 2%", "2% – 2.5%", "2.5% – 3%", "≥ 3%"}
	reply := `{"probabilities":[{"outcome":"< 2%","p":0.2},{"outcome":"2% – 2.5%","p":0.4},{"outcome":"2.5% – 3%","p":0.3},{"outcome":"≥ 3%","p":0.1}],"median":"2.4%","reasoning":"x","keyFactors":["x"],"confidence":"low"}`
	e := methodsEnv(t, func(r llmtest.Request) llmtest.Reply {
		if strings.HasPrefix(r.System, "You are the") {
			if r.Stream {
				t.Error("a level frame's members have no tools")
			}
			return llmtest.Reply{Text: reply}
		}
		return llmtest.Reply{Status: 500}
	})
	date := todayUTC().AddDate(0, 5, 0).Format(time.DateOnly)
	f := waitRun(t, e, create(t, e, Draft{Shape: "ordered", Question: "What will US CPI inflation (YoY) be in the release due by " + date + "?", Outcomes: outs,
		ResolutionDate: date, Category: "macro"}))
	r := f.Result
	if f.Status != "completed" || r.Measured != nil || r.Methods == nil || r.Methods.Plan != "short" || r.Methods.WeightsFrom != "prior" || r.View.Scale != "log" || // every edge above 0

		r.View.Unit.Suffix != "%" || r.View.Basis.Kind != "none" || r.Specialists[0].Method != "judgment" || r.Specialists[0].Stats.StatedMedian == nil ||
		*r.Specialists[0].Stats.StatedMedian != 2.4 || r.Methods.Agreement != "duplicate" {
		t.Fatalf("level: %+v %+v", r.Methods, r.View)
	}
	// A level frame gives its members no tools by design: the flag says so, not that they skipped their tools (gap round G21).
	want := "Base-rate statistician, Market & quant analyst and Narrative & macro skeptic gave near-identical numbers from the same research, " +
		"with no model of their own, so they count as one view (largest gap 0 points)."
	if !slices.ContainsFunc(r.Flags, func(f Flag) bool { return f.Type == "duplicate" && f.Note == want }) {
		t.Fatalf("level duplicate flag: %+v", r.Flags)
	}
}

func yahooDown(sym string) error { return &yahoo.UnavailableError{Symbol: sym, Reason: "HTTP 503"} }

// An update's change numbers come from the server (gap round G20): a numeric
// changeNote loses the sentence that states levels (written before the
// server's checks), and viewChange reads the change off the two stored views.
func TestUpdateViewChange(t *testing.T) {
	var aggs atomic.Int32
	route := tsmRoute(t, nil)
	e := methodsEnv(t, func(r llmtest.Request) llmtest.Reply {
		rep := route(r)
		if strings.HasPrefix(r.System, "You are the aggregator") && aggs.Add(1) == 2 {
			rep.Text = `{"changeNote":"The median rises from $570 to about $650 on stronger guidance. Guidance improved after the capex call.",` + strings.TrimPrefix(tsmAggregator, "{")
		}
		return rep
	})
	first := waitRun(t, e, create(t, e, tsmQuestion()))
	w := e.Do("POST", "/api/forecasts/"+first.ID+"/update", "{}")
	if w.Code != 201 {
		t.Fatalf("update: %d %s", w.Code, w.Body)
	}
	u := waitRun(t, e, apptest.JSON[forecastBody](t, w).Forecast.ID)
	if u.Status != "completed" || u.Result.ChangeNote != "Guidance improved after the capex call." {
		t.Fatalf("update: %s %q", u.Status, u.Result.ChangeNote)
	}
	want := viewChange(first.Result.View, u.Result.View)
	if !strings.HasPrefix(want, "Median $611 → $611 (+0.0%) · chance at or above today ") || u.Result.ViewChange != want {
		t.Fatalf("viewChange %q, want %q", u.Result.ViewChange, want)
	}
	var prompt string
	for _, q := range e.Bedrock.Requests() {
		if strings.HasPrefix(q.System, "You are the aggregator") {
			prompt = q.Prompt + q.System
		}
	}
	if !strings.Contains(prompt, "the reasons the estimate moved, in words: no levels, ranges or percentages (the page prints the change)") {
		t.Fatal("the numeric prompts ask for the reasons only")
	}
}

// The change line and the changeNote lint (gap round G20).
func TestViewChangeWords(t *testing.T) {
	prev := deskView(tsmDraft(), probsAt(tsmOutcomes, .20, .19, .17, .16, .14, .14), tsmRef, 1825, ViewBasis{Kind: "capped_drift", Label: "x"})
	next := deskView(tsmDraft(), probsAt(tsmOutcomes, .17, .17, .15, .16, .15, .20), tsmRef, 1825, ViewBasis{Kind: "capped_drift", Label: "x"})
	if got := viewChange(prev, next); !strings.HasPrefix(got, "Median $") || !strings.Contains(got, " → $611 (+") || !strings.Contains(got, "· chance at or above today ") {
		t.Fatalf("anchored: %q", got)
	}
	none := deskView(tsmDraft(), probsAt(tsmOutcomes, .17, .17, .15, .16, .15, .20), nil, 1825, noBasis)
	if got := viewChange(prev, none); !strings.HasPrefix(got, "80% range ") || strings.Contains(got, "Median") {
		t.Fatalf("no basis: %q", got)
	}
	if viewChange(nil, next) != "" || viewChange(prev, nil) != "" {
		t.Fatal("no view, no change")
	}
	for in, want := range map[string]string{
		"The median rises from $570 to about $650 on stronger guidance. Guidance improved.": "Guidance improved.",
		"No material change.":                           "No material change.",
		"Odds rose 5 points. The capex call went well.": "Odds rose 5 points. The capex call went well.",
		"The chance rose to 62%.":                       "",
		"Spreads widened 25 bp. Rates fell.":            "Rates fell.",
	} {
		if got := changeWords(in, Unit{Prefix: "$"}); got != want {
			t.Errorf("changeWords(%q) = %q, want %q", in, got, want)
		}
	}
	if got := changeWords("CPI eases to 2.4%. Energy fell.", Unit{Suffix: "%"}); got != "Energy fell." {
		t.Errorf("a %% unit: %q", got)
	}
}

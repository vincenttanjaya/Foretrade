package forecast

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo/yahootest"
)

// tsmKit is the TSM question's tool kit over a synthetic measured history,
// with a finding and two sources to verify inputs against (one with a deep
// research page) and a finding read from the user's material.
func tsmKit(t *testing.T) *deskKit {
	h, yh := measureEnv(t)
	yh.Set(yahootest.Daily("TSM", "USD", synthCloses(1, 29, 0.10, 0.38, 485.12)...))
	yh.Set(yahootest.Daily("^SOX", "USD", synthCloses(2, 30, 0.09, 0.30, 5200)...))
	yh.Set(yahootest.Daily("^TWII", "TWD", synthCloses(3, 30, 0.05, 0.22, 22000)...))
	yh.Set(yahootest.Daily("TWDUSD=X", "USD", flatCloses(30*261, 0.031)...))
	d := tsmDraft()
	fr := frameOf(d, frameDay)
	return &deskKit{h: h, d: d, fr: fr, m: h.measure(context.Background(), fr, d, frameDay), logs: map[string]*toolLog{},
		findings: []Finding{{ID: "F1", Claim: "Consensus EPS is $16.56 for this year and $21.33 for next (2026-10-01).", SourceIDs: []string{"S1"}},
			{ID: "F2", Claim: "The user's notes say TSM trades at 29.3x.", SourceIDs: []string{"D1"}, Origin: "material"}},
		sources: []Source{{ID: "S1", Title: "TSMC estimates", text: "Analysts see a forward P/E of 22x.", page: "Over a decade the stock traded between 12x and 25x earnings."},
			{ID: "S2", Title: "TSMC news", text: "No figures here."}}}
}

func run(t *testing.T, tool llm.Tool, input string) (map[string]any, error) {
	t.Helper()
	out, err := tool.Run(context.Background(), json.RawMessage(input))
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("%s: %v in %s", tool.Name, err, out)
	}
	return m, nil
}

func sumP(t *testing.T, v any) float64 {
	t.Helper()
	s := 0.0
	for _, b := range v.([]any) {
		s += b.(map[string]any)["p"].(float64)
	}
	return s
}

// TestNumericTools: history_stats reads only the subject, universe symbols
// and benchmarks, at most four times, and maps windows onto today's price;
// scenario_model verifies each cited input against the run's texts and
// keeps the last success; stress_mix caps its events and needs a base.
func TestNumericTools(t *testing.T) {
	k := tsmKit(t)
	log := newToolLog()
	hist := k.historyTool(log)
	enum := hist.Schema["properties"].(map[string]any)["symbol"].(map[string]any)["enum"].([]string)
	for _, s := range []string{"TSM", "SOX", "SPX", "UST10Y", "BTC", "NVDA"} {
		if !slices.Contains(enum, s) {
			t.Errorf("history_stats cannot read %s", s)
		}
	}
	if _, err := run(t, hist, `{"symbol":"GME","horizon_days":1825}`); err == nil || !strings.Contains(err.Error(), "unknown symbol") {
		t.Fatalf("an arbitrary ticker: %v", err)
	}
	out, err := run(t, hist, `{"symbol":"sox","horizon_days":1825,"thresholds":[300,450,600,800,1100]}`)
	if err != nil || !within(sumP(t, out["buckets"]), 1, 1e-9) || out["level"].(float64) != 485.12 || out["lognormal"] == nil ||
		!within(sumP(t, out["lognormal"].(map[string]any)["buckets"]), 1, 1e-9) || len(log.hist) != 6 || !slices.Equal(log.symbols, []string{"SOX"}) {
		t.Fatalf("SOX windows on TSM's price: %v %v", out, err)
	}
	if !strings.Contains(out["lognormal"].(map[string]any)["label"].(string), "halved and capped (an assumption)") {
		t.Fatalf("the lognormal block is labelled: %v", out["lognormal"])
	}
	// No uncapped drift reaches the desk: the history's own halved drift is left out, and windows that grew faster
	// than the capped drift are scaled down to it before they are mapped onto the ranges.
	hist5 := out["history"].(map[string]any)
	if hist5["driftHalvedPct"] != nil {
		t.Fatalf("the uncapped halved drift: %v", hist5["driftHalvedPct"])
	}
	w := hist5["window"].(map[string]any)["q"].([]any)[3].(float64)
	drift := out["lognormal"].(map[string]any)["driftAnnPct"].(float64)
	if sc, ok := out["windowScale"].(float64); !ok || !within(sc*w, math.Pow(1+drift/100, 1825.0/365), 1e-9) || !strings.Contains(out["windowNote"].(string), "scaled") {
		t.Fatalf("SOX windows (median ×%v) at the capped drift %v%%: scale %v %v", w, drift, out["windowScale"], out["windowNote"])
	}
	if _, err := run(t, hist, `{"symbol":"TSM","horizon_days":1825,"thresholds":[1e12],"level":{"value":1.2e12,"basis":"S2"}}`); err == nil ||
		!strings.Contains(err.Error(), "level must cite a source that states it") {
		t.Fatalf("an unsourced level: %v", err)
	}
	if _, err := run(t, hist, `{"symbol":"TSM","horizon_days":1825}`); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, hist, `{"symbol":"TSM","horizon_days":1825}`); !errors.Is(err, errToolLimit) {
		t.Fatalf("the fifth call: %v", err)
	}
	if m := log.method("base_rate", false); m != "history" || log.ok["history_stats"] != 2 || log.failed["history_stats"] != 2 {
		t.Fatalf("record: %s %v %v", m, log.ok, log.failed)
	}

	// A history shorter than the horizon plus a year has no windows to scale to the capped drift: no shares at all
	// (the live check's BTC at 2,800 days read "100% ≥ $660,000", its own uncapped return, and all zeros at 10 years),
	// one line why, and the lognormal reference alone, which is also the member's record.
	k.h.d.Yahoo.(*yahootest.Fake).Set(yahootest.Daily("NVDA", "USD", synthCloses(5, 8.2, 0.25, 0.55, 180)...))
	short := newToolLog()
	for _, days := range []int{2800, 3650} {
		out, err := run(t, k.historyTool(short), fmt.Sprintf(`{"symbol":"NVDA","horizon_days":%d,"thresholds":[300,450,600,800,1100]}`, days))
		note, _ := out["note"].(string)
		if err != nil || out["buckets"] != nil || out["independent"] != nil || out["windowScale"] != nil || out["level"].(float64) != 485.12 ||
			note != fmt.Sprintf("no window shares: NVDA's history (8.2 years) is too short for %s-day windows, which need the horizon plus a year of history", groupDigits(fmt.Sprint(days))) {
			t.Fatalf("NVDA's 8.2 years at %d days: %v %v", days, out, err)
		}
		logn := out["lognormal"].(map[string]any)["buckets"]
		if !within(sumP(t, logn), 1, 1e-9) || len(short.hist) != 6 || !within(short.hist[5], logn.([]any)[5].(map[string]any)["p"].(float64), 1e-9) {
			t.Fatalf("the lognormal is the reference and the record: %v %v", logn, short.hist)
		}
	}
	if out, err := run(t, k.historyTool(short), `{"symbol":"NVDA","horizon_days":1000,"thresholds":[300,450,600,800,1100]}`); err != nil ||
		!within(sumP(t, out["buckets"]), 1, 1e-9) || out["independent"].(float64) < 2 || out["note"] != nil {
		t.Fatalf("NVDA at 1,000 days has its windows: %v %v", out, err)
	}

	// scenario_model: each cited input checked against what its id states.
	qlog := newToolLog()
	scen := k.scenarioTool(qlog)
	y := frameDay.Year()
	in := func(pe float64, peBasis, growthBasis string) string {
		b, _ := json.Marshal(map[string]any{"scenarios": []map[string]any{
			{"name": "Digestion", "kind": "bear", "p": 0.3, "eps": 21.33, "eps_year": y + 1, "eps_basis": "F1", "growth_pct": 3, "exit_year": y + 6, "growth_basis": growthBasis, "pe": 14, "pe_basis": "assumption"},
			{"name": "Compounding", "kind": "base", "p": 0.5, "eps": 21.33, "eps_year": y + 1, "eps_basis": "F1", "growth_pct": 12, "exit_year": y + 6, "growth_basis": "model knowledge, as of 2025-06", "pe": pe, "pe_basis": peBasis},
			{"name": "Supercycle", "kind": "bull", "p": 0.2, "eps": 21.33, "eps_year": y + 1, "eps_basis": "[F1]", "growth_pct": 20, "exit_year": y + 6, "growth_basis": "assumption", "pe": 29.3, "pe_basis": "F2"},
		}})
		return string(b)
	}
	out, err = run(t, scen, in(25, "S1", "F99"))
	if err != nil {
		t.Fatal(err)
	}
	inputs := func(i int) []any { return out["scenarios"].([]any)[i].(map[string]any)["inputs"].([]any) }
	field := func(i, j int, key string) any { return inputs(i)[j].(map[string]any)[key] }
	switch {
	case field(0, 0, "basis") != "F1" || field(0, 0, "verified") != true: // the finding states 21.33
		t.Errorf("EPS from F1: %v", inputs(0))
	case field(0, 1, "basis") != "assumption" || field(0, 1, "kind") != "assumption": // F99 is no id of this run
		t.Errorf("unknown id: %v", inputs(0))
	case field(1, 2, "basis") != "S1" || field(1, 2, "verified") != true: // 25x is in S1's read page
		t.Errorf("P/E from S1's page: %v", inputs(1))
	case field(1, 1, "kind") != "model_knowledge" || field(1, 1, "verified") != false:
		t.Errorf("model knowledge: %v", inputs(1))
	case field(2, 2, "basis") != "assumption": // the user's material never verifies a number
		t.Errorf("P/E from the user's material: %v", inputs(2))
	case field(2, 0, "basis") != "F1":
		t.Errorf("a bracketed id: %v", inputs(2))
	}
	if w := strings.Join(toStrings(out["warnings"]), " | "); !strings.Contains(w, "growthPct 3 cited F99 is not an id of this run's research: labelled assumption") ||
		!strings.Contains(w, "pe 29.3 cited F2 is not an id of this run's research: labelled assumption") {
		t.Errorf("warnings %q", w)
	}
	if !within(sumP(t, out["buckets"]), 1, 1e-9) || !strings.HasPrefix(out["volBasis"].(string), "H1 full-history ") || out["reverse"].([]any)[1].(map[string]any)["impliedGrowthPct"] == nil {
		t.Fatalf("scenario_model: %v", out)
	}
	first := qlog.scen
	if _, err := run(t, scen, `{"scenarios":[{"name":"x","kind":"bear","p":0.5,"level":100,"level_basis":"assumption"}]}`); err == nil || qlog.scen != first ||
		qlog.method("quant", false) != "fundamentals" || !within(first.levels[1].Level, 939.8, 0.1) { // 21.33 × 1.12^5 × 25
		t.Fatalf("a failed call keeps the last success: %v %+v", err, qlog.scen)
	}

	// stress_mix: bounded events on the measured base.
	slog := newToolLog()
	stress := k.stressTool(slog)
	ev := func(p, move float64, pBasis string) string {
		return fmt.Sprintf(`{"name":"a","p":%v,"move_pct":%v,"p_basis":%q,"move_basis":"assumption"}`, p, move, pBasis)
	}
	for _, c := range []struct{ events, want string }{
		{ev(0.25, -30, "assumption") + "," + ev(0.2, -30, "assumption"), "at most 0.40"},
		{ev(0.31, -30, "assumption"), "p must be 0.01..0.30"},
		{ev(0.1, -96, "assumption"), "move_pct must be -95..300"},
		{ev(0.1, -30, ""), "needs a basis"},
		{"", "give 1 to 4 events"},
	} {
		// a fresh log each: at most 2 calls
		if _, err := run(t, k.stressTool(newToolLog()), `{"events":[`+c.events+`]}`); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.events, err)
		}
	}
	out, err = run(t, stress, `{"events":[{"name":"Taiwan blockade","p":0.05,"move_pct":-60,"p_basis":"assumption","move_basis":"F1"}]}`)
	if err != nil || out["median"].(float64) >= out["baseMedian"].(float64) || !within(sumP(t, out["buckets"]), 1, 1e-9) ||
		slog.method("skeptic", false) != "regime" || slog.stress.events[0].Move != -0.6 || slog.stress.events[0].Inputs[1].Basis != "assumption" {
		t.Fatalf("stress_mix: %v %v %+v", out, err, slog.stress)
	}
	none := *k
	none.m = nil
	if _, err := run(t, none.stressTool(newToolLog()), `{"events":[{"name":"a","p":0.1,"move_pct":-30,"p_basis":"assumption","move_basis":"assumption"}]}`); err == nil ||
		!strings.Contains(err.Error(), "no base distribution; describe the tails in words") {
		t.Fatalf("no base: %v", err)
	}

	// Methods and anchors from the record.
	failed := newToolLog()
	failed.calls["stress_mix"], failed.failed["stress_mix"] = 1, 1
	if failed.method("skeptic", false) != "fallback" || newToolLog().method("skeptic", false) != "judgment" || newToolLog().method("base_rate", true) != "history" ||
		newToolLog().method("quant", false) != "judgment" {
		t.Fatal("fallback when every call failed, judgment when never called, history with H1")
	}
	if a := anchorOf("history", log, k.m, k.fr, k.d.Outcomes); len(a) != 6 || a[5].P != k.m.blend[5] {
		t.Fatalf("history anchor %v", a)
	}
	if anchorOf("judgment", log, k.m, k.fr, k.d.Outcomes) != nil || anchorOf("regime", newToolLog(), k.m, k.fr, k.d.Outcomes) != nil {
		t.Fatal("no anchor without a record")
	}
	if a := anchorOf("fundamentals", qlog, k.m, k.fr, k.d.Outcomes); len(a) != 6 || a[0].Outcome != "< $300" {
		t.Fatalf("fundamentals anchor %v", a)
	}
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

// verifyInput labels a basis by what its id states in this run (F claims, S
// title/snippet/page, H1's block); the user's material and unknown ids are
// assumptions, and model knowledge keeps its date.
func TestVerifyInput(t *testing.T) {
	k := tsmKit(t)
	for _, c := range []struct {
		field       string
		value       float64
		basis, want string
		kind        string
		verified    bool
	}{
		{"eps", 21.33, "F1", "F1", "source", true},
		{"eps", 16.56, "per F1", "F1", "source", true},
		{"pe", 22, "S1", "S1", "source", true},
		{"pe", 12, "S1", "S1", "source", true}, // the read page
		{"pe", 30, "S1", "assumption", "assumption", false},
		{"pe", 29.3, "F2", "assumption", "assumption", false},
		{"level", 485.12, "H1", "H1", "source", true},
		{"cagrPct", 19.8, "H1", "H1", "source", true},               // H1's CAGR since the listing
		{"level", 300, "H1", "assumption", "assumption", false},     // a range edge: H1 restates the question's ranges, it does not measure them
		{"pe", 13, "H1", "assumption", "assumption", false},         // "13:30 UTC", the quote's time
		{"growthPct", -18, "H1", "assumption", "assumption", false}, // "2009-12-18", a date
		{"pe", 13.5, "H1", "assumption", "assumption", false},       // a lognormal share on the ranges, "13.5%"
		{"eps", 21.33, "S7", "assumption", "assumption", false},
		{"pe", 22, "F1", "assumption", "assumption", false}, // a number in a field's form only (G16): F1 states no P/E
		{"pe", 21.33, "F1", "assumption", "assumption", false},
		{"growthPct", 21.33, "F1", "assumption", "assumption", false},
		{"pe", 20, "model knowledge, as of 2025-06", "model knowledge, as of 2025-06", "model_knowledge", false},
		{"pe", 20, "assumption", "assumption", "assumption", false},
	} {
		got, _ := k.verifyInput(c.field, c.value, c.basis)
		if got.Basis != c.want || got.Kind != c.kind || got.Verified != c.verified || got.Value != c.value {
			t.Errorf("%s %v cited %q: %+v", c.field, c.value, c.basis, got)
		}
	}
	// The review's probe (gap round G16): a source whose dates and counts held 25, 35, 20 and 16.
	probe := &deskKit{sources: []Source{{ID: "S3", Title: "TSMC third-quarter revenue beats estimates", Read: true,
		text: "Published Oct 25, 2026. TSMC said third-quarter revenue rose 30.4%.",
		page: "HSINCHU, Oct 16, 2026 - TSMC posted revenue of NT$990 billion. Some 35 analysts cover the stock; shares are up 20 times since 2006."}}}
	for _, c := range []struct {
		field string
		value float64
	}{{"pe", 25}, {"pe", 35}, {"pe", 20}, {"growthPct", 16}, {"growthPct", 30}} {
		if got, why := probe.verifyInput(c.field, c.value, "S3"); got.Kind != "assumption" || got.Verified || why != "does not state it" {
			t.Errorf("probe %s %v: %+v %q", c.field, c.value, got, why)
		}
	}
	if got, _ := probe.verifyInput("growthPct", 30.4, "S3"); !got.Verified {
		t.Error("30.4% is stated as a percentage")
	}
	if !k.known("F1") || !k.known("S2") || !k.known("H1") || k.known("F2") || k.known("S9") {
		t.Fatal("known ids: the run's web findings, sources and H1")
	}
	if l := k.historySymbols(); !slices.Contains(l, "SOX") || slices.Contains(l, "GME") {
		t.Fatalf("history symbols %v", l)
	}
	if _, ok := market.Benchmarks["SOX"]; !ok {
		t.Fatal("SOX is a benchmark")
	}
}

// A member's method rests on its seat's tool: a short-horizon stock's quant
// that read the subject's history_stats is a volatility model with no anchor
// of its own (the base rate's series); a long-horizon quant whose
// scenario_model failed is a fallback even when history_stats answered; the
// desk's scenario_model takes no cagr_pct mode.
func TestMethodTool(t *testing.T) {
	k := tsmKit(t)
	short := tsmDraft()
	short.ResolutionDate = frameDay.AddDate(0, 3, 0).Format(time.DateOnly)
	sk := *k
	sk.d, sk.fr, sk.logs = short, frameOf(short, frameDay), map[string]*toolLog{}
	sk.m = k.h.measure(context.Background(), sk.fr, short, frameDay)
	quant := sk.seat(roles[1], nil, false)
	if sk.fr.Kind != "price_short" || len(quant.tools) != 1 || quant.tools[0].Name != "history_stats" || methodTool("quant", sk.fr) != "history_stats" {
		t.Fatalf("short-horizon stock quant: %s %d tools", sk.fr.Kind, len(quant.tools))
	}
	if _, err := run(t, quant.tools[0], `{"symbol":"TSM","horizon_days":91,"thresholds":[300,450,600,800,1100]}`); err != nil {
		t.Fatal(err)
	}
	log := sk.logs["quant"]
	if m := log.method("quant", true); m != "volatility" || anchorOf(m, log, sk.m, sk.fr, short.Outcomes) != nil ||
		methodDetail(Specialist{Method: m}, log, sk.m, Unit{Prefix: "$"}) != "history_stats lognormal: TSM" {
		t.Fatalf("short quant: %s", m)
	}

	long := newToolLog()
	long.tool = methodTool("quant", k.fr)
	long.calls["scenario_model"], long.failed["scenario_model"], long.calls["history_stats"], long.ok["history_stats"] = 1, 1, 1, 1
	if m := long.method("quant", true); long.tool != "scenario_model" || m != "fallback" {
		t.Fatalf("a failed scenario_model beside a history_stats that answered: %s", m)
	}
	cagr := `{"scenarios":[{"name":"a","kind":"bear","p":0.5,"cagr_pct":3,"cagr_basis":"assumption"},{"name":"b","kind":"bull","p":0.5,"cagr_pct":15,"cagr_basis":"assumption"}]}`
	if _, err := run(t, k.scenarioTool(newToolLog()), cagr); err == nil || !strings.Contains(err.Error(), "cagr_pct is not a mode here") {
		t.Fatalf("cagr mode: %v", err)
	}

	// A market-cap question's scenarios are market caps: EPS × P/E (a price per share) is refused, levels pass.
	mc := *k
	mc.fr.Measure = "market_cap"
	mc.findings = append(slices.Clone(k.findings), Finding{ID: "F3", Claim: "TSMC's market cap is $1.2T (2026-10-01).", SourceIDs: []string{"S1"}})
	y := frameDay.Year()
	eps := fmt.Sprintf(`{"name":"a","kind":"bear","p":0.5,"eps":21.33,"eps_year":%d,"eps_basis":"F1","growth_pct":3,"exit_year":%d,"growth_basis":"assumption","pe":14,"pe_basis":"assumption"}`, y+1, y+6)
	lvl := `{"name":"b","kind":"bull","p":0.5,"level":2e12,"level_basis":"assumption"}`
	if _, err := run(t, mc.scenarioTool(newToolLog()), `{"level":{"value":1.2e12,"basis":"F3"},"scenarios":[`+eps+`,`+lvl+`]}`); err == nil || !strings.Contains(err.Error(), "in level mode") {
		t.Fatalf("EPS mode on a market cap: %v", err)
	}
	if _, err := run(t, mc.scenarioTool(newToolLog()), `{"level":{"value":1.2e12,"basis":"F3"},"scenarios":[{"name":"a","kind":"bear","p":0.5,"level":8e11,"level_basis":"assumption"},`+lvl+`]}`); err != nil {
		t.Fatalf("level mode on a market cap: %v", err)
	}
}

// "Cited" means cited (gap round G16): a TSM run whose only cited inputs are
// a source's dates and counts ("Oct 25", "35 analysts", "20 times since
// 2006", "Oct 16") labels them assumptions, so its view rests on the capped
// drift, not on the scenarios.
func TestCitedMeansCited(t *testing.T) {
	k := tsmKit(t)
	k.sources = append(k.sources, Source{ID: "S3", Title: "TSMC third-quarter revenue beats estimates", Read: true,
		text: "Published Oct 25, 2026. TSMC said third-quarter revenue rose 30.4%.",
		page: "HSINCHU, Oct 16, 2026 - TSMC posted revenue of NT$990 billion. Some 35 analysts cover the stock; shares are up 20 times since 2006."})
	qlog := newToolLog()
	k.logs["quant"] = qlog
	y := frameDay.Year()
	b, _ := json.Marshal(map[string]any{"scenarios": []map[string]any{
		{"name": "Digestion", "kind": "bear", "p": 0.3, "eps": 21.33, "eps_year": y + 1, "eps_basis": "assumption", "growth_pct": 16, "exit_year": y + 6, "growth_basis": "S3", "pe": 20, "pe_basis": "S3"},
		{"name": "Compounding", "kind": "base", "p": 0.5, "eps": 21.33, "eps_year": y + 1, "eps_basis": "assumption", "growth_pct": 16, "exit_year": y + 6, "growth_basis": "S3", "pe": 25, "pe_basis": "S3"},
		{"name": "Supercycle", "kind": "bull", "p": 0.2, "eps": 21.33, "eps_year": y + 1, "eps_basis": "assumption", "growth_pct": 16, "exit_year": y + 6, "growth_basis": "S3", "pe": 35, "pe_basis": "S3"},
	}})
	out, err := run(t, k.scenarioTool(qlog), string(b))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range out["scenarios"].([]any) {
		for _, in := range s.(map[string]any)["inputs"].([]any) {
			if in := in.(map[string]any); in["kind"] != "assumption" || in["verified"] == true {
				t.Errorf("input %v", in)
			}
		}
	}
	panel := []Specialist{{Role: "quant", Probabilities: probsAt(tsmOutcomes, k.m.lognormal...)}}
	if got := k.basis(panel, probsAt(tsmOutcomes, k.m.lognormal...)); got.Kind != "capped_drift" {
		t.Fatalf("basis %+v", got)
	}
}

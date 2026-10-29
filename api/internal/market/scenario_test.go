package market

import (
	"cmp"
	"encoding/json"
	"math"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func f64(x float64) *float64 { return &x }

// tsmScenarios are design §6's quant scenarios: FY2027 consensus EPS $21.33
// (cited), EPS growth to FY2032 and the exit P/E per scenario (assumptions).
func tsmScenarios() []ScenarioInput {
	sc := func(name, kind string, p, g, pe float64) ScenarioInput {
		return ScenarioInput{Name: name, Kind: kind, P: p, EPS: f64(21.33), EPSYear: 2027, EPSBasis: "[1]",
			GrowthPct: f64(g), GrowthBasis: "assumption", PE: f64(pe), PEBasis: "assumption"}
	}
	return []ScenarioInput{
		sc("AI capex digestion", "bear", 0.30, 3, 14),
		sc("Compounding foundry leader", "base", 0.50, 12, 20),
		sc("AI supercycle", "bull", 0.20, 20, 25),
	}
}

// §6.2's mixture: spot $485, σ 0.313 a year (the PDF's σ_T 0.70 over 5 years),
// the three scenarios at T = 5.
func tsmMixture(t *testing.T) Mixture {
	t.Helper()
	lv, _, err := ValidateScenarios(tsmScenarios(), 485, 5, 2031)
	if err != nil {
		t.Fatal(err)
	}
	return NewMixture(485, 5, 0.313, lv)
}

func TestValidateScenarios(t *testing.T) {
	lv, warns, err := ValidateScenarios(tsmScenarios(), 485, 5, 2031)
	if err != nil || len(warns) != 0 {
		t.Fatalf("%v %v", err, warns)
	}
	for i, want := range []float64{346.2, 751.8, 1326.9} {
		if math.Abs(lv[i].Level-want) > 0.05 {
			t.Errorf("scenario %d level %.2f, want %.1f", i, lv[i].Level, want)
		}
	}
	eps := lv[1].Inputs[0]
	if eps.Field != "eps" || eps.Value != 21.33 || *eps.Year != 2027 || eps.Basis != "[1]" || eps.Kind != "source" || eps.Verified ||
		lv[1].Inputs[1].Field != "growthPct" || *lv[1].Inputs[1].Year != 2032 || lv[1].Inputs[2].Kind != "assumption" {
		t.Errorf("inputs %+v", lv[1].Inputs)
	}

	// Probabilities within ±0.05 renormalize; outside fail.
	in := tsmScenarios()
	in[0].P = 0.33
	if lv, _, err := ValidateScenarios(in, 485, 5, 2031); err != nil || math.Abs(lv[0].P-0.33/1.03) > 1e-12 {
		t.Errorf("renormalize: %v %+v", err, lv)
	}

	// Level and cagr modes, kinds inferred by price when missing.
	mixed := []ScenarioInput{
		{Name: "Up", P: 0.4, CAGRPct: f64(15), CAGRBasis: "model knowledge, as of 2025-06 (long-run semis returns)"},
		{Name: "Down", P: 0.3, Level: f64(300), LevelBasis: "assumption"},
		{Name: "Flat", P: 0.3, Level: f64(500), LevelBasis: "[2]"},
	}
	lv, _, err = ValidateScenarios(mixed, 485, 5, 2031)
	if err != nil || lv[0].Name != "Down" || lv[0].Kind != "bear" || lv[1].Kind != "base" || lv[2].Kind != "bull" ||
		math.Abs(lv[2].Level-485*math.Pow(1.15, 5)) > 1e-9 || lv[2].Inputs[0].Basis != "model knowledge, as of 2025-06" || lv[2].Inputs[0].Kind != "model_knowledge" {
		t.Fatalf("mixed %v %+v", err, lv)
	}
	five := []ScenarioInput{{Name: "a", P: .2, Level: f64(100), LevelBasis: "x"}, {Name: "b", P: .2, Level: f64(300), LevelBasis: "x"},
		{Name: "c", P: .2, Level: f64(500), LevelBasis: "x"}, {Name: "d", P: .2, Level: f64(700), LevelBasis: "x"}, {Name: "e", P: .2, Level: f64(900), LevelBasis: "x"}}
	if lv, _, err := ValidateScenarios(five, 485, 5, 2031); err != nil || lv[0].Kind != "tail" || lv[1].Kind != "bear" || lv[2].Kind != "base" || lv[4].Kind != "bull" {
		t.Errorf("five kinds %v %+v", err, lv)
	}

	// Warnings, not errors.
	in = tsmScenarios()
	in[2].PE, in[2].GrowthPct = f64(80), f64(50)
	if _, warns, err := ValidateScenarios(in, 485, 5, 2031); err != nil || len(warns) != 3 ||
		!strings.Contains(warns[0], "P/E of 80") || !strings.Contains(warns[1], "growth of 50%") || !strings.Contains(warns[2], "over 10×") {
		t.Errorf("warnings %v %v", warns, err)
	}
	// Levels in another currency are written with its mark, never "$".
	lvl := []ScenarioInput{{Name: "Bear", Kind: "bear", P: 0.3, Level: f64(21_510), LevelBasis: "assumption"}, {Name: "Base", Kind: "base", P: 0.7, Level: f64(300_000), LevelBasis: "assumption"}}
	if _, warns, err := ValidateScenariosIn(lvl, 262_000, 3, 2029, "₩"); err != nil || len(warns) != 1 || warns[0] != "Bear: ₩21,510 is under 0.1× today's price" {
		t.Errorf("₩ warnings %q %v", warns, err)
	}
	lvl[0].Level = f64(1_000)
	if _, _, err := ValidateScenariosIn(lvl, 262_000, 3, 2029, "₩"); err == nil || err.Error() != `scenario "Bear": level must be between ₩5,240 and ₩52.4M` {
		t.Errorf("₩ error %v", err)
	}
	if FormatLevelIn(73.85, "S$") != "S$73.85" || FormatLevelIn(2.12e6, "HK$") != "HK$2.12M" || FormatLevelIn(485, "$") != FormatLevel(485) {
		t.Error("FormatLevelIn")
	}

	for name, c := range map[string]struct {
		edit func([]ScenarioInput) []ScenarioInput
		want string
	}{
		"one":       {func(s []ScenarioInput) []ScenarioInput { return s[:1] }, "give 2 to 5 scenarios"},
		"sum":       {func(s []ScenarioInput) []ScenarioInput { s[0].P = 0.6; return s }, "probabilities must sum to 1"},
		"p":         {func(s []ScenarioInput) []ScenarioInput { s[0].P = 0.96; return s }, "p must be 0.01..0.95"},
		"name":      {func(s []ScenarioInput) []ScenarioInput { s[0].Name = " "; return s }, "needs a name"},
		"two modes": {func(s []ScenarioInput) []ScenarioInput { s[0].Level = f64(300); return s }, "exactly one mode"},
		"no mode": {func(s []ScenarioInput) []ScenarioInput {
			s[0] = ScenarioInput{Name: "x", Kind: "bear", P: 0.3}
			return s
		}, "exactly one mode"},
		"partial":   {func(s []ScenarioInput) []ScenarioInput { s[0].PE = nil; return s }, "EPS mode needs eps, eps_year, growth_pct and pe"},
		"neg eps":   {func(s []ScenarioInput) []ScenarioInput { s[0].EPS = f64(-1); return s }, "the fundamentals model needs positive EPS; use level mode and say why"},
		"eps year":  {func(s []ScenarioInput) []ScenarioInput { s[0].EPSYear = 2020; return s }, "eps_year must be 2025..2032"},
		"exit year": {func(s []ScenarioInput) []ScenarioInput { s[0].ExitYear = 2026; return s }, "exit_year must be at or after eps_year"},
		"growth":    {func(s []ScenarioInput) []ScenarioInput { s[0].GrowthPct = f64(90); return s }, "growth_pct must be -40..80"},
		"pe":        {func(s []ScenarioInput) []ScenarioInput { s[0].PE = f64(1); return s }, "pe must be 2..150"},
		"basis": {func(s []ScenarioInput) []ScenarioInput { s[1].PEBasis = ""; return s },
			"each input needs a basis: a source id, 'model knowledge, as of YYYY-MM' or 'assumption'"},
		"level":  {func(s []ScenarioInput) []ScenarioInput { return mixedLevel(5) }, "level must be between"},
		"cagr":   {func(s []ScenarioInput) []ScenarioInput { s[0] = cagrScenario(200); return s }, "cagr_pct must be -60..150"},
		"order":  {func(s []ScenarioInput) []ScenarioInput { s[0].PE = f64(40); return s }, "scenario kinds disagree with their prices"},
		"lbasis": {func(s []ScenarioInput) []ScenarioInput { m := mixedLevel(300); m[0].LevelBasis = ""; return m }, "each input needs a basis"},
	} {
		if _, _, err := ValidateScenarios(c.edit(tsmScenarios()), 485, 5, 2031); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
}

// TestScenarioKinds: a kind goes by price, never by array order. A tail sits
// at either end: the production failure was "can TSMC 10x" with an upside
// "AI super-cycle" tail above the bull, refused three times with "order the
// scenarios by price" whatever the order. A kind that disagrees with its
// price gets an error whose fix, applied literally, validates.
func TestScenarioKinds(t *testing.T) {
	sc := func(name, kind string, p, g, pe float64) ScenarioInput {
		return ScenarioInput{Name: name, Kind: kind, P: p, EPS: f64(19.85), EPSYear: 2027, EPSBasis: "[1]",
			GrowthPct: f64(g), GrowthBasis: "assumption", PE: f64(pe), PEBasis: "assumption"}
	}
	bear, base, bull := sc("AI capex digestion", "bear", 0.2, 4, 14), sc("Compounding foundry leader", "base", 0.45, 12, 20), sc("AI demand stays hot", "bull", 0.25, 18, 24)
	up, down := sc("AI super-cycle", "tail", 0.1, 28, 30), ScenarioInput{Name: "Taiwan Strait blockade", Kind: "tail", P: 0.1, Level: f64(120), LevelBasis: "assumption"}
	val := func(in ...ScenarioInput) ([]ScenarioLevel, error) {
		lv, _, err := ValidateScenarios(in, 424.61, 10, 2036)
		return lv, err
	}
	// The simulator's three tries (prompt order, descending, tail first), then both tails at once.
	for name, in := range map[string][]ScenarioInput{
		"ascending": {bear, base, bull, up}, "descending": {up, bull, base, bear}, "tail first": {up, bear, base, bull},
		"both tails": {down, withP(bear, 0.15), base, bull, withP(up, 0.05)},
	} {
		lv, err := val(in...)
		if err != nil || lv[len(lv)-1].Name != "AI super-cycle" || lv[len(lv)-1].Kind != "tail" || math.Abs(lv[len(lv)-1].Level-7030.42) > 0.01 ||
			!slices.IsSortedFunc(lv, func(a, b ScenarioLevel) int { return cmp.Compare(a.Level, b.Level) }) {
			t.Errorf("%s: %v %+v", name, err, lv)
		}
	}
	if lv, err := val(down, bear, base, bull); err != nil || lv[0].Kind != "tail" || lv[0].Level != 120 {
		t.Errorf("downside tail: %v %+v", err, lv)
	}

	// A tail inside the range: price it beyond, or take a kind that fits there.
	mid := sc("Steady share gains", "tail", 0.1, 15, 22)
	_, err := val(bear, base, bull, mid)
	if err == nil || err.Error() != `scenario kinds disagree with their prices: "Steady share gains" (tail, $1,767) sits between "AI capex digestion" (bear, $411.36) and "AI demand stays hot" (bull, $2,493). `+
		`A tail is an extreme case below every other scenario (a war, a bust) or above them all (a super-cycle): price it below $411.36 or above $2,493, or make it a base or bull` {
		t.Fatalf("tail inside: %v", err)
	}
	for _, k := range []string{"base", "bull"} {
		mid.Kind = k
		if _, err := val(bear, base, bull, mid); err != nil {
			t.Errorf("tail made a %s: %v", k, err)
		}
	}
	// Bears, bases and bulls out of price order: the relabel it names validates.
	swapped := []ScenarioInput{withP(bull, 0.35), base, bear}
	swapped[0].Kind, swapped[2].Kind = "bear", "bull"
	_, err = val(swapped...)
	if err == nil || err.Error() != `scenario kinds disagree with their prices: "AI demand stays hot" (bear, $2,493) is above "Compounding foundry leader" (base, $1,233). `+
		`A kind goes by price, not by its place in the array: every bear below every base, every base below every bull. `+
		`If a price is wrong (a sign or a P/E typo), fix the inputs that set it; if the prices are right, relabel by price (make "AI capex digestion" bear, "AI demand stays hot" bull)` {
		t.Fatalf("swapped: %v", err)
	}
	swapped[0].Kind, swapped[2].Kind = "bull", "bear"
	if _, err := val(swapped...); err != nil {
		t.Errorf("relabelled: %v", err)
	}
	// Equal levels never disagree; one bear/base/bull and a tail always fit.
	if _, err := val(ScenarioInput{Name: "a", Kind: "bull", P: 0.5, Level: f64(500), LevelBasis: "x"}, ScenarioInput{Name: "b", Kind: "bear", P: 0.3, Level: f64(500), LevelBasis: "x"},
		ScenarioInput{Name: "c", Kind: "tail", P: 0.2, Level: f64(500), LevelBasis: "x"}); err != nil {
		t.Errorf("equal levels: %v", err)
	}
}

func withP(s ScenarioInput, p float64) ScenarioInput { s.P = p; return s }

func mixedLevel(level float64) []ScenarioInput {
	return []ScenarioInput{{Name: "low", P: 0.5, Level: f64(level), LevelBasis: "assumption"}, {Name: "high", P: 0.5, Level: f64(900), LevelBasis: "assumption"}}
}

func cagrScenario(c float64) ScenarioInput {
	return ScenarioInput{Name: "fast", Kind: "bear", P: 0.3, CAGRPct: f64(c), CAGRBasis: "assumption"}
}

func TestClassifyBasis(t *testing.T) {
	for in, want := range map[string][2]string{
		"model knowledge, as of 2025-06": {"model knowledge, as of 2025-06", "model_knowledge"},
		"Model knowledge (2024)":         {"model knowledge, as of 2024", "model_knowledge"},
		"model knowledge":                {"assumption", "assumption"},
		"Assumption: cycle":              {"assumption", "assumption"},
		" [3] ":                          {"[3]", "source"},
		"quote":                          {"quote", "source"},
	} {
		if l, k := ClassifyBasis(in); l != want[0] || k != want[1] {
			t.Errorf("%q: %q %q, want %v", in, l, k, want)
		}
	}
}

func TestMixture(t *testing.T) {
	m := tsmMixture(t)
	// Variance matching (§6.2): σ within 0.228, total log variance σ²T.
	if math.Abs(m.SigmaPath-0.2276) > 0.0005 {
		t.Errorf("SigmaPath %.4f, want 0.228", m.SigmaPath)
	}
	mean, second := 0.0, 0.0
	for i, s := range m.Scen {
		mean += s.P * m.mu[i] * 5
	}
	for i, s := range m.Scen {
		d := m.mu[i]*5 - mean
		second += s.P * (d*d + m.SigmaPath*m.SigmaPath*5)
	}
	if math.Abs(second-0.313*0.313*5) > 1e-9 {
		t.Errorf("log variance %v, want %v", second, 0.313*0.313*5)
	}
	// Read before the horizon the view is still as wide as the measured σ
	// (the paths have spread apart less by then); past it, wider.
	for _, at := range []float64{1, 3, 5, 10} {
		mean, second := 0.0, 0.0
		for i, s := range m.Scen {
			mean += s.P * m.mu[i] * at
		}
		sig := m.sigmaAt(at)
		for i, s := range m.Scen {
			d := m.mu[i]*at - mean
			second += s.P * (d*d + sig*sig*at)
		}
		if want := 0.313 * 0.313 * at; at <= 5 && math.Abs(second-want) > 1e-9 || at > 5 && !(second > want) {
			t.Errorf("log variance at %v years %v, measured %v", at, second, want)
		}
	}
	// So a 1-year read's tail odds are not squeezed by the 5-year SigmaPath.
	squeezed := 0.0
	for i, s := range m.Scen {
		e, _ := passage(0.5, m.mu[i], m.SigmaPath, 1)
		squeezed += s.P * e
	}
	if e1, _ := m.Passage(0.5, 1); !(e1 > 1.5*squeezed) {
		t.Errorf("P(≤ 0.5× within a year) %v, squeezed %v", e1, squeezed)
	}
	// Scenarios wider than the measured σ: the path σ floors at half of it.
	wide, _, _ := ValidateScenarios(mixedLevel(30), 485, 1, 2027)
	if w := NewMixture(485, 1, 0.2, wide); math.Abs(w.SigmaPath-0.1) > 1e-12 {
		t.Errorf("floored SigmaPath %v", w.SigmaPath)
	}
	if a := NewMixture(485, 5, 0, m.Scen); !a.VolAssumed || a.VolAnn != 0.30 {
		t.Errorf("no measured σ: %+v", a)
	}

	// The §6.2 view against the baseline at 5 years.
	edges := LadderEdges(485, 0.313*math.Sqrt(5))
	view, base := m.Buckets(edges), ZeroDrift(485, 5, 0.313).Buckets(edges)
	for i, want := range []float64{8.6, 11.1, 12.9, 23.0, 15.2, 16.6, 12.5} {
		if math.Abs(view[i]*100-want) > 0.15 {
			t.Errorf("view bucket %d %.2f%%, want %.1f%%", i, view[i]*100, want)
		}
	}
	for i, want := range []float64{17.2, 16.3, 16.5, 23.3, 11.6, 9.7, 5.3} {
		if math.Abs(base[i]*100-want) > 0.15 {
			t.Errorf("baseline bucket %d %.2f%%, want %.1f%%", i, base[i]*100, want)
		}
	}
	if s := sum(view); math.Abs(s-1) > 1e-12 {
		t.Errorf("view buckets sum %v", s)
	}
	for _, c := range []struct{ got, want, tol float64 }{
		// §6.2 prints $667; its own formulas give $677.4 (CDF(667) = 0.492): see Deviations.
		{m.Quantile(0.5), 677.4, 0.5}, {m.Quantile(0.05), 207, 1}, {m.Quantile(0.95), 2070, 5}, {1 - m.CDF(485), 0.67, 0.005},
	} {
		if math.Abs(c.got-c.want) > c.tol {
			t.Errorf("view %v, want %v ± %v", c.got, c.want, c.tol)
		}
	}
	if half, _ := m.Passage(0.5, 5); math.Abs(half-0.08) > 0.005 {
		t.Errorf("P(≤ 0.5×) %v, want 8%%", half)
	}
	if two, _ := m.Passage(2, 5); math.Abs(two-0.31) > 0.005 {
		t.Errorf("P(≥ 2×) %v, want 31%%", two)
	}
	for _, q := range []float64{0.05, 0.3, 0.5, 0.9} {
		if x := m.Quantile(q); math.Abs(m.CDF(x)-q) > 1e-9 {
			t.Errorf("CDF(Quantile(%v)) = %v", q, m.CDF(x))
		}
	}

	// ZeroDrift is OutlookFrom's model; one scenario's passage is passage.
	z, o := ZeroDrift(485, 1825.0/365, 0.4), OutlookFrom("TSM", 485, 0.4, 1825)
	s := 0.4 * math.Sqrt(1825.0/365)
	for _, x := range []float64{150, 300, 485, 900, 2000} {
		if want := normCDF(math.Log(x/485) / s); math.Abs(z.CDF(x)-want) > 1e-12 {
			t.Errorf("ZeroDrift CDF(%v) %v, want %v", x, z.CDF(x), want)
		}
	}
	if math.Abs(z.Quantile(0.05)-o.P5) > 1e-6 || math.Abs(z.Quantile(0.95)-o.P95) > 1e-6 {
		t.Errorf("ZeroDrift quantiles %v %v vs %v %v", z.Quantile(0.05), z.Quantile(0.95), o.P5, o.P95)
	}
	d := DriftMixture(485, 5, 0.4, 0.06)
	if e, p := d.Passage(10, 20); func() bool { we, wp := passage(10, 0.06, 0.4, 20); return e != we || p != wp }() {
		t.Errorf("one path's passage %v %v", e, p)
	}
}

func sum(xs []float64) float64 {
	s := 0.0
	for _, x := range xs {
		s += x
	}
	return s
}

func TestTimeToTarget(t *testing.T) {
	m := tsmMixture(t)
	base := ZeroDrift(485, 5, 0.313)
	own, since := 18.4, "1997-10-09"
	tt := NewTimeToTarget(m, base, 10, &own, &since)
	if tt.Target != 4850 || len(tt.Rows) != 6 || *tt.OwnCAGRPct != own || *tt.OwnSince != since {
		t.Fatalf("%+v", tt)
	}
	row := func(y int) TargetRow {
		return tt.Rows[slices.IndexFunc(tt.Rows, func(r TargetRow) bool { return r.Years == y })]
	}
	for _, c := range []struct {
		years                int
		end, touch           float64
		baseEnd, baseTouch   float64
		extrapolated         bool
		checkBase, checkView bool
	}{
		{10, 0.081, 0.099, 0.010, 0.020, false, true, true},
		{15, 0.224, 0.258, 0.029, 0.058, true, true, true},
		{20, 0.338, 0.382, 0.050, 0.100, true, true, true},
		{5, 0.001, 0.002, 0.001, 0.001, false, false, false},
	} {
		r := row(c.years)
		if r.Extrapolated != c.extrapolated {
			t.Errorf("%dy extrapolated %v", c.years, r.Extrapolated)
		}
		if c.checkView && (math.Abs(r.PEnd-c.end) > 0.0015 || math.Abs(r.PTouch-c.touch) > 0.0015) {
			t.Errorf("%dy view %.4f / %.4f, want %.3f / %.3f", c.years, r.PEnd, r.PTouch, c.end, c.touch)
		}
		if c.checkBase && (math.Abs(r.BaselinePEnd-c.baseEnd) > 0.0015 || math.Abs(r.BaselinePTouch-c.baseTouch) > 0.0015) {
			t.Errorf("%dy baseline %.4f / %.4f, want %.3f / %.3f", c.years, r.BaselinePEnd, r.BaselinePTouch, c.baseEnd, c.baseTouch)
		}
	}
	if tt.MedianYears == nil || *tt.MedianYears != 30 {
		t.Errorf("10× median years %v, want 30 (the grid's last step)", tt.MedianYears)
	}
	if two := NewTimeToTarget(m, base, 2, nil, nil); two.MedianYears == nil || *two.MedianYears != 10 {
		t.Errorf("2× median years %v, want 10", two.MedianYears)
	}
	if far := NewTimeToTarget(m, base, 12, nil, nil); far.MedianYears != nil {
		t.Errorf("12× within 30 years: %v", *far.MedianYears)
	}
	by := tt.ByScenario
	if len(by) != 3 || by[0].Years != nil || math.Abs(by[1].AnnualPct-9.2) > 0.05 || math.Abs(*by[1].Years-26.3) > 0.05 ||
		math.Abs(by[2].AnnualPct-22.3) > 0.05 || math.Abs(*by[2].Years-11.4) > 0.05 {
		t.Errorf("by scenario %+v", by)
	}
	for i, want := range []float64{115.4, 58.5, 25.9, 16.6, 12.2} {
		if r := tt.RequiredCAGR[i]; r.Years != ladderYears[i] || math.Abs(r.Pct-want) > 0.05 {
			t.Errorf("required CAGR %+v, want %v", r, want)
		}
	}
	// A drift view has no scenario horizon to extrapolate past.
	if d := NewTimeToTarget(DriftMixture(485, 5, 0.313, 0.08), base, 10, nil, nil); d.Rows[5].Extrapolated || len(d.ByScenario) != 1 {
		t.Errorf("drift view %+v", d)
	}
}

func TestLadderEdges(t *testing.T) {
	for _, c := range []struct {
		spot, s float64
		want    []float64
	}{
		{485, 0.7, []float64{250, 360, 485, 750, 1000, 1500}},
		{485, 0.1, []float64{400, 440, 485, 530, 600}},       // 388 → 400 and 606 → 600: within 4%
		{485, 0.3, []float64{300, 400, 485, 600, 800, 1000}}, // 291 → 300, 776 → 800, 970 → 1,000
		{485, 1.2, []float64{120, 250, 485, 1000, 2000, 4000}},
		{100_000, 0.4, []float64{60_000, 80_000, 100_000, 130_000, 160_000, 200_000}}, // 125k: 120k is not within 4%
		{0.2, 0.7, []float64{0.1, 0.15, 0.2, 0.3, 0.4, 0.6}},
	} {
		if got := LadderEdges(c.spot, c.s); !slices.Equal(got, c.want) {
			t.Errorf("LadderEdges(%v, %v) = %v, want %v", c.spot, c.s, got, c.want)
		}
	}
	// No spot collides two edges under this table (searched from $0.01 to
	// $100k); the 3-figure fallback is there for table edits.
}

// TestRoundSigExact: ladder edges from a million up are whole numbers (the
// live check's 10-year ₩ and BTC ladders read "₩999,999.9999999999"), and
// below a million roundSig gives what slice 1's arithmetic gave.
func TestRoundSigExact(t *testing.T) {
	for _, c := range []struct {
		name    string
		spot, s float64
		want    []float64
	}{
		// Samsung 005930.KS at ₩262,000, σ_5y 41.36%, 10 years: the live draft's ladder.
		{"₩262,000 10y", 262_000, 0.4136 * math.Sqrt(10), []float64{66_000, 130_000, 262_000, 520_000, 1_000_000, 2_100_000}},
		// BTC above about $125k at 10 years (σ_5y 51.18%).
		{"BTC $130,000 10y", 130_000, 0.5118 * math.Sqrt(10), []float64{33_000, 65_000, 130_000, 250_000, 500_000, 1_000_000}},
		{"BTC $200,000 10y", 200_000, 0.5118 * math.Sqrt(10), []float64{50_000, 100_000, 200_000, 400_000, 800_000, 1_600_000}},
		{"₩1,048,000 3y", 1_048_000, 0.4136 * math.Sqrt(3), []float64{520_000, 800_000, 1_050_000, 1_600_000, 2_100_000, 3_100_000}},
	} {
		got := LadderEdges(c.spot, c.s)
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: LadderEdges = %v, want %v", c.name, got, c.want)
		}
		for _, e := range got {
			if s := strconv.FormatFloat(e, 'f', -1, 64); strings.Contains(s, ".") {
				t.Errorf("%s: edge %s is not whole", c.name, s)
			}
		}
	}
	for x, want := range map[[2]float64]float64{{1_048_000, 2}: 1e6, {1_600_400, 2}: 1.6e6, {2_096_000, 3}: 2.1e6, {12_345_678, 3}: 1.23e7, {7.77e12, 2}: 7.8e12} {
		if got := roundSig(x[0], int(x[1])); got != want {
			t.Errorf("roundSig(%v, %v) = %v, want %v", x[0], x[1], got, want)
		}
	}
	// Below a million: slice 1's arithmetic (multiply by 10^(figs−1−⌊log10 x⌋),
	// round, divide) on random levels from 1e-6 to 1e6 and every figure count
	// the callers use (the chat card's ladder, OutlookFrom, FormatLevel).
	legacy := func(x float64, figs int) float64 {
		scale := math.Pow(10, float64(figs)-1-math.Floor(math.Log10(x)))
		return math.Round(x*scale) / scale
	}
	r := rand.New(rand.NewPCG(9, 9))
	for range 200_000 {
		x, figs := math.Pow(10, -6+12*r.Float64()), 2+r.IntN(7)
		if got, old := roundSig(x, figs), legacy(x, figs); got != old {
			t.Fatalf("roundSig(%v, %d) = %v, slice 1 gave %v", x, figs, got, old)
		}
	}
	if roundSig(math.Inf(1), 3) != math.Inf(1) || !math.IsNaN(roundSig(math.NaN(), 3)) {
		t.Error("roundSig passes +Inf and NaN through")
	}
}

func TestNumberIn(t *testing.T) {
	text := "Consensus EPS $16.56 this year and 21.33 for next; gross margin 55.5%; 29.3x trailing; market cap $1.2T; capex 950bn; 5 TWSE shares"
	for _, c := range []struct {
		v    float64
		want bool
	}{
		{16.56, true}, {21.33, true}, {55.5, true}, {29.3, true}, {29, false}, {1.2e12, true}, {1.2, true}, {9.5e11, true},
		{16.6, true}, {17, false}, {5, true}, {5e12, false}, {-55.5, true},
	} {
		if got := NumberIn(c.v, text); got != c.want {
			t.Errorf("NumberIn(%v) = %v, want %v", c.v, got, c.want)
		}
	}
	if NumberIn(21.33, "") || !NumberIn(1234.5, "revenue of 1,234.5 million") {
		t.Error("empty text, or commas")
	}
}

// TestFormatLevel runs the table the web's fmtLevelPrice shares
// (testdata/format_level.json), plus the API-only edges.
func TestFormatLevel(t *testing.T) {
	b, err := os.ReadFile("testdata/format_level.json")
	if err != nil {
		t.Fatal(err)
	}
	var table struct{ Cases [][2]any }
	if err := json.Unmarshal(b, &table); err != nil || len(table.Cases) < 30 {
		t.Fatalf("table: %v (%d cases)", err, len(table.Cases))
	}
	for _, c := range table.Cases {
		x, want := c[0].(float64), c[1].(string)
		if got := FormatLevel(x); got != want {
			t.Errorf("FormatLevel(%v) = %q, want %q", x, got, want)
		}
	}
	for x, want := range map[float64]string{0: "$0", -3: "$0", math.NaN(): "$0", math.Inf(1): "$0", 1.2e9: "$1.20B", 999.7e6: "$1.00B", 4.5e12: "$4.50T"} {
		if got := FormatLevel(x); got != want {
			t.Errorf("FormatLevel(%v) = %q, want %q", x, got, want)
		}
	}
}

// The ladder follows the view: a strong view no longer piles into the open top
// bucket, the baseline column uses the same edges, spot stays an edge.
func TestViewLadderEdges(t *testing.T) {
	// The live NVDA 10-year drift case (AQ1-LIVE-CHECK): spot $240.59, σ 58.9%, halved drift 15.64%/yr.
	spot, vol := 240.59, 0.589
	s := vol * math.Sqrt(10)
	view := DriftMixture(spot, 10, vol, 0.1564)
	old := LadderEdges(spot, s)
	if top := 1 - view.CDF(old[len(old)-1]); math.Abs(top-0.383) > 0.005 {
		t.Fatalf("the spot-centred ladder's top bucket %.3f, want the live 38.3%%", top)
	}
	edges := ViewLadderEdges(spot, s, view)
	p := view.Buckets(edges)
	if len(edges) > 7 || !slices.Contains(edges, 241) || p[len(p)-1] > 0.16 || p[0] > 0.12 || !slices.IsSorted(edges) {
		t.Errorf("NVDA edges %v, view buckets %.3f", edges, p)
	}
	want := []float64{120, 241, 500, 1000, 2000, 4000, 7500}
	if !slices.Equal(edges, want) {
		t.Errorf("NVDA edges %v, want %v", edges, want)
	}
	labels := make([]string, len(edges))
	for i, e := range edges {
		labels[i] = FormatLevel(e)
	}
	if strings.Join(labels, " ") != "$120 $241 $500 $1,000 $2,000 $4,000 $7,500" {
		t.Errorf("labels %v", labels)
	}

	// A view inside the ladder (TSM §6.2 at 5 years) keeps LadderEdges as they were.
	m := tsmMixture(t)
	if got := ViewLadderEdges(485, 0.313*math.Sqrt(5), m); !slices.Equal(got, LadderEdges(485, 0.313*math.Sqrt(5))) {
		t.Errorf("TSM edges %v", got)
	}
	// A strong bear view grows the ladder downward.
	bear := DriftMixture(485, 10, 0.4, -0.2)
	got := ViewLadderEdges(485, 0.4*math.Sqrt(10), bear)
	if b := bear.Buckets(got); got[0] >= LadderEdges(485, 0.4*math.Sqrt(10))[0] || b[0] > 0.12 || !slices.Contains(got, 485) || len(got) > 7 {
		t.Errorf("bear edges %v, buckets %.3f", got, b)
	}
	// A narrow short view: the s < 0.25 row, untouched.
	if got := ViewLadderEdges(485, 0.1, DriftMixture(485, 0.1, 0.3, 0)); !slices.Equal(got, LadderEdges(485, 0.1)) {
		t.Errorf("short edges %v", got)
	}
}

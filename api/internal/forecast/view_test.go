package forecast

import (
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/market"
)

// The stored TSM report's six buckets (AQ-DESIGN §4.8, §6.1).
var tsmOutcomes = []string{"< $300", "$300 – $450", "$450 – $600", "$600 – $800", "$800 – $1,100", "≥ $1,100"}

func tsmDraft() Draft {
	return Draft{Shape: "ordered", Question: "What will be the NYSE closing price of TSM ADR on 2031-10-06?", Outcomes: tsmOutcomes,
		ResolutionDate: "2031-10-06", Category: "markets"}
}

func probsAt(outcomes []string, ps ...float64) []OutcomeP {
	out := make([]OutcomeP, len(outcomes))
	for i, o := range outcomes {
		out[i] = OutcomeP{o, ps[i]}
	}
	return out
}

var tsmRef = &ViewRef{Value: 485, Label: "TSM, NYSE regular-market price", AsOf: "2026-10-06T20:00:00Z", Source: "Yahoo Finance (delayed)"}

// cited is a run's basis with a cited valuation input.
var cited = ViewBasis{Kind: "scenarios", Label: "Valuation scenarios with cited inputs"}

func within(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

// TestBucketDistribution pins §4.8's reading of the buckets on the stored TSM
// report and on §6.1's re-run, and checks the maths on random distributions.
func TestBucketDistribution(t *testing.T) {
	// The stored report: λ_0 0.363, λ_n 0.387; p10 ≈247.5, p25 355.8, median 570.3, p75 917.0, p90 ≈1,350.5.
	bounds, _ := bucketBounds(tsmOutcomes)
	ds, ok := newDist(bounds, []float64{.17, .19, .17, .16, .14, .17})
	if !ok || !within(ds.lam0, 0.363, 5e-4) || !within(ds.lamN, 0.387, 5e-4) {
		t.Fatalf("tails: %+v", ds)
	}
	v := deskView(tsmDraft(), probsAt(tsmOutcomes, .17, .19, .17, .16, .14, .17), tsmRef, 1825, cited)
	switch {
	case !within(v.P10.Value, 247.5, 0.5) || !v.P10.Approx, !within(v.P25.Value, 355.8, 0.5) || v.P25.Approx,
		!within(v.Median.Value, 570.3, 0.5) || v.Median.Approx, !within(v.P75.Value, 917.0, 0.5), !within(v.P90.Value, 1350.5, 0.5) || !v.P90.Approx:
		t.Fatalf("stored quantiles: %+v %+v %+v %+v %+v", v.P10, v.P25, v.Median, v.P75, v.P90)
	case !within(v.PAbove.Value, 0.596, 5e-4) || v.PAbove.Approx, !within(*v.AnnualPct, 3.29, 0.01), !v.Flat, v.Headline != "view", v.Scale != "log":
		t.Fatalf("stored: pAbove %+v annual %v flat %v %s", v.PAbove, *v.AnnualPct, v.Flat, v.Headline)
	case !within(entropy([]float64{.17, .19, .17, .16, .14, .17}), 0.998, 5e-4):
		t.Fatal("entropy")
	}
	// §6.1's final: median 610.9, p10 ≈241.9, p90 ≈1,476, pAbove 0.621, +4.73%/yr.
	v = deskView(tsmDraft(), probsAt(tsmOutcomes, .17, .17, .15, .16, .15, .20), tsmRef, 1825, cited)
	if !within(v.Median.Value, 610.9, 0.5) || !within(v.P10.Value, 241.9, 0.5) || !within(v.P90.Value, 1476, 0.5) || !within(v.PAbove.Value, 0.621, 5e-4) ||
		!within(*v.AnnualPct, 4.73, 0.01) || !within(v.P25.Value, 363, 0.5) || !within(v.P75.Value, 989, 0.5) || !within(*v.ChangePct, 25.96, 0.05) {
		t.Fatalf("§6.1: %+v", v)
	}
	if len(v.Edges) != 5 || v.Edges[0].Value != 300 || !within(*v.Edges[0].VsRefPct, -38.14, 0.01) || v.Unit != (Unit{"$", ""}) {
		t.Fatalf("edges %+v unit %+v", v.Edges, v.Unit)
	}

	// Log-uniform mass in a closed range: its median is the geometric mean.
	three := []string{"< 100", "100 – 400", "≥ 400"}
	b3, _ := bucketBounds(three)
	d3, _ := newDist(b3, []float64{0.1, 0.8, 0.1})
	if q := d3.quantile(0.5); q.Approx || !within(q.Value, 200, 1e-9) {
		t.Fatalf("geometric mean: %+v", q)
	}
	// K = 3 and the tail clamp: a heavy open end is at most twice its neighbour's width.
	d3, _ = newDist(b3, []float64{0.6, 0.1, 0.3})
	if w := math.Log(4); !within(d3.lam0, 2*w, 1e-12) || !within(d3.lamN, 2*w, 1e-12) {
		t.Fatalf("clamp: %v %v", d3.lam0, d3.lamN)
	}
	// Linear scale with negative edges (a growth rate).
	cpi := []string{"< -1%", "-1% – 1%", "1% – 3%", "≥ 3%"}
	bc, _ := bucketBounds(cpi)
	dc, _ := newDist(bc, []float64{0.1, 0.4, 0.4, 0.1})
	if dc.log || !within(dc.quantile(0.5).Value, 1, 1e-9) || !within(dc.cdf(0), 0.3, 1e-9) {
		t.Fatalf("linear: %+v %v", dc.quantile(0.5), dc.cdf(0))
	}
	lv := deskView(Draft{Shape: "ordered", Outcomes: cpi, ResolutionDate: "2027-03-31"}, probsAt(cpi, 0.1, 0.4, 0.4, 0.1), nil, 180, cited)
	if lv.Scale != "linear" || lv.Unit != (Unit{"", "%"}) || lv.PAbove != nil || lv.ChangePct != nil || lv.Lead != "Our median is 1% for Mar 31, 2027; 50% range -0.25%–2.25%, 80% range -1%–3%." {
		t.Fatalf("linear view: %+v %q", lv, lv.Lead)
	}

	// Random distributions: monotone quantiles, F(Q(q)) = q, the reference in either tail.
	r := rand.New(rand.NewPCG(9, 9))
	for range 1000 {
		k := 3 + r.IntN(6)
		ps := make([]float64, k)
		for i := range ps {
			ps[i] = 0.01 + r.Float64()
		}
		edges := make([]float64, k-1)
		x := 10 + r.Float64()*100
		for i := range edges {
			edges[i], x = x, x*(1.05+r.Float64())
		}
		ds, ok := newDist(boundsOf(edges), ps)
		if !ok {
			t.Fatal("newDist")
		}
		prev := 0.0
		for q := 0.01; q < 1; q += 0.01 {
			got := ds.quantile(q).Value
			if got < prev || !within(ds.cdf(got), q, 1e-9) {
				t.Fatalf("q %v: %v (prev %v), F %v", q, got, prev, ds.cdf(got))
			}
			prev = got
		}
		for _, ref := range []float64{edges[0] / 3, edges[k-2] * 3} {
			if p := 1 - ds.cdf(ref); !ds.inTail(ref) || p < 0 || p > 1 {
				t.Fatalf("ref %v in a tail: %v", ref, p)
			}
		}
	}

	// Random linear ladders (negative edges too) and log ladders: the reading keeps each range's mass (F at every
	// edge is the running sum of the buckets), its quantiles are ordered and invert F, P(above) is a probability, and
	// a range given a tiny share still reads as a distribution.
	for n := range 2000 {
		k := 3 + r.IntN(6)
		ps := make([]float64, k)
		sum := 0.0
		for i := range ps {
			ps[i] = r.Float64() * r.Float64()
			if r.IntN(5) == 0 {
				ps[i] = 0 // a model's exact zero, floored
			}
			sum += ps[i]
		}
		if sum == 0 {
			ps[0], sum = 1, 1
		}
		edges := make([]float64, k-1)
		x := -50 + r.Float64()*40 // linear: the first edge is below zero
		if n%2 == 0 {
			x = 0.01 + r.Float64()*1000
		}
		for i := range edges {
			edges[i] = x
			if n%2 == 0 {
				x *= 1.01 + 3*r.Float64()
			} else {
				x += 0.01 + 10*r.Float64()
			}
		}
		ds, ok := newDist(boundsOf(edges), ps)
		if !ok || ds.log != (n%2 == 0) {
			t.Fatalf("newDist %v %v", edges, ps)
		}
		for i, e := range edges {
			if !within(ds.cdf(e), sumTo(ds.p, i+1), 1e-9) {
				t.Fatalf("mass at edge %d (%v): F %v, buckets %v", i, e, ds.cdf(e), ds.p)
			}
		}
		qs := []float64{0.1, 0.25, 0.5, 0.75, 0.9}
		prev := math.Inf(-1)
		for _, q := range qs {
			v := ds.quantile(q)
			if v.Value < prev || math.IsNaN(v.Value) || !within(ds.cdf(v.Value), q, 1e-7) {
				t.Fatalf("q %v: %+v (prev %v) F %v on %v %v", q, v, prev, ds.cdf(v.Value), edges, ds.p)
			}
			prev = v.Value
		}
		ref := edges[r.IntN(len(edges))] + (r.Float64()-0.5)*(edges[len(edges)-1]-edges[0]+1)
		if ds.log && ref <= 0 {
			ref = edges[0] / 2
		}
		if p := 1 - ds.cdf(ref); p < -1e-12 || p > 1+1e-12 || math.IsNaN(p) {
			t.Fatalf("P(above %v) = %v", ref, p)
		}
	}
}

// sumTo is the sum of ps[:i].
func sumTo(ps []float64, i int) float64 {
	s := 0.0
	for _, p := range ps[:i] {
		s += p
	}
	return s
}

// TestFlatHeadline: a flat or long-horizon view leads with the median; only a
// peaked short ladder leads with its range.
func TestFlatHeadline(t *testing.T) {
	if v := deskView(tsmDraft(), probsAt(tsmOutcomes, .17, .19, .17, .16, .14, .17), nil, 1825, cited); !v.Flat || v.Headline != "view" {
		t.Fatalf("TSM: %v %s", v.Flat, v.Headline)
	}
	btc := []string{"< $60,000", "$60,000 – $65,000", "$65,000 – $70,000", "$70,000 – $75,000", "≥ $75,000"}
	d := Draft{Shape: "ordered", Outcomes: btc, ResolutionDate: "2026-10-14"}
	if v := deskView(d, probsAt(btc, .05, .2, .46, .2, .09), nil, 7, cited); v.Flat || v.Headline != "bucket" {
		t.Fatalf("7-day 46%%: %v %s", v.Flat, v.Headline)
	}
	if v := deskView(d, probsAt(btc, .05, .2, .46, .2, .09), nil, 730, cited); v.Headline != "view" {
		t.Fatalf("2-year peaked: %s", v.Headline)
	}
	if v := deskView(d, probsAt(btc, .05, .38, .40, .1, .07), nil, 7, cited); !v.Flat || v.Headline != "view" {
		t.Fatalf("top two within 3 points: %v %s", v.Flat, v.Headline)
	}
}

// TestUnitAndLead: currency marks, suffixes, compact levels and §4.8's exact leads.
func TestUnitAndLead(t *testing.T) {
	for labels, want := range map[string]Unit{
		"< S$0.50|S$0.50 – S$0.60": {"S$", ""}, "< NT$900|NT$900 – NT$1,000": {"NT$", ""}, "< 2.5%|2.5% – 3%": {"", "%"},
		"< 25bp|25 bp – 50 bp": {"", "bp"}, "Under $80k|$80k – $90k": {"$", ""}, "< 100|100 – 200": {"", ""}, "< US$5|US$5 – 6": {"US$", ""},
	} {
		if got := unitOf(strings.Split(labels, "|")); got != want {
			t.Errorf("unitOf(%q) = %+v, want %+v", labels, got, want)
		}
	}
	usd := Unit{"$", ""}
	for x, want := range map[float64]string{610.9: "$611", 1476: "$1,480", 241.9: "$242", 67400: "$67,400", 2.12e6: "$2.12M", 1.2e12: "$1.20T",
		9.5e8: "$950M", 0.08823: "$0.0882", 96.44: "$96.4", 0: "$0"} {
		if got := fmtLevel(x, usd); got != want {
			t.Errorf("fmtLevel(%v) = %q, want %q", x, got, want)
		}
	}
	if got := fmtLevel(2.5, Unit{"", "%"}); got != "2.5%" {
		t.Errorf("percent: %q", got)
	}
	if got := fmtLevel(0.552, Unit{"S$", ""}); got != "S$0.552" {
		t.Errorf("S$: %q", got)
	}
	v := deskView(tsmDraft(), probsAt(tsmOutcomes, .17, .17, .15, .16, .15, .20), tsmRef, 1825, cited)
	if want := "Our median is $611 for Oct 6, 2031, +26% from today's $485 (about +4.7% a year); 50% range $363–$989, 80% range ≈$242–≈$1,480; 62% chance it closes at or above $485."; v.Lead != want {
		t.Errorf("lead with a reference:\n%s\n%s", v.Lead, want)
	}
	v = deskView(tsmDraft(), probsAt(tsmOutcomes, .17, .19, .17, .16, .14, .17), nil, 1825, cited)
	if want := "Our median is $570 for Oct 6, 2031; 50% range $356–$917, 80% range ≈$247–≈$1,350."; v.Lead != want {
		t.Errorf("lead without:\n%s\n%s", v.Lead, want)
	}
	v = deskView(tsmDraft(), probsAt(tsmOutcomes, .05, .05, .05, .1, .15, .6), tsmRef, 1825, cited)
	if !v.Median.Approx || !strings.HasPrefix(v.Lead, "Our median is above $1,100 for Oct 6, 2031; 50% range ") {
		t.Errorf("a median in a tail: %q", v.Lead)
	}
	// No cited scenarios and no measured history: the ranges only, no P(above) or change, even with today's level.
	v = deskView(tsmDraft(), probsAt(tsmOutcomes, .17, .19, .17, .16, .14, .17), tsmRef, 1825, noBasis)
	if want := "The desk's 50% range for Oct 6, 2031 is $356–$917 and its 80% range ≈$247–≈$1,350."; v.Lead != want || v.PAbove != nil ||
		v.ChangePct != nil || v.AnnualPct != nil || v.Basis.Kind != "none" || v.brief().Basis != "none" || *v.Edges[0].VsRefPct > -38 {
		t.Errorf("no basis: %q %+v", v.Lead, v)
	}
	if b := (&DeskView{}).brief(); b.Basis != "none" {
		t.Errorf("a stored view without a basis reads as none: %+v", b)
	}

	// Today's level at its own precision (the shared FormatLevel rule), the level P(above) and the change are
	// computed at: the live check's leads read "today's $83,000", "S$73.9" and "at or above $83,000".
	for _, c := range []struct {
		ref      float64
		outcomes []string
		today    string
	}{
		{472.2, tsmOutcomes, "$472.20"},
		{83_048, []string{"< $60,000", "$60,000 – $80,000", "$80,000 – $100,000", "$100,000 – $120,000", "≥ $120,000"}, "$83,048"},
		{73.85, []string{"< S$60", "S$60 – S$70", "S$70 – S$80", "S$80 – S$90", "≥ S$90"}, "S$73.85"},
		{262_000, []string{"< ₩200,000", "₩200,000 – ₩250,000", "₩250,000 – ₩300,000", "₩300,000 – ₩350,000", "≥ ₩350,000"}, "₩262,000"},
		{1_047_300, []string{"< ₩800,000", "₩800,000 – ₩1,000,000", "₩1,000,000 – ₩1,200,000", "₩1,200,000 – ₩1,400,000", "≥ ₩1,400,000"}, "₩1,047,300"},
	} {
		d := Draft{Shape: "ordered", Outcomes: c.outcomes, ResolutionDate: "2027-10-07"}
		ps := []float64{.1, .2, .3, .2, .2}
		if len(c.outcomes) == 6 {
			ps = append(ps, .1)
			ps[0] = 0
		}
		v := deskView(d, probsAt(c.outcomes, ps...), &ViewRef{Value: c.ref}, 365, cited)
		if !strings.Contains(v.Lead, " from today's "+c.today+" (about ") || !strings.HasSuffix(v.Lead, " chance it closes at or above "+c.today+".") {
			t.Errorf("today's %v: %q", c.ref, v.Lead)
		}
	}
	if todayLevel(2.4, Unit{"", "%"}) != "2.4%" || todayLevel(472.2, usd) != "$472.20" || todayLevel(-3, Unit{"", "bp"}) != "-3bp" {
		t.Errorf("todayLevel: %s %s %s", todayLevel(2.4, Unit{"", "%"}), todayLevel(472.2, usd), todayLevel(-3, Unit{"", "bp"}))
	}
	if fmtPrice(0.06152, usd) != "$0.0615" || fmtPrice(485, usd) != "$485" || fmtPrice(999.996, usd) != "$1,000" || fmtPrice(9.5e6, Unit{Prefix: "IDR "}) != "IDR 9,500,000" ||
		fmtPrice(2e15, usd) != "$2,000T" {
		t.Errorf("fmtPrice: %s %s %s %s", fmtPrice(0.06152, usd), fmtPrice(485, usd), fmtPrice(999.996, usd), fmtPrice(9.5e6, Unit{Prefix: "IDR "}))
	}
}

// TestSanitizeScenarios: levels from the record, a tail from the stress
// events, the partition and the odds from §6.1's final distribution.
func TestSanitizeScenarios(t *testing.T) {
	lv := []market.ScenarioLevel{
		{Name: "AI capex digestion", Kind: "bear", P: 0.3, Level: 346.2, Inputs: []market.Input{{Field: "eps", Value: 21.33, Basis: "F1", Kind: "source", Verified: true}}},
		{Name: "Compounding foundry leader", Kind: "base", P: 0.5, Level: 751.8},
		{Name: "AI supercycle", Kind: "bull", P: 0.2, Level: 1326.9},
	}
	events := []stressEvent{{Name: "AI-capex bust", P: 0.10, Move: -0.35}, {Name: "Taiwan blockade or conflict", P: 0.05, Move: -0.60,
		Inputs: []market.Input{{Field: "eventP", Value: 0.05, Basis: "assumption", Kind: "assumption"}}}, {Name: "Tiny", P: 0.02, Move: -0.9}}
	texts := []scenarioText{{Kind: "base", Thesis: "FY2032 EPS near $38 (from $21.33 FY2027, F1) at about 20x. " + strings.Repeat("x", 300),
		MustBeTrue: []string{"EPS growth near 12%/yr (F1, F9)", " ", "b", "c", "d"}}, {Kind: "BULL", Thesis: "Multiple held near 25x, below today's 29.3x (S5)."}}
	known := func(id string) bool { return id == "F1" || id == "S5" }
	sc := sanitizeScenarios(tsmDraft(), probsAt(tsmOutcomes, .17, .17, .15, .16, .15, .20), tsmRef, 1825, lv, events, 580, texts, known)
	if len(sc) != 4 || sc[0].Kind != "tail" || sc[0].Name != "Taiwan blockade or conflict" || !within(sc[0].Level, 232, 1e-9) || *sc[0].ModelP != 0.05 ||
		sc[0].Lo != nil || !within(*sc[0].Hi, 283.4, 0.1) || !within(*sc[1].Hi, 510.2, 0.1) || !within(*sc[2].Hi, 998.8, 0.2) || sc[3].Hi != nil {
		t.Fatalf("partition: %+v", sc)
	}
	sum := 0.0
	for i, want := range []float64{0.148, 0.258, 0.349, 0.245} {
		if !within(sc[i].P, want, 1.5e-3) {
			t.Errorf("%s p %v, want %v", sc[i].Kind, sc[i].P, want)
		}
		sum += sc[i].P
	}
	if !within(sum, 1, 1e-9) || sc[1].Inputs[0].Basis != "F1" || len(sc[2].Thesis) > 240*4 || len([]rune(sc[2].Thesis)) != 240 ||
		!slices.Equal(sc[2].MustBeTrue, []string{"EPS growth near 12%/yr (F1, F9)", "b", "c"}) || !slices.Equal(sc[2].SourceIDs, []string{"F1"}) ||
		!slices.Equal(sc[3].SourceIDs, []string{"S5"}) || sc[1].Thesis != "" || !within(*sc[2].VsRefPct, 55.0, 0.1) || !within(*sc[3].AnnualPct, 22.3, 0.1) {
		t.Fatalf("texts and numbers: %+v", sc)
	}
	// A model tail stops the stress tail, an upside one above the bull does
	// not; one scenario alone is no list.
	lv[2].Kind = "tail"
	if sc := sanitizeScenarios(tsmDraft(), probsAt(tsmOutcomes, .17, .17, .15, .16, .15, .20), tsmRef, 1825, lv, events, 580, nil, known); len(sc) != 4 ||
		sc[0].Name != "Taiwan blockade or conflict" || sc[3].Kind != "tail" {
		t.Fatalf("with an upside model tail: %+v", sc)
	}
	// Two tails, one "tail" text: the super-cycle's thesis goes to the model's
	// own tail (the aggregator wrote it for the scenarios under Methods), by
	// name or, unnamed, before the stress tail; never to the blockade. A text
	// named for the blockade goes to it, wherever it is listed.
	up := scenarioText{Kind: "tail", Name: "AI supercycle", Thesis: "AI accelerator demand doubles again (S5)."}
	war := scenarioText{Kind: "tail", Name: "taiwan blockade or conflict ", Thesis: "A blockade halts the fabs."}
	unnamed := up
	unnamed.Name = ""
	for name, c := range map[string]struct {
		texts    []scenarioText
		war, top string
	}{
		"named": {[]scenarioText{up}, "", up.Thesis}, "unnamed": {[]scenarioText{unnamed}, "", up.Thesis},
		"both": {[]scenarioText{war, up}, war.Thesis, up.Thesis}, "war only": {[]scenarioText{war}, war.Thesis, ""},
	} {
		sc := sanitizeScenarios(tsmDraft(), probsAt(tsmOutcomes, .17, .17, .15, .16, .15, .20), tsmRef, 1825, lv, events, 580, c.texts, known)
		if len(sc) != 4 || sc[0].Thesis != c.war || sc[3].Name != "AI supercycle" || sc[3].Thesis != c.top {
			t.Errorf("tail texts %s: %q / %q", name, sc[0].Thesis, sc[3].Thesis)
		}
	}
	lv[0].Kind, lv[2].Kind = "tail", "bull"
	if sc := sanitizeScenarios(tsmDraft(), probsAt(tsmOutcomes, .17, .17, .15, .16, .15, .20), tsmRef, 1825, lv, events, 580, nil, known); len(sc) != 3 {
		t.Fatalf("with a model tail: %d", len(sc))
	}
	if sc := sanitizeScenarios(tsmDraft(), probsAt(tsmOutcomes, .17, .17, .15, .16, .15, .20), tsmRef, 1825, lv[:1], nil, 580, nil, known); sc != nil {
		t.Fatalf("one scenario: %+v", sc)
	}
}

// TestAnswerFirst: process sentences go; an all-process numeric summary
// becomes the lead; "$1.5" is not a sentence break.
func TestAnswerFirst(t *testing.T) {
	tsm := "We stay close to the ensemble. A lognormal with a median near $580 reproduces the specialists' numbers. We expect TSM to compound with earnings at $1.5 trillion of capex. The main risk is a Taiwan shock."
	if got := answerFirst(tsm, "Our median is $611."); got != "We expect TSM to compound with earnings at $1.5 trillion of capex. The main risk is a Taiwan shock." {
		t.Errorf("TSM: %q", got)
	}
	if got := answerFirst("The panel averages the specialists. The pool is wide.", "Our median is $611."); got != "Our median is $611." {
		t.Errorf("all process: %q", got)
	}
	if got := answerFirst("The panel puts \"Yes\" at 30%.", ""); got != "The panel puts \"Yes\" at 30%." {
		t.Errorf("binary, nothing else: %q", got)
	}
	if got := answerFirst("We stick with the ensemble. We lean No: ETF outflows (S2).", ""); got != "We lean No: ETF outflows (S2)." {
		t.Errorf("binary process talk: %q", got)
	}
	if got := answerFirst(strings.Repeat("a", 700), ""); len([]rune(got)) != 600 {
		t.Errorf("cap: %d", len(got))
	}
}

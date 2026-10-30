package forecast

import (
	"cmp"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/market"
)

// member builds a panel member with a method and, when anchored, an anchor.
func seatOf(role, method string, anchored bool, ps ...float64) Specialist {
	s := Specialist{Role: role, Name: roles[roleIndex(role)].Name, Method: method, Probabilities: probsAt(tsmOutcomes, ps...)}
	if anchored {
		s.Anchor = probsAt(tsmOutcomes, ps...)
	}
	return s
}

var (
	flatTSM = []float64{.17, .19, .17, .16, .14, .17}
	nearTSM = []float64{.18, .18, .17, .16, .15, .16}
)

// TestAgreement: near-identical members without their own method's data are
// one view; anchored members with different methods agree even within the
// duplicate distance; anything else is diverse.
func TestAgreement(t *testing.T) {
	// The stored TSM report: three judgment members, about equal.
	panel := []Specialist{seatOf("base_rate", "judgment", false, flatTSM...), seatOf("quant", "judgment", false, nearTSM...),
		seatOf("skeptic", "judgment", false, .17, .17, .18, .16, .15, .17)}
	ag := agree(panel)
	if ag.class != "duplicate" || len(ag.groups) != 1 || len(ag.groups[0]) != 3 {
		t.Fatalf("TSM-like panel: %+v", ag)
	}
	f := duplicateFlag(panel, ag.groups[0], false)
	if f.Type != "duplicate" || f.Note != "Base-rate statistician, Market & quant analyst and Narrative & macro skeptic gave near-identical numbers without their own method's tool data, so they count as one view (largest gap 2 points)." {
		t.Fatalf("flag %+v", f)
	}
	res := Result{Specialists: panel, Probabilities: probsAt(tsmOutcomes, flatTSM...), Confidence: "high",
		Flags: []Flag{{"duplicate", "All three used the same lognormal."}, {"duplicate", "S2 and S3 repeat one report."}}}
	k := &deskKit{d: tsmDraft(), fr: frameOf(tsmDraft(), frameDay), logs: map[string]*toolLog{}}
	k.finish(&res, aggExtras{}, ag)
	dups := slices.DeleteFunc(slices.Clone(res.Flags), func(f Flag) bool { return f.Type != "duplicate" })
	if res.Confidence != "medium" || len(dups) != 2 || dups[0].Note != "S2 and S3 repeat one report." || !strings.HasPrefix(dups[1].Note, "Base-rate statistician, Market") {
		t.Fatalf("the server's flag replaces the model's method flag; confidence capped: %s %+v", res.Confidence, res.Flags)
	}

	// Three anchored, distinct methods within 0.05 of each other agree: no flag.
	panel = []Specialist{seatOf("base_rate", "history", true, flatTSM...), seatOf("quant", "fundamentals", true, nearTSM...),
		seatOf("skeptic", "regime", true, .16, .19, .17, .17, .14, .17)}
	if ag := agree(panel); ag.class != "independent_agree" || ag.groups != nil {
		t.Fatalf("independent agreement: %+v", ag)
	}
	res = Result{Specialists: panel, Probabilities: probsAt(tsmOutcomes, flatTSM...), Confidence: "high", Flags: []Flag{{"duplicate", "The same model twice."}}}
	k.finish(&res, aggExtras{}, agree(panel))
	if res.Confidence != "high" || slices.ContainsFunc(res.Flags, func(f Flag) bool { return f.Type == "duplicate" }) || res.Methods.Agreement != "independent_agree" {
		t.Fatalf("independent agreement is never flagged: %+v", res)
	}

	// A diverse panel: TV 0.21 between two anchored members.
	panel[1] = seatOf("quant", "fundamentals", true, .05, .10, .15, .20, .25, .25)
	if ag := agree(panel); ag.class != "diverse" || tv(panel[0].Probabilities, panel[1].Probabilities) < 0.2 {
		t.Fatalf("diverse: %+v", ag)
	}
	// Two volatility members at TV 0.04 duplicate one method; a different, anchored third stays out.
	panel = []Specialist{seatOf("base_rate", "volatility", true, flatTSM...), seatOf("quant", "volatility", true, .19, .17, .17, .16, .14, .17),
		seatOf("skeptic", "regime", true, .30, .20, .15, .15, .10, .10)}
	if ag := agree(panel); ag.class != "duplicate" || !slices.Equal(ag.groups[0], []int{0, 1}) || len(ag.groups) != 1 {
		t.Fatalf("two volatility members: %+v", ag)
	} else if f := duplicateFlag(panel, ag.groups[0], false); f.Note != "Base-rate statistician and Market & quant analyst gave near-identical numbers from the same kind of model, so they count as one view (largest gap 2 points)." {
		t.Fatalf("two models of one kind ran their tools: %q", f.Note)
	}
	// Fewer than two anchored members, all within 0.06: one view; the flag says only of the bare members that they
	// lacked their method's data.
	panel = []Specialist{seatOf("base_rate", "history", true, flatTSM...), seatOf("quant", "fundamentals", false, nearTSM...), seatOf("skeptic", "regime", false, flatTSM...)}
	if ag := agree(panel); ag.class != "duplicate" || len(ag.groups[0]) != 3 {
		t.Fatalf("one anchor: %+v", ag)
	} else if f := duplicateFlag(panel, ag.groups[0], false); f.Note != "Base-rate statistician, Market & quant analyst and Narrative & macro skeptic gave near-identical numbers (Market & quant analyst and Narrative & macro skeptic without their own method's tool data), so they count as one view (largest gap 1 point)." {
		t.Fatalf("one anchored member in the group: %q", f.Note)
	}

	// Stated vs implied median, in the Methods block.
	d := tsmDraft()
	fr := frameOf(d, frameDay)
	q := seatOf("quant", "fundamentals", false, flatTSM...)
	q.Stats = statsOf(d, q.Probabilities, tsmRef, ptr(800.0))
	views := panelViews(d, fr, []Specialist{q}, map[string]*toolLog{"quant": {scen: &scenarioRecord{levels: []market.ScenarioLevel{{Kind: "base", Level: 751.8, P: 1}}}}},
		&measurement{ref: tsmRef}, agreement{class: "diverse", gap: 9}, q.Probabilities)
	for _, want := range []string{"- Market & quant analyst · fundamentals (scenario_model: base $752 100%) · prior weight 0.45 · its forecast: median $570, 50% $356–$917, 80% ≈$247–≈$1,350, P(≥ $485) 60%. Stated median $800; its probabilities imply $570.",
		"Agreement: diverse (largest gap between two members on one range: 9 points).", `Prior-weighted pool: "< $300" 17%;`} {
		if !strings.Contains(views, want) {
			t.Errorf("Methods block lacks %q:\n%s", want, views)
		}
	}
	q.Stats.StatedMedian = ptr(600.0) // within 15% (log): no line
	if v := panelViews(d, fr, []Specialist{q}, map[string]*toolLog{}, nil, agreement{class: "duplicate", groups: [][]int{{0}}}, q.Probabilities); strings.Contains(v, "Stated median") ||
		!strings.Contains(v, "Agreement: duplicate (Market & quant analyst near-identical: count them as one view). Methods: fundamentals.") {
		t.Fatalf("no gap line within 15%%: %s", v)
	}
}

// TestRetry: a duplicate panel re-runs its first judgment member in role
// order, never a fallback one, and tells it its method's tool.
func TestRetry(t *testing.T) {
	panel := []Specialist{seatOf("base_rate", "history", true, .30, .20, .15, .15, .10, .10), seatOf("quant", "judgment", false, flatTSM...),
		seatOf("skeptic", "judgment", false, flatTSM...)}
	ag := agree(panel)
	if i := retryPick(panel, ag); ag.class != "duplicate" || i != 1 {
		t.Fatalf("the quant is the first judgment member: %d %+v", i, ag)
	}
	panel[1].Method = "fallback"
	if i := retryPick(panel, agree(panel)); i != 2 {
		t.Fatalf("a fallback member is never retried: %d", i)
	}
	panel[2].Method = "fallback"
	if i := retryPick(panel, agree(panel)); i != -1 {
		t.Fatalf("no judgment member: %d", i)
	}
	if retryPick(panel, agreement{class: "diverse"}) != -1 {
		t.Fatal("only a duplicate panel retries")
	}
	long, crypto := frameOf(tsmDraft(), frameDay), frameOf(btcLong(), frameDay)
	for _, c := range []struct {
		role string
		fr   frame
		want string
	}{{"base_rate", long, "history_stats"}, {"quant", long, "scenario_model"}, {"quant", crypto, "price_outlook"}, {"skeptic", long, "stress_mix"}} {
		if got := methodTool(c.role, c.fr); got != c.want {
			t.Errorf("%s: %s", c.role, got)
		}
	}
	short := tsmDraft()
	short.ResolutionDate = frameDay.AddDate(0, 3, 0).Format(time.DateOnly)
	if !retryDue(long, 80*time.Second) || retryDue(long, 70*time.Second) || retryDue(frameOf(short, frameDay), 2*time.Minute) || !retryDue(crypto, retryMinLeft) {
		t.Fatal("a retry: long-horizon price frames only, with at least 75 s of the panel's time left")
	}
	if got := fmt.Sprintf(retryNote, "stress_mix"); got != "You did not use your method. Call stress_mix as your brief says, then reply with only the JSON object." {
		t.Fatal(got)
	}
}

// TestPoolAndGuard: the desk's weights when each is 0.10-0.70 (else the
// priors), duplicates counted once at their largest weight, a failed member
// renormalized away; the guard blends a too-sharp or too-narrow final
// halfway back to the pool, never on independent agreement, and never
// sharpens.
func TestPoolAndGuard(t *testing.T) {
	fr := frameOf(tsmDraft(), frameDay)
	panel := []Specialist{seatOf("base_rate", "history", true, .30, .20, .15, .15, .10, .10), seatOf("quant", "fundamentals", true, .10, .15, .15, .20, .20, .20),
		seatOf("skeptic", "regime", true, .25, .20, .15, .15, .15, .10)}
	w, why, from := panelWeights([]rawWeight{{"base_rate", 0.2, " why "}, {"QUANT", 0.5, ""}, {"skeptic", 0.3, "tails"}}, panel, fr)
	if from != "desk" || w["quant"] != 0.5 || why["base_rate"] != "why" || why["quant"] != "" {
		t.Fatalf("desk weights %v %v %s", w, why, from)
	}
	if w, _, from := panelWeights([]rawWeight{{"base_rate", 0.1, ""}, {"quant", 0.8, ""}, {"skeptic", 0.1, ""}}, panel, fr); from != "prior" || w["quant"] != 0.45 {
		t.Fatalf("0.8 is out of bounds: the priors (%v %s)", w, from)
	}
	if _, _, from := panelWeights(nil, panel, fr); from != "prior" {
		t.Fatal("no weights: the priors")
	}
	pl, eff := pool(panel, weightsFor(fr), nil)
	if !within(pl[0].P, 0.3*0.30+0.45*0.10+0.25*0.25, 1e-12) || !within(eff[1], 0.45, 1e-12) {
		t.Fatalf("pool %v eff %v", pl, eff)
	}
	merged, eff := pool(panel, weightsFor(fr), [][]int{{1, 2}}) // quant and skeptic one view, at the quant's 0.45
	if want := (0.3*0.30 + 0.45*(0.10+0.25)/2) / 0.75; !within(merged[0].P, want, 1e-12) || !within(eff[1]+eff[2], 0.6, 1e-12) || !within(eff[0], 0.4, 1e-12) {
		t.Fatalf("merged duplicates %v eff %v", merged, eff)
	}
	two, eff := pool(panel[:2], weightsFor(fr), nil) // the skeptic failed
	if !within(eff[0]+eff[1], 1, 1e-12) || !within(two[0].P, (0.3*0.30+0.45*0.10)/0.75, 1e-12) {
		t.Fatalf("renormalized %v %v", two, eff)
	}

	d := tsmDraft()
	width := func(p []OutcomeP) float64 { return logWidth(d, p) }
	sharp := probsAt(tsmOutcomes, .02, .03, .05, .80, .05, .05)
	got, trig := guardFinal(sharp, pl, "diverse", width, 0)
	if trig != "sharpness" || !within(got[3].P, (0.80+pl[3].P)/2, 1e-12) {
		t.Fatalf("sharpness: %s %v", trig, got)
	}
	if got, trig := guardFinal(sharp, pl, "independent_agree", width, 0); trig != "" || got[3].P != 0.80 {
		t.Fatal("independent agreement may be sharper")
	}
	// Narrower than the measured range but within the sharpness slack (TV 0.08): the width trigger widens it.
	narrow := probsAt(tsmOutcomes, pl[0].P-.04, pl[1].P, pl[2].P+.04, pl[3].P+.04, pl[4].P, pl[5].P-.04)
	pw, nw := width(pl), width(narrow)
	if got, trig := guardFinal(narrow, pl, "diverse", width, 2*nw); trig != "width" || !within(got[0].P, (narrow[0].P+pl[0].P)/2, 1e-12) || width(got) <= nw || nw >= pw {
		t.Fatalf("width (0.5 × the measured 80%% range): %s %v (final %v, pool %v)", trig, got, nw, pw)
	}
	if _, trig := guardFinal(narrow, pl, "diverse", width, nw/0.8); trig != "" {
		t.Fatal("width at 0.8 × the reference passes")
	}
	// A final that is the pool, or a pool narrower than the final: a blend would not widen it, so no guard.
	if got, trig := guardFinal(pl, pl, "diverse", width, 2*pw); trig != "" || got[0].P != pl[0].P {
		t.Fatalf("a no-op guard on the pool itself: %s", trig)
	}
	if got, trig := guardFinal(pl, narrow, "diverse", width, 2*pw); trig != "" || got[0].P != pl[0].P {
		t.Fatalf("a width guard that would narrow: %s", trig)
	}
	flatter := probsAt(tsmOutcomes, .17, .17, .16, .17, .16, .17)
	if got, trig := guardFinal(flatter, pl, "diverse", width, 0); trig != "" || entropy(probsOfOrder(got)) < entropy(probsOfOrder(flatter))-1e-12 {
		t.Fatal("the guard never sharpens")
	}
	// Random finals and pools: whatever fires, the result is never narrower nor less entropic than the final.
	rng := rand.New(rand.NewPCG(5, 5))
	for range 2000 {
		ps, qs := make([]float64, 6), make([]float64, 6)
		for i := range ps {
			ps[i], qs[i] = 0.01+rng.Float64()*rng.Float64(), 0.01+rng.Float64()
		}
		f, p := rescale(probsAt(tsmOutcomes, ps...)), rescale(probsAt(tsmOutcomes, qs...))
		got, trig := guardFinal(f, p, "diverse", width, 3*width(f)*rng.Float64())
		switch {
		case trig == "" && tv(got, f) != 0:
			t.Fatalf("moved without a guard: %v → %v", f, got)
		case trig == "sharpness" && entropy(probsOfOrder(got)) < entropy(probsOfOrder(f)):
			t.Fatalf("sharpness guard sharpened: %v → %v (pool %v)", f, got, p)
		case trig == "width" && width(got) <= width(f):
			t.Fatalf("width guard narrowed: %v → %v (pool %v)", f, got, p)
		}
	}

	// finish records the weights, the pool and the guard's sentence.
	res := Result{Specialists: panel, Probabilities: sharp, Confidence: "medium"}
	k := &deskKit{d: tsmDraft(), fr: fr, logs: map[string]*toolLog{}}
	k.finish(&res, aggExtras{Weights: []rawWeight{{"base_rate", 0.3, "ref"}, {"quant", 0.45, ""}, {"skeptic", 0.25, ""}}, MethodNote: strings.Repeat("n", 400)}, agree(panel))
	m := res.Methods
	if m.WeightsFrom != "desk" || m.Guard == nil || *m.Guard != "sharpness" || !strings.HasSuffix(m.Note, " "+guardSentence) || len([]rune(m.Note)) != 300+1+len([]rune(guardSentence)) ||
		*m.Weights[0].Why != "ref" || m.Weights[1].Why != nil || m.Plan != "long_stock" || res.Probabilities[3].P >= 0.80 {
		t.Fatalf("methods %+v probs %v", m, res.Probabilities)
	}
}

// TestViewBasis: a view rests on cited valuation scenarios, else on the
// measured history's capped drift only while it puts no more of its
// probability above the drift's own median than the drift's lognormal does,
// both read off the ranges, beyond the precision finals are written in (the
// cap is a ceiling), with the drift's own median as the most it states, else
// on nothing.
func TestViewBasis(t *testing.T) {
	k := &deskKit{d: tsmDraft(), fr: frameOf(tsmDraft(), frameDay), logs: map[string]*toolLog{}}
	edges := edgesOf(k.fr.Bounds)
	logn := market.DriftMixture(485, 1825.0/365, 0.38, 0.09).Buckets(edges) // H1's capped-drift lognormal on the TSM ranges
	k.m = &measurement{sub: subject{Symbol: "TSM"}, ref: tsmRef, h1: true, lognormal: logn, mu: 0.09, stats: market.HistoryStats{From: "1997-10-01"}}
	at := 485 * math.Exp(0.09*1825/365) // the drift's own median, $760.6
	panel := []Specialist{{Role: "base_rate"}, {Role: "quant"}, {Role: "skeptic"}}
	// The drift +0.8 points a year faster over these five years: 0.04 (log)
	// above the drift's median, inside the old 5% slack, and 1.9 points of
	// probability more above it than the drift puts there.
	faster := market.DriftMixture(485, 1825.0/365, 0.38, 0.09+0.008).Buckets(edges)
	for _, c := range []struct {
		name string
		ps   []float64
		want string
	}{
		{"the lognormal itself", logn, "capped_drift"},
		{"§6.1's final, median $611", []float64{.17, .17, .15, .16, .15, .20}, "capped_drift"},
		{"a bearish final", []float64{.40, .20, .15, .10, .10, .05}, "capped_drift"},
		{"a centre far above the cap", []float64{.02, .03, .05, .10, .30, .50}, "none"},
		{"0.8 points a year above the drift", faster, "none"},
	} {
		final := probsAt(tsmOutcomes, c.ps...)
		b := k.basis(panel, final)
		if b.Kind != c.want || (c.want == "none" && b != aboveDrift) || (c.want == "capped_drift" && (!strings.HasPrefix(b.Label, "Measured TSM history since 1997; drift ") || math.Abs(b.ceiling-at) > 1e-9)) {
			t.Errorf("%s: %+v", c.name, b)
		}
		if v := deskView(tsmDraft(), final, tsmRef, 1825, b); b.Kind == "capped_drift" && (v.Median.Value > at || v.Basis.Label != b.Label+heldNoteWant(tsmDraft(), final, tsmRef, 1825, b, v)) {
			t.Errorf("%s: median %.2f under the drift's %.2f, label %q", c.name, v.Median.Value, at, v.Basis.Label)
		}
	}
	// A 50% range above the drift's own median cannot hold its median at the
	// drift, however close the two read: a $1-wide range just above it holding
	// 70% reads within 0.03 points a year of the drift, but 80% of the final
	// sits above the drift's median.
	narrow := []string{"< $300", "$300 – $450", "$450 – $600", "$600 – $761", "$761 – $762", "≥ $762"}
	kn := &deskKit{d: Draft{Shape: "ordered", Outcomes: narrow, ResolutionDate: tsmDraft().ResolutionDate}, fr: k.fr, logs: map[string]*toolLog{}}
	kn.m = &measurement{sub: subject{Symbol: "TSM"}, ref: tsmRef, h1: true, mu: 0.09, stats: market.HistoryStats{From: "1997-10-01"},
		lognormal: market.DriftMixture(485, 1825.0/365, 0.38, 0.09).Buckets([]float64{300, 450, 600, 761, 762})}
	if b := kn.basis(panel, probsAt(narrow, .02, .03, .05, .10, .70, .10)); b != aboveDrift {
		t.Errorf("a 50%% range above the drift's median: %+v", b)
	}
	if b := kn.basis(panel, probsAt(narrow, kn.m.lognormal...)); b.Kind != "capped_drift" {
		t.Errorf("the drift's lognormal on narrow ranges: %+v", b)
	}
	// A cited valuation input may carry the centre above the drift.
	q := newToolLog()
	q.scen = &scenarioRecord{levels: []market.ScenarioLevel{{Kind: "base", Level: 900, Inputs: []market.Input{{Field: "eps", Value: 21.33, Basis: "F1", Kind: "source", Verified: true}}}}}
	k.logs["quant"] = q
	if b := k.basis(panel, probsAt(tsmOutcomes, .02, .03, .05, .10, .30, .50)); b.Kind != "scenarios" || b.ceiling != 0 {
		t.Errorf("cited scenarios: %+v", b)
	}
	// Without the quant in the panel (it failed) its record does not count; without H1 there is no basis.
	if b := k.basis(panel[:1], probsAt(tsmOutcomes, .02, .03, .05, .10, .30, .50)); b != aboveDrift {
		t.Errorf("a failed quant's record: %+v", b)
	}
	k.m = nil
	if b := k.basis(panel[:1], probsAt(tsmOutcomes, logn...)); b != noBasis {
		t.Errorf("no measurement: %+v", b)
	}
}

// TestDriftCeiling: on the live check's inputs (TSM, NVDA, Samsung in ₩ and
// BTC: today's level, full-history σ, the capped drift) and their draft
// ladders (σ_5y), at every horizon and whatever the final's drift:
//   - no capped_drift view states a pace above its label's: its median is at
//     most the drift's own, today's level × e^{μT}, and its lead's "about +x%
//     a year" prints the label's figure at most (the old 5% log slack let
//     NVDA's 1-year lead read "about +15.9% a year" under +10.0%/yr);
//   - nor a chance of closing at or above today above the drift lognormal's
//     own, read off the same ranges, plus driftMassTol;
//   - a final that is the drift's lognormal, or one under it, keeps its
//     median, and so does it as H1 prints it (0.1 point a range): read
//     against the drift's exact pace, its median read high in a wide range
//     and 7 of the live check's 22 ladders (NVDA at 90 days: +2.58% against
//     +2.38%) said "its centre sits above" the drift it was;
//   - a final whose lognormal puts 2 points of probability more above the
//     drift's median than the drift does is none and states no median; one
//     within half a point keeps it (a pace slack of 0.05 points a year was
//     0.03 to 0.25 points of probability, under the precision finals are
//     written in).
func TestDriftCeiling(t *testing.T) {
	stated, held, over := 0, 0, 0
	for _, c := range []struct {
		sym, q, prefix  string
		spot, vol, vol5 float64
		mu              float64 // log, a year
		capped          bool
	}{
		{"TSM", "What will be the NYSE closing price of TSM on %s?", "$", 472.20, 0.4327, 0.3831, math.Log1p(0.0772), false},
		{"NVDA", "What will be the Nasdaq closing price of NVDA on %s?", "$", 237.47, 0.5888, 0.5148, math.Log1p(0.10), true},
		{"SMSN", "What will be the KRX closing price of Samsung Electronics (SMSN) on %s?", "₩", 262000, 0.3653, 0.4136, math.Log1p(0.0727), false},
		{"BTC", "What will be the closing price of BTC on %s?", "$", 83048, 0.6235, 0.5118, math.Log1p(0.10), true},
	} {
		ref := &ViewRef{Value: c.spot, Label: c.sym + " regular-market price", AsOf: "2026-10-07T20:00:00Z", Source: "Yahoo Finance (delayed)"}
		label := (math.Exp(c.mu) - 1) * 100
		for _, days := range []int{90, 182, 365, 730, 1095, 1825, 3650} {
			years := float64(days) / 365
			ladder := market.LadderEdges(c.spot, c.vol5*math.Sqrt(years))
			outcomes := []string{"< " + labelLevel(ladder[0], c.prefix)}
			for i := 1; i < len(ladder); i++ {
				outcomes = append(outcomes, labelLevel(ladder[i-1], c.prefix)+" – "+labelLevel(ladder[i], c.prefix))
			}
			outcomes = append(outcomes, "≥ "+labelLevel(ladder[len(ladder)-1], c.prefix))
			date := frameDay.AddDate(0, 0, days).Format(time.DateOnly)
			d := Draft{Shape: "ordered", Question: fmt.Sprintf(c.q, date), Outcomes: outcomes, ResolutionDate: date, Category: "markets"}
			fr := frameOf(d, frameDay)
			m := &measurement{sub: subject{Symbol: c.sym}, ref: ref, h1: true, mu: c.mu, cap: market.DriftCap{CapPct: 10, Applied: c.capped},
				stats: market.HistoryStats{From: "1999-01-22"}, lognormal: market.DriftMixture(c.spot, years, c.vol, c.mu).Buckets(ladder)}
			k := &deskKit{d: d, fr: fr, m: m, logs: map[string]*toolLog{}}
			if fr.Measure != "price" || fr.HorizonDays != days || k.reference() == nil {
				t.Fatalf("%s %d days: frame %+v", c.sym, days, fr)
			}
			driftMedian := c.spot * math.Exp(c.mu*years)
			where := fmt.Sprintf("%s %d days", c.sym, days)
			for dx := -0.06; dx <= 0.06+1e-9; dx += 0.0025 { // the final's drift, per year, around the drift
				ps := market.DriftMixture(c.spot, years, c.vol, c.mu+dx).Buckets(ladder)
				final := probsAt(outcomes, ps...)
				if dx <= 1e-9 { // as H1 prints it: 0.1 point a range, normalize rescaling the sum
					h1 := make([]OutcomeP, len(ps))
					for i, p := range ps {
						h1[i] = OutcomeP{outcomes[i], math.Round(p*1000) / 10}
					}
					got, ok := normalize(outcomes, h1, false)
					if b := k.basis(nil, got); !ok || b.Kind != "capped_drift" {
						t.Errorf("%s: the drift %+.4f a year as H1 prints it (%v) states its median, got %+v", where, dx, h1, b)
					}
				}
				b := k.basis(nil, final)
				v := deskView(d, final, k.reference(), fr.HorizonDays, b)
				read, _ := newDist(fr.Bounds, probsOf(outcomes, final))
				switch b.Kind {
				case "capped_drift":
					stated++
					pace, drift := *v.ChangePct, (math.Exp(c.mu*years)-1)*100
					if v.AnnualPct != nil {
						pace, drift = *v.AnnualPct, label
						if !strings.Contains(v.Lead, fmt.Sprintf("(about %+.1f%% a year)", min(pace, label))) || !strings.Contains(b.Label, fmt.Sprintf("drift %+.1f%%/yr", label)) {
							t.Errorf("%s, drift %+.4f: %q under %q", where, dx, v.Lead, b.Label)
						}
					}
					if v.Median.Value > driftMedian*(1+1e-12) || pace > drift+1e-9 || !strings.HasPrefix(v.Lead, "Our median is ") {
						t.Errorf("%s, drift %+.4f: median %.4f (the drift's %.4f), pace %.4f%% under a %.4f%% label: %q", where, dx, v.Median.Value, driftMedian, pace, drift, v.Lead)
					}
					// The median is the final's own reading, or the drift's when that reads higher, and
					// the chance at most the drift's plus driftMassTol (the label says which is held).
					raw := read.quantile(0.5).Value
					lnd, _ := newDist(fr.Bounds, m.lognormal)
					rawP, mostP := 1-read.cdf(c.spot), 1-lnd.cdf(c.spot)+driftMassTol
					note := heldNoteWant(d, final, k.reference(), fr.HorizonDays, b, v)
					if want := min(raw, driftMedian); math.Abs(v.Median.Value-want) > 1e-9*want || !strings.HasSuffix(b.Label, driftWords(m)) || v.Basis.Label != b.Label+note ||
						(note != "" && note != heldNoteOf(raw > driftMedian && strings.Contains(note, "median"), rawP > mostP && strings.Contains(note, "chance"))) ||
						v.Median.Value < v.P25.Value || v.Median.Value > v.P75.Value || v.PAbove.Value != min(rawP, mostP) {
						t.Errorf("%s, drift %+.4f: median %.4f, read %.4f, the drift's %.4f, 50%% %.4f–%.4f, P(≥ today) %.4f, read %.4f, most %.4f, label %q", where, dx,
							v.Median.Value, raw, driftMedian, v.P25.Value, v.P75.Value, v.PAbove.Value, rawP, mostP, v.Basis.Label)
					}
					if raw > driftMedian {
						held++
					}
				case "none":
					if b != aboveDrift || v.PAbove != nil || v.ChangePct != nil || strings.Contains(v.Lead, "median") {
						t.Errorf("%s, drift %+.4f: %+v %q", where, dx, b, v.Lead)
					}
				default:
					t.Fatalf("basis %+v", b)
				}
				if dx <= 1e-9 && b.Kind != "capped_drift" {
					t.Errorf("%s: a final at or under the drift (%+.4f a year) states its median, got %+v", where, dx, b)
				}
				// The share the final's lognormal puts above the drift's median, over half.
				switch more := 0.5*math.Erfc(-dx*math.Sqrt(years)/c.vol/math.Sqrt2) - 0.5; {
				case more >= 2*driftMassTol && b.Kind != "none":
					t.Errorf("%s: a final %+.4f a year over the drift (%.2f points more above its median) states no median, got %+v", where, dx, more*100, b)
				case more <= driftMassTol/2 && b.Kind != "capped_drift":
					t.Errorf("%s: a final %+.4f a year over the drift (%.2f points more above its median) states its median, got %+v", where, dx, more*100, b)
				case more >= 2*driftMassTol:
					over++
				}
			}
		}
	}
	if stated == 0 || held == 0 || over == 0 {
		t.Fatalf("%d views rested on the drift, %d held at its median, %d over it", stated, held, over)
	}
	t.Logf("%d views rested on the drift, %d held at its median, %d over it", stated, held, over)
}

// TestDriftHeld: the live check's seven ladders where a final that is the
// capped drift's lognormal read above the drift's exact pace and lost its
// median: it now states the drift's own median and pace, and says so.
func TestDriftHeld(t *testing.T) {
	for _, c := range []struct {
		name, q, prefix string
		spot, vol, vol5 float64
		annual          float64 // the capped drift, %/yr
		days            int
		lead, label     string
	}{
		{"TSM 1y", "What will be the NYSE closing price of TSM on %s?", "$", 472.20, 0.4327, 0.3831, 7.72, 365,
			"Our median is $509 for Oct 7, 2027, +7.7% from today's $472.20 (about +7.7% a year)", "drift +7.7%/yr"},
		{"NVDA 90d", "What will be the Nasdaq closing price of NVDA on %s?", "$", 237.47, 0.5888, 0.5148, 10, 90,
			"Our median is $243 for Jan 5, 2027, +2.4% from today's $237.47;", "drift +10.0%/yr"},
		{"NVDA 1y", "What will be the Nasdaq closing price of NVDA on %s?", "$", 237.47, 0.5888, 0.5148, 10, 365,
			"Our median is $261 for Oct 7, 2027, +10% from today's $237.47 (about +10.0% a year)", "drift +10.0%/yr"},
		{"Samsung ₩ 90d", "What will be the KRX closing price of Samsung Electronics (SMSN) on %s?", "₩", 262000, 0.3653, 0.4136, 7.27, 90,
			"Our median is ₩267,000 for Jan 5, 2027, +1.7% from today's ₩262,000;", "drift +7.3%/yr"},
		{"Samsung ₩ 1y", "What will be the KRX closing price of Samsung Electronics (SMSN) on %s?", "₩", 262000, 0.3653, 0.4136, 7.27, 365,
			"Our median is ₩281,000 for Oct 7, 2027, +7.3% from today's ₩262,000 (about +7.3% a year)", "drift +7.3%/yr"},
		{"BTC 90d", "What will be the closing price of BTC on %s?", "$", 83048, 0.6235, 0.5118, 10, 90,
			"Our median is $85,000 for Jan 5, 2027, +2.4% from today's $83,048;", "drift +10.0%/yr"},
		{"BTC 1y", "What will be the closing price of BTC on %s?", "$", 83048, 0.6235, 0.5118, 10, 365,
			"Our median is $91,400 for Oct 7, 2027, +10% from today's $83,048 (about +10.0% a year)", "drift +10.0%/yr"},
	} {
		years, mu := float64(c.days)/365, math.Log1p(c.annual/100)
		ladder := market.LadderEdges(c.spot, c.vol5*math.Sqrt(years))
		outcomes := []string{"< " + labelLevel(ladder[0], c.prefix)}
		for i := 1; i < len(ladder); i++ {
			outcomes = append(outcomes, labelLevel(ladder[i-1], c.prefix)+" – "+labelLevel(ladder[i], c.prefix))
		}
		outcomes = append(outcomes, "≥ "+labelLevel(ladder[len(ladder)-1], c.prefix))
		date := frameDay.AddDate(0, 0, c.days).Format(time.DateOnly)
		d := Draft{Shape: "ordered", Question: fmt.Sprintf(c.q, date), Outcomes: outcomes, ResolutionDate: date, Category: "markets"}
		fr := frameOf(d, frameDay)
		logn := market.DriftMixture(c.spot, years, c.vol, mu).Buckets(ladder)
		m := &measurement{sub: subject{Symbol: "X"}, ref: &ViewRef{Value: c.spot}, h1: true, mu: mu, stats: market.HistoryStats{From: "2000-01-04"}, lognormal: logn}
		k := &deskKit{d: d, fr: fr, m: m, logs: map[string]*toolLog{}}
		final := probsAt(outcomes, logn...)
		read, _ := newDist(fr.Bounds, logn)
		raw, exact := read.quantile(0.5).Value, c.spot*math.Exp(mu*years)
		// Against the drift's exact pace, the old rule's reading: over by more than 0.05 points.
		y := max(years, 1)
		if (math.Pow(raw/c.spot, 1/y)-math.Pow(exact/c.spot, 1/y))*100 <= 0.05 {
			t.Errorf("%s: read %.4f vs exact %.4f is no longer a case the old rule missed", c.name, raw, exact)
		}
		b := k.basis(nil, final)
		v := deskView(d, final, k.reference(), fr.HorizonDays, b)
		t.Logf("%s: read %+.2f%% vs the drift's %+.2f%%", c.name, (math.Pow(raw/c.spot, 1/y)-1)*100, (math.Pow(exact/c.spot, 1/y)-1)*100)
		if b.Kind != "capped_drift" || !strings.HasSuffix(v.Basis.Label, heldNote) || !strings.Contains(b.Label, c.label) || math.Abs(v.Median.Value-exact) > 1e-9*exact ||
			math.Abs(*v.ChangePct-(math.Exp(mu*years)-1)*100) > 1e-9 || !strings.HasPrefix(v.Lead, c.lead) || v.PAbove == nil {
			t.Errorf("%s: basis %+v, median %.4f (read %.4f, the drift's %.4f), lead %q", c.name, b, v.Median.Value, raw, exact, v.Lead)
		}
		if v.AnnualPct != nil && math.Abs(*v.AnnualPct-c.annual) > 1e-9 {
			t.Errorf("%s: annual %.6f, the drift %.2f", c.name, *v.AnnualPct, c.annual)
		}
	}
}

// judgmentPanel is a one-member panel, a judgment quant whose final is
// final: finish's pool is the final, so only the volatility floor can move it.
func judgmentPanel(final []OutcomeP) []Specialist {
	return []Specialist{{Role: "quant", Name: "Market & quant analyst", Method: "judgment", Probabilities: final}}
}

// heldNoteWant is the held note a capped_drift view's label should end with:
// a hold that changes a figure as the page prints it, read off the leads with
// and without the hold (the median's figure, change and pace before "; 50%
// range", the chance after the last "; ") and the hero's median figure.
func heldNoteWant(d Draft, final []OutcomeP, ref *ViewRef, days int, b ViewBasis, v *DeskView) string {
	raw := deskView(d, final, ref, days, ViewBasis{Kind: b.Kind, Label: b.Label})
	head := func(l string) string { return l[:strings.Index(l, "; 50% range")] }
	tail := func(l string) string { return l[strings.LastIndex(l, "; "):] }
	return heldNoteOf(head(raw.Lead) != head(v.Lead) || fmtQ(raw.Median, v.Unit) != fmtQ(v.Median, v.Unit), tail(raw.Lead) != tail(v.Lead))
}

// liveLadder is one of the live check's 22 cases: today's level, the
// full-history σ, the capped drift (log, a year) and the question's ladder.
type liveLadder struct {
	name, prefix  string
	spot, vol, mu float64
	days          int
	edges         []float64
}

var liveLadders = []liveLadder{
	{"TSM 90d", "$", 472.20, 0.4327, 0.074329, 90, []float64{380, 420, 472, 500, 600}},
	{"TSM 1y", "$", 472.20, 0.4327, 0.074329, 365, []float64{280, 380, 472, 600, 750, 940}},
	{"TSM 3y", "$", 472.20, 0.4327, 0.074329, 1095, []float64{240, 350, 472, 710, 940, 1400}},
	{"TSM 10y", "$", 472.20, 0.4327, 0.074329, 3650, []float64{120, 240, 472, 940, 1900, 3800}},
	{"NVDA 90d", "$", 237.47, 0.5888, 0.095310, 90, []float64{140, 190, 237, 300, 380, 470}},
	{"NVDA 1y", "$", 237.47, 0.5888, 0.095310, 365, []float64{120, 180, 237, 360, 470, 710}},
	{"NVDA 3y", "$", 237.47, 0.5888, 0.095310, 1095, []float64{120, 180, 237, 360, 470, 710}},
	{"NVDA 10y", "$", 237.47, 0.5888, 0.095310, 3650, []float64{60, 120, 237, 470, 950, 1900}},
	{"Samsung ₩ 90d", "₩", 262000, 0.3653, 0.070177, 90, []float64{210000, 240000, 262000, 290000, 330000}},
	{"Samsung ₩ 1y", "₩", 262000, 0.3653, 0.070177, 365, []float64{160000, 210000, 262000, 330000, 420000, 520000}},
	{"Samsung ₩ 3y", "₩", 262000, 0.3653, 0.070177, 1095, []float64{130000, 200000, 262000, 400000, 520000, 800000}},
	{"Samsung ₩ 10y", "₩", 262000, 0.3653, 0.070177, 3650, []float64{66000, 130000, 262000, 520000, 1000000, 2100000}},
	{"Samsung US$ 3y", "$", 194.97, 0.3704, 0.070505, 1095, []float64{100, 150, 195, 300, 400, 600}},
	{"Samsung US$ 10y", "$", 194.97, 0.3704, 0.070505, 3650, []float64{50, 100, 195, 400, 800, 1500}},
	{"DBS 90d", "S$", 73.85, 0.2434, 0.020359, 90, []float64{60, 66, 73.9, 80, 92}},
	{"DBS 1y", "S$", 73.85, 0.2434, 0.020359, 365, []float64{60, 66, 73.9, 80, 92}},
	{"DBS 3y", "S$", 73.85, 0.2434, 0.020359, 1095, []float64{44, 60, 73.9, 92, 120, 150}},
	{"DBS 10y", "S$", 73.85, 0.2434, 0.020359, 3650, []float64{37, 55, 73.9, 110, 150, 220}},
	{"BTC 90d", "$", 83048, 0.6235, 0.095310, 90, []float64{50000, 66000, 83000, 100000, 130000, 170000}},
	{"BTC 1y", "$", 83048, 0.6235, 0.095310, 365, []float64{40000, 60000, 83000, 120000, 170000, 250000}},
	{"BTC 3y", "$", 83048, 0.6235, 0.095310, 1095, []float64{40000, 60000, 83000, 120000, 170000, 250000}},
	{"BTC 10y", "$", 83048, 0.6235, 0.095310, 3650, []float64{20000, 40000, 83000, 170000, 330000, 660000}},
}

// ladderDesk is a desk on edges: a kit whose measurement is today's level,
// σ and the capped drift μ (log, a year) with H1's lognormal on the ranges,
// and the draft, ranges and reference it reads.
func ladderDesk(t *testing.T, prefix string, spot, vol, mu float64, days int, edges []float64) (*deskKit, Draft, []string, [][2]float64, *ViewRef, []float64) {
	t.Helper()
	outcomes := []string{"< " + labelLevel(edges[0], prefix)}
	for i := 1; i < len(edges); i++ {
		outcomes = append(outcomes, labelLevel(edges[i-1], prefix)+" – "+labelLevel(edges[i], prefix))
	}
	outcomes = append(outcomes, "≥ "+labelLevel(edges[len(edges)-1], prefix))
	bounds, ok := bucketBounds(outcomes)
	if !ok {
		t.Fatalf("ladder %v: %v", edges, outcomes)
	}
	d := Draft{Shape: "ordered", Outcomes: outcomes, ResolutionDate: frameDay.AddDate(0, 0, days).Format(time.DateOnly)}
	ref := &ViewRef{Value: spot}
	logn := market.DriftMixture(spot, float64(days)/365, vol, mu).Buckets(edges)
	k := &deskKit{d: d, fr: frame{Kind: "price_short", Measure: "price", HorizonDays: days, Bounds: bounds}, logs: map[string]*toolLog{},
		m: &measurement{sub: subject{Symbol: "X"}, ref: ref, h1: true, vol: vol, mu: mu, stats: market.HistoryStats{From: "2000-01-04"}, lognormal: logn}}
	return k, d, outcomes, bounds, ref, logn
}

func (c liveLadder) desk(t *testing.T) (*deskKit, Draft, []string, [][2]float64, *ViewRef, []float64) {
	return ladderDesk(t, c.prefix, c.spot, c.vol, c.mu, c.days, c.edges)
}

// TestDriftPrecision: on the live check's 22 ladders, the
// capped drift's lognormal written the way the desk's finals are written
// keeps its median: H1's own column copied verbatim (0.1 point a range),
// whole percents each rounded (normalize rescales the sum), whole percents by
// largest remainder, and the lognormal at 0.75× the measured σ. A 0.05-point
// pace slack lost it on 4, 9, 13 and 6 of them. One rendition still does:
// TSM at a year in whole percents by largest remainder (8/16/18/22/17/11/8)
// puts 1.06 points more above the drift's median than the drift does, past
// the 1-point tolerance. A final 2 points a year over the drift is none on
// 18 of them, every ladder from three years among them. Through finish the
// volatility floor leaves every rendition of the drift as written (none is
// over 2% narrower than H1's lognormal) and widens the 0.75× σ final on all
// 22 ladders, which keeps its median.
func TestDriftPrecision(t *testing.T) {
	percents := func(ps []float64, step float64) []float64 {
		out := make([]float64, len(ps))
		for i, p := range ps {
			out[i] = math.Round(p*100/step) * step
		}
		return out
	}
	largestRemainder := func(ps []float64) []float64 {
		out, order, left := make([]float64, len(ps)), make([]int, len(ps)), 100
		for i, p := range ps {
			out[i], order[i] = math.Floor(p*100), i
			left -= int(out[i])
		}
		slices.SortStableFunc(order, func(a, b int) int { return cmp.Compare(ps[b]*100-out[b], ps[a]*100-out[a]) })
		for _, i := range order[:left] {
			out[i]++
		}
		return out
	}
	logsTo(t, io.Discard)
	lost, widened := map[string][]string{}, 0
	for _, c := range liveLadders {
		years := float64(c.days) / 365
		k, d, outcomes, bounds, ref, logn := c.desk(t)
		at := c.spot * math.Exp(c.mu*years)
		ln, _ := newDist(bounds, logn)
		for _, w := range []struct {
			name string
			ps   []float64
		}{
			{"H1's column", percents(logn, 0.1)},
			{"whole percents", percents(logn, 1)},
			{"largest remainder", largestRemainder(logn)},
			{"0.75σ", market.DriftMixture(c.spot, years, 0.75*c.vol, c.mu).Buckets(c.edges)},
		} {
			got, ok := normalize(outcomes, probsAt(outcomes, w.ps...), false)
			if !ok {
				t.Fatalf("%s, %s: %v does not normalize", c.name, w.name, w.ps)
			}
			b := k.basis(nil, got)
			v := deskView(d, got, ref, c.days, b)
			fd, _ := newDist(bounds, probsOf(outcomes, got))
			more := (ln.cdf(at) - fd.cdf(at)) * 100
			// The run's path: the volatility floor widens the 0.75× σ final (to
			// H1's width, keeping its median) and leaves the drift as written.
			if res, fv := deskRun(k, judgmentPanel(got), got); fv.Basis.Kind != b.Kind || (res.Methods.Guard != nil) != (w.name == "0.75σ") {
				t.Errorf("%s, %s: through finish %s, guard %v", c.name, w.name, fv.Basis.Kind, res.Methods.Guard)
			} else if res.Methods.Guard != nil {
				widened++
			}
			if b.Kind != "capped_drift" {
				lost[w.name] = append(lost[w.name], fmt.Sprintf("%s %.2f", c.name, more))
				continue
			}
			if v.Median.Value > at*(1+1e-12) || v.PAbove == nil || v.ChangePct == nil || !strings.HasPrefix(v.Lead, "Our median is ") {
				t.Errorf("%s, %s: median %.4f under the drift's %.4f, lead %q", c.name, w.name, v.Median.Value, at, v.Lead)
			}
		}
		over := probsAt(outcomes, market.DriftMixture(c.spot, years, c.vol, math.Log(math.Exp(c.mu)+0.02)).Buckets(c.edges)...)
		if b := k.basis(nil, over); b.Kind == "none" {
			lost["2 points a year over"] = append(lost["2 points a year over"], c.name)
		} else if c.days >= 1095 {
			t.Errorf("%s: a final 2 points a year over the drift states no median, got %+v", c.name, b)
		}
	}
	t.Logf("lost their median: %v", lost)
	if l := lost["largest remainder"]; len(lost["H1's column"])+len(lost["whole percents"])+len(lost["0.75σ"]) > 0 || len(l) != 1 || !strings.HasPrefix(l[0], "TSM 1y ") {
		t.Errorf("finals at the drift, as the desk writes them, that lost their median: %v", lost)
	}
	if n := len(lost["2 points a year over"]); n != 18 {
		t.Errorf("a final 2 points a year over the drift is none on %d of 22, not 18", n)
	}
	if widened != 22 {
		t.Errorf("the floor widened %d of the 22 finals at 0.75× σ and nothing else", widened)
	}

	// The live check's example: TSM at a year with H1's column copied as the
	// final reads +7.88 against the drift's +7.82; it now states the drift's
	// median and pace, held, rather than the ranges alone.
	ladder := []float64{280, 380, 472, 600, 750, 940}
	outcomes := []string{"< $280", "$280 – $380", "$380 – $472", "$472 – $600", "$600 – $750", "$750 – $940", "≥ $940"}
	bounds, _ := bucketBounds(outcomes)
	d := Draft{Shape: "ordered", Outcomes: outcomes, ResolutionDate: frameDay.AddDate(0, 0, 365).Format(time.DateOnly)}
	ref := &ViewRef{Value: 472.20}
	k := &deskKit{d: d, fr: frame{Kind: "price_short", Measure: "price", HorizonDays: 365, Bounds: bounds}, logs: map[string]*toolLog{},
		m: &measurement{sub: subject{Symbol: "TSM"}, ref: ref, h1: true, mu: 0.074329, stats: market.HistoryStats{From: "1997-10-09"},
			lognormal: market.DriftMixture(472.20, 1, 0.4327, 0.074329).Buckets(ladder)}}
	final, _ := normalize(outcomes, probsAt(outcomes, 8.4, 16.6, 18.1, 21.7, 16.7, 10.7, 7.8), false)
	b := k.basis(nil, final)
	v := deskView(d, final, ref, 365, b)
	if b.Kind != "capped_drift" || !strings.HasSuffix(v.Basis.Label, heldNote) || v.PAbove == nil ||
		!strings.HasPrefix(v.Lead, "Our median is $509 for Oct 7, 2027, +7.7% from today's $472.20 (about +7.7% a year)") {
		t.Errorf("TSM 1y, H1's column: %+v %q", v.Basis, v.Lead)
	}
}

// TestDriftChance: the mass rule reads a final at one
// point, the drift's own median, so a final with the drift's centre and a
// narrower spread than H1's passes it with more of its probability between
// today and that median. Its view then stated a far higher chance of a gain
// than the drift in its label gives: NVDA at ten years at half H1's σ, "85%
// chance it closes at or above $237.47" under "drift +10.0%/yr", whose own
// lognormal gives 69.6%. On the re-check's cases (no guard fired on them:
// independent agreement, or no windows to set a width floor) the chance is
// now held as the median is: at most the drift lognormal's own P(≥ today),
// read off the same ranges, plus driftMassTol, in the view, its lead, its
// label and the list row's brief. The quantiles stay the final's own, so
// the hold alone contradicted them (P9: "50% range $316–$1,200 … 71%
// chance"); it is the backstop now. A run's final reaches the view widened
// by the volatility floor, and states its own chance (70.05% for NVDA at ten
// years at half H1's σ: widened to H1's σ at the final's own median, a hair
// under the drift's; 70.07% under the earlier floor, toward the drift's own lognormal)
// beside a 50% range that starts below today.
func TestDriftChance(t *testing.T) {
	ladders := map[string]liveLadder{}
	for _, c := range liveLadders {
		ladders[c.name] = c
	}
	logsTo(t, io.Discard)
	for _, c := range []struct {
		name    string
		scale   float64 // the final's σ, a share of H1's, at the drift's centre
		lead    string  // the chance the lead stated before: the final's own reading
		drift   float64 // the drift's lognormal P(≥ today), %
		widened float64 // the run's view now: the chance of the final the floor widened, %
	}{
		{"NVDA 10y", 0.5, "85%", 69.6, 70.05},
		{"NVDA 10y", 0.6, "80%", 69.6, 70.01},
		{"NVDA 10y", 0.75, "75%", 69.6, 69.96},
		{"NVDA 3y", 0.5, "71%", 61.0, 61.26},
		{"BTC 10y", 0.6, "79%", 68.6, 69.31},
		{"TSM 10y", 0.5, "86%", 70.6, 71.27},
	} {
		l := ladders[c.name]
		where := fmt.Sprintf("%s at %.2f× σ", c.name, c.scale)
		years := float64(l.days) / 365
		k, d, outcomes, bounds, ref, logn := l.desk(t)
		final := probsAt(outcomes, market.DriftMixture(l.spot, years, c.scale*l.vol, l.mu).Buckets(l.edges)...)
		fd, _ := newDist(bounds, probsOf(outcomes, final))
		ln, _ := newDist(bounds, logn)
		read, drift := 1-fd.cdf(l.spot), 1-ln.cdf(l.spot)
		if got := fmt.Sprintf("%.0f%%", read*100); got != c.lead || !within(drift*100, c.drift, 0.1) {
			t.Fatalf("%s: the final reads %s and the drift %.2f%%, not the re-check's %s and %.1f%%", where, got, drift*100, c.lead, c.drift)
		}
		b := k.basis(nil, final)
		v := deskView(d, final, ref, l.days, b)
		if b.Kind != "capped_drift" || v.PAbove == nil {
			t.Fatalf("%s: %+v %q", where, b, v.Lead)
		}
		at := l.spot * math.Exp(l.mu*years)
		held := min(read, drift+driftMassTol)
		if v.PAbove.Value > drift+driftMassTol+1e-12 || v.PAbove.Value != held || v.PAbove.Approx != fd.inTail(l.spot) {
			t.Errorf("%s: states P(≥ today) %.4f, the final reads %.4f, the drift %.4f + %.2f", where, v.PAbove.Value, read, drift, driftMassTol)
		}
		chance := fmt.Sprintf("; %.0f%% chance it closes at or above %s.", held*100, todayLevel(l.spot, v.Unit))
		if note := heldNoteWant(d, final, ref, l.days, b, v); !strings.HasSuffix(v.Lead, chance) || strings.Contains(v.Lead, " "+c.lead+" chance") ||
			b.Label != "Measured X history since 2000; "+driftWords(k.m) || v.Basis.Label != b.Label+note || note != heldNoteOf(fd.quantile(0.5).Value > at && strings.Contains(note, "median"), true) {
			t.Errorf("%s: lead %q, label %q", where, v.Lead, v.Basis.Label)
		}
		if br := v.brief(); br.PAbove == nil || *br.PAbove != *v.PAbove || br.Basis != "capped_drift" {
			t.Errorf("%s: the list row's brief %+v", where, br)
		}
		for _, q := range []struct {
			got  Q
			want float64
		}{{v.P10, .10}, {v.P25, .25}, {v.P75, .75}, {v.P90, .90}} {
			if q.got != fd.quantile(q.want) {
				t.Errorf("%s: the %.0fth percentile %+v is not the final's %+v", where, q.want*100, q.got, fd.quantile(q.want))
			}
		}
		t.Logf("%s: the final reads %.1f%%, the drift %.1f%%; the view states %.1f%%: %q", where, read*100, drift*100, v.PAbove.Value*100, chance)
		// A run no longer views the final as written: the floor widens it
		// first, and its view states the widened final's own chance, with a
		// 50% range from below today.
		res, fv := deskRun(k, judgmentPanel(final), final)
		wd, _ := newDist(bounds, probsOf(outcomes, res.Probabilities))
		if fv.PAbove == nil || fv.PAbove.Value != 1-wd.cdf(l.spot) || !within(fv.PAbove.Value*100, c.widened, 0.005) || fv.P25.Value >= l.spot || *res.Methods.Guard != "volatility" {
			t.Errorf("%s through finish: %+v %q", where, fv.PAbove, fv.Lead)
		}
	}
	// A lognormal that does not read on the ranges holds the chance at its exact share above today.
	l := ladders["NVDA 10y"]
	k, d, outcomes, _, ref, logn := l.desk(t)
	k.m.lognormal = logn[1:]
	final := probsAt(outcomes, market.DriftMixture(l.spot, 10, 0.5*l.vol, l.mu).Buckets(l.edges)...)
	exact := 0.5 * math.Erfc(-l.mu*math.Sqrt(10)/l.vol/math.Sqrt2)
	if v := deskView(d, final, ref, l.days, k.basis(nil, final)); v.PAbove == nil || !within(v.PAbove.Value, exact+driftMassTol, 1e-12) || !within(exact, 0.6956, 1e-4) {
		t.Errorf("an unread lognormal: %+v, its exact share %.4f", v.PAbove, exact)
	}
}

// TestDriftChanceBound: over random ladders and finals, a view on the capped
// drift never states a P(≥ today) above the drift lognormal's own, read off
// the same ranges, plus driftMassTol. It states the final's own reading
// when that is lower, and its label says when the chance is held; a view on
// cited scenarios states the final's own reading whatever it is, and a view
// on nothing states none. These are views of finals as written, the
// backstop under the volatility floor (TestVolFloorBound has the run's
// path, where the hold never binds).
func TestDriftChanceBound(t *testing.T) {
	r := rand.New(rand.NewPCG(8, 4))
	kept, held, cited, none, skipped := 0, 0, 0, 0, 0
	for i := 0; i < 3000; i++ {
		spot := 10 * math.Pow(10, 5*r.Float64())
		vol := 0.15 + 0.75*r.Float64()
		mu := math.Log1p(-0.02 + 0.12*r.Float64()) // −2 to +10%/yr
		days := 30 + r.IntN(3621)
		years := float64(days) / 365
		ladder := market.LadderEdges(spot, vol*(0.8+0.4*r.Float64())*math.Sqrt(years))
		k, d, outcomes, bounds, ref, logn := ladderDesk(t, "$", spot, vol, mu, days, ladder)
		// The final: a lognormal at or near the drift, narrower or wider than
		// H1's, as is, in whole percents or perturbed.
		ps := market.DriftMixture(spot, years, vol*(0.3+1.3*r.Float64()), mu+(r.Float64()*0.05-0.04)).Buckets(ladder)
		switch r.IntN(3) {
		case 1:
			for j := range ps {
				ps[j] = math.Round(ps[j] * 100)
			}
		case 2:
			sum := 0.0
			for j := range ps {
				ps[j] *= math.Exp(0.3 * r.NormFloat64())
				sum += ps[j]
			}
			for j := range ps {
				ps[j] /= sum
			}
		}
		final, ok := normalize(outcomes, probsAt(outcomes, ps...), false)
		if !ok {
			skipped++
			continue
		}
		var panel []Specialist
		if r.IntN(5) == 0 { // a cited valuation input: the scenarios basis, unchanged
			q := newToolLog()
			q.scen = &scenarioRecord{levels: []market.ScenarioLevel{{Kind: "base", Level: spot * 2, Inputs: []market.Input{{Field: "eps", Value: 1, Basis: "F1", Kind: "source", Verified: true}}}}}
			k.logs["quant"], panel = q, []Specialist{{Role: "quant"}}
		}
		b := k.basis(panel, final)
		v := deskView(d, final, ref, days, b)
		fd, _ := newDist(bounds, probsOf(outcomes, final))
		ln, _ := newDist(bounds, logn)
		read, most := 1-fd.cdf(spot), 1-ln.cdf(spot)+driftMassTol
		where := fmt.Sprintf("#%d: spot %.6g, σ %.3f, μ %+.4f, %d days, %v", i, spot, vol, mu, days, final)
		switch b.Kind {
		case "capped_drift":
			kept++
			if v.PAbove == nil || v.PAbove.Value > most+1e-12 || v.PAbove.Value != min(read, most) {
				t.Fatalf("%s: states %+v, the final reads %.4f, the drift's most %.4f", where, v.PAbove, read, most)
			}
			medianHeld := fd.quantile(0.5).Value > spot*math.Exp(mu*years)
			if read > most {
				held++
			}
			note := heldNoteWant(d, final, ref, days, b, v)
			if !strings.HasSuffix(b.Label, driftWords(k.m)) || v.Basis.Label != b.Label+note ||
				(note != "" && note != heldNoteOf(medianHeld && strings.Contains(note, "median"), read > most && strings.Contains(note, "chance"))) ||
				!strings.Contains(v.Lead, fmt.Sprintf("; %s%.0f%% chance it closes at or above ", map[bool]string{true: "≈"}[v.PAbove.Approx], v.PAbove.Value*100)) {
				t.Fatalf("%s: label %q, lead %q", where, v.Basis.Label, v.Lead)
			}
			if br := v.brief(); br.PAbove == nil || *br.PAbove != *v.PAbove {
				t.Fatalf("%s: brief %+v", where, br)
			}
		case "scenarios":
			cited++
			if v.PAbove == nil || v.PAbove.Value != read {
				t.Fatalf("%s: on cited scenarios states %+v, the final reads %.4f", where, v.PAbove, read)
			}
		case "none":
			none++
			if b != aboveDrift || v.PAbove != nil || v.brief().PAbove != nil {
				t.Fatalf("%s: %+v states %+v", where, b, v.PAbove)
			}
		}
	}
	if kept == 0 || held == 0 || cited == 0 || none == 0 {
		t.Fatalf("%d views on the drift, %d of them with the chance held, %d on cited scenarios, %d on nothing", kept, held, cited, none)
	}
	t.Logf("%d views on the drift, %d of them with the chance held; %d on cited scenarios, %d on nothing; %d finals normalize refused", kept, held, cited, none, skipped)
}

// "Independent methods agree" only across inputs: the history
// and regime anchors both rest on H1, so a TSM 10y panel whose only agreeing
// anchors are those two is diverse and the guard runs; a fundamentals member
// that agrees makes it independent. A volatility member whose σ came from
// history_stats is in H1's family too.
func TestAgreementAcrossInputs(t *testing.T) {
	hist := seatOf("base_rate", "history", true, flatTSM...)
	regime := seatOf("skeptic", "regime", true, nearTSM...)
	judge := seatOf("quant", "judgment", false, .30, .25, .20, .12, .08, .05)
	if tv(hist.Probabilities, regime.Probabilities) > agreeTV {
		t.Fatal("fixture: the anchors agree")
	}
	ag := agree([]Specialist{hist, judge, regime})
	if ag.class != "diverse" {
		t.Fatalf("history + regime: %+v", ag)
	}
	sharp := probsAt(tsmOutcomes, .02, .03, .05, .10, .30, .50)
	if _, guard := guardFinal(sharp, probsAt(tsmOutcomes, flatTSM...), ag.class, func(p []OutcomeP) float64 { return logWidth(tsmDraft(), p) }, 0); guard == "" {
		t.Fatal("the guard runs on a diverse panel")
	}
	fund := seatOf("quant", "fundamentals", true, .17, .18, .17, .17, .15, .16)
	if ag := agree([]Specialist{hist, fund, regime}); ag.class != "independent_agree" {
		t.Fatalf("history + fundamentals + regime: %+v", ag)
	}
	// The recorded family wins over the method's: a history_stats volatility member is H1's.
	l := newToolLog()
	l.ok["history_stats"] = 1
	vol := seatOf("quant", "volatility", true, .17, .18, .17, .17, .15, .16)
	vol.family = l.family("volatility")
	if ag := agree([]Specialist{hist, vol, judge}); vol.family != "history" || ag.class != "diverse" {
		t.Fatalf("history_stats volatility: %q %+v", vol.family, ag)
	}
	l.ok["price_outlook"] = 1
	vol.family = l.family("volatility")
	if ag := agree([]Specialist{hist, vol, judge}); vol.family != "volatility" || ag.class != "independent_agree" {
		t.Fatalf("price_outlook volatility: %q %+v", vol.family, ag)
	}
}

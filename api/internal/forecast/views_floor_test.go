package forecast

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/market"
)

// barsFromToday is the scored bars' share from the ladder's edge nearest
// today's level up (LadderEdges puts one there: $237 for NVDA's $237.47),
// what the page's bars under the hero add up to beside its chance tile.
func barsFromToday(bounds [][2]float64, ps []float64, spot float64) (edge, share float64) {
	best := math.Inf(1)
	for _, b := range bounds[1:] {
		if d := math.Abs(math.Log(b[0] / spot)); d < best {
			best, edge = d, b[0]
		}
	}
	for i, b := range bounds {
		if i > 0 && b[0] >= edge {
			share += ps[i]
		}
	}
	return edge, share
}

// contradicts names the view's quantiles that disagree with the chance it
// states, on one distribution: a 50% range that starts above today beside a
// chance under 75% (the 10th percentile: 90%), or the mirror below.
func contradicts(v *DeskView, spot float64) string {
	p := v.PAbove.Value
	switch {
	case v.P25.Value > spot && p < 0.75:
		return fmt.Sprintf("50%% range from %.4g above today %.4g beside %.2f%%", v.P25.Value, spot, p*100)
	case v.P10.Value > spot && p < 0.90:
		return fmt.Sprintf("80%% range from %.4g above today %.4g beside %.2f%%", v.P10.Value, spot, p*100)
	case v.P75.Value < spot && p > 0.25:
		return fmt.Sprintf("50%% range up to %.4g below today %.4g beside %.2f%%", v.P75.Value, spot, p*100)
	case v.P90.Value < spot && p > 0.10:
		return fmt.Sprintf("80%% range up to %.4g below today %.4g beside %.2f%%", v.P90.Value, spot, p*100)
	}
	return ""
}

// logsTo sends slog's records to w until the test ends (finish logs each
// view); to io.Discard, none is formatted.
func logsTo(t *testing.T, w io.Writer) {
	previous := slog.Default()
	opts := &slog.HandlerOptions{}
	if w == io.Discard {
		opts.Level = slog.LevelError + 1
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(w, opts)))
	t.Cleanup(func() { slog.SetDefault(previous) })
}

// deskRun is a numeric run's last steps on a final, as run.go takes them:
// finish (the weights, the pool, the guard, then the volatility floor) and
// the view of the stored final.
func deskRun(k *deskKit, panel []Specialist, final []OutcomeP) (Result, *DeskView) {
	res := Result{Specialists: panel, Probabilities: final, Confidence: "medium"}
	k.finish(&res, aggExtras{}, agree(panel))
	return res, deskView(k.d, res.Probabilities, k.reference(), k.fr.HorizonDays, k.basis(panel, res.Probabilities))
}

// floorRef is the volatility floor's reference for a final on the capped
// drift (volFloor): H1's lognormal at the full-history σ moved to the median
// the final's view states (its own, at most the drift's own, today's level ×
// e^{μT}), read on the question's ranges (bounds), and that median.
func floorRef(k *deskKit, bounds [][2]float64, final []OutcomeP) ([]float64, float64) {
	fd, _ := newDist(bounds, probsOf(k.d.Outcomes, final))
	median := min(fd.quantile(0.5).Value, k.m.spot()*math.Exp(k.m.mu*float64(k.fr.HorizonDays)/365))
	return k.m.lognormalAt(bounds, k.fr.HorizonDays, median, func(ps []float64) float64 {
		ds, _ := newDist(bounds, ps)
		return ds.cdf(median)
	}), median
}

// widthOn is logWidth on ranges already read (bounds): ln p90 − ln p10.
func widthOn(bounds [][2]float64, outcomes []string, probs []OutcomeP) float64 {
	ds, _ := newDist(bounds, probsOf(outcomes, probs))
	return math.Log(ds.quantile(0.9).Value / ds.quantile(0.1).Value)
}

// TestVolFloor: holding only the median and the chance
// left a narrow final on the capped drift contradicting itself. On the
// re-check's sweep (the 22 live ladders, lognormal finals at 0.5 to 1.25× H1's
// σ and −1 to +0.5 points a year around the drift) the earlier view of the
// final as written held the chance on 272 of its 579 views on the drift, 74
// of them at the drift's centre; 49 stated a 50% (or 80%) range that started
// above today beside a chance under 75% (90%), 15 at the centre ("50% range
// $316–$1,200 … 71% chance" for NVDA at ten years at half H1's σ), and on
// 159 the bars from today's edge up added to over 2 points more than the
// chance tile, up to 15. Now finish widens such a final to the measured
// volatility before the view reads it, on a panel that agrees independently
// (the guard is off) and on a diverse one (the guard first): every view on
// the drift states the stored final's own chance, its ranges agree with it,
// and the bars from today's edge are within a point of it. The final is
// widened around its own median: to H1's σ at the median its
// view states, which it keeps.
func TestVolFloor(t *testing.T) {
	logsTo(t, io.Discard)
	type tally struct{ views, held, held0, contra, contra0, gap2, floored, guarded int }
	var before tally
	after := map[string]*tally{"agree": {}, "diverse": {}}
	worstGap := 0.0
	for _, c := range liveLadders {
		years := float64(c.days) / 365
		for _, sf := range []float64{0.5, 0.6, 0.75, 0.9, 1, 1.25} {
			for _, dp := range []float64{-1, -0.5, 0, 0.25, 0.5} {
				k, d, outcomes, bounds, ref, logn := c.desk(t)
				k.fr.Plan = "long_stock"
				final := probsAt(outcomes, market.DriftMixture(c.spot, years, sf*c.vol, math.Log(math.Exp(c.mu)+dp/100)).Buckets(c.edges)...)
				where := fmt.Sprintf("%s at %.2f× σ, %+.2f points a year", c.name, sf, dp)
				// The view of the final as the aggregator wrote it.
				b0 := k.basis(nil, final)
				if v0 := deskView(d, final, ref, c.days, b0); b0.Kind == "capped_drift" {
					before.views++
					fd, _ := newDist(bounds, probsOf(outcomes, final))
					if 1-fd.cdf(c.spot) > v0.PAbove.Value {
						before.held++
						before.held0 += map[bool]int{true: 1}[dp == 0]
						if contradicts(v0, c.spot) != "" || v0.Median.Value > c.spot && v0.PAbove.Value < 0.5 {
							before.contra++
							before.contra0 += map[bool]int{true: 1}[dp == 0]
						}
						if _, bars := barsFromToday(bounds, probsOf(outcomes, final), c.spot); bars-v0.PAbove.Value > 0.02 {
							before.gap2++
						}
					}
				}
				ln := probsAt(outcomes, logn...)
				for kind, panel := range map[string][]Specialist{
					// Two anchored methods on different inputs within 0.10 of each other: the guard is off.
					// (History and regime both rest on H1, so they no longer agree independently.)
					"agree": {{Role: "base_rate", Name: "Base-rate statistician", Method: "history", Anchor: ln, Probabilities: ln},
						{Role: "quant", Name: "Market & quant analyst", Method: "fundamentals", Anchor: ln, Probabilities: ln},
						{Role: "skeptic", Name: "Narrative & macro skeptic", Method: "judgment", Probabilities: final}},
					// A judgment member and a wider, lower regime view: the guard may blend first.
					"diverse": {{Role: "quant", Name: "Market & quant analyst", Method: "judgment", Probabilities: final},
						{Role: "skeptic", Name: "Narrative & macro skeptic", Method: "regime", Anchor: probsAt(outcomes, market.DriftMixture(c.spot, years, 1.3*c.vol, c.mu-0.02).Buckets(c.edges)...),
							Probabilities: probsAt(outcomes, market.DriftMixture(c.spot, years, 1.3*c.vol, c.mu-0.02).Buckets(c.edges)...)}},
				} {
					// The final the floor sees: the guard's, when it blends back to the pool.
					pl, _ := pool(panel, weightsFor(k.fr), agree(panel).groups)
					guarded, _ := guardFinal(final, pl, agree(panel).class, func(p []OutcomeP) float64 { return logWidth(d, p) }, k.refWidth())
					res, v := deskRun(k, panel, final)
					a := after[kind]
					if b, bg := k.basis(panel, res.Probabilities), k.basis(panel, guarded); b.Kind != bg.Kind || kind == "agree" && bg.Kind != b0.Kind {
						t.Errorf("%s (%s): the floor moved the basis %s → %s (as written: %s)", where, kind, bg.Kind, b.Kind, b0.Kind)
					}
					if g := res.Methods.Guard; g != nil && *g == "volatility" {
						a.floored++
						fl, _ := floorRef(k, bounds, guarded)
						lw := widthOn(bounds, outcomes, probsAt(outcomes, fl...))
						if w := widthOn(bounds, outcomes, res.Probabilities); w < (1-volWidthTol)*lw-1e-9 || !strings.HasSuffix(res.Methods.Note, volSentence) && !strings.HasSuffix(res.Methods.Note, volGainSentence) {
							t.Errorf("%s (%s): widened to %.4f of the width of H1's σ at its median, note %q", where, kind, w/lw, res.Methods.Note)
						}
						if vg := deskView(d, guarded, ref, c.days, k.basis(panel, guarded)); vg.Basis.Kind == "capped_drift" && math.Abs(v.Median.Value/vg.Median.Value-1) > 1e-9 {
							t.Errorf("%s (%s): the floor moved the median %.6g → %.6g", where, kind, vg.Median.Value, v.Median.Value)
						}
					} else if g != nil {
						a.guarded++
					}
					if v.Basis.Kind != "capped_drift" {
						continue
					}
					a.views++
					fd, _ := newDist(bounds, probsOf(outcomes, res.Probabilities))
					read := 1 - fd.cdf(c.spot)
					_, bars := barsFromToday(bounds, probsOf(outcomes, res.Probabilities), c.spot)
					worstGap = max(worstGap, math.Abs(bars-v.PAbove.Value))
					if v.PAbove.Value != read {
						a.held++
						t.Errorf("%s (%s): states %.4f, the stored final reads %.4f: %q", where, kind, v.PAbove.Value, read, v.Lead)
					}
					if why := contradicts(v, c.spot); why != "" {
						a.contra++
						t.Errorf("%s (%s): %s: %q", where, kind, why, v.Lead)
					}
					if math.Abs(bars-v.PAbove.Value) > 0.01 {
						a.gap2++
						t.Errorf("%s (%s): the bars from today's edge add to %.2f%% beside a chance of %.2f%%", where, kind, bars*100, v.PAbove.Value*100)
					}
					if strings.Contains(v.Basis.Label, "the chance") {
						t.Errorf("%s (%s): label %q", where, kind, v.Basis.Label)
					}
					for _, q := range []struct {
						got  Q
						want float64
					}{{v.P10, .10}, {v.P25, .25}, {v.P75, .75}, {v.P90, .90}} {
						if q.got != fd.quantile(q.want) {
							t.Errorf("%s (%s): the %.0fth percentile is not the stored final's", where, kind, q.want*100)
						}
					}
				}
			}
		}
	}
	if before != (tally{views: 579, held: 272, held0: 74, contra: 49, contra0: 15, gap2: 159}) {
		t.Errorf("the earlier view of these finals: %+v, not the re-check's 579 views, 272 held (74 at the centre), 49 contradicting (15), 159 bars over by 2 points", before)
	}
	for kind, a := range after {
		if a.views < 579 || kind == "agree" && (a.views != 579 || a.guarded != 0) || a.floored == 0 {
			t.Errorf("%s: %+v", kind, a)
		}
		t.Logf("%s panel: %d views on the drift, %d widened (%d blended back to the pool only), %d with the chance held, %d contradicting, %d with bars a point off", kind, a.views, a.floored, a.guarded, a.held, a.contra, a.gap2)
	}
	t.Logf("before: %+v; the bars from today's edge now within %.2f points of the chance", before, worstGap*100)

	// The two leads the re-check quoted, now read off the widened final.
	for _, c := range []struct{ name, before, after string }{
		{"NVDA 10y", "50% range $316–$1,200, 80% range $173–≈$1,990; 71% chance", "50% range $177–≈$2,080, 80% range ≈$58.9–≈$7,110; 70% chance it closes at or above $237.47."},
		{"Samsung ₩ 10y", "50% range ₩337,000–₩819,000, 80% range ₩241,000–₩1.23M; 74% chance", "50% range ₩245,000–₩1.16M, 80% range ₩121,000–≈₩2.21M; 73% chance it closes at or above ₩262,000."},
	} {
		for _, l := range liveLadders {
			if l.name != c.name {
				continue
			}
			k, d, outcomes, _, ref, _ := l.desk(t)
			final := probsAt(outcomes, market.DriftMixture(l.spot, 10, 0.5*l.vol, l.mu).Buckets(l.edges)...)
			if v0 := deskView(d, final, ref, l.days, k.basis(nil, final)); !strings.Contains(v0.Lead, c.before) {
				t.Errorf("%s at half H1's σ, as written: %q", c.name, v0.Lead)
			}
			res, v := deskRun(k, []Specialist{{Role: "quant", Name: "Market & quant analyst", Method: "judgment", Probabilities: final}}, final)
			if !strings.HasSuffix(v.Lead, c.after) || *res.Methods.Guard != "volatility" || res.Methods.Note != volSentence {
				t.Errorf("%s at half H1's σ, widened: %q, %+v", c.name, v.Lead, res.Methods)
			}
		}
	}
}

// TestVolFloorBound: over random ladders and finals, a final whose view
// rests on the capped drift comes out of finish no narrower than H1's
// lognormal (within volWidthTol) and no surer of a gain than the drift
// allows, so its view states the stored final's own chance: no 50% or 80%
// range on one side of today beside a chance the range rules out, the
// median inside the 50% range, the bars from today's level within a point
// of the chance. A final on cited scenarios or over the drift is left as
// written. The finals: lognormals at 0.3 to 1.6× H1's σ and −4 to +1 points
// a year around the drift (as is, in whole percents, or perturbed), and
// random shapes; a fifth of the draws with a cited valuation input.
func TestVolFloorBound(t *testing.T) {
	logsTo(t, io.Discard)
	r := rand.New(rand.NewPCG(9, 5))
	var kept, floored, gain, narrowed, cited, none, heldMedianQuiet, edgeOff int
	for i := 0; i < 2000; i++ {
		spot := 10 * math.Pow(10, 5*r.Float64())
		vol := 0.15 + 0.75*r.Float64()
		mu := math.Log1p(-0.02 + 0.12*r.Float64())
		days := 30 + r.IntN(3621)
		years := float64(days) / 365
		ladder := market.LadderEdges(spot, vol*(0.8+0.4*r.Float64())*math.Sqrt(years))
		k, d, outcomes, bounds, ref, logn := ladderDesk(t, "$", spot, vol, mu, days, ladder)
		var ps []float64
		switch shape := r.IntN(4); shape {
		case 3: // any shape
			ps = make([]float64, len(ladder)+1)
			for j := range ps {
				ps[j] = 0.01 + r.ExpFloat64()
			}
		default:
			ps = market.DriftMixture(spot, years, vol*(0.3+1.3*r.Float64()), mu+(r.Float64()*0.05-0.04)).Buckets(ladder)
			for j := range ps {
				switch shape {
				case 1:
					ps[j] = math.Round(ps[j] * 100)
				case 2:
					ps[j] *= math.Exp(0.3 * r.NormFloat64())
				}
			}
		}
		final, ok := normalize(outcomes, probsAt(outcomes, ps...), false)
		if !ok {
			final = rescale(probsAt(outcomes, ps...))
		}
		panel := []Specialist{{Role: "quant", Name: "Market & quant analyst", Method: "judgment", Probabilities: final}}
		if r.IntN(5) == 0 {
			q := newToolLog()
			q.scen = &scenarioRecord{levels: []market.ScenarioLevel{{Kind: "base", Level: spot * 2, Inputs: []market.Input{{Field: "eps", Value: 1, Basis: "F1", Kind: "source", Verified: true}}}}}
			k.logs["quant"], panel[0].Method = q, "fundamentals"
		}
		b0 := k.basis(panel, final)
		res, v := deskRun(k, panel, final)
		where := fmt.Sprintf("#%d: spot %.6g, σ %.3f, μ %+.4f, %d days, %v", i, spot, vol, mu, days, final)
		widened := res.Methods.Guard != nil && *res.Methods.Guard == "volatility"
		if v.Basis.Kind != b0.Kind {
			t.Fatalf("%s: the floor moved the basis %s → %s", where, b0.Kind, v.Basis.Kind)
		}
		switch v.Basis.Kind {
		case "capped_drift":
			kept++
			fd, _ := newDist(bounds, probsOf(outcomes, res.Probabilities))
			fl, median := floorRef(k, bounds, final)
			lw, w := widthOn(bounds, outcomes, probsAt(outcomes, fl...)), widthOn(bounds, outcomes, res.Probabilities)
			read, at := 1-fd.cdf(spot), spot*math.Exp(mu*years)
			if w < (1-volWidthTol)*lw-1e-9 || read > v.Basis.chance+1e-12 || v.PAbove.Value != read {
				t.Fatalf("%s: width %.4f of H1's σ at its median, reads %.4f (the most %.4f), states %.4f", where, w/lw, read, v.Basis.chance, v.PAbove.Value)
			}
			_ = logn
			if why := contradicts(v, spot); why != "" || v.Median.Value < v.P25.Value || v.Median.Value > v.P75.Value || v.Median.Value > at*(1+1e-12) {
				t.Fatalf("%s: %s; median %.6g, 50%% %.6g–%.6g, the drift's %.6g: %q", where, why, v.Median.Value, v.P25.Value, v.P75.Value, at, v.Lead)
			}
			if edge, bars := barsFromToday(bounds, probsOf(outcomes, res.Probabilities), spot); math.Abs(bars+fd.cdf(edge)-fd.cdf(spot)-v.PAbove.Value) > 0.01 {
				t.Fatalf("%s: bars %.4f from %.6g, chance %.4f", where, bars, edge, v.PAbove.Value)
			} else if math.Abs(bars-v.PAbove.Value) > 0.01 {
				edgeOff++ // the ladder's edge is not today's level: the range between them holds the rest
			}
			if fd.quantile(0.5).Value > v.Median.Value && !strings.Contains(v.Basis.Label, "median") {
				heldMedianQuiet++
			}
			if widened {
				floored++
				if !strings.HasSuffix(res.Methods.Note, volSentence) {
					gain++ // the chance-only note
				}
				if w < widthOn(bounds, outcomes, final) {
					narrowed++ // only a final surer of a gain than the drift, and never under the floor
				}
				v0 := deskView(d, final, ref, days, b0)
				f0, _ := newDist(bounds, probsOf(outcomes, final))
				rd, _ := newDist(bounds, fl)
				own, toward := 1-f0.cdf(spot), 1-rd.cdf(spot)
				switch m0, m1, trough := humps(bounds, probsOf(outcomes, final), probsOf(outcomes, res.Probabilities), v.Median.Value); {
				case math.Abs(v.Median.Value/v0.Median.Value-1) > 1e-9 || fmtQ(v.Median, v.Unit) != fmtQ(v0.Median, v0.Unit):
					t.Fatalf("%s: the floor moved the median %.6g → %.6g (the reference's %.6g): %q", where, v0.Median.Value, v.Median.Value, median, v.Lead)
				case !f0.inTail(spot) && (read < min(own, toward)-1e-9 || read > max(own, toward)+1e-9):
					t.Fatalf("%s: the chance %.4f is not between the final's %.4f and the reference's %.4f", where, read, own, toward)
				case m0 <= 1 && m1 >= 2 && trough:
					t.Fatalf("%s: widened into two humps with the median in the trough: %v", where, res.Probabilities)
				}
				if tv(res.Probabilities, probsAt(outcomes, fl...)) >= tv(final, probsAt(outcomes, fl...)) {
					t.Fatalf("%s: the floor did not move toward H1's σ at its median", where)
				}
			} else if res.Methods.Guard == nil && tv(res.Probabilities, final) != 0 {
				t.Fatalf("%s: moved without a guard", where)
			}
		case "scenarios":
			cited++
			if widened || tv(res.Probabilities, final) != 0 {
				t.Fatalf("%s: a final on cited scenarios moved", where)
			}
		case "none":
			none++
			if widened || v.PAbove != nil || v.brief().PAbove != nil || tv(res.Probabilities, final) != 0 {
				t.Fatalf("%s: a final over the drift moved or states a chance: %+v", where, v.PAbove)
			}
		}
	}
	if kept == 0 || floored == 0 || gain == 0 || cited == 0 || none == 0 {
		t.Fatalf("%d on the drift, %d widened (%d only for the chance), %d on cited scenarios, %d on nothing", kept, floored, gain, cited, none)
	}
	t.Logf("%d views on the drift, %d widened (%d only surer of a gain than the drift, %d of those narrower than written); %d on cited scenarios, %d on nothing; %d median holds too small to print; %d ladders with today's edge a point of probability away from today",
		kept, floored, gain, narrowed, cited, none, heldMedianQuiet, edgeOff)
}

// TestVolFloorCentre: the floor widens a final around
// its own median and never moves its centre toward the drift. An earlier floor mixed
// a narrow final toward the drift's own lognormal, so it moved a cautious
// final's median and chance toward the drift's: (a) it widened finals already
// at H1's σ that sat under the drift, whose open tails read narrower on the
// same ladder than the drift-centred lognormal's (NVDA at ten years at +5.0%
// a year and 60% became +7.0% and 64%, Samsung ₩ at a year at −2.8% and 47%
// became +1.4% and 51%, under a note that said "widened"); (b) it mostly
// replaced a narrow cautious final with the drift's view, the more so the
// narrower it was (TSM at a year at −12.3% a year: +5.1% and 54% at half H1's
// σ, −8.6% and 42% at H1's σ). Now:
//   - a lognormal at H1's σ or wider, centred 1 to 10 points a year under the
//     drift, is never widened (132 finals on the 22 live ladders at each of 1,
//     1.1 and 1.25× σ; the earlier floor widened 18 at 1×);
//   - over spreads of 0.3 to 1.5× σ (by 0.2) at 3, 5, 7.5 and 10 points a
//     year under the drift, and (by 0.05) 20 under for TSM at a year (−12.3%
//     a year) and NVDA at ten (−10%), the view states the desk's own median
//     as it prints it; its
//     chance lies between the desk's own and that of H1's σ at the same
//     median, so it moves toward 50% only as far as the widening takes it and
//     never past it; and the chance's distance from 50% does not grow with the
//     spread on 87 of the 90 grids, TSM at a year and NVDA at ten among them.
//     On three DBS grids it grows by at most 0.7 point, where the desk's own
//     median, as §4.8 reads it, moves with its spread and the view states it
//     (at ten years 7.5 points under: S$42.8 at 0.3× σ, S$42.3 from 0.6×);
//     with the desk's median fixed as it reads, by at most 0.21 point (the
//     least weight on binned ranges);
//   - no widened final has two humps with its median in the trough.
func TestVolFloorCentre(t *testing.T) {
	logsTo(t, io.Discard)
	labels := map[string][]string{}
	for _, c := range liveLadders {
		_, _, labels[c.name], _, _, _ = c.desk(t)
	}
	at := func(c liveLadder, sf, dp float64) []OutcomeP {
		return probsAt(labels[c.name], market.DriftMixture(c.spot, float64(c.days)/365, sf*c.vol, math.Log(math.Exp(c.mu)+dp/100)).Buckets(c.edges)...)
	}
	// (a) At H1's σ or wider: the view as written, unwidened.
	widened := 0
	for _, c := range liveLadders {
		for _, sf := range []float64{1, 1.1, 1.25} {
			for _, dp := range []float64{-1, -2, -3, -5, -7.5, -10} {
				k, d, _, _, ref, _ := c.desk(t)
				final := at(c, sf, dp)
				b := k.basis(nil, final)
				res, v := deskRun(k, judgmentPanel(final), final)
				if v0 := deskView(d, final, ref, c.days, b); b.Kind != "capped_drift" || res.Methods.Guard != nil || v.Lead != v0.Lead {
					widened++
					t.Errorf("%s at %.2f× σ, %+.1f points a year: %s, guard %v: %q → %q", c.name, sf, dp, b.Kind, res.Methods.Guard != nil, v0.Lead, v.Lead)
				}
			}
		}
	}
	for _, c := range []struct {
		name   string
		dp     float64
		states string
	}{
		{"NVDA 10y", -5, "Our median is $386 for Oct 4, 2036, +63% from today's $237.47 (about +5.0% a year); 50% range $109–$1,390, 80% range ≈$37.3–≈$4,130; 60% chance it closes at or above $237.47."},
		{"Samsung ₩ 1y", -10, "Our median is ₩255,000 for Oct 7, 2027, -2.8% from today's ₩262,000 (about -2.8% a year); 50% range ₩196,000–₩327,000, 80% range ≈₩160,000–₩411,000; 47% chance it closes at or above ₩262,000."},
	} {
		for _, l := range liveLadders {
			if l.name == c.name {
				final := at(l, 1, c.dp)
				k, _, _, _, _, _ := l.desk(t)
				if res, v := deskRun(k, judgmentPanel(final), final); res.Methods.Guard != nil || v.Lead != c.states {
					t.Errorf("%s at H1's σ, %+.1f points a year: %q, %v", c.name, c.dp, v.Lead, res.Methods.Note)
				}
			}
		}
	}

	// (b) The spread grids.
	type grid struct {
		c   liveLadder
		dp  float64
		sfs []float64
	}
	var grids []grid
	coarse, fine := []float64{}, []float64{}
	for s := 30; s <= 150; s += 20 {
		coarse = append(coarse, float64(s)/100)
	}
	for s := 30; s <= 150; s += 5 {
		fine = append(fine, float64(s)/100)
	}
	for _, c := range liveLadders {
		for _, dp := range []float64{-3, -5, -7.5, -10} {
			grids = append(grids, grid{c, dp, coarse})
		}
		if c.name == "TSM 1y" || c.name == "NVDA 10y" {
			grids = append(grids, grid{c, -20, fine})
		}
	}
	var finals, floored, bumpy, dips int
	worst, worstAt := 0.0, ""
	for _, g := range grids {
		k, d, outcomes, bounds, ref, _ := g.c.desk(t)
		closest, dipped := math.Inf(1), false // the smallest distance from 50% at a narrower spread
		for _, sf := range g.sfs {
			final := at(g.c, sf, g.dp)
			b := k.basis(nil, final)
			if b.Kind != "capped_drift" {
				continue // NVDA at 90 days 3 points under, at its narrowest spreads
			}
			finals++
			where := fmt.Sprintf("%s at %.2f× σ, %+.1f points a year", g.c.name, sf, g.dp)
			v0 := deskView(d, final, ref, g.c.days, b)
			res, v := deskRun(k, judgmentPanel(final), final)
			if fmtQ(v.Median, v.Unit) != fmtQ(v0.Median, v0.Unit) || leadCentre(v.Lead) != leadCentre(v0.Lead) || math.Abs(v.Median.Value/v0.Median.Value-1) > 1e-9 {
				t.Errorf("%s: the median moved: %q → %q", where, v0.Lead, v.Lead)
			}
			if res.Methods.Guard != nil {
				floored++
				fl, _ := floorRef(k, bounds, final)
				rd, _ := newDist(bounds, fl)
				own, toward := v0.PAbove.Value, 1-rd.cdf(g.c.spot)
				if p := v.PAbove.Value; p < min(own, toward)-1e-9 || p > max(own, toward)+1e-9 || (p-0.5)*(own-0.5) < 0 {
					t.Errorf("%s: the chance %.4f is not between the desk's %.4f and H1's σ at its median %.4f", where, p, own, toward)
				}
				if m0, m1, trough := humps(bounds, probsOf(outcomes, final), probsOf(outcomes, res.Probabilities), v.Median.Value); m0 <= 1 && m1 >= 2 && trough {
					bumpy++
					t.Errorf("%s: two humps with the median in the trough: %q", where, v.Lead)
				}
			}
			dist := math.Abs(v.PAbove.Value - 0.5)
			if dist-closest > worst {
				worst, worstAt = dist-closest, where
			}
			if g.dp == -20 && dist > closest+1e-9 {
				t.Errorf("%s: the chance moves away from 50%% as the spread grows: %q", where, v.Lead)
			}
			dipped = dipped || dist > closest+1e-9
			closest = min(closest, dist)
		}
		dips += map[bool]int{true: 1}[dipped]
	}
	if worst > 0.01 || widened > 0 || floored < 300 {
		t.Errorf("the chance's distance from 50%% grows with the spread by up to %.4f points (%s); %d widened at H1's σ; %d of %d widened", worst*100, worstAt, widened, floored, finals)
	}
	t.Logf("%d grids, %d grid finals on the drift, %d widened; on %d grids the chance's distance from 50%% grows with the spread, by at most %.4f points (%s); %d with two humps", len(grids), finals, floored, dips, worst*100, worstAt, bumpy)

	// The grids at 7.5, 10 and 20 points under with the desk's median fixed as
	// it reads (each final H1's lognormal at σ × 0.3 to 1.5 centred to read the
	// 1× final's median).
	worst, worstAt, dips = 0, "", 0
	for _, g := range grids {
		if g.dp > -7.5 {
			continue
		}
		k, d, outcomes, bounds, ref, _ := g.c.desk(t)
		md, _ := newDist(bounds, probsOf(outcomes, at(g.c, 1, g.dp)))
		median := md.quantile(0.5).Value
		closest, dipped := math.Inf(1), false
		for _, sf := range coarse {
			mm := *k.m
			mm.vol = sf * g.c.vol
			final := probsAt(outcomes, mm.lognormalAt(bounds, g.c.days, median, func(ps []float64) float64 { ds, _ := newDist(bounds, ps); return ds.cdf(median) })...)
			if k.basis(nil, final).Kind != "capped_drift" {
				continue
			}
			_, v := deskRun(k, judgmentPanel(final), final)
			if v0 := deskView(d, final, ref, g.c.days, k.basis(nil, final)); fmtQ(v.Median, v.Unit) != fmtQ(v0.Median, v0.Unit) {
				t.Errorf("%s at %.2f× σ at a fixed median: %q → %q", g.c.name, sf, v0.Lead, v.Lead)
			}
			dist := math.Abs(v.PAbove.Value - 0.5)
			if dist-closest > worst {
				worst, worstAt = dist-closest, fmt.Sprintf("%s at %.2f× σ, %+.1f points a year", g.c.name, sf, g.dp)
			}
			dipped = dipped || dist > closest+1e-9
			closest = min(closest, dist)
		}
		dips += map[bool]int{true: 1}[dipped]
	}
	if worst > 0.003 {
		t.Errorf("at a fixed median the chance's distance from 50%% grows with the spread by up to %.4f points (%s)", worst*100, worstAt)
	}
	t.Logf("at a fixed median: on %d grids, by at most %.4f points (%s)", dips, worst*100, worstAt)

	// The re-check's two tables, as the page now states them.
	for _, c := range []struct {
		name   string
		sf     float64
		states string // the lead's centre and its chance
	}{
		{"TSM 1y", 0.5, "(about -12.1% a year); 50% range $308–$556, 80% range ≈$247–$723; 38% chance"},
		{"TSM 1y", 1, "(about -12.2% a year); 50% range $305–$559, 80% range ≈$243–$728; 38% chance"},
		{"TSM 1y", 1.5, "(about -12.3% a year); 50% range ≈$268–$645, 80% range ≈$173–≈$949; 42% chance"},
		{"NVDA 10y", 0.3, "(about -10.0% a year); 50% range ≈$29.2–$269, 80% range ≈$8.18–$856; 27% chance"},
		{"NVDA 10y", 0.5, "(about -10.0% a year); 50% range ≈$28.7–$273, 80% range ≈$8.06–$843; 27% chance"},
		{"NVDA 10y", 0.75, "(about -10.0% a year); 50% range ≈$28.5–$278, 80% range ≈$8.01–$837; 28% chance"},
	} {
		for _, l := range liveLadders {
			if l.name == c.name {
				k, _, _, _, _, _ := l.desk(t)
				final := at(l, c.sf, -20)
				if _, v := deskRun(k, judgmentPanel(final), final); !strings.Contains(v.Lead, c.states) {
					t.Errorf("%s at %.2f× σ, 20 points a year under the drift: %q", c.name, c.sf, v.Lead)
				}
			}
		}
	}
}

// leadCentre is a lead's median, change and pace (before "; 50% range").
func leadCentre(lead string) string {
	if i := strings.Index(lead, "; 50% range"); i >= 0 {
		return lead[:i]
	}
	return lead
}

// TestLognormalAt: the floor's reference is H1's lognormal at the full-history
// σ, its probabilities on the question's ranges a pure shift of the drift's
// (one centre, the same σ), and §4.8's reading of it puts half its
// probability below the median it was moved to, in a closed range or an open
// one. A mix of a final with it keeps the final's median as the view reads it.
func TestLognormalAt(t *testing.T) {
	r := rand.New(rand.NewPCG(6, 10))
	tails := 0
	for i := 0; i < 400; i++ {
		spot, vol, days := 10*math.Pow(10, 5*r.Float64()), 0.15+0.75*r.Float64(), 30+r.IntN(3621)
		years := float64(days) / 365
		ladder := market.LadderEdges(spot, vol*(0.8+0.4*r.Float64())*math.Sqrt(years))
		k, _, outcomes, bounds, _, _ := ladderDesk(t, "$", spot, vol, 0.05, days, ladder)
		// A median a final can read: a lognormal's at a random centre and spread.
		md, _ := newDist(bounds, market.DriftMixture(spot, years, vol*(0.3+1.3*r.Float64()), 1.5*vol*r.NormFloat64()/math.Sqrt(years)).Buckets(ladder))
		median := md.quantile(0.5).Value
		tails += map[bool]int{true: 1}[md.quantile(0.5).Approx]
		ps := k.m.lognormalAt(bounds, days, median, func(ps []float64) float64 { ds, _ := newDist(bounds, ps); return ds.cdf(median) })
		where := fmt.Sprintf("#%d: spot %.6g, σ %.3f, %d days, median %.6g", i, spot, vol, days, median)
		if ps == nil {
			t.Fatalf("%s: no centre", where)
		}
		ds, _ := newDist(bounds, ps)
		// The centre, from the first range's share: P(< e0) = Φ((ln(e0/spot) − c)/s).
		s := vol * math.Sqrt(years)
		c := math.Log(ladder[0]/spot) - s*math.Sqrt2*math.Erfinv(2*ps[0]-1)
		want := market.DriftMixture(spot, years, vol, c/years).Buckets(ladder)
		for j := range ps {
			if math.Abs(ps[j]-want[j]) > 1e-9 {
				t.Fatalf("%s: range %d holds %.12f, a lognormal at σ centred at %+.4f %.12f", where, j, ps[j], c, want[j])
			}
		}
		if got := ds.cdf(median); math.Abs(got-0.5) > 1e-12 {
			t.Fatalf("%s: reads %.15f below the median", where, got)
		}
		// A narrower final at the same median, mixed with it, keeps that median.
		k.m.vol = vol * 0.5
		fin := k.m.lognormalAt(bounds, days, median, func(g []float64) float64 { d, _ := newDist(bounds, g); return d.cdf(median) })
		k.m.vol = vol
		if fin == nil {
			continue
		}
		mixed := make([]float64, len(fin))
		for j := range fin {
			mixed[j] = 0.3*fin[j] + 0.7*ps[j]
		}
		if md, _ := newDist(bounds, mixed); !ds.inTail(median) && math.Abs(md.quantile(0.5).Value/median-1) > 1e-9 {
			t.Fatalf("%s: the mix reads its median at %.9g", where, md.quantile(0.5).Value)
		}
		_ = outcomes
	}
	if tails == 0 {
		t.Error("no median in an open range")
	}
	t.Logf("400 references, %d of them for a median in an open range", tails)
}

// TestFloorInFinish: finish runs the floor last, on every panel. A final
// the guard blends back to the pool and the floor then widens says both;
// one only surer of a gain than the drift says that; the logged record
// keeps both, and a final as wide as H1's lognormal at the drift's chance
// is untouched.
func TestFloorInFinish(t *testing.T) {
	var l liveLadder
	for _, c := range liveLadders {
		if c.name == "NVDA 10y" {
			l = c
		}
	}
	k, _, outcomes, bounds, _, logn := l.desk(t)
	k.fr.Plan = "long_stock"
	narrow := probsAt(outcomes, market.DriftMixture(l.spot, 10, 0.5*l.vol, l.mu).Buckets(l.edges)...)
	// A diverse panel whose pool is far flatter: the guard blends first.
	flatter := probsAt(outcomes, .14, .14, .14, .14, .14, .14, .16)
	panel := []Specialist{{Role: "quant", Name: "Market & quant analyst", Method: "judgment", Probabilities: narrow},
		{Role: "skeptic", Name: "Narrative & macro skeptic", Method: "regime", Anchor: flatter, Probabilities: flatter}}
	var logs bytes.Buffer
	logsTo(t, &logs)
	res := Result{Specialists: panel, Probabilities: narrow}
	k.finish(&res, aggExtras{MethodNote: "We trusted the history."}, agree(panel))
	if g := res.Methods.Guard; g == nil || *g != "volatility" || res.Methods.Note != "We trusted the history. "+guardSentence+" "+volSentence ||
		!strings.Contains(logs.String(), " widthFinal=2.44") || !strings.Contains(logs.String(), " guard=volatility floorWeight=0.96") {
		t.Errorf("guard then floor: %v %q, logged %q", g, res.Methods.Note, logs.String())
	}
	// H1's lognormal with 5 points moved from just under today ($120–$237) to
	// just over it ($237–$470): as wide (both ends lie outside those ranges)
	// and as much above the drift's median ($616), but surer of a gain, so
	// only the chance moves it.
	lumpy := probsOf(outcomes, probsAt(outcomes, logn...))
	lumpy[2], lumpy[3] = lumpy[2]-0.05, lumpy[3]+0.05
	gain := probsAt(outcomes, lumpy...)
	res = Result{Specialists: panel[:1], Probabilities: gain}
	k.finish(&res, aggExtras{}, agree(panel[:1]))
	fd, _ := newDist(bounds, probsOf(outcomes, res.Probabilities))
	b := k.basis(nil, gain)
	if g := res.Methods.Guard; g == nil || *g != "volatility" || res.Methods.Note != volGainSentence || !within(1-fd.cdf(l.spot), b.chance, 1e-6) {
		t.Errorf("only surer of a gain: %v %q, reads %.4f, the most %.4f", g, res.Methods.Note, 1-fd.cdf(l.spot), b.chance)
	}
	// H1's lognormal itself: nothing to do.
	ln := probsAt(outcomes, logn...)
	res = Result{Specialists: panel[:1], Probabilities: ln}
	k.finish(&res, aggExtras{}, agree(panel[:1]))
	if res.Methods.Guard != nil || tv(res.Probabilities, ln) != 0 {
		t.Errorf("the lognormal itself: %v", res.Methods.Guard)
	}
	// Without a measurement, or on cited scenarios, the floor is off.
	k.m = nil
	res = Result{Specialists: panel[:1], Probabilities: narrow}
	k.finish(&res, aggExtras{}, agree(panel[:1]))
	if res.Methods.Guard != nil {
		t.Errorf("no measurement: %v", *res.Methods.Guard)
	}
}

// TestHeldNotePrinted (the live check's low item): a hold the page does not
// print says nothing. §6.1's live judgment panel read 65.85% and stated
// 65.82%, both "66%", under a label that said the chance shown was under
// where the ranges put it; a median held by less than its last printed
// figure is the same.
func TestHeldNotePrinted(t *testing.T) {
	d := tsmDraft()
	final := probsAt(tsmOutcomes, .17, .17, .15, .16, .15, .20)
	ds, _ := newDist(edgesBounds(t, d), probsOf(tsmOutcomes, final))
	read, median := 1-ds.cdf(tsmRef.Value), ds.quantile(0.5).Value
	base := ViewBasis{Kind: "capped_drift", Label: "Measured TSM history since 1997; drift +9.4%/yr"}
	for _, c := range []struct {
		name            string
		ceiling, chance float64
		note            string
	}{
		{"nothing held", 0, 0, ""},
		{"the chance by 0.03 points", 0, read - 0.0003, ""},
		{"the chance by 2 points", 0, read - 0.02, heldChanceNote},
		{"the median by 0.01%", median * 0.9999, 0, ""},
		{"the median by 3%", median * 0.97, 0, heldNote},
		{"both, printed", median * 0.97, read - 0.02, heldBothNote},
		{"both, only the median printed", median * 0.97, read - 0.0003, heldNote},
	} {
		b := base
		b.ceiling, b.chance = c.ceiling, c.chance
		v := deskView(d, final, tsmRef, 1825, b)
		if v.Basis.Label != base.Label+c.note {
			t.Errorf("%s: label %q, lead %q", c.name, v.Basis.Label, v.Lead)
		}
	}
	if v := deskView(d, final, tsmRef, 1825, ViewBasis{Kind: "capped_drift", Label: base.Label, chance: read - 0.0003}); !within(v.PAbove.Value, read-0.0003, 1e-12) || !strings.HasSuffix(v.Lead, "; 62% chance it closes at or above $485.") {
		t.Errorf("a quiet hold still states the held figure: %+v %q", v.PAbove, v.Lead)
	}
}

func edgesBounds(t *testing.T, d Draft) [][2]float64 {
	t.Helper()
	b, ok := bucketBounds(d.Outcomes)
	if !ok {
		t.Fatalf("%v", d.Outcomes)
	}
	return b
}

// humps is the humps of a final's density per log width over the closed
// ranges as written (m0) and widened (m1), and whether the widened median's
// range lies in a trough (a denser range on each side).
func humps(bounds [][2]float64, before, after []float64, median float64) (m0, m1 int, trough bool) {
	count := func(p []float64) (int, []float64) {
		var d []float64
		for i := 1; i < len(bounds)-1; i++ {
			d = append(d, p[i]/math.Log(bounds[i][1]/bounds[i][0]))
		}
		n := 0
		for i := range d {
			if (i == 0 || d[i] > d[i-1]) && (i == len(d)-1 || d[i] > d[i+1]) {
				n++
			}
		}
		return n, d
	}
	m0, _ = count(before)
	m1, d := count(after)
	for i := 1; i < len(bounds)-1; i++ {
		if median < bounds[i][0] || median >= bounds[i][1] {
			continue
		}
		left, right := slices.Max(append([]float64{0}, d[:i-1]...)), slices.Max(append([]float64{0}, d[i:]...))
		trough = left > d[i-1] && right > d[i-1]
	}
	return m0, m1, trough
}

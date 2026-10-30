package forecast

// Panel views: in a numeric frame the server reads each member's
// method from its tool record, says whether the members agree independently,
// differ, or duplicate one view, pools them with prior (or the desk's)
// weights, and keeps the final distribution from being sharper than the
// methods support. Duplicates count once; independent agreement may be
// sharper; the pool guard can only flatten. A final that rests on the
// capped drift alone is held to the measured volatility on every panel
// (volFloor): it is mixed toward H1's lognormal moved to its own median, so
// it widens around its own centre (a lumpy final surer of a gain than the
// drift allows is narrowed toward that width, never under the floor).

import (
	"cmp"
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/market"
)

const (
	dupTV        = 0.06             // members this close without their own method's data are one view
	agreeTV      = 0.10             // anchored members with different methods this close agree
	retryMinLeft = 75 * time.Second // the corrective retry needs this much of the panel's time left
	sharpSlack   = 0.08             // the final may be this much less entropic than the pool…
	guardTV      = 0.10             // …or this close to it in TV before the guard looks
	widthFloor   = 0.75             // the final 80% range may not be narrower than this share of the methods'
	medianGap    = 0.15             // a stated median further than this (log) from the implied one is noted
)

// Methods is how the desk combined a numeric frame's members (contract Methods).
type Methods struct {
	Plan        string         `json:"plan"` // long_stock | long_crypto | short | long_level
	Weights     []MethodWeight `json:"weights"`
	WeightsFrom string         `json:"weightsFrom"` // desk | prior
	Pool        []OutcomeP     `json:"pool"`
	Agreement   string         `json:"agreement"` // independent_agree | diverse | duplicate
	Guard       *string        `json:"guard"`     // sharpness | width (blended back to the pool) | volatility (widened to H1's σ, the last word); null when the final stood
	Note        string         `json:"note"`      // the desk's methodNote (≤ 300) and each server check's sentence
}

// MethodWeight is one member's effective weight in the pool (a duplicate
// group's weight is split among its members, so they sum to 1).
type MethodWeight struct {
	Role   string  `json:"role"`
	Method string  `json:"method"`
	Weight float64 `json:"weight"`
	Why    *string `json:"why"`
}

const guardSentence = "Server check: the final distribution was sharper than the methods support, so it was blended halfway back to the pool."

// tv is the total variation distance between two distributions over the same outcomes.
func tv(p, q []OutcomeP) float64 {
	s := 0.0
	for i := range p {
		s += math.Abs(p[i].P - q[i].P)
	}
	return s / 2
}

// agreement is the panel's agreement class and its duplicate groups.
type agreement struct {
	class  string  // independent_agree | diverse | duplicate
	groups [][]int // duplicate groups (panel indices, each of 2+), in role order
	gap    float64 // the largest gap between two members on one range, in points
}

var dupMethods = []string{"judgment", "fallback", "volatility"}

// agree classifies panel. Two members duplicate each other when they are
// within dupTV and either share a method that brings no data of its own
// (judgment, fallback, volatility) or both lack an anchor; with fewer than two
// anchored members, a panel all within dupTV is one view. Members with
// anchors from different methods that are all within agreeTV agree
// independently, even within dupTV of each other, but only across inputs:
// their anchors must rest on at least two input families
// (inputFamily). The history and regime anchors both rest on H1, so their
// agreement alone is diverse and the guard runs. Otherwise diverse.
func agree(panel []Specialist) agreement {
	n := len(panel)
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	find := func(i int) int {
		for parent[i] != i {
			i = parent[i]
		}
		return i
	}
	ag := agreement{class: "diverse"}
	anchored, all := 0, true
	for i := range panel {
		if panel[i].Anchor != nil {
			anchored++
		}
		for j := i + 1; j < n; j++ {
			d := tv(panel[i].Probabilities, panel[j].Probabilities)
			all = all && d <= dupTV
			for k := range panel[i].Probabilities {
				ag.gap = max(ag.gap, math.Abs(panel[i].Probabilities[k].P-panel[j].Probabilities[k].P)*100)
			}
			same := panel[i].Method == panel[j].Method && slices.Contains(dupMethods, panel[i].Method)
			if d <= dupTV && (same || panel[i].Anchor == nil && panel[j].Anchor == nil) {
				parent[find(j)] = find(i)
			}
		}
	}
	if anchored < 2 && all && n >= 2 {
		for i := range parent {
			parent[i] = 0
		}
	}
	byRoot := map[int][]int{}
	for i := range panel {
		byRoot[find(i)] = append(byRoot[find(i)], i)
	}
	for i := range panel {
		if g := byRoot[i]; len(g) >= 2 {
			ag.groups = append(ag.groups, g)
		}
	}
	if len(ag.groups) > 0 {
		ag.class = "duplicate"
		return ag
	}
	methods, families, close := map[string]bool{}, map[string]bool{}, true
	for i := range panel {
		if panel[i].Anchor == nil {
			continue
		}
		methods[panel[i].Method], families[inputFamily(panel[i])] = true, true
		for j := i + 1; j < n; j++ {
			if panel[j].Anchor != nil && tv(panel[i].Probabilities, panel[j].Probabilities) > agreeTV {
				close = false
			}
		}
	}
	if anchored >= 2 && len(methods) >= 2 && len(families) >= 2 && close {
		ag.class = "independent_agree"
	}
	return ag
}

// inputFamily is the input a member's anchor rests on: its recorded family,
// else its method's ("history" and "regime" rest on H1's measured history;
// fundamentals and volatility on their own tools).
func inputFamily(s Specialist) string {
	if s.family != "" {
		return s.family
	}
	if s.Method == "regime" {
		return "history"
	}
	return s.Method
}

// duplicateFlag is the server's duplicate flag for a group, naming its
// members and their largest gap, and saying only of the members that ran no
// method of their own (judgment, fallback, no anchor) that they lacked its
// data: a group can hold an anchored member (one anchor, all within dupTV) or
// two of one kind of model (two volatility members). On a level frame
// (CPI, a rate), which gives the members no tools by design, it says they
// worked from the same research with no model of their own.
func duplicateFlag(panel []Specialist, g []int, level bool) Flag {
	var names, bare []string
	gap := 0.0
	for k := range panel[g[0]].Probabilities {
		lo, hi := 1.0, 0.0
		for _, i := range g {
			lo, hi = min(lo, panel[i].Probabilities[k].P), max(hi, panel[i].Probabilities[k].P)
		}
		gap = max(gap, hi-lo)
	}
	for _, i := range g {
		names = append(names, panel[i].Name)
		if s := panel[i]; s.Anchor == nil || s.Method == "judgment" || s.Method == "fallback" {
			bare = append(bare, s.Name)
		}
	}
	how := "without their own method's tool data"
	switch {
	case level:
		how = "from the same research, with no model of their own"
	case len(bare) == 0:
		how = "from the same kind of model"
	case len(bare) < len(names):
		how = "(" + andList(bare) + " without " + map[bool]string{true: "its", false: "their"}[len(bare) == 1] + " own method's tool data)"
	}
	return Flag{"duplicate", fmt.Sprintf("%s gave near-identical numbers %s, so they count as one view (largest gap %s).",
		andList(names), how, plural(int(math.Round(gap*100)), "point"))}
}

func andList(xs []string) string {
	if len(xs) <= 1 {
		return strings.Join(xs, "")
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

// rawWeight is one of the aggregator's weights.
type rawWeight struct {
	Role   string  `json:"role"`
	Weight float64 `json:"weight"`
	Why    string  `json:"why"`
}

// panelWeights is the desk's weights when each panel role has one in
// [0.10, 0.70] (renormalized), else the plan's priors (from "prior").
func panelWeights(raw []rawWeight, panel []Specialist, fr frame) (w map[string]float64, why map[string]string, from string) {
	w, why = map[string]float64{}, map[string]string{}
	ok := len(raw) > 0
	for _, s := range panel {
		i := slices.IndexFunc(raw, func(r rawWeight) bool { return strings.EqualFold(strings.TrimSpace(r.Role), s.Role) })
		if i < 0 || !(raw[i].Weight >= 0.10 && raw[i].Weight <= 0.70) {
			ok = false
			break
		}
		w[s.Role] = raw[i].Weight
		if r := truncate(strings.TrimSpace(raw[i].Why), 200); r != "" {
			why[s.Role] = r
		}
	}
	if ok {
		return w, why, "desk"
	}
	prior := weightsFor(fr)
	w, why = map[string]float64{}, map[string]string{}
	for _, s := range panel {
		w[s.Role] = prior[s.Role]
	}
	return w, why, "prior"
}

// pool is the weighted linear pool of the panel, a duplicate group counted
// once at the largest of its members' weights (its distribution their mean);
// a failed member is simply absent. Linear, never logarithmic: a linear pool
// of calibrated forecasts stays calibrated in expectation, a log pool
// sharpens without evidence. eff is each member's effective weight.
func pool(panel []Specialist, w map[string]float64, groups [][]int) (out []OutcomeP, eff []float64) {
	group := make([]int, len(panel))
	for i := range group {
		group[i] = -1
	}
	for g, members := range groups {
		for _, i := range members {
			group[i] = g
		}
	}
	type part struct {
		weight  float64
		members []int
	}
	var parts []part
	seen := map[int]int{}
	for i, s := range panel {
		if g := group[i]; g >= 0 {
			if k, ok := seen[g]; ok {
				parts[k].weight = max(parts[k].weight, w[s.Role])
				parts[k].members = append(parts[k].members, i)
				continue
			}
			seen[g] = len(parts)
		}
		parts = append(parts, part{w[s.Role], []int{i}})
	}
	total := 0.0
	for _, p := range parts {
		total += p.weight
	}
	eff = make([]float64, len(panel))
	out = make([]OutcomeP, len(panel[0].Probabilities))
	for k := range out {
		out[k].Outcome = panel[0].Probabilities[k].Outcome
	}
	for _, p := range parts {
		for _, i := range p.members {
			e := p.weight / float64(len(p.members)) / total
			if !(total > 0) {
				e = 1 / float64(len(panel))
			}
			eff[i] = e
			for k := range out {
				out[k].P += e * panel[i].Probabilities[k].P
			}
		}
	}
	return rescale(out), eff
}

// guardFinal blends final halfway back to pool once, when the panel does not
// agree independently and final is sharper than its methods support: much
// less entropic than the pool and far from it, or an 80% range narrower than
// widthFloor × the narrower of the measured windows' and the scenario
// model's (width, a log width; refWidth 0 = not measured). It can only
// flatten: a width blend that would not widen the final (the pool is as
// narrow, or the final is the pool) is no guard, so the page never says the
// server moved a number it did not.
func guardFinal(final, pl []OutcomeP, class string, width func([]OutcomeP) float64, refWidth float64) ([]OutcomeP, string) {
	if class == "independent_agree" || len(final) != len(pl) {
		return final, ""
	}
	out := make([]OutcomeP, len(final))
	for i := range final {
		out[i] = OutcomeP{final[i].Outcome, (final[i].P + pl[i].P) / 2}
	}
	out = rescale(out)
	pf, pp := probsOfOrder(final), probsOfOrder(pl)
	switch fw := width(final); {
	case entropy(pf) < entropy(pp)-sharpSlack && tv(final, pl) > guardTV: // the blend is more entropic: entropy is concave
		return out, "sharpness"
	case refWidth > 0 && fw > 0 && fw < widthFloor*refWidth && width(out) > fw:
		return out, "width"
	}
	return final, ""
}

func probsOfOrder(ps []OutcomeP) []float64 {
	out := make([]float64, len(ps))
	for i, p := range ps {
		out[i] = p.P
	}
	return out
}

// logWidth is a distribution's 80% range as ln p90 − ln p10 (0 in linear scale).
func logWidth(d Draft, probs []OutcomeP) float64 {
	bounds, ok := bucketBounds(d.Outcomes)
	if !ok {
		return 0
	}
	ds, ok := newDist(bounds, probsOf(d.Outcomes, probs))
	if !ok || !ds.log {
		return 0
	}
	return math.Log(ds.quantile(0.9).Value / ds.quantile(0.1).Value)
}

// retryPick is the member a duplicate panel re-runs: the first judgment
// member of a duplicate group, in role order (never a fallback one: its tool
// failed and would fail again). -1 for none.
func retryPick(panel []Specialist, ag agreement) int {
	if ag.class != "duplicate" {
		return -1
	}
	best := -1
	for _, g := range ag.groups {
		for _, i := range g {
			if panel[i].Method == "judgment" && (best < 0 || roleIndex(panel[i].Role) < roleIndex(panel[best].Role)) {
				best = i
			}
		}
	}
	return best
}

// retryDue reports whether a run may spend its one corrective retry: in a
// long-horizon price frame, with at least retryMinLeft of the panel's time left.
func retryDue(fr frame, left time.Duration) bool {
	return fr.Kind == "price_long" && left >= retryMinLeft
}

func roleIndex(id string) int {
	return slices.IndexFunc(roles, func(r role) bool { return r.ID == id })
}

// panelViews is the aggregator's Methods block: each member's method and
// tool data, its prior weight, the server's read of its anchor and of its
// forecast; the agreement; the prior-weighted pool; and the measured windows
// applied to today's price.
func panelViews(d Draft, fr frame, panel []Specialist, logs map[string]*toolLog, m *measurement, ag agreement, pl []OutcomeP) string {
	u := unitOf(d.Outcomes)
	ref := m.spot()
	if fr.Measure != "price" {
		ref = 0
	}
	prior := weightsFor(fr)
	var b strings.Builder
	b.WriteString("\nMethods (computed by the server, in the question's unit):\n")
	var methods []string
	for _, s := range panel {
		methods = append(methods, cmp.Or(s.Method, "judgment"))
		fmt.Fprintf(&b, "- %s · %s (%s) · prior weight %.2f", s.Name, cmp.Or(s.Method, "judgment"), methodDetail(s, logs[s.Role], m, u), prior[s.Role])
		if s.Anchor != nil {
			b.WriteString(" · anchor " + readView(d, s.Anchor, ref, u, false))
		}
		b.WriteString(" · its forecast: " + readView(d, s.Probabilities, ref, u, true) + ".")
		if s.Stats != nil && s.Stats.StatedMedian != nil && s.Stats.Median.Value > 0 && *s.Stats.StatedMedian > 0 &&
			math.Abs(math.Log(*s.Stats.StatedMedian/s.Stats.Median.Value)) > medianGap {
			fmt.Fprintf(&b, " Stated median %s; its probabilities imply %s.", fmtLevel(*s.Stats.StatedMedian, u), fmtLevel(s.Stats.Median.Value, u))
		}
		b.WriteString("\n")
	}
	switch ag.class {
	case "duplicate":
		var groups []string
		for _, g := range ag.groups {
			var names []string
			for _, i := range g {
				names = append(names, panel[i].Name)
			}
			groups = append(groups, andList(names))
		}
		fmt.Fprintf(&b, "Agreement: duplicate (%s near-identical: count them as one view). Methods: %s.\n", strings.Join(groups, "; "), strings.Join(methods, ", "))
	case "independent_agree":
		fmt.Fprintf(&b, "Agreement: independent_agree (members with different methods and data within %.0f points of each other; largest gap on one range: %.0f points).\n", agreeTV*100, ag.gap)
	default:
		fmt.Fprintf(&b, "Agreement: diverse (largest gap between two members on one range: %.0f points).\n", ag.gap)
	}
	var parts []string
	for _, o := range pl {
		parts = append(parts, fmt.Sprintf("%q %s%%", o.Outcome, strconv.FormatFloat(round(o.P*100, 1), 'f', -1, 64)))
	}
	fmt.Fprintf(&b, "Prior-weighted pool: %s (%s).\n", strings.Join(parts, "; "), readView(d, pl, ref, u, false))
	if m != nil && m.h1 && m.stats.Window != nil && ref > 0 {
		w, scale, how := m.stats.Window, 1.0, ""
		if m.scale > 0 && m.scale < 1 { // as on H1's ranges: the windows' spread at the capped drift
			scale, how = m.scale, ", each scaled to the capped drift"
		}
		fmt.Fprintf(&b, "Measured-history reference (H1 windows of %s days applied to today's price%s): 80%% range %s–%s.\n",
			groupDigits(strconv.Itoa(w.Days)), how, fmtLevel(ref*w.Q[1]*scale, u), fmtLevel(ref*w.Q[5]*scale, u))
	}
	return b.String()
}

// methodDetail is a member's tool data in a few words.
func methodDetail(s Specialist, log *toolLog, m *measurement, u Unit) string {
	if log == nil {
		return "no tool data"
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	switch s.Method {
	case "history":
		var parts []string
		if m != nil && m.blend != nil {
			parts = append(parts, "H1 blend")
		} else if m != nil && m.h1 {
			parts = append(parts, "H1")
		}
		if len(log.symbols) > 0 {
			syms := slices.Clone(log.symbols)
			slices.Sort(syms)
			parts = append(parts, "history_stats: "+strings.Join(slices.Compact(syms), ", "))
		}
		return cmp.Or(strings.Join(parts, "; "), "measured history")
	case "fundamentals":
		var parts []string
		for _, l := range log.scen.levels {
			parts = append(parts, fmt.Sprintf("%s %s %.0f%%", l.Kind, fmtLevel(l.Level, u), l.P*100))
		}
		return "scenario_model: " + strings.Join(parts, ", ")
	case "volatility":
		if o := log.outlook; o != nil {
			return fmt.Sprintf("price_outlook: zero drift, volatility %.0f%%/yr", o.VolAnnPct)
		}
		if len(log.symbols) > 0 {
			return "history_stats lognormal: " + strings.Join(slices.Compact(slices.Sorted(slices.Values(log.symbols))), ", ")
		}
		return "price_outlook"
	case "regime":
		var parts []string
		for _, e := range log.stress.events {
			parts = append(parts, fmt.Sprintf("%s %.0f%% %+.0f%%", e.Name, e.P*100, e.Move*100))
		}
		return "stress_mix: " + strings.Join(parts, "; ")
	case "fallback":
		return "its tool failed: no tool data"
	}
	return "no tool data"
}

// readView is a distribution's numbers in a line: "median $580, P(≥ $485)
// 60%", or with full the 50% and 80% ranges too.
func readView(d Draft, probs []OutcomeP, ref float64, u Unit, full bool) string {
	bounds, ok := bucketBounds(d.Outcomes)
	if !ok {
		return ""
	}
	ds, ok := newDist(bounds, probsOf(d.Outcomes, probs))
	if !ok {
		return ""
	}
	s := "median " + fmtQ(ds.quantile(0.5), u)
	if full {
		s += fmt.Sprintf(", 50%% %s–%s, 80%% %s–%s", fmtQ(ds.quantile(0.25), u), fmtQ(ds.quantile(0.75), u), fmtQ(ds.quantile(0.1), u), fmtQ(ds.quantile(0.9), u))
	}
	if ref > 0 {
		s += fmt.Sprintf(", P(≥ %s) %.0f%%", todayLevel(ref, u), (1-ds.cdf(ref))*100)
	}
	return s
}

// logView records the desk's calibration-relevant numbers (no behaviour):
// finalWidth is the aggregator's final's, before any guard; floorWeight the
// volatility floor's weight on H1's lognormal (0: it did not fire).
func logView(m *Methods, final, pl []OutcomeP, finalWidth, refWidth, floorWeight float64) {
	methods := make([]string, len(m.Weights))
	for i, w := range m.Weights {
		methods[i] = w.Method
	}
	guard := "" // the value, not the pointer's address
	if m.Guard != nil {
		guard = *m.Guard
	}
	slog.Info("forecast view", "agreement", m.Agreement, "methods", strings.Join(methods, ","), "hnFinal", entropy(probsOfOrder(final)),
		"hnPool", entropy(probsOfOrder(pl)), "tvFinalPool", tv(final, pl), "widthFinal", finalWidth, "widthMeasured", refWidth, "guard", guard,
		"floorWeight", floorWeight)
}

// okMembers is the members that answered, in role order (the panel).
func okMembers(specs []Specialist, errs []error) []Specialist {
	var out []Specialist
	for i := range specs {
		if errs[i] == nil {
			out = append(out, specs[i])
		}
	}
	return out
}

// read fills each answered member's server record: the method its tool
// calls show, its method's anchor, its distribution read as a view (with
// the median it stated) and, for the quant, its valuation scenarios.
func (k *deskKit) read(specs []Specialist, stated []*float64, errs []error) {
	h1 := k.m != nil && k.m.h1
	for i := range specs {
		if errs[i] != nil {
			continue
		}
		s, log := &specs[i], k.logs[specs[i].Role]
		if log == nil {
			log = newToolLog()
		}
		s.Method = log.method(s.Role, h1)
		s.Anchor = anchorOf(s.Method, log, k.m, k.fr, k.d.Outcomes)
		s.family = log.family(s.Method)
		s.Stats = statsOf(k.d, s.Probabilities, k.reference(), stated[i])
		s.Model = nil
		if s.Method == "fundamentals" {
			log.mu.Lock()
			for _, l := range log.scen.levels {
				ms := ModelScenario{Name: l.Name, Kind: l.Kind, P: l.P, Price: l.Level, Inputs: l.Inputs}
				if ref := log.scen.ref; ref > 0 {
					ms.VsRefPct = ptr((l.Level/ref - 1) * 100)
				}
				s.Model = append(s.Model, ms)
			}
			log.mu.Unlock()
		}
	}
}

// driftMassTol is how much more of its probability than the capped drift's
// lognormal a final may put above the drift's own median and the view still
// rest on that drift: the precision finals are written in. H1 prints its
// lognormal column to 0.1 point, the aggregator writes whole percents and
// normalize rescales sums up to 2 points off. A pace slack (0.05 points a
// year, or of the change under a year) was finer than that, 0.03 to 0.25
// points of probability crossing the median, so near the drift rounding
// decided the basis: TSM's one-year final copied from H1's own column
// (8.4/16.6/18.1/21.7/16.7/10.7/7.8) read +7.88 against the drift's +7.82 and
// lost its median. The ceiling (deskView), not this tolerance, keeps a view
// on the drift from stating more than the drift.
const driftMassTol = 0.01

// driftReading is a final read against the capped drift (driftRead).
type driftReading struct {
	over bool    // the final's centre is above the drift's beyond driftMassTol: no median, no chance
	at   float64 // the drift's own median, today's level × e^{μT}: the most a view on the drift states
	// chance is the most P(≥ today) a view on the drift states: the drift
	// lognormal's own, read off the same ranges, plus driftMassTol (0: not read).
	chance float64
}

// driftRead reads a final against the capped drift on the question's
// ranges. at is the drift's own median, today's level × e^{μT}, which a view
// on the drift never states more than (deskView). over: the final puts more
// of its probability above at than the drift's lognormal does, both read as
// §4.8 reads them (uniform in log inside a range), by more than
// driftMassTol, so its centre is above the drift's beyond the precision it
// is written in (a 50% range above at puts 75% there). Reading both at the
// same point on the same ranges cancels the reading's error, so the drift's
// lognormal itself is never over (about half its probability reads above
// at: 50.24% for NVDA at 90 days). One measure at every horizon, in the unit
// the final is written in.
//
// The mass rule looks at one point, the drift's median, so a final with the
// drift's centre and a narrower spread passes it with more of its
// probability between today and that median: NVDA at ten years at half H1's
// σ read 85% at or above today against the drift's 69.6%. The volatility
// floor (volFloor) widens such a final to H1's σ at its own median first;
// as a backstop the view's chance is held as its median is, at most the
// drift lognormal's own P(≥ today), read off the same ranges at today's
// level, plus driftMassTol.
func (m *measurement) driftRead(d Draft, probs []OutcomeP, days int) driftReading {
	bounds, ok := bucketBounds(d.Outcomes)
	ref := m.spot()
	if !ok || !(ref > 0) || days <= 0 {
		return driftReading{}
	}
	final, ok := newDist(bounds, probsOf(d.Outcomes, probs))
	if !ok {
		return driftReading{}
	}
	years := float64(days) / 365
	r := driftReading{at: ref * math.Exp(m.mu*years)}
	below, above := 0.5, 0.5 // the lognormal's exact shares when it does not read on these ranges
	if m.vol > 0 {
		above = 0.5 * math.Erfc(-m.mu*math.Sqrt(years)/m.vol/math.Sqrt2)
	}
	if ln, ok := newDist(bounds, m.lognormal); ok {
		below, above = ln.cdf(r.at), 1-ln.cdf(ref)
	}
	if r.over = below-final.cdf(r.at) > driftMassTol; r.over {
		return r
	}
	r.chance = above + driftMassTol
	return r
}

// aboveDrift is the basis of a view whose centre the capped drift does not
// support and no cited scenario backs.
var aboveDrift = ViewBasis{Kind: "none", Label: "Read from the desk's ranges: its centre sits above what the measured history at the capped drift supports and no cited scenario backs it, so it states no median."}

// The notes that end a capped_drift label when the view states the drift's
// figures rather than the final's (driftRead) and the page prints them
// differently (deskView): its median held at the drift's own, its chance of
// closing at or above today held to at most a point over the drift's, or
// both.
const (
	heldNote       = "; the median shown is the drift's own, a little under where the ranges alone put it"
	heldChanceNote = "; the chance shown of closing at or above today is at most a point over the drift's, under where the ranges alone put it"
	heldBothNote   = "; the median shown is the drift's own and the chance of closing at or above today at most a point over the drift's, both under where the ranges alone put them"
)

// heldNoteOf is the note for what a view on the drift holds ("" for nothing).
func heldNoteOf(median, chance bool) string {
	switch {
	case median && chance:
		return heldBothNote
	case median:
		return heldNote
	case chance:
		return heldChanceNote
	}
	return ""
}

// basis is what the run's view of probs rests on (ViewBasis): the quant's
// valuation scenarios when one of their inputs is cited and verified, else
// the measured history's capped-drift lognormal when the panel saw it and
// the final's centre is not above that drift's (driftRead: the cap is a
// ceiling, and a higher centre without cited inputs is a promise without
// evidence), with the drift's own median and its chance of closing at or
// above today (plus driftMassTol) as the most the view states, else none.
// The label's held notes are deskView's: only a hold the page prints says so.
func (k *deskKit) basis(panel []Specialist, probs []OutcomeP) ViewBasis {
	if q := k.logs["quant"]; q != nil && slices.ContainsFunc(panel, func(s Specialist) bool { return s.Role == "quant" }) {
		q.mu.Lock()
		cited := q.scen != nil && slices.ContainsFunc(q.scen.levels, func(l market.ScenarioLevel) bool {
			return slices.ContainsFunc(l.Inputs, func(in market.Input) bool { return in.Kind == "source" && in.Verified })
		})
		q.mu.Unlock()
		if cited {
			label := "Valuation scenarios with cited inputs"
			if k.m != nil && k.m.h1 {
				label += ", weighed with the measured history"
			}
			return ViewBasis{Kind: "scenarios", Label: label}
		}
	}
	if m := k.m; m != nil && m.lognormal != nil {
		r := m.driftRead(k.d, probs, k.fr.HorizonDays)
		if r.over {
			return aboveDrift
		}
		return ViewBasis{Kind: "capped_drift", ceiling: r.at, chance: r.chance,
			Label: fmt.Sprintf("Measured %s history since %s; %s", m.sub.Symbol, m.stats.From[:4], driftWords(m))}
	}
	return noBasis
}

// volWidthTol is how much narrower than H1's capped-drift lognormal, as a
// share of its 80% log width, a final may be and the volatility floor leave
// it: the precision finals are written in. H1's column (0.1 point a range)
// reads at most 0.44% narrower than the lognormal itself over 20,000 random
// ladders; whole percents at most 1.74% on the live check's 22 ladders and
// over 2% on 0.65% of the random ones (at most 3.2%).
const volWidthTol = 0.02

// The methods notes of a final the volatility floor moved (volFloor): one
// narrower than the measured volatility, or one only more sure of a gain
// than the capped drift (moved toward H1's σ at the median its view states,
// the drift's own only when that caps it).
const (
	volSentence     = "Server check: the final distribution was widened to the measured volatility: no cited input supports a narrower range."
	volGainSentence = "Server check: the final distribution was moved toward the measured volatility around the median shown: no cited input supports a higher chance of closing at or above today."
)

// volFloor keeps a final whose view would rest on the capped drift (no cited
// scenario input: basis capped_drift) from being more confident than the
// measured volatility, on every panel, independent agreement and no measured
// windows included, and it widens such a final around its own centre. Its
// floor is H1's lognormal at the full-history σ moved to the final's own
// median, the one its view states (at most the drift's own: deskView's
// ceiling), read on the same ranges (lognormalAt: its reading puts half its
// probability below that median). A final whose 80% log width (logWidth, the
// width guard's measure) is under (1 − volWidthTol) × that lognormal's, or
// whose P(≥ today) reads above the chance a view on the drift may state (the
// drift lognormal's own plus driftMassTol), is mixed toward it,
// (1 − w)·final + w·lognormal, with the least weight w that brings it within
// both, and the mix becomes the final.
//
// Inside the closed ranges §4.8's reading is linear in the probabilities, so
// a mix of two distributions that both read half their probability below
// the median reads the same (in an open range the lognormal is centred on
// the mix's reading for each weight): the view's median, change and pace
// are the final's own, and a cautious or bearish final stays so. Its chance
// ends between its own and the lognormal's: a lognormal final's moves toward
// 50%, only as far as the widening takes it; a lumpy final's can move away
// from 50%, toward the lognormal's. The share above the drift's own median
// can only fall (the lognormal's centre is at or under the drift's), so the
// basis never changes, and that lognormal's chance is at or under the drift
// lognormal's, so the chance bound is reachable. A lognormal at H1's σ, at
// any centre, is its own reference and is never widened: reading both on the
// same ranges cancels the reading's error (an earlier drift-centred reference
// read a final at H1's σ but under the drift up to 4% narrower, and
// moved it up). Every readout then comes from one distribution: the 50%
// range, the bars and P(≥ today) agree, and the chance hold (deskView) does
// not bind.
//
// The chance part can narrow a final at or above the floor, toward H1's σ
// and never under the floor, 0.98 of the lognormal's width (the final's own
// width can fall further): a lumpy final as wide as the lognormal but with
// more of its probability just above today. Leaving it as written for the
// view's chance hold brought P9's contradictions back (on 7,150 of the live
// check's stress finals, 35 held chances, 19 with the bars from today more
// than a point off the tile, up to 8.8, and 4 ranges that rule the chance
// out). narrow reports the width's part (the note says which). A final on
// cited scenarios, over the drift (none), without H1's lognormal or whose
// median does not read is untouched (w = 0).
func (k *deskKit) volFloor(panel []Specialist, probs []OutcomeP) (out []OutcomeP, w float64, narrow bool) {
	m := k.m
	bounds, ok := bucketBounds(k.d.Outcomes)
	ref := m.spot()
	if !ok || !(ref > 0) || len(m.lognormal) != len(k.d.Outcomes) || len(probs) != len(k.d.Outcomes) {
		return probs, 0, false
	}
	b := k.basis(panel, probs)
	if b.Kind != "capped_drift" {
		return probs, 0, false
	}
	final, sum := probsOf(k.d.Outcomes, probs), 0.0
	for _, p := range final {
		sum += p
	}
	for i := range final {
		final[i] /= sum
	}
	fd, ok := newDist(bounds, final)
	if !ok || !fd.log {
		return probs, 0, false
	}
	// The median the view states: the final's own, at most the drift's.
	median := fd.quantile(0.5)
	capped := b.ceiling > 0 && median.Value > b.ceiling
	if capped {
		median.Value = b.ceiling
	}
	below := func(ps []float64) float64 {
		ds, ok := newDist(bounds, ps)
		if !ok {
			return math.NaN()
		}
		return ds.cdf(median.Value)
	}
	logn := m.lognormalAt(bounds, k.fr.HorizonDays, median.Value, below)
	if logn == nil {
		return probs, 0, false
	}
	mixed := make([]float64, len(final))
	mixOf := func(w float64, ln []float64) []float64 {
		for i, p := range final {
			mixed[i] = (1-w)*p + w*ln[i]
		}
		return mixed
	}
	// In an open range §4.8's tail is not linear in the probabilities, so for
	// a median there the lognormal is centred for each weight on the mix's
	// own reading, which keeps the median there too.
	mix := func(w float64) []float64 {
		if capped || !median.Approx || w == 0 {
			return mixOf(w, logn)
		}
		if ln := m.lognormalAt(bounds, k.fr.HorizonDays, median.Value, func(g []float64) float64 { return below(mixOf(w, g)) }); ln != nil {
			return mixOf(w, ln)
		}
		return mixOf(w, logn)
	}
	// The width guard's measure (logWidth) and P(≥ today), read as §4.8 reads them.
	read := func(ps []float64) (width, chance float64) {
		ds, ok := newDist(bounds, ps)
		if !ok || !ds.log {
			return 0, 0
		}
		return math.Log(ds.quantile(0.9).Value / ds.quantile(0.1).Value), 1 - ds.cdf(ref)
	}
	lw, _ := read(logn)
	floor := (1 - volWidthTol) * lw
	width, _ := read(final)
	narrow = floor > 0 && width < floor
	within := func(w float64) bool {
		width, chance := read(mix(w))
		return (!(floor > 0) || width >= floor) && (b.chance == 0 || chance <= b.chance)
	}
	if within(0) {
		return probs, 0, false
	}
	// The first weight on a 1/64 grid within both, then bisection down to
	// the least one (the width need not grow with w: a lumpy final can widen
	// past the lognormal on the way). None: the final stands.
	lo, hi := 0.0, -1.0
	for s := 1; s <= 64; s++ {
		if within(float64(s) / 64) {
			hi = float64(s) / 64
			break
		}
		lo = float64(s) / 64
	}
	if hi < 0 {
		return probs, 0, false
	}
	for range 30 {
		if mid := (lo + hi) / 2; within(mid) {
			hi = mid
		} else {
			lo = mid
		}
	}
	ps := mix(hi)
	out = make([]OutcomeP, len(probs))
	for i, p := range probs { // in the final's own order
		out[i] = OutcomeP{p.Outcome, ps[slices.Index(k.d.Outcomes, p.Outcome)]}
	}
	return out, hi, narrow
}

// lognormalAt is H1's lognormal at the full-history σ on the question's
// ranges, centred so that below of its probabilities, which falls as the
// centre rises, is one half: with below the §4.8 reading under median, the
// lognormal whose reading puts half its probability below median. A
// lognormal's reading is not its own median (a range is read uniform in
// log), so centring on the reading is what keeps a mix's median where it
// was. The centre is found by regula falsi (the Illinois variant) inside a
// bracket around median, to 1e-13 of probability. nil without a σ, a horizon
// or such a centre.
func (m *measurement) lognormalAt(bounds [][2]float64, days int, median float64, below func([]float64) float64) []float64 {
	spot := m.spot()
	if !(m.vol > 0) || days <= 0 || !(spot > 0) || !(median > 0) {
		return nil
	}
	years, edges := float64(days)/365, edgesOf(bounds)
	at := func(c float64) ([]float64, float64) { // c: the centre's log move from today
		ps := market.DriftMixture(spot, years, m.vol, c/years).Buckets(edges)
		return ps, below(ps) - 0.5
	}
	s := m.vol * math.Sqrt(years)
	lo, hi := math.Log(median/spot)-s, math.Log(median/spot)+s
	_, flo := at(lo)
	for i := 0; i < 8 && flo <= 0; i++ {
		lo -= s
		_, flo = at(lo)
	}
	_, fhi := at(hi)
	for i := 0; i < 8 && fhi >= 0; i++ {
		hi += s
		_, fhi = at(hi)
	}
	if !(flo > 0) || !(fhi < 0) {
		return nil
	}
	best, bestF := lo, flo
	for i, side := 0, 0; i < 200 && hi-lo > 1e-15*(1+math.Abs(lo)) && math.Abs(bestF) > 1e-13; i++ {
		c := hi - fhi*(hi-lo)/(fhi-flo)
		if !(c > lo && c < hi) {
			c = (lo + hi) / 2
		}
		_, fc := at(c)
		if math.Abs(fc) < math.Abs(bestF) {
			best, bestF = c, fc
		}
		switch {
		case fc > 0:
			lo, flo = c, fc
			if side > 0 {
				fhi /= 2
			}
			side = 1
		case fc < 0:
			hi, fhi = c, fc
			if side < 0 {
				flo /= 2
			}
			side = -1
		default:
			lo, hi = c, c
		}
	}
	ps, _ := at(best)
	return ps
}

// reference is today's measured price when the question measures it: the
// view's and the members' reference (nil otherwise).
func (k *deskKit) reference() *ViewRef {
	if k == nil || k.fr.Measure != "price" || k.m == nil {
		return nil
	}
	return k.m.ref
}

// refWidth is the narrower of the measured windows' and the scenario
// model's 80% log widths (0 when neither was measured).
func (k *deskKit) refWidth() float64 {
	w := k.m.windowWidth()
	if q := k.logs["quant"]; q != nil {
		q.mu.Lock()
		if q.scen != nil && q.scen.width > 0 && (w == 0 || q.scen.width < w) {
			w = q.scen.width
		}
		q.mu.Unlock()
	}
	return w
}

// methodFlag matches a model-written duplicate flag about the panel's
// methods, which the server judges itself.
var methodFlag = regexp.MustCompile(`(?i)lognormal|same (model|assumption)`)

// finish applies a numeric frame's server rules to the sanitized result:
// the desk's weights (else the priors), the weighted pool, the guard (which
// can only flatten the final number toward the pool), then the volatility
// floor (a final on the capped drift alone, mixed toward H1's lognormal),
// the duplicate flag with its confidence cap, and the Methods record.
func (k *deskKit) finish(res *Result, ext aggExtras, ag agreement) {
	panel := res.Specialists
	w, why, from := panelWeights(ext.Weights, panel, k.fr)
	pl, eff := pool(panel, w, ag.groups)
	finalWidth, refWidth := logWidth(k.d, res.Probabilities), k.refWidth()
	m := &Methods{Plan: k.fr.Plan, WeightsFrom: from, Pool: pl, Agreement: ag.class, Weights: []MethodWeight{},
		Note: truncate(strings.TrimSpace(ext.MethodNote), 300)}
	for i, s := range panel {
		mw := MethodWeight{Role: s.Role, Method: s.Method, Weight: eff[i]}
		if y, ok := why[s.Role]; ok {
			mw.Why = ptr(y)
		}
		m.Weights = append(m.Weights, mw)
	}
	width := func(p []OutcomeP) float64 { return logWidth(k.d, p) }
	if guarded, trigger := guardFinal(res.Probabilities, pl, ag.class, width, refWidth); trigger != "" {
		res.Probabilities, m.Guard = guarded, ptr(trigger)
		m.Note = strings.TrimSpace(m.Note + " " + guardSentence)
	}
	// Then the volatility floor, on every panel: the last word on the stored final.
	floored, floorW, narrow := k.volFloor(panel, res.Probabilities)
	if floorW > 0 {
		res.Probabilities, m.Guard = floored, ptr("volatility")
		m.Note = strings.TrimSpace(m.Note + " " + map[bool]string{true: volSentence, false: volGainSentence}[narrow])
	}
	res.Flags = slices.DeleteFunc(res.Flags, func(f Flag) bool { return f.Type == "duplicate" && methodFlag.MatchString(f.Note) })
	if ag.class == "duplicate" {
		for _, g := range ag.groups {
			res.Flags = append(res.Flags, duplicateFlag(panel, g, k.fr.Kind == "level"))
		}
		if res.Confidence == "high" {
			res.Confidence = "medium"
		}
	}
	res.Methods = m
	logView(m, res.Probabilities, pl, finalWidth, refWidth, floorW)
}

// scenarios is the result's scenarios: the quant's last scenario_model
// record (when the quant is in the panel) and the skeptic's stress events,
// with their odds read off the final probabilities (sanitizeScenarios).
func (k *deskKit) scenarios(panel []Specialist, probs []OutcomeP, texts []scenarioText) []DeskScenario {
	in := func(role string) bool {
		return slices.ContainsFunc(panel, func(s Specialist) bool { return s.Role == role })
	}
	var levels []market.ScenarioLevel
	if q := k.logs["quant"]; q != nil && in("quant") {
		q.mu.Lock()
		if q.scen != nil {
			levels = q.scen.levels
		}
		q.mu.Unlock()
	}
	var events []stressEvent
	base := 0.0
	if sk := k.logs["skeptic"]; sk != nil && in("skeptic") {
		sk.mu.Lock()
		if sk.stress != nil {
			events, base = sk.stress.events, sk.stress.baseMedian
		}
		sk.mu.Unlock()
	}
	return sanitizeScenarios(k.d, probs, k.reference(), k.fr.HorizonDays, levels, events, base, texts, k.known)
}

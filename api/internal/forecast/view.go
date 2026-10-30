package forecast

// The desk view: what an ordered numeric answer leads with. The
// final bucket probabilities are read as a distribution (log-uniform inside
// each closed range, exponential open tails) for a median, central ranges and
// P(at or above today), so a near-uniform set of buckets reads as a wide view
// with a centre instead of a "most likely range" one point above uniform. Pure
// arithmetic on the probabilities: the same view is derived for older rows.

import (
	"cmp"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/vincenttanjaya/Foretrade/api/internal/market"
)

const (
	longHorizonDays = 180  // past this a price question is long-horizon: log buckets, the view headline
	flatRatio       = 1.5  // flat when the top range holds under 1.5× an even share,
	flatEntropy     = 0.92 // or the normalized entropy is at least this,
	flatGap         = 0.03 // or the top two ranges are within 3 points
	bucketLeadP     = 0.40 // a short-horizon view may lead with its top range from 40%
)

// Q is a value read off the buckets; Approx when it lands in an open-ended
// range, where the tail's shape is an assumption (the page prints "≈").
type Q struct {
	Value  float64 `json:"value"`
	Approx bool    `json:"approx,omitempty"`
}

// Unit is how the question writes its numbers: a currency prefix ("$",
// "S$", "NT$") and a suffix ("%", "bp").
type Unit struct {
	Prefix string `json:"prefix"`
	Suffix string `json:"suffix"`
}

// ViewRef is today's measured level of the subject (Yahoo or Binance), never
// a model's or a web page's number.
type ViewRef struct {
	Value  float64 `json:"value"`
	Label  string  `json:"label"`  // "TSM, NYSE regular-market price"
	AsOf   string  `json:"asOf"`   // RFC 3339, UTC
	Source string  `json:"source"` // "Yahoo Finance (delayed)" | "Binance spot"
}

type ViewEdge struct {
	Value    float64  `json:"value"`
	VsRefPct *float64 `json:"vsRefPct"` // log scale with a reference only
}

// DeskView is the server's reading of the final distribution (contract
// DeskView). Lead is the only place the desk's numbers are written as prose.
type DeskView struct {
	Scale       string     `json:"scale"` // log | linear
	Unit        Unit       `json:"unit"`
	HorizonDays int        `json:"horizonDays"`
	Reference   *ViewRef   `json:"reference"`
	Median      Q          `json:"median"`
	P10         Q          `json:"p10"`
	P25         Q          `json:"p25"`
	P75         Q          `json:"p75"`
	P90         Q          `json:"p90"`
	PAbove      *Q         `json:"pAbove"`    // P(close ≥ reference); null without one
	ChangePct   *float64   `json:"changePct"` // log scale: median vs the reference, %
	Change      *float64   `json:"change"`    // linear scale: median − reference
	AnnualPct   *float64   `json:"annualPct"` // log scale, horizons of a year or more
	Flat        bool       `json:"flat"`
	Headline    string     `json:"headline"` // view | bucket
	Edges       []ViewEdge `json:"edges"`
	Lead        string     `json:"lead"`
	Basis       ViewBasis  `json:"basis"`
	Derived     bool       `json:"derived,omitempty"` // computed on read for an older row stored without a view
}

// ViewBasis is what the view's centre rests on. The desk states a median,
// a change or P(at or above today) only on evidence (the owner's rule: no
// bad numbers, no promise without evidence): the quant's valuation scenarios
// with at least one cited input that its source states, or the measured
// history's lognormal at the capped drift (labelled an assumption). Without
// either ("none": an older row, a failed measurement, a question with no
// subject) the view gives the ranges only; its median stays in the data
// for the bars but is not a claim.
type ViewBasis struct {
	Kind  string `json:"kind"`  // scenarios | capped_drift | none
	Label string `json:"label"` // one line for the page, with the drift's label when there is one
	// ceiling (capped_drift) is the drift's own median, today's level ×
	// e^{μT}: the view states no median above it (0: none).
	ceiling float64
	// chance (capped_drift) is the drift lognormal's own P(≥ today), read off
	// the same ranges, plus driftMassTol: the view states no higher P(above)
	// (0: none).
	chance float64
}

// anchored reports a basis the desk may state a median on.
func (b ViewBasis) anchored() bool { return b.Kind == "scenarios" || b.Kind == "capped_drift" }

// noBasis is the basis of a view no evidence anchors.
var noBasis = ViewBasis{Kind: "none", Label: "Read from the desk's ranges: no cited scenarios or measured history set its centre, so it states no median."}

// ViewBrief is a list row's slice of the view (contract ForecastSummary.view).
type ViewBrief struct {
	Median   Q      `json:"median"`
	P10      Q      `json:"p10"`
	P90      Q      `json:"p90"`
	PAbove   *Q     `json:"pAbove"`
	Unit     Unit   `json:"unit"`
	Flat     bool   `json:"flat"`
	Headline string `json:"headline"`
	Basis    string `json:"basis"` // ViewBasis.Kind: a row states its median only when it is not "none"
}

func (v *DeskView) brief() *ViewBrief {
	if v == nil {
		return nil
	}
	return &ViewBrief{Median: v.Median, P10: v.P10, P90: v.P90, PAbove: v.PAbove, Unit: v.Unit, Flat: v.Flat, Headline: v.Headline, Basis: cmp.Or(v.Basis.Kind, "none")}
}

// dist is an ordered question's final buckets as a distribution in t = ln x
// (log scale) or x (linear): uniform in t inside each closed range, and an
// exponential tail on each open end whose density meets its neighbour's.
type dist struct {
	log        bool
	u          []float64 // the K−1 finite edges in t, ascending
	p          []float64 // the K bucket probabilities
	lam0, lamN float64   // the tails' scales in t
}

// newDist reads probs (in bounds' order) on bounds (bucketBounds: open ends,
// at least 3 ranges). ok is false on a shape mismatch.
func newDist(bounds [][2]float64, probs []float64) (dist, bool) {
	k := len(bounds)
	if k < 3 || len(probs) != k {
		return dist{}, false
	}
	d := dist{log: true, p: make([]float64, k)}
	for _, b := range bounds[1:] {
		if !(b[0] > 0) {
			d.log = false
		}
	}
	sum := 0.0
	for i, p := range probs {
		if !(p >= 0) || math.IsInf(p, 0) {
			return dist{}, false
		}
		d.p[i] = max(p, 1e-9)
		sum += d.p[i]
	}
	for i := range d.p {
		d.p[i] /= sum
	}
	for _, b := range bounds[1:] {
		d.u = append(d.u, d.t(b[0]))
	}
	w1, wn := d.u[1]-d.u[0], d.u[k-2]-d.u[k-3]
	d.lam0 = min(max(d.p[0]*w1/d.p[1], 0.25*w1), 2*w1)
	d.lamN = min(max(d.p[k-1]*wn/d.p[k-2], 0.25*wn), 2*wn)
	return d, true
}

func (d dist) t(x float64) float64 {
	if d.log {
		return math.Log(x)
	}
	return x
}

func (d dist) x(t float64) float64 {
	if d.log {
		return math.Exp(t)
	}
	return t
}

// cdf is P(X < x).
func (d dist) cdf(x float64) float64 {
	if d.log && !(x > 0) {
		return 0
	}
	t, n := d.t(x), len(d.u)
	if t < d.u[0] {
		return d.p[0] * math.Exp((t-d.u[0])/d.lam0)
	}
	if t >= d.u[n-1] {
		return 1 - d.p[n]*math.Exp(-(t-d.u[n-1])/d.lamN)
	}
	c := d.p[0]
	for i := 1; i < n; i++ {
		if t < d.u[i] {
			return c + d.p[i]*(t-d.u[i-1])/(d.u[i]-d.u[i-1])
		}
		c += d.p[i]
	}
	return c
}

// quantile inverts cdf piecewise; approx when q falls in an open end.
func (d dist) quantile(q float64) Q {
	n := len(d.u)
	switch {
	case q < d.p[0]:
		return Q{d.x(d.u[0] + d.lam0*math.Log(q/d.p[0])), true}
	case q > 1-d.p[n]:
		return Q{d.x(d.u[n-1] - d.lamN*math.Log((1-q)/d.p[n])), true}
	}
	c := d.p[0]
	for i := 1; i < n; i++ {
		if q <= c+d.p[i] {
			return Q{d.x(d.u[i-1] + (q-c)/d.p[i]*(d.u[i]-d.u[i-1])), false}
		}
		c += d.p[i]
	}
	return Q{d.x(d.u[n-1]), false}
}

// inTail reports whether x lies in an open-ended range.
func (d dist) inTail(x float64) bool {
	t := d.t(x)
	return t < d.u[0] || t >= d.u[len(d.u)-1]
}

// entropy is the normalized entropy of the buckets: 1 for an even spread.
func entropy(p []float64) float64 {
	h := 0.0
	for _, x := range p {
		if x > 0 {
			h -= x * math.Log(x)
		}
	}
	return h / math.Log(float64(len(p)))
}

// flat reports a distribution with no range that stands out.
func flat(p []float64) bool {
	s := slices.Sorted(slices.Values(p))
	top, second := s[len(s)-1], s[len(s)-2]
	return top < flatRatio/float64(len(p)) || entropy(p) >= flatEntropy || top-second < flatGap
}

// probsOf is probs' values in outcomes' order (by label).
func probsOf(outcomes []string, probs []OutcomeP) []float64 {
	out := make([]float64, len(outcomes))
	for i, o := range outcomes {
		out[i] = pOf(probs, o)
	}
	return out
}

// deskView reads the final probabilities of an ordered question as the desk
// view; ref is the subject's measured level (nil without one), basis what
// its centre rests on: without one (noBasis) the view states no P(above) or
// change and its lead gives the ranges only; on the capped drift its median
// is at most the drift's own (basis.ceiling), so neither it nor the change
// and pace read from it state more than the drift, and its P(above) at most
// the drift's own plus driftMassTol (basis.chance). The quantiles stay the
// final's readings. A hold that changes a figure as the page prints it ends
// the label with its note (heldNoteOf); one too small to print says
// nothing. nil when the buckets do not parse.
func deskView(d Draft, probs []OutcomeP, ref *ViewRef, horizonDays int, basis ViewBasis) *DeskView {
	bounds, ok := bucketBounds(d.Outcomes)
	if d.Shape != "ordered" || !ok {
		return nil
	}
	p := probsOf(d.Outcomes, probs)
	ds, ok := newDist(bounds, p)
	if !ok {
		return nil
	}
	v := &DeskView{Scale: "linear", Unit: unitOf(d.Outcomes), HorizonDays: horizonDays, Reference: ref, Edges: []ViewEdge{},
		P10: ds.quantile(0.10), P25: ds.quantile(0.25), P75: ds.quantile(0.75), P90: ds.quantile(0.90), Basis: basis}
	if ds.log {
		v.Scale = "log"
	}
	states := ref != nil && (!ds.log || ref.Value > 0) && basis.anchored()
	v.centre(ds.quantile(0.5), ds, states)
	readMedian := v.medianWords(ds, bounds)
	if c := basis.ceiling; c > 0 && v.Median.Value > c {
		v.centre(Q{c, ds.inTail(c)}, ds, states)
	}
	readChance := ""
	if states {
		v.PAbove = &Q{1 - ds.cdf(ref.Value), ds.inTail(ref.Value)}
		readChance = chanceWords(*v.PAbove)
		if c := basis.chance; c > 0 && v.PAbove.Value > c {
			v.PAbove.Value = c
		}
	}
	if basis.Kind == "capped_drift" {
		v.Basis.Label += heldNoteOf(v.medianWords(ds, bounds) != readMedian, v.PAbove != nil && chanceWords(*v.PAbove) != readChance)
	}
	for _, b := range bounds[1:] {
		e := ViewEdge{Value: b[0]}
		if ref != nil && ds.log && ref.Value > 0 {
			e.VsRefPct = ptr((b[0]/ref.Value - 1) * 100)
		}
		v.Edges = append(v.Edges, e)
	}
	v.Flat = flat(ds.p)
	v.Headline = "view"
	if !v.Flat && horizonDays <= longHorizonDays && slices.Max(ds.p) >= bucketLeadP {
		v.Headline = "bucket"
	}
	v.Lead = viewLead(v, d.ResolutionDate, ds, bounds)
	return v
}

// centre sets the view's median and, when the view states them, the change
// and the pace read from it against today's level.
func (v *DeskView) centre(median Q, ds dist, states bool) {
	v.Median, v.ChangePct, v.AnnualPct, v.Change = median, nil, nil, nil
	if !states {
		return
	}
	ref := v.Reference.Value
	if !ds.log {
		v.Change = ptr(median.Value - ref)
		return
	}
	v.ChangePct = ptr((median.Value/ref - 1) * 100)
	if v.HorizonDays >= 365 {
		v.AnnualPct = ptr((math.Pow(median.Value/ref, 365/float64(v.HorizonDays)) - 1) * 100)
	}
}

// medianWords is every way the page prints the view's median: the hero's
// and the list row's figure ("≈$1,250"), the lead's ("above $1,100") and
// the change and pace read from it.
func (v *DeskView) medianWords(ds dist, bounds [][2]float64) string {
	return fmtQ(v.Median, v.Unit) + " | " + leadMedian(v, ds, bounds) + " | " + leadChange(v)
}

// chanceWords is P(at or above today) as the page prints it: "≈62%". A
// chance whose whole percent rounds to 0 or 100 reads "<1%" or ">99%", with
// no ≈ (the binary hero's rule; the web's fmtChance is the same).
func chanceWords(q Q) string {
	switch s := fmt.Sprintf("%.0f", q.Value*100); s {
	case "0", "-0":
		return "<1%"
	case "100":
		return ">99%"
	default:
		return map[bool]string{true: "≈"}[q.Approx] + s + "%"
	}
}

// leadMedian is the lead's median: "$611", or "above $1,100" in an open range.
func leadMedian(v *DeskView, ds dist, bounds [][2]float64) string {
	if !v.Median.Approx {
		return fmtLevel(v.Median.Value, v.Unit)
	}
	if ds.t(v.Median.Value) < ds.u[0] {
		return "below " + fmtLevel(bounds[0][1], v.Unit)
	}
	return "above " + fmtLevel(bounds[len(bounds)-1][0], v.Unit)
}

// leadChange is the lead's change from today and its pace: ", +26% from
// today's $485 (about +4.7% a year)"; none for a median in an open range.
func leadChange(v *DeskView) string {
	ref := v.Reference
	if ref == nil || v.Median.Approx {
		return ""
	}
	switch {
	case v.ChangePct != nil:
		s := fmt.Sprintf(", %s from today's %s", signedPct(*v.ChangePct), todayLevel(ref.Value, v.Unit))
		if v.AnnualPct != nil {
			s += fmt.Sprintf(" (about %+.1f%% a year)", *v.AnnualPct)
		}
		return s
	case v.Change != nil:
		return fmt.Sprintf(", %s from today's %s", signedLevel(*v.Change, v.Unit), todayLevel(ref.Value, v.Unit))
	}
	return ""
}

// specStats is a panel member's distribution read the same way.
type SpecStats struct {
	Median       Q        `json:"median"`
	P10          Q        `json:"p10"`
	P90          Q        `json:"p90"`
	PAbove       *Q       `json:"pAbove"`
	StatedMedian *float64 `json:"statedMedian"` // the median the member wrote, in the question's unit
	derived      bool     // filled on read for an older row (derive): never stored
}

func statsOf(d Draft, probs []OutcomeP, ref *ViewRef, stated *float64) *SpecStats {
	bounds, ok := bucketBounds(d.Outcomes)
	if d.Shape != "ordered" || !ok {
		return nil
	}
	ds, ok := newDist(bounds, probsOf(d.Outcomes, probs))
	if !ok {
		return nil
	}
	s := &SpecStats{Median: ds.quantile(0.5), P10: ds.quantile(0.10), P90: ds.quantile(0.90)}
	if ref != nil && (!ds.log || ref.Value > 0) {
		s.PAbove = &Q{1 - ds.cdf(ref.Value), ds.inTail(ref.Value)}
	}
	if stated != nil && !math.IsNaN(*stated) && !math.IsInf(*stated, 0) && (!ds.log || *stated > 0) {
		s.StatedMedian = ptr(*stated)
	}
	return s
}

// viewLead is the view's numbers line, fixed templates:
// "Our median is $611 for Oct 6, 2031, +26% from today's $485 (about +4.7% a
// year); 50% range $363–$989, 80% range ≈$242–≈$1,480; 62% chance it closes
// at or above $485." A median in an open range reads "above $1,100". The
// desk's levels are 3 significant figures (fmtLevel); today's level is
// printed at its own precision (todayLevel: "$472.20", "$83,048"), the level
// the change and the chance are computed at.
func viewLead(v *DeskView, date string, ds dist, bounds [][2]float64) string {
	u := v.Unit
	var b strings.Builder
	if !v.Basis.anchored() { // no evidence for a centre: the ranges only
		b.WriteString("The desk's 50% range")
		if t, err := time.Parse(time.DateOnly, date); err == nil {
			b.WriteString(" for " + t.Format("Jan 2, 2006"))
		}
		fmt.Fprintf(&b, " is %s–%s and its 80%% range %s–%s.", fmtQ(v.P25, u), fmtQ(v.P75, u), fmtQ(v.P10, u), fmtQ(v.P90, u))
		return b.String()
	}
	b.WriteString("Our median is " + leadMedian(v, ds, bounds))
	if t, err := time.Parse(time.DateOnly, date); err == nil {
		b.WriteString(" for " + t.Format("Jan 2, 2006"))
	}
	b.WriteString(leadChange(v))
	fmt.Fprintf(&b, "; 50%% range %s–%s, 80%% range %s–%s", fmtQ(v.P25, u), fmtQ(v.P75, u), fmtQ(v.P10, u), fmtQ(v.P90, u))
	if v.PAbove != nil && v.Reference != nil {
		fmt.Fprintf(&b, "; %s chance it closes at or above %s", chanceWords(*v.PAbove), todayLevel(v.Reference.Value, u))
	}
	b.WriteString(".")
	return b.String()
}

// todayLevel is today's level in the desk's prose: a price at its own
// precision (fmtPrice: "$472.20", "S$73.85", "$83,048"); a level in % or bp,
// or not above zero, as the desk writes its levels (fmtLevel). The web's
// fmtToday is the same rule.
func todayLevel(x float64, u Unit) string {
	if u.Suffix != "" || !(x > 0) {
		return fmtLevel(x, u)
	}
	return fmtPrice(x, u)
}

func fmtQ(q Q, u Unit) string {
	if q.Approx {
		return "≈" + fmtLevel(q.Value, u)
	}
	return fmtLevel(q.Value, u)
}

// signedPct is a change in percent: whole points from ±10%, else one decimal.
func signedPct(x float64) string {
	if math.Abs(x) >= 9.95 {
		return fmt.Sprintf("%+.0f%%", x)
	}
	return fmt.Sprintf("%+.1f%%", x)
}

func signedLevel(x float64, u Unit) string {
	s := fmtLevel(math.Abs(x), u)
	if x < 0 {
		return "-" + s
	}
	return "+" + s
}

// fmtLevel writes x in the question's unit at 3 significant figures,
// grouped, compact (M, B, T) from a million: "$611", "$1,480", "$2.12M",
// "S$0.552", "2.5%".
func fmtLevel(x float64, u Unit) string {
	sign := ""
	if x < 0 {
		sign, x = "-", -x
	}
	var s string
	switch {
	case math.IsNaN(x) || math.IsInf(x, 0):
		s = "?"
	case x == 0:
		s = "0"
	case roundSig(x, 3) >= 1e6:
		unit, suffix := 1e6, "M"
		for _, next := range []struct {
			unit   float64
			suffix string
		}{{1e9, "B"}, {1e12, "T"}} {
			if roundSig(x, 3) >= next.unit {
				unit, suffix = next.unit, next.suffix
			}
		}
		m := roundSig(x/unit, 3)
		if m >= 1000 {
			s = groupDigits(strconv.FormatFloat(math.Round(m), 'f', 0, 64)) + suffix
		} else {
			s = strconv.FormatFloat(m, 'f', max(0, 2-int(math.Floor(math.Log10(m)))), 64) + suffix
		}
	default:
		r := roundSig(x, 3)
		if r >= 1000 {
			s = groupDigits(strconv.FormatFloat(r, 'f', 0, 64))
		} else {
			s = strconv.FormatFloat(r, 'f', max(0, 2-int(math.Floor(math.Log10(r)))), 64)
			if u.Suffix != "" || u.Prefix == "" {
				if strings.Contains(s, ".") {
					s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
				}
			}
		}
	}
	return sign + u.Prefix + s + u.Suffix
}

func groupDigits(s string) string {
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// The currency marks a label may put before its first number, longest first.
var unitPrefixes = []string{"US$", "HK$", "NT$", "S$", "A$", "C$", "$", "€", "£", "¥", "₩"}

var (
	firstNumber = regexp.MustCompile(`\d`)
	bpSuffix    = regexp.MustCompile(`(?i)^\s*(bps?|basis points?)\b`)
)

// unitOf is the unit of an ordered question's labels: the currency mark
// before the first number of the first label that has one, and "%" or "bp"
// after it.
func unitOf(outcomes []string) Unit {
	for _, o := range outcomes {
		loc := firstNumber.FindStringIndex(o)
		if loc == nil {
			continue
		}
		var u Unit
		before := strings.TrimRightFunc(o[:loc[0]], unicode.IsSpace)
		for _, p := range unitPrefixes {
			if strings.HasSuffix(before, p) {
				u.Prefix = p
				break
			}
		}
		rest := strings.TrimLeft(o[loc[0]:], "0123456789.,")
		switch {
		case strings.HasPrefix(strings.TrimSpace(rest), "%"):
			u.Suffix = "%"
		case bpSuffix.MatchString(rest):
			u.Suffix = "bp"
		}
		return u
	}
	return Unit{}
}

// answerFirst lints a summary into a thesis: sentences about the process
// (the ensemble, the pool, the panel, the specialists, staying close to
// something) go, as long as one sentence remains; a summary of nothing else
// becomes lead when there is one (numeric frames), else stays as it was.
// At most 600 runes.
func answerFirst(summary, lead string) string {
	var kept []string
	for _, s := range sentences(summary) {
		if !slices.ContainsFunc(processTalk, func(re *regexp.Regexp) bool { return re.MatchString(s) }) {
			kept = append(kept, s)
		}
	}
	out := strings.Join(kept, " ")
	if out == "" {
		out = cmp.Or(lead, strings.TrimSpace(summary))
	}
	return truncate(out, 600)
}

var processTalk = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(ensemble|the pool|the panel|specialists?|aggregat\w*|average of|plain average)\b`),
	regexp.MustCompile(`(?i)\b(we|the desk)\s+(stay|stick|remain|keep)\s+(close\s+)?(to|with)\b`),
	regexp.MustCompile(`(?i)\b(reproduces|matches|echoes)\s+the\s+specialists`),
}

// sentences splits text after ".", "!" or "?" followed by whitespace, so
// "$1.5" stays whole.
func sentences(text string) []string {
	var out []string
	rs := []rune(strings.TrimSpace(text))
	start := 0
	for i := 0; i < len(rs); i++ {
		if strings.ContainsRune(".!?", rs[i]) && i+1 < len(rs) && unicode.IsSpace(rs[i+1]) {
			if s := strings.TrimSpace(string(rs[start : i+1])); s != "" {
				out = append(out, s)
			}
			start = i + 1
		}
	}
	if s := strings.TrimSpace(string(rs[start:])); s != "" {
		out = append(out, s)
	}
	return out
}

// DeskScenario is one scenario on the result (contract DeskScenario): its
// level and inputs from the quant's scenario_model record (a tail from the
// skeptic's stress_mix), its range a partition of the line between
// neighbouring levels, and its odds read off the final buckets.
type DeskScenario struct {
	Kind       string         `json:"kind"` // tail | bear | base | bull
	Name       string         `json:"name"`
	P          float64        `json:"p"`      // the final distribution's mass on [lo, hi)
	ModelP     *float64       `json:"modelP"` // the quant's (or the stress event's) own weight
	Lo         *float64       `json:"lo"`     // null: open below
	Hi         *float64       `json:"hi"`     // null: open above
	Level      float64        `json:"level"`
	VsRefPct   *float64       `json:"vsRefPct"`
	AnnualPct  *float64       `json:"annualPct"`
	Inputs     []market.Input `json:"inputs"`
	Thesis     string         `json:"thesis"`
	MustBeTrue []string       `json:"mustBeTrue"`
	SourceIDs  []string       `json:"sourceIds"`
}

// scenarioText is the aggregator's words for one scenario kind.
type scenarioText struct {
	Kind       string   `json:"kind"`
	Name       string   `json:"name"`
	Thesis     string   `json:"thesis"`
	MustBeTrue []string `json:"mustBeTrue"`
}

// stressEvent is one of the skeptic's priced events, as stress_mix recorded it.
type stressEvent struct {
	Name    string
	P, Move float64 // probability over the horizon; move as a fraction (−0.6)
	Inputs  []market.Input
}

var idToken = regexp.MustCompile(`\b[SFH]\d+\b`)

// sanitizeScenarios builds the result's scenarios: levels and inputs only
// from the quant's last scenario_model (sc), plus a tail from the skeptic's
// most negative stress event of at least 3% when the model has no downside
// tail and the event lands below every model level (baseMedian: the stress
// base's median, the H1 lognormal's); sorted by level, each range [lo, hi) cut
// at the geometric (linear scale: arithmetic) mean of neighbouring levels, and
// p = the final distribution's mass on it, so the scenarios' odds cannot
// contradict what is scored. Texts come from the aggregator's entry of the
// same kind, capped; their ids are kept when the run has them. nil under 2.
func sanitizeScenarios(d Draft, probs []OutcomeP, ref *ViewRef, horizonDays int, sc []market.ScenarioLevel, events []stressEvent, baseMedian float64,
	texts []scenarioText, known func(id string) bool) []DeskScenario {
	bounds, ok := bucketBounds(d.Outcomes)
	if d.Shape != "ordered" || !ok || len(sc) == 0 {
		return nil
	}
	ds, ok := newDist(bounds, probsOf(d.Outcomes, probs))
	if !ok {
		return nil
	}
	var out []DeskScenario
	for _, s := range sc {
		out = append(out, DeskScenario{Kind: s.Kind, Name: s.Name, ModelP: ptr(s.P), Level: s.Level, Inputs: slices.Clone(s.Inputs)})
	}
	// Only the model's downside tail (at or below its other levels) stands in
	// for the stress tail: an upside one above the bull does not.
	hasTail := slices.ContainsFunc(sc, func(t market.ScenarioLevel) bool {
		return t.Kind == "tail" && !slices.ContainsFunc(sc, func(s market.ScenarioLevel) bool { return s.Kind != "tail" && s.Level < t.Level })
	})
	if !hasTail && baseMedian > 0 {
		var worst *stressEvent
		for i, e := range events {
			if e.P >= 0.03 && e.Move < 0 && (worst == nil || e.Move < worst.Move) {
				worst = &events[i]
			}
		}
		lowest := slices.MinFunc(out, func(a, b DeskScenario) int { return cmp.Compare(a.Level, b.Level) }).Level
		if worst != nil && baseMedian*(1+worst.Move) < lowest {
			out = append(out, DeskScenario{Kind: "tail", Name: worst.Name, ModelP: ptr(worst.P), Level: baseMedian * (1 + worst.Move), Inputs: slices.Clone(worst.Inputs)})
		}
	}
	if len(out) < 2 {
		return nil
	}
	// Texts before the sort, so the model's scenarios (the ones the aggregator
	// saw under Methods) come before the stress tail: an upside model tail and
	// the stress tail are both "tail", and the super-cycle's thesis must never
	// land on the blockade.
	used := make([]bool, len(texts))
	for i := range out {
		s := &out[i]
		s.MustBeTrue = []string{}
		if j := scenarioTextFor(texts, used, out, i); j >= 0 {
			used[j] = true
			s.Thesis = truncate(strings.TrimSpace(texts[j].Thesis), 240)
			for _, m := range texts[j].MustBeTrue {
				if m = truncate(strings.TrimSpace(m), 160); m != "" && len(s.MustBeTrue) < 3 {
					s.MustBeTrue = append(s.MustBeTrue, m)
				}
			}
		}
	}
	slices.SortStableFunc(out, func(a, b DeskScenario) int { return cmp.Compare(a.Level, b.Level) })
	for i := range out {
		s := &out[i]
		if i > 0 {
			s.Lo = ptr(cut(ds, out[i-1].Level, s.Level))
		}
		if i < len(out)-1 {
			s.Hi = ptr(cut(ds, s.Level, out[i+1].Level))
		}
		lo, hi := 0.0, 1.0
		if s.Lo != nil {
			lo = ds.cdf(*s.Lo)
		}
		if s.Hi != nil {
			hi = ds.cdf(*s.Hi)
		}
		s.P = hi - lo
		if ref != nil && ref.Value > 0 && s.Level > 0 {
			s.VsRefPct = ptr((s.Level/ref.Value - 1) * 100)
			if horizonDays >= 365 {
				s.AnnualPct = ptr((math.Pow(s.Level/ref.Value, 365/float64(horizonDays)) - 1) * 100)
			}
		}
		s.SourceIDs = []string{}
		if s.Inputs == nil {
			s.Inputs = []market.Input{}
		}
		for _, id := range idToken.FindAllString(s.Thesis+" "+strings.Join(s.MustBeTrue, " "), -1) {
			if known(id) && !slices.Contains(s.SourceIDs, id) {
				s.SourceIDs = append(s.SourceIDs, id)
			}
		}
	}
	return out
}

// scenarioTextFor is the index of the unused aggregator text for out[i], -1
// for none: of its kind, the one named like it, else the first not named
// for another scenario.
func scenarioTextFor(texts []scenarioText, used []bool, out []DeskScenario, i int) int {
	same := func(a, b string) bool { return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b)) }
	pick := -1
	for j, t := range texts {
		if used[j] || !same(t.Kind, out[i].Kind) {
			continue
		}
		if same(t.Name, out[i].Name) {
			return j
		}
		if pick < 0 && !slices.ContainsFunc(out, func(o DeskScenario) bool { return same(t.Name, o.Name) }) {
			pick = j
		}
	}
	return pick
}

// cut is the boundary between two neighbouring scenario levels.
func cut(ds dist, a, b float64) float64 {
	if ds.log && a > 0 && b > 0 {
		return math.Sqrt(a * b)
	}
	return (a + b) / 2
}

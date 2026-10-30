package market

import (
	"cmp"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Scenario and drift mixtures: the view a long-horizon price answer leads
// with. A view is 1-5 paths in log price, each a drifted Brownian motion that
// reaches its scenario's level at the horizon, weighted by the scenario's p.
// The chat's price_outlook card uses it now; the forecast desk's
// scenario_model next round, so both price a view the same way.

const (
	assumedVol       = 0.30 // σ when no measured history exists; always labelled
	pathVolFloor     = 0.5  // within-scenario σ never below half the measured σ
	medianGridStep   = 0.25 // years
	medianGridMax    = 30.0 // years
	extrapolateAfter = 2.0  // time-to-target rows past 2 × the scenarios' horizon are marked extrapolated
)

var (
	targetYears = []int{1, 3, 5, 10, 15, 20}
	ladderYears = []int{3, 5, 10, 15, 20}
)

// ScenarioInput is one scenario as a model writes it (the JSON names are the
// tools'). Exactly one mode: EPS (eps, eps_year, growth_pct, pe; exit_year
// optional), level (a price at the horizon) or cagr (an annual price return).
// Every input carries a basis, verified by the caller.
type ScenarioInput struct {
	Name string  `json:"name"` // ≤ 60, e.g. "AI capex digestion"
	Kind string  `json:"kind"` // bear | base | bull | tail
	P    float64 `json:"p"`

	EPS         *float64 `json:"eps"`
	EPSYear     int      `json:"eps_year"`
	EPSBasis    string   `json:"eps_basis"`
	GrowthPct   *float64 `json:"growth_pct"`
	ExitYear    int      `json:"exit_year"`
	GrowthBasis string   `json:"growth_basis"`
	PE          *float64 `json:"pe"`
	PEBasis     string   `json:"pe_basis"`

	Level      *float64 `json:"level"`
	LevelBasis string   `json:"level_basis"`

	CAGRPct   *float64 `json:"cagr_pct"`
	CAGRBasis string   `json:"cagr_basis"`
}

// Input is one scenario input with its basis: Kind source (an id the caller
// verified against its text), model_knowledge ("model knowledge, as of
// YYYY-MM") or assumption. Verified only for a source whose text states it.
type Input struct {
	Field    string  `json:"field"` // eps | growthPct | pe | level | cagrPct
	Value    float64 `json:"value"`
	Year     *int    `json:"year,omitempty"` // eps_year for eps, exit_year for growthPct
	Basis    string  `json:"basis"`          // "[2]" | "quote" | "model knowledge, as of 2025-06" | "assumption"
	Kind     string  `json:"kind"`           // source | model_knowledge | assumption
	Verified bool    `json:"verified"`
}

// ScenarioLevel is a validated scenario: its price at the horizon.
type ScenarioLevel struct {
	Name   string
	Kind   string
	P      float64
	Level  float64
	Inputs []Input
}

var kindRank = map[string]int{"tail": 0, "bear": 1, "base": 2, "bull": 3}

const basisMsg = "each input needs a basis: a source id, 'model knowledge, as of YYYY-MM' or 'assumption'"

// ValidateScenarios checks 2-5 scenarios at a horizon of years (resolving in
// resYear) against spot and turns each into a level, sorted low to high.
// Errors are for the model to fix; warnings flag inputs outside the usual
// range without rejecting them. Levels in its text are US dollars.
func ValidateScenarios(in []ScenarioInput, spot, years float64, resYear int) ([]ScenarioLevel, []string, error) {
	return ValidateScenariosIn(in, spot, years, resYear, "$")
}

// ValidateScenariosIn is ValidateScenarios for levels in another currency:
// its errors and warnings write them with mark ("₩21,510", "S$6.06").
func ValidateScenariosIn(in []ScenarioInput, spot, years float64, resYear int, mark string) ([]ScenarioLevel, []string, error) {
	level := func(x float64) string { return FormatLevelIn(x, mark) }
	if len(in) < 2 || len(in) > 5 {
		return nil, nil, fmt.Errorf("give 2 to 5 scenarios, not %d", len(in))
	}
	if !(spot > 0) || !(years > 0) {
		return nil, nil, fmt.Errorf("no price or horizon to value the scenarios against")
	}
	thisYear := resYear - int(years) // the horizon's start year, near enough for the EPS-year window
	out := make([]ScenarioLevel, len(in))
	var warns []string
	sum := 0.0
	for i, s := range in {
		name := strings.TrimSpace(s.Name)
		if name == "" {
			return nil, nil, fmt.Errorf("scenario %d needs a name", i+1)
		}
		if utf8.RuneCountInString(name) > 60 {
			name = string([]rune(name)[:60])
		}
		if !(s.P >= 0.01 && s.P <= 0.95) {
			return nil, nil, fmt.Errorf("scenario %q: p must be 0.01..0.95", name)
		}
		sum += s.P
		epsMode := s.EPS != nil || s.GrowthPct != nil || s.PE != nil || s.EPSYear != 0
		if n := b2i(epsMode) + b2i(s.Level != nil) + b2i(s.CAGRPct != nil); n != 1 {
			return nil, nil, fmt.Errorf("scenario %q: give exactly one mode: eps, eps_year, growth_pct and pe; or level; or cagr_pct", name)
		}
		sc := ScenarioLevel{Name: name, Kind: strings.ToLower(strings.TrimSpace(s.Kind)), P: s.P}
		switch {
		case epsMode:
			if s.EPS == nil || s.GrowthPct == nil || s.PE == nil || s.EPSYear == 0 {
				return nil, nil, fmt.Errorf("scenario %q: EPS mode needs eps, eps_year, growth_pct and pe", name)
			}
			eps, g, pe := *s.EPS, *s.GrowthPct, *s.PE
			exit := s.ExitYear
			if exit == 0 {
				exit = resYear + 1 // forward EPS at the resolution date
			}
			switch {
			case !(eps > 0):
				return nil, nil, fmt.Errorf("scenario %q: the fundamentals model needs positive EPS; use level mode and say why", name)
			case s.EPSYear < thisYear-1 || s.EPSYear > resYear+1:
				return nil, nil, fmt.Errorf("scenario %q: eps_year must be %d..%d", name, thisYear-1, resYear+1)
			case exit < s.EPSYear:
				return nil, nil, fmt.Errorf("scenario %q: exit_year must be at or after eps_year", name)
			case !(g >= -40 && g <= 80):
				return nil, nil, fmt.Errorf("scenario %q: growth_pct must be -40..80", name)
			case !(pe >= 2 && pe <= 150):
				return nil, nil, fmt.Errorf("scenario %q: pe must be 2..150", name)
			case blank(s.EPSBasis, s.GrowthBasis, s.PEBasis):
				return nil, nil, fmt.Errorf("scenario %q: %s", name, basisMsg)
			}
			sc.Level = eps * math.Pow(1+g/100, float64(exit-s.EPSYear)) * pe
			epsYear := s.EPSYear
			sc.Inputs = []Input{input("eps", eps, &epsYear, s.EPSBasis), input("growthPct", g, &exit, s.GrowthBasis), input("pe", pe, nil, s.PEBasis)}
			if pe < 6 || pe > 60 {
				warns = append(warns, fmt.Sprintf("%s: a P/E of %g is outside the usual 6-60", name, pe))
			}
			if g < -20 || g > 40 {
				warns = append(warns, fmt.Sprintf("%s: EPS growth of %g%%/yr is outside the usual -20..40", name, g))
			}
		case s.Level != nil:
			if !(*s.Level > 0 && *s.Level >= spot/50 && *s.Level <= spot*200) {
				return nil, nil, fmt.Errorf("scenario %q: level must be between %s and %s", name, level(spot/50), level(spot*200))
			}
			if blank(s.LevelBasis) {
				return nil, nil, fmt.Errorf("scenario %q: %s", name, basisMsg)
			}
			sc.Level, sc.Inputs = *s.Level, []Input{input("level", *s.Level, nil, s.LevelBasis)}
		default:
			c := *s.CAGRPct
			if !(c >= -60 && c <= 150) {
				return nil, nil, fmt.Errorf("scenario %q: cagr_pct must be -60..150", name)
			}
			if blank(s.CAGRBasis) {
				return nil, nil, fmt.Errorf("scenario %q: %s", name, basisMsg)
			}
			sc.Level, sc.Inputs = spot*math.Pow(1+c/100, years), []Input{input("cagrPct", c, nil, s.CAGRBasis)}
		}
		if sc.Level > 10*spot || sc.Level < 0.1*spot {
			warns = append(warns, fmt.Sprintf("%s: %s is %s today's price", name, level(sc.Level), map[bool]string{true: "over 10×", false: "under 0.1×"}[sc.Level > spot]))
		}
		out[i] = sc
	}
	if math.Abs(sum-1) > 0.05 {
		return nil, nil, fmt.Errorf("probabilities must sum to 1 (they sum to %.2f)", sum)
	}
	for i := range out {
		out[i].P /= sum
	}
	inferKinds(out)
	if err := checkKinds(out, level); err != nil {
		return nil, nil, err
	}
	slices.SortStableFunc(out, func(a, b ScenarioLevel) int {
		if a.Level != b.Level {
			return cmp.Compare(a.Level, b.Level)
		}
		return kindRank[a.Kind] - kindRank[b.Kind]
	})
	return out, warns, nil
}

// checkKinds holds each kind to its price, whatever the array's order: every
// bear at or below every base and every base at or below every bull, and a
// tail at or beyond either end of them, below all (a war, a bust) or above
// all (a super-cycle: a multi-bagger question's upside tail is as honest as a
// downside one). An error names the kind and the price that disagree and the
// kinds that would fit, never the array's order: reordering cannot fix it.
func checkKinds(sc []ScenarioLevel, level func(float64) string) error {
	show := func(s ScenarioLevel) string { return fmt.Sprintf("%q (%s, %s)", s.Name, s.Kind, level(s.Level)) }
	var low, high *ScenarioLevel // the cheapest and dearest bear/base/bull
	for i, a := range sc {
		if a.Kind == "tail" {
			continue
		}
		if low == nil || a.Level < low.Level {
			low = &sc[i]
		}
		if high == nil || a.Level > high.Level {
			high = &sc[i]
		}
		for _, b := range sc {
			if b.Kind != "tail" && kindRank[a.Kind] < kindRank[b.Kind] && a.Level > b.Level {
				return fmt.Errorf("scenario kinds disagree with their prices: %s is above %s. A kind goes by price, not by its place in the array: "+
					"every bear below every base, every base below every bull. If a price is wrong (a sign or a P/E typo), fix the inputs that set it; if the prices are right, relabel by price (%s)", show(a), show(b), relabel(sc))
			}
		}
	}
	for _, t := range sc {
		if t.Kind != "tail" || low == nil || !(t.Level > low.Level && t.Level < high.Level) {
			continue
		}
		var fits []string
		for _, k := range []string{"bear", "base", "bull"} {
			if !slices.ContainsFunc(sc, func(s ScenarioLevel) bool {
				return s.Kind != "tail" && (kindRank[s.Kind] < kindRank[k] && s.Level > t.Level || kindRank[s.Kind] > kindRank[k] && s.Level < t.Level)
			}) {
				fits = append(fits, k)
			}
		}
		return fmt.Errorf("scenario kinds disagree with their prices: %s sits between %s and %s. A tail is an extreme case below every other scenario "+
			"(a war, a bust) or above them all (a super-cycle): price it below %s or above %s, or make it a %s",
			show(t), show(*low), show(*high), level(low.Level), level(high.Level), orList(fits))
	}
	return nil
}

// relabel is checkKinds' fix for bears, bases and bulls out of price order:
// the same kinds handed out low to high, as `make "B" bear, "A" base`.
func relabel(sc []ScenarioLevel) string {
	var idx []int
	var kinds []string
	for i, s := range sc {
		if s.Kind != "tail" {
			idx, kinds = append(idx, i), append(kinds, s.Kind)
		}
	}
	slices.SortStableFunc(idx, func(a, b int) int { return cmp.Compare(sc[a].Level, sc[b].Level) })
	slices.SortStableFunc(kinds, func(a, b string) int { return kindRank[a] - kindRank[b] })
	var moves []string
	for r, i := range idx {
		if sc[i].Kind != kinds[r] {
			moves = append(moves, fmt.Sprintf("%q %s", sc[i].Name, kinds[r]))
		}
	}
	return "make " + strings.Join(moves, ", ")
}

// orList is "bear", "bear or base" or "bear, base or bull".
func orList(xs []string) string {
	if len(xs) < 2 {
		return strings.Join(xs, "")
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " or " + xs[len(xs)-1]
}

// inferKinds names each scenario whose kind is not tail/bear/base/bull by its
// price rank: the lowest bear (tail when it is the fifth below a bear), the
// highest bull, the rest base.
func inferKinds(sc []ScenarioLevel) {
	order := make([]int, len(sc))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int { return cmp.Compare(sc[a].Level, sc[b].Level) })
	for r, i := range order {
		if _, ok := kindRank[sc[i].Kind]; ok {
			continue
		}
		switch {
		case r == 0 && len(sc) == 5:
			sc[i].Kind = "tail"
		case r == 0 || r == 1 && len(sc) == 5:
			sc[i].Kind = "bear"
		case r == len(sc)-1:
			sc[i].Kind = "bull"
		default:
			sc[i].Kind = "base"
		}
	}
}

func input(field string, v float64, year *int, basis string) Input {
	label, kind := ClassifyBasis(basis)
	return Input{Field: field, Value: v, Year: year, Basis: label, Kind: kind}
}

var (
	modelKnowledgeRe = regexp.MustCompile(`(?i)^model knowledge.*?\b((?:19|20)\d\d(?:-\d\d)?)\b`)
	assumptionRe     = regexp.MustCompile(`(?i)^assumption`)
)

// ClassifyBasis labels a basis: "model knowledge, as of YYYY-MM" (with its
// date), "assumption", or a source id for the caller to verify (kind source,
// the trimmed id as written). Model knowledge without a date is an assumption.
func ClassifyBasis(basis string) (label, kind string) {
	b := strings.TrimSpace(basis)
	if m := modelKnowledgeRe.FindStringSubmatch(b); m != nil {
		return "model knowledge, as of " + m[1], "model_knowledge"
	}
	if assumptionRe.MatchString(b) || strings.HasPrefix(strings.ToLower(b), "model knowledge") || b == "" {
		return "assumption", "assumption"
	}
	if utf8.RuneCountInString(b) > 40 {
		b = string([]rune(b)[:40])
	}
	return b, "source"
}

// Mixture is a price view at Years: each scenario a path in log price with
// drift ln(Level/Spot)/Years and annual σ SigmaPath, mixed by P.
type Mixture struct {
	Spot, Years, VolAnn float64 // VolAnn: measured long-run σ (annual), or the labelled 30% assumption
	Scen                []ScenarioLevel
	SigmaPath           float64 // annual σ around each path (variance-matched)
	VolAssumed          bool
	mu                  []float64 // annual log drift per scenario
	spread              float64   // between-scenario variance of the annual log drift
	drift               bool      // one historical-drift path, not scenarios at a horizon
}

// NewMixture is the scenarios' view. Variance matching: with spread = Σ p_s
// (m_s − m̄)² the between-scenario variance of the annual log return,
//
//	SigmaPath = sqrt(max(VolAnn² − T·spread, (pathVolFloor·VolAnn)²))
//
// so at the horizon the mixture's total log variance is the measured VolAnn²·T
// unless the scenarios alone spread wider (then the within-scenario σ is
// floored at half the measured σ): the view moves the centre and the skew; it
// cannot narrow the spread below what the asset's own history shows. Without
// a measured σ (volAnn ≤ 0) VolAnn is assumedVol and VolAssumed is set.
func NewMixture(spot, years, volAnn float64, sc []ScenarioLevel) Mixture {
	m := Mixture{Spot: spot, Years: years, VolAnn: volAnn, Scen: sc}
	if !(volAnn > 0) {
		m.VolAnn, m.VolAssumed = assumedVol, true
	}
	mean := 0.0
	for _, s := range sc {
		mu := math.Log(s.Level/spot) / years
		m.mu = append(m.mu, mu)
		mean += s.P * mu
	}
	spread := 0.0
	for i, s := range sc {
		spread += s.P * (m.mu[i] - mean) * (m.mu[i] - mean)
	}
	m.spread = spread
	m.SigmaPath = m.sigmaAt(years)
	return m
}

// sigmaAt is the within-scenario σ for a view read at t years: variance
// matched at t up to the scenarios' horizon (the paths have spread apart only
// t·spread by then, so a fixed SigmaPath would make a 1-year read narrower
// than measured volatility), SigmaPath beyond it.
func (m Mixture) sigmaAt(t float64) float64 {
	v := max(m.VolAnn, 0.01)
	if m.drift {
		return v
	}
	return math.Sqrt(max(v*v-min(t, m.Years)*m.spread, pathVolFloor*pathVolFloor*v*v))
}

// DriftMixture is one path at annual log drift mu with σ = volAnn: the
// historical-drift view, and with mu = 0 the volatility-only baseline.
func DriftMixture(spot, years, volAnn, mu float64) Mixture {
	m := Mixture{Spot: spot, Years: years, VolAnn: volAnn, drift: true, mu: []float64{mu},
		Scen: []ScenarioLevel{{Name: "drift", Kind: "base", P: 1, Level: spot * math.Exp(mu*years)}}}
	if !(volAnn > 0) {
		m.VolAnn, m.VolAssumed = assumedVol, true
	}
	m.SigmaPath = max(m.VolAnn, 0.01)
	return m
}

// ZeroDrift is the volatility-only baseline, OutlookFrom's model (median spot).
func ZeroDrift(spot, years, volAnn float64) Mixture { return DriftMixture(spot, years, volAnn, 0) }

// Mu is scenario i's annual log drift.
func (m Mixture) Mu(i int) float64 { return m.mu[i] }

// CDF is P(S_T < x) at the horizon.
func (m Mixture) CDF(x float64) float64 { return m.cdfAt(x, m.Years) }

func (m Mixture) cdfAt(x, t float64) float64 {
	if !(x > 0) {
		return 0
	}
	s := m.sigmaAt(t) * math.Sqrt(t)
	p := 0.0
	for i, sc := range m.Scen {
		p += sc.P * normCDF((math.Log(x/m.Spot)-m.mu[i]*t)/s)
	}
	return p
}

// Quantile is the price with CDF q at the horizon: bisection on log x, 60 steps.
func (m Mixture) Quantile(q float64) float64 {
	s := m.SigmaPath * math.Sqrt(m.Years)
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, mu := range m.mu {
		lo, hi = min(lo, mu*m.Years-12*s), max(hi, mu*m.Years+12*s)
	}
	for range 60 {
		mid := (lo + hi) / 2
		if m.CDF(m.Spot*math.Exp(mid)) < q {
			lo = mid
		} else {
			hi = mid
		}
	}
	return m.Spot * math.Exp((lo+hi)/2)
}

// Buckets is the probability of each range the edges cut (len(edges)+1,
// open-ended at both ends), summing to 1.
func (m Mixture) Buckets(edges []float64) []float64 {
	out := make([]float64, len(edges)+1)
	prev := 0.0
	for i, e := range edges {
		c := m.CDF(e)
		out[i], prev = c-prev, c
	}
	out[len(edges)] = 1 - prev
	return out
}

// Passage is the view's pEnd and pTouch for x·spot after years (longHorizon's
// passage per path, mixed by p, at sigmaAt(years)). Beyond the horizon each
// path keeps its drift.
func (m Mixture) Passage(x, years float64) (pEnd, pTouch float64) {
	sig := m.sigmaAt(years)
	for i, sc := range m.Scen {
		e, t := passage(x, m.mu[i], sig, years)
		pEnd, pTouch = pEnd+sc.P*e, pTouch+sc.P*t
	}
	return pEnd, min(pTouch, 1)
}

// TimeToTarget answers "by when": the odds of a multiple of spot by year
// under the view and the zero-drift baseline, the view's median time, each
// scenario's years, and the annual return each horizon requires.
type TimeToTarget struct {
	Multiple    float64     `json:"multiple"`
	Target      float64     `json:"target"` // multiple × spot
	Rows        []TargetRow `json:"rows"`   // years 1, 3, 5, 10, 15, 20
	MedianYears *float64    `json:"medianYears"`
	// MedianExtrapolated: the median assumes a scenario view's returns carry
	// on past extrapolateAfter × its horizon, as the rows
	// there are flagged; false for a drift view.
	MedianExtrapolated bool            `json:"medianExtrapolated"`
	ByScenario         []ScenarioYears `json:"byScenario"`
	RequiredCAGR       []RequiredCAGR  `json:"requiredCagr"` // years 3, 5, 10, 15, 20
	OwnCAGRPct         *float64        `json:"ownCagrPct"`   // the asset's own price CAGR over its whole history
	OwnSince           *string         `json:"ownSince"`
}

type TargetRow struct {
	Years          int     `json:"years"`
	PEnd           float64 `json:"pEnd"` // under the view
	PTouch         float64 `json:"pTouch"`
	BaselinePEnd   float64 `json:"baselinePEnd"` // zero drift
	BaselinePTouch float64 `json:"baselinePTouch"`
	Extrapolated   bool    `json:"extrapolated"` // past 2 × the scenarios' horizon
}

type ScenarioYears struct {
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	AnnualPct float64  `json:"annualPct"` // the path's annual price return
	Years     *float64 `json:"years"`     // to the target on that path; null when it never gets there
}

type RequiredCAGR struct {
	Years int     `json:"years"`
	Pct   float64 `json:"pct"`
}

// NewTimeToTarget computes TimeToTarget for multiple (> 1) under view, with
// base the zero-drift baseline. MedianYears is the first t on a 0.25-year grid
// to 30 years where the view's pEnd reaches 0.5; nil beyond.
func NewTimeToTarget(view, base Mixture, multiple float64, ownCAGRPct *float64, ownSince *string) TimeToTarget {
	tt := TimeToTarget{Multiple: multiple, Target: multiple * view.Spot, RequiredCAGR: RequiredCAGRs(multiple, ladderYears),
		OwnCAGRPct: ownCAGRPct, OwnSince: ownSince}
	for _, y := range targetYears {
		r := TargetRow{Years: y, Extrapolated: !view.drift && float64(y) > extrapolateAfter*view.Years}
		r.PEnd, r.PTouch = view.Passage(multiple, float64(y))
		r.BaselinePEnd, r.BaselinePTouch = base.Passage(multiple, float64(y))
		tt.Rows = append(tt.Rows, r)
	}
	for k := 1; float64(k)*medianGridStep <= medianGridMax+1e-9; k++ {
		t := float64(k) * medianGridStep
		if pEnd, _ := view.Passage(multiple, t); pEnd >= 0.5 {
			tt.MedianYears, tt.MedianExtrapolated = &t, !view.drift && t > extrapolateAfter*view.Years
			break
		}
	}
	for i, s := range view.Scen {
		sy := ScenarioYears{Name: s.Name, Kind: s.Kind, AnnualPct: (math.Exp(view.mu[i]) - 1) * 100}
		if mu := view.mu[i]; mu > 0 {
			sy.Years = ptrF(math.Log(multiple) / mu)
		}
		tt.ByScenario = append(tt.ByScenario, sy)
	}
	return tt
}

// RequiredCAGRs is multiple^(1/t) − 1, in percent, for each t.
func RequiredCAGRs(multiple float64, years []int) []RequiredCAGR {
	out := make([]RequiredCAGR, len(years))
	for i, y := range years {
		out[i] = RequiredCAGR{Years: y, Pct: (math.Pow(multiple, 1/float64(y)) - 1) * 100}
	}
	return out
}

// ladder is LadderEdges' table: multiples of spot by s = σ√T.
var ladder = []struct {
	below float64
	x     []float64
}{
	{0.25, []float64{0.8, 0.9, 1, 1.1, 1.25}},
	{0.5, []float64{0.6, 0.8, 1, 1.25, 1.6, 2}},
	{0.9, []float64{0.5, 0.75, 1, 1.5, 2, 3}},
	{math.Inf(1), []float64{0.25, 0.5, 1, 2, 4, 8}},
}

var niceSteps = []float64{1, 1.2, 1.5, 2, 2.5, 3, 4, 5, 6, 7.5, 8, 10}

// LadderEdges cuts a long-horizon distribution at multiples of spot chosen by
// s = σ√T (never at the model's quantiles, so mass is not even by
// construction and a view shows as shape). Rounding, one rule: the 1× edge to
// 3 significant figures; every other edge to 2, then replaced by the nearest
// of {1, 1.2, 1.5, 2, 2.5, 3, 4, 5, 6, 7.5, 8} × 10^k when that is within 4%
// of the unrounded edge; if two edges collide, the pass is redone at 3
// significant figures without the nudge.
func LadderEdges(spot, s float64) []float64 { return roundEdges(spot, ladderRow(s)) }

func ladderRow(s float64) []float64 {
	for _, row := range ladder {
		if s < row.below {
			return row.x
		}
	}
	return ladder[len(ladder)-1].x
}

func roundEdges(spot float64, xs []float64) []float64 {
	for _, nudge := range []bool{true, false} {
		edges := make([]float64, len(xs))
		ok := true
		for i, x := range xs {
			raw := spot * x
			switch {
			case x == 1:
				edges[i] = roundSig(raw, 3)
			case nudge:
				edges[i] = roundSig(raw, 2)
				if n := nearestNice(raw); math.Abs(n-raw) < 0.04*raw {
					edges[i] = n
				}
			default:
				edges[i] = roundSig(raw, 3)
			}
			ok = ok && (i == 0 || edges[i] > edges[i-1])
		}
		if ok || !nudge {
			return edges
		}
	}
	return nil
}

const (
	maxLadderEdges = 7    // at most 8 buckets
	openTailMax    = 0.12 // the share of the view an open end bucket should hold at most
	ladderReach    = 12   // steps a ladder may grow past its row at either end
)

// ViewLadderEdges is LadderEdges(spot, s) moved to where the view sits. When
// the view's p75 is above the last edge or its p25 below the first (a strong
// view piling into an open end bucket), the ladder grows at the crowded end
// with its row's own outer step, up to 7 edges (8 buckets), then shifts that
// way, dropping an edge at the other end while that end's open bucket stays
// within 12%, until each open end holds at most 12% of the view where the
// edges allow. Spot stays an edge; edges round as LadderEdges'. The view's and
// the baseline's columns use the same edges.
func ViewLadderEdges(spot, s float64, view Mixture) []float64 {
	row := ladderRow(s)
	base := roundEdges(spot, row)
	if !(view.Quantile(0.75) > base[len(base)-1] || view.Quantile(0.25) < base[0]) {
		return base
	}
	n := len(row)
	down, up := row[0]/row[1], row[n-1]/row[n-2]
	var xs []float64
	for k := ladderReach; k >= 1; k-- {
		xs = append(xs, row[0]*math.Pow(down, float64(k)))
	}
	xs = append(xs, row...)
	for k := 1; k <= ladderReach; k++ {
		xs = append(xs, row[n-1]*math.Pow(up, float64(k)))
	}
	all := roundEdges(spot, xs)
	lo, hi, one := ladderReach, ladderReach+n-1, ladderReach+slices.Index(row, 1)
	low := func(i int) float64 { return view.CDF(all[i]) }
	high := func(j int) float64 { return 1 - view.CDF(all[j]) }
	for range 4 * ladderReach {
		switch {
		case high(hi) > openTailMax && hi+1 < len(all) && hi-lo+1 < maxLadderEdges:
			hi++
		case high(hi) > openTailMax && hi+1 < len(all) && lo < one && low(lo+1) <= openTailMax:
			lo, hi = lo+1, hi+1
		case low(lo) > openTailMax && lo > 0 && hi-lo+1 < maxLadderEdges:
			lo--
		case low(lo) > openTailMax && lo > 0 && hi > one && high(hi-1) <= openTailMax:
			lo, hi = lo-1, hi-1
		default:
			return roundEdges(spot, xs[lo:hi+1])
		}
	}
	return roundEdges(spot, xs[lo:hi+1])
}

func nearestNice(x float64) float64 {
	k := math.Pow(10, math.Floor(math.Log10(x)))
	best := k
	for _, n := range niceSteps {
		if math.Abs(n*k-x) < math.Abs(best-x) {
			best = n * k
		}
	}
	return roundSig(best, 3) // 1.5 × 0.1 is 0.15000000000000002 in floats
}

// FormatLevel formats a price level for bucket labels, summaries and
// warnings; the web's fmtLevelPrice follows the same table
// (testdata/format_level.json): three significant figures in millions from
// $1M ("$2.12M", "$12.3M"), then billions and trillions ("$1.20B"); whole
// dollars with commas from $1,000 ("$2,424"); from $1 two decimals
// ("$482.38", "$96.44"), except a level that is whole at cents prints none
// ("$60", "$250"); below $1 three significant figures, trailing zeros dropped
// down to two decimals ("$0.0882", "$0.50"), never an exponent. Rounding is
// x·10^d rounded half up, which the web repeats step for step, so a binary
// tie ($99.995, stored as 99.99499…) reads "$100" on both sides.
func FormatLevel(x float64) string {
	switch {
	case !(x > 0) || math.IsInf(x, 1):
		return "$0"
	case x >= 999_999.5:
		unit, suffix := 1e6, "M"
		if roundSig(x/1e6, 3) >= 1000 {
			unit, suffix = 1e9, "B"
			if roundSig(x/1e9, 3) >= 1000 {
				unit, suffix = 1e12, "T"
			}
		}
		m := roundSig(x/unit, 3)
		if m >= 1000 { // past $1,000T: grouped, as the web prints it
			return "$" + groupInt(int64(math.Round(m))) + suffix
		}
		return "$" + strconv.FormatFloat(m, 'f', 2-int(math.Floor(math.Log10(m))), 64) + suffix
	case x < 1:
		d := 2 - int(math.Floor(math.Log10(x))) // decimals for three significant figures
		r := math.Round(x*math.Pow(10, float64(d))) / math.Pow(10, float64(d))
		if r >= 1 {
			return "$1"
		}
		s := strconv.FormatFloat(r, 'f', d, 64)
		for strings.HasSuffix(s, "0") && len(s)-strings.IndexByte(s, '.') > 3 {
			s = s[:len(s)-1]
		}
		return "$" + s
	}
	cents := math.Round(x * 100)
	switch {
	case cents >= 100_000:
		return "$" + groupInt(int64(math.Round(x)))
	case math.Mod(cents, 100) == 0:
		return "$" + strconv.FormatInt(int64(cents/100), 10)
	}
	return "$" + strconv.FormatFloat(cents/100, 'f', 2, 64)
}

// FormatLevelIn is FormatLevel's rule with another currency's mark in place
// of "$": FormatLevelIn(262000, "₩") = "₩262,000", (73.85, "S$") = "S$73.85".
func FormatLevelIn(x float64, mark string) string {
	return mark + strings.TrimPrefix(FormatLevel(x), "$")
}

func groupInt(n int64) string {
	s := strconv.FormatInt(n, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// numberRe finds the numbers in a source's text with an optional scale word:
// "$16.56", "55.5%", "29.3x", "$1.2T", "950bn".
var numberRe = regexp.MustCompile(`(?i)(\d[\d,]*(?:\.\d+)?)(?:\s*(bn|tn|[kmbt])\b)?`)

var scaleOf = map[string]float64{"k": 1e3, "m": 1e6, "b": 1e9, "bn": 1e9, "t": 1e12, "tn": 1e12}

// NumberIn reports whether text states value: some number in it (as written,
// or times its scale word) within 0.5% relative. Lexical only: "FY27 EPS
// 21.33" and "21.33% margin" both match 21.33.
func NumberIn(value float64, text string) bool {
	v := math.Abs(value)
	for _, m := range numberRe.FindAllStringSubmatch(text, -1) {
		n, err := strconv.ParseFloat(strings.ReplaceAll(m[1], ",", ""), 64)
		if err != nil {
			continue
		}
		for _, c := range []float64{n, n * cmpScale(m[2])} {
			if math.Abs(v-c) <= 0.005*c {
				return true
			}
		}
	}
	return false
}

func cmpScale(word string) float64 {
	if s, ok := scaleOf[strings.ToLower(word)]; ok {
		return s
	}
	return 1
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func blank(xs ...string) bool {
	for _, x := range xs {
		if strings.TrimSpace(x) == "" {
			return true
		}
	}
	return false
}

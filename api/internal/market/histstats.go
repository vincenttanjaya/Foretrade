package market

import (
	"cmp"
	"math"
	"slices"
	"sort"
	"time"
)

// HistoryStats is what a daily series measures: realized volatility, price
// CAGRs, drawdowns and every rolling window of one horizon. Pure arithmetic on
// the closes; a figure the series cannot support is nil.
type HistoryStats struct {
	Symbol   string  `json:"symbol"`
	Source   string  `json:"source"`
	Currency string  `json:"currency"`
	From     string  `json:"from"` // YYYY-MM-DD
	To       string  `json:"to"`
	Years    float64 `json:"years"`
	Sessions int     `json:"sessions"`
	Last     float64 `json:"last"`
	High     float64 `json:"high"` // highest close
	HighDate string  `json:"highDate"`
	// VolAnnPct: stdev of daily log returns × √PerYear over the last 1 and 5
	// years ("y1", "y5") and the whole series ("all"); nil when the series does
	// not cover the window or has under 120 returns in it.
	VolAnnPct map[string]*float64 `json:"volAnnPct"`
	// CAGRPct: price CAGR over "y1", "y3", "y5", "y10" and "all"; nil when not
	// covered ("all" needs a year).
	CAGRPct        map[string]*float64 `json:"cagrPct"`
	MaxDrawdown    *Drawdown           `json:"maxDrawdown"`
	Drawdowns      []Drawdown          `json:"drawdowns"` // up to 3 more episodes deeper than 40%, deepest first
	Window         *Windows            `json:"window"`    // nil when the history is shorter than the horizon + 1 year
	DriftHalvedPct *float64            `json:"driftHalvedPct"`
}

// Drawdown is one fall from a running peak; Recovered is the first close back
// at or above the peak, nil when none yet.
type Drawdown struct {
	Pct       float64 `json:"pct"` // negative, e.g. −83.5
	Peak      string  `json:"peak"`
	Trough    string  `json:"trough"`
	Recovered *string `json:"recovered"`
}

// Windows is every rolling window of Days in the series: for each close, the
// ending multiple at the last close on or before Days later.
type Windows struct {
	Days        int        `json:"days"`
	N           int        `json:"n"`           // overlapping windows
	Independent int        `json:"independent"` // ⌊span / Days⌋: how many do not overlap
	Q           [7]float64 `json:"q"`           // ending multiple at p5, p10, p25, p50, p75, p90, p95
	PUp         float64    `json:"pUp"`         // share that ended higher
	PDouble     float64    `json:"pDouble"`     // ≥ 2×
	PHalve      float64    `json:"pHalve"`      // ≤ 0.5×
	Worst       WindowEnd  `json:"worst"`
	Best        WindowEnd  `json:"best"`
}

type WindowEnd struct {
	Multiple float64 `json:"multiple"`
	Start    string  `json:"start"`
	End      string  `json:"end"`
}

var windowQ = [7]float64{0.05, 0.10, 0.25, 0.50, 0.75, 0.90, 0.95}

const minVolReturns = 120

// ComputeHistoryStats measures s for a horizon of horizonDays (0 skips the
// windows). s needs at least two closes.
func ComputeHistoryStats(s DailySeries, horizonDays int) HistoryStats {
	cl := s.Closes
	h := HistoryStats{Symbol: s.Symbol, Source: s.Source, Currency: s.Currency,
		VolAnnPct: map[string]*float64{}, CAGRPct: map[string]*float64{}}
	if len(cl) < 2 {
		return h
	}
	first, last := cl[0], cl[len(cl)-1]
	h.From, h.To, h.Years, h.Sessions, h.Last = day(first.T), day(last.T), s.Years(), len(cl), last.C
	for _, k := range cl {
		if k.C > h.High {
			h.High, h.HighDate = k.C, day(k.T)
		}
	}
	for key, years := range map[string]int{"y1": 1, "y5": 5} {
		if i, ok := since(cl, last.T.AddDate(-years, 0, 0)); ok {
			h.VolAnnPct[key] = volPct(cl[i:], s.PerYear)
		} else {
			h.VolAnnPct[key] = nil
		}
	}
	h.VolAnnPct["all"] = volPct(cl, s.PerYear)
	for key, years := range map[string]int{"y1": 1, "y3": 3, "y5": 5, "y10": 10} {
		h.CAGRPct[key] = nil
		if i, ok := since(cl, last.T.AddDate(-years, 0, 0)); ok && cl[i].C > 0 {
			h.CAGRPct[key] = ptrF((math.Pow(last.C/cl[i].C, 1/float64(years)) - 1) * 100)
		}
	}
	h.CAGRPct["all"] = nil
	if h.Years >= 1 {
		h.CAGRPct["all"] = ptrF((math.Pow(last.C/first.C, 1/h.Years) - 1) * 100)
	}
	h.MaxDrawdown, h.Drawdowns = drawdowns(cl)
	h.Window = windows(s, horizonDays)
	if mu, ok := HalvedDrift(s); ok {
		h.DriftHalvedPct = ptrF(mu * 100)
	}
	return h
}

// since is the index of the last close on or before start: the window's
// anchor. false when the series starts more than a week after start.
func since(cl []DayClose, start time.Time) (int, bool) {
	i := sort.Search(len(cl), func(i int) bool { return cl[i].T.After(start) }) - 1
	if i < 0 {
		if cl[0].T.Sub(start) > 7*24*time.Hour {
			return 0, false
		}
		i = 0
	}
	return i, i < len(cl)-1
}

// volPct is the annualized sample stdev of daily log returns in percent, nil
// under minVolReturns returns.
func volPct(cl []DayClose, perYear float64) *float64 {
	if len(cl)-1 < minVolReturns {
		return nil
	}
	return ptrF(logVol(cl) * math.Sqrt(perYear) * 100)
}

func logVol(cl []DayClose) float64 {
	n := len(cl) - 1
	mean := math.Log(cl[n].C/cl[0].C) / float64(n)
	ss := 0.0
	for i := 1; i < len(cl); i++ {
		d := math.Log(cl[i].C/cl[i-1].C) - mean
		ss += d * d
	}
	return math.Sqrt(ss / float64(n-1))
}

// VolAnn is s's full-history annualized volatility as a fraction (0.31), 0
// under minVolReturns returns.
func (s DailySeries) VolAnn() float64 {
	if v := volPct(s.Closes, s.PerYear); v != nil {
		return *v / 100
	}
	return 0
}

// drawdowns walks the running peak: an episode starts below it and ends at the
// first close at or above it. The deepest is max; up to 3 more deeper than 40%
// follow, deepest first.
func drawdowns(cl []DayClose) (*Drawdown, []Drawdown) {
	var all []Drawdown
	peak, peakAt := cl[0].C, cl[0].T
	var cur *Drawdown
	for _, k := range cl[1:] {
		switch {
		case k.C >= peak:
			if cur != nil {
				r := day(k.T)
				cur.Recovered = &r
				all = append(all, *cur)
				cur = nil
			}
			peak, peakAt = k.C, k.T
		case cur == nil:
			cur = &Drawdown{Pct: (k.C/peak - 1) * 100, Peak: day(peakAt), Trough: day(k.T)}
		case (k.C/peak-1)*100 < cur.Pct:
			cur.Pct, cur.Trough = (k.C/peak-1)*100, day(k.T)
		}
	}
	if cur != nil {
		all = append(all, *cur)
	}
	if len(all) == 0 {
		return nil, nil
	}
	slices.SortStableFunc(all, func(a, b Drawdown) int { return cmp.Compare(a.Pct, b.Pct) })
	deepest := all[0]
	var more []Drawdown
	for _, d := range all[1:] {
		if d.Pct < -40 && len(more) < 3 {
			more = append(more, d)
		}
	}
	return &deepest, more
}

// windows is every rolling window of days whose end the series reaches; nil
// when the series spans less than days + 365.
func windows(s DailySeries, days int) *Windows {
	cl := s.Closes
	if days <= 0 || len(cl) < 2 || cl[len(cl)-1].T.Sub(cl[0].T) < time.Duration(days+365)*24*time.Hour {
		return nil
	}
	mult, starts, ends := windowMultiples(s, days)
	if len(mult) == 0 {
		return nil
	}
	w := &Windows{Days: days, N: len(mult), Independent: int(cl[len(cl)-1].T.Sub(cl[0].T).Hours() / 24 / float64(days))}
	worst, best := 0, 0
	for i, m := range mult {
		if m > 1 {
			w.PUp++
		}
		if m >= 2 {
			w.PDouble++
		}
		if m <= 0.5 {
			w.PHalve++
		}
		if m < mult[worst] {
			worst = i
		}
		if m > mult[best] {
			best = i
		}
	}
	n := float64(len(mult))
	w.PUp, w.PDouble, w.PHalve = w.PUp/n, w.PDouble/n, w.PHalve/n
	w.Worst = WindowEnd{mult[worst], day(starts[worst]), day(ends[worst])}
	w.Best = WindowEnd{mult[best], day(starts[best]), day(ends[best])}
	sorted := slices.Clone(mult)
	slices.Sort(sorted)
	for i, q := range windowQ {
		w.Q[i] = quantile(sorted, q)
	}
	return w
}

// windowMultiples is, for every close i whose T_i + days the series reaches,
// C_end / C_i with end the last close on or before T_i + days (the resolution
// rule: "if not a trading day, the last close before"). A level series gives
// C_end − C_i instead.
func windowMultiples(s DailySeries, days int) (mult []float64, starts, ends []time.Time) {
	cl := s.Closes
	lastT := cl[len(cl)-1].T
	j := 0
	for i := range cl {
		target := cl[i].T.AddDate(0, 0, days)
		if target.After(lastT) {
			break
		}
		for j+1 < len(cl) && !cl[j+1].T.After(target) {
			j++
		}
		v := cl[j].C / cl[i].C
		if s.Kind == "level" {
			v = cl[j].C - cl[i].C
		}
		mult, starts, ends = append(mult, v), append(starts, cl[i].T), append(ends, cl[j].T)
	}
	return mult, starts, ends
}

// WindowShares is the share of s's windows of horizonDays that would end in
// each [lo, hi) of bounds when applied to level (level × multiple; level +
// change for a level series), and how many windows are independent. Shares
// sum to 1 when bounds partition the line (±Inf ends).
func WindowShares(s DailySeries, horizonDays int, level float64, bounds [][2]float64) (shares []float64, independent int) {
	shares = make([]float64, len(bounds))
	if horizonDays <= 0 || len(s.Closes) < 2 {
		return shares, 0
	}
	mult, _, _ := windowMultiples(s, horizonDays)
	for _, m := range mult {
		x := level * m
		if s.Kind == "level" {
			x = level + m
		}
		for k, b := range bounds {
			if x >= b[0] && x < b[1] {
				shares[k]++
				break
			}
		}
	}
	if len(mult) > 0 {
		for k := range shares {
			shares[k] /= float64(len(mult))
		}
	}
	span := s.Closes[len(s.Closes)-1].T.Sub(s.Closes[0].T).Hours() / 24
	return shares, int(span / float64(horizonDays))
}

// HalvedDrift is the annual log drift ln(last/first)/years, halved and capped
// at ±25%/yr; ok from 2 years of history. Halved and capped because a single
// surviving listing's past return overstates its future one (survivorship):
// an assumption, labelled wherever it is shown. longHorizon's DriftCase uses
// the same convention.
func HalvedDrift(s DailySeries) (mu float64, ok bool) {
	years := s.Years()
	if years < 2 {
		return 0, false
	}
	first, last := s.Closes[0].C, s.Closes[len(s.Closes)-1].C
	if !(first > 0 && last > 0) {
		return 0, false
	}
	return halved(math.Log(last/first) / years), true
}

func halved(cagrLog float64) float64 { return min(max(cagrLog/2, -0.25), 0.25) }

// quantile is linear interpolation between order statistics of sorted.
func quantile(sorted []float64, q float64) float64 {
	h := q * float64(len(sorted)-1)
	lo := int(math.Floor(h))
	if lo+1 >= len(sorted) {
		return sorted[len(sorted)-1]
	}
	return sorted[lo] + (h-float64(lo))*(sorted[lo+1]-sorted[lo])
}

func day(t time.Time) string { return t.UTC().Format(time.DateOnly) }

func ptrF(x float64) *float64 { return &x }

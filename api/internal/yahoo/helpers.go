package yahoo

import (
	"math"
	"sort"
	"time"
)

// Pure helpers over a chart's bars (oldest first, as Chart returns them).
// A value that the bars cannot support is nil, never estimated.

// ATH is the highest high in a run of bars. It is the listing's lifetime high
// only when the bars start at its first trade (Chart range "max"); From says
// how far back they reach, and callers label it.
type ATH struct {
	High float64   `json:"high"`
	At   time.Time `json:"at"`   // the bar that made it
	From time.Time `json:"from"` // the first bar covered
	Bars int       `json:"bars"`
}

// AllTimeHigh is the highest H over bars (the earliest bar on a tie); false
// without bars.
func AllTimeHigh(bars []Bar) (ATH, bool) {
	if len(bars) == 0 {
		return ATH{}, false
	}
	a := ATH{High: bars[0].H, At: bars[0].T, From: bars[0].T, Bars: len(bars)}
	for _, b := range bars[1:] {
		if b.H > a.High {
			a.High, a.At = b.H, b.T
		}
	}
	return a, true
}

// DrawdownPct is how far price sits below high, in percent (≤ 0 below it).
func DrawdownPct(price, high float64) float64 {
	return (price/high - 1) * 100
}

// Returns are trailing total returns in percent (adjusted closes), nil when
// the bars do not reach back that far.
type Returns struct {
	Y1 *float64 `json:"y1"`
	Y3 *float64 `json:"y3"`
	Y5 *float64 `json:"y5"`
}

// TrailingReturns is ReturnPct over 1, 3 and 5 years.
func TrailingReturns(bars []Bar) Returns {
	return Returns{Y1: ReturnPct(bars, 1), Y3: ReturnPct(bars, 3), Y5: ReturnPct(bars, 5)}
}

// coverSlack is how late the first bar may start and still cover a window:
// a window can open on a weekend or holiday.
const coverSlack = 7 * 24 * time.Hour

// ReturnPct is the percent change from the last close at or before the last
// bar's time minus years to the last close. Closes are adjusted (Adj) when
// every bar has one, else C. nil when the bars start more than a week after
// that date.
func ReturnPct(bars []Bar, years int) *float64 {
	if len(bars) < 2 || years <= 0 {
		return nil
	}
	px := closeOf(bars)
	last := bars[len(bars)-1]
	start := last.T.AddDate(-years, 0, 0)
	i := sort.Search(len(bars), func(i int) bool { return bars[i].T.After(start) }) - 1
	if i < 0 {
		if bars[0].T.Sub(start) > coverSlack {
			return nil
		}
		i = 0
	}
	if i == len(bars)-1 {
		return nil
	}
	r := (px(last) - px(bars[i])) / px(bars[i]) * 100
	return &r
}

// closeOf is the adjusted close when every bar has one, else the close.
func closeOf(bars []Bar) func(Bar) float64 {
	for _, b := range bars {
		if b.Adj <= 0 {
			return func(b Bar) float64 { return b.C }
		}
	}
	return func(b Bar) float64 { return b.Adj }
}

// TradingDays annualises daily stock volatility (crypto's market package uses 365).
const TradingDays = 252

// VolatilityPct is the realised volatility of the last year of daily bars,
// annualised (sample standard deviation of log returns of adjusted closes ×
// √252), in percent. nil unless the bars cover the whole year (first bar
// within a week of its start) with at least 120 returns.
func VolatilityPct(bars []Bar) *float64 {
	if len(bars) < 2 {
		return nil
	}
	px := closeOf(bars)
	start := bars[len(bars)-1].T.AddDate(-1, 0, 0)
	i := sort.Search(len(bars), func(i int) bool { return !bars[i].T.Before(start) })
	if i == 0 && bars[0].T.Sub(start) > coverSlack {
		return nil
	}
	if i > 0 {
		i-- // the close before the window anchors its first return
	}
	r := make([]float64, 0, len(bars)-i)
	for j := i + 1; j < len(bars); j++ {
		r = append(r, math.Log(px(bars[j])/px(bars[j-1])))
	}
	if len(r) < 120 {
		return nil
	}
	mean := 0.0
	for _, x := range r {
		mean += x
	}
	mean /= float64(len(r))
	ss := 0.0
	for _, x := range r {
		ss += (x - mean) * (x - mean)
	}
	v := math.Sqrt(ss/float64(len(r)-1)) * math.Sqrt(TradingDays) * 100
	return &v
}

// WeeklyCloses is the last close (C) of each ISO week, weeks counted in loc
// (Meta.Location: an exchange's Monday can start on Sunday UTC). T is that
// bar's time; the newest week may still be in progress.
func WeeklyCloses(bars []Bar, loc *time.Location) []Point {
	if loc == nil {
		loc = time.UTC
	}
	var out []Point
	lastY, lastW := 0, 0
	for _, b := range bars {
		y, w := b.T.In(loc).ISOWeek()
		if n := len(out); n > 0 && y == lastY && w == lastW {
			out[n-1] = Point{T: b.T, V: b.C}
			continue
		}
		out = append(out, Point{T: b.T, V: b.C})
		lastY, lastW = y, w
	}
	return out
}

// Closes is each bar's C; Values is each point's V.
func Closes(bars []Bar) []float64 {
	out := make([]float64, len(bars))
	for i, b := range bars {
		out[i] = b.C
	}
	return out
}

func Values(points []Point) []float64 {
	out := make([]float64, len(points))
	for i, p := range points {
		out[i] = p.V
	}
	return out
}

// SMA is the mean of the last n values; nil with fewer than n.
func SMA(v []float64, n int) *float64 {
	if n <= 0 || len(v) < n {
		return nil
	}
	s := 0.0
	for _, x := range v[len(v)-n:] {
		s += x
	}
	s /= float64(n)
	return &s
}

// RSI is the latest Wilder RSI(n) over v (seeded with the simple mean of the
// first n changes, then smoothed over the rest, as market.RSI); nil with n
// or fewer values. Flat input is 50.
func RSI(v []float64, n int) *float64 {
	if n <= 0 || len(v) <= n {
		return nil
	}
	var gain, loss float64
	for i := 1; i <= n; i++ {
		d := v[i] - v[i-1]
		gain += max(d, 0)
		loss += max(-d, 0)
	}
	gain, loss = gain/float64(n), loss/float64(n)
	for i := n + 1; i < len(v); i++ {
		d := v[i] - v[i-1]
		gain = (gain*float64(n-1) + max(d, 0)) / float64(n)
		loss = (loss*float64(n-1) + max(-d, 0)) / float64(n)
	}
	var r float64
	switch {
	case gain == 0 && loss == 0:
		r = 50
	case loss == 0:
		r = 100
	default:
		r = 100 - 100/(1+gain/loss)
	}
	return &r
}

// ToUSD converts bars quoted in a currency to USD with its daily FX history
// (FXHistory): each bar's prices times the rate of the latest FX day that
// started at or before the bar. Bars older than the first FX day are dropped,
// never priced at a guessed rate; no FX points drop every bar. Volume is
// unchanged. USD bars need no conversion (FXHistory answers nil for USD).
func ToUSD(bars []Bar, fx []Point) []Bar {
	out := make([]Bar, 0, len(bars))
	j := -1
	for _, b := range bars {
		for j+1 < len(fx) && !fx[j+1].T.After(b.T) {
			j++
		}
		if j < 0 {
			continue
		}
		r := fx[j].V
		out = append(out, Bar{T: b.T, O: b.O * r, H: b.H * r, L: b.L * r, C: b.C * r, V: b.V, Adj: b.Adj * r})
	}
	return out
}

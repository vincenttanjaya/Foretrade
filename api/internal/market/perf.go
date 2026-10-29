package market

import "math"

// MinAnnualizeDays is the shortest window AnnualizedPct annualizes.
const MinAnnualizeDays = 90

// maxRatio caps Sortino when a curve never falls (it would otherwise be +Inf).
const maxRatio = 100

// BarSeconds is an interval's bar length in seconds; 0 for an unknown interval.
func BarSeconds(interval string) int64 {
	return map[string]int64{"15m": 900, "1h": 3600, "4h": 14400, "1d": 86400}[interval]
}

// BarsPerYear is the number of interval bars in a 365-day year (crypto
// trades every day); 0 for an unknown interval.
func BarsPerYear(interval string) float64 {
	sec := BarSeconds(interval)
	if sec == 0 {
		return 0
	}
	return 365 * 86400 / float64(sec)
}

// SessionBarsPerYear is how many bars a year a run of bars from startT to
// endT (their open times, Unix seconds) has: (bars − 1) × 365 days over the
// span. A quote-feed listing trades in sessions (TWSE: 5 hourly bars a
// weekday), so its ratios annualize with this, not BarsPerYear. 0 when the
// span is not positive.
func SessionBarsPerYear(bars int, startT, endT int64) float64 {
	if endT <= startT || bars < 2 {
		return 0
	}
	return float64(bars-1) * 365 * 86400 / float64(endT-startT)
}

// EquityStats are the risk numbers of an equity curve. Ratios are annualized
// with BarsPerYear (StatsPerYear: a given bars a year) and a zero risk-free
// rate; percentages are ×100.
type EquityStats struct {
	Sharpe         float64 // mean / sample stdev of per-bar returns × √P; 0 under 2 returns or a flat curve
	Sortino        float64 // mean / downside deviation × √P, clamped to ±100 (100 when it never falls but rises)
	VolAnnPct      float64 // sample stdev of per-bar returns × √P × 100
	MaxDrawdownPct float64 // largest peak-to-trough fall in percent of the peak, a positive magnitude
}

// Stats computes EquityStats for equity marked once per interval bar (oldest
// first). Returns are r_i = E_i/E_{i−1} − 1, skipping steps from E ≤ 0. The
// downside deviation is √(Σ min(r_i, 0)² / N) over all N returns, not the
// stdev of the negative ones.
func Stats(equity []float64, interval string) EquityStats {
	return StatsPerYear(equity, BarsPerYear(interval))
}

// StatsPerYear is Stats annualized with perYear bars a year.
func StatsPerYear(equity []float64, perYear float64) EquityStats {
	st := EquityStats{MaxDrawdownPct: maxDrawdown(equity)}
	r := make([]float64, 0, len(equity))
	for i := 1; i < len(equity); i++ {
		if equity[i-1] > 0 {
			r = append(r, equity[i]/equity[i-1]-1)
		}
	}
	if len(r) < 2 {
		return st
	}
	n, sqrtP := float64(len(r)), math.Sqrt(perYear)
	mean, ss, down := 0.0, 0.0, 0.0
	for _, x := range r {
		mean += x
		down += min(x, 0) * min(x, 0)
	}
	mean /= n
	for _, x := range r {
		ss += (x - mean) * (x - mean)
	}
	sd, dd := math.Sqrt(ss/(n-1)), math.Sqrt(down/n)
	st.VolAnnPct = sd * sqrtP * 100
	if sd >= 1e-12 {
		st.Sharpe = mean / sd * sqrtP
	}
	switch {
	case dd >= 1e-12:
		st.Sortino = min(max(mean/dd*sqrtP, -maxRatio), maxRatio)
	case mean > 0:
		st.Sortino = maxRatio
	}
	return st
}

// maxDrawdown is the largest peak-to-trough fall, in percent of the peak.
func maxDrawdown(v []float64) float64 {
	peak, dd := math.Inf(-1), 0.0
	for _, x := range v {
		peak = max(peak, x)
		if peak > 0 {
			dd = max(dd, (peak-x)/peak*100)
		}
	}
	return dd
}

// AnnualizedPct compounds the growth from first to last over days to a
// 365-day year, in percent. It is nil under MinAnnualizeDays (a short window
// would extrapolate noise into a huge number: +10% in 30 days reads +219%)
// and −100 when last ≤ 0.
func AnnualizedPct(first, last, days float64) *float64 {
	if days < MinAnnualizeDays || first <= 0 {
		return nil
	}
	a := -100.0
	if last > 0 {
		a = (math.Pow(last/first, 365/days) - 1) * 100
	}
	return &a
}

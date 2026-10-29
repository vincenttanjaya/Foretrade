package market

import "math"

// Pure return statistics over aligned daily series, shared by the portfolio
// review (paper) and anything else that compares assets. A NaN element is a
// day the series has no value (not listed yet): pair statistics use the days
// both series have.

// Returns are the simple returns between consecutive closes, len(closes)−1;
// NaN where either close is NaN or the earlier one is not positive.
func Returns(closes []float64) []float64 {
	if len(closes) == 0 {
		return nil
	}
	out := make([]float64, len(closes)-1)
	for i := range out {
		if closes[i] > 0 && !math.IsNaN(closes[i+1]) {
			out[i] = closes[i+1]/closes[i] - 1
		} else {
			out[i] = math.NaN()
		}
	}
	return out
}

// Correlation is Pearson's ρ of a and b over the days both have (the shorter
// length when they differ). It needs at least minN such days and both series
// to vary, else ok is false.
func Correlation(a, b []float64, minN int) (rho float64, ok bool) {
	x, y := shared(a, b)
	if len(x) < max(minN, 2) {
		return 0, false
	}
	sxy, sxx, syy := moments(x, y)
	if sxx < 1e-18 || syy < 1e-18 {
		return 0, false
	}
	return sxy / math.Sqrt(sxx*syy), true
}

// Beta is cov(x, bench) / var(bench) over the days both have: how much x
// moves per 1% of bench. It needs at least minN such days and a benchmark
// that varies, else ok is false; a flat x has beta 0.
func Beta(x, bench []float64, minN int) (beta float64, ok bool) {
	a, b := shared(x, bench)
	if len(a) < max(minN, 2) {
		return 0, false
	}
	sab, _, sbb := moments(a, b)
	if sbb < 1e-18 {
		return 0, false
	}
	return sab / sbb, true
}

// shared keeps the days where both series are finite, in order.
func shared(a, b []float64) (x, y []float64) {
	for i := range min(len(a), len(b)) {
		if !math.IsNaN(a[i]) && !math.IsInf(a[i], 0) && !math.IsNaN(b[i]) && !math.IsInf(b[i], 0) {
			x, y = append(x, a[i]), append(y, b[i])
		}
	}
	return x, y
}

// moments are the centred cross and squared sums of two equal-length series.
func moments(x, y []float64) (sxy, sxx, syy float64) {
	mx, my := 0.0, 0.0
	for i := range x {
		mx, my = mx+x[i], my+y[i]
	}
	mx, my = mx/float64(len(x)), my/float64(len(y))
	for i := range x {
		dx, dy := x[i]-mx, y[i]-my
		sxy, sxx, syy = sxy+dx*dy, sxx+dx*dx, syy+dy*dy
	}
	return sxy, sxx, syy
}

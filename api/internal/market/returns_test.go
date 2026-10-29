package market

import (
	"math"
	"testing"
)

func TestReturns(t *testing.T) {
	nan := math.NaN()
	got := Returns([]float64{nan, 100, 110, nan, 99, 0, 5})
	want := []float64{nan, 0.1, nan, nan, -1, nan} // 99 → 0 is −100%; from 0 there is no return
	if len(got) != len(want) {
		t.Fatalf("len %d, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if math.IsNaN(want[i]) != math.IsNaN(got[i]) || (!math.IsNaN(want[i]) && math.Abs(got[i]-want[i]) > 1e-12) {
			t.Errorf("return %d = %v, want %v", i, got[i], want[i])
		}
	}
	if Returns(nil) != nil || len(Returns([]float64{1})) != 0 {
		t.Error("no closes: no returns")
	}
}

func TestCorrelationAndBeta(t *testing.T) {
	nan := math.NaN()
	btc := []float64{0.01, -0.02, 0.03, 0.005, -0.015, 0.02, -0.01, 0.0, 0.025, -0.03}
	scaled := make([]float64, len(btc)) // 1.5 × BTC: ρ 1, β 1.5
	inverse := make([]float64, len(btc))
	flat := make([]float64, len(btc))
	for i, r := range btc {
		scaled[i], inverse[i], flat[i] = 1.5*r, -r, 0
	}
	check := func(name string, got, want float64, ok, wantOK bool) {
		t.Helper()
		if ok != wantOK || (ok && math.Abs(got-want) > 1e-9) {
			t.Errorf("%s = %v (ok %v), want %v (ok %v)", name, got, ok, want, wantOK)
		}
	}
	rho, ok := Correlation(btc, scaled, 5)
	check("ρ(btc, 1.5 btc)", rho, 1, ok, true)
	rho, ok = Correlation(btc, inverse, 5)
	check("ρ(btc, −btc)", rho, -1, ok, true)
	beta, ok := Beta(scaled, btc, 5)
	check("β(1.5 btc)", beta, 1.5, ok, true)
	beta, ok = Beta(inverse, btc, 5)
	check("β(−btc)", beta, -1, ok, true)

	// A flat series has no correlation and a beta of 0; a flat benchmark has no beta.
	_, ok = Correlation(btc, flat, 5)
	check("ρ(btc, flat)", 0, 0, ok, false)
	beta, ok = Beta(flat, btc, 5)
	check("β(flat)", beta, 0, ok, true)
	_, ok = Beta(btc, flat, 5)
	check("β on a flat benchmark", 0, 0, ok, false)

	// Only the days both have count, and under minN of them is no statistic.
	gappy := append([]float64{nan, nan, nan}, scaled[3:]...)
	rho, ok = Correlation(btc, gappy, 7)
	check("ρ with 7 shared days", rho, 1, ok, true)
	_, ok = Correlation(btc, gappy, 8)
	check("ρ with 7 shared days, 8 needed", 0, 0, ok, false)
	_, ok = Beta(btc[:1], scaled[:1], 1)
	check("β of one day", 0, 0, ok, false)

	// Not a perfect fit: the usual formulas on a small series (hand-computed).
	x := []float64{1, 2, 3, 4, 5}
	y := []float64{2, 4, 5, 4, 5}
	rho, _ = Correlation(x, y, 2)
	beta, _ = Beta(y, x, 2)
	if math.Abs(rho-0.7745966692) > 1e-9 || math.Abs(beta-0.6) > 1e-9 {
		t.Errorf("ρ %v β %v, want 0.7746 and 0.6", rho, beta)
	}
}

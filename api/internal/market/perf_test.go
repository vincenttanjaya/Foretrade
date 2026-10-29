package market

import (
	"math"
	"testing"
)

func TestBarsPerYear(t *testing.T) {
	for iv, want := range map[string]float64{"15m": 35040, "1h": 8760, "4h": 2190, "1d": 365, "5m": 0} {
		if got := BarsPerYear(iv); got != want {
			t.Errorf("BarsPerYear(%s) = %v, want %v", iv, got, want)
		}
	}
}

func TestStats(t *testing.T) {
	// Returns +10%, −10%, +10%, 0: mean 0.025, Σ(r−mean)² = 0.0275, downside Σmin(r,0)² = 0.01 over N = 4.
	st := Stats([]float64{100, 110, 99, 108.9, 108.9}, "1d")
	sd, p := math.Sqrt(0.0275/3), math.Sqrt(365)
	if !near(st.Sharpe, 0.025/sd*p) || !near(st.Sortino, 0.025/math.Sqrt(0.01/4)*p) ||
		!near(st.VolAnnPct, sd*p*100) || !near(st.MaxDrawdownPct, 10) {
		t.Fatalf("stats %+v", st)
	}

	for name, tc := range map[string]struct {
		equity   []float64
		interval string
		want     EquityStats
	}{
		"never falls: Sortino capped": {[]float64{100, 101, 102}, "1d", EquityStats{Sortino: 100}},
		"flat":                        {[]float64{100, 100, 100}, "1d", EquityStats{}},
		"one return":                  {[]float64{100, 120}, "1d", EquityStats{}},
		"empty":                       {nil, "1d", EquityStats{}},
		"wiped out: no return from 0": {[]float64{100, 0, 50}, "1d", EquityStats{MaxDrawdownPct: 100}},
	} {
		st := Stats(tc.equity, tc.interval)
		st.Sharpe, st.VolAnnPct = 0, 0 // checked above
		if st != tc.want {
			t.Errorf("%s: %+v, want %+v", name, st, tc.want)
		}
	}
	if st := Stats([]float64{100, 200, 199.99, 400}, "15m"); st.Sortino != 100 {
		t.Errorf("tiny downside must clamp to 100: %v", st.Sortino)
	}
	if st := Stats([]float64{100, 50, 50.001, 25}, "15m"); st.Sortino != -100 {
		t.Errorf("steady losses must clamp to −100: %v", st.Sortino)
	}
}

func TestAnnualizedPct(t *testing.T) {
	if got := AnnualizedPct(100, 110, 90); got == nil || !near(*got, (math.Pow(1.1, 365.0/90)-1)*100) {
		t.Fatalf("90 days: %v", got)
	}
	if got := AnnualizedPct(100, 0, 120); got == nil || *got != -100 {
		t.Fatalf("wiped out: %v", got)
	}
	if AnnualizedPct(100, 103, 89) != nil || AnnualizedPct(100, 110, 30) != nil || AnnualizedPct(0, 103, 120) != nil {
		t.Fatal("under 90 days or no starting equity must be null")
	}
}

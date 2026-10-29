package market

import (
	"math"
	"math/rand/v2"
	"testing"
	"time"
)

// series is one close per calendar day from 1997-01-01 under f(day index).
func series(n int, perYear float64, f func(i int) float64) DailySeries {
	s := DailySeries{Symbol: "TST", Source: "test", Currency: "USD", PerYear: perYear, Kind: "price"}
	t0 := time.Date(1997, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range n {
		s.Closes = append(s.Closes, DayClose{T: t0.AddDate(0, 0, i), C: f(i)})
	}
	return s
}

// gbm is a seeded geometric Brownian motion of n daily closes, σ and μ (log
// drift) annual on 365 days a year.
func gbm(n int, sigma, mu float64, seed uint64) DailySeries {
	r := rand.New(rand.NewPCG(seed, 1))
	x := math.Log(100.0)
	dt := 1.0 / 365
	return series(n, 365, func(i int) float64 {
		if i > 0 {
			x += mu*dt + sigma*math.Sqrt(dt)*r.NormFloat64()
		}
		return math.Exp(x)
	})
}

func TestComputeHistoryStats(t *testing.T) {
	// A constant 10% CAGR: exact CAGRs, no volatility, every 5-year window ×1.61.
	steady := series(29*365+1, 365, func(i int) float64 { return 100 * math.Pow(1.1, float64(i)/365) })
	h := ComputeHistoryStats(steady, 1825)
	for _, k := range []string{"y1", "y3", "y5", "y10", "all"} {
		if c := h.CAGRPct[k]; c == nil || math.Abs(*c-10) > 0.01 {
			t.Errorf("CAGR %s = %v, want 10", k, c)
		}
	}
	if v := h.VolAnnPct["all"]; v == nil || *v > 1e-6 {
		t.Errorf("steady vol %v", v)
	}
	w := h.Window
	if w == nil || w.Days != 1825 || w.Independent != 5 || w.N != 29*365+1-1825 || w.PUp != 1 || w.PDouble != 0 {
		t.Fatalf("windows %+v", w)
	}
	for _, q := range w.Q {
		if math.Abs(q-math.Pow(1.1, 5)) > 1e-9 {
			t.Errorf("window multiple %v, want %v", q, math.Pow(1.1, 5))
		}
	}
	if h.From != "1997-01-01" || h.Sessions != 29*365+1 || h.MaxDrawdown != nil || h.Last != steady.Closes[len(steady.Closes)-1].C ||
		h.HighDate != h.To || math.Abs(h.Years-29) > 1e-9 {
		t.Errorf("stats %+v", h)
	}

	// A seeded GBM: vol near the truth, the halved drift half the realized log CAGR.
	g := gbm(20*365+1, 0.35, 0.12, 7)
	gs := ComputeHistoryStats(g, 365)
	if v := gs.VolAnnPct["all"]; v == nil || math.Abs(*v-35) > 0.7 {
		t.Errorf("GBM vol %v, want 35 ± 0.7", v)
	}
	realized := math.Log(g.Closes[len(g.Closes)-1].C/g.Closes[0].C) / g.Years()
	if mu, ok := HalvedDrift(g); !ok || math.Abs(mu-realized/2) > 1e-12 || gs.DriftHalvedPct == nil || *gs.DriftHalvedPct != mu*100 {
		t.Errorf("halved drift %v %v, want %v", mu, ok, realized/2)
	}
	if mu, ok := HalvedDrift(series(3*365, 365, func(i int) float64 { return 100 * math.Exp(float64(i)/365) })); !ok || mu != 0.25 {
		t.Errorf("drift cap: %v %v", mu, ok)
	}
	if _, ok := HalvedDrift(series(2*365-1, 365, func(i int) float64 { return 100 })); ok {
		t.Error("under 2 years there is no drift")
	}
	// y1 and y5 vol: covered windows only.
	if gs.VolAnnPct["y1"] == nil || gs.VolAnnPct["y5"] == nil {
		t.Errorf("windowed vol %v %v", gs.VolAnnPct["y1"], gs.VolAnnPct["y5"])
	}
	short := ComputeHistoryStats(gbm(400, 0.3, 0, 1), 365)
	if short.VolAnnPct["y5"] != nil || short.CAGRPct["y3"] != nil || short.Window != nil || short.VolAnnPct["y1"] == nil {
		t.Errorf("400 days: y5 vol %v, y3 CAGR %v, window %v", short.VolAnnPct["y5"], short.CAGRPct["y3"], short.Window)
	}

	// A window ends on the last close on or before T + h: weekends are missing.
	wk := DailySeries{PerYear: 252, Kind: "price"}
	for d := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC); d.Year() < 2023; d = d.AddDate(0, 0, 1) {
		if d.Weekday() != time.Saturday && d.Weekday() != time.Sunday {
			wk.Closes = append(wk.Closes, DayClose{T: d, C: float64(d.YearDay())})
		}
	}
	mult, starts, ends := windowMultiples(wk, 3)
	for i := range mult {
		target := starts[i].AddDate(0, 0, 3)
		if ends[i].After(target) || target.Sub(ends[i]) > 2*24*time.Hour {
			t.Fatalf("window %d: %s → %s", i, starts[i], ends[i])
		}
	}
	if starts[1].Weekday() != time.Thursday || ends[1].Weekday() != time.Friday { // Thu + 3 is Sunday: Friday's close
		t.Errorf("Thursday window ends %s", ends[1].Weekday())
	}

	// A V: −50% then back above the peak; then an unrecovered −67.5%.
	v := series(300, 365, func(i int) float64 {
		switch {
		case i < 100:
			return 100 - 0.5*float64(i)
		case i < 200:
			return 50 + 0.6*float64(i-100)
		}
		return 110 - 0.75*float64(i-200)
	})
	vs := ComputeHistoryStats(v, 0)
	if d := vs.MaxDrawdown; d == nil || math.Abs(d.Pct-(35.75/110-1)*100) > 1e-9 || d.Peak != day(v.Closes[200].T) || d.Trough != day(v.Closes[299].T) || d.Recovered != nil {
		t.Errorf("max drawdown %+v", d)
	}
	if len(vs.Drawdowns) != 1 || math.Abs(vs.Drawdowns[0].Pct+50) > 1e-9 || vs.Drawdowns[0].Trough != day(v.Closes[100].T) || vs.Drawdowns[0].Peak != "1997-01-01" ||
		vs.Drawdowns[0].Recovered == nil || *vs.Drawdowns[0].Recovered != day(v.Closes[184].T) {
		t.Errorf("drawdowns %+v", vs.Drawdowns)
	}

	// Under horizon + 1 year of history: no windows.
	if w := ComputeHistoryStats(steady, 29*365).Window; w != nil {
		t.Errorf("window %+v", w)
	}
}

// Closes are C (split-adjusted), never Adj: questions resolve on the quoted price.
func TestHistoryUsesCloseNotAdj(t *testing.T) {
	c, f := feedClient(t)
	ch := tsmChart(30, 0.3, 3)
	for i := range ch.Bars {
		ch.Bars[i].Adj = ch.Bars[i].C * 0.5
	}
	f.Set(ch)
	s, err := c.DailyHistory(t.Context(), "TSM")
	if err != nil || s.Closes[len(s.Closes)-1].C != ch.Bars[len(ch.Bars)-1].C {
		t.Fatalf("%v %v", s.Closes[len(s.Closes)-1], err)
	}
}

func TestWindowShares(t *testing.T) {
	g := gbm(10*365, 0.3, 0.05, 3)
	inf := math.Inf(1)
	shares, ind := WindowShares(g, 365, 485, [][2]float64{{-inf, 300}, {300, 485}, {485, 700}, {700, inf}})
	sum := 0.0
	for _, s := range shares {
		sum += s
	}
	if math.Abs(sum-1) > 1e-9 || ind != 9 || shares[2] == 0 {
		t.Errorf("shares %v (sum %v), independent %d", shares, sum, ind)
	}
	// A level series (a yield) moves by differences: +1 point a year from 4.0.
	lv := series(4*365, 252, func(i int) float64 { return 2 + float64(i)/365 })
	lv.Kind = "level"
	shares, _ = WindowShares(lv, 365, 4.0, [][2]float64{{-inf, 4.5}, {4.5, 5.5}, {5.5, inf}})
	if shares[1] != 1 {
		t.Errorf("level shares %v", shares)
	}
}

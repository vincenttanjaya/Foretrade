package yahoo

import (
	"math"
	"testing"
	"time"
)

var day0 = time.Date(2020, 1, 1, 1, 0, 0, 0, time.UTC)

// daily is one bar a calendar day from day0 with closes c (H = C+1, L = C−1).
func daily(c ...float64) []Bar {
	out := make([]Bar, len(c))
	for i, x := range c {
		out[i] = Bar{T: day0.AddDate(0, 0, i), O: x, H: x + 1, L: x - 1, C: x, V: 10}
	}
	return out
}

func near(p *float64, want float64) bool { return p != nil && math.Abs(*p-want) < 1e-9 }

func TestAllTimeHighAndDrawdown(t *testing.T) {
	if _, ok := AllTimeHigh(nil); ok {
		t.Fatal("no bars, no high")
	}
	bars := daily(10, 30, 20, 30, 5)
	a, ok := AllTimeHigh(bars)
	if !ok || a.High != 31 || !a.At.Equal(bars[1].T) || !a.From.Equal(day0) || a.Bars != 5 {
		t.Fatalf("%+v", a)
	}
	if d := DrawdownPct(15.5, 31); d != -50 {
		t.Fatalf("drawdown %v", d)
	}
}

func TestReturnPct(t *testing.T) {
	// Six years of daily closes rising 100 a year (a calendar year of days).
	start := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(6, 0, 0)
	var bars []Bar
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		bars = append(bars, Bar{T: d, O: 1, H: 1, L: 1, C: 100 + 100*d.Sub(start).Hours()/24/365.25})
	}
	r := TrailingReturns(bars)
	last := bars[len(bars)-1].C
	at := func(years int) float64 {
		for i := len(bars) - 1; i >= 0; i-- {
			if !bars[i].T.After(end.AddDate(-years, 0, 0)) {
				return bars[i].C
			}
		}
		return math.NaN()
	}
	if !near(r.Y1, (last/at(1)-1)*100) || !near(r.Y3, (last/at(3)-1)*100) || !near(r.Y5, (last/at(5)-1)*100) {
		t.Fatalf("%v %v %v", *r.Y1, *r.Y3, *r.Y5)
	}
	if ReturnPct(bars, 7) != nil {
		t.Fatal("seven years from six of bars")
	}
	// A first bar a few days into the window still covers it.
	if ReturnPct(bars[3:], 6) == nil || ReturnPct(bars[10:], 6) != nil {
		t.Fatal("cover slack")
	}
	// Adjusted closes when every bar has one.
	adj := daily(100, 110)
	adj[0].T, adj[1].T = end.AddDate(-1, 0, 0), end
	adj[0].Adj, adj[1].Adj = 50, 60
	if !near(ReturnPct(adj, 1), 20) {
		t.Fatalf("adjusted: %v", *ReturnPct(adj, 1))
	}
	adj[0].Adj = 0
	if !near(ReturnPct(adj, 1), 10) {
		t.Fatal("a bar without adjclose falls back to closes")
	}
}

func TestVolatilityPct(t *testing.T) {
	// Closes alternating ±1% log moves for 400 days.
	var bars []Bar
	x := 100.0
	for i := range 400 {
		if i%2 == 1 {
			x *= math.Exp(0.01)
		} else if i > 0 {
			x *= math.Exp(-0.01)
		}
		bars = append(bars, Bar{T: day0.AddDate(0, 0, i), O: x, H: x, L: x, C: x})
	}
	v := VolatilityPct(bars)
	// 365 returns of ±0.01 alternate: mean ≈ 0, sample sd ≈ 0.01.
	if v == nil || math.Abs(*v-0.01*math.Sqrt(252)*100) > 0.05 {
		t.Fatalf("vol %v", v)
	}
	flat := daily(make([]float64, 400)...)
	for i := range flat {
		flat[i].C = 50
	}
	if v := VolatilityPct(flat); !near(v, 0) {
		t.Fatalf("flat %v", v)
	}
	if VolatilityPct(bars[300:]) != nil {
		t.Fatal("100 days do not make a year")
	}
}

func TestWeeklyCloses(t *testing.T) {
	tokyo, _ := time.LoadLocation("Asia/Tokyo")
	// Fri 2026-10-02, then Sunday 23:00 UTC = Monday 08:00 in Tokyo, then Tue, then next Mon.
	bars := []Bar{
		{T: time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC), C: 1},
		{T: time.Date(2026, 10, 4, 23, 0, 0, 0, time.UTC), C: 2},
		{T: time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC), C: 3},
		{T: time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC), C: 4},
	}
	got := WeeklyCloses(bars, tokyo)
	if len(got) != 3 || got[0].V != 1 || got[1].V != 3 || !got[1].T.Equal(bars[2].T) || got[2].V != 4 {
		t.Fatalf("tokyo %+v", got)
	}
	if utc := WeeklyCloses(bars, nil); len(utc) != 3 || utc[0].V != 2 { // Sunday UTC ends week 40
		t.Fatalf("utc %+v", utc)
	}
}

func TestSMAAndRSI(t *testing.T) {
	v := []float64{1, 2, 3, 4, 5}
	if !near(SMA(v, 2), 4.5) || !near(SMA(v, 5), 3) || SMA(v, 6) != nil || SMA(v, 0) != nil {
		t.Fatal("sma")
	}
	if RSI(v, 5) != nil || !near(RSI(v, 4), 100) || !near(RSI([]float64{3, 3, 3}, 2), 50) || !near(RSI([]float64{5, 4, 3}, 2), 0) {
		t.Fatal("rsi edges")
	}
	// Seed: changes +1, −1 → gain 0.5, loss 0.5; then +2: gain (0.5+2)/2, loss 0.25.
	if r := RSI([]float64{10, 11, 10, 12}, 2); !near(r, 100-100/(1+1.25/0.25)) {
		t.Fatalf("wilder %v", *r)
	}
	if !near(SMA(Values(WeeklyCloses(daily(1, 2), nil)), 1), 2) || Closes(daily(7))[0] != 7 {
		t.Fatal("closes")
	}
}

func TestToUSD(t *testing.T) {
	bars := daily(10, 20, 30, 40)
	bars[3].Adj = 39
	fx := []Point{{T: bars[1].T.Add(-time.Hour), V: 0.5}, {T: bars[3].T, V: 0.25}}
	got := ToUSD(bars, fx)
	// The first bar predates every rate: dropped. The third uses the rate before it.
	if len(got) != 3 || got[0].C != 10 || got[0].H != 10.5 || got[1].C != 15 || got[2].C != 10 || got[2].Adj != 9.75 || got[2].V != 10 {
		t.Fatalf("%+v", got)
	}
	if len(ToUSD(bars, nil)) != 0 {
		t.Fatal("no rates, no bars")
	}
	if bars[1].C != 20 {
		t.Fatal("input changed")
	}
}

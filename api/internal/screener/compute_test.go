package screener

import (
	"math"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo"
)

// lastWeek is the newest weekly bar weekly builds: Monday 2026-09-28 00:00
// Tokyo, which is Sunday 15:00 UTC.
var lastWeek = time.Date(2026, 9, 27, 15, 0, 0, 0, time.UTC)

// weekly is a Tokyo listing's chart of one bar per week ending at lastWeek
// (O = C, H = C×1.02, L = C×0.98), priced at the last close on that week's
// Friday close (06:30 UTC), as Yahoo's meta is.
func weekly(symbol string, closes ...float64) yahoo.Chart {
	bars := make([]yahoo.Bar, len(closes))
	for i, c := range closes {
		bars[i] = yahoo.Bar{T: lastWeek.AddDate(0, 0, -7*(len(closes)-1-i)), O: c, H: c * 1.02, L: c * 0.98, C: c, V: 1000}
	}
	m := yahoo.Meta{Symbol: symbol, Currency: "JPY", ExchangeName: "JPX", FullExchangeName: "Tokyo", InstrumentType: "EQUITY",
		Timezone: "Asia/Tokyo", LongName: "Test " + symbol}
	if n := len(bars); n > 0 {
		p, at := bars[n-1].C, lastWeek.Add(4*24*time.Hour+15*time.Hour+30*time.Minute)
		m.Price, m.PriceTime = &p, &at
	}
	return yahoo.Chart{Meta: m, Range: "max", Interval: "1wk", Bars: bars}
}

func series(n int, f func(i int) float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = f(i)
	}
	return out
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// 210 weeks: 100 for 200 weeks, then ten weekly falls of 2. The average is
// of the last 200 closes; last week's distance uses the average ending then.
func TestComputeKnownValues(t *testing.T) {
	closes := series(210, func(i int) float64 { return min(100, 100-2*float64(i-199)) })
	m, ok := compute(weekly("1332.T", closes...))
	if !ok || m.Weeks != 210 || m.Price != 80 || m.Currency != "JPY" || m.Name != "Test 1332.T" || !m.FirstWeek.Equal(lastWeek.AddDate(0, 0, -7*209)) {
		t.Fatalf("%+v", m)
	}
	sum := 0.0
	for _, c := range closes[10:] {
		sum += c
	}
	sma := sum / 200
	prevSum := 0.0
	for _, c := range closes[9:209] {
		prevSum += c
	}
	if !near(*m.SMA, sma) || !near(*m.Pct, (80/sma-1)*100) || !near(*m.PrevPct, (82/(prevSum/200)-1)*100) {
		t.Fatalf("sma %v pct %v prev %v", *m.SMA, *m.Pct, *m.PrevPct)
	}
	if d := direction(m.Pct, m.PrevPct); d == nil || *d != "deepening" {
		t.Fatalf("direction %v", d)
	}
	if want := yahoo.RSI(closes, 14); m.RSI == nil || !near(*m.RSI, *want) || *m.RSI >= 30 {
		t.Fatalf("rsi %v want %v", m.RSI, *want)
	}
	// The high is the first week's: 100 × 1.02, the earliest bar on a tie.
	if m.ATH == nil || !near(*m.ATH, 102) || !m.ATHAt.Equal(lastWeek.AddDate(0, 0, -7*209)) || !near(*m.Drawdown, (80/102.0-1)*100) {
		t.Fatalf("ath %v at %v drawdown %v", m.ATH, m.ATHAt, m.Drawdown)
	}
}

// Yahoo's live bar inside the newest week replaces that week's close, so the
// weeks are not double counted and the price is the live one.
func TestComputeLiveBarInTheCurrentWeek(t *testing.T) {
	ch := weekly("1605.T", series(201, func(i int) float64 { return 100 + float64(i%2) })...)
	live := lastWeek.Add(2*24*time.Hour + 15*time.Hour) // Wednesday 15:00 in Tokyo, the same week
	ch.Bars = append(ch.Bars, yahoo.Bar{T: live, O: 120, H: 130, L: 119, C: 125})
	p := 125.0
	ch.Meta.Price, ch.Meta.PriceTime = &p, &live
	m, _ := compute(ch)
	if m.Weeks != 201 || m.Price != 125 || !m.PriceAt.Equal(live) || *m.ATH != 130 || !m.ATHAt.Equal(live) || *m.Drawdown >= 0 {
		t.Fatalf("%+v", m)
	}
	if d := direction(m.Pct, m.PrevPct); d == nil || *d != "recovering" {
		t.Fatalf("direction %v (pct %v prev %v)", d, *m.Pct, *m.PrevPct)
	}
}

// Under 200 weeks the average and its direction are unsupported (nil, never
// estimated); RSI and the high still are. No bars computes nothing.
func TestComputeShortHistory(t *testing.T) {
	m, ok := compute(weekly("285A.T", series(150, func(i int) float64 { return 50 + float64(i) })...))
	if !ok || m.Weeks != 150 || m.SMA != nil || m.Pct != nil || m.PrevPct != nil || m.RSI == nil || *m.RSI != 100 || !near(*m.ATH, 199*1.02) {
		t.Fatalf("%+v", m)
	}
	m, _ = compute(weekly("X.T", series(200, func(int) float64 { return 10 })...))
	if m.SMA == nil || *m.Pct != 0 || m.PrevPct != nil || direction(m.Pct, m.PrevPct) != nil {
		t.Fatalf("exactly 200 weeks: %+v", m)
	}
	if _, ok := compute(weekly("Y.T")); ok {
		t.Fatal("computed without bars")
	}
}

// A meta price older than the last bar is ignored; one above every high
// becomes the high (drawdown 0).
func TestComputePriceChoice(t *testing.T) {
	ch := weekly("Z.T", 10, 11, 12)
	old, p := lastWeek.Add(-time.Hour), 99.0
	ch.Meta.Price, ch.Meta.PriceTime = &p, &old
	if m, _ := compute(ch); m.Price != 12 || !m.PriceAt.Equal(lastWeek) {
		t.Fatalf("stale meta price used: %+v", m)
	}
	newer := lastWeek.Add(time.Hour)
	ch.Meta.PriceTime = &newer
	if m, _ := compute(ch); m.Price != 99 || *m.ATH != 99 || !m.ATHAt.Equal(newer) || *m.Drawdown != 0 {
		t.Fatalf("%+v", m)
	}
}

// A high over 100× the price (reverse splits scale old prices up) is
// withheld, never shown as a −99.9% drawdown; up to 100× it stands.
func TestComputeWithholdsImplausibleHigh(t *testing.T) {
	m, ok := compute(weekly("W.T", 5000, 40))
	if !ok || m.ATH != nil || m.ATHAt != nil || m.Drawdown != nil || m.Price != 40 || m.RSI != nil {
		t.Fatalf("%+v", m)
	}
	if m, _ := compute(weekly("V.T", 3900, 40)); m.ATH == nil || !near(*m.ATH, 3978) || !near(*m.Drawdown, (40/3978.0-1)*100) {
		t.Fatalf("under 100×: %+v", m)
	}
}

func TestDirection(t *testing.T) {
	a, b := -10.0, -12.0
	for _, c := range []struct {
		now, prev *float64
		want      string
	}{{&a, &b, "recovering"}, {&b, &a, "deepening"}, {&a, &a, ""}, {nil, &a, ""}, {&a, nil, ""}} {
		got := direction(c.now, c.prev)
		if (got == nil) != (c.want == "") || got != nil && *got != c.want {
			t.Fatalf("%v vs %v: %v, want %q", c.now, c.prev, got, c.want)
		}
	}
}

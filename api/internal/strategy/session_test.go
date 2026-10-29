package strategy

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
)

// sessionRuns is one set of 2,000 hourly returns as 24/7 crypto bars and as
// TWSE session bars (5 a weekday, 01:00–05:00 UTC).
func sessionRuns(t *testing.T) (crypto, twse []market.Candle) {
	t.Helper()
	r := rand.New(rand.NewPCG(1, 2))
	const bars = 2000
	rets := make([]float64, bars)
	for i := range rets {
		rets[i] = 0.00012 + 0.0067*r.NormFloat64()
	}
	build := func(times []int64) []market.Candle {
		out := make([]market.Candle, bars)
		p := 100.0
		for i := range out {
			o := p
			p *= math.Exp(rets[i])
			out[i] = market.Candle{T: times[i], O: o, H: max(o, p) * 1.001, L: min(o, p) * 0.999, C: p, V: 1000}
		}
		return out
	}
	start := time.Date(2025, 1, 6, 0, 0, 0, 0, time.UTC) // a Monday
	var hourly, session []int64
	for i := range bars {
		hourly = append(hourly, start.Add(time.Duration(i)*time.Hour).Unix())
	}
	for d := start; len(session) < bars; d = d.AddDate(0, 0, 1) {
		if wd := d.Weekday(); wd == time.Saturday || wd == time.Sunday {
			continue
		}
		for h := 1; h <= 5 && len(session) < bars; h++ {
			session = append(session, d.Add(time.Duration(h)*time.Hour).Unix())
		}
	}
	return build(hourly), build(session)
}

var alwaysLong = Spec{Name: "Always long", Interval: "1h", Bars: 1999, PositionPct: 100,
	LongEntry: all(when(ind("price", 0), ">", Indicator{Kind: "const", Value: num(0)}))}

// A quote-feed listing's bars exist only in its sessions, so its ratios and
// window are measured over its own span (gap round G15): the TWSE-shaped run
// of the same returns reads Sharpe ≈2.11 and vol ≈23.9% over 557 days, where
// 24/7 bars read 5.45 and 61.8% over 83 days. The crypto run is unchanged.
func TestQuoteFeedAnnualization(t *testing.T) {
	crypto, twse := sessionRuns(t)
	feed := alwaysLong
	feed.Symbol = "MEDIATEK" // TWSE 2454
	res, err := Backtest(feed, twse)
	if err != nil {
		t.Fatal(err)
	}
	m := res.Metrics
	days := spanDays(m.StartT, m.EndT)
	if math.Abs(m.Sharpe-2.11) > 0.02 || math.Abs(*m.VolatilityAnnPct-23.9) > 0.1 || math.Round(days) != 557 || m.AnnualizedReturnPct == nil ||
		m.BarsPerYear == nil || math.Abs(*m.BarsPerYear-1310) > 5 {
		t.Fatalf("TWSE run: Sharpe %v vol %v days %.1f annualized %v perYear %v", m.Sharpe, *m.VolatilityAnnPct, days, m.AnnualizedReturnPct, m.BarsPerYear)
	}
	if want := (math.Pow(1+m.TotalReturnPct/100, 365/days) - 1) * 100; math.Abs(*m.AnnualizedReturnPct-want) > 0.01 || m.Calmar == nil {
		t.Fatalf("annualized %v, want %v", *m.AnnualizedReturnPct, want)
	}

	// 24/7: today's arithmetic exactly (the review measured Sharpe 5.45, vol 61.8%, annualized withheld at 83 days).
	btc := alwaysLong
	btc.Symbol = "BTC"
	res, err = Backtest(btc, crypto)
	if err != nil {
		t.Fatal(err)
	}
	c := res.Metrics
	if c.Sharpe != 5.45 || math.Round(*c.VolatilityAnnPct*10) != 618 || c.AnnualizedReturnPct != nil || c.BarsPerYear != nil {
		t.Fatalf("crypto run: %+v vol %v", c, *c.VolatilityAnnPct)
	}
	if b, _ := json.Marshal(c); containsKey(b, "barsPerYear") {
		t.Fatalf("a 24/7 run carries no barsPerYear: %s", b)
	}
}

func containsKey(b []byte, k string) bool {
	var m map[string]any
	json.Unmarshal(b, &m)
	_, ok := m[k]
	return ok
}

// A quote-feed row saved with 24/7-era metrics reads back re-annualized; a
// crypto row and an already-session row read back byte-identical.
func TestSessionMetricsOnRead(t *testing.T) {
	_, twse := sessionRuns(t)
	feed := alwaysLong
	feed.Symbol = "MEDIATEK"
	now, _ := Backtest(feed, twse)
	old := alwaysLong
	old.Symbol = "BTC" // the same bars measured the 24/7 way, as rows saved before G15 were
	legacy, _ := Backtest(old, twse)
	m := legacy.Metrics
	if m.Sharpe != 5.45 || m.AnnualizedReturnPct != nil {
		t.Fatalf("legacy metrics %+v", m)
	}
	sessionMetrics(&m, "1h", "MEDIATEK")
	if math.Abs(m.Sharpe-now.Metrics.Sharpe) > 0.011 || math.Abs(*m.VolatilityAnnPct-*now.Metrics.VolatilityAnnPct) > 0.011 ||
		m.AnnualizedReturnPct == nil || math.Abs(*m.AnnualizedReturnPct-*now.Metrics.AnnualizedReturnPct) > 0.011 || m.Calmar == nil || m.BarsPerYear == nil {
		t.Fatalf("re-annualized %+v, fresh %+v", m, now.Metrics)
	}
	if math.Abs(*m.Sortino-*now.Metrics.Sortino) > 0.02 {
		t.Fatalf("sortino %v vs %v", *m.Sortino, *now.Metrics.Sortino)
	}
	clamped := legacy.Metrics
	clamped.Sortino = num(100)
	sessionMetrics(&clamped, "1h", "MEDIATEK")
	if *clamped.Sortino != 100 {
		t.Fatalf("the clamp stays: %v", *clamped.Sortino)
	}
	for _, c := range []struct {
		m      Metrics
		symbol string
	}{{legacy.Metrics, "BTC"}, {now.Metrics, "MEDIATEK"}} {
		before, _ := json.Marshal(c.m)
		sessionMetrics(&c.m, "1h", c.symbol)
		if after, _ := json.Marshal(c.m); string(after) != string(before) {
			t.Errorf("%s changed on read:\n%s\n%s", c.symbol, before, after)
		}
	}
}

// The community board orders a legacy quote-feed row by the Sharpe it reads
// back with: stored 5.45 (true ≈2.11) comes after a crypto 3.0.
func TestCommunityOrdersSessionSharpe(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	ctx := context.Background()
	_, twse := sessionRuns(t)
	feed := alwaysLong
	feed.Symbol = "BTC"
	legacy, _ := Backtest(feed, twse) // 24/7-era metrics of a TWSE run
	saved(t, e, e.User, "crypto", 20, 3.0, 400)
	id := saved(t, e, e.User, "taiwan", 20, legacy.Metrics.Sharpe, 400)
	m, _ := json.Marshal(legacy.Metrics)
	if _, err := e.DB.Exec(ctx, `update strategies set metrics = $2::jsonb || '{"trades":20}', spec = spec || '{"symbol":"MEDIATEK","interval":"1h"}'
		where id = $1`, id, string(m)); err != nil {
		t.Fatal(err)
	}
	e.DB.Exec(ctx, "update strategies set published_at = now()")
	l := communityOf(t, e, e.Cookie)
	if got := names(l); fmt.Sprint(got) != "[crypto taiwan]" || math.Abs(l[1].Metrics.Sharpe-2.11) > 0.02 {
		t.Fatalf("order %v: %+v", got, l)
	}
}

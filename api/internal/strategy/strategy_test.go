package strategy

import (
	"encoding/json"
	"math"
	"regexp"
	"strings"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
)

// Fills pay 2 bps of slippage against the position: buys at px·up, sells at px·dn.
const up, dn = 1 + paper.Slippage, 1 - paper.Slippage

// bar is open, high, low, close; candles are an hour apart.
type bar [4]float64

func series(bars ...bar) []market.Candle {
	out := make([]market.Candle, len(bars))
	for i, b := range bars {
		out[i] = market.Candle{T: int64(i) * 3600, O: b[0], H: b[1], L: b[2], C: b[3], V: 1 + float64(i%5)}
	}
	return out
}

func flat(px float64) bar { return bar{px, px, px, px} }

func k(v float64) Indicator { return Indicator{Kind: "const", Value: &v} }

// crossSpec goes long when the close crosses above level (and short below,
// when short is set). No fee unless the test sets one.
func crossSpec(level float64, short bool) Spec {
	s := Spec{Name: "t", Symbol: "BTC", Interval: "1h", Bars: 1000, PositionPct: 100,
		LongEntry: all(when(price, "crosses_above", k(level)))}
	if short {
		s.ShortEntry = all(when(price, "crosses_below", k(level)))
	}
	return s
}

func near(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 0.011 {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

func run(t *testing.T, s Spec, c []market.Candle) Result {
	t.Helper()
	if err := s.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	res, err := Backtest(s, c)
	if err != nil {
		t.Fatalf("backtest: %v", err)
	}
	return res
}

func TestCrossoverFillsNextOpen(t *testing.T) {
	c := series(flat(9), flat(9), bar{9, 11, 9, 11}, bar{11.5, 12, 11.5, 12}, bar{12, 13, 12, 13})
	res := run(t, crossSpec(10, false), c)
	if len(res.Trades) != 1 {
		t.Fatalf("trades %+v", res.Trades)
	}
	tr := res.Trades[0]
	if tr.Side != "long" || tr.EntryT != c[3].T || tr.Entry != 11.5*up || tr.Exit != 13*dn || tr.Reason != "end" {
		t.Fatalf("trade %+v: want long at bar 3 open 11.5, closed at the end at 13, both slipped", tr)
	}
	near(t, "pnlPct", tr.PnlPct, (13*dn/(11.5*up)-1)*100)
	near(t, "equity at signal bar", res.Equity[2].V, 100) // signal on the close, not filled yet
	near(t, "equity at fill bar", res.Equity[3].V, 100*12/(11.5*up))
	near(t, "total", res.Metrics.TotalReturnPct, (13*dn/(11.5*up)-1)*100)
	near(t, "buy & hold from the first fill-able open", res.Metrics.BuyHoldReturnPct, (13.0/9-1)*100)
	near(t, "exposure", res.Metrics.ExposurePct, 40)
	if res.Metrics.Trades != 1 || *res.Metrics.WinRatePct != 100 || res.Metrics.ProfitFactor != maxProfit {
		t.Fatalf("metrics %+v", res.Metrics)
	}
}

func TestStopBeatsTargetInSameBar(t *testing.T) {
	s := crossSpec(10, false)
	s.StopLossPct, s.TakeProfitPct = 5, 3
	// Bar 2 opens at 12 and touches both 11.4 (stop) and 12.36 (target): stop wins.
	c := series(flat(9), bar{9, 11, 9, 11}, bar{12, 12.5, 11.2, 11.5}, flat(11.5))
	res := run(t, s, c)
	if len(res.Trades) != 1 || res.Trades[0].Reason != "stop_loss" || res.Trades[0].ExitT != c[2].T {
		t.Fatalf("trades %+v", res.Trades)
	}
	// Entry 12·up; the 5% stop sits at 12·up·0.95 and the exit slips below it.
	near(t, "exit", res.Trades[0].Exit, 12*up*0.95*dn)
	near(t, "pnlPct", res.Trades[0].PnlPct, (0.95*dn-1)*100)
	near(t, "total", res.Metrics.TotalReturnPct, (0.95*dn-1)*100)
	near(t, "max drawdown", res.Metrics.MaxDrawdownPct, (1-0.95*dn)*100)
	near(t, "exposure counts the bar stopped out on", res.Metrics.ExposurePct, 25)
}

func TestStopFillsAtOpenOnGap(t *testing.T) {
	s := crossSpec(10, false)
	s.StopLossPct = 5
	c := series(flat(9), bar{9, 11, 9, 11}, bar{12, 12.2, 11.8, 12}, bar{10.5, 10.6, 10.4, 10.5}, flat(10.5))
	res := run(t, s, c)
	if len(res.Trades) != 1 || res.Trades[0].Reason != "stop_loss" || res.Trades[0].Exit != 10.5*dn {
		t.Fatalf("gap below the 11.4 stop must fill at the 10.5 open (slipped): %+v", res.Trades)
	}
}

func TestTakeProfit(t *testing.T) {
	s := crossSpec(10, false)
	s.TakeProfitPct = 3
	c := series(flat(9), bar{9, 11, 9, 11}, bar{12, 12.5, 11.9, 12.4}, flat(12.4))
	res := run(t, s, c)
	if len(res.Trades) != 1 || res.Trades[0].Reason != "take_profit" {
		t.Fatalf("trades %+v", res.Trades)
	}
	near(t, "exit", res.Trades[0].Exit, 12*up*1.03*dn)
	near(t, "pnlPct", res.Trades[0].PnlPct, (1.03*dn-1)*100)
}

// The trail follows the best high of the bars after the entry bar up to the
// previous one: a bar's own new high never raises the level it is checked against.
func TestTrailingStop(t *testing.T) {
	s := crossSpec(10, false)
	s.TrailingStopPct = 5
	if err := (&Spec{TrailingStopPct: 0.2}).Validate(); err == nil {
		t.Fatal("trail 0.2 accepted")
	}
	c := series(flat(9), bar{9, 11, 9, 11},
		bar{12, 12.5, 11.9, 12.4},   // entry at the open; its own high never counts
		bar{12.4, 14, 12.3, 13.8},   // nothing to trail yet; best high 14 from here on
		bar{13.8, 16, 13.4, 15.5},   // trail 13.3 holds; with look-ahead (16 → 15.2) it would have stopped here
		bar{15.5, 15.6, 14.0, 14.2}, // trail 16 × 0.95 = 15.2 is hit
		flat(14.2))
	res := run(t, s, c)
	if len(res.Trades) != 1 || res.Trades[0].Reason != "trailing_stop" || res.Trades[0].ExitT != c[5].T {
		t.Fatalf("trades %+v", res.Trades)
	}
	near(t, "exit at the trailed level", res.Trades[0].Exit, 16*0.95*dn)

	// After a dip the tighter level binds: the 5% fixed stop (11.40) over a 10% trail (11.34), a 4% trail (12.10) over the stop.
	dip := series(flat(9), bar{9, 11, 9, 11}, bar{12, 12.5, 11.9, 12.2}, bar{12.2, 12.6, 12.0, 12.1}, bar{12.1, 12.2, 11.0, 11.2}, flat(11.2))
	for _, tc := range []struct {
		trail, exit float64
		reason      string
	}{{10, 12 * up * 0.95 * dn, "stop_loss"}, {4, 12.6 * 0.96 * dn, "trailing_stop"}} {
		s.StopLossPct, s.TrailingStopPct = 5, tc.trail
		res := run(t, s, dip)
		if len(res.Trades) != 1 || res.Trades[0].Reason != tc.reason || res.Trades[0].ExitT != dip[4].T {
			t.Fatalf("trail %v: %+v", tc.trail, res.Trades)
		}
		near(t, "exit", res.Trades[0].Exit, tc.exit)
	}

	// A short trails the lowest low the same way, mirrored.
	short := crossSpec(10, true)
	short.TrailingStopPct = 5
	res = run(t, short, series(flat(11), bar{11, 11, 9, 9}, bar{8, 8.1, 7.5, 7.6}, bar{7.6, 7.7, 6.0, 6.2}, bar{6.2, 6.5, 6.1, 6.4}, flat(6.4)))
	if len(res.Trades) != 1 || res.Trades[0].Side != "short" || res.Trades[0].Reason != "trailing_stop" {
		t.Fatalf("short: %+v", res.Trades)
	}
	near(t, "short exit", res.Trades[0].Exit, 6*1.05*up)
}

func TestFeesBothSides(t *testing.T) {
	s := crossSpec(10, false)
	s.FeePct = 0.1
	c := series(flat(9), flat(9), bar{9, 11, 9, 11}, bar{11.5, 12, 11.5, 12}, bar{12, 13, 12, 13})
	res := run(t, s, c)
	x := 13 * dn / (11.5 * up)          // exit over entry, both slipped
	want := (x - 1 - 0.001*(1+x)) * 100 // gross move minus entry and exit fee
	near(t, "pnlPct", res.Trades[0].PnlPct, want)
	near(t, "total", res.Metrics.TotalReturnPct, want)
	near(t, "feesPct", *res.Metrics.FeesPct, 0.1*(1+x)) // 0.1% of 100 on entry, of 100·x on exit
}

// The risk metrics come from the equity curve Backtest marks: rebuilt here by
// hand for a trade that dips before it wins, with a 0.1% fee each side.
func TestRiskMetricsOnHandBuiltCurve(t *testing.T) {
	s := crossSpec(10, false)
	s.FeePct = 0.1
	c := series(flat(9), flat(9), bar{9, 11, 9, 11}, bar{11.5, 12, 11.5, 12}, bar{12, 12, 10.5, 11}, bar{11, 13, 11, 13})
	res := run(t, s, c)
	e := 11.5 * up // entry
	u := 100 / e   // units; the entry fee is u·e·0.1% = 0.1
	equity := []float64{100, 100, 100, 99.9 + u*(12-e), 99.9 + u*(11-e), 99.9 + u*(13*dn-e) - u*13*dn*0.001}
	for i, p := range res.Equity {
		near(t, "equity", p.V, equity[i])
	}
	want := market.Stats(equity, "1h")
	m := res.Metrics
	near(t, "sharpe", m.Sharpe, want.Sharpe)
	near(t, "sortino", *m.Sortino, want.Sortino)
	near(t, "volatility", *m.VolatilityAnnPct, want.VolAnnPct)
	near(t, "max drawdown", m.MaxDrawdownPct, want.MaxDrawdownPct)
	near(t, "feesPct", *m.FeesPct, 0.1+u*13*dn*0.001)
	if *m.VolatilityAnnPct <= 0 || m.AnnualizedReturnPct != nil || m.Calmar != nil {
		t.Fatalf("a 6-hour window has no annualized return or Calmar: %+v", m)
	}
}

// Annualizing needs 90 days of bars: 200 × 15m bars or 89 daily bars give
// null (and so no Calmar), 90 daily bars give both.
func TestAnnualizedNeeds90Days(t *testing.T) {
	bars := []bar{flat(9), {9, 11, 9, 11}, flat(12), flat(10)} // fill at 12, dip to 10, then back to 12
	for len(bars) < 200 {
		bars = append(bars, flat(12))
	}
	for _, tc := range []struct {
		interval string
		n        int
	}{{"15m", 200}, {"1d", 89}} {
		s := crossSpec(10, false)
		s.Interval = tc.interval
		if m := run(t, s, series(bars[:tc.n]...)).Metrics; m.AnnualizedReturnPct != nil || m.Calmar != nil {
			t.Errorf("%d × %s: annualized %v, calmar %v; want null", tc.n, tc.interval, m.AnnualizedReturnPct, m.Calmar)
		}
	}
	s := crossSpec(10, false)
	s.Interval = "1d"
	m := run(t, s, series(bars[:90]...)).Metrics
	if m.AnnualizedReturnPct == nil || m.Calmar == nil {
		t.Fatalf("90 daily bars: %+v", m)
	}
	growth := dn / up // in at 12·up, out at 12·dn
	near(t, "annualized", *m.AnnualizedReturnPct, (math.Pow(growth, 365.0/90)-1)*100)
	near(t, "max drawdown", m.MaxDrawdownPct, (1-10/(12*up))*100) // from 100 before the fill to the dip
	near(t, "calmar", *m.Calmar, *m.AnnualizedReturnPct/m.MaxDrawdownPct)
}

// A trade is a win when its net P&L is positive, even when its pnlPct rounds
// to 0.00 (the test profit factor uses).
func TestWinsCountedOnNet(t *testing.T) {
	c := series(flat(9), bar{9, 11, 9, 11}, flat(10), flat(10.0045)) // net +0.005%: in at 10·up, out at 10.0045·dn
	res := run(t, crossSpec(10, false), c)
	if tr := res.Trades[0]; tr.PnlPct != 0 || *res.Metrics.WinRatePct != 100 || res.Metrics.ProfitFactor != maxProfit {
		t.Fatalf("trade %+v, metrics %+v", tr, res.Metrics)
	}
}

func TestOppositeEntryFlips(t *testing.T) {
	c := series(flat(9), bar{9, 11, 9, 11}, bar{11, 12, 11, 12}, bar{12, 12, 9, 9}, bar{9, 9, 8, 8}, bar{8, 8, 7, 7})
	res := run(t, crossSpec(10, true), c)
	if len(res.Trades) != 2 {
		t.Fatalf("trades %+v", res.Trades)
	}
	long, short := res.Trades[0], res.Trades[1]
	// Selling 9 closes the long and opens the short at the same slipped price.
	if long.Side != "long" || long.Entry != 11*up || long.Exit != 9*dn || long.Reason != "signal" ||
		short.Side != "short" || short.Entry != 9*dn || short.EntryT != long.ExitT || short.Exit != 7*up || short.Reason != "end" {
		t.Fatalf("want long 11→9 flipped into short 9→7 at the same open: %+v", res.Trades)
	}
	near(t, "short pnlPct", short.PnlPct, (1-7*up/(9*dn))*100)
	m := res.Metrics
	near(t, "total", m.TotalReturnPct, 100*(9*dn/(11*up))*(2-7*up/(9*dn))-100)
	near(t, "max drawdown", m.MaxDrawdownPct, 25) // 12 → 9 marked against the same entry
	near(t, "win rate", *m.WinRatePct, 50)
	near(t, "profit factor", m.ProfitFactor, (9*dn/(11*up))*(1-7*up/(9*dn))/(1-9*dn/(11*up)))
	near(t, "exposure", m.ExposurePct, 400.0/6)
}

func TestNoTradesIsFlat(t *testing.T) {
	c := series(flat(9), flat(9.5), flat(10), flat(9.5), flat(9))
	res := run(t, crossSpec(1000, false), c)
	m := res.Metrics
	if m.Trades != 0 || len(res.Trades) != 0 || m.TotalReturnPct != 0 || m.Sharpe != 0 || m.MaxDrawdownPct != 0 ||
		m.WinRatePct != nil || m.ProfitFactor != 0 || m.ExposurePct != 0 || m.Bars != 5 {
		t.Fatalf("metrics %+v", m)
	}
	for _, p := range res.Equity {
		if p.V != 100 {
			t.Fatalf("equity must stay at 100: %+v", res.Equity)
		}
	}
}

func TestWarmupAndHistory(t *testing.T) {
	s := crossSpec(10, false)
	s.LongEntry = all(when(price, "crosses_above", ind("sma", 3)))
	c := series(flat(9), flat(9), flat(9), flat(9), flat(9), flat(9))
	res := run(t, s, c)
	if res.Metrics.StartT != c[2].T || res.Metrics.Bars != 4 {
		t.Fatalf("window must start where SMA(3) has a value: %+v", res.Metrics)
	}
	s.LongEntry = all(when(price, ">", ind("sma", 50)))
	if _, err := Backtest(s, c); err == nil {
		t.Fatal("SMA(50) on 6 bars must fail")
	}
}

// Every built-in validates unchanged and runs on a long synthetic series
// with capped curves. (A sine-driven series never gives some of them a
// signal, so per-strategy trade counts are checked on live data instead.)
func TestLabsRunOnSyntheticData(t *testing.T) {
	var bars []bar
	px := 100.0
	for i := range 2500 {
		next := px * (1 + 0.0015 + 0.02*math.Sin(float64(i)/9) + 0.004*math.Cos(float64(i)/2.3))
		bars = append(bars, bar{px, max(px, next) * 1.01, min(px, next) * 0.99, next})
		px = next
	}
	c := series(bars...)
	total := 0
	for _, l := range labs {
		s := l.spec
		before, _ := json.Marshal(s)
		res := run(t, s, c)
		if after, _ := json.Marshal(s); string(before) != string(after) {
			t.Errorf("%s: Validate changed the built-in:\n%s\n%s", s.Name, before, after)
		}
		m := res.Metrics
		if len(res.Equity) > maxPoints || len(res.Benchmark) > maxPoints || len(res.Trades) > maxTrades ||
			res.Equity[0].V != 100 || res.Benchmark[0].V != 100 {
			t.Errorf("%s: %d points, %d trades, metrics %+v", s.Name, len(res.Equity), len(res.Trades), m)
		}
		total += m.Trades
	}
	if total == 0 {
		t.Error("no built-in traded at all")
	}
}

func TestValidateRejects(t *testing.T) {
	for name, mutate := range map[string]func(*Spec){
		"period 0":         func(s *Spec) { s.LongEntry.All[0].Right = ind("sma", 0) },
		"period 401":       func(s *Spec) { s.LongEntry.All[0].Right = ind("ema", 401) },
		"positionPct 0":    func(s *Spec) { s.PositionPct = 0 },
		"positionPct 101":  func(s *Spec) { s.PositionPct = 101 },
		"feePct 1.5":       func(s *Spec) { s.FeePct = 1.5 },
		"feePct negative":  func(s *Spec) { s.FeePct = -0.1 },
		"bars 199":         func(s *Spec) { s.Bars = 199 },
		"bars 3001":        func(s *Spec) { s.Bars = 3001 },
		"stop 0.05":        func(s *Spec) { s.StopLossPct = 0.05 },
		"take 51":          func(s *Spec) { s.TakeProfitPct = 51 },
		"trail 0.4":        func(s *Spec) { s.TrailingStopPct = 0.4 },
		"trail 51":         func(s *Spec) { s.TrailingStopPct = 51 },
		"trail negative":   func(s *Spec) { s.TrailingStopPct = -2 },
		"no entry":         func(s *Spec) { s.LongExit, s.LongEntry = s.LongEntry, nil },
		"empty entry":      func(s *Spec) { s.LongEntry = &RuleSet{} },
		"unknown symbol":   func(s *Spec) { s.Symbol = "SHIB" },
		"bad interval":     func(s *Spec) { s.Interval = "5m" },
		"9 conditions":     func(s *Spec) { s.LongEntry.Any = make([]Condition, 8) },
		"unknown kind":     func(s *Spec) { s.LongEntry.All[0].Left = Indicator{Kind: "vwap"} },
		"unknown op":       func(s *Spec) { s.LongEntry.All[0].Op = ">=" },
		"macd fast > slow": func(s *Spec) { s.LongEntry.All[0].Left = Indicator{Kind: "macd", Fast: 26, Slow: 12, Signal: 9} },
		"const no value":   func(s *Spec) { s.LongEntry.All[0].Right = Indicator{Kind: "const"} },
		"const vs const":   func(s *Spec) { s.LongEntry.All[0].Left = k(1) },
		"bb stdDev 0":      func(s *Spec) { s.LongEntry.All[0].Right = Indicator{Kind: "bb_upper", Period: 20} },
	} {
		s := crossSpec(10, false)
		mutate(&s)
		if err := s.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestValidateDefaultsAndCanonicalizes(t *testing.T) {
	var s Spec
	err := json.Unmarshal([]byte(`{"symbol":"eth","interval":"4h","positionPct":50,"longExit":{},
		"longEntry":{"all":[{"left":{"kind":"price","period":7},"op":">","right":{"kind":"const","value":0}}]}}`), &s)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(s)
	got := string(b)
	for _, want := range []string{`"symbol":"ETH"`, `"bars":1000`, `"feePct":0.05`, `"left":{"kind":"price"}`, `"value":0`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
	if s.LongExit != nil || s.Name != "Custom strategy" {
		t.Errorf("empty rule set must be dropped and a name defaulted: %s", got)
	}
}

func TestPromptExamplesValidate(t *testing.T) {
	for _, e := range designExamples {
		var g generated
		if err := json.Unmarshal([]byte(e.reply), &g); err != nil {
			t.Fatalf("%q: %v", e.idea, err)
		}
		if err := g.Spec.Validate(); err != nil {
			t.Errorf("%q: %v", e.idea, err)
		}
	}
}

func TestNameIDsAreStable(t *testing.T) {
	uuid := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	seen := map[string]bool{}
	for _, l := range labs {
		id := nameID(l.spec.Name)
		if !uuid.MatchString(id) || nameID(l.spec.Name) != id || seen[id] || !isUUID(id) {
			t.Errorf("%s: bad or duplicate id %s", l.spec.Name, id)
		}
		seen[id] = true
	}
	// Pinned: changing the namespace or the derivation breaks saved links.
	if got := nameID("BTC EMA Trend Rider"); got != pinnedID {
		t.Errorf("nameID drifted: %s", got)
	}
}

func TestSample(t *testing.T) {
	if s := sample(1000, 400); len(s) != 400 || s[0] != 0 || s[399] != 999 {
		t.Errorf("sample(1000, 400) = len %d, %d…%d", len(s), s[0], s[len(s)-1])
	}
	if s := sample(3, 400); len(s) != 3 || s[2] != 2 {
		t.Errorf("sample(3, 400) = %v", s)
	}
}

const pinnedID = "967c9273-c207-560a-9ae7-43707b7d1451"

func TestShortSample(t *testing.T) {
	const day = 86400
	for _, c := range []struct {
		days, trades int64
		want         bool
	}{{180, 10, false}, {400, 30, false}, {179, 30, true}, {83, 11, true}, {400, 9, true}, {0, 0, true}} {
		if got := shortSample(Metrics{StartT: 1_700_000_000, EndT: 1_700_000_000 + c.days*day, Trades: int(c.trades)}); got != c.want {
			t.Errorf("%d days, %d trades: %v", c.days, c.trades, got)
		}
	}
}

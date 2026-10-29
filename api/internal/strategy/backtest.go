package strategy

import (
	"fmt"
	"math"
	"slices"

	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
)

// Metrics is the contract Metrics. Strategies saved before Sortino,
// VolatilityAnnPct, Calmar and FeesPct existed read them back as 0 / nil.
type Metrics struct {
	TotalReturnPct      float64  `json:"totalReturnPct"`
	AnnualizedReturnPct *float64 `json:"annualizedReturnPct"` // nil under market.MinAnnualizeDays (90)
	BuyHoldReturnPct    float64  `json:"buyHoldReturnPct"`
	Sharpe              float64  `json:"sharpe"`
	Sortino             *float64 `json:"sortino,omitempty"` // nil only on rows saved before it existed (also volatility, fees): absent, not 0
	VolatilityAnnPct    *float64 `json:"volatilityAnnPct,omitempty"`
	MaxDrawdownPct      float64  `json:"maxDrawdownPct"` // positive magnitude: 12.5 = a 12.5% peak-to-trough fall
	Calmar              *float64 `json:"calmar"`         // annualized / max drawdown; nil when either is missing or 0
	WinRatePct          *float64 `json:"winRatePct"`     // nil without a closed trade
	Trades              int      `json:"trades"`
	ProfitFactor        float64  `json:"profitFactor"`
	ExposurePct         float64  `json:"exposurePct"`
	FeesPct             *float64 `json:"feesPct,omitempty"` // fees paid, in percent of starting equity
	StartT              int64    `json:"startT"`
	EndT                int64    `json:"endT"`
	Bars                int      `json:"bars"`
	// BarsPerYear is what a quote-feed run's ratios are annualized with: its
	// own bars a year over its span (market.SessionBarsPerYear), its window
	// (and so the annualized return) the span itself. Absent on a 24/7 run,
	// which uses market.BarsPerYear(interval), and on a quote-feed row saved
	// before it (normalized on read: sessionMetrics).
	BarsPerYear *float64 `json:"barsPerYear,omitempty"`
}

type Trade struct {
	Side   string  `json:"side"` // long | short
	EntryT int64   `json:"entryT"`
	ExitT  int64   `json:"exitT"`
	Entry  float64 `json:"entry"`
	Exit   float64 `json:"exit"`
	PnlPct float64 `json:"pnlPct"` // net of both fees, relative to the position's entry notional
	Reason string  `json:"reason"` // signal | stop_loss | trailing_stop | take_profit | end
}

type Point struct {
	T int64   `json:"t"`
	V float64 `json:"v"`
}

// Result is also the POST /api/strategies/backtest response body.
type Result struct {
	Metrics   Metrics `json:"metrics"`
	Equity    []Point `json:"equity"`
	Benchmark []Point `json:"benchmark"`
	Trades    []Trade `json:"trades"`
}

const (
	maxPoints = 400
	maxTrades = 100
	maxProfit = 100 // profit factor cap: no losing trades would otherwise be +Inf
)

// Backtest runs a validated spec over closed candles (oldest first).
//
// The window starts at the first bar where every indicator has a value.
// Signals are read on a bar's close and filled at the next bar's open; one
// position at a time. An exit rule or the opposite entry closes a position
// (the opposite entry also opens the reverse one at the same open). Stop and
// target are checked on each bar's low/high, stop first, filling at the level
// or at the open if the bar gapped through it. A trailing stop trails the best
// high (lowest low for a short) of the bars after the entry bar up to the one
// before, so it never looks ahead; the tighter of it and the fixed stop applies. Every fill pays the fee and the
// paper engine's slippage against us. Equity starts at 100 and is marked to
// market at every close; the benchmark buys at the first open the strategy
// could have traded and holds.
func Backtest(s Spec, candles []market.Candle) (Result, error) { return backtestFrom(s, candles, 0) }

// backtestFrom is Backtest with the window starting no earlier than
// candles[from]: the bars before it only warm the indicators up (the
// live-forward run).
func backtestFrom(s Spec, candles []market.Candle, from int) (Result, error) {
	n := len(candles)
	ev := newEvaluator(candles)
	sig := ev.signals(s)
	start := max(ev.warmup, from)
	if start > n-3 {
		return Result{}, fmt.Errorf("not enough history: the indicators need about %d bars and only %d are available", start+3, n)
	}

	fee, size := s.FeePct/100, s.PositionPct/100
	cash := 100.0 // equity while flat; collateral (equity minus open P&L) in a position
	side, want := 0, 0
	var units, entry, fees float64
	var best float64 // best high (long) or low (short) since the entry bar; 0 until a bar has closed after it
	var entryT int64
	entryAt := 0
	var trades []Trade
	var grossWin, grossLoss float64
	wins := 0
	closeAt := func(px float64, t int64, reason string) {
		px *= 1 - float64(side)*paper.Slippage
		pnl := float64(side) * units * (px - entry)
		exitFee := units * px * fee
		cash += pnl - exitFee
		fees += exitFee
		net := pnl - exitFee - units*entry*fee
		if net > 0 {
			wins++
			grossWin += net
		} else {
			grossLoss -= net
		}
		t0 := Trade{Side: "long", EntryT: entryT, ExitT: t, Entry: entry, Exit: px, PnlPct: round2(net / (units * entry) * 100), Reason: reason}
		if side < 0 {
			t0.Side = "short"
		}
		trades = append(trades, t0)
		side = 0
	}

	m := n - start
	equity, bench := make([]float64, m), make([]float64, m)
	held := 0
	for i := start; i < n; i++ {
		c := candles[i]
		if want != side {
			if side != 0 {
				closeAt(c.O, c.T, "signal")
			}
			if want != 0 && cash > 0 {
				side, entry, entryT = want, c.O*(1+float64(want)*paper.Slippage), c.T
				entryAt, best = i, 0
				units = cash * size / entry
				cash -= units * entry * fee
				fees += units * entry * fee
			}
		}
		if side != 0 { // held during this bar, even if a stop closes it below
			held++
		}
		if side != 0 {
			px, reason := 0.0, "stop_loss"
			if s.StopLossPct > 0 {
				px = entry * (1 - float64(side)*s.StopLossPct/100)
			}
			if trail := best * (1 - float64(side)*s.TrailingStopPct/100); s.TrailingStopPct > 0 && best > 0 &&
				(px == 0 || float64(side)*(trail-px) > 0) { // tighter: higher for a long, lower for a short
				px, reason = trail, "trailing_stop"
			}
			if side > 0 && px > 0 && c.L <= px {
				closeAt(min(c.O, px), c.T, reason)
			} else if side < 0 && px > 0 && c.H >= px {
				closeAt(max(c.O, px), c.T, reason)
			}
		}
		if side != 0 && s.TakeProfitPct > 0 {
			px := entry * (1 + float64(side)*s.TakeProfitPct/100)
			if side > 0 && c.H >= px {
				closeAt(max(c.O, px), c.T, "take_profit")
			} else if side < 0 && c.L <= px {
				closeAt(min(c.O, px), c.T, "take_profit")
			}
		}
		if side != 0 && cash+float64(side)*units*(c.C-entry) <= 0 {
			// ponytail: liquidated at the close that wipes equity out, not intrabar.
			closeAt(c.C, c.T, "stop_loss")
			cash = 0
		}
		if side != 0 && i == n-1 {
			closeAt(c.C, c.T, "end")
		}
		if side != 0 && i > entryAt { // this bar's extreme counts from the next bar on
			ext := c.H
			if side < 0 {
				ext = c.L
			}
			if best == 0 || float64(side)*(ext-best) > 0 {
				best = ext
			}
		}

		k := i - start
		equity[k] = cash
		if side != 0 {
			equity[k] += float64(side) * units * (c.C - entry)
		}
		bench[k] = 100
		if i > start {
			bench[k] = 100 * c.C / candles[start+1].O
		}

		want = sig.next(side, i)
	}

	st, days := market.Stats(equity, s.Interval), windowDays(m, s.Interval)
	var perYear *float64
	if quoteFeed(s.Symbol) { // session bars: annualize over the run's own span, not 24/7 bars
		perYear = num(market.SessionBarsPerYear(m, candles[start].T, candles[n-1].T))
		st, days = market.StatsPerYear(equity, *perYear), spanDays(candles[start].T, candles[n-1].T)
	}
	met := Metrics{
		TotalReturnPct:      equity[m-1] - 100,
		AnnualizedReturnPct: market.AnnualizedPct(100, equity[m-1], days),
		BuyHoldReturnPct:    bench[m-1] - 100,
		Sharpe:              st.Sharpe,
		Sortino:             num(round2(st.Sortino)),
		VolatilityAnnPct:    num(round2(st.VolAnnPct)),
		MaxDrawdownPct:      st.MaxDrawdownPct,
		Trades:              len(trades),
		ExposurePct:         float64(held) / float64(m) * 100,
		FeesPct:             num(round2(fees)),
		StartT:              candles[start].T,
		EndT:                candles[n-1].T,
		Bars:                m,
		BarsPerYear:         perYear,
	}
	if len(trades) > 0 {
		met.WinRatePct = num(round2(float64(wins) / float64(len(trades)) * 100))
		met.ProfitFactor = maxProfit
		if grossLoss > 0 {
			met.ProfitFactor = min(grossWin/grossLoss, maxProfit)
		}
	}
	for _, p := range []*float64{&met.TotalReturnPct, &met.BuyHoldReturnPct, &met.Sharpe,
		&met.MaxDrawdownPct, &met.ProfitFactor, &met.ExposurePct} {
		*p = round2(*p)
	}
	if a := met.AnnualizedReturnPct; a != nil {
		*a = round2(*a)
		if met.MaxDrawdownPct > 0 {
			met.Calmar = num(round2(*a / met.MaxDrawdownPct))
		}
	}

	if len(trades) > maxTrades {
		trades = trades[len(trades)-maxTrades:]
	}
	if trades == nil {
		trades = []Trade{}
	}
	return Result{Metrics: met, Equity: points(candles[start:], equity), Benchmark: points(candles[start:], bench), Trades: trades}, nil
}

// Decide is the position Backtest moves to after the close of the last bar
// in closed (closed bars only, oldest first) while holding pos ("long",
// "short" or "flat"): an exit rule or the opposite entry closes, the
// opposite entry flips, long wins a tie between entries. Before every
// indicator has a value it holds, as Backtest does. Stops and targets are
// not part of it: the paper engine's watcher fills those.
func Decide(s Spec, closed []market.Candle, pos string) string {
	ev := newEvaluator(closed)
	sig := ev.signals(s)
	i := len(closed) - 1
	if i < 1 || i < ev.warmup {
		return pos
	}
	side := map[string]int{"long": 1, "short": -1}[pos]
	return [...]string{"short", "flat", "long"}[sig.next(side, i)+1]
}

// windowDays is how many days bars of interval span around the clock (a
// quote feed's window is its span: spanDays).
func windowDays(bars int, interval string) float64 {
	return float64(bars) / market.BarsPerYear(interval) * 365
}

func spanDays(startT, endT int64) float64 { return float64(endT-startT) / 86400 }

// quoteFeed reports whether symbol is a quote-feed listing: bars only in its
// venue's sessions.
func quoteFeed(symbol string) bool {
	info, ok := market.Lookup(symbol)
	return ok && info.QuoteFeed()
}

// sessionMetrics re-annualizes a quote-feed row saved before its ratios used
// its own session bars (no BarsPerYear): Sharpe, Sortino (a ±100 clamp
// stays) and volatility × √(session bars a year / 24/7 bars a year), and the
// annualized return and Calmar over its span. Other rows are untouched.
func sessionMetrics(m *Metrics, interval, symbol string) {
	full := market.BarsPerYear(interval)
	perYear := market.SessionBarsPerYear(m.Bars, m.StartT, m.EndT)
	if m.BarsPerYear != nil || !quoteFeed(symbol) || full <= 0 || perYear <= 0 {
		return
	}
	k := math.Sqrt(perYear / full)
	m.Sharpe = round2(m.Sharpe * k)
	if m.Sortino != nil && math.Abs(*m.Sortino) < maxSortino {
		m.Sortino = num(round2(min(max(*m.Sortino*k, -maxSortino), maxSortino)))
	}
	if m.VolatilityAnnPct != nil {
		m.VolatilityAnnPct = num(round2(*m.VolatilityAnnPct * k))
	}
	m.AnnualizedReturnPct, m.Calmar = market.AnnualizedPct(100, 100+m.TotalReturnPct, spanDays(m.StartT, m.EndT)), nil
	if a := m.AnnualizedReturnPct; a != nil {
		*a = round2(*a)
		if m.MaxDrawdownPct > 0 {
			m.Calmar = num(round2(*a / m.MaxDrawdownPct))
		}
	}
	m.BarsPerYear = &perYear
}

// maxSortino is market.Stats' Sortino clamp.
const maxSortino = 100

// points pairs values with bar times, evenly downsampled to at most
// maxPoints and always keeping the first and last bar.
func points(c []market.Candle, v []float64) []Point {
	idx := sample(len(v), maxPoints)
	out := make([]Point, len(idx))
	for i, j := range idx {
		out[i] = Point{T: c[j].T, V: round2(v[j])}
	}
	return out
}

// sample returns at most k evenly spaced indices of 0..n-1, first and last included.
func sample(n, k int) []int {
	if n <= k {
		k = n
	}
	out := make([]int, k)
	for i := range out {
		if k > 1 {
			out[i] = i * (n - 1) / (k - 1)
		}
	}
	return out
}

func round2(x float64) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return 0 // JSON cannot carry them
	}
	return math.Round(x*100) / 100
}

// evaluator computes each distinct indicator series once per backtest.
type evaluator struct {
	candles                  []market.Candle
	close, high, low, volume []float64
	cache                    map[string][]float64
	warmup                   int // first bar where every compiled series has a value
}

func newEvaluator(c []market.Candle) *evaluator {
	ev := &evaluator{candles: c, cache: map[string][]float64{},
		close: market.Closes(c), high: make([]float64, len(c)), low: make([]float64, len(c)), volume: make([]float64, len(c))}
	for i, k := range c {
		ev.high[i], ev.low[i], ev.volume[i] = k.H, k.L, k.V
	}
	return ev
}

func (ev *evaluator) series(in Indicator) []float64 {
	key := fmt.Sprintf("%s/%d/%g/%d/%d/%d", in.Kind, in.Period, in.StdDev, in.Fast, in.Slow, in.Signal)
	if in.Value != nil {
		key += fmt.Sprintf("/%g", *in.Value)
	}
	if s, ok := ev.cache[key]; ok {
		return s
	}
	var s []float64
	switch in.Kind {
	case "price":
		s = ev.close
	case "volume":
		s = ev.volume
	case "const":
		s = make([]float64, len(ev.close))
		for i := range s {
			s[i] = *in.Value
		}
	case "sma":
		s = market.SMA(ev.close, in.Period)
	case "ema":
		s = market.EMA(ev.close, in.Period)
	case "rsi":
		s = market.RSI(ev.close, in.Period)
	case "roc":
		s = market.ROC(ev.close, in.Period)
	case "atr":
		s = market.ATR(ev.candles, in.Period)
	case "highest": // Donchian: highest high of the previous n bars
		s = market.Highest(ev.high, in.Period)
	case "lowest":
		s = market.Lowest(ev.low, in.Period)
	case "volume_sma":
		s = market.SMA(ev.volume, in.Period)
	case "bb_upper", "bb_lower":
		upper, _, lower := market.Bollinger(ev.close, in.Period, in.StdDev)
		s = lower
		if in.Kind == "bb_upper" {
			s = upper
		}
	case "macd", "macd_signal":
		macd, sig := market.MACD(ev.close, in.Fast, in.Slow, in.Signal)
		s = sig
		if in.Kind == "macd" {
			s = macd
		}
	}
	first := slices.IndexFunc(s, func(x float64) bool { return !math.IsNaN(x) })
	if first < 0 {
		first = len(s)
	}
	ev.warmup = max(ev.warmup, first)
	ev.cache[key] = s
	return s
}

type cond struct {
	l, r []float64
	op   string
}

type rules struct{ all, any []cond }

// signals are a spec's four compiled rule sets.
type signals struct{ longEntry, longExit, shortEntry, shortExit *rules }

func (ev *evaluator) signals(s Spec) signals {
	return signals{ev.compile(s.LongEntry), ev.compile(s.LongExit), ev.compile(s.ShortEntry), ev.compile(s.ShortExit)}
}

// next is the side (1 long, -1 short, 0 flat) a position on side moves to
// after bar i's close: an exit or the opposite entry closes, the opposite
// entry flips, and long wins when both entries fire on the same bar.
func (g signals) next(side, i int) int {
	switch side {
	case 1:
		if g.shortEntry.fires(i) {
			return -1
		} else if g.longExit.fires(i) {
			return 0
		}
	case -1:
		if g.longEntry.fires(i) {
			return 1
		} else if g.shortExit.fires(i) {
			return 0
		}
	default:
		if g.longEntry.fires(i) {
			return 1
		} else if g.shortEntry.fires(i) {
			return -1
		}
	}
	return side
}

// compile resolves a rule set's indicators to series; nil never fires.
func (ev *evaluator) compile(rs *RuleSet) *rules {
	if rs == nil {
		return nil
	}
	conv := func(cs []Condition) []cond {
		out := make([]cond, len(cs))
		for i, c := range cs {
			out[i] = cond{l: ev.series(c.Left), r: ev.series(c.Right), op: c.Op}
		}
		return out
	}
	return &rules{all: conv(rs.All), any: conv(rs.Any)}
}

// fires reports whether the rule set holds at bar i: all AND (any).
func (r *rules) fires(i int) bool {
	if r == nil || len(r.all)+len(r.any) == 0 {
		return false
	}
	for _, c := range r.all {
		if !c.holds(i) {
			return false
		}
	}
	if len(r.any) == 0 {
		return true
	}
	return slices.ContainsFunc(r.any, func(c cond) bool { return c.holds(i) })
}

// holds compares at bar i; NaN (warm-up) compares false. A cross needs the
// previous bar at or on the other side: prev l <= prev r && l > r.
func (c cond) holds(i int) bool {
	l, r := c.l, c.r
	switch c.op {
	case ">":
		return l[i] > r[i]
	case "<":
		return l[i] < r[i]
	case "crosses_above":
		return i > 0 && l[i-1] <= r[i-1] && l[i] > r[i]
	case "crosses_below":
		return i > 0 && l[i-1] >= r[i-1] && l[i] < r[i]
	}
	return false
}

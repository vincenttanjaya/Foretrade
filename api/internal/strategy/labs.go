package strategy

import (
	"cmp"
	"context"
	"crypto/sha1"
	"fmt"
	"slices"
	"sync"
	"time"
)

const (
	labsAuthor = "Foretrade Labs"
	labsTTL    = time.Hour
)

// labsCreated is the Labs launch: the first UTC day after the built-ins'
// rules were committed (2026-10-02; unchanged since). Their backtest ends
// here and their live forward starts here, so the two never overlap. A var
// so tests can move it inside the fake market's history.
var labsCreated = time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)

// labsNamespace seeds the built-in strategies' name-derived IDs. Never change
// it: saved links to /strategies/{id} depend on it.
var labsNamespace = [16]byte{0x5e, 0x1a, 0x9c, 0x4d, 0x2b, 0x7f, 0x4e, 0x10, 0x8a, 0x3c, 0x61, 0xd2, 0x0b, 0xe4, 0x97, 0x25}

// nameID is a UUID v5 (RFC 9562): SHA-1 of namespace + name, version and
// variant bits set. The same name always yields the same ID.
func nameID(name string) string {
	h := sha1.New()
	h.Write(labsNamespace[:])
	h.Write([]byte(name))
	b := h.Sum(nil)[:16]
	b[6] = b[6]&0x0f | 0x50
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

type labsStrategy struct {
	description string
	spec        Spec
}

func num(v float64) *float64 { return &v }

func ind(kind string, period int) Indicator { return Indicator{Kind: kind, Period: period} }

var (
	price = Indicator{Kind: "price"}
	macd  = Indicator{Kind: "macd", Fast: 12, Slow: 26, Signal: 9}
	macdS = Indicator{Kind: "macd_signal", Fast: 12, Slow: 26, Signal: 9}
)

func when(l Indicator, op string, r Indicator) Condition { return Condition{Left: l, Op: op, Right: r} }

func all(cs ...Condition) *RuleSet { return &RuleSet{All: cs} }

// labs are the built-in strategies shown on the Top strategies board.
var labs = []labsStrategy{
	{"Rides 4h Bitcoin trends: long when the 20 EMA crosses above the 50 EMA, out on the cross back down or a 5% stop.",
		Spec{Name: "BTC EMA Trend Rider", Symbol: "BTC", Interval: "4h", Bars: 1500, PositionPct: 100, FeePct: 0.05, StopLossPct: 5,
			LongEntry: all(when(ind("ema", 20), "crosses_above", ind("ema", 50))),
			LongExit:  all(when(ind("ema", 20), "crosses_below", ind("ema", 50)))}},
	{"Buys hourly ETH washouts when RSI(14) dips below 30 and sells the bounce when RSI recovers above 55; 4% stop.",
		Spec{Name: "ETH RSI Dip Buyer", Symbol: "ETH", Interval: "1h", Bars: 2000, PositionPct: 100, FeePct: 0.05, StopLossPct: 4,
			LongEntry: all(when(ind("rsi", 14), "crosses_below", Indicator{Kind: "const", Value: num(30)})),
			LongExit:  all(when(ind("rsi", 14), "crosses_above", Indicator{Kind: "const", Value: num(55)}))}},
	{"Chases 4h Solana volatility breakouts above the upper Bollinger band (20, 2) and exits back below the 20 SMA; 6% stop, 15% target.",
		Spec{Name: "SOL Bollinger Breakout", Symbol: "SOL", Interval: "4h", Bars: 1500, PositionPct: 100, FeePct: 0.05, StopLossPct: 6, TakeProfitPct: 15,
			LongEntry: all(when(price, "crosses_above", Indicator{Kind: "bb_upper", Period: 20, StdDev: 2})),
			LongExit:  all(when(price, "crosses_below", ind("sma", 20)))}},
	{"Trades Chainlink momentum on 4h: long on a MACD (12/26/9) cross above its signal while MACD is positive, out on the cross back down.",
		Spec{Name: "LINK MACD Momentum", Symbol: "LINK", Interval: "4h", Bars: 1500, PositionPct: 100, FeePct: 0.05, StopLossPct: 7,
			LongEntry: all(when(macd, "crosses_above", macdS), when(macd, ">", Indicator{Kind: "const", Value: num(0)})),
			LongExit:  all(when(macd, "crosses_below", macdS))}},
	{"The classic turtle rule on daily Bitcoin: buy a close above the prior 20-day high, sell a close below the prior 10-day low.",
		Spec{Name: "BTC Donchian Turtle", Symbol: "BTC", Interval: "1d", Bars: 1500, PositionPct: 100, FeePct: 0.05,
			LongEntry: all(when(price, "crosses_above", ind("highest", 20))),
			LongExit:  all(when(price, "crosses_below", ind("lowest", 10)))}},
	{"Holds daily ETH while the 50-day SMA is above the 200-day SMA (golden cross) and steps aside on the death cross.",
		Spec{Name: "ETH Golden Cross", Symbol: "ETH", Interval: "1d", Bars: 2000, PositionPct: 100, FeePct: 0.05,
			LongEntry: all(when(ind("sma", 50), "crosses_above", ind("sma", 200))),
			LongExit:  all(when(ind("sma", 50), "crosses_below", ind("sma", 200)))}},
	{"Buys hourly Dogecoin breakouts above the prior 48-hour high on above-average volume; exits under the 20 EMA, 4% stop, 10% target.",
		Spec{Name: "DOGE Volume Breakout", Symbol: "DOGE", Interval: "1h", Bars: 2000, PositionPct: 50, FeePct: 0.05, StopLossPct: 4, TakeProfitPct: 10,
			LongEntry: all(when(price, "crosses_above", ind("highest", 48)), when(Indicator{Kind: "volume"}, ">", ind("volume_sma", 20))),
			LongExit:  all(when(price, "crosses_below", ind("ema", 20)))}},
	{"Trades daily Avalanche trends both ways: long when the 20 EMA crosses above the 50 EMA, short on the cross below; 10% stop either way.",
		Spec{Name: "AVAX Long/Short Trend", Symbol: "AVAX", Interval: "1d", Bars: 1500, PositionPct: 100, FeePct: 0.05, StopLossPct: 10,
			LongEntry:  all(when(ind("ema", 20), "crosses_above", ind("ema", 50))),
			ShortEntry: all(when(ind("ema", 20), "crosses_below", ind("ema", 50)))}},
}

// labsCache backtests the built-ins on first use and keeps them for labsTTL
// (their window ends at the launch, so a refresh only guards a bad fetch).
type labsCache struct {
	mu   sync.Mutex
	at   time.Time
	list []Strategy // full samples first, each group by Sharpe, best first
}

// get returns the backtested built-ins. The lock is held across the refresh
// so concurrent callers wait for one round of Binance calls.
func (c *labsCache) get(ctx context.Context, run func(context.Context, Spec) (Result, error)) ([]Strategy, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.list != nil && time.Since(c.at) < labsTTL {
		return c.list, nil
	}
	list := make([]Strategy, len(labs))
	errs := make([]error, len(labs))
	var wg sync.WaitGroup
	for i, l := range labs {
		wg.Go(func() {
			res, err := run(ctx, l.spec)
			if err != nil {
				errs[i] = fmt.Errorf("%s: %w", l.spec.Name, err)
				return
			}
			s := Strategy{ID: nameID(l.spec.Name), Name: l.spec.Name, Description: l.description, Author: labsAuthor,
				Prompt: l.description, Spec: l.spec, Result: res, ShortSample: shortSample(res.Metrics), CreatedAt: labsCreated}
			s.Explanation = fallbackExplanation(s)
			list[i] = s
		})
	}
	wg.Wait()
	if err := cmp.Or(errs...); err != nil {
		return nil, err
	}
	slices.SortStableFunc(list, func(a, b Strategy) int {
		return cmp.Or(boolCmp(a.ShortSample, b.ShortSample), cmp.Compare(b.Metrics.Sharpe, a.Metrics.Sharpe))
	})
	c.list, c.at = list, time.Now()
	return list, nil
}

// boolCmp orders false before true.
func boolCmp(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	}
	return -1
}

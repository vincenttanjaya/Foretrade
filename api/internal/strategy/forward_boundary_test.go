package strategy

import (
	"math"
	"slices"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/market"
)

// Dropping since's subseconds admits an open that happened before publication.
func TestFirstOpenPreservesPublicationPrecision(t *testing.T) {
	for _, tc := range []struct {
		name      string
		sec       int64
		wantAfter int64
	}{
		{"15m", 900, 87300},
		{"1h", 3600, 90000},
		{"4h", 14400, 100800},
		{"1d", 86400, 172800},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, pub := range []struct {
				name   string
				offset time.Duration
				want   int64
			}{
				{"aligned", 0, 86400},
				{"one_nanosecond_after", time.Nanosecond, tc.wantAfter},
				{"one_millisecond_after", time.Millisecond, tc.wantAfter},
			} {
				t.Run(pub.name, func(t *testing.T) {
					since := time.Unix(86400, 0).Add(pub.offset)
					if got := firstOpen(since, tc.sec); got != pub.want {
						t.Errorf("first open = %d, want %d for publication %s", got, pub.want, since.Format(time.RFC3339Nano))
					}
				})
			}
		})
	}
}

// Replaying the previous close after a subsecond publication imports a known
// cross and a pre-publication position; the forward book must instead be flat.
func TestForwardSubsecondPublicationRejectsKnownCross(t *testing.T) {
	short := crossSpec(10, true)
	short.LongEntry = nil
	for _, tc := range []struct {
		name           string
		spec           Spec
		candles        []market.Candle
		alignedReturn  float64
		alignedBuyHold float64
		afterBuyHold   float64
	}{
		{"long", crossSpec(10, false), series(flat(9), bar{9, 11, 9, 11}, flat(30), flat(40), flat(50), flat(60)), 99.92, 100, 50},
		{"short", short, series(flat(11), bar{11, 11, 9, 9}, flat(6), flat(4), flat(3), flat(2)), 66.65, -66.67, -50},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The cross closes at 7200. At exactly 7200 it may fill: long
			// 30.006 -> 59.988, short 5.9988 -> 2.0004 (2 bps each side).
			// After 7200, buy & hold starts at 40 or 4, ending at 60 or 2.
			for _, pub := range []struct {
				name       string
				offset     time.Duration
				wantTrades int
				wantStart  int64
				wantBars   int
				wantReturn float64
				wantHold   float64
			}{
				{"aligned", 0, 1, 3600, 5, tc.alignedReturn, tc.alignedBuyHold},
				{"one_nanosecond_after", time.Nanosecond, 0, 7200, 4, 0, tc.afterBuyHold},
				{"one_millisecond_after", time.Millisecond, 0, 7200, 4, 0, tc.afterBuyHold},
			} {
				t.Run(pub.name, func(t *testing.T) {
					since := time.Unix(7200, 0).Add(pub.offset)
					res := forwardRun(tc.spec, since, tc.candles)
					if res == nil {
						t.Fatal("four eligible closed bars with price-only rules must produce a forward run")
					}
					for _, tr := range res.Trades {
						if time.Unix(tr.EntryT, 0).Before(since) {
							t.Errorf("fill at %d precedes exact publication %s", tr.EntryT, since.Format(time.RFC3339Nano))
						}
						if tr.Side != tc.name || tr.EntryT != 7200 {
							t.Errorf("aligned cross must fill %s at 7200: %+v", tc.name, tr)
						}
					}
					if len(res.Trades) != pub.wantTrades || res.Metrics.Trades != pub.wantTrades {
						t.Errorf("trades = %+v (metric %d), want %d", res.Trades, res.Metrics.Trades, pub.wantTrades)
					}
					if res.Metrics.StartT != pub.wantStart || res.Metrics.EndT != 18000 || res.Metrics.Bars != pub.wantBars {
						t.Errorf("window = %+v, want start %d, end 18000, %d bars", res.Metrics, pub.wantStart, pub.wantBars)
					}
					if len(res.Equity) == 0 || len(res.Benchmark) == 0 {
						t.Fatal("measured run needs its flat anchor")
					}
					anchor := Point{T: pub.wantStart, V: 100}
					if res.Equity[0] != anchor || res.Benchmark[0] != anchor {
						t.Errorf("anchors = %+v / %+v, want %+v", res.Equity[0], res.Benchmark[0], anchor)
					}
					if res.Metrics.TotalReturnPct != pub.wantReturn || res.Metrics.BuyHoldReturnPct != pub.wantHold {
						t.Errorf("returns = %v / %v, want %v / %v", res.Metrics.TotalReturnPct, res.Metrics.BuyHoldReturnPct, pub.wantReturn, pub.wantHold)
					}
					if pub.wantTrades == 0 {
						if res.Metrics.ExposurePct != 0 {
							t.Errorf("known cross carried exposure into the flat forward book: %v", res.Metrics.ExposurePct)
						}
						for _, p := range res.Equity {
							if p.V != 100 {
								t.Errorf("pre-publication position changed flat equity: %+v", p)
							}
						}
					}
				})
			}
		})
	}
}

// Moving the first eligible signal one bar earlier or later misprices both
// the strategy and benchmark. Future bars must not revise the earlier marks.
func TestForwardFirstAllowedOpenPricesTradeAndBenchmark(t *testing.T) {
	short := crossSpec(10, true)
	short.LongEntry = nil
	for _, tc := range []struct {
		name       string
		spec       Spec
		candles    []market.Candle
		wantTrade  Trade
		wantHold   float64
		wantEquity []Point
		wantBench  []Point
		wantPrefix []Point
	}{
		{
			"long", crossSpec(10, false),
			series(flat(9), flat(9), bar{9, 11, 9, 11}, bar{20, 22, 20, 22}, bar{22, 24, 22, 24}, bar{24, 25, 24, 25}),
			Trade{Side: "long", EntryT: 10800, ExitT: 18000, Entry: 20.004, Exit: 24.995, PnlPct: 24.95, Reason: "end"}, 25,
			[]Point{{7200, 100}, {10800, 109.98}, {14400, 119.98}, {18000, 124.95}},
			[]Point{{7200, 100}, {10800, 110}, {14400, 120}, {18000, 125}},
			[]Point{{7200, 100}, {10800, 109.98}, {14400, 119.95}},
		},
		{
			"short", short,
			series(flat(11), flat(11), bar{11, 11, 9, 9}, bar{8, 8, 7, 7}, bar{7, 7, 6.5, 6.5}, bar{6.5, 6.5, 6, 6}),
			Trade{Side: "short", EntryT: 10800, ExitT: 18000, Entry: 7.9984, Exit: 6.0012, PnlPct: 24.97, Reason: "end"}, -25,
			[]Point{{7200, 100}, {10800, 112.48}, {14400, 118.73}, {18000, 124.97}},
			[]Point{{7200, 100}, {10800, 87.5}, {14400, 81.25}, {18000, 75}},
			[]Point{{7200, 100}, {10800, 112.48}, {14400, 118.72}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Publication is inside bar 2. Its close signals; bar 3 opens at
			// 20 or 8. The benchmark has no costs. Strategy entry/exit pay
			// 2 bps, giving the independently calculated curves above.
			for _, offset := range []time.Duration{time.Nanosecond, time.Millisecond} {
				t.Run(offset.String(), func(t *testing.T) {
					since := time.Unix(7200, 0).Add(offset)
					res := forwardRun(tc.spec, since, tc.candles)
					if res == nil || len(res.Trades) != 1 {
						t.Fatalf("post-publication close must trade once: %+v", res)
					}
					tr, want := res.Trades[0], tc.wantTrade
					if time.Unix(tr.EntryT, 0).Before(since) {
						t.Errorf("fill at %d precedes exact publication %s", tr.EntryT, since.Format(time.RFC3339Nano))
					}
					if tr.Side != want.Side || tr.EntryT != want.EntryT || tr.ExitT != want.ExitT || tr.Reason != want.Reason ||
						math.Abs(tr.Entry-want.Entry) > 1e-9 || math.Abs(tr.Exit-want.Exit) > 1e-9 || tr.PnlPct != want.PnlPct {
						t.Errorf("trade = %+v, want %+v", tr, want)
					}
					if res.Metrics.StartT != 7200 || res.Metrics.Bars != 4 || res.Metrics.TotalReturnPct != want.PnlPct || res.Metrics.BuyHoldReturnPct != tc.wantHold {
						t.Errorf("first allowed open produced wrong metrics: %+v", res.Metrics)
					}
					if !slices.Equal(res.Equity, tc.wantEquity) || !slices.Equal(res.Benchmark, tc.wantBench) {
						t.Errorf("curves = %+v / %+v, want %+v / %+v", res.Equity, res.Benchmark, tc.wantEquity, tc.wantBench)
					}
					part := forwardRun(tc.spec, since, tc.candles[:5])
					if part == nil {
						t.Fatal("three eligible closed bars must produce the shorter run")
					}
					if !slices.Equal(part.Equity, tc.wantPrefix) || !slices.Equal(part.Benchmark, tc.wantBench[:3]) {
						t.Errorf("prefix curves = %+v / %+v, want %+v / %+v", part.Equity, part.Benchmark, tc.wantPrefix, tc.wantBench[:3])
					}
				})
			}
		})
	}
}

package strategy

import (
	"context"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
)

func TestDecide(t *testing.T) {
	long := crossSpec(10, true) // long on a cross above 10, short on a cross below
	gt := Spec{LongEntry: all(when(price, ">", k(10))), LongExit: all(when(price, "<", k(8)))}
	lt := Spec{ShortEntry: all(when(price, "<", k(10))), ShortExit: all(when(price, ">", k(12)))}
	up := series(flat(9), flat(9), flat(11))    // crosses above 10 on the last bar
	down := series(flat(11), flat(11), flat(9)) // crosses below 10 on the last bar
	above := series(flat(9), flat(11), flat(11.5))
	for name, tc := range map[string]struct {
		s      Spec
		closed []market.Candle
		pos    string
		want   string
	}{
		"crosses_above on the last bar enters":   {long, up, "flat", "long"},
		"a cross one bar back is old news":       {long, above, "flat", "flat"},
		"crosses_below on the last bar shorts":   {long, down, "flat", "short"},
		"opposite entry flips a long":            {long, down, "long", "short"},
		"opposite entry flips a short":           {long, up, "short", "long"},
		"a held long stays without a signal":     {long, above, "long", "long"},
		"> enters":                               {gt, above, "flat", "long"},
		"exit rule closes a long":                {gt, series(flat(9), flat(9), flat(7)), "long", "flat"},
		"< enters a short":                       {lt, down, "flat", "short"},
		"exit rule covers a short":               {lt, series(flat(9), flat(9), flat(13)), "short", "flat"},
		"long wins when both entries fire":       {Spec{LongEntry: gt.LongEntry, ShortEntry: lt.ShortEntry}, series(flat(9), flat(11)), "flat", "long"},
		"before the indicators warm up it holds": {Spec{LongEntry: all(when(price, ">", ind("sma", 5)))}, up, "flat", "flat"},
		"one bar is not enough to decide":        {gt, series(flat(11)), "flat", "flat"},
	} {
		if got := Decide(tc.s, tc.closed, tc.pos); got != tc.want {
			t.Errorf("%s: %s → %s, want %s", name, tc.pos, got, tc.want)
		}
	}
	// Only the bars it is given: the cross on the bar after closed is not seen yet.
	if got := Decide(long, up[:2], "flat"); got != "flat" {
		t.Errorf("a still-open bar leaked into the decision: %s", got)
	}
}

// Stepping Decide bar by bar over a growing window of closed bars rebuilds
// exactly the trades Backtest books, for every built-in without its stop
// and target (those belong to the paper engine's watcher, not to Decide).
func TestDecideMatchesBacktest(t *testing.T) {
	bin := markettest.New(t)
	mkt := testMarketClient(bin.URL)
	type leg struct {
		side         string
		entryT, exit int64
	}
	total := 0
	for _, l := range labs {
		s := l.spec
		s.StopLossPct, s.TakeProfitPct = 0, 0
		c, err := mkt.Candles(context.Background(), s.Symbol, s.Interval, 1000)
		if err != nil {
			t.Fatal(err)
		}
		c = c[:len(c)-1] // closed bars only
		res, err := Backtest(s, c)
		if err != nil {
			t.Fatal(err)
		}
		var got []leg
		pos, openT := "flat", int64(0)
		for i := range len(c) - 1 { // a signal on bar i fills at bar i+1's open
			next := Decide(s, c[:i+1], pos)
			if next == pos {
				continue
			}
			if pos != "flat" {
				got = append(got, leg{pos, openT, c[i+1].T})
			}
			pos, openT = next, c[i+1].T
		}
		if pos != "flat" { // Backtest closes what is still open on the last bar
			got = append(got, leg{pos, openT, c[len(c)-1].T})
		}
		got = got[max(0, len(got)-maxTrades):]
		total += len(got)
		if len(got) != len(res.Trades) {
			t.Errorf("%s: Decide made %d trades, Backtest %d", s.Name, len(got), len(res.Trades))
			continue
		}
		for i, tr := range res.Trades {
			if want := (leg{tr.Side, tr.EntryT, tr.ExitT}); got[i] != want {
				t.Errorf("%s trade %d: Decide %+v, Backtest %+v", s.Name, i, got[i], want)
			}
		}
	}
	if total < 20 {
		t.Fatalf("only %d trades compared", total)
	}
}

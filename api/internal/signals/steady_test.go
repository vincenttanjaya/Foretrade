package signals

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
)

// The steady-v1 acceptance fixtures (contract "Signals board"): each one is a
// case where the rule before steady-v1 decides otherwise (named in the test).

// picked is a pick made at at from a (price 100, ATR 3.21): target 106.4,
// invalidation 93.58 bullish (mirrored bearish), valid 72 h.
func picked(a asset, side string, at time.Time) Pick {
	b := build([]asset{a}, nil, nil, nil, at)
	for _, p := range append(b.Bullish, b.Bearish...) {
		if p.Side == side {
			p.Rationale = "From the board that picked it."
			p.Headlines = []Headline{}
			return p
		}
	}
	panic("no " + side + " pick for " + a.Symbol)
}

// hb is a 1h bar opened h hours after t.
func hb(t time.Time, h float64, high, low, closePx float64) market.Candle {
	return market.Candle{T: t.Add(time.Duration(h * float64(time.Hour))).Unix(), O: closePx, H: high, L: low, C: closePx}
}

// quiet is 1h bars from t − 2 h to t + n h that touch no 2 ATR level, closing
// at 100 + i/100.
func quiet(t time.Time, n int) []market.Candle {
	var out []market.Candle
	for i := -2; i < n; i++ {
		c := 100 + float64(i)/100
		out = append(out, hb(t, float64(i), c+1, c-1, c))
	}
	return out
}

func sideOf(b Board, side string) []Pick {
	if side == "bearish" {
		return b.Bearish
	}
	return b.Bullish
}

func find(b Board, symbol string) (Pick, bool) {
	for _, p := range append(append([]Pick{}, b.Bullish...), b.Bearish...) {
		if p.Symbol == symbol {
			return p, true
		}
	}
	return Pick{}, false
}

// warmUp is an asset 40 days old: 41 daily bars (40 closed and today's) rising
// gently, so SMA50 is still 0 and trend reads uptrend through the artefact.
func warmUp(t *testing.T, closed int) asset {
	t.Helper()
	var bars []market.Candle
	day0 := now.Truncate(24 * time.Hour).Add(-40 * 24 * time.Hour)
	for i := 0; i <= 40; i++ {
		c := 100 * (1 + 0.002*float64(i) + 0.01*float64(i%3-1))
		bars = append(bars, market.Candle{T: day0.Add(time.Duration(i) * 24 * time.Hour).Unix(), O: c, H: c * 1.02, L: c * 0.98, C: c})
	}
	a := market.AnalyzeCandles("STRK", "1d", bars, market.Closes(bars))
	if a.Sma50 != 0 || a.Trend != "uptrend" || a.Atr14 <= 0 {
		t.Fatalf("fixture: %+v", a)
	}
	return asset{a, time.Unix(bars[len(bars)-1].T, 0).UTC(), closed}
}

// Acceptance 1: an asset under 60 closed daily bars ranks as an uptrend
// through SMA50 = 0 (here at score ≥ 20) and the rule before steady-v1 picked
// it; it is not eligible now. The same asset at 60 bars is, and an ATR of 0
// is not.
func TestGuardWarmUp(t *testing.T) {
	young := warmUp(t, 40)
	f := young.Analysis
	if s := score(f.Return30dPct, f.Return7dPct, f.Rsi14, f.Trend, nil); s < threshold {
		t.Fatalf("the artefact scores %d", s)
	}
	if eligible(young, now) {
		t.Fatal("40 closed bars eligible")
	}
	if b := build([]asset{young, mk("ETH", 20, "uptrend")}, nil, nil, nil, now); len(b.Bullish) != 1 || b.Bullish[0].Symbol != "ETH" {
		t.Fatalf("the warm-up asset was picked: %v", symbols(b.Bullish))
	}
	grown := warmUp(t, 60)
	if b := build([]asset{grown}, nil, nil, nil, now); !eligible(grown, now) || len(b.Bullish) != 1 || b.Bullish[0].Symbol != "STRK" {
		t.Fatalf("at 60 bars: %v", symbols(b.Bullish))
	}
	flat := mk("BTC", 40, "uptrend")
	flat.Atr14 = 0
	stale := mk("SOL", 40, "uptrend")
	stale.opened = now.Add(-49 * time.Hour)
	if eligible(flat, now) || eligible(stale, now) || !eligible(mk("ETH", 0, "range"), now) {
		t.Fatal("eligibility: an ATR of 0 or a halted asset")
	}
	// The ledger lists every analyzed asset, eligible or not.
	if u := universeOf([]asset{young, grown, flat}, now); len(u) != 3 || u[0].E || !u[1].E || u[2].E || u[2].P != 100 {
		t.Fatalf("universe %+v", u)
	}
}

// analyze counts the closed daily bars from the same cached read as the
// analysis: a perp the venue serves 41 daily bars of has 40 closed.
func TestAnalyzeCountsClosedBars(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	e.Binance.SetRetention("xyz:NVDA", 41)
	at := markettest.End.Add(12 * time.Hour) // the fake's newest daily bar opened at End: still open
	got, err := analyze(context.Background(), e.Deps.Market, at)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]asset{}
	for _, a := range got {
		by[a.Symbol] = a
	}
	if btc, nvda := by["BTC"], by["NVDA"]; btc.closedBars != 299 || !eligible(btc, at) || nvda.closedBars != 40 || eligible(nvda, at) {
		t.Fatalf("BTC %d bars (eligible %v), NVDA %d bars (eligible %v)", btc.closedBars, eligible(btc, at), nvda.closedBars, eligible(nvda, at))
	}
}

// Acceptance 2: a held pick whose 1h high, after the pick and closed by now,
// reached its target leaves at the next board (target_hit at the bar's close,
// at the target) and its slot refills: here with the same asset as a new
// pick, since it still ranks first. The rule before steady-v1 kept the old
// pick (same pickedAt and price) and recorded nothing.
func TestTargetExit(t *testing.T) {
	t0 := now
	x := picked(mk("BTC", 40, "uptrend"), "bullish", t0)
	if x.Target != 106.4 || x.Invalidation != 93.58 {
		t.Fatalf("levels %+v", x)
	}
	bars := []market.Candle{hb(t0, 0, 101, 99, 100.5), hb(t0, 1, 104, 100, 103), hb(t0, 2, 106.5, 102, 105), hb(t0, 3, 99, 90, 91)}
	at := t0.Add(4 * time.Hour)
	prev := &Board{CreatedAt: t0, Bullish: []Pick{x}, Bearish: []Pick{}}
	kept, exits := resolveHeld(prev, at, func(string) ([]market.Candle, error) { return bars, nil })
	if len(kept) != 0 || len(exits) != 1 || exits[0] != (ledgerExit{"BTC", "bullish", t0, "target_hit", t0.Add(3 * time.Hour), 106.4}) {
		t.Fatalf("kept %+v, exits %+v", kept, exits)
	}
	now2 := mk("BTC", 40, "uptrend")
	now2.Price, now2.Atr14 = 104, 3.5
	b := build([]asset{now2, mk("ETH", 20, "uptrend")}, nil, kept, []Board{*prev}, at)
	if got := strings.Join(symbols(b.Bullish), " "); got != "BTC:bullish ETH:bullish" {
		t.Fatalf("bullish %s", got)
	}
	if p := b.Bullish[0]; !p.PickedAt.Equal(at) || p.PriceAtPick != 104 || p.Target != 111 || p.Invalidation != 97 || !p.fresh {
		t.Fatalf("the slot's new pick: %+v", p)
	}
}

// Acceptance 3: a low at or under the invalidation leaves the pick
// invalidated at that level although its asset still ranks first; a bar that
// touched both levels is invalidated (the conservative read). Bearish mirrors.
func TestInvalidationExit(t *testing.T) {
	t0, at := now, now.Add(8*time.Hour)
	x := picked(mk("BTC", 40, "uptrend"), "bullish", t0)
	for name, bars := range map[string][]market.Candle{
		"low":  {hb(t0, 0, 101, 99, 100), hb(t0, 1, 101, 93.5, 94), hb(t0, 2, 120, 90, 110)},
		"both": {hb(t0, 0, 101, 99, 100), hb(t0, 1, 107, 93, 100)},
	} {
		if got, ok := resolve(x, bars, at); !ok || got != (ledgerExit{"BTC", "bullish", t0, "invalidated", t0.Add(2 * time.Hour), 93.58}) {
			t.Errorf("%s: %+v %v", name, got, ok)
		}
	}
	y := picked(mk("LINK", -40, "downtrend"), "bearish", t0) // target 93.58, invalidation 106.4
	if got, ok := resolve(y, []market.Candle{hb(t0, 0, 101, 99, 100), hb(t0, 5, 106.4, 99, 105)}, at); !ok || got.Reason != "invalidated" || got.ExitPrice != 106.4 {
		t.Errorf("bearish high: %+v %v", got, ok)
	}
	if got, ok := resolve(y, []market.Candle{hb(t0, 0, 101, 93.58, 95)}, at); !ok || got.Reason != "target_hit" || got.ExitPrice != 93.58 || !got.ExitAt.Equal(t0.Add(time.Hour)) {
		t.Errorf("bearish low: %+v %v", got, ok)
	}
}

// Acceptance 4: a bar that opened before the pick (here 30 min before it)
// does not count, however far it reached; nor does a bar not closed by now.
func TestBarsBeforeThePickIgnored(t *testing.T) {
	t0 := now
	x := picked(mk("BTC", 40, "uptrend"), "bullish", t0)
	bars := []market.Candle{hb(t0, -0.5, 110, 99, 100), hb(t0, 0.5, 101, 99, 100), hb(t0, 3, 120, 99, 100)}
	if got, ok := resolve(x, bars, t0.Add(3*time.Hour+30*time.Minute)); ok {
		t.Fatalf("exit %+v", got)
	}
	if got, ok := resolve(x, bars, t0.Add(4*time.Hour)); !ok || got.Reason != "target_hit" || !got.ExitAt.Equal(t0.Add(4*time.Hour)) {
		t.Fatalf("once the bar closed: %+v %v", got, ok)
	}
}

// Acceptance 5: a pick whose score falls to 5 stays, with this board's score,
// factors and volatility tier and its pick-time fields and rationale; the
// rule before steady-v1 removed it (score_fell). An asset not analyzed this
// board keeps its previous values.
func TestHeldThroughRankDrop(t *testing.T) {
	t0, at := now, now.Add(4*time.Hour)
	x := picked(mk("BTC", 40, "uptrend"), "bullish", t0)
	y := picked(mk("SOL", 30, "uptrend"), "bullish", t0)
	weak := mk("BTC", 3.37, "range")
	weak.RealizedVolAnnPct = 30
	b := build([]asset{weak, mk("ETH", 50, "uptrend")}, nil, []Pick{x, y}, nil, at)
	if got := strings.Join(symbols(b.Bullish), " "); got != "ETH:bullish SOL:bullish BTC:bullish" {
		t.Fatalf("bullish %s", got)
	}
	got, _ := find(b, "BTC")
	if got.Score != 5 || got.Factors.Return30dPct != 3.37 || got.Factors.Trend != "range" || got.VolTier != "low" || got.fresh ||
		!got.PickedAt.Equal(t0) || got.PriceAtPick != 100 || got.Target != 106.4 || got.Invalidation != 93.58 || !got.ValidUntil.Equal(t0.Add(valid)) ||
		got.Rationale != "From the board that picked it." {
		t.Fatalf("held BTC %+v", got)
	}
	if sol, _ := find(b, "SOL"); sol.Score != y.Score || sol.Factors != y.Factors || !sol.PickedAt.Equal(t0) {
		t.Fatalf("SOL, not analyzed this board, %+v", sol)
	}
}

// Acceptance 6: a pick still ranked first at validUntil closes expired at the
// last 1h close at or before validUntil and a new pick starts with a new
// pickedAt. The rule before steady-v1 re-picked it silently, no record kept.
func TestExpiry(t *testing.T) {
	t0 := now
	end := t0.Add(valid)
	x := picked(mk("BTC", 40, "uptrend"), "bullish", t0)
	bars := append(quiet(t0, 72), hb(t0, 72, 200, 1, 999)) // the bar opened at validUntil is past it
	if got, ok := resolve(x, bars, end.Add(-4*time.Hour)); ok {
		t.Fatalf("before its end: %+v", got)
	}
	kept, exits := resolveHeld(&Board{Bullish: []Pick{x}}, end, func(string) ([]market.Candle, error) { return bars, nil })
	if len(kept) != 0 || len(exits) != 1 || exits[0] != (ledgerExit{"BTC", "bullish", t0, "expired", end, 100.71}) {
		t.Fatalf("at its end: kept %+v, exits %+v", kept, exits)
	}
	// Live boards drift off the 72 h mark, so the board that ends a pick is usually hours past validUntil: a bar
	// that closed after validUntil (here one touching both levels) is outside the pick's life and changes nothing.
	if got, ok := resolve(x, bars, end.Add(4*time.Hour)); !ok || got != (ledgerExit{"BTC", "bullish", t0, "expired", end, 100.71}) {
		t.Fatalf("resolved 4 h past its end: %+v %v", got, ok)
	}
	a := mk("BTC", 40, "uptrend")
	a.opened = end.Add(-8 * time.Hour)
	b := build([]asset{a}, nil, kept, []Board{{CreatedAt: end.Add(-4 * time.Hour), Bullish: []Pick{x}}}, end)
	if len(b.Bullish) != 1 || !b.Bullish[0].PickedAt.Equal(end) || !b.Bullish[0].ValidUntil.Equal(end.Add(valid)) || !b.Bullish[0].fresh {
		t.Fatalf("the next pick %+v", b.Bullish)
	}
}

// Acceptance 7: an asset on the bearish side of a board 3 days ago (here an
// old-rule board: no invalidation, no ledger) is skipped although it now
// scores highest, and the 5th-ranked qualifier is picked; exactly 7 days back
// still blocks, 7 days and one board back does not. The rule before
// steady-v1 flipped it to bullish.
func TestCooldown(t *testing.T) {
	assets := []asset{mk("SOL", 60, "uptrend"), mk("BTC", 30, "uptrend"), mk("ETH", 20, "uptrend"), mk("BNB", 10, "uptrend"), mk("XRP", 5, "uptrend"),
		mk("ADA", 0, "uptrend")} // 50, 47, 43, 34, 27, 20
	old := Pick{Symbol: "SOL", Side: "bearish", Score: -30, PriceAtPick: 150, Target: 140, ValidUntil: now}
	for _, c := range []struct {
		ago  time.Duration
		want string
	}{
		{3 * 24 * time.Hour, "BTC ETH BNB XRP"},
		{6*24*time.Hour + 20*time.Hour, "BTC ETH BNB XRP"},
		{7 * 24 * time.Hour, "BTC ETH BNB XRP"}, // the rule's 7 days, written out: the constant cannot drift unseen
		{7*24*time.Hour + every, "SOL BTC ETH BNB"},
	} {
		recent := []Board{{CreatedAt: now.Add(-c.ago), Bullish: []Pick{}, Bearish: []Pick{old}}}
		b := build(assets, nil, nil, recent, now)
		var got []string
		for _, p := range b.Bullish {
			got = append(got, p.Symbol)
		}
		if strings.Join(got, " ") != c.want {
			t.Errorf("bearish %s ago: bullish %v, want %s", c.ago, got, c.want)
		}
	}
	// The same side is no block: a bullish pick on a recent board may be picked bullish again.
	recent := []Board{{CreatedAt: now.Add(-24 * time.Hour), Bullish: []Pick{{Symbol: "SOL", Side: "bullish"}}}}
	if b := build(assets, nil, nil, recent, now); b.Bullish[0].Symbol != "SOL" {
		t.Errorf("same side blocked: %v", symbols(b.Bullish))
	}
	// A kept pick holds its side and its symbol stays off the other.
	k := picked(mk("LINK", -40, "downtrend"), "bearish", now.Add(-8*time.Hour))
	b := build([]asset{mk("LINK", 60, "uptrend"), mk("BTC", 30, "uptrend")}, nil, []Pick{k}, nil, now)
	if got := strings.Join(append(symbols(b.Bullish), symbols(b.Bearish)...), " "); got != "BTC:bullish LINK:bearish" {
		t.Errorf("kept bearish LINK now scoring 60: %s", got)
	}
}

// Acceptance 9: picks of a board stored before steady-v1 (invalidation 0)
// are dropped without a candle read or a record, and the board fills from
// scratch (the same asset on the same side becomes a new pick).
func TestLegacyPrevDropped(t *testing.T) {
	legacy := &Board{CreatedAt: now.Add(-4 * time.Hour), Bullish: []Pick{{Symbol: "BTC", Side: "bullish", Score: 49, PriceAtPick: 95,
		PickedAt: now.Add(-10 * time.Hour), Target: 104, ValidUntil: now.Add(62 * time.Hour)}}, Bearish: []Pick{}}
	reads := 0
	kept, exits := resolveHeld(legacy, now, func(string) ([]market.Candle, error) { reads++; return nil, nil })
	if len(kept) != 0 || exits == nil || len(exits) != 0 || reads != 0 {
		t.Fatalf("kept %+v, exits %+v, %d reads", kept, exits, reads)
	}
	b := build([]asset{mk("BTC", 40, "uptrend")}, nil, kept, []Board{*legacy}, now)
	if len(b.Bullish) != 1 || !b.Bullish[0].PickedAt.Equal(now) || b.Bullish[0].PriceAtPick != 100 || b.Bullish[0].Invalidation != 93.58 {
		t.Fatalf("from scratch: %+v", b.Bullish)
	}
	if kept, _ := resolveHeld(nil, now, nil); kept != nil {
		t.Fatal("no previous board")
	}
	// A steady pick whose 2 ATR is over its price has an invalidation below 0 (it can never be reached): it is held
	// like any other, not taken for a legacy pick and dropped unrecorded.
	wild := mk("PEPE", 40, "uptrend")
	wild.Price, wild.Atr14 = 1, 0.75
	w := picked(wild, "bullish", now.Add(-4*time.Hour))
	if w.Target != 2.5 || w.Invalidation != -0.5 {
		t.Fatalf("levels %+v", w)
	}
	near := []market.Candle{hb(w.PickedAt, -1, 1.05, 0.95, 1), hb(w.PickedAt, 0, 1.2, 0.6, 1.1), hb(w.PickedAt, 1, 1.3, 0.01, 0.9)}
	kept, exits = resolveHeld(&Board{Bullish: []Pick{w}}, now, func(string) ([]market.Candle, error) { return near, nil })
	if len(kept) != 1 || kept[0].Invalidation != -0.5 || len(exits) != 0 {
		t.Fatalf("a negative invalidation: kept %+v, exits %+v", kept, exits)
	}
}

// Acceptance 10: when the hourly read fails the pick stays held (no exit, no
// error for the board) and is resolved later from the same past bars; so does
// a pick past its end with no close yet. A day past its end it is dropped
// without an exit, so it cannot sit on the board.
func TestCandleFailureKeepsThePick(t *testing.T) {
	t0 := now
	x := picked(mk("BTC", 40, "uptrend"), "bullish", t0)
	down := func(string) ([]market.Candle, error) { return nil, errors.New("binance down") }
	kept, exits := resolveHeld(&Board{Bullish: []Pick{x}}, t0.Add(8*time.Hour), down)
	if len(kept) != 1 || kept[0].PickedAt != x.PickedAt || kept[0].Target != x.Target || len(exits) != 0 {
		t.Fatalf("read failed: kept %+v exits %+v", kept, exits)
	}
	// The next board reads the bars it missed: the touch at hour 2 still counts, at its own close.
	bars := []market.Candle{hb(t0, 0, 101, 99, 100), hb(t0, 2, 107, 99, 106)}
	if _, exits = resolveHeld(&Board{Bullish: kept}, t0.Add(12*time.Hour), func(string) ([]market.Candle, error) { return bars, nil }); len(exits) != 1 ||
		!exits[0].ExitAt.Equal(t0.Add(3*time.Hour)) {
		t.Fatalf("resolved later: %+v", exits)
	}
	// Past its end with no bar in its window: held, then dropped a day on.
	gap := []market.Candle{hb(t0, -1, 101, 99, 100)}
	end := t0.Add(valid)
	if kept, exits = resolveHeld(&Board{Bullish: []Pick{x}}, end, func(string) ([]market.Candle, error) { return gap, nil }); len(kept) != 1 || len(exits) != 0 {
		t.Fatalf("no close at its end: kept %+v exits %+v", kept, exits)
	}
	if kept, exits = resolveHeld(&Board{Bullish: []Pick{x}}, end.Add(stuck), down); len(kept) != 0 || len(exits) != 0 {
		t.Fatalf("a day past its end: kept %+v exits %+v", kept, exits)
	}
	// Bars that start after the pick (an outage past the cached window) cannot say how it ended.
	late := quiet(t0.Add(30*time.Hour), 60)
	if kept, exits = resolveHeld(&Board{Bullish: []Pick{x}}, end.Add(20*time.Hour), func(string) ([]market.Candle, error) { return late, nil }); len(kept) != 1 || len(exits) != 0 {
		t.Fatalf("a window after the pick: kept %+v exits %+v", kept, exits)
	}
}

// Each side holds at most 4 (kept picks count) and is ordered by this
// board's score, bullish descending and bearish ascending, ties in universe
// order; a side may be empty.
func TestSteadyOrderAndCap(t *testing.T) {
	k1 := picked(mk("DOGE", 25, "uptrend"), "bullish", now.Add(-8*time.Hour))
	k2 := picked(mk("ADA", -40, "downtrend"), "bearish", now.Add(-8*time.Hour))
	assets := []asset{mk("ETH", 20, "uptrend"), mk("BTC", 20, "uptrend"), mk("SOL", 60, "uptrend"), mk("XRP", 30, "uptrend"), mk("BNB", 45, "uptrend"),
		mk("DOGE", 0, "uptrend"), mk("LINK", -20, "downtrend"), mk("AVAX", -20, "downtrend"), mk("ADA", -40, "downtrend")}
	b := build(assets, nil, []Pick{k1, k2}, nil, now)
	if got := strings.Join(symbols(b.Bullish), " "); got != "SOL:bullish BNB:bullish XRP:bullish DOGE:bullish" {
		t.Errorf("bullish %s", got) // DOGE kept at 20: four a side, so neither ETH nor BTC fits
	}
	if got := strings.Join(symbols(b.Bearish), " "); got != "ADA:bearish AVAX:bearish LINK:bearish" {
		t.Errorf("bearish %s", got) // AVAX and LINK tie at −43: universe order
	}
	if b := build([]asset{mk("ETH", 20, "uptrend"), mk("BTC", 20, "uptrend")}, nil, nil, nil, now); strings.Join(symbols(b.Bullish), " ") != "BTC:bullish ETH:bullish" || b.Bearish == nil {
		t.Errorf("a tie: %v %#v", symbols(b.Bullish), b.Bearish)
	}
}

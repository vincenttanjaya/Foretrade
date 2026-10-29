package paper

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
)

func ptr(f float64) *float64 { return &f }

// TestEngine runs the ledger against the test DB and fake Binance prices
// (one cached assets call). Skips without DATABASE_URL_TEST.
func TestEngine(t *testing.T) {
	pool := storetest.Pool(t)
	user := storetest.User(t, pool)
	ctx := context.Background()
	e := NewEngine(pool, testMarketClient(markettest.New(t).URL))

	mark, err := e.market.Price(ctx, "BTC")
	if err != nil {
		t.Fatal(err)
	}
	p, err := e.Portfolio(ctx, user)
	if err != nil || p.Cash != StartingBalance || len(p.Positions) != 0 {
		t.Fatalf("fresh account: %+v %v", p, err)
	}

	var ve *ValidationError
	for _, req := range []OrderRequest{
		{Symbol: "NOPE", Side: "buy", Notional: ptr(100)},
		{Symbol: "BTC", Side: "hold", Notional: ptr(100)},
		{Symbol: "BTC", Side: "buy", Notional: ptr(100), Qty: ptr(1)},
		{Symbol: "BTC", Side: "buy", Notional: ptr(5)},
		{Symbol: "BTC", Side: "buy", Notional: ptr(100), StopLoss: ptr(mark * 1.1)},    // stop above a long
		{Symbol: "BTC", Side: "sell", Notional: ptr(100), TakeProfit: ptr(mark * 1.1)}, // target above a short
		{Symbol: "BTC", Side: "buy", Notional: ptr(100), Source: "stop_loss"},
	} {
		if _, _, err := e.Order(ctx, user, req); !errors.As(err, &ve) {
			t.Errorf("%+v: err = %v, want ValidationError", req, err)
		}
	}
	if _, _, err := e.Order(ctx, user, OrderRequest{Symbol: "BTC", Side: "buy", Notional: ptr(250_000)}); !errors.Is(err, ErrInsufficient) {
		t.Fatalf("2.5× equity: err = %v", err)
	}
	// qty × price overflows to ±Inf and equity turns NaN (the buy only on amd64, where
	// nothing fuses into an FMA; the sell everywhere): the risk rule must still refuse.
	for _, side := range []string{"buy", "sell"} {
		if _, _, err := e.Order(ctx, user, OrderRequest{Symbol: "BTC", Side: side, Qty: ptr(1e308)}); !errors.Is(err, ErrInsufficient) {
			t.Fatalf("%s 1e308 BTC: err = %v", side, err)
		}
	}

	tr, p, err := e.Order(ctx, user, OrderRequest{Symbol: "btc", Side: "buy", Notional: ptr(1000),
		StopLoss: ptr(mark * 0.5), TakeProfit: ptr(mark * 2), Note: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if tr.Symbol != "BTC" || tr.Source != "manual" || !approx(tr.Notional, 1000) || !approx(tr.Fee, 1) || tr.Price <= mark {
		t.Fatalf("buy trade: %+v (mark %v)", tr, mark)
	}
	if len(p.Positions) != 1 || p.Positions[0].StopLoss == nil || !approx(p.Cash, StartingBalance-1001) {
		t.Fatalf("after buy: %+v", p)
	}

	half := p.Positions[0].Qty / 2
	tr, p, err = e.Order(ctx, user, OrderRequest{Symbol: "BTC", Side: "sell", Qty: ptr(half)})
	if err != nil || tr.Side != "sell" || !approx(p.Positions[0].Qty, half) || p.Positions[0].TakeProfit == nil {
		t.Fatalf("partial sell keeps stops: %+v %+v %v", tr, p, err)
	}
	if !approx(tr.RealizedPnl, half*(tr.Price-p.Positions[0].AvgPrice)-tr.Fee) {
		t.Fatalf("realized = %v", tr.RealizedPnl)
	}

	// Five concurrent buys serialize on the account row: cash must account for all of them.
	var wg sync.WaitGroup
	errs := make(chan error, 5)
	for range 5 {
		wg.Go(func() {
			_, _, err := e.Order(ctx, user, OrderRequest{Symbol: "ETH", Side: "buy", Notional: ptr(100)})
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	trades, err := e.Trades(ctx, user, 50)
	if err != nil || len(trades) != 7 {
		t.Fatalf("trades: %d %v", len(trades), err)
	}
	cash := StartingBalance
	for _, tr := range trades {
		sign := -1.0
		if tr.Side == "sell" {
			sign = 1
		}
		cash += sign*tr.Notional - tr.Fee
	}
	if p, _ = e.Portfolio(ctx, user); math.Abs(p.Cash-cash) > 1e-6 {
		t.Fatalf("cash %v, trades imply %v", p.Cash, cash)
	}

	// Watcher: a stop above the mark fires on the next check.
	if _, err := pool.Exec(ctx, "update positions set stop_loss = $2 where user_id = $1 and symbol = 'ETH'", user, 1e9); err != nil {
		t.Fatal(err)
	}
	if err := e.checkStops(ctx); err != nil {
		t.Fatal(err)
	}
	if trades, _ = e.Trades(ctx, user, 1); trades[0].Source != "stop_loss" || trades[0].Symbol != "ETH" {
		t.Fatalf("watcher close: %+v", trades[0])
	}

	tr, p, err = e.Close(ctx, user, "BTC", "manual")
	if err != nil || len(p.Positions) != 0 || tr.Side != "sell" {
		t.Fatalf("close: %+v %+v %v", tr, p, err)
	}
	if _, _, err := e.Close(ctx, user, "BTC", "manual"); !errors.Is(err, ErrNoPosition) {
		t.Fatalf("close again: %v", err)
	}
	if !approx(p.Equity, p.StartingBalance+p.RealizedPnl) {
		t.Fatalf("flat equity %v != start + realized %v", p.Equity, p.RealizedPnl)
	}

	if p, err = e.Reset(ctx, user); err != nil || p.Cash != StartingBalance || p.RealizedPnl != 0 {
		t.Fatalf("reset: %+v %v", p, err)
	}
	if trades, _ = e.Trades(ctx, user, 50); len(trades) != 0 {
		t.Fatalf("reset left %d trades", len(trades))
	}
}

// A basket's legs are booked in one transaction; history lists them newest
// first, in the reverse of the order they were booked.
func TestTradesListBasketLegsInBookingOrder(t *testing.T) {
	pool := storetest.Pool(t)
	user := storetest.User(t, pool)
	ctx := context.Background()
	e := NewEngine(pool, testMarketClient(markettest.New(t).URL))
	syms := []string{"BTC", "ETH", "SOL", "BNB", "XRP", "DOGE"} // 1 in 720 to pass by chance
	reqs := make([]OrderRequest, len(syms))
	for i, s := range syms {
		reqs[i] = OrderRequest{Symbol: s, Side: "buy", Notional: ptr(100)}
	}
	if _, _, err := e.Orders(ctx, user, reqs); err != nil {
		t.Fatal(err)
	}
	got, err := e.Trades(ctx, user, 10)
	if err != nil || len(got) != len(syms) {
		t.Fatalf("trades: %v %v", got, err)
	}
	for i, tr := range got {
		if want := syms[len(syms)-1-i]; tr.Symbol != want {
			t.Fatalf("trade %d is %s, want %s (history %v)", i, tr.Symbol, want, got)
		}
	}
}

// A close whose transaction began first but waited for the account lock is
// booked after the buy that overtook it, so history must list it as newest.
func TestTradesFollowBookingOrderAcrossOverlappingRequests(t *testing.T) {
	pool := storetest.Pool(t)
	user := storetest.User(t, pool)
	ctx := context.Background()
	e := NewEngine(pool, testMarketClient(markettest.New(t).URL))
	marks, _, err := e.marks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.Order(ctx, user, OrderRequest{Symbol: "ETH", Side: "buy", Notional: ptr(1000)}); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx) // the close starts first...
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, _, err := e.Order(ctx, user, OrderRequest{Symbol: "ETH", Side: "buy", Notional: ptr(500)}); err != nil {
		t.Fatal(err) // ...but this buy takes the account lock first
	}
	acct, positions, err := load(ctx, tx, user, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fillOrder(ctx, tx, user, order{symbol: "ETH", closeAll: true, source: "manual"}, &acct, positions, marks, nil); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := e.Trades(ctx, user, 10)
	if err != nil || len(got) != 3 || got[0].Side != "sell" || got[1].Notional != 500 {
		t.Fatalf("history is not in booking order: %+v %v", got, err)
	}
}

// A held pair Binance stops quoting is valued at its average price: the
// portfolio and other orders keep working, while orders and stops on that pair
// wait for a live price instead of filling at zero.
func TestUnquotedPositionValuedAtAvg(t *testing.T) {
	pool := storetest.Pool(t)
	user := storetest.User(t, pool)
	ctx := context.Background()
	bin := markettest.New(t)
	e := NewEngine(pool, testMarketClient(bin.URL))
	mark := markettest.Price("AAVEUSDT")
	if _, _, err := e.Order(ctx, user, OrderRequest{Symbol: "AAVE", Side: "buy", Notional: ptr(1000), StopLoss: ptr(mark * 0.5)}); err != nil {
		t.Fatal(err)
	}

	bin.Down("AAVEUSDT", true)
	e = NewEngine(pool, testMarketClient(bin.URL)) // a fresh client: no cached ticker
	if err := e.checkStops(ctx); err != nil {
		t.Fatal(err)
	}
	p, err := e.Portfolio(ctx, user)
	if err != nil || len(p.Positions) != 1 {
		t.Fatalf("portfolio: %+v %v", p, err)
	}
	pos := p.Positions[0]
	if pos.MarkPrice != pos.AvgPrice || pos.UnrealizedPnl != 0 || !approx(p.Equity, p.Cash+pos.Qty*pos.AvgPrice) {
		t.Fatalf("unquoted position at avg: %+v, equity %v", pos, p.Equity)
	}
	if _, p, err = e.Order(ctx, user, OrderRequest{Symbol: "BTC", Side: "buy", Notional: ptr(100)}); err != nil || len(p.Positions) != 2 {
		t.Fatalf("another pair still trades: %+v %v", p, err)
	}
	if _, _, err := e.Close(ctx, user, "AAVE", "manual"); !errors.Is(err, market.ErrUpstream) {
		t.Fatalf("close without a quote: %v", err)
	}
	if _, err := e.protect(ctx, user, "AAVE", level{set: true, v: ptr(mark * 0.6)}, level{}, nil); !errors.Is(err, market.ErrUpstream) {
		t.Fatalf("stop edit without a quote: %v", err)
	}
}

// A halted pair keeps its last price: a position in it is valued there (its
// loss shows), and what shrinks it fills there, the watcher's closes too;
// anything that opens, adds to or flips one is refused (409 from the API),
// and a basket with such a leg fills nothing.
func TestHaltedPair(t *testing.T) {
	pool := storetest.Pool(t)
	user := storetest.User(t, pool)
	ctx := context.Background()
	bin := markettest.New(t)
	e := NewEngine(pool, testMarketClient(bin.URL))
	for _, sym := range []string{"TON", "SOL"} {
		if _, _, err := e.Order(ctx, user, OrderRequest{Symbol: sym, Side: "buy", Notional: ptr(1000)}); err != nil {
			t.Fatal(err)
		}
	}

	bin.SetPrice("TONUSDT", 4) // the last trade before the halt; bought at 5
	bin.Halt("TONUSDT", true)
	e = NewEngine(pool, testMarketClient(bin.URL)) // a fresh client: no cached ticker
	held := func(p Portfolio) Position {
		for _, pos := range p.Positions {
			if pos.Symbol == "TON" {
				return pos
			}
		}
		return Position{}
	}
	p, err := e.Portfolio(ctx, user)
	ton := held(p)
	if err != nil || ton.MarkPrice != 4 || !(ton.UnrealizedPnl < -190) || !approx(ton.UnrealizedPnl, (4-ton.AvgPrice)*ton.Qty) {
		t.Fatalf("TON at its last price: %+v %v", ton, err)
	}

	var halted *HaltedError
	for _, req := range []OrderRequest{
		{Symbol: "TON", Side: "buy", Notional: ptr(100)},       // adds
		{Symbol: "ton", Side: "sell", Qty: ptr(ton.Qty * 1.5)}, // flips to a smaller short
		{Symbol: "TON", Side: "sell", Qty: ptr(ton.Qty + 3)},   // flips to a $12 short
	} {
		if _, _, err := e.Order(ctx, user, req); !errors.As(err, &halted) || halted.Symbol != "TON" {
			t.Errorf("%+v: %v, want HaltedError", req, err)
		}
	}
	if _, _, err := e.Orders(ctx, user, []OrderRequest{{Symbol: "SOL", Side: "buy", Notional: ptr(100)},
		{Symbol: "TON", Side: "buy", Notional: ptr(100)}}); !errors.As(err, &halted) {
		t.Fatalf("basket with a halted leg: %v", err)
	}
	if w := serve(e)("POST", "/api/orders", user, `{"symbol":"TON","side":"buy","notional":50}`); w.Code != 409 || code(t, w) != "conflict" ||
		!strings.Contains(w.Body.String(), "TON is halted on Binance, so new orders are paused") {
		t.Fatalf("API: %d %s", w.Code, w.Body)
	}

	tr, p, err := e.Order(ctx, user, OrderRequest{Symbol: "TON", Side: "sell", Qty: ptr(ton.Qty / 2)})
	if err != nil || tr.Price != FillPrice(4, false) || !approx(held(p).Qty, ton.Qty/2) {
		t.Fatalf("trim: %+v %v", tr, err)
	}
	// "Sell it all" by notional at the mark overshoots by the slippage: the dust short fills.
	if _, p, err = e.Order(ctx, user, OrderRequest{Symbol: "TON", Side: "sell", Notional: ptr(held(p).Qty * 4)}); err != nil ||
		!(held(p).Qty < 0) || !(-held(p).Qty*4 < 1) {
		t.Fatalf("sell it all: %+v %v", held(p), err)
	}
	// The watcher closes on the frozen mark: a short stop at 1 is touched by 4.
	if _, err := pool.Exec(ctx, "update positions set stop_loss = 1 where user_id = $1 and symbol = 'TON'", user); err != nil {
		t.Fatal(err)
	}
	if err := e.checkStops(ctx); err != nil {
		t.Fatal(err)
	}
	if p, err = e.Portfolio(ctx, user); err != nil || held(p).Symbol != "" || len(p.Positions) != 1 {
		t.Fatalf("stop close: %+v %v", p.Positions, err)
	}
	if _, _, err := e.Order(ctx, user, OrderRequest{Symbol: "TON", Side: "sell", Notional: ptr(100)}); !errors.As(err, &halted) {
		t.Fatalf("a short from flat: %v", err)
	}

	bin.Halt("TONUSDT", false) // trading resumes
	if _, _, err := NewEngine(pool, testMarketClient(bin.URL)).Order(ctx, user, OrderRequest{Symbol: "TON", Side: "buy", Notional: ptr(100)}); err != nil {
		t.Fatalf("after the halt: %v", err)
	}
}

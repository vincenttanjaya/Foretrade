package paper

import (
	"context"
	"errors"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
	"strings"
	"testing"
	"time"
)

func TestStockHaltErrorVenue(t *testing.T) {
	if text := (&HaltedError{Symbol: "AAPL"}).Error(); strings.Contains(text, "Binance") || !strings.Contains(text, "Hyperliquid") {
		t.Fatalf("stock error falsevenue:%s", text)
	}
}

// TSMC trades like the US perps: filled at its xyz mark, by symbol or by name.
func TestAsiaPerpPaperFill(t *testing.T) {
	pool := storetest.Pool(t)
	user := storetest.User(t, pool)
	b := markettest.New(t)
	ctx := context.Background()
	e := NewEngine(pool, testMarketClient(b.URL))
	mark := markettest.Price("xyz:TSM")
	tr, p, err := e.Order(ctx, user, OrderRequest{Symbol: "TSMC", Side: "buy", Qty: ptr(2)})
	if err != nil || tr.Symbol != "TSM" || !approx(tr.Price, FillPrice(mark, true)) || !approx(tr.Fee, tr.Notional*.001) {
		t.Fatalf("TSM fill %+v %v", tr, err)
	}
	if len(p.Positions) != 1 || p.Positions[0].Symbol != "TSM" || p.Positions[0].Qty != 2 || p.Positions[0].MarkPrice != mark {
		t.Fatalf("TSM position %+v", p.Positions)
	}
	if tr, p, err = e.Order(ctx, user, OrderRequest{Symbol: "tsm", Side: "sell", Qty: ptr(2)}); err != nil || !approx(tr.Price, FillPrice(mark, false)) || len(p.Positions) != 0 {
		t.Fatalf("TSM close %+v %+v %v", tr, p.Positions, err)
	}
}

func TestStockSignedPaperAccountingAndAtomicBasket(t *testing.T) {
	pool := storetest.Pool(t)
	user := storetest.User(t, pool)
	b := markettest.New(t)
	ctx := context.Background()
	e := NewEngine(pool, testMarketClient(b.URL))
	mark := markettest.Price("xyz:AAPL")
	for _, side := range []string{"buy", "sell"} {
		tr, p, err := e.Order(ctx, user, OrderRequest{Symbol: "AAPL", Side: side, Qty: ptr(2)})
		if err != nil {
			t.Fatal(err)
		}
		if !approx(tr.Price, FillPrice(mark, side == "buy")) || !approx(tr.Fee, tr.Notional*.001) {
			t.Fatalf("perp fills/fees:%+v", tr)
		}
		if side == "buy" && (len(p.Positions) != 1 || p.Positions[0].Qty != 2 || p.Positions[0].MarkPrice != mark) {
			t.Fatalf("long valuation%+v", p)
		}
		if side == "sell" && len(p.Positions) != 0 {
			t.Fatalf("close long%+v", p)
		}
	}
	tr, p, err := e.Order(ctx, user, OrderRequest{Symbol: "AAPL", Side: "sell", Qty: ptr(3)})
	if err != nil || len(p.Positions) != 1 || p.Positions[0].Qty != -3 || tr.Price != FillPrice(mark, false) {
		t.Fatalf("signedshort %+v %+v %v", tr, p, err)
	}
	_, p, err = e.Order(ctx, user, OrderRequest{Symbol: "AAPL", Side: "buy", Qty: ptr(1)})
	if err != nil || p.Positions[0].Qty != -2 {
		t.Fatalf("reduce short%+v %v", p, err)
	}
	_, p, err = e.Order(ctx, user, OrderRequest{Symbol: "AAPL", Side: "buy", Qty: ptr(2)})
	if err != nil || len(p.Positions) != 0 {
		t.Fatalf("close short%+v %v", p, err)
	}
	before, _ := e.Portfolio(ctx, user)
	b.Delist("xyz:AAPL", true)
	e = NewEngine(pool, testMarketClient(b.URL))
	if _, _, err := e.Orders(ctx, user, []OrderRequest{{Symbol: "BTC", Side: "buy", Qty: ptr(.01)}, {Symbol: "AAPL", Side: "buy", Qty: ptr(1)}}); !errors.Is(err, market.ErrUpstream) {
		t.Fatalf("missing mark basket%v", err)
	}
	after, _ := e.Portfolio(ctx, user)
	if after.Cash != before.Cash || len(after.Positions) != 0 {
		t.Fatalf("mixedbasket partiallyfilled%+v", after)
	}
	b.Delist("xyz:AAPL", false)
	e = NewEngine(pool, testMarketClient(b.URL))
	if trs, _, err := e.Orders(ctx, user, []OrderRequest{{Symbol: "BTC", Side: "buy", Qty: ptr(.01)}, {Symbol: "AAPL", Side: "buy", Qty: ptr(1)}}); err != nil || len(trs) != 2 {
		t.Fatalf("mixedbasket%+v %v", trs, err)
	}
	// A known weekend is a regular-US-session closure, never a paper halt.
	weekend := time.Date(2026, 10, 3, 15, 0, 0, 0, time.UTC)
	if open, _ := market.USSession(weekend); open {
		t.Fatal("weekend calendar open")
	}
	if _, _, err := e.Order(ctx, user, OrderRequest{Symbol: "AAPL", Side: "buy", Qty: ptr(1)}); err != nil {
		t.Fatalf("continuousperp order%v", err)
	}
	if _, err := pool.Exec(ctx, "update positions set stop_loss=1000 where user_id=$1 and symbol='AAPL'", user); err != nil {
		t.Fatal(err)
	}
	if err := e.checkStops(ctx); err != nil {
		t.Fatal(err)
	}
	trs, _ := e.Trades(ctx, user, 1)
	if len(trs) != 1 || trs[0].Symbol != "AAPL" || trs[0].Source != "stop_loss" || trs[0].Price != FillPrice(mark, false) {
		t.Fatalf("stockstop%+v", trs)
	}
}

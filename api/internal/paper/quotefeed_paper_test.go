package paper

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo/yahootest"
)

// feedEngine is an engine whose market prices DBS (SGD) from a Yahoo fake
// quoted at quoted.
func feedEngine(t *testing.T, pool *pgxpool.Pool, b *markettest.Binance, quoted time.Time) *Engine {
	t.Helper()
	f := yahootest.New()
	dbs := yahootest.Daily("D05.SI", "SGD", 77.83, 78.37)
	dbs.Meta.PriceTime = &quoted
	f.Set(dbs)
	f.SetFX("SGD", 0.78)
	m := testMarketClient(b.URL)
	m.Yahoo = f
	return NewEngine(pool, m)
}

// A quote-feed stock fills at its USD mark (local price × FX) with the usual
// slippage and fee, and is valued there.
func TestQuoteFeedPaperFill(t *testing.T) {
	pool := storetest.Pool(t)
	user := storetest.User(t, pool)
	b := markettest.New(t)
	ctx := context.Background()
	e := feedEngine(t, pool, b, time.Now())
	mark := 78.37 * 0.78
	tr, p, err := e.Order(ctx, user, OrderRequest{Symbol: "dbs", Side: "buy", Qty: ptr(10)})
	if err != nil || tr.Symbol != "DBS" || !approx(tr.Price, FillPrice(mark, true)) || !approx(tr.Fee, tr.Notional*FeeRate) {
		t.Fatalf("DBS fill %+v %v", tr, err)
	}
	if len(p.Positions) != 1 || !approx(p.Positions[0].MarkPrice, mark) {
		t.Fatalf("DBS position %+v", p.Positions)
	}

	// Days later the quote is stale: no order fills, not even the close.
	e = feedEngine(t, pool, b, time.Now().Add(-market.QuoteStaleAfter-time.Hour))
	for _, side := range []string{"buy", "sell"} {
		_, _, err := e.Order(ctx, user, OrderRequest{Symbol: "DBS", Side: side, Qty: ptr(10)})
		var halted *HaltedError
		if !errors.As(err, &halted) || !strings.Contains(err.Error(), "has no fresh quote (none from the last 4 days") {
			t.Fatalf("stale %s: %v", side, err)
		}
	}
	if p, err := e.Portfolio(ctx, user); err != nil || len(p.Positions) != 1 || !approx(p.Positions[0].MarkPrice, mark) {
		t.Fatalf("a stale quote still values the position: %+v %v", p, err)
	}

	// No quote at all is a market-data outage.
	if _, _, err := e.Order(ctx, user, OrderRequest{Symbol: "TENCENT", Side: "buy", Qty: ptr(1)}); !errors.Is(err, market.ErrUpstream) {
		t.Fatalf("unquoted: %v", err)
	}
}

// Over an exchange holiday longer than 4 days (KRX's year-end closure) a
// quote-feed position still closes, at the last close; once KRX has traded
// half an hour without a fresh quote, it is paused as before (gap round G4).
func TestQuoteFeedPaperHolidayClose(t *testing.T) {
	pool := storetest.Pool(t)
	user := storetest.User(t, pool)
	b := markettest.New(t)
	ctx := context.Background()
	seoul, _ := time.LoadLocation("Asia/Seoul")
	closed := time.Date(2026, 12, 30, 15, 30, 0, 0, seoul)
	engine := func(now time.Time) *Engine {
		f := yahootest.New()
		naver := yahootest.Daily("035420.KS", "KRW", 198000, 200000)
		naver.Meta.PriceTime, naver.Meta.Timezone = &closed, "Asia/Seoul"
		f.Set(naver)
		f.SetFX("KRW", 0.0007)
		m := testMarketClient(b.URL)
		m.Yahoo, m.Now = f, func() time.Time { return now }
		return NewEngine(pool, m)
	}
	mark := 200000 * 0.0007
	if _, _, err := engine(closed.Add(time.Hour)).Order(ctx, user, OrderRequest{Symbol: "NAVER", Side: "buy", Qty: ptr(2)}); err != nil {
		t.Fatal(err)
	}
	tr, p, err := engine(time.Date(2027, 1, 2, 12, 0, 0, 0, seoul)).Order(ctx, user, OrderRequest{Symbol: "NAVER", Side: "sell", Qty: ptr(2)})
	if err != nil || !approx(tr.Price, FillPrice(mark, false)) || len(p.Positions) != 0 {
		t.Fatalf("holiday close: %+v %+v %v", tr, p.Positions, err)
	}
	var halted *HaltedError
	if _, _, err := engine(time.Date(2027, 1, 4, 10, 0, 0, 0, seoul)).Order(ctx, user, OrderRequest{Symbol: "NAVER", Side: "buy", Qty: ptr(1)}); !errors.As(err, &halted) {
		t.Fatalf("after the open with no fresh quote: %v", err)
	}
}

// A listed row nobody has read yet fills at a fresh Yahoo read instead of
// "no live price"; a named feed's quote over a minute old is read again
// before its fill.
func TestListedFillFreshQuote(t *testing.T) {
	pool := storetest.Pool(t)
	user := storetest.User(t, pool)
	b := markettest.New(t)
	ctx := context.Background()
	f := yahootest.New()
	now := time.Now().UTC().Truncate(time.Second)
	h := yahootest.Daily("6501.T", "JPY", 4800, 4850)
	h.Meta.PriceTime = &now
	f.Set(h)
	f.SetFX("JPY", 0.0066)
	m := testMarketClient(b.URL)
	m.Yahoo = f
	e := NewEngine(pool, m)
	tr, p, err := e.Order(ctx, user, OrderRequest{Symbol: "6501.t", Side: "buy", Qty: ptr(100)})
	if err != nil || tr.Symbol != "6501.T" || !approx(tr.Price, FillPrice(4850*0.0066, true)) || len(p.Positions) != 1 {
		t.Fatalf("listed fill %+v %v", tr, err)
	}
	reads := f.Calls("6501.T")
	h2 := yahootest.Daily("6501.T", "JPY", 4800, 4900)
	h2.Meta.PriceTime = &now
	f.Set(h2)
	if tr, _, err := e.Order(ctx, user, OrderRequest{Symbol: "6501.T", Side: "sell", Qty: ptr(50)}); err != nil || f.Calls("6501.T") != reads ||
		!approx(tr.Price, FillPrice(4850*0.0066, false)) {
		t.Fatalf("a read under a minute old is reused: %+v %v (%d reads)", tr, err, f.Calls("6501.T"))
	}
	// Research-only and unserved tickers have no price.
	if _, _, err := e.Order(ctx, user, OrderRequest{Symbol: "7011.T", Side: "buy", Qty: ptr(1)}); !errors.Is(err, market.ErrUpstream) {
		t.Fatalf("unserved listed: %v", err)
	}
	if _, _, err := e.Order(ctx, user, OrderRequest{Symbol: "MER.PS", Side: "buy", Qty: ptr(1)}); err == nil || errors.Is(err, market.ErrUpstream) {
		t.Fatalf("research-only: %v", err)
	}
}

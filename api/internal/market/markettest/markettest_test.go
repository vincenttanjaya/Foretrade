package markettest_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
)

func TestFakeXYZContextsAndCandles(t *testing.T) {
	b := markettest.New(t)
	r, err := http.Post(b.URL+"/info", "application/json", strings.NewReader(`{"type":"metaAndAssetCtxs","dex":"xyz"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	var body []json.RawMessage
	json.NewDecoder(r.Body).Decode(&body)
	var meta struct{ Universe []struct{ Name string } }
	json.Unmarshal(body[0], &meta)
	if len(meta.Universe) != 38 || meta.Universe[0].Name != "xyz:NVDA" || meta.Universe[8].Name != "xyz:XYZ100" || meta.Universe[18].Name != "xyz:EWY" || meta.Universe[37].Name != "xyz:KSTR" {
		t.Fatalf("xyz metadata %+v", meta)
	}
	c := testMarketClient(b.URL)
	q, err := c.Quote(context.Background(), "AAPL")
	if err != nil || q.Price != markettest.Price("xyz:AAPL") || q.Halted {
		t.Fatalf("AAPL quote %+v %v", q, err)
	}
	cs, err := c.Candles(context.Background(), "AAPL", "1d", 10)
	if err != nil || len(cs) == 0 || len(cs) > 10 || cs[len(cs)-1].T != markettest.End.Unix() {
		t.Fatalf("xyz candles %+v %v", cs, err)
	}
	b.Down("xyz:AAPL", true)
	if _, err := testMarketClient(b.URL).Quote(context.Background(), "AAPL"); !errors.Is(err, market.ErrUpstream) {
		t.Fatalf("missing xyz quote %v", err)
	}
	if _, err := testMarketClient(b.URL).Quote(context.Background(), "BTC"); err != nil {
		t.Fatalf("xyz must not break Binance %v", err)
	}
}

// The fake must speak what the real client reads: tickers for the whole
// universe (live unless halted), klines paged backwards by endTime, and
// outages as ErrUpstream.
func TestFakeBinanceThroughClient(t *testing.T) {
	b := markettest.New(t)
	ctx := context.Background()

	assets, err := testMarketClient(b.URL).Assets(ctx)
	if err != nil || len(assets) != len(market.Universe)-len(market.QuoteFeeds) || assets[0].Price != markettest.Price("BTCUSDT") ||
		slices.ContainsFunc(assets, func(a market.Asset) bool { return a.Halted }) {
		t.Fatalf("assets: %v %+v", err, assets)
	}
	if markettest.Price("NOPEUSDT") != 10 || markettest.Close("BTCUSDT", 0) != markettest.Price("BTCUSDT") {
		t.Fatal("newest close must equal the ticker price; unknown pairs default to 10")
	}

	bars, err := testMarketClient(b.URL).Candles(ctx, "ETH", "4h", 2500) // three pages
	if err != nil || len(bars) != 2500 {
		t.Fatalf("candles: %v, %d bars", err, len(bars))
	}
	for i := 1; i < len(bars); i++ {
		if bars[i].T-bars[i-1].T != 4*3600 {
			t.Fatalf("bars %d/%d not contiguous: %d → %d", i-1, i, bars[i-1].T, bars[i].T)
		}
	}
	if last := bars[len(bars)-1]; last.T != markettest.End.Unix() || last.C != markettest.Price("ETHUSDT") || !(last.L < last.H) {
		t.Fatalf("newest bar %+v", last)
	}

	b.SetPrice("BTCUSDT", 101_500)
	if assets, err := testMarketClient(b.URL).Assets(ctx); err != nil || assets[0].Price != 101_500 || assets[1].Price != markettest.Price("ETHUSDT") {
		t.Fatalf("moved BTC: %v %+v", err, assets[:2])
	}
	if bars, err := testMarketClient(b.URL).Candles(ctx, "BTC", "1d", 2); err != nil || bars[1].C != markettest.Price("BTCUSDT") {
		t.Fatalf("klines keep their series after SetPrice: %v %+v", err, bars)
	}

	b.Halt("TONUSDT", true)
	if q, err := testMarketClient(b.URL).Quote(ctx, "TON"); err != nil || !q.Halted || q.Price != markettest.Price("TONUSDT") {
		t.Fatalf("halted TON keeps its last price: %v %+v", err, q)
	}
	b.Halt("TONUSDT", false)

	b.Down("SOLUSDT", true)
	if _, err := testMarketClient(b.URL).Candles(ctx, "SOL", "1d", 10); !errors.Is(err, market.ErrUpstream) {
		t.Fatalf("down pair klines: %v", err)
	}
	if _, err := testMarketClient(b.URL).Assets(ctx); !errors.Is(err, market.ErrUpstream) {
		t.Fatalf("a pair missing from the ticker: %v", err)
	}
	b.Down("SOLUSDT", false)
	if _, err := testMarketClient(b.URL + "/nope").Assets(ctx); !errors.Is(err, market.ErrUpstream) {
		t.Fatalf("unknown path: %v", err)
	}
	b.Down("", true)
	if _, err := testMarketClient(b.URL).Candles(ctx, "BTC", "1h", 10); !errors.Is(err, market.ErrUpstream) {
		t.Fatalf("everything down: %v", err)
	}
}

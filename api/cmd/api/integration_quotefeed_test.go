package main

import (
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo/yahootest"
)

// quoteFeedKeys is the contract's QuoteFeed.
const quoteFeedKeys = "symbol exchange currency localPrice previousClose fx fxSymbol fxAsOf asOf marketState stale " +
	"source class lastTradeAt readAt delaySeconds timeBasis label"

// TestIntegrationQuoteFeed: quote-feed stocks come from YAHOO_BASE through the
// real client (spark batches, FX pairs): listed with their quote block,
// charted in USD, and filled at the USD mark like any paper order.
func TestIntegrationQuoteFeed(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	// served from boot: the boot's first refresh may read them before a test
	// could set them, and the client caches a not-found for 15 minutes
	s := boot(t, options{yahoo: func(f *yahootest.Fake) {
		dbs := yahootest.Daily("D05.SI", "SGD", 76, 77.83, 78.37)
		prev := 77.83
		dbs.Meta.PriceTime, dbs.Meta.ChartPreviousClose, dbs.Meta.Timezone = &now, &prev, "Asia/Singapore"
		f.Set(dbs)
		hsdt := yahootest.Daily("HSDT", "USD", 2.5, 2.65)
		hsdt.Meta.PriceTime = &now
		f.Set(hsdt)
		f.SetFX("SGD", 0.78)
	}})
	c := s.signIn(t)

	assets := c.call("GET", "/api/market/assets", nil, 200)
	rows := list(t, assets, "assets")
	n := len(market.Universe) - len(market.QuoteFeeds) // the crypto and perp rows
	if len(rows) != n+2 || str(t, rows[n], "symbol") != "HSDT" || str(t, rows[n+1], "symbol") != "DBS" {
		t.Fatalf("quote feeds after the perps: %d rows", len(rows))
	}
	hsdtRow, dbsRow := rows[n], rows[n+1]
	shape(t, dbsRow, "", assetKeys+" quoteFeed")
	shape(t, dbsRow, "quoteFeed", quoteFeedKeys)
	if str(t, dbsRow, "quoteFeed.source") != "yahoo" || str(t, dbsRow, "quoteFeed.class") != "delayed" ||
		str(t, dbsRow, "quoteFeed.timeBasis") != "trade" || !strings.HasPrefix(str(t, dbsRow, "quoteFeed.label"), "delayed · last trade ") ||
		!isTime(t, dbsRow, "quoteFeed.lastTradeAt").Equal(now) || isTime(t, dbsRow, "quoteFeed.readAt").Before(now) {
		t.Fatalf("DBS quote feed provenance %v", at(t, dbsRow, "quoteFeed"))
	}
	if str(t, dbsRow, "venue") != "yahoo" || str(t, dbsRow, "pair") != "D05.SI" || !near(num(t, dbsRow, "price"), 78.37*0.78) ||
		num(t, dbsRow, "quoteFeed.localPrice") != 78.37 || str(t, dbsRow, "quoteFeed.fxSymbol") != "SGDUSD=X" ||
		str(t, dbsRow, "sessionVenue") != "SGX" || str(t, dbsRow, "localCurrency") != "SGD" || num(t, dbsRow, "change24hPct") != 0.694 {
		t.Fatalf("DBS row %v", dbsRow)
	}
	if str(t, hsdtRow, "region") != "us" || num(t, hsdtRow, "price") != 2.65 || num(t, hsdtRow, "quoteFeed.fx") != 1 {
		t.Fatalf("HSDT row %v", hsdtRow)
	}

	candles := list(t, c.call("GET", "/api/market/candles?symbol=DBS&interval=1d&limit=3", nil, 200), "candles")
	if len(candles) != 3 || !near(num(t, candles[2], "c"), 78.37*0.78) || num(t, candles[2], "t") != float64(time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC).Unix()) {
		t.Fatalf("DBS candles %v", candles)
	}
	fill := c.call("POST", "/api/orders", map[string]any{"symbol": "DBS", "side": "buy", "qty": 10}, 200)
	if str(t, fill, "trade.symbol") != "DBS" || !near(num(t, fill, "trade.price"), paper.FillPrice(78.37*0.78, true)) {
		t.Fatalf("DBS fill %v", fill)
	}
	// A listing Yahoo does not serve is left out; its order is a market-data outage.
	c.call("POST", "/api/orders", map[string]any{"symbol": "TENCENT", "side": "buy", "qty": 1}, 502)
}

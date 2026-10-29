package main

import (
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
)

// End-to-end characterization of the selected Universe behaviors proved by
// permanent unit/handler REDs: real server wiring, fake venues, signed paper
// fills and saved generic portfolio benchmark fields. No paid/live inference.
func TestIntegrationUniverse(t *testing.T) {
	s := boot(t, options{model: func(r llmtest.Request) llmtest.Reply {
		if !strings.HasPrefix(r.System, "You build mixed crypto and stock/index perp portfolios") {
			t.Errorf("unexpected model call %q", r.System)
			return llmtest.Reply{Status: 400}
		}
		return llmtest.Reply{Text: `{"name":"Mixed stocks","thesis":"Paper mixed venues.","holdings":[{"symbol":"AAPL","weight":0.4},{"symbol":"META","weight":0.2},{"symbol":"BTC","weight":0.4}]}`}
	}})
	c := s.signIn(t)
	assets := c.call("GET", "/api/market/assets", nil, 200)
	rows := list(t, assets, "assets")
	shapeAll(t, assets, "assets", assetKeys)
	if len(rows) != 107 {
		t.Fatalf("configured mixed assets %d", len(rows))
	}
	for i, a := range market.Universe[:len(market.Universe)-len(market.QuoteFeeds)] { // the empty Yahoo fake prices no quote feed
		if str(t, rows[i], "symbol") != a.Symbol || str(t, rows[i], "pair") != a.Pair || str(t, rows[i], "class") != a.Class || str(t, rows[i], "venue") != a.Venue {
			t.Fatalf("catalog row%d %v", i, rows[i])
		}
		if a.Class == market.Crypto {
			for _, key := range []string{"session", "sessionChangeAt", "region", "sessionVenue", "underlying", "localCurrency"} {
				if !isNull(t, rows[i], key) {
					t.Fatalf("crypto has %s %v", key, rows[i])
				}
			}
		} else {
			if str(t, rows[i], "sessionVenue") != a.Listing.SessionVenue || str(t, rows[i], "region") != a.Listing.Region || str(t, rows[i], "localCurrency") != a.Listing.LocalCurrency {
				t.Fatalf("perp listing %v", rows[i])
			}
			// Every venue calendar is verified through 2027 (NYSE through 2028),
			// except SSE's, published for 2026 only: unknown (null) after it.
			if a.Listing.SessionVenue == market.SSE && time.Now().Year() > 2026 {
				if !isNull(t, rows[i], "session") {
					t.Fatalf("unverified SSE session %v", rows[i])
				}
			} else if session := str(t, rows[i], "session"); session != "open" && session != "closed" {
				t.Fatalf("known venue calendar session %v", rows[i])
			}
		}
	}
	candles := list(t, c.call("GET", "/api/market/candles?symbol=AAPL&interval=1d&limit=10", nil, 200), "candles")
	if len(candles) == 0 || len(candles) > 10 || num(t, candles[len(candles)-1], "t") != float64(markettest.End.Unix()) {
		t.Fatalf("actual retained perp history %v", candles)
	}
	mark := markettest.Price("xyz:AAPL")
	for _, order := range []struct {
		side string
		qty  float64
	}{{"buy", 2}, {"sell", 2}, {"sell", 3}, {"buy", 1}, {"buy", 2}} {
		fill := c.call("POST", "/api/orders", map[string]any{"symbol": "AAPL", "side": order.side, "qty": order.qty}, 200)
		if !near(num(t, fill, "trade.price"), paper.FillPrice(mark, order.side == "buy")) || !near(num(t, fill, "trade.fee"), num(t, fill, "trade.notional")*.001) {
			t.Fatalf("signed stock fill %v", fill)
		}
	}
	// TSMC, an Asian perp, fills like any stock perp, and its everyday name resolves.
	tsm := c.call("POST", "/api/orders", map[string]any{"symbol": "TSMC", "side": "buy", "qty": 1}, 200)
	if str(t, tsm, "trade.symbol") != "TSM" || !near(num(t, tsm, "trade.price"), paper.FillPrice(markettest.Price("xyz:TSM"), true)) {
		t.Fatalf("TSM fill %v", tsm)
	}
	c.call("POST", "/api/orders", map[string]any{"symbol": "TSM", "side": "sell", "qty": 1}, 200)
	if positions := list(t, c.call("GET", "/api/portfolio", nil, 200), "portfolio.positions"); len(positions) != 0 {
		t.Fatalf("short reduction/closure %v", positions)
	}
	build := c.call("POST", "/api/ai-portfolios/build", map[string]string{"prompt": "stock majority mixed universe"}, 201)
	if str(t, build, "portfolio.backtest.benchmarkSymbol") != "SP500" || str(t, build, "portfolio.backtest.benchmarkLabel") != "S&P 500 perp" || num(t, build, "portfolio.backtest.bars") < 31 {
		t.Fatalf("saved stock benchmark %v", build)
	}
	id := str(t, build, "portfolio.id")
	if n := s.count(t, "select count(*) from ai_portfolios where id=$1 and user_id=$2 and backtest->>'benchmarkSymbol'='SP500'", id, c.id); n != 1 {
		t.Fatal("benchmark identity not persisted")
	}
	for range 2 {
		c.call("POST", "/api/ai-portfolios/"+id+"/invest", map[string]float64{"amount": 1000}, 200)
	}
	detail := c.call("GET", "/api/ai-portfolios/"+id, nil, 200)
	inv := at(t, detail, "portfolio.invested")
	if str(t, inv, "benchmarkSymbol") != "SP500" || num(t, inv, "amount") != 2000 || num(t, inv, "trades") != 6 || !isNull(t, inv, "btcReturnPct") || !isNull(t, inv, "btcSeries") || !isNull(t, inv, "benchmarkReturnPct") {
		t.Fatalf("generic saved-choice topup/retention %v", inv)
	}
	// This fake's newest history predates these actual current-time fills, so its
	// since-investment comparison is correctly unavailable, never a shortened claim.
	listRows := list(t, c.call("GET", "/api/ai-portfolios", nil, 200), "portfolios")
	if len(listRows) != 1 || str(t, listRows[0], "backtest.benchmarkSymbol") != "SP500" || !isNull(t, listRows[0], "invested.benchmarkSeries") {
		t.Fatalf("list saved choice %v", listRows)
	}
}

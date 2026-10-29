package main

import (
	"context"
	"strings"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/alerts"
	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo/yahootest"
)

const (
	athKeys      = "symbol ath athAt athLive coveredFrom source sourceLabel ticker currency basis price fromAthPct"
	drawdownKeys = "pct sellPct ath athAt athLive coveredFrom source sourceLabel ticker currency basis threshold outcome price fromAthPct"
)

// Drawdown triggers over HTTP: the high a USD perp reads from its Yahoo
// listing (YAHOO_BASE's fake), a sell trigger on a paper long that a check
// at its threshold fires once (run here directly; the server's 15 s loop may
// win the claim), selling the long with a ref back to it, and the bell item.
func TestIntegrationDrawdownAlerts(t *testing.T) {
	s := boot(t, options{})
	c := s.signIn(t)
	s.yahoo.Set(yahootest.Daily("NVDA", "USD", 100, 250, 180))
	q := c.call("GET", "/api/alerts/ath?symbol=NVDA", nil, 200)
	shape(t, q, "", "ath")
	shape(t, q, "ath", athKeys)
	isTime(t, q, "ath.athAt")
	if num(t, q, "ath.ath") != 252.5 || str(t, q, "ath.source") != "yahoo" || str(t, q, "ath.basis") != "mark" || num(t, q, "ath.price") != 235 ||
		!strings.HasPrefix(str(t, q, "ath.sourceLabel"), "Yahoo Finance NVDA lifetime daily highs since ") {
		t.Fatalf("NVDA high %v", q)
	}
	c.fail("GET", "/api/alerts/ath?symbol=TSLA", nil, 502, "upstream") // no Yahoo history for TSLA here

	c.call("POST", "/api/orders", map[string]any{"symbol": "SOL", "side": "buy", "notional": 1000}, 200)
	a := c.call("POST", "/api/alerts", map[string]any{"symbol": "SOL", "trigger": "drawdown", "drawdownPct": 10, "action": "sell", "note": "Cut it"}, 201)
	shape(t, a, "alert", alertKeys)
	shape(t, a, "alert.drawdown", drawdownKeys)
	id, level := str(t, a, "alert.id"), num(t, a, "alert.drawdown.threshold")
	if str(t, a, "alert.trigger") != "drawdown" || num(t, a, "alert.drawdown.sellPct") != 100 || str(t, a, "alert.drawdown.source") != "binance" ||
		!isNull(t, a, "alert.drawdown.outcome") || !isNull(t, a, "alert.op") || num(t, a, "alert.drawdown.price") != 200 {
		t.Fatalf("created %v", a)
	}
	c.fail("POST", "/api/alerts", map[string]any{"symbol": "SOL", "trigger": "drawdown", "drawdownPct": 100}, 400, "bad_request")

	s.binance.SetPrice("SOLUSDT", level)
	mkt := market.NewClient(s.binance.URL) // a cold cache sees the new price
	if err := alerts.Check(context.Background(), &app.Deps{DB: s.db, Market: mkt, Paper: paper.NewEngine(s.db, mkt)}); err != nil {
		t.Fatal(err)
	}
	l := c.call("GET", "/api/alerts", nil, 200)
	shapeAll(t, l, "alerts", alertKeys)
	if str(t, l, "alerts.0.status") != "fired" || str(t, l, "alerts.0.drawdown.outcome") != "sold" || num(t, l, "alerts.0.firedPrice") != level {
		t.Fatalf("after the check: %v", l)
	}
	newest := at(t, c.call("GET", "/api/portfolio/trades?limit=1", nil, 200), "trades.0")
	if str(t, newest, "source") != "alert" || str(t, newest, "side") != "sell" || str(t, newest, "ref.kind") != "alert" || str(t, newest, "ref.id") != id ||
		!strings.HasPrefix(str(t, newest, "ref.label"), "Drawdown alert: SOL 10% below ATH $") {
		t.Fatalf("sale %v", newest)
	}
	if s.count(t, "select count(*) from trades where ref_id = $1", id) != 1 || s.count(t, "select count(*) from positions") != 0 {
		t.Fatal("sold more than once, or the long is left")
	}
	n := c.call("GET", "/api/notifications", nil, 200)
	shapeAll(t, n, "items", notificationKeys)
	if str(t, n, "items.0.kind") != "drawdown_order" || str(t, n, "items.0.title") != "Drawdown sell filled · SOL" || str(t, n, "items.0.link") != "/account" {
		t.Fatalf("bell %v", n)
	}
}

package main

import (
	"math"
	"strings"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
)

// The trade desk: every fill links back to its origin (an AI portfolio's
// legs, an agent card's chat; an alert's in integration_alerts_test.go), an invested portfolio reports its paper P&L,
// the agent's protection card is applied by PATCH (revision bumped), and the
// perps row reads Hyperliquid (the harness stub).
func TestIntegrationTradeDesk(t *testing.T) {
	s := boot(t, options{model: func(r llmtest.Request) llmtest.Reply {
		switch {
		case strings.HasPrefix(r.System, "You build mixed crypto and stock/index perp portfolios"):
			return llmtest.Reply{Text: basketReply}
		case r.Prompt == "Buy BTC?":
			return llmtest.Reply{Tools: []llmtest.ToolUse{{ID: "t1", Name: "propose_trade",
				Input: `{"symbol":"BTC","side":"buy","notional_usd":1500,"rationale":"Trend intact."}`}}}
		case r.Prompt == "Set stops on my BTC":
			return llmtest.Reply{Tools: []llmtest.ToolUse{{ID: "t2", Name: "set_protection",
				Input: `{"symbol":"BTC","stop_loss":95000,"take_profit":110000,"rationale":"Under the swing low."}`}}}
		case r.AfterTools: // a tool result came back
			return llmtest.Reply{Text: "**View:** bullish above $95k."}
		}
		t.Errorf("unexpected model call %.60q / %q", r.System, r.Prompt)
		return llmtest.Reply{Status: 400}
	}})
	c := s.signIn(t)

	// Invest: every leg links to the portfolio, which then reports its position.
	id := str(t, c.call("POST", "/api/ai-portfolios/build", map[string]string{"prompt": "Blue chips"}, 201), "portfolio.id")
	if !isNull(t, c.call("GET", "/api/ai-portfolios/"+id, nil, 200), "portfolio.invested") {
		t.Fatal("invested before investing")
	}
	c.call("POST", "/api/ai-portfolios/"+id+"/invest", map[string]float64{"amount": 1000}, 200)
	trades := c.call("GET", "/api/portfolio/trades", nil, 200)
	shapeAll(t, trades, "trades", tradeKeys)
	for _, tr := range list(t, trades, "trades") {
		shape(t, tr, "ref", "kind id label")
		if str(t, tr, "ref.kind") != "portfolio" || str(t, tr, "ref.id") != id || str(t, tr, "ref.label") != "Core Majors" {
			t.Fatalf("invest leg ref: %v", tr)
		}
	}
	p := c.call("GET", "/api/ai-portfolios/"+id, nil, 200)
	shape(t, p, "portfolio.invested", "amount trades firstAt realizedPnl unrealizedPnl totalPnl returnPct btcReturnPct series btcSeries benchmarkSymbol benchmarkLabel benchmarkReturnPct benchmarkSeries windowStart windowEnd bars")
	inv := at(t, p, "portfolio.invested")
	isTime(t, inv, "firstAt")
	if !near(num(t, inv, "amount"), 1000) || num(t, inv, "trades") != 3 || num(t, inv, "returnPct") >= 0 ||
		math.Abs(num(t, inv, "returnPct")-num(t, inv, "totalPnl")/1000*100) > 0.011 { // both rounded to cents
		t.Fatalf("invested: %v", inv)
	}
	if l := list(t, c.call("GET", "/api/ai-portfolios", nil, 200), "portfolios"); isNull(t, l[0], "invested") {
		t.Fatalf("list without invested: %v", l)
	}

	// An agent card's fill links to its chat.
	ev := c.stream(map[string]any{"message": "Buy BTC?"})
	chatID := str(t, ev[0].data, "chatId")
	var pid string
	for _, e := range ev {
		if e.name == "card" && str(t, e.data, "type") == "trade_proposal" {
			pid = str(t, e.data, "data.proposalId")
		}
	}
	c.call("POST", "/api/orders", map[string]any{"symbol": "BTC", "side": "buy", "notional": 1500, "source": "agent", "proposalId": pid}, 200)
	newest := at(t, c.call("GET", "/api/portfolio/trades?limit=1", nil, 200), "trades.0")
	if str(t, newest, "ref.kind") != "chat" || str(t, newest, "ref.id") != chatID || str(t, newest, "ref.label") != "Buy BTC?" {
		t.Fatalf("agent fill ref: %v", newest)
	}
	// "alert" is a trade source (Automations); only an alert's own fill links back, its ref_id is server-only.
	o := c.call("POST", "/api/orders", map[string]any{"symbol": "ETH", "side": "buy", "notional": 50, "source": "alert"}, 200)
	if str(t, o, "trade.source") != "alert" || !isNull(t, at(t, c.call("GET", "/api/portfolio/trades?limit=1", nil, 200), "trades.0"), "ref") {
		t.Fatalf("alert-source order: %v", o)
	}

	// "Set stops on my BTC": a protection card, never a trade card; Confirm is the PATCH.
	ev = c.stream(map[string]any{"message": "Set stops on my BTC"})
	var card any
	for _, e := range ev {
		if e.name == "card" {
			if str(t, e.data, "type") != "protection" {
				t.Fatalf("card %v", e.data)
			}
			card = at(t, e.data, "data")
		}
	}
	shape(t, card, "", "symbol side qty markPrice current proposed rationale")
	shape(t, card, "proposed", "stopLoss takeProfit")
	if str(t, card, "side") != "long" || num(t, card, "proposed.stopLoss") != 95000 || !isNull(t, card, "current.stopLoss") {
		t.Fatalf("protection card: %v", card)
	}
	before := num(t, c.call("GET", "/api/portfolio", nil, 200), "portfolio.revision")
	pf := c.call("PATCH", "/api/portfolio/positions/BTC", map[string]float64{"stopLoss": 95000, "takeProfit": 110000}, 200)
	shape(t, pf, "", "portfolio")
	shape(t, pf, "portfolio", portfolioKeys)
	var btc any
	for _, pos := range list(t, pf, "portfolio.positions") {
		if str(t, pos, "symbol") == "BTC" {
			btc = pos
		}
	}
	if num(t, pf, "portfolio.revision") != before+1 || num(t, btc, "stopLoss") != 95000 || num(t, btc, "takeProfit") != 110000 {
		t.Fatalf("after PATCH: %v", pf)
	}
	c.fail("PATCH", "/api/portfolio/positions/BTC", map[string]float64{"stopLoss": 101000}, 400, "bad_request")
	c.fail("PATCH", "/api/portfolio/positions/BTC", map[string]any{}, 400, "bad_request")
	c.fail("PATCH", "/api/portfolio/positions/DOGE", map[string]float64{"stopLoss": 0.1}, 404, "not_found")
	c.fail("PATCH", "/api/portfolio/positions/PEPE", map[string]float64{"stopLoss": 1}, 400, "bad_request")
	if s.count(t, "select count(*) from positions where user_id = $1 and symbol = 'BTC' and stop_loss = 95000 and take_profit = 110000", c.id) != 1 {
		t.Fatal("stored levels")
	}

	// Perps context from the Hyperliquid stub.
	d := c.call("GET", "/api/market/derivatives?symbol=BTC", nil, 200)
	shape(t, d, "derivatives", "symbol venue markPrice fundingRatePct fundingAnnualizedPct openInterestUsd premiumPct volume24hUsd asOf")
	isTime(t, d, "derivatives.asOf")
	if str(t, d, "derivatives.venue") != "Hyperliquid" || !near(num(t, d, "derivatives.openInterestUsd"), 1.2e9) ||
		!near(num(t, d, "derivatives.fundingAnnualizedPct"), num(t, d, "derivatives.fundingRatePct")*24*365) {
		t.Fatalf("derivatives: %v", d)
	}
	if !isNull(t, c.call("GET", "/api/market/derivatives?symbol=PAXG", nil, 200), "derivatives") {
		t.Fatal("PAXG has no perp on the stub")
	}
	c.fail("GET", "/api/market/derivatives?symbol=PEPE", nil, 400, "bad_request")
}

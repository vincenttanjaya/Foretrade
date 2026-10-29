package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/autopilot"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
)

const autopilotKeys = "id strategyId name symbol interval status stopReason allocationUsd maxDrawdownPct positionQty avgPrice markPrice " +
	"equity pnl pnlPct drawdownPct lastBarT nextCheckAt log startedAt stoppedAt stopLossPct takeProfitPct trailingStopPct"

// alwaysSpec enters long on every bar: the first check after a deploy fills.
const alwaysSpec = `{"name":"Always Long","symbol":"SOL","interval":"4h","bars":300,"positionPct":50,"feePct":0.05,"stopLossPct":5,
	"longEntry":{"all":[{"left":{"kind":"price"},"op":">","right":{"kind":"const","value":0}}]}}`

func TestIntegrationAutopilot(t *testing.T) {
	s := boot(t, options{model: func(r llmtest.Request) llmtest.Reply {
		switch {
		case strings.HasPrefix(r.System, "You design trading strategies"):
			return llmtest.Reply{Text: `{"name":"Always Long","description":"Long on every bar.","spec":` + alwaysSpec + `}`}
		case strings.HasPrefix(r.System, "You explain a strategy backtest"):
			return llmtest.Reply{Text: "It is always long."}
		}
		t.Errorf("unexpected model call: %.60q", r.System)
		return llmtest.Reply{Status: 400}
	}})
	c := s.signIn(t)

	// A built-in: deployed and watching (no Labs entry rule holds on the fake's last bar), with the stop and target set at deploy.
	top := list(t, c.call("GET", "/api/strategies?scope=top", nil, 200), "strategies")
	labsID, labsName := str(t, top[0], "id"), str(t, top[0], "name")
	dep := c.call("POST", "/api/autopilots", map[string]any{"strategyId": labsID, "allocationUsd": 5000, "maxDrawdownPct": 10,
		"stopLossPct": 4, "takeProfitPct": 8}, 201)
	shape(t, dep, "", "autopilot trade portfolio")
	shape(t, dep, "autopilot", autopilotKeys)
	shape(t, dep, "portfolio", portfolioKeys)
	shapeAll(t, dep, "autopilot.log", "t kind text")
	id := str(t, dep, "autopilot.id")
	if str(t, dep, "autopilot.status") != "running" || str(t, dep, "autopilot.name") != labsName || num(t, dep, "autopilot.positionQty") != 0 ||
		!isNull(t, dep, "trade") || num(t, dep, "autopilot.equity") != 5000 || str(t, dep, "autopilot.log.0.kind") != "deploy" ||
		num(t, dep, "autopilot.stopLossPct") != 4 || num(t, dep, "autopilot.takeProfitPct") != 8 ||
		!strings.HasSuffix(str(t, dep, "autopilot.log.0.text"), "bar · stop 4% · target 8%.") {
		t.Fatalf("deploy: %v", dep)
	}
	isTime(t, dep, "autopilot.nextCheckAt")
	isTime(t, dep, "autopilot.startedAt")
	c.fail("POST", "/api/autopilots", map[string]any{"strategyId": labsID, "allocationUsd": 5000, "maxDrawdownPct": 10}, 409, "conflict")
	c.fail("POST", "/api/autopilots", map[string]any{"strategyId": someID, "allocationUsd": 5000, "maxDrawdownPct": 10}, 404, "not_found")
	s.signIn(t).fail("GET", "/api/autopilots/"+id, nil, 404, "not_found")
	stopped := c.call("POST", "/api/autopilots/"+id+"/stop", map[string]bool{"closePosition": true}, 200)
	if str(t, stopped, "autopilot.status") != "stopped" || str(t, stopped, "autopilot.stopReason") != "user" || !isNull(t, stopped, "trade") {
		t.Fatalf("stop while flat: %v", stopped)
	}

	// An own strategy whose rule holds now. Its spec has a 5% stop and no target: no deploy until one is set.
	strat := c.call("POST", "/api/strategies/generate", map[string]string{"prompt": "Always be long SOL", "symbol": "SOL", "interval": "4h"}, 201)
	deploy := map[string]any{"strategyId": str(t, strat, "strategy.id"), "allocationUsd": 10000, "maxDrawdownPct": 20}
	c.fail("POST", "/api/autopilots", deploy, 400, "bad_request")
	deploy["takeProfitPct"] = 60
	c.fail("POST", "/api/autopilots", deploy, 400, "bad_request")
	// With a target and a 3% trail it enters at once, its stop at the tighter trail (200 × 0.97); stop closes it at market.
	deploy["takeProfitPct"], deploy["trailingStopPct"] = 10, 3
	dep = c.call("POST", "/api/autopilots", deploy, 201)
	shape(t, dep, "trade", tradeKeys)
	id = str(t, dep, "autopilot.id")
	if str(t, dep, "trade.source") != "autopilot" || str(t, dep, "trade.note") != "Autopilot · Always Long" || num(t, dep, "autopilot.positionQty") <= 0 ||
		str(t, dep, "autopilot.log.0.kind") != "entry" || !near(num(t, dep, "portfolio.positions.0.stopLoss"), 194) ||
		!near(num(t, dep, "portfolio.positions.0.takeProfit"), 220) || num(t, dep, "autopilot.stopLossPct") != 5 ||
		num(t, dep, "autopilot.takeProfitPct") != 10 || num(t, dep, "autopilot.trailingStopPct") != 3 {
		t.Fatalf("deploy with entry: %v", dep)
	}
	// SOL rises to 210 and the checker runs (here directly, not on the server's 30 s tick): the stop trails up to 210 × 0.97.
	s.binance.SetPrice("SOLUSDT", 210)
	mkt := market.NewClient(s.binance.URL) // a cold cache sees the new price
	stop := func() float64 {
		return num(t, c.call("GET", "/api/portfolio", nil, 200), "portfolio.positions.0.stopLoss")
	}
	// Retried: a server's own tick may hold the database-wide autopilot lock for a moment.
	for i := 0; i < 20 && !near(stop(), 203.7); i++ {
		autopilot.Check(context.Background(), &app.Deps{DB: s.db, Market: mkt, Paper: paper.NewEngine(s.db, mkt)}, time.Now())
		time.Sleep(time.Duration(i) * 20 * time.Millisecond)
	}
	if !near(stop(), 203.7) {
		t.Fatalf("trailed stop %v, want 203.70", stop())
	}
	detail := c.call("GET", "/api/autopilots/"+id, nil, 200)
	shapeAll(t, detail, "trades", tradeKeys)
	if len(list(t, detail, "trades")) != 1 || len(list(t, c.call("GET", "/api/autopilots", nil, 200), "autopilots")) != 2 {
		t.Fatalf("detail: %v", detail)
	}
	closed := c.call("POST", "/api/autopilots/"+id+"/stop", map[string]bool{"closePosition": true}, 200)
	if str(t, closed, "trade.side") != "sell" || len(list(t, closed, "portfolio.positions")) != 0 || num(t, closed, "autopilot.positionQty") != 0 {
		t.Fatalf("stop and close: %v", closed)
	}
	c.fail("POST", "/api/autopilots/"+id+"/stop", "{}", 409, "conflict")
	trades := list(t, c.call("GET", "/api/portfolio/trades", nil, 200), "trades")
	if len(trades) != 2 || str(t, trades[0], "source") != "autopilot" || str(t, trades[0], "side") != "sell" || str(t, trades[1], "source") != "autopilot" {
		t.Fatalf("account trades: %v", trades)
	}
}

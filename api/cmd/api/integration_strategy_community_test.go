package main

import (
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
)

const communityKeys = "published publishedAt owner mine stars starred copies deploys copiedFrom"

// Community strategies, two users: A generates and publishes, B finds it on
// the community board, stars it, copies it (re-backtested, free, no model
// call) and deploys the copy, which A's card counts; A unpublishes and B can
// no longer read it while B's copy stays. A Labs strategy copies too. Every
// read carries the live forward: nothing to run when just saved or
// published, the same rules since the publish once bars have closed.
func TestIntegrationStrategyCommunity(t *testing.T) {
	s := boot(t, options{model: func(r llmtest.Request) llmtest.Reply {
		switch {
		case strings.HasPrefix(r.System, "You design trading strategies"):
			return llmtest.Reply{Text: `{"name":"BTC EMA Cross","description":"Rides 4h EMA crosses.","spec":` + emaSpec + `}`}
		case strings.HasPrefix(r.System, "You explain a strategy backtest"):
			return llmtest.Reply{Text: "It rides 4h trends."}
		}
		t.Errorf("unexpected model call: %.60q", r.System)
		return llmtest.Reply{Status: 400}
	}})
	a, b := s.signIn(t), s.signIn(t)
	if _, err := s.db.Exec(t.Context(), "update users set name = 'Bea' where id = $1", b.id); err != nil {
		t.Fatal(err)
	}

	gen := a.call("POST", "/api/strategies/generate", map[string]string{"prompt": "Buy BTC when the 20 EMA crosses above the 50 EMA"}, 201)
	shape(t, gen, "strategy", strategyKeys)
	shape(t, gen, "strategy.community", communityKeys)
	id := str(t, gen, "strategy.id")
	if at(t, gen, "strategy.community.published") != false || at(t, gen, "strategy.community.mine") != true || num(t, gen, "strategy.metrics.trades") < 3 {
		t.Fatalf("generated: %v", at(t, gen, "strategy.community"))
	}
	shape(t, gen, "strategy.forward", forwardKeys)
	if str(t, gen, "strategy.forward.basis") != "saved" || !isNull(t, gen, "strategy.forward.metrics") || at(t, gen, "strategy.forward.early") != true ||
		len(list(t, gen, "strategy.forward.equity")) != 0 || !isTime(t, gen, "strategy.forward.since").Equal(isTime(t, gen, "strategy.createdAt")) ||
		!isNull(t, gen, "strategy.forward.asOf") || at(t, gen, "strategy.forward.stale") != false || str(t, gen, "strategy.forward.reason") != "too_new" {
		t.Fatalf("a strategy saved just now has no forward run yet: %v", at(t, gen, "strategy.forward"))
	}
	b.fail("GET", "/api/strategies/"+id, nil, 404, "not_found")
	b.fail("POST", "/api/strategies/"+id+"/publish", map[string]bool{"published": true}, 404, "not_found")
	pub := a.call("POST", "/api/strategies/"+id+"/publish", map[string]bool{"published": true}, 200)
	shape(t, pub, "", "strategy")
	if at(t, pub, "strategy.community.published") != true {
		t.Fatalf("publish: %v", pub)
	}
	isTime(t, pub, "strategy.community.publishedAt")
	if str(t, pub, "strategy.forward.basis") != "published" || !isTime(t, pub, "strategy.forward.since").Equal(isTime(t, pub, "strategy.community.publishedAt")) ||
		!isNull(t, pub, "strategy.forward.asOf") || str(t, pub, "strategy.forward.reason") != "too_new" {
		t.Fatalf("publish moves the forward to the publish: %v", at(t, pub, "strategy.forward"))
	}

	// Published ten days before the fake market's newest bar: the board shows how the rules did since.
	since := markettest.End.Add(-10 * 24 * time.Hour).Add(123 * time.Microsecond)
	if _, err := s.db.Exec(t.Context(), "update strategies set published_at = $2 where id = $1", id, since); err != nil {
		t.Fatal(err)
	}
	board := b.call("GET", "/api/strategies?scope=community", nil, 200)
	shapeAll(t, board, "strategies", stratSumKeys)
	if l := list(t, board, "strategies"); len(l) != 1 || str(t, l[0], "id") != id || str(t, l[0], "community.owner") != "Integration" || at(t, l[0], "community.mine") != false {
		t.Fatalf("community: %v", board)
	}
	shape(t, board, "strategies.0.forward", fwdSumKeys)
	shape(t, board, "strategies.0.forward.metrics", metricsKeys)
	if num(t, board, "strategies.0.forward.metrics.startT") != float64(since.Unix()) || at(t, board, "strategies.0.forward.early") != true ||
		!isTime(t, board, "strategies.0.forward.since").Equal(since) || !isTime(t, board, "strategies.0.forward.asOf").Equal(markettest.End.Add(4*time.Hour)) ||
		at(t, board, "strategies.0.forward.stale") != true || !isNull(t, board, "strategies.0.forward.reason") ||
		num(t, board, "strategies.0.forward.metrics.endT") != float64(markettest.End.Unix()) ||
		num(t, board, "strategies.0.metrics.totalReturnPct") != num(t, gen, "strategy.metrics.totalReturnPct") {
		t.Fatalf("board forward: %v", at(t, board, "strategies.0.forward"))
	}
	detail := b.call("GET", "/api/strategies/"+id, nil, 200)
	shape(t, detail, "strategy.forward", forwardKeys)
	if num(t, detail, "strategy.forward.equity.0.v") != 100 || num(t, detail, "strategy.forward.metrics.totalReturnPct") != num(t, board, "strategies.0.forward.metrics.totalReturnPct") ||
		!isTime(t, detail, "strategy.forward.asOf").Equal(isTime(t, board, "strategies.0.forward.asOf")) ||
		num(t, detail, "strategy.metrics.totalReturnPct") != num(t, gen, "strategy.metrics.totalReturnPct") {
		t.Fatalf("detail forward: %v", at(t, detail, "strategy.forward"))
	}
	if got := b.call("POST", "/api/strategies/"+id+"/star", map[string]bool{"starred": true}, 200); num(t, got, "stars") != 1 || at(t, got, "starred") != true {
		t.Fatalf("star: %v", got)
	}
	b.call("POST", "/api/strategies/"+id+"/star", map[string]bool{"starred": true}, 200) // idempotent

	cp := b.call("POST", "/api/strategies/"+id+"/copy", nil, 201)
	shape(t, cp, "strategy.community.copiedFrom", "id name owner")
	shape(t, cp, "strategy.forward", forwardKeys)
	if str(t, cp, "strategy.forward.basis") != "saved" || !isTime(t, cp, "strategy.forward.since").Equal(isTime(t, cp, "strategy.createdAt")) ||
		isTime(t, cp, "strategy.forward.since").Equal(since) || !isNull(t, cp, "strategy.forward.metrics") || !isNull(t, cp, "strategy.forward.asOf") ||
		str(t, cp, "strategy.forward.reason") != "too_new" {
		t.Fatalf("copy must start a new owner's saved window: %v", at(t, cp, "strategy.forward"))
	}
	copyID := str(t, cp, "strategy.id")
	if str(t, cp, "strategy.community.copiedFrom.id") != id || str(t, cp, "strategy.community.owner") != "Bea" || str(t, cp, "strategy.author") != "Bea" ||
		num(t, cp, "strategy.metrics.sharpe") != num(t, gen, "strategy.metrics.sharpe") || str(t, cp, "strategy.explanation") != "It rides 4h trends." {
		t.Fatalf("copy: %v", cp)
	}
	if num(t, b.call("GET", "/api/me", nil, 200), "credits.used") != 0 {
		t.Fatal("star and copy are free")
	}
	// Copy → Deploy: B's autopilot on the copy counts as a deploy of A's strategy.
	b.call("POST", "/api/autopilots", map[string]any{"strategyId": copyID, "allocationUsd": 1000, "maxDrawdownPct": 10, "takeProfitPct": 8}, 201)
	b.fail("POST", "/api/autopilots", map[string]any{"strategyId": id, "allocationUsd": 1000, "maxDrawdownPct": 10, "takeProfitPct": 8}, 404, "not_found")
	if c := at(t, a.call("GET", "/api/strategies/"+id, nil, 200), "strategy.community"); num(t, c, "stars") != 1 || num(t, c, "copies") != 1 || num(t, c, "deploys") != 1 {
		t.Fatalf("A's counts: %v", c)
	}

	// A unpublishes: off the board, unreadable for B; B's copy stays, its source no longer shown.
	unpub := a.call("POST", "/api/strategies/"+id+"/publish", map[string]bool{"published": false}, 200)
	if str(t, unpub, "strategy.forward.basis") != "saved" || !isTime(t, unpub, "strategy.forward.since").Equal(isTime(t, gen, "strategy.createdAt")) {
		t.Fatalf("unpublish must restore its own saved window: %v", at(t, unpub, "strategy.forward"))
	}
	if l := list(t, b.call("GET", "/api/strategies?scope=community", nil, 200), "strategies"); len(l) != 0 {
		t.Fatalf("community after unpublish: %v", l)
	}
	b.fail("GET", "/api/strategies/"+id, nil, 404, "not_found")
	b.fail("POST", "/api/strategies/"+id+"/star", map[string]bool{"starred": false}, 404, "not_found")
	b.fail("POST", "/api/strategies/"+id+"/copy", nil, 404, "not_found")
	if got := b.call("GET", "/api/strategies/"+copyID, nil, 200); !isNull(t, got, "strategy.community.copiedFrom") {
		t.Fatalf("copy after unpublish: %v", got)
	}

	// A Labs copy: its source reads as Foretrade Labs.
	labsID := str(t, b.call("GET", "/api/strategies?scope=top", nil, 200), "strategies.0.id")
	lab := b.call("POST", "/api/strategies/"+labsID+"/copy", nil, 201)
	if str(t, lab, "strategy.community.copiedFrom.owner") != "Foretrade Labs" || str(t, lab, "strategy.community.copiedFrom.id") != labsID {
		t.Fatalf("labs copy: %v", lab)
	}
	if n := len(list(t, b.call("GET", "/api/strategies?scope=mine", nil, 200), "strategies")); n != 2 {
		t.Fatalf("B's studio: %d", n)
	}
}

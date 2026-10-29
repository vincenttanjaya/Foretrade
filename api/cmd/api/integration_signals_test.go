package main

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/signals"
)

// The signals board is computed at boot without a user or credits, and the
// account's equity curve and P&L by source agree with its portfolio.
func TestIntegrationSignalsAndPerformance(t *testing.T) {
	var deps *app.Deps
	s := boot(t, options{signals: true, runtime: &runOptions{deps: func(d *app.Deps) { deps = d }}})
	c := s.signIn(t)

	var board any
	eventually(t, 30*time.Second, "the first signals board", func() bool {
		board = c.call("GET", "/api/signals", nil, 200)
		return !isNull(t, board, "board")
	})
	shape(t, board, "", "board history historyUnavailable record refreshing refresh asOf firstBoard")
	shape(t, board, "board", "id createdAt nextAt bullish bearish method")
	shape(t, board, "refresh", "state failures lastErrorAt retryAt")
	if created, next := isTime(t, board, "board.createdAt"), isTime(t, board, "board.nextAt"); next.Sub(created) != 4*time.Hour-10*time.Minute ||
		len(list(t, board, "board.bullish")) > 4 || len(list(t, board, "board.bearish")) > 4 || at(t, board, "historyUnavailable") != false ||
		!isTime(t, board, "asOf").Equal(created) || at(t, board, "refreshing") != false || at(t, board, "firstBoard") != false ||
		str(t, board, "refresh.state") != "idle" || num(t, board, "refresh.failures") != 0 || str(t, board, "record.rule") != "steady-v1" ||
		!strings.HasPrefix(str(t, board, "board.method"), "Momentum screen:") {
		t.Fatalf("board: %v", board)
	}
	// The stored board entered the pick ledger's sync (no pick: the fake's daily bars are days old, nothing is eligible).
	eventually(t, 30*time.Second, "the ledger sync", func() bool {
		return s.count(t, "select count(*) from signal_ledger_sync where last_board_at = (select max(created_at) from signal_boards)") == 1
	})
	if n := s.count(t, "select count(*) from signal_boards"); n != 1 {
		t.Fatalf("boards: %d", n)
	}

	// A board that stops validating: the read says refreshing (not "first board") and nudges the job,
	// which stores a valid one at once instead of when the old one turns 4 h old. Reads are cached for
	// up to a minute, so the test drops the cache the way the job's next check would.
	if _, err := s.db.Exec(t.Context(), "update signal_boards set board = board - '_quality'"); err != nil {
		t.Fatal(err)
	}
	signals.Forget(deps)
	if got := c.call("GET", "/api/signals", nil, 200); !isNull(t, got, "board") || at(t, got, "refreshing") != true || at(t, got, "firstBoard") != false {
		t.Fatalf("an invalid board: %v", got)
	}
	eventually(t, 30*time.Second, "the replacement board", func() bool {
		board = c.call("GET", "/api/signals", nil, 200)
		return !isNull(t, board, "board")
	})
	if n := s.count(t, "select count(*) from signal_boards"); n != 2 || at(t, board, "refreshing") != false {
		t.Fatalf("after the self-heal: %d boards, %v", n, board)
	}
	if n := s.count(t, "select coalesce(sum(credits), 0) from ai_usage"); n != 0 {
		t.Fatalf("credits spent on the board: %d", n)
	}

	c.call("POST", "/api/orders", map[string]any{"symbol": "BTC", "side": "buy", "notional": 2000}, 200)
	c.call("POST", "/api/orders", map[string]any{"symbol": "ETH", "side": "sell", "notional": 800, "source": "agent"}, 200)
	pf := c.call("GET", "/api/portfolio", nil, 200)
	equity, total := num(t, pf, "portfolio.equity"), num(t, pf, "portfolio.totalPnl")

	curve := c.call("GET", "/api/portfolio/equity", nil, 200)
	shape(t, curve, "", "range interval startingBalance points")
	shapeAll(t, curve, "points", "t v")
	points := list(t, curve, "points")
	last := len(points) - 1
	if str(t, curve, "range") != "7d" || str(t, curve, "interval") != "1h" || num(t, curve, "startingBalance") != 100_000 ||
		last < 168 || math.Abs(num(t, points[last], "v")-equity) > 0.01 || num(t, points[0], "v") != 100_000 {
		t.Fatalf("equity curve: %d points, first %v, last %v, portfolio equity %v", len(points), points[0], points[last], equity)
	}
	for i := 1; i <= last; i++ {
		if num(t, points[i], "t") <= num(t, points[i-1], "t") {
			t.Fatalf("points not ascending at %d", i)
		}
	}
	if iv := str(t, c.call("GET", "/api/portfolio/equity?range=30d", nil, 200), "interval"); iv != "4h" {
		t.Fatalf("30d interval %s", iv)
	}
	c.fail("GET", "/api/portfolio/equity?range=90d", nil, 400, "bad_request")

	attr := c.call("GET", "/api/portfolio/attribution", nil, 200)
	shape(t, attr, "", "sources totalPnl")
	shapeAll(t, attr, "sources", "source trades realizedPnl unrealizedPnl fees totalPnl")
	sum := 0.0
	seen := map[string]bool{}
	for _, src := range list(t, attr, "sources") {
		sum += num(t, src, "totalPnl")
		seen[str(t, src, "source")] = num(t, src, "trades") == 1
	}
	if len(seen) != 2 || !seen["manual"] || !seen["agent"] || math.Abs(sum-total) > 0.01 || math.Abs(num(t, attr, "totalPnl")-total) > 0.01 {
		t.Fatalf("attribution %v, portfolio total P&L %v", attr, total)
	}

	// Portfolio health (round 7): BTC long $2,000 and ETH short $800 is a concentrated two-position book, no stops,
	// 0 credits; the fake Binance moves every pair alike, so the short is a hedge, not a cluster.
	review := c.call("GET", "/api/portfolio/review", nil, 200)
	shape(t, review, "", "asOf revision window positions equity grossExposure netExposure exposurePct predictionStakes predictionStakesPct holdings "+
		"concentration betaBtc volAnnPct var worstDay drawdown30dPct stress correlations clusters stops notes coverage")
	shapeAll(t, review, "holdings", "symbol side value pct beta stopLoss halted markAvailable stopStatus")
	shapeAll(t, review, "stress", "scenario shockPct estPnl estPnlPct")
	shapeAll(t, review, "correlations", "a b rho days")
	shape(t, review, "stops", "covered uncovered uncoveredPct atRiskUsd")
	isTime(t, review, "asOf")
	if num(t, review, "positions") != 2 || num(t, review, "revision") != num(t, pf, "portfolio.revision") || math.Abs(num(t, review, "equity")-equity) > 0.01 ||
		str(t, review, "concentration.level") != "high" || math.Abs(num(t, review, "concentration.top1Pct")-71.4) > 0.1 ||
		str(t, review, "holdings.0.symbol") != "BTC" || str(t, review, "holdings.1.side") != "short" || num(t, review, "window.bars") != 90 ||
		num(t, review, "stops.covered") != 0 || len(list(t, review, "stops.uncovered")) != 2 || len(list(t, review, "clusters")) != 0 ||
		len(list(t, review, "correlations")) != 1 || num(t, review, "correlations.0.rho") != 1 || len(list(t, review, "stress")) != 3 ||
		num(t, review, "betaBtc") != 0.01 || len(list(t, review, "notes")) < 3 {
		t.Fatalf("review: %v", review)
	}
	if n := s.count(t, "select coalesce(sum(credits), 0) from ai_usage"); n != 0 {
		t.Fatalf("credits spent on the review: %d", n)
	}
}

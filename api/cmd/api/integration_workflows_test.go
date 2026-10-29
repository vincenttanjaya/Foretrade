package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/alerts"
	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
	"github.com/vincenttanjaya/Foretrade/api/internal/research"
)

// Workflows: a daily brief and a weekly buy set over HTTP run once when due
// (forced here with SQL, then the checker run directly; the claim makes it
// safe next to the server's own 15 s tick), the buy's fill links back to its
// automation, the list shows each last run and the bell one item per run.
func TestIntegrationWorkflows(t *testing.T) {
	s := boot(t, options{})
	c := s.signIn(t)

	brief := c.call("POST", "/api/alerts", map[string]any{"symbol": "BTC", "trigger": "schedule", "every": "day", "at": "08:00"}, 201)
	shape(t, brief, "alert", alertKeys)
	next := isTime(t, brief, "alert.nextRunAt")
	if str(t, brief, "alert.trigger") != "schedule" || !isNull(t, brief, "alert.op") || !isNull(t, brief, "alert.price") ||
		str(t, brief, "alert.every") != "day" || str(t, brief, "alert.at") != "08:00" || !isNull(t, brief, "alert.weekday") ||
		!isNull(t, brief, "alert.lastRun") || next.UTC().Format("15:04") != "08:00" || !next.After(time.Now()) || next.After(time.Now().Add(24*time.Hour)) {
		t.Fatalf("brief %v", brief)
	}
	buy := str(t, c.call("POST", "/api/alerts", map[string]any{"symbol": "SOL", "trigger": "schedule", "every": "week", "at": "09:00", "weekday": 1,
		"action": "buy", "notional": 100, "source": "agent", "proposalId": "6f1c2c1e-1111-4000-8000-0000000000b4"}, 201), "alert.id")
	c.fail("POST", "/api/alerts", map[string]any{"symbol": "SOL", "trigger": "schedule", "every": "week", "at": "09:00"}, 400, "bad_request")
	c.fail("POST", "/api/alerts", map[string]any{"symbol": "SOL", "trigger": "schedule", "every": "week", "at": "09:00", "weekday": 1,
		"action": "buy", "notional": 100, "source": "agent", "proposalId": "6f1c2c1e-1111-4000-8000-0000000000b4"}, 409, "conflict")

	// Weekdays: the next slot is Monday to Friday; a weekday is refused for it. Cancelled so the runs below stay two.
	wd := c.call("POST", "/api/alerts", map[string]any{"symbol": "ETH", "trigger": "schedule", "every": "weekdays", "at": "07:30"}, 201)
	if next := isTime(t, wd, "alert.nextRunAt").UTC(); str(t, wd, "alert.every") != "weekdays" || !isNull(t, wd, "alert.weekday") ||
		next.Weekday() == time.Saturday || next.Weekday() == time.Sunday || next.Format("15:04") != "07:30" || !next.After(time.Now()) {
		t.Fatalf("weekdays %v", wd)
	}
	c.fail("POST", "/api/alerts", map[string]any{"symbol": "ETH", "trigger": "schedule", "every": "weekdays", "at": "07:30", "weekday": 1}, 400, "bad_request")
	c.call("DELETE", "/api/alerts/"+str(t, wd, "alert.id"), nil, 204)

	if _, err := s.db.Exec(context.Background(), "update alerts set next_run_at = now() - interval '1 minute' where trigger = 'schedule'"); err != nil {
		t.Fatal(err)
	}
	mkt := market.NewClient(s.binance.URL)
	d := &app.Deps{DB: s.db, Market: mkt, Paper: paper.NewEngine(s.db, mkt), Research: research.New(research.Config{}, s.db, nil)}
	if err := alerts.Check(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if n := s.count(t, "select count(*) from alert_runs"); n != 2 {
		t.Fatalf("%d runs", n)
	}

	newest := at(t, c.call("GET", "/api/portfolio/trades?limit=1", nil, 200), "trades.0")
	if str(t, newest, "source") != "alert" || str(t, newest, "symbol") != "SOL" || str(t, newest, "ref.kind") != "alert" || str(t, newest, "ref.id") != buy ||
		str(t, newest, "note") != "Workflow: buy $100 SOL Mondays 09:00 UTC" || str(t, newest, "ref.label") != str(t, newest, "note") {
		t.Fatalf("scheduled fill %v", newest)
	}
	if s.count(t, "select count(*) from alert_runs where alert_id = $1 and status = 'ok' and trade_id = $2", buy, str(t, newest, "id")) != 1 {
		t.Fatal("the run does not hold the trade")
	}

	l := c.call("GET", "/api/alerts?status=active", nil, 200)
	shapeAll(t, l, "alerts", alertKeys)
	if len(list(t, l, "alerts")) != 2 {
		t.Fatalf("schedules stay active: %v", l)
	}
	for i := range 2 {
		run := at(t, l, fmt.Sprintf("alerts.%d.lastRun", i))
		shape(t, run, "", "id at status body error reportChatId excerpt verdict")
		isTime(t, run, "at")
		if str(t, run, "id") == "" || !isNull(t, run, "reportChatId") || !isNull(t, run, "excerpt") || !isNull(t, run, "verdict") {
			t.Fatalf("ordinary deterministic run gained Research metadata: %v", run)
		}
		if str(t, run, "status") != "ok" || !isNull(t, run, "error") {
			t.Fatalf("last run %v", run)
		}
		if isTime(t, l, fmt.Sprintf("alerts.%d.nextRunAt", i)).Before(time.Now()) {
			t.Fatalf("next run not moved on: %v", l)
		}
	}

	n := c.call("GET", "/api/notifications", nil, 200)
	shapeAll(t, n, "items", notificationKeys)
	kinds := map[string]string{}
	for i := range list(t, n, "items") {
		item := at(t, n, fmt.Sprintf("items.%d", i))
		kinds[str(t, item, "kind")] = str(t, item, "title") + " | " + str(t, item, "body")
		if str(t, item, "link") != "/automations" {
			t.Fatalf("link %v", item)
		}
	}
	if len(kinds) != 2 || !strings.HasPrefix(kinds["workflow"], "Daily brief · BTC | BTC $") || !strings.Contains(kinds["workflow"], "% 24h)") ||
		!strings.HasPrefix(kinds["workflow_order"], "Scheduled buy filled · SOL | Bought $100 SOL at $") {
		t.Fatalf("bell %v", kinds)
	}
}

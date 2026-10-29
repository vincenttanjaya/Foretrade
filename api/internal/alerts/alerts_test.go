package alerts

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
	"github.com/vincenttanjaya/Foretrade/api/internal/research"
	"github.com/vincenttanjaya/Foretrade/api/internal/research/researchtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
)

var ctx = context.Background()

// price moves the fake ticker and drops the market cache, as 10 s would.
func price(e *apptest.Env, pair string, p float64) {
	e.Binance.SetPrice(pair, p)
	e.Market = testMarketClient(e.Binance.URL)
	e.Paper = paper.NewEngine(e.DB, e.Market)
}

func create(t *testing.T, e *apptest.Env, body string) Alert {
	t.Helper()
	w := e.Do("POST", "/api/alerts", body)
	if w.Code != 201 {
		t.Fatalf("create %s: %d %s", body, w.Code, w.Body)
	}
	return apptest.JSON[struct{ Alert Alert }](t, w).Alert
}

func errMsg(t *testing.T, e *apptest.Env, method, path, body string) (int, string) {
	t.Helper()
	w := e.Do(method, path, body)
	return w.Code, apptest.JSON[struct{ Error struct{ Message string } }](t, w).Error.Message
}

func load(t *testing.T, e *apptest.Env, id string) Alert {
	t.Helper()
	a, err := scan(e.DB.QueryRow(ctx, "select "+columns+" from alerts where id = $1", id))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// POST validates against the live price (fake SOL $200), caps active alerts at
// 20 and sets an agent card once; DELETE cancels only the caller's active ones.
func TestCreateListCancel(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	for body, want := range map[string]string{
		`{"symbol":"PEPE","op":"below","price":1}`:                                                `unknown symbol "PEPE"`,
		`{"symbol":"SOL","op":"near","price":190}`:                                                "op must be above or below.",
		`{"symbol":"SOL","op":"below","price":0}`:                                                 "price must be a positive number.",
		`{"symbol":"SOL","op":"below","price":205}`:                                               "SOL is already below $205.00 (now $200.00).",
		`{"symbol":"SOL","op":"above","price":200}`:                                               "SOL is already above $200.00 (now $200.00).",
		`{"symbol":"SOL","op":"below","price":190,"action":"buy"}`:                                "notional must be at least $10",
		`{"symbol":"SOL","op":"below","price":190,"action":"buy","notional":5}`:                   "notional must be at least $10",
		`{"symbol":"SOL","op":"below","price":190,"action":"hold","notional":50}`:                 "action must be notify, buy or sell.",
		`{"symbol":"SOL","op":"below","price":190,"notional":50}`:                                 "A notify alert takes no notional, stopLoss or takeProfit.",
		`{"symbol":"SOL","op":"below","price":190,"stopLoss":180}`:                                "A notify alert takes no notional, stopLoss or takeProfit.",
		`{"symbol":"SOL","op":"below","price":190,"action":"buy","notional":50,"stopLoss":195}`:   "For a long, the stop must be below the price ($190.00).",
		`{"symbol":"SOL","op":"below","price":190,"action":"buy","notional":50,"takeProfit":185}`: "For a long, the target must be above the price ($190.00).",
		`{"symbol":"SOL","op":"above","price":210,"action":"sell","notional":50,"stopLoss":205}`:  "For a short, the stop must be above the price ($210.00).",
		`{"symbol":"SOL","op":"below","price":190,"proposalId":"not-a-uuid"}`:                     "proposalId must be a UUID.",
		`{"symbol":"SOL","op":"below","price":190,"source":"autopilot"}`:                          "source must be manual, ticket or agent.",
		`{"symbol":"SOL","op":"below","price":190,"note":"` + strings.Repeat("x", 201) + `"}`:     "note must be at most 200 characters.",
	} {
		if code, msg := errMsg(t, e, "POST", "/api/alerts", body); code != 400 || msg != want {
			t.Errorf("%.70s: %d %q, want %q", body, code, msg, want)
		}
	}

	a := create(t, e, `{"symbol":"sol","op":"below","price":190,"note":" Dip zone "}`)
	if a.Symbol != "SOL" || a.Action != "notify" || a.Source != "manual" || a.Status != "active" || a.Note != "Dip zone" ||
		a.Notional != nil || a.ProposalID != nil || a.FiredAt != nil {
		t.Fatalf("created %+v", a)
	}
	pid := "6f1c2c1e-1111-4000-8000-0000000000a1"
	card := `{"symbol":"SOL","op":"below","price":180,"action":"buy","notional":1000,"stopLoss":170,"takeProfit":210,"source":"agent","proposalId":"` + pid + `"}`
	b := create(t, e, card)
	if b.Action != "buy" || *b.Notional != 1000 || *b.StopLoss != 170 || *b.TakeProfit != 210 || *b.ProposalID != pid || b.Source != "agent" {
		t.Fatalf("order alert %+v", b)
	}
	if code, msg := errMsg(t, e, "POST", "/api/alerts", card); code != 409 || msg != "This alert was already set." {
		t.Fatalf("replay: %d %q", code, msg)
	}

	// List: active first, then newest first; ?status=active leaves the rest out.
	if _, err := e.DB.Exec(ctx, "update alerts set status = 'fired', fired_at = now(), fired_price = 189 where id = $1", a.ID); err != nil {
		t.Fatal(err)
	}
	c := create(t, e, `{"symbol":"BTC","op":"above","price":110000}`)
	list := func(q string) (ids []string) {
		for _, x := range apptest.JSON[struct{ Alerts []Alert }](t, e.Do("GET", "/api/alerts"+q, nil)).Alerts {
			ids = append(ids, x.ID)
		}
		return ids
	}
	if got := fmt.Sprint(list("")); got != fmt.Sprint([]string{c.ID, b.ID, a.ID}) {
		t.Fatalf("all: %v", got)
	}
	if got := fmt.Sprint(list("?status=active")); got != fmt.Sprint([]string{c.ID, b.ID}) {
		t.Fatalf("active: %v", got)
	}
	if code, _ := errMsg(t, e, "GET", "/api/alerts?status=open", ""); code != 400 {
		t.Fatalf("bad status: %d", code)
	}

	// DELETE: the caller's active alert only.
	if w := e.Do("DELETE", "/api/alerts/"+c.ID, "{}"); w.Code != 204 || load(t, e, c.ID).Status != "cancelled" {
		t.Fatalf("cancel: %d", w.Code)
	}
	for id, want := range map[string]string{c.ID: "This alert was already cancelled.", a.ID: "This alert already fired."} {
		if code, msg := errMsg(t, e, "DELETE", "/api/alerts/"+id, "{}"); code != 409 || msg != want {
			t.Errorf("delete %s: %d %q", id, code, msg)
		}
	}
	other := e.Session(storetest.User(t, e.DB))
	for _, w := range []int{
		e.DoAs(other, "DELETE", "/api/alerts/"+b.ID, "{}").Code,
		e.Do("DELETE", "/api/alerts/6f1c2c1e-1111-4000-8000-0000000000ff", "{}").Code,
		e.Do("DELETE", "/api/alerts/nope", "{}").Code,
	} {
		if w != 404 {
			t.Errorf("foreign/unknown delete: %d", w)
		}
	}
	if load(t, e, b.ID).Status != "active" {
		t.Fatal("another user cancelled it")
	}

	// The cap: 20 active alerts.
	for range 19 { // b is the 20th
		create(t, e, `{"symbol":"ETH","op":"above","price":5000}`)
	}
	if code, msg := errMsg(t, e, "POST", "/api/alerts", `{"symbol":"ETH","op":"above","price":5000}`); code != 409 || msg != "You have 20 active alerts. Cancel one first." {
		t.Fatalf("cap: %d %q", code, msg)
	}
}

// Check fires at the boundary, claims each alert once, places the order with
// source "alert" and its ref, records failures, and skips disabled accounts.
func TestCheck(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	up := create(t, e, `{"symbol":"SOL","op":"above","price":210}`)
	down := create(t, e, `{"symbol":"SOL","op":"below","price":190,"action":"buy","notional":1000,"stopLoss":180,"takeProfit":220}`)
	gap := create(t, e, `{"symbol":"SOL","op":"below","price":185,"action":"buy","notional":500,"stopLoss":176}`)
	big := create(t, e, `{"symbol":"ETH","op":"below","price":3900,"action":"buy","notional":500000}`)

	price(e, "SOLUSDT", 209.99)
	if err := Check(ctx, e.Deps); err != nil || load(t, e, up.ID).Status != "active" {
		t.Fatalf("below the trigger: %v", err)
	}
	price(e, "SOLUSDT", 210) // mark = price fires
	if err := Check(ctx, e.Deps); err != nil {
		t.Fatal(err)
	}
	if a := load(t, e, up.ID); a.Status != "fired" || *a.FiredPrice != 210 || a.FiredAt == nil || a.TradeID != nil {
		t.Fatalf("above: %+v", a)
	}
	if load(t, e, down.ID).Status != "active" {
		t.Fatal("below fired early")
	}

	// Two checks at once at the boundary: one fill.
	price(e, "SOLUSDT", 190)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			if err := Check(ctx, e.Deps); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	a := load(t, e, down.ID)
	trades, _ := e.Paper.Trades(ctx, e.User, 10)
	if a.Status != "fired" || a.TradeID == nil || len(trades) != 1 {
		t.Fatalf("concurrent checks: %+v, %d trades", a, len(trades))
	}
	tr := trades[0]
	if tr.ID != *a.TradeID || tr.Source != "alert" || tr.Side != "buy" || tr.Note != "Alert: SOL below $190.00" ||
		tr.Ref == nil || tr.Ref.Kind != "alert" || tr.Ref.ID != down.ID || tr.Ref.Label != tr.Note {
		t.Fatalf("fill %+v ref %+v", tr, tr.Ref)
	}
	pf, _ := e.Paper.Portfolio(ctx, e.User)
	if p := pf.Positions[0]; p.Symbol != "SOL" || *p.StopLoss != 180 || *p.TakeProfit != 220 {
		t.Fatalf("position %+v", p)
	}
	if load(t, e, gap.ID).Status != "active" {
		t.Fatal("the $185 alert fired at $190")
	}

	// A gap through the stop: the order is refused, the alert records why.
	price(e, "SOLUSDT", 175)
	price(e, "ETHUSDT", 3800) // and a buy beyond 2× equity
	if err := Check(ctx, e.Deps); err != nil {
		t.Fatal(err)
	}
	if a := load(t, e, gap.ID); a.Status != "failed" || *a.Error != "stopLoss must be below the price for a long" || a.TradeID != nil {
		t.Fatalf("gap: %+v", a)
	}
	if a := load(t, e, big.ID); a.Status != "failed" || *a.Error != "Not enough buying power when the alert fired." {
		t.Fatalf("buying power: %+v", a)
	}

	// A pair halted at $18.50 still fires on that last price; the buy is refused and the alert says why.
	halted := create(t, e, `{"symbol":"LINK","op":"below","price":19,"action":"buy","notional":100}`)
	e.Binance.Halt("LINKUSDT", true)
	price(e, "LINKUSDT", 18.5)
	if err := Check(ctx, e.Deps); err != nil {
		t.Fatal(err)
	}
	if a := load(t, e, halted.ID); a.Status != "failed" || a.TradeID != nil ||
		*a.Error != "LINK is halted on Binance, so new orders are paused; reducing or closing a position still fills at its last price." {
		t.Fatalf("halted: %+v", a)
	}

	// A disabled account's alerts never fire.
	off := create(t, e, `{"symbol":"BTC","op":"above","price":100500}`)
	e.DB.Exec(ctx, "update users set disabled_at = now() where id = $1", e.User)
	price(e, "BTCUSDT", 101000)
	if err := Check(ctx, e.Deps); err != nil || load(t, e, off.ID).Status != "active" {
		t.Fatalf("disabled: %v %s", err, load(t, e, off.ID).Status)
	}

	// Nothing active (the disabled account's alert doesn't count): no market call.
	e.Binance.Down("", true)
	price(e, "BTCUSDT", 101000)
	if err := Check(ctx, e.Deps); err != nil {
		t.Fatalf("no active alerts, yet: %v", err)
	}
	e.DB.Exec(ctx, "update users set disabled_at = null where id = $1", e.User)
	if err := Check(ctx, e.Deps); err == nil {
		t.Fatal("an active alert must read the market (down here)")
	}
}

// The bell: copy per kind, newest first, the last 14 days, unread since seen.
func TestNotifications(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	exec := func(sql string, args ...any) string {
		t.Helper()
		var id string
		if err := e.DB.QueryRow(ctx, sql+" returning id::text", args...).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	ago := func(m int) time.Time { return time.Now().Add(-time.Duration(m) * time.Minute) }
	alert := func(at time.Time, cols, vals string, args ...any) string {
		return exec(`insert into alerts (user_id, fired_at, `+cols+`) values ($1, $2, `+vals+`)`, append([]any{e.User, at}, args...)...)
	}
	trade := func(at time.Time, side string, qty, px, realized float64, source, note string) string {
		return exec(`insert into trades (user_id, symbol, side, qty, price, notional, fee, realized_pnl, source, note, created_at)
			values ($1, 'SOL', $2, $3::float8, $4::float8, $3::float8 * $4::float8, 1, $5, $6, $7, $8)`, e.User, side, qty, px, realized, source, note, at)
	}
	forecast := func(status, shape, result string, completed, resolved *time.Time, outcome *string, created time.Time) string {
		return exec(`insert into forecasts (user_id, shape, question, outcomes, resolution_date, status, result, completed_at, resolved_at, outcome, created_at)
			values ($1, $2, 'Will SOL close above $250 on Dec 31?', '["Yes","No"]', '2026-12-31', $3, $4::jsonb, $5, $6, $7, $8)`,
			e.User, shape, status, result, completed, resolved, outcome, created)
	}
	binary := `{"probabilities":[{"outcome":"Yes","p":0.28},{"outcome":"No","p":0.72}]}`

	notify := alert(ago(10), "symbol, op, price, status, fired_price, note", "'SOL', 'below', 175, 'fired', 174.8, $3", "Dip buy zone")
	rose := alert(ago(20), "symbol, op, price, status, fired_price", "'BTC', 'above', 110000, 'fired', 110250")
	fill := trade(ago(30), "buy", 1000/174.83, 174.83, -1, "alert", "Alert: SOL below $175.00")
	order := alert(ago(30), "symbol, op, price, status, fired_price, action, notional, trade_id", "'SOL', 'below', 175, 'fired', 174.8, 'buy', 1000, $3", fill)
	failed := alert(ago(40), "symbol, op, price, status, fired_price, action, notional, error", "'ETH', 'below', 3900, 'failed', 3899, 'buy', 500000, 'Not enough buying power when the alert fired.'")
	alert(ago(50), "symbol, op, price, status, fired_price", "'SOL', 'above', 250, 'cancelled', null") // not a notification
	stop := trade(ago(60), "sell", 12.4, 118.2, -1084.1, "stop_loss", "Stop loss triggered")
	target := trade(ago(70), "buy", 0.5, 96, 0.3201, "take_profit", "Take profit triggered")
	pilot := trade(ago(80), "buy", 2, 150, -0.3, "autopilot", "Autopilot · SOL EMA cross")
	trade(ago(85), "buy", 1, 150, -0.15, "manual", "") // not a notification
	ready := forecast("completed", "binary", binary, new(ago(90)), nil, nil, ago(92))
	broke := forecast("failed", "binary", "null", nil, nil, nil, ago(100))
	resolved := forecast("completed", "ordered", `{"probabilities":[{"outcome":"Low","p":0.2},{"outcome":"Mid","p":0.5},{"outcome":"High","p":0.3}]}`,
		new(ago(20*24*60)), new(ago(110)), new("Mid"), ago(20*24*60)) // completed 20 days ago: only the resolution shows
	trade(ago(15*24*60), "sell", 1, 120, -5, "stop_loss", "Stop loss triggered") // 15 days ago
	alert(ago(15*24*60), "symbol, op, price, status, fired_price", "'SOL', 'below', 100, 'fired', 99")
	e.DB.Exec(ctx, "update users set notifications_seen_at = now() - interval '45 minutes' where id = $1", e.User)

	type page struct {
		Items  []Notification
		Unread int
	}
	got := apptest.JSON[page](t, e.Do("GET", "/api/notifications", nil))
	sol, btc, eth := "SOL", "BTC", "ETH"
	want := []Notification{
		{"alert:" + notify, "alert", "SOL fell below $175.00", "Now $174.80 · Dip buy zone", &sol, "/automations", time.Time{}},
		{"alert:" + rose, "alert", "BTC rose above $110,000", "Now $110,250", &btc, "/automations", time.Time{}},
		{"alert_order:" + order, "alert_order", "Alert order filled · SOL", "Bought $1,000 at $174.83 (trigger $175.00)", &sol, "/account", time.Time{}},
		{"alert_failed:" + failed, "alert_failed", "Alert order not filled · ETH", "Not enough buying power when the alert fired.", &eth, "/automations", time.Time{}},
		{"stop_loss:" + stop, "stop_loss", "Stop loss hit · SOL", "Sold 12.4 SOL at $118.20 · realized −$1,084.10", &sol, "/account", time.Time{}},
		{"take_profit:" + target, "take_profit", "Target hit · SOL", "Bought 0.5 SOL at $96.00 · realized +$0.32", &sol, "/account", time.Time{}},
		{"autopilot:" + pilot, "autopilot", "Autopilot bought SOL", "Autopilot · SOL EMA cross", &sol, "/account", time.Time{}},
		{"forecast_completed:" + ready, "forecast_completed", "Forecast ready · Yes 28%", "Will SOL close above $250 on Dec 31?", nil, "/forecasts/" + ready, time.Time{}},
		{"forecast_failed:" + broke, "forecast_failed", "Forecast failed", "Will SOL close above $250 on Dec 31?", nil, "/forecasts/" + broke, time.Time{}},
		{"forecast_resolved:" + resolved, "forecast_resolved", "Forecast resolved · Mid", "Will SOL close above $250 on Dec 31?", nil, "/forecasts/" + resolved, time.Time{}},
	}
	if len(got.Items) != len(want) || got.Unread != 4 {
		t.Fatalf("%d items, unread %d: %+v", len(got.Items), got.Unread, got.Items)
	}
	for i, n := range got.Items {
		at := n.At
		n.At = time.Time{}
		w := want[i]
		if n.ID != w.ID || n.Kind != w.Kind || n.Title != w.Title || n.Body != w.Body || n.Link != w.Link || deref(n.Symbol) != deref(w.Symbol) {
			t.Errorf("item %d\n got %+v\nwant %+v", i, n, w)
		}
		if i > 0 && at.After(got.Items[i-1].At) {
			t.Errorf("item %d is newer than the one before", i)
		}
	}

	if got := apptest.JSON[page](t, e.Do("GET", "/api/notifications?limit=2", nil)); len(got.Items) != 2 || got.Unread != 4 {
		t.Fatalf("limit 2: %d items, unread %d", len(got.Items), got.Unread)
	}
	if w := e.Do("GET", "/api/notifications?limit=51", nil); w.Code != 400 {
		t.Fatalf("limit 51: %d", w.Code)
	}
	if w := e.Do("POST", "/api/notifications/seen", "{}"); w.Code != 204 {
		t.Fatalf("seen: %d", w.Code)
	}
	if got := apptest.JSON[page](t, e.Do("GET", "/api/notifications", nil)); got.Unread != 0 || len(got.Items) != len(want) {
		t.Fatalf("after seen: unread %d", got.Unread)
	}
	// Another user sees none of it.
	if got := apptest.JSON[page](t, e.DoAs(e.Session(storetest.User(t, e.DB)), "GET", "/api/notifications", nil)); len(got.Items) != 0 || got.Unread != 0 {
		t.Fatalf("other user: %+v", got)
	}
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

// Schedules: POST checks them without a market call and stores the first run;
// the price check leaves them alone; a due one runs once across concurrent
// checks and moves to its next slot; a brief reads the market, the wire's
// sentiment and top headline (or says the price is unavailable); an order
// fills with source "alert" and its ref, a refused one logs a failed run and
// stays active; the list carries the last run and the bell the three kinds.
func TestSchedules(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	const weekday = "weekday (0–6, 0 = Sunday) is required for a weekly schedule and only then."
	for body, want := range map[string]string{
		`{"symbol":"BTC","trigger":"cron","every":"day","at":"08:00"}`:                                                "trigger must be price, schedule, odds or drawdown.",
		`{"symbol":"BTC","trigger":"schedule","every":"day","at":"08:00","op":"below"}`:                               "A scheduled automation takes no op or price.",
		`{"symbol":"BTC","trigger":"schedule","every":"day","at":"08:00","price":90000}`:                              "A scheduled automation takes no op or price.",
		`{"symbol":"BTC","trigger":"schedule","every":"hour","at":"08:00"}`:                                           "every must be day, weekdays or week.",
		`{"symbol":"BTC","trigger":"schedule","every":"weekdays","at":"09:00","weekday":1}`:                           weekday,
		`{"symbol":"BTC","trigger":"schedule","every":"day","at":"8:00"}`:                                             "at must be HH:MM (UTC).",
		`{"symbol":"BTC","trigger":"schedule","every":"day","at":"24:00"}`:                                            "at must be HH:MM (UTC).",
		`{"symbol":"BTC","trigger":"schedule","every":"week","at":"09:00"}`:                                           weekday,
		`{"symbol":"BTC","trigger":"schedule","every":"day","at":"09:00","weekday":1}`:                                weekday,
		`{"symbol":"BTC","trigger":"schedule","every":"week","at":"09:00","weekday":7}`:                               weekday,
		`{"symbol":"BTC","trigger":"schedule","every":"day","at":"09:00","action":"buy","notional":5}`:                "notional must be at least $10",
		`{"symbol":"BTC","trigger":"schedule","every":"day","at":"09:00","action":"buy","notional":100,"stopLoss":1}`: "A scheduled order takes no stopLoss or takeProfit.",
		`{"symbol":"BTC","trigger":"schedule","every":"day","at":"09:00","notional":100}`:                             "A notify alert takes no notional, stopLoss or takeProfit.",
	} {
		if code, msg := errMsg(t, e, "POST", "/api/alerts", body); code != 400 || msg != want {
			t.Errorf("%.80s: %d %q, want %q", body, code, msg, want)
		}
	}

	// Create: no market call (the ticker is down), next run = the next 08:00 UTC.
	e.Binance.Down("", true)
	price(e, "BTCUSDT", 100000)
	before := nextRun("day", 8*60, 0, time.Now())
	daily := create(t, e, `{"symbol":"btc","trigger":"schedule","every":"day","at":"08:00","note":"Morning read"}`)
	if daily.Trigger != "schedule" || daily.Op != nil || daily.Price != nil || *daily.Every != "day" || *daily.At != "08:00" || daily.Weekday != nil ||
		daily.Action != "notify" || daily.Status != "active" || daily.LastRun != nil ||
		!(daily.NextRunAt.Equal(before) || daily.NextRunAt.Equal(nextRun("day", 8*60, 0, time.Now()))) {
		t.Fatalf("daily %+v next %v", daily, daily.NextRunAt)
	}
	// The price check ignores schedules: only a schedule is active, so no market call.
	if err := Check(ctx, e.Deps); err != nil {
		t.Fatalf("a schedule that is not due: %v", err)
	}
	e.Binance.Down("", false)
	price(e, "BTCUSDT", 100000)
	weekly := create(t, e, `{"symbol":"ETH","trigger":"schedule","every":"week","at":"09:00","weekday":1}`)
	buy := create(t, e, `{"symbol":"BTC","trigger":"schedule","every":"week","at":"09:00","weekday":1,"action":"buy","notional":99.99999999999999}`) // stored in cents: 100
	weekdays := create(t, e, `{"symbol":"XRP","trigger":"schedule","every":"weekdays","at":"07:00"}`)
	if wd := weekdays.NextRunAt.UTC().Weekday(); *weekdays.Every != "weekdays" || weekdays.Weekday != nil || wd == time.Saturday || wd == time.Sunday ||
		weekdays.NextRunAt.UTC().Format("15:04") != "07:00" || !weekdays.NextRunAt.Equal(nextRun("weekdays", 7*60, 0, weekdays.CreatedAt)) {
		t.Fatalf("weekdays %+v next %v", weekdays, weekdays.NextRunAt)
	}
	broke := create(t, e, `{"symbol":"SOL","trigger":"schedule","every":"day","at":"09:30","action":"buy","notional":500000}`)
	if *weekly.Weekday != 1 || weekly.NextRunAt.UTC().Weekday() != time.Monday || weekly.NextRunAt.UTC().Format("15:04") != "09:00" || *buy.Notional != 100 {
		t.Fatalf("weekly %+v buy %+v", weekly, buy)
	}

	// The wire: a fresh brief with BTC's sentiment and a BTC headline.
	reason := strings.Repeat("ETF demand keeps building ", 6) // 156 chars, clipped to 140
	raw, _ := json.Marshal(research.MarketBrief{Headline: "h", Sentiment: []research.Sentiment{{Symbol: "BTC", Score: 0.4, Label: "bullish", Reason: reason}}})
	headline := "Bitcoin ETF inflows top $1.2B as BTC holds $100k"
	if _, err := e.DB.Exec(ctx, `insert into market_briefs (brief) values ($1)`, raw); err != nil {
		t.Fatal(err)
	}
	researchtest.AcceptBriefs(t, e.DB)
	if _, err := e.DB.Exec(ctx, `insert into news_items (url, title, source, published_at, symbols) values ('https://example.com/btc', $1, 'Reuters', now(), '{BTC}')`, headline); err != nil {
		t.Fatal(err)
	}
	due := func(ids ...string) {
		t.Helper()
		for _, id := range ids {
			if _, err := e.DB.Exec(ctx, "update alerts set next_run_at = now() - interval '1 minute' where id = $1", id); err != nil {
				t.Fatal(err)
			}
		}
	}
	type run struct {
		Status, Body string
		TradeID, Err *string
	}
	runs := func(id string) (out []run) {
		t.Helper()
		rows, _ := e.DB.Query(ctx, "select status, body, trade_id::text, error from alert_runs where alert_id = $1 order by ran_at", id)
		for rows.Next() {
			var r run
			if err := rows.Scan(&r.Status, &r.Body, &r.TradeID, &r.Err); err != nil {
				t.Fatal(err)
			}
			out = append(out, r)
		}
		return out
	}

	// Two checks at once: one run, and the next 08:00.
	due(daily.ID)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			if err := Check(ctx, e.Deps); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	assets, _ := e.Market.Assets(ctx)
	btc := assets[slices.IndexFunc(assets, func(a market.Asset) bool { return a.Symbol == "BTC" })]
	want := fmt.Sprintf("BTC %s (%+.1f%% 24h) · wire sentiment bullish: %s… · Top headline: %s", paper.Money(btc.Price), btc.Change24hPct,
		reason[:139], headline)
	if r := runs(daily.ID); len(r) != 1 || r[0].Status != "ok" || r[0].Body != want || r[0].TradeID != nil {
		t.Fatalf("daily runs %+v\nwant %q", r, want)
	}
	if a := load(t, e, daily.ID); a.Status != "active" || a.FiredAt == nil || !a.NextRunAt.Equal(nextRun("day", 8*60, 0, time.Now())) {
		t.Fatalf("after the run %+v next %v", a, a.NextRunAt)
	}

	// The market down: the brief says so (ETH and XRP have no sentiment or headline here).
	due(weekly.ID, weekdays.ID)
	e.Binance.Down("", true)
	price(e, "ETHUSDT", 3000)
	if err := Check(ctx, e.Deps); err != nil {
		t.Fatal(err)
	}
	if r := runs(weekly.ID); len(r) != 1 || r[0].Body != "ETH price unavailable right now" {
		t.Fatalf("weekly runs %+v", r)
	}
	if a := load(t, e, weekdays.ID); len(runs(weekdays.ID)) != 1 || !a.NextRunAt.Equal(nextRun("weekdays", 7*60, 0, time.Now())) {
		t.Fatalf("weekdays after its run %+v", a)
	}
	e.Binance.Down("", false)
	price(e, "BTCUSDT", 100000)

	// Orders: a fill with source "alert" and its ref; a refused one logs why and stays active.
	due(buy.ID, broke.ID)
	if err := Check(ctx, e.Deps); err != nil {
		t.Fatal(err)
	}
	trades, _ := e.Paper.Trades(ctx, e.User, 10)
	if len(trades) != 1 {
		t.Fatalf("%d trades", len(trades))
	}
	tr := trades[0]
	if tr.Source != "alert" || tr.Side != "buy" || tr.Symbol != "BTC" || tr.Note != "Workflow: buy $100 BTC Mondays 09:00 UTC" ||
		tr.Ref == nil || tr.Ref.Kind != "alert" || tr.Ref.ID != buy.ID || tr.Ref.Label != tr.Note {
		t.Fatalf("scheduled fill %+v ref %+v", tr, tr.Ref)
	}
	if r := runs(buy.ID); len(r) != 1 || r[0].Status != "ok" || *r[0].TradeID != tr.ID || r[0].Body != "Bought $100 BTC at "+paper.Money(tr.Price) {
		t.Fatalf("buy runs %+v", r)
	}
	if r := runs(broke.ID); len(r) != 1 || r[0].Status != "failed" || *r[0].Err != "Not enough buying power when the alert fired." || r[0].TradeID != nil {
		t.Fatalf("refused runs %+v", r)
	}
	if a := load(t, e, broke.ID); a.Status != "active" || !a.NextRunAt.After(time.Now()) {
		t.Fatalf("refused schedule %+v", a)
	}

	// The list carries each schedule's last run.
	listed := map[string]Alert{}
	for _, a := range apptest.JSON[struct{ Alerts []Alert }](t, e.Do("GET", "/api/alerts", nil)).Alerts {
		listed[a.ID] = a
	}
	if l := listed[buy.ID].LastRun; l == nil || l.Status != "ok" || l.Body != "Bought $100 BTC at "+paper.Money(tr.Price) || l.Error != nil || l.At.IsZero() {
		t.Fatalf("buy lastRun %+v", l)
	}
	if l := listed[broke.ID].LastRun; l == nil || l.Status != "failed" || l.Body != "" || *l.Error != "Not enough buying power when the alert fired." {
		t.Fatalf("refused lastRun %+v", l)
	}

	// The bell: one item per run, newest first.
	got := apptest.JSON[struct{ Items []Notification }](t, e.Do("GET", "/api/notifications", nil)).Items
	var kinds []string
	for _, n := range got {
		kinds = append(kinds, n.Kind+" | "+n.Title+" | "+n.Body+" | "+n.Link+" | "+deref(n.Symbol))
	}
	slices.Sort(kinds)
	if w := []string{
		"workflow | Daily brief · BTC | " + want + " | /automations | BTC",
		"workflow | Weekday brief · XRP | XRP price unavailable right now | /automations | XRP",
		"workflow | Weekly brief · ETH | ETH price unavailable right now | /automations | ETH",
		"workflow_failed | Scheduled order not filled · SOL | Not enough buying power when the alert fired. | /automations | SOL",
		"workflow_order | Scheduled buy filled · BTC | Bought $100 BTC at " + paper.Money(tr.Price) + " | /automations | BTC",
	}; !slices.Equal(kinds, w) {
		t.Fatalf("bell\n%s", strings.Join(kinds, "\n"))
	}

	// Cancel works as for alerts; a cancelled schedule never runs.
	if w := e.Do("DELETE", "/api/alerts/"+daily.ID, "{}"); w.Code != 204 {
		t.Fatalf("cancel: %d", w.Code)
	}
	due(daily.ID)
	if err := Check(ctx, e.Deps); err != nil || len(runs(daily.ID)) != 1 {
		t.Fatalf("a cancelled schedule ran: %v", err)
	}
	// Entire contaminated legacy brief is suppressed in the real scheduled text.
	if _, err := e.DB.Exec(ctx, `update market_briefs set brief=brief-'_quality'`); err != nil {
		t.Fatal(err)
	}
	fresh := create(t, e, `{"symbol":"BTC","trigger":"schedule","every":"day","at":"08:00"}`)
	due(fresh.ID)
	if err := Check(ctx, e.Deps); err != nil {
		t.Fatal(err)
	}
	if r := runs(fresh.ID); len(r) != 1 || strings.Contains(r[0].Body, "ETF demand keeps building") || strings.Contains(r[0].Body, "bullish") || !strings.Contains(r[0].Body, headline) {
		t.Fatalf("contaminated scheduled text: %+v", r)
	}
	if len(e.Bedrock.Requests()) != 0 {
		t.Fatal("scheduled read repaired brief")
	}

}

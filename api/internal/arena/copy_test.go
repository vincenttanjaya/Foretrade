package arena

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/vincenttanjaya/Foretrade/api/internal/alerts"
	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/autopilot"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
)

func copyEnv(t *testing.T) (*apptest.Env, []liveAgentRow) {
	t.Helper()
	e := liveEnv(t, func(llmtest.Request) llmtest.Reply { return llmtest.Reply{Text: "Holding."} })
	alerts.Register(e.Mux, e.Deps)
	autopilot.Register(e.Mux, e.Deps)
	_, agents, err := loadLiveDesk(ctx, e.Deps, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return e, agents
}
func newCopy(t *testing.T, e *apptest.Env, a liveAgentRow, ratio, cap float64) CopyPlan {
	t.Helper()
	w := e.Do("POST", "/api/arena/live/agents/"+a.id+"/copy", map[string]any{"ratio": ratio, "maxUsd": cap})
	if w.Code != 201 {
		t.Fatalf("copy: %d %s", w.Code, w.Body)
	}
	return apptest.JSON[struct{ Plan CopyPlan }](t, w).Plan
}
func copyBook(t *testing.T, e *apptest.Env, id string) planRow {
	t.Helper()
	p, err := scanPlan(e.DB.QueryRow(ctx, "select "+planColumns+" where p.id=$1", id))
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func copyFill(t *testing.T, e *apptest.Env, a liveAgentRow, step int, side string, n float64) {
	t.Helper()
	// A committed agent step is the recovery source; deliberately omit immediate delivery.
	f := []Fill{{Symbol: "BTC", Side: side, Notional: n, Qty: n / 100_000, Price: 100_000}}
	now := time.Now()
	bar := lastClosedBar(now) + int64(step+1)*liveBar
	if _, err := e.DB.Exec(ctx, `insert into live_bars(bar_t,decided_at,marks) values($1,$2,'{"BTC":100000}') on conflict do nothing`, bar, now); err != nil {
		t.Fatal(err)
	}
	if _, err := e.DB.Exec(ctx, `insert into live_steps(agent_id,step,bar_t,decided_at,equity,cash,positions,fills) values($1,$2,$3,$4,100000,100000,'[]',$5)`, a.id, step, bar, now, f); err != nil {
		t.Fatal(err)
	}
}
func deliver(t *testing.T, e *apptest.Env) {
	t.Helper()
	if err := recoverCopies(ctx, e.Deps, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestCopyValidationOwnershipAndStops(t *testing.T) {
	e, a := copyEnv(t)
	for _, req := range []string{`{"ratio":0.05,"maxUsd":100}`, `{"ratio":0.099,"maxUsd":100}`, `{"ratio":1.1,"maxUsd":100}`, `{"ratio":0.1,"maxUsd":99}`, `{"ratio":0.1,"maxUsd":50001}`} {
		if w := e.Do("POST", "/api/arena/live/agents/"+a[0].id+"/copy", req); w.Code != 400 {
			t.Fatalf("bounds: %d %s", w.Code, w.Body)
		}
	}
	p := newCopy(t, e, a[0], .1, 100)
	if w := e.Do("POST", "/api/arena/live/agents/"+a[0].id+"/copy", `{"ratio":1,"maxUsd":50000}`); w.Code != 409 {
		t.Fatalf("duplicate: %d", w.Code)
	}
	foreign := e.Session(storetest.User(t, e.DB))
	if w := e.DoAs(foreign, "POST", "/api/copy-plans/"+p.ID+"/stop", `{"closePositions":true}`); w.Code != 404 {
		t.Fatalf("foreign stop: %d", w.Code)
	}
	if w := e.DoAs(foreign, "GET", "/api/copy-plans", nil); strings.Contains(w.Body.String(), p.ID) {
		t.Fatal("private plan leaked")
	}
	e.LiveArena = false
	if w := e.Do("POST", "/api/copy-plans/"+p.ID+"/stop", `{"closePositions":false}`); w.Code != 200 {
		t.Fatalf("stop while paused: %d %s", w.Code, w.Body)
	}
	if w := e.Do("POST", "/api/copy-plans/"+p.ID+"/stop", `{}`); w.Code != 409 {
		t.Fatalf("second stop: %d", w.Code)
	}
	bell := e.Do("GET", "/api/notifications", nil)
	if !strings.Contains(bell.Body.String(), "copy_stopped") {
		t.Fatalf("stop bell absent: %s", bell.Body)
	}
}

func TestCopyRecoveryIdempotenceCapAndMinimum(t *testing.T) {
	e, a := copyEnv(t)
	p := newCopy(t, e, a[0], .1, 100)
	copyFill(t, e, a[0], 0, "buy", 5000)
	deliver(t, e)
	if got := copyBook(t, e, p.ID).positions["BTC"]; math.Abs(got-100/100_020.0) > 1e-10 {
		t.Fatalf("cap/ratio book: %v", got)
	}
	deliver(t, e)
	if got := liveCount(t, e, "select count(*) from trades where ref_id=$1", p.ID); got != 1 {
		t.Fatalf("replayed delivery: %d", got)
	}
	copyFill(t, e, a[0], 1, "buy", 50)
	deliver(t, e)
	if got := liveCount(t, e, "select count(*) from trades where ref_id=$1", p.ID); got != 1 {
		t.Fatalf("sub-$10 leg filled: %d", got)
	}
	if !strings.Contains(e.Do("GET", "/api/notifications", nil).Body.String(), "copy_order") {
		t.Fatal("copy fill bell absent")
	}
	w := e.Do("POST", "/api/copy-plans/"+p.ID+"/stop", `{"closePositions":true}`)
	if w.Code != 200 {
		t.Fatalf("close: %d %s", w.Code, w.Body)
	}
	if pf, err := e.Paper.Portfolio(ctx, e.User); err != nil || len(pf.Positions) != 0 {
		t.Fatalf("closed %+v %v", pf, err)
	}
}

func TestCopyDeliveryRollback(t *testing.T) {
	e, a := copyEnv(t)
	p := newCopy(t, e, a[0], .5, 1000)
	copyFill(t, e, a[0], 0, "buy", 1000)
	if _, err := e.DB.Exec(ctx, `create function reject_copy_save() returns trigger language plpgsql as $$ begin raise exception 'copy save rejected'; end $$;
 create trigger reject_copy before update on copy_plans for each row execute function reject_copy_save()`); err != nil {
		t.Fatal(err)
	}
	if err := recoverCopies(ctx, e.Deps, time.Now()); err == nil {
		t.Fatal("save failure must fail delivery")
	}
	if got := liveCount(t, e, "select count(*) from trades where ref_id=$1", p.ID); got != 0 {
		t.Fatalf("fill escaped rollback: %d", got)
	}
	if _, err := e.DB.Exec(ctx, "drop trigger reject_copy on copy_plans"); err != nil {
		t.Fatal(err)
	}
	deliver(t, e)
	deliver(t, e)
	if got := liveCount(t, e, "select count(*) from trades where ref_id=$1", p.ID); got != 1 {
		t.Fatalf("recovery count: %d", got)
	}
}

func TestCopyMultiplePlansManualReductionAndShort(t *testing.T) {
	e, a := copyEnv(t)
	p := newCopy(t, e, a[0], 1, 50000)
	q := newCopy(t, e, a[1], 1, 50000)
	copyFill(t, e, a[0], 0, "buy", 1000)
	copyFill(t, e, a[1], 0, "buy", 1000)
	deliver(t, e)
	pf, _ := e.Paper.Portfolio(ctx, e.User)
	if len(pf.Positions) != 1 {
		t.Fatalf("missing delivered holdings: %+v", pf.Positions)
	}
	half := pf.Positions[0].Qty / 2
	if _, _, err := e.Paper.Order(ctx, e.User, paper.OrderRequest{Symbol: "BTC", Side: "sell", Qty: &half}); err != nil {
		t.Fatal(err)
	}
	w := e.Do("POST", "/api/copy-plans/"+p.ID+"/stop", `{"closePositions":true}`)
	if w.Code != 200 {
		t.Fatalf("close: %d %s", w.Code, w.Body)
	}
	pf, _ = e.Paper.Portfolio(ctx, e.User)
	remaining := copyBook(t, e, q.ID).positions["BTC"]
	if len(pf.Positions) != 1 || math.Abs(pf.Positions[0].Qty-remaining) > 1e-9 || math.Abs(remaining-half/2) > 1e-9 {
		t.Fatalf("sold another plan: %+v, other=%v", pf.Positions, remaining)
	}
	if _, _, err := e.Paper.Close(ctx, e.User, "BTC", "manual"); err != nil {
		t.Fatal(err)
	}
	n := 100.0
	if _, _, err := e.Paper.Order(ctx, e.User, paper.OrderRequest{Symbol: "BTC", Side: "sell", Notional: &n}); err != nil {
		t.Fatal(err)
	}
	copyFill(t, e, a[1], 1, "buy", 1000)
	deliver(t, e)
	pf, _ = e.Paper.Portfolio(ctx, e.User)
	if len(pf.Positions) != 1 || pf.Positions[0].Qty >= 0 || len(copyBook(t, e, q.ID).positions) != 0 {
		t.Fatalf("copy offset external short: %+v", pf.Positions)
	}
}

func TestCopyFutureOnlyAndAutopilotOverlap(t *testing.T) {
	e, a := copyEnv(t)
	if _, err := e.DB.Exec(ctx, "update live_agents set last_bar_t=$2 where id=$1", a[0].id, lastClosedBar(time.Now())+liveBar); err != nil {
		t.Fatal(err)
	}
	p := newCopy(t, e, a[0], 1, 1000)
	copyFill(t, e, a[0], 0, "buy", 1000)
	deliver(t, e)
	if got := liveCount(t, e, "select count(*) from trades where ref_id=$1", p.ID); got != 0 {
		t.Fatal("pre-activation decision copied")
	}
	var sid string
	if err := e.DB.QueryRow(ctx, `insert into strategies(user_id,name,description,prompt,spec,metrics,equity,benchmark,trades) values($1,'Core','Core','Core','{"symbol":"BTC","interval":"4h","bars":300,"stopLossPct":5,"takeProfitPct":10}','{}','[]','[]','[]') returning id`, e.User).Scan(&sid); err != nil {
		t.Fatal(err)
	}
	w := e.Do("POST", "/api/autopilots", `{"strategyId":"`+sid+`","allocationUsd":1000,"maxDrawdownPct":20,"stopLossPct":5,"takeProfitPct":10}`)
	if w.Code != 409 {
		t.Fatalf("copy→autopilot overlap: %d %s", w.Code, w.Body)
	}
	if _, err := e.DB.Exec(ctx, "update copy_plans set status='stopped',stopped_at=now() where id=$1", p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.DB.Exec(ctx, `insert into autopilots(user_id,strategy_id,name,spec,allocation_usd,max_drawdown_pct,status,peak_equity) values($1,'test','Core','{"symbol":"BTC"}',1000,20,'running',1000)`, e.User); err != nil {
		t.Fatal(err)
	}
	if w := e.Do("POST", "/api/arena/live/agents/"+a[1].id+"/copy", `{"ratio":0.5,"maxUsd":1000}`); w.Code != 409 {
		t.Fatalf("autopilot→copy overlap: %d %s", w.Code, w.Body)
	}
	if _, err := e.DB.Exec(ctx, `update autopilots set spec='{"symbol":"DOT"}' where user_id=$1`, e.User); err != nil {
		t.Fatal(err)
	}
	newCopy(t, e, a[1], .5, 1000)
}

func TestCopyMissingHaltedQuotesAndDisabledRecovery(t *testing.T) {
	e, a := copyEnv(t)
	p := newCopy(t, e, a[0], 1, 1000)
	e.Binance.Halt("BTCUSDT", true)
	e.Market = testMarketClient(e.Binance.URL)
	e.Paper = paper.NewEngine(e.DB, e.Market)
	copyFill(t, e, a[0], 0, "buy", 1000)
	deliver(t, e)
	if got := liveCount(t, e, "select count(*) from trades where ref_id=$1", p.ID); got != 0 {
		t.Fatal("halted buy filled")
	}
	e.Binance.Halt("BTCUSDT", false)
	e.Market = testMarketClient(e.Binance.URL)
	e.Paper = paper.NewEngine(e.DB, e.Market)
	copyFill(t, e, a[0], 1, "buy", 1000)
	deliver(t, e)
	if _, err := e.DB.Exec(ctx, "update users set disabled_at=now() where id=$1", e.User); err != nil {
		t.Fatal(err)
	}
	copyFill(t, e, a[0], 2, "sell", 1000)
	deliver(t, e)
	if got := copyBook(t, e, p.ID); got.status != "stopped" || got.stopReason == nil || *got.stopReason != "account_disabled" {
		t.Fatalf("disabled %+v", got)
	}
	if got := liveCount(t, e, "select count(*) from trades where ref_id=$1", p.ID); got != 1 {
		t.Fatalf("disabled fill: %d", got)
	}
}

func TestCopyKeepResetAndMissingQuote(t *testing.T) {
	e, a := copyEnv(t)
	p := newCopy(t, e, a[0], .5, 1000)
	copyFill(t, e, a[0], 0, "buy", 1000)
	deliver(t, e)
	w := e.Do("POST", "/api/copy-plans/"+p.ID+"/stop", `{"closePositions":false}`)
	if w.Code != 200 {
		t.Fatalf("keep: %d %s", w.Code, w.Body)
	}
	pf, _ := e.Paper.Portfolio(ctx, e.User)
	if len(pf.Positions) != 1 || math.Abs(pf.Positions[0].Qty-500/100020.0) > 1e-9 || liveCount(t, e, "select count(*) from trades where ref_id=$1", p.ID) != 1 {
		t.Fatalf("keep sold holdings: %+v", pf.Positions)
	}
	q := newCopy(t, e, a[1], 1, 1000)
	copyFill(t, e, a[1], 0, "buy", 1000)
	deliver(t, e)
	if _, err := e.Paper.Reset(ctx, e.User); err != nil {
		t.Fatal(err)
	}
	copyFill(t, e, a[1], 1, "sell", 1000)
	deliver(t, e)
	pf, _ = e.Paper.Portfolio(ctx, e.User)
	if len(pf.Positions) != 0 || len(copyBook(t, e, q.ID).positions) != 0 {
		t.Fatalf("reset opened a short: %+v", pf.Positions)
	}
	copyFill(t, e, a[1], 2, "buy", 1000)
	e.Binance.Down("BTCUSDT", true)
	e.Market = testMarketClient(e.Binance.URL)
	e.Paper = paper.NewEngine(e.DB, e.Market)
	if err := recoverCopies(ctx, e.Deps, time.Now()); err == nil {
		t.Fatal("missing core quote must defer delivery")
	}
	if got := liveCount(t, e, "select count(*) from trades where ref_id=$1", q.ID); got != 0 {
		t.Fatal("missing price filled")
	}
	e.Binance.Down("BTCUSDT", false)
	e.Market = testMarketClient(e.Binance.URL)
	e.Paper = paper.NewEngine(e.DB, e.Market)
	deliver(t, e)
	if got := liveCount(t, e, "select count(*) from trades where ref_id=$1", q.ID); got != 1 {
		t.Fatalf("missing quote did not recover: %d", got)
	}
}

// sideConn is a connection to the test schema outside the app's pool. Lock
// holders and lock observers use it: the pool has max(4, NumCPU) connections,
// and a test that holds one while the racers it blocks take the rest cannot
// get another to poll or to commit, so it deadlocks itself on 4-CPU hosts.
func sideConn(t *testing.T, e *apptest.Env) *pgx.Conn {
	t.Helper()
	c, err := pgx.ConnectConfig(ctx, e.DB.Config().ConnConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(ctx) })
	return c
}

func copyWaiter(t *testing.T, obs *pgx.Conn, pid int) int {
	t.Helper()
	for range 500 {
		var n int
		if err := obs.QueryRow(ctx, "select coalesce(max(pid),0) from pg_stat_activity where $1=any(pg_blocking_pids(pid))", pid).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			return n
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("copy operation did not wait on expected lock")
	return 0
}

// Queue a manual close first, then a recovered copy sell, then stop/close.
// The account and user locks must force fresh ownership at both copy operations.
func TestCopyManualCloseAndStopRacingDelivery(t *testing.T) {
	e, a := copyEnv(t)
	p := newCopy(t, e, a[0], 1, 1000)
	copyFill(t, e, a[0], 0, "buy", 1000)
	deliver(t, e)
	copyFill(t, e, a[0], 1, "sell", 1000)
	// Three racers each need one pooled connection; the holder and observer bring their own.
	obs := sideConn(t, e)
	hold, err := sideConn(t, e).Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Rollback(ctx)
	var pid int
	if err := hold.QueryRow(ctx, "select pg_backend_pid()").Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err := hold.Exec(ctx, "select 1 from accounts where user_id=$1 for update", e.User); err != nil {
		t.Fatal(err)
	}
	manual := make(chan error, 1)
	go func() { _, _, err := e.Paper.Close(ctx, e.User, "BTC", "manual"); manual <- err }()
	manualPID := copyWaiter(t, obs, pid)
	delivered := make(chan error, 1)
	go func() { delivered <- recoverCopies(ctx, e.Deps, time.Now()) }()
	// Wait until delivery owns the user lock and waits for the account.
	var deliveryPID int
	for range 500 {
		if err := obs.QueryRow(ctx, `select coalesce(max(pid),0) from pg_stat_activity where pid<>$2 and ($1=any(pg_blocking_pids(pid)) or $2=any(pg_blocking_pids(pid)))`, pid, manualPID).Scan(&deliveryPID); err != nil {
			t.Fatal(err)
		}
		if deliveryPID != 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if deliveryPID == 0 {
		select {
		case err := <-delivered:
			t.Fatalf("delivery returned before locking: %v", err)
		default:
		}
		rows, _ := obs.Query(ctx, `select pid,coalesce(wait_event,''),query,pg_blocking_pids(pid)::text from pg_stat_activity where wait_event_type='Lock'`)
		if rows != nil {
			for rows.Next() {
				var p int
				var wait, q, blocked string
				rows.Scan(&p, &wait, &q, &blocked)
				t.Logf("pid=%d wait=%s query=%s blocked=%s", p, wait, q, blocked)
			}
			rows.Close()
		}
		t.Fatal("delivery never held user before account")
	}
	stopped := make(chan int, 1)
	go func() { stopped <- e.Do("POST", "/api/copy-plans/"+p.ID+"/stop", `{"closePositions":true}`).Code }()
	copyWaiter(t, obs, deliveryPID)
	if err := hold.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for _, done := range []<-chan error{manual, delivered} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("racing operation deadlocked")
		}
	}
	select {
	case status := <-stopped:
		if status != 200 {
			t.Fatalf("stop status %d", status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stop deadlocked")
	}
	pf, _ := e.Paper.Portfolio(ctx, e.User)
	if len(pf.Positions) != 0 || liveCount(t, e, "select count(*) from trades where user_id=$1 and side='sell'", e.User) != 1 {
		t.Fatalf("copy raced into short: %+v", pf.Positions)
	}
}

func TestCopyAndAutopilotCreationRace(t *testing.T) {
	e, a := copyEnv(t)
	var sid string
	if err := e.DB.QueryRow(ctx, `insert into strategies(user_id,name,description,prompt,spec,metrics,equity,benchmark,trades) values($1,'Core','Core','Core','{"symbol":"BTC","interval":"4h","bars":300,"stopLossPct":5,"takeProfitPct":10}','{}','[]','[]','[]') returning id`, e.User).Scan(&sid); err != nil {
		t.Fatal(err)
	}
	obs := sideConn(t, e)
	hold, err := sideConn(t, e).Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Rollback(ctx)
	var pid int
	if err := hold.QueryRow(ctx, "select pg_backend_pid()").Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err := hold.Exec(ctx, "select 1 from users where id=$1 for no key update", e.User); err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 2)
	go func() {
		done <- e.Do("POST", "/api/arena/live/agents/"+a[0].id+"/copy", `{"ratio":0.5,"maxUsd":1000}`).Code
	}()
	go func() {
		done <- e.Do("POST", "/api/autopilots", `{"strategyId":"`+sid+`","allocationUsd":1000,"maxDrawdownPct":20}`).Code
	}()
	copyWaiter(t, obs, pid)
	if err := hold.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	results := map[int]int{}
	for range 2 {
		select {
		case status := <-done:
			results[status]++
		case <-time.After(5 * time.Second):
			t.Fatal("creation race deadlocked")
		}
	}
	if results[201] != 1 || results[409] != 1 {
		t.Fatalf("both incompatible automations started: %+v", results)
	}
}

func TestCopyFIFOAttributionWithManualOpeningLot(t *testing.T) {
	e, a := copyEnv(t)
	if _, _, err := e.Paper.Order(ctx, e.User, paper.OrderRequest{Symbol: "BTC", Side: "buy", Notional: new(1000.0)}); err != nil {
		t.Fatal(err)
	}
	p := newCopy(t, e, a[0], 1, 1000)
	copyFill(t, e, a[0], 0, "buy", 1000)
	deliver(t, e)
	copyFill(t, e, a[0], 1, "sell", 500)
	deliver(t, e)
	plans, err := plansOf(ctx, e.Deps, e.User)
	if err != nil {
		t.Fatal(err)
	}
	v := plans[0]
	// The sale reduces the delivery book but FIFO closes the earlier manual lot.
	if v.Invested == nil || *v.Invested != 1000 || v.RealizedPnl == nil || *v.RealizedPnl != -1 || len(v.Positions) != 1 || v.Positions[0].Qty >= 1000/100020.0 || v.Trades != 2 {
		t.Fatalf("attribution mixed with virtual book: %+v", v)
	}
	if liveCount(t, e, "select count(*) from trades where ref_id=$1", p.ID) != 2 {
		t.Fatal("copy refs missing")
	}
}

func TestCopyStopOlderThanListWindow(t *testing.T) {
	e, a := copyEnv(t)
	p := newCopy(t, e, a[0], .5, 1000)
	if _, err := e.DB.Exec(ctx, "update copy_plans set created_at=now()-interval '2 days' where id=$1", p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.DB.Exec(ctx, `insert into copy_plans(user_id,agent_id,ratio,max_usd,status,stopped_at) select $1,$2,.5,1000,'stopped',now() from generate_series(1,25)`, e.User, a[0].id); err != nil {
		t.Fatal(err)
	}
	if w := e.Do("POST", "/api/copy-plans/"+p.ID+"/stop", `{"closePositions":false}`); w.Code != 200 {
		t.Fatalf("old active plan disappeared after stop: %d %s", w.Code, w.Body)
	}
}

func TestCopyOwnershipReadFailsClosed(t *testing.T) {
	e, a := copyEnv(t)
	p := newCopy(t, e, a[0], 1, 1000)
	copyFill(t, e, a[0], 0, "buy", 1000)
	// The pending-step query still works; the locked ownership/name lookup fails.
	if _, err := e.DB.Exec(ctx, "alter table live_agents rename to unavailable_live_agents"); err != nil {
		t.Fatal(err)
	}
	if err := recoverCopies(ctx, e.Deps, time.Now()); err == nil {
		t.Fatal("ownership query failure granted exposure")
	}
	if got := liveCount(t, e, "select count(*) from trades where ref_id=$1", p.ID); got != 0 {
		t.Fatalf("filled without ownership: %d", got)
	}
	if _, err := e.DB.Exec(ctx, "alter table unavailable_live_agents rename to live_agents"); err != nil {
		t.Fatal(err)
	}
	deliver(t, e)
	if got := liveCount(t, e, "select count(*) from trades where ref_id=$1", p.ID); got != 1 {
		t.Fatalf("ownership recovery: %d", got)
	}
}

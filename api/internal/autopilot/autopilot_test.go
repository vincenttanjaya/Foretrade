package autopilot

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
	"github.com/vincenttanjaya/Foretrade/api/internal/strategy"
)

const iv = 4 * 3600 // the specs below trade 4h bars

var (
	ctx = context.Background()
	// Injected clocks: at End the fake's newest bar is still open, an interval later it has closed.
	bar1, bar2 = markettest.End, markettest.End.Add(iv * time.Second)
)

// always is a rule set that holds on every bar.
func always() *strategy.RuleSet {
	zero := 0.0
	return &strategy.RuleSet{All: []strategy.Condition{{Left: strategy.Indicator{Kind: "price"}, Op: ">",
		Right: strategy.Indicator{Kind: "const", Value: &zero}}}}
}

func spec(mutate func(*strategy.Spec)) strategy.Spec {
	s := strategy.Spec{Name: "Always", Symbol: "SOL", Interval: "4h", Bars: 300, PositionPct: 50, FeePct: 0.05, LongEntry: always()}
	if mutate != nil {
		mutate(&s)
	}
	return s
}

// deployed inserts a running autopilot for the env's user, as deploy does.
func deployed(t *testing.T, e *apptest.Env, s strategy.Spec, alloc, maxDD float64) *pilot {
	t.Helper()
	p, err := scan(e.DB.QueryRow(ctx, `insert into autopilots (user_id, strategy_id, name, spec, allocation_usd, max_drawdown_pct, status, peak_equity)
		values ($1, 'test', $2, $3, $4, $5, 'running', $4) returning `+columns, e.User, s.Name, s, alloc, maxDD))
	if err != nil {
		t.Fatal(err)
	}
	return &p
}

func mustStep(t *testing.T, e *apptest.Env, p *pilot, now time.Time) []paper.Trade {
	t.Helper()
	trades, _, err := step(ctx, e.Deps, p, now)
	if err != nil {
		t.Fatal(err)
	}
	return trades
}

func holdingOf(t *testing.T, e *apptest.Env, symbol string) *paper.Position {
	t.Helper()
	pf, err := e.Paper.Portfolio(ctx, e.User)
	if err != nil {
		t.Fatal(err)
	}
	return position(pf, symbol)
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6*max(1, math.Abs(b)) }

// fresh drops the market caches, so a SetPrice or an outage shows at once.
func fresh(e *apptest.Env) {
	e.Market = testMarketClient(e.Binance.URL)
	e.Paper = paper.NewEngine(e.DB, e.Market)
}

func TestStepEntrySizingExitAndSameBar(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	p := deployed(t, e, spec(func(s *strategy.Spec) { s.LongExit, s.StopLossPct, s.TakeProfitPct = always(), 5, 10 }), 10_000, 20)
	mark := markettest.Price("SOLUSDT")

	in := mustStep(t, e, p, bar1)
	if len(in) != 1 || in[0].Side != "buy" || !near(in[0].Notional, 5000) || in[0].Source != "autopilot" || in[0].Note != "Autopilot · Always" {
		t.Fatalf("entry: %+v", in)
	}
	pos := holdingOf(t, e, "SOL")
	if pos == nil || !near(pos.Qty, in[0].Qty) || !near(*pos.StopLoss, mark*0.95) || !near(*pos.TakeProfit, mark*1.1) {
		t.Fatalf("paper position: %+v", pos)
	}
	if !near(p.Qty, in[0].Qty) || !near(p.Avg, in[0].Price) || !near(p.Realized, -in[0].Fee) || *p.LastBarT != bar1.Unix()-iv ||
		p.Log[0].Kind != "entry" || !strings.Contains(p.Log[0].Text, "Opened Long") || !strings.Contains(p.Log[0].Text, "stop $190 · target $220") {
		t.Fatalf("books after entry: %+v", p)
	}

	// The same closed bar is never decided twice, though its exit rule holds.
	logs := len(p.Log)
	if again := mustStep(t, e, p, bar1.Add(29*time.Second)); len(again) != 0 || len(p.Log) != logs || *p.LastBarT != bar1.Unix()-iv {
		t.Fatalf("same bar re-evaluated: %+v", again)
	}

	out := mustStep(t, e, p, bar2)
	if len(out) != 1 || out[0].Side != "sell" || !near(out[0].Qty, in[0].Qty) || p.Qty != 0 || holdingOf(t, e, "SOL") != nil {
		t.Fatalf("exit: %+v, qty %v", out, p.Qty)
	}
	want := -in[0].Fee + in[0].Qty*(out[0].Price-in[0].Price) - out[0].Fee
	if !near(p.Realized, want) || p.Log[0].Kind != "exit" || *p.LastBarT != bar2.Unix()-iv {
		t.Fatalf("books after exit: realized %v (want %v), log %+v", p.Realized, want, p.Log[0])
	}
	// The exit line is the round trip net of both fees: what the card's P&L shows once flat.
	if text := fmt.Sprintf("Closed %s at %s: %s net of fees.", holding(in[0].Qty, "SOL"), usd(out[0].Price), signedUSD(want)); p.Log[0].Text != text {
		t.Fatalf("exit log %q, want %q", p.Log[0].Text, text)
	}
	// Saved as it went.
	var qty, realized float64
	var last int64
	e.DB.QueryRow(ctx, "select position_qty, realized_pnl, last_bar_t from autopilots where id = $1", p.ID).Scan(&qty, &realized, &last)
	if qty != 0 || !near(realized, want) || last != *p.LastBarT {
		t.Fatalf("row: %v %v %v", qty, realized, last)
	}
}

// A trailing stop enters at the tighter of the trail and the fixed stop, then
// follows a rising mark on every check (same bar, no decision) and never steps back.
func TestStepTrailsTheStop(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	p := deployed(t, e, spec(func(s *strategy.Spec) { s.StopLossPct, s.TakeProfitPct, s.TrailingStopPct = 5, 30, 3 }), 10_000, 20)
	mark := markettest.Price("SOLUSDT")
	stop := func() float64 { return *holdingOf(t, e, "SOL").StopLoss }
	mustStep(t, e, p, bar1)
	if !near(stop(), mark*0.97) {
		t.Fatalf("entry stop %v, want the 3%% trail %v", stop(), mark*0.97)
	}
	logs := len(p.Log)
	for _, tc := range []struct{ px, stop float64 }{
		{1.1, 1.1 * 0.97},    // up 10%: raised
		{1.08, 1.1 * 0.97},   // down again: kept
		{1.1005, 1.1 * 0.97}, // a new high under 0.1% above the stop's: kept
		{1.15, 1.15 * 0.97},  // a new high: raised again
	} {
		e.Binance.SetPrice("SOLUSDT", mark*tc.px)
		fresh(e)
		if mustStep(t, e, p, bar1); !near(stop(), mark*tc.stop) {
			t.Fatalf("mark ×%v: stop %v, want %v", tc.px, stop(), mark*tc.stop)
		}
	}
	if len(p.Log) != logs || p.Qty == 0 {
		t.Fatalf("trail moves are not logged: %+v", p.Log)
	}

	// A short trails down from the lowest mark.
	e.Binance.SetPrice("SOLUSDT", mark)
	fresh(e)
	short := deployed(t, otherUser(t, e), spec(func(s *strategy.Spec) {
		s.LongEntry, s.ShortEntry, s.TakeProfitPct, s.TrailingStopPct = nil, always(), 30, 3
	}), 10_000, 20)
	if mustStep(t, e, short, bar1); short.Qty >= 0 || !near(stop(), mark*1.03) {
		t.Fatalf("short entry: qty %v, stop %v", short.Qty, stop())
	}
	for _, tc := range []struct{ px, stop float64 }{{0.9, 0.9 * 1.03}, {0.95, 0.9 * 1.03}} {
		e.Binance.SetPrice("SOLUSDT", mark*tc.px)
		fresh(e)
		if mustStep(t, e, short, bar1); !near(stop(), mark*tc.stop) {
			t.Fatalf("short, mark ×%v: stop %v, want %v", tc.px, stop(), mark*tc.stop)
		}
	}
}

func TestStepFlip(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	p := deployed(t, e, spec(func(s *strategy.Spec) { s.ShortEntry = always() }), 10_000, 20)
	if in := mustStep(t, e, p, bar1); len(in) != 1 || p.Qty <= 0 {
		t.Fatalf("long wins the tie: %+v", in)
	}
	long := p.Qty
	flip := mustStep(t, e, p, bar2)
	if len(flip) != 2 || flip[0].Side != "sell" || !near(flip[0].Qty, long) || flip[1].Side != "sell" || !near(flip[1].Notional, 5000) ||
		!near(p.Qty, -flip[1].Qty) || p.Log[0].Kind != "entry" || !strings.HasPrefix(p.Log[0].Text, "Opened Short") || p.Log[1].Kind != "exit" {
		t.Fatalf("flip: %+v, books %+v", flip, p)
	}
	if pos := holdingOf(t, e, "SOL"); pos == nil || !near(pos.Qty, p.Qty) {
		t.Fatalf("paper position after the flip: %+v", pos)
	}
}

func TestStepSyncAfterStopLoss(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	p := deployed(t, e, spec(func(s *strategy.Spec) { s.StopLossPct = 5 }), 10_000, 20)
	in := mustStep(t, e, p, bar1)
	stop, _, err := e.Paper.Close(ctx, e.User, "SOL", "stop_loss") // as the watcher fills it
	if err != nil {
		t.Fatal(err)
	}
	if again := mustStep(t, e, p, bar1); len(again) != 0 || p.Qty != 0 || p.Log[0].Kind != "sync" ||
		p.Log[0].Text != "Position closed outside the autopilot (stop loss)." {
		t.Fatalf("sync: %+v, books %+v", again, p)
	}
	if want := -in[0].Fee + in[0].Qty*(stop.Price-in[0].Price) - stop.Fee; !near(p.Realized, want) {
		t.Fatalf("realized %v, want %v", p.Realized, want)
	}
}

func TestStepClampsAReducedPosition(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	p := deployed(t, e, spec(nil), 10_000, 20)
	in := mustStep(t, e, p, bar1)
	half := in[0].Qty / 2
	if _, _, err := e.Paper.Order(ctx, e.User, paper.OrderRequest{Symbol: "SOL", Side: "sell", Qty: &half}); err != nil {
		t.Fatal(err)
	}
	if mustStep(t, e, p, bar1); !near(p.Qty, half) || p.Log[0].Kind != "sync" {
		t.Fatalf("clamp: %+v", p)
	}
}

func TestStepDrawdownStop(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	p := deployed(t, e, spec(func(s *strategy.Spec) { s.PositionPct = 100 }), 10_000, 10)
	mustStep(t, e, p, bar1)
	e.Binance.SetPrice("SOLUSDT", markettest.Price("SOLUSDT")*0.85) // −15% against a 10% cap
	fresh(e)
	out := mustStep(t, e, p, bar1)
	if len(out) != 1 || out[0].Side != "sell" || p.Qty != 0 || p.Status != "stopped" || *p.StopReason != "max_drawdown" || p.StoppedAt == nil ||
		p.Log[0].Text != "Max drawdown 10% hit: position closed, autopilot stopped." || holdingOf(t, e, "SOL") != nil {
		t.Fatalf("drawdown stop: %+v, books %+v", out, p)
	}
	var status string
	e.DB.QueryRow(ctx, "select status from autopilots where id = $1", p.ID).Scan(&status)
	if status != "stopped" {
		t.Fatalf("row status %s", status)
	}
}

func TestStepRefusalsAreLogged(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	used := 199_000.0 // nearly all of the 2× gross limit
	if _, _, err := e.Paper.Order(ctx, e.User, paper.OrderRequest{Symbol: "BTC", Side: "buy", Notional: &used}); err != nil {
		t.Fatal(err)
	}
	p := deployed(t, e, spec(nil), 10_000, 20)
	if out := mustStep(t, e, p, bar1); len(out) != 0 || p.Qty != 0 || p.Log[0].Text != "Not enough buying power; skipped." ||
		*p.LastBarT != bar1.Unix()-iv {
		t.Fatalf("buying power: %+v", p)
	}
	tiny := deployed(t, otherUser(t, e), spec(func(s *strategy.Spec) { s.PositionPct = 5 }), 100, 20)
	if out := mustStep(t, e, tiny, bar1); len(out) != 0 || tiny.Log[0].Kind != "error" || !strings.Contains(tiny.Log[0].Text, "$10 minimum") {
		t.Fatalf("under the minimum: %+v", tiny.Log)
	}

	e.Binance.Down("", true)
	fresh(e)
	down := deployed(t, otherUser(t, e), spec(nil), 10_000, 20)
	if _, _, err := step(ctx, e.Deps, down, bar1); err == nil || down.LastBarT != nil {
		t.Fatalf("market down: err %v, last bar %v", err, down.LastBarT)
	}
}

// otherUser points e at a fresh user (one running autopilot per user).
func otherUser(t *testing.T, e *apptest.Env) *apptest.Env {
	t.Helper()
	e.User = storetest.User(t, e.DB)
	e.Cookie = e.Session(e.User)
	return e
}

func TestTickStepsRunningAutopilots(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	p := deployed(t, e, spec(nil), 10_000, 20)
	qty := func() (q float64) {
		e.DB.QueryRow(ctx, "select position_qty from autopilots where id = $1", p.ID).Scan(&q)
		return q
	}
	// Another API instance holds the lock: this one's tick leaves the row alone.
	other, err := e.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	other.Exec(ctx, "select pg_advisory_xact_lock($1)", lockKey)
	Check(ctx, e.Deps, bar1)
	other.Rollback(ctx)
	if qty() != 0 {
		t.Fatal("a tick acted while another instance held the lock")
	}
	// Free again: it enters. (Retried: parallel test packages share the database and its advisory locks.)
	for i := 0; i < 50 && qty() == 0; i++ {
		Check(ctx, e.Deps, bar1)
		time.Sleep(20 * time.Millisecond)
	}
	var log string
	e.DB.QueryRow(ctx, "select log::text from autopilots where id = $1", p.ID).Scan(&log)
	if qty() <= 0 || !strings.Contains(log, `"kind": "entry"`) {
		t.Fatalf("tick: qty %v, log %s", qty(), log)
	}
}

// The loader stops the autopilot of an account an admin disabled (by hand, or
// before the admin route stopped it): no order, the position stays in the
// account, and the books close at the mark.
func TestLoadStopsDisabledAccounts(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	p := deployed(t, e, spec(func(s *strategy.Spec) { s.LongExit = always() }), 10_000, 20)
	in := mustStep(t, e, p, bar1)
	if _, err := e.DB.Exec(ctx, "update users set disabled_at = now() where id = $1", e.User); err != nil {
		t.Fatal(err)
	}
	if err := load(ctx, e.Deps, p.ID, bar2); err != nil { // bar2's exit rule holds, but no order goes out
		t.Fatal(err)
	}
	got, err := scan(e.DB.QueryRow(ctx, "select "+columns+" from autopilots where id = $1", p.ID))
	if err != nil {
		t.Fatal(err)
	}
	mark := markettest.Price("SOLUSDT")
	if got.Status != "stopped" || *got.StopReason != "account_disabled" || got.StoppedAt == nil || got.Qty != 0 ||
		!near(got.Realized, -in[0].Fee+in[0].Qty*(mark-in[0].Price)) || got.Log[0].Kind != "stop" ||
		got.Log[0].Text != "Account disabled by an admin; position kept." {
		t.Fatalf("after the loader: %+v", got)
	}
	var trades int
	e.DB.QueryRow(ctx, "select count(*) from trades where user_id = $1", e.User).Scan(&trades)
	if pos := holdingOf(t, e, "SOL"); trades != 1 || pos == nil || !near(pos.Qty, in[0].Qty) {
		t.Fatalf("trades %d, position %+v", trades, pos)
	}
	if err := load(ctx, e.Deps, p.ID, bar2.Add(iv*time.Second)); !errors.Is(err, pgx.ErrNoRows) { // stopped: the tick skips it
		t.Fatalf("second load: %v", err)
	}
}

// On a halted pair stopping with a close still fills, at the last price, and a
// new entry is refused and logged like any refusal (the bar is done). A pair
// Binance stopped pricing makes stop a 502, not a 500, and the autopilot runs on.
func TestHaltedPair(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	p := deployed(t, e, spec(func(s *strategy.Spec) { s.StopLossPct, s.TakeProfitPct = 5, 10 }), 10_000, 20)
	if in := mustStep(t, e, p, bar1); len(in) != 1 {
		t.Fatalf("entry: %+v", in)
	}
	e.Binance.Halt("SOLUSDT", true)
	fresh(e)
	w := e.Do("POST", "/api/autopilots/"+p.ID+"/stop", map[string]bool{"closePosition": true})
	stopped := apptest.JSON[deployResp](t, w)
	if w.Code != 200 || stopped.Autopilot.Status != "stopped" || stopped.Trade == nil ||
		stopped.Trade.Price != paper.FillPrice(markettest.Price("SOLUSDT"), false) || position(*stopped.Portfolio, "SOL") != nil {
		t.Fatalf("stop and close: %d %s", w.Code, w.Body)
	}

	entry := deployed(t, otherUser(t, e), spec(nil), 10_000, 20)
	if out := mustStep(t, e, entry, bar1); len(out) != 0 || entry.Qty != 0 || *entry.LastBarT != bar1.Unix()-iv ||
		entry.Log[0].Text != "Order refused: SOL is halted on Binance, so new orders are paused; reducing or closing a position still fills at its last price." {
		t.Fatalf("entry: %+v", entry.Log)
	}

	aave := deployed(t, otherUser(t, e), spec(func(s *strategy.Spec) { s.Symbol = "AAVE" }), 10_000, 20)
	if in := mustStep(t, e, aave, bar1); len(in) != 1 {
		t.Fatalf("AAVE entry: %+v", in)
	}
	e.Binance.Down("AAVEUSDT", true)
	fresh(e)
	if w := e.Do("POST", "/api/autopilots/"+aave.ID+"/stop", map[string]bool{"closePosition": false}); w.Code != 502 || apptest.ErrorCode(t, w) != "upstream" {
		t.Fatalf("stop without a price: %d %s", w.Code, w.Body)
	}
}

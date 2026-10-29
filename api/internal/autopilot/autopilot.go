// Package autopilot runs a saved or built-in strategy on the paper engine:
// /api/autopilots*. A server goroutine checks every running autopilot each
// closed bar with strategy.Decide and places paper orders (source
// "autopilot"); Go rules only, no model call, no credits. One running
// autopilot per user, stopped by the user or by its drawdown cap.
package autopilot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/httpx"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
	"github.com/vincenttanjaya/Foretrade/api/internal/strategy"
)

// Entry is one line of an autopilot's log (contract Autopilot.log).
type Entry struct {
	T    time.Time `json:"t"`
	Kind string    `json:"kind"` // deploy | entry | exit | sync | stop | error
	Text string    `json:"text"`
}

// Autopilot is the contract Autopilot. The five valuation fields are nil
// when market data is unavailable.
type Autopilot struct {
	ID             string     `json:"id"`
	StrategyID     string     `json:"strategyId"`
	Name           string     `json:"name"`
	Symbol         string     `json:"symbol"`
	Interval       string     `json:"interval"`
	Status         string     `json:"status"`     // running | stopped
	StopReason     *string    `json:"stopReason"` // user | max_drawdown | account_disabled
	AllocationUsd  float64    `json:"allocationUsd"`
	MaxDrawdownPct float64    `json:"maxDrawdownPct"`
	PositionQty    float64    `json:"positionQty"`
	AvgPrice       float64    `json:"avgPrice"`
	MarkPrice      *float64   `json:"markPrice"`
	Equity         *float64   `json:"equity"`
	Pnl            *float64   `json:"pnl"`
	PnlPct         *float64   `json:"pnlPct"`
	DrawdownPct    *float64   `json:"drawdownPct"`
	LastBarT       *int64     `json:"lastBarT"`    // open time of the last closed bar it decided on
	NextCheckAt    *time.Time `json:"nextCheckAt"` // close of the bar after lastBarT, while running
	Log            []Entry    `json:"log"`         // newest first, ≤ 50
	StartedAt      time.Time  `json:"startedAt"`
	StoppedAt      *time.Time `json:"stoppedAt"`

	// The snapshot's risk levels, in percent; nil = none.
	StopLossPct     *float64 `json:"stopLossPct"`
	TakeProfitPct   *float64 `json:"takeProfitPct"`
	TrailingStopPct *float64 `json:"trailingStopPct"`
}

// pilot is an autopilots row: the strategy snapshot and the autopilot's own
// books (its position, average cost and realized P&L net of fees).
type pilot struct {
	ID, UserID, StrategyID, Name string
	Spec                         strategy.Spec
	Allocation, MaxDD            float64
	Status                       string
	StopReason                   *string
	Qty, Avg, Realized, Peak     float64
	LastBarT                     *int64
	Log                          []Entry
	StartedAt                    time.Time
	StoppedAt                    *time.Time
}

const (
	columns = `id, user_id, strategy_id, name, spec, allocation_usd, max_drawdown_pct, status, stop_reason,
		position_qty, avg_price, realized_pnl, peak_equity, last_bar_t, log, started_at, stopped_at`
	maxLog   = 50
	minOrder = 10 // the paper engine's minimum notional
)

// lockKey is the Postgres advisory lock behind every step and stop, shared
// by all API instances: two tickers, or a tick and a stop, never act on an
// autopilot at once. mu keeps it to one waiting connection per process.
// ponytail: one lock for all autopilots; per-user keys if many ever run.
const lockKey = 7_241_071

var slot = make(chan struct{}, 1) // one lock waiter/holder, with cancelable process admission

// hold takes the autopilot lock, waiting for it unless try; ok is false when
// try finds it taken. release ends the transaction that holds it.
func hold(ctx context.Context, d *app.Deps, try bool) (release func(), ok bool, err error) {
	select {
	case slot <- struct{}{}:
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
	tx, err := d.DB.Begin(ctx)
	if err != nil {
		<-slot
		return nil, false, err
	}
	release = func() {
		app.Rollback(ctx, tx)
		<-slot
	}
	ok = true
	if try {
		err = tx.QueryRow(ctx, "select pg_try_advisory_xact_lock($1)", lockKey).Scan(&ok)
	} else {
		_, err = tx.Exec(ctx, "select pg_advisory_xact_lock($1)", lockKey)
	}
	if err != nil || !ok {
		release()
		return nil, false, err
	}
	return release, true, nil
}

func scan(row pgx.Row) (pilot, error) {
	var p pilot
	err := row.Scan(&p.ID, &p.UserID, &p.StrategyID, &p.Name, &p.Spec, &p.Allocation, &p.MaxDD, &p.Status, &p.StopReason,
		&p.Qty, &p.Avg, &p.Realized, &p.Peak, &p.LastBarT, &p.Log, &p.StartedAt, &p.StoppedAt)
	return p, err
}

// execer is the pool or the transaction a save runs on.
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// save writes the mutable state of a row that is still running.
func (p *pilot) save(ctx context.Context, db execer) error {
	_, err := db.Exec(ctx, `update autopilots set status = $2, stop_reason = $3, position_qty = $4, avg_price = $5,
		realized_pnl = $6, peak_equity = $7, last_bar_t = $8, log = $9, stopped_at = $10 where id = $1 and status = 'running'`,
		p.ID, p.Status, p.StopReason, p.Qty, p.Avg, p.Realized, p.Peak, p.LastBarT, p.Log, p.StoppedAt)
	return err
}

func (p *pilot) log(now time.Time, kind, text string) {
	p.Log = append([]Entry{{T: now.UTC(), Kind: kind, Text: text}}, p.Log...)[:min(len(p.Log)+1, maxLog)]
}

func (p *pilot) stop(reason string, now time.Time) {
	t := now.UTC()
	p.Status, p.StopReason, p.StoppedAt = "stopped", &reason, &t
}

// disable stops p for an account an admin disabled: the position stays in
// the account, and the books close at mark when there is one.
func (p *pilot) disable(mark *float64, now time.Time) {
	if mark != nil && p.Qty != 0 {
		p.Realized += p.Qty * (*mark - p.Avg)
		p.Qty, p.Avg = 0, 0
	}
	p.stop("account_disabled", now)
	p.log(now, "stop", "Account disabled by an admin; position kept.")
}

// StopForDisabled stops userID's running autopilots inside tx, the admin
// transaction that disables the account. It makes no market call, so the
// position stays in the account and the books stay as they are. It first
// takes the autopilot lock, so a step in progress finishes before, and none
// starts until tx ends. It returns how many it stopped.
// LockTx precedes user and account row locks in admin disable/delete paths.
func LockTx(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, "select pg_advisory_xact_lock($1)", lockKey)
	return err
}

func StopForDisabled(ctx context.Context, tx pgx.Tx, userID string) (int, error) {
	if _, err := tx.Exec(ctx, "select pg_advisory_xact_lock($1)", lockKey); err != nil {
		return 0, err
	}
	rows, _ := tx.Query(ctx, "select "+columns+" from autopilots where user_id = $1 and status = 'running'", userID)
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (pilot, error) { return scan(row) })
	if err != nil {
		return 0, err
	}
	now := time.Now()
	for i := range list {
		list[i].disable(nil, now)
		if err := list[i].save(ctx, tx); err != nil {
			return 0, err
		}
	}
	return len(list), nil
}

func (p *pilot) equity(mark float64) float64 { return p.Allocation + p.Realized + p.Qty*(mark-p.Avg) }

func (p *pilot) side() string {
	switch {
	case p.Qty > 0:
		return "long"
	case p.Qty < 0:
		return "short"
	}
	return "flat"
}

// view values the row at mark (nil: market data unavailable).
func (p *pilot) view(mark *float64) Autopilot {
	a := Autopilot{ID: p.ID, StrategyID: p.StrategyID, Name: p.Name, Symbol: p.Spec.Symbol, Interval: p.Spec.Interval,
		Status: p.Status, StopReason: p.StopReason, AllocationUsd: p.Allocation, MaxDrawdownPct: p.MaxDD, PositionQty: p.Qty,
		AvgPrice: p.Avg, MarkPrice: mark, LastBarT: p.LastBarT, Log: p.Log, StartedAt: p.StartedAt, StoppedAt: p.StoppedAt,
		StopLossPct: pct(p.Spec.StopLossPct), TakeProfitPct: pct(p.Spec.TakeProfitPct), TrailingStopPct: pct(p.Spec.TrailingStopPct)}
	if a.Log == nil {
		a.Log = []Entry{}
	}
	if mark != nil {
		eq := p.equity(*mark)
		peak := max(p.Peak, eq)
		a.Equity, a.Pnl = ptr(round2(eq)), ptr(round2(eq-p.Allocation))
		a.PnlPct, a.DrawdownPct = ptr(round2((eq-p.Allocation)/p.Allocation*100)), ptr(round2((peak-eq)/peak*100))
	}
	if p.Status == "running" && p.LastBarT != nil {
		next := time.Unix(*p.LastBarT+2*market.BarSeconds(p.Spec.Interval), 0).UTC()
		a.NextCheckAt = &next
	}
	return a
}

// Register mounts /api/autopilots* behind the session check.
func Register(mux *http.ServeMux, d *app.Deps) {
	h := &handler{d}
	auth := func(f http.HandlerFunc) http.Handler { return d.Auth.Require(f) }
	mux.Handle("POST /api/autopilots", auth(h.deploy))
	mux.Handle("GET /api/autopilots", auth(h.list))
	mux.Handle("GET /api/autopilots/{id}", auth(h.get))
	mux.Handle("POST /api/autopilots/{id}/stop", auth(h.stop))
}

type handler struct{ d *app.Deps }

func (h *handler) deploy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StrategyID     string  `json:"strategyId"`
		AllocationUsd  float64 `json:"allocationUsd"`
		MaxDrawdownPct float64 `json:"maxDrawdownPct"`
		// Overrides of the snapshotted spec's levels; null or absent keeps the spec's.
		StopLossPct     *float64 `json:"stopLossPct"`
		TakeProfitPct   *float64 `json:"takeProfitPct"`
		TrailingStopPct *float64 `json:"trailingStopPct"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		app.WriteError(w, &app.BadRequest{Msg: err.Error()})
		return
	}
	switch {
	case !(req.AllocationUsd >= 100 && req.AllocationUsd <= 100_000):
		app.WriteError(w, &app.BadRequest{Msg: "allocationUsd must be 100..100,000."})
		return
	case !(req.MaxDrawdownPct >= 1 && req.MaxDrawdownPct <= 50):
		app.WriteError(w, &app.BadRequest{Msg: "maxDrawdownPct must be 1..50."})
		return
	}
	for _, o := range []struct {
		name   string
		v      *float64
		lo, hi float64
	}{{"stopLossPct", req.StopLossPct, 0.1, 50}, {"takeProfitPct", req.TakeProfitPct, 0.1, 50}, {"trailingStopPct", req.TrailingStopPct, 0.5, 50}} {
		if o.v != nil && !(*o.v >= o.lo && *o.v <= o.hi) {
			app.WriteError(w, &app.BadRequest{Msg: fmt.Sprintf("%s must be %g to %g.", o.name, o.lo, o.hi)})
			return
		}
	}
	ctx, uid := r.Context(), httpx.UserID(r.Context())
	st, err := strategy.Find(ctx, h.d, uid, req.StrategyID)
	if err != nil {
		app.WriteError(w, err)
		return
	}
	spec := st.Spec
	if req.StopLossPct != nil {
		spec.StopLossPct = *req.StopLossPct
	}
	if req.TakeProfitPct != nil {
		spec.TakeProfitPct = *req.TakeProfitPct
	}
	if req.TrailingStopPct != nil {
		spec.TrailingStopPct = *req.TrailingStopPct
	}
	// An autopilot entry never runs naked: it always carries a stop and a target.
	if spec.StopLossPct <= 0 && spec.TrailingStopPct <= 0 || spec.TakeProfitPct <= 0 {
		app.WriteError(w, &app.BadRequest{Msg: "Autopilot entries always carry a stop and a target: set stopLossPct (or trailingStopPct) and takeProfitPct."})
		return
	}

	release, _, err := hold(ctx, h.d, false)
	if err != nil {
		app.WriteError(w, err)
		return
	}
	defer release()
	var p pilot
	err = pgx.BeginFunc(ctx, h.d.DB, func(tx pgx.Tx) error {
		var disabled bool
		if err := tx.QueryRow(ctx, "select disabled_at is not null from users where id=$1 for no key update", uid).Scan(&disabled); err != nil {
			return err
		}
		if disabled {
			return app.ErrNotFound
		}
		if _, err := h.d.Paper.LockTx(ctx, tx, uid, nil); err != nil {
			return err
		}
		if slices.ContainsFunc(market.Core, func(a market.AssetInfo) bool { return a.Symbol == spec.Symbol }) {
			var overlap bool
			if err := tx.QueryRow(ctx, "select exists(select 1 from copy_plans where user_id=$1 and status='active')", uid).Scan(&overlap); err != nil {
				return err
			}
			if overlap {
				return &app.Conflict{Msg: "Stop your copy plans before deploying a core-asset autopilot."}
			}
		}
		var err error
		p, err = scan(tx.QueryRow(ctx, `insert into autopilots(user_id,strategy_id,name,spec,allocation_usd,max_drawdown_pct,status,peak_equity)
   values($1,$2,$3,$4,$5,$6,'running',$5) on conflict(user_id) where status='running' do nothing returning `+columns, uid, st.ID, st.Name, spec, req.AllocationUsd, req.MaxDrawdownPct))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) { // autopilots_one_running
		var name string
		if err := h.d.DB.QueryRow(ctx, "select name from autopilots where user_id = $1 and status = 'running'", uid).Scan(&name); err != nil {
			app.WriteError(w, err)
			return
		}
		app.WriteError(w, &app.Conflict{Msg: fmt.Sprintf("You already have an autopilot running (%s). Stop it first.", name)})
		return
	}
	if err != nil {
		app.WriteError(w, err)
		return
	}
	now := time.Now()
	p.log(now, "deploy", fmt.Sprintf("Deployed with %s; checks every closed %s bar · %s.", usd(p.Allocation), p.Spec.Interval, risk(p.Spec)))
	// A rule that already holds on the last close enters now, not a bar later.
	sctx, cancel := context.WithTimeout(app.Detached(ctx), 20*time.Second)
	defer cancel()
	trades, pf, err := step(sctx, h.d, &p, now)
	if err != nil {
		slog.Warn("autopilot first check", "id", p.ID, "err", err)
		p.log(now, "error", "Market data is unavailable right now; the next check retries.")
		if err := p.save(sctx, h.d.DB); err != nil {
			app.WriteError(w, err)
			return
		}
	}
	h.respond(w, r, http.StatusCreated, p, trades, pf)
}

// respond writes {autopilot, trade, portfolio}: the last fill (or null) and
// the account after it (null when market data is down).
func (h *handler) respond(w http.ResponseWriter, r *http.Request, status int, p pilot, trades []paper.Trade, pf *paper.Portfolio) {
	var trade *paper.Trade
	if len(trades) > 0 {
		trade = &trades[len(trades)-1]
	}
	if pf == nil {
		if cur, err := h.d.Paper.Portfolio(r.Context(), p.UserID); err == nil {
			pf = &cur
		}
	}
	marks := h.marks(r.Context())
	httpx.JSON(w, status, map[string]any{"autopilot": p.view(marks[p.Spec.Symbol]), "trade": trade, "portfolio": pf})
}

// marks are the live prices by symbol; empty when market data is down.
func (h *handler) marks(ctx context.Context) map[string]*float64 {
	out := map[string]*float64{}
	assets, err := h.d.Market.Assets(ctx)
	if err != nil {
		slog.Warn("autopilot marks", "err", err)
	}
	for _, a := range assets {
		out[a.Symbol] = ptr(a.Price)
	}
	return out
}

// ponytail: newest 20, no pagination; one runs at a time, so that is weeks of history.
func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out, err := List(ctx, h.d, httpx.UserID(ctx), 20)
	if err != nil {
		app.WriteError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"autopilots": out})
}

// own loads the caller's autopilot id; anything else is app.ErrNotFound.
func (h *handler) own(ctx context.Context, id string) (pilot, error) {
	if !validID(id) {
		return pilot{}, app.ErrNotFound
	}
	return scan(h.d.DB.QueryRow(ctx, "select "+columns+" from autopilots where id = $1 and user_id = $2", id, httpx.UserID(ctx)))
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, err := h.own(ctx, r.PathValue("id"))
	if err != nil {
		app.WriteError(w, err)
		return
	}
	// Its fills: autopilot trades on its symbol while it ran (+1 min for a closing fill).
	rows, _ := h.d.DB.Query(ctx, `
		select id, symbol, side, qty, price, notional, fee, realized_pnl, source, note, created_at from trades
		where user_id = $1 and symbol = $2 and source = 'autopilot'
		  and created_at >= $3 and created_at <= coalesce($4::timestamptz + interval '1 minute', now())
		order by created_at desc, seq desc limit 50`, p.UserID, p.Spec.Symbol, p.StartedAt, p.StoppedAt)
	trades, err := pgx.CollectRows(rows, pgx.RowToStructByPos[paper.Trade])
	if err != nil {
		app.WriteError(w, err)
		return
	}
	if trades == nil {
		trades = []paper.Trade{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"autopilot": p.view(h.marks(ctx)[p.Spec.Symbol]), "trades": trades})
}

func (h *handler) stop(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ClosePosition bool `json:"closePosition"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		app.WriteError(w, &app.BadRequest{Msg: err.Error()})
		return
	}
	ctx := r.Context()
	release, _, err := hold(ctx, h.d, false)
	if err != nil {
		app.WriteError(w, err)
		return
	}
	defer release()
	p, err := h.own(ctx, r.PathValue("id"))
	if err != nil {
		app.WriteError(w, err)
		return
	}
	if p.Status != "running" {
		app.WriteError(w, &app.Conflict{Msg: "This autopilot is not running."})
		return
	}
	now := time.Now()
	pf, err := h.d.Paper.Portfolio(ctx, p.UserID)
	if err != nil {
		app.WriteError(w, err)
		return
	}
	if err := p.sync(ctx, h.d, pf, now); err != nil {
		app.WriteError(w, err)
		return
	}
	var trades []paper.Trade
	var after *paper.Portfolio
	switch {
	case p.Qty != 0 && req.ClosePosition:
		mark, err := h.d.Market.Price(ctx, p.Spec.Symbol)
		if err == nil {
			trades, after, err = p.trade(ctx, h.d, "flat", mark, now)
		}
		if err != nil {
			app.WriteError(w, err)
			return
		}
		if p.Qty != 0 { // a refused close was logged; keep it running
			if err := p.save(ctx, h.d.DB); err != nil {
				app.WriteError(w, err)
				return
			}
			app.WriteError(w, &app.Conflict{Msg: p.Log[0].Text})
			return
		}
	case p.Qty != 0:
		// The position stays in the account; the autopilot's books close at the mark.
		mark, err := h.d.Market.Price(ctx, p.Spec.Symbol)
		if err != nil {
			app.WriteError(w, err)
			return
		}
		p.log(now, "exit", fmt.Sprintf("Kept %s in your account, valued at %s.", holding(p.Qty, p.Spec.Symbol), usd(mark)))
		p.Realized += p.Qty * (mark - p.Avg)
		p.Qty, p.Avg = 0, 0
	}
	p.stop("user", now)
	p.log(now, "stop", "Stopped by you.")
	if err := p.save(ctx, h.d.DB); err != nil {
		app.WriteError(w, err)
		return
	}
	if after == nil {
		after = &pf
	}
	h.respond(w, r, http.StatusOK, p, trades, after)
}

// validID keeps malformed ids from reaching Postgres as a 500.
func validID(s string) bool {
	var u pgtype.UUID
	return len(s) == 36 && u.Scan(s) == nil
}

func ptr[T any](v T) *T { return &v }

// pct is a spec percentage for the API: nil when 0 (none).
func pct(x float64) *float64 {
	if x == 0 {
		return nil
	}
	return &x
}

// risk is "stop 5% · target 10% · trailing 3%" (each part only when set).
func risk(s strategy.Spec) string {
	var parts []string
	for _, l := range []struct {
		name string
		v    float64
	}{{"stop", s.StopLossPct}, {"target", s.TakeProfitPct}, {"trailing", s.TrailingStopPct}} {
		if l.v > 0 {
			parts = append(parts, fmt.Sprintf("%s %g%%", l.name, l.v))
		}
	}
	return strings.Join(parts, " · ")
}

func round2(x float64) float64 { return math.Round(x*100) / 100 }

// usd is "$5,000", "$142.10" or "$0.2013".
func usd(x float64) string {
	if math.Abs(x) < 1 {
		return "$" + strconv.FormatFloat(x, 'g', 4, 64)
	}
	whole, frac, _ := strings.Cut(strconv.FormatFloat(math.Abs(x), 'f', 2, 64), ".")
	for i := len(whole) - 3; i > 0; i -= 3 {
		whole = whole[:i] + "," + whole[i:]
	}
	s := "$" + whole
	if frac != "00" {
		s += "." + frac
	}
	if x < 0 {
		s = "-" + s
	}
	return s
}

// holding is "Long 0.42 SOL" / "Short 1,200 DOGE".
func holding(qty float64, symbol string) string {
	d := 6
	if math.Abs(qty) >= 1 {
		d = 4
	}
	n := strings.TrimRight(strings.TrimRight(strconv.FormatFloat(math.Abs(qty), 'f', d, 64), "0"), ".")
	if qty < 0 {
		return "Short " + n + " " + symbol
	}
	return "Long " + n + " " + symbol
}

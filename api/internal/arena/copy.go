package arena

// Copy plans (contract "Live arena and copy plans"): a user mirrors a live
// agent's fills into their own paper account at a ratio, under a dollar cap.
// Every leg goes through the paper engine (source "copy", ref_id = the plan),
// so account rules hold. Delivery books track ownership; P&L uses the engine's
// FIFO opening-lot attribution and can differ when manual holdings overlap.

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/httpx"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
)

const (
	minRatio, maxRatio = 0.1, 1.0
	minMaxUsd, maxMax  = 100.0, 50_000.0
	planLog            = 50
	planDust           = 1e-9
)

// PlanEntry is one line of a plan's log (contract CopyPlan.log).
type PlanEntry struct {
	T    time.Time `json:"t"`
	Kind string    `json:"kind"` // start | order | skip | sync | stop
	Text string    `json:"text"`
}

type PlanPosition struct {
	Symbol string  `json:"symbol"`
	Qty    float64 `json:"qty"`
}

// CopyPlan is the contract CopyPlan.
type CopyPlan struct {
	ID            string         `json:"id"`
	AgentID       string         `json:"agentId"`
	AgentName     string         `json:"agentName"`
	PersonaID     string         `json:"personaId"`
	Ratio         float64        `json:"ratio"`
	MaxUsd        float64        `json:"maxUsd"`
	Status        string         `json:"status"` // active | stopped
	StopReason    *string        `json:"stopReason"`
	Positions     []PlanPosition `json:"positions"`
	DeployedUsd   *float64       `json:"deployedUsd"`
	Invested      *float64       `json:"invested"`
	Trades        int            `json:"trades"`
	RealizedPnl   *float64       `json:"realizedPnl"`
	UnrealizedPnl *float64       `json:"unrealizedPnl"`
	Pnl           *float64       `json:"pnl"`
	Log           []PlanEntry    `json:"log"`
	CreatedAt     time.Time      `json:"createdAt"`
	StoppedAt     *time.Time     `json:"stoppedAt"`
}

// planRow is a copy_plans row with its agent's name and persona.
type planRow struct {
	id, userID, agentID, agentName, personaID string
	ratio, maxUsd                             float64
	status                                    string
	stopReason                                *string
	positions                                 map[string]float64 // the plan's own book: qty per symbol
	startBar                                  *int64
	lastStep                                  *int
	log                                       []PlanEntry
	createdAt                                 time.Time
	stoppedAt                                 *time.Time
	disabled                                  bool // the account is disabled (users.disabled_at)
}

const planColumns = `p.id, p.user_id, p.agent_id, a.name, a.persona_id, p.ratio, p.max_usd, p.status, p.stop_reason, p.positions, p.last_step, p.start_bar_t,
	p.log, p.created_at, p.stopped_at, u.disabled_at is not null
	from copy_plans p join live_agents a on a.id = p.agent_id join users u on u.id = p.user_id`

func scanPlan(row pgx.Row) (planRow, error) {
	var p planRow
	err := row.Scan(&p.id, &p.userID, &p.agentID, &p.agentName, &p.personaID, &p.ratio, &p.maxUsd, &p.status, &p.stopReason, &p.positions,
		&p.lastStep, &p.startBar, &p.log, &p.createdAt, &p.stoppedAt, &p.disabled)
	if p.positions == nil {
		p.positions = map[string]float64{}
	}
	if p.log == nil {
		p.log = []PlanEntry{}
	}
	return p, err
}

func (p *planRow) note() string { return "Copy · " + p.agentName }

func (p *planRow) add(now time.Time, kind, text string) {
	p.log = append([]PlanEntry{{T: now.UTC(), Kind: kind, Text: text}}, p.log...)[:min(len(p.log)+1, planLog)]
}

func (p *planRow) stop(reason string, now time.Time) {
	t := now.UTC()
	p.status, p.stopReason, p.stoppedAt = "stopped", &reason, &t
}

// save writes the mutable state of a plan that is still active.
func (p *planRow) save(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `update copy_plans set status = $2, stop_reason = $3, positions = $4, last_step = $5, log = $6, stopped_at = $7
		where id = $1 and status = 'active'`, p.id, p.status, p.stopReason, p.positions, p.lastStep, p.log, p.stoppedAt)
	return err
}

// hold books a signed quantity on the plan's own book.
func (p *planRow) hold(sym string, signed float64) {
	q := p.positions[sym] + signed
	if q < planDust {
		delete(p.positions, sym)
	} else {
		p.positions[sym] = q
	}
}

// deployed is the plan's book at marks (a symbol without a mark counts 0).
func (p *planRow) deployed(marks map[string]float64) float64 {
	sum := 0.0
	for sym, q := range p.positions {
		sum += q * marks[sym]
	}
	return sum
}

// accountQty is the account's signed quantity in sym.
func accountQty(pf paper.Portfolio, sym string) float64 {
	for _, pos := range pf.Positions {
		if pos.Symbol == sym {
			return pos.Qty
		}
	}
	return 0
}

// plansOf lists the user's plans, newest first (active first), valued at
// the live marks and attributed by ref (null when market data is down).
func plansOf(ctx context.Context, d *app.Deps, userID string) ([]CopyPlan, error) {
	tx, err := d.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer app.Rollback(ctx, tx)
	marks, _, _ := liveMarks(ctx, d.Market)
	out, err := plansOfTx(ctx, d, tx, userID, marks)
	if err != nil {
		return nil, err
	}
	return out, tx.Commit(ctx)
}

func plansOfTx(ctx context.Context, d *app.Deps, tx pgx.Tx, userID string, marks map[string]float64) ([]CopyPlan, error) {
	rows, _ := tx.Query(ctx, "select "+planColumns+" where p.user_id = $1 order by (p.status = 'active') desc, p.created_at desc limit 20", userID)
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (planRow, error) { return scanPlan(row) })
	if err != nil {
		return nil, err
	}
	out := make([]CopyPlan, 0, len(list))
	if len(list) == 0 {
		return out, nil
	}
	counts := map[string]int{}
	crows, _ := tx.Query(ctx, "select ref_id::text, count(*) from trades where user_id = $1 and source = 'copy' and ref_id is not null group by ref_id", userID)
	var ref string
	var n int
	if _, err := pgx.ForEachRow(crows, []any{&ref, &n}, func() error {
		counts[ref] = n
		return nil
	}); err != nil {
		return nil, err
	}
	var byRef map[string]paper.RefPnl
	if marks != nil {
		if byRef, err = d.Paper.ByRefTx(ctx, tx, userID, marks); err != nil {
			return nil, err
		}
	}
	for _, p := range list {
		out = append(out, p.view(marks, byRef, counts[p.id]))
	}
	return out, nil
}

func (p *planRow) view(marks map[string]float64, byRef map[string]paper.RefPnl, trades int) CopyPlan {
	v := CopyPlan{ID: p.id, AgentID: p.agentID, AgentName: p.agentName, PersonaID: p.personaID, Ratio: p.ratio, MaxUsd: p.maxUsd,
		Status: p.status, StopReason: p.stopReason, Positions: []PlanPosition{}, Trades: trades, Log: p.log, CreatedAt: p.createdAt, StoppedAt: p.stoppedAt}
	for _, sym := range slices.Sorted(maps.Keys(p.positions)) {
		v.Positions = append(v.Positions, PlanPosition{Symbol: sym, Qty: p.positions[sym]})
	}
	if marks != nil {
		v.DeployedUsd = ptr(round2(p.deployed(marks)))
	}
	if byRef != nil {
		r := byRef[p.id]
		v.Invested, v.RealizedPnl, v.UnrealizedPnl = ptr(round2(r.Amount)), ptr(round2(r.RealizedPnl)), ptr(round2(r.UnrealizedPnl))
		v.Pnl = ptr(round2(r.RealizedPnl + r.UnrealizedPnl))
	}
	return v
}

func ptr[T any](v T) *T { return &v }

// Handlers.

func (h *handler) handleCopy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Ratio  float64 `json:"ratio"`
		MaxUsd float64 `json:"maxUsd"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		app.WriteError(w, &app.BadRequest{Msg: err.Error()})
		return
	}
	switch {
	case !(req.Ratio >= minRatio && req.Ratio <= maxRatio):
		app.WriteError(w, &app.BadRequest{Msg: "ratio must be 0.1..1."})
		return
	case !(req.MaxUsd >= minMaxUsd && req.MaxUsd <= maxMax):
		app.WriteError(w, &app.BadRequest{Msg: "maxUsd must be 100..50,000."})
		return
	}
	ctx, uid, id := r.Context(), httpx.UserID(r.Context()), r.PathValue("id")
	if !validID(id) {
		app.WriteError(w, app.ErrNotFound)
		return
	}
	if !h.d.LiveArena {
		app.WriteError(w, &app.Conflict{Msg: "The live desk is paused on this server."})
		return
	}
	var pid string
	err := pgx.BeginFunc(ctx, h.d.DB, func(tx pgx.Tx) error {
		disabled, err := lockCopyUser(ctx, tx, uid)
		if err != nil {
			return err
		}
		if disabled {
			return app.ErrNotFound
		}
		if _, err := h.d.Paper.LockTx(ctx, tx, uid, nil); err != nil {
			return err
		}
		var name string
		var cursor int
		var bar *int64
		if err := tx.QueryRow(ctx, "select name,steps-1,last_bar_t from live_agents where id=$1", id).Scan(&name, &cursor, &bar); err != nil {
			return err
		}
		var overlap bool
		if err := tx.QueryRow(ctx, `select exists(select 1 from autopilots where user_id=$1 and status='running' and spec->>'symbol'=any($2::text[]))`, uid, coreSymbols()).Scan(&overlap); err != nil {
			return err
		}
		if overlap {
			return &app.Conflict{Msg: "Stop your core-asset autopilot before starting a copy plan."}
		}
		now := time.Now().UTC()
		entries := []PlanEntry{{T: now, Kind: "start", Text: fmt.Sprintf("Copying future moves from %s at %.0f%%, at most %s deployed. Paper only; no initial catch-up.", name, req.Ratio*100, paper.Money(req.MaxUsd))}}
		err = tx.QueryRow(ctx, `insert into copy_plans(user_id,agent_id,ratio,max_usd,log,last_step,start_bar_t) values($1,$2,$3,$4,$5,$6,$7) returning id`, uid, id, req.Ratio, req.MaxUsd, entries, cursor, bar).Scan(&pid)
		if pg := (*pgconn.PgError)(nil); errors.As(err, &pg) && pg.Code == "23505" {
			return &app.Conflict{Msg: fmt.Sprintf("You already copy %s. Stop that plan first.", name)}
		}
		return err
	})
	if err != nil {
		app.WriteError(w, err)
		return
	}
	plan, err := h.plan(ctx, uid, pid)
	if err != nil {
		app.WriteError(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"plan": plan})
}

// plan is one of the caller's plans as the API shows it; a foreign or unknown
// id is pgx.ErrNoRows (404).
func (h *handler) plan(ctx context.Context, uid, id string) (CopyPlan, error) {
	tx, err := h.d.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return CopyPlan{}, err
	}
	defer app.Rollback(ctx, tx)
	p, err := scanPlan(tx.QueryRow(ctx, "select "+planColumns+" where p.id=$1 and p.user_id=$2", id, uid))
	if err != nil {
		return CopyPlan{}, err
	}
	var count int
	if err := tx.QueryRow(ctx, "select count(*) from trades where user_id=$1 and ref_id=$2 and source='copy'", uid, id).Scan(&count); err != nil {
		return CopyPlan{}, err
	}
	marks, _, merr := liveMarks(ctx, h.d.Market)
	var byRef map[string]paper.RefPnl
	if merr == nil {
		byRef, err = h.d.Paper.ByRefTx(ctx, tx, uid, marks)
		if err != nil {
			return CopyPlan{}, err
		}
	}
	return p.view(marks, byRef, count), tx.Commit(ctx)
}

func (h *handler) handlePlans(w http.ResponseWriter, r *http.Request) {
	plans, err := plansOf(r.Context(), h.d, httpx.UserID(r.Context()))
	if err != nil {
		app.WriteError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"plans": plans})
}

func (h *handler) handlePlanStop(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ClosePositions bool `json:"closePositions"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		app.WriteError(w, &app.BadRequest{Msg: err.Error()})
		return
	}
	ctx, uid, id := r.Context(), httpx.UserID(r.Context()), r.PathValue("id")
	if !validID(id) {
		app.WriteError(w, app.ErrNotFound)
		return
	}
	now := time.Now().UTC()
	trades := []paper.Trade{}
	var pf *paper.Portfolio
	err := pgx.BeginFunc(ctx, h.d.DB, func(tx pgx.Tx) error {
		if _, err := lockCopyUser(ctx, tx, uid); err != nil {
			return err
		}
		quotes, merr := h.d.Market.Assets(ctx)
		ledger, err := h.d.Paper.LockTx(ctx, tx, uid, quotes)
		if err != nil {
			return err
		}
		plans, err := copyBooks(ctx, tx, uid)
		if err != nil {
			return err
		}
		idx := slices.IndexFunc(plans, func(p planRow) bool { return p.id == id })
		if idx < 0 {
			var status string
			if err := tx.QueryRow(ctx, "select status from copy_plans where id=$1 and user_id=$2", id, uid).Scan(&status); err != nil {
				return err
			}
			return &app.Conflict{Msg: "This copy plan is not active."}
		}
		reconcileCopies(plans, ledger.Portfolio(), now)
		p := &plans[idx]
		if req.ClosePositions && merr == nil {
			for _, sym := range slices.Sorted(maps.Keys(p.positions)) {
				cur := ledger.Portfolio()
				q := min(p.positions[sym], max(accountQty(cur, sym)-otherCopies(plans, p.id, sym), 0))
				if q*markOf(cur, sym) < minOrder {
					p.add(now, "skip", sym+": below the minimum close size; kept.")
					continue
				}
				req := paper.OrderRequest{Symbol: sym, Side: "sell", Qty: &q, Source: "copy", Note: p.note(), RefID: &p.id}
				t, err := ledger.Order(ctx, req)
				if err != nil {
					if !copyRefusal(err) {
						return err
					}
					p.add(now, "skip", sym+" could not be closed: "+closeReason(err))
					continue
				}
				trades = append(trades, t)
				p.hold(sym, -t.Qty)
				p.add(now, "order", fmt.Sprintf("Closed %s %s at %s.", fmtQty(t.Qty), sym, paper.Money(t.Price)))
			}
		} else if req.ClosePositions && merr != nil {
			p.add(now, "skip", "Market quotes unavailable; positions kept.")
		}
		p.stop("user", now)
		p.add(now, "stop", "Stopped by you; remaining positions kept.")
		for i := range plans {
			if err := plans[i].save(ctx, tx); err != nil {
				return err
			}
		}
		if merr == nil {
			cur := ledger.Portfolio()
			pf = &cur
		}
		return nil
	})
	if err != nil {
		app.WriteError(w, err)
		return
	}
	plan, err := h.plan(ctx, uid, id)
	if err != nil {
		app.WriteError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"plan": plan, "trades": trades, "portfolio": pf})
}

// closeReason is the user-facing text of a refused close.
func closeReason(err error) string {
	var invalid *paper.ValidationError
	var halted *paper.HaltedError
	switch {
	case errors.As(err, &invalid):
		return invalid.Msg + "."
	case errors.As(err, &halted):
		return halted.Error()
	case errors.Is(err, paper.ErrInsufficient):
		return "not enough buying power."
	}
	return "market data is unavailable right now."
}

func markOf(pf paper.Portfolio, sym string) float64 {
	for _, pos := range pf.Positions {
		if pos.Symbol == sym {
			return pos.MarkPrice
		}
	}
	return 0
}

// fmtQty is "12.4", "0.004215": up to 4 decimals from 1 up, 6 below.
func fmtQty(q float64) string {
	d := 6
	if math.Abs(q) >= 1 {
		d = 4
	}
	s := fmt.Sprintf("%.*f", d, q)
	for len(s) > 0 && s[len(s)-1] == '0' {
		s = s[:len(s)-1]
	}
	if len(s) > 0 && s[len(s)-1] == '.' {
		s = s[:len(s)-1]
	}
	return s
}

func joinAnd(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	return fmt.Sprintf("%s and %s", joinComma(parts[:len(parts)-1]), parts[len(parts)-1])
}

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}

// User → account → books is shared by copy creation, delivery and stop.
// FOR NO KEY UPDATE also permits paper's FK checks while blocking disable.
func lockCopyUser(ctx context.Context, tx pgx.Tx, uid string) (bool, error) {
	var disabled bool
	err := tx.QueryRow(ctx, "select disabled_at is not null from users where id=$1 for no key update", uid).Scan(&disabled)
	return disabled, err
}
func coreSymbols() []string {
	out := make([]string, 0, len(market.Core))
	for _, a := range market.Core {
		out = append(out, a.Symbol)
	}
	return out
}
func copyBooks(ctx context.Context, tx pgx.Tx, uid string) ([]planRow, error) {
	rows, err := tx.Query(ctx, "select "+planColumns+" where p.user_id=$1 and p.status='active' order by p.created_at,p.id for update of p", uid)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (planRow, error) { return scanPlan(row) })
}
func reconcileCopies(plans []planRow, pf paper.Portfolio, now time.Time) {
	sums := map[string]float64{}
	for _, p := range plans {
		for sym, q := range p.positions {
			sums[sym] += q
		}
	}
	for sym, total := range sums {
		available := max(accountQty(pf, sym), 0)
		if total <= available+planDust {
			continue
		}
		for i := range plans {
			p := &plans[i]
			q := p.positions[sym]
			if q <= 0 {
				continue
			}
			reduced := q * available / total
			if reduced <= planDust {
				delete(p.positions, sym)
			} else {
				p.positions[sym] = reduced
			}
			p.add(now, "sync", fmt.Sprintf("%s reduced outside copy plans; proportional ownership now %s.", sym, fmtQty(reduced)))
		}
	}
}
func otherCopies(plans []planRow, id, sym string) float64 {
	total := 0.0
	for _, p := range plans {
		if p.id != id {
			total += p.positions[sym]
		}
	}
	return total
}
func copyRefusal(err error) bool {
	var invalid *paper.ValidationError
	var halted *paper.HaltedError
	return errors.Is(err, paper.ErrInsufficient) || errors.Is(err, market.ErrUpstream) || errors.As(err, &invalid) || errors.As(err, &halted)
}

// StopCopiesForDisabled runs inside admin's user-lock transaction, without
// market data or a live ticker. Positions stay in the aggregate account.
func StopCopiesForDisabled(ctx context.Context, tx pgx.Tx, uid string) (int, error) {
	plans, err := copyBooks(ctx, tx, uid)
	if err != nil {
		return 0, err
	}
	for i := range plans {
		p := &plans[i]
		p.stop("account_disabled", time.Now())
		p.add(time.Now(), "stop", "Account disabled by an admin; positions kept.")
		if err := p.save(ctx, tx); err != nil {
			return 0, err
		}
	}
	return len(plans), nil
}

// recoverCopies visits pending committed steps in bounded batches. Each step
// commits all legs with their delivery IDs; failure leaves the cursor unchanged.
func recoverCopies(ctx context.Context, d *app.Deps, now time.Time) error {
	rows, err := d.DB.Query(ctx, `select p.id::text,p.user_id::text,s.step,s.bar_t,s.fills
  from copy_plans p join live_steps s on s.agent_id=p.agent_id
  where p.status='active' and s.step>coalesce(p.last_step,-1)
  order by s.step,p.created_at,p.id limit 150`)
	if err != nil {
		return err
	}
	type delivery struct {
		id, uid string
		step    int
		bar     int64
		fills   []Fill
	}
	items, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (delivery, error) {
		var x delivery
		return x, r.Scan(&x.id, &x.uid, &x.step, &x.bar, &x.fills)
	})
	if err != nil {
		return err
	}
	// Maintenance is independent of zero-fill steps and of the enabled switch.
	disabledRows, err := d.DB.Query(ctx, `select distinct p.user_id::text from copy_plans p join users u on u.id=p.user_id where p.status='active' and u.disabled_at is not null`)
	if err != nil {
		return err
	}
	disabledUsers, err := pgx.CollectRows(disabledRows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, uid := range disabledUsers {
		if err := pgx.BeginFunc(ctx, d.DB, func(tx pgx.Tx) error {
			disabled, err := lockCopyUser(ctx, tx, uid)
			if err != nil || !disabled {
				return err
			}
			_, err = StopCopiesForDisabled(ctx, tx, uid)
			return err
		}); err != nil {
			return err
		}
	}
	if !d.LiveArena {
		return nil
	}
	for _, x := range items {
		if err := pgx.BeginFunc(ctx, d.DB, func(tx pgx.Tx) error {
			disabled, err := lockCopyUser(ctx, tx, x.uid)
			if err != nil {
				return err
			}
			if disabled {
				_, err := StopCopiesForDisabled(ctx, tx, x.uid)
				return err
			}
			quotes, err := d.Market.Assets(ctx)
			if err != nil {
				return err
			}
			ledger, err := d.Paper.LockTx(ctx, tx, x.uid, quotes)
			if err != nil {
				return err
			}
			plans, err := copyBooks(ctx, tx, x.uid)
			if err != nil {
				return err
			}
			idx := slices.IndexFunc(plans, func(p planRow) bool { return p.id == x.id })
			if idx < 0 {
				return nil
			}
			p := &plans[idx]
			if p.lastStep != nil && *p.lastStep >= x.step {
				return nil
			}
			reconcileCopies(plans, ledger.Portfolio(), now)
			if p.startBar == nil || x.bar > *p.startBar {
				type leg struct {
					index int
					fill  Fill
				}
				legs := make([]leg, len(x.fills))
				for i, f := range x.fills {
					legs[i] = leg{i, f}
				}
				slices.SortStableFunc(legs, func(a, b leg) int {
					if a.fill.Side == b.fill.Side {
						return 0
					}
					if a.fill.Side == "sell" {
						return -1
					}
					return 1
				})
				marks := map[string]float64{}
				for _, a := range quotes {
					marks[a.Symbol] = a.Price
				}
				for _, l := range legs {
					f := l.fill
					cur := ledger.Portfolio()
					req := paper.OrderRequest{Symbol: f.Symbol, Side: f.Side, Source: "copy", Note: p.note(), RefID: &p.id}
					var skipped string
					if marks[f.Symbol] <= 0 {
						skipped = f.Symbol + ": no current quote; skipped."
					} else if f.Side == "buy" {
						n := min(f.Notional*p.ratio, p.maxUsd-p.deployed(marks))
						req.Notional = &n
						if accountQty(cur, f.Symbol) < -planDust {
							skipped = f.Symbol + ": external short; copy buy skipped."
						} else if n < minOrder {
							skipped = f.Symbol + ": cap reached or below $10; buy skipped."
						}
					} else {
						q := min(f.Qty*p.ratio, p.positions[f.Symbol], max(accountQty(cur, f.Symbol)-otherCopies(plans, p.id, f.Symbol), 0))
						req.Qty = &q
						if q*marks[f.Symbol] < minOrder {
							skipped = f.Symbol + ": no available owned long exposure above $10; sell skipped."
						}
					}
					var tradeID *string
					status := "skipped"
					if skipped == "" {
						t, err := ledger.Order(ctx, req)
						if err != nil {
							if !copyRefusal(err) {
								return err
							}
							skipped = f.Symbol + ": " + closeReason(err)
						} else {
							tradeID = &t.ID
							status = "filled"
							signed := t.Qty
							if t.Side == "sell" {
								signed = -signed
							}
							p.hold(f.Symbol, signed)
							p.add(now, "order", fmt.Sprintf("%s %s of %s at %s.", map[string]string{"buy": "Bought", "sell": "Sold"}[t.Side], paper.Money(t.Notional), t.Symbol, paper.Money(t.Price)))
						}
					}
					if skipped != "" {
						p.add(now, "skip", skipped)
					}
					if _, err := tx.Exec(ctx, `insert into copy_deliveries(plan_id,step,leg,trade_id,status) values($1,$2,$3,$4,$5)`, p.id, x.step, l.index, tradeID, status); err != nil {
						return err
					}
				}
			}
			p.lastStep = &x.step
			for i := range plans {
				if err := plans[i].save(ctx, tx); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

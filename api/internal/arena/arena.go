// Package arena is the AI backtest arena ("time travel"):
// persona agents on the one Bedrock model trade a past window step by step,
// each seeing only bars closed by the simulated time, filled at the next
// bar's open by the paper engine's rules and scored against BTC and an
// equal-weight basket. Runs happen in a goroutine; the client polls
// GET /api/arena/runs/{id}.
package arena

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/httpx"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm"
)

const (
	startingCash  = 100_000.0
	maxAgents     = 4
	maxSteps      = 90 // the cadences' day spans cap steps here; the DB check backs it up
	maxActiveRuns = 2  // process-wide: protects the Bedrock quota chat also uses
	maxNewsCalls  = 120
	maxPriceCalls = 6 // per agent-step: every tool result is resent on each later turn of the call
	loadTimeout   = 20 * time.Second
	newsTimeout   = 15 * time.Second // per Brave lookup, detached from the step that asked
)

// minStart is the earliest window start: Binance spot history begins in 2017.
var minStart = time.Date(2018, 1, 1, 0, 0, 0, 0, time.UTC)

type cadence struct {
	id, label                     string
	iv                            int64 // seconds per bar
	minDays, maxDays, defaultDays int
}

var cadences = []cadence{{"1d", "Daily", 86400, 30, 90, 60}, {"4h", "4-hour", 14400, 7, 14, 14}}

func cadenceOf(id string) cadence {
	return cadences[max(slices.IndexFunc(cadences, func(c cadence) bool { return c.id == id }), 0)]
}

const contamination = "This window overlaps the model's training data (cutoff %s). " +
	"The model may remember what happened; treat results as optimistic."

type Target struct {
	Symbol    string  `json:"symbol"`
	WeightPct float64 `json:"weightPct"`
}

type Fill struct {
	Symbol      string  `json:"symbol"`
	Side        string  `json:"side"`
	Qty         float64 `json:"qty"`
	Price       float64 `json:"price"`
	Notional    float64 `json:"notional"`
	Fee         float64 `json:"fee"`
	RealizedPnl float64 `json:"realizedPnl"` // net of the fee, as in paper
}

type Position struct {
	Symbol    string  `json:"symbol"`
	Qty       float64 `json:"qty"`
	AvgPrice  float64 `json:"avgPrice"`
	Mark      float64 `json:"mark"`
	WeightPct float64 `json:"weightPct"`
}

type ToolLog struct {
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input"`
	OK      bool            `json:"ok"`
	Summary string          `json:"summary"`
}

// Step is one agent's full state after one step (contract ArenaStep).
type Step struct {
	Step      int        `json:"step"`
	T         int64      `json:"t"` // decision time
	Equity    float64    `json:"equity"`
	Cash      float64    `json:"cash"`
	Positions []Position `json:"positions"`
	Targets   []Target   `json:"targets"` // null = held
	Fills     []Fill     `json:"fills"`
	Tools     []ToolLog  `json:"tools"`
	Reasoning string     `json:"reasoning"` // plain text, may echo untrusted headlines
	Error     *string    `json:"error"`
}

type Metrics struct {
	TotalReturnPct      float64  `json:"totalReturnPct"`
	AnnualizedReturnPct *float64 `json:"annualizedReturnPct"` // null under market.MinAnnualizeDays (90)
	Sharpe              float64  `json:"sharpe"`
	Sortino             float64  `json:"sortino"`
	Calmar              *float64 `json:"calmar"`
	MaxDrawdownPct      float64  `json:"maxDrawdownPct"`
	VolatilityAnnPct    float64  `json:"volatilityAnnPct"`
	WinRatePct          *float64 `json:"winRatePct"` // sells with realized P&L > 0, net of fee; null without sells
	Fills               int      `json:"fills"`
	TurnoverPct         float64  `json:"turnoverPct"` // Σ notional / mean equity
	ExposurePct         float64  `json:"exposurePct"` // mean invested share of equity
	FeesUsd             float64  `json:"feesUsd"`
	VsBtcPct            float64  `json:"vsBtcPct"`
	VsBasketPct         float64  `json:"vsBasketPct"`
}

type Point struct {
	T int64   `json:"t"`
	V float64 `json:"v"`
}

type Agent struct {
	Index         int     `json:"index"`
	Name          string  `json:"name"`
	PersonaID     string  `json:"personaId"`
	Instruction   string  `json:"instruction"`
	Status        string  `json:"status"` // active | benched
	Rank          int     `json:"rank"`
	Errors        int     `json:"errors"`
	Equity        []Point `json:"equity"`
	Metrics       Metrics `json:"metrics"`
	LastReasoning string  `json:"lastReasoning"`
}

type Benchmark struct {
	ID      string  `json:"id"` // btc | basket
	Name    string  `json:"name"`
	Equity  []Point `json:"equity"`
	Metrics Metrics `json:"metrics"`
}

type Run struct {
	ID              string      `json:"id"`
	Name            string      `json:"name"`
	Status          string      `json:"status"` // running | completed | failed | cancelled
	Error           *string     `json:"error"`
	Cadence         string      `json:"cadence"`
	Start           string      `json:"start"`
	End             string      `json:"end"`
	Steps           int         `json:"steps"`
	StepsDone       int         `json:"stepsDone"`
	Universe        []string    `json:"universe"`
	StartingCash    float64     `json:"startingCash"`
	Model           string      `json:"model"`
	KnowledgeCutoff string      `json:"knowledgeCutoff"`
	Contaminated    bool        `json:"contaminated"`
	Credits         int         `json:"credits"` // charged minus refunds
	Agents          []Agent     `json:"agents"`
	Benchmarks      []Benchmark `json:"benchmarks"`
	CreatedAt       time.Time   `json:"createdAt"`
	CompletedAt     *time.Time  `json:"completedAt"`
}

type Leader struct {
	Name           string  `json:"name"`
	TotalReturnPct float64 `json:"totalReturnPct"`
}

type Summary struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Status       string    `json:"status"`
	Cadence      string    `json:"cadence"`
	Start        string    `json:"start"`
	End          string    `json:"end"`
	Steps        int       `json:"steps"`
	StepsDone    int       `json:"stepsDone"`
	Contaminated bool      `json:"contaminated"`
	Leader       *Leader   `json:"leader"` // null until completed
	BtcReturnPct float64   `json:"btcReturnPct"`
	CreatedAt    time.Time `json:"createdAt"`
}

// agentRow is an agent as stored in arena_runs.agents; metrics are written
// when the run completes.
type agentRow struct {
	Name        string   `json:"name"`
	PersonaID   string   `json:"personaId"`
	Instruction string   `json:"instruction"`
	Metrics     *Metrics `json:"metrics"`
}

// runRow is an arena_runs row.
type runRow struct {
	id, name, status, cadence string
	err                       *string
	startAt, cutoff           time.Time
	steps, stepsDone, credits int
	universe                  []string
	model                     string
	agents                    []agentRow
	benchmarks                map[string][]float64
	createdAt                 time.Time
	completedAt               *time.Time
}

type handler struct {
	d    *app.Deps
	runs sync.WaitGroup // background runs; tests wait for them

	mu sync.Mutex
	// live holds a slot per user with a run in this process, from before its
	// load until its runner returns: the caps count runners, not running rows,
	// which a cancel flips while the step in flight still calls the model.
	// The value stops the runner; nil while loading.
	live map[string]context.CancelCauseFunc
}

func Register(mux *http.ServeMux, d *app.Deps) { register(mux, d) }

func register(mux *http.ServeMux, d *app.Deps) *handler {
	h := &handler{d: d, live: map[string]context.CancelCauseFunc{}}
	auth := func(f http.HandlerFunc) http.Handler { return d.Auth.Require(f) }
	mux.Handle("GET /api/arena/options", auth(h.handleOptions))
	mux.Handle("POST /api/arena/runs", auth(h.handleCreate))
	mux.Handle("GET /api/arena/runs", auth(h.handleList))
	mux.Handle("GET /api/arena/runs/{id}", auth(h.handleGet))
	mux.Handle("GET /api/arena/runs/{id}/steps", auth(h.handleSteps))
	mux.Handle("POST /api/arena/runs/{id}/cancel", auth(h.handleCancel))
	mux.Handle("DELETE /api/arena/runs/{id}", auth(h.handleDelete))
	h.registerLive(mux) // the live desk and copy plans (live.go, copy.go)
	return h
}

func (h *handler) handleOptions(w http.ResponseWriter, r *http.Request) {
	type option struct {
		Cadence      string `json:"cadence"`
		MinDays      int    `json:"minDays"`
		MaxDays      int    `json:"maxDays"`
		DefaultStart string `json:"defaultStart"`
		DefaultEnd   string `json:"defaultEnd"`
	}
	type personaOut struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	end := todayUTC().AddDate(0, 0, -1)
	opts := []option{}
	for _, c := range cadences {
		opts = append(opts, option{c.id, c.minDays, c.maxDays, defaultStart(c, end, h.d.LLM.KnowledgeCutoff()).Format(time.DateOnly), end.Format(time.DateOnly)})
	}
	ps := []personaOut{}
	for _, p := range personas {
		desc := p.Mandate
		if p.ID == "custom" {
			desc = "Your own mandate: describe how this agent should trade (10–1,000 characters)."
		}
		ps = append(ps, personaOut{p.ID, p.Name, desc})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"personas": ps, "knowledgeCutoff": h.d.LLM.KnowledgeCutoff().Format(time.DateOnly),
		"latestEnd": end.Format(time.DateOnly), "cadences": opts, "maxAgents": maxAgents, "maxSteps": maxSteps, "creditsPerAgentStep": 0.5})
}

// defaultStart opens the default window after the knowledge cutoff when the
// span allows the cadence's minimum; otherwise the window overlaps it.
func defaultStart(c cadence, end, cutoff time.Time) time.Time {
	s := end.AddDate(0, 0, 1-c.defaultDays)
	if after := cutoff.AddDate(0, 0, 1); after.After(s) {
		s = after
	}
	if latest := end.AddDate(0, 0, 1-c.minDays); s.After(latest) {
		s = latest
	}
	return s
}

type createRequest struct {
	Cadence string `json:"cadence"`
	Start   string `json:"start"`
	End     string `json:"end"`
	Agents  []struct {
		PersonaID   string `json:"personaId"`
		Name        string `json:"name"`
		Instruction string `json:"instruction"`
	} `json:"agents"`
	Name string `json:"name"`
}

type plan struct {
	cad    cadence
	start  time.Time
	steps  int
	name   string
	agents []agentRow
}

func validate(req createRequest, today time.Time) (plan, error) {
	bad := func(format string, a ...any) (plan, error) {
		return plan{}, &app.BadRequest{Msg: fmt.Sprintf(format, a...)}
	}
	i := slices.IndexFunc(cadences, func(c cadence) bool { return c.id == req.Cadence })
	if i < 0 {
		return bad("cadence must be 1d or 4h.")
	}
	c := cadences[i]
	start, err1 := time.Parse(time.DateOnly, req.Start)
	end, err2 := time.Parse(time.DateOnly, req.End)
	latest := today.AddDate(0, 0, -1)
	switch {
	case err1 != nil || err2 != nil:
		return bad("start and end must be dates (YYYY-MM-DD).")
	case end.After(latest):
		return bad("end must be on or before %s (yesterday, UTC): every bar in the window must have closed.", latest.Format(time.DateOnly))
	case start.Before(minStart):
		return bad("start must be on or after %s.", minStart.Format(time.DateOnly))
	case end.Before(start):
		return bad("start must be on or before end.")
	}
	days := int(end.Sub(start)/(24*time.Hour)) + 1
	if days < c.minDays || days > c.maxDays {
		return bad("A %s run spans %d–%d days, start and end included.", strings.ToLower(c.label), c.minDays, c.maxDays)
	}
	p := plan{cad: c, start: start, steps: int(int64(days) * 86400 / c.iv)}
	if n := len(req.Agents); n < 1 || n > maxAgents {
		return bad("Pick 1 to %d agents.", maxAgents)
	}
	for _, a := range req.Agents {
		per, ok := personaByID(a.PersonaID)
		if !ok {
			return bad("personaId must be momentum, contrarian, macro or custom.")
		}
		row := agentRow{Name: per.Name, PersonaID: per.ID}
		if per.ID == "custom" {
			row.Instruction = strings.TrimSpace(a.Instruction)
			if n := utf8.RuneCountInString(row.Instruction); n < 10 || n > 1000 {
				return bad("A custom agent needs an instruction of 10–1,000 characters.")
			}
			name := strings.Join(strings.Fields(a.Name), " ")
			if utf8.RuneCountInString(name) > 40 {
				return bad("An agent name is at most 40 characters.")
			}
			row.Name = cmp.Or(name, per.Name)
		} else if slices.ContainsFunc(p.agents, func(r agentRow) bool { return r.PersonaID == per.ID }) {
			return bad("Each built-in persona can join a run once.")
		}
		p.agents = append(p.agents, row)
	}
	if p.name = strings.Join(strings.Fields(req.Name), " "); utf8.RuneCountInString(p.name) > 80 {
		return bad("name is at most 80 characters.")
	}
	if p.name == "" {
		from := start.Format("Jan 2")
		if start.Year() != end.Year() {
			from = start.Format("Jan 2, 2006")
		}
		p.name = fmt.Sprintf("%s · %s → %s", c.label, from, end.Format("Jan 2, 2006"))
	}
	return p, nil
}

// cost is the run's credits: half a credit per agent-step, rounded up.
func cost(agents, steps int) int { return (agents*steps + 1) / 2 }

var errBusy = &app.Conflict{Msg: "You already have an arena run in progress. Wait for it to finish or cancel it."}

func (h *handler) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := httpx.Decode(w, r, &req); err != nil {
		app.WriteError(w, &app.BadRequest{Msg: err.Error()})
		return
	}
	p, err := validate(req, todayUTC())
	if err != nil {
		app.WriteError(w, err)
		return
	}
	ctx, user := r.Context(), httpx.UserID(r.Context())
	jctx, done, accepted := h.d.Tasks.Begin("arena", ctx)
	if !accepted {
		httpx.Error(w, http.StatusServiceUnavailable, "unavailable", "Server is stopping.")
		return
	}
	launched := false
	defer func() {
		if !launched {
			done()
		}
	}()
	// Take a slot (see live); the one-running-run index backs it up.
	h.mu.Lock()
	_, busy := h.live[user]
	full := len(h.live) >= maxActiveRuns
	if !busy && !full {
		h.live[user] = nil
	}
	h.mu.Unlock()
	if busy {
		app.WriteError(w, errBusy)
		return
	}
	if full {
		httpx.Error(w, http.StatusTooManyRequests, "rate_limited", "Two arena runs are already in progress. Try again in a few minutes.")
		return
	}
	started := false
	defer func() {
		if !started {
			h.release(user)
		}
	}()
	// Credits before Binance: a user without them costs no klines calls.
	price := cost(len(p.agents), p.steps)
	credits, err := h.d.Usage.Spend(ctx, user, price)
	if err != nil {
		app.WriteError(w, err)
		return
	}
	lctx, cancel := context.WithTimeout(ctx, loadTimeout)
	t, err := load(lctx, h.d.Market, p.cad, p.start, p.steps)
	cancel()
	if err != nil {
		h.d.Usage.Refund(ctx, user, price)
		app.WriteError(w, err)
		return
	}
	insertCtx, insertCancel := context.WithTimeout(jctx, 10*time.Second)
	row, err := h.insert(insertCtx, user, p, t) // process-owned: a disconnect must not orphan it
	insertCancel()
	if err != nil {
		h.d.Usage.Refund(ctx, user, price)
		if pg := (*pgconn.PgError)(nil); errors.As(err, &pg) && pg.Code == "23505" { // the one-running-run index
			err = errBusy
		}
		app.WriteError(w, err)
		return
	}
	agents := make([]*agent, len(p.agents))
	for i, a := range p.agents {
		agents[i] = newAgent(a, t.universe)
	}
	rctx, stop := context.WithCancelCause(jctx)
	h.mu.Lock()
	h.live[user] = stop
	h.mu.Unlock()
	started = true
	h.runs.Add(1)
	launched = true
	go func() {
		defer done()
		defer stop(nil)
		h.run(rctx, row.id, user,
			&runner{id: row.id, llm: h.d.LLM, news: newNewsDesk(h.d.Research), t: t, agents: agents, save: h.saver(row.id)})
	}()
	run := buildRun(row, nil)
	warnings := []string{}
	if run.Contaminated {
		warnings = append(warnings, fmt.Sprintf(contamination, run.KnowledgeCutoff))
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"run": run, "warnings": warnings, "credits": credits})
}

func (h *handler) handleList(w http.ResponseWriter, r *http.Request) {
	list, err := h.list(r.Context(), httpx.UserID(r.Context()))
	if err != nil {
		app.WriteError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"runs": list})
}

func (h *handler) handleGet(w http.ResponseWriter, r *http.Request) {
	run, err := h.get(r.Context(), httpx.UserID(r.Context()), r.PathValue("id"))
	if err != nil {
		app.WriteError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"run": run})
}

func (h *handler) handleSteps(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	agent, err := strconv.Atoi(q.Get("agent"))
	if err != nil {
		app.WriteError(w, &app.BadRequest{Msg: "agent is required: the agent's index."})
		return
	}
	after, limit := -1, 100
	if v := q.Get("after"); v != "" {
		if after, err = strconv.Atoi(v); err != nil || after < -1 {
			app.WriteError(w, &app.BadRequest{Msg: "after must be a step number (-1 for the first page)."})
			return
		}
	}
	if v := q.Get("limit"); v != "" {
		if limit, err = strconv.Atoi(v); err != nil || limit < 1 || limit > 100 {
			app.WriteError(w, &app.BadRequest{Msg: "limit must be 1..100."})
			return
		}
	}
	ctx, id := r.Context(), r.PathValue("id")
	if !validID(id) {
		app.WriteError(w, app.ErrNotFound)
		return
	}
	var agents int
	var startAt time.Time
	var cad string
	if err := h.d.DB.QueryRow(ctx, `select jsonb_array_length(agents), start_at, cadence from arena_runs where id = $1 and user_id = $2`,
		id, httpx.UserID(ctx)).Scan(&agents, &startAt, &cad); err != nil {
		app.WriteError(w, err)
		return
	}
	if agent < 0 || agent >= agents {
		app.WriteError(w, &app.BadRequest{Msg: fmt.Sprintf("agent must be 0..%d.", agents-1)})
		return
	}
	rows, err := h.d.DB.Query(ctx, `
		select step, equity, cash, positions, targets, fills, tools, reasoning, error
		from arena_steps where run_id = $1 and agent = $2 and step > $3 order by step limit $4`, id, agent, after, limit)
	if err != nil {
		app.WriteError(w, err)
		return
	}
	steps, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Step, error) {
		var s Step
		err := row.Scan(&s.Step, &s.Equity, &s.Cash, &s.Positions, &s.Targets, &s.Fills, &s.Tools, &s.Reasoning, &s.Error)
		s.T = startAt.Unix() + int64(s.Step)*cadenceOf(cad).iv
		return s, err
	})
	if err != nil {
		app.WriteError(w, err)
		return
	}
	if steps == nil {
		steps = []Step{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"steps": steps})
}

func (h *handler) handleCancel(w http.ResponseWriter, r *http.Request) {
	ctx, user, id := r.Context(), httpx.UserID(r.Context()), r.PathValue("id")
	if !validID(id) {
		app.WriteError(w, app.ErrNotFound)
		return
	}
	n, err := settle(ctx, h.d, func() (pgx.Rows, error) {
		return h.d.DB.Query(ctx, `update arena_runs set status = 'cancelled', `+cancelSet+`
			where id = $1 and user_id = $2 and status = 'running' `+cancelReturning, id, user)
	})
	if err != nil {
		app.WriteError(w, err)
		return
	}
	if n > 0 { // abort the step in flight: its model calls end and the slot frees now
		h.mu.Lock()
		if stop := h.live[user]; stop != nil {
			stop(errStopped)
		}
		h.mu.Unlock()
	}
	run, err := h.get(ctx, user, id)
	if err == nil && n == 0 {
		err = &app.Conflict{Msg: "This run is not running."}
	}
	if err != nil {
		app.WriteError(w, err)
		return
	}
	credits, err := h.d.Usage.Get(ctx, user)
	if err != nil {
		app.WriteError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"run": run, "credits": credits})
}

func (h *handler) handleDelete(w http.ResponseWriter, r *http.Request) {
	ctx, user, id := r.Context(), httpx.UserID(r.Context()), r.PathValue("id")
	if !validID(id) {
		app.WriteError(w, app.ErrNotFound)
		return
	}
	tag, err := h.d.DB.Exec(ctx, `delete from arena_runs where id = $1 and user_id = $2 and status <> 'running'`, id, user)
	if err == nil && tag.RowsAffected() == 0 {
		var status string
		if err = h.d.DB.QueryRow(ctx, `select status from arena_runs where id = $1 and user_id = $2`, id, user).Scan(&status); err == nil {
			err = &app.Conflict{Msg: "Cancel the run before deleting it."}
		}
	}
	if err != nil {
		app.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Storage. Every handler query is scoped to the owner.

func (h *handler) insert(ctx context.Context, user string, p plan, t *tape) (runRow, error) {
	row := runRow{name: p.name, status: "running", cadence: p.cad.id, startAt: p.start, cutoff: h.d.LLM.KnowledgeCutoff(), steps: p.steps,
		credits: cost(len(p.agents), p.steps), universe: t.universe, model: h.d.LLM.Model(), agents: p.agents, benchmarks: benchmarks(t)}
	err := h.d.DB.QueryRow(ctx, `
		insert into arena_runs (user_id, name, status, cadence, start_at, steps, universe, model, knowledge_cutoff, agents, benchmarks, credits)
		values ($1, $2, 'running', $3, $4, $5, $6, $7, $8, $9, $10, $11) returning id, created_at`,
		user, row.name, row.cadence, row.startAt, row.steps, row.universe, row.model, row.cutoff, row.agents, row.benchmarks, row.credits,
	).Scan(&row.id, &row.createdAt)
	return row, err
}

// get returns pgx.ErrNoRows (→ 404) for a missing or foreign run. Agent
// equity and metrics come from the step rows (at most 4 × 90), cut to
// stepsDone like the baselines: a step saved between the two reads waits for
// the next poll.
func (h *handler) get(ctx context.Context, user, id string) (Run, error) {
	if !validID(id) {
		return Run{}, app.ErrNotFound
	}
	var rr runRow
	err := h.d.DB.QueryRow(ctx, `
		select id, name, status, error, cadence, start_at, steps, steps_done, universe, model, knowledge_cutoff, agents, benchmarks,
		       credits, created_at, completed_at
		from arena_runs where id = $1 and user_id = $2`, id, user).Scan(&rr.id, &rr.name, &rr.status, &rr.err, &rr.cadence,
		&rr.startAt, &rr.steps, &rr.stepsDone, &rr.universe, &rr.model, &rr.cutoff, &rr.agents, &rr.benchmarks, &rr.credits,
		&rr.createdAt, &rr.completedAt)
	if err != nil {
		return Run{}, err
	}
	rows, err := h.d.DB.Query(ctx, `
		select agent, equity, cash, fills, reasoning, error from arena_steps where run_id = $1 and step < $2 order by agent, step`,
		id, rr.stepsDone)
	if err != nil {
		return Run{}, err
	}
	steps := make([][]Step, len(rr.agents))
	var s Step
	var i int
	_, err = pgx.ForEachRow(rows, []any{&i, &s.Equity, &s.Cash, &s.Fills, &s.Reasoning, &s.Error}, func() error {
		if i >= 0 && i < len(steps) {
			steps[i] = append(steps[i], s)
		}
		s = Step{}
		return nil
	})
	if err != nil {
		return Run{}, err
	}
	return buildRun(rr, steps), nil
}

// ponytail: newest 50, no pagination; add a cursor when someone keeps more.
func (h *handler) list(ctx context.Context, user string) ([]Summary, error) {
	rows, err := h.d.DB.Query(ctx, `
		select id, name, status, cadence, start_at, steps, steps_done, knowledge_cutoff, agents,
		       ((benchmarks->'btc'->>steps_done)::float8 / (benchmarks->'btc'->>0)::float8 - 1) * 100, created_at
		from arena_runs where user_id = $1 order by created_at desc limit 50`, user)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Summary, error) {
		var s Summary
		var startAt, cutoff time.Time
		var agents []agentRow
		err := row.Scan(&s.ID, &s.Name, &s.Status, &s.Cadence, &startAt, &s.Steps, &s.StepsDone, &cutoff, &agents, &s.BtcReturnPct, &s.CreatedAt)
		s.Start, s.End = startAt.UTC().Format(time.DateOnly), endDate(startAt, s.Steps, cadenceOf(s.Cadence).iv)
		s.Contaminated, s.BtcReturnPct = !startAt.After(cutoff), round2(s.BtcReturnPct)
		if s.Status == "completed" {
			s.Leader = leader(agents)
		}
		return s, err
	})
	if out == nil {
		out = []Summary{}
	}
	return out, err
}

// saver stores a step's rows with steps_done in one transaction. A run that
// left 'running' (cancelled, recovered) updates nothing: the transaction
// rolls back, nothing of the step is kept, and the runner stops.
func (h *handler) saver(id string) func(context.Context, int, []Step) (bool, error) {
	return func(ctx context.Context, k int, rows []Step) (bool, error) {
		tx, err := h.d.DB.Begin(ctx)
		if err != nil {
			return false, err
		}
		defer app.Rollback(ctx, tx)
		tag, err := tx.Exec(ctx, `update arena_runs set steps_done = $2 where id = $1 and status = 'running'`, id, k+1)
		if err != nil || tag.RowsAffected() == 0 {
			return false, err
		}
		for i, s := range rows {
			var targets any // nil → SQL null: held
			if s.Targets != nil {
				targets = s.Targets
			}
			if _, err := tx.Exec(ctx, `
				insert into arena_steps (run_id, agent, step, equity, cash, positions, targets, fills, tools, reasoning, error)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
				id, i, k, s.Equity, s.Cash, s.Positions, targets, s.Fills, s.Tools, s.Reasoning, s.Error); err != nil {
				return false, err
			}
		}
		return true, tx.Commit(ctx)
	}
}

// Every flip out of 'running' goes through settle: credits drop to what the
// finished steps cost, and the update returns what to refund. Its status =
// 'running' guard lets exactly one actor flip a run, so a refund happens once.
const (
	settleSet       = `completed_at = now(), credits = (steps_done * jsonb_array_length(agents) + 1) / 2`
	settleReturning = `returning user_id, steps, steps_done, jsonb_array_length(agents), (created_at at time zone 'utc')::date`
	// A user cancel also bills the step in flight: its model calls already
	// ran, and refunding them would make a create→cancel loop free.
	cancelSet       = `completed_at = now(), credits = (least(steps_done + 1, steps) * jsonb_array_length(agents) + 1) / 2`
	cancelReturning = `returning user_id, steps, least(steps_done + 1, steps), jsonb_array_length(agents), (created_at at time zone 'utc')::date`
)

// settle runs the flipping update and refunds the unrun steps of every run
// it returned, to the day the run was charged (a run can outlive it); n is
// how many runs it flipped.
// ponytail: created_at trails the charge by the window load (≤ 20 s), so a
// create straddling 00:00 UTC refunds into the new day.
func settle(ctx context.Context, d *app.Deps, update func() (pgx.Rows, error)) (n int, err error) {
	rows, err := update()
	if err != nil {
		return 0, err
	}
	var user string
	var steps, done, agents int
	var day time.Time
	_, err = pgx.ForEachRow(rows, []any{&user, &steps, &done, &agents, &day}, func() error {
		n++
		if refund := cost(agents, steps) - cost(agents, done); refund > 0 {
			d.Usage.RefundOn(ctx, user, day, refund)
		}
		return nil
	})
	return n, err
}

// run executes the loop detached from the request: it owns its timeout and
// turns a panic into a failed run. A cancel stops it through ctx. The user's
// slot frees once the outcome is stored.
func (h *handler) run(ctx context.Context, id, user string, r *runner) {
	defer h.runs.Done()
	defer h.release(user)
	err := errors.New("run did not finish")
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("panic: %v", v)
		}
		h.finish(ctx, id, user, err)
	}()
	rctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	err = r.loop(rctx)
}

// finish stores the outcome with a fresh deadline: completed with each
// agent's final metrics, or failed with a user-facing reason. Either way the
// unrun steps are refunded. A stopped run was settled by whoever stopped it.
func (h *handler) finish(ctx context.Context, id, user string, err error) {
	if errors.Is(err, errStopped) || errors.Is(context.Cause(ctx), errStopped) {
		slog.Info("arena run stopped", "id", id)
		return
	}
	ctx, cancel := app.Cleanup(ctx)
	defer cancel()
	status, msg, agents := "completed", (*string)(nil), any(nil)
	if err == nil {
		var run Run
		if run, err = h.get(ctx, user, id); err == nil {
			rows := make([]agentRow, len(run.Agents))
			for i, a := range run.Agents {
				rows[i] = agentRow{Name: a.Name, PersonaID: a.PersonaID, Instruction: a.Instruction, Metrics: &a.Metrics}
			}
			agents = rows
		}
	}
	if err != nil {
		slog.Warn("arena run failed", "id", id, "err", err)
		reason := runError(err)
		status, msg = "failed", &reason
	}
	if _, err := settle(ctx, h.d, func() (pgx.Rows, error) {
		return h.d.DB.Query(ctx, `update arena_runs set status = $2, error = $3, agents = coalesce($4, agents), `+settleSet+`
			where id = $1 and status = 'running' `+settleReturning, id, status, msg, agents)
	}); err != nil {
		slog.Error("arena run store failed", "id", id, "err", err)
	}
	slog.Info("arena run finished", "id", id, "status", status)
}

// release frees the user's run slot.
func (h *handler) release(user string) {
	h.mu.Lock()
	delete(h.live, user)
	h.mu.Unlock()
}

// Recover fails runs orphaned by a restart and refunds their unrun steps.
// Runs live in the process that created them, so at boot every running run
// is orphaned; after that a sweep every 5 minutes catches runs still running
// past the 60-minute timeout.
// Boot settlement is synchronous; the caller owns the returned periodic loop.
func Recover(ctx context.Context, d *app.Deps) func() {
	sweep := func(boot bool) {
		n, err := settle(ctx, d, func() (pgx.Rows, error) {
			return d.DB.Query(ctx, `update arena_runs set status = 'failed', error = 'Interrupted by a server restart.', `+settleSet+`
				where status = 'running' and ($1 or created_at < now() - interval '75 minutes') `+settleReturning, boot)
		})
		if err != nil {
			if ctx.Err() == nil {
				slog.Error("arena recover", "err", err)
			}
			return
		}
		if n > 0 {
			slog.Info("arena runs interrupted by restart marked failed", "count", n)
		}
	}
	sweep(true)
	return func() {
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				sweep(false)
			}
		}
	}
}

// runError is the user-facing text stored on a failed run.
func runError(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "The arena run timed out."
	case errors.Is(err, llm.ErrUpstream), errors.Is(err, errAllBenched):
		return "The AI model is unavailable right now."
	}
	return "The run could not be completed."
}

// buildRun assembles the contract ArenaRun: baselines cut to stepsDone, and
// per agent its equity points, metrics, rank, errors and benched status.
func buildRun(rr runRow, steps [][]Step) Run {
	c := cadenceOf(rr.cadence)
	start := rr.startAt.Unix()
	run := Run{ID: rr.id, Name: rr.name, Status: rr.status, Error: rr.err, Cadence: rr.cadence, Start: rr.startAt.UTC().Format(time.DateOnly),
		End: endDate(rr.startAt, rr.steps, c.iv), Steps: rr.steps, StepsDone: rr.stepsDone, Universe: rr.universe, StartingCash: startingCash,
		Model: rr.model, KnowledgeCutoff: rr.cutoff.Format(time.DateOnly), Contaminated: !rr.startAt.After(rr.cutoff), Credits: rr.credits,
		Agents: []Agent{}, Benchmarks: []Benchmark{}, CreatedAt: rr.createdAt, CompletedAt: rr.completedAt}
	for _, b := range []struct {
		id, name string
		assets   int
	}{{"btc", "BTC buy & hold", 1}, {"basket", "Equal-weight basket", len(rr.universe)}} {
		v := rr.benchmarks[b.id][:rr.stepsDone+1]
		run.Benchmarks = append(run.Benchmarks, Benchmark{ID: b.id, Name: b.name, Equity: points(start, c.iv, v),
			Metrics: metrics(c.id, c.iv, v, make([]float64, len(v)), baselineFills(b.assets))})
	}
	btc, basket := run.Benchmarks[0].Metrics.TotalReturnPct, run.Benchmarks[1].Metrics.TotalReturnPct
	vs := func(m *Metrics) {
		m.VsBtcPct, m.VsBasketPct = round2(m.TotalReturnPct-btc), round2(m.TotalReturnPct-basket)
	}
	vs(&run.Benchmarks[0].Metrics)
	vs(&run.Benchmarks[1].Metrics)
	for i, ar := range rr.agents {
		var ss []Step
		if i < len(steps) {
			ss = steps[i]
		}
		eq, cash, fills := []float64{startingCash}, []float64{startingCash}, []Fill{}
		a := Agent{Index: i, Name: ar.Name, PersonaID: ar.PersonaID, Instruction: ar.Instruction, Status: "active"}
		streak := 0
		for _, s := range ss {
			eq, cash, fills = append(eq, s.Equity), append(cash, s.Cash), append(fills, s.Fills...)
			if s.Error != nil {
				a.Errors++
				if streak++; streak >= benchAfter {
					a.Status = "benched"
				}
			} else {
				streak = 0
			}
			if s.Reasoning != "" {
				a.LastReasoning = clip(s.Reasoning, noteText)
			}
		}
		a.Equity, a.Metrics = points(start, c.iv, eq), metrics(c.id, c.iv, eq, cash, fills)
		vs(&a.Metrics)
		run.Agents = append(run.Agents, a)
	}
	order := make([]int, len(run.Agents))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool { return better(run.Agents[order[i]].Metrics, run.Agents[order[j]].Metrics) })
	for r, i := range order {
		run.Agents[i].Rank = r + 1
	}
	return run
}

// better ranks by total return, then by the smaller drawdown.
func better(a, b Metrics) bool {
	if a.TotalReturnPct != b.TotalReturnPct {
		return a.TotalReturnPct > b.TotalReturnPct
	}
	return a.MaxDrawdownPct < b.MaxDrawdownPct
}

// leader is the best agent by its stored final metrics; nil without them.
func leader(agents []agentRow) *Leader {
	var best *agentRow
	for i := range agents {
		if a := &agents[i]; a.Metrics != nil && (best == nil || better(*a.Metrics, *best.Metrics)) {
			best = a
		}
	}
	if best == nil {
		return nil
	}
	return &Leader{Name: best.Name, TotalReturnPct: best.Metrics.TotalReturnPct}
}

// points pairs values with their mark times: v[0] at the window start, v[k]
// at the close of step k-1.
func points(start, iv int64, v []float64) []Point {
	out := make([]Point, len(v))
	for k, x := range v {
		out[k] = Point{T: start + int64(k)*iv, V: round2(x)}
	}
	return out
}

// endDate is the window's last day (inclusive). Days are UTC: pgx reads
// start_at in the server's zone, which west of UTC is the day before.
func endDate(startAt time.Time, steps int, iv int64) string {
	return startAt.UTC().Add(time.Duration(int64(steps)*iv)*time.Second).AddDate(0, 0, -1).Format(time.DateOnly)
}

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func validID(s string) bool { return uuidRE.MatchString(s) }

// todayUTC is the current UTC date at midnight.
func todayUTC() time.Time { return time.Now().UTC().Truncate(24 * time.Hour) }

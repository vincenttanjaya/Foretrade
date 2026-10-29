// Package strategy serves the strategy studio: /api/strategies*. A strategy
// is a JSON rule spec (never generated code) that Backtest runs on Binance
// candles. Sonnet only writes the spec and, after the fact, explains the
// numbers Go computed.
package strategy

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/httpx"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/usage"
)

type Strategy struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Author      string         `json:"author"`
	Prompt      string         `json:"prompt"`
	Spec        Spec           `json:"spec"`
	Result                     // metrics, equity, benchmark, trades
	Explanation string         `json:"explanation"`
	ShortSample bool           `json:"shortSample"` // see shortSample
	CreatedAt   time.Time      `json:"createdAt"`
	Community   *CommunityInfo `json:"community"` // nil for built-in Labs
	Forward     *Forward       `json:"forward"`   // the saved rules' forward result or explicit availability state
}

type Summary struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Author      string          `json:"author"`
	Symbol      string          `json:"symbol"`
	Interval    string          `json:"interval"`
	Metrics     Metrics         `json:"metrics"`
	Sparkline   []float64       `json:"sparkline"`
	ShortSample bool            `json:"shortSample"`
	CreatedAt   time.Time       `json:"createdAt"`
	Community   *CommunityInfo  `json:"community"`
	Forward     *ForwardSummary `json:"forward"`
}

const sparkPoints = 60

// A backtest over fewer than 180 days or with fewer than 10 trades is a short
// sample: its Sharpe says little, so Top strategies ranks it after the rest.
const (
	minSampleDays   = 180
	minSampleTrades = 10
)

func shortSample(m Metrics) bool {
	return m.EndT-m.StartT < minSampleDays*86400 || m.Trades < minSampleTrades
}

// barsFloor is the history a generated spec gets at least: 90+ days, so its
// annualized return and Calmar are numbers, except on 15m where the 3000-bar
// cap is about 31 days. Built-in Labs keep their own.
var barsFloor = map[string]int{"15m": 3000, "1h": 2500, "4h": 1000, "1d": 1500}

func summarize(s Strategy) Summary {
	idx := sample(len(s.Equity), sparkPoints)
	spark := make([]float64, len(idx))
	for i, j := range idx {
		spark[i] = s.Equity[j].V
	}
	sum := Summary{ID: s.ID, Name: s.Name, Author: s.Author, Symbol: s.Spec.Symbol, Interval: s.Spec.Interval,
		Metrics: s.Metrics, Sparkline: spark, ShortSample: s.ShortSample, CreatedAt: s.CreatedAt, Community: s.Community}
	if s.Forward != nil {
		sum.Forward = &s.Forward.ForwardSummary
	}
	return sum
}

type handler struct {
	d    *app.Deps
	labs labsCache
	fwd  forwardCache
}

// Register mounts /api/strategies* behind the session check.
func Register(mux *http.ServeMux, d *app.Deps) {
	h := &handler{d: d}
	auth := func(f http.HandlerFunc) http.Handler { return d.Auth.Require(f) }
	mux.Handle("POST /api/strategies/generate", auth(h.generate))
	mux.Handle("POST /api/strategies/backtest", auth(h.backtest))
	mux.Handle("POST /api/strategies/optimize", auth(h.optimize))
	mux.Handle("GET /api/strategies", auth(h.list))
	mux.Handle("GET /api/strategies/{id}", auth(h.get))
	mux.Handle("DELETE /api/strategies/{id}", auth(h.delete))
	mux.Handle("POST /api/strategies/{id}/publish", auth(h.publish))
	mux.Handle("POST /api/strategies/{id}/star", auth(h.star))
	mux.Handle("POST /api/strategies/{id}/copy", auth(h.copy))
}

// closed fetches the spec's candles without the still-open last one:
// signals only read closed bars.
func (h *handler) closed(ctx context.Context, s Spec) ([]market.Candle, error) {
	candles, err := h.d.Market.Candles(ctx, s.Symbol, s.Interval, min(s.Bars+1, 3000))
	if err != nil {
		return nil, err
	}
	return candles[:len(candles)-1], nil
}

// run fetches the spec's closed candles and backtests them.
func (h *handler) run(ctx context.Context, s Spec) (Result, error) {
	candles, err := h.closed(ctx, s)
	if err != nil {
		return Result{}, err
	}
	res, err := Backtest(s, candles)
	if err != nil {
		return Result{}, &app.BadRequest{Msg: err.Error()}
	}
	return res, nil
}

// runBefore is run on the spec's bars that opened before end, an in-sample
// window that never overlaps the live forward from end (the built-ins).
func (h *handler) runBefore(end time.Time) func(context.Context, Spec) (Result, error) {
	return func(ctx context.Context, s Spec) (Result, error) {
		candles, err := h.d.Market.CandlesBefore(ctx, s.Symbol, s.Interval, end, s.Bars)
		if err != nil {
			return Result{}, err
		}
		res, err := Backtest(s, candles)
		if err != nil {
			return Result{}, &app.BadRequest{Msg: err.Error()}
		}
		return res, nil
	}
}

// specOf reads and validates the {spec} body of /backtest and /optimize; on
// false it has written the 400.
func specOf(w http.ResponseWriter, r *http.Request) (Spec, bool) {
	var req struct {
		Spec *Spec `json:"spec"`
	}
	err := httpx.Decode(w, r, &req)
	if err == nil && req.Spec == nil {
		err = errors.New("spec is required.")
	}
	if err == nil {
		err = req.Spec.Validate()
	}
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad_request", err.Error())
		return Spec{}, false
	}
	return *req.Spec, true
}

func (h *handler) backtest(w http.ResponseWriter, r *http.Request) {
	s, ok := specOf(w, r)
	if !ok {
		return
	}
	res, err := h.run(r.Context(), s)
	if err != nil {
		app.WriteError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

// OptimizeCell is one stop/target pair's backtest (contract OptimizeCell).
type OptimizeCell struct {
	StopLossPct    *float64 `json:"stopLossPct"` // nil = none
	TakeProfitPct  *float64 `json:"takeProfitPct"`
	Sharpe         float64  `json:"sharpe"`
	TotalReturnPct float64  `json:"totalReturnPct"`
	MaxDrawdownPct float64  `json:"maxDrawdownPct"`
	Trades         int      `json:"trades"`
}

var (
	gridStops   = []float64{1, 2, 3, 5, 8}
	gridTargets = []float64{2, 4, 6, 10, 15}
)

const (
	maxBest       = 3
	minBestTrades = 5
)

// optimize backtests the spec as sent and over the 5×5 stop × target grid,
// all on one candle fetch: free, not saved.
func (h *handler) optimize(w http.ResponseWriter, r *http.Request) {
	s, ok := specOf(w, r)
	if !ok {
		return
	}
	candles, err := h.closed(r.Context(), s)
	if err != nil {
		app.WriteError(w, err)
		return
	}
	cell := func(s Spec) (OptimizeCell, error) {
		res, err := Backtest(s, candles)
		m := res.Metrics
		c := OptimizeCell{Sharpe: m.Sharpe, TotalReturnPct: m.TotalReturnPct, MaxDrawdownPct: m.MaxDrawdownPct, Trades: m.Trades}
		if s.StopLossPct > 0 {
			c.StopLossPct = num(s.StopLossPct)
		}
		if s.TakeProfitPct > 0 {
			c.TakeProfitPct = num(s.TakeProfitPct)
		}
		return c, err
	}
	current, err := cell(s)
	if err != nil { // not enough history; the grid shares the warm-up, so it would fail too
		app.WriteError(w, &app.BadRequest{Msg: err.Error()})
		return
	}
	grid := make([]OptimizeCell, 0, len(gridStops)*len(gridTargets))
	for _, stop := range gridStops {
		for _, target := range gridTargets {
			t := s
			t.StopLossPct, t.TakeProfitPct = stop, target
			c, _ := cell(t)
			grid = append(grid, c)
		}
	}
	// ponytail: best of 25 tries on the same history is in-sample; the UI says so.
	best := slices.DeleteFunc(slices.Clone(grid), func(c OptimizeCell) bool { return c.Trades < minBestTrades })
	slices.SortStableFunc(best, func(a, b OptimizeCell) int { return cmp.Compare(b.Sharpe, a.Sharpe) })
	httpx.JSON(w, http.StatusOK, map[string]any{"grid": grid, "best": best[:min(len(best), maxBest)], "current": current})
}

// generated is what the model returns for a strategy idea.
type generated struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Spec        Spec   `json:"spec"`
}

func (h *handler) generate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Prompt   string `json:"prompt"`
		Symbol   string `json:"symbol"`
		Interval string `json:"interval"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	prompt := strings.TrimSpace(req.Prompt)
	if n := utf8.RuneCountInString(prompt); n < 5 || n > 1000 {
		httpx.Error(w, http.StatusBadRequest, "bad_request", "prompt must be 5..1000 characters.")
		return
	}
	symbol, interval := "BTC", "4h"
	if req.Symbol != "" {
		info, ok := market.Lookup(req.Symbol)
		if !ok {
			httpx.Error(w, http.StatusBadRequest, "bad_request", "Unknown symbol.")
			return
		}
		symbol = info.Symbol
	}
	if req.Interval != "" {
		if !market.ValidInterval(req.Interval) {
			httpx.Error(w, http.StatusBadRequest, "bad_request", "interval must be one of 15m, 1h, 4h, 1d.")
			return
		}
		interval = req.Interval
	}

	ctx, uid := r.Context(), httpx.UserID(r.Context())
	credits, err := h.d.Usage.Spend(ctx, uid, usage.CostStrategy)
	if err != nil {
		app.WriteError(w, err)
		return
	}
	ask := designPrompt(prompt, symbol, interval)
	var g generated
	if err := h.d.LLM.CompleteJSON(ctx, designSystem, ask, llm.Medium, &g); err != nil {
		if ctx.Err() == nil { // nothing usable came back; a client that hung up still pays
			h.d.Usage.Refund(ctx, uid, usage.CostStrategy)
		}
		app.WriteError(w, err)
		return
	}
	res, err := h.check(ctx, &g, symbol, interval)
	var bad *app.BadRequest
	if errors.As(err, &bad) {
		// One repair round: show the model its spec and the validation error.
		prev, _ := json.Marshal(g)
		ask += fmt.Sprintf("\n\nYour previous reply was:\n%s\nIt was rejected: %s\nReply with the corrected JSON object only.", prev, bad.Msg)
		g = generated{}
		if err := h.d.LLM.CompleteJSON(ctx, designSystem, ask, llm.Medium, &g); err != nil {
			app.WriteError(w, err)
			return
		}
		if res, err = h.check(ctx, &g, symbol, interval); errors.As(err, &bad) {
			err = fmt.Errorf("%w: strategy spec still invalid after retry: %s", llm.ErrUpstream, bad.Msg)
		}
	}
	if err != nil {
		app.WriteError(w, err)
		return
	}

	s := Strategy{Name: g.Name, Description: g.Description, Prompt: prompt, Spec: g.Spec, Result: res, ShortSample: shortSample(res.Metrics)}
	s.Explanation = h.explain(ctx, s)
	err = h.d.DB.QueryRow(ctx, `
		insert into strategies (user_id, name, description, prompt, spec, metrics, equity, benchmark, trades, explanation)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		returning id, created_at, (select name from users where id = $1)`,
		uid, s.Name, s.Description, s.Prompt, s.Spec, s.Metrics, s.Equity, s.Benchmark, s.Trades, s.Explanation,
	).Scan(&s.ID, &s.CreatedAt, &s.Author)
	if err != nil {
		app.WriteError(w, err)
		return
	}
	s.Community = &CommunityInfo{Owner: s.Author, Mine: true}
	s.Forward = h.forward(ctx, s) // saved just now: nothing to run or fetch yet
	httpx.JSON(w, http.StatusCreated, map[string]any{"strategy": s, "credits": credits})
}

// check fills what the model left out from the user's selected market,
// validates, and backtests. Spec problems come back as *app.BadRequest.
func (h *handler) check(ctx context.Context, g *generated, symbol, interval string) (Result, error) {
	g.Name = clip(cmp.Or(strings.TrimSpace(g.Name), strings.TrimSpace(g.Spec.Name), "AI strategy"), 80)
	g.Description = clip(strings.TrimSpace(g.Description), 500)
	g.Spec.Name = g.Name
	g.Spec.Symbol = cmp.Or(g.Spec.Symbol, symbol)
	g.Spec.Interval = cmp.Or(g.Spec.Interval, interval)
	g.Spec.Bars = max(g.Spec.Bars, barsFloor[g.Spec.Interval])
	if err := g.Spec.Validate(); err != nil {
		return Result{}, &app.BadRequest{Msg: err.Error()}
	}
	return h.run(ctx, g.Spec)
}

// explain asks for a short reading of the computed metrics; a failure falls
// back to a plain Go summary rather than failing the request.
func (h *handler) explain(ctx context.Context, s Strategy) string {
	facts, _ := json.Marshal(map[string]any{"name": s.Name, "description": s.Description, "spec": s.Spec,
		"metrics": s.Metrics, "windowDays": (s.Metrics.EndT - s.Metrics.StartT) / 86400})
	text, err := h.d.LLM.Complete(ctx, explainSystem, string(facts), llm.Low)
	if text = strings.TrimSpace(text); err != nil || text == "" {
		slog.Warn("strategy explanation failed", "err", err)
		return fallbackExplanation(s)
	}
	return text
}

func fallbackExplanation(s Strategy) string {
	m := s.Metrics
	days := (m.EndT - m.StartT) / 86400
	text := fmt.Sprintf("Over the last %d days of %s %s bars this strategy returned %+.1f%% against %+.1f%% for buy-and-hold, "+
		"with a %.1f%% maximum drawdown and a Sharpe ratio of %.2f across %d trades.",
		days, s.Spec.Symbol, s.Spec.Interval, m.TotalReturnPct, m.BuyHoldReturnPct, m.MaxDrawdownPct, m.Sharpe, m.Trades)
	if m.Trades < 30 {
		text += " That is a small sample, so treat the result as anecdotal."
	}
	return text + " It is one historical window; past results do not predict future ones."
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var list []Strategy
	var err error
	switch r.URL.Query().Get("scope") {
	case "top":
		list, err = h.labs.get(ctx, h.runBefore(labsCreated))
		list = slices.Clone(list) // the cache's own rows stay without a forward
	case "", "mine":
		list, err = h.mine(ctx, httpx.UserID(ctx))
	case "community":
		list, err = h.community(ctx, httpx.UserID(ctx))
	default:
		httpx.Error(w, http.StatusBadRequest, "bad_request", "scope must be mine, top or community.")
		return
	}
	if err != nil {
		app.WriteError(w, err)
		return
	}
	_, done, accepted := h.d.Tasks.Begin("strategy_forward", ctx)
	if !accepted {
		httpx.Error(w, http.StatusServiceUnavailable, "unavailable", "Server is stopping.")
		return
	}
	workersDone := h.forwards(ctx, list)
	go func() { defer done(); <-workersDone }()
	out := make([]Summary, len(list))
	for i, s := range list {
		out[i] = summarize(s)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"strategies": out})
}

// selectStrategy reads saved strategies as user $1 sees them: with their
// community counts, and copiedFrom only while its source is visible to $1.
// ponytail: counts are correlated subqueries per row (≤ 100 rows a page; autopilots has no strategy_id index).
var selectStrategy = `
	select s.id, s.name, s.description, u.name, s.prompt, s.spec, s.metrics, s.equity, s.benchmark, s.trades,
		s.explanation, s.created_at, s.published_at, s.user_id = $1, k.stars, k.starred, k.copies, k.deploys, src.id, src.name, src.owner
	from strategies s join users u on u.id = s.user_id
	cross join lateral (select
		(select count(*) from strategy_stars where strategy_id = s.id) stars,
		exists (select 1 from strategy_stars where strategy_id = s.id and user_id = $1) starred,
		(select count(*) from strategies where copied_from = s.id) copies,
		(select count(distinct a.user_id) from autopilots a
		 where a.strategy_id = s.id::text or a.strategy_id in (select id::text from strategies where copied_from = s.id)) deploys) k
	left join lateral (select c.id, c.name, cu.name owner from strategies c join users cu on cu.id = c.user_id
		where c.id = s.copied_from and ` + visible("c", "cu") + `) src on true`

// ponytail: "mine" loads full rows (≤ 400-point curves) to build sparklines; cap at 100.
func (h *handler) mine(ctx context.Context, uid string) ([]Strategy, error) {
	rows, err := h.d.DB.Query(ctx, selectStrategy+" where s.user_id = $1 order by s.created_at desc limit 100", uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Strategy
	for rows.Next() {
		var s Strategy
		if err := scan(rows, &s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func scan(row interface{ Scan(...any) error }, s *Strategy) error {
	c := &CommunityInfo{}
	var srcID, srcName, srcOwner *string
	err := row.Scan(&s.ID, &s.Name, &s.Description, &s.Author, &s.Prompt, &s.Spec, &s.Metrics, &s.Equity,
		&s.Benchmark, &s.Trades, &s.Explanation, &s.CreatedAt, &c.PublishedAt, &c.Mine, &c.Stars, &c.Starred, &c.Copies, &c.Deploys,
		&srcID, &srcName, &srcOwner)
	c.Published, c.Owner = c.PublishedAt != nil, s.Author
	if c.CopiedFrom = labsSource(*s); srcID != nil {
		c.CopiedFrom = &Source{ID: *srcID, Name: *srcName, Owner: *srcOwner}
	}
	s.Community = c
	if !c.Mine {
		s.Prompt = "" // another user's own words stay private; copies carry the rules, not the ask
	}
	// ponytail: rows saved before the 90-day rule annualized short windows, and
	// before the null win rate stored 0 for no trades (older ones also lack
	// sortino, volatilityAnnPct, calmar, feesPct: absent / null on read);
	// fixed on read, no backfill.
	// A quote-feed row saved before its session annualization is re-annualized
	// (sessionMetrics); its annualized return then already follows the rule.
	sessionMetrics(&s.Metrics, s.Spec.Interval, s.Spec.Symbol)
	if s.Metrics.BarsPerYear == nil && windowDays(s.Metrics.Bars, s.Spec.Interval) < market.MinAnnualizeDays {
		s.Metrics.AnnualizedReturnPct, s.Metrics.Calmar = nil, nil
	}
	if s.Metrics.Trades == 0 {
		s.Metrics.WinRatePct = nil
	}
	s.ShortSample = shortSample(s.Metrics)
	return err
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), r.PathValue("id")
	if !isUUID(id) {
		app.WriteError(w, app.ErrNotFound)
		return
	}
	if slices.ContainsFunc(labs, func(l labsStrategy) bool { return nameID(l.spec.Name) == id }) {
		list, err := h.labs.get(ctx, h.runBefore(labsCreated))
		if err != nil {
			app.WriteError(w, err)
			return
		}
		s := list[slices.IndexFunc(list, func(s Strategy) bool { return s.ID == id })]
		s.Forward = h.forward(ctx, s)
		httpx.JSON(w, http.StatusOK, map[string]any{"strategy": s})
		return
	}
	s, err := h.load(ctx, httpx.UserID(ctx), id, visible("s", "u")) // own, or published (read-only for others)
	if err != nil {
		app.WriteError(w, err)
		return
	}
	s.Forward = h.forward(ctx, s)
	httpx.JSON(w, http.StatusOK, map[string]any{"strategy": s})
}

// Find returns the user's saved strategy id or the built-in with that id
// (its definition only: Result is left empty, the backtest lives in the
// labs cache); anything else is app.ErrNotFound.
func Find(ctx context.Context, d *app.Deps, userID, id string) (Strategy, error) {
	if !isUUID(id) {
		return Strategy{}, app.ErrNotFound
	}
	if i := slices.IndexFunc(labs, func(l labsStrategy) bool { return nameID(l.spec.Name) == id }); i >= 0 {
		l := labs[i]
		return Strategy{ID: id, Name: l.spec.Name, Description: l.description, Author: labsAuthor, Prompt: l.description,
			Spec: l.spec, CreatedAt: labsCreated}, nil
	}
	var s Strategy
	err := scan(d.DB.QueryRow(ctx, selectStrategy+" where s.id = $2 and s.user_id = $1", userID, id), &s)
	if errors.Is(err, pgx.ErrNoRows) {
		return Strategy{}, app.ErrNotFound
	}
	return s, err
}

func (h *handler) delete(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), r.PathValue("id")
	if !isUUID(id) {
		app.WriteError(w, app.ErrNotFound)
		return
	}
	tag, err := h.d.DB.Exec(ctx, "delete from strategies where id = $1 and user_id = $2", id, httpx.UserID(ctx))
	if err != nil {
		app.WriteError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		app.WriteError(w, app.ErrNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// isUUID keeps malformed ids from reaching Postgres as a 500.
func isUUID(s string) bool {
	var u pgtype.UUID
	return len(s) == 36 && u.Scan(s) == nil
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

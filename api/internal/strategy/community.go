package strategy

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/httpx"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
)

// CommunityInfo is a saved strategy's standing in the community (contract
// CommunityInfo); nil on built-in Labs.
type CommunityInfo struct {
	Published   bool       `json:"published"`
	PublishedAt *time.Time `json:"publishedAt"`
	Owner       string     `json:"owner"` // users.name
	Mine        bool       `json:"mine"`
	Stars       int        `json:"stars"`
	Starred     bool       `json:"starred"` // by the caller
	Copies      int        `json:"copies"`  // strategies copied from this one
	Deploys     int        `json:"deploys"` // distinct users with an autopilot on it or on a copy
	CopiedFrom  *Source    `json:"copiedFrom"`
}

// Source is where a copy came from, while the caller can still see it.
type Source struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Owner string `json:"owner"`
}

// minPublishTrades keeps one-trade flukes off the community board.
const (
	minPublishTrades = 3
	maxCommunity     = 50
	maxSaved         = 100 // the studio lists 100; copies are free, so they stop here
)

// visible is the SQL test that strategy alias s (owner alias u) is $1's own
// or published by an enabled account: what GET, star, copy and copiedFrom
// may show.
func visible(s, u string) string {
	return fmt.Sprintf("(%[1]s.user_id = $1 or (%[1]s.published_at is not null and %[2]s.disabled_at is null))", s, u)
}

// labsSource recognizes a copy of a built-in, whose copied_from is null (it
// cannot reference a Labs id): same name and prompt as the built-in.
// ponytail: a user who saves a Labs description verbatim under its name reads as a Labs copy.
func labsSource(s Strategy) *Source {
	i := slices.IndexFunc(labs, func(l labsStrategy) bool { return l.spec.Name == s.Name && l.description == s.Prompt })
	if i < 0 {
		return nil
	}
	return &Source{ID: nameID(s.Name), Name: s.Name, Owner: labsAuthor}
}

// community is every enabled account's published strategies: full samples
// first, then stars, Sharpe and the newest publish. A quote-feed row saved
// before its session annualization orders by the Sharpe it reads back with
// (sessionMetrics: × √((bars − 1) × bar seconds / span)).
func (h *handler) community(ctx context.Context, uid string) ([]Strategy, error) {
	rows, err := h.d.DB.Query(ctx, selectStrategy+fmt.Sprintf(`
		where s.published_at is not null and u.disabled_at is null
		order by coalesce((s.metrics->>'endT')::bigint - (s.metrics->>'startT')::bigint < %d or (s.metrics->>'trades')::int < %d, true),
			k.stars desc, %s desc nulls last, s.published_at desc
		limit %d`, minSampleDays*86400, minSampleTrades, communitySharpe, maxCommunity), uid, quoteFeedSymbols())
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

// communitySharpe is the Sharpe a row reads back with, in SQL ($2: the
// quote-feed symbols).
const communitySharpe = `case
		when s.spec->>'symbol' = any($2::text[]) and s.metrics->'barsPerYear' is null
			and (s.metrics->>'endT')::bigint > (s.metrics->>'startT')::bigint and (s.metrics->>'bars')::int >= 2
		then (s.metrics->>'sharpe')::float8 * sqrt(((s.metrics->>'bars')::float8 - 1)
			* (case s.spec->>'interval' when '15m' then 900 when '1h' then 3600 when '4h' then 14400 when '1d' then 86400 end)
			/ ((s.metrics->>'endT')::bigint - (s.metrics->>'startT')::bigint))
		else (s.metrics->>'sharpe')::float8 end`

// quoteFeedSymbols are the named quote feeds' symbols, then the listed ones'.
func quoteFeedSymbols() []string {
	out := make([]string, 0, len(market.QuoteFeeds)+len(market.Listed))
	for _, a := range slices.Concat(market.QuoteFeeds, market.Listed) {
		out = append(out, a.Symbol)
	}
	return out
}

// load reads one saved strategy as uid sees it, under an extra where clause
// on $2 = id.
func (h *handler) load(ctx context.Context, uid, id, where string) (Strategy, error) {
	var s Strategy
	err := scan(h.d.DB.QueryRow(ctx, selectStrategy+" where s.id = $2 and "+where, uid, id), &s)
	return s, err
}

// publish lists or unlists an own strategy: POST /api/strategies/{id}/publish.
func (h *handler) publish(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Published *bool `json:"published"`
	}
	if err := httpx.Decode(w, r, &req); err != nil || req.Published == nil {
		httpx.Error(w, http.StatusBadRequest, "bad_request", "published must be true or false.")
		return
	}
	ctx, uid, id := r.Context(), httpx.UserID(r.Context()), r.PathValue("id")
	if !isUUID(id) {
		app.WriteError(w, app.ErrNotFound)
		return
	}
	var trades int
	err := h.d.DB.QueryRow(ctx, "select coalesce((metrics->>'trades')::int, 0) from strategies where id = $1 and user_id = $2", id, uid).Scan(&trades)
	if err == nil && *req.Published && trades < minPublishTrades {
		err = &app.Conflict{Msg: fmt.Sprintf("Backtest at least %d trades before publishing.", minPublishTrades)}
	}
	if err == nil { // unpublishing keeps stars and copies; re-publishing an already published one keeps its date
		_, err = h.d.DB.Exec(ctx, "update strategies set published_at = case when $3::boolean then coalesce(published_at, now()) end where id = $1 and user_id = $2",
			id, uid, *req.Published)
	}
	var s Strategy
	if err == nil {
		s, err = h.load(ctx, uid, id, "s.user_id = $1")
	}
	if err != nil {
		app.WriteError(w, err)
		return
	}
	s.Forward = h.forward(ctx, s) // since moved to the publish (nothing to run yet) or back to the save
	httpx.JSON(w, http.StatusOK, map[string]any{"strategy": s})
}

// star stars or unstars a published strategy (own included): POST
// /api/strategies/{id}/star. Idempotent both ways.
func (h *handler) star(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Starred *bool `json:"starred"`
	}
	if err := httpx.Decode(w, r, &req); err != nil || req.Starred == nil {
		httpx.Error(w, http.StatusBadRequest, "bad_request", "starred must be true or false.")
		return
	}
	ctx, uid, id := r.Context(), httpx.UserID(r.Context()), r.PathValue("id")
	var ok bool
	if isUUID(id) {
		if err := h.d.DB.QueryRow(ctx, `select exists (select 1 from strategies s join users u on u.id = s.user_id
			where s.id = $2 and s.published_at is not null and `+visible("s", "u")+")", uid, id).Scan(&ok); err != nil {
			app.WriteError(w, err)
			return
		}
	}
	if !ok {
		app.WriteError(w, app.ErrNotFound)
		return
	}
	// ponytail: checked then written; an unpublish in between still takes the star (stars survive an unpublish anyway).
	sql := "insert into strategy_stars (user_id, strategy_id) values ($1, $2) on conflict do nothing"
	if !*req.Starred {
		sql = "delete from strategy_stars where user_id = $1 and strategy_id = $2"
	}
	var out struct {
		Stars   int  `json:"stars"`
		Starred bool `json:"starred"`
	}
	_, err := h.d.DB.Exec(ctx, sql, uid, id)
	if err == nil {
		err = h.d.DB.QueryRow(ctx, "select count(*), coalesce(bool_or(user_id = $1), false) from strategy_stars where strategy_id = $2", uid, id).
			Scan(&out.Stars, &out.Starred)
	}
	if err != nil {
		app.WriteError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// copy saves a published, built-in or own strategy into the caller's studio,
// re-backtested on fresh candles: POST /api/strategies/{id}/copy. Go only:
// no model call, no credits.
func (h *handler) copy(w http.ResponseWriter, r *http.Request) {
	ctx, uid, id := r.Context(), httpx.UserID(r.Context()), r.PathValue("id")
	var src Strategy
	var from *string // copied_from: null for a built-in
	var err error
	if slices.ContainsFunc(labs, func(l labsStrategy) bool { return nameID(l.spec.Name) == id }) {
		src, err = Find(ctx, h.d, uid, id)
	} else if !isUUID(id) {
		err = app.ErrNotFound
	} else if src, err = h.load(ctx, uid, id, visible("s", "u")); err == nil {
		from = &src.ID
	}
	var n int
	if err == nil {
		err = h.d.DB.QueryRow(ctx, "select count(*) from strategies where user_id = $1", uid).Scan(&n)
	}
	if err == nil && n >= maxSaved {
		err = &app.Conflict{Msg: fmt.Sprintf("You have %d saved strategies. Delete one to copy another.", maxSaved)}
	}
	if err != nil {
		app.WriteError(w, err)
		return
	}
	res, err := h.run(ctx, src.Spec)
	if err != nil {
		app.WriteError(w, err)
		return
	}
	c := Strategy{Name: src.Name, Description: src.Description, Prompt: src.Prompt, Spec: src.Spec, Result: res, Explanation: src.Explanation}
	if from == nil { // a built-in's read is Go's summary of its numbers
		c.Explanation = fallbackExplanation(c)
	}
	var newID string
	err = h.d.DB.QueryRow(ctx, `
		insert into strategies (user_id, name, description, prompt, spec, metrics, equity, benchmark, trades, explanation, copied_from)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) returning id`,
		uid, c.Name, c.Description, c.Prompt, c.Spec, c.Metrics, c.Equity, c.Benchmark, c.Trades, c.Explanation, from).Scan(&newID)
	if err == nil {
		c, err = h.load(ctx, uid, newID, "s.user_id = $1")
	}
	if err != nil {
		app.WriteError(w, err)
		return
	}
	c.Forward = h.forward(ctx, c) // saved just now: its forward starts here
	httpx.JSON(w, http.StatusCreated, map[string]any{"strategy": c})
}

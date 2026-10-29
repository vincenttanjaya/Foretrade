package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/httpx"
)

// Seeded counts the showcase rows copied into an account (contract `seeded`).
type Seeded struct {
	Forecasts  int64 `json:"forecasts"`
	Portfolios int64 `json:"portfolios"`
	Strategies int64 `json:"strategies"`
	ArenaRuns  int64 `json:"arenaRuns"`
}

// Every column copied, by table; id and user_id are always fresh. A new
// column is left out of the copy until it is added here on purpose.
// forecasts: not replay_key (always null on an owned row), parent_id (remapped
// onto the copies) nor seeded_from (set to the source row).
// arena_runs: not credits (the copy cost nothing: 0).
const (
	forecastCols  = "shape, question, outcomes, resolution_criteria, resolution_date, category, status, error, result, created_at, completed_at, price_rule, outcome, resolved_at, resolved_by, as_of, anchor_p, context, linked_market, materials"
	portfolioCols = "name, thesis, prompt, holdings, backtest, created_at"
	strategyCols  = "name, description, prompt, spec, metrics, equity, benchmark, trades, explanation, created_at"
	arenaRunCols  = "name, status, cadence, start_at, steps, steps_done, universe, model, knowledge_cutoff, agents, benchmarks, error, created_at, completed_at"
	arenaStepCols = "agent, step, equity, cash, positions, targets, fills, tools, reasoning, error"
)

// showcaseSource is the id and normalized email of the account to copy from;
// an unknown email is a 400 naming field.
func showcaseSource(ctx context.Context, tx pgx.Tx, field, email string) (id, normalized string, err error) {
	normalized = strings.ToLower(strings.TrimSpace(email))
	err = tx.QueryRow(ctx, "select id from users where email = $1", normalized).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", &app.BadRequest{Msg: field + ": no account with that email."}
	}
	return id, normalized, err
}

// copyForecasts copies from's 3 newest lineage heads (completed runs no
// completed update supersedes) plus up to 2 newest resolved heads not among
// them (so the copy's own track record is not empty) with their completed
// ancestors, so each copy keeps its probability history, then points every
// copy's parent_id at the copy of its source's parent. A source row already
// copied into to is skipped, so a re-seed adds none. Returns the heads copied.
// ponytail: walks 20 levels up, like the forecast desk's lineage.
func copyForecasts(ctx context.Context, tx pgx.Tx, from, to string) (int64, error) {
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		with recursive lineage_heads as (
			select f.id, f.completed_at, f.outcome from forecasts f
			where f.user_id = $1 and f.status = 'completed'
			  and not exists (select 1 from forecasts c where c.parent_id = f.id and c.status = 'completed')
		), newest as (
			select id from lineage_heads order by completed_at desc nulls last limit 3
		), heads as (
			select id from newest
			union all
			(select id from lineage_heads where outcome is not null and id not in (select id from newest)
			 order by completed_at desc nulls last limit 2)
		), up as (
			select id, parent_id, 0 as depth from forecasts where id in (select id from heads)
			union all
			select f.id, f.parent_id, up.depth + 1 from forecasts f join up on f.id = up.parent_id where up.depth < 20
		)
		insert into forecasts (user_id, seeded_from, %[1]s)
		select $2, s.id, %[1]s from forecasts s
		where s.id in (select id from up) and s.user_id = $1 and s.status = 'completed'
		  and not exists (select 1 from forecasts t where t.user_id = $2 and t.seeded_from = s.id)
		returning id, seeded_from in (select id from heads)`, forecastCols), from, to)
	if err != nil {
		return 0, err
	}
	var ids []string
	var heads int64
	var id string
	var head bool
	if _, err := pgx.ForEachRow(rows, []any{&id, &head}, func() error {
		ids = append(ids, id)
		if head {
			heads++
		}
		return nil
	}); err != nil {
		return 0, err
	}
	_, err = tx.Exec(ctx, `
		update forecasts c set parent_id = p.id from forecasts src, forecasts p
		where c.id = any($1) and src.id = c.seeded_from and p.user_id = $2 and p.seeded_from = src.parent_id`, ids, to)
	return heads, err
}

// copyShowcase copies from's showcase content into to as fresh rows owned by
// to, in tx, winners first: the 3 newest forecasts (lineage heads) plus up to 2
// resolved ones, with their completed update history (timestamps kept, so
// reports read as dated); up to 2 AI portfolios that beat BTC, by the most;
// up to 3 strategies with Sharpe above 0.5 that beat buy-and-hold, best Sharpe
// first (10+ trades first); and the completed arena run whose best agent beat
// BTC by the most, with its steps. When no portfolio or strategy qualifies,
// the single best one is copied instead (a fresh account never opens to an
// empty tab): the portfolio by return minus BTC's, the strategy by Sharpe
// among those with 3+ trades. Chats, trades, positions, autopilots, alerts
// and a strategy's publish date, stars and copy source are never copied.
// ponytail: portfolios, strategies and arena runs are not deduped, each call copies them again; dedupe on (source id) if admins re-seed often.
func copyShowcase(ctx context.Context, tx pgx.Tx, from, to string) (Seeded, error) {
	var s Seeded
	var err error
	if s.Forecasts, err = copyForecasts(ctx, tx, from, to); err != nil {
		return s, err
	}
	const edge = "(backtest->>'returnPct')::float8 - (backtest->>'benchmarkReturnPct')::float8"
	for _, c := range []struct {
		n             *int64
		table, cols   string
		winners, best string
	}{
		{&s.Portfolios, "ai_portfolios", portfolioCols,
			"and " + edge + " > 0 order by " + edge + " desc nulls last, created_at desc limit 2",
			"and " + edge + " is not null order by " + edge + " desc, created_at desc limit 1"},
		{&s.Strategies, "strategies", strategyCols,
			`and (metrics->>'sharpe')::float8 > 0.5 and (metrics->>'totalReturnPct')::float8 > (metrics->>'buyHoldReturnPct')::float8
			 order by ((metrics->>'trades')::int >= 10) desc nulls last, (metrics->>'sharpe')::float8 desc nulls last, created_at desc limit 3`,
			`and (metrics->>'trades')::int >= 3 and (metrics->>'sharpe')::float8 is not null
			 order by (metrics->>'sharpe')::float8 desc, created_at desc limit 1`},
	} {
		for _, where := range []string{c.winners, c.best} {
			tag, err := tx.Exec(ctx, fmt.Sprintf("insert into %[1]s (user_id, %[2]s) select $2, %[2]s from %[1]s where user_id = $1 %[3]s",
				c.table, c.cols, where), from, to)
			if err != nil {
				return s, err
			}
			if *c.n = tag.RowsAffected(); *c.n > 0 {
				break
			}
		}
	}
	var src, run string
	err = tx.QueryRow(ctx, `
		select id from arena_runs where user_id = $1 and status = 'completed'
		order by (select max((a->'metrics'->>'totalReturnPct')::float8) from jsonb_array_elements(agents) a)
		  - ((benchmarks->'btc'->>steps_done)::float8 / nullif((benchmarks->'btc'->>0)::float8, 0) - 1) * 100 desc nulls last,
		  created_at desc limit 1`, from).Scan(&src)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	err = tx.QueryRow(ctx, fmt.Sprintf("insert into arena_runs (user_id, credits, %[1]s) select $2, 0, %[1]s from arena_runs where id = $1 returning id",
		arenaRunCols), src, to).Scan(&run)
	if err != nil {
		return s, err
	}
	s.ArenaRuns = 1
	_, err = tx.Exec(ctx, fmt.Sprintf("insert into arena_steps (run_id, %[1]s) select $2, %[1]s from arena_steps where run_id = $1", arenaStepCols), src, run)
	return s, err
}

// seed copies showcase content from another account into {id}: POST /api/admin/users/{id}/seed.
func (h *handler) seed(w http.ResponseWriter, r *http.Request) {
	var req struct {
		From string `json:"from"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	var seeded Seeded
	h.mutate(w, r, func(ctx context.Context, tx pgx.Tx, uid string, t target) (string, map[string]any, error) {
		from, email, err := showcaseSource(ctx, tx, "from", req.From)
		if err != nil {
			return "", nil, err
		}
		if from == uid {
			return "", nil, &app.BadRequest{Msg: "Pick a different account to copy from."}
		}
		seeded, err = copyShowcase(ctx, tx, from, uid)
		return "user.seed", map[string]any{"seededFrom": email, "seeded": seeded}, err
	}, func(string) map[string]any { return map[string]any{"seeded": seeded} })
}

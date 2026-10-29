package admin

import (
	"reflect"
	"strings"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
)

// showcase gives uid more of everything than a seed copies, plus rows a seed must skip:
// forecasts are a lineage (L0 updated by L1, the newest head), heads Q2..Q6 (Q2, Q4, Q5
// and Q6 resolved), an archived update of Q2 (it does not supersede Q2) and a running run;
// losing portfolios and strategies; an older arena run that beat BTC by more than a newer one.
func showcase(t *testing.T, e *apptest.Env, uid string) {
	t.Helper()
	forecast := func(q, status string, daysAgo int, parent string) string {
		t.Helper()
		return scalar[string](t, e, `insert into forecasts (user_id, shape, question, outcomes, resolution_date, status, result, price_rule, created_at, completed_at, parent_id)
			values ($1, 'binary', $2, '["Yes","No"]', '2030-01-01', $3, '{"probabilities":[{"outcome":"Yes","p":0.6}]}',
				'{"symbol":"BTC","op":"above","threshold":1,"mode":"on_date"}', now() - make_interval(days => $4), now() - make_interval(days => $4), nullif($5, '')::uuid)
			returning id`, uid, q, status, daysAgo, parent)
	}
	forecast("L1", "completed", 1, forecast("L0", "completed", 5, ""))
	q2 := forecast("Q2", "completed", 2, "")
	forecast("Q3", "completed", 3, "")
	forecast("Q4", "completed", 4, "")
	forecast("Q5", "completed", 7, "")
	forecast("Q6", "completed", 8, "")
	exec(t, e, `update forecasts set outcome = 'Yes', resolved_at = now(), resolved_by = 'manual' where user_id = $1 and question in ('Q2', 'Q4', 'Q5', 'Q6')`, uid)
	forecast("Q2 archived update", "archived", 0, q2)
	forecast("Running", "running", 0, "")
	// Portfolios beat BTC by +20, -5 and unknown points; strategies: thin (3 trades, Sharpe 2), steady (12, 0.8), best (20, 1.5),
	// loser (15, 1.2, trails buy-and-hold), negative (30, Sharpe -0.4, beats a falling buy-and-hold), bare (no metrics).
	exec(t, e, `insert into ai_portfolios (user_id, name, thesis, prompt, holdings, backtest, created_at)
		select $1, n, 't', 'p', '[]', b::jsonb, now() - make_interval(days => i) from (values
			(1, 'Lagging', '{"returnPct":5,"benchmarkReturnPct":10}'), (2, 'Best', '{"returnPct":30,"benchmarkReturnPct":10}'),
			(4, 'Unknown', '{}')) v(i, n, b)`, uid)
	exec(t, e, `insert into strategies (user_id, name, description, prompt, spec, metrics, equity, benchmark, trades, created_at)
		select $1, n, '', '', '{}', m::jsonb, '[]', '[]', '[]', now() - make_interval(days => i) from (values
			(1, 'Thin', '{"trades":3,"sharpe":2.0,"totalReturnPct":30,"buyHoldReturnPct":10}'),
			(2, 'Steady', '{"trades":12,"sharpe":0.8,"totalReturnPct":15,"buyHoldReturnPct":10}'),
			(3, 'Best', '{"trades":20,"sharpe":1.5,"totalReturnPct":40,"buyHoldReturnPct":10}'), (4, 'Bare', '{}'),
			(5, 'Loser', '{"trades":15,"sharpe":1.2,"totalReturnPct":5,"buyHoldReturnPct":10}'),
			(6, 'Negative', '{"trades":30,"sharpe":-0.4,"totalReturnPct":-8,"buyHoldReturnPct":-12}')) v(i, n, m)`, uid)
	// The best agent vs BTC: Best run +12 vs BTC +1 (11 points; agent B has no metrics), Newest run +15 vs +10 (5 points).
	exec(t, e, `insert into arena_runs (user_id, name, status, cadence, start_at, steps, steps_done, universe, model, knowledge_cutoff, agents,
		benchmarks, credits, created_at) values
		($1, 'Best run', 'completed', '1d', now(), 2, 2, '{BTC}', 'm', '2026-06-30',
			'[{"name":"A","metrics":{"totalReturnPct":12}},{"name":"B","metrics":null}]', '{"btc":[100,101,101]}', 15, now() - interval '9 days'),
		($1, 'Newest run', 'completed', '1d', now(), 2, 2, '{BTC}', 'm', '2026-06-30', '[{"name":"A","metrics":{"totalReturnPct":15}}]',
			'{"btc":[100,105,110]}', 15, now() - interval '2 days'),
		($1, 'Bare run', 'completed', '1d', now(), 2, 2, '{BTC}', 'm', '2026-06-30', '[]', '{}', 15, now() - interval '1 days'),
		($1, 'Failed run', 'failed', '1d', now(), 2, 0, '{BTC}', 'm', '2026-06-30', '[]', '{}', 0, now())`, uid)
	exec(t, e, `insert into arena_steps (run_id, agent, step, equity, cash, positions, reasoning)
		select id, 0, s, 100000 + s, 50000, '{}', 'step ' || s from arena_runs, generate_series(0, 2) s where user_id = $1 and name = 'Best run'`, uid)
	exec(t, e, "insert into chats (user_id, title) values ($1, 'Private chat')", uid)
	exec(t, e, `insert into trades (user_id, symbol, side, qty, price, notional, fee, source) values ($1, 'BTC', 'buy', 1, 1, 1, 0, 'manual')`, uid)
}

// lineage lists uid's forecasts oldest first as "question<-parent question", each copy tied to a source row of src.
func lineage(t *testing.T, e *apptest.Env, uid, src string) string {
	t.Helper()
	return scalar[string](t, e, `select string_agg(c.question || coalesce('<-' || p.question, ''), ',' order by c.created_at)
		from forecasts c join forecasts s on s.id = c.seeded_from and s.user_id = $2 and s.question = c.question
		left join forecasts p on p.id = c.parent_id and p.user_id = $1
		where c.user_id = $1`, uid, src)
}

type seedResp struct {
	User   User
	Seeded *Seeded
}

func TestSeedFrom(t *testing.T) {
	e := setup(t, apptest.Options{})
	src, srcEmail, _ := account(t, e, "user")
	showcase(t, e, src)
	count := func(uid, table string) int {
		return scalar[int](t, e, "select count(*) from "+table+" where user_id = $1", uid)
	}

	// An unknown source is a 400 and creates nothing.
	if w := e.Do("POST", "/api/admin/users", map[string]any{"email": "nobody@example.test", "name": "N", "seedFrom": "ghost@example.test"}); w.Code != 400 ||
		!strings.Contains(w.Body.String(), "seedFrom: no account with that email.") || scalar[int](t, e, "select count(*) from users where email = 'nobody@example.test'") != 0 {
		t.Fatalf("unknown seedFrom: %d %s", w.Code, w.Body)
	}

	w := e.Do("POST", "/api/admin/users", map[string]any{"email": "investor@example.test", "name": "Investor", "seedFrom": "  " + strings.ToUpper(srcEmail)})
	res := apptest.JSON[seedResp](t, w)
	uid := res.User.ID
	if w.Code != 201 || res.Seeded == nil || *res.Seeded != (Seeded{Forecasts: 5, Portfolios: 1, Strategies: 3, ArenaRuns: 1}) {
		t.Fatalf("create with seedFrom: %d %s", w.Code, w.Body)
	}
	// The 3 newest lineage heads, the 2 newest resolved heads besides them (Q2 counts once; Q6 is left) and L1's
	// root, as fresh rows owned by the new account, timestamps kept, each copy pointing at its source and L1's copy at
	// L0's; never the archived update or the running run.
	if got := lineage(t, e, uid, src); got != "Q5,L0,Q4,Q3,Q2,L1<-L0" {
		t.Errorf("forecasts: %s", got)
	}
	if n := scalar[int](t, e, `select count(*) from forecasts c join forecasts s on s.id = c.seeded_from
		where c.user_id = $1 and c.id <> s.id and c.created_at = s.created_at and c.completed_at = s.completed_at and c.result = s.result and c.price_rule = s.price_rule
		  and c.outcome is not distinct from s.outcome`, uid); n != 6 {
		t.Errorf("copied forecasts match their sources: %d", n)
	}
	// Only portfolios that beat BTC; strategies with Sharpe above 0.5 that beat buy-and-hold, best Sharpe first, 10+ trades first.
	if got := scalar[string](t, e, `select string_agg(name, ',' order by name) from ai_portfolios where user_id = $1`, uid); got != "Best" {
		t.Errorf("portfolios: %s", got)
	}
	if got := scalar[string](t, e, `select string_agg(name, ',' order by created_at desc) from strategies where user_id = $1`, uid); got != "Thin,Steady,Best" {
		t.Errorf("strategies: %s", got)
	}
	if got := scalar[string](t, e, `select r.name || ' ' || r.credits || ' ' || count(s.*) || ' ' || string_agg(s.reasoning, '|' order by s.step)
		from arena_runs r join arena_steps s on s.run_id = r.id where r.user_id = $1 group by r.id`, uid); got != "Best run 0 3 step 0|step 1|step 2" {
		t.Errorf("arena: %s", got)
	}
	if count(uid, "chats")+count(uid, "trades")+count(uid, "autopilots") != 0 || count(src, "forecasts") != 9 || count(src, "arena_runs") != 4 {
		t.Errorf("copied too much or moved the source's rows")
	}
	got := audits(t, e)
	if len(got) != 1 || got[0].Action != "user.create" || !reflect.DeepEqual(detail(t, got[0]), map[string]any{"role": "user", "creditLimit": nil,
		"passwordGenerated": true, "seededFrom": srcEmail, "seeded": map[string]any{"forecasts": 5.0, "portfolios": 1.0, "strategies": 3.0, "arenaRuns": 1.0}}) {
		t.Fatalf("audit: %s", got[0].Detail)
	}

	// /seed copies portfolios, strategies and the arena run again into an existing account, but no forecast twice;
	// self-copy and unknown sources are 400s.
	w = e.Do("POST", "/api/admin/users/"+uid+"/seed", map[string]string{"from": srcEmail})
	if res := apptest.JSON[seedResp](t, w); w.Code != 200 || res.Seeded == nil || *res.Seeded != (Seeded{0, 1, 3, 1}) || res.User.ID != uid ||
		count(uid, "forecasts") != 6 || count(uid, "arena_runs") != 2 {
		t.Fatalf("seed: %d %s", w.Code, w.Body)
	}
	for body, msg := range map[string]string{`{"from":"investor@example.test"}`: "Pick a different account to copy from.",
		`{"from":"ghost@example.test"}`: "from: no account with that email.", `{}`: "from: no account with that email."} {
		if w := e.Do("POST", "/api/admin/users/"+uid+"/seed", body); w.Code != 400 || !strings.Contains(w.Body.String(), msg) {
			t.Errorf("%s: %d %s", body, w.Code, w.Body)
		}
	}
	if w := e.Do("POST", "/api/admin/users/00000000-0000-4000-8000-000000000000/seed", map[string]string{"from": srcEmail}); w.Code != 404 {
		t.Errorf("unknown account: %d", w.Code)
	}
	got = audits(t, e)
	if len(got) != 2 || got[0].Action != "user.seed" || got[0].Target.ID != uid || !reflect.DeepEqual(detail(t, got[0]), map[string]any{"seededFrom": srcEmail,
		"seeded": map[string]any{"forecasts": 0.0, "portfolios": 1.0, "strategies": 3.0, "arenaRuns": 1.0}}) {
		t.Fatalf("seed audit: %+v", got)
	}

	// The source updates L1: a re-seed copies only the new head, as an update of L1's earlier copy.
	exec(t, e, `insert into forecasts (user_id, shape, question, outcomes, resolution_date, status, result, created_at, completed_at, parent_id)
		select user_id, shape, 'L2', outcomes, resolution_date, 'completed', result, now(), now(), id from forecasts where user_id = $1 and question = 'L1'`, src)
	if w := e.Do("POST", "/api/admin/users/"+uid+"/seed", map[string]string{"from": srcEmail}); w.Code != 200 || apptest.JSON[seedResp](t, w).Seeded.Forecasts != 1 {
		t.Fatalf("re-seed after an update: %d %s", w.Code, w.Body)
	}
	if got := lineage(t, e, uid, src); got != "Q5,L0,Q4,Q3,Q2,L1<-L0,L2<-L1" {
		t.Errorf("forecasts after the update: %s", got)
	}

	// A source with nothing to show copies nothing, and that is fine.
	_, emptyEmail, _ := account(t, e, "user")
	if w := e.Do("POST", "/api/admin/users", map[string]any{"email": "blank@example.test", "name": "B", "seedFrom": emptyEmail}); w.Code != 201 ||
		!strings.Contains(w.Body.String(), `"seeded":{"forecasts":0,"portfolios":0,"strategies":0,"arenaRuns":0}`) {
		t.Fatalf("empty source: %d %s", w.Code, w.Body)
	}
}

func TestSeedKeepsCanonicalLinkedLineage(t *testing.T) {
	e := setup(t, apptest.Options{})
	src, email, _ := account(t, e, "user")
	showcase(t, e, src)
	exec(t, e, `update forecasts set linked_market='{"marketId":"5170727","conditionId":"0x1111111111111111111111111111111111111111111111111111111111111111","marketSlug":"bitcoin-reaches","eventSlug":"bitcoin-october","question":"Will Bitcoin reach $150,000 in October?","description":"Use Binance 1m High in the complete ET calendar month.","resolutionSource":null,"endDate":"2099-11-01T04:00:00Z","asOf":"2026-10-05T00:00:00Z"}' where user_id=$1 and question in ('L0','L1')`, src)
	exec(t, e, `update forecasts set materials='[{"id":"D1","kind":"document","mediaType":"text/csv","name":"rates.csv","size":20,"status":"read","summary":"A rates table.","asOf":null,"findings":[],"excerpt":"","readAt":"2026-10-01T00:00:00Z"}]' where user_id=$1 and question = 'L0'`, src)
	w := e.Do("POST", "/api/admin/users", map[string]any{"email": "linked-copy@example.test", "name": "Copy", "seedFrom": email})
	if w.Code != 201 {
		t.Fatal(w.Body)
	}
	uid := apptest.JSON[seedResp](t, w).User.ID
	if n := scalar[int](t, e, `select count(*) from forecasts c join forecasts s on s.id=c.seeded_from where c.user_id=$1 and s.question in ('L0','L1') and c.linked_market=s.linked_market`, uid); n != 2 {
		t.Fatalf("linked snapshots lost from completed lineage: %d", n)
	}
	if n := scalar[int](t, e, `select count(*) from forecasts c join forecasts s on s.id=c.seeded_from where c.user_id=$1 and s.question = 'L0' and c.materials=s.materials and jsonb_array_length(c.materials) = 1`, uid); n != 1 {
		t.Fatalf("materials not copied: the copy's evidence cites D ids: %d", n) // round 9
	}
	if scalar[int](t, e, `select count(*) from chats where user_id=$1`, uid)+scalar[int](t, e, `select count(*) from trades where user_id=$1`, uid) != 0 {
		t.Fatal("private data copied")
	}
}

// A source with no winner still gives the account one portfolio and one
// strategy: the best by return minus BTC's (with a backtest), and the best
// Sharpe among strategies with 3+ trades; a published strategy's publish date
// and stars stay behind.
func TestSeedFallback(t *testing.T) {
	e := setup(t, apptest.Options{})
	src, srcEmail, _ := account(t, e, "user")
	exec(t, e, `insert into ai_portfolios (user_id, name, thesis, prompt, holdings, backtest)
		select $1, n, 't', 'p', '[]', b::jsonb from (values ('Trailing', '{"returnPct":5,"benchmarkReturnPct":10}'),
			('Worse', '{"returnPct":-3,"benchmarkReturnPct":10}'), ('Unknown', '{}')) v(n, b)`, src)
	exec(t, e, `insert into strategies (user_id, name, description, prompt, spec, metrics, equity, benchmark, trades, published_at)
		select $1, n, '', '', '{}', m::jsonb, '[]', '[]', '[]', now() from (values
			('Thin', '{"trades":2,"sharpe":3,"totalReturnPct":5,"buyHoldReturnPct":10}'),
			('Meh', '{"trades":5,"sharpe":0.3,"totalReturnPct":2,"buyHoldReturnPct":10}'),
			('Negative', '{"trades":8,"sharpe":-0.2,"totalReturnPct":-4,"buyHoldReturnPct":10}'), ('Bare', '{}')) v(n, m)`, src)
	exec(t, e, "insert into strategy_stars (user_id, strategy_id) select $1, id from strategies where user_id = $1", src)

	w := e.Do("POST", "/api/admin/users", map[string]any{"email": "fallback@example.test", "name": "F", "seedFrom": srcEmail})
	res := apptest.JSON[seedResp](t, w)
	if w.Code != 201 || res.Seeded == nil || *res.Seeded != (Seeded{Portfolios: 1, Strategies: 1}) {
		t.Fatalf("seed: %d %s", w.Code, w.Body)
	}
	uid := res.User.ID
	if got := scalar[string](t, e, `select string_agg(name, ',') from ai_portfolios where user_id = $1`, uid); got != "Trailing" {
		t.Errorf("portfolios: %s", got)
	}
	if got := scalar[string](t, e, `select string_agg(name || ' ' || (published_at is null), ',') from strategies where user_id = $1`, uid); got != "Meh true" {
		t.Errorf("strategies: %s", got)
	}
	if n := scalar[int](t, e, `select count(*) from strategy_stars st join strategies s on s.id = st.strategy_id where s.user_id = $1`, uid); n != 0 {
		t.Errorf("stars copied: %d", n)
	}
}

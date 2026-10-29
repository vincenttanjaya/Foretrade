package main

import (
	"testing"
)

// Showcase accounts: an admin creates an investor account seeded from the
// demo user, and on first login its Forecasts, Portfolios, Strategies and
// Arena pages are populated with winners only, the forecast opening as a full
// report with its update history, a resolved forecast giving the account its
// own track record, and the bell quiet. /seed copies into an existing account,
// never a forecast twice.
func TestIntegrationAdminSeed(t *testing.T) {
	s := boot(t, options{demo: true})
	var demo string
	if err := s.db.QueryRow(t.Context(), "select id from users where email = 'demo@example.test'").Scan(&demo); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`with root as (insert into forecasts (user_id, shape, question, outcomes, resolution_criteria, resolution_date, category, status, result, price_rule, created_at, completed_at)
			values ($1, 'binary', 'Will BTC close above $120,000 on 2026-10-31?', '["Yes","No"]', 'Binance BTCUSDT daily close.', '2026-10-31', 'crypto', 'completed',
				'{"probabilities":[{"outcome":"Yes","p":0.25},{"outcome":"No","p":0.75}],"summary":"First read.","evidence":[],"drivers":[],"flags":[],"whatWouldChange":[],"confidence":"low"}',
				'{"symbol":"BTC","op":"above","threshold":120000,"mode":"on_date"}', now() - interval '4 days', now() - interval '4 days') returning id)
		insert into forecasts (user_id, shape, question, outcomes, resolution_criteria, resolution_date, category, status, result, price_rule, created_at, completed_at, parent_id)
		select $1, 'binary', 'Will BTC close above $120,000 on 2026-10-31?', '["Yes","No"]', 'Binance BTCUSDT daily close.', '2026-10-31', 'crypto', 'completed',
			'{"probabilities":[{"outcome":"Yes","p":0.31},{"outcome":"No","p":0.69}],"summary":"We lean No.","evidence":[],"drivers":[],"flags":[],"whatWouldChange":[],"confidence":"medium"}',
			'{"symbol":"BTC","op":"above","threshold":120000,"mode":"on_date"}', now() - interval '2 days', now() - interval '2 days', id from root`,
		`insert into forecasts (user_id, shape, question, outcomes, resolution_criteria, resolution_date, category, status, result, price_rule, created_at, completed_at,
			outcome, resolved_at, resolved_by)
		values ($1, 'binary', 'Will ETH close above $9,000 on 2026-09-30?', '["Yes","No"]', 'Binance ETHUSDT daily close.', '2026-09-30', 'crypto', 'completed',
			'{"probabilities":[{"outcome":"Yes","p":0.2},{"outcome":"No","p":0.8}],"summary":"We lean No.","evidence":[],"drivers":[],"flags":[],"whatWouldChange":[],"confidence":"medium"}',
			'{"symbol":"ETH","op":"above","threshold":9000,"mode":"on_date"}', now() - interval '20 days', now() - interval '20 days', 'No', now() - interval '3 days', 'auto')`,
		`insert into ai_portfolios (user_id, name, thesis, prompt, holdings, backtest)
		values ($1, 'Layer-1 core', 'Large caps.', 'L1s', '[{"symbol":"BTC","weight":0.6,"rationale":"Anchor"},{"symbol":"ETH","weight":0.4,"rationale":"Platform"}]',
			'{"returnPct":18,"benchmarkReturnPct":9}'),
			($1, 'Meme basket', 'Memes.', 'memes', '[{"symbol":"DOGE","weight":1,"rationale":"Memes"}]', '{"returnPct":-12,"benchmarkReturnPct":9}')`,
		`insert into strategies (user_id, name, description, prompt, spec, metrics, equity, benchmark, trades)
		values ($1, 'BTC EMA cross', 'Trend.', 'EMA cross', '{"name":"BTC EMA cross","symbol":"BTC","interval":"4h","bars":1000,"positionPct":100,"feePct":0.05}',
			'{"totalReturnPct":12,"buyHoldReturnPct":6,"sharpe":1.1,"maxDrawdownPct":8,"trades":14,"startT":1750000000,"endT":1770000000,"bars":1000}', '[{"t":1,"v":100}]', '[]', '[]'),
			($1, 'ETH dip buyer', 'Mean reversion.', 'Buy dips', '{"name":"ETH dip buyer","symbol":"ETH","interval":"1h","bars":1000,"positionPct":100,"feePct":0.05}',
			'{"totalReturnPct":-4,"buyHoldReturnPct":10,"sharpe":-0.3,"maxDrawdownPct":15,"trades":40,"startT":1750000000,"endT":1770000000,"bars":1000}', '[{"t":1,"v":100}]', '[]', '[]')`,
		// The older run's best agent beat BTC by 7.5 points (9.5% vs 2%); the newer one's by 1 (4% vs 3%): the older one is copied.
		`with r as (insert into arena_runs (user_id, name, status, cadence, start_at, steps, steps_done, universe, model, knowledge_cutoff, agents, benchmarks, credits, created_at, completed_at)
			values ($1, 'Momentum vs hold', 'completed', '1d', '2026-08-01', 2, 2, '{BTC,ETH}', 'm', '2026-06-30',
				'[{"name":"Momentum","personaId":"momentum","instruction":"Ride trends.","metrics":{"totalReturnPct":9.5}},{"name":"Value","personaId":"value","instruction":"Buy cheap.","metrics":null}]',
				'{"btc":[100000,101000,102000],"basket":[100000,100500,101000]}', 1, now() - interval '5 days', now()) returning id)
		insert into arena_steps (run_id, agent, step, equity, cash, positions, reasoning)
		select id, 0, s, 100000 + 500 * s, 100000, '{}', 'Held.' from r, generate_series(0, 1) s`,
		`insert into arena_runs (user_id, name, status, cadence, start_at, steps, steps_done, universe, model, knowledge_cutoff, agents, benchmarks, credits, completed_at)
		values ($1, 'Newer, closer to BTC', 'completed', '1d', '2026-09-01', 2, 2, '{BTC,ETH}', 'm', '2026-06-30',
			'[{"name":"Momentum","personaId":"momentum","instruction":"Ride trends.","metrics":{"totalReturnPct":4}}]',
			'{"btc":[100000,102000,103000],"basket":[100000,100500,101000]}', 1, now())`,
		`insert into chats (user_id, title) values ($1, 'Private')`,
	} {
		if _, err := s.db.Exec(t.Context(), q, demo); err != nil {
			t.Fatal(err)
		}
	}

	admin := s.signIn(t)
	if _, err := s.db.Exec(t.Context(), "update users set role = 'admin' where id = $1", admin.id); err != nil {
		t.Fatal(err)
	}
	asAdmin := func(method, path string, body any, want int) any {
		t.Helper()
		req := admin.request(method, path, body)
		req.Header.Set("Origin", adminOrigin)
		resp, b := admin.send(req)
		if resp.StatusCode != want {
			t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, b)
		}
		return decode(t, b)
	}
	if got := asAdmin("POST", "/api/admin/users", map[string]any{"email": "lp@example.test", "name": "LP", "seedFrom": "nobody@example.test"}, 400); str(t, got, "error.message") != "seedFrom: no account with that email." {
		t.Fatalf("unknown seedFrom: %v", got)
	}
	created := asAdmin("POST", "/api/admin/users", map[string]any{"email": "lp@example.test", "name": "LP", "seedFrom": "Demo@Example.Test",
		"password": testPassword}, 201)
	shape(t, created, "", "user seeded")
	shape(t, created, "seeded", "forecasts portfolios strategies arenaRuns")
	if num(t, created, "seeded.forecasts") != 2 || num(t, created, "seeded.portfolios") != 1 || num(t, created, "seeded.strategies") != 1 || num(t, created, "seeded.arenaRuns") != 1 {
		t.Fatalf("seeded: %v", created)
	}
	uid := str(t, created, "user.id")
	if audit := asAdmin("GET", "/api/admin/audit", nil, 200); str(t, audit, "entries.0.action") != "user.create" ||
		str(t, audit, "entries.0.detail.seededFrom") != "demo@example.test" || num(t, audit, "entries.0.detail.seeded.forecasts") != 2 {
		t.Fatalf("audit: %v", audit)
	}

	// First login: every showcase page has content, the forecast is a full report with its update history
	// (the lineage's root came along), and the bell lists none of the copies.
	lp := s.client(t)
	lp.call("POST", "/api/auth/login", map[string]string{"email": "lp@example.test", "password": testPassword}, 200)
	forecasts := list(t, lp.call("GET", "/api/forecasts", nil, 200), "forecasts")
	if len(forecasts) != 2 || str(t, forecasts[1], "resolvedOutcome") != "No" {
		t.Fatalf("forecasts: %v", forecasts)
	}
	if tr := lp.call("GET", "/api/forecasts/track-record", nil, 200); num(t, tr, "mine.n") != 1 || !near(num(t, tr, "mine.brier"), 0.04) { // (0.2 − 0)²
		t.Fatalf("the copy's own track record: %v", at(t, tr, "mine"))
	}
	f := lp.call("GET", "/api/forecasts/"+str(t, forecasts[0], "id"), nil, 200)
	if str(t, f, "forecast.status") != "completed" || num(t, f, "forecast.result.probabilities.0.p") != 0.31 || str(t, f, "forecast.result.summary") != "We lean No." ||
		str(t, f, "forecast.priceRule.symbol") != "BTC" || str(t, f, "forecast.createdAt") == "" || isNull(t, f, "forecast.parentId") {
		t.Fatalf("copied report: %v", f)
	}
	if h := list(t, f, "forecast.history"); len(h) != 2 || num(t, h[0], "p") != 0.25 || num(t, h[1], "p") != 0.31 || str(t, h[1], "id") != str(t, forecasts[0], "id") {
		t.Fatalf("copied history: %v", h)
	}
	if bell := lp.call("GET", "/api/notifications", nil, 200); len(list(t, bell, "items")) != 0 || num(t, bell, "unread") != 0 {
		t.Fatalf("bell after seeding: %v", bell)
	}
	portfolios := list(t, lp.call("GET", "/api/ai-portfolios", nil, 200), "portfolios")
	strategies := list(t, lp.call("GET", "/api/strategies?scope=mine", nil, 200), "strategies")
	if len(portfolios) != 1 || str(t, portfolios[0], "name") != "Layer-1 core" || len(strategies) != 1 || str(t, strategies[0], "name") != "BTC EMA cross" ||
		len(list(t, lp.call("GET", "/api/chats", nil, 200), "chats")) != 0 {
		t.Fatal("only the winning portfolio and strategy, and no chats")
	}
	runs := list(t, lp.call("GET", "/api/arena/runs", nil, 200), "runs")
	if len(runs) != 1 || str(t, runs[0], "status") != "completed" || str(t, runs[0], "name") != "Momentum vs hold" {
		t.Fatalf("arena runs: %v", runs)
	}
	if run := lp.call("GET", "/api/arena/runs/"+str(t, runs[0], "id"), nil, 200); num(t, run, "run.credits") != 0 || len(list(t, run, "run.agents")) != 2 {
		t.Fatalf("arena run: %v", run)
	}
	if s.count(t, "select count(*) from arena_steps st join arena_runs r on r.id = st.run_id where r.user_id = $1", uid) != 2 {
		t.Fatal("arena steps not copied")
	}

	// /seed copies again, but not a forecast already copied; copying an account into itself is refused.
	if got := asAdmin("POST", "/api/admin/users/"+uid+"/seed", map[string]string{"from": "demo@example.test"}, 200); num(t, got, "seeded.forecasts") != 0 ||
		num(t, got, "seeded.portfolios") != 1 || str(t, got, "user.id") != uid {
		t.Fatalf("seed: %v", got)
	}
	if got := asAdmin("POST", "/api/admin/users/"+uid+"/seed", map[string]string{"from": "lp@example.test"}, 400); str(t, got, "error.message") != "Pick a different account to copy from." {
		t.Fatalf("self seed: %v", got)
	}
	if s.count(t, "select count(*) from forecasts where user_id = $1", uid) != 3 || s.count(t, "select count(*) from forecasts where user_id = $1", demo) != 3 {
		t.Fatal("seed counts")
	}
}

package arena

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
	"github.com/vincenttanjaya/Foretrade/api/internal/research"
)

var ctx = context.Background()

// liveEnv mounts the arena with the live desk on and a scripted model.
func liveEnv(t *testing.T, reply func(llmtest.Request) llmtest.Reply) *apptest.Env {
	t.Helper()
	e, _ := newEnv(t, apptest.Options{Reply: reply}, nil)
	e.LiveArena = true
	return e
}

// orders is a place_orders tool call.
func orders(targets ...Target) llmtest.Reply {
	return llmtest.Reply{Tools: []llmtest.ToolUse{toolUse("o", "place_orders", map[string]any{"targets": targets})}}
}

func isLive(r llmtest.Request) bool { return strings.Contains(r.System, "live paper-trading desk") }

func TestLiveOrderToolTiming(t *testing.T) {
	live := (&turn{live: true, cadence: "4h", universe: []string{"BTC"}}).tools()[2].Description
	historical := (&turn{cadence: "4h", universe: []string{"BTC"}}).tools()[2].Description
	if !strings.Contains(live, "live price after this decision") || strings.Contains(live, "next bar") {
		t.Fatalf("live orders must describe their actual fill time: %s", live)
	}
	if !strings.Contains(historical, "next bar's open") {
		t.Fatalf("historical timing changed: %s", historical)
	}
}

// Check the actual toolResult on the next model request, not just the tool's
// description: the final reasoning must use the correct execution clock.
func TestDecisionOrderConfirmationSeenByModel(t *testing.T) {
	for _, tc := range []struct {
		name         string
		live         bool
		wantTiming   string
		wrongTiming  string
		wantRequests int
	}{{"live", true, "live price after this decision", "next open", 2},
		{"historical", false, "next open", "live price", 4}} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			seen := map[string]int{}
			_, srv := llmtest.New(t, func(r llmtest.Request) llmtest.Reply {
				mu.Lock()
				key := string(r.Body)
				seen[key]++
				n := seen[key]
				mu.Unlock()
				// Also prove that the historical shared decision caller retains
				// SDK retries when the live caller opts out.
				if !tc.live && n == 1 {
					return llmtest.Reply{Status: http.StatusInternalServerError}
				}
				if len(r.Messages) == 1 {
					return orders(Target{"BTC", 10})
				}
				if len(r.Messages) != 3 || len(r.Messages[2].Content) != 1 {
					t.Errorf("unexpected tool-result request: %s", r.Body)
					return llmtest.Reply{Text: "Missing confirmation."}
				}
				b, err := json.Marshal(r.Messages[2].Content[0]["toolResult"])
				var result struct{ Content []struct{ Text string } }
				if err != nil || json.Unmarshal(b, &result) != nil || len(result.Content) != 1 {
					t.Errorf("missing tool-result text: %s", b)
					return llmtest.Reply{Text: "Missing confirmation."}
				}
				return llmtest.Reply{Text: result.Content[0].Text}
			})
			cfg := srv.Config()
			cfg.Retryer = nil
			a := &agent{cash: startingCash, pos: map[string]*holding{}}
			s := &turn{live: tc.live, cadence: "4h", universe: []string{"BTC"}}
			s.value(a, map[string]float64{"BTC": 100000})
			a.decide(ctx, llm.New(cfg, llmtest.Model), s, "Decision", "", "Step 1")
			if a.err != nil || !strings.Contains(a.text, tc.wantTiming) || strings.Contains(a.text, tc.wrongTiming) {
				t.Fatalf("model received incorrect %s confirmation: %q err=%v", tc.name, a.text, a.err)
			}
			if len(a.targets) != 1 || a.targets[0].WeightPct != 10 || len(srv.Requests()) != tc.wantRequests {
				t.Fatalf("order/retry behavior changed: targets=%+v requests=%d", a.targets, len(srv.Requests()))
			}
		})
	}
}

// Pause the first quote fetch after metadata is read, then commit a tick and
// delivery. A response must contain either the old state or the new state.
func TestLiveBoardSnapshotAcrossTickAndCopyDelivery(t *testing.T) {
	e, agents := copyEnv(t)
	p := newCopy(t, e, agents[0], 1, 1000)
	quotes, err := e.Market.Assets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var enteredOnce, releaseOnce sync.Once
	upstream, _ := url.Parse(e.Binance.URL)
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		enteredOnce.Do(func() { close(entered); <-release })
		proxy.ServeHTTP(w, r)
	}))
	defer srv.Close()
	defer releaseOnce.Do(func() { close(release) })
	e.Market = testMarketClient(srv.URL)
	type result struct {
		b   LiveBoard
		err error
	}
	done := make(chan result, 1)
	go func() { b, err := liveBoard(ctx, e.Deps, e.User, time.Now()); done <- result{b, err} }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("board did not reach quote boundary")
	}
	now := time.Now().UTC()
	err = pgx.BeginFunc(ctx, e.DB, func(tx pgx.Tx) error {
		if _, err := lockCopyUser(ctx, tx, e.User); err != nil {
			return err
		}
		ledger, err := e.Paper.LockTx(ctx, tx, e.User, quotes)
		if err != nil {
			return err
		}
		trade, err := ledger.Order(ctx, paper.OrderRequest{Symbol: "BTC", Side: "buy", Notional: new(100.0), Source: "copy", RefID: &p.ID})
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "update copy_plans set positions=jsonb_build_object('BTC',$2::float8) where id=$1", p.ID, trade.Qty); err != nil {
			return err
		}
		bar := lastClosedBar(now)
		if _, err := tx.Exec(ctx, `insert into live_bars(bar_t,decided_at,marks) values($1,$2,'{"BTC":100000}');`, bar, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into live_steps(agent_id,step,bar_t,decided_at,equity,cash,positions) select id,0,$1,$2,100000,100000,'[]' from live_agents`, bar, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "update live_agents set steps=1,last_bar_t=$1", bar); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "update live_desk set last_step_at=$1 where id=1", now)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	releaseOnce.Do(func() { close(release) })
	var old LiveBoard
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatal(r.err)
		}
		old = r.b
	case <-time.After(5 * time.Second):
		t.Fatal("board did not finish")
	}
	if old.Desk.Status != "starting" || old.Desk.Steps != 0 || old.Desk.LastStepAt != nil || old.Agents[0].Steps != 0 {
		t.Errorf("mixed tick metadata: status=%s bars=%d lastStep=%v agentSteps=%d", old.Desk.Status, old.Desk.Steps, old.Desk.LastStepAt, old.Agents[0].Steps)
	}
	if plan := old.MyPlans[0]; len(plan.Positions) != 0 || plan.Trades != 0 || plan.Invested == nil || *plan.Invested != 0 {
		invested := -1.0
		if plan.Invested != nil {
			invested = *plan.Invested
		}
		t.Errorf("mixed copy state: positions=%v trades=%d invested=%v", plan.Positions, plan.Trades, invested)
	}
	fresh := board(t, e)
	if fresh.Desk.Status != "live" || fresh.Desk.Steps != 1 || fresh.Desk.LastStepAt == nil || fresh.Agents[0].Steps != 1 {
		t.Fatalf("fresh tick missing: %+v", fresh.Desk)
	}
	if plan := fresh.MyPlans[0]; len(plan.Positions) != 1 || plan.Trades != 1 || plan.Invested == nil || *plan.Invested != 100 {
		t.Fatalf("fresh copy missing: %+v", plan)
	}
}

func board(t *testing.T, e *apptest.Env) LiveBoard {
	t.Helper()
	w := e.Do("GET", "/api/arena/live", nil)
	if w.Code != 200 {
		t.Fatalf("board: %d %s", w.Code, w.Body)
	}
	return apptest.JSON[LiveBoard](t, w)
}

func agentNamed(t *testing.T, b LiveBoard, name string) LiveAgent {
	t.Helper()
	for _, a := range b.Agents {
		if a.Name == name {
			return a
		}
	}
	t.Fatalf("no agent %q in %+v", name, b.Agents)
	return LiveAgent{}
}

func liveCount(t *testing.T, e *apptest.Env, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := e.DB.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// One tick decides the latest closed bar for every agent once, at the live
// prices, leaving the halted pair out; the next bar gets one more decision
// each; the board and the step pages read it all back; the kill switch
// pauses the board and refuses copies.
// liveClock is a test's tick clock for the wall-clock time real: mid-bar
// (2 h into the 4h bar before real's), so a "+1 minute" tick never crosses a
// 4h boundary (run in the last minute before a boundary, as at
// 07:59:13 UTC, it landed on a new, really due bar and the tests failed).
func liveClock(real time.Time) time.Time {
	return real.UTC().Truncate(4 * time.Hour).Add(-2 * time.Hour)
}

// liveClocks are the wall clocks the clock-sensitive tests run at: now, and
// 30 s before the next 4h boundary, where the unanchored clock failed.
func liveClocks() []struct {
	name string
	real time.Time
} {
	return []struct {
		name string
		real time.Time
	}{{"now", time.Now()}, {"boundary-30s", time.Now().UTC().Truncate(4 * time.Hour).Add(4*time.Hour - 30*time.Second)}}
}

func TestLiveDesk(t *testing.T) {
	for _, c := range liveClocks() {
		t.Run(c.name, func(t *testing.T) { liveDeskAt(t, liveClock(c.real), c.name == "now") })
	}
	if c := liveClock(liveClocks()[1].real); lastClosedBar(c) != lastClosedBar(c.Add(time.Minute)) || lastClosedBar(c.Add(4*time.Hour)) != lastClosedBar(c)+liveBar {
		t.Fatal("the anchored clock stays inside its bar for a minute and moves one bar in 4 h")
	}
}

// liveDeskAt is TestLiveDesk at now; full runs the board, paging, outage and
// kill-switch checks after the ticks (the boundary case stops after them).
func liveDeskAt(t *testing.T, now time.Time, full bool) {
	e := liveEnv(t, func(r llmtest.Request) llmtest.Reply {
		switch {
		case !isLive(r):
			t.Errorf("unexpected model call: %.60q", r.System)
			return llmtest.Reply{Status: 400}
		case !strings.HasPrefix(r.System, "You are Trend Rider") || strings.Contains(r.Prompt, "BTC qty"):
			return llmtest.Reply{Text: "Holding cash; no edge."}
		case len(r.Messages) == 1: // the halted pair is refused, then BTC alone is staged
			return orders(Target{"BTC", 50}, Target{"TON", 10})
		case len(r.Messages) == 3:
			return orders(Target{"BTC", 50})
		}
		return llmtest.Reply{Text: "Bought BTC at half weight on the uptrend."}
	})
	e.Binance.Halt("TONUSDT", true)
	if err := LiveTick(ctx, e.Deps, now); err != nil {
		t.Fatal(err)
	}
	reqs := e.Bedrock.Requests()
	if len(reqs) != 5 || reqs[0].Effort != "low" || !strings.Contains(reqs[0].Prompt, "Live desk · ") || !strings.Contains(reqs[0].Prompt, "latest closed 4h bar opened") ||
		!strings.Contains(reqs[0].Prompt, "Portfolio at the live price: equity $100,000") || !strings.Contains(reqs[0].System, "cash plus any of: BTC, ETH, SOL") ||
		strings.Contains(reqs[0].System, "TON") {
		t.Fatalf("first step: %d calls, %q", len(reqs), reqs[0].Prompt)
	}
	for _, r := range reqs {
		if len(r.Messages) == 3 { // the tool result that refused TON
			if out, _ := json.Marshal(r.Messages[2].Content); !strings.Contains(string(out), "not in the tradable universe") {
				t.Fatalf("TON must be refused: %s", out)
			}
		}
	}
	if liveCount(t, e, "select count(*) from live_agents") != 3 || liveCount(t, e, "select count(*) from live_steps") != 3 ||
		liveCount(t, e, "select count(*) from live_bars") != 1 || liveCount(t, e, "select cardinality(universe) from live_desk") != 11 {
		t.Fatal("rows after the first tick")
	}
	// The same bar again: nothing to decide, no model call.
	if err := LiveTick(ctx, e.Deps, now.Add(time.Minute)); err != nil || len(e.Bedrock.Requests()) != 5 {
		t.Fatalf("second tick on the same bar: %v, %d calls", err, len(e.Bedrock.Requests()))
	}
	if err := LiveTick(ctx, e.Deps, now.Add(4*time.Hour)); err != nil || len(e.Bedrock.Requests()) != 8 || liveCount(t, e, "select count(*) from live_steps") != 6 {
		t.Fatalf("next bar: %v, %d calls", err, len(e.Bedrock.Requests()))
	}
	if !full {
		return
	}

	b := board(t, e)
	d := b.Desk
	if !d.Enabled || d.Status != "live" || d.StartedAt == nil || d.LastStepAt == nil || d.NextStepAt == nil || d.NextStepAt.Unix()%liveBar != 0 ||
		d.Cadence != "4h" || d.StartingCash != startingCash || len(d.Universe) != 11 || d.Steps != 2 || d.Method == "" {
		t.Fatalf("desk: %+v", d)
	}
	if len(b.Agents) != 3 || len(b.Benchmarks) != 2 || b.Benchmarks[0].ID != "btc" || len(b.Benchmarks[1].Equity) != 4 || len(b.MyPlans) != 0 {
		t.Fatalf("board: %d agents, %+v", len(b.Agents), b.Benchmarks)
	}
	tr := agentNamed(t, b, "Trend Rider")
	qty := 0.5
	if tr.PersonaID != "momentum" || tr.Status != "active" || tr.Rank != 3 || tr.Errors != 0 || tr.Steps != 2 || len(tr.Equity) != 4 || tr.Equity[0].V != startingCash ||
		len(tr.Positions) != 1 || tr.Positions[0].Symbol != "BTC" || math.Abs(tr.Positions[0].Qty-qty) > 1e-9 || tr.Positions[0].Mark != 100_000 ||
		math.Abs(tr.Cash-(startingCash-50_010-50_010*paper.FeeRate)) > 1e-6 || tr.Metrics.Fills != 1 || tr.LastReasoning != "Holding cash; no edge." ||
		tr.LastStepAt == nil || tr.Mandate == "" || tr.Followers != 0 || math.Abs(tr.Positions[0].WeightPct-50) > 0.1 {
		t.Fatalf("trend rider: %+v", tr)
	}
	if mr := agentNamed(t, b, "Mean Reverter"); mr.EquityNow != startingCash || len(mr.Positions) != 0 || mr.Rank > 2 || mr.Metrics.TotalReturnPct != 0 {
		t.Fatalf("mean reverter: %+v", mr)
	}

	w := e.Do("GET", "/api/arena/live/agents/"+tr.ID+"/steps", nil)
	steps := apptest.JSON[struct{ Steps []Step }](t, w).Steps
	if w.Code != 200 || len(steps) != 2 || steps[0].Step != 1 || steps[1].Step != 0 || steps[1].Targets == nil || len(steps[1].Fills) != 1 ||
		steps[1].Fills[0].Side != "buy" || steps[1].T != now.Unix() || len(steps[1].Tools) != 2 || steps[1].Tools[0].OK || !steps[1].Tools[1].OK ||
		steps[0].Targets != nil || steps[0].Reasoning != "Holding cash; no edge." {
		t.Fatalf("steps: %d %s", w.Code, w.Body)
	}
	if w := e.Do("GET", "/api/arena/live/agents/"+tr.ID+"/steps?limit=1&before=1", nil); len(apptest.JSON[struct{ Steps []Step }](t, w).Steps) != 1 {
		t.Fatalf("paged: %s", w.Body)
	}
	for _, q := range []string{"?limit=0", "?limit=51", "?before=-1", "?limit=x"} {
		if w := e.Do("GET", "/api/arena/live/agents/"+tr.ID+"/steps"+q, nil); w.Code != 400 {
			t.Errorf("steps%s: %d", q, w.Code)
		}
	}
	for _, p := range []string{"/api/arena/live/agents/nope/steps", "/api/arena/live/agents/6f1c2c1e-0000-4000-8000-000000000000/steps"} {
		if w := e.Do("GET", p, nil); w.Code != 404 {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
	for _, r := range [][2]string{{"GET", "/api/arena/live"}, {"GET", "/api/arena/live/agents/" + tr.ID + "/steps"},
		{"POST", "/api/arena/live/agents/" + tr.ID + "/copy"}, {"GET", "/api/copy-plans"}, {"POST", "/api/copy-plans/" + tr.ID + "/stop"}} {
		if w := e.DoAs(nil, r[0], r[1], "{}"); w.Code != 401 {
			t.Errorf("%s %s without a session: %d", r[0], r[1], w.Code)
		}
	}

	// Market data down: the board still answers, without the live point.
	e.Binance.Down("", true)
	time.Sleep(11 * time.Second) // past the assets cache
	if b := board(t, e); len(agentNamed(t, b, "Trend Rider").Equity) != 3 || agentNamed(t, b, "Trend Rider").EquityNow == 0 {
		t.Fatalf("board without market data: %+v", agentNamed(t, b, "Trend Rider"))
	}
	if err := LiveTick(ctx, e.Deps, now.Add(8*time.Hour)); err == nil || liveCount(t, e, "select count(*) from live_desk where last_error is not null") != 1 {
		t.Fatalf("tick without market data: %v", err)
	}
	e.Binance.Down("", false)

	// The kill switch: paused board, copies refused, everything else readable.
	e.LiveArena = false
	if b := board(t, e); b.Desk.Enabled || b.Desk.Status != "paused" || b.Desk.NextStepAt != nil || len(b.Agents) != 3 {
		t.Fatalf("paused: %+v", b.Desk)
	}
	if w := e.Do("POST", "/api/arena/live/agents/"+tr.ID+"/copy", map[string]any{"ratio": 0.5, "maxUsd": 1000}); w.Code != 409 ||
		!strings.Contains(w.Body.String(), "paused on this server") {
		t.Fatalf("copy while paused: %d %s", w.Code, w.Body)
	}
}

// A failing model holds with an error; three failures in a row bench the
// agent for a day (no calls, rows still written), then it decides again and
// is re-benched by the next failure.
func TestLiveBench(t *testing.T) {
	var failing atomic.Bool
	failing.Store(true)
	e := liveEnv(t, func(r llmtest.Request) llmtest.Reply {
		if failing.Load() {
			return llmtest.Reply{Status: http.StatusInternalServerError}
		}
		return llmtest.Reply{Text: "Holding."}
	})
	now := time.Now()
	for k := range 3 {
		if err := LiveTick(ctx, e.Deps, now.Add(time.Duration(k)*4*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(e.Bedrock.Requests()); n != 9 {
		t.Fatalf("%d calls", n)
	}
	b := board(t, e)
	for _, a := range b.Agents {
		if a.Status != "benched" || a.Errors != 3 || a.BenchedUntil == nil || a.Steps != 3 {
			t.Fatalf("agent after 3 failures: %+v", a)
		}
	}
	w := e.Do("GET", "/api/arena/live/agents/"+b.Agents[0].ID+"/steps", nil)
	if steps := apptest.JSON[struct{ Steps []Step }](t, w).Steps; len(steps) != 3 || steps[0].Error == nil || *steps[0].Error != "Model error; held." {
		t.Fatalf("failed steps: %s", w.Body)
	}
	// Benched: the next bar is held without a call, and still recorded.
	if err := LiveTick(ctx, e.Deps, now.Add(12*time.Hour)); err != nil || len(e.Bedrock.Requests()) != 9 || liveCount(t, e, "select count(*) from live_steps") != 12 {
		t.Fatalf("benched tick: %v, %d calls", err, len(e.Bedrock.Requests()))
	}
	// A day later it tries again, holds fine and is active with a clean slate.
	failing.Store(false)
	if err := LiveTick(ctx, e.Deps, now.Add(32*time.Hour)); err != nil || len(e.Bedrock.Requests()) != 12 {
		t.Fatalf("after the bench: %v, %d calls", err, len(e.Bedrock.Requests()))
	}
	if a := board(t, e).Agents[0]; a.Status != "active" || a.Errors != 0 {
		t.Fatalf("back: %+v", a)
	}
	// One more failure after the bench expired re-benches it at once.
	failing.Store(true)
	if _, err := e.DB.Exec(ctx, "update live_agents set fails = 3, benched_until = now() - interval '1 minute'"); err != nil {
		t.Fatal(err)
	}
	if err := LiveTick(ctx, e.Deps, now.Add(36*time.Hour)); err != nil || len(e.Bedrock.Requests()) != 15 {
		t.Fatalf("retry: %v, %d calls", err, len(e.Bedrock.Requests()))
	}
	if a := board(t, e).Agents[0]; a.Status != "benched" || a.Errors != 4 {
		t.Fatalf("re-benched: %+v", a)
	}
}

// get_news on the live desk reads the stored wire: the asset the query
// names, else macro; undated items are left out; no search call is made.
func TestLiveNews(t *testing.T) {
	e := liveEnv(t, nil)
	for _, row := range [][]any{
		{"https://w.test/btc", "Bitcoin ETF inflows resume", "Desk", "Inflows", time.Now().Add(-2 * time.Hour), []string{"BTC"}, "asset"},
		{"https://w.test/old", "Bitcoin last week", "Desk", "Old", time.Now().Add(-5 * 24 * time.Hour), []string{"BTC"}, "asset"},
		{"https://w.test/fed", "Fed holds rates", "Wire", "Macro", time.Now().Add(-3 * time.Hour), []string{}, "macro"},
		{"https://w.test/future", "Bitcoin ETF ruling tomorrow", "Desk", "Future", time.Now().Add(24 * time.Hour), []string{"BTC"}, "asset"},
		{"https://w.test/undated", "Undated take", "Blog", "No date", nil, []string{"BTC"}, "asset"},
	} {
		if _, err := e.DB.Exec(ctx, `insert into news_items (url, title, source, description, published_at, symbols, topic) values ($1, $2, $3, $4, $5, $6, $7)`, row...); err != nil {
			t.Fatal(err)
		}
	}
	news := liveNews(e.Research)
	titles := func(items []research.Result) []string {
		var out []string
		for _, it := range items {
			out = append(out, it.Title)
		}
		return out
	}
	btc, ok := news(ctx, "bitcoin ETF flows", 2)
	if !ok || strings.Join(titles(btc), ",") != "Bitcoin ETF inflows resume" {
		t.Fatalf("btc: %v %v", titles(btc), ok)
	}
	if week, _ := news(ctx, "BTC", 7); len(week) != 2 {
		t.Fatalf("7 days: %v", titles(week))
	}
	if macro, _ := news(ctx, "Fed rates", 2); strings.Join(titles(macro), ",") != "Fed holds rates" {
		t.Fatalf("macro: %v", titles(macro))
	}
	for q, want := range map[string]string{"bitcoin ETF flows": "BTC", "SOL momentum": "SOL", "Solana upgrade": "SOL", "Fed rates": "", "ETH, flows": "ETH", "gold": "", "macro risk": "", "risk-off sentiment": "", "SK hynix earnings": "", "TSMC capex": ""} {
		if got := namedAsset(q); got != want {
			t.Errorf("namedAsset(%q) = %q, want %q", q, got, want)
		}
	}
	if _, ok := liveNews(nil)(ctx, "bitcoin", 2); ok {
		t.Fatal("no research service: unavailable")
	}
	// The tool wraps the items and reports them, from a live turn.
	s := &turn{universe: []string{"BTC"}, cadence: liveCadence, iv: liveBar, live: true, news: news}
	out, err := s.getNews(ctx, json.RawMessage(`{"query":"bitcoin","days":1}`))
	if err != nil || !strings.HasPrefix(out, "<untrusted_web_content>") || !strings.Contains(out, "Desk · Bitcoin ETF inflows resume") || s.summary != "1 headlines" {
		t.Fatalf("tool: %q %v", out, err)
	}
	if tools := s.tools(); !strings.Contains(tools[1].Description, "market wire") {
		t.Fatalf("live news tool text: %s", tools[1].Description)
	}
}

func TestLiveClock(t *testing.T) {
	at := time.Date(2026, 10, 5, 13, 7, 0, 0, time.UTC)
	if bar := lastClosedBar(at); bar != time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC).Unix() {
		t.Fatalf("last closed bar: %v", time.Unix(bar, 0).UTC())
	}
	if next := nextBarClose(at); !next.Equal(time.Date(2026, 10, 5, 16, 0, 0, 0, time.UTC)) {
		t.Fatalf("next close: %v", next)
	}
	if bar := lastClosedBar(time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)); bar != time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC).Unix() {
		t.Fatalf("at a bar close the bar that just closed is next: %v", time.Unix(bar, 0).UTC())
	}
}

// A paid slot must survive a failed outcome commit. Restart cannot pay it twice.
func TestLiveClaimBeforeInferenceAndFailedSave(t *testing.T) {
	for _, c := range liveClocks() {
		t.Run(c.name, func(t *testing.T) { liveClaimBeforeInference(t, liveClock(c.real)) })
	}
}

func liveClaimBeforeInference(t *testing.T, now time.Time) {
	var e *apptest.Env
	e = liveEnv(t, func(r llmtest.Request) llmtest.Reply {
		if n := liveCount(t, e, "select count(*) from live_agents where last_bar_t is not null"); n != 3 {
			t.Errorf("inference started without durable slot claims: %d", n)
		}
		return llmtest.Reply{Text: "Hold."}
	})
	if _, _, err := loadLiveDesk(ctx, e.Deps, now); err != nil {
		t.Fatal(err)
	}
	if _, err := e.DB.Exec(ctx, `create function reject_live_save() returns trigger language plpgsql as $$ begin raise exception 'save rejected'; end $$;
		create trigger reject_save before insert on live_steps for each row execute function reject_live_save()`); err != nil {
		t.Fatal(err)
	}
	if err := LiveTick(ctx, e.Deps, now); err == nil {
		t.Fatal("save must fail")
	}
	calls := len(e.Bedrock.Requests())
	if calls != 3 {
		t.Fatalf("paid calls: %d", calls)
	}
	if _, err := e.DB.Exec(ctx, "drop trigger reject_save on live_steps"); err != nil {
		t.Fatal(err)
	}
	if err := LiveTick(ctx, e.Deps, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := len(e.Bedrock.Requests()); got != calls {
		t.Fatalf("restart repeated paid slot: %d → %d", calls, got)
	}
}

func TestLiveRefreshesMarksAfterInference(t *testing.T) {
	var e *apptest.Env
	e = liveEnv(t, func(r llmtest.Request) llmtest.Reply {
		if strings.HasPrefix(r.System, "You are Trend Rider") && len(r.Messages) == 1 {
			return orders(Target{"BTC", 50})
		}
		e.Binance.SetPrice("BTCUSDT", 200_000)
		return llmtest.Reply{Text: "Bought at refreshed price."}
	})
	if err := LiveTick(ctx, e.Deps, time.Now()); err != nil {
		t.Fatal(err)
	}
	a := agentNamed(t, board(t, e), "Trend Rider")
	if len(a.Positions) != 1 || math.Abs(a.Positions[0].Qty-0.25) > 1e-9 {
		t.Fatalf("filled using stale pre-inference mark: %+v", a.Positions)
	}
}

func TestLiveModelRequestsBounded(t *testing.T) {
	e := liveEnv(t, func(r llmtest.Request) llmtest.Reply {
		if len(r.Messages) == 7 {
			return llmtest.Reply{Text: "Hold."}
		}
		return orders(Target{"BTC", 10})
	})
	// Production uses the SDK retryer. Even with it configured, live requests
	// must stay within the durable claim's four-attempt allowance.
	cfg := e.Bedrock.Config()
	cfg.Retryer = nil
	e.LLM = llm.New(cfg, llmtest.Model)
	now := time.Now().UTC().Truncate(24 * time.Hour).Add(5 * time.Minute)
	for k := range 6 {
		if err := LiveTick(ctx, e.Deps, now.Add(time.Duration(k)*4*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(e.Bedrock.Requests()); got != 72 {
		t.Fatalf("requests/day = %d, want 72", got)
	}
	if err := LiveTick(ctx, e.Deps, now.Add(23*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := len(e.Bedrock.Requests()); got != 72 {
		t.Fatalf("same slot replayed: %d", got)
	}
}

func TestLiveModelRetriesStayWithinClaims(t *testing.T) {
	for _, tc := range []struct {
		name         string
		failMessages int
		wantRequests int
	}{{"first turn", 0, 3}, {"final turn", 7, 12}} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			seen := map[string]int{}
			e := liveEnv(t, func(r llmtest.Request) llmtest.Reply {
				mu.Lock()
				key := string(r.Body)
				seen[key]++
				n := seen[key]
				mu.Unlock()
				// A retry would succeed, exposing accidental SDK retries rather
				// than merely asserting that a permanently failing fake failed.
				if n == 1 && (tc.failMessages == 0 || len(r.Messages) == tc.failMessages) {
					return llmtest.Reply{Status: http.StatusInternalServerError}
				}
				if len(r.Messages) == 7 {
					return llmtest.Reply{Text: "Held."}
				}
				return orders(Target{"BTC", 10})
			})
			cfg := e.Bedrock.Config()
			cfg.Retryer = nil // SDK default, as in production, with fake URL/credentials.
			e.LLM = llm.New(cfg, llmtest.Model)
			now := time.Now().UTC()
			if err := LiveTick(ctx, e.Deps, now); err != nil {
				t.Fatal(err)
			}
			claims := liveCount(t, e, "select count(*) from live_attempts")
			requests := len(e.Bedrock.Requests())
			t.Logf("durable claims=%d ceiling=%d actual HTTP attempts=%d", claims, claims*4, requests)
			if claims != 3 || requests != tc.wantRequests {
				t.Fatalf("live SDK retry exceeded its claimed allowance: claims=%d actual=%d want=%d", claims, requests, tc.wantRequests)
			}
			for _, a := range board(t, e).Agents {
				if a.Errors != 1 || a.LastError == nil || len(a.Positions) != 0 {
					t.Errorf("failed inference must hold and consume the failure allowance: %+v", a)
				}
			}
			if err := LiveTick(ctx, e.Deps, now); err != nil {
				t.Fatal(err)
			}
			if got := len(e.Bedrock.Requests()); got != tc.wantRequests {
				t.Fatalf("failed same-bar claim retried: %d", got)
			}
		})
	}
}

func TestLiveBoardBoundedHistoryAndHealth(t *testing.T) {
	e := liveEnv(t, nil)
	now := time.Now().UTC()
	if _, _, err := loadLiveDesk(ctx, e.Deps, now.Add(-400*4*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.DB.Exec(ctx, `insert into live_bars(bar_t,decided_at,marks)
 select $1::bigint+n*14400,$2::timestamptz+n*interval '4 hours','{"BTC":100000}' from generate_series(0,399) n`, lastClosedBar(now)-400*liveBar, now.Add(-400*4*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.DB.Exec(ctx, `insert into live_steps(agent_id,step,bar_t,decided_at,equity,cash,positions,fills)
 select a.id,row_number()over(partition by a.id order by b.bar_t)-1,b.bar_t,b.decided_at,100000,100000,'[]','[]' from live_agents a cross join live_bars b;
 update live_agents set steps=400;
 update live_desk set last_step_at=now()-interval '5 hours'`); err != nil {
		t.Fatal(err)
	}
	b := board(t, e)
	if len(b.Agents[0].Equity) > 362 || len(b.Benchmarks[0].Equity) > 362 {
		t.Fatalf("unbounded chart: %d", len(b.Agents[0].Equity))
	}
	if b.Desk.Status != "stale" {
		t.Fatalf("stale desk looks active: %s", b.Desk.Status)
	}
	if w := e.Do("GET", "/api/arena/live", nil); strings.Contains(w.Body.String(), llmtest.Model) || strings.Contains(w.Body.String(), `"model"`) {
		t.Fatalf("public provider disclosure: %s", w.Body)
	}
	if _, err := e.DB.Exec(ctx, "update live_desk set last_error='Stored market quotes unavailable'"); err != nil {
		t.Fatal(err)
	}
	if b := board(t, e); b.Desk.Status != "outage" {
		t.Fatalf("outage hidden: %s", b.Desk.Status)
	}
}

func TestLiveOffCannotInfer(t *testing.T) {
	e := liveEnv(t, nil)
	e.LiveArena = false
	if err := LiveTick(ctx, e.Deps, time.Now()); err != nil {
		t.Fatal(err)
	}
	if n := liveCount(t, e, "select count(*) from live_desk"); n != 0 {
		t.Fatal("off initialized live desk")
	}
}

func TestLiveFailedSavesExhaustFailureAllowance(t *testing.T) {
	e := liveEnv(t, func(llmtest.Request) llmtest.Reply { return llmtest.Reply{Text: "Hold."} })
	if _, _, err := loadLiveDesk(ctx, e.Deps, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.DB.Exec(ctx, `create function reject_all_live_saves() returns trigger language plpgsql as $$ begin raise exception 'save rejected'; end $$;
 create trigger reject_all_saves before insert on live_steps for each row execute function reject_all_live_saves()`); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for k := range 3 {
		if err := LiveTick(ctx, e.Deps, now.Add(time.Duration(k)*4*time.Hour)); err == nil {
			t.Fatal("save unexpectedly passed")
		}
	}
	if n := len(e.Bedrock.Requests()); n != 9 {
		t.Fatalf("requests before bench: %d", n)
	}
	if err := LiveTick(ctx, e.Deps, now.Add(12*time.Hour)); err == nil {
		t.Fatal("save unexpectedly passed")
	}
	if n := len(e.Bedrock.Requests()); n != 9 {
		t.Fatalf("lost-save failure allowance ignored: %d", n)
	}
	if n := liveCount(t, e, "select count(*) from live_agents where fails=3 and benched_until is not null"); n != 3 {
		t.Fatalf("durable failed saves not benched: %d", n)
	}
}

func TestLiveUsesOnlyClosedBars(t *testing.T) {
	e := liveEnv(t, func(r llmtest.Request) llmtest.Reply {
		if len(r.Messages) == 1 {
			return llmtest.Reply{Tools: []llmtest.ToolUse{toolUse("prices", "get_prices", map[string]any{"symbol": "BTC", "interval": "4h", "bars": 5})}}
		}
		out, _ := json.Marshal(r.Messages[2].Content)
		if strings.Contains(string(out), "2026-10-01T00:00Z,") || !strings.Contains(string(out), "2026-09-30T20:00Z,") {
			t.Errorf("live tool included unclosed bar or lost newest close: %s", out)
		}
		return llmtest.Reply{Text: "Held using closed bars."}
	})
	if err := LiveTick(ctx, e.Deps, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if n := len(e.Bedrock.Requests()); n != 6 {
		t.Fatalf("price turns: %d", n)
	}
	for _, r := range e.Bedrock.Requests() {
		var v struct {
			InferenceConfig struct {
				MaxTokens int `json:"maxTokens"`
			} `json:"inferenceConfig"`
		}
		if err := json.Unmarshal(r.Body, &v); err != nil {
			t.Fatal(err)
		}
		if r.Effort != "low" || v.InferenceConfig.MaxTokens != 4096 {
			t.Fatalf("unbounded model config: effort=%s tokens=%d", r.Effort, v.InferenceConfig.MaxTokens)
		}
	}
}

func TestLiveInitialHaltedBitcoinBenchmarkKeepsCash(t *testing.T) {
	e := liveEnv(t, func(llmtest.Request) llmtest.Reply { return llmtest.Reply{Text: "Hold."} })
	e.Binance.Halt("BTCUSDT", true)
	if err := LiveTick(ctx, e.Deps, time.Now()); err != nil {
		t.Fatal(err)
	}
	b := board(t, e)
	if b.Benchmarks[0].Metrics.ExposurePct != 0 || b.Benchmarks[0].Metrics.TotalReturnPct != 0 || b.Benchmarks[0].Equity[len(b.Benchmarks[0].Equity)-1].V != 100000 {
		t.Fatalf("benchmark bought halted BTC: %+v", b.Benchmarks[0])
	}
}

func TestLiveOutageBeforeFirstStep(t *testing.T) {
	e := liveEnv(t, nil)
	if _, _, err := loadLiveDesk(ctx, e.Deps, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.DB.Exec(ctx, "update live_desk set last_error='No closed market bars available'"); err != nil {
		t.Fatal(err)
	}
	if b := board(t, e); b.Desk.Status != "outage" {
		t.Fatalf("first-step outage hidden: %s", b.Desk.Status)
	}
}

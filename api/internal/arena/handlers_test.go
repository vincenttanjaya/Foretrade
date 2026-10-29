package arena

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
)

// gate holds every model call of one step (1-based) until opened, and says
// when the first one arrived.
type gate struct {
	at            int
	ch, reached   chan struct{}
	once, arrived sync.Once
}

func newGate(at int) *gate {
	return &gate{at: at, ch: make(chan struct{}), reached: make(chan struct{})}
}
func (g *gate) open() { g.once.Do(func() { close(g.ch) }) }

func (g *gate) wait(r llmtest.Request) {
	if g != nil && stepOf(r) == g.at {
		g.arrived.Do(func() { close(g.reached) })
		<-g.ch
	}
}

// holdingModel is a model that writes one sentence and never trades.
func holdingModel(g *gate) func(llmtest.Request) llmtest.Reply {
	return func(r llmtest.Request) llmtest.Reply {
		g.wait(r)
		return llmtest.Reply{Text: "Holding cash; no edge."}
	}
}

// newEnv mounts the arena with a model; the cleanups open the gate, then wait
// for background runs, before apptest drops the schema.
func newEnv(t *testing.T, o apptest.Options, g *gate) (*apptest.Env, *handler) {
	t.Helper()
	var h *handler
	e := apptest.New(t, func(mux *http.ServeMux, d *app.Deps) { h = register(mux, d) }, o)
	t.Cleanup(h.runs.Wait)
	if g != nil {
		t.Cleanup(g.open)
	}
	return e, h
}

func body(agents ...string) map[string]any {
	var list []map[string]any
	for _, a := range agents {
		list = append(list, map[string]any{"personaId": a})
	}
	return map[string]any{"cadence": "1d", "start": "2026-09-01", "end": "2026-09-30", "agents": list}
}

type created struct {
	Run      Run
	Warnings []string
	Credits  struct{ Used int }
}

func create(t *testing.T, e *apptest.Env, c *http.Cookie, b map[string]any) created {
	t.Helper()
	w := e.DoAs(c, "POST", "/api/arena/runs", b)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	return apptest.JSON[created](t, w)
}

func getRun(t *testing.T, e *apptest.Env, id string) Run {
	t.Helper()
	w := e.Do("GET", "/api/arena/runs/"+id, nil)
	if w.Code != 200 {
		t.Fatalf("get run: %d %s", w.Code, w.Body)
	}
	return apptest.JSON[struct{ Run Run }](t, w).Run
}

func used(t *testing.T, e *apptest.Env, user string) int {
	t.Helper()
	c, err := e.Usage.Get(context.Background(), user)
	if err != nil {
		t.Fatal(err)
	}
	return c.Used
}

func stepRows(t *testing.T, e *apptest.Env, id string) int {
	t.Helper()
	var n int
	if err := e.DB.QueryRow(context.Background(), "select count(*) from arena_steps where run_id = $1", id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestOptionsAndValidation(t *testing.T) {
	e, _ := newEnv(t, apptest.Options{}, nil)
	id := "6f1c2c1e-0000-4000-8000-000000000000"
	for _, r := range [][2]string{{"GET", "/api/arena/options"}, {"POST", "/api/arena/runs"}, {"GET", "/api/arena/runs"},
		{"GET", "/api/arena/runs/" + id}, {"GET", "/api/arena/runs/" + id + "/steps"}, {"POST", "/api/arena/runs/" + id + "/cancel"},
		{"DELETE", "/api/arena/runs/" + id}} {
		if w := e.DoAs(nil, r[0], r[1], "{}"); w.Code != 401 {
			t.Errorf("%s %s without a session: %d", r[0], r[1], w.Code)
		}
	}

	w := e.Do("GET", "/api/arena/options", nil)
	opts := apptest.JSON[struct {
		Personas        []struct{ ID, Name, Description string }
		KnowledgeCutoff string
		LatestEnd       string
		Cadences        []struct {
			Cadence                  string
			MinDays, MaxDays         int
			DefaultStart, DefaultEnd string
		}
		MaxAgents, MaxSteps int
		CreditsPerAgentStep float64
	}](t, w)
	latest := todayUTC().AddDate(0, 0, -1).Format(time.DateOnly)
	if w.Code != 200 || len(opts.Personas) != 4 || opts.Personas[2].Name != "News Desk" || opts.KnowledgeCutoff != "2026-06-30" ||
		opts.LatestEnd != latest || len(opts.Cadences) != 2 || opts.Cadences[1].Cadence != "4h" || opts.Cadences[1].MaxDays != 14 ||
		opts.Cadences[0].DefaultEnd != latest || opts.MaxAgents != 4 || opts.MaxSteps != 90 || opts.CreditsPerAgentStep != 0.5 {
		t.Fatalf("options: %d %s", w.Code, w.Body)
	}

	tomorrow := todayUTC().Format(time.DateOnly)
	for _, b := range []any{`{"cadence":`, map[string]any{"cadence": "1d", "start": "2026-09-01", "end": tomorrow, "agents": []any{map[string]any{"personaId": "momentum"}}},
		map[string]any{"cadence": "1d", "start": "2026-06-01", "end": "2026-08-31", "agents": []any{map[string]any{"personaId": "momentum"}}}, // 92 days
		body()} {
		if w := e.Do("POST", "/api/arena/runs", b); w.Code != 400 || apptest.ErrorCode(t, w) != "bad_request" {
			t.Errorf("%v: %d %s", b, w.Code, w.Body)
		}
	}
	for _, path := range []string{"/api/arena/runs/" + id, "/api/arena/runs/nope", "/api/arena/runs/" + id + "/steps?agent=0"} {
		if w := e.Do("GET", path, nil); w.Code != 404 {
			t.Errorf("GET %s: %d", path, w.Code)
		}
	}
	for _, r := range [][2]string{{"POST", "/api/arena/runs/nope/cancel"}, {"POST", "/api/arena/runs/" + id + "/cancel"},
		{"DELETE", "/api/arena/runs/nope"}, {"DELETE", "/api/arena/runs/" + id}, {"GET", "/api/arena/runs/nope/steps?agent=0"}} {
		if w := e.Do(r[0], r[1], "{}"); w.Code != 404 {
			t.Errorf("%s %s: %d", r[0], r[1], w.Code)
		}
	}
	if w := e.Do("GET", "/api/arena/runs", nil); w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"runs":[]}` {
		t.Fatalf("empty list: %d %s", w.Code, w.Body)
	}
}

// Two agents hold through 30 daily steps: 30 credits, a 409 for a second
// run meanwhile, then 60 step rows, stored metrics and 31-point curves.
func TestRunToCompletion(t *testing.T) {
	g := newGate(2)
	e, h := newEnv(t, apptest.Options{Reply: holdingModel(g)}, g)
	c := create(t, e, e.Cookie, body("momentum", "contrarian"))
	r := c.Run
	if c.Credits.Used != 30 || r.Status != "running" || r.Steps != 30 || r.StepsDone != 0 || r.Start != "2026-09-01" || r.End != "2026-09-30" ||
		len(r.Universe) != 12 || r.Contaminated || len(c.Warnings) != 0 || r.Credits != 30 || r.Model != llmtest.Model ||
		r.Name != "Daily · Sep 1 → Sep 30, 2026" || len(r.Agents) != 2 || len(r.Agents[0].Equity) != 1 || len(r.Benchmarks[0].Equity) != 1 {
		t.Fatalf("created: %+v", c)
	}
	if w := e.Do("POST", "/api/arena/runs", body("macro")); w.Code != 409 || apptest.ErrorCode(t, w) != "conflict" || used(t, e, e.User) != 30 {
		t.Fatalf("a second run while one runs: %d %s", w.Code, w.Body)
	}
	<-g.reached
	if w := e.Do("DELETE", "/api/arena/runs/"+r.ID, "{}"); w.Code != 409 {
		t.Fatalf("delete while running: %d", w.Code)
	}
	if mid := getRun(t, e, r.ID); mid.Status != "running" || mid.StepsDone != 1 || len(mid.Agents[1].Equity) != 2 || len(mid.Benchmarks[1].Equity) != 2 {
		t.Fatalf("mid-run: %+v", mid)
	}
	g.open()
	h.runs.Wait()

	r = getRun(t, e, r.ID)
	if r.Status != "completed" || r.StepsDone != 30 || r.Credits != 30 || r.CompletedAt == nil || r.Error != nil || used(t, e, e.User) != 30 {
		t.Fatalf("completed: %+v", r)
	}
	btc := r.Benchmarks[0]
	for _, a := range r.Agents {
		if len(a.Equity) != 31 || a.Equity[30].V != startingCash || a.Equity[30].T != time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).Unix() ||
			a.Metrics.TotalReturnPct != 0 || a.Metrics.VsBtcPct != -btc.Metrics.TotalReturnPct || a.Status != "active" ||
			a.LastReasoning != "Holding cash; no edge." || a.Metrics.AnnualizedReturnPct != nil || a.Metrics.WinRatePct != nil { // 30 days, no sells
			t.Fatalf("agent: %+v", a)
		}
	}
	if r.Agents[0].Rank+r.Agents[1].Rank != 3 || len(btc.Equity) != 31 || btc.ID != "btc" || btc.Metrics.Fills != 1 || r.Benchmarks[1].Metrics.Fills != 12 ||
		btc.Metrics.ExposurePct != 100 || btc.Metrics.VsBtcPct != 0 {
		t.Fatalf("ranks / benchmarks: %+v", r.Benchmarks)
	}
	var stored []agentRow
	if err := e.DB.QueryRow(context.Background(), "select agents from arena_runs where id = $1", r.ID).Scan(&stored); err != nil ||
		stored[0].Metrics == nil || stored[1].Metrics.VsBtcPct != r.Agents[1].Metrics.VsBtcPct || stepRows(t, e, r.ID) != 60 {
		t.Fatalf("stored agents %+v %v", stored, err)
	}
	reqs := e.Bedrock.Requests()
	if len(reqs) != 60 || reqs[0].Effort != "low" || !strings.Contains(reqs[0].Prompt, "Step 1 of 30 · Simulated time: 2026-09-01 00:00 UTC") {
		t.Fatalf("%d model calls: %+v", len(reqs), reqs[0].Prompt)
	}

	w := e.Do("GET", "/api/arena/runs/"+r.ID+"/steps?agent=1&after=-1&limit=10", nil)
	steps := apptest.JSON[struct{ Steps []Step }](t, w).Steps
	if w.Code != 200 || len(steps) != 10 || steps[0].Step != 0 || steps[9].Step != 9 || steps[3].T != time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC).Unix() ||
		steps[0].Targets != nil || len(steps[0].Tools) != 0 || len(steps[0].Fills) != 0 || steps[0].Reasoning != "Holding cash; no edge." || steps[0].Equity != startingCash {
		t.Fatalf("steps: %d %s", w.Code, w.Body)
	}
	if w := e.Do("GET", "/api/arena/runs/"+r.ID+"/steps?agent=0&after=27", nil); len(apptest.JSON[struct{ Steps []Step }](t, w).Steps) != 2 {
		t.Fatalf("after 27: %s", w.Body)
	}
	if w := e.Do("GET", "/api/arena/runs/"+r.ID+"/steps?agent=0&after=29", nil); strings.TrimSpace(w.Body.String()) != `{"steps":[]}` {
		t.Fatalf("nothing after the last step: %s", w.Body)
	}
	for _, q := range []string{"", "?agent=2", "?agent=x", "?agent=0&after=-2", "?agent=0&limit=101", "?agent=0&limit=0"} {
		if w := e.Do("GET", "/api/arena/runs/"+r.ID+"/steps"+q, nil); w.Code != 400 {
			t.Errorf("steps%s: %d", q, w.Code)
		}
	}

	w = e.Do("GET", "/api/arena/runs", nil)
	list := apptest.JSON[struct{ Runs []Summary }](t, w).Runs
	if w.Code != 200 || len(list) != 1 || list[0].Leader == nil || list[0].Leader.TotalReturnPct != 0 || list[0].BtcReturnPct != btc.Metrics.TotalReturnPct ||
		list[0].End != "2026-09-30" || list[0].StepsDone != 30 {
		t.Fatalf("list: %s", w.Body)
	}
	if w := e.Do("POST", "/api/arena/runs/"+r.ID+"/cancel", "{}"); w.Code != 409 || apptest.ErrorCode(t, w) != "conflict" {
		t.Fatalf("cancel a finished run: %d", w.Code)
	}
	if w := e.DoAs(e.Session(storetest.User(t, e.DB)), "GET", "/api/arena/runs/"+r.ID, nil); w.Code != 404 {
		t.Fatalf("another user's run: %d", w.Code)
	}
	if w := e.Do("DELETE", "/api/arena/runs/"+r.ID, "{}"); w.Code != 204 || stepRows(t, e, r.ID) != 0 {
		t.Fatalf("delete: %d", w.Code)
	}
}

// A contaminated window warns; two runs process-wide is the cap.
func TestContaminationAndCapacity(t *testing.T) {
	g := newGate(1)
	e, _ := newEnv(t, apptest.Options{Reply: holdingModel(g)}, g)
	b := body("macro")
	b["start"], b["end"] = "2026-06-01", "2026-06-30"
	c := create(t, e, e.Cookie, b)
	if !c.Run.Contaminated || len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0], "cutoff 2026-06-30") || c.Run.KnowledgeCutoff != "2026-06-30" {
		t.Fatalf("contaminated: %+v", c)
	}
	create(t, e, e.Session(storetest.User(t, e.DB)), body("momentum"))
	if w := e.DoAs(e.Session(storetest.User(t, e.DB)), "POST", "/api/arena/runs", body("momentum")); w.Code != 429 || apptest.ErrorCode(t, w) != "rate_limited" {
		t.Fatalf("third run: %d %s", w.Code, w.Body)
	}
}

// Cancel flips the run, bills the step in flight (its model calls ran),
// refunds the rest and aborts that step, so the runner stops without writing
// it and frees the user's slot.
// Recover does the same for a run orphaned by a restart, refunding to the
// day the run was charged.
func TestCancelAndRecover(t *testing.T) {
	g := newGate(3)
	e, h := newEnv(t, apptest.Options{Reply: holdingModel(g)}, g)
	id := create(t, e, e.Cookie, body("momentum")).Run.ID
	<-g.reached // steps 0 and 1 are saved; step 2 is deciding
	w := e.Do("POST", "/api/arena/runs/"+id+"/cancel", "{}")
	got := apptest.JSON[struct {
		Run     Run
		Credits struct{ Used int }
	}](t, w)
	if w.Code != 200 || got.Run.Status != "cancelled" || got.Run.StepsDone != 2 || got.Run.Credits != 2 || got.Credits.Used != 2 || got.Run.CompletedAt == nil {
		t.Fatalf("cancel: %d %s", w.Code, w.Body)
	}
	stopped := make(chan struct{})
	go func() { h.runs.Wait(); close(stopped) }()
	select {
	case <-stopped: // the gate is still shut: the cancel ended the model call
	case <-time.After(5 * time.Second):
		t.Fatal("cancel must abort the step in flight")
	}
	if r := getRun(t, e, id); r.Status != "cancelled" || r.StepsDone != 2 || stepRows(t, e, id) != 2 || used(t, e, e.User) != 2 || len(h.live) != 0 {
		t.Fatalf("after the runner stopped: %+v, %d rows", r, stepRows(t, e, id))
	}
	// The caps count runners, not running rows: one still stopping keeps its slot.
	h.live[e.User] = nil
	if w := e.Do("POST", "/api/arena/runs", body("momentum")); w.Code != 409 || used(t, e, e.User) != 2 {
		t.Fatalf("create while the last runner stops: %d %s", w.Code, w.Body)
	}
	h.release(e.User)
	// A poll that reads steps_done just before the saver commits the next step
	// scores the agents over the same steps as the baselines.
	if _, err := e.DB.Exec(context.Background(), "update arena_runs set steps_done = 1 where id = $1", id); err != nil {
		t.Fatal(err)
	}
	if r := getRun(t, e, id); len(r.Agents[0].Equity) != 2 || len(r.Benchmarks[0].Equity) != 2 {
		t.Fatalf("cut to stepsDone: %d agent points, %d btc points", len(r.Agents[0].Equity), len(r.Benchmarks[0].Equity))
	}

	g2 := newGate(2)
	e2, h2 := newEnv(t, apptest.Options{Reply: holdingModel(g2)}, g2)
	id = create(t, e2, e2.Cookie, body("momentum", "contrarian")).Run.ID
	<-g2.reached
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Charged yesterday, with credits spent today: the refund goes to yesterday.
	for _, q := range []string{"update arena_runs set created_at = created_at - interval '1 day' where id = $1",
		"update ai_usage set day = day - 1 where user_id = (select user_id from arena_runs where id = $1)"} {
		if _, err := e2.DB.Exec(ctx, q, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e2.Usage.Spend(ctx, e2.User, 5); err != nil {
		t.Fatal(err)
	}
	Recover(ctx, e2.Deps)
	r := getRun(t, e2, id)
	var yesterday int
	if err := e2.DB.QueryRow(ctx, "select credits from ai_usage where user_id = $1 and day = $2", e2.User, todayUTC().AddDate(0, 0, -1)).Scan(&yesterday); err != nil {
		t.Fatal(err)
	}
	if r.Status != "failed" || r.Error == nil || *r.Error != "Interrupted by a server restart." || r.Credits != 1 || yesterday != 1 || used(t, e2, e2.User) != 5 {
		t.Fatalf("recovered: %+v, %d used yesterday, %d today", r, yesterday, used(t, e2, e2.User))
	}
	g2.open()
	h2.runs.Wait()
	if stepRows(t, e2, id) != 2 || getRun(t, e2, id).Status != "failed" {
		t.Fatal("the orphaned runner must stop at its next save")
	}
}

// Failures before a run starts cost nothing; a run whose model always fails
// benches its agent and fails with a refund of the unrun steps.
func TestRunFailures(t *testing.T) {
	e, h := newEnv(t, apptest.Options{Credits: 20, Reply: func(llmtest.Request) llmtest.Reply { return llmtest.Reply{Status: 500} }}, nil)
	e.Binance.Down("ETHUSDT", true)
	if w := e.Do("POST", "/api/arena/runs", body("momentum")); w.Code != 502 || apptest.ErrorCode(t, w) != "upstream" || used(t, e, e.User) != 0 {
		t.Fatalf("binance down: %d %s", w.Code, w.Body)
	}
	if w := e.Do("POST", "/api/arena/runs", body("momentum", "macro")); w.Code != 429 || apptest.ErrorCode(t, w) != "ai_quota" {
		t.Fatalf("30 credits over a limit of 20, checked before Binance: %d %s", w.Code, w.Body)
	}
	e.Binance.Down("ETHUSDT", false)
	id := create(t, e, e.Cookie, body("momentum")).Run.ID
	h.runs.Wait()
	r := getRun(t, e, id)
	if r.Status != "failed" || *r.Error != "The AI model is unavailable right now." || r.StepsDone != 3 || r.Credits != 2 || used(t, e, e.User) != 2 ||
		r.Agents[0].Status != "benched" || r.Agents[0].Errors != 3 {
		t.Fatalf("failed run: %+v", r)
	}
	w := e.Do("GET", "/api/arena/runs", nil)
	if list := apptest.JSON[struct{ Runs []Summary }](t, w).Runs; len(list) != 1 || list[0].Leader != nil || list[0].Status != "failed" {
		t.Fatalf("list: %s", w.Body)
	}
}

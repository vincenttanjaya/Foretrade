package alerts

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
)

// gammaFake serves Gamma's /events?slug=<slug>: one open Yes/No market
// "Will <slug>?" per known slug at its Yes price, and counts the reads.
type gammaFake struct {
	mu   sync.Mutex
	yes  map[string]float64
	hits map[string]int
	down bool
	hang bool // each read waits until the caller gives up
}

func newGamma(t *testing.T, e *apptest.Env) *gammaFake {
	g := &gammaFake{yes: map[string]float64{}, hits: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		slug := r.URL.Query().Get("slug")
		g.hits[slug]++
		if g.hang {
			g.mu.Unlock()
			<-r.Context().Done()
			return
		}
		defer g.mu.Unlock()
		if g.down || r.URL.Path != "/events" {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		out := []map[string]any{}
		if yes, ok := g.yes[slug]; ok {
			prices, _ := json.Marshal([]string{fmt.Sprint(yes), fmt.Sprint(1 - yes)})
			out = append(out, map[string]any{"slug": slug, "markets": []map[string]any{{"question": "Will " + slug + "?", "outcomes": `["Yes", "No"]`,
				"outcomePrices": string(prices), "endDate": "2099-12-31T00:00:00Z", "active": true, "closed": false, "volumeNum": 1e6}}})
		}
		json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	e.PolymarketBase = srv.URL
	return g
}

func (g *gammaFake) set(slug string, yes float64) { g.mu.Lock(); g.yes[slug] = yes; g.mu.Unlock() }
func (g *gammaFake) reads() map[string]int {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := map[string]int{}
	for k, v := range g.hits {
		out[k] = v
	}
	clear(g.hits)
	return out
}

// oddsForecast stores user's completed binary forecast (desk Yes 19%) matched
// to the market "Will <slug>?" at a stored Yes price; slug "" stores no market.
func oddsForecast(t *testing.T, e *apptest.Env, user, slug string, yes float64) string {
	t.Helper()
	market := "null"
	if slug != "" {
		market = fmt.Sprintf(`{"source":"Polymarket","question":"Will %s?","url":"https://polymarket.com/event/%s","yesPrice":%v,"volumeUsd":1000000,
			"endDate":"2099-12-31T00:00:00Z","match":"exact","edgePp":0,"evYes":0,"evNo":0,"signal":"wait","thin":false,"asOf":"2026-10-01T00:00:00Z"}`, slug, slug, yes)
	}
	var id string
	if err := e.DB.QueryRow(ctx, `insert into forecasts (user_id, shape, question, outcomes, resolution_date, status, result, completed_at)
		values ($1, 'binary', 'Will it happen?', '["Yes","No"]', '2099-12-31', 'completed', $2::jsonb, now()) returning id::text`, user,
		`{"probabilities":[{"outcome":"Yes","p":0.19},{"outcome":"No","p":0.81}],"confidence":"medium","market":`+market+`}`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// at freezes the loop's clock at t; the test moves it with the returned func.
func at(t *testing.T, start time.Time) func(time.Duration) {
	now := start
	clock = func() time.Time { return now }
	t.Cleanup(func() { clock = time.Now })
	return func(d time.Duration) { now = now.Add(d) }
}

// POST validates an odds trigger against the caller's completed forecast and
// its market, re-priced live (the stored price when Gamma is down), then stores
// it with forecast_id, symbol "" and the market; the list carries the market.
func TestOddsCreate(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	g := newGamma(t, e)
	g.set("fed-cut", 0.3)
	fc := oddsForecast(t, e, e.User, "fed-cut", 0.2)
	none := oddsForecast(t, e, e.User, "", 0)
	other := oddsForecast(t, e, storetest.User(t, e.DB), "fed-cut", 0.2)
	archived := oddsForecast(t, e, e.User, "fed-cut", 0.2)
	e.DB.Exec(ctx, "update forecasts set archived_from_status = status, status = 'archived' where id = $1", archived)
	body := func(id, rest string) string { return `{"trigger":"odds","forecastId":"` + id + `",` + rest + `}` }

	for b, want := range map[string]string{
		body("not-a-uuid", `"op":"above","price":0.5`):                           "Unknown forecast.",
		body(other, `"op":"above","price":0.5`):                                  "Unknown forecast.",
		body(archived, `"op":"above","price":0.5`):                               "Unknown forecast.",
		body("00000000-0000-0000-0000-000000000000", `"op":"above","price":0.5`): "Unknown forecast.",
		body(none, `"op":"above","price":0.5`):                                   "This forecast has no matched Polymarket market.",
		body(fc, `"op":"above","price":0.5,"action":"buy","notional":100`):       "Odds alerts notify only.",
		body(fc, `"op":"near","price":0.5`):                                      "op must be above or below.",
		body(fc, `"op":"above","price":0`):                                       "Pick a level between 1% and 99%.",
		body(fc, `"op":"above","price":0.995`):                                   "Pick a level between 1% and 99%.",
		body(fc, `"op":"above","price":25`):                                      "Pick a level between 1% and 99%.",
		body(fc, `"op":"above","price":0.5,"stopLoss":0.4`):                      "A notify alert takes no notional, stopLoss or takeProfit.",
		body(fc, `"op":"above","price":0.25`):                                    "The market is already at 30%. Pick a level above it.", // live 30%, stored 20%
		body(fc, `"op":"below","price":0.305`):                                   "The market is already at 30%. Pick a level below it.",
		body(fc, `"op":"above","price":0.5,"proposalId":"x"`):                    "proposalId must be a UUID.",
	} {
		if code, msg := errMsg(t, e, "POST", "/api/alerts", b); code != 400 || msg != want {
			t.Errorf("%.90s: %d %q, want %q", b, code, msg, want)
		}
	}
	var stored float64
	if e.DB.QueryRow(ctx, `select (result->'market'->>'yesPrice')::float8 from forecasts where id = $1`, fc).Scan(&stored); stored != 0.3 {
		t.Fatalf("the live re-price is stored: %v", stored)
	}

	a := create(t, e, body(fc, `"op":"above","price":0.355,"note":" Market catching up "`))
	if a.Trigger != "odds" || a.Symbol != "" || *a.ForecastID != fc || *a.Op != "above" || *a.Price != 0.355 || a.Action != "notify" || a.Note != "Market catching up" ||
		a.Market == nil || a.Market.YesPrice != 0.3 || a.Market.Question != "Will fed-cut?" || a.Market.URL != "https://polymarket.com/event/fed-cut" || a.Market.Outcome != nil {
		t.Fatalf("created %+v %+v", a, a.Market)
	}
	if got := load(t, e, a.ID); *got.ForecastID != fc || got.Market != nil {
		t.Fatalf("stored %+v", got)
	}

	// Gamma down: the stored 30% decides.
	g.mu.Lock()
	g.down = true
	g.mu.Unlock()
	if code, msg := errMsg(t, e, "POST", "/api/alerts", body(fc, `"op":"below","price":0.31`)); code != 400 || msg != "The market is already at 30%. Pick a level below it." {
		t.Fatalf("stored fallback: %d %q", code, msg)
	}
	b := create(t, e, body(fc, `"op":"below","price":0.2`))

	list := apptest.JSON[struct{ Alerts []Alert }](t, e.Do("GET", "/api/alerts", nil)).Alerts
	if len(list) != 2 || list[0].ID != b.ID || list[1].Market == nil || list[1].Market.YesPrice != 0.3 || *list[1].ForecastID != fc {
		t.Fatalf("list %+v", list)
	}

	// The table refuses an odds trigger that would trade.
	_, err := e.DB.Exec(ctx, `insert into alerts (user_id, symbol, trigger, op, price, action, notional, forecast_id)
		values ($1, '', 'odds', 'above', 0.5, 'buy', 100, $2)`, e.User, fc)
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.ConstraintName != "alerts_trigger_shape" {
		t.Fatalf("odds + buy: %v", err)
	}
	// A price trigger elsewhere still validates against the market (fake SOL $200).
	if code, msg := errMsg(t, e, "POST", "/api/alerts", `{"symbol":"SOL","op":"below","price":205}`); code != 400 || msg != "SOL is already below $205.00 (now $200.00)." {
		t.Fatalf("price trigger: %d %q", code, msg)
	}
}

// The loop re-prices each forecast with active odds triggers at most once a
// minute and at most 20 per check, fires above and below once each at the
// live Yes price, and the bell says so.
func TestCheckOdds(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	g := newGamma(t, e)
	advance := at(t, time.Now())
	g.set("fed-cut", 0.2)
	fc := oddsForecast(t, e, e.User, "fed-cut", 0.2)
	up := create(t, e, `{"trigger":"odds","forecastId":"`+fc+`","op":"above","price":0.255}`)
	down := create(t, e, `{"trigger":"odds","forecastId":"`+fc+`","op":"below","price":0.15,"note":"Fade it"}`)
	g.reads()

	g.set("fed-cut", 0.3)
	if err := Check(ctx, e.Deps); err != nil {
		t.Fatal(err)
	}
	if a := load(t, e, up.ID); a.Status != "fired" || *a.FiredPrice != 0.3 || a.FiredAt == nil {
		t.Fatalf("above: %+v", a)
	}
	if load(t, e, down.ID).Status != "active" || g.reads()["fed-cut"] != 1 {
		t.Fatal("below fired early, or not one read")
	}

	// Within the minute: no read, nothing fires.
	g.set("fed-cut", 0.1)
	advance(59 * time.Second)
	if err := Check(ctx, e.Deps); err != nil || load(t, e, down.ID).Status != "active" || g.reads()["fed-cut"] != 0 {
		t.Fatalf("throttled: %v", err)
	}
	advance(time.Second)
	if err := Check(ctx, e.Deps); err != nil {
		t.Fatal(err)
	}
	if a := load(t, e, down.ID); a.Status != "fired" || *a.FiredPrice != 0.1 {
		t.Fatalf("below: %+v", a)
	}
	var asOf time.Time
	if e.DB.QueryRow(ctx, `select (result->'market'->>'asOf')::timestamptz from forecasts where id = $1`, fc).Scan(&asOf); time.Since(asOf) > time.Minute {
		t.Fatalf("the report's market was not refreshed: %v", asOf)
	}
	advance(time.Minute)
	if err := Check(ctx, e.Deps); err != nil || g.reads()["fed-cut"] != 1 { // the read that fired it; none since
		t.Fatalf("nothing active on it: %v", err)
	}

	// The bell: the market's question, the level, now vs the desk.
	type page struct{ Items []Notification }
	items := apptest.JSON[page](t, e.Do("GET", "/api/notifications", nil)).Items
	if len(items) != 3 || items[2].Kind != "forecast_completed" {
		t.Fatalf("bell %+v", items)
	}
	for i, w := range []Notification{
		{ID: "odds_alert:" + down.ID, Kind: "odds_alert", Title: `Polymarket moved: "Will fed-cut?" fell below 15%`, Body: "Now 10% · desk 19% · Fade it", Link: "/forecasts/" + fc},
		{ID: "odds_alert:" + up.ID, Kind: "odds_alert", Title: `Polymarket moved: "Will fed-cut?" rose above 25.5%`, Body: "Now 30% · desk 19%", Link: "/forecasts/" + fc},
	} {
		if n := items[i]; n.ID != w.ID || n.Kind != w.Kind || n.Title != w.Title || n.Body != w.Body || n.Link != w.Link || n.Symbol != nil {
			t.Errorf("item %d: %+v", i, n)
		}
	}

	// 21 forecasts with a trigger each (other users: the 20-alert cap is per user): 20 reads, then the 21st.
	var ids []string
	for i := range 21 {
		slug := fmt.Sprintf("m%02d", i)
		g.set(slug, 0.5)
		user := storetest.User(t, e.DB)
		id := oddsForecast(t, e, user, slug, 0.5)
		ids = append(ids, id)
		if _, err := e.DB.Exec(ctx, `insert into alerts (user_id, symbol, trigger, op, price, forecast_id) values ($1, '', 'odds', 'above', 0.9, $2)`, user, id); err != nil {
			t.Fatal(err)
		}
	}
	advance(time.Minute)
	if err := Check(ctx, e.Deps); err != nil || len(g.reads()) != 20 {
		t.Fatalf("first check: %v", err)
	}
	if err := Check(ctx, e.Deps); err != nil || len(g.reads()) != 1 {
		t.Fatalf("second check: %v", err)
	}
	if err := Check(ctx, e.Deps); err != nil || len(g.reads()) != 0 {
		t.Fatalf("third check: %v", err)
	}
	// Gamma down: skipped, nothing fires, the next minute tries again.
	g.mu.Lock()
	g.down = true
	g.mu.Unlock()
	advance(time.Minute)
	if err := Check(ctx, e.Deps); err != nil || len(g.reads()) != 20 {
		t.Fatalf("down: %v", err)
	}
	var active int
	if e.DB.QueryRow(ctx, `select count(*) from alerts where trigger = 'odds' and status = 'active'`).Scan(&active); active != 21 {
		t.Fatalf("active %d", active)
	}
}

// Past what 20 a check can re-price (80 a minute at one check per 15 s),
// the longest-waiting go first: the checks keep rotating through all of
// them, every forecast read in any five checks in a row, not the lowest ids
// every minute and the rest never.
func TestCheckOddsFair(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	g := newGamma(t, e)
	advance := at(t, time.Now())
	const n, checks = 100, 16 // three full rotations and then some
	for i := range n {
		slug := fmt.Sprintf("f%03d", i)
		g.set(slug, 0.5)
		user := storetest.User(t, e.DB)
		if _, err := e.DB.Exec(ctx, `insert into alerts (user_id, symbol, trigger, op, price, forecast_id) values ($1, '', 'odds', 'above', 0.9, $2)`,
			user, oddsForecast(t, e, user, slug, 0.5)); err != nil {
			t.Fatal(err)
		}
	}
	var reads []map[string]int
	for range checks {
		if err := Check(ctx, e.Deps); err != nil {
			t.Fatal(err)
		}
		reads = append(reads, g.reads())
		advance(15 * time.Second)
	}
	window := (n + maxOddsForecasts - 1) / maxOddsForecasts
	for i := 0; i+window <= checks; i++ {
		read := map[string]bool{}
		for _, r := range reads[i : i+window] {
			for slug := range r {
				read[slug] = true
			}
		}
		if len(read) != n {
			t.Fatalf("checks %d..%d re-priced %d of %d forecasts", i, i+window-1, len(read), n)
		}
	}
}

// A check that runs out of budget on a hanging Gamma leaves the forecasts it
// never asked where they were in the queue: the next check takes them first,
// so a slow Gamma does not cut off the same tail every round.
func TestCheckOddsBudget(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	g := newGamma(t, e)
	advance := at(t, time.Now())
	budget := oddsBudget
	t.Cleanup(func() { oddsBudget = budget })
	for i := range 30 {
		slug := fmt.Sprintf("b%02d", i)
		g.set(slug, 0.5)
		user := storetest.User(t, e.DB)
		if _, err := e.DB.Exec(ctx, `insert into alerts (user_id, symbol, trigger, op, price, forecast_id) values ($1, '', 'odds', 'above', 0.9, $2)`,
			user, oddsForecast(t, e, user, slug, 0.5)); err != nil {
			t.Fatal(err)
		}
	}
	check := func(hang bool) map[string]int {
		t.Helper()
		g.mu.Lock()
		g.hang = hang
		g.mu.Unlock()
		oddsBudget = budget // a full check must not run out under a loaded -race run
		if hang {
			oddsBudget = 300 * time.Millisecond
		}
		if err := Check(ctx, e.Deps); err != nil {
			t.Fatal(err)
		}
		return g.reads()
	}

	// Never re-priced: the first read takes the whole budget, the other 19 picked stay never re-priced.
	hung := check(true)
	if len(hung) != 1 {
		t.Fatalf("hanging: %v", hung)
	}
	advance(15 * time.Second)
	second := check(false)
	for slug := range hung {
		if len(second) != 20 || second[slug] != 0 { // without the restore: only the 10 never picked
			t.Fatalf("after the hang: %d read, %v", len(second), second)
		}
	}
	advance(15 * time.Second)
	if got := check(false); len(got) != 9 {
		t.Fatalf("the rest: %v", got)
	}

	// Re-priced before: the 19 picked but not asked keep their last re-price,
	// so they still come before the 9 re-priced 15 s after them.
	advance(45 * time.Second)
	if again := check(true); !maps.Equal(again, hung) { // the longest-waiting one again
		t.Fatalf("hanging again: %v, first %v", again, hung)
	}
	advance(15 * time.Second)
	if got := check(false); !maps.Equal(got, second) {
		t.Fatalf("after the second hang: %v, want %v", got, second)
	}
}

// Schedule runs older than 30 days are pruned, at most once an hour.
func TestPruneRuns(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	// prune runs at most once an hour per process: start from no prune, and put back the process's own
	// afterwards (gap round G24: a second run under -count=2 found the first run's prune and skipped its own).
	oddsMu.Lock()
	saved := pruned
	pruned = time.Time{}
	oddsMu.Unlock()
	t.Cleanup(func() {
		oddsMu.Lock()
		pruned = saved
		oddsMu.Unlock()
	})
	advance := at(t, time.Now().Add(1000*time.Hour)) // past any prune an earlier test ran
	s := create(t, e, `{"symbol":"BTC","trigger":"schedule","every":"day","at":"08:00"}`)
	run := func(days int) {
		t.Helper()
		if _, err := e.DB.Exec(ctx, `insert into alert_runs (alert_id, user_id, status, body, ran_at) values ($1, $2, 'ok', 'b', now() - make_interval(days => $3))`,
			s.ID, e.User, days); err != nil {
			t.Fatal(err)
		}
	}
	runs := func() (n int) {
		e.DB.QueryRow(ctx, `select count(*) from alert_runs where alert_id = $1`, s.ID).Scan(&n)
		return n
	}
	run(31)
	run(29)
	if err := Check(ctx, e.Deps); err != nil || runs() != 1 {
		t.Fatalf("pruned: %v, %d runs", err, runs())
	}
	run(40)
	advance(59 * time.Minute)
	if err := Check(ctx, e.Deps); err != nil || runs() != 2 {
		t.Fatalf("within the hour: %v, %d runs", err, runs())
	}
	advance(time.Minute)
	if err := Check(ctx, e.Deps); err != nil || runs() != 1 {
		t.Fatalf("an hour later: %v, %d runs", err, runs())
	}
}

func TestPct(t *testing.T) {
	for p, want := range map[float64]string{0.25: "25%", 0.265: "26.5%", 0.19: "19%", 0.1934: "19.3%", 0.01: "1%", 0.995: "99.5%"} {
		if got := Pct(p); got != want {
			t.Errorf("Pct(%v) = %q, want %q", p, got, want)
		}
	}
}

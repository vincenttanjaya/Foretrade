package strategy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
)

// saved inserts a strategy owned by uid with the design example's spec (BTC 4h
// EMA cross) and made-up metrics, so a copy's fresh backtest is told apart.
func saved(t *testing.T, e *apptest.Env, uid, name string, trades int, sharpe float64, days int) string {
	t.Helper()
	var g generated
	json.Unmarshal([]byte(designExamples[0].reply), &g)
	g.Spec.Name = name
	metrics := fmt.Sprintf(`{"totalReturnPct":7,"buyHoldReturnPct":3,"sharpe":%v,"maxDrawdownPct":5,"trades":%d,"startT":1700000000,"endT":%d,"bars":1000}`,
		sharpe, trades, 1700000000+days*86400)
	var id string
	if err := e.DB.QueryRow(context.Background(), `insert into strategies (user_id, name, description, prompt, spec, metrics, equity, benchmark, trades, explanation)
		values ($1, $2, 'Trend.', 'EMA cross', $3, $4::jsonb, '[{"t":1,"v":100}]', '[]', '[]', 'Old read.') returning id`, uid, name, g.Spec, metrics).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

type got struct {
	Strategy Strategy
}

func communityOf(t *testing.T, e *apptest.Env, c *http.Cookie) []Summary {
	t.Helper()
	w := e.DoAs(c, "GET", "/api/strategies?scope=community", nil)
	if w.Code != 200 {
		t.Fatalf("community: %d %s", w.Code, w.Body)
	}
	return apptest.JSON[struct{ Strategies []Summary }](t, w).Strategies
}

func names(l []Summary) (out []string) {
	for _, s := range l {
		out = append(out, s.Name)
	}
	return out
}

// A publishes, B stars and copies; the gate, own-only publish, published-only
// star, the 404s for someone else's unpublished strategy, unpublish and
// delete keeping copies. No model call (apptest fails any) and no credits.
func TestCommunityPublishStarCopy(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	ctx := context.Background()
	b := storetest.User(t, e.DB)
	bc := e.Session(b)
	e.DB.Exec(ctx, "update users set name = 'Ann' where id = $1", e.User)
	e.DB.Exec(ctx, "update users set name = 'Ben' where id = $1", b)

	thin := saved(t, e, e.User, "Thin", 2, 3, 400)
	mine := saved(t, e, e.User, "Trend", 12, 1.2, 400)
	labID := nameID("BTC EMA Trend Rider")
	publish := func(c *http.Cookie, id string, on bool) *http.Response {
		return e.DoAs(c, "POST", "/api/strategies/"+id+"/publish", map[string]bool{"published": on}).Result()
	}

	// Fewer than 3 trades: 409. Someone else's, a Labs one or an unknown id: 404. A bad body: 400.
	if w := e.Do("POST", "/api/strategies/"+thin+"/publish", map[string]bool{"published": true}); w.Code != 409 ||
		apptest.ErrorCode(t, w) != "conflict" || !strings.Contains(w.Body.String(), "Backtest at least 3 trades before publishing.") {
		t.Fatalf("thin publish: %d %s", w.Code, w.Body)
	}
	for _, r := range []struct {
		c  *http.Cookie
		id string
	}{{bc, mine}, {e.Cookie, labID}, {e.Cookie, "nope"}} {
		if code := publish(r.c, r.id, true).StatusCode; code != 404 {
			t.Errorf("publish %s: %d", r.id, code)
		}
	}
	if w := e.Do("POST", "/api/strategies/"+mine+"/publish", `{}`); w.Code != 400 {
		t.Fatalf("no published field: %d", w.Code)
	}

	// Unpublished: B cannot read, star or copy it; nothing is listed.
	for _, r := range [][2]string{{"GET", ""}, {"POST", "/star"}, {"POST", "/copy"}} {
		if w := e.DoAs(bc, r[0], "/api/strategies/"+mine+r[1], map[string]bool{"starred": true}); w.Code != 404 {
			t.Errorf("%s %s unpublished: %d", r[0], r[1], w.Code)
		}
	}
	if w := e.Do("POST", "/api/strategies/"+mine+"/star", map[string]bool{"starred": true}); w.Code != 404 {
		t.Errorf("starring an own unpublished strategy: %d", w.Code)
	}
	if l := communityOf(t, e, bc); len(l) != 0 {
		t.Fatalf("community before publishing: %v", names(l))
	}

	w := e.Do("POST", "/api/strategies/"+mine+"/publish", map[string]bool{"published": true})
	s := apptest.JSON[got](t, w).Strategy
	if w.Code != 200 || s.Community == nil || !s.Community.Published || s.Community.PublishedAt == nil || !s.Community.Mine || s.Community.Owner != "Ann" {
		t.Fatalf("publish: %d %s", w.Code, w.Body)
	}

	// B sees it read-only, lists it, stars it twice (idempotent), unstars twice.
	w = e.DoAs(bc, "GET", "/api/strategies/"+mine, nil)
	if s := apptest.JSON[got](t, w).Strategy; w.Code != 200 || s.Community.Mine || s.Community.Owner != "Ann" || s.Author != "Ann" {
		t.Fatalf("B reads A's published strategy: %d %s", w.Code, w.Body)
	}
	if l := communityOf(t, e, bc); len(l) != 1 || l[0].ID != mine || l[0].Community == nil || l[0].Community.Mine {
		t.Fatalf("community: %+v", l)
	}
	for i, want := range []struct {
		on    bool
		stars int
	}{{true, 1}, {true, 1}, {false, 0}, {false, 0}, {true, 1}} {
		w := e.DoAs(bc, "POST", "/api/strategies/"+mine+"/star", map[string]bool{"starred": want.on})
		res := apptest.JSON[struct {
			Stars   int
			Starred bool
		}](t, w)
		if w.Code != 200 || res.Stars != want.stars || res.Starred != want.on {
			t.Fatalf("star %d: %d %s", i, w.Code, w.Body)
		}
	}
	if w := e.Do("POST", "/api/strategies/"+mine+"/star", map[string]bool{"starred": true}); w.Code != 200 ||
		!strings.Contains(w.Body.String(), `{"stars":2,"starred":true}`) { // own published: allowed
		t.Fatalf("own star: %d %s", w.Code, w.Body)
	}

	// B copies it: a fresh backtest saved for B, copied_from set, no credits.
	w = e.DoAs(bc, "POST", "/api/strategies/"+mine+"/copy", nil)
	cp := apptest.JSON[got](t, w).Strategy
	fresh := apptest.JSON[Result](t, e.Do("POST", "/api/strategies/backtest", map[string]any{"spec": cp.Spec}))
	if w.Code != 201 || cp.ID == mine || cp.Name != "Trend" || cp.Prompt != "" || cp.Description != "Trend." || cp.Explanation != "Old read." ||
		cp.Metrics.Trades != fresh.Metrics.Trades || cp.Metrics.Sharpe != fresh.Metrics.Sharpe || cp.Metrics.Sharpe == 1.2 || len(cp.Equity) < 2 ||
		cp.Community == nil || !cp.Community.Mine || cp.Community.Owner != "Ben" || cp.Community.Published ||
		cp.Community.CopiedFrom == nil || *cp.Community.CopiedFrom != (Source{mine, "Trend", "Ann"}) {
		t.Fatalf("copy: %d %s", w.Code, w.Body)
	}
	var owner, from string
	e.DB.QueryRow(ctx, "select user_id, copied_from from strategies where id = $1", cp.ID).Scan(&owner, &from)
	if owner != b || from != mine {
		t.Fatalf("copy row: owner %s, copied_from %s", owner, from)
	}
	// B deploys the copy: A's strategy counts 1 copy and 1 deploy (one user, however many autopilots).
	for range 2 {
		e.DB.Exec(ctx, `insert into autopilots (user_id, strategy_id, name, spec, allocation_usd, max_drawdown_pct, status, peak_equity)
			values ($1, $2, 'x', '{}', 1000, 10, 'stopped', 1000)`, b, cp.ID)
	}
	c := apptest.JSON[got](t, e.Do("GET", "/api/strategies/"+mine, nil)).Strategy.Community
	if c.Copies != 1 || c.Deploys != 1 || c.Stars != 2 || !c.Starred {
		t.Fatalf("A's counts: %+v", c)
	}

	// Labs and own copies work too; a Labs copy has no copied_from and reads as copied from Foretrade Labs.
	w = e.DoAs(bc, "POST", "/api/strategies/"+labID+"/copy", nil)
	lab := apptest.JSON[got](t, w).Strategy
	if w.Code != 201 || lab.Name != "BTC EMA Trend Rider" || lab.Metrics.Trades == 0 || lab.Explanation == "" ||
		lab.Community.CopiedFrom == nil || *lab.Community.CopiedFrom != (Source{labID, "BTC EMA Trend Rider", labsAuthor}) {
		t.Fatalf("labs copy: %d %s", w.Code, w.Body)
	}
	var labFrom *string
	e.DB.QueryRow(ctx, "select copied_from from strategies where id = $1", lab.ID).Scan(&labFrom)
	if labFrom != nil {
		t.Fatalf("labs copy copied_from %v", *labFrom)
	}
	if w := e.Do("POST", "/api/strategies/"+thin+"/copy", nil); w.Code != 201 || apptest.JSON[got](t, w).Strategy.Community.CopiedFrom.ID != thin {
		t.Fatalf("own copy: %d %s", w.Code, w.Body)
	}
	if creditsUsed(t, e) != 0 || len(e.Bedrock.Requests()) != 0 {
		t.Fatal("publish, star and copy are free and never call the model")
	}

	// A unpublishes: gone for B, stars and copies kept, B's copy stays but its source is no longer shown.
	if code := publish(e.Cookie, mine, false).StatusCode; code != 200 {
		t.Fatalf("unpublish: %d", code)
	}
	if l := communityOf(t, e, bc); len(l) != 0 {
		t.Fatalf("community after unpublish: %v", names(l))
	}
	if w := e.DoAs(bc, "GET", "/api/strategies/"+mine, nil); w.Code != 404 {
		t.Fatalf("B reads the unpublished one: %d", w.Code)
	}
	if w := e.DoAs(bc, "GET", "/api/strategies/"+cp.ID, nil); w.Code != 200 || apptest.JSON[got](t, w).Strategy.Community.CopiedFrom != nil {
		t.Fatalf("B's copy after unpublish: %d %s", w.Code, w.Body)
	}
	if c := apptest.JSON[got](t, e.Do("GET", "/api/strategies/"+mine, nil)).Strategy.Community; c.Published || c.Stars != 2 || c.Copies != 1 {
		t.Fatalf("after unpublish: %+v", c)
	}

	// A deletes it: B's copy survives with copied_from null.
	if w := e.Do("DELETE", "/api/strategies/"+mine, nil); w.Code != 204 {
		t.Fatalf("delete: %d", w.Code)
	}
	var left *string
	if err := e.DB.QueryRow(ctx, "select copied_from from strategies where id = $1", cp.ID).Scan(&left); err != nil || left != nil {
		t.Fatalf("copy after the source's delete: %v %v", left, err)
	}
}

// The board: full samples first, then stars, Sharpe; a disabled owner's
// strategies are hidden from it and from GET.
func TestCommunityOrdering(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	ctx := context.Background()
	other, gone := storetest.User(t, e.DB), storetest.User(t, e.DB)
	ids := map[string]string{
		"short-starred": saved(t, e, e.User, "short-starred", 5, 3, 400), // 5 trades: a short sample, however starred
		"low":           saved(t, e, e.User, "low", 20, 0.4, 400),
		"high":          saved(t, e, other, "high", 20, 1.5, 400),
		"starred":       saved(t, e, other, "starred", 20, 0.9, 400),
		"short-window":  saved(t, e, other, "short-window", 40, 2.5, 90),
		"disabled":      saved(t, e, gone, "disabled", 30, 4, 400),
	}
	e.DB.Exec(ctx, "update strategies set published_at = now()")
	for _, st := range [][2]string{{e.User, "short-starred"}, {other, "short-starred"}, {other, "starred"}} {
		e.DB.Exec(ctx, "insert into strategy_stars (user_id, strategy_id) values ($1, $2)", st[0], ids[st[1]])
	}
	e.DB.Exec(ctx, "update users set disabled_at = now() where id = $1", gone)

	want := []string{"starred", "high", "low", "short-starred", "short-window"}
	if got := names(communityOf(t, e, e.Cookie)); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("order %v, want %v", got, want)
	}
	if l := communityOf(t, e, e.Cookie); l[0].ShortSample || !l[3].ShortSample || l[3].Community.Stars != 2 || !l[3].Community.Starred ||
		l[0].Community.Stars != 1 || l[0].Community.Starred || !l[3].Community.Mine || l[0].Community.Mine {
		t.Fatalf("summaries: %+v", l)
	}
	if w := e.Do("GET", "/api/strategies/"+ids["disabled"], nil); w.Code != 404 {
		t.Fatalf("a disabled owner's published strategy: %d", w.Code)
	}
	if w := e.Do("GET", "/api/strategies?scope=top", nil); strings.Contains(w.Body.String(), `"community":{`) {
		t.Fatal("Labs carry community null")
	}
	if w := e.Do("GET", "/api/strategies?scope=mine", nil); !strings.Contains(w.Body.String(), `"community":{"published":true`) {
		t.Fatalf("mine carries community: %s", w.Body)
	}
}

// Another user's prompt never leaves the API, and free copies stop at maxSaved.
func TestCommunityPromptPrivateAndCopyCap(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	b := storetest.User(t, e.DB)
	bc := e.Session(b)
	id := saved(t, e, e.User, "Trend", 12, 1.2, 400)
	if w := e.Do("POST", "/api/strategies/"+id+"/publish", map[string]bool{"published": true}); w.Code != 200 {
		t.Fatalf("publish: %d %s", w.Code, w.Body)
	}
	if w := e.Do("GET", "/api/strategies/"+id, nil); apptest.JSON[got](t, w).Strategy.Prompt != "EMA cross" {
		t.Fatalf("owner read: %s", w.Body)
	}
	if w := e.DoAs(bc, "GET", "/api/strategies/"+id, nil); w.Code != 200 || apptest.JSON[got](t, w).Strategy.Prompt != "" {
		t.Fatalf("other user read: %d %s", w.Code, w.Body)
	}
	for i := 0; i < maxSaved; i++ {
		saved(t, e, b, fmt.Sprintf("Mine %d", i), 3, 0.5, 400)
	}
	if w := e.DoAs(bc, "POST", "/api/strategies/"+id+"/copy", nil); w.Code != 409 || !strings.Contains(w.Body.String(), "Delete one to copy another.") {
		t.Fatalf("copy over the cap: %d %s", w.Code, w.Body)
	}
}

// The community Sharpe correction covers every quote feed, the listed rows too.
func TestQuoteFeedSymbolsCoverListed(t *testing.T) {
	syms := quoteFeedSymbols()
	if len(syms) != len(market.QuoteFeeds)+len(market.Listed) || !slices.Contains(syms, "MEDIATEK") || !slices.Contains(syms, "6501.T") ||
		slices.Contains(syms, "BTC") || slices.Contains(syms, "SMSN") {
		t.Fatalf("%d symbols", len(syms))
	}
}

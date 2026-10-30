package research

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
)

// fakeBedrock answers every model call with reply; prompts returns those seen so far.
func fakeBedrock(t *testing.T, reply string) (ai *llm.Client, prompts func() []string) {
	ai, srv := llmtest.New(t, func(llmtest.Request) llmtest.Reply { return llmtest.Reply{Text: reply} })
	return ai, func() []string {
		var out []string
		for _, r := range srv.Requests() {
			out = append(out, r.Prompt)
		}
		return out
	}
}

// TestIngestAgainstDB runs a full ingestion against the test DB with fake
// Brave, crawl4ai and Bedrock servers, then reads it back through the API.
func TestIngestAgainstDB(t *testing.T) {
	pool := storetest.Schema(t) // exact row counts below: no other process may share these tables
	ctx := context.Background()
	clear := func() {
		if _, err := pool.Exec(ctx, "delete from news_items; delete from market_briefs"); err != nil {
			t.Fatal(err)
		}
	}

	now := time.Now().UTC()
	age := func(d time.Duration) string { return now.Add(-d).Format("2006-01-02T15:04:05") }
	if _, err := pool.Exec(ctx, `insert into news_items (url, title, source, published_at, fetched_at, symbols, content, crawled_at) values
		('https://old.example/published', 'Old', 'x', now() - interval '15 days', now() - interval '15 days', '{BTC}', null, null),
		('https://old.example/undated', 'Old undated', 'x', null, now() - interval '15 days', '{}', null, null),
		('https://cached.example/story', 'Cached Ethereum story', 'Cache Times', now() - interval '5 hours', now() - interval '3 hours',
		 '{ETH}', 'Cached article text that is long enough. ' || repeat('Analysts said demand stayed firm. ', 12), now() - interval '3 hours')`); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	queries := map[string]int{}
	countries := map[string]string{}
	for _, ws := range wireSearches(true) {
		countries[ws.query] = ws.country
	}
	brave := newFakeBrave(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		mu.Lock()
		queries[q.Get("q")]++
		mu.Unlock()
		if q.Get("freshness") != "pd" || q.Get("count") != "10" || r.URL.Path != "/news/search" {
			t.Errorf("ingest search %s %v", r.URL.Path, q)
		}
		if want, ok := countries[q.Get("q")]; !ok || q.Get("country") != want { // US macro and markets us; assets worldwide
			t.Errorf("query %q: country %q", q.Get("q"), q.Get("country"))
		}
		shared := fmt.Sprintf(`{"title":"Fed holds rates; bitcoin dips","url":"https://news.example/shared","description":"Markets wait","page_age":%q,"profile":{"name":"Wire"}}`, age(2*time.Hour))
		switch q.Get("q") {
		case "Bitcoin BTC crypto":
			fmt.Fprintf(w, `{"results":[{"title":"Bitcoin ETF inflows top $1bn","url":"https://news.example/btc-etf","description":"Ethereum funds also gained","page_age":%q,"profile":{"name":"Desk"}},%s]}`, age(time.Hour), shared)
		case "Ethereum ETH crypto":
			fmt.Fprintf(w, `{"results":[%s]}`, shared)
		case "Chainlink LINK crypto":
			fmt.Fprintf(w, `{"results":[{"title":"Chainlink CCIP 2.0 launches","url":"https://news.example/link","description":"Traders eye $SUI too; click the link","page_age":%q,"meta_url":{"hostname":"www.news.example"}}]}`, age(3*time.Hour))
		case "Federal Reserve interest rates":
			fmt.Fprintf(w, `{"results":[%s,{"title":"Powell says policy is well positioned","url":"https://news.example/powell","description":""},
				{"title":"Pakistan's inflation remains in double digits","url":"https://news.example/pk","description":"The central bank held"}]}`, shared) // not US: skipped
		case "US dollar treasury yields": // an off-topic title whose description passes the US-macro rule: skipped
			fmt.Fprint(w, `{"results":[{"title":"What It Means for You: Five Tips Before Payday","url":"https://life.example/payday","description":"The US dollar and your budget"}]}`)
		default:
			fmt.Fprint(w, `{"results":[]}`)
		}
	})

	var crawlMu sync.Mutex
	tasks := map[string]string{}
	crawled := map[string]int{}
	crawlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		crawlMu.Lock()
		defer crawlMu.Unlock()
		if r.URL.Path == "/crawl" {
			var body struct{ URLs []string }
			json.NewDecoder(r.Body).Decode(&body)
			id := fmt.Sprint(len(tasks) + 1)
			tasks[id] = body.URLs[0]
			crawled[body.URLs[0]]++
			fmt.Fprintf(w, `{"task_id":%q}`, id)
			return
		}
		u := tasks[strings.TrimPrefix(r.URL.Path, "/task/")]
		if strings.HasSuffix(u, "/link") {
			fmt.Fprint(w, `{"status":"failed","error":"maximum recursion depth exceeded"}`)
			return
		}
		fmt.Fprintf(w, `{"status":"completed","results":[{"markdown":"* [Home](/)\n# Story\n\nBody of %s with enough words. %s"}]}`, u,
			strings.Repeat("Analysts said demand stayed firm through the week while volumes rose. ", 6))
	}))
	defer crawlSrv.Close()

	ai, prompts := fakeBedrock(t, `{"headline":"Bitcoin ETF demand returns","summary":"Inflows lifted BTC while the Fed held.",
		"bullets":[{"text":"ETF inflows top $1bn","symbols":["BTC"],"sources":[1]},
		           {"text":"Chainlink ships CCIP 2.0","symbols":["LINK","NOPE"],"sources":[3]},
		           {"text":"The Fed held rates","symbols":[],"sources":[2,5,99]}],
		"sentiment":[{"symbol":"BTC","score":0.6,"label":"neutral","reason":"ETF inflows"},
		             {"symbol":"DOGE","score":0.2,"label":"bullish","reason":"no DOGE news"}]}`)

	s := New(Config{BraveAPIKey: "k", BraveBase: brave.srv.URL, CrawlBase: crawlSrv.URL, CrawlToken: "tok"}, pool, ai)
	s.brave.gap = 0
	s.crawl.pollDelay = time.Millisecond
	s.crawl.direct.lookupIP = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
	}
	s.crawl.direct.dialContext = func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("direct fetch blocked in test")
	}

	if b, err := s.LatestBrief(ctx); b != nil || err != nil {
		t.Fatalf("no brief yet: %+v %v", b, err)
	}
	if err := s.Ingest(ctx); err != nil {
		t.Fatal(err)
	}

	// 18 searches, one per core asset (PAXG as "gold price") and per macro
	// topic, and on this first run the 18 regional ones.
	if n := int(brave.calls.Load()); n != len(market.Core)+len(macroTopics)+len(regionalSearches) || n != 36 {
		t.Errorf("%d Brave calls", n)
	}
	for _, q := range []string{"Bitcoin BTC crypto", "Toncoin TON crypto", "gold price", "US dollar treasury yields"} {
		if queries[q] != 1 {
			t.Errorf("query %q ran %d times", q, queries[q])
		}
	}
	if queries["Aave AAVE crypto"] != 0 {
		t.Error("the wire searches core assets only")
	}

	type row struct {
		Symbols   []string
		Topic     string
		Published bool
		Content   *string
		Crawled   bool
		Regions   []string
	}
	rows := map[string]row{}
	rs, err := pool.Query(ctx, "select url, symbols, topic, published_at is not null, content, crawled_at is not null, regions from news_items")
	if err != nil {
		t.Fatal(err)
	}
	for rs.Next() {
		var u string
		var r row
		if err := rs.Scan(&u, &r.Symbols, &r.Topic, &r.Published, &r.Content, &r.Crawled, &r.Regions); err != nil {
			t.Fatal(err)
		}
		rows[u] = r
	}
	if rs.Err() != nil {
		t.Fatal(rs.Err())
	}
	if len(rows) != 5 {
		t.Fatalf("want 4 ingested + 1 cached rows (old ones deleted, the payday page skipped), got %v", rows)
	}
	check := func(u string, symbols []string, topic string, published, content, crawledAt bool) {
		t.Helper()
		r, ok := rows[u]
		if !ok || !slices.Equal(r.Symbols, symbols) || r.Topic != topic || r.Published != published ||
			(r.Content != nil) != content || r.Crawled != crawledAt {
			t.Errorf("%s: %+v (found %t)", u, r, ok)
		}
	}
	check("https://news.example/btc-etf", []string{"BTC", "ETH"}, "asset", true, true, true)
	check("https://news.example/shared", []string{"BTC"}, "asset", true, true, true)        // deduped; it names bitcoin only, so not ETH
	check("https://news.example/link", []string{"LINK", "SUI"}, "asset", true, false, true) // read failed: marked, not retried
	check("https://news.example/powell", []string{}, "macro", false, true, true)            // the US region's unread item
	check("https://cached.example/story", []string{"ETH"}, "asset", true, true, true)
	for u, want := range map[string][]string{"https://news.example/btc-etf": {"crypto"}, "https://news.example/shared": {"crypto", "us"}, // merged like symbols
		"https://news.example/link": {"crypto"}, "https://news.example/powell": {"us"}, "https://cached.example/story": {}} { // a row ingestion never labelled
		if got := rows[u].Regions; got == nil || !slices.Equal(got, want) {
			t.Errorf("%s: regions %#v, want %v", u, got, want)
		}
	}
	if c := rows["https://news.example/btc-etf"].Content; c == nil || !strings.HasPrefix(*c, "# Story\n\nBody of https://news.example/btc-etf") {
		t.Errorf("stored content %v", c)
	}

	// The brief saw the 5 recent headlines inside the untrusted wrapper.
	if p := prompts(); len(p) != 1 || !strings.Contains(p[0], "<untrusted_web_content>") ||
		!strings.Contains(p[0], "[1] Bitcoin ETF inflows top $1bn | news.example") { // Brave's "Desk" does not match the domain
		t.Fatalf("brief prompts %q", p)
	}
	ids := map[string]string{}
	news, err := s.News(ctx, NewsFilter{})
	if err != nil || len(news) != 5 {
		t.Fatalf("news %+v %v", news, err)
	}
	for _, it := range news {
		ids[it.URL] = it.ID
	}
	b, err := s.LatestBrief(ctx)
	if err != nil || b == nil {
		t.Fatalf("brief %+v %v", b, err)
	}
	if b.ID == "" || b.CreatedAt.IsZero() || b.Headline != "Bitcoin ETF demand returns" || b.ArticleCount != 5 || len(b.Bullets) != 3 {
		t.Errorf("brief %+v", b)
	}
	if got := b.Bullets[2].Sources; !slices.Equal(got, []string{ids["https://news.example/shared"], ids["https://news.example/powell"]}) {
		t.Errorf("bullet sources %v (ids %v)", got, ids)
	}
	if !slices.Equal(b.Bullets[1].Symbols, []string{"LINK"}) {
		t.Errorf("bullet symbols %v", b.Bullets[1].Symbols)
	}
	if want := []Sentiment{{Symbol: "BTC", Score: 0.6, Label: "bullish", Reason: "ETF inflows"}}; !slices.Equal(b.Sentiment, want) {
		t.Errorf("sentiment %+v", b.Sentiment)
	}

	// Stored-news reads.
	if h, err := s.Headlines(ctx, "eth", 48*time.Hour, 0); err != nil || len(h) != 2 || // not the bitcoin-only shared story
		h[0].URL != "https://cached.example/story" || h[1].URL != "https://news.example/btc-etf" { // the title naming ETH first
		t.Errorf("ETH headlines %+v %v", h, err)
	}
	if h, err := s.Headlines(ctx, "BTC", 90*time.Minute, 5); err != nil || len(h) != 1 {
		t.Errorf("since filter %+v %v", h, err)
	}
	if _, err := s.Headlines(ctx, "NOPE", 0, 5); err == nil {
		t.Error("unknown symbol must fail")
	}
	p, err := s.ReadPage(ctx, "https://cached.example/story", 6)
	if err != nil || p.Via != "cache" || p.Title != "Cached Ethereum story" || p.Text != "Cache…" {
		t.Errorf("cached read %+v %v", p, err)
	}

	// Handlers.
	mux := http.NewServeMux()
	s.Register(mux, func(h http.Handler) http.Handler { return h })
	get := func(path string, out any) {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), out) != nil {
			t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body)
		}
	}
	var list struct{ News []map[string]any }
	get("/api/news?symbol=btc", &list)
	if len(list.News) != 2 || list.News[0]["url"] != "https://news.example/btc-etf" || list.News[0]["hasContent"] != true {
		t.Errorf("symbol=btc %v", list.News)
	}
	get("/api/news?topic=macro", &list)
	if len(list.News) != 1 || list.News[0]["publishedAt"] != nil || list.News[0]["topic"] != "macro" {
		t.Errorf("topic=macro %v", list.News)
	}
	if syms, ok := list.News[0]["symbols"].([]any); !ok || len(syms) != 0 {
		t.Errorf("symbols must be [] not null: %v", list.News[0]["symbols"])
	}
	get("/api/news?region=us", &list)
	if len(list.News) != 2 || list.News[0]["url"] != "https://news.example/shared" || fmt.Sprint(list.News[0]["regions"]) != "[crypto us]" {
		t.Errorf("region=us %v", list.News)
	}
	get("/api/news?symbol=eth", &list)
	if regions, ok := list.News[0]["regions"].([]any); list.News[0]["url"] != "https://cached.example/story" || !ok || len(regions) != 0 {
		t.Errorf("regions must be [] not null: %v", list.News[0])
	}
	for _, q := range []string{"region=US", "region=asia", "region=europe"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/news?"+q, nil))
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"bad_request"`) {
			t.Errorf("%s: %d %s", q, rec.Code, rec.Body)
		}
	}
	get("/api/news?limit=2", &list)
	if len(list.News) != 2 || list.News[0]["url"] != "https://news.example/btc-etf" {
		t.Errorf("limit=2 %v", list.News)
	}
	var brief struct{ Brief *MarketBrief }
	get("/api/news/brief", &brief)
	if brief.Brief == nil || brief.Brief.ID != b.ID {
		t.Errorf("brief endpoint %+v", brief.Brief)
	}

	// A second run upserts without duplicating or re-reading anything.
	if err := s.Ingest(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	pool.QueryRow(ctx, "select count(*) from news_items").Scan(&count)
	if count != 5 || crawled["https://news.example/btc-etf"] != 1 || crawled["https://news.example/link"] != 1 {
		t.Errorf("second run: %d rows, crawls %v", count, crawled)
	}

	s.ingestMu.Lock()
	if err := s.Ingest(ctx); !errors.Is(err, errBusy) {
		t.Errorf("concurrent run: %v", err)
	}
	s.ingestMu.Unlock()
	if err := New(Config{}, pool, nil).Ingest(ctx); !errors.Is(err, ErrUnavailable) {
		t.Errorf("disabled ingest: %v", err)
	}

	// Run: items were just fetched, so the loop waits (no third brief) and
	// stops on cancel; with an empty table it ingests at boot.
	runFor := func(until func() bool) {
		t.Helper()
		rctx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { s.Run(rctx); close(done) }()
		for deadline := time.Now().Add(5 * time.Second); !until() && time.Now().Before(deadline); {
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
		<-done
	}
	briefs := func() int { return len(prompts()) }
	start := time.Now()
	runFor(func() bool { return time.Since(start) > 100*time.Millisecond })
	if briefs() != 2 {
		t.Errorf("Run ingested although the last run is fresh: %d briefs", briefs())
	}
	clear()
	runFor(func() bool { return briefs() == 3 })
	if briefs() != 3 {
		t.Errorf("Run did not ingest at boot with no news: %d briefs", briefs())
	}
}

// An asset search's result that names no asset is stored untagged ('{}', never
// NULL) instead of under the searched symbol, and per-symbol reads skip it.
func TestIngestLeavesUnnamedResultsUntagged(t *testing.T) {
	pool := storetest.Schema(t)
	ctx := context.Background()
	brave := newFakeBrave(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") == "Ethereum ETH crypto" {
			fmt.Fprint(w, `{"results":[{"title":"Staking yields rise across the market","url":"https://news.example/staking"}]}`)
			return
		}
		fmt.Fprint(w, `{"results":[]}`)
	})
	s := New(Config{BraveAPIKey: "k", BraveBase: brave.srv.URL}, pool, nil)
	s.brave.gap = 0
	if err := s.Ingest(ctx); err != nil {
		t.Fatal(err)
	}
	var symbols []string
	var null bool
	if err := pool.QueryRow(ctx, "select symbols, symbols is null from news_items where url = 'https://news.example/staking'").Scan(&symbols, &null); err != nil {
		t.Fatal(err)
	}
	if null || symbols == nil || len(symbols) != 0 {
		t.Fatalf("symbols %#v (null %v), want '{}'", symbols, null)
	}
	if h, err := s.Headlines(ctx, "ETH", 0, 10); err != nil || len(h) != 0 {
		t.Fatalf("ETH headlines %+v %v", h, err)
	}
}

func TestIngestSkipsRejectedLegacyBeforeCrawl(t *testing.T) {
	db := storetest.Schema(t)
	ctx := context.Background()
	for i, source := range []string{"Betting News", "Bookmaker Daily", "Desk"} {
		title := "Bitcoin rallies"
		if source == "Desk" {
			title = "Bitcoin ETF demand grows"
		}
		if _, err := db.Exec(ctx, `insert into news_items (title,url,source,symbols,published_at) values ($1,$2,$3,'{BTC}',now()-make_interval(hours=>$4))`, title, fmt.Sprintf("https://news.example/%d", i), source, i); err != nil {
			t.Fatal(err)
		}
	}
	brave := newFakeBrave(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"results":[]}`) })
	var mu sync.Mutex
	var crawled []string
	crawl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			URLs []string `json:"urls"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		crawled = append(crawled, body.URLs...)
		mu.Unlock()
		w.WriteHeader(500)
	}))
	defer crawl.Close()
	s := New(Config{BraveAPIKey: "k", BraveBase: brave.srv.URL, CrawlBase: crawl.URL, CrawlToken: "fixture"}, db, nil)
	s.brave.gap = 0
	s.crawl.direct.lookupIP = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
	}
	s.crawl.direct.dialContext = func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("fixture blocks direct fetch")
	}
	if err := s.Ingest(ctx); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(crawled, []string{"https://news.example/2"}) {
		t.Fatalf("legacy junk consumed crawl slots: %v", crawled)
	}
	for _, url := range []string{"https://news.example/0", "https://news.example/1"} {
		var attempted bool
		if err := db.QueryRow(ctx, `select crawled_at is not null from news_items where url=$1`, url).Scan(&attempted); err != nil || attempted {
			t.Fatalf("junk marked attempted: %s %v %v", url, attempted, err)
		}
	}
}

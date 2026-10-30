package research

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
)

// briefFixture stores n wire rows and a brief citing the first three of them
// as its inputs, created ago, and returns the rows and the brief's id.
func briefFixture(t *testing.T, db *pgxpool.Pool, prefix string, n int, ago time.Duration) ([]NewsItem, string) {
	t.Helper()
	ctx := context.Background()
	var inputs []NewsItem
	for i := range n {
		var it NewsItem
		if err := db.QueryRow(ctx, `insert into news_items (title,url,source,description,symbols,topic,published_at) values ($1,$2,'Reuters','Bitcoin gains on ETF demand','{BTC}','asset',now() - interval '3 hours')
			returning id,title,url,source,description,published_at,symbols,topic,regions`,
			fmt.Sprintf("Bitcoin BTC climbs as ETF flows grow %s%d", prefix, i), fmt.Sprintf("https://www.reuters.com/markets/%s-btc-%d", prefix, i)).
			Scan(&it.ID, &it.Title, &it.URL, &it.Source, &it.Description, &it.PublishedAt, &it.Symbols, &it.Topic, &it.Regions); err != nil {
			t.Fatal(err)
		}
		inputs = append(inputs, it)
	}
	b := MarketBrief{Headline: "Bitcoin climbs " + prefix, Summary: "s", ArticleCount: len(inputs),
		Bullets: []BriefBullet{{Text: "a", Sources: []string{inputs[0].ID}}, {Text: "b", Sources: []string{inputs[1].ID}}, {Text: "c", Sources: []string{inputs[2].ID, inputs[0].ID}}}}
	raw, _ := json.Marshal(struct {
		MarketBrief
		Quality InputQuality `json:"_quality"`
	}{b, InputQuality{Version: 1, Inputs: inputs}})
	var id string
	if err := db.QueryRow(ctx, `insert into market_briefs (brief, created_at) values ($1, now() - make_interval(secs => $2)) returning id`, raw, ago.Seconds()).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return inputs, id
}

// An ingest that sees a cited URL again with another snippet and a later
// page_age keeps the row as first stored, so the brief (and every signals
// board resting on it) still validates.
func TestIngestKeepsCitedRows(t *testing.T) {
	db := storetest.Schema(t)
	ctx := context.Background()
	inputs, id := briefFixture(t, db, "k", 5, time.Hour)
	if _, err := db.Exec(ctx, `update news_items set fetched_at = now() - interval '2 hours'`); err != nil {
		t.Fatal(err)
	}
	brave := newFakeBrave(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") == "Bitcoin BTC crypto" {
			fmt.Fprintf(w, `{"results":[{"title":%q,"url":%q,"description":"Bitcoin gains on ETF demand, updated with a longer snippet","page_age":%q,"profile":{"name":"Reuters"}}]}`,
				inputs[0].Title, inputs[0].URL, time.Now().UTC().Add(-time.Minute).Format("2006-01-02T15:04:05"))
			return
		}
		fmt.Fprint(w, `{"results":[]}`)
	})
	s := New(Config{BraveAPIKey: "k", BraveBase: brave.srv.URL}, db, nil)
	s.brave.gap = 0
	s.crawl.direct.lookupIP = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
	}
	s.crawl.direct.dialContext = func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("direct fetch blocked in test")
	}
	for range 2 {
		if err := s.Ingest(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var desc string
	var published, fetched time.Time
	if err := db.QueryRow(ctx, `select description, published_at, fetched_at from news_items where id = $1`, inputs[0].ID).Scan(&desc, &published, &fetched); err != nil ||
		desc != inputs[0].Description || !published.Equal(*inputs[0].PublishedAt) || time.Since(fetched) > time.Minute {
		t.Fatalf("re-seen row: %q %v %v %v", desc, published, fetched, err) // fetched_at moved: the upsert reached the row
	}
	if b, err := s.LatestBrief(ctx); err != nil || b == nil || b.ID != id {
		t.Fatalf("brief after two ingests: %+v %v", b, err)
	}
	// A signals board's sentiment dependency: the brief's own inputs still validate.
	if got, err := s.BriefInputs(ctx, id); err != nil || len(got) != len(inputs) {
		t.Fatalf("brief inputs: %d %v", len(got), err)
	}
}

// LatestBrief walks back past a brief that does not validate, within 24 h;
// the brief response lists every cited row, newest 100 or not.
func TestLatestBriefFallbackAndSources(t *testing.T) {
	db := storetest.Schema(t)
	ctx := context.Background()
	s := New(Config{}, db, nil)
	inputs, prev := briefFixture(t, db, "p", 4, 3*time.Hour)
	_, newest := briefFixture(t, db, "n", 3, time.Hour)
	if _, err := db.Exec(ctx, `update news_items set description = 'changed' where url like '%/n-btc-0'`); err != nil {
		t.Fatal(err)
	}
	if b, err := s.LatestBrief(ctx); err != nil || b == nil || b.ID != prev {
		t.Fatalf("fallback: %+v %v (newest %s)", b, err, newest)
	}

	// 120 newer wire rows: the brief's citations are past the newest 100.
	for i := range 120 {
		if _, err := db.Exec(ctx, `insert into news_items (title,url,source,symbols,topic,published_at) values ($1,$2,'Wire','{BTC}','asset',now())`,
			fmt.Sprintf("Bitcoin BTC wire %d", i), fmt.Sprintf("https://wire.example/%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	s.Register(mux, func(h http.Handler) http.Handler { return h })
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/news/brief", nil))
	var got struct {
		Brief *struct {
			ID      string        `json:"id"`
			Sources []BriefSource `json:"sources"`
		} `json:"brief"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Brief == nil || got.Brief.ID != prev || len(got.Brief.Sources) != 3 {
		t.Fatalf("brief response %d %s", rec.Code, rec.Body)
	}
	for i, src := range got.Brief.Sources {
		if src.ID != inputs[i].ID || src.Title != inputs[i].Title || src.URL != inputs[i].URL || src.Source != "Reuters" || src.PublishedAt == nil {
			t.Errorf("source %d %+v", i, src)
		}
	}
	wire, _ := s.News(ctx, NewsFilter{Limit: 100})
	for _, n := range wire {
		if n.ID == inputs[0].ID {
			t.Fatal("the cited row is among the newest 100: the fixture proves nothing")
		}
	}

	// Past 24 h a brief that validates is found only when it is the newest.
	if _, err := db.Exec(ctx, `update market_briefs set created_at = created_at - interval '1 day'`); err != nil {
		t.Fatal(err)
	}
	if b, err := s.LatestBrief(ctx); err != nil || b != nil {
		t.Fatalf("both over 24 h old: %+v %v", b, err)
	}
	if _, err := db.Exec(ctx, `delete from market_briefs where id = $1`, newest); err != nil {
		t.Fatal(err)
	}
	if b, err := s.LatestBrief(ctx); err != nil || b == nil || b.ID != prev {
		t.Fatalf("the newest, 27 h old and valid: %+v %v", b, err)
	}
	rec = httptest.NewRecorder()
	if _, err := db.Exec(ctx, `delete from market_briefs`); err != nil {
		t.Fatal(err)
	}
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/news/brief", nil))
	if rec.Body.String() != `{"brief":null}`+"\n" {
		t.Errorf("no brief: %s", rec.Body)
	}
}

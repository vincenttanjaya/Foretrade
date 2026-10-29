package research

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
)

func TestBriefInputSnapshots(t *testing.T) {
	// Labels a later search adds (ingest merges symbols and regions) keep the brief.
	valid := map[string]bool{"valid": true, "retagged": true, "relabelled": true}
	for _, mutation := range []string{"valid", "missing-provenance", "incomplete", "changed-title", "changed-source", "changed-description", "deleted", "junk-input", "uncited-bullet",
		"retagged", "relabelled", "untagged"} {
		t.Run(mutation, func(t *testing.T) {
			db := storetest.Schema(t)
			ctx := context.Background()
			var id string
			if err := db.QueryRow(ctx, `insert into news_items (title,url,source,description,symbols) values ('Bitcoin ETF inflows rise','https://reuters.com/btc','Reuters','Bitcoin gains','{BTC}') returning id`).Scan(&id); err != nil {
				t.Fatal(err)
			}
			inputs := []NewsItem{{ID: id, Title: "Bitcoin ETF inflows rise", URL: "https://reuters.com/btc", Source: "Reuters", Description: "Bitcoin gains", Symbols: []string{"BTC"}, Topic: "asset"}}
			b := MarketBrief{Headline: "Clean headline", Summary: "Clean summary", ArticleCount: 1, Bullets: []BriefBullet{{Text: "One", Sources: []string{id}}, {Text: "Two", Sources: []string{id}}, {Text: "Three", Sources: []string{id}}}}
			stored := map[string]any{}
			raw, _ := json.Marshal(b)
			json.Unmarshal(raw, &stored)
			stored["_quality"] = map[string]any{"version": 1, "inputs": inputs}
			switch mutation {
			case "missing-provenance":
				delete(stored, "_quality")
			case "incomplete":
				stored["articleCount"] = 2
			case "changed-title":
				db.Exec(ctx, `update news_items set title='Bitcoin falls' where id=$1`, id)
			case "changed-source":
				db.Exec(ctx, `update news_items set source='Other desk' where id=$1`, id)
			case "changed-description":
				db.Exec(ctx, `update news_items set description='Bitcoin slides' where id=$1`, id)
			case "deleted":
				db.Exec(ctx, `delete from news_items where id=$1`, id)
			case "junk-input":
				inputs[0].Source = "Betting News"
				db.Exec(ctx, `update news_items set source='Betting News' where id=$1`, id)
			case "retagged":
				db.Exec(ctx, `update news_items set symbols='{BTC,ETH}' where id=$1`, id)
			case "relabelled":
				db.Exec(ctx, `update news_items set regions='{crypto,us}' where id=$1`, id)
			case "untagged":
				db.Exec(ctx, `update news_items set symbols='{}' where id=$1`, id)
			case "uncited-bullet":
				b.Bullets[0].Sources = nil
				stored["bullets"] = b.Bullets
			}
			raw, _ = json.Marshal(stored)
			if _, err := db.Exec(ctx, `insert into market_briefs (brief) values ($1)`, raw); err != nil {
				t.Fatal(err)
			}
			s := New(Config{}, db, nil)
			for range 2 {
				got, err := s.LatestBrief(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if (got != nil) != valid[mutation] {
					t.Fatalf("brief availability %v: %+v", mutation, got)
				}
				if got != nil {
					public, _ := json.Marshal(got)
					var shape map[string]any
					json.Unmarshal(public, &shape)
					if _, ok := shape["_quality"]; ok {
						t.Fatal("internal provenance leaked")
					}
				}
			}
		})
	}
}

// otherEconomy reads a stored input's labels from its row: a headline stored
// before a regional search labelled it (or before the regions column) is not
// another country's economy once the row says it is East Asian.
func TestValidateNewsInputsReadsRowLabels(t *testing.T) {
	db := storetest.Schema(t)
	ctx := context.Background()
	n := NewsItem{Title: "Nikkei climbs as Bank of Japan holds rates", URL: "https://reuters.com/nikkei", Source: "Reuters", Symbols: []string{}, Topic: "macro"}
	if !otherEconomy(n) {
		t.Fatal("the fixture is not another economy's macro story without labels")
	}
	if err := db.QueryRow(ctx, `insert into news_items (title,url,source,symbols,topic,regions) values ($1,$2,$3,'{}','macro','{east_asia}') returning id`,
		n.Title, n.URL, n.Source).Scan(&n.ID); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		regions string
		valid   bool
	}{{"{east_asia}", true}, {"{}", false}} {
		db.Exec(ctx, `update news_items set regions=$2 where id=$1`, n.ID, tc.regions)
		if got, err := ValidateNewsInputs(ctx, db, []NewsItem{n}); err != nil || got != tc.valid {
			t.Errorf("row regions %s: %v %v", tc.regions, got, err)
		}
	}
	if got, err := ValidateNewsInputs(ctx, db, nil); err != nil || !got {
		t.Errorf("no inputs: %v %v", got, err)
	}
}

func TestCachedPageRejectsJunkPreservesGenericResearch(t *testing.T) {
	db := storetest.Schema(t)
	ctx := context.Background()
	for _, tc := range []struct {
		source, title string
		reject        bool
	}{{"Betting News", "Bitcoin rallies", true}, {"Desk", "Bitcoin rallies | BettingNews", true}, {"Desk", "How to grow tomatoes", false}} {
		url := "https://news.example/" + tc.source + tc.title
		_, err := db.Exec(ctx, `insert into news_items (title,url,source,content) values ($1,$2,$3,repeat('Useful page content. ',100))`, tc.title, url, tc.source)
		if err != nil {
			t.Fatal(err)
		}
		p, err := New(Config{}, db, nil).ReadPage(ctx, url, 1000)
		if tc.reject && err == nil {
			t.Fatalf("cached junk returned: %+v", p)
		}
		if !tc.reject && (err != nil || p.Via != "cache") {
			t.Fatalf("generic cache rejected: %+v %v", p, err)
		}
	}
}

func TestBraveRawPublisherQuality(t *testing.T) {
	brave := newFakeBrave(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"results":[
 {"title":"Bitcoin rallies","url":"https://neutral.example/publisher","profile":{"name":"Bookmaker Daily"}},
 {"title":"Bitcoin rallies | BettingNews","url":"https://neutral.example/tail"},
 {"title":"Bitcoin rallies","url":"https://OPENPR.COM.:443/junk"},
 {"title":"NBA odds market opens tonight","url":"https://neutral.example/sport"},
 {"title":"Bitcoin holds $85,000, BNB eyes $800","url":"https://cointurk.com/btc"},
 {"title":"SEC Proposes New Custody Framework for Crypto Assets","url":"https://sec.gov/custody"},
 {"title":"This Week in Crypto Law (October 4, 2026)","url":"https://law.example/weekly"},
 {"title":"Fed holds rates, dollar slips","url":"https://reuters.com/fed"},
 {"title":"European shares climb after strong results","url":"https://reuters.com/shares"},
 {"title":"How to grow tomatoes","url":"https://garden.example/tomatoes"}]}`)
	})
	s := New(Config{BraveAPIKey: "fixture", BraveBase: brave.srv.URL}, nil, nil)
	got, err := s.SearchNews(context.Background(), "explicit research", "pd", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 6 {
		t.Fatalf("raw publisher boundary: %+v", got)
	}
	for _, it := range got {
		if strings.Contains(it.URL, "neutral.example") || strings.Contains(it.URL, "OPENPR") {
			t.Fatalf("raw junk survives: %+v", it)
		}
	}
}

// Ordinary newsrooms can publish excluded industry stories; filtering must
// inspect title/description rather than relying on a gambling publisher/host.
func TestGamblingIndustryReachableBoundaries(t *testing.T) {
	cases := []struct {
		title, description string
		keep               bool
	}{
		{"Gambling stocks rise as regulation eases", "", false},
		{"Betting stocks rally on strong earnings", "", false},
		{"European shares climb after results", "Gambling companies report strong earnings", false},
		{"Markets rally on earnings", "Betting operators raise revenue forecasts", false},
		{"Gambling sector expands amid deregulation", "", false},
		{"Betting firms lift profit forecasts", "", false},
		{"Polymarket odds favour a December cut", "", true},
		{"European shares climb after strong results", "", true},
		{"Investors betting on a Fed cut lift stocks", "", true},
		{"Fed holds rates, dollar slips", "Investors are betting on lower inflation", true},
		{"Market share rises as options and futures volumes grow", "", true},
		{"Investors are betting stocks will rally after a Fed rate cut", "", true},
		{"Investors are betting stocks rally after a Fed cut", "", true},
		{"Fed decision boosts markets", "Analysts are betting companies report stronger earnings", true},
		{"Investors are betting shares will rise after a Fed rate cut", "", true},
		{"Fed cut lifts stocks", "Traders are betting stocks will extend their rally", true},
		{"Investors betting profits will grow after the Fed cut lift markets", "", true},
		{"Analysts are betting earnings could recover as stocks rise", "", true},
		{"Fed rate cut boosts markets", "Investors are betting companies will report stronger revenues", true},
		{"Fed policy lifts shares", "Traders were betting industry would recover", true},
		{"Stocks rise on rate cuts", "Analysts are betting operators can improve margins", true},
		{"Betting stocks will rally on industry earnings", "", false},
		{"Markets gain as investors are betting profits will improve", "Gambling firms expand", false},
		{"Investors are betting stocks will rally as betting companies grow", "", false},
	}
	t.Run("Brave", func(t *testing.T) {
		for i, tc := range cases {
			t.Run(fmt.Sprint(i), func(t *testing.T) {
				brave := newFakeBrave(t, func(w http.ResponseWriter, r *http.Request) {
					json.NewEncoder(w).Encode(map[string]any{"results": []any{map[string]any{"title": tc.title, "description": tc.description, "url": "https://www.reuters.com/markets/industry", "profile": map[string]any{"name": "Reuters"}}}})
				})
				s := New(Config{BraveAPIKey: "fixture", BraveBase: brave.srv.URL}, nil, nil)
				got, err := s.SearchNews(context.Background(), "market industry", "pd", 10)
				if err != nil {
					t.Fatal(err)
				}
				if (len(got) == 1) != tc.keep {
					t.Errorf("Brave title=%q description=%q keep=%v results=%+v", tc.title, tc.description, tc.keep, got)
				}
			})
		}
	})
	t.Run("StoredWire", func(t *testing.T) {
		db := storetest.Schema(t)
		ctx := context.Background()
		for i, tc := range cases {
			if _, err := db.Exec(ctx, `insert into news_items (title,description,url,source,published_at) values ($1,$2,$3,'Reuters',now()-make_interval(secs=>$4))`, tc.title, tc.description, fmt.Sprintf("https://www.reuters.com/markets/industry/%d", i), i); err != nil {
				t.Fatal(err)
			}
		}
		got, err := New(Config{}, db, nil).News(ctx, NewsFilter{Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		found := map[string]bool{}
		for _, n := range got {
			found[n.Title] = true
		}
		for _, tc := range cases {
			if found[tc.title] != tc.keep {
				t.Errorf("stored title=%q description=%q keep=%v actual=%v", tc.title, tc.description, tc.keep, found[tc.title])
			}
		}
	})
}

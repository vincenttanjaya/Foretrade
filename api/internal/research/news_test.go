package research

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
)

func TestNormTitle(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Bitcoin Tops $120K as ETFs Pile In - 24/7 Wall St.", "bitcoin tops 120k as etfs pile in"},
		{"Bitcoin tops $120K as ETFs pile in", "bitcoin tops 120k as etfs pile in"},
		{"Fed holds rates | Reuters", "fed holds rates"},
		{"Fed holds rates – Financial Times", "fed holds rates"},
		{"Ether ETF - flows - Yahoo Finance", "ether etf flows"}, // only the last segment
		{"Markets wrap - a very long tail that is clearly part of the headline itself", "markets wrap a very long tail that is clearly part of the headline itself"}, // over 40: kept
		{"- Reuters", "reuters"}, // nothing before the dash: kept
		{"  Solana’s  TVL...doubles!! ", "solana s tvl doubles"},
	} {
		if got := normTitle(tc.in); got != tc.want {
			t.Errorf("normTitle(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNewsAndSearchDedupeSyndicatedCopies(t *testing.T) {
	pool := storetest.Schema(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `insert into news_items (url, title, source, published_at, symbols) values
		('https://247wallst.com/a', 'Bitcoin Tops $120K as ETFs Pile In - 24/7 Wall St.', '24/7 Wall St.', now() - interval '1 hour', '{BTC}'),
		('https://aol.com/b', 'Bitcoin tops $120K as ETFs pile in', 'AOL', now() - interval '2 hours', '{BTC}'),
		('https://news.example/c', 'Bitcoin miners sell', 'Desk', now() - interval '3 hours', '{BTC}')`); err != nil {
		t.Fatal(err)
	}
	s := New(Config{}, pool, nil)
	items, err := s.Headlines(ctx, "BTC", 0, 10)
	if err != nil || len(items) != 2 || items[0].Source != "24/7 Wall St." || items[1].Title != "Bitcoin miners sell" {
		t.Fatalf("headlines %+v %v", items, err)
	}

	f := newFakeBrave(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"results":[
			{"title":"Bitcoin tops $120K as ETFs pile in","url":"https://aol.com/b"},
			{"title":"Bitcoin Tops $120K as ETFs Pile In - Yahoo Finance","url":"https://finance.yahoo.com/a","profile":{"name":"Yahoo Finance"}},
			{"title":"Bitcoin miners sell","url":"https://news.example/c"}]}`)
	})
	s = New(Config{BraveAPIKey: "k", BraveBase: f.srv.URL}, nil, nil)
	got, err := s.SearchNews(ctx, "bitcoin", "pd", 10)
	if err != nil || len(got) != 2 || got[0].Source != "Yahoo Finance" || got[1].URL != "https://news.example/c" {
		t.Fatalf("search %+v %v", got, err) // the known outlet's copy wins: it is ranked first
	}
}

// Headlines (the AI surfaces' read) puts known outlets first within each
// OnTopic group, newest first within those; GET /api/news stays newest first.
func TestHeadlinesRankKnownOutletsFirst(t *testing.T) {
	pool := storetest.Schema(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `insert into news_items (url, title, source, description, published_at, symbols) values
		('https://blog.example/sol-1', 'Solana DEX volumes hit a record', 'Blog', '', now() - interval '1 hour', '{SOL}'),
		('https://www.coindesk.com/sol-2', 'Solana validators ship Firedancer', 'CoinDesk', '', now() - interval '3 hours', '{SOL}'),
		('https://www.reuters.com/sol-3', 'Funds pile into Solana ETFs', 'Reuters', '', now() - interval '5 hours', '{SOL}'),
		('https://www.reuters.com/fed', 'Fed holds rates', 'Reuters', 'Bitcoin and solana dipped', now() - interval '30 minutes', '{BTC,SOL}')`); err != nil {
		t.Fatal(err)
	}
	s := New(Config{}, pool, nil)
	urls := func(items []NewsItem, err error) (out []string) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range items {
			out = append(out, n.URL)
		}
		return out
	}
	if got := urls(s.Headlines(ctx, "sol", 0, 10)); fmt.Sprint(got) !=
		"[https://www.coindesk.com/sol-2 https://www.reuters.com/sol-3 https://blog.example/sol-1 https://www.reuters.com/fed]" {
		t.Errorf("SOL headlines %v", got) // the title-named group first, known outlets first in it; the Fed story names SOL only in its description
	}
	if got := urls(s.Headlines(ctx, "", 0, 10)); fmt.Sprint(got) !=
		"[https://www.reuters.com/fed https://www.coindesk.com/sol-2 https://www.reuters.com/sol-3 https://blog.example/sol-1]" {
		t.Errorf("all headlines %v", got)
	}
	if got := urls(s.News(ctx, NewsFilter{Symbol: "SOL"})); got[0] != "https://blog.example/sol-1" {
		t.Errorf("GET /api/news keeps newest first: %v", got)
	}
}

func TestLegacyBriefEntirelySuppressed(t *testing.T) {
	pool := storetest.Schema(t)
	ctx := context.Background()
	_, err := pool.Exec(ctx, `insert into market_briefs (brief) values ('{"headline":"Bookmaker rally","summary":"Junk derived","bullets":[],"sentiment":[{"symbol":"BTC","score":1,"reason":"Gambling"}],"articleCount":1}')`)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		b, err := New(Config{}, pool, nil).LatestBrief(ctx)
		if err != nil || b != nil {
			t.Fatalf("legacy prose exposed: %+v %v", b, err)
		}
	}
}

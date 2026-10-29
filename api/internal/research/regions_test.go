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

	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
)

// Regional market headlines reach the wire by index, exchange, central-bank,
// "<country> stocks" or universe-stock names; lifestyle pages from the same
// places and junk stay out exactly as before.
func TestRegionalWireRelevance(t *testing.T) {
	for title, want := range map[string]bool{
		// US
		"Russell 2000 notches its best week since July": true,
		"Nvidia unveils its next AI chip":               true,
		"NYSE extends its opening hours":                true,
		"Best things to do in New York this weekend":    false,
		// East Asia
		"Nikkei 225 closes at a record high":    true,
		"KOSPI ends at a three-month high":      true,
		"TAIEX tops 25,000 for the first time":  true,
		"Hang Seng slides 2% on property woes":  true,
		"BOK holds at 2.5% as won slides":       true,
		"TSMC lifts its full-year outlook":      true,
		"SoftBank unveils a $20bn AI fund":      true,
		"Zhipu debuts in Hong Kong":             true,
		"Tokyo's best ramen shops, ranked":      false,
		"Seoul fashion week: the looks to know": false,
		"Samsung unveils the Galaxy Z Fold 8":   false, // the group's phones, not Samsung Electronics' shares
		"Nikkei 225 forecast 2026-2030":         false, // junk title
		// Southeast Asia
		"Straits Times Index edges up":                 true,
		"STI ends flat ahead of MAS decision":          true,
		"JCI slides as rupiah weakens":                 true,
		"SET index falls to a five-year low":           true,
		"PSEi rebounds on bargain hunting":             true,
		"VN-Index hits 1,300":                          true,
		"Bangko Sentral cuts by 25 bp":                 true,
		"Monetary Authority of Singapore keeps policy": true,
		"MAS leaves policy unchanged":                  true,
		"IDX Composite slips below 7,000":              true,
		"Singapore's best hawker centres":              false,
		"Singapore sees a rise in STI cases":           false, // a health story: STI only before a move word
		"STI testing clinic opens in Tampines":         false,
		"MAS flight delayed in Singapore":              false,
		"BSP chief slams the government":               false,
		"Vietnam travel: 10 beaches to visit":          false,
		"Thai street food festival returns":            false,
		"800 SGD to USD: Today's Exchange Rate":        false, // converter
		"Best Singapore casino bonus codes":            false, // spam
		"Hang Seng index today - Sports Betting Stats": false, // gambling site
	} {
		if got := Relevant(title, "", "https://news.example/a", "Desk", ""); got != want {
			t.Errorf("Relevant(%q) = %v", title, got)
		}
	}
	for u, want := range map[string]bool{"https://asia.nikkei.com/markets/a": true, "https://www.nikkei.com/article/a": false, // Japanese edition
		"https://en.yna.co.kr/view/a": true, "https://koreajoongangdaily.joins.com/a": true, "https://focustaiwan.tw/business/a": true,
		"https://www.scmp.com/business/a": true, "https://www.straitstimes.com/business/a": true, "https://e.vnexpress.net/news/a": true,
		"https://business.inquirer.net/a": true, "https://www.theedgemalaysia.com/a": true} {
		if Known(u) != want {
			t.Errorf("Known(%s) = %v", u, !want)
		}
	}
}

func TestWireRegions(t *testing.T) {
	asset := wireSearch{"Bitcoin BTC crypto", "asset", RegionCrypto, ""}
	usMacro := wireSearch{"Federal Reserve interest rates", "macro", RegionUS, "us"}
	mag7 := wireSearch{"Magnificent Seven stocks", "asset", RegionUS, "us"}
	kospi := wireSearch{"KOSPI Korea stocks Bank of Korea", "macro", RegionEastAsia, "kr"}
	sti := wireSearch{"Singapore stocks STI MAS", "macro", RegionSoutheastAsia, ""}
	earnings := wireSearch{"Wall Street earnings", "macro", RegionUS, "us"}
	for _, tc := range []struct {
		ws          wireSearch
		title, desc string
		want        []string // nil: skipped
	}{
		{asset, "Bitcoin slides as BOJ hike lifts the yen", "", []string{"crypto", "east_asia"}}, // a cue names East Asia
		{asset, "Staking yields rise across the market", "", []string{"crypto"}},                 // untagged: the search's region
		{asset, "Bitcoin dips as Asian shares fall", "Hang Seng led losses", []string{"crypto"}}, // cues count in the title only
		{usMacro, "Fed holds rates; bitcoin dips", "", []string{"crypto", "us"}},
		{usMacro, "Pakistan's inflation remains in double digits", "", nil},           // another country's economy
		{usMacro, "Bank of Korea holds rates as won slides", "", nil},                 // US macro keeps its rule …
		{kospi, "Bank of Korea holds rates as won slides", "", []string{"east_asia"}}, // … the regional search keeps the story
		{kospi, "Stocks edge higher as exporters gain", "Korea's benchmark rose 0.4%", []string{"east_asia"}},
		{kospi, "European shares climb after strong results", "", nil}, // names no region
		{kospi, "Samsung Electronics shares hit a record", "", []string{"east_asia"}},
		{sti, "STI edges up as MAS keeps policy unchanged", "", []string{"southeast_asia"}},
		{sti, "Asian markets mixed ahead of US data", "", []string{"southeast_asia"}},
		{sti, "Dow slips as yields climb", "", []string{"us"}}, // another region's market: labelled as such
		// US tickers that are also Asian index or central-bank acronyms name no Asian market.
		{earnings, "Masco (MAS) Q3 earnings beat estimates", "Wall Street expected less", []string{"us"}},
		{earnings, "Johnson Controls (JCI) earnings top estimates", "Wall Street expected less", []string{"us"}},
		{earnings, "BOK Financial earnings top estimates", "Wall Street expected less", []string{"us"}},
		{mag7, "Nvidia and Tesla shares lead a rebound", "", []string{"us"}},
		{mag7, "Alibaba shares jump on cloud growth", "", []string{"east_asia", "us"}},
	} {
		text := tc.title + "\n" + tc.desc
		got, ok := wireRegions(tc.ws, tc.title, text, tagSymbols(text))
		if ok != (tc.want != nil) || !slices.Equal(got, tc.want) {
			t.Errorf("%s: %q = %v %v, want %v", tc.ws.query, tc.title, got, ok, tc.want)
		}
	}
}

// Every asset maps to a region: crypto, a US company's listing, an Asian
// local listing's exchange, or a US-listed Asian company's home market (the
// Indian ADRs belong to neither Asian region).
func TestAssetRegions(t *testing.T) {
	southeast := []string{"SE", "GRAB", "PHI", "TLK", "KARO", "KLIC"} // US-listed Singapore, Philippine and Indonesian companies
	neither := []string{"INFY", "HDB", "IBN"}
	seen := map[string]int{}
	for _, a := range market.Universe {
		want := RegionCrypto
		switch {
		case !a.Perp():
		case a.Listing.Region == "us":
			want = RegionUS
		case slices.Contains([]string{market.SGX, market.IDX, market.SET, market.BURSA, market.HOSE}, a.Listing.SessionVenue) || slices.Contains(southeast, a.Symbol):
			want = RegionSoutheastAsia
		case slices.Contains(neither, a.Symbol):
			want = ""
		case slices.Contains([]string{market.KRX, market.TSE, market.HKEX, market.SSE, market.TWSE}, a.Listing.SessionVenue):
			want = RegionEastAsia
		default:
			if _, ok := usListedAsia[a.Symbol]; !ok {
				t.Errorf("%s: a new US-listed Asian row; give usListedAsia its home region", a.Symbol)
				continue
			}
			want = RegionEastAsia
		}
		seen[want]++
		if got := assetRegion(a); got != want {
			t.Errorf("assetRegion(%s) = %q, want %q", a.Symbol, got, want)
		}
	}
	for sym := range usListedAsia {
		if a, ok := market.Lookup(sym); !ok || a.Listing == nil || a.Listing.Region != "asia" || a.Listing.SessionVenue != market.NYSE {
			t.Errorf("usListedAsia %s is not a US-listed Asian row", sym)
		}
	}
	// 21 US perps and HSDT; 17 Asian perps and 92 East Asian quote feeds; 42 Southeast Asian quote feeds.
	if seen[RegionCrypto] != 69 || seen[RegionUS] != 22 || seen[RegionEastAsia] != 109 || seen[RegionSoutheastAsia] != 42 || seen[""] != 3 {
		t.Errorf("regions %v", seen)
	}
	for sym, want := range map[string][]string{"DBS": {"southeast_asia"}, "GRAB": {"southeast_asia"}, "TENCENT": {"east_asia"}, "KIA": {"east_asia"},
		"QUANTA": {"east_asia"}, "SONY": {"east_asia"}, "HSDT": {"us"}, "PLTR": {"us"}, "INFY": {}, "BTC": {"crypto"}} {
		if got := contentRegions("", []string{sym}); !slices.Equal(got, want) {
			t.Errorf("contentRegions(%s) = %v, want %v", sym, got, want)
		}
	}
}

// The regional searches run on every other ingest; the Brave estimate the
// README documents stays well inside the background share.
func TestWireSearchesBudget(t *testing.T) {
	base, all := wireSearches(false), wireSearches(true)
	if len(base) != 18 || len(all) != len(base)+len(regionalSearches) || len(regionalSearches) < 14 || len(regionalSearches) > 18 {
		t.Fatalf("%d base, %d with %d regional searches", len(base), len(all), len(regionalSearches))
	}
	// Brave's country codes (news search API reference) have no sg, th or vn.
	countries := []string{"", "us", "jp", "kr", "tw", "hk", "cn", "id", "my", "ph"}
	seen := map[string]bool{}
	for _, ws := range all {
		if seen[ws.query] || !slices.Contains(countries, ws.country) || ws.topic != "asset" && ws.topic != "macro" || !slices.Contains(Regions, ws.region) {
			t.Errorf("search %+v", ws)
		}
		seen[ws.query] = true
	}
	for _, r := range Regions {
		if !slices.ContainsFunc(all, func(ws wireSearch) bool { return ws.region == r }) {
			t.Errorf("no %s search", r)
		}
	}
	runs := int(24 * time.Hour / ingestEvery)
	regional := runs / regionalEvery * len(regionalSearches)
	if before, after := runs*len(base), runs*len(base)+regional; before != 216 || after != 324 || regional < 90 || regional > 110 ||
		2*after > int(backgroundShare*1500) {
		t.Errorf("Brave calls a day: %d before, %d after (%d regional)", before, after, regional)
	}
}

// The brief takes the newest 80 crypto, US or unlabelled headlines plus up
// to 10 that are only East Asian and 10 only Southeast Asian, in order.
func TestBriefInputs(t *testing.T) {
	var items []NewsItem
	for i := range 120 {
		regions := [][]string{{"crypto"}, {"east_asia"}, {"east_asia", "southeast_asia"}, {"southeast_asia"}, {"east_asia", "us"}, {}}[i%6]
		items = append(items, NewsItem{ID: fmt.Sprintf("%03d", i), Regions: regions})
	}
	got := briefInputs(items)
	count := map[string]int{}
	for i, it := range got {
		if i > 0 && it.ID <= got[i-1].ID {
			t.Fatalf("order changed at %d: %v", i, it.ID)
		}
		count[fmt.Sprint(it.Regions)]++
	}
	// 20 each of crypto, East Asia + US and unlabelled; the two East Asian kinds share 10.
	if count["[crypto]"] != 20 || count["[east_asia us]"] != 20 || count["[]"] != 20 ||
		count["[east_asia]"]+count["[east_asia southeast_asia]"] != 10 || count["[southeast_asia]"] != 10 || len(got) != 80 {
		t.Errorf("brief inputs %v", count)
	}
	var us []NewsItem
	for i := range 90 {
		us = append(us, NewsItem{ID: fmt.Sprintf("%03d", i), Regions: []string{"us"}})
	}
	if got := briefInputs(us); len(got) != briefMaxItems || got[79].ID != "079" {
		t.Errorf("%d inputs", len(got))
	}
}

func TestNormalizeBriefRegions(t *testing.T) {
	items := []NewsItem{
		{ID: "id-1", Symbols: []string{"BTC"}, Regions: []string{"crypto", "east_asia"}},
		{ID: "id-2", Symbols: []string{}, Regions: []string{"southeast_asia"}},
		{ID: "id-3", Symbols: []string{}, Regions: []string{"east_asia"}},
	}
	var d briefDraft
	if err := json.Unmarshal([]byte(`{"headline":"Asia rallies","summary":"The Nikkei hit a record.",
	 "bullets":[{"text":"a","sources":[1]},{"text":"b","sources":[2]},{"text":"c","sources":[3]}],
	 "regions":[{"region":"southeast_asia","summary":" The STI edged up. "},{"region":"europe","summary":"Not a region."},
	            {"region":"us","summary":"No US headlines."},{"region":"crypto","summary":"  "},
	            {"region":"east_asia","summary":"The Nikkei closed at a record."},{"region":"east_asia","summary":"A duplicate."}]}`), &d); err != nil {
		t.Fatal(err)
	}
	b, err := normalizeBrief(d, items, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if want := []RegionBrief{{"east_asia", "The Nikkei closed at a record."}, {"southeast_asia", "The STI edged up."}}; !slices.Equal(b.Regions, want) {
		t.Errorf("regions %+v", b.Regions)
	}
	if raw, _ := json.Marshal(MarketBrief{}); strings.Contains(string(raw), "regions") {
		t.Errorf("a brief without region lines omits the field: %s", raw)
	}
	p := briefPrompt(items)
	if !strings.Contains(p, "| symbols: BTC | regions: crypto,east_asia\n") || !strings.Contains(p, "Regions with headlines: crypto, east_asia, southeast_asia.") {
		t.Errorf("prompt %s", p)
	}
}

// Ingestion runs the regional searches on its first run and every other one
// after; their results are labelled, merged by URL, read two per region and
// briefed, and GET /api/news filters by region.
func TestIngestRegional(t *testing.T) {
	pool := storetest.Schema(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `insert into news_items (url, title, source, published_at, symbols, topic) values
		('https://news.example/pk', 'Pakistan''s inflation remains in double digits', 'Dawn', now() - interval '1 hour', '{}', 'macro')`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	res := func(title, url, desc string, ago time.Duration) string {
		return fmt.Sprintf(`{"title":%q,"url":%q,"description":%q,"page_age":%q}`, title, url, desc, now.Add(-ago).Format("2006-01-02T15:04:05"))
	}
	boj := res("Bitcoin slides as BOJ hike lifts the yen", "https://news.example/btc-boj", "", time.Hour)
	bok := res("Bank of Korea holds rates as won slides", "https://news.example/bok", "Exporters weigh the currency", 2*time.Hour)
	fixtures := map[string][]string{
		"Bitcoin BTC crypto":             {boj},
		"Federal Reserve interest rates": {bok}, // not US: skipped by the US macro rule
		"Magnificent Seven stocks":       {res("Nvidia and Tesla shares lead a Magnificent Seven rebound", "https://www.cnbc.com/mag7", "", 90*time.Minute)},
		"Nikkei Japan stocks": {
			res("Nikkei 225 closes at a record as chip stocks rally", "https://asia.nikkei.com/markets/nikkei", "Tokyo Electron led", 3*time.Hour),
			res("Stocks edge higher as exporters gain", "https://news.example/exporters", "Japan's benchmark rose 0.4% as the yen weakened", 4*time.Hour),
			res("Tokyo's best ramen shops, ranked", "https://food.example/ramen", "Where to eat", 5*time.Hour),         // off topic
			res("European shares climb after strong results", "https://news.example/europe", "Banks led", 5*time.Hour), // no region
		},
		"Bank of Japan BOJ":                           {boj}, // merged with the BTC search's copy
		"KOSPI Korea stocks Bank of Korea":            {bok},
		"Samsung Electronics SK hynix Hyundai shares": {res("Samsung Electronics shares hit a record on AI memory demand", "https://www.koreaherald.com/samsung", "", 6*time.Hour)},
		"Singapore stocks STI MAS":                    {res("STI edges up as MAS keeps policy unchanged", "https://www.straitstimes.com/sti", "", 150*time.Minute)},
		"Vietnam stocks VN-Index": {
			res("VN-Index hits 1,300 as foreign buying returns", "https://e.vnexpress.net/vn-index", "", 210*time.Minute),
			res("Vietnam travel: 10 beaches to visit", "https://travel.example/vn", "", 3*time.Hour), // off topic
		},
		"Indonesia stocks JCI Bank Indonesia": {res("JCI slides as rupiah weakens", "https://news.example/jci", "Bank Indonesia stepped in", 270*time.Minute)},
	}
	brave := newFakeBrave(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"results":[%s]}`, strings.Join(fixtures[r.URL.Query().Get("q")], ","))
	})
	var crawlMu sync.Mutex
	var crawled []string
	crawlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		crawlMu.Lock()
		defer crawlMu.Unlock()
		if r.URL.Path == "/crawl" {
			var body struct{ URLs []string }
			json.NewDecoder(r.Body).Decode(&body)
			crawled = append(crawled, body.URLs[0])
			fmt.Fprintf(w, `{"task_id":"%d"}`, len(crawled))
			return
		}
		fmt.Fprintf(w, `{"status":"completed","results":[{"markdown":"# Story\n\n%s"}]}`, strings.Repeat("Analysts said demand stayed firm through the week. ", 12))
	}))
	defer crawlSrv.Close()
	ai, model := llmtest.New(t, func(llmtest.Request) llmtest.Reply {
		return llmtest.Reply{Text: `{"headline":"Asia rallies as the BOJ hikes","summary":"Japan's Nikkei hit a record while bitcoin slid.",
			"bullets":[{"text":"Bitcoin slid on the BOJ hike","symbols":["BTC"],"sources":[1]},{"text":"The Nikkei hit a record","sources":[5]},
			           {"text":"The STI edged up","sources":[4]}],
			"sentiment":[{"symbol":"BTC","score":-0.3,"label":"bearish","reason":"BOJ hike"}],
			"regions":[{"region":"east_asia","summary":"The Nikkei closed at a record."},{"region":"southeast_asia","summary":"The STI edged up."}]}`}
	})
	s := New(Config{BraveAPIKey: "k", BraveBase: brave.srv.URL, CrawlBase: crawlSrv.URL, CrawlToken: "tok"}, pool, ai)
	s.brave.gap = 0
	s.crawl.pollDelay = time.Millisecond
	s.crawl.direct.lookupIP = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
	}
	s.crawl.direct.dialContext = func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("direct fetch blocked in test")
	}

	// Runs 1 and 3 search the regions too, run 2 only the assets and macro
	// topics (the cache is cleared so every search reaches Brave).
	for i, want := range []int32{36, 54, 90} {
		s.mu.Lock()
		clear(s.cache)
		s.mu.Unlock()
		if err := s.Ingest(ctx); err != nil {
			t.Fatal(err)
		}
		if n := brave.calls.Load(); n != want {
			t.Fatalf("run %d: %d Brave calls, want %d", i+1, n, want)
		}
		if i == 0 {
			// Run 1 read BTC's story, then two unread per region, known outlets first.
			crawlMu.Lock()
			got := slices.Clone(crawled)
			crawlMu.Unlock()
			slices.Sort(got)
			if want := []string{"https://asia.nikkei.com/markets/nikkei", "https://e.vnexpress.net/vn-index", "https://news.example/btc-boj",
				"https://www.cnbc.com/mag7", "https://www.koreaherald.com/samsung", "https://www.straitstimes.com/sti"}; !slices.Equal(got, want) {
				t.Errorf("run 1 read %q", got)
			}
		}
	}

	type row struct {
		Symbols, Regions []string
		Topic            string
	}
	rows := map[string]row{}
	rs, err := pool.Query(ctx, "select url, symbols, regions, topic from news_items")
	if err != nil {
		t.Fatal(err)
	}
	for rs.Next() {
		var u string
		var r row
		if err := rs.Scan(&u, &r.Symbols, &r.Regions, &r.Topic); err != nil {
			t.Fatal(err)
		}
		rows[u] = r
	}
	for u, want := range map[string]row{
		"https://news.example/btc-boj":           {[]string{"BTC"}, []string{"crypto", "east_asia"}, "asset"}, // merged; the first topic kept
		"https://news.example/bok":               {[]string{}, []string{"east_asia"}, "macro"},
		"https://asia.nikkei.com/markets/nikkei": {[]string{"TOKYOELEC"}, []string{"east_asia"}, "macro"}, // "Tokyo Electron led": a quote-feed stock
		"https://news.example/exporters":         {[]string{}, []string{"east_asia"}, "macro"},            // by its description
		"https://www.koreaherald.com/samsung":    {[]string{"SMSN"}, []string{"east_asia"}, "asset"},
		"https://www.straitstimes.com/sti":       {[]string{}, []string{"southeast_asia"}, "macro"},
		"https://e.vnexpress.net/vn-index":       {[]string{}, []string{"southeast_asia"}, "macro"},
		"https://news.example/jci":               {[]string{}, []string{"southeast_asia"}, "macro"},
		"https://www.cnbc.com/mag7":              {[]string{"NVDA", "TSLA"}, []string{"us"}, "asset"},
		"https://news.example/pk":                {[]string{}, []string{}, "macro"},
	} {
		if got, ok := rows[u]; !ok || !slices.Equal(got.Symbols, want.Symbols) || !slices.Equal(got.Regions, want.Regions) || got.Topic != want.Topic {
			t.Errorf("%s: %+v (found %t)", u, got, ok)
		}
	}
	if len(rows) != 10 {
		t.Errorf("the ramen, beach and European pages are skipped: %d rows", len(rows))
	}

	urls := func(f NewsFilter) (out []string) {
		t.Helper()
		items, err := s.News(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range items {
			out = append(out, strings.TrimPrefix(n.URL, "https://"))
		}
		return out
	}
	for f, want := range map[NewsFilter][]string{
		{Region: "east_asia"}:      {"news.example/btc-boj", "news.example/bok", "asia.nikkei.com/markets/nikkei", "news.example/exporters", "www.koreaherald.com/samsung"},
		{Region: "southeast_asia"}: {"www.straitstimes.com/sti", "e.vnexpress.net/vn-index", "news.example/jci"},
		{Region: "us"}:             {"www.cnbc.com/mag7"},
		{Region: "crypto"}:         {"news.example/btc-boj"},
		{Topic: "macro"}:           {"news.example/bok", "www.straitstimes.com/sti", "asia.nikkei.com/markets/nikkei", "e.vnexpress.net/vn-index", "news.example/exporters", "news.example/jci"}, // Pakistan stays hidden
		{Symbol: "SMSN"}:           {"www.koreaherald.com/samsung"},
	} {
		if got := urls(f); !slices.Equal(got, want) {
			t.Errorf("%+v: %q", f, got)
		}
	}
	if _, err := s.News(ctx, NewsFilter{Region: "asia"}); err == nil {
		t.Error("unknown region must fail")
	}

	// The brief saw every regional headline and keeps its region lines.
	reqs := model.Requests()
	if len(reqs) != 3 || !strings.Contains(reqs[0].System, "crypto, US and Asian markets") ||
		!strings.Contains(reqs[0].Prompt, "[1] Bitcoin slides as BOJ hike lifts the yen | news.example") ||
		!strings.Contains(reqs[0].Prompt, "regions: southeast_asia\n") || !strings.Contains(reqs[0].Prompt, "Regions with headlines: crypto, us, east_asia, southeast_asia.") {
		t.Fatalf("brief requests %d: %+v", len(reqs), reqs[0])
	}
	b, err := s.LatestBrief(ctx)
	if err != nil || b == nil {
		t.Fatalf("brief %+v %v", b, err)
	}
	if want := []RegionBrief{{"east_asia", "The Nikkei closed at a record."}, {"southeast_asia", "The STI edged up."}}; b.ArticleCount != 9 || !slices.Equal(b.Regions, want) {
		t.Errorf("brief %+v", b)
	}
	mux := http.NewServeMux()
	s.Register(mux, func(h http.Handler) http.Handler { return h })
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/news?region=southeast_asia&limit=2", nil))
	var list struct{ News []NewsItem }
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &list) != nil || len(list.News) != 2 || list.News[0].URL != "https://www.straitstimes.com/sti" {
		t.Errorf("region=southeast_asia: %d %s", rec.Code, rec.Body)
	}
}

package screener

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo/yahootest"
)

func env(t *testing.T) *apptest.Env { return apptest.New(t, Register, apptest.Options{}) }

// seed stores a computed row as the job would (status ok unless pct is nil).
func seed(t *testing.T, e *apptest.Env, ticker string, pct, prev, rsi *float64) {
	t.Helper()
	status := "ok"
	if pct == nil {
		status = "insufficient_history"
	}
	if _, err := e.DB.Exec(context.Background(), `insert into screener_rows (ticker, status, attempted_at, computed_at, yahoo_name,
		currency, price, price_at, fx_usd, fx_pair, fx_at, price_usd, weeks, first_week, sma200w, pct_vs_sma, prev_pct_vs_sma, rsi14w,
		ath, ath_at, drawdown_pct) values ($1, $2, now(), now(), 'Yahoo ' || $1, 'KRW', 1000, now(), 0.00075, 'KRWUSD=X', now(),
		0.75, 260, '2001-01-01', 1100, $3, $4, $5, 2000, '2021-01-04', -50)`, ticker, status, pct, prev, rsi); err != nil {
		t.Fatal(err)
	}
}

func f(v float64) *float64 { return &v }

type page struct {
	Rows []struct {
		Ticker, Name, ListName, Market, Status, Currency string
		StatusNote                                       *string
		Indices                                          []Membership
		PctVsSMA                                         *float64 `json:"pctVsSma"`
		Direction, TradableSymbol, TradeVia, TradeNote   *string
		PriceUSD                                         *float64 `json:"priceUsd"`
		FX                                               *FX
		RSI14w                                           *float64
		ATHAt, ATHFrom                                   *string
		UpdatedAt                                        *time.Time
	}
	NextCursor      *string
	Total           int
	Counts          map[string]int
	Markets         []MarketCount
	AsOf            *time.Time
	Run             *RunView
	LastCompletedAt *time.Time
	NextRunAt       time.Time
	Schedule        string
	PollSeconds     int
	Source          Source
	Method          string
}

func get(t *testing.T, e *apptest.Env, params url.Values) page {
	t.Helper()
	w := e.Do("GET", "/api/screener/asia?"+params.Encode(), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("%s: %d %s", params.Encode(), w.Code, w.Body)
	}
	return apptest.JSON[page](t, w)
}

func tickers(p page) []string {
	var out []string
	for _, r := range p.Rows {
		out = append(out, r.Ticker)
	}
	return out
}

// Before any run every constituent is listed: pending, or unavailable when
// Yahoo served none (the PSEi), with the lists' sources and the next run.
func TestAsiaBeforeAnyRun(t *testing.T) {
	e := env(t)
	if w := e.DoAs(nil, "GET", "/api/screener/asia", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("no session: %d", w.Code)
	}
	p := get(t, e, nil)
	if p.Total != 785 || len(p.Rows) != 50 || p.NextCursor == nil ||
		!maps.Equal(p.Counts, map[string]int{"all": 785, "below": 0, "deep": 0, "distressed": 0, "approaching": 0, "oversold": 0}) ||
		p.AsOf != nil || p.Run != nil || p.LastCompletedAt != nil || p.NextRunAt.Weekday() == time.Saturday || p.NextRunAt.Weekday() == time.Sunday ||
		p.NextRunAt.Hour() != 10 || p.NextRunAt.Minute() != 45 || p.PollSeconds != 60 || !strings.HasPrefix(p.Schedule, "Every weekday at 10:45 UTC") ||
		len(p.Source.Lists) != 10 || p.Source.Prices == "" || p.Source.FX == "" || p.Method == "" || len(p.Markets) != 10 ||
		p.Markets[0] != (MarketCount{Market{"JP", "Japan"}, 225}) || p.Markets[8].Count != 30 {
		t.Fatalf("%+v", p)
	}
	r := p.Rows[0]
	if r.Status != "pending" || r.StatusNote != nil || r.PctVsSMA != nil || r.Name != r.ListName || len(r.Indices) != 1 {
		t.Fatalf("pending row %+v", r)
	}
	ph := get(t, e, url.Values{"market": {"ph"}})
	if ph.Total != 30 || ph.Counts["all"] != 30 {
		t.Fatalf("PH %+v", ph)
	}
	for _, r := range ph.Rows {
		if r.Status != "unavailable" || r.StatusNote == nil || r.Market != "PH" {
			t.Fatalf("PSEi row %+v", r)
		}
	}
}

// The filter bands' edges, the market and search narrowing, and the counts
// per tab within them.
func TestAsiaFilters(t *testing.T) {
	e := env(t)
	for ticker, v := range map[string][2]float64{ // pct vs the 200WMA, RSI
		"005930.KS": {-5, 40}, "000660.KS": {-4.99, 40}, "005380.KS": {-50, 25}, "035420.KS": {-50.01, 50}, "373220.KS": {-70, 20},
		"7203.T": {-70.01, 31}, "6758.T": {0, 29}, "8306.T": {15, 50}, "9984.T": {15.01, 10}, "2330.TW": {-1, 29.99},
	} {
		seed(t, e, ticker, f(v[0]), nil, f(v[1]))
	}
	seed(t, e, "2317.TW", nil, nil, f(10)) // under 200 weeks: only "all"
	want := map[string][]string{
		"below":       {"005930.KS"},              // (−50, −5]
		"deep":        {"005380.KS", "035420.KS"}, // (−70, −50]
		"distressed":  {"373220.KS", "7203.T"},    // ≤ −70
		"approaching": {"6758.T", "8306.T"},       // [0, 15]
		"oversold":    {"005380.KS", "373220.KS", "2330.TW"},
	}
	all := get(t, e, nil)
	for filter, tick := range want {
		p := get(t, e, url.Values{"filter": {filter}})
		got := tickers(p)
		slices.Sort(got)
		slices.Sort(tick)
		if !slices.Equal(got, tick) || p.Total != len(tick) || all.Counts[filter] != len(tick) {
			t.Fatalf("%s: %v (total %d, count %d), want %v", filter, got, p.Total, all.Counts[filter], tick)
		}
	}
	kr := get(t, e, url.Values{"market": {"KR"}, "filter": {"below"}})
	if kr.Total != 1 || kr.Counts["all"] != 200 || kr.Counts["distressed"] != 1 || kr.Counts["deep"] != 2 || kr.Counts["approaching"] != 0 {
		t.Fatalf("KR %+v", kr.Counts)
	}
	for q, first := range map[string]string{"toyota motor": "7203.T", "7203": "7203.T", "005930.ks": "005930.KS", "Yahoo 2330": "2330.TW"} {
		if p := get(t, e, url.Values{"q": {q}}); p.Total != 1 || p.Rows[0].Ticker != first {
			t.Fatalf("q=%s: %v", q, tickers(p))
		}
	}
	if p := get(t, e, url.Values{"q": {"straits times"}}); p.Total != 30 {
		t.Fatalf("index search: %d", p.Total)
	}
	// A country finds its market's rows (gap round G9): by its name, or exactly by its code.
	per := map[string]int{}
	for _, m := range all.Markets {
		per[m.Code] = m.Count
	}
	for q, want := range map[string]string{"korea": "KR", "South Korea": "KR", "japan": "JP", "Taiwan": "TW", "kr": "KR"} {
		p, in := get(t, e, url.Values{"q": {q}}), get(t, e, url.Values{"q": {q}, "market": {want}})
		if in.Total != per[want] || per[want] < 30 || q != "kr" && p.Total != per[want] || p.Total < per[want] {
			t.Errorf("q=%s: %d of %d %s rows (total %d)", q, in.Total, per[want], want, p.Total)
		}
	}
	none := map[string]int{"all": 0, "below": 0, "deep": 0, "distressed": 0, "approaching": 0, "oversold": 0}
	if p := get(t, e, url.Values{"q": {"nothing like this"}}); p.Total != 0 || len(p.Rows) != 0 || p.NextCursor != nil || !maps.Equal(p.Counts, none) {
		t.Fatalf("no match %+v", p)
	}
}

// counts names every filter, seeded at 0, so a tab no row matches still reads
// 0 rather than missing (the web renders each tab's count from this map).
func TestCountsNameEveryFilter(t *testing.T) {
	raw, err := json.Marshal(Page{Rows: []Row{}, Counts: zeroCounts()})
	if err != nil || !strings.Contains(string(raw), `"counts":{"all":0,"approaching":0,"below":0,"deep":0,"distressed":0,"oversold":0}`) {
		t.Fatalf("%s %v", raw, err)
	}
	names := slices.Sorted(maps.Keys(filters))
	if !slices.Equal(names, slices.Sorted(slices.Values(filterNames))) {
		t.Fatalf("filters %v, names %v", names, filterNames)
	}
}

// Sorting is whitelisted, ascending or "-" descending, values before rows
// without one, ticker breaking ties; the cursor walks every row exactly once.
func TestAsiaSortAndCursor(t *testing.T) {
	e := env(t)
	seed(t, e, "7203.T", f(-30), f(-20), f(40))
	seed(t, e, "6758.T", f(-10), f(-12), f(55))
	seed(t, e, "005930.KS", f(-30), f(-31), f(35))
	p := get(t, e, url.Values{"limit": {"4"}})
	if got := tickers(p); !slices.Equal(got, []string{"005930.KS", "7203.T", "6758.T", "000080.KS"}) {
		t.Fatalf("default sort: %v", got)
	}
	if d := p.Rows[0].Direction; d == nil || *d != "recovering" || *p.Rows[1].Direction != "deepening" {
		t.Fatalf("direction %v %v", d, p.Rows[1].Direction)
	}
	p = get(t, e, url.Values{"sort": {"-pctVsSma"}, "limit": {"3"}})
	if got := tickers(p); !slices.Equal(got, []string{"6758.T", "005930.KS", "7203.T"}) {
		t.Fatalf("descending: %v", got)
	}
	p = get(t, e, url.Values{"sort": {"-direction"}, "limit": {"3"}})
	if got := tickers(p); !slices.Equal(got, []string{"6758.T", "005930.KS", "7203.T"}) {
		t.Fatalf("by direction: %v", got)
	}
	p = get(t, e, url.Values{"sort": {"ticker"}, "limit": {"2"}})
	if got := tickers(p); !slices.Equal(got, []string{"000080.KS", "0001.HK"}) {
		t.Fatalf("by ticker: %v", got)
	}
	for _, s := range []string{"name", "-market", "price", "priceUsd", "rsi", "-drawdown", "athAt", "-updated", "pctVsSma"} {
		seen := map[string]bool{}
		var cur string
		for pages := 0; ; pages++ {
			q := url.Values{"sort": {s}, "limit": {"100"}}
			if cur != "" {
				q.Set("cursor", cur)
			}
			p := get(t, e, q)
			for _, r := range p.Rows {
				if seen[r.Ticker] {
					t.Fatalf("sort %s: %s twice", s, r.Ticker)
				}
				seen[r.Ticker] = true
			}
			if p.NextCursor == nil {
				break
			}
			cur = *p.NextCursor
			if pages > 10 {
				t.Fatalf("sort %s: no end", s)
			}
		}
		if len(seen) != 785 {
			t.Fatalf("sort %s walked %d rows", s, len(seen))
		}
	}
	// A cursor belongs to its sort.
	first := get(t, e, url.Values{"sort": {"rsi"}, "limit": {"1"}})
	if w := e.Do("GET", "/api/screener/asia?sort=-rsi&cursor="+*first.NextCursor, nil); w.Code != 400 {
		t.Fatalf("cursor of another sort: %d", w.Code)
	}
	if got := get(t, e, url.Values{"sort": {"rsi"}, "cursor": {*first.NextCursor}, "limit": {"1"}}); got.Rows[0].Ticker != "7203.T" {
		t.Fatalf("second page by rsi: %v", tickers(got))
	}
}

func TestAsiaRejectsBadParams(t *testing.T) {
	e := env(t)
	for _, q := range []string{"filter=cheap", "market=US", "sort=price%20desc", "sort=1", "sort=--rsi", "limit=0", "limit=101", "limit=x",
		"cursor=!!!", "cursor=e30", "q=" + strings.Repeat("a", 65)} {
		w := e.Do("GET", "/api/screener/asia?"+q, nil)
		if w.Code != 400 || apptest.ErrorCode(t, w) != "bad_request" {
			t.Fatalf("%s: %d %s", q, w.Code, w.Body)
		}
	}
}

// A listing trades here when some instrument references its Yahoo ticker,
// found at request time (a named one, else its listed row), or through a US
// listing in the ADR map; the rest stay research only.
func TestAsiaTradable(t *testing.T) {
	e := env(t)
	byTicker := func(q string) page { return get(t, e, url.Values{"q": {q}}) }
	if r := byTicker("005930.KS").Rows[0]; r.TradableSymbol == nil || *r.TradableSymbol != "SMSN" || *r.TradeVia != "listing" ||
		*r.TradeNote != "KRX 005930 common share, KRW→USD" {
		t.Fatalf("Samsung %+v", r)
	}
	if r := byTicker("2330.TW").Rows[0]; r.TradableSymbol == nil || *r.TradableSymbol != "TSM" || *r.TradeVia != "adr" ||
		*r.TradeNote != "NYSE: TSM ADS = 5 TWSE 2330 shares" {
		t.Fatalf("TSMC %+v", r)
	}
	if r := byTicker("9988.HK").Rows[0]; r.TradableSymbol == nil || *r.TradableSymbol != "BABA" {
		t.Fatalf("Alibaba %+v", r)
	}
	// A listed constituent trades under its own ticker.
	if r := byTicker("1332.T").Rows[0]; r.TradableSymbol == nil || *r.TradableSymbol != "1332.T" || *r.TradeVia != "listing" ||
		*r.TradeNote != "TSE: 1332 (Nikkei 225), JPY→USD at Yahoo FX" {
		t.Fatalf("listed %+v", r)
	}
	// A research-only row says why in tradeNote.
	for out, why := range map[string]string{
		"268280.KS": "Trades under $1M a day (30-day mean $127k on 2026-10-13)",
		"MER.PS":    "No keyless Yahoo data for this listing (Yahoo answered 404 on 2026-10-13)",
		"BANPU.BK":  "Yahoo's daily history has gaps (1 daily bar in the month to 2026-10-13)",
		"0126Z0.KS": "Listed under a year ago (first trade 2025-11-25)",
	} {
		if r := byTicker(out).Rows[0]; r.TradableSymbol != nil || r.TradeVia != nil || r.TradeNote == nil || *r.TradeNote != why {
			t.Fatalf("%s is research only: %+v", out, r)
		}
	}
	// The quote feeds' local listings trade directly; a local line whose ADR is the universe row, through it.
	for ticker, want := range map[string][2]string{"D05.SI": {"DBS", "listing"}, "0700.HK": {"TENCENT", "listing"}, "2382.TW": {"QUANTA", "listing"},
		"000270.KS": {"KIA", "listing"}, "285A.T": {"KIOXIA", "listing"}, "2412.TW": {"CHT", "adr"}, "015760.KS": {"KEP", "adr"}, "034220.KS": {"LPL", "adr"},
		"7203.T": {"TM", "adr"}, "TLKM.JK": {"TLKM", "listing"}, "TEL.PS": {"PHI", "adr"}, "6501.T": {"6501.T", "listing"}, "C38U.SI": {"C38U.SI", "listing"}} {
		if r := byTicker(ticker).Rows[0]; r.TradableSymbol == nil || *r.TradableSymbol != want[0] || *r.TradeVia != want[1] {
			t.Errorf("%s: %+v, want %v", ticker, r, want)
		}
	}
	// Every constituent: each listed one under its own ticker, the ADR rows
	// as before, and no PSEi row but PLDT's.
	trade := tradables()
	count := map[string]int{}
	for _, st := range Stocks() {
		tr := trade(st.Ticker)
		switch {
		case tr == nil:
			count["research"]++
			if _, ok := market.ListedExcluded(st.Ticker); !ok {
				t.Errorf("%s is research only with no reason", st.Ticker)
			}
		case tr.Symbol == st.Ticker:
			count["own"]++
			if a, ok := market.Lookup(st.Ticker); !ok || !a.IsListed() || tr.Via != "listing" || tr.Note != a.Listing.Underlying {
				t.Errorf("%s: %+v", st.Ticker, tr)
			}
		default:
			count[tr.Via]++
		}
		if st.Market == "PH" && tr != nil && st.Ticker != "TEL.PS" {
			t.Errorf("PSEi %s trades as %+v", st.Ticker, tr)
		}
	}
	if count["own"] != 604 || count["listing"] != 94 || count["adr"] != 24 || count["research"] != 63 {
		t.Fatalf("counts %v", count)
	}
	// An instrument another package adds is picked up without a list here.
	saved := market.Universe
	t.Cleanup(func() { market.Universe = saved })
	market.Universe = append(slices.Clone(saved), market.AssetInfo{Symbol: "MIWON", Pair: "268280.KS", Name: "Miwon", Class: market.Stock,
		Venue: "yahoo", Listing: &market.Listing{Region: "asia", SessionVenue: "KRX", Underlying: "KRX: 268280", LocalCurrency: "KRW", Yahoo: "268280.KS"}})
	if r := byTicker("268280.KS").Rows[0]; r.TradableSymbol == nil || *r.TradableSymbol != "MIWON" || *r.TradeVia != "listing" {
		t.Fatalf("added instrument %+v", r)
	}
}

// After a run the page carries it, the newest computed time, the USD price
// with its FX rate, and a failed refresh keeps the earlier values with its
// reason.
func TestAsiaAfterARun(t *testing.T) {
	e := env(t)
	e.Yahoo.Set(weekly("1332.T", series(230, func(i int) float64 { return 800 + float64(i%9) })...))
	e.Yahoo.SetFX("JPY", 0.0063)
	if _, err := refresh(context.Background(), e.Deps, fast(2)); err != nil {
		t.Fatal(err)
	}
	p := get(t, e, url.Values{"q": {"1332.T"}})
	r := p.Rows[0]
	if p.Run == nil || p.Run.Status != "completed" || p.Run.Tickers != 2 || p.Run.OK != 1 || p.Run.Unavailable != 1 || p.LastCompletedAt == nil ||
		p.AsOf == nil || r.Status != "ok" || r.Name != "Test 1332.T" || r.ListName != "NISSUI CORP." || r.Currency != "JPY" ||
		r.FX == nil || r.FX.Pair != "JPYUSD=X" || r.FX.USDPerUnit != 0.0063 || r.PriceUSD == nil || r.UpdatedAt == nil {
		t.Fatalf("%+v %+v", p.Run, r)
	}
	// Weekly bars open at Monday 00:00 in Tokyo (Sunday in UTC): dates are the exchange's.
	first := lastWeek.AddDate(0, 0, -7*229).In(time.FixedZone("JST", 9*3600)).Format(time.DateOnly)
	if ath, _ := time.Parse(time.DateOnly, *r.ATHAt); ath.Weekday() != time.Monday || *r.ATHFrom != first {
		t.Fatalf("ath %s from %s, want a Monday from %s", *r.ATHAt, *r.ATHFrom, first)
	}
	if r := get(t, e, url.Values{"q": {"1605.T"}}).Rows[0]; r.Status != "unavailable" || *r.StatusNote != "Yahoo serves no data for this ticker" {
		t.Fatalf("not served %+v", r)
	}
	e.Yahoo.SetErr("1332.T", yahootest.Down)
	if _, err := refresh(context.Background(), e.Deps, fast(1)); err != nil {
		t.Fatal(err)
	}
	if again := get(t, e, url.Values{"q": {"1332.T"}}).Rows[0]; again.Status != "failed" || *again.StatusNote != "Yahoo answered HTTP 503" ||
		*again.PctVsSMA != *r.PctVsSMA || !again.UpdatedAt.Equal(*r.UpdatedAt) {
		t.Fatalf("after a failed refresh %+v", again)
	}
}

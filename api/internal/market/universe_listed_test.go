package market

import (
	"context"
	"encoding/json"
	"maps"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo/yahootest"
)

// The listed rows: 604 quote feeds in Asia under their own Yahoo ticker, each
// venue in its currency, the underlying line with its FX note, and none
// colliding with a named instrument.
func TestListedCatalog(t *testing.T) {
	if len(Listed) != 604 || len(listedExcluded) != 63 {
		t.Fatalf("%d listed, %d excluded", len(Listed), len(listedExcluded))
	}
	suffix := map[string]string{".T": TSE, ".KS": KRX, ".HK": HKEX, ".TW": TWSE, ".TWO": TWSE /* TPEx keeps the TWSE session */, ".SI": SGX, ".JK": IDX, ".BK": SET, ".KL": BURSA, ".VN": HOSE}
	named := map[string]bool{}
	for _, a := range Universe {
		named[a.Symbol] = true
		if a.Listing != nil && a.Listing.Yahoo != "" {
			named[strings.ToUpper(a.Listing.Yahoo)] = true
		}
	}
	re := regexp.MustCompile(ListedPattern)
	perVenue := map[string]int{}
	for _, a := range Listed {
		l := a.Listing
		venue := suffix[a.Symbol[strings.LastIndex(a.Symbol, "."):]]
		if !a.QuoteFeed() || !a.Perp() || a.Class != Stock || l == nil || l.Region != "asia" || a.Symbol != a.Pair || l.Yahoo != a.Symbol ||
			l.SessionVenue != venue || !re.MatchString(a.Symbol) || a.Name == "" || len(l.Underlying) > 120 || !a.IsListed() {
			t.Fatalf("row %+v %+v", a, l)
		}
		if l.LocalCurrency == "USD" && (venue != SGX || !strings.HasSuffix(l.Underlying, "quoted in USD (no FX)")) ||
			l.LocalCurrency != "USD" && !strings.HasSuffix(l.Underlying, l.LocalCurrency+"→USD at Yahoo FX") {
			t.Fatalf("%s: %q in %s", a.Symbol, l.Underlying, l.LocalCurrency)
		}
		if !slices.Contains(listedVenues[venue], l.LocalCurrency) {
			t.Fatalf("%s quotes in %s", a.Symbol, l.LocalCurrency)
		}
		if named[a.Symbol] {
			t.Fatalf("%s collides with a named instrument", a.Symbol)
		}
		perVenue[venue]++
	}
	want := map[string]int{TSE: 213, KRX: 145, HKEX: 83, SET: 44, IDX: 35, TWSE: 26, BURSA: 26, HOSE: 23, SGX: 9}
	for v, n := range want {
		if perVenue[v] != n {
			t.Errorf("%s: %d rows, want %d", v, perVenue[v], n)
		}
	}
	if h := Listed[slices.IndexFunc(Listed, func(a AssetInfo) bool { return a.Symbol == "6501.T" })]; h.Name != "Hitachi, Ltd." ||
		h.Listing.Underlying != "TSE: 6501 (Nikkei 225), JPY→USD at Yahoo FX" || h.Listing.LocalCurrency != "JPY" {
		t.Fatalf("6501.T %+v %+v", h, h.Listing)
	}
	for _, a := range Universe {
		if a.IsListed() {
			t.Fatalf("named %s reads as listed", a.Symbol)
		}
	}
}

// Lookup finds a listed row by its ticker in any case; a research-only
// constituent is not an instrument, and says why.
func TestLookupListed(t *testing.T) {
	a, ok := Lookup(" 6501.t ")
	if !ok || a.Symbol != "6501.T" || !a.QuoteFeed() || !a.IsListed() {
		t.Fatalf("6501.t: %+v %v", a, ok)
	}
	for _, out := range []string{"MER.PS", "268280.KS", "BANPU.BK", "6501", "T"} {
		if _, ok := Lookup(out); ok {
			t.Fatalf("%s resolved", out)
		}
	}
	if r, ok := ListedExcluded("mer.ps"); !ok || r != "No keyless Yahoo data for this listing (Yahoo answered 404 on 2026-10-13)" {
		t.Fatalf("MER.PS: %q %v", r, ok)
	}
	if r, ok := ListedExcluded("268280.KS"); !ok || r != "Trades under $1M a day (30-day mean $127k on 2026-10-13)" {
		t.Fatalf("268280.KS: %q", r)
	}
	if _, ok := ListedExcluded("6501.T"); ok {
		t.Fatal("a listed ticker has no exclusion")
	}
	if _, ok := LookupCrypto("6501.T"); ok {
		t.Fatal("a listed row resolved as crypto")
	}
	// ByYahoo: the named instrument of a configured listing first, else the listed row.
	for ticker, want := range map[string]string{"D05.SI": "DBS", "005930.KS": "SMSN", "6501.t": "6501.T", "HSDT": "HSDT"} {
		if a, ok := ByYahoo(ticker); !ok || a.Symbol != want {
			t.Errorf("ByYahoo(%s) = %s %v, want %s", ticker, a.Symbol, ok, want)
		}
	}
	if _, ok := ByYahoo("MER.PS"); ok {
		t.Error("ByYahoo(MER.PS)")
	}
	if _, ok := ByYahoo(" "); ok {
		t.Error("ByYahoo of nothing")
	}
	// Instruments is the named universe then the listed rows, in a new slice.
	all := Instruments()
	if len(all) != 849 || !slices.Equal(all[:len(Universe)], Universe) || !slices.Equal(all[len(Universe):], Listed) {
		t.Fatalf("%d instruments", len(all))
	}
	all[0].Symbol = "X"
	if Universe[0].Symbol != "BTC" {
		t.Fatal("Instruments shares Universe's array")
	}
}

// The named universe, every prompt and tool enum built from it, stays exactly
// as before the listed rows.
func TestNamedUniverseUnchanged(t *testing.T) {
	golden, err := os.ReadFile("testdata/symbollist-245.golden")
	if err != nil {
		t.Fatal(err)
	}
	if len(Universe) != 245 || len(QuoteFeeds) != 138 || len(CryptoUniverse) != 69 || len(StockPerps) != 38 || SymbolList() != string(golden) {
		t.Fatalf("%d named, %d quote feeds; SymbolList %q", len(Universe), len(QuoteFeeds), SymbolList())
	}
	if len(feedRows()) != 742 || !slices.Equal(feedRows()[:138], QuoteFeeds) {
		t.Fatalf("%d feed rows", len(feedRows()))
	}
}

// The loader refuses a listed.json that would break an instrument.
func TestLoadListedRejects(t *testing.T) {
	row := func(sym, venue, ccy, underlying string) string {
		b, _ := json.Marshal(map[string]string{"symbol": sym, "name": "N", "venue": venue, "currency": ccy, "underlying": underlying})
		return string(b)
	}
	good := row("6501.T", TSE, "JPY", "TSE: 6501, JPY→USD at Yahoo FX")
	named := []AssetInfo{{Symbol: "DBS", Pair: "D05.SI", Listing: &Listing{Yahoo: "D05.SI"}}}
	if rows, by, idx, _, err := loadListed([]byte(`{"rows":[`+good+`],"excluded":[{"ticker":"MER.PS","reason":"no data"}]}`), named); err != nil ||
		len(rows) != 1 || by["6501.T"] != 0 || len(idx) != 1 {
		t.Fatalf("good: %v", err)
	}
	for name, raw := range map[string]string{
		"not json":          `{`,
		"bad ticker":        `{"rows":[` + row("6501.PS", TSE, "JPY", "x JPY→USD at Yahoo FX") + `]}`,
		"lower case":        `{"rows":[` + row("abc.T", TSE, "JPY", "x JPY→USD at Yahoo FX") + `]}`,
		"duplicate":         `{"rows":[` + good + `,` + good + `]}`,
		"named listing":     `{"rows":[` + row("D05.SI", SGX, "SGD", "x SGD→USD at Yahoo FX") + `]}`,
		"unknown venue":     `{"rows":[` + row("6501.T", "OSE", "JPY", "x JPY→USD at Yahoo FX") + `]}`,
		"currency":          `{"rows":[` + row("6501.T", TSE, "USD", "x quoted in USD (no FX)") + `]}`,
		"long underlying":   `{"rows":[` + row("6501.T", TSE, "JPY", strings.Repeat("x", 120)+"JPY→USD at Yahoo FX") + `]}`,
		"no FX note":        `{"rows":[` + row("6501.T", TSE, "JPY", "TSE: 6501") + `]}`,
		"no reason":         `{"rows":[],"excluded":[{"ticker":"MER.PS","reason":""}]}`,
		"excluded and rows": `{"rows":[` + good + `],"excluded":[{"ticker":"6501.T","reason":"thin"}]}`,
	} {
		if _, _, _, _, err := loadListed([]byte(raw), named); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
	defer func() {
		if recover() == nil {
			t.Error("mustLoadListed did not panic")
		}
	}()
	mustLoadListed([]byte(`{`), nil)
}

// A priced listed row follows the named rows in Assets; the assets list keeps
// answering the named rows only.
func TestAssetsListedRows(t *testing.T) {
	c, f := feedClient(t)
	now := time.Now().UTC().Truncate(time.Second)
	h := yahootest.Daily("6501.T", "JPY", 4800, 4850)
	h.Meta.PriceTime, h.Meta.Timezone = &now, "Asia/Tokyo"
	f.Set(h)
	f.SetFX("JPY", 0.0066)
	assets, err := c.Assets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	last := assets[len(assets)-1]
	if len(assets) != len(Universe)-len(QuoteFeeds)+4 || last.Symbol != "6501.T" || last.Pair != "6501.T" || !approxEq(last.Price, 4850*0.0066) ||
		last.QuoteFeed == nil || *last.SessionVenue != TSE || *last.Underlying != "TSE: 6501 (Nikkei 225), JPY→USD at Yahoo FX" || *last.Region != "asia" {
		t.Fatalf("%d assets, last %+v", len(assets), last)
	}
	if q, err := c.Quote(context.Background(), "6501.t"); err != nil || q.Symbol != "6501.T" {
		t.Fatalf("quote %+v %v", q, err)
	}
	w := get(mount(c), context.Background(), "/api/market/assets")
	if w.Code != 200 || strings.Contains(w.Body.String(), `"symbol":"6501.T"`) || !strings.Contains(w.Body.String(), `"symbol":"DBS"`) {
		t.Fatalf("assets list %d", w.Code)
	}
	if w := get(mount(c), context.Background(), "/api/market/candles?symbol=6501.t&interval=1d&limit=2"); w.Code != 200 {
		t.Fatalf("listed candles %d %s", w.Code, w.Body)
	}
}

// BenchmarkAssets849 is a cached Assets read with every quote feed priced:
// the per-call cost is the session projection of 849 rows (under 2 ms).
func BenchmarkAssets849(b *testing.B) {
	srv := markettest.New(b)
	c := testMarketClient(srv.URL)
	f := yahootest.New()
	now := time.Now().UTC().Truncate(time.Second)
	ccys := map[string]bool{}
	for _, a := range feedRows() {
		ch := yahootest.Daily(a.Pair, a.Listing.LocalCurrency, 10, 11)
		ch.Meta.PriceTime = &now
		f.Set(ch)
		ccys[a.Listing.LocalCurrency] = true
	}
	for ccy := range ccys {
		if ccy != "USD" {
			f.SetFX(ccy, 0.01)
		}
	}
	c.Yahoo = f
	ctx := context.Background()
	assets, err := c.Assets(ctx)
	if err != nil || len(assets) != 849 {
		b.Fatalf("%d assets %v", len(assets), err)
	}
	b.ResetTimer()
	for b.Loop() {
		if _, err := c.Assets(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

var _ yahoo.Source = (*yahootest.Fake)(nil)

func symbolsOf(t *testing.T, body []byte) []string {
	t.Helper()
	var page struct{ Assets []Asset }
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(page.Assets))
	for i, a := range page.Assets {
		out[i] = a.Symbol
	}
	return out
}

// GET /api/market/assets: no scope is the named rows, exactly as before;
// scope=named adds with='s listed rows; scope=listed is the listed rows;
// halted rows are left out every time.
func TestAssetsScope(t *testing.T) {
	c, f := feedClient(t)
	now := time.Now().UTC().Truncate(time.Second)
	for _, sym := range []string{"6501.T", "000810.KS"} {
		ch := yahootest.Daily(sym, map[bool]string{true: "JPY", false: "KRW"}[strings.HasSuffix(sym, ".T")], 100, 101)
		ch.Meta.PriceTime = &now
		f.Set(ch)
	}
	old := now.Add(-QuoteStaleAfter - time.Hour)
	stale := yahootest.Daily("0005.HK", "HKD", 100, 101)
	stale.Meta.PriceTime = &old
	f.Set(stale)
	f.SetFX("JPY", 0.0066)
	f.SetFX("KRW", 0.00072)
	f.SetFX("HKD", 0.128)
	mux, ctx := mount(c), context.Background()
	c.Touch("6501.T", "000810.KS", "0005.HK") // read in the first refresh
	plain := get(mux, ctx, "/api/market/assets")
	named := symbolsOf(t, plain.Body.Bytes())
	if plain.Code != 200 || len(named) != len(Universe)-len(QuoteFeeds)+3 || slices.ContainsFunc(named, func(s string) bool { return strings.Contains(s, ".") }) {
		t.Fatalf("no scope: %d %v", plain.Code, named[len(named)-3:])
	}
	if w := get(mux, ctx, "/api/market/assets?scope=named"); w.Code != 200 || !slices.Equal(symbolsOf(t, w.Body.Bytes()), named) {
		t.Fatalf("scope=named differs from no scope: %d", w.Code)
	}
	w := get(mux, ctx, "/api/market/assets?scope=named&with=6501.t,0005.HK,MER.PS,BTC,6501.T")
	got := symbolsOf(t, w.Body.Bytes())
	if w.Code != 200 || !slices.Equal(got[:len(named)], named) || !slices.Equal(got[len(named):], []string{"6501.T"}) {
		t.Fatalf("with: %d %v", w.Code, got[len(named):])
	}
	if w := get(mux, ctx, "/api/market/assets?scope=listed"); w.Code != 200 || !slices.Equal(symbolsOf(t, w.Body.Bytes()), []string{"6501.T", "000810.KS"}) {
		t.Fatalf("listed: %d %s", w.Code, w.Body)
	}
	// A with= row read over two minutes ago is read again before the answer.
	c.feeds.mu.Lock()
	q := c.feeds.quotes["000810.KS"]
	q.at = q.at.Add(-3 * time.Minute)
	c.feeds.quotes["000810.KS"] = q
	c.feeds.mu.Unlock()
	reads := f.Calls("000810.KS")
	if w := get(mux, ctx, "/api/market/assets?scope=named&with=000810.KS"); w.Code != 200 || f.Calls("000810.KS") != reads+1 {
		t.Fatalf("with= re-read: %d, %d reads", w.Code, f.Calls("000810.KS")-reads)
	}
	for _, bad := range []string{"?scope=all", "?with=6501.T", "?scope=listed&with=6501.T",
		"?scope=named&with=" + strings.Repeat("6501.T,", 20) + "6501.T"} {
		if w := get(mux, ctx, "/api/market/assets"+bad); w.Code != 400 {
			t.Errorf("%s: %d", bad, w.Code)
		}
	}
	// The candles of a listed row touch it.
	c.feeds.mu.Lock()
	c.feeds.touched = nil
	c.feeds.mu.Unlock()
	w = get(mux, ctx, "/api/market/analysis?symbol=000810.ks&interval=1d")
	c.feeds.mu.Lock()
	hot := c.feeds.hot("000810.KS", c.now())
	c.feeds.mu.Unlock()
	if w.Code == 400 || !hot {
		t.Fatalf("analysis did not touch: %d", w.Code)
	}
}

// GET /api/market/instruments: all 849, priced or not, with an ETag and an
// hour's private cache; a matching If-None-Match is 304.
func TestInstruments(t *testing.T) {
	c := testMarketClient("")
	mux, ctx := mount(c), context.Background()
	w := get(mux, ctx, "/api/market/instruments")
	var page struct {
		Instruments []map[string]any
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || w.Code != 200 || len(page.Instruments) != 849 {
		t.Fatalf("%d %d %v", w.Code, len(page.Instruments), err)
	}
	keys := "class index listed localCurrency name pair region sessionVenue symbol underlying venue yahoo"
	for _, row := range []map[string]any{page.Instruments[0], page.Instruments[300]} {
		got := slices.Sorted(maps.Keys(row))
		if strings.Join(got, " ") != keys {
			t.Fatalf("keys %v", got)
		}
	}
	row := func(sym string) map[string]any {
		return page.Instruments[slices.IndexFunc(page.Instruments, func(r map[string]any) bool { return r["symbol"] == sym })]
	}
	btc, smsn, dbs, h := page.Instruments[0], row("SMSN"), row("DBS"), row("6501.T")
	if btc["symbol"] != "BTC" || btc["region"] != nil || btc["yahoo"] != nil || btc["listed"] != false || btc["index"] != nil {
		t.Fatalf("BTC %v", btc)
	}
	if smsn["symbol"] != "SMSN" || smsn["yahoo"] != "005930.KS" || smsn["venue"] != "hyperliquid" {
		t.Fatalf("SMSN %v", smsn)
	}
	if dbs["venue"] != "yahoo" || dbs["listed"] != false || dbs["index"] != nil || dbs["sessionVenue"] != "SGX" {
		t.Fatalf("DBS %v", dbs)
	}
	if h["pair"] != "6501.T" || h["name"] != "Hitachi, Ltd." || h["listed"] != true || h["index"] != "Nikkei 225" || h["region"] != "asia" ||
		h["localCurrency"] != "JPY" || h["underlying"] != "TSE: 6501 (Nikkei 225), JPY→USD at Yahoo FX" || h["yahoo"] != "6501.T" || h["class"] != "stock" {
		t.Fatalf("6501.T %v", h)
	}
	etag := w.Header().Get("ETag")
	if w.Header().Get("Cache-Control") != "private, max-age=3600" || !strings.HasSuffix(etag, `-849"`) || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("headers %v", w.Header())
	}
	r := httptest.NewRequestWithContext(ctx, "GET", "/api/market/instruments", nil)
	r.Header.Set("X-Test-User", "u")
	r.Header.Set("If-None-Match", `"other", `+etag)
	nm := httptest.NewRecorder()
	mux.ServeHTTP(nm, r)
	if nm.Code != 304 || nm.Body.Len() != 0 || nm.Header().Get("ETag") != etag {
		t.Fatalf("If-None-Match: %d", nm.Code)
	}
	r = httptest.NewRequest("GET", "/api/market/instruments", nil)
	anon := httptest.NewRecorder()
	mux.ServeHTTP(anon, r)
	if anon.Code != 401 {
		t.Fatalf("signed out: %d", anon.Code)
	}
	if idx, ok := ListedIndex("BTC"); ok || idx != "" {
		t.Fatal("BTC has a listed index")
	}
}

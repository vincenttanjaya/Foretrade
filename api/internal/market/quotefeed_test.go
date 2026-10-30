package market

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo/yahootest"
)

// The quote-feed rows follow every perp, each with its Yahoo listing, and
// collide with nothing (crypto included).
func TestQuoteFeedCatalog(t *testing.T) {
	if len(QuoteFeeds) != 67+71 || !slices.Equal(Universe[len(Universe)-len(QuoteFeeds):], QuoteFeeds) {
		t.Fatalf("%d quote feeds, appended %v", len(QuoteFeeds), Universe[len(Universe)-len(QuoteFeeds)].Symbol)
	}
	if slices.ContainsFunc(StockPerps, AssetInfo.QuoteFeed) || slices.ContainsFunc(Universe[:len(Universe)-len(QuoteFeeds)], AssetInfo.QuoteFeed) {
		t.Fatal("a quote feed among the perps")
	}
	taken := map[string]bool{} // every earlier symbol and its venue's base name (BTC of BTCUSDT, NVDA of xyz:NVDA)
	for _, a := range Universe[:len(Universe)-len(QuoteFeeds)] {
		taken[a.Symbol], taken[strings.TrimSuffix(strings.TrimPrefix(a.Pair, "xyz:"), "USDT")] = true, true
	}
	tickers := map[string]bool{} // and no Yahoo listing is configured twice
	for _, a := range Universe[:len(Universe)-len(QuoteFeeds)] {
		if a.Listing != nil && a.Listing.Yahoo != "" {
			tickers[a.Listing.Yahoo] = true
		}
	}
	for _, a := range QuoteFeeds {
		if taken[a.Symbol] || tickers[a.Pair] {
			t.Fatalf("%s collides", a.Symbol)
		}
		taken[a.Symbol], tickers[a.Pair] = true, true
	}
	currency := map[string]string{SGX: "SGD", IDX: "IDR", SET: "THB", BURSA: "MYR", HOSE: "VND", HKEX: "HKD", TSE: "JPY", KRX: "KRW", TWSE: "TWD", NYSE: "USD"}
	for _, a := range QuoteFeeds {
		l := a.Listing
		if !a.QuoteFeed() || !a.Perp() || a.Class != Stock || l == nil || l.Yahoo != a.Pair || len(l.Underlying) == 0 || len(l.Underlying) > 120 ||
			currency[l.SessionVenue] != l.LocalCurrency && !(l.SessionVenue == SGX && l.LocalCurrency == "USD") || (l.Region == "us") != (a.Symbol == "HSDT") ||
			l.Region != "us" && l.Region != "asia" {
			t.Fatalf("row %+v %+v", a, l)
		}
		if _, ok := yahoo.Normalize(a.Pair); !ok {
			t.Fatalf("%s is not a Yahoo ticker", a.Pair)
		}
		if l.LocalCurrency != "USD" && !strings.HasSuffix(l.Underlying, l.LocalCurrency+"→USD at Yahoo FX") {
			t.Fatalf("%s underlying %q has no FX note", a.Symbol, l.Underlying)
		}
		if got, ok := Lookup(strings.ToLower(a.Symbol)); !ok || got.Symbol != a.Symbol {
			t.Fatalf("lookup %s", a.Symbol)
		}
		if _, ok := LookupCrypto(a.Symbol); ok {
			t.Fatalf("%s resolved as crypto", a.Symbol)
		}
	}
}

// The Singapore, Taiwan and Korea focus rows follow the first 67 in the
// measured order. US-listed issuers (the Nasdaq ones too) follow the NYSE
// session in USD, and SGX lines SGX quotes in USD need no FX.
func TestQuoteFeedSGTWKR(t *testing.T) {
	rows := QuoteFeeds[67:]
	count := map[string]int{}
	for _, a := range rows {
		switch l := a.Listing; {
		case strings.HasSuffix(a.Pair, ".SI"):
			count["SG"]++
		case strings.HasSuffix(a.Pair, ".TW"):
			count["TW"]++
		case strings.HasSuffix(a.Pair, ".KS"):
			count["KR"]++
		case l.SessionVenue == NYSE && l.LocalCurrency == "USD":
			count["US"]++
		default:
			t.Fatalf("row %+v %+v", a, l)
		}
	}
	if count["SG"] != 18 || count["TW"] != 22 || count["KR"] != 22 || count["US"] != 9 || rows[0].Symbol != "KEPPEL" || rows[len(rows)-1].Symbol != "LPL" {
		t.Fatalf("rows %v, first %s last %s", count, rows[0].Symbol, rows[len(rows)-1].Symbol)
	}
	for _, c := range []struct{ symbol, venue, ccy string }{
		{"KARO", NYSE, "USD"}, {"KLIC", NYSE, "USD"}, {"HIMX", NYSE, "USD"}, {"SIMO", NYSE, "USD"}, {"CHT", NYSE, "USD"},
		{"JARDINE", SGX, "USD"}, {"HKLAND", SGX, "USD"}, {"SIA", SGX, "SGD"}, {"QUANTA", TWSE, "TWD"}, {"SAMBIO", KRX, "KRW"},
	} {
		a, ok := Lookup(c.symbol)
		if !ok || !a.QuoteFeed() || a.Listing.SessionVenue != c.venue || a.Listing.LocalCurrency != c.ccy {
			t.Fatalf("%s %+v", c.symbol, a.Listing)
		}
	}
	for _, out := range []string{"402340.KS", "017670.KS", "C38U.SI", "FLEX"} { // SK Square, SK Telecom's local line, a REIT, a US company
		if slices.ContainsFunc(Universe, func(a AssetInfo) bool { return a.Pair == out }) {
			t.Fatalf("%s is excluded", out)
		}
	}
}

// feedClient is a market client over the fake Binance/Hyperliquid and a
// Yahoo fake holding DBS (SGD), HSDT (USD) and BBCA (IDR, a venue without a
// verified calendar, whose state comes from Yahoo's trading period).
func feedClient(t *testing.T) (*Client, *yahootest.Fake) {
	t.Helper()
	b := markettest.New(t)
	c := testMarketClient(b.URL)
	f := yahootest.New()
	now := time.Now().UTC().Truncate(time.Second)
	dbs := yahootest.Daily("D05.SI", "SGD", 76, 77.83, 78.37)
	dbs.Meta.Timezone, dbs.Meta.ExchangeName, dbs.Meta.FullExchangeName = "Asia/Singapore", "SES", "SES"
	prev, hi, lo, vol := 77.83, 78.42, 77.51, 2.5e6
	dbs.Meta.PriceTime, dbs.Meta.ChartPreviousClose, dbs.Meta.DayHigh, dbs.Meta.DayLow, dbs.Meta.DayVolume = &now, &prev, &hi, &lo, &vol
	f.Set(dbs)
	hsdt := yahootest.Daily("HSDT", "USD", 2.5, 2.65)
	hsdt.Meta.PriceTime = &now
	f.Set(hsdt)
	bbca := yahootest.Daily("BBCA.JK", "IDR", 6100, 6175)
	start, end := now.Add(-time.Hour), now.Add(time.Hour)
	bbca.Meta.PriceTime, bbca.Meta.RegularStart, bbca.Meta.RegularEnd = &now, &start, &end
	f.Set(bbca)
	f.SetFX("SGD", 0.78)
	f.SetFX("IDR", 0.00005588)
	c.Yahoo = f
	return c, f
}

func TestQuoteFeedAssets(t *testing.T) {
	c, f := feedClient(t)
	assets, err := c.Assets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Asset{}
	for _, a := range assets {
		by[a.Symbol] = a
	}
	if len(assets) != len(Universe)-len(QuoteFeeds)+3 {
		t.Fatalf("%d assets: only the three Yahoo serves join the perps and crypto", len(assets))
	}
	dbs := by["DBS"]
	q := dbs.QuoteFeed
	if q == nil || dbs.Venue != "yahoo" || dbs.Pair != "D05.SI" || !approxEq(dbs.Price, 78.37*0.78) || q.LocalPrice != 78.37 || q.FX != 0.78 ||
		q.FXSymbol != "SGDUSD=X" || q.FXAsOf == nil || q.Currency != "SGD" || q.Exchange != "SES" || q.Stale || dbs.Halted {
		t.Fatalf("DBS %+v %+v", dbs, q)
	}
	if dbs.Change24hPct != 0.694 || *q.PreviousClose != 77.83 || dbs.Range24hSource != "session" || !approxEq(dbs.High24h, 78.42*0.78) ||
		!approxEq(dbs.Low24h, 77.51*0.78) || !approxEq(dbs.Volume24hUsd, 2.5e6*dbs.Price) {
		t.Fatalf("DBS stats %+v", dbs)
	}
	if *dbs.SessionVenue != SGX || *dbs.LocalCurrency != "SGD" || *dbs.Underlying != "SGX: D05 ordinary share, SGD→USD at Yahoo FX" {
		t.Fatalf("DBS listing %+v", dbs)
	}
	if s := q.MarketState; s != "open" && s != "closed" && s != "unknown" || (dbs.Session == nil) != (s == "unknown") {
		t.Fatalf("DBS state %q session %v", s, dbs.Session)
	}
	h := by["HSDT"]
	if h.Price != 2.65 || h.QuoteFeed.FX != 1 || h.QuoteFeed.FXSymbol != "" || h.QuoteFeed.FXAsOf != nil || *h.Region != "us" || *h.SessionVenue != NYSE ||
		h.Range24hSource != "unavailable" || h.Change24hPct != 0 || h.QuoteFeed.PreviousClose != nil {
		t.Fatalf("HSDT %+v %+v", h, h.QuoteFeed)
	}
	// IDX has no verified calendar: the session is unknown and the state is Yahoo's trading period.
	if b := by["BBCA"]; b.Session != nil || b.QuoteFeed.MarketState != "open" || !approxEq(b.Price, 6175*0.00005588) {
		t.Fatalf("BBCA %+v %+v", b, b.QuoteFeed)
	}
	// The JSON carries the block; crypto rows have none.
	raw, _ := json.Marshal(assets)
	if !strings.Contains(string(raw), `"quoteFeed":{"symbol":"D05.SI","exchange":"SES","currency":"SGD","localPrice":78.37,`) ||
		strings.Count(string(raw), `"quoteFeed"`) != 3 {
		t.Fatalf("json %s", raw)
	}
	// Within the TTL the next read asks Yahoo nothing.
	calls := f.Calls("D05.SI")
	c.quoteFeedAssets(context.Background(), false)
	if f.Calls("D05.SI") != calls {
		t.Fatal("refetched inside the TTL")
	}
	if d, err := c.Derivatives(context.Background(), "DBS"); d != nil || err != nil {
		t.Fatalf("a quote feed has no perp: %+v %v", d, err)
	}
}

// A quote over QuoteStaleAfter old is halted (left out of the list, orders
// paused) but still values what is held; an outage keeps the last quote.
func TestQuoteFeedStaleAndOutage(t *testing.T) {
	c, f := feedClient(t)
	old := time.Now().Add(-QuoteStaleAfter - time.Hour)
	stale := yahootest.Daily("SE", "USD", 96, 96.85)
	stale.Meta.PriceTime = &old
	f.Set(stale)
	got := c.quoteFeedAssets(context.Background(), false)
	if se := got["SE"]; !se.Halted || !se.QuoteFeed.Stale || se.Price != 96.85 {
		t.Fatalf("stale SE %+v", se)
	}
	w := get(mount(c), context.Background(), "/api/market/assets")
	if w.Code != 200 || strings.Contains(w.Body.String(), `"symbol":"SE"`) || !strings.Contains(w.Body.String(), `"symbol":"DBS"`) {
		t.Fatalf("list %d %s", w.Code, w.Body.String()[:200])
	}
	// Yahoo goes down: the due rows keep their last reading, and the client
	// waits feedRetry before asking again.
	for _, s := range []string{"HSDT", "D05.SI", "BBCA.JK", "SE"} {
		f.SetErr(s, yahootest.Down)
	}
	c.feeds.mu.Lock()
	for k, q := range c.feeds.quotes {
		q.at = q.at.Add(-time.Hour)
		c.feeds.quotes[k] = q
	}
	c.feeds.mu.Unlock()
	got = c.quoteFeedAssets(context.Background(), false)
	if got["HSDT"].Price != 2.65 || got["DBS"].QuoteFeed.LocalPrice != 78.37 {
		t.Fatalf("outage dropped the last quotes: %+v", got)
	}
	calls := f.Calls("HSDT")
	c.quoteFeedAssets(context.Background(), false)
	if f.Calls("HSDT") != calls {
		t.Fatal("retried before feedRetry")
	}
}

// A listing the feed has not re-read for QuoteSilentAfter while its session is
// open is halted too: its last price no longer follows the market. While the
// session is closed an older read is still the last close.
func TestQuoteFeedSilentWhileOpen(t *testing.T) {
	info, _ := Lookup("SE")                                 // NYSE's verified calendar
	open := time.Date(2026, 10, 7, 15, 0, 0, 0, time.UTC)   // Wednesday 11:00 in New York
	closed := time.Date(2026, 10, 7, 22, 0, 0, 0, time.UTC) // 18:00, after the close
	meta := yahootest.Daily("SE", "USD", 96, 96.85).Meta
	for _, c := range []struct {
		name      string
		now, read time.Time
		quoted    time.Time
		halted    bool
	}{
		{"read recently", open, open.Add(-QuoteSilentAfter + time.Minute), open.Add(-QuoteSilentAfter + time.Minute), false},
		{"silent while open", open, open.Add(-QuoteSilentAfter - time.Minute), open.Add(-QuoteSilentAfter - time.Minute), true},
		{"closed", closed, closed.Add(-3 * time.Hour), closed.Add(-2 * time.Hour), false},
		{"days old", closed, closed, closed.Add(-QuoteStaleAfter - time.Hour), true},
	} {
		q := yahoo.Quote{Symbol: "SE", Price: 96.85, Time: c.quoted, Currency: "USD", Meta: meta, Via: "spark"}
		as, ok := feedAsset(info, servedQuote{fq: feedQuote{q: q, at: c.read}, source: "yahoo", class: ClassDelayed, basis: "trade"}, yahoo.Rate{Ccy: "USD", USD: 1}, c.now)
		if !ok || as.Halted != c.halted || as.QuoteFeed.Stale != c.halted || as.Price != 96.85 {
			t.Errorf("%s: %+v %+v", c.name, as, as.QuoteFeed)
		}
	}
}

// slowQuotes holds every Quotes call until release is closed, as Yahoo's
// shared budget does while a batch job is queued on it.
type slowQuotes struct {
	*yahootest.Fake
	release chan struct{}
	calls   chan struct{}
}

func (s slowQuotes) Quotes(ctx context.Context, symbols []string) (map[string]yahoo.Quote, error) {
	s.calls <- struct{}{}
	<-s.release
	return s.Fake.Quotes(ctx, symbols)
}

// A refresh outlives the read that started it, and reads meanwhile join it
// instead of starting another.
func TestQuoteFeedDetachedRefresh(t *testing.T) {
	c, f := feedClient(t)
	slow := slowQuotes{f, make(chan struct{}), make(chan struct{}, 64)}
	c.Yahoo = slow
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan map[string]Asset)
	go func() { got <- c.quoteFeedAssets(ctx, false) }()
	<-slow.calls
	cancel() // the caller leaves; the refresh carries on
	if rows := <-got; len(rows) != 0 {
		t.Fatalf("rows before the first quote %v", rows)
	}
	c.feeds.mu.Lock()
	done := c.feeds.running
	c.feeds.mu.Unlock()
	if done == nil {
		t.Fatal("the refresh stopped with its caller")
	}
	if rows := c.quoteFeedAssets(ctx, false); len(rows) != 0 || len(slow.calls) != 0 { // joins the running one
		t.Fatalf("a second refresh started: %d calls, %v", len(slow.calls), rows)
	}
	close(slow.release)
	<-done
	rows := c.quoteFeedAssets(context.Background(), false) // (the rows Yahoo lacks are asked for again)
	if rows["DBS"].QuoteFeed == nil || rows["HSDT"].Price != 2.65 {
		t.Fatalf("after the detached refresh: %v", rows)
	}
}

// Without a Yahoo source the rows are left out and their candles are an
// upstream error.
func TestQuoteFeedUnconfigured(t *testing.T) {
	b := markettest.New(t)
	c := testMarketClient(b.URL)
	assets, err := c.Assets(context.Background())
	if err != nil || len(assets) != len(Universe)-len(QuoteFeeds) || slices.ContainsFunc(assets, func(a Asset) bool { return a.QuoteFeed != nil }) {
		t.Fatalf("%d %v", len(assets), err)
	}
	if _, err := c.Candles(context.Background(), "DBS", "1d", 10); !errors.Is(err, ErrUpstream) {
		t.Fatal(err)
	}
}

func TestQuoteFeedCandles(t *testing.T) {
	c, f := feedClient(t)
	ctx := context.Background()
	// Daily: converted at each day's FX close, stamped 00:00 UTC of the local date.
	got, err := c.Candles(ctx, "DBS", "1d", 2)
	if err != nil || len(got) != 2 || !approxEq(got[1].C, 78.37*0.78) || got[1].T != time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC).Unix() ||
		!approxEq(got[0].C, 77.83*0.78) || got[1].V != 1000 {
		t.Fatalf("daily %+v %v", got, err)
	}
	// USD listings are not converted.
	if got, err := c.Candles(ctx, "HSDT", "1d", 5); err != nil || len(got) != 2 || got[1].C != 2.65 {
		t.Fatalf("HSDT %+v %v", got, err)
	}
	// Intraday bars use the latest rate; 4h buckets merge the hours inside them.
	hours := yahoo.Chart{Meta: yahoo.Meta{Symbol: "D05.SI", Currency: "SGD", Timezone: "Asia/Singapore", InstrumentType: "EQUITY"}}
	base := time.Date(2026, 10, 5, 1, 0, 0, 0, time.UTC)
	for i, cl := range []float64{10, 11, 12, 13, 14} {
		hours.Bars = append(hours.Bars, yahoo.Bar{T: base.Add(time.Duration(i) * time.Hour), O: cl, H: cl + 1, L: cl - 1, C: cl + .5, V: 100})
	}
	f.Set(hours)
	got, err = c.Candles(ctx, "DBS", "1h", 3)
	if err != nil || len(got) != 3 || !approxEq(got[2].C, 14.5*0.78) || got[2].T != base.Add(4*time.Hour).Unix() {
		t.Fatalf("1h %+v %v", got, err)
	}
	got, err = c.Candles(ctx, "DBS", "4h", 10)
	if err != nil || len(got) != 2 || got[0].T != time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC).Unix() || !approxEq(got[0].O, 10*0.78) ||
		!approxEq(got[0].C, 12.5*0.78) || !approxEq(got[0].H, 13*0.78) || !approxEq(got[0].L, 9*0.78) || got[0].V != 300 || got[1].V != 200 {
		t.Fatalf("4h %+v %v", got, err)
	}
	end := base.Add(2 * time.Hour)
	if got, err := c.CandlesBefore(ctx, "DBS", "1h", end, 10); err != nil || len(got) != 2 || got[1].T != base.Add(time.Hour).Unix() {
		t.Fatalf("before %+v %v", got, err)
	}
	// A listing Yahoo stops serving is upstream data unavailable, never the caller's 404.
	f.SetErr("D05.SI", yahootest.NotFound("D05.SI"))
	if _, err := c.Candles(ctx, "DBS", "15m", 10); !errors.Is(err, ErrUpstream) || yahoo.IsNotFound(err) {
		t.Fatalf("not found: %v", err)
	}
	if rng, iv := feedRange("1d", 1000, false); rng != "5y" || iv != "1d" {
		t.Fatal(rng, iv)
	}
	if rng, iv := feedRange("1d", 10, true); rng != "max" || iv != "1d" {
		t.Fatal(rng, iv)
	}
	if rng, iv := feedRange("4h", 1000, false); rng != "2y" || iv != "60m" {
		t.Fatal(rng, iv)
	}
}

// A quote feed's returns span calendar days: weekends carry Friday's close,
// so 7d is a week back, not seven sessions.
func TestQuoteFeedAnalyzeCalendarDays(t *testing.T) {
	c, f := feedClient(t)
	closes := make([]float64, 15) // weekdays to Monday 2026-10-05
	for i := range closes {
		closes[i] = 100 + float64(i)
	}
	f.Set(yahootest.Daily("HSDT", "USD", closes...))
	a, err := c.Analyze(context.Background(), "HSDT", "1d")
	// 1d: Monday vs Sunday, which carries Friday's 113; 7d: vs Monday 2026-09-28 (109), five sessions back.
	if err != nil || a.Price != 114 || !approxEq(a.Return1dPct, (114.0/113-1)*100) || !approxEq(a.Return7dPct, (114.0/109-1)*100) {
		t.Fatalf("%+v %v", a, err)
	}
	if got := CalendarCloses([]Candle{{T: 0, C: 1}, {T: 3 * 86400, C: 2}, {T: 4 * 86400, C: 3}}); !slices.Equal(got, []float64{1, 1, 1, 2, 3}) {
		t.Fatal(got)
	}
	if _, err := c.quoteFeedBars(context.Background(), QuoteFeeds[0], "1m", 10, 0); err == nil || !strings.Contains(err.Error(), "15m, 1h, 4h, 1d, 1w and 1M only") {
		t.Fatal(err)
	}
}

// The quote-feed venues: lunch breaks, official closures and the verified
// horizon; venues without an official calendar are unknown.
func TestQuoteFeedVenueSessions(t *testing.T) {
	at := func(zone string, y int, m time.Month, d, hh, mm int) time.Time {
		return time.Date(y, m, d, hh, mm, 0, 0, func() *time.Location { l, _ := time.LoadLocation(zone); return l }())
	}
	for _, c := range []struct {
		venue string
		t     time.Time
		open  bool
		next  time.Time
		known bool
	}{
		{SGX, at("Asia/Singapore", 2026, 10, 6, 11, 0), true, at("Asia/Singapore", 2026, 10, 6, 12, 0), true},
		{SGX, at("Asia/Singapore", 2026, 10, 6, 12, 30), false, at("Asia/Singapore", 2026, 10, 6, 13, 0), true},
		{SGX, at("Asia/Singapore", 2026, 11, 6, 17, 30), false, at("Asia/Singapore", 2026, 11, 10, 9, 0), true}, // Deepavali Monday
		{SGX, at("Asia/Singapore", 2026, 12, 23, 18, 0), false, at("Asia/Singapore", 2026, 12, 24, 9, 0), true},
		{SGX, at("Asia/Singapore", 2026, 12, 24, 10, 0), true, at("Asia/Singapore", 2026, 12, 24, 12, 0), true},  // Christmas Eve half-day
		{SGX, at("Asia/Singapore", 2026, 12, 24, 13, 30), false, at("Asia/Singapore", 2026, 12, 28, 9, 0), true}, // then Christmas and the weekend
		{SGX, at("Asia/Singapore", 2026, 12, 31, 12, 30), false, at("Asia/Singapore", 2027, 1, 4, 9, 0), true},   // New Year's Eve half-day, then 1 January
		{SGX, at("Asia/Singapore", 2027, 1, 1, 10, 0), false, at("Asia/Singapore", 2027, 1, 4, 9, 0), true},
		{SGX, at("Asia/Singapore", 2027, 2, 4, 18, 0), false, time.Time{}, true},                               // the next open is past the horizon
		{SGX, at("Asia/Singapore", 2027, 2, 5, 10, 0), false, time.Time{}, false},                              // the unpublished Lunar New Year eve
		{SGX, at("Asia/Singapore", 2026, 2, 16, 12, 30), false, at("Asia/Singapore", 2026, 2, 19, 9, 0), true}, // the 2026 eve half-day
		{SET, at("Asia/Bangkok", 2026, 10, 6, 13, 0), false, at("Asia/Bangkok", 2026, 10, 6, 14, 0), true},
		{SET, at("Asia/Bangkok", 2026, 10, 12, 16, 0), true, at("Asia/Bangkok", 2026, 10, 12, 16, 30), true},
		{SET, at("Asia/Bangkok", 2026, 10, 12, 17, 0), false, at("Asia/Bangkok", 2026, 10, 14, 10, 0), true}, // the Great Memorial Day
		{TWSE, at("Asia/Taipei", 2026, 10, 8, 13, 0), true, at("Asia/Taipei", 2026, 10, 8, 13, 30), true},
		{TWSE, at("Asia/Taipei", 2026, 10, 8, 14, 0), false, at("Asia/Taipei", 2026, 10, 12, 9, 0), true}, // 9 October adjusted holiday
		{TWSE, at("Asia/Taipei", 2027, 1, 4, 10, 0), false, time.Time{}, false},
		{IDX, at("Asia/Jakarta", 2026, 10, 6, 10, 0), false, time.Time{}, false},
		{BURSA, at("Asia/Kuala_Lumpur", 2026, 10, 6, 10, 0), false, time.Time{}, false},
		{HOSE, at("Asia/Ho_Chi_Minh", 2026, 10, 6, 10, 0), false, time.Time{}, false},
	} {
		open, next, known := calendars[c.venue].session(c.t)
		if open != c.open || known != c.known || !next.Equal(c.next) {
			t.Errorf("%s %v: open %v next %v known %v", c.venue, c.t, open, next, known)
		}
	}
	if s := calendars[SGX].sessionsOn(at("Asia/Singapore", 2027, 2, 8, 0, 0)); s != nil {
		t.Errorf("SGX 2027-02-08 (the observed Lunar New Year Sunday) reads %v", s)
	}
	// A verified calendar decides the market state; otherwise Yahoo's period does.
	dbs, _ := Lookup("DBS")
	lunch := at("Asia/Singapore", 2026, 10, 6, 12, 30)
	start, end := lunch.Add(-4*time.Hour), lunch.Add(5*time.Hour)
	if s := marketState(dbs, &start, &end, lunch); s != "closed" {
		t.Fatalf("SGX lunch reads %s", s)
	}
	// The Christmas Eve half-day: closed from 12:00 whatever Yahoo's period says.
	half := at("Asia/Singapore", 2026, 12, 24, 13, 30)
	yStart, yEnd := at("Asia/Singapore", 2026, 12, 24, 9, 0), at("Asia/Singapore", 2026, 12, 24, 17, 0)
	row := withSessions([]Asset{{Symbol: "DBS", Class: Stock, QuoteFeed: &QuoteFeedInfo{Symbol: "D05.SI", regularStart: &yStart, regularEnd: &yEnd}}}, half)[0]
	if row.QuoteFeed.MarketState != "closed" || row.Session == nil || *row.Session != "closed" || row.SessionChangeAt == nil ||
		!row.SessionChangeAt.Equal(at("Asia/Singapore", 2026, 12, 28, 9, 0)) {
		t.Fatalf("DBS on the half-day: %s %v %v", row.QuoteFeed.MarketState, row.Session, row.SessionChangeAt)
	}
	bbca, _ := Lookup("BBCA")
	if marketState(bbca, &start, &end, lunch) != "open" || marketState(bbca, &start, &end, end) != "closed" || marketState(bbca, nil, nil, lunch) != "unknown" {
		t.Fatal("Yahoo period state")
	}
}

func approxEq(a, b float64) bool { return a == b || a-b < 1e-9*max(1, b) && b-a < 1e-9*max(1, b) }

// Past 4 days a quote stays current while its venue's verified calendar says
// no regular session has been open 30 minutes since: KRX's
// year-end closure and HKEX's Lunar New Year run past 4 days. A venue with no
// calendar keeps the 4-day rule, and the silent rule is unchanged.
func TestQuoteFeedHolidayStale(t *testing.T) {
	seoul, hk := zone("Asia/Seoul"), zone("Asia/Hong_Kong")
	krxClose := time.Date(2026, 12, 30, 15, 30, 0, 0, seoul)
	hkHalf := time.Date(2027, 2, 5, 12, 0, 0, 0, hk)
	krxOpen := time.Date(2026, 10, 8, 10, 0, 0, 0, seoul)
	for _, c := range []struct {
		name, symbol      string
		quoted, read, now time.Time
		stale             bool
	}{
		{"KRX year-end, Sunday", "NAVER", krxClose, krxClose, time.Date(2027, 1, 3, 12, 0, 0, 0, time.UTC), false},
		{"KRX before 30 minutes of trading", "NAVER", krxClose, time.Date(2027, 1, 4, 9, 20, 0, 0, seoul), time.Date(2027, 1, 4, 9, 20, 0, 0, seoul), false},
		{"KRX traded 30 minutes since", "NAVER", krxClose, time.Date(2027, 1, 4, 9, 31, 0, 0, seoul), time.Date(2027, 1, 4, 9, 31, 0, 0, seoul), true},
		{"HKEX Lunar New Year after a half-day", "TENCENT", hkHalf, hkHalf, time.Date(2027, 2, 10, 9, 0, 0, 0, hk), false},
		{"HKEX after the holiday's first session", "TENCENT", hkHalf, time.Date(2027, 2, 10, 10, 0, 0, 0, hk), time.Date(2027, 2, 10, 10, 0, 0, 0, hk), true},
		{"IDX has no calendar", "BBCA", time.Now().Add(-5 * 24 * time.Hour), time.Now(), time.Now(), true},
		{"KRX open, no read for 21 minutes", "NAVER", krxOpen.Add(-21 * time.Minute), krxOpen.Add(-21 * time.Minute), krxOpen, true},
		{"KRX open, read a minute ago", "NAVER", krxOpen.Add(-time.Minute), krxOpen.Add(-time.Minute), krxOpen, false},
	} {
		info, _ := Lookup(c.symbol)
		meta := yahootest.Daily(info.Pair, "KRW", 200000).Meta
		q := yahoo.Quote{Symbol: info.Pair, Price: 200000, Time: c.quoted, Currency: "KRW", Meta: meta, Via: "spark"}
		as, ok := feedAsset(info, servedQuote{fq: feedQuote{q: q, at: c.read}, source: "yahoo", class: ClassDelayed, basis: "trade"}, yahoo.Rate{Ccy: "KRW", USD: 0.0007, Symbol: "KRWUSD=X"}, c.now)
		if !ok || as.QuoteFeed.Stale != c.stale || as.Halted != c.stale {
			t.Errorf("%s: stale %v, want %v", c.name, as.QuoteFeed.Stale, c.stale)
		}
	}
	// A date past a venue's verified horizon keeps the 4-day rule.
	if known, _ := calendars[KRX].openedSince(time.Date(2027, 12, 30, 15, 30, 0, 0, seoul), time.Date(2028, 1, 3, 9, 0, 0, 0, seoul)); known {
		t.Error("2028 is past KRX's horizon")
	}

	// Listed in GET /api/market/assets over the year-end closure, gone once KRX has traded without a new quote.
	c, f := feedClient(t)
	naver := yahootest.Daily("035420.KS", "KRW", 198000, 200000)
	naver.Meta.PriceTime, naver.Meta.Timezone = &krxClose, "Asia/Seoul"
	f.Set(naver)
	f.SetFX("KRW", 0.0007)
	c.Now = func() time.Time { return time.Date(2027, 1, 2, 12, 0, 0, 0, seoul) }
	if w := get(mount(c), context.Background(), "/api/market/assets"); w.Code != 200 || !strings.Contains(w.Body.String(), `"symbol":"NAVER"`) {
		t.Fatalf("2027-01-02: NAVER not listed (%d)", w.Code)
	}
	c, f = feedClient(t)
	f.Set(naver)
	f.SetFX("KRW", 0.0007)
	c.Now = func() time.Time { return time.Date(2027, 1, 4, 10, 0, 0, 0, seoul) }
	if w := get(mount(c), context.Background(), "/api/market/assets"); w.Code != 200 || strings.Contains(w.Body.String(), `"symbol":"NAVER"`) {
		t.Fatalf("2027-01-04 after the open: NAVER listed (%d)", w.Code)
	}
}

// A quote feed's outlook σ is measured over calendar days, as Analyze's
// realized volatility is: weekday bars no longer annualize
// with √365 as if every bar were a day. A 24/7 outlook is unchanged.
func TestQuoteFeedOutlookCalendarVol(t *testing.T) {
	c, f := feedClient(t)
	closes := make([]float64, 120)
	p := 100.0
	for i := range closes {
		p *= 1 + 0.02*float64(i%3-1) + 0.003*float64(i%5-2) // a trading-day series, weekdays only
		closes[i] = p
	}
	f.Set(yahootest.Daily("HSDT", "USD", closes...))
	ctx := context.Background()
	a, err := c.Analyze(ctx, "HSDT", "1d")
	if err != nil {
		t.Fatal(err)
	}
	o, err := c.Outlook(ctx, "HSDT", 30)
	if err != nil || math.Abs(o.VolAnnPct-a.RealizedVolAnnPct) > 0.01 || o.VolBasis.Days != 90 || o.VolBasis.Window != "90d" {
		t.Fatalf("outlook %v (%+v) vs analysis %v: %v", o.VolAnnPct, o.VolBasis, a.RealizedVolAnnPct, err)
	}
	daily, _ := c.Candles(ctx, "HSDT", "1d", dailyBars)
	if trading := OutlookCandles("HSDT", daily, 30); trading.VolAnnPct <= o.VolAnnPct*1.1 || trading.VolBasis.Days != 90 {
		t.Fatalf("trading-day σ %v should read higher than the calendar one %v", trading.VolAnnPct, o.VolAnnPct)
	}
	if long := outlookCandles("HSDT", daily, 730, true); long.VolBasis.Window != "long-run" || long.VolBasis.Days != len(CalendarCloses(daily)) ||
		long.VolBasis.Since != time.Unix(daily[0].T, 0).UTC().Format(time.DateOnly) {
		t.Fatalf("long-run basis %+v", long.VolBasis)
	}
	// 24/7: the exported pure function and the client path agree byte for byte.
	btc, _ := c.Candles(ctx, "BTC", "1d", dailyBars)
	got, err := c.Outlook(ctx, "BTC", 30)
	want := OutlookCandles("BTC", btc, 30)
	if g, _ := json.Marshal(got); err != nil || string(g) != string(must(json.Marshal(want))) {
		t.Fatalf("crypto outlook changed: %v", err)
	}
}

func must[T any](v T, _ error) T { return v }

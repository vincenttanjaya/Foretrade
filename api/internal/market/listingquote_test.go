package market

import (
	"slices"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo/yahootest"
)

// Watch keeps only listings no quote-feed instrument reads: a perp's or an
// ADR's local line and a research-only ticker, never a listed or named feed's
// own ticker, an instrument symbol, a bad ticker, an unknown venue, a
// duplicate or a row without a currency. Its rows stay out of Lookup.
func TestWatchKeepsOnlyUntradedListings(t *testing.T) {
	c := testMarketClient("")
	n := c.Watch([]WatchRow{
		{"2330.tw", TWSE, "TWD"}, {"005930.KS", KRX, "KRW"}, {"268280.KS", KRX, "KRW"},
		{"6501.T", TSE, "JPY"}, {"D05.SI", SGX, "SGD"}, {"SMSN", KRX, "KRW"}, {"bad ticker", KRX, "KRW"},
		{"TEL.PS", "PSE", "PHP"}, {"2330.TW", TWSE, "TWD"}, {"9988.HK", HKEX, ""},
	})
	got := []string{}
	for _, a := range c.feeds.watch {
		got = append(got, a.Symbol)
	}
	if n != 3 || !slices.Equal(got, []string{"2330.TW", "005930.KS", "268280.KS"}) || c.feeds.watchBy["268280.KS"] != 2 {
		t.Fatalf("kept %d: %v", n, got)
	}
	if _, ok := Lookup("2330.TW"); ok {
		t.Fatal("a watch row is an instrument")
	}
	if a := c.feeds.watch[0]; !a.QuoteFeed() || a.IsListed() || a.Listing.SessionVenue != TWSE || a.Listing.LocalCurrency != "TWD" {
		t.Fatalf("row %+v", a)
	}
	if c.Watch(nil) != 0 || len(c.feeds.watch) != 0 {
		t.Fatal("Watch(nil) keeps rows")
	}
}

// Each watch read sets the next: 15 minutes while open or unknown; after the
// close, the settled read (an hour after the later of the calendar's close and
// Yahoo's end, or Yahoo's end alone where there is no calendar); then the next
// open (the calendar's, a holiday skipped; else Yahoo's start on the next
// weekday); a lunch break waits for the afternoon.
func TestWatchNext(t *testing.T) {
	row := func(venue, ccy string) AssetInfo {
		return AssetInfo{Symbol: "X", Pair: "X", Listing: &Listing{SessionVenue: venue, LocalCurrency: ccy}}
	}
	kr, jp, hk, id := row(KRX, "KRW"), row(TSE, "JPY"), row(HKEX, "HKD"), row(IDX, "IDR")
	kst := func(d, h, m int) time.Time { return local("Asia/Seoul", 2026, 10, d, h, m) }
	hkt := func(d, h, m int) time.Time { return local("Asia/Hong_Kong", 2026, 10, d, h, m) }
	wib := func(d, h, m int) time.Time { return local("Asia/Jakarta", 2026, 10, d, h, m) }
	period := func(start, end time.Time) yahoo.Meta {
		return yahoo.Meta{Timezone: "Asia/Jakarta", RegularStart: &start, RegularEnd: &end}
	}
	krYahoo := yahoo.Meta{RegularStart: ptr(kst(14, 9, 0)), RegularEnd: ptr(kst(14, 15, 0))} // Yahoo's KRX day ends 15:00
	hkYahoo := yahoo.Meta{RegularStart: ptr(hkt(14, 9, 30)), RegularEnd: ptr(hkt(14, 16, 10))}
	idx := period(wib(14, 9, 0), wib(14, 16, 15))
	idxFri := period(wib(16, 9, 0), wib(16, 16, 15))
	for _, c := range []struct {
		name string
		a    AssetInfo
		m    yahoo.Meta
		at   time.Time
		want time.Time
	}{
		{"KRX open", kr, krYahoo, kst(14, 10, 0), kst(14, 10, 15)},
		{"KRX just closed: the settled read", kr, krYahoo, kst(14, 15, 40), kst(14, 16, 30)},
		{"KRX settled: the next open", kr, krYahoo, kst(14, 16, 30), kst(15, 9, 0)},
		{"KRX Friday: Monday's open", kr, yahoo.Meta{}, kst(16, 17, 0), kst(19, 9, 0)},
		{"KRX before Hangul Day: the Monday after", kr, yahoo.Meta{}, kst(8, 17, 0), kst(12, 9, 0)},
		{"KRX before the open", kr, yahoo.Meta{}, kst(15, 7, 0), kst(15, 9, 0)},
		{"TSE lunch: the afternoon", jp, yahoo.Meta{}, local("Asia/Tokyo", 2026, 10, 14, 11, 40), local("Asia/Tokyo", 2026, 10, 14, 12, 30)},
		{"HKEX after 16:00: settled from Yahoo's 16:10", hk, hkYahoo, hkt(14, 16, 5), hkt(14, 17, 10)},
		{"HKEX settled", hk, hkYahoo, hkt(14, 17, 10), hkt(15, 9, 30)},
		{"IDX open (Yahoo's period)", id, idx, wib(14, 10, 0), wib(14, 10, 15)},
		{"IDX just closed", id, idx, wib(14, 16, 20), wib(14, 17, 15)},
		{"IDX settled: tomorrow at Yahoo's start", id, idx, wib(14, 17, 15), wib(15, 9, 0)},
		{"IDX Friday settled: Monday", id, idxFri, wib(16, 18, 0), wib(19, 9, 0)},
		{"IDX before the open (the coming day's period)", id, idx, wib(14, 8, 0), wib(14, 9, 0)},
		{"IDX with no period: unknown counts as open", id, yahoo.Meta{}, wib(14, 20, 0), wib(14, 20, 15)},
		{"KRX past its calendar, no period: 15 minutes", kr, yahoo.Meta{}, kst(14, 0, 0).AddDate(2, 0, 0), kst(14, 0, 15).AddDate(2, 0, 0)},
	} {
		if got := watchNext(c.a, c.m, c.at); !got.Equal(c.want) {
			t.Errorf("%s: %v, want %v", c.name, got.In(c.want.Location()), c.want)
		}
	}
	// Past the calendar's horizon a closed row with a Yahoo period follows it.
	late := local("Asia/Seoul", 2028, 10, 12, 20, 0) // a Thursday
	m := yahoo.Meta{Timezone: "Asia/Seoul", RegularStart: ptr(late.Add(-11 * time.Hour)), RegularEnd: ptr(late.Add(-5 * time.Hour))}
	if got := watchNext(kr, m, late); !got.Equal(late.Add(13 * time.Hour)) {
		t.Errorf("past the horizon, settled: %v", got)
	}
}

func ptr[T any](v T) *T { return &v }

// The watch tier is read last, after every listed row, in the same batches;
// a read's next time makes it due, a failed one waits 15 minutes; with spark
// batches off it is left out.
func TestWatchTierPlan(t *testing.T) {
	now := local("Asia/Seoul", 2026, 10, 14, 10, 0) // KRX, TSE, HKEX, TWSE open
	f := yahootest.New()
	c := clocked(f, &now)
	c.Watch([]WatchRow{{"005930.KS", KRX, "KRW"}, {"2330.TW", TWSE, "TWD"}})
	c.feeds.mu.Lock()
	rows, urgent := c.feeds.plan(newFeedClock(now), false, true)
	c.feeds.mu.Unlock()
	if len(rows) != feedMaxCalls*feedBatch || !urgent || slices.ContainsFunc(rows, func(a AssetInfo) bool { return a.Symbol == "005930.KS" }) {
		t.Fatalf("first plan: %d rows, the watch tier inside the cap", len(rows))
	}
	// Once every instrument has a quote, the watch rows come after the listed ones due.
	f.Set(yahootest.Daily("005930.KS", "KRW", 70000))
	f.Set(yahootest.Daily("2330.TW", "TWD", 1000))
	refresh(t, c) // 400 instrument rows, which the fake does not serve
	refresh(t, c) // the other 342, then the watch rows
	if calls := f.Batches(); len(calls) != 2*feedMaxCalls-2 || calls[len(calls)-1] != (len(QuoteFeeds)+len(Listed)+2)%feedBatch {
		t.Fatalf("batches %v", calls)
	}
	q, ok := c.feeds.quotes["005930.KS"]
	if !ok || !q.next.Equal(q.at.Add(watchOpenTTL)) || c.feeds.quotes["2330.TW"].q.Price != 1000 {
		t.Fatalf("watch quotes %+v", q)
	}
	read := f.Calls("005930.KS")
	now = q.at.Add(watchOpenTTL - time.Second)
	refresh(t, c)
	if f.Calls("005930.KS") != read {
		t.Fatal("read before its next time")
	}
	now = q.at.Add(watchOpenTTL)
	c.feeds.mu.Lock()
	rows, _ = c.feeds.plan(newFeedClock(now), false, true)
	c.feeds.mu.Unlock()
	if i := slices.IndexFunc(rows, func(a AssetInfo) bool { return a.Symbol == "005930.KS" }); i < 0 ||
		slices.ContainsFunc(rows[i:], func(a AssetInfo) bool { return a.IsListed() }) {
		t.Fatalf("watch row not after every listed row: at %d of %d", i, len(rows))
	}
	c.feeds.mu.Lock()
	unbatched, _ := c.feeds.plan(newFeedClock(now), false, false)
	c.feeds.failedAt["005930.KS"] = now.Add(-watchRetry + time.Second)
	failed, _ := c.feeds.plan(newFeedClock(now), false, true)
	c.feeds.mu.Unlock()
	if slices.ContainsFunc(unbatched, func(a AssetInfo) bool { return a.Symbol == "005930.KS" }) ||
		slices.ContainsFunc(failed, func(a AssetInfo) bool { return a.Symbol == "005930.KS" }) {
		t.Fatal("watch row planned with spark off, or 15 minutes after a failed read")
	}
	// An answer older than the held quote is not kept, but the row is not due again at once.
	old := q.q
	old.Time = old.Time.Add(-time.Hour)
	c.feeds.mu.Lock()
	c.feeds.store(c.feeds.watch[:1], map[string]yahoo.Quote{"005930.KS": old}, now)
	kept := c.feeds.quotes["005930.KS"]
	c.feeds.mu.Unlock()
	if !kept.q.Time.Equal(q.q.Time) || !kept.next.Equal(now.Add(watchOpenTTL)) {
		t.Fatalf("older answer: %+v", kept)
	}
}

// ListingQuotes answers from the trade tier for a ticker a quote-feed
// instrument reads and from the watch tier otherwise, with the session, the
// tier's interval, when it turns stale, the measured delay and the FX rate;
// it reads nothing itself.
func TestListingQuotes(t *testing.T) {
	now := local("Asia/Seoul", 2026, 10, 14, 10, 0)
	f := yahootest.New()
	c := clocked(f, &now)
	c.Watch([]WatchRow{{"005930.KS", KRX, "KRW"}})
	if got := c.ListingQuotes([]string{"005930.KS", "000810.KS"}); len(got) != 0 {
		t.Fatalf("before any read: %v", got)
	}
	trade, krx := listed(t, "000810.KS"), named(t, "NAVER")
	read := now.Add(-2 * time.Minute)
	samsung := yahoo.Quote{Symbol: "005930.KS", Price: 70000, Time: read.Add(-20 * time.Minute), Currency: "KRW"}
	c.feeds.mu.Lock()
	c.feeds.store([]AssetInfo{trade, krx, c.feeds.watch[0]}, map[string]yahoo.Quote{
		trade.Pair:  {Symbol: trade.Pair, Price: 300000, Time: read.Add(-20 * time.Minute), Currency: "KRW"},
		krx.Pair:    {Symbol: krx.Pair, Price: 200000, Time: read.Add(-20 * time.Minute), Currency: "KRW"},
		"005930.KS": samsung}, read)
	c.feeds.fx = map[string]feedFX{"KRW": {yahoo.Rate{Ccy: "KRW", USD: 0.00072, Time: read, Symbol: "KRWUSD=X"}, read}}
	c.feeds.mu.Unlock()
	reads := f.Calls("005930.KS")
	got := c.ListingQuotes([]string{"005930.ks", trade.Pair, krx.Pair, "6501.T", "nonsense ticker"})
	if len(got) != 3 || f.Calls("005930.KS") != reads {
		t.Fatalf("quotes %v", got)
	}
	s, l, n := got["005930.KS"], got[trade.Pair], got[krx.Pair]
	if s.Tier != "screener" || s.Source != "yahoo" || s.Price != 70000 || s.Currency != "KRW" || !s.LastTradeAt.Equal(read.Add(-20*time.Minute)) ||
		!s.ReadAt.Equal(read) || s.Session != "open" || s.Refresh != watchOpenTTL || !s.StaleAt.Equal(read.Add(watchOpenTTL+quoteGrace)) ||
		s.Stale || s.DelayMinutes == nil || *s.DelayMinutes != 20 || s.DelayBasis != "measured" || s.FX == nil || s.FX.USD != 0.00072 {
		t.Fatalf("watch quote %+v", s)
	}
	if l.Tier != "trade" || l.Refresh != feedListedOpenTTL || !l.StaleAt.Equal(read.Add(feedListedOpenTTL+quoteGrace)) || l.Stale ||
		n.Tier != "trade" || n.Refresh != feedOpenTTL || !n.StaleAt.Equal(read.Add(feedOpenTTL+quoteGrace)) || n.Stale {
		t.Fatalf("trade quotes %+v %+v", l, n)
	}
	// A named row not re-read 3 minutes on is stale; a listed one is not yet.
	now = read.Add(feedOpenTTL + quoteGrace + time.Second)
	got = c.ListingQuotes([]string{trade.Pair, krx.Pair})
	if !got[krx.Pair].Stale || got[trade.Pair].Stale {
		t.Fatalf("3 minutes on: %+v", got)
	}
	// After the close a settled listed row is held to the next open: no
	// interval, stale only once the open's read is late; the watch row waits
	// for its settled read.
	settled := local("Asia/Seoul", 2026, 10, 14, 16, 31)
	c.feeds.mu.Lock()
	c.feeds.store([]AssetInfo{trade, c.feeds.watch[0]}, map[string]yahoo.Quote{
		trade.Pair:  {Symbol: trade.Pair, Price: 301000, Time: local("Asia/Seoul", 2026, 10, 14, 15, 30), Currency: "KRW"},
		"005930.KS": {Symbol: "005930.KS", Price: 70500, Time: local("Asia/Seoul", 2026, 10, 14, 15, 30), Currency: "KRW"}}, settled)
	c.feeds.mu.Unlock()
	now = local("Asia/Seoul", 2026, 10, 14, 23, 0)
	got = c.ListingQuotes([]string{trade.Pair, "005930.KS"})
	open := local("Asia/Seoul", 2026, 10, 15, 9, 0)
	if h := got[trade.Pair]; h.Session != "closed" || h.Refresh != 0 || !h.StaleAt.Equal(open.Add(quoteGrace)) || h.Stale {
		t.Fatalf("held trade row %+v", h)
	}
	if w := got["005930.KS"]; w.Session != "closed" || w.Refresh != 0 || !w.StaleAt.Equal(open.Add(quoteGrace)) || w.Stale || w.Price != 70500 {
		t.Fatalf("settled watch row %+v", w)
	}
	now = open.Add(quoteGrace + time.Second)
	if got := c.ListingQuotes([]string{trade.Pair}); !got[trade.Pair].Stale || got[trade.Pair].Session != "open" {
		t.Fatalf("not re-read after the open: %+v", got)
	}
	// A USD listing needs no rate; the measured delays.
	if d, ok := YahooDelay(SET); !ok || d != 30 {
		t.Fatalf("SET delay %v", d)
	}
	if _, ok := YahooDelay(NYSE); ok {
		t.Fatal("NYSE has a measured delay")
	}
}

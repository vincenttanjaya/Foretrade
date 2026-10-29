package market

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo/yahootest"
)

func listed(t *testing.T, symbol string) AssetInfo {
	t.Helper()
	a, ok := Lookup(symbol)
	if !ok || !a.IsListed() {
		t.Fatalf("%s is not listed", symbol)
	}
	return a
}

func named(t *testing.T, symbol string) AssetInfo {
	t.Helper()
	a, ok := Lookup(symbol)
	if !ok || a.IsListed() || !a.QuoteFeed() {
		t.Fatalf("%s is not a named quote feed", symbol)
	}
	return a
}

func local(zoneName string, y int, m time.Month, d, hh, mm int) time.Time {
	return time.Date(y, m, d, hh, mm, 0, 0, zone(zoneName))
}

// readAt is a's quote as read at at, with Yahoo's trading period [start, end).
func readAt(a AssetInfo, at time.Time, start, end *time.Time) feedQuote {
	q := yahoo.Quote{Symbol: a.Pair, Price: 100, Time: at.Add(-15 * time.Minute), Currency: a.Listing.LocalCurrency,
		Meta: yahoo.Meta{RegularStart: start, RegularEnd: end}, Via: "spark"}
	return newFeedQuote(a, q, at)
}

// due() by tier and state: a named or hot row every minute while open, any
// other listed row every 5 minutes; 15 while closed; at once on a flip; an
// unknown session counts as open; a failed read waits feedRetry.
func TestFeedTiers(t *testing.T) {
	open := local("Asia/Seoul", 2026, 10, 14, 10, 0) // Wednesday, KRX open
	closed := local("Asia/Seoul", 2026, 10, 14, 20, 0)
	naver, kr := named(t, "NAVER"), listed(t, "000810.KS")
	bbni := listed(t, "BBNI.JK") // IDX: no verified calendar
	f := &feedState{failedAt: map[string]time.Time{}}
	for _, c := range []struct {
		name       string
		a          AssetInfo
		read, now  time.Time
		hot, fresh bool
		due        bool
	}{
		{"named, open, 59 s", naver, open.Add(-59 * time.Second), open, false, false, false},
		{"named, open, a minute", naver, open.Add(-time.Minute), open, false, false, true},
		{"named, open, fresh", naver, open.Add(-10 * time.Second), open, false, true, true},
		{"listed, open, 4 minutes", kr, open.Add(-4 * time.Minute), open, false, false, false},
		{"listed, open, fresh is not for it", kr, open.Add(-4 * time.Minute), open, false, true, false},
		{"listed, open, 5 minutes", kr, open.Add(-5 * time.Minute), open, false, false, true},
		{"hot listed, open, a minute", kr, open.Add(-time.Minute), open, true, false, true},
		{"named, closed, 14 minutes", naver, closed.Add(-14 * time.Minute), closed, false, false, false},
		{"named, closed, 15 minutes", naver, closed.Add(-15 * time.Minute), closed, false, false, true},
		{"hot listed, closed, 15 minutes", kr, closed.Add(-15 * time.Minute), closed, true, false, true},
		{"listed, held since the close", kr, closed.Add(-2 * time.Hour), closed, false, false, false},
		{"named, the open flips it", naver, local("Asia/Seoul", 2026, 10, 14, 8, 59), local("Asia/Seoul", 2026, 10, 14, 9, 0), false, false, true},
		{"listed, the open flips a held row", kr, local("Asia/Seoul", 2026, 10, 13, 20, 0), local("Asia/Seoul", 2026, 10, 14, 9, 0), false, false, true},
		{"unknown counts as open: 4 minutes", bbni, open.Add(-4 * time.Minute), open, false, false, false},
		{"unknown counts as open: 5 minutes", bbni, open.Add(-5 * time.Minute), open, false, false, true},
	} {
		q := readAt(c.a, c.read, nil, nil)
		if got := f.due(c.a, q, true, newFeedClock(c.now), c.hot, c.fresh); got != c.due {
			t.Errorf("%s: due %v", c.name, got)
		}
	}
	if !f.due(kr, feedQuote{}, false, newFeedClock(open), false, false) {
		t.Error("a row never read is due")
	}
	f.failedAt[kr.Symbol] = open.Add(-29 * time.Second)
	if f.due(kr, feedQuote{}, false, newFeedClock(open), false, false) {
		t.Error("a row whose read failed 29 s ago is due")
	}
	f.failedAt[kr.Symbol] = open.Add(-feedRetry)
	if !f.due(kr, feedQuote{}, false, newFeedClock(open), false, false) {
		t.Error("a row whose read failed 30 s ago is not due")
	}
}

// A closed listed row is held only on a venue with a verified calendar, and
// only once read an hour after the later of the calendar's final close and
// Yahoo's regular end; the next open makes it due. A lunch break never holds.
func TestSettledHold(t *testing.T) {
	kr := listed(t, "000810.KS")
	yStart, yEnd := local("Asia/Seoul", 2026, 10, 14, 9, 0), local("Asia/Seoul", 2026, 10, 14, 15, 0) // Yahoo's KRX period ends at 15:00
	early := readAt(kr, local("Asia/Seoul", 2026, 10, 14, 15, 55), &yStart, &yEnd)
	late := readAt(kr, local("Asia/Seoul", 2026, 10, 14, 16, 31), &yStart, &yEnd)
	f := &feedState{}
	if early.settled || !late.settled || early.state != "closed" {
		t.Fatalf("15:55 settled %v, 16:31 settled %v", early.settled, late.settled)
	}
	if !f.due(kr, early, true, newFeedClock(local("Asia/Seoul", 2026, 10, 14, 16, 10)), false, false) {
		t.Error("an unsettled closed row waits 15 minutes, not longer")
	}
	for _, now := range []time.Time{local("Asia/Seoul", 2026, 10, 14, 23, 0), local("Asia/Seoul", 2026, 10, 15, 8, 59)} {
		if f.due(kr, late, true, newFeedClock(now), false, false) {
			t.Errorf("held row due at %v", now)
		}
	}
	if !f.due(kr, late, true, newFeedClock(local("Asia/Seoul", 2026, 10, 15, 9, 0)), false, false) {
		t.Error("the next trading day's open does not make the held row due")
	}
	if !f.due(kr, late, true, newFeedClock(local("Asia/Seoul", 2026, 10, 14, 16, 46)), true, false) {
		t.Error("a hot row is never held")
	}
	// From local midnight Yahoo reports the coming day's period: a read then
	// (a restart overnight) is settled on the calendar's last close and held to
	// the open, not read every 15 minutes all night.
	ns, ne := local("Asia/Seoul", 2026, 10, 15, 9, 0), local("Asia/Seoul", 2026, 10, 15, 15, 0)
	night := readAt(kr, local("Asia/Seoul", 2026, 10, 15, 0, 10), &ns, &ne)
	if !night.settled || f.due(kr, night, true, newFeedClock(local("Asia/Seoul", 2026, 10, 15, 8, 59)), false, false) ||
		!f.due(kr, night, true, newFeedClock(local("Asia/Seoul", 2026, 10, 15, 9, 0)), false, false) {
		t.Errorf("a 00:10 KST read with the coming day's Yahoo period: %+v", night)
	}
	// HKEX: Yahoo's period runs to 16:10, past the calendar's 16:00.
	hk := listed(t, "0005.HK")
	hs, he := local("Asia/Hong_Kong", 2026, 10, 14, 9, 30), local("Asia/Hong_Kong", 2026, 10, 14, 16, 10)
	if readAt(hk, local("Asia/Hong_Kong", 2026, 10, 14, 17, 5), &hs, &he).settled || !readAt(hk, local("Asia/Hong_Kong", 2026, 10, 14, 17, 10), &hs, &he).settled {
		t.Error("HKEX's hold starts at 17:10")
	}
	// TSE's lunch break (11:30–12:30) is closed but never settled.
	jp := listed(t, "6501.T")
	if q := readAt(jp, local("Asia/Tokyo", 2026, 10, 14, 12, 29), nil, nil); q.state != "closed" || q.settled {
		t.Errorf("lunch read %+v", q)
	}
	// Before the open the previous close is settled.
	if q := readAt(jp, local("Asia/Tokyo", 2026, 10, 14, 8, 0), nil, nil); !q.settled {
		t.Error("a pre-open read is held to the open")
	}
	// IDX has no calendar: a closed row keeps the 15-minute TTL.
	id := listed(t, "BBNI.JK")
	is, ie := local("Asia/Jakarta", 2026, 10, 14, 9, 0), local("Asia/Jakarta", 2026, 10, 14, 16, 15)
	q := readAt(id, local("Asia/Jakarta", 2026, 10, 14, 17, 0), &is, &ie)
	if q.settled || q.state != "closed" || f.due(id, q, true, newFeedClock(local("Asia/Jakarta", 2026, 10, 14, 17, 14)), false, false) ||
		!f.due(id, q, true, newFeedClock(local("Asia/Jakarta", 2026, 10, 14, 17, 15)), false, false) {
		t.Errorf("IDX row read at 17:00: %+v", q)
	}
	// Past a venue's horizon nothing is held.
	if q := readAt(kr, local("Asia/Seoul", 2028, 1, 4, 20, 0), nil, nil); q.settled {
		t.Error("held past the horizon")
	}
	// Nor is a row settled on the horizon's last trading day: no calendar open
	// would ever end that hold, so it falls back to the 15-minute closed TTL
	// (TWSE's and SET's calendars end with 2026; Yahoo's stored period is the
	// last day's).
	for _, c := range []struct {
		a          AssetInfo
		start, end time.Time
		read, now  time.Time
	}{
		{listed(t, "3017.TW"), local("Asia/Taipei", 2026, 12, 31, 9, 0), local("Asia/Taipei", 2026, 12, 31, 13, 30),
			local("Asia/Taipei", 2026, 12, 31, 15, 0), local("Asia/Taipei", 2027, 1, 4, 9, 30)},
		{listed(t, "BBL.BK"), local("Asia/Bangkok", 2026, 12, 30, 10, 0), local("Asia/Bangkok", 2026, 12, 30, 16, 30),
			local("Asia/Bangkok", 2026, 12, 30, 18, 0), local("Asia/Bangkok", 2027, 1, 4, 10, 30)},
	} {
		q := readAt(c.a, c.read, &c.start, &c.end)
		if !q.settled {
			t.Fatalf("%s: a read after the horizon's last close is settled", c.a.Symbol)
		}
		if f.due(c.a, q, true, newFeedClock(c.read.Add(14*time.Minute)), false, false) {
			t.Errorf("%s: due inside the horizon while held", c.a.Symbol)
		}
		if !f.due(c.a, q, true, newFeedClock(c.now), false, false) {
			t.Errorf("%s: still held at %v, past the calendar's horizon", c.a.Symbol, c.now)
		}
	}
	if _, ok := calendars[KRX].finalClose(local("Asia/Seoul", 2026, 10, 14, 12, 0)); ok {
		t.Error("a final close inside the trading day")
	}
	if at, ok := calendars[KRX].finalClose(local("Asia/Seoul", 2026, 10, 12, 8, 0)); !ok || !at.Equal(local("Asia/Seoul", 2026, 10, 8, 15, 30)) {
		t.Errorf("before Monday's open after the 9 October holiday: %v %v", at, ok)
	}
}

// recorder is a Yahoo source that records every Quotes call's tickers and
// answers every ticker at price 100, last traded at the clock's time.
type recorder struct {
	mu     sync.Mutex
	now    func() time.Time
	calls  [][]string
	fx     atomic.Int32
	period func(AssetInfo, time.Time) (*time.Time, *time.Time) // Yahoo's trading period; nil: none
	fail   map[string]bool
	block  chan struct{} // non-nil: every Quotes call waits for it
	byPair map[string]AssetInfo
	extra  map[string]AssetInfo // rows it serves besides the quote feeds (the watch tier)
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *recorder) Chart(context.Context, string, string, string) (yahoo.Chart, error) {
	return yahoo.Chart{}, &yahoo.UnavailableError{Reason: "no charts here"}
}
func (r *recorder) Quote(ctx context.Context, s string) (yahoo.Quote, error) {
	qs, err := r.Quotes(ctx, []string{s})
	return qs[s], err
}
func (r *recorder) FX(_ context.Context, ccy string) (yahoo.Rate, error) {
	r.fx.Add(1)
	return yahoo.Rate{Ccy: ccy, USD: 0.01, Time: r.now(), Symbol: ccy + "USD=X"}, nil
}
func (r *recorder) FXHistory(context.Context, string, string) ([]yahoo.Point, error) { return nil, nil }
func (r *recorder) Quotes(_ context.Context, symbols []string) (map[string]yahoo.Quote, error) {
	if r.block != nil {
		<-r.block
	}
	r.mu.Lock()
	r.calls = append(r.calls, slices.Clone(symbols))
	if r.byPair == nil {
		r.byPair = map[string]AssetInfo{}
		for _, a := range feedRows() {
			r.byPair[a.Pair] = a
		}
	}
	r.mu.Unlock()
	out := map[string]yahoo.Quote{}
	var err error
	now := r.now()
	for _, s := range symbols {
		if r.fail[s] {
			err = yahootest.Down
			continue
		}
		a, ok := r.byPair[s]
		if !ok {
			a, ok = r.extra[s] // a watch row
		}
		if !ok {
			continue // not served
		}
		var start, end *time.Time
		if r.period != nil && a.Listing != nil {
			start, end = r.period(a, now)
		}
		out[s] = yahoo.Quote{Symbol: s, Price: 100, Time: now, Currency: a.Listing.LocalCurrency, Meta: yahoo.Meta{RegularStart: start, RegularEnd: end}, Via: "spark"}
	}
	return out, err
}

func (r *recorder) quoteCalls() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

// callsSince is the Quotes calls after the first n.
func (r *recorder) callsSince(n int) [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls[n:])
}

// clocked is a market client over src whose quote-feed clock is *now.
func clocked(src yahoo.Source, now *time.Time) *Client {
	c := testMarketClient("")
	c.Yahoo = src
	c.Now = func() time.Time { return *now }
	return c
}

// refresh runs one poller step at the client's clock and waits for it.
func refresh(t *testing.T, c *Client) {
	t.Helper()
	if done, _ := c.startFeeds(context.Background(), c.now(), false); done != nil {
		<-done
	}
}

// Hot rows are read first, then named, then listed; no refresh makes more
// than 20 Quotes calls (the rest waits for the next).
func TestFeedPriorityAndCap(t *testing.T) {
	now := time.Date(2026, 10, 14, 1, 30, 0, 0, time.UTC) // TSE, KRX, HKEX, TWSE, SGX open
	r := &recorder{now: func() time.Time { return now }}
	c := clocked(r, &now)
	hot := []string{"C38U.SI", "6501.T", "000810.KS"}
	c.Touch(append(hot, "BTC", "MER.PS")...) // a crypto and a research-only ticker are ignored
	refresh(t, c)
	calls := r.quoteCalls()
	if len(calls) != feedMaxCalls || !slices.Equal(calls[0][:3], []string{"6501.T", "000810.KS", "C38U.SI"}) || calls[0][3] != QuoteFeeds[0].Pair {
		t.Fatalf("%d calls, first %v", len(calls), calls[0][:5])
	}
	var asked []string
	for _, call := range calls {
		if len(call) > feedBatch {
			t.Fatalf("a batch of %d", len(call))
		}
		asked = append(asked, call...)
	}
	if asked[3+len(QuoteFeeds)-1] != QuoteFeeds[len(QuoteFeeds)-1].Pair || !c.feeds.quotes["6501.T"].at.Equal(now) {
		t.Fatalf("named rows before listed: %v", asked[len(QuoteFeeds):len(QuoteFeeds)+5])
	}
	// The rest of the 742 waits for the next refresh: never-read first.
	refresh(t, c)
	if calls := r.quoteCalls(); len(calls) != feedMaxCalls+18 || len(c.feeds.quotes) != len(QuoteFeeds)+len(Listed) {
		t.Fatalf("%d calls, %d quotes", len(calls), len(c.feeds.quotes))
	}
	// A minute on, only the named and hot rows that are open are due.
	now = now.Add(time.Minute)
	refresh(t, c)
	last := r.quoteCalls()[feedMaxCalls+18:]
	var again []string
	for _, call := range last {
		again = append(again, call...)
	}
	// Hot 6501.T and named DBS (open), named BBCA (IDX, no Yahoo period here:
	// unknown counts as open); not listed 1332.T (open, read a minute ago) nor
	// named PTT (SET opens at 03:00 UTC) nor HSDT (NYSE closed).
	for sym, want := range map[string]bool{"6501.T": true, "D05.SI": true, "BBCA.JK": true, "1332.T": false, "PTT.BK": false, "HSDT": false} {
		if slices.Contains(again, sym) != want {
			t.Errorf("a minute on, %s read %v: %v", sym, !want, again)
		}
	}
	// Listed rows fall due oldest read first.
	now = now.Add(5 * time.Minute)
	c.feeds.mu.Lock()
	k := newFeedClock(now)
	rows, urgent := c.feeds.plan(k, false, true)
	c.feeds.mu.Unlock()
	if len(rows) != feedMaxCalls*feedBatch || !urgent || !rows[len(rows)-1].IsListed() {
		t.Fatalf("plan %d rows", len(rows))
	}
}

// A read waits for the refresh only when a named or hot row is due or
// unread: a due set of listed rows alone never holds it.
func TestAssetsDoesNotWaitForListed(t *testing.T) {
	now := time.Date(2026, 10, 14, 1, 30, 0, 0, time.UTC)
	r := &recorder{now: func() time.Time { return now }}
	c := clocked(r, &now)
	refresh(t, c)
	refresh(t, c) // every row read
	now = now.Add(5 * time.Minute)
	c.feeds.mu.Lock()
	for _, a := range QuoteFeeds { // the named rows were just read; the listed ones are 5 minutes old
		q := c.feeds.quotes[a.Symbol]
		q.at = now
		c.feeds.quotes[a.Symbol] = q
	}
	c.feeds.mu.Unlock()
	r.block = make(chan struct{})
	start := time.Now()
	rows := c.quoteFeedAssets(context.Background(), false)
	if waited := time.Since(start); waited > feedWait/2 || len(rows) != len(QuoteFeeds)+len(Listed) {
		t.Fatalf("waited %v for listed rows (%d rows)", waited, len(rows))
	}
	c.feeds.mu.Lock()
	running := c.feeds.running
	c.feeds.mu.Unlock()
	if running == nil {
		t.Fatal("no refresh started for the due listed rows")
	}
	// A named row due: the read waits (here until the refresh it joins ends).
	c.feeds.mu.Lock()
	q := c.feeds.quotes["DBS"]
	q.at = now.Add(-time.Hour)
	c.feeds.quotes["DBS"] = q
	c.feeds.mu.Unlock()
	go func() { time.Sleep(50 * time.Millisecond); close(r.block) }()
	start = time.Now()
	c.quoteFeedAssets(context.Background(), false)
	if waited := time.Since(start); waited < 40*time.Millisecond {
		t.Fatalf("did not wait for a due named row (%v)", waited)
	}
}

// One failing batch delays only its own rows; the rest land.
func TestFeedBatchFailureIsolated(t *testing.T) {
	now := time.Date(2026, 10, 14, 1, 30, 0, 0, time.UTC)
	r := &recorder{now: func() time.Time { return now }, fail: map[string]bool{}}
	for _, a := range QuoteFeeds[20:40] { // the second batch
		r.fail[a.Pair] = true
	}
	c := clocked(r, &now)
	refresh(t, c)
	c.feeds.mu.Lock()
	_, bad := c.feeds.quotes[QuoteFeeds[25].Symbol]
	_, good := c.feeds.quotes[QuoteFeeds[45].Symbol]
	failed := len(c.feeds.failedAt)
	c.feeds.mu.Unlock()
	if bad || !good || failed != 20 {
		t.Fatalf("bad %v good %v failed %d", bad, good, failed)
	}
	// Within feedRetry the failed rows wait; then they are first among the named rows again.
	n := len(r.quoteCalls())
	now = now.Add(10 * time.Second)
	refresh(t, c) // the listed rows the first refresh's cap left
	for _, call := range r.quoteCalls()[n:] {
		if slices.Contains(call, QuoteFeeds[25].Pair) {
			t.Fatal("a failed row re-read within feedRetry")
		}
	}
	now = now.Add(feedRetry)
	delete(r.fail, QuoteFeeds[25].Pair)
	n = len(r.quoteCalls())
	refresh(t, c)
	if calls := r.quoteCalls()[n:]; len(calls) == 0 || !slices.Contains(calls[0], QuoteFeeds[25].Pair) {
		t.Fatalf("the failed rows were not retried: %v", calls)
	}
}

// RunFeeds refreshes listed rows with no reader, re-reads Interest at most
// once a minute, and stops with its context.
func TestRunFeedsWarm(t *testing.T) {
	f := yahootest.New()
	now := time.Now().UTC().Truncate(time.Second)
	h := yahootest.Daily("6501.T", "JPY", 4800, 4850)
	h.Meta.PriceTime = &now
	f.Set(h)
	f.SetFX("JPY", 0.0066)
	c := testMarketClient("")
	c.Yahoo = f
	var interest atomic.Int32
	c.Interest = func(context.Context) ([]string, error) {
		interest.Add(1)
		return []string{"6501.t", "BTC", "nope"}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { c.RunFeeds(ctx); close(stopped) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		c.feeds.mu.Lock()
		_, ok := c.feeds.quotes["6501.T"]
		hot := c.feeds.interest["6501.T"]
		c.feeds.mu.Unlock()
		if ok && hot {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("RunFeeds did not read the listed row")
		}
		time.Sleep(10 * time.Millisecond)
	}
	c.feedStep(ctx) // within the minute: no second Interest read
	if interest.Load() != 1 || len(c.feeds.interest) != 1 {
		t.Fatalf("interest read %d times: %v", interest.Load(), c.feeds.interest)
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("RunFeeds did not stop")
	}
	(&Client{}).feedStep(context.Background()) // no Yahoo: nothing to do
}

// A held listed row (Interest) is read every minute while open, not every
// five; a failed Interest read keeps the last set.
func TestInterestHot(t *testing.T) {
	now := time.Date(2026, 10, 14, 1, 30, 0, 0, time.UTC)
	r := &recorder{now: func() time.Time { return now }}
	c := clocked(r, &now)
	fail := false
	c.Interest = func(context.Context) ([]string, error) {
		if fail {
			return nil, yahootest.Down
		}
		return []string{"000810.KS"}, nil
	}
	idle := Listed[slices.IndexFunc(Listed, func(a AssetInfo) bool { return a.Listing.SessionVenue == KRX && a.Symbol != "000810.KS" })]
	ctx := context.Background()
	c.refreshInterest(ctx, now)
	refresh(t, c)
	refresh(t, c)
	for range 3 {
		now = now.Add(time.Minute)
		n := len(r.quoteCalls())
		refresh(t, c)
		var asked []string
		for _, call := range r.quoteCalls()[n:] {
			asked = append(asked, call...)
		}
		if len(asked) == 0 || asked[0] != "000810.KS" {
			t.Fatalf("held row not read first a minute on: %v", asked[:min(3, len(asked))])
		}
		if slices.Contains(asked, idle.Pair) {
			t.Fatalf("idle listed %s read a minute on", idle.Symbol)
		}
	}
	fail = true
	c.refreshInterest(ctx, now.Add(time.Minute))
	if !c.feeds.interest["000810.KS"] {
		t.Fatal("a failed Interest read dropped the set")
	}
}

// With spark off (each quote would be its own chart read), the poller reads
// named and hot rows only; the rest keep their last quote.
func TestSparkOffReadsNamedAndHotOnly(t *testing.T) {
	now := time.Date(2026, 10, 14, 1, 30, 0, 0, time.UTC)
	f := yahootest.New()
	f.SetBatched(false)
	c := clocked(f, &now)
	c.Touch("6501.T")
	refresh(t, c)
	var asked int
	for _, n := range f.Batches() {
		asked += n
	}
	if asked != len(QuoteFeeds)+1 || f.Calls("6501.T") != 1 || f.Calls("1332.T") != 0 {
		t.Fatalf("asked %d (%v)", asked, f.Batches())
	}
	f.SetBatched(true)
	now = now.Add(feedRetry) // the rows the fake does not serve failed; a moment later the listed rows come too
	refresh(t, c)
	if f.Calls("1332.T") != 1 {
		t.Fatal("listed rows not read once spark is back")
	}
}

// FX refreshes every 5 minutes while a venue quoting in it is open, every 30
// while all are closed.
func TestFXHoldWhileClosed(t *testing.T) {
	jp := listed(t, "6501.T")
	open := local("Asia/Tokyo", 2026, 10, 14, 10, 0)
	closed := local("Asia/Tokyo", 2026, 10, 14, 20, 0)
	f := &feedState{fx: map[string]feedFX{}}
	for _, c := range []struct {
		now     time.Time
		age     time.Duration
		due     bool
		failing bool
	}{
		{open, 4 * time.Minute, false, false}, {open, 5 * time.Minute, true, false},
		{closed, 10 * time.Minute, false, false}, {closed, 29 * time.Minute, false, false}, {closed, 30 * time.Minute, true, false},
		{closed, time.Hour, false, true},
	} {
		f.store([]AssetInfo{jp}, map[string]yahoo.Quote{"6501.T": readAt(jp, c.now, nil, nil).q}, c.now)
		f.fx["JPY"] = feedFX{yahoo.Rate{Ccy: "JPY", USD: 0.0066}, c.now.Add(-c.age)}
		delete(f.failedAt, "fx:JPY")
		if c.failing {
			f.failedAt["fx:JPY"] = c.now.Add(-time.Second)
		}
		if got := f.dueFX(newFeedClock(c.now)); slices.Contains(got, "JPY") != c.due {
			t.Errorf("%v, rate %v old: due %v", c.now, c.age, got)
		}
	}
	// A USD row needs no rate; an IDX rate follows its rows' Yahoo period.
	g := &feedState{}
	d01, bbni := listed(t, "D01.SI"), listed(t, "BBNI.JK")
	is, ie := local("Asia/Jakarta", 2026, 10, 14, 9, 0), local("Asia/Jakarta", 2026, 10, 14, 16, 15)
	idxOpen := local("Asia/Jakarta", 2026, 10, 14, 10, 0)
	g.store([]AssetInfo{d01, bbni}, map[string]yahoo.Quote{"D01.SI": {Price: 3, Time: idxOpen, Currency: "USD"},
		"BBNI.JK": {Price: 4000, Time: idxOpen, Currency: "IDR", Meta: yahoo.Meta{RegularStart: &is, RegularEnd: &ie}}}, idxOpen)
	g.fx = map[string]feedFX{"IDR": {yahoo.Rate{Ccy: "IDR", USD: 0.00006}, idxOpen.Add(-10 * time.Minute)}}
	if got := g.dueFX(newFeedClock(idxOpen)); !slices.Equal(got, []string{"IDR"}) {
		t.Fatalf("IDX open, rate 10 minutes old: %v", got)
	}
	g.fx["IDR"] = feedFX{yahoo.Rate{Ccy: "IDR", USD: 0.00006}, ie.Add(31 * time.Minute)}
	if got := g.dueFX(newFeedClock(ie.Add(time.Hour))); len(got) != 0 {
		t.Fatalf("IDX closed, rate 29 minutes old: %v", got)
	}
}

// A closed quote feed's candles are read once per feedClosedTTL; open, they
// follow the usual cache.
func TestQuoteFeedClosedCandlesCache(t *testing.T) {
	c, f := feedClient(t)
	c.Now = func() time.Time { return local("Asia/Tokyo", 2026, 10, 14, 20, 0) } // TSE closed
	f.Set(yahootest.Daily("6501.T", "JPY", 4800, 4850))
	f.SetFX("JPY", 0.0066)
	ctx := context.Background()
	for range 3 {
		if _, err := c.Candles(ctx, "6501.T", "15m", 10); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.Calls("6501.T"); n != 1 {
		t.Fatalf("%d chart reads while closed", n)
	}
	c.mu.Lock()
	_, ok := c.candles["6501.T/15m/1000/closed"]
	c.mu.Unlock()
	if !ok {
		t.Fatal("no closed-session cache entry")
	}
	c.Now = func() time.Time { return local("Asia/Tokyo", 2026, 10, 14, 10, 0) }
	c.Candles(ctx, "6501.T", "15m", 10)
	if n := f.Calls("6501.T"); n != 2 {
		t.Fatalf("open session reused the closed read: %d", n)
	}
}

// yahooPeriod is a stand-in for Yahoo's current trading period on 2026-10-13
// and 10-14 (Tue, Wed): the regular session of the venue's local date, Yahoo's
// KRX period ending at 15:00 and HKEX's at 16:10, IDX 09:00–16:15, Bursa
// 09:00–17:00 and HOSE 09:00–15:00 with no lunch.
var periodZones sync.Map

func yahooPeriod(a AssetInfo, now time.Time) (*time.Time, *time.Time) {
	type hm struct{ h, m int }
	p := map[string][3]any{
		NYSE: {"America/New_York", hm{9, 30}, hm{16, 0}}, TSE: {"Asia/Tokyo", hm{9, 0}, hm{15, 30}}, KRX: {"Asia/Seoul", hm{9, 0}, hm{15, 0}},
		HKEX: {"Asia/Hong_Kong", hm{9, 30}, hm{16, 10}}, SGX: {"Asia/Singapore", hm{9, 0}, hm{17, 0}}, TWSE: {"Asia/Taipei", hm{9, 0}, hm{13, 30}},
		SET: {"Asia/Bangkok", hm{10, 0}, hm{16, 30}}, IDX: {"Asia/Jakarta", hm{9, 0}, hm{16, 15}}, BURSA: {"Asia/Kuala_Lumpur", hm{9, 0}, hm{17, 0}},
		HOSE: {"Asia/Ho_Chi_Minh", hm{9, 0}, hm{15, 0}},
	}[a.Listing.SessionVenue]
	z, ok := periodZones.Load(p[0])
	if !ok {
		z, _ = periodZones.LoadOrStore(p[0], zone(p[0].(string)))
	}
	loc := z.(*time.Location)
	l := now.In(loc)
	o, e := p[1].(hm), p[2].(hm)
	start := time.Date(l.Year(), l.Month(), l.Day(), o.h, o.m, 0, 0, loc)
	end := time.Date(l.Year(), l.Month(), l.Day(), e.h, e.m, 0, 0, loc)
	return &start, &end
}

// The poller's Yahoo budget over an ordinary weekday (2026-10-14, after a day
// of warm-up) with every quote feed and 45 hot listed rows (5 per venue), in
// 15 s ticks: the design's simulation gives 7,015 quote calls and 984 FX
// reads. With the screener's 62 watch rows (as many per venue as
// screener.QuoteWatch leaves the tier) the batches they share with due
// listed rows add about 90 calls.
func TestFeedBudgetWeekday(t *testing.T) {
	if testing.Short() {
		t.Skip("a two-day simulation")
	}
	var watch []WatchRow
	for venue, n := range map[string]int{TSE: 7, KRX: 31, TWSE: 4, HKEX: 7, IDX: 6, SET: 2, BURSA: 1, HOSE: 4} {
		for i := range n {
			watch = append(watch, WatchRow{Ticker: fmt.Sprintf("ZZ%s%02d.X", venue, i), Venue: venue, Currency: listedVenues[venue][0]})
		}
	}
	slices.SortFunc(watch, func(a, b WatchRow) int { return strings.Compare(a.Ticker, b.Ticker) })
	var quotes, fx, withWatch, fxWatch, reads, peak int
	t.Run("days", func(t *testing.T) { // the two simulations side by side
		t.Run("shipped", func(t *testing.T) { t.Parallel(); quotes, fx, _, _ = budgetDay(t, nil) })
		t.Run("watch", func(t *testing.T) { t.Parallel(); withWatch, fxWatch, reads, peak = budgetDay(t, watch) })
	})
	if t.Failed() {
		return
	}
	if quotes > 7700 || fx > 1000 {
		t.Fatalf("%d quote calls, %d FX reads", quotes, fx)
	}
	t.Logf("the watch tier adds %d quote calls a day (%d watch-row reads) and %d FX reads; peak minute %d",
		withWatch-quotes, reads, fxWatch-fx, peak)
	if withWatch-quotes > 150 || fxWatch != fx || reads < 62*2 {
		t.Fatalf("watch tier: %d more calls, %d more FX reads, %d reads", withWatch-quotes, fxWatch-fx, reads)
	}
}

// budgetDay simulates 2026-10-14 after a day of warm-up and returns its quote
// calls, FX reads, watch-row reads and peak calls in a minute.
func budgetDay(t *testing.T, watch []WatchRow) (quotes, fx, watchReads, peak int) {
	t.Helper()
	day := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	now := day.Add(-24 * time.Hour) // a day of warm-up, as the design's simulation runs
	r := &recorder{now: func() time.Time { return now }, period: yahooPeriod, extra: map[string]AssetInfo{}}
	c := clocked(r, &now)
	if c.Watch(watch) != len(watch) {
		t.Fatal("watch rows refused")
	}
	for _, a := range c.feeds.watch {
		r.extra[a.Pair] = a
	}
	hot := map[string]int{}
	var interest []string
	for _, a := range Listed {
		if v := a.Listing.SessionVenue; hot[v] < 5 {
			hot[v]++
			interest = append(interest, a.Symbol)
		}
	}
	c.Interest = func(context.Context) ([]string, error) { return interest, nil }
	ctx := context.Background()
	var warmCalls, warmFX int
	perHour := make([]int, 24)
	perMinute := map[int]int{}
	for ; now.Before(day.Add(24 * time.Hour)); now = now.Add(feedTick) {
		if now.Equal(day) {
			warmCalls, warmFX = r.count(), int(r.fx.Load())
		}
		n := r.count()
		c.refreshInterest(ctx, now)
		if done, _ := c.startFeeds(ctx, now, false); done != nil {
			<-done
		}
		if !now.Before(day) {
			got := r.count() - n
			perHour[now.Hour()] += got
			perMinute[int(now.Sub(day)/time.Minute)] += got
			if got > feedMaxCalls {
				t.Fatalf("%d calls in one refresh at %v", got, now)
			}
			for _, call := range r.callsSince(n) {
				for _, s := range call {
					if _, ok := r.extra[s]; ok {
						watchReads++
					}
				}
			}
		}
	}
	quotes, fx = r.count()-warmCalls, int(r.fx.Load())-warmFX
	busiest := 0
	for h, n := range perHour {
		if n > perHour[busiest] {
			busiest = h
		}
	}
	for _, n := range perMinute {
		peak = max(peak, n)
	}
	t.Logf("2026-10-14, %d watch rows: %d quote calls and %d FX reads (%d a day); busiest hour %02d:00 UTC with %d quote calls; peak minute %d; by hour %v",
		len(watch), quotes, fx, quotes+fx, busiest, perHour[busiest], peak, perHour)
	if len(interest) != 45 {
		t.Fatalf("%d hot rows", len(interest))
	}
	return quotes, fx, watchReads, peak
}

func TestFeedClockAndHelpers(t *testing.T) {
	k := newFeedClock(local("Asia/Seoul", 2026, 10, 14, 10, 0))
	naver := named(t, "NAVER")
	if k.state(naver, yahoo.Meta{}) != "open" || k.known[KRX] != "open" {
		t.Fatal("KRX state")
	}
	if s := k.state(AssetInfo{}, yahoo.Meta{}); s != "unknown" {
		t.Fatalf("no listing: %s", s)
	}
	if settledRead(AssetInfo{}, nil, time.Now()) {
		t.Fatal("settled without a listing")
	}
	var nilCal *calendar
	if _, ok := nilCal.finalClose(time.Now()); ok {
		t.Fatal("nil calendar")
	}
	if !strings.Contains(ListedPattern, "KS") {
		t.Fatal(ListedPattern)
	}
}

// paused is a Yahoo source whose breaker is open.
type paused struct {
	recorder
	n atomic.Int32
}

func (p *paused) Quotes(context.Context, []string) (map[string]yahoo.Quote, error) {
	p.n.Add(1)
	return nil, &yahoo.UnavailableError{Reason: "rate limited by Yahoo; paused until 09:14 UTC", Paused: true}
}

// The open Yahoo breaker pauses the whole feed: one refused call stops the
// refresh, no row is marked failed, and nothing is asked for feedRetry.
func TestFeedPausesOnBreaker(t *testing.T) {
	now := time.Date(2026, 10, 14, 1, 30, 0, 0, time.UTC)
	p := &paused{recorder: recorder{now: func() time.Time { return now }}}
	c := clocked(p, &now)
	refresh(t, c)
	if p.n.Load() != 1 || len(c.feeds.failedAt) != 0 || !c.feeds.paused.Equal(now.Add(feedRetry)) {
		t.Fatalf("%d calls, %d failed, paused until %v", p.n.Load(), len(c.feeds.failedAt), c.feeds.paused)
	}
	now = now.Add(feedRetry - time.Second)
	if done, urgent := c.startFeeds(context.Background(), now, false); done != nil || !urgent || p.n.Load() != 1 {
		t.Fatalf("refreshed while paused (%v)", urgent)
	}
	now = now.Add(time.Second)
	refresh(t, c)
	if p.n.Load() != 2 {
		t.Fatal("not resumed after the pause")
	}
}

// FillAssets reads a quote-feed row before a fill only when its last read is
// over a minute old (or missing), keeps the last quote when the read fails,
// and still reports a stale row halted.
func TestFillAssetsReadsOnce(t *testing.T) {
	c, f := feedClient(t)
	ctx := context.Background()
	rows, err := c.FillAssets(ctx, []string{"dbs", "BTC", "DBS"})
	if err != nil || len(rows) != 1 || !approxEq(rows["DBS"].Price, 78.37*0.78) || rows["DBS"].SessionVenue == nil || *rows["DBS"].SessionVenue != SGX ||
		rows["DBS"].QuoteFeed.MarketState == "" || len(f.Batches()) != 1 || f.Batches()[0] != 1 {
		t.Fatalf("first fill read: %+v %v %v", rows, err, f.Batches())
	}
	if _, err := c.FillAssets(ctx, []string{"DBS"}); err != nil || len(f.Batches()) != 1 {
		t.Fatalf("re-read under a minute: %v", f.Batches())
	}
	age := func(sym string, d time.Duration) {
		c.feeds.mu.Lock()
		q := c.feeds.quotes[sym]
		q.at = q.at.Add(-d)
		c.feeds.quotes[sym] = q
		c.feeds.mu.Unlock()
	}
	age("DBS", freshFill+time.Second)
	if _, err := c.FillAssets(ctx, []string{"DBS"}); err != nil || len(f.Batches()) != 2 {
		t.Fatalf("no re-read over a minute: %v", f.Batches())
	}
	// A failed read keeps the last quote.
	f.SetErr("D05.SI", yahootest.Down)
	age("DBS", freshFill+time.Second)
	rows, err = c.FillAssets(ctx, []string{"DBS"})
	if err == nil || rows["DBS"].QuoteFeed == nil || rows["DBS"].QuoteFeed.LocalPrice != 78.37 || rows["DBS"].Halted {
		t.Fatalf("after a failed read: %+v %v", rows, err)
	}
	// A stale quote is still halted.
	old := time.Now().Add(-QuoteStaleAfter - time.Hour)
	se := yahootest.Daily("SE", "USD", 96, 96.85)
	se.Meta.PriceTime = &old
	f.Set(se)
	if rows, _ := c.FillAssets(ctx, []string{"SE"}); !rows["SE"].Halted || !rows["SE"].QuoteFeed.Stale {
		t.Fatalf("stale SE %+v", rows["SE"])
	}
	// No source, no rows; a crypto symbol is not a quote feed.
	if rows, err := (&Client{}).FillAssets(ctx, []string{"DBS"}); err != nil || len(rows) != 0 {
		t.Fatal("no source")
	}
	if rows, err := c.FillAssets(ctx, []string{"BTC"}); err != nil || len(rows) != 0 {
		t.Fatal("crypto")
	}
	// Quote reads an unread listed row instead of reporting no price.
	h := yahootest.Daily("6501.T", "JPY", 4800, 4850)
	now := time.Now().UTC().Truncate(time.Second)
	h.Meta.PriceTime = &now
	f.Set(h)
	f.SetFX("JPY", 0.0066)
	a, err := c.Quote(ctx, "6501.t")
	c.feeds.mu.Lock()
	hot := c.feeds.hot("6501.T", c.now())
	c.feeds.mu.Unlock()
	if err != nil || !approxEq(a.Price, 4850*0.0066) || !hot {
		t.Fatalf("listed quote %+v %v", a, err)
	}
	if _, err := c.Quote(ctx, "7011.T"); err == nil {
		t.Fatal("a row Yahoo does not serve has no price")
	}
}

package market

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"slices"
	"sync"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo"
)

// Quote-feed stocks (universe_quotefeed.go's named rows and the listed rows of
// universe_listed.go) are priced from the keyless Yahoo feed: the listing's
// regular-market price (during its regular session Yahoo's latest
// regular-market trade, delayed: measured 2026-10-13 as the newest trade's age
// while open, KRX and TWSE 20 minutes, TSE, HKEX, Bursa and HOSE 15, SGX and
// IDX 10, SET 30 on 2026-10-14 (yahooDelay); US listings not yet measured; the
// last close outside it)
// times Yahoo's {CCY}USD=X rate for a non-USD listing, so every price here is
// USD.
//
// Quotes go through the shared, rate-gated Yahoo client in spark batches of
// 20, at most feedMaxCalls batches a refresh (the rest waits for the next).
// A named row, and a hot listed one (held, in an active alert, the symbol of a
// running autopilot, or Touched by a chart, an analysis or a quote in the last
// 10 minutes), is read every minute while its session is open and every 15
// minutes while it is closed. Any other listed row is read every 5 minutes
// while open and every 15 while closed, except that on a venue whose verified
// calendar knows the session, a read at least an hour after the later of the
// calendar's final close and Yahoo's regular end (once passed) holds it until
// the next open (KRX from 16:30 KST, HKEX from 17:10 HKT); past the calendar's
// horizon nothing is held. Every row is read at once when
// its session has opened or closed since its last read; an unknown session
// counts as open. A refresh reads hot rows never read, hot rows due, named
// rows due, listed rows never read, then listed rows due, oldest first; while
// the Yahoo client is off spark batches (a quote then costs a chart read),
// only named and hot rows. The screener's watch tier (listingquote.go) comes
// after them all. FX refreshes every 5 minutes while some venue
// quoting in that currency is open, else every 30. A row whose read failed is
// not read again for feedRetry; the open Yahoo breaker pauses the whole feed.
// RunFeeds keeps it warm every 15 s; without the loop, Assets starts the same
// refresh lazily. One refresh runs at a time, detached from the read that
// started it: a read waits for it at most feedWait, and only when a named or
// hot row is due or unread, so batch jobs queued on the same Yahoo budget (signals,
// the screener) delay a refresh without failing it. FillAssets re-reads a
// quote over a minute old before a paper fill. A failed refresh keeps the last
// measured quote, whose asOf says how old it is; one over QuoteStaleAfter
// whose venue has traded since (or whose venue has no verified calendar), or
// one not re-read for QuoteSilentAfter while its session is open, halts
// orders. Unlike the perps these do not trade around the clock: outside the
// session a paper fill uses the last close, and the asset says so
// (QuoteFeedInfo.MarketState).

const (
	feedOpenTTL       = time.Minute      // a named or hot row while its session is open
	feedListedOpenTTL = 5 * time.Minute  // any other listed row while open
	feedClosedTTL     = 15 * time.Minute // while closed: the price does not move
	feedSettle        = time.Hour        // after the close: Yahoo's delayed closing trade has landed
	feedFXTTL         = 5 * time.Minute  // a rate some open venue quotes in
	feedFXClosedTTL   = 30 * time.Minute // a rate whose venues are all closed
	feedRetry         = 30 * time.Second // a row (or rate) whose read failed is not due again before this
	feedWait          = 5 * time.Second  // a refresh never holds Assets longer
	feedTimeout       = 45 * time.Second // a detached refresh's own bound, pacing waits included
	feedBatch         = 20               // quotes per batch: one Yahoo spark call
	feedMaxCalls      = 20               // batches per refresh; the rest waits for the next one
	feedTick          = 15 * time.Second // RunFeeds' step
	feedHotFor        = 10 * time.Minute // a Touched row stays hot this long
	feedInterestTTL   = time.Minute      // Interest is re-read at most this often
	freshFill         = time.Minute      // FillAssets re-reads a quote older than this

	// QuoteStaleAfter is how old a quote-feed listing's latest regular-market
	// price may be before it counts as halted: a long weekend with a holiday
	// is under it, a feed that stopped answering is not. Past it a quote stays
	// current while its venue's verified calendar says no regular session has
	// been open 30 minutes since (KRX's and TSE's year-end, Lunar New Year,
	// Golden Week); a venue with no calendar, or dates past its horizon, keep
	// the 4-day rule.
	QuoteStaleAfter = 4 * 24 * time.Hour
	// QuoteSilentAfter is how long a listing may go without a successful read
	// while its regular session is open before it counts as halted too: the
	// feed stopped answering while the listing trades, so the last price no
	// longer follows the market. It outlasts a closed-session read
	// (feedClosedTTL) carried into the open plus a slow refresh.
	QuoteSilentAfter = 20 * time.Minute
)

// QuoteFeedInfo is what a quote-feed row's USD price is made of, and how old
// it is (Asset.QuoteFeed; nil on every other row).
type QuoteFeedInfo struct {
	Symbol        string     `json:"symbol"`        // Yahoo ticker: "D05.SI", "HSDT"
	Exchange      string     `json:"exchange"`      // Yahoo's exchange name: "SES", "NasdaqCM"
	Currency      string     `json:"currency"`      // the local price's currency as Yahoo labels it
	LocalPrice    float64    `json:"localPrice"`    // regular-market price in Currency
	PreviousClose *float64   `json:"previousClose"` // the previous session's close in Currency; nil when unknown
	FX            float64    `json:"fx"`            // USD per 1 unit of Currency (1 for USD)
	FXSymbol      string     `json:"fxSymbol"`      // "SGDUSD=X"; "" for USD
	FXAsOf        *time.Time `json:"fxAsOf"`        // the rate's time; nil for USD
	AsOf          time.Time  `json:"asOf"`          // regularMarketTime: when LocalPrice was set
	// MarketState is the listing's regular session at read time: "open" (the
	// price follows it), "closed" (the price is the last close) or "unknown".
	// It follows SessionVenue's verified calendar, else Yahoo's own current
	// trading period for the listing (no lunch break there).
	MarketState string `json:"marketState"`
	// Stale: AsOf over QuoteStaleAfter old unless SessionVenue's verified
	// calendar shows no regular session open 30 minutes since AsOf (an
	// exchange holiday), or no read for QuoteSilentAfter while MarketState is
	// open. The row is Halted.
	Stale bool `json:"stale"`
	// Source is whose price this is: "yahoo", or a real-time adapter's name
	// ("naver"); Class "delayed" (Yahoo), "realtime" or "near-realtime".
	Source string `json:"source"`
	Class  string `json:"class"`
	// LastTradeAt is the price's own last-trade time (= AsOf), ReadAt this
	// server's read of it, DelaySeconds min(ReadAt, the session's last open
	// moment) − LastTradeAt (computed, never assumed), TimeBasis what
	// LastTradeAt is ("trade": the source's trade time; "volume": the first
	// read showing the current cumulative volume; "snapshot": the source's
	// snapshot time) and Label the honest label (QuoteLabel).
	LastTradeAt  time.Time `json:"lastTradeAt"`
	ReadAt       time.Time `json:"readAt"`
	DelaySeconds float64   `json:"delaySeconds"`
	TimeBasis    string    `json:"timeBasis"`
	Label        string    `json:"label"`

	regularStart, regularEnd *time.Time // Yahoo's current trading period
}

// feedState is the last measured quote of each quote-feed row, the FX rates
// and what the poller knows about interest.
type feedState struct {
	mu       sync.Mutex
	quotes   map[string]feedQuote // by symbol
	fx       map[string]feedFX    // by Yahoo currency label
	failedAt map[string]time.Time // a row's (or "fx:" + currency's) last failed read
	paused   time.Time            // the Yahoo breaker was open: no refresh before this
	failing  bool                 // the last refresh failed (logged once per streak)
	// running is closed when the refresh in flight ends; nil when none runs.
	running    chan struct{}
	touched    map[string]time.Time // Touch, by symbol
	interest   map[string]bool      // Interest's quote-feed symbols
	interestAt time.Time            // the last Interest read (or attempt)
	// ccyVenues is each quoted non-USD currency's session venues, and
	// venueMeta each venue's newest read meta (its Yahoo trading period), for
	// dueFX.
	ccyVenues map[string]map[string]bool
	venueMeta map[string]yahoo.Meta
	// watch is the watch tier's rows (Watch) and watchBy their index by
	// ticker; their quotes are in quotes under the ticker.
	watch   []AssetInfo
	watchBy map[string]int
	// rt is the real-time sources' chains and quotes (realtime.go); nil until
	// UseRealtime, then every venue's chain, empty when switched off.
	rt *rtState
	// sessions caches each venue's session until its next boundary (sessionOf).
	sessions map[string]sessCache
}

type feedQuote struct {
	q       yahoo.Quote
	at      time.Time // when it was read
	state   string    // the listing's market state at at
	settled bool      // read feedSettle after the close on a verified calendar (settledRead)
	next    time.Time // a watch row's next read (watchNext); zero on every other row
}

// newFeedQuote is q read at at for row a.
func newFeedQuote(a AssetInfo, q yahoo.Quote, at time.Time) feedQuote {
	m := q.Meta
	return feedQuote{q: q, at: at, state: marketState(a, m.RegularStart, m.RegularEnd, at), settled: settledRead(a, m.RegularEnd, at)}
}

type feedFX struct {
	r  yahoo.Rate
	at time.Time
}

// feedRows are the quote-feed rows the poller reads: the named QuoteFeeds,
// then the listed instruments.
func feedRows() []AssetInfo { return slices.Concat(QuoteFeeds, Listed) }

// marketState is a's regular session at t: its venue's verified calendar,
// else the Yahoo trading period start/end.
func marketState(a AssetInfo, start, end *time.Time, t time.Time) string {
	if a.Listing != nil {
		if open, _, known := calendars[a.Listing.SessionVenue].session(t); known {
			if open {
				return "open"
			}
			return "closed"
		}
	}
	return periodState(start, end, t)
}

// periodState is "open" inside Yahoo's trading period [start, end),
// "closed" outside it, "unknown" without one.
func periodState(start, end *time.Time, t time.Time) string {
	if start == nil || end == nil {
		return "unknown"
	}
	if !t.Before(*start) && t.Before(*end) {
		return "open"
	}
	return "closed"
}

// settledRead reports whether a read of a at at came after its session had
// settled: on a venue whose verified calendar knows at, closed, and at least
// feedSettle after the later of the calendar's final close and Yahoo's regular
// end (KRX trades to 15:30 while Yahoo's period ends at 15:00; HKEX's period
// runs to 16:10). Yahoo's period counts only once it has ended: from local
// midnight Yahoo already reports the coming day's period (read 2026-10-13 on
// TSE and KRX at 00:10 local), which says nothing about the last close. A
// lunch break never settles. The price then holds to the next open.
func settledRead(a AssetInfo, regularEnd *time.Time, at time.Time) bool {
	if a.Listing == nil {
		return false
	}
	c := calendars[a.Listing.SessionVenue]
	if open, _, known := c.session(at); !known || open {
		return false
	}
	end, ok := c.finalClose(at)
	if !ok {
		return false
	}
	if regularEnd != nil && regularEnd.After(end) && !regularEnd.After(at) {
		end = *regularEnd
	}
	return !at.Before(end.Add(feedSettle))
}

// feedClock is one moment's market state per venue, each computed once: a
// venue whose verified calendar knows the moment has one state; any other row
// reads its own Yahoo period.
type feedClock struct {
	now   time.Time
	known map[string]string // venue → "open" | "closed" | "" (no verified calendar now)
}

func newFeedClock(now time.Time) *feedClock { return &feedClock{now: now, known: map[string]string{}} }

func (k *feedClock) state(a AssetInfo, m yahoo.Meta) string {
	if a.Listing != nil {
		if s := k.venue(a.Listing.SessionVenue); s != "" {
			return s
		}
	}
	return periodState(m.RegularStart, m.RegularEnd, k.now)
}

// venue is the venue's state from its verified calendar, "" when it has none
// now.
func (k *feedClock) venue(v string) string {
	s, ok := k.known[v]
	if !ok {
		switch open, _, known := calendars[v].session(k.now); {
		case !known:
		case open:
			s = "open"
		default:
			s = "closed"
		}
		k.known[v] = s
	}
	return s
}

// hot reports whether symbol's row is read like a named one: held, alerted or
// autopiloted (Interest), or Touched in the last feedHotFor. Callers hold mu.
func (f *feedState) hot(symbol string, now time.Time) bool {
	if f.interest[symbol] {
		return true
	}
	t, ok := f.touched[symbol]
	return ok && now.Sub(t) < feedHotFor
}

// due reports whether row a needs a read at k.now (q, ok: its last read).
// fresh (after a long decision) refreshes every named or hot row whose
// session is not closed. Callers hold mu.
func (f *feedState) due(a AssetInfo, q feedQuote, ok bool, k *feedClock, hot, fresh bool) bool {
	return f.dueTier(a, q, ok, k, !hot && a.IsListed(), fresh)
}

// dueTier is due for a row of the quiet (listed, not hot) tier or not.
func (f *feedState) dueTier(a AssetInfo, q feedQuote, ok bool, k *feedClock, quiet, fresh bool) bool {
	if t, failed := f.failedAt[a.Symbol]; failed && k.now.Sub(t) < feedRetry {
		return false
	}
	if !ok {
		return true
	}
	state := k.state(a, q.q.Meta)
	switch {
	case state != q.state:
		return true // the session opened or closed since the read
	case state == "closed" && quiet && q.settled && k.venue(a.Listing.SessionVenue) != "":
		return false // held: the next open flips the state (past the calendar's horizon nothing would)
	case state == "closed":
		return k.now.Sub(q.at) >= feedClosedTTL
	case quiet:
		return k.now.Sub(q.at) >= feedListedOpenTTL
	}
	return fresh || k.now.Sub(q.at) >= feedOpenTTL
}

// plan is the next refresh's rows in priority order, at most feedMaxCalls
// batches: hot rows never read, hot rows due, named rows due, listed rows
// never read, listed rows due (oldest read first), then the watch tier's the
// same way. Without batched quotes, only named and hot rows. urgent: a named or hot row is due or
// unread, so a reader waits for the refresh. Callers hold mu.
func (f *feedState) plan(k *feedClock, fresh, batched bool) (rows []AssetInfo, urgent bool) {
	var hotNew, hotDue, named, listedNew, listedDue []AssetInfo
	for i, tier := range [][]AssetInfo{QuoteFeeds, Listed} {
		isListed := i == 1
		for _, a := range tier {
			q, ok := f.quotes[a.Symbol]
			hot := (len(f.interest) > 0 || len(f.touched) > 0) && f.hot(a.Symbol, k.now)
			if !f.dueTier(a, q, ok, k, isListed && !hot, fresh) {
				continue
			}
			switch {
			case hot && !ok:
				hotNew = append(hotNew, a)
			case hot:
				hotDue = append(hotDue, a)
			case !isListed:
				named = append(named, a)
			case !ok:
				listedNew = append(listedNew, a)
			default:
				listedDue = append(listedDue, a)
			}
		}
	}
	var watchNew, watchDue []AssetInfo
	for _, a := range f.watch {
		switch q, ok := f.quotes[a.Symbol]; {
		case !f.dueWatch(a, q, ok, k.now):
		case ok:
			watchDue = append(watchDue, a)
		default:
			watchNew = append(watchNew, a)
		}
	}
	byRead := func(x, y AssetInfo) int { return f.quotes[x.Symbol].at.Compare(f.quotes[y.Symbol].at) }
	slices.SortStableFunc(listedDue, byRead)
	slices.SortStableFunc(watchDue, byRead)
	rows = slices.Concat(hotNew, hotDue, named)
	urgent = len(rows) > 0
	if batched {
		rows = slices.Concat(rows, listedNew, listedDue, watchNew, watchDue)
	}
	return rows[:min(len(rows), feedMaxCalls*feedBatch)], urgent
}

// now is the quote feeds' clock.
func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// batched reports whether the Yahoo source reads quotes in batches now (a
// source that cannot say is taken to).
func (c *Client) batched() bool {
	b, ok := c.Yahoo.(yahoo.Batcher)
	return !ok || b.Batched()
}

// startFeeds starts a detached refresh of what is due at now unless one is
// running (or the Yahoo breaker paused the feed), and returns the running
// refresh (nil when none) and whether a named or hot row is due or unread.
func (c *Client) startFeeds(ctx context.Context, now time.Time, fresh bool) (chan struct{}, bool) {
	batched := c.batched()
	f := &c.feeds
	f.mu.Lock()
	defer f.mu.Unlock()
	k := newFeedClock(now)
	rows, urgent := f.plan(k, fresh, batched)
	if f.running != nil || now.Before(f.paused) {
		return f.running, urgent
	}
	if len(rows) == 0 && len(f.dueFX(k)) == 0 {
		return nil, false
	}
	done := make(chan struct{})
	f.running = done
	go func() {
		defer close(done)
		c.refreshFeeds(context.WithoutCancel(ctx), rows)
	}()
	return done, urgent
}

// quoteFeedAssets is the quote-feed rows of Assets, by symbol: a wait of at
// most feedWait for the refresh of what is due when a named or hot row is
// (started here unless one is already running), then every row with a
// measured quote and USD rate. nil without a Yahoo source (the rows are left
// out).
func (c *Client) quoteFeedAssets(ctx context.Context, fresh bool) map[string]Asset {
	if c.Yahoo == nil {
		return nil
	}
	now := c.now()
	if done, urgent := c.startFeeds(ctx, now, fresh); done != nil && urgent {
		wait := time.NewTimer(feedWait)
		select {
		case <-done:
		case <-wait.C:
		case <-ctx.Done():
		}
		wait.Stop()
	}
	f := &c.feeds
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]Asset, len(f.quotes))
	for _, tier := range [][]AssetInfo{QuoteFeeds, Listed} {
		for _, a := range tier {
			if as, ok := f.asset(a, now); ok {
				out[a.Symbol] = as
			}
		}
	}
	return out
}

// asset is row a's Assets row at now from its freshest valid quote (served:
// a real-time source's or Yahoo's) and USD rate, if both are known. Callers
// hold mu.
func (f *feedState) asset(a AssetInfo, now time.Time) (Asset, bool) {
	sq, ok := f.served(a, now)
	if !ok {
		return Asset{}, false
	}
	var rate yahoo.Rate
	if ccy := sq.fq.q.Currency; ccy == "USD" {
		rate = yahoo.Rate{Ccy: "USD", USD: 1}
	} else if fx, ok := f.fx[ccy]; ok {
		rate = fx.r
	} else {
		return Asset{}, false // no USD price without a rate
	}
	return feedAsset(a, sq, rate, now)
}

// dueFX is the quoted rows' currencies whose rate is missing or older than
// its TTL: feedFXTTL while a venue quoting in it is open (or its session is
// unknown), feedFXClosedTTL while every one is closed. Callers hold mu.
func (f *feedState) dueFX(k *feedClock) []string {
	var out []string
	for _, ccy := range slices.Sorted(maps.Keys(f.ccyVenues)) {
		if t, failed := f.failedAt["fx:"+ccy]; failed && k.now.Sub(t) < feedRetry {
			continue
		}
		ttl := feedFXClosedTTL
		for v := range f.ccyVenues[ccy] {
			s := k.venue(v)
			if s == "" {
				m := f.venueMeta[v]
				s = periodState(m.RegularStart, m.RegularEnd, k.now)
			}
			if s != "closed" {
				ttl = feedFXTTL
				break
			}
		}
		if r, ok := f.fx[ccy]; ok && k.now.Sub(r.at) < ttl {
			continue
		}
		out = append(out, ccy)
	}
	return out
}

// refreshFeeds reads rows in Yahoo's batches, each followed by the rates its
// currencies need, within feedTimeout; it ends the running refresh. Every
// batch lands as soon as it is read, so a read that stops waiting sees what
// is done. A row Yahoo did not answer keeps its last reading and is not due
// again for feedRetry; the open breaker stops the refresh and pauses the
// feed. ctx is detached from any one read.
func (c *Client) refreshFeeds(ctx context.Context, rows []AssetInfo) {
	rctx, cancel := context.WithTimeout(ctx, feedTimeout)
	defer cancel()
	f := &c.feeds
	var errs []error
	paused := false
	tried := map[string]bool{} // currencies asked for in this refresh
	for batch := range slices.Chunk(rows, feedBatch) {
		tickers := make([]string, len(batch))
		for i, a := range batch {
			tickers[i] = a.Pair
		}
		got, err := c.Yahoo.Quotes(rctx, tickers)
		errs = append(errs, err)
		if paused = yahoo.IsPaused(err) && len(got) == 0; paused {
			break
		}
		f.mu.Lock()
		f.store(batch, got, c.now())
		f.mu.Unlock()
		errs = append(errs, c.refreshFX(rctx, tried)...)
		if rctx.Err() != nil {
			break
		}
	}
	if len(rows) == 0 {
		errs = append(errs, c.refreshFX(rctx, tried)...)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.running = nil
	if paused {
		f.paused = c.now().Add(feedRetry)
	}
	if err := errors.Join(errs...); err != nil {
		if !f.failing {
			slog.Warn("market: quote-feed refresh failed; keeping the last quotes", "err", err)
		}
		f.failing = true
	} else {
		f.failing = false
	}
}

// store keeps batch's answered quotes, read at at; a row with no usable
// answer is marked failed. An older answer never replaces a newer quote.
// Callers hold mu.
func (f *feedState) store(batch []AssetInfo, got map[string]yahoo.Quote, at time.Time) {
	if f.quotes == nil {
		f.quotes = map[string]feedQuote{}
	}
	if f.failedAt == nil {
		f.failedAt = map[string]time.Time{}
	}
	if f.ccyVenues == nil {
		f.ccyVenues, f.venueMeta = map[string]map[string]bool{}, map[string]yahoo.Meta{}
	}
	type read struct {
		venue string
		start time.Time
		end   time.Time
	}
	seen := map[read]feedQuote{} // one session reading per venue and period in a batch
	for _, a := range batch {
		q, ok := got[a.Pair]
		if !ok || !(q.Price > 0) || q.Time.IsZero() || !finite(q.Price) {
			f.failedAt[a.Symbol] = at
			continue
		}
		delete(f.failedAt, a.Symbol)
		_, watched := f.watchBy[a.Symbol]
		if old, had := f.quotes[a.Symbol]; had && q.Time.Before(old.q.Time) {
			if watched { // read, though not newer: not due again at once
				old.next = watchNext(a, old.q.Meta, at)
				f.quotes[a.Symbol] = old
			}
			continue
		}
		m := q.Meta
		key := read{venue: a.Listing.SessionVenue}
		if m.RegularStart != nil && m.RegularEnd != nil {
			key.start, key.end = *m.RegularStart, *m.RegularEnd
		}
		fq, cached := seen[key]
		if !cached {
			fq = newFeedQuote(a, q, at)
			seen[key] = fq
		}
		nq := feedQuote{q: q, at: at, state: fq.state, settled: fq.settled}
		if watched {
			nq.next = watchNext(a, m, at)
		}
		f.quotes[a.Symbol] = nq
		if q.Currency != "USD" {
			if f.ccyVenues[q.Currency] == nil {
				f.ccyVenues[q.Currency] = map[string]bool{}
			}
			f.ccyVenues[q.Currency][a.Listing.SessionVenue] = true
		}
		if old, ok := f.venueMeta[a.Listing.SessionVenue]; !ok || m.RegularEnd != nil && (old.RegularEnd == nil || !m.RegularEnd.Before(*old.RegularEnd)) {
			f.venueMeta[a.Listing.SessionVenue] = m
		}
	}
}

// refreshFX reads the due rates not yet tried in this refresh, at once, and
// keeps the ones that came back; one that failed is not due for feedRetry.
func (c *Client) refreshFX(ctx context.Context, tried map[string]bool) []error {
	f := &c.feeds
	f.mu.Lock()
	ccys := slices.DeleteFunc(f.dueFX(newFeedClock(c.now())), func(ccy string) bool { return tried[ccy] })
	f.mu.Unlock()
	rates := make([]yahoo.Rate, len(ccys))
	errs := make([]error, len(ccys))
	var wg sync.WaitGroup
	for i, ccy := range ccys {
		tried[ccy] = true
		wg.Go(func() { rates[i], errs[i] = c.Yahoo.FX(ctx, ccy) })
	}
	wg.Wait()
	at := c.now()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fx == nil {
		f.fx = map[string]feedFX{}
	}
	if f.failedAt == nil {
		f.failedAt = map[string]time.Time{}
	}
	for i, ccy := range ccys {
		if errs[i] == nil && rates[i].USD > 0 && finite(rates[i].USD) {
			f.fx[ccy] = feedFX{rates[i], at}
			delete(f.failedAt, "fx:"+ccy)
		} else {
			f.failedAt["fx:"+ccy] = at
		}
	}
	return errs
}

// FillAssets is the quote-feed rows of symbols (others are ignored) as a
// paper fill should see them: any row whose freshest quote (served) was read
// over freshFill ago, or never, is read again first from its venue's
// real-time source (one request per venue, while the session is open and the
// source's floor has passed), then any row still over freshFill old, never
// read, or read before its session opened or closed from Yahoo, in one call;
// all within feedWait, each kept when not older. The rows come with their
// sessions, past the Assets cache; a symbol with no quote or USD rate is
// absent. On a failed read the last quote stands (the error says why), and
// the staleness rules decide whether it is halted.
func (c *Client) FillAssets(ctx context.Context, symbols []string) (map[string]Asset, error) {
	return c.freshAssets(ctx, symbols, freshFill)
}

// freshAssets is FillAssets with the read age maxAge.
func (c *Client) freshAssets(ctx context.Context, symbols []string, maxAge time.Duration) (map[string]Asset, error) {
	out := map[string]Asset{}
	if c.Yahoo == nil {
		return out, nil
	}
	var rows []AssetInfo
	for _, s := range symbols {
		if a, ok := Lookup(s); ok && a.QuoteFeed() && !slices.ContainsFunc(rows, func(r AssetInfo) bool { return r.Symbol == a.Symbol }) {
			rows = append(rows, a)
		}
	}
	if len(rows) == 0 {
		return out, nil
	}
	rctx, cancel := context.WithTimeout(ctx, feedWait)
	defer cancel()
	now := c.now()
	f := &c.feeds
	f.mu.Lock()
	polls := f.rtFillPolls(rows, now, maxAge)
	f.mu.Unlock()
	if len(polls) > 0 { // the real-time source first
		c.rtFill(rctx, polls)
		now = c.now()
	}
	f.mu.Lock()
	k := newFeedClock(now)
	var read []AssetInfo
	for _, a := range rows {
		sq, ok := f.served(a, now)
		if ok && sq.rt != nil && now.Sub(sq.fq.at) <= maxAge {
			continue // a fresh real-time quote
		}
		if q, ok := f.quotes[a.Symbol]; !ok || now.Sub(q.at) > maxAge || k.state(a, q.q.Meta) != q.state {
			read = append(read, a)
		}
	}
	f.mu.Unlock()
	var err error
	if len(read) > 0 {
		tickers := make([]string, len(read))
		for i, a := range read {
			tickers[i] = a.Pair
		}
		var got map[string]yahoo.Quote
		got, err = c.Yahoo.Quotes(rctx, tickers)
		f.mu.Lock()
		f.store(read, got, c.now())
		f.mu.Unlock()
		err = errors.Join(err, errors.Join(c.refreshFX(rctx, map[string]bool{})...))
	}
	at := c.now()
	f.mu.Lock()
	var assets []Asset
	for _, a := range rows {
		if as, ok := f.asset(a, at); ok {
			assets = append(assets, as)
		}
	}
	f.mu.Unlock()
	for _, as := range withSessions(assets, at) {
		out[as.Symbol] = as
	}
	return out, err
}

// Touch keeps the quote-feed rows of symbols hot for feedHotFor: read every
// minute while their market is open, first in every refresh. Charts,
// analyses, the assets list's with= and Quote call it.
func (c *Client) Touch(symbols ...string) {
	now := c.now()
	f := &c.feeds
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.touched == nil {
		f.touched = map[string]time.Time{}
	}
	for _, s := range symbols {
		if a, ok := Lookup(s); ok && a.QuoteFeed() {
			f.touched[a.Symbol] = now
		}
	}
	if len(f.touched) > 2*len(QuoteFeeds)+len(Listed) {
		maps.DeleteFunc(f.touched, func(_ string, t time.Time) bool { return now.Sub(t) >= feedHotFor })
	}
}

// refreshInterest re-reads Interest when its last read is feedInterestTTL
// old, within feedWait; a failed read keeps the last set.
func (c *Client) refreshInterest(ctx context.Context, now time.Time) {
	if c.Interest == nil {
		return
	}
	f := &c.feeds
	f.mu.Lock()
	due := now.Sub(f.interestAt) >= feedInterestTTL
	if due {
		f.interestAt = now
	}
	f.mu.Unlock()
	if !due {
		return
	}
	ictx, cancel := context.WithTimeout(ctx, feedWait)
	defer cancel()
	syms, err := c.Interest(ictx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("market: quote-feed interest unavailable; keeping the last set", "err", err)
		}
		return
	}
	set := make(map[string]bool, len(syms))
	for _, s := range syms {
		if a, ok := Lookup(s); ok && a.QuoteFeed() {
			set[a.Symbol] = true
		}
	}
	f.mu.Lock()
	f.interest = set
	f.mu.Unlock()
}

// RunFeeds keeps the quote feeds warm until ctx ends: at once and then every
// feedTick it re-reads Interest when due and starts a refresh when anything
// is due, so listed rows stay current with no reader. Assets keeps its own
// lazy trigger, so a client without the loop behaves as before.
func (c *Client) RunFeeds(ctx context.Context) {
	t := time.NewTicker(feedTick)
	defer t.Stop()
	for {
		c.feedStep(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// feedStep is one RunFeeds tick.
func (c *Client) feedStep(ctx context.Context) {
	if c.Yahoo == nil || ctx.Err() != nil {
		return
	}
	now := c.now()
	c.refreshInterest(ctx, now)
	c.startFeeds(ctx, now, false)
}

// feedClosed reports whether a's session is closed now (its verified
// calendar, else the last quote's Yahoo period); unknown is not closed.
func (c *Client) feedClosed(a AssetInfo) bool {
	f := &c.feeds
	f.mu.Lock()
	q := f.quotes[a.Symbol]
	f.mu.Unlock()
	return marketState(a, q.q.Meta.RegularStart, q.q.Meta.RegularEnd, c.now()) == "closed"
}

// feedAsset is a's Assets row from its served quote and USD rate.
func feedAsset(a AssetInfo, sq servedQuote, rate yahoo.Rate, now time.Time) (Asset, bool) {
	fq := sq.fq
	q := fq.q
	m := q.Meta
	price := q.Price * rate.USD
	if !(price > 0) || !finite(price) {
		return Asset{}, false
	}
	silent := marketState(a, m.RegularStart, m.RegularEnd, now) == "open" && now.Sub(fq.at) > QuoteSilentAfter
	old := quoteOld(a, q.Time, now) // an exchange holiday longer than 4 days: the last close is still the price
	info := &QuoteFeedInfo{Symbol: a.Pair, Exchange: m.Exchange(), Currency: q.Currency, LocalPrice: q.Price, FX: rate.USD,
		FXSymbol: rate.Symbol, AsOf: q.Time.UTC(), Stale: old || silent,
		Source: sq.source, Class: sq.class, LastTradeAt: q.Time.UTC(), ReadAt: fq.at.UTC(), DelaySeconds: sq.delay,
		TimeBasis: sq.basis, Label: QuoteLabel(sq.class, sq.delay, q.Time),
		regularStart: m.RegularStart, regularEnd: m.RegularEnd}
	if rate.Symbol != "" {
		at := rate.Time.UTC()
		info.FXAsOf = &at
	}
	as := Asset{Symbol: a.Symbol, Pair: a.Pair, Name: a.Name, Price: price, Range24hSource: "unavailable",
		Class: a.Class, Venue: a.Venue, Halted: info.Stale, QuoteFeed: info}
	// Only a 1-day spark meta's chart previous close is the previous session's
	// (the chart fallback reads 5 days).
	if p := m.ChartPreviousClose; p != nil && q.Via != "chart" {
		prev := *p
		info.PreviousClose = &prev
		if chg := math.Round((q.Price/prev-1)*100_000) / 1000; finite(chg) {
			as.Change24hPct = chg
		}
	}
	if m.DayHigh != nil && m.DayLow != nil && *m.DayHigh >= *m.DayLow {
		as.High24h, as.Low24h = max(*m.DayHigh*rate.USD, price), min(*m.DayLow*rate.USD, price)
		as.Range24hSource = "session"
	}
	if m.DayVolume != nil {
		if v := *m.DayVolume * price; finite(v) {
			as.Volume24hUsd = v // the session's shares at the latest price: approximate notional
		}
	}
	return as, true
}

// quoteFeedBars reads a quote-feed listing's candles from Yahoo, converted to
// USD: daily bars at the same day's FX close (bars before Yahoo's first rate
// are dropped), intraday bars at the latest rate. 1d bars are stamped 00:00
// UTC of their exchange-local trading date like the other daily series; 4h bars
// are Yahoo's 60-minute bars resampled into UTC 4-hour buckets. Yahoo keeps
// 15-minute bars for 60 days (this reads one month) and 60-minute bars for two
// years, so those histories are shorter than a crypto pair's.
func (c *Client) quoteFeedBars(ctx context.Context, info AssetInfo, interval string, limit int, endTime int64) ([]Candle, error) {
	return c.quoteFeedBarsInCurrency(ctx, info, interval, limit, endTime, false)
}

// quoteFeedBarsInCurrency shares the bar/timestamp handling while allowing a
// chart to read the listing's original OHLC without a USD or FX dependency.
func (c *Client) quoteFeedBarsInCurrency(ctx context.Context, info AssetInfo, interval string, limit int, endTime int64, original bool) ([]Candle, error) {
	if c.Yahoo == nil {
		return nil, fmt.Errorf("%w: %s: the Yahoo feed is not configured", ErrUpstream, info.Pair)
	}
	if !validChartInterval(interval) {
		return nil, fmt.Errorf("quote feed %s charts 15m, 1h, 4h, 1d, 1w and 1M only, not %q", info.Symbol, interval)
	}
	rng, yiv := feedRange(interval, limit, endTime != 0)
	if calendarInterval(interval) && !original {
		// Convert each trading day's OHLC at that day's FX before grouping;
		// multiplying a whole historical month by today's rate distorts it.
		yiv = "1d"
	}
	ch, err := c.Yahoo.Chart(ctx, info.Pair, rng, yiv)
	if err != nil {
		return nil, feedErr(ctx, info, err)
	}
	bars := ch.Bars
	incompleteFXPrefix := false
	if ccy := ch.Meta.Currency; !original && ccy != "USD" {
		if interval == "1d" || calendarInterval(interval) {
			fx, err := c.Yahoo.FXHistory(ctx, ccy, rng)
			if err != nil {
				return nil, feedErr(ctx, info, err)
			}
			converted := yahoo.ToUSD(bars, fx)
			if calendarInterval(interval) && len(converted) > 0 {
				first := chartPeriodStart(feedTradingDate(converted[0].T, ch.Meta.Location()), interval)
				for _, b := range bars {
					if !b.T.Before(converted[0].T) {
						break
					}
					if chartPeriodStart(feedTradingDate(b.T, ch.Meta.Location()), interval).Equal(first) {
						incompleteFXPrefix = true
						break
					}
				}
			}
			bars = converted
		} else {
			r, err := c.Yahoo.FX(ctx, ccy)
			if err != nil {
				return nil, feedErr(ctx, info, err)
			}
			bars = slices.Clone(bars)
			for i := range bars {
				b := &bars[i]
				b.O, b.H, b.L, b.C, b.Adj = b.O*r.USD, b.H*r.USD, b.L*r.USD, b.C*r.USD, b.Adj*r.USD
			}
		}
	}
	out := feedCandles(bars, interval, ch.Meta.Location())
	if incompleteFXPrefix && len(out) > 0 {
		// Daily USD history omits bars predating the first FX rate. A long
		// candle containing one of those known missing days is incomplete.
		out = out[1:]
	}
	if endTime > 0 {
		out = slices.DeleteFunc(out, func(k Candle) bool { return k.T*1000 > endTime })
	}
	out = out[max(len(out)-limit, 0):]
	if len(out) == 0 && endTime == 0 {
		return nil, fmt.Errorf("%w: no candles for %s %s", ErrUpstream, info.Pair, interval)
	}
	return out, nil
}

// feedRange is the Yahoo range and interval covering limit bars of interval:
// the shortest of Yahoo's ranges that holds them, the lifetime for history
// before a past end.
func feedRange(interval string, limit int, before bool) (rng, yahooInterval string) {
	if before && (interval == "1h" || interval == "4h") {
		return "2y", "60m" // a small older page can lie anywhere in the retained window
	}
	pick := func(tiers ...any) string {
		for i := 0; i+1 < len(tiers); i += 2 {
			if limit <= tiers[i].(int) {
				return tiers[i+1].(string)
			}
		}
		return tiers[len(tiers)-1].(string)
	}
	switch interval {
	case "1w":
		if before {
			return "max", "1wk"
		}
		return pick(240, "5y", 480, "10y", "max"), "1wk"
	case "1M":
		if before {
			return "max", "1mo"
		}
		return pick(60, "5y", 120, "10y", "max"), "1mo"
	case "15m":
		return "1mo", "15m" // Yahoo serves 15-minute bars for the last 60 days only
	case "1h":
		return pick(800, "6mo", 1700, "1y", "2y"), "60m" // 60-minute bars: the last 730 days
	case "4h":
		return pick(200, "6mo", 450, "1y", "2y"), "60m"
	}
	if before {
		return "max", "1d"
	}
	return pick(240, "1y", 480, "2y", 1200, "5y", 2400, "10y", "max"), "1d"
}

// feedCandles turns Yahoo bars into Candles (see quoteFeedBars).
func feedCandles(bars []yahoo.Bar, interval string, loc *time.Location) []Candle {
	out := make([]Candle, 0, len(bars))
	for _, b := range bars {
		t := b.T.Unix()
		switch interval {
		case "1d", "1w", "1M":
			t = feedTradingDate(b.T, loc).Unix()
			if calendarInterval(interval) {
				t = chartPeriodStart(time.Unix(t, 0), interval).Unix()
			}
		case "4h":
			t -= t % BarSeconds("4h")
		}
		k := Candle{T: t, O: b.O, H: b.H, L: b.L, C: b.C, V: b.V}
		if n := len(out); n > 0 && out[n-1].T == t { // the next source bar in the same bucket
			prev := &out[n-1]
			prev.H, prev.L, prev.C, prev.V = max(prev.H, k.H), min(prev.L, k.L), k.C, prev.V+k.V
			continue
		}
		out = append(out, k)
	}
	return out
}

func feedTradingDate(at time.Time, loc *time.Location) time.Time {
	y, m, d := at.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// feedErr is a Yahoo failure as this package's upstream error. Yahoo's
// not-found is not the caller's: the configured listing's data is unavailable.
func feedErr(ctx context.Context, info AssetInfo, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("%w: %s: %v", ErrUpstream, info.Pair, err)
}

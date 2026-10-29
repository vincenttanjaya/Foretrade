package market

import (
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo"
)

// The watch tier: listings the screener shows that no instrument quotes under
// their own ticker (a perp-traded or ADR-traded constituent's local line, a
// research-only constituent), read by the quote-feed poller for the screener
// alone. Its rows are not instruments: Lookup, Assets, FillAssets and Touch
// never see them. They come last in every refresh plan, share its spark
// batches (at most 20 tickers a call), its 20-batch cap, its breaker pause and
// its FX book, and are left out while spark batches are off. Each read sets
// the row's next one (watchNext): 15 minutes later while its session is open
// or unknown; once closed, one read an hour after the close (the settled
// close), then none before the next open. A row whose read failed waits
// watchRetry.
const (
	watchOpenTTL = 15 * time.Minute
	watchRetry   = 15 * time.Minute

	// quoteGrace is how late a due read may land before the quote counts as
	// stale (ListingQuote.StaleAt): a RunFeeds tick, a slow refresh and a
	// failed read's retry.
	quoteGrace = 2 * time.Minute
)

// WatchRow is a listing for the watch tier.
type WatchRow struct {
	Ticker   string // the Yahoo ticker, e.g. 2330.TW
	Venue    string // its session venue: TSE, KRX, TWSE, HKEX, SGX, IDX, SET, BURSA or HOSE
	Currency string // its quote currency, e.g. TWD
}

// feedPairs indexes the quote-feed instruments (the named QuoteFeeds, then
// Listed) by their Yahoo ticker: the listings the trade tier already reads.
var feedPairs = sync.OnceValue(func() map[string]AssetInfo {
	out := map[string]AssetInfo{}
	for _, a := range slices.Concat(QuoteFeeds, Listed) {
		if k := strings.ToUpper(a.Pair); out[k].Symbol == "" {
			out[k] = a
		}
	}
	return out
})

// Watch sets the watch tier's rows (replacing any earlier set) and returns how
// many it keeps. A ticker some quote-feed instrument already reads, one that
// is an instrument's symbol, an invalid ticker, a duplicate or an unknown venue
// is left out: the trade tier has it, or it cannot be read here.
func (c *Client) Watch(rows []WatchRow) int {
	var keep []AssetInfo
	by := map[string]int{}
	for _, r := range rows {
		t, ok := yahoo.Normalize(r.Ticker)
		_, venue := listedVenues[r.Venue]
		_, instrument := Lookup(t)
		_, traded := feedPairs()[t]
		_, dup := by[t]
		if !ok || !venue || instrument || traded || dup || r.Currency == "" {
			continue
		}
		by[t] = len(keep)
		keep = append(keep, AssetInfo{Symbol: t, Pair: t, Name: t, Class: Stock, Venue: QuoteFeedVenue,
			Listing: &Listing{Region: "asia", SessionVenue: r.Venue, Underlying: r.Venue + ": " + t, LocalCurrency: r.Currency, Yahoo: t}})
	}
	f := &c.feeds
	f.mu.Lock()
	defer f.mu.Unlock()
	f.watch, f.watchBy = keep, by
	return len(keep)
}

// dueWatch reports whether watch row a needs a read at now (q, ok: its last
// read). Callers hold mu.
func (f *feedState) dueWatch(a AssetInfo, q feedQuote, ok bool, now time.Time) bool {
	if t, failed := f.failedAt[a.Symbol]; failed && now.Sub(t) < watchRetry {
		return false
	}
	return !ok || !now.Before(q.next)
}

// watchNext is when a watch row read at at (Yahoo's meta m) is due again:
// watchOpenTTL later while its session is open or unknown; closed before the
// close has settled, at the settled read (settleAt); closed and settled,
// before the open or in a lunch break, at the next open (nextOpen); else
// (past a calendar's horizon with no Yahoo period) watchOpenTTL later.
func watchNext(a AssetInfo, m yahoo.Meta, at time.Time) time.Time {
	if marketState(a, m.RegularStart, m.RegularEnd, at) != "closed" {
		return at.Add(watchOpenTTL)
	}
	if s, ok := settleAt(a, m, at); ok && at.Before(s) {
		return s
	}
	if o, ok := nextOpen(a, m, at); ok {
		return o
	}
	return at.Add(watchOpenTTL)
}

// settleAt is when a closed read of a at at has settled: feedSettle after the
// later of the verified calendar's final close and Yahoo's regular end, as
// settledRead, counting a Yahoo end not yet passed when it is the same
// session's (under feedSettle after the close: HKEX's 16:10, not the coming
// day's period Yahoo reports from local midnight); on a venue with no verified
// calendar for at, feedSettle after Yahoo's period end once it has passed.
// false inside a trading day (a lunch break) or without either.
func settleAt(a AssetInfo, m yahoo.Meta, at time.Time) (time.Time, bool) {
	c := calendars[a.Listing.SessionVenue]
	if _, _, known := c.session(at); known {
		end, ok := c.finalClose(at)
		if !ok {
			return time.Time{}, false
		}
		if e := m.RegularEnd; e != nil && e.After(end) && (!e.After(at) || e.Sub(end) < feedSettle) {
			end = *e
		}
		return end.Add(feedSettle), true
	}
	if m.RegularEnd != nil && !m.RegularEnd.After(at) {
		return m.RegularEnd.Add(feedSettle), true
	}
	return time.Time{}, false
}

// nextOpen is a's next regular open after at while it is closed: its verified
// calendar's, else Yahoo's period start, or that start's local time on the
// first weekday after at (holidays unknown: a read then finds it closed and
// waits again). false when neither is known.
func nextOpen(a AssetInfo, m yahoo.Meta, at time.Time) (time.Time, bool) {
	if open, next, known := calendars[a.Listing.SessionVenue].session(at); known {
		return next, !open && !next.IsZero()
	}
	if m.RegularStart == nil {
		return time.Time{}, false
	}
	loc := m.Location()
	s, l := m.RegularStart.In(loc), at.In(loc)
	t := time.Date(l.Year(), l.Month(), l.Day(), s.Hour(), s.Minute(), s.Second(), 0, loc)
	if m.RegularStart.After(at) {
		t = s
	}
	for !t.After(at) || t.Weekday() == time.Saturday || t.Weekday() == time.Sunday {
		t = t.AddDate(0, 0, 1)
	}
	return t.UTC(), true
}

// yahooDelay is how far Yahoo's keyless regular-market price runs behind each
// venue, in minutes: the smallest age of the newest trade over a batch read
// while the venue was open, a fixed offset (five KRX repeats read 20.00–20.02).
// Yahoo states none: no chart or spark meta carries exchangeDataDelayedBy.
// Measured 2026-10-13 over every listed constituent; SET on 2026-10-14 (its
// 10-13 was a holiday): 30.00 and 30.02 over the SET50 at 04:04 and 04:08 UTC.
// US listings are not measured.
var yahooDelay = map[string]float64{KRX: 20, TWSE: 20, TSE: 15, HKEX: 15, BURSA: 15, HOSE: 15, IDX: 10, SGX: 10, SET: 30}

// YahooDelay is how many minutes Yahoo's quotes of venue run behind the
// exchange, as measured (yahooDelay); false when not measured.
func YahooDelay(venue string) (minutes float64, ok bool) {
	minutes, ok = yahooDelay[venue]
	return minutes, ok
}

// ListingQuote is the newest quote the quote feeds hold for one Yahoo listing,
// for a table of listings rather than instruments (the screener).
type ListingQuote struct {
	Ticker string
	Source string // whose price: "yahoo", or a real-time adapter's name ("naver")
	// Class is "delayed" (Yahoo), "realtime" or "near-realtime".
	Class string
	// Tier is "trade" (a quote-feed instrument's own row: the Trade page's
	// quote, read every 1, 5 or 15 minutes) or "screener" (the watch tier).
	Tier        string
	Price       float64 // regular-market price in Currency
	Currency    string
	LastTradeAt time.Time // the source's last-trade time (regularMarketTime), UTC
	ReadAt      time.Time // this server's read, UTC
	// Session is the listing's regular session now: "open", "closed" or
	// "unknown" (its verified calendar, else Yahoo's period).
	Session string
	// Refresh is the tier's read interval now: 0 while the row is held to the
	// next open or to the settled read after the close.
	Refresh time.Duration
	// StaleAt is the latest time the tier's next read is due, plus
	// quoteGrace: past it without a newer read the quote is stale.
	StaleAt time.Time
	// Stale: past StaleAt, or no new trade for QuoteStaleAfter unless the
	// venue's verified calendar shows no session open since (a holiday).
	Stale bool
	// DelayMinutes is how far the source runs behind the exchange (nil when
	// unknown); DelayBasis says how that is known: "measured" or "stated".
	// Yahoo's is the venue's measured constant (YahooDelay); a real-time
	// quote's is DelaySeconds / 60.
	DelayMinutes *float64
	DelayBasis   string
	// DelaySeconds is computed for every quote: min(ReadAt, the session's
	// last open moment) − LastTradeAt, at least 0. TimeBasis says what
	// LastTradeAt is (trade | volume | snapshot) and Label is QuoteLabel's.
	DelaySeconds float64
	TimeBasis    string
	Label        string
	// FX is the current USD rate of Currency the quote feeds hold (USD: 1); nil
	// when none is held yet.
	FX *yahoo.Rate
}

// ListingQuotes is the newest held quote of each ticker that has one: from
// the trade tier when a quote-feed instrument reads the ticker, else from the
// watch tier. It reads nothing from Yahoo and starts no refresh.
func (c *Client) ListingQuotes(tickers []string) map[string]ListingQuote {
	now := c.now()
	f := &c.feeds
	f.mu.Lock()
	defer f.mu.Unlock()
	k := newFeedClock(now)
	out := make(map[string]ListingQuote, len(tickers))
	for _, s := range tickers {
		t, ok := yahoo.Normalize(s)
		if !ok {
			continue
		}
		a, trade := feedPairs()[t]
		if !trade {
			i, watched := f.watchBy[t]
			if !watched {
				continue
			}
			a = f.watch[i]
		}
		sq, ok := f.served(a, now)
		q := sq.fq
		if !ok || !(q.q.Price > 0) {
			continue
		}
		lq := ListingQuote{Ticker: t, Source: sq.source, Class: sq.class, Tier: "screener", Price: q.q.Price, Currency: q.q.Currency,
			LastTradeAt: q.q.Time.UTC(), ReadAt: q.at.UTC(), Session: k.state(a, q.q.Meta),
			DelaySeconds: sq.delay, TimeBasis: sq.basis, Label: QuoteLabel(sq.class, sq.delay, q.q.Time)}
		if trade {
			lq.Tier = "trade"
		}
		var due time.Time
		switch {
		case sq.rt != nil: // the real-time source's interval while open; its settled read holds to the next open
			if s := f.session(sq.venue, now); s.open {
				lq.Refresh, due = sq.interval, q.at.Add(sq.rt.liveFor())
			} else if !s.next.IsZero() {
				due = s.next
			} else {
				due = q.at.Add(feedClosedTTL)
			}
			d := math.Round(sq.delay/60*100) / 100
			lq.DelayMinutes, lq.DelayBasis = &d, "measured"
		case trade:
			lq.Refresh, due = f.tradeDue(a, q, k, (len(f.interest) > 0 || len(f.touched) > 0) && f.hot(a.Symbol, now))
		default:
			due = q.next
			if lq.Session != "closed" {
				lq.Refresh = watchOpenTTL
			}
		}
		lq.StaleAt = due.Add(quoteGrace).UTC()
		lq.Stale = now.After(lq.StaleAt) || quoteOld(a, q.q.Time, now)
		if d, ok := YahooDelay(a.Listing.SessionVenue); ok && sq.rt == nil {
			lq.DelayMinutes, lq.DelayBasis = &d, "measured"
		}
		if q.q.Currency == "USD" {
			lq.FX = &yahoo.Rate{Ccy: "USD", USD: 1}
		} else if fx, ok := f.fx[q.q.Currency]; ok {
			r := fx.r
			lq.FX = &r
		}
		out[t] = lq
	}
	return out
}

// tradeDue is a trade-tier row's read interval at k.now (0 while held to the
// next open) and when its next read is due: its interval after the read, or
// the first session boundary after the read when that comes sooner (the
// poller reads a row at once when its session opened or closed). Callers
// hold mu.
func (f *feedState) tradeDue(a AssetInfo, q feedQuote, k *feedClock, hot bool) (time.Duration, time.Time) {
	state := k.state(a, q.q.Meta)
	quiet := a.IsListed() && !hot
	var ttl time.Duration
	switch {
	case state == "closed" && quiet && q.settled && k.venue(a.Listing.SessionVenue) != "":
		// held: the next open is the boundary below
	case state == "closed":
		ttl = feedClosedTTL
	case quiet:
		ttl = feedListedOpenTTL
	default:
		ttl = feedOpenTTL
	}
	var due time.Time
	if ttl > 0 {
		due = q.at.Add(ttl)
	}
	if b, ok := boundaryAfter(a, q.q.Meta, q.at); ok && (due.IsZero() || b.Before(due)) {
		due = b
	}
	if due.IsZero() { // held past the calendar's horizon: the closed interval
		due = q.at.Add(feedClosedTTL)
	}
	return ttl, due
}

// boundaryAfter is a's first regular-session boundary (an open or a close)
// after at: its verified calendar's, else its Yahoo period's start or end.
func boundaryAfter(a AssetInfo, m yahoo.Meta, at time.Time) (time.Time, bool) {
	if _, next, known := calendars[a.Listing.SessionVenue].session(at); known {
		return next, !next.IsZero()
	}
	for _, b := range []*time.Time{m.RegularStart, m.RegularEnd} {
		if b != nil && b.After(at) {
			return *b, true
		}
	}
	return time.Time{}, false
}

// quoteOld reports whether a quote last traded at asOf is stale at now: over
// QuoteStaleAfter old, unless a's verified calendar shows no regular session
// open tradedFor since (an exchange holiday longer than 4 days).
func quoteOld(a AssetInfo, asOf, now time.Time) bool {
	old := now.Sub(asOf) > QuoteStaleAfter
	if old && a.Listing != nil {
		known, opened := calendars[a.Listing.SessionVenue].openedSince(asOf, now)
		old = !known || opened
	}
	return old
}

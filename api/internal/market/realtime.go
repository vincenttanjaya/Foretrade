package market

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/market/rtquote"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo"
)

// Real-time quotes (scratchpad RT-QUOTES.md; contract §2 "Real-time
// sources"). KRX, HKEX, HOSE, SGX and TWSE each have a chain of real-time
// adapters (rtquote.Enabled, in rtquote.All order) in front of Yahoo. While a
// venue's session is open (its verified calendar; a venue with none, its
// adapter's Hours), RunRealtime polls the first unpaused source of its chain
// with one request for every listing of the venue (named feeds, listed rows,
// the screener's watch rows, the canaries), no closer than the source's
// interval and never under its floor; after the final close it makes one
// settled read, then nothing until the next open. Every row passes the guards
// in rtStore; a source whose canaries stop printing fresh trades, or fall
// behind Yahoo, or that fails three times in a row, is degraded and paused
// with backoff, and a refused request pauses it 30 minutes or more: its venue
// then serves from the next source, else Yahoo. The quotes sit in feedState
// beside Yahoo's, keyed by ticker: served picks a listing's freshest valid
// quote for Assets, ListingQuotes and fills.

const (
	rtTimeout      = 8 * time.Second  // one request, end to end
	rtMaxBody      = 4 << 20          // Vietcap's whole board is about 1 MB
	rtFuture       = 15 * time.Second // a trade time this far past our read is rejected
	rtJump         = 0.20             // a price this far from the last good one is rejected
	rtFailures     = 3                // failures in a row before a source is degraded
	rtBackoff      = time.Minute      // a degraded source's first pause, doubling
	rtBackoffMax   = 30 * time.Minute
	rtRefusalPause = 30 * time.Minute // a refusal's first pause, doubling
	rtRefusalMax   = 6 * time.Hour
	rtResetAfter   = 30 * time.Minute // ok this long: the backoffs start over
	rtOpenGrace    = 2 * time.Minute  // no health check this soon after an open (yesterday's trades still show)
	rtBehind       = time.Minute      // Yahoo's canary trade this much newer than the source's: behind
	rtClosedPause  = 5 * time.Minute  // the source says closed while the calendar says open: next look
	rtClosedSoon   = time.Minute      // the same within rtClosedPause of an interval's start (yesterday's board still up)
	rtLiveMin      = time.Minute      // an open-session row serves while read within max(3 × interval, this)
	rtTick         = time.Second
	rtRecent       = 10 // rejected rows kept per source for the detail route
)

// Source states.
const (
	rtIdle     = "idle"     // not proven yet (a boot): never serves
	rtOK       = "ok"       // serves
	rtDegraded = "degraded" // failed its health check or its reads: paused, then probed
	rtRefused  = "refused"  // refused our request: paused 30 minutes or more
)

// rtState is the real-time part of feedState (guarded by its mu).
type rtState struct {
	venues []*rtVenue // rtquote.Venues order
	base   string     // QUOTES_REALTIME_BASE; "" → each adapter's Host
}

type rtVenue struct {
	venue, token string
	chain        []*rtSource
	hours        *rtHours  // the adapter's Hours when the venue has no verified calendar
	lastReq      time.Time // the venue's last request, any source: the poll interval runs from it
	settled      time.Time // the final close whose settled read was made
	closedUntil  time.Time // the source said closed while the calendar said open: no poll before this
	inflight     bool
}

type rtSource struct {
	a        rtquote.Adapter
	info     rtquote.Info
	state    string
	reason   string
	fails    int
	backoff  time.Duration // the next degrade's pause
	refusalP time.Duration // the next refusal's pause
	paused   time.Time     // no request before this
	okSince  time.Time     // the current ok streak's start
	since    time.Time     // the first successful poll since the source was last degraded (or boot)
	lastReq  time.Time
	lastOK   time.Time
	refusals int
	refusal  *RTRefusal
	rows     map[string]*rtRow // by ticker
	day      rtDay
	recent   []RTRejected
}

// rtRow is one listing's newest accepted real-time quote.
type rtRow struct {
	price  float64
	ccy    string
	trade  time.Time // the derived last-trade time; zero until known (the volume clock's baseline)
	basis  string    // trade | volume | snapshot
	read   time.Time // our read
	volume *float64
}

// rtDay is one source's request count for a UTC day.
type rtDay struct {
	date               string
	requests, rows     int
	rejected           int
	perMinute          map[int]int
	busiest, busiestAt int // requests in the busiest minute and that minute (minutes after 00:00 UTC)
}

func (d *rtDay) roll(now time.Time) {
	if date := now.UTC().Format(time.DateOnly); d.date != date {
		*d = rtDay{date: date, perMinute: map[int]int{}}
	}
}

func (d *rtDay) request(now time.Time) {
	d.roll(now)
	d.requests++
	u := now.UTC()
	m := u.Hour()*60 + u.Minute()
	d.perMinute[m]++
	if n := d.perMinute[m]; n > d.busiest {
		d.busiest, d.busiestAt = n, m
	}
}

// UseRealtime sets the real-time chains: adapters in chain order within a
// venue (rtquote.Enabled), polled by RunRealtime; base "" sends each to its
// own Host (QUOTES_REALTIME_BASE points every one at a test fake). Every
// venue of rtquote.Venues gets a chain, empty when switched off. Call it
// before RunRealtime and the first read.
func (c *Client) UseRealtime(adapters []rtquote.Adapter, base string) {
	st := &rtState{base: strings.TrimRight(base, "/")}
	for _, v := range rtquote.Venues {
		rv := &rtVenue{venue: v.Venue, token: v.Switch}
		for _, a := range adapters {
			in := a.Info()
			if in.Venue != v.Venue {
				continue
			}
			rv.chain = append(rv.chain, &rtSource{a: a, info: in, state: rtIdle, backoff: rtBackoff, refusalP: rtRefusalPause,
				rows: map[string]*rtRow{}})
			if in.Hours != nil && rv.hours == nil && calendars[v.Venue] == nil {
				if h, err := parseHours(*in.Hours); err == nil {
					rv.hours = h
				} else {
					slog.Error("market: realtime hours unreadable; the venue is never polled", "source", in.Name, "err", err)
				}
			}
		}
		st.venues = append(st.venues, rv)
	}
	f := &c.feeds
	f.mu.Lock()
	f.rt = st
	f.mu.Unlock()
}

// rtState is the configured state, or the five venues with empty chains.
// Callers hold mu.
func (f *feedState) rtVenues() []*rtVenue {
	if f.rt == nil {
		f.rt = &rtState{}
		for _, v := range rtquote.Venues {
			f.rt.venues = append(f.rt.venues, &rtVenue{venue: v.Venue, token: v.Switch})
		}
	}
	return f.rt.venues
}

func (f *feedState) rtVenue(venue string) *rtVenue {
	for _, v := range f.rtVenues() {
		if v.venue == venue {
			return v
		}
	}
	return nil
}

// interval is a source's poll spacing: its Every, never under its Floor or
// rtquote.MinFloor.
func (s *rtSource) interval() time.Duration {
	return max(s.info.Every, s.floor())
}

func (s *rtSource) floor() time.Duration { return max(s.info.Floor, rtquote.MinFloor) }

// liveFor is how long after its read an open-session row still serves.
func (s *rtSource) liveFor() time.Duration { return max(3*s.interval(), rtLiveMin) }

// active is the first source of v's chain not paused at now: the one the next
// poll asks.
func (v *rtVenue) active(now time.Time) *rtSource {
	for _, s := range v.chain {
		if !now.Before(s.paused) {
			return s
		}
	}
	return nil
}

// serving is the first source of v's chain that is ok and not paused: the
// one whose rows price the venue (nil: Yahoo).
func (v *rtVenue) serving(now time.Time) *rtSource {
	for _, s := range v.chain {
		if s.state == rtOK && !now.Before(s.paused) {
			return s
		}
	}
	return nil
}

// --- sessions -------------------------------------------------------------

// rtHours is a venue's weekday sessions from an adapter's Hours (no holidays).
type rtHours struct {
	loc      *time.Location
	sessions []hours
}

func parseHours(h rtquote.Hours) (*rtHours, error) {
	loc, err := time.LoadLocation(h.Zone)
	if err != nil {
		return nil, err
	}
	out := &rtHours{loc: loc}
	for _, w := range h.Sessions {
		from, err1 := parseHM(w.From)
		to, err2 := parseHM(w.To)
		if err := errors.Join(err1, err2); err != nil || from >= to {
			return nil, fmt.Errorf("session %v: %v", w, err)
		}
		out.sessions = append(out.sessions, hours{from, to})
	}
	if len(out.sessions) == 0 {
		return nil, errors.New("no sessions")
	}
	return out, nil
}

func parseHM(s string) (hm, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, err
	}
	return clock(t.Hour(), t.Minute()), nil
}

// venueDay is a venue's regular intervals on a local day (at local midnight)
// and whether that day is known: the verified calendar's, else the Hours'
// weekdays.
type venueDay func(day time.Time) ([][2]time.Time, bool)

// sessionDays is venue's day function and zone: its verified calendar, else
// h; nil when neither exists.
func sessionDays(venue string, h *rtHours) (venueDay, *time.Location) {
	if c := calendars[venue]; c != nil {
		return func(day time.Time) ([][2]time.Time, bool) {
			if !c.verified(day) {
				return nil, false
			}
			return c.sessionsOn(day), true
		}, c.loc
	}
	if h == nil {
		return nil, nil
	}
	return func(day time.Time) ([][2]time.Time, bool) {
		if wd := day.Weekday(); wd == time.Saturday || wd == time.Sunday {
			return nil, true
		}
		y, m, d := day.Date()
		var out [][2]time.Time
		for _, p := range h.sessions {
			out = append(out, [2]time.Time{
				time.Date(y, m, d, int(p.open)/60, int(p.open)%60, 0, 0, h.loc),
				time.Date(y, m, d, int(p.close)/60, int(p.close)%60, 0, 0, h.loc)})
		}
		return out, true
	}, h.loc
}

// rtSession is a venue's regular session around one moment.
type rtSession struct {
	known      bool
	open       bool
	start, end time.Time // open: the current interval
	next       time.Time // closed: the next interval's start (zero past the horizon)
	lastEnd    time.Time // the last interval end at or before the moment (zero when unknown)
	final      bool      // lastEnd was its day's final close (not a lunch break)
}

// sessionAt is the session of a venue (days, loc) at t.
func sessionAt(days venueDay, loc *time.Location, t time.Time) rtSession {
	var s rtSession
	if days == nil {
		return s
	}
	local := t.In(loc)
	midnight := func(d int) time.Time {
		return time.Date(local.Year(), local.Month(), local.Day()+d, 0, 0, 0, 0, loc)
	}
	if _, known := days(midnight(0)); !known {
		return s
	}
	s.known = true
forward:
	for d := range 16 {
		iv, known := days(midnight(d))
		if !known {
			break
		}
		for _, p := range iv {
			switch {
			case t.Before(p[0]):
				s.next = p[0].UTC()
				break forward
			case t.Before(p[1]):
				s.open, s.start, s.end = true, p[0].UTC(), p[1].UTC()
				break forward
			}
		}
	}
	for d := 0; d > -16; d-- {
		iv, known := days(midnight(d))
		if !known {
			break
		}
		for i := len(iv) - 1; i >= 0; i-- {
			if !iv[i][1].After(t) {
				s.lastEnd, s.final = iv[i][1].UTC(), i == len(iv)-1
				return s
			}
		}
	}
	return s
}

// sessCache is one venue's session from from until its next boundary.
type sessCache struct {
	from, until time.Time
	s           rtSession
}

// sessionOf is venue's session at t (its verified calendar, else h), kept
// until the next open or close: between two boundaries nothing in it
// changes. Callers hold mu.
func (f *feedState) sessionOf(venue string, h *rtHours, t time.Time) rtSession {
	if c, ok := f.sessions[venue]; ok && !t.Before(c.from) && t.Before(c.until) {
		return c.s
	}
	days, loc := sessionDays(venue, h)
	s := sessionAt(days, loc, t)
	until := s.next
	if s.open {
		until = s.end
	}
	if s.known && !until.IsZero() {
		if f.sessions == nil {
			f.sessions = map[string]sessCache{}
		}
		f.sessions[venue] = sessCache{from: t, until: until, s: s}
	}
	return s
}

// session is v's session at t. Callers hold mu.
func (f *feedState) session(v *rtVenue, t time.Time) rtSession {
	return f.sessionOf(v.venue, v.hours, t)
}

// quiet reports whether t falls in one of the source's Quiet windows (local).
func (s *rtSource) quiet(v *rtVenue, t time.Time) bool {
	_, loc := sessionDays(v.venue, v.hours)
	if loc == nil {
		return false
	}
	l := t.In(loc)
	now := clock(l.Hour(), l.Minute())
	for _, w := range s.info.Quiet {
		from, err1 := parseHM(w.From)
		to, err2 := parseHM(w.To)
		if err1 == nil && err2 == nil && now >= from && now < to {
			return true
		}
	}
	return false
}

// --- scheduling -----------------------------------------------------------

// rtPoll is one request to make.
type rtPoll struct {
	v      *rtVenue
	s      *rtSource
	want   rtquote.Want
	settle time.Time // the final close a settled read is for; zero in session
}

// RunRealtime polls the real-time sources until ctx ends (the
// realtime_quotes task): each RealtimeTick (1 s) it starts every due venue's
// poll, one in flight per venue. Without chains it returns at once.
func (c *Client) RunRealtime(ctx context.Context) {
	f := &c.feeds
	f.mu.Lock()
	any := slices.ContainsFunc(f.rtVenues(), func(v *rtVenue) bool { return len(v.chain) > 0 })
	f.mu.Unlock()
	if !any {
		return
	}
	t := time.NewTicker(cmp.Or(c.RealtimeTick, rtTick))
	defer t.Stop()
	for {
		c.rtStep(ctx, false)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// rtStep starts every due poll at c.now(): detached (one goroutine each) or,
// with wait, one after the other before it returns (tests, simulations).
func (c *Client) rtStep(ctx context.Context, wait bool) int {
	if ctx.Err() != nil {
		return 0
	}
	now := c.now()
	f := &c.feeds
	f.mu.Lock()
	var polls []rtPoll
	for _, v := range f.rtVenues() {
		if p, ok := f.rtDue(v, now); ok {
			v.inflight = true
			polls = append(polls, p)
		}
	}
	f.mu.Unlock()
	for _, p := range polls {
		if wait {
			c.rtPoll(ctx, p)
		} else {
			go c.rtPoll(context.WithoutCancel(ctx), p)
		}
	}
	return len(polls)
}

// rtDue is v's poll due at now, if any. Callers hold mu.
func (f *feedState) rtDue(v *rtVenue, now time.Time) (rtPoll, bool) {
	s := v.active(now)
	if v.inflight || s == nil || now.Before(v.closedUntil) || !s.lastReq.IsZero() && now.Sub(s.lastReq) < s.floor() {
		return rtPoll{}, false
	}
	sess := f.session(v, now)
	p := rtPoll{v: v, s: s}
	switch {
	case !sess.known: // nothing is polled while the session is unknown
		return rtPoll{}, false
	case sess.open:
		if !v.lastReq.IsZero() && now.Sub(v.lastReq) < s.interval() {
			return rtPoll{}, false
		}
	case sess.final && !sess.lastEnd.IsZero() && !v.settled.Equal(sess.lastEnd) && !now.Before(sess.lastEnd.Add(s.info.Settle)) &&
		(sess.next.IsZero() || now.Before(sess.next)):
		p.settle = sess.lastEnd // the one settled read of this close
	default:
		return rtPoll{}, false
	}
	p.want = f.rtWant(v, s)
	if len(p.want) == 0 {
		return rtPoll{}, false
	}
	return p, true
}

// rtWant is every listing of v the source can serve: the named quote feeds,
// the listed rows and the screener's watch rows on the venue, plus the
// canaries. Callers hold mu.
func (f *feedState) rtWant(v *rtVenue, s *rtSource) rtquote.Want {
	want := rtquote.Want{}
	add := func(ticker, ccy string) {
		if sym, ok := s.a.Symbol(ticker); ok {
			if _, dup := want[sym]; !dup {
				want[sym] = rtquote.Wanted{Ticker: ticker, Currency: ccy}
			}
		}
	}
	for _, a := range slices.Concat(feedRows(), f.watch) {
		if a.Listing != nil && a.Listing.SessionVenue == v.venue {
			add(a.Pair, a.Listing.LocalCurrency)
		}
	}
	for _, t := range s.info.Canaries {
		add(t, venueCurrency(v.venue, t))
	}
	return want
}

// venueCurrency is ticker's currency on venue: a quote-feed or watch row's,
// else the venue's first listing currency.
func venueCurrency(venue, ticker string) string {
	if a, ok := feedPairs()[ticker]; ok && a.Listing != nil {
		return a.Listing.LocalCurrency
	}
	if ccys := listedVenues[venue]; len(ccys) > 0 {
		return ccys[0]
	}
	return ""
}

// rtHTTP is the real-time client: RealtimeHTTP, else one with rtTimeout.
func (c *Client) rtHTTP() *http.Client {
	if c.RealtimeHTTP != nil {
		return c.RealtimeHTTP
	}
	return rtDefaultHTTP
}

var rtDefaultHTTP = &http.Client{Timeout: rtTimeout}

// rtForbidden are request headers no real-time request may carry: no faked
// browser, no Referer games, no cookies.
func rtForbidden(h string) bool {
	h = http.CanonicalHeaderKey(h)
	return h != "Accept" && h != "Content-Type"
}

// rtPrepare checks an adapter's request against the policy (only Accept and
// Content-Type set, the expected scheme and host) and sets the honest
// User-Agent.
func rtPrepare(req *http.Request, host string) error {
	for k := range req.Header {
		if rtForbidden(k) {
			return fmt.Errorf("request sets %s: only Accept and Content-Type are allowed", k)
		}
	}
	u, err := url.Parse(host)
	if err != nil {
		return err
	}
	if req.URL.Scheme != u.Scheme || req.URL.Host != u.Host {
		return fmt.Errorf("request goes to %s://%s, not %s", req.URL.Scheme, req.URL.Host, host)
	}
	req.Header.Set("User-Agent", rtquote.UserAgent)
	return nil
}

// rtRefusal reports whether an HTTP status refuses us (an auth wall, a ban,
// rate limiting, a legal block).
func rtRefusal(status int) bool {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusProxyAuthRequired, http.StatusTooManyRequests,
		http.StatusUnavailableForLegalReasons:
		return true
	}
	return false
}

// rtFetch sends p's one request and parses the answer.
func (c *Client) rtFetch(ctx context.Context, p rtPoll, host string) (rtquote.Batch, int, error) {
	ctx, cancel := context.WithTimeout(ctx, rtTimeout)
	defer cancel()
	req, err := p.s.a.Request(ctx, host, p.want)
	if err != nil {
		return rtquote.Batch{}, 0, err
	}
	if err := rtPrepare(req, host); err != nil {
		return rtquote.Batch{}, 0, err
	}
	resp, err := c.rtHTTP().Do(req)
	if err != nil {
		return rtquote.Batch{}, 0, fmt.Errorf("unreachable: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, rtMaxBody+1))
	switch {
	case err != nil:
		return rtquote.Batch{}, resp.StatusCode, fmt.Errorf("answer cut off: %w", err)
	case len(body) > rtMaxBody:
		return rtquote.Batch{}, resp.StatusCode, errors.New("answer too large")
	case rtRefusal(resp.StatusCode):
		return rtquote.Batch{}, resp.StatusCode, rtquote.Refused("HTTP " + strconv.Itoa(resp.StatusCode))
	case resp.StatusCode/100 != 2:
		if b, ok := p.s.a.(rtquote.Blocker); ok {
			if why, blocked := b.Blocked(resp.StatusCode, body); blocked {
				return rtquote.Batch{}, resp.StatusCode, rtquote.Refused(why)
			}
		}
		return rtquote.Batch{}, resp.StatusCode, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := p.s.a.Parse(body, p.want)
	return b, resp.StatusCode, err
}

// rtPoll makes p's request and lands its answer.
func (c *Client) rtPoll(ctx context.Context, p rtPoll) {
	f := &c.feeds
	start := c.now()
	f.mu.Lock()
	host := cmp.Or(f.rt.base, p.s.info.Host)
	p.v.lastReq, p.s.lastReq = start, start
	p.s.day.request(start)
	if !p.settle.IsZero() {
		p.v.settled = p.settle
	}
	f.mu.Unlock()

	b, status, err := c.rtFetch(ctx, p, host)
	at := c.now()

	f.mu.Lock()
	defer f.mu.Unlock()
	p.v.inflight = false
	s := p.s
	switch {
	case errors.Is(err, rtquote.ErrRefused):
		s.refuse(p.v, status, err, at)
		if next := p.v.active(at); !p.settle.IsZero() && next != nil && next != s {
			p.v.settled = time.Time{} // the chain's next source makes the settled read the refused one did not
		}
		return
	case err != nil:
		s.fail(p.v, err.Error(), at)
		return
	}
	sess := f.session(p.v, at)
	if b.Closed && sess.open && p.settle.IsZero() {
		// a holiday the calendar does not know, a delayed open, a typhoon; in
		// an interval's first minutes the source may not have turned over yet
		pause := rtClosedPause
		if at.Sub(sess.start) < rtClosedPause {
			pause = rtClosedSoon
		}
		p.v.closedUntil = at.Add(pause)
	}
	accepted := f.rtStore(p, b, sess, at)
	if accepted == 0 && !b.Closed { // a closed board with nothing traded (a holiday's zeroed rows) is an answer
		s.fail(p.v, "no usable row", at)
		return
	}
	s.fails, s.lastOK = 0, at
	if s.since.IsZero() && accepted > 0 && !b.Closed { // polling while the source says closed proves nothing
		s.since = at
	}
	if p.settle.IsZero() && sess.open && !b.Closed {
		f.rtHealth(p.v, s, sess, at)
	}
}

// fail counts a failed read; the third in a row degrades the source.
// Callers hold mu.
func (s *rtSource) fail(v *rtVenue, why string, now time.Time) {
	s.fails++
	if s.fails >= rtFailures {
		s.degrade(v, fmt.Sprintf("%d failed reads in a row: %s", s.fails, why), now)
	}
}

// degrade pauses the source for its backoff (doubling). Callers hold mu.
func (s *rtSource) degrade(v *rtVenue, why string, now time.Time) {
	if s.state != rtDegraded || s.reason != why {
		slog.Warn("market: real-time source degraded; the venue falls back", "venue", v.venue, "source", s.info.Name,
			"reason", why, "pause", s.backoff.String())
	}
	s.state, s.reason, s.paused = rtDegraded, why, now.Add(s.backoff)
	s.backoff = min(2*s.backoff, rtBackoffMax)
	s.fails, s.okSince, s.since = 0, time.Time{}, time.Time{}
}

// refuse records a refusal and pauses the source (30 minutes, doubling to 6
// hours). Callers hold mu.
func (s *rtSource) refuse(v *rtVenue, status int, err error, now time.Time) {
	s.refusals++
	s.refusal = &RTRefusal{At: now.UTC(), Status: status, Reason: strings.TrimPrefix(err.Error(), rtquote.ErrRefused.Error()+": ")}
	slog.Warn("market: real-time source refused our request; not used meanwhile", "venue", v.venue, "source", s.info.Name,
		"status", status, "reason", s.refusal.Reason, "pause", s.refusalP.String())
	s.state, s.reason, s.paused = rtRefused, "refused: "+s.refusal.Reason, now.Add(s.refusalP)
	s.refusalP = min(2*s.refusalP, rtRefusalMax)
	s.fails, s.okSince, s.since = 0, time.Time{}, time.Time{}
}

// pass marks a healthy source ok. Callers hold mu.
func (s *rtSource) pass(v *rtVenue, now time.Time) {
	if s.state != rtOK {
		slog.Info("market: real-time source serves", "venue", v.venue, "source", s.info.Name, "was", s.state)
		s.okSince = now
	}
	s.state, s.reason = rtOK, ""
	if now.Sub(s.okSince) >= rtResetAfter {
		s.backoff, s.refusalP = rtBackoff, rtRefusalPause
	}
}

// --- rows -----------------------------------------------------------------

// yahooFor is the Yahoo quote the quote feeds hold for ticker (the trade
// tier's instrument row, else the watch row). Callers hold mu.
func (f *feedState) yahooFor(ticker string) (feedQuote, bool) {
	if a, ok := feedPairs()[ticker]; ok {
		q, ok := f.quotes[a.Symbol]
		return q, ok
	}
	if _, ok := f.watchBy[ticker]; ok {
		q, ok := f.quotes[ticker]
		return q, ok
	}
	return feedQuote{}, false
}

// rtStore guards and keeps b's rows, read at at, and returns how many it
// accepted. Per row: a ticker not asked for, a price not finite or not
// positive, a currency not the listing's, a trade time past our read by over
// rtFuture, or a price over rtJump from the last good one (the newer by trade
// time of the source's last accepted price and the Yahoo quote held) is
// rejected; the rest stand. Volume-clocked rows (TimesNone, and TimesSnapshot
// with a volume) are dated by the first read that showed their current
// volume, never later than the session's last end (nor, for a volume that
// moved across a lunch break, than the break's start), and keep that read's
// price while the volume stands still; a cumulative volume of 0 dates nothing
// (no trade this session yet). A row dated before the row held (olderAnswer:
// a staler copy of the answer) is rejected and the held row stands. Callers
// hold mu.
func (f *feedState) rtStore(p rtPoll, b rtquote.Batch, sess rtSession, at time.Time) int {
	s := p.s
	s.day.roll(at)
	asked := map[string]rtquote.Wanted{}
	for _, w := range p.want {
		asked[w.Ticker] = w
	}
	reject := func(ticker, symbol, why string) {
		s.day.rejected++
		s.recent = append(s.recent, RTRejected{Ticker: ticker, Symbol: symbol, Reason: why, At: at.UTC()})
		if n := len(s.recent); n > rtRecent {
			s.recent = slices.Delete(s.recent, 0, n-rtRecent)
		}
	}
	for _, r := range b.Rejected {
		reject(r.Ticker, r.Symbol, r.Reason)
	}
	stamp := b.Stamp
	if stamp.IsZero() {
		stamp = at
	}
	// a read outside the session is dated no later than its end
	var closedEnd time.Time
	if !sess.open && !sess.lastEnd.IsZero() {
		closedEnd = sess.lastEnd
	}
	if !closedEnd.IsZero() && stamp.After(closedEnd) {
		stamp = closedEnd
	}
	accepted := 0
	seen := map[string]bool{}
	for _, r := range b.Rows {
		w, ok := asked[r.Ticker]
		old := s.rows[r.Ticker]
		r.Currency = cmp.Or(r.Currency, w.Currency) // a source that states none: the listing's
		switch {
		case !ok || seen[r.Ticker]:
			reject(r.Ticker, "", "mismatched symbol: not asked for, or twice")
			continue
		case !(r.Price > 0) || !finite(r.Price) || r.Volume != nil && (!finite(*r.Volume) || *r.Volume < 0):
			reject(r.Ticker, "", "bad parse: price or volume not a positive number")
			continue
		case r.Currency != w.Currency:
			reject(r.Ticker, "", fmt.Sprintf("currency %s, the listing's is %s", r.Currency, w.Currency))
			continue
		}
		seen[r.Ticker] = true
		row := rtRow{price: r.Price, ccy: r.Currency, read: at, volume: r.Volume}
		switch {
		case s.info.Times == rtquote.TimesTrade:
			row.trade, row.basis = r.TradeAt.UTC(), "trade"
			if r.TradeAt.IsZero() {
				reject(r.Ticker, "", "no trade time")
				continue
			}
		case s.info.Times == rtquote.TimesSnapshot && r.Volume == nil:
			row.trade, row.basis = stamp.UTC(), "snapshot"
		case r.Volume == nil:
			reject(r.Ticker, "", "no volume to date the price by")
			continue
		default:
			row.basis = "volume"
			if s.info.Times == rtquote.TimesSnapshot {
				row.basis = "snapshot"
			}
			switch {
			case old == nil || old.volume == nil: // the baseline: the row serves once its volume moves
			case *r.Volume == 0: // nothing traded this session yet: the price is an earlier session's
			case *old.volume == *r.Volume:
				// nothing traded since: a price that moved without volume (an
				// auction's indicative price) is no trade, so the row keeps its own
				row.trade = old.trade
				if !old.trade.IsZero() {
					row.price = old.price
				}
			default:
				row.trade = stamp.UTC()
				// the volume moved between the last read before a break and the
				// first after it: those trades may be the morning's last, so the
				// row is dated no later than the break's start
				if sess.open && !sess.final && !sess.lastEnd.IsZero() && old.read.Before(sess.lastEnd) && row.trade.After(sess.lastEnd) {
					row.trade = sess.lastEnd
				}
			}
		}
		if !row.trade.IsZero() && row.trade.After(at.Add(rtFuture)) {
			reject(r.Ticker, "", "future timestamp "+row.trade.UTC().Format(time.RFC3339))
			continue
		}
		if why := olderAnswer(old, row, closedEnd); why != "" {
			reject(r.Ticker, "", why)
			continue
		}
		if good, ok := f.rtLastGood(r.Ticker, old); ok && math.Abs(row.price/good-1) > rtJump {
			reject(r.Ticker, "", fmt.Sprintf("price %g is %.0f%% from the last good %g", row.price, (row.price/good-1)*100, good))
			continue
		}
		s.rows[r.Ticker] = &row
		accepted++
		if r.Currency != "USD" { // its USD rate is read like a Yahoo row's
			if f.ccyVenues == nil {
				f.ccyVenues, f.venueMeta = map[string]map[string]bool{}, map[string]yahoo.Meta{}
			}
			if f.ccyVenues[r.Currency] == nil {
				f.ccyVenues[r.Currency] = map[string]bool{}
			}
			f.ccyVenues[r.Currency][p.v.venue] = true
		}
	}
	s.day.rows += accepted
	return accepted
}

// rtLastGood is the last good price of ticker: the newer, by trade time (a
// baseline row by its read), of the source's last accepted row and the Yahoo
// quote held. Callers hold mu.
func (f *feedState) rtLastGood(ticker string, old *rtRow) (float64, bool) {
	var price float64
	var at time.Time
	if old != nil {
		price, at = old.price, cmp.Or(old.trade, old.read)
	}
	if q, ok := f.yahooFor(ticker); ok && q.q.Price > 0 && (at.IsZero() || q.q.Time.After(at)) {
		price = q.q.Price
	}
	return price, price > 0
}

// olderAnswer says why row is an older answer than old, the row held ("" when
// it is not): its trade time (the source's own, or under the volume clock the
// source's clock at a moved volume) is before the held one's, as from another
// edge's stale cache or a lagging node. The held row stands: an older answer
// never replaces a newer quote. (A lower cumulative volume alone is not
// enough: the first read of a day can still show the last session's board,
// after-hours volume included.) Outside the session a read is dated no later
// than its end (end), so the held time counts as no later than that either.
func olderAnswer(old *rtRow, row rtRow, end time.Time) string {
	if old == nil || old.trade.IsZero() || row.trade.IsZero() {
		return ""
	}
	held := old.trade
	if !end.IsZero() && held.After(end) {
		held = end
	}
	if !row.trade.Before(held) {
		return ""
	}
	return "older than the quote held: trade " + row.trade.UTC().Format(time.TimeOnly) + ", held " +
		old.trade.UTC().Format(time.TimeOnly)
}

// rtHealth checks a source after an in-session poll at now: from rtOpenGrace
// after an open and outside its Quiet windows, it is degraded when no canary
// has a known trade after MaxLag of polling, when the newest canary trade is
// older than MaxLag, or when Yahoo holds a canary trade rtBehind newer than
// its own; ok otherwise. Callers hold mu.
func (f *feedState) rtHealth(v *rtVenue, s *rtSource, sess rtSession, now time.Time) {
	if now.Sub(sess.start) < rtOpenGrace || s.quiet(v, now) {
		return // too soon after an open, or a closing call: the state stands
	}
	var newest time.Time
	for _, t := range s.info.Canaries {
		r := s.rows[t]
		if r == nil || r.trade.IsZero() {
			continue
		}
		if r.trade.After(newest) {
			newest = r.trade
		}
		if y, ok := f.yahooFor(t); ok && y.q.Time.Sub(r.trade) > rtBehind {
			s.degrade(v, fmt.Sprintf("behind Yahoo on %s: Yahoo's trade %s, the source's %s", t,
				y.q.Time.UTC().Format(time.TimeOnly), r.trade.UTC().Format(time.TimeOnly)), now)
			return
		}
	}
	switch {
	case newest.IsZero() && now.Sub(s.since) >= s.info.MaxLag:
		s.degrade(v, fmt.Sprintf("no canary trade after %s of polling", now.Sub(s.since).Round(time.Second)), now)
	case newest.IsZero(): // the volume clock's baseline: undecided
	case now.Sub(newest) > s.info.MaxLag:
		s.degrade(v, fmt.Sprintf("canaries stale: the newest trade is %s old", now.Sub(newest).Round(time.Second)), now)
	default:
		s.pass(v, now)
	}
}

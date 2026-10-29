package market

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/httpx"
)

// Venue statuses (QuoteSources, VenueSource.Status).
const (
	StatusOK       = "ok"       // the venue's first enabled real-time source serves
	StatusDegraded = "degraded" // an earlier source failed its health check or its reads and is backing off
	StatusFallback = "fallback" // an earlier source is switched off or refused our request, or the session is unknown (no clock to poll by)
	StatusClosed   = "closed"   // the session is closed: nothing is polled
)

// venueStatus is v's serving source name, status and the age in seconds of
// its newest trade (-1 when none) at now. Callers hold mu.
func (f *feedState) venueStatus(v *rtVenue, now time.Time) (source, status string, age int) {
	serving := v.serving(now)
	source = "yahoo"
	if serving != nil {
		source = serving.info.Name
	}
	age = f.newestAge(v, serving, now)
	sess := f.session(v, now)
	switch {
	case !sess.known:
		// no clock (nothing switched on, or a date past the verified calendar's
		// horizon): nothing is polled and Yahoo serves, open or not (served)
		return "yahoo", StatusFallback, f.newestAge(v, nil, now)
	case !sess.open || now.Before(v.closedUntil):
		return source, StatusClosed, age
	case len(v.chain) > 0 && serving == v.chain[0]:
		return source, StatusOK, age
	}
	for _, s := range v.chain {
		if s == serving {
			break
		}
		if s.state == rtRefused {
			return source, StatusFallback, age
		}
	}
	if len(v.chain) == 0 {
		return source, StatusFallback, age
	}
	return source, StatusDegraded, age
}

// newestAge is the age in whole seconds at now of the newest trade the
// serving source holds for v (Yahoo's quotes of the venue when serving is
// nil); -1 when it holds none. Callers hold mu.
func (f *feedState) newestAge(v *rtVenue, serving *rtSource, now time.Time) int {
	var newest time.Time
	if serving != nil {
		for _, r := range serving.rows {
			if r.trade.After(newest) {
				newest = r.trade
			}
		}
	} else {
		for _, a := range append(feedRows(), f.watch...) {
			if a.Listing == nil || a.Listing.SessionVenue != v.venue {
				continue
			}
			if q, ok := f.quotes[a.Symbol]; ok && q.q.Time.After(newest) {
				newest = q.q.Time
			}
		}
	}
	if newest.IsZero() {
		return -1
	}
	return int(max(0, now.Sub(newest)/time.Second))
}

// QuoteSources is /healthz's X-Quote-Sources value: per real-time venue, in
// a fixed order, the source now serving, its status and the age of its
// newest trade: "krx=naver ok 2s; hkex=tencent ok 3s; hose=vietcap ok 1s;
// sgx=sgx ok 70s; twse=mis ok 15s" ("-" for an age not known yet).
func (c *Client) QuoteSources() string {
	now := c.now()
	f := &c.feeds
	f.mu.Lock()
	defer f.mu.Unlock()
	parts := make([]string, 0, 5)
	for _, v := range f.rtVenues() {
		src, status, age := f.venueStatus(v, now)
		a := "-"
		if age >= 0 {
			a = fmt.Sprintf("%ds", age)
		}
		parts = append(parts, fmt.Sprintf("%s=%s %s %s", v.token, src, status, a))
	}
	return strings.Join(parts, "; ")
}

// VenueSource is one venue of GET /api/market/quote-sources.
type VenueSource struct {
	Venue      string     `json:"venue"`  // KRX HKEX HOSE SGX TWSE
	Source     string     `json:"source"` // serving now: an adapter's name, or "yahoo"
	Status     string     `json:"status"` // ok | degraded | fallback | closed
	NewestAge  *int       `json:"newestTradeAgeSeconds"`
	Session    string     `json:"session"` // open | closed | unknown (the verified calendar, else the adapter's hours)
	NextPollAt *time.Time `json:"nextPollAt"`
	Chain      []RTSource `json:"chain"` // in order; empty when the venue is switched off
}

// RTSource is one chain source's state.
type RTSource struct {
	Source       string       `json:"source"`
	Class        string       `json:"class"` // realtime | near-realtime
	State        string       `json:"state"` // idle | ok | degraded | refused
	Reason       *string      `json:"reason"`
	PollSeconds  int          `json:"pollSeconds"`
	FloorSeconds int          `json:"floorSeconds"`
	PausedUntil  *time.Time   `json:"pausedUntil"`
	LastPollAt   *time.Time   `json:"lastPollAt"`
	LastOKAt     *time.Time   `json:"lastOkAt"`
	Failures     int          `json:"consecutiveFailures"`
	Refusals     int          `json:"refusals"`
	LastRefusal  *RTRefusal   `json:"lastRefusal"`
	Today        RTToday      `json:"today"`
	Canaries     []RTCanary   `json:"canaries"`
	Rejected     []RTRejected `json:"rejected"` // the last few, newest last
}

// RTRefusal is a refused request.
type RTRefusal struct {
	At     time.Time `json:"at"`
	Status int       `json:"status"` // the HTTP status (0 when a 2xx body was a block page)
	Reason string    `json:"reason"`
}

// RTToday is a source's UTC day so far.
type RTToday struct {
	Date            string     `json:"date"`
	Requests        int        `json:"requests"`
	Rows            int        `json:"rows"`
	RejectedRows    int        `json:"rejectedRows"`
	BusiestMinute   int        `json:"busiestMinute"` // requests in it
	BusiestMinuteAt *time.Time `json:"busiestMinuteAt"`
}

// RTCanary is a canary's trade time at the source and at Yahoo.
type RTCanary struct {
	Ticker           string     `json:"ticker"`
	LastTradeAt      *time.Time `json:"lastTradeAt"`
	AgeSeconds       *int       `json:"ageSeconds"`
	YahooLastTradeAt *time.Time `json:"yahooLastTradeAt"`
}

// RTRejected is a rejected row.
type RTRejected struct {
	Ticker string    `json:"ticker"`
	Symbol string    `json:"symbol"`
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
}

func tptr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

// QuoteSourcesDetail is GET /api/market/quote-sources: every real-time
// venue's status, as /healthz states it, with its chain's states.
func (c *Client) QuoteSourcesDetail() []VenueSource {
	now := c.now()
	f := &c.feeds
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []VenueSource
	for _, v := range f.rtVenues() {
		src, status, age := f.venueStatus(v, now)
		vs := VenueSource{Venue: v.venue, Source: src, Status: status, Session: "unknown", Chain: []RTSource{}}
		if age >= 0 {
			vs.NewestAge = &age
		}
		if s := f.session(v, now); s.known {
			vs.Session = "closed"
			if s.open {
				vs.Session = "open"
			}
		}
		vs.NextPollAt = f.rtNextPoll(v, now)
		for _, s := range v.chain {
			s.day.roll(now)
			rs := RTSource{Source: s.info.Name, Class: s.info.Class, State: s.state, PollSeconds: int(s.interval() / time.Second),
				FloorSeconds: int(s.floor() / time.Second), PausedUntil: tptr(s.paused), LastPollAt: tptr(s.lastReq),
				LastOKAt: tptr(s.lastOK), Failures: s.fails, Refusals: s.refusals, LastRefusal: s.refusal,
				Today:    RTToday{Date: s.day.date, Requests: s.day.requests, Rows: s.day.rows, RejectedRows: s.day.rejected, BusiestMinute: s.day.busiest},
				Rejected: append([]RTRejected{}, s.recent...)}
			if !now.Before(s.paused) {
				rs.PausedUntil = nil
			}
			if s.reason != "" {
				r := s.reason
				rs.Reason = &r
			}
			if s.day.busiest > 0 {
				d, _ := time.Parse(time.DateOnly, s.day.date)
				rs.Today.BusiestMinuteAt = tptr(d.Add(time.Duration(s.day.busiestAt) * time.Minute))
			}
			for _, t := range s.info.Canaries {
				cn := RTCanary{Ticker: t}
				if r := s.rows[t]; r != nil && !r.trade.IsZero() {
					cn.LastTradeAt = tptr(r.trade)
					a := int(max(0, now.Sub(r.trade)/time.Second))
					cn.AgeSeconds = &a
				}
				if y, ok := f.yahooFor(t); ok {
					cn.YahooLastTradeAt = tptr(y.q.Time)
				}
				rs.Canaries = append(rs.Canaries, cn)
			}
			vs.Chain = append(vs.Chain, rs)
		}
		out = append(out, vs)
	}
	return out
}

// rtNextPoll is when v's next poll is due, as far as now can tell (nil:
// none before the next open, or no source). Callers hold mu.
func (f *feedState) rtNextPoll(v *rtVenue, now time.Time) *time.Time {
	s := v.active(now)
	if s == nil {
		for _, c := range v.chain { // all paused: the earliest end of a pause
			if s == nil || c.paused.Before(s.paused) {
				s = c
			}
		}
		if s == nil {
			return nil
		}
	}
	sess := f.session(v, now)
	switch {
	case !sess.known:
		return nil
	case sess.open:
		return tptr(latest(v.lastReq.Add(s.interval()), s.lastReq.Add(s.floor()), s.paused, v.closedUntil, now))
	case sess.final && !sess.lastEnd.IsZero() && !v.settled.Equal(sess.lastEnd):
		return tptr(latest(sess.lastEnd.Add(s.info.Settle), s.paused, now))
	}
	if sess.next.IsZero() {
		return nil
	}
	return tptr(latest(sess.next, s.paused))
}

// latest is the latest of ts.
func latest(ts ...time.Time) time.Time {
	var out time.Time
	for _, t := range ts {
		if t.After(out) {
			out = t
		}
	}
	return out
}

// handleQuoteSources is GET /api/market/quote-sources.
func (c *Client) handleQuoteSources(w http.ResponseWriter, r *http.Request) {
	httpx.JSON(w, http.StatusOK, map[string]any{"asOf": c.now().UTC(), "venues": c.QuoteSourcesDetail()})
}

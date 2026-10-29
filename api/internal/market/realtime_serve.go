package market

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/market/rtquote"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo"
)

// Price classes of a served quote (QuoteFeedInfo.Class, ListingQuote.Class).
const (
	ClassRealtime = rtquote.ClassRealtime
	ClassNear     = rtquote.ClassNear
	ClassDelayed  = "delayed" // Yahoo
)

// servedQuote is a listing's freshest valid quote: Yahoo's, or a real-time
// source's rebuilt as the feed state holds a Yahoo one.
type servedQuote struct {
	fq       feedQuote
	source   string  // "yahoo" or the adapter's name
	class    string  // ClassDelayed, ClassRealtime or ClassNear
	basis    string  // what the trade time is: trade | volume | snapshot
	delay    float64 // seconds: min(read, the session's last open moment) − trade, ≥ 0
	interval time.Duration
	rt       *rtSource // nil for Yahoo
	venue    *rtVenue
}

// served is a's freshest valid quote at now: its venue's serving real-time
// source's row when the venue's session is known (not past its calendar's
// horizon) and that row has a known trade time, was read within the
// source's liveFor while the session is open, and is not older by trade time
// than the Yahoo quote held (after a close, a row dated at or past it is not
// older than any print past it); else the Yahoo quote. Callers hold mu.
func (f *feedState) served(a AssetInfo, now time.Time) (servedQuote, bool) {
	yq, okY := f.quotes[a.Symbol]
	out := servedQuote{fq: yq, source: "yahoo", class: ClassDelayed, basis: "trade"}
	if okY {
		out.delay = f.delaySeconds(a, yq.q.Meta, yq.at, yq.q.Time)
	}
	if a.Listing == nil {
		return out, okY
	}
	v := f.rtVenue(a.Listing.SessionVenue)
	if v == nil {
		return out, okY
	}
	s := v.serving(now)
	if s == nil {
		return out, okY
	}
	r := s.rows[a.Pair]
	if r == nil || r.trade.IsZero() {
		return out, okY
	}
	sess := f.session(v, now)
	if !sess.known { // past the calendar's horizon nothing is polled: no read can be held to the session
		return out, okY
	}
	yt := yq.q.Time
	if !sess.open && !sess.lastEnd.IsZero() && yt.After(sess.lastEnd) && !r.trade.Before(sess.lastEnd) {
		// both past the close (a closing auction's, trade-at-close's prints):
		// the settled read stands rather than Yahoo's later print of the close
		yt = r.trade
	}
	switch {
	case sess.open && now.Sub(r.read) > s.liveFor():
		return out, okY // the poller stopped reading it: not live any more
	case okY && r.trade.Before(yt):
		return out, okY // Yahoo knows a newer trade
	}
	var meta yahoo.Meta
	via := "chart" // no previous close known
	if okY {
		meta, via = yq.q.Meta, yq.q.Via
		if loc := f.venueLoc(a, meta); !sameDay(yq.q.Time, r.trade, loc) {
			// Yahoo's newest trade is an earlier session's (it runs minutes
			// behind): its price is that session's close, and its day range
			// and volume are not today's.
			prev := yq.q.Price
			meta.ChartPreviousClose, meta.PreviousClose = &prev, &prev
			meta.DayHigh, meta.DayLow, meta.DayVolume = nil, nil, nil
			via = "spark"
		}
	}
	if r.volume != nil {
		vol := *r.volume
		meta.DayVolume = &vol
	}
	q := yahoo.Quote{Symbol: a.Pair, Price: r.price, Time: r.trade, Currency: r.ccy, Meta: meta, Via: via}
	return servedQuote{
		fq:     feedQuote{q: q, at: r.read, state: marketState(a, meta.RegularStart, meta.RegularEnd, r.read)},
		source: s.info.Name, class: s.info.Class, basis: r.basis, delay: f.delaySeconds(a, meta, r.read, r.trade),
		interval: s.interval(), rt: s, venue: v,
	}, true
}

// sameDay reports whether a and b fall on the same local date in loc.
func sameDay(a, b time.Time, loc *time.Location) bool {
	ay, am, ad := a.In(loc).Date()
	by, bm, bd := b.In(loc).Date()
	return ay == by && am == bm && ad == bd
}

// venueLoc is a's exchange zone: its verified calendar's, its real-time
// adapter's Hours', else Yahoo's meta. Callers hold mu.
func (f *feedState) venueLoc(a AssetInfo, m yahoo.Meta) *time.Location {
	if _, loc := sessionDays(a.Listing.SessionVenue, f.hoursFor(a.Listing.SessionVenue)); loc != nil {
		return loc
	}
	return m.Location()
}

// hoursFor is venue's real-time Hours, when an adapter gave the venue some.
// Callers hold mu.
func (f *feedState) hoursFor(venue string) *rtHours {
	if f.rt == nil {
		return nil
	}
	for _, v := range f.rt.venues {
		if v.venue == venue {
			return v.hours
		}
	}
	return nil
}

// lastOpen is the last moment at or before t that a's regular session was
// open: t itself while open, else the session's last end (its verified
// calendar, its adapter's Hours, else Yahoo's period once ended); t when
// unknown. Callers hold mu.
func (f *feedState) lastOpen(a AssetInfo, m yahoo.Meta, t time.Time) time.Time {
	if a.Listing != nil {
		if s := f.sessionOf(a.Listing.SessionVenue, f.hoursFor(a.Listing.SessionVenue), t); s.known {
			switch {
			case s.open:
				return t
			case !s.lastEnd.IsZero():
				return s.lastEnd
			}
			return t
		}
	}
	if m.RegularStart != nil && m.RegularEnd != nil {
		if !t.Before(*m.RegularStart) && t.Before(*m.RegularEnd) {
			return t
		}
		if !m.RegularEnd.After(t) {
			return *m.RegularEnd
		}
	}
	return t
}

// delaySeconds is how far behind a quote read at read is: min(read, the
// session's last open moment) − its trade time, at least 0. Callers hold mu.
func (f *feedState) delaySeconds(a AssetInfo, m yahoo.Meta, read, trade time.Time) float64 {
	end := f.lastOpen(a, m, read)
	if end.After(read) {
		end = read
	}
	return max(0, math.Round(end.Sub(trade).Seconds()*10)/10)
}

// QuoteLabel is the honest label of a price (RT-QUOTES §5): a real-time
// source's quote under 60 s behind reads "real-time · 04:03:12 UTC"; a real-
// time or near-real-time one at most 5 minutes behind "about 2 min behind ·
// 04:03 UTC"; anything else, Yahoo always, "delayed · last trade 04:03 UTC".
// The time is the quote's last trade, UTC. Nothing says "live".
func QuoteLabel(class string, delaySeconds float64, lastTrade time.Time) string {
	t := lastTrade.UTC()
	switch {
	case class == ClassRealtime && delaySeconds < 60:
		return "real-time · " + t.Format(time.TimeOnly) + " UTC"
	case (class == ClassRealtime || class == ClassNear) && delaySeconds <= 300:
		return fmt.Sprintf("about %d min behind · %s UTC", max(1, int(math.Round(delaySeconds/60))), t.Format("15:04"))
	}
	return "delayed · last trade " + t.Format("15:04") + " UTC"
}

// rtFillPolls are the real-time polls a fill of rows should make first at
// now: one per venue whose row's freshest quote was read over maxAge ago (or
// never), while its session is open and its active source's floor has
// passed. Callers hold mu.
func (f *feedState) rtFillPolls(rows []AssetInfo, now time.Time, maxAge time.Duration) []rtPoll {
	var polls []rtPoll
	for _, a := range rows {
		if a.Listing == nil {
			continue
		}
		if sq, ok := f.served(a, now); ok && now.Sub(sq.fq.at) <= maxAge {
			continue
		}
		v := f.rtVenue(a.Listing.SessionVenue)
		if v == nil || v.inflight || now.Before(v.closedUntil) {
			continue
		}
		s := v.active(now)
		if s == nil || !s.lastReq.IsZero() && now.Sub(s.lastReq) < s.floor() || !f.session(v, now).open {
			continue
		}
		want := f.rtWant(v, s)
		if len(want) == 0 {
			continue
		}
		v.inflight = true
		polls = append(polls, rtPoll{v: v, s: s, want: want})
	}
	return polls
}

// rtFill makes polls at once, side by side, within ctx.
func (c *Client) rtFill(ctx context.Context, polls []rtPoll) {
	var wg sync.WaitGroup
	for _, p := range polls {
		wg.Go(func() { c.rtPoll(ctx, p) })
	}
	wg.Wait()
}

package market

import (
	"cmp"
	"slices"
	"time"
	_ "time/tzdata" // exchange clocks must not depend on the host's zoneinfo
)

// Session venues: the exchange whose regular session a perp's Session reports.
const (
	NYSE = "NYSE"
	KRX  = "KRX"
	TSE  = "TSE"
	HKEX = "HKEX"
	SSE  = "SSE"
)

// Session venues of the quote-feed listings (universe_quotefeed.go). IDX,
// BURSA and HOSE have no verified calendar here, so their session is unknown.
const (
	SGX   = "SGX"
	IDX   = "IDX"
	SET   = "SET"
	BURSA = "BURSA"
	HOSE  = "HOSE"
	TWSE  = "TWSE"
)

// hm is a local time of day in minutes after midnight.
type hm int

func clock(h, m int) hm { return hm(h*60 + m) }

// hours is one local [open, close) interval.
type hours struct{ open, close hm }

// calendar is one exchange's regular continuous session: weekday hours, an
// optional lunch break (closed), official full-day closures and shortened
// days, and the years those are verified for. Weekends are closed. Pre- and
// after-market sessions and auctions outside hours count as closed;
// unscheduled closures (typhoon, black rainstorm) are not modelled.
type calendar struct {
	loc      *time.Location
	hours    hours            // the regular day
	lunch    hours            // the break inside it; zero when none
	from, to int              // verified years, inclusive; to < from: none (session unknown)
	until    string           // optional local day from which the session is unknown (YYYY-MM-DD)
	holidays map[string]bool  // weekday full-day closures, local YYYY-MM-DD
	short    map[string]hours // shortened days: a nonzero open or close replaces the regular one; lunch still applies
}

func zone(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err) // unreachable: time/tzdata is embedded
	}
	return loc
}

// newYork is NYSE's clock (EST/EDT).
var newYork = zone("America/New_York")

// calendars holds each session venue's calendar.
//
// NYSE: full-day closures and 13:00 ET early closes from its published
// 2026–2028 calendars (a Saturday holiday is observed the Friday before, a
// Sunday one the Monday after; New Year's Day 2028 falls on a Saturday and is
// not observed). Official source2026-10-05: nyse.com/trade/hours-calendars.
// Unknown outside2026–2028.
//
// KRX (09:00–15:30 KST, no lunch break): weekday closures from KRX's own
// holiday service for 2026 and 2027 (global.krx.co.kr and open.krx.co.kr
// market-holiday pages, read 2026-10-06), including the year-end closing day.
// The published 2028 list omits the statutory 2028 National Assembly election
// day, so 2028 is unknown. KRX's delayed opens (the year's first trading day,
// the CSAT day) are not modelled.
//
// TSE (09:00–11:30 and 12:30–15:30 JST): JPX market holidays for 2026 and 2027
// (jpx.co.jp/english/corporate/about-jpx/calendar/, updated 2026-02-06), which
// match Japan's Cabinet Office national-holiday list
// (www8.cao.go.jp/chosei/shukujitsu/syukujitsu.csv) plus JPX's Dec 31–Jan 3
// closures. 2028 national holidays are not yet gazetted: unknown.
//
// HKEX (09:30–12:00 and 13:00–16:00 HKT): weekday general holidays from the
// HKSAR government's gazetted lists for 2026 and 2027
// (gov.hk/en/about/abouthk/holiday/2026.htm, 2027.htm) and HKEX's calendar
// (hkex.com.hk/News/HKEX-Calendar). Half-days close at 12:00 on the eves of
// Christmas, New Year and Lunar New Year when they are weekdays (HKEX
// trading-hours page; the 2026 dates and 2027-02-05 are also on its
// calendar). The 2028 gazette is not published: unknown. Typhoon and
// black-rainstorm closures are unscheduled and not modelled.
//
// SSE (China A-shares, 09:30–11:30 and 13:00–15:00 CST): weekday closures from
// the Shanghai Stock Exchange's notice on its 2026 holiday closures (上证公告
// 〔2025〕45号, 2025-12-22, sse.com.cn/disclosure/announcement/general/c/
// c_20251222_10802507.shtml, read 2026-10-06), which follows the State
// Council's 2026 holiday schedule. The market also stays shut on the weekend
// make-up working days it names, so weekends stay closed. Opening and closing
// call auctions outside 09:30–15:00 and STAR post-close fixed-price trading
// count as closed. The 2027 notice is not published: unknown.
//
// SGX (09:00–12:00 and 13:00–17:00 SGT; SGX-ST Regulatory Notice 8.2.1 on
// rulebook.sgx.com): Singapore's gazetted public holidays for 2026 and 2027
// (mom.gov.sg public-holidays page, read 2026-10-06 and, for 2027, its
// 19 June 2026 edition), a Sunday holiday observed the Monday after, as SGX's
// own 2026 calendar (api2.sgx.com "SGX Calendar 2026") marks 1 June, 10 August
// and 9 November; a Saturday holiday (1 May and 25 December 2027) closes
// nothing. Half-days trade to 12:00 on 16 February, 24 December and
// 31 December 2026 (sgx.com/stock-exchange/trading, via the api2.sgx.com
// content API, read 2026-10-13; it lists 2025 and 2026 only). SGX has not
// published its 2027 half-days, so the session is unknown from the first 2027
// eve, the Lunar New Year eve on 2027-02-05.
//
// SET (10:00–12:30 and 14:00–16:30 ICT, the continuous sessions after the
// random opens; set.or.th trading-hours page): SET's 2026 holidays from its
// official "SET Calendar 2026" (media.set.or.th, Nov 2025 edition), including
// 2 January's additional special holiday and the 31 December New Year's Eve
// closure. 2027 is not published: unknown.
//
// TWSE (09:00–13:30 CST, no lunch break; twse.com.tw trading page): TWSE's 2026
// holiday schedule (twse.com.tw holidaySchedule, read 2026-10-06), including
// the 12–13 February no-trading days. The 2027 schedule is not published:
// unknown.
//
// IDX, Bursa Malaysia and HOSE publish their closures as notices (several move
// with the moon sighting) that we could not read from an official source, so
// their sessions are unknown (no calendar).
var calendars = map[string]*calendar{
	NYSE: {loc: newYork, hours: hours{clock(9, 30), clock(16, 0)}, from: 2026, to: 2028,
		holidays: dates(
			"2026-01-01", "2026-01-19", "2026-02-16", "2026-04-03", "2026-05-25",
			"2026-06-19", "2026-07-03", "2026-09-07", "2026-11-26", "2026-12-25",
			"2027-01-01", "2027-01-18", "2027-02-15", "2027-03-26", "2027-05-31",
			"2027-06-18", "2027-07-05", "2027-09-06", "2027-11-25", "2027-12-24",
			"2028-01-17", "2028-02-21", "2028-04-14", "2028-05-29", "2028-06-19", "2028-07-04", "2028-09-04", "2028-11-23", "2028-12-25"),
		short: closesAt(clock(13, 0), "2026-11-27", "2026-12-24", "2027-11-26", "2028-07-03", "2028-11-24")},
	KRX: {loc: zone("Asia/Seoul"), hours: hours{clock(9, 0), clock(15, 30)}, from: 2026, to: 2027,
		holidays: dates(
			"2026-01-01", "2026-02-16", "2026-02-17", "2026-02-18", "2026-03-02", "2026-05-01", "2026-05-05", "2026-05-25", "2026-06-03",
			"2026-07-17", "2026-08-17", "2026-09-24", "2026-09-25", "2026-10-05", "2026-10-09", "2026-12-25", "2026-12-31",
			"2027-01-01", "2027-02-08", "2027-02-09", "2027-03-01", "2027-05-03", "2027-05-05", "2027-05-13", "2027-07-19",
			"2027-08-16", "2027-09-14", "2027-09-15", "2027-09-16", "2027-10-04", "2027-10-11", "2027-12-27", "2027-12-31")},
	TSE: {loc: zone("Asia/Tokyo"), hours: hours{clock(9, 0), clock(15, 30)}, lunch: hours{clock(11, 30), clock(12, 30)}, from: 2026, to: 2027,
		holidays: dates(
			"2026-01-01", "2026-01-02", "2026-01-12", "2026-02-11", "2026-02-23", "2026-03-20", "2026-04-29", "2026-05-04", "2026-05-05", "2026-05-06",
			"2026-07-20", "2026-08-11", "2026-09-21", "2026-09-22", "2026-09-23", "2026-10-12", "2026-11-03", "2026-11-23", "2026-12-31",
			"2027-01-01", "2027-01-11", "2027-02-11", "2027-02-23", "2027-03-22", "2027-04-29", "2027-05-03", "2027-05-04", "2027-05-05",
			"2027-07-19", "2027-08-11", "2027-09-20", "2027-09-23", "2027-10-11", "2027-11-03", "2027-11-23", "2027-12-31")},
	HKEX: {loc: zone("Asia/Hong_Kong"), hours: hours{clock(9, 30), clock(16, 0)}, lunch: hours{clock(12, 0), clock(13, 0)}, from: 2026, to: 2027,
		holidays: dates(
			"2026-01-01", "2026-02-17", "2026-02-18", "2026-02-19", "2026-04-03", "2026-04-06", "2026-04-07", "2026-05-01", "2026-05-25",
			"2026-06-19", "2026-07-01", "2026-10-01", "2026-10-19", "2026-12-25",
			"2027-01-01", "2027-02-08", "2027-02-09", "2027-03-26", "2027-03-29", "2027-04-05", "2027-05-13", "2027-06-09", "2027-07-01",
			"2027-09-16", "2027-10-01", "2027-10-08", "2027-12-27"),
		short: closesAt(clock(12, 0), "2026-02-16", "2026-12-24", "2026-12-31", "2027-02-05", "2027-12-24", "2027-12-31")},
	SSE: {loc: zone("Asia/Shanghai"), hours: hours{clock(9, 30), clock(15, 0)}, lunch: hours{clock(11, 30), clock(13, 0)}, from: 2026, to: 2026,
		holidays: dates(
			"2026-01-01", "2026-01-02", "2026-02-16", "2026-02-17", "2026-02-18", "2026-02-19", "2026-02-20", "2026-02-23",
			"2026-04-06", "2026-05-01", "2026-05-04", "2026-05-05", "2026-06-19", "2026-09-25",
			"2026-10-01", "2026-10-02", "2026-10-05", "2026-10-06", "2026-10-07")},
	SGX: {loc: zone("Asia/Singapore"), hours: hours{clock(9, 0), clock(17, 0)}, lunch: hours{clock(12, 0), clock(13, 0)}, from: 2026, to: 2027,
		until: "2027-02-05",
		holidays: dates("2026-01-01", "2026-02-17", "2026-02-18", "2026-04-03", "2026-05-01", "2026-05-27", "2026-06-01", "2026-08-10",
			"2026-11-09", "2026-12-25",
			"2027-01-01", "2027-02-08", "2027-03-10", "2027-03-26", "2027-05-17", "2027-05-20", "2027-08-09", "2027-10-28"),
		short: closesAt(clock(12, 0), "2026-02-16", "2026-12-24", "2026-12-31")},
	SET: {loc: zone("Asia/Bangkok"), hours: hours{clock(10, 0), clock(16, 30)}, lunch: hours{clock(12, 30), clock(14, 0)}, from: 2026, to: 2026,
		holidays: dates("2026-01-01", "2026-01-02", "2026-03-03", "2026-04-06", "2026-04-13", "2026-04-14", "2026-04-15", "2026-05-01",
			"2026-05-04", "2026-06-01", "2026-06-03", "2026-07-28", "2026-07-29", "2026-08-12", "2026-10-13", "2026-10-23", "2026-12-07",
			"2026-12-10", "2026-12-31")},
	TWSE: {loc: zone("Asia/Taipei"), hours: hours{clock(9, 0), clock(13, 30)}, from: 2026, to: 2026,
		holidays: dates("2026-01-01", "2026-02-12", "2026-02-13", "2026-02-16", "2026-02-17", "2026-02-18", "2026-02-19", "2026-02-20",
			"2026-02-27", "2026-04-03", "2026-04-06", "2026-05-01", "2026-06-19", "2026-09-25", "2026-09-28", "2026-10-09", "2026-10-26",
			"2026-12-25")},
}

func dates(days ...string) map[string]bool {
	out := make(map[string]bool, len(days))
	for _, d := range days {
		out[d] = true
	}
	return out
}

// closesAt marks days that open as usual and close early at close.
func closesAt(close hm, days ...string) map[string]hours {
	out := make(map[string]hours, len(days))
	for _, d := range days {
		out[d] = hours{close: close}
	}
	return out
}

// USSession reports whether NYSE's regular session (09:30–16:00 ET, 13:00 on
// an early-close day) is open at t, and when it next closes (open) or opens
// (closed), in UTC. Pre- and after-market hours count as closed.
func USSession(t time.Time) (open bool, next time.Time) {
	open, next, _ = calendars[NYSE].session(t)
	return open, next
}

// session reports whether c's regular session is open at t and when it next
// closes (open) or opens (closed), in UTC; next is zero when that boundary is
// past the verified horizon. known is false when t's local year is outside it.
func (c *calendar) session(t time.Time) (open bool, next time.Time, known bool) {
	if c == nil {
		return false, time.Time{}, false
	}
	local := t.In(c.loc)
	if !c.verified(local) {
		return false, time.Time{}, false
	}
	for d := range 16 { // longer than any closure (SSE's Spring Festival run, 11 days from close to open, is the longest)
		day := time.Date(local.Year(), local.Month(), local.Day()+d, 0, 0, 0, 0, c.loc)
		if !c.verified(day) {
			break
		}
		for _, s := range c.sessionsOn(day) {
			switch {
			case local.Before(s[0]):
				return false, s[0].UTC(), true
			case local.Before(s[1]):
				return true, s[1].UTC(), true
			}
		}
	}
	return false, time.Time{}, true // no known boundary
}

// finalClose is the last final close of a trading day at or before t (UTC),
// on c's verified calendar; false while t is inside a trading day (a lunch
// break included) or when no verified close is within 16 days.
func (c *calendar) finalClose(t time.Time) (time.Time, bool) {
	if c == nil {
		return time.Time{}, false
	}
	local := t.In(c.loc)
	for d := range 16 {
		day := time.Date(local.Year(), local.Month(), local.Day()-d, 0, 0, 0, 0, c.loc)
		if !c.verified(day) {
			break
		}
		s := c.sessionsOn(day)
		switch {
		case len(s) == 0:
		case !s[len(s)-1][1].After(t):
			return s[len(s)-1][1].UTC(), true
		case !t.Before(s[0][0]):
			return time.Time{}, false // inside the day
		}
	}
	return time.Time{}, false
}

// tradedFor is how long a regular session must have been open before a
// quote from before it counts as stale (openedSince).
const tradedFor = 30 * time.Minute

// openedSince reports whether a regular session (a lunch break's afternoon
// half counts) opened after asOf and had been open tradedFor by now (opened),
// from c's verified calendar. known is false when a local day from asOf to
// now is outside the verified horizon or the gap is over 16 days, as session.
func (c *calendar) openedSince(asOf, now time.Time) (known, opened bool) {
	if c == nil {
		return false, false
	}
	from, to := asOf.In(c.loc), now.In(c.loc)
	if to.Sub(from) > 16*24*time.Hour {
		return false, false
	}
	last := time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, c.loc)
	for day := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, c.loc); !day.After(last); day = day.AddDate(0, 0, 1) {
		if !c.verified(day) {
			return false, false
		}
		for _, s := range c.sessionsOn(day) {
			if s[0].After(asOf) && !now.Before(s[0].Add(tradedFor)) {
				return true, true
			}
		}
	}
	return true, false
}

// verified reports whether local's calendar day is inside the verified horizon.
func (c *calendar) verified(local time.Time) bool {
	key := local.Format(time.DateOnly)
	return local.Year() >= c.from && local.Year() <= c.to && (c.until == "" || key < c.until)
}

// lastVerifiedDay is c's last verified local day, at its local midnight: Dec 31
// of to, or the day before until when that comes first.
func (c *calendar) lastVerifiedDay() time.Time {
	last := time.Date(c.to, time.December, 31, 0, 0, 0, 0, c.loc)
	if c.until != "" {
		if u, err := time.ParseInLocation(time.DateOnly, c.until, c.loc); err == nil && !u.AddDate(0, 0, -1).After(last) {
			last = u.AddDate(0, 0, -1)
		}
	}
	return last
}

// CalendarWarnDays is how close a venue's last verified day may come before
// CalendarHorizons lists it: its next calendar is due.
const CalendarWarnDays = 30

// VenueHorizon is a session venue and its last verified local day (at local
// midnight): after it the venue's session is unknown.
type VenueHorizon struct {
	Venue string
	Last  time.Time
}

// CalendarHorizons lists, earliest first, every venue whose last verified day
// is fewer than CalendarWarnDays days after now's UTC date (or already past),
// so the boot log can warn before a calendar runs out.
func CalendarHorizons(now time.Time) []VenueHorizon {
	var out []VenueHorizon
	for venue, c := range calendars {
		if last := c.lastVerifiedDay(); daysBetween(now, last) < CalendarWarnDays {
			out = append(out, VenueHorizon{venue, last})
		}
	}
	slices.SortFunc(out, func(a, b VenueHorizon) int {
		return cmp.Or(cmp.Compare(a.Last.Format(time.DateOnly), b.Last.Format(time.DateOnly)), cmp.Compare(a.Venue, b.Venue))
	})
	return out
}

// daysBetween is the whole days from a's UTC date to b's local date.
func daysBetween(a, b time.Time) int {
	a = a.UTC()
	from := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, time.UTC)
	to := time.Date(b.Year(), b.Month(), b.Day(), 0, 0, 0, 0, time.UTC)
	return int(to.Sub(from).Hours() / 24)
}

// sessionsOn is day's regular trading intervals in order: none on a weekend or
// holiday, two around a lunch break, and a shortened day's own bounds.
func (c *calendar) sessionsOn(day time.Time) [][2]time.Time {
	key := day.Format(time.DateOnly)
	if wd := day.Weekday(); wd == time.Saturday || wd == time.Sunday || c.holidays[key] {
		return nil
	}
	h := c.hours
	if s, ok := c.short[key]; ok {
		h = hours{cmp.Or(s.open, h.open), cmp.Or(s.close, h.close)}
	}
	parts := []hours{h}
	if c.lunch.close > c.lunch.open {
		parts = []hours{{h.open, min(h.close, c.lunch.open)}, {max(h.open, c.lunch.close), h.close}}
	}
	var out [][2]time.Time
	y, m, d := day.Date()
	for _, p := range parts {
		if p.open < p.close {
			out = append(out, [2]time.Time{
				time.Date(y, m, d, int(p.open)/60, int(p.open)%60, 0, 0, c.loc),
				time.Date(y, m, d, int(p.close)/60, int(p.close)%60, 0, 0, c.loc)})
		}
	}
	return out
}

// withSessions returns owned rows/pointers; cached marks never freeze the
// session clock. It projects each perp's Listing and its venue's session at
// now; crypto keeps all of them nil. Each venue's session is computed once.
func withSessions(rows []Asset, now time.Time) []Asset {
	type venueSession struct {
		state string
		next  time.Time
		known bool
	}
	sessions := map[string]*venueSession{}
	sessionOf := func(venue string) *venueSession {
		if s := sessions[venue]; s != nil {
			return s
		}
		open, next, known := calendars[venue].session(now)
		s := &venueSession{state: "closed", next: next, known: known}
		if open {
			s.state = "open"
		}
		sessions[venue] = s
		return s
	}
	out := append([]Asset(nil), rows...)
	for i := range out {
		a := &out[i]
		a.Session, a.SessionChangeAt = nil, nil
		a.Region, a.SessionVenue, a.Underlying, a.LocalCurrency = nil, nil, nil, nil
		info, ok := lookupListed(a.Symbol)
		if !ok {
			info, ok = Lookup(a.Symbol)
		}
		if !ok || info.Listing == nil {
			continue
		}
		l := *info.Listing
		a.Region, a.SessionVenue, a.Underlying, a.LocalCurrency = &l.Region, &l.SessionVenue, &l.Underlying, &l.LocalCurrency
		s := sessionOf(l.SessionVenue)
		if a.QuoteFeed != nil {
			q := *a.QuoteFeed
			if s.known {
				q.MarketState = s.state // the verified calendar decides, as marketState does
			} else {
				q.MarketState = marketState(info, q.regularStart, q.regularEnd, now)
			}
			a.QuoteFeed = &q
		}
		if !s.known {
			continue
		}
		state := s.state
		a.Session = &state
		if !s.next.IsZero() {
			next := s.next
			a.SessionChangeAt = &next
		}
	}
	return out
}

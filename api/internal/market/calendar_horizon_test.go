package market

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// calendarSources is the official page to re-read for each venue's next
// calendar.
var calendarSources = map[string]string{
	NYSE: "https://www.nyse.com/trade/hours-calendars",
	KRX:  "https://global.krx.co.kr/contents/GLB/05/0501/0501110000/GLB0501110000.jsp (KRX market holidays)",
	TSE:  "https://www.jpx.co.jp/english/corporate/about-jpx/calendar/",
	HKEX: "https://www.gov.hk/en/about/abouthk/holiday/ (the gazetted general holidays) and hkex.com.hk/News/HKEX-Calendar",
	SSE:  "https://www.sse.com.cn/disclosure/dealinstruc/closed/list/ (the yearly holiday-closure notice)",
	SGX:  "https://www.sgx.com/stock-exchange/trading (half-days) and https://www.mom.gov.sg/employment-practices/public-holidays",
	SET:  "https://www.set.or.th/en/about/event-calendar/holiday",
	TWSE: "https://www.twse.com.tw/rwd/en/holidaySchedule/holidaySchedule?response=json",
}

// calendarWaiver lets a venue's horizon come within CalendarWarnDays while its
// next calendar is not yet published, up to and including until.
type calendarWaiver struct{ until, reason string }

var calendarWaivers = map[string]calendarWaiver{
	SET:  {"2026-12-10", "SET's 2026 calendar was its Nov 2025 edition"},
	TWSE: {"2026-12-20", "2027 JSON empty on 2026-10-13"},
	SSE:  {"2026-12-28", "the yearly notice came on 2025-12-22 and 2024-12-23"},
	SGX:  {"2027-01-20", "the 2026 calendar PDF was uploaded in January 2026"},
}

// calendarAlarms names each venue whose last verified day is fewer than
// CalendarWarnDays after today, with no waiver covering today.
func calendarAlarms(today time.Time) []string {
	var out []string
	day := today.UTC().Format(time.DateOnly)
	for _, h := range CalendarHorizons(today) {
		if w, ok := calendarWaivers[h.Venue]; ok && day <= w.until {
			continue
		}
		out = append(out, fmt.Sprintf("%s: verified to %s, re-read %s", h.Venue, h.Last.Format(time.DateOnly), calendarSources[h.Venue]))
	}
	return out
}

// The horizon guard: every session calendar runs at least CalendarWarnDays
// past today, or a dated waiver says why its next one is not out yet. When it
// fails, re-read the named page and add the next year's dates with a source line.
func TestCalendarHorizons(t *testing.T) {
	// ponytail: opt-in, or every clone's go test turns red the day a calendar runs low; the server
	// also logs these at boot. Run with CALENDAR_ALARMS=1 when refreshing the calendars.
	if os.Getenv("CALENDAR_ALARMS") == "1" {
		for _, a := range calendarAlarms(time.Now()) {
			t.Error(a)
		}
	}
	for venue, w := range calendarWaivers {
		until, err := time.ParseInLocation(time.DateOnly, w.until, calendars[venue].loc)
		if err != nil || until.After(calendars[venue].lastVerifiedDay()) || w.reason == "" {
			t.Errorf("%s waiver until %s (%q) runs past its last verified day %s", venue, w.until, w.reason, calendars[venue].lastVerifiedDay().Format(time.DateOnly))
		}
	}
	for venue := range calendars {
		if calendarSources[venue] == "" {
			t.Errorf("%s has no official page to re-read", venue)
		}
	}
}

// The guard's arithmetic at injected dates: a venue alarms once its waiver
// has ended and its last verified day is near or past.
func TestCalendarHorizonsInjected(t *testing.T) {
	day := func(s string) time.Time { d, _ := time.Parse(time.DateOnly, s); return d.Add(12 * time.Hour) }
	alarmed := func(today string) []string {
		var venues []string
		for _, a := range calendarAlarms(day(today)) {
			venues = append(venues, a[:strings.Index(a, ":")])
		}
		slices.Sort(venues)
		return venues
	}
	for _, c := range []struct {
		today string
		want  []string
	}{
		{"2026-10-13", nil},
		{"2026-12-10", nil},             // every waiver still holds
		{"2026-12-11", []string{"SET"}}, // SET's waiver ended
		{"2026-12-20", []string{"SET"}},
		{"2026-12-21", []string{"SET", "TWSE"}}, // TWSE's ended
		{"2026-12-29", []string{"SET", "SSE", "TWSE"}},
		{"2027-01-20", []string{"SET", "SSE", "TWSE"}},
		{"2027-01-25", []string{"SET", "SGX", "SSE", "TWSE"}}, // SGX's ended, 10 days before 2027-02-04
	} {
		if got := alarmed(c.today); !slices.Equal(got, c.want) {
			t.Errorf("%s: alarms %v, want %v", c.today, got, c.want)
		}
	}
	a := calendarAlarms(day("2026-12-21"))
	if len(a) != 2 || !strings.Contains(a[1], "TWSE: verified to 2026-12-31") || !strings.Contains(a[1], "twse.com.tw/rwd/en/holidaySchedule") {
		t.Errorf("TWSE alarm %q", a)
	}
	for venue, want := range map[string]string{NYSE: "2028-12-31", KRX: "2027-12-31", SGX: "2027-02-04", TWSE: "2026-12-31", SET: "2026-12-31"} {
		if got := calendars[venue].lastVerifiedDay(); got.Format(time.DateOnly) != want || got.Location() != calendars[venue].loc || got.Hour() != 0 {
			t.Errorf("%s last verified %v, want %s", venue, got, want)
		}
	}
	// The boot log's list: within 30 days, earliest first.
	if h := CalendarHorizons(day("2026-12-15")); len(h) != 3 || h[0].Venue != "SET" || h[1].Venue != "SSE" || h[2].Venue != "TWSE" {
		t.Errorf("horizons %v", h)
	}
	if h := CalendarHorizons(day("2026-10-13")); len(h) != 0 {
		t.Errorf("horizons today %v", h)
	}
}

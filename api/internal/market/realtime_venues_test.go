package market

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/market/rtquote"
	"github.com/vincenttanjaya/Foretrade/api/internal/market/rtquote/rtquotetest"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo"
)

// The five venues together: every registered adapter (rtquote.All) on one
// scheduler against fakes of their sources, each answering in its own format
// (rtquotetest.Board); and TWSE's chain of its two real sources.

// venuesOpen is whether a venue trades at t: its verified calendar, HOSE
// Vietcap's hours.
func venuesOpen(t *testing.T) func(string, time.Time) bool {
	t.Helper()
	hose, err := parseHours(*rtquote.Vietcap{}.Info().Hours)
	if err != nil {
		t.Fatal(err)
	}
	return func(venue string, at time.Time) bool {
		var h *rtHours
		if venue == HOSE {
			h = hose
		}
		days, loc := sessionDays(venue, h)
		return sessionAt(days, loc, at).open
	}
}

// venuesClient is a market client with adapters on board, at *now, with a
// USD rate for each venue's currency.
func venuesClient(t *testing.T, now *time.Time, adapters ...rtquote.Adapter) (*Client, *rtquotetest.Board) {
	t.Helper()
	c, _ := rtClient(t, now, adapters...)
	board := &rtquotetest.Board{Now: c.Now, Open: venuesOpen(t), Since: time.Date(2026, 10, 13, 5, 0, 0, 0, time.UTC)}
	for _, a := range feedRows() {
		if sym, ok := (rtquote.SGX{}).Symbol(a.Pair); ok && a.Listing != nil && a.Listing.SessionVenue == SGX {
			board.SGX = append(board.SGX, sym)
		}
	}
	c.RealtimeHTTP = &http.Client{Transport: board}
	for ccy, usd := range map[string]float64{"KRW": 0.0007, "HKD": 0.128, "VND": 0.000038, "SGD": 0.77, "TWD": 0.031} {
		c.feeds.fx[ccy] = feedFX{r: yahoo.Rate{Ccy: ccy, USD: usd, Symbol: ccy + "USD=X"}, at: *now}
	}
	return c, board
}

// feedOf is a named feed's quote fields as Assets serves them at now.
func feedOf(t *testing.T, c *Client, symbol string, now time.Time) QuoteFeedInfo {
	t.Helper()
	c.feeds.mu.Lock()
	defer c.feeds.mu.Unlock()
	as, ok := c.feeds.asset(named(t, symbol), now)
	if !ok || as.QuoteFeed == nil {
		t.Fatalf("%s not priced", symbol)
	}
	return *as.QuoteFeed
}

// TestRealtimeFiveVenuesDay: all six adapters on one scheduler through
// Wednesday 2026-10-14, each source answering in its own format. Each venue
// is polled at its own source's host with one honest request per interval
// while it trades, gets one settled read after its close and nothing else;
// every source stays ok all day; mid-morning /healthz names all five ok and
// each venue's named feed is priced from its source with its label; at
// night all five read closed. The per-host counts are the day's budget.
// Under -race (the polls run one after the other: nothing to race) it
// simulates the five minutes before mid-morning only.
func TestRealtimeFiveVenuesDay(t *testing.T) {
	day := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	// mid-morning, 12:00 KST, 11:00 HKT, 10:00 ICT, 11:00 SGT and 11:00 CST:
	// all five trade; the day starts after every Asian close of the 13th (the
	// boot makes their settled reads)
	mid := day.Add(3 * time.Hour)
	now, end := day.Add(-4*time.Hour), day.Add(24*time.Hour)
	if raceDetector {
		now, end = mid.Add(-5*time.Minute), mid.Add(time.Second)
	}
	c, board := venuesClient(t, &now, rtquote.All...)
	ok := regexp.MustCompile(`^krx=naver ok \d+s; hkex=tencent ok \d+s; hose=vietcap ok \d+s; sgx=sgx ok \d+s; twse=mis ok \d+s$`)
	for ; now.Before(end); now = now.Add(time.Second) {
		step(c)
		if !now.Equal(mid) {
			continue
		}
		if got := c.QuoteSources(); !ok.MatchString(got) {
			t.Fatalf("at 03:00 UTC: %s", got)
		}
		// each venue polled at 03:00:00: Naver's own clock dates a moved
		// volume; Tencent and MIS give the trade (a second back); Vietcap's
		// board clock is its newest message; SGX's snapshot is of 02:59
		for _, w := range []struct {
			symbol, source, class, basis, label string
			delay                               float64
		}{
			{"NAVER", "naver", ClassRealtime, "volume", "real-time · 03:00:00 UTC", 0},
			{"TENCENT", "tencent", ClassRealtime, "trade", "real-time · 02:59:59 UTC", 1},
			{"VCB", "vietcap", ClassRealtime, "volume", "real-time · 02:59:59 UTC", 1},
			{"DBS", "sgx", ClassNear, "snapshot", "about 1 min behind · 02:59 UTC", 60},
			{"MEDIATEK", "mis", ClassRealtime, "trade", "real-time · 02:59:59 UTC", 1},
		} {
			qf := feedOf(t, c, w.symbol, now)
			if qf.Source != w.source || qf.Class != w.class || qf.TimeBasis != w.basis || qf.Label != w.label || qf.DelaySeconds != w.delay {
				t.Errorf("%s at 03:00 UTC: %s %s %s %q delay %v", w.symbol, qf.Source, qf.Class, qf.TimeBasis, qf.Label, qf.DelaySeconds)
			}
		}
	}
	if raceDetector {
		t.Log("-race: the day's budget is checked without it")
		return
	}
	now = day.Add(24*time.Hour - time.Second) // 08:59:59 KST on the 15th: before the next open
	night := regexp.MustCompile(`^krx=naver closed \d+s; hkex=tencent closed \d+s; hose=vietcap closed \d+s; sgx=sgx closed \d+s; twse=mis closed \d+s$`)
	if got := c.QuoteSources(); !night.MatchString(got) {
		t.Fatalf("at night: %s", got)
	}
	// after its close each venue's settled read still prices it, dated by the
	// last trade before the close (the board trades nothing after it)
	for _, w := range []struct{ symbol, source, label string }{
		{"NAVER", "naver", "real-time · 06:29:50 UTC"}, {"TENCENT", "tencent", "real-time · 07:59:49 UTC"},
		{"VCB", "vietcap", "real-time · 07:44:49 UTC"}, {"DBS", "sgx", "about 1 min behind · 08:59 UTC"},
		{"MEDIATEK", "mis", "real-time · 05:29:49 UTC"},
	} {
		if qf := feedOf(t, c, w.symbol, now); qf.Source != w.source || qf.Label != w.label {
			t.Errorf("%s at night: %s %q", w.symbol, qf.Source, qf.Label)
		}
	}

	type host struct {
		source            string
		requests, busiest int
		perMinute         map[int]int
		first, last       time.Time
	}
	hosts := map[string]*host{}
	all := map[int]int{}
	total := 0
	for _, r := range board.Requests() {
		if r.Source == "" || r.Status != http.StatusOK {
			t.Fatalf("%s %s%s answered %d", r.Method, r.Host, r.Path, r.Status)
		}
		for k := range r.Header {
			if k != "Accept" && k != "Content-Type" && k != "User-Agent" && k != "Accept-Encoding" {
				t.Fatalf("%s sent %s", r.Source, k)
			}
		}
		if r.Header.Get("User-Agent") != rtquote.UserAgent || r.Symbols == 0 {
			t.Fatalf("%s: %v, %d symbols", r.Source, r.Header, r.Symbols)
		}
		if r.At.Before(day) {
			continue // the 13th's settled reads at boot
		}
		h := hosts[r.Host]
		if h == nil {
			h = &host{source: r.Source, perMinute: map[int]int{}, first: r.At}
			hosts[r.Host] = h
		}
		if h.source != r.Source {
			t.Fatalf("%s asked %s's host %s", r.Source, h.source, r.Host)
		}
		m := int(r.At.Sub(day) / time.Minute)
		h.requests++
		h.perMinute[m]++
		h.busiest = max(h.busiest, h.perMinute[m])
		h.last = r.At
		all[m]++
		total++
	}
	peak := 0
	for _, n := range all {
		peak = max(peak, n)
	}
	want := map[string]struct {
		requests, busiest int
		first, last       string
	}{ // the session every interval, then the settled read; MIS's first read at 09:00:00 CST still
		// shows the 13th's trades (its own Closed), so its next look is a minute later, not 10 s
		"polling.finance.naver.com": {2341, 6, "00:00:00", "06:32:00"}, "qt.gtimg.cn": {1981, 6, "01:30:00", "08:12:00"},
		"trading.vietcap.com.vn": {1441, 6, "02:15:00", "07:50:00"}, "api.sgx.com": {841, 2, "01:00:00", "09:20:00"},
		"mis.twse.com.tw": {1616, 6, "01:00:00", "05:35:00"},
	}
	for _, a := range rtquote.All {
		in := a.Info()
		name := strings.TrimPrefix(in.Host, "https://")
		h, w := hosts[name], want[name]
		if in.Gate != "" {
			if h != nil {
				t.Errorf("%s (TWSE's second source) asked %d times while MIS served", in.Name, h.requests)
			}
			t.Logf("%s (%s): standby behind %s's first source", name, in.Name, in.Venue)
			continue
		}
		if h == nil {
			t.Fatalf("%s: no request", in.Name)
		}
		t.Logf("%s (%s): %d requests on 2026-10-14, busiest minute %d, first %s, last %s UTC", name, in.Name, h.requests, h.busiest,
			h.first.Format(time.TimeOnly), h.last.Format(time.TimeOnly))
		if h.requests != w.requests || h.busiest != w.busiest || h.first.Format(time.TimeOnly) != w.first || h.last.Format(time.TimeOnly) != w.last {
			t.Errorf("%s: want %d requests, busiest minute %d, %s to %s", in.Name, w.requests, w.busiest, w.first, w.last)
		}
		if s := src(c, in.Venue, 0); s.state != rtOK || s.refusals != 0 {
			t.Errorf("%s ended %s (%s), %d refusals", in.Name, s.state, s.reason, s.refusals)
		}
	}
	t.Logf("all hosts: %d requests, busiest minute %d", total, peak)
	if total != 8220 || peak > 26 {
		t.Errorf("all hosts: %d requests, busiest minute %d", total, peak)
	}
}

// TestRealtimeTWSEChain: TWSE's two real sources through the framework. MIS
// refused with TWSE's F5 page (HTTP 502): recorded with its support ID,
// paused, and Yahoo Taiwan serves real time ("twse=yahoo-tw fallback"); once
// the pause ends and MIS answers, it serves again and Yahoo Taiwan stands by.
// MIS refusing the settled read leaves it to Yahoo Taiwan; both refusing it
// leaves it at that, with nothing more asked before the next open.
func TestRealtimeTWSEChain(t *testing.T) {
	tw := func(h, m, s int) time.Time { return time.Date(2026, 10, 14, h, m, s, 0, zone("Asia/Taipei")).UTC() }
	now := tw(11, 0, 0)
	c, board := venuesClient(t, &now, rtquote.MIS{}, rtquote.YahooTW{})
	board.Refuse("mis", true)
	step(c) // MIS: refused
	now = now.Add(10 * time.Second)
	step(c) // Yahoo Taiwan: ok at once (trade times, past the open's grace)
	if got := c.QuoteSources(); !regexp.MustCompile(`; twse=yahoo-tw fallback [01]s$`).MatchString(got) {
		t.Fatal(got)
	}
	qf := feedOf(t, c, "MEDIATEK", now)
	if qf.Source != "yahoo-tw" || qf.Class != ClassRealtime || qf.Label != "real-time · 03:00:09 UTC" || qf.DelaySeconds != 1 {
		t.Fatalf("%+v", qf)
	}
	mis := src(c, TWSE, 0)
	if mis.state != rtRefused || mis.refusal == nil || mis.refusal.Status != http.StatusBadGateway ||
		!strings.Contains(mis.refusal.Reason, "FOR SECURITY REASONS") || !strings.Contains(mis.refusal.Reason, "support ID 1234567890123456789") {
		t.Fatalf("%s %+v", mis.state, mis.refusal)
	}
	// MIS's pause (30 minutes) ends: it is refused again and paused an hour;
	// the hour ends, it answers, proves itself and serves
	for end := tw(12, 31, 0); now.Before(end); now = now.Add(time.Second) {
		step(c)
		if now.Equal(tw(11, 45, 0)) {
			board.Refuse("mis", false)
		}
		if now.Equal(tw(11, 30, 10)) && mis.refusals != 2 {
			t.Fatalf("%d refusals at 11:30", mis.refusals)
		}
	}
	if got := c.QuoteSources(); !regexp.MustCompile(`; twse=mis ok \d+s$`).MatchString(got) {
		t.Fatal(got)
	}
	count := func(source string, from time.Time) int {
		n := 0
		for _, r := range board.Requests() {
			if r.Source == source && !r.At.Before(from) {
				n++
			}
		}
		return n
	}
	if n := count("yahoo-tw", tw(12, 30, 11)); n != 0 {
		t.Fatalf("Yahoo Taiwan asked %d times after MIS came back", n)
	}
	// the settled read at 13:35: MIS refuses it, so Yahoo Taiwan makes it
	for end := tw(13, 40, 0); now.Before(end); now = now.Add(time.Second) {
		if now.Equal(tw(13, 34, 0)) {
			board.Refuse("mis", true)
		}
		step(c)
	}
	if mis, ytw := count("mis", tw(13, 30, 0)), count("yahoo-tw", tw(13, 30, 0)); mis != 1 || ytw != 1 {
		t.Fatalf("settled reads: MIS %d, Yahoo Taiwan %d", mis, ytw)
	}
	// the next day both refuse it: nothing more is asked until the open after
	now = tw(13, 30, 0).Add(24 * time.Hour)
	c, board = venuesClient(t, &now, rtquote.MIS{}, rtquote.YahooTW{})
	board.Refuse("mis", true)
	board.Refuse("yahoo-tw", true)
	for end := tw(8, 59, 0).Add(48 * time.Hour); now.Before(end); now = now.Add(5 * time.Second) {
		step(c)
	}
	if n := len(board.Requests()); n != 2 {
		t.Fatalf("%d requests after both refused the settled read", n)
	}
}

package market

import (
	"bytes"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/market/rtquote"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo"
)

// HKEX's real-time source (rtquote.Tencent) through the framework, on its
// saved answers (rtquote/testdata/tencent_*): what it serves and how it is
// labelled, how the framework catches the delayed feed (a plain-prefix
// answer, or a silent switch caught by the canaries' trade times), its
// refusals, and a whole HKEX day's schedule at its rate.

func tencentBody(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("rtquote/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// tencentClient is a market client with Tencent alone on a fake HTTP side
// answering body, at *now, with Yahoo holding Tencent (0700.HK) 15 minutes
// behind as it was at 13:16 HKT on 2026-10-14 (0700 at 425.2, 13:01:21).
func tencentClient(t *testing.T, now *time.Time, body []byte) (*Client, *rtFake) {
	t.Helper()
	c, fake := rtClient(t, now, rtquote.Tencent{})
	c.feeds.fx["HKD"] = feedFX{r: yahoo.Rate{Ccy: "HKD", USD: 0.1286, Symbol: "HKDUSD=X"}, at: *now}
	fake.answer = func(*http.Request) (int, string) { return 200, string(body) }
	yahooQuote(c, named(t, "TENCENT"), 425.2, time.Date(2026, 10, 14, 5, 1, 21, 0, time.UTC), now.Add(-30*time.Second))
	return c, fake
}

// TestTencentServes: the six-name read at 13:16:36 HKT makes Tencent ok and
// prices 0700.HK in real time, labelled from its own trade time; the
// request was one honest GET for every HKEX listing and canary.
func TestTencentServes(t *testing.T) {
	now := time.Date(2026, 10, 14, 5, 16, 36, 375e6, time.UTC)
	c, fake := tencentClient(t, &now, tencentBody(t, "tencent_r4_rt6.txt"))
	if step(c) != 1 {
		t.Fatal("no poll")
	}
	if s := src(c, HKEX, 0); s.state != rtOK {
		t.Fatalf("%s %q", s.state, s.reason)
	}
	if got := c.QuoteSources(); !strings.Contains(got, "; hkex=tencent ok 1s; ") {
		t.Fatal(got)
	}
	c.feeds.mu.Lock()
	as, ok := c.feeds.asset(named(t, "TENCENT"), now)
	c.feeds.mu.Unlock()
	qf := as.QuoteFeed
	if !ok || qf.Source != "tencent" || qf.Class != ClassRealtime || qf.TimeBasis != "trade" || qf.LocalPrice != 427.6 ||
		qf.Label != "real-time · 05:16:34 UTC" || qf.DelaySeconds != 2.4 || !qf.LastTradeAt.Equal(time.Date(2026, 10, 14, 5, 16, 34, 0, time.UTC)) {
		t.Fatalf("%v %+v", ok, qf)
	}
	req := fake.reqs[0]
	if req.Method != http.MethodGet || req.URL.Host != "qt.gtimg.cn" || !strings.HasPrefix(req.URL.Path, "/q=r_hk") ||
		req.Header.Get("User-Agent") != rtquote.UserAgent || len(req.Header) != 2 || req.Header.Get("Accept") == "" {
		t.Fatalf("%s %s %v", req.Method, req.URL, req.Header)
	}
	asked := strings.Split(strings.TrimPrefix(req.URL.Path, "/q="), ",")
	for _, c := range []string{"r_hk09988", "r_hk00700", "r_hk01810", "r_hk00001", "r_hk03690"} {
		if !slices.Contains(asked, c) {
			t.Errorf("%s not asked for", c)
		}
	}
	if len(asked) != 89 { // the 5 named feeds, the 83 listed rows and the canary 9988 (the screener's rows join when it runs)
		t.Errorf("asked %d codes", len(asked))
	}
}

// TestTencentDelayedFeedFallsBack: the delayed feed never serves. A silent
// switch (the 15-minute feed under the r_ name) parses, but its canaries'
// newest trade is 15 minutes old: degraded at once, and Yahoo serves. An
// answer of plain-prefix lines lands no row: three such reads degrade it,
// each line listed with its reason.
func TestTencentDelayedFeedFallsBack(t *testing.T) {
	plain := tencentBody(t, "tencent_r4_plain.txt")
	now := time.Date(2026, 10, 14, 5, 16, 27, 871e6, time.UTC)
	c, _ := tencentClient(t, &now, bytes.ReplaceAll(plain, []byte("v_hk"), []byte("v_r_hk")))
	step(c)
	s := src(c, HKEX, 0)
	if s.state != rtDegraded || !strings.Contains(s.reason, "canaries stale: the newest trade is 15m7s old") {
		t.Fatalf("silent switch: %s %q", s.state, s.reason)
	}
	if got := c.QuoteSources(); !strings.Contains(got, "; hkex=yahoo degraded ") {
		t.Fatal(got)
	}
	c.feeds.mu.Lock()
	as, _ := c.feeds.asset(named(t, "TENCENT"), now)
	c.feeds.mu.Unlock()
	if as.QuoteFeed.Source != "yahoo" || !strings.HasPrefix(as.QuoteFeed.Label, "delayed · last trade ") {
		t.Fatalf("%+v", as.QuoteFeed)
	}

	now = now.Add(24 * time.Hour) // Thursday, a fresh client
	c, fake := tencentClient(t, &now, plain)
	for range 3 {
		step(c)
		now = now.Add(10 * time.Second)
	}
	s = src(c, HKEX, 0)
	if fake.count() != 3 || s.state != rtDegraded || !strings.Contains(s.reason, "3 failed reads in a row: no usable row") {
		t.Fatalf("plain prefix: %d reads, %s %q", fake.count(), s.state, s.reason)
	}
	if len(s.recent) == 0 || !strings.Contains(s.recent[0].Reason, "delayed feed") || s.recent[0].Ticker == "" {
		t.Fatalf("rejected %+v", s.recent)
	}
	if got := c.QuoteSources(); !strings.Contains(got, "; hkex=yahoo degraded ") {
		t.Fatal(got)
	}
}

// TestTencentRefused: a 403, and a block page served with 200, are refusals:
// the source is paused 30 minutes and Yahoo serves; a malformed answer is a
// failed read.
func TestTencentRefused(t *testing.T) {
	now := time.Date(2026, 10, 14, 5, 16, 36, 0, time.UTC)
	c, fake := tencentClient(t, &now, nil)
	fake.answer = func(*http.Request) (int, string) { return 403, "Forbidden" }
	step(c)
	if s := src(c, HKEX, 0); s.state != rtRefused || !s.paused.Equal(now.Add(30*time.Minute)) || s.refusal.Status != 403 {
		t.Fatalf("403: %s %v %+v", s.state, s.paused, s.refusal)
	}
	if got := c.QuoteSources(); !strings.Contains(got, "; hkex=yahoo fallback ") {
		t.Fatal(got)
	}
	now = now.Add(30 * time.Minute)
	fake.answer = func(*http.Request) (int, string) { return 200, "<html><body>Access denied</body></html>" }
	step(c)
	if s := src(c, HKEX, 0); s.state != rtRefused || s.refusals != 2 || !strings.Contains(s.refusal.Reason, "HTML page") ||
		!s.paused.Equal(now.Add(time.Hour)) {
		t.Fatalf("block page: %s %+v %v", s.state, s.refusal, s.paused)
	}
	now = now.Add(time.Hour)
	fake.answer = func(*http.Request) (int, string) { return 200, `v_r_hk00700="100~` } // cut off
	step(c)
	if s := src(c, HKEX, 0); s.state != rtRefused || s.fails != 1 || s.refusals != 2 {
		t.Fatalf("malformed: %s fails %d refusals %d", s.state, s.fails, s.refusals)
	}
}

var tencentTime = regexp.MustCompile(`20[0-9]{2}/[0-9]{2}/[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2}`)

// TestTencentDay: Wednesday 2026-10-14 from 08:00 HKT with Tencent
// answering fresh trades: the boot's settled read of the previous close,
// then every 10 s through 09:30-12:00 and 13:00-16:00 (none in the lunch
// break, never two requests under 10 s apart), and the settled read at 16:12,
// past the closing auction: 1,981 requests in the day's sessions, at most 6 a
// minute; ok throughout.
func TestTencentDay(t *testing.T) {
	hk := func(h, m int) time.Time { return time.Date(2026, 10, 14, h, m, 0, 0, zone("Asia/Hong_Kong")).UTC() }
	now := hk(8, 0)
	c, fake := rtClient(t, &now, rtquote.Tencent{}) // Yahoo holds nothing: the source answers alone
	c.feeds.fx["HKD"] = feedFX{r: yahoo.Rate{Ccy: "HKD", USD: 0.1286, Symbol: "HKDUSD=X"}, at: now}
	body := tencentBody(t, "tencent_r4_rt6.txt")
	fake.answer = func(*http.Request) (int, string) { // every line trades at the clock's now
		return 200, string(tencentTime.ReplaceAll(body, []byte(now.In(zone("Asia/Hong_Kong")).Format("2006/01/02 15:04:05"))))
	}
	notOK := 0
	for end := hk(23, 59); now.Before(end); now = now.Add(time.Second) {
		step(c)
		if now.After(hk(9, 32)) && src(c, HKEX, 0).state != rtOK { // idle until the first check, 2 minutes after the open
			notOK++
		}
	}
	if notOK != 0 || src(c, HKEX, 0).state != rtOK {
		t.Fatalf("not ok for %d s: %s %q", notOK, src(c, HKEX, 0).state, src(c, HKEX, 0).reason)
	}
	if !fake.at[0].Equal(hk(8, 0)) {
		t.Fatalf("boot read at %v", fake.at[0])
	}
	day := fake.at[1:]
	perMinute := map[time.Time]int{}
	for i, at := range day {
		perMinute[at.Truncate(time.Minute)]++
		if !at.Before(hk(12, 0)) && at.Before(hk(13, 0)) || at.Before(hk(9, 30)) {
			t.Fatalf("polled at %v", at.In(zone("Asia/Hong_Kong")))
		}
		if i > 0 && at.Sub(day[i-1]) < 10*time.Second {
			t.Fatalf("%v then %v", day[i-1], at)
		}
	}
	busiest := 0
	for _, n := range perMinute {
		busiest = max(busiest, n)
	}
	if len(day) != 900+1080+1 || busiest != 6 || !day[len(day)-1].Equal(hk(16, 12)) || !day[0].Equal(hk(9, 30)) {
		t.Fatalf("%d requests, busiest minute %d, first %v, last %v", len(day), busiest, day[0], day[len(day)-1])
	}
}

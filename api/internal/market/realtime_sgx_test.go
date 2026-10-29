package market

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/market/rtquote"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo"
)

// SGX's adapter (rtquote.SGX) through the framework, on its trimmed real
// answers (rtquote/testdata/sgx_*.json) and an injected clock: how its rows
// are dated, labelled and served, its rate floor, a stale board and a
// refusal falling back to Yahoo, and the settled read after the close.

// sgxAt is a UTC moment on 2026-10-14 ("08:21:05").
func sgxAt(hms string) time.Time {
	t, err := time.Parse(time.DateTime, "2026-10-14 "+hms)
	if err != nil {
		panic(err)
	}
	return t
}

// sgxServer answers every request with the body of the fixture it is set
// to or, when live is set, with s7's board as SGX would publish it at the
// clock's now: every row at the minute 90 s before (the research's median
// lag is 99 s), each volume up by 100 shares a request.
type sgxServer struct {
	mu    sync.Mutex
	body  string
	live  []map[string]any
	reads int
	now   func() time.Time
}

func (s *sgxServer) answer() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.live == nil {
		return s.body
	}
	s.reads++
	now := s.now()
	minute := now.Add(-90 * time.Second).Truncate(time.Minute).Format("20060102_150405")
	var rows []map[string]any
	for _, r := range s.live {
		c := map[string]any{}
		for k, v := range r {
			c[k] = v
		}
		c["trading_time"], c["vl"] = minute, r["vl"].(float64)+0.1*float64(s.reads)
		rows = append(rows, c)
	}
	b, _ := json.Marshal(map[string]any{"meta": map[string]any{"code": "200", "processedTime": now.UnixMilli()},
		"data": map[string]any{"prices": rows}})
	return string(b)
}

// goLive makes s answer a moving board from s7's rows.
func (s *sgxServer) goLive(t *testing.T, now func() time.Time) {
	t.Helper()
	b, err := os.ReadFile("rtquote/testdata/sgx_s7_session.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Data struct {
			Prices []map[string]any `json:"prices"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.live, s.now = doc.Data.Prices, now
	s.mu.Unlock()
}

func (s *sgxServer) set(t *testing.T, name string) {
	t.Helper()
	b, err := os.ReadFile("rtquote/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.body = string(b)
	s.mu.Unlock()
}

func (s *sgxServer) setBody(body string) {
	s.mu.Lock()
	s.body = body
	s.mu.Unlock()
}

// sgxClient is a market client with only the SGX adapter, its requests
// answered by srv, at *now.
func sgxClient(t *testing.T, now *time.Time) (*Client, *rtFake, *sgxServer) {
	t.Helper()
	c, fake := rtClient(t, now, rtquote.SGX{})
	srv := &sgxServer{}
	fake.answer = func(*http.Request) (int, string) { return 200, srv.answer() }
	c.feeds.mu.Lock()
	c.feeds.fx["SGD"] = feedFX{r: yahoo.Rate{Ccy: "SGD", USD: 0.77, Symbol: "SGDUSD=X"}, at: *now}
	c.feeds.mu.Unlock()
	return c, fake, srv
}

// sgxFeed is the DBS quote feed row's QuoteFeed at now.
func sgxFeed(t *testing.T, c *Client, now time.Time) *QuoteFeedInfo {
	t.Helper()
	c.feeds.mu.Lock()
	defer c.feeds.mu.Unlock()
	as, ok := c.feeds.asset(named(t, "DBS"), now)
	if !ok || as.QuoteFeed == nil {
		t.Fatal("no DBS asset")
	}
	return as.QuoteFeed
}

// TestSGXServesSnapshots: after the 13:00 SGT reopen the first answer is a
// baseline, the next (the 13:00 snapshot, read 05:01:50 UTC) dates every
// moved row at its minute; no verdict in the quiet window; in the afternoon
// a fresh answer makes the source ok and DBS reads "about 1 min behind",
// thin names whose volume did not move keep their earlier minute, and the
// header names sgx with its newest trade's age.
func TestSGXServesSnapshots(t *testing.T) {
	now := sgxAt("05:00:05")
	c, fake, srv := sgxClient(t, &now)
	srv.set(t, "sgx_r2_break.json")
	if step(c) != 1 {
		t.Fatal("no poll at 13:00:05 SGT")
	}
	now = sgxAt("05:01:50")
	srv.set(t, "sgx_r3_reopen.json")
	step(c)
	s := src(c, SGX, 0)
	c.feeds.mu.Lock()
	dbs, state := *s.rows["D05.SI"], s.state
	c.feeds.mu.Unlock()
	if !dbs.trade.Equal(sgxAt("05:00:00")) || dbs.basis != "snapshot" || dbs.price != 74.1 || *dbs.volume != 1_703_830 {
		t.Fatalf("DBS at the reopen %+v", dbs)
	}
	if state != rtIdle { // 13:01:50 SGT is in the quiet window: no verdict yet
		t.Fatalf("state %s in the quiet window", state)
	}

	now = sgxAt("08:21:05")
	srv.set(t, "sgx_s7_session.json")
	step(c)
	q := sgxFeed(t, c, now)
	if q.Source != "sgx" || q.Class != ClassNear || q.TimeBasis != "snapshot" || q.DelaySeconds != 65 ||
		q.Label != "about 1 min behind · 08:20 UTC" || q.LocalPrice != 74.12 || !q.LastTradeAt.Equal(sgxAt("08:20:00")) {
		t.Fatalf("DBS feed %+v", q)
	}
	now = sgxAt("08:23:02")
	srv.set(t, "sgx_s8_session.json")
	step(c)
	c.feeds.mu.Lock()
	for ticker, want := range map[string]string{"D05.SI": "08:21:00", "A17U.SI": "08:21:00", "F34.SI": "08:20:00", "J69U.SI": "08:20:00"} {
		if got := s.rows[ticker].trade; !got.Equal(sgxAt(want)) {
			t.Errorf("%s dated %v, want %s", ticker, got, want)
		}
	}
	c.feeds.mu.Unlock()
	if h := c.QuoteSources(); !strings.Contains(h, "sgx=sgx ok 122s") {
		t.Fatalf("header %q", h)
	}
	if q := sgxFeed(t, c, now); q.Label != "about 2 min behind · 08:21 UTC" || q.DelaySeconds != 122 {
		t.Fatalf("DBS feed %+v", q)
	}
	if lq := c.ListingQuotes([]string{"D05.SI"})["D05.SI"]; lq.Source != "sgx" || lq.Class != ClassNear || lq.Refresh != 30*time.Second {
		t.Fatalf("listing quote %+v", lq)
	}
	if fake.count() != 4 {
		t.Fatalf("%d requests", fake.count())
	}
}

// TestSGXRateFloor: over an open hour the real adapter is asked exactly
// every 30 s, never two requests closer, at most 2 in a minute; a fill
// re-read inside the floor makes no request, and one after it makes one.
func TestSGXRateFloor(t *testing.T) {
	now := sgxAt("06:00:00") // 14:00 SGT
	c, fake, srv := sgxClient(t, &now)
	srv.goLive(t, func() time.Time { return now })
	for end := now.Add(time.Hour); now.Before(end); now = now.Add(time.Second) {
		step(c)
	}
	if fake.count() != 120 || src(c, SGX, 0).state != rtOK {
		t.Fatalf("%d requests in an hour (want 120), state %s", fake.count(), src(c, SGX, 0).state)
	}
	perMinute := map[int]int{}
	for i, at := range fake.at {
		perMinute[int(at.Sub(sgxAt("06:00:00"))/time.Minute)]++
		if i > 0 && at.Sub(fake.at[i-1]) < 30*time.Second {
			t.Fatalf("requests %v and %v are under 30 s apart", fake.at[i-1], at)
		}
	}
	for m, n := range perMinute {
		if n > 2 {
			t.Fatalf("%d requests in minute %d", n, m)
		}
	}
	dbs := named(t, "DBS")
	fill := func() int {
		c.feeds.mu.Lock()
		polls := c.feeds.rtFillPolls([]AssetInfo{dbs}, now, 0) // every quote counts as old
		c.feeds.mu.Unlock()
		c.rtFill(context.Background(), polls)
		return len(polls)
	}
	last := fake.at[len(fake.at)-1]
	now = last.Add(10 * time.Second)
	if n := fill(); n != 0 {
		t.Fatalf("a fill 10 s after a poll made %d requests", n)
	}
	now = last.Add(30 * time.Second)
	if n := fill(); n != 1 || fake.count() != 121 {
		t.Fatalf("a fill after the floor made %d requests (%d in all)", n, fake.count())
	}
}

// TestSGXStaleBoardFallsBack: a board whose minute and volumes stop moving
// while the session is open (s8 answered again and again) leaves the
// canaries' newest trade at 08:21; once that is over 5 minutes old the
// source is degraded and DBS is priced from Yahoo again, labelled delayed.
func TestSGXStaleBoardFallsBack(t *testing.T) {
	now := sgxAt("08:21:05")
	c, _, srv := sgxClient(t, &now)
	yahooQuote(c, named(t, "DBS"), 74.10, sgxAt("08:11:00"), now) // Yahoo, 10 minutes behind
	srv.set(t, "sgx_s7_session.json")
	step(c)
	now = sgxAt("08:21:35")
	srv.set(t, "sgx_s8_session.json")
	var degradedAt time.Time
	for ; now.Before(sgxAt("08:30:00")); now = now.Add(time.Second) {
		step(c)
		c.feeds.mu.Lock()
		s := c.feeds.rtVenue(SGX).chain[0]
		if s.state == rtDegraded && degradedAt.IsZero() {
			degradedAt = now
			if !strings.Contains(s.reason, "canaries stale") {
				t.Errorf("reason %q", s.reason)
			}
		}
		c.feeds.mu.Unlock()
		if !degradedAt.IsZero() {
			break
		}
		if now.Equal(sgxAt("08:24:00")) {
			if q := sgxFeed(t, c, now); q.Source != "sgx" {
				t.Fatalf("at 08:24 %+v", q)
			}
		}
	}
	if degradedAt.Before(sgxAt("08:26:00")) || degradedAt.After(sgxAt("08:26:35")) {
		t.Fatalf("degraded at %v, want the first poll after 08:26:00", degradedAt)
	}
	q := sgxFeed(t, c, now)
	if q.Source != "yahoo" || q.Class != ClassDelayed || !strings.HasPrefix(q.Label, "delayed · last trade 08:11") {
		t.Fatalf("after the fallback %+v", q)
	}
	if h := c.QuoteSources(); !strings.Contains(h, "sgx=yahoo degraded") {
		t.Fatalf("header %q", h)
	}
}

// TestSGXRefusedFallsBack: an HTML page served for the board is a refusal:
// the source is paused 30 minutes and recorded, and the venue serves Yahoo.
func TestSGXRefusedFallsBack(t *testing.T) {
	now := sgxAt("02:00:00") // 10:00 SGT
	c, fake, srv := sgxClient(t, &now)
	srv.setBody("<html><head><title>Access Denied</title></head><body>Request blocked.</body></html>")
	step(c)
	s := src(c, SGX, 0)
	c.feeds.mu.Lock()
	state, paused, refusals := s.state, s.paused, s.refusals
	c.feeds.mu.Unlock()
	if state != rtRefused || !paused.Equal(now.Add(30*time.Minute)) || refusals != 1 {
		t.Fatalf("state %s paused %v refusals %d", state, paused, refusals)
	}
	for end := now.Add(29 * time.Minute); now.Before(end); now = now.Add(time.Second) {
		step(c)
	}
	if fake.count() != 1 {
		t.Fatalf("%d requests while paused", fake.count()-1)
	}
	if h := c.QuoteSources(); !strings.Contains(h, "sgx=yahoo fallback") {
		t.Fatalf("header %q", h)
	}
}

// TestSGXSettledRead: after 17:00 SGT nothing is asked until 17:20, when the
// one settled read finds the board stopped at 17:16 (our read of
// 2026-10-14): the close is dated at the session's end and delaySeconds is
// 0; nothing more is asked that night.
func TestSGXSettledRead(t *testing.T) {
	now := sgxAt("08:21:05")
	c, fake, srv := sgxClient(t, &now)
	srv.set(t, "sgx_s7_session.json")
	step(c)
	now = sgxAt("08:23:02")
	srv.set(t, "sgx_s8_session.json")
	step(c) // ok: the last in-session board we hold
	if st := src(c, SGX, 0).state; st != rtOK {
		t.Fatalf("state %s", st)
	}
	now = sgxAt("09:00:00") // 17:00 SGT: the close
	srv.set(t, "sgx_after_close.json")
	var asked []time.Time
	for end := sgxAt("16:00:00"); now.Before(end); now = now.Add(time.Second) {
		if step(c) > 0 {
			asked = append(asked, now)
		}
	}
	if len(asked) != 1 || !asked[0].Equal(sgxAt("09:20:00")) {
		t.Fatalf("after the close asked at %v, want once at 09:20:00 UTC", asked)
	}
	q := sgxFeed(t, c, now)
	if q.Source != "sgx" || q.LocalPrice != 74.34 || !q.LastTradeAt.Equal(sgxAt("09:00:00")) || q.DelaySeconds != 0 {
		t.Fatalf("settled DBS %+v", q)
	}
	if h := c.QuoteSources(); !strings.Contains(h, "sgx=sgx closed") || fake.count() != 3 {
		t.Fatalf("header %q, %d requests", h, fake.count())
	}
}

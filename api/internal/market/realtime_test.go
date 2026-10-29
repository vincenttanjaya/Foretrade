package market

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/market/rtquote"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo"
)

// kst is a KRX wall-clock time on 2026-10-14 (a Wednesday the verified
// calendar opens 09:00-15:30 KST): every test picks its own moment.
func kst(h, m, s int) time.Time {
	return time.Date(2026, 10, 14, h, m, s, 0, zone("Asia/Seoul")).UTC()
}

// fakeRT is a test adapter over a tiny JSON shape:
// {"stamp": RFC3339, "closed": bool, "rows": [{"s": sym, "p": price, "c": ccy, "at": RFC3339, "v": volume}]}.
type fakeRT struct{ info rtquote.Info }

func (f fakeRT) Info() rtquote.Info { return f.info }
func (f fakeRT) Symbol(t string) (string, bool) {
	i := strings.LastIndexByte(t, '.')
	if i <= 0 {
		return "", false
	}
	return t[:i], true
}
func (f fakeRT) Request(ctx context.Context, host string, want rtquote.Want) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, host+"/q?s="+strings.Join(want.Symbols(), ","), nil)
	if err == nil {
		req.Header.Set("Accept", "application/json")
	}
	return req, err
}
func (f fakeRT) Parse(body []byte, want rtquote.Want) (rtquote.Batch, error) {
	var doc struct {
		Stamp  time.Time `json:"stamp"`
		Closed bool      `json:"closed"`
		Rows   []struct {
			S  string    `json:"s"`
			P  float64   `json:"p"`
			C  string    `json:"c"`
			At time.Time `json:"at"`
			V  *float64  `json:"v"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return rtquote.Batch{}, err
	}
	b := rtquote.Batch{Stamp: doc.Stamp, Closed: doc.Closed}
	for _, r := range doc.Rows {
		w, ok := want[r.S]
		ticker := w.Ticker
		if !ok {
			ticker = r.S + ".XX" // what a confused source would map it to
		}
		b.Rows = append(b.Rows, rtquote.Row{Ticker: ticker, Price: r.P, Currency: r.C, TradeAt: r.At, Volume: r.V})
	}
	return b, nil
}

// blockerRT is fakeRT that reads a 502 page as a firewall's refusal.
type blockerRT struct{ fakeRT }

func (blockerRT) Blocked(status int, body []byte) (string, bool) {
	return "firewall page", status == http.StatusBadGateway && bytes.Contains(body, []byte("SECURITY"))
}

// fakeRow is one listing's state at a fake source.
type fakeRow struct {
	price, vol float64
	at         time.Time // the source's trade time (TimesTrade)
	ccy        string
}

// rtFake is the fake sources' HTTP side: it records every request and
// answers from rows (or answer, when set).
type rtFake struct {
	mu     sync.Mutex
	now    func() time.Time
	reqs   []*http.Request
	at     []time.Time
	rows   map[string]fakeRow // by source symbol
	closed bool
	live   bool                              // every asked row trades at each request: its time is now, its volume grows
	answer func(*http.Request) (int, string) // overrides rows when set
}

func (f *rtFake) RoundTrip(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, req)
	f.at = append(f.at, f.now())
	status, body := 200, ""
	if f.answer != nil {
		status, body = f.answer(req)
	} else {
		type row struct {
			S  string     `json:"s"`
			P  float64    `json:"p"`
			C  string     `json:"c"`
			At *time.Time `json:"at,omitempty"`
			V  float64    `json:"v"`
		}
		doc := struct {
			Stamp  time.Time `json:"stamp"`
			Closed bool      `json:"closed"`
			Rows   []row     `json:"rows"`
		}{Stamp: f.now(), Closed: f.closed}
		for _, s := range strings.Split(req.URL.Query().Get("s"), ",") {
			if r, ok := f.rows[s]; ok {
				if f.live {
					r.at, r.vol = f.now(), r.vol+1
					f.rows[s] = r
				}
				rw := row{S: s, P: r.price, C: cmpStr(r.ccy, "KRW"), V: r.vol}
				if !r.at.IsZero() {
					at := r.at
					rw.At = &at
				}
				doc.Rows = append(doc.Rows, rw)
			}
		}
		b, _ := json.Marshal(doc)
		body = string(b)
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}, Request: req}, nil
}

func cmpStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (f *rtFake) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

func (f *rtFake) set(sym string, r fakeRow) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rows == nil {
		f.rows = map[string]fakeRow{}
	}
	f.rows[sym] = r
}

// trade moves sym's fake price and volume (a new trade at the clock's now).
func (f *rtFake) trade(sym string, price float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.rows[sym]
	r.price, r.vol, r.at = price, r.vol+100, f.now()
	f.rows[sym] = r
}

// krxInfo is a fake KRX source with Naver's schedule.
func krxInfo(times string) rtquote.Info {
	in := rtquote.Naver{}.Info()
	in.Name, in.Times = "fake", times
	return in
}

// noYahoo is a Yahoo source that serves nothing, so tests set quotes by hand.
type noYahoo struct{}

func (noYahoo) Chart(context.Context, string, string, string) (yahoo.Chart, error) {
	return yahoo.Chart{}, &yahoo.UnavailableError{Reason: "no"}
}
func (noYahoo) Quotes(context.Context, []string) (map[string]yahoo.Quote, error) { return nil, nil }
func (noYahoo) Quote(context.Context, string) (yahoo.Quote, error) {
	return yahoo.Quote{}, &yahoo.UnavailableError{Reason: "no"}
}
func (noYahoo) FX(_ context.Context, ccy string) (yahoo.Rate, error) {
	return yahoo.Rate{Ccy: ccy, USD: 0.0007, Symbol: ccy + "USD=X"}, nil
}
func (noYahoo) FXHistory(context.Context, string, string) ([]yahoo.Point, error) { return nil, nil }

// rtClient is a market client with adapters on a fake HTTP side, at *now.
func rtClient(t *testing.T, now *time.Time, adapters ...rtquote.Adapter) (*Client, *rtFake) {
	t.Helper()
	c := testMarketClient("")
	c.Yahoo = noYahoo{}
	c.Now = func() time.Time { return *now }
	fake := &rtFake{now: c.Now}
	c.RealtimeHTTP = &http.Client{Transport: fake}
	c.UseRealtime(adapters, "")
	c.feeds.fx = map[string]feedFX{"KRW": {r: yahoo.Rate{Ccy: "KRW", USD: 0.0007, Symbol: "KRWUSD=X"}, at: *now}}
	return c, fake
}

// step runs one scheduler step at the client's clock and waits for its polls.
func step(c *Client) int { return c.rtStep(context.Background(), true) }

// src is venue's chain source i.
func src(c *Client, venue string, i int) *rtSource {
	c.feeds.mu.Lock()
	defer c.feeds.mu.Unlock()
	return c.feeds.rtVenue(venue).chain[i]
}

// yahooQuote holds a Yahoo quote for a's row by hand (read at read).
func yahooQuote(c *Client, a AssetInfo, price float64, trade, read time.Time) {
	c.feeds.mu.Lock()
	defer c.feeds.mu.Unlock()
	if c.feeds.quotes == nil {
		c.feeds.quotes = map[string]feedQuote{}
	}
	q := yahoo.Quote{Symbol: a.Pair, Price: price, Time: trade, Currency: a.Listing.LocalCurrency, Via: "spark"}
	c.feeds.quotes[a.Symbol] = newFeedQuote(a, q, read)
}

func TestRealtimeRequestPolicy(t *testing.T) {
	good := func() *http.Request {
		r, _ := http.NewRequest("GET", "https://polling.finance.naver.com/api/x", nil)
		r.Header.Set("Accept", "application/json")
		r.Header.Set("Content-Type", "application/json")
		return r
	}
	r := good()
	if err := rtPrepare(r, "https://polling.finance.naver.com"); err != nil || r.Header.Get("User-Agent") != rtquote.UserAgent || len(r.Header) != 3 {
		t.Fatalf("%v %v", err, r.Header)
	}
	if rtquote.UserAgent != "Foretrade/1.0 (+https://github.com/vincenttanjaya/Foretrade)" {
		t.Fatal(rtquote.UserAgent)
	}
	for _, h := range []string{"User-Agent", "Referer", "Cookie", "Origin", "Sec-Fetch-Mode", "X-Requested-With", "Authorization"} {
		r := good()
		r.Header.Set(h, "x")
		if err := rtPrepare(r, "https://polling.finance.naver.com"); err == nil {
			t.Errorf("%s allowed", h)
		}
	}
	if err := rtPrepare(good(), "https://example.com"); err == nil {
		t.Error("another host allowed")
	}
	if err := rtPrepare(good(), "http://polling.finance.naver.com"); err == nil {
		t.Error("another scheme allowed")
	}
}

// Refusals pause a source 30 minutes (doubling), failures degrade it after
// three in a row (1 minute, doubling); a Blocker reads a 502 page as a
// refusal; the venue serves Yahoo meanwhile.
func TestRealtimeRefusalAndFailures(t *testing.T) {
	now := kst(10, 0, 0)
	c, fake := rtClient(t, &now, blockerRT{fakeRT{krxInfo(rtquote.TimesTrade)}})
	status := 403
	fake.answer = func(*http.Request) (int, string) { return status, "denied" }
	if step(c) != 1 || src(c, "KRX", 0).state != rtRefused || src(c, "KRX", 0).refusals != 1 {
		t.Fatalf("403: %+v", src(c, "KRX", 0))
	}
	if s := src(c, "KRX", 0); !s.paused.Equal(now.Add(30*time.Minute)) || s.refusal.Status != 403 || s.refusal.Reason != "HTTP 403" {
		t.Fatalf("pause %v refusal %+v", s.paused, s.refusal)
	}
	if !strings.HasPrefix(c.QuoteSources(), "krx=yahoo fallback ") {
		t.Fatal(c.QuoteSources())
	}
	now = now.Add(29 * time.Minute)
	if step(c) != 0 {
		t.Fatal("polled while refused")
	}
	now = now.Add(time.Minute)
	status = 429
	if step(c) != 1 || !src(c, "KRX", 0).paused.Equal(now.Add(time.Hour)) {
		t.Fatalf("second refusal pause %v", src(c, "KRX", 0).paused)
	}
	now = now.Add(time.Hour)
	status = 502
	fake.answer = func(*http.Request) (int, string) {
		return status, "FOR SECURITY REASONS, THIS PAGE CAN NOT BE ACCESSED"
	}
	if step(c) != 1 || src(c, "KRX", 0).refusals != 3 || src(c, "KRX", 0).refusal.Reason != "firewall page" {
		t.Fatalf("blocker: %+v", src(c, "KRX", 0).refusal)
	}

	// failures: three in a row degrade it, then backoff doubles
	now = kst(10, 0, 0).Add(24 * time.Hour) // Thursday
	c, fake = rtClient(t, &now, fakeRT{krxInfo(rtquote.TimesTrade)})
	fake.answer = func(*http.Request) (int, string) { return 502, "bad gateway" }
	for i := range 3 {
		if step(c) != 1 {
			t.Fatalf("poll %d not made", i)
		}
		now = now.Add(10 * time.Second)
	}
	s := src(c, "KRX", 0)
	if s.state != rtDegraded || !strings.Contains(s.reason, "3 failed reads") || !s.paused.Equal(now.Add(-10*time.Second).Add(time.Minute)) {
		t.Fatalf("after 3 failures: %s %q paused %v", s.state, s.reason, s.paused)
	}
	if !strings.HasPrefix(c.QuoteSources(), "krx=yahoo degraded ") {
		t.Fatal(c.QuoteSources())
	}
	fake.answer = func(*http.Request) (int, string) { return 200, "<html>not json</html>" }
	for range 3 {
		now = now.Add(time.Minute)
		step(c)
	}
	if s := src(c, "KRX", 0); s.state != rtDegraded || s.backoff != 4*time.Minute {
		t.Fatalf("backoff %v (want 4m after two degrades)", s.backoff)
	}
}

// The per-row guards reject what they must and keep the rest of the batch.
func TestRealtimeRowGuards(t *testing.T) {
	now := kst(10, 0, 0)
	c, fake := rtClient(t, &now, fakeRT{krxInfo(rtquote.TimesTrade)})
	naverA, kia := named(t, "NAVER"), named(t, "KIA")
	yahooQuote(c, kia, 100_000, now.Add(-20*time.Minute), now.Add(-time.Minute)) // Yahoo's last good price for Kia
	fake.set("035420", fakeRow{price: 250_000, at: now.Add(-time.Second)})       // NAVER: fine
	fake.set("000270", fakeRow{price: 125_000, at: now.Add(-time.Second)})       // Kia: 25% from Yahoo's
	fake.set("373220", fakeRow{price: 400_000, at: now.Add(time.Minute)})        // LGES: a future trade
	fake.set("207940", fakeRow{price: 0, at: now})                               // a bad price
	fake.set("068270", fakeRow{price: 180_000, at: now, ccy: "USD"})             // the wrong currency
	fake.set("999999", fakeRow{price: 1, at: now})                               // never asked: not in the request
	fake.answer = nil
	if step(c) != 1 {
		t.Fatal("no poll")
	}
	s := src(c, "KRX", 0)
	if r := s.rows["035420.KS"]; r == nil || r.price != 250_000 || r.basis != "trade" {
		t.Fatalf("NAVER %+v", r)
	}
	reasons := map[string]string{}
	for _, r := range s.recent {
		reasons[r.Ticker] = r.Reason
	}
	for ticker, frag := range map[string]string{"000270.KS": "from the last good", "373220.KS": "future", "207940.KS": "bad parse",
		"068270.KS": "currency USD"} {
		if !strings.Contains(reasons[ticker], frag) {
			t.Errorf("%s: %q, want %q", ticker, reasons[ticker], frag)
		}
		if s.rows[ticker] != nil {
			t.Errorf("%s kept", ticker)
		}
	}
	if s.day.rejected != 4 || s.day.rows < 1 {
		t.Fatalf("today %+v", s.day)
	}
	// a symbol the source maps to a ticker not asked for is rejected too
	fake.answer = func(*http.Request) (int, string) {
		return 200, fmt.Sprintf(`{"rows":[{"s":"035420","p":251000,"c":"KRW","at":%q},{"s":"123456","p":5,"c":"KRW","at":%q}]}`,
			now.Format(time.RFC3339), now.Format(time.RFC3339))
	}
	now = now.Add(10 * time.Second)
	step(c)
	if last := s.recent[len(s.recent)-1]; last.Ticker != "123456.XX" || !strings.Contains(last.Reason, "mismatched symbol") {
		t.Fatalf("mismatch %+v", last)
	}
	if s.rows["035420.KS"].price != 251_000 || naverA.Pair != "035420.KS" {
		t.Fatal("the good row of a batch with a bad one was not kept")
	}
}

// A source with no trade time dates each price by the first read that
// showed its current volume: the first read is a baseline, a moved volume
// dates the row at the answer's stamp, an unmoved one keeps its date, and a
// read after the close dates at most at the close.
func TestRealtimeVolumeClock(t *testing.T) {
	now := kst(10, 0, 0)
	c, fake := rtClient(t, &now, fakeRT{krxInfo(rtquote.TimesNone)})
	fake.set("035420", fakeRow{price: 250_000, vol: 1000})
	step(c)
	s := src(c, "KRX", 0)
	if r := s.rows["035420.KS"]; r == nil || !r.trade.IsZero() || r.basis != "volume" {
		t.Fatalf("baseline %+v", r)
	}
	if _, ok := c.ListingQuotes([]string{"035420.KS"})["035420.KS"]; ok {
		t.Fatal("a baseline row served")
	}
	now = now.Add(10 * time.Second)
	fake.set("035420", fakeRow{price: 250_500, vol: 1100})
	step(c)
	if r := s.rows["035420.KS"]; !r.trade.Equal(now) || r.price != 250_500 {
		t.Fatalf("moved %+v", r)
	}
	dated := now
	now = now.Add(10 * time.Second)
	step(c)
	if r := s.rows["035420.KS"]; !r.trade.Equal(dated) || !r.read.Equal(now) {
		t.Fatalf("unmoved %+v", r)
	}
	// the closing call prints at 15:30; the settled read at 15:32 dates it 15:30
	now = kst(15, 32, 0)
	fake.set("035420", fakeRow{price: 251_000, vol: 5000})
	if step(c) != 1 {
		t.Fatal("no settled read")
	}
	if r := s.rows["035420.KS"]; !r.trade.Equal(kst(15, 30, 0)) || r.price != 251_000 {
		t.Fatalf("settled %+v", r)
	}
	// a snapshot source with no volume uses the snapshot time
	now = kst(10, 0, 0).Add(24 * time.Hour)
	c, fake = rtClient(t, &now, fakeRT{krxInfo(rtquote.TimesSnapshot)})
	fake.answer = func(*http.Request) (int, string) {
		return 200, fmt.Sprintf(`{"stamp":%q,"rows":[{"s":"035420","p":250000,"c":"KRW"}]}`, now.Add(-90*time.Second).Format(time.RFC3339))
	}
	step(c)
	if r := src(c, "KRX", 0).rows["035420.KS"]; r == nil || !r.trade.Equal(now.Add(-90*time.Second)) || r.basis != "snapshot" {
		t.Fatalf("snapshot %+v", r)
	}
	// rows without a volume cannot be dated by one
	now = kst(10, 0, 0).Add(48 * time.Hour)
	c, fake = rtClient(t, &now, fakeRT{krxInfo(rtquote.TimesNone)})
	fake.answer = func(*http.Request) (int, string) { return 200, `{"rows":[{"s":"035420","p":250000,"c":"KRW"}]}` }
	step(c)
	if s := src(c, "KRX", 0); s.rows["035420.KS"] != nil || !strings.Contains(s.recent[0].Reason, "no volume") || s.fails != 1 {
		t.Fatalf("no volume: %+v", s.recent)
	}
}

// One request per poll at the interval, never under the floor, none when
// closed (night, weekend, holiday, a lunch break), exactly one settled read
// after each final close; a source that says closed is asked again 5
// minutes later.
func TestRealtimeSchedule(t *testing.T) {
	now := kst(8, 59, 0)
	c, fake := rtClient(t, &now, fakeRT{krxInfo(rtquote.TimesTrade)})
	fake.live = true
	for _, s := range []string{"000660", "005930", "009150"} {
		fake.set(s, fakeRow{price: 1000})
	}
	day := map[string]int{}
	for end := kst(23, 59, 59); now.Before(end); now = now.Add(time.Second) {
		n := fake.count()
		step(c)
		if fake.count() > n {
			l := now.In(zone("Asia/Seoul"))
			day[fmt.Sprintf("%02d", l.Hour())]++
		}
	}
	// the boot at 08:59 makes the previous close's settled read; 09:00:00 to
	// 15:29:50 every 10 s is 2,340 polls; plus the settled read at 15:32
	total := 0
	for _, n := range day {
		total += n
	}
	if total != 2342 || day["08"] != 1 || day["15"] != 180+1 || day["16"] != 0 || src(c, "KRX", 0).state != rtOK {
		t.Fatalf("day %v (%d), %s %q", day, total, src(c, "KRX", 0).state, src(c, "KRX", 0).reason)
	}
	for i := 2; i < len(fake.at)-1; i++ {
		if d := fake.at[i].Sub(fake.at[i-1]); d != 10*time.Second {
			t.Fatalf("poll %d %v after the last", i, d)
		}
	}
	if last := fake.at[len(fake.at)-1]; !last.Equal(kst(15, 32, 0)) {
		t.Fatalf("settled read at %v", last)
	}
	// a weekend and a holiday (Hangul Day, 2026-10-09) poll nothing, a boot
	// after a close makes the one settled read only
	for _, from := range []time.Time{time.Date(2026, 10, 17, 0, 0, 0, 0, zone("Asia/Seoul")), time.Date(2026, 10, 9, 0, 0, 0, 0, zone("Asia/Seoul"))} {
		now = from.UTC()
		c, fake = rtClient(t, &now, fakeRT{krxInfo(rtquote.TimesTrade)})
		fake.live = true
		fake.set("005930", fakeRow{price: 1000})
		for end := now.Add(24 * time.Hour); now.Before(end); now = now.Add(30 * time.Second) {
			step(c)
		}
		if fake.count() != 1 || fake.at[0].Sub(from) > time.Minute {
			t.Fatalf("%s: %d requests %v (want the previous close's settled read at boot only)", from.Format(time.DateOnly), fake.count(), fake.at)
		}
	}
	// a lunch break (HKEX 12:00-13:00 HKT) is not polled and makes no settled read
	hk := func(h, m int) time.Time { return time.Date(2026, 10, 14, h, m, 0, 0, zone("Asia/Hong_Kong")).UTC() }
	info := krxInfo(rtquote.TimesTrade)
	info.Venue, info.Switch, info.Quiet, info.Canaries = "HKEX", "hkex", nil, []string{"0700.HK"}
	now = hk(11, 59)
	c, fake = rtClient(t, &now, fakeRT{info})
	fake.live = true
	fake.set("0700", fakeRow{price: 400, ccy: "HKD"})
	for end := hk(13, 1); now.Before(end); now = now.Add(time.Second) {
		step(c)
	}
	for _, at := range fake.at {
		if !at.Before(hk(12, 0)) && at.Before(hk(13, 0)) {
			t.Fatalf("polled in the lunch break at %v", at)
		}
	}
	if fake.count() != 6+6 {
		t.Fatalf("%d polls around lunch", fake.count())
	}
	// the floor holds even when Every is shorter
	info.Every, info.Floor = time.Second, 5*time.Second
	now = hk(10, 0)
	c, fake = rtClient(t, &now, fakeRT{info})
	fake.live = true
	fake.set("0700", fakeRow{price: 400, ccy: "HKD"})
	for range 60 {
		step(c)
		now = now.Add(time.Second)
	}
	if fake.count() != 12 {
		t.Fatalf("%d polls in a minute at a 5 s floor", fake.count())
	}
	// the source says closed (a holiday the calendar does not know): 5 minutes
	now = kst(10, 0, 0)
	c, fake = rtClient(t, &now, fakeRT{krxInfo(rtquote.TimesTrade)})
	fake.closed = true
	fake.set("035420", fakeRow{price: 250_000, at: kst(15, 30, 0).Add(-24 * time.Hour)})
	for range 599 {
		step(c)
		now = now.Add(time.Second)
	}
	if fake.count() != 2 || !strings.HasPrefix(c.QuoteSources(), "krx=yahoo closed") {
		t.Fatalf("%d polls in 10 minutes while the source says closed; %s", fake.count(), c.QuoteSources())
	}
}

// The health check: fresh canaries make a source ok; canaries that stop
// trading, or fall behind Yahoo, degrade it; the venue serves Yahoo while it
// is paused, and the next probe brings it back.
func TestRealtimeHealth(t *testing.T) {
	now := kst(10, 0, 0)
	c, fake := rtClient(t, &now, fakeRT{krxInfo(rtquote.TimesTrade)})
	canaries := []string{"000660", "005930", "009150"}
	for _, s := range canaries {
		fake.set(s, fakeRow{price: 1000, at: now})
	}
	sem := named(t, "SEMCO") // 009150.KS, a canary and a named feed
	step(c)
	if s := src(c, "KRX", 0); s.state != rtOK {
		t.Fatalf("fresh canaries: %s %q", s.state, s.reason)
	}
	c.feeds.mu.Lock()
	sq, ok := c.feeds.served(sem, now)
	c.feeds.mu.Unlock()
	if !ok || sq.source != "fake" || sq.class != ClassRealtime {
		t.Fatalf("served %+v", sq)
	}
	if got := c.QuoteSources(); got != "krx=fake ok 0s; hkex=yahoo closed -; hose=yahoo fallback -; sgx=yahoo fallback -; twse=yahoo fallback -" {
		t.Fatal(got)
	}
	// the canaries stop trading
	for range 9 {
		now = now.Add(10 * time.Second)
		step(c)
	}
	if s := src(c, "KRX", 0); s.state != rtOK {
		t.Fatalf("90 s without a trade is still within MaxLag: %s", s.state)
	}
	now = now.Add(10 * time.Second)
	step(c)
	s := src(c, "KRX", 0)
	if s.state != rtDegraded || !strings.Contains(s.reason, "canaries stale") {
		t.Fatalf("stale: %s %q", s.state, s.reason)
	}
	c.feeds.mu.Lock()
	sq, ok = c.feeds.served(sem, now)
	c.feeds.mu.Unlock()
	if ok && sq.rt != nil {
		t.Fatal("a degraded source still serves")
	}
	if got := c.QuoteSources(); !strings.HasPrefix(got, "krx=yahoo degraded -") {
		t.Fatal(got)
	}
	// paused a minute; the probe after it finds fresh trades
	n := fake.count()
	now = now.Add(50 * time.Second)
	step(c)
	if fake.count() != n {
		t.Fatal("polled while paused")
	}
	now = now.Add(10 * time.Second)
	for _, s := range canaries {
		fake.trade(s, 1001)
	}
	step(c)
	if s := src(c, "KRX", 0); s.state != rtOK {
		t.Fatalf("probe: %s %q", s.state, s.reason)
	}
	// Yahoo holds a newer trade of a canary: the source fell behind
	yahooQuote(c, sem, 1000, now.Add(5*time.Minute), now)
	now = now.Add(10 * time.Second)
	step(c)
	if s := src(c, "KRX", 0); s.state != rtDegraded || !strings.Contains(s.reason, "behind Yahoo on 009150.KS") {
		t.Fatalf("behind: %s %q", s.state, s.reason)
	}
	// no health check in the closing call, nor in the first 2 minutes after the open
	now = kst(15, 21, 0)
	c, fake = rtClient(t, &now, fakeRT{krxInfo(rtquote.TimesTrade)})
	for _, s := range canaries {
		fake.set(s, fakeRow{price: 1000, at: now.Add(-time.Hour)})
	}
	step(c)
	now = kst(9, 1, 0).Add(24 * time.Hour)
	step(c)
	if s := src(c, "KRX", 0); s.state != rtIdle {
		t.Fatalf("checked in a quiet window or the open's grace: %s", s.state)
	}
	// a volume-clocked source with no canary trade after MaxLag is degraded
	now = kst(10, 0, 0).Add(2 * 24 * time.Hour)
	c, fake = rtClient(t, &now, fakeRT{krxInfo(rtquote.TimesNone)})
	for _, s := range canaries {
		fake.set(s, fakeRow{price: 1000, vol: 5})
	}
	for range 10 {
		step(c)
		now = now.Add(10 * time.Second)
	}
	if s := src(c, "KRX", 0); s.state != rtDegraded || !strings.Contains(s.reason, "no canary trade") {
		t.Fatalf("frozen volumes: %s %q", s.state, s.reason)
	}
}

// A chain (TWSE's MIS then Yahoo Taiwan): the second serves while the first
// is refused (fallback) or degraded, and the first takes over again once it
// passes.
func TestRealtimeChain(t *testing.T) {
	tw := func(h, m, s int) time.Time { return time.Date(2026, 10, 14, h, m, s, 0, zone("Asia/Taipei")).UTC() }
	first, second := krxInfo(rtquote.TimesTrade), krxInfo(rtquote.TimesTrade)
	first.Name, first.Venue, first.Switch, first.Canaries, first.Quiet = "one", "TWSE", "twse", []string{"2454.TW"}, nil
	second.Name, second.Venue, second.Switch, second.Canaries, second.Quiet = "two", "TWSE", "twse", []string{"2454.TW"}, nil
	now := tw(10, 0, 0)
	c, fake := rtClient(t, &now, fakeRT{first}, fakeRT{second})
	c.feeds.fx["TWD"] = feedFX{r: yahoo.Rate{Ccy: "TWD", USD: 0.03, Symbol: "TWDUSD=X"}, at: now}
	calls := map[string]int{}
	refuse := true
	fake.answer = func(r *http.Request) (int, string) {
		calls[r.URL.Host]++
		if refuse && len(calls) == 1 && calls[r.URL.Host] == 1 {
			return 403, "no"
		}
		return 200, fmt.Sprintf(`{"rows":[{"s":"2454","p":1500,"c":"TWD","at":%q}]}`, now.Format(time.RFC3339))
	}
	step(c) // one: refused
	now = now.Add(10 * time.Second)
	step(c) // two: serves
	if got := c.QuoteSources(); !strings.Contains(got, "twse=two fallback 0s") {
		t.Fatal(got)
	}
	// one's pause ends: its probe passes and it serves again
	now = now.Add(30 * time.Minute)
	refuse = false
	step(c)
	if got := c.QuoteSources(); !strings.Contains(got, "twse=one ok 0s") || src(c, "TWSE", 0).state != rtOK {
		t.Fatal(got)
	}
	// one degrades (three failures): two serves, reported degraded
	fake.answer = func(r *http.Request) (int, string) {
		if strings.Contains(r.URL.String(), "2454") && len(calls) > 0 && src(c, "TWSE", 0).state != rtDegraded {
			return 500, "oops"
		}
		return 200, fmt.Sprintf(`{"rows":[{"s":"2454","p":1500,"c":"TWD","at":%q}]}`, now.Format(time.RFC3339))
	}
	for range 4 {
		now = now.Add(10 * time.Second)
		step(c)
	}
	if got := c.QuoteSources(); !strings.Contains(got, "twse=two degraded 0s") {
		t.Fatal(got, src(c, "TWSE", 0).state)
	}
}

// The freshest valid quote prices the named feed, the listed row and the
// watch row of a listing alike, with its own labels; Yahoo wins when it
// knows a newer trade, and when the real-time row was not read lately.
func TestRealtimeSelection(t *testing.T) {
	now := kst(10, 0, 0)
	c, fake := rtClient(t, &now, fakeRT{krxInfo(rtquote.TimesTrade)})
	if c.Watch([]WatchRow{{Ticker: "005930.KS", Venue: KRX, Currency: "KRW"}}) != 1 {
		t.Fatal("watch row refused")
	}
	navr, sk := named(t, "NAVER"), listed(t, "402340.KS")
	yahooQuote(c, navr, 240_000, kst(15, 30, 0).Add(-24*time.Hour), now.Add(-30*time.Second)) // yesterday's close
	yahooQuote(c, sk, 240_000, now.Add(-20*time.Minute), now.Add(-30*time.Second))            // today, 20 minutes behind
	c.feeds.mu.Lock()
	q := c.feeds.quotes[sk.Symbol]
	prev, hi, lo := 238_000.0, 260_000.0, 239_000.0
	q.q.Meta.ChartPreviousClose, q.q.Meta.DayHigh, q.q.Meta.DayLow = &prev, &hi, &lo
	c.feeds.quotes[sk.Symbol] = q
	c.feeds.mu.Unlock()
	for _, s := range []string{"000660", "005930", "009150", "035420", "402340"} {
		fake.set(s, fakeRow{price: 250_000, at: now.Add(-2 * time.Second), vol: 1e6})
	}
	step(c)
	c.feeds.mu.Lock()
	as, ok := c.feeds.asset(navr, now)
	listedRow, ok2 := c.feeds.asset(sk, now)
	c.feeds.mu.Unlock()
	if !ok || !ok2 {
		t.Fatal("no assets")
	}
	qf := as.QuoteFeed
	if as.Price != 250_000*0.0007 || qf.Source != "fake" || qf.Class != ClassRealtime || qf.TimeBasis != "trade" ||
		qf.DelaySeconds != 2 || qf.Label != "real-time · 00:59:58 UTC" || !qf.LastTradeAt.Equal(now.Add(-2*time.Second)) ||
		!qf.ReadAt.Equal(now) || qf.LocalPrice != 250_000 || listedRow.QuoteFeed.Source != "fake" {
		t.Fatalf("named %+v %+v; listed %+v", as, qf, listedRow.QuoteFeed)
	}
	if qf.PreviousClose == nil || *qf.PreviousClose != 240_000 || as.Change24hPct != 4.167 || as.Range24hSource != "unavailable" {
		t.Fatalf("Yahoo's earlier-day price is the previous close: %v %v %s", qf.PreviousClose, as.Change24hPct, as.Range24hSource)
	}
	if lf := listedRow.QuoteFeed; *lf.PreviousClose != 238_000 || listedRow.High24h != 260_000*0.0007 || listedRow.Low24h != 239_000*0.0007 ||
		listedRow.Volume24hUsd != 1e6*250_000*0.0007 {
		t.Fatalf("same-day Yahoo meta: %+v %+v", listedRow, lf)
	}
	lq := c.ListingQuotes([]string{"035420.KS", "005930.KS", "402340.KS"})
	if w := lq["005930.KS"]; w.Source != "fake" || w.Tier != "screener" || w.Label != "real-time · 00:59:58 UTC" ||
		w.Refresh != 10*time.Second || *w.DelayMinutes != 0.03 || w.DelayBasis != "measured" || w.Stale ||
		!w.StaleAt.Equal(now.Add(time.Minute+quoteGrace)) || w.Class != ClassRealtime {
		t.Fatalf("watch row %+v", w)
	}
	if w := lq["035420.KS"]; w.Tier != "trade" || w.Source != "fake" || w.Price != 250_000 {
		t.Fatalf("trade row %+v", w)
	}
	// Yahoo knows a newer trade: it wins
	yahooQuote(c, navr, 241_000, now.Add(time.Second), now)
	c.feeds.mu.Lock()
	as, _ = c.feeds.asset(navr, now)
	c.feeds.mu.Unlock()
	if as.QuoteFeed.Source != "yahoo" || as.QuoteFeed.Class != ClassDelayed || !strings.HasPrefix(as.QuoteFeed.Label, "delayed · last trade ") {
		t.Fatalf("newer Yahoo lost: %+v", as.QuoteFeed)
	}
	// not read for over a minute while open: Yahoo again
	now = now.Add(61 * time.Second)
	c.feeds.mu.Lock()
	sq, _ := c.feeds.served(sk, now)
	c.feeds.mu.Unlock()
	if sq.rt != nil {
		t.Fatal("an unread real-time row still serves")
	}
	if got := c.ListingQuotes([]string{"402340.KS"})["402340.KS"]; got.Source != "yahoo" || *got.DelayMinutes != 20 {
		t.Fatalf("Yahoo listing quote %+v", got)
	}
}

// A fill re-reads a quote over a minute old from the real-time source first
// (one request), and from Yahoo only what that did not cover; the floor
// stops a second real-time request.
func TestRealtimeFillReadsSourceFirst(t *testing.T) {
	now := kst(10, 0, 0)
	r := &recorder{now: func() time.Time { return now }}
	c, fake := rtClient(t, &now, fakeRT{krxInfo(rtquote.TimesTrade)})
	c.Yahoo = r
	navr := named(t, "NAVER")
	fake.live = true
	for _, s := range []string{"000660", "005930", "009150", "035420"} {
		fake.set(s, fakeRow{price: 250_000})
	}
	now = now.Add(-10 * time.Second) // the poller's last read
	step(c)
	now = now.Add(80 * time.Second) // then it stalled: the quote is 70 s old
	got, err := c.FillAssets(context.Background(), []string{"NAVER", "DBS"})
	if err != nil {
		t.Fatal(err)
	}
	if fake.count() != 2 || got["NAVER"].QuoteFeed == nil || got["NAVER"].QuoteFeed.Source != "fake" || !got["NAVER"].QuoteFeed.ReadAt.Equal(now) {
		t.Fatalf("%d requests, %+v", fake.count(), got["NAVER"])
	}
	for _, call := range r.quoteCalls() {
		if slices.Contains(call, navr.Pair) {
			t.Fatalf("Yahoo read %v too", call)
		}
	}
	// fresh: nothing is read again
	now = now.Add(30 * time.Second)
	c.FillAssets(context.Background(), []string{"NAVER"})
	if fake.count() != 2 {
		t.Fatal("re-read a fresh quote")
	}
	// over a minute old again: the source again
	now = now.Add(40 * time.Second)
	fake.set("035420", fakeRow{price: 251_000})
	got, _ = c.FillAssets(context.Background(), []string{"NAVER"})
	if fake.count() != 3 || got["NAVER"].QuoteFeed.LocalPrice != 251_000 {
		t.Fatalf("%d requests, %+v", fake.count(), got["NAVER"].QuoteFeed)
	}
	// within the floor of the last request: Yahoo, not a second real-time request
	c.feeds.mu.Lock()
	c.feeds.rtVenue(KRX).chain[0].rows["035420.KS"].read = now.Add(-2 * time.Minute)
	c.feeds.mu.Unlock()
	now = now.Add(2 * time.Second)
	n := r.count()
	c.FillAssets(context.Background(), []string{"NAVER"})
	if fake.count() != 3 || r.count() != n+1 {
		t.Fatalf("under the floor: %d real-time, %d Yahoo calls", fake.count(), r.count()-n)
	}
	// closed: Yahoo, as before
	now = kst(20, 0, 0)
	n = r.count()
	c.FillAssets(context.Background(), []string{"NAVER"})
	if fake.count() != 3 || r.count() != n+1 {
		t.Fatalf("closed fill: %d real-time, %d Yahoo calls", fake.count(), r.count()-n)
	}
}

func TestQuoteLabel(t *testing.T) {
	at := time.Date(2026, 10, 14, 4, 3, 12, 0, time.UTC)
	for _, c := range []struct {
		class string
		delay float64
		want  string
	}{
		{ClassRealtime, 0, "real-time · 04:03:12 UTC"},
		{ClassRealtime, 59.9, "real-time · 04:03:12 UTC"},
		{ClassRealtime, 60, "about 1 min behind · 04:03 UTC"},
		{ClassRealtime, 150, "about 3 min behind · 04:03 UTC"},
		{ClassRealtime, 300, "about 5 min behind · 04:03 UTC"},
		{ClassRealtime, 301, "delayed · last trade 04:03 UTC"},
		{ClassNear, 41, "about 1 min behind · 04:03 UTC"},
		{ClassNear, 99, "about 2 min behind · 04:03 UTC"},
		{ClassNear, 400, "delayed · last trade 04:03 UTC"},
		{ClassDelayed, 0, "delayed · last trade 04:03 UTC"},
		{ClassDelayed, 1200, "delayed · last trade 04:03 UTC"},
	} {
		if got := QuoteLabel(c.class, c.delay, at); got != c.want || strings.Contains(got, "live") {
			t.Errorf("%s %v: %q, want %q", c.class, c.delay, got, c.want)
		}
	}
}

// The delay of a read after the close (or in a lunch break) runs to the
// session's end, not to the read; Yahoo's too.
func TestRealtimeDelayAtClose(t *testing.T) {
	now := kst(16, 0, 0)
	c, _ := rtClient(t, &now)
	navr := named(t, "NAVER")
	c.feeds.mu.Lock()
	defer c.feeds.mu.Unlock()
	if d := c.feeds.delaySeconds(navr, yahoo.Meta{}, kst(16, 0, 0), kst(15, 29, 58)); d != 2 {
		t.Fatalf("after the close: %v", d)
	}
	if d := c.feeds.delaySeconds(navr, yahoo.Meta{}, kst(10, 0, 0), kst(9, 40, 0)); d != 1200 {
		t.Fatalf("open: %v", d)
	}
	if d := c.feeds.delaySeconds(navr, yahoo.Meta{}, kst(10, 0, 0), kst(10, 0, 5)); d != 0 {
		t.Fatalf("a slightly fast clock: %v", d)
	}
	hk := time.Date(2026, 10, 14, 12, 30, 0, 0, zone("Asia/Hong_Kong")).UTC()
	tencent := named(t, "TENCENT")
	if d := c.feeds.delaySeconds(tencent, yahoo.Meta{}, hk, hk.Add(-31*time.Minute)); d != 60 {
		t.Fatalf("lunch: %v", d)
	}
	bbca := named(t, "BBCA") // no calendar: Yahoo's period
	start, end := kst(10, 0, 0), kst(14, 0, 0)
	if d := c.feeds.delaySeconds(bbca, yahoo.Meta{RegularStart: &start, RegularEnd: &end}, kst(15, 0, 0), kst(13, 50, 0)); d != 600 {
		t.Fatalf("Yahoo period: %v", d)
	}
}

// /healthz's header names every venue, in order, and the detail route
// carries each chain's state.
func TestQuoteSourcesDetail(t *testing.T) {
	now := kst(10, 0, 0)
	c, fake := rtClient(t, &now, rtquote.Naver{})
	if got := c.QuoteSources(); got != "krx=yahoo degraded -; hkex=yahoo closed -; hose=yahoo fallback -; sgx=yahoo fallback -; twse=yahoo fallback -" {
		t.Fatalf("before the first poll: %q", got)
	}
	vol := 1000.0
	naverBody := func() string {
		vol += 10
		var rows []string
		for _, code := range []string{"000660", "005930", "009150"} {
			rows = append(rows, fmt.Sprintf(`{"itemCode":%q,"closePriceRaw":"100000","accumulatedTradingVolumeRaw":"%d",`+
				`"marketStatus":"OPEN","marketSessionType":"regularMarket","localTradedAt":%q,"stockExchangeType":{"code":"KS","delayTime":0},`+
				`"tradeStopType":{"name":"TRADING"},"currencyType":{"code":"KRW"}}`, code, int(vol), now.In(zone("Asia/Seoul")).Format(time.RFC3339Nano)))
		}
		return `{"pollingInterval":7000,"datas":[` + strings.Join(rows, ",") + `]}`
	}
	fake.answer = func(*http.Request) (int, string) { return 200, naverBody() }
	step(c)
	now = now.Add(10 * time.Second)
	step(c)
	now = now.Add(2 * time.Second)
	if got := c.QuoteSources(); !strings.HasPrefix(got, "krx=naver ok 2s; hkex=yahoo closed -; hose=yahoo fallback") {
		t.Fatalf("serving: %q", got)
	}
	if ua := fake.reqs[0].Header.Get("User-Agent"); ua != rtquote.UserAgent || fake.reqs[0].Header.Get("Referer") != "" ||
		fake.reqs[0].Header.Get("Cookie") != "" {
		t.Fatalf("headers %v", fake.reqs[0].Header)
	}
	rec := httptest.NewRecorder()
	c.handleQuoteSources(rec, httptest.NewRequest("GET", "/api/market/quote-sources", nil))
	var body struct {
		Venues []VenueSource `json:"venues"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.Venues) != 5 {
		t.Fatalf("%v %s", err, rec.Body)
	}
	krx := body.Venues[0]
	if krx.Venue != "KRX" || krx.Status != "ok" || krx.Source != "naver" || krx.Session != "open" || *krx.NewestAge != 2 ||
		len(krx.Chain) != 1 || krx.NextPollAt == nil || !krx.NextPollAt.Equal(now.Add(8*time.Second)) {
		t.Fatalf("KRX %+v", krx)
	}
	ch := krx.Chain[0]
	if ch.State != "ok" || ch.PollSeconds != 10 || ch.FloorSeconds != 5 || ch.Today.Requests != 2 || ch.Today.BusiestMinute != 2 ||
		len(ch.Canaries) != 3 || ch.Canaries[0].AgeSeconds == nil || ch.Reason != nil || ch.LastOKAt == nil {
		t.Fatalf("chain %+v", ch)
	}
	if hk := body.Venues[1]; hk.Venue != "HKEX" || hk.Status != "closed" || len(hk.Chain) != 0 || hk.Session != "closed" {
		t.Fatalf("HKEX %+v", hk)
	}
	if !strings.Contains(rec.Body.String(), `"newestTradeAgeSeconds":2`) {
		t.Fatal(rec.Body.String())
	}
	// closed after the settled read
	now = kst(16, 0, 0)
	step(c)
	if got := c.QuoteSources(); !strings.HasPrefix(got, "krx=naver closed ") {
		t.Fatalf("closed: %q", got)
	}
}

// RunRealtime steps until its context ends; without chains it returns.
func TestRunRealtime(t *testing.T) {
	now := kst(10, 0, 0)
	c, fake := rtClient(t, &now, rtquote.Naver{})
	fake.answer = func(*http.Request) (int, string) { return 200, `{"datas":[]}` }
	c.RealtimeTick = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.RunRealtime(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for fake.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if fake.count() == 0 {
		t.Fatal("no poll")
	}
	idle, _ := rtClient(t, &now)
	idle.RunRealtime(context.Background()) // returns at once
}

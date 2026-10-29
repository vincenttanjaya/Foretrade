package market

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/market/rtquote"
)

// The framework's rules the builders' venues called for: a volume clock
// across a lunch break (HOSE, SGX), before a session's first trade and
// through a closing auction's indicative prices (HOSE), a
// source that says closed (MIS and Vietcap at the open, a holiday's zeroed
// board), and a close printed past the session's end (SGX's trade-at-close,
// HKEX's closing auction).

// A volume clock across a lunch break: a volume that moved between the last
// read before the break and the first after it is dated at the break's
// start, not after it (the trades may be the morning's last). A cumulative
// volume of 0 dates nothing: the session has not traded yet.
func TestRealtimeVolumeClockBreak(t *testing.T) {
	hk := func(h, m, s int) time.Time { return time.Date(2026, 10, 14, h, m, s, 0, zone("Asia/Hong_Kong")).UTC() }
	info := krxInfo(rtquote.TimesNone)
	info.Venue, info.Switch, info.Quiet, info.Canaries = HKEX, "hkex", nil, []string{"0700.HK"}
	now := hk(11, 59, 40)
	c, fake := rtClient(t, &now, fakeRT{info})
	fake.set("0700", fakeRow{price: 400, vol: 1000, ccy: "HKD"})
	step(c) // the baseline
	now = hk(11, 59, 50)
	fake.set("0700", fakeRow{price: 401, vol: 1100, ccy: "HKD"})
	step(c)
	s := src(c, HKEX, 0)
	if r := s.rows["0700.HK"]; !r.trade.Equal(hk(11, 59, 50)) {
		t.Fatalf("before the break %+v", r)
	}
	fake.set("0700", fakeRow{price: 402, vol: 1200, ccy: "HKD"}) // a trade at 11:59:58, after the last morning read
	now = hk(13, 0, 0)
	step(c)
	if r := s.rows["0700.HK"]; !r.trade.Equal(hk(12, 0, 0)) || r.price != 402 {
		t.Fatalf("across the break %+v", r)
	}
	now = hk(13, 0, 10)
	fake.set("0700", fakeRow{price: 403, vol: 1300, ccy: "HKD"})
	step(c)
	if r := s.rows["0700.HK"]; !r.trade.Equal(hk(13, 0, 10)) {
		t.Fatalf("after the break %+v", r)
	}

	// KRX's open: yesterday's settled volume, then 0 (nothing traded yet),
	// then the open's first trades
	now = kst(15, 32, 0).Add(-24 * time.Hour)
	c, fake = rtClient(t, &now, fakeRT{krxInfo(rtquote.TimesNone)})
	fake.set("035420", fakeRow{price: 250_000, vol: 5000})
	step(c) // the settled read of the 13th: the baseline
	now = kst(9, 0, 0)
	fake.set("035420", fakeRow{price: 250_000, vol: 0})
	step(c)
	s = src(c, KRX, 0)
	s.state = rtOK // proven (the open's grace holds its health check back)
	if r := s.rows["035420.KS"]; r == nil || !r.trade.IsZero() {
		t.Fatalf("no trade yet %+v", r)
	}
	if _, ok := c.ListingQuotes([]string{"035420.KS"})["035420.KS"]; ok {
		t.Fatal("yesterday's close served as today's")
	}
	now = kst(9, 0, 10)
	fake.set("035420", fakeRow{price: 251_000, vol: 300})
	step(c)
	if r := s.rows["035420.KS"]; !r.trade.Equal(kst(9, 0, 10)) {
		t.Fatalf("the first trades %+v", r)
	}
}

// A source that says closed: right after an interval's start (yesterday's
// board may still be up) it is asked again in a minute, later in 5 minutes;
// a closed answer with no usable row (a holiday's zeroed board) is an answer,
// not a failure.
func TestRealtimeClosedAnswers(t *testing.T) {
	now := kst(9, 0, 0)
	c, fake := rtClient(t, &now, fakeRT{krxInfo(rtquote.TimesTrade)})
	fake.closed = true
	fake.set("035420", fakeRow{price: 250_000, at: kst(15, 30, 0).Add(-24 * time.Hour)})
	for end := kst(9, 10, 0); now.Before(end); now = now.Add(time.Second) {
		step(c)
	}
	var at []string
	for _, a := range fake.at {
		at = append(at, a.In(zone("Asia/Seoul")).Format("15:04"))
	}
	if got := strings.Join(at, " "); got != "09:00 09:01 09:02 09:03 09:04 09:05" {
		t.Fatalf("polls %s", got)
	}
	fake.closed, fake.live = false, true
	for _, s := range []string{"000660", "005930", "009150"} { // the canaries trade
		fake.set(s, fakeRow{price: 1000})
	}
	for end := kst(9, 11, 0); now.Before(end); now = now.Add(time.Second) {
		step(c)
	}
	if n := fake.count(); n != 6+6 {
		t.Fatalf("%d polls once open", n)
	}

	// a holiday's board: closed, every row zeroed
	now = kst(10, 0, 0)
	c, fake = rtClient(t, &now, fakeRT{krxInfo(rtquote.TimesTrade)})
	fake.answer = func(*http.Request) (int, string) {
		return 200, fmt.Sprintf(`{"closed":true,"rows":[{"s":"035420","p":0,"c":"KRW","at":%q}]}`, now.Format(time.RFC3339))
	}
	for end := kst(10, 29, 59); now.Before(end); now = now.Add(time.Second) {
		step(c)
	}
	if s := src(c, KRX, 0); fake.count() != 6 || s.fails != 0 || s.state != rtIdle || s.lastOK.IsZero() {
		t.Fatalf("%d polls, %d failures, %s %q", fake.count(), s.fails, s.state, s.reason)
	}
	if got := c.QuoteSources(); !strings.HasPrefix(got, "krx=yahoo closed") {
		t.Fatal(got)
	}
}

// After the close a settled read stands against Yahoo's later print of the
// close (a closing auction's or trade-at-close's, past the session's end),
// but not against a print Yahoo has from after the source's last trade.
func TestRealtimeSettledBeatsLaterPrint(t *testing.T) {
	now := kst(15, 0, 0)
	c, fake := rtClient(t, &now, fakeRT{krxInfo(rtquote.TimesTrade)})
	fake.live = true
	for _, s := range []string{"000660", "005930", "009150"} {
		fake.set(s, fakeRow{price: 1000})
	}
	fake.set("035420", fakeRow{price: 250_000, at: now})
	step(c)
	if s := src(c, KRX, 0); s.state != rtOK {
		t.Fatal(s.state, s.reason)
	}
	navr := named(t, "NAVER")
	fake.live = false
	fake.set("035420", fakeRow{price: 251_000, at: kst(15, 30, 0)}) // the closing call's print
	now = kst(15, 32, 0)
	if step(c) != 1 {
		t.Fatal("no settled read")
	}
	now = kst(16, 0, 0)
	yahooQuote(c, navr, 251_000, kst(15, 30, 4), now) // Yahoo dates the same close 4 s later
	if qf := feedOf(t, c, "NAVER", now); qf.Source != "fake" || qf.Label != "real-time · 06:30:00 UTC" || qf.DelaySeconds != 0 {
		t.Fatalf("settled read %+v", qf)
	}
	// the source's last trade was before the close; Yahoo has one after it
	now = kst(15, 32, 0).Add(24 * time.Hour)
	c, fake = rtClient(t, &now, fakeRT{krxInfo(rtquote.TimesTrade)})
	src(c, KRX, 0).state = rtOK
	fake.set("035420", fakeRow{price: 250_500, at: kst(15, 29, 0).Add(24 * time.Hour)})
	step(c)
	yahooQuote(c, navr, 251_000, kst(15, 30, 4).Add(24*time.Hour), now)
	if qf := feedOf(t, c, "NAVER", now); qf.Source != "yahoo" || qf.LocalPrice != 251_000 {
		t.Fatalf("Yahoo's later print %+v", qf)
	}
}

// Under the volume clock a price that moves while the volume stands still
// (a closing auction's indicative price) is no trade: the row keeps the
// price and time of the read that last moved its volume, and takes the new
// price once the volume moves (the auction's match).
func TestRealtimeVolumeClockAuction(t *testing.T) {
	now := kst(15, 19, 50)
	c, fake := rtClient(t, &now, fakeRT{krxInfo(rtquote.TimesNone)})
	fake.set("035420", fakeRow{price: 250_000, vol: 1000})
	step(c) // the baseline
	now = kst(15, 20, 0)
	fake.set("035420", fakeRow{price: 250_500, vol: 1100})
	step(c)
	s := src(c, KRX, 0)
	for _, indicative := range []float64{251_000, 249_000} {
		now = now.Add(10 * time.Second)
		fake.set("035420", fakeRow{price: indicative, vol: 1100})
		step(c)
		if r := s.rows["035420.KS"]; r.price != 250_500 || !r.trade.Equal(kst(15, 20, 0)) || !r.read.Equal(now) {
			t.Fatalf("indicative %v: %+v", indicative, r)
		}
	}
	now = kst(15, 32, 0)
	fake.set("035420", fakeRow{price: 251_500, vol: 9000}) // the 15:30 match
	step(c)
	if r := s.rows["035420.KS"]; r.price != 251_500 || !r.trade.Equal(kst(15, 30, 0)) {
		t.Fatalf("the match %+v", r)
	}
}

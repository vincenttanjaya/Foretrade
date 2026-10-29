package market

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/market/rtquote"
)

// An older answer never replaces a newer quote: a row whose trade time is
// before the one held (a staler copy of the answer, from another edge's
// cache or a lagging node) is rejected and the held row keeps pricing the
// listing, fills included; a newer one replaces it again.
func TestRealtimeOlderAnswerKeepsNewer(t *testing.T) {
	now := kst(10, 0, 0)
	c, fake := rtClient(t, &now, fakeRT{krxInfo(rtquote.TimesTrade)})
	navr := named(t, "NAVER")
	for _, s := range []string{"000660", "005930", "009150"} {
		fake.set(s, fakeRow{price: 100_000, at: now.Add(-time.Second), vol: 1e6})
	}
	fake.set("035420", fakeRow{price: 250_000, at: now.Add(-time.Second), vol: 1e6})
	step(c)
	s := src(c, "KRX", 0)
	if s.state != rtOK || s.rows["035420.KS"].price != 250_000 {
		t.Fatalf("not serving: %s %+v", s.state, s.rows["035420.KS"])
	}

	// the next answer is an older copy: NAVER's trade 30 s before the one held
	now = now.Add(10 * time.Second)
	for _, sym := range []string{"000660", "005930", "009150"} {
		fake.set(sym, fakeRow{price: 100_100, at: now.Add(-time.Second), vol: 1.1e6})
	}
	fake.set("035420", fakeRow{price: 249_000, at: kst(9, 59, 29), vol: 9e5})
	step(c)
	r := s.rows["035420.KS"]
	if r.price != 250_000 || !r.trade.Equal(kst(9, 59, 59)) {
		t.Fatalf("an older answer replaced the held row: %+v", r)
	}
	if last := s.recent[len(s.recent)-1]; last.Ticker != "035420.KS" || !strings.Contains(last.Reason, "older than the quote held") {
		t.Fatalf("not listed as rejected: %+v", s.recent)
	}
	c.feeds.mu.Lock()
	as, ok := c.feeds.asset(navr, now)
	c.feeds.mu.Unlock()
	if !ok || as.QuoteFeed.LocalPrice != 250_000 || as.QuoteFeed.Source != "fake" {
		t.Fatalf("served %+v", as.QuoteFeed)
	}

	// the same trade again is no older: it stands (and is read again)
	now = now.Add(10 * time.Second)
	fake.set("035420", fakeRow{price: 250_000, at: kst(9, 59, 59), vol: 1e6})
	step(c)
	if r := s.rows["035420.KS"]; !r.read.Equal(now) || r.price != 250_000 {
		t.Fatalf("an equal answer was not taken: %+v", r)
	}
	// a newer one replaces it
	now = now.Add(10 * time.Second)
	fake.set("035420", fakeRow{price: 251_000, at: now.Add(-time.Second), vol: 1.2e6})
	step(c)
	if r := s.rows["035420.KS"]; r.price != 251_000 || !r.trade.Equal(now.Add(-time.Second)) {
		t.Fatalf("a newer answer was not taken: %+v", r)
	}
}

// Under the volume clock the answer's own clock dates a moved volume: an
// answer whose clock is older than the held row's date is a staler copy and
// is rejected; a lower volume with a newer clock is taken (the first read of a
// day can still show the last session's board, after-hours volume included).
// The settled read, dated no later than the close, is not older than a row
// the last in-session read dated a moment past the close (a board clock a
// few seconds ahead).
func TestRealtimeOlderAnswerVolumeClock(t *testing.T) {
	now := kst(10, 0, 0)
	c, fake := rtClient(t, &now, fakeRT{krxInfo(rtquote.TimesNone)})
	answer := func(stamp time.Time, price, vol float64) {
		fake.answer = func(*http.Request) (int, string) {
			return 200, fmt.Sprintf(`{"stamp":%q,"rows":[{"s":"035420","p":%g,"c":"KRW","v":%g}]}`, stamp.Format(time.RFC3339), price, vol)
		}
	}
	answer(now, 250_000, 1000) // the baseline
	step(c)
	now = now.Add(10 * time.Second)
	answer(now, 250_500, 1100) // moved: dated at the answer's clock
	step(c)
	s := src(c, "KRX", 0)
	if r := s.rows["035420.KS"]; !r.trade.Equal(now) || r.price != 250_500 {
		t.Fatalf("moved %+v", r)
	}
	dated := now
	now = now.Add(10 * time.Second)
	answer(now.Add(-25*time.Second), 249_900, 1050) // a staler copy: its clock before the held date
	step(c)
	if r := s.rows["035420.KS"]; !r.trade.Equal(dated) || r.price != 250_500 || *r.volume != 1100 {
		t.Fatalf("a staler copy replaced the held row: %+v", r)
	}
	if last := s.recent[len(s.recent)-1]; !strings.Contains(last.Reason, "older than the quote held") {
		t.Fatalf("not listed as rejected: %+v", last)
	}

	// the last in-session read dated the closing print a moment past 15:30
	now = kst(15, 29, 58)
	answer(kst(15, 30, 2), 252_000, 5000)
	step(c)
	now = kst(15, 32, 0)
	answer(kst(15, 32, 0), 252_000, 5200) // the settled read: dated 15:30, the close
	if step(c) != 1 {
		t.Fatal("no settled read")
	}
	if r := s.rows["035420.KS"]; !r.trade.Equal(kst(15, 30, 0)) || !r.read.Equal(now) || *r.volume != 5200 {
		t.Fatalf("the settled read was taken as older: %+v %+v", r, s.recent[len(s.recent)-1])
	}

	// next morning the board's volume starts again, lower, with a newer clock
	now = kst(9, 0, 5).Add(24 * time.Hour)
	answer(now, 252_500, 300)
	step(c)
	if r := s.rows["035420.KS"]; !r.trade.Equal(now) || r.price != 252_500 || *r.volume != 300 {
		t.Fatalf("the new day's first volume was refused: %+v", r)
	}
}

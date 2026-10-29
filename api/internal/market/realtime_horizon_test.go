package market

import (
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/market/rtquote"
)

// Past a venue's verified calendar (KRX's ends with 2027) its session is
// unknown: nothing is polled, Yahoo prices it, and /healthz says fallback,
// not closed.
func TestRealtimePastHorizonFallsBack(t *testing.T) {
	now := time.Date(2027, 12, 30, 10, 0, 0, 0, zone("Asia/Seoul")).UTC() // a KRX trading day
	c, fake := rtClient(t, &now, fakeRT{krxInfo(rtquote.TimesTrade)})
	fake.live = true
	for _, s := range []string{"000660", "005930", "009150", "035420"} {
		fake.set(s, fakeRow{price: 250_000})
	}
	step(c)
	if !strings.HasPrefix(c.QuoteSources(), "krx=fake ok ") {
		t.Fatalf("not serving: %s", c.QuoteSources())
	}
	navr := named(t, "NAVER")
	yahooQuote(c, navr, 240_000, now.Add(-20*time.Minute), now)

	now = time.Date(2028, 1, 4, 10, 0, 0, 0, zone("Asia/Seoul")).UTC() // a Tuesday no calendar covers
	if n := step(c); n != 0 {
		t.Fatalf("%d polls past the horizon", n)
	}
	if h := c.QuoteSources(); !strings.HasPrefix(h, "krx=yahoo fallback ") {
		t.Fatalf("header %q", h)
	}
	c.feeds.mu.Lock()
	sq, _ := c.feeds.served(navr, now)
	c.feeds.mu.Unlock()
	if sq.source != "yahoo" || sq.rt != nil {
		t.Fatalf("a real-time row served past the horizon: %+v", sq)
	}
	if d := c.QuoteSourcesDetail()[0]; d.Status != StatusFallback || d.Session != "unknown" || d.Source != "yahoo" {
		t.Fatalf("detail %+v", d)
	}
}

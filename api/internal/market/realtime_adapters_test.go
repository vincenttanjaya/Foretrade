package market

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/market/rtquote"
)

// screenerTickers is the screener's constituents by venue (its watch tier),
// read from its file: the market package cannot import the screener.
func screenerTickers(t *testing.T) map[string][]string {
	t.Helper()
	raw, err := os.ReadFile("../screener/constituents.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Market, Yahoo string
		Resolves      bool `json:"yahooResolves"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	venue := map[string]string{"JP": TSE, "KR": KRX, "TW": TWSE, "HK": HKEX, "SG": SGX, "ID": IDX, "TH": SET, "MY": BURSA, "VN": HOSE}
	out := map[string][]string{}
	for _, r := range rows {
		if v, ok := venue[r.Market]; ok && r.Resolves {
			out[v] = append(out[v], r.Yahoo)
		}
	}
	return out
}

// TestAdaptersConformance holds every registered adapter (rtquote.All) to
// the framework's contract, so an adapter added to the list is checked
// without a test of its own here: a sane Info, every ticker of its venue
// mapped to a distinct symbol, three canaries that are our tickers on the
// venue, a request with no forbidden header that stays on its host, and an
// error for an unreadable answer.
func TestAdaptersConformance(t *testing.T) {
	if len(rtquote.All) == 0 {
		t.Fatal("no adapter registered")
	}
	watch := screenerTickers(t)
	names := map[string]bool{}
	for i, a := range rtquote.All {
		in := a.Info()
		t.Run(in.Name, func(t *testing.T) {
			venue := slices.IndexFunc(rtquote.Venues, func(v struct{ Switch, Venue string }) bool { return v.Venue == in.Venue })
			switch {
			case in.Name == "" || in.Name == "yahoo" || names[in.Name]:
				t.Fatalf("name %q empty, Yahoo's or taken", in.Name)
			case venue < 0 || rtquote.Venues[venue].Switch != in.Switch:
				t.Fatalf("venue %q switch %q", in.Venue, in.Switch)
			case in.Gate != "" && in.Gate != rtquote.GateTWSEYahoo:
				t.Fatalf("gate %q", in.Gate)
			case !strings.HasPrefix(in.Host, "https://") || strings.Count(in.Host, "/") != 2:
				t.Fatalf("host %q: scheme and host only", in.Host)
			case in.Class != rtquote.ClassRealtime && in.Class != rtquote.ClassNear:
				t.Fatalf("class %q", in.Class)
			case !slices.Contains([]string{rtquote.TimesTrade, rtquote.TimesSnapshot, rtquote.TimesNone}, in.Times):
				t.Fatalf("times %q", in.Times)
			case in.Floor < rtquote.MinFloor || in.Every < in.Floor:
				t.Fatalf("every %v floor %v (floor at least %v, every at least the floor)", in.Every, in.Floor, rtquote.MinFloor)
			case in.Settle <= 0 || in.Settle > time.Hour || in.MaxLag <= 0 || in.MaxLag > 10*time.Minute:
				t.Fatalf("settle %v maxLag %v", in.Settle, in.MaxLag)
			case (calendars[in.Venue] == nil) != (in.Hours != nil):
				t.Fatalf("hours %v: required exactly when %s has no verified calendar", in.Hours, in.Venue)
			}
			names[in.Name] = true
			if in.Hours != nil {
				if _, err := parseHours(*in.Hours); err != nil {
					t.Fatalf("hours: %v", err)
				}
			}
			for _, w := range in.Quiet {
				a, err1 := parseHM(w.From)
				b, err2 := parseHM(w.To)
				if err1 != nil || err2 != nil || a >= b {
					t.Fatalf("quiet window %v", w)
				}
			}
			// the gated source comes after the ungated ones of its venue
			for _, b := range rtquote.All[:i] {
				if bi := b.Info(); bi.Venue == in.Venue && bi.Gate != "" && in.Gate == "" {
					t.Fatalf("%s comes after the gated %s", in.Name, bi.Name)
				}
			}
			// every ticker of the venue maps, each to its own symbol
			tickers := map[string]bool{}
			for _, r := range feedRows() {
				if r.Listing != nil && r.Listing.SessionVenue == in.Venue {
					tickers[r.Pair] = true
				}
			}
			for _, w := range watch[in.Venue] {
				tickers[w] = true
			}
			if len(in.Canaries) != 3 {
				t.Fatalf("%d canaries, want the top 3", len(in.Canaries))
			}
			for _, c := range in.Canaries {
				if !tickers[c] {
					t.Fatalf("canary %s is not one of our %s tickers", c, in.Venue)
				}
			}
			want := rtquote.Want{}
			for ticker := range tickers {
				sym, ok := a.Symbol(ticker)
				if !ok || sym == "" {
					t.Errorf("%s does not map", ticker)
					continue
				}
				if w, dup := want[sym]; dup {
					t.Errorf("%s and %s both map to %s", ticker, w.Ticker, sym)
				}
				want[sym] = rtquote.Wanted{Ticker: ticker, Currency: venueCurrency(in.Venue, ticker)}
			}
			for _, bad := range []string{"", "AAPL", "7203.T.X", "0700.HK.KS"} {
				if _, ok := a.Symbol(bad); ok && in.Venue != TSE {
					t.Errorf("%q maps", bad)
				}
			}
			for _, host := range []string{in.Host, "http://127.0.0.1:9"} {
				req, err := a.Request(context.Background(), host, want)
				if err != nil {
					t.Fatal(err)
				}
				if err := rtPrepare(req, host); err != nil {
					t.Fatalf("request to %s: %v", host, err)
				}
				if req.Method != http.MethodGet && req.Method != http.MethodPost {
					t.Fatalf("method %s", req.Method)
				}
				if req.Header.Get("User-Agent") != rtquote.UserAgent {
					t.Fatal("user agent")
				}
			}
			for _, garbage := range []string{"", "<html><body>Just a moment...</body></html>", "\x00\x01"} {
				if _, err := a.Parse([]byte(garbage), want); err == nil {
					t.Errorf("parsed %q", garbage)
				}
			}
		})
	}
}

// TestRealtimeBudgetWeekday simulates Wednesday 2026-10-14 (UTC) for every
// registered adapter's schedule (its Info; the requests go to a fake that
// answers fresh trades) and reports each host's requests and busiest
// minute: one request per interval while the session is open, one settled
// read after the close, none otherwise.
func TestRealtimeBudgetWeekday(t *testing.T) {
	day := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	now := day.Add(-4 * time.Hour) // after every Asian close of the 13th: warm-up
	var adapters []rtquote.Adapter
	for _, a := range rtquote.All {
		adapters = append(adapters, fakeRT{a.Info()})
	}
	c, fake := rtClient(t, &now, adapters...)
	fake.live = true
	for _, a := range rtquote.All {
		in := a.Info()
		for _, ticker := range in.Canaries {
			sym, _ := fakeRT{}.Symbol(ticker)
			fake.set(sym, fakeRow{price: 100, ccy: venueCurrency(in.Venue, ticker)})
		}
	}
	for ; now.Before(day.Add(24 * time.Hour)); now = now.Add(time.Second) {
		step(c)
	}
	type host struct {
		requests, busiest int
		perMinute         map[int]int
		first, last       time.Time
	}
	hosts := map[string]*host{}
	total := 0
	for i, req := range fake.reqs {
		at := fake.at[i]
		if at.Before(day) {
			continue
		}
		h := hosts[req.URL.Host]
		if h == nil {
			h = &host{perMinute: map[int]int{}, first: at}
			hosts[req.URL.Host] = h
		}
		h.requests++
		total++
		m := int(at.Sub(day) / time.Minute)
		h.perMinute[m]++
		h.busiest = max(h.busiest, h.perMinute[m])
		h.last = at
	}
	busiestAll := map[int]int{}
	for _, h := range hosts {
		for m, n := range h.perMinute {
			busiestAll[m] += n
		}
	}
	peak := 0
	for _, n := range busiestAll {
		peak = max(peak, n)
	}
	first := map[string]bool{} // a venue's later sources stand by while its first serves
	for _, a := range rtquote.All {
		in := a.Info()
		h := hosts[strings.TrimPrefix(in.Host, "https://")]
		if first[in.Venue] {
			if h != nil {
				t.Errorf("%s (a fallback) was asked %d times while its venue's first source served", in.Name, h.requests)
			}
			t.Logf("%s (%s): standby behind its venue's first source", in.Host, in.Name)
			continue
		}
		first[in.Venue] = true
		if h == nil {
			t.Fatalf("%s made no request", in.Name)
		}
		limit := int(time.Minute / max(in.Every, in.Floor))
		if h.busiest > limit {
			t.Errorf("%s: %d requests in a minute, its interval allows %d", in.Name, h.busiest, limit)
		}
		t.Logf("%s (%s): %d requests on 2026-10-14, busiest minute %d, first %s, last %s UTC", in.Host, in.Name, h.requests,
			h.busiest, h.first.Format("15:04:05"), h.last.Format("15:04:05"))
		if src := src(c, in.Venue, 0); src.state != rtOK {
			t.Errorf("%s ended %s (%s)", in.Name, src.state, src.reason)
		}
	}
	t.Logf("all hosts: %d requests, busiest minute %d", total, peak)
	if naver := hosts["polling.finance.naver.com"]; naver == nil || naver.requests != 2340+1 || naver.busiest != 6 {
		t.Fatalf("naver %+v: 09:00-15:30 KST every 10 s and the settled read", naver)
	}
}

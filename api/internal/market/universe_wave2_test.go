package market

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
)

// The second batch's twelve US stocks and seven Asian perps follow the first 19 perps;
// nothing before them moves, and each carries its listing and Yahoo ticker.
func TestWave2Catalog(t *testing.T) {
	want := []struct {
		symbol, name, region, venue, underlying, currency, yahoo string
	}{
		{"PLTR", "Palantir", "us", NYSE, "Nasdaq: PLTR Class A common share", "USD", "PLTR"},
		{"COIN", "Coinbase", "us", NYSE, "Nasdaq: COIN Class A common share", "USD", "COIN"},
		{"HOOD", "Robinhood", "us", NYSE, "Nasdaq: HOOD Class A common share", "USD", "HOOD"},
		{"MSTR", "Strategy Inc (MicroStrategy)", "us", NYSE, "Nasdaq: MSTR Class A common share", "USD", "MSTR"},
		{"INTC", "Intel", "us", NYSE, "Nasdaq: INTC common share", "USD", "INTC"},
		{"AMD", "AMD", "us", NYSE, "Nasdaq: AMD common share", "USD", "AMD"},
		{"ORCL", "Oracle", "us", NYSE, "NYSE: ORCL common share", "USD", "ORCL"},
		{"NFLX", "Netflix", "us", NYSE, "Nasdaq: NFLX common share", "USD", "NFLX"},
		{"CRCL", "Circle Internet Group", "us", NYSE, "NYSE: CRCL Class A common share", "USD", "CRCL"},
		{"MU", "Micron Technology", "us", NYSE, "Nasdaq: MU common share", "USD", "MU"},
		{"SNDK", "SanDisk", "us", NYSE, "Nasdaq: SNDK common share", "USD", "SNDK"},
		{"CRWV", "CoreWeave", "us", NYSE, "Nasdaq: CRWV Class A common share", "USD", "CRWV"},
		{"SKHY", "SK hynix (Nasdaq ADS)", "asia", NYSE, "Nasdaq: SKHY ADS on KRX 000660 shares (live price ratio ≈0.14 shares per ADS); early bars are pre-IPO implied prices", "USD", "SKHY"},
		{"CXMT", "CXMT (ChangXin Memory)", "asia", SSE, "SSE STAR 688825 A-share, CNY→USD; early bars are pre-IPO implied prices", "CNY", "688825.SS"},
		{"UNITREE", "Unitree Robotics", "asia", SSE, "SSE STAR 688836 A-share, CNY→USD; early bars are pre-IPO implied prices", "CNY", "688836.SS"},
		{"GIGADEV", "GigaDevice Semiconductor", "asia", SSE, "SSE 603986 A-share, CNY→USD", "CNY", "603986.SS"},
		{"KIOXIA", "Kioxia Holdings", "asia", TSE, "TSE 285A ordinary share, JPY→USD; bars before the 2026-09-28 3-for-1 split are divided by 3", "JPY", "285A.T"},
		{"EWT", "iShares MSCI Taiwan ETF", "asia", NYSE, "NYSE Arca: EWT, USD (TWD exposure inside the ETF)", "USD", "EWT"},
		{"KSTR", "KraneShares STAR 50 ETF", "asia", NYSE, "NYSE Arca: KSTR, USD (STAR 50 A-share and CNY exposure inside the ETF)", "USD", "KSTR"},
	}
	if len(Universe) != 88+len(want)+len(QuoteFeeds) || len(StockPerps) != 19+len(want) || Universe[87].Symbol != "EWY" {
		t.Fatalf("universe %d perps %d row 87 %s", len(Universe), len(StockPerps), Universe[87].Symbol)
	}
	for i, w := range want {
		a := Universe[88+i]
		if a.Symbol != w.symbol || a.Pair != "xyz:"+w.symbol || a.Name != w.name || a.Class != Stock || a.Venue != "hyperliquid" || StockPerps[19+i].Symbol != w.symbol {
			t.Fatalf("row %d = %+v, want %s", 88+i, a, w.symbol)
		}
		if l := a.Listing; l == nil || *l != (Listing{w.region, w.venue, w.underlying, w.currency, w.yahoo}) {
			t.Fatalf("%s listing %+v", w.symbol, l)
		}
		if got, ok := Lookup(strings.ToLower(w.symbol)); !ok || got.Symbol != w.symbol {
			t.Fatalf("lookup %s: %+v %v", w.symbol, got, ok)
		}
		if _, ok := LookupCrypto(w.symbol); ok {
			t.Fatalf("%s resolved as crypto", w.symbol)
		}
	}
}

// The new rows are priced from the fake xyz dex and serialize their listing;
// CNY and SSE are new values of the existing fields.
func TestWave2AssetsListing(t *testing.T) {
	b := markettest.New(t)
	w := get(mount(testMarketClient(b.URL)), context.Background(), "/api/market/assets")
	var body struct{ Assets []Asset }
	// No Yahoo feed here: the quote feeds have no quote and are left out.
	if err := json.Unmarshal(w.Body.Bytes(), &body); w.Code != 200 || err != nil || len(body.Assets) != len(Universe)-len(QuoteFeeds) {
		t.Fatalf("assets %d %v %d", w.Code, err, len(body.Assets))
	}
	by := map[string]Asset{}
	for _, a := range body.Assets {
		by[a.Symbol] = a
	}
	for _, a := range StockPerps[19:] {
		got := by[a.Symbol]
		if got.Price != markettest.Price(a.Pair) || got.Region == nil || *got.Region != a.Listing.Region || *got.SessionVenue != a.Listing.SessionVenue ||
			*got.Underlying != a.Listing.Underlying || *got.LocalCurrency != a.Listing.LocalCurrency {
			t.Errorf("%s row %+v", a.Symbol, got)
		}
	}
	if !strings.Contains(w.Body.String(), `"symbol":"CXMT"`) || !strings.Contains(w.Body.String(), `"sessionVenue":"SSE"`) || !strings.Contains(w.Body.String(), `"localCurrency":"CNY"`) {
		t.Fatalf("SSE/CNY not serialized: %s", w.Body)
	}
	if strings.Contains(w.Body.String(), "688825.SS") {
		t.Fatal("the internal Yahoo ticker leaked into the API")
	}
}

// SSE's regular session at exact UTC instants (CST is UTC+8): the lunch break,
// the close, the 2026 notice's closures and the unverified 2027.
func TestSSESessions(t *testing.T) {
	const unknown = "unknown"
	for _, tc := range []struct {
		at, state, next string
	}{
		{"2026-10-08T01:29:59Z", "closed", "2026-10-08T01:30:00Z"},
		{"2026-10-08T01:30:00Z", "open", "2026-10-08T03:30:00Z"},
		{"2026-10-08T03:30:00Z", "closed", "2026-10-08T05:00:00Z"}, // lunch
		{"2026-10-08T04:59:59Z", "closed", "2026-10-08T05:00:00Z"},
		{"2026-10-08T05:00:00Z", "open", "2026-10-08T07:00:00Z"},
		{"2026-10-08T07:00:00Z", "closed", "2026-10-09T01:30:00Z"},
		{"2026-10-06T02:00:00Z", "closed", "2026-10-08T01:30:00Z"}, // National Day, Oct 1–7
		{"2026-09-30T07:00:00Z", "closed", "2026-10-08T01:30:00Z"},
		{"2026-09-24T07:00:00Z", "closed", "2026-09-28T01:30:00Z"}, // Mid-Autumn (Fri)
		{"2026-02-13T07:00:00Z", "closed", "2026-02-24T01:30:00Z"}, // Spring Festival, the longest run
		{"2026-04-03T07:00:00Z", "closed", "2026-04-07T01:30:00Z"}, // Qingming (Mon)
		{"2026-04-30T07:00:00Z", "closed", "2026-05-06T01:30:00Z"}, // Labour Day
		{"2026-06-18T07:00:00Z", "closed", "2026-06-22T01:30:00Z"}, // Dragon Boat (Fri)
		{"2026-10-10T02:00:00Z", "closed", "2026-10-12T01:30:00Z"}, // a make-up Saturday stays closed
		{"2026-12-31T06:00:00Z", "open", "2026-12-31T07:00:00Z"},
		{"2026-12-31T07:00:00Z", "closed", ""}, // the next open is in unverified 2027
		{"2027-01-04T02:00:00Z", unknown, ""},
		{"2025-12-31T02:00:00Z", unknown, ""},
	} {
		t.Run(tc.at, func(t *testing.T) {
			at, _ := time.Parse(time.RFC3339, tc.at)
			open, next, known := calendars[SSE].session(at)
			state := map[bool]string{true: "open", false: "closed"}[open]
			if !known {
				state = unknown
			}
			want := time.Time{}
			if tc.next != "" {
				want, _ = time.Parse(time.RFC3339, tc.next)
			}
			if state != tc.state || !next.Equal(want) {
				t.Fatalf("%s next %s, want %s %s", state, next, tc.state, want)
			}
		})
	}
	rows := withSessions([]Asset{{Symbol: "CXMT", Class: Stock}, {Symbol: "NVDA", Class: Stock}}, time.Date(2027, 3, 1, 2, 0, 0, 0, time.UTC))
	if rows[0].Session != nil || rows[0].SessionChangeAt != nil || rows[0].SessionVenue == nil || *rows[0].SessionVenue != SSE || rows[1].Session == nil {
		t.Fatalf("2027: SSE unverified, NYSE known: %+v %+v", rows[0], rows[1])
	}
}

// Kioxia's xyz history is served in post-split units at the one perp-candle
// chokepoint: bars that closed before the 2026-09-28 08:00Z switch are divided
// by 3 (volume ×3), the daily bar across it is rebuilt from 4h bars, and so
// every consumer (daily, intraday, history before a date) sees no split cliff.
func TestKioxiaSplitAdjusted(t *testing.T) {
	ctx := context.Background()
	b := markettest.New(t)
	c := testMarketClient(b.URL)
	cut := markettest.KioxiaSplit.Unix()

	// The fake, like the dex, is unadjusted.
	raw := rawDaily(t, b.URL, "xyz:KIOXIA")
	day := func(bars []Candle, at time.Time) Candle {
		for _, x := range bars {
			if x.T == at.Unix() {
				return x
			}
		}
		t.Fatalf("no bar at %s", at)
		return Candle{}
	}
	pre, splitDay := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	if r := day(raw, pre); math.Abs(r.C/markettest.Close("xyz:KIOXIA", 4)-3) > 1e-9 {
		t.Fatalf("fake pre-split bar is not unadjusted: %+v", r)
	}

	for _, interval := range []string{"1d", "4h", "1h", "15m"} {
		bars, err := c.Candles(ctx, "KIOXIA", interval, 3000)
		if err != nil || len(bars) < 100 {
			t.Fatalf("%s: %d bars %v", interval, len(bars), err)
		}
		for i := 1; i < len(bars); i++ {
			if move := bars[i].C/bars[i-1].C - 1; math.Abs(move) > 0.35 {
				t.Fatalf("%s: %.0f%% close move at %d", interval, move*100, bars[i].T)
			}
			if bars[i].T-bars[i-1].T != BarSeconds(interval) {
				t.Fatalf("%s: gap at %d", interval, bars[i].T)
			}
		}
		for _, x := range bars {
			if x.H > markettest.Price("xyz:KIOXIA")*1.5 {
				t.Fatalf("%s: unadjusted high %+v", interval, x)
			}
		}
	}

	daily, _ := c.Candles(ctx, "KIOXIA", "1d", 300)
	if a, r := day(daily, pre), day(raw, pre); math.Abs(a.O*3-r.O) > 1e-9 || math.Abs(a.C*3-r.C) > 1e-9 || math.Abs(a.H*3-r.H) > 1e-9 || math.Abs(a.L*3-r.L) > 1e-9 || a.V != r.V*3 {
		t.Fatalf("pre-split bar %+v from %+v", a, r)
	}
	fourH, _ := c.Candles(ctx, "KIOXIA", "4h", 3000)
	var want Candle
	for _, x := range fourH {
		if x.T >= splitDay.Unix() && x.T < splitDay.Unix()+86400 {
			if want.T == 0 {
				want = Candle{T: splitDay.Unix(), O: x.O, H: x.H, L: x.L}
			}
			want.H, want.L, want.C, want.V = max(want.H, x.H), min(want.L, x.L), x.C, want.V+x.V
		}
	}
	if got := day(daily, splitDay); got != want || got.V != 2*3000+4*1000 {
		t.Fatalf("split-day bar %+v, want %+v from 4h", got, want)
	}
	if got := day(daily, time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)); got != day(raw, time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("post-split bar changed: %+v", got)
	}
	hourly, _ := c.Candles(ctx, "KIOXIA", "1h", 300)
	for _, x := range hourly {
		if x.T+3600 <= cut && x.V != 3000 || x.T >= cut && x.V != 1000 {
			t.Fatalf("hourly volume at %d: %v", x.T, x.V)
		}
	}

	// History before a date and the rebuilt bar's cache: a second read asks no 4h bars.
	before, err := c.CandlesBefore(ctx, "KIOXIA", "1d", splitDay.Add(48*time.Hour), 10)
	if err != nil || len(before) != 10 || before[len(before)-1].T != splitDay.Unix()+86400 || before[len(before)-2] != want {
		t.Fatalf("history before: %+v %v", before, err)
	}
	n := b.Requests("candle:xyz:KIOXIA")
	if _, err := c.CandlesBefore(ctx, "KIOXIA", "1d", splitDay.Add(48*time.Hour), 10); err != nil || b.Requests("candle:xyz:KIOXIA") != n+1 {
		t.Fatalf("rebuilt split-day bar not kept: %d → %d requests, %v", n, b.Requests("candle:xyz:KIOXIA"), err)
	}

	// Other perps are untouched.
	nvda, _ := c.Candles(ctx, "NVDA", "1d", 10)
	if nvda[len(nvda)-1].C != markettest.Price("xyz:NVDA") || nvda[len(nvda)-4].C != markettest.Close("xyz:NVDA", 3) {
		t.Fatalf("NVDA adjusted: %+v", nvda)
	}
}

// rawDaily reads coin's daily bars straight from the fake, unadjusted.
func rawDaily(t *testing.T, base, coin string) []Candle {
	t.Helper()
	body := fmt.Sprintf(`{"type":"candleSnapshot","req":{"coin":%q,"interval":"1d","startTime":0,"endTime":%d}}`, coin, markettest.End.UnixMilli())
	r, err := http.Post(base+"/info", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	var rows []struct {
		T             int64
		O, H, L, C, V string
	}
	if err := json.NewDecoder(r.Body).Decode(&rows); err != nil {
		t.Fatal(err)
	}
	out := make([]Candle, len(rows))
	for i, x := range rows {
		out[i] = Candle{T: x.T / 1000, O: num(x.O), H: num(x.H), L: num(x.L), C: num(x.C), V: num(x.V)}
	}
	return out
}

// When the finer bars a split-day bar needs are no longer retained, that bar
// is left out rather than served half-adjusted; an upstream failure is an error.
func TestSplitDayBarWithoutFinerHistory(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != http.StatusOK {
			http.Error(w, "down", status)
			return
		}
		w.Write([]byte(`[]`))
	}))
	t.Cleanup(srv.Close)
	c := testMarketClient(srv.URL)
	s := splits["xyz:KIOXIA"]
	day := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC).Unix()
	bars := []Candle{{T: day - 86400, O: 360, H: 366, L: 353, C: 357, V: 10}, {T: day, O: 356.5, H: 356.53, L: 109.8, C: 114.12, V: 20}, {T: day + 86400, O: 114, H: 116, L: 109, C: 115, V: 30}}
	out, err := c.adjustSplit(context.Background(), "xyz:KIOXIA", "1d", s, bars)
	if err != nil || len(out) != 2 || out[0] != (Candle{T: day - 86400, O: 120, H: 122, L: 353.0 / 3, C: 119, V: 30}) || out[1] != bars[2] {
		t.Fatalf("adjusted %+v %v", out, err)
	}
	status = http.StatusInternalServerError
	if _, err := c.adjustSplit(context.Background(), "xyz:KIOXIA", "1d", s, bars); !errors.Is(err, ErrUpstream) {
		t.Fatalf("failed rebuild: %v", err)
	}
	if out, err := c.adjustSplit(context.Background(), "xyz:KIOXIA", "15m", s, []Candle{{T: s.at.Unix() - 900, O: 3, H: 3, L: 3, C: 3, V: 1}}); err != nil || out[0].C != 1 || out[0].V != 3 {
		t.Fatalf("15m pre-split bar %+v %v", out, err)
	}
}

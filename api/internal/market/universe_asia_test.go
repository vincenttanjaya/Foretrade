package market

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
)

// The ten 2026-10-06 Asia admissions follow the nine US perps; nothing before
// them moves, and every perp carries the listing the web contract renders.
func TestAsiaCatalog(t *testing.T) {
	if len(Universe) != 107+len(QuoteFeeds) || len(CryptoUniverse) != 69 || len(StockPerps) != 38 || len(Core) != 12 {
		t.Fatalf("universe %d crypto %d perps %d core %d", len(Universe), len(CryptoUniverse), len(StockPerps), len(Core))
	}
	want := []struct {
		symbol, name, region, venue, underlying, currency string
	}{
		{"TSM", "TSMC (Taiwan Semiconductor)", "asia", NYSE, "NYSE: TSM ADS = 5 TWSE 2330 shares", "USD"},
		{"BABA", "Alibaba Group", "asia", NYSE, "NYSE: BABA ADS = 8 HKEX 9988 shares", "USD"},
		{"SMSN", "Samsung Electronics", "asia", KRX, "KRX 005930 common share, KRW→USD", "KRW"},
		{"SKHX", "SK hynix", "asia", KRX, "KRX 000660 common share, KRW→USD", "KRW"},
		{"HYUNDAI", "Hyundai Motor", "asia", KRX, "KRX 005380 common share, KRW→USD", "KRW"},
		{"SOFTBANK", "SoftBank Group", "asia", TSE, "TSE 9984 ordinary share, JPY→USD", "JPY"},
		{"MINIMAX", "MiniMax Group", "asia", HKEX, "HKEX 0100 Class A share, HKD→USD", "HKD"},
		{"ZHIPU", "Zhipu AI (Knowledge Atlas)", "asia", HKEX, "HKEX 2513 H-share, HKD→USD", "HKD"},
		{"EWJ", "iShares MSCI Japan ETF", "asia", NYSE, "NYSE Arca: EWJ, USD (JPY exposure inside the ETF)", "USD"},
		{"EWY", "iShares MSCI South Korea ETF", "asia", NYSE, "NYSE Arca: EWY, USD (KRW exposure inside the ETF)", "USD"},
	}
	for i, w := range want {
		a := Universe[78+i]
		if a.Symbol != w.symbol || a.Pair != "xyz:"+w.symbol || a.Name != w.name || a.Class != Stock || a.Venue != "hyperliquid" || !a.Perp() {
			t.Fatalf("row %d = %+v, want %s", 78+i, a, w.symbol)
		}
		l := a.Listing
		if l == nil || l.Region != w.region || l.SessionVenue != w.venue || l.Underlying != w.underlying || l.LocalCurrency != w.currency {
			t.Fatalf("%s listing %+v", w.symbol, l)
		}
		if got, ok := Lookup(strings.ToLower(w.symbol)); !ok || got.Symbol != w.symbol {
			t.Fatalf("lookup %s: %+v %v", w.symbol, got, ok)
		}
		if _, ok := LookupCrypto(w.symbol); ok {
			t.Fatalf("%s resolved as crypto", w.symbol)
		}
		if StockPerps[9+i].Symbol != w.symbol {
			t.Fatalf("perp %d = %s", 9+i, StockPerps[9+i].Symbol)
		}
	}
	// The original 78 rows: crypto has no listing, the nine US perps are NYSE.
	us := map[string]string{
		"NVDA": "Nasdaq: NVDA common share", "TSLA": "Nasdaq: TSLA common share", "AAPL": "Nasdaq: AAPL common share",
		"MSFT": "Nasdaq: MSFT common share", "AMZN": "Nasdaq: AMZN common share", "GOOGL": "Nasdaq: GOOGL Class A common share",
		"META": "Nasdaq: META Class A common share", "SP500": "S&P 500 index level (USD)", "XYZ100": "trade.xyz XYZ100 index (USD)",
	}
	crypto := map[string]bool{}
	for i, a := range Universe[:78] {
		switch {
		case i < 69:
			if a.Listing != nil || a.Perp() {
				t.Fatalf("crypto row carries a listing: %+v", a)
			}
			crypto[a.Symbol] = true
		default:
			l := a.Listing
			if l == nil || l.Region != "us" || l.SessionVenue != NYSE || l.LocalCurrency != "USD" || l.Underlying != us[a.Symbol] {
				t.Fatalf("US listing %s %+v", a.Symbol, l)
			}
		}
	}
	for _, a := range StockPerps {
		if crypto[a.Symbol] || crypto[strings.TrimPrefix(a.Pair, "xyz:")] {
			t.Fatalf("perp %s collides with a crypto symbol", a.Symbol)
		}
		if l := a.Listing; l == nil || len(l.Underlying) == 0 || len(l.Underlying) > 120 || calendars[l.SessionVenue] == nil ||
			!slices.Contains([]string{"us", "asia"}, l.Region) || !slices.Contains([]string{"USD", "KRW", "JPY", "HKD", "CNY"}, l.LocalCurrency) {
			t.Fatalf("perp %s listing %+v", a.Symbol, l)
		}
	}
	if l := SymbolList(); !strings.Contains(l, ", META, SP500, XYZ100, TSM, BABA, SMSN, SKHX, HYUNDAI, SOFTBANK, MINIMAX, ZHIPU, EWJ, EWY, PLTR, COIN, HOOD, MSTR, INTC, AMD, ORCL, NFLX, CRCL, MU, SNDK, CRWV, SKHY, CXMT, UNITREE, GIGADEV, KIOXIA, EWT, KSTR, HSDT, ") {
		t.Fatalf("symbol list %q", l)
	}
	if l := CryptoSymbolList(); strings.Contains(l, "TSM") || strings.Contains(l, "NVDA") || !strings.HasSuffix(l, ", PENDLE") {
		t.Fatalf("crypto symbol list %q", l)
	}
}

// GET /api/market/assets serializes the listing for a US stock, an Asian stock
// and (as null) crypto, the new pairs priced from the fake xyz dex.
func TestAssetsListingFields(t *testing.T) {
	b := markettest.New(t)
	w := get(mount(testMarketClient(b.URL)), context.Background(), "/api/market/assets")
	var body struct{ Assets []map[string]json.RawMessage }
	if err := json.Unmarshal(w.Body.Bytes(), &body); w.Code != 200 || err != nil || len(body.Assets) != 107 {
		t.Fatalf("assets %d %v %d", w.Code, err, len(body.Assets))
	}
	by := map[string]map[string]json.RawMessage{}
	for _, a := range body.Assets {
		var s string
		json.Unmarshal(a["symbol"], &s)
		by[s] = a
	}
	for symbol, want := range map[string][4]string{
		"AAPL":     {`"us"`, `"NYSE"`, `"Nasdaq: AAPL common share"`, `"USD"`},
		"SMSN":     {`"asia"`, `"KRX"`, `"KRX 005930 common share, KRW→USD"`, `"KRW"`},
		"SOFTBANK": {`"asia"`, `"TSE"`, `"TSE 9984 ordinary share, JPY→USD"`, `"JPY"`},
		"ZHIPU":    {`"asia"`, `"HKEX"`, `"HKEX 2513 H-share, HKD→USD"`, `"HKD"`},
		"TSM":      {`"asia"`, `"NYSE"`, `"NYSE: TSM ADS = 5 TWSE 2330 shares"`, `"USD"`},
		"BTC":      {`null`, `null`, `null`, `null`},
	} {
		a := by[symbol]
		for i, key := range []string{"region", "sessionVenue", "underlying", "localCurrency"} {
			raw, ok := a[key]
			var got, exp any
			json.Unmarshal(raw, &got)
			json.Unmarshal([]byte(want[i]), &exp)
			if !ok || got != exp {
				t.Errorf("%s.%s = %s, want %s", symbol, key, raw, want[i])
			}
		}
		if _, ok := a["session"]; !ok {
			t.Errorf("%s has no session key", symbol)
		}
	}
	for symbol, price := range map[string]float64{"TSM": 485, "BABA": 110.6, "SMSN": 202.5, "SKHX": 1325, "HYUNDAI": 259,
		"SOFTBANK": 39.9, "MINIMAX": 31.9, "ZHIPU": 91.6, "EWJ": 99.7, "EWY": 188.7} {
		var got float64
		json.Unmarshal(by[symbol]["price"], &got)
		if got != price || got != markettest.Price("xyz:"+symbol) {
			t.Errorf("%s price %v, want %v", symbol, got, price)
		}
	}
}

// Listing pointers are owned by each read, like the session clock.
func TestListingIsResponseLocal(t *testing.T) {
	rows := []Asset{{Symbol: "SMSN", Class: Stock, Venue: "hyperliquid", Price: 200}, {Symbol: "BTC", Class: Crypto, Venue: "binance", Price: 1}}
	a := withSessions(rows, time.Now())
	b := withSessions(rows, time.Now())
	if a[0].Region == nil || *a[0].Region != "asia" || a[1].Region != nil || rows[0].Region != nil {
		t.Fatalf("listing projection %+v %+v", a[0], rows[0])
	}
	*a[0].Region, *a[0].Underlying = "forged", "forged"
	if *b[0].Region != "asia" || Universe[80].Listing.Region != "asia" || Universe[80].Listing.Underlying != "KRX 005930 common share, KRW→USD" {
		t.Fatal("listing pointer aliases the catalog or another read")
	}
}

// Each Asian venue's regular session at exact UTC instants: open, lunch,
// close, holidays, half-days and the edges of the verified horizon.
func TestAsiaVenueSessions(t *testing.T) {
	const unknown = "unknown"
	for _, tc := range []struct {
		venue, at, state, next string // next "" = no known boundary
	}{
		// KRX 09:00–15:30 KST (UTC+9), no lunch break.
		{KRX, "2026-10-05T23:59:59Z", "closed", "2026-10-06T00:00:00Z"},
		{KRX, "2026-10-06T00:00:00Z", "open", "2026-10-06T06:30:00Z"},
		{KRX, "2026-10-06T03:00:00Z", "open", "2026-10-06T06:30:00Z"},
		{KRX, "2026-10-06T06:30:00Z", "closed", "2026-10-07T00:00:00Z"},
		{KRX, "2026-10-09T01:00:00Z", "closed", "2026-10-12T00:00:00Z"}, // Hangeul Day (Fri)
		{KRX, "2026-09-23T06:30:00Z", "closed", "2026-09-28T00:00:00Z"}, // Chuseok
		{KRX, "2027-12-30T06:30:00Z", "closed", ""},                     // year-end closing, then 2028 is unverified
		{KRX, "2025-12-30T01:00:00Z", unknown, ""},
		{KRX, "2028-01-04T01:00:00Z", unknown, ""},
		// TSE 09:00–11:30 and 12:30–15:30 JST (UTC+9).
		{TSE, "2026-10-06T00:00:00Z", "open", "2026-10-06T02:30:00Z"},
		{TSE, "2026-10-06T02:30:00Z", "closed", "2026-10-06T03:30:00Z"}, // lunch
		{TSE, "2026-10-06T03:29:59Z", "closed", "2026-10-06T03:30:00Z"},
		{TSE, "2026-10-06T03:30:00Z", "open", "2026-10-06T06:30:00Z"},
		{TSE, "2026-10-06T06:30:00Z", "closed", "2026-10-07T00:00:00Z"},
		{TSE, "2026-10-09T06:30:00Z", "closed", "2026-10-13T00:00:00Z"}, // Sports Day (Mon)
		{TSE, "2026-09-18T06:30:00Z", "closed", "2026-09-24T00:00:00Z"}, // Sep 21–23
		{TSE, "2026-12-30T06:30:00Z", "closed", "2027-01-04T00:00:00Z"}, // Dec 31–Jan 3
		{TSE, "2028-01-04T01:00:00Z", unknown, ""},
		// HKEX 09:30–12:00 and 13:00–16:00 HKT (UTC+8); eves close at 12:00.
		{HKEX, "2026-10-06T01:29:59Z", "closed", "2026-10-06T01:30:00Z"},
		{HKEX, "2026-10-06T01:30:00Z", "open", "2026-10-06T04:00:00Z"},
		{HKEX, "2026-10-06T04:00:00Z", "closed", "2026-10-06T05:00:00Z"}, // lunch
		{HKEX, "2026-10-06T05:00:00Z", "open", "2026-10-06T08:00:00Z"},
		{HKEX, "2026-10-06T08:00:00Z", "closed", "2026-10-07T01:30:00Z"},
		{HKEX, "2026-10-16T08:00:00Z", "closed", "2026-10-20T01:30:00Z"}, // Chung Yeung (Mon)
		{HKEX, "2026-12-24T03:59:59Z", "open", "2026-12-24T04:00:00Z"},   // Christmas Eve half-day
		{HKEX, "2026-12-24T04:00:00Z", "closed", "2026-12-28T01:30:00Z"},
		{HKEX, "2026-02-16T04:30:00Z", "closed", "2026-02-20T01:30:00Z"}, // Lunar New Year's Eve, then three days
		{HKEX, "2027-12-31T04:00:00Z", "closed", ""},
		{HKEX, "2028-01-03T02:00:00Z", unknown, ""},
	} {
		t.Run(tc.venue+" "+tc.at, func(t *testing.T) {
			at, _ := time.Parse(time.RFC3339, tc.at)
			open, next, known := calendars[tc.venue].session(at)
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
}

// The projected Asset follows each row's own venue: null outside a venue's
// horizon even while NYSE still knows its session.
func TestAsiaSessionProjection(t *testing.T) {
	rows := []Asset{{Symbol: "NVDA", Class: Stock}, {Symbol: "TSM", Class: Stock}, {Symbol: "SMSN", Class: Stock}, {Symbol: "MINIMAX", Class: Stock}, {Symbol: "BTC", Class: Crypto}}
	at, _ := time.Parse(time.RFC3339, "2026-10-06T04:30:00Z") // HKEX lunch, KRX open, NYSE overnight
	out := withSessions(rows, at)
	for i, want := range []string{"closed", "closed", "open", "closed"} {
		if out[i].Session == nil || *out[i].Session != want || out[i].SessionChangeAt == nil {
			t.Fatalf("%s session %v", out[i].Symbol, out[i].Session)
		}
	}
	if !out[3].SessionChangeAt.Equal(time.Date(2026, 10, 6, 5, 0, 0, 0, time.UTC)) || out[4].Session != nil || out[4].SessionVenue != nil {
		t.Fatalf("HKEX afternoon open %v / crypto %+v", out[3].SessionChangeAt, out[4])
	}
	out = withSessions(rows, time.Date(2028, 3, 1, 15, 0, 0, 0, time.UTC))
	if out[0].Session == nil || out[1].Session == nil || out[2].Session != nil || out[2].SessionChangeAt != nil || out[2].SessionVenue == nil || *out[2].SessionVenue != KRX {
		t.Fatalf("2028: NYSE known, KRX unverified: %+v %+v", out[0], out[2])
	}
}

// Calendar data sanity: every closure and half-day is a weekday inside its
// venue's verified years, and no day is both.
func TestVenueCalendarData(t *testing.T) {
	for venue, c := range calendars {
		check := func(day string) time.Time {
			d, err := time.ParseInLocation(time.DateOnly, day, c.loc)
			if err != nil || d.Year() < c.from || d.Year() > c.to || d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
				t.Errorf("%s %s: %v %v", venue, day, d.Weekday(), err)
			}
			return d
		}
		for day := range c.holidays {
			check(day)
		}
		for day := range c.short {
			check(day)
			if c.holidays[day] {
				t.Errorf("%s %s is a holiday and a half-day", venue, day)
			}
		}
	}
}

// "TSMC" is what users and the AI say; it resolves to the canonical TSM.
func TestTSMCAlias(t *testing.T) {
	a, ok := Lookup(" tsmc ")
	if !ok || a.Symbol != "TSM" || a.Pair != "xyz:TSM" || a.Listing.Region != "asia" || a.Listing.SessionVenue != NYSE || a.Listing.LocalCurrency != "USD" {
		t.Fatalf("TSMC: %+v %v", a, ok)
	}
	if _, ok := LookupCrypto("TSMC"); ok {
		t.Fatal("TSMC resolved as crypto")
	}
	q, err := testMarketClient(markettest.New(t).URL).Quote(context.Background(), "TSMC")
	if err != nil || q.Symbol != "TSM" || q.Price != 485 {
		t.Fatalf("TSMC quote %+v %v", q, err)
	}
}

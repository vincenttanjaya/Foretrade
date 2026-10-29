package main

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo/yahootest"
)

// TestIntegrationListed: a listed Asian constituent (6501.T) through the real
// Yahoo client, the warm loop and the database: in the instrument catalogue,
// out of the plain assets list, in scope=listed and with=, charted, filled
// at a fresh read, held (so the poller's interest set names it), and the
// screener's Trade link; a research-only ticker is not an instrument.
func TestIntegrationListed(t *testing.T) {
	s := boot(t, options{})
	now := time.Now().UTC().Truncate(time.Second)
	h := yahootest.Daily("6501.T", "JPY", 4700, 4800, 4850)
	h.Meta.PriceTime, h.Meta.Timezone, h.Meta.LongName = &now, "Asia/Tokyo", "Hitachi, Ltd."
	s.yahoo.Set(h)
	s.yahoo.SetFX("JPY", 0.0066)
	c := s.signIn(t)

	// The catalogue: every instrument, priced or not, cached by ETag.
	resp, body := c.send(c.request("GET", "/api/market/instruments", nil))
	if resp.StatusCode != 200 || resp.Header.Get("Cache-Control") != "private, max-age=3600" || resp.Header.Get("ETag") == "" {
		t.Fatalf("instruments: %d %v", resp.StatusCode, resp.Header)
	}
	rows := list(t, decode(t, body), "instruments")
	if len(rows) != len(market.Instruments()) || len(rows) != 849 {
		t.Fatalf("%d instruments", len(rows))
	}
	shapeAll(t, decode(t, body), "instruments", "symbol pair name class venue region sessionVenue localCurrency underlying yahoo listed index")
	i := slices.IndexFunc(rows, func(r any) bool { return str(t, r, "symbol") == "6501.T" })
	if i < 0 || str(t, rows[i], "index") != "Nikkei 225" || at(t, rows[i], "listed") != true || str(t, rows[i], "sessionVenue") != "TSE" {
		t.Fatalf("6501.T in the catalogue: %v", rows[max(i, 0)])
	}
	again := c.request("GET", "/api/market/instruments", nil)
	again.Header.Set("If-None-Match", resp.Header.Get("ETag"))
	if r, b := c.send(again); r.StatusCode != http.StatusNotModified || len(b) != 0 {
		t.Fatalf("If-None-Match: %d", r.StatusCode)
	}

	// The plain list stays the named rows; the listed poll and with= carry 6501.T.
	symbols := func(path string) []string {
		var out []string
		for _, r := range list(t, c.call("GET", path, nil, 200), "assets") {
			out = append(out, str(t, r, "symbol"))
		}
		return out
	}
	eventually(t, 20*time.Second, "the warm loop's read of 6501.T", func() bool {
		return slices.Contains(symbols("/api/market/assets?scope=listed"), "6501.T")
	})
	if slices.Contains(symbols("/api/market/assets"), "6501.T") {
		t.Fatal("a listed row in the plain assets list")
	}
	withRows := list(t, c.call("GET", "/api/market/assets?scope=named&with=6501.t", nil, 200), "assets")
	last := withRows[len(withRows)-1]
	shape(t, last, "", assetKeys+" quoteFeed")
	if str(t, last, "symbol") != "6501.T" || str(t, last, "venue") != "yahoo" || !near(num(t, last, "price"), 4850*0.0066) ||
		str(t, last, "underlying") != "TSE: 6501 (Nikkei 225), JPY→USD at Yahoo FX" || str(t, last, "quoteFeed.fxSymbol") != "JPYUSD=X" {
		t.Fatalf("with=6501.T: %v", last)
	}
	c.fail("GET", "/api/market/assets?scope=everything", nil, 400, "bad_request")

	// Charted in USD, filled at the USD mark, held.
	candles := list(t, c.call("GET", "/api/market/candles?symbol=6501.T&interval=1d&limit=3", nil, 200), "candles")
	if len(candles) != 3 || !near(num(t, candles[2], "c"), 4850*0.0066) {
		t.Fatalf("candles %v", candles)
	}
	fill := c.call("POST", "/api/orders", map[string]any{"symbol": "6501.t", "side": "buy", "qty": 100}, 200)
	if str(t, fill, "trade.symbol") != "6501.T" || !near(num(t, fill, "trade.price"), paper.FillPrice(4850*0.0066, true)) {
		t.Fatalf("fill %v", fill)
	}
	pos := list(t, c.call("GET", "/api/portfolio", nil, 200), "portfolio.positions")
	if len(pos) != 1 || str(t, pos[0], "symbol") != "6501.T" || !near(num(t, pos[0], "markPrice"), 4850*0.0066) {
		t.Fatalf("positions %v", pos)
	}
	if n := s.count(t, "select count(*) from positions where symbol = '6501.T' and qty = 100"); n != 1 {
		t.Fatalf("%d stored positions", n)
	}
	// The warm loop's interest query (read at boot) works, and now names the held row.
	held, err := feedInterest(s.db)(t.Context())
	if err != nil || !slices.Contains(held, "6501.T") || strings.Contains(s.logs.String(), "quote-feed interest unavailable") {
		t.Fatalf("interest %v %v", held, err)
	}

	// The screener links it; a research-only ticker is no instrument.
	row := list(t, c.call("GET", "/api/screener/asia?q=6501.T", nil, 200), "rows")[0]
	if str(t, row, "tradableSymbol") != "6501.T" || str(t, row, "tradeVia") != "listing" {
		t.Fatalf("screener %v", row)
	}
	c.fail("POST", "/api/orders", map[string]any{"symbol": "MER.PS", "side": "buy", "qty": 1}, 400, "bad_request")
	c.fail("GET", "/api/market/candles?symbol=MER.PS&interval=1d", nil, 400, "bad_request")
}

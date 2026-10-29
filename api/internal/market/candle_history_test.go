package market

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo/yahootest"
)

// An older page excludes the cursor bar, so pages can be prepended without
// repeating a candle or leaving a gap, for both spot and retained perp history.
func TestCandleHistoryPagesHTTP(t *testing.T) {
	b := markettest.New(t)
	mux := mount(testMarketClient(b.URL))
	for _, symbol := range []string{"BTC", "NVDA"} {
		t.Run(symbol, func(t *testing.T) {
			path := "/api/market/candles?symbol=" + symbol + "&interval=1h"
			latest := readHistoryCandles(t, mux, path+"&limit=5")
			if len(latest) != 5 || latest[4].T != markettest.End.Unix() {
				t.Fatalf("latest page: %+v", latest)
			}
			cursor := latest[0].T
			for range 2 {
				older := readHistoryCandles(t, mux, fmt.Sprintf("%s&limit=3&before=%d", path, cursor))
				if len(older) != 3 || older[0].T != cursor-3*3600 || older[2].T != cursor-3600 {
					t.Fatalf("page before %d: %+v", cursor, older)
				}
				cursor = older[0].T
			}
			if got := readHistoryCandles(t, mux, fmt.Sprintf("%s&before=%d", path, cursor)); len(got) != 500 {
				t.Fatalf("default historical page: %d candles", len(got))
			}
			if got := readHistoryCandles(t, mux, fmt.Sprintf("%s&limit=1000&before=%d", path, cursor)); len(got) != 1000 {
				t.Fatalf("maximum historical page: %d candles", len(got))
			}
			if got := readHistoryCandles(t, mux, path+"&before=1"); got == nil || len(got) != 0 {
				t.Fatalf("before listing must return an empty array: %+v", got)
			}
		})
	}
}

func TestCandleHistoryBeforeValidation(t *testing.T) {
	b := markettest.New(t)
	c := testMarketClient(b.URL)
	c.Now = func() time.Time { return markettest.End }
	mux := mount(c)
	for _, before := range []string{"", "0", "-1", "yesterday", "123.5", "9223372036854775808", "9223372036854776"} {
		w := get(mux, context.Background(), "/api/market/candles?symbol=BTC&interval=1h&limit=1&before="+before)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"bad_request"`) {
			t.Errorf("invalid before %q: %d %s", before, w.Code, w.Body)
		}
	}
	// A valid future cursor still returns the latest available bars, including
	// a perp candle opening exactly at now, rather than querying a future window.
	before := markettest.End.AddDate(1, 0, 0).Unix()
	for _, symbol := range []string{"BTC", "NVDA"} {
		if got := readHistoryCandles(t, mux, fmt.Sprintf("/api/market/candles?symbol=%s&interval=1h&limit=1&before=%d", symbol, before)); len(got) != 1 || got[0].T != markettest.End.Unix() {
			t.Fatalf("%s future cursor: %+v", symbol, got)
		}
	}
}

func TestCandleHistoryDoesNotReuseLatestCache(t *testing.T) {
	b := markettest.New(t)
	mux := mount(testMarketClient(b.URL))
	path := "/api/market/candles?symbol=BTC&interval=1h&limit=3"
	latest := readHistoryCandles(t, mux, path)
	b.Down("BTCUSDT", true)
	w := get(mux, context.Background(), fmt.Sprintf("%s&before=%d", path, latest[0].T))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("historical read used latest cache: %d %s", w.Code, w.Body)
	}
}

func TestCandleHistoryOriginalCurrencyHTTP(t *testing.T) {
	c, f := feedClient(t)
	f.Set(yahootest.Daily("D05.SI", "SGD", 10, 20, 30, 40))
	f.Set(yahootest.Daily("SGDUSD=X", "USD", .25, .5, .75, 1))
	mux := mount(c)
	before := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC).Unix()
	path := fmt.Sprintf("/api/market/candles?symbol=DBS&interval=1d&limit=2&before=%d", before)
	usd := readHistoryCandles(t, mux, path)
	if len(usd) != 2 || usd[0].C != 10 || usd[1].C != 22.5 || usd[1].T != before-3*86400 {
		t.Fatalf("USD history must use each day's FX and exclude the cursor: %+v", usd)
	}
	f.SetErr("SGDUSD=X", yahootest.NotFound("SGDUSD=X"))
	native := readHistoryCandles(t, mux, path+"&currency=original")
	if len(native) != 2 || native[0].O != 20 || native[0].C != 20 || native[1].C != 30 || native[0].V != 1000 || native[1].T != usd[1].T {
		t.Fatalf("original history must preserve native OHLC without FX: %+v", native)
	}
}

// A small historical page still needs Yahoo's full retained hourly window;
// a range sized only to that page would hide available bars past six months.
func TestCandleHistoryHourlyQuoteFeedWindow(t *testing.T) {
	old := time.Date(2025, 10, 5, 12, 0, 0, 0, time.UTC).Unix()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("range") != "2y" {
			fmt.Fprint(w, `{"chart":{"result":[{"meta":{"symbol":"HSDT","currency":"USD"},"timestamp":[],"indicators":{"quote":[{}]}}],"error":null}}`)
			return
		}
		fmt.Fprintf(w, `{"chart":{"result":[{"meta":{"symbol":"HSDT","currency":"USD"},"timestamp":[%d],"indicators":{"quote":[{"open":[20],"high":[22],"low":[19],"close":[21],"volume":[100]}]}}],"error":null}}`, old)
	}))
	t.Cleanup(srv.Close)
	c, _ := feedClient(t)
	c.Yahoo = yahoo.New(yahoo.Config{Base: srv.URL, Unpaced: true})
	mux := mount(c)
	for _, interval := range []string{"1h", "4h"} {
		got := readHistoryCandles(t, mux, fmt.Sprintf("/api/market/candles?symbol=HSDT&interval=%s&limit=1&before=%d", interval, old+3600))
		if len(got) != 1 || got[0].T != old || got[0].C != 21 {
			t.Errorf("%s history hidden by a short latest-page window: %+v", interval, got)
		}
	}
}

func readHistoryCandles(t *testing.T, mux *http.ServeMux, path string) []Candle {
	t.Helper()
	w := get(mux, context.Background(), path)
	var body struct{ Candles []Candle }
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != http.StatusOK {
		t.Fatalf("%s: %d %s / %v", path, w.Code, w.Body, err)
	}
	return body.Candles
}

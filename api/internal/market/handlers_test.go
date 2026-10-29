package market

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
)

// mount serves c's endpoints behind a stand-in session check that rejects
// requests without the X-Test-User header.
func mount(c *Client) *http.ServeMux {
	mux := http.NewServeMux()
	c.Register(mux, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Test-User") == "" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	return mux
}

func get(mux *http.ServeMux, ctx context.Context, path string) *httptest.ResponseRecorder {
	r := httptest.NewRequestWithContext(ctx, "GET", path, nil)
	r.Header.Set("X-Test-User", "u")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func TestHandlers(t *testing.T) {
	b := markettest.New(t)
	mux := mount(testMarketClient(b.URL))
	ctx := context.Background()

	for _, path := range []string{"/api/market/assets", "/api/market/candles?symbol=BTC&interval=1h", "/api/market/analysis?symbol=BTC"} {
		r := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Errorf("%s without a session: %d", path, w.Code)
		}
	}

	w := get(mux, ctx, "/api/market/assets")
	var assets struct{ Assets []Asset }
	json.Unmarshal(w.Body.Bytes(), &assets)
	if w.Code != 200 || len(assets.Assets) != 107 || assets.Assets[0].Symbol != "BTC" || assets.Assets[0].Price != 100_000 ||
		assets.Assets[11].Pair != "PAXGUSDT" || assets.Assets[39].Symbol != "STX" || assets.Assets[0].Volume24hUsd != 1e9 {
		t.Fatalf("assets: %d %s", w.Code, w.Body)
	}

	w = get(mux, ctx, "/api/market/candles?symbol=eth&interval=1h&limit=5")
	var candles struct{ Candles []Candle }
	json.Unmarshal(w.Body.Bytes(), &candles)
	if w.Code != 200 || len(candles.Candles) != 5 || candles.Candles[4].T != markettest.End.Unix() || candles.Candles[4].C != 4000 {
		t.Fatalf("candles: %d %s", w.Code, w.Body)
	}
	if w = get(mux, ctx, "/api/market/candles?symbol=SOL&interval=1d"); w.Code != 200 || strings.Count(w.Body.String(), `"t":`) != 500 {
		t.Fatalf("default limit 500: %d, %d candles", w.Code, strings.Count(w.Body.String(), `"t":`))
	}

	w = get(mux, ctx, "/api/market/analysis?symbol=btc")
	var an struct{ Analysis Analysis }
	json.Unmarshal(w.Body.Bytes(), &an)
	if a := an.Analysis; w.Code != 200 || a.Symbol != "BTC" || a.Interval != "4h" || a.Price != 100_000 || a.Sma200 == nil ||
		!(a.Support < a.Price && a.Price <= a.Resistance) || a.RealizedVolAnnPct <= 0 || a.Rsi14 <= 0 || a.Rsi14 >= 100 {
		t.Fatalf("analysis: %d %s", w.Code, w.Body)
	}

	for path, msg := range map[string]string{
		"/api/market/candles?symbol=PEPE&interval=1h":           "Unknown symbol.",
		"/api/market/candles?symbol=BTC&interval=5m":            "interval must be",
		"/api/market/candles?symbol=BTC&interval=1h&limit=0":    "limit must be 1..1000.",
		"/api/market/candles?symbol=BTC&interval=1h&limit=1001": "limit must be 1..1000.",
		"/api/market/candles?symbol=BTC&interval=1h&limit=ten":  "limit must be 1..1000.",
		"/api/market/analysis?symbol=PEPE":                      "Unknown symbol.",
		"/api/market/analysis?symbol=BTC&interval=2h":           "interval must be",
	} {
		if w := get(mux, ctx, path); w.Code != 400 || !strings.Contains(w.Body.String(), `"bad_request"`) || !strings.Contains(w.Body.String(), msg) {
			t.Errorf("%s: %d %s", path, w.Code, w.Body)
		}
	}

	// A client that gave up is a 499, not an upstream failure.
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if w := get(mount(testMarketClient(b.URL)), canceled, "/api/market/candles?symbol=BTC&interval=15m"); w.Code != 499 {
		t.Fatalf("canceled: %d %s", w.Code, w.Body)
	}

	b.Down("", true)
	down := mount(testMarketClient(b.URL)) // a fresh client: nothing cached
	for _, path := range []string{"/api/market/assets", "/api/market/candles?symbol=BTC&interval=1h", "/api/market/analysis?symbol=BTC&interval=1d"} {
		if w := get(down, ctx, path); w.Code != 502 || !strings.Contains(w.Body.String(), `"upstream"`) {
			t.Errorf("%s with Binance down: %d %s", path, w.Code, w.Body)
		}
	}
}

// The client caches briefly, so many callers make one upstream call.
func TestClientCaches(t *testing.T) {
	b := markettest.New(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Redirect(w, r, b.URL+r.URL.RequestURI(), http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	c := testMarketClient(srv.URL)
	c.HyperliquidBase = b.URL
	ctx := context.Background()
	for range 3 {
		if p, err := c.Price(ctx, "eth"); err != nil || p != 4000 {
			t.Fatalf("price: %v %v", p, err)
		}
		if _, err := c.Analyze(ctx, "ETH", "1d"); err != nil { // 1d: daily bars serve both series
			t.Fatal(err)
		}
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("%d upstream calls, want 2 (one ticker, one klines)", n)
	}
	if _, err := c.Price(ctx, "PEPE"); err == nil {
		t.Fatal("unknown symbol must fail")
	}
}

func TestClientRejects(t *testing.T) {
	b := markettest.New(t)
	c := testMarketClient(b.URL)
	ctx := context.Background()
	if _, err := c.Candles(ctx, "PEPE", "1h", 10); err == nil {
		t.Error("candles: unknown symbol")
	}
	if _, err := c.Candles(ctx, "BTC", "5m", 10); err == nil {
		t.Error("candles: unknown interval")
	}
	if _, err := c.Analyze(ctx, "BTC", "2h"); err == nil {
		t.Error("analyze: unknown interval")
	}
	if _, err := c.Outlook(ctx, "PEPE", 7); err == nil {
		t.Error("outlook: unknown symbol")
	}
	for days, want := range map[int]int{0: 1, 30: 30, 1000: 365} {
		if o, err := c.Outlook(ctx, "btc", days); err != nil || o.HorizonDays != want || o.Spot != 100_000 || o.Symbol != "BTC" {
			t.Errorf("outlook %d days: %+v %v", days, o, err)
		}
	}
	b.Down("SOLUSDT", true)
	if _, err := c.Analyze(ctx, "SOL", "4h"); !errors.Is(err, ErrUpstream) {
		t.Errorf("analyze with the pair down: %v", err)
	}
	if _, err := c.Outlook(ctx, "SOL", 7); !errors.Is(err, ErrUpstream) {
		t.Errorf("outlook with the pair down: %v", err)
	}
	if _, err := testMarketClient(b.URL).Analyze(ctx, "BTC", "4h"); err != nil {
		t.Errorf("analyze 4h fetches the interval and daily bars: %v", err)
	}
}

// Anything malformed from Binance is ErrUpstream, never a zero price.
func TestUpstreamFailures(t *testing.T) {
	ctx := context.Background()
	serve := func(body string, status int) string {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			fmt.Fprint(w, body)
		}))
		t.Cleanup(srv.Close)
		return srv.URL
	}
	var rows []string
	for _, a := range Universe {
		price := "1.5"
		if a.Symbol == "TON" {
			price = "0"
		}
		rows = append(rows, fmt.Sprintf(`{"symbol":%q,"lastPrice":%q}`, a.Pair, price))
	}
	for name, base := range map[string]string{
		"zero price":  serve("["+strings.Join(rows, ",")+"]", 200),
		"status 429":  serve(`{"code":-1003}`, 429),
		"not JSON":    serve(`<html>`, 200),
		"closed port": "http://127.0.0.1:1",
	} {
		if _, err := testMarketClient(base).Assets(ctx); !errors.Is(err, ErrUpstream) {
			t.Errorf("assets, %s: %v", name, err)
		}
	}
	for name, base := range map[string]string{
		"short kline": serve(`[[1700000000000,"1","1","1","1"]]`, 200),
		"no klines":   serve(`[]`, 200),
	} {
		if _, err := testMarketClient(base).Candles(ctx, "BTC", "1h", 10); !errors.Is(err, ErrUpstream) {
			t.Errorf("candles, %s: %v", name, err)
		}
	}
}

// TONUSDT's row as Binance answered it on 2026-10-05 (status BREAK): still
// quoted at its last trade, with a 24h count, but closeTime is 619 h old.
const haltedTON = `{"symbol":"TONUSDT","priceChange":"0.01500000","priceChangePercent":"0.946","weightedAvgPrice":"1.60765501",` +
	`"prevClosePrice":"0.00000000","lastPrice":"1.60000000","lastQty":"205.92000000","bidPrice":"0.00000000","bidQty":"0.00000000",` +
	`"askPrice":"0.00000000","askQty":"0.00000000","openPrice":"1.58500000","highPrice":"1.64100000","lowPrice":"1.58000000",` +
	`"volume":"4800378.41000000","quoteVolume":"7717352.41982000","openTime":1788847251019,"closeTime":1788933651019,` +
	`"firstId":109462494,"lastId":109548428,"count":85935}`

// A halted pair stays in Assets at its last price, flagged, core or not, so
// what users hold in it keeps its value; the public list leaves it out. A
// pair that traded under an hour ago is live, and so is a row without a
// closeTime (fail open).
func TestHaltedPair(t *testing.T) {
	rows := []string{haltedTON}
	for _, a := range Universe {
		switch a.Symbol {
		case "TON":
		case "ARB":
			rows = append(rows, fmt.Sprintf(`{"symbol":%q,"lastPrice":"1.5","count":9}`, a.Pair))
		default:
			rows = append(rows, fmt.Sprintf(`{"symbol":%q,"lastPrice":"1.5","count":9,"closeTime":%d}`, a.Pair, time.Now().Add(-50*time.Minute).UnixMilli()))
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "["+strings.Join(rows, ",")+"]") }))
	t.Cleanup(srv.Close)
	c := testMarketClient(srv.URL)
	ctx := context.Background()

	assets, err := c.Assets(ctx)
	if err != nil || len(assets) != len(CryptoUniverse) {
		t.Fatalf("assets: %d, %v", len(assets), err)
	}
	for _, a := range assets {
		if a.Halted != (a.Symbol == "TON") {
			t.Errorf("%s halted = %v", a.Symbol, a.Halted)
		}
	}
	if q, err := c.Quote(ctx, "ton"); err != nil || !q.Halted || q.Price != 1.6 || q.Change24hPct != 0.946 {
		t.Fatalf("TON quote: %+v %v", q, err)
	}
	if p, err := c.Price(ctx, "TON"); err != nil || p != 1.6 {
		t.Fatalf("TON price: %v %v", p, err)
	}

	w := get(mount(c), ctx, "/api/market/assets")
	var list struct{ Assets []Asset }
	json.Unmarshal(w.Body.Bytes(), &list)
	if w.Code != 200 || len(list.Assets) != len(CryptoUniverse)-1 || slices.ContainsFunc(list.Assets, func(a Asset) bool { return a.Symbol == "TON" }) ||
		strings.Contains(w.Body.String(), "halted") {
		t.Fatalf("public list: %d %s", w.Code, w.Body)
	}

	// A Binance-wide stop removes every crypto row; independent continuous perps remain available.
	b := markettest.New(t)
	b.Halt("", true)
	if w := get(mount(testMarketClient(b.URL)), ctx, "/api/market/assets"); w.Code != 200 || strings.Count(w.Body.String(), `"symbol":`) != len(StockPerps) || strings.Contains(w.Body.String(), `"class":"crypto"`) {
		t.Fatalf("all halted: %d %q", w.Code, w.Body)
	}
}

func TestEdgeHelpers(t *testing.T) {
	if pctBack([]float64{5}, 1) != 0 || pctBack([]float64{0, 5}, 1) != 0 {
		t.Error("pctBack without enough history or from zero must be 0")
	}
	if last(nil) != 0 || last([]float64{math.NaN()}) != 0 {
		t.Error("last of an empty or warming-up series must be 0")
	}
	if roundSig(0, 3) != 0 || roundSig(-1, 3) != -1 {
		t.Error("roundSig keeps non-positive values")
	}
	if _, ok := Lookup(" eth "); !ok || ValidInterval("5m") {
		t.Error("Lookup trims and ignores case; 5m is not an interval")
	}
	if testMarketClient("").base != "https://data-api.binance.vision" {
		t.Error("default base URL")
	}
	for _, s := range [][]float64{SMA([]float64{1, 2}, 0), EMA([]float64{1, 2}, 3), RSI([]float64{1}, 14), ROC([]float64{1, 2}, 0),
		ATR([]Candle{{C: 1}}, 14), Highest([]float64{1, 2}, 0)} {
		for _, x := range s {
			if !math.IsNaN(x) {
				t.Errorf("too little history must stay NaN: %v", s)
			}
		}
	}
}

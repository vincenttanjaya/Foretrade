package market

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
)

// A metaAndAssetCtxs answer with every number a string, as Hyperliquid sends
// it: BTC listed, ETH delisted, SOL absent, a non-universe coin and a
// lower-case name (not ours) mixed in.
const hlAnswer = `[{"universe":[{"szDecimals":5,"name":"BTC","maxLeverage":40},{"name":"kPEPE"},{"name":"ETH","isDelisted":true},{"name":"doge"}]},
	[{"funding":"0.0000125","openInterest":"12000.5","prevDayPx":"99000.0","dayNtlVlm":"2512345678.9","premium":"-0.0003","oraclePx":"100030.0","markPx":"100000.0","midPx":"100001.0","impactPxs":["99999","100002"]},
	 {"funding":"0.0001","openInterest":"1","premium":"0","markPx":"0.01","dayNtlVlm":"1"},
	 {"funding":"0.0001","openInterest":"5","premium":"0.001","markPx":"4000","dayNtlVlm":"1"},
	 {"funding":"0.0001","openInterest":"5","premium":null,"markPx":"0.2","dayNtlVlm":"1"}]]`

func TestDerivatives(t *testing.T) {
	var hits atomic.Int32
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || r.URL.Path != "/info" || string(body) != `{"type":"metaAndAssetCtxs"}` ||
			r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("request %s %s %s", r.Method, r.URL.Path, body)
		}
		if fail.Load() {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		io.WriteString(w, hlAnswer)
	}))
	t.Cleanup(srv.Close)
	c := testMarketClient("http://127.0.0.1:1")
	c.HyperliquidBase = srv.URL
	ctx := context.Background()

	d, err := c.Derivatives(ctx, "btc")
	if err != nil || d == nil {
		t.Fatalf("BTC: %+v %v", d, err)
	}
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-9*math.Max(1, math.Abs(b)) }
	if d.Symbol != "BTC" || d.Venue != "Hyperliquid" || d.MarkPrice != 100_000 || !near(d.FundingRatePct, 0.00125) ||
		!near(d.FundingAnnualizedPct, 0.00125*24*365) || !near(d.OpenInterestUsd, 12000.5*100_000) || !near(d.PremiumPct, -0.03) ||
		d.Volume24hUsd != 2512345678.9 || time.Since(d.AsOf) > time.Minute {
		t.Fatalf("BTC: %+v", d)
	}
	for _, sym := range []string{"ETH", "SOL", "DOGE"} { // delisted, absent, listed under another spelling
		if d, err := c.Derivatives(ctx, sym); err != nil || d != nil {
			t.Errorf("%s: %+v %v, want nil", sym, d, err)
		}
	}
	if _, err := c.Derivatives(ctx, "PEPE"); err == nil {
		t.Error("unknown symbol: want an error")
	}
	if hits.Load() != 1 {
		t.Fatalf("one answer serves every asset for 60 s: %d upstream calls", hits.Load())
	}

	// Expired, and Hyperliquid fails: the failure is cached too.
	fail.Store(true)
	c.perps.at = time.Now().Add(-derivativesTTL - time.Second)
	for range 3 {
		if d, err := c.Derivatives(ctx, "BTC"); !errors.Is(err, ErrUpstream) || d != nil {
			t.Fatalf("down: %+v %v", d, err)
		}
	}
	if hits.Load() != 2 {
		t.Fatalf("a failure is cached for 60 s: %d upstream calls", hits.Load())
	}
	fail.Store(false)
	c.perps.at = time.Now().Add(-derivativesTTL - time.Second)
	if d, err := c.Derivatives(ctx, "BTC"); err != nil || d == nil || hits.Load() != 3 {
		t.Fatalf("recovered: %+v %v (%d calls)", d, err, hits.Load())
	}

	// A malformed answer is an upstream error; a canceled caller is not cached.
	for _, body := range []string{`{}`, `[{"universe":[{"name":"BTC"}]},[]]`, `not json`} {
		bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }))
		c := testMarketClient("")
		c.HyperliquidBase = bad.URL
		if _, err := c.Derivatives(ctx, "BTC"); !errors.Is(err, ErrUpstream) {
			t.Errorf("%s: %v", body, err)
		}
		bad.Close()
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	fresh := testMarketClient("")
	fresh.HyperliquidBase = srv.URL
	if _, err := fresh.Derivatives(canceled, "BTC"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: %v", err)
	}
	if d, err := fresh.Derivatives(ctx, "BTC"); err != nil || d == nil {
		t.Fatalf("after a canceled call: %+v %v", d, err)
	}
}

func TestDerivativesHandler(t *testing.T) {
	b := markettest.New(t)
	c := testMarketClient(b.URL)
	c.HyperliquidBase = b.URL
	mux := mount(c)
	ctx := context.Background()

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/market/derivatives?symbol=BTC", nil))
	if w.Code != 401 {
		t.Fatalf("without a session: %d", w.Code)
	}
	w = get(mux, ctx, "/api/market/derivatives?symbol=sol")
	var body struct{ Derivatives *Derivatives }
	if err := json.Unmarshal(w.Body.Bytes(), &body); w.Code != 200 || err != nil || body.Derivatives == nil ||
		body.Derivatives.Symbol != "SOL" || body.Derivatives.FundingRatePct >= 0 || body.Derivatives.OpenInterestUsd != 12000*200 {
		t.Fatalf("SOL: %d %s", w.Code, w.Body)
	}
	for _, sym := range []string{"TON", "PAXG"} { // delisted, unlisted
		if w := get(mux, ctx, "/api/market/derivatives?symbol="+sym); w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"derivatives":null}` {
			t.Errorf("%s: %d %s", sym, w.Code, w.Body)
		}
	}
	if w := get(mux, ctx, "/api/market/derivatives?symbol=PEPE"); w.Code != 400 {
		t.Errorf("unknown symbol: %d %s", w.Code, w.Body)
	}

	// Hyperliquid down: null, never a 502.
	b.Down("", true)
	down := testMarketClient(b.URL)
	down.HyperliquidBase = b.URL
	if w := get(mount(down), ctx, "/api/market/derivatives?symbol=BTC"); w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"derivatives":null}` {
		t.Fatalf("upstream down: %d %s", w.Code, w.Body)
	}
}

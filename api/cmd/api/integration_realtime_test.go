package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/market/rtquote"
	"github.com/vincenttanjaya/Foretrade/api/internal/market/rtquote/rtquotetest"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo/yahootest"
)

// fakeNaver answers Naver's polling v2 path for every code asked: a fixed
// price per code and a KRX volume that grows with each answer (so every
// listing trades), stamped by the market's clock; or a refusal.
type fakeNaver struct {
	mu      sync.Mutex
	now     func() time.Time
	prices  map[string]int
	volume  int
	status  int
	headers []http.Header
	codes   int
}

func (f *fakeNaver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.headers = append(f.headers, r.Header.Clone())
	codes, ok := strings.CutPrefix(r.URL.Path, "/api/realtime/domestic/stock/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	if f.status != 0 {
		w.WriteHeader(f.status)
		return
	}
	f.volume += 10
	stamp := f.now().In(time.FixedZone("KST", 9*3600)).Format(time.RFC3339Nano)
	var rows []map[string]any
	for _, code := range strings.Split(codes, ",") {
		price := f.prices[code]
		if price == 0 {
			price = 100000
		}
		rows = append(rows, map[string]any{"itemCode": code, "closePriceRaw": fmt.Sprint(price),
			"accumulatedTradingVolumeRaw": fmt.Sprint(f.volume), "marketStatus": "OPEN", "marketSessionType": "regularMarket",
			"localTradedAt": stamp, "stockExchangeType": map[string]any{"code": "KS", "delayTime": 0},
			"tradeStopType": map[string]any{"name": "TRADING"}, "currencyType": map[string]any{"code": "KRW"}})
	}
	f.codes = len(rows)
	json.NewEncoder(w).Encode(map[string]any{"pollingInterval": 7000, "datas": rows, "time": "20261014100000"})
}

func (f *fakeNaver) set(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = status
}

// TestIntegrationRealtimeQuotes: KRX's real-time source through the real
// scheduler, guards and selection, on a market clock running 20× from 10:00
// KST on a trading Wednesday: /healthz names it, the Trade page's rows and
// the screener's watch row carry its price and labels, a paper fill uses
// it, the detail route reports it, every request was honest, and a refusal
// falls the venue back to Yahoo.
func TestIntegrationRealtimeQuotes(t *testing.T) {
	base, started := time.Date(2026, 10, 14, 1, 0, 0, 0, time.UTC), time.Now()
	clock := func() time.Time { return base.Add(20 * time.Since(started)) }
	naver := &fakeNaver{now: clock, prices: map[string]int{"035420": 250000, "005930": 238000}}
	srv := httptest.NewServer(naver)
	t.Cleanup(srv.Close)
	yahooAt := base.Add(-20 * time.Minute)
	s := boot(t, options{rt: &realtimeOpts{base: srv.URL, now: clock, tick: 20 * time.Millisecond}, yahoo: func(f *yahootest.Fake) {
		nv := yahootest.Daily("035420.KS", "KRW", 240000, 245000)
		prev := 240000.0
		nv.Meta.PriceTime, nv.Meta.ChartPreviousClose, nv.Meta.Timezone = &yahooAt, &prev, "Asia/Seoul"
		f.Set(nv)
		f.SetFX("KRW", 0.0007)
	}})
	anon := s.client(t)
	var header string
	eventually(t, 20*time.Second, "krx=naver ok in /healthz", func() bool {
		resp, _ := anon.send(anon.request("GET", "/healthz", nil))
		header = resp.Header.Get("X-Quote-Sources")
		return strings.HasPrefix(header, "krx=naver ok ")
	})
	if !strings.Contains(header, "; hkex=yahoo ") || !strings.Contains(header, "; hose=yahoo fallback -; sgx=yahoo ") ||
		!strings.Contains(header, "; twse=yahoo ") || strings.Count(header, ";") != 4 {
		t.Fatalf("X-Quote-Sources %q", header)
	}

	c := s.signIn(t)
	var row any
	eventually(t, 10*time.Second, "NAVER priced from naver", func() bool {
		for _, r := range list(t, c.call("GET", "/api/market/assets", nil, 200), "assets") {
			if str(t, r, "symbol") == "NAVER" {
				row = r
				return str(t, r, "quoteFeed.source") == "naver"
			}
		}
		return false
	})
	shape(t, row, "quoteFeed", quoteFeedKeys)
	if num(t, row, "quoteFeed.localPrice") != 250000 || !near(num(t, row, "price"), 250000*0.0007) || str(t, row, "quoteFeed.class") != "realtime" ||
		str(t, row, "quoteFeed.timeBasis") != "volume" || !strings.HasPrefix(str(t, row, "quoteFeed.label"), "real-time · ") ||
		num(t, row, "quoteFeed.delaySeconds") > 30 || num(t, row, "quoteFeed.previousClose") != 240000 ||
		isTime(t, row, "quoteFeed.lastTradeAt").Before(yahooAt) || isTime(t, row, "quoteFeed.lastTradeAt").After(isTime(t, row, "quoteFeed.readAt")) {
		t.Fatalf("NAVER %v", row)
	}
	fill := c.call("POST", "/api/orders", map[string]any{"symbol": "NAVER", "side": "buy", "qty": 1}, 200)
	if !near(num(t, fill, "trade.price"), paper.FillPrice(250000*0.0007, true)) {
		t.Fatalf("fill %v", fill)
	}
	// the screener's watch row of Samsung (SMSN trades as its perp) shares the one request
	var q any
	eventually(t, 10*time.Second, "005930.KS priced from naver", func() bool {
		rows := list(t, c.call("GET", "/api/screener/asia/quotes?tickers=005930.KS", nil, 200), "rows")
		if len(rows) != 1 || isNull(t, rows[0], "quote") {
			return false
		}
		q = rows[0]
		return str(t, q, "quote.source") == "naver"
	})
	shape(t, q, "quote", screenerQuoteKeys)
	if num(t, q, "price") != 238000 || str(t, q, "quote.tier") != "screener" || str(t, q, "quote.class") != "realtime" ||
		num(t, q, "quote.refreshSeconds") != 10 || !strings.HasPrefix(str(t, q, "quote.label"), "real-time · ") {
		t.Fatalf("Samsung %v", q)
	}
	detail := c.call("GET", "/api/market/quote-sources", nil, 200)
	shapeAll(t, detail, "venues", "venue source status newestTradeAgeSeconds session nextPollAt chain")
	krx := list(t, detail, "venues")[0]
	if str(t, krx, "venue") != "KRX" || str(t, krx, "status") != "ok" || str(t, krx, "source") != "naver" ||
		str(t, krx, "chain.0.state") != "ok" || num(t, krx, "chain.0.today.requests") < 2 || num(t, krx, "chain.0.pollSeconds") != 10 {
		t.Fatalf("detail %v", krx)
	}
	s.client(t).fail("GET", "/api/market/quote-sources", nil, 401, "unauthorized")
	naver.mu.Lock()
	for _, h := range naver.headers {
		if h.Get("User-Agent") != rtquote.UserAgent || h.Get("Referer") != "" || h.Get("Cookie") != "" || h.Get("Origin") != "" {
			t.Errorf("request headers %v", h)
		}
	}
	if naver.codes < 170 { // every KRX feed, listed and watch row and the canaries, in one request
		t.Errorf("%d codes in a request", naver.codes)
	}
	naver.mu.Unlock()

	// Naver refuses us: the venue falls back to Yahoo, and says so
	naver.set(http.StatusForbidden)
	eventually(t, 10*time.Second, "krx=yahoo fallback", func() bool {
		resp, _ := anon.send(anon.request("GET", "/healthz", nil))
		return strings.HasPrefix(resp.Header.Get("X-Quote-Sources"), "krx=yahoo fallback ")
	})
	eventually(t, 10*time.Second, "NAVER back on Yahoo", func() bool {
		for _, r := range list(t, c.call("GET", "/api/market/assets", nil, 200), "assets") {
			if str(t, r, "symbol") == "NAVER" {
				row = r
				return str(t, r, "quoteFeed.source") == "yahoo"
			}
		}
		return false
	})
	if num(t, row, "quoteFeed.localPrice") != 245000 || !strings.HasPrefix(str(t, row, "quoteFeed.label"), "delayed · last trade ") {
		t.Fatalf("fallback %v", row)
	}
	krx = list(t, c.call("GET", "/api/market/quote-sources", nil, 200), "venues")[0]
	if str(t, krx, "chain.0.state") != "refused" || num(t, krx, "chain.0.refusals") != 1 || num(t, krx, "chain.0.lastRefusal.status") != 403 {
		t.Fatalf("refusal %v", krx)
	}
	s.waitLog(t, "real-time source refused our request", "naver")
}

// TestIntegrationRealtimeVenues: the five venues' real-time sources on, as
// production runs them by default (QUOTES_REALTIME unset is all five), each
// adapter's request answered in its source's own format by one fake
// (rtquotetest.Board behind QUOTES_REALTIME_BASE), on a market clock running
// 20× from 10:58 SGT on a trading Wednesday, when all five trade: /healthz
// names all five sources ok, each venue's named feed is priced from its
// source with its label, a paper fill on HKEX uses Tencent's price, the
// detail route lists every chain, every request was honest and asked for
// every listing of its venue, and MIS refused (TWSE's F5 page) leaves TWSE
// to Yahoo Taiwan.
func TestIntegrationRealtimeVenues(t *testing.T) {
	base, started := time.Date(2026, 10, 14, 2, 58, 0, 0, time.UTC), time.Now()
	clock := func() time.Time { return base.Add(20 * time.Since(started)) }
	board := &rtquotetest.Board{Now: clock}
	for _, a := range slices.Concat(market.QuoteFeeds, market.Listed) {
		if a.Listing != nil && a.Listing.SessionVenue == market.SGX {
			board.SGX = append(board.SGX, strings.TrimSuffix(a.Pair, ".SI"))
		}
	}
	board.SetPrice("tencent", "r_hk00700", 430)
	srv := httptest.NewServer(board)
	t.Cleanup(srv.Close)
	s := boot(t, options{rt: &realtimeOpts{base: srv.URL, now: clock, tick: 20 * time.Millisecond, venues: rtquote.DefaultSwitches},
		yahoo: func(f *yahootest.Fake) {
			for ccy, usd := range map[string]float64{"KRW": 0.0007, "HKD": 0.128, "VND": 0.000038, "SGD": 0.77, "TWD": 0.031} {
				f.SetFX(ccy, usd)
			}
		}})
	anon := s.client(t)
	allOK := regexp.MustCompile(`^krx=naver ok \d+s; hkex=tencent ok \d+s; hose=vietcap ok \d+s; sgx=sgx ok \d+s; twse=mis ok \d+s$`)
	var header string
	eventually(t, 30*time.Second, "all five sources ok in /healthz", func() bool {
		resp, _ := anon.send(anon.request("GET", "/healthz", nil))
		header = resp.Header.Get("X-Quote-Sources")
		return allOK.MatchString(header)
	})

	c := s.signIn(t)
	feeds := map[string]struct{ source, class, label string }{
		"NAVER": {"naver", "realtime", "real-time · "}, "TENCENT": {"tencent", "realtime", "real-time · "},
		"VCB": {"vietcap", "realtime", "real-time · "}, "DBS": {"sgx", "near-realtime", "about "},
		"MEDIATEK": {"mis", "realtime", "real-time · "},
	}
	rows := map[string]any{}
	eventually(t, 10*time.Second, "each venue's named feed from its source", func() bool {
		for _, r := range list(t, c.call("GET", "/api/market/assets", nil, 200), "assets") {
			if w, ok := feeds[str(t, r, "symbol")]; ok && str(t, r, "quoteFeed.source") == w.source {
				rows[str(t, r, "symbol")] = r
			}
		}
		return len(rows) == len(feeds)
	})
	for symbol, w := range feeds {
		row := rows[symbol]
		shape(t, row, "quoteFeed", quoteFeedKeys)
		if str(t, row, "quoteFeed.class") != w.class || !strings.HasPrefix(str(t, row, "quoteFeed.label"), w.label) ||
			num(t, row, "quoteFeed.delaySeconds") > 180 || isTime(t, row, "quoteFeed.lastTradeAt").After(isTime(t, row, "quoteFeed.readAt")) {
			t.Errorf("%s %v", symbol, at(t, row, "quoteFeed"))
		}
	}
	if p := num(t, rows["TENCENT"], "quoteFeed.localPrice"); p != 430 {
		t.Fatalf("TENCENT at %v", p)
	}
	fill := c.call("POST", "/api/orders", map[string]any{"symbol": "TENCENT", "side": "buy", "qty": 1}, 200)
	if !near(num(t, fill, "trade.price"), paper.FillPrice(430*0.128, true)) {
		t.Fatalf("fill %v", fill)
	}

	venues := list(t, c.call("GET", "/api/market/quote-sources", nil, 200), "venues")
	if len(venues) != 5 {
		t.Fatalf("%d venues", len(venues))
	}
	for i, w := range []struct{ venue, source string }{{"KRX", "naver"}, {"HKEX", "tencent"}, {"HOSE", "vietcap"}, {"SGX", "sgx"}, {"TWSE", "mis"}} {
		v := venues[i]
		if str(t, v, "venue") != w.venue || str(t, v, "source") != w.source || str(t, v, "status") != "ok" || str(t, v, "session") != "open" ||
			str(t, v, "chain.0.source") != w.source || str(t, v, "chain.0.state") != "ok" || num(t, v, "chain.0.today.requests") < 1 {
			t.Errorf("detail %v", v)
		}
	}
	if twse := venues[4]; len(list(t, twse, "chain")) != 2 || str(t, twse, "chain.1.source") != "yahoo-tw" || str(t, twse, "chain.1.state") != "idle" {
		t.Fatalf("TWSE chain %v", twse)
	}

	least := map[string]int{"naver": 170, "tencent": 89, "vietcap": 26, "sgx": 31, "mis": 52}
	for _, r := range board.Requests() {
		if r.Status != http.StatusOK || r.Header.Get("User-Agent") != rtquote.UserAgent || r.Header.Get("Referer") != "" ||
			r.Header.Get("Cookie") != "" || r.Header.Get("Origin") != "" || r.Symbols < least[r.Source] {
			t.Errorf("%s %s %s: %d, %d symbols, %v", r.Source, r.Method, r.Path, r.Status, r.Symbols, r.Header)
		}
	}

	// MIS refuses us: TWSE moves to Yahoo Taiwan, real time still, and says so
	board.Refuse("mis", true)
	eventually(t, 10*time.Second, "twse=yahoo-tw fallback", func() bool {
		resp, _ := anon.send(anon.request("GET", "/healthz", nil))
		return regexp.MustCompile(`; twse=yahoo-tw fallback \d+s$`).MatchString(resp.Header.Get("X-Quote-Sources"))
	})
	var row any
	eventually(t, 10*time.Second, "MEDIATEK from yahoo-tw", func() bool {
		for _, r := range list(t, c.call("GET", "/api/market/assets", nil, 200), "assets") {
			if str(t, r, "symbol") == "MEDIATEK" {
				row = r
				return str(t, r, "quoteFeed.source") == "yahoo-tw"
			}
		}
		return false
	})
	if !strings.HasPrefix(str(t, row, "quoteFeed.label"), "real-time · ") {
		t.Fatalf("MEDIATEK %v", at(t, row, "quoteFeed"))
	}
	twse := list(t, c.call("GET", "/api/market/quote-sources", nil, 200), "venues")[4]
	if str(t, twse, "chain.0.state") != "refused" || num(t, twse, "chain.0.lastRefusal.status") != 502 ||
		!strings.Contains(str(t, twse, "chain.0.lastRefusal.reason"), "FOR SECURITY REASONS") || str(t, twse, "chain.1.state") != "ok" {
		t.Fatalf("refusal %v", twse)
	}
	s.waitLog(t, "real-time source refused our request", "mis")
}

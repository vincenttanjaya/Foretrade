package market

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHIP3PublicDefault(t *testing.T) {
	c := testMarketClient("http://127.0.0.1:1")
	c.HyperliquidBase = ""
	var hits atomic.Int32
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://api.hyperliquid.xyz/info" {
			t.Errorf("blank-base target %s", r.URL)
		}
		hits.Add(1)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`[{"universe":[{"name":"xyz:AAPL"}]},[{"markPx":"200","prevDayPx":"100","funding":null,"openInterest":"2","dayNtlVlm":"5"}]]`))}, nil
	})
	d, err := c.Derivatives(context.Background(), "AAPL")
	if err != nil || d == nil || d.MarkPrice != 200 || d.OpenInterestUsd != 400 || hits.Load() != 1 {
		t.Fatalf("default xyz routing: %+v %v hits%d", d, err, hits.Load())
	}
}

func TestHIP3InvalidMarksAndIdentity(t *testing.T) {
	for _, mark := range []string{"NaN", "+Inf", "-Inf", "0", "-1", "junk"} {
		t.Run(mark, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode([]any{map[string]any{"universe": []map[string]any{{"name": "xyz:AAPL"}}}, []map[string]any{{"markPx": mark, "prevDayPx": "NaN", "openInterest": "NaN", "dayNtlVlm": "Inf", "funding": "NaN"}}})
			}))
			defer s.Close()
			c := testMarketClient("http://127.0.0.1:1")
			c.HyperliquidBase = s.URL
			d, err := c.Derivatives(context.Background(), "AAPL")
			if err != nil || d != nil {
				t.Fatalf("invalid mark must be unavailable: %+v %v", d, err)
			}
		})
	}
	for _, body := range []string{`[{"universe":[{"name":"xyz:AAPL"}]},[]]`, `[{"universe":[{"name":"xyz:AAPL"},{"name":"xyz:AAPL"}]},[{"markPx":"200"},{"markPx":"201"}]]`} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }))
		c := testMarketClient("")
		c.HyperliquidBase = s.URL
		if _, err := c.xyzMarkets(context.Background()); !errors.Is(err, ErrUpstream) {
			t.Errorf("malformed aligned identity accepted: %s %v", body, err)
		}
		s.Close()
	}
}

func TestHIP3HistoryAdmissionAndCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		var active, peak atomic.Int32
		c := testMarketClient("http://127.0.0.1:1")
		c.HyperliquidBase = "https://fake.example"
		c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			n := active.Add(1)
			defer active.Add(-1)
			for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
			}
			select {
			case <-release:
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`[]`))}, nil
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan struct{})
		go func() { c.dayRanges(ctx); close(done) }()
		synctest.Wait()
		if peak.Load() != 3 {
			t.Errorf("HIP3 history peak %d, maximum3", peak.Load())
		}
		cancel()
		close(release)
		<-done
		if active.Load() != 0 {
			t.Fatalf("owner returned with %d HTTP owners", active.Load())
		}
		if _, err := c.fetchPerpCandles(context.Background(), "xyz:AAPL", "1h", 1, time.Now().UnixMilli()); err != nil {
			t.Fatal(err)
		}
	})
}

func TestHIP3FreshAssetsRefreshesXYZMark(t *testing.T) {
	b := markettest.New(t)
	c := testMarketClient(b.URL)
	ctx := context.Background()
	if _, err := c.Assets(ctx); err != nil {
		t.Fatal(err)
	}
	b.SetPrice("xyz:AAPL", 400)
	rows, err := c.FreshAssets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range rows {
		if a.Symbol == "AAPL" {
			if a.Price != 400 {
				t.Fatalf("fresh execution mark reused oldxyz context: %g", a.Price)
			}
			return
		}
	}
	t.Fatal("AAPL missing")
}

func TestHIP3SharedContextsAndIsolation(t *testing.T) {
	b := markettest.New(t)
	c := testMarketClient(b.URL)
	ctx := context.Background()
	if _, err := c.Assets(ctx); err != nil {
		t.Fatal(err)
	}
	for _, a := range StockPerps {
		if d, err := c.Derivatives(ctx, a.Symbol); err != nil || d == nil {
			t.Fatalf("%s %+v %v", a.Symbol, d, err)
		}
	}
	if b.Requests("xyz:meta") != 1 {
		t.Fatalf("quotes/derivatives context calls%d", b.Requests("xyz:meta"))
	}
	b.Delist("xyz:AAPL", true)
	b.SetPrice("xyz:NVDA", 0)
	b.Down("xyz:META", true)
	fresh := testMarketClient(b.URL)
	for _, symbol := range []string{"AAPL", "NVDA", "META"} {
		if _, err := fresh.Quote(ctx, symbol); !errors.Is(err, ErrUpstream) {
			t.Fatalf("unavailable %s accepted: %v", symbol, err)
		}
	}
	b.Down("xyz", true)
	if rows, err := testMarketClient(b.URL).Assets(ctx); err != nil || len(rows) != 69 {
		t.Fatalf("xyz outage removed Binance %d %v", len(rows), err)
	}
}

func candleRow(open int64) map[string]any {
	return map[string]any{"t": open, "T": open + 3600000 - 1, "s": "xyz:AAPL", "i": "1h", "o": "100", "h": "110", "l": "90", "c": "105", "v": "10"}
}

func TestHIP3CandleTimestampFields(t *testing.T) {
	b := markettest.New(t)
	at := time.Now().Truncate(time.Hour).UnixMilli()
	b.SetCandleRows("xyz:AAPL", []map[string]any{candleRow(at)})
	cs, err := testMarketClient(b.URL).Candles(context.Background(), "AAPL", "1h", 10)
	if err != nil || len(cs) != 1 || cs[0].T != at/1000 {
		t.Fatalf("open vsclose timestamp: %+v %v", cs, err)
	}
}

func TestHIP3CandlesBeforeRetention(t *testing.T) {
	b := markettest.New(t)
	b.SetRetention("xyz:AAPL", 5)
	c := testMarketClient(b.URL)
	cs, err := c.CandlesBefore(context.Background(), "AAPL", "1d", markettest.End.AddDate(0, 0, -10), 1000)
	if err != nil || len(cs) != 0 {
		t.Fatalf("synthetic pre-retention history %+v %v", cs, err)
	}
}

func TestHIP3CandleValidation(t *testing.T) {
	at := time.Now().Truncate(time.Hour).UnixMilli()
	for _, tc := range []struct {
		field string
		value any
	}{{"o", "NaN"}, {"c", "+Inf"}, {"v", "-1"}, {"h", "80"}, {"l", "120"}, {"s", "xyz:NVDA"}, {"i", "1d"}, {"t", 0}, {"T", at - 1}} {
		t.Run(tc.field+":"+stringify(tc.value), func(t *testing.T) {
			b := markettest.New(t)
			row := candleRow(at)
			row[tc.field] = tc.value
			b.SetCandleRows("xyz:AAPL", []map[string]any{row})
			if _, err := testMarketClient(b.URL).Candles(context.Background(), "AAPL", "1h", 10); !errors.Is(err, ErrUpstream) {
				t.Fatalf("invalid candle accepted: %v", err)
			}
		})
	}
	b := markettest.New(t)
	b.SetCandleRows("xyz:AAPL", []map[string]any{candleRow(at), candleRow(at - 3600000), candleRow(at), candleRow(at + 3600000)})
	cs, err := testMarketClient(b.URL).CandlesBefore(context.Background(), "AAPL", "1h", time.UnixMilli(at+1000), 10)
	if err != nil || len(cs) != 2 || cs[0].T != (at-3600000)/1000 || cs[1].T != at/1000 {
		t.Fatalf("order/dedupe/end %+v %v", cs, err)
	}
}

func stringify(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestHIP3QuoteRangeUnavailable(t *testing.T) {
	b := markettest.New(t)
	for _, a := range StockPerps {
		b.SetCandleRows(a.Pair, []map[string]any{})
	}
	q, err := testMarketClient(b.URL).Quote(context.Background(), "AAPL")
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	raw, _ := json.Marshal(q)
	json.Unmarshal(raw, &fields)
	if q.High24h != 0 || q.Low24h != 0 || fields["range24hSource"] != "unavailable" {
		t.Fatalf("invented24h range from mark/prev instead of unavailable: %s", raw)
	}
}

func TestHIP3DerivedOverflowIsolation(t *testing.T) {
	for _, tc := range []struct {
		name, field, value string
		badQuote           bool
	}{
		{"change", "markPx", "1e308", true},
		{"open interest", "openInterest", "1e308", false},
		{"annualized funding", "funding", "1e308", false},
		{"premium", "premium", "1e308", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := markettest.New(t)
			b.SetContext("xyz:AAPL", tc.field, tc.value)
			if tc.badQuote {
				b.SetContext("xyz:AAPL", "prevDayPx", "1e-308")
			}
			c := testMarketClient(b.URL)
			rows, err := c.Assets(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := json.Marshal(rows); err != nil {
				t.Errorf("bad xyz scalar made mixed DTO unencodable: %v", err)
			}
			found, core := false, 0
			for _, a := range rows {
				if a.Symbol == "AAPL" {
					found = true
				}
				for _, base := range Core {
					if a.Symbol == base.Symbol {
						core++
					}
				}
			}
			if core != len(Core) || found == tc.badQuote {
				t.Errorf("isolation healthyCore=%d AAPL=%v badQuote=%v", core, found, tc.badQuote)
			}
			d, err := c.Derivatives(context.Background(), "AAPL")
			if err != nil || d != nil {
				t.Errorf("overflow derivative must be unavailable: %+v %v", d, err)
			}
			// A context decoration failure cannot remove healthy BTC price data.
			if price, err := c.Price(context.Background(), "BTC"); err != nil || price != markettest.Price("BTCUSDT") {
				t.Fatalf("healthyCore damaged: %g %v", price, err)
			}
		})
	}
}

// Characterize the existing shared admission across overlapping callers;
// this preservation proof does not claim a new failure in the reviewed guard.
func TestHIP3SharedAdmissionAcrossRangeCandlesAndPortfolio(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		var active, peak atomic.Int32
		c := testMarketClient("http://127.0.0.1:1")
		c.HyperliquidBase = "https://fake.example"
		c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			n := active.Add(1)
			defer active.Add(-1)
			for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
			}
			select {
			case <-release:
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`[]`))}, nil
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var owners sync.WaitGroup
		owners.Go(func() { c.dayRanges(ctx) })
		// Ordinary candle and historical portfolio-shaped batches share this client.
		owners.Go(func() { c.Candles(ctx, "AAPL", "1d", 180) })
		for _, symbol := range []string{"NVDA", "TSLA", "META"} {
			owners.Go(func() { c.CandlesBefore(ctx, symbol, "4h", time.Now(), 1000) })
		}
		synctest.Wait()
		if active.Load() != 3 || peak.Load() != 3 {
			t.Fatalf("cross-path shared capacity: active=%d peak=%d", active.Load(), peak.Load())
		}
		cancel()
		synctest.Wait()
		owners.Wait()
		if active.Load() != 0 || len(c.historySlots) != 0 {
			t.Fatalf("owners/capacity abandoned: active=%d slots=%d", active.Load(), len(c.historySlots))
		}
		close(release)
		if _, err := c.CandlesBefore(context.Background(), "AAPL", "1h", time.Now(), 1); err != nil {
			t.Fatalf("capacity not reusable: %v", err)
		}
		if active.Load() != 0 || len(c.historySlots) != 0 {
			t.Fatal("reused operation failed to release capacity")
		}
	})
}

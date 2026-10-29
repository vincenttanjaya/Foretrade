package market

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestCandlesCacheRevalidatesAtIntervalBoundary(t *testing.T) {
	for _, tc := range []struct {
		interval string
		period   time.Duration
	}{{"15m", 15 * time.Minute}, {"1h", time.Hour}, {"4h", 4 * time.Hour}, {"1d", 24 * time.Hour}} {
		t.Run(tc.interval, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				time.Sleep(tc.period - 5*time.Second)
				origin := time.Now()
				boundary := origin.Add(5 * time.Second)
				var hits atomic.Int32
				server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					hits.Add(1)
					close := "100"
					if !time.Now().Before(boundary) {
						close = "200"
					}
					json.NewEncoder(w).Encode([][]any{{boundary.Add(-tc.period).UnixMilli(), "100", close, "100", close, "1"}})
				}))
				defer server.Close()
				client := testMarketClient(server.URL)
				// Only this package's test swaps the private client for the standard
				// in-memory HTTP network; production constructors are unchanged.
				client.http = server.Client()
				read := func() []Candle {
					bars, err := client.Candles(context.Background(), "BTC", tc.interval, 1)
					if err != nil || len(bars) != 1 {
						t.Fatalf("latest candles: bars=%v err=%v", bars, err)
					}
					return bars
				}
				if bars := read(); bars[0].C != 100 || hits.Load() != 1 {
					t.Fatalf("open source: bars=%v hits=%d", bars, hits.Load())
				}
				time.Sleep(4 * time.Second)
				if bars := read(); bars[0].C != 100 || hits.Load() != 1 {
					t.Fatalf("same-bar cache: bars=%v hits=%d", bars, hits.Load())
				}
				time.Sleep(time.Second)
				if bars := read(); bars[0].C != 200 || hits.Load() != 2 || time.Since(origin) != 5*time.Second {
					t.Fatalf("close must revalidate within TTL: bars=%v hits=%d age=%s", bars, hits.Load(), time.Since(origin))
				}
				if bars := read(); bars[0].C != 200 || hits.Load() != 2 {
					t.Fatalf("completed-row cache: bars=%v hits=%d", bars, hits.Load())
				}
			})
		})
	}
}

func TestCandlesPreCloseFetchCannotExtendCachePastClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		time.Sleep(time.Hour - 5*time.Second)
		origin := time.Now()
		boundary := origin.Add(5 * time.Second)
		var hits atomic.Int32
		server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close := "200"
			if hits.Add(1) == 1 {
				if !time.Now().Before(boundary) {
					t.Error("first request did not observe the open source")
				}
				close = "100" // provisional value observed before close, delayed in transit
				time.Sleep(6 * time.Second)
			}
			json.NewEncoder(w).Encode([][]any{{boundary.Add(-time.Hour).UnixMilli(), "100", close, "100", close, "1"}})
		}))
		defer server.Close()
		client := testMarketClient(server.URL)
		client.http = server.Client()
		ctx := context.Background()
		first, err := client.Candles(ctx, "BTC", "1h", 1)
		if err != nil || len(first) != 1 || first[0].C != 100 || time.Since(origin) != 6*time.Second {
			t.Fatalf("delayed provisional response: bars=%v age=%s err=%v", first, time.Since(origin), err)
		}
		final, err := client.Candles(ctx, "BTC", "1h", 1)
		if err != nil || len(final) != 1 || final[0].C != 200 || hits.Load() != 2 {
			t.Fatalf("pre-close fetch extended provisional cache after close: bars=%v hits=%d err=%v", final, hits.Load(), err)
		}
	})
}

package strategy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
)

// The real cache and handler share synctest's isolated clock: this is a ten-second
// cache hit across an hourly close, not a handler-only jump beyond the cache TTL.
func TestForwardRevalidatesCachedOpenCandleAtClose(t *testing.T) {
	var boundary atomic.Int64
	var finalized atomic.Bool
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		// No client connection stays open inside the clock bubble while it sleeps.
		w.Header().Set("Connection", "close")
		rows := make([]market.Candle, 0, 120)
		for i := int64(-120); i < 0; i++ {
			close := 100.0
			if i == -1 && finalized.Load() {
				close = 200
			}
			rows = append(rows, market.Candle{T: boundary.Load() + i*3600,
				O: 100, H: close, L: 100, C: close, V: 1})
		}
		json.NewEncoder(w).Encode(candleRows(rows))
	}))
	defer server.Close()

	synctest.Test(t, func(t *testing.T) {
		time.Sleep(time.Hour - 5*time.Second)
		origin := time.Now()
		boundary.Store(origin.Unix() + 5)
		ctx := context.Background()
		client := testMarketClient(server.URL)
		warm, err := client.Candles(ctx, "BTC", "1h", 120)
		if err != nil {
			t.Fatal(err)
		}
		if warm[len(warm)-1].T+3600 <= origin.Unix() || len(orderedClosed(warm, 3600, origin.Unix())) != 119 {
			t.Fatal("cache was not populated with a genuinely open final row")
		}
		time.Sleep(4 * time.Second)
		if _, err := client.Candles(ctx, "BTC", "1h", 120); err != nil || hits.Load() != 1 {
			t.Fatalf("pre-close cache was not reused: hits=%d err=%v", hits.Load(), err)
		}
		time.Sleep(time.Second)
		finalized.Store(true)
		time.Sleep(5 * time.Second)
		if age := time.Since(origin); age != 10*time.Second {
			t.Fatalf("cache age=%s, want 10s within the 30s TTL", age)
		}
		s := Strategy{ID: "cached-open-boundary", Spec: crossSpec(1, false),
			CreatedAt: time.Unix(boundary.Load()-4*3600, 0), Community: &CommunityInfo{}}
		h := &handler{d: &app.Deps{Market: client}}
		got := h.forward(ctx, s)
		fresh := &handler{d: &app.Deps{Market: testMarketClient(server.URL)}}
		truth := fresh.forward(ctx, s)
		if truth.Metrics == nil || truth.Metrics.BuyHoldReturnPct != 100 {
			t.Fatalf("finalized HTTP truth is not the hand-derived 100%% return: %+v", truth)
		}
		if got.Metrics == nil || got.AsOf == nil || got.AsOf.Unix() != boundary.Load() || got.Stale || got.Reason != nil {
			t.Fatalf("completed source must produce fresh measured handler metadata: %+v", got)
		}
		if got.Metrics.BuyHoldReturnPct != truth.Metrics.BuyHoldReturnPct || hits.Load() != 3 {
			t.Fatalf("cached open row was promoted instead of revalidated: cached B&H=%v final B&H=%v asOf=%s stale=%v hits=%d cacheAge=%s",
				got.Metrics.BuyHoldReturnPct, truth.Metrics.BuyHoldReturnPct, got.AsOf.Format(time.RFC3339), got.Stale, hits.Load(), time.Since(origin))
		}
		if again := h.forward(ctx, s); again.Metrics.BuyHoldReturnPct != 100 || hits.Load() != 3 {
			t.Fatalf("completed forward result was not retained: %+v hits=%d", again, hits.Load())
		}
	})
}

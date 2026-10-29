package market

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Err remains the ordinary context check. Done observes admission to a
// cancellable wait, so cancellation cannot happen before load reaches it.
type cacheWaitContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (c *cacheWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Done()
}

func TestCacheCanceledWaiterDoesNotWaitForLeader(t *testing.T) {
	for _, kind := range []string{"assets", "candles"} {
		t.Run(kind, func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			var hits atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/info" {
					http.Error(w, "xyz unavailable", 502)
					return
				}
				if hits.Add(1) == 1 {
					close(started)
				}
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				if kind == "assets" {
					rows := []map[string]any{}
					for _, a := range Universe {
						rows = append(rows, map[string]any{"symbol": a.Pair, "lastPrice": "100"})
					}
					json.NewEncoder(w).Encode(rows)
				} else {
					json.NewEncoder(w).Encode([]any{[]any{time.Now().UnixMilli(), "100", "110", "90", "105", "10"}})
				}
			}))
			t.Cleanup(server.Close)
			client := testMarketClient(server.URL)
			load := func(ctx context.Context) error {
				if kind == "assets" {
					_, err := client.Assets(ctx)
					return err
				}
				_, err := client.Candles(ctx, "BTC", "1d", 300)
				return err
			}
			ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			leader := make(chan error, 1)
			go func() { leader <- load(ctx) }()
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("leader did not reach the controlled upstream")
			}
			waitCtx, cancel := context.WithCancel(ctx)
			observed := &cacheWaitContext{Context: waitCtx, entered: make(chan struct{})}
			waiter := make(chan error, 1)
			go func() { waiter <- load(observed) }()
			select {
			case <-observed.entered:
			case <-time.After(250 * time.Millisecond):
				t.Error("caller did not enter a cancellable wait behind the leader")
			}
			cancel()
			returned := false
			select {
			case err := <-waiter:
				returned = true
				if !errors.Is(err, context.Canceled) {
					t.Errorf("canceled waiter: %v", err)
				}
			case <-time.After(250 * time.Millisecond):
				t.Error("canceled waiter remained behind the active cache fetch")
			}
			close(release)
			if err := <-leader; err != nil {
				t.Fatal(err)
			}
			if !returned {
				select {
				case <-waiter:
				case <-ctx.Done():
					t.Fatal("waiter did not join after releasing the leader")
				}
			}
			if err := load(ctx); err != nil || hits.Load() != 1 {
				t.Fatalf("canceled waiter damaged healthy singleflight/cache: hits=%d err=%v", hits.Load(), err)
			}
		})
	}
}

func TestCachePreCanceledCallerCannotUseWarmValue(t *testing.T) {
	client := testMarketClient("http://127.0.0.1:1")
	client.assets.val, client.assets.at = []Asset{{Symbol: "BTC", Price: 100}}, time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if v, err := client.Assets(ctx); !errors.Is(err, context.Canceled) || len(v) != 0 {
		t.Fatalf("canceled warm read returned owned data: %v %v", v, err)
	}
}

func TestCacheCanceledLeaderDoesNotPublishLateValue(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var entry cached[int]
	value, err := entry.get(ctx, time.Minute, func() (int, error) {
		cancel() // a successful response arriving after its owner canceled
		return 42, nil
	})
	if !errors.Is(err, context.Canceled) || value != 0 || !entry.at.IsZero() {
		t.Fatalf("canceled leader published late result: value=%d at=%s err=%v ctx=%v", value, entry.at, err, ctx.Err())
	}
}

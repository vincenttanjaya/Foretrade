package market

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func freshTickerRows(price string) []map[string]any {
	rows := make([]map[string]any, 0, len(Universe))
	for _, asset := range Universe {
		rows = append(rows, map[string]any{"symbol": asset.Pair, "lastPrice": price})
	}
	return rows
}

func TestFreshAssetsWaitersCanCancel(t *testing.T) {
	for _, mode := range []string{"fresh leader normal waiter", "normal leader fresh waiter", "fresh leader fresh waiter"} {
		t.Run(mode, func(t *testing.T) {
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
					json.NewEncoder(w).Encode(freshTickerRows("100"))
				case <-r.Context().Done():
				}
			}))
			defer server.Close()
			client := testMarketClient(server.URL)
			load := func(ctx context.Context, fresh bool) error {
				if fresh {
					_, err := client.FreshAssets(ctx)
					return err
				}
				_, err := client.Assets(ctx)
				return err
			}
			ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			leader := make(chan error, 1)
			go func() { leader <- load(ctx, strings.HasPrefix(mode, "fresh")) }()
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("leader did not reach upstream")
			}
			waitCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			observed := &cacheWaitContext{Context: waitCtx, entered: make(chan struct{})}
			waiter := make(chan error, 1)
			go func() { waiter <- load(observed, strings.HasSuffix(mode, "fresh waiter")) }()
			select {
			case <-observed.entered:
			case <-time.After(250 * time.Millisecond):
				t.Error("waiter cannot observe cancellation during fresh quote loading")
			}
			cancel()
			returned := false
			select {
			case err := <-waiter:
				returned = true
				if !errors.Is(err, context.Canceled) {
					t.Errorf("canceled waiter returned %v", err)
				}
			case <-time.After(250 * time.Millisecond):
				t.Error("canceled waiter remained blocked by quote loading")
			}
			close(release)
			if err := <-leader; err != nil {
				t.Fatal(err)
			}
			if !returned {
				select {
				case <-waiter:
				case <-ctx.Done():
					t.Fatal("waiter did not join after releasing leader")
				}
			}
			assets, err := client.Assets(ctx)
			if err != nil || len(assets) == 0 || assets[0].Price != 100 || hits.Load() != 1 {
				t.Fatalf("canceled waiter changed healthy quote cache: assets=%v hits=%d err=%v", assets, hits.Load(), err)
			}
		})
	}
}

func TestFreshAssetsFetchesAfterInflightQuote(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/info" {
			http.Error(w, "xyz unavailable", 502)
			return
		}
		price := "200"
		if hits.Add(1) == 1 {
			price = "100" // quote observed before the post-inference fresh call
			close(started)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		json.NewEncoder(w).Encode(freshTickerRows(price))
	}))
	defer server.Close()
	client := testMarketClient(server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	leader := make(chan error, 1)
	go func() { _, err := client.Assets(ctx); leader <- err }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("old quote did not reach upstream")
	}
	observed := &cacheWaitContext{Context: ctx, entered: make(chan struct{})}
	fresh := make(chan []Asset, 1)
	freshErr := make(chan error, 1)
	go func() { assets, err := client.FreshAssets(observed); fresh <- assets; freshErr <- err }()
	select {
	case <-observed.entered:
	case <-time.After(250 * time.Millisecond):
		t.Error("fresh quote cannot join in-flight quote with a cancellable wait")
	}
	close(release)
	if err := <-leader; err != nil {
		t.Fatal(err)
	}
	assets, err := <-fresh, <-freshErr
	if err != nil || len(assets) == 0 || assets[0].Price != 200 {
		t.Fatalf("fresh execution quote reused the earlier in-flight response: assets=%v err=%v", assets, err)
	}
	assets, err = client.Assets(ctx)
	if err != nil || len(assets) == 0 || assets[0].Price != 200 || hits.Load() != 2 {
		t.Fatalf("ordered fresh publication lost: assets=%v hits=%d err=%v", assets, hits.Load(), err)
	}
}

type freshRoundTripFunc func(*http.Request) (*http.Response, error)

func (f freshRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type freshCancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b freshCancelBody) Close() error {
	b.cancel() // a valid decoded response whose owner canceled before cache publication
	return b.ReadCloser.Close()
}

func TestFreshAssetsCanceledOwnerCannotPublish(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/info" {
			http.Error(w, "xyz unavailable", 502)
			return
		}
		json.NewEncoder(w).Encode(freshTickerRows("100"))
	}))
	defer server.Close()
	client := testMarketClient(server.URL)
	if _, err := client.Assets(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body, err := json.Marshal(freshTickerRows("200"))
	if err != nil {
		t.Fatal(err)
	}
	client.http.Transport = freshRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Request: r,
			Body: freshCancelBody{ReadCloser: io.NopCloser(strings.NewReader(string(body))), cancel: cancel}}, nil
	})
	assets, err := client.FreshAssets(ctx)
	if !errors.Is(err, context.Canceled) || len(assets) != 0 {
		t.Errorf("canceled fresh owner returned a successful quote: assets=%v err=%v", assets, err)
	}
	assets, err = client.Assets(context.Background())
	if err != nil || len(assets) == 0 || assets[0].Price != 100 {
		t.Fatalf("canceled fresh owner replaced the last healthy snapshot: assets=%v err=%v", assets, err)
	}
}

package research

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestBraveUsage(t *testing.T) {
	s := New(Config{BraveAPIKey: "k", DailyLimit: 7}, nil, nil)
	if used, limit := s.BraveUsage(); used != 0 || limit != 7 {
		t.Fatalf("fresh: %d/%d", used, limit)
	}
	if err := s.brave.take(context.Background()); err != nil {
		t.Fatal(err)
	}
	if used, _ := s.BraveUsage(); used != 1 {
		t.Fatalf("after one call: %d", used)
	}
	if left, limit := s.BraveLeft(); left != 6 || limit != 7 {
		t.Fatalf("left after one call: %d/%d", left, limit)
	}
	s.brave.day = "2000-01-01" // yesterday's count is not today's
	if used, _ := s.BraveUsage(); used != 0 {
		t.Fatalf("stale day: %d", used)
	}
	if left, _ := s.BraveLeft(); left != 7 {
		t.Fatalf("a new day resets what is left: %d", left)
	}
}

func TestCrawlerCancelledWaiterDoesNotWaitForProbeOwner(t *testing.T) {
	entered, release, upstreamDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	open := sync.OnceFunc(func() { close(release) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(upstreamDone)
		close(entered)
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()
	defer open()
	s := New(Config{CrawlBase: srv.URL, CrawlToken: "test"}, nil, nil)
	holder := make(chan CrawlerStatus, 1)
	go func() { holder <- s.Crawler(t.Context()) }()
	t.Cleanup(func() {
		open()
		select {
		case <-holder:
		case <-time.After(5 * time.Second):
			t.Error("actual probe owner did not exit")
		}
		select {
		case <-upstreamDone:
		case <-time.After(5 * time.Second):
			t.Error("fake probe driver did not exit")
		}
	})
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("actual probe not entered")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	waiter := make(chan CrawlerStatus, 1)
	go func() { waiter <- s.Crawler(ctx) }()
	t.Cleanup(func() {
		open()
		select {
		case <-waiter:
		case <-time.After(5 * time.Second):
			t.Error("probe waiter did not exit")
		}
	})
	select {
	case st := <-waiter:
		waiter <- st
		if st.Reachable != nil {
			t.Fatal("canceled waiter invented health result")
		}
	case <-time.After(time.Second):
		t.Fatal("canceled probe waiter blocked on health owner")
	}
}

func TestCrawlerStatus(t *testing.T) {
	ctx := context.Background()
	if st := New(Config{CrawlBase: "https://crawl.example"}, nil, nil).Crawler(ctx); st.Configured || st.Reachable != nil ||
		st.Base != "https://crawl.example" {
		t.Fatalf("no token: %+v", st)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	s := New(Config{CrawlBase: srv.URL, CrawlToken: "t"}, nil, nil)
	if st := s.Crawler(ctx); !st.Configured || st.Reachable == nil || !*st.Reachable {
		t.Fatalf("up: %+v", st)
	}
	srv.Close()
	if st := s.Crawler(ctx); !*st.Reachable {
		t.Fatal("the probe is cached for a minute")
	}
	s.health.at = time.Time{}
	if st := s.Crawler(ctx); *st.Reachable {
		t.Fatal("down after the cache expires")
	}
	if st := New(Config{CrawlBase: "http://bad host", CrawlToken: "t"}, nil, nil).Crawler(ctx); *st.Reachable {
		t.Fatal("an invalid base is unreachable")
	}
}

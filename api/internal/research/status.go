package research

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// BraveUsage is the Brave calls spent this UTC day and the daily budget,
// process-wide (in memory: a restart resets it).
func (s *Service) BraveUsage() (used, limit int) {
	b := s.brave
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.day != time.Now().UTC().Format(time.DateOnly) {
		return 0, b.limit
	}
	return b.used, b.limit
}

// BraveLeft is the Brave calls left this UTC day and the daily budget: the
// forecast desk's deep research checks it before spending a run's searches.
func (s *Service) BraveLeft() (left, limit int) {
	used, limit := s.BraveUsage()
	return max(limit-used, 0), limit
}

// CrawlerStatus is the admin console's view of crawl4ai.
type CrawlerStatus struct {
	Base       string `json:"base"`
	Configured bool   `json:"configured"` // base and token set
	Reachable  *bool  `json:"reachable"`  // GET {base}/health answered 2xx within 3 s; nil when not configured
}

// health caches the crawler probe for a minute.
type health struct {
	mu      sync.Mutex // protects publication; never held while probing or waiting
	at      time.Time
	ok      bool
	loading chan struct{}
}

// Crawler probes GET {CRAWL_BASE}/health (3 s, cached 60 s).
func (s *Service) Crawler(ctx context.Context) CrawlerStatus {
	c := s.crawl
	base, token := c.target()
	st := CrawlerStatus{Base: base, Configured: base != "" && token != ""}
	if !st.Configured {
		return st
	}
	h := &s.health
	for {
		h.mu.Lock()
		if time.Since(h.at) <= time.Minute {
			ok := h.ok
			h.mu.Unlock()
			st.Reachable = &ok
			return st
		}
		if h.loading != nil {
			done := h.loading
			h.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return st
			}
		}
		if ctx.Err() != nil {
			h.mu.Unlock()
			return st
		}
		done := make(chan struct{})
		h.loading = done
		h.mu.Unlock()
		ok := c.healthy(ctx)
		h.mu.Lock()
		if ctx.Err() == nil {
			h.ok, h.at = ok, time.Now()
			st.Reachable = &ok
		}
		h.loading = nil
		close(done)
		h.mu.Unlock()
		return st
	}
}

func (c *crawler) healthy(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	base, token := c.target()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/health", nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.http.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode <= 299
}

package research

// A crawl4ai service's async submit-then-poll API (POST /crawl {"urls": [...],
// "priority": N} → task id, then GET /task/{id} until the result is ready),
// with the SSRF-guarded direct fetch (fallback.go) when the crawler fails.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type crawler struct {
	mu    sync.RWMutex // guards base and token: SetSearch swaps them at runtime
	base  string
	token string
	http  *http.Client

	pollDelay      time.Duration
	pollAttempts   int
	primaryTimeout time.Duration
	direct         *directFetcher
}

func newCrawler(base, token string) *crawler {
	return &crawler{
		base:           strings.TrimSuffix(base, "/"),
		token:          token,
		http:           &http.Client{Timeout: 30 * time.Second},
		pollDelay:      time.Second,
		pollAttempts:   30,
		primaryTimeout: 10 * time.Second, // short: the direct fetch serves most pages anyway
		direct:         newDirectFetcher(),
	}
}

type crawlPage struct {
	Markdown string `json:"markdown"`
	Metadata struct {
		Title string `json:"title"`
	} `json:"metadata"`
}

type taskResult struct {
	Status  string      `json:"status"`
	Results []crawlPage `json:"results"`
	Result  *crawlPage  `json:"result"`
}

func (t *taskResult) page() crawlPage {
	if len(t.Results) > 0 {
		return t.Results[0]
	}
	if t.Result != nil {
		return *t.Result
	}
	return crawlPage{}
}

// target is the crawl4ai service and its token.
func (c *crawler) target() (base, token string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.base, c.token
}

// fetch returns the raw page text: crawl4ai markdown, or visible text from
// the direct fetch when the crawler fails or is not configured.
func (c *crawler) fetch(ctx context.Context, pageURL string) (Page, error) {
	if err := ctx.Err(); err != nil {
		return Page{}, err
	}
	primaryErr := errors.New("crawl4ai: not configured")
	if base, token := c.target(); base != "" && token != "" { // never hand URLs to an unauthenticated crawler
		budget := c.primaryTimeout
		if budget <= 0 || budget > 30*time.Second {
			budget = 30 * time.Second
		}
		// Reserve half a short caller deadline for recovery. A crawler submission
		// or stalled poll must not consume all the time available to the caller.
		if deadline, ok := ctx.Deadline(); ok {
			budget = min(budget, time.Until(deadline)/2)
		}
		primaryCtx, cancel := context.WithTimeout(ctx, budget)
		page, err := c.fetchPrimary(primaryCtx, pageURL)
		cancel()
		if err == nil {
			return Page{Title: page.Metadata.Title, Text: page.Markdown, Via: "crawl4ai"}, nil
		}
		primaryErr = err
	}
	if err := ctx.Err(); err != nil {
		return Page{}, err
	}
	title, text, err := c.direct.fetch(ctx, pageURL)
	if ctx.Err() != nil {
		return Page{}, ctx.Err()
	}
	// Third-party errors can contain complete URLs or credentials. Keep log
	// metadata to the target hostname and fixed categories only.
	host := ""
	if u, parseErr := url.Parse(pageURL); parseErr == nil {
		host = u.Hostname()
	}
	primaryCategory := "unavailable"
	if errors.Is(primaryErr, context.DeadlineExceeded) {
		primaryCategory = "timeout"
	}
	if err != nil {
		slog.WarnContext(ctx, "crawl4ai direct fallback failed", "host", host,
			"primary_error", primaryCategory, "fallback_error", directCategory(err))
		return Page{}, fmt.Errorf("%w; %w", primaryErr, err)
	}
	slog.InfoContext(ctx, "crawl4ai direct fallback recovered", "host", host, "primary_error", primaryCategory)
	return Page{Title: title, Text: text, Via: "direct"}, nil
}

func (c *crawler) fetchPrimary(ctx context.Context, pageURL string) (crawlPage, error) {
	taskID, err := c.submit(ctx, pageURL)
	if err != nil {
		return crawlPage{}, err
	}
	for i := 0; i < c.pollAttempts; i++ {
		select {
		case <-ctx.Done():
			return crawlPage{}, ctx.Err()
		case <-time.After(c.pollDelay):
		}
		var tr taskResult
		if err := c.do(ctx, http.MethodGet, "/task/"+url.PathEscape(taskID), nil, &tr); err != nil {
			continue
		}
		status := strings.ToLower(tr.Status)
		if page := tr.page(); page.Markdown != "" {
			return page, nil
		}
		if status == "failed" || status == "error" {
			return crawlPage{}, fmt.Errorf("crawl4ai: task %s failed", taskID)
		}
		// A completed task with no markdown (blocked/empty page) is done — don't
		// keep polling to a false timeout.
		if status == "completed" || status == "done" {
			return crawlPage{}, fmt.Errorf("crawl4ai: task %s returned no content", taskID)
		}
	}
	return crawlPage{}, fmt.Errorf("crawl4ai: task %s timed out", taskID)
}

func (c *crawler) submit(ctx context.Context, pageURL string) (string, error) {
	var resp struct {
		TaskID string `json:"task_id"`
		ID     string `json:"id"`
		Task   string `json:"task"`
	}
	body := map[string]any{"urls": []string{pageURL}, "priority": 10}
	if err := c.do(ctx, http.MethodPost, "/crawl", body, &resp); err != nil {
		return "", err
	}
	for _, id := range []string{resp.TaskID, resp.ID, resp.Task} {
		if id != "" {
			return id, nil
		}
	}
	return "", fmt.Errorf("crawl4ai: no task id in response")
}

func (c *crawler) do(ctx context.Context, method, path string, body any, out any) error {
	reader := bytes.NewReader(nil)
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	base, token := c.target()
	req, err := http.NewRequestWithContext(ctx, method, base+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Foretrade")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("crawl4ai %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("crawl4ai %s %s: HTTP %d", method, path, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
}

package research

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	braveBase = "https://api.search.brave.com/res/v1"
	cacheTTL  = 15 * time.Minute
	maxCache  = 1000
)

// brave calls the Brave Search API under a process-wide daily budget and a
// minimum gap between calls.
type brave struct {
	base string
	http *http.Client

	mu    sync.Mutex
	key   string // SetSearch swaps it at runtime
	limit int    // calls per UTC day
	day   string // UTC date of used
	used  int
	next  time.Time     // earliest start of the next call
	gap   time.Duration // 100 ms = 10 calls/s
}

func newBrave(key, base string, limit int) *brave {
	if limit <= 0 {
		limit = 5000
	}
	return &brave{
		key:   key,
		base:  strings.TrimSuffix(cmp.Or(base, braveBase), "/"),
		http:  &http.Client{Timeout: 15 * time.Second},
		limit: limit,
		gap:   100 * time.Millisecond,
	}
}

func (b *brave) apiKey() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.key
}

type backgroundKey struct{}

// Background marks ctx as background work (the wire ingest): its Brave calls
// stop at backgroundShare of the daily budget, so the rest stays for people
// in chat, forecasts, copilot and the news page.
func Background(ctx context.Context) context.Context {
	return context.WithValue(ctx, backgroundKey{}, true)
}

// ponytail: a fixed 60/40 split; make it adaptive if interactive use ever leaves the 40% idle.
const backgroundShare = 0.6

// take spends one call of today's budget and waits for the call's slot.
// ponytail: a slot reserved by a caller that then gives up stays spent.
func (b *brave) take(ctx context.Context) error {
	b.mu.Lock()
	now := time.Now()
	if day := now.UTC().Format(time.DateOnly); day != b.day {
		b.day, b.used = day, 0
	}
	if b.used >= b.limit {
		b.mu.Unlock()
		return fmt.Errorf("%w: daily Brave budget of %d calls used", ErrUnavailable, b.limit)
	}
	if ctx.Value(backgroundKey{}) != nil && b.used >= int(float64(b.limit)*backgroundShare) {
		b.mu.Unlock()
		return fmt.Errorf("%w: background share of the daily Brave budget used", ErrUnavailable)
	}
	if b.used++; b.used == b.limit {
		slog.Warn("brave daily budget exhausted", "limit", b.limit)
	}
	start := now
	if b.next.After(now) {
		start = b.next
	}
	b.next = start.Add(b.gap)
	b.mu.Unlock()
	if wait := start.Sub(now); wait > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
	return nil
}

type braveResult struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
	PageAge     string `json:"page_age"`
	Profile     struct {
		Name string `json:"name"`
	} `json:"profile"`
}

// search calls /{endpoint}/search ("news" or "web"); country ("us") narrows
// Brave to one market, "" searches all; lang is Brave's search_lang ("" →
// "en"). Only an English search drops non-English results as junk.
func (b *brave) search(ctx context.Context, endpoint, query, freshness, country, lang string, count int) ([]Result, error) {
	key := b.apiKey()
	if key == "" {
		return nil, fmt.Errorf("%w: no Brave API key", ErrUnavailable)
	}
	if err := b.take(ctx); err != nil {
		return nil, err
	}
	lang = cmp.Or(lang, "en")
	q := url.Values{"q": {query}, "count": {strconv.Itoa(count)}, "search_lang": {lang}}
	if freshness != "" {
		q.Set("freshness", freshness)
	}
	if country != "" {
		q.Set("country", country)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.base+"/"+endpoint+"/search?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Foretrade")
	req.Header.Set("X-Subscription-Token", key)
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: brave %s: %w", ErrUnavailable, endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: brave %s: HTTP %d", ErrUnavailable, endpoint, resp.StatusCode)
	}
	var parsed struct {
		Results []braveResult `json:"results"` // news
		Web     struct {
			Results []braveResult `json:"results"`
		} `json:"web"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("%w: brave %s: %w", ErrUnavailable, endpoint, err)
	}
	out := []Result{}
	for _, r := range append(parsed.Results, parsed.Web.Results...) {
		u, err := url.Parse(r.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
			continue // only plain web links reach the UI and the crawler
		}
		title, desc, name := plainText(r.Title), plainText(r.Description), plainText(r.Profile.Name)
		if title == "" || junkIn(title, desc, r.URL, name, lang == "en") {
			continue // spam never reaches the wire, chat, forecasts or the arena
		}
		out = append(out, Result{
			Title:       title,
			URL:         r.URL,
			Source:      sourceName(name, hostOf(r.URL)),
			Description: desc,
			PublishedAt: parsePageAge(r.PageAge),
		})
	}
	// Recognized outlets first, Brave's order within each group; one copy of a syndicated story.
	slices.SortStableFunc(out, func(a, b Result) int { return cmp.Compare(rank(a.URL), rank(b.URL)) })
	return firstByTitle(out, func(r Result) string { return r.Title }), nil
}

// sourceName is Brave's publisher name when it matches the link's domain
// ("Yahoo Finance" for finance.yahoo.com, "24/7 Wall St." for 247wallst.com),
// else the hostname: Brave sometimes names a place ("United States"). Names
// or labels under 3 characters must match exactly, so x.com and t.co do not
// take the name of every publisher with an x or a t in it.
// ponytail: the second-level label of a two-part suffix (bbc.co.uk → "co")
// never matches, so those show the hostname.
func sourceName(name, host string) string {
	labels := strings.Split(host, ".")
	n, label := alnum(name), alnum(labels[max(len(labels)-2, 0)])
	if n != "" && (n == label || len(n) >= 3 && len(label) >= 3 && (strings.Contains(n, label) || strings.Contains(label, n))) {
		return name
	}
	return host
}

// alnum is s lower-cased with everything but letters and digits removed.
func alnum(s string) string {
	return strings.Map(func(r rune) rune {
		if isWordRune(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, s)
}

func rank(rawURL string) int {
	if known(hostOf(rawURL)) {
		return 0
	}
	return 1
}

var htmlTag = regexp.MustCompile(`<[^>]*>`)

// plainText drops Brave's highlight tags and entities and collapses whitespace.
func plainText(s string) string {
	return strings.Join(strings.Fields(html.UnescapeString(htmlTag.ReplaceAllString(s, ""))), " ")
}

// parsePageAge reads Brave's page_age, usually "2006-01-02T15:04:05" in UTC.
func parsePageAge(s string) *time.Time {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", time.DateOnly} {
		if t, err := time.Parse(layout, s); err == nil {
			t = t.UTC()
			return &t
		}
	}
	return nil
}

type cached struct {
	at      time.Time
	results []Result
}

func (s *Service) search(ctx context.Context, endpoint, query, freshness, country, lang string, count int) ([]Result, error) {
	query = strings.Join(strings.Fields(query), " ")
	if query == "" {
		return nil, errors.New("research: empty search query")
	}
	if utf8.RuneCountInString(query) > 400 { // Brave's limit
		query = string([]rune(query)[:400])
	}
	if count <= 0 {
		count = 10
	}
	count = min(count, 20)
	key := endpoint + "\x00" + freshness + "\x00" + country + "\x00" + cmp.Or(lang, "en") + "\x00" + strconv.Itoa(count) + "\x00" + query
	s.mu.Lock()
	c, ok := s.cache[key]
	s.mu.Unlock()
	if ok && time.Since(c.at) < cacheTTL {
		return slices.Clone(c.results), nil
	}
	results, err := s.brave.search(ctx, endpoint, query, freshness, country, lang, count)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if len(s.cache) >= maxCache {
		// ponytail: sweep on insert; the daily budget keeps fresh entries far below maxCache.
		for k, v := range s.cache {
			if time.Since(v.at) >= cacheTTL {
				delete(s.cache, k)
			}
		}
	}
	s.cache[key] = cached{at: time.Now(), results: results}
	s.mu.Unlock()
	return slices.Clone(results), nil
}

// Package research is the market-intelligence core: live news and web search
// on the Brave Search API, article reading through crawl4ai with an
// SSRF-guarded direct-fetch fallback, the stored news wire (news_items) that a
// background loop refreshes every 2 h, and the AI market brief (market_briefs).
//
// Feature packages reach it as app.Deps.Research (*Service). Its Go API:
//
//	Enabled() bool
//	    A Brave key is configured. When false every search returns ErrUnavailable
//	    and Run waits; stored news, briefs and ReadPage still work.
//	SetSearch(braveKey, crawlBase, crawlToken string)
//	    Swaps the keys at runtime (the admin console's Settings).
//
//	SearchNews(ctx, query, freshness string, count int) ([]Result, error)
//	    Brave News. freshness "pd" | "pw" | "pm" | "py" (day, week, month,
//	    year), a date range "YYYY-MM-DDtoYYYY-MM-DD" (both valid dates,
//	    from ≤ to, inclusive) or "" for no filter; anything else is an error.
//	    count 1..20 (≤ 0 → 10).
//	SearchWeb(ctx, query string, count int) ([]Result, error)
//	    Brave Web search, same count rules, no freshness filter.
//	SearchIn(ctx, endpoint, query, freshness, country, lang string, count int) ([]Result, error)
//	    Either search ("news" or "web", else an error) with a freshness (web
//	    too), a Brave country ("" = worldwide) and search_lang ("" = "en");
//	    SearchNews and SearchWeb are it with country and lang "". Only an
//	    English search drops non-English results; every other junk rule applies.
//	    All three: results are cached 15 min per (endpoint, query, freshness,
//	    country, lang, count); misses spend the process-wide daily budget
//	    (BRAVE_DAILY_LIMIT per UTC day) and are spaced ≥ 100 ms apart (≤ 10
//	    calls/s). Disabled, over budget or an upstream failure →
//	    errors.Is(err, ErrUnavailable): answer "search unavailable", never fail
//	    the request. Only http(s) result URLs are returned; Title/Description
//	    are plain text (Brave's <strong> and HTML entities removed). Empty query → error.
//	BraveUsage() (used, limit int) / BraveLeft() (left, limit int)
//	    Today's Brave meter: calls spent (or left) this UTC day and the daily budget.
//
//	ReadPage(ctx, url string, maxChars int) (Page, error)
//	    Readable text of a page: link-farm and navigation lines dropped,
//	    whitespace collapsed, at most maxChars runes (≤ 0 or > 20,000 → 20,000;
//	    "…" marks a cut). Sources in order: the stored news_items.content for
//	    that exact URL (Via "cache"), crawl4ai (Via "crawl4ai"; ≤ 10 s and at
//	    most half the caller's remaining deadline), then the direct fetch
//	    (Via "direct": public IPs only, every redirect and the dialled address
//	    re-checked, 1 MiB cap, no JavaScript). A URL that is not public http(s)
//	    on port 80/443 → errors.Is(err, ErrBadURL) before any request. A
//	    successful read of a stored article saves its content. A stub (cleaned
//	    text under 400 runes, or under 3,000 with bot-check wording such as
//	    "Just a moment" or "enable JavaScript") → errors.Is(err, ErrNotReadable),
//	    nothing saved; a cached stub is a cache miss. Always pass a deadline
//	    (the forecast research stage uses 25 s per page, deep research 20 s).
//	    A non-English title is junk unless ctx is marked AnyLanguage.
//	AnyLanguage(ctx) context.Context
//	    Marks ctx so ReadPage keeps pages in any language (deep research asks
//	    for local-language press on purpose); chat and ingestion never mark it.
//
//	Headlines(ctx, symbol string, since time.Duration, limit int) ([]NewsItem, error)
//	    Stored headlines tagged with symbol ("" = any; case-insensitive; unknown →
//	    error), published (or, when the date is unknown, fetched) within since
//	    (≤ 0 = no limit), limit 1..100 (≤ 0 → 10). No Brave call. For a
//	    symbol, items whose title names it come first and at most one roundup
//	    (a page naming 4+ assets, kept only when its title names it) comes
//	    last; within each group known outlets first, then newest first. Rows
//	    that fail Relevant (junk, off-topic, not naming the symbol) are hidden.
//	News(ctx, NewsFilter) ([]NewsItem, error)
//	    The GET /api/news query; Limit ≤ 0 → 30, capped at 100; Region one of
//	    Regions ("" any; else an error). Same hiding.
//	LatestBrief(ctx) (*MarketBrief, error)
//	    Newest market brief, or nil (no error) before the first one exists.
//
//	Ingest(ctx) error
//	    One wire-ingestion run exactly per the contract (18 Brave calls, plus
//	    18 regional ones on every other run starting with the first, ≤ 30 page
//	    reads, one low-effort Sonnet call for the brief when an llm.Client was
//	    given, 14-day retention). A second concurrent call returns an error at
//	    once instead of running.
//	Run(ctx)
//	    The loop main starts with the server's lifetime: first run at boot when
//	    max(news_items.fetched_at) is over 2 h old (else when it turns 2 h old),
//	    then every 2 h; while disabled it checks again every minute. Returns
//	    when ctx ends.
//	Register(mux, require)
//	    GET /api/news, GET /api/news/brief and GET /api/news/pulse behind the session check.
//	FearGreed(ctx) *FearGreed
//	    alternative.me's Crypto Fear & Greed Index, fetched at most once an hour
//	    (5 s timeout, last good value kept); nil until a fetch has succeeded.
//
//	Known(url string) bool
//	    The URL is on a recognized outlet (newsroom, data desk or official
//	    source): ranked first, and exempt from the SEO-title junk rules.
//
//	Relevant(title, description, url, source, symbol string) bool
//	    A stored wire headline (a NewsItem, or a copy of one such as a signals
//	    board's) may reach an AI surface: not junk (spam, gambling or coupon
//	    wording on the title, description or source name, blocked or gambling
//	    hosts, converter, template and SEO titles, non-English), its title is
//	    about markets (names a universe asset, a US-macro term, a market word
//	    or a region's index, exchange, central bank or stocks) and, with a
//	    symbol, it names that asset (a roundup in its title).
//	    Readers of stored wire copies call it on read, so old rows are hidden.
//
//	Untrusted(text string) string
//	    Wraps fetched web text in <untrusted_web_content> tags for a prompt
//	    (stray wrapper tags inside the text are removed first). Every Result,
//	    Page and NewsItem field is untrusted web text: wrap it, and say in the
//	    system prompt that instructions inside those tags must be ignored.
package research

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm"
)

var (
	// ErrUnavailable: search is disabled, over today's budget, or Brave failed.
	ErrUnavailable = errors.New("research: search unavailable")
	// ErrBadURL: ReadPage refuses URLs that are not public http(s) addresses.
	ErrBadURL = errors.New("research: url must be a public http(s) address")
	// ErrNotReadable: the page is a bot check, a JavaScript wall or nearly empty (see stub).
	ErrNotReadable = errors.New("not readable (blocked or empty page)")
)

// maxContent caps stored article text and ReadPage output, in runes.
const maxContent = 20_000

// Result is one Brave search hit.
type Result struct {
	Title       string     `json:"title"`
	URL         string     `json:"url"`
	Source      string     `json:"source"` // publisher name, else hostname
	Description string     `json:"description"`
	PublishedAt *time.Time `json:"publishedAt"` // Brave page_age; nil when unknown
}

// Page is the readable text of a web page.
type Page struct {
	URL   string
	Title string // may be empty
	Text  string
	Via   string // "cache" | "crawl4ai" | "direct"
}

// NewsItem is a stored headline (contract NewsItem). Field order matches the
// news query's columns.
type NewsItem struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	URL         string     `json:"url"`
	Source      string     `json:"source"`
	Description string     `json:"description"`
	PublishedAt *time.Time `json:"publishedAt"`
	Symbols     []string   `json:"symbols"`
	Topic       string     `json:"topic"`   // "asset" | "macro"
	Regions     []string   `json:"regions"` // sorted Regions it touches; [] global or unclassified
	HasContent  bool       `json:"hasContent"`
}

// NewsFilter selects stored news. Zero values mean "any".
type NewsFilter struct {
	Symbol string        // universe symbol, case-insensitive
	Topic  string        // "asset" | "macro"
	Region string        // one of Regions
	Since  time.Duration // published (or fetched) within
	Limit  int           // ≤ 0 → 30, capped at 100
}

// MarketBrief is the contract MarketBrief.
type MarketBrief struct {
	ID           string        `json:"id"`
	Headline     string        `json:"headline"`
	Summary      string        `json:"summary"`
	Bullets      []BriefBullet `json:"bullets"`   // 3..6
	Sentiment    []Sentiment   `json:"sentiment"` // universe assets with news, universe order
	ArticleCount int           `json:"articleCount"`
	Window       Window        `json:"window"`
	CreatedAt    time.Time     `json:"createdAt"`
	// Regions whose markets moved, in Regions order; absent when none did and on older briefs.
	Regions []RegionBrief `json:"regions,omitempty"`
}

// RegionBrief is one region's line in a MarketBrief.
type RegionBrief struct {
	Region  string `json:"region"` // one of Regions
	Summary string `json:"summary"`
}

type BriefBullet struct {
	Text    string   `json:"text"`
	Symbols []string `json:"symbols"`
	Sources []string `json:"sources"` // NewsItem ids
}

type Sentiment struct {
	Symbol string  `json:"symbol"`
	Score  float64 `json:"score"` // -1..1
	Label  string  `json:"label"` // "bullish" (≥ 0.15) | "bearish" (≤ -0.15) | "neutral"
	Reason string  `json:"reason"`
}

type Window struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// Config holds the service settings; main fills it from config.Config.
type Config struct {
	BraveAPIKey string
	BraveBase   string // "" → https://api.search.brave.com/res/v1 (tests point it at httptest)
	CrawlBase   string // crawl4ai service; empty base or token → direct fetch only
	CrawlToken  string
	DailyLimit  int // Brave calls per UTC day; ≤ 0 → 5000
}

type Service struct {
	db    *pgxpool.Pool
	ai    *llm.Client // nil → no market brief
	brave *brave
	crawl *crawler

	mu    sync.Mutex // guards cache
	cache map[string]cached

	ingestMu sync.Mutex // held for a whole Ingest run
	ingests  int        // Ingest runs so far (guarded by ingestMu): the regional searches run on every other one
	health   health     // cached crawler probe (status.go)

	fngURL string // Fear & Greed endpoint (pulse.go); tests point it at httptest
	pulse  pulse
}

// New builds the service. db holds news_items and market_briefs; ai writes
// the market brief (nil skips it).
func New(cfg Config, db *pgxpool.Pool, ai *llm.Client) *Service {
	return &Service{
		db:     db,
		ai:     ai,
		brave:  newBrave(cfg.BraveAPIKey, cfg.BraveBase, cfg.DailyLimit),
		crawl:  newCrawler(cfg.CrawlBase, cfg.CrawlToken),
		cache:  map[string]cached{},
		fngURL: fngURL,
	}
}

func (s *Service) Enabled() bool { return s.brave.apiKey() != "" }

// SetSearch swaps the Brave key and the crawl4ai service at runtime (the
// admin console's Settings); today's Brave budget carries over and the
// crawler is probed afresh.
func (s *Service) SetSearch(braveKey, crawlBase, crawlToken string) {
	s.brave.mu.Lock()
	s.brave.key = braveKey
	s.brave.mu.Unlock()
	s.crawl.mu.Lock()
	s.crawl.base, s.crawl.token = strings.TrimSuffix(crawlBase, "/"), crawlToken
	s.crawl.mu.Unlock()
	s.health.mu.Lock()
	s.health.at = time.Time{}
	s.health.mu.Unlock()
}

func (s *Service) SearchNews(ctx context.Context, query, freshness string, count int) ([]Result, error) {
	return s.SearchIn(ctx, "news", query, freshness, "", "", count)
}

// SearchIn is a news or web search with a freshness, a Brave country ("" =
// worldwide) and search_lang ("" = "en").
func (s *Service) SearchIn(ctx context.Context, endpoint, query, freshness, country, lang string, count int) ([]Result, error) {
	if endpoint != "news" && endpoint != "web" {
		return nil, fmt.Errorf("research: endpoint must be news or web, got %q", endpoint)
	}
	if !validFreshness(freshness) {
		return nil, fmt.Errorf("research: freshness must be pd, pw, pm, py, YYYY-MM-DDtoYYYY-MM-DD or empty, got %q", freshness)
	}
	return s.search(ctx, endpoint, query, freshness, country, lang, count)
}

// validFreshness accepts Brave's relative windows and its date-range form.
// time.Parse with time.DateOnly demands exactly YYYY-MM-DD and a real date.
func validFreshness(f string) bool {
	switch f {
	case "", "pd", "pw", "pm", "py":
		return true
	}
	from, to, ok := strings.Cut(f, "to")
	a, errA := time.Parse(time.DateOnly, from)
	b, errB := time.Parse(time.DateOnly, to)
	return ok && errA == nil && errB == nil && !a.After(b)
}

func (s *Service) SearchWeb(ctx context.Context, query string, count int) ([]Result, error) {
	return s.SearchIn(ctx, "web", query, "", "", "", count)
}

type anyLanguageKey struct{}

// AnyLanguage marks ctx so ReadPage keeps a page whatever its language: deep
// research asks for local-language press (a Lianhe Zaobao story) on purpose.
func AnyLanguage(ctx context.Context) context.Context {
	return context.WithValue(ctx, anyLanguageKey{}, true)
}

// stubText is the wording of bot checks and JavaScript walls (Cloudflare's "Just a moment…").
var stubText = regexp.MustCompile(`(?i)just a moment|verify (that )?you are (a )?human|checking (your browser|if the site connection is secure)|` +
	`security check|enable javascript|attention required|access denied|are you a robot|captcha`)

// stub reports whether cleaned page text is not an article: under 400 runes,
// or under 3,000 with bot-check wording (a long article whose footer says
// "enable JavaScript" still reads).
// ponytail: length and wording heuristics; a paywall teaser over 400 runes still reads.
func stub(text string) bool {
	n := utf8.RuneCountInString(text)
	return n < 400 || n < 3000 && stubText.MatchString(text)
}

func (s *Service) ReadPage(ctx context.Context, rawURL string, maxChars int) (Page, error) {
	if maxChars <= 0 || maxChars > maxContent {
		maxChars = maxContent
	}
	english := ctx.Value(anyLanguageKey{}) == nil
	rawURL = strings.TrimSpace(rawURL)
	if s.db != nil {
		p := Page{URL: rawURL, Via: "cache"}
		var source, description string
		err := s.db.QueryRow(ctx, "select title, content, source, description from news_items where url = $1 and content is not null",
			rawURL).Scan(&p.Title, &p.Text, &source, &description)
		if err == nil && junkIn(p.Title, description, rawURL, source, english) {
			return Page{}, ErrNotReadable
		}
		if err == nil && !stub(p.Text) { // a stub saved before the check is a miss
			p.Text = truncate(p.Text, maxChars)
			return p, nil
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return Page{}, fmt.Errorf("research: read cache: %w", err)
		}
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return Page{}, ErrBadURL
	}
	// ponytail: one pre-check before the crawler; its redirects and DNS
	// re-resolution are out of our reach, so the crawl service must block
	// private, link-local and IMDS egress itself (the direct fallback
	// re-checks every hop).
	if err := s.crawl.direct.validateURL(ctx, u); err != nil {
		switch directCategory(err) {
		case "invalid_url", "blocked_destination":
			return Page{}, fmt.Errorf("%w (%s)", ErrBadURL, directCategory(err))
		}
		return Page{}, fmt.Errorf("research: read %s: %w", u.Hostname(), err)
	}
	if junk("", "", rawURL, "") {
		return Page{}, ErrNotReadable
	}
	p, err := s.crawl.fetch(ctx, rawURL)
	if err != nil {
		return Page{}, fmt.Errorf("research: read %s: %w", u.Hostname(), err)
	}
	p.URL = rawURL
	p.Title = truncate(strings.Join(strings.Fields(p.Title), " "), 300)
	if junkIn(p.Title, "", rawURL, "", english) {
		return Page{}, ErrNotReadable
	}
	if p.Text = cleanText(p.Text, maxContent); stub(p.Text) {
		return Page{}, fmt.Errorf("research: read %s: %w", u.Hostname(), ErrNotReadable)
	}
	if s.db != nil {
		if _, err := s.db.Exec(ctx, "update news_items set content = $2, crawled_at = now() where url = $1",
			rawURL, p.Text); err != nil {
			slog.Warn("news content save failed", "host", u.Hostname(), "err", err)
		}
	}
	p.Text = truncate(p.Text, maxChars)
	return p, nil
}

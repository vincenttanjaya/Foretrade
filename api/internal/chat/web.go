package chat

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vincenttanjaya/Foretrade/api/internal/research"
)

// Market-intelligence tools: news and web search, page reads and the stored
// news wire. Every source shown to the model gets a per-message number [n]
// that the answer cites; the numbered list becomes the message's sources card.

const (
	maxSources    = 15 // per message; the sources card lists them all
	maxReads      = 3  // read_page calls per message
	maxSearches   = 4  // search_news + web_search calls per message; bounds one user's draw on the shared Brave budget
	pageChars     = 6000
	readTimeout   = 25 * time.Second
	newsCount     = 6
	webCount      = 5
	wireWindow    = 48 * time.Hour
	wireHeadlines = 8
)

type sourceRef struct {
	N           int        `json:"n"`
	Title       string     `json:"title"`
	URL         string     `json:"url"`
	Source      string     `json:"source"`
	PublishedAt *time.Time `json:"publishedAt"`
	Kind        string     `json:"kind"` // where it was first numbered: wire | news | web | page | market

	// text is what the model was shown of it (snippet, then the read page's
	// excerpt), never serialized: price_outlook checks cited numbers against it.
	text string
}

// sources numbers the web sources shown to the model in one message. The
// same URL keeps its number; past maxSources nothing new is numbered, so
// every [n] the model can cite is on the card.
// ponytail: exact URL match; "?utm=" variants of one article get two numbers.
type sources struct{ items []sourceRef }

func (s *sources) add(kind, title, rawURL, source string, published *time.Time) (n int, ok bool) {
	if n := s.number(rawURL); n > 0 {
		return n, true
	}
	if len(s.items) >= maxSources {
		return 0, false
	}
	n = len(s.items) + 1
	s.items = append(s.items, sourceRef{N: n, Title: title, URL: rawURL, Source: source, PublishedAt: published, Kind: kind})
	return n, true
}

// note appends text the model was shown to source n (bounded per source).
func (s *sources) note(n int, text string) {
	for i := range s.items {
		if it := &s.items[i]; it.N == n && utf8.RuneCountInString(it.text) < 2*pageChars {
			it.text += text + "\n"
		}
	}
}

// number is rawURL's source number, 0 when it was not shown.
func (s *sources) number(rawURL string) int {
	for _, it := range s.items {
		if it.URL == rawURL {
			return it.N
		}
	}
	return 0
}

// card is the message's sources card, nil when no source was shown.
func (s *sources) card() *Card {
	if len(s.items) == 0 {
		return nil
	}
	return &Card{Type: "sources", Data: map[string]any{"items": s.items}}
}

// entry is one numbered source as the model sees it. JSON-encoded, one per
// line, so web text cannot forge another entry or number.
type entry struct {
	N           int        `json:"n"`
	Title       string     `json:"title"`
	Source      string     `json:"source"`
	PublishedAt *time.Time `json:"-"`
	Published   string     `json:"published,omitempty"`
	URL         string     `json:"url"`
	Description string     `json:"description,omitempty"`
}

// numbered registers each source (of kind) and returns its entry lines plus
// how many were dropped because the message already holds maxSources.
func (a *agentRun) numbered(kind string, items []entry) (lines []string, dropped int) {
	for _, e := range items {
		n, ok := a.src.add(kind, e.Title, e.URL, e.Source, e.PublishedAt)
		if !ok {
			dropped++
			continue
		}
		e.N, e.Description = n, clip(e.Description, 400)
		a.src.note(n, e.Title+"\n"+e.Description)
		if e.PublishedAt != nil {
			e.Published = e.PublishedAt.UTC().Format("2006-01-02 15:04 UTC")
		}
		b, _ := json.Marshal(e)
		lines = append(lines, string(b))
	}
	return lines, dropped
}

// searchResults formats Brave hits (kind news or web) for the model.
func (a *agentRun) searchResults(kind, header string, results []research.Result) string {
	items := make([]entry, len(results))
	for i, r := range results {
		items[i] = entry{Title: r.Title, Source: r.Source, PublishedAt: r.PublishedAt, URL: r.URL, Description: r.Description}
	}
	lines, dropped := a.numbered(kind, items)
	if len(lines) == 0 && dropped == 0 {
		return header + "\nNo results. Try a broader query or a longer freshness window."
	}
	return header + " Cite a result as [n] using its n." + droppedNote(dropped) + "\n" + research.Untrusted(strings.Join(lines, "\n"))
}

func droppedNote(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(" %d more not shown: this message already has %d sources.", n, maxSources)
}

func (a *agentRun) searchNews(ctx context.Context, input json.RawMessage) (string, error) {
	query, freshness, err := parseSearchInput(input)
	if err != nil {
		return "", err
	}
	if err := a.countSearch(); err != nil {
		return "", err
	}
	results, err := a.d.Research.SearchNews(ctx, query, freshness, newsCount)
	if err != nil {
		return "", a.searchErr(err)
	}
	results = onTopic(query, results)
	a.summary = plural(len(results), "article")
	return a.searchResults("news", fmt.Sprintf("News results for %q (past %s).", query, freshnessWord[freshness]), results), nil
}

func (a *agentRun) webSearch(ctx context.Context, input json.RawMessage) (string, error) {
	query, _, err := parseSearchInput(input)
	if err != nil {
		return "", err
	}
	if err := a.countSearch(); err != nil {
		return "", err
	}
	results, err := a.d.Research.SearchWeb(ctx, query, webCount)
	if err != nil {
		return "", a.searchErr(err)
	}
	results = onTopic(query, results)
	a.summary = plural(len(results), "result")
	return a.searchResults("web", fmt.Sprintf("Web results for %q.", query), results), nil
}

// onTopic orders a search about one universe asset: results whose title
// names it first, then the rest, then at most one multi-coin roundup, so the
// lowest [n] go to on-topic sources.
func onTopic(query string, results []research.Result) []research.Result {
	syms := research.Symbols(query)
	if len(syms) != 1 {
		return results
	}
	return research.OnTopic(results, syms[0], func(r research.Result) string { return r.Title },
		func(r research.Result) string { return r.Description })
}

// countSearch spends one of the message's maxSearches.
func (a *agentRun) countSearch() error {
	if a.searches >= maxSearches {
		return fmt.Errorf("search limit reached (%d per message); answer from what you have", maxSearches)
	}
	a.searches++
	return nil
}

func (a *agentRun) searchErr(err error) error {
	if errors.Is(err, research.ErrUnavailable) {
		a.summary = "Search unavailable"
		return errors.New("search is unavailable right now; continue with get_market_wire and the live market tools, and tell the user live search was unavailable")
	}
	slog.Warn("chat search", "err", err)
	return errors.New("search failed; try a different query")
}

// readPage reads a page the model was shown (or the user wrote), so text
// injected into a page cannot send the agent to an arbitrary address.
func (a *agentRun) readPage(ctx context.Context, input json.RawMessage) (string, error) {
	u, err := parseReadInput(input)
	if err != nil {
		return "", err
	}
	n := a.src.number(u)
	switch {
	case n == 0 && !strings.Contains(a.userText, u):
		return "", errors.New("read_page only opens URLs from this message's search results or market wire, or ones the user wrote; search first")
	case n == 0 && len(a.src.items) >= maxSources:
		return "", fmt.Errorf("this message already has %d sources; answer from them", maxSources)
	case a.reads >= maxReads:
		return "", fmt.Errorf("read limit reached (%d pages per message); answer from what you have", maxReads)
	}
	a.reads++
	rctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	host := hostOf(u)
	p, err := a.d.Research.ReadPage(rctx, u, pageChars)
	if errors.Is(err, research.ErrBadURL) {
		return "", errors.New("that address is not a public web page")
	}
	if err != nil {
		slog.Warn("chat read_page", "err", err)
		a.summary = "Couldn't read " + host
		return "", fmt.Errorf("couldn't read %s (blocked, timed out or no readable text); try another source", host)
	}
	if n == 0 {
		n, _ = a.src.add("page", cmp.Or(p.Title, host), u, host, nil)
	}
	a.src.note(n, p.Title+"\n"+p.Text)
	chars := utf8.RuneCountInString(p.Text)
	a.summary = fmt.Sprintf("Read %s chars %s", commas(int64(chars)), viaWord[p.Via])
	return pageResult(n, p, chars), nil
}

var viaWord = map[string]string{"cache": "from the news cache", "crawl4ai": "via crawl4ai", "direct": "via direct fetch"}

func pageResult(n int, p research.Page, chars int) string {
	return fmt.Sprintf("Page [%d]: %s chars of readable text (at most %s). Cite it as [%d].\n", n, commas(int64(chars)), commas(pageChars), n) +
		research.Untrusted("Title: "+p.Title+"\nURL: "+p.URL+"\n\n"+p.Text)
}

func (a *agentRun) marketWire(ctx context.Context, input json.RawMessage) (string, error) {
	symbol, err := parseWireInput(input)
	if err != nil {
		return "", err
	}
	brief, err := a.d.Research.LatestBrief(ctx)
	if err != nil {
		return "", toolErr(err)
	}
	items, err := a.d.Research.Headlines(ctx, symbol, wireWindow, wireHeadlines)
	if err != nil {
		return "", toolErr(err)
	}
	a.summary = plural(len(items), "headline")
	if brief != nil {
		a.summary = "Brief · " + a.summary
	}
	return a.wireText(time.Now(), symbol, brief, items), nil
}

// wireText formats the stored wire: the latest brief (sentiment narrowed to
// symbol when one is given) and numbered headlines, all as untrusted text.
func (a *agentRun) wireText(now time.Time, symbol string, brief *research.MarketBrief, items []research.NewsItem) string {
	scope := cmp.Or(symbol, "all assets")
	if brief == nil && len(items) == 0 {
		return fmt.Sprintf("The market wire has no market brief and no stored headlines for %s in the last 48 h. Use search_news if you need news.", scope)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Market wire for %s (stored news, refreshed every 2 h; no live search).", scope)
	var body []string
	if brief != nil {
		fmt.Fprintf(&sb, " Market brief written %s (%.1f h ago) from %d articles.", brief.CreatedAt.UTC().Format("2006-01-02 15:04 UTC"),
			now.Sub(brief.CreatedAt).Hours(), brief.ArticleCount)
		bullets := make([]string, len(brief.Bullets))
		for i, b := range brief.Bullets {
			bullets[i] = b.Text
		}
		sentiment := []research.Sentiment{}
		for _, s := range brief.Sentiment {
			if symbol == "" || s.Symbol == symbol {
				sentiment = append(sentiment, s)
			}
		}
		b, _ := json.Marshal(map[string]any{"brief": map[string]any{"headline": brief.Headline, "summary": brief.Summary,
			"bullets": bullets, "sentiment": sentiment}})
		body = append(body, string(b))
	}
	entries := make([]entry, len(items))
	for i, it := range items {
		entries[i] = entry{Title: it.Title, Source: it.Source, PublishedAt: it.PublishedAt, URL: it.URL, Description: it.Description}
	}
	lines, dropped := a.numbered("wire", entries)
	fmt.Fprintf(&sb, " %d headlines from the last 48 h, on-topic first, then newest; cite one as [n] using its n.%s\n", len(lines), droppedNote(dropped))
	sb.WriteString(research.Untrusted(strings.Join(append(body, lines...), "\n")))
	return sb.String()
}

var freshnessCode = map[string]string{"day": "pd", "week": "pw", "month": "pm"}
var freshnessWord = map[string]string{"pd": "day", "pw": "week", "pm": "month"}

// parseSearchInput validates a search tool's query and the news freshness
// ("day" | "week" | "month", default week), returned as Brave's code.
func parseSearchInput(input json.RawMessage) (query, freshness string, err error) {
	var in struct{ Query, Freshness string }
	if err := json.Unmarshal(input, &in); err != nil {
		return "", "", fmt.Errorf("invalid input: %v", err)
	}
	query = strings.Join(strings.Fields(in.Query), " ")
	if n := utf8.RuneCountInString(query); n == 0 || n > 200 {
		return "", "", errors.New("query must be 1..200 characters")
	}
	freshness, ok := freshnessCode[cmp.Or(in.Freshness, "week")]
	if !ok {
		return "", "", errors.New(`freshness must be "day", "week" or "month"`)
	}
	return query, freshness, nil
}

func parseReadInput(input json.RawMessage) (string, error) {
	var in struct{ URL string }
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid input: %v", err)
	}
	raw := strings.TrimSpace(in.URL)
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 2048 || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return "", errors.New("url must be a full http(s) address from a search result")
	}
	return raw, nil
}

func parseWireInput(input json.RawMessage) (string, error) {
	var in struct{ Symbol string }
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid input: %v", err)
	}
	if in.Symbol == "" {
		return "", nil
	}
	return lookup(in.Symbol)
}

// hostOf is the URL's host without "www.", for chips and source names.
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(u.Hostname(), "www.")
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

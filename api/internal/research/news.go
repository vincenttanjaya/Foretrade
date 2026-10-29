package research

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/vincenttanjaya/Foretrade/api/internal/httpx"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
)

// Headlines is News for the AI surfaces: within each OnTopic group, known
// outlets first, newest first within those.
func (s *Service) Headlines(ctx context.Context, symbol string, since time.Duration, limit int) ([]NewsItem, error) {
	if limit <= 0 {
		limit = 10
	}
	items, err := s.News(ctx, NewsFilter{Symbol: symbol, Since: since, Limit: limit})
	if err != nil {
		return nil, err
	}
	if info, ok := market.Lookup(symbol); ok {
		symbol = info.Symbol
	}
	group := func(n NewsItem) int {
		if symbol == "" {
			return 0
		}
		return topicGroup(n.Title, n.Description, symbol)
	}
	slices.SortStableFunc(items, func(a, b NewsItem) int {
		return cmp.Or(cmp.Compare(group(a), group(b)), cmp.Compare(rank(a.URL), rank(b.URL)))
	})
	return items, nil
}

func (s *Service) News(ctx context.Context, f NewsFilter) ([]NewsItem, error) {
	if f.Limit <= 0 {
		f.Limit = 30
	}
	f.Limit = min(f.Limit, 100)
	items, err := s.news(ctx, f, f.Limit+50) // headroom for the rows dropped below
	if err != nil {
		return nil, err
	}
	return items[:min(len(items), f.Limit)], nil
}

// news reads up to rows stored items matching f (its Limit unused), newest
// first, and drops the hidden ones.
func (s *Service) news(ctx context.Context, f NewsFilter, rows int) ([]NewsItem, error) {
	symbol := ""
	if f.Symbol != "" {
		info, ok := market.Lookup(f.Symbol)
		if !ok {
			return nil, fmt.Errorf("research: unknown symbol %q", f.Symbol)
		}
		symbol = info.Symbol
	}
	if f.Topic != "" && f.Topic != "asset" && f.Topic != "macro" {
		return nil, fmt.Errorf("research: topic must be asset or macro, got %q", f.Topic)
	}
	if f.Region != "" && !slices.Contains(Regions, f.Region) {
		return nil, fmt.Errorf("research: region must be one of %v, got %q", Regions, f.Region)
	}
	var since *time.Time
	if f.Since > 0 {
		t := time.Now().Add(-f.Since)
		since = &t
	}
	q, _ := s.db.Query(ctx, `select id, title, url, source, description, published_at, symbols, topic, regions, content is not null
		from news_items
		where ($1 = '' or symbols @> array[$1]::text[]) and ($2 = '' or topic = $2)
		  and ($3::timestamptz is null or coalesce(published_at, fetched_at) >= $3)
		  and ($5 = '' or regions @> array[$5]::text[])
		order by published_at desc nulls last, fetched_at desc limit $4`,
		symbol, f.Topic, since, rows, f.Region)
	items, err := pgx.CollectRows(q, pgx.RowToStructByPos[NewsItem])
	if err != nil {
		return nil, fmt.Errorf("research: news: %w", err)
	}
	// Junk, an off-topic title (a converter or lifestyle page a search let
	// through) and for a symbol a row that does not name it are hidden on read:
	// rows stored before the filters and the tagging fix (which tagged a search's
	// results with its symbol) are not cleaned up.
	// ponytail: they age out of the 14-day retention; until then the +50 headroom covers them.
	// Untagged macro rows about another country's economy go too (ingest skips them now).
	items = slices.DeleteFunc(items, func(n NewsItem) bool {
		return !Relevant(n.Title, n.Description, n.URL, n.Source, symbol) || otherEconomy(n)
	})
	items = firstByTitle(items, func(n NewsItem) string { return n.Title })
	return OnTopic(items, symbol, func(n NewsItem) string { return n.Title }, func(n NewsItem) string { return n.Description }), nil
}

// briefFallback is how far back LatestBrief walks past a brief that does not
// validate: a failed brief call (one every 2 h) never blanks every consumer's
// sentiment until the next one validates.
const briefFallback = 24 * time.Hour

// LatestBrief is the newest market brief that validates, among the newest one
// and those of the last 24 h, newest first; nil when none does.
func (s *Service) LatestBrief(ctx context.Context) (*MarketBrief, error) {
	b, _, err := s.latestBrief(ctx)
	return b, err
}

// latestBrief is LatestBrief with the brief's stored inputs (its _quality).
// Only the candidates' ids are listed; each brief is read and validated in
// turn, newest first, so a valid newest brief costs one row as before.
func (s *Service) latestBrief(ctx context.Context) (*MarketBrief, []NewsItem, error) {
	rows, _ := s.db.Query(ctx, `select id, created_at from market_briefs
		where created_at >= now() - make_interval(secs => $1) or created_at = (select max(created_at) from market_briefs)
		order by created_at desc, id desc`, briefFallback.Seconds())
	type row struct {
		id      string
		created time.Time
	}
	briefs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var b row
		return b, r.Scan(&b.id, &b.created)
	})
	if err != nil {
		return nil, nil, fmt.Errorf("research: latest brief: %w", err)
	}
	for _, r := range briefs {
		var raw []byte
		if err := s.db.QueryRow(ctx, `select brief from market_briefs where id = $1`, r.id).Scan(&raw); errors.Is(err, pgx.ErrNoRows) {
			continue // deleted since the list was read
		} else if err != nil {
			return nil, nil, fmt.Errorf("research: latest brief: %w", err)
		}
		var b MarketBrief
		if json.Unmarshal(raw, &b) != nil {
			continue // a row this build cannot read does not validate
		}
		valid, err := s.validBrief(ctx, raw, b)
		if err != nil {
			return nil, nil, fmt.Errorf("research: brief inputs: %w", err)
		}
		if !valid {
			continue
		}
		var stored struct {
			Quality InputQuality `json:"_quality"`
		}
		json.Unmarshal(raw, &stored)
		b.ID, b.CreatedAt = r.id, r.created
		return &b, stored.Quality.Inputs, nil
	}
	return nil, nil, nil
}

// BriefSource is one wire row a brief's bullets cite, as GET /api/news/brief
// lists it: the stored input the brief was validated against, so a citation
// resolves even when the row is no longer among the newest wire items.
type BriefSource struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	URL         string     `json:"url"`
	Source      string     `json:"source"`
	PublishedAt *time.Time `json:"publishedAt"`
}

// citedSources is the inputs any of b's bullets cite, in input order.
func citedSources(b *MarketBrief, inputs []NewsItem) []BriefSource {
	cited := map[string]bool{}
	for _, bl := range b.Bullets {
		for _, id := range bl.Sources {
			cited[id] = true
		}
	}
	out := []BriefSource{}
	for _, n := range inputs {
		if cited[n.ID] {
			out = append(out, BriefSource{ID: n.ID, Title: n.Title, URL: n.URL, Source: n.Source, PublishedAt: n.PublishedAt})
			delete(cited, n.ID)
		}
	}
	return out
}

// Register mounts the news endpoints behind require (the session check).
func (s *Service) Register(mux *http.ServeMux, require func(http.Handler) http.Handler) {
	mux.Handle("GET /api/news", require(http.HandlerFunc(s.handleNews)))
	mux.Handle("GET /api/news/brief", require(http.HandlerFunc(s.handleBrief)))
	mux.Handle("GET /api/news/pulse", require(http.HandlerFunc(s.handlePulse)))
}

func (s *Service) handleNews(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := NewsFilter{Topic: q.Get("topic"), Region: q.Get("region"), Limit: 30}
	if sym := q.Get("symbol"); sym != "" {
		info, ok := market.Lookup(sym)
		if !ok {
			httpx.Error(w, http.StatusBadRequest, "bad_request", "Unknown symbol.")
			return
		}
		f.Symbol = info.Symbol
	}
	if f.Topic != "" && f.Topic != "asset" && f.Topic != "macro" {
		httpx.Error(w, http.StatusBadRequest, "bad_request", "topic must be asset or macro.")
		return
	}
	if f.Region != "" && !slices.Contains(Regions, f.Region) {
		httpx.Error(w, http.StatusBadRequest, "bad_request", "region must be crypto, us, east_asia or southeast_asia.")
		return
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			httpx.Error(w, http.StatusBadRequest, "bad_request", "limit must be 1..100.")
			return
		}
		f.Limit = n
	}
	items, err := s.News(r.Context(), f)
	if err != nil {
		httpx.ServerError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"news": items})
}

func (s *Service) handleBrief(w http.ResponseWriter, r *http.Request) {
	b, inputs, err := s.latestBrief(r.Context())
	if err != nil {
		httpx.ServerError(w, r, err)
		return
	}
	if b == nil {
		httpx.JSON(w, http.StatusOK, map[string]any{"brief": nil})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"brief": struct {
		*MarketBrief
		Sources []BriefSource `json:"sources"`
	}{b, citedSources(b, inputs)}})
}

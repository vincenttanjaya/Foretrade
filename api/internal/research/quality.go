package research

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// InputQuality is persisted beside derived output, never in public responses.
// Inputs includes every model input, even inputs omitted from its citations.
type InputQuality struct {
	Version int        `json:"version"`
	Inputs  []NewsItem `json:"inputs"`
}

// ValidateNewsInputs checks immutable prompt fields using exact row IDs, not a
// capped wire query, in one read. Crawl state is excluded because it is not a
// prompt input. Symbols and regions are labels a later search may add to a row
// (ingest merges them; not web text): the stored symbols must still be among
// the row's, regions are not compared, and otherEconomy reads the row's labels.
func ValidateNewsInputs(ctx context.Context, db *pgxpool.Pool, inputs []NewsItem) (bool, error) {
	seen := map[string]bool{}
	ids := make([]string, 0, len(inputs))
	for _, n := range inputs {
		if n.ID == "" || seen[n.ID] || !Relevant(n.Title, n.Description, n.URL, n.Source, "") {
			return false, nil
		}
		seen[n.ID] = true
		ids = append(ids, n.ID)
	}
	if len(ids) == 0 {
		return true, nil
	}
	rows, _ := db.Query(ctx, `select id,title,url,source,description,published_at,symbols,topic,regions from news_items where id = any($1::uuid[])`, ids)
	stored, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (NewsItem, error) {
		var n NewsItem
		err := row.Scan(&n.ID, &n.Title, &n.URL, &n.Source, &n.Description, &n.PublishedAt, &n.Symbols, &n.Topic, &n.Regions)
		return n, err
	})
	if err != nil {
		return false, err
	}
	byID := make(map[string]NewsItem, len(stored))
	for _, n := range stored {
		byID[n.ID] = n
	}
	for _, n := range inputs {
		current, ok := byID[n.ID]
		if !ok || otherEconomy(current) {
			return false, nil
		}
		if current.Title != n.Title || current.URL != n.URL || current.Source != n.Source || current.Description != n.Description || current.Topic != n.Topic ||
			slices.ContainsFunc(n.Symbols, func(s string) bool { return !slices.Contains(current.Symbols, s) }) {
			return false, nil
		}
		if (current.PublishedAt == nil) != (n.PublishedAt == nil) || current.PublishedAt != nil && !current.PublishedAt.Equal(*n.PublishedAt) {
			return false, nil
		}
	}
	return true, nil
}

func (s *Service) validBrief(ctx context.Context, raw []byte, b MarketBrief) (bool, error) {
	var stored struct {
		Quality *InputQuality `json:"_quality"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		return false, err
	}
	q := stored.Quality
	if q == nil || q.Version != 1 || len(q.Inputs) == 0 || len(q.Inputs) != b.ArticleCount || len(b.Bullets) < 3 || len(b.Bullets) > 6 {
		return false, nil
	}
	valid, err := ValidateNewsInputs(ctx, s.db, q.Inputs)
	if !valid || err != nil {
		return valid, err
	}
	for _, bl := range b.Bullets {
		if len(bl.Sources) == 0 {
			return false, nil
		}
		for _, id := range bl.Sources {
			found := false
			for _, n := range q.Inputs {
				found = found || n.ID == id
			}
			if !found {
				return false, nil
			}
		}
	}
	return true, nil
}

// BriefInputs returns the already-validated complete inputs for a specific
// brief. Signals retain these snapshots for their sentiment dependency.
func (s *Service) BriefInputs(ctx context.Context, id string) ([]NewsItem, error) {
	var raw []byte
	err := s.db.QueryRow(ctx, `select brief from market_briefs where id=$1`, id).Scan(&raw)
	if err != nil {
		return nil, err
	}
	var b MarketBrief
	if err = json.Unmarshal(raw, &b); err != nil {
		return nil, err
	}
	valid, err := s.validBrief(ctx, raw, b)
	if err != nil {
		return nil, err
	}
	if !valid {
		return nil, nil
	}
	var stored struct {
		Quality InputQuality `json:"_quality"`
	}
	json.Unmarshal(raw, &stored)
	return stored.Quality.Inputs, nil
}

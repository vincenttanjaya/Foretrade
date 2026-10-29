package forecast

import (
	"cmp"
	"context"
	"errors"
	"math"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
)

// Quote is one open Polymarket Yes/No market, for the agent chat's
// get_market_odds tool.
type Quote struct {
	Question, URL, EndDate string // URL: the market's event page; EndDate as Gamma gives it
	YesPrice               float64
	VolumeUSD              *float64 // nil when Gamma has none
}

const oddsBudget = 8 * time.Second

// SearchOdds finds up to 3 open Polymarket markets for a free-text event,
// busiest first. It searches the model's Polymarket-style queries, then the
// query's keywords, then the query itself, until a search finds markets.
func SearchOdds(ctx context.Context, d *app.Deps, query string) ([]Quote, error) {
	if d.PolymarketBase == "" {
		return nil, errors.New("prediction-market search is off")
	}
	ctx, cancel := context.WithTimeout(ctx, oddsBudget)
	defer cancel()
	h := &handler{d}
	var cands []candidate
	tried := map[string]bool{}
	for _, q := range append(h.marketQueries(ctx, Draft{Question: query}), marketQuery(query), query) {
		k := strings.ToLower(q)
		if q == "" || tried[k] {
			continue
		}
		tried[k] = true
		var err error
		if cands, err = h.searchMarkets(ctx, q); err != nil {
			return nil, err
		}
		if len(cands) > 0 {
			break
		}
	}
	vol := func(c candidate) float64 {
		if c.volume == nil {
			return 0
		}
		return *c.volume
	}
	slices.SortStableFunc(cands, func(a, b candidate) int { return cmp.Compare(vol(b), vol(a)) })
	out := []Quote{}
	seen := map[string]bool{}
	for _, c := range cands {
		if seen[c.question] || len(out) == 3 {
			continue
		}
		seen[c.question] = true
		out = append(out, Quote{Question: c.question, URL: "https://polymarket.com/event/" + url.PathEscape(c.event), EndDate: c.endDate,
			YesPrice: math.Round(c.yes*1e4) / 1e4, VolumeUSD: c.volume})
	}
	return out, nil
}

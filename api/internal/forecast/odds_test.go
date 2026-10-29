package forecast

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
)

// oddsGamma answers Gamma's public search with events (slug → markets as
// {question, yes, volume}) for the queries in byQuery, nothing for others, and
// records every query.
func oddsGamma(t *testing.T, byQuery map[string]map[string][][3]any) (string, func() []string) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		mu.Lock()
		seen = append(seen, q)
		mu.Unlock()
		events := []map[string]any{}
		for slug, ms := range byQuery[q] {
			var markets []map[string]any
			for _, m := range ms {
				yes := m[1].(float64)
				prices, _ := json.Marshal([]string{strconv.FormatFloat(yes, 'f', -1, 64), strconv.FormatFloat(1-yes, 'f', -1, 64)})
				markets = append(markets, map[string]any{"question": m[0], "outcomes": `["Yes", "No"]`, "outcomePrices": string(prices),
					"endDate": "2026-12-10T19:00:00Z", "active": true, "closed": false, "volumeNum": m[2]})
			}
			events = append(events, map[string]any{"slug": slug, "markets": markets})
		}
		json.NewEncoder(w).Encode(map[string]any{"events": events})
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), seen...) }
}

// SearchOdds searches the query writer's terms first and returns the 3
// busiest markets, one per question; with no writer terms it falls back to
// the keywords, then the query; it is off without a Gamma base.
func TestSearchOdds(t *testing.T) {
	ctx := context.Background()
	base, seen := oddsGamma(t, map[string]map[string][][3]any{
		"fed december cut": {
			"fed-decision-in-december": {{"Fed decreases rates by 25 bps after December meeting?", 0.65004, 9e6},
				{"No change in Fed rates after December meeting?", 0.3, 5e6}, {"Fed decreases rates by 50+ bps after December meeting?", 0.04, 1e6},
				{"Fed increases rates after December meeting?", 0.01, nil}},
			"fed-december-2026-repeat": {{"Fed decreases rates by 25 bps after December meeting?", 0.6, 2e6}},
		},
		"How likely is the Fed to cut in December?": {"fed-raw": {{"Fed cuts in December?", 0.62, 100.0}}},
	})
	ai, _ := llmtest.New(t, func(r llmtest.Request) llmtest.Reply {
		if r.Prompt == "Question: How likely is the Fed to cut in December?" {
			return llmtest.Reply{Text: `{"queries": []}`}
		}
		return llmtest.Reply{Text: `{"queries": ["fed december cut"]}`}
	})
	d := &app.Deps{PolymarketBase: base, LLM: ai}

	qs, err := SearchOdds(ctx, d, "Will the Fed cut rates in December?")
	if err != nil || len(qs) != 3 {
		t.Fatalf("%+v %v", qs, err)
	}
	if q := qs[0]; q.Question != "Fed decreases rates by 25 bps after December meeting?" || q.YesPrice != 0.65 || *q.VolumeUSD != 9e6 ||
		q.URL != "https://polymarket.com/event/fed-decision-in-december" || q.EndDate != "2026-12-10T19:00:00Z" {
		t.Fatalf("top %+v", q)
	}
	if qs[1].Question != "No change in Fed rates after December meeting?" || qs[2].Question != "Fed decreases rates by 50+ bps after December meeting?" {
		t.Fatalf("order %+v", qs)
	}
	if got := seen(); len(got) != 1 || got[0] != "fed december cut" {
		t.Fatalf("queries %q", got)
	}

	// No writer terms: the keywords find nothing, the query itself does.
	raw := "How likely is the Fed to cut in December?"
	if qs, err := SearchOdds(ctx, d, raw); err != nil || len(qs) != 1 || qs[0].Question != "Fed cuts in December?" {
		t.Fatalf("fallback %+v %v", qs, err)
	}
	if got := seen()[1:]; len(got) != 2 || got[0] != marketQuery(raw) || got[1] != raw {
		t.Fatalf("fallback queries %q", got)
	}
	if qs, err := SearchOdds(ctx, &app.Deps{PolymarketBase: base}, "Will aliens land?"); err != nil || len(qs) != 0 || qs == nil {
		t.Fatalf("no match: %+v %v", qs, err)
	}
	if _, err := SearchOdds(ctx, &app.Deps{}, "anything"); err == nil || err.Error() != "prediction-market search is off" {
		t.Fatalf("off: %v", err)
	}
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "down", 500) }))
	defer down.Close()
	if _, err := SearchOdds(ctx, &app.Deps{PolymarketBase: down.URL}, "anything"); err == nil {
		t.Fatal("gamma down: want an error")
	}
}

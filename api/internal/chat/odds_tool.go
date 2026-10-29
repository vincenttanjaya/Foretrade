package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/vincenttanjaya/Foretrade/api/internal/forecast"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm"
	"github.com/vincenttanjaya/Foretrade/api/internal/research"
)

const maxOdds = 2 // get_market_odds calls per message

func (a *agentRun) oddsTool() llm.Tool {
	return llm.Tool{
		Name: "get_market_odds",
		Description: "Live Polymarket prediction-market odds for a real-world event: up to 3 open Yes/No markets matching the " +
			"query, busiest first, each with its question, Yes price (the crowd's probability, 0..1), volume (USD) and end " +
			"date, numbered [n]. At most 2 calls per message.",
		Schema: schema(map[string]any{
			"query": map[string]any{"type": "string", "minLength": 3, "maxLength": 200, "description": "The event in plain words, e.g. \"Fed rate cut December\"."},
		}, "query"),
		Run: a.getMarketOdds,
	}
}

// getMarketOdds numbers each market as a source of kind "market" so the
// answer cites it as [n] and the sources card lists it.
func (a *agentRun) getMarketOdds(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct{ Query string }
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid input: %v", err)
	}
	query := strings.Join(strings.Fields(in.Query), " ")
	switch n := utf8.RuneCountInString(query); {
	case n < 3 || n > 200:
		return "", errors.New("query must be 3..200 characters")
	case a.odds >= maxOdds:
		return "", fmt.Errorf("prediction-market limit reached (%d per message); answer from what you have", maxOdds)
	}
	a.odds++
	quotes, err := forecast.SearchOdds(ctx, a.d, query)
	if err != nil {
		slog.Warn("chat get_market_odds", "err", err)
		a.summary = "Prediction markets unavailable"
		return "", errors.New("prediction-market odds are unavailable right now; answer without them")
	}
	var lines []string
	dropped := 0
	for _, q := range quotes {
		// ponytail: an event's markets share its page; the #question fragment keeps one [n] per market.
		n, ok := a.src.add("market", q.Question, q.URL+"#"+strings.ReplaceAll(normalize(q.Question), " ", "-"), "Polymarket", nil)
		if !ok {
			dropped++
			continue
		}
		b, _ := json.Marshal(map[string]any{"n": n, "question": q.Question, "yesPrice": q.YesPrice, "volumeUsd": q.VolumeUSD, "endDate": q.EndDate})
		lines = append(lines, string(b))
	}
	if len(lines) == 0 && dropped == 0 {
		a.summary = "No matching Polymarket markets"
		return fmt.Sprintf("No open Polymarket market matches %q. Say so; the desk can estimate it with draft_forecast.", query), nil
	}
	a.summary = "Polymarket · " + plural(len(lines), "market")
	return "Polymarket markets, busiest first. Prediction-market prices are crowd odds, not facts; cite them as [n]." + droppedNote(dropped) +
		"\n" + research.Untrusted(strings.Join(lines, "\n")), nil
}

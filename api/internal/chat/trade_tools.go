package chat

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
)

// levels is a stop and target pair; nil = none (current) or keep (proposed).
type levels struct {
	StopLoss   *float64 `json:"stopLoss"`
	TakeProfit *float64 `json:"takeProfit"`
}

// protection is the "protection" card: new levels for a position the user
// holds, applied by PATCH /api/portfolio/positions/{symbol} on Confirm.
type protection struct {
	Symbol    string  `json:"symbol"`
	Side      string  `json:"side"` // long | short
	Qty       float64 `json:"qty"`  // absolute
	MarkPrice float64 `json:"markPrice"`
	Current   levels  `json:"current"`
	Proposed  levels  `json:"proposed"`
	Rationale string  `json:"rationale"`
}

// tradeTools are the position-management and perps-context tools.
func (a *agentRun) tradeTools() []llm.Tool {
	symbol := map[string]any{"type": "string", "enum": symbols(), "description": "Ticker from the universe, e.g. BTC."}
	return []llm.Tool{
		{
			Name: "set_protection",
			Description: "Use this, never a trade card, when the user asks to set, move or add a stop or target on a position " +
				"they hold, or as the single warranted recommendation during an explicitly requested portfolio review. The user confirms on the card. Shows a protection card for the open position; it changes nothing " +
				"itself. Levels must sit on the right side of the live price: long → stop_loss below and take_profit above; " +
				"short → the reverse. Omit a level to keep the current one. Returns the card's numbers (distances %, dollars " +
				"at risk to the stop, reward:risk).",
			Schema: schema(map[string]any{
				"symbol":      symbol,
				"stop_loss":   map[string]any{"type": "number", "description": "New stop price (USD). Omit to keep the current stop."},
				"take_profit": map[string]any{"type": "number", "description": "New target price (USD). Omit to keep the current target."},
				"rationale":   map[string]any{"type": "string", "maxLength": 300, "description": "One or two sentences shown on the card."},
			}, "symbol", "rationale"),
			Run: a.setProtection,
		},
		{
			Name: "get_derivatives",
			Description: "Perpetual-futures positioning from Hyperliquid: funding (hourly, annualized), open interest USD, " +
				"mark premium, 24h volume. Use for positioning, crowding, leverage or funding questions. Context only: " +
				"crypto paper trades fill at Binance spot; stock and index perps at this perp's mark.",
			Schema: schema(map[string]any{"symbol": symbol}, "symbol"),
			Run:    a.getDerivatives,
		},
	}
}

// setProtection checks new levels against the live price and the user's
// position and shows a card. It never touches the position: Confirm does.
func (a *agentRun) setProtection(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Symbol     string   `json:"symbol"`
		StopLoss   *float64 `json:"stop_loss"`
		TakeProfit *float64 `json:"take_profit"`
		Rationale  string   `json:"rationale"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid input: %v", err)
	}
	symbol, err := lookup(in.Symbol)
	if err != nil {
		return "", err
	}
	in.Rationale = strings.TrimSpace(in.Rationale)
	switch {
	case in.StopLoss == nil && in.TakeProfit == nil:
		return "", errors.New("give stop_loss, take_profit or both")
	case in.Rationale == "":
		return "", errors.New("rationale is required")
	case utf8.RuneCountInString(in.Rationale) > 300:
		return "", errors.New("rationale must be at most 300 characters")
	}
	price, err := a.d.Market.Price(ctx, symbol)
	if err != nil {
		return "", toolErr(err)
	}
	pf, err := a.d.Paper.Portfolio(ctx, a.userID)
	if err != nil {
		return "", toolErr(err)
	}
	var pos *paper.Position
	for i := range pf.Positions {
		if pf.Positions[i].Symbol == symbol {
			pos = &pf.Positions[i]
		}
	}
	if pos == nil {
		return "", fmt.Errorf("No open position in %s; use propose_trade to open one.", symbol)
	}
	if err := paper.CheckProtection(pos.Qty, price, in.StopLoss, in.TakeProfit); err != nil {
		return "", err
	}
	side := "long"
	if pos.Qty < 0 {
		side = "short"
	}
	card := protection{Symbol: symbol, Side: side, Qty: math.Abs(pos.Qty), MarkPrice: price,
		Current:  levels{pos.StopLoss, pos.TakeProfit},
		Proposed: levels{in.StopLoss, in.TakeProfit}, Rationale: in.Rationale}
	a.card = &Card{Type: "protection", Data: card}

	stop, target := cmp.Or(in.StopLoss, pos.StopLoss), cmp.Or(in.TakeProfit, pos.TakeProfit) // the levels after Confirm
	var chip []string
	out := map[string]any{
		"status": "Card shown to the user. NOT applied: nothing changes until they tap Confirm protection (paper trading only). " +
			"A level you omitted is kept; the card shows it as (unchanged).",
		"card":         card,
		"afterConfirm": levels{stop, target},
	}
	if stop != nil {
		chip = append(chip, "stop "+usd(*stop))
		out["stopDistancePct"] = round2((*stop/price - 1) * 100)
		out["usdAtRisk"] = round2(card.Qty * math.Abs(price-*stop))
	}
	if target != nil {
		chip = append(chip, "target "+usd(*target))
		out["targetDistancePct"] = round2((*target/price - 1) * 100)
	}
	if stop != nil && target != nil {
		out["rewardToRisk"] = round2(math.Abs(*target-price) / math.Abs(price-*stop))
	}
	a.summary = fmt.Sprintf("Protect %s %s · %s", symbol, side, strings.Join(chip, " · "))
	return toJSON(out)
}

func (a *agentRun) getDerivatives(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct{ Symbol string }
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid input: %v", err)
	}
	symbol, err := lookup(in.Symbol)
	if err != nil {
		return "", err
	}
	d, err := a.d.Market.Derivatives(ctx, symbol)
	if err != nil {
		slog.Warn("chat tool: derivatives", "err", err)
	}
	if d == nil {
		a.summary = "No perp data for " + symbol
		return fmt.Sprintf("No perp data for %s right now.", symbol), nil
	}
	a.summary = fmt.Sprintf("%s funding %+.4f%%/h · OI %s", symbol, d.FundingRatePct, compactUSD(d.OpenInterestUsd))
	return toJSON(d)
}

// compactUSD formats large dollar amounts for chips: $1.2B · $340.5M · $12.3K.
func compactUSD(x float64) string {
	for _, u := range []struct {
		div    float64
		suffix string
	}{{1e9, "B"}, {1e6, "M"}, {1e3, "K"}} {
		if math.Abs(x) >= u.div {
			return fmt.Sprintf("$%.1f%s", x/u.div, u.suffix)
		}
	}
	return usd(x)
}

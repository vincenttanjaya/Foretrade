package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm"
	"github.com/vincenttanjaya/Foretrade/api/internal/research"
	"github.com/vincenttanjaya/Foretrade/api/internal/signals"
)

// boardPick is a Signals board pick as the agent reads it: the board's own
// numbers plus the move since the pick at the live price (null when live
// prices are unavailable). Headlines stay on the Signals page.
type boardPick struct {
	Symbol            string          `json:"symbol"`
	Side              string          `json:"side"`
	Score             int             `json:"score"`
	PriceAtPick       float64         `json:"priceAtPick"`
	PickedAt          time.Time       `json:"pickedAt"`
	Target            float64         `json:"target"`
	Invalidation      float64         `json:"invalidation"`
	ValidUntil        time.Time       `json:"validUntil"`
	VolTier           string          `json:"volTier"`
	Factors           signals.Factors `json:"factors"`
	Rationale         string          `json:"rationale"`
	LivePrice         *float64        `json:"livePrice"`
	SincePickPct      *float64        `json:"sincePickPct"`      // signed in the pick's direction
	TargetDistancePct *float64        `json:"targetDistancePct"` // (target / live − 1) × 100
}

func (a *agentRun) signalsTool() llm.Tool {
	return llm.Tool{
		Name: "get_signals",
		Description: "The Signals board: a momentum screen (shared, recomputed every 4 h), not a forecast. Up to 4 bullish (strongest " +
			"momentum) and 4 bearish (weakest) picks with score -100..100, factors, target and invalidation (2 ATR each way), pick time, " +
			"valid-until (72 h), the move since the pick in its direction, and the board's track record (every pick scored at 72 h " +
			"against a random pick from the same list) with its verdict. Use for questions about the board or one of its picks.",
		Schema: schema(map[string]any{}),
		Run:    a.getSignals,
	}
}

// recordSide is one side of the board's track record as the agent reads it.
type recordSide struct {
	Verdict    string   `json:"verdict"` // insufficient | no_difference | better | worse
	Scored     int      `json:"scored"`
	ExcessPct  *float64 `json:"excessPct"` // vs a random pick from the same list, 72 h, 99% CI
	ExcessLo99 *float64 `json:"excessLo99"`
	ExcessHi99 *float64 `json:"excessHi99"`
}

func sideOf(s signals.RecordSide) recordSide {
	return recordSide{Verdict: s.Verdict, Scored: s.Scored, ExcessPct: s.ExcessPct, ExcessLo99: s.ExcessLo99, ExcessHi99: s.ExcessHi99}
}

// getSignals reads the latest board and its track record, with live prices
// from one Assets call.
func (a *agentRun) getSignals(ctx context.Context, _ json.RawMessage) (string, error) {
	b, err := signals.Latest(ctx, a.d.DB)
	if err != nil {
		return "", toolErr(err)
	}
	if b == nil {
		a.summary = "No valid board"
		return "No valid signals board right now; a refresh is due.", nil
	}
	live := map[string]float64{}
	if assets, err := a.d.Market.Assets(ctx); err != nil {
		slog.Warn("chat: signals live prices", "err", err)
	} else {
		for _, x := range assets {
			live[x.Symbol] = x.Price
		}
	}
	picks := func(ps []signals.Pick) []boardPick {
		out := make([]boardPick, len(ps))
		for i, p := range ps {
			out[i] = boardPick{Symbol: p.Symbol, Side: p.Side, Score: p.Score, PriceAtPick: p.PriceAtPick, PickedAt: p.PickedAt,
				Target: p.Target, Invalidation: p.Invalidation, ValidUntil: p.ValidUntil, VolTier: p.VolTier, Factors: p.Factors, Rationale: p.Rationale}
			if px := live[p.Symbol]; px > 0 && p.PriceAtPick > 0 {
				since := round2((px/p.PriceAtPick - 1) * 100)
				if p.Side == "bearish" {
					since = -since
				}
				dist := round2((p.Target/px - 1) * 100)
				out[i].LivePrice, out[i].SincePickPct, out[i].TargetDistancePct = &px, &since, &dist
			}
		}
		return out
	}
	rec, err := signals.LoadRecord(ctx, a.d.DB)
	if err != nil {
		return "", toolErr(err)
	}
	a.summary = fmt.Sprintf("%d bullish · %d bearish", len(b.Bullish), len(b.Bearish))
	board := map[string]any{"boardAt": b.CreatedAt, "nextAt": b.NextAt, "method": b.Method,
		"bullish": picks(b.Bullish), "bearish": picks(b.Bearish),
		"record": map[string]any{"rule": rec.Rule, "since": rec.Since, "horizonHours": rec.HorizonHours, "minPicks": rec.MinPicks,
			"all": sideOf(rec.All), "bullish": sideOf(rec.Bullish), "bearish": sideOf(rec.Bearish),
			"replay": map[string]any{"from": rec.Replay.From, "to": rec.Replay.To, "picks": rec.Replay.Picks,
				"excessPct": rec.Replay.ExcessPct, "excessLo95": rec.Replay.ExcessLo95, "excessHi95": rec.Replay.ExcessHi95}}}
	if age := time.Since(b.CreatedAt).Hours(); age > 8 { // a board refreshes every 4 h: an old one is the agent's to flag
		board["boardAgeHours"] = math.Round(age*10) / 10
	}
	out, err := toJSON(board)
	// The rationale is model-written from headlines: data, never instructions.
	return "The Signals board as the user sees it.\n" + research.Untrusted(out), err
}

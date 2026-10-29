package paper

import (
	"context"
	"time"
)

type TradeFilter struct {
	Limit  int
	Symbol string
}

// Attribution is complete since-reset FIFO and outcome stakes, as on HTTP.
type Attribution struct {
	Sources  []SourcePnl `json:"sources"`
	TotalPnl float64     `json:"totalPnl"`
}

func (e *Engine) Attribution(ctx context.Context, userID string) (Attribution, error) {
	a, _, err := e.AttributionWithEquity(ctx, userID, "", time.Time{})
	return a, err
}

// AttributionWithEquity derives both scopes from the same captured owned
// ledger. The optional curve still uses the existing selected-range replay;
// market history is fetched after the account transaction has been released.
func (e *Engine) AttributionWithEquity(ctx context.Context, userID, rng string, now time.Time) (Attribution, *EquityCurve, error) {
	if rng != "" {
		if _, ok := ranges[rng]; !ok {
			return Attribution{}, nil, invalid("range must be 1d, 7d or 30d")
		}
	}
	p, trades, stakes, err := e.history(ctx, userID)
	if err != nil {
		return Attribution{}, nil, err
	}
	marks := map[string]float64{}
	for _, pos := range p.Positions {
		marks[pos.Symbol] = pos.MarkPrice
	}
	a := Attribution{Sources: withPredictions(attribute(trades, marks, bySource), stakes), TotalPnl: p.TotalPnl}
	var curve *EquityCurve
	if rng != "" {
		c, err := e.equitySnapshot(ctx, p, trades, stakes, rng, now)
		if err != nil {
			return Attribution{}, nil, err
		}
		curve = &c
	}
	return a, curve, nil
}

// Equity reuses the existing full-ledger selected-range replay and live end.
func (e *Engine) Equity(ctx context.Context, userID, rng string, now time.Time) (EquityCurve, error) {
	return e.equity(ctx, userID, rng, now)
}

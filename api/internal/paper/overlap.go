package paper

import (
	"context"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"slices"
	"time"
)

// Overlap is a response-local projection of a basket against one caller's
// captured current book. It contains no model or persisted basket state.
type Overlap struct {
	Revision     int64            `json:"revision"`
	AsOf         time.Time        `json:"asOf"`
	Holdings     []OverlapHolding `json:"holdings"`
	Correlations []Correlation    `json:"correlations"`
	Coverage     ReviewCoverage   `json:"coverage"`
}
type OverlapHolding struct {
	Symbol    string  `json:"symbol"`
	Side      string  `json:"side"`
	BookPct   float64 `json:"bookPct"`
	BasketPct float64 `json:"basketPct"`
	Shared    bool    `json:"shared"`
}

func (e *Engine) Overlap(ctx context.Context, userID string, basket map[string]float64, now time.Time) (Overlap, error) {
	r, err := e.Review(ctx, userID, now)
	if err != nil {
		return Overlap{}, err
	}
	out := Overlap{Revision: r.Revision, AsOf: r.AsOf, Holdings: []OverlapHolding{}, Correlations: []Correlation{}, Coverage: r.Coverage}
	out.Coverage.MissingSymbols = slices.Clone(r.Coverage.MissingSymbols)
	symbols := []string{}
	for s := range basket {
		symbols = append(symbols, s)
	}
	slices.Sort(symbols)
	want := slices.Clone(symbols)
	for _, h := range r.Holdings {
		w, ok := basket[h.Symbol]
		out.Holdings = append(out.Holdings, OverlapHolding{h.Symbol, h.Side, h.Pct, round(w*100, 1), ok})
		if !slices.Contains(want, h.Symbol) {
			want = append(want, h.Symbol)
		}
	}
	if len(r.Holdings) == 0 {
		return out, nil
	}
	history, _, _ := e.dailyReturns(ctx, want)
	comparisonUnavailable := false
	for _, s := range symbols {
		for _, h := range r.Holdings {
			a, b := history.bySymbol[s], history.bySymbol[h.Symbol]
			pair := Correlation{A: s, B: h.Symbol, Days: sharedDays(a, b)}
			if rho, ok := market.Correlation(a, b, reviewMinDays); ok {
				pair.Rho = ptrTo(round(rho, 2))
			} else {
				if !comparisonUnavailable {
					// Book weights may use entry-price estimates: keep that provenance
					// when adding an independent comparison-coverage warning.
					reason := "Some basket-to-book correlations are unavailable: insufficient common observations or flat return variance; unknown does not imply independence."
					if out.Coverage.Reason != nil {
						reason = *out.Coverage.Reason + " " + reason
					}
					out.Coverage.Reason = ptrTo(reason)
					comparisonUnavailable = true
				}
				for _, symbol := range []string{s, h.Symbol} {
					if !slices.Contains(out.Coverage.MissingSymbols, symbol) {
						out.Coverage.MissingSymbols = append(out.Coverage.MissingSymbols, symbol)
					}
				}
			}
			out.Correlations = append(out.Correlations, pair)
		}
	}
	return out, nil
}

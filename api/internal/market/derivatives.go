package market

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/httpx"
)

// Derivatives is perpetual-futures positioning for one asset from Hyperliquid
// (public info API, no key). Context for a crypto pair (paper trades it spot);
// for a stock or index it is the very perp whose mark the paper engine fills at.
type Derivatives struct {
	Symbol               string    `json:"symbol"`
	Venue                string    `json:"venue"` // "Hyperliquid"
	MarkPrice            float64   `json:"markPrice"`
	FundingRatePct       float64   `json:"fundingRatePct"`       // hourly funding × 100
	FundingAnnualizedPct float64   `json:"fundingAnnualizedPct"` // fundingRatePct × 24 × 365
	OpenInterestUsd      float64   `json:"openInterestUsd"`      // open interest (coins) × mark
	PremiumPct           float64   `json:"premiumPct"`           // mark premium over the oracle × 100
	Volume24hUsd         float64   `json:"volume24hUsd"`
	AsOf                 time.Time `json:"asOf"`
}

const derivativesTTL = 60 * time.Second

// perpBook is one metaAndAssetCtxs answer by universe symbol, or the error
// fetching it: both are cached for derivativesTTL.
type perpBook struct {
	bySymbol map[string]Derivatives
	err      error
}

// Derivatives is the asset's perp context, nil when Hyperliquid lists no
// live perp for it (or derived context arithmetic is unrepresentable). A crypto pair reads
// Hyperliquid's own perps: one answer serves every pair for 60 s, and so does
// a failure. A stock or index reads its xyz-dex perp from the answer Assets
// uses (10 s).
func (c *Client) Derivatives(ctx context.Context, symbol string) (*Derivatives, error) {
	info, ok := Lookup(symbol)
	if !ok {
		return nil, fmt.Errorf("unknown symbol %q", symbol)
	}
	if info.QuoteFeed() {
		return nil, nil // priced from Yahoo: there is no perp
	}
	if info.Perp() {
		book, err := c.xyzMarkets(ctx)
		if err != nil {
			return nil, err
		}
		if x, ok := book.bySymbol[info.Symbol]; ok {
			if d, ok := derivativesOf(info.Symbol, x, book.at); ok {
				return &d, nil
			}
		}
		return nil, nil
	}
	book, err := c.perps.get(ctx, derivativesTTL, func() (perpBook, error) {
		by, err := c.fetchPerps(ctx)
		if errors.Is(err, context.Canceled) {
			return perpBook{}, err // the caller gave up: not worth caching
		}
		return perpBook{by, err}, nil
	})
	if err = cmp.Or(err, book.err); err != nil {
		return nil, err
	}
	if d, ok := book.bySymbol[info.Symbol]; ok {
		return &d, nil
	}
	return nil, nil
}

// fetchPerps maps Hyperliquid's own perps onto the crypto pairs, by exact
// name (a stock symbol listed there is not ours).
func (c *Client) fetchPerps(ctx context.Context) (map[string]Derivatives, error) {
	m, err := c.fetchMeta(ctx, "")
	if err != nil {
		return nil, err
	}
	now, out := time.Now().UTC(), map[string]Derivatives{}
	for _, a := range CryptoPairs {
		if x, ok := m[a.Symbol]; ok {
			if d, ok := derivativesOf(a.Symbol, x, now); ok {
				out[a.Symbol] = d
			}
		}
	}
	return out, nil
}

func derivativesOf(symbol string, x perpCtx, at time.Time) (Derivatives, bool) {
	funding := num(x.Funding) * 100
	d := Derivatives{Symbol: symbol, Venue: "Hyperliquid", MarkPrice: num(x.MarkPx),
		FundingRatePct: funding, FundingAnnualizedPct: funding * 24 * 365, OpenInterestUsd: num(x.OpenInterest) * num(x.MarkPx),
		PremiumPct: num(x.Premium) * 100, Volume24hUsd: num(x.DayNtlVlm), AsOf: at}
	for _, v := range []float64{d.MarkPrice, d.FundingRatePct, d.FundingAnnualizedPct, d.OpenInterestUsd, d.PremiumPct, d.Volume24hUsd} {
		if !finite(v) {
			return Derivatives{}, false
		}
	}
	return d, true
}

// handleDerivatives is GET /api/market/derivatives?symbol=BTC: the perp
// context or null, never a 502 (it is decoration on the trade page).
func (c *Client) handleDerivatives(w http.ResponseWriter, r *http.Request) {
	info, ok := Lookup(r.URL.Query().Get("symbol"))
	if !ok {
		httpx.Error(w, http.StatusBadRequest, "bad_request", "Unknown symbol.")
		return
	}
	d, err := c.Derivatives(r.Context(), info.Symbol)
	if err != nil {
		slog.Warn("market derivatives", "err", err)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"derivatives": d})
}

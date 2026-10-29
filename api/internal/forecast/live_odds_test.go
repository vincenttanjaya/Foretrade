package forecast

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
)

// LiveOdds re-prices a stored market from Gamma and stores it (any owner, no
// request); a "below" rule keeps its flip; a Gamma error or a forecast without
// a market leaves the row as it was.
func TestLiveOdds(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	ctx := context.Background()
	yes := 0.88
	e.PolymarketBase = gammaEvents(t, "largest-company-end-of-december-2026", func() []map[string]any {
		return []map[string]any{gammaMarket(nvdaQ, "2027-01-01T00:00:00Z", yes, 1.4e6)}
	})
	id := stored(t, e, e.User, nvdaDraft, nvdaResult(), "")
	storedMarket := func(id string) *MarketOdds {
		t.Helper()
		var m *MarketOdds
		if err := e.DB.QueryRow(ctx, `select result->'market' from forecasts where id = $1`, id).Scan(&m); err != nil {
			t.Fatal(err)
		}
		return m
	}

	m, err := LiveOdds(ctx, e.Deps, id)
	if err != nil || m.YesPrice != 0.88 || m.EdgePp != -7 || m.Signal != "no" || m.Outcome != "Nvidia" || time.Since(m.AsOf) > time.Minute {
		t.Fatalf("re-priced: %v %+v", err, m)
	}
	if got := storedMarket(id); got.YesPrice != 0.88 || !got.AsOf.Equal(m.AsOf) || got.EdgePp != -7 {
		t.Fatalf("not stored: %+v", got)
	}
	var probs int
	if e.DB.QueryRow(ctx, `select jsonb_array_length(result->'probabilities') from forecasts where id = $1`, id).Scan(&probs); probs != 3 {
		t.Fatalf("the rest of the result changed: %d probabilities", probs)
	}

	// Gamma down: the error comes back, the stored market stays.
	e.PolymarketBase = "http://127.0.0.1:1"
	if _, err := LiveOdds(ctx, e.Deps, id); err == nil || storedMarket(id).YesPrice != 0.88 {
		t.Fatalf("gamma down: %v %+v", err, storedMarket(id))
	}

	// A "below" rule prices 1 − the market's Yes.
	btc := "Will the price of Bitcoin be above $120,000 on December 31?"
	e.PolymarketBase = gammaEvents(t, "bitcoin-above-on-december-31", func() []map[string]any {
		return []map[string]any{gammaMarket(btc, "2099-12-31T12:00:00Z", 0.3, 5e4)}
	})
	below := stored(t, e, e.User, Draft{Shape: "binary", Question: "Will BTC close below $120,000 on 2099-12-31?", Outcomes: yesNo,
		ResolutionDate: "2099-12-31", PriceRule: &PriceRule{"BTC", "below", 120000, "on_date"}},
		Result{Probabilities: ps("Yes", 0.6, "No", 0.4), Confidence: "high",
			Market: &MarketOdds{Source: "Polymarket", Question: btc, URL: "https://polymarket.com/event/bitcoin-above-on-december-31", YesPrice: 0.5}}, "")
	if m, err := LiveOdds(ctx, e.Deps, below); err != nil || m.YesPrice != 0.7 || m.EdgePp != -10 || storedMarket(below).YesPrice != 0.7 {
		t.Fatalf("below: %v %+v", err, m)
	}

	none := stored(t, e, e.User, nvdaDraft, Result{Probabilities: ps("Nvidia", 0.8, "Apple", 0.2)}, "")
	if _, err := LiveOdds(ctx, e.Deps, none); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("no market: %v", err)
	}
}

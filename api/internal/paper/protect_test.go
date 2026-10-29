package paper

import (
	"context"
	"errors"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
)

// SetStop moves only the stop, checked against the mark like a PATCH.
func TestSetStop(t *testing.T) {
	pool := storetest.Pool(t)
	user := storetest.User(t, pool)
	e := NewEngine(pool, testMarketClient(markettest.New(t).URL))
	ctx := context.Background()
	if err := e.SetStop(ctx, user, "BTC", 90_000); !errors.Is(err, ErrNoPosition) {
		t.Fatalf("no position: %v", err)
	}
	if _, _, err := e.Order(ctx, user, OrderRequest{Symbol: "BTC", Side: "buy", Notional: ptr(1000), StopLoss: ptr(90_000), TakeProfit: ptr(120_000)}); err != nil {
		t.Fatal(err)
	}
	before, _ := e.Portfolio(ctx, user)
	if err := e.SetStop(ctx, user, "BTC", 97_000); err != nil {
		t.Fatal(err)
	}
	var invalid *ValidationError
	if err := e.SetStop(ctx, user, "BTC", 100_500); !errors.As(err, &invalid) { // above the $100,000 mark
		t.Fatalf("stop above the mark: %v", err)
	}
	p, _ := e.Portfolio(ctx, user)
	if pos := p.Positions[0]; *pos.StopLoss != 97_000 || *pos.TakeProfit != 120_000 || p.Revision != before.Revision+1 {
		t.Fatalf("after SetStop: %+v, revision %d after %d", pos, p.Revision, before.Revision)
	}
}

package paper

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
)

// forecastRow inserts a completed forecast of user's for positions to reference.
func forecastRow(t *testing.T, pool *pgxpool.Pool, user string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(), `insert into forecasts (user_id, shape, question, outcomes, resolution_date, status)
		values ($1, 'categorical', 'Largest company?', '["Nvidia","Apple"]', '2099-12-31', 'completed') returning id`, user).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// buy stakes cost on (outcome, side) at price through Stake.
func buy(t *testing.T, e *Engine, user, forecast, outcome, side string, price, cost float64) Portfolio {
	t.Helper()
	p, err := e.Stake(context.Background(), user, cost, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into market_positions (user_id, forecast_id, outcome, side, question, url, price, contracts, cost)
			values ($1, $2, $3, $4, 'Q?', 'https://polymarket.com/event/q', $5, $6, $7)`, user, forecast, outcome, side, price, cost/price, cost)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestStake(t *testing.T) {
	pool := storetest.Pool(t)
	user := storetest.User(t, pool)
	e := NewEngine(pool, testMarketClient(markettest.New(t).URL))
	ctx := context.Background()
	f := forecastRow(t, pool, user)
	start, err := e.Portfolio(ctx, user)
	if err != nil {
		t.Fatal(err)
	}

	p := buy(t, e, user, f, "Nvidia", "yes", 0.86, 100)
	if p.Cash != start.Cash-100 || p.PredictionStakes != 100 || p.Equity != start.Equity || p.Revision != start.Revision+1 {
		t.Fatalf("stake: %+v", p)
	}
	if again, _ := e.Portfolio(ctx, user); again.Cash != p.Cash || again.PredictionStakes != 100 || again.Equity != p.Equity || again.Revision != p.Revision {
		t.Fatalf("read back: %+v vs %+v", again, p)
	}
	// An insert error rolls the cash back; more than the cash is refused.
	boom := errors.New("boom")
	if _, err := e.Stake(ctx, user, 50, func(context.Context, pgx.Tx) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("insert error: %v", err)
	}
	if _, err := e.Stake(ctx, user, start.Cash, func(context.Context, pgx.Tx) error { t.Fatal("inserted"); return nil }); !errors.Is(err, ErrInsufficient) {
		t.Fatalf("over the cash: %v", err)
	}
	if again, _ := e.Portfolio(ctx, user); again.Cash != p.Cash || again.Revision != p.Revision {
		t.Fatalf("a refused stake moved the account: %+v", again)
	}

	// A reset deletes the positions with the trades.
	if r, err := e.Reset(ctx, user); err != nil || r.PredictionStakes != 0 || r.Equity != StartingBalance {
		t.Fatalf("reset: %+v %v", r, err)
	}
	var n int
	if err := pool.QueryRow(ctx, `select count(*) from market_positions where user_id = $1`, user).Scan(&n); err != nil || n != 0 {
		t.Fatalf("positions after reset: %d %v", n, err)
	}
}

// Yes wins on its option, No on any other; a win pays $1 a contract into cash
// and books payout − cost as realized, one revision per position.
func TestSettleTx(t *testing.T) {
	pool := storetest.Pool(t)
	user := storetest.User(t, pool)
	e := NewEngine(pool, testMarketClient(markettest.New(t).URL))
	ctx := context.Background()
	settle := func(f, outcome string) {
		t.Helper()
		if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error { return SettleTx(ctx, tx, []string{f}, outcome) }); err != nil {
			t.Fatal(err)
		}
	}
	status := func(f string) (out []string) {
		rows, _ := pool.Query(ctx, `select status || ':' || coalesce(round(payout::numeric, 2)::text, '-') from market_positions where forecast_id = $1 order by created_at`, f)
		out, _ = pgx.CollectRows(rows, pgx.RowTo[string])
		return out
	}

	// Binary: Yes at 50¢ ($100 → 200 contracts) and No at 25¢ ($50 → 200), resolved Yes.
	bin := forecastRow(t, pool, user)
	buy(t, e, user, bin, "Yes", "yes", 0.5, 100)
	before := buy(t, e, user, bin, "Yes", "no", 0.25, 50)
	settle(bin, "Yes")
	after, _ := e.Portfolio(ctx, user)
	if got := status(bin); len(got) != 2 || got[0] != "won:200.00" || got[1] != "lost:0.00" {
		t.Fatalf("binary: %v", got)
	}
	if after.Cash != before.Cash+200 || after.RealizedPnl != before.RealizedPnl+50 || after.PredictionStakes != 0 || after.Revision != before.Revision+2 ||
		after.Equity != before.Equity+50 {
		t.Fatalf("account: %+v → %+v", before, after)
	}
	settle(bin, "No") // settled positions stay as they are
	if again, _ := e.Portfolio(ctx, user); again.Cash != after.Cash || again.Revision != after.Revision {
		t.Fatalf("settled twice: %+v", again)
	}

	// Categorical: the market prices Nvidia. Resolved Apple: Nvidia Yes loses, Nvidia No wins.
	cat := forecastRow(t, pool, user)
	buy(t, e, user, cat, "Nvidia", "yes", 0.86, 100)
	buy(t, e, user, cat, "Nvidia", "no", 0.16, 16)
	settle(cat, "Apple")
	if got := status(cat); got[0] != "lost:0.00" || got[1] != "won:100.00" {
		t.Fatalf("categorical, another option: %v", got)
	}
	same := forecastRow(t, pool, user)
	buy(t, e, user, same, "Nvidia", "yes", 0.5, 10)
	buy(t, e, user, same, "Nvidia", "no", 0.5, 10)
	settle(same, "Nvidia")
	if got := status(same); got[0] != "won:20.00" || got[1] != "lost:0.00" {
		t.Fatalf("categorical, its option: %v", got)
	}
	var settledAt *time.Time
	if err := pool.QueryRow(ctx, `select max(settled_at) from market_positions where forecast_id = $1`, same).Scan(&settledAt); err != nil || settledAt == nil {
		t.Fatalf("settled_at: %v %v", settledAt, err)
	}
}

// The equity curve and P&L by source count stakes: no dip on buying, the
// settled P&L on the "prediction" row, and Σ sources = total P&L.
func TestHistoryWithPredictions(t *testing.T) {
	pool := storetest.Pool(t)
	user := storetest.User(t, pool)
	e := NewEngine(pool, testMarketClient(markettest.New(t).URL))
	ctx := context.Background()
	notional := 1000.0
	if _, _, err := e.Order(ctx, user, OrderRequest{Symbol: "ETH", Side: "buy", Notional: &notional, Source: "agent"}); err != nil {
		t.Fatal(err)
	}
	won, open := forecastRow(t, pool, user), forecastRow(t, pool, user)
	buy(t, e, user, won, "Nvidia", "yes", 0.8, 100)
	buy(t, e, user, open, "Nvidia", "no", 0.2, 40)
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error { return SettleTx(ctx, tx, []string{won}, "Nvidia") }); err != nil {
		t.Fatal(err)
	}

	p, trades, stakes, err := e.history(ctx, user)
	if err != nil || len(trades) != 1 || len(stakes) != 2 {
		t.Fatalf("history: %d trades, %d stakes, %v", len(trades), len(stakes), err)
	}
	marks := map[string]float64{}
	for _, pos := range p.Positions {
		marks[pos.Symbol] = pos.MarkPrice
	}
	sources := withPredictions(attribute(trades, marks, bySource), stakes)
	sum := 0.0
	var pred *SourcePnl
	for i, s := range sources {
		sum += s.TotalPnl
		if s.Source == "prediction" {
			pred = &sources[i]
		}
	}
	if pred == nil || pred.Trades != 2 || math.Abs(pred.RealizedPnl-25) > 1e-9 || pred.UnrealizedPnl != 0 || pred.Fees != 0 || pred.TotalPnl != pred.RealizedPnl ||
		math.Abs(sum-p.TotalPnl) > 1e-6 || sources[0].Source != "prediction" {
		t.Fatalf("sources %+v, total %v", sources, p.TotalPnl)
	}
	if got := withPredictions([]SourcePnl{}, nil); len(got) != 0 {
		t.Fatalf("no stakes, no row: %+v", got)
	}

	c, err := e.equity(ctx, user, "1d", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if last := c.Points[len(c.Points)-1]; math.Abs(last.V-p.Equity) > 1e-6 {
		t.Fatalf("the curve ends at the live equity: %v vs %v", last.V, p.Equity)
	}
}

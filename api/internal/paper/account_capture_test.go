package paper

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
)

func TestAccountHistoryCapturedBeforeConcurrentReset(t *testing.T) {
	pool := storetest.Schema(t)
	user := storetest.User(t, pool)
	e := NewEngine(pool, testMarketClient(markettest.New(t).URL))
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	n := 1000.0
	if _, _, err := e.Order(ctx, user, OrderRequest{Symbol: "ETH", Side: "buy", Notional: &n}); err != nil {
		t.Fatal(err)
	}
	f := forecastRow(t, pool, user)
	want := buy(t, e, user, f, "Nvidia", "yes", 0.5, 100)
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.WithoutCancel(ctx))
	if _, err := blocker.Exec(ctx, "lock table positions in access exclusive mode"); err != nil {
		t.Fatal(err)
	}
	var relation uint32
	if err := blocker.QueryRow(ctx, "select 'positions'::regclass::oid").Scan(&relation); err != nil {
		t.Fatal(err)
	}
	type capture struct {
		p      Portfolio
		trades []Trade
		stakes []stake
		err    error
	}
	result, done := make(chan capture, 1), make(chan struct{})
	go func() {
		defer close(done)
		p, trades, stakes, err := e.history(ctx, user)
		result <- capture{p, trades, stakes, err}
	}()
	defer func() { cancel(); blocker.Rollback(context.Background()); <-done }()
	// Wait for the owning history's positions query, after its account read.
	// A real table lock makes the capture boundary deterministic, without a
	// production hook or timing-based guess about when the goroutine started.
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `select exists(select 1 from pg_locks l join pg_stat_activity a on a.pid=l.pid
			where l.relation=$1 and not l.granted and a.query like 'select symbol, qty, avg_price%')`, relation).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("history did not reach captured positions read")
		}
	}
	// Reset commits after the capture's first read, before its remaining reads.
	if _, err := reset(ctx, blocker, user); err != nil {
		t.Fatal(err)
	}
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	got := <-result
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.p.Revision != 2 || got.p.Cash != 98899 || got.p.Equity != want.Equity || got.p.PredictionStakes != 100 || !reflect.DeepEqual(got.p.Positions, want.Positions) || len(got.trades) != 1 || len(got.stakes) != 1 || got.stakes[0].settledAt != nil {
		t.Fatalf("mixed old cash with reset ledger: %+v trades%d stakes%d", got.p, len(got.trades), len(got.stakes))
	}
	newP, trades, stakes, err := e.history(ctx, user)
	if err != nil || newP.Equity != StartingBalance || newP.PredictionStakes != 0 || len(trades) != 0 || len(stakes) != 0 {
		t.Fatalf("next capture did not observe committed reset: %+v %v", newP, err)
	}
}

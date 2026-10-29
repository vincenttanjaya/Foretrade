package paper

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
)

// Pause the real reader after cash is read, so a concurrent fill can expose
// a mixed cash/positions snapshot without relying on scheduler timing.
type cashReadGate struct{ read, resume chan struct{} }
type cashReadKey struct{}

func (g *cashReadGate) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, cashReadKey{}, strings.HasPrefix(d.SQL, "select cash, starting_balance, realized_pnl"))
}

func (g *cashReadGate) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	if ctx.Value(cashReadKey{}) == true {
		close(g.read)
		select {
		case <-g.resume:
		case <-ctx.Done():
		}
	}
}

func TestPortfolioDoesNotMixCashAndPositionsAcrossAFill(t *testing.T) {
	pool := storetest.Pool(t)
	user := storetest.User(t, pool)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mkt := testMarketClient(markettest.New(t).URL)
	if _, err := NewEngine(pool, mkt).Portfolio(ctx, user); err != nil {
		t.Fatal(err)
	}
	gate := &cashReadGate{read: make(chan struct{}), resume: make(chan struct{})}
	var once sync.Once
	resume := func() { once.Do(func() { close(gate.resume) }) }
	defer resume()
	rcfg := pool.Config().Copy()
	rcfg.ConnConfig.Tracer = gate
	reader, err := pgxpool.NewWithConfig(ctx, rcfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reader.Close)
	wcfg := pool.Config().Copy()
	appName := "snapshot-writer-" + user
	wcfg.ConnConfig.RuntimeParams["application_name"] = appName
	writer, err := pgxpool.NewWithConfig(ctx, wcfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(writer.Close)
	type result struct {
		p   Portfolio
		err error
	}
	readDone := make(chan result, 1)
	go func() {
		p, err := NewEngine(reader, mkt).Portfolio(ctx, user)
		readDone <- result{p, err}
	}()
	select {
	case <-gate.read:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	writeDone := make(chan error, 1)
	go func() {
		_, _, err := NewEngine(writer, mkt).Order(ctx, user, OrderRequest{Symbol: "BTC", Side: "buy", Notional: ptr(50000)})
		writeDone <- err
	}()
	// Either the old code commits the fill, or a consistent reader blocks it.
	// Observe the actual DB lock instead of assuming a fixed sleep is enough.
	finished := false
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
waitWriter:
	for {
		select {
		case err := <-writeDone:
			if err != nil {
				t.Fatal(err)
			}
			finished = true
			break waitWriter
		case <-tick.C:
			var blocked bool
			if err := pool.QueryRow(ctx, `select exists(select 1 from pg_stat_activity
				where application_name = $1 and cardinality(pg_blocking_pids(pid)) > 0)`, appName).Scan(&blocked); err != nil {
				t.Fatal(err)
			}
			if blocked {
				break waitWriter
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	resume()
	read := <-readDone
	if read.err != nil || read.p.Cash != 100000 || read.p.Equity != 100000 || len(read.p.Positions) != 0 {
		t.Fatalf("mixed account snapshot: %+v, %v", read.p, read.err)
	}
	if !finished {
		if err := <-writeDone; err != nil {
			t.Fatal(err)
		}
	}
}

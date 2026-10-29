package usage

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
)

type transactionalMeter interface {
	SpendTx(context.Context, pgx.Tx, string, int, time.Time) (Credits, error)
	RefundTx(context.Context, pgx.Tx, string, time.Time, int) error
}

func transactionMeter(t *testing.T, m *Meter) transactionalMeter {
	t.Helper()
	txMeter, ok := any(m).(transactionalMeter)
	if !ok {
		t.Fatal("meter cannot atomically account research in its caller's transaction")
	}
	return txMeter
}

func TestSpendTxRollsBackWithCallerState(t *testing.T) {
	pool := storetest.Pool(t)
	user := storetest.User(t, pool)
	m := transactionMeter(t, NewMeter(pool, 5))
	day := time.Date(2026, 10, 5, 23, 59, 0, 0, time.UTC)
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	c, err := m.SpendTx(t.Context(), tx, user, 2, day)
	if err != nil || c.Used != 2 || c.Limit != 5 || !c.ResetsAt.Equal(time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("explicit charged-day spend: %+v, %v", c, err)
	}
	if _, err := tx.Exec(t.Context(), "update users set name='Caller state' where id=$1", user); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	var count int
	var name string
	if err := pool.QueryRow(t.Context(), "select count(*) from ai_usage where user_id=$1", user).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), "select name from users where id=$1", user).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if count != 0 || name != "Test" {
		t.Fatalf("partial rollback: usage rows=%d, name=%q", count, name)
	}
}

func TestSpendTxQuotaReadsSameTransactionAndUTCDay(t *testing.T) {
	pool := storetest.Pool(t)
	user := storetest.User(t, pool)
	// A pool-free meter proves quota reporting does not escape the caller's tx.
	m := transactionMeter(t, NewMeter(nil, 5))
	day := time.Date(2026, 10, 6, 1, 30, 0, 0, time.FixedZone("UTC+2", 2*3600))
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(t.Context(), "update users set credit_limit=3 where id=$1", user); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SpendTx(t.Context(), tx, user, 2, day); err != nil {
		t.Fatal(err)
	}
	c, err := m.SpendTx(t.Context(), tx, user, 2, day)
	if !errors.Is(err, ErrQuota) || c.Used != 2 || c.Limit != 3 || !c.ResetsAt.Equal(time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("quota must see uncommitted owner/day state: %+v, %v", c, err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	var credits int
	if err := pool.QueryRow(t.Context(), "select credits from ai_usage where user_id=$1 and day='2026-10-05'", user).Scan(&credits); err != nil || credits != 2 {
		t.Fatalf("charged day: credits=%d err=%v", credits, err)
	}
}

func TestRefundTxOriginalDayAndAtomicTerminalState(t *testing.T) {
	pool := storetest.Pool(t)
	user := storetest.User(t, pool)
	m := transactionMeter(t, NewMeter(pool, 10))
	day := time.Date(2026, 10, 5, 23, 59, 0, 0, time.UTC)
	if _, err := pool.Exec(t.Context(), "insert into ai_usage(user_id,day,credits) values($1,'2026-10-05',4),($1,'2026-10-06',6)", user); err != nil {
		t.Fatal(err)
	}
	// The terminal CAS is owned by the caller. A failed terminal transaction
	// must roll back its refund, and competing completions must refund once.
	if _, err := pool.Exec(t.Context(), "update users set name='pending' where id=$1", user); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.RefundTx(t.Context(), tx, user, day, 2); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var winners atomic.Int32
	for range 8 {
		wg.Go(func() {
			err := pgx.BeginFunc(t.Context(), pool, func(tx pgx.Tx) error {
				tag, err := tx.Exec(t.Context(), "update users set name='terminal' where id=$1 and name='pending'", user)
				if err != nil || tag.RowsAffected() == 0 {
					return err
				}
				winners.Add(1)
				return m.RefundTx(t.Context(), tx, user, day, 2)
			})
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	var oldDay, newDay int
	if err := pool.QueryRow(t.Context(), "select (select credits from ai_usage where user_id=$1 and day='2026-10-05'),(select credits from ai_usage where user_id=$1 and day='2026-10-06')", user).Scan(&oldDay, &newDay); err != nil {
		t.Fatal(err)
	}
	if winners.Load() != 1 || oldDay != 2 || newDay != 6 {
		t.Fatalf("refund winners=%d old=%d new=%d", winners.Load(), oldDay, newDay)
	}
}

func TestTransactionalAccountingPropagatesCancellation(t *testing.T) {
	pool := storetest.Pool(t)
	user := storetest.User(t, pool)
	m := transactionMeter(t, NewMeter(pool, 5))
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := m.SpendTx(ctx, tx, user, 2, time.Now()); err == nil || errors.Is(err, ErrQuota) {
		t.Fatalf("cancelled debit: %v", err)
	}
	if err := m.RefundTx(ctx, tx, user, time.Now(), 2); err == nil {
		t.Fatal("cancelled refund silently succeeded")
	}
}

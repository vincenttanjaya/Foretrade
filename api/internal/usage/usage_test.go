package usage

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
)

func TestMeter(t *testing.T) {
	pool := storetest.Pool(t)
	user := storetest.User(t, pool)
	ctx := context.Background()
	m := NewMeter(pool, 5)

	if _, err := m.Spend(ctx, user, 6); !errors.Is(err, ErrQuota) {
		t.Fatalf("cost above the limit on a fresh day: err = %v, want ErrQuota", err)
	}
	c, err := m.Spend(ctx, user, 3)
	if err != nil || c.Used != 3 || c.Limit != 5 {
		t.Fatalf("spend 3: %+v, %v", c, err)
	}
	if want := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, 1); !c.ResetsAt.Equal(want) {
		t.Fatalf("resetsAt = %v, want %v", c.ResetsAt, want)
	}
	if c, err = m.Spend(ctx, user, 3); !errors.Is(err, ErrQuota) || c.Used != 3 {
		t.Fatalf("over limit: %+v, %v", c, err)
	}
	if c, err = m.Spend(ctx, user, 2); err != nil || c.Used != 5 {
		t.Fatalf("exactly at limit: %+v, %v", c, err)
	}
	m.Refund(ctx, user, 4)
	if c, err = m.Get(ctx, user); err != nil || c.Used != 1 {
		t.Fatalf("after refund: %+v, %v", c, err)
	}
}

// Concurrent spends must never overshoot the limit: exactly limit/cost succeed.
func TestSpendIsAtomic(t *testing.T) {
	pool := storetest.Pool(t)
	user := storetest.User(t, pool)
	m := NewMeter(pool, 5)
	var wg sync.WaitGroup
	var ok, quota atomic.Int32
	for range 12 {
		wg.Go(func() {
			switch _, err := m.Spend(context.Background(), user, 1); {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, ErrQuota):
				quota.Add(1)
			default:
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if c, _ := m.Get(context.Background(), user); ok.Load() != 5 || quota.Load() != 7 || c.Used != 5 {
		t.Fatalf("ok %d, quota %d, used %d", ok.Load(), quota.Load(), c.Used)
	}
	m.Refund(context.Background(), user, 50)
	if c, _ := m.Get(context.Background(), user); c.Used != 0 {
		t.Fatalf("a refund is floored at 0: %d", c.Used)
	}
}

// A user's credit_limit replaces the default inside Spend's one statement, so
// a change applies to the next call; 0 blocks every call; lowering it below
// today's usage leaves used > limit.
func TestPerUserLimit(t *testing.T) {
	pool := storetest.Pool(t)
	user := storetest.User(t, pool)
	ctx := context.Background()
	m := NewMeter(pool, 5)
	setLimit := func(n any) {
		t.Helper()
		if _, err := pool.Exec(ctx, "update users set credit_limit = $2 where id = $1", user, n); err != nil {
			t.Fatal(err)
		}
	}
	setLimit(8)
	if c, err := m.Spend(ctx, user, 7); err != nil || c.Used != 7 || c.Limit != 8 {
		t.Fatalf("own limit above the default: %+v, %v", c, err)
	}
	if c, err := m.Spend(ctx, user, 2); !errors.Is(err, ErrQuota) || c.Used != 7 || c.Limit != 8 {
		t.Fatalf("over the own limit: %+v, %v", c, err)
	}
	setLimit(3)
	if c, err := m.Get(ctx, user); err != nil || c.Used != 7 || c.Limit != 3 {
		t.Fatalf("lowered below today's usage: %+v, %v", c, err)
	}
	setLimit(nil)
	if c, err := m.Get(ctx, user); err != nil || c.Limit != 5 || m.DefaultLimit() != 5 {
		t.Fatalf("back to the default: %+v, %v", c, err)
	}
	other := storetest.User(t, pool)
	if _, err := pool.Exec(ctx, "update users set credit_limit = 0 where id = $1", other); err != nil {
		t.Fatal(err)
	}
	if c, err := m.Spend(ctx, other, 1); !errors.Is(err, ErrQuota) || c.Used != 0 || c.Limit != 0 {
		t.Fatalf("limit 0 blocks the first call: %+v, %v", c, err)
	}
}

func TestMeterDBErrors(t *testing.T) {
	pool := storetest.Pool(t)
	user := storetest.User(t, pool)
	pool.Close()
	m := NewMeter(pool, 5)
	if _, err := m.Spend(context.Background(), user, 1); err == nil || errors.Is(err, ErrQuota) {
		t.Fatalf("spend on a dead db: %v", err)
	}
	if _, err := m.Get(context.Background(), user); err == nil {
		t.Fatal("get on a dead db must fail")
	}
	m.Refund(context.Background(), user, 1) // best effort: logs, never panics
}

// The real refund waits behind a real row lock. A caller's cleanup deadline
// must survive the layer that shields cancellation.
func TestRefundOnPreservesCleanupDeadline(t *testing.T) {
	pool := storetest.Schema(t)
	user := storetest.User(t, pool)
	m := NewMeter(pool, 5)
	if _, err := m.Spend(t.Context(), user, 2); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	var pid int
	if err := tx.QueryRow(t.Context(), "select pg_backend_pid()").Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), "update ai_usage set credits=credits where user_id=$1", user); err != nil {
		t.Fatal(err)
	}
	cleanup, cleanupCancel := CleanupContext(context.Background())
	defer cleanupCancel()
	ctx, cancel := context.WithTimeout(cleanup, 500*time.Millisecond)
	defer cancel()
	ctx, ownCancel := CleanupContext(ctx)
	defer ownCancel()
	owned := ctx
	ctx = context.WithoutCancel(ctx) // the explicit cleanup budget still owns this shield
	done := make(chan struct{})
	go func() { defer close(done); m.RefundOn(ctx, user, today(), 2) }()
	t.Cleanup(func() {
		tx.Rollback(context.Background())
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("refund driver did not exit")
		}
	})
	for {
		var waiting bool
		if err := pool.QueryRow(t.Context(), "select exists(select 1 from pg_stat_activity where $1=any(pg_blocking_pids(pid)) and wait_event_type='Lock')", pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-owned.Done():
			t.Fatal("refund never entered the real row-lock wait")
		default:
		}
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RefundOn removed the bounded cleanup deadline while waiting for a row lock")
	}
	// Compare against the deadline instant, not owned.Err(): the refund's own
	// timer for the same instant can fire, cancel and return before owned's.
	if deadline, _ := owned.Deadline(); time.Now().Before(deadline) {
		t.Fatalf("refund returned %v before its locked cleanup deadline", time.Until(deadline))
	}
}

func TestCleanupFreshOperationAndNestedBudget(t *testing.T) {
	operation, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	cleanup, closeCleanup := CleanupContext(operation)
	defer closeCleanup()
	if cleanup.Err() != nil {
		t.Fatalf("fresh accepted cleanup inherited spent operation deadline: %v", cleanup.Err())
	}
	deadline, ok := cleanup.Deadline()
	if !ok || time.Until(deadline) > 10*time.Second || time.Until(deadline) < 9*time.Second {
		t.Fatalf("fresh cleanup must have its own finite ceiling: %v", deadline)
	}
	short, closeShort := context.WithTimeout(cleanup, time.Second)
	defer closeShort()
	nested, closeNested := CleanupContext(short)
	defer closeNested()
	want, _ := short.Deadline()
	got, _ := nested.Deadline()
	if !got.Equal(want) {
		t.Fatalf("nested cleanup restarted owned shorter budget: %v want %v", got, want)
	}
	spent, closeSpent := context.WithDeadline(cleanup, time.Now().Add(-time.Second))
	defer closeSpent()
	finished, closeFinished := CleanupContext(spent)
	defer closeFinished()
	if finished.Err() != context.DeadlineExceeded {
		t.Fatalf("spent owned cleanup was restarted: %v", finished.Err())
	}
}

// Owner-only mode refuses non-admins with ErrOwnerOnly (also ErrQuota, so the
// callers' refund paths apply) and charges nothing; admins spend as usual.
func TestOwnerOnly(t *testing.T) {
	pool := storetest.Pool(t)
	user, admin := storetest.User(t, pool), storetest.User(t, pool)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, "update users set role = 'admin' where id = $1", admin); err != nil {
		t.Fatal(err)
	}
	on := true
	m := NewMeter(pool, 5)
	m.OwnerOnly = func() bool { return on }
	if c, err := m.Spend(ctx, user, 1); !errors.Is(err, ErrOwnerOnly) || !errors.Is(err, ErrQuota) || c.Used != 0 || c.Limit != 5 {
		t.Fatalf("non-admin: %+v, %v", c, err)
	}
	if c, err := m.Spend(ctx, admin, 2); err != nil || c.Used != 2 {
		t.Fatalf("admin: %+v, %v", c, err)
	}
	on = false
	if c, err := m.Spend(ctx, user, 1); err != nil || c.Used != 1 {
		t.Fatalf("mode off: %+v, %v", c, err)
	}
}

package alerts

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
)

func TestCanceledPriceCheckDoesNotAdmitNextFire(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	for range 2 {
		create(t, e, `{"symbol":"SOL","op":"below","price":190,"action":"buy","notional":50}`)
	}
	price(e, "SOLUSDT", 180)
	tx, err := e.DB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	var pid int
	if err := tx.QueryRow(t.Context(), "select pg_backend_pid()").Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), "update users set name=name where id=$1", e.User); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- checkPrices(ctx, e.Deps) }()
	t.Cleanup(func() {
		cancel()
		tx.Rollback(context.Background())
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("actual price checker did not exit")
		}
	})
	bound, stop := context.WithTimeout(t.Context(), 5*time.Second)
	defer stop()
	for {
		var waiting bool
		if err := e.DB.QueryRow(bound, "select exists(select 1 from pg_stat_activity where $1=any(pg_blocking_pids(pid)) and wait_event_type='Lock')", pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
	}
	cancel()
	tx.Rollback(context.Background())
	select {
	case err := <-done:
		done <- err
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled checker: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("accepted first fire did not finish")
	}
	var fired, active int
	if err := e.DB.QueryRow(t.Context(), "select count(*) filter(where status='fired'),count(*) filter(where status='active') from alerts where user_id=$1", e.User).Scan(&fired, &active); err != nil {
		t.Fatal(err)
	}
	if fired != 1 || active != 1 {
		t.Fatalf("shutdown admitted additional shielded work: fired=%d active=%d", fired, active)
	}
}

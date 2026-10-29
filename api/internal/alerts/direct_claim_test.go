package alerts

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
)

// The disable transaction wins ownership before the claim begins, but is
// deliberately uncommitted when the claim takes its first SQL snapshot. A
// one-statement EXISTS(enabled user) can see the old enabled version, wait
// on the alert row, and fire after the disable commits. A claim must serialize
// with the user update and recheck the committed owner before changing alerts.
func TestDirectAlertClaimHonorsDisableCommittedWhileWaiting(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	assertDirectFree(t, e)
	id, err := directStoreWrite(t, e, "insert", "", directStoreFixture(e.User))
	if err != nil {
		t.Fatal(err)
	}
	claimCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	alertTx, err := e.DB.Begin(claimCtx)
	if err != nil {
		t.Fatal(err)
	}
	defer alertTx.Rollback(ctx)
	var alertPID int
	if err := alertTx.QueryRow(claimCtx, "select pg_backend_pid() from alerts where id = $1 for update", id).Scan(&alertPID); err != nil {
		t.Fatal(err)
	}
	disableTx, err := e.DB.Begin(claimCtx)
	if err != nil {
		t.Fatal(err)
	}
	defer disableTx.Rollback(ctx)
	if _, err := disableTx.Exec(claimCtx, "update users set disabled_at = now() where id = $1", e.User); err != nil {
		t.Fatal(err)
	}
	var disablePID int
	if err := disableTx.QueryRow(claimCtx, "select pg_backend_pid()").Scan(&disablePID); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	var fireErr error
	go func() {
		fireErr = fire(claimCtx, e.Deps, id, 0.5)
		close(done)
	}()
	defer func() {
		cancel()
		disableTx.Rollback(ctx)
		alertTx.Rollback(ctx)
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("alert claim did not stop after cleanup released its locks")
		}
	}()

	// Observe a real PostgreSQL lock wait, rather than relying on sleeps or
	// text matching the claim SQL. Either the alert lock (old implementation)
	// or the user lock (serialized implementation) must block this claim.
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	waitCtx, stopWait := context.WithTimeout(claimCtx, 5*time.Second)
	defer stopWait()
	for {
		var waitingPID int
		err := e.DB.QueryRow(waitCtx, `select pid from pg_stat_activity
			where wait_event_type = 'Lock'
			and ($1::int = any(pg_blocking_pids(pid)) or $2::int = any(pg_blocking_pids(pid)))
			limit 1`, alertPID, disablePID).Scan(&waitingPID)
		if err == nil {
			break
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("observe alert-claim lock wait: %v", err)
		}
		select {
		case <-done:
			t.Fatalf("claim completed before its controlled lock wait: %v", fireErr)
		case <-waitCtx.Done():
			t.Fatal("claim never reached a PostgreSQL lock wait")
		case <-tick.C:
		}
	}
	if err := disableTx.Commit(claimCtx); err != nil {
		t.Fatal(err)
	}
	if err := alertTx.Rollback(claimCtx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		if fireErr != nil {
			t.Fatal(fireErr)
		}
	case <-claimCtx.Done():
		t.Fatal("claim did not finish after the winning disable committed")
	}
	var disabled bool
	if err := e.DB.QueryRow(ctx, "select disabled_at is not null from users where id = $1", e.User).Scan(&disabled); err != nil || !disabled {
		t.Fatalf("the controlled disable did not commit: disabled=%v err=%v", disabled, err)
	}
	if a := load(t, e, id); a.Status != "active" || a.FiredAt != nil || a.FiredPrice != nil {
		t.Fatalf("the committed disable must win before the alert claim: %+v", a)
	}
}

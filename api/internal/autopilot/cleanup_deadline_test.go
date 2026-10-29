package autopilot

import (
	"context"
	"errors"
	"net/http/httptrace"
	"sync"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
)

// Sync has already reconciled the accepted paper book when the actual candle
// request spends the operation deadline. The deferred save must still persist
// that state, using its own bounded cleanup lifetime.
func TestStepPersistsSyncAfterOperationDeadline(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	p := deployed(t, e, spec(nil), 10_000, 20)
	p.Qty, p.Avg = 2, 20 // the paper book was reset since this last saved state
	if err := p.save(t.Context(), e.DB); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Paper.Portfolio(t.Context(), e.User); err != nil {
		t.Fatal(err) // warm quotes so the controlled first-byte gate is candles
	}
	entered, release, exited, returned := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan error, 1)
	open := sync.OnceFunc(func() { close(release) })
	operation, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	operation = httptrace.WithClientTrace(operation, &httptrace.ClientTrace{GotFirstResponseByte: func() {
		close(entered)
		defer close(exited)
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
	}})
	go func() { _, _, err := step(operation, e.Deps, p, time.Now()); returned <- err }()
	t.Cleanup(func() {
		cancel()
		open()
		select {
		case <-returned:
		case <-time.After(5 * time.Second):
			t.Error("actual autopilot step driver did not exit")
		}
	})
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("actual market request did not reach its first-byte barrier")
	}
	<-operation.Done()
	open()
	select {
	case err := <-returned:
		returned <- err
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("operation did not spend its deadline: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("step did not complete accepted cleanup")
	}
	<-exited
	var qty float64
	var log []Entry
	if err := e.DB.QueryRow(t.Context(), "select position_qty, log from autopilots where id=$1", p.ID).Scan(&qty, &log); err != nil {
		t.Fatal(err)
	}
	if qty != 0 || len(log) != 1 || log[0].Kind != "sync" || log[0].Text != "Position closed outside the autopilot (account reset)." {
		t.Fatalf("accepted sync was lost after operation deadline: qty=%v log=%+v", qty, log)
	}
}

package autopilot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
)

func TestHoldCancelledWaiterDoesNotWaitForOwner(t *testing.T) {
	d := &app.Deps{DB: storetest.Schema(t)}
	release, ok, err := hold(t.Context(), d, false)
	if err != nil || !ok {
		t.Fatalf("actual lock owner: %v %v", ok, err)
	}
	released := false
	defer func() {
		if !released {
			release()
			released = true
		}
	}()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	done := make(chan error, 1)
	go func() {
		release, _, err := hold(ctx, d, false)
		if release != nil {
			release()
		}
		done <- err
	}()
	t.Cleanup(func() {
		if !released {
			release()
			released = true
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("actual autopilot lock waiter did not exit")
		}
	})
	select {
	case err := <-done:
		done <- err
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled lock waiter: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled autopilot waited for the existing holder")
	}
}

package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func TestTasksDetachedValuesAdmissionAndActualCompletion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		type key struct{}
		request, cancelClient := context.WithCancel(context.WithValue(context.Background(), key{}, "owned-user"))
		owner := NewTasks(context.Background(), nil)
		ctx, complete, ok := owner.Begin("accepted", request)
		if !ok {
			t.Fatal("initial admission rejected")
		}
		cancelClient()
		if ctx.Err() != nil || ctx.Value(key{}) != "owned-user" {
			t.Fatal("normal client departure canceled accepted work or dropped values")
		}
		op, cancelOp := context.WithTimeout(ctx, time.Second)
		defer cancelOp()
		shared := Detached(op)
		if _, ok := shared.Deadline(); ok {
			t.Fatal("shared work retained operation deadline")
		}
		acknowledged, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
		go func() { defer close(returned); defer complete(); <-ctx.Done(); close(acknowledged); <-release }()
		owner.Stop()
		<-acknowledged
		if shared.Err() != context.Canceled {
			t.Fatal("detached shared work stripped process cancellation")
		}
		if _, done, ok := owner.Begin("late", request); ok {
			done()
			t.Fatal("accepted after Stop")
		}
		called := false
		handler := owner.Handler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
		if w.Code != 503 || called {
			t.Fatal("HTTP admission remained open after stop")
		}
		deadline, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := owner.Wait(deadline); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("held completion not qualified: %v", err)
		}
		select {
		case <-returned:
			t.Fatal("return receipt published while task held")
		default:
		}
		close(release)
		<-returned
		if err := owner.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
		complete() // deferred/panic paths may share an idempotent receipt
	})
}

func TestTasksConcurrentAdmissionStop(t *testing.T) {
	owner := NewTasks(context.Background(), nil)
	var producers sync.WaitGroup
	for range 64 {
		producers.Go(func() {
			if _, done, ok := owner.Begin("racing", context.Background()); ok {
				defer done()
			}
		})
	}
	owner.Stop()
	producers.Wait()
	if err := owner.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, done, ok := owner.Begin("late", context.Background()); ok {
		done()
		t.Fatal("admitted after group drain")
	}
}

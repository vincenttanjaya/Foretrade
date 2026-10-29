package strategy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
)

// The genuine worker is held inside the actual market client's synchronous
// connection trace. HTTP must retain its normal five-second response budget;
// process completion must retain the real workers/coordinator beyond it.
func TestForwardHTTPReturnKeepsActualWorkersOwned(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	id := saved(t, e, e.User, "Owned forward", 4, 1, 100)
	if _, err := e.DB.Exec(t.Context(), "update strategies set created_at=$2 where id=$1", id, markettest.End.Add(-72*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var events []string
	e.Tasks = app.NewTasks(t.Context(), func(e string) { mu.Lock(); events = append(events, e); mu.Unlock() })
	entered, release, sourceExited, httpReturned := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	open := sync.OnceFunc(func() { close(release) })
	requestCtx := httptrace.WithClientTrace(e.Tasks.Context(), &httptrace.ClientTrace{GotConn: func(httptrace.GotConnInfo) {
		close(entered)
		defer close(sourceExited)
		select {
		case <-release:
		case <-time.After(15 * time.Second):
		}
	}})
	r := httptest.NewRequestWithContext(requestCtx, "GET", "/api/strategies?scope=mine", nil)
	r.AddCookie(e.Cookie)
	w := httptest.NewRecorder()
	go func() { defer close(httpReturned); e.Tasks.Handler(e.Mux).ServeHTTP(w, r) }()
	t.Cleanup(func() {
		open()
		e.Tasks.Stop()
		for name, done := range map[string]<-chan struct{}{"actual source client": sourceExited, "HTTP list": httpReturned} {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Errorf("%s driver did not exit", name)
			}
		}
		join, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := e.Tasks.Wait(join); err != nil {
			t.Errorf("process owner did not join actual forward children: %v", err)
		}
	})
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("actual forward worker did not enter market client")
	}
	select {
	case <-httpReturned:
	case <-time.After(7 * time.Second):
		t.Fatal("forward HTTP response exceeded its existing budget")
	}
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"reason":"timeout"`) {
		t.Fatalf("actual HTTP forward budget response: %d %s", w.Code, w.Body)
	}
	select {
	case <-sourceExited:
		t.Fatal("source barrier was released before the HTTP response")
	default:
	}
	e.Tasks.Stop()
	bound, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	if err := e.Tasks.Wait(bound); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("HTTP return discarded genuine forward worker/coordinator process ownership: %v", err)
	}
	open()
	join, stopJoin := context.WithTimeout(t.Context(), 5*time.Second)
	defer stopJoin()
	if err := e.Tasks.Wait(join); err != nil {
		t.Fatal(err)
	}
	<-sourceExited
	mu.Lock()
	defer mu.Unlock()
	positions := map[string]int{}
	for i, e := range events {
		positions[e] = i + 1
	}
	if positions["task_done:http"] == 0 || positions["task_done:strategy_forward"] <= positions["task_done:http"] {
		t.Fatalf("actual forward completion was not retained beyond HTTP return: %v", events)
	}
	t.Logf("actual HTTP/forward worker/coordinator completion: %v", events)
}

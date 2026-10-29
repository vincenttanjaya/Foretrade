package chat

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/httpx"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
)

type panicNativeClient struct {
	ping    <-chan struct{}
	entered chan struct{}
	exited  chan struct{}
}

func (c *panicNativeClient) Do(r *http.Request) (*http.Response, error) {
	defer close(c.exited)
	resp, err := (&http.Client{Timeout: 25 * time.Second}).Do(r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	select {
	case <-c.ping:
	case <-time.After(20 * time.Second):
		return nil, errors.New("actual keepalive never entered")
	}
	close(c.entered)
	panic("controlled native model transport panic")
}

type heldPingWriter struct {
	*httptest.ResponseRecorder
	entered chan struct{}
	release <-chan struct{}
	exited  chan struct{}
}

func (w *heldPingWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }

func (w *heldPingWriter) Write(b []byte) (int, error) {
	if strings.HasPrefix(string(b), ": ping") {
		close(w.entered)
		defer close(w.exited)
		select {
		case <-w.release:
		case <-time.After(30 * time.Second):
			return 0, errors.New("test ping barrier exceeded its bound")
		}
	}
	return w.ResponseRecorder.Write(b)
}

func TestChatPanicJoinsKeepaliveBeforeProcessDrain(t *testing.T) {
	upstreamEntered, upstreamRelease, upstreamDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	openUpstream := sync.OnceFunc(func() { close(upstreamRelease) })
	e := apptest.New(t, Register, apptest.Options{Reply: func(llmtest.Request) llmtest.Reply {
		return llmtest.Reply{Text: "Native response.", AfterFirst: func() {
			close(upstreamEntered)
			defer close(upstreamDone)
			select {
			case <-upstreamRelease:
			case <-time.After(35 * time.Second):
			}
		}}
	}})
	pingEntered, pingRelease, pingExited, panicEntered, modelExited, handlerDone := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	openPing := sync.OnceFunc(func() { close(pingRelease) })
	var mu sync.Mutex
	var events []string
	e.Tasks = app.NewTasks(t.Context(), func(e string) { mu.Lock(); events = append(events, e); mu.Unlock() })
	cfg := e.Bedrock.Config()
	cfg.HTTPClient = &panicNativeClient{ping: pingEntered, entered: panicEntered, exited: modelExited}
	e.LLM = llm.New(cfg, llmtest.Model)
	w := &heldPingWriter{ResponseRecorder: httptest.NewRecorder(), entered: pingEntered, release: pingRelease, exited: pingExited}
	r := httptest.NewRequestWithContext(e.Tasks.Context(), "POST", "/api/chat/stream", strings.NewReader(`{"message":"Compare Bitcoin liquidity risk"}`))
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(e.Cookie)
	go func() { defer close(handlerDone); e.Tasks.Handler(httpx.Recover(e.Mux)).ServeHTTP(w, r) }()
	t.Cleanup(func() {
		openPing()
		openUpstream()
		e.Tasks.Stop()
		for name, done := range map[string]<-chan struct{}{"handler": handlerDone, "native model client": modelExited, "keepalive write": pingExited, "native fake callback": upstreamDone} {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Errorf("%s driver did not exit", name)
			}
		}
	})
	select {
	case <-upstreamEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("real native fake request did not enter")
	}
	select {
	case <-panicEntered:
	case <-time.After(20 * time.Second):
		t.Fatal("accepted chat did not panic with its actual keepalive active")
	}
	e.Tasks.Stop()
	bound, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	if err := e.Tasks.Wait(bound); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("process drained before the panicked handler joined actual keepalive: %v", err)
	}
	select {
	case <-handlerDone:
		t.Fatal("HTTP handler completed while its keepalive write remained held")
	default:
	}
	openPing()
	join, stopJoin := context.WithTimeout(t.Context(), 5*time.Second)
	defer stopJoin()
	if err := e.Tasks.Wait(join); err != nil {
		t.Fatal(err)
	}
	<-handlerDone
	<-pingExited
	<-modelExited
	openUpstream()
	<-upstreamDone
	mu.Lock()
	defer mu.Unlock()
	t.Logf("actual panic/keepalive owner completion: %v", events)
}

package llm_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm"
)

// fake is a scripted HTTP provider: each request gets the next answer. The
// SSE fixtures in the wire tests follow each provider's documented stream.
type fake struct {
	t   *testing.T
	srv *httptest.Server

	mu      sync.Mutex
	answers []answer
	reqs    []got
}

type got struct {
	Method, Path string
	Header       http.Header
	Body         map[string]any
	Raw          string
}

// answer is a 200 SSE stream of events, or an error status with body (a 200
// JSON body when events is nil and status is 0).
type answer struct {
	status     int
	retryAfter string
	body       string
	events     []string
	pause      chan struct{} // stream: flush the first event, then wait for it to close
	drop       bool          // close the connection without a response
}

func newFake(t *testing.T, answers ...answer) *fake {
	f := &fake{t: t, answers: answers}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) push(a ...answer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answers = append(f.answers, a...)
}

func (f *fake) requests() []got {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reqs)
}

func (f *fake) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	g := got{Method: r.Method, Path: r.URL.RequestURI(), Header: r.Header.Clone(), Raw: string(raw)}
	json.Unmarshal(raw, &g.Body)
	f.mu.Lock()
	f.reqs = append(f.reqs, g)
	if len(f.answers) == 0 {
		f.mu.Unlock()
		f.t.Errorf("fake: unexpected request %s %s", r.Method, g.Path)
		w.WriteHeader(http.StatusTeapot)
		return
	}
	a := f.answers[0]
	f.answers = f.answers[1:]
	f.mu.Unlock()
	if a.retryAfter != "" {
		w.Header().Set("Retry-After", a.retryAfter)
	}
	switch {
	case a.drop:
		conn, _, _ := http.NewResponseController(w).Hijack()
		conn.Close()
	case a.status != 0:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(a.status)
		io.WriteString(w, a.body)
	case a.events == nil:
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, a.body)
	default:
		w.Header().Set("Content-Type", "text/event-stream")
		for i, e := range a.events {
			io.WriteString(w, e)
			if i == 0 && a.pause != nil {
				http.NewResponseController(w).Flush()
				<-a.pause
			}
		}
	}
}

// client is a client for s pointed at the fake.
func (f *fake) client(s llm.Settings) *llm.Client {
	f.t.Helper()
	s.BaseURL = f.srv.URL
	c, err := llm.NewFromSettings(s)
	if err != nil {
		f.t.Fatal(err)
	}
	return c
}

func sse(data string) string              { return "data: " + data + "\n\n" }
func named(event, data string) string     { return "event: " + event + "\ndata: " + data + "\n\n" }
func stream(events ...string) answer      { return answer{events: events} }
func failure(status int, b string) answer { return answer{status: status, body: b} }

// recorder makes tools that return fixed output and record their inputs.
type recorder struct {
	mu     sync.Mutex
	inputs []string
}

func (r *recorder) tool(name, out string) llm.Tool {
	return llm.Tool{Name: name, Description: "Looks up " + name + ".", Schema: map[string]any{"type": "object"},
		Run: func(_ context.Context, in json.RawMessage) (string, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.inputs = append(r.inputs, name+":"+string(in))
			return out, nil
		}}
}

func failing(name string) llm.Tool {
	return llm.Tool{Name: name, Schema: map[string]any{"type": "object"},
		Run: func(context.Context, json.RawMessage) (string, error) { return "", errors.New("down") }}
}

// path digs into a decoded JSON body: string keys and int indexes.
func path(v any, keys ...any) any {
	for _, k := range keys {
		switch k := k.(type) {
		case string:
			m, _ := v.(map[string]any)
			v = m[k]
		case int:
			a, _ := v.([]any)
			if k < 0 {
				k += len(a)
			}
			if k < 0 || k >= len(a) {
				return nil
			}
			v = a[k]
		}
	}
	return v
}

// compact is v as compact JSON.
func compact(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

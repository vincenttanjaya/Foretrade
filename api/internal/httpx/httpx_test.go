package httpx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCORSAndCSRF(t *testing.T) {
	allowed := []string{"http://localhost:5173"}
	h := CORS(allowed, CSRF(allowed, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})))
	do := func(method, origin, ctype string, preflight bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/orders", nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if ctype != "" {
			r.Header.Set("Content-Type", ctype)
		}
		if preflight {
			r.Header.Set("Access-Control-Request-Method", "POST")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	w := do("OPTIONS", "http://localhost:5173", "", true)
	if w.Code != 204 || w.Header().Get("Access-Control-Allow-Origin") != "http://localhost:5173" ||
		w.Header().Get("Access-Control-Allow-Credentials") != "true" || w.Header().Get("Access-Control-Allow-Headers") != "Content-Type" {
		t.Fatalf("preflight: %d %v", w.Code, w.Header())
	}
	if w := do("OPTIONS", "https://evil.example", "", true); w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("disallowed origin must not be echoed")
	}
	for _, tc := range []struct {
		method, origin, ctype string
		want                  int
	}{
		{"GET", "", "", 418},
		{"POST", "http://localhost:5173", "application/json; charset=utf-8", 418},
		{"POST", "", "application/json", 418}, // same-origin tools (curl) send no Origin
		{"POST", "http://localhost:5173", "text/plain", 403},
		{"POST", "http://localhost:5173", "application/x-www-form-urlencoded", 403},
		{"DELETE", "https://evil.example", "application/json", 403},
		{"POST", "", "", 403},
	} {
		if w := do(tc.method, tc.origin, tc.ctype, false); w.Code != tc.want {
			t.Errorf("%s origin=%q type=%q: %d, want %d", tc.method, tc.origin, tc.ctype, w.Code, tc.want)
		}
	}
}

func TestServerErrorCanceled(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/portfolio", nil)
	w := httptest.NewRecorder()
	ServerError(w, r, fmt.Errorf("query: %w", context.Canceled))
	if w.Code != StatusClientClosed || w.Body.Len() != 0 {
		t.Fatalf("canceled: %d %q", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	ServerError(w, r, errors.New("boom"))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("real error: %d", w.Code)
	}
}

func TestDecode(t *testing.T) {
	type in struct {
		N int `json:"n"`
	}
	for body, want := range map[string]string{
		`{"n": 3}`:   "",
		``:           "request body must be a JSON object",
		`{"n": 3`:    "request body is not valid JSON",
		`{n: 3}`:     "request body is not valid JSON",
		`{"n": "3"}`: `field "n" has the wrong type`,
		`[1]`:        "request body is not valid JSON",
		`{"n": 1, "pad": "` + strings.Repeat("x", 64<<10) + `"}`: "request body is too large",
	} {
		var v in
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		err := Decode(httptest.NewRecorder(), r, &v)
		if got := fmt.Sprint(err); (want == "" && err != nil) || (want != "" && got != want) {
			t.Errorf("Decode(%.40q) = %v, want %q", body, err, want)
		}
		if want == "" && v.N != 3 {
			t.Errorf("decoded %+v", v)
		}
	}
}

func TestUserContext(t *testing.T) {
	if UserID(context.Background()) != "" {
		t.Fatal("no user outside Require")
	}
	if got := UserID(WithUser(context.Background(), "u-1")); got != "u-1" {
		t.Fatalf("UserID = %q", got)
	}
}

// noFlush hides the recorder's Flush, like a writer that cannot stream.
type noFlush struct{ w http.ResponseWriter }

func (n noFlush) Header() http.Header         { return n.w.Header() }
func (n noFlush) Write(b []byte) (int, error) { return n.w.Write(b) }
func (n noFlush) WriteHeader(code int)        { n.w.WriteHeader(code) }

// failWriter accepts headers but fails every body write: the client is gone.
type failWriter struct{ *httptest.ResponseRecorder }

func (failWriter) Write([]byte) (int, error)       { return 0, errors.New("broken pipe") }
func (failWriter) WriteString(string) (int, error) { return 0, errors.New("broken pipe") }

func TestSSE(t *testing.T) {
	w := httptest.NewRecorder()
	s, err := NewSSE(w)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send("delta", map[string]string{"text": "hi"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Ping(); err != nil {
		t.Fatal(err)
	}
	h := w.Header()
	if h.Get("Content-Type") != "text/event-stream" || h.Get("Cache-Control") != "no-cache" || h.Get("X-Accel-Buffering") != "no" ||
		w.Body.String() != "event: delta\ndata: {\"text\":\"hi\"}\n\n: ping\n\n" || !w.Flushed {
		t.Fatalf("headers %v body %q", h, w.Body)
	}
	if err := s.Send("bad", func() {}); err == nil {
		t.Fatal("unencodable data must fail")
	}
	if _, err := NewSSE(noFlush{httptest.NewRecorder()}); err == nil {
		t.Fatal("a writer without Flush cannot stream")
	}
	gone, _ := NewSSE(failWriter{httptest.NewRecorder()})
	if gone.Send("delta", "x") == nil || gone.Ping() == nil {
		t.Fatal("writes to a gone client must fail")
	}
}

// Log records method, path and status (never the query string) and still
// lets SSE flush through its wrapper.
func TestLog(t *testing.T) {
	var buf strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	h := Log(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stream" {
			s, err := NewSSE(w)
			if err != nil {
				t.Errorf("SSE through Log: %v", err)
				return
			}
			s.Send("done", 1)
			return
		}
		w.WriteHeader(http.StatusTeapot)
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/tea?token=secret", nil))
	if w.Code != http.StatusTeapot || !strings.Contains(buf.String(), "status=418") || !strings.Contains(buf.String(), "path=/tea ") ||
		strings.Contains(buf.String(), "secret") {
		t.Fatalf("code %d, log %q", w.Code, buf.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/stream", nil))
	if !w.Flushed || !strings.Contains(w.Body.String(), "event: done") {
		t.Fatalf("stream through Log: %q", w.Body)
	}
}

// A healthy /healthz, the container healthcheck's poll, is not logged; a
// failing one is.
func TestLogHealthz(t *testing.T) {
	var buf strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	status := http.StatusOK
	h := Log(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/healthz", nil))
	if buf.Len() != 0 {
		t.Fatalf("healthy probe logged: %q", buf.String())
	}
	status = http.StatusServiceUnavailable
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/healthz", nil))
	if !strings.Contains(buf.String(), "path=/healthz status=503") {
		t.Fatalf("failing probe: %q", buf.String())
	}
}

func TestRecover(t *testing.T) {
	h := Recover(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/abort" {
			panic(http.ErrAbortHandler)
		}
		panic("nil map")
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/boom", nil))
	if w.Code != 500 || !strings.Contains(w.Body.String(), `"code":"internal"`) || strings.Contains(w.Body.String(), "nil map") {
		t.Fatalf("panic: %d %s", w.Code, w.Body)
	}
	defer func() {
		if v := recover(); v != http.ErrAbortHandler {
			t.Fatalf("ErrAbortHandler must propagate to net/http, got %v", v)
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/abort", nil))
}

// The cap covers the complete body, including padding after a valid first value.
func TestDecodeCompleteBodyLimit(t *testing.T) {
	for _, limit := range []int64{65_536, 524_288, 4_194_304} {
		for _, extra := range []int{0, 1} {
			body := `{"n":1}` + strings.Repeat(" ", int(limit)-7+extra)
			var v struct{ N int }
			err := DecodeLimit(httptest.NewRecorder(), httptest.NewRequest("POST", "/", strings.NewReader(body)), &v, limit)
			if extra == 0 && (err != nil || v.N != 1) {
				t.Fatalf("exact cap %d: %v", limit, err)
			}
			if extra == 1 && (err == nil || err.Error() != "request body is too large") {
				t.Fatalf("over cap %d accepted: %v", limit, err)
			}
		}
	}
	var v struct{ N int }
	if err := Decode(httptest.NewRecorder(), httptest.NewRequest("POST", "/", strings.NewReader(`{"n":1} {"n":2}`)), &v); err == nil {
		t.Fatal("multiple JSON values accepted")
	}
}

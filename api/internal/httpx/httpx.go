// Package httpx holds the HTTP plumbing shared by every handler: JSON in/out,
// the contract's error shape, the request user, SSE, and the middleware chain.
package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"slices"
	"time"
)

const maxBody = 64 << 10

func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// Error writes {"error":{"code":…,"message":…}}.
func Error(w http.ResponseWriter, status int, code, msg string) {
	JSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
}

// StatusClientClosed is recorded for requests the client abandoned (nginx's 499).
const StatusClientClosed = 499

// ServerError logs err and writes a generic 500; internals never reach the client.
// A request the client cancelled is not a server fault: debug log and 499.
func ServerError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, context.Canceled) {
		slog.Debug("request canceled", "method", r.Method, "path", r.URL.Path, "err", err)
		w.WriteHeader(StatusClientClosed)
		return
	}
	slog.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	Error(w, http.StatusInternalServerError, "internal", "Something went wrong.")
}

// Decode reads a JSON body capped at 64 KiB. Its error text is safe to show the client.
func Decode(w http.ResponseWriter, r *http.Request, v any) error {
	return DecodeLimit(w, r, v, maxBody)
}

// DecodeLimit preserves Decode's safe errors with an endpoint-specific wire cap.
func DecodeLimit(w http.ResponseWriter, r *http.Request, v any, limit int64) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	err := dec.Decode(v)
	if err == nil {
		var extra any
		err = dec.Decode(&extra)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err == nil {
			return errors.New("request body must contain one JSON value")
		}
	}
	var tooBig *http.MaxBytesError
	var syntax *json.SyntaxError
	var typ *json.UnmarshalTypeError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, io.EOF):
		return errors.New("request body must be a JSON object")
	case errors.As(err, &tooBig):
		return errors.New("request body is too large")
	case errors.As(err, &syntax), errors.Is(err, io.ErrUnexpectedEOF):
		return errors.New("request body is not valid JSON")
	case errors.As(err, &typ) && typ.Field != "":
		return fmt.Errorf("field %q has the wrong type", typ.Field)
	default:
		return errors.New("request body is not valid JSON")
	}
}

type userKey struct{}

func WithUser(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, userKey{}, userID)
}

// UserID is the authenticated user's id, or "" outside auth.Service.Require.
func UserID(ctx context.Context) string {
	id, _ := ctx.Value(userKey{}).(string)
	return id
}

// SSE writes text/event-stream frames, flushing after each one.
type SSE struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

func NewSSE(w http.ResponseWriter) (*SSE, error) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	s := &SSE{w: w, rc: http.NewResponseController(w)}
	if err := s.rc.Flush(); err != nil {
		return nil, fmt.Errorf("sse: %w", err)
	}
	return s, nil
}

func (s *SSE) Send(event string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	// A slow or departed reader must not hold the stream mutex indefinitely.
	// The deadline is per frame; model think time between frames is unchanged.
	s.rc.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, b); err != nil {
		return err
	}
	return s.rc.Flush()
}

// Ping writes an SSE comment so proxies keep an idle stream open.
func (s *SSE) Ping() error {
	s.rc.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(s.w, ": ping\n\n"); err != nil {
		return err
	}
	return s.rc.Flush()
}

// Log records method, path, status and duration. Never bodies or query strings.
// A healthy /healthz is skipped: the image's HEALTHCHECK polls it every 10 s.
func Log(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if r.URL.Path == "/healthz" && rec.status == http.StatusOK {
			return
		}
		slog.Info("http", "method", r.Method, "path", r.URL.Path, "status", rec.status,
			"ms", time.Since(start).Milliseconds())
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach Flush on the real writer (SSE).
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// Recover turns a handler panic into a logged 500.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				ServerError(w, r, fmt.Errorf("panic: %v", v))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// CORS echoes an allowed Origin with credentials and answers preflights with 204.
func CORS(allowed []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		w.Header().Add("Vary", "Origin")
		if origin != "" && slices.Contains(allowed, origin) {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")
			if r.Method == http.MethodOptions {
				h.Set("Access-Control-Allow-Methods", "GET,POST,PUT,PATCH,DELETE,OPTIONS")
				h.Set("Access-Control-Allow-Headers", "Content-Type")
				h.Set("Access-Control-Max-Age", "600")
			}
		}
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// CSRF requires every state-changing request to be application/json (a type
// a cross-site form cannot send without a preflight) and, when an Origin is
// present, to come from an allowed origin.
func CSRF(allowed []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		origin := r.Header.Get("Origin")
		if mt != "application/json" || (origin != "" && !slices.Contains(allowed, origin)) {
			Error(w, http.StatusForbidden, "forbidden", "Cross-site request rejected.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

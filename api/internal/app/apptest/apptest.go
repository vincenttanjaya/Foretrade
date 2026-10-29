// Package apptest builds the full app.Deps for handler tests: a test schema
// of their own, fake Binance and Bedrock servers (and optionally Brave), an
// in-memory Yahoo feed, and a signed-in user. Tests skip when DATABASE_URL_TEST is unset.
package apptest

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/auth"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
	"github.com/vincenttanjaya/Foretrade/api/internal/research"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
	"github.com/vincenttanjaya/Foretrade/api/internal/usage"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo/yahootest"
)

type Options struct {
	Reply   func(llmtest.Request) llmtest.Reply // Bedrock's answers; nil → any model call fails the test
	Brave   http.HandlerFunc                    // a fake Brave Search API; nil → search disabled
	Credits int                                 // daily AI credits; 0 → 500
}

type Env struct {
	*app.Deps
	Mux     *http.ServeMux
	User    string       // the signed-in user's id
	Cookie  *http.Cookie // its session
	Binance *markettest.Binance
	Bedrock *llmtest.Server
	Yahoo   *yahootest.Fake // d.Yahoo: empty, so every symbol is not found until a test Sets it
	t       testing.TB
}

// New wires the deps, mounts register on a fresh mux and signs a new user in.
func New(t testing.TB, register func(*http.ServeMux, *app.Deps), o Options) *Env {
	t.Helper()
	pool := storetest.Schema(t)
	bin := markettest.New(t)
	reply := o.Reply
	if reply == nil {
		reply = llmtest.Queue(t)
	}
	ai, bedrock := llmtest.New(t, reply)
	rcfg := research.Config{}
	if o.Brave != nil {
		srv := httptest.NewServer(o.Brave)
		t.Cleanup(srv.Close)
		rcfg = research.Config{BraveAPIKey: "test", BraveBase: srv.URL}
	}
	mkt := market.NewClient(bin.URL)
	mkt.HyperliquidBase = bin.URL
	meter := usage.NewMeter(pool, cmp.Or(o.Credits, 500))
	d := &app.Deps{DB: pool, Market: mkt, LLM: ai, Usage: meter, Paper: paper.NewEngine(pool, mkt),
		Auth: auth.NewService(pool, meter, false), Research: research.New(rcfg, pool, ai)}
	yh := yahootest.New()
	d.Yahoo = yh
	mkt.Yahoo = yh
	e := &Env{Deps: d, Mux: http.NewServeMux(), Binance: bin, Bedrock: bedrock, Yahoo: yh, t: t}
	register(e.Mux, d)
	e.User = storetest.User(t, pool)
	e.Cookie = e.Session(e.User)
	return e
}

// Session signs userID in the way auth's login does (random token, SHA-256
// stored), without paying for bcrypt.
func (e *Env) Session(userID string) *http.Cookie {
	e.t.Helper()
	raw := make([]byte, 32)
	rand.Read(raw)
	sum := sha256.Sum256(raw)
	if _, err := e.DB.Exec(context.Background(),
		"insert into sessions (token_hash, user_id, expires_at) values ($1, $2, now() + interval '1 hour')", sum[:], userID); err != nil {
		e.t.Fatal(err)
	}
	return &http.Cookie{Name: "tsid", Value: base64.RawURLEncoding.EncodeToString(raw)}
}

// Do serves one request as the env's user. A string body is sent as is,
// anything else JSON-encoded; non-GET requests carry Content-Type JSON.
func (e *Env) Do(method, path string, body any) *httptest.ResponseRecorder {
	return e.DoAs(e.Cookie, method, path, body)
}

// DoAs is Do with another session; nil sends no cookie.
func (e *Env) DoAs(c *http.Cookie, method, path string, body any) *httptest.ResponseRecorder {
	return e.DoCtx(context.Background(), c, method, path, body)
}

// DoCtx is DoAs with a request context, e.g. one the test cancels.
func (e *Env) DoCtx(ctx context.Context, c *http.Cookie, method, path string, body any) *httptest.ResponseRecorder {
	var r io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		r = strings.NewReader(b)
	default:
		j, err := json.Marshal(b)
		if err != nil {
			e.t.Fatal(err)
		}
		r = bytes.NewReader(j)
	}
	req := httptest.NewRequestWithContext(ctx, method, path, r)
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
	}
	if c != nil {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	e.Mux.ServeHTTP(w, req)
	return w
}

// JSON decodes a response body, failing the test on bad JSON.
func JSON[T any](t testing.TB, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %d response %q: %v", w.Code, w.Body, err)
	}
	return v
}

// ErrorCode is the contract error code of a response ("" when there is none).
func ErrorCode(t testing.TB, w *httptest.ResponseRecorder) string {
	t.Helper()
	return JSON[struct {
		Error struct{ Code string } `json:"error"`
	}](t, w).Error.Code
}

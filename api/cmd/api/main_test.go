package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/vincenttanjaya/Foretrade/api/internal/aiconfig"
	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/auth"
	"github.com/vincenttanjaya/Foretrade/api/internal/config"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
	"github.com/vincenttanjaya/Foretrade/api/internal/research"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo/yahootest"
)

// quoteSourcesOff is X-Quote-Sources with no real-time source switched on.
var quoteSourcesOff = regexp.MustCompile(`^krx=yahoo (fallback|closed) -; hkex=yahoo (fallback|closed) -; hose=yahoo fallback -; ` +
	`sgx=yahoo (fallback|closed) -; twse=yahoo (fallback|closed) -$`)

const (
	origin      = "https://app.example.com"
	adminOrigin = "https://admin.example.com"
)

// The route table: every contract endpoint is mounted, and all but login and
// healthz sit behind the session check (401 without a cookie, before any
// database access). The middleware chain answers CORS and CSRF first.
func TestHandler(t *testing.T) {
	h := handler([]string{origin, adminOrigin}, &app.Deps{Auth: auth.NewService(nil, nil, false), Market: market.NewClient("http://127.0.0.1:1"),
		Paper: paper.NewEngine(nil, nil), Research: research.New(research.Config{}, nil, nil), AdminOrigins: []string{adminOrigin},
		Settings: aiconfig.New(nil, config.Config{}, nil, nil)})
	do := func(method, path, ctype, from string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader("{}"))
		if ctype != "" {
			r.Header.Set("Content-Type", ctype)
		}
		if from != "" {
			r.Header.Set("Origin", from)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	if w := do("GET", "/healthz", "", ""); w.Code != 200 || w.Body.String() != "ok" {
		t.Fatalf("healthz: %d %q", w.Code, w.Body)
	} else if h := w.Header().Get("X-Quote-Sources"); !quoteSourcesOff.MatchString(h) { // closed or not depends on the hour: both pass
		t.Fatalf("X-Quote-Sources %q", h)
	}
	id := "6f1c2c1e-0000-4000-8000-000000000000"
	for _, route := range []string{
		"GET /api/me", "POST /api/auth/logout",
		"GET /api/market/assets", "GET /api/market/candles", "GET /api/market/analysis", "GET /api/market/quote-sources",
		"GET /api/portfolio", "GET /api/portfolio/trades", "POST /api/orders", "POST /api/portfolio/close", "POST /api/portfolio/reset",
		"GET /api/chats", "GET /api/chats/" + id, "DELETE /api/chats/" + id, "POST /api/chat/stream",
		"POST /api/copilot/plan",
		"POST /api/forecasts/draft", "POST /api/forecasts", "GET /api/forecasts", "GET /api/forecasts/" + id, "DELETE /api/forecasts/" + id,
		"GET /api/predictions/markets", "GET /api/predictions/markets/900/draft",
		"POST /api/strategies/generate", "POST /api/strategies/backtest", "GET /api/strategies", "GET /api/strategies/" + id, "DELETE /api/strategies/" + id,
		"POST /api/ai-portfolios/build", "GET /api/ai-portfolios", "GET /api/ai-portfolios/" + id, "POST /api/ai-portfolios/" + id + "/invest",
		"GET /api/news", "GET /api/news/brief",
		"GET /api/arena/options", "POST /api/arena/runs", "GET /api/arena/runs", "GET /api/arena/runs/" + id,
		"GET /api/arena/runs/" + id + "/steps", "POST /api/arena/runs/" + id + "/cancel", "DELETE /api/arena/runs/" + id,
		"GET /api/admin/overview", "GET /api/admin/users", "POST /api/admin/users", "GET /api/admin/users/" + id,
		"PATCH /api/admin/users/" + id, "DELETE /api/admin/users/" + id, "POST /api/admin/users/" + id + "/password",
		"POST /api/admin/users/" + id + "/sessions/revoke", "POST /api/admin/users/" + id + "/reset-portfolio",
		"POST /api/admin/users/" + id + "/reset-credits", "GET /api/admin/audit", "GET /api/admin/anything-else",
		"GET /api/admin/settings", "PUT /api/admin/settings", "POST /api/admin/settings/test", "POST /api/admin/settings/models",
		"POST /api/admin/settings/chatgpt/start", "POST /api/admin/settings/chatgpt/signout",
	} {
		method, path, _ := strings.Cut(route, " ")
		from := origin
		if strings.HasPrefix(path, "/api/admin/") {
			from = adminOrigin
		}
		w := do(method, path, "application/json", from)
		if w.Code != 401 || !strings.Contains(w.Body.String(), `"code":"unauthorized"`) || w.Header().Get("Access-Control-Allow-Origin") != from {
			t.Errorf("%s: %d %s", route, w.Code, w.Body)
		}
	}
	// The web app's origin passes CORS and CSRF but not the admin API's own origin check.
	for _, method := range []string{"GET", "POST"} {
		if w := do(method, "/api/admin/users", "application/json", origin); w.Code != 403 || !strings.Contains(w.Body.String(), `"code":"forbidden"`) {
			t.Errorf("%s /api/admin/users from the web app: %d %s", method, w.Code, w.Body)
		}
	}
	if w := do("POST", "/api/auth/login", "application/json", origin); w.Code != 400 {
		t.Errorf("login is public: %d %s", w.Code, w.Body)
	}
	if w := do("GET", "/auth/callback?state=nope", "", ""); w.Code != 400 || !strings.Contains(w.Body.String(), "unknown or expired") {
		t.Errorf("the ChatGPT callback is public, its state the only key: %d %s", w.Code, w.Body)
	}
	if w := do("GET", "/api/nope", "", ""); w.Code != 404 {
		t.Errorf("unknown route: %d", w.Code)
	}
	if w := do("PUT", "/api/orders", "application/json", ""); w.Code != 405 {
		t.Errorf("wrong method: %d", w.Code)
	}
	if w := do("POST", "/api/orders", "text/plain", origin); w.Code != 403 {
		t.Errorf("form-like POST: %d", w.Code)
	}
	if w := do("POST", "/api/auth/login", "application/json", "https://evil.example"); w.Code != 403 || w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("cross-site login: %d", w.Code)
	}
	r := httptest.NewRequest("OPTIONS", "/api/orders", nil)
	r.Header.Set("Origin", origin)
	r.Header.Set("Access-Control-Request-Method", "POST")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent || w.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Errorf("preflight: %d %v", w.Code, w.Header())
	}
}

// start loads the config and binds PORT before booting; either failing stops it.
func TestStartErrors(t *testing.T) {
	isolateAWS(t, "")
	t.Setenv("MODEL_KNOWLEDGE_CUTOFF", "June")
	if err := start(context.Background()); err == nil || !strings.Contains(err.Error(), "MODEL_KNOWLEDGE_CUTOFF") {
		t.Fatalf("bad config: %v", err)
	}
	t.Setenv("MODEL_KNOWLEDGE_CUTOFF", "")
	t.Setenv("PORT", "-1")
	if err := start(context.Background()); err == nil || !strings.Contains(err.Error(), "listen") {
		t.Fatalf("bad port: %v", err)
	}
}

// A boot that fails returns its error and closes the listener it was given.
func TestRunBootFailure(t *testing.T) {
	isolateAWS(t, "")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), config.Config{}, ln); err == nil || !strings.Contains(err.Error(), "database url is empty") {
		t.Fatalf("run without a database: %v", err)
	}
	if _, err := ln.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("listener left open: %v", err)
	}
}

// run migrates a fresh schema on DATABASE_URL_TEST, serves on the listener it
// is given and returns nil once ctx ends. No AI provider is set (nor a
// SECRET_KEY): the server still boots, with AI off.
func TestRunServesUntilCancelled(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL_TEST")
	if dsn == "" {
		t.Skip("DATABASE_URL_TEST not set")
	}
	dsn, db := freshSchema(t, dsn)
	isolateAWS(t, "")
	logs := captureLogs(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{DatabaseURL: dsn, AllowedOrigins: []string{origin}, AIDailyCredits: 500, BinanceBase: markettest.New(t).URL}
	cfg.HyperliquidBase = cfg.BinanceBase
	cfg.YahooBase, cfg.ScreenerMaxTickers = yahootest.New().Serve(t), 1 // the workers price quote feeds and the screener boots: never the real Yahoo
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, cfg, ln) }()

	// The listener is bound already, so this waits for the boot, not a retry loop.
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Get("http://" + ln.Addr().String() + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var migrations int
	files, _ := filepath.Glob("../../internal/store/migrations/*.sql") // every embedded migration, however many land
	if err := db.QueryRow(context.Background(), "select count(*) from schema_migrations").Scan(&migrations); err != nil || resp.StatusCode != 200 ||
		string(body) != "ok" || len(files) == 0 || migrations != len(files) {
		t.Fatalf("healthz %d %q, %d of %d migrations (%v)", resp.StatusCode, body, migrations, len(files), err)
	}
	if out := logs.String(); !strings.Contains(out, `msg=settings ai="" model=""`) || !strings.Contains(out, "SECRET_KEY is not 64 hex characters") {
		t.Fatalf("boot log: %s", out)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("run did not return after ctx ended")
	}
}

// Boot ensures the demo user and the bootstrap admin, returns their emails as
// managed, and never undoes what the console changed on a restart.
func TestEnsureAccounts(t *testing.T) {
	cost := auth.BcryptCost
	auth.BcryptCost = bcrypt.MinCost
	t.Cleanup(func() { auth.BcryptCost = cost })
	pool := storetest.Schema(t)
	ctx := context.Background()
	a := auth.NewService(pool, nil, false)
	cfg := config.Config{DemoEmail: " Demo@Example.test", DemoPassword: "demo-pass", AdminEmail: "admin@example.test", AdminPassword: "admin-pass-1234"}
	state := func(email string) (role, name string, disabled bool) {
		t.Helper()
		if err := pool.QueryRow(ctx, "select role, name, disabled_at is not null from users where email = $1", email).Scan(&role, &name, &disabled); err != nil {
			t.Fatal(err)
		}
		return
	}
	managed, err := ensureAccounts(ctx, a, cfg)
	if err != nil || !slices.Equal(managed, []string{"demo@example.test", "admin@example.test"}) {
		t.Fatalf("first boot: %v %v", managed, err)
	}
	if role, name, _ := state("demo@example.test"); role != "user" || name != "Demo Investor" {
		t.Fatalf("demo: %s %s", role, name)
	}
	if role, name, _ := state("admin@example.test"); role != "admin" || name != "Admin" {
		t.Fatalf("admin: %s %s", role, name)
	}
	if _, err := pool.Exec(ctx, `update users set disabled_at = now() where email = 'demo@example.test';
		update users set role = 'user' where email = 'admin@example.test'`); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureAccounts(ctx, a, cfg); err != nil {
		t.Fatal(err)
	}
	if role, _, disabled := state("demo@example.test"); role != "user" || !disabled {
		t.Fatal("a restart must not re-enable the demo account")
	}
	if role, _, _ := state("admin@example.test"); role != "user" {
		t.Fatal("a restart must not re-promote a demoted bootstrap admin")
	}
	if _, err := pool.Exec(ctx, "delete from users where email = 'admin@example.test'"); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureAccounts(ctx, a, cfg); err != nil {
		t.Fatal(err)
	}
	if role, _, disabled := state("admin@example.test"); role != "admin" || disabled {
		t.Fatal("a deleted bootstrap admin is recreated with defaults")
	}
	if managed, err := ensureAccounts(ctx, a, config.Config{DemoEmail: "x@example.test", AdminPassword: "no-email-set"}); err != nil || managed != nil {
		t.Fatalf("half-configured accounts are skipped: %v %v", managed, err)
	}
	pool.Close()
	if _, err := ensureAccounts(ctx, a, cfg); err == nil {
		t.Fatal("db down must fail boot")
	}
}

func TestVersion(t *testing.T) {
	t.Cleanup(func() { commit = "" })
	commit = "0123456789abcdef0123"
	if v := version(); v != "0123456789ab" {
		t.Fatalf("ldflags commit: %q", v)
	}
	commit = ""
	if v := version(); v != "dev" && len(v) != 12 {
		t.Fatalf("fallback: %q", v)
	}
	// Only a commit id reaches /healthz.
	for in, want := range map[string]string{"nogit": "dev", "0123456789AB": "dev", "abc12": "dev", "abc1234": "abc1234", "0123456789abcdef0123456789abcdef01234567": "0123456789ab",
		"0123456789abcdef0123456789abcdef012345678": "dev", "deadbeef-dirty": "dev"} {
		if commit = in; version() != want {
			t.Errorf("commit %q: %q, want %q", in, version(), want)
		}
	}
}

// GET /healthz says which commit is serving: the header is
// the boot version, the body stays "ok".
func TestHealthzCommit(t *testing.T) {
	h := handler([]string{origin}, &app.Deps{Auth: auth.NewService(nil, nil, false), Market: market.NewClient("http://127.0.0.1:1")})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != 200 || w.Body.String() != "ok" || w.Header().Get("X-Api-Commit") != apiVersion() || apiVersion() == "" {
		t.Fatalf("healthz: %d %q %q", w.Code, w.Body, w.Header().Get("X-Api-Commit"))
	}
}

// The boot log names each venue whose calendar ends within 30 days, and stays
// silent while none does.
func TestWarnCalendarHorizons(t *testing.T) {
	var buf strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	warnCalendarHorizons(time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC))
	if buf.Len() != 0 {
		t.Fatalf("warned today: %s", buf.String())
	}
	warnCalendarHorizons(time.Date(2026, 12, 15, 0, 0, 0, 0, time.UTC))
	if out := buf.String(); !strings.Contains(out, "level=WARN") || !strings.Contains(out, `lastVerified="SET 2026-12-31, SSE 2026-12-31, TWSE 2026-12-31"`) {
		t.Fatalf("boot warning: %s", out)
	}
}

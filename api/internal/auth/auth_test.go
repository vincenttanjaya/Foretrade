package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/vincenttanjaya/Foretrade/api/internal/httpx"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
	"github.com/vincenttanjaya/Foretrade/api/internal/usage"
)

func TestMain(m *testing.M) {
	if BcryptCost != 12 {
		panic("production bcrypt cost must stay 12")
	}
	// ponytail: cost 12 takes seconds per hash under -race; the hash logic is the same at MinCost.
	BcryptCost = bcrypt.MinCost
	os.Exit(m.Run())
}

func TestLimiter(t *testing.T) {
	l := &limiter{max: 2, window: time.Minute, hits: map[string]*window{}}
	now := time.Now()
	if !l.allow("a", now) || !l.allow("a", now) || l.allow("a", now) {
		t.Fatal("want 2 allowed then blocked")
	}
	if !l.allow("b", now) {
		t.Fatal("keys must be independent")
	}
	if !l.allow("a", now.Add(time.Minute)) {
		t.Fatal("window must reset")
	}
}

// clientIP reads X-Forwarded-For only from a trusted proxy, and then its
// rightmost hop that is not a proxy: the hops left of it are the client's own.
func TestClientIP(t *testing.T) {
	bridge := []netip.Prefix{netip.MustParsePrefix("172.16.0.0/12")}
	for _, tc := range []struct {
		remote, xff string
		trusted     []netip.Prefix
		want        string
	}{
		{"127.0.0.1:5000", "203.0.113.9, 10.0.0.1", nil, "10.0.0.1"},
		{"127.0.0.1:5000", "6.6.6.6, 203.0.113.9", nil, "203.0.113.9"}, // a client-chosen first hop is ignored
		{"[::1]:5000", "203.0.113.9", nil, "203.0.113.9"},
		{"127.0.0.1:5000", "", nil, "127.0.0.1"},
		{"127.0.0.1:5000", "garbage", nil, "127.0.0.1"},
		{"198.51.100.7:5000", "203.0.113.9", nil, "198.51.100.7"}, // spoofed header from a non-proxy peer
		{"172.18.0.1:5000", "203.0.113.9", nil, "172.18.0.1"},     // a host proxy via the Docker bridge, untrusted
		{"172.18.0.1:5000", "203.0.113.9", bridge, "203.0.113.9"}, // ... and trusted
		{"172.18.0.1:5000", "6.6.6.6, 203.0.113.9, 172.18.0.5", bridge, "203.0.113.9"},
		{"172.18.0.1:5000", "172.18.0.5", bridge, "172.18.0.1"}, // every hop a proxy
		{"198.51.100.7:5000", "203.0.113.9", bridge, "198.51.100.7"},
	} {
		r := httptest.NewRequest("POST", "/", nil)
		r.RemoteAddr = tc.remote
		if tc.xff != "" {
			r.Header.Set("X-Forwarded-For", tc.xff)
		}
		if got := clientIP(r, tc.trusted); got != tc.want {
			t.Errorf("clientIP(%s, %q, %v) = %s, want %s", tc.remote, tc.xff, tc.trusted, got, tc.want)
		}
	}
	// A second header line is the proxy's: it counts as the rightmost hops.
	r := httptest.NewRequest("POST", "/", nil)
	r.RemoteAddr = "127.0.0.1:5000"
	r.Header.Add("X-Forwarded-For", "6.6.6.6")
	r.Header.Add("X-Forwarded-For", "203.0.113.9")
	if got := clientIP(r, nil); got != "203.0.113.9" {
		t.Errorf("two header lines: %s", got)
	}
}

func TestLoginFlow(t *testing.T) {
	pool := storetest.Pool(t)
	ctx := context.Background()
	s := NewService(pool, usage.NewMeter(pool, 500), false)
	email := strings.ToLower("Flow-" + time.Now().Format("150405.000000") + "@Example.test")
	u, err := s.EnsureUser(ctx, "  "+strings.ToUpper(email), "Flow", "pw-123456", "user")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Exec(ctx, "delete from users where id = $1", u.ID) })
	mux := http.NewServeMux()
	s.Register(mux)

	do := func(method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	if w := do("POST", "/api/auth/login", `{"email":"`+email+`","password":"wrong"}`, nil); w.Code != 401 ||
		!strings.Contains(w.Body.String(), "invalid_credentials") {
		t.Fatalf("bad password: %d %s", w.Code, w.Body)
	}
	if w := do("POST", "/api/auth/login", `{"email":"nobody@example.test","password":"x"}`, nil); w.Code != 401 {
		t.Fatalf("unknown email: %d", w.Code)
	}
	w := do("POST", "/api/auth/login", `{"email":"`+email+`","password":"pw-123456"}`, nil)
	if w.Code != 200 {
		t.Fatalf("login: %d %s", w.Code, w.Body)
	}
	cookie := w.Result().Cookies()[0]
	if cookie.Name != "tsid" || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie = %+v", cookie)
	}
	if w := do("GET", "/api/me", "", cookie); w.Code != 200 || !strings.Contains(w.Body.String(), `"limit":500`) ||
		!strings.Contains(w.Body.String(), `"role":"user"`) {
		t.Fatalf("me: %d %s", w.Code, w.Body)
	}
	if w := do("POST", "/api/auth/logout", "{}", cookie); w.Code != 204 {
		t.Fatalf("logout: %d", w.Code)
	}
	if w := do("GET", "/api/me", "", cookie); w.Code != 401 {
		t.Fatalf("me after logout: %d", w.Code)
	}

	// Re-ensuring the same password (a redeploy) keeps sessions; a new one revokes them.
	cookie = do("POST", "/api/auth/login", `{"email":"`+email+`","password":"pw-123456"}`, nil).Result().Cookies()[0]
	if _, err := s.EnsureUser(ctx, email, "Flow", "pw-123456", "user"); err != nil {
		t.Fatal(err)
	}
	if w := do("GET", "/api/me", "", cookie); w.Code != 200 {
		t.Fatalf("me after same-password ensure: %d", w.Code)
	}
	if _, err := s.EnsureUser(ctx, email, "Flow", "pw-rotated", "user"); err != nil {
		t.Fatal(err)
	}
	if w := do("GET", "/api/me", "", cookie); w.Code != 401 {
		t.Fatalf("me after password rotation: %d", w.Code)
	}
	if w := do("POST", "/api/auth/login", `{"email":"`+email+`","password":"pw-123456"}`, nil); w.Code != 401 {
		t.Fatalf("old password after rotation: %d", w.Code)
	}
}

func TestLimiterPrunesExpiredWindows(t *testing.T) {
	l := &limiter{max: 10, window: time.Minute, hits: map[string]*window{}}
	old := time.Now().Add(-time.Hour)
	for i := range 10_001 {
		l.hits[fmt.Sprint(i)] = &window{start: old, n: 3}
	}
	l.allow("fresh", time.Now())
	if len(l.hits) != 1 {
		t.Fatalf("expired windows must be dropped past 10k keys: %d left", len(l.hits))
	}
}

func TestClientIPWithoutPort(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	r.RemoteAddr = "192.0.2.1"
	if got := clientIP(r, nil); got != "192.0.2.1" {
		t.Fatalf("clientIP = %q", got)
	}
}

func TestCookie(t *testing.T) {
	s := NewService(nil, nil, true)
	c := s.cookie("tok", sessionTTL)
	if c.Name != "tsid" || c.Value != "tok" || !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode ||
		c.MaxAge != 7*24*3600 || c.Path != "/" || c.Domain != "" {
		t.Fatalf("session cookie = %+v", c)
	}
	if c := s.cookie("", -1); c.MaxAge != -1 || c.Value != "" {
		t.Fatalf("clearing cookie = %+v", c)
	}
	if NewService(nil, nil, false).cookie("t", time.Hour).Secure {
		t.Fatal("COOKIE_SECURE=false must not set Secure")
	}
}

// Login is limited to 10 attempts per client per window, counted before the
// body is read; behind the local proxy each forwarded client has its own budget.
func TestLoginRateLimit(t *testing.T) {
	s := NewService(nil, nil, false) // every attempt below fails before touching the DB
	mux := http.NewServeMux()
	s.Register(mux)
	attempt := func(remote, xff string) int {
		r := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(`not json`))
		r.RemoteAddr, r.Header["X-Forwarded-For"] = remote, []string{xff}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code
	}
	for i := range 10 {
		if code := attempt("127.0.0.1:4000", "203.0.113.5"); code != 400 {
			t.Fatalf("attempt %d: %d", i+1, code)
		}
	}
	if code := attempt("127.0.0.1:4000", "203.0.113.5"); code != 429 {
		t.Fatalf("11th attempt: %d, want 429", code)
	}
	if code := attempt("127.0.0.1:4000", "203.0.113.6"); code != 400 {
		t.Fatalf("another forwarded client: %d", code)
	}
}

func TestLoginValidation(t *testing.T) {
	s := NewService(nil, nil, false) // none of these reach the DB
	mux := http.NewServeMux()
	s.Register(mux)
	for body, want := range map[string]int{
		`{"email":"a@b.c"}`: 400,
		`{"password":"x"}`:  400,
		`[`:                 400,
		`{"email":"a@b.c","password":"` + strings.Repeat("x", 73) + `"}`:   401, // over bcrypt's 72 bytes: never valid
		`{"email":"` + strings.Repeat("a", 250) + `@b.cd","password":"x"}`: 401, // over 254: never stored
	} {
		r := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(body))
		r.RemoteAddr = fmt.Sprintf("198.51.100.%d:1", len(body)%250) // stay under the rate limit
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("%.60s: %d %s, want %d", body, w.Code, w.Body, want)
		}
	}
	for _, args := range [][3]string{{" ", "x", "pw"}, {"a@b.c", "x", ""}, {"a@b.c", "", "pw"}} {
		if _, err := s.EnsureUser(context.Background(), args[0], args[1], args[2], "user"); err == nil {
			t.Fatalf("%q must be refused", args)
		}
	}
}

// Require turns away every request without a live session: no cookie, a
// malformed one, an unknown token or an expired session.
func TestRequire(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(httpx.UserID(r.Context()))) })
	call := func(s *Service, c *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/me", nil)
		if c != nil {
			r.AddCookie(c)
		}
		w := httptest.NewRecorder()
		s.Require(ok).ServeHTTP(w, r)
		return w
	}
	offline := NewService(nil, nil, false)
	for _, c := range []*http.Cookie{nil, {Name: "tsid", Value: "not base64!"}, {Name: "tsid", Value: "c2hvcnQ"}} {
		if w := call(offline, c); w.Code != 401 || !strings.Contains(w.Body.String(), `"unauthorized"`) {
			t.Errorf("cookie %v: %d %s", c, w.Code, w.Body)
		}
	}

	pool := storetest.Pool(t)
	user := storetest.User(t, pool)
	s := NewService(pool, usage.NewMeter(pool, 500), false)
	session := func(expires string) *http.Cookie {
		raw := make([]byte, 32)
		rand.Read(raw)
		sum := sha256.Sum256(raw)
		if _, err := pool.Exec(context.Background(), "insert into sessions (token_hash, user_id, expires_at) values ($1, $2, now() + $3::interval)",
			sum[:], user, expires); err != nil {
			t.Fatal(err)
		}
		return &http.Cookie{Name: "tsid", Value: base64.RawURLEncoding.EncodeToString(raw)}
	}
	if w := call(s, session("1 hour")); w.Code != 200 || w.Body.String() != user {
		t.Fatalf("live session: %d %q", w.Code, w.Body)
	}
	if w := call(s, session("-1 second")); w.Code != 401 {
		t.Fatalf("expired session: %d", w.Code)
	}
	if w := call(s, &http.Cookie{Name: "tsid", Value: base64.RawURLEncoding.EncodeToString(make([]byte, 32))}); w.Code != 401 {
		t.Fatalf("unknown token: %d", w.Code)
	}

	// A broken database is a 500, never a pass.
	broken := storetest.Pool(t)
	broken.Close()
	if w := call(NewService(broken, nil, false), session("1 hour")); w.Code != 500 {
		t.Fatalf("db down in Require: %d", w.Code)
	}
	mux := http.NewServeMux()
	NewService(broken, nil, false).Register(mux)
	r := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(`{"email":"a@b.c","password":"x"}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 500 {
		t.Fatalf("db down in login: %d", w.Code)
	}
	mux = http.NewServeMux()
	NewService(pool, usage.NewMeter(broken, 500), false).Register(mux)
	r = httptest.NewRequest("GET", "/api/me", nil)
	r.AddCookie(session("1 hour"))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 500 || strings.Contains(w.Body.String(), "closed") {
		t.Fatalf("credits unavailable in /api/me: %d %s", w.Code, w.Body)
	}
}

// A login records last_login_at and last_active_at; a disabled account gets
// the same 401 as a wrong password, and its live sessions stop working.
func TestDisabledAccount(t *testing.T) {
	pool := storetest.Pool(t)
	ctx := context.Background()
	s := NewService(pool, usage.NewMeter(pool, 500), false)
	email := "disabled-" + strings.ToLower(rand.Text()) + "@example.test"
	u, err := s.EnsureUser(ctx, email, "Dis", "pw-123456789", "user")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Exec(ctx, "delete from users where id = $1", u.ID) })
	mux := http.NewServeMux()
	s.Register(mux)
	login := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(`{"email":"`+email+`","password":"pw-123456789"}`))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	w := login()
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"role":"user"`) {
		t.Fatalf("login: %d %s", w.Code, w.Body)
	}
	var loggedIn bool
	pool.QueryRow(ctx, "select last_login_at > now() - interval '1 minute' and last_active_at = last_login_at from users where id = $1",
		u.ID).Scan(&loggedIn)
	if !loggedIn {
		t.Fatal("login must set last_login_at and last_active_at")
	}
	cookie := w.Result().Cookies()[0]
	if _, err := pool.Exec(ctx, "update users set disabled_at = now() where id = $1", u.ID); err != nil {
		t.Fatal(err)
	}
	if w := login(); w.Code != 401 || !strings.Contains(w.Body.String(), "Wrong email or password.") {
		t.Fatalf("disabled login: %d %s", w.Code, w.Body)
	}
	r := httptest.NewRequest("GET", "/api/me", nil)
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("a disabled user's session: %d", w.Code)
	}
}

// A login that verified the old password while an admin's password reset was
// in flight must not open a session once the reset commits.
func TestLoginDuringPasswordReset(t *testing.T) {
	pool := storetest.Pool(t)
	ctx := context.Background()
	s := NewService(pool, usage.NewMeter(pool, 500), false)
	email := "reset-" + strings.ToLower(rand.Text()) + "@example.test"
	u, err := s.EnsureUser(ctx, email, "Reset", "old-password-123", "user")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Exec(ctx, "delete from users where id = $1", u.ID) })
	hash, err := HashPassword("new-password-123")
	if err != nil {
		t.Fatal(err)
	}
	reset, err := pool.Begin(ctx) // the admin's reset, not yet committed
	if err != nil {
		t.Fatal(err)
	}
	defer reset.Rollback(ctx)
	var pid int
	if err := reset.QueryRow(ctx, "select pg_backend_pid()").Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err := reset.Exec(ctx, "update users set password_hash = $2 where id = $1", u.ID, hash); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, _, err := s.login(ctx, email, "old-password-123"); done <- err }()
	for i := 0; ; i++ { // the login reads the old hash, which still matches, then must wait for the reset
		select {
		case err := <-done:
			t.Fatalf("login finished while the reset was in flight: %v", err)
		default:
		}
		var waiting bool
		if err := pool.QueryRow(ctx, "select exists (select from pg_stat_activity where $1 = any(pg_blocking_pids(pid)))", pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if i == 500 {
			t.Fatal("the login never waited for the reset")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := reset.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	err = <-done
	var n int
	pool.QueryRow(ctx, "select count(*) from sessions where user_id = $1", u.ID).Scan(&n)
	if !errors.Is(err, errInvalidCredentials) || n != 0 {
		t.Fatalf("login after the reset: %v, %d sessions", err, n)
	}
}

// Require records activity at most once per 5 minutes per user.
func TestRequireTouchesLastActive(t *testing.T) {
	pool := storetest.Pool(t)
	user := storetest.User(t, pool)
	s := NewService(pool, nil, false)
	cookie := session(t, pool, user)
	call := func() {
		r := httptest.NewRequest("GET", "/", nil)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		s.Require(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("require: %d", w.Code)
		}
	}
	lastActive := func() (at *time.Time) {
		pool.QueryRow(context.Background(), "select last_active_at from users where id = $1", user).Scan(&at)
		return at
	}
	call()
	first := lastActive()
	if first == nil {
		t.Fatal("first request must set last_active_at")
	}
	call()
	if again := lastActive(); !again.Equal(*first) {
		t.Fatalf("a second request within 5 min must not write: %v → %v", first, again)
	}
	pool.Exec(context.Background(), "update users set last_active_at = now() - interval '6 minutes' where id = $1", user)
	call()
	if now := lastActive(); now == nil || time.Since(*now) > time.Minute {
		t.Fatalf("a stale last_active_at must be refreshed: %v", now)
	}
}

// RequireAdmin: 401 without a session, 403 for a non-admin (read on every
// request), no-store everywhere, and 60 mutations per admin per window.
func TestRequireAdmin(t *testing.T) {
	pool := storetest.Pool(t)
	user := storetest.User(t, pool)
	s := NewService(pool, nil, false)
	cookie := session(t, pool, user)
	h := s.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(httpx.UserID(r.Context()))) }))
	call := func(method string, c *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/admin/x", nil)
		if c != nil {
			r.AddCookie(c)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: Cache-Control %q", method, w.Header().Get("Cache-Control"))
		}
		return w
	}
	if w := call("GET", nil); w.Code != 401 {
		t.Fatalf("no session: %d", w.Code)
	}
	if w := call("GET", cookie); w.Code != 403 || !strings.Contains(w.Body.String(), `"forbidden"`) || !strings.Contains(w.Body.String(), "Admins only.") {
		t.Fatalf("non-admin: %d %s", w.Code, w.Body)
	}
	pool.Exec(context.Background(), "update users set role = 'admin' where id = $1", user)
	if w := call("GET", cookie); w.Code != 200 || w.Body.String() != user {
		t.Fatalf("admin: %d %s", w.Code, w.Body)
	}
	for i := range 60 {
		if w := call("POST", cookie); w.Code != 200 {
			t.Fatalf("mutation %d: %d", i+1, w.Code)
		}
	}
	if w := call("PATCH", cookie); w.Code != 429 || !strings.Contains(w.Body.String(), "Too many admin changes") {
		t.Fatalf("61st mutation: %d %s", w.Code, w.Body)
	}
	if w := call("GET", cookie); w.Code != 200 {
		t.Fatalf("reads are not limited: %d", w.Code)
	}
	pool.Exec(context.Background(), "update users set role = 'user' where id = $1", user)
	if w := call("GET", cookie); w.Code != 403 {
		t.Fatalf("demoted: %d", w.Code)
	}
}

// EnsureUser creates a missing account with the given role; on an existing one
// it only refreshes a changed password (signing it out) and never re-enables,
// re-promotes or renames what the console changed.
func TestEnsureUser(t *testing.T) {
	pool := storetest.Pool(t)
	ctx := context.Background()
	s := NewService(pool, nil, false)
	email := "boot-" + strings.ToLower(rand.Text()) + "@example.test"
	u, err := s.EnsureUser(ctx, email, "Admin", "admin-pass-1234", "admin")
	if err != nil || u.Role != "admin" || u.Name != "Admin" || u.ID == "" {
		t.Fatalf("create: %+v %v", u, err)
	}
	t.Cleanup(func() { pool.Exec(ctx, "delete from users where id = $1", u.ID) })
	if _, err := pool.Exec(ctx, `update users set role = 'user', disabled_at = now(), name = 'Renamed', credit_limit = 7
		where id = $1`, u.ID); err != nil {
		t.Fatal(err)
	}
	session(t, pool, u.ID)
	sessions := func() (n int) {
		pool.QueryRow(ctx, "select count(*) from sessions where user_id = $1", u.ID).Scan(&n)
		return n
	}
	again, err := s.EnsureUser(ctx, email, "Admin", "admin-pass-1234", "admin")
	if err != nil || again.ID != u.ID || again.Role != "user" || again.Name != "Renamed" || sessions() != 1 {
		t.Fatalf("restart with the same password: %+v %v, %d sessions", again, err, sessions())
	}
	if _, err := s.EnsureUser(ctx, email, "Admin", "admin-pass-rotated", "admin"); err != nil || sessions() != 0 {
		t.Fatalf("rotation: %v, %d sessions", err, sessions())
	}
	var role, name, hash string
	var disabled bool
	var limit int
	pool.QueryRow(ctx, "select role, name, password_hash, disabled_at is not null, credit_limit from users where id = $1",
		u.ID).Scan(&role, &name, &hash, &disabled, &limit)
	if role != "user" || name != "Renamed" || !disabled || limit != 7 || bcrypt.CompareHashAndPassword([]byte(hash), []byte("admin-pass-rotated")) != nil {
		t.Fatalf("after rotation: role %s name %s disabled %t limit %d", role, name, disabled, limit)
	}
	broken := storetest.Pool(t)
	broken.Close()
	if _, err := NewService(broken, nil, false).EnsureUser(ctx, email, "Admin", "x", "admin"); err == nil {
		t.Fatal("db down must fail")
	}
}

// session inserts a live session for user, the way login stores one.
func session(t *testing.T, pool *pgxpool.Pool, user string) *http.Cookie {
	t.Helper()
	raw := make([]byte, 32)
	rand.Read(raw)
	sum := sha256.Sum256(raw)
	if _, err := pool.Exec(context.Background(), "insert into sessions (token_hash, user_id, expires_at) values ($1, $2, now() + interval '1 hour')",
		sum[:], user); err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: "tsid", Value: base64.RawURLEncoding.EncodeToString(raw)}
}

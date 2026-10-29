package main

import (
	"context"
	"crypto/rand"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/vincenttanjaya/Foretrade/api/internal/config"
)

// spaDirs writes two built SPAs (index.html and one hashed asset each) and a
// secret beside them that no request may reach.
func spaDirs(t *testing.T) (web, admin string) {
	t.Helper()
	root := t.TempDir()
	for name, body := range map[string]string{
		"web/index.html": "web index", "web/assets/app-1a2b.js": "web js",
		"admin/index.html": "admin index", "admin/assets/admin-3c4d.js": "admin js", "secret.txt": "TOP-SECRET",
	} {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(root, "web"), filepath.Join(root, "admin")
}

// The listeners' routes, against a stub API: PORT serves the web SPA and the
// API but the console's routes, ADMIN_PORT the console's SPA and the whole
// API, and a single listener the whole API plus WEB_DIR when set.
func TestSites(t *testing.T) {
	web, admin := spaDirs(t)
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("api")) })
	adminLn := &net.TCPListener{}
	split := sites(api, config.Config{WebDir: web, AdminDir: admin}, adminLn)
	single := sites(api, config.Config{WebDir: web, AdminDir: admin}, nil)
	bare := sites(api, config.Config{}, nil)
	apiOnly := sites(api, config.Config{}, adminLn) // ADMIN_PORT without built SPAs
	do := func(h http.Handler, onAdmin bool, method, target string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, target, nil)
		if onAdmin {
			r = r.WithContext(listenerContext(r.Context(), adminLn, adminLn))
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	const page, asset = "no-cache", "public, max-age=31536000, immutable"
	for _, tc := range []struct {
		name    string
		h       http.Handler
		onAdmin bool
		method  string
		target  string
		code    int
		body    string
		cache   string
	}{
		{"web root", split, false, "GET", "/", 200, "web index", page},
		{"web deep link", split, false, "GET", "/trade/0700.HK", 200, "web index", page},
		{"web HEAD", split, false, "HEAD", "/portfolio", 200, "", page},
		{"web asset", split, false, "GET", "/assets/app-1a2b.js", 200, "web js", asset},
		{"web missing asset", split, false, "GET", "/assets/app-old.js", 404, "404 page not found\n", ""},
		{"web other SPA's asset", split, false, "GET", "/assets/admin-3c4d.js", 404, "404 page not found\n", ""},
		{"web api", split, false, "GET", "/api/me", 200, "api", ""},
		{"web login", split, false, "POST", "/api/auth/login", 200, "api", ""},
		{"web healthz", split, false, "GET", "/healthz", 200, "api", ""},
		{"web admin api", split, false, "GET", "/api/admin/users", 404, "404 page not found\n", ""},
		{"web admin api write", split, false, "POST", "/api/admin/users", 404, "404 page not found\n", ""},
		{"web admin api escaped", split, false, "GET", "/api/%61dmin/users", 404, "404 page not found\n", ""},
		{"web admin api root", split, false, "GET", "/api/admin/", 404, "404 page not found\n", ""},
		{"web oauth callback", split, false, "GET", "/auth/callback?code=c&state=s", 404, "404 page not found\n", ""},

		{"admin root", split, true, "GET", "/", 200, "admin index", page},
		{"admin deep link", split, true, "GET", "/users/42", 200, "admin index", page},
		{"admin asset", split, true, "GET", "/assets/admin-3c4d.js", 200, "admin js", asset},
		{"admin admin api", split, true, "GET", "/api/admin/users", 200, "api", ""},
		{"admin api", split, true, "GET", "/api/me", 200, "api", ""},
		{"admin oauth callback", split, true, "GET", "/auth/callback?code=c&state=s", 200, "api", ""},
		{"admin healthz", split, true, "GET", "/healthz", 200, "api", ""},

		{"single root", single, false, "GET", "/", 200, "web index", page},
		{"single admin api", single, false, "GET", "/api/admin/users", 200, "api", ""},
		{"single oauth callback", single, false, "GET", "/auth/callback", 200, "api", ""},
		{"single healthz", single, false, "GET", "/healthz", 200, "api", ""},

		{"bare root", bare, false, "GET", "/", 200, "api", ""},
		{"bare unknown", bare, false, "GET", "/settings", 200, "api", ""},
		{"bare admin api", bare, false, "GET", "/api/admin/users", 200, "api", ""},

		{"api-only web root", apiOnly, false, "GET", "/", 200, "api", ""},
		{"api-only web admin api", apiOnly, false, "GET", "/api/admin/users", 404, "404 page not found\n", ""},
		{"api-only admin admin api", apiOnly, true, "GET", "/api/admin/users", 200, "api", ""},
	} {
		w := do(tc.h, tc.onAdmin, tc.method, tc.target)
		if w.Code != tc.code || w.Body.String() != tc.body || w.Header().Get("Cache-Control") != tc.cache {
			t.Errorf("%s: %s %s = %d %q cache %q", tc.name, tc.method, tc.target, w.Code, w.Body, w.Header().Get("Cache-Control"))
		}
	}
	if ct := do(split, false, "GET", "/assets/app-1a2b.js").Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
		t.Errorf("asset type %q", ct)
	}
	if ct := do(split, false, "GET", "/portfolio").Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("index type %q", ct)
	}
	// Every SPA response refuses framing and type sniffing.
	for _, tc := range []struct {
		onAdmin bool
		target  string
	}{{false, "/"}, {false, "/assets/app-1a2b.js"}, {false, "/assets/app-old.js"}, {true, "/users/42"}} {
		h := do(split, tc.onAdmin, "GET", tc.target).Header()
		if h.Get("X-Frame-Options") != "DENY" || h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Referrer-Policy") != "strict-origin-when-cross-origin" {
			t.Errorf("%s headers: %v", tc.target, h)
		}
	}
	// A cached index revalidates to a 304.
	r := httptest.NewRequest("GET", "/portfolio", nil)
	r.Header.Set("If-Modified-Since", do(split, false, "GET", "/").Header().Get("Last-Modified"))
	w := httptest.NewRecorder()
	split.ServeHTTP(w, r)
	if w.Code != http.StatusNotModified {
		t.Errorf("revalidation: %d", w.Code)
	}
}

// No path, however encoded, reads a file outside the SPA's directory: a
// literal ".." is redirected to its clean in-root path, an escaped one refused.
func TestSPATraversal(t *testing.T) {
	web, _ := spaDirs(t)
	h := site(http.NotFoundHandler(), web, false)
	for target, want := range map[string]int{
		"/../secret.txt": 307, "//../secret.txt": 307, "/secret.txt": 200,
		"/..%2fsecret.txt": 400, "/%2e%2e/secret.txt": 400, "/%2e%2e%2fsecret.txt": 400, "/..%5csecret.txt": 400,
		"/assets/..%2f..%2fsecret.txt": 400, "/assets/%2e%2e/%2e%2e/secret.txt": 400,
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", target, nil)) // parsed as the server parses a request line
		if w.Code != want || want == 307 && w.Header().Get("Location") != "/secret.txt" || want == 200 && w.Body.String() != "web index" ||
			strings.Contains(w.Body.String(), "TOP-SECRET") {
			t.Errorf("%s: %d %q %v", target, w.Code, w.Body, w.Header())
		}
	}
}

// The image's HEALTHCHECK passes only on a 200 from /healthz on PORT.
func TestHealthcheck(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			status = http.StatusNotFound
		}
		w.WriteHeader(status)
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	t.Setenv("PORT", port)
	if got := healthcheck(); got != 0 {
		t.Fatalf("healthy: %d", got)
	}
	status = http.StatusServiceUnavailable
	if got := healthcheck(); got != 1 {
		t.Fatalf("unhealthy: %d", got)
	}
	srv.Close()
	if got := healthcheck(); got != 1 {
		t.Fatalf("down: %d", got)
	}
}

// start binds ADMIN_PORT when set; a failed bind stops it before boot.
func TestStartAdminPortError(t *testing.T) {
	isolateAWS(t, "")
	t.Setenv("MODEL_KNOWLEDGE_CUTOFF", "")
	t.Setenv("PORT", "0")
	t.Setenv("ADMIN_PORT", "-1")
	if err := start(context.Background()); err == nil || !strings.Contains(err.Error(), "listen") {
		t.Fatalf("bad admin port: %v", err)
	}
}

// A failed boot closes the admin listener too.
func TestRunBootFailureClosesAdmin(t *testing.T) {
	isolateAWS(t, "")
	var lns [2]net.Listener
	for i := range lns {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		lns[i] = ln
	}
	if err := runWithOptions(context.Background(), config.Config{}, lns[0], runOptions{admin: lns[1]}); err == nil {
		t.Fatal("run without a database must fail")
	}
	for _, ln := range lns {
		if _, err := ln.Accept(); !errors.Is(err, net.ErrClosed) {
			t.Fatalf("listener %s left open: %v", ln.Addr(), err)
		}
	}
}

// With an admin listener, the booted server serves the web SPA and the public
// API on PORT and the console's SPA and the whole API on ADMIN_PORT; an admin
// signs in on the console's port, the same session gets 404 for /api/admin/*
// on PORT, and shutdown closes both listeners.
func TestIntegrationListeners(t *testing.T) {
	if os.Getenv("INTEGRATION_DATABASE_URL") == "" {
		t.Skip("INTEGRATION_DATABASE_URL not set") // as boot would, before the listener below exists
	}
	web, admin := spaDirs(t)
	t.Setenv("WEB_DIR", web)
	t.Setenv("ADMIN_DIR", admin)
	aln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	console := "http://" + aln.Addr().String()
	t.Cleanup(func() { // after boot's cleanup has stopped the server
		if c, err := net.Dial("tcp", aln.Addr().String()); err == nil {
			c.Close()
			t.Error("admin listener still open after shutdown")
		}
	})
	s := boot(t, options{runtime: &runOptions{admin: aln}})

	email := "it-admin-" + strings.ToLower(rand.Text()) + "@example.test"
	hash, err := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(context.Background(), "insert into users (email, name, password_hash, role) values ($1, 'Admin', $2, 'admin')", email, hash); err != nil {
		t.Fatal(err)
	}
	c := s.client(t) // its jar sends the session to both ports: cookies ignore the port
	do := func(method, base, path, from string, body string) (int, string) {
		t.Helper()
		r, err := http.NewRequest(method, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if from != "" {
			r.Header.Set("Origin", from)
		}
		if method != http.MethodGet {
			r.Header.Set("Content-Type", "application/json")
		}
		resp, b := c.send(r)
		return resp.StatusCode, string(b)
	}
	for _, tc := range []struct {
		method, base, path, from, body string
		code                           int
		want                           string
	}{
		{"GET", s.url, "/", "", "", 200, "web index"},
		{"GET", s.url, "/portfolio", "", "", 200, "web index"},
		{"GET", s.url, "/healthz", "", "", 200, "ok"},
		{"GET", s.url, "/api/me", origin, "", 401, `"unauthorized"`},
		{"GET", console, "/", "", "", 200, "admin index"},
		{"GET", console, "/users", "", "", 200, "admin index"},
		{"GET", console, "/healthz", "", "", 200, "ok"},
		{"GET", console, "/api/admin/audit", adminOrigin, "", 401, `"unauthorized"`},
		{"POST", console, "/api/auth/login", adminOrigin, `{"email":"` + email + `","password":"` + testPassword + `"}`, 200, `"role":"admin"`},
		{"GET", console, "/api/admin/audit", adminOrigin, "", 200, `"entries"`},
		{"GET", s.url, "/api/admin/audit", "", "", 404, "404 page not found"},
		{"GET", s.url, "/api/me", origin, "", 200, email},
	} {
		if code, body := do(tc.method, tc.base, tc.path, tc.from, tc.body); code != tc.code || !strings.Contains(body, tc.want) {
			t.Errorf("%s %s%s: %d %.200s", tc.method, tc.base, tc.path, code, body)
		}
	}
}

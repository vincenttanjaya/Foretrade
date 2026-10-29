package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/auth"
	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
	"github.com/vincenttanjaya/Foretrade/api/internal/research"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
	"github.com/vincenttanjaya/Foretrade/api/internal/usage"
)

func TestMain(m *testing.M) {
	// ponytail: cost 12 takes seconds per hash under -race; the hash logic is the same at MinCost.
	auth.BcryptCost = bcrypt.MinCost
	os.Exit(m.Run())
}

var ctx = context.Background()

// setup is a handler env (auth routes + admin routes) whose signed-in user is an admin.
func setup(t *testing.T, o apptest.Options) *apptest.Env {
	t.Helper()
	e := apptest.New(t, func(mux *http.ServeMux, d *app.Deps) {
		d.Auth.Register(mux)
		Register(mux, d)
	}, o)
	exec(t, e, "update users set role = 'admin', last_active_at = now() where id = $1", e.User)
	e.Version, e.StartedAt = "0123456789ab", time.Now().Add(-time.Hour)
	return e
}

func exec(t *testing.T, e *apptest.Env, sql string, args ...any) {
	t.Helper()
	if _, err := e.DB.Exec(ctx, sql, args...); err != nil {
		t.Fatal(err)
	}
}

func scalar[T any](t *testing.T, e *apptest.Env, sql string, args ...any) T {
	t.Helper()
	var v T
	if err := e.DB.QueryRow(ctx, sql, args...).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// account adds a user with role, recently active (so Require never writes its
// row), and a live session.
func account(t *testing.T, e *apptest.Env, role string) (id, email string, c *http.Cookie) {
	t.Helper()
	id = storetest.User(t, e.DB)
	email = scalar[string](t, e, "update users set role = $2, last_active_at = now() where id = $1 returning email", id, role)
	return id, email, e.Session(id)
}

type userResp struct {
	User     User    `json:"user"`
	Password *string `json:"password"`
	Revoked  *int    `json:"revoked"`
}

func audits(t *testing.T, e *apptest.Env) []AuditEntry {
	t.Helper()
	w := e.Do("GET", "/api/admin/audit?limit=100", nil)
	if w.Code != 200 {
		t.Fatalf("audit: %d %s", w.Code, w.Body)
	}
	return apptest.JSON[struct{ Entries []AuditEntry }](t, w).Entries
}

// detail decodes an audit entry's detail for comparison (numbers are float64).
func detail(t *testing.T, a AuditEntry) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(a.Detail, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func me(e *apptest.Env, c *http.Cookie) int { return e.DoAs(c, "GET", "/api/me", nil).Code }

func login(e *apptest.Env, email, password string) *httptest.ResponseRecorder {
	return e.DoAs(nil, "POST", "/api/auth/login", map[string]string{"email": email, "password": password})
}

// Every admin route: 401 without a session, 403 for a non-admin, served for
// an admin; all of them no-store.
func TestAccessMatrix(t *testing.T) {
	e := setup(t, apptest.Options{})
	target, _, _ := account(t, e, "user")
	victim, _, _ := account(t, e, "user")
	_, source, user := account(t, e, "user")
	for _, rt := range []struct {
		method, path, body string
		want               int
	}{
		{"GET", "/api/admin/overview", "", 200},
		{"GET", "/api/admin/users", "", 200},
		{"POST", "/api/admin/users", `{"email":"matrix@example.test","name":"Matrix"}`, 201},
		{"GET", "/api/admin/users/" + target, "", 200},
		{"PATCH", "/api/admin/users/" + target, `{"name":"Renamed"}`, 200},
		{"POST", "/api/admin/users/" + target + "/password", `{}`, 200},
		{"POST", "/api/admin/users/" + target + "/sessions/revoke", `{}`, 200},
		{"POST", "/api/admin/users/" + target + "/reset-portfolio", `{}`, 200},
		{"POST", "/api/admin/users/" + target + "/reset-credits", `{}`, 200},
		{"POST", "/api/admin/users/" + target + "/seed", `{"from":"` + source + `"}`, 200},
		{"DELETE", "/api/admin/users/" + victim, `{}`, 204},
		{"GET", "/api/admin/audit", "", 200},
		{"GET", "/api/admin/nope", "", 404},
	} {
		for _, as := range []struct {
			cookie *http.Cookie
			want   int
		}{{nil, 401}, {user, 403}, {e.Cookie, rt.want}} {
			var body any
			if rt.body != "" {
				body = rt.body
			}
			w := e.DoAs(as.cookie, rt.method, rt.path, body)
			if w.Code != as.want || w.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("%s %s: %d (cache %q) %.200s, want %d", rt.method, rt.path, w.Code, w.Header().Get("Cache-Control"), w.Body, as.want)
			}
			if as.want == 403 && !strings.Contains(w.Body.String(), "Admins only.") {
				t.Errorf("%s %s 403 body: %s", rt.method, rt.path, w.Body)
			}
		}
	}
}

// Only the console's origin may call the admin API from a browser. The web
// app's origin shares the cookie and ALLOWED_ORIGINS but is refused.
func TestAdminOrigin(t *testing.T) {
	e := apptest.New(t, func(mux *http.ServeMux, d *app.Deps) {
		d.AdminOrigins = []string{"https://admin.example.com"}
		Register(mux, d)
	}, apptest.Options{})
	exec(t, e, "update users set role = 'admin' where id = $1", e.User)
	for i, tc := range []struct {
		method, origin string
		want           int
	}{
		{"GET", "https://app.example.com", 403},
		{"POST", "https://app.example.com", 403},
		{"GET", "https://admin.example.com", 200},
		{"POST", "https://admin.example.com", 201},
		{"GET", "", 200}, // curl
	} {
		r := httptest.NewRequest(tc.method, "/api/admin/users", strings.NewReader(fmt.Sprintf(`{"email":"origin%d@example.test","name":"O","role":"admin"}`, i)))
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(e.Cookie)
		if tc.origin != "" {
			r.Header.Set("Origin", tc.origin)
		}
		w := httptest.NewRecorder()
		e.Mux.ServeHTTP(w, r)
		if w.Code != tc.want || w.Header().Get("Cache-Control") != "no-store" || (tc.want == 403 && apptest.ErrorCode(t, w) != "forbidden") {
			t.Errorf("%s from %q: %d %s", tc.method, tc.origin, w.Code, w.Body)
		}
	}
	if n := scalar[int](t, e, "select count(*) from users where email like 'origin%'"); n != 1 {
		t.Fatalf("%d accounts created, want only the console's", n)
	}
}

func TestCreate(t *testing.T) {
	e := setup(t, apptest.Options{})
	e.Managed = []string{"managed@example.test"}

	// No password given: one is generated, returned once, and works.
	w := e.Do("POST", "/api/admin/users", map[string]any{"email": "  New.Person@Example.TEST ", "name": " New Person ",
		"role": "admin", "creditLimit": 50})
	res := apptest.JSON[userResp](t, w)
	u := res.User
	if w.Code != 201 || res.Password == nil || len(*res.Password) != 26 || u.Email != "new.person@example.test" || u.Name != "New Person" ||
		u.Role != "admin" || u.Status != "active" || u.CreditLimit == nil || *u.CreditLimit != 50 || u.Sessions != 0 ||
		u.Equity == nil || *u.Equity != 100000 || u.Managed || u.LastLoginAt != nil {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	if w := login(e, u.Email, *res.Password); w.Code != 200 || !strings.Contains(w.Body.String(), `"role":"admin"`) {
		t.Fatalf("login with the generated password: %d %s", w.Code, w.Body)
	}
	hash := scalar[string](t, e, "select password_hash from users where id = $1", u.ID)
	if cost, err := bcrypt.Cost([]byte(hash)); err != nil || cost != auth.BcryptCost {
		t.Fatalf("stored hash: cost %d, %v", cost, err)
	}

	// A supplied password is never echoed; null creditLimit is the default.
	const pw = "correct horse battery"
	w = e.Do("POST", "/api/admin/users", map[string]any{"email": "managed@example.test", "name": "Managed", "password": pw, "creditLimit": nil})
	if res := apptest.JSON[userResp](t, w); w.Code != 201 || strings.Contains(w.Body.String(), pw) || res.Password != nil ||
		res.User.CreditLimit != nil || !res.User.Managed || res.User.Role != "user" {
		t.Fatalf("supplied password: %d %s", w.Code, w.Body)
	}
	if w := login(e, "managed@example.test", pw); w.Code != 200 {
		t.Fatalf("login with the supplied password: %d", w.Code)
	}
	if w := e.Do("POST", "/api/admin/users", `{"email":"MANAGED@example.test","name":"Again"}`); w.Code != 409 ||
		!strings.Contains(w.Body.String(), msgExists) {
		t.Fatalf("duplicate email: %d %s", w.Code, w.Body)
	}

	for _, body := range []string{
		`{"email":`, `{"email":5,"name":"X"}`,
		`{"email":"nobody","name":"X"}`,
		`{"email":"Bob <bob@example.test>","name":"X"}`,
		`{"email":"bob@localhost","name":"X"}`,
		`{"email":"` + strings.Repeat("a", 250) + `@example.test","name":"X"}`,
		`{"email":"ok@example.test","name":"   "}`,
		`{"email":"ok@example.test","name":"` + strings.Repeat("n", 81) + `"}`,
		`{"email":"ok@example.test","name":"a\u0000b"}`,
		`{"email":"ok@example.test","name":"X","role":"root"}`,
		`{"email":"ok@example.test","name":"X","creditLimit":-1}`,
		`{"email":"ok@example.test","name":"X","creditLimit":100001}`,
		`{"email":"ok@example.test","name":"X","creditLimit":1.5}`,
		`{"email":"ok@example.test","name":"X","creditLimit":"5"}`,
		`{"email":"ok@example.test","name":"X","password":"short"}`,
		`{"email":"ok@example.test","name":"X","password":"              "}`,
		`{"email":"ok@example.test","name":"X","password":"` + strings.Repeat("p", 73) + `"}`,
	} {
		if w := e.Do("POST", "/api/admin/users", body); w.Code != 400 || apptest.ErrorCode(t, w) != "bad_request" {
			t.Errorf("%.80s: %d %s", body, w.Code, w.Body)
		}
	}

	// Exactly the two creates were audited, newest first; no password anywhere.
	got := audits(t, e)
	if len(got) != 2 || got[0].Action != "user.create" || got[0].Target.Email != "managed@example.test" || got[0].Admin.ID != e.User ||
		!reflect.DeepEqual(detail(t, got[0]), map[string]any{"role": "user", "creditLimit": nil, "passwordGenerated": false}) ||
		!reflect.DeepEqual(detail(t, got[1]), map[string]any{"role": "admin", "creditLimit": 50.0, "passwordGenerated": true}) {
		t.Fatalf("audit: %+v", got)
	}
	if raw := scalar[string](t, e, "select string_agg(detail::text, ' ') from admin_audit"); strings.Contains(raw, pw) ||
		strings.Contains(raw, *res.Password) || strings.Contains(raw, "$2a$") {
		t.Fatalf("audit leaks a secret: %s", raw)
	}
}

func TestList(t *testing.T) {
	e := setup(t, apptest.Options{})
	ids := map[string]string{}
	for i, u := range []struct{ email, name, role, extra string }{
		{"alpha@list.test", "Zed 100%", "user", "last_login_at = now() - interval '1 hour'"},
		{"beta@list.test", "yan_b", "admin", "last_login_at = null"},
		{"gamma@list.test", "Xavier", "user", "disabled_at = now(), last_login_at = now() - interval '2 hours'"},
		{"delta@list.test", "nab", "user", "last_login_at = null"},
	} {
		ids[u.email] = scalar[string](t, e, `insert into users (email, name, password_hash, role, created_at) values ($1, $2, 'x', $3, now() - $4::interval)
			returning id`, u.email, u.name, u.role, fmt.Sprintf("%d minutes", i+1))
		exec(t, e, "update users set "+u.extra+" where id = $1", ids[u.email])
	}
	alpha := ids["alpha@list.test"]
	exec(t, e, "insert into accounts (user_id, cash) values ($1, 50000)", alpha)
	exec(t, e, "insert into positions (user_id, symbol, qty, avg_price) values ($1, 'BTC', 1, 1), ($1, 'ZZZ', 10, 2)", alpha) // ZZZ: no live price
	e.Session(alpha)
	exec(t, e, "insert into sessions (token_hash, user_id, expires_at) values ('\\x01', $1, now() - interval '1 second')", alpha)
	if _, err := e.Usage.Spend(ctx, ids["delta@list.test"], 9); err != nil {
		t.Fatal(err)
	}

	type page struct {
		Users              []User
		Total, Page, Limit int
		DefaultCreditLimit int
	}
	list := func(query string) (page, []string) {
		t.Helper()
		w := e.Do("GET", "/api/admin/users?"+query, nil)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", query, w.Code, w.Body)
		}
		p := apptest.JSON[page](t, w)
		var emails []string
		for _, u := range p.Users {
			emails = append(emails, strings.TrimSuffix(u.Email, "@list.test"))
		}
		return p, emails
	}
	for query, want := range map[string]string{
		"q=LIST.TEST&sort=email&order=asc": "alpha beta delta gamma",
		"q=list.test&sort=email":           "gamma delta beta alpha",
		"q=%25":                            "alpha", // a literal %
		"q=n_b":                            "beta",  // a literal _ (not "nab")
		"q=%20%20xav%20":                   "gamma",
		"q=list.test&status=disabled":      "gamma",
		"q=list.test&status=active&sort=email&order=asc":                   "alpha beta delta",
		"q=list.test&role=admin":                                           "beta",
		"q=list.test&sort=name&order=asc":                                  "delta gamma beta alpha",
		"q=list.test&sort=creditsToday&order=desc&role=user&status=active": "delta alpha",
		"q=list.test&sort=email&order=asc&limit=2&page=2":                  "delta gamma",
		"q=list.test&page=3&limit=2":                                       "",
	} {
		p, got := list(query)
		if strings.Join(got, " ") != want {
			t.Errorf("%s: %v, want %s", query, got, want)
		}
		if strings.Contains(query, "page=") && p.Total != 4 {
			t.Errorf("%s: total %d", query, p.Total)
		}
	}
	// Nulls sort last in both orders.
	for order, first := range map[string]string{"desc": "alpha gamma", "asc": "gamma alpha"} {
		if _, got := list("q=list.test&sort=lastLoginAt&order=" + order); strings.Join(got[:2], " ") != first {
			t.Errorf("lastLoginAt %s: %v", order, got)
		}
	}
	p, _ := list("q=alpha")
	a := p.Users[0]
	if p.Page != 1 || p.Limit != 25 || p.DefaultCreditLimit != 500 || p.Total != 1 || a.Sessions != 1 || a.Equity == nil ||
		*a.Equity != 50000+markettest.Price("BTCUSDT")+20 || a.Status != "active" || a.LastLoginAt == nil {
		t.Fatalf("alpha: %+v", p)
	}
	if p, _ := list("q=delta"); p.Users[0].CreditsToday != 9 || p.Users[0].CreditLimit != nil {
		t.Fatalf("delta: %+v", p.Users[0])
	}
	if p, _ := list("q=gamma"); p.Users[0].Status != "disabled" || p.Users[0].DisabledAt == nil {
		t.Fatalf("gamma: %+v", p.Users[0])
	}

	for _, query := range []string{"status=x", "role=root", "sort=password", "order=up", "page=0", "page=a", "limit=0", "limit=101",
		"q=" + strings.Repeat("q", 101), "q=%00", "q=%ff"} {
		if w := e.Do("GET", "/api/admin/users?"+query, nil); w.Code != 400 {
			t.Errorf("%s: %d", query, w.Code)
		}
	}
}

func TestUpdate(t *testing.T) {
	e := setup(t, apptest.Options{})
	target, _, cookie := account(t, e, "user")
	patch := func(body string) (*httptest.ResponseRecorder, User) {
		t.Helper()
		w := e.Do("PATCH", "/api/admin/users/"+target, body)
		return w, apptest.JSON[userResp](t, w).User
	}
	n := func() int { return len(audits(t, e)) }

	w, u := patch(`{"name":" Renamed ","role":"admin","creditLimit":42}`)
	if w.Code != 200 || u.Name != "Renamed" || u.Role != "admin" || *u.CreditLimit != 42 {
		t.Fatalf("update: %d %s", w.Code, w.Body)
	}
	got := audits(t, e)
	if len(got) != 1 || got[0].Action != "user.update" || !reflect.DeepEqual(detail(t, got[0]), map[string]any{
		"name":        map[string]any{"from": "Test", "to": "Renamed"},
		"role":        map[string]any{"from": "user", "to": "admin"},
		"creditLimit": map[string]any{"from": nil, "to": 42.0}}) {
		t.Fatalf("update audit: %+v", got)
	}
	if w, _ := patch(`{"name":"Renamed","role":"admin","creditLimit":42,"disabled":false}`); w.Code != 200 || n() != 1 {
		t.Fatalf("a PATCH that changes nothing writes nothing: %d, %d entries", w.Code, n())
	}
	if w, u := patch(`{"creditLimit":null,"role":"user"}`); w.Code != 200 || u.CreditLimit != nil || u.Role != "user" ||
		!reflect.DeepEqual(detail(t, audits(t, e)[0]), map[string]any{
			"creditLimit": map[string]any{"from": 42.0, "to": nil}, "role": map[string]any{"from": "admin", "to": "user"}}) {
		t.Fatalf("limit back to the default: %d %s", w.Code, w.Body)
	}

	// Disabling signs the account out everywhere and stops its running autopilot (its position and books
	// stay: no market call); enabling lets it sign in again.
	e.Session(target)
	exec(t, e, `insert into autopilots (user_id, strategy_id, name, spec, allocation_usd, max_drawdown_pct, status, peak_equity,
		position_qty, avg_price, realized_pnl) values ($1, 'test', 'Always', '{"symbol":"SOL","interval":"4h"}', 1000, 20, 'running', 1000, 2, 150, -0.3),
		($1, 'test', 'Old', '{"symbol":"SOL","interval":"4h"}', 1000, 20, 'stopped', 1000, 0, 0, 5)`, target)
	w, u = patch(`{"disabled":true}`)
	if w.Code != 200 || u.Status != "disabled" || u.DisabledAt == nil || u.Sessions != 0 || me(e, cookie) != 401 {
		t.Fatalf("disable: %d %s", w.Code, w.Body)
	}
	if a := audits(t, e)[0]; a.Action != "user.disable" || !reflect.DeepEqual(detail(t, a), map[string]any{"sessionsRevoked": 2.0, "autopilotsStopped": 1.0, "copyPlansStopped": 0.0}) {
		t.Fatalf("disable audit: %+v", a)
	}
	if got := scalar[string](t, e, `select concat_ws(' | ', status, stop_reason, position_qty, avg_price, realized_pnl, stopped_at is not null, log->0->>'kind', log->0->>'text')
		from autopilots where user_id = $1 and name = 'Always'`, target); got != "stopped | account_disabled | 2 | 150 | -0.3 | t | stop | Account disabled by an admin; position kept." {
		t.Fatalf("autopilot after the disable: %s", got)
	}
	if w, _ := patch(`{"disabled":true}`); w.Code != 200 || n() != 3 {
		t.Fatalf("disabling twice: %d, %d entries", w.Code, n())
	}
	if w, u := patch(`{"disabled":false}`); w.Code != 200 || u.Status != "active" || u.DisabledAt != nil ||
		audits(t, e)[0].Action != "user.enable" || string(audits(t, e)[0].Detail) != "{}" {
		t.Fatalf("enable: %d %s", w.Code, w.Body)
	}

	for _, body := range []string{`{}`, `{"unknown":1}`, `{"name":""}`, `{"name":"a\u0000b"}`, `{"role":"root"}`, `{"creditLimit":-5}`, `{"disabled":"yes"}`, `[`} {
		if w, _ := patch(body); w.Code != 400 {
			t.Errorf("%s: %d", body, w.Code)
		}
	}
	for _, path := range []string{"not-a-uuid", "00000000-0000-4000-8000-000000000000", "00000000x0000x4000x8000x000000000000", strings.Repeat("0", 36)} {
		if w := e.Do("PATCH", "/api/admin/users/"+path, `{"name":"X"}`); w.Code != 404 {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
	for _, body := range []string{`{"role":"user"}`, `{"disabled":true}`} {
		if w := e.Do("PATCH", "/api/admin/users/"+e.User, body); w.Code != 409 || !strings.Contains(w.Body.String(), msgSelf) {
			t.Errorf("self %s: %d %s", body, w.Code, w.Body)
		}
	}
	if w := e.Do("PATCH", "/api/admin/users/"+e.User, `{"name":"Me"}`); w.Code != 200 {
		t.Fatalf("renaming yourself: %d", w.Code)
	}
}

// waitBlocked waits until n requests are queued on the active-admin lock.
func waitBlocked(t *testing.T, e *apptest.Env, n int) {
	t.Helper()
	for range 500 {
		if scalar[int](t, e, `select count(*) from pg_stat_activity where wait_event_type = 'Lock'
			and query like '%role = ''admin'' and disabled_at is null%'`) >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%d requests never queued on the admin lock", n)
}

// Two admins demoting each other at once: the lock serializes them, so one
// wins and the other is the last active admin (409). Never zero admins.
func TestConcurrentDemotion(t *testing.T) {
	e := setup(t, apptest.Options{})
	b, _, bCookie := account(t, e, "admin")
	hold, err := e.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hold.Exec(ctx, "select id from users where role = 'admin' for update"); err != nil {
		t.Fatal(err)
	}
	results := make(chan *httptest.ResponseRecorder, 2)
	go func() { results <- e.Do("PATCH", "/api/admin/users/"+b, `{"role":"user"}`) }()
	go func() { results <- e.DoAs(bCookie, "PATCH", "/api/admin/users/"+e.User, `{"role":"user"}`) }()
	waitBlocked(t, e, 2)
	if err := hold.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	r1, r2 := <-results, <-results
	codes := []int{r1.Code, r2.Code}
	slices.Sort(codes)
	loser := r1
	if r1.Code == 200 {
		loser = r2
	}
	if codes[0] != 200 || codes[1] != 409 || !strings.Contains(loser.Body.String(), msgLastAdmin) {
		t.Fatalf("codes %v: %s", codes, loser.Body)
	}
	// Either admin may have won, so count with SQL: the env's own admin may be demoted.
	if n, a := scalar[int](t, e, "select count(*) from users where role = 'admin' and disabled_at is null"),
		scalar[int](t, e, "select count(*) from admin_audit"); n != 1 || a != 1 {
		t.Fatalf("%d active admins, %d audit entries", n, a)
	}
}

// A caller demoted while its change waits for the admin lock is refused once
// it gets the lock, and its change is not made.
func TestDemotedCallerIsRefused(t *testing.T) {
	e := setup(t, apptest.Options{})
	b, _, bCookie := account(t, e, "admin")
	c, _, _ := account(t, e, "user")
	hold, err := e.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hold.Exec(ctx, "update users set role = 'user' where id = $1", b); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- e.DoAs(bCookie, "PATCH", "/api/admin/users/"+c, `{"disabled":true}`) }()
	waitBlocked(t, e, 1)
	if err := hold.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if w := <-done; w.Code != 403 || apptest.ErrorCode(t, w) != "forbidden" {
		t.Fatalf("demoted caller: %d %s", w.Code, w.Body)
	}
	if scalar[bool](t, e, "select disabled_at is not null from users where id = $1", c) || len(audits(t, e)) != 0 {
		t.Fatal("the refused change must not be made or audited")
	}
}

func TestPasswordAndSessions(t *testing.T) {
	e := setup(t, apptest.Options{})
	target, email, cookie := account(t, e, "user")
	post := func(path, body string) (*httptest.ResponseRecorder, userResp) {
		t.Helper()
		w := e.Do("POST", "/api/admin/users/"+path, body)
		return w, apptest.JSON[userResp](t, w)
	}

	const pw = "a-new-password-123"
	w, res := post(target+"/password", `{"password":"`+pw+`"}`)
	if w.Code != 200 || strings.Contains(w.Body.String(), pw) || res.Password != nil || res.User.Sessions != 0 || me(e, cookie) != 401 {
		t.Fatalf("set password: %d %s", w.Code, w.Body)
	}
	if login(e, email, pw).Code != 200 {
		t.Fatal("login with the new password")
	}
	if a := audits(t, e)[0]; a.Action != "user.password" || !reflect.DeepEqual(detail(t, a), map[string]any{"generated": false, "sessionsRevoked": 1.0}) {
		t.Fatalf("password audit: %+v %s", a, a.Detail)
	}
	exec(t, e, "update users set disabled_at = now() where id = $1", target)
	w, res = post(target+"/password", `{}`)
	if w.Code != 200 || res.Password == nil || len(*res.Password) != 26 || res.User.Status != "disabled" {
		t.Fatalf("generated password: %d %s", w.Code, w.Body)
	}
	if a := audits(t, e)[0]; !reflect.DeepEqual(detail(t, a), map[string]any{"generated": true, "sessionsRevoked": 1.0}) {
		t.Fatalf("generated audit: %s", a.Detail)
	}

	// Your own password: this session stays, the others go.
	other := e.Session(e.User)
	if w, _ := post(e.User+"/password", `{}`); w.Code != 200 || me(e, e.Cookie) != 200 || me(e, other) != 401 {
		t.Fatalf("own password: %d, current %d, other %d", w.Code, me(e, e.Cookie), me(e, other))
	}

	e.Managed = []string{email}
	n := len(audits(t, e))
	if w, _ := post(target+"/password", `{}`); w.Code != 409 || !strings.Contains(w.Body.String(), "server config") || len(audits(t, e)) != n {
		t.Fatalf("managed: %d %s", w.Code, w.Body)
	}
	for _, body := range []string{`{"password":"short"}`, `{"password":5}`, `[`} {
		if w, _ := post(target+"/password", body); w.Code != 400 {
			t.Errorf("%s: %d", body, w.Code)
		}
	}
	if w, _ := post("00000000-0000-4000-8000-000000000000/password", `{}`); w.Code != 404 {
		t.Errorf("unknown user: %d", w.Code)
	}

	// Revoke: all of the target's sessions, audited even when there were none.
	e.Session(target)
	e.Session(target)
	if w, res := post(target+"/sessions/revoke", `{}`); w.Code != 200 || *res.Revoked != 2 || res.User.Sessions != 0 {
		t.Fatalf("revoke: %d %s", w.Code, w.Body)
	}
	if w, res := post(target+"/sessions/revoke", `{}`); w.Code != 200 || *res.Revoked != 0 ||
		audits(t, e)[0].Action != "user.sessions_revoke" || !reflect.DeepEqual(detail(t, audits(t, e)[0]), map[string]any{"sessionsRevoked": 0.0}) {
		t.Fatalf("revoke again: %d %s %s", w.Code, w.Body, audits(t, e)[0].Detail)
	}
	if w, _ := post(e.User+"/sessions/revoke", `{}`); w.Code != 200 || me(e, e.Cookie) != 200 {
		t.Fatal("revoking your own sessions keeps this one")
	}
}

func TestResets(t *testing.T) {
	e := setup(t, apptest.Options{})
	target, _, _ := account(t, e, "user")
	if _, _, err := e.Paper.Order(ctx, target, paper.OrderRequest{Symbol: "BTC", Side: "buy", Notional: new(1000.0)}); err != nil {
		t.Fatal(err)
	}
	w := e.Do("POST", "/api/admin/users/"+target+"/reset-portfolio", `{}`)
	res := apptest.JSON[struct {
		User      User
		Portfolio *paper.Portfolio
	}](t, w)
	if w.Code != 200 || res.Portfolio == nil || res.Portfolio.Cash != 100000 || len(res.Portfolio.Positions) != 0 ||
		*res.User.Equity != 100000 || scalar[int](t, e, "select count(*) from trades where user_id = $1", target) != 0 {
		t.Fatalf("reset portfolio: %d %s", w.Code, w.Body)
	}
	if a := audits(t, e)[0]; a.Action != "user.portfolio_reset" || string(a.Detail) != "{}" || a.Target.ID != target {
		t.Fatalf("audit: %+v", a)
	}

	if _, err := e.Usage.Spend(ctx, target, 30); err != nil {
		t.Fatal(err)
	}
	for _, before := range []float64{30, 0} {
		w := e.Do("POST", "/api/admin/users/"+target+"/reset-credits", `{}`)
		if u := apptest.JSON[userResp](t, w).User; w.Code != 200 || u.CreditsToday != 0 ||
			!reflect.DeepEqual(detail(t, audits(t, e)[0]), map[string]any{"creditsBefore": before}) {
			t.Fatalf("reset credits (before %v): %d %s", before, w.Code, w.Body)
		}
	}
	for _, path := range []string{"/reset-portfolio", "/reset-credits", "/sessions/revoke"} {
		if w := e.Do("POST", "/api/admin/users/00000000-0000-4000-8000-000000000000"+path, `{}`); w.Code != 404 {
			t.Errorf("%s on an unknown user: %d", path, w.Code)
		}
	}
}

// waiter waits until a backend is blocked on pid's locks and returns its pid.
func waiter(t *testing.T, e *apptest.Env, pid int) int {
	t.Helper()
	for range 500 {
		if w := scalar[int](t, e, "select coalesce(max(pid), 0) from pg_stat_activity where $1 = any(pg_blocking_pids(pid))", pid); w != 0 {
			return w
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("nothing ever waited on backend %d", pid)
	return 0
}

// A paper order holds the account row when the admin's reset or delete
// arrives, then inserts its trade (an FK check on the user row). Both must
// finish: the admin side must not hold a lock that blocks the FK check.
func TestAdminActionRacingAnOrder(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		want         int
	}{{"POST", "/reset-portfolio", 200}, {"DELETE", "", 204}} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			e := setup(t, apptest.Options{})
			target, _, _ := account(t, e, "user")
			if _, _, err := e.Paper.Order(ctx, target, paper.OrderRequest{Symbol: "BTC", Side: "buy", Notional: new(1000.0)}); err != nil {
				t.Fatal(err)
			}
			// Hold the position row so the next order stops after locking the account.
			hold, err := e.DB.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer hold.Rollback(ctx)
			var holdPID int
			if err := hold.QueryRow(ctx, "select pg_backend_pid()").Scan(&holdPID); err != nil {
				t.Fatal(err)
			}
			if _, err := hold.Exec(ctx, "select 1 from positions where user_id = $1 for update", target); err != nil {
				t.Fatal(err)
			}
			order := make(chan error, 1)
			go func() {
				_, _, err := e.Paper.Order(ctx, target, paper.OrderRequest{Symbol: "BTC", Side: "buy", Notional: new(500.0)})
				order <- err
			}()
			orderPID := waiter(t, e, holdPID)
			admin := make(chan *httptest.ResponseRecorder, 1)
			go func() { admin <- e.Do(tc.method, "/api/admin/users/"+target+tc.path, `{}`) }()
			waiter(t, e, orderPID)
			if err := hold.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			// After a delete the order's trade is in, but reading the portfolio back may fail: the user is gone.
			w, err := <-admin, <-order
			if w.Code != tc.want || strings.Contains(fmt.Sprint(err), "40P01") || (tc.want == 200 && err != nil) {
				t.Fatalf("admin %d %s; order: %v", w.Code, w.Body, err)
			}
		})
	}
}

// The per-user limit set in the console is what usage.Spend enforces.
func TestCreditLimitEnforced(t *testing.T) {
	e := setup(t, apptest.Options{})
	target, _, cookie := account(t, e, "user")
	if w := e.Do("PATCH", "/api/admin/users/"+target, `{"creditLimit":3}`); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if _, err := e.Usage.Spend(ctx, target, 2); err != nil {
		t.Fatal(err)
	}
	_, err := e.Usage.Spend(ctx, target, 2)
	w := httptest.NewRecorder()
	app.WriteError(w, err)
	if !errors.Is(err, usage.ErrQuota) || w.Code != 429 {
		t.Fatalf("over the per-user limit: %v → %d", err, w.Code)
	}
	if w := e.DoAs(cookie, "GET", "/api/me", nil); !strings.Contains(w.Body.String(), `"used":2,"limit":3`) {
		t.Fatalf("me: %s", w.Body)
	}
}

// seed gives uid one of everything the overview counts, created ago before now.
func seed(t *testing.T, e *apptest.Env, uid, ago string) {
	t.Helper()
	exec(t, e, `with c as (insert into chats (user_id, title, created_at, updated_at) values ($1, 'Chat', now() - $2::interval, now() - $2::interval)
		returning id) insert into chat_messages (chat_id, role, content, created_at)
		select id, r, 'hi', now() - $2::interval from c, unnest(array['user', 'assistant']) r`, uid, ago)
	exec(t, e, `insert into forecasts (user_id, shape, question, outcomes, resolution_date, status, created_at)
		values ($1, 'binary', 'Will it?', '["Yes","No"]', '2030-01-01', 'completed', now() - $2::interval)`, uid, ago)
	exec(t, e, `insert into strategies (user_id, name, description, prompt, spec, metrics, equity, benchmark, trades, created_at)
		values ($1, 'Strat', '', '', '{}', '{}', '[]', '[]', '[]', now() - $2::interval)`, uid, ago)
	exec(t, e, `insert into ai_portfolios (user_id, name, thesis, prompt, holdings, backtest, created_at)
		values ($1, 'Basket', '', '', '[]', '{}', now() - $2::interval)`, uid, ago)
	exec(t, e, `insert into arena_runs (user_id, name, status, cadence, start_at, steps, universe, model, knowledge_cutoff, agents,
		benchmarks, credits, created_at) values ($1, 'Run', 'completed', '1d', now(), 30, '{BTC}', 'm', '2026-06-30', '[]', '{}', 15,
		now() - $2::interval)`, uid, ago)
	exec(t, e, `insert into trades (user_id, symbol, side, qty, price, notional, fee, source, created_at)
		values ($1, 'BTC', 'buy', 0.01, 100000, 1000, 1, 'manual', now() - $2::interval)`, uid, ago)
}

func TestOverview(t *testing.T) {
	var braveCalls atomic.Int32
	e := setup(t, apptest.Options{Brave: func(w http.ResponseWriter, r *http.Request) {
		braveCalls.Add(1)
		w.Write([]byte(`{"results":[]}`))
	}})
	today := time.Now().UTC().Truncate(24 * time.Hour)
	day := func(ago int) time.Time { return today.AddDate(0, 0, -ago) }
	u1 := storetest.User(t, e.DB)
	exec(t, e, "update users set created_at = now() - interval '40 days', last_active_at = now() - interval '2 days' where id = $1", u1)
	u2 := storetest.User(t, e.DB)
	exec(t, e, "update users set created_at = now() - interval '3 days', last_active_at = now() - interval '10 days', disabled_at = now() where id = $1", u2)
	u3 := storetest.User(t, e.DB)
	exec(t, e, "update users set role = 'admin' where id = $1", u3)
	for _, u := range []struct {
		id      string
		ago, cr int
	}{{e.User, 0, 5}, {u1, 0, 3}, {u1, 2, 10}, {u2, 10, 7}, {u2, 40, 100}} {
		exec(t, e, "insert into ai_usage (user_id, day, credits) values ($1, $2, $3)", u.id, day(u.ago), u.cr)
	}
	seed(t, e, u1, "0 seconds")
	seed(t, e, u1, "3 days")
	seed(t, e, u2, "40 days")
	exec(t, e, `insert into forecasts (user_id, shape, question, outcomes, resolution_date, status)
		values ($1, 'binary', 'Running?', '["Yes","No"]', '2030-01-01', 'running')`, u3)
	exec(t, e, `insert into arena_runs (user_id, name, status, cadence, start_at, steps, universe, model, knowledge_cutoff, agents,
		benchmarks, credits) values ($1, 'Live', 'running', '1d', now(), 30, '{BTC}', 'm', '2026-06-30', '[]', '{}', 15)`, u3)
	exec(t, e, "insert into accounts (user_id, cash) values ($1, 90000), ($2, 100000)", u1, e.User)
	exec(t, e, "insert into live_desk (id, universe, start_marks) values (1, '{BTC}', '{\"BTC\": 100000}')")
	exec(t, e, `insert into live_agents (persona_id, name, benched_until) values ('momentum', 'Trend Rider', null), ('contrarian', 'Mean Reverter', now() + interval '1 day')`)
	exec(t, e, `insert into copy_plans (user_id, agent_id, ratio, max_usd, status, created_at)
		select $1, id, 0.5, 1000, s, now() - d from live_agents, unnest(array['active', 'stopped'], array['0 seconds', '3 days']::interval[]) as x(s, d)
		where persona_id = 'momentum'`, u1)
	exec(t, e, "insert into positions (user_id, symbol, qty, avg_price) values ($1, 'BTC', 0.5, 1), ($1, 'ZZZ', 10, 2)", u1) // ZZZ at its average price
	exec(t, e, `insert into news_items (url, title, source, fetched_at) values ('https://a.test/1', 'A', 'a', now() - interval '1 hour'),
		('https://a.test/2', 'B', 'b', now() - interval '3 hours')`)
	exec(t, e, "insert into market_briefs (brief, created_at) values ('{}', now() - interval '90 minutes')")
	if _, err := e.Research.SearchNews(ctx, "bitcoin", "", 5); err != nil || braveCalls.Load() != 1 {
		t.Fatalf("brave: %v, %d calls", err, braveCalls.Load())
	}

	w := e.Do("GET", "/api/admin/overview", nil)
	if w.Code != 200 {
		t.Fatalf("overview: %d %s", w.Code, w.Body)
	}
	o := apptest.JSON[struct{ Overview Overview }](t, w).Overview
	if u := o.Users; u.Total != 4 || u.Admins != 2 || u.Disabled != 1 || u.New30d != 3 || u.Active != (Windowed{1, 2, 3}) {
		t.Errorf("users: %+v", u)
	}
	c := o.Credits
	if c.Windowed != (Windowed{8, 18, 25}) || c.DefaultLimit != 500 || len(c.Daily) != 30 || c.Daily[29] != (DayValue{day(0).Format(time.DateOnly), 8}) ||
		c.Daily[27].Value != 10 || c.Daily[19].Value != 7 || c.Daily[0].Day != day(29).Format(time.DateOnly) {
		t.Errorf("credits: %+v", c)
	}
	var keys []string
	for _, f := range o.Features {
		keys = append(keys, f.Key)
		want := Windowed{1, 2, 2}
		if f.Key == "forecasts" || f.Key == "arena_runs" {
			want = Windowed{2, 3, 3} // + the running one
		}
		if f.Windowed != want || len(f.Daily) != 30 || f.Daily[26].Value != 1 || f.Label == "" {
			t.Errorf("feature %s: %+v", f.Key, f)
		}
	}
	if strings.Join(keys, " ") != "chat_messages forecasts strategies ai_portfolios arena_runs orders copy_plans" || o.Features[5].Label != "Paper orders" ||
		o.Features[6].Label != "Copy plans" {
		t.Errorf("features: %v", keys)
	}
	if len(o.TopUsers) != 2 || o.TopUsers[0].ID != u1 || o.TopUsers[0].Credits != 13 || o.TopUsers[1].ID != e.User || o.TopUsers[1].Credits != 5 {
		t.Errorf("top users: %+v", o.TopUsers)
	}
	equity := 190000 + 0.5*markettest.Price("BTCUSDT") + 20
	if p := o.Paper; p.Accounts != 2 || p.OpenPositions != 2 || p.Equity == nil || *p.Equity != equity || *p.TotalPnl != equity-200000 ||
		p.Notional30d != 2000 {
		t.Errorf("paper: %+v", p)
	}
	s := o.System
	if s.Version != "0123456789ab" || s.UptimeSec < 3599 || !s.DB.OK || s.DB.LatencyMs <= 0 || s.Model == "" || s.KnowledgeCutoff != "2026-06-30" ||
		!s.News.Enabled || s.News.Items != 2 || s.News.LastIngestAt == nil || time.Since(*s.News.LastIngestAt) > 61*time.Minute ||
		s.News.BriefAgeMin == nil || *s.News.BriefAgeMin < 89 || *s.News.BriefAgeMin > 91 || s.Brave.Used != 1 || s.Brave.Limit != 5000 ||
		s.Crawler.Configured || s.Crawler.Reachable != nil || s.Jobs.ForecastsRunning != 1 || s.Jobs.ArenaRunsRunning != 1 ||
		s.Signals.BoardAt != nil || s.Signals.Failures != 0 || s.Signals.LastErrorAt != nil {
		t.Errorf("system: %+v", s)
	}
	// The signals board's health: its newest board and the refresh failures in a row.
	if _, err := e.DB.Exec(context.Background(), `insert into signal_boards (board, created_at) values ('{}', now() - interval '2 hours');
		insert into signals_refresh (failures, last_error, last_error_at) values (3, 'x', now() - interval '5 minutes')`); err != nil {
		t.Fatal(err)
	}
	sg := apptest.JSON[struct{ Overview Overview }](t, e.Do("GET", "/api/admin/overview", nil)).Overview.System.Signals
	if sg.BoardAt == nil || time.Since(*sg.BoardAt) < 119*time.Minute || sg.Failures != 3 || sg.LastErrorAt == nil || time.Since(*sg.LastErrorAt) > 6*time.Minute {
		t.Errorf("signals: %+v", sg)
	}
	// The live desk: two agents (one benched) with an active and a stopped plan; the kill switch is read from Deps.
	if la := s.Jobs.LiveArena; la.Enabled || la.StartedAt == nil || la.LastStepAt != nil || la.Agents != 2 || la.Benched != 1 || la.CopyPlans != 1 ||
		la.NextStepAt != nil {
		t.Errorf("live arena: %+v", la)
	}
	e.LiveArena = true
	o = apptest.JSON[struct{ Overview Overview }](t, e.Do("GET", "/api/admin/overview", nil)).Overview
	if la := o.System.Jobs.LiveArena; !la.Enabled || la.NextStepAt == nil || la.NextStepAt.Unix()%14400 != 0 || !la.NextStepAt.After(o.AsOf) {
		t.Errorf("live arena on: %+v", la)
	}
	if time.Since(o.AsOf) > time.Minute || o.AsOf.Location() != time.UTC {
		t.Errorf("asOf %v", o.AsOf)
	}
}

func TestUserDetail(t *testing.T) {
	e := setup(t, apptest.Options{})
	target, email, _ := account(t, e, "user")
	seed(t, e, target, "0 seconds")
	seed(t, e, target, "3 days")
	// A $1,000 open stake moves cash into the stake: equity stays 100,000. A settled one is no longer equity.
	f := scalar[string](t, e, "select id::text from forecasts where user_id = $1 limit 1", target)
	if _, err := e.Paper.Stake(ctx, target, 1000, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into market_positions (user_id, forecast_id, outcome, side, question, url, price, contracts, cost)
			values ($1, $2, 'Yes', 'yes', 'Q?', 'https://polymarket.com/event/q', 0.5, 2000, 1000)`, target, f)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	exec(t, e, `insert into market_positions (user_id, forecast_id, outcome, side, question, url, price, contracts, cost, status, payout, settled_at)
		values ($1, $2, 'Yes', 'yes', 'Q?', 'https://polymarket.com/event/q', 0.5, 600, 300, 'lost', 0, now())`, target, f)
	today := time.Now().UTC().Truncate(24 * time.Hour)
	exec(t, e, "insert into ai_usage (user_id, day, credits) values ($1, $2, 4), ($1, $3, 6)", target, today, today.AddDate(0, 0, -2))
	if w := e.Do("PATCH", "/api/admin/users/"+target, `{"name":"Detail"}`); w.Code != 200 {
		t.Fatal(w.Code)
	}
	w := e.Do("GET", "/api/admin/users/"+target, nil)
	d := apptest.JSON[Detail](t, w)
	if w.Code != 200 || d.User.ID != target || d.User.Name != "Detail" || d.User.CreditsToday != 4 || d.DefaultCreditLimit != 500 ||
		len(d.Usage) != 30 || d.Usage[29].Value != 4 || d.Usage[27].Value != 6 ||
		d.Counts != (Counts{Chats: 2, ChatMessages: 2, Forecasts: 2, Strategies: 2, AIPortfolios: 2, ArenaRuns: 2, Trades: 2}) ||
		d.Portfolio == nil || d.Portfolio.Cash != 99000 || d.Portfolio.Equity != 100000 || d.User.Equity == nil || *d.User.Equity != d.Portfolio.Equity ||
		len(d.Trades) != 2 || !d.Trades[0].CreatedAt.After(d.Trades[1].CreatedAt) {
		t.Fatalf("detail: %d %s", w.Code, w.Body)
	}
	for kind, items := range d.Activity {
		if len(items) != 2 || !items[0].At.After(items[1].At) || items[0].Title == "" {
			t.Errorf("activity %s: %+v", kind, items)
		}
		if hasStatus := kind == "forecasts" || kind == "arenaRuns"; hasStatus != (items[0].Status != nil) ||
			(hasStatus && (*items[0].Status != "completed" || *items[1].Status != "completed")) {
			t.Errorf("activity %s status: %+v", kind, items)
		}
	}
	if len(d.Activity) != 5 || len(d.Audit) != 1 || d.Audit[0].Action != "user.update" || d.Audit[0].Target.Email != email {
		t.Fatalf("activity/audit: %d kinds, %+v", len(d.Activity), d.Audit)
	}
	for _, id := range []string{"nope", "00000000-0000-4000-8000-000000000000", "00000000x0000x4000x8000x000000000000"} {
		if w := e.Do("GET", "/api/admin/users/"+id, nil); w.Code != 404 {
			t.Errorf("%s: %d", id, w.Code)
		}
	}
}

func TestDelete(t *testing.T) {
	e := setup(t, apptest.Options{})
	target, email, cookie := account(t, e, "admin")
	seed(t, e, target, "0 seconds")
	if _, err := e.Usage.Spend(ctx, target, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Paper.Portfolio(ctx, target); err != nil {
		t.Fatal(err)
	}
	if w := e.Do("DELETE", "/api/admin/users/"+e.User, `{}`); w.Code != 409 || !strings.Contains(w.Body.String(), msgSelf) {
		t.Fatalf("self delete: %d %s", w.Code, w.Body)
	}
	if w := e.Do("DELETE", "/api/admin/users/"+target, `{}`); w.Code != 204 || me(e, cookie) != 401 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	for _, table := range []string{"sessions", "accounts", "trades", "ai_usage", "chats", "forecasts", "strategies", "ai_portfolios", "arena_runs"} {
		if n := scalar[int](t, e, "select count(*) from "+table+" where user_id = $1", target); n != 0 {
			t.Errorf("%s: %d rows left", table, n)
		}
	}
	if n := scalar[int](t, e, "select count(*) from chat_messages"); n != 0 {
		t.Errorf("chat_messages: %d rows left", n)
	}
	got := audits(t, e)
	if len(got) != 1 || got[0].Action != "user.delete" || got[0].Target.ID != target || got[0].Target.Email != email ||
		!reflect.DeepEqual(detail(t, got[0]), map[string]any{"name": "Test", "role": "admin"}) {
		t.Fatalf("delete audit: %+v", got)
	}
	if w := e.Do("DELETE", "/api/admin/users/"+target, `{}`); w.Code != 404 {
		t.Fatalf("deleted twice: %d", w.Code)
	}
}

func TestAuditPaging(t *testing.T) {
	e := setup(t, apptest.Options{})
	target, _, _ := account(t, e, "user")
	for i := range 3 {
		if w := e.Do("PATCH", "/api/admin/users/"+target, fmt.Sprintf(`{"name":"N%d"}`, i)); w.Code != 200 {
			t.Fatal(w.Code)
		}
	}
	type page struct {
		Entries            []AuditEntry
		Total, Page, Limit int
	}
	w := e.Do("GET", "/api/admin/audit?limit=2&page=1", nil)
	p := apptest.JSON[page](t, w)
	if w.Code != 200 || p.Total != 3 || p.Page != 1 || p.Limit != 2 || len(p.Entries) != 2 ||
		!reflect.DeepEqual(detail(t, p.Entries[0])["name"], map[string]any{"from": "N1", "to": "N2"}) {
		t.Fatalf("page 1: %d %s", w.Code, w.Body)
	}
	if p := apptest.JSON[page](t, e.Do("GET", "/api/admin/audit?limit=2&page=9", nil)); p.Total != 3 || len(p.Entries) != 0 || p.Entries == nil {
		t.Fatalf("past the end: %+v", p)
	}
	if p := apptest.JSON[page](t, e.Do("GET", "/api/admin/audit", nil)); p.Limit != 50 || len(p.Entries) != 3 {
		t.Fatalf("default limit: %+v", p)
	}
	if w := e.Do("GET", "/api/admin/audit?limit=101", nil); w.Code != 400 {
		t.Fatalf("limit 101: %d", w.Code)
	}
}

// Without market data, equity and portfolio degrade to null; nothing fails.
func TestMarketDown(t *testing.T) {
	e := setup(t, apptest.Options{})
	e.Binance.Down("", true)
	target, _, _ := account(t, e, "user")
	w := e.Do("GET", "/api/admin/overview", nil)
	if o := apptest.JSON[struct{ Overview Overview }](t, w).Overview; w.Code != 200 || o.Paper.Equity != nil || o.Paper.TotalPnl != nil {
		t.Fatalf("overview: %d %s", w.Code, w.Body)
	}
	w = e.Do("GET", "/api/admin/users/"+target, nil)
	if d := apptest.JSON[Detail](t, w); w.Code != 200 || d.Portfolio != nil || d.User.Equity != nil {
		t.Fatalf("detail: %d %s", w.Code, w.Body)
	}
	w = e.Do("POST", "/api/admin/users/"+target+"/reset-portfolio", `{}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"portfolio":null`) {
		t.Fatalf("reset: %d %s", w.Code, w.Body)
	}
}

// The crawler probe: configured with a base and token, reachable on a 2xx
// /health within 3 s, cached for a minute.
func TestCrawlerStatus(t *testing.T) {
	e := setup(t, apptest.Options{})
	for _, tc := range []struct {
		status    int
		reachable bool
	}{{200, true}, {503, false}} {
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			if r.URL.Path != "/health" || r.Header.Get("Authorization") != "Bearer tok" {
				t.Errorf("probe %s %q", r.URL.Path, r.Header.Get("Authorization"))
			}
			w.WriteHeader(tc.status)
		}))
		e.Research = research.New(research.Config{CrawlBase: srv.URL, CrawlToken: "tok"}, e.DB, nil)
		for range 2 {
			o := apptest.JSON[struct{ Overview Overview }](t, e.Do("GET", "/api/admin/overview", nil)).Overview
			if c := o.System.Crawler; c.Base != srv.URL || !c.Configured || c.Reachable == nil || *c.Reachable != tc.reachable {
				t.Errorf("%d: %+v", tc.status, c)
			}
		}
		if hits.Load() != 1 {
			t.Errorf("%d: %d probes, want 1 (cached)", tc.status, hits.Load())
		}
		srv.Close()
	}
}

// Disable stops copy plans in its own transaction even while the job is off,
// preserving positions. Audit failure must roll back both state changes.
func TestDisableCopyPlansAtomicWhileOff(t *testing.T) {
	e := setup(t, apptest.Options{})
	target, _, _ := account(t, e, "user")
	var aid, pid string
	if err := e.DB.QueryRow(ctx, `insert into live_agents(persona_id,name) values('momentum','Trend Rider') returning id`).Scan(&aid); err != nil {
		t.Fatal(err)
	}
	if err := e.DB.QueryRow(ctx, `insert into copy_plans(user_id,agent_id,ratio,max_usd,positions) values($1,$2,.1,1000,'{"BTC":0.01}') returning id`, target, aid).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	exec(t, e, `insert into accounts(user_id) values($1)`, target)
	exec(t, e, `insert into positions(user_id,symbol,qty,avg_price) values($1,'BTC',.01,100000)`, target)
	exec(t, e, `create function reject_copy_audit() returns trigger language plpgsql as $$ begin raise exception 'audit rejected'; end $$;
 create trigger reject_audit before insert on admin_audit for each row execute function reject_copy_audit()`)
	if w := e.Do("PATCH", "/api/admin/users/"+target, `{"disabled":true}`); w.Code != 500 {
		t.Fatalf("expected rollback: %d %s", w.Code, w.Body)
	}
	if scalar[bool](t, e, "select disabled_at is not null from users where id=$1", target) || scalar[string](t, e, "select status from copy_plans where id=$1", pid) != "active" {
		t.Fatal("disable escaped rollback")
	}
	exec(t, e, "drop trigger reject_audit on admin_audit")
	if w := e.Do("PATCH", "/api/admin/users/"+target, `{"disabled":true}`); w.Code != 200 {
		t.Fatalf("disable: %d %s", w.Code, w.Body)
	}
	if got := scalar[string](t, e, "select status||':'||coalesce(stop_reason,'') from copy_plans where id=$1", pid); got != "stopped:account_disabled" {
		t.Fatalf("copy plan survived disabled account: %s", got)
	}
	if got := scalar[float64](t, e, "select qty from positions where user_id=$1 and symbol='BTC'", target); got != .01 {
		t.Fatalf("disable sold position: %v", got)
	}
	if d := detail(t, audits(t, e)[0]); d["copyPlansStopped"] != 1.0 {
		t.Fatalf("copy stop audit: %+v", d)
	}
}

func TestLiveArenaHealthCountsClaims(t *testing.T) {
	e := setup(t, apptest.Options{})
	e.LiveArena = true
	exec(t, e, `insert into live_desk(id,universe,start_marks,last_step_at,last_error) values(1,'{BTC}','{"BTC":100000}',now()-interval '5 hours','Quotes unavailable');
 insert into live_agents(persona_id,name) values('momentum','Trend Rider');
 insert into live_attempts(agent_id,bar_t,claimed_at) select id,1,now() from live_agents`)
	w := e.Do("GET", "/api/admin/overview", nil)
	v := apptest.JSON[struct {
		Overview struct {
			System struct {
				Jobs struct{ LiveArena map[string]any }
			}
		}
	}](t, w).Overview.System.Jobs.LiveArena
	if w.Code != 200 || v["attemptsToday"] != 1.0 || v["requestCeilingToday"] != 4.0 || v["maxRequestsPerDay"] != 72.0 || v["maxTokensPerRequest"] != 4096.0 || v["status"] != "outage" || v["stale"] != true {
		t.Fatalf("health does not disclose bounded claims: %d %+v", w.Code, v)
	}
}

func TestAdminDisableDeleteRacingCopyTransaction(t *testing.T) {
	for _, method := range []string{"PATCH", "DELETE"} {
		t.Run(method, func(t *testing.T) {
			e := setup(t, apptest.Options{})
			target, _, _ := account(t, e, "user")
			exec(t, e, "insert into accounts(user_id) values($1)", target)
			var aid, pid string
			if err := e.DB.QueryRow(ctx, "insert into live_agents(persona_id,name) values('momentum','Trend Rider') returning id").Scan(&aid); err != nil {
				t.Fatal(err)
			}
			if err := e.DB.QueryRow(ctx, "insert into copy_plans(user_id,agent_id,ratio,max_usd) values($1,$2,.5,1000) returning id", target, aid).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			tx, err := e.DB.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			var holder int
			if err := tx.QueryRow(ctx, "select pg_backend_pid()").Scan(&holder); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, "select 1 from users where id=$1 for no key update", target); err != nil {
				t.Fatal(err)
			}
			quotes, err := e.Market.Assets(ctx)
			if err != nil {
				t.Fatal(err)
			}
			ledger, err := e.Paper.LockTx(ctx, tx, target, quotes)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- e.Do(method, "/api/admin/users/"+target, `{"disabled":true}`) }()
			waiter(t, e, holder)
			// A real paper copy fill and its ownership save still commit before the admin action.
			trade, err := ledger.Order(ctx, paper.OrderRequest{Symbol: "BTC", Side: "buy", Notional: new(100.0), Source: "copy", RefID: &pid})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, "update copy_plans set positions=jsonb_build_object('BTC',$2::float8) where id=$1", pid, trade.Qty); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case w := <-done:
				want := 200
				if method == "DELETE" {
					want = 204
				}
				if w.Code != want {
					t.Fatalf("admin race: %d %s", w.Code, w.Body)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("admin action deadlocked with copy")
			}
			if method == "PATCH" {
				if got := scalar[string](t, e, "select status from copy_plans where id=$1", pid); got != "stopped" {
					t.Fatalf("active after disable: %s", got)
				}
			} else if scalar[int](t, e, "select count(*) from copy_plans where id=$1", pid) != 0 {
				t.Fatal("deleted user's plan survived")
			}
		})
	}
}

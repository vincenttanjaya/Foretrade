package aiconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vincenttanjaya/Foretrade/api/internal/config"
	"github.com/vincenttanjaya/Foretrade/api/internal/httpx"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm"
	"github.com/vincenttanjaya/Foretrade/api/internal/research"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
)

// Secrets the tests store; none may ever reach a response, a log line or an error.
const (
	apiKey    = "sk-test-SECRET-0123456789abcdef"
	braveKey  = "brave-SECRET-0123456789abcd"
	crawlTok  = "crawl-SECRET-0123456789abcd"
	envAPIKey = "sk-env-SECRET-0123456789wxyz"
)

var secrets = []string{apiKey, braveKey, crawlTok, envAPIKey, "access-SECRET", "refresh-SECRET"}

type env struct {
	*Service
	t      *testing.T
	pool   *pgxpool.Pool
	admin  string
	ai     *llm.Client
	rs     *research.Service
	mux    *http.ServeMux
	logs   *syncBuffer
	tested []llm.Settings // every provider test, in order
	fail   error          // the next provider tests' result
	mu     sync.Mutex
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// newEnv is a Service on a schema of its own with fake provider calls, an
// admin, and the default logger recorded.
func newEnv(t *testing.T, cfg config.Config) *env {
	t.Helper()
	pool := storetest.Schema(t)
	return newEnvOn(t, pool, cfg)
}

func newEnvOn(t *testing.T, pool *pgxpool.Pool, cfg config.Config) *env {
	t.Helper()
	admin := storetest.User(t, pool)
	if _, err := pool.Exec(context.Background(), "update users set role = 'admin' where id = $1", admin); err != nil {
		t.Fatal(err)
	}
	ai, err := llm.NewFromSettings(llm.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, pool: pool, admin: admin, ai: ai, rs: research.New(research.Config{BraveBase: cfg.BraveBase}, nil, nil), mux: http.NewServeMux(),
		logs: &syncBuffer{}}
	e.Service = New(pool, cfg, ai, e.rs)
	e.Service.test = func(_ context.Context, s llm.Settings) error {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.tested = append(e.tested, s)
		return e.fail
	}
	e.Service.models = func(_ context.Context, s llm.Settings) ([]llm.ModelInfo, error) {
		if s.Provider == "broken" {
			return nil, errors.New("401 invalid api key")
		}
		return []llm.ModelInfo{{ID: "m-" + s.Provider, Label: "Model"}}, nil
	}
	e.Register(e.mux)
	e.mux.HandleFunc("GET /auth/callback", e.Callback)
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() {
		slog.SetDefault(prev)
		e.clean(e.logs.String(), "log")
	})
	if err := e.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	return e
}

// clean fails the test when text holds any stored secret.
func (e *env) clean(text, where string) {
	e.t.Helper()
	for _, s := range secrets {
		if strings.Contains(text, s) {
			e.t.Errorf("%s holds a secret %q: %s", where, s, text)
		}
	}
}

// do serves one request as the env's admin from origin ("" sends none). Every
// response is checked for secrets.
func (e *env) do(method, path string, body any, origin string) *httptest.ResponseRecorder {
	e.t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(b))
	r = r.WithContext(httpx.WithUser(r.Context(), e.admin))
	r.Header.Set("Content-Type", "application/json")
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, r)
	e.clean(w.Body.String()+w.Header().Get("Location"), method+" "+path)
	return w
}

func (e *env) view(w *httptest.ResponseRecorder) View {
	e.t.Helper()
	var v View
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &v) != nil {
		e.t.Fatalf("%d %s", w.Code, w.Body)
	}
	return v
}

func (e *env) audits() []string {
	e.t.Helper()
	rows, err := e.pool.Query(context.Background(), "select action || ' ' || detail::text from admin_audit where admin_id = $1 order by created_at, id", e.admin)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for rows.Next() {
		var s string
		rows.Scan(&s)
		out = append(out, s)
	}
	return out
}

func (e *env) testedLen() int { e.mu.Lock(); defer e.mu.Unlock(); return len(e.tested) }
func (e *env) lastTested() llm.Settings {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.tested[len(e.tested)-1]
}

// The stored-settings lifecycle: a test failure saves nothing, a save tests
// then reconfigures the client, a key survives a model change but not a
// provider switch, and every change is audited by field name only.
func TestSaveTestsFirstAndKeepsKeyOnlyForTheSameProvider(t *testing.T) {
	e := newEnv(t, config.Config{SecretKey: keyA})
	v := e.view(e.do("GET", "/api/admin/settings", nil, ""))
	if v.AI.Source != "none" || v.AI.Status != "off" || v.AI.ReadOnly || !v.SecretKeySet || v.Providers == nil || e.ai.Configured() {
		t.Fatalf("fresh: %+v", v)
	}

	e.fail = errors.New("401 Incorrect API key provided")
	w := e.do("PUT", "/api/admin/settings", map[string]any{"ai": map[string]any{"provider": "openai", "model": "m1", "apiKey": apiKey}}, "")
	if w.Code != 400 || !strings.Contains(w.Body.String(), "401 Incorrect API key provided") {
		t.Fatalf("failing test: %d %s", w.Code, w.Body)
	}
	if got := e.lastTested(); got.Provider != "openai" || got.Model != "m1" || got.APIKey != apiKey {
		t.Fatalf("tested the draft: %+v", got)
	}
	if v := e.view(e.do("GET", "/api/admin/settings", nil, "")); v.AI.Provider != "" || v.AI.APIKey.Set || len(e.audits()) != 0 || e.ai.Configured() {
		t.Fatalf("a failed test saved: %+v %v", v.AI, e.audits())
	}

	e.fail = nil
	v = e.view(e.do("PUT", "/api/admin/settings", map[string]any{"ai": map[string]any{"provider": "openai", "model": "m1", "apiKey": apiKey,
		"knowledgeCutoff": "2026-08-01"}}, ""))
	if v.AI.Source != "db" || v.AI.Status != "ready" || v.AI.APIKey != (secretView{true, "…cdef"}) || v.AI.KnowledgeCutoff != "2026-08-01" {
		t.Fatalf("saved: %+v", v.AI)
	}
	if s := e.ai.Settings(); s.Provider != "openai" || s.Model != "m1" || s.APIKey != apiKey || e.ai.KnowledgeCutoff().Format("2006-01-02") != "2026-08-01" {
		t.Fatalf("client not reconfigured: %+v", s.Provider)
	}
	var sealed []byte
	e.pool.QueryRow(context.Background(), "select secret from ai_settings").Scan(&sealed)
	if len(sealed) == 0 || bytes.Contains(sealed, []byte(apiKey)) {
		t.Fatal("the key must be stored sealed")
	}

	n := e.testedLen()
	e.view(e.do("PUT", "/api/admin/settings", map[string]any{"ai": map[string]any{"model": "m2"}}, ""))
	if e.testedLen() != n+1 || e.lastTested().APIKey != apiKey || e.ai.Settings().APIKey != apiKey {
		t.Fatal("a model change keeps the stored key, and is tested with it")
	}
	n = e.testedLen()
	e.view(e.do("PUT", "/api/admin/settings", map[string]any{}, ""))
	e.view(e.do("PUT", "/api/admin/settings", map[string]any{"ai": map[string]any{"model": "m2"}}, ""))
	if e.testedLen() != n || len(e.audits()) != 2 {
		t.Fatalf("no change: no test, no audit: %v", e.audits())
	}

	v = e.view(e.do("PUT", "/api/admin/settings", map[string]any{"ai": map[string]any{"provider": "ollama", "baseUrl": "http://host.docker.internal:11434/v1"}}, ""))
	if v.AI.APIKey.Set || e.lastTested().APIKey != "" || e.ai.Settings().APIKey != "" {
		t.Fatal("switching provider without a key must clear it")
	}
	e.pool.QueryRow(context.Background(), "select secret from ai_settings").Scan(&sealed)
	if sealed != nil {
		t.Fatal("cleared key still stored")
	}

	n = e.testedLen()
	v = e.view(e.do("PUT", "/api/admin/settings", map[string]any{"ai": map[string]any{"provider": ""}}, ""))
	if v.AI.Status != "off" || v.AI.Source != "none" || e.ai.Configured() || e.testedLen() != n {
		t.Fatalf("off: %+v (tests %d → %d)", v.AI, n, e.testedLen())
	}
	want := []string{
		`settings.update {"changed": ["provider", "model", "knowledgeCutoff", "apiKey"]}`,
		`settings.update {"changed": ["model"]}`,
		`settings.update {"changed": ["provider", "baseUrl", "apiKey"]}`,
		`settings.update {"changed": ["provider"]}`,
	}
	if got := e.audits(); !slices.Equal(got, want) {
		t.Fatalf("audit %v", got)
	}

	for _, bad := range []map[string]any{{"ai": map[string]any{"effort": "max"}}, {"ai": map[string]any{"baseUrl": "nope"}}, {"ai": "x"}} {
		if w := e.do("PUT", "/api/admin/settings", bad, ""); w.Code != 400 {
			t.Errorf("%v: %d", bad, w.Code)
		}
	}
	if _, err := e.pool.Exec(context.Background(), "delete from users where id = $1", e.admin); err != nil {
		t.Fatal(err)
	}
	if w := e.do("PUT", "/api/admin/settings", map[string]any{"ai": map[string]any{"model": "m3"}}, ""); w.Code != 403 {
		t.Fatalf("a deleted admin's change: %d %s", w.Code, w.Body)
	}
}

// Without SECRET_KEY nothing secret can be saved (400, before any provider
// call), a keyless provider still can; a key that no longer opens (SECRET_KEY
// changed) reads as absent, is flagged, and is cleared on request.
func TestSecretKeyMissingOrChanged(t *testing.T) {
	pool := storetest.Schema(t)
	e := newEnvOn(t, pool, config.Config{})
	if v := e.view(e.do("GET", "/api/admin/settings", nil, "")); v.SecretKeySet {
		t.Fatal("secretKeySet with no SECRET_KEY")
	}
	for _, body := range []map[string]any{{"ai": map[string]any{"provider": "openai", "apiKey": apiKey}}, {"search": map[string]any{"braveApiKey": braveKey}}} {
		if w := e.do("PUT", "/api/admin/settings", body, ""); w.Code != 400 || !strings.Contains(w.Body.String(), "SECRET_KEY is not set") {
			t.Fatalf("%v: %d %s", body, w.Code, w.Body)
		}
	}
	if e.testedLen() != 0 {
		t.Fatal("tested a draft that can't be saved")
	}
	if v := e.view(e.do("PUT", "/api/admin/settings", map[string]any{"ai": map[string]any{"provider": "ollama", "model": "llama3"}}, "")); v.AI.Status != "ready" {
		t.Fatalf("keyless provider: %+v", v.AI)
	}

	a := newEnvOn(t, pool, config.Config{SecretKey: keyA})
	a.view(a.do("PUT", "/api/admin/settings", map[string]any{"ai": map[string]any{"provider": "openai", "apiKey": apiKey},
		"search": map[string]any{"braveApiKey": braveKey}}, ""))

	b := newEnvOn(t, pool, config.Config{SecretKey: keyB}) // Load: logged by name, not fatal
	if !strings.Contains(b.logs.String(), "secret=apiKey") || !strings.Contains(b.logs.String(), "secret=braveApiKey") {
		t.Fatalf("boot log: %s", b.logs)
	}
	v := b.view(b.do("GET", "/api/admin/settings", nil, ""))
	if v.AI.Status != "error" || !strings.Contains(v.AI.Error, "API key can't be decrypted") || v.AI.APIKey.Set ||
		v.Search.BraveAPIKey.Set || !strings.Contains(v.Search.Error, "search key can't be decrypted") || b.ai.Settings().APIKey != "" || b.rs.Enabled() {
		t.Fatalf("undecryptable: %+v %+v", v.AI, v.Search)
	}
	v = b.view(b.do("PUT", "/api/admin/settings", map[string]any{"ai": map[string]any{"clearKey": true}, "search": map[string]any{"clearBraveApiKey": true}}, ""))
	if strings.Contains(v.AI.Error, "decrypted") || !strings.Contains(v.AI.Error, "needs an API key") || v.Search.Error != "" {
		t.Fatalf("cleared: %+v %+v", v.AI, v.Search)
	}
}

// AI_PROVIDER set: the environment rules AI (read-only, 409 on a change, Test
// still works over it), search keys stay editable, and the stored row is ignored.
func TestEnvModeIsReadOnly(t *testing.T) {
	e := newEnv(t, config.Config{SecretKey: keyA, AI: llm.Settings{Provider: "anthropic", Model: "env-model", APIKey: envAPIKey}})
	if s := e.ai.Settings(); s.Provider != "anthropic" || s.APIKey != envAPIKey {
		t.Fatalf("boot did not configure the env settings: %q", s.Provider)
	}
	v := e.view(e.do("GET", "/api/admin/settings", nil, ""))
	if v.AI.Source != "env" || !v.AI.ReadOnly || v.AI.Provider != "anthropic" || v.AI.APIKey != (secretView{true, "…wxyz"}) || v.AI.Status != "ready" {
		t.Fatalf("env view: %+v", v.AI)
	}
	if w := e.do("PUT", "/api/admin/settings", map[string]any{"ai": map[string]any{"model": "other"}}, ""); w.Code != 409 || !strings.Contains(w.Body.String(), "AI_PROVIDER") {
		t.Fatalf("env change: %d %s", w.Code, w.Body)
	}
	if v := e.view(e.do("PUT", "/api/admin/settings", map[string]any{"search": map[string]any{"braveApiKey": braveKey}}, "")); v.Search.BraveAPIKey.Source != "db" {
		t.Fatalf("search in env mode: %+v", v.Search)
	}
	var res struct {
		OK       bool   `json:"ok"`
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}
	w := e.do("POST", "/api/admin/settings/test", map[string]any{"ai": map[string]any{"model": "draft-model"}}, "")
	if json.Unmarshal(w.Body.Bytes(), &res); !res.OK || res.Provider != "anthropic" || res.Model != "draft-model" || e.lastTested().APIKey != envAPIKey {
		t.Fatalf("test over env: %s", w.Body)
	}
	if e.ai.Settings().Model != "env-model" {
		t.Fatal("a Test must not change the settings in force")
	}
}

// POST …/test always answers 200; POST …/models lists the draft provider's
// models or returns its error as a 400.
func TestTestAndModelsEndpoints(t *testing.T) {
	e := newEnv(t, config.Config{SecretKey: keyA})
	e.view(e.do("PUT", "/api/admin/settings", map[string]any{"ai": map[string]any{"provider": "openrouter", "apiKey": apiKey}}, ""))
	type result struct {
		OK        bool   `json:"ok"`
		Error     string `json:"error"`
		LatencyMs *int64 `json:"latencyMs"`
	}
	check := func(body any, ok bool, msg string) {
		t.Helper()
		var r result
		w := e.do("POST", "/api/admin/settings/test", body, "")
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &r) != nil || r.OK != ok || !strings.Contains(r.Error, msg) || ok != (r.LatencyMs != nil) {
			t.Fatalf("%v: %d %s", body, w.Code, w.Body)
		}
	}
	check(map[string]any{"ai": map[string]any{"model": "x"}}, true, "")
	if s := e.lastTested(); s.Provider != "openrouter" || s.Model != "x" || s.APIKey != apiKey {
		t.Fatalf("draft over current: %+v", s.Provider)
	}
	check(map[string]any{"ai": map[string]any{"provider": ""}}, false, "Choose a provider")
	check(map[string]any{"ai": map[string]any{"effort": "max"}}, false, "effort must be")
	e.fail = errors.New("model not found")
	check(map[string]any{}, false, "model not found")

	w := e.do("POST", "/api/admin/settings/models", map[string]any{"ai": map[string]any{"provider": "groq"}}, "")
	if w.Code != 200 || w.Body.String() != `{"models":[{"id":"m-groq","label":"Model"}]}`+"\n" {
		t.Fatalf("models: %d %s", w.Code, w.Body)
	}
	if w := e.do("POST", "/api/admin/settings/models", map[string]any{"ai": map[string]any{"provider": "broken"}}, ""); w.Code != 400 || !strings.Contains(w.Body.String(), "invalid api key") {
		t.Fatalf("models error: %d %s", w.Code, w.Body)
	}
}

// Search keys: the row's where set, else the environment's, applied to the
// research service at boot and on every save; the env crawl token never goes
// to a crawl base set in the console.
func TestSearchKeysApplyAtRuntime(t *testing.T) {
	var mu sync.Mutex
	var tokens []string
	brave := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		tokens = append(tokens, r.Header.Get("X-Subscription-Token"))
		mu.Unlock()
		w.Write([]byte(`{"web":{"results":[]}}`))
	}))
	defer brave.Close()
	crawl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer crawl.Close()
	e := newEnv(t, config.Config{SecretKey: keyA, BraveAPIKey: "env-brave-0123456789", BraveBase: brave.URL, CrawlBase: crawl.URL, CrawlToken: "env-crawl-token"})
	last := func() string {
		t.Helper()
		e.rs.SearchWeb(context.Background(), "q"+strings.Repeat("x", len(tokens)), 3) // a new query each time: no cache
		mu.Lock()
		defer mu.Unlock()
		return tokens[len(tokens)-1]
	}
	if last() != "env-brave-0123456789" {
		t.Fatal("env Brave key not applied at boot")
	}
	v := e.view(e.do("GET", "/api/admin/settings", nil, ""))
	if v.Search.BraveAPIKey != (sourcedSecret{secretView{true, "…6789"}, "env"}) || v.Search.CrawlBaseSource != "env" || v.Search.CrawlToken.Source != "env" {
		t.Fatalf("env search view: %+v", v.Search)
	}
	if st := e.rs.Crawler(context.Background()); st.Base != crawl.URL || !st.Configured {
		t.Fatalf("env crawler: %+v", st)
	}

	v = e.view(e.do("PUT", "/api/admin/settings", map[string]any{"search": map[string]any{"braveApiKey": braveKey}}, ""))
	if last() != braveKey || v.Search.BraveAPIKey != (sourcedSecret{secretView{true, "…abcd"}, "db"}) {
		t.Fatalf("saved Brave key not applied: %+v", v.Search)
	}
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer other.Close()
	v = e.view(e.do("PUT", "/api/admin/settings", map[string]any{"search": map[string]any{"crawlBase": other.URL}}, ""))
	if st := e.rs.Crawler(context.Background()); st.Base != other.URL || st.Configured || v.Search.CrawlToken.Set {
		t.Fatalf("the env crawl token followed a console base: %+v %+v", st, v.Search)
	}
	e.view(e.do("PUT", "/api/admin/settings", map[string]any{"search": map[string]any{"crawlToken": crawlTok}}, ""))
	if st := e.rs.Crawler(context.Background()); !st.Configured {
		t.Fatalf("console crawl token: %+v", st)
	}
	v = e.view(e.do("PUT", "/api/admin/settings", map[string]any{"search": map[string]any{"clearBraveApiKey": true, "crawlBase": ""}}, ""))
	if last() != "env-brave-0123456789" || v.Search.BraveAPIKey.Source != "env" || v.Search.CrawlBase != crawl.URL || v.Search.CrawlToken.Source != "env" {
		t.Fatalf("cleared: back to env: %+v", v.Search)
	}
	if got := e.audits(); len(got) != 4 || got[3] != `settings.update {"changed": ["braveApiKey", "crawlBase", "crawlToken"]}` {
		t.Fatalf("audit %v", got)
	}
}

// A deleted settings row (an operator's reset) is recreated with the
// defaults: boot and every endpoint keep working, and a save sticks.
func TestMissingRowIsRecreated(t *testing.T) {
	e := newEnv(t, config.Config{SecretKey: keyA})
	if _, err := e.pool.Exec(context.Background(), "delete from ai_settings"); err != nil {
		t.Fatal(err)
	}
	if err := e.Load(context.Background()); err != nil {
		t.Fatalf("boot: %v", err)
	}
	if v := e.view(e.do("GET", "/api/admin/settings", nil, "")); v.AI.Status != "off" {
		t.Fatalf("reset: %+v", v.AI)
	}
	e.pool.Exec(context.Background(), "delete from ai_settings")
	if v := e.view(e.do("PUT", "/api/admin/settings", map[string]any{"ai": map[string]any{"provider": "ollama"}}, "")); v.AI.Provider != "ollama" {
		t.Fatalf("save after a reset: %+v", v.AI)
	}
}

package aiconfig

import (
	"cmp"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/config"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm"
)

const consoleOrigin = "http://localhost:8081"

// fakeAuth is auth.openai.com: the token endpoint (code and refresh grants),
// the JWKS and revocation, recording each form it receives.
type fakeAuth struct {
	*httptest.Server
	key    *rsa.PrivateKey // the JWKS's
	signer *rsa.PrivateKey // signs id_tokens; nil → key
	mu     sync.Mutex
	forms  map[string][]url.Values // by path
	issued int                     // token sets issued: access-SECRET-<n>
	token  func(form url.Values) (int, any)
}

var testKey = func() *rsa.PrivateKey {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return k
}()

func newFakeAuth(t *testing.T) *fakeAuth {
	f := &fakeAuth{key: testKey, forms: map[string][]url.Values{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/jwks.json" {
			b64 := base64.RawURLEncoding.EncodeToString
			json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "k1", "alg": "RS256",
				"n": b64(f.key.N.Bytes()), "e": b64(big.NewInt(int64(f.key.E)).Bytes())}}})
			return
		}
		r.ParseForm()
		f.mu.Lock()
		f.forms[r.URL.Path] = append(f.forms[r.URL.Path], r.PostForm)
		f.mu.Unlock()
		if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Errorf("%s: content type %q", r.URL.Path, r.Header.Get("Content-Type"))
		}
		switch r.URL.Path {
		case "/api/accounts/oauth/revoke":
		case "/api/accounts/oauth/token":
			status, body := f.token(r.PostForm)
			w.WriteHeader(status)
			json.NewEncoder(w).Encode(body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeAuth) sent(path string) []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.forms[path]
}

// idToken signs claims with key (RS256, kid k1).
func idToken(key *rsa.PrivateKey, claims map[string]any) string {
	b64 := func(v any) string { b, _ := json.Marshal(v); return base64.RawURLEncoding.EncodeToString(b) }
	signed := b64(map[string]string{"alg": "RS256", "kid": "k1", "typ": "JWT"}) + "." + b64(claims)
	sum := sha256.Sum256([]byte(signed))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	return signed + "." + base64.RawURLEncoding.EncodeToString(sig)
}

type signIn struct {
	*env
	auth *fakeAuth
}

func newSignIn(t *testing.T, cfg config.Config) *signIn {
	cfg.SecretKey = keyA
	e := newEnv(t, cfg)
	f := newFakeAuth(t)
	e.authBase = f.URL
	return &signIn{e, f}
}

// start asks for the authorize URL as the console at consoleOrigin.
func (s *signIn) start() url.Values {
	s.t.Helper()
	w := s.do("POST", "/api/admin/settings/chatgpt/start", map[string]any{}, consoleOrigin)
	var out struct{ URL string }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		s.t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	u, err := url.Parse(out.URL)
	if err != nil || u.Scheme+"://"+u.Host != s.auth.URL || u.Path != "/api/accounts/authorize" {
		s.t.Fatalf("authorize URL %q", out.URL)
	}
	return u.Query()
}

// callback is the browser's return to /auth/callback; it reports the redirect.
func (s *signIn) callback(q url.Values) *url.URL {
	s.t.Helper()
	w := s.do("GET", "/auth/callback?"+q.Encode(), nil, "")
	if w.Code != http.StatusFound {
		s.t.Fatalf("callback: %d %s", w.Code, w.Body)
	}
	u, _ := url.Parse(w.Header().Get("Location"))
	return u
}

// grant answers code and refresh grants with fresh tokens; claims tweak the id_token.
func (s *signIn) grant(nonce string, tweak func(claims, reply map[string]any)) {
	s.auth.token = func(form url.Values) (int, any) {
		s.auth.mu.Lock()
		s.auth.issued++
		n := s.auth.issued
		s.auth.mu.Unlock()
		claims := map[string]any{"iss": s.auth.URL, "aud": []string{"oaiapp_1"}, "exp": time.Now().Add(time.Hour).Unix(), "nonce": nonce,
			"sub": "user-1", "email": "owner@example.com"}
		reply := map[string]any{"access_token": "access-SECRET-" + string(rune('0'+n)), "refresh_token": "refresh-SECRET-" + string(rune('0'+n)),
			"token_type": "Bearer", "expires_in": 3600, "scope": scopes}
		if tweak != nil {
			tweak(claims, reply)
		}
		reply["id_token"] = idToken(cmp.Or(s.auth.signer, s.auth.key), claims)
		return 200, reply
	}
}

// The authorize URL is exactly the documented one: PKCE S256, fresh state and
// nonce, the loopback redirect on the console's port, the host id, and the
// registration hint only for a first registration.
func TestChatGPTStart(t *testing.T) {
	s := newSignIn(t, config.Config{})
	for _, origin := range []string{"", "https://admin.example.com", "http://admin.example.com:8081", "http://192.168.1.5:8081"} {
		if w := s.do("POST", "/api/admin/settings/chatgpt/start", map[string]any{}, origin); w.Code != 400 || !strings.Contains(w.Body.String(), "ssh -L 8081:127.0.0.1:8081") {
			t.Errorf("origin %q: %d %s", origin, w.Code, w.Body)
		}
	}
	q := s.start()
	var hostID string
	s.pool.QueryRow(context.Background(), "select host_id from ai_settings").Scan(&hostID)
	p := s.pending[q.Get("state")]
	sum := sha256.Sum256([]byte(p.verifier))
	want := url.Values{"client_id": {"dynamic_agent_client"}, "agent_name_hint": {"Foretrade"}, "ext_agent_host_id": {hostID},
		"response_type": {"code"}, "redirect_uri": {"http://127.0.0.1:8081/auth/callback"},
		"scope": {"openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"}, "resource": {"https://api.openai.com/v1"},
		"state": {q.Get("state")}, "nonce": {p.nonce}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"}}
	if q.Encode() != want.Encode() || !strings.HasPrefix(hostID, "urn:uuid:") || len(q.Get("state")) < 43 || len(p.verifier) < 43 || q.Get("state") == p.nonce {
		t.Fatalf("authorize query\n got %v\nwant %v", q, want)
	}
	if q2 := s.start(); q2.Get("state") == q.Get("state") || q2.Get("nonce") == q.Get("nonce") {
		t.Fatal("state and nonce must be fresh per attempt")
	}
	s.do("POST", "/api/admin/settings/chatgpt/start", map[string]any{}, "http://127.0.0.1:5174") // any loopback port
	for _, p := range s.pending {
		if p.redirect == "http://127.0.0.1:5174/auth/callback" && p.origin == "http://127.0.0.1:5174" {
			return
		}
	}
	t.Fatal("the redirect follows the console's port")
}

// A sign-in exchanges the code with the PKCE verifier, verifies the id_token,
// stores the credential sealed, makes chatgpt the provider and hands the
// client its tokens; a second sign-in reauthorizes the issued client.
func TestChatGPTSignIn(t *testing.T) {
	s := newSignIn(t, config.Config{})
	s.view(s.do("PUT", "/api/admin/settings", map[string]any{"ai": map[string]any{"provider": "openai", "model": "gpt-x", "apiKey": apiKey}}, ""))
	q := s.start()
	p := s.pending[q.Get("state")]
	s.grant(q.Get("nonce"), nil)
	back := s.callback(url.Values{"code": {"code-1"}, "state": {q.Get("state")}, "client_id": {"oaiapp_1"}, "scope": {scopes}})
	if back.String() != consoleOrigin+"/settings?chatgpt=ok" {
		t.Fatalf("redirect %s", back)
	}
	form := s.auth.sent("/api/accounts/oauth/token")[0]
	want := url.Values{"grant_type": {"authorization_code"}, "client_id": {"oaiapp_1"}, "code": {"code-1"}, "code_verifier": {p.verifier},
		"redirect_uri": {"http://127.0.0.1:8081/auth/callback"}, "resource": {"https://api.openai.com/v1"}}
	if form.Encode() != want.Encode() {
		t.Fatalf("exchange %v", form)
	}
	v := s.view(s.do("GET", "/api/admin/settings", nil, ""))
	if v.AI.Provider != "chatgpt" || v.AI.Model != "" || v.AI.APIKey.Set || !v.AI.ChatGPT.SignedIn || v.AI.ChatGPT.Email != "owner@example.com" ||
		v.AI.Status != "error" || !strings.Contains(v.AI.Error, "choose a model") {
		t.Fatalf("signed in: %+v", v.AI)
	}
	if s.ai.Configured() { // the plan has no default model, and the replaced openai key must not stay in force
		t.Fatalf("client still on %q", s.ai.Settings().Provider)
	}
	if v = s.view(s.do("PUT", "/api/admin/settings", map[string]any{"ai": map[string]any{"model": "gpt-plan"}}, "")); v.AI.Status != "ready" {
		t.Fatalf("model picked: %+v", v.AI)
	}
	st := s.ai.Settings()
	if st.Provider != "chatgpt" || st.ChatGPT == nil {
		t.Fatalf("client: %q", st.Provider)
	}
	if tok, err := st.ChatGPT.Token(context.Background()); err != nil || tok != "access-SECRET-1" {
		t.Fatalf("token: %v", err)
	}
	if got := s.audits(); got[len(got)-2] != `settings.chatgpt.signin {"changed": ["chatgpt", "provider", "model", "apiKey"]}` { // then the model's settings.update
		t.Fatalf("audit %v", got)
	}
	var sealed []byte
	s.pool.QueryRow(context.Background(), "select chatgpt from ai_settings").Scan(&sealed)
	if strings.Contains(string(sealed), "SECRET") || strings.Contains(string(sealed), "oaiapp_1") {
		t.Fatal("credential stored in clear")
	}
	if w := s.do("GET", "/auth/callback?"+q.Encode(), nil, ""); w.Code != 400 {
		t.Fatalf("a state is single use: %d", w.Code)
	}

	q = s.start()
	if q.Get("client_id") != "oaiapp_1" || q.Has("agent_name_hint") || q.Get("id_token_hint") == "" {
		t.Fatalf("reauthorization query %v", q)
	}
	s.grant(q.Get("nonce"), nil)
	if back := s.callback(url.Values{"code": {"code-2"}, "state": {q.Get("state")}}); back.Query().Get("chatgpt") != "ok" {
		t.Fatalf("reauthorization without client_id: %s", back)
	}
	q = s.start()
	s.grant(q.Get("nonce"), func(claims, _ map[string]any) { claims["sub"] = "someone-else" })
	if back := s.callback(url.Values{"code": {"c"}, "state": {q.Get("state")}}); !strings.Contains(back.Query().Get("message"), "another ChatGPT account") {
		t.Fatalf("another account: %s", back)
	}
	q = s.start()
	if back := s.callback(url.Values{"code": {"c"}, "state": {q.Get("state")}, "client_id": {"oaiapp_2"}}); !strings.Contains(back.Query().Get("message"), "another app registration") {
		t.Fatalf("another client: %s", back)
	}
}

// Every failed check ends on the console's Settings page with a safe message
// and stores nothing.
func TestChatGPTCallbackRejects(t *testing.T) {
	s := newSignIn(t, config.Config{})
	for _, c := range []struct {
		name  string
		q     url.Values
		tweak func(claims, reply map[string]any)
		key   *rsa.PrivateKey
		msg   string
	}{
		{name: "declined", q: url.Values{"error": {"access_denied"}}, msg: "Sign-in was cancelled."},
		{name: "other error", q: url.Values{"error": {"<script>"}}, msg: "ChatGPT sign-in failed (unknown)."},
		{name: "no client", q: url.Values{"code": {"c"}}, msg: "registration is incomplete"},
		{name: "dynamic client echoed", q: url.Values{"code": {"c"}, "client_id": {"dynamic_agent_client"}}, msg: "registration is incomplete"},
		{name: "no code", q: url.Values{"client_id": {"oaiapp_1"}}, msg: "no authorization code"},
		{name: "invalid_grant", q: url.Values{"code": {"c"}, "client_id": {"oaiapp_1"}}, msg: "refused the sign-in (invalid_grant)"},
		{name: "no plan scope", tweak: func(_, r map[string]any) { r["scope"] = "openid profile email offline_access" }, msg: "did not grant plan usage"},
		{name: "audience", tweak: func(c, _ map[string]any) { c["aud"] = "oaiapp_other" }, msg: "did not verify"},
		{name: "issuer", tweak: func(c, _ map[string]any) { c["iss"] = "https://evil.example" }, msg: "did not verify"},
		{name: "expired", tweak: func(c, _ map[string]any) { c["exp"] = time.Now().Add(-time.Minute).Unix() }, msg: "did not verify"},
		{name: "nonce", tweak: func(c, _ map[string]any) { c["nonce"] = "replayed" }, msg: "did not verify"},
		{name: "signature", key: otherKey(), msg: "did not verify"},
	} {
		t.Run(c.name, func(t *testing.T) {
			q := s.start()
			s.grant(q.Get("nonce"), c.tweak)
			if c.name == "invalid_grant" {
				s.auth.token = func(url.Values) (int, any) {
					return 400, map[string]any{"error": "invalid_grant", "error_description": "code used"}
				}
			}
			s.auth.signer = c.key // nil: the JWKS's own key
			defer func() { s.auth.signer = nil }()
			in := url.Values{"code": {"c"}, "client_id": {"oaiapp_1"}}
			if c.q != nil {
				in = c.q
			}
			in.Set("state", q.Get("state"))
			back := s.callback(in)
			if back.Query().Get("chatgpt") != "error" || !strings.Contains(back.Query().Get("message"), c.msg) || back.Path != "/settings" {
				t.Fatalf("redirect %s", back)
			}
			var stored bool
			s.pool.QueryRow(context.Background(), "select chatgpt is not null from ai_settings").Scan(&stored)
			if stored {
				t.Fatal("a rejected sign-in stored a credential")
			}
		})
	}
	for name, state := range map[string]string{"unknown": "nope", "empty": ""} {
		if w := s.do("GET", "/auth/callback?state="+state+"&code=c", nil, ""); w.Code != 400 {
			t.Errorf("%s state: %d", name, w.Code)
		}
	}
	q := s.start()
	s.mu.Lock()
	p := s.pending[q.Get("state")]
	p.at = time.Now().Add(-pendingTTL - time.Second)
	s.pending[q.Get("state")] = p
	s.mu.Unlock()
	if w := s.do("GET", "/auth/callback?state="+q.Get("state")+"&code=c&client_id=oaiapp_1", nil, ""); w.Code != 400 || !strings.Contains(w.Body.String(), "expired") {
		t.Fatalf("expired state: %d %s", w.Code, w.Body)
	}
}

func otherKey() *rsa.PrivateKey {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return k
}

// signedIn signs the env in as oaiapp_1 with tokens that expire at exp.
func (s *signIn) signedIn(exp time.Time) {
	s.t.Helper()
	q := s.start()
	s.grant(q.Get("nonce"), func(_, r map[string]any) { r["expires_in"] = int(time.Until(exp).Seconds()) })
	if back := s.callback(url.Values{"code": {"c"}, "state": {q.Get("state")}, "client_id": {"oaiapp_1"}}); back.Query().Get("chatgpt") != "ok" {
		s.t.Fatalf("sign in: %s", back)
	}
	s.grant("", nil) // refreshes: an hour each
}

// Tokens refresh near expiry under the row lock, once for concurrent
// callers, with rotation; a forced refresh replaces a refused token; a
// terminal refresh error clears the tokens but keeps the issued client.
func TestChatGPTRefresh(t *testing.T) {
	s := newSignIn(t, config.Config{})
	s.signedIn(time.Now().Add(2 * time.Minute)) // within the 5-minute skew: due
	var ts llm.TokenSource = s.Service          // the client is off until a model is picked
	var wg sync.WaitGroup
	got := make([]string, 4)
	for i := range got {
		wg.Go(func() { got[i], _ = ts.Token(context.Background()) })
	}
	wg.Wait()
	refreshes := s.auth.sent("/api/accounts/oauth/token")[1:]
	if len(refreshes) != 1 || got[0] != "access-SECRET-2" || got[3] != "access-SECRET-2" {
		t.Fatalf("one refresh for concurrent callers: %d, tokens %q", len(refreshes), got)
	}
	want := url.Values{"grant_type": {"refresh_token"}, "client_id": {"oaiapp_1"}, "refresh_token": {"refresh-SECRET-1"}, "resource": {"https://api.openai.com/v1"}}
	if refreshes[0].Encode() != want.Encode() {
		t.Fatalf("refresh form %v", refreshes[0])
	}
	if tok, _ := ts.Token(context.Background()); tok != "access-SECRET-2" || len(s.auth.sent("/api/accounts/oauth/token")) != 2 {
		t.Fatal("a fresh token is served without a refresh")
	}
	if tok, err := ts.Refresh(context.Background()); err != nil || tok != "access-SECRET-3" {
		t.Fatalf("forced refresh: %q %v", tok, err)
	}
	if last := s.auth.sent("/api/accounts/oauth/token")[2]; last.Get("refresh_token") != "refresh-SECRET-2" {
		t.Fatalf("the rotated refresh token must be used: %v", last)
	}

	// A stale refusal after someone else refreshed takes theirs, no new call.
	if tok, _ := s.refresh(context.Background(), "access-SECRET-2"); tok != "access-SECRET-3" || len(s.auth.sent("/api/accounts/oauth/token")) != 3 {
		t.Fatal("refreshed twice for one refusal")
	}

	s.auth.token = func(url.Values) (int, any) {
		return 400, map[string]any{"error": map[string]string{"code": "refresh_token_reused", "message": "reused"}}
	}
	if _, err := ts.Refresh(context.Background()); !errors.Is(err, llm.ErrUpstream) {
		t.Fatalf("terminal refresh: %v", err)
	}
	v := s.view(s.do("GET", "/api/admin/settings", nil, ""))
	if v.AI.ChatGPT.SignedIn || !strings.Contains(v.AI.ChatGPT.Error, "sign in again") || v.AI.Status != "error" {
		t.Fatalf("after a terminal refresh: %+v", v.AI)
	}
	if !strings.Contains(s.logs.String(), "code=refresh_token_reused") {
		t.Fatalf("log: %s", s.logs)
	}
	if _, err := ts.Token(context.Background()); !errors.Is(err, llm.ErrUpstream) {
		t.Fatal("signed out: no token")
	}
	if q := s.start(); q.Get("client_id") != "oaiapp_1" || q.Get("id_token_hint") == "" {
		t.Fatalf("the issued client is kept for signing in again: %v", q)
	}

	s.signedIn(time.Now().Add(time.Minute))
	s.auth.token = func(url.Values) (int, any) { return 503, map[string]any{"detail": "busy"} }
	_, err := ts.Token(context.Background())
	if !errors.Is(err, llm.ErrUpstream) || !s.view(s.do("GET", "/api/admin/settings", nil, "")).AI.ChatGPT.SignedIn {
		t.Fatalf("a transient refresh error keeps the sign-in: %v", err)
	}
}

// Sign-out revokes the refresh token, forgets the credential and turns AI
// off; an unconfirmed revocation still signs out here, and says so.
func TestChatGPTSignOut(t *testing.T) {
	s := newSignIn(t, config.Config{})
	s.signedIn(time.Now().Add(time.Hour))
	v := s.view(s.do("POST", "/api/admin/settings/chatgpt/signout", map[string]any{}, ""))
	revoked := s.auth.sent("/api/accounts/oauth/revoke")
	want := url.Values{"token": {"refresh-SECRET-1"}, "token_type_hint": {"refresh_token"}, "client_id": {"oaiapp_1"}}
	if len(revoked) != 1 || revoked[0].Encode() != want.Encode() {
		t.Fatalf("revoke %v", revoked)
	}
	if v.AI.ChatGPT.SignedIn || v.AI.ChatGPT.Error != "" || v.AI.Provider != "" || v.AI.Status != "off" || s.ai.Configured() {
		t.Fatalf("signed out: %+v", v.AI)
	}
	if got := s.audits(); got[len(got)-1] != `settings.chatgpt.signout {"changed": ["chatgpt", "provider"]}` {
		t.Fatalf("audit %v", got)
	}
	n := len(s.audits())
	s.view(s.do("POST", "/api/admin/settings/chatgpt/signout", map[string]any{}, ""))
	if len(s.audits()) != n {
		t.Fatal("signing out twice changes nothing")
	}

	s.signedIn(time.Now().Add(time.Hour))
	s.auth.Close() // revocation unreachable
	v = s.view(s.do("POST", "/api/admin/settings/chatgpt/signout", map[string]any{}, ""))
	if v.AI.ChatGPT.SignedIn || !strings.Contains(v.AI.ChatGPT.Error, "did not confirm") {
		t.Fatalf("unconfirmed: %+v", v.AI.ChatGPT)
	}
}

// In env mode the console can sign in only when the environment names chatgpt.
func TestChatGPTStartInEnvMode(t *testing.T) {
	s := newSignIn(t, config.Config{AI: llm.Settings{Provider: "openai", APIKey: envAPIKey}})
	if w := s.do("POST", "/api/admin/settings/chatgpt/start", map[string]any{}, consoleOrigin); w.Code != 409 {
		t.Fatalf("env openai: %d", w.Code)
	}
	s = newSignIn(t, config.Config{AI: llm.Settings{Provider: "chatgpt", Model: "env-model"}})
	if v := s.view(s.do("GET", "/api/admin/settings", nil, "")); v.AI.Status != "error" || v.AI.Error != "Sign in with ChatGPT." {
		t.Fatalf("env chatgpt, not signed in: %+v", v.AI)
	}
	s.signedIn(time.Now().Add(time.Hour))
	if v := s.view(s.do("GET", "/api/admin/settings", nil, "")); v.AI.Status != "ready" || v.AI.Model != "env-model" || !v.AI.ReadOnly {
		t.Fatalf("env chatgpt, signed in: %+v", v.AI)
	}
}

// The plan's token goes only to its fixed endpoint: no draft, stored row or
// environment base URL travels with it, and a sign-in or save stores none.
func TestChatGPTTokenNeverGoesToABaseURL(t *testing.T) {
	const foreign = "https://collector.example"
	s := newSignIn(t, config.Config{})
	s.view(s.do("PUT", "/api/admin/settings", map[string]any{"ai": map[string]any{"provider": "ollama", "baseUrl": "http://host.docker.internal:11434/v1"}}, ""))
	s.signedIn(time.Now().Add(time.Hour))
	if got := s.audits(); got[len(got)-1] != `settings.chatgpt.signin {"changed": ["chatgpt", "provider", "baseUrl"]}` {
		t.Fatalf("audit %v", got)
	}
	var seen []llm.Settings
	s.Service.models = func(_ context.Context, st llm.Settings) ([]llm.ModelInfo, error) {
		seen = append(seen, st)
		return nil, nil
	}
	draft := map[string]any{"ai": map[string]any{"baseUrl": foreign}}
	s.do("POST", "/api/admin/settings/test", draft, "")
	s.do("POST", "/api/admin/settings/models", draft, "")
	s.do("PUT", "/api/admin/settings", draft, "")
	// A row from before this rule: the client must not take its base URL either.
	if _, err := s.pool.Exec(context.Background(), "update ai_settings set base_url = $1, model = 'm'", foreign); err != nil {
		t.Fatal(err)
	}
	if err := s.reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	seen = append(append(seen, s.tested...), s.ai.Settings())
	for _, st := range seen {
		if st.Provider == "chatgpt" && (st.ChatGPT == nil || st.BaseURL != "") {
			t.Fatalf("the plan's token with base URL %q", st.BaseURL)
		}
	}
	s.signedIn(time.Now().Add(time.Hour))
	var base string
	s.pool.QueryRow(context.Background(), "select base_url from ai_settings").Scan(&base)
	if base != "" {
		t.Fatalf("stored base URL %q", base)
	}

	s = newSignIn(t, config.Config{AI: llm.Settings{Provider: "chatgpt", Model: "m", BaseURL: foreign}})
	if st := s.ai.Settings(); st.BaseURL != "" || st.ChatGPT == nil {
		t.Fatalf("env base URL %q", st.BaseURL)
	}
}

// A refresh the issuer has accepted (the old refresh token is spent) is
// stored even when the caller that asked for it goes away mid-exchange.
func TestChatGPTRefreshOutlivesItsCaller(t *testing.T) {
	s := newSignIn(t, config.Config{})
	s.signedIn(time.Now().Add(2 * time.Minute)) // due
	received, release := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	spent := map[string]bool{}
	s.auth.token = func(form url.Values) (int, any) {
		mu.Lock()
		reused := spent[form.Get("refresh_token")]
		spent[form.Get("refresh_token")] = true
		mu.Unlock()
		if reused {
			return 400, map[string]any{"error": map[string]string{"code": "refresh_token_reused"}}
		}
		close(received)
		<-release
		return 200, map[string]any{"access_token": "access-SECRET-r", "refresh_token": "refresh-SECRET-r", "expires_in": 3600, "scope": scopes}
	}
	var ts llm.TokenSource = s.Service // the client is off until a model is picked
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := ts.Token(ctx); done <- err }()
	<-received
	cancel() // the chat user closed the tab
	close(release)
	<-done
	if tok, err := ts.Token(context.Background()); err != nil || tok != "access-SECRET-r" {
		t.Fatalf("the rotated token was lost: %q %v", tok, err)
	}
	if v := s.view(s.do("GET", "/api/admin/settings", nil, "")); !v.AI.ChatGPT.SignedIn {
		t.Fatalf("signed out: %+v", v.AI.ChatGPT)
	}
}

// In env mode a sign-in stores only the credential: the dormant console
// config (provider, model, key) is back in force once AI_PROVIDER goes.
func TestChatGPTSignInInEnvModeKeepsTheRow(t *testing.T) {
	s := newSignIn(t, config.Config{})
	s.view(s.do("PUT", "/api/admin/settings", map[string]any{"ai": map[string]any{"provider": "openai", "model": "gpt-x", "apiKey": apiKey}}, ""))
	s.Service.env = llm.Settings{Provider: "chatgpt", Model: "env-model"}
	s.signedIn(time.Now().Add(time.Hour))
	if v := s.view(s.do("GET", "/api/admin/settings", nil, "")); v.AI.Status != "ready" || v.AI.Model != "env-model" {
		t.Fatalf("env chatgpt: %+v", v.AI)
	}
	if got := s.audits(); got[len(got)-1] != `settings.chatgpt.signin {"changed": ["chatgpt"]}` {
		t.Fatalf("audit %v", got)
	}
	s.Service.env = llm.Settings{}
	if err := s.reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := s.ai.Settings(); st.Provider != "openai" || st.Model != "gpt-x" || st.APIKey != apiKey {
		t.Fatalf("the console config was rewritten: %q %q key %v", st.Provider, st.Model, st.APIKey != "")
	}
}

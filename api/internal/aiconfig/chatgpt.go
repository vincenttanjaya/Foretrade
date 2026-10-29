package aiconfig

// "Sign in with ChatGPT for open-source and locally hosted apps"
// (developers.openai.com/siwc/token-sharing-open-source, re-read 2026-10-29):
// OAuth 2 with PKCE and dynamic client registration, a loopback redirect on
// the admin console's port, an RS256 id_token checked against the issuer's
// JWKS, rotating refresh tokens renewed under the settings row's lock.

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
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/vincenttanjaya/Foretrade/api/internal/httpx"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm"
)

const (
	dynamicClient = "dynamic_agent_client" // first registration; never stored as the issued client
	agentName     = "Foretrade"            // agent_name_hint: the consent screen's app name
	scopes        = "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"
	planScope     = "chatgpt.tokens.use.direct" // without it the tokens can't call the plan
	resource      = "https://api.openai.com/v1"
	pendingTTL    = 10 * time.Minute
	refreshSkew   = 5 * time.Minute
	refreshBudget = time.Minute // the row lock, the exchange (s.http's 30s) and the commit
)

// credential is a ChatGPT sign-in. A terminal refresh error clears only the
// tokens: the issued client, id_token and identity stay for signing in again.
type credential struct {
	ClientID          string    `json:"client_id"`
	AccessToken       string    `json:"access_token,omitempty"`
	RefreshToken      string    `json:"refresh_token,omitempty"`
	IDToken           string    `json:"id_token,omitempty"`
	ExpiresAt         time.Time `json:"expires_at"`
	EarliestRefreshAt time.Time `json:"earliest_refresh_at"`
	Scope             string    `json:"scope,omitempty"`
	Sub               string    `json:"sub,omitempty"`
	Email             string    `json:"email,omitempty"`
}

func (c *credential) signedIn() bool { return c != nil && c.AccessToken != "" && c.RefreshToken != "" }

// due: expired, or within refreshSkew of expiry and not before earliest_refresh_at.
func (c *credential) due(now time.Time) bool {
	return !now.Before(c.ExpiresAt) || now.Add(refreshSkew).After(c.ExpiresAt) && !now.Before(c.EarliestRefreshAt)
}

// update takes a token response; absent fields keep their value.
func (c *credential) update(t tokenReply, now time.Time) {
	c.AccessToken = t.AccessToken
	c.RefreshToken = cmp.Or(t.RefreshToken, c.RefreshToken)
	c.IDToken = cmp.Or(t.IDToken, c.IDToken)
	c.Scope = cmp.Or(t.Scope, c.Scope)
	c.ExpiresAt = now.Add(time.Duration(cmp.Or(t.ExpiresIn, 3600)) * time.Second)
	c.EarliestRefreshAt = t.earliest()
}

// pending is a sign-in between start and callback, found by its single-use
// state: the callback arrives on 127.0.0.1 without the console's cookie.
// ponytail: in memory, lost on restart; the admin starts again.
type pending struct {
	verifier, nonce, redirect, origin string
	clientID                          string // "" on first registration
	admin                             string
	at                                time.Time
}

var errSignedOut = fmt.Errorf("%w: ChatGPT is not signed in", llm.ErrUpstream)

const loopbackOnly = "Sign in with ChatGPT needs the admin console on a loopback address: open it at http://localhost:<admin port> " +
	"on the server, or from your machine through an SSH tunnel (ssh -L 8081:127.0.0.1:8081 <server>, then http://localhost:8081)."

// redirectPort is the port of a loopback http Origin: the redirect URI is
// http://127.0.0.1:<port>/auth/callback, where only the port may vary.
func redirectPort(origin string) (string, error) {
	u, err := url.Parse(origin)
	if origin == "" || err != nil || u.Scheme != "http" {
		return "", invalid(loopbackOnly)
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		return cmp.Or(u.Port(), "80"), nil
	}
	return "", invalid(loopbackOnly)
}

func randomText() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// start is POST /api/admin/settings/chatgpt/start: the authorize URL the
// console opens. A stored client reauthorizes (with the last id_token as a
// hint); otherwise this host registers a new one.
func (s *Service) start(w http.ResponseWriter, r *http.Request) {
	port, err := redirectPort(r.Header.Get("Origin"))
	switch {
	case s.envMode() && s.env.Provider != "chatgpt":
		err = errEnv
	case s.box == nil:
		err = invalid(errNoKey.Error())
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	row, err := s.read(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	p := pending{verifier: randomText(), nonce: randomText(), redirect: "http://127.0.0.1:" + port + "/auth/callback",
		origin: r.Header.Get("Origin"), admin: httpx.UserID(r.Context()), at: time.Now()}
	challenge := sha256.Sum256([]byte(p.verifier))
	state := randomText()
	q := url.Values{"response_type": {"code"}, "redirect_uri": {p.redirect}, "scope": {scopes}, "resource": {resource},
		"state": {state}, "nonce": {p.nonce}, "code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])},
		"code_challenge_method": {"S256"}, "ext_agent_host_id": {row.hostID}, "client_id": {dynamicClient}, "agent_name_hint": {agentName}}
	if c := row.cred; c != nil && c.ClientID != "" {
		p.clientID = c.ClientID
		q.Set("client_id", c.ClientID)
		q.Del("agent_name_hint")
		if c.IDToken != "" {
			q.Set("id_token_hint", c.IDToken)
		}
	}
	s.mu.Lock()
	for k, old := range s.pending {
		if time.Since(old.at) > pendingTTL {
			delete(s.pending, k)
		}
	}
	s.pending[state] = p
	s.mu.Unlock()
	httpx.JSON(w, http.StatusOK, map[string]string{"url": s.authBase + "/api/accounts/authorize?" + q.Encode()})
}

// Callback is GET /auth/callback, outside the admin gate: the single-use
// state stands in for the session. It finishes the sign-in, makes chatgpt
// the provider and sends the browser back to the console's Settings page.
func (s *Service) Callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	s.mu.Lock()
	p, ok := s.pending[q.Get("state")]
	delete(s.pending, q.Get("state"))
	s.mu.Unlock()
	if !ok || time.Since(p.at) > pendingTTL {
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "This sign-in is unknown or expired. Start again from the admin console's Settings page.", http.StatusBadRequest)
		return
	}
	back := p.origin + "/settings?chatgpt=ok"
	if err := s.finish(r.Context(), p, q); err != nil {
		var bad invalid
		msg := "ChatGPT sign-in failed. Try again."
		if errors.As(err, &bad) {
			msg = string(bad)
		}
		slog.Warn("chatgpt sign-in failed", "err", err) // never a token: errors carry codes only
		back = p.origin + "/settings?chatgpt=error&message=" + url.QueryEscape(msg)
	}
	http.Redirect(w, r, back, http.StatusFound)
}

// safeCode keeps an OAuth error code fit to show: lower-case words, ≤ 64 chars.
var safeCode = regexp.MustCompile(`^[a-z0-9_.]{1,64}$`)

func code(s string) string {
	if safeCode.MatchString(s) {
		return s
	}
	return "unknown"
}

func (s *Service) finish(ctx context.Context, p pending, q url.Values) error {
	if e := q.Get("error"); e != "" {
		if e == "access_denied" {
			return invalid("Sign-in was cancelled.")
		}
		return invalid("ChatGPT sign-in failed (" + code(e) + ").")
	}
	client := q.Get("client_id")
	switch {
	case p.clientID == "" && (client == "" || client == dynamicClient):
		return invalid("ChatGPT registration is incomplete: sign in again.")
	case p.clientID != "" && client == "":
		client = p.clientID
	case p.clientID != "" && client != p.clientID:
		return invalid("ChatGPT returned another app registration: sign out, then sign in again.")
	}
	if q.Get("code") == "" {
		return invalid("ChatGPT returned no authorization code: sign in again.")
	}
	// ponytail: a failed exchange after a first registration forgets the issued client; the next sign-in registers another.
	t, err := s.exchange(ctx, url.Values{"grant_type": {"authorization_code"}, "client_id": {client}, "code": {q.Get("code")},
		"code_verifier": {p.verifier}, "redirect_uri": {p.redirect}, "resource": {resource}})
	var te *tokenError
	if errors.As(err, &te) {
		return invalid("ChatGPT refused the sign-in (" + te.Code + "): sign in again.")
	}
	if err != nil {
		return err
	}
	if !slices.Contains(strings.Fields(t.Scope), planScope) {
		return invalid("ChatGPT did not grant plan usage (" + planScope + "): sign in again and allow it.")
	}
	id, err := s.verify(ctx, t.IDToken, client, p.nonce)
	if err != nil {
		return fmt.Errorf("%w (%w)", invalid("ChatGPT's identity token did not verify: sign in again."), err)
	}
	c := credential{ClientID: client, Sub: id.Sub, Email: id.Email}
	c.update(t, time.Now())
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var sealed []byte
		var provider string
		var model, base, key bool // set: the columns a switch to chatgpt clears
		if err := tx.QueryRow(ctx, "select chatgpt, provider, model <> '', base_url <> '', secret is not null from ai_settings where id = 1 for update").
			Scan(&sealed, &provider, &model, &base, &key); err != nil {
			return err
		}
		if old, _ := s.openCred(sealed); p.clientID != "" && old != nil && old.Sub != "" && old.Sub != c.Sub {
			return invalid("That is another ChatGPT account than this app's registration: sign out first.")
		}
		b, err := s.sealCred(&c)
		if err != nil {
			return err
		}
		// In env mode the row is the dormant console config: only the credential changes.
		sets, changed := "chatgpt = $1", []string{"chatgpt"}
		if !s.envMode() {
			// chatgpt has no base URL; a provider switch drops the old model and key, as a PUT would.
			switched := provider != "chatgpt"
			sets += ", provider = 'chatgpt', base_url = ''"
			if switched {
				sets += ", model = '', secret = null, secret_hint = ''"
			}
			for _, f := range []struct {
				name string
				diff bool
			}{{"provider", switched}, {"model", switched && model}, {"baseUrl", base}, {"apiKey", switched && key}} {
				if f.diff {
					changed = append(changed, f.name)
				}
			}
		}
		if _, err := tx.Exec(ctx, "update ai_settings set "+sets+", updated_at = now(), updated_by = $2 where id = 1", b, p.admin); err != nil {
			return err
		}
		return audit(ctx, tx, p.admin, "settings.chatgpt.signin", changed)
	})
	if errors.Is(err, errForbidden) {
		return invalid("The admin who started this sign-in no longer exists.")
	}
	if err != nil {
		return err
	}
	return s.reload(ctx)
}

// signOut is POST /api/admin/settings/chatgpt/signout: revoke the refresh
// token, forget the credential and, when chatgpt was the provider, turn AI off.
func (s *Service) signOut(w http.ResponseWriter, r *http.Request) {
	ctx, admin := r.Context(), httpx.UserID(r.Context())
	cur, err := s.read(ctx)
	if err != nil {
		fail(w, r, err)
		return
	}
	if !cur.stored["chatgpt"] {
		httpx.JSON(w, http.StatusOK, s.view(cur))
		return
	}
	confirmed := true
	if cur.cred.signedIn() {
		if err := s.revoke(ctx, cur.cred); err != nil {
			slog.Warn("chatgpt revoke not confirmed", "err", err)
			confirmed = false
		}
	}
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var provider string
		if err := tx.QueryRow(ctx, "select provider from ai_settings where id = 1 for update").Scan(&provider); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update ai_settings set chatgpt = null, provider = case when provider = 'chatgpt' then '' else provider end,
			updated_at = now(), updated_by = $1 where id = 1`, admin); err != nil {
			return err
		}
		changed := []string{"chatgpt"}
		if provider == "chatgpt" {
			changed = append(changed, "provider")
		}
		return audit(ctx, tx, admin, "settings.chatgpt.signout", changed)
	})
	if err == nil {
		cur, err = s.read(ctx)
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	s.apply(cur)
	v := s.view(cur)
	if !confirmed {
		v.AI.ChatGPT.Error = "Signed out here, but ChatGPT did not confirm it: you can also disconnect Foretrade in ChatGPT's settings."
	}
	httpx.JSON(w, http.StatusOK, v)
}

func (s *Service) revoke(ctx context.Context, c *credential) error {
	resp, err := s.post(ctx, "/api/accounts/oauth/revoke", url.Values{"token": {c.RefreshToken}, "token_type_hint": {"refresh_token"}, "client_id": {c.ClientID}})
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("revoke: HTTP %d", resp.StatusCode)
	}
	return nil
}

func (s *Service) post(ctx context.Context, path string, form url.Values) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.authBase+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	return s.http.Do(req)
}

type tokenReply struct {
	AccessToken       string          `json:"access_token"`
	RefreshToken      string          `json:"refresh_token"`
	IDToken           string          `json:"id_token"`
	ExpiresIn         int64           `json:"expires_in"`
	Scope             string          `json:"scope"`
	EarliestRefreshAt json.RawMessage `json:"earliest_refresh_at"`
}

// earliest reads earliest_refresh_at, which the docs list but don't define:
// Unix seconds or an RFC 3339 time; anything else is no constraint.
func (t tokenReply) earliest() time.Time {
	var v any
	json.Unmarshal(t.EarliestRefreshAt, &v)
	switch v := v.(type) {
	case float64:
		return time.Unix(int64(v), 0)
	case string:
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return time.Unix(n, 0)
		}
		at, _ := time.Parse(time.RFC3339, v)
		return at
	}
	return time.Time{}
}

// tokenError is the token endpoint's refusal; Code (e.g. invalid_grant) is safe to show.
type tokenError struct {
	Status int
	Code   string
}

func (e *tokenError) Error() string {
	return fmt.Sprintf("chatgpt token endpoint: HTTP %d %s", e.Status, e.Code)
}

// exchange posts to the token endpoint. Its errors never carry a token.
func (s *Service) exchange(ctx context.Context, form url.Values) (tokenReply, error) {
	var t tokenReply
	resp, err := s.post(ctx, "/api/accounts/oauth/token", form)
	if err != nil {
		return t, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return t, err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct{ Error json.RawMessage }
		json.Unmarshal(body, &e)
		var c string
		if json.Unmarshal(e.Error, &c) != nil {
			var o struct{ Code, Type string }
			json.Unmarshal(e.Error, &o)
			c = cmp.Or(o.Code, o.Type)
		}
		return t, &tokenError{resp.StatusCode, code(c)}
	}
	if err := json.Unmarshal(body, &t); err != nil || t.AccessToken == "" {
		return t, errors.New("chatgpt token endpoint: no access token in the reply")
	}
	return t, nil
}

// terminal are the refresh errors after which only a new sign-in helps.
var terminal = []string{"invalid_grant", "invalid_refresh_token", "token_expired", "refresh_token_expired", "refresh_token_invalidated", "refresh_token_reused"}

// Token is the signed-in plan's access token, refreshed within 5 minutes of
// its expiry (llm.TokenSource).
func (s *Service) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	c := s.cred
	s.mu.Unlock()
	if c.signedIn() && !c.due(time.Now()) {
		return c.AccessToken, nil
	}
	return s.refresh(ctx, "")
}

// Refresh replaces the token the provider just refused (a 401).
func (s *Service) Refresh(ctx context.Context) (string, error) {
	s.mu.Lock()
	c := s.cred
	s.mu.Unlock()
	if !c.signedIn() {
		return "", errSignedOut
	}
	return s.refresh(ctx, c.AccessToken)
}

// refresh renews the stored credential under the row's lock (select … for
// update), so no two callers or processes spend one rotating refresh token.
// stale is a refused token, renewed even before it is due unless another
// caller has replaced it already. A terminal error clears the tokens.
// It runs on its own budget, not the caller's: a rotation the issuer has
// accepted is stored even when the request that asked for it is gone.
// ponytail: a commit that fails after the refresh loses the rotated token; the admin signs in again.
// ponytail: a cancelled caller still waits out the refresh (≤ refreshBudget); hand it to a goroutine if that wait matters.
func (s *Service) refresh(ctx context.Context, stale string) (string, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshBudget)
	defer cancel()
	var c *credential
	var ended string
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var sealed []byte
		if err := tx.QueryRow(ctx, "select chatgpt from ai_settings where id = 1 for update").Scan(&sealed); err != nil {
			return err
		}
		var err error
		if c, err = s.openCred(sealed); err != nil || !c.signedIn() {
			c = nil
			return errSignedOut
		}
		now := time.Now()
		if stale == "" && !c.due(now) || stale != "" && c.AccessToken != stale {
			return nil
		}
		t, err := s.exchange(ctx, url.Values{"grant_type": {"refresh_token"}, "client_id": {c.ClientID},
			"refresh_token": {c.RefreshToken}, "resource": {resource}})
		var te *tokenError
		switch {
		case errors.As(err, &te) && slices.Contains(terminal, te.Code):
			ended = te.Code
			c.AccessToken, c.RefreshToken, c.ExpiresAt = "", "", time.Time{}
		case err != nil:
			return err
		default:
			c.update(t, now)
		}
		b, err := s.sealCred(c)
		if err == nil {
			_, err = tx.Exec(ctx, "update ai_settings set chatgpt = $1 where id = 1", b)
		}
		return err
	})
	if err == nil || errors.Is(err, errSignedOut) {
		s.mu.Lock()
		s.cred = c
		s.mu.Unlock()
	}
	switch {
	case errors.Is(err, llm.ErrUpstream):
		return "", err
	case err != nil:
		return "", fmt.Errorf("%w: chatgpt token refresh: %w", llm.ErrUpstream, err)
	case ended != "":
		slog.Warn("chatgpt sign-in ended: an admin must sign in again", "code", ended)
		return "", errSignedOut
	}
	return c.AccessToken, nil
}

func (s *Service) openCred(sealed []byte) (*credential, error) {
	if sealed == nil {
		return nil, nil
	}
	plain, err := s.box.open(sealed)
	if err != nil {
		return nil, err
	}
	var c credential
	return &c, json.Unmarshal(plain, &c)
}

func (s *Service) sealCred(c *credential) ([]byte, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	return s.box.seal(b)
}

// identity is the id_token's verified claims.
type identity struct {
	Iss   string   `json:"iss"`
	Aud   audience `json:"aud"`
	Exp   float64  `json:"exp"`
	Nonce string   `json:"nonce"`
	Sub   string   `json:"sub"`
	Email string   `json:"email"`
}

// audience is a JWT aud: one string or a list.
type audience []string

func (a *audience) UnmarshalJSON(b []byte) error {
	var one string
	if json.Unmarshal(b, &one) == nil {
		*a = audience{one}
		return nil
	}
	return json.Unmarshal(b, (*[]string)(a))
}

// verify checks an RS256 id_token against the issuer's JWKS, then its
// issuer, audience (the issued client), expiry and nonce.
func (s *Service) verify(ctx context.Context, token, client, nonce string) (identity, error) {
	var id identity
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return id, errors.New("id_token is not a JWS")
	}
	var head struct{ Alg, Kid string }
	if err := decodePart(parts[0], &head); err != nil || head.Alg != "RS256" {
		return id, fmt.Errorf("id_token alg %q", head.Alg)
	}
	key, err := s.jwk(ctx, head.Kid)
	if err != nil {
		return id, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return id, err
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig); err != nil {
		return id, errors.New("id_token signature")
	}
	if err := decodePart(parts[1], &id); err != nil {
		return id, err
	}
	switch {
	case id.Iss != s.authBase:
		return id, errors.New("id_token issuer")
	case !slices.Contains(id.Aud, client):
		return id, errors.New("id_token audience")
	case !time.Now().Before(time.Unix(int64(id.Exp), 0)):
		return id, errors.New("id_token expired")
	case id.Nonce != nonce:
		return id, errors.New("id_token nonce")
	case id.Sub == "":
		return id, errors.New("id_token subject")
	}
	return id, nil
}

func decodePart(part string, v any) error {
	b, err := base64.RawURLEncoding.DecodeString(part)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// jwks caches the issuer's signing keys; an unknown kid refetches them at
// most once a minute.
type jwks struct {
	mu   sync.Mutex
	keys map[string]*rsa.PublicKey
	at   time.Time
}

func (s *Service) jwk(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	j := &s.keys
	j.mu.Lock()
	defer j.mu.Unlock() // held across the fetch: sign-ins are rare
	if k := j.keys[kid]; k != nil {
		return k, nil
	}
	if time.Since(j.at) < time.Minute {
		return nil, fmt.Errorf("id_token key %q unknown", kid)
	}
	j.at = time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.authBase+"/.well-known/jwks.json", nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var set struct {
		Keys []struct{ Kty, Kid, N, E string }
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&set); err != nil || resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jwks: HTTP %d", resp.StatusCode)
	}
	j.keys = map[string]*rsa.PublicKey{}
	for _, k := range set.Keys {
		n, errN := base64.RawURLEncoding.DecodeString(k.N)
		e, errE := base64.RawURLEncoding.DecodeString(k.E)
		if k.Kty != "RSA" || errN != nil || errE != nil || len(e) == 0 || len(e) > 4 {
			continue
		}
		j.keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	}
	if k := j.keys[kid]; k != nil {
		return k, nil
	}
	return nil, fmt.Errorf("id_token key %q unknown", kid)
}

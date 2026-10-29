// Package auth owns users, password login, cookie sessions and the Require
// and RequireAdmin middleware. Session tokens are 32 random bytes; only their
// SHA-256 is stored.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/vincenttanjaya/Foretrade/api/internal/httpx"
	"github.com/vincenttanjaya/Foretrade/api/internal/profile"
	"github.com/vincenttanjaya/Foretrade/api/internal/usage"
)

const (
	cookieName = "tsid"
	sessionTTL = 7 * 24 * time.Hour
)

// BcryptCost is 12 in production; a var only so tests can hash at MinCost.
var BcryptCost = 12

var errInvalidCredentials = errors.New("invalid credentials")

// dummyHash is compared against when the email is unknown, so a miss costs
// the same bcrypt time as a wrong password.
var dummyHash = sync.OnceValue(func() []byte {
	h, _ := bcrypt.GenerateFromPassword([]byte("not-a-real-password"), BcryptCost)
	return h
})

type User struct {
	ID      string          `json:"id"`
	Email   string          `json:"email"`
	Name    string          `json:"name"`
	Role    string          `json:"role"` // "user" | "admin"
	Profile profile.Profile `json:"profile"`
}

type Service struct {
	db      *pgxpool.Pool
	usage   *usage.Meter
	secure  bool
	limiter *limiter // logins per client IP
	admin   *limiter // admin mutations per admin
}

func NewService(db *pgxpool.Pool, meter *usage.Meter, cookieSecure bool) *Service {
	return &Service{db: db, usage: meter, secure: cookieSecure,
		limiter: &limiter{max: 10, window: 10 * time.Minute, hits: map[string]*window{}},
		admin:   &limiter{max: 60, window: 10 * time.Minute, hits: map[string]*window{}}}
}

// HashPassword is the bcrypt hash every stored password uses.
func HashPassword(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), BcryptCost)
	return string(h), err
}

// EnsureUser is the boot-time bootstrap of a config-managed account (demo,
// admin): it creates the user when the email is missing, else refreshes only
// a changed password hash, which signs out that account's sessions (rotation
// revokes a leak; an unchanged password keeps them, so a redeploy logs nobody
// out). Role, name, credit limit and disabled state are never touched, so
// console changes survive deploys; a mismatch with the config is logged.
func (s *Service) EnsureUser(ctx context.Context, email, name, password, role string, initiallyEnabled ...bool) (User, error) {
	u := User{Email: normalizeEmail(email)}
	if u.Email == "" || password == "" || name == "" {
		return User{}, fmt.Errorf("ensure user: email, name and password are required")
	}
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var old string
		var disabled bool
		err := tx.QueryRow(ctx, "select id, name, role, password_hash, disabled_at is not null from users where email = $1 for update",
			u.Email).Scan(&u.ID, &u.Name, &u.Role, &old, &disabled)
		if errors.Is(err, pgx.ErrNoRows) {
			hash, err := HashPassword(password)
			if err != nil {
				return err
			}
			u.Name, u.Role = name, role
			return tx.QueryRow(ctx, "insert into users (email, name, password_hash, role) values ($1, $2, $3, $4) returning id",
				u.Email, name, hash, role).Scan(&u.ID)
		}
		if err != nil {
			return err
		}
		if u.Role != role || disabled {
			slog.Warn("bootstrap account changed in the console; left as is", "email", u.Email, "role", u.Role, "disabled", disabled)
		}
		if bcrypt.CompareHashAndPassword([]byte(old), []byte(password)) == nil {
			return nil
		}
		hash, err := HashPassword(password)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "update users set password_hash = $2 where id = $1", u.ID, hash); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "delete from sessions where user_id = $1", u.ID)
		return err
	})
	if err != nil {
		return User{}, fmt.Errorf("ensure user: %w", err)
	}
	enabled := len(initiallyEnabled) == 0 || initiallyEnabled[0]
	u.Profile, err = profile.Ensure(ctx, s.db, u.ID, enabled)
	if err != nil {
		return User{}, fmt.Errorf("ensure profile: %w", err)
	}
	return u, nil
}

// login checks the password and opens a session, returning its raw token. A
// disabled account fails like a wrong password, after the same bcrypt work.
func (s *Service) login(ctx context.Context, email, password string) (User, string, error) {
	u := User{Email: normalizeEmail(email)}
	var hash string
	var disabled bool
	err := s.db.QueryRow(ctx, "select id, name, role, password_hash, disabled_at is not null from users where email = $1",
		u.Email).Scan(&u.ID, &u.Name, &u.Role, &hash, &disabled)
	if errors.Is(err, pgx.ErrNoRows) {
		bcrypt.CompareHashAndPassword(dummyHash(), []byte(password))
		return User{}, "", errInvalidCredentials
	}
	if err != nil {
		return User{}, "", err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil || disabled {
		return User{}, "", errInvalidCredentials
	}

	raw := make([]byte, 32)
	rand.Read(raw)
	token := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256(raw)
	if _, err := s.db.Exec(ctx, "delete from sessions where user_id = $1 and expires_at < now()", u.ID); err != nil {
		return User{}, "", err
	}
	// The session only opens if the row still holds the hash just verified:
	// FOR SHARE waits for an in-flight password reset or disable, then re-checks,
	// so a login proved with the old password can't outlive the reset.
	tag, err := s.db.Exec(ctx, `insert into sessions (token_hash, user_id, expires_at)
		select $1, id, $3 from users where id = $2 and password_hash = $4 and disabled_at is null for share`,
		sum[:], u.ID, time.Now().Add(sessionTTL), hash)
	if err != nil {
		return User{}, "", err
	}
	if tag.RowsAffected() == 0 {
		return User{}, "", errInvalidCredentials
	}
	if _, err := s.db.Exec(ctx, "update users set last_login_at = now(), last_active_at = now() where id = $1", u.ID); err != nil {
		return User{}, "", err
	}
	u.Profile, err = profile.Load(ctx, s.db, u.ID)
	if err != nil {
		return User{}, "", err
	}
	return u, token, nil
}

// TokenHash returns the stored digest for the request's session cookie.
func TokenHash(r *http.Request) ([]byte, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return nil, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil || len(raw) != 32 {
		return nil, false
	}
	sum := sha256.Sum256(raw)
	return sum[:], true
}

// Require rejects requests without a live session of an enabled user and
// puts the user id in the context.
func (s *Service) Require(next http.Handler) http.Handler { return s.require(next, false) }

// RequireAdmin is Require for /api/admin/*: the user must also be an admin
// (403 otherwise, read on every request so a demotion applies at once), every
// response is no-store, and mutations are limited per admin.
func (s *Service) RequireAdmin(next http.Handler) http.Handler { return s.require(next, true) }

// require checks the session and, in the same statement, records the user's
// activity at most once per 5 minutes.
func (s *Service) require(next http.Handler, admin bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if admin {
			w.Header().Set("Cache-Control", "no-store")
		}
		hash, ok := TokenHash(r)
		if !ok {
			httpx.Error(w, http.StatusUnauthorized, "unauthorized", "Please sign in.")
			return
		}
		var userID, role string
		err := s.db.QueryRow(r.Context(), `
			with live as (select u.id, u.role from sessions s join users u on u.id = s.user_id
				where s.token_hash = $1 and s.expires_at > now() and u.disabled_at is null),
			touch as (update users set last_active_at = now() where id = (select id from live)
				and (last_active_at is null or last_active_at < now() - interval '5 minutes'))
			select id, role from live`, hash).Scan(&userID, &role)
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusUnauthorized, "unauthorized", "Please sign in.")
			return
		}
		if err != nil {
			httpx.ServerError(w, r, err)
			return
		}
		if admin && role != "admin" {
			httpx.Error(w, http.StatusForbidden, "forbidden", "Admins only.")
			return
		}
		if admin && r.Method != http.MethodGet && !s.admin.allow(userID, time.Now()) {
			httpx.Error(w, http.StatusTooManyRequests, "rate_limited", "Too many admin changes. Try again in a few minutes.")
			return
		}
		next.ServeHTTP(w, r.WithContext(httpx.WithUser(r.Context(), userID)))
	})
}

func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	mux.Handle("POST /api/auth/logout", s.Require(http.HandlerFunc(s.handleLogout)))
	mux.Handle("GET /api/me", s.Require(http.HandlerFunc(s.handleMe)))
}

func (s *Service) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.limiter.allow(clientIP(r), time.Now()) {
		httpx.Error(w, http.StatusTooManyRequests, "rate_limited", "Too many sign-in attempts. Try again in a few minutes.")
		return
	}
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if req.Email == "" || req.Password == "" {
		httpx.Error(w, http.StatusBadRequest, "bad_request", "Email and password are required.")
		return
	}
	var u User
	var token string
	err := errInvalidCredentials // bcrypt refuses passwords over 72 bytes, so none is stored
	if len(req.Email) <= 254 && len(req.Password) <= 72 {
		u, token, err = s.login(r.Context(), req.Email, req.Password)
	}
	if errors.Is(err, errInvalidCredentials) {
		httpx.Error(w, http.StatusUnauthorized, "invalid_credentials", "Wrong email or password.")
		return
	}
	if err != nil {
		httpx.ServerError(w, r, err)
		return
	}
	http.SetCookie(w, s.cookie(token, sessionTTL))
	httpx.JSON(w, http.StatusOK, map[string]any{"user": u})
}

func (s *Service) handleLogout(w http.ResponseWriter, r *http.Request) {
	hash, _ := TokenHash(r) // Require already proved it parses
	if _, err := s.db.Exec(r.Context(), "delete from sessions where token_hash = $1", hash); err != nil {
		httpx.ServerError(w, r, err)
		return
	}
	http.SetCookie(w, s.cookie("", -1))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleMe(w http.ResponseWriter, r *http.Request) {
	userID := httpx.UserID(r.Context())
	u := User{ID: userID}
	if err := s.db.QueryRow(r.Context(), "select email, name, role from users where id = $1",
		userID).Scan(&u.Email, &u.Name, &u.Role); err != nil {
		httpx.ServerError(w, r, err)
		return
	}
	credits, err := s.usage.Get(r.Context(), userID)
	if err != nil {
		httpx.ServerError(w, r, err)
		return
	}
	u.Profile, err = profile.Load(r.Context(), s.db, userID)
	if err != nil {
		httpx.ServerError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"user": u, "credits": credits})
}

// cookie builds the host-only session cookie; a negative ttl deletes it.
func (s *Service) cookie(token string, ttl time.Duration) *http.Cookie {
	c := &http.Cookie{Name: cookieName, Value: token, Path: "/", HttpOnly: true, Secure: s.secure,
		SameSite: http.SameSiteLaxMode, MaxAge: int(ttl.Seconds())}
	if ttl < 0 {
		c.MaxAge = -1
	}
	return c
}

func normalizeEmail(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

// clientIP trusts X-Forwarded-For's first hop only from the local reverse
// proxy (Caddy on the same host); otherwise it is the TCP peer.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if addr, err := netip.ParseAddr(host); err == nil && addr.IsLoopback() {
		if first, _, _ := strings.Cut(r.Header.Get("X-Forwarded-For"), ","); strings.TrimSpace(first) != "" {
			return strings.TrimSpace(first)
		}
	}
	return host
}

// limiter is a fixed-window counter per key (client IP, admin id).
// ponytail: in-memory and per process; resets on restart. Move to Postgres or
// Redis if the API ever runs on more than one instance.
type limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	hits   map[string]*window
}

type window struct {
	start time.Time
	n     int
}

func (l *limiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.hits) > 10_000 { // drop expired windows so the map stays bounded
		for k, w := range l.hits {
			if now.Sub(w.start) >= l.window {
				delete(l.hits, k)
			}
		}
	}
	w := l.hits[key]
	if w == nil || now.Sub(w.start) >= l.window {
		w = &window{start: now}
		l.hits[key] = w
	}
	w.n++
	return w.n <= l.max
}

// Package admin serves the admin console API, /api/admin/* (API contract §3
// "Admin console"): the overview, account management, the audit log and
// (mounted from aiconfig) the AI and search settings. The
// whole prefix is mounted once behind auth.RequireAdmin. Every mutation writes
// its admin_audit row(s) in the transaction that makes the change.
package admin

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/mail"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/arena"
	"github.com/vincenttanjaya/Foretrade/api/internal/auth"
	"github.com/vincenttanjaya/Foretrade/api/internal/autopilot"
	"github.com/vincenttanjaya/Foretrade/api/internal/httpx"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
	"github.com/vincenttanjaya/Foretrade/api/internal/profile"
)

const (
	msgSelf      = "You can't disable, demote or delete your own account."
	msgLastAdmin = "At least one active admin must remain."
	msgManaged   = "This account's password comes from the server config. Rotate it there and redeploy."
	msgExists    = "An account with this email already exists."
)

// User is the contract's AdminUser.
type User struct {
	ID           string     `json:"id"`
	Email        string     `json:"email"`
	Name         string     `json:"name"`
	Role         string     `json:"role"`
	Status       string     `json:"status"` // "active" | "disabled"
	Managed      bool       `json:"managed"`
	CreatedAt    time.Time  `json:"createdAt"`
	LastLoginAt  *time.Time `json:"lastLoginAt"`
	LastActiveAt *time.Time `json:"lastActiveAt"`
	DisabledAt   *time.Time `json:"disabledAt"`
	CreditsToday int        `json:"creditsToday"`
	CreditLimit  *int       `json:"creditLimit"` // nil = the default (AI_DAILY_CREDITS)
	Equity       *float64   `json:"equity"`      // nil when market data is unavailable
	Sessions     int        `json:"sessions"`
}

type Ref struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

type AuditEntry struct {
	ID        string          `json:"id"`
	Action    string          `json:"action"`
	Admin     Ref             `json:"admin"`
	Target    *Ref            `json:"target"`
	Detail    json.RawMessage `json:"detail"`
	CreatedAt time.Time       `json:"createdAt"`
}

type handler struct{ d *app.Deps }

// Register mounts /api/admin/* behind adminOrigin and RequireAdmin: one gate
// for the whole prefix, so no admin route can be added without it.
func Register(mux *http.ServeMux, d *app.Deps) {
	h := &handler{d: d}
	sub := http.NewServeMux()
	sub.HandleFunc("GET /api/admin/overview", h.overview)
	sub.HandleFunc("GET /api/admin/users", h.list)
	sub.HandleFunc("POST /api/admin/users", h.create)
	sub.HandleFunc("GET /api/admin/users/{id}", h.detail)
	sub.HandleFunc("PATCH /api/admin/users/{id}", h.update)
	sub.HandleFunc("DELETE /api/admin/users/{id}", h.remove)
	sub.HandleFunc("POST /api/admin/users/{id}/password", h.password)
	sub.HandleFunc("POST /api/admin/users/{id}/sessions/revoke", h.revoke)
	sub.HandleFunc("POST /api/admin/users/{id}/reset-portfolio", h.resetPortfolio)
	sub.HandleFunc("POST /api/admin/users/{id}/reset-credits", h.resetCredits)
	sub.HandleFunc("POST /api/admin/users/{id}/seed", h.seed)
	sub.HandleFunc("GET /api/admin/audit", h.audit)
	if d.Settings != nil {
		d.Settings.Register(sub)
	}
	mux.Handle("/api/admin/", adminOrigin(d.AdminOrigins, d.Auth.RequireAdmin(sub)))
}

// adminOrigin refuses browser calls from any origin but the console's: the web
// app shares the session cookie and ALLOWED_ORIGINS, so a script running there
// could otherwise drive an admin's session. Browsers send Origin on every
// cross-origin fetch; a request without one (curl) passes.
func adminOrigin(allowed []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); o != "" && !slices.Contains(allowed, o) {
			w.Header().Set("Cache-Control", "no-store")
			httpx.Error(w, http.StatusForbidden, "forbidden", "Cross-site request rejected.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// userSQL selects AdminUser columns (scanUser's order). Named args: today
// (date), syms/marks (latest prices; null marks → null equity). Equity is
// the paper ledger's: cash + open prediction stakes at cost + Σ qty × mark,
// a pair with no live price at its average price.
const userSQL = `select u.id, u.email, u.name, u.role, u.created_at, u.last_login_at, u.last_active_at, u.disabled_at,
	coalesce(us.credits, 0), u.credit_limit,
	case when @marks::float8[] is not null then coalesce(a.cash, 100000)
		+ coalesce((select sum(mp.cost) from market_positions mp where mp.user_id = u.id and mp.status = 'open'), 0)
		+ coalesce((select sum(p.qty * coalesce(m.mark, p.avg_price))
		from positions p left join unnest(@syms::text[], @marks::float8[]) m(symbol, mark) using (symbol) where p.user_id = u.id), 0) end,
	(select count(*) from sessions s where s.user_id = u.id and s.expires_at > now())
	from users u
	left join ai_usage us on us.user_id = u.id and us.day = @today
	left join accounts a on a.user_id = u.id`

func (h *handler) scanUser(row pgx.Row) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.CreatedAt, &u.LastLoginAt, &u.LastActiveAt, &u.DisabledAt,
		&u.CreditsToday, &u.CreditLimit, &u.Equity, &u.Sessions)
	u.Status = "active"
	if u.DisabledAt != nil {
		u.Status = "disabled"
	}
	u.Managed = slices.Contains(h.d.Managed, u.Email)
	return u, err
}

// args are the named args every admin query draws from: the UTC windows and
// the latest marks (nil when market data is unavailable).
func (h *handler) args(ctx context.Context) pgx.NamedArgs {
	today := time.Now().UTC().Truncate(24 * time.Hour)
	a := pgx.NamedArgs{"today": today, "todayStart": today,
		"since7": today.AddDate(0, 0, -6), "since7Day": today.AddDate(0, 0, -6),
		"since": today.AddDate(0, 0, -29), "sinceDay": today.AddDate(0, 0, -29)}
	assets, err := h.d.Market.Assets(ctx)
	if err != nil {
		slog.Warn("admin: marks unavailable", "err", err)
		a["syms"], a["marks"] = []string(nil), []float64(nil)
		return a
	}
	syms, marks := make([]string, len(assets)), make([]float64, len(assets))
	for i, x := range assets {
		syms[i], marks[i] = x.Symbol, x.Price
	}
	a["syms"], a["marks"] = syms, marks
	return a
}

func (h *handler) user(ctx context.Context, id string) (User, error) {
	a := h.args(ctx)
	a["id"] = id
	return h.scanUser(h.d.DB.QueryRow(ctx, userSQL+" where u.id = @id", a))
}

// reply writes {user, …extra} with the account's fresh state.
func (h *handler) reply(w http.ResponseWriter, r *http.Request, status int, id string, extra map[string]any) {
	u, err := h.user(r.Context(), id)
	if err != nil {
		app.WriteError(w, err)
		return
	}
	body := map[string]any{"user": u}
	maps.Copy(body, extra)
	httpx.JSON(w, status, body)
}

// id is the {id} path value; anything but a canonical UUID is a 404 before
// Postgres sees it (pgtype's Scan does not check the hyphens' positions).
func id(r *http.Request) (string, error) {
	s := r.PathValue("id")
	if u := (pgtype.UUID{}); len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' || u.Scan(s) != nil {
		return "", app.ErrNotFound
	}
	return strings.ToLower(s), nil
}

var sorts = map[string]string{"createdAt": "u.created_at", "lastActiveAt": "u.last_active_at", "lastLoginAt": "u.last_login_at",
	"email": "u.email", "name": "lower(u.name)", "creditsToday": "coalesce(us.credits, 0)"}

// userFilter is the list's where clause; @q is an ILIKE pattern or "".
const userFilter = ` where (@q = '' or u.email ilike @q escape '\' or u.name ilike @q escape '\')
	and (@status = 'all' or (@status = 'disabled') = (u.disabled_at is not null))
	and (@role = '' or u.role = @role)`

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	search := strings.TrimSpace(q.Get("q"))
	status, role := cmp.Or(q.Get("status"), "all"), q.Get("role")
	sort, order := cmp.Or(q.Get("sort"), "createdAt"), cmp.Or(q.Get("order"), "desc")
	page, limit, err := paging(q, 25)
	switch {
	case err != nil:
	case utf8.RuneCountInString(search) > 100 || strings.ContainsRune(search, 0) || !utf8.ValidString(search): // Postgres rejects both
		err = &app.BadRequest{Msg: "q must be at most 100 characters."}
	case status != "all" && status != "active" && status != "disabled":
		err = &app.BadRequest{Msg: "status must be all, active or disabled."}
	case role != "" && role != "user" && role != "admin":
		err = &app.BadRequest{Msg: "role must be user or admin."}
	case sorts[sort] == "":
		err = &app.BadRequest{Msg: "sort must be createdAt, lastActiveAt, lastLoginAt, email, name or creditsToday."}
	case order != "asc" && order != "desc":
		err = &app.BadRequest{Msg: "order must be asc or desc."}
	}
	if err != nil {
		app.WriteError(w, err)
		return
	}
	ctx := r.Context()
	a := h.args(ctx)
	a["q"], a["status"], a["role"], a["limit"], a["offset"] = "", status, role, limit, min(page-1, 1<<40)*limit
	if search != "" {
		a["q"] = "%" + likeEscaper.Replace(search) + "%"
	}
	var total int
	if err := h.d.DB.QueryRow(ctx, "select count(*) from users u"+userFilter, a).Scan(&total); err != nil {
		app.WriteError(w, err)
		return
	}
	rows, err := h.d.DB.Query(ctx, userSQL+userFilter+fmt.Sprintf(" order by %s %s nulls last, u.id %[2]s limit @limit offset @offset",
		sorts[sort], order), a)
	if err != nil {
		app.WriteError(w, err)
		return
	}
	users, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (User, error) { return h.scanUser(row) })
	if err != nil {
		app.WriteError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"users": users, "total": total, "page": page, "limit": limit,
		"defaultCreditLimit": h.d.Usage.DefaultLimit()})
}

// paging reads page (≥ 1, default 1) and limit (1..100, default def).
func paging(q url.Values, def int) (page, limit int, err error) {
	page, limit = 1, def
	if s := q.Get("page"); s != "" {
		if page, err = strconv.Atoi(s); err != nil || page < 1 {
			return 0, 0, &app.BadRequest{Msg: "page must be an integer ≥ 1."}
		}
	}
	if s := q.Get("limit"); s != "" {
		if limit, err = strconv.Atoi(s); err != nil || limit < 1 || limit > 100 {
			return 0, 0, &app.BadRequest{Msg: "limit must be an integer 1..100."}
		}
	}
	return page, limit, nil
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email       string          `json:"email"`
		Name        string          `json:"name"`
		Role        *string         `json:"role"`
		CreditLimit json.RawMessage `json:"creditLimit"`
		Password    *string         `json:"password"`
		SeedFrom    string          `json:"seedFrom"` // an account to copy showcase content from
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	email, err := validEmail(req.Email)
	name, nameErr := validName(req.Name)
	role, roleErr := validRole(cmp.Or(req.Role, new("user")))
	limit, _, limitErr := creditLimit(req.CreditLimit)
	password, generated, pwErr := newPassword(req.Password)
	if err := cmp.Or(err, nameErr, roleErr, limitErr, pwErr); err != nil {
		app.WriteError(w, err)
		return
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		app.WriteError(w, err)
		return
	}
	ctx, admin := r.Context(), httpx.UserID(r.Context())
	var uid string
	var seeded *Seeded
	err = pgx.BeginFunc(ctx, h.d.DB, func(tx pgx.Tx) error {
		var from, fromEmail string
		if strings.TrimSpace(req.SeedFrom) != "" {
			var err error
			if from, fromEmail, err = showcaseSource(ctx, tx, "seedFrom", req.SeedFrom); err != nil {
				return err
			}
		}
		err := tx.QueryRow(ctx, "insert into users (email, name, password_hash, role, credit_limit) values ($1, $2, $3, $4, $5) returning id",
			email, name, hash, role, limit).Scan(&uid)
		if pg := (*pgconn.PgError)(nil); errors.As(err, &pg) && pg.Code == "23505" {
			return &app.Conflict{Msg: msgExists}
		}
		if err != nil {
			return err
		}
		detail := map[string]any{"role": role, "creditLimit": limit, "passwordGenerated": generated}
		if _, err := profile.EnsureTx(ctx, tx, uid, true); err != nil {
			return err
		}
		if from != "" {
			s, err := copyShowcase(ctx, tx, from, uid)
			if err != nil {
				return err
			}
			seeded, detail["seededFrom"], detail["seeded"] = &s, fromEmail, s
		}
		return record(ctx, tx, admin, "user.create", uid, email, detail)
	})
	if err != nil {
		app.WriteError(w, err)
		return
	}
	extra := map[string]any{}
	if generated {
		extra["password"] = password // shown once; never stored in clear or logged
	}
	if seeded != nil {
		extra["seeded"] = seeded
	}
	h.reply(w, r, http.StatusCreated, uid, extra)
}

// target is the locked row of the account an admin acts on.
type target struct {
	email, name, role string
	disabled          bool
	limit             *int
}

// lockTarget locks the account for the rest of tx; a missing one is a 404.
// FOR NO KEY UPDATE (here and in keepAdmin) still serializes admin changes but
// lets FK checks (KEY SHARE) through: paper orders lock the paper account and
// then touch the user row through their trade insert's FK, so a stronger lock
// here deadlocks against them.
func lockTarget(ctx context.Context, tx pgx.Tx, id string) (target, error) {
	var t target
	err := tx.QueryRow(ctx, "select email, name, role, disabled_at is not null, credit_limit from users where id = $1 for no key update",
		id).Scan(&t.email, &t.name, &t.role, &t.disabled, &t.limit)
	return t, err
}

// keepAdmin guards a demote, disable or delete of target by caller. It locks
// the active admin rows in id order (so concurrent changes queue instead of
// deadlocking), then requires that an active admin besides target remains and
// that the caller is still one. Two admins demoting each other at once: the
// second waits for the first, then sees itself as the last admin and gets a 409.
func keepAdmin(ctx context.Context, tx pgx.Tx, caller, target string) error {
	rows, err := tx.Query(ctx, "select id::text from users where role = 'admin' and disabled_at is null order by id for no key update")
	if err != nil {
		return err
	}
	admins, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	admins = slices.DeleteFunc(admins, func(id string) bool { return id == target })
	if len(admins) == 0 {
		return &app.Conflict{Msg: msgLastAdmin}
	}
	if !slices.Contains(admins, caller) {
		return app.ErrForbidden
	}
	return nil
}

func (h *handler) update(w http.ResponseWriter, r *http.Request) {
	uid, err := id(r)
	if err != nil {
		app.WriteError(w, err)
		return
	}
	var req struct {
		Name        *string         `json:"name"`
		Role        *string         `json:"role"`
		CreditLimit json.RawMessage `json:"creditLimit"`
		Disabled    *bool           `json:"disabled"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	var name, role string
	var nameErr, roleErr error
	if req.Name != nil {
		name, nameErr = validName(*req.Name)
	}
	if req.Role != nil {
		role, roleErr = validRole(req.Role)
	}
	limit, limitSet, limitErr := creditLimit(req.CreditLimit)
	if err := cmp.Or(nameErr, roleErr, limitErr); err != nil {
		app.WriteError(w, err)
		return
	}
	if req.Name == nil && req.Role == nil && !limitSet && req.Disabled == nil {
		app.WriteError(w, &app.BadRequest{Msg: "Give at least one of name, role, creditLimit or disabled."})
		return
	}
	ctx, admin := r.Context(), httpx.UserID(r.Context())
	removes := role == "user" || (req.Disabled != nil && *req.Disabled) // could take an admin away
	if removes && uid == admin {
		app.WriteError(w, &app.Conflict{Msg: msgSelf})
		return
	}
	err = pgx.BeginFunc(ctx, h.d.DB, func(tx pgx.Tx) error {
		if req.Disabled != nil && *req.Disabled {
			if err := autopilot.LockTx(ctx, tx); err != nil {
				return err
			}
		}
		if removes {
			if err := keepAdmin(ctx, tx, admin, uid); err != nil {
				return err
			}
		}
		t, err := lockTarget(ctx, tx, uid)
		if err != nil {
			return err
		}
		changes := map[string]any{}
		next := t
		if req.Name != nil && name != t.name {
			changes["name"], next.name = fromTo(t.name, name), name
		}
		if req.Role != nil && role != t.role {
			changes["role"], next.role = fromTo(t.role, role), role
		}
		if limitSet && !equalLimit(limit, t.limit) {
			changes["creditLimit"], next.limit = fromTo(t.limit, limit), limit
		}
		if len(changes) > 0 {
			if _, err := tx.Exec(ctx, "update users set name = $2, role = $3, credit_limit = $4 where id = $1",
				uid, next.name, next.role, next.limit); err != nil {
				return err
			}
			if err := record(ctx, tx, admin, "user.update", uid, t.email, changes); err != nil {
				return err
			}
		}
		if req.Disabled == nil || *req.Disabled == t.disabled {
			return nil
		}
		if !*req.Disabled {
			if _, err := tx.Exec(ctx, "update users set disabled_at = null where id = $1", uid); err != nil {
				return err
			}
			return record(ctx, tx, admin, "user.enable", uid, t.email, nil)
		}
		if _, err := tx.Exec(ctx, "update users set disabled_at = now() where id = $1", uid); err != nil {
			return err
		}
		revoked, err := revokeSessions(ctx, tx, uid, nil)
		if err != nil {
			return err
		}
		stopped, err := autopilot.StopForDisabled(ctx, tx, uid) // no more paper trades for a disabled account
		if err != nil {
			return err
		}
		copies, err := arena.StopCopiesForDisabled(ctx, tx, uid)
		if err != nil {
			return err
		}
		return record(ctx, tx, admin, "user.disable", uid, t.email, map[string]any{"sessionsRevoked": revoked, "autopilotsStopped": stopped, "copyPlansStopped": copies})
	})
	if err != nil {
		app.WriteError(w, err)
		return
	}
	h.reply(w, r, http.StatusOK, uid, nil)
}

func fromTo(from, to any) map[string]any { return map[string]any{"from": from, "to": to} }

func equalLimit(a, b *int) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }

func (h *handler) remove(w http.ResponseWriter, r *http.Request) {
	uid, err := id(r)
	if err != nil {
		app.WriteError(w, err)
		return
	}
	ctx, admin := r.Context(), httpx.UserID(r.Context())
	if uid == admin {
		app.WriteError(w, &app.Conflict{Msg: msgSelf})
		return
	}
	err = pgx.BeginFunc(ctx, h.d.DB, func(tx pgx.Tx) error {
		// Advisory → user → account, shared with deploy, copy delivery and disable.
		if err := autopilot.LockTx(ctx, tx); err != nil {
			return err
		}
		if err := keepAdmin(ctx, tx, admin, uid); err != nil {
			return err
		}
		t, err := lockTarget(ctx, tx, uid)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "select 1 from accounts where user_id=$1 for update", uid); err != nil {
			return err
		}
		if err := record(ctx, tx, admin, "user.delete", uid, t.email, map[string]any{"name": t.name, "role": t.role}); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "delete from users where id = $1", uid) // cascades to everything the account owns
		return err
	})
	if err != nil {
		app.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) password(w http.ResponseWriter, r *http.Request) {
	uid, err := id(r)
	if err != nil {
		app.WriteError(w, err)
		return
	}
	var req struct {
		Password *string `json:"password"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	password, generated, err := newPassword(req.Password)
	if err != nil {
		app.WriteError(w, err)
		return
	}
	hash, err := auth.HashPassword(password) // before the transaction: bcrypt is slow, row locks should not be
	if err != nil {
		app.WriteError(w, err)
		return
	}
	ctx, admin := r.Context(), httpx.UserID(r.Context())
	keep, _ := auth.TokenHash(r)
	err = pgx.BeginFunc(ctx, h.d.DB, func(tx pgx.Tx) error {
		t, err := lockTarget(ctx, tx, uid)
		if err != nil {
			return err
		}
		if slices.Contains(h.d.Managed, t.email) {
			return &app.Conflict{Msg: msgManaged}
		}
		if _, err := tx.Exec(ctx, "update users set password_hash = $2 where id = $1", uid, hash); err != nil {
			return err
		}
		revoked, err := revokeSessions(ctx, tx, uid, keep)
		if err != nil {
			return err
		}
		return record(ctx, tx, admin, "user.password", uid, t.email, map[string]any{"generated": generated, "sessionsRevoked": revoked})
	})
	if err != nil {
		app.WriteError(w, err)
		return
	}
	extra := map[string]any{}
	if generated {
		extra["password"] = password
	}
	h.reply(w, r, http.StatusOK, uid, extra)
}

// revokeSessions deletes the account's sessions except keep (the caller's
// current one, so resetting your own password keeps you signed in here).
func revokeSessions(ctx context.Context, tx pgx.Tx, uid string, keep []byte) (int64, error) {
	tag, err := tx.Exec(ctx, "delete from sessions where user_id = $1 and token_hash is distinct from $2", uid, keep)
	return tag.RowsAffected(), err
}

func (h *handler) revoke(w http.ResponseWriter, r *http.Request) {
	var revoked int64
	h.mutate(w, r, func(ctx context.Context, tx pgx.Tx, uid string, t target) (string, map[string]any, error) {
		keep, _ := auth.TokenHash(r)
		var err error
		revoked, err = revokeSessions(ctx, tx, uid, keep)
		return "user.sessions_revoke", map[string]any{"sessionsRevoked": revoked}, err
	}, func(uid string) map[string]any { return map[string]any{"revoked": revoked} })
}

func (h *handler) resetPortfolio(w http.ResponseWriter, r *http.Request) {
	h.mutate(w, r, func(ctx context.Context, tx pgx.Tx, uid string, t target) (string, map[string]any, error) {
		return "user.portfolio_reset", nil, paper.ResetTx(ctx, tx, uid)
	}, func(uid string) map[string]any { return map[string]any{"portfolio": h.portfolio(r.Context(), uid)} })
}

func (h *handler) resetCredits(w http.ResponseWriter, r *http.Request) {
	h.mutate(w, r, func(ctx context.Context, tx pgx.Tx, uid string, t target) (string, map[string]any, error) {
		day := time.Now().UTC().Truncate(24 * time.Hour)
		var before int
		err := tx.QueryRow(ctx, "select credits from ai_usage where user_id = $1 and day = $2 for update", uid, day).Scan(&before)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", nil, err
		}
		_, err = tx.Exec(ctx, "update ai_usage set credits = 0 where user_id = $1 and day = $2", uid, day)
		return "user.credits_reset", map[string]any{"creditsBefore": before}, err
	}, nil)
}

// mutate runs a single-action change on {id} in a transaction with its audit
// row, then replies {user, …extra()}.
func (h *handler) mutate(w http.ResponseWriter, r *http.Request,
	change func(ctx context.Context, tx pgx.Tx, uid string, t target) (string, map[string]any, error),
	extra func(uid string) map[string]any) {
	uid, err := id(r)
	if err != nil {
		app.WriteError(w, err)
		return
	}
	ctx, admin := r.Context(), httpx.UserID(r.Context())
	err = pgx.BeginFunc(ctx, h.d.DB, func(tx pgx.Tx) error {
		t, err := lockTarget(ctx, tx, uid)
		if err != nil {
			return err
		}
		action, detail, err := change(ctx, tx, uid, t)
		if err != nil {
			return err
		}
		return record(ctx, tx, admin, action, uid, t.email, detail)
	})
	if err != nil {
		app.WriteError(w, err)
		return
	}
	var more map[string]any
	if extra != nil {
		more = extra(uid)
	}
	h.reply(w, r, http.StatusOK, uid, more)
}

// portfolio is GET /api/portfolio for the account, or nil without market data.
func (h *handler) portfolio(ctx context.Context, uid string) *paper.Portfolio {
	p, err := h.d.Paper.Portfolio(ctx, uid)
	if err != nil {
		if !errors.Is(err, market.ErrUpstream) {
			slog.Error("admin: portfolio", "user", uid, "err", err)
		}
		return nil
	}
	return &p
}

// record writes one audit row in tx, snapshotting the acting admin's email
// (an admin whose account is gone cannot act).
func record(ctx context.Context, tx pgx.Tx, admin, action, targetID, targetEmail string, detail map[string]any) error {
	if detail == nil {
		detail = map[string]any{}
	}
	err := tx.QueryRow(ctx, `insert into admin_audit (admin_id, admin_email, action, target_user_id, target_email, detail)
		select id, email, $2, $3, $4, $5 from users where id = $1 returning id`,
		admin, action, targetID, targetEmail, detail).Scan(new(string))
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ErrForbidden
	}
	return err
}

const auditSQL = `select id, action, admin_id, admin_email, target_user_id, coalesce(target_email, ''), detail, created_at from admin_audit`

func scanAudit(row pgx.CollectableRow) (AuditEntry, error) {
	var e AuditEntry
	var target Ref
	var targetID *string
	err := row.Scan(&e.ID, &e.Action, &e.Admin.ID, &e.Admin.Email, &targetID, &target.Email, &e.Detail, &e.CreatedAt)
	if targetID != nil {
		target.ID, e.Target = *targetID, &target
	}
	return e, err
}

func (h *handler) audit(w http.ResponseWriter, r *http.Request) {
	page, limit, err := paging(r.URL.Query(), 50)
	if err != nil {
		app.WriteError(w, err)
		return
	}
	ctx := r.Context()
	var total int
	if err := h.d.DB.QueryRow(ctx, "select count(*) from admin_audit").Scan(&total); err != nil {
		app.WriteError(w, err)
		return
	}
	rows, err := h.d.DB.Query(ctx, auditSQL+" order by created_at desc, id desc limit $1 offset $2", limit, min(page-1, 1<<40)*limit)
	if err != nil {
		app.WriteError(w, err)
		return
	}
	entries, err := pgx.CollectRows(rows, scanAudit)
	if err != nil {
		app.WriteError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"entries": entries, "total": total, "page": page, "limit": limit})
}

// validEmail: trimmed, lower-cased, 3..254 chars, one bare address whose domain has a dot.
func validEmail(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	a, err := mail.ParseAddress(s)
	_, domain, _ := strings.Cut(s, "@")
	if len(s) < 3 || len(s) > 254 || err != nil || a.Address != s || !strings.Contains(domain, ".") {
		return "", &app.BadRequest{Msg: "email must be a valid email address."}
	}
	return s, nil
}

func validName(s string) (string, error) {
	s = strings.TrimSpace(s)
	if n := utf8.RuneCountInString(s); n < 1 || n > 80 || strings.ContainsRune(s, 0) { // Postgres rejects NUL
		return "", &app.BadRequest{Msg: "name must be 1..80 characters."}
	}
	return s, nil
}

func validRole(s *string) (string, error) {
	if *s != "user" && *s != "admin" {
		return "", &app.BadRequest{Msg: "role must be user or admin."}
	}
	return *s, nil
}

// creditLimit reads an optional creditLimit: absent → set false; null → nil
// (the default); else an integer 0..100000.
func creditLimit(raw json.RawMessage) (limit *int, set bool, err error) {
	if raw == nil {
		return nil, false, nil
	}
	if json.Unmarshal(raw, &limit) != nil || (limit != nil && (*limit < 0 || *limit > 100_000)) {
		return nil, true, &app.BadRequest{Msg: "creditLimit must be an integer 0..100000 or null."}
	}
	return limit, true, nil
}

// newPassword checks a supplied password or generates one: 26 base32
// characters (128 bits) from crypto/rand.
func newPassword(p *string) (password string, generated bool, err error) {
	if p == nil {
		return rand.Text(), true, nil
	}
	if len(*p) < 12 || len(*p) > 72 || strings.TrimSpace(*p) == "" {
		return "", false, &app.BadRequest{Msg: "password must be 12..72 bytes and not only spaces."}
	}
	return *p, false, nil
}

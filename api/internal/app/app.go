// Package app is what feature packages receive: the shared dependencies and
// one error writer that maps package errors to the contract's error codes.
package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vincenttanjaya/Foretrade/api/internal/aiconfig"
	"github.com/vincenttanjaya/Foretrade/api/internal/auth"
	"github.com/vincenttanjaya/Foretrade/api/internal/httpx"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/notify"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
	"github.com/vincenttanjaya/Foretrade/api/internal/research"
	"github.com/vincenttanjaya/Foretrade/api/internal/usage"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo"
)

type Deps struct {
	Tasks    *Tasks // process admission and completion; nil for standalone package callers
	DB       *pgxpool.Pool
	Market   *market.Client
	LLM      *llm.Client
	Usage    *usage.Meter
	Paper    *paper.Engine
	Auth     *auth.Service
	Research *research.Service // news/web search, page reading, stored wire, market brief
	Yahoo    yahoo.Source      // keyless Yahoo Finance charts, quotes and FX; read it through YahooSource
	Settings *aiconfig.Service // the AI provider and search keys the admin console sets; nil in package tests

	// Admin console system status and bootstrap accounts.
	Version   string    // 12-char commit (main.commit, else vcs.revision) or "dev"
	StartedAt time.Time // this process
	Managed   []string  // lower-cased DEMO_EMAIL / ADMIN_EMAIL: their passwords come from the config

	AdminOrigins []string // ADMIN_ORIGINS: the only browser origins /api/admin/* serves

	PolymarketBase string // POLYMARKET_BASE: the Gamma API the forecast desk compares against; "" → no market odds

	// The forecast desk's research stage (round 8). The zero values keep the
	// one-shot stage, so package tests that build Deps by hand are unchanged.
	ForecastDeepResearch bool // FORECAST_RESEARCH_MODE != quick: the multi-round deep research stage
	ForecastSearches     int  // FORECAST_RESEARCH_SEARCHES: deep research's per-run Brave call cap; 0 → 18
	// ForecastMethods (FORECAST_METHODS != off, round 9b): ordered numeric
	// questions get per-frame methods, tools, the pool and the guard. The zero
	// value keeps every question on round 8b's roles and prompts.
	ForecastMethods bool

	LiveArena bool // LIVE_ARENA: the shared live paper desk runs in this process (off = the board reads paused)

	// Notification channels (contract "Notification channels"): bell items also
	// sent by email and to each user's Lark bot. Mailer nil → email is off; Lark
	// nil → a default client; AppURL is the web app's origin for the links.
	Mailer notify.Mailer
	Lark   *notify.Lark
	AppURL string
}

// AIOff reports that d's model client has no provider configured: background
// jobs that would call the model skip their turn quietly. Without a client
// (package tests) it is false, so callers behave as before.
func (d *Deps) AIOff() bool { return d != nil && d.LLM != nil && !d.LLM.Configured() }

// YahooSource is d.Yahoo, or yahoo.Disabled (every call unavailable) when
// none is wired, so callers need no nil check.
func (d *Deps) YahooSource() yahoo.Source {
	if d == nil || d.Yahoo == nil {
		return yahoo.Disabled
	}
	return d.Yahoo
}

var (
	ErrNotFound  = errors.New("not found")
	ErrForbidden = errors.New("forbidden") // the caller is no longer an active admin
)

// Cleanup bounds accepted persistence after cancellation, retaining a shorter
// cleanup deadline passed by the owner.
func Cleanup(ctx context.Context) (context.Context, context.CancelFunc) {
	return usage.CleanupContext(ctx)
}

func Rollback(ctx context.Context, tx pgx.Tx) {
	ctx, cancel := Cleanup(ctx)
	defer cancel()
	tx.Rollback(ctx)
}

// BadRequest is a 400 whose Msg is shown to the user.
type BadRequest struct{ Msg string }

func (e *BadRequest) Error() string { return e.Msg }

// Conflict is a 409: the resource is not in a state that allows the request.
// Msg is shown to the user.
type Conflict struct{ Msg string }

func (e *Conflict) Error() string { return e.Msg }

// WriteError writes err as the contract's error body. Unknown errors are
// logged and become a generic 500; a request the client cancelled is only a
// debug line and a 499.
func WriteError(w http.ResponseWriter, err error) {
	var bad *BadRequest
	var conflict *Conflict
	var invalid *paper.ValidationError
	var halted *paper.HaltedError
	switch {
	case errors.As(err, &bad):
		httpx.Error(w, http.StatusBadRequest, "bad_request", bad.Msg)
	case errors.As(err, &conflict):
		httpx.Error(w, http.StatusConflict, "conflict", conflict.Msg)
	case errors.As(err, &invalid):
		httpx.Error(w, http.StatusBadRequest, "bad_request", invalid.Msg)
	case errors.As(err, &halted):
		httpx.Error(w, http.StatusConflict, "conflict", halted.Error())
	case errors.Is(err, usage.ErrOwnerOnly):
		httpx.Error(w, http.StatusForbidden, "ai_owner_only", "AI is in owner-only mode on this server.")
	case errors.Is(err, usage.ErrQuota):
		httpx.Error(w, http.StatusTooManyRequests, "ai_quota", "You have used today's AI credits. They reset at 00:00 UTC.")
	case errors.Is(err, paper.ErrInsufficient):
		httpx.Error(w, http.StatusBadRequest, "insufficient_buying_power", "Not enough buying power: gross exposure may not exceed 2× equity.")
	case errors.Is(err, ErrForbidden):
		httpx.Error(w, http.StatusForbidden, "forbidden", "Admins only.")
	case errors.Is(err, ErrNotFound), errors.Is(err, pgx.ErrNoRows), errors.Is(err, paper.ErrNoPosition), yahoo.IsNotFound(err):
		httpx.Error(w, http.StatusNotFound, "not_found", "Not found.")
	case errors.Is(err, context.Canceled):
		slog.Debug("request canceled", "err", err)
		w.WriteHeader(httpx.StatusClientClosed)
	case errors.Is(err, market.ErrUpstream), errors.Is(err, yahoo.ErrUnavailable):
		slog.Warn("upstream market data", "err", err)
		httpx.Error(w, http.StatusBadGateway, "upstream", "Market data is unavailable right now. Try again shortly.")
	case errors.Is(err, llm.ErrUpstream):
		slog.Warn("upstream ai", "err", err)
		httpx.Error(w, http.StatusBadGateway, "upstream", "The AI model is unavailable right now. Try again shortly.")
	default:
		slog.Error("request failed", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "internal", "Something went wrong.")
	}
}

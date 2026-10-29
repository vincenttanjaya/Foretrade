// Package usage meters AI credits per user per UTC day. The meter doubles as
// the cap on model spend, so credits are spent before a model call.
package usage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrQuota = errors.New("ai_quota")

// ErrOwnerOnly refuses a non-admin while OwnerOnly holds. It wraps ErrQuota,
// so a caller that knows only ErrQuota still refuses (and refunds); one that
// explains or pauses on spent credits checks ErrOwnerOnly first.
var ErrOwnerOnly = fmt.Errorf("%w: ai is in owner-only mode", ErrQuota)

const (
	CostChat      = 2
	CostCopilot   = 1
	CostDraft     = 1
	CostForecast  = 5
	CostMaterial  = 1 // each file newly attached to a forecast run (read once); refunded when the desk cannot read it
	CostStrategy  = 2
	CostPortfolio = 2
)

type Credits struct {
	Used     int       `json:"used"`
	Limit    int       `json:"limit"`
	ResetsAt time.Time `json:"resetsAt"`
}

type Meter struct {
	db    *pgxpool.Pool
	limit int // default daily limit; a user's credit_limit overrides it

	// OwnerOnly, when set and true, keeps the AI to admins: the ChatGPT plan
	// behind it is bound to the one person who signed in.
	OwnerOnly func() bool
}

func NewMeter(db *pgxpool.Pool, dailyLimit int) *Meter {
	return &Meter{db: db, limit: dailyLimit}
}

// Spend adds cost to today's usage only if the result stays within the
// user's limit (their credit_limit, else the default), in one statement, so
// concurrent requests cannot overspend and a limit change applies to the next call.
func (m *Meter) Spend(ctx context.Context, userID string, cost int) (Credits, error) {
	return m.spend(ctx, m.db, userID, cost, today())
}

// SpendTx debits the explicit charged UTC day inside the caller's transaction.
// It never commits; claim, debit and the caller's durable state roll back together.
func (m *Meter) SpendTx(ctx context.Context, tx pgx.Tx, userID string, cost int, day time.Time) (Credits, error) {
	return m.spend(ctx, tx, userID, cost, utcDay(day))
}

type meterQueries interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func (m *Meter) spend(ctx context.Context, q meterQueries, userID string, cost int, day time.Time) (Credits, error) {
	if m.OwnerOnly != nil && m.OwnerOnly() {
		var admin bool
		if err := q.QueryRow(ctx, `select role = 'admin' from users where id = $1::uuid`, userID).Scan(&admin); err != nil {
			return Credits{}, err
		}
		if !admin {
			c, err := m.get(ctx, q, userID, day)
			if err != nil {
				return c, err
			}
			return c, ErrOwnerOnly
		}
	}
	var used, limit int
	err := q.QueryRow(ctx, `
		with lim as (select coalesce((select credit_limit from users where id = $1::uuid), $4::int) as n)
		insert into ai_usage (user_id, day, credits)
		select $1::uuid, $2::date, $3::int from lim where $3::int <= lim.n
		on conflict (user_id, day) do update set credits = ai_usage.credits + excluded.credits
		where ai_usage.credits + excluded.credits <= (select n from lim)
		returning credits, (select n from lim)`, userID, day, cost, m.limit).Scan(&used, &limit)
	if errors.Is(err, pgx.ErrNoRows) {
		c, err := m.get(ctx, q, userID, day)
		if err != nil {
			return c, err
		}
		return c, ErrQuota
	}
	if err != nil {
		return Credits{}, err
	}
	return credits(used, limit, day), nil
}

// Refund gives credits back when the model call failed outright. Best effort.
// ponytail: a refund straddling UTC midnight lands on the new day (floored at 0).
func (m *Meter) Refund(ctx context.Context, userID string, cost int) {
	m.RefundOn(ctx, userID, today(), cost)
}

// RefundOn gives credits back to the UTC day they were spent on, for work
// that can outlive the day it was charged (an arena run).
func (m *Meter) RefundOn(ctx context.Context, userID string, day time.Time, cost int) {
	ctx, cancel := CleanupContext(ctx)
	defer cancel()
	if err := refundOn(ctx, m.db, userID, day, cost); err != nil {
		slog.Error("usage refund failed", "user", userID, "err", err)
	}
}

// RefundTx credits the original UTC day in the caller's terminal transaction.
// The caller must condition that transaction on its durable refunded flag; this
// helper preserves errors and cancellation and does not independently commit.
func (m *Meter) RefundTx(ctx context.Context, tx pgx.Tx, userID string, day time.Time, cost int) error {
	return refundOn(ctx, tx, userID, utcDay(day), cost)
}

func refundOn(ctx context.Context, q meterQueries, userID string, day time.Time, cost int) error {
	_, err := q.Exec(ctx, `update ai_usage set credits = greatest(credits - $3, 0) where user_id = $1 and day = $2`, userID, day, cost)
	return err
}

type cleanupKey struct{}

// CleanupContext shields cancellation for an accepted write, with a fresh
// ten-second ceiling independent of the operation that ended. Once cleanup
// owns the lifetime, nested writes/refunds keep its shorter or spent deadline,
// even across another cancellation shield.
func CleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	deadline := time.Now().Add(10 * time.Second)
	if owned, ok := ctx.Value(cleanupKey{}).(time.Time); ok {
		if owned.Before(deadline) {
			deadline = owned
		}
		if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
			deadline = d
		}
	}
	ctx, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
	return context.WithValue(ctx, cleanupKey{}, deadline), cancel
}

// Get is today's usage against the user's limit.
func (m *Meter) Get(ctx context.Context, userID string) (Credits, error) {
	return m.get(ctx, m.db, userID, today())
}

func (m *Meter) get(ctx context.Context, q meterQueries, userID string, day time.Time) (Credits, error) {
	var used, limit int
	err := q.QueryRow(ctx, `
		select coalesce((select credits from ai_usage where user_id = $1 and day = $2), 0),
			coalesce((select credit_limit from users where id = $1), $3)`, userID, day, m.limit).Scan(&used, &limit)
	if err != nil {
		return Credits{}, err
	}
	return credits(used, limit, day), nil
}

// DefaultLimit is AI_DAILY_CREDITS, the limit of users without their own.
func (m *Meter) DefaultLimit() int { return m.limit }

func credits(used, limit int, day time.Time) Credits {
	return Credits{Used: used, Limit: limit, ResetsAt: day.AddDate(0, 0, 1)}
}

// today is the current UTC date at midnight.
func today() time.Time {
	return utcDay(time.Now())
}

func utcDay(day time.Time) time.Time { return day.UTC().Truncate(24 * time.Hour) }

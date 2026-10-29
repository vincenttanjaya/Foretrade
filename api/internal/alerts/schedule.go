package alerts

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
)

// nextRun is the first instant strictly after `after` at atMinute (minutes
// after 00:00 UTC) every day, Monday to Friday for every "weekdays", or on
// weekday (0 = Sunday) for every "week".
func nextRun(every string, atMinute, weekday int, after time.Time) time.Time {
	after = after.UTC()
	t := time.Date(after.Year(), after.Month(), after.Day(), 0, atMinute, 0, 0, time.UTC)
	step := 1
	if every == "week" {
		step = 7
		t = t.AddDate(0, 0, (weekday-int(t.Weekday())+7)%7)
	}
	if !t.After(after) {
		t = t.AddDate(0, 0, step)
	}
	for every == "weekdays" && (t.Weekday() == time.Saturday || t.Weekday() == time.Sunday) {
		t = t.AddDate(0, 0, 1)
	}
	return t
}

// atMinute reads a validated "HH:MM" as minutes after midnight.
func atMinute(hhmm string) int {
	t, _ := time.Parse("15:04", hhmm)
	return t.Hour()*60 + t.Minute()
}

// FirstRun is a validated schedule's first run: the first matching instant strictly after now.
func FirstRun(in Input) time.Time {
	wd := 0
	if in.Weekday != nil {
		wd = *in.Weekday
	}
	return nextRun(in.Every, atMinute(in.At), wd, time.Now())
}

// ScheduleLabel is a schedule as words: "daily 08:00 UTC", "weekdays 08:00 UTC" or "Mondays 09:00 UTC".
func ScheduleLabel(every, at string, weekday *int) string {
	switch {
	case every == "week" && weekday != nil:
		return fmt.Sprintf("%ss %s UTC", time.Weekday(*weekday), at)
	case every == "weekdays":
		return "weekdays " + at + " UTC"
	}
	return "daily " + at + " UTC"
}

// dollars is an order size: "$100", "$1,000", "$250.50".
func dollars(x float64) string { return strings.TrimSuffix(paper.Money(x), ".00") }

// runSchedules runs each due schedule of an enabled account once: the claim
// moves next_run_at on in one update, so concurrent checks never run it twice.
// ponytail: runs missed while the API was down are not replayed; a late check runs once, then the next slot.
func runSchedules(ctx context.Context, d *app.Deps) error {
	type due struct {
		id, every   string
		at, weekday int
		next        time.Time
	}
	rows, _ := d.DB.Query(ctx, `select a.id::text, a.every, a.at_minute, coalesce(a.weekday, 0), a.next_run_at
		from alerts a join users u on u.id = a.user_id
		where a.trigger = 'schedule' and a.action<>'research' and a.status = 'active' and a.next_run_at <= now() and u.disabled_at is null`)
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (due, error) {
		var s due
		return s, r.Scan(&s.id, &s.every, &s.at, &s.weekday, &s.next)
	})
	if err != nil {
		return err
	}
	for _, s := range list {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var userID string
		a, err := scan(d.DB.QueryRow(ctx, `update alerts set next_run_at = $3, fired_at = now()
			where id = $1 and next_run_at = $2 and action<>'research' and status = 'active' returning `+columns+`, user_id::text`,
			s.id, s.next, nextRun(s.every, s.at, s.weekday, time.Now())), &userID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue // claimed elsewhere or cancelled meanwhile
		}
		if err == nil {
			err = runOnce(context.WithoutCancel(ctx), d, a, userID)
		}
		if err != nil {
			slog.Error("schedule run", "id", s.id, "err", err)
		}
	}
	return nil
}

// runOnce posts a claimed schedule's brief, or places its paper order, and logs the run.
func runOnce(ctx context.Context, d *app.Deps, a Alert, userID string) error {
	if a.Action == "research" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	logRun := func(status, body string, tradeID, msg *string) error {
		_, err := d.DB.Exec(ctx, `insert into alert_runs (alert_id, user_id, status, body, trade_id, error) values ($1, $2, $3, $4, $5, $6)`,
			a.ID, userID, status, body, tradeID, msg)
		return err
	}
	if a.Action == "notify" {
		return logRun("ok", brief(ctx, d, a.Symbol), nil, nil)
	}
	t, _, err := d.Paper.Order(ctx, userID, paper.OrderRequest{Symbol: a.Symbol, Side: a.Action, Notional: a.Notional, Source: "alert",
		Note: fmt.Sprintf("Workflow: %s %s %s %s", a.Action, dollars(*a.Notional), a.Symbol, ScheduleLabel(*a.Every, *a.At, a.Weekday)), RefID: &a.ID})
	if err != nil {
		msg := orderError(a.ID, err)
		return logRun("failed", "", nil, &msg)
	}
	verb := map[string]string{"buy": "Bought", "sell": "Sold"}[a.Action]
	return logRun("ok", fmt.Sprintf("%s %s %s at %s", verb, dollars(*a.Notional), a.Symbol, paper.Money(t.Price)), &t.ID, nil)
}

// brief is a schedule's deterministic brief, no model call: "BTC $61,240.50
// (+1.2% 24h)", then the stored wire's sentiment (a brief under 48 h old)
// and top headline (48 h) when there are any.
func brief(ctx context.Context, d *app.Deps, symbol string) string {
	body := symbol + " price unavailable right now"
	if assets, err := d.Market.Assets(ctx); err == nil {
		for _, x := range assets {
			if x.Symbol == symbol {
				body = fmt.Sprintf("%s %s (%+.1f%% 24h)", symbol, paper.Money(x.Price), x.Change24hPct)
			}
		}
	}
	if b, err := d.Research.LatestBrief(ctx); err != nil {
		slog.Warn("schedule brief: market brief", "err", err)
	} else if b != nil && time.Since(b.CreatedAt) < 48*time.Hour {
		for _, s := range b.Sentiment {
			if s.Symbol == symbol {
				body += fmt.Sprintf(" · wire sentiment %s: %s", s.Label, clip(s.Reason, 140))
			}
		}
	}
	if h, err := d.Research.Headlines(ctx, symbol, 48*time.Hour, 1); err != nil {
		slog.Warn("schedule brief: headlines", "err", err)
	} else if len(h) > 0 {
		body += " · Top headline: " + clip(h[0].Title, 120)
	}
	return body
}

package store

import (
	"context"
	"crypto/rand"
	"errors"
	"io/fs"
	neturl "net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// 0032 keeps every automation stored before it (price, order, schedule,
// research and odds rows in every status) and admits a drawdown row only in
// its own shape: a resolved high with its source, sell_pct exactly for a
// sale, no order fields, and an outcome exactly when fired.
func TestDrawdownMigrationKeepsAlerts(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL_TEST")
	if dsn == "" {
		t.Skip("DATABASE_URL_TEST not set")
	}
	ctx := t.Context()
	admin, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := "drawdown_mig_" + strings.ToLower(rand.Text())
	if _, err := admin.Exec(ctx, "create schema "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Exec(context.Background(), "drop schema "+schema+" cascade") })
	u, err := neturl.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	pool, err := Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	// Every earlier migration, as a database deployed before 0032 has them (production at 0031).
	files, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "create table schema_migrations (version text primary key, applied_at timestamptz not null default now())"); err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		version := strings.TrimSuffix(strings.TrimPrefix(file, "migrations/"), ".sql")
		if version >= "0032_drawdown_alerts" {
			continue
		}
		body, err := migrations.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(body)); err != nil {
			t.Fatalf("%s: %v", version, err)
		}
		if _, err := pool.Exec(ctx, "insert into schema_migrations (version) values ($1)", version); err != nil {
			t.Fatal(err)
		}
	}
	var user, forecast string
	if err := pool.QueryRow(ctx, "insert into users (email, name, password_hash) values ('dd@example.test', 'T', 'x') returning id").Scan(&user); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `insert into forecasts (user_id, shape, question, outcomes, resolution_date, status)
		values ($1, 'binary', 'Will SOL close above $250 on Dec 31?', '["Yes","No"]', '2026-12-31', 'completed') returning id`, user).Scan(&forecast); err != nil {
		t.Fatal(err)
	}
	for _, row := range []string{
		`(user_id, symbol, op, price) values ($1, 'SOL', 'below', 150)`,
		`(user_id, symbol, op, price, action, notional, status, fired_at, fired_price) values ($1, 'BTC', 'below', 90000, 'buy', 500, 'fired', now(), 89000)`,
		`(user_id, symbol, trigger, every, at_minute, next_run_at) values ($1, 'ETH', 'schedule', 'day', 480, now())`,
		`(user_id, symbol, trigger, every, at_minute, next_run_at, action, notional, status) values ($1, 'ETH', 'schedule', 'weekdays', 60, now(), 'sell', 100, 'cancelled')`,
		`(user_id, symbol, trigger, every, at_minute, next_run_at, action, prompt, notify_when, status) values ($1, '', 'schedule', 'day', 60, now(), 'research', 'Research the market today', 'always', 'paused')`,
		`(user_id, symbol, trigger, forecast_id, op, price) values ($1, '', 'odds', $2, 'above', 0.4)`,
	} {
		args := []any{user}
		if strings.Contains(row, "$2") {
			args = append(args, forecast)
		}
		if _, err := pool.Exec(ctx, "insert into alerts "+row, args...); err != nil {
			t.Fatalf("seed %s: %v", row, err)
		}
	}

	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var n, empty int
	if err := pool.QueryRow(ctx, `select count(*), count(*) filter (where drawdown_bps is null and sell_pct is null and ath is null and ath_source is null
		and ath_live is null and outcome is null) from alerts`).Scan(&n, &empty); err != nil || n != 6 || empty != 6 {
		t.Fatalf("kept %d alerts, %d without drawdown fields: %v", n, empty, err)
	}

	const cols = `(user_id, symbol, trigger, action, drawdown_bps, sell_pct, ath, ath_at, ath_from, ath_source, ath_ticker, ath_currency, ath_live, notional, status, outcome)`
	valid := []any{user, "SOL", "drawdown", "sell", 2000, 100, 295.83, "2025-01-19", "2020-08-11", "binance", "SOLUSDT", "USD", false, nil, "active", nil}
	insert := func(change func(v []any)) error {
		v := append([]any(nil), valid...)
		change(v)
		_, err := pool.Exec(ctx, "insert into alerts "+cols+" values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)", v...)
		return err
	}
	if err := insert(func([]any) {}); err != nil {
		t.Fatalf("a valid drawdown row: %v", err)
	}
	if err := insert(func(v []any) { v[3], v[5] = "notify", nil }); err != nil {
		t.Fatalf("a notify drawdown row: %v", err)
	}
	if err := insert(func(v []any) { v[14], v[15] = "fired", "sold" }); err != nil {
		t.Fatalf("a fired drawdown row: %v", err)
	}
	for name, change := range map[string]func(v []any){
		"no high":             func(v []any) { v[6] = nil },
		"no source":           func(v []any) { v[9] = nil },
		"unknown source":      func(v []any) { v[9] = "guess" },
		"bps out of range":    func(v []any) { v[4] = 10000 },
		"sell without a pct":  func(v []any) { v[5] = nil },
		"notify with a pct":   func(v []any) { v[3] = "notify" },
		"a notional":          func(v []any) { v[13] = 100.0 },
		"a buy":               func(v []any) { v[3] = "buy" },
		"fired, no outcome":   func(v []any) { v[14] = "fired" },
		"active with outcome": func(v []any) { v[15] = "notified" },
		"no symbol":           func(v []any) { v[1] = "" },
	} {
		var pg *pgconn.PgError
		if err := insert(change); !errors.As(err, &pg) || pg.Code != "23514" {
			t.Errorf("%s: %v, want a check violation", name, err)
		}
	}
	// A price alert cannot carry drawdown fields.
	if _, err := pool.Exec(ctx, "insert into alerts (user_id, symbol, op, price, ath) values ($1, 'SOL', 'below', 150, 200)", user); err == nil {
		t.Fatal("a price alert took a high")
	}
}

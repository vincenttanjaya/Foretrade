package store

import (
	"context"
	"crypto/rand"
	"io/fs"
	neturl "net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// mainRelease is the last migration the deployed database has run: the files
// up to it are main's release, and every file after it is new to production.
const mainRelease = "0031_forecast_materials"

// A database the deployed release migrated (through 0031) takes this branch's
// migrator to the exact schema a fresh database gets: 0032 to 0039 run after
// everything it already has, in name order, and nothing it holds is rerun or lost.
func TestMigrateUpgradesDeployedRelease(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL_TEST")
	if dsn == "" {
		t.Skip("DATABASE_URL_TEST not set")
	}
	ctx := t.Context()
	deployed, fresh := schemaPool(t, dsn, "release_up_"), schemaPool(t, dsn, "release_fresh_")

	files, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	release := fstest.MapFS{}
	var added []string
	for _, file := range files {
		version := strings.TrimSuffix(strings.TrimPrefix(file, "migrations/"), ".sql")
		if version > mainRelease {
			added = append(added, version)
			continue
		}
		body, err := migrations.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		release[file] = &fstest.MapFile{Data: body}
	}
	if !slices.Equal(added, []string{"0032_drawdown_alerts", "0033_asia_screener", "0034_signals_refresh", "0035_signal_picks", "0036_screener_run_calls", "0037_notification_channels", "0038_alert_currency", "0039_ai_settings"}) {
		t.Fatalf("migrations after %s: %v", mainRelease, added)
	}
	if err := migrate(ctx, deployed, release); err != nil {
		t.Fatalf("deployed release: %v", err)
	}
	if got := ledger(t, deployed); len(got) != len(files)-len(added) || got[len(got)-1] != mainRelease {
		t.Fatalf("deployed ledger %v", got)
	}
	var user, alert string
	if err := deployed.QueryRow(ctx, "insert into users (email, name, password_hash) values ('up@example.test', 'U', 'x') returning id").Scan(&user); err != nil {
		t.Fatal(err)
	}
	if err := deployed.QueryRow(ctx, "insert into alerts (user_id, symbol, op, price) values ($1, 'SOL', 'below', 150) returning id", user).Scan(&alert); err != nil {
		t.Fatal(err)
	}
	if _, err := deployed.Exec(ctx, `insert into forecasts (user_id, shape, question, outcomes, resolution_date, status, materials)
		values ($1, 'binary', 'Will SOL close above $250 on Dec 31?', '["Yes","No"]', '2026-12-31', 'completed', '[{"name":"memo.pdf"}]')`, user); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ { // the upgrade, then a rerun that changes nothing
		if err := Migrate(ctx, deployed); err != nil {
			t.Fatalf("upgrade %d: %v", i, err)
		}
	}
	if err := Migrate(ctx, fresh); err != nil {
		t.Fatal(err)
	}

	// Applied after the release's last file, 0032 to 0039 in order.
	rows, err := deployed.Query(ctx, "select version from schema_migrations order by applied_at, version")
	if err != nil {
		t.Fatal(err)
	}
	order, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(order) != len(files) || !slices.Equal(order[len(order)-9:], []string{mainRelease, "0032_drawdown_alerts", "0033_asia_screener", "0034_signals_refresh", "0035_signal_picks", "0036_screener_run_calls", "0037_notification_channels", "0038_alert_currency", "0039_ai_settings"}) {
		t.Fatalf("applied order %v %v", order, err)
	}
	if got, want := ledger(t, deployed), ledger(t, fresh); !slices.Equal(got, want) {
		t.Fatalf("upgraded ledger %v, fresh %v", got, want)
	}
	for what, query := range map[string]string{
		"tables": "select table_name from information_schema.tables where table_schema = current_schema() order by 1",
		"columns": `select table_name || '.' || column_name || ' ' || data_type || ' ' || is_nullable || ' ' || coalesce(column_default, '')
			from information_schema.columns where table_schema = current_schema() order by 1`,
		"constraints": `select cl.relname || ' ' || con.conname || ' ' || pg_get_constraintdef(con.oid) from pg_constraint con
			join pg_class cl on cl.oid = con.conrelid where cl.relnamespace = current_schema()::regnamespace order by 1`,
		"indexes": "select indexname || ' ' || replace(indexdef, current_schema() || '.', '') from pg_indexes where schemaname = current_schema() order by 1",
	} {
		got, want := catalog(t, deployed, query), catalog(t, fresh, query)
		if !slices.Equal(got, want) {
			t.Errorf("%s differ:\nupgraded %v\nfresh    %v", what, got, want)
		}
		if what == "tables" && (!slices.Contains(got, "screener_runs") || !slices.Contains(got, "screener_rows")) {
			t.Errorf("tables %v", got)
		}
	}
	var symbol, materials string
	if err := deployed.QueryRow(ctx, `select a.symbol, f.materials::text from alerts a join forecasts f on f.user_id = a.user_id
		where a.id = $1 and a.drawdown_bps is null`, alert).Scan(&symbol, &materials); err != nil ||
		symbol != "SOL" || materials != `[{"name": "memo.pdf"}]` {
		t.Fatalf("kept rows: %q %q %v", symbol, materials, err)
	}
}

// schemaPool is a pool on a throwaway schema of dsn, dropped after the test.
func schemaPool(t *testing.T, dsn, prefix string) *pgxpool.Pool {
	t.Helper()
	ctx := t.Context()
	admin, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := prefix + strings.ToLower(rand.Text())
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
	return pool
}

func ledger(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	return catalog(t, pool, "select version from schema_migrations order by version")
}

func catalog(t *testing.T, pool *pgxpool.Pool, query string) []string {
	t.Helper()
	rows, err := pool.Query(t.Context(), query)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return out
}

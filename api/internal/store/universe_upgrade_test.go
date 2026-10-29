package store

import (
	"context"
	"crypto/rand"
	"io/fs"
	neturl "net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Preservation characterization: all immutable pre-0023 SQL is applied to a
// temporary schema in the assigned test database, with real owned records.
// Production Migrate then upgrades/reruns; no new Universe migration is needed.
func TestMigrateUpgradeUniverseCompatibility(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL_TEST")
	if dsn == "" {
		t.Skip("DATABASE_URL_TEST not set")
	}
	ctx := context.Background()
	admin, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := "universe_upgrade_" + strings.ToLower(rand.Text())
	if _, err := admin.Exec(ctx, "create schema "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(context.Background(), "drop schema "+pgx.Identifier{schema}.Sanitize()+" cascade")
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
	defer pool.Close()
	files, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 39 || !strings.HasSuffix(files[21], "0022_strategy_community.sql") {
		t.Fatalf("immutable full-stem inventory %v", files)
	}
	if _, err := pool.Exec(ctx, "create table schema_migrations (version text primary key, applied_at timestamptz not null default now())"); err != nil {
		t.Fatal(err)
	}
	for _, file := range files[:22] {
		body, err := migrations.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, string(body)); err == nil {
			_, err = tx.Exec(ctx, "insert into schema_migrations(version) values ($1)", strings.TrimSuffix(strings.TrimPrefix(file, "migrations/"), ".sql"))
		}
		if err != nil {
			tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var uid, id string
	if err := pool.QueryRow(ctx, "insert into users(email,name,password_hash) values ('universe-upgrade@example.test','Owned prior record','not-a-login') returning id").Scan(&uid); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `insert into ai_portfolios(user_id,name,thesis,prompt,holdings,backtest) values ($1,'Prior basket','Saved','Prior',
 '[{"symbol":"AAPL","weight":0.6},{"symbol":"BTC","weight":0.4}]','{"returnPct":5,"benchmarkReturnPct":3,"series":[{"t":86400,"v":100},{"t":172800,"v":105}]}') returning id`, uid).Scan(&id); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		"insert into accounts(user_id,cash,starting_balance,revision) values ($1,99900,100000,3)",
		"insert into positions(user_id,symbol,qty,avg_price) values ($1,'AAPL',-1,332)",
		"insert into trades(user_id,symbol,side,qty,price,notional,fee,realized_pnl,source,note) values ($1,'AAPL','sell',1,332,332,0.332,-0.332,'portfolio','Owned prior fill')",
		"insert into forecasts(user_id,shape,question,outcomes,resolution_date,status) values ($1,'binary','Owned prior forecast','[\"Yes\",\"No\"]','2099-01-01','completed')",
	} {
		if _, err := pool.Exec(ctx, sql, uid); err != nil {
			t.Fatal(err)
		}
	}
	type ledgerRow struct {
		Version string
		At      time.Time
	}
	ledger := func() []ledgerRow {
		t.Helper()
		rows, err := pool.Query(ctx, "select version,applied_at from schema_migrations order by version")
		if err != nil {
			t.Fatal(err)
		}
		out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[ledgerRow])
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	snapshot := func() []string {
		t.Helper()
		out := []string{}
		for _, table := range []string{"users", "accounts", "positions", "trades", "forecasts", "ai_portfolios"} {
			projection := "to_jsonb(r)"
			if table == "forecasts" {
				projection += " - 'linked_market' - 'materials'"
			} // new fields (nullable, or defaulted to []); every prior field stays exact
			if table == "users" {
				projection += " - 'notify_email' - 'notify_email_since' - 'notify_lark' - 'notify_lark_since' - 'lark_webhook' - 'lark_secret'"
			} // 0037's notification channels: off, null, or now() at the upgrade
			rows, err := pool.Query(ctx, "select ("+projection+")::text from "+pgx.Identifier{table}.Sanitize()+" r order by ("+projection+")::text")
			if err != nil {
				t.Fatal(err)
			}
			got, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, got...)
		}
		return out
	}
	before, owned := ledger(), snapshot()
	var wg sync.WaitGroup
	errs := make([]error, 3)
	for i := range errs {
		wg.Go(func() { errs[i] = Migrate(ctx, pool) })
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	after := ledger()
	if len(after) != 39 {
		t.Fatalf("full final ledger %v", after)
	}
	for _, row := range before {
		found := false
		for _, next := range after {
			if row.Version == next.Version {
				found = true
				if !row.At.Equal(next.At) {
					t.Fatalf("historical timestamp changed %s", row.Version)
				}
			}
		}
		if !found {
			t.Fatalf("historical stem disappeared %s", row.Version)
		}
	}
	if got := snapshot(); !reflect.DeepEqual(got, owned) {
		t.Fatalf("upgrade changed signed positions/ledger/owner/static JSON: before=%v after=%v", owned, got)
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, ledger()) || !reflect.DeepEqual(owned, snapshot()) {
		t.Fatal("rerun changed full-stem ledger timestamps or owned records")
	}
	t.Logf("prior22 → exact39 full stems, concurrent upgrade3, rerun; owned legacy basket %s and signed paper rows preserved", id)
}

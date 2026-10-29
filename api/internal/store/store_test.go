package store

import (
	"context"
	"crypto/rand"
	neturl "net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestOpenErrors(t *testing.T) {
	ctx := context.Background()
	for url, want := range map[string]string{
		"":               "database url is empty",
		"postgres://%zz": "open db",
		"postgres://nobody@127.0.0.1:1/x?sslmode=disable&connect_timeout=2": "ping db", // nothing listens on port 1
	} {
		if _, err := Open(ctx, url); err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("Open(%q) = %v, want %q…", url, err, want)
		}
	}
}

// Migrate builds the whole schema from nothing, once, even when several
// boots race, and a re-run is a no-op. It runs in a throwaway schema.
func TestMigrateFreshSchema(t *testing.T) {
	url := os.Getenv("DATABASE_URL_TEST")
	if url == "" {
		t.Skip("DATABASE_URL_TEST not set")
	}
	ctx := context.Background()
	admin, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close) // after the drop below: cleanups run last in, first out
	schema := "migrate_" + strings.ToLower(rand.Text())
	if _, err := admin.Exec(ctx, "create schema "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Exec(context.Background(), "drop schema "+schema+" cascade") })

	u, err := neturl.Parse(url)
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
	var wg sync.WaitGroup
	errs := make([]error, 3)
	for i := range errs {
		wg.Go(func() { errs[i] = Migrate(ctx, pool) })
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("concurrent migrate: %v", err)
		}
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("re-run: %v", err)
	}

	rows, _ := pool.Query(ctx, "select version from schema_migrations order by version")
	versions, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || !slices.Equal(versions, []string{"0001_init", "0002_news", "0003_arena", "0004_admin", "0005_account_revision", "0006_trade_seq",
		"0007_trade_proposals", "0008_forecast_track_record", "0009_autopilot", "0010_signals", "0011_trade_refs", "0012_forecast_context",
		"0013_alerts", "0014_forecast_updates", "0015_forecast_archives", "0016_showcase_seed", "0017_workflows", "0018_market_positions", "0019_forecast_progress", "0020_weekday_schedules", "0021_odds_alerts", "0022_strategy_community", "0023_forecast_linked_market", "0023_live_arena", "0024_direct_market_alerts", "0024_live_arena_delivery", "0025_chat_images", "0026_user_profiles", "0027_scheduled_ai_research", "0030_news_regions", "0031_forecast_materials", "0032_drawdown_alerts", "0033_asia_screener", "0034_signals_refresh", "0035_signal_picks", "0036_screener_run_calls", "0037_notification_channels", "0038_alert_currency", "0039_ai_settings"}) {
		t.Fatalf("versions %v %v", versions, err)
	}
	rows, _ = pool.Query(ctx, "select table_name from information_schema.tables where table_schema = $1 order by 1", schema)
	tables, err := pgx.CollectRows(rows, pgx.RowTo[string])
	want := []string{"accounts", "admin_audit", "ai_portfolios", "ai_settings", "ai_usage", "alert_runs", "alerts", "arena_runs", "arena_steps", "autopilots", "chat_messages", "chats", "copy_deliveries", "copy_plans", "forecasts", "live_agents", "live_attempts", "live_bars", "live_desk", "live_steps",
		"market_briefs", "market_positions", "news_items", "notification_deliveries", "positions", "profile_memory_receipts", "research_worker_slots", "schema_migrations", "screener_rows", "screener_runs", "sessions", "signal_boards", "signal_ledger_sync", "signal_picks", "signals_refresh", "strategies", "strategy_stars", "trades", "user_profiles", "users"}
	if err != nil || !slices.Equal(tables, want) {
		t.Fatalf("tables %v %v", tables, err)
	}

	// Apply the archive migration to a legacy table with existing records;
	// the temporary table shadows the real one within this transaction.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `create temporary table forecasts (status text, result jsonb) on commit drop;
		insert into forecasts values ('archived', '{"summary":"saved"}'), ('archived', null), ('running', null)`); err != nil {
		t.Fatal(err)
	}
	archiveMigration, err := migrations.ReadFile("migrations/0015_forecast_archives.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, string(archiveMigration)); err != nil {
		t.Fatal(err)
	}
	var preserved, failed, active int
	if err := tx.QueryRow(ctx, `select
		count(*) filter (where status = 'archived' and result is not null and archived_from_status = 'completed'),
		count(*) filter (where status = 'archived' and result is null and archived_from_status = 'failed'),
		count(*) filter (where status = 'running' and archived_from_status is null) from forecasts`).Scan(&preserved, &failed, &active); err != nil ||
		preserved != 1 || failed != 1 || active != 1 {
		t.Fatalf("legacy archive migration: completed=%d failed=%d active=%d err=%v", preserved, failed, active, err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := Migrate(canceled, pool); err == nil {
		t.Fatal("a canceled migrate must fail")
	}
}

// Upgrade a pre-image history with real saved messages; rerunning must retain both rows and ledger identities.
func TestImageMetadataUpgrade(t *testing.T) {
	url := os.Getenv("DATABASE_URL_TEST")
	if url == "" {
		t.Skip("DATABASE_URL_TEST not set")
	}
	ctx := t.Context()
	admin, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := "image_upgrade_" + strings.ToLower(rand.Text())
	if _, err := admin.Exec(ctx, "create schema "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Exec(context.Background(), "drop schema "+schema+" cascade") })
	u, err := neturl.Parse(url)
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
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "alter table chat_messages drop column attachments; delete from schema_migrations where version='0025_chat_images'"); err != nil {
		t.Fatal(err)
	}
	var user, chat, msg string
	if err := pool.QueryRow(ctx, "insert into users(email,name,password_hash) values('image@test','Image','hash') returning id").Scan(&user); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "insert into chats(user_id,title) values($1,'Legacy') returning id", user).Scan(&chat); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "insert into chat_messages(chat_id,role,content,cards) values($1,'user','Saved before images','[]') returning id", chat).Scan(&msg); err != nil {
		t.Fatal(err)
	}
	var before int
	if err := pool.QueryRow(ctx, "select count(*) from schema_migrations").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before != 38 {
		t.Fatalf("prior full-stem inventory %d", before)
	}
	for i := 0; i < 2; i++ {
		if err := Migrate(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	var content, attachments string
	if err := pool.QueryRow(ctx, "select content,attachments::text from chat_messages where id=$1", msg).Scan(&content, &attachments); err != nil {
		t.Fatal(err)
	}
	if content != "Saved before images" || attachments != "[]" {
		t.Fatalf("upgrade changed history: %q %q", content, attachments)
	}
	var after, known int
	if err := pool.QueryRow(ctx, `select count(*),count(*) filter(where version in ('0023_forecast_linked_market','0023_live_arena','0024_direct_market_alerts','0024_live_arena_delivery','0025_chat_images')) from schema_migrations`).Scan(&after, &known); err != nil {
		t.Fatal(err)
	}
	if after != 39 || known != 5 {
		t.Fatalf("upgrade/rerun stems: %d %d", after, known)
	}
}

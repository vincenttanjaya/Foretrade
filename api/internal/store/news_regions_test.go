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
)

// 0030 labels the wire stored before it: rows from the core crypto searches
// (topic asset) or tagged with a crypto asset are crypto, rows from the US
// macro searches (every macro row) or tagged with a US stock are us, and rows
// tagged with an Asian stock east_asia; sorted, never null, GIN-indexed.
func TestNewsRegionsBackfill(t *testing.T) {
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
	schema := "news_regions_" + strings.ToLower(rand.Text())
	if _, err := admin.Exec(ctx, "create schema "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Exec(context.Background(), "drop schema "+schema+" cascade") }) // t.Context() is done by now
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

	// Every earlier migration, as a database deployed before 0030 has them.
	files, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "create table schema_migrations (version text primary key, applied_at timestamptz not null default now())"); err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		version := strings.TrimSuffix(strings.TrimPrefix(file, "migrations/"), ".sql")
		if version >= "0030_news_regions" {
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
	if _, err := pool.Exec(ctx, `insert into news_items (url, title, source, symbols, topic) values
		('https://a.test/btc', 'Bitcoin ETF inflows', 'x', '{BTC}', 'asset'),
		('https://a.test/staking', 'Staking yields rise', 'x', '{}', 'asset'),
		('https://a.test/fed', 'Fed holds rates', 'x', '{}', 'macro'),
		('https://a.test/fed-btc', 'Fed holds; bitcoin dips', 'x', '{BTC}', 'macro'),
		('https://a.test/nvda', 'Nvidia shares lead the S&P 500', 'x', '{NVDA}', 'macro'),
		('https://a.test/nvda-asset', 'Render rallies with Nvidia shares', 'x', '{NVDA,RENDER}', 'asset'),
		('https://a.test/tsm', 'TSMC shares and bitcoin climb', 'x', '{BTC,TSM}', 'asset'),
		('https://a.test/tsm-macro', 'TSMC shares lift the Nasdaq', 'x', '{TSM}', 'macro')`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "insert into news_items (url, title, source) values ('https://a.test/new', 'New', 'x')"); err != nil {
		t.Fatal(err)
	}
	for url, want := range map[string][]string{
		"https://a.test/btc": {"crypto"}, "https://a.test/staking": {"crypto"}, "https://a.test/fed": {"us"},
		"https://a.test/fed-btc": {"crypto", "us"}, "https://a.test/nvda": {"us"}, "https://a.test/nvda-asset": {"crypto", "us"},
		"https://a.test/tsm": {"crypto", "east_asia"}, "https://a.test/tsm-macro": {"east_asia", "us"},
		"https://a.test/new": {}, // the default until ingestion labels it
	} {
		var got []string
		if err := pool.QueryRow(ctx, "select regions from news_items where url = $1", url).Scan(&got); err != nil || got == nil || !slices.Equal(got, want) {
			t.Errorf("%s: regions %#v %v, want %v", url, got, err, want)
		}
	}
	var index string
	if err := pool.QueryRow(ctx, "select indexdef from pg_indexes where schemaname = $1 and indexname = 'news_items_regions'", schema).Scan(&index); err != nil ||
		!strings.Contains(index, "USING gin (regions)") {
		t.Errorf("regions index %q %v", index, err)
	}
}

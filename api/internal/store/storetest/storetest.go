// Package storetest gives DB tests a migrated pool on DATABASE_URL_TEST (or a
// schema of their own) and a throwaway user. Tests skip when the variable is unset.
package storetest

import (
	"context"
	"crypto/rand"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vincenttanjaya/Foretrade/api/internal/store"
)

func Pool(t testing.TB) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("DATABASE_URL_TEST")
	if url == "" {
		t.Skip("DATABASE_URL_TEST not set")
	}
	ctx := context.Background()
	pool, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := store.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool
}

// Schema is a pool on a freshly migrated schema of the test's own, dropped
// on cleanup. Tests that write tables every user shares (news_items,
// market_briefs) use it, so packages running in parallel cannot see each
// other's rows.
func Schema(t testing.TB) *pgxpool.Pool {
	t.Helper()
	admin := Pool(t)
	ctx := context.Background()
	name := "test_" + strings.ToLower(rand.Text())
	if _, err := admin.Exec(ctx, "create schema "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Exec(context.Background(), "drop schema "+name+" cascade") })
	u, err := url.Parse(os.Getenv("DATABASE_URL_TEST"))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", name)
	u.RawQuery = q.Encode()
	pool, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close) // runs before the drop: cleanups are last in, first out
	if err := store.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool
}

// User inserts a user with a unique email and deletes it (cascading) on cleanup.
func User(t testing.TB, pool *pgxpool.Pool) string {
	t.Helper()
	var id string
	email := "test-" + strings.ToLower(rand.Text()) + "@example.test"
	if err := pool.QueryRow(context.Background(),
		"insert into users (email, name, password_hash) values ($1, 'Test', 'x') returning id",
		email).Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Exec(context.Background(), "delete from users where id = $1", id) })
	return id
}

package storetest

import (
	"context"
	"strings"
	"testing"
)

func TestPoolAndUser(t *testing.T) {
	pool := Pool(t)
	var id string
	t.Run("user", func(t *testing.T) {
		id = User(t, pool)
		var email string
		if err := pool.QueryRow(context.Background(), "select email from users where id = $1", id).Scan(&email); err != nil || email == "" {
			t.Fatalf("user row: %q %v", email, err)
		}
	})
	var n int
	if err := pool.QueryRow(context.Background(), "select count(*) from users where id = $1", id).Scan(&n); err != nil || n != 0 {
		t.Fatalf("the user must be deleted when its test ends: %d %v", n, err)
	}
}

// A test schema holds the whole schema, sees none of the shared rows and is
// gone after the test.
func TestSchema(t *testing.T) {
	ctx := context.Background()
	shared := Pool(t)
	var name string
	t.Run("schema", func(t *testing.T) {
		pool := Schema(t)
		if err := pool.QueryRow(ctx, "select current_schema()").Scan(&name); err != nil || !strings.HasPrefix(name, "test_") {
			t.Fatalf("schema %q %v", name, err)
		}
		if _, err := pool.Exec(ctx, "insert into news_items (url, title, source) values ('https://isolated.example', 't', 's')"); err != nil {
			t.Fatal(err)
		}
		var n int
		shared.QueryRow(ctx, "select count(*) from news_items where url = 'https://isolated.example'").Scan(&n)
		if n != 0 {
			t.Fatal("a test schema's rows leaked into the shared schema")
		}
		User(t, pool)
	})
	var n int
	if err := shared.QueryRow(ctx, "select count(*) from information_schema.schemata where schema_name = $1", name).Scan(&n); err != nil || n != 0 {
		t.Fatalf("schema %s must be dropped after its test: %d %v", name, n, err)
	}
}

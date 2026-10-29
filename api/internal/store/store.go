// Package store opens the Postgres pool and applies the embedded migrations.
package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

// migrateLock is an arbitrary advisory-lock key shared by everything that migrates.
const migrateLock = 7_241_070

func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	if url == "" {
		return nil, fmt.Errorf("database url is empty")
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}
	return pool, nil
}

// Migrate applies each migrations/*.sql file once, in name order, each in its
// own transaction, recording it in schema_migrations.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error { return migrate(ctx, pool, migrations) }

// migrate is Migrate over any set of migrations/*.sql files (tests replay a
// release's subset to stand up the schema a deployed database has).
func migrate(ctx context.Context, pool *pgxpool.Pool, set fs.FS) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	defer conn.Release()
	// Serializes concurrent boots and parallel test packages on one database.
	if _, err := conn.Exec(ctx, "select pg_advisory_lock($1)", migrateLock); err != nil {
		return fmt.Errorf("migrate: lock: %w", err)
	}
	defer conn.Exec(context.WithoutCancel(ctx), "select pg_advisory_unlock($1)", migrateLock)

	if _, err := conn.Exec(ctx, `create table if not exists schema_migrations (
		version text primary key, applied_at timestamptz not null default now())`); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	files, err := fs.Glob(set, "migrations/*.sql") // sorted
	if err != nil {
		return err
	}
	for _, file := range files {
		version := strings.TrimSuffix(strings.TrimPrefix(file, "migrations/"), ".sql")
		var applied bool
		if err := conn.QueryRow(ctx, "select exists(select 1 from schema_migrations where version = $1)",
			version).Scan(&applied); err != nil {
			return fmt.Errorf("migrate %s: %w", version, err)
		}
		if applied {
			continue
		}
		body, err := fs.ReadFile(set, file)
		if err != nil {
			return err
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return fmt.Errorf("migrate %s: %w", version, err)
		}
		// Exec without arguments uses the simple protocol, so a file may hold many statements.
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("migrate %s: %w", version, err)
		}
		if _, err := tx.Exec(ctx, "insert into schema_migrations (version) values ($1)", version); err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("migrate %s: %w", version, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("migrate %s: %w", version, err)
		}
	}
	return nil
}

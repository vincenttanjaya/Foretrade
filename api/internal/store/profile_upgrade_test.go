package store

import (
	"context"
	"io/fs"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/vincenttanjaya/Foretrade/api/internal/profile"
)

func TestProfileMigrationUpgradeUnionAndRerun(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL_TEST")
	if dsn == "" {
		t.Skip("DATABASE_URL_TEST not set")
	}
	for _, mode := range []string{"original22", "live-only", "prediction-only", "current27", "current28"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			admin, err := Open(ctx, dsn)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(admin.Close)
			name := "profile_upgrade_" + strings.ReplaceAll(uuid.NewV4().String(), "-", "")
			if _, err = admin.Exec(ctx, "create schema "+name); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				clean, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				admin.Exec(clean, "drop schema "+name+" cascade")
			})
			u, err := url.Parse(dsn)
			if err != nil {
				t.Fatal(err)
			}
			q := u.Query()
			q.Set("search_path", name)
			u.RawQuery = q.Encode()
			db, err := Open(ctx, u.String())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(db.Close)
			files, err := fs.Glob(migrations, "migrations/*.sql")
			if err != nil || len(files) != 39 {
				t.Fatalf("full stems: %v %v", files, err)
			}
			if _, err = db.Exec(ctx, "create table schema_migrations(version text primary key,applied_at timestamptz not null default now())"); err != nil {
				t.Fatal(err)
			}
			for i, file := range files[:28] {
				apply := i < 22 || (mode == "current27" && i < 27) || mode == "current28" || (mode == "live-only" && strings.Contains(file, "live_arena")) || (mode == "prediction-only" && (strings.Contains(file, "forecast_linked_market") || strings.Contains(file, "direct_market_alerts")))
				if !apply {
					continue
				}
				body, e := migrations.ReadFile(file)
				if e != nil {
					t.Fatal(e)
				}
				if err = pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
					if _, e := tx.Exec(ctx, string(body)); e != nil {
						return e
					}
					_, e := tx.Exec(ctx, "insert into schema_migrations(version) values($1)", strings.TrimSuffix(strings.TrimPrefix(file, "migrations/"), ".sql"))
					return e
				}); err != nil {
					t.Fatalf("prior stem %s: %v", file, err)
				}
			}
			var owner, demo string
			if err = db.QueryRow(ctx, "insert into users(email,name,password_hash) values('legacy-owner@example.test','Preserved owner','old-hash') returning id").Scan(&owner); err != nil {
				t.Fatal(err)
			}
			if err = db.QueryRow(ctx, "insert into users(email,name,password_hash) values('demo-owner@example.test','Preserved demo','demo-hash') returning id").Scan(&demo); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(ctx, "insert into accounts(user_id,cash) values($1,98765.43)", owner); err != nil {
				t.Fatal(err)
			}
			type ledgerRow struct {
				Version string
				At      time.Time
			}
			ledger := func() []ledgerRow {
				t.Helper()
				rows, e := db.Query(ctx, "select version,applied_at from schema_migrations order by version")
				if e != nil {
					t.Fatal(e)
				}
				values, e := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ledgerRow, error) {
					var v ledgerRow
					e := row.Scan(&v.Version, &v.At)
					return v, e
				})
				if e != nil {
					t.Fatal(e)
				}
				return values
			}
			var oldProfile profile.Profile
			if mode == "current28" {
				oldProfile, err = profile.Ensure(ctx, db, owner, true)
				if err != nil {
					t.Fatal(err)
				}
				prefs := oldProfile.Preferences
				prefs.CustomPrompt = "Private choice before Research"
				prefs.Enabled = false
				oldProfile, err = profile.Update(ctx, db, owner, prefs, oldProfile.Revision)
				if err != nil {
					t.Fatal(err)
				}
			}
			before := ledger()
			if err = Migrate(ctx, db); err != nil {
				t.Fatal(err)
			}
			after := ledger()
			var slotCount int
			if err = db.QueryRow(ctx, "select count(*) from research_worker_slots").Scan(&slotCount); err != nil || slotCount != 2 {
				t.Fatalf("exact Research slots %d %v", slotCount, err)
			}
			if mode == "current28" {
				got, e := profile.Load(ctx, db, owner)
				if e != nil || !reflect.DeepEqual(got, oldProfile) {
					t.Fatalf("Research changed persisted Profile: %+v %+v %v", got, oldProfile, e)
				}
			}
			if len(after) != 39 {
				t.Fatalf("upgraded stems: %v", after)
			}
			for _, old := range before {
				found := false
				for _, next := range after {
					if old.Version == next.Version {
						found = true
						if !old.At.Equal(next.At) {
							t.Fatalf("changed historical appliedAt: %+v", old)
						}
					}
				}
				if !found {
					t.Fatalf("lost full stem %s", old.Version)
				}
			}
			p, err := profile.Ensure(ctx, db, owner, true)
			if err != nil || (mode != "current28" && !p.Enabled) {
				t.Fatalf("legacy normal: %+v %v", p, err)
			}
			prefs := p.Preferences
			prefs.Enabled = false
			prefs.CustomPrompt = "Preserved private choice"
			p, err = profile.Update(ctx, db, owner, prefs, p.Revision)
			if err != nil {
				t.Fatal(err)
			}
			d, err := profile.Ensure(ctx, db, demo, false)
			if err != nil || d.Enabled {
				t.Fatalf("explicit existing demo: %+v %v", d, err)
			}
			dp := d.Preferences
			dp.Enabled = true
			d, err = profile.Update(ctx, db, demo, dp, d.Revision)
			if err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			errs := make([]error, 3)
			for i := range errs {
				wg.Go(func() { errs[i] = Migrate(ctx, db) })
			}
			wg.Wait()
			for _, e := range errs {
				if e != nil {
					t.Fatal(e)
				}
			}
			if !reflect.DeepEqual(after, ledger()) {
				t.Fatal("rerun changed migration ledger")
			}
			current, e := profile.Load(ctx, db, owner)
			if e != nil || !reflect.DeepEqual(current, p) {
				t.Fatalf("rerun changed owned profile: %+v %v", current, e)
			}
			current, e = profile.Ensure(ctx, db, demo, false)
			if e != nil || !reflect.DeepEqual(current, d) {
				t.Fatalf("rerun changed stored demo choice: %+v %v", current, e)
			}
			var cash float64
			var display, password string
			if err = db.QueryRow(ctx, "select cash,name,password_hash from accounts join users on users.id=accounts.user_id where users.id=$1", owner).Scan(&cash, &display, &password); err != nil || cash != 98765.43 || display != "Preserved owner" || password != "old-hash" {
				t.Fatalf("migration changed owned rows: %v %s %s %v", cash, display, password, err)
			}
		})
	}
}

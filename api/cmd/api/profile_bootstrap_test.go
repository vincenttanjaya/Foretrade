package main

import (
	"reflect"
	"testing"
	"uuid"

	"github.com/vincenttanjaya/Foretrade/api/internal/auth"
	"github.com/vincenttanjaya/Foretrade/api/internal/config"
	"github.com/vincenttanjaya/Foretrade/api/internal/profile"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
	"github.com/vincenttanjaya/Foretrade/api/internal/usage"
	"golang.org/x/crypto/bcrypt"
)

func TestProfileBootstrapOnceDemoAndAdmin(t *testing.T) {
	db := storetest.Schema(t)
	service := auth.NewService(db, usage.NewMeter(db, 500), false)
	oldCost := auth.BcryptCost
	auth.BcryptCost = bcrypt.MinCost
	t.Cleanup(func() { auth.BcryptCost = oldCost })
	key := uuid.NewV4().String()
	cfg := config.Config{DemoEmail: "demo-" + key + "@example.test", DemoPassword: "demo-password", AdminEmail: "admin-" + key + "@example.test", AdminPassword: "admin-password"}
	if _, err := ensureAccounts(t.Context(), service, cfg); err != nil {
		t.Fatal(err)
	}
	var demo, admin string
	if err := db.QueryRow(t.Context(), "select id from users where email=$1", cfg.DemoEmail).Scan(&demo); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(t.Context(), "select id from users where email=$1", cfg.AdminEmail).Scan(&admin); err != nil {
		t.Fatal(err)
	}
	dp, err := profile.Load(t.Context(), db, demo)
	if err != nil || dp.Enabled || dp.Revision != 0 {
		t.Fatalf("demo default: %+v %v", dp, err)
	}
	ap, err := profile.Load(t.Context(), db, admin)
	if err != nil || !ap.Enabled || ap.Revision != 0 {
		t.Fatalf("managed admin is normal: %+v %v", ap, err)
	}
	normal := storetest.User(t, db)
	np, err := profile.Load(t.Context(), db, normal)
	if err != nil || !np.Enabled {
		t.Fatalf("normal initial: %+v %v", np, err)
	}
	dprefs := dp.Preferences
	dprefs.Enabled = true
	dprefs.CustomPrompt = "Preserved demo choice"
	dp, err = profile.Update(t.Context(), db, demo, dprefs, dp.Revision)
	if err != nil {
		t.Fatal(err)
	}
	aprefs := ap.Preferences
	aprefs.Enabled = false
	aprefs.Style = new("swing")
	ap, err = profile.Update(t.Context(), db, admin, aprefs, ap.Revision)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		if i == 1 {
			cfg.DemoPassword = "rotated-demo"
			cfg.AdminPassword = "rotated-admin"
		}
		restarted := auth.NewService(db, usage.NewMeter(db, 500), false)
		if _, err = ensureAccounts(t.Context(), restarted, cfg); err != nil {
			t.Fatal(err)
		}
		nextDemo, e := profile.Load(t.Context(), db, demo)
		if e != nil || !reflect.DeepEqual(nextDemo, dp) {
			t.Fatalf("demo boot%d overwrite: %+v %v", i, nextDemo, e)
		}
		nextAdmin, e := profile.Load(t.Context(), db, admin)
		if e != nil || !reflect.DeepEqual(nextAdmin, ap) {
			t.Fatalf("admin boot%d overwrite: %+v %v", i, nextAdmin, e)
		}
	}
}

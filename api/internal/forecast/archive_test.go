package forecast

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
)

// Archive browsing must preserve reports and ownership while ordinary lists
// and AI actions keep treating archived forecasts as inactive.
func TestArchiveBrowseAndRestore(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	ctx := context.Background()
	var id string
	if err := e.DB.QueryRow(ctx, `insert into forecasts
		(user_id, shape, question, outcomes, resolution_date, status, result, completed_at)
		values ($1, 'binary', 'Will the event happen?', '["Yes","No"]', '2099-12-31', 'completed',
		'{"probabilities":[{"outcome":"Yes","p":0.7},{"outcome":"No","p":0.3}],"summary":"Saved report"}', now()) returning id`, e.User).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if w := e.Do("DELETE", "/api/forecasts/"+id, nil); w.Code != 204 {
		t.Fatalf("archive: %d %s", w.Code, w.Body)
	}
	w := e.Do("GET", "/api/forecasts?status=archived", nil)
	list := apptest.JSON[struct{ Forecasts []map[string]any }](t, w).Forecasts
	if w.Code != 200 || len(list) != 1 || list[0]["id"] != id || list[0]["archivedFromStatus"] != "completed" {
		t.Fatalf("archive list: %d %s", w.Code, w.Body)
	}
	w = e.Do("GET", "/api/forecasts/"+id, nil)
	f := apptest.JSON[struct{ Forecast map[string]any }](t, w).Forecast
	if w.Code != 200 || f["status"] != "archived" || f["archivedFromStatus"] != "completed" || !strings.Contains(w.Body.String(), "Saved report") {
		t.Fatalf("archive report: %d %s", w.Code, w.Body)
	}
	if w := e.Do("GET", "/api/forecasts", nil); !strings.Contains(w.Body.String(), `"forecasts":[]`) {
		t.Fatalf("active list contains archive: %s", w.Body)
	}
	before := credits(t, e)
	for _, action := range []string{"update", "resolve"} {
		body := `{}`
		if action == "resolve" {
			body = `{"outcome":"Yes"}`
		}
		if w := e.Do("POST", "/api/forecasts/"+id+"/"+action, body); w.Code != 404 && w.Code != 409 {
			t.Errorf("archived %s: %d %s", action, w.Code, w.Body)
		}
	}
	if credits(t, e) != before {
		t.Fatal("archived actions spent credits")
	}
	other := e.Session(storetest.User(t, e.DB))
	if w := e.DoAs(other, "GET", "/api/forecasts?status=archived", nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"forecasts":[]`) {
		t.Fatalf("other archive list: %d %s", w.Code, w.Body)
	}
	for _, req := range []struct{ method, path string }{{"GET", "/api/forecasts/" + id}, {"POST", "/api/forecasts/" + id + "/restore"}} {
		if w := e.DoAs(other, req.method, req.path, `{}`); w.Code != 404 {
			t.Errorf("foreign %s: %d %s", req.path, w.Code, w.Body)
		}
	}
	if w := e.DoAs(nil, "POST", "/api/forecasts/"+id+"/restore", `{}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("restore without session: %d", w.Code)
	}
	w = e.Do("POST", "/api/forecasts/"+id+"/restore", `{}`)
	f = apptest.JSON[struct{ Forecast map[string]any }](t, w).Forecast
	if w.Code != 200 || f["status"] != "completed" || f["archivedFromStatus"] != nil || !strings.Contains(w.Body.String(), "Saved report") || credits(t, e) != before {
		t.Fatalf("restore: %d %s", w.Code, w.Body)
	}
	if w := e.Do("POST", "/api/forecasts/"+id+"/restore", `{}`); w.Code != 409 {
		t.Fatalf("restore active forecast: %d %s", w.Code, w.Body)
	}
	if w := e.Do("GET", "/api/forecasts?status=archived", nil); !strings.Contains(w.Body.String(), `"forecasts":[]`) {
		t.Fatalf("restored forecast still archived: %s", w.Body)
	}
}

// Restoring an interrupted run must never revive a worker or let its late
// writes replace the failure; a failed run must retain its original error.
func TestRestoreInterruptedAndFailedForecasts(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	h := &handler{e.Deps}
	ctx := context.Background()
	for _, original := range []string{"running", "failed"} {
		t.Run(original, func(t *testing.T) {
			var id string
			if err := e.DB.QueryRow(ctx, `insert into forecasts
				(user_id, shape, question, outcomes, resolution_date, status, error)
				values ($1, 'binary', 'Will the event happen?', '["Yes","No"]', '2099-12-31', $2, $3) returning id`, e.User, original, "Existing upstream failure").Scan(&id); err != nil {
				t.Fatal(err)
			}
			if w := e.Do("DELETE", "/api/forecasts/"+id, nil); w.Code != 204 {
				t.Fatalf("archive: %d %s", w.Code, w.Body)
			}
			// The in-flight worker cannot finish or fail an archived run.
			if err := h.complete(ctx, id, Result{Summary: "Late completion"}); err != nil {
				t.Fatal(err)
			}
			if err := h.fail(ctx, id, "Late failure"); err != nil {
				t.Fatal(err)
			}
			w := e.Do("GET", "/api/forecasts/"+id, nil)
			f := apptest.JSON[struct{ Forecast map[string]any }](t, w).Forecast
			if w.Code != 200 || f["status"] != "archived" || f["archivedFromStatus"] != original {
				t.Fatalf("archived state: %d %s", w.Code, w.Body)
			}
			w = e.Do("POST", "/api/forecasts/"+id+"/restore", `{}`)
			got := apptest.JSON[struct{ Forecast Forecast }](t, w).Forecast
			if w.Code != 200 || got.Status != "failed" || got.Error == nil || got.Result != nil {
				t.Fatalf("restore: %d %s", w.Code, w.Body)
			}
			if original == "running" && !strings.Contains(strings.ToLower(*got.Error), "archiv") || original == "failed" && *got.Error != "Existing upstream failure" {
				t.Fatalf("restored error: %q", *got.Error)
			}
			if err := h.complete(ctx, id, Result{Summary: "Late completion after restore"}); err != nil {
				t.Fatal(err)
			}
			if err := h.fail(ctx, id, "Late failure after restore"); err != nil {
				t.Fatal(err)
			}
			if f := apptest.JSON[struct{ Forecast Forecast }](t, e.Do("GET", "/api/forecasts/"+id, nil)).Forecast; f.Status != "failed" || f.Result != nil || f.Error == nil || *f.Error != *got.Error {
				t.Fatalf("late worker changed restored run: %+v", f)
			}
		})
	}
}

func TestRestoreLegacyArchiveAndReplayProtection(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	ctx := context.Background()
	for _, completed := range []bool{true, false} {
		var id string
		if err := e.DB.QueryRow(ctx, `insert into forecasts
			(user_id, shape, question, outcomes, resolution_date, status, result, completed_at)
			values ($1, 'binary', 'Legacy archive?', '["Yes","No"]', '2099-12-31', 'archived',
			case when $2 then '{"summary":"Legacy result"}'::jsonb end, case when $2 then now() end) returning id`, e.User, completed).Scan(&id); err != nil {
			t.Fatal(err)
		}
		want := "failed"
		if completed {
			want = "completed"
		}
		w := e.Do("POST", "/api/forecasts/"+id+"/restore", `{}`)
		f := apptest.JSON[struct{ Forecast Forecast }](t, w).Forecast
		if w.Code != 200 || f.Status != want || !completed && f.Error == nil {
			t.Fatalf("legacy restore (%t): %d %s", completed, w.Code, w.Body)
		}
	}
	var replay string
	if err := e.DB.QueryRow(ctx, `insert into forecasts (shape, question, outcomes, resolution_date, status, replay_key)
		values ('binary', 'Replay?', '["Yes","No"]', '2020-01-01', 'archived', 'archive-test-replay') returning id`).Scan(&replay); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{replay, "not-an-id", "00000000-0000-0000-0000-000000000000"} {
		if w := e.Do("POST", "/api/forecasts/"+id+"/restore", `{}`); w.Code != 404 {
			t.Errorf("restore unavailable %s: %d %s", id, w.Code, w.Body)
		}
	}
	if w := e.Do("GET", "/api/forecasts/"+replay, nil); w.Code != 404 {
		t.Fatalf("archived replay readable: %d %s", w.Code, w.Body)
	}
	if w := e.Do("GET", "/api/forecasts?scope=replay&status=archived", nil); w.Code != 400 {
		t.Fatalf("archived replay list: %d %s", w.Code, w.Body)
	}
}

func TestArchiveListIncludesSupersededRuns(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	d := Draft{Shape: "binary", Question: "Will the event happen?", Outcomes: []string{"Yes", "No"}, ResolutionDate: "2099-12-31"}
	parent := completed(t, e, e.User, d, 0.4)
	child := completed(t, e, e.User, d, 0.6)
	if _, err := e.DB.Exec(context.Background(), `update forecasts set parent_id = $1 where id = $2`, parent, child); err != nil {
		t.Fatal(err)
	}
	if w := e.Do("DELETE", "/api/forecasts/"+parent, nil); w.Code != 204 {
		t.Fatalf("archive parent: %d %s", w.Code, w.Body)
	}
	for path, want := range map[string]string{"/api/forecasts": child, "/api/forecasts?status=archived": parent} {
		w := e.Do("GET", path, nil)
		items := apptest.JSON[struct{ Forecasts []Summary }](t, w).Forecasts
		if w.Code != 200 || len(items) != 1 || items[0].ID != want {
			t.Errorf("%s: %d %s", path, w.Code, w.Body)
		}
	}
}

// A resolution that read the forecast before archive must recheck its state
// when its write acquires the row lock; archived reports stay immutable.
func TestArchiveWinsAgainstPendingResolution(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"set", `{"outcome":"Yes"}`}, {"clear", `{"outcome":null}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := apptest.New(t, Register, apptest.Options{})
			d := Draft{Shape: "binary", Question: "Will the event happen?", Outcomes: []string{"Yes", "No"}, ResolutionDate: "2099-12-31"}
			id := completed(t, e, e.User, d, 0.4)
			ctx := context.Background()
			if _, err := e.DB.Exec(ctx, `update forecasts set outcome = 'No', resolved_at = now(), resolved_by = 'manual' where id = $1`, id); err != nil {
				t.Fatal(err)
			}
			tx, err := e.DB.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			var blocker int
			if err := tx.QueryRow(ctx, `select pg_backend_pid() from forecasts where id = $1 for update`, id).Scan(&blocker); err != nil {
				t.Fatal(err)
			}
			done := make(chan int, 1)
			go func() {
				done <- e.Do("POST", "/api/forecasts/"+id+"/resolve", tc.body).Code
			}()
			for deadline := time.Now().Add(5 * time.Second); ; {
				var waiting bool
				if err := e.DB.QueryRow(ctx, `select exists (select 1 from pg_stat_activity where $1 = any(pg_blocking_pids(pid)))`, blocker).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("resolution did not wait on the forecast row lock")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if _, err := tx.Exec(ctx, `update forecasts set archived_from_status = status, status = 'archived' where id = $1`, id); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if code := <-done; code != 404 && code != 409 {
				t.Fatalf("resolution after archive: %d", code)
			}
			f := apptest.JSON[struct{ Forecast Forecast }](t, e.Do("GET", "/api/forecasts/"+id, nil)).Forecast
			if f.Status != "archived" || f.Resolution == nil || f.Resolution.Outcome != "No" {
				t.Fatalf("pending resolution changed archived report: %+v", f)
			}
		})
	}
}

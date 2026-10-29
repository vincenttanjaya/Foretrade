package alerts

import (
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
)

// A showcase copy (seeded_from set) never rings the bell, whether completed,
// failed or resolved; the user's own runs, an update of a copy included, do.
func TestFeedSkipsSeededForecasts(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	forecast := func(status, seededFrom, parent string) string {
		t.Helper()
		var id string
		if err := e.DB.QueryRow(ctx, `insert into forecasts (user_id, shape, question, outcomes, resolution_date, status, result, completed_at,
			outcome, resolved_at, seeded_from, parent_id)
			values ($1, 'binary', 'Will SOL close above $130?', '["Yes","No"]', '2026-12-31', $2, '{"probabilities":[{"outcome":"Yes","p":0.39}]}', now(),
				case when $3 <> '' then 'No' end, case when $3 <> '' then now() end, nullif($3, '')::uuid, nullif($4, '')::uuid)
			returning id::text`, e.User, status, seededFrom, parent).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	src := "6f1c2c1e-0000-4000-8000-000000000001" // another account's row; no foreign key
	copied := forecast("completed", src, "")
	forecast("failed", src, "")
	own := forecast("completed", "", copied) // the user's Update of a copy

	items, unread, err := feed(ctx, e.DB, e.User)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "forecast_completed:"+own || items[0].Title != "Forecast ready · Yes 39%" || unread != 1 {
		t.Fatalf("feed: %+v, unread %d", items, unread)
	}
}

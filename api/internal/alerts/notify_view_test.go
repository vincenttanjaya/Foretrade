package alerts

import (
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
)

// An ordered report whose view leads titles the bell by its view, as the
// forecast page states it (gap round G14); a bucket headline keeps its top
// range, and a binary one is unchanged (TestNotifications).
func TestForecastReadyTitlesTheView(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	probs := `[{"outcome":"< $300","p":0.17},{"outcome":"$300 – $450","p":0.17},{"outcome":"$450 – $600","p":0.15},{"outcome":"$600 – $800","p":0.16},{"outcome":"$800 – $1,100","p":0.15},{"outcome":"≥ $1,100","p":0.2}]`
	view := func(headline, basis string) string {
		return `{"probabilities":` + probs + `,"view":{"lead":"x","median":{"value":610.9},"p10":{"value":242,"approx":true},"p25":{"value":363},"p75":{"value":989},
			"p90":{"value":1480,"approx":true},"pAbove":{"value":0.62},"unit":{"prefix":"$","suffix":""},"flat":true,"headline":"` + headline + `","basis":{"kind":"` + basis + `","label":"x"}}}`
	}
	for i, c := range []struct{ result, want string }{
		{view("view", "capped_drift"), "Forecast ready · median $611"},
		{view("view", "none"), "Forecast ready · 80% range ≈$242–≈$1,480"},
		{view("bucket", "capped_drift"), "Forecast ready · ≥ $1,100 20%"},
		{`{"probabilities":` + probs + `}`, "Forecast ready · ≥ $1,100 20%"}, // stored before round 9b: no view to state
	} {
		if _, err := e.DB.Exec(ctx, `insert into forecasts (user_id, shape, question, outcomes, resolution_date, status, result, completed_at)
			values ($1, 'ordered', 'Where will TSM close on 2031-10-08?', '["< $300","$300 – $450","$450 – $600","$600 – $800","$800 – $1,100","≥ $1,100"]',
			'2031-10-08', 'completed', $2::jsonb, $3)`, e.User, c.result, time.Now().Add(-time.Duration(i+1)*time.Minute)); err != nil {
			t.Fatal(err)
		}
		type page struct{ Items []Notification }
		got := apptest.JSON[page](t, e.Do("GET", "/api/notifications", nil))
		if len(got.Items) != i+1 || got.Items[i].Title != c.want || !strings.HasPrefix(got.Items[i].ID, "forecast_completed:") {
			t.Errorf("case %d: %+v", i, got.Items)
		}
	}
}

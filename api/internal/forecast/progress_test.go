package forecast

import (
	"context"
	"strings"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
)

// The tracker writes the trace to a running row only; GET returns it (null
// before the first write), and a nil tracker does nothing.
func TestTracker(t *testing.T) {
	var none *tracker
	none.set(func(*Progress) { t.Fatal("a nil tracker must not run") })

	e := apptest.New(t, Register, apptest.Options{})
	h := &handler{e.Deps}
	ctx := context.Background()
	d := Draft{Shape: "categorical", Question: "Which company is the largest by market cap on 2099-12-31?", Outcomes: []string{"Nvidia", "Apple"},
		ResolutionDate: "2099-12-31"}
	f, err := h.insert(ctx, e.User, d, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	get := func() *Progress {
		t.Helper()
		w := e.Do("GET", "/api/forecasts/"+f.ID, nil)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"progress":`) {
			t.Fatalf("get: %d %s", w.Code, w.Body)
		}
		return apptest.JSON[struct{ Forecast Forecast }](t, w).Forecast.Progress
	}
	if get() != nil {
		t.Fatal("a new run has no trace until its first write")
	}

	tr := newTracker(ctx, e.DB, f.ID, d, true, nil)
	tr.set(func(*Progress) {})
	if w := e.Do("GET", "/api/forecasts/"+f.ID, nil); !strings.Contains(w.Body.String(),
		`"progress":{"research":{"status":"pending","queries":[],"found":0,"read":0,"mode":null,"depth":null,"angles":[],"searches":0,"findings":0,"steps":[]},"specialists":[{"role":"base_rate","status":"pending","top":null},`) ||
		!strings.Contains(w.Body.String(), `"aggregator":{"status":"pending"},"market":{"status":"pending","matched":false},"materials":null,"updatedAt":"`) {
		t.Fatalf("initial trace: %s", w.Body)
	}
	tr.set(func(p *Progress) {
		p.Research.Status, p.Research.Queries = "running", []string{"Nvidia Apple market cap"}
		p.Specialists[2].Status, p.Specialists[2].Top = "done", &OutcomeP{"Nvidia", 0.41}
	})
	p := get()
	if p.Research.Status != "running" || p.Research.Queries[0] != "Nvidia Apple market cap" || len(p.Specialists) != 3 || p.Specialists[2].Role != "skeptic" ||
		*p.Specialists[2].Top != (OutcomeP{"Nvidia", 0.41}) || p.UpdatedAt.IsZero() {
		t.Fatalf("trace: %+v", p)
	}

	// Once the run is stored, late writes are dropped.
	if err := h.complete(ctx, f.ID, Result{}); err != nil {
		t.Fatal(err)
	}
	tr.set(func(p *Progress) { p.Aggregator.Status = "done" })
	if p := get(); p.Aggregator.Status != "pending" || p.Research.Status != "running" {
		t.Fatalf("a write after completion landed: %+v", p)
	}

	// No market lookup: skipped from the start. An ordered run looks up its strike ladder.
	if s := newTracker(ctx, e.DB, f.ID, Draft{Shape: "binary"}, false, nil).p.Market.Status; s != "skipped" {
		t.Errorf("no POLYMARKET_BASE: market %s", s)
	}
	if s := newTracker(ctx, e.DB, f.ID, Draft{Shape: "ordered"}, true, nil).p.Market.Status; s != "pending" {
		t.Errorf("ordered: market %s", s)
	}
}

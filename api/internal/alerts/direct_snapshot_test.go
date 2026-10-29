package alerts

import (
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
)

// Different pinned snapshots of one stable ID can coexist after upstream
// changes. The fresh canonical read is shared by ID, while applicability must
// be decided separately for each alert's immutable snapshot. An old alert
// must not poison the whole group by pinning the shared read to its old slug.
func TestCheckDirectOddsCurrentSnapshotFiresBesideDormantOldSnapshot(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	g := newDirectGamma(t, e)
	directClock(t)
	assertDirectFree(t, e)
	old := directCreate(t, e, e.Cookie, directBody("above", 0.5))
	const oldID = "00000000-0000-4000-8000-000000000001"
	if _, err := e.DB.Exec(ctx, "update alerts set id = $2, created_at = $3 where id = $1", old["id"], oldID, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	const currentSlug = directSlug + "-revised"
	const currentRules = directRules + " If Binance suspends trading at the observation time, use the first BTC/USDT trade after trading resumes."
	g.change(func(m map[string]any) {
		m["slug"], m["description"] = currentSlug, currentRules
	})
	g.mu.Lock()
	// Register the revised leaf route as well as the stable ID. Both routes
	// refer to the same fixture so the later price move reaches both readers.
	g.others = map[string]map[string]any{directID: g.market}
	g.mu.Unlock()
	currentBody := directBody("above", 0.6)
	// The numeric ID cross-check also verifies the supplied revised leaf.
	currentBody["marketId"], currentBody["marketSlug"] = directID, currentSlug
	current := directCreate(t, e, e.Cookie, currentBody)
	const currentID = "ffffffff-ffff-4fff-8fff-ffffffffffff"
	if _, err := e.DB.Exec(ctx, "update alerts set id = $2 where id = $1", current["id"], currentID); err != nil {
		t.Fatal(err)
	}
	if directObject(t, old["linkedMarket"])["marketSlug"] != directSlug ||
		directObject(t, current["linkedMarket"])["marketSlug"] != currentSlug ||
		directObject(t, current["linkedMarket"])["description"] != currentRules ||
		old["marketId"] != directID || current["marketId"] != directID {
		t.Fatal("fixture must have a validated old and current snapshot of the same numeric ID")
	}

	// Applicability must be independent of the feed's order for these two
	// snapshots; rows with the same market ID have no defined tie order.
	g.price(0.7) // both levels are crossed, but only the current contract applies
	g.reads()
	if err := Check(ctx, e.Deps); err != nil {
		t.Fatal(err)
	}
	if reads := g.reads(); reads != 1 {
		t.Errorf("two snapshots of one stable ID need one fresh read, got %d", reads)
	}
	if a := load(t, e, oldID); a.Status != "active" || a.FiredAt != nil || a.FiredPrice != nil {
		t.Errorf("an obsolete snapshot must remain dormant: %+v", a)
	}
	if a := load(t, e, currentID); a.Status != "fired" || a.FiredPrice == nil || *a.FiredPrice != 0.7 || a.FiredAt == nil || a.TradeID != nil {
		t.Errorf("an obsolete first snapshot must not suppress the current snapshot: %+v", a)
	}
	w := e.Do("GET", "/api/alerts", nil)
	listed := apptest.JSON[struct{ Alerts []map[string]any }](t, w).Alerts
	if w.Code != 200 || len(listed) != 2 {
		t.Fatalf("both pinned snapshots must remain visible: status=%d alerts=%#v", w.Code, listed)
	}
	for _, a := range listed {
		link := directObject(t, a["linkedMarket"])
		if a["id"] == oldID && (link["marketSlug"] != directSlug || link["description"] != directRules) {
			t.Errorf("old alert's immutable snapshot changed: %#v", link)
		}
		if a["id"] == currentID && (link["marketSlug"] != currentSlug || link["description"] != currentRules || directObject(t, a["market"])["yesPrice"] != 0.7) {
			t.Errorf("current alert must keep its own snapshot and fresh quote: %#v", a)
		}
	}
	items := apptest.JSON[struct{ Items []Notification }](t, e.Do("GET", "/api/notifications", nil)).Items
	if len(items) != 1 || items[0].ID != "odds_alert:"+currentID || items[0].Link != "/automations?tab=history&alert="+currentID || items[0].Body != "Now 70%" {
		t.Errorf("only the current snapshot should notify: %+v", items)
	}
	if err := Check(ctx, e.Deps); err != nil || g.reads() != 0 || load(t, e, oldID).Status != "active" {
		t.Fatalf("the shared ID remains minute-throttled with its obsolete snapshot dormant: %v", err)
	}
	assertDirectFree(t, e)
}

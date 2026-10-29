package alerts

import (
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
)

// Compatibility callers still send USD amounts. New notifications preserve
// that level's value at creation, then follow the listing rather than FX.
func TestNewForeignNotifyAlertsAlwaysUseNativeCurrency(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	sgd(e, 36, 30, 40, 36)
	e.Yahoo.SetFX("SGD", 0.8)
	var list []Alert
	for _, body := range []string{
		`{"symbol":"DBS","op":"below","price":20}`,
		`{"symbol":"DBS","op":"below","price":20,"currency":" usd "}`,
		`{"symbol":"DBS","op":"below","price":25,"currency":"sgd"}`,
	} {
		a := create(t, e, body)
		if a.Currency == nil || *a.Currency != "SGD" || *a.Price != 25 {
			t.Fatalf("native level for %s: %+v", body, a)
		}
		list = append(list, a)
	}
	order := create(t, e, `{"symbol":"DBS","op":"below","price":20,"action":"buy","notional":100}`)
	if order.Currency != nil || *order.Price != 20 {
		t.Fatalf("USD order changed: %+v", order)
	}

	// The new native alerts stay active at SGD 27 even when its USD value
	// falls below the old $20 input. A USD paper order still crosses at $20.
	sgd(e, 27, 30, 40, 27)
	e.Yahoo.SetFX("SGD", 0.5)
	e.Market.Now = func() time.Time { return time.Now().Add(31 * time.Minute) }
	if _, err := e.Market.FreshAssets(ctx); err != nil {
		t.Fatal(err)
	}
	if err := checkPrices(ctx, e.Deps); err != nil {
		t.Fatal(err)
	}
	for _, a := range list {
		if got := load(t, e, a.ID); got.Status != "active" {
			t.Fatalf("FX crossed a native level: %+v", got)
		}
	}
	if got := load(t, e, order.ID); got.FiredAt == nil || got.Currency != nil || got.FiredPrice == nil || *got.FiredPrice != 13.5 {
		t.Fatalf("USD order did not keep its trigger basis: %+v", got)
	}

	sgd(e, 24, 30, 40, 24)
	e.Market.Now = func() time.Time { return time.Now().Add(62 * time.Minute) }
	if err := checkPrices(ctx, e.Deps); err != nil {
		t.Fatal(err)
	}
	for _, a := range list {
		if got := load(t, e, a.ID); got.Status != "fired" || *got.FiredPrice != 24 || *got.Currency != "SGD" {
			t.Fatalf("native alert did not cross: %+v", got)
		}
	}
}

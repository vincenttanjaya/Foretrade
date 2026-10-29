package alerts

import (
	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
	"testing"
)

func TestStockPriceAlertFillsAtPerpMark(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	a := create(t, e, `{"symbol":"AAPL","op":"below","price":330,"action":"buy","notional":500}`)
	price(e, "xyz:AAPL", 320)
	if err := checkPrices(ctx, e.Deps); err != nil {
		t.Fatal(err)
	}
	a = load(t, e, a.ID)
	if a.Status != "fired" || a.TradeID == nil {
		t.Fatalf("stockalert%+v", a)
	}
	trs, err := e.Paper.Trades(ctx, e.User, 1)
	if err != nil || len(trs) != 1 || trs[0].Symbol != "AAPL" || trs[0].Source != "alert" || trs[0].Price != paper.FillPrice(320, true) {
		t.Fatalf("stockfill%+v %v", trs, err)
	}
}

package autopilot

import (
	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
	"github.com/vincenttanjaya/Foretrade/api/internal/strategy"
	"testing"
	"time"
)

func TestStockAutopilotOnClosedUSSession(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	p := deployed(t, e, spec(func(s *strategy.Spec) { s.Symbol = "AAPL" }), 10000, 20)
	now := time.Date(2026, 10, 3, 15, 0, 0, 0, time.UTC)
	if open, _ := market.USSession(now); open {
		t.Fatal("expectedweekend")
	}
	trs := mustStep(t, e, p, now)
	if len(trs) != 1 || trs[0].Symbol != "AAPL" || trs[0].Source != "autopilot" || trs[0].Price != paper.FillPrice(markettest.Price("xyz:AAPL"), true) {
		t.Fatalf("continuousstockpilot%+v", trs)
	}
	if pos := holdingOf(t, e, "AAPL"); pos == nil || pos.Qty <= 0 {
		t.Fatalf("stockholding%+v", pos)
	}
	if again := mustStep(t, e, p, now); len(again) != 0 {
		t.Fatalf("duplicateclosedbar%+v", again)
	}
}

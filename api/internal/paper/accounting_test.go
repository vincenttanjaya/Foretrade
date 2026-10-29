package paper

import (
	"math"
	"testing"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestBook(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		qty, avg, trade, price   float64
		wantQty, wantAvg, wantRP float64 // realized P&L net of fee
	}{
		{"open long", 0, 0, 2, 100, 2, 100, -0.2},
		{"add long", 2, 100, 2, 110, 4, 105, -0.22},
		{"partial close long at profit", 4, 105, -1, 125, 3, 105, 20 - 0.125},
		{"full close long at loss", 3, 105, -3, 95, 0, 0, -30 - 0.285},
		{"flip long to short", 1, 100, -3, 120, -2, 120, 20 - 0.36},
		{"open short", 0, 0, -2, 50, -2, 50, -0.1},
		{"add short", -2, 50, -2, 60, -4, 55, -0.12},
		{"cover short at profit", -4, 55, 1, 45, -3, 55, 10 - 0.045},
		{"cover short at loss", -3, 55, 3, 65, 0, 0, -30 - 0.195},
		{"flip short to long", -1, 50, 2, 40, 1, 40, 10 - 0.08},
	} {
		f := Book(tc.qty, tc.avg, tc.trade, tc.price)
		if !approx(f.Qty, tc.wantQty) || !approx(f.AvgPrice, tc.wantAvg) || !approx(f.Realized, tc.wantRP) {
			t.Errorf("%s: got qty %v avg %v realized %v, want %v %v %v",
				tc.name, f.Qty, f.AvgPrice, f.Realized, tc.wantQty, tc.wantAvg, tc.wantRP)
		}
		notional := math.Abs(tc.trade) * tc.price
		if !approx(f.Fee, notional*FeeRate) || !approx(f.CashDelta, -tc.trade*tc.price-f.Fee) {
			t.Errorf("%s: fee %v cash %v", tc.name, f.Fee, f.CashDelta)
		}
	}
	if f := Book(0.3, 100, -0.1-0.2, 100); f.Qty != 0 || f.AvgPrice != 0 {
		t.Errorf("float dust must close the position: %+v", f)
	}
}

// Equity must always equal start + Σ realized + unrealized (fees included).
func TestLedgerIdentity(t *testing.T) {
	cash, realized, qty, avg := StartingBalance, 0.0, 0.0, 0.0
	for _, step := range []struct{ trade, price float64 }{{1, 100}, {0.5, 120}, {-2, 90}, {1, 80}, {0.5, 110}} {
		f := Book(qty, avg, step.trade, step.price)
		cash, realized, qty, avg = cash+f.CashDelta, realized+f.Realized, f.Qty, f.AvgPrice
		mark := step.price * 1.05
		p := buildPortfolio(account{Cash: cash, StartingBalance: StartingBalance, RealizedPnl: realized},
			map[string]position{"BTC": {Qty: qty, AvgPrice: avg}}, map[string]float64{"BTC": mark})
		if !approx(p.Equity, StartingBalance+p.RealizedPnl+p.UnrealizedPnl) || !approx(p.TotalPnl, p.RealizedPnl+p.UnrealizedPnl) {
			t.Fatalf("after %+v: equity %v realized %v unrealized %v", step, p.Equity, p.RealizedPnl, p.UnrealizedPnl)
		}
	}
}

func TestBuildPortfolio(t *testing.T) {
	sl := 90.0
	p := buildPortfolio(account{Cash: 50_000, StartingBalance: 100_000},
		map[string]position{"ETH": {Qty: -10, AvgPrice: 200, StopLoss: &sl}, "BTC": {Qty: 1, AvgPrice: 50_000}},
		map[string]float64{"BTC": 60_000, "ETH": 180})
	if p.Positions[0].Symbol != "BTC" || p.Positions[1].Symbol != "ETH" {
		t.Fatalf("positions must be sorted by symbol: %+v", p.Positions)
	}
	eth := p.Positions[1]
	if eth.MarketValue != -1800 || eth.UnrealizedPnl != 200 || !approx(eth.UnrealizedPnlPct, 10) || eth.StopLoss == nil {
		t.Fatalf("short position: %+v", eth)
	}
	if p.Equity != 108_200 || p.GrossExposure != 61_800 || p.BuyingPower != 2*108_200-61_800 || !approx(p.TotalPnlPct, 8.2) {
		t.Fatalf("portfolio: %+v", p)
	}
}

func TestTriggered(t *testing.T) {
	sl, tp := 90.0, 120.0
	for _, tc := range []struct {
		qty, mark float64
		want      string
	}{{1, 89, "stop_loss"}, {1, 90, "stop_loss"}, {1, 121, "take_profit"}, {1, 100, ""}} {
		if got := triggered(tc.qty, tc.mark, &sl, &tp); got != tc.want {
			t.Errorf("long qty %v mark %v: %q, want %q", tc.qty, tc.mark, got, tc.want)
		}
	}
	ssl, stp := 120.0, 90.0 // short: stop above, target below
	if triggered(-1, 121, &ssl, &stp) != "stop_loss" || triggered(-1, 89, &ssl, &stp) != "take_profit" || triggered(-1, 100, &ssl, &stp) != "" {
		t.Error("short triggers")
	}
	if triggered(1, 50, nil, nil) != "" {
		t.Error("no levels, no trigger")
	}
}

// MaxFill × buying power, split across several buys, passes the 2x rule after
// fees and slippage (aipf invest relies on it); the full buying power does not.
func TestMaxFill(t *testing.T) {
	marks := map[string]float64{"BTC": 60_000, "ETH": 3_000, "SOL": 150}
	weights := []struct {
		sym string
		w   float64
	}{{"BTC", 0.4}, {"ETH", 0.3}, {"SOL", 0.3}}
	fits := func(share float64) bool {
		cash, positions := StartingBalance, map[string]position{}
		amount := share * maxGross * StartingBalance // fresh account: buying power = 2 × equity
		for _, h := range weights {
			price := FillPrice(marks[h.sym], true)
			f := Book(0, 0, amount*h.w/price, price)
			cash += f.CashDelta
			positions[h.sym] = position{Qty: f.Qty, AvgPrice: f.AvgPrice}
			if equity, gross := exposure(cash, positions, marks); !(equity > 0 && gross <= maxGross*equity) {
				return false
			}
		}
		return true
	}
	if !fits(MaxFill) || fits(1) {
		t.Fatalf("MaxFill fits = %v, full buying power fits = %v", fits(MaxFill), fits(1))
	}
}

// Open prediction stakes sit in equity at cost: buying one moves cash, not
// equity or buying power.
func TestBuildPortfolioStaked(t *testing.T) {
	marks := map[string]float64{"BTC": 60_000}
	pos := map[string]position{"BTC": {Qty: 1, AvgPrice: 50_000}}
	before := buildPortfolio(account{Cash: 50_000, StartingBalance: 100_000}, pos, marks)
	after := buildPortfolio(account{Cash: 49_900, StartingBalance: 100_000, Staked: 100}, pos, marks)
	if after.PredictionStakes != 100 || after.Cash != 49_900 || after.Equity != before.Equity || after.BuyingPower != before.BuyingPower ||
		after.TotalPnl != 10_000 {
		t.Fatalf("staked: %+v, before %+v", after, before)
	}
}

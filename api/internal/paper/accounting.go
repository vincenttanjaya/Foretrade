package paper

import (
	"math"
	"sort"
)

const (
	StartingBalance = 100_000.0
	Slippage        = 0.0002 // 2 bps on every fill (FillPrice): buys pay up, sells receive less
	FeeRate         = 0.001  // 0.1% of notional on every fill (Book)
	maxGross        = 2.0    // gross exposure ≤ 2 × equity
	dust            = 1e-9   // smaller remainders count as flat

	// MaxFill is the share of buying power that buys can always spend: each
	// fill's fee and slippage cost ~0.22% of it under the 2x rule. The web
	// order ticket's MAX_FILL is the same headroom.
	MaxFill = 0.995
)

// Fill is one trade booked against a position. Realized P&L is net of the
// fee, so every fee is a realized cost and equity = start + realized + unrealized.
type Fill struct {
	Qty, AvgPrice float64 // resulting position; Qty 0 = flat
	Realized      float64 // P&L on the reduced part, minus the fee
	CashDelta     float64
	Fee           float64
}

// Book applies a signed trade quantity (buy > 0, sell < 0) at price to a
// position with average-cost accounting: adding re-weights the average,
// reducing books P&L against it, and a flip opens the remainder at price.
// The fee is FeeRate of the notional; price should already be a FillPrice.
func Book(qty, avg, tradeQty, price float64) Fill {
	f := Fill{Qty: qty + tradeQty, Fee: math.Abs(tradeQty) * price * FeeRate}
	f.CashDelta = -tradeQty*price - f.Fee
	switch {
	case qty == 0 || (qty > 0) == (tradeQty > 0): // open or add
		f.AvgPrice = (qty*avg + tradeQty*price) / f.Qty
	case math.Abs(tradeQty) <= math.Abs(qty): // reduce or close
		f.Realized = math.Abs(tradeQty) * (price - avg) * sign(qty)
		f.AvgPrice = avg
	default: // flip: close all, open the rest at price
		f.Realized = math.Abs(qty) * (price - avg) * sign(qty)
		f.AvgPrice = price
	}
	if math.Abs(f.Qty) < dust {
		f.Qty, f.AvgPrice = 0, 0
	}
	f.Realized -= f.Fee
	return f
}

func sign(x float64) float64 {
	if x < 0 {
		return -1
	}
	return 1
}

// FillPrice applies Slippage to the mark: a buy pays more, a sell receives less.
func FillPrice(mark float64, buy bool) float64 {
	if buy {
		return mark * (1 + Slippage)
	}
	return mark * (1 - Slippage)
}

// triggered reports which protective order a mark touches, stop first.
func triggered(qty, mark float64, stop, target *float64) string {
	long := qty > 0
	switch {
	case stop != nil && ((long && mark <= *stop) || (!long && mark >= *stop)):
		return "stop_loss"
	case target != nil && ((long && mark >= *target) || (!long && mark <= *target)):
		return "take_profit"
	}
	return ""
}

// exposure is equity and gross exposure (Σ|qty × mark|) at the given marks.
func exposure(cash float64, positions map[string]position, marks map[string]float64) (equity, gross float64) {
	equity = cash
	for sym, p := range positions {
		equity += p.Qty * markOf(marks, sym, p)
		gross += math.Abs(p.Qty * markOf(marks, sym, p))
	}
	return equity, gross
}

// markOf is sym's mark, or the position's average price when Binance no longer
// prices the pair (market.Assets dropped it), so one delisting cannot zero the
// account's equity or buying power. A halted pair is still priced: its last trade.
// ponytail: stale valuation until Binance lists the pair again; orders and stops on it wait for a live price.
func markOf(marks map[string]float64, sym string, p position) float64 {
	if m, ok := marks[sym]; ok {
		return m
	}
	return p.AvgPrice
}

// buildPortfolio values the account at the marks. Positions are by symbol.
// equity = cash + open prediction stakes + Σ qty × mark.
// ponytail: open stakes are valued at cost until settlement; mark-to-market from the refreshed yesPrice later
func buildPortfolio(a account, positions map[string]position, marks map[string]float64) Portfolio {
	p := Portfolio{Revision: a.Revision, Cash: a.Cash, StartingBalance: a.StartingBalance, RealizedPnl: a.RealizedPnl, Positions: []Position{},
		PredictionStakes: a.Staked}
	for sym, pos := range positions {
		mark := markOf(marks, sym, pos)
		cost := math.Abs(pos.Qty) * pos.AvgPrice
		pp := Position{Symbol: sym, Qty: pos.Qty, AvgPrice: pos.AvgPrice, MarkPrice: mark,
			MarketValue: pos.Qty * mark, UnrealizedPnl: (mark - pos.AvgPrice) * pos.Qty,
			StopLoss: pos.StopLoss, TakeProfit: pos.TakeProfit}
		if cost > 0 {
			pp.UnrealizedPnlPct = pp.UnrealizedPnl / cost * 100
		}
		p.UnrealizedPnl += pp.UnrealizedPnl
		p.Positions = append(p.Positions, pp)
	}
	sort.Slice(p.Positions, func(i, j int) bool { return p.Positions[i].Symbol < p.Positions[j].Symbol })
	p.Equity, p.GrossExposure = exposure(a.Cash+a.Staked, positions, marks)
	p.TotalPnl = p.Equity - a.StartingBalance
	if a.StartingBalance > 0 {
		p.TotalPnlPct = p.TotalPnl / a.StartingBalance * 100
	}
	p.BuyingPower = max(maxGross*p.Equity-p.GrossExposure, 0)
	return p
}

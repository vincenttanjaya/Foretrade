package screener

import (
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo"
)

const (
	smaWeeks = 200 // the 200-week moving average
	rsiWeeks = 14  // RSI(14) on weekly closes
)

// metrics is what one lifetime weekly chart supports. A nil field is a value
// the bars cannot support (fewer than 200 or 15 weeks), never estimated.
type metrics struct {
	Name      string // Yahoo's long name, else its short name
	Currency  string // Yahoo's meta currency, e.g. JPY (D01.SI is USD)
	Price     float64
	PriceAt   time.Time
	Weeks     int        // weekly closes
	FirstWeek time.Time  // the first bar: what the all-time high covers
	SMA       *float64   // mean of the last 200 weekly closes
	Pct       *float64   // price vs SMA: (price/SMA − 1)·100
	PrevPct   *float64   // the same a week earlier: last week's close vs the SMA ending then
	RSI       *float64   // Wilder RSI(14) of the weekly closes
	ATH       *float64   // the highest weekly high; nil when over maxATHMultiple × the price
	ATHAt     *time.Time // the bar that made it
	Drawdown  *float64   // price vs ATH in percent (≤ 0)
}

// maxATHMultiple bounds the all-time high: one over this many times the price
// is withheld (nil), as the alerts and get_stock_quote withhold it. Reverse
// splits scale old prices up, so such a high is no measure of a drawdown.
const maxATHMultiple = 100

// compute reads a chart of weekly bars (Chart range "max", interval "1wk",
// oldest first). Yahoo appends the live session as an extra bar inside the
// current week; WeeklyCloses keeps the last close of each exchange-local ISO
// week, so the newest week's close is the latest price and may be partial.
// The price is the meta's regular-market price when it is no older than the
// last bar, else the last close. false when there are no bars.
func compute(ch yahoo.Chart) (metrics, bool) {
	weekly := yahoo.WeeklyCloses(ch.Bars, ch.Meta.Location())
	if len(weekly) == 0 {
		return metrics{}, false
	}
	closes := yahoo.Values(weekly)
	n := len(closes)
	last := weekly[n-1]
	m := metrics{Name: ch.Meta.Name(), Currency: ch.Meta.Currency, Price: last.V, PriceAt: last.T, Weeks: n, FirstWeek: ch.Bars[0].T}
	if p, at := ch.Meta.Price, ch.Meta.PriceTime; p != nil && at != nil && !at.Before(last.T) {
		m.Price, m.PriceAt = *p, *at
	}
	if sma := yahoo.SMA(closes, smaWeeks); sma != nil {
		m.SMA = sma
		m.Pct = ptr(pct(m.Price, *sma))
		if prev := yahoo.SMA(closes[:n-1], smaWeeks); prev != nil {
			m.PrevPct = ptr(pct(closes[n-2], *prev))
		}
	}
	m.RSI = yahoo.RSI(closes, rsiWeeks)
	ath, _ := yahoo.AllTimeHigh(ch.Bars)
	if m.Price > ath.High { // a regular-market price newer than every bar
		ath.High, ath.At = m.Price, m.PriceAt
	}
	if ath.High <= maxATHMultiple*m.Price {
		m.ATH, m.ATHAt, m.Drawdown = &ath.High, &ath.At, ptr(yahoo.DrawdownPct(m.Price, ath.High))
	}
	return m, true
}

func pct(price, base float64) float64 { return (price/base - 1) * 100 }

func ptr[T any](v T) *T { return &v }

// direction compares this week's distance from the 200-week average with last
// week's: "deepening" when it fell (further below, or closer from above),
// "recovering" when it rose; nil without both or when unchanged.
func direction(now, prev *float64) *string {
	switch {
	case now == nil || prev == nil || *now == *prev:
		return nil
	case *now < *prev:
		return ptr("deepening")
	}
	return ptr("recovering")
}

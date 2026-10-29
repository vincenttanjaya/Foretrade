package screener

import (
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo"
)

// venues is each screened market's session venue (the Philippines has no
// Yahoo data, so no quote to read).
var venues = map[string]string{"JP": market.TSE, "KR": market.KRX, "TW": market.TWSE, "HK": market.HKEX, "SG": market.SGX,
	"ID": market.IDX, "TH": market.SET, "MY": market.BURSA, "VN": market.HOSE}

// QuoteWatch is every constituent Yahoo serves, as a row for the market
// layer's watch tier (market.Client.Watch), which keeps only those no
// quote-feed instrument reads under their own ticker: the perp-traded and
// ADR-traded constituents' local lines and the research-only ones. The rest
// are priced from the quote feeds the Trade page uses, never read twice.
func QuoteWatch() []market.WatchRow {
	var out []market.WatchRow
	for _, st := range stocks {
		if v, ok := venues[st.Market]; ok && st.Resolves {
			out = append(out, market.WatchRow{Ticker: st.Ticker, Venue: v, Currency: st.Currency})
		}
	}
	return out
}

// price reprices r at its live quote q against s, the run's stored row (zero
// before any run), as the method says: price, priceAt, priceUsd and fx from
// the quote; pctVsSma against the run's 200-week mean; pctVsSmaPrev and
// direction against last week's close (the run's own pctVsSma once the quote
// is in a later exchange-local week than the run's price); the drawdown
// against the higher of the run's all-time high and the quote. The run's
// values stand when its price is newer than the quote, or its currency is
// not the quote's.
func (r *Row) price(q market.ListingQuote, s storedRow) {
	if s.computed != nil && (r.Currency != q.Currency || s.priceAt != nil && q.LastTradeAt.Before(*s.priceAt)) {
		return
	}
	p, at := q.Price, q.LastTradeAt
	r.Currency, r.Price, r.PriceAt = q.Currency, &p, &at
	switch {
	case q.FX != nil:
		r.PriceUSD, r.FX = ptr(p*q.FX.USD), nil
		if q.FX.Symbol != "" {
			r.FX = &FX{Pair: q.FX.Symbol, USDPerUnit: q.FX.USD, At: q.FX.Time.UTC()}
		}
	case r.FX != nil:
		r.PriceUSD = ptr(p * r.FX.USDPerUnit)
	default:
		r.PriceUSD = nil
	}
	if s.sma != nil && *s.sma > 0 {
		now, prev := pct(p, *s.sma), s.pctPrev
		if s.priceAt != nil && laterWeek(at, *s.priceAt, zones[r.Market]) {
			prev = s.pctNow // the run's newest week is the quote's last week
		}
		r.PctVsSMA, r.PctVsSMAPrev, r.Direction, r.change = round2(&now), round2(prev), direction(&now, prev), nil
		if prev != nil {
			r.change = ptr(now - *prev)
		}
	}
	if s.ath != nil {
		ath, athAt := *s.ath, r.ATHAt
		if p > ath {
			ath, athAt = p, localDate(&at, r.Market)
		}
		r.ATH, r.ATHAt, r.DrawdownPct = &ath, athAt, round2(ptr(yahoo.DrawdownPct(p, ath)))
		if ath > maxATHMultiple*p { // withheld, as the run withholds it
			r.ATH, r.ATHAt, r.DrawdownPct = nil, nil, nil
		}
	}
	r.Quote = &Quote{Source: q.Source, Class: q.Class, Tier: q.Tier, Price: p, Currency: q.Currency, LastTradeAt: at, ReadAt: q.ReadAt,
		DelayMinutes: q.DelayMinutes, DelaySeconds: q.DelaySeconds, TimeBasis: q.TimeBasis, Label: q.Label,
		Session: q.Session, StaleAt: q.StaleAt, Stale: q.Stale}
	if q.DelayBasis != "" {
		r.Quote.DelayBasis = &q.DelayBasis
	}
	if q.Refresh > 0 {
		r.Quote.RefreshSeconds = ptr(int(q.Refresh / time.Second))
	}
}

// laterWeek reports whether a falls in a later ISO week than b in loc.
func laterWeek(a, b time.Time, loc *time.Location) bool {
	monday := func(t time.Time) time.Time {
		t = t.In(loc)
		return time.Date(t.Year(), t.Month(), t.Day()-(int(t.Weekday())+6)%7, 0, 0, 0, 0, loc)
	}
	return monday(a).After(monday(b))
}

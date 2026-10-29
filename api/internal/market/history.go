package market

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo"
)

// Long daily history for the multi-year views (the chat's price_outlook card,
// and the forecast desk's history tools next round). A stock or index perp's
// own venue history is about a year; its underlying listing's Yahoo daily
// closes go back decades, so every long-horizon number reads the listing.

// DayClose is one session's close.
type DayClose struct {
	T time.Time // the session's date, 00:00 UTC
	C float64   // split-adjusted close (yahoo.Bar.C), never Adj: questions resolve on the quoted price
}

// DailySeries is one instrument's daily closes, oldest first.
type DailySeries struct {
	Symbol   string     // universe symbol ("TSM", "BTC") or benchmark key ("SOX")
	Ticker   string     // what was fetched: "TSM" (Yahoo), "BTCUSDT" (Binance), "xyz:XYZ100" (the perp)
	Source   string     // "Yahoo Finance daily closes (NYSE: TSM)" | "Binance daily closes (BTC/USDT)"
	Currency string     // the series' currency as served
	PerYear  float64    // 252 for exchange sessions, 365 for 24/7 calendar closes
	Kind     string     // "price", or "level" for a rate (UST10Y): windows use absolute changes
	Closes   []DayClose // oldest first
}

// Years is the span from the first close to the last, in years of 365 days
// (longHorizon's convention).
func (s DailySeries) Years() float64 {
	if len(s.Closes) < 2 {
		return 0
	}
	return s.Closes[len(s.Closes)-1].T.Sub(s.Closes[0].T).Hours() / 24 / 365
}

// Benchmarks are the only non-universe series TickerHistory reads (a fixed
// allowlist, so a model cannot spend the shared Yahoo budget on arbitrary
// tickers): key → Yahoo ticker. UST10Y is a yield level, not a price.
var Benchmarks = map[string]string{
	"SPX": "^GSPC", "NDX": "^NDX", "SOX": "^SOX", "TWII": "^TWII", "N225": "^N225", "HSI": "^HSI",
	"KOSPI": "^KS11", "STI": "^STI", "JKSE": "^JKSE", "UST10Y": "^TNX",
}

// DailyHistory is the longest daily history for a universe symbol: a stock or
// index with a Yahoo listing reads its full Yahoo daily history (period1=0,
// cached 12 h by the Yahoo client) in the listing's currency; crypto reads
// Binance's daily bars (up to maxCandles, about 8.2 years); a perp with no
// listing reads its own venue bars. Yahoo failures (yahoo.ErrUnavailable)
// pass through; nothing is filled in.
func (c *Client) DailyHistory(ctx context.Context, symbol string) (DailySeries, error) {
	return c.dailyHistory(ctx, symbol, false)
}

// DailyHistoryUSD is DailyHistory with a non-USD listing converted to USD at
// Yahoo's daily FX closes (yahoo.ToUSD; bars before the first rate are
// dropped). A USD spot needs it: a currency's own trend is part of a non-USD
// listing's return to a dollar holder, so the outlook card's drift, CAGRs and
// σ read this series. When only the FX read fails the error wraps
// ErrFXHistory, and a caller may fall back to DailyHistory, said so.
func (c *Client) DailyHistoryUSD(ctx context.Context, symbol string) (DailySeries, error) {
	return c.dailyHistory(ctx, symbol, true)
}

// ErrFXHistory marks a converted series whose listing read but whose FX
// history did not (it also wraps the Yahoo error).
var ErrFXHistory = errors.New("FX history unavailable")

// fxSource ends a converted series' Source.
const fxSource = ", in USD at Yahoo daily FX"

// Converted reports whether the series was converted to USD from the
// listing's own currency (DailyHistoryUSD, TickerHistoryUSD).
func (s DailySeries) Converted() bool { return strings.HasSuffix(s.Source, fxSource) }

func (c *Client) dailyHistory(ctx context.Context, symbol string, usd bool) (DailySeries, error) {
	info, ok := Lookup(symbol)
	if !ok {
		return DailySeries{}, fmt.Errorf("unknown symbol %q", symbol)
	}
	if l := info.Listing; l != nil && (l.Yahoo != "" || info.QuoteFeed()) {
		ticker := l.Yahoo
		if info.QuoteFeed() {
			ticker = info.Pair // a quote feed's Pair is its Yahoo ticker
		}
		return c.yahooSeries(ctx, info.Symbol, ticker, usd)
	}
	bars, err := c.Candles(ctx, info.Symbol, "1d", maxCandles)
	if err != nil {
		return DailySeries{}, err
	}
	s := DailySeries{Symbol: info.Symbol, Ticker: info.Pair, Currency: "USD", PerYear: 365, Kind: "price", Closes: calendarDays(bars)}
	if len(s.Closes) == 0 {
		return DailySeries{}, fmt.Errorf("%w: no daily bars for %s", ErrUpstream, info.Pair)
	}
	if info.Perp() {
		s.Source = fmt.Sprintf("Hyperliquid perp daily closes (%s), venue history since %s", info.Pair, s.Closes[0].T.Format(time.DateOnly))
	} else {
		s.Source = "Binance daily closes (" + strings.TrimSuffix(info.Pair, "USDT") + "/USDT)"
	}
	return s, nil
}

// TickerHistory is a benchmark's full Yahoo daily history (key from
// Benchmarks), in the index's own currency.
func (c *Client) TickerHistory(ctx context.Context, key string) (DailySeries, error) {
	return c.tickerHistory(ctx, key, false)
}

// TickerHistoryUSD is TickerHistory converted to USD at Yahoo's daily FX
// closes, as DailyHistoryUSD: the drift cap for a USD-measured drift.
func (c *Client) TickerHistoryUSD(ctx context.Context, key string) (DailySeries, error) {
	return c.tickerHistory(ctx, key, true)
}

func (c *Client) tickerHistory(ctx context.Context, key string, usd bool) (DailySeries, error) {
	key = strings.ToUpper(strings.TrimSpace(key))
	ticker, ok := Benchmarks[key]
	if !ok {
		return DailySeries{}, fmt.Errorf("unknown benchmark %q", key)
	}
	s, err := c.yahooSeries(ctx, key, ticker, usd && key != "UST10Y") // a yield is a level, not a price
	if key == "UST10Y" {
		s.Kind = "level"
	}
	return s, err
}

// yahooSeries reads ticker's lifetime daily chart: one close per exchange-local
// session date (the last bar of a date wins), in the listing's currency or,
// with usd, converted (its Source then says so).
func (c *Client) yahooSeries(ctx context.Context, symbol, ticker string, usd bool) (DailySeries, error) {
	src := c.Yahoo
	if src == nil {
		src = yahoo.Disabled
	}
	ch, err := src.Chart(ctx, ticker, "max", "1d")
	if err != nil {
		return DailySeries{}, err
	}
	bars, ccy, conv := ch.Bars, ch.Meta.Currency, ""
	if usd && ccy != "" && ccy != "USD" {
		fx, err := src.FXHistory(ctx, ccy, "max")
		if err != nil {
			return DailySeries{}, fmt.Errorf("%w (%s): %w", ErrFXHistory, ccy, err)
		}
		bars, ccy, conv = yahoo.ToUSD(bars, fx), "USD", fxSource
	}
	loc := ch.Meta.Location()
	s := DailySeries{Symbol: symbol, Ticker: ticker, Currency: ccy, PerYear: 252, Kind: "price",
		Source: "Yahoo Finance daily closes (" + exchangeOf(ch.Meta) + ticker + ")" + conv}
	for _, b := range bars {
		if !(b.C > 0) || !finite(b.C) {
			continue
		}
		y, m, d := b.T.In(loc).Date()
		k := DayClose{T: time.Date(y, m, d, 0, 0, 0, 0, time.UTC), C: b.C}
		if n := len(s.Closes); n > 0 && !s.Closes[n-1].T.Before(k.T) {
			s.Closes[n-1] = k // the same session read twice (the live bar): keep the later
			continue
		}
		s.Closes = append(s.Closes, k)
	}
	if len(s.Closes) < 2 {
		return DailySeries{}, &yahoo.UnavailableError{Symbol: ticker, Reason: "Yahoo sent no daily history"}
	}
	return s, nil
}

func exchangeOf(m yahoo.Meta) string {
	if e := m.Exchange(); e != "" {
		return e + ": "
	}
	return ""
}

// CalendarCloses is one close per calendar day from a quote feed's first daily
// bar to its last: a day without a session carries the previous close. Bar
// counts then read as days, as they do for the rows that trade every day, so
// "7d" is a week and not seven sessions, and a weekend adds flat days to the
// volatility the way a 365-day year expects.
func CalendarCloses(daily []Candle) []float64 {
	days := calendarDays(daily)
	out := make([]float64, len(days))
	for i, d := range days {
		out[i] = d.C
	}
	return out
}

// calendarDays is CalendarCloses with each day's date.
func calendarDays(daily []Candle) []DayClose {
	if len(daily) == 0 {
		return nil
	}
	out := make([]DayClose, 0, (daily[len(daily)-1].T-daily[0].T)/86400+1)
	for i, k := range daily {
		if i > 0 {
			for t := daily[i-1].T + 86400; t < k.T; t += 86400 {
				out = append(out, DayClose{T: time.Unix(t, 0).UTC(), C: daily[i-1].C})
			}
		}
		out = append(out, DayClose{T: time.Unix(k.T, 0).UTC(), C: k.C})
	}
	return out
}

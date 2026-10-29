// Package yahoo is a keyless Yahoo Finance client: chart history, latest
// regular-market quotes and USD FX rates from query1.finance.yahoo.com, plus
// pure helpers over daily bars (all-time high, trailing returns, realised
// volatility, weekly closes, SMA, Wilder RSI, USD conversion).
//
// The endpoints are unofficial. Every Client built with default pacing shares
// one process-wide gate: 2 requests/s (burst 4), at most 4 in flight, a 10 s
// timeout per attempt, two retries of a 429 or 5xx, and a breaker that pauses
// every call for a few minutes after repeated 429s. Answers are cached per
// key (quotes 60 s, daily charts 1 h, weekly/monthly or max-range charts
// 12 h) and concurrent identical requests share one fetch. A failure is an
// *UnavailableError (errors.Is ErrUnavailable) with a reason; nothing is
// fabricated, and response bodies are never logged.
//
// Consumers depend on Source; yahootest has an in-memory fake and a fake
// Yahoo server for integration tests.
package yahoo

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
	_ "time/tzdata" // exchange time zones must not depend on the host's zoneinfo
)

// Source is the Yahoo feed consumers depend on (app.Deps.Yahoo).
type Source interface {
	// Chart is one symbol's bars for a Yahoo range (1d 5d 1mo 3mo 6mo 1y 2y 5y
	// 10y ytd max) and interval (1m 2m 5m 15m 30m 60m 90m 1h 1d 5d 1wk 1mo 3mo).
	// "max" is fetched as the full lifetime window at the interval asked for
	// (Yahoo's own range=max downgrades daily and weekly to monthly bars).
	Chart(ctx context.Context, symbol, rng, interval string) (Chart, error)
	// Quotes is the latest regular-market quote of each symbol Yahoo serves,
	// keyed by the normalized (trimmed, upper-case) requested symbol. A symbol
	// missing from the map is unavailable (not served, or its batch failed). A
	// non-nil error reports a batch that failed (or the breaker), so it can
	// come with a partial map. Prices are regularMarketPrice as Yahoo rounds
	// it (FX pairs to 4 decimals: use FX for rates).
	Quotes(ctx context.Context, symbols []string) (map[string]Quote, error)
	// Quote is one symbol's quote, or an *UnavailableError saying why not.
	Quote(ctx context.Context, symbol string) (Quote, error)
	// FX is the USD value of one unit of ccy ("SGD" → SGDUSD=X, its newest
	// daily close at full precision); USD is 1. Minor units are divided out
	// ("GBp" → GBPUSD=X / 100).
	FX(ctx context.Context, ccy string) (Rate, error)
	// FXHistory is ccy's daily USD rate (the FX pair's daily closes, oldest
	// first) over a Yahoo range, for ToUSD. USD needs no conversion: nil, nil.
	FXHistory(ctx context.Context, ccy, rng string) ([]Point, error)
}

// Batcher is the optional side of a Source that says whether its quotes are
// batched right now (Client.Batched): the quote-feed poller reads only what
// matters while each quote costs a request of its own.
type Batcher interface{ Batched() bool }

type noCache struct{}

// NoCache marks ctx so a Client's Chart keeps nothing it fetches (an answer
// already cached is still served): for batch jobs that read each symbol once,
// such as the screener's weekday run, whose lifetime charts would otherwise crowd
// the shared cache.
func NoCache(ctx context.Context) context.Context { return context.WithValue(ctx, noCache{}, true) }

type callCount struct{}

// WithCallCount marks ctx so that every request sent to Yahoo on its behalf
// adds one to n: a Client counts each HTTP attempt (retries included; an
// answer from its cache, or from a fetch another caller started, sends none),
// the fake each request it would send. Batch jobs report their Yahoo calls
// with it.
func WithCallCount(ctx context.Context, n *atomic.Int64) context.Context {
	return context.WithValue(ctx, callCount{}, n)
}

// CountCall adds one to ctx's call count, if WithCallCount set one: a Source
// calls it for each request it sends.
func CountCall(ctx context.Context) {
	if n, ok := ctx.Value(callCount{}).(*atomic.Int64); ok && n != nil {
		n.Add(1)
	}
}

// ErrUnavailable is what every *UnavailableError Is.
var ErrUnavailable = errors.New("yahoo data unavailable")

// UnavailableError is a Yahoo answer that could not be had. Reason is fixed
// text written here (never Yahoo's own error text) and safe to show.
type UnavailableError struct {
	Symbol   string // "" for a whole batch
	Reason   string // e.g. "not found on Yahoo", "rate limited by Yahoo; paused until 09:14 UTC"
	NotFound bool   // Yahoo does not serve the symbol (or it is not a valid Yahoo symbol)
	Paused   bool   // the rate-limit breaker is open: every call fails at once until it closes
}

func (e *UnavailableError) Error() string {
	if e.Symbol == "" {
		return "yahoo: unavailable: " + e.Reason
	}
	return "yahoo: " + e.Symbol + " unavailable: " + e.Reason
}

func (e *UnavailableError) Is(target error) bool { return target == ErrUnavailable }

// IsPaused reports whether err is the open rate-limit breaker's: nothing
// reached Yahoo, and nothing will until the pause ends.
func IsPaused(err error) bool {
	var u *UnavailableError
	return errors.As(err, &u) && u.Paused
}

// IsNotFound reports whether err says Yahoo does not serve the symbol.
func IsNotFound(err error) bool {
	var u *UnavailableError
	return errors.As(err, &u) && u.NotFound
}

func unavailable(symbol, format string, a ...any) *UnavailableError {
	return &UnavailableError{Symbol: symbol, Reason: fmt.Sprintf(format, a...)}
}

func notFound(symbol string) *UnavailableError {
	return &UnavailableError{Symbol: symbol, Reason: "not found on Yahoo", NotFound: true}
}

// Chart is one chart answer: the listing's meta and its bars, oldest first.
type Chart struct {
	Meta     Meta   `json:"meta"`
	Range    string `json:"range"`
	Interval string `json:"interval"`
	Bars     []Bar  `json:"bars"`
}

// Meta is chart.result[0].meta. Optional fields are nil when Yahoo sent none.
type Meta struct {
	Symbol           string     `json:"symbol"`
	Currency         string     `json:"currency"`         // as Yahoo labels it: "USD", "SGD", "GBp" (pence)
	ExchangeName     string     `json:"exchangeName"`     // "NCM", "TAI", "SES"
	FullExchangeName string     `json:"fullExchangeName"` // "NasdaqCM", "Taiwan", "SES"
	InstrumentType   string     `json:"instrumentType"`   // "EQUITY", "ETF", "INDEX", "CURRENCY"
	Timezone         string     `json:"exchangeTimezoneName"`
	LongName         string     `json:"longName,omitempty"`
	ShortName        string     `json:"shortName,omitempty"`
	Price            *float64   `json:"regularMarketPrice"`
	PriceTime        *time.Time `json:"regularMarketTime"`
	PreviousClose    *float64   `json:"previousClose"` // chartPreviousClose for ranges over a day
	FiftyTwoWeekHigh *float64   `json:"fiftyTwoWeekHigh"`
	FiftyTwoWeekLow  *float64   `json:"fiftyTwoWeekLow"`
	// ChartPreviousClose is the close before the chart's first bar: on a 1d
	// range (spark quotes) the previous session's close, on longer ranges older.
	ChartPreviousClose *float64 `json:"chartPreviousClose"`
	// DayHigh, DayLow and DayVolume are the latest regular session's (the
	// one regularMarketTime is in); nil when Yahoo sent none.
	DayHigh        *float64   `json:"regularMarketDayHigh"`
	DayLow         *float64   `json:"regularMarketDayLow"`
	DayVolume      *float64   `json:"regularMarketVolume"`
	FirstTradeDate *time.Time `json:"firstTradeDate"`
	RegularStart   *time.Time `json:"regularStart"` // currentTradingPeriod.regular
	RegularEnd     *time.Time `json:"regularEnd"`
}

// Name is the long name, else the short name, else the symbol.
func (m Meta) Name() string {
	for _, s := range []string{m.LongName, m.ShortName} {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return m.Symbol
}

// Exchange is the full exchange name, else the short one.
func (m Meta) Exchange() string {
	if m.FullExchangeName != "" {
		return m.FullExchangeName
	}
	return m.ExchangeName
}

// Location is the exchange's time zone; UTC when unknown.
func (m Meta) Location() *time.Location {
	if loc, err := time.LoadLocation(m.Timezone); err == nil && m.Timezone != "" {
		return loc
	}
	return time.UTC
}

// Bar is one bar: OHLC split-adjusted as Yahoo serves it, Adj the dividend-
// and split-adjusted close (0 when Yahoo sent none, e.g. intraday), V 0 when
// Yahoo sent no volume. T is the bar's open, UTC. Bars with a null or
// non-positive O/H/L/C are skipped, never filled in.
type Bar struct {
	T   time.Time `json:"t"`
	O   float64   `json:"o"`
	H   float64   `json:"h"`
	L   float64   `json:"l"`
	C   float64   `json:"c"`
	V   float64   `json:"v"`
	Adj float64   `json:"adj,omitempty"`
}

// Quote is a symbol's latest regular-market price.
type Quote struct {
	Symbol   string    `json:"symbol"` // as requested, normalized
	Price    float64   `json:"price"`  // regularMarketPrice, in Currency
	Time     time.Time `json:"time"`   // regularMarketTime
	Currency string    `json:"currency"`
	Meta     Meta      `json:"meta"`
	Via      string    `json:"via"` // "spark" (batched) or "chart" (per-symbol fallback)
}

// Rate is the USD value of one unit of Ccy.
type Rate struct {
	Ccy    string    `json:"ccy"`
	USD    float64   `json:"usd"`
	Time   time.Time `json:"time"`   // the FX bar's time (the live bar's latest tick); zero for USD
	Symbol string    `json:"symbol"` // "SGDUSD=X"; "" for USD
}

// Point is one dated value (an FX close, a weekly close).
type Point struct {
	T time.Time `json:"t"`
	V float64   `json:"v"`
}

// Disabled is a Source whose every call is unavailable ("not configured"):
// what app.Deps.YahooSource returns when no client is wired.
var Disabled Source = disabled{}

type disabled struct{}

func off(symbol string) error {
	return &UnavailableError{Symbol: symbol, Reason: "Yahoo feed not configured"}
}

func (disabled) Chart(_ context.Context, symbol, _, _ string) (Chart, error) {
	return Chart{}, off(symbol)
}
func (disabled) Quotes(context.Context, []string) (map[string]Quote, error) { return nil, off("") }
func (disabled) Quote(_ context.Context, symbol string) (Quote, error)      { return Quote{}, off(symbol) }
func (disabled) FX(_ context.Context, ccy string) (Rate, error)             { return Rate{}, off(ccy) }
func (disabled) FXHistory(_ context.Context, ccy, _ string) ([]Point, error) {
	return nil, off(ccy)
}

// Normalize trims and upper-cases a Yahoo symbol and checks it is 1–24 of
// A–Z 0–9 . - ^ = & _ (2330.TW, ^GSPC, SGDUSD=X, BRK-B, M&M.NS).
func Normalize(symbol string) (string, bool) {
	s := strings.ToUpper(strings.TrimSpace(symbol))
	if s == "" || len(s) > 24 {
		return "", false
	}
	for _, r := range s {
		if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune(".-^=&_", r)) {
			return "", false
		}
	}
	return s, true
}

var ranges = map[string]bool{"1d": true, "5d": true, "1mo": true, "3mo": true, "6mo": true, "1y": true, "2y": true,
	"5y": true, "10y": true, "ytd": true, "max": true}

var intervals = map[string]bool{"1m": true, "2m": true, "5m": true, "15m": true, "30m": true, "60m": true, "90m": true,
	"1h": true, "1d": true, "5d": true, "1wk": true, "1mo": true, "3mo": true}

// ValidRange and ValidInterval report whether Chart accepts the value.
func ValidRange(rng string) bool         { return ranges[rng] }
func ValidInterval(interval string) bool { return intervals[interval] }

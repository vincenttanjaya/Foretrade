// Package yahootest fakes the Yahoo feed for tests. Fake is an in-memory
// yahoo.Source (apptest wires one as app.Deps.Yahoo); Fake.Serve answers the
// v8 chart and v7 spark endpoints from the same data, for YAHOO_BASE in the
// integration suite. A symbol nobody Set is not found, as on Yahoo.
package yahootest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo"
)

// End is the newest bar Daily builds: Monday 2026-10-05 at the NYSE open.
var End = time.Date(2026, 10, 5, 13, 30, 0, 0, time.UTC)

type Fake struct {
	mu        sync.Mutex
	charts    map[string]yahoo.Chart
	errs      map[string]error
	calls     map[string]int
	batches   []int // symbols per Quotes call
	sparks    []int // symbols per served spark request
	unbatched bool
}

var (
	_ yahoo.Source  = (*Fake)(nil)
	_ yahoo.Batcher = (*Fake)(nil)
)

func New() *Fake {
	return &Fake{charts: map[string]yahoo.Chart{}, errs: map[string]error{}, calls: map[string]int{}}
}

// Daily is a chart of one bar per weekday ending at End with the given
// closes (O = C, H = C×1.01, L = C×0.99, Adj = C, V 1000), its meta priced at
// the last close and time. NYSE meta unless the currency says otherwise.
func Daily(symbol, currency string, closes ...float64) yahoo.Chart {
	bars := make([]yahoo.Bar, len(closes))
	t := End
	for i := len(closes) - 1; i >= 0; i-- {
		c := closes[i]
		bars[i] = yahoo.Bar{T: t, O: c, H: c * 1.01, L: c * 0.99, C: c, V: 1000, Adj: c}
		for t = t.AddDate(0, 0, -1); t.Weekday() == time.Saturday || t.Weekday() == time.Sunday; t = t.AddDate(0, 0, -1) {
		}
	}
	m := yahoo.Meta{Symbol: strings.ToUpper(symbol), Currency: currency, ExchangeName: "NYQ", FullExchangeName: "NYSE",
		InstrumentType: "EQUITY", Timezone: "America/New_York", ShortName: strings.ToUpper(symbol)}
	if strings.HasSuffix(m.Symbol, "=X") {
		m.ExchangeName, m.FullExchangeName, m.InstrumentType, m.Timezone = "CCY", "CCY", "CURRENCY", "Europe/London"
	}
	if n := len(bars); n > 0 {
		p, at := bars[n-1].C, bars[n-1].T
		m.Price, m.PriceTime = &p, &at
		first := bars[0].T
		m.FirstTradeDate = &first
	}
	return yahoo.Chart{Meta: m, Bars: bars}
}

// Set stores c under its Meta.Symbol; Chart answers it for any range and
// interval (all of its bars).
func (f *Fake) Set(c yahoo.Chart) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.charts[strings.ToUpper(c.Meta.Symbol)] = c
}

// SetErr makes every call for symbol fail with err; nil clears it.
func (f *Fake) SetErr(symbol string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil {
		delete(f.errs, strings.ToUpper(symbol))
	} else {
		f.errs[strings.ToUpper(symbol)] = err
	}
}

// SetFX stores ccy's "{CCY}USD=X" pair at usd per unit (30 daily bars).
func (f *Fake) SetFX(ccy string, usd float64) {
	closes := make([]float64, 30)
	for i := range closes {
		closes[i] = usd
	}
	f.Set(Daily(strings.ToUpper(ccy)+"USD=X", "USD", closes...))
}

// Calls is how many Source calls or HTTP requests named symbol.
func (f *Fake) Calls(symbol string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[strings.ToUpper(symbol)]
}

// Batches is the symbol count of every Quotes call so far, in order.
func (f *Fake) Batches() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.batches)
}

// Sparks is the symbol count of every spark request Serve answered, in order.
func (f *Fake) Sparks() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.sparks)
}

// SetBatched sets what Batched answers (true until set): false is a client
// whose spark reads are off, so each quote would cost a chart read.
func (f *Fake) SetBatched(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unbatched = !on
}

// Batched implements yahoo.Batcher.
func (f *Fake) Batched() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.unbatched
}

func (f *Fake) get(symbol string) (yahoo.Chart, error) {
	sym, ok := yahoo.Normalize(symbol)
	if !ok {
		return yahoo.Chart{}, &yahoo.UnavailableError{Symbol: symbol, Reason: "not a valid Yahoo symbol", NotFound: true}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[sym]++
	if err := f.errs[sym]; err != nil {
		return yahoo.Chart{}, err
	}
	c, ok := f.charts[sym]
	if !ok {
		return yahoo.Chart{}, &yahoo.UnavailableError{Symbol: sym, Reason: "not found on Yahoo", NotFound: true}
	}
	c.Bars = slices.Clone(c.Bars)
	return c, nil
}

// Chart, Quote, FX and FXHistory each count one call on a ctx marked with
// yahoo.WithCallCount, and Quotes one per batch of 20, as the client's
// requests would.
func (f *Fake) Chart(ctx context.Context, symbol, rng, interval string) (yahoo.Chart, error) {
	if !yahoo.ValidRange(rng) || !yahoo.ValidInterval(interval) {
		return yahoo.Chart{}, fmt.Errorf("yahoo: invalid range %q or interval %q", rng, interval)
	}
	yahoo.CountCall(ctx)
	c, err := f.get(symbol)
	c.Range, c.Interval = rng, interval
	return c, err
}

func (f *Fake) Quote(ctx context.Context, symbol string) (yahoo.Quote, error) {
	yahoo.CountCall(ctx)
	return f.quote(symbol)
}

func (f *Fake) quote(symbol string) (yahoo.Quote, error) {
	c, err := f.get(symbol)
	if err != nil {
		return yahoo.Quote{}, err
	}
	m := c.Meta
	if m.Price == nil || m.PriceTime == nil || m.Currency == "" {
		return yahoo.Quote{}, &yahoo.UnavailableError{Symbol: m.Symbol, Reason: "Yahoo sent no regular-market price"}
	}
	return yahoo.Quote{Symbol: strings.ToUpper(strings.TrimSpace(symbol)), Price: *m.Price, Time: *m.PriceTime,
		Currency: m.Currency, Meta: m, Via: "fake"}, nil
}

func (f *Fake) Quotes(ctx context.Context, symbols []string) (map[string]yahoo.Quote, error) {
	f.mu.Lock()
	f.batches = append(f.batches, len(symbols))
	f.mu.Unlock()
	for range (len(symbols) + 19) / 20 {
		yahoo.CountCall(ctx)
	}
	out := map[string]yahoo.Quote{}
	var first error
	for _, s := range symbols {
		q, err := f.quote(s)
		switch {
		case err == nil:
			out[q.Symbol] = q
		case first == nil && !yahoo.IsNotFound(err):
			first = err
		}
	}
	return out, first
}

func (f *Fake) FX(ctx context.Context, ccy string) (yahoo.Rate, error) {
	code := strings.ToUpper(strings.TrimSpace(ccy))
	if code == "USD" {
		return yahoo.Rate{Ccy: "USD", USD: 1}, nil
	}
	yahoo.CountCall(ctx)
	c, err := f.get(code + "USD=X")
	if err != nil {
		return yahoo.Rate{}, err
	}
	if len(c.Bars) == 0 {
		return yahoo.Rate{}, &yahoo.UnavailableError{Symbol: c.Meta.Symbol, Reason: "Yahoo sent no FX rate"}
	}
	last := c.Bars[len(c.Bars)-1]
	return yahoo.Rate{Ccy: code, USD: last.C, Time: last.T, Symbol: c.Meta.Symbol}, nil
}

func (f *Fake) FXHistory(ctx context.Context, ccy, rng string) ([]yahoo.Point, error) {
	code := strings.ToUpper(strings.TrimSpace(ccy))
	if code == "USD" {
		return nil, nil
	}
	c, err := f.Chart(ctx, code+"USD=X", rng, "1d")
	if err != nil {
		return nil, err
	}
	out := make([]yahoo.Point, len(c.Bars))
	for i, b := range c.Bars {
		out[i] = yahoo.Point{T: b.T, V: b.C}
	}
	return out, nil
}

// Serve starts a fake Yahoo answering GET /v8/finance/chart/{symbol} and
// /v7/finance/spark?symbols= from f, closed when the test ends, and returns
// its URL. SetErr with a not-found error answers 404, any other 503.
func (f *Fake) Serve(t testing.TB) string {
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return srv.URL
}

const notFoundDoc = `{"chart":{"result":null,"error":{"code":"Not Found","description":"No data found, symbol may be delisted"}}}`

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case strings.HasPrefix(r.URL.Path, "/v8/finance/chart/"):
		c, err := f.get(strings.TrimPrefix(r.URL.Path, "/v8/finance/chart/"))
		if err != nil {
			status := http.StatusServiceUnavailable
			if yahoo.IsNotFound(err) {
				status = http.StatusNotFound
			}
			w.WriteHeader(status)
			write(w, notFoundDoc)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"chart": map[string]any{"result": []any{result(c)}, "error": nil}})
	case r.URL.Path == "/v7/finance/spark":
		var res []any
		syms := strings.Split(r.URL.Query().Get("symbols"), ",")
		f.mu.Lock()
		f.sparks = append(f.sparks, len(syms))
		f.mu.Unlock()
		for _, s := range syms {
			if c, err := f.get(s); err == nil {
				res = append(res, map[string]any{"symbol": c.Meta.Symbol, "response": []any{result(c)}})
			}
		}
		if len(res) == 0 {
			w.WriteHeader(http.StatusNotFound)
			write(w, `{"spark":{"result":null,"error":{"code":"Not Found","description":"No data found for spark symbols"}}}`)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"spark": map[string]any{"result": res, "error": nil}})
	default:
		w.WriteHeader(http.StatusNotFound)
		write(w, `{"finance":{"result":null,"error":{"code":"Not Found"}}}`)
	}
}

func write(w http.ResponseWriter, s string) { w.Write([]byte(s)) }

// result is c in Yahoo's chart.result[0] shape.
func result(c yahoo.Chart) map[string]any {
	m := c.Meta
	meta := map[string]any{"symbol": m.Symbol, "currency": m.Currency, "exchangeName": m.ExchangeName,
		"fullExchangeName": m.FullExchangeName, "instrumentType": m.InstrumentType, "exchangeTimezoneName": m.Timezone,
		"longName": m.LongName, "shortName": m.ShortName, "regularMarketPrice": m.Price, "previousClose": m.PreviousClose,
		"fiftyTwoWeekHigh": m.FiftyTwoWeekHigh, "fiftyTwoWeekLow": m.FiftyTwoWeekLow, "chartPreviousClose": m.ChartPreviousClose,
		"regularMarketDayHigh": m.DayHigh, "regularMarketDayLow": m.DayLow, "regularMarketVolume": m.DayVolume,
		"regularMarketTime": unix(m.PriceTime), "firstTradeDate": unix(m.FirstTradeDate)}
	if m.RegularStart != nil && m.RegularEnd != nil {
		meta["currentTradingPeriod"] = map[string]any{"regular": map[string]any{"start": unix(m.RegularStart), "end": unix(m.RegularEnd)}}
	}
	n := len(c.Bars)
	ts, o, h, l, cl, v, adj := make([]int64, n), make([]float64, n), make([]float64, n), make([]float64, n),
		make([]float64, n), make([]float64, n), make([]float64, n)
	for i, b := range c.Bars {
		ts[i], o[i], h[i], l[i], cl[i], v[i], adj[i] = b.T.Unix(), b.O, b.H, b.L, b.C, b.V, b.Adj
	}
	return map[string]any{"meta": meta, "timestamp": ts, "indicators": map[string]any{
		"quote":    []any{map[string]any{"open": o, "high": h, "low": l, "close": cl, "volume": v}},
		"adjclose": []any{map[string]any{"adjclose": adj}},
	}}
}

func unix(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.Unix()
}

// NotFound is the error SetErr takes for a symbol Yahoo does not serve.
func NotFound(symbol string) error {
	return &yahoo.UnavailableError{Symbol: strings.ToUpper(symbol), Reason: "not found on Yahoo", NotFound: true}
}

// Down is an unavailable error that is not a not-found (an outage).
var Down error = &yahoo.UnavailableError{Reason: "Yahoo answered HTTP 503"}

package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/research"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo"
)

const (
	maxQuotes    = 6                // get_stock_quote calls per message
	quoteTimeout = 20 * time.Second // one call's Yahoo budget, pacing included
)

func (a *agentRun) quoteTool() llm.Tool {
	return llm.Tool{
		Name: "get_stock_quote",
		Description: "Latest quote and history for any listed stock, ETF or index by its Yahoo Finance ticker (TSLA, HSDT, 2330.TW, " +
			"D05.SI, 0700.HK, 7203.T, 005930.KS, ^GSPC), in or outside the paper universe: last price, currency " +
			"(plus a USD conversion), exchange, as-of time, previous close, 52-week high/low, 1y/3y/5y total returns, the all-time " +
			"high with its date and the drawdown from it, and 1y realized volatility. The price is the app's freshest: real-time " +
			"(near real-time on SGX) where the app reads the exchange's own feed for that listing, else Yahoo's delayed " +
			"regular-market price; priceSource and priceLabel say which and how far behind. It follows market hours: outside them " +
			"it is the last close. A universe stock or index symbol (SMSN, SP500) resolves to its underlying listing. At most 6 calls per message.",
		Schema: schema(map[string]any{
			"symbol": map[string]any{"type": "string", "maxLength": 24, "description": "Yahoo Finance ticker with its exchange suffix, e.g. TSLA, 2330.TW, D05.SI."},
		}, "symbol"),
		Run: a.stockQuote,
	}
}

// stockQuote is the measured read the scenario rules lean on: one Yahoo quote
// (the live price) and one lifetime daily chart (returns, ATH, volatility),
// fetched together. Nothing is estimated: a figure the data cannot support is
// null, and a symbol Yahoo cannot serve is a tool error that says so.
func (a *agentRun) stockQuote(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Symbol string `json:"symbol"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid input: %v", err)
	}
	requested, _ := yahoo.Normalize(in.Symbol)
	sym, paper := quoteSymbol(in.Symbol)
	switch {
	case sym == "":
		return "", errors.New("symbol must be a Yahoo Finance ticker: 1..24 characters of A-Z, 0-9 and . - ^ = & _, e.g. TSLA, 2330.TW, D05.SI, 0700.HK")
	case a.quotes >= maxQuotes:
		return "", fmt.Errorf("stock-quote limit reached (%d per message); answer from what you have", maxQuotes)
	}
	a.quotes++
	ctx, cancel := context.WithTimeout(ctx, quoteTimeout)
	defer cancel()
	y := a.d.YahooSource()
	var (
		q           yahoo.Quote
		ch          yahoo.Chart
		qErr, chErr error
		wg          sync.WaitGroup
	)
	wg.Go(func() { q, qErr = y.Quote(ctx, sym) })
	wg.Go(func() { ch, chErr = y.Chart(ctx, sym, "max", "1d") })
	wg.Wait()
	if qErr != nil && chErr != nil {
		return "", a.quoteErr(sym, qErr, chErr)
	}

	out := stockQuote{Symbol: sym, Source: yahooSource}
	if requested != sym {
		out.Requested = requested
	}
	meta := ch.Meta
	loc := meta.Location()
	var priceAt time.Time
	if qErr == nil {
		meta = q.Meta
		loc = meta.Location()
		out.Price, out.Currency, priceAt = &q.Price, q.Currency, q.Time
	} else if ch.Meta.Price != nil && ch.Meta.PriceTime != nil { // the history call carries the same regular-market fields
		out.Price, out.Currency, priceAt = ch.Meta.Price, ch.Meta.Currency, *ch.Meta.PriceTime
	} else {
		out.Currency = ch.Meta.Currency
		out.Notes = append(out.Notes, "No price: "+reason(qErr)+".")
	}
	out.PreviousClose = meta.PreviousClose
	if out.Price != nil {
		out.PriceSource, out.PriceLabel = "yahoo", market.QuoteLabel(market.ClassDelayed, 0, priceAt)
	}
	// The market layer's freshest quote of a listing it reads (a quote feed, a
	// listed row, a screener row) when it is a real-time source's and newer.
	if lq, ok := a.listingQuote(sym); ok && lq.Source != "yahoo" && (out.Price == nil || lq.Currency == out.Currency && !lq.LastTradeAt.Before(priceAt)) {
		if out.Price != nil && !sameLocalDay(priceAt, lq.LastTradeAt, loc) {
			prev := *out.Price // Yahoo, minutes behind, still shows the last session's close
			out.PreviousClose = &prev
		}
		price := lq.Price
		out.Price, out.Currency, priceAt = &price, lq.Currency, lq.LastTradeAt
		out.PriceSource, out.PriceLabel = lq.Source, lq.Label
		out.Source = "price: " + lq.Source + ", a keyless " + classWord(lq.Class) + " feed of the exchange's trades (unofficial); " +
			"everything else: " + yahooSource
	}
	if out.Price != nil {
		out.at(priceAt, loc)
	}
	out.Name, out.Exchange, out.Type, out.Timezone = clip(meta.Name(), 120), meta.Exchange(), meta.InstrumentType, meta.Timezone
	out.High52, out.Low52 = cmpPtr(meta.FiftyTwoWeekHigh, ch.Meta.FiftyTwoWeekHigh), cmpPtr(meta.FiftyTwoWeekLow, ch.Meta.FiftyTwoWeekLow)
	if out.Price != nil && out.PreviousClose != nil && *out.PreviousClose > 0 {
		out.ChangePct = ptr(round2((*out.Price / *out.PreviousClose - 1) * 100))
	}
	if minorUnit(out.Currency) {
		out.Notes = append(out.Notes, out.Currency+" is a minor unit: prices are in 1/100 of the main currency.")
	}
	if out.Price != nil && out.Currency != "" && out.Currency != "USD" {
		if r, err := y.FX(ctx, out.Currency); err == nil && r.USD > 0 {
			out.USD = &usdQuote{Price: round4(*out.Price * r.USD), Rate: r.USD, Pair: r.Symbol, RateAsOf: r.Time.UTC().Format("2006-01-02")}
		} else {
			out.Notes = append(out.Notes, "No USD conversion: the "+out.Currency+" rate is unavailable ("+reason(err)+").")
		}
	}

	if chErr != nil || len(ch.Bars) == 0 {
		out.Notes = append(out.Notes, "History unavailable ("+reason(chErr)+"): returns, all-time high and volatility are null.")
	} else {
		bars := ch.Bars
		first, last := bars[0].T.In(loc), bars[len(bars)-1].T.In(loc)
		out.History = &quoteHistory{From: first.Format("2006-01-02"), To: last.Format("2006-01-02"), DailyBars: len(bars)}
		r := yahoo.TrailingReturns(bars)
		out.Returns = &yahoo.Returns{Y1: returnPtr(r.Y1), Y3: returnPtr(r.Y3), Y5: returnPtr(r.Y5)}
		out.Volatility = roundPtr(yahoo.VolatilityPct(bars))
		if ath, ok := yahoo.AllTimeHigh(bars); ok {
			out.ATH = &athRead{High: ath.High, Date: ath.At.In(loc).Format("2006-01-02"),
				Basis: "highest split-adjusted daily high in Yahoo's history since " + out.History.From}
			if out.Price != nil && *out.Price > ath.High && out.AsOf != nil {
				out.ATH = &athRead{High: *out.Price, Date: (*out.AsOf)[:10], Basis: "the latest price, above every daily high since " + out.History.From}
			}
			switch {
			case out.Price != nil && out.ATH.High > 100**out.Price:
				// Reverse splits scale old prices up (HSDT's split-adjusted 2017 high
				// reads in the millions), so the figure is withheld, not shown as −100%.
				out.ATH = nil
				out.Notes = append(out.Notes, "All-time high and drawdown withheld: the split-adjusted lifetime high in this history is over 100× the price, "+
					"which reverse splits and dilution produce, so it is not a usable figure: this listing's all-time high is unavailable. "+
					"Multi-year returns span the same splits.")
			case out.Price != nil:
				out.Drawdown = ptr(round2(yahoo.DrawdownPct(*out.Price, out.ATH.High)))
			}
		}
	}

	out.Paper = paperRead(requested, paper)
	a.summary = quoteSummary(out)
	a.recordNums(out)
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	head := "Yahoo Finance quote for " + sym
	switch {
	case out.PriceSource != "" && out.PriceSource != "yahoo":
		head = "Quote for " + sym + " (price " + out.PriceLabel + " from " + out.PriceSource + ", a keyless " +
			classWord(a.priceClass(sym)) + " feed; everything else Yahoo Finance): measured data as of the times shown; cite the price as " +
			out.PriceSource + "'s and the rest as Yahoo Finance, each with its as-of time. "
	case out.PriceLabel != "":
		head += " (price " + out.PriceLabel + "): measured data as of the times shown; cite it as Yahoo Finance with its as-of date. "
	default:
		head += ": measured data as of the times shown; cite it as Yahoo Finance with its as-of date. "
	}
	return head + "Names and exchange labels are the provider's text.\n" + research.Untrusted(string(b)), nil
}

// yahooSource is the quote tool's provenance for Yahoo's figures.
const yahooSource = "Yahoo Finance (keyless, unofficial; regular-market prices, delayed, follow market hours)"

// listingQuote is the market layer's freshest quote of ticker, if it reads
// that listing.
func (a *agentRun) listingQuote(ticker string) (market.ListingQuote, bool) {
	read := a.listings
	if read == nil {
		if a.d == nil || a.d.Market == nil {
			return market.ListingQuote{}, false
		}
		read = a.d.Market.ListingQuotes
	}
	q, ok := read([]string{ticker})[ticker]
	return q, ok
}

// priceClass is the class of the market layer's quote of ticker.
func (a *agentRun) priceClass(ticker string) string {
	q, _ := a.listingQuote(ticker)
	return q.Class
}

// classWord names a price class for the tool's text.
func classWord(class string) string {
	if class == market.ClassNear {
		return "near-real-time"
	}
	return "real-time"
}

// sameLocalDay reports whether a and b are the same date in loc.
func sameLocalDay(a, b time.Time, loc *time.Location) bool {
	return a.In(loc).Format(time.DateOnly) == b.In(loc).Format(time.DateOnly)
}

// quoteErr is the tool error for a symbol neither call could serve: not
// listed on Yahoo, or the feed unavailable. Reasons are the client's fixed
// text, never Yahoo's.
func (a *agentRun) quoteErr(sym string, qErr, chErr error) error {
	if yahoo.IsNotFound(qErr) && yahoo.IsNotFound(chErr) {
		a.summary = "No Yahoo listing " + sym
		return fmt.Errorf("Yahoo Finance has no listing %q. Check the ticker and its exchange suffix (2330.TW, D05.SI, 0700.HK, "+
			"7203.T, 005930.KS) or use web_search; never estimate its price", sym)
	}
	err := qErr
	if yahoo.IsNotFound(qErr) { // the history call's outage is the reason
		err = chErr
	}
	if !errors.Is(err, yahoo.ErrUnavailable) {
		slog.Warn("chat get_stock_quote", "symbol", sym, "err", err)
	}
	a.summary = "Quote unavailable"
	return fmt.Errorf("Yahoo Finance data for %s is unavailable right now (%s); say so and answer without a live quote, never estimating one", sym, reason(err))
}

// stockQuote is get_stock_quote's result; null means the data cannot support
// the figure.
type stockQuote struct {
	Symbol        string         `json:"symbol"`
	Requested     string         `json:"requested,omitempty"` // a universe symbol that resolved to its listing
	Name          string         `json:"name"`
	Exchange      string         `json:"exchange"`
	Type          string         `json:"instrumentType"`
	Timezone      string         `json:"timezone"`
	Currency      string         `json:"currency"`              // as Yahoo labels it (GBp is pence)
	Price         *float64       `json:"price"`                 // the freshest price the app has (PriceSource)
	AsOf          *string        `json:"asOf"`                  // its last trade, RFC 3339 in the exchange's zone
	PriceSource   string         `json:"priceSource,omitempty"` // "yahoo", or the real-time feed's name ("naver")
	PriceLabel    string         `json:"priceLabel,omitempty"`  // "real-time · 04:03:12 UTC" | "about 2 min behind · 04:03 UTC" | "delayed · last trade 04:03 UTC"
	PreviousClose *float64       `json:"previousClose"`
	ChangePct     *float64       `json:"changePct"`
	USD           *usdQuote      `json:"usd"` // nil for a USD listing or without a rate
	High52        *float64       `json:"fiftyTwoWeekHigh"`
	Low52         *float64       `json:"fiftyTwoWeekLow"`
	Returns       *yahoo.Returns `json:"returnsPct"` // to the last daily close
	Volatility    *float64       `json:"volatility1yPct"`
	ATH           *athRead       `json:"allTimeHigh"`
	Drawdown      *float64       `json:"drawdownFromAthPct"`
	History       *quoteHistory  `json:"history"`
	Paper         paperStatus    `json:"paper"`
	Notes         []string       `json:"notes,omitempty"`
	Source        string         `json:"source"`
}

type usdQuote struct {
	Price    float64 `json:"price"`
	Rate     float64 `json:"rate"` // USD per unit, the pair's newest daily close
	Pair     string  `json:"pair"`
	RateAsOf string  `json:"rateAsOf"`
}

type athRead struct {
	High  float64 `json:"high"`
	Date  string  `json:"date"` // exchange-local
	Basis string  `json:"basis"`
}

type quoteHistory struct {
	From      string `json:"from"`
	To        string `json:"to"` // the last daily close returns run to
	DailyBars int    `json:"dailyBars"`
}

type paperStatus struct {
	Symbol *string `json:"symbol"` // the universe asset that trades this listing, null when research only
	Note   string  `json:"note"`
}

func (q *stockQuote) at(t time.Time, loc *time.Location) {
	s := t.In(loc).Format(time.RFC3339)
	q.AsOf = &s
}

// quoteSymbol is the Yahoo ticker for raw ("" when it cannot be one) and the
// universe asset trading that listing. A universe stock or index symbol
// resolves to its underlying listing (SMSN → 005930.KS, SP500 → ^GSPC).
func quoteSymbol(raw string) (string, *market.AssetInfo) {
	sym, ok := yahoo.Normalize(raw)
	if !ok {
		return "", nil
	}
	if info, ok := market.Lookup(sym); ok && info.Perp() && info.Listing != nil && info.Listing.Yahoo != "" {
		sym = info.Listing.Yahoo
	}
	if x, ok := market.ByYahoo(sym); ok {
		return sym, &x
	}
	return sym, nil
}

// paperRead says how paper trading relates to the listing: which asset trades
// it and at what price, or research only (only the trade card is withheld).
func paperRead(requested string, info *market.AssetInfo) paperStatus {
	if info == nil {
		note := "Outside the paper universe: research only. Analyze it in full; only the trade card is withheld."
		if c, ok := market.LookupCrypto(requested); ok {
			note += " " + c.Symbol + " is also a universe crypto: this is the Yahoo listing above, not the coin (use get_asset_analysis for the coin)."
		}
		return paperStatus{Note: note}
	}
	s := info.Symbol
	if info.IsListed() {
		return paperStatus{Symbol: &s, Note: "Paper-tradable as " + s + " on the Trade page (/trade/" + s + "): fills use its freshest quote, the price above (Yahoo's delayed regular-market trade unless priceSource names a real-time feed; outside market hours, the last close). This chat's trade and alert tools do not take listed tickers yet."}
	}
	if info.Venue == "yahoo" {
		return paperStatus{Symbol: &s, Note: "Paper-tradable as " + s + ", filled at its freshest quote, the price above (Yahoo's delayed regular-market price unless priceSource names a real-time feed; outside market hours, the last close)."}
	}
	return paperStatus{Symbol: &s, Note: "Paper-tradable as " + s + ", its Hyperliquid perp: paper fills use the perp's USD mark (get_asset_analysis), not this listing's price."}
}

func quoteSummary(q stockQuote) string {
	if q.Price == nil {
		return q.Symbol + " · no live price"
	}
	s := q.Symbol + " " + money(*q.Price, q.Currency)
	if q.Drawdown != nil {
		s += fmt.Sprintf(" · %+.1f%% from ATH", *q.Drawdown)
	}
	return s
}

// money is a price for chips: $142.31 in USD, else "1,450 TWD".
func money(x float64, ccy string) string {
	if ccy == "USD" || ccy == "" {
		return usd(x)
	}
	return strings.TrimPrefix(usd(x), "$") + " " + ccy
}

// minorUnit: Yahoo quotes these listings in 1/100 of the currency.
func minorUnit(ccy string) bool {
	switch ccy {
	case "GBp", "GBX", "ZAc", "ILA":
		return true
	}
	return false
}

// reason is an unavailable error's fixed text, safe to show.
func reason(err error) string {
	var u *yahoo.UnavailableError
	if errors.As(err, &u) && u.Reason != "" {
		return u.Reason
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "Yahoo timed out"
	}
	return "Yahoo data unavailable"
}

func cmpPtr(a, b *float64) *float64 {
	if a != nil {
		return a
	}
	return b
}

func ptr(x float64) *float64 { return &x }

func roundPtr(x *float64) *float64 {
	if x == nil {
		return nil
	}
	return ptr(round2(*x))
}

// returnPtr rounds a return like roundPtr, except that a near-total loss
// (HSDT's 5 years: −99.996%) reads −99.99, never a −100 a listing that still
// trades cannot have.
func returnPtr(x *float64) *float64 {
	r := roundPtr(x)
	if r != nil && *r <= -100 && *x > -100 {
		*r = -99.99
	}
	return r
}

func round4(x float64) float64 { return math.Round(x*1e4) / 1e4 }

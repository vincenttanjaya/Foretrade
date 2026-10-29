package rtquote

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// YahooTW is TWSE's second real-time source, behind MIS and gated by
// QUOTES_REALTIME_TWSE_YAHOO (GateTWSEYahoo): Yahoo奇摩股市's internal quote
// API, the one its own pages call. One GET carries every listing:
// /_td-stock/api/resource/StockServices.stockList;symbols=2330.TW%2C2317.TW%2C...
// on tw.stock.yahoo.com (52 symbols, about 102 KB). The research's
// fields=avgPrice,orderbook is left out: our reads without it got the same
// fields and size. Its
// symbols are our Yahoo tickers. No key, cookie, crumb or Referer: our two
// reads with UserAgent alone on 2026-10-14 (12:38:58 and 12:40:01 UTC, after
// the close) got HTTP 200 and every symbol asked.
//
// Fields read, per row: symbol, exchangeDataDelayedBy (0 for TWSE, real
// time; the same API gives its HK quotes 15, so any other value is
// rejected), price.raw (the last trade), regularMarketTime (its UTC time),
// currency, volume (the cumulative volume in shares), limitUpPrice.raw and
// limitDownPrice.raw (a price outside them is rejected) and marketStatus
// ("open" on all 429 rows of the research's in-session reads, "close" on all
// 52 of ours after the close). An answer with no row "open" is Closed. A
// trade later than twLastPrint (13:35 Taipei) is rejected: after the
// after-hours fixed-price session it dates a row 14:30:00 with that
// session's volume added (46 of 52 rows on 2026-10-14; the price is the
// close). The answer has no server clock, so Batch.Stamp stays zero.
//
// It is real time: TSMC's trade time was 1-13 s old at the research's read
// over 10 reads, the batch medians 3-6 s (research tw_yahootw_lags.txt),
// against MIS's 13-93 s and Yahoo's 20 minutes in the same rounds; TSMC
// 2525 at 04:43:40 matched Yahoo's delayed 1-minute bar for 04:43 (closed at
// 2525.0, xcheck_tw_yahootw_vs_delayed_bars.txt). No refusal in the
// research's 14 reads of the host or in our 2.
//
// Canaries, interval, floor, windows and Settle are MIS's: the same venue,
// so its settled read comes at the same time whichever source serves it.
//
// Terms: tw.stock.yahoo.com/robots.txt has "Disallow: /_td-stock" for every
// user agent, and Yahoo's terms ban automated access or collection
// (§2.2(d)9) and commercial reuse (§2.3(e),
// legal.yahoo.com/e2/zh-hant/yahoo/terms/otos/index.html). The owner
// accepted that risk on 2026-10-14; QUOTES_REALTIME_TWSE_YAHOO=off removes
// this source alone.
type YahooTW struct{}

func (YahooTW) Info() Info {
	in := MIS{}.Info()
	in.Name, in.Gate, in.Host = "yahoo-tw", GateTWSEYahoo, "https://tw.stock.yahoo.com"
	return in
}

func (YahooTW) Symbol(ticker string) (string, bool) {
	if !twTicker.MatchString(ticker) {
		return "", false
	}
	return ticker, true
}

func (YahooTW) Request(ctx context.Context, host string, want Want) (*http.Request, error) {
	if len(want) == 0 {
		return nil, fmt.Errorf("yahoo-tw: nothing to ask for")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		host+"/_td-stock/api/resource/StockServices.stockList;symbols="+strings.Join(want.Symbols(), "%2C"), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	return req, nil
}

// ytwRow is the part of a stockList row YahooTW reads.
type ytwRow struct {
	Symbol    string   `json:"symbol"`
	DelayedBy *float64 `json:"exchangeDataDelayedBy"`
	Price     *ytwRaw  `json:"price"`
	Time      string   `json:"regularMarketTime"`
	Currency  string   `json:"currency"`
	Volume    *string  `json:"volume"`
	Up        *ytwRaw  `json:"limitUpPrice"`
	Down      *ytwRaw  `json:"limitDownPrice"`
	Status    string   `json:"marketStatus"`
}

// ytwRaw is one of its numbers: {"raw": "2510", "fmt": "2,510", "sort": 2510}.
type ytwRaw struct {
	Raw string `json:"raw"`
}

func (r *ytwRaw) raw() string {
	if r == nil {
		return ""
	}
	return r.Raw
}

func (YahooTW) Parse(body []byte, want Want) (Batch, error) {
	var rows []json.RawMessage
	if err := json.Unmarshal(body, &rows); err != nil {
		return Batch{}, fmt.Errorf("yahoo-tw: not a JSON list of quotes: %w", err)
	}
	if rows == nil {
		return Batch{}, fmt.Errorf("yahoo-tw: null")
	}
	var b Batch
	open := 0
	for _, raw := range rows {
		var r ytwRow
		bad := json.Unmarshal(raw, &r) // a field of the wrong type: the others still decode
		w, ok := want[r.Symbol]
		reject := func(why string) {
			b.Rejected = append(b.Rejected, Reject{Ticker: w.Ticker, Symbol: r.Symbol, Reason: why})
		}
		switch {
		case !ok:
			reject("not asked for")
			continue
		case bad != nil:
			reject("bad parse: " + bad.Error())
			continue
		}
		if r.DelayedBy == nil || *r.DelayedBy != 0 {
			delay := "missing"
			if r.DelayedBy != nil {
				delay = strconv.FormatFloat(*r.DelayedBy, 'f', -1, 64)
			}
			reject("not real time (exchangeDataDelayedBy " + delay + ")")
			continue
		}
		price, err := twDecimal(r.Price.raw())
		if err != nil {
			reject("price: " + err.Error())
			continue
		}
		at, err := time.Parse(time.RFC3339, r.Time)
		if err != nil {
			reject(fmt.Sprintf("regularMarketTime %q: not a time", r.Time))
			continue
		}
		if why, ok := twCheck(price, at, r.Up.raw(), r.Down.raw()); !ok {
			reject(why)
			continue
		}
		row := Row{Ticker: w.Ticker, Price: price, Currency: w.Currency, TradeAt: at.UTC()}
		if r.Currency != "" {
			row.Currency = r.Currency
		}
		if r.Volume != nil && *r.Volume != "" {
			v, err := strconv.ParseUint(*r.Volume, 10, 53)
			if err != nil {
				reject(fmt.Sprintf("volume %q is not a whole number of shares", *r.Volume))
				continue
			}
			vol := float64(v)
			row.Volume = &vol
		}
		b.Rows = append(b.Rows, row)
		if r.Status == "open" {
			open++
		}
	}
	b.Closed = len(b.Rows) > 0 && open == 0
	return b, nil
}

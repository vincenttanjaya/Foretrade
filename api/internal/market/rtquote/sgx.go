package rtquote

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"time"
)

// SGX is SGX's near-real-time source: the exchange's own securities API, the
// one sgx.com's price pages read. One GET carries the whole board:
// /securities/v1.1?excludetypes=bonds&params=nc,lt,trading_time,vl on
// api.sgx.com (all 1,286 SGX securities, about 120 KB, 13 KB gzipped,
// measured 2026-10-14). It needs no key, cookie, Origin, Referer or browser
// User-Agent: our two reads with Accept and the framework's UserAgent alone,
// at 12:19:59 and 12:24:59 UTC on 2026-10-14, each got HTTP 200 in about
// 1 s with all 31 of our codes (SGX closed: testdata/sgx_after_close.json
// is the first). The research's reads sent Origin, Referer and a browser
// User-Agent; this adapter sends none of them.
//
// It is a once-a-minute snapshot, not a trade feed, so its class is
// ClassNear ("about N min behind"): over 19 research reads in both sessions
// the snapshot was 41-125 s old at read time (median 99 s); a 1-minute bar
// check put its price at Yahoo's later close for the snapshot's own minute,
// or the one before, in 15 of 24 reads (scratchpad/rt/results/sg-my-id-ph.md).
// Answers are cached at the edge: two reads 48 s apart (r4, r5) got the same
// body, built at the same meta.processedTime. Yahoo, the fallback, is 10
// minutes behind on SGX.
//
// Fields read, per row of data.prices: nc (the SGX code, our ticker without
// .SI), lt (the last trade price, in the security's trading currency: SGD,
// or USD for J36, H78 and D01, which the API does not state, so the
// listing's currency stands), vl (the day's cumulative volume in thousands
// of shares, made shares in Row.Volume: D05's 3,020.76 at 08:28 UTC against
// 3,125,670 shares on Yahoo and TradingView at 08:32, and its value
// v / (vl × 1,000) = 74.25, the day's average price) and trading_time
// ("20261014_082000": the snapshot minute in UTC, read at 08:21:05 UTC).
// Our 31 codes are all present (s7), each once. The board lists a few codes
// twice, as stocks and as others, with the same price and an older time on
// the others row (B73, SJJR, ZNIR in s7); a code listed twice gives its
// newest row, and two rows of the same time that disagree are both
// rejected. meta.code must be "200"; meta.processedTime is the answer's
// build time (SGX's clock), against which a snapshot time in the future is
// rejected.
//
// Times (TimesSnapshot with a Volume): trading_time is the minute of the
// row's last refresh, not the security's own last trade, so a thin stock
// can show an old price under a fresh minute; the market layer therefore
// dates each row by the snapshot of the first read that showed its current
// volume. Each security type is refreshed on its own, so one answer can
// hold two adjacent minutes (s5: our 8 REITs at 08:16, our 23 stocks at
// 08:15; s6 alike; late1 the other way round): the answer's Stamp is the
// oldest minute among the rows it returns, never newer than a row's own,
// and a row more than 2 minutes older than the answer's middle minute (the
// median of our rows') is rejected as stale rather than drag every row back
// (none of ours ever was). A stale board (one whose minutes stop moving)
// keeps its rows dated by its old minute, so their delay shows, and the
// canaries' volumes stop moving: the market layer marks the source degraded
// after MaxLag. The minute keeps moving through the 12:00-13:00 break with
// no volume change (sgx_secall, r1, r2), so the break dates nothing new.
// The API has no session field: Closed is never set, and the verified SGX
// calendar decides when to poll.
//
// Schedule: every 30 s with a 30 s floor (fill re-reads included), so at
// most 2 requests a minute, 840 in a full day (09:00-12:00 and 13:00-17:00
// SGT) plus the settled read. That read comes 20 minutes after 17:00: the
// closing auction ends at a random moment in 17:05-17:06, trade-at-close
// runs at the closing price to 17:16, and the board then stops: our read
// at 12:19 UTC on 2026-10-14 found 1,236 of its rows, all 31 of ours, still
// at 20261014_091600 (17:16 SGT), so a read at 17:20 has the close and the
// day's whole volume. Quiet: the first 4 minutes after each open (09:00,
// 13:00), when the first continuous minutes' snapshots are still being
// published (up to 125 s after their minute): no health verdict there,
// beyond the market layer's 2-minute open grace.
//
// Canaries: the three largest of our SGX tickers by traded value (field v)
// in the research's last afternoon read (late3_sgx_secall, 08:29:58 UTC on
// 2026-10-14): DBS 224.3 million SGD, OCBC 153.9 million, UOB 114.3 million
// (next CapitaLand Ascendas REIT 61.1 million); the same three led at
// 05:17 (r9) and 08:21 (s7).
//
// Terms: sgx.com's robots.txt says "User-agent: * Allow: /"; api.sgx.com's
// robots.txt answers 403. The sgx.com terms page renders only with
// JavaScript and was not read; SGX's Market Data Policy
// (datadirect.sgx.com/Market-Data-Policy) says redistribution needs an SGX
// licence. The owner accepted that risk on 2026-10-14.
type SGX struct{}

var sgxTicker = regexp.MustCompile(`^([0-9A-Z]{3,4})\.SI$`)

// sgxPath is the one request's path and query: the whole board, bonds left
// out, only the four fields read.
const sgxPath = "/securities/v1.1?excludetypes=bonds&params=nc,lt,trading_time,vl"

// sgxStaleRow: a row this much older than the answer's middle minute (the
// median among our rows) is stale; the board's types are refreshed at most a
// minute apart.
const sgxStaleRow = 2 * time.Minute

func (SGX) Info() Info {
	return Info{Name: "sgx", Venue: "SGX", Switch: "sgx", Host: "https://api.sgx.com",
		Class: ClassNear, Times: TimesSnapshot, Every: 30 * time.Second, Floor: 30 * time.Second,
		Settle: 20 * time.Minute, MaxLag: 5 * time.Minute,
		Canaries: []string{"D05.SI", "O39.SI", "U11.SI"},
		Quiet:    []Window{{"09:00", "09:04"}, {"13:00", "13:04"}}} // the open's first snapshots are still on their way
}

func (SGX) Symbol(ticker string) (string, bool) {
	m := sgxTicker.FindStringSubmatch(ticker)
	if m == nil {
		return "", false
	}
	return m[1], true
}

func (SGX) Request(ctx context.Context, host string, want Want) (*http.Request, error) {
	if len(want) == 0 {
		return nil, fmt.Errorf("sgx: nothing to ask for")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, host+sgxPath, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	return req, nil
}

// sgxRow is the part of a board row SGX reads.
type sgxRow struct {
	Code        string          `json:"nc"`
	Last        json.RawMessage `json:"lt"`
	Volume      json.RawMessage `json:"vl"` // thousands of shares
	TradingTime string          `json:"trading_time"`
}

// sgxRead is one of our rows as read, before the answer-wide checks.
type sgxRead struct {
	w      Wanted
	price  float64
	volume float64
	at     time.Time
	twice  bool // two rows of the same time disagreed
}

func (SGX) Parse(body []byte, want Want) (Batch, error) {
	if t := bytes.TrimSpace(body); len(t) > 0 && t[0] == '<' {
		return Batch{}, Refused("sgx: an HTML page instead of the board (a block or challenge page)")
	}
	var doc struct {
		Meta *struct {
			Code          string `json:"code"`
			ProcessedTime *int64 `json:"processedTime"`
		} `json:"meta"`
		Data *struct {
			Prices *[]sgxRow `json:"prices"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return Batch{}, fmt.Errorf("sgx: not JSON: %w", err)
	}
	switch {
	case doc.Meta == nil || doc.Meta.Code != "200":
		return Batch{}, fmt.Errorf("sgx: no meta code 200")
	case doc.Data == nil || doc.Data.Prices == nil:
		return Batch{}, fmt.Errorf("sgx: no data.prices")
	}
	var built time.Time // SGX's clock when it built the answer
	if p := doc.Meta.ProcessedTime; p != nil && *p > 0 {
		built = time.UnixMilli(*p).UTC()
	}
	var b Batch
	reject := func(sym string, w Wanted, why string) {
		b.Rejected = append(b.Rejected, Reject{Ticker: w.Ticker, Symbol: sym, Reason: why})
	}
	reads := map[string]*sgxRead{}
	var order []string
	for _, r := range *doc.Data.Prices {
		w, ok := want[r.Code]
		if !ok {
			continue // the whole board comes back: the rest is not ours
		}
		at, err := time.ParseInLocation("20060102_150405", r.TradingTime, time.UTC)
		if err != nil {
			reject(r.Code, w, fmt.Sprintf("trading_time %q unreadable", r.TradingTime))
			continue
		}
		price, perr := sgxNumber(r.Last)
		volume, verr := sgxNumber(r.Volume)
		switch {
		case perr != nil:
			reject(r.Code, w, "price (lt): "+perr.Error())
			continue
		case price == 0:
			reject(r.Code, w, "no last trade (lt 0)")
			continue
		case verr != nil:
			reject(r.Code, w, "volume (vl): "+verr.Error())
			continue
		case !built.IsZero() && at.After(built.Add(15*time.Second)):
			reject(r.Code, w, "future timestamp: snapshot "+at.Format(time.RFC3339)+", answer built "+built.Format(time.RFC3339))
			continue
		}
		read := &sgxRead{w: w, price: price, volume: math.Round(volume * 1000), at: at}
		old := reads[r.Code]
		switch {
		case old == nil:
			order = append(order, r.Code)
			reads[r.Code] = read
		case at.After(old.at): // listed twice: the newer row stands
			reads[r.Code] = read
		case at.Equal(old.at) && (price != old.price || read.volume != old.volume):
			old.twice = true
		}
	}
	var minutes []time.Time // the middle one is the answer's: one odd row cannot move it
	for _, sym := range order {
		if r := reads[sym]; !r.twice {
			minutes = append(minutes, r.at)
		}
	}
	slices.SortFunc(minutes, time.Time.Compare)
	var middle time.Time
	if len(minutes) > 0 {
		middle = minutes[(len(minutes)-1)/2]
	}
	for _, sym := range order {
		r := reads[sym]
		switch {
		case r.twice:
			reject(sym, r.w, "listed twice at one snapshot with different prices or volumes")
			continue
		case middle.Sub(r.at) > sgxStaleRow:
			reject(sym, r.w, fmt.Sprintf("stale row: snapshot %s, the answer's %s",
				r.at.Format(time.TimeOnly), middle.Format(time.TimeOnly)))
			continue
		}
		vol := r.volume
		b.Rows = append(b.Rows, Row{Ticker: r.w.Ticker, Price: r.price, Volume: &vol})
		if b.Stamp.IsZero() || r.at.Before(b.Stamp) {
			b.Stamp = r.at // the oldest minute among the rows returned
		}
	}
	return b, nil
}

// sgxNumber is a board number, JSON's or one in a string: finite and at
// least 0 (a price of 0 is the board's "no trade", rejected by the caller).
func sgxNumber(raw json.RawMessage) (float64, error) {
	s := string(bytes.TrimSpace(raw))
	if len(s) >= 2 && s[0] == '"' {
		if err := json.Unmarshal(raw, &s); err != nil {
			return 0, err
		}
	}
	if s == "" || s == "null" {
		return 0, fmt.Errorf("missing")
	}
	n, err := strconv.ParseFloat(s, 64)
	switch {
	case err != nil:
		return 0, fmt.Errorf("%q is not a number", s)
	case math.IsNaN(n) || math.IsInf(n, 0) || n < 0:
		return 0, fmt.Errorf("%q is not a finite number of at least 0", s)
	}
	return n, nil
}

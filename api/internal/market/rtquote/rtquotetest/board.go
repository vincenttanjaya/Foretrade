// Package rtquotetest is a fake of the six real-time sources (rtquote.All)
// for tests: one HTTP side that answers each adapter's request in that
// source's own format, routed by path, so it serves either one adapter at
// its own host (Board as a client Transport) or every adapter at once behind
// QUOTES_REALTIME_BASE (Board as a server Handler). Its listings trade by the
// clock: while a venue trades (Open), each asked listing has traded a second
// before every answer and its cumulative volume has grown; while it does not,
// the answers hold the last trades, and the sources that state a session say
// it is over. SGX's board is a once-a-minute snapshot a minute behind, as
// api.sgx.com's is. It imports nothing internal.
package rtquotetest

import (
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

// Sources answered, by the adapter's name, and each one's venue.
var venues = map[string]string{"naver": "KRX", "tencent": "HKEX", "vietcap": "HOSE", "sgx": "SGX", "mis": "TWSE", "yahoo-tw": "TWSE"}

// Board is the fake sources' state.
type Board struct {
	Now  func() time.Time
	Open func(venue string, t time.Time) bool // whether venue trades at t; nil: always
	// SGX is the codes on SGX's board (its one request asks for the whole
	// board): D05, O39, ...
	SGX []string
	// Since is every listing's last trade before its first answer (its
	// volume then 1,000 shares); zero: an hour before that answer.
	Since time.Time

	mu       sync.Mutex
	rows     map[string]*row // by source and symbol
	refuse   map[string]bool // by source
	requests []Request
	sgxAt    time.Time // the newest minute SGX's board has taken in
	sgxOpen  time.Time // the newest of those SGX traded in: the board's minute
}

// Request is one request the board answered.
type Request struct {
	Source  string // the adapter's name, "" when no source's path
	At      time.Time
	Method  string
	Host    string // as asked: each adapter's own host, or the test base's
	Path    string // the path and query
	Header  http.Header
	Symbols int // listings asked for (SGX: the board's)
	Status  int
}

type row struct {
	price, volume float64
	at            time.Time // the last trade (SGX: the snapshot minute its volume last moved)
}

// price is a listing's price, 50-949, the same at each source that names it
// (MIS's tse_2454.tw and Yahoo Taiwan's 2454.TW).
func price(sym string) float64 {
	k := strings.ToUpper(sym)
	for _, p := range []string{"R_HK", "TSE_", "OTC_"} {
		k = strings.TrimPrefix(k, p)
	}
	k = strings.TrimSuffix(strings.TrimSuffix(k, ".TWO"), ".TW")
	return 50 + float64(crc32.ChecksumIEEE([]byte(k))%900)
}

// Refuse makes source refuse every request (on) or answer again: MIS with
// TWSE's F5 block page and HTTP 502, the others with HTTP 403.
func (b *Board) Refuse(source string, on bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.refuse == nil {
		b.refuse = map[string]bool{}
	}
	b.refuse[source] = on
}

// SetPrice sets a listing's price at source, by the source's symbol.
func (b *Board) SetPrice(source, sym string, price float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.row(source, sym, b.Now()).price = price
}

// Requests is every request answered so far.
func (b *Board) Requests() []Request {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]Request(nil), b.requests...)
}

// row is source's listing sym, created at now. Callers hold mu.
func (b *Board) row(source, sym string, now time.Time) *row {
	if b.rows == nil {
		b.rows = map[string]*row{}
	}
	k := source + " " + sym
	r := b.rows[k]
	if r == nil {
		at := b.Since
		if at.IsZero() {
			at = now.Add(-time.Hour)
		}
		r = &row{price: price(sym), volume: 1000, at: at}
		b.rows[k] = r
	}
	return r
}

func (b *Board) open(venue string, t time.Time) bool { return b.Open == nil || b.Open(venue, t) }

// trading is whether source's venue traded a second before now: then each
// listing an answer shows has just traded.
func (b *Board) trading(source string, now time.Time) bool {
	return b.open(venues[source], now.Add(-time.Second))
}

// trade is source's listing sym as an answer at now shows it: traded a
// second before now when trading. Callers hold mu.
func (b *Board) trade(source, sym string, now time.Time, trading bool) *row {
	r := b.row(source, sym, now)
	if at := now.Add(-time.Second); trading && at.After(r.at) {
		r.at, r.volume = at, r.volume+1000
	}
	return r
}

// RoundTrip answers a client's request (Board as an http.Client Transport).
func (b *Board) RoundTrip(req *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	b.ServeHTTP(rec, req)
	resp := rec.Result()
	resp.Request = req
	return resp, nil
}

// ServeHTTP answers one source's request, by its path.
func (b *Board) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.Now()
	path := req.URL.Path
	src := ""
	switch {
	case strings.HasPrefix(path, "/api/realtime/domestic/stock/"):
		src = "naver"
	case strings.HasPrefix(path, "/q="):
		src = "tencent"
	case path == "/api/price/symbols/getList":
		src = "vietcap"
	case path == "/securities/v1.1":
		src = "sgx"
	case path == "/stock/api/getStockInfo.jsp":
		src = "mis"
	case strings.HasPrefix(path, "/_td-stock/api/resource/StockServices.stockList;symbols="):
		src = "yahoo-tw"
	}
	rec := Request{Source: src, At: now, Method: req.Method, Host: req.Host, Path: req.URL.RequestURI(), Header: req.Header.Clone()}
	if rec.Host == "" {
		rec.Host = req.URL.Host
	}
	status, ctype, body := http.StatusNotFound, "text/plain", "not found"
	if src != "" && b.refuse[src] {
		status, body = http.StatusForbidden, "forbidden"
		if src == "mis" {
			status, ctype, body = http.StatusBadGateway, "text/html", misBlock
		}
	} else if src != "" {
		status, ctype = http.StatusOK, "application/json"
		var n int
		switch src {
		case "naver":
			body, n = b.naver(strings.Split(strings.TrimPrefix(path, "/api/realtime/domestic/stock/"), ","), now)
		case "tencent":
			ctype = "text/plain; charset=GBK"
			body, n = b.tencent(strings.Split(strings.TrimPrefix(path, "/q="), ","), now)
		case "vietcap":
			var in struct{ Symbols []string }
			raw, _ := io.ReadAll(req.Body)
			if err := json.Unmarshal(raw, &in); err != nil || req.Method != http.MethodPost {
				status, body = http.StatusBadRequest, "bad request"
				break
			}
			body, n = b.vietcap(in.Symbols, now)
		case "sgx":
			body, n = b.sgx(now)
		case "mis":
			body, n = b.mis(strings.Split(req.URL.Query().Get("ex_ch"), "|"), now)
		case "yahoo-tw":
			body, n = b.yahooTW(strings.Split(strings.TrimPrefix(path, "/_td-stock/api/resource/StockServices.stockList;symbols="), ","), now)
		}
		rec.Symbols = n
	}
	rec.Status = status
	b.requests = append(b.requests, rec)
	w.Header().Set("Content-Type", ctype)
	w.WriteHeader(status)
	io.WriteString(w, body)
}

// misBlock is TWSE's F5 firewall page (its words, a made-up support ID).
const misBlock = `<html><head><title>Request Rejected</title></head><body>FOR SECURITY REASONS, THIS PAGE CAN NOT BE ACCESSED!<br>因為安全性考量，您所需要的網頁無法被存取。<br>錯誤代碼：1234567890123456789</body></html>`

var (
	seoul    = time.FixedZone("KST", 9*3600)
	hongKong = time.FixedZone("HKT", 8*3600)
	taipei   = time.FixedZone("CST", 8*3600)
)

// The answers are written out by hand (their symbols are plain ASCII, so
// %q is JSON's quoting): a day of polls stays quick.

// naver is Naver's polling v2: KRX's price and volume, its server clock.
func (b *Board) naver(codes []string, now time.Time) (string, int) {
	trading := b.trading("naver", now)
	status, session := "OPEN", "regularMarket"
	if !b.open("KRX", now) {
		status, session = "CLOSE", "afterMarket"
	}
	clock := now.In(seoul)
	var out strings.Builder
	out.WriteString(`{"pollingInterval":7000,"datas":[`)
	for i, c := range codes {
		r := b.trade("naver", c, now, trading)
		if i > 0 {
			out.WriteByte(',')
		}
		fmt.Fprintf(&out, `{"itemCode":%q,"closePriceRaw":"%.0f","accumulatedTradingVolumeRaw":"%.0f","marketStatus":%q,`+
			`"marketSessionType":%q,"localTradedAt":%q,"stockExchangeType":{"code":"KS","delayTime":0},"tradeStopType":{"name":"TRADING"}}`,
			c, r.price, r.volume, status, session, clock.Format(time.RFC3339Nano))
	}
	fmt.Fprintf(&out, `],"time":%q}`, clock.Format("20060102150405"))
	return out.String(), len(codes)
}

// tencent is qt.gtimg.cn's r_ lines: [0] market, [2] code, [3] price, [6]
// volume, [30] the last trade (HKT), [75] currency (blank: the listing's).
func (b *Board) tencent(syms []string, now time.Time) (string, int) {
	trading := b.trading("tencent", now)
	var out strings.Builder
	for _, s := range syms {
		r := b.trade("tencent", s, now, trading)
		f := make([]string, 78)
		f[0], f[1], f[2] = "100", "NAME", strings.TrimPrefix(s, "r_hk")
		f[3], f[6], f[30] = fmt.Sprintf("%.3f", r.price), fmt.Sprintf("%.1f", r.volume), r.at.In(hongKong).Format("2006/01/02 15:04:05")
		fmt.Fprintf(&out, "v_%s=\"%s\";\n", s, strings.Join(f, "~"))
	}
	return out.String(), len(syms)
}

// vietcap is Vietcap's getList: the listing, the last match and its
// session (ENDED once the venue stops).
func (b *Board) vietcap(syms []string, now time.Time) (string, int) {
	trading := b.trading("vietcap", now)
	session := "LO_MORNING"
	if !b.open("HOSE", now) {
		session = "ENDED"
	}
	var out strings.Builder
	out.WriteByte('[')
	for i, s := range syms {
		r := b.trade("vietcap", s, now, trading)
		if i > 0 {
			out.WriteByte(',')
		}
		fmt.Fprintf(&out, `{"listingInfo":{"symbol":%q,"board":"HSX","tradingStatus":"ACTIVATED"},"matchPrice":{"symbol":%q,`+
			`"matchPrice":%g,"accumulatedVolume":%.0f,"time":%q,"session":%q,"ceilingPrice":%g,"floorPrice":%g}}`,
			s, s, r.price, r.volume, r.at.UTC().Format(time.RFC3339Nano), session, r.price*1.07, r.price*0.93)
	}
	out.WriteByte(']')
	return out.String(), len(syms)
}

// sgx is api.sgx.com's whole board: a snapshot of the minute before last,
// each of its minutes in which SGX traded adding a thousand shares to every
// code, its volumes in thousands of shares; it stops with the market.
func (b *Board) sgx(now time.Time) (string, int) {
	if len(b.SGX) == 0 {
		return fmt.Sprintf(`{"meta":{"code":"200","processedTime":%d},"data":{"prices":[]}}`, now.UnixMilli()), 0
	}
	for _, code := range b.SGX {
		b.row("sgx", code, now)
	}
	snap := now.Add(-time.Minute).Truncate(time.Minute)
	if b.sgxAt.IsZero() {
		b.sgxAt = b.row("sgx", b.SGX[0], now).at.Truncate(time.Minute)
		b.sgxOpen = b.sgxAt
	}
	for m := b.sgxAt.Add(time.Minute); !m.After(snap); m = m.Add(time.Minute) {
		if b.open("SGX", m) {
			for _, code := range b.SGX {
				r := b.row("sgx", code, now)
				r.at, r.volume = m, r.volume+1000
			}
			b.sgxOpen = m
		}
	}
	if snap.After(b.sgxAt) {
		b.sgxAt = snap
	}
	var out strings.Builder
	fmt.Fprintf(&out, `{"meta":{"code":"200","processedTime":%d},"data":{"prices":[`, now.UnixMilli())
	minute := b.sgxOpen.UTC().Format("20060102_150405")
	for i, code := range b.SGX {
		r := b.row("sgx", code, now)
		if i > 0 {
			out.WriteByte(',')
		}
		fmt.Fprintf(&out, `{"nc":%q,"lt":%g,"vl":%g,"trading_time":%q}`, code, r.price, r.volume/1000, minute)
	}
	out.WriteString("]}}")
	return out.String(), len(b.SGX)
}

// mis is TWSE MIS's getStockInfo.jsp: each row's last trade (date, time and
// price), its volume in lots, its limits, and the server clock.
func (b *Board) mis(syms []string, now time.Time) (string, int) {
	trading := b.trading("mis", now)
	var out strings.Builder
	out.WriteString(`{"msgArray":[`)
	for i, s := range syms {
		ex, ch, _ := strings.Cut(s, "_")
		r := b.trade("mis", s, now, trading)
		at := r.at.In(taipei)
		if i > 0 {
			out.WriteByte(',')
		}
		fmt.Fprintf(&out, `{"ex":%q,"ch":%q,"d":%q,"t":%q,"z":"%.4f","v":"%.0f","ts":"0","u":"%.4f","w":"%.4f","trade":{"t":%q,"z":"%.4f"}}`,
			ex, ch, at.Format("20060102"), at.Format("15:04:05"), r.price, r.volume/1000, r.price*1.1, r.price*0.9, at.Format("15:04:05"), r.price)
	}
	clock := now.In(taipei)
	fmt.Fprintf(&out, `],"rtcode":"0000","rtmessage":"OK","queryTime":{"sysDate":%q,"sysTime":%q}}`, clock.Format("20060102"), clock.Format("15:04:05"))
	return out.String(), len(syms)
}

// yahooTW is Yahoo Taiwan's stockList: real time (delayed by 0), the last
// trade in UTC, shares, the limits and the market's state.
func (b *Board) yahooTW(syms []string, now time.Time) (string, int) {
	trading := b.trading("yahoo-tw", now)
	status := "open"
	if !b.open("TWSE", now) {
		status = "close"
	}
	var out strings.Builder
	out.WriteByte('[')
	for i, s := range syms {
		r := b.trade("yahoo-tw", s, now, trading)
		if i > 0 {
			out.WriteByte(',')
		}
		fmt.Fprintf(&out, `{"symbol":%q,"exchangeDataDelayedBy":0,"price":{"raw":"%.2f"},"regularMarketTime":%q,"volume":"%.0f",`+
			`"limitUpPrice":{"raw":"%.2f"},"limitDownPrice":{"raw":"%.2f"},"marketStatus":%q}`,
			s, r.price, r.at.UTC().Format(time.RFC3339), r.volume, r.price*1.1, r.price*0.9, status)
	}
	out.WriteByte(']')
	return out.String(), len(syms)
}

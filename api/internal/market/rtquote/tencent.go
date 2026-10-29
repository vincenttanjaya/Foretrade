package rtquote

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Tencent is HKEX's real-time source: Tencent's quote server qt.gtimg.cn
// with the r_ prefix, which is real time; the plain prefix (hk00700) is the
// 15-minute delayed feed. One GET carries every code:
// /q=r_hk00001,r_hk00700,... (89 codes, about 37 KB, measured 2026-10-14;
// 178 codes in one GET also worked). It needs no key, cookie, Referer or
// browser User-Agent: two reads with UserAgent alone and Accept */* on
// 2026-10-14 at 12:02:50 and 12:08:15 UTC (HKEX closed) were both HTTP 200
// in 1.6 s, every one of the 89 codes asked for answered on its r_ line with
// the closing auction's price (testdata/tencent_after_close.txt). The
// research reads had sent a browser-like User-Agent.
//
// The answer is GBK text, one statement per listing:
// v_r_hk00700="100~腾讯控股~00700~425.400~425.200~...~2026/10/14 13:01:10~...~HKD~1~30";
// with '~'-separated fields. Fields read: [0] the market (100 is Hong
// Kong), [2] the code (it must be the line's own), [3] the last trade's
// price, [6] the cumulative session volume in shares, [30] the last trade's
// time (YYYY/MM/DD HH:MM:SS, Hong Kong time) and [75] the currency. The
// Chinese name [1] is never decoded, but its double-byte GBK characters are
// stepped over whole when the fields are split, since a GBK trail byte can be
// 0x7E, the '~' separator itself. There is no server clock in the body
// (Stamp stays zero) and no documented market-state field, so Closed is
// never set (HKEX has a verified calendar): [77] read 30 in the afternoon
// session, 40 in the lunch break and 50 after the close on the r_ feed, but
// 30 or 40 in the same minutes on the delayed one.
//
// Measured on 2026-10-14 (scratchpad/rt/results/tw-hk.md, reads
// hk_tencent_*): the six most liquid names' medians were 1-2.5 s behind our
// read (at most 24 s), Tencent (0700) 1-8 s, and all 89 codes' medians 7-13 s
// (thin names really had not traded for minutes: [30] is the last trade, not
// the read). The prices matched Yahoo's later 1-minute closes within 1-3
// ticks. During the lunch break [30] read 11:59:59.
//
// The delayed feed: in the same minutes the plain prefix answered 906-907 s
// behind. A line without the r_ prefix is rejected ("delayed feed"), so an
// answer of only such lines lands no row and counts as a failed read. A
// silent switch that keeps the r_ name shows in the trade times instead: the
// canaries' newest trade about 900 s old is over MaxLag, so the market layer
// marks the source degraded and the venue serves Yahoo
// (internal/market/realtime_tencent_test.go).
//
// Canaries: the three largest of our HKEX tickers by turnover ([37]) in the
// 89-code read at 13:32:02 HKT on 2026-10-14 (hk_tencent_r5_rt_all): Alibaba
// (9988) HKD 6.21 billion, Tencent (0700) 4.57 billion, Xiaomi (1810) 4.29
// billion; the same three led all five full reads from 13:01 to 13:32.
//
// Schedule: every 10 s, never closer than 5 s (the owner's limit for this
// source; community guidance asks only for 100 ms between calls). The
// calendar's continuous sessions (09:30-12:00, 13:00-16:00) have no quiet
// window; the settled read comes 12 minutes after the close, past the closing
// auction (16:00-16:10, its random close 16:08-16:10; half-days 12:00-12:10).
// A full day is 1,981 requests (900 in the morning, 1,080 in the afternoon,
// the settled read), at most 6 in any minute (TestTencentDay).
//
// Terms: Tencent publishes no terms for this server and its robots.txt
// answers "bad request"; HKEX licenses its real-time data to Tencent for
// Tencent's own products, not to us. The owner accepted that risk on
// 2026-10-14.
type Tencent struct{}

var (
	tencentTicker = regexp.MustCompile(`^([0-9]{4}|[1-9][0-9]{4})\.HK$`)
	tencentName   = regexp.MustCompile(`^(r_)?hk([0-9]{5})$`)
	tencentStmt   = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
	tencentDec    = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)
)

func (Tencent) Info() Info {
	return Info{Name: "tencent", Venue: "HKEX", Switch: "hkex", Host: "https://qt.gtimg.cn",
		Class: ClassRealtime, Times: TimesTrade, Every: 10 * time.Second, Floor: 5 * time.Second,
		Settle: 12 * time.Minute, MaxLag: 90 * time.Second,
		Canaries: []string{"9988.HK", "0700.HK", "1810.HK"}}
}

// Symbol is "r_hk" and the code zero-padded to five digits: 0700.HK is
// r_hk00700. Yahoo writes codes under 10000 with four digits, so a
// five-digit ticker starting with 0 is not ours.
func (Tencent) Symbol(ticker string) (string, bool) {
	m := tencentTicker.FindStringSubmatch(ticker)
	if m == nil {
		return "", false
	}
	return "r_hk" + strings.Repeat("0", 5-len(m[1])) + m[1], true
}

func (Tencent) Request(ctx context.Context, host string, want Want) (*http.Request, error) {
	if len(want) == 0 {
		return nil, fmt.Errorf("tencent: nothing to ask for")
	}
	for _, s := range want.Symbols() {
		if m := tencentName.FindStringSubmatch(s); m == nil || m[1] == "" {
			return nil, fmt.Errorf("tencent: %q is not an r_hk symbol", s)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, host+"/q="+strings.Join(want.Symbols(), ","), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "*/*")
	return req, nil
}

// tencentStatement is one v_<name>="<value>"; of an answer.
type tencentStatement struct {
	name  string
	value []byte
}

func (Tencent) Parse(body []byte, want Want) (Batch, error) {
	if head := bytes.ToLower(body[:min(len(body), 512)]); bytes.Contains(head, []byte("<html")) ||
		bytes.Contains(head, []byte("<!doctype")) {
		return Batch{}, Refused("tencent: an HTML page instead of quotes")
	}
	stmts, err := tencentStatements(body)
	if err != nil {
		return Batch{}, err
	}
	var b Batch
	for _, st := range stmts {
		m := tencentName.FindStringSubmatch(st.name)
		sym := st.name
		if m != nil {
			sym = "r_hk" + m[2]
		}
		w, asked := want[sym]
		reject := func(why string) {
			b.Rejected = append(b.Rejected, Reject{Ticker: w.Ticker, Symbol: st.name, Reason: why})
		}
		switch {
		case m == nil:
			reject("not a quote line")
			continue
		case m[1] == "":
			reject("delayed feed: the plain hk prefix is 15 minutes behind, only r_ is real time")
			continue
		case !asked:
			reject("not asked for")
			continue
		}
		row, why := tencentRow(tencentFields(st.value, '~'), m[2])
		if why != "" {
			reject(why)
			continue
		}
		row.Ticker = w.Ticker
		row.Currency = cmp.Or(row.Currency, w.Currency)
		b.Rows = append(b.Rows, row)
	}
	return b, nil
}

// tencentRow reads one statement's fields; why says what is wrong with it.
func tencentRow(f [][]byte, code string) (Row, string) {
	field := func(i int) string {
		if i < len(f) {
			return string(f[i])
		}
		return ""
	}
	if len(f) <= 30 {
		return Row{}, fmt.Sprintf("short line: %d fields", len(f))
	}
	if field(0) != "100" {
		return Row{}, fmt.Sprintf("not a Hong Kong quote: market %q", field(0))
	}
	if field(2) != code {
		return Row{}, fmt.Sprintf("mismatched symbol: the line for %s carries code %q", code, field(2))
	}
	price, err := tencentNumber(field(3))
	if err != nil {
		return Row{}, "price: " + err.Error()
	}
	if price <= 0 {
		return Row{}, fmt.Sprintf("price: %q is not positive", field(3))
	}
	volume, err := tencentNumber(field(6))
	if err != nil {
		return Row{}, "volume: " + err.Error()
	}
	at, err := time.ParseInLocation("2006/01/02 15:04:05", field(30), tencentZone)
	if err != nil {
		return Row{}, fmt.Sprintf("trade time: %q is not YYYY/MM/DD HH:MM:SS", field(30))
	}
	ccy := strings.TrimSpace(field(75))
	if strings.Trim(ccy, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" {
		return Row{}, fmt.Sprintf("currency %q", ccy)
	}
	return Row{Price: price, Currency: ccy, TradeAt: at.UTC(), Volume: &volume}, ""
}

// tencentStatements splits an answer into its statements. A statement's
// value ends at the first `";` followed by the next statement or the end:
// neither byte can be part of a GBK character.
func tencentStatements(body []byte) ([]tencentStatement, error) {
	var out []tencentStatement
	rest := body
	for {
		rest = bytes.TrimLeft(rest, " \t\r\n")
		if len(rest) == 0 {
			break
		}
		at := len(body) - len(rest)
		eq := bytes.Index(rest, []byte(`="`))
		if !bytes.HasPrefix(rest, []byte("v_")) || eq < 0 || !tencentStmt.Match(rest[2:eq]) {
			return nil, fmt.Errorf("tencent: no v_<name>=\" statement at byte %d", at)
		}
		name, value := string(rest[2:eq]), rest[eq+2:]
		end := -1
		for from := 0; ; {
			i := bytes.Index(value[from:], []byte(`";`))
			if i < 0 {
				break
			}
			next := bytes.TrimLeft(value[from+i+2:], " \t\r\n")
			if len(next) == 0 || bytes.HasPrefix(next, []byte("v_")) {
				end = from + i
				break
			}
			from += i + 2
		}
		if end < 0 {
			return nil, fmt.Errorf("tencent: statement v_%s at byte %d is not closed", name, at)
		}
		out = append(out, tencentStatement{name: name, value: value[:end]})
		rest = value[end+2:]
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("tencent: no quote line")
	}
	return out, nil
}

// tencentFields splits GBK text at sep, an ASCII byte, stepping over each
// double-byte character (lead 0x81-0xFE) and GB18030 four-byte sequence
// whole: a trail byte can equal sep.
func tencentFields(s []byte, sep byte) [][]byte {
	var out [][]byte
	start, i := 0, 0
	for i < len(s) {
		switch c := s[i]; {
		case c == sep:
			out = append(out, s[start:i])
			i++
			start = i
		case c >= 0x81 && c <= 0xFE && i+1 < len(s) && s[i+1] >= 0x30 && s[i+1] <= 0x39:
			i += 4
		case c >= 0x81 && c <= 0xFE:
			i += 2
		default:
			i++
		}
	}
	return append(out, s[min(start, len(s)):])
}

// tencentNumber is a plain decimal ("425.400", "8532198.0"), never negative.
func tencentNumber(s string) (float64, error) {
	if !tencentDec.MatchString(s) {
		return 0, fmt.Errorf("%q is not a decimal", s)
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("%q: %w", s, err)
	}
	return n, nil
}

var tencentZone = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Hong_Kong")
	if err != nil {
		return time.FixedZone("HKT", 8*3600) // Hong Kong keeps no daylight saving
	}
	return loc
}()

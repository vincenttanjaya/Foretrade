package rtquote

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// MIS is TWSE's first real-time source: the exchange's own market
// information site (基本市況報導, mis.twse.com.tw), getStockInfo.jsp, the
// JSON its quote pages poll. One GET carries every listing:
// /stock/api/getStockInfo.jsp?ex_ch=tse_2330.tw%7Ctse_2317.tw%7C...&json=1&delay=0
// (52 symbols, about 37 KB in session and 42 KB after the close). The "|"
// between symbols goes as %7C: the research's one read with a raw "|" was
// refused. No key, cookie or Referer: it sets a JSESSIONID but answers
// without one.
//
// Fields read, per msgArray row: ex and ch (tse + 2330.tw: the symbol we
// asked for; otc for a TPEx listing), d (the row's trading date, Taipei),
// trade.z and trade.t (the last trade's price and time; the top-level z and
// t, the newest update's, only when a row has no trade object: z is "-"
// when that update was a quote change), v (the regular session's cumulative
// volume in lots of 1,000 shares; Row.Volume is in shares), u and w (the
// day's limit-up and limit-down prices: a price outside them is rejected)
// and ts ("0" on every row we have seen; read as the trial-match flag, so on
// "1" the top-level z is not taken for a trade). Never read: pz and ps (a
// call auction's price and volume: after the close pz differed from the
// closing trade on Lite-On, 283.5 against 283.0), fv and the o* fields
// (after-hours trading: fv is exactly the lots Yahoo Taiwan's volume adds
// after the close). Per answer:
// rtcode ("0000" when it served) and queryTime.sysDate/sysTime, the server
// clock and the answer's Stamp (-3.1 to +0.03 s from the end of our read
// over 5 research reads). A trade later than twLastPrint (13:35 Taipei) is
// rejected. An answer whose rows all carry an earlier trading date than the
// server's (a day without trading, such as a typhoon closure) is Closed;
// after the close on a trading day the rows keep that day's date, so the
// settled read is not Closed.
//
// It is real time by design (delay=0; its page polls every userDelay 5000
// ms) but runs behind the trades: TSMC's own trade time was 13-93 s old at
// the research's read over 5 reads, the batch medians 23-81 s, and MediaTek
// once 210 s (research mis_lag_stats.txt; Yahoo Taiwan was 1-13 s in the
// same minutes). The framework dates each quote by its trade time, so its
// label says real-time only for a quote under 60 s behind and "about N min
// behind" otherwise. Settle is 5 minutes: with trade times up to 93 s
// behind, a read 2 minutes after 13:30 could still miss the closing print.
// After the close of 2026-10-14 every row carried its 13:30:00 closing
// trade, and MIS's closes matched Yahoo Taiwan's on all 52 symbols.
//
// Limits and refusals: TWSE publishes no rate limit; the community twstock
// README states 3 requests per 5 s, with bans beyond (pypi.org/project/
// twstock). We stay far under it: one request per 10 s while open, never two
// within 5 s (Floor; fill re-reads included), and one request carries every
// symbol. TWSE's F5 firewall refuses with a block page "FOR SECURITY
// REASONS, THIS PAGE CAN NOT BE ACCESSED" and HTTP 502 (Blocked); the
// research also got that page with HTTP 200 from another TWSE host (Parse).
// Both are refusals: the venue moves to Yahoo Taiwan, the next source, for
// 30 minutes or more. It refused 7 of 12 research reads (a browser-like
// User-Agent, reads at least 15 s apart, no pattern by URL or pace) and 3
// of our 4 with UserAgent alone on 2026-10-14 (12:36:09, 12:36:59 and
// 12:37:21 UTC refused, 12:37:42 served all 52 symbols; reads at least 20 s
// apart, the same request each time): it refuses often but not always, at
// a rate like the research's browser-like User-Agent met.
//
// Canaries: the three largest of our TWSE tickers by the day's traded value
// on 2026-10-14, from the reads after the close (12:37:42 UTC: MIS's v ×
// close; 12:38:58 and 12:40:01 UTC: Yahoo Taiwan's turnoverM over all 55 of
// our tickers): Nan Ya Plastics 59.5 billion TWD, TSMC 58.1 billion,
// MediaTek 29.8 billion (Yageo 28.7 billion next). At midday Yageo was third
// (13:19 Taipei, tw_mis_r9_all52 and tw_ytw_api_r9_all). The newest of the
// three canaries' trades was 14-63 s old in the 5 research reads of MIS,
// under MaxLag: past it, MIS is behind its own pace and Yahoo Taiwan
// serves.
//
// Terms: TWSE's terms of use ban automated download without its consent
// (clause 6) and redistribution without written consent (clause 8,
// twse.com.tw/zh/page/terms/use.html); TWSE sells real-time redistribution
// through its information-vendor licence. The owner accepted that risk on
// 2026-10-14.
type MIS struct{}

// twTicker is our Yahoo ticker of a TWSE (.TW) or TPEx (.TWO) listing: 2330,
// 00878, 00632R.
var twTicker = regexp.MustCompile(`^([0-9]{4,6}[A-Z]?)\.(TWO?)$`)

// twLastPrint is the latest Taipei time of day a regular-session trade can
// carry: TWSE closes at 13:30 (the market calendar), and a trade stamped
// later belongs to its after-hours sessions (Yahoo Taiwan dated 46 of our
// 52 rows 14:30:00 after the close of 2026-10-14, each with MIS's fv lots of
// after-hours fixed-price volume on top of the session's).
const twLastPrint = 13*time.Hour + 35*time.Minute

func (MIS) Info() Info {
	return Info{Name: "mis", Venue: "TWSE", Switch: "twse", Host: "https://mis.twse.com.tw",
		Class: ClassRealtime, Times: TimesTrade, Every: 10 * time.Second, Floor: 5 * time.Second,
		Settle: 5 * time.Minute, MaxLag: 90 * time.Second,
		Canaries: []string{"1303.TW", "2330.TW", "2454.TW"},
		Quiet:    []Window{{"13:25", "13:30"}}} // the closing call: one print at 13:30
}

func (MIS) Symbol(ticker string) (string, bool) {
	m := twTicker.FindStringSubmatch(ticker)
	if m == nil {
		return "", false
	}
	if m[2] == "TWO" {
		return "otc_" + m[1] + ".tw", true
	}
	return "tse_" + m[1] + ".tw", true
}

func (MIS) Request(ctx context.Context, host string, want Want) (*http.Request, error) {
	if len(want) == 0 {
		return nil, fmt.Errorf("mis: nothing to ask for")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		host+"/stock/api/getStockInfo.jsp?ex_ch="+strings.Join(want.Symbols(), "%7C")+"&json=1&delay=0", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	return req, nil
}

// misRow is the part of a msgArray row MIS reads.
type misRow struct {
	Ex    string `json:"ex"`
	Ch    string `json:"ch"`
	Date  string `json:"d"`
	Time  string `json:"t"`
	Last  string `json:"z"`
	Vol   string `json:"v"`
	Up    string `json:"u"`
	Down  string `json:"w"`
	Trial string `json:"ts"`
	Trade *struct {
		T string `json:"t"`
		Z string `json:"z"`
	} `json:"trade"`
}

// misBlock reads TWSE's F5 firewall page: whether body is it, and the reason
// with its support ID ("錯誤代碼：7692643789991231192") when it gives one.
func misBlock(body []byte) (string, bool) {
	if !bytes.Contains(body, []byte("FOR SECURITY REASONS")) && !bytes.Contains(body, []byte("因為安全性考量")) {
		return "", false
	}
	why := "TWSE's firewall block page (FOR SECURITY REASONS, THIS PAGE CAN NOT BE ACCESSED)"
	if m := misSupportID.FindSubmatch(body); m != nil {
		why += ", support ID " + string(m[1])
	}
	return why, true
}

var misSupportID = regexp.MustCompile(`錯誤代碼[：:]\s*([0-9]+)`)

// Blocked reads the block page TWSE's firewall serves with HTTP 502.
func (MIS) Blocked(status int, body []byte) (string, bool) {
	why, ok := misBlock(body)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("HTTP %d: %s", status, why), true
}

func (MIS) Parse(body []byte, want Want) (Batch, error) {
	if why, ok := misBlock(body); ok {
		return Batch{}, Refused(why)
	}
	var doc struct {
		Rows      *[]json.RawMessage `json:"msgArray"`
		Code      string             `json:"rtcode"`
		Message   string             `json:"rtmessage"`
		QueryTime *struct {
			SysDate string `json:"sysDate"`
			SysTime string `json:"sysTime"`
		} `json:"queryTime"`
	}
	if err := json.Unmarshal(body, &doc); err != nil { // the blank lines before its JSON are whitespace to the decoder
		return Batch{}, fmt.Errorf("mis: not JSON: %w", err)
	}
	if doc.Code != "0000" {
		return Batch{}, fmt.Errorf("mis: rtcode %q (%s)", doc.Code, doc.Message)
	}
	if doc.Rows == nil {
		return Batch{}, fmt.Errorf("mis: no msgArray")
	}
	var b Batch
	today := ""
	if q := doc.QueryTime; q != nil {
		if t, err := time.ParseInLocation("2006010215:04:05", q.SysDate+q.SysTime, taipei); err == nil {
			b.Stamp, today = t.UTC(), q.SysDate
		}
	}
	current := 0 // rows on the server's trading date
	for _, raw := range *doc.Rows {
		var r misRow
		bad := json.Unmarshal(raw, &r) // a field of the wrong type: the others still decode
		sym := r.Ex + "_" + r.Ch
		w, ok := want[sym]
		reject := func(why string) {
			b.Rejected = append(b.Rejected, Reject{Ticker: w.Ticker, Symbol: sym, Reason: why})
		}
		switch {
		case !ok:
			reject("not asked for")
			continue
		case bad != nil:
			reject("bad parse: " + bad.Error())
			continue
		}
		price, at := "", ""
		switch {
		case r.Trade != nil && r.Trade.Z != "" && r.Trade.Z != "-":
			price, at = r.Trade.Z, r.Trade.T
		case r.Trial != "1" && r.Last != "" && r.Last != "-":
			price, at = r.Last, r.Time
		}
		if price == "" {
			reject("no trade yet")
			continue
		}
		p, err := twDecimal(price)
		if err != nil {
			reject("price: " + err.Error())
			continue
		}
		tradeAt, err := time.ParseInLocation("2006010215:04:05", r.Date+at, taipei)
		if err != nil {
			reject(fmt.Sprintf("trade time %q %q: not a date and time", r.Date, at))
			continue
		}
		if why, ok := twCheck(p, tradeAt, r.Up, r.Down); !ok {
			reject(why)
			continue
		}
		row := Row{Ticker: w.Ticker, Price: p, Currency: w.Currency, TradeAt: tradeAt.UTC()}
		if r.Vol != "" && r.Vol != "-" {
			lots, err := strconv.ParseUint(r.Vol, 10, 53)
			if err != nil {
				reject(fmt.Sprintf("volume %q is not a whole number of lots", r.Vol))
				continue
			}
			shares := float64(lots) * 1000
			row.Volume = &shares
		}
		b.Rows = append(b.Rows, row)
		if r.Date == today {
			current++
		}
	}
	b.Closed = len(b.Rows) > 0 && today != "" && current == 0
	return b, nil
}

// twCheck holds a TWSE trade to the regular session and the day's limits
// (up and down as the source gives them; skipped when either is missing).
func twCheck(price float64, at time.Time, up, down string) (string, bool) {
	l := at.In(taipei)
	if d := time.Duration(l.Hour())*time.Hour + time.Duration(l.Minute())*time.Minute + time.Duration(l.Second())*time.Second; d > twLastPrint {
		return "after-hours trade at " + l.Format("15:04:05") + " Taipei, past the regular session", false
	}
	u, errU := twDecimal(up)
	w, errW := twDecimal(down)
	if errU == nil && errW == nil && (price > u || price < w) {
		return fmt.Sprintf("price %g outside the day's limits %g-%g", price, w, u), false
	}
	return "", true
}

var taipei = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Taipei")
	if err != nil {
		return time.FixedZone("CST", 8*3600) // Taiwan keeps no daylight saving
	}
	return loc
}()

// twDecimal is one of the TWSE sources' decimals ("2515.0000", "44.15"):
// plain digits with at most one point, positive.
func twDecimal(s string) (float64, error) {
	if s == "" || s == "-" {
		return 0, fmt.Errorf("missing")
	}
	if strings.Trim(s, "0123456789.") != "" || strings.Count(s, ".") > 1 {
		return 0, fmt.Errorf("%q is not a number", s)
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || !(n > 0) {
		return 0, fmt.Errorf("%q is not a positive number", s)
	}
	return n, nil
}

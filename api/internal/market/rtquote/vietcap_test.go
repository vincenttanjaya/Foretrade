package rtquote

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ourHOSE is every HOSE ticker we have: listed.json's 23, the named quote
// feeds VCB, VHM and FPT, and the screener's MCH, SAB, TCX and VPL (the
// market package's conformance test reads the same from the repo's files).
var ourHOSE = strings.Fields(`ACB.VN BID.VN BSR.VN CTG.VN FPT.VN GAS.VN GVR.VN HDB.VN HPG.VN LPB.VN MBB.VN MCH.VN
	MSN.VN MWG.VN SAB.VN SHB.VN SSB.VN SSI.VN STB.VN TCB.VN TCX.VN VCB.VN VHM.VN VIB.VN VIC.VN VJC.VN VNM.VN VPB.VN
	VPL.VN VRE.VN`)

// vietcapWant asks for every symbol in a fixture (the board's placeholder
// rows aside), as the market layer would.
func vietcapWant(t *testing.T, body []byte) Want {
	t.Helper()
	var rows []struct {
		MatchPrice struct {
			Symbol string `json:"symbol"`
		} `json:"matchPrice"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatal(err)
	}
	want := Want{}
	for _, r := range rows {
		if r.MatchPrice.Symbol == "" {
			continue
		}
		ticker := r.MatchPrice.Symbol + ".VN"
		sym, ok := Vietcap{}.Symbol(ticker)
		if !ok || sym != r.MatchPrice.Symbol {
			t.Fatalf("%s maps to %q, %v", ticker, sym, ok)
		}
		want[sym] = Wanted{Ticker: ticker, Currency: "VND"}
	}
	return want
}

func vietcapParse(t *testing.T, name string) (Batch, Want) {
	t.Helper()
	body := fixture(t, name)
	want := vietcapWant(t, body)
	b, err := Vietcap{}.Parse(body, want)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return b, want
}

func utc(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return t
}

// TestVietcapParseSession: the full-board read at 06:02:13.9-06:02:16.7 UTC
// on 2026-10-14, the afternoon session (research cov_vci_all400, trimmed to
// our 29 then on the board; TCX was not asked): every row parses at the
// board's last match and cumulative volume, with no trade time of its own (the
// market layer's volume clock dates it), the listing's currency, and the
// board's newest message time as the answer's stamp.
func TestVietcapParseSession(t *testing.T) {
	b, want := vietcapParse(t, "vietcap_session.json")
	if len(want) != 29 || len(b.Rows) != 29 || len(b.Rejected) != 0 || b.Closed {
		t.Fatalf("want %d, rows %d, rejected %v, closed %v", len(want), len(b.Rows), b.Rejected, b.Closed)
	}
	rows := byTicker(b)
	for ticker, c := range map[string]struct{ price, volume float64 }{
		"TCB.VN": {32900, 8_653_900}, "VIC.VN": {220200, 697_300}, "FPT.VN": {56600, 4_043_500}, "SAB.VN": {43000, 58_600},
	} {
		r := rows[ticker]
		if r.Price != c.price || r.Volume == nil || *r.Volume != c.volume || !r.TradeAt.IsZero() || r.Currency != "" {
			t.Errorf("%s %+v, want %v at volume %v", ticker, r, c.price, c.volume)
		}
	}
	if !b.Stamp.Equal(utc("2026-10-14T06:02:16.351Z")) { // BSR's match, the newest message of the 29
		t.Fatalf("stamp %v", b.Stamp)
	}
}

// TestVietcapBurst: two of the research's rounds, 6.6 minutes apart (r1 at
// 06:01:31.6-06:01:33.9 UTC, r2 at 06:08:10.7-06:08:12.8): every one of the
// eight names' cumulative volume rose (what the volume clock dates a price
// by), and each stamp is the board's clock within a few seconds of the read.
func TestVietcapBurst(t *testing.T) {
	a, _ := vietcapParse(t, "vietcap_r1.json")
	b, _ := vietcapParse(t, "vietcap_r2.json")
	if len(a.Rows) != 8 || len(b.Rows) != 8 {
		t.Fatalf("rows %d, %d", len(a.Rows), len(b.Rows))
	}
	before := byTicker(a)
	for _, r := range b.Rows {
		if *r.Volume <= *before[r.Ticker].Volume {
			t.Errorf("%s volume %v then %v", r.Ticker, *before[r.Ticker].Volume, *r.Volume)
		}
	}
	if vic := byTicker(b)["VIC.VN"]; before["VIC.VN"].Price != 220000 || vic.Price != 219900 || *vic.Volume != 764_300 {
		t.Fatalf("VIC %+v", vic)
	}
	if !a.Stamp.Equal(utc("2026-10-14T06:01:34.315Z")) || !b.Stamp.Equal(utc("2026-10-14T06:08:13.039Z")) {
		t.Fatalf("stamps %v, %v", a.Stamp, b.Stamp)
	}
}

// TestVietcapStaleBoard: an answer that is not fresh says so through its
// stamp. The research's read at 05:41:26 UTC fell in the lunch break: the
// rows still read LO_MORNING (not Closed: the break is the calendar's
// business, and it is not polled) and the board's newest message is the
// morning's last, 71 minutes old. A board that falls 20 minutes behind
// (round r2 with every time moved back) stamps its answer 20 minutes back,
// so the market layer dates moved volumes then, labels the delay and, past
// MaxLag on the canaries, degrades the source.
func TestVietcapStaleBoard(t *testing.T) {
	lunch, _ := vietcapParse(t, "vietcap_lunch.json")
	if len(lunch.Rows) != 3 || lunch.Closed || !lunch.Stamp.Equal(utc("2026-10-14T04:29:59.557Z")) {
		t.Fatalf("lunch: rows %d closed %v stamp %v", len(lunch.Rows), lunch.Closed, lunch.Stamp)
	}
	if age := utc("2026-10-14T05:41:26.186Z").Sub(lunch.Stamp); age < 71*time.Minute || age > 72*time.Minute {
		t.Fatalf("lunch stamp %v old", age)
	}

	var rows []map[string]any
	if err := json.Unmarshal(fixture(t, "vietcap_r2.json"), &rows); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		mp := r["matchPrice"].(map[string]any)
		mp["time"] = utc(mp["time"].(string)).Add(-20 * time.Minute).Format(time.RFC3339Nano)
	}
	body, _ := json.Marshal(rows)
	fresh, want := vietcapParse(t, "vietcap_r2.json")
	late, err := Vietcap{}.Parse(body, want)
	if err != nil || len(late.Rows) != 8 {
		t.Fatalf("late: %v %v", late, err)
	}
	if d := fresh.Stamp.Sub(late.Stamp); d != 20*time.Minute {
		t.Fatalf("a board 20 minutes behind stamped %v behind", d)
	}
	for _, r := range late.Rows {
		if !r.TradeAt.IsZero() {
			t.Fatalf("%s carries a trade time of its own", r.Ticker)
		}
	}
}

// TestVietcapAfterClose: the research's read at 08:18 UTC: every row ENDED,
// so the answer is Closed, at the closing auction's price stamped 07:45:00.023
// (the market layer clamps a settled read's stamp to the 14:45 close).
func TestVietcapAfterClose(t *testing.T) {
	b, _ := vietcapParse(t, "vietcap_after_close.json")
	if len(b.Rows) != 8 || !b.Closed || !b.Stamp.Equal(utc("2026-10-14T07:45:00.023Z")) {
		t.Fatalf("rows %d closed %v stamp %v", len(b.Rows), b.Closed, b.Stamp)
	}
	if vic := byTicker(b)["VIC.VN"]; vic.Price != 218400 || *vic.Volume != 3_116_100 {
		t.Fatalf("VIC %+v", vic)
	}
}

// TestVietcapLiveAfterClose: our own read at 12:14:22 UTC on 2026-10-14 with
// the honest User-Agent, all 30 of our HOSE tickers (the screener's TCX
// included): every one answered, Closed; and the canaries are the day's top
// three of ours by traded value in that answer.
func TestVietcapLiveAfterClose(t *testing.T) {
	body := fixture(t, "vietcap_live_after_close.json")
	b, want := vietcapParse(t, "vietcap_live_after_close.json")
	if len(want) != len(ourHOSE) || len(b.Rows) != len(ourHOSE) || len(b.Rejected) != 0 || !b.Closed {
		t.Fatalf("want %d, rows %d, rejected %v, closed %v", len(want), len(b.Rows), b.Rejected, b.Closed)
	}
	rows := byTicker(b)
	for _, ticker := range ourHOSE {
		if _, ok := rows[ticker]; !ok {
			t.Errorf("no row for %s", ticker)
		}
	}
	if tcx := rows["TCX.VN"]; tcx.Price != 27250 || *tcx.Volume != 2_189_600 {
		t.Fatalf("TCX %+v", tcx)
	}
	if !b.Stamp.Equal(utc("2026-10-14T07:45:00.023Z")) {
		t.Fatalf("stamp %v", b.Stamp)
	}

	type traded struct {
		MatchPrice struct {
			Symbol string  `json:"symbol"`
			Value  float64 `json:"accumulatedValue"` // million VND
		} `json:"matchPrice"`
	}
	var value []traded
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatal(err)
	}
	slices.SortFunc(value, func(a, b traded) int { return cmp.Compare(b.MatchPrice.Value, a.MatchPrice.Value) })
	var top []string
	for _, v := range value[:3] {
		top = append(top, v.MatchPrice.Symbol+".VN")
	}
	if got := (Vietcap{}).Info().Canaries; !slices.Equal(got, top) {
		t.Fatalf("canaries %v, the day's top three by value %v", got, top)
	}
}

// TestVietcapUnknownSymbol: our read at 12:15:09 UTC asked our 26 trade-tier
// tickers and ZZZ, which HOSE does not list: the board answers a placeholder
// row (no listing, empty symbol) and the rest as usual. The placeholder is
// not a row; the symbol it stands for is listed as rejected.
func TestVietcapUnknownSymbol(t *testing.T) {
	body := fixture(t, "vietcap_live_unknown.json")
	want := vietcapWant(t, body)
	want["ZZZ"] = Wanted{Ticker: "ZZZ.VN", Currency: "VND"}
	b, err := Vietcap{}.Parse(body, want)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Rows) != 26 || !b.Closed {
		t.Fatalf("rows %d closed %v", len(b.Rows), b.Closed)
	}
	if len(b.Rejected) != 1 || b.Rejected[0].Ticker != "ZZZ.VN" || !strings.Contains(b.Rejected[0].Reason, "does not know") {
		t.Fatalf("rejected %+v", b.Rejected)
	}
}

// TestVietcapRowEdges: per-row reasons, from a real row (VIC in round r1)
// changed one field at a time; an epoch-millisecond time is read too.
func TestVietcapRowEdges(t *testing.T) {
	var rows []map[string]any
	if err := json.Unmarshal(fixture(t, "vietcap_r1.json"), &rows); err != nil {
		t.Fatal(err)
	}
	base := rows[slices.IndexFunc(rows, func(r map[string]any) bool {
		return r["matchPrice"].(map[string]any)["symbol"] == "VIC"
	})]
	clone := func(sym string, edit func(r, li, mp map[string]any)) map[string]any {
		raw, _ := json.Marshal(base)
		var r map[string]any
		json.Unmarshal(raw, &r)
		li, mp := r["listingInfo"].(map[string]any), r["matchPrice"].(map[string]any)
		li["symbol"], mp["symbol"] = sym, sym
		edit(r, li, mp)
		return r
	}
	same := func(r, li, mp map[string]any) {}
	body, _ := json.Marshal([]any{
		clone("VIC", same),
		clone("TCB", func(r, li, mp map[string]any) { mp["time"] = 1791957694237 }), // epoch ms: 06:01:34.237
		clone("HPG", func(r, li, mp map[string]any) { li["symbol"] = "HPX" }),
		clone("VPB", func(r, li, mp map[string]any) { li["board"] = "UPCOM" }),
		clone("SSI", func(r, li, mp map[string]any) { li["tradingStatus"] = "TRADING_SUSPENSION" }),
		clone("VCB", func(r, li, mp map[string]any) { mp["matchPrice"] = 0 }),
		clone("FPT", func(r, li, mp map[string]any) { mp["accumulatedVolume"] = 0 }),
		clone("VHM", func(r, li, mp map[string]any) { delete(mp, "accumulatedVolume") }),
		clone("MBB", func(r, li, mp map[string]any) { mp["matchPrice"] = 300000 }), // VIC's band is 206,500-237,500
		clone("GAS", func(r, li, mp map[string]any) { mp["time"] = "13:01:33" }),
		clone("BID", func(r, li, mp map[string]any) { delete(mp, "time") }),
		clone("MWG", func(r, li, mp map[string]any) { r["matchPrice"] = nil }), // named by its listing
		clone("ACB", func(r, li, mp map[string]any) { r["listingInfo"] = nil }),
		clone("XYZ", same),
		map[string]any{"listingInfo": nil, "matchPrice": map[string]any{"symbol": "", "matchPrice": 0}}, // the placeholder
	})

	want := Want{}
	for _, s := range []string{"VIC", "TCB", "HPG", "VPB", "SSI", "VCB", "FPT", "VHM", "MBB", "GAS", "BID", "MWG", "ACB", "LPB"} {
		want[s] = Wanted{Ticker: s + ".VN", Currency: "VND"}
	}
	b, err := Vietcap{}.Parse(body, want)
	if err != nil {
		t.Fatal(err)
	}
	rows2 := byTicker(b)
	if len(b.Rows) != 2 || rows2["VIC.VN"].Price != 220000 || *rows2["VIC.VN"].Volume != 696_700 || rows2["TCB.VN"].Price != 220000 {
		t.Fatalf("rows %+v", b.Rows)
	}
	if !b.Stamp.Equal(utc("2026-10-14T06:01:34.237Z")) || b.Closed { // TCB's epoch-ms time is the newest
		t.Fatalf("stamp %v closed %v", b.Stamp, b.Closed)
	}
	reasons := map[string]string{}
	for _, r := range b.Rejected {
		reasons[r.Symbol] = r.Reason
		if r.Symbol != "XYZ" && r.Ticker != r.Symbol+".VN" {
			t.Errorf("%s rejected as ticker %q", r.Symbol, r.Ticker)
		}
	}
	for sym, frag := range map[string]string{"HPG": "symbols disagree", "VPB": "not a HOSE listing", "SSI": "suspended",
		"VCB": "no trade today", "FPT": "no trade today", "VHM": "no trade today", "MBB": "outside the day's band",
		"GAS": "unreadable", "BID": "time missing", "MWG": "no match data", "ACB": "no listing data",
		"XYZ": "not asked for", "LPB": "does not know"} {
		if !strings.Contains(reasons[sym], frag) {
			t.Errorf("%s: %q, want %q", sym, reasons[sym], frag)
		}
	}
	if len(b.Rejected) != 13 {
		t.Errorf("%d rejected: %+v", len(b.Rejected), b.Rejected)
	}
}

// TestVietcapClosed: Closed is decided by the asked rows' sessions alone: all
// ENDED, even with nothing usable (a board reset on a holiday: price 0), is
// Closed; one asked row still in a session is not; a row not asked for does
// not count.
func TestVietcapClosed(t *testing.T) {
	row := func(sym, session string, price float64) map[string]any {
		return map[string]any{
			"listingInfo": map[string]any{"symbol": sym, "board": "HSX", "tradingStatus": "TRADING_ACTIVATED"},
			"matchPrice": map[string]any{"symbol": sym, "matchPrice": price, "accumulatedVolume": 1000, "time": "2026-10-14T07:45:00.023Z",
				"session": session, "ceilingPrice": 60000, "floorPrice": 50000}}
	}
	want := Want{"VCB": {"VCB.VN", "VND"}, "FPT": {"FPT.VN", "VND"}}
	for _, c := range []struct {
		name   string
		rows   []any
		closed bool
		n      int
	}{
		{"all ENDED", []any{row("VCB", "ENDED", 56300), row("FPT", "ENDED", 57100)}, true, 2},
		{"holiday reset", []any{row("VCB", "ENDED", 0), row("FPT", "ENDED", 0)}, true, 0},
		{"one in session", []any{row("VCB", "ENDED", 56300), row("FPT", "LO_AFTERNOON", 57100)}, false, 2},
		{"unasked in session", []any{row("VCB", "ENDED", 56300), row("FPT", "ENDED", 57100), row("HPG", "LO_AFTERNOON", 55000)}, true, 2},
		{"no session field", []any{row("VCB", "ENDED", 56300), row("FPT", "", 57100)}, false, 2},
	} {
		body, _ := json.Marshal(c.rows)
		b, err := Vietcap{}.Parse(body, want)
		if err != nil || b.Closed != c.closed || len(b.Rows) != c.n {
			t.Errorf("%s: closed %v rows %d err %v", c.name, b.Closed, len(b.Rows), err)
		}
	}
}

// TestVietcapRefusedAndMalformed: an answer that is not the board's JSON
// list is an error; a block or challenge page is a refusal (the market layer
// pauses the source and records it), any other HTML page a failure.
func TestVietcapRefusedAndMalformed(t *testing.T) {
	want := Want{"VCB": {"VCB.VN", "VND"}}
	for _, bad := range []string{"", "null", "[]", "{}", `{"status":500,"message":"Internal error"}`, "\x00\x01\x02",
		`[{"matchPrice":{"symbol":"VCB"`, `"VCB"`, "<html><body>Bảo trì hệ thống</body></html>"} {
		_, err := Vietcap{}.Parse([]byte(bad), want)
		if err == nil || errors.Is(err, ErrRefused) {
			t.Errorf("%q: %v, want a failure", bad, err)
		}
	}
	for _, page := range []string{"<html><body>Just a moment...</body></html>", "\n<!DOCTYPE html><title>Access Denied</title>",
		"<html>Request Rejected. The requested URL was rejected.</html>", "<html><div class=g-recaptcha></div> captcha</html>"} {
		if _, err := (Vietcap{}).Parse([]byte(page), want); !errors.Is(err, ErrRefused) {
			t.Errorf("%q: %v, want a refusal", page, err)
		}
	}
}

func TestVietcapSymbolAndRequest(t *testing.T) {
	seen := map[string]string{}
	for _, ticker := range ourHOSE {
		sym, ok := Vietcap{}.Symbol(ticker)
		if !ok || sym+".VN" != ticker {
			t.Errorf("%s → %q %v", ticker, sym, ok)
		}
		if other, dup := seen[sym]; dup {
			t.Errorf("%s and %s both map to %s", ticker, other, sym)
		}
		seen[sym] = ticker
	}
	for in, want := range map[string]string{"C32.VN": "C32", "HT1.VN": "HT1", "VCB": "", "VCB.HN": "", "vcb.VN": "", "VCBX.VN": "",
		"VC.VN": "", "1VC.VN": "", "E1VFVN30.VN": "", "VCB.VN.KS": "", "005930.KS": "", "0700.HK": "", " VCB.VN": "", "": ""} {
		if got, ok := (Vietcap{}).Symbol(in); got != want || ok != (want != "") {
			t.Errorf("%q → %q %v", in, got, ok)
		}
	}
	req, err := Vietcap{}.Request(context.Background(), "https://trading.vietcap.com.vn",
		Want{"VHM": {"VHM.VN", "VND"}, "FPT": {"FPT.VN", "VND"}, "VCB": {"VCB.VN", "VND"}})
	if err != nil {
		t.Fatal(err)
	}
	if req.Method != "POST" || req.URL.String() != "https://trading.vietcap.com.vn/api/price/symbols/getList" {
		t.Fatalf("%s %s", req.Method, req.URL)
	}
	if len(req.Header) != 2 || req.Header.Get("Accept") != "application/json" || req.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("headers %v", req.Header)
	}
	body, _ := io.ReadAll(req.Body)
	if string(body) != `{"symbols":["FPT","VCB","VHM"]}` || req.ContentLength != int64(len(body)) {
		t.Fatalf("body %s (length %d)", body, req.ContentLength)
	}
	if _, err := (Vietcap{}).Request(context.Background(), "https://x", Want{}); err == nil {
		t.Fatal("an empty request was built")
	}
}

// vietcapRobots is trading.vietcap.com.vn/robots.txt as read on 2026-10-14
// at 05:45 UTC (research robots_trading.vietcap.com.vn).
const vietcapRobots = "User-agent: *\nAllow: /\nCrawl-delay: 10\n"

// TestVietcapRateFloor: the board is never asked faster than its robots.txt
// allows. Floor (the market layer's least spacing of any two requests, fill
// re-reads included) is at least the Crawl-delay, and the poll interval at
// least the floor: 6 requests in a minute at most, 1,441 on a full HOSE day
// (every 10 s over 4 hours of sessions, plus the settled read).
func TestVietcapRateFloor(t *testing.T) {
	m := regexp.MustCompile(`(?m)^Crawl-delay:\s*(\d+)\s*$`).FindStringSubmatch(vietcapRobots)
	secs, _ := strconv.Atoi(m[1])
	delay := time.Duration(secs) * time.Second
	in := Vietcap{}.Info()
	if delay != 10*time.Second || in.Floor < delay || in.Every < in.Floor || in.Floor < MinFloor {
		t.Fatalf("crawl delay %v: floor %v, every %v", delay, in.Floor, in.Every)
	}
	if perMinute := int(time.Minute / max(in.Every, in.Floor)); perMinute > 6 {
		t.Fatalf("%d requests a minute", perMinute)
	}
	if in.Hours == nil || in.Hours.Zone != "Asia/Ho_Chi_Minh" || len(in.Hours.Sessions) != 2 {
		t.Fatalf("hours %+v", in.Hours)
	}
	loc, err := time.LoadLocation(in.Hours.Zone)
	if err != nil {
		t.Fatal(err)
	}
	var open time.Duration
	for _, w := range in.Hours.Sessions {
		from, _ := time.ParseInLocation("15:04", w.From, loc)
		to, _ := time.ParseInLocation("15:04", w.To, loc)
		open += to.Sub(from)
	}
	if polls := int(open/in.Every) + 1; open != 4*time.Hour || polls != 1441 {
		t.Fatalf("open %v, %d requests a day", open, polls)
	}
	q := in.Quiet
	if len(q) != 1 || q[0].From != "14:30" || q[0].To != in.Hours.Sessions[1].To || in.Settle != 5*time.Minute || in.Times != TimesNone {
		t.Fatalf("quiet %v settle %v times %s", q, in.Settle, in.Times)
	}
}

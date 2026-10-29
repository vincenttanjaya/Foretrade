package rtquote

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// misWant asks for every row of a MIS fixture, as the market layer would,
// checking that each row's symbol is our ticker's.
func misWant(t *testing.T, body []byte) Want {
	t.Helper()
	var doc struct {
		MsgArray []struct{ C, Ex string } `json:"msgArray"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	want := Want{}
	for _, r := range doc.MsgArray {
		ticker := r.C + ".TW"
		sym, ok := MIS{}.Symbol(ticker)
		if !ok || sym != r.Ex+"_"+r.C+".tw" {
			t.Fatalf("%s maps to %q, %v", ticker, sym, ok)
		}
		want[sym] = Wanted{Ticker: ticker, Currency: "TWD"}
	}
	return want
}

// misUTC is an RFC 3339 time (the TWSE tests' clock).
func misUTC(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// TestMISParseSession: MIS's 52-symbol answer at 13:19:33 Taipei on
// 2026-10-14 (research tw_mis_r9_all52, trimmed): every row is ours and
// parses, priced and dated by its last trade (trade.z, trade.t) whether or
// not the newest update traded, volume in shares, the server clock as the
// stamp, and the market is open.
func TestMISParseSession(t *testing.T) {
	body := fixture(t, "mis_session.json")
	want := misWant(t, body)
	b, err := MIS{}.Parse(body, want)
	if err != nil {
		t.Fatal(err)
	}
	if len(want) != 52 || len(b.Rows) != 52 || len(b.Rejected) != 0 || b.Closed {
		t.Fatalf("want %d, rows %d, rejected %v, closed %v", len(want), len(b.Rows), b.Rejected, b.Closed)
	}
	if !b.Stamp.Equal(misUTC("2026-10-14T05:19:33Z")) {
		t.Fatalf("stamp %v", b.Stamp)
	}
	rows := byTicker(b)
	for ticker, w := range map[string]struct {
		price  float64
		trade  string
		volume float64
	}{
		"2330.TW": {2515, "2026-10-14T05:18:00Z", 14_119_000}, // z "-": the newest update was a quote; the trade is older
		"4904.TW": {107.5, "2026-10-14T05:19:02Z", 3_217_000}, // z = trade.z: the newest update traded
		"2059.TW": {9980, "2026-10-14T05:13:58Z", 511_000},
		"1303.TW": {350, "2026-10-14T05:18:30Z", 168_731_000},
	} {
		r := rows[ticker]
		if r.Price != w.price || !r.TradeAt.Equal(misUTC(w.trade)) || r.Volume == nil || *r.Volume != w.volume || r.Currency != "TWD" {
			t.Errorf("%s: %+v (volume %v), want %+v", ticker, r, r.Volume, w)
		}
	}
	for _, r := range b.Rows { // every trade is before the server's clock and within the session
		if r.TradeAt.After(b.Stamp) || r.TradeAt.Before(misUTC("2026-10-14T05:00:00Z")) {
			t.Errorf("%s traded at %v", r.Ticker, r.TradeAt)
		}
	}
	// the canaries' newest trade (Nan Ya's), 63 s before the server's clock: under MaxLag
	newest := time.Time{}
	for _, c := range (MIS{}).Info().Canaries {
		if r, ok := rows[c]; ok && r.TradeAt.After(newest) {
			newest = r.TradeAt
		}
	}
	if age := b.Stamp.Sub(newest); age != 63*time.Second || age > (MIS{}).Info().MaxLag {
		t.Fatalf("newest canary trade %v old", age)
	}
}

// TestMISAfterClose: our 52 symbols read with the honest User-Agent at
// 20:37:42 Taipei on 2026-10-14: every row is the 13:30:00 closing trade,
// with the regular session's volume (the after-hours fv lots, and the trial
// pz, are not read); the rows carry today's date, so the answer is not
// Closed.
func TestMISAfterClose(t *testing.T) {
	body := fixture(t, "mis_after_close.json")
	b, err := MIS{}.Parse(body, misWant(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Rows) != 52 || len(b.Rejected) != 0 || b.Closed || !b.Stamp.Equal(misUTC("2026-10-14T12:37:44Z")) {
		t.Fatalf("rows %d rejected %v closed %v stamp %v", len(b.Rows), b.Rejected, b.Closed, b.Stamp)
	}
	for _, r := range b.Rows {
		if !r.TradeAt.Equal(misUTC("2026-10-14T05:30:00Z")) {
			t.Errorf("%s at %v", r.Ticker, r.TradeAt)
		}
	}
	rows := byTicker(b)
	if r := rows["1303.TW"]; r.Price != 349 || *r.Volume != 173_438_000 { // fv 66 after-hours lots are not in it
		t.Fatalf("Nan Ya %+v", r)
	}
	if r := rows["2301.TW"]; r.Price != 283 { // the closing trade, not the trial pz 283.5
		t.Fatalf("Lite-On %v", r.Price)
	}
}

// misEdit is the mis_session.json answer with its rows replaced by edits of
// its first (2330) row, each under its own code.
func misEdit(t *testing.T, edits map[string]func(map[string]any)) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(fixture(t, "mis_session.json"), &doc); err != nil {
		t.Fatal(err)
	}
	base, _ := json.Marshal(doc["msgArray"].([]any)[0])
	var rows []any
	for code, edit := range edits {
		var r map[string]any
		json.Unmarshal(base, &r)
		r["c"], r["ch"] = code, code+".tw"
		edit(r)
		rows = append(rows, r)
	}
	doc["msgArray"] = rows
	out, _ := json.Marshal(doc)
	return out
}

// TestMISRowEdges: per-row reasons, from the real TSMC row changed one field
// at a time; the rest of the answer stands.
func TestMISRowEdges(t *testing.T) {
	noTrade := func(r map[string]any) { delete(r, "trade") }
	body := misEdit(t, map[string]func(map[string]any){
		"2330": func(map[string]any) {},
		"2317": func(r map[string]any) { // no trade object: z and t
			noTrade(r)
			r["z"], r["t"], r["u"], r["w"] = "244.5000", "13:19:20", "264.0000", "216.0000"
		},
		// no trade object and z "-": nothing traded yet
		"2454": noTrade,
		"2308": func(r map[string]any) { noTrade(r); r["z"], r["ts"] = "2500.0000", "1" }, // a trial match is not a trade
		"2881": func(r map[string]any) { r["trade"].(map[string]any)["z"] = "2,515" },
		"2882": func(r map[string]any) { r["trade"].(map[string]any)["t"] = "1:18 PM" },
		"2891": func(r map[string]any) { r["trade"].(map[string]any)["t"] = "14:30:00" },  // after hours
		"3008": func(r map[string]any) { r["trade"].(map[string]any)["z"] = "2740.0000" }, // over limit-up 2735
		"2357": func(r map[string]any) { r["v"] = "14,119" },
		// v a number, not a string: bad parse
		"2345": func(r map[string]any) { r["v"] = 14119 },
		"3231": func(r map[string]any) { r["v"] = "-"; r["u"], r["w"] = "-", "-" }, // no volume and no limits: still a quote
		"9999": func(map[string]any) {},
		"6488": func(r map[string]any) { r["ex"] = "otc" }, // a TPEx listing
	})
	want := Want{}
	for _, ticker := range []string{"2330.TW", "2317.TW", "2454.TW", "2308.TW", "2881.TW", "2882.TW", "2891.TW", "3008.TW",
		"2357.TW", "2345.TW", "3231.TW", "6488.TWO"} {
		sym, _ := MIS{}.Symbol(ticker)
		want[sym] = Wanted{Ticker: ticker, Currency: "TWD"}
	}
	b, err := MIS{}.Parse(body, want)
	if err != nil {
		t.Fatal(err)
	}
	rows := byTicker(b)
	if len(rows) != 4 || rows["2330.TW"].Price != 2515 || rows["6488.TWO"].Price != 2515 || rows["3231.TW"].Volume != nil {
		t.Fatalf("rows %+v", b.Rows)
	}
	if r := rows["2317.TW"]; r.Price != 244.5 || !r.TradeAt.Equal(misUTC("2026-10-14T05:19:20Z")) {
		t.Fatalf("z and t: %+v", r)
	}
	reasons := map[string]string{}
	for _, r := range b.Rejected {
		reasons[r.Symbol] = r.Reason
	}
	for sym, frag := range map[string]string{"tse_2454.tw": "no trade yet", "tse_2308.tw": "no trade yet",
		"tse_2881.tw": "price: ", "tse_2882.tw": "trade time", "tse_2891.tw": "after-hours trade at 14:30:00",
		"tse_3008.tw": "outside the day's limits 2245-2735", "tse_2357.tw": "whole number of lots",
		"tse_2345.tw": "bad parse", "tse_9999.tw": "not asked for"} {
		if !strings.Contains(reasons[sym], frag) {
			t.Errorf("%s: %q, want %q", sym, reasons[sym], frag)
		}
	}
	if len(b.Rejected) != 9 || b.Closed {
		t.Fatalf("rejected %v closed %v", b.Rejected, b.Closed)
	}
}

// TestMISStale: rows whose trading date is before the server's (a day TWSE
// does not trade, such as a typhoon closure) still parse, at their own old
// trade times, and the answer is Closed; one row on today's date is enough
// for it not to be.
func TestMISStale(t *testing.T) {
	yesterday := func(r map[string]any) { r["d"] = "20261013" }
	body := misEdit(t, map[string]func(map[string]any){"2330": yesterday, "2317": yesterday})
	want := Want{"tse_2330.tw": {"2330.TW", "TWD"}, "tse_2317.tw": {"2317.TW", "TWD"}}
	b, err := MIS{}.Parse(body, want)
	if err != nil || len(b.Rows) != 2 || !b.Closed {
		t.Fatalf("%+v %v", b, err)
	}
	if r := byTicker(b)["2330.TW"]; !r.TradeAt.Equal(misUTC("2026-10-13T05:18:00Z")) {
		t.Fatalf("trade %v", r.TradeAt)
	}
	body = misEdit(t, map[string]func(map[string]any){"2330": yesterday, "2317": func(map[string]any) {}})
	if b, err := (MIS{}).Parse(body, want); err != nil || b.Closed {
		t.Fatalf("one row today: closed %v, %v", b.Closed, err)
	}
}

// TestMISRefusals: TWSE's firewall page (our first honest read, 12:36:09 UTC
// on 2026-10-14, HTTP 502) is a refusal with its support ID whether it comes
// as a 502 (Blocked) or a 200 (Parse); any other bad answer is a failure.
func TestMISRefusals(t *testing.T) {
	page := fixture(t, "mis_blocked.html")
	why, ok := MIS{}.Blocked(502, page)
	if !ok || why != "HTTP 502: TWSE's firewall block page (FOR SECURITY REASONS, THIS PAGE CAN NOT BE ACCESSED), support ID 7641438222307404480" {
		t.Fatalf("%q %v", why, ok)
	}
	if _, err := (MIS{}).Parse(page, Want{}); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "7641438222307404480") {
		t.Fatalf("a 200 block page: %v", err)
	}
	if _, ok := (MIS{}).Blocked(502, []byte("<html><body>502 Bad Gateway</body></html>")); ok {
		t.Fatal("a plain 502 read as a refusal")
	}
	for _, bad := range []string{"", "\n\n", "<html>maintenance</html>", `{"msgArray":[],"rtcode":"9999","rtmessage":"Error"}`,
		`{"rtcode":"0000","rtmessage":"OK"}`, `{"msgArray":{},"rtcode":"0000"}`} {
		_, err := MIS{}.Parse([]byte(bad), Want{})
		if err == nil || errors.Is(err, ErrRefused) {
			t.Errorf("%q: %v, want a failure", bad, err)
		}
	}
}

// TestMISSymbolAndRequest: every TWSE ticker of ours maps (the 52 of the
// fixtures and the screener's 2303, 2412 and 3711), a TPEx one to otc_, junk
// does not; the request carries them all in one GET, %7C-separated, with
// json=1 and delay=0 and no header but Accept.
func TestMISSymbolAndRequest(t *testing.T) {
	ours := misWant(t, fixture(t, "mis_after_close.json"))
	for _, ticker := range []string{"2303.TW", "2412.TW", "3711.TW"} {
		sym, ok := MIS{}.Symbol(ticker)
		if !ok || sym != "tse_"+strings.TrimSuffix(ticker, ".TW")+".tw" {
			t.Fatalf("%s → %q", ticker, sym)
		}
		ours[sym] = Wanted{Ticker: ticker, Currency: "TWD"}
	}
	if len(ours) != 55 {
		t.Fatalf("%d of our tickers", len(ours))
	}
	for in, want := range map[string]string{"2330.TW": "tse_2330.tw", "00878.TW": "tse_00878.tw", "00632R.TW": "tse_00632R.tw",
		"6488.TWO": "otc_6488.tw", "2330": "", "2330.tw": "", "233.TW": "", "2330.TW.KS": "", "0700.HK": "", "2330.TWX": ""} {
		if got, ok := (MIS{}).Symbol(in); got != want || ok != (want != "") {
			t.Errorf("%s → %q %v", in, got, ok)
		}
	}
	req, err := MIS{}.Request(context.Background(), "https://mis.twse.com.tw",
		Want{"tse_2330.tw": {"2330.TW", "TWD"}, "tse_2317.tw": {"2317.TW", "TWD"}, "otc_6488.tw": {"6488.TWO", "TWD"}})
	if err != nil {
		t.Fatal(err)
	}
	if req.Method != "GET" || req.URL.String() !=
		"https://mis.twse.com.tw/stock/api/getStockInfo.jsp?ex_ch=otc_6488.tw%7Ctse_2317.tw%7Ctse_2330.tw&json=1&delay=0" {
		t.Fatalf("%s %s", req.Method, req.URL)
	}
	if len(req.Header) != 1 || req.Header.Get("Accept") != "application/json" {
		t.Fatalf("headers %v", req.Header)
	}
	if req, err := (MIS{}).Request(context.Background(), "https://mis.twse.com.tw", ours); err != nil ||
		strings.Count(req.URL.RawQuery, "%7C") != 54 || len(req.URL.String()) > 2048 {
		t.Fatalf("all 55: %v, %d bytes", err, len(req.URL.String()))
	}
	if _, err := (MIS{}).Request(context.Background(), "https://x", Want{}); err == nil {
		t.Fatal("an empty request was built")
	}
}

// TestMISRateFloor: MIS is polled every 10 s while open and never twice
// within 5 s (fill re-reads included): one request per 5 s at most, under
// the 3 per 5 s twstock documents, so 1,620 a day over the 270-minute
// session plus the settled read; its trade-time health allows 90 s.
func TestMISRateFloor(t *testing.T) {
	in := MIS{}.Info()
	if in.Floor < MinFloor || in.Floor != 5*time.Second || in.Every != 10*time.Second {
		t.Fatalf("every %v floor %v", in.Every, in.Floor)
	}
	if per5s := int(5 * time.Second / in.Floor); per5s > 3 {
		t.Fatalf("%d requests in 5 s, over the 3 documented", per5s)
	}
	if day := int(270*time.Minute/in.Every) + 1; day != 1621 {
		t.Fatalf("%d requests a day", day)
	}
	if in.Venue != "TWSE" || in.Switch != "twse" || in.Gate != "" || in.Times != TimesTrade || in.Class != ClassRealtime ||
		in.MaxLag != 90*time.Second || in.Settle != 5*time.Minute || len(in.Quiet) != 1 || in.Quiet[0] != (Window{"13:25", "13:30"}) ||
		in.Hours != nil {
		t.Fatalf("%+v", in)
	}
}

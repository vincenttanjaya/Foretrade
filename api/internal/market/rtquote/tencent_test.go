package rtquote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The fixtures are saved answers trimmed by
// scratchpad/rt-hkex/scripts/trim_tencent.py: our HKEX codes only, GBK as
// served, every field the adapter does not read blanked (positions kept).
// tencent_session.txt: 89 codes read at 13:32:02 HKT on 2026-10-14 (research
// hk_tencent_r5_rt_all); tencent_r4_rt6.txt and tencent_r4_plain.txt: the
// same six names on the r_ and the plain prefix at 13:16:36 and 13:16:27
// (hk_tencent_r4_rt_6, hk_tencent_r4_plain); tencent_lunch.txt: both
// prefixes at 12:45:32 (hk_tencent_lunch); tencent_after_close.txt: our 89
// codes at 20:02:51 HKT, read with the honest User-Agent.

var tencentFixtureLine = regexp.MustCompile(`v_((?:r_)?hk([0-9]{5}))="`)

// tencentWant asks for every code in a fixture as the market layer would:
// our ticker (four digits under 10000) through Symbol.
func tencentWant(t *testing.T, body []byte) Want {
	t.Helper()
	want := Want{}
	for _, m := range tencentFixtureLine.FindAllSubmatch(body, -1) {
		code := string(m[2])
		ticker := strings.TrimPrefix(code, "0") + ".HK"
		sym, ok := Tencent{}.Symbol(ticker)
		if !ok || sym != "r_hk"+code {
			t.Fatalf("%s maps to %q, %v", ticker, sym, ok)
		}
		want[sym] = Wanted{Ticker: ticker, Currency: "HKD"}
	}
	if len(want) == 0 {
		t.Fatal("no code in the fixture")
	}
	return want
}

func tencentParse(t *testing.T, name string) (Batch, Want) {
	t.Helper()
	body := fixture(t, name)
	want := tencentWant(t, body)
	b, err := Tencent{}.Parse(body, want)
	if err != nil {
		t.Fatal(err)
	}
	return b, want
}

func tencentHKT(h, m, s int) time.Time { return time.Date(2026, 10, 14, h, m, s, 0, tencentZone).UTC() }

// TestTencentParseSession: the 89-code read at 13:32:02 HKT: every row is
// ours and parses (price, volume, currency, the trade time in HKT), none is
// later than the read, the thinnest name's last trade was 293 s before it,
// and there is no server clock or market state to read.
func TestTencentParseSession(t *testing.T) {
	b, want := tencentParse(t, "tencent_session.txt")
	if len(want) != 89 || len(b.Rows) != 89 || len(b.Rejected) != 0 || b.Closed || !b.Stamp.IsZero() {
		t.Fatalf("want %d, rows %d, rejected %v, closed %v, stamp %v", len(want), len(b.Rows), b.Rejected, b.Closed, b.Stamp)
	}
	rows := byTicker(b)
	read := time.Date(2026, 10, 14, 5, 32, 2, 157e6, time.UTC)
	oldest := read
	for ticker, r := range rows {
		if r.Currency != "HKD" || r.Volume == nil || *r.Volume <= 0 || r.Price <= 0 || r.TradeAt.After(read) {
			t.Fatalf("%s %+v", ticker, r)
		}
		if r.TradeAt.Before(oldest) {
			oldest = r.TradeAt
		}
	}
	if !oldest.Equal(tencentHKT(13, 27, 9)) {
		t.Fatalf("oldest trade %v", oldest)
	}
	for ticker, w := range map[string]struct {
		price, volume float64
		at            time.Time
	}{
		"0700.HK": {430, 10_820_861, tencentHKT(13, 32, 0)},
		"9988.HK": {113, 56_287_214, tencentHKT(13, 32, 0)},
		"1810.HK": {26.66, 164_737_346, tencentHKT(13, 32, 0)},
		"0939.HK": {9.835, 91_661_294, tencentHKT(13, 31, 55)},
		"0001.HK": {66.75, 1_881_195, tencentHKT(13, 31, 57)},
		"1038.HK": {67.5, 476_176, tencentHKT(13, 27, 9)}, // the thinnest: 293 s before the read
	} {
		r := rows[ticker]
		if r.Price != w.price || *r.Volume != w.volume || !r.TradeAt.Equal(w.at) {
			t.Errorf("%s %+v (volume %v), want %+v", ticker, r, *r.Volume, w)
		}
	}
	if got := rows["0700.HK"].TradeAt.Format(time.RFC3339); got != "2026-10-14T05:32:00Z" {
		t.Fatal(got)
	}
}

// TestTencentDelayedFeed: the plain prefix is the 15-minute feed. Read in the
// same minute as the r_ prefix, its trade times are 15 minutes older; every
// plain line is rejected as the delayed feed (with the ticker it was for),
// so an answer of plain lines lands no row; in a mixed answer the r_ rows
// stand. In the lunch break both read 11:59:59.
func TestTencentDelayedFeed(t *testing.T) {
	rt, _ := tencentParse(t, "tencent_r4_rt6.txt")
	plainBody := fixture(t, "tencent_r4_plain.txt")
	plain, err := Tencent{}.Parse(plainBody, tencentWant(t, bytes.ReplaceAll(plainBody, []byte("v_hk"), []byte("v_r_hk"))))
	if err != nil {
		t.Fatal(err)
	}
	if len(rt.Rows) != 6 || len(plain.Rows) != 0 || len(plain.Rejected) != 6 {
		t.Fatalf("r_ rows %d; plain rows %d rejected %d", len(rt.Rows), len(plain.Rows), len(plain.Rejected))
	}
	for _, r := range plain.Rejected {
		if !strings.Contains(r.Reason, "delayed feed") || r.Ticker == "" || !strings.HasPrefix(r.Symbol, "hk") {
			t.Errorf("%+v", r)
		}
	}
	// the same six names re-labelled r_ (a silent switch): they parse, and
	// their trade times are what gives them away
	silent, err := Tencent{}.Parse(bytes.ReplaceAll(plainBody, []byte("v_hk"), []byte("v_r_hk")), tencentWant(t, bytes.ReplaceAll(plainBody, []byte("v_hk"), []byte("v_r_hk"))))
	if err != nil || len(silent.Rows) != 6 {
		t.Fatal(silent, err)
	}
	fresh, stale := byTicker(rt), byTicker(silent)
	readRT := time.Date(2026, 10, 14, 5, 16, 36, 375e6, time.UTC)
	readPlain := time.Date(2026, 10, 14, 5, 16, 27, 871e6, time.UTC)
	for ticker, r := range fresh {
		if lag := readRT.Sub(r.TradeAt); lag < 0 || lag > 4*time.Second {
			t.Errorf("r_ %s %v behind", ticker, lag)
		}
		if lag := readPlain.Sub(stale[ticker].TradeAt); lag < 906*time.Second || lag > 909*time.Second {
			t.Errorf("plain %s %v behind", ticker, lag)
		}
	}
	lunch, _ := tencentParse(t, "tencent_lunch.txt")
	if len(lunch.Rows) != 2 || len(lunch.Rejected) != 2 {
		t.Fatalf("lunch %+v", lunch)
	}
	for _, r := range lunch.Rows {
		if !r.TradeAt.Equal(tencentHKT(11, 59, 59)) {
			t.Errorf("lunch %+v", r)
		}
	}
}

// TestTencentAfterClose: our 89 codes read at 20:02:51 HKT on 2026-10-14
// with the honest User-Agent: every row is the closing auction's price,
// dated 16:08:26-16:08:31 (its random close), so the settled read at 16:12
// carries it.
func TestTencentAfterClose(t *testing.T) {
	b, _ := tencentParse(t, "tencent_after_close.txt")
	if len(b.Rows) != 89 || len(b.Rejected) != 0 || b.Closed {
		t.Fatalf("rows %d rejected %v closed %v", len(b.Rows), b.Rejected, b.Closed)
	}
	for _, r := range b.Rows {
		if r.TradeAt.Before(tencentHKT(16, 8, 26)) || r.TradeAt.After(tencentHKT(16, 8, 31)) {
			t.Errorf("%s traded %v", r.Ticker, r.TradeAt)
		}
	}
	rows := byTicker(b)
	if r := rows["0700.HK"]; r.Price != 423.4 || *r.Volume != 19_933_548 || !r.TradeAt.Equal(tencentHKT(16, 8, 31)) {
		t.Fatalf("Tencent %+v", r)
	}
	for _, c := range (Tencent{}).Info().Canaries {
		if _, ok := rows[c]; !ok {
			t.Errorf("canary %s missing", c)
		}
	}
}

// TestTencentRowEdges: per-row reasons, from a real line changed one field
// at a time; and answers that cannot be read at all.
func TestTencentRowEdges(t *testing.T) {
	var base []string
	for _, line := range strings.Split(string(fixture(t, "tencent_r4_rt6.txt")), "\n") {
		if strings.HasPrefix(line, "v_r_hk00700=") {
			base = strings.Split(strings.TrimSuffix(strings.TrimPrefix(line, `v_r_hk00700="`), `";`), "~")
		}
	}
	if len(base) != 78 {
		t.Fatalf("base line: %d fields", len(base))
	}
	line := func(name string, edit func(f []string) []string) string {
		f := edit(append([]string(nil), base...))
		return "v_" + name + `="` + strings.Join(f, "~") + `";` + "\n"
	}
	set := func(i int, v string) func([]string) []string {
		return func(f []string) []string { f[i] = v; return f }
	}
	tilde := "\x81\x7e" // a GBK character whose trail byte is '~' (U+4E8A)
	codes := map[string]func([]string) []string{
		"00001": set(1, "\xb3\xa4\xba\xcd"+tilde), // fine: 长和 and a character whose trail byte is '~', no separator
		"00002": set(0, "1"),
		"00003": set(2, "00700"),
		"00005": set(3, "0.000"),
		"00006": set(3, "-"),
		"00012": set(6, ""),
		"00016": set(30, "2026-10-14 13:16:34"),
		"00027": func(f []string) []string { return f[:30] },
		"00066": set(75, "CNY"),       // the listing's currency is the market layer's check
		"00101": set(75, "hkd"),       // not a currency code
		"00175": set(75, ""),          // none stated: the listing's
		"00241": set(46, `HK";X LTD`), // a quote in the English name does not end the line
	}
	var body strings.Builder
	want := Want{}
	for code, edit := range codes {
		body.WriteString(line("r_hk"+code, func(f []string) []string { f[2] = code; return edit(f) }))
		want["r_hk"+code] = Wanted{Ticker: strings.TrimPrefix(code, "0") + ".HK", Currency: "HKD"}
	}
	body.WriteString(line("r_hk09999", func(f []string) []string { f[2] = "09999"; return f })) // not asked for
	body.WriteString(`v_pv_none_match="1";` + "\n")                                             // Tencent knows no such code
	b, err := Tencent{}.Parse([]byte(body.String()), want)
	if err != nil {
		t.Fatal(err)
	}
	rows := byTicker(b)
	if len(rows) != 4 {
		t.Fatalf("rows %+v", b.Rows)
	}
	if r := rows["0001.HK"]; r.Price != 427.6 || r.Currency != "HKD" || !r.TradeAt.Equal(tencentHKT(13, 16, 34)) || *r.Volume != 9_559_131 {
		t.Fatalf("the name with a '~' trail byte: %+v", r)
	}
	if rows["0066.HK"].Currency != "CNY" || rows["0175.HK"].Currency != "HKD" || rows["0241.HK"].Price != 427.6 {
		t.Fatalf("currencies %+v %+v %+v", rows["0066.HK"], rows["0175.HK"], rows["0241.HK"])
	}
	reasons := map[string]string{}
	for _, r := range b.Rejected {
		reasons[r.Symbol] = r.Reason
	}
	for sym, frag := range map[string]string{"r_hk00002": "not a Hong Kong quote", "r_hk00003": "mismatched symbol",
		"r_hk00005": "not positive", "r_hk00006": "price:", "r_hk00012": "volume:", "r_hk00016": "trade time",
		"r_hk00027": "short line", "r_hk00101": "currency", "r_hk09999": "not asked for", "pv_none_match": "not a quote line"} {
		if !strings.Contains(reasons[sym], frag) {
			t.Errorf("%s: %q, want %q", sym, reasons[sym], frag)
		}
	}
	if len(b.Rejected) != 10 {
		t.Errorf("rejected %+v", b.Rejected)
	}
	// answers that cannot be read: an error; a block or challenge page: a refusal
	for _, bad := range []string{"", "   \n", "\x00\x01", `{"code":-1}`, `v_r_hk00700="100~x`, `v_r_hk00700="100~x"; junk`,
		`v_r_hk00700 = "1";`, `x=1; v_r_hk00700="1";`} {
		_, err := Tencent{}.Parse([]byte(bad), want)
		if err == nil || errors.Is(err, ErrRefused) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	for _, page := range []string{"<html><body>Just a moment...</body></html>", "\n<!DOCTYPE html><title>403</title>"} {
		if _, err := (Tencent{}).Parse([]byte(page), want); !errors.Is(err, ErrRefused) {
			t.Errorf("%q: %v, want a refusal", page, err)
		}
	}
}

// TestTencentSymbolAndRequest: every one of our HKEX tickers (listed rows,
// named feeds, the screener's constituents) maps to its own r_hk symbol and
// back; nothing else maps; one GET carries them all, with Accept only.
func TestTencentSymbolAndRequest(t *testing.T) {
	for in, want := range map[string]string{"0700.HK": "r_hk00700", "0001.HK": "r_hk00001", "9988.HK": "r_hk09988",
		"80737.HK": "r_hk80737", "00700.HK": "", "700.HK": "", "0700.hk": "", "0700.HK.KS": "", "0700": "", "AAPL": "",
		"7203.T": "", "005930.KS": "", "2330.TW": "", "D05.SI": "", "": ""} {
		if got, ok := (Tencent{}).Symbol(in); got != want || ok != (want != "") {
			t.Errorf("%q → %q %v", in, got, ok)
		}
	}
	ours := map[string]bool{}
	var listed struct {
		Rows []struct{ Symbol, Venue string } `json:"rows"`
	}
	if err := json.Unmarshal(tencentRead(t, "../listed.json"), &listed); err != nil {
		t.Fatal(err)
	}
	for _, r := range listed.Rows {
		if r.Venue == "HKEX" {
			ours[r.Symbol] = true
		}
	}
	var screener []struct{ Market, Yahoo string }
	if err := json.Unmarshal(tencentRead(t, "../../screener/constituents.json"), &screener); err != nil {
		t.Fatal(err)
	}
	for _, r := range screener {
		if r.Market == "HK" {
			ours[r.Yahoo] = true
		}
	}
	for _, m := range regexp.MustCompile(`"([0-9]{4}\.HK)"`).FindAllSubmatch(tencentRead(t, "../universe_quotefeed.go"), -1) {
		ours[string(m[1])] = true
	}
	if len(ours) < 95 {
		t.Fatalf("%d HKEX tickers", len(ours))
	}
	want := Want{}
	for ticker := range ours {
		sym, ok := Tencent{}.Symbol(ticker)
		if !ok {
			t.Errorf("%s does not map", ticker)
			continue
		}
		if w, dup := want[sym]; dup {
			t.Errorf("%s and %s both map to %s", ticker, w.Ticker, sym)
		}
		if back := strings.TrimPrefix(sym[4:], "0") + ".HK"; back != ticker {
			t.Errorf("%s → %s → %s", ticker, sym, back)
		}
		want[sym] = Wanted{Ticker: ticker, Currency: "HKD"}
	}
	// every code of the session read is ours, and every one of ours that is
	// a named feed or listed row was in it
	session := tencentWant(t, fixture(t, "tencent_session.txt"))
	for sym, w := range session {
		if want[sym].Ticker != w.Ticker {
			t.Errorf("%s (%s) in the session read is not ours", sym, w.Ticker)
		}
	}
	req, err := Tencent{}.Request(context.Background(), "https://qt.gtimg.cn", want)
	if err != nil {
		t.Fatal(err)
	}
	if req.Method != "GET" || !strings.HasPrefix(req.URL.String(), "https://qt.gtimg.cn/q=r_hk00001,r_hk00002,") ||
		strings.Count(req.URL.String(), "r_hk") != len(want) || len(req.URL.String()) > 2048 {
		t.Fatalf("%s %s", req.Method, req.URL)
	}
	if len(req.Header) != 1 || req.Header.Get("Accept") != "*/*" {
		t.Fatalf("headers %v", req.Header)
	}
	req, _ = Tencent{}.Request(context.Background(), "https://qt.gtimg.cn", Want{"r_hk09988": {"9988.HK", "HKD"}, "r_hk00700": {"0700.HK", "HKD"}})
	if req.URL.String() != "https://qt.gtimg.cn/q=r_hk00700,r_hk09988" {
		t.Fatal(req.URL)
	}
	for _, bad := range []Want{{}, {"hk00700": {"0700.HK", "HKD"}}, {"r_hk00700,x": {"0700.HK", "HKD"}}} {
		if _, err := (Tencent{}).Request(context.Background(), "https://qt.gtimg.cn", bad); err == nil {
			t.Errorf("%v: a request was built", bad)
		}
	}
}

// TestTencentInfo: the schedule the owner allowed (every 10 s, never under
// 5 s apart), real time by trade time, a settled read past the closing
// auction, the three canaries, no hours of its own (HKEX has a verified
// calendar) and no quiet window.
func TestTencentInfo(t *testing.T) {
	in := Tencent{}.Info()
	if in.Name != "tencent" || in.Venue != "HKEX" || in.Switch != "hkex" || in.Gate != "" || in.Host != "https://qt.gtimg.cn" ||
		in.Class != ClassRealtime || in.Times != TimesTrade {
		t.Fatalf("%+v", in)
	}
	if in.Every != 10*time.Second || in.Floor != 5*time.Second || in.Floor < MinFloor || in.Every < in.Floor {
		t.Fatalf("every %v floor %v: at most one request per 5 s, one per 10 s by design", in.Every, in.Floor)
	}
	if in.Settle != 12*time.Minute || in.MaxLag != 90*time.Second || in.Hours != nil || len(in.Quiet) != 0 {
		t.Fatalf("settle %v maxLag %v hours %v quiet %v", in.Settle, in.MaxLag, in.Hours, in.Quiet)
	}
	if strings.Join(in.Canaries, ",") != "9988.HK,0700.HK,1810.HK" {
		t.Fatal(in.Canaries)
	}
}

// tencentFields steps over GBK characters whole, the four-byte GB18030 form
// included, and keeps empty fields.
func TestTencentFields(t *testing.T) {
	for _, c := range []struct {
		in   string
		want []string
	}{
		{"a~b", []string{"a", "b"}},
		{"~~", []string{"", "", ""}},
		{"", []string{""}},
		{"\x81\x7e~x", []string{"\x81\x7e", "x"}},
		{"\x81\x30\x81\x30~\xb3\xa4~", []string{"\x81\x30\x81\x30", "\xb3\xa4", ""}},
		{"x~\x81", []string{"x", "\x81"}}, // a lone lead byte at the end
	} {
		var got []string
		for _, f := range tencentFields([]byte(c.in), '~') {
			got = append(got, string(f))
		}
		if strings.Join(got, "|") != strings.Join(c.want, "|") || len(got) != len(c.want) {
			t.Errorf("%q: %q, want %q", c.in, got, c.want)
		}
	}
}

func tencentRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

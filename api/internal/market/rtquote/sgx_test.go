package rtquote

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// sgxBoard is a fixture's rows as the test reads them, by code, beside
// SGX{}.Parse.
type sgxBoard struct {
	built time.Time
	rows  map[string]map[string]any
	order []string
}

func sgxReadBoard(t *testing.T, body []byte) sgxBoard {
	t.Helper()
	var doc struct {
		Meta struct {
			ProcessedTime int64 `json:"processedTime"`
		} `json:"meta"`
		Data struct {
			Prices []map[string]any `json:"prices"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	b := sgxBoard{built: time.UnixMilli(doc.Meta.ProcessedTime).UTC(), rows: map[string]map[string]any{}}
	for _, r := range doc.Data.Prices {
		code := r["nc"].(string)
		b.rows[code] = r
		b.order = append(b.order, code)
	}
	return b
}

// sgxWant asks for every code of a board, as the market layer would: each
// is our Yahoo ticker less ".SI".
func sgxWant(t *testing.T, b sgxBoard) Want {
	t.Helper()
	want := Want{}
	for _, code := range b.order {
		ticker := code + ".SI"
		sym, ok := SGX{}.Symbol(ticker)
		if !ok || sym != code {
			t.Fatalf("%s maps to %q, %v", ticker, sym, ok)
		}
		ccy := "SGD"
		if code == "J36" || code == "H78" || code == "D01" { // quoted in USD on SGX (listed.json, universe_quotefeed.go)
			ccy = "USD"
		}
		want[sym] = Wanted{Ticker: ticker, Currency: ccy}
	}
	return want
}

func sgxParse(t *testing.T, name string) (Batch, sgxBoard) {
	t.Helper()
	body := fixture(t, name)
	board := sgxReadBoard(t, body)
	b, err := SGX{}.Parse(body, sgxWant(t, board))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return b, board
}

func sgxUTC(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// TestSGXParseSession: the board at 08:21:05 UTC on 2026-10-14 (research
// s7_sgx_secall, trimmed to our 31 codes): every row parses, field for
// field: the ticker is the code plus .SI, the price is lt, the volume is vl
// in thousands of shares made shares, there is no trade time of the row's
// own, the currency is left to the listing (SGX states none), and the Stamp
// is the snapshot minute (08:20 UTC), not SGX's build time (08:21:06).
func TestSGXParseSession(t *testing.T) {
	b, board := sgxParse(t, "sgx_s7_session.json")
	if len(b.Rows) != 31 || len(b.Rejected) != 0 || b.Closed {
		t.Fatalf("rows %d rejected %v closed %v", len(b.Rows), b.Rejected, b.Closed)
	}
	if !b.Stamp.Equal(sgxUTC("2026-10-14T08:20:00Z")) || !board.built.Equal(sgxUTC("2026-10-14T08:21:06.007Z")) {
		t.Fatalf("stamp %v, built %v", b.Stamp, board.built)
	}
	seen := map[string]bool{}
	for _, r := range b.Rows {
		code := strings.TrimSuffix(r.Ticker, ".SI")
		src, ok := board.rows[code]
		switch {
		case !ok || seen[code]:
			t.Fatalf("row %s not on the board, or twice", r.Ticker)
		case r.Price != src["lt"].(float64):
			t.Errorf("%s price %v, lt %v", r.Ticker, r.Price, src["lt"])
		case r.Volume == nil || *r.Volume != math.Round(src["vl"].(float64)*1000) || *r.Volume != math.Trunc(*r.Volume):
			t.Errorf("%s volume %v, vl %v", r.Ticker, r.Volume, src["vl"])
		case !r.TradeAt.IsZero() || r.Currency != "":
			t.Errorf("%s trade %v currency %q", r.Ticker, r.TradeAt, r.Currency)
		}
		seen[code] = true
	}
	rows := byTicker(b)
	if d := rows["D05.SI"]; d.Price != 74.12 || *d.Volume != 2_925_600 {
		t.Fatalf("DBS %v %v", d.Price, *d.Volume)
	}
	if j := rows["J36.SI"]; j.Price != 57.87 || *j.Volume != 380_640 { // Jardine Matheson, in USD
		t.Fatalf("Jardine %v %v", j.Price, *j.Volume)
	}
}

// TestSGXSplitSnapshot: at 08:17:02 UTC (s5) the board held two minutes, the
// REITs at 08:16 and the rest at 08:15: every row stands and the Stamp is
// the older minute, so no row is dated newer than its own snapshot.
func TestSGXSplitSnapshot(t *testing.T) {
	b, board := sgxParse(t, "sgx_s5_split.json")
	if len(b.Rows) != 31 || len(b.Rejected) != 0 {
		t.Fatalf("rows %d rejected %v", len(b.Rows), b.Rejected)
	}
	var later []string
	for _, code := range board.order {
		if board.rows[code]["trading_time"] == "20261014_081600" {
			later = append(later, code)
		}
	}
	slices.Sort(later)
	if !slices.Equal(later, []string{"A17U", "AJBU", "BUOU", "C38U", "J69U", "M44U", "ME8U", "N2IU"}) {
		t.Fatalf("08:16 rows %v", later)
	}
	if !b.Stamp.Equal(sgxUTC("2026-10-14T08:15:00Z")) {
		t.Fatalf("stamp %v, want the older minute", b.Stamp)
	}
}

// TestSGXBreakAndReopen: through the 12:00-13:00 SGT break the minute moves
// (04:57, 04:58 UTC) and no volume does, so the volume clock dates nothing
// new; the 13:00 snapshot (read 05:01:50) moves all 31 volumes. Between two
// session minutes (s7, s8) four thin names keep their volume: a fresh
// minute does not make them fresh.
func TestSGXBreakAndReopen(t *testing.T) {
	r1, _ := sgxParse(t, "sgx_r1_break.json")
	r2, _ := sgxParse(t, "sgx_r2_break.json")
	r3, _ := sgxParse(t, "sgx_r3_reopen.json")
	for _, c := range []struct {
		b     Batch
		stamp string
	}{{r1, "2026-10-14T04:57:00Z"}, {r2, "2026-10-14T04:58:00Z"}, {r3, "2026-10-14T05:00:00Z"}} {
		if len(c.b.Rows) != 31 || !c.b.Stamp.Equal(sgxUTC(c.stamp)) {
			t.Fatalf("rows %d stamp %v, want %s", len(c.b.Rows), c.b.Stamp, c.stamp)
		}
	}
	moved := func(a, b Batch) (out []string) {
		ra, rb := byTicker(a), byTicker(b)
		for ticker, r := range ra {
			if *r.Volume != *rb[ticker].Volume {
				out = append(out, ticker)
			}
		}
		return out
	}
	if m := moved(r1, r2); len(m) != 0 {
		t.Fatalf("volumes moved in the break: %v", m)
	}
	if m := moved(r2, r3); len(m) != 31 {
		t.Fatalf("%d volumes moved at the reopen, want 31", len(m))
	}
	if d := byTicker(r3)["D05.SI"]; *d.Volume != 1_703_830 || d.Price != 74.1 {
		t.Fatalf("DBS at the reopen %v %v", d.Price, *d.Volume)
	}
	s7, _ := sgxParse(t, "sgx_s7_session.json")
	s8, _ := sgxParse(t, "sgx_s8_session.json")
	if s8.Stamp.Sub(s7.Stamp) != time.Minute {
		t.Fatalf("stamps %v → %v", s7.Stamp, s8.Stamp)
	}
	var still []string
	r7, r8 := byTicker(s7), byTicker(s8)
	for ticker := range r7 {
		if *r7[ticker].Volume == *r8[ticker].Volume {
			still = append(still, ticker)
		}
	}
	slices.Sort(still)
	if !slices.Equal(still, []string{"D01.SI", "F34.SI", "J69U.SI", "S58.SI"}) {
		t.Fatalf("unmoved %v", still)
	}
}

// TestSGXAfterClose: our read at 12:19:59 UTC on 2026-10-14 with the honest
// User-Agent: the board stopped at 09:16 UTC (17:16 SGT, the end of
// trade-at-close) while SGX built the answer at 12:20:01. The Stamp is the
// frozen minute, never the build time, so the market layer cannot date the
// close as fresh; the API has no session field, so Closed stays false.
func TestSGXAfterClose(t *testing.T) {
	b, board := sgxParse(t, "sgx_after_close.json")
	if len(b.Rows) != 31 || len(b.Rejected) != 0 || b.Closed {
		t.Fatalf("rows %d rejected %v closed %v", len(b.Rows), b.Rejected, b.Closed)
	}
	if !b.Stamp.Equal(sgxUTC("2026-10-14T09:16:00Z")) || board.built.Sub(b.Stamp) < 3*time.Hour {
		t.Fatalf("stamp %v built %v", b.Stamp, board.built)
	}
	if d := byTicker(b)["D05.SI"]; d.Price != 74.34 || *d.Volume != 4_559_810 {
		t.Fatalf("DBS close %v %v", d.Price, *d.Volume)
	}
}

// TestSGXCanaries: the canaries are the top 3 of our codes by traded value
// (v, local currency; the three USD names counted at 1.5 SGD, more than the
// rate, and still far behind) in the research's last afternoon read.
func TestSGXCanaries(t *testing.T) {
	board := sgxReadBoard(t, fixture(t, "sgx_late3_value.json"))
	value := func(code string) float64 {
		v := board.rows[code]["v"].(float64)
		if code == "J36" || code == "H78" || code == "D01" {
			v *= 1.5
		}
		return v
	}
	codes := slices.Clone(board.order)
	slices.SortFunc(codes, func(a, b string) int { return cmp.Compare(value(b), value(a)) })
	var top []string
	for _, c := range codes[:3] {
		top = append(top, c+".SI")
	}
	if got := (SGX{}).Info().Canaries; !slices.Equal(got, top) {
		t.Fatalf("canaries %v, top 3 by value %v", got, top)
	}
	if v := board.rows["D05"]["v"].(float64); v != 224300111.5 {
		t.Fatalf("DBS value %v", v)
	}
}

// TestSGXRowEdges: per-row outcomes, from DBS's real row changed one field
// at a time; the board's other codes are skipped without a word, a code
// listed twice gives its newest row, a row is stale against the answer's
// middle minute (not its newest), and the Stamp is the oldest minute of
// the rows kept (a rejected row never moves it).
func TestSGXRowEdges(t *testing.T) {
	board := sgxReadBoard(t, fixture(t, "sgx_s7_session.json")) // 08:20 snapshot, built 08:21:06
	base := board.rows["D05"]
	clone := func(code string, edit func(map[string]any)) map[string]any {
		r := map[string]any{}
		for k, v := range base {
			r[k] = v
		}
		r["nc"] = code
		edit(r)
		return r
	}
	keep := func(map[string]any) {}
	rows := []any{
		clone("D05", keep),
		clone("O39", func(r map[string]any) { r["lt"] = "28.91"; r["vl"] = "5144.47" }), // numbers in strings
		clone("U11", func(r map[string]any) { delete(r, "lt") }),
		clone("Z74", func(r map[string]any) { r["lt"] = 0.0 }),
		clone("C6L", func(r map[string]any) { r["lt"] = "6.5x" }),
		clone("S68", func(r map[string]any) { r["vl"] = nil }),
		clone("BN4", func(r map[string]any) { r["vl"] = -1.0 }),
		clone("G13", func(r map[string]any) { r["trading_time"] = "2026-10-14 08:20" }),
		clone("U96", func(r map[string]any) { r["trading_time"] = "20261014_082500" }),  // after the 08:21:06 build
		clone("A17U", func(r map[string]any) { r["trading_time"] = "20261014_081000" }), // 10 min behind the rest
		clone("B73", keep), // the board's, not ours
		clone("H78", func(r map[string]any) { r["trading_time"] = "20261014_080022"; r["type"] = "others" }),
		clone("H78", func(r map[string]any) { r["lt"] = 8.12; r["vl"] = 666.8 }),                             // the newer row stands
		clone("H78", func(r map[string]any) { r["trading_time"] = "20261013_230043"; r["type"] = "others" }), // an older one after it does not
		clone("C09", func(r map[string]any) { r["lt"] = 7.06 }),
		clone("C09", func(r map[string]any) { r["lt"] = 7.07 }), // the same minute, another price
		clone("F34", func(r map[string]any) { r["trading_time"] = "20261014_081900" }),
		clone("F34", func(r map[string]any) { r["trading_time"] = "20261014_081900" }), // the same row twice
		clone("V03", func(r map[string]any) { r["vl"] = 0.0 }),                         // no volume yet: stands
		clone("S63", func(r map[string]any) { r["trading_time"] = "20261014_082105" }), // one row ahead of the rest
	}
	body, _ := json.Marshal(map[string]any{"meta": map[string]any{"code": "200", "processedTime": board.built.UnixMilli()},
		"data": map[string]any{"prices": rows}})
	want := Want{}
	for _, c := range []string{"D05", "O39", "U11", "Z74", "C6L", "S68", "BN4", "G13", "U96", "A17U", "H78", "C09", "F34", "V03", "S63"} {
		want[c] = Wanted{Ticker: c + ".SI", Currency: "SGD"}
	}
	b, err := SGX{}.Parse(body, want)
	if err != nil {
		t.Fatal(err)
	}
	got := byTicker(b)
	var kept []string
	for ticker := range got {
		kept = append(kept, ticker)
	}
	slices.Sort(kept)
	if !slices.Equal(kept, []string{"D05.SI", "F34.SI", "H78.SI", "O39.SI", "S63.SI", "V03.SI"}) || len(b.Rows) != 6 {
		t.Fatalf("kept %v (%d rows)", kept, len(b.Rows))
	}
	if o := got["O39.SI"]; o.Price != 28.91 || *o.Volume != 5_144_470 {
		t.Fatalf("OCBC %v %v", o.Price, *o.Volume)
	}
	if h := got["H78.SI"]; h.Price != 8.12 || *h.Volume != 666_800 {
		t.Fatalf("Hongkong Land %v %v: the 08:20 row should stand", h.Price, *h.Volume)
	}
	if v := got["V03.SI"]; *v.Volume != 0 {
		t.Fatalf("Venture volume %v", *v.Volume)
	}
	// F34's minute, the oldest kept: S63's later one moves neither the Stamp
	// nor the minute stale rows are measured from (08:19 is not stale)
	if !b.Stamp.Equal(sgxUTC("2026-10-14T08:19:00Z")) {
		t.Fatalf("stamp %v", b.Stamp)
	}
	reasons := map[string][]string{}
	for _, r := range b.Rejected {
		reasons[r.Symbol] = append(reasons[r.Symbol], r.Reason)
		if r.Ticker != r.Symbol+".SI" {
			t.Errorf("reject %+v: ticker", r)
		}
	}
	for sym, frag := range map[string]string{"U11": "price (lt): missing", "Z74": "no last trade", "C6L": "not a number",
		"S68": "volume (vl): missing", "BN4": "at least 0", "G13": "unreadable", "U96": "future timestamp",
		"A17U": "stale row", "C09": "listed twice"} {
		if len(reasons[sym]) != 1 || !strings.Contains(reasons[sym][0], frag) {
			t.Errorf("%s: %q, want one %q", sym, reasons[sym], frag)
		}
	}
	if len(b.Rejected) != 9 {
		t.Errorf("%d rejected: %+v", len(b.Rejected), b.Rejected)
	}
}

// TestSGXDelayedFeed: a board whose minutes run 15 minutes behind SGX's own
// build time (a stuck or delayed snapshot) keeps its rows, dated by that
// old minute: the delay shows, and the market layer's canary check (MaxLag)
// degrades the source. Nothing in it is dated by the build time.
func TestSGXDelayedFeed(t *testing.T) {
	board := sgxReadBoard(t, fixture(t, "sgx_s7_session.json"))
	var rows []any
	for _, code := range board.order {
		r := board.rows[code]
		r["trading_time"] = "20261014_080500"
		rows = append(rows, r)
	}
	body, _ := json.Marshal(map[string]any{"meta": map[string]any{"code": "200", "processedTime": board.built.UnixMilli()},
		"data": map[string]any{"prices": rows}})
	b, err := SGX{}.Parse(body, sgxWant(t, board))
	if err != nil || len(b.Rows) != 31 {
		t.Fatalf("%d rows, %v", len(b.Rows), err)
	}
	if !b.Stamp.Equal(sgxUTC("2026-10-14T08:05:00Z")) || board.built.Sub(b.Stamp) < 16*time.Minute {
		t.Fatalf("stamp %v built %v", b.Stamp, board.built)
	}
	if in := (SGX{}).Info(); in.MaxLag >= board.built.Sub(b.Stamp) {
		t.Fatalf("maxLag %v would not catch a board %v behind", in.MaxLag, board.built.Sub(b.Stamp))
	}
}

// TestSGXRefusedOrMalformed: an HTML page served for the board is a
// refusal (a block or challenge page); an answer without the board, a
// failed meta code, API Gateway's own JSON error, a cut-off body or junk is
// an error that is not a refusal; an empty board is no rows.
func TestSGXRefusedOrMalformed(t *testing.T) {
	want := Want{"D05": {"D05.SI", "SGD"}}
	for _, page := range []string{"<html><body>Just a moment...</body></html>", "  \n<!DOCTYPE html><title>Access Denied</title>"} {
		if _, err := (SGX{}).Parse([]byte(page), want); !errors.Is(err, ErrRefused) {
			t.Errorf("%q: %v, want a refusal", page, err)
		}
	}
	cut := fixture(t, "sgx_s7_session.json")
	cut = cut[:len(cut)/2]
	for _, bad := range []string{"", "\x00\x01", "[]", `{"message":"Forbidden"}`, `{"meta":{"code":"200"}}`,
		`{"meta":{"code":"200"},"data":{}}`, `{"meta":{"code":"500","message":"error"},"data":{"prices":[]}}`,
		`{"data":{"prices":[]}}`, string(cut)} {
		_, err := SGX{}.Parse([]byte(bad), want)
		if err == nil || errors.Is(err, ErrRefused) {
			t.Errorf("%.40q: %v, want an error that is not a refusal", bad, err)
		}
	}
	b, err := SGX{}.Parse([]byte(`{"meta":{"code":"200","processedTime":1791966066007},"data":{"prices":[]}}`), want)
	if err != nil || len(b.Rows) != 0 || len(b.Rejected) != 0 || !b.Stamp.IsZero() {
		t.Fatalf("empty board: %+v %v", b, err)
	}
}

// sgxOurTickers is every SGX ticker of ours, read from the files that list
// them (the market package cannot be imported here): the named quote feeds,
// listed.json's SGX rows and the screener's SG constituents.
func sgxOurTickers(t *testing.T) []string {
	t.Helper()
	set := map[string]bool{}
	src, err := os.ReadFile("../universe_quotefeed.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range regexp.MustCompile(`"([0-9A-Z]+\.SI)"`).FindAllStringSubmatch(string(src), -1) {
		set[m[1]] = true
	}
	var listed struct {
		Rows []struct{ Symbol, Venue string } `json:"rows"`
	}
	raw, err := os.ReadFile("../listed.json")
	if err != nil || json.Unmarshal(raw, &listed) != nil {
		t.Fatal("listed.json", err)
	}
	var screener []struct{ Market, Yahoo string }
	raw, err = os.ReadFile("../../screener/constituents.json")
	if err != nil || json.Unmarshal(raw, &screener) != nil {
		t.Fatal("constituents.json", err)
	}
	for _, r := range listed.Rows {
		if r.Venue == "SGX" {
			set[r.Symbol] = true
		}
	}
	for _, r := range screener {
		if r.Market == "SG" {
			set[r.Yahoo] = true
		}
	}
	var out []string
	for ticker := range set {
		out = append(out, ticker)
	}
	slices.Sort(out)
	return out
}

// TestSGXSymbolAndRequest: every SGX ticker of ours maps to its own code,
// and each code is on the board (31 of 31 in s7); anything else does not
// map. The request is one GET of the whole board, the same whatever is
// asked for, with Accept as its only header.
func TestSGXSymbolAndRequest(t *testing.T) {
	ours := sgxOurTickers(t)
	board := sgxReadBoard(t, fixture(t, "sgx_s7_session.json"))
	if len(ours) != 31 {
		t.Fatalf("%d SGX tickers of ours: %v", len(ours), ours)
	}
	codes := map[string]string{}
	for _, ticker := range ours {
		sym, ok := SGX{}.Symbol(ticker)
		switch {
		case !ok || sym != strings.TrimSuffix(ticker, ".SI"):
			t.Errorf("%s → %q %v", ticker, sym, ok)
		case codes[sym] != "":
			t.Errorf("%s and %s both map to %s", ticker, codes[sym], sym)
		case board.rows[sym] == nil:
			t.Errorf("%s is not on the board", sym)
		}
		codes[sym] = ticker
	}
	for _, bad := range []string{"", "D05", "D05.SG", "d05.si", "D05.SI.KS", "D05.SIX", ".SI", "AB.SI", "ABCDE.SI",
		"D-5.SI", "0700.HK", "005930.KS", "7203.T", "AAPL"} {
		if sym, ok := (SGX{}).Symbol(bad); ok {
			t.Errorf("%q maps to %q", bad, sym)
		}
	}
	const url = "https://api.sgx.com/securities/v1.1?excludetypes=bonds&params=nc,lt,trading_time,vl"
	for _, want := range []Want{{"D05": {"D05.SI", "SGD"}}, sgxWant(t, board)} {
		req, err := SGX{}.Request(context.Background(), "https://api.sgx.com", want)
		if err != nil {
			t.Fatal(err)
		}
		if req.Method != http.MethodGet || req.URL.String() != url || req.Body != nil {
			t.Fatalf("%s %s", req.Method, req.URL)
		}
		if len(req.Header) != 1 || req.Header.Get("Accept") != "application/json" {
			t.Fatalf("headers %v", req.Header)
		}
	}
	if _, err := (SGX{}).Request(context.Background(), "https://api.sgx.com", Want{}); err == nil {
		t.Fatal("an empty request was built")
	}
}

// TestSGXInfo: a near-real-time snapshot source for SGX's switch, polled
// every 30 s and never faster (the floor holds fill re-reads too): at most 2
// requests a minute, within the 30-60 s the snapshot warrants. SGX has a
// verified calendar, so no Hours; the settled read comes after the board
// stops at 17:16 SGT.
func TestSGXInfo(t *testing.T) {
	in := SGX{}.Info()
	switch {
	case in.Name != "sgx" || in.Venue != "SGX" || in.Switch != "sgx" || in.Gate != "" || in.Host != "https://api.sgx.com":
		t.Fatalf("identity %+v", in)
	case in.Class != ClassNear || in.Times != TimesSnapshot:
		t.Fatalf("class %q times %q", in.Class, in.Times)
	case in.Every != 30*time.Second || in.Floor != 30*time.Second || in.Floor < MinFloor:
		t.Fatalf("every %v floor %v", in.Every, in.Floor)
	case int(time.Minute/max(in.Every, in.Floor)) != 2:
		t.Fatalf("%d requests a minute", int(time.Minute/max(in.Every, in.Floor)))
	case in.Settle != 20*time.Minute || in.MaxLag != 5*time.Minute || in.Hours != nil:
		t.Fatalf("settle %v maxLag %v hours %v", in.Settle, in.MaxLag, in.Hours)
	}
	if !slices.Equal(in.Quiet, []Window{{"09:00", "09:04"}, {"13:00", "13:04"}}) {
		t.Fatalf("quiet %v", in.Quiet)
	}
	names := func(as []Adapter) (out []string) {
		for _, a := range as {
			out = append(out, a.Info().Name)
		}
		return out
	}
	if got := names(Enabled([]string{"sgx"}, true)); !slices.Equal(got, []string{"sgx"}) {
		t.Fatalf("sgx switch: %v", got)
	}
	if slices.Contains(names(Enabled([]string{"krx", "hkex", "hose", "twse"}, true)), "sgx") {
		t.Fatal("sgx enabled with its switch off")
	}
}

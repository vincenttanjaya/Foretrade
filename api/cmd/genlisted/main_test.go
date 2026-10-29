package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/screener"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo/yahootest"
)

const committed = "../../internal/market/listed.json"

func stock(ticker, mkt, ccy string) screener.Stock {
	idx := map[string]string{"JP": "Nikkei 225", "KR": "KOSPI 200", "PH": "PSEi", "TH": "SET50", "SG": "Straits Times Index"}[mkt]
	return screener.Stock{Ticker: ticker, Code: strings.SplitN(ticker, ".", 2)[0], Name: "LIST " + ticker, Market: mkt, Currency: ccy,
		Indices: []screener.Membership{{Index: idx}}}
}

func ok(ticker, mkt, ccy string, traded int64) Measurement {
	return Measurement{Ticker: ticker, Market: mkt, MeasuredAt: "2026-10-13T03:30:00Z", Status: "ok", InstrumentType: "EQUITY",
		Currency: ccy, FirstTradeDate: "2000-01-04", LongName: "Yahoo " + ticker, RegularMarketTime: "2026-10-13T03:29:00Z",
		DailyBars: 18, DailyBarsWithClose: 18, TradedValueUSD30d: traded}
}

// fixture: one constituent per path through the rule, and a duplicate.
func fixture() ([]screener.Stock, []market.AssetInfo, func(string) string, map[string]Measurement) {
	stocks := []screener.Stock{stock("AAA.T", "JP", "JPY"), stock("BBB.T", "JP", "JPY"), stock("CCC.PS", "PH", "PHP"),
		stock("DDD.T", "JP", "JPY"), stock("EEE.T", "JP", "JPY"), stock("FFF.KS", "KR", "KRW"), stock("GGG.KS", "KR", "KRW"),
		stock("HHH.BK", "TH", "THB"), stock("III.BK", "TH", "THB"), stock("JJJ.T", "JP", "JPY"), stock("KKK.SI", "SG", "USD"),
		stock("LLL.T", "JP", "JPY"), stock("JJJ.T", "JP", "JPY")}
	named := []market.AssetInfo{
		{Symbol: "AAA", Pair: "AAA.T", Class: market.Stock, Venue: "yahoo", Listing: &market.Listing{Yahoo: "AAA.T"}},
		{Symbol: "BBBY", Pair: "BBBY", Class: market.Stock, Venue: "yahoo", Listing: &market.Listing{Yahoo: "BBBY"}},
	}
	adr := func(t string) string { return map[string]string{"BBB.T": "BBBY", "JJJ.T": "NOTLISTED"}[t] }
	ms := map[string]Measurement{
		"CCC.PS": {Ticker: "CCC.PS", Market: "PH", MeasuredAt: "2026-10-13T03:40:55Z", Status: "not_found", HTTP: 404, Error: "Not Found"},
		"DDD.T":  ok("DDD.T", "JP", "JPY", 5e6),
		"EEE.T":  ok("EEE.T", "JP", "USD", 5e6),
		"FFF.KS": ok("FFF.KS", "KR", "KRW", 268_280),
		"GGG.KS": ok("GGG.KS", "KR", "KRW", 9e6),
		"HHH.BK": ok("HHH.BK", "TH", "THB", 9e6),
		"III.BK": ok("III.BK", "TH", "THB", 9e6),
		"JJJ.T":  ok("JJJ.T", "JP", "JPY", 359_950_261),
		"KKK.SI": ok("KKK.SI", "SG", "USD", 6_701_446),
	}
	d := ms["DDD.T"]
	d.InstrumentType = "ETF"
	ms["DDD.T"] = d
	g := ms["GGG.KS"]
	g.FirstTradeDate = "2025-11-25"
	ms["GGG.KS"] = g
	h := ms["HHH.BK"]
	h.RegularMarketTime = "2026-10-08T09:36:10Z"
	ms["HHH.BK"] = h
	i := ms["III.BK"]
	i.DailyBarsWithClose = 1
	ms["III.BK"] = i
	return stocks, named, adr, ms
}

func encoded(t *testing.T, f File) []byte {
	t.Helper()
	raw, err := f.encode()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestBuild(t *testing.T) {
	stocks, named, adr, ms := fixture()
	opts := defaultOptions()
	opts.on = "2026-10-13"
	f, rep, err := build(stocks, named, adr, ms, File{}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if rep.skipped["listing"] != 1 || rep.skipped["adr"] != 1 {
		t.Fatalf("skipped %v", rep.skipped)
	}
	if len(f.Rows) != 2 || f.Rows[0].Symbol != "JJJ.T" || f.Rows[1].Symbol != "KKK.SI" || f.MeasuredOn != "2026-10-13" {
		t.Fatalf("rows %+v", f.Rows)
	}
	j := f.Rows[0]
	if j.Name != "Yahoo JJJ.T" || j.ListName != "LIST JJJ.T" || j.Venue != market.TSE || j.Currency != "JPY" || j.Index != "Nikkei 225" ||
		j.Underlying != "TSE: JJJ (Nikkei 225), JPY→USD at Yahoo FX" || j.Measured.TradedValueUSD30d != 359_950_261 || j.Left != "" {
		t.Fatalf("JJJ.T %+v", j)
	}
	if k := f.Rows[1]; k.Underlying != "SGX: KKK (Straits Times Index), quoted in USD (no FX)" || k.Currency != "USD" || k.Venue != market.SGX {
		t.Fatalf("KKK.SI %+v", k)
	}
	want := map[string]string{
		"CCC.PS": "No keyless Yahoo data for this listing (Yahoo answered 404 on 2026-10-13)",
		"DDD.T":  "Yahoo serves it as ETF, not as an equity",
		"EEE.T":  "Yahoo quotes it in USD, not in the list's JPY",
		"FFF.KS": "Trades under $1M a day (30-day mean $268k on 2026-10-13)",
		"GGG.KS": "Listed under a year ago (first trade 2025-11-25)",
		"HHH.BK": "No fresh Yahoo quote (last trade 2026-10-08)",
		"III.BK": "Yahoo's daily history has gaps (1 daily bar in the month to 2026-10-13)",
		"LLL.T":  "Not measured on Yahoo yet",
	}
	if len(f.Excluded) != len(want) {
		t.Fatalf("excluded %+v", f.Excluded)
	}
	for _, x := range f.Excluded {
		if want[x.Ticker] != x.Reason || x.ListName != "LIST "+x.Ticker || x.Index == "" || (x.Measured == nil) != (x.Ticker == "LLL.T") {
			t.Errorf("%s: %q, want %q", x.Ticker, x.Reason, want[x.Ticker])
		}
	}
	// Deterministic and idempotent: a rebuild over its own output is the same file.
	again, _, _ := build(stocks, named, adr, f.measurements(), f, opts)
	if !bytes.Equal(encoded(t, again), encoded(t, f)) {
		t.Fatal("a rebuild over the output differs")
	}

	// Append-only: a joiner follows the earlier rows; a leaver stays with
	// index "" and the date it left, and keeps it on later builds.
	stocks2 := append(slices.DeleteFunc(slices.Clone(stocks), func(s screener.Stock) bool { return s.Ticker == "JJJ.T" }), stock("MMM.T", "JP", "JPY"))
	ms2 := f.measurements()
	ms2["MMM.T"] = ok("MMM.T", "JP", "JPY", 2e6)
	opts.on = "2026-11-02"
	g, rep2, err := build(stocks2, named, adr, ms2, f, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Rows) != 3 || g.Rows[0].Symbol != "JJJ.T" || g.Rows[1].Symbol != "KKK.SI" || g.Rows[2].Symbol != "MMM.T" || !slices.Equal(rep2.left, []string{"JJJ.T"}) {
		t.Fatalf("rows %+v, left %v", g.Rows, rep2.left)
	}
	if l := g.Rows[0]; l.Index != "" || l.Left != "2026-11-02" || l.Underlying != "TSE: JJJ, JPY→USD at Yahoo FX" || l.Measured != j.Measured {
		t.Fatalf("leaver %+v", l)
	}
	opts.on = "2026-12-01"
	h, _, _ := build(stocks2, named, adr, g.measurements(), g, opts)
	if !bytes.Equal(encoded(t, h), encoded(t, g)) {
		t.Fatal("a leaver's date moved on a later build")
	}
	// A listed row a new reading would exclude stays (positions may hold it).
	ms3 := g.measurements()
	thin := ms3["KKK.SI"]
	thin.TradedValueUSD30d = 10
	ms3["KKK.SI"] = thin
	k, rep3, _ := build(stocks2, named, adr, ms3, g, opts)
	if len(k.Rows) != 3 || k.Rows[1].Measured.TradedValueUSD30d != 10 || len(rep3.kept) != 1 || !strings.HasPrefix(rep3.kept[0], "KKK.SI: Trades under $1M") {
		t.Fatalf("kept %v %+v", rep3.kept, k.Rows)
	}
	// -drop removes a leaver; a current constituent cannot be dropped.
	opts.drop = []string{"JJJ.T"}
	d, _, err := build(stocks2, named, adr, g.measurements(), g, opts)
	if err != nil || len(d.Rows) != 2 || d.Rows[0].Symbol != "KKK.SI" {
		t.Fatalf("drop: %v %+v", err, d.Rows)
	}
	opts.drop = []string{"KKK.SI"}
	if _, _, err := build(stocks2, named, adr, g.measurements(), g, opts); err == nil || !strings.Contains(err.Error(), "still a constituent") {
		t.Fatalf("drop of a constituent: %v", err)
	}
	// A row a named instrument trades now leaves the file (the loader refuses a collision).
	named2 := append(slices.Clone(named), market.AssetInfo{Symbol: "MMMX", Pair: "MMM.T", Venue: "yahoo", Listing: &market.Listing{Yahoo: "MMM.T"}})
	opts.drop = nil
	n, rep4, _ := build(stocks2, named2, adr, g.measurements(), g, opts)
	if len(n.Rows) != 2 || !slices.Equal(rep4.named, []string{"MMM.T"}) {
		t.Fatalf("named now %+v %v", n.Rows, rep4.named)
	}
	var out bytes.Buffer
	rep4.print(&out, n)
	if !strings.Contains(out.String(), "2 rows") || !strings.Contains(out.String(), "named instrument trades it now") {
		t.Fatal(out.String())
	}
}

// A Taipei Exchange (.TWO) listing names TPEx in its reference line.
func TestUnderlyingTPEx(t *testing.T) {
	if got := underlying("TW", "6488.TWO", "", "TWD"); got != "TPEx: 6488, TWD→USD at Yahoo FX" {
		t.Fatal(got)
	}
}

func TestDollars(t *testing.T) {
	for v, want := range map[int64]string{1_000_000: "$1M", 250_000: "$250k", 127_308: "$127k", 1_500_000: "$1.5M", 999: "$999", 9_045_827: "$9M"} {
		if got := dollars(v); got != want {
			t.Errorf("%d: %s, want %s", v, got, want)
		}
	}
}

func measurementsIn(t *testing.T, path string) map[string]Measurement {
	t.Helper()
	mf, err := readMeasurements(path)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]Measurement{}
	for _, m := range mf.Measurements {
		out[m.Ticker] = m
	}
	return out
}

// The 2026-10-13 measurements give the 604 index rows of the design: exactly
// the committed listed.json.
func TestBuild20261013(t *testing.T) {
	ms := measurementsIn(t, "testdata/measure-2026-10-13.json")
	opts := defaultOptions()
	f, rep, err := build(screener.Stocks(), market.Universe, screener.ADRFor, ms, File{}, opts)
	if err != nil {
		t.Fatal(err)
	}
	byMarket := map[string]int{}
	for _, r := range f.Rows {
		byMarket[r.Market]++
	}
	want := map[string]int{"JP": 213, "KR": 145, "HK": 83, "TH": 44, "ID": 35, "TW": 26, "MY": 26, "VN": 23, "SG": 9}
	if len(f.Rows) != 604 || len(byMarket) != len(want) || rep.skipped["listing"] != 94 || rep.skipped["adr"] != 24 {
		t.Fatalf("%d rows %v, skipped %v", len(f.Rows), byMarket, rep.skipped)
	}
	for m, n := range want {
		if byMarket[m] != n {
			t.Errorf("%s: %d rows, want %d", m, byMarket[m], n)
		}
	}
	kinds := map[string]int{}
	for _, x := range f.Excluded {
		kinds[strings.SplitN(x.Reason, " (", 2)[0]]++
	}
	if len(f.Excluded) != 63 || kinds["No keyless Yahoo data for this listing"] != 29 || kinds["Trades under $1M a day"] != 27 ||
		kinds["Listed under a year ago"] != 6 || kinds["Yahoo's daily history has gaps"] != 1 {
		t.Fatalf("excluded %v", kinds)
	}
	for _, x := range f.Excluded {
		if x.Ticker == "BANPU.BK" && x.Reason != "Yahoo's daily history has gaps (1 daily bar in the month to 2026-10-13)" ||
			x.Ticker == "268280.KS" && x.Reason != "Trades under $1M a day (30-day mean $127k on 2026-10-13)" {
			t.Errorf("%s: %s", x.Ticker, x.Reason)
		}
	}
	if !slices.ContainsFunc(f.Rows, func(r Row) bool { return r.Symbol == "2445.KL" }) { // its freshest read was current
		t.Error("2445.KL is admitted on its spark read")
	}
	raw, err := os.ReadFile(committed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded(t, f), raw) {
		t.Fatal("listed.json is not the 2026-10-13 build: go run ./cmd/genlisted build -measure cmd/genlisted/testdata/measure-2026-10-13.json")
	}
	// The relaxed bar (owner question 2) admits 635: only two thin KOSPI rows and BANPU.BK stay out with the PSEi.
	opts.minTradedUSD, opts.minHistoryDays = 250_000, 0
	relaxed, _, _ := build(screener.Stocks(), market.Universe, screener.ADRFor, ms, File{}, opts)
	var out []string
	for _, x := range relaxed.Excluded {
		if !strings.HasSuffix(x.Ticker, ".PS") {
			out = append(out, x.Ticker)
		}
	}
	if len(relaxed.Rows) != 635 || !slices.Equal(out, []string{"002840.KS", "268280.KS", "BANPU.BK"}) {
		t.Fatalf("relaxed: %d rows, out %v", len(relaxed.Rows), out)
	}
}

// Rebuilding from listed.json's own measurements is listed.json byte for byte.
func TestListedUpToDate(t *testing.T) {
	prev, err := readListed(committed)
	if err != nil {
		t.Fatal(err)
	}
	f, _, err := build(screener.Stocks(), market.Universe, screener.ADRFor, prev.measurements(), prev, defaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(committed)
	if !bytes.Equal(encoded(t, f), raw) {
		t.Fatal("listed.json is out of date: go run ./cmd/genlisted build")
	}
}

func TestRun(t *testing.T) {
	ctx := context.Background()
	out := filepath.Join(t.TempDir(), "listed.json")
	var stdout bytes.Buffer
	if err := run(ctx, []string{"build", "-out", out, "-measure", "testdata/measure-2026-10-13.json"}, &stdout); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(out)
	want, _ := os.ReadFile(committed)
	if !bytes.Equal(got, want) || !strings.HasPrefix(stdout.String(), "604 rows") || !strings.Contains(stdout.String(), "research-only MER.PS (PH)") {
		t.Fatalf("build: %s", stdout.String())
	}
	for _, args := range [][]string{nil, {"bogus"}, {"measure"}, {"measure", "-out", out, "-tickers", "some"},
		{"build", "-out", out, "-drop", "6501.T"}, {"build", "-out", out, "-measure", "missing.json"}} {
		if err := run(ctx, args, &stdout); err == nil {
			t.Errorf("%v: no error", args)
		}
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	os.WriteFile(bad, []byte(`{"measurements":[{"ticker":"6501.T","status":"maybe"}]}`), 0o644)
	if _, err := readMeasurements(bad); err == nil {
		t.Error("a measurement with no measuredAt and an unknown status is refused")
	}
}

// measure reads each ticker once, through any Source, and keeps a 404 but
// not an outage.
func TestMeasure(t *testing.T) {
	f := yahootest.New()
	ch := yahootest.Daily("6501.T", "JPY", 4000, 4100, 4200)
	ch.Meta.LongName = " Hitachi, Ltd. "
	f.Set(ch)
	f.SetFX("JPY", 0.0066)
	f.SetErr("7011.T", yahootest.Down)
	stocks := []screener.Stock{stock("6501.T", "JP", "JPY"), stock("MER.PS", "PH", "PHP"), stock("7011.T", "JP", "JPY")}
	at := time.Date(2026, 10, 13, 3, 30, 51, 500, time.UTC)
	var log bytes.Buffer
	mf := measure(context.Background(), f, stocks, 0, func() time.Time { return at }, &log)
	if len(mf.Measurements) != 2 || !strings.Contains(log.String(), "7011.T") {
		t.Fatalf("%+v %s", mf.Measurements, log.String())
	}
	m := mf.Measurements[0]
	if m.Status != "ok" || m.MeasuredAt != "2026-10-13T03:30:51Z" || m.LongName != "Hitachi, Ltd." || m.Currency != "JPY" ||
		m.InstrumentType != "EQUITY" || m.DailyBarsWithClose != 3 || m.TradedValueUSD30d != 27060 || // mean(close × 1000) × 0.0066
		m.RegularMarketTime != yahootest.End.Format(time.RFC3339) || m.FirstTradeDate != "2026-10-01" {
		t.Fatalf("6501.T %+v", m)
	}
	if p := mf.Measurements[1]; p.Status != "not_found" || p.HTTP != 404 {
		t.Fatalf("MER.PS %+v", p)
	}
	if f.Calls("JPYUSD=X") != 1 {
		t.Fatal("one FX read per currency")
	}
	// A cancelled run stops before the next read.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if mf := measure(ctx, f, stocks, time.Millisecond, time.Now, &log); len(mf.Measurements) != 0 {
		t.Fatalf("after cancel %+v", mf.Measurements)
	}
	var _ yahoo.Source = f
}

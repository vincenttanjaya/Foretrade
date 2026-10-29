package rtquote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// ytwWant asks for every row of a Yahoo Taiwan fixture, as the market layer
// would, checking that each symbol is our ticker unchanged.
func ytwWant(t *testing.T, body []byte) Want {
	t.Helper()
	var rows []struct{ Symbol, Currency string }
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatal(err)
	}
	want := Want{}
	for _, r := range rows {
		sym, ok := YahooTW{}.Symbol(r.Symbol)
		if !ok || sym != r.Symbol {
			t.Fatalf("%s maps to %q, %v", r.Symbol, sym, ok)
		}
		want[sym] = Wanted{Ticker: r.Symbol, Currency: "TWD"}
	}
	return want
}

// TestYahooTWParseSession: the 52-symbol answer at 13:19:35 Taipei on
// 2026-10-14 (research tw_ytw_api_r9_all, trimmed), two seconds after MIS's
// (mis_session.json): every row is ours and real time, priced and dated by
// its own last trade, seconds old where MIS's was a minute or more, and the
// market is open.
func TestYahooTWParseSession(t *testing.T) {
	body := fixture(t, "yahootw_session.json")
	want := ytwWant(t, body)
	b, err := YahooTW{}.Parse(body, want)
	if err != nil {
		t.Fatal(err)
	}
	if len(want) != 52 || len(b.Rows) != 52 || len(b.Rejected) != 0 || b.Closed || !b.Stamp.IsZero() {
		t.Fatalf("want %d, rows %d, rejected %v, closed %v, stamp %v", len(want), len(b.Rows), b.Rejected, b.Closed, b.Stamp)
	}
	read := misUTC("2026-10-14T05:19:35Z") // the research read's end, to the second
	rows := byTicker(b)
	tsmc := rows["2330.TW"]
	if tsmc.Price != 2510 || !tsmc.TradeAt.Equal(misUTC("2026-10-14T05:19:30Z")) || tsmc.Currency != "TWD" || *tsmc.Volume != 14_193_000 {
		t.Fatalf("TSMC %+v", tsmc)
	}
	mis, err := MIS{}.Parse(fixture(t, "mis_session.json"), misWant(t, fixture(t, "mis_session.json")))
	if err != nil {
		t.Fatal(err)
	}
	ahead := 0
	for _, m := range mis.Rows {
		if !rows[m.Ticker].TradeAt.Before(m.TradeAt) {
			ahead++
		}
	}
	if ahead < 50 { // Yahoo Taiwan's trade is the newer (or the same) on nearly every symbol
		t.Fatalf("Yahoo Taiwan at least as new as MIS on %d of 52", ahead)
	}
	misRows := byTicker(mis)
	for _, c := range (YahooTW{}).Info().Canaries { // the canaries: seconds old here, a minute or more on MIS
		if lag := read.Sub(rows[c].TradeAt); lag > 15*time.Second || mis.Stamp.Sub(misRows[c].TradeAt) < time.Minute {
			t.Errorf("%s: Yahoo Taiwan %v behind, MIS %v", c, lag, mis.Stamp.Sub(misRows[c].TradeAt))
		}
	}
}

// TestYahooTWAfterClose: our 52 symbols read with the honest User-Agent at
// 20:38:58 Taipei on 2026-10-14: every row reads "close" (Closed), and the
// 46 rows Yahoo dates 14:30:00 (the after-hours fixed-price session, whose
// volume it adds) are rejected; the 6 without after-hours trades keep their
// 13:30 close.
func TestYahooTWAfterClose(t *testing.T) {
	body := fixture(t, "yahootw_after_close.json")
	b, err := YahooTW{}.Parse(body, ytwWant(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Rows) != 6 || len(b.Rejected) != 46 || !b.Closed {
		t.Fatalf("rows %d rejected %d closed %v", len(b.Rows), len(b.Rejected), b.Closed)
	}
	for _, r := range b.Rows {
		if !r.TradeAt.Equal(misUTC("2026-10-14T05:30:00Z")) {
			t.Errorf("%s at %v", r.Ticker, r.TradeAt)
		}
	}
	if r := byTicker(b)["2059.TW"]; r.Price != 10005 || *r.Volume != 580_000 { // MIS: 580 lots, fv 0
		t.Fatalf("King Slide %+v", r)
	}
	for _, r := range b.Rejected {
		if r.Reason != "after-hours trade at 14:30:00 Taipei, past the regular session" || r.Ticker == "" {
			t.Fatalf("%+v", r)
		}
	}
	// its closing prices are MIS's, read a minute earlier, on all 52
	var rows []struct {
		Symbol string
		Price  struct{ Raw string }
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatal(err)
	}
	mis, err := MIS{}.Parse(fixture(t, "mis_after_close.json"), misWant(t, fixture(t, "mis_after_close.json")))
	if err != nil {
		t.Fatal(err)
	}
	closes := byTicker(mis)
	for _, r := range rows {
		if p, _ := twDecimal(r.Price.Raw); closes[r.Symbol].Price != p {
			t.Errorf("%s: Yahoo Taiwan %s, MIS %v", r.Symbol, r.Price.Raw, closes[r.Symbol].Price)
		}
	}
}

// TestYahooTWDelayed: the same API's own delayed quotes (its HK rows, read
// in the research at 05:02:19 UTC, hk_ytw_r1: exchangeDataDelayedBy 15, the
// 03:59 lunch-break trades) are rejected row by row, never served as real
// time; a missing delay is rejected too.
func TestYahooTWDelayed(t *testing.T) {
	body := fixture(t, "yahootw_hk_delayed.json")
	want := Want{"0700.HK": {"0700.HK", "HKD"}, "9988.HK": {"9988.HK", "HKD"}, "0005.HK": {"0005.HK", "HKD"}}
	b, err := YahooTW{}.Parse(body, want)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Rows) != 0 || len(b.Rejected) != 3 || b.Closed {
		t.Fatalf("%+v", b)
	}
	for _, r := range b.Rejected {
		if r.Reason != "not real time (exchangeDataDelayedBy 15)" {
			t.Fatalf("%+v", r)
		}
	}
	for _, sym := range []string{"0700.HK", "9988.HK", "0005.HK"} {
		if _, ok := (YahooTW{}).Symbol(sym); ok {
			t.Fatalf("%s maps to the TWSE source", sym)
		}
	}
	edited := ytwEdit(t, map[string]func(map[string]any){"2330.TW": func(r map[string]any) { delete(r, "exchangeDataDelayedBy") }})
	if b, _ := (YahooTW{}).Parse(edited, Want{"2330.TW": {"2330.TW", "TWD"}}); len(b.Rows) != 0 ||
		len(b.Rejected) != 1 || b.Rejected[0].Reason != "not real time (exchangeDataDelayedBy missing)" {
		t.Fatalf("%+v", b)
	}
}

// ytwEdit is yahootw_session.json's TSMC row once per symbol of edits, each
// changed by its edit.
func ytwEdit(t *testing.T, edits map[string]func(map[string]any)) []byte {
	t.Helper()
	var rows []map[string]any
	if err := json.Unmarshal(fixture(t, "yahootw_session.json"), &rows); err != nil {
		t.Fatal(err)
	}
	base, _ := json.Marshal(rows[0])
	var out []any
	for sym, edit := range edits {
		var r map[string]any
		json.Unmarshal(base, &r)
		r["symbol"] = sym
		edit(r)
		out = append(out, r)
	}
	body, _ := json.Marshal(out)
	return body
}

// TestYahooTWRowEdges: per-row reasons, from the real TSMC row changed one
// field at a time; the rest of the answer stands.
func TestYahooTWRowEdges(t *testing.T) {
	body := ytwEdit(t, map[string]func(map[string]any){
		"2330.TW":  func(map[string]any) {},
		"2317.TW":  func(r map[string]any) { delete(r, "price") },
		"2454.TW":  func(r map[string]any) { r["price"] = map[string]any{"raw": "2,510"} },
		"2308.TW":  func(r map[string]any) { r["regularMarketTime"] = "13:19:30" },
		"2881.TW":  func(r map[string]any) { r["price"] = map[string]any{"raw": "2740"} }, // over limit-up 2735
		"2882.TW":  func(r map[string]any) { r["volume"] = "14,193,000" },
		"2891.TW":  func(r map[string]any) { r["price"] = map[string]any{"raw": 2510} }, // a number, not a string: bad parse
		"3008.TW":  func(r map[string]any) { r["currency"] = "USD" },                    // kept: the market layer rejects it
		"3231.TW":  func(r map[string]any) { delete(r, "volume"); delete(r, "limitUpPrice") },
		"9999.TW":  func(map[string]any) {},
		"6488.TWO": func(r map[string]any) { r["exchange"] = "TWO" },
	})
	want := Want{}
	for _, s := range []string{"2330.TW", "2317.TW", "2454.TW", "2308.TW", "2881.TW", "2882.TW", "2891.TW", "3008.TW", "3231.TW", "6488.TWO"} {
		want[s] = Wanted{Ticker: s, Currency: "TWD"}
	}
	b, err := YahooTW{}.Parse(body, want)
	if err != nil {
		t.Fatal(err)
	}
	rows := byTicker(b)
	if len(rows) != 4 || rows["2330.TW"].Price != 2510 || rows["3008.TW"].Currency != "USD" || rows["3231.TW"].Volume != nil ||
		rows["6488.TWO"].Price != 2510 || b.Closed {
		t.Fatalf("rows %+v closed %v", b.Rows, b.Closed)
	}
	reasons := map[string]string{}
	for _, r := range b.Rejected {
		reasons[r.Symbol] = r.Reason
	}
	for sym, frag := range map[string]string{"2317.TW": "price: missing", "2454.TW": "not a number", "2308.TW": "not a time",
		"2881.TW": "outside the day's limits 2245-2735", "2882.TW": "whole number of shares", "2891.TW": "bad parse",
		"9999.TW": "not asked for"} {
		if !strings.Contains(reasons[sym], frag) {
			t.Errorf("%s: %q, want %q", sym, reasons[sym], frag)
		}
	}
	if len(b.Rejected) != 7 {
		t.Fatalf("rejected %v", b.Rejected)
	}
	for _, bad := range []string{"", "null", "{}", `{"error":{"code":"Not Found"}}`, "<html><title>Yahoo</title></html>"} {
		if _, err := (YahooTW{}).Parse([]byte(bad), want); err == nil || errors.Is(err, ErrRefused) {
			t.Errorf("%q: %v, want a failure", bad, err)
		}
	}
	if b, err := (YahooTW{}).Parse([]byte("[]"), want); err != nil || len(b.Rows) != 0 || b.Closed {
		t.Fatalf("an empty list: %+v %v", b, err) // no usable row: the market layer counts a failure
	}
}

// TestYahooTWSymbolAndRequest: our tickers are its symbols (the canary
// evidence read, yahootw_watch3.json, holds the screener's 2303, 2412 and
// 3711 beside 2327 and 2454); one GET carries them all, %2C-separated, with
// no fields and no header but Accept.
func TestYahooTWSymbolAndRequest(t *testing.T) {
	ours := ytwWant(t, fixture(t, "yahootw_after_close.json"))
	for s, w := range ytwWant(t, fixture(t, "yahootw_watch3.json")) {
		ours[s] = w
	}
	if len(ours) != 55 {
		t.Fatalf("%d of our tickers", len(ours))
	}
	for in, want := range map[string]string{"2330.TW": "2330.TW", "6488.TWO": "6488.TWO", "00878.TW": "00878.TW",
		"2330": "", "2330.tw": "", "0700.HK": "", "2330.TW.KS": "", "AAPL": ""} {
		if got, ok := (YahooTW{}).Symbol(in); got != want || ok != (want != "") {
			t.Errorf("%s → %q %v", in, got, ok)
		}
	}
	req, err := YahooTW{}.Request(context.Background(), "https://tw.stock.yahoo.com",
		Want{"2330.TW": {"2330.TW", "TWD"}, "2317.TW": {"2317.TW", "TWD"}})
	if err != nil {
		t.Fatal(err)
	}
	if req.Method != "GET" || req.URL.String() !=
		"https://tw.stock.yahoo.com/_td-stock/api/resource/StockServices.stockList;symbols=2317.TW%2C2330.TW" {
		t.Fatalf("%s %s", req.Method, req.URL)
	}
	if len(req.Header) != 1 || req.Header.Get("Accept") != "application/json" {
		t.Fatalf("headers %v", req.Header)
	}
	if req, err := (YahooTW{}).Request(context.Background(), "https://tw.stock.yahoo.com", ours); err != nil ||
		strings.Count(req.URL.String(), "%2C") != 54 {
		t.Fatalf("all 55: %v", err)
	}
	if _, err := (YahooTW{}).Request(context.Background(), "https://x", Want{}); err == nil {
		t.Fatal("an empty request was built")
	}
}

// TestTWSECanaries: the canaries are the day's top three of our 55 TWSE
// tickers by traded value in Yahoo Taiwan's answers after the close
// (turnoverM, millions of TWD).
func TestTWSECanaries(t *testing.T) {
	var rows []struct {
		Symbol    string
		TurnoverM string
	}
	for _, name := range []string{"yahootw_after_close.json", "yahootw_watch3.json"} {
		var part []struct {
			Symbol    string
			TurnoverM string
		}
		if err := json.Unmarshal(fixture(t, name), &part); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, part...)
	}
	value := map[string]float64{}
	for _, r := range rows {
		v, err := twDecimal(r.TurnoverM)
		if err != nil {
			t.Fatal(r.Symbol, err)
		}
		value[r.Symbol] = v
	}
	if len(value) != 55 {
		t.Fatalf("%d tickers", len(value))
	}
	canaries := MIS{}.Info().Canaries
	least := value[canaries[0]]
	for _, c := range canaries {
		least = min(least, value[c])
	}
	for s, v := range value {
		in := s == canaries[0] || s == canaries[1] || s == canaries[2]
		if !in && v >= least {
			t.Errorf("%s traded %v million, more than the canary at %v", s, v, least)
		}
	}
	if y := (YahooTW{}).Info().Canaries; strings.Join(y, ",") != strings.Join(canaries, ",") {
		t.Fatalf("Yahoo Taiwan's canaries %v", y)
	}
}

// TestYahooTWChain: Yahoo Taiwan is polled as MIS is (interval, floor,
// windows, canaries, settled read), is gated by QUOTES_REALTIME_TWSE_YAHOO and
// comes after MIS in TWSE's chain, so it is asked only while MIS is refused
// or degraded.
func TestYahooTWChain(t *testing.T) {
	m, y := MIS{}.Info(), YahooTW{}.Info()
	if y.Name != "yahoo-tw" || y.Gate != GateTWSEYahoo || y.Host != "https://tw.stock.yahoo.com" {
		t.Fatalf("%+v", y)
	}
	y.Name, y.Gate, y.Host = m.Name, m.Gate, m.Host
	if fmt.Sprint(y) != fmt.Sprint(m) {
		t.Fatalf("Yahoo Taiwan %+v, MIS %+v", y, m)
	}
	mis, ytw := -1, -1
	for i, a := range All {
		switch a.Info().Name {
		case "mis":
			mis = i
		case "yahoo-tw":
			ytw = i
		}
	}
	if mis < 0 || ytw <= mis {
		t.Fatalf("chain: mis %d, yahoo-tw %d", mis, ytw)
	}
	names := func(as []Adapter) (out []string) {
		for _, a := range as {
			out = append(out, a.Info().Name)
		}
		return out
	}
	if got := names(Enabled([]string{"twse"}, false)); strings.Join(got, ",") != "mis" {
		t.Fatalf("gate off: %v", got)
	}
	if got := names(Enabled([]string{"twse"}, true)); strings.Join(got, ",") != "mis,yahoo-tw" {
		t.Fatalf("gate on: %v", got)
	}
}

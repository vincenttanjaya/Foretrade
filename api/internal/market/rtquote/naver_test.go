package rtquote

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// naverWant asks for every code in a fixture, as the market layer would.
func naverWant(t *testing.T, body []byte) Want {
	t.Helper()
	var doc struct {
		Datas []struct {
			ItemCode string `json:"itemCode"`
		} `json:"datas"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	want := Want{}
	for _, d := range doc.Datas {
		sym, ok := Naver{}.Symbol(d.ItemCode + ".KS")
		if !ok || sym != d.ItemCode {
			t.Fatalf("%s.KS maps to %q, %v", d.ItemCode, sym, ok)
		}
		want[sym] = Wanted{Ticker: d.ItemCode + ".KS", Currency: "KRW"}
	}
	return want
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func byTicker(b Batch) map[string]Row {
	out := map[string]Row{}
	for _, r := range b.Rows {
		out[r.Ticker] = r
	}
	return out
}

// TestNaverParseSession: the 200-code read at 13:03:50 KST on 2026-10-14
// (research b3_naver_pollnew_200, trimmed): every row is ours and parses,
// at the KRX price and volume (never the Nextrade fields), stamped by
// Naver's server clock, and the market is open.
func TestNaverParseSession(t *testing.T) {
	body := fixture(t, "naver_b3_session.json")
	want := naverWant(t, body)
	b, err := Naver{}.Parse(body, want)
	if err != nil {
		t.Fatal(err)
	}
	if len(want) != 200 || len(b.Rows) != 200 || len(b.Rejected) != 0 || b.Closed {
		t.Fatalf("want %d, rows %d, rejected %v, closed %v", len(want), len(b.Rows), b.Rejected, b.Closed)
	}
	rows := byTicker(b)
	sam := rows["005930.KS"]
	if sam.Price != 238000 || sam.Currency != "KRW" || sam.Volume == nil || *sam.Volume != 8_717_916 || !sam.TradeAt.IsZero() {
		t.Fatalf("Samsung %+v", sam)
	}
	if b.Stamp.Format(time.RFC3339Nano) != "2026-10-14T04:03:50.294621Z" { // the first row's localTradedAt, in UTC
		t.Fatalf("stamp %v", b.Stamp)
	}
}

// TestNaverBurst: two reads 7.8 s apart in the research's burst: the KRX
// volume rose (the market layer dates the price by it) and the stamps are
// Naver's clock.
func TestNaverBurst(t *testing.T) {
	var got [2]Batch
	for i, name := range []string{"naver_s00.json", "naver_s01.json"} {
		body := fixture(t, name)
		b, err := Naver{}.Parse(body, naverWant(t, body))
		if err != nil || len(b.Rows) != 5 {
			t.Fatalf("%s: %v %v", name, b, err)
		}
		got[i] = b
	}
	a, b := byTicker(got[0])["005930.KS"], byTicker(got[1])["005930.KS"]
	if *a.Volume != 8_722_653 || *b.Volume != 8_723_092 || a.Price != 238000 || b.Price != 238500 {
		t.Fatalf("Samsung %v/%v, %v/%v", *a.Volume, a.Price, *b.Volume, b.Price)
	}
	if d := got[1].Stamp.Sub(got[0].Stamp); d < 7*time.Second || d > 9*time.Second {
		t.Fatalf("stamps %v → %v", got[0].Stamp, got[1].Stamp)
	}
	if got[0].Stamp.Format(time.RFC3339Nano) != "2026-10-14T04:04:31.319791Z" {
		t.Fatalf("stamp %v", got[0].Stamp)
	}
}

// TestNaverAfterClose: our 171 codes read at 19:45 KST on 2026-10-14 with
// the honest User-Agent: every row reads OPEN but afterMarket, so the answer
// is Closed; the price is KRX's close (233,000), not Nextrade's 233,500.
func TestNaverAfterClose(t *testing.T) {
	body := fixture(t, "naver_after_close.json")
	b, err := Naver{}.Parse(body, naverWant(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Rows) != 171 || !b.Closed {
		t.Fatalf("rows %d closed %v", len(b.Rows), b.Closed)
	}
	if r := byTicker(b)["005930.KS"]; r.Price != 233000 || *r.Volume != 17_241_960 {
		t.Fatalf("Samsung %+v", r)
	}
}

// TestNaverRowEdges: per-row reasons, from a real row changed one field at
// a time; the formatted fields stand in for missing raw ones.
func TestNaverRowEdges(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal(fixture(t, "naver_s00.json"), &doc); err != nil {
		t.Fatal(err)
	}
	base := doc["datas"].([]any)[0].(map[string]any) // 005930
	clone := func(code string, edit func(map[string]any)) map[string]any {
		raw, _ := json.Marshal(base)
		var r map[string]any
		json.Unmarshal(raw, &r)
		r["itemCode"] = code
		edit(r)
		return r
	}
	rows := []any{
		clone("005930", func(r map[string]any) { delete(r, "closePriceRaw"); delete(r, "accumulatedTradingVolumeRaw") }), // formatted twins
		clone("000660", func(r map[string]any) { delete(r, "closePriceRaw"); delete(r, "closePrice") }),
		clone("402340", func(r map[string]any) { r["stockExchangeType"].(map[string]any)["delayTime"] = 20 }),
		clone("042700", func(r map[string]any) { r["tradeStopType"].(map[string]any)["name"] = "HALT" }),
		clone("034020", func(r map[string]any) { r["stockExchangeType"].(map[string]any)["code"] = "KQ" }),
		clone("999999", func(map[string]any) {}),
		clone("009150", func(r map[string]any) { r["closePriceRaw"] = "12.5" }),
		clone("000270", func(r map[string]any) {
			delete(r, "accumulatedTradingVolumeRaw")
			delete(r, "accumulatedTradingVolume")
		}),
	}
	body, _ := json.Marshal(map[string]any{"datas": rows})
	want := Want{}
	for _, c := range []string{"005930", "000660", "402340", "042700", "034020", "009150", "000270"} {
		want[c] = Wanted{Ticker: c + ".KS", Currency: "KRW"}
	}
	b, err := Naver{}.Parse(body, want)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Rows) != 1 || b.Rows[0].Ticker != "005930.KS" || b.Rows[0].Price != 238000 || *b.Rows[0].Volume != 8_722_653 {
		t.Fatalf("rows %+v", b.Rows)
	}
	reasons := map[string]string{}
	for _, r := range b.Rejected {
		reasons[r.Symbol] = r.Reason
	}
	for sym, frag := range map[string]string{"000660": "price: missing", "402340": "delayTime", "042700": "trading stopped",
		"034020": "KOSPI", "999999": "not asked for", "009150": "whole number", "000270": "volume: missing"} {
		if !strings.Contains(reasons[sym], frag) {
			t.Errorf("%s: %q, want %q", sym, reasons[sym], frag)
		}
	}
	for _, bad := range []string{"", "<html>blocked</html>", `{"pollingInterval":7000}`} {
		if _, err := (Naver{}).Parse([]byte(bad), want); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestNaverSymbolAndRequest(t *testing.T) {
	for in, want := range map[string]string{"005930.KS": "005930", "0126Z0.KS": "0126Z0", "005930.KQ": "", "5930.KS": "",
		"005930": "", "7203.T": ""} {
		if got, ok := (Naver{}).Symbol(in); got != want || ok != (want != "") {
			t.Errorf("%s → %q %v", in, got, ok)
		}
	}
	req, err := Naver{}.Request(context.Background(), "https://polling.finance.naver.com",
		Want{"005930": {"005930.KS", "KRW"}, "000660": {"000660.KS", "KRW"}})
	if err != nil {
		t.Fatal(err)
	}
	if req.Method != "GET" || req.URL.String() != "https://polling.finance.naver.com/api/realtime/domestic/stock/000660,005930" {
		t.Fatalf("%s %s", req.Method, req.URL)
	}
	if len(req.Header) != 1 || req.Header.Get("Accept") != "application/json" {
		t.Fatalf("headers %v", req.Header)
	}
	if _, err := (Naver{}).Request(context.Background(), "https://x", Want{}); err == nil {
		t.Fatal("an empty request was built")
	}
}

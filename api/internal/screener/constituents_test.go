package screener

import (
	"strings"
	"testing"
)

// The embedded lists: ten indices, 785 listings, every row sourced and dated,
// and the 30 PSEi rows Yahoo does not serve kept, not dropped.
func TestEmbeddedConstituents(t *testing.T) {
	if len(stocks) != 785 {
		t.Fatalf("%d stocks", len(stocks))
	}
	want := map[string]int{"Nikkei 225": 225, "KOSPI 200": 200, "FTSE TWSE Taiwan 50": 50, "Hang Seng Index": 95,
		"Straits Times Index": 30, "IDX LQ45": 45, "SET50": 50, "FTSE Bursa Malaysia KLCI": 30, "PSEi": 30, "VN30": 30}
	if len(lists) != len(want) {
		t.Fatalf("lists %+v", lists)
	}
	for _, l := range lists {
		if want[l.Index] != l.Count || len(l.Sources) == 0 || len(l.AsOf) != len("2026-10-06") || l.SourceKind == "" {
			t.Fatalf("list %+v", l)
		}
	}
	if l := lists[2]; l.Index != "FTSE TWSE Taiwan 50" || l.SourceKind != "mixed" || len(l.Sources) != 2 {
		t.Fatalf("Taiwan 50 adds 6446 from a secondary report: %+v", l)
	}
	perMarket := map[string]int{}
	seen := map[string]bool{}
	for _, s := range stocks {
		perMarket[s.Market]++
		if seen[s.Ticker] || len(s.Indices) == 0 || s.Name == "" || s.Code == "" || s.Currency == "" {
			t.Fatalf("stock %+v", s)
		}
		seen[s.Ticker] = true
		if s.Resolves == (s.Market == "PH") || s.Market == "PH" && !strings.HasSuffix(s.Ticker, ".PS") {
			t.Fatalf("%s: resolves %v", s.Ticker, s.Resolves)
		}
		for _, m := range s.Indices {
			if m.Source == "" || m.AsOf == "" || m.SourceKind != "official" && m.SourceKind != "secondary" {
				t.Fatalf("%s membership %+v", s.Ticker, m)
			}
		}
	}
	if perMarket["JP"] != 225 || perMarket["PH"] != 30 || perMarket["HK"] != 95 || len(perMarket) != len(Markets) {
		t.Fatalf("per market %v", perMarket)
	}
	for _, m := range Markets {
		if zones[m.Code] == nil {
			t.Fatalf("no time zone for %s", m.Code)
		}
	}
	if s := stocks[0]; s.Ticker != "1332.T" || s.Market != "JP" || s.Indices[0].Index != "Nikkei 225" {
		t.Fatalf("first %+v", s)
	}
	// Stocks hands out a copy.
	cp := Stocks()
	cp[0].Ticker = "X"
	if stocks[0].Ticker != "1332.T" {
		t.Fatal("Stocks shares its backing array")
	}
}

// A ticker in two indices is one stock with both memberships; a repeated
// membership counts once; a list with official and secondary rows is mixed.
func TestLoadMergesByTicker(t *testing.T) {
	raw := `[
	 {"index":"A","market":"KR","yahoo":"005930.ks","code":"005930","name":"Samsung","currency":"KRW","source":"https://a","sourceKind":"official","asOf":"2026-09-01","yahooResolves":true},
	 {"index":"B","market":"KR","yahoo":"005930.KS","code":"005930","name":"Samsung Elec","currency":"KRW","source":"https://b","sourceKind":"secondary","asOf":"2026-09-02","yahooResolves":false},
	 {"index":"B","market":"KR","yahoo":"005930.KS","code":"005930","name":"Samsung Elec","currency":"KRW","source":"https://b","sourceKind":"secondary","asOf":"2026-09-02","yahooResolves":false},
	 {"index":"A","market":"KR","yahoo":"000660.KS","code":"000660","name":"SK hynix","currency":"KRW","source":"https://a2","sourceKind":"secondary","asOf":"2026-09-03","yahooResolves":true}
	]`
	s, l, err := load([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(s) != 2 || s[0].Ticker != "005930.KS" || s[0].Name != "Samsung" || len(s[0].Indices) != 2 || !s[0].Resolves ||
		s[0].Indices[1] != (Membership{Index: "B", Source: "https://b", SourceKind: "secondary", AsOf: "2026-09-02"}) {
		t.Fatalf("stocks %+v", s)
	}
	if len(l) != 2 || l[0].Count != 2 || l[0].SourceKind != "mixed" || l[0].AsOf != "2026-09-03" || len(l[0].Sources) != 2 ||
		l[1].Count != 1 || l[1].SourceKind != "secondary" {
		t.Fatalf("lists %+v", l)
	}
	for _, bad := range []string{
		`[{"index":"A","market":"US","yahoo":"AAPL","code":"AAPL","name":"Apple","source":"s","sourceKind":"official","asOf":"2026-09-01"}]`,
		`[{"index":"A","market":"JP","yahoo":"bad ticker","code":"1","name":"X","source":"s","sourceKind":"official","asOf":"2026-09-01"}]`,
		`[{"index":"A","market":"JP","yahoo":"7203.T","code":"7203","name":"Toyota","source":"","sourceKind":"official","asOf":"2026-09-01"}]`,
		`[{"index":"A","market":"JP","yahoo":"7203.T","code":"7203","name":"Toyota","source":"s","sourceKind":"blog","asOf":"2026-09-01"}]`,
		`{}`,
	} {
		if _, _, err := load([]byte(bad)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}

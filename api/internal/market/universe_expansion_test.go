package market

import (
	"reflect"
	"strings"
	"testing"
)

func TestExpandedCatalogContract(t *testing.T) {
	if len(Universe) != 107+len(QuoteFeeds) {
		t.Fatalf("configured universe = %d, want 69 crypto + 9 US + 10 Asian + 12 US + 7 Asian perps + the quote feeds", len(Universe))
	}
	if len(Core) != 12 || Core[0].Symbol != "BTC" || Core[11].Symbol != "PAXG" {
		t.Fatal("original Core changed")
	}
	seen := map[string]bool{}
	for i, a := range Universe {
		if seen[a.Symbol] {
			t.Fatalf("duplicate bare symbol %s", a.Symbol)
		}
		seen[a.Symbol] = true
		v := reflect.ValueOf(a)
		class, venue := v.FieldByName("Class"), v.FieldByName("Venue")
		if !class.IsValid() || !venue.IsValid() || class.Kind() != reflect.String || venue.Kind() != reflect.String {
			t.Fatalf("AssetInfo must expose string Class and Venue fields: %+v", a)
		}
		if i < 69 && (class.String() != "crypto" || venue.String() != "binance") {
			t.Fatalf("crypto routing: %+v", a)
		}
		if i >= 69 && i < 107 && (!strings.HasPrefix(a.Pair, "xyz:") || venue.String() != "hyperliquid") {
			t.Fatalf("HIP3 routing: %+v", a)
		}
		if i >= 107 && (strings.HasPrefix(a.Pair, "xyz:") || venue.String() != "yahoo") {
			t.Fatalf("quote-feed routing: %+v", a)
		}
	}
	want := strings.Fields("ZEC QNT SAND MUBARAK STRK AXS TRUMP ZRO ACE VIRTUAL ORCA RUNE RAY NIL MOVR PHA DASH JST ETHFI NMR WLFI JTO POL CAKE JUP AR SAGA CRV PENDLE NVDA TSLA AAPL MSFT AMZN GOOGL META SP500 XYZ100 TSM BABA SMSN SKHX HYUNDAI SOFTBANK MINIMAX ZHIPU EWJ EWY PLTR COIN HOOD MSTR INTC AMD ORCL NFLX CRCL MU SNDK CRWV SKHY CXMT UNITREE GIGADEV KIOXIA EWT KSTR")
	for i, symbol := range want {
		if Universe[40+i].Symbol != symbol {
			t.Fatalf("appended catalog %d=%s, want %s", i, Universe[40+i].Symbol, symbol)
		}
	}
}

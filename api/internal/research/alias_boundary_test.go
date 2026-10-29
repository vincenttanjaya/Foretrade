package research

import (
	"slices"
	"strings"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/market"
)

func TestAmbiguousUniverseAliasesRequireLocalIdentity(t *testing.T) {
	for _, text := range []string{
		"The Fed just cut rates", "Just a rate cut lifts investors",
		"Investors dash for safety", "Dash for safety as bond yields rise",
		"The political saga continues", "Saga of bank failures continues",
		"The quant traders increase bond exposure", "Quant Traders Cut Equity Exposure",
		"Bitcoin rallies as investors just digest the Fed's decision",
		"BTC traders dash for safety during a political saga; quant funds sell crypto",
		"Bitcoin token demand rises. Just a political Saga; Dash for safety, Quant traders say",
		"Bitcoin rises while just token prices fall", "Crypto traders just buy coins",
	} {
		t.Run(text, func(t *testing.T) {
			for _, got := range [][]string{tagSymbols(text), QuestionSymbols(text)} {
				for _, sym := range []string{"JST", "DASH", "SAGA", "QNT"} {
					if slices.Contains(got, sym) {
						t.Errorf("ordinary words tagged %s in %q: %v", sym, text, got)
					}
				}
			}
		})
	}
	for _, tc := range []struct{ symbol, name string }{{"JST", "JUST"}, {"DASH", "Dash"}, {"SAGA", "Saga"}, {"QNT", "Quant"}} {
		for _, text := range []string{"$" + tc.symbol, "$" + strings.ToLower(tc.symbol), tc.symbol + "/USDT", strings.ToLower(tc.symbol) + "usdt", tc.name + " token rises", tc.name + " cryptocurrency adoption grows", tc.name + " blockchain launches"} {
			t.Run(text, func(t *testing.T) {
				if got := tagSymbols(text); !slices.Contains(got, tc.symbol) {
					t.Errorf("explicit identity lost in news %q: %v", text, got)
				}
				if got := QuestionSymbols(text); !slices.Contains(got, tc.symbol) {
					t.Errorf("explicit identity lost in question %q: %v", text, got)
				}
			})
		}
	}
	for _, text := range []string{"Quant Network expands", "Saga protocol launches", "JUST coin rises", "Dash (DASH) token rises"} {
		if len(tagSymbols(text)) != 1 || len(QuestionSymbols(text)) != 1 {
			t.Errorf("qualified proper name lost: %q", text)
		}
	}
}

func TestUniverseAliasPreservation(t *testing.T) {
	for _, a := range market.CryptoUniverse {
		t.Run(a.Symbol, func(t *testing.T) {
			if got := QuestionSymbols(strings.ToLower(a.Symbol) + "/usdt"); !slices.Equal(got, []string{a.Symbol}) {
				t.Errorf("crypto pair identity: %v", got)
			}
			if got := tagSymbols(tagNames[a.Symbol][0] + " token adoption grows"); !slices.Contains(got, a.Symbol) {
				t.Errorf("qualified crypto name: %v", got)
			}
		})
	}
	for _, text := range []string{"Apple earnings lift shares", "Nvidia company revenue rises", "Meta Platforms shares rise"} {
		if len(tagSymbols(text)) != 1 || len(QuestionSymbols(text)) != 0 {
			t.Errorf("company/forecast boundary: %q tags=%v questions=%v", text, tagSymbols(text), QuestionSymbols(text))
		}
	}
}

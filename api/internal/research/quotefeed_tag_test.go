package research

import (
	"slices"
	"testing"
)

// Quote-feed stocks are tagged by company name or cashtag in a financial
// headline, never by a bare short ticker (SE, ASX, HDB and KB are words or
// other institutions there).
func TestQuoteFeedTags(t *testing.T) {
	for text, want := range map[string]string{
		"Tencent shares jump after earnings beat":          "TENCENT",
		"Delta Electronics stock hits a record":            "DELTA",
		"DBS Group profit rises as investors cheer":        "DBS",
		"$SE stock climbs":                                 "SE",
		"Solana Company shares slide as SOL treasury dips": "HSDT",
	} {
		if got := tagSymbols(text); !slices.Contains(got, want) {
			t.Errorf("%q tagged %v, want %s", text, got, want)
		}
	}
	for _, text := range []string{
		"ASX shares close lower as Australian miners slip",
		"SE Asia stocks rally on rate-cut bets",
		"HDB resale flat prices rise; developers' shares gain",
		"Company memo: KB limits on uploads",
	} {
		for _, sym := range []string{"ASX", "SE", "HDB", "KB"} {
			if got := tagSymbols(text); slices.Contains(got, sym) {
				t.Errorf("%q tagged %s: %v", text, sym, got)
			}
		}
	}
}

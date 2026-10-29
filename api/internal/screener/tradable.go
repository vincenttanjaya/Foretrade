package screener

import (
	"strings"

	"github.com/vincenttanjaya/Foretrade/api/internal/market"
)

// adrs maps a local listing's Yahoo ticker to the Yahoo ticker of a US
// listing of the same company (an ADR or ADS, or for YUMC the same shares on
// NYSE), for "via ADR" trading when only that one is in the universe. The
// ratios live in each instrument's Listing.Underlying.
var adrs = map[string]string{
	"2330.TW": "TSM", "2303.TW": "UMC", "3711.TW": "ASX",
	"9988.HK": "BABA", "9618.HK": "JD", "9888.HK": "BIDU", "9866.HK": "NIO", "2015.HK": "LI", "9868.HK": "XPEV",
	"9999.HK": "NTES", "9961.HK": "TCOM", "9626.HK": "BILI", "1698.HK": "TME", "2423.HK": "BEKE", "2057.HK": "ZTO",
	"9987.HK": "YUMC",
	"6758.T":  "SONY", "7203.T": "TM", "8306.T": "MUFG", "7267.T": "HMC", "8316.T": "SMFG",
	"105560.KS": "KB", "055550.KS": "SHG", "005490.KS": "PKX",
	"2412.TW": "CHT", "015760.KS": "KEP", "017670.KS": "SKM", "030200.KS": "KT", "034220.KS": "LPL",
	"TLKM.JK": "TLK", "TEL.PS": "PHI",
}

// ADRFor is the Yahoo ticker of the US listing the ADR map pairs with a local
// listing's Yahoo ticker ("2330.TW" → "TSM"), or "" when none. cmd/genlisted
// leaves a ticker whose ADR is an instrument out of the listed rows.
func ADRFor(ticker string) string { return adrs[strings.ToUpper(strings.TrimSpace(ticker))] }

// trade is how a screened ticker trades here, if it does.
type trade struct {
	Symbol string // the universe symbol
	Via    string // "listing": an instrument references the ticker itself; "adr": its US listing
	Note   string // the instrument's Listing.Underlying, e.g. "NYSE: TSM ADS = 5 TWSE 2330 shares"
}

// tradables indexes every instrument (the named universe as it is now, then
// the listed ones) by Listing.Yahoo, so instruments other packages add
// (perps, quote feeds, listed rows) are found without a list here.
func tradables() func(ticker string) *trade {
	by := map[string]market.AssetInfo{}
	for _, a := range market.Instruments() {
		if a.Listing == nil || a.Listing.Yahoo == "" {
			continue
		}
		if k := strings.ToUpper(a.Listing.Yahoo); by[k].Symbol == "" {
			by[k] = a
		}
	}
	return func(ticker string) *trade {
		if a, ok := by[ticker]; ok {
			return &trade{Symbol: a.Symbol, Via: "listing", Note: a.Listing.Underlying}
		}
		if a, ok := by[adrs[ticker]]; ok && adrs[ticker] != "" {
			return &trade{Symbol: a.Symbol, Via: "adr", Note: a.Listing.Underlying}
		}
		return nil
	}
}

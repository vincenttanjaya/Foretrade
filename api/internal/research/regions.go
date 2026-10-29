package research

import (
	"regexp"
	"slices"

	"github.com/vincenttanjaya/Foretrade/api/internal/market"
)

// The regions a stored headline touches (NewsItem.Regions, GET /api/news?region=).
// One story can touch several; none means global or unclassified.
const (
	RegionCrypto        = "crypto"
	RegionUS            = "us"
	RegionEastAsia      = "east_asia"
	RegionSoutheastAsia = "southeast_asia"
)

// Regions lists every region, in display order.
var Regions = []string{RegionCrypto, RegionUS, RegionEastAsia, RegionSoutheastAsia}

// marketCues are the words in a title that name a region's markets whatever
// search found it: its indices, exchanges and central banks and "<country>
// stocks|shares|markets" (crypto: the crypto industry's own words). They also
// put a regional market headline on the wire (aboutMarkets). Acronyms count
// only in upper case ("BOJ", "MAS"), the rest in any case; the acronyms that
// are also US tickers or common abbreviations count only before a move or
// policy word (acronymMove).
// ponytail: word lists, like marketWords; a model labelling pass is the upgrade.
var marketCues = map[string]*regexp.Regexp{
	RegionCrypto: regexp.MustCompile(`(?i)\b(crypto\w*|stablecoins?|blockchain|defi|altcoins?|memecoins?|web3|tokeni[sz]\w*|binance|coinbase|tether)\b`),
	RegionUS: regexp.MustCompile(`(?i:\b(wall street|s&p 500|nasdaq|dow jones|nyse|russell 2000|federal reserve|magnificent seven)\b)|` +
		`\b(Dow|FOMC)\b|\b(US|U\.S\.) (stocks?|shares|equities|stock markets?|markets)\b`),
	RegionEastAsia: regexp.MustCompile(`(?i:\b(nikkei|topix|tokyo stock exchange|bank of japan|kospi|kosdaq|korea exchange|bank of korea|taiex|` +
		`taiwan stock exchange|hang seng|hong kong stock exchange|shanghai composite|shanghai stock exchange|shenzhen (component|stock exchange)|` +
		`csi 300|chinext|people.s bank of china|(japan(ese)?|tokyo|(south )?korean?|seoul|taiwan(ese)?|hong kong|china|chinese|mainland|shanghai|shenzhen) ` +
		`(stocks?|shares|equities|stock markets?|markets|bourses?|indexes|indices))\b)|\b(BOJ|BoJ|PBOC|PBoC|HKEX|TWSE|KRX)\b|` +
		`\b(BOK|BoK) ` + acronymMove),
	RegionSoutheastAsia: regexp.MustCompile(`(?i:\b(straits times index|singapore exchange|monetary authority of singapore|jakarta composite|` +
		`indonesia stock exchange|bank indonesia|stock exchange of thailand|set index|bank of thailand|bursa malaysia|fbm klci|bank negara|` +
		`philippine stock exchange|bangko sentral|vn-?index|ho chi minh (city )?stock exchange|state bank of vietnam|` +
		`(singapore|indonesian?|jakarta|thai|thailand|malaysian?|philippines?|vietnam(ese)?|southeast asian?|asean) ` +
		`(stocks?|shares|equities|stock markets?|markets|bourses?|indexes|indices))\b)|\b(IHSG|KLCI|PSEi|PSEI|SGX)\b|` +
		`\b(STI|MAS|JCI|IDX|BSP|HOSE) ` + acronymMove),
}

// acronymMove is the move or policy word an ambiguous acronym needs next
// ("STI edges up", "MAS leaves policy unchanged", "BOK holds"): "STI cases
// rise" (a health story), "Masco (MAS) earnings", "BOK Financial" and "BSP
// chief" name no Asian market.
const acronymMove = `(?i:rises?|falls?|gains?|slips?|slides?|edges?|climbs?|dips?|drops?|ends?|closes?|opens?|jumps?|surges?|` +
	`soars?|sinks?|tumbles?|plunges?|rebounds?|extends?|hits?|nears?|tops?|holds?|keeps?|leaves?|cuts?|hikes?|raises?|lowers?|` +
	`eases?|tightens?|maintains?|flat|higher|lower|up|down|steady|mixed|policy|meeting|governor|index|composite)\b`

// regionPlaces are the place, currency and "Asia" words that confirm an Asian
// search's result is about its region, in its title or description ("Stocks
// edge up" with "Singapore's benchmark …" below it).
var regionPlaces = map[string]*regexp.Regexp{
	RegionEastAsia: regexp.MustCompile(`(?i)\b(japan\w*|tokyo|osaka|yen|korea\w*|seoul|taiwan\w*|taipei|hong kong|china|chinese|beijing|` +
		`shanghai|shenzhen|yuan|renminbi|asia\w*)\b`),
	RegionSoutheastAsia: regexp.MustCompile(`(?i)\b(singapore\w*|indonesia\w*|jakarta|rupiah|thai\w*|bangkok|baht|malaysia\w*|kuala lumpur|` +
		`ringgit|philippine\w*|filipino|manila|vietnam\w*|hanoi|ho chi minh|asean|asia\w*)\b`),
}

// stockNames are the universe's stocks by unambiguous company name, so a
// headline about one is on the wire without a financial word ("TSMC lifts its
// outlook"). Apple, Amazon, Google and Meta are ordinary words: they count
// through the tagging rules, which want company context.
var stockNames = regexp.MustCompile(`(?i)\b(nvidia|tesla|microsoft|alphabet|tsmc|taiwan semiconductor|samsung electronics|sk hynix|` +
	`hyundai motor|softbank|alibaba|minimax|zhipu)\b`)

// regionalMarket reports whether a title names a region's market (marketCues)
// or a universe stock (stockNames).
func regionalMarket(title string) bool {
	return stockNames.MatchString(title) || slices.ContainsFunc(Regions, func(r string) bool { return marketCues[r].MatchString(title) })
}

// assetRegion is a universe asset's region: crypto, US for a US company, the
// exchange's region for an Asian local listing, and for an Asian company or
// fund listed in the US (an ADR, a US share or an ETF) its home market's
// (usListedAsia). "" is an Asian listing of neither region (the Indian ADRs).
// ponytail: a new US-listed Asian row needs an entry in usListedAsia, and
// TestAssetRegions fails until it has one.
func assetRegion(a market.AssetInfo) string {
	switch {
	case !a.Perp():
		return RegionCrypto
	case a.Listing == nil || a.Listing.Region == "us":
		return RegionUS
	}
	if r, ok := venueRegions[a.Listing.SessionVenue]; ok {
		return r
	}
	return usListedAsia[a.Symbol]
}

// venueRegions are the Asian exchanges' regions.
var venueRegions = map[string]string{
	market.KRX: RegionEastAsia, market.TSE: RegionEastAsia, market.HKEX: RegionEastAsia, market.SSE: RegionEastAsia, market.TWSE: RegionEastAsia,
	market.SGX: RegionSoutheastAsia, market.IDX: RegionSoutheastAsia, market.SET: RegionSoutheastAsia, market.BURSA: RegionSoutheastAsia,
	market.HOSE: RegionSoutheastAsia,
}

// usListedAsia are the US-listed Asian rows by their company's (or, for an
// ETF, its exposure's) home market: Taiwan, China, Hong Kong, Japan and Korea
// are East Asian; Singapore, Indonesia and the Philippines Southeast Asian;
// India is neither ("").
var usListedAsia = map[string]string{
	// Perps on Hyperliquid.
	"TSM": RegionEastAsia, "BABA": RegionEastAsia, "EWJ": RegionEastAsia, "EWY": RegionEastAsia, "SKHY": RegionEastAsia,
	"EWT": RegionEastAsia, "KSTR": RegionEastAsia,
	// Quote feeds.
	"SE": RegionSoutheastAsia, "GRAB": RegionSoutheastAsia, "PHI": RegionSoutheastAsia, "TLK": RegionSoutheastAsia,
	"KARO": RegionSoutheastAsia, "KLIC": RegionSoutheastAsia,
	"PDD": RegionEastAsia, "JD": RegionEastAsia, "BIDU": RegionEastAsia, "NIO": RegionEastAsia, "LI": RegionEastAsia,
	"XPEV": RegionEastAsia, "NTES": RegionEastAsia, "TCOM": RegionEastAsia, "BILI": RegionEastAsia, "FUTU": RegionEastAsia,
	"TME": RegionEastAsia, "BEKE": RegionEastAsia, "ZTO": RegionEastAsia, "YUMC": RegionEastAsia, "VIPS": RegionEastAsia,
	"SONY": RegionEastAsia, "TM": RegionEastAsia, "MUFG": RegionEastAsia, "HMC": RegionEastAsia, "SMFG": RegionEastAsia,
	"CPNG": RegionEastAsia, "KB": RegionEastAsia, "SHG": RegionEastAsia, "PKX": RegionEastAsia, "KEP": RegionEastAsia,
	"SKM": RegionEastAsia, "KT": RegionEastAsia, "LPL": RegionEastAsia,
	"UMC": RegionEastAsia, "ASX": RegionEastAsia, "CHT": RegionEastAsia, "HIMX": RegionEastAsia, "SIMO": RegionEastAsia,
	"INFY": "", "HDB": "", "IBN": "",
}

// contentRegions are the regions a headline names whatever search found it:
// each tagged asset's (crypto, or a stock's listing) and each region whose
// marketCues its title matches. The title alone counts for the cues: a
// description mentions other markets in passing ("Asian shares fell").
func contentRegions(title string, tags []string) []string {
	out := []string{}
	for _, sym := range tags {
		if a, ok := market.Lookup(sym); ok && assetRegion(a) != "" {
			out = append(out, assetRegion(a))
		}
	}
	for _, r := range Regions {
		if marketCues[r].MatchString(title) {
			out = append(out, r)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// wireRegions labels a result of search ws (text is its title and
// description, tags its symbols) with its contentRegions plus the search's own
// region, sorted, and reports false for a result ingestion skips:
//   - a crypto asset search's results are crypto, whatever else they name;
//   - a US macro search's untagged result must be US- or crypto-relevant
//     (usMacro), so another country's economy is skipped as before; a US stock
//     search's results are US;
//   - an Asian search's result gets the search's region when it names one of
//     its places or markets, and is skipped when it names no region at all
//     (another country's market: "European shares climb").
func wireRegions(ws wireSearch, title, text string, tags []string) ([]string, bool) {
	out := contentRegions(title, tags)
	switch {
	case ws.region == RegionUS && ws.topic == "macro" && len(tags) == 0 && !usMacro(text):
		return nil, false
	case ws.region == RegionCrypto || ws.region == RegionUS ||
		regionPlaces[ws.region].MatchString(text) || marketCues[ws.region].MatchString(text):
		out = append(out, ws.region)
	case len(out) == 0:
		return nil, false
	}
	slices.Sort(out)
	return slices.Compact(out), true
}

// asian reports whether region is East or Southeast Asia.
func asian(region string) bool { return region == RegionEastAsia || region == RegionSoutheastAsia }

// otherEconomy is an untagged macro row about another country's economy:
// neither US- nor crypto-relevant (usMacro) nor labelled East or Southeast
// Asian by a regional search. Ingestion's US macro searches skip such
// results; reads and stored-output checks hide rows stored before that rule.
func otherEconomy(n NewsItem) bool {
	return n.Topic == "macro" && len(n.Symbols) == 0 && !usMacro(n.Title+" "+n.Description) && !slices.ContainsFunc(n.Regions, asian)
}

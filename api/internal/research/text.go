package research

import (
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/vincenttanjaya/Foretrade/api/internal/market"
)

// tagNames are the names that tag a headline with a universe asset.
var tagNames = map[string][]string{
	"BTC": {"Bitcoin"}, "ETH": {"Ethereum"}, "SOL": {"Solana"}, "BNB": {"BNB"}, "XRP": {"XRP"},
	"DOGE": {"Dogecoin"}, "ADA": {"Cardano"}, "AVAX": {"Avalanche"}, "LINK": {"Chainlink"},
	"SUI": {"Sui"}, "TON": {"Toncoin", "Telegram Open Network"}, "PAXG": {"gold"},
	"TRX": {"TRON"}, "DOT": {"Polkadot"}, "LTC": {"Litecoin"}, "BCH": {"Bitcoin Cash"}, "NEAR": {"NEAR Protocol"},
	"APT": {"Aptos"}, "ARB": {"Arbitrum"}, "OP": {"Optimism"}, "UNI": {"Uniswap"}, "AAVE": {"Aave"},
	"ATOM": {"Cosmos"}, "FIL": {"Filecoin"}, "ICP": {"Internet Computer"}, "ETC": {"Ethereum Classic"},
	"XLM": {"Stellar"}, "HBAR": {"Hedera"}, "INJ": {"Injective"}, "SEI": {"Sei"}, "TIA": {"Celestia"},
	"WLD": {"Worldcoin"}, "RENDER": {"Render"}, "FET": {"Fetch.ai", "ASI Alliance"}, "ENA": {"Ethena"},
	"TAO": {"Bittensor"}, "ONDO": {"Ondo"}, "ALGO": {"Algorand"}, "LDO": {"Lido"}, "STX": {"Stacks"},
	"ZEC": {"Zcash"}, "QNT": {"Quant"}, "SAND": {"The Sandbox"}, "MUBARAK": {"Mubarak"}, "STRK": {"Starknet"}, "AXS": {"Axie Infinity"}, "TRUMP": {"Official Trump"}, "ZRO": {"LayerZero"}, "ACE": {"Fusionist"}, "VIRTUAL": {"Virtuals Protocol"}, "ORCA": {"Orca"}, "RUNE": {"THORChain"}, "RAY": {"Raydium"}, "NIL": {"Nillion"}, "MOVR": {"Moonriver"}, "PHA": {"Phala Network"}, "DASH": {"Dash"}, "JST": {"JUST"}, "ETHFI": {"ether.fi"}, "NMR": {"Numeraire"}, "WLFI": {"World Liberty Financial"}, "JTO": {"Jito"}, "POL": {"Polygon"}, "CAKE": {"PancakeSwap"}, "JUP": {"Jupiter"}, "AR": {"Arweave"}, "SAGA": {"Saga"}, "CRV": {"Curve DAO"}, "PENDLE": {"Pendle"},
}

// exactCaseNames are names that are also ordinary words ("an avalanche of
// sell orders", "sui generis", "market intel", a price "oracle"), so only the
// capitalized proper noun counts.
// ponytail: a capitalized word still tags ("Avalanche" opening a sentence, a
// Title Case "Stellar Quarter"); good enough for headlines.
var exactCaseNames = map[string]bool{"Avalanche": true, "Sui": true, "Optimism": true, "Cosmos": true, "Stellar": true,
	"Render": true, "Stacks": true, "Sei": true, "Ondo": true, "Intel": true, "Oracle": true}

// ExactCaseName reports whether an asset name is also an ordinary word, so
// only its capitalized form names the asset ("Stellar", not "stellar results").
func ExactCaseName(name string) bool { return exactCaseNames[name] }

// wordTickers are tickers that are English words; they count only via the
// name or a $TICKER cashtag, never as a bare word.
// DOT (US Department of Transportation) and APT (state hacking groups) are
// acronyms in macro and security headlines.
var wordTickers = map[string]bool{"LINK": true, "SUI": true, "TON": true, "NEAR": true, "OP": true, "UNI": true,
	"ATOM": true, "ETC": true, "TAO": true, "DOT": true, "APT": true, "ACE": true, "AR": true, "TRUMP": true, "SAND": true, "DASH": true, "CAKE": true, "SAGA": true, "VIRTUAL": true, "NIL": true}

// These added names occur in ordinary financial headlines even in title case.
// Require a local token identity, rather than crypto context elsewhere in the
// sentence. In particular, lower-case "just token prices" remains an adverb.
var qualifiedCryptoNames = map[string]*regexp.Regexp{
	"JUST":  regexp.MustCompile(`(?:^|[^\pL\pN])JUST\s+(?i:token|coin|cryptocurrency|blockchain|network|protocol)(?:$|[^\pL\pN])`),
	"Dash":  regexp.MustCompile(`(?:^|[^\pL\pN])(?:Dash|DASH)\s+(?i:token|coin|cryptocurrency|blockchain|network|protocol)(?:$|[^\pL\pN])`),
	"Saga":  regexp.MustCompile(`(?:^|[^\pL\pN])(?:Saga|SAGA)\s+(?i:token|coin|cryptocurrency|blockchain|network|protocol)(?:$|[^\pL\pN])`),
	"Quant": regexp.MustCompile(`(?:^|[^\pL\pN])(?:Quant|QUANT)\s+(?i:token|coin|cryptocurrency|blockchain|network|protocol)(?:$|[^\pL\pN])`),
}

// tagSymbols returns every universe asset the text mentions, sorted (never
// nil): by name as a whole word, as $TICKER, or as an upper-case whole-word
// ticker unless that ticker is an English word. Text that names no asset gets
// no tag, whatever search returned it.
// ponytail: "gold" tags PAXG, so "digital gold" (a bitcoin cliché) does too.
func tagSymbols(text string) []string {
	out := []string{}
	lower := strings.ToLower(text)
	for _, a := range market.Universe {
		if a.Perp() {
			if companyMention(text, a) {
				out = append(out, a.Symbol)
			}
			continue
		}
		hit := containsWord(text, "$"+a.Symbol) || !wordTickers[a.Symbol] && containsWord(text, a.Symbol)
		for _, name := range tagNames[a.Symbol] {
			if qualified, ok := qualifiedCryptoNames[name]; ok {
				symbol := strings.ToLower(a.Symbol)
				hit = hit || qualified.MatchString(text) || containsWord(text, name+" ("+a.Symbol+")") || containsWord(lower, "$"+symbol) ||
					containsWord(lower, symbol+"/usdt") || containsWord(lower, symbol+"usdt")
				continue
			}
			if name == "Official Trump" && !financialCryptoContext(lower) {
				continue
			}
			if exactCaseNames[name] {
				hit = hit || containsWord(text, name)
			} else {
				hit = hit || containsWord(lower, strings.ToLower(name))
			}
		}
		if hit && !slices.Contains(out, a.Symbol) {
			out = append(out, a.Symbol)
		}
	}
	slices.Sort(out)
	return out
}

// Mentions reports whether text names the universe asset symbol, by the
// rules that tag stored news (name, $TICKER, or an upper-case ticker).
func Mentions(text, symbol string) bool { return slices.Contains(tagSymbols(text), symbol) }

// lowerWords are tickers that are not words in upper case but are in lower
// case ("render", "algo trading", "an arb"), so only the upper-case ticker
// names them in a question. "sei" and "ondo" are not English words, so they
// count in any case here, though headlines need the capitalized name.
var lowerWords = map[string]bool{"RENDER": true, "ALGO": true, "ARB": true}

// QuestionSymbols lists the universe assets a forecast question, its
// criteria or a user's idea names, sorted (never nil): the tagging rules,
// plus what people type in any case: the Binance pair for every asset
// ("LINK/USDT", "linkusdt"), $ticker ("$sol", "$link"), and a ticker that is
// a word in neither case ("eth", "doge"; never "link", "near", "render").
func QuestionSymbols(text string) []string {
	out := slices.DeleteFunc(tagSymbols(text), func(symbol string) bool { _, ok := market.LookupCrypto(symbol); return !ok })
	lower := strings.ToLower(text)
	for _, a := range market.CryptoUniverse {
		s := strings.ToLower(a.Symbol)
		hit := containsWord(lower, s+"/usdt") || containsWord(lower, s+"usdt") || containsWord(lower, "$"+s) ||
			!wordTickers[a.Symbol] && !lowerWords[a.Symbol] && containsWord(lower, s)
		if hit && !slices.Contains(out, a.Symbol) {
			out = append(out, a.Symbol)
		}
	}
	slices.Sort(out)
	return out
}

// companyNames are the company names that tag a stock or index perp (with a
// financial word nearby, companyMention). Oracle, Circle and Strategy are also
// ordinary words (a crypto "oracle", "inner circle", a "trading strategy"), so
// they need the company form.
var companyNames = map[string][]string{"NVDA": {"nvidia"}, "TSLA": {"tesla"}, "AAPL": {"apple"}, "MSFT": {"microsoft"}, "AMZN": {"amazon"}, "GOOGL": {"alphabet", "google"}, "META": {"meta", "meta platforms"}, "SP500": {"s&p 500"}, "XYZ100": {"xyz100"},
	"TSM": {"tsmc", "taiwan semiconductor"}, "BABA": {"alibaba"}, "SMSN": {"samsung electronics"}, "SKHX": {"sk hynix"}, "HYUNDAI": {"hyundai motor"},
	"SOFTBANK": {"softbank"}, "MINIMAX": {"minimax"}, "ZHIPU": {"zhipu"}, "EWJ": {"ishares msci japan"}, "EWY": {"ishares msci south korea"},
	"PLTR": {"palantir"}, "COIN": {"coinbase"}, "HOOD": {"robinhood"}, "MSTR": {"microstrategy", "strategy inc"}, "INTC": {"intel"},
	"AMD": {"advanced micro devices"}, "ORCL": {"oracle corp", "oracle corporation", "oracle shares", "oracle stock"}, "NFLX": {"netflix"},
	"CRCL": {"circle internet", "circle shares", "circle stock"}, "MU": {"micron"}, "SNDK": {"sandisk"}, "CRWV": {"coreweave"},
	"SKHY": {"sk hynix"}, "CXMT": {"changxin memory"}, "UNITREE": {"unitree"}, "GIGADEV": {"gigadevice"}, "KIOXIA": {"kioxia"},
	"EWT": {"ishares msci taiwan"}, "KSTR": {"kraneshares star 50", "star 50 etf"}}

// financialWords make a company name a company (companyMention).
var financialWords = []string{"stock", "stocks", "share", "shares", "earnings", "revenue", "profit", "investor", "investors", "nasdaq", "nyse", "perp", "perps", "dividend", "company", "corporation", "inc", "ceo", "platforms"}

// Company headlines are separate from Binance forecast question aliases.
func companyMention(text string, a market.AssetInfo) bool {
	lower := strings.ToLower(text)
	if containsWord(lower, "$"+strings.ToLower(a.Symbol)) {
		return true
	}
	named := containsWord(text, a.Symbol)
	if a.QuoteFeed() { // many of these tickers are short words (SE, LI, KB, ASX, HDB): only the company name counts
		named = containsWord(lower, strings.ToLower(feedName(a)))
	}
	for _, name := range companyNames[a.Symbol] {
		named = named || containsWord(lower, name)
	}
	if !named {
		return false
	}
	for _, word := range financialWords {
		if containsWord(lower, word) {
			return true
		}
	}
	return false
}

// feedName is a quote-feed stock's company name ("Sea Limited", not "… (ADR)").
func feedName(a market.AssetInfo) string {
	name, _, _ := strings.Cut(a.Name, " (")
	return name
}

// questionWords are what a forecast question about a listed price says
// besides the financial words: "closing price of TSM ADR", "market cap".
var questionWords = []string{"price", "prices", "close", "closes", "closing", "trade", "trades", "trading", "market cap", "market capitalization",
	"market capitalisation", "valuation", "adr", "ads", "worth"}

// listedOn is "NYSE: TSM" (any exchange prefix): the ticker is group 1.
var listedOn = regexp.MustCompile(`(?i)\b(?:NYSE(?: Arca)?|NASDAQ|AMEX|TWSE|TPEX|TSE|TYO|HKEX|HKG|SGX|KRX|KOSPI|KOSDAQ|SSE|SZSE|IDX|NSE|BSE|PSE)\s*:\s*([A-Za-z0-9.^-]+)`)

// QuestionStocks lists the universe stocks and indices (perps and quote
// feeds) a forecast question or idea names, sorted (never nil): as $TSM,
// (TSM) or NYSE: TSM; as the upper-case ticker word (perps only: many
// quote-feed tickers are short words); or by company name (companyNames, a
// quote feed's own name) next to a financial or price word ("the closing
// price of Taiwan Semiconductor"). Crypto is QuestionSymbols'.
func QuestionStocks(text string) []string {
	out := []string{}
	lower := strings.ToLower(text)
	listed := map[string]bool{}
	for _, m := range listedOn.FindAllStringSubmatch(text, -1) {
		listed[strings.ToUpper(m[1])] = true
	}
	context := slices.ContainsFunc(append(slices.Clone(financialWords), questionWords...), func(w string) bool { return containsWord(lower, w) }) ||
		strings.Contains(text, "$")
	for _, a := range market.Universe {
		if !a.Perp() {
			continue
		}
		sym := a.Symbol
		hit := containsWord(lower, "$"+strings.ToLower(sym)) || strings.Contains(text, "("+sym+")") || listed[sym] ||
			a.Listing != nil && a.Listing.Yahoo != "" && listed[strings.ToUpper(a.Listing.Yahoo)] ||
			!a.QuoteFeed() && containsWord(text, sym)
		if !hit && context {
			names := companyNames[sym]
			if a.QuoteFeed() {
				names = []string{strings.ToLower(feedName(a))}
			}
			hit = slices.ContainsFunc(names, func(n string) bool { return containsWord(lower, n) })
		}
		if hit && !slices.Contains(out, sym) {
			out = append(out, sym)
		}
	}
	slices.Sort(out)
	return out
}
func financialCryptoContext(lower string) bool {
	for _, w := range []string{"token", "coin", "crypto", "memecoin", "cryptocurrency"} {
		if containsWord(lower, w) {
			return true
		}
	}
	return false
}

// normTitle is a headline's dedupe key: lower case, a trailing " - Outlet",
// " | Outlet" or " – Outlet" of at most 40 characters dropped, and every run
// of other characters than letters and digits turned into one space, so a
// syndicated copy ("… - 24/7 Wall St." and the same story on AOL) matches.
func normTitle(t string) string {
	t = strings.ToLower(t)
	cut, rest := -1, ""
	for _, sep := range []string{" - ", " | ", " – "} {
		if i := strings.LastIndex(t, sep); i > cut {
			cut, rest = i, t[i+len(sep):]
		}
	}
	if cut > 0 && utf8.RuneCountInString(rest) <= 40 {
		t = t[:cut]
	}
	return strings.Join(strings.FieldsFunc(t, func(r rune) bool { return !isWordRune(r) }), " ")
}

// firstByTitle keeps the first item per normTitle, in order.
func firstByTitle[T any](items []T, title func(T) string) []T {
	seen := map[string]bool{}
	return slices.DeleteFunc(items, func(it T) bool {
		k := normTitle(title(it))
		dup := seen[k]
		seen[k] = true
		return dup
	})
}

// containsWord reports whether w occurs in s with no letter or digit
// directly before or after it.
func containsWord(s, w string) bool {
	for i := 0; ; {
		j := strings.Index(s[i:], w)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(w)
		before, _ := utf8.DecodeLastRuneInString(s[:start])
		after, _ := utf8.DecodeRuneInString(s[end:])
		if !isWordRune(before) && !isWordRune(after) {
			return true
		}
		i = start + 1
	}
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

var (
	mdImage = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	mdLink  = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
)

// cleanText turns crawler markdown or fetched page text into readable prose:
// images go, links keep their text, lines that are mostly links (menus,
// tag clouds, "related" lists) or carry no letters/digits are dropped, so
// are runs of 4+ short lines (menus flattened to text), whitespace and
// repeated lines collapse, and the result is cut to maxChars.
// ponytail: line heuristics, not a readability parser (a short-item list or a
// stat table goes with the menus); swap in one if pages read badly.
func cleanText(s string, maxChars int) string {
	norm := strings.Split(s, "\n")
	for i, line := range norm {
		line = mdImage.ReplaceAllString(line, "")
		hasLinks := mdLink.MatchString(line)
		outside := mdLink.ReplaceAllString(line, "")
		line = strings.Join(strings.Fields(mdLink.ReplaceAllString(line, "$1")), " ")
		if hasLinks && letters(outside) < 20 || !strings.ContainsFunc(line, isWordRune) {
			line = ""
		}
		norm[i] = line
	}
	for i := 0; i < len(norm); {
		j := i
		for j < len(norm) && norm[j] != "" && len(strings.Fields(norm[j])) <= 3 {
			j++
		}
		if j-i >= 4 {
			clear(norm[i:j])
		}
		i = max(j, i+1)
	}
	var lines []string
	seen := map[string]bool{}
	blank := false
	for _, line := range norm {
		if line == "" {
			blank = len(lines) > 0
			continue
		}
		if seen[line] { // a repeated ticker or marquee keeps only its first copy
			continue
		}
		seen[line] = true
		if blank {
			lines = append(lines, "")
			blank = false
		}
		lines = append(lines, line)
	}
	return truncate(strings.Join(lines, "\n"), maxChars)
}

func letters(s string) int {
	n := 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			n++
		}
	}
	return n
}

// truncate cuts s to at most max runes, marking a cut with "…".
func truncate(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	if max < 1 {
		return ""
	}
	r := []rune(s)
	return strings.TrimRightFunc(string(r[:max-1]), unicode.IsSpace) + "…"
}

var untrustedTag = regexp.MustCompile(`(?i)<\s*/?\s*untrusted_web_content\s*>`)

// Untrusted wraps fetched web text for a model prompt. Wrapper tags inside
// the text are removed, repeatedly so nested pieces cannot reassemble one,
// and the text cannot close the block early.
func Untrusted(text string) string {
	for prev := ""; prev != text; {
		prev, text = text, untrustedTag.ReplaceAllString(text, "")
	}
	return "<untrusted_web_content>\n" + text + "\n</untrusted_web_content>"
}

package research

import (
	"cmp"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// spam matches casino, adult, coupon and SEO-farm wording that leaks into
// crypto searches ("Bitcoin casinos - Poshakvilla", "Top Bitcoin betting sites
// ranked"), on a title, a description or a source name. Gambling-industry
// stories are excluded from this crypto/macro wire by the selected quality policy.
var spam = regexp.MustCompile(`(?i)\b(sports betting|online gambling|bookmakers?|parlays?|premier league|sports fixtures?|betting apps?|casinos?|free spins|bonus codes?|no-deposit bonus|sportsbooks?|sweepstakes|online pokies|` +
	`slot (machines|games|sites)|betting (sites|tips|stats|picks)|gambling (sites?|apps?|platforms?|bonus\w*)|` +
	`(coupon|promo|discount|voucher|referral) codes?|escorts?|porn\w*|viagra|cialis|payday loans?|essay writing|` +
	`replica watches|presales?)\b`)

// Industry nouns also occur as subjects of financial expectation clauses:
// "investors are betting stocks will rally". Check each occurrence separately
// so an expectation clause cannot exempt another explicit gambling story.
const industryNouns = `(?:stocks?|shares|industr(?:y|ies)|sectors?|compan(?:y|ies)|firms?|operators?|business(?:es)?|revenues?|profits?|earnings)`

var industryPhrase = regexp.MustCompile(`(?i)\b(gambling|betting)[ -]+` + industryNouns + `\b`)
var bettingVerbPrefix = regexp.MustCompile(`(?i)\b(?:am|are|is|was|were|be|been|being|investors?|traders?|analysts?|funds?|markets?|people|they|we)(?:\s+(?:still|now|already|increasingly))?\s+$`)
var expectationClause = regexp.MustCompile(`(?i)^\s+(?:will|would|can|could|may|might|should|must|are|is|was|were|have|has|had|rises?|falls?|rall(?:y|ies)|recovers?|grows?|reports?|outperforms?|improves?|gains?|drops?|climbs?|extends?|increases?|declines?|stays?|remains?)\b`)

func industryStory(text string) bool {
	for _, span := range industryPhrase.FindAllStringIndex(text, -1) {
		if strings.EqualFold(text[span[0]:span[0]+len("betting")], "betting") &&
			bettingVerbPrefix.MatchString(text[:span[0]]) && expectationClause.MatchString(text[span[1]:]) {
			continue
		}
		return true
	}
	return false
}

// Sports odds/fixture pages can otherwise pass on the ordinary word "market".
// Require sports context; financial odds, options and market share remain valid.
var sportsOdds = regexp.MustCompile(`(?i)\b(nba|nfl|nhl|mlb|football|soccer|cricket|tennis|rugby|super bowl|world cup|sports?)\b.{0,100}\b(odds|fixtures?|betting|matches?|scores?)\b|\b(odds|fixtures?)\b.{0,100}\b(nba|nfl|nhl|mlb|football|soccer|cricket|tennis|rugby|super bowl|world cup|sports?)\b`)

// junkHost matches gambling, coupon and deals sites by a substring of their
// lower-cased hostname ("sportsbettingstats.com"), publisher name ("Sports
// Betting Stats") or the site name a title ends in ("… - Sports Betting
// Stats"): a backstop for a page whose headline reads clean.
var junkHost = regexp.MustCompile(`casino|gambl|betting|sportsbook|bookmaker|parlay|coupon|promo-?codes?|voucher|cashback`)

// junkSite reports whether a publisher name or a title's site suffix is a
// gambling, coupon or spam site's.
func junkSite(name string) bool {
	return spam.MatchString(name) || junkHost.MatchString(strings.ToLower(name))
}

// promo matches the presale tokens paid placements push into crypto news
// ("Best Crypto to Buy in October 2026: Remittix, Solana …").
// ponytail: denylist; new presale names leak until added; a model relevance pass is the upgrade.
var promo = regexp.MustCompile(`(?i)\b(Remittix|Pepeto|BlockDAG|Bitcoin Hyper|Little Pepe|MAGACOIN( FINANCE)?|Ozak AI|Mutuum Finance|` +
	`Ruvi AI|BlockchainFX|Layer Brett|Lightchain AI)\b`)

// openpr.com, techbullion.com, einpresswire.com: paid press releases (token presale shills);
// airdropalert.com: airdrop listings; the rest: MarketBeat's auto-generated price pages.
var blockedHosts = []string{"poshakvilla.com", "openpr.com", "airdropalert.com", "techbullion.com", "einpresswire.com",
	"themarketsdaily.com", "thecerbatgem.com", "tickerreport.com", "dailypolitical.com", "thelincolnianonline.com", "wkrb13.com",
	"etfdailynews.com", "americanbankingnews.com", "modernreaders.com", "thestockobserver.com", "transcriptdaily.com",
	"theenterpriseleader.com", "zolmax.com"}

// pricePage is an evergreen price or prediction page ("/price/chainlink/"),
// not news.
var pricePage = regexp.MustCompile(`/price-prediction|/price/[a-z0-9-]+/?$`)

// knownOutlets are newsrooms, data desks and official sources, ranked first.
var knownOutlets = []string{"reuters.com", "bloomberg.com", "cnbc.com", "wsj.com", "ft.com", "apnews.com", "coindesk.com",
	"theblock.co", "decrypt.co", "cointelegraph.com", "blockworks.co", "dlnews.com", "finance.yahoo.com", "marketwatch.com",
	"barrons.com", "forbes.com", "fortune.com", "businessinsider.com", "axios.com", "fxstreet.com", "investing.com", "kitco.com",
	"theguardian.com", "nytimes.com", "economist.com", "federalreserve.gov", "sec.gov", "bls.gov", "cmegroup.com", "coinbase.com",
	"kraken.com", "messari.io", "glassnode.com", "benzinga.com", "thestreet.com", "morningstar.com", "cryptoslate.com",
	"bitcoinmagazine.com", "coingecko.com", "coinmarketcap.com", "politico.com", "schwab.com", "cftc.gov", "treasury.gov",
	// Asian newsrooms for the regional searches (Reuters, Bloomberg and CNBC Asia are above).
	"asia.nikkei.com", "koreaherald.com", "joins.com", "joongang.co.kr", "yna.co.kr", "taipeitimes.com", "focustaiwan.tw",
	"scmp.com", "straitstimes.com", "businesstimes.com.sg", "thejakartapost.com", "bangkokpost.com", "theedgemalaysia.com",
	"inquirer.net", "vnexpress.net"}

// hostOf is rawURL's lower-case hostname without "www." ("" when unparsable).
func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.TrimSuffix(strings.ToLower(u.Hostname()), "."), "www.")
}

// onHost reports whether host is one of domains or a subdomain of one.
func onHost(host string, domains []string) bool {
	return slices.ContainsFunc(domains, func(d string) bool { return host == d || strings.HasSuffix(host, "."+d) })
}

// junkTitle matches evergreen, template and promo titles that are not news:
// an unfilled CMS template ("Month Day, Year"), multi-year ranges ("2026,
// 2027 – 2030"), a site's own name or tagline ("… News and Analysis"), and
// listicle shills ("Best Crypto to Buy …", "next 100x", "We Asked AI …", "AI sets
// ETH price for <date>").
// ponytail: regex blocklist; a model relevance pass is the upgrade if junk keeps leaking.
var junkTitle = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bmonth day,?\s*year\b`),
	regexp.MustCompile(`\b20\d\d\s*(,|–|-|to)\s*20\d\d\b`),
	regexp.MustCompile(`(?i)(news and analysis|latest news|breaking news)\W*$`),
	regexp.MustCompile(`(?i)\bbest (crypto(currenc(y|ies))?s?|altcoins?|coins?|tokens?|meme ?coins?) to (buy|invest)\b`),
	regexp.MustCompile(`(?i)\bnext (10|50|100|1000)x\b`),
	regexp.MustCompile(`(?i)\b\d+x from here\b`),
	regexp.MustCompile(`(?i)\bcrypto(currency)? rankings?\b`),
	regexp.MustCompile(`(?i)\bself[- ]reported market cap`),
	regexp.MustCompile(`(?i)\bwe asked (ai|chatgpt|grok|claude)\b`),
	regexp.MustCompile(`(?i)\bAI\b.{0,40}\b(sets|predicts|picks|forecasts)\b.{0,60}\bprice\b.{0,40}\b(for|by|on)\b`), // "AI sets Ethereum price for October 31, 2026"
	// Currency-converter pages ("800 JPY to USD: Today's Exchange Rate and What
	// It Means for You", "1 BTC to USD", "How much is 500 dollars in yen?"): an
	// amount, a currency, "to" and a currency (ISO codes upper case, names in any
	// case), or exchange-rate template wording. "Yen exchange rate hits 160" and
	// "Bitcoin falls from 100,000 USD to 90,000 USD" are news and stay.
	regexp.MustCompile(`\b\d[\d,.]*\s*(?i:k|m)?\s+(?:[A-Z]{3}|(?i:` + currency + `))\s+(?i:to)\s+(?:[A-Z]{3}|(?i:` + currency + `))\b`),
	regexp.MustCompile(`(?i)\b(today'?s?|live|current) exchange rates?\b`),
	regexp.MustCompile(`(?i)\bexchange rates? (today|live|now|calculator|converter|history|chart)\b`),
	regexp.MustCompile(`(?i)\bcurrency (converter|calculator)\b`),
	regexp.MustCompile(`(?i)\bhow much is \d`),
}

// currency is a currency name with an optional qualifier ("US dollar",
// "Japanese yen"), for the converter rule above.
const currency = `(?:[a-z]+ )?(?:dollars?|yen|euros?|pounds?|sterling|rupees?|rupiah|ringgit|yuan|renminbi|won|pesos?|francs?|dirhams?|` +
	`baht|rand|lira|naira|r[ou]ubles?|dong|reais|real|zloty|krona|kroner?|shillings?|dinars?|riyals?|taka)`

// marketWords are the crypto and market words a wire headline may name
// instead of a universe asset or a US-macro term (usMacro): the vocabulary of
// crypto markets ("Firedancer ships on mainnet", "Staking yields rise") and of
// the macro and markets news that move them ("Stocks slide as oil jumps").
// ponytail: a word list; a model relevance pass is the upgrade if on-topic
// headlines with vague titles go missing.
var marketWords = regexp.MustCompile(`(?i)\b(blockchain|altcoins?|tokens?|tokeni[sz]\w*|coins?|memecoins?|stablecoins?|defi|nfts?|web3|` +
	`on-?chain|l2s?|layer[- ]?[12]|mainnet|testnet|hard ?forks?|smart contracts?|rollups?|dapps?|oracles?|protocols?|dex|cex|` +
	`perps?|perpetuals?|derivatives|options|miners?|mining|hashrate|wallets?|staking|stakers?|validators?|custod(y|ial|ian)|` +
	`liquidations?|whales?|hodl\w*|halving|satoshis?|exchange (lists|listing|delists|hack\w*|outage|reserves|inflows|outflows|volumes?)|` +
	`crypto exchanges?|binance|coinbase|kraken|okx|bybit|bitget|upbit|tether|usdt|usdc|ripple|microstrategy|blackrock|grayscale|fidelity|` +
	`polymarket|prediction markets?|rwa|` +
	`markets?|stocks?|shares|equities|equity|bonds?|yields?|inflation|cpi|pce|ppi|gdp|recession|interest rates?|rate (cuts?|hikes?|decisions?|path)|` +
	`holds? rates|rates (steady|unchanged|higher|lower|on hold)|basis points|bps|economy|economic|economists?|jobs report|jobless|payrolls|` +
	`unemployment|employment|earnings|investors?|traders?|trading|rally|rallies|rallied|selloff|sell-off|sells? off|prices?|dollars?|` +
	`bullish|bearish|bull run|bear market|futures|gold|silver|oil|crude|opec|commodit\w*|forex|yen|euro|sterling|central banks?|boj|ecb|boe|` +
	`pboc|regulat\w*|lawsuit|hack(ed|ers?|s)?|exploit\w*|liquidity|volatility|risk assets?|hedge funds?|banks?|banking|t-bills?|` +
	`debt ceiling|shutdown|sanctions?|geopolit\w*)\b`)

// aboutMarkets reports whether a wire headline is about crypto markets, a
// universe asset, a region's markets or the macro that moves them: its title
// names a universe asset (the tagging rules), a US-macro term (usMacro), a
// market word (marketWords), a region's index, exchange, central bank or
// "<country> stocks" (marketCues) or a universe stock (stockNames). The title
// alone counts: the description of a forex converter or a lifestyle page says
// "US dollar" too, which is how "800 JPY to USD: Today's Exchange Rate and
// What It Means for You" reached the wire.
func aboutMarkets(title string) bool {
	return len(tagSymbols(title)) > 0 || usMacro(title) || marketWords.MatchString(title) || regionalMarket(title)
}

// seoTitle matches SEO templates that are junk except from a known outlet,
// which writes the same words as news ("Bitcoin price prediction" on Reuters):
// price predictions in any form and MarketBeat's auto titles ("Coinmetro
// Token Price Down 1.2% Over Last Week (XCM)").
var seoTitle = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bprice predictions?\b`),
	regexp.MustCompile(`(?i)\bprice (up|down) \d+(\.\d+)?% (over|in) (the )?(last|past) (week|day|24 hours|month)\b`),
}

// foreignWords are function words and common market words of the languages
// Brave still returns despite search_lang=en (Spanish, Portuguese, French,
// German, Italian, Indonesian, Turkish), minus words English titles use too
// ("a", "in", "die", "per", "son").
var foreignWords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`el la los las del y que por para con una es se su al lo como más pero sus este esta hoy ` +
		`precio sube cae baja mercado criptomonedas según tras desde ` +
		`os das dos em na um não é ao pelo pela preço hoje sobe cai mercado ` +
		`le les des du et est pour sur avec dans au aux qui plus selon ` +
		`der das und ist mit für auf dem nicht ein eine zu im vom bei ` +
		`il di che gli della delle nel sono ` +
		`yang dan ke dari untuk ini itu dengan akan ` +
		`ve bir için ile bu`) {
		m[w] = true
	}
	return m
}()

var englishWords = map[string]bool{"the": true, "of": true, "and": true, "to": true, "in": true, "for": true, "on": true,
	"with": true, "is": true, "are": true, "as": true, "at": true, "by": true, "from": true, "after": true, "over": true,
	"its": true, "will": true, "new": true, "says": true, "could": true, "amid": true, "than": true, "this": true,
	"why": true, "how": true, "what": true, "price": true, "market": true, "rises": true, "falls": true, "hits": true}

var words = regexp.MustCompile(`\p{L}+`)

// foreign reports whether a headline is not in English: mostly non-Latin
// letters (Chinese, Russian, Arabic …), or at least two foreign words or
// accented words that outnumber English function words two to one ("Solana
// (SOL) el 3 de octubre de 2026: volumen elevado y señales técnicas mixtas").
// ponytail: word lists; a language-ID model or library is the upgrade if other languages leak.
func foreign(text string) bool {
	var latin, other, f, en int
	for _, w := range words.FindAllString(text, -1) {
		lw := strings.ToLower(w)
		accented := false
		for _, r := range w {
			switch {
			case r < 0x80:
				latin++
			case unicode.Is(unicode.Latin, r):
				latin++
				accented = true
			default:
				other++
			}
		}
		switch {
		case englishWords[lw]:
			en++
		case foreignWords[lw] || accented:
			f++
		}
	}
	return other > latin || f >= 2 && f > 2*en
}

// junk is a result no AI surface should see: spam or promo wording (on the
// title, the description or the source name), a blocked or gambling host, a
// press release (except a known outlet's: the Fed, the SEC and the Treasury
// publish under /pressreleases), a site's homepage or title, an evergreen
// price page, a template, converter or SEO title or a headline that is not in
// English. source is Brave's publisher name or the stored source ("" skips it).
func junk(title, description, rawURL, source string) bool {
	return junkIn(title, description, rawURL, source, true)
}

// junkIn is junk with the English-only rule on or off: a search or read that
// asked for another language keeps its non-English text.
func junkIn(title, description, rawURL, source string, english bool) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return true
	}
	path, host := strings.ToLower(u.Path), hostOf(rawURL)
	labels := strings.Split(host, ".")
	matches := func(re *regexp.Regexp) bool { return re.MatchString(title) }
	seo := slices.ContainsFunc(seoTitle, matches) || strings.Contains(path, "/press-release") || strings.Contains(path, "/pressrelease")
	return industryStory(title) || industryStory(description) || sportsOdds.MatchString(title+" "+description) || spam.MatchString(title) || spam.MatchString(description) || junkSite(source) || junkSite(titleSite(title)) ||
		promo.MatchString(title) || promo.MatchString(description) ||
		onHost(host, blockedHosts) || junkHost.MatchString(host) || path == "" || path == "/" || pricePage.MatchString(path) ||
		slices.ContainsFunc(junkTitle, matches) || seo && !known(host) ||
		alnum(title) == alnum(labels[max(len(labels)-2, 0)]) || english && foreign(title+" "+description)
}

// roundup is a page that names 4+ universe assets: a multi-coin price or
// levels page ("Pandabull | Support Resistance Levels"), not one asset's news.
func roundup(title, description string) bool { return len(tagSymbols(title+" "+description)) >= 4 }

// Relevant reports whether a stored wire headline may reach an AI surface:
// not junk, about markets (its title names a universe asset, a US-macro term
// or a market word; see aboutMarkets), and when symbol is set, it names that
// asset in its title or description (Mentions), and a roundup names it in its
// title. Live search results keep only the junk rules: a forecast question or
// a chat search can be about anything.
func Relevant(title, description, rawURL, source, symbol string) bool {
	if junk(title, description, rawURL, source) || !aboutMarkets(title) {
		return false
	}
	return symbol == "" || Mentions(title+" "+description, symbol) && (!roundup(title, description) || Mentions(title, symbol))
}

// topicGroup is an item's OnTopic group for symbol: 0 a single-asset item
// whose title names it, 1 another single-asset item, 2 a roundup.
func topicGroup(title, desc, symbol string) int {
	switch {
	case roundup(title, desc):
		return 2
	case Mentions(title, symbol):
		return 0
	}
	return 1
}

// OnTopic orders items for one asset, stably: single-asset items whose title
// names symbol, then the other single-asset items, then at most one roundup.
// symbol "" returns items unchanged.
func OnTopic[T any](items []T, symbol string, title, desc func(T) string) []T {
	if symbol == "" {
		return items
	}
	group := func(it T) int { return topicGroup(title(it), desc(it), symbol) }
	out := slices.Clone(items)
	slices.SortStableFunc(out, func(a, b T) int { return cmp.Compare(group(a), group(b)) })
	if i := slices.IndexFunc(out, func(it T) bool { return group(it) == 2 }); i >= 0 {
		out = out[:i+1] // roundups sort last: keep the first
	}
	return out
}

// Symbols lists the universe assets text names, by the tagging rules (sorted, never nil).
func Symbols(text string) []string { return tagSymbols(text) }

var (
	usWords   = regexp.MustCompile(`\b(US|USA|Fed|FOMC|SEC|CFTC|Dow)\b|\bU\.S\.|\bS&P\b`)
	usPhrases = regexp.MustCompile(`(?i)\b(america\w*|federal reserve|powell|treasur(y|ies)|wall street|nasdaq|congress|white house|tariffs?|dollar|crypto\w*|bitcoin|stablecoins?|etfs?)\b`)
)

// usMacro reports whether a macro headline is about US markets or crypto,
// not another country's economy ("Pakistan's inflation remains in double digits").
// ponytail: keyword match; a named country's story that mentions the dollar still passes.
func usMacro(text string) bool { return usWords.MatchString(text) || usPhrases.MatchString(text) }

// known is a result from a recognized outlet.
func known(host string) bool { return onHost(host, knownOutlets) }

// Known reports whether rawURL is on a recognized outlet (newsroom, data desk or official source).
func Known(rawURL string) bool { return known(hostOf(rawURL)) }

// titleSite extracts only a publisher suffix, preserving ordinary headline wording.
func titleSite(title string) string {
	for _, sep := range []string{" | ", " - ", " – ", " — "} {
		if i := strings.LastIndex(title, sep); i >= 0 && len(title[i+len(sep):]) <= 40 {
			return title[i+len(sep):]
		}
	}
	return ""
}

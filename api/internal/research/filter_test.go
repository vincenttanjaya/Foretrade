package research

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
)

func TestJunk(t *testing.T) {
	for _, tc := range []struct {
		title, desc, url string
		junk             bool
	}{
		{"Bitcoin casinos - Poshakvilla", "", "https://poshakvilla.com/bitcoin-casinos/", true}, // seen in prod's wire
		{"Best crypto casino bonus codes for October", "", "https://spam.example/a", true},
		{"Bitcoin ETF", "Claim 50 free spins today", "https://spam.example/b", true},
		{"Top Bitcoin betting sites ranked", "", "https://spam.example/c", true},
		{"A totally normal title", "", "https://blog.poshakvilla.com/x", true},          // blocked host and its subdomains
		{"Las Vegas Sands casino earnings beat", "", "https://www.reuters.com/x", true}, // by design: the blocklist drops it too
		{"Spot bitcoin ETFs log $1bn of inflows", "BlackRock's IBIT led", "https://www.coindesk.com/a", false},
		{"Fed holds rates steady, signals two cuts", "Powell spoke after the meeting", "https://www.reuters.com/b", false},
		{"Slotted for launch: Ethereum's next upgrade", "Developers set a date", "https://decrypt.co/c", false},
		{"Pornography-free headline about Solana", "", "https://x.example", true},
		{"Pros and Cons of 12 Best Cryptos to Invest Now - Apeing Presale Sold 480M+ Tokens", "", "https://www.openpr.com/news/1", true}, // seen in the agent's sources
		{"Crypto Watchlist 2026", "", "https://news.example/presale-x", false},
		{"New token presale raises $5m", "", "https://spam.example/d", true},
		// Homepages, evergreen price pages and airdrop listings are not news.
		{"COINTURK NEWS - Bitcoin, Blockchain and Cryptocurrency News", "", "https://cointurk.com/", true},
		{"Crypto news", "", "https://news.example", true},
		{"Chainlink (LINK) Price Prediction 2026-2030", "", "https://coincodex.com/crypto/chainlink/price-prediction/", true},
		{"Chainlink price today", "", "https://www.coinbase.com/price/chainlink", true},
		{"Chainlink price today", "", "https://www.coinbase.com/en-gb/price/chainlink-2/", true},
		{"Chainlink price analysis: bulls eye $30", "", "https://news.example/price/chainlink/analysis-oct-3", false},
		{"Claim the Chainlink airdrop", "", "https://airdropalert.com/chainlink", true},
		{"Spot Chainlink ETF filed", "", "https://www.coindesk.com/markets/2026/10/03/link-etf", false},
		// Template "AI price target" pages (finbold-style), any outlet; real AI and price news stays.
		{"AI sets Ethereum price for October 31, 2026", "", "https://finbold.com/ai-sets-ethereum-price", true},
		{"ChatGPT-5 AI predicts XRP price by the end of 2026", "", "https://www.coindesk.com/x", true},
		{"Nvidia's AI chip sales lift its price target for 2027 at Morgan Stanley", "", "https://www.reuters.com/n", false},
		{"Ethereum price slips as AI tokens rally on Nvidia earnings", "", "https://www.coindesk.com/e", false},
	} {
		if got := junk(tc.title, tc.desc, tc.url, ""); got != tc.junk {
			t.Errorf("junk(%q, %q, %s) = %v", tc.title, tc.desc, tc.url, got)
		}
	}
	for host, want := range map[string]bool{"reuters.com": true, "finance.yahoo.com": true, "uk.finance.yahoo.com": true, "yahoo.com": false,
		"notreuters.com": false, "coindesk.com": true} {
		if known(host) != want {
			t.Errorf("known(%s) = %v", host, !want)
		}
	}
	if !Known("https://www.politico.com/x") || !Known("https://www.cftc.gov/PressRoom") || Known("https://politico.example/x") || Known("::bad") {
		t.Error("Known")
	}
}

func TestSearchDropsJunkAndRanksOutlets(t *testing.T) {
	f := newFakeBrave(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"results":[
			{"title":"Bitcoin price prediction blog","url":"https://blog.example/btc"},
			{"title":"Bitcoin casinos - Poshakvilla","url":"https://poshakvilla.com/bitcoin-casinos/"},
			{"title":"Bitcoin slips as yields rise","url":"https://www.reuters.com/markets/btc"},
			{"title":"Win big: bitcoin sportsbook bonus","url":"https://bets.example/x"},
			{"title":"Bitcoin miners sell","url":"https://www.coindesk.com/miners"},
			{"title":"Bitcoin odds for the Super Bowl","url":"https://sbs.example/odds","profile":{"name":"Sports Betting Stats"}},
			{"title":"Another blog","url":"https://other.example/y"}]}`)
	})
	s := New(Config{BraveAPIKey: "k", BraveBase: f.srv.URL}, nil, nil)
	got, err := s.SearchNews(context.Background(), "bitcoin", "pd", 10)
	if err != nil {
		t.Fatal(err)
	}
	var urls []string
	for _, r := range got {
		urls = append(urls, r.URL)
	}
	// "Bitcoin price prediction blog" is an SEO title on an unknown host: junk since round 4; the Super Bowl odds go by
	// their source name (round 7). Live search has no on-topic rule, so "Another blog" stays.
	want := []string{"https://www.reuters.com/markets/btc", "https://www.coindesk.com/miners", "https://other.example/y"}
	if fmt.Sprint(urls) != fmt.Sprint(want) {
		t.Fatalf("results %v, want %v", urls, want)
	}
}

func TestNewsHidesStoredSpamAndMistags(t *testing.T) {
	pool := storetest.Schema(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `insert into news_items (url, title, source, description, published_at, symbols) values
		('https://poshakvilla.com/bitcoin-casinos/', 'Bitcoin casinos - Poshakvilla', 'poshakvilla.com', '', now(), '{BTC}'),
		('https://news.example/etf', 'Bitcoin ETF inflows top $1bn', 'Desk', '', now() - interval '1 hour', '{BTC}'),
		('https://news.example/fed', 'Fed holds rates', 'Wire', 'Risk assets including bitcoin dipped', now() - interval '2 hours', '{BTC}'),
		('https://news.example/fed-2', 'Fed holds rates again', 'Wire', '', now() - interval '150 minutes', '{BTC}'),
		('https://news.example/bnb', 'BNB Chain volumes hit a record', 'Desk', '', now() - interval '3 hours', '{AVAX,BNB}'),
		('https://www.airdropalert.com/link', 'Chainlink airdrop', 'AirdropAlert', '', now(), '{LINK}'),
		('https://es.example/occ', 'Mercado cripto: la OCC aprueba nuevas reglas', 'Noticias', '', now(), '{LINK}')`); err != nil {
		t.Fatal(err)
	}
	s := New(Config{}, pool, nil)
	items, err := s.News(ctx, NewsFilter{Symbol: "BTC", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	// The BTC search's stored rows that never name bitcoin (the old tagging rule) are hidden; one that names it
	// only in its description stays.
	if len(items) != 2 || items[0].Title != "Bitcoin ETF inflows top $1bn" || items[1].Title != "Fed holds rates" {
		t.Fatalf("news: %+v", items)
	}
	if h, _ := s.Headlines(ctx, "BTC", 0, 10); len(h) != 2 {
		t.Fatalf("headlines: %+v", h)
	}
	if h, _ := s.Headlines(ctx, "AVAX", 0, 10); len(h) != 0 {
		t.Fatalf("AVAX shows another coin's story: %+v", h)
	}
	if h, _ := s.Headlines(ctx, "BNB", 0, 10); len(h) != 1 {
		t.Fatalf("BNB: %+v", h)
	}
	if h, _ := s.Headlines(ctx, "LINK", 0, 10); len(h) != 0 { // the Spanish OCC row and the airdrop listing
		t.Fatalf("LINK: %+v", h)
	}
	if all, _ := s.News(ctx, NewsFilter{}); len(all) != 4 { // unfiltered: only junk goes (spam, airdrop, the Spanish row)
		t.Fatalf("all news: %+v", all)
	}
}

// Brave returns other languages despite search_lang=en; English headlines that
// borrow a foreign or accented word stay.
func TestForeign(t *testing.T) {
	for _, tc := range []struct {
		text    string
		foreign bool
	}{
		{"Solana (SOL) el 3 de octubre de 2026: volumen elevado y señales técnicas mixtas", true}, // topped prod's SOL news
		{"Bitcoin cae a 80.000 dólares tras la decisión de la Fed", true},
		{"Preço do Bitcoin hoje: BTC sobe 3% com entrada de ETFs", true},
		{"Le bitcoin dépasse les 90 000 dollars pour la première fois", true},
		{"Bitcoin steigt über 90.000 Dollar und ist auf Rekordkurs", true},
		{"Harga Bitcoin hari ini naik ke level tertinggi dan akan terus", true},
		{"比特币突破9万美元 创历史新高", true},
		{"Биткоин обновил исторический максимум", true},
		{"Solana Eyes $135 After Fiserv Stablecoin Platform Launch", false},
		{"Banco de España backs MiCA rules for crypto firms", false},
		{"Société Générale's crypto arm launches a euro stablecoin on Solana", false},
		{"El Salvador buys more bitcoin as IMF talks continue", false},
		{"Pokémon card market goes on-chain with a new token", false},
		{"Spot bitcoin ETFs log $1bn of inflows BlackRock's IBIT led", false},
		{"Fed holds rates steady, signals two cuts", false},
	} {
		if got := foreign(tc.text); got != tc.foreign {
			t.Errorf("foreign(%q) = %v", tc.text, got)
		}
	}
	if !junk("Solana (SOL) el 3 de octubre de 2026: volumen elevado y señales técnicas mixtas", "", "https://www.diariobitcoin.com/sol-3-octubre", "") {
		t.Error("a Spanish headline is not junk")
	}
	// A search or read that asked for another language keeps it; the other rules still hold.
	if junkIn("比特币突破9万美元 创历史新高", "", "https://www.zaobao.com.sg/finance/story", "", false) ||
		!junkIn("比特币突破9万美元 创历史新高", "", "https://www.zaobao.com.sg/finance/story", "", true) ||
		!junkIn("Best Crypto to Buy Now: 比特币", "", "https://www.zaobao.com.sg/finance/promo", "", false) ||
		!junkIn("比特币", "", "https://www.zaobao.com.sg/", "", false) {
		t.Error("junkIn with english off must only lift the language rule")
	}
}

func TestJunkTitles(t *testing.T) {
	for _, tc := range []struct {
		title, url string
		junk       bool
	}{
		{"Fortune · Current price of gold: Month Day, Year | Fortune", "https://fortune.com/article/current-price-of-gold", true},
		{"Avalanche (AVAX) Price Prediction 2026, 2027 – 2030: Will AVAX Price Hit $100?", "https://news.example/avax", true},
		{"Bitcoin price prediction for 2027", "https://news.example/btc", true},
		{"COINTURK NEWS - Bitcoin, Blockchain and Cryptocurrency News and Analysis", "https://cointurk.com/news", true},
		{"Crypto: Latest News", "https://news.example/crypto", true},
		{"Stocktradersdaily", "https://stocktradersdaily.com/news/123", true},                                         // the site's own name
		{"BNB Price Prediction: 44% Below Record, 37th Burn Nears", "https://cryptoticker.io/en/bnb-37th-burn", true}, // round 4: any price prediction off a known outlet
		{"Bitcoin price prediction: analysts see $150k", "https://www.reuters.com/markets/btc-prediction", false},     // a newsroom's own story
		{"Bitcoin ETF inflows hit $1bn as price nears 2026 high", "https://www.coindesk.com/a", false},
		{"Breaking news: SEC approves spot SOL ETF", "https://news.example/sol-etf", false}, // only a trailing tagline counts
	} {
		if got := junk(tc.title, "", tc.url, ""); got != tc.junk {
			t.Errorf("junk(%q, %s) = %v", tc.title, tc.url, got)
		}
	}
}

// The two multi-coin pages from the reviews: a BNB article whose description
// names four coins, and a levels page naming eight.
var (
	bnbRoundup = NewsItem{Title: "BNB Nears 37th Burn, 44% Below Record", URL: "https://cryptoticker.io/en/bnb-price-prediction-37th-burn-record-minus-44-percent",
		Description: "MANTRA OM price surges despite ... crypto of 2025. ... Ethereum holds firm above $2,300 while Bitcoin leads the rally. " +
			"Technical analysis suggests a potential ETH coin breakout toward $2,500 in the coming days. ... As of February 4, 2026, Solana (SOL) has decisively ..."}
	levelsRoundup = NewsItem{Title: "Pandabull | Support Resistance Levels - BTC", URL: "https://pandabull.io/support_resistance/BTC",
		Description: "We provide support and resistance analysis for 500+ cryptocurrency symbols including Bitcoin (BTC), Ethereum (ETH), Ripple (XRP), " +
			"Solana (SOL), Binance Coin (BNB), Sui (SUI), Avalanche (AVAX), Toncoin (TON), and many more."}
)

func TestRoundupsAndOnTopic(t *testing.T) {
	if !roundup(bnbRoundup.Title, bnbRoundup.Description) || !roundup(levelsRoundup.Title, levelsRoundup.Description) ||
		roundup("Bitcoin and Ether ETFs draw $1bn", "Solana funds lag") {
		t.Fatal("roundup")
	}
	relevant := func(n NewsItem, symbol string) bool { return Relevant(n.Title, n.Description, n.URL, n.Source, symbol) }
	for _, sym := range []string{"SOL", "ETH"} {
		if relevant(bnbRoundup, sym) || relevant(levelsRoundup, sym) {
			t.Errorf("%s keeps a roundup whose title does not name it", sym)
		}
	}
	if !relevant(bnbRoundup, "BNB") || relevant(bnbRoundup, "BTC") || !relevant(levelsRoundup, "BTC") {
		t.Error("a roundup stays for the asset its title names")
	}

	btc := NewsItem{Title: "Bitcoin slips as yields rise", URL: "https://www.reuters.com/btc"}
	desc := NewsItem{Title: "Fed holds rates", Description: "Risk assets including bitcoin dipped", URL: "https://news.example/fed"}
	second := levelsRoundup
	second.Title, second.URL = "Pandabull | Support Resistance Levels - BTC (daily)", "https://pandabull.io/support_resistance/BTC/1d"
	in := []NewsItem{levelsRoundup, desc, second, btc}
	title, description := func(n NewsItem) string { return n.Title }, func(n NewsItem) string { return n.Description }
	urls := func(items []NewsItem) (out []string) {
		for _, n := range items {
			out = append(out, n.URL)
		}
		return out
	}
	if got := urls(OnTopic(in, "BTC", title, description)); !slices.Equal(got, []string{btc.URL, desc.URL, levelsRoundup.URL}) {
		t.Errorf("OnTopic(BTC) = %q", got)
	}
	if in[0].URL != levelsRoundup.URL || len(OnTopic(in, "", title, description)) != 4 {
		t.Error("OnTopic changed its input, or reordered without a symbol")
	}
}

// Round 4 source hygiene: presale promos, listicle and SEO titles, MarketBeat
// auto pages and press releases; a known outlet keeps its own price stories.
func TestJunkRound4(t *testing.T) {
	for _, tc := range []struct {
		title, desc, url string
		junk             bool
	}{
		{"Remittix Draws Whales as XRP Stalls", "", "https://news.example/remittix", true},
		{"Solana eyes $200 as buyers return", "Analysts also like presale token Remittix for 2026", "https://news.example/sol", true},
		{"Best Crypto to Buy in October 2026: Remittix, Solana and More", "", "https://www.streetinsider.com/x", true},
		{"Best Crypto to Buy Now: 5 picks", "", "https://news.example/best", true},
		{"Top Altcoins to Invest In This Week", "", "https://news.example/alts", false}, // "top", not "best"
		{"Next 100x Crypto: Three Tokens to Watch", "", "https://news.example/100x", true},
		{"Could SUI Go 10x From Here?", "", "https://news.example/sui", true},
		{"2026 Cryptocurrency Rankings", "", "https://news.example/rankings", true},
		{"Token climbs, self-reported market cap tops $1bn", "", "https://news.example/cap", true},
		{"We Asked AI Which Coin Will Win October", "", "https://news.example/ai", true},
		{"BlockDAG presale nears its cap", "", "https://news.example/bdag", true},
		{"Bitcoin hyperinflation hedge debate returns", "", "https://news.example/hyper", false}, // not the "Bitcoin Hyper" token
		{"SUI Price Prediction: Bull Run Started!", "", "https://coingabbar.com/en/crypto-currency-news/sui-price-prediction", true},
		{"Bitcoin price prediction", "", "https://www.reuters.com/markets/bitcoin-price-prediction", false},
		{"Solana Price Prediction October 2026", "", "https://news.example/sol-oct", true},
		{"Coinmetro Token Price Down 1.2% Over Last Week (XCM)", "", "https://news.example/xcm", true},
		{"Coinmetro Token Price Down 1.2% Over Last Week (XCM)", "", "https://www.themarketsdaily.com/2026/10/02/xcm.html", true}, // blocked host
		{"Some token shares rose", "", "https://tickerreport.com/banking-finance/1", true},
		{"Bitcoin price down 5% over the past week", "", "https://www.reuters.com/markets/btc-week", false},
		{"Exchange lists new token", "", "https://news.example/press-release/exchange-lists", true},
		{"Exchange lists new token", "", "https://www.globenewswire.com/news-release/2026/10/02/pressrelease-123.html", true},
		{"Exchange lists new token", "", "https://techbullion.com/exchange-lists", true},
		{"Exchange lists new token", "", "https://www.einpresswire.com/article/1", true},
		{"CFTC approves spot crypto trading on DCMs", "", "https://www.cftc.gov/PressRoom/PressReleases/9999-26", false}, // a known outlet's own releases stay
		{"Federal Reserve issues FOMC statement", "", "https://www.federalreserve.gov/newsevents/pressreleases/monetary20261028a.htm", false},
	} {
		if got := junk(tc.title, tc.desc, tc.url, ""); got != tc.junk {
			t.Errorf("junk(%q, %q, %s) = %v", tc.title, tc.desc, tc.url, got)
		}
	}
}

// Round 7 wire quality: the forex-converter page a sports-betting site put in
// prod's wire (cited as source 1 by the agent), converter and gambling
// wording, and the host and source-name backstops; the on-topic headlines
// listed beside it stay.
func TestJunkRound7(t *testing.T) {
	for _, tc := range []struct {
		title, url, source string
		junk               bool
	}{
		{"800 JPY to USD: Today's Exchange Rate and What It Means for You - Sports Betting Stats", "https://sbs.example/800-jpy-to-usd", "Sports Betting Stats", true},
		{"800 JPY to USD: Today's Exchange Rate and What It Means for You", "https://rates.example/800-jpy-to-usd", "", true}, // the converter template alone
		{"1 BTC to USD", "https://rates.example/btc-usd", "", true},
		{"500 Japanese Yen to US Dollar", "https://rates.example/jpy-usd", "", true},
		{"1.5m USD to EUR converted", "https://rates.example/usd-eur", "", true},
		{"USD to JPY exchange rate today", "https://rates.example/usd-jpy", "", true},
		{"How much is 1 Bitcoin in dollars?", "https://rates.example/how-much", "", true},
		{"Currency converter: live rates", "https://rates.example/convert", "", true},
		{"Bitcoin holds $85,000, BNB eyes $800 as traders await the Fed", "https://cointurk.com/btc-85000", "COINTURK NEWS", false},
		{"SEC Proposes New Custody Framework for Crypto Assets", "https://law.example/sec-custody", "", false},
		{"This Week in Crypto Law (October 4, 2026)", "https://law.example/this-week", "", false},
		{"Fed holds rates, dollar slips", "https://www.reuters.com/fed", "Reuters", false},
		{"Yen exchange rate hits 160 as BOJ holds", "https://www.reuters.com/yen", "Reuters", false},      // news, not a template
		{"Bitcoin falls from 100,000 USD to 90,000 USD in a week", "https://news.example/btc", "", false}, // "USD to 90,000", not a currency pair
		{"Dollar slides to 150 yen after the Fed", "https://news.example/usd", "", false},
		{"Strategy buys 1,000 BTC for $100m", "https://news.example/mstr", "", false},
		{"Polymarket odds favour a December cut", "https://news.example/poly", "", false}, // "odds" is not gambling spam
		{"Bitcoin odds for the Super Bowl", "https://sbs.example/odds", "Sports Betting Stats", true},
		{"Bitcoin odds for the Super Bowl", "https://sportsbettingstats.com/odds", "", true}, // host backstop
		{"Dollar slips after the Fed", "https://www.bettingnews.example/fed", "", true},
		{"Dollar slips after the Fed", "https://coupon-codes.example/fed", "", true},
		{"Bookmakers price a 70% chance of a cut", "https://news.example/bookies", "", true}, // by design, like casino news
		{"Online gambling stocks fall", "https://www.reuters.com/gambling", "Reuters", true},
		{"Binance referral code 2026: 20% off fees", "https://promo.example/binance", "", true},
		{"Treasury's 10-year coupon auction draws weak demand", "https://www.reuters.com/auction", "Reuters", false},
	} {
		if got := junk(tc.title, "", tc.url, tc.source); got != tc.junk {
			t.Errorf("junk(%q, %s, %q) = %v", tc.title, tc.url, tc.source, got)
		}
	}
}

// A wire headline must name a universe asset, a US-macro term or a market word
// in its title; the description does not count (a converter's says "US dollar").
func TestAboutMarkets(t *testing.T) {
	for title, want := range map[string]bool{
		"800 JPY to USD: Today's Exchange Rate and What It Means for You - Sports Betting Stats": false,
		"What It Means for You: Five Tips Before Payday":                                         false,
		"Ten ways to sleep better this autumn":                                                   false,
		"Bitcoin holds $85,000, BNB eyes $800":                                                   true, // assets
		"SEC Proposes New Custody Framework for Crypto Assets":                                   true, // SEC, crypto
		"This Week in Crypto Law (October 4, 2026)":                                              true,
		"Fed holds rates, dollar slips":                                                          true, // macro
		"Powell says policy is well positioned":                                                  true,
		"Jobless claims fall to 210,000":                                                         true,
		"Stocks slide as oil jumps":                                                              true,
		"Firedancer ships on mainnet":                                                            true, // crypto word
		"Staking yields rise across the market":                                                  true,
		"Gold hits a record":                                                                     true, // PAXG's name
		"Pakistan's inflation remains in double digits":                                          true, // on topic; the US-macro rule decides separately
	} {
		if aboutMarkets(title) != want {
			t.Errorf("aboutMarkets(%q) = %v", title, !want)
		}
	}
	if Relevant("What It Means for You: Five Tips Before Payday", "The US dollar and your budget", "https://life.example/payday", "", "") {
		t.Error("Relevant keeps an off-topic title whose description says US dollar")
	}
	if !Relevant("Fed holds rates, dollar slips", "", "https://www.reuters.com/fed", "Reuters", "") {
		t.Error("Relevant drops a macro headline")
	}
}

// Rows stored before round 7 are hidden on read: the converter page from
// prod's wire (its description passed the US-macro rule), a converter whose
// title names an asset, and an off-topic lifestyle page; the on-topic rows
// beside them stay in the contract's order.
func TestNewsHidesOffTopicRows(t *testing.T) {
	pool := storetest.Schema(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `insert into news_items (url, title, source, description, published_at, symbols, topic) values
		('https://sbs.example/800-jpy-to-usd', '800 JPY to USD: Today''s Exchange Rate and What It Means for You - Sports Betting Stats', 'Sports Betting Stats',
		 'Convert 800 Japanese yen to US dollars at today''s rate.', now(), '{}', 'macro'),
		('https://rates.example/btc-usd', '1 BTC to USD: Today''s Exchange Rate', 'Rates', 'Bitcoin to US dollar', now() - interval '5 minutes', '{BTC}', 'asset'),
		('https://life.example/payday', 'What It Means for You: Five Tips Before Payday', 'Life', 'The US dollar and your budget', now() - interval '10 minutes', '{}', 'macro'),
		('https://cointurk.com/btc-85000', 'Bitcoin holds $85,000, BNB eyes $800', 'COINTURK NEWS', '', now() - interval '1 hour', '{BNB,BTC}', 'asset'),
		('https://law.example/sec-custody', 'SEC Proposes New Custody Framework for Crypto Assets', 'Law Desk', '', now() - interval '2 hours', '{}', 'macro'),
		('https://law.example/this-week', 'This Week in Crypto Law (October 4, 2026)', 'Law Desk', '', now() - interval '3 hours', '{}', 'macro'),
		('https://www.reuters.com/fed', 'Fed holds rates, dollar slips', 'Reuters', 'Bitcoin dipped', now() - interval '4 hours', '{BTC}', 'macro')`); err != nil {
		t.Fatal(err)
	}
	s := New(Config{}, pool, nil)
	titles := func(items []NewsItem) (out []string) {
		for _, it := range items {
			out = append(out, it.Title)
		}
		return out
	}
	all, err := s.News(ctx, NewsFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if got := titles(all); !slices.Equal(got, []string{"Bitcoin holds $85,000, BNB eyes $800", "SEC Proposes New Custody Framework for Crypto Assets",
		"This Week in Crypto Law (October 4, 2026)", "Fed holds rates, dollar slips"}) {
		t.Errorf("news: %q", got)
	}
	if h, _ := s.Headlines(ctx, "BTC", 0, 10); !slices.Equal(titles(h), []string{"Bitcoin holds $85,000, BNB eyes $800", "Fed holds rates, dollar slips"}) {
		t.Errorf("BTC headlines: %q", titles(h))
	}
	if h, _ := s.News(ctx, NewsFilter{Topic: "macro"}); len(h) != 3 || h[0].Title != "SEC Proposes New Custody Framework for Crypto Assets" {
		t.Errorf("macro: %q", titles(h))
	}
}

func TestUSMacro(t *testing.T) {
	for text, want := range map[string]bool{
		"Fed holds rates steady as Powell flags inflation risks":    true,
		"US CPI rises 0.3% in September":                            true,
		"U.S. jobless claims fall":                                  true,
		"S&P 500 closes at a record":                                true,
		"Treasury yields climb after the auction":                   true,
		"India's resilience built over past decade … FM Sitharaman": false,
		"Pakistan's inflation remains in double digits":             false,
		"3 Eurozone Bank Stocks to Watch":                           false,
		"Trust us: the focus group says rates matter":               false, // "us" is not "US"
	} {
		if usMacro(text) != want {
			t.Errorf("usMacro(%q) = %v", text, !want)
		}
	}
}

// Per-asset reads hide roundups that do not name the asset in their title and
// list title matches first; the unfiltered read drops non-US untagged macro.
func TestNewsOnTopicAndUSMacro(t *testing.T) {
	pool := storetest.Schema(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `insert into news_items (url, title, source, description, published_at, symbols, topic) values
		($1, $2, 'cryptoticker.io', $3, now(), '{ADA,BNB,BTC,ETH,SOL,TON}', 'asset'),
		($4, $5, 'pandabull.io', $6, now() - interval '10 minutes', '{AVAX,BNB,BTC,ETH,SOL,SUI,TON,XRP}', 'asset'),
		('https://news.example/fed', 'Fed holds rates', 'Wire', 'Risk assets including bitcoin and solana dipped', now() - interval '20 minutes', '{BTC,SOL}', 'macro'),
		('https://news.example/sol', 'Solana DEX volumes hit a record', 'Desk', '', now() - interval '1 hour', '{SOL}', 'asset'),
		('https://news.example/btc', 'Bitcoin miners sell', 'Desk', '', now() - interval '2 hours', '{BTC}', 'asset'),
		('https://news.example/pk', 'Pakistan''s inflation remains in double digits', 'Dawn', '', now() - interval '3 hours', '{}', 'macro'),
		('https://news.example/cpi', 'US CPI rises 0.3% in September', 'Wire', '', now() - interval '4 hours', '{}', 'macro')`,
		bnbRoundup.URL, bnbRoundup.Title, bnbRoundup.Description, levelsRoundup.URL, levelsRoundup.Title, levelsRoundup.Description); err != nil {
		t.Fatal(err)
	}
	s := New(Config{}, pool, nil)
	titles := func(f NewsFilter) []string {
		t.Helper()
		items, err := s.News(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, it := range items {
			out = append(out, it.Title)
		}
		return out
	}
	for sym, want := range map[string][]string{
		"SOL": {"Solana DEX volumes hit a record", "Fed holds rates"},
		"ETH": nil,
		"BTC": {"Bitcoin miners sell", "Fed holds rates", levelsRoundup.Title},
		"BNB": {bnbRoundup.Title}, // the levels page's title names BTC only
	} {
		if got := titles(NewsFilter{Symbol: sym}); !slices.Equal(got, want) {
			t.Errorf("%s: %q, want %q", sym, got, want)
		}
	}
	if got := titles(NewsFilter{Topic: "macro"}); !slices.Equal(got, []string{"Fed holds rates", "US CPI rises 0.3% in September"}) {
		t.Errorf("macro: %q", got)
	}
}

func TestSelectedNewsQuality(t *testing.T) {
	for _, tc := range []struct{ title, source, url string }{
		{"Bitcoin rallies", "Betting News", "https://news.example/a"},
		{"Bitcoin rallies | BettingNews", "Desk", "https://news.example/a"},
		{"Bitcoin rallies", "Desk", "https://OPENPR.COM.:443/a"},
		{"Premier League odds market opens for Saturday fixtures", "Desk", "https://news.example/a"},
		{"Bookmakers price a 70% chance of a cut", "Reuters", "https://reuters.com/a"},
	} {
		if Relevant(tc.title, "", tc.url, tc.source, "") {
			t.Errorf("accepted junk: %+v", tc)
		}
	}
	for _, title := range []string{"European shares climb after strong results", "Polymarket odds favour a December cut", "Treasury coupon auction draws weak demand"} {
		if !Relevant(title, "", "https://reuters.com/a", "Reuters", "") {
			t.Errorf("rejected finance: %s", title)
		}
	}
}

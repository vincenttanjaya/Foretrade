// Package market serves the fixed universe, crypto from Binance public market
// data and US and Asian stocks and indices from their Hyperliquid perps (both
// keyless), caches it briefly, and does the indicator, analysis and
// price-outlook math. Every number the AI cites comes from here.
package market

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo"
)

// Class is what an asset is: a crypto pair on Binance, or a US or Asian stock
// or index that trades here as its Hyperliquid perp.
type Class = string

const (
	Crypto Class = "crypto"
	Stock  Class = "stock"
	Index  Class = "index"
)

type AssetInfo struct {
	Symbol, Pair, Name string // Pair is the upstream instrument: BTCUSDT, xyz:NVDA
	Class              string
	Venue              string
	Listing            *Listing // what a perp references; nil for crypto
}

// Listing is what a stock or index perp references. The perp's mark is USD
// either way: trade.xyz's oracle converts a KRW, JPY, HKD or CNY underlying at
// the prevailing FX rate (offshore CNH for CNY; no FX happens here).
type Listing struct {
	Region        string // "us" or "asia"
	SessionVenue  string // NYSE, KRX, TSE, HKEX or SSE: whose regular session Asset.Session reports
	Underlying    string // short human-readable reference, at most 120 characters
	LocalCurrency string // the underlying's currency: USD, KRW, JPY, HKD or CNY
	// Yahoo is the underlying listing's Yahoo Finance ticker ("" when none):
	// the source for its lifetime high and listing quotes. Internal, not in the API.
	Yahoo string
}

// Perp reports whether the asset is priced, charted and filled from its
// Hyperliquid perp (US and Asian stocks and indices) instead of a Binance spot pair.
func (a AssetInfo) Perp() bool { return a.Class != Crypto }

// Universe is the selected fixed 69 crypto plus 38 stock/index perps: nine US,
// ten Asian, then wave 2's twelve US and seven Asian. WLFI's admission
// counts400 returned bars, including the open bar. NMR has an explicit
// paper-only current24h-volume exception; see the contract. Seven US stocks
// have300 closed daily bars; SP500 alone uses actual shorter retained index
// history. The first Asian perps were admitted on 2026-10-06 with at least 100
// closed daily bars and $0.5M mean 30-day notional volume; wave 2's US stocks
// with at least 200 bars and the same volume, and its Asian perps by owner
// approval (short or pre-IPO-converted history, thin volume; see the contract).
// None of this attests underlying exchange tracking.
// ponytail: the Go slice is the config; changing the universe is one PR.
var Universe = []AssetInfo{
	{"BTC", "BTCUSDT", "Bitcoin", Crypto, "binance", nil},
	{"ETH", "ETHUSDT", "Ethereum", Crypto, "binance", nil},
	{"SOL", "SOLUSDT", "Solana", Crypto, "binance", nil},
	{"BNB", "BNBUSDT", "BNB", Crypto, "binance", nil},
	{"XRP", "XRPUSDT", "XRP", Crypto, "binance", nil},
	{"DOGE", "DOGEUSDT", "Dogecoin", Crypto, "binance", nil},
	{"ADA", "ADAUSDT", "Cardano", Crypto, "binance", nil},
	{"AVAX", "AVAXUSDT", "Avalanche", Crypto, "binance", nil},
	{"LINK", "LINKUSDT", "Chainlink", Crypto, "binance", nil},
	{"SUI", "SUIUSDT", "Sui", Crypto, "binance", nil},
	{"TON", "TONUSDT", "Toncoin", Crypto, "binance", nil},
	{"PAXG", "PAXGUSDT", "PAX Gold (gold proxy)", Crypto, "binance", nil},
	{"TRX", "TRXUSDT", "TRON", Crypto, "binance", nil},
	{"DOT", "DOTUSDT", "Polkadot", Crypto, "binance", nil},
	{"LTC", "LTCUSDT", "Litecoin", Crypto, "binance", nil},
	{"BCH", "BCHUSDT", "Bitcoin Cash", Crypto, "binance", nil},
	{"NEAR", "NEARUSDT", "NEAR Protocol", Crypto, "binance", nil},
	{"APT", "APTUSDT", "Aptos", Crypto, "binance", nil},
	{"ARB", "ARBUSDT", "Arbitrum", Crypto, "binance", nil},
	{"OP", "OPUSDT", "Optimism", Crypto, "binance", nil},
	{"UNI", "UNIUSDT", "Uniswap", Crypto, "binance", nil},
	{"AAVE", "AAVEUSDT", "Aave", Crypto, "binance", nil},
	{"ATOM", "ATOMUSDT", "Cosmos", Crypto, "binance", nil},
	{"FIL", "FILUSDT", "Filecoin", Crypto, "binance", nil},
	{"ICP", "ICPUSDT", "Internet Computer", Crypto, "binance", nil},
	{"ETC", "ETCUSDT", "Ethereum Classic", Crypto, "binance", nil},
	{"XLM", "XLMUSDT", "Stellar", Crypto, "binance", nil},
	{"HBAR", "HBARUSDT", "Hedera", Crypto, "binance", nil},
	{"INJ", "INJUSDT", "Injective", Crypto, "binance", nil},
	{"SEI", "SEIUSDT", "Sei", Crypto, "binance", nil},
	{"TIA", "TIAUSDT", "Celestia", Crypto, "binance", nil},
	{"WLD", "WLDUSDT", "Worldcoin", Crypto, "binance", nil},
	{"RENDER", "RENDERUSDT", "Render", Crypto, "binance", nil},
	{"FET", "FETUSDT", "Artificial Superintelligence (FET)", Crypto, "binance", nil},
	{"ENA", "ENAUSDT", "Ethena", Crypto, "binance", nil},
	{"TAO", "TAOUSDT", "Bittensor", Crypto, "binance", nil},
	{"ONDO", "ONDOUSDT", "Ondo", Crypto, "binance", nil},
	{"ALGO", "ALGOUSDT", "Algorand", Crypto, "binance", nil},
	{"LDO", "LDOUSDT", "Lido DAO", Crypto, "binance", nil},
	{"STX", "STXUSDT", "Stacks", Crypto, "binance", nil},
	// Round 7, by 24h quote volume on 2026-10-05 (re-verified that day: TRADING,
	// 400 returned daily bars and fresh trades; NMR current24h exception is documented).
	{"ZEC", "ZECUSDT", "Zcash", Crypto, "binance", nil},
	{"QNT", "QNTUSDT", "Quant", Crypto, "binance", nil},
	{"SAND", "SANDUSDT", "The Sandbox", Crypto, "binance", nil},
	{"MUBARAK", "MUBARAKUSDT", "Mubarak", Crypto, "binance", nil},
	{"STRK", "STRKUSDT", "Starknet", Crypto, "binance", nil},
	{"AXS", "AXSUSDT", "Axie Infinity", Crypto, "binance", nil},
	{"TRUMP", "TRUMPUSDT", "Official Trump", Crypto, "binance", nil},
	{"ZRO", "ZROUSDT", "LayerZero", Crypto, "binance", nil},
	{"ACE", "ACEUSDT", "Fusionist", Crypto, "binance", nil},
	{"VIRTUAL", "VIRTUALUSDT", "Virtuals Protocol", Crypto, "binance", nil},
	{"ORCA", "ORCAUSDT", "Orca", Crypto, "binance", nil},
	{"RUNE", "RUNEUSDT", "THORChain", Crypto, "binance", nil},
	{"RAY", "RAYUSDT", "Raydium", Crypto, "binance", nil},
	{"NIL", "NILUSDT", "Nillion", Crypto, "binance", nil},
	{"MOVR", "MOVRUSDT", "Moonriver", Crypto, "binance", nil},
	{"PHA", "PHAUSDT", "Phala Network", Crypto, "binance", nil},
	{"DASH", "DASHUSDT", "Dash", Crypto, "binance", nil},
	{"JST", "JSTUSDT", "JUST (JST)", Crypto, "binance", nil},
	{"ETHFI", "ETHFIUSDT", "ether.fi", Crypto, "binance", nil},
	{"NMR", "NMRUSDT", "Numeraire", Crypto, "binance", nil},
	{"WLFI", "WLFIUSDT", "World Liberty Financial", Crypto, "binance", nil},
	{"JTO", "JTOUSDT", "Jito", Crypto, "binance", nil},
	{"POL", "POLUSDT", "Polygon (ex-MATIC)", Crypto, "binance", nil},
	{"CAKE", "CAKEUSDT", "PancakeSwap", Crypto, "binance", nil},
	{"JUP", "JUPUSDT", "Jupiter", Crypto, "binance", nil},
	{"AR", "ARUSDT", "Arweave", Crypto, "binance", nil},
	{"SAGA", "SAGAUSDT", "Saga", Crypto, "binance", nil},
	{"CRV", "CRVUSDT", "Curve DAO", Crypto, "binance", nil},
	{"PENDLE", "PENDLEUSDT", "Pendle", Crypto, "binance", nil},
	// US stocks and indices: trade.xyz perps on Hyperliquid (never core).
	{"NVDA", "xyz:NVDA", "Nvidia", Stock, "hyperliquid", &Listing{"us", NYSE, "Nasdaq: NVDA common share", "USD", "NVDA"}},
	{"TSLA", "xyz:TSLA", "Tesla", Stock, "hyperliquid", &Listing{"us", NYSE, "Nasdaq: TSLA common share", "USD", "TSLA"}},
	{"AAPL", "xyz:AAPL", "Apple", Stock, "hyperliquid", &Listing{"us", NYSE, "Nasdaq: AAPL common share", "USD", "AAPL"}},
	{"MSFT", "xyz:MSFT", "Microsoft", Stock, "hyperliquid", &Listing{"us", NYSE, "Nasdaq: MSFT common share", "USD", "MSFT"}},
	{"AMZN", "xyz:AMZN", "Amazon", Stock, "hyperliquid", &Listing{"us", NYSE, "Nasdaq: AMZN common share", "USD", "AMZN"}},
	{"GOOGL", "xyz:GOOGL", "Alphabet", Stock, "hyperliquid", &Listing{"us", NYSE, "Nasdaq: GOOGL Class A common share", "USD", "GOOGL"}},
	{"META", "xyz:META", "Meta", Stock, "hyperliquid", &Listing{"us", NYSE, "Nasdaq: META Class A common share", "USD", "META"}},
	{"SP500", "xyz:SP500", "S&P 500 (perp)", Index, "hyperliquid", &Listing{"us", NYSE, "S&P 500 index level (USD)", "USD", "^GSPC"}},
	{"XYZ100", "xyz:XYZ100", "XYZ100 perp", Index, "hyperliquid", &Listing{"us", NYSE, "trade.xyz XYZ100 index (USD)", "USD", ""}},
	// Asian stocks and APAC ETFs, the same venue's perps (2026-10-06 admission;
	// exclusions are in the contract). TSM, BABA, EWJ and EWY reference
	// NYSE-listed ADSs and ETFs; the rest local shares the oracle prices in USD.
	{"TSM", "xyz:TSM", "TSMC (Taiwan Semiconductor)", Stock, "hyperliquid", &Listing{"asia", NYSE, "NYSE: TSM ADS = 5 TWSE 2330 shares", "USD", "TSM"}},
	{"BABA", "xyz:BABA", "Alibaba Group", Stock, "hyperliquid", &Listing{"asia", NYSE, "NYSE: BABA ADS = 8 HKEX 9988 shares", "USD", "BABA"}},
	{"SMSN", "xyz:SMSN", "Samsung Electronics", Stock, "hyperliquid", &Listing{"asia", KRX, "KRX 005930 common share, KRW→USD", "KRW", "005930.KS"}},
	{"SKHX", "xyz:SKHX", "SK hynix", Stock, "hyperliquid", &Listing{"asia", KRX, "KRX 000660 common share, KRW→USD", "KRW", "000660.KS"}},
	{"HYUNDAI", "xyz:HYUNDAI", "Hyundai Motor", Stock, "hyperliquid", &Listing{"asia", KRX, "KRX 005380 common share, KRW→USD", "KRW", "005380.KS"}},
	{"SOFTBANK", "xyz:SOFTBANK", "SoftBank Group", Stock, "hyperliquid", &Listing{"asia", TSE, "TSE 9984 ordinary share, JPY→USD", "JPY", "9984.T"}},
	{"MINIMAX", "xyz:MINIMAX", "MiniMax Group", Stock, "hyperliquid", &Listing{"asia", HKEX, "HKEX 0100 Class A share, HKD→USD", "HKD", "0100.HK"}},
	{"ZHIPU", "xyz:ZHIPU", "Zhipu AI (Knowledge Atlas)", Stock, "hyperliquid", &Listing{"asia", HKEX, "HKEX 2513 H-share, HKD→USD", "HKD", "2513.HK"}},
	{"EWJ", "xyz:EWJ", "iShares MSCI Japan ETF", Stock, "hyperliquid", &Listing{"asia", NYSE, "NYSE Arca: EWJ, USD (JPY exposure inside the ETF)", "USD", "EWJ"}},
	{"EWY", "xyz:EWY", "iShares MSCI South Korea ETF", Stock, "hyperliquid", &Listing{"asia", NYSE, "NYSE Arca: EWY, USD (KRW exposure inside the ETF)", "USD", "EWY"}},
	// Wave 2 (measured 2026-10-06): twelve more US stocks under the US rule in the
	// contract, then seven Asian perps the owner approved despite the history
	// caveats their underlying names (exclusions are in the contract).
	{"PLTR", "xyz:PLTR", "Palantir", Stock, "hyperliquid", &Listing{"us", NYSE, "Nasdaq: PLTR Class A common share", "USD", "PLTR"}},
	{"COIN", "xyz:COIN", "Coinbase", Stock, "hyperliquid", &Listing{"us", NYSE, "Nasdaq: COIN Class A common share", "USD", "COIN"}},
	{"HOOD", "xyz:HOOD", "Robinhood", Stock, "hyperliquid", &Listing{"us", NYSE, "Nasdaq: HOOD Class A common share", "USD", "HOOD"}},
	{"MSTR", "xyz:MSTR", "Strategy Inc (MicroStrategy)", Stock, "hyperliquid", &Listing{"us", NYSE, "Nasdaq: MSTR Class A common share", "USD", "MSTR"}},
	{"INTC", "xyz:INTC", "Intel", Stock, "hyperliquid", &Listing{"us", NYSE, "Nasdaq: INTC common share", "USD", "INTC"}},
	{"AMD", "xyz:AMD", "AMD", Stock, "hyperliquid", &Listing{"us", NYSE, "Nasdaq: AMD common share", "USD", "AMD"}},
	{"ORCL", "xyz:ORCL", "Oracle", Stock, "hyperliquid", &Listing{"us", NYSE, "NYSE: ORCL common share", "USD", "ORCL"}},
	{"NFLX", "xyz:NFLX", "Netflix", Stock, "hyperliquid", &Listing{"us", NYSE, "Nasdaq: NFLX common share", "USD", "NFLX"}},
	{"CRCL", "xyz:CRCL", "Circle Internet Group", Stock, "hyperliquid", &Listing{"us", NYSE, "NYSE: CRCL Class A common share", "USD", "CRCL"}},
	{"MU", "xyz:MU", "Micron Technology", Stock, "hyperliquid", &Listing{"us", NYSE, "Nasdaq: MU common share", "USD", "MU"}},
	{"SNDK", "xyz:SNDK", "SanDisk", Stock, "hyperliquid", &Listing{"us", NYSE, "Nasdaq: SNDK common share", "USD", "SNDK"}},
	{"CRWV", "xyz:CRWV", "CoreWeave", Stock, "hyperliquid", &Listing{"us", NYSE, "Nasdaq: CRWV Class A common share", "USD", "CRWV"}},
	{"SKHY", "xyz:SKHY", "SK hynix (Nasdaq ADS)", Stock, "hyperliquid", &Listing{"asia", NYSE, "Nasdaq: SKHY ADS on KRX 000660 shares (live price ratio ≈0.14 shares per ADS); early bars are pre-IPO implied prices", "USD", "SKHY"}},
	{"CXMT", "xyz:CXMT", "CXMT (ChangXin Memory)", Stock, "hyperliquid", &Listing{"asia", SSE, "SSE STAR 688825 A-share, CNY→USD; early bars are pre-IPO implied prices", "CNY", "688825.SS"}},
	{"UNITREE", "xyz:UNITREE", "Unitree Robotics", Stock, "hyperliquid", &Listing{"asia", SSE, "SSE STAR 688836 A-share, CNY→USD; early bars are pre-IPO implied prices", "CNY", "688836.SS"}},
	{"GIGADEV", "xyz:GIGADEV", "GigaDevice Semiconductor", Stock, "hyperliquid", &Listing{"asia", SSE, "SSE 603986 A-share, CNY→USD", "CNY", "603986.SS"}},
	{"KIOXIA", "xyz:KIOXIA", "Kioxia Holdings", Stock, "hyperliquid", &Listing{"asia", TSE, "TSE 285A ordinary share, JPY→USD; bars before the 2026-09-28 3-for-1 split are divided by 3", "JPY", "285A.T"}},
	{"EWT", "xyz:EWT", "iShares MSCI Taiwan ETF", Stock, "hyperliquid", &Listing{"asia", NYSE, "NYSE Arca: EWT, USD (TWD exposure inside the ETF)", "USD", "EWT"}},
	{"KSTR", "xyz:KSTR", "KraneShares STAR 50 ETF", Stock, "hyperliquid", &Listing{"asia", NYSE, "NYSE Arca: KSTR, USD (STAR 50 A-share and CNY exposure inside the ETF)", "USD", "KSTR"}},
}

// Core is the original 12, which also drive the news wire, the market brief's
// sentiment and the AI arena, so their cost stays flat as Universe grows.
var Core = Universe[:12:12]

// CryptoPairs is the Binance part of Universe (core first) and StockPerps the
// Hyperliquid part, US and Asian. The arena, the forecast desk's price rules
// and its prediction-market matching stay on crypto.
var (
	CryptoUniverse = Universe[:nCrypto:nCrypto]
	CryptoPairs    = CryptoUniverse
	StockPerps     = Universe[nCrypto:]
	nCrypto        = slices.IndexFunc(Universe, AssetInfo.Perp)
)

// SymbolList names the full tradable universe. Crypto-only callers use CryptoSymbolList.
func SymbolList() string {
	out := make([]string, len(Universe))
	for i, a := range Universe {
		out[i] = a.Symbol
	}
	return strings.Join(out, ", ")
}

// aliases are names people and the AI say for a symbol; Lookup resolves them
// to the canonical symbol.
var aliases = map[string]string{"TSMC": "TSM"}

// Lookup is case-insensitive and returns the canonical upper-case symbol
// (an alias such as "TSMC" resolves to TSM): a named instrument in Universe,
// else a listed one ("6501.t" is 6501.T).
func Lookup(symbol string) (AssetInfo, bool) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	symbol = cmp.Or(aliases[symbol], symbol)
	i := slices.IndexFunc(Universe, func(a AssetInfo) bool { return a.Symbol == symbol })
	if i < 0 {
		return lookupListed(symbol)
	}
	return Universe[i], true
}

// LookupCrypto rejects stock/index instruments even when global lookup succeeds.
func LookupCrypto(symbol string) (AssetInfo, bool) {
	a, ok := Lookup(symbol)
	return a, ok && a.Class == Crypto
}
func CryptoSymbolList() string {
	out := make([]string, len(CryptoUniverse))
	for i, a := range CryptoUniverse {
		out[i] = a.Symbol
	}
	return strings.Join(out, ", ")
}

func ValidInterval(iv string) bool {
	switch iv {
	case "15m", "1h", "4h", "1d":
		return true
	}
	return false
}

var ErrUpstream = errors.New("market data unavailable")

type Asset struct {
	Symbol         string  `json:"symbol"`
	Pair           string  `json:"pair"`
	Name           string  `json:"name"`
	Price          float64 `json:"price"`
	Change24hPct   float64 `json:"change24hPct"`
	High24h        float64 `json:"high24h"`
	Low24h         float64 `json:"low24h"`
	Volume24hUsd   float64 `json:"volume24hUsd"`
	Range24hSource string  `json:"range24hSource"` // ticker | hourly_candles | session (a quote feed's) | unavailable; unavailable high/low are 0
	Class          Class   `json:"class"`
	Venue          string  `json:"venue"` // AssetInfo.Venue: a perp's price is its Hyperliquid mark, not an exchange print
	// Session is SessionVenue's regular session for a stock or index, "open"
	// or "closed" (nil for crypto and outside the venue's verified calendar),
	// and SessionChangeAt when it next flips; a lunch break is closed. Perps
	// trade around the clock either way: closed describes the exchange's
	// regular session, not verified oracle-update age or a paper halt.
	Session         *string    `json:"session"`
	SessionChangeAt *time.Time `json:"sessionChangeAt"`
	// The perp's Listing ("us"/"asia", NYSE/KRX/TSE/HKEX/SSE, a short reference,
	// USD/KRW/JPY/HKD/CNY); all nil for crypto. Price stays the USD perp mark.
	Region        *string `json:"region"`
	SessionVenue  *string `json:"sessionVenue"`
	Underlying    *string `json:"underlying"`
	LocalCurrency *string `json:"localCurrency"`
	// Halted: Binance stopped trading the pair but still quotes its last trade,
	// so Price and the 24h stats are frozen there. Kept so held positions are
	// valued at that price; GET /api/market/assets leaves the pair out and the
	// paper engine opens nothing in it. Serialized for the models' market tools.
	// A quote-feed row is halted when its quote is stale (QuoteStaleAfter).
	Halted bool `json:"halted,omitempty"`
	// QuoteFeed is a venue "yahoo" row's quote: local price, FX, as-of and the
	// listing's market state (nil on every other row).
	QuoteFeed *QuoteFeedInfo `json:"quoteFeed,omitempty"`
}

// Candle is one kline; T is the open time in unix seconds.
type Candle struct {
	T int64   `json:"t"`
	O float64 `json:"o"`
	H float64 `json:"h"`
	L float64 `json:"l"`
	C float64 `json:"c"`
	V float64 `json:"v"`
}

const (
	assetsTTL  = 10 * time.Second
	candlesTTL = 30 * time.Second
	maxCandles = 3000

	// haltedAfter is how old a pair's 24h-ticker closeTime (its last trade) may
	// be before the pair counts as halted. On 2026-10-05 every trading pair in
	// the universe was within 15 s of serverTime (the thinnest, ETC and LDO,
	// trade 16k-27k times a day) while TONUSDT, status BREAK, sat 619 h back
	// with a nonzero count: an hour without a trade is a halt, not a quiet market.
	haltedAfter = time.Hour
)

type Client struct {
	base string
	http *http.Client

	// HyperliquidBase is Hyperliquid's API (https://api.hyperliquid.xyz in
	// prod), for the stock and index perps and Derivatives. Set before use; ""
	// selects the official public Hyperliquid endpoint when blank.
	HyperliquidBase string

	// Yahoo prices and charts the quote-feed stocks (quotefeed.go). Set before
	// use; nil leaves them out of Assets and fails their candles as upstream.
	Yahoo yahoo.Source
	feeds feedState
	// Now is the quote feeds' clock (staleness and refresh timing); nil is
	// time.Now. Tests set it to read a venue's holiday.
	Now func() time.Time
	// RealtimeHTTP sends the real-time sources' requests (realtime.go); nil is
	// a client with an 8 s timeout. RealtimeTick is RunRealtime's step; 0 is
	// 1 s. Tests set them.
	RealtimeHTTP *http.Client
	RealtimeTick time.Duration
	// Interest lists the symbols users hold, alert on or run an autopilot on
	// (main reads them from the database). RunFeeds re-reads it at most once a
	// minute and keeps those quote-feed rows hot. nil: none.
	Interest func(context.Context) ([]string, error)

	assets cached[[]Asset]
	perps  cached[perpBook]
	xyz    cached[dexBook]             // the stock and index perps, shared by Assets and Derivatives
	ranges cached[map[string]dayRange] // their 24h high and low

	mu           sync.Mutex
	candles      map[string]*cached[[]Candle]
	splitBars    map[string]Candle // closed bars rebuilt across a split (perps.go), by pair/interval/open
	historySlots chan struct{}     // per-client HIP3 admission, acquired before operation goroutines or HTTP; never closed
}

func NewClient(baseURL string) *Client {
	if baseURL == "" {
		baseURL = "https://data-api.binance.vision"
	}
	return &Client{base: baseURL, http: &http.Client{Timeout: 10 * time.Second},
		candles: map[string]*cached[[]Candle]{}, historySlots: make(chan struct{}, 3)}
}

// cached holds one value for a TTL. One caller owns the synchronous fetch;
// other callers wait on loading without holding the state lock and may cancel
// their own wait. Only the fetch owner closes loading or publishes its result.
type cached[T any] struct {
	mu       sync.Mutex
	at       time.Time
	val      T
	loading  chan struct{}
	period   time.Duration // latest candles expire at their next close; zero for ordinary TTLs
	calendar string        // weekly/monthly charts expire at their calendar boundary
	until    time.Time     // boundary anchored to fetch start, not a delayed response's arrival
}

func (c *cached[T]) get(ctx context.Context, ttl time.Duration, fetch func() (T, error)) (T, error) {
	var zero T
	for {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		c.mu.Lock()
		if err := ctx.Err(); err != nil {
			c.mu.Unlock()
			return zero, err
		}
		now := time.Now()
		if !c.at.IsZero() && now.Sub(c.at) < ttl && (c.until.IsZero() || now.Before(c.until)) {
			v := c.val
			c.mu.Unlock()
			return v, nil
		}
		if done := c.loading; done != nil {
			c.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return zero, ctx.Err()
			}
		}
		done := make(chan struct{})
		c.loading = done
		var until time.Time
		if c.calendar != "" {
			until = nextChartBoundary(now, c.calendar)
		} else if c.period > 0 {
			until = now.Truncate(c.period).Add(c.period)
		}
		c.mu.Unlock()
		v, err := fetch()
		c.mu.Lock()
		if canceled := ctx.Err(); canceled != nil {
			v, err = zero, canceled // a late response cannot publish for a canceled owner
		}
		if err == nil {
			c.val, c.at = v, time.Now()
			c.until = until
		}
		c.loading = nil
		close(done)
		c.mu.Unlock()
		return v, err
	}
}

// Assets returns 24h stats for the universe, in universe order. A non-core
// pair Binance stopped pricing is left out (a core one fails the call), and so
// is a stock or index perp Hyperliquid does not list live or price (none is
// core); a halted pair stays, at its last price, with Halted set: callers that
// pick assets for new trades skip it.
func (c *Client) Assets(ctx context.Context) ([]Asset, error) {
	v, err := c.assets.get(ctx, assetsTTL, func() ([]Asset, error) { return c.fetchAssets(ctx) })
	return withSessions(v, time.Now()), err
}

// FreshAssets refreshes the shared snapshot after a long-running decision, so
// execution does not reuse the quote that preceded inference. A zero TTL forces
// a new fetch even after joining an older owner; publication stays ordered with
// ordinary Assets reads, and both owners and waiters retain cancellation.
func (c *Client) FreshAssets(ctx context.Context) ([]Asset, error) {
	v, err := c.assets.get(ctx, 0, func() ([]Asset, error) { return c.fetchAssetsWithFresh(ctx, true) })
	return withSessions(v, time.Now()), err
}

// Price is the last trade price (a perp's mark), at most assetsTTL old (a
// halted pair's last trade before the halt).
func (c *Client) Price(ctx context.Context, symbol string) (float64, error) {
	a, err := c.Quote(ctx, symbol)
	return a.Price, err
}

// Quote is symbol's row of Assets. A universe pair Assets left out is
// ErrUpstream, like the rest of a market outage.
func (c *Client) Quote(ctx context.Context, symbol string) (Asset, error) {
	info, ok := Lookup(symbol)
	if !ok {
		return Asset{}, fmt.Errorf("unknown symbol %q", symbol)
	}
	if info.QuoteFeed() {
		c.Touch(info.Symbol)
	}
	assets, err := c.Assets(ctx)
	if err != nil {
		return Asset{}, err
	}
	for _, a := range assets {
		if a.Symbol == info.Symbol {
			return a, nil
		}
	}
	if info.QuoteFeed() { // a listed row nobody has read yet: read it now
		if rows, _ := c.FillAssets(ctx, []string{info.Symbol}); rows[info.Symbol].Price > 0 {
			return rows[info.Symbol], nil
		}
	}
	return Asset{}, fmt.Errorf("%w: no price for %s", ErrUpstream, info.Pair)
}

// Candles returns the latest limit candles (1..3000), oldest first, including
// a still-open bar when present. Cached snapshots expire at the next close so
// an open row cannot become finalized data merely as the reader's clock advances.
func (c *Client) Candles(ctx context.Context, symbol, interval string, limit int) ([]Candle, error) {
	if !ValidInterval(interval) {
		return nil, fmt.Errorf("unknown interval %q", interval)
	}
	return c.candlesInCurrency(ctx, symbol, interval, limit, false)
}

// candlesInCurrency keeps original listing bars in a separate cache. All
// financial calculations use Candles, whose USD behavior stays unchanged.
func (c *Client) candlesInCurrency(ctx context.Context, symbol, interval string, limit int, original bool) ([]Candle, error) {
	info, ok := Lookup(symbol)
	if !ok || !validChartInterval(interval) {
		return nil, fmt.Errorf("unknown symbol or interval %q/%q", symbol, interval)
	}
	limit = min(max(limit, 1), maxCandles)
	// Fetch in whole 1000-bar pages (same klines calls as an exact limit) so the
	// caller's limit cannot multiply cache keys and upstream calls.
	depth := (limit + 999) / 1000 * 1000
	key := fmt.Sprintf("%s/%s/%d", info.Pair, interval, depth)
	if original {
		key += "/original"
	}
	ttl, period := candlesTTL, time.Duration(BarSeconds(interval))*time.Second
	calendar := ""
	if calendarInterval(interval) {
		calendar = interval
	}
	if info.QuoteFeed() && c.feedClosed(info) {
		// A closed session's bars cannot change: one read holds feedClosedTTL.
		key, ttl, period = key+"/closed", feedClosedTTL, 0
		calendar = ""
	}

	c.mu.Lock()
	entry := c.candles[key]
	if entry == nil {
		if len(c.candles) > 500 { // keep the map bounded: forget idle keys
			for k, e := range c.candles {
				if e.mu.TryLock() {
					if e.loading == nil && time.Since(e.at) > max(candlesTTL, feedClosedTTL) {
						delete(c.candles, k)
					}
					e.mu.Unlock()
				}
			}
		}
		entry = &cached[[]Candle]{period: period, calendar: calendar}
		c.candles[key] = entry
	}
	c.mu.Unlock()

	v, err := entry.get(ctx, ttl, func() ([]Candle, error) {
		if original {
			return c.quoteFeedBarsInCurrency(ctx, info, interval, depth, 0, true)
		}
		return c.fetchBars(ctx, info, interval, depth, 0)
	})
	return slices.Clone(v[max(len(v)-limit, 0):]), err
}

// CandlesBefore returns the newest limit candles (1..3000) that opened
// before end, oldest first. A bar still open at call time is included when it
// opened before end; none, and no error, when the pair has no bars before
// end (it had not listed yet). History never changes, so it is not cached: the caller
// keeps what it fetched.
func (c *Client) CandlesBefore(ctx context.Context, symbol, interval string, end time.Time, limit int) ([]Candle, error) {
	if !ValidInterval(interval) {
		return nil, fmt.Errorf("unknown interval %q", interval)
	}
	return c.candlesBeforeInCurrency(ctx, symbol, interval, end, limit, false)
}

// candlesBeforeInCurrency exposes the same exclusive historical cursor for
// original listing bars without changing CandlesBefore's USD contract.
func (c *Client) candlesBeforeInCurrency(ctx context.Context, symbol, interval string, end time.Time, limit int, original bool) ([]Candle, error) {
	info, ok := Lookup(symbol)
	if !ok || !validChartInterval(interval) || end.Unix() <= 0 {
		return nil, fmt.Errorf("unknown symbol, interval or end %q/%q/%v", symbol, interval, end)
	}
	limit = min(max(limit, 1), maxCandles)
	if original {
		return c.quoteFeedBarsInCurrency(ctx, info, interval, limit, end.UnixMilli()-1, true)
	}
	return c.fetchBars(ctx, info, interval, limit, end.UnixMilli()-1)
}

// fetchBars routes a candle read by venue. A perp returns its actual retained
// listing window (Hyperliquid retains about 5,000 bars per interval), which may
// be shorter than the requested sample.
func (c *Client) fetchBars(ctx context.Context, info AssetInfo, interval string, limit int, endTime int64) ([]Candle, error) {
	if info.QuoteFeed() {
		return c.quoteFeedBars(ctx, info, interval, limit, endTime)
	}
	if !info.Perp() {
		return c.fetchCandles(ctx, info.Pair, interval, limit, endTime)
	}
	if calendarInterval(interval) {
		return c.perpCalendarCandles(ctx, info.Pair, interval, limit, endTime)
	}
	return c.fetchPerpCandles(ctx, info.Pair, interval, limit, endTime)
}

// fetchAssets reads Binance's ticker and, at the same time, the perps and
// the quote feeds: the named rows in Universe order, then the listed rows
// that have a price, in Listed order.
func (c *Client) fetchAssets(ctx context.Context) ([]Asset, error) {
	return c.fetchAssetsWithFresh(ctx, false)
}
func (c *Client) fetchAssetsWithFresh(ctx context.Context, fresh bool) ([]Asset, error) {
	var perps, feeds map[string]Asset
	var wg sync.WaitGroup
	wg.Go(func() { perps = c.perpQuotesWithFresh(ctx, fresh) })
	wg.Go(func() { feeds = c.quoteFeedAssets(ctx, fresh) })
	spot, err := c.spotQuotes(ctx)
	wg.Wait()
	if err != nil {
		return nil, err
	}
	// A core pair Binance omits or does not price fails the call; any other
	// asset is dropped, so one delisting cannot take the market list down.
	out := make([]Asset, 0, len(Universe)+len(Listed))
	for k, a := range Universe {
		as := spot[a.Symbol]
		switch {
		case a.QuoteFeed():
			as = feeds[a.Symbol]
		case a.Perp():
			as = perps[a.Symbol]
		}
		switch {
		case as.Price > 0:
			out = append(out, as)
		case k < len(Core):
			return nil, fmt.Errorf("%w: no price for %s", ErrUpstream, a.Pair)
		case a.QuoteFeed(): // a failed refresh is logged once, in refreshFeeds
		case !a.Perp() || perps != nil: // a failed perp read is logged once, in perpQuotes
			slog.Warn("market: asset dropped, no price", "pair", a.Pair)
		}
	}
	for _, a := range Listed {
		if as := feeds[a.Symbol]; as.Price > 0 {
			out = append(out, as)
		}
	}
	return out, nil
}

// spotQuotes is Binance's 24h ticker for the crypto pairs, by symbol.
func (c *Client) spotQuotes(ctx context.Context) (map[string]Asset, error) {
	pairs := make([]string, len(CryptoPairs))
	for i, a := range CryptoPairs {
		pairs[i] = a.Pair
	}
	symbols, _ := json.Marshal(pairs)
	var rows []struct {
		Symbol             string `json:"symbol"`
		LastPrice          string `json:"lastPrice"`
		PriceChangePercent string `json:"priceChangePercent"`
		HighPrice          string `json:"highPrice"`
		LowPrice           string `json:"lowPrice"`
		QuoteVolume        string `json:"quoteVolume"`
		CloseTime          int64  `json:"closeTime"` // unix ms of the window's end: the last trade
	}
	if err := c.get(ctx, "/api/v3/ticker/24hr", url.Values{"symbols": {string(symbols)}}, &rows); err != nil {
		return nil, err
	}
	byPair := make(map[string]int, len(rows))
	for i, r := range rows {
		byPair[r.Symbol] = i
	}
	// A halted pair keeps its frozen last price (TON at $1.60 for weeks) and is
	// kept, flagged: it is priced, and what users hold in it must stay valued.
	// A missing closeTime is not a halt (fail open).
	out := make(map[string]Asset, len(rows))
	for _, a := range CryptoPairs {
		if i, ok := byPair[a.Pair]; ok {
			r := rows[i]
			out[a.Symbol] = Asset{Symbol: a.Symbol, Pair: a.Pair, Name: a.Name,
				Price: num(r.LastPrice), Change24hPct: num(r.PriceChangePercent),
				High24h: num(r.HighPrice), Low24h: num(r.LowPrice), Volume24hUsd: num(r.QuoteVolume),
				Class: a.Class, Venue: a.Venue, Range24hSource: "ticker",
				Halted: r.CloseTime > 0 && time.Since(time.UnixMilli(r.CloseTime)) > haltedAfter}
		}
	}
	return out, nil
}

// fetchCandles pages backwards 1000 klines at a time (Binance's maximum),
// starting from the bars that opened at or before endTime (unix ms; 0 = now).
func (c *Client) fetchCandles(ctx context.Context, pair, interval string, limit int, endTime int64) ([]Candle, error) {
	var out []Candle
	for len(out) < limit {
		q := url.Values{"symbol": {pair}, "interval": {interval}, "limit": {strconv.Itoa(min(limit-len(out), 1000))}}
		if endTime > 0 {
			q.Set("endTime", strconv.FormatInt(endTime, 10))
		}
		var rows [][]any
		if err := c.get(ctx, "/api/v3/klines", q, &rows); err != nil {
			return nil, err
		}
		page := make([]Candle, 0, len(rows))
		for _, r := range rows {
			if len(r) < 6 {
				return nil, fmt.Errorf("%w: malformed kline", ErrUpstream)
			}
			openMs, _ := r[0].(float64)
			page = append(page, Candle{T: int64(openMs) / 1000,
				O: anyNum(r[1]), H: anyNum(r[2]), L: anyNum(r[3]), C: anyNum(r[4]), V: anyNum(r[5])})
		}
		out = append(page, out...)
		if len(rows) == 0 || len(page) < 1000 { // reached the start of the pair's history
			break
		}
		endTime = page[0].T*1000 - 1
	}
	// No bars at all is a failure only for the live series: history before a
	// pair listed is empty (endTime is still the caller's when out is empty).
	if len(out) == 0 && endTime == 0 {
		return nil, fmt.Errorf("%w: no candles for %s %s", ErrUpstream, pair, interval)
	}
	return out, nil
}

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%s: %w", path, err) // the caller gave up; Binance did not fail
	}
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrUpstream, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: %s: status %d", ErrUpstream, path, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%w: %s: decode: %w", ErrUpstream, path, err)
	}
	return nil
}

func num(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	if !finite(f) {
		return 0
	}
	return f
}

func anyNum(v any) float64 {
	s, _ := v.(string)
	return num(s)
}

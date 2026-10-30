// Contract-shaped sample data (API-CONTRACT.md) for component and page tests.
import { isStockPerp, SYMBOLS } from '../pages/strategies/strategy'
import { QUOTE_FEED_LISTINGS } from '../pages/strategies/quoteFeeds'
import type {
  AIPortfolio,
  NotificationChannels,
  Alert,
  AlertRun,
  AthQuote,
  Drawdown,
  Notification,
  PriceAlertProposal,
  Analysis,
  ArenaAgent,
  ArenaMetrics,
  ArenaOptions,
  ArenaRun,
  ArenaRunSummary,
  ArenaStep,
  Asset,
  Autopilot,
  Credits,
  DeskMeasured,
  DeskRead,
  DeskScenario,
  DeskView,
  Forecast,
  Instrument,
  ForecastFinding,
  ForecastMaterial,
  ForecastProgress,
  ForecastResult,
  ForecastSource,
  ForecastSummary,
  MarketBrief,
  MarketPulse,
  MarketOdds,
  NewsItem,
  OutcomeP,
  Plan,
  Point,
  Portfolio,
  PortfolioReview,
  Position,
  PriceOutlook,
  ScenarioInput,
  ScreenerPage,
  ScreenerQuote,
  ScreenerRow,
  SourceRef,
  Specialist,
  Strategy,
  StrategySummary,
  StrategyForward,
  Trade,
  TrackRecord,
  User,
  Profile,
} from '../lib/types'

export const profile = (over: Partial<Profile> = {}): Profile => ({ enabled: false, riskProfile: null, style: null, focusSymbols: [], customPrompt: '', memories: [], revision: 0, updatedAt: '2026-10-02T00:00:00Z', ...over })
export const user: User = { id: 'u1', email: 'demo@example.com', name: 'Demo Trader', role: 'user', profile: profile() }
export const credits: Credits = { used: 18, limit: 500, resetsAt: '2026-10-03T00:00:00Z' }

const asset = (symbol: string, name: string, price: number, change24hPct: number): Asset => ({
  symbol,
  pair: `${symbol}USDT`,
  name,
  price,
  change24hPct,
  high24h: price * 1.02,
  low24h: price * 0.97,
  volume24hUsd: 1.2e9,
  class: 'crypto', venue: 'binance', session: null, sessionChangeAt: null, range24hSource: 'ticker',
})

export const assets: Asset[] = [
  asset('BTC', 'Bitcoin', 84213.2, 2.31),
  asset('ETH', 'Ethereum', 3000, -1.02),
  asset('SOL', 'Solana', 150, 4.5),
  asset('PAXG', 'PAX Gold', 2650, 0.1),
]

const ASIA_PRICES = { TSM: 485, BABA: 160, SMSN: 62, SKHX: 210, HYUNDAI: 190, SOFTBANK: 120, MINIMAX: 58, ZHIPU: 45, EWJ: 78, EWY: 72 }
const WAVE2_PRICES = { PLTR: 190, COIN: 188, HOOD: 114, MSTR: 165, INTC: 116, AMD: 632, ORCL: 143, NFLX: 68, CRCL: 84, MU: 1058, SNDK: 1695, CRWV: 88,
  SKHY: 190, CXMT: 8.4, UNITREE: 68, GIGADEV: 52, KIOXIA: 119, EWT: 118, KSTR: 22 }
type PerpMeta = [NonNullable<Asset['region']>, NonNullable<Asset['sessionVenue']>, string, NonNullable<Asset['localCurrency']>]
const nasdaq = (s: string, cls = ''): PerpMeta => ['us', 'NYSE', `Nasdaq: ${s} ${cls}common share`, 'USD']
/** Region, session venue, underlying and listing currency per stock/index perp (contract: Asia brief). */
const PERP_META: Record<string, PerpMeta> = {
  NVDA: nasdaq('NVDA'), TSLA: nasdaq('TSLA'), AAPL: nasdaq('AAPL'), MSFT: nasdaq('MSFT'), AMZN: nasdaq('AMZN'), GOOGL: nasdaq('GOOGL'), META: nasdaq('META'),
  SP500: ['us', 'NYSE', 'S&P 500 index level (USD)', 'USD'],
  XYZ100: ['us', 'NYSE', 'trade.xyz XYZ100 index (USD)', 'USD'],
  TSM: ['asia', 'NYSE', 'NYSE: TSM ADS = 5 TWSE 2330 shares', 'USD'],
  BABA: ['asia', 'NYSE', 'NYSE: BABA ADS = 8 HKEX 9988 shares', 'USD'],
  SMSN: ['asia', 'KRX', 'KRX 005930 common share, KRW→USD', 'KRW'],
  SKHX: ['asia', 'KRX', 'KRX 000660 common share, KRW→USD', 'KRW'],
  HYUNDAI: ['asia', 'KRX', 'KRX 005380 common share, KRW→USD', 'KRW'],
  SOFTBANK: ['asia', 'TSE', 'TSE 9984 ordinary share, JPY→USD', 'JPY'],
  MINIMAX: ['asia', 'HKEX', 'HKEX 0100 Class A share, HKD→USD', 'HKD'],
  ZHIPU: ['asia', 'HKEX', 'HKEX 2513 H-share, HKD→USD', 'HKD'],
  EWJ: ['asia', 'NYSE', 'NYSE Arca: EWJ, USD (JPY exposure inside the ETF)', 'USD'],
  EWY: ['asia', 'NYSE', 'NYSE Arca: EWY, USD (KRW exposure inside the ETF)', 'USD'],
  PLTR: nasdaq('PLTR', 'Class A '), COIN: nasdaq('COIN', 'Class A '), HOOD: nasdaq('HOOD', 'Class A '), MSTR: nasdaq('MSTR', 'Class A '),
  INTC: nasdaq('INTC'), AMD: nasdaq('AMD'), ORCL: ['us', 'NYSE', 'NYSE: ORCL common share', 'USD'], NFLX: nasdaq('NFLX'),
  CRCL: ['us', 'NYSE', 'NYSE: CRCL Class A common share', 'USD'], MU: nasdaq('MU'), SNDK: nasdaq('SNDK'), CRWV: nasdaq('CRWV', 'Class A '),
  SKHY: ['asia', 'NYSE', 'Nasdaq: SKHY ADS on KRX 000660 shares (live price ratio ≈0.14 shares per ADS); early bars are pre-IPO implied prices', 'USD'],
  CXMT: ['asia', 'SSE', 'SSE STAR 688825 A-share, CNY→USD; early bars are pre-IPO implied prices', 'CNY'],
  UNITREE: ['asia', 'SSE', 'SSE STAR 688836 A-share, CNY→USD; early bars are pre-IPO implied prices', 'CNY'],
  GIGADEV: ['asia', 'SSE', 'SSE 603986 A-share, CNY→USD', 'CNY'],
  KIOXIA: ['asia', 'TSE', 'TSE 285A ordinary share, JPY→USD; bars before the 2026-09-28 3-for-1 split are divided by 3', 'JPY'],
  EWT: ['asia', 'NYSE', 'NYSE Arca: EWT, USD (TWD exposure inside the ETF)', 'USD'],
  KSTR: ['asia', 'NYSE', 'NYSE Arca: KSTR, USD (STAR 50 A-share and CNY exposure inside the ETF)', 'USD'],
}

// Quote feeds (venue yahoo): local prices and Yahoo FX as read live on 2026-10-06 for a few, synthetic for the rest.
const FEED_FX: Record<string, number> = { USD: 1, SGD: 0.782179, IDR: 0.0000558815, THB: 0.0297708, MYR: 0.244888, VND: 0.0000384615, HKD: 0.127427, JPY: 0.00632083, KRW: 0.000746899, TWD: 0.0314723 }
const FEED_LOCAL: Record<string, number> = { HSDT: 2.65, SE: 96.85, DBS: 78.56, TENCENT: 428.2, BBCA: 6100, TOKYOELEC: 12805 }
/** The USD quote feeds' underlying text, copied from the API's rows (internal/market/universe_quotefeed.go): the search reads it. */
const FEED_UNDERLYING_USD: Record<string, string> = {
  HSDT: 'Nasdaq: HSDT common share',
  SE: 'NYSE: SE ADS of Class A ordinary shares (no local listing)',
  GRAB: 'Nasdaq: GRAB Class A ordinary share',
  PHI: 'NYSE: PHI ADS of PSE TEL common shares',
  TLK: 'NYSE: TLK ADS = 100 IDX TLKM shares',
  PDD: 'Nasdaq: PDD ADS of Class A ordinary shares (no local listing)',
  JD: 'Nasdaq: JD ADS = 2 HKEX 9618 Class A shares',
  BIDU: 'Nasdaq: BIDU ADS = 8 HKEX 9888 Class A shares',
  NIO: 'NYSE: NIO ADS = 1 HKEX 9866 Class A share',
  LI: 'Nasdaq: LI ADS = 2 HKEX 2015 Class A shares',
  XPEV: 'NYSE: XPEV ADS = 2 HKEX 9868 Class A shares',
  NTES: 'Nasdaq: NTES ADS = 5 HKEX 9999 ordinary shares',
  TCOM: 'Nasdaq: TCOM ADS = 1 HKEX 9961 ordinary share',
  BILI: 'Nasdaq: BILI ADS = 1 HKEX 9626 Class Z share',
  FUTU: 'Nasdaq: FUTU ADS of Class A ordinary shares (no local listing)',
  TME: 'NYSE: TME ADS = 2 HKEX 1698 Class A shares',
  BEKE: 'NYSE: BEKE ADS = 3 HKEX 2423 Class A shares',
  ZTO: 'NYSE: ZTO ADS = 1 HKEX 2057 Class A share',
  YUMC: 'NYSE: YUMC common share (also listed as HKEX 9987)',
  VIPS: 'NYSE: VIPS ADS of Class A ordinary shares (no local listing)',
  SONY: 'NYSE: SONY ADS = 1 TSE 6758 share',
  TM: 'NYSE: TM ADS = 10 TSE 7203 shares',
  MUFG: 'NYSE: MUFG ADS = 1 TSE 8306 share',
  HMC: 'NYSE: HMC ADS = 3 TSE 7267 shares',
  SMFG: 'NYSE: SMFG ADS of TSE 8316 shares',
  CPNG: 'NYSE: CPNG Class A common share (no Korean listing)',
  KB: 'NYSE: KB ADS = 1 KRX 105560 share',
  SHG: 'NYSE: SHG ADS = 1 KRX 055550 share',
  PKX: 'NYSE: PKX ADS = 1/4 KRX 005490 share',
  UMC: 'NYSE: UMC ADS = 5 TWSE 2303 shares',
  ASX: 'NYSE: ASX ADS = 2 TWSE 3711 shares',
  INFY: 'NYSE: INFY ADS = 1 NSE INFY share',
  HDB: 'NYSE: HDB ADS = 3 NSE HDFCBANK shares',
  IBN: 'NYSE: IBN ADS = 2 NSE ICICIBANK shares',
  JARDINE: 'SGX J36 ordinary share, quoted in USD (no FX)',
  KARO: 'NASDAQ: KARO ordinary share, Singapore-incorporated issuer, USD',
  HKLAND: 'SGX H78 ordinary share, quoted in USD (no FX)',
  KLIC: 'NASDAQ: KLIC common stock, Singapore-headquartered issuer (US-incorporated), USD',
  CHT: 'NYSE: CHT ADS = 10 TWSE 2412 shares (ratio checked vs live prices)',
  HIMX: 'NASDAQ: HIMX ADS, Tainan-based issuer, no local listing, USD',
  SIMO: 'NASDAQ: SIMO ADS, no local listing, USD',
  KEP: 'NYSE: KEP ADS = 0.5 KRX 015760 share (ratio checked vs live prices)',
  SKM: 'NYSE: SKM ADS on KRX 017670 shares (live price ratio 0.551 local shares per ADS)',
  KT: 'NYSE: KT ADS = 0.5 KRX 030200 share (ratio checked vs live prices)',
  LPL: 'NYSE: LPL ADS = 0.5 KRX 034220 share (ratio checked vs live prices)',
}
// IDX, Bursa and HOSE have no verified calendar, so the API always sends their session as null (contract §2).
const UNVERIFIED_VENUES = new Set(['IDX', 'BURSA', 'HOSE'])
/** A quote-feed row: USD price = local × FX, closed, last quote at the 2026-10-05 US close. */
export function feedAsset(symbol: string, name: string, i = 0, over: Partial<Asset> = {}): Asset {
  const l = QUOTE_FEED_LISTINGS[symbol]
  const fx = FEED_FX[l.localCurrency]
  const local = FEED_LOCAL[symbol] ?? (20 + (i % 40)) / fx
  const price = local * fx
  return {
    symbol, name, price, pair: l.yahoo, class: 'stock', venue: 'yahoo', change24hPct: 0.8, high24h: price * 1.01, low24h: price * 0.99,
    volume24hUsd: 5e6, range24hSource: 'session', session: UNVERIFIED_VENUES.has(l.sessionVenue) ? null : 'closed', sessionChangeAt: null, region: l.region,
    sessionVenue: l.sessionVenue,
    underlying: l.localCurrency === 'USD' ? FEED_UNDERLYING_USD[symbol] : `${l.sessionVenue}: ${l.yahoo.split('.')[0]} ordinary share, ${l.localCurrency}→USD at Yahoo FX`,
    localCurrency: l.localCurrency,
    quoteFeed: { symbol: l.yahoo, exchange: l.sessionVenue, currency: l.localCurrency, localPrice: local, previousClose: local / 1.008, fx,
      fxSymbol: l.localCurrency === 'USD' ? '' : `${l.localCurrency}USD=X`, fxAsOf: l.localCurrency === 'USD' ? null : '2026-10-06T08:58:47Z',
      asOf: '2026-10-05T20:00:01Z', marketState: 'closed', stale: false },
    ...over,
  }
}

/** Full configured mock catalog. Provider marks deliberately differ from the HIP3 chart closes. */
export const universeAssets: Asset[] = SYMBOLS.map(([symbol, name], i) => {
  const existing = assets.find((a) => a.symbol === symbol)
  if (existing) return existing
  if (symbol in QUOTE_FEED_LISTINGS) return feedAsset(symbol, name, i)
  const price = ({ NVDA: 180, TSLA: 280, AAPL: 225, MSFT: 400, AMZN: 190, GOOGL: 170, META: 550, SP500: 6000, XYZ100: 20000, ...ASIA_PRICES, ...WAVE2_PRICES } as Record<string, number>)[symbol] ?? 10 + i
  if (!isStockPerp(symbol)) return { ...asset(symbol, name, price, 0.5), region: null, sessionVenue: null, underlying: null, localCurrency: null }
  const meta = PERP_META[symbol]
  return { symbol, name, price, pair: `xyz:${symbol}`, class: symbol === 'SP500' || symbol === 'XYZ100' ? 'index' : 'stock', venue: 'hyperliquid', change24hPct: 1.2, high24h: price * 1.02, low24h: price * 0.98, volume24hUsd: 1e7, range24hSource: 'hourly_candles', session: 'closed', sessionChangeAt: null, region: meta[0], sessionVenue: meta[1], underlying: meta[2], localCurrency: meta[3] }
})

// Listed Asian index stocks (contract §2, the API's internal/market/listed.json): symbol = pair = the Yahoo ticker, a
// quote feed in USD at Yahoo FX. Names are Yahoo's longName (the 2026-10-13 reads); local prices are illustrative.
type ListedRow = readonly [symbol: string, name: string, venue: 'TSE' | 'KRX' | 'IDX', ccy: 'JPY' | 'KRW' | 'IDR', index: string, local: number]
const LISTED: readonly ListedRow[] = [
  ['6501.T', 'Hitachi, Ltd.', 'TSE', 'JPY', 'Nikkei 225', 4726],
  ['000810.KS', 'Samsung Fire & Marine Insurance Co., Ltd.', 'KRX', 'KRW', 'KOSPI 200', 412000],
  ['BBNI.JK', 'PT Bank Negara Indonesia (Persero) Tbk', 'IDX', 'IDR', 'IDX LQ45', 4310],
]
const listedUnderlying = ([symbol, , venue, ccy, index]: ListedRow) => `${venue}: ${symbol.split('.')[0]} (${index}), ${ccy}→USD at Yahoo FX`

/** The catalogue row (GET /api/market/instruments) of a named universe asset. */
export const namedInstrument = (a: Asset): Instrument => ({
  symbol: a.symbol, pair: a.pair, name: a.name, class: a.class, venue: a.venue, region: a.region ?? null, sessionVenue: a.sessionVenue ?? null,
  localCurrency: a.localCurrency ?? null, underlying: a.underlying ?? null,
  yahoo: a.venue === 'yahoo' ? (a.quoteFeed?.symbol ?? a.pair) : a.class === 'crypto' || a.symbol === 'XYZ100' ? null : a.symbol, listed: false, index: null,
})
export const listedInstruments: Instrument[] = LISTED.map((row) => ({
  symbol: row[0], pair: row[0], name: row[1], class: 'stock', venue: 'yahoo', region: 'asia', sessionVenue: row[2], localCurrency: row[3],
  underlying: listedUnderlying(row), yahoo: row[0], listed: true, index: row[4],
}))
/** The full catalogue: the named universe (SYMBOLS), then the listed fixtures. */
export const instruments: Instrument[] = [...universeAssets.map(namedInstrument), ...listedInstruments]

/**
 * A listed row's asset as `scope=listed` sends it. 6501.T: TSE open, last trade 03:31 UTC; 000810.KS: KRX closed;
 * BBNI.JK: IDX has no verified calendar (session null), open per Yahoo.
 */
export function listedAsset(symbol: string, over: Partial<Asset> = {}): Asset {
  const row = LISTED.find(([s]) => s === symbol)!
  const [, name, venue, ccy, , local] = row
  const fx = FEED_FX[ccy]
  const price = local * fx
  const state = venue === 'KRX' ? 'closed' : 'open'
  const asOf = venue === 'TSE' ? '2026-10-13T03:31:00Z' : venue === 'KRX' ? '2026-10-13T06:30:00Z' : '2026-10-13T03:32:45Z'
  return {
    symbol, name, price, pair: symbol, class: 'stock', venue: 'yahoo', change24hPct: venue === 'KRX' ? -1.4 : 0.9, high24h: price * 1.012, low24h: price * 0.991,
    volume24hUsd: 3.6e8, range24hSource: 'session', session: venue === 'IDX' ? null : state, sessionChangeAt: venue === 'IDX' ? null : '2026-10-13T06:30:00Z',
    region: 'asia', sessionVenue: venue, underlying: listedUnderlying(row), localCurrency: ccy,
    quoteFeed: { symbol, exchange: venue, currency: ccy, localPrice: local, previousClose: local / 1.009, fx, fxSymbol: `${ccy}USD=X`,
      fxAsOf: '2026-10-13T03:20:00Z', asOf, marketState: state, stale: false },
    ...over,
  }
}
export const listedAssets: Asset[] = LISTED.map(([symbol]) => listedAsset(symbol))

export const position: Position = {
  symbol: 'ETH',
  qty: 2,
  avgPrice: 2900,
  markPrice: 3000,
  marketValue: 6000,
  unrealizedPnl: 200,
  unrealizedPnlPct: 3.45,
  stopLoss: 2700,
  takeProfit: null,
}

export const portfolio: Portfolio = {
  cash: 94000,
  equity: 100194,
  startingBalance: 100000,
  unrealizedPnl: 200,
  realizedPnl: -6,
  totalPnl: 194,
  totalPnlPct: 0.19,
  grossExposure: 6000,
  buyingPower: 194388,
  positions: [position],
  revision: 1,
}

export const emptyPortfolio: Portfolio = {
  ...portfolio,
  cash: 100000,
  equity: 100000,
  unrealizedPnl: 0,
  realizedPnl: 0,
  totalPnl: 0,
  totalPnlPct: 0,
  grossExposure: 0,
  buyingPower: 200000,
  positions: [],
}

export const trade = (over: Partial<Trade> = {}): Trade => ({
  id: 't1',
  symbol: 'ETH',
  side: 'buy',
  qty: 2,
  price: 2900,
  notional: 5800,
  fee: 5.8,
  realizedPnl: -5.8,
  source: 'manual',
  note: '',
  createdAt: '2026-10-02T08:00:00Z',
  ...over,
})

export const analysis: Analysis = {
  symbol: 'BTC',
  interval: '1h',
  price: 84213.2,
  return1dPct: 1.2,
  return7dPct: 3.4,
  return30dPct: 8.1,
  realizedVolAnnPct: 48.6,
  rsi14: 61.4,
  sma20: 83000,
  sma50: 81000,
  sma200: 70000,
  ema20: 83500,
  atr14: 900,
  support: 81500,
  resistance: 86000,
  trend: 'uptrend',
}

export const outlook: PriceOutlook = {
  symbol: 'BTC',
  spot: 84213.2,
  horizonDays: 3,
  volAnnPct: 48.6,
  buckets: [
    { label: '< $80.0k', lo: null, hi: 80000, p: 0.15 },
    { label: '$80.0k – $88.0k', lo: 80000, hi: 88000, p: 0.62 },
    { label: '≥ $88.0k', lo: 88000, hi: null, p: 0.23 },
  ],
  p5: 78000,
  p50: 84200,
  p95: 91000,
}

/**
 * A 10-year outlook as the API computes it (zero-drift lognormal, σ 62% long-run from 3,000 daily closes, reflection-
 * principle touches; the drift case at a halved 15%/yr). Numbers come from that model, not from a market.
 */
export const longOutlook: PriceOutlook = {
  symbol: 'BTC',
  spot: 84213.2,
  horizonDays: 3650,
  volAnnPct: 62,
  buckets: [
    { label: '< $4.45k', lo: null, hi: 4450, p: 0.0668 },
    { label: '$4.45k – $19.4k', lo: 4450, hi: 19400, p: 0.1602 },
    { label: '$19.4k – $51.6k', lo: 19400, hi: 51600, p: 0.1744 },
    { label: '$51.6k – $137k', lo: 51600, hi: 137000, p: 0.1967 },
    { label: '$137k – $366k', lo: 137000, hi: 366000, p: 0.1752 },
    { label: '$366k – $1.59M', lo: 366000, hi: 1590000, p: 0.1598 },
    { label: '≥ $1.59M', lo: 1590000, hi: null, p: 0.0669 },
  ],
  p5: 3348.23,
  p50: 84213.2,
  p95: 2118093.53,
  volBasis: { window: 'long-run', days: 3000, since: '2018-07-17' },
  longHorizon: {
    multiples: [
      { x: 0.5, pEnd: 0.3618, pTouch: 0.7237 },
      { x: 2, pEnd: 0.3618, pTouch: 0.7237 },
      { x: 5, pEnd: 0.2059, pTouch: 0.4117 },
      { x: 10, pEnd: 0.1201, pTouch: 0.2402 },
    ],
    tenX: [
      { years: 1, pTouch: 0.0002041 },
      { years: 3, pTouch: 0.03202 },
      { years: 5, pTouch: 0.09674 },
      { years: 10, pTouch: 0.2402 },
    ],
    driftCase: {
      label: 'historical drift, halved (assumption)',
      muAnnPct: 15,
      p50: 377417.38,
      multiples: [
        { x: 0.5, pEnd: 0.1317, pTouch: 0.5157 },
        { x: 2, pEnd: 0.6597, pTouch: 0.8858 },
        { x: 5, pEnd: 0.4777, pTouch: 0.6757 },
        { x: 10, pEnd: 0.3411, pTouch: 0.4993 },
      ],
    },
  },
}

/** A 2-year outlook on a market with under 2 years of history: σ 48% from 380 closes, so no drift case. Model outputs. */
export const twoYearOutlook: PriceOutlook = {
  symbol: 'NVDA',
  spot: 182.5,
  horizonDays: 730,
  volAnnPct: 48,
  buckets: [
    { label: '< $65.9', lo: null, hi: 65.9, p: 0.0667 },
    { label: '$65.9 – $110', lo: 65.9, hi: 110, p: 0.1612 },
    { label: '$110 – $154', lo: 110, hi: 154, p: 0.1733 },
    { label: '$154 – $216', lo: 154, hi: 216, p: 0.1968 },
    { label: '$216 – $304', lo: 216, hi: 304, p: 0.1759 },
    { label: '$304 – $505', lo: 304, hi: 505, p: 0.1592 },
    { label: '≥ $505', lo: 505, hi: null, p: 0.0669 },
  ],
  p5: 59.75,
  p50: 182.5,
  p95: 557.42,
  volBasis: { window: 'long-run', days: 380, since: '2025-09-18' },
  longHorizon: {
    multiples: [
      { x: 0.5, pEnd: 0.1536, pTouch: 0.3072 },
      { x: 2, pEnd: 0.1536, pTouch: 0.3072 },
      { x: 5, pEnd: 0.008872, pTouch: 0.01774 },
      { x: 10, pEnd: 0.0003469, pTouch: 0.0006938 },
    ],
    tenX: [{ years: 1, pTouch: 0.00000161 }],
    driftCase: null,
  },
}

/**
 * "Can TSMC 10x, if yes by when" as the API answers it (numbers to 6 significant
 * figures): three EPS × P/E scenarios at five years (FY2027 EPS $21.33 cited [1]; growth and multiples labelled, the
 * bull's "P/E 25 [1]" relabelled an assumption), variance-matched mixture, the 10× odds by year and the zero-drift
 * baseline beside it. Run against the API's fakes: spot $485 and a synthetic 29-year series shaped like a listing's
 * Yahoo closes, so the history block is not TSM's measured numbers.
 */
export const tsmOutlookView: PriceOutlook = {
  symbol: 'TSM',
  spot: 485,
  horizonDays: 1825,
  volAnnPct: 38.1998,
  buckets: [
    { label: '< $135', lo: null, hi: 135, p: 0.0671696 },
    { label: '$135 – $256', lo: 135, hi: 256, p: 0.160043 },
    { label: '$256 – $392', lo: 256, hi: 392, p: 0.174378 },
    { label: '$392 – $600', lo: 392, hi: 600, p: 0.19677 },
    { label: '$600 – $920', lo: 600, hi: 920, p: 0.174869 },
    { label: '$920 – $1.75k', lo: 920, hi: 1750, p: 0.16026 },
    { label: '≥ $1.75k', lo: 1750, hi: null, p: 0.0665101 },
  ],
  p5: 119.004,
  p50: 485,
  p95: 1976.61,
  volBasis: { window: 'long-run', days: 7569, since: '1997-10-01', source: 'Yahoo Finance daily closes (NYSE: TSM)' },
  longHorizon: {
    multiples: [
      { x: 0.5, pEnd: 0.208544, pTouch: 0.417088 },
      { x: 2, pEnd: 0.208544, pTouch: 0.417088 },
      { x: 5, pEnd: 0.0297685, pTouch: 0.0595371 },
      { x: 10, pEnd: 0.00351215, pTouch: 0.0070243 },
    ],
    tenX: [{ years: 1, pTouch: 1.66262e-09 }, { years: 3, pTouch: 0.000501188 }, { years: 5, pTouch: 0.0070243 }],
    driftCase: {
      label: 'historical drift, halved (assumption)',
      muAnnPct: 6.19521,
      p50: 661.103,
      multiples: [
        { x: 0.5, pEnd: 0.120172, pTouch: 0.301573 },
        { x: 2, pEnd: 0.326774, pTouch: 0.54325 },
        { x: 5, pEnd: 0.0640593, pTouch: 0.112397 },
        { x: 10, pEnd: 0.00982286, pTouch: 0.0176852 },
      ],
    },
  },
  horizon: { days: 1825, basis: 'by_when', reason: 'You asked by when it could reach 10×: odds by year, with the view at 5 years' },
  history: {
    source: 'Yahoo Finance daily closes (NYSE: TSM)',
    from: '1997-10-01',
    to: '2026-10-05',
    years: 29,
    volAnnPct: 38.1998,
    cagrPct: { y5: -2.55513, y10: -18.5437, all: 13.1907 },
    maxDrawdownPct: -91.9391,
    maxDrawdownPeak: '2016-11-01',
    maxDrawdownTrough: '2026-04-24',
    recovered: null,
    driftHalvedPct: 6.19521,
  },
  view: {
    kind: 'scenarios',
    label: 'Our scenarios (subjective weights)',
    muAnnPct: null,
    median: 672.253,
    p5: 161.904,
    p25: 372.894,
    p75: 1200.19,
    p95: 2690.49,
    pAbove: 0.645838,
    changePct: 38.6088,
    annualPct: 6.74761,
    buckets: [
      { label: '< $250', lo: null, hi: 250, p: 0.128294, pBaseline: 0.218926 },
      { label: '$250 – $360', lo: 250, hi: 360, p: 0.109085, pBaseline: 0.144645 },
      { label: '$360 – $485', lo: 360, hi: 485, p: 0.116783, pBaseline: 0.136428 },
      { label: '$485 – $750', lo: 485, hi: 750, p: 0.196025, pBaseline: 0.195096 },
      { label: '$750 – $1,000', lo: 750, hi: 1000, p: 0.127159, pBaseline: 0.106447 },
      { label: '$1,000 – $1,500', lo: 1000, hi: 1500, p: 0.148739, pBaseline: 0.105345 },
      { label: '≥ $1,500', lo: 1500, hi: null, p: 0.173916, pBaseline: 0.0931123 },
    ],
    multiples: [
      { x: 0.5, pEnd: 0.121032, pTouch: 0.237693, baselinePEnd: 0.208544, baselinePTouch: 0.417088 },
      { x: 2, pEnd: 0.335523, pTouch: 0.490287, baselinePEnd: 0.208544, baselinePTouch: 0.417088 },
      { x: 5, pEnd: 0.0645204, pTouch: 0.0956056, baselinePEnd: 0.0297685, baselinePTouch: 0.0595371 },
      { x: 10, pEnd: 0.00875027, pTouch: 0.0133084, baselinePEnd: 0.00351215, baselinePTouch: 0.0070243 },
    ],
    listingSpot: null,
    volAnnPct: 38.1998,
    volAssumed: false,
  },
  scenarios: [
    {
      name: 'AI capex digestion',
      kind: 'bear',
      p: 0.3,
      level: 346.182,
      changePct: -28.6222,
      annualPct: -6.5213,
      inputs: [
        { field: 'eps', value: 21.33, year: 2027, basis: '[1]', kind: 'source', verified: true },
        { field: 'growthPct', value: 3, year: 2032, basis: 'assumption', kind: 'assumption', verified: false },
        { field: 'pe', value: 14, basis: 'model knowledge, as of 2025-06', kind: 'model_knowledge', verified: false },
      ],
    },
    {
      name: 'Compounding foundry leader',
      kind: 'base',
      p: 0.5,
      level: 751.815,
      changePct: 55.0134,
      annualPct: 9.16259,
      inputs: [
        { field: 'eps', value: 21.33, year: 2027, basis: '[1]', kind: 'source', verified: true },
        { field: 'growthPct', value: 12, year: 2032, basis: 'assumption', kind: 'assumption', verified: false },
        { field: 'pe', value: 20, basis: 'assumption', kind: 'assumption', verified: false },
      ],
    },
    {
      name: 'AI supercycle',
      kind: 'bull',
      p: 0.2,
      level: 1326.9,
      changePct: 173.587,
      annualPct: 22.2979,
      inputs: [
        { field: 'eps', value: 21.33, year: 2027, basis: '[1]', kind: 'source', verified: true },
        { field: 'growthPct', value: 20, year: 2032, basis: 'assumption', kind: 'assumption', verified: false },
        { field: 'pe', value: 25, basis: 'assumption', kind: 'assumption', verified: false },
      ],
    },
  ],
  timeToTarget: {
    multiple: 10,
    target: 4850,
    rows: [
      { years: 1, pEnd: 1.86294e-09, pTouch: 3.48747e-09, baselinePEnd: 8.31311e-10, baselinePTouch: 1.66262e-09, extrapolated: false },
      { years: 3, pEnd: 0.000591702, pTouch: 0.000990793, baselinePEnd: 0.000250594, baselinePTouch: 0.000501188, extrapolated: false },
      { years: 5, pEnd: 0.00875027, pTouch: 0.0133084, baselinePEnd: 0.00351215, baselinePTouch: 0.0070243, extrapolated: false },
      { years: 10, pEnd: 0.115961, pTouch: 0.154374, baselinePEnd: 0.028316, baselinePTouch: 0.056632, extrapolated: false },
      { years: 15, pEnd: 0.250092, pTouch: 0.311166, baselinePEnd: 0.0598116, baselinePTouch: 0.119623, extrapolated: true },
      { years: 20, pEnd: 0.353564, pTouch: 0.423444, baselinePEnd: 0.0888541, baselinePTouch: 0.177708, extrapolated: true },
    ],
    medianYears: null,
    byScenario: [
      { name: 'AI capex digestion', kind: 'bear', annualPct: -6.5213, years: null },
      { name: 'Compounding foundry leader', kind: 'base', annualPct: 9.16259, years: 26.2647 },
      { name: 'AI supercycle', kind: 'bull', annualPct: 22.2979, years: 11.4392 },
    ],
    requiredCagr: [{ years: 3, pct: 115.443 }, { years: 5, pct: 58.4893 }, { years: 10, pct: 25.8925 }, { years: 15, pct: 16.5914 }, { years: 20, pct: 12.2018 }],
    ownCagrPct: 13.1907,
    ownSince: '1997-10-01',
  },
  // The card's note as the API writes it (the raw "pe 25 cited [1] not found…" line goes to the model only).
  warnings: ['P/E 25× is not stated in source [1]: shown as an assumption'],
}

/**
 * "NVDA 10 years outlook" as the API answers it with no scenarios (the API's fakes: spot $235, a
 * synthetic 27-year NVDA-like series and a synthetic S&P 500): the horizon read from the question (3,653 days, over
 * the model's 1,825), the halved drift capped at the S&P 500's long-run return + 2 points, and the ladder grown to
 * eight ranges so the upside is not one open bucket. Model outputs from synthetic inputs, not NVDA's numbers.
 */
export const nvdaOutlookView: PriceOutlook = {
  symbol: 'NVDA',
  spot: 235,
  horizonDays: 3653,
  volAnnPct: 56.0248,
  buckets: [
    { label: '< $16.5', lo: null, hi: 16.5, p: 0.0669796 },
    { label: '$16.5 – $62.2', lo: 16.5, hi: 62.2, p: 0.159658 },
    { label: '$62.2 – $151', lo: 62.2, hi: 151, p: 0.174829 },
    { label: '$151 – $366', lo: 151, hi: 366, p: 0.197229 },
    { label: '$366 – $888', lo: 366, hi: 888, p: 0.174693 },
    { label: '$888 – $3.35k', lo: 888, hi: 3350, p: 0.159698 },
    { label: '≥ $3.35k', lo: 3350, hi: null, p: 0.0669134 },
  ],
  p5: 12.7339,
  p50: 235,
  p95: 4336.85,
  volBasis: { window: 'long-run', days: 7047, since: '1999-10-01', source: 'Yahoo Finance daily closes (NYSE: NVDA)' },
  longHorizon: {
    multiples: [
      { x: 0.5, pEnd: 0.347869, pTouch: 0.695737 },
      { x: 2, pEnd: 0.347869, pTouch: 0.695737 },
      { x: 5, pEnd: 0.181923, pTouch: 0.363846 },
      { x: 10, pEnd: 0.0969475, pTouch: 0.193895 },
    ],
    tenX: [{ years: 1, pTouch: 3.95766e-05 }, { years: 3, pTouch: 0.0176503 }, { years: 5, pTouch: 0.0660595 }, { years: 10, pTouch: 0.193712 }],
    driftCase: {
      label: 'historical drift, halved (assumption)',
      muAnnPct: 14.5863,
      p50: 1011.73,
      multiples: [
        { x: 0.5, pEnd: 0.112234, pTouch: 0.46263 },
        { x: 2, pEnd: 0.667337, pTouch: 0.881089 },
        { x: 5, pEnd: 0.466366, pTouch: 0.65231 },
        { x: 10, pEnd: 0.317219, pTouch: 0.460748 },
      ],
    },
  },
  horizon: { days: 3653, basis: 'explicit', reason: 'From your question: "10 years outlook"' },
  history: {
    source: 'Yahoo Finance daily closes (NYSE: NVDA)',
    from: '1999-10-01',
    to: '2026-10-05',
    years: 27,
    volAnnPct: 56.0248,
    cagrPct: { y5: 6.91674, y10: 21.3286, all: 33.8737 },
    maxDrawdownPct: -66.9285,
    maxDrawdownPeak: '2013-08-16',
    maxDrawdownTrough: '2015-12-28',
    recovered: '2016-10-14',
    driftHalvedPct: 14.5863,
  },
  view: {
    kind: 'historical_drift',
    label: 'Historical drift since 1999, capped at the market\'s long-run return + 2 pts (assumption)',
    muAnnPct: 9.2249,
    median: 591.602,
    p5: 32.057,
    p25: 178.998,
    p75: 1955.29,
    p95: 10917.8,
    pAbove: 0.698784,
    changePct: 151.745,
    annualPct: 9.66379,
    buckets: [
      { label: '< $60', lo: null, hi: 60, p: 0.098319, pBaseline: 0.220566 },
      { label: '$60 – $120', lo: 60, hi: 120, p: 0.0857122, pBaseline: 0.131703 },
      { label: '$120 – $235', lo: 120, hi: 235, p: 0.117185, pBaseline: 0.147731 },
      { label: '$235 – $470', lo: 235, hi: 470, p: 0.147136, pBaseline: 0.152131 },
      { label: '$470 – $940', lo: 470, hi: 940, p: 0.1547, pBaseline: 0.130809 },
      { label: '$940 – $1,900', lo: 940, hi: 1900, p: 0.141777, pBaseline: 0.0979031 },
      { label: '$1,900 – $3,800', lo: 1900, hi: 3800, p: 0.108171, pBaseline: 0.0609833 },
      { label: '≥ $3,800', lo: 3800, hi: null, p: 0.147, pBaseline: 0.0581733 },
    ],
    multiples: [
      { x: 0.5, pEnd: 0.180888, pTouch: 0.54793, baselinePEnd: 0.347869, baselinePTouch: 0.695737 },
      { x: 2, pEnd: 0.551648, pTouch: 0.823514, baselinePEnd: 0.347869, baselinePTouch: 0.695737 },
      { x: 5, pEnd: 0.349321, pTouch: 0.546361, baselinePEnd: 0.181923, baselinePTouch: 0.363846 },
      { x: 10, pEnd: 0.218215, pTouch: 0.351277, baselinePEnd: 0.0969475, baselinePTouch: 0.193895 },
    ],
    listingSpot: null,
    volAnnPct: 56.0248,
    volAssumed: false,
    driftCap: {
      benchmark: 'SPX',
      source: 'Yahoo Finance daily closes (NYSE: ^GSPC)',
      since: '1966-09-27',
      benchmarkCagrPct: 7.66379,
      capPct: 9.66379,
      uncappedPct: 15.7038,
      applied: true,
    },
  },
  // The API's "Horizon set from the question …, not horizon_days 1825" is a note to the model, in the tool
  // result only; the horizon line already says where the 10 years came from.
  warnings: [],
}

/**
 * "Is SOL a good long-term hold?" with horizon_days 1,095 and no horizon_reason: accepted, basis
 * "agent", the reason saying so, and a warning to the model. The API's fakes; not SOL's numbers.
 */
export const solAgentView: PriceOutlook = {
  symbol: 'SOL',
  spot: 200,
  horizonDays: 1095,
  volAnnPct: 16.4678,
  buckets: [
    { label: '< $130', lo: null, hi: 130, p: 0.0654841 },
    { label: '$130 – $161', lo: 130, hi: 161, p: 0.157999 },
    { label: '$161 – $186', lo: 161, hi: 186, p: 0.176099 },
    { label: '$186 – $215', lo: 186, hi: 215, p: 0.200496 },
    { label: '$215 – $248', lo: 215, hi: 248, p: 0.174546 },
    { label: '$248 – $307', lo: 248, hi: 307, p: 0.158878 },
    { label: '≥ $307', lo: 307, hi: null, p: 0.0664972 },
  ],
  p5: 125.105,
  p50: 200,
  p95: 319.731,
  volBasis: { window: 'long-run', days: 3000, since: '2018-07-16' },
  longHorizon: {
    multiples: [
      { x: 0.5, pEnd: 0.00754679, pTouch: 0.0150936 },
      { x: 2, pEnd: 0.00754679, pTouch: 0.0150936 },
      { x: 5, pEnd: 8.37613e-09, pTouch: 1.67523e-08 },
      { x: 10, pEnd: 3.4379e-16, pTouch: 6.87581e-16 },
    ],
    tenX: [{ years: 1, pTouch: 1.99827e-44 }, { years: 3, pTouch: 6.87581e-16 }],
    driftCase: {
      label: 'historical drift, halved (assumption)',
      muAnnPct: 6.99,
      p50: 246.662,
      multiples: [
        { x: 0.5, pEnd: 0.000774562, pTouch: 0.00203873 },
        { x: 2, pEnd: 0.0450442, pTouch: 0.0726429 },
        { x: 5, pEnd: 4.61497e-07, pTouch: 8.21844e-07 },
        { x: 10, pEnd: 1.08802e-13, pTouch: 1.99921e-13 },
      ],
    },
  },
  horizon: { days: 1095, basis: 'agent', reason: 'Horizon chosen by the agent: 1,095 days (3 years)' },
  history: {
    source: 'Binance daily closes (SOL/USDT)',
    from: '2018-07-16',
    to: '2026-10-01',
    years: 8.2,
    volAnnPct: 16.4678,
    cagrPct: { y5: 15.5102, y10: null, all: 15.0044 },
    maxDrawdownPct: -14.0437,
    maxDrawdownPeak: '2025-05-24',
    maxDrawdownTrough: '2025-06-15',
    recovered: '2025-07-04',
    driftHalvedPct: 6.99,
  },
  view: {
    kind: 'historical_drift',
    label: 'Historical drift since 2018, halved (assumption)',
    muAnnPct: 6.99,
    median: 246.662,
    p5: 154.293,
    p25: 203.493,
    p75: 298.988,
    p95: 394.327,
    pAbove: 0.768889,
    changePct: 23.3308,
    annualPct: 7.2401,
    buckets: [
      { label: '< $120', lo: null, hi: 120, p: 0.00576661, pBaseline: 0.0366531 },
      { label: '$120 – $160', lo: 120, hi: 160, p: 0.0588011, pBaseline: 0.180358 },
      { label: '$160 – $200', lo: 160, hi: 200, p: 0.166543, pBaseline: 0.282988 },
      { label: '$200 – $250', lo: 200, hi: 250, p: 0.287685, pBaseline: 0.282988 },
      { label: '$250 – $320', lo: 250, hi: 320, p: 0.300479, pBaseline: 0.167315 },
      { label: '$320 – $400', lo: 320, hi: 400, p: 0.135681, pBaseline: 0.0421501 },
      { label: '≥ $400', lo: 400, hi: null, p: 0.0450442, pBaseline: 0.00754679 },
    ],
    multiples: [
      { x: 0.5, pEnd: 0.000774562, pTouch: 0.00203873, baselinePEnd: 0.00754679, baselinePTouch: 0.0150936 },
      { x: 2, pEnd: 0.0450442, pTouch: 0.0726429, baselinePEnd: 0.00754679, baselinePTouch: 0.0150936 },
      { x: 5, pEnd: 4.61497e-07, pTouch: 8.21844e-07, baselinePEnd: 8.37613e-09, baselinePTouch: 1.67523e-08 },
      { x: 10, pEnd: 1.08802e-13, pTouch: 1.99921e-13, baselinePEnd: 3.4379e-16, baselinePTouch: 6.87581e-16 },
    ],
    listingSpot: null,
    volAnnPct: 16.4678,
    volAssumed: false,
    driftCap: { benchmark: '', source: '', since: null, benchmarkCagrPct: null, capPct: 10, uncappedPct: 7.2401, applied: false },
  },
  // The "No horizon_reason: …" note is the model's, in the tool result only; the horizon line says it.
  warnings: [],
}

/**
 * The tail note the API gives only to a scenarios view whose downside is really too thin, as the API writes it for
 * the reader (the advice to add a tail scenario goes to the model only).
 */
export const thinTailWarning = "The scenarios' downside is thinner than the volatility baseline's: 5th percentile 58% below the median, against 75%"

/** The notes an older card carries for the model: the card shows none of them as written. */
export const legacyModelHints = [
  'No horizon_reason: the card says the agent chose 180 days; when the question names no timeframe, say why the horizon fits it',
  'Horizon set from the question ("5y outlook": 1,826 days), not horizon_days 365',
  'No view: pass scenarios to give a direction',
  "Your view's downside is thin: its 5th percentile ($267.38) is 58% below its median ($636.75), against 75% for the volatility baseline: consider a tail scenario",
  'pe 20 cited [1] not found in [1]: labelled assumption',
  "eps 21.33 cited quote not found in this message's get_stock_quote or get_asset_analysis results: labelled assumption",
  'growthPct 12 cited S3 is not a source id ([n] or quote): labelled assumption',
  'level 1100 cited [9] is not a source in this message: labelled assumption',
]

/**
 * "BTC next 3 months" as the API answers it against the market fake (spot $100,000): no scenarios, so the
 * view is the halved historical drift at 92 days; six-figure levels, no multiples (they read <1% under a year).
 * Synthetic numbers from the API's own engine, not BTC's.
 */
export const btcShortView: PriceOutlook = {
  symbol: 'BTC',
  spot: 100000,
  horizonDays: 92,
  volAnnPct: 17,
  buckets: [
    { label: '< $88.8k', lo: null, hi: 88800, p: 0.05 },
    { label: '$88.8k – $95.8k', lo: 88800, hi: 95800, p: 0.2 },
    { label: '$95.8k – $100k', lo: 95800, hi: 100000, p: 0.25 },
    { label: '$100k – $104k', lo: 100000, hi: 104400, p: 0.25 },
    { label: '$104k – $113k', lo: 104400, hi: 112600, p: 0.2 },
    { label: '≥ $113k', lo: 112600, hi: null, p: 0.05 },
  ],
  p5: 87900.12,
  p50: 100000,
  p95: 113770.4,
  volBasis: { window: '90d', days: 90, since: '2026-07-04' },
  horizon: { days: 92, basis: 'explicit', reason: 'From your question: "next 3 months"' },
  history: {
    source: 'Binance daily closes (BTC/USDT)',
    from: '2018-07-16',
    to: '2026-10-01',
    years: 8.2,
    volAnnPct: 16.5,
    cagrPct: { y5: 15.5, y10: null, all: 15 },
    maxDrawdownPct: -14,
    maxDrawdownPeak: '2025-05-24',
    maxDrawdownTrough: '2025-06-15',
    recovered: '2025-07-04',
  },
  view: {
    kind: 'historical_drift',
    label: 'Historical drift since 2018, halved (assumption)',
    muAnnPct: 6.99,
    median: 101777.48,
    p5: 88448.55,
    p25: 96084.64,
    p75: 107807.6,
    p95: 117115.03,
    pAbove: 0.5818,
    changePct: 1.78,
    annualPct: null,
    buckets: [
      { label: '< $80,000', lo: null, hi: 80000, p: 0.0024, pBaseline: 0.0045 },
      { label: '$80,000 – $90,000', lo: 80000, hi: 90000, p: 0.0724, pBaseline: 0.104 },
      { label: '$90,000 – $100,000', lo: 90000, hi: 100000, p: 0.3434, pBaseline: 0.3915 },
      { label: '$100,000 – $110,000', lo: 100000, hi: 110000, p: 0.4005, pBaseline: 0.368 },
      { label: '$110,000 – $130,000', lo: 110000, hi: 130000, p: 0.1792, pBaseline: 0.131 },
      { label: '≥ $130,000', lo: 130000, hi: null, p: 0.0021, pBaseline: 0.0011 },
    ],
    multiples: [
      { x: 0.5, pEnd: 0, pTouch: 0, baselinePEnd: 0, baselinePTouch: 0 },
      { x: 2, pEnd: 0, pTouch: 0, baselinePEnd: 0, baselinePTouch: 0 },
      { x: 5, pEnd: 0, pTouch: 0, baselinePEnd: 0, baselinePTouch: 0 },
      { x: 10, pEnd: 0, pTouch: 0, baselinePEnd: 0, baselinePTouch: 0 },
    ],
    listingSpot: null,
    volAnnPct: 17,
    volAssumed: false,
  },
}

/** The source tsmOutlookView's EPS input cites as [1] (a fake, contract-shaped result). */
export const tsmSource: SourceRef = {
  n: 1,
  title: 'TSMC consensus EPS climbs as AI demand holds',
  url: 'https://news.example.com/tsmc-eps',
  source: 'news.example.com',
  publishedAt: null,
  kind: 'web',
}

/**
 * The card the owner flagged, as the API built it before the price outlook view: TSM over 5 years, zero drift, σ 43.8% from 247 venue
 * closes, mirror-symmetric buckets and no view fields. Copied from that card's numbers.
 */
export const legacyOutlook: PriceOutlook = {
  symbol: 'TSM',
  spot: 483.52,
  horizonDays: 1825,
  volAnnPct: 43.8,
  buckets: [
    { label: '< $111', lo: null, hi: 111, p: 0.0665 },
    { label: '$111 – $232', lo: 111, hi: 232, p: 0.1602 },
    { label: '$232 – $378', lo: 232, hi: 378, p: 0.1751 },
    { label: '$378 – $618', lo: 378, hi: 618, p: 0.1971 },
    { label: '$618 – $1.01k', lo: 618, hi: 1010, p: 0.1751 },
    { label: '$1.01k – $2.10k', lo: 1010, hi: 2100, p: 0.1591 },
    { label: '≥ $2.10k', lo: 2100, hi: null, p: 0.0669 },
  ],
  p5: 96.4379,
  p50: 483.52,
  p95: 2424.2,
  volBasis: { window: 'long-run', days: 247, since: '2026-02-03' },
  longHorizon: {
    multiples: [
      { x: 0.5, pEnd: 0.2396, pTouch: 0.4791 },
      { x: 2, pEnd: 0.2396, pTouch: 0.4791 },
      { x: 5, pEnd: 0.0502, pTouch: 0.1003 },
      { x: 10, pEnd: 0.0094, pTouch: 0.0187 },
    ],
    tenX: [
      { years: 1, pTouch: 0 },
      { years: 3, pTouch: 0.002404 },
      { years: 5, pTouch: 0.018722 },
    ],
    driftCase: null,
  },
}

export const plan = (over: Partial<Plan> = {}): Plan => ({
  symbol: 'BTC',
  style: 'day',
  direction: 'long',
  entry: 84213.2,
  stopLoss: 82850.456,
  takeProfit: 86900.1,
  riskReward: 2,
  confidence: 64,
  horizon: '1-3 days',
  rationale: ['Higher lows on the 1h', 'RSI holding above 50'],
  invalidation: 'A 1h close below $82,800',
  suggestedNotional: 2500.4,
  catalysts: ['ETF inflows extend a third day'],
  interval: '1h',
  higherTrend: { interval: '1d', trend: 'uptrend' },
  desk: null,
  generatedAt: '2026-10-02T08:30:00Z',
  ...over,
})

/** The forecast desk on SOL: a 28% Yes on "above $125" leans No and opposes a long. */
export const deskRead = (over: Partial<DeskRead> = {}): DeskRead => ({
  forecastId: 'f-sol',
  question: 'Will SOL close above $125 on 2026-10-09?',
  resolutionDate: '2026-10-09',
  outcome: 'Yes',
  p: 0.28,
  lean: 'No 72%',
  priceRule: { symbol: 'SOL', op: 'above', threshold: 125, mode: 'on_date' },
  stance: 'opposes',
  ...over,
})

export const news = (over: Partial<NewsItem> = {}): NewsItem => ({
  id: 'n1',
  title: 'Bitcoin ETFs log a third day of inflows',
  url: 'https://www.reuters.com/markets/btc-etf',
  source: 'reuters.com',
  description: 'Spot funds took in $420m.',
  publishedAt: '2026-10-02T07:00:00Z',
  symbols: ['BTC'],
  topic: 'asset',
  regions: ['crypto'],
  hasContent: true,
  ...over,
})

export const brief: MarketBrief = {
  id: 'b1',
  headline: 'Risk-on into the Fed',
  summary: 'Crypto firm as ETF inflows extend.',
  bullets: [
    { text: 'BTC ETF inflows extend', symbols: ['BTC'], sources: ['n1'] },
    { text: 'Gold slips as yields rise', symbols: ['PAXG'], sources: [] },
  ],
  sentiment: [
    { symbol: 'BTC', score: 0.4, label: 'bullish', reason: 'ETF inflows' },
    { symbol: 'PAXG', score: -0.3, label: 'bearish', reason: 'Yields up' },
  ],
  articleCount: 42,
  window: { from: '2026-10-01T08:00:00Z', to: '2026-10-02T08:00:00Z' },
  createdAt: '2026-10-02T08:00:00Z',
}

export const forecastSummary = (over: Partial<ForecastSummary> = {}): ForecastSummary => ({
  id: 'f1',
  shape: 'binary',
  question: 'Will ETH outperform BTC over Q4 2026?',
  category: 'crypto',
  status: 'completed',
  resolutionDate: '2026-12-31',
  createdAt: '2026-10-01T08:00:00Z',
  completedAt: '2026-10-01T08:02:00Z',
  headline: { outcome: 'Yes', p: 0.42 },
  resolvedOutcome: null,
  replay: false,
  ...over,
})

/** Polymarket's odds for the fixture question: desk 62% vs market 55% → edge +7 pts, lean Yes. */
export const marketOdds: MarketOdds = {
  source: 'Polymarket',
  question: 'ETH outperforms BTC in Q4 2026?',
  url: 'https://polymarket.com/event/eth-vs-btc-q4-2026',
  yesPrice: 0.55,
  volumeUsd: 1_200_000,
  endDate: '2026-12-31T23:59:00Z',
  match: 'exact',
  edgePp: 7,
  evYes: 0.07,
  evNo: -0.07,
  signal: 'yes',
  thin: false,
  asOf: '2026-10-02T14:05:00Z',
}

export const forecastResult: ForecastResult = {
  probabilities: [
    { outcome: 'Yes', p: 0.62 },
    { outcome: 'No', p: 0.38 },
  ],
  ensemble: [
    { outcome: 'Yes', p: 0.55 },
    { outcome: 'No', p: 0.45 },
  ],
  specialists: [
    {
      role: 'skeptic',
      name: 'Narrative & macro skeptic',
      probabilities: [
        { outcome: 'Yes', p: 0.48 },
        { outcome: 'No', p: 0.52 },
      ],
      reasoning: 'Consensus is already long ETH beta.',
      keyFactors: ['Crowded positioning'],
      confidence: 'medium',
    },
    {
      role: 'base_rate',
      name: 'Base-rate statistician',
      probabilities: [
        { outcome: 'Yes', p: 0.6 },
        { outcome: 'No', p: 0.4 },
      ],
      reasoning: 'ETH beat BTC in 6 of the last 10 Q4s.',
      keyFactors: ['Seasonality'],
      confidence: 'high',
    },
  ],
  summary: 'ETH has the edge on flows and seasonality.',
  drivers: [
    { factor: 'ETF inflows', direction: 'up', weight: 'high' },
    { factor: 'Rising yields', direction: 'down', weight: 'low' },
  ],
  evidence: [
    { claim: 'ETH ETFs took in $1.2bn in September', source: 'S1', stance: 'supports', asOf: '2026-09-30', url: 'https://a.io/eth' },
    { claim: 'Funding is elevated', source: 'Coinglass', stance: 'opposes', asOf: 'live', url: null },
  ],
  flags: [{ type: 'stale', note: 'One source is a week old.' }],
  whatWouldChange: ['A hawkish Fed surprise'],
  confidence: 'medium',
  knowledgeNote: 'Live web research via Brave Search (2 sources, 1 article read).',
  sources: [
    { id: 'S1', title: 'ETH ETF flows hit a record', url: 'https://a.io/eth', source: 'The Block', publishedAt: '2026-09-30T00:00:00Z', read: true },
    { id: 'S2', title: 'Funding rates climb', url: 'https://b.io/funding', source: 'CoinDesk', publishedAt: null, read: false },
  ],
  research: { queries: ['eth etf flows', 'eth btc ratio'] },
  market: marketOdds,
}

export const forecast = (over: Partial<Forecast> = {}): Forecast => ({
  id: 'f1234567-aaaa',
  shape: 'binary',
  question: 'Will ETH outperform BTC over Q4 2026?',
  outcomes: ['Yes', 'No'],
  resolutionCriteria: 'Resolves Yes if ETH/BTC on Binance closes Dec 31 above its Oct 1 close.',
  resolutionDate: '2026-12-31',
  category: 'crypto',
  status: 'completed',
  error: null,
  result: forecastResult,
  createdAt: '2026-10-02T08:00:00Z',
  completedAt: '2026-10-02T08:02:00Z',
  priceRule: null,
  context: '',
  resolution: null,
  replay: null,
  ...over,
})

/** A historical-replay row: SOL as of 2026-08-12, resolved Yes on 2026-08-26 (the desk said 30%). */
export const replayForecast = (over: Partial<Forecast> = {}): Forecast =>
  forecast({
    id: 'r1234567-bbbb',
    question: 'Will the Binance SOL/USDT daily close on 2026-08-26 be above $191?',
    resolutionCriteria: 'Resolves Yes if the Binance SOLUSDT 1d candle that opens at 00:00 UTC on 2026-08-26 closes above $191; otherwise No.',
    resolutionDate: '2026-08-26',
    priceRule: { symbol: 'SOL', op: 'above', threshold: 191, mode: 'on_date' },
    result: {
      ...forecastResult,
      probabilities: [
        { outcome: 'Yes', p: 0.3 },
        { outcome: 'No', p: 0.7 },
      ],
      knowledgeNote: 'Historical replay as of 2026-08-12: Binance daily data and 8 dated news headlines published before that date; no pages read.',
      market: null,
      reference: { kind: 'volatility', pYes: 0.282, spot: 182.6, volAnnPct: 61.4, horizonDays: 14, asOf: '2026-08-12T00:00:00Z' },
      guard: null,
    },
    createdAt: '2026-08-12T00:07:00Z',
    completedAt: '2026-08-12T00:07:00Z',
    resolution: { outcome: 'Yes', resolvedAt: '2026-08-26T23:59:59.999Z', by: 'auto' },
    replay: { asOf: '2026-08-12', anchorP: 0.282 },
    ...over,
  })

export const replaySummaries: ForecastSummary[] = [
  forecastSummary({
    id: 'r1234567-bbbb',
    question: 'Will the Binance SOL/USDT daily close on 2026-08-26 be above $191?',
    resolutionDate: '2026-08-26',
    createdAt: '2026-08-12T00:07:00Z',
    completedAt: '2026-08-12T00:07:00Z',
    headline: { outcome: 'Yes', p: 0.3 },
    resolvedOutcome: 'Yes',
    replay: true,
  }),
  forecastSummary({
    id: 'r2234567-cccc',
    question: 'Will the Binance BTC/USDT daily close on 2026-07-29 be above $61,100?',
    resolutionDate: '2026-07-29',
    createdAt: '2026-07-15T00:00:00Z',
    completedAt: '2026-07-15T00:00:00Z',
    headline: { outcome: 'Yes', p: 0.73 },
    resolvedOutcome: 'Yes',
    replay: true,
  }),
]

const bins = (counts: number[], meanP: number[], observed: number[]): TrackRecord['bins'] =>
  counts.map((n, i) => ({ lo: i / 5, hi: (i + 1) / 5, n, meanP: n ? meanP[i] : null, observed: n ? observed[i] : null }))

/** The replay record (n = 24) and an empty own record. */
export const trackRecord = (mine: Partial<TrackRecord> = {}): { replay: TrackRecord; mine: TrackRecord } => ({
  replay: {
    n: 24,
    brier: 0.1957,
    baselineBrier: 0.25,
    anchorBrier: 0.1814,
    logLoss: 0.574,
    hitRate: 0.75,
    bins: bins([0, 7, 9, 8, 0], [0, 0.31, 0.5, 0.69, 0], [0, 0.43, 0.78, 1, 0]),
    skill: 0.2172,
    brierCi90: [0.142, 0.251],
    anchorDiffCi90: [-0.021, 0.05],
    anchorN: 24,
    anchorSkill: 1 - 0.1957 / 0.1814,
    claim: { status: 'too_few', basis: null, minN: 50, n: 24, skill: null }, // 24 < 50: no score shown
  },
  mine: {
    n: 0,
    brier: null,
    baselineBrier: null,
    anchorBrier: null,
    logLoss: null,
    hitRate: null,
    bins: bins([0, 0, 0, 0, 0], [], []),
    skill: null,
    brierCi90: null,
    anchorDiffCi90: null,
    anchorN: 0,
    anchorSkill: null,
    claim: { status: 'too_few', basis: null, minN: 50, n: mine.n ?? 0, skill: null },
    ...mine,
  },
})

const DAY = 86400
export const series = (values: number[], start = 1_780_000_000): Point[] => values.map((v, i) => ({ t: start + i * DAY, v }))

const metrics = {
  totalReturnPct: 24.5,
  annualizedReturnPct: 31.2,
  buyHoldReturnPct: 12.1,
  sharpe: 1.42,
  sortino: 2.05,
  volatilityAnnPct: 38.6,
  calmar: 1.7,
  feesPct: 2.31,
  maxDrawdownPct: 18.4,
  winRatePct: 48,
  trades: 23,
  profitFactor: 1.8,
  exposurePct: 41,
  startT: 1_780_000_000,
  endT: 1_790_899_200,
  bars: 1000,
}

/** Independent saved-rule replay; callers choose explicit availability variants. */
export const forward = (over: Partial<StrategyForward> = {}): StrategyForward => {
  const f: StrategyForward = {
    since: '2026-09-01T02:30:00.123Z',
    basis: 'published',
    metrics: { ...metrics, totalReturnPct: 4.2, buyHoldReturnPct: 1.3, trades: 4,
      startT: Date.parse('2026-09-01T00:00:00Z') / 1000, endT: Date.parse('2026-10-04T20:00:00Z') / 1000, bars: 204 },
    early: false,
    asOf: '2026-10-05T00:00:00Z',
    stale: false,
    reason: null,
    equity: [
      { t: Date.parse('2026-09-01T00:00:00Z') / 1000, v: 100 },
      { t: Date.parse('2026-09-01T04:00:00Z') / 1000, v: 100.5 },
      { t: Date.parse('2026-10-04T20:00:00Z') / 1000, v: 104.2 },
    ],
    benchmark: [
      { t: Date.parse('2026-09-01T00:00:00Z') / 1000, v: 100 },
      { t: Date.parse('2026-09-01T04:00:00Z') / 1000, v: 100.2 },
      { t: Date.parse('2026-10-04T20:00:00Z') / 1000, v: 101.3 },
    ],
    trades: [],
    ...over,
  }
  // Representative closed trades stay inside the measured window; unavailable variants have none.
  if (over.trades === undefined && f.metrics) {
    const first = f.equity.find((p) => p.t >= Date.parse(f.since) / 1000)?.t ?? Math.ceil(Date.parse(f.since) / 1000)
    const total = f.metrics.trades
    const step = Math.min(14400, f.equity.length > 1 ? f.equity[1].t - f.equity[0].t : 14400)
    const stride = Math.floor((f.metrics.endT - first) / Math.max(1, total) / step) * step
    const gain = (1 + f.metrics.totalReturnPct / 100) ** (1 / Math.max(1, total)) - 1
    f.trades = Array.from({ length: Math.min(total, 100) }, (_, i) => {
      const entryT = first + (Math.max(0, total - 100) + i) * stride
      return { side: 'long', entryT, exitT: Math.min(entryT + step, f.metrics!.endT), entry: 100, exit: 100 * (1 + gain), pnlPct: gain * 100, reason: 'signal' }
    })
  }
  return f
}

export const strategy = (over: Partial<Strategy> = {}): Strategy => ({
  id: 's1',
  name: 'SOL EMA cross',
  description: 'Trend following on SOL 4h.',
  author: 'Demo Trader',
  prompt: 'Buy SOL when EMA 20 crosses above EMA 50',
  spec: {
    name: 'SOL EMA cross',
    symbol: 'SOL',
    interval: '4h',
    bars: 1000,
    longEntry: { all: [{ left: { kind: 'ema', period: 20 }, op: 'crosses_above', right: { kind: 'ema', period: 50 } }] },
    longExit: { all: [{ left: { kind: 'ema', period: 20 }, op: 'crosses_below', right: { kind: 'ema', period: 50 } }] },
    stopLossPct: 4,
    positionPct: 100,
    feePct: 0.05,
  },
  metrics,
  equity: series([100, 104, 110, 124.5]),
  benchmark: series([100, 102, 108, 112.1]),
  trades: [
    { side: 'long', entryT: 1_780_000_000, exitT: 1_780_086_400, entry: 140, exit: 150, pnlPct: 7.1, reason: 'signal' },
    { side: 'short', entryT: 1_780_172_800, exitT: 1_780_259_200, entry: 150, exit: 156, pnlPct: -4, reason: 'stop_loss' },
  ],
  explanation: 'The strategy **beat** buy & hold with a smaller drawdown.',
  shortSample: false,
  createdAt: '2026-10-01T08:00:00Z',
  ...over,
})

export const strategySummary = (over: Partial<StrategySummary> = {}): StrategySummary => ({
  id: 's1',
  name: 'SOL EMA cross',
  author: 'Demo Trader',
  symbol: 'SOL',
  interval: '4h',
  metrics,
  sparkline: [100, 104, 110, 124.5],
  shortSample: false,
  createdAt: '2026-10-01T08:00:00Z',
  ...over,
})

export const aiPortfolio = (over: Partial<AIPortfolio> = {}): AIPortfolio => ({
  overlap: null,
  id: 'p1',
  name: 'Layer-1 core',
  thesis: 'Majors with lower drawdown than BTC.',
  prompt: 'Layer-1s with less drawdown than BTC',
  holdings: [
    { symbol: 'BTC', weight: 0.5, rationale: 'The anchor.' },
    { symbol: 'ETH', weight: 0.3, rationale: 'Smart-contract beta.' },
    { symbol: 'PAXG', weight: 0.2, rationale: 'Gold hedge.' },
  ],
  backtest: {
    benchmarkSymbol: 'BTC', benchmarkLabel: 'BTC', windowStart: 1_780_000_000, windowEnd: 1_780_000_000 + 180 * 86400, bars: 181,
    series: series(Array.from({ length: 181 }, (_, i) => 100 + i * 0.1)),
    benchmark: series(Array.from({ length: 181 }, (_, i) => 100 + i * 0.05)),
    returnPct: 18,
    benchmarkReturnPct: 9,
    maxDrawdownPct: 7.5,
    volatilityAnnPct: 32,
  },
  createdAt: '2026-10-01T08:00:00Z',
  ...over,
})

/** A shorter actual common daily sample, compared with the saved S&P 500 perp. */
export const stockPortfolio = (over: Partial<AIPortfolio> = {}): AIPortfolio => {
  const start = Math.floor(Date.now() / 86_400_000) * 86400 - 97 * 86400
  return aiPortfolio({ name: 'US stock basket', thesis: 'Paper stock and index perp exposure; selected comparison is S&P 500 perp.', holdings: [{ symbol: 'AAPL', weight: 0.4, rationale: 'Apple.' }, { symbol: 'NVDA', weight: 0.35, rationale: 'NVIDIA.' }, { symbol: 'BTC', weight: 0.25, rationale: 'Crypto allocation.' }], backtest: {
    benchmarkSymbol: 'SP500', benchmarkLabel: 'S&P 500 perp', windowStart: start, windowEnd: start + 96 * 86400, bars: 97,
    series: Array.from({ length: 97 }, (_, i) => ({ t: start + i * 86400, v: 100 + i * 0.1 })), benchmark: Array.from({ length: 97 }, (_, i) => ({ t: start + i * 86400, v: 100 + i * 0.05 })), returnPct: 9.6, benchmarkReturnPct: 4.8, maxDrawdownPct: 0, volatilityAnnPct: 1.2,
  }, ...over })
}

// ── AI backtest arena ───────────────────────────────────────────────────────

export const arenaOptions: ArenaOptions = {
  personas: [
    { id: 'momentum', name: 'Trend Rider', description: 'Holds the strongest trends.' },
    { id: 'contrarian', name: 'Mean Reverter', description: 'Buys oversold large caps.' },
    { id: 'macro', name: 'News Desk', description: 'Trades the headlines.' },
  ],
  knowledgeCutoff: '2026-06-30',
  latestEnd: '2026-10-01',
  cadences: [
    { cadence: '1d', minDays: 30, maxDays: 90, defaultStart: '2026-08-03', defaultEnd: '2026-10-01' },
    { cadence: '4h', minDays: 7, maxDays: 14, defaultStart: '2026-09-18', defaultEnd: '2026-10-01' },
  ],
  maxAgents: 4,
  maxSteps: 90,
  creditsPerAgentStep: 0.5,
}

export const arenaMetrics = (over: Partial<ArenaMetrics> = {}): ArenaMetrics => ({
  totalReturnPct: 4.2,
  annualizedReturnPct: 28.4,
  sharpe: 1.31,
  sortino: 1.92,
  calmar: 3.1,
  maxDrawdownPct: 6.2,
  volatilityAnnPct: 41,
  winRatePct: 55,
  fills: 18,
  turnoverPct: 140,
  exposurePct: 72,
  feesUsd: 312,
  vsBtcPct: 1.5,
  vsBasketPct: 2.1,
  ...over,
})

const ARENA_START = Date.parse('2026-08-03T00:00:00Z') / 1000
const curve = (n: number, k = 1) => Array.from({ length: n + 1 }, (_, i) => ({ t: ARENA_START + i * DAY, v: 100000 + i * 500 * k }))

export const arenaAgent = (over: Partial<ArenaAgent> = {}): ArenaAgent => ({
  index: 0,
  name: 'Trend Rider',
  personaId: 'momentum',
  instruction: '',
  status: 'active',
  rank: 1,
  errors: 0,
  equity: curve(2),
  metrics: arenaMetrics(),
  lastReasoning: 'Rotated into BTC.',
  ...over,
})

export const arenaRun = (over: Partial<ArenaRun> = {}): ArenaRun => ({
  id: 'r1',
  name: 'Daily · Aug 3 → Oct 1, 2026',
  status: 'running',
  error: null,
  cadence: '1d',
  start: '2026-08-03',
  end: '2026-10-01',
  steps: 60,
  stepsDone: 2,
  universe: ['BTC', 'ETH', 'SOL'],
  startingCash: 100000,
  model: 'global.anthropic.claude-sonnet-5-5', // the Bedrock id; the page never shows it
  knowledgeCutoff: '2026-06-30',
  contaminated: false,
  credits: 60,
  agents: [
    arenaAgent(),
    arenaAgent({ index: 1, name: 'Gold bug', personaId: 'custom', instruction: 'Hold PAXG as the core.', rank: 2, equity: curve(2, -1), metrics: arenaMetrics({ totalReturnPct: -1.3, vsBtcPct: -4 }) }),
  ],
  benchmarks: [
    { id: 'btc', name: 'BTC buy & hold', equity: curve(2, 0.5), metrics: arenaMetrics({ totalReturnPct: 2.7, vsBtcPct: 0 }) },
    { id: 'basket', name: 'Equal-weight basket', equity: curve(2, 0.2), metrics: arenaMetrics({ totalReturnPct: 1.1 }) },
  ],
  createdAt: '2026-10-02T08:00:00Z',
  completedAt: null,
  ...over,
})

export const arenaStep = (step: number, over: Partial<ArenaStep> = {}): ArenaStep => ({
  step,
  t: ARENA_START + step * DAY,
  equity: 100000 + (step + 1) * 500,
  cash: 50000,
  positions: [{ symbol: 'BTC', qty: 0.8, avgPrice: 61200, mark: 64120, weightPct: 50 }],
  targets: [{ symbol: 'BTC', weightPct: 50 }],
  fills: [{ symbol: 'BTC', side: 'buy', qty: 0.8, price: 61212.24, notional: 48969.79, fee: 48.97, realizedPnl: -48.97 }],
  tools: [{ name: 'get_news', input: { query: 'ETF flows', days: 2 }, ok: true, summary: '4 headlines' }],
  reasoning: `Step ${step + 1}: bought BTC on the trend.`,
  error: null,
  ...over,
})

export const arenaSummary = (over: Partial<ArenaRunSummary> = {}): ArenaRunSummary => ({
  id: 'r1',
  name: 'Daily · Aug 3 → Oct 1, 2026',
  status: 'completed',
  cadence: '1d',
  start: '2026-08-03',
  end: '2026-10-01',
  steps: 60,
  stepsDone: 60,
  contaminated: false,
  leader: { name: 'Trend Rider', totalReturnPct: 12.4 },
  btcReturnPct: 8.1,
  createdAt: '2026-10-02T08:00:00Z',
  ...over,
})

// ── Agent chat ──────────────────────────────────────────────────────────────
type TradeProposal = import('../lib/types').TradeProposal
type Card = import('../lib/types').Card

export const proposal = (over: Partial<TradeProposal> = {}): TradeProposal => ({
  proposalId: '9b2f0c1e-7d4a-4c1b-9f3e-2a6d8e5c4b10',
  symbol: 'BTC',
  side: 'buy',
  notional: 500,
  price: 84000,
  stopLoss: 80000,
  takeProfit: 92000,
  rationale: 'Trend intact above the 50 SMA.',
  forecast: null,
  status: 'open',
  trade: null,
  ...over,
})

export const forecastCard: Card = {
  type: 'forecast',
  data: { id: 'f1234567-aaaa', question: 'Will ETH outperform BTC over Q4 2026?', shape: 'binary', headline: { outcome: 'Yes', p: 0.62 }, resolutionDate: '2026-12-31' },
}

export const followupsCard: Card = {
  type: 'followups',
  data: { prompts: ['Size this for 1% account risk', 'What would invalidate this?', 'Compare with ETH'] },
}

// ── Signals board & account performance ─────────────────────────────────────
type SignalPick = import('../lib/types').SignalPick
type SignalBoard = import('../lib/types').SignalBoard
type EquityCurve = import('../lib/types').EquityCurve
type SourcePnl = import('../lib/types').SourcePnl

const fromNow = (min: number) => new Date(Date.now() + min * 60_000).toISOString()

/**
 * BTC at 84,213.20 (fx.assets) picked at 80,000 with an 88,000 target and a 72,000 invalidation (2 ATR of 4,000 each
 * way): +5.3% since the pick, 53% of the way.
 */
export const signalPick = (over: Partial<SignalPick> = {}): SignalPick => ({
  symbol: 'BTC',
  side: 'bullish',
  score: 62,
  priceAtPick: 80000,
  pickedAt: fromNow(-14 * 60),
  target: 88000,
  invalidation: 72000,
  validUntil: fromNow((2 * 24 + 5) * 60 + 30), // "Ends in 2d 5h"
  volTier: 'medium',
  factors: { return7dPct: 3.1, return30dPct: 12.4, rsi14: 61, trend: 'uptrend', volAnnPct: 52, drawdownPct: 4.1, sentiment: 0.4 },
  rationale: 'BTC is up 12% in a month and holds its uptrend; ETF inflows lead the wire.',
  headlines: [
    { title: 'Bitcoin ETFs log a third day of inflows', url: 'https://example.com/btc-etf', source: 'Example Wire' },
    { title: 'Miners hold more coins', url: 'https://example.com/btc-miners', source: 'Desk' },
  ],
  ...over,
})

export const signalBoard = (over: Partial<SignalBoard> = {}): SignalBoard => ({
  id: 'sb1',
  createdAt: fromNow(-60),
  nextAt: fromNow(170),
  bullish: [
    signalPick(),
    signalPick({ symbol: 'SOL', score: 41, priceAtPick: 150, target: 165, invalidation: 135, volTier: 'high', validUntil: fromNow(-5), headlines: [],
      factors: { return7dPct: 1, return30dPct: 8.2, rsi14: 55.4, trend: 'range', volAnnPct: 91, drawdownPct: 9, sentiment: null },
      rationale: '30-day +8.2%, RSI 55, range-bound.' }),
  ],
  bearish: [
    signalPick({ symbol: 'ETH', side: 'bearish', score: -38, priceAtPick: 3200, target: 2800, invalidation: 3600, volTier: 'low', headlines: [],
      factors: { return7dPct: -6, return30dPct: -9.5, rsi14: 38, trend: 'downtrend', volAnnPct: 40, drawdownPct: 18, sentiment: -0.3 },
      rationale: 'ETH lost 10% in a month and trades in a downtrend.' }),
  ],
  method:
    'Momentum screen: 7- and 30-day returns, trend and RSI (plus wire sentiment for the 12 core assets), computed in Go every 4 hours. A pick stays until its target (2 ATR in its favour), its invalidation (2 ATR against it) or 72 hours, and every pick is scored at 72 hours against a random pick from the same list. Descriptions by AI.',
  ...over,
})

/**
 * A board stored before steady-v1 (a legacy board), as the API serves it until the first steady refresh replaces it:
 * invalidation 0 on its picks, and the previous rule's method.
 */
export const signalBoardLegacy = (): SignalBoard => {
  const b = signalBoard({ method: 'Momentum (7/30-day), trend, RSI, realized volatility and wire sentiment, computed in Go; rationale by AI. Recomputed every 4 hours.' })
  const zero = (p: SignalPick): SignalPick => ({ ...p, invalidation: 0 })
  return { ...b, bullish: b.bullish.map(zero), bearish: b.bearish.map(zero) }
}

type SignalRecord = import('../lib/types').SignalRecord
type SignalRecordSide = import('../lib/types').SignalRecordSide

/** The replay reference the API serves today (design SIG2 §7.4: C1, main universe, holdout). */
export const signalReplay: SignalRecord['replay'] = {
  from: '2025-12-24', to: '2026-10-10', picks: 864, days: 281, blocks: 97,
  movedItsWayPct: 49.3, randomMovedItsWayPct: 49.3, avgResultPct: -0.12, avgResultNetPct: -0.22, avgRandomPct: -0.09,
  excessPct: -0.03, excessLo95: -0.97, excessHi95: 0.72,
  bullish: { picks: 449, targetFirstPct: 20.3, invalidatedFirstPct: 11.8, expiredPct: 67.9,
    randomTargetFirstPct: 12.1, randomInvalidatedFirstPct: 8.1, excessPct: 0.53, excessLo95: -0.65, excessHi95: 1.38 },
  bearish: { picks: 415, targetFirstPct: 7.2, invalidatedFirstPct: 10.1, expiredPct: 82.7,
    randomTargetFirstPct: 7.5, randomInvalidatedFirstPct: 12.4, excessPct: -0.64, excessLo95: -1.77, excessHi95: 0.39 },
}

const recordSide = (over: Partial<SignalRecordSide> = {}): SignalRecordSide => ({
  scored: 46, blocks: 8, movedItsWayPct: 47.83, randomMovedItsWayPct: 49.12, avgResultPct: -0.31, avgResultNetPct: -0.41,
  avgRandomPct: -0.12, excessPct: -0.19, excessLo99: -1.62, excessHi99: 1.24, targetHit: 7, invalidated: 6, expired: 33,
  verdict: 'insufficient', ...over,
})

/** A young ledger (46 scored picks over 8 blocks): every side "insufficient". */
export const signalRecord = (over: Partial<SignalRecord> = {}): SignalRecord => ({
  rule: 'steady-v1', since: '2026-09-20T00:00:00Z', horizonHours: 72, costPct: 0.1, minPicks: 200, minBlocks: 30, confidence: 0.99,
  open: 5, unscored: 0,
  all: recordSide(),
  bullish: recordSide({ scored: 25, blocks: 8, movedItsWayPct: 48, randomMovedItsWayPct: 48.9, avgResultPct: 0.42, avgResultNetPct: 0.32,
    avgRandomPct: 0.21, excessPct: 0.21, excessLo99: -1.9, excessHi99: 2.32, targetHit: 5, invalidated: 3, expired: 17 }),
  bearish: recordSide({ scored: 21, blocks: 7, movedItsWayPct: 47.62, randomMovedItsWayPct: 49.38, avgResultPct: -1.18, avgResultNetPct: -1.28,
    avgRandomPct: -0.52, excessPct: -0.66, excessLo99: -2.71, excessHi99: 1.39, targetHit: 2, invalidated: 3, expired: 16 }),
  replay: signalReplay,
  ...over,
})

const emptySide = (): SignalRecordSide => ({
  scored: 0, blocks: 0, movedItsWayPct: null, randomMovedItsWayPct: null, avgResultPct: null, avgResultNetPct: null, avgRandomPct: null,
  excessPct: null, excessLo99: null, excessHi99: null, targetHit: 0, invalidated: 0, expired: 0, verdict: 'insufficient',
})

/** The record before the ledger holds any pick. */
export const signalRecordEmpty = (): SignalRecord =>
  signalRecord({ since: null, open: 0, unscored: 0, all: emptySide(), bullish: emptySide(), bearish: emptySide() })

/** The record 30 h after the switch to steady-v1: the first board's 4 picks are waiting for their 72 h score. */
export const signalRecordSwitch = (): SignalRecord =>
  signalRecord({ since: fromNow(-30 * 60), open: 4, unscored: 0, all: emptySide(), bullish: emptySide(), bearish: emptySide() })

export const equityCurve = (over: Partial<EquityCurve> = {}): EquityCurve => ({
  range: '7d',
  interval: '1h',
  startingBalance: 100000,
  points: [
    { t: 1759190400, v: 100000 },
    { t: 1759194000, v: 99950 },
    { t: 1759197600, v: 100120 },
    { t: 1759201200, v: 100194 },
  ],
  ...over,
})

/** Portfolio health of fx.portfolio: one ETH long with a stop. */
export const review = (over: Partial<PortfolioReview> = {}): PortfolioReview => ({
  asOf: '2026-10-05T12:00:00Z',
  revision: 1,
  window: { days: 90, bars: 90 },
  coverage: { modeledGrossExposure: 6000, missingSymbols: [], commonDays: 90, reason: null },
  predictionStakes: 0,
  positions: 1,
  equity: 100194,
  grossExposure: 6000,
  netExposure: 6000,
  exposurePct: 6,
  predictionStakesPct: 0,
  holdings: [{ symbol: 'ETH', side: 'long', value: 6000, pct: 100, beta: 1.18, stopLoss: 2700, halted: false, markAvailable: true, stopStatus: 'valid' }],
  concentration: { level: 'high', top1Pct: 100, top3Pct: 100, hhi: 1, effective: 1 },
  betaBtc: 0.07,
  volAnnPct: 3.9,
  var: { pct: 0.4, usd: 412.5, cvarUsd: 560.2 },
  worstDay: { date: '2026-08-03', pnl: -720.4, pnlPct: -0.7 },
  drawdown30dPct: 1.2,
  stress: [
    { scenario: 'BTC −10%', shockPct: -10, estPnl: -708, estPnlPct: -0.7 },
    { scenario: 'BTC −20%', shockPct: -20, estPnl: -1416, estPnlPct: -1.4 },
    { scenario: 'BTC +10%', shockPct: 10, estPnl: 708, estPnlPct: 0.7 },
  ],
  correlations: [],
  clusters: [],
  stops: { covered: 1, uncovered: [], uncoveredPct: 0, atRiskUsd: 600 },
  notes: [
    'ETH is 100.0% of your exposure.',
    "A 10% BTC drop would cost about $708.00 (0.7% of equity) at the book's beta of 0.07.",
    'Covered stop levels are $600 away before fees and slippage.',
  ],
  ...over,
})

/** Sums to fx.portfolio.totalPnl (194). */
export const attribution: { sources: SourcePnl[]; totalPnl: number } = {
  sources: [
    { source: 'agent', trades: 2, realizedPnl: 150.5, unrealizedPnl: 200, fees: 3.2, totalPnl: 350.5 },
    { source: 'manual', trades: 1, realizedPnl: -6, unrealizedPnl: -150.5, fees: 6, totalPnl: -156.5 },
  ],
  totalPnl: 194,
}

/** A running SOL autopilot, long 0.42 at $142.10; its last decided bar opened 2026-10-02 04:00 UTC. */
export const autopilot = (over: Partial<Autopilot> = {}): Autopilot => ({
  id: 'ap1',
  strategyId: 's1',
  name: 'SOL EMA cross',
  symbol: 'SOL',
  interval: '4h',
  status: 'running',
  stopReason: null,
  allocationUsd: 5000,
  maxDrawdownPct: 10,
  positionQty: 0.42,
  avgPrice: 142.1,
  markPrice: 150,
  equity: 5003.32,
  pnl: 3.32,
  pnlPct: 0.07,
  drawdownPct: 2.5,
  lastBarT: 1_790_913_600,
  nextCheckAt: '2026-10-02T12:00:00Z',
  log: [
    { t: '2026-10-02T08:00:21Z', kind: 'entry', text: 'Opened Long 0.42 SOL at $142.10 · stop $136.42.' },
    { t: '2026-10-02T07:59:40Z', kind: 'deploy', text: 'Deployed with $5,000; checks every closed 4h bar.' },
  ],
  startedAt: '2026-10-02T07:59:40Z',
  stoppedAt: null,
  ...over,
})

// ── Signals history & strategy optimizer ──────────────────────────
type RemovedPick = import('../lib/types').RemovedPick
type OptimizeCell = import('../lib/types').OptimizeCell
type OptimizeResult = import('../lib/types').OptimizeResult

/**
 * Newest exit first (steady-v1 ledger rows): an invalidated pick still waiting for its 72 h score, a target hit and a
 * bearish pick whose 72 h ended with the price 2.5% lower (returnPct +2.5 in its direction).
 */
export const signalHistory: RemovedPick[] = [
  { symbol: 'XRP', side: 'bullish', pickedAt: fromNow(-30 * 60), removedAt: fromNow(-2 * 60), exitAt: fromNow(-3 * 60), priceAtPick: 2.5,
    target: 2.8, invalidation: 2.2, exitPrice: 2.2, returnPct: -12, result72Pct: null, random72Pct: null, scoredAt: null, reason: 'invalidated' },
  { symbol: 'AVAX', side: 'bullish', pickedAt: '2026-09-29T08:00:00Z', removedAt: '2026-10-01T12:00:00Z', exitAt: '2026-10-01T11:00:00Z',
    priceAtPick: 30, target: 33.9, invalidation: 26.1, exitPrice: 33.9, returnPct: 13, result72Pct: 9.84, random72Pct: 1.21,
    scoredAt: '2026-10-02T08:00:00Z', reason: 'target_hit' },
  { symbol: 'DOGE', side: 'bearish', pickedAt: '2026-09-27T04:00:00Z', removedAt: '2026-09-30T04:00:00Z', exitAt: '2026-09-30T04:00:00Z',
    priceAtPick: 0.2, target: 0.18, invalidation: 0.22, exitPrice: 0.195, returnPct: 2.5, result72Pct: 2.5, random72Pct: -0.4,
    scoredAt: '2026-09-30T04:00:00Z', reason: 'expired' },
]

/**
 * How the picks of boards stored before steady-v1 left them (legacy rows, newest first): each reason the API reads from
 * a legacy board chain. invalidation 0, exitAt = the close of exitPrice's 1h bar, no 72 h score keys.
 * AVAX hit its target and DOGE's validity ended on the same board; LINK still cleared the threshold but was pushed out of
 * the four a side; XRP's score fell; ADA had no fresh bar; DOT left a board stored before the reasons were recorded.
 */
// AVAX and DOGE left the same board: one instant, read once (two reads of the clock can fall a millisecond apart and
// put DOGE, listed second, after AVAX).
const sameBoardGone = fromNow(-31 * 60)
const sameBoardExit = fromNow(-31 * 60 - 20)
export const signalHistoryLegacy: RemovedPick[] = [
  { symbol: 'AVAX', side: 'bullish', pickedAt: fromNow(-60 * 60), removedAt: sameBoardGone, priceAtPick: 30, target: 33.5, invalidation: 0,
    exitAt: sameBoardExit, exitPrice: 33.9, returnPct: 13, reason: 'target_hit', legacy: true },
  { symbol: 'DOGE', side: 'bearish', pickedAt: fromNow(-104 * 60), removedAt: sameBoardGone, priceAtPick: 0.2, target: 0.18, invalidation: 0,
    exitAt: sameBoardExit, exitPrice: 0.205, returnPct: -2.5, reason: 'expired', legacy: true },
  { symbol: 'LINK', side: 'bullish', pickedAt: fromNow(-50 * 60), removedAt: fromNow(-35 * 60), priceAtPick: 22, target: 24.5, invalidation: 0,
    exitAt: fromNow(-35 * 60 - 20), exitPrice: 22.4, returnPct: 1.82, reason: 'outranked', legacy: true },
  { symbol: 'XRP', side: 'bullish', pickedAt: fromNow(-52 * 60), removedAt: fromNow(-39 * 60), priceAtPick: 2.5, target: 2.8, invalidation: 0,
    exitAt: fromNow(-39 * 60 - 20), exitPrice: 2.52, returnPct: 0.8, reason: 'score_fell', legacy: true },
  { symbol: 'ADA', side: 'bearish', pickedAt: fromNow(-70 * 60), removedAt: fromNow(-43 * 60), priceAtPick: 0.8, target: 0.72, invalidation: 0,
    exitAt: fromNow(-43 * 60 - 20), exitPrice: 0.79, returnPct: 1.25, reason: 'no_data', legacy: true },
  { symbol: 'DOT', side: 'bullish', pickedAt: fromNow(-5 * 24 * 60), removedAt: fromNow(-4 * 24 * 60), priceAtPick: 4.2, target: 4.6, invalidation: 0,
    exitAt: fromNow(-4 * 24 * 60 - 20), exitPrice: 4.1, returnPct: -2.38, reason: 'left', legacy: true },
]

/**
 * The week after the switch to steady-v1 (30 h ago): the ledger's first exit (XRP, invalidated, its 72 h score still
 * due), then the legacy rows, starting with SOL, which the first steady board did not carry (every legacy pick leaves
 * there: "left"). The ledger holds the first board's picks, none scored yet.
 */
export const signalHistorySwitch = (): RemovedPick[] => [
  signalHistory[0],
  { symbol: 'SOL', side: 'bullish', pickedAt: fromNow(-40 * 60), removedAt: fromNow(-30 * 60), priceAtPick: 150, target: 165, invalidation: 0,
    exitAt: fromNow(-30 * 60 - 20), exitPrice: 151.5, returnPct: 1, reason: 'left', legacy: true },
  ...signalHistoryLegacy,
]

const STOPS = [1, 2, 3, 5, 8]
const TARGETS = [2, 4, 6, 10, 15]
const cell = (stop: number | null, target: number | null, sharpe: number, trades = 12): OptimizeCell => ({
  stopLossPct: stop,
  takeProfitPct: target,
  sharpe,
  totalReturnPct: Math.round(sharpe * 1000) / 100,
  maxDrawdownPct: 10 + (stop ?? 0),
  trades,
})
/** The 5×5 grid around fx.strategy() (4% stop, no target): Stop 3% · Target 10% leads at 1.64; Stop 8% · Target 15% has too few trades. */
export const optimizeResult: OptimizeResult = {
  grid: STOPS.flatMap((s, i) => TARGETS.map((t, j) => cell(s, t, s === 3 && t === 10 ? 1.64 : s === 8 && t === 15 ? 2.5 : Math.round((0.2 + i * 0.1 + j * 0.05) * 100) / 100, s === 8 && t === 15 ? 3 : 12))),
  best: [cell(3, 10, 1.64), cell(8, 10, 0.75), cell(5, 15, 0.7)],
  current: cell(4, null, 1.12),
}

// ── Trade desk: perps context, protection cards, invested portfolios ──
type Derivatives = import('../lib/types').Derivatives
type ProtectionProposal = import('../lib/types').ProtectionProposal
type AIPortfolioInvested = import('../lib/types').AIPortfolioInvested

/** BTC perps on Hyperliquid: +0.0013 %/h funding (11.4% a year), $1.24B open interest, +0.02% premium. */
export const derivatives = (over: Partial<Derivatives> = {}): Derivatives => ({
  symbol: 'BTC',
  venue: 'Hyperliquid',
  markPrice: 84200,
  fundingRatePct: 0.0013,
  fundingAnnualizedPct: 0.0013 * 24 * 365,
  openInterestUsd: 1_240_000_000,
  premiumPct: 0.02,
  volume24hUsd: 2.5e9,
  asOf: '2026-10-02T08:00:00Z',
  ...over,
})

/** Move the ETH long's stop from $2,700 up to $2,850 and add a $3,300 target (mark $3,000). */
export const protection = (over: Partial<ProtectionProposal> = {}): ProtectionProposal => ({
  symbol: 'ETH',
  side: 'long',
  qty: 2,
  markPrice: 3000,
  current: { stopLoss: 2700, takeProfit: null },
  proposed: { stopLoss: 2850, takeProfit: 3300 },
  rationale: 'Trail the stop under the 4h higher low.',
  ...over,
})

/** $10,000 in, now $10,123 (+1.23%) while BTC did +0.8%, since Oct 3. */
export const invested = (over: Partial<AIPortfolioInvested> = {}): AIPortfolioInvested => ({
  benchmarkSymbol: 'BTC', benchmarkLabel: 'BTC', benchmarkReturnPct: 0.8, benchmarkSeries: null, series: null, btcSeries: null, windowStart: null, windowEnd: null, bars: 0,
  amount: 10000,
  trades: 3,
  firstAt: '2026-10-03T09:00:00Z',
  realizedPnl: -10,
  unrealizedPnl: 133,
  totalPnl: 123,
  returnPct: 1.23,
  btcReturnPct: 0.8,
  ...over,
})

// ── Market pulse ──
export const pulse: MarketPulse = { fearGreed: { value: 71, label: 'Greed', asOf: '2026-10-03T00:00:00Z', source: 'alternative.me' } }

// ── Automations ──
/** A $1,000 SOL limit buy at $142.50 (live $150): stop $135, target $165, set from the agent's card. */
export const alert = (over: Partial<Alert> = {}): Alert => ({
  id: 'al1',
  symbol: 'SOL',
  prompt: null,
  thesis: null,
  notifyWhen: null,
  quotaStreak: 0,
  trigger: 'price',
  op: 'below',
  price: 142.5,
  every: null,
  at: null,
  weekday: null,
  nextRunAt: null,
  lastRun: null,
  action: 'buy',
  notional: 1000,
  stopLoss: 135,
  takeProfit: 165,
  note: 'Buy the retest of support.',
  source: 'agent',
  proposalId: '6f1c2c1e-0000-4000-8000-0000000000a1',
  status: 'active',
  tradeId: null,
  error: null,
  firedPrice: null,
  createdAt: '2026-10-02T09:00:00Z',
  firedAt: null,
  ...over,
})

// ── Workflows: schedules ──
/** A $100 BTC buy every Monday at 09:00 UTC that last ran Mon Sep 28. */
export const schedule = (over: Partial<Alert> = {}): Alert =>
  alert({
    id: 'sc1',
    symbol: 'BTC',
    trigger: 'schedule',
    op: null,
    price: null,
    every: 'week',
    at: '09:00',
    weekday: 1,
    nextRunAt: '2026-10-05T09:00:00Z',
    lastRun: { id: 'sr1', at: '2026-09-28T09:00:00Z', status: 'ok', body: 'Bought $100 BTC at $61,240.50', error: null, reportChatId: null, excerpt: null, verdict: null },
    notional: 100,
    stopLoss: null,
    takeProfit: null,
    note: 'DCA',
    source: 'manual',
    proposalId: null,
    ...over,
  })

/** The agent's set_schedule card: buy $100 of BTC every Monday at 09:00 UTC. */
export const scheduleCard = (over: Partial<PriceAlertProposal> = {}): PriceAlertProposal => ({
  proposalId: '6f1c2c1e-0000-4000-8000-0000000000b4',
  symbol: 'BTC',
  trigger: 'schedule',
  op: null,
  price: null,
  markPrice: null,
  distancePct: null,
  every: 'week',
  at: '09:00',
  weekday: 1,
  action: 'buy',
  notional: 100,
  stopLoss: null,
  takeProfit: null,
  note: 'Dollar-cost average into BTC.',
  ...over,
})

export const researchRun = (over: Partial<AlertRun> = {}): AlertRun => ({
  id: 'rr1', at: '2026-10-02T08:00:00Z', status: 'ok', body: 'Current evidence supports ETF demand [1].', error: null,
  reportChatId: 'research-chat', excerpt: 'Current evidence supports ETF demand [1].', verdict: 'holds', ...over,
})

export const researchSchedule = (over: Partial<Alert> = {}): Alert => schedule({
  id: 'research1', symbol: 'BTC', action: 'research', notional: null, every: 'weekdays', at: '08:00', weekday: null,
  prompt: 'Review BTC demand and current market catalysts with sources.',
  thesis: { statement: 'BTC demand supports the uptrend.', invalidation: 'ETF demand reverses for a week.' },
  notifyWhen: 'always', note: 'Research history', lastRun: null, ...over,
})

export const researchCard = (over: Partial<PriceAlertProposal> = {}): PriceAlertProposal => scheduleCard({
  action: 'research', notional: null, every: 'weekdays', at: '08:00', weekday: null,
  prompt: 'Review market catalysts with current sources.', notifyWhen: 'always', note: 'Recurring research', ...over,
})

/** The agent's card for fx.alert(): −5% from the $150 mark. */
export const priceAlert = (over: Partial<PriceAlertProposal> = {}): PriceAlertProposal => ({
  proposalId: '6f1c2c1e-0000-4000-8000-0000000000a1',
  symbol: 'SOL',
  op: 'below',
  price: 142.5,
  markPrice: 150,
  distancePct: -5,
  action: 'buy',
  notional: 1000,
  stopLoss: 135,
  takeProfit: 165,
  note: 'Buy the retest of support.',
  ...over,
})

export const notification = (over: Partial<Notification> = {}): Notification => ({
  id: 'alert:al2',
  kind: 'alert',
  title: 'SOL fell below $145.00',
  body: 'Now $144.80 · Dip zone',
  symbol: 'SOL',
  link: '/automations',
  at: '2026-10-02T14:05:00Z',
  ...over,
})

export const notifications: Notification[] = [
  notification(),
  notification({
    id: 'stop_loss:t9',
    kind: 'stop_loss',
    title: 'Stop loss hit · ETH',
    body: 'Sold 2 ETH at $2,700.00 · realized −$412.40',
    symbol: 'ETH',
    link: '/account',
    at: '2026-10-02T12:00:00Z',
  }),
  notification({
    id: 'forecast_completed:f1',
    kind: 'forecast_completed',
    title: 'Forecast ready · Yes 28%',
    body: 'Will BTC close above $150,000 on Dec 31, 2026?',
    symbol: null,
    link: '/forecasts/f1',
    at: '2026-10-01T10:00:00Z',
  }),
]

// ── Research provenance ──

export const wireNote = 'Live search found no usable sources for this run; the panel used the stored news wire (last 72 h).'

// ── Community strategies, AI portfolio actual vs BTC ── begin
type Community = NonNullable<Strategy['community']>

/** An own, unpublished strategy's community info (the API sets it on every saved strategy). */
export const community = (over: Partial<Community> = {}): Community => ({
  published: false,
  publishedAt: null,
  owner: 'Demo Trader',
  mine: true,
  stars: 0,
  starred: false,
  copies: 0,
  deploys: 0,
  copiedFrom: null,
  ...over,
})

/** Another user's published strategy as the board and its page show it: 4 stars, 2 copies, 1 deploy. */
export const publishedBy = (owner = 'Ann Lee', over: Partial<Community> = {}) =>
  community({ owner, mine: false, published: true, publishedAt: '2026-10-02T09:00:00Z', stars: 4, copies: 2, deploys: 1, ...over })

/** Since-invest series: hourly from the first buy, the paper basket a touch under 100 after fees, BTC rebased to 100. */
export const sinceInvest = {
  windowStart: 1_790_928_000, windowEnd: 1_790_938_800, bars: 4,
  benchmarkSeries: [100, 100.2, 100.5, 100.8].map((v, i) => ({ t: 1_790_928_000 + i * 3600, v })),
  series: [99.86, 100.4, 101.2, 100.9].map((v, i) => ({ t: 1_790_928_000 + i * 3600, v })),
  btcSeries: [100, 100.2, 100.5, 100.8].map((v, i) => ({ t: 1_790_928_000 + i * 3600, v })),
}
// ── Community strategies ── end

// ── Desk vs market by strike ── begin
export const ladderBuckets = ['< $80,000', '$80,000 – $90,000', '$90,000 – $100,000', '≥ $100,000']

/** One row of Polymarket's Oct 9 Bitcoin ladder: its price against the desk's P(above) (the edge). */
export const ladderRow = (strike: number, yesPrice: number, edgePp: number, signal: MarketOdds['signal'], volumeUsd = 50_000): MarketOdds => ({
  ...marketOdds,
  question: `Will the price of Bitcoin be above $${strike.toLocaleString('en-US')} on October 9?`,
  url: 'https://polymarket.com/event/bitcoin-above-on-october-9-2026',
  yesPrice,
  volumeUsd,
  endDate: '2026-10-09T16:00:00Z',
  match: 'exact',
  edgePp,
  evYes: Math.round(edgePp) / 100,
  evNo: -Math.round(edgePp) / 100,
  signal,
  thin: volumeUsd < 1000,
  strike,
})

/** An ordered BTC report (desk P(above): $80k 90%, $85k 75%, $90k 60%, $95k 40%) next to the ladder: NO, WAIT, YES, thin. */
export const ladderForecast = (over: Partial<Forecast> = {}): Forecast =>
  forecast({
    id: 'f-ladder',
    shape: 'ordered',
    question: 'Where will the Binance BTC/USDT daily close be on 2026-10-09?',
    outcomes: ladderBuckets,
    resolutionCriteria: 'Resolves to the bucket holding the Binance BTCUSDT 1d close for 2026-10-09 (UTC).',
    resolutionDate: '2026-10-09',
    result: {
      ...forecastResult,
      probabilities: ladderBuckets.map((outcome, i) => ({ outcome, p: [0.1, 0.3, 0.4, 0.2][i] })),
      ensemble: ladderBuckets.map((outcome) => ({ outcome, p: 0.25 })),
      market: null,
      ladder: [ladderRow(80000, 0.95, -5, 'no'), ladderRow(85000, 0.725, 2.5, 'wait'), ladderRow(90000, 0.5, 10, 'yes'), ladderRow(95000, 0.385, 1.5, 'wait', 400)],
    },
    ...over,
  })
// ── Desk vs market by strike ── end

// ── Prediction-market odds triggers ── begin
/** An odds trigger on fx.forecast()'s matched market (Polymarket 55%, desk 62%): notify when it rises above 62%. */
export const oddsAlert = (over: Partial<Alert> = {}): Alert =>
  alert({
    id: 'od1',
    symbol: '',
    trigger: 'odds',
    op: 'above',
    price: 0.62,
    action: 'notify',
    notional: null,
    stopLoss: null,
    takeProfit: null,
    note: '',
    source: 'manual',
    proposalId: null,
    forecastId: 'f1234567-aaaa',
    market: { question: marketOdds.question, url: marketOdds.url, outcome: null, yesPrice: 0.55, asOf: '2026-10-02T14:05:00Z' },
    ...over,
  })

/** The agent's set_odds_alert card for the same market: above 62% (the desk), the market at 55%. */
export const oddsCard = (over: Partial<PriceAlertProposal> = {}): PriceAlertProposal => ({
  proposalId: '6f1c2c1e-0000-4000-8000-0000000000c5',
  symbol: '',
  trigger: 'odds',
  forecastId: 'f1234567-aaaa',
  market: { question: marketOdds.question, url: marketOdds.url, outcome: null },
  op: 'above',
  price: 0.62,
  markPrice: 0.55,
  distancePct: null,
  action: 'notify',
  notional: null,
  stopLoss: null,
  takeProfit: null,
  note: "When the market reaches the desk's number.",
  ...over,
})

// ── Drawdown triggers: test values, not market data ──
const solHigh = {
  ath: 250, athAt: '2025-01-19T00:00:00Z', athLive: false, coveredFrom: '2020-08-11T00:00:00Z', source: 'binance' as const,
  sourceLabel: 'Binance SOLUSDT daily highs since 2020-08-11', ticker: 'SOLUSDT', currency: 'USD', basis: 'mark' as const,
}

/** GET /api/alerts/ath for SOL: a $250 high on Jan 19, 2025; the $150 mark is 40% below it. */
export const athQuote = (over: Partial<AthQuote> = {}): AthQuote => ({ symbol: 'SOL', ...solHigh, price: 150, fromAthPct: -40, ...over })

/** An active drawdown trigger: sell 100% of the SOL long 50% below its high (at $125). */
export const drawdownAlert = (over: Partial<Alert> = {}, dd: Partial<Drawdown> = {}): Alert =>
  alert({
    id: 'dd1', trigger: 'drawdown', op: null, price: null, action: 'sell', notional: null, stopLoss: null, takeProfit: null,
    note: 'Cut on a deep drawdown', source: 'manual', proposalId: null,
    drawdown: { pct: 50, sellPct: 100, ...solHigh, threshold: 125, outcome: null, price: 150, fromAthPct: -40, ...dd },
    ...over,
  })

/** The agent's set_drawdown_alert card for drawdownAlert(): 50% below the $250 high, sell 100% of the long. */
export const drawdownCard = (over: Partial<PriceAlertProposal> = {}): PriceAlertProposal => ({
  proposalId: '6f1c2c1e-0000-4000-8000-0000000000d6',
  symbol: 'SOL',
  trigger: 'drawdown',
  op: null,
  price: null,
  markPrice: 150,
  distancePct: -16.67,
  action: 'sell',
  notional: null,
  stopLoss: null,
  takeProfit: null,
  note: 'Protect the position on a deep drawdown.',
  drawdownPct: 50,
  sellPct: 100,
  ath: solHigh,
  threshold: 125,
  fromAthPct: -40,
  ...over,
})

export const drawdownNotification = notification({
  id: 'drawdown_alert:dd1',
  kind: 'drawdown_alert',
  title: 'SOL fell 50% below its all-time high',
  body: 'Now $124.80 · ATH $250.00 · Nothing to sell: no paper long',
  symbol: 'SOL',
  link: '/automations?tab=history&alert=dd1',
})

export const oddsNotification = notification({
  id: 'odds_alert:od1',
  kind: 'odds_alert',
  title: 'Polymarket moved: "ETH outperforms BTC in Q4 2026?" rose above 62%',
  body: 'Now 62.5% · desk 62%',
  symbol: null,
  link: '/forecasts/f1234567-aaaa',
})
// ── Prediction-market odds triggers ── end

export const liveDesk = (over: Partial<import('../lib/types').LiveDesk> = {}): import('../lib/types').LiveDesk => ({
  enabled: true, status: 'live', startedAt: '2026-10-01T00:00:00Z', lastStepAt: '2026-10-05T08:00:00Z', nextStepAt: '2026-10-05T12:00:00Z',
  cadence: '4h', startingCash: 100000, universe: ['BTC', 'ETH', 'SOL'], steps: 26, method: 'Closed bars and stored market wire; long-only paper fills.',
  lastError: null, stale: false, historyStartAt: '2026-10-01T00:00:00Z', historyLimited: false, chartWindowBars: 360, ...over,
})
export const liveAgent = (over: Partial<import('../lib/types').LiveAgent> = {}): import('../lib/types').LiveAgent => ({
  id: 'live-momentum', personaId: 'momentum', name: 'Trend Rider', mandate: 'Follow strong trends, keep cash when conviction is low.', status: 'active', benchedUntil: null,
  rank: 1, errors: 0, steps: 26, equity: [{ t: 1790812800, v: 100000 }, { t: 1791187200, v: 101000 }], cash: 95000, equityNow: 101000,
  positions: [{ symbol: 'BTC', qty: 0.1, avgPrice: 59000, mark: 60000, marketValue: 6000, unrealizedPnl: 100, weightPct: 5.94 }],
  metrics: arenaMetrics({ totalReturnPct: 1 }), lastReasoning: 'Trend remains firm.', lastStepAt: '2026-10-05T08:00:00Z', lastError: null, followers: 2, ...over,
})
export const copyPlan = (over: Partial<import('../lib/types').CopyPlan> = {}): import('../lib/types').CopyPlan => ({
  id: 'cp1', agentId: 'live-momentum', agentName: 'Trend Rider', personaId: 'momentum', ratio: 0.1, maxUsd: 5000, status: 'active', stopReason: null,
  positions: [], deployedUsd: 0, invested: 0, trades: 0, realizedPnl: 0, unrealizedPnl: 0, pnl: 0,
  log: [{ t: '2026-10-05T09:00:00Z', kind: 'start', text: 'Copying future fills only.' }], createdAt: '2026-10-05T09:00:00Z', stoppedAt: null, ...over,
})
export const liveBoard = (over: Partial<import('../lib/types').LiveBoard> = {}): import('../lib/types').LiveBoard => ({
  desk: liveDesk(), agents: [liveAgent(), liveAgent({ id: 'live-contrarian', personaId: 'contrarian', name: 'Mean Reverter', rank: 2 }), liveAgent({ id: 'live-macro', personaId: 'macro', name: 'News Desk', rank: 3 })],
  benchmarks: [{ id: 'btc', name: 'BTC buy & hold', equity: [{ t: 1790812800, v: 100000 }, { t: 1791187200, v: 100500 }], metrics: arenaMetrics() }], myPlans: [], ...over,
})

// ── Asia screener ────────────────────────────────────────────────────────────
// As of Wednesday 2026-10-14 07:10 UTC (tests that read freshness fix the clock at SCREENER_NOW): TSE, KRX and TWSE
// have closed for the day; HKEX, SGX and IDX are open. The averages are from Tuesday's 10:45 UTC run.
export const SCREENER_NOW = '2026-10-14T07:10:00Z'
/** A quote the screener tier read after KRX's close (held to the settle read); a row built with it takes its price. */
export const screenerQuote = (over: Partial<ScreenerQuote> = {}): ScreenerQuote => ({
  source: 'yahoo', tier: 'screener', price: 272000, currency: 'KRW', lastTradeAt: '2026-10-14T06:30:00Z', readAt: '2026-10-14T06:55:04Z',
  delayMinutes: 20, delayBasis: 'measured', session: 'closed', refreshSeconds: null, staleAt: '2026-10-14T07:32:00Z', stale: false, ...over,
})
/** An open session's screener-tier quote: read every 15 minutes, stale 2 minutes after the next read is due. */
const openQuote = (over: Partial<ScreenerQuote>) =>
  screenerQuote({ session: 'open', refreshSeconds: 900, readAt: '2026-10-14T07:00:04Z', staleAt: '2026-10-14T07:17:04Z', ...over })
const tseClose = screenerQuote({ lastTradeAt: '2026-10-14T06:30:00Z', delayMinutes: 15, readAt: '2026-10-14T06:52:10Z' })
const nikkei = { index: 'Nikkei 225', source: 'https://indexes.nikkei.co.jp/nkave/archives/file/nikkei_stock_average_weight_en.csv', sourceKind: 'official', asOf: '2026-10-01' } as const
/** A row as the live API sends it: its quote (KRX's close unless `quote` is given) carries the row's price and currency. */
export const screenerRow = (over: Partial<ScreenerRow> = {}): ScreenerRow => {
  const r: ScreenerRow = {
    ticker: '005930.KS', code: '005930', name: 'Samsung Electronics Co., Ltd.', listName: 'Samsung Electronics', market: 'KR',
    indices: [{ index: 'KOSPI 200', source: 'https://en.wikipedia.org/wiki/KOSPI_200', sourceKind: 'secondary', asOf: '2026-10-06' }],
    currency: 'KRW', status: 'ok', statusNote: null,
    price: 272000, priceAt: '2026-10-14T06:30:00Z', priceUsd: 203.22, fx: { pair: 'KRWUSD=X', usdPerUnit: 0.000747133, at: '2026-10-14T07:00:35Z' },
    weeks: 1397, sma200w: 104457.75, pctVsSma: 160.39, pctVsSmaPrev: 166.93, direction: 'deepening', rsi14w: 54.22,
    ath: 374500, athAt: '2026-06-15', athFrom: '2000-01-03', drawdownPct: -27.37,
    updatedAt: '2026-10-13T10:52:00Z', checkedAt: '2026-10-13T10:52:00Z',
    tradableSymbol: 'SMSN', tradeVia: 'listing', tradeNote: 'KRX 005930 common share, KRW→USD',
    ...over,
  }
  const q = 'quote' in over ? over.quote : r.price != null ? screenerQuote() : null
  return { ...r, quote: q ? { ...q, price: r.price ?? q.price, currency: r.currency } : null }
}
/** The same row as the API sent it before the live screener: no quote, the run's prices. */
export const screenerRowBeforeLive = (r: ScreenerRow): ScreenerRow => {
  const { quote: _quote, ...rest } = r
  void _quote
  return rest
}
const untradable = { tradableSymbol: null, tradeVia: null, tradeNote: null }
const empty = { price: null, priceAt: null, priceUsd: null, fx: null, weeks: null, sma200w: null, pctVsSma: null, pctVsSmaPrev: null,
  direction: null, rsi14w: null, ath: null, athAt: null, athFrom: null, drawdownPct: null, updatedAt: null }
export const screenerRows: ScreenerRow[] = [
  screenerRow({ ticker: 'BBCA.JK', code: 'BBCA', name: 'PT Bank Central Asia Tbk', listName: 'Bank Central Asia Tbk.', market: 'ID', currency: 'IDR',
    indices: [{ index: 'IDX LQ45', source: 'https://stockwatch.id/lq45', sourceKind: 'secondary', asOf: '2026-08-03' }],
    price: 6100, priceAt: '2026-10-14T06:58:41Z', priceUsd: 0.3407, fx: { pair: 'IDRUSD=X', usdPerUnit: 0.0000558503, at: '2026-10-14T07:00:16Z' }, weeks: 1166,
    sma200w: 8622.62, pctVsSma: -29.26, pctVsSmaPrev: -29.36, direction: 'recovering', rsi14w: 42.48, ath: 10950, athAt: '2024-09-23', drawdownPct: -44.29,
    ...untradable, quote: openQuote({ lastTradeAt: '2026-10-14T06:58:41Z', delayMinutes: 10 }) }),
  screenerRow({ ticker: '0000.T', code: '0000', name: 'Sample Deep Value Co.', listName: 'SAMPLE DEEP VALUE CO.', market: 'JP', currency: 'JPY', indices: [nikkei],
    price: 1500, priceUsd: 9.49, fx: { pair: 'JPYUSD=X', usdPerUnit: 0.00632819, at: '2026-10-14T07:00:43Z' }, weeks: 1200, sma200w: 3400,
    pctVsSma: -55.88, pctVsSmaPrev: -54.1, direction: 'deepening', rsi14w: 24.6, ath: 5200, athAt: '2021-03-01', drawdownPct: -71.15, ...untradable, quote: tseClose }),
  screenerRow(),
  // A new high from the closing price: the high is the price, dated by its trading day, drawdown 0.
  screenerRow({ ticker: '2330.TW', code: '2330', name: 'Taiwan Semiconductor Manufacturing Company Limited', listName: 'Taiwan Semiconductor Manufacturing',
    market: 'TW', currency: 'TWD', indices: [{ index: 'FTSE TWSE Taiwan 50', source: 'https://research.ftserussell.com/tw50', sourceKind: 'official', asOf: '2026-09-21' }],
    price: 2600, priceAt: '2026-10-14T05:30:00Z', priceUsd: 81.81, fx: { pair: 'TWDUSD=X', usdPerUnit: 0.0314663, at: '2026-10-14T07:00:41Z' }, sma200w: 1123.54,
    pctVsSma: 131.41, pctVsSmaPrev: 124.61, direction: 'recovering', rsi14w: 72.93, ath: 2600, athAt: '2026-10-14', drawdownPct: 0,
    tradableSymbol: 'TSM', tradeVia: 'adr', tradeNote: 'NYSE: TSM ADS = 5 TWSE 2330 shares',
    quote: screenerQuote({ lastTradeAt: '2026-10-14T05:30:00Z', readAt: '2026-10-14T06:31:02Z', staleAt: '2026-10-15T01:02:00Z' }) }),
  // Stale: SGX is open, but nothing newer was read after 06:35 (the next read was due at 06:50).
  screenerRow({ ticker: 'D01.SI', code: 'D01', name: 'DFI Retail Group Holdings Limited', listName: 'DFI Retail Group', market: 'SG', currency: 'USD',
    indices: [{ index: 'Straits Times Index', source: 'https://research.ftserussell.com/sti', sourceKind: 'official', asOf: '2026-09-21' }],
    price: 3.15, priceAt: '2026-10-14T06:24:30Z', priceUsd: 3.15, fx: null, sma200w: 2.85, pctVsSma: 10.38, pctVsSmaPrev: 8.68, direction: 'recovering', rsi14w: 36.63,
    ath: 13.88, athAt: '2013-02-04', drawdownPct: -77.31, ...untradable,
    quote: openQuote({ lastTradeAt: '2026-10-14T06:24:30Z', delayMinutes: 10, readAt: '2026-10-14T06:35:01Z', staleAt: '2026-10-14T06:52:01Z', stale: true }) }),
  screenerRow({ ticker: '285A.T', code: '285A', name: 'Kioxia Holdings Corporation', listName: 'KIOXIA HOLDINGS CORP.', market: 'JP', currency: 'JPY', indices: [nikkei],
    status: 'insufficient_history', statusNote: '95 weeks of history; the 200-week average needs 200', price: 18740, priceUsd: 118.59,
    fx: { pair: 'JPYUSD=X', usdPerUnit: 0.00632819, at: '2026-10-14T07:00:43Z' }, weeks: 95, sma200w: null, pctVsSma: null, pctVsSmaPrev: null, direction: null,
    rsi14w: 53.19, ath: 37570, athAt: '2026-06-22', athFrom: '2024-12-16', drawdownPct: -50.12, ...untradable, quote: tseClose }),
  // The last run failed for it: the price is live, the averages are the run before's.
  screenerRow({ ticker: '0700.HK', code: '00700', name: 'Tencent Holdings Limited', listName: 'TENCENT', market: 'HK', currency: 'HKD',
    indices: [{ index: 'Hang Seng Index', source: 'https://www.aastocks.com/hsi', sourceKind: 'secondary', asOf: '2026-10-06' }],
    status: 'failed', statusNote: 'rate limited by Yahoo', price: 428.2, priceAt: '2026-10-14T06:51:12Z', priceUsd: 54.56,
    fx: { pair: 'HKDUSD=X', usdPerUnit: 0.127425, at: '2026-10-14T07:00:00Z' }, sma200w: 425.53, pctVsSma: 0.63, pctVsSmaPrev: -0.88, direction: 'recovering',
    rsi14w: 42.45, ath: 714.9, athAt: '2021-02-15', drawdownPct: -40.1, updatedAt: '2026-10-12T10:51:00Z', checkedAt: '2026-10-13T10:51:00Z', ...untradable,
    quote: openQuote({ lastTradeAt: '2026-10-14T06:51:12Z', delayMinutes: 15 }) }),
  screenerRow({ ticker: '1332.T', code: '1332', name: 'NISSUI CORP.', listName: 'NISSUI CORP.', market: 'JP', currency: 'JPY', indices: [nikkei],
    status: 'pending', statusNote: null, ...empty, checkedAt: null, ...untradable }),
  screenerRow({ ticker: 'SM.PS', code: 'SM', name: 'SM Investments', listName: 'SM Investments', market: 'PH', currency: 'PHP',
    indices: [{ index: 'PSEi', source: 'https://en.wikipedia.org/wiki/PSE_Composite_Index', sourceKind: 'secondary', asOf: '2026-10-06' }],
    status: 'unavailable', statusNote: 'Yahoo serves no data for this ticker', ...empty, ...untradable,
    tradeNote: 'No keyless Yahoo data for this listing (Yahoo answered 404 on 2026-10-13)' }), // why it is research only (contract §screener)
]
/** A listed Asian stock's screener row: tradable under its own ticker (tradeVia listing), priced by the Trade page's quote. */
export const listedScreenerRow: ScreenerRow = screenerRow({ ticker: '6501.T', code: '6501', name: 'Hitachi, Ltd.', listName: 'HITACHI, LTD.', market: 'JP',
  currency: 'JPY', indices: [nikkei], price: 4726, priceUsd: 29.87, fx: { pair: 'JPYUSD=X', usdPerUnit: 0.00632083, at: '2026-10-14T07:00:43Z' }, weeks: 1240,
  sma200w: 2910.5, pctVsSma: 62.38, pctVsSmaPrev: 60.11, direction: 'recovering', rsi14w: 51.6, ath: 5002, athAt: '2026-09-21', drawdownPct: -5.52,
  tradableSymbol: '6501.T', tradeVia: 'listing', tradeNote: 'TSE: 6501 (Nikkei 225), JPY→USD at Yahoo FX',
  quote: screenerQuote({ ...tseClose, tier: 'trade', readAt: '2026-10-14T06:58:00Z', refreshSeconds: 900, staleAt: '2026-10-14T07:15:00Z' }) })
export const SCREENER_SCHEDULE = "Every weekday at 10:45 UTC, after the last Asian close (SET's closing auction ends about 09:40 UTC), and at " +
  'boot when no run completed in the last day; a failed run is retried after 3 hours.' // the API's
export const screenerPage = (over: Partial<ScreenerPage> = {}): ScreenerPage => ({
  rows: screenerRows,
  nextCursor: null,
  total: screenerRows.length,
  counts: { all: 9, below: 1, deep: 1, distressed: 0, approaching: 2, oversold: 1 },
  markets: [
    { code: 'JP', name: 'Japan', count: 225 }, { code: 'KR', name: 'South Korea', count: 200 }, { code: 'TW', name: 'Taiwan', count: 50 },
    { code: 'HK', name: 'Hong Kong', count: 95 }, { code: 'SG', name: 'Singapore', count: 30 }, { code: 'ID', name: 'Indonesia', count: 45 },
    { code: 'TH', name: 'Thailand', count: 50 }, { code: 'MY', name: 'Malaysia', count: 30 }, { code: 'PH', name: 'Philippines', count: 30 },
    { code: 'VN', name: 'Vietnam', count: 30 },
  ],
  asOf: '2026-10-13T10:52:00Z',
  run: { status: 'completed', startedAt: '2026-10-13T10:45:00Z', finishedAt: '2026-10-13T10:59:12Z', tickers: 785, done: 785, ok: 690, insufficient: 60,
    unavailable: 30, failed: 5, error: null, durationSeconds: 852, yahooCalls: 794 },
  lastCompletedAt: '2026-10-13T10:59:12Z',
  nextRunAt: '2026-10-14T10:45:00Z',
  source: {
    prices: "each listing's latest regular-market quote from Yahoo Finance (keyless, unofficial and delayed: each row's quote says by how much), " +
      "read every 1 to 15 minutes while its market is open. Averages: each listing's lifetime weekly bars from Yahoo's chart API, split-adjusted, " +
      'in its local currency, read by the latest run',
    fx: "Yahoo Finance {CCY}USD=X: the rate the quote feeds hold now (its newest daily close), else the run's",
    lists: [
      { index: 'Nikkei 225', market: 'JP', count: 225, asOf: '2026-10-01', sourceKind: 'official', sources: [nikkei.source] },
      { index: 'PSEi', market: 'PH', count: 30, asOf: '2026-10-06', sourceKind: 'secondary', sources: ['https://en.wikipedia.org/wiki/PSE_Composite_Index'] },
    ],
  },
  method: "% vs 200WMA = (the quote's price ÷ the latest run's mean of the last 200 weekly closes − 1) × 100. Recomputed every weekday at 10:45 UTC.",
  pollSeconds: 60,
  schedule: SCREENER_SCHEDULE,
  ...over,
})
/** The page as the API sent it before the live screener: run prices, Saturday runs, no in-place refresh. */
export const screenerPageBeforeLive = (over: Partial<ScreenerPage> = {}): ScreenerPage => {
  const { pollSeconds: _poll, schedule: _schedule, ...page } = screenerPage(over)
  void _poll
  void _schedule
  const { durationSeconds: _d, yahooCalls: _c, ...run } = page.run ?? ({} as NonNullable<ScreenerPage['run']>)
  void _d
  void _c
  return {
    ...page,
    rows: page.rows.map(screenerRowBeforeLive),
    run: page.run && run,
    source: {
      ...page.source,
      prices: "Yahoo Finance chart API (keyless, unofficial): each listing's lifetime weekly bars, split-adjusted, in its local currency",
      fx: "Yahoo Finance {CCY}USD=X: the pair's newest daily close when the row was computed",
    },
    method: '% vs 200WMA = (price ÷ mean of the last 200 weekly closes − 1) × 100. Recomputed every Saturday 02:00 UTC.',
    nextRunAt: '2026-10-17T02:00:00Z',
  }
}

// ── Deep research: an ordered IPO-opening report with a findings ledger, and a run mid-research ──

export const deepBuckets = ['Below S$1.70', 'S$1.70 – S$1.79', 'S$1.80 – S$1.89', 'S$1.90 – S$1.99', 'S$2.00 or higher']

const src = (id: string, title: string, source: string, publishedAt: string | null, read: boolean): ForecastSource => ({
  id, title, url: `https://${source.toLowerCase().replace(/[^a-z]+/g, '-')}.example/${id.toLowerCase()}`, source, publishedAt, read,
})

export const deepSources: ForecastSource[] = [
  src('S1', 'Semiconductor firm Orchid Materials lodges preliminary prospectus to raise an indicative S$126 million', 'The Business Times', '2026-10-05T00:00:00Z', true),
  src('S2', 'Orchid Materials launches S$150m IPO', 'IFR', '2026-10-05T00:00:00Z', true),
  src('S3', 'JustCo slips below offer price in SGX debut', 'The Straits Times', '2026-05-22T00:00:00Z', true),
  src('S4', 'AI-powered customer experience platform Toku opens trading on SGX', 'SGX Group', '2026-01-22T00:00:00Z', false),
  src('S5', 'Foundation Healthcare opens 0.7% above IPO price in SGX debut', 'The Straits Times', '2026-07-08T00:00:00Z', true),
  src('S6', '兰花材料拟新交所主板上市：半导体材料企业', 'Lianhe Zaobao', '2026-10-06T00:00:00Z', true),
  src('S7', 'Regulatory Notice 8.2.1: trading hours, market phases and principles for trade matching', 'SGX Rulebooks', null, true),
  src('S8', 'Samsung earnings kick off key October for chip industry', 'Aju Press', '2026-10-05T00:00:00Z', false),
  src('S9', 'STI reaches record high before September consolidation', 'SGX Group', '2026-10-01T00:00:00Z', false),
  src('S10', 'As other Asian equity markets boom, Singapore sees value over volume in its IPO rebound', 'CNA', '2026-09-07T00:00:00Z', true),
]

const finding = (id: string, angle: string, direction: string, strength: ForecastFinding['strength'], asOf: string | null, sourceIds: string[], claim: string): ForecastFinding =>
  ({ id, claim, sourceIds, asOf, angle, direction, strength })

/** 14 findings over all five ordered directions and four angles; F12 also cites S99 (not in the pack), F14 has no date. */
export const deepFindings: ForecastFinding[] = [
  finding('F1', 'A1', 'lower', 'slight', '2026-05-22', ['S3'], 'On 22 May 2026, JustCo opened at S$0.835, 11.2% below its S$0.94 IPO price, and closed at S$0.775.'),
  finding('F2', 'A1', 'higher', 'slight', '2026-01-22', ['S4'], 'Toku began trading on 22 January 2026 at S$0.260 against an IPO price of S$0.250, a 4% opening premium.'),
  finding('F3', 'A1', 'higher', 'slight', '2026-07-08', ['S5'], 'Foundation Healthcare opened at S$0.765 on 8 July 2026, about 0.7% above its S$0.76 IPO price.'),
  finding('F4', 'A1', 'center', 'minimal', '2026-08-05', ['S10'], 'All-Link Air & Sea opened at its S$0.53 IPO price on 5 August 2026, a flat debut.'),
  finding('F5', 'A2', 'center', 'moderate', '2026-10-05', ['S1', 'S2'], 'Orchid Materials lodged a preliminary prospectus on 5 October 2026 proposing 70 million shares at an indicative S$1.80 each.'),
  finding('F6', 'A2', 'center', 'moderate', '2026-10-05', ['S2'], 'IFR reported the SGX IPO at a fixed price of S$1.80, the reference for any first-day premium or discount.'),
  finding('F7', 'A2', 'higher', 'minimal', '2026-10-06', ['S6'], "Lianhe Zaobao reported that five cornerstone investors signed to subscribe at S$1.80, about 6% of post-listing shares."),
  finding('F8', 'A2', 'tail', 'minimal', '2026-06', ['S1'], 'The offering price is set by bookbuilding, so the final price may differ from the preliminary S$1.80 terms.'),
  finding('F9', 'A3', 'higher', 'slight', '2026-10-05', ['S8'], 'Micron forecast tighter memory supply in 2027 and 2028, citing demand for high-bandwidth memory and AI chips.'),
  finding('F10', 'A3', 'lower', 'slight', '2026-06-30', ['S1'], "Orchid Materials' five largest customers accounted for 38.6% of its 1H FY2026 revenue."),
  finding('F11', 'A4', 'lower', 'minimal', '2026-09-30', ['S9'], 'The Straits Times Index hit a record 5,828.5 on 4 September, then fell 1.4% in September to 5,675.88.'),
  finding('F12', 'A4', 'lower', 'minimal', '2026-09-07', ['S10', 'S99'], 'CNA said 2026 SGX listings had not delivered a sustained post-debut rally.'),
  finding('F13', 'A4', 'context', 'minimal', '2026-10-05', ['S9'], 'The S&P 500 rose and the Nasdaq hit a record high while Treasury yields stayed near multi-year highs.'),
  finding('F14', 'A2', 'context', 'minimal', null, ['S7'], "SGX Rule 8.2.1 matches orders in the opening routine at a single computed price, which becomes the day's opening price."),
]

export const deepAngles: NonNullable<ForecastResult['research']['angles']> = [
  { id: 'A1', kind: 'comparables', label: 'Recent SGX mainboard debuts: opening vs offer', queries: ['SGX mainboard IPO debut opening price 2026', 'JustCo SGX debut opening price'], findings: 4 },
  { id: 'A2', kind: 'primary', label: 'Prospectus terms and the opening auction', queries: ['Orchid Materials prospectus offer price', 'SGX opening routine rule 8.2.1'], findings: 5 },
  { id: 'A3', kind: 'drivers', label: 'Semiconductor materials backdrop', queries: ['memory chip outlook October 2026'], findings: 2 },
  { id: 'A4', kind: 'sentiment', label: 'Singapore market and IPO sentiment', queries: ['STI October 2026', 'Orchid Materials 新交所 上市 招股'], findings: 3 },
]

export const deepForecastResult: ForecastResult = {
  ...forecastResult,
  probabilities: [0.12, 0.2, 0.36, 0.2, 0.12].map((p, i) => ({ outcome: deepBuckets[i], p })),
  ensemble: [0.14, 0.2, 0.32, 0.2, 0.14].map((p, i) => ({ outcome: deepBuckets[i], p })),
  specialists: forecastResult.specialists.map((s) => ({ ...s, probabilities: [0.12, 0.2, 0.36, 0.2, 0.12].map((p, i) => ({ outcome: deepBuckets[i], p })) })),
  summary: 'An opening near the S$1.80 offer is most likely; recent SGX debuts split both ways.',
  evidence: [
    { claim: 'Indicative offer price of S$1.80', source: 'The Business Times', stance: 'supports', asOf: '2026-10-05', url: deepSources[0].url },
    { claim: 'JustCo opened 11.2% below offer', source: 'The Straits Times', stance: 'opposes', asOf: '2026-05-22', url: deepSources[2].url },
  ],
  flags: [],
  knowledgeNote: 'Deep web research via Brave Search (2 rounds, 17 searches, 10 sources, 7 articles read, 14 findings) + model knowledge (training data) as of 2026-10-06 09:50 UTC.',
  sources: deepSources,
  research: {
    queries: deepAngles.flatMap((a) => a.queries),
    status: 'live',
    depth: 'deep',
    rounds: 2,
    searches: 17,
    candidates: 71,
    angles: deepAngles,
  },
  findings: deepFindings,
  watch: [
    {
      signal: 'Final offer price and prospectus terms',
      detail: 'The registered prospectus fixes the price and the cornerstone allocation before the 28 October listing.',
      branches: [
        { if: 'The final price is set below S$1.80', then: 'The buckets shift down with the reference price', direction: 'lower' },
        { if: 'The offer is confirmed at S$1.80', then: 'The central buckets hold', direction: 'center' },
      ],
    },
    {
      signal: 'Public tranche subscription rate',
      detail: 'SGX publishes the subscription result the day before trading starts.',
      branches: [{ if: 'The public tranche is several times oversubscribed', then: 'An opening premium becomes likelier', direction: 'higher' }],
    },
  ],
  market: null,
}

export const deepForecast = (over: Partial<Forecast> = {}): Forecast =>
  forecast({
    id: 'd1234567-cccc',
    shape: 'ordered',
    question: 'What will be the opening price of Orchid Materials Holding shares on their SGX debut?',
    outcomes: deepBuckets,
    resolutionCriteria: 'Resolves to the bucket holding the first traded price on SGX on the listing day.',
    resolutionDate: '2026-10-28',
    category: 'markets',
    result: deepForecastResult,
    ...over,
  })

/** A deep run mid-research: six steps logged (the screen failed), the gap review running. */
export const deepProgress: ForecastProgress = {
  research: {
    status: 'running',
    queries: deepAngles.flatMap((a) => a.queries),
    found: 10,
    read: 7,
    mode: null,
    depth: 'deep',
    angles: deepAngles.map((a) => a.label),
    searches: 12,
    findings: 11,
    steps: [
      { phase: 'plan', round: 0, status: 'done', count: 4, of: 0 },
      { phase: 'search', round: 1, status: 'done', count: 58, of: 12 },
      { phase: 'screen', round: 1, status: 'failed', count: 10, of: 58 },
      { phase: 'read', round: 1, status: 'done', count: 7, of: 10 },
      { phase: 'extract', round: 1, status: 'done', count: 11, of: 4 },
      { phase: 'gap', round: 1, status: 'running', count: 0, of: 0 },
    ],
  },
  specialists: [
    { role: 'base_rate', status: 'pending', top: null },
    { role: 'quant', status: 'pending', top: null },
    { role: 'skeptic', status: 'pending', top: null },
  ],
  aggregator: { status: 'pending' },
  market: { status: 'skipped', matched: false },
  updatedAt: '2026-10-06T09:47:00Z',
}

// ── Forecast materials: the deep report with three of the user's files, and a run still reading them ──

/** D1 a broker note the desk read (3 findings), D2 a CSV reused from an earlier run, D3 a chart it couldn't read. */
export const materials: ForecastMaterial[] = [
  {
    id: 'D1', kind: 'document', mediaType: 'application/pdf', name: 'Q3 broker note (final).pdf', size: 1_234_567, pages: 12, status: 'read',
    summary: 'A broker note on Orchid Materials dated September 2026: it sets a S$2.05 target and expects strong retail demand for the IPO. Its <b>numbers</b> are the broker\'s own projections.',
    relevance: 'direct', asOf: '2026-09',
    findings: [
      { claim: 'The broker note projects first-day demand of three times the public tranche.', ref: 'p. 4', asOf: '2026-09-30', direction: 'higher', strength: 'moderate' },
      { claim: 'The note sets a 12-month target price of S$2.05, against the S$1.80 offer.', ref: 'p. 2', asOf: '2026-09-30', direction: 'higher', strength: 'slight' },
      { claim: 'It flags customer concentration: the top five customers were 39% of 1H revenue.', ref: 'p. 9', asOf: '2026-06-30', direction: 'lower', strength: 'slight' },
    ],
    excerpt: 'We expect the public tranche to be about 3x covered … Target price S$2.05 (12 months) … <b>Key risk</b>: customer concentration (top five 39% of 1H FY26 revenue).',
    readAt: '2026-10-06T09:41:00Z',
  },
  {
    id: 'D2', kind: 'document', mediaType: 'text/csv', name: 'sgx-debuts-2026.csv', size: 4_200, status: 'read',
    summary: 'A table of 2026 SGX mainboard debuts with offer and opening prices: six of nine opened at or above their offer.',
    relevance: 'background', asOf: '2026-08',
    findings: [{ claim: 'Six of the nine 2026 SGX mainboard debuts in the table opened at or above the offer price.', ref: 'rows 2-10', asOf: '2026-08', direction: 'higher', strength: 'slight' }],
    excerpt: 'JustCo,0.94,0.835\nToku,0.25,0.26\nFoundation Healthcare,0.76,0.765',
    readAt: '2026-10-05T11:02:00Z', reusedFrom: 'd0000000-prev',
  },
  {
    id: 'D3', kind: 'image', mediaType: 'image/jpeg', name: 'order-book-photo.jpg', size: 880_000, status: 'failed', error: 'unreadable',
    summary: '', asOf: null, findings: [], excerpt: '', readAt: null,
  },
]

const materialFinding = (id: string, material: string, f: ForecastMaterial['findings'][number]): ForecastFinding =>
  ({ id, claim: f.claim, sourceIds: [material], asOf: f.asOf, angle: '', direction: f.direction, strength: f.strength, origin: 'material', ref: f.ref })

export const materialsForecastResult: ForecastResult = {
  ...deepForecastResult,
  findings: [
    ...deepFindings,
    ...materials[0].findings.map((f, i) => materialFinding(`F${15 + i}`, 'D1', f)),
    materialFinding('F18', 'D2', materials[1].findings[0]),
  ],
  evidence: [
    ...deepForecastResult.evidence,
    { claim: 'A broker note expects the public tranche about 3x covered', source: 'Your material · Q3 broker note (final).pdf', stance: 'supports', asOf: '2026-09-30', url: null, materialId: 'D1' },
  ],
  flags: [{ type: 'low_information', note: "The desk couldn't read your file order-book-photo.jpg, so this forecast does not use it." }],
}

export const materialsForecast = (over: Partial<Forecast> = {}): Forecast =>
  deepForecast({ id: 'm1234567-dddd', result: materialsForecastResult, materials, ...over })

/** A run reading its materials: one reused, one read, one still going; research has not started. */
export const materialsProgress: ForecastProgress = {
  ...deepProgress,
  research: { status: 'pending', queries: [], found: 0, read: 0, mode: null, depth: null, angles: [], searches: 0, findings: 0, steps: [] },
  materials: { status: 'running', total: 3, reused: 1, read: 2, failed: 0 },
}

// ── Desk view and methods: the TSM question re-run with three methods, its old report, a short BTC ladder ──
// Numbers are AQ-DESIGN §6.1's worked example (the PDF's buckets and its stand-in for H1); the measured history is
// synthetic, not TSM's. The view and the scenarios are the API's JSON for these buckets (the API's pure view code);
// the specialist stats are the same interpolation of their probabilities.

export const tsmBuckets = ['< $300', '$300 – $450', '$450 – $600', '$600 – $800', '$800 – $1,100', '≥ $1,100']
const tsmP = (ps: number[]): OutcomeP[] => tsmBuckets.map((outcome, i) => ({ outcome, p: ps[i] }))
const tsmEdges = [300, 450, 600, 800, 1100].map((value) => ({ value, vsRefPct: (value / 485 - 1) * 100 }))

export const tsmSources: ForecastSource[] = [
  { id: 'S1', title: 'TSMC raises 2026 revenue growth guidance on AI demand', url: 'https://news.example/tsmc-guidance', source: 'Reuters', publishedAt: '2026-10-01T00:00:00Z', read: true },
  { id: 'S2', title: 'Taiwan Semiconductor ADR valuation and multiples', url: 'https://quotes.example/tsm-valuation', source: 'Quotes', publishedAt: '2026-10-05T00:00:00Z', read: true },
  { id: 'S3', title: 'TSM ADR closes at a record $485.80', url: 'https://news.example/tsm-record', source: 'Bloomberg', publishedAt: '2026-10-06T00:00:00Z', read: false },
  { id: 'S5', title: 'TSM consensus estimates: EPS $16.56 this year, $21.33 next', url: 'https://estimates.example/tsm', source: 'Estimates', publishedAt: '2026-10-04T00:00:00Z', read: true },
]

export const tsmFindings: ForecastFinding[] = [
  { id: 'F1', claim: 'TSMC guided 2026 revenue growth to about 30% in US dollars, citing AI accelerators.', sourceIds: ['S1'], asOf: '2026-10-01', angle: 'A1', direction: 'higher', strength: 'strong' },
  { id: 'F2', claim: 'The ADR trades at 29.3× current-year EPS, above its ten-year range of about 14–25×.', sourceIds: ['S2'], asOf: '2026-10-05', angle: 'A2', direction: 'lower', strength: 'moderate' },
  { id: 'F3', claim: 'Consensus EPS for the ADR is $16.56 for FY2026 and $21.33 for FY2027.', sourceIds: ['S5'], asOf: '2026-10-04', angle: 'A2', direction: 'center', strength: 'strong' },
  { id: 'F4', claim: 'The ADR closed at a record $485.80 on Oct 5, near its 52-week high of $487.47.', sourceIds: ['S3', 'S5'], asOf: '2026-10-05', angle: 'A3', direction: 'context', strength: 'moderate' },
]

const eps = (): ScenarioInput => ({ field: 'eps', value: 21.33, year: 2027, basis: 'F3', kind: 'source', verified: true })
const growth = (pct: number): ScenarioInput => ({ field: 'growthPct', value: pct, year: 2032, basis: 'assumption', kind: 'assumption', verified: false })
const pe = (x: number, basis = 'assumption'): ScenarioInput =>
  ({ field: 'pe', value: x, basis, kind: basis.startsWith('model knowledge') ? 'model_knowledge' : 'assumption', verified: false })

export const tsmModel: NonNullable<Specialist['model']> = [
  { name: 'AI capex digestion', kind: 'bear', p: 0.3, price: 346.2, vsRefPct: -28.62, inputs: [eps(), growth(3), pe(14)] },
  { name: 'Compounding foundry leader', kind: 'base', p: 0.5, price: 751.8, vsRefPct: 55.01, inputs: [eps(), growth(12), pe(20)] },
  { name: 'AI supercycle', kind: 'bull', p: 0.2, price: 1326.9, vsRefPct: 173.59, inputs: [eps(), growth(20), pe(25, 'model knowledge, as of 2025-06')] },
]

export const tsmScenarios: DeskScenario[] = [
  {
    kind: 'tail', name: 'Taiwan blockade or conflict', p: 0.14773975084330035, modelP: 0.05, lo: null, hi: 283.4050105414511, level: 232, vsRefPct: -52.16494845360824, annualPct: -13.712229220403561,
    inputs: [{ field: 'eventP', value: 0.05, basis: 'assumption', kind: 'assumption', verified: false }, { field: 'movePct', value: -60, basis: 'assumption', kind: 'assumption', verified: false }],
    thesis: 'A blockade or conflict halts output at the Taiwan fabs for months.', mustBeTrue: ['A blockade or conflict that halts TSMC’s Taiwan fabs (assumption)'], sourceIds: [],
  },
  {
    kind: 'bear', name: 'AI capex digestion', p: 0.2576949335822102, modelP: 0.3, lo: 283.4050105414511, hi: 510.16973646032744, level: 346.2, vsRefPct: -28.61855670103093, annualPct: -6.520352729441492, inputs: tsmModel[0].inputs,
    thesis: 'Hyperscalers pause AI spending and the multiple returns to its old range.', mustBeTrue: ['EPS growth under 5%/yr after FY2027 (F3) and the multiple back to about 14× (assumption)'], sourceIds: ['F3'],
  },
  {
    kind: 'base', name: 'Compounding foundry leader', p: 0.3490971924136256, modelP: 0.5, lo: 510.16973646032744, hi: 998.7809669792472, level: 751.8, vsRefPct: 55.01030927835051, annualPct: 9.16215900818591, inputs: tsmModel[1].inputs,
    thesis: 'Earnings compound with leading-edge share while the multiple eases.', mustBeTrue: ['FY2032 EPS near $38, from $21.33 in FY2027 (F3)', 'An exit multiple of about 20×, below today’s 29.3× (F2)'], sourceIds: ['F3', 'F2'],
  },
  {
    kind: 'bull', name: 'AI supercycle', p: 0.24546812316086386, modelP: 0.2, lo: 998.7809669792472, hi: null, level: 1326.9, vsRefPct: 173.5876288659794, annualPct: 22.2979820441386, inputs: tsmModel[2].inputs,
    thesis: 'AI demand keeps EPS growth near 20% a year through FY2032.', mustBeTrue: ['20%/yr EPS growth through FY2032 (F1, S1)', 'The multiple held near 25× (H1 shows the range)'], sourceIds: ['F1', 'S1', 'H1'],
  },
]

export const tsmMeasured: DeskMeasured = {
  symbol: 'TSM', name: 'TSMC (Taiwan Semiconductor)', source: 'Yahoo Finance daily closes (NYSE: TSM)', currency: 'USD',
  from: '1997-10-09', to: '2026-10-06', sessions: 7298,
  volAnnPct: { y1: 34.8, y5: 36.2, all: 38.1 },
  cagrPct: { y5: 21.4, y10: 24.9, all: 13.2 },
  maxDrawdown: { pct: -83.4, peak: '2000-03-07', trough: '2002-10-09', recovered: '2010-01-04' },
  window: { days: 1825, n: 5469, independent: 5, p10: 0.62, p50: 1.71, p90: 4.05, pUp: 0.74 },
  url: 'https://finance.yahoo.com/quote/TSM/history',
}

const tsmView: DeskView = {
  scale: 'log', unit: { prefix: '$', suffix: '' }, horizonDays: 1825,
  reference: { value: 485, label: 'TSM, NYSE regular-market price', asOf: '2026-10-06T20:00:00Z', source: 'Yahoo Finance (delayed)' },
  median: { value: 610.8856471272738 }, p10: { value: 241.92584013610596, approx: true }, p25: { value: 363.0677966127341 }, p75: { value: 989.2174886497396 },
  p90: { value: 1476.4254367930055, approx: true }, pAbove: { value: 0.6209457894605135 }, changePct: 25.95580353139666, change: null, annualPct: 4.723376556875536,
  flat: true, headline: 'view', edges: tsmEdges,
  lead: "Our median is $611 for Oct 6, 2031, +26% from today's $485 (about +4.7% a year); 50% range $363–$989, 80% range ≈$242–≈$1,480; 62% chance it closes at or above $485.",
  basis: { kind: 'scenarios', label: 'Valuation scenarios with cited inputs, weighed with the measured history' },
}

const specialistTsm = (role: Specialist['role'], name: string, ps: number[], over: Partial<Specialist>): Specialist => ({
  role, name, probabilities: tsmP(ps), reasoning: '', keyFactors: [], confidence: 'medium', ...over,
})

/** tsmView in brief, as a list row and the chat's cards carry it (ForecastSummary.view). */
export const tsmViewBrief = {
  median: tsmView.median, p10: tsmView.p10, p90: tsmView.p90, pAbove: tsmView.pAbove, unit: tsmView.unit, flat: tsmView.flat, headline: tsmView.headline,
  basis: tsmView.basis.kind,
} satisfies NonNullable<ForecastSummary['view']>

export const tsmDeskResult: ForecastResult = {
  probabilities: tsmP([0.17, 0.17, 0.15, 0.16, 0.15, 0.2]),
  ensemble: tsmP([0.175, 0.177, 0.154, 0.154, 0.145, 0.195]),
  specialists: [
    specialistTsm('base_rate', 'Base-rate statistician', [0.173, 0.185, 0.161, 0.158, 0.143, 0.18], {
      reasoning: 'Five-year windows of the measured history, blended with a lognormal at the full-history volatility (H1); SOX windows say the same within 3 points.',
      keyFactors: ['Measured five-year windows (H1)', 'Peers via SOX'], method: 'history', anchor: tsmP([0.173, 0.185, 0.161, 0.158, 0.143, 0.18]),
      stats: { median: { value: 580 }, p10: { value: 243.7, approx: true }, p90: { value: 1392.3, approx: true }, pAbove: { value: 0.6001 }, statedMedian: 580 },
    }),
    specialistTsm('quant', 'Market & quant analyst', [0.13, 0.15, 0.14, 0.16, 0.17, 0.25], {
      reasoning: 'EPS × P/E scenarios from the FY2027 consensus (F3): bear 14×, base 20×, bull 25×. Today’s $485 is 22.7× next year’s EPS.',
      keyFactors: ['FY2027 consensus EPS $21.33 (F3)', 'Exit multiple'], confidence: 'high', method: 'fundamentals', anchor: tsmP([0.135, 0.156, 0.144, 0.157, 0.163, 0.246]),
      stats: { median: { value: 692.8 }, p10: { value: 273.6, approx: true }, p90: { value: 1689.5, approx: true }, pAbove: { value: 0.6835 }, statedMedian: 690 },
      model: tsmModel,
    }),
    specialistTsm('skeptic', 'Narrative & macro skeptic', [0.21, 0.19, 0.16, 0.15, 0.13, 0.16], {
      reasoning: 'Two tail events priced on the history base: a Taiwan blockade (5%, −60%) and an AI-capex bust (10%, −35%).',
      keyFactors: ['Taiwan risk', 'Capex cycle'], method: 'regime', anchor: tsmP([0.217, 0.19, 0.156, 0.148, 0.13, 0.16]),
      stats: { median: { value: 538.6 }, p10: { value: 215.1, approx: true }, p90: { value: 1322.5, approx: true }, pAbove: { value: 0.5583 }, statedMedian: 540 },
    }),
  ],
  summary: 'We expect TSM to compound with earnings rather than with its multiple: at 22.7× next year’s consensus EPS the base case needs about 12% a year of EPS growth after FY2027 to reach the mid-$700s. The main risk is a capex digestion or a Taiwan shock.',
  drivers: [
    { factor: 'AI accelerator demand', direction: 'up', weight: 'high' },
    { factor: 'Taiwan geopolitical risk', direction: 'down', weight: 'medium' },
  ],
  evidence: [{ claim: 'Consensus EPS is $21.33 for FY2027', source: 'S5', stance: 'supports', asOf: '2026-10-04', url: 'https://estimates.example/tsm' }],
  flags: [],
  whatWouldChange: ['Two quarters of falling hyperscaler capex'],
  confidence: 'medium',
  knowledgeNote: 'Deep web research via Brave Search (4 sources) + Yahoo daily history (TSM since 1997) + model knowledge (training data).',
  sources: tsmSources,
  research: { queries: [], status: 'live', depth: 'deep', rounds: 1, searches: 8 },
  findings: tsmFindings,
  view: tsmView,
  scenarios: tsmScenarios,
  methods: {
    plan: 'long_stock',
    weights: [
      { role: 'base_rate', method: 'history', weight: 0.3, why: 'Measured windows set the width.' },
      { role: 'quant', method: 'fundamentals', weight: 0.45, why: 'Earnings and the exit multiple explain most of a five-year move.' },
      { role: 'skeptic', method: 'regime', weight: 0.25, why: 'The tails the history under-weights.' },
    ],
    weightsFrom: 'desk',
    pool: tsmP([0.167, 0.173, 0.152, 0.155, 0.149, 0.204]),
    agreement: 'diverse',
    guard: null,
    note: 'We trusted the valuation scenarios for the centre and the measured history for the width.',
  },
  measured: tsmMeasured,
}

export const tsmDeskForecast = (over: Partial<Forecast> = {}): Forecast =>
  forecast({
    id: 'v1234567-eeee',
    shape: 'ordered',
    question: 'What will be the NYSE closing price of TSM ADR on 2031-10-06?',
    outcomes: tsmBuckets,
    resolutionCriteria: 'Resolves to the range holding the NYSE closing price of the TSM ADR on 2031-10-06 (Yahoo Finance history); if not a trading day, the last close before.',
    resolutionDate: '2031-10-06',
    category: 'markets',
    result: tsmDeskResult,
    createdAt: '2026-10-06T21:00:00Z',
    completedAt: '2026-10-06T21:06:00Z',
    ...over,
  })

/**
 * The same question stored before desk views: the old near-uniform buckets, one method, and the view GET derives, which
 * no evidence anchors (basis none: the ranges only, no median, no reference).
 */
export const tsmLegacyResult: ForecastResult = {
  ...forecastResult,
  probabilities: tsmP([0.17, 0.19, 0.17, 0.16, 0.14, 0.17]),
  ensemble: tsmP([0.17, 0.19, 0.17, 0.16, 0.14, 0.17]),
  specialists: tsmDeskResult.specialists.map(({ method: _m, anchor: _a, stats: _s, model: _x, ...s }) => ({ ...s, probabilities: tsmP([0.17, 0.19, 0.17, 0.16, 0.14, 0.17]) })),
  summary: 'We stay close to the ensemble. A lognormal with a median near $580 reproduces the specialists’ numbers.',
  market: null,
  flags: [{ type: 'duplicate', note: 'The three specialists built the same lognormal.' }],
  view: {
    ...tsmView, reference: null, pAbove: null, changePct: null, annualPct: null, derived: true,
    median: { value: 570.299824634681 }, p10: { value: 247.46736940914099, approx: true }, p25: { value: 355.8483387869405 }, p75: { value: 916.9857186531046 },
    p90: { value: 1350.535044612285, approx: true },
    edges: tsmEdges.map((e) => ({ ...e, vsRefPct: null })),
    lead: "The desk's 50% range for Oct 6, 2031 is $356–$917 and its 80% range ≈$247–≈$1,350.",
    basis: { kind: 'none', label: "Read from the desk's ranges: no cited scenarios or measured history set its centre, so it states no median." },
  },
}

/** A seven-day BTC ladder with one range at 46%: the hero stays the top range, with the view's numbers under it. */
export const btcBucketResult: ForecastResult = {
  ...forecastResult,
  probabilities: ['< $62,000', '$62,000 – $66,000', '$66,000 – $70,000', '$70,000 – $74,000', '≥ $74,000'].map((outcome, i) => ({ outcome, p: [0.08, 0.22, 0.46, 0.16, 0.08][i] })),
  ensemble: [],
  market: null,
  view: {
    scale: 'log', unit: { prefix: '$', suffix: '' }, horizonDays: 7,
    reference: { value: 66500, label: 'BTC', asOf: '2026-10-07T12:00:00Z', source: 'Binance BTC/USDT' },
    median: { value: 67710 }, p10: { value: 62353 }, p25: { value: 65069 }, p75: { value: 69911 }, p90: { value: 73488 },
    pAbove: { value: 0.641 }, changePct: 1.82, change: null, annualPct: null, flat: false, headline: 'bucket',
    edges: [62000, 66000, 70000, 74000].map((value) => ({ value, vsRefPct: Math.round((value / 66500 - 1) * 1e4) / 100 })),
    lead: "Our median is $67,700 for Oct 14, 2026, +1.8% from today's $66,500; 50% range $65,100–$69,900, 80% range $62,400–$73,500; 64% chance it closes at or above $66,500.",
    basis: { kind: 'capped_drift', label: "Measured BTC history since 2018; drift +10.0%/yr (half the history's CAGR, capped at 10%/yr: an assumption)" },
  },
}

export const btcBucketForecast = (over: Partial<Forecast> = {}): Forecast =>
  forecast({
    id: 'b1234567-ffff', shape: 'ordered', question: 'Where will BTC close on Oct 14, 2026?', outcomes: btcBucketResult.probabilities.map((o) => o.outcome),
    resolutionCriteria: 'Resolves to the range holding the Binance BTCUSDT daily close on 2026-10-14.', resolutionDate: '2026-10-14', result: btcBucketResult, ...over,
  })

/** A new draft of the same question: ranges at multiples of today's $485 (LadderEdges at σ√T 0.7) and the level they use. */
export const tsmDraftBuckets = ['< $250', '$250 – $360', '$360 – $485', '$485 – $750', '$750 – $1,000', '$1,000 – $1,500', '≥ $1,500']
export const tsmDraftReference = { value: 485.12, label: 'TSM, NYSE regular-market price', asOf: '2026-10-06T20:00:00Z', source: 'Yahoo Finance (delayed)' }

export const notificationChannels = (over: Partial<NotificationChannels> = {}): NotificationChannels => ({
  email: { enabled: false, address: 'demo@example.com', available: true },
  lark: { enabled: false, webhook: null, signed: false },
  ...over,
})

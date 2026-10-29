// Pure helpers for the strategy studio, shared with the AI portfolio pages. Checked by strategy.test.ts.
import { fmtPct } from '../../lib/format'
import type { Asset, Condition, ForwardSummary, Indicator, Interval, Metrics, Point, RuleSet, StrategySpec } from '../../lib/types'
import { QUOTE_FEED_LISTINGS, QUOTE_FEED_SYMBOLS } from './quoteFeeds'

export const LABS = 'Foretrade Labs' // author of the built-in strategies; they cannot be deleted
export const AI_COST = 2 // credits per strategy generate and per portfolio build (contract §3)

/** Crypto and the perps, core 12 first (contract §2); SYMBOLS adds the quote feeds after them. */
const CONFIGURED = [
  ['BTC', 'Bitcoin'],
  ['ETH', 'Ethereum'],
  ['SOL', 'Solana'],
  ['BNB', 'BNB'],
  ['XRP', 'XRP'],
  ['DOGE', 'Dogecoin'],
  ['ADA', 'Cardano'],
  ['AVAX', 'Avalanche'],
  ['LINK', 'Chainlink'],
  ['SUI', 'Sui'],
  ['TON', 'Toncoin'],
  ['PAXG', 'PAX Gold'],
  ['TRX', 'TRON'],
  ['DOT', 'Polkadot'],
  ['LTC', 'Litecoin'],
  ['BCH', 'Bitcoin Cash'],
  ['NEAR', 'NEAR Protocol'],
  ['APT', 'Aptos'],
  ['ARB', 'Arbitrum'],
  ['OP', 'Optimism'],
  ['UNI', 'Uniswap'],
  ['AAVE', 'Aave'],
  ['ATOM', 'Cosmos'],
  ['FIL', 'Filecoin'],
  ['ICP', 'Internet Computer'],
  ['ETC', 'Ethereum Classic'],
  ['XLM', 'Stellar'],
  ['HBAR', 'Hedera'],
  ['INJ', 'Injective'],
  ['SEI', 'Sei'],
  ['TIA', 'Celestia'],
  ['WLD', 'Worldcoin'],
  ['RENDER', 'Render'],
  ['FET', 'Artificial Superintelligence'],
  ['ENA', 'Ethena'],
  ['TAO', 'Bittensor'],
  ['ONDO', 'Ondo'],
  ['ALGO', 'Algorand'],
  ['LDO', 'Lido DAO'],
  ['STX', 'Stacks'],
  ['ZEC', 'Zcash'],
  ['QNT', 'Quant'],
  ['SAND', 'The Sandbox'],
  ['MUBARAK', 'Mubarak'],
  ['STRK', 'Starknet'],
  ['AXS', 'Axie Infinity'],
  ['TRUMP', 'Official Trump'],
  ['ZRO', 'LayerZero'],
  ['ACE', 'Fusionist'],
  ['VIRTUAL', 'Virtuals Protocol'],
  ['ORCA', 'Orca'],
  ['RUNE', 'THORChain'],
  ['RAY', 'Raydium'],
  ['NIL', 'Nillion'],
  ['MOVR', 'Moonriver'],
  ['PHA', 'Phala Network'],
  ['DASH', 'Dash'],
  ['JST', 'JUST (JST)'],
  ['ETHFI', 'ether.fi'],
  ['NMR', 'Numeraire'],
  ['WLFI', 'World Liberty Financial'],
  ['JTO', 'Jito'],
  ['POL', 'Polygon (ex-MATIC)'],
  ['CAKE', 'PancakeSwap'],
  ['JUP', 'Jupiter'],
  ['AR', 'Arweave'],
  ['SAGA', 'Saga'],
  ['CRV', 'Curve DAO'],
  ['PENDLE', 'Pendle'],
  ['NVDA', 'NVIDIA'],
  ['TSLA', 'Tesla'],
  ['AAPL', 'Apple'],
  ['MSFT', 'Microsoft'],
  ['AMZN', 'Amazon'],
  ['GOOGL', 'Alphabet (Google group)'],
  ['META', 'Meta Platforms'],
  ['SP500', 'S&P 500 perp'],
  ['XYZ100', 'XYZ100 perp'],
  ['TSM', 'TSMC (Taiwan Semiconductor)'],
  ['BABA', 'Alibaba Group'],
  ['SMSN', 'Samsung Electronics'],
  ['SKHX', 'SK hynix'],
  ['HYUNDAI', 'Hyundai Motor'],
  ['SOFTBANK', 'SoftBank Group'],
  ['MINIMAX', 'MiniMax Group'],
  ['ZHIPU', 'Zhipu AI (Knowledge Atlas)'],
  ['EWJ', 'iShares MSCI Japan ETF'],
  ['EWY', 'iShares MSCI South Korea ETF'],
  // Wave 2 (contract §2): twelve US stocks, then seven Asia perps.
  ['PLTR', 'Palantir'],
  ['COIN', 'Coinbase'],
  ['HOOD', 'Robinhood'],
  ['MSTR', 'Strategy Inc (MicroStrategy)'],
  ['INTC', 'Intel'],
  ['AMD', 'AMD'],
  ['ORCL', 'Oracle'],
  ['NFLX', 'Netflix'],
  ['CRCL', 'Circle Internet Group'],
  ['MU', 'Micron Technology'],
  ['SNDK', 'SanDisk'],
  ['CRWV', 'CoreWeave'],
  ['SKHY', 'SK hynix (Nasdaq ADS)'],
  ['CXMT', 'CXMT (ChangXin Memory)'],
  ['UNITREE', 'Unitree Robotics'],
  ['GIGADEV', 'GigaDevice Semiconductor'],
  ['KIOXIA', 'Kioxia Holdings'],
  ['EWT', 'iShares MSCI Taiwan ETF'],
  ['KSTR', 'KraneShares STAR 50 ETF'],
] as const
export const US_PERPS = new Set<string>([
  'NVDA', 'TSLA', 'AAPL', 'MSFT', 'AMZN', 'GOOGL', 'META', 'SP500', 'XYZ100',
  'PLTR', 'COIN', 'HOOD', 'MSTR', 'INTC', 'AMD', 'ORCL', 'NFLX', 'CRCL', 'MU', 'SNDK', 'CRWV',
])
export const ASIA_PERPS = new Set<string>([
  'TSM', 'BABA', 'SMSN', 'SKHX', 'HYUNDAI', 'SOFTBANK', 'MINIMAX', 'ZHIPU', 'EWJ', 'EWY',
  'SKHY', 'CXMT', 'UNITREE', 'GIGADEV', 'KIOXIA', 'EWT', 'KSTR',
])
export const isUSPerp = (symbol: string) => US_PERPS.has(symbol)
/** Any stock, ETF or index perp (US or Asia): Hyperliquid mark fills, no funding, dividends or split adjustment. */
export const isStockPerp = (symbol: string) => US_PERPS.has(symbol) || ASIA_PERPS.has(symbol)

export type MarketGroup = 'crypto' | 'us' | 'asia'
export const MARKET_GROUPS: readonly { key: MarketGroup; label: string }[] = [
  { key: 'crypto', label: 'Crypto' },
  { key: 'us', label: 'US stocks & indices' },
  { key: 'asia', label: 'Asia stocks & ETFs' },
]
/**
 * The trade page's market list and the automations picker: crypto, US, then the Asia perps and US-listed Asian ADRs
 * (live or NYSE-hours), then one group per local Asian venue, Singapore, Taiwan and Korea first. The venue groups hold
 * Yahoo quote feeds, which arrive delayed (`delayed`). MARKET_GROUPS stays for the other pickers.
 */
export type VenueGroup = MarketGroup | 'SGX' | 'TWSE' | 'KRX' | 'TSE' | 'HKEX' | 'IDX' | 'SET' | 'BURSA' | 'HOSE'
export const VENUE_GROUPS: readonly { key: VenueGroup; label: string; delayed?: true }[] = [
  { key: 'crypto', label: 'Crypto' },
  { key: 'us', label: 'US stocks & indices' },
  { key: 'asia', label: 'Asia perps & US-listed ADRs' },
  { key: 'SGX', label: 'Singapore · SGX', delayed: true },
  { key: 'TWSE', label: 'Taiwan · TWSE', delayed: true },
  { key: 'KRX', label: 'South Korea · KRX', delayed: true },
  { key: 'TSE', label: 'Japan · TSE', delayed: true },
  { key: 'HKEX', label: 'Hong Kong · HKEX', delayed: true },
  { key: 'IDX', label: 'Indonesia · IDX', delayed: true },
  { key: 'SET', label: 'Thailand · SET', delayed: true },
  { key: 'BURSA', label: 'Malaysia · Bursa', delayed: true },
  { key: 'HOSE', label: 'Vietnam · HOSE', delayed: true },
]
const LOCAL_VENUES = new Set<string>(VENUE_GROUPS.filter((g) => g.delayed).map((g) => g.key))
/**
 * An asset's (or catalogue row's) market-list group: its MARKET_GROUPS group, with an Asian quote feed on a local
 * venue in that venue's group. Asia perps and US-hours quote feeds stay in "Asia perps & US-listed ADRs".
 */
export function venueGroup(a: Pick<Asset, 'symbol' | 'class' | 'region' | 'venue'> & { sessionVenue?: Asset['sessionVenue'] }): VenueGroup {
  const group = marketGroup(a)
  if (group !== 'asia') return group
  const feed = a.venue === 'yahoo'
  const venue = a.sessionVenue ?? QUOTE_FEED_LISTINGS[a.symbol]?.sessionVenue
  return feed && venue && LOCAL_VENUES.has(venue) ? (venue as VenueGroup) : 'asia'
}

/** Configured symbol → its market-list group, for pickers with no live asset or catalogue row for it. */
export function symbolVenueGroup(symbol: string): VenueGroup {
  const l = QUOTE_FEED_LISTINGS[symbol]
  return l ? venueGroup({ symbol, class: 'stock', region: l.region, venue: 'yahoo', sessionVenue: l.sessionVenue }) : symbolGroup(symbol)
}

/** Configured symbol → group (for pickers that list SYMBOLS rather than live assets). */
export const symbolGroup = (symbol: string): MarketGroup =>
  QUOTE_FEED_LISTINGS[symbol]?.region ?? (ASIA_PERPS.has(symbol) ? 'asia' : US_PERPS.has(symbol) ? 'us' : 'crypto')
/** Live asset → group: the API's region when present, else its class (crypto) or the configured table. */
export function marketGroup(a: Pick<Asset, 'symbol' | 'class' | 'region'>): MarketGroup {
  if (a.region === 'us' || a.region === 'asia') return a.region
  if (a.class === 'crypto') return 'crypto'
  return QUOTE_FEED_LISTINGS[a.symbol]?.region ?? (ASIA_PERPS.has(a.symbol) ? 'asia' : 'us')
}

/**
 * Regular-session line for a perp header: "KRX session open". An API without `sessionVenue` reported the
 * US (NYSE) session, so US perps keep "US session …" there; anything else without a venue is unknown.
 */
export function sessionLabel(symbol: string, a: Pick<Asset, 'session' | 'sessionVenue'> | undefined) {
  const venue = a?.sessionVenue ?? (a?.sessionVenue === undefined && !ASIA_PERPS.has(symbol) ? 'US' : null)
  return { venue, text: venue ? `${venue} session ${a?.session ?? 'unknown'}` : 'Regular session unknown' }
}

/** Paper-fill FX note for perps whose underlying lists in KRW/JPY/HKD/CNY; empty for USD listings and crypto. */
export function fxNote(a: Pick<Asset, 'localCurrency'> | undefined) {
  const ccy = a?.localCurrency
  if (!ccy || ccy === 'USD') return ''
  // China A-shares: the oracle uses the offshore yuan (CNH) rate, not the onshore CNY fixing.
  const rate = ccy === 'CNY' ? ' at the offshore USD/CNH rate' : ''
  return `Price is the USD perp mark: trade.xyz's oracle converts the ${ccy} listing to USD${rate}. This app does no FX conversion.`
}
/** The asset universe (contract §2): crypto and the perps, then the quote-feed stocks (quoteFeeds.ts). */
export const SYMBOLS = [...CONFIGURED, ...QUOTE_FEED_SYMBOLS] as const
export const assetName = (symbol: string) => SYMBOLS.find(([s]) => s === symbol)?.[1] ?? symbol

export const INTERVALS: { value: Interval; label: string }[] = [
  { value: '15m', label: '15 min' },
  { value: '1h', label: '1 hour' },
  { value: '4h', label: '4 hours' },
  { value: '1d', label: '1 day' },
]

// Gold first, then distinct hues for multi-series charts and allocations; green and red stay
// reserved for gains and losses.
export const PALETTE = ['#C8962E', '#3B6FD8', '#0E9F8E', '#7C5CD6', '#D9467A', '#E07B39', '#64748B', '#8A9A2B']

/** Index series (100 = start) → cumulative-return label for chart axes: 112.3 → "+12.3%". */
export const fmtIndex = (v: number) => {
  const r = v - 100
  return `${r > 0 ? '+' : r < 0 ? '−' : ''}${Math.abs(r).toFixed(1)}%`
}

/** Sharpe, profit factor…: "1.42". */
export const fmtNum = (x: number | null | undefined) => (typeof x === 'number' && Number.isFinite(x) ? x.toFixed(2) : '—')

/** Max drawdown arrives as a positive magnitude; show it as the fall it is: 18.4 → "−18.4%". */
export const fmtDD = (x: number | null | undefined, decimals = 1) =>
  typeof x === 'number' && Number.isFinite(x) ? `${x ? '−' : ''}${Math.abs(x).toFixed(decimals)}%` : '—'

/** Days a backtest spans, from its first to its last bar (NaN when unknown). */
const windowDays = (m: Pick<Metrics, 'startT' | 'endT'> | undefined) => (m ? (m.endT - m.startT) / 86400 : NaN)

/** Backtest window for a table cell: "42 d" under a year, else "2.7 y". */
export function fmtWindow(m: Pick<Metrics, 'startT' | 'endT'> | undefined) {
  const d = windowDays(m)
  if (!(d >= 0)) return '—'
  return d < 365 ? `${Math.round(d)} d` : `${(d / 365).toFixed(1)} y`
}

/**
 * The return a reader should see first. Under 90 days the server leaves the annualized figure out (it would
 * extrapolate noise), and under 10 trades the page holds it back too, so the headline is the plain total over the window.
 */
export function returnHeadline(m: Metrics | undefined) {
  if (!m) return ''
  const total = fmtPct(m.totalReturnPct, { sign: true, decimals: 1 })
  if (m.annualizedReturnPct == null || m.trades < 10) return `Total return ${total} over ${Math.round(windowDays(m))} days`
  return `Annualized ${fmtPct(m.annualizedReturnPct, { sign: true, decimals: 1 })} · total ${total} over ${fmtWindow(m).replace(' d', ' days').replace(' y', ' years')}`
}

/** ISO time → "14:05 UTC" (with "Oct 2, " in front when `date`). */
export function utcTime(iso: string | null | undefined, date = false) {
  const d = iso ? new Date(iso) : null
  if (!d || Number.isNaN(d.getTime())) return '—'
  const hm = d.toISOString().slice(11, 16)
  return `${date ? `${d.toLocaleDateString('en-US', { month: 'short', day: 'numeric', timeZone: 'UTC' })}, ` : ''}${hm} UTC`
}

/**
 * Unix seconds → ISO string for the shared date formatters. Daily bars open at 00:00 UTC, so `daily` gives
 * their UTC calendar date ("2026-10-02"), which fmtDate does not shift a day back for viewers west of UTC.
 */
export const isoAt = (t: number, daily = false) =>
  Number.isFinite(t) && t > 0 ? new Date(t * 1000).toISOString().slice(0, daily ? 10 : undefined) : null

/**
 * Last value per UTC day, stamped at the day's start. The chart's time axis spaces points by index, so
 * curves from 15m, 1h and 1d backtests must share one daily grid or recent hourly points stretch the axis.
 */
export function dailyCloses(points: Point[]): Point[] {
  const out: Point[] = []
  for (const p of points) {
    const t = p.t - (p.t % 86400)
    if (out.at(-1)?.t === t) out[out.length - 1] = { t, v: p.v }
    else out.push({ t, v: p.v })
  }
  return out
}

/** The points of the last `days` days (by time, not count), rebased so the first one is 100. */
export function rebaseSince(points: Point[], days: number): Point[] {
  const end = points.at(-1)?.t
  if (end == null) return []
  const slice = points.filter((p) => p.t >= end - days * 86400)
  const base = slice[0]?.v
  if (!(base > 0)) return slice // a wiped-out curve has nothing to rebase on
  return slice.map((p) => ({ t: p.t, v: (p.v / base) * 100 }))
}

// ── Live forward (round 7) ───────────────────────────────────────────────────

const FORWARD_LABEL: Record<ForwardSummary['basis'], string> = { published: 'Since publish', saved: 'Since saved', launch: 'Since launch' }
const FORWARD_MARK: Record<ForwardSummary['basis'], string> = { published: 'Published', saved: 'Saved', launch: 'Launched' }

/** What the forward window counts from: "Since publish", "Since saved" or "Since launch". */
export const forwardLabel = (f: Pick<ForwardSummary, 'basis'>) => FORWARD_LABEL[f.basis] ?? 'Live forward'

/** The chart marker where the forward run starts: "Published", "Saved" or "Launched". */
export const forwardMark = (f: Pick<ForwardSummary, 'basis'>) => FORWARD_MARK[f.basis] ?? 'Live forward'

/** Why a forward window is early, or null when it is a full one: "12 days and 2 trades". */
export function earlyReason(f: Pick<ForwardSummary, 'metrics' | 'early'>) {
  const m = f.metrics
  if (!f.early || !m) return null
  const days = Math.round(windowDays(m))
  return `${days} ${days === 1 ? 'day' : 'days'} and ${m.trades} ${m.trades === 1 ? 'trade' : 'trades'}`
}

/**
 * "Sort by live forward": full windows first, then early ones, each by forward return (best first); no number yet or
 * unavailable last. Used on a board's own rows only: the API ranks on the backtest and takes no forward sort.
 */
export function compareForward(a: ForwardSummary | null | undefined, b: ForwardSummary | null | undefined) {
  const tier = (f: ForwardSummary | null | undefined) => (!f?.metrics ? 2 : f.early ? 1 : 0)
  return tier(a) - tier(b) || (b?.metrics?.totalReturnPct ?? 0) - (a?.metrics?.totalReturnPct ?? 0)
}

// ── Spec → readable rules ────────────────────────────────────────────────────

const n = (x: unknown) => (typeof x === 'number' && Number.isFinite(x) ? x.toLocaleString('en-US', { maximumFractionDigits: 4 }) : '?')

/** Mid-sentence name of an indicator: "EMA(20)", "price", "the prior 20-bar high". */
export function describeIndicator(ind: Indicator): string {
  switch (ind?.kind) {
    case 'price':
      return 'price'
    case 'volume':
      return 'volume'
    case 'const':
      return n(ind.value)
    case 'sma':
    case 'ema':
    case 'rsi':
    case 'roc':
    case 'atr':
      return `${ind.kind.toUpperCase()}(${n(ind.period)})`
    case 'highest':
      return `the prior ${n(ind.period)}-bar high`
    case 'lowest':
      return `the prior ${n(ind.period)}-bar low`
    case 'volume_sma':
      return `the ${n(ind.period)}-bar average volume`
    case 'bb_upper':
    case 'bb_lower':
      return `the ${ind.kind === 'bb_upper' ? 'upper' : 'lower'} Bollinger band (${n(ind.period)}, ${n(ind.stdDev)}σ)`
    case 'macd':
      return `MACD(${n(ind.fast)},${n(ind.slow)},${n(ind.signal)})`
    case 'macd_signal':
      return `the MACD signal line (${n(ind.fast)},${n(ind.slow)},${n(ind.signal)})`
    default:
      return String((ind as { kind?: unknown } | null)?.kind ?? '?')
  }
}

const OPS: Record<string, string> = { '>': '>', '<': '<', crosses_above: 'crosses above', crosses_below: 'crosses below' }

export const describeCondition = (c: Condition) =>
  `${describeIndicator(c.left)} ${OPS[c.op] ?? c.op} ${describeIndicator(c.right)}`

/** "a, b and c" */
function list(items: string[], word: 'and' | 'or') {
  return items.length < 2 ? (items[0] ?? '') : `${items.slice(0, -1).join(', ')} ${word} ${items.at(-1)}`
}

/** all → AND, any → OR, both → all AND (any). null when the set has no conditions. */
export function describeRuleSet(rs: RuleSet | undefined): string | null {
  const all = (rs?.all ?? []).map(describeCondition)
  const any = (rs?.any ?? []).map(describeCondition)
  if (!all.length) return any.length ? list(any, 'or') : null
  if (any.length) all.push(any.length > 1 ? `(${list(any, 'or')})` : any[0])
  return list(all, 'and')
}

export type Rule = { label: string; text: string }

/** The spec as sentences, one per side and phase: "Enter long when EMA(20) crosses above EMA(50)." */
export function describeRules(spec: StrategySpec): Rule[] {
  const out: Rule[] = []
  for (const [side, entry, exit, opposite] of [
    ['long', spec.longEntry, spec.longExit, spec.shortEntry],
    ['short', spec.shortEntry, spec.shortExit, spec.longEntry],
  ] as const) {
    const enter = describeRuleSet(entry)
    if (!enter) continue
    const cap = side === 'long' ? 'Long' : 'Short'
    out.push({ label: `${cap} entry`, text: `Enter ${side} when ${enter}.` })
    const leave = describeRuleSet(exit)
    if (leave) {
      out.push({ label: `${cap} exit`, text: `${side === 'long' ? 'Exit long' : 'Cover the short'} when ${leave}.` })
      continue
    }
    // Without an exit rule the position only closes on a stop, a target, a flip or the last bar.
    const ways = [
      spec.stopLossPct ? `the ${n(spec.stopLossPct)}% stop` : '',
      spec.trailingStopPct ? `the ${n(spec.trailingStopPct)}% trailing stop` : '',
      spec.takeProfitPct ? `the ${n(spec.takeProfitPct)}% target` : '',
      describeRuleSet(opposite) ? `an opposite entry signal` : '',
      'the end of the test',
    ].filter(Boolean)
    out.push({ label: `${cap} exit`, text: `No exit rule: the ${side} closes on ${list(ways, 'or')}.` })
  }
  return out
}

// ── Tune card ────────────────────────────────────────────────────────────────

export type TuneInput = { stop: string; take: string; size: string; trail?: string }
export type TuneValues = { stopLossPct?: number; takeProfitPct?: number; positionPct: number; trailingStopPct?: number }
export type TuneErrors = Partial<Record<keyof TuneInput, string>>

/** Validates the stop / take-profit / trailing-stop / position fields against the spec bounds (blank stop, target or trail = none). */
export function parseTune(input: TuneInput): { values: TuneValues | null; errors: TuneErrors } {
  const errors: TuneErrors = {}
  const optional = (key: 'stop' | 'take' | 'trail', lo = 0.1) => {
    const s = (input[key] ?? '').trim()
    if (!s) return undefined
    const v = Number(s)
    if (!(v >= lo && v <= 50)) errors[key] = `Use ${lo}–50%, or leave blank for none.`
    return v
  }
  const stopLossPct = optional('stop')
  const takeProfitPct = optional('take')
  const trailingStopPct = optional('trail', 0.5)
  const positionPct = Number(input.size.trim())
  if (!input.size.trim() || !(positionPct >= 1 && positionPct <= 100)) errors.size = 'Use 1–100% of equity.'
  return { values: Object.keys(errors).length ? null : { stopLossPct, takeProfitPct, positionPct, trailingStopPct }, errors }
}

/** True when the values would backtest something different from `spec` (0 and absent both mean none). */
export function tuneChanged(spec: StrategySpec, v: TuneValues) {
  return (
    (spec.stopLossPct || undefined) !== v.stopLossPct ||
    (spec.takeProfitPct || undefined) !== v.takeProfitPct ||
    (spec.trailingStopPct || undefined) !== v.trailingStopPct ||
    spec.positionPct !== v.positionPct
  )
}

/** Spec field → input text ("" for none). */
export const tuneText = (x: number | undefined) => (x ? String(x) : '')

// Pure helpers for the trade terminal and account page. Checked by util.test.ts.
import { fmtUsd } from '../../lib/format'
import type { Asset, Candle, ChartInterval, Interval, MarketBrief, NewsItem, Side } from '../../lib/types'
import { assetName, marketGroup } from '../strategies/strategy'

export const FEE_RATE = 0.001 // 0.1% of notional, deducted from cash (contract: paper trading)
export const MIN_NOTIONAL = 10
// Fee + 2 bps slippage shrink equity after the fill, so 100% of buying power would breach the 2× rule.
// ponytail: flat 0.5% headroom; derive it from fee + slippage if those ever become configurable.
export const MAX_FILL = 0.995

/** Binance kline stream payload → Candle, or null when it is not a usable kline. */
export function klineCandle(raw: string): Candle | null {
  let k: Record<string, unknown> | undefined
  try {
    k = (JSON.parse(raw) as { k?: Record<string, unknown> })?.k
  } catch {
    return null
  }
  if (!k) return null
  const c = { t: Math.floor(Number(k.t) / 1000), o: Number(k.o), h: Number(k.h), l: Number(k.l), c: Number(k.c), v: Number(k.v) }
  return Object.values(c).every(Number.isFinite) && c.t > 0 ? c : null
}

/** Folds a live candle in place: same open time replaces the last bar, newer appends, older is ignored (returns false). */
export function mergeCandle(candles: Candle[], c: Candle) {
  const last = candles.at(-1)
  if (last && c.t < last.t) return false
  if (last && c.t === last.t) candles[candles.length - 1] = c
  else candles.push(c)
  return true
}

/** A chart interval's bar length in seconds. */
export const BAR_SECONDS: Record<Interval, number> = { '15m': 900, '1h': 3600, '4h': 14_400, '1d': 86_400 }

/** A quote at its last-trade time (unix seconds): price in USD, localPrice in the original listing currency. */
export type Tick = { t: number; price: number; localPrice?: number }
/**
 * What a quote feed's bars are, in the chart's toolbar: whose bars and how delayed ("Yahoo bars · 20 min delayed"), and,
 * while a fresher quote moves the last bar, what that bar follows ("last bar real-time"); `title` says it in full.
 */
export type ChartBars = { bars: string; last?: string; title: string }

/**
 * The open time of the bar a tick at `t` falls in, past the last bar's span: the bars keep their own alignment (Yahoo's
 * hourly bars start at the session's open, Hong Kong's on the half hour) unless a whole step aligned to UTC starts later
 * (a session reopening on the hour after lunch).
 */
export const tickBarTime = (last: number, t: number, step: number) => Math.max(last + Math.floor((t - last) / step) * step, t - (t % step))

/** Weekly and monthly chart candles use Monday and calendar-month starts in UTC. */
function calendarBarTime(t: number, interval: '1w' | '1M') {
  const d = new Date(t * 1000)
  const day = Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), interval === '1M' ? 1 : d.getUTCDate()) / 1000
  return interval === '1w' ? day - ((d.getUTCDay() + 6) % 7) * 86_400 : day
}

/**
 * Folds a quote into the candles in place, as the socket folds a kline: inside the last bar's span it becomes the bar's
 * close (stretching its high or low); past it, it opens a new bar at that price with no volume (the quote carries none);
 * before the last bar, or not a positive price, it is ignored (returns false). A calendar interval compares calendar
 * periods, including a listing's partial first bar; existing numeric spans preserve the venue's session alignment.
 */
export function foldTick(candles: Candle[], tick: Tick, span: number | ChartInterval) {
  const last = candles.at(-1)
  if (!last || !(tick.price > 0) || !Number.isFinite(tick.price) || !Number.isFinite(tick.t) || tick.t < last.t) return false
  let open: number
  if (span === '1w' || span === '1M') {
    const period = calendarBarTime(tick.t, span)
    open = period === calendarBarTime(last.t, span) ? last.t : period
  } else {
    const step = typeof span === 'number' ? span : BAR_SECONDS[span]
    open = tick.t < last.t + step ? last.t : tickBarTime(last.t, tick.t, step)
  }
  if (!Number.isFinite(open)) return false
  if (open === last.t) candles[candles.length - 1] = { ...last, c: tick.price, h: Math.max(last.h, tick.price), l: Math.min(last.l, tick.price) }
  else candles.push({ t: open, o: tick.price, h: tick.price, l: tick.price, c: tick.price, v: 0 })
  return true
}

/** Simple moving average of closes, one point per bar from the `period`-th bar on. */
export function sma(candles: Candle[], period: number) {
  const out: { t: number; v: number }[] = []
  let sum = 0
  candles.forEach((c, i) => {
    sum += c.c
    if (i >= period) sum -= candles[i - period].c
    if (i >= period - 1) out.push({ t: c.t, v: sum / period })
  })
  return out
}

/** Decimals for chart prices, by magnitude (≈ Binance tick sizes for this universe). */
export function pricePrecision(price: number) {
  // ponytail: magnitude heuristic; read tickSize from exchangeInfo if a pair ever looks wrong.
  const a = Math.abs(price)
  return a >= 100 ? 2 : a >= 10 ? 3 : a >= 1 ? 4 : 5
}

/** Rounds a level to its chart precision, for pre-filling inputs. */
export const roundPrice = (p: number) => Number(p.toFixed(pricePrecision(p)))

/** Feed URLs come from the open web: only plain http(s) ones become links. */
export const isHttpUrl = (u: unknown): u is string => typeof u === 'string' && /^https?:\/\/[^\s]/i.test(u)

/** Linkable headlines only, at most `max` (the API may omit the array while the backend catches up). */
export const linkable = (items: NewsItem[] | null | undefined, max = Infinity) =>
  (Array.isArray(items) ? items : []).filter((n) => n && n.title && isHttpUrl(n.url)).slice(0, max)

/** The wire brief's read on one symbol, or null (no brief yet, or no news for it in the window). */
export const sentimentFor = (brief: MarketBrief | null | undefined, symbol: string) =>
  (Array.isArray(brief?.sentiment) ? brief.sentiment : []).find((s) => s?.symbol === symbol) ?? null

// Places behind the venue and currency codes a listing's metadata carries, so "korea" or "hong kong" finds its listings.
const PLACES: Record<string, string> = {
  KRX: 'south korea', KRW: 'south korea', TSE: 'japan tokyo', JPY: 'japan', HKEX: 'hong kong', HKD: 'hong kong', TWSE: 'taiwan', TWD: 'taiwan',
  SSE: 'china shanghai', CNY: 'china', SGX: 'singapore', SGD: 'singapore', IDX: 'indonesia', IDR: 'indonesia', SET: 'thailand', THB: 'thailand',
  BURSA: 'malaysia', MYR: 'malaysia', HOSE: 'vietnam', VND: 'vietnam', NSE: 'india', PSE: 'philippines',
}

// The issuer's country for the Asia rows whose venue, currency and underlying codes don't name it: US-listed and
// HK-listed mainland issuers, US-listed Singapore and Taiwan issuers, and SGX lines of Thai and Hong Kong issuers.
// From the rows in the API's internal/market (market.go, universe_quotefeed.go). KARO and KLIC name Singapore in their
// underlying text; CPNG ("no Korean listing") matches "korea" by text, so it names South Korea here.
const ISSUER_PLACE: Record<string, string> = {
  BABA: 'china', PDD: 'china', JD: 'china', BIDU: 'china', NIO: 'china', LI: 'china', XPEV: 'china', NTES: 'china', TCOM: 'china',
  BILI: 'china', FUTU: 'china hong kong', TME: 'china', BEKE: 'china', ZTO: 'china', YUMC: 'china', VIPS: 'china', TENCENT: 'china',
  XIAOMI: 'china', MEITUAN: 'china', BYD: 'china', MINIMAX: 'china', ZHIPU: 'china',
  SE: 'singapore', GRAB: 'singapore', HIMX: 'taiwan', SIMO: 'taiwan', CPNG: 'south korea',
  THAIBEV: 'thailand', JARDINE: 'hong kong', HKLAND: 'hong kong',
}

/**
 * Lower-case text the market search matches: symbol, names, group (crypto / us / asia), venue, underlying (a listed
 * stock's code and index: "TSE: 6501 (Nikkei 225)") and its places, plus a quote feed's Yahoo ticker ("d05.si").
 */
export function marketSearchText(a: Asset) {
  const codes = [a.sessionVenue, a.localCurrency, ...(a.underlying?.match(/\b[A-Z]{3,4}\b/g) ?? [])]
  const places = new Set([...codes.map((c) => c && PLACES[c]), ISSUER_PLACE[a.symbol]].filter(Boolean))
  const yahoo = a.venue === 'yahoo' ? [a.pair, a.quoteFeed?.symbol] : []
  return [a.symbol, a.name, assetName(a.symbol), marketGroup(a), a.sessionVenue, a.underlying, ...yahoo, ...places].filter(Boolean).join(' ').toLowerCase()
}

/** Signed position quantity after an order of `amount` USD at `price`. */
export const resultingQty = (posQty: number, side: Side, amount: number, price: number) =>
  price > 0 ? posQty + ((side === 'buy' ? 1 : -1) * amount) / price : posQty

/** A Copilot plan's notional for the ticket: the nearest $50 within `max` ($4,999 → $5,000); under $50 of room, whole dollars. */
export const planAmount = (n: number, max: number) =>
  Math.max(0, max < 50 ? Math.floor(Math.min(n, max)) : Math.min(Math.round(n / 50) * 50, Math.floor(max / 50) * 50))

export type TicketInput = {
  side: Side
  amount: number // USD notional
  price: number // live price (0 when unknown)
  posQty: number // current signed position in this symbol
  buyingPower: number
  stop: number | null
  take: number | null
  leverage?: number // > 1: the amount field is margin, so the cap is stated as margin
}
export type TicketErrors = { amount?: string; stop?: string; take?: string }

/**
 * Client-side mirror of the order rules: min $10, buying power, and stop/target on the right side of
 * the price for the position the order leaves behind (long: stop < price < target; short: reverse).
 */
export function checkTicket(t: TicketInput): TicketErrors {
  const e: TicketErrors = {}
  if (!(t.amount > 0)) e.amount = 'Enter an amount in USD.'
  else if (t.amount < MIN_NOTIONAL) e.amount = `The minimum order is ${fmtUsd(MIN_NOTIONAL, { decimals: 0 })}.`
  if (!(t.price > 0)) return e

  const qty = resultingQty(t.posQty, t.side, t.amount > 0 ? t.amount : 0, t.price)
  const added = (Math.abs(qty) - Math.abs(t.posQty)) * t.price // exposure this order adds
  const max = Math.max(0, t.buyingPower * MAX_FILL)
  const lev = t.leverage ?? 1
  if (!e.amount && added > max) e.amount = `Above your buying power. The most you can add is about ${fmtUsd(max / lev)}${lev > 1 ? ' of margin' : ''}.`

  const flat = Math.abs(qty * t.price) < 0.01
  const long = qty > 0
  const check = (v: number | null, isStop: boolean) => {
    if (v == null) return undefined
    if (!(v > 0)) return 'Enter a price above zero.'
    if (flat) return 'This order closes the position, so there is nothing to protect.'
    const below = isStop === long // long stop and short target sit below the price
    if (below && v >= t.price) return `For a ${long ? 'long' : 'short'}, set this below the price.`
    if (!below && v <= t.price) return `For a ${long ? 'long' : 'short'}, set this above the price.`
    return undefined
  }
  const stop = check(t.stop, true)
  const take = check(t.take, false)
  if (stop) e.stop = stop
  if (take) e.take = take
  return e
}

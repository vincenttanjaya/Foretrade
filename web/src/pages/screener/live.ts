// The screener's live quotes: their labels, their freshness and the in-place refresh of the loaded rows.
import { basisText, delaySeconds, freshness, priceLabel, sourceName, type LabelQuote } from '../../lib/quoteLabel'
import type { ScreenerQuote, ScreenerRow, ScreenerSortKey } from '../../lib/types'
import { utcClock } from '../strategies/quoteFeeds'

export { sourceName }

/** The light endpoint's limit: at most 100 tickers a call. */
export const QUOTES_PER_CALL = 100

/** ISO time → "03:31:12 UTC"; '' when it does not parse. */
export function utcSeconds(iso: string | null | undefined) {
  const d = new Date(iso ?? '')
  return Number.isNaN(d.getTime()) ? '' : `${d.toISOString().slice(11, 19)} UTC`
}

/** ISO time → "Oct 13" (its UTC date: every Asian session here trades within one UTC day); '' when it does not parse. */
export function utcDay(iso: string | null | undefined) {
  const d = new Date(iso ?? '')
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleDateString('en-US', { month: 'short', day: 'numeric', timeZone: 'UTC' })
}

/**
 * A quote's label, from its own fields and never "live" for a delayed price (lib/quoteLabel): a real-time source under a
 * minute behind reads "real-time · 03:31:12 UTC", one 1 to 5 minutes behind (SGX's snapshot) "about 2 min behind · 03:31
 * UTC", anything else, an unknown delay included, "delayed · last trade 03:31 UTC".
 */
export const quoteLabel = (q: LabelQuote) => priceLabel(q)

/**
 * A label with one place to wrap, before its time ("delayed · last trade" / "06:30 UTC"), so its column can narrow
 * enough for the whole table to fit a 1440 px screen; where there is room it stays on one line.
 */
export function breakBeforeTime(label: string) {
  const i = label.search(/ \d{2}:\d{2}/)
  const keep = (t: string) => t.replaceAll(' ', '\u00a0')
  return i < 0 ? keep(label) : `${keep(label.slice(0, i))} ${keep(label.slice(i + 1))}`
}

/** Stale: the API said so when it answered, or this browser's clock has passed the quote's `staleAt` since. */
export function quoteStale(q: Pick<ScreenerQuote, 'stale' | 'staleAt'>, now = Date.now()) {
  const at = Date.parse(q.staleAt)
  return q.stale || (Number.isFinite(at) && now >= at)
}

const SESSION: Record<ScreenerQuote['session'], string> = { open: 'Open', closed: 'Closed', unknown: 'Session unknown' }

/** The line under the label: the listing's session now and the last trade's date, "Closed · Oct 13". */
export function sessionLine(q: Pick<ScreenerQuote, 'session' | 'lastTradeAt'>) {
  const day = utcDay(q.lastTradeAt)
  return `${SESSION[q.session] ?? SESSION.unknown}${day ? ` · ${day}` : ''}`
}

/** "every minute", "every 5 min"; a quote held to the next open (null) is read again at the open. */
export function refreshText(seconds: number | null) {
  if (seconds == null || !(seconds > 0)) return 'read again at the next open'
  if (seconds < 60) return `re-read every ${seconds} s`
  if (seconds === 60) return 're-read every minute'
  return `re-read every ${Math.round(seconds / 60)} min`
}

/** How far the source runs behind the exchange: "about 15 min behind the exchange (measured)", "real-time (2 s)". */
export function delayText(q: Pick<ScreenerQuote, 'delayMinutes' | 'delayBasis'> & Partial<Pick<LabelQuote, 'source' | 'delaySeconds' | 'lastTradeAt'>>) {
  const d = delaySeconds(q)
  if (d == null) return 'delay unknown'
  const basis = q.delayBasis === 'stated' ? ' (as the source states)' : q.delayBasis === 'measured' ? ' (measured)' : ''
  if (freshness({ lastTradeAt: '', ...q }) === 'realtime') return d === 0 && q.delaySeconds == null ? 'real-time' : `real-time (${Math.round(d)} s behind)`
  const minutes = Math.round(d / 60)
  return `${minutes < 1 ? 'under a minute' : `about ${minutes} min`} behind the exchange${basis}`
}

/** The label's tooltip: whose quote, how delayed, when it was read and how often, and (a real-time quote) what its time is. */
export function quoteTitle(q: ScreenerQuote) {
  const read = utcClock(q.readAt)
  const basis = basisText(q.timeBasis)
  return `${sourceName(q.source)} · ${delayText(q)} · ${read ? `read ${read}, ` : ''}${refreshText(q.refreshSeconds)}${basis ? ` · ${basis}` : ''}`
}

/** The Stale badge's tooltip: since when nothing newer was read. */
export function staleTitle(q: ScreenerQuote) {
  const read = utcClock(q.readAt)
  return `No newer quote${read ? ` since ${read}` : ''} (${refreshText(q.refreshSeconds)}): the price and the columns from it may be out of date.`
}

/**
 * A new all-time high from the live price: the API then sends the price as the high, dated the trade's day, with a 0%
 * drawdown (a high the run found stays dated by its week).
 */
export const liveHigh = (r: Pick<ScreenerRow, 'quote' | 'drawdownPct' | 'athAt'>) =>
  r.quote != null && r.drawdownPct === 0 && r.athAt != null && r.athAt === r.quote.lastTradeAt.slice(0, 10)

/** The tickers the in-place refresh asks for: every loaded row with data (a row with no data feed stays as it is). */
export const refreshTickers = (rows: ScreenerRow[]) => rows.filter((r) => r.status !== 'unavailable').map((r) => r.ticker)

export function chunk<T>(items: T[], size: number): T[][] {
  const out: T[][] = []
  for (let i = 0; i < items.length; i += size) out.push(items.slice(i, i + size))
  return out
}

/** Fresh rows replace the loaded ones in place, by ticker: the order stays as loaded, and rows not returned are kept. */
export function mergeRows(rows: ScreenerRow[], fresh: ScreenerRow[]): ScreenerRow[] {
  if (!fresh.length) return rows
  const byTicker = new Map(fresh.map((r) => [r.ticker, r]))
  return rows.map((r) => byTicker.get(r.ticker) ?? r)
}

/** The value a sort orders by, as the API orders it (`updated`: the quote's last trade for a live row, else the run's time). */
export function sortValue(r: ScreenerRow, key: ScreenerSortKey): number | string | null {
  switch (key) {
    case 'price':
      return r.price
    case 'priceUsd':
      return r.priceUsd
    case 'pctVsSma':
      return r.pctVsSma
    case 'direction':
      return r.pctVsSma != null && r.pctVsSmaPrev != null ? r.pctVsSma - r.pctVsSmaPrev : null
    case 'rsi':
      return r.rsi14w
    case 'drawdown':
      return r.drawdownPct
    case 'athAt':
      return r.athAt
    case 'updated':
      return r.quote?.lastTradeAt ?? r.updatedAt
    default:
      return r.ticker // ticker, name, market: a quote never moves them
  }
}

/**
 * Whether fresh rows could change the order or the tab counts the page was loaded with: a moved value in the sorted
 * column, or in the 200-week distance that every band reads (the RSI the oversold band also reads is the run's).
 */
export function orderMoved(rows: ScreenerRow[], fresh: ScreenerRow[], key: ScreenerSortKey) {
  const loaded = new Map(rows.map((r) => [r.ticker, r]))
  return fresh.some((f) => {
    const r = loaded.get(f.ticker)
    return r != null && (sortValue(r, key) !== sortValue(f, key) || r.pctVsSma !== f.pctVsSma)
  })
}

/** "every minute", "every 2 min", "every 45 s": how often the page refreshes its rows. */
export const everyText = (seconds: number) =>
  seconds === 60 ? 'every minute' : seconds % 60 === 0 ? `every ${seconds / 60} min` : `every ${seconds} s`

/** The page's refresh interval from the API's `pollSeconds`, kept within 15 s to 10 min; null: no in-place refresh. */
export function pollMs(seconds: number | null | undefined) {
  if (typeof seconds !== 'number' || !Number.isFinite(seconds) || seconds <= 0) return null
  return Math.min(600, Math.max(15, Math.round(seconds))) * 1000
}

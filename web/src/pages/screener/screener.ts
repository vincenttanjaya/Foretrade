// Pure helpers for the Asia screener page: query parsing, labels, local-currency prices and the agent prompt.
import { fmtDate, fmtPct } from '../../lib/format'
import type { ScreenerFilter, ScreenerMarket, ScreenerRow, ScreenerSort, ScreenerSortKey } from '../../lib/types'
import { athWithheld } from '../automations/pipeline'
import { liveHigh, quoteLabel, utcDay } from './live'

export const PAGE_SIZE = 50

export const FILTERS: { value: ScreenerFilter; label: string; hint: string }[] = [
  { value: 'all', label: 'All', hint: 'Every constituent' },
  { value: 'below', label: 'Below', hint: '5–50% below the 200-week average' },
  { value: 'deep', label: 'Deep value', hint: '50–70% below the 200-week average' },
  { value: 'distressed', label: 'Distressed', hint: '70% or more below the 200-week average' },
  { value: 'approaching', label: 'Approaching', hint: 'Within 15% above the 200-week average' },
  { value: 'oversold', label: 'Oversold', hint: '14-week RSI under 30 and below the 200-week average' },
]

export const MARKETS: ScreenerMarket[] = ['JP', 'KR', 'TW', 'HK', 'SG', 'ID', 'TH', 'MY', 'PH', 'VN']

const SORT_KEYS: ScreenerSortKey[] = ['ticker', 'name', 'market', 'price', 'priceUsd', 'pctVsSma', 'direction', 'rsi', 'drawdown', 'athAt', 'updated']

/** The first click on a column sorts the way a screen is read: deepest below, most oversold, latest first. */
export const FIRST_DIRECTION: Record<ScreenerSortKey, 'asc' | 'desc'> = {
  ticker: 'asc', name: 'asc', market: 'asc', price: 'desc', priceUsd: 'desc', pctVsSma: 'asc', direction: 'asc',
  rsi: 'asc', drawdown: 'asc', athAt: 'desc', updated: 'desc',
}

export const DEFAULT_SORT: ScreenerSort = 'pctVsSma'

export type Query = { filter: ScreenerFilter; market: ScreenerMarket | ''; q: string; sort: ScreenerSort }

/** The page's query from its URL; anything unknown falls back to the default. */
export function readQuery(params: URLSearchParams): Query {
  const filter = params.get('filter') as ScreenerFilter
  const market = (params.get('market') ?? '').toUpperCase() as ScreenerMarket
  const sort = params.get('sort') ?? ''
  return {
    filter: FILTERS.some((f) => f.value === filter) ? filter : 'all',
    market: MARKETS.includes(market) ? market : '',
    q: (params.get('q') ?? '').slice(0, 64),
    sort: SORT_KEYS.includes(sort.replace(/^-/, '') as ScreenerSortKey) ? (sort as ScreenerSort) : DEFAULT_SORT,
  }
}

export function sortParts(sort: ScreenerSort): { key: ScreenerSortKey; desc: boolean } {
  return { key: sort.replace(/^-/, '') as ScreenerSortKey, desc: sort.startsWith('-') }
}

/** Clicking a column: its first direction, then the other way. */
export function nextSort(current: ScreenerSort, key: ScreenerSortKey): ScreenerSort {
  const now = sortParts(current)
  const desc = now.key === key ? !now.desc : FIRST_DIRECTION[key] === 'desc'
  return (desc ? `-${key}` : key) as ScreenerSort
}

/** A local-currency price: "¥2,930", "₩272,000", "NT$2,585", "SGD 78.56"; sub-unit prices keep 4 decimals. */
export function fmtLocal(n: number | null | undefined, currency: string) {
  if (typeof n !== 'number' || !Number.isFinite(n)) return '—'
  const a = Math.abs(n)
  const [min, max] = a >= 1000 ? [0, 2] : a >= 1 ? [2, 2] : [2, 4]
  try {
    return n.toLocaleString('en-US', { style: 'currency', currency, minimumFractionDigits: min, maximumFractionDigits: max })
  } catch {
    return `${n.toLocaleString('en-US', { minimumFractionDigits: min, maximumFractionDigits: max })} ${currency}`
  }
}

/** What a row's status means, for a cell that has no value to show. */
export function statusLabel(r: ScreenerRow): string {
  switch (r.status) {
    case 'pending':
      return 'Not screened yet'
    case 'unavailable':
      return 'Unavailable (no data feed)'
    case 'insufficient_history':
      return `n/a, insufficient history${r.weeks != null ? ` (${r.weeks} wks)` : ''}`
    case 'failed':
      return r.updatedAt ? 'Last refresh failed' : 'Refresh failed'
    default:
      return '—'
  }
}

export const indexNames = (r: ScreenerRow) => r.indices.map((i) => i.index).join(', ')

/**
 * The row's all-time high is withheld: over 100× the price (reverse splits). The API sends it null then, with
 * `athAt` and `drawdownPct`, while a computed row otherwise always has one; an older response may still carry it.
 */
export const athHidden = (r: ScreenerRow) => athWithheld(r.ath, r.price) || (r.ath == null && r.price != null && r.updatedAt != null)

/** The research prompt "View" opens the agent with: the row's measured facts (its quote labelled as the page labels it), then the question. */
export function viewPrompt(r: ScreenerRow, marketName: string): string {
  const facts: string[] = []
  if (r.quote && r.price != null) {
    const day = utcDay(r.quote.lastTradeAt)
    facts.push(`a price of ${fmtLocal(r.price, r.currency)} (${quoteLabel(r.quote)}${day ? ` on ${day}` : ''})`)
  }
  if (r.pctVsSma != null) {
    const dir = r.direction ? ` and ${r.direction} week over week` : ''
    facts.push(`${fmtPct(r.pctVsSma, { sign: true, decimals: 1 })} vs its 200-week moving average${dir}`)
  } else if (r.status === 'insufficient_history' && r.weeks != null) {
    facts.push(`listed on Yahoo for only ${r.weeks} weeks, too short for a 200-week average`)
  }
  if (r.rsi14w != null) facts.push(`a 14-week RSI of ${r.rsi14w.toFixed(1)}`)
  if (r.drawdownPct != null && r.athAt && !athHidden(r))
    facts.push(
      liveHigh(r)
        ? `a new all-time high on ${fmtDate(r.athAt)}`
        : `${fmtPct(Math.abs(r.drawdownPct), { decimals: 1 })} below its all-time high from the week of ${fmtDate(r.athAt)}`,
    )
  const screen = facts.length ? ` The Asia screener shows ${facts.join(', ')}.` : ''
  return (
    `Analyse ${r.name} (${r.ticker}, ${marketName}, ${indexNames(r)}).${screen} ` +
    'Is it a long-term opportunity or a value trap? Cover the business, valuation, catalysts and risks. ' +
    'It is research only here: there is no paper market for it.'
  )
}


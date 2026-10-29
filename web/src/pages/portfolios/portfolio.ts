// Pure helpers for the AI portfolio pages. Checked by portfolio.test.ts.
import type { AIPortfolio, Point } from '../../lib/types'
import { fmtDate } from '../../lib/format'

export const MIN_INVEST = 50 // contract: POST /api/ai-portfolios/{id}/invest amount ≥ 50
export const MIN_ORDER = 10 // the paper engine's minimum notional; smaller slices are skipped by the API

export type Range = '1M' | '3M' | '6M'
export const RANGE_DAYS: Record<Range, number> = { '1M': 30, '3M': 90, '6M': 180 }

export type RangeStats = { returnPct: number; maxDrawdownPct: number; volatilityAnnPct: number | null }

/**
 * Return, max drawdown (positive magnitude) and annualized volatility of a daily index series —
 * the same math as the API's backtest (stdev of log returns, n−1, × √365). null under 2 points.
 */
export function rangeStats(series: Point[]): RangeStats | null {
  const v = series.map((p) => p.v)
  if (v.length < 2 || !v.every((x) => Number.isFinite(x) && x > 0)) return null
  let peak = v[0]
  let dd = 0
  for (const x of v) {
    peak = Math.max(peak, x)
    dd = Math.max(dd, ((peak - x) / peak) * 100)
  }
  const r = v.slice(1).map((x, i) => Math.log(x / v[i]))
  const mean = r.reduce((a, b) => a + b, 0) / r.length
  const sd = r.length > 1 ? Math.sqrt(r.reduce((a, b) => a + (b - mean) ** 2, 0) / (r.length - 1)) : 0
  return { returnPct: (v.at(-1)! / v[0] - 1) * 100, maxDrawdownPct: dd, volatilityAnnPct: series.length >= 31 && series.every((p, i) => !i || p.t - series[i - 1].t === series[1].t - series[0].t) ? sd * Math.sqrt(365) * 100 : null }
}

/**
 * Stats of the since-invest chart: the actual series is per 100 of cost (fees included), so its return is last − 100,
 * not last ÷ first; volatility is annualized for the series' own bar length (1h, 4h or 1d). null under 2 points.
 */
export function sinceStats(series: Point[], benchmark: Point[]): (RangeStats & { benchmarkReturnPct: number | null }) | null {
  const s = rangeStats(series)
  if (!s) return null
  const step = series[1].t - series[0].t
  const regular = [3600, 14400, 86400].includes(step) && series.every((p, i) => !i || p.t - series[i - 1].t === step)
  const aligned = benchmark.length === series.length && benchmark.every((p, i) => p.t === series[i].t && Number.isFinite(p.v) && p.v > 0)
  return { ...s, returnPct: series.at(-1)!.v - 100, volatilityAnnPct: regular && s.volatilityAnnPct != null ? s.volatilityAnnPct * Math.sqrt(86400 / step) : null, benchmarkReturnPct: aligned ? benchmark.at(-1)!.v - 100 : null }
}

/** Saved identity wins over current holdings, prices or later investments. */
export function comparison(value: { benchmarkSymbol?: string | null; benchmarkLabel?: string | null } | undefined) {
  const symbol = value?.benchmarkSymbol || 'BTC'
  return { symbol, label: value?.benchmarkLabel || (symbol === 'SP500' ? 'S&P 500 perp' : symbol) }
}

/** Actual source bounds/count, with legacy bounds derived only from measured points. */
export function measuredWindow(value: { windowStart?: number | null; windowEnd?: number | null; bars?: number | null; series?: Point[] }, kind = 'daily') {
  const start = value.windowStart ?? value.series?.[0]?.t
  const end = value.windowEnd ?? value.series?.at(-1)?.t
  const bars = value.bars || value.series?.length || 0
  if (!(start != null && end != null && start > 0 && end >= start && bars > 0)) return 'Window unavailable'
  const date = (t: number) => {
    const utc = new Date(t * 1000).toISOString()
    return `${fmtDate(utc.slice(0, 10))}${kind === 'plotted' ? ` ${utc.slice(11, 16)}` : ''}`
  }
  return `${bars} ${kind} bars · ${date(start)} – ${date(end)} (UTC)`
}

export type Slice = { symbol: string; weight: number; notional: number; skipped: boolean }

/** amount × weight per holding, rounded to cents like the API; slices under the $10 minimum are skipped. */
export function allocate(amount: number, holdings: AIPortfolio['holdings']): Slice[] {
  return holdings.map((h) => {
    const notional = Number.isFinite(amount) ? Math.round(amount * h.weight * 100) / 100 : 0
    return { symbol: h.symbol, weight: h.weight, notional, skipped: notional < MIN_ORDER }
  })
}

/** Weights as fractions of their sum (the API already sums them to 1; this keeps the visuals honest if not). */
export function shares(holdings: AIPortfolio['holdings']) {
  const total = holdings.reduce((a, h) => a + (h.weight > 0 ? h.weight : 0), 0)
  return holdings.map((h) => (total > 0 && h.weight > 0 ? h.weight / total : 0))
}

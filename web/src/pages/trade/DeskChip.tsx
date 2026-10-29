import { useEffect, useState } from 'react'
import { Link } from 'react-router'
import { api } from '../../lib/api'
import type { DeskRead } from '../../lib/types'
import { fmtDay, fmtThreshold } from '../forecasts/forecast'

const pct = (p: number) => `${Math.round(p * 100)}%`

/** "SOL above $125 on Oct 9" for a price rule, else the question clipped to 60 characters. */
export function deskSubject(d: DeskRead) {
  const r = d.priceRule
  if (r) return `${r.symbol} ${r.op} ${fmtThreshold(r.threshold)} ${r.mode === 'by_date' ? 'by' : 'on'} ${fmtDay(d.resolutionDate)}`
  return d.question.length > 60 ? `${d.question.slice(0, 59)}…` : d.question
}

/** "Yes 28% (leans No)": a binary Yes under 50% never reads as support. */
export const deskHeadline = (d: DeskRead) => `${d.outcome} ${pct(d.p)}${d.outcome === 'Yes' && d.p < 0.5 ? ' (leans No)' : ''}`

/** The idea the desk link pre-fills: "Will SOL close above $210 on Oct 10?" (price × 1.05 to 2 significant figures, a week out). */
export function deskIdea(symbol: string, price: number, now = Date.now()) {
  const target = Number((price * 1.05).toPrecision(2))
  return `Will ${symbol} close above ${fmtThreshold(target)} on ${fmtDay(new Date(now + 7 * 86_400_000).toISOString())}?`
}

/** The user's forecast desk on the symbol, fetched once per symbol (no polling): undefined while loading, null when there is none. */
export function useDesk(symbol: string) {
  const [desk, setDesk] = useState<DeskRead | null>()
  useEffect(() => {
    const ctrl = new AbortController()
    api.getDesk(symbol, ctrl.signal).then(
      (r) => setDesk(r.desk ?? null),
      () => !ctrl.signal.aborted && setDesk(null), // ponytail: a failed read shows the ask link; the desk is optional context
    )
    return () => ctrl.abort()
  }, [symbol])
  return desk
}

/** The desk's read as a link to its report, or (no read) a muted link that drafts a question about the symbol. */
export function DeskChip({ symbol, price, read }: { symbol: string; price?: number; read: DeskRead | null }) {
  if (!read)
    return (
      <Link
        to="/forecasts/new"
        state={price ? { idea: deskIdea(symbol, price) } : undefined}
        className="text-[12.5px] text-muted hover:text-accent-ink hover:underline"
      >
        Ask the desk about {symbol} →
      </Link>
    )
  return (
    <Link
      to={`/forecasts/${read.forecastId}`}
      title={read.question}
      className="inline-flex max-w-full flex-wrap items-center gap-x-1.5 gap-y-0.5 rounded-lg border border-border bg-surface-2/60 px-2.5 py-1 text-[12.5px] text-text transition-colors hover:border-accent/50 hover:text-accent-ink"
    >
      <span className="shrink-0 font-mono text-[10.5px] tracking-[0.08em] text-muted uppercase">Forecast desk</span>
      <span aria-hidden="true" className="text-muted">
        {' · '}
      </span>
      <span className="shrink-0 font-medium tabular-nums">{deskHeadline(read)}</span>
      <span aria-hidden="true" className="text-muted">
        {' · '}
      </span>
      <span className="min-w-0">{deskSubject(read)}</span>
      <span aria-hidden="true">{' →'}</span>
    </Link>
  )
}

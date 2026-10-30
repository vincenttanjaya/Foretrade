// H1 on a numeric report (API contract): the subject's own measured daily history the specialists saw, from
// the market data source (never from model or web text). Scenario conditions that cite H1 link here.
import type { ReactNode } from 'react'
import { Card } from '../../components/ui/Card'
import type { DeskMeasured } from '../../lib/types'
import { ExternalLink } from './DeskVsMarket'
import { fmtAnnual, fmtChange, fmtDay, pct0 } from './forecast'

const year = (iso: string) => iso.slice(0, 4)
const month = (iso: string) => {
  const d = new Date(`${iso.slice(0, 10)}T00:00:00Z`)
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString('en-US', { month: 'short', year: 'numeric', timeZone: 'UTC' })
}
const pctOrDash = (x: number | null, signed = false) => (x == null ? '—' : signed ? fmtAnnual(x) : `${Math.round(x)}%`)

/** "5-year" for 1,825 days, "90-day" otherwise. */
export const windowSpan = (days: number) => (days >= 365 && Math.abs(days / 365 - Math.round(days / 365)) < 0.02 ? `${Math.round(days / 365)}-year` : `${days.toLocaleString('en-US')}-day`)

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid grid-cols-[6.5rem_1fr] gap-3 py-2.5">
      <dt className="label self-baseline leading-tight">{label}</dt>
      <dd className="min-w-0 text-[13px] leading-snug text-text tabular-nums">{children}</dd>
    </div>
  )
}

export function MeasuredHistory({ m }: { m: DeskMeasured }) {
  const dd = m.maxDrawdown
  const w = m.window
  return (
    <Card id="measured" className="scroll-mt-6 p-5 print:break-inside-avoid">
      <div className="label">Measured history · H1</div>
      <h3 className="mt-1 font-display text-[19px] font-medium text-text">How {m.symbol || m.name} has moved</h3>
      <p className="mt-1 font-mono text-[11px] leading-snug text-muted">
        {m.url ? <ExternalLink href={m.url}>{m.source}</ExternalLink> : m.source} · {fmtDay(m.from, true)} – {fmtDay(m.to, true)}
        {m.sessions > 0 && ` · ${m.sessions.toLocaleString('en-US')} sessions`}
      </p>
      <dl className="mt-2 divide-y divide-border">
        <Row label="Volatility">
          <span className="font-mono">
            {pctOrDash(m.volAnnPct.y1)} 1y · {pctOrDash(m.volAnnPct.y5)} 5y · {pctOrDash(m.volAnnPct.all)} full
          </span>
          <span className="text-muted"> a year</span>
        </Row>
        <Row label="Price CAGR">
          <span className="font-mono">
            {pctOrDash(m.cagrPct.y5, true)} 5y · {pctOrDash(m.cagrPct.y10, true)} 10y · {pctOrDash(m.cagrPct.all, true)} since {year(m.from)}
          </span>
        </Row>
        {dd && (
          <Row label="Max drawdown">
            <span className="font-mono">{fmtChange(dd.pct, true)}</span>{' '}
            <span className="text-muted">
              (peak {month(dd.peak)}
              {dd.trough && `, trough ${month(dd.trough)}`}, {dd.recovered ? `recovered ${month(dd.recovered)}` : 'not recovered'})
            </span>
          </Row>
        )}
      </dl>
      {w && (
        <p className="mt-2 border-t border-border pt-3 text-[13px] leading-snug text-text">
          Past {windowSpan(w.days)} windows: {pct0(w.pUp)}% ended higher ({w.independent.toLocaleString('en-US')} independent of {w.n.toLocaleString('en-US')} overlapping)
          <span className="mt-0.5 block font-mono text-[11.5px] text-muted tabular-nums">
            ending multiple ×{w.p50.toFixed(2)} median · ×{w.p10.toFixed(2)}–×{w.p90.toFixed(2)} for 80%
          </span>
        </p>
      )}
      <p className="mt-3 text-[12px] leading-snug text-muted">Price returns, dividends excluded. Past windows overlap, start from every point of the cycle and come from an asset that survived to be asked about, so they flatter what comes next.</p>
    </Card>
  )
}

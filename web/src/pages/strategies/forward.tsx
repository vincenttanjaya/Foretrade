// Saved-rule replay from candles, separate from the backtest and private autopilot fills.
import type { ReactNode } from 'react'
import { Badge } from '../../components/ui/Badge'
import { Button } from '../../components/ui/Button'
import { Card, CardHeader } from '../../components/ui/Card'
import { LineChart } from '../../components/ui/LineChart'
import { PriceChange } from '../../components/ui/PriceChange'
import { Select } from '../../components/ui/Input'
import { cx } from '../../lib/cx'
import { fmtDate, fmtPct } from '../../lib/format'
import type { ForwardSummary, Metrics, Strategy } from '../../lib/types'
import { earlyReason, fmtDD, fmtIndex, fmtNum, fmtWindow, forwardLabel, forwardMark, isoAt } from './strategy'

const EARLY_NOTE = 'Under 30 days or 3 trades: too early to judge either way.'

/** Preserve the API's exact UTC precision, including publication microseconds. */
const instant = (iso: string) => iso.replace('T', ' ').replace(/Z$/, ' UTC')
const pointInstant = (t: number) => instant(new Date(t * 1000).toISOString().replace('.000Z', 'Z'))
const preciseKey = (iso: string) => iso.replace(/(?:\.(\d+))?Z$/, (_, fraction: string = '') => `.${fraction.padEnd(9, '0')}Z`)
const measuredWindow = (m: Metrics) => fmtWindow(m).replace(' d', ' days').replace(' y', ' years')

const REASONS: Record<NonNullable<ForwardSummary['reason']>, string> = {
  too_new: 'Too new to measure: waiting for three eligible closed bars.',
  history_unavailable: 'The complete history and indicator warm-up since this boundary are unavailable.',
  market_unavailable: 'Market data is unavailable right now.',
  timeout: 'The forward time budget was reached. Try again in a minute.',
  busy: 'Live forward is busy right now. Try again in a minute.',
}
const reason = (f: ForwardSummary) => f.reason ? REASONS[f.reason] : f.stale ? 'The source has not reached the newest expected candle close.' : ''

export function EarlyBadge({ f }: { f: ForwardSummary }) {
  return <Badge title={`${earlyReason(f) ?? 'No measured window'}. ${EARLY_NOTE}`}>Early</Badge>
}

export function ForwardSort({ label, value, onChange }: { label: string; value: 'default' | 'forward'; onChange: (v: 'default' | 'forward') => void }) {
  return (
    <label className="flex flex-wrap items-center gap-2 text-[12px] text-muted">
      Order
      <Select aria-label={label} value={value} onChange={(e) => onChange(e.target.value as 'default' | 'forward')} className="h-8! py-1! text-[12px]!">
        <option value="default">Default order</option>
        <option value="forward">Live forward return</option>
      </Select>
    </label>
  )
}

/** Source dates and measured windows stay attached to cached returns. */
export function ForwardLine({ f, symbol, className }: { f: ForwardSummary | null | undefined; symbol: string; className?: string }) {
  if (f === undefined) return null // backward compatibility for older saved/seeded payloads
  const box = cx('flex flex-wrap items-center gap-x-1.5 gap-y-1 text-[12px] text-muted', className)
  if (f === null) return <p className={box}>Live forward unavailable</p>
  const m = f.metrics
  return (
    <div className={box}>
      <span className="font-medium text-text">{forwardLabel(f)} · {fmtDate(f.since.split('T')[0])}</span>
      {m ? <>
        <PriceChange value={m.totalReturnPct} decimals={1} className="text-[12.5px]" />
        <span className="font-mono tabular-nums">· {measuredWindow(m)} · B&H {symbol} {fmtPct(m.buyHoldReturnPct, { sign: true, decimals: 1 })}</span>
      </> : <span>· {reason(f) || 'Live forward unavailable.'} No measured window.</span>}
      {m && f.early && <EarlyBadge f={f} />}
      {f.stale && <Badge title={reason(f)}>Stale</Badge>}
      {f.asOf && <span className="basis-full text-[11px]">Source as of {instant(f.asOf)}{m && f.stale ? ` · ${reason(f)}` : ''}</span>}
    </div>
  )
}

const edge = (m: Metrics) => {
  const d = m.totalReturnPct - m.buyHoldReturnPct
  return `${d >= 0 ? '+' : '−'}${Math.abs(d).toFixed(1)} pts`
}

/** Saved and forward simulations each start flat at 100; neither inherits the other's account. */
export function ForwardCard({ s, modified, onRefresh, refreshing }: { s: Strategy; modified: boolean; onRefresh?: () => void; refreshing?: boolean }) {
  const f = s.forward
  if (f === undefined) return null
  const fm = f?.metrics
  const symbol = s.spec.symbol
  const span = (m: Metrics) => `${fmtDate(isoAt(m.startT, true))} – ${fmtDate(isoAt(m.endT, true))}`
  // Points use integer seconds. Nonzero subsecond since excludes that second's bookkeeping anchor.
  const fractional = !!f?.since.match(/\.(\d*[1-9]\d*)(?:Z|[+-])/)
  const firstAllowed = f ? Math.floor(Date.parse(f.since) / 1000) + (fractional ? 1 : 0) : Infinity
  const equity = (f?.equity ?? []).filter((p) => p.t >= firstAllowed)
  const benchmark = (f?.benchmark ?? []).filter((p) => p.t >= firstAllowed)
  const first = equity[0]
  const exact = first?.t === firstAllowed && !fractional
  const gap = f?.basis === 'published' && preciseKey(f.since) > preciseKey(s.createdAt)
  const rows: [string, (x: Metrics) => ReactNode][] = [
    ['Return', (x) => <PriceChange value={x.totalReturnPct} decimals={1} />],
    [`Buy & hold ${symbol}`, (x) => fmtPct(x.buyHoldReturnPct, { sign: true, decimals: 1 })],
    ['Edge vs buy & hold', edge],
    ['Sharpe ratio', (x) => fmtNum(x.sharpe)],
    ['Max drawdown', (x) => fmtDD(x.maxDrawdownPct)],
    ['Trades', (x) => x.trades],
    ['Window', measuredWindow],
  ]

  return (
    <Card className="mt-6 min-w-0" role="region" aria-label="Live forward">
      <CardHeader label="In-sample vs out-of-sample" title={f ? forwardLabel(f) : 'Live forward'} actions={<>
        {fm && f?.early && <EarlyBadge f={f} />}
        {f?.stale && <Badge title={reason(f)}>Stale</Badge>}
        {onRefresh && <Button variant="secondary" size="sm" loading={refreshing} onClick={onRefresh}>Refresh live forward</Button>}
      </>} />
      <div className="px-5 pb-5">
        {f && <div className="mb-4 space-y-1 text-[12.5px] text-muted">
          <p>{forwardMark(f)} boundary: <time dateTime={f.since} className="font-mono text-text">{instant(f.since)}</time></p>
          <p>{f.asOf ? <>Source as of <time dateTime={f.asOf}>{instant(f.asOf)}</time></> : 'Source time unavailable.'}</p>
          {f.stale && <p>{reason(f)}{fm && ' The figures retain their original measured window.'}</p>}
        </div>}
        {!fm ? <p className="text-[13.5px] text-muted">{f ? reason(f) || 'Live forward is unavailable.' : 'Live forward is unavailable.'} No measured window.</p> : <>
          <div className="relative overflow-x-auto">
            <table aria-label="Saved backtest and live forward metrics" className="w-full min-w-[420px] text-[13.5px]">
              <thead><tr className="border-b border-border">
                <td />
                <th scope="col" className="label px-3 pb-2 text-right font-medium">Saved backtest · in-sample</th>
                <th scope="col" className="label pb-2 pl-3 text-right font-medium text-accent-ink">Live forward · out-of-sample</th>
              </tr></thead>
              <tbody className="divide-y divide-border">
                {rows.map(([label, cell]) => <tr key={label}>
                  <th scope="row" className="py-2 pr-3 text-left font-normal text-muted">{label}</th>
                  <td className="px-3 py-2 text-right font-mono text-text tabular-nums">{s.metrics ? cell(s.metrics) : '—'}</td>
                  <td className="py-2 pl-3 text-right font-mono font-medium text-text tabular-nums">{cell(fm)}</td>
                </tr>)}
                <tr><th scope="row" className="py-2 pr-3 text-left font-normal text-muted">Dates</th>
                  <td className="px-3 py-2 text-right text-[12px] text-muted">{span(s.metrics)}</td>
                  <td className="py-2 pl-3 text-right text-[12px] text-muted">{span(fm)}</td>
                </tr>
              </tbody>
            </table>
          </div>
          {equity.length > 1 ? <div className="mt-5">
            <LineChart height={260} format={fmtIndex} series={[
              { name: 'Live forward (saved rules)', points: equity, color: '#3B6FD8', width: 2 },
              { name: `Buy & hold ${symbol}`, points: benchmark, muted: true },
            ]} markers={first && f ? [{ series: 0, t: first.t, text: forwardMark(f) }] : []} />
            {first && f && <p className="mt-2 text-[12px] text-muted">{exact ? `${forwardMark(f)} marker: exact boundary.` : `First plotted bar: ${pointInstant(first.t)}. The marker does not show a price at the publication instant.`}</p>}
          </div> : <p className="mt-4 text-[12.5px] text-muted">No plotted bars after the boundary yet.</p>}
        </>}
        {f && <div className="mt-4 space-y-1.5 text-[12.5px] leading-relaxed text-muted">
          {fm && f.early && <p><span className="font-medium text-text">Early: {earlyReason(f)}.</span> {EARLY_NOTE}</p>}
          {fm?.trades === 0 && <p>No trades yet: the entry rules haven't fired, so the measured forward reads flat.</p>}
          <p>The saved rules replay closed candles with the backtest's fees and fills. The forward simulation starts flat at 100; no backtest positions carry over. Buy & hold buys the same first eligible open. No model call; autopilot results stay private.</p>
          <p>The charts show separate simulations at their original scale. Any pre-boundary bookkeeping anchor is omitted from the forward chart.</p>
          {gap && <p>The time between saving and publishing is unmeasured; it belongs to neither window. No line connects the two simulations.</p>}
          {modified && <p className="text-accent-ink">The live forward is for the saved rules. It does not measure the modified rules on screen.</p>}
        </div>}
      </div>
    </Card>
  )
}

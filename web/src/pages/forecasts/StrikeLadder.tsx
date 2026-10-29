// Desk vs market by strike (round 6): an ordered price report's buckets against Polymarket's daily "above" ladder
// (API contract "Desk vs market by strike (round 6)").
import { useState } from 'react'
import { Badge } from '../../components/ui/Badge'
import { Button } from '../../components/ui/Button'
import { Card } from '../../components/ui/Card'
import { IconAlert } from '../../components/ui/icons'
import { useToast } from '../../components/toast'
import { api } from '../../lib/api'
import { cx } from '../../lib/cx'
import { errorMessage } from '../../lib/errors'
import { fmtUsd } from '../../lib/format'
import type { Forecast, ForecastResult, MarketOdds } from '../../lib/types'
import { ExternalLink, SearchAgain } from './DeskVsMarket'
import { fmtDay, fmtPts, httpUrl, isThin } from './forecast'

// Tighter cells and stacked lean badges below sm keep all five columns inside a 375 px card.
const th = 'label px-1.5 py-2 font-medium whitespace-nowrap first:pl-0 last:pr-0 sm:px-3'
const td = 'px-1.5 py-2.5 whitespace-nowrap first:pl-0 last:pr-0 sm:px-3'
const num = 'text-right font-mono tabular-nums'

/** "41%", "38.5%". */
const pct1 = (p: number) => `${Math.round(p * 1000) / 10}%`
/** "Oct 9, 16:00 UTC": Polymarket's ladders settle at noon ET, so a local date can read a day off. */
const utc = (iso: string) => {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : `${fmtDay(iso)}, ${d.toISOString().slice(11, 16)} UTC`
}
/** The desk's P(above): the market's price plus the edge (the API rounds the edge to 0.1 pt). */
const deskOf = (m: MarketOdds) => m.yesPrice + m.edgePp / 100
const LEAN = { yes: ['YES', 'up'], no: ['NO', 'down'], wait: ['WAIT', 'neutral'] } as const

/**
 * The ladder card on an ordered, non-replay report: per strike the desk's P(close above) from its buckets, Polymarket's
 * price, the edge and a lean, with Refresh odds and Search again. No ladder: why, and Search again.
 */
export function StrikeLadder({ f, r, onChange }: { f: Forecast; r: ForecastResult; onChange: (f: Forecast) => void }) {
  const { toast } = useToast()
  const [busy, setBusy] = useState(false)
  const live = !f.replay && f.status === 'completed'
  const rows = r.ladder ?? []
  if (!rows.length && !live) return null

  async function refresh(rematch: boolean) {
    setBusy(true)
    try {
      onChange((await api.refreshMarket(f.id, rematch)).forecast)
    } catch (err) {
      toast(errorMessage(err), 'error')
    } finally {
      setBusy(false)
    }
  }

  const heading = (
    <h2 id="strike-ladder" className="label">
      Desk vs market by strike
    </h2>
  )
  const searchAgain = live && <SearchAgain checkedAt={r.marketCheckedAt} busy={busy} onClick={() => refresh(true)} />

  if (!rows.length) {
    const asset = /\b([A-Z0-9]{2,10})\/USDT\b/.exec(f.question)?.[1] ?? 'crypto'
    return (
      <Card className="mt-4 p-5 md:p-6 print:break-inside-avoid">
        <section aria-labelledby="strike-ladder">
          {heading}
          <div className="mt-3 flex flex-wrap items-center gap-x-4 gap-y-2">
            <p className="max-w-xl text-[13px] text-muted">
              No Polymarket strike ladder ends within a day of {fmtDay(f.resolutionDate, true)}. Polymarket lists daily {asset} price
              ladders about a week ahead.
            </p>
            {searchAgain}
          </div>
        </section>
      </Card>
    )
  }

  const first = rows[0]
  const asset = /price of (.+?) be above/i.exec(first.question)?.[1] ?? 'Price'
  const url = httpUrl(first.url)
  const volume = rows.reduce((a, m) => a + (m.volumeUsd ?? 0), 0)
  return (
    <Card className="mt-4 p-5 md:p-6 print:break-inside-avoid">
      <section aria-labelledby="strike-ladder">
        {heading}
        <p className="mt-1.5 text-[13px] leading-snug text-muted">
          Polymarket · {asset} above ___ on {fmtDay(first.endDate)} · ends {utc(first.endDate)} · {fmtUsd(volume, { compact: true })} volume
          {url && (
            <>
              {' · '}
              <ExternalLink href={url.href}>View on Polymarket</ExternalLink>
            </>
          )}
        </p>
        <div className="mt-3 overflow-x-auto">
          <table className="w-full text-[13px]">
            <thead className="border-b border-border text-left">
              <tr>
                <th className={th}>Strike</th>
                <th className={cx(th, 'text-right')}>Desk</th>
                <th className={cx(th, 'text-right')}>Market</th>
                <th className={cx(th, 'text-right')}>Edge</th>
                <th className={cx(th, 'text-right')}>Lean</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {rows.map((m) => {
                const thin = isThin(m)
                const [lean, tone] = LEAN[thin ? 'wait' : m.signal] ?? LEAN.wait
                return (
                  <tr key={m.question}>
                    <td className={cx(td, 'font-mono text-text tabular-nums')}>
                      {fmtUsd(m.strike ?? 0, { decimals: Number.isInteger(m.strike) ? 0 : 2 })}
                    </td>
                    <td className={cx(td, num, 'text-text')}>{pct1(deskOf(m))}</td>
                    <td className={cx(td, num, 'text-muted')}>{pct1(m.yesPrice)}</td>
                    <td className={cx(td, num, thin || !m.edgePp ? 'text-muted' : m.edgePp > 0 ? 'text-up' : 'text-down')}>{fmtPts(m.edgePp)}</td>
                    <td className={cx(td, 'text-right')}>
                      <span className="inline-flex flex-col items-end gap-1 sm:flex-row sm:items-center sm:gap-1.5">
                        {thin && <Badge tone="outline">thin</Badge>}
                        <Badge tone={tone}>{lean}</Badge>
                      </span>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
        {rows.some((m) => m.match === 'close') && (
          <p className="mt-3 flex gap-2 text-[13px] leading-snug text-text">
            <IconAlert size={15} className="mt-px shrink-0 text-accent-ink" />
            Close match: this ladder ends a day off the resolution date — read its rules.
          </p>
        )}
        {live && (
          <div className="mt-4 flex flex-wrap items-center gap-2 print:hidden">
            <Button variant="secondary" size="sm" loading={busy} onClick={() => refresh(false)}>
              Refresh odds
            </Button>
            {searchAgain}
            <span className="font-mono text-[11px] text-muted">as of {utc(first.asOf)}</span>
          </div>
        )}
        <p className="mt-4 border-t border-border pt-3 text-[12px] text-muted">
          Desk P(above) adds up the report's buckets, linear inside a bucket. Polymarket prices are crowd odds, not facts.
          Simulated · not financial advice.
        </p>
      </section>
    </Card>
  )
}

// The agent's "protection" card: new stop / target levels for a position the user holds. Confirm applies them with
// PATCH /api/portfolio/positions/:symbol; the agent never changes a position itself.
import { useState } from 'react'
import { Link } from 'react-router'
import { AssetIcon } from '../../components/ui/AssetIcon'
import { Badge } from '../../components/ui/Badge'
import { Button } from '../../components/ui/Button'
import { Card } from '../../components/ui/Card'
import { IconAlert, IconArrowRight, IconCheck } from '../../components/ui/icons'
import { ApiError, api } from '../../lib/api'
import { fmtPct, fmtPrice, fmtQty } from '../../lib/format'
import { usePortfolio } from '../../lib/portfolio'
import type { ProtectionProposal, ProtectionRequest } from '../../lib/types'

const distance = (level: number, mark: number) => `${fmtPct((level / mark - 1) * 100, { sign: true, decimals: 1 })} from mark`

export function ProtectionCard({ data }: { data: ProtectionProposal }) {
  const { portfolio, setPortfolio } = usePortfolio()
  const [pending, setPending] = useState(false)
  const [done, setDone] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const symbol = String(data.symbol ?? '').toUpperCase()
  const mark = data.markPrice
  const current = data.current ?? { stopLoss: null, takeProfit: null }
  const proposed = data.proposed ?? { stopLoss: null, takeProfit: null }
  const stop = proposed.stopLoss ?? current.stopLoss
  const target = proposed.takeProfit ?? current.takeProfit
  const rr = stop != null && target != null && mark !== stop ? Math.abs(target - mark) / Math.abs(mark - stop) : null
  // After a reload the card shows as set while the position still carries its levels; the server refuses a stale
  // Confirm (expect below), so an old card can't undo a stop moved since.
  const held = portfolio?.positions.find((p) => p.symbol === symbol)
  const applied = held != null && (held.stopLoss ?? null) === stop && (held.takeProfit ?? null) === target

  async function confirm() {
    const body: ProtectionRequest = { expect: { stopLoss: current.stopLoss, takeProfit: current.takeProfit } }
    if (proposed.stopLoss != null) body.stopLoss = proposed.stopLoss
    if (proposed.takeProfit != null) body.takeProfit = proposed.takeProfit
    setPending(true)
    setError(null)
    try {
      const res = await api.setProtection(symbol, body)
      setPortfolio(res.portfolio) // revision-aware: an older snapshot can't overwrite it
      setDone(true)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'The protection could not be set. Please try again.')
    } finally {
      setPending(false)
    }
  }

  const rows = [
    { label: 'Stop', from: current.stopLoss, to: proposed.stopLoss },
    { label: 'Target', from: current.takeProfit, to: proposed.takeProfit },
  ]

  return (
    <Card className="overflow-hidden">
      <div className="flex items-start justify-between gap-3 px-4 pt-4">
        <div className="flex min-w-0 items-center gap-3">
          <AssetIcon symbol={symbol} size={38} />
          <div className="min-w-0">
            <div className="label">Protection · paper</div>
            <div className="font-display text-[19px] leading-snug font-medium text-text">
              Protect {symbol} {data.side} · {fmtQty(data.qty)} {symbol}
            </div>
          </div>
        </div>
        <Badge tone="accent">Mark {fmtPrice(mark)}</Badge>
      </div>

      <dl className="mx-4 mt-4 divide-y divide-border rounded-xl border border-border">
        {rows.map((r) => {
          const level = r.to ?? r.from
          return (
            <div key={r.label} className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5 px-3.5 py-2.5 font-mono text-[13px] tabular-nums">
              <dt className="w-14 font-sans text-[12.5px] text-muted">{r.label}</dt>
              <dd className="text-text">
                {r.to == null ? (
                  <>
                    {r.from == null ? 'None' : fmtPrice(r.from)} <span className="text-muted">(unchanged)</span>
                  </>
                ) : (
                  <>
                    <span className="text-muted">{r.from == null ? 'none' : fmtPrice(r.from)}</span> → {fmtPrice(r.to)}
                  </>
                )}
              </dd>
              {level != null && <dd className="text-[11.5px] text-muted">({distance(level, mark)})</dd>}
            </div>
          )
        })}
        {rr != null && (
          <div className="flex items-baseline gap-2 px-3.5 py-2.5 font-mono text-[13px] tabular-nums">
            <dt className="w-14 font-sans text-[12.5px] text-muted">R:R</dt>
            <dd className="text-text">{rr.toFixed(1)} : 1</dd>
          </div>
        )}
      </dl>

      {data.rationale && <p className="px-4 pt-3.5 text-[14px] leading-relaxed text-pretty text-text">{data.rationale}</p>}

      {done || applied ? (
        <div role="status" className="m-4 flex flex-wrap items-center gap-x-3 gap-y-2 rounded-xl bg-up/8 px-4 py-3">
          <span className="grid size-7 shrink-0 place-items-center rounded-full bg-up/15 text-up">
            <IconCheck size={15} strokeWidth={2.4} />
          </span>
          <span className="flex-1 text-[14px] font-medium text-text">Protection set</span>
          <Link to={`/trade/${encodeURIComponent(symbol)}`} className="inline-flex items-center gap-1 text-[13px] font-medium text-accent-ink hover:underline">
            View on Trade <IconArrowRight size={14} />
          </Link>
        </div>
      ) : (
        <div className="flex flex-wrap items-center justify-end gap-x-3 gap-y-2 px-4 pt-4 pb-4">
          <p className="mr-auto text-[12px] text-muted">Applies to your open paper position. Nothing is traded.</p>
          <Button variant="primary" loading={pending} onClick={confirm} className="w-full sm:w-auto">
            Confirm protection
          </Button>
          {error && (
            <p role="alert" className="flex w-full items-start gap-2 rounded-lg bg-down/8 px-3 py-2 text-[13px] text-down">
              <IconAlert size={15} className="mt-px shrink-0" />
              {error}
            </p>
          )}
        </div>
      )}
    </Card>
  )
}

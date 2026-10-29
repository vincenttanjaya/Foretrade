// "Tell me when the market moves" (round 6): an odds trigger on the report's matched Polymarket market, set from the Desk
// vs market card (POST /api/alerts, trigger odds, notify only, 0 credits). The server re-prices the market about once a minute.
import { useState } from 'react'
import { Link, useNavigate } from 'react-router'
import { useToast } from '../../components/toast'
import { Button } from '../../components/ui/Button'
import { IconAlert, IconArrowRight, IconBell, IconCheck } from '../../components/ui/icons'
import { api } from '../../lib/api'
import { cx } from '../../lib/cx'
import { errorMessage } from '../../lib/errors'
import type { AlertOp, ForecastResult, MarketOdds } from '../../lib/types'
import { yesP } from './forecast'

/** "19%", "23.1%": a probability with one decimal only when needed (as the API's messages print it). */
export const pctLabel = (p: number) => `${Math.round(p * 1000) / 10}%`

/** The desk's P of the option a market prices (binary: Yes); null when the report has none for it. */
export const deskOf = (r: Pick<ForecastResult, 'probabilities'>, m: Pick<MarketOdds, 'outcome'>) =>
  m.outcome ? (r.probabilities.find((p) => p.outcome === m.outcome)?.p ?? null) : yesP(r.probabilities)

export type OddsPreset = { key: 'desk' | 'edge'; label: string; op: AlertOp; level: number }

const clamp = (p: number) => Math.min(0.99, Math.max(0.01, Math.round(p * 1000) / 1000))

/**
 * The one-tap levels: the market reaching the desk's number, and the edge widening by 10 points (the market moving 10
 * points further from the desk), clamped to 1–99%. A level the market is already at or past is left out.
 */
export function oddsPresets(yes: number, desk: number): OddsPreset[] {
  const deskAbove = desk > yes
  const atDesk = clamp(desk)
  const wider = clamp(deskAbove ? yes - 0.1 : yes + 0.1)
  const presets: OddsPreset[] = [
    { key: 'desk', label: `When the market reaches the desk's ${pctLabel(atDesk)}`, op: deskAbove ? 'above' : 'below', level: atDesk },
    { key: 'edge', label: 'If the edge widens by 10 pts', op: deskAbove ? 'below' : 'above', level: wider },
  ]
  return presets.filter((p) => (p.op === 'above' ? p.level > yes : p.level < yes))
}

const opWords = (op: AlertOp) => (op === 'above' ? 'rises above' : 'falls below')

/** "Alert me" under the stake row: presets set in one tap, or a custom level on the Automations page. */
export function OddsAlertButton({ forecastId, m, desk }: { forecastId: string; m: MarketOdds; desk: number }) {
  const navigate = useNavigate()
  const { toast } = useToast()
  const presets = oddsPresets(m.yesPrice, desk)
  const [open, setOpen] = useState(false)
  const [pick, setPick] = useState<OddsPreset['key'] | null>(presets[0]?.key ?? null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [set, setSet] = useState<OddsPreset | null>(null)
  const chosen = presets.find((p) => p.key === pick) ?? null

  async function submit() {
    if (!chosen || busy) return
    setBusy(true)
    setError(null)
    try {
      await api.createAlert({ symbol: '', trigger: 'odds', forecastId, op: chosen.op, price: chosen.level, action: 'notify', source: 'manual' })
      toast(`Alert set. You'll get a bell when Polymarket crosses ${pctLabel(chosen.level)}.`)
      setSet(chosen)
      setOpen(false)
    } catch (err) {
      setError(errorMessage(err, "The alert wasn't set. Please try again."))
    } finally {
      setBusy(false)
    }
  }

  const custom = () =>
    navigate('/automations', {
      state: { trigger: 'odds', forecastId, op: chosen?.op ?? (desk > m.yesPrice ? 'above' : 'below'), pricePct: Math.round((chosen?.level ?? desk) * 1000) / 10 },
    })

  return (
    <div className="mt-4 border-t border-border pt-4 print:hidden">
      {set && !open ? (
        <p role="status" className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[13px] text-text">
          <IconCheck size={14} strokeWidth={2.4} className="shrink-0 text-up" />
          Alert set: Polymarket {opWords(set.op)} {pctLabel(set.level)}.
          <Link to="/automations" className="inline-flex items-center gap-1 font-medium text-accent-ink hover:underline">
            Manage in Automations <IconArrowRight size={13} />
          </Link>
        </p>
      ) : (
        <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
          <Button variant="secondary" size="sm" aria-expanded={open} aria-controls="odds-alert" onClick={() => setOpen(!open)}>
            <IconBell size={14} /> Alert me
          </Button>
          <span className="text-[12.5px] text-muted">A bell when Polymarket moves. Notify only, 0 credits.</span>
        </div>
      )}
      {open && (
        <div id="odds-alert" role="group" aria-label="Market alert" className="mt-3 rounded-xl border border-border bg-surface-2/60 p-3">
          <div className="flex flex-col gap-2">
            {presets.map((p) => (
              <button
                key={p.key}
                type="button"
                aria-pressed={pick === p.key}
                disabled={busy}
                onClick={() => setPick(p.key)}
                className={cx(
                  'flex flex-wrap items-baseline justify-between gap-x-3 gap-y-0.5 rounded-lg border px-3 py-2 text-left text-[13.5px] transition-colors disabled:opacity-55',
                  pick === p.key ? 'border-accent bg-accent/8 text-text' : 'border-border bg-surface text-text hover:bg-surface-2',
                )}
              >
                <span className="font-medium">{p.label}</span>
                <span className="font-mono text-[12px] text-muted tabular-nums">
                  {opWords(p.op)} {pctLabel(p.level)}
                </span>
              </button>
            ))}
            <button type="button" onClick={custom} className="self-start text-[13px] font-medium text-accent-ink hover:underline">
              Custom level…
            </button>
          </div>
          {error && (
            <p role="alert" className="mt-3 flex items-start gap-2 rounded-lg bg-down/8 px-3 py-2 text-[13px] text-down">
              <IconAlert size={15} className="mt-px shrink-0" />
              {error}
            </p>
          )}
          <div className="mt-3 flex flex-wrap items-center justify-end gap-x-3 gap-y-2">
            <p className="mr-auto text-[12px] text-muted">Polymarket now {pctLabel(m.yesPrice)} · re-checked about once a minute.</p>
            <Button variant="primary" size="sm" loading={busy} disabled={!chosen} onClick={submit} className="w-full sm:w-auto">
              Set alert
            </Button>
          </div>
        </div>
      )}
    </div>
  )
}

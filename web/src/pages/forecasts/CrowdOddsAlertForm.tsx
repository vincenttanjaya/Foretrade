import { useRef, useState, type FormEvent } from 'react'
import { Link } from 'react-router'
import { Button } from '../../components/ui/Button'
import { Input, Label, Select, Textarea } from '../../components/ui/Input'
import { api } from '../../lib/api'
import { errorMessage } from '../../lib/errors'
import type { AlertOp, PredictionEvent, PredictionMarket } from '../../lib/types'
import { pctLabel } from './OddsAlertButton'

export function CrowdOddsAlertForm({ market: m, event, asOf, onClose }: {
  market: PredictionMarket
  event: Pick<PredictionEvent, 'slug' | 'title' | 'url'>
  asOf?: string
  onClose: () => void
}) {
  const [op, setOp] = useState<AlertOp>('above')
  const [level, setLevel] = useState('')
  const [note, setNote] = useState('')
  const [busy, setBusy] = useState(false)
  const pending = useRef(false)
  const [error, setError] = useState<string | null>(null)
  const [created, setCreated] = useState(false)
  const value = Number(level)
  const crossed = value > 0 && (op === 'above' ? value / 100 <= m.yesPrice : value / 100 >= m.yesPrice)
  const ready = level.trim() !== '' && Number.isFinite(value) && value >= 1 && value <= 99 && !crossed
  async function submit(e: FormEvent) {
    e.preventDefault()
    if (!ready || pending.current) return
    pending.current = true
    setBusy(true)
    setError(null)
    try {
      await api.createAlert({
        symbol: '', trigger: 'odds', marketSlug: m.marketSlug, marketId: m.id, op,
        price: Math.round(value * 100) / 10000, action: 'notify', note: note.trim() || undefined, source: 'manual',
      })
      setCreated(true)
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      pending.current = false
      setBusy(false)
    }
  }
  return (
    <section aria-label={`Crowd odds alert: ${m.question}`} className="mt-4 rounded-xl border border-border bg-surface-2 p-4 [overflow-wrap:anywhere]">
      <h3 className="font-display text-xl">Crowd odds alert</h3>
      <p className="mt-2 text-sm">{m.question}</p>
      <p className="mt-1 text-[13px] text-muted">
        {event.title} · own Yes {pctLabel(m.yesPrice)}. Free · notify only. The server checks fresh odds before setting it.
      </p>
      {asOf && <p className="mt-1 text-[12px] text-muted">Displayed quote read {asOf}.</p>}
      {created ? (
        <>
          <p role="status" className="mt-3 text-sm">Alert set. You will receive a bell when the crowd crosses your level.</p>
          <Link to="/automations" className="mt-2 inline-block text-sm font-medium text-accent-ink hover:underline">Manage in Automations</Link>
          <Button variant="ghost" className="ml-3" onClick={onClose}>Close</Button>
        </>
      ) : (
        <form onSubmit={submit} className="mt-3 grid gap-3 sm:grid-cols-2">
          <div>
            <Label htmlFor={`crowd-op-${m.id}`}>Condition</Label>
            <Select id={`crowd-op-${m.id}`} value={op} disabled={busy} onChange={(e) => setOp(e.target.value as AlertOp)}>
              <option value="above">Rises above</option>
              <option value="below">Falls below</option>
            </Select>
          </div>
          <div>
            <Label htmlFor={`crowd-level-${m.id}`}>Yes probability (%)</Label>
            <Input id={`crowd-level-${m.id}`} type="number" min="1" max="99" step="0.01" value={level} disabled={busy} onChange={(e) => setLevel(e.target.value)} />
          </div>
          <div className="sm:col-span-2">
            <Label htmlFor={`crowd-note-${m.id}`}>Note</Label>
            <Textarea id={`crowd-note-${m.id}`} maxLength={200} value={note} disabled={busy} onChange={(e) => setNote(e.target.value)} rows={2} />
          </div>
          {crossed && <p className="text-[13px] text-muted sm:col-span-2">This level is already crossed by the displayed quote. Pick a level {op} {pctLabel(m.yesPrice)}.</p>}
          {error && <p role="alert" className="text-sm text-down sm:col-span-2">{error}</p>}
          <div className="flex flex-wrap gap-2 sm:col-span-2">
            <Button type="submit" loading={busy} disabled={!ready}>Set alert</Button>
            <Button variant="ghost" disabled={busy} onClick={onClose}>Cancel</Button>
          </div>
        </form>
      )}
    </section>
  )
}

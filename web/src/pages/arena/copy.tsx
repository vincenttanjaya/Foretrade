import { useEffect, useRef, useState } from 'react'
import { Link } from 'react-router'
import { api, ApiError } from '../../lib/api'
import { errorMessage } from '../../lib/errors'
import { fmtQty, fmtUsd } from '../../lib/format'
import { usePortfolio } from '../../lib/portfolio'
import type { CopyPlan, LiveAgent } from '../../lib/types'
import { useToast } from '../../components/toast'
import { Button } from '../../components/ui/Button'
import { Badge } from '../../components/ui/Badge'
import { Card } from '../../components/ui/Card'
import { Input } from '../../components/ui/Input'
import { PriceChange } from '../../components/ui/PriceChange'
import { copyInput, useLiveRead, utc } from './live'

export const liveDialogClass = 'card m-auto w-[calc(100%-2rem)] max-w-lg max-h-[calc(100dvh-2rem)] overflow-y-auto p-6 text-text backdrop:bg-[rgb(15_27_45/0.45)] backdrop:backdrop-blur-[2px]'
export const attributionText = 'P&L attributed to copied opening lots · since the latest account reset'
export const overlapText = 'Manual trades can change what the plan owns. FIFO attributed P&L may differ from its reconciled holdings.'

export function CopyButton({ agent, active, enabled, onCreated, onConflict }: { agent: LiveAgent; active?: CopyPlan; enabled: boolean; onCreated: (plan: CopyPlan) => void; onConflict: () => void }) {
  const dialog = useRef<HTMLDialogElement>(null)
  const lock = useRef(false)
  const [busy, setBusy] = useState(false)
  const [opened, setOpened] = useState(false)
  const [percent, setPercent] = useState('10')
  const [cap, setCap] = useState('5000')
  const [error, setError] = useState('')
  const { toast } = useToast()
  const input = copyInput(percent, cap)
  async function submit(e: React.FormEvent) {
    e.preventDefault()
    if (!input || active || lock.current) return
    lock.current = true; setBusy(true); setError('')
    try {
      const { plan } = await api.copyLiveAgent(agent.id, input)
      onCreated(plan); dialog.current?.close(); toast(`Copying ${agent.name} · future paper fills only.`)
    } catch (err) {
      setError(errorMessage(err, 'Could not start copying. Please try again.'))
      if (err instanceof ApiError && (err.status === 409 || err.status === 404)) onConflict()
    } finally { lock.current = false; setBusy(false) }
  }
  return <>
    {active && !opened ? <Link to="/account#copy-plans" className="text-sm font-medium text-accent-ink underline">Manage in Account</Link> : <Button size="sm" variant="accent" disabled={!enabled || !!active} onClick={() => { setOpened(true); setError(''); dialog.current?.showModal() }}>Copy (paper)</Button>}
    <dialog ref={dialog} onClose={() => setOpened(false)} aria-labelledby={`copy-${agent.id}`} onCancel={(e) => busy && e.preventDefault()} className={liveDialogClass}>
      <form onSubmit={submit}>
        <div className="label">Copy plan · paper trading</div>
        <h2 id={`copy-${agent.id}`} className="mt-2 break-words font-display text-2xl font-medium">Copy {agent.name} (paper)</h2>
        <p className="mt-3 text-sm leading-relaxed text-muted">Copies future fills only. It does not buy the agent's current holdings. Buys are limited by your cap and buying power. {overlapText}</p>
        <p className="mt-2 text-sm text-muted">A running autopilot on any core symbol conflicts with copy plans. Non-core autopilots can remain active.</p>
        <label className="mt-5 block text-sm" htmlFor={`ratio-${agent.id}`}>Copy ratio (%)</label>
        <Input id={`ratio-${agent.id}`} inputMode="decimal" value={percent} disabled={busy} onChange={(e) => setPercent(e.target.value)} aria-describedby={`copy-limits-${agent.id}`} />
        <label className="mt-4 block text-sm" htmlFor={`cap-${agent.id}`}>Deployed-value cap (USD)</label>
        <Input id={`cap-${agent.id}`} inputMode="decimal" value={cap} disabled={busy} onChange={(e) => setCap(e.target.value)} aria-describedby={`copy-limits-${agent.id}`} />
        <p id={`copy-limits-${agent.id}`} className="mt-2 text-xs text-muted">Use 10–100% and $100–$50,000. This cap limits future buys; it is not an upfront allocation.</p>
        {agent.status === 'benched' && <p className="mt-2 text-sm text-muted">This agent is benched and holding until {utc(agent.benchedUntil)}.</p>}
        {error && <p role="alert" className="mt-3 break-words text-sm text-down">{error}</p>}
        <div className="mt-5 flex flex-wrap justify-end gap-2">
          <Button variant="secondary" disabled={busy} onClick={() => dialog.current?.close()}>Cancel</Button>
          <Button type="submit" loading={busy} disabled={!input || busy || !!active}>Confirm copy (paper)</Button>
        </div>
      </form>
    </dialog>
  </>
}

export function CopyPlanCard({ plan, onChanged, onConflict, onOpenChange }: { plan: CopyPlan; onChanged: (plan: CopyPlan) => void; onConflict: () => void; onOpenChange?: (open: boolean) => void }) {
  const dialog = useRef<HTMLDialogElement>(null)
  const lock = useRef(false)
  const [busy, setBusy] = useState<'keep' | 'close' | null>(null)
  const [error, setError] = useState('')
  const { setPortfolio, refresh } = usePortfolio()
  const { toast } = useToast()
  async function stop(closePositions: boolean) {
    if (lock.current) return
    lock.current = true; setBusy(closePositions ? 'close' : 'keep'); setError('')
    try {
      const r = await api.stopCopyPlan(plan.id, closePositions)
      onChanged(r.plan)
      if (r.portfolio) setPortfolio(r.portfolio); else void refresh()
      dialog.current?.close(); toast('Copy plan stopped.')
    } catch (err) {
      setError(errorMessage(err, 'Could not stop the copy plan. Please try again.'))
      if (err instanceof ApiError && (err.status === 409 || err.status === 404)) onConflict()
    } finally { lock.current = false; setBusy(null) }
  }
  return <Card role="region" aria-label={`Copy plan ${plan.agentName}`} className="min-w-0 p-5">
    <div className="flex flex-wrap items-start justify-between gap-3">
      <div className="min-w-0"><div className="label">Copy · paper</div><h3 className="mt-1 break-words font-display text-xl">{plan.agentName}</h3></div>
      <Badge tone={plan.status === 'active' ? 'accent' : 'neutral'}>{plan.status === 'active' ? 'Active' : plan.stopReason === 'account_disabled' ? 'Stopped: account disabled' : 'Stopped'}</Badge>
    </div>
    <p className="mt-3 font-mono text-xs">{Math.round(plan.ratio * 100)}% ratio · {fmtUsd(plan.maxUsd)} cap</p>
    <dl className="mt-3 grid grid-cols-2 gap-3 text-sm"><div><dt className="label">Deployed</dt><dd className="mt-1 font-mono">{plan.deployedUsd == null ? '—' : fmtUsd(plan.deployedUsd)}</dd></div><div><dt className="label">Attributed P&L</dt><dd className="mt-1"><PriceChange value={plan.pnl} kind="usd" /></dd></div></dl>
    <p className="mt-3 text-xs leading-relaxed text-muted">{attributionText}. {overlapText}</p>
    <p className="mt-2 text-xs text-muted">Started {utc(plan.createdAt)}{plan.stoppedAt && <> · Stopped {utc(plan.stoppedAt)}</>}</p>
    <p className="mt-3 break-words font-mono text-xs">{plan.positions.length ? `Reconciled holdings: ${plan.positions.map((p) => `${fmtQty(p.qty)} ${p.symbol}`).join(' · ')}` : 'No reconciled holdings.'}</p>
    {plan.status === 'stopped' && plan.positions.length > 0 && <p className="mt-2 text-sm text-muted">Holdings remain in your account. See the log for kept or refused closing legs.</p>}
    {plan.log.length > 0 && <details className="mt-3"><summary className="cursor-pointer text-sm text-muted">Recent plan log</summary><ol className="mt-2 space-y-2">{plan.log.slice(0, 50).map((l, i) => <li key={`${l.t}-${i}`} className="break-words text-xs"><time className="font-mono text-muted">{utc(l.t)}</time> · {l.kind} · {l.text}</li>)}</ol></details>}
    {plan.status === 'active' && <Button size="sm" variant="danger" className="mt-4" onClick={() => { setError(''); onOpenChange?.(true); dialog.current?.showModal() }}>Stop copying</Button>}
    <dialog ref={dialog} onClose={() => onOpenChange?.(false)} aria-labelledby={`stop-copy-${plan.id}`} onCancel={(e) => !!busy && e.preventDefault()} className={liveDialogClass}>
      <div className="label">Copy plan · paper</div><h2 id={`stop-copy-${plan.id}`} className="mt-2 break-words font-display text-2xl">Stop copying {plan.agentName}?</h2>
      <p className="mt-3 text-sm text-muted">Keep positions in your paper account, or close only available reconciled long ownership at market. Manual reductions may have changed it. Refused closing legs remain in your account and appear in the plan log.</p>
      {error && <p role="alert" className="mt-3 break-words text-sm text-down">{error}</p>}
      <div className="mt-5 flex flex-wrap justify-end gap-2"><Button variant="ghost" disabled={!!busy} onClick={() => dialog.current?.close()}>Cancel</Button><Button variant="secondary" disabled={!!busy} loading={busy === 'keep'} onClick={() => stop(false)}>Stop and keep positions</Button><Button variant="danger" disabled={!!busy} loading={busy === 'close'} onClick={() => stop(true)}>Stop and close owned positions</Button></div>
    </dialog>
  </Card>
}

/** Keep an open confirmation mounted when an owner refresh removes its row, so the mutation error stays reviewable. */
export function CopyPlanList({ plans, onChanged, onConflict }: { plans: CopyPlan[]; onChanged: (plan: CopyPlan) => void; onConflict: () => void }) {
  const [openPlans, setOpenPlans] = useState<CopyPlan[]>([])
  const [outcomes, setOutcomes] = useState<CopyPlan[]>([])
  const shown = [...plans, ...openPlans.filter((p) => !plans.some((current) => current.id === p.id))]
  function opened(plan: CopyPlan, open: boolean) {
    setOpenPlans((previous) => [...previous.filter((p) => p.id !== plan.id), ...(open ? [plan] : [])])
  }
  function changed(plan: CopyPlan) {
    if (plan.status === 'stopped') setOutcomes((previous) => [plan, ...previous.filter((p) => p.id !== plan.id)])
    onChanged(plan)
  }
  return <>
    <div className="mt-4 grid min-w-0 gap-4 md:grid-cols-2">{shown.map((p) => <CopyPlanCard key={p.id} plan={p} onChanged={changed} onConflict={onConflict} onOpenChange={(open) => opened(p, open)} />)}</div>
    {outcomes.length > 0 && <div className="mt-4 grid min-w-0 gap-4 md:grid-cols-2">{outcomes.map((p) => <Card key={p.id} role="region" aria-label={`Stop result ${p.agentName}`} className="min-w-0 p-5">
      <div className="label">Stop result · paper trading</div>
      <h3 className="mt-1 break-words font-display text-xl">Stopped copying {p.agentName}</h3>
      <p className="mt-2 text-xs text-muted">Stopped {utc(p.stoppedAt)}</p>
      <p className="mt-3 text-sm text-muted">This is the outcome at stop. Copying has ended; remaining holdings stay in your account and are no longer protected by this copy plan.</p>
      <p className="mt-3 break-words font-mono text-sm">{p.positions.length ? `Remaining holdings at stop: ${p.positions.map((h) => `${fmtQty(h.qty)} ${h.symbol}`).join(' · ')}` : 'No remaining holdings at stop.'}</p>
      {p.log.length > 0 && <ol aria-label="Stop result log" className="mt-3 space-y-2">{p.log.slice(0, 50).map((l, i) => <li key={`${l.t}-${i}`} className="break-words text-xs"><time className="font-mono text-muted">{utc(l.t)}</time> · {l.kind} · {l.text}</li>)}</ol>}
      <div className="mt-4 flex flex-wrap items-center gap-3"><Link to="/account#copy-plans" className="text-sm font-medium text-accent-ink underline">Review in Account</Link><Button size="sm" variant="secondary" onClick={() => setOutcomes((previous) => previous.filter((result) => result.id !== p.id))}>Dismiss</Button></div>
    </Card>)}</div>}
  </>
}

export function AccountCopyPlans({ revision, onStopped }: { revision: number | null; onStopped: () => void }) {
  const read = useLiveRead((signal) => api.copyPlans(signal).then((r) => r.plans))
  const previous = useRef(revision)
  useEffect(() => {
    if (revision != null && previous.current != null && revision > previous.current) read.refresh()
    previous.current = revision
  }, [revision, read.refresh])
  function changed(plan: CopyPlan) { read.commit((plans) => (plans ?? []).map((p) => p.id === plan.id ? plan : p)); onStopped() }
  return <section id="copy-plans" aria-labelledby="copy-plans-title" className="mt-6 scroll-mt-6">
    <h2 id="copy-plans-title" className="font-display text-2xl">Copy plans</h2><p className="mt-1 text-sm text-muted">Active and recent plans · up to 20 · paper trading</p>
    {read.error && <p role="alert" className="mt-3 text-sm text-down">{read.data ? "Couldn't refresh; showing last loaded data. " : ''}{read.error} <Button variant="secondary" size="sm" onClick={read.refresh}>Retry copy plans</Button></p>}
    {read.data == null ? !read.error && <p role="status" className="mt-3 text-sm text-muted">Loading copy plans…</p> : <><CopyPlanList plans={read.data} onChanged={changed} onConflict={read.refresh} />{!read.data.length && <p className="mt-3 text-sm text-muted">No copy plans yet. <Link to="/strategies?tab=arena" className="text-accent-ink underline">Watch the live desk</Link></p>}</>}
  </section>
}

import { useEffect, useRef, useState } from 'react'
import { api } from '../../lib/api'
import { errorMessage } from '../../lib/errors'
import { fmtPct, fmtPrice, fmtQty, fmtUsd } from '../../lib/format'
import type { ArenaStep, LiveAgent } from '../../lib/types'
import { Button } from '../../components/ui/Button'
import { AssetIcon } from '../../components/ui/AssetIcon'
import { Select } from '../../components/ui/Input'
import { PriceChange } from '../../components/ui/PriceChange'
import { liveDialogClass } from './copy'
import { mergeSteps, targetChange, utc } from './live'

export function LiveDecisionLog({ agent, agents, onSwitch, onClose }: { agent: LiveAgent; agents: LiveAgent[]; onSwitch: (id: string) => void; onClose: () => void }) {
  const dialog = useRef<HTMLDialogElement>(null)
  const [rows, setRows] = useState<ArenaStep[]>([])
  const [busy, setBusy] = useState(false)
  const [more, setMore] = useState(true)
  const [error, setError] = useState('')
  const [retryBefore, setRetryBefore] = useState<number | undefined>()
  const request = useRef(0)
  const ctrl = useRef<AbortController | null>(null)
  const owner = useRef(agent.id)
  const windowRows = useRef<ArenaStep[]>([])
  const read = useRef<(before?: number) => void>(() => {})
  read.current = async (before?: number) => {
    if (ctrl.current) return
    const token = ++request.current, id = agent.id, abort = new AbortController()
    ctrl.current = abort; setBusy(true); setError(''); setRetryBefore(before)
    try {
      const r = await api.liveArenaSteps(id, { limit: 20, ...(before == null ? {} : { before }) }, abort.signal)
      if (token !== request.current || abort.signal.aborted || owner.current !== id) return
      // A disjoint newest page starts a contiguous window; its cursor must recover any omitted interval.
      const restart = before == null && !r.steps.some((s) => windowRows.current.some((old) => old.step === s.step))
      const next = mergeSteps(restart ? [] : windowRows.current, r.steps)
      windowRows.current = next
      setRows(next)
      if (before != null || restart) setMore(r.steps.length === 20)
    } catch (err) {
      if (token === request.current && !abort.signal.aborted && owner.current === id) setError(errorMessage(err, 'Could not load decisions.'))
    } finally { if (token === request.current) { ctrl.current = null; setBusy(false) } }
  }
  useEffect(() => { dialog.current?.showModal(); return () => { request.current++; ctrl.current?.abort(); ctrl.current = null } }, [])
  useEffect(() => {
    request.current++; ctrl.current?.abort(); ctrl.current = null; owner.current = agent.id; windowRows.current = []
    setRows([]); setMore(true); setError(''); setBusy(false)
    read.current()
    return () => { request.current++; ctrl.current?.abort(); ctrl.current = null }
  }, [agent.id])
  const stamp = useRef({ id: agent.id, steps: agent.steps, at: agent.lastStepAt })
  useEffect(() => {
    const next = { id: agent.id, steps: agent.steps, at: agent.lastStepAt }
    if (stamp.current.id === next.id && (stamp.current.steps !== next.steps || stamp.current.at !== next.at) && owner.current === agent.id) {
      request.current++; ctrl.current?.abort(); ctrl.current = null; read.current()
    }
    stamp.current = next
  }, [agent.id, agent.steps, agent.lastStepAt])
  return <dialog ref={dialog} aria-labelledby="live-decisions-title" onClose={onClose} className={`${liveDialogClass} max-w-3xl!`}>
    <div className="flex flex-wrap items-start justify-between gap-3"><div className="min-w-0"><div className="label">Live paper decisions · actual UTC</div><h2 id="live-decisions-title" className="mt-2 break-words font-display text-2xl">{agent.name} decisions</h2></div><Button size="sm" variant="secondary" onClick={() => dialog.current?.close()}>Close</Button></div>
    {agents.length > 1 && <Select aria-label="Decision agent" value={agent.id} className="mt-4" onChange={(e) => onSwitch(e.target.value)}>{agents.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}</Select>}
    <p className="mt-3 break-words text-sm text-muted">{agent.mandate}</p><p className="mt-2 font-mono text-sm">Cash {fmtUsd(agent.cash)} · Equity {fmtUsd(agent.equityNow)}</p>
    {agent.lastError && <p className="mt-2 break-words text-sm text-down">{agent.lastError}</p>}
    <section aria-label="Agent positions" className="mt-4"><h3 className="label">Current positions</h3>{agent.positions.length ? <ul className="mt-2 space-y-2">{agent.positions.map((p) => <li key={p.symbol} className="flex flex-wrap items-center gap-2 text-xs"><AssetIcon symbol={p.symbol} size={18} /><span className="font-mono">{fmtQty(p.qty)} {p.symbol} @ {fmtPrice(p.mark)} · {fmtUsd(p.marketValue)} · {fmtPct(p.weightPct)}</span><PriceChange value={p.unrealizedPnl} kind="usd" /></li>)}</ul> : <p className="mt-2 text-sm text-muted">No open positions.</p>}</section>
    <p className="mt-4 break-words text-sm"><span className="label">Latest reasoning </span>{agent.lastReasoning || 'No reasoning yet.'}</p>
    {error && <p role="alert" className="mt-4 break-words text-sm text-down">{rows.length > 0 ? 'Showing loaded decisions. ' : ''}{error} <Button size="sm" variant="secondary" onClick={() => read.current(retryBefore)}>Retry decisions</Button></p>}
    {busy && <p role="status" className="mt-4 text-sm text-muted">Loading decisions…</p>}
    {!rows.length && !busy && !error && <p className="mt-4 text-sm text-muted">No decisions yet.</p>}
    <ol aria-label="Decision history" className="mt-5 space-y-3">{rows.map((s, i) => <li key={s.step} className="rounded-lg border border-border p-4"><div className="flex flex-wrap items-center gap-2 font-mono text-xs"><strong>Decision {s.step + 1}</strong><time>{utc(new Date(s.t * 1000).toISOString())}</time><span>Equity {fmtUsd(s.equity)}</span>{rows[i+1] && <PriceChange value={s.equity - rows[i+1].equity} kind="usd" />}</div>{s.error && <p className="mt-2 break-words text-xs text-down">{s.error}</p>}<p className="mt-2 break-words whitespace-pre-wrap text-sm">{s.reasoning || 'No rationale recorded.'}</p><div className="mt-2 flex flex-wrap gap-2 text-xs text-muted">{s.targets ? s.targets.map((t) => <span key={t.symbol}>{t.symbol} {targetChange(t.weightPct, rows[i+1] ? (rows[i+1].targets?.find((p) => p.symbol === t.symbol)?.weightPct ?? rows[i+1].positions.find((p) => p.symbol === t.symbol)?.weightPct ?? 0) : undefined)}</span>) : 'Held · no target orders'}</div>{s.fills.map((f,j) => <p key={j} className="mt-2 break-words font-mono text-xs">{f.side === 'buy' ? 'Buy' : 'Sell'} {fmtQty(f.qty)} {f.symbol} @ {fmtPrice(f.price)} · fee {fmtUsd(f.fee)}</p>)}{s.tools.length > 0 && <details className="mt-2"><summary className="cursor-pointer text-xs text-muted">Tools ({s.tools.length})</summary><ul className="mt-2 space-y-2 text-xs">{s.tools.map((t,j) => <li key={j} className="break-words">{t.name} · {t.ok ? 'OK' : 'Failed'} · {t.summary}</li>)}</ul></details>}</li>)}</ol>
    {rows.length >= 100 ? <p className="mt-4 text-xs text-muted">Showing the newest 100 loaded decisions. Older history is available on the server.</p> : more && rows.length > 0 ? <Button className="mt-4" variant="secondary" disabled={busy} onClick={() => read.current(rows.at(-1)!.step)}>Load older</Button> : rows.length > 0 && <p className="mt-4 text-xs text-muted">End of available decision history.</p>}
  </dialog>
}

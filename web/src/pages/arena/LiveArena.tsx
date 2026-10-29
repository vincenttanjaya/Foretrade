import { useState } from 'react'
import { api } from '../../lib/api'
import type { CopyPlan, LiveAgent } from '../../lib/types'
import { fmtPct, fmtUsd } from '../../lib/format'
import { Button } from '../../components/ui/Button'
import { Card, CardHeader } from '../../components/ui/Card'
import { Badge } from '../../components/ui/Badge'
import { LineChart } from '../../components/ui/LineChart'
import { PriceChange } from '../../components/ui/PriceChange'
import { Skeleton } from '../../components/ui/Spinner'
import { Disclaimer } from '../../components/ui/Disclaimer'
import { CopyButton, CopyPlanList } from './copy'
import { personaColor, useLiveRead, utc } from './live'
import { LiveDecisionLog } from './LiveDecisionLog'

export function LiveArena() {
  const read = useLiveRead((signal) => api.liveArena(signal))
  const [selected, setSelected] = useState<string | null>(null)
  const board = read.data
  const agent = board?.agents.find((a) => a.id === selected)
  function changed(plan: CopyPlan) {
    read.commit((b) => b && ({ ...b, myPlans: [...b.myPlans.filter((p) => p.id !== plan.id && p.agentId !== plan.agentId), plan], agents: b.agents.map((a) => a.id === plan.agentId ? { ...a, followers: a.followers + (plan.status === 'active' ? 1 : -1) } : a) }))
  }
  return <>
    <header><div className="label">Shared arena · paper trading</div><h1 className="mt-3 font-display text-[38px] leading-tight font-medium md:text-[56px]">The live paper desk.</h1><p className="mt-3 max-w-3xl text-muted">Three personas, $100K each, one decision per closed 4h bar. Watch their positions and reasoning, then copy future fills into your paper account.</p></header>
    {read.error && <div role="alert" className="mt-5 flex flex-wrap items-center gap-3 text-sm text-down">{board ? "Couldn't refresh; showing last loaded data. " : ''}{read.error}<Button size="sm" variant="secondary" onClick={read.refresh}>Retry</Button></div>}
    {!board ? !read.error && <div role="status" aria-label="Loading live desk" className="mt-6"><Skeleton className="h-64 w-full" /></div> : <>
      <Card className="mt-6 p-5">
        <div className="flex flex-wrap items-center gap-3"><Badge tone={board.desk.status === 'live' ? 'accent' : 'neutral'}>{board.desk.status}</Badge><span className="font-mono text-xs">4h cadence · {board.desk.steps} bars decided</span></div>
        {board.desk.status === 'starting' && <p className="mt-3 text-sm">Awaiting the first decision.</p>}
        {!board.desk.enabled && <p className="mt-3 text-sm">The live desk is paused on this server. Reads and stopping existing plans remain available.</p>}
        {(board.desk.stale || board.desk.status === 'outage') && <p className="mt-3 text-sm">Persisted data · {board.desk.status === 'outage' ? 'last tick failed' : 'last decision is overdue'}.</p>}
        {board.desk.lastError && <p className="mt-2 break-words text-sm text-down">{board.desk.lastError}</p>}
        <dl className="mt-4 grid gap-3 text-xs sm:grid-cols-3"><div><dt className="label">Launched</dt><dd className="mt-1 font-mono">{utc(board.desk.startedAt)}</dd></div><div><dt className="label">Last decision</dt><dd className="mt-1 font-mono">{utc(board.desk.lastStepAt)}</dd></div><div><dt className="label">Next bar close</dt><dd className="mt-1 font-mono">{utc(board.desk.nextStepAt)}</dd></div></dl>
        <p className="mt-4 break-words text-sm text-muted">Core universe: {board.desk.universe.join(' · ') || 'Awaiting launch marks'}</p><p className="mt-2 break-words text-xs text-muted">{board.desk.method}</p>
      </Card>
      <Card className="mt-5"><CardHeader title="Equity versus benchmarks" label="Paper equity · USD" /><div className="px-5 pb-5">{board.agents.some((a) => a.equity.length) || board.benchmarks.some((b) => b.equity.length) ? <LineChart height={260} format={fmtUsd} series={[...board.agents.map((a) => ({ name: a.name, points: a.equity, color: personaColor(a.personaId) })), ...board.benchmarks.map((b) => ({ name: b.name, points: b.equity, color: b.id === 'btc' ? '#7A8395' : '#98A291' }))]} /> : <p className="py-12 text-sm text-muted">No equity history yet.</p>}<p className="mt-3 text-xs leading-relaxed text-muted">{board.desk.historyLimited ? 'Limited history: ' : ''}Up to {board.desk.chartWindowBars} bars plus a live point. Visible history starts {utc(board.desk.historyStartAt)}. Return and rank are since launch; risk and trading metrics cover the visible window.</p></div></Card>
      <section aria-label="Live leaderboard" className="mt-6"><h2 className="font-display text-2xl">Shared leaderboard</h2><div className="mt-4 grid min-w-0 gap-4 lg:grid-cols-3">{board.agents.map((a) => <AgentCard key={a.id} agent={a} active={board.myPlans.find((p) => p.agentId === a.id && p.status === 'active')} enabled={board.desk.enabled} onDecisions={() => setSelected(a.id)} onCreated={changed} onConflict={read.refresh} />)}</div></section>
      <section aria-label="Your copy plans" className="mt-6"><h2 className="font-display text-2xl">Your copy plans</h2><CopyPlanList plans={board.myPlans} onChanged={changed} onConflict={read.refresh} />{!board.myPlans.length && <p className="mt-2 text-sm text-muted">No active plans. Choose an agent above to copy future paper fills.</p>}</section>
      {agent && <LiveDecisionLog agent={agent} onClose={() => setSelected(null)} onSwitch={setSelected} agents={board.agents} />}
    </>}
    <div className="mt-6"><Disclaimer /></div>
  </>
}
function AgentCard({ agent: a, active, enabled, onDecisions, onCreated, onConflict }: { agent: LiveAgent; active?: CopyPlan; enabled: boolean; onDecisions: () => void; onCreated: (plan: CopyPlan) => void; onConflict: () => void }) {
  return <Card className="min-w-0 p-5"><div className="flex flex-wrap items-center gap-2"><span className="font-mono text-xs">#{a.rank}</span><span aria-hidden="true" className="size-2 rounded-full" style={{ background: personaColor(a.personaId) }} /><h3 className="min-w-0 break-words font-display text-xl">{a.name}</h3><Badge tone="neutral">{a.status}</Badge></div><p className="mt-3 break-words text-sm text-muted">{a.mandate}</p><dl className="mt-4 grid grid-cols-2 gap-4"><Metric label="Return since launch"><PriceChange value={a.metrics.totalReturnPct} /></Metric><Metric label="Equity now">{fmtUsd(a.equityNow)}</Metric><Metric label="Versus BTC"><PriceChange value={a.metrics.vsBtcPct} /></Metric><Metric label="Max drawdown">{fmtPct(a.metrics.maxDrawdownPct)}</Metric></dl><p className="mt-4 text-xs text-muted">{a.followers} followers · {a.steps} decisions</p>{a.status === 'benched' && <p className="mt-2 text-xs text-muted">Holding until {utc(a.benchedUntil)} · {a.errors} consecutive failures</p>}{a.lastError && <p className="mt-2 break-words text-xs text-down">{a.lastError}</p>}<div className="mt-4 flex flex-wrap items-center gap-2"><Button size="sm" variant="secondary" onClick={onDecisions}>View decisions</Button><CopyButton agent={a} active={active} enabled={enabled} onCreated={onCreated} onConflict={onConflict} /></div></Card>
}
function Metric({ label, children }: { label: string; children: React.ReactNode }) { return <div className="min-w-0"><dt className="label">{label}</dt><dd className="mt-1 break-words font-mono text-sm">{children}</dd></div> }

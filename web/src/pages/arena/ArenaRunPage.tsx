import { useEffect, useRef, useState } from 'react'
import { Link, useLocation, useNavigate, useParams } from 'react-router'
import { useToast } from '../../components/toast'
import { Button, buttonClass } from '../../components/ui/Button'
import { Card } from '../../components/ui/Card'
import { Disclaimer } from '../../components/ui/Disclaimer'
import { EmptyState } from '../../components/ui/EmptyState'
import { IconAlert, IconStrategy } from '../../components/ui/icons'
import { LineChart } from '../../components/ui/LineChart'
import { Skeleton } from '../../components/ui/Spinner'
import { api, ApiError } from '../../lib/api'
import { useAuth } from '../../lib/auth'
import { errorMessage } from '../../lib/errors'
import { fmtDate, fmtPct } from '../../lib/format'
import type { ArenaRun, ArenaStep } from '../../lib/types'
import { usePoll } from '../../lib/usePoll'
import { BackLink } from '../strategies/parts'
import { agentColor, BASELINE_COLOR, etaMin, lastStepAt, simAt, windowText } from './arena'
import { ContaminatedBadge, DecisionLog, Leaderboard, StatusBadge } from './parts'

/** /strategies/arena/:id — progress, equity race, leaderboard and each agent's decision log; polls while running. */
export function ArenaRunPage() {
  const { id = '' } = useParams()
  return <Detail key={id} id={id} />
}

function Detail({ id }: { id: string }) {
  const { toast } = useToast()
  const { refresh } = useAuth()
  // Coming straight from Start run, the new run rides along in router state.
  const seeded = (useLocation().state as { run?: ArenaRun } | null)?.run
  const [run, setRun] = useState<ArenaRun | null>(seeded?.id === id ? seeded : null)
  const [error, setError] = useState<string | null>(null)
  const [notFound, setNotFound] = useState(false)
  const loading = useRef(false)
  const lastStatus = useRef(run?.status ?? null)

  const apply = (r: ArenaRun) => {
    lastStatus.current = r.status
    setRun(r)
  }

  // First load (and Retry, by clearing the error), then every 3 s while the run is in flight.
  usePoll(
    async () => {
      if (loading.current) return
      loading.current = true
      try {
        const { run: r } = await api.arenaRun(id)
        if (lastStatus.current === 'running' && r.status !== 'running') {
          if (r.status === 'completed') toast('The arena run finished.', 'success')
          else if (r.status === 'failed') toast('The arena run failed.', 'error')
          if (r.status !== 'completed') refresh().catch(() => {}) // unrun steps were refunded
        }
        apply(r)
        setError(null)
      } catch (err) {
        if (err instanceof ApiError && (err.status === 404 || err.status === 400)) setNotFound(true)
        else setError(errorMessage(err))
      } finally {
        loading.current = false
      }
    },
    3000,
    !notFound && ((run === null && !error) || run?.status === 'running'),
  )

  return (
    <div className="mx-auto max-w-6xl px-4 py-8 md:px-8 md:py-10">
      <BackLink to="/strategies?tab=arena&arena=historical">Arena</BackLink>
      {notFound ? (
        <EmptyState
          icon={<IconStrategy size={20} />}
          title="This run doesn't exist."
          description="It may have been deleted, or the link is wrong."
          action={
            <Link to="/strategies?tab=arena&arena=historical" className={buttonClass('secondary')}>
              Back to the arena
            </Link>
          }
        />
      ) : run ? (
        <Loaded run={run} onRun={apply} reconnecting={!!error} />
      ) : error ? (
        <EmptyState
          icon={<IconAlert size={20} />}
          title="Couldn't load this run."
          description={error}
          action={<Button variant="secondary" onClick={() => setError(null)}>Try again</Button>}
        />
      ) : (
        <div aria-busy="true" aria-label="Loading run">
          <Skeleton className="h-11 w-3/4 max-w-xl" />
          <Skeleton className="mt-6 h-24 w-full rounded-[14px]" />
          <Skeleton className="mt-6 h-[380px] w-full rounded-[14px]" />
        </div>
      )}
    </div>
  )
}

function Loaded({ run, onRun, reconnecting }: { run: ArenaRun; onRun: (r: ArenaRun) => void; reconnecting: boolean }) {
  const [agent, setAgent] = useState(0)
  const { steps, error, retry } = useSteps(run, agent)
  const cash = run.startingCash || 100000
  const series = [
    ...run.agents.map((a) => ({ name: a.name, color: agentColor(a.index), points: a.equity ?? [] })),
    ...run.benchmarks.map((b) => ({ name: b.name, color: BASELINE_COLOR[b.id] ?? BASELINE_COLOR.btc, points: b.equity ?? [] })),
  ]

  return (
    <>
      <header className="flex flex-wrap items-start justify-between gap-x-8 gap-y-5">
        <div className="max-w-3xl min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <StatusBadge status={run.status} />
            {run.contaminated && <ContaminatedBadge />}
            <span className="font-mono text-[11.5px] text-muted">
              {[windowText(run), `${run.steps} steps`, `${run.agents.length} ${run.agents.length === 1 ? 'agent' : 'agents'}`, `${run.credits} cr`]
                .filter(Boolean)
                .join(' · ')}
            </span>
          </div>
          <h1 className="mt-3 font-display text-[32px] leading-[1.08] font-medium tracking-[-0.015em] text-balance text-text md:text-[44px]">
            {run.name}
          </h1>
          <p className="mt-2 font-mono text-[11.5px] text-muted">
            <span title={run.universe.join(', ')}>{run.universe.length} assets</span> · started{' '}
            {fmtDate(run.createdAt, { time: true })}
          </p>
        </div>
        <Actions run={run} onRun={onRun} />
      </header>

      <Progress run={run} reconnecting={reconnecting} />

      <Card className="mt-6 p-5">
        <div className="mb-4">
          <div className="label">Equity race · return on ${(cash / 1000).toFixed(0)}k</div>
          <h2 className="mt-1 font-display text-[19px] leading-snug font-medium text-text">Agents vs buy & hold</h2>
        </div>
        {series.some((s) => s.points.length > 1) ? (
          <LineChart height={320} series={series} format={(v) => fmtPct((v / cash - 1) * 100, { sign: true, decimals: 1 })} />
        ) : (
          <p className="grid h-[320px] place-items-center rounded-lg bg-surface-2/60 text-sm text-muted">
            {run.status === 'running' ? 'The curves start after the first step.' : 'No steps were completed.'}
          </p>
        )}
      </Card>

      <Leaderboard agents={run.agents} benchmarks={run.benchmarks} daily={run.cadence === '1d'} />

      <DecisionLog run={run} agent={agent} onAgent={setAgent} steps={steps} error={error} onRetry={retry} />
    </>
  )
}

/**
 * The selected agent's steps, cached per agent. New steps are fetched with after = the last one held and appended,
 * whenever the run reports more finished steps than are loaded (each poll while running, and once at the end).
 */
function useSteps(run: ArenaRun, agent: number) {
  const [byAgent, setByAgent] = useState<Record<number, ArenaStep[]>>({})
  // Tagged with its agent: a late failure for one agent must not cover another agent's cached log.
  const [error, setError] = useState<{ agent: number; msg: string } | null>(null)
  const [nonce, setNonce] = useState(0)
  const inFlight = useRef(new Set<number>())
  const have = byAgent[agent]?.at(-1)?.step ?? -1
  const behind = have < run.stepsDone - 1 || !byAgent[agent]

  useEffect(() => {
    if (!behind || inFlight.current.has(agent)) return
    inFlight.current.add(agent)
    setError(null)
    // ponytail: one page per call; a run has at most 90 steps and a page holds 100.
    api
      .arenaSteps(run.id, agent, have)
      .then(({ steps }) =>
        setByAgent((m) => {
          const last = m[agent]?.at(-1)?.step ?? -1
          return { ...m, [agent]: [...(m[agent] ?? []), ...(steps ?? []).filter((s) => s.step > last)] }
        }),
      )
      .catch((err) => setError({ agent, msg: errorMessage(err, 'Could not load the decisions.') }))
      .finally(() => inFlight.current.delete(agent))
  }, [run.id, agent, have, behind, run.stepsDone, nonce])

  return { steps: byAgent[agent], error: error?.agent === agent ? error.msg : null, retry: () => setNonce((n) => n + 1) }
}

function Progress({ run, reconnecting }: { run: ArenaRun; reconnecting: boolean }) {
  const pct = run.steps ? Math.min(100, (run.stepsDone / run.steps) * 100) : 0
  const at = lastStepAt(run)
  return (
    <Card className="mt-6 p-5">
      <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
        <p className="font-mono text-[13px] text-text tabular-nums">
          Step {run.stepsDone} / {run.steps}
          {at != null && <span className="text-muted"> · sim {simAt(at, run.cadence)}</span>}
        </p>
        <p className="font-mono text-[11.5px] text-muted">
          {run.status === 'running'
            ? reconnecting
              ? 'Connection hiccup · retrying…'
              : `≈ ${etaMin(run.steps - run.stepsDone)} min left`
            : run.completedAt
              ? `Ended ${fmtDate(run.completedAt, { time: true })}`
              : ''}
        </p>
      </div>
      <div
        role="progressbar"
        aria-label="Steps finished"
        aria-valuemin={0}
        aria-valuemax={run.steps}
        aria-valuenow={run.stepsDone}
        className="mt-3 h-1.5 overflow-hidden rounded-full bg-surface-2"
      >
        <div className="h-full rounded-full bg-accent transition-[width] duration-500" style={{ width: `${pct}%` }} />
      </div>
      {run.status === 'failed' && (
        <p role="alert" className="mt-4 flex items-start gap-2 text-[13.5px] text-down">
          <IconAlert size={16} className="mt-px shrink-0" />
          {run.error || 'The run could not be completed.'} Unrun steps were refunded.
        </p>
      )}
      {run.status === 'cancelled' && <p className="mt-4 text-[13.5px] text-muted">Cancelled after {run.stepsDone} steps. Unrun steps were refunded.</p>}
      {run.contaminated && (
        <p className="mt-4 text-[13px] leading-relaxed text-muted">
          This window overlaps the model's training data (cutoff {fmtDate(run.knowledgeCutoff)}). The model may remember what
          happened; treat results as optimistic.
        </p>
      )}
      <Disclaimer className="mt-4" />
    </Card>
  )
}

function Actions({ run, onRun }: { run: ArenaRun; onRun: (r: ArenaRun) => void }) {
  const navigate = useNavigate()
  const { toast } = useToast()
  const { setCredits } = useAuth()
  const dialog = useRef<HTMLDialogElement>(null)
  const [busy, setBusy] = useState(false)

  async function cancel() {
    setBusy(true)
    try {
      const res = await api.cancelArenaRun(run.id)
      if (res.credits) setCredits(res.credits)
      onRun(res.run)
      toast('Run cancelled. Unrun steps were refunded.', 'info')
    } catch (err) {
      toast(errorMessage(err, 'Could not cancel the run.'), 'error')
    } finally {
      setBusy(false)
    }
  }

  async function remove() {
    setBusy(true)
    try {
      await api.deleteArenaRun(run.id)
      toast(`Deleted "${run.name}".`)
      navigate('/strategies?tab=arena&arena=historical', { replace: true })
    } catch (err) {
      toast(errorMessage(err, 'Could not delete the run.'), 'error')
      setBusy(false)
    }
  }

  if (run.status === 'running')
    return (
      <Button variant="danger" onClick={cancel} loading={busy}>
        Cancel run
      </Button>
    )

  return (
    <>
      <Button variant="danger" onClick={() => dialog.current?.showModal()}>
        Delete
      </Button>
      <dialog
        ref={dialog}
        aria-labelledby="delete-run-title"
        onCancel={(e) => busy && e.preventDefault()}
        className="card m-auto w-[calc(100%-2rem)] max-w-md animate-fade-up p-6 text-text backdrop:bg-[rgb(15_27_45/0.45)] backdrop:backdrop-blur-[2px]"
      >
        <div className="label mb-2">AI backtest arena</div>
        <h2 id="delete-run-title" className="font-display text-2xl leading-tight font-medium">
          Delete “{run.name}”?
        </h2>
        <p className="mt-2.5 text-sm leading-relaxed text-muted">The leaderboard and every decision are removed. This can't be undone.</p>
        <div className="mt-6 flex justify-end gap-2">
          <Button variant="ghost" onClick={() => dialog.current?.close()} disabled={busy}>
            Keep it
          </Button>
          <Button variant="danger" onClick={remove} loading={busy}>
            Delete run
          </Button>
        </div>
      </dialog>
    </>
  )
}

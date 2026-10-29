import { useRef, useState } from 'react'
import { Link, useNavigate } from 'react-router'
import { useToast } from '../../components/toast'
import { Button } from '../../components/ui/Button'
import { Card } from '../../components/ui/Card'
import { EmptyState } from '../../components/ui/EmptyState'
import { IconAlert, IconArrowRight, IconStrategy } from '../../components/ui/icons'
import { Input, Label, Textarea } from '../../components/ui/Input'
import { PriceChange } from '../../components/ui/PriceChange'
import { Skeleton } from '../../components/ui/Spinner'
import { Tabs } from '../../components/ui/Tabs'
import { api, ApiError } from '../../lib/api'
import { useAuth } from '../../lib/auth'
import { cx } from '../../lib/cx'
import { errorMessage } from '../../lib/errors'
import { fmtDate, timeAgo } from '../../lib/format'
import type { ArenaAgentRequest, ArenaCadence, ArenaOptions, ArenaPersonaId, ArenaRunSummary } from '../../lib/types'
import { usePoll } from '../../lib/usePoll'
import { LoadError, useCreditsLeft, useLoad } from '../strategies/parts'
import {
  addDays,
  CADENCE_LABEL,
  cost,
  etaMin,
  isContaminated,
  MAX_INSTRUCTION,
  setupProblem,
  stepsFor,
  windowText,
} from './arena'
import { ContaminatedBadge, StatusBadge } from './parts'

/** Strategies → Arena tab: set up a time-travel backtest of AI agents, and the list of past runs. */
export function ArenaSetup() {
  const opts = useLoad((signal) => api.arenaOptions(signal))

  return (
    <>
      <header className="max-w-3xl">
        <div className="label">AI backtest arena</div>
        <h1 className="mt-3 font-display text-[38px] leading-[1.04] font-medium tracking-[-0.02em] text-balance text-text md:text-[56px]">
          Let the agents trade the past.
        </h1>
        <p className="mt-4 max-w-2xl text-[16px] leading-relaxed text-pretty text-muted">
          Pick a past window and up to four AI agents. Each one replays it step by step, seeing only the bars and headlines
          published before the simulated time, and trades a $100,000 paper book against BTC and an equal-weight basket.
        </p>
      </header>

      {opts.data ? (
        <SetupCard opts={opts.data} />
      ) : opts.error ? (
        <div className="mt-8">
          <LoadError error={opts.error} what="arena" back={{ to: '/strategies', label: 'Back to the studio' }} onRetry={opts.retry} />
        </div>
      ) : (
        <Skeleton className="mt-8 h-[520px] w-full rounded-[14px]" />
      )}

      <PastRuns />
    </>
  )
}

function SetupCard({ opts }: { opts: ArenaOptions }) {
  const navigate = useNavigate()
  const { toast } = useToast()
  const { setCredits, refresh } = useAuth()
  const left = useCreditsLeft()
  const builtins = opts.personas.filter((p) => p.id !== 'custom')
  const [cadence, setCadence] = useState<ArenaCadence>(opts.cadences[0]?.cadence ?? '1d')
  const cad = opts.cadences.find((c) => c.cadence === cadence)
  const [dates, setDates] = useState<{ start: string; end: string } | null>(null) // null = the cadence's defaults
  const start = dates?.start ?? cad?.defaultStart ?? ''
  const end = dates?.end ?? cad?.defaultEnd ?? ''
  const [picked, setPicked] = useState<ArenaPersonaId[]>(builtins.map((p) => p.id).slice(0, opts.maxAgents))
  const [custom, setCustom] = useState(false)
  const [name, setName] = useState('')
  const [instruction, setInstruction] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const agents = picked.length + (custom ? 1 : 0)
  const steps = stepsFor(cadence, start, end)
  const credits = cost(agents, steps)
  const problem = setupProblem({ cadence, start, end, agents, instruction: custom ? instruction : null }, opts)
  const short = left != null && left < credits
  const full = agents >= opts.maxAgents

  function toggle(id: ArenaPersonaId) {
    setPicked((p) => (p.includes(id) ? p.filter((x) => x !== id) : [...p, id]))
  }

  async function startRun() {
    if (problem || short || busy) return
    setBusy(true)
    setError(null)
    const req: ArenaAgentRequest[] = builtins.filter((p) => picked.includes(p.id)).map((p) => ({ personaId: p.id }))
    if (custom) req.push({ personaId: 'custom', name: name.trim() || undefined, instruction: instruction.trim() })
    try {
      const res = await api.createArenaRun({ cadence, start, end, agents: req })
      if (res.credits) setCredits(res.credits)
      navigate(`/strategies/arena/${res.run.id}`, { state: { run: res.run } })
    } catch (err) {
      if (err instanceof ApiError && err.status === 409) {
        // One running run per user: point at it.
        const running = (await api.arenaRuns().catch(() => null))?.runs?.find((r) => r.status === 'running')
        toast(
          <>
            You already have a run in progress; it has to finish or be cancelled first.{' '}
            {running && (
              <Link to={`/strategies/arena/${running.id}`} className="font-medium text-accent-ink underline">
                Open “{running.name}”
              </Link>
            )}
          </>,
          'error',
        )
      } else {
        setError(errorMessage(err, 'The run could not be started.'))
      }
      refresh().catch(() => {}) // a failed start spends nothing; re-read the pill anyway
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card className="mt-8 p-5 md:p-6">
      <section aria-labelledby="arena-window">
        <h2 id="arena-window" className="label">
          1 · The window
        </h2>
        <Tabs
          className="mt-3"
          value={cadence}
          onChange={(c) => {
            setCadence(c)
            setDates(null)
          }}
          items={opts.cadences.map((c) => ({
            value: c.cadence,
            label: `${CADENCE_LABEL[c.cadence]} · ${c.minDays}–${c.maxDays} days`,
          }))}
          aria-label="Cadence"
        />
        <div className="mt-4 grid max-w-md grid-cols-2 gap-3">
          <div className="min-w-0">
            <Label htmlFor="arena-start">Start</Label>
            <Input
              id="arena-start"
              type="date"
              value={start}
              max={cad ? addDays(opts.latestEnd, 1 - cad.minDays) : opts.latestEnd}
              onChange={(e) => setDates({ start: e.target.value, end })}
              className="font-mono tabular-nums"
            />
          </div>
          <div className="min-w-0">
            <Label htmlFor="arena-end">End</Label>
            <Input
              id="arena-end"
              type="date"
              value={end}
              min={cad ? addDays(start, cad.minDays - 1) : start}
              max={cad && addDays(start, cad.maxDays - 1) < opts.latestEnd ? addDays(start, cad.maxDays - 1) : opts.latestEnd}
              onChange={(e) => setDates({ start, end: e.target.value })}
              className="font-mono tabular-nums"
            />
          </div>
        </div>
        <p className="mt-2 text-[12.5px] text-muted">
          UTC days, both included. Agents decide at each {cadence === '1d' ? 'day' : '4-hour bar'}'s open from closed bars only;
          orders fill at that open with fees and slippage.
        </p>
        {isContaminated(start, opts.knowledgeCutoff) && (
          <p role="note" className="mt-4 flex items-start gap-2.5 rounded-xl border border-accent/40 bg-accent-soft px-4 py-3 text-[13.5px] leading-relaxed text-text">
            <IconAlert size={16} className="mt-0.5 shrink-0 text-accent-ink" />
            <span>
              This window overlaps the model's training data (cutoff {fmtDate(opts.knowledgeCutoff)}). The model may remember
              what happened; treat results as optimistic.
            </span>
          </p>
        )}
      </section>

      <section aria-labelledby="arena-agents" className="mt-7">
        <div className="flex flex-wrap items-baseline justify-between gap-2">
          <h2 id="arena-agents" className="label">
            2 · The agents
          </h2>
          <span className="font-mono text-[11px] text-muted">
            {agents}/{opts.maxAgents} · same model, different instructions
          </span>
        </div>
        <ul className="mt-3 grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          {builtins.map((p) => (
            <PersonaCard
              key={p.id}
              name={p.name}
              description={p.description}
              checked={picked.includes(p.id)}
              disabled={busy || (full && !picked.includes(p.id))}
              onChange={() => toggle(p.id)}
            />
          ))}
          <PersonaCard
            name="Your agent"
            description="Write its mandate yourself: what to hold, when to trade, how much risk."
            checked={custom}
            disabled={busy || (full && !custom)}
            onChange={() => setCustom(!custom)}
          />
        </ul>
        {custom && (
          <div className="mt-4 grid gap-3 rounded-xl border border-border bg-surface-2/50 p-4">
            <div className="max-w-xs">
              <Label htmlFor="arena-name">Agent name</Label>
              <Input id="arena-name" value={name} maxLength={40} placeholder="Your agent" disabled={busy} onChange={(e) => setName(e.target.value)} />
            </div>
            <div>
              <div className="flex items-baseline justify-between gap-2">
                <Label htmlFor="arena-instruction">Instruction</Label>
                <span className="font-mono text-[11px] text-muted tabular-nums">
                  {instruction.length}/{MAX_INSTRUCTION}
                </span>
              </div>
              <Textarea
                id="arena-instruction"
                value={instruction}
                maxLength={MAX_INSTRUCTION}
                rows={3}
                disabled={busy}
                placeholder="e.g. Hold BTC and ETH only. Go to cash when BTC closes below its 50-day average; never put more than 60% in one asset."
                onChange={(e) => setInstruction(e.target.value)}
                className="max-h-56 bg-surface"
              />
            </div>
          </div>
        )}
      </section>

      <div className="mt-7 flex flex-wrap items-center gap-x-4 gap-y-3 border-t border-border pt-5">
        <div aria-live="polite" className="min-w-0 flex-1 text-[13px]">
          <p className="font-mono text-[12.5px] text-text tabular-nums">
            {steps} steps · ≈ {etaMin(steps)} min · {credits} credits
          </p>
          {error ? (
            <p role="alert" className="mt-1 text-down">
              {error}
            </p>
          ) : problem ? (
            <p className="mt-1 text-down">{problem}</p>
          ) : short ? (
            <p className="mt-1 text-down">
              Needs {credits} credits; {left} left today. Credits reset at 00:00 UTC.
            </p>
          ) : (
            <p className="mt-1 text-muted">Charged at the start; steps that never run are refunded.</p>
          )}
        </div>
        <Button variant="accent" size="lg" onClick={startRun} loading={busy} disabled={!!problem || short} className="w-full sm:w-auto">
          {busy ? 'Starting…' : 'Start run'}
          <span className="font-mono text-[11px] font-normal opacity-70">· {credits} cr</span>
        </Button>
      </div>
    </Card>
  )
}

function PersonaCard({
  name,
  description,
  checked,
  disabled,
  onChange,
}: {
  name: string
  description: string
  checked: boolean
  disabled: boolean
  onChange: () => void
}) {
  return (
    <li>
      <label
        className={cx(
          'flex h-full gap-3 rounded-xl border p-4 transition-colors',
          checked ? 'border-accent bg-accent-soft/50' : 'border-border bg-surface hover:border-accent/50',
          disabled && !checked ? 'cursor-not-allowed opacity-55' : 'cursor-pointer',
        )}
      >
        <input type="checkbox" checked={checked} disabled={disabled} onChange={onChange} className="mt-1 size-4 shrink-0 accent-[var(--accent)]" />
        <span className="min-w-0">
          <span className="block font-display text-[17px] leading-snug font-medium text-text">{name}</span>
          <span className="mt-1 block text-[13px] leading-relaxed text-muted">{description}</span>
        </span>
      </label>
    </li>
  )
}

// ── Past runs ───────────────────────────────────────────────────────────────

function PastRuns() {
  const [runs, setRuns] = useState<ArenaRunSummary[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const loading = useRef(false)
  const running = runs?.some((r) => r.status === 'running') ?? false

  // First load (and Retry, by clearing the error), then every 5 s while a run is in flight.
  usePoll(
    async () => {
      if (loading.current) return
      loading.current = true
      try {
        const res = await api.arenaRuns()
        setRuns(res.runs ?? [])
        setError(null)
      } catch (err) {
        setError(errorMessage(err))
      } finally {
        loading.current = false
      }
    },
    5000,
    (runs === null && !error) || running,
  )

  return (
    <section aria-labelledby="arena-runs" className="mt-14">
      <div className="mb-4 flex items-baseline justify-between gap-4">
        <h2 id="arena-runs" className="font-display text-[26px] leading-tight font-medium text-text">
          Past runs
        </h2>
        {runs && runs.length > 0 && <span className="font-mono text-[11px] text-muted">{runs.length} runs</span>}
      </div>
      {!runs && error ? (
        <EmptyState
          icon={<IconAlert size={20} />}
          title="Couldn't load your runs."
          description={error}
          action={<Button variant="secondary" onClick={() => setError(null)}>Try again</Button>}
        />
      ) : runs?.length === 0 ? (
        <EmptyState
          icon={<IconStrategy size={20} />}
          title="No runs yet."
          description="Start one above. A 30-day daily run with one agent costs 15 credits and takes about 2 minutes."
        />
      ) : (
        <Card className="overflow-hidden">
          <ul aria-busy={!runs} className="divide-y divide-border">
            {runs
              ? runs.map((r) => <RunRow key={r.id} r={r} />)
              : [0, 1, 2].map((i) => (
                  <li key={i} className="px-5 py-4">
                    <Skeleton className="h-5 w-2/3" />
                    <Skeleton className="mt-2 h-4 w-1/3" />
                  </li>
                ))}
          </ul>
        </Card>
      )}
    </section>
  )
}

function RunRow({ r }: { r: ArenaRunSummary }) {
  const pct = r.steps ? (r.stepsDone / r.steps) * 100 : 0
  return (
    <li>
      <Link
        to={`/strategies/arena/${r.id}`}
        className="group grid gap-x-6 gap-y-2 px-5 py-4 transition-colors hover:bg-surface-2/60 sm:grid-cols-[minmax(0,1fr)_auto]"
      >
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <StatusBadge status={r.status} pct={r.status === 'running' ? pct : undefined} />
            {r.contaminated && <ContaminatedBadge />}
            <span className="font-mono text-[11px] text-muted">{timeAgo(r.createdAt)}</span>
          </div>
          <h3 className="mt-1.5 truncate font-medium text-text group-hover:text-accent-ink">{r.name}</h3>
          <p className="mt-0.5 font-mono text-[11.5px] text-muted">
            {[windowText(r), `${r.steps} steps`].filter(Boolean).join(' · ')}
          </p>
        </div>
        <dl className="flex items-center gap-5 text-[12.5px] sm:justify-end">
          <div className="min-w-0">
            <dt className="label">Leader</dt>
            <dd className="mt-0.5 flex items-baseline gap-1.5">
              {r.leader ? (
                <>
                  <span className="truncate text-text">{r.leader.name}</span>
                  <PriceChange value={r.leader.totalReturnPct} decimals={1} />
                </>
              ) : (
                <span className="text-muted">—</span>
              )}
            </dd>
          </div>
          <div>
            <dt className="label">BTC</dt>
            <dd className="mt-0.5">
              <PriceChange value={r.btcReturnPct} decimals={1} />
            </dd>
          </div>
          <IconArrowRight size={15} className="hidden shrink-0 text-muted group-hover:text-accent-ink sm:block" />
        </dl>
      </Link>
    </li>
  )
}

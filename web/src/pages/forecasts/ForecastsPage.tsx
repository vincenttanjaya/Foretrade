import { useEffect, useRef, useState, type ReactNode } from 'react'
import { Link, useSearchParams } from 'react-router'
import { Badge } from '../../components/ui/Badge'
import { Button, buttonClass } from '../../components/ui/Button'
import { Card } from '../../components/ui/Card'
import { EmptyState } from '../../components/ui/EmptyState'
import { Input, Label, Select } from '../../components/ui/Input'
import { IconAlert, IconArrowRight, IconForecast } from '../../components/ui/icons'
import { Skeleton } from '../../components/ui/Spinner'
import { api } from '../../lib/api'
import { cx } from '../../lib/cx'
import { fmtDate, fmtProb, timeAgo } from '../../lib/format'
import type { ForecastShape, ForecastSummary } from '../../lib/types'
import { usePoll } from '../../lib/usePoll'
import { errorMessage } from '../../lib/errors'
import { CATEGORIES, categoryLabel, EXAMPLE_IDEAS, fmtDay, fmtQ, SHAPES, topOutcome, viewReading } from './forecast'
import { PulseDot, ShapeBadge, StatusPill } from './parts'
import { TrackRecordCard, type TrackRecords } from './TrackRecord'
import { PredictionMarkets } from './PredictionMarkets'
import { Tabs } from '../../components/ui/Tabs'

type StatusFilter = 'all' | 'running' | 'completed' | 'failed' | 'archived'
type Scope = 'mine' | 'replay'
type ResolutionFilter = 'all' | 'unresolved' | 'due' | 'resolved'
type SortOrder = 'newest' | 'oldest' | 'resolution'

export function ForecastsPage() {
  const [params, setParams] = useSearchParams()
  const tab = params.get('tab') === 'markets' ? 'markets' : 'forecasts'
  return (
    <>
      <div className="mx-auto max-w-6xl px-4 pt-6 md:px-8">
        <Tabs
          aria-label="Forecast desk views"
          value={tab}
          onChange={(next) => {
            const query = new URLSearchParams(params)
            if (next === 'markets') query.set('tab', next)
            else query.delete('tab')
            setParams(query)
          }}
          items={[{ value: 'forecasts', label: 'Your forecasts' }, { value: 'markets', label: 'Markets' }]}
        />
      </div>
      {tab === 'markets' ? <PredictionMarkets /> : <ForecastDashboard />}
    </>
  )
}

function ForecastDashboard() {
  const [list, setList] = useState<ForecastSummary[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [status, setStatus] = useState<StatusFilter>('all')
  const [shape, setShape] = useState<ForecastShape | null>(null)
  const [query, setQuery] = useState('')
  const [category, setCategory] = useState('all')
  const [resolution, setResolution] = useState<ResolutionFilter>('all')
  const [sort, setSort] = useState<SortOrder>('newest')
  const [scope, setScope] = useState<Scope>('mine')
  const [replay, setReplay] = useState<ForecastSummary[] | null>(null)
  const [archived, setArchived] = useState<ForecastSummary[] | null>(null)
  const [archiveError, setArchiveError] = useState<string | null>(null)
  const [archiveAttempt, setArchiveAttempt] = useState(0)
  const [records, setRecords] = useState<TrackRecords | null>(null)
  const [recordsError, setRecordsError] = useState<string | null>(null)
  const loading = useRef(false)
  const listTop = useRef<HTMLDivElement>(null)

  useEffect(() => {
    api.trackRecord().then(setRecords, (err) => setRecordsError(errorMessage(err)))
  }, [])

  // The replay questions load the first time someone asks for them.
  useEffect(() => {
    if (scope !== 'replay' || replay) return
    api.forecasts({ scope: 'replay' }).then(
      (res) => setReplay(Array.isArray(res.forecasts) ? res.forecasts : []),
      (err) => {
        setScope('mine')
        setError(errorMessage(err))
      },
    )
  }, [scope, replay])

  useEffect(() => {
    if (status !== 'archived' || scope !== 'mine') return
    let cancelled = false
    setArchived(null)
    setArchiveError(null)
    api.forecasts({ status: 'archived' }).then(
      (res) => { if (!cancelled) setArchived(Array.isArray(res.forecasts) ? res.forecasts : []) },
      (err) => { if (!cancelled) setArchiveError(errorMessage(err)) },
    )
    return () => { cancelled = true }
  }, [status, scope, archiveAttempt])

  const running = list?.filter((f) => f.status === 'running').length ?? 0
  const completed = list?.filter((f) => f.status === 'completed').length ?? 0

  // One poller does the first load (and Retry, by clearing the error) and then refreshes every 5 s
  // while anything is in flight.
  usePoll(
    async () => {
      if (loading.current) return
      loading.current = true
      try {
        const res = await api.forecasts()
        setList(Array.isArray(res.forecasts) ? res.forecasts : [])
        setError(null)
      } catch (err) {
        setError(errorMessage(err))
      } finally {
        loading.current = false
      }
    },
    5000,
    (list === null && !error) || running > 0,
  )

  const archiveView = scope === 'mine' && status === 'archived'
  const rows = scope === 'replay' ? replay : archiveView ? archived : list
  const search = query.trim().toLocaleLowerCase()
  const today = new Date().toISOString().slice(0, 10)
  const shown = (rows ?? []).filter((f) => {
    const resolved = f.resolvedOutcome != null
    const searchable = [f.question, f.headline?.outcome, topOutcome(f), f.resolvedOutcome].filter(Boolean).join(' ').toLocaleLowerCase()
    return (status === 'all' || f.status === status) && (!shape || f.shape === shape) &&
      (category === 'all' || f.category === category) && (!search || searchable.includes(search)) &&
      (resolution === 'all' || (resolution === 'resolved' ? resolved : !resolved &&
        (resolution === 'unresolved' || f.resolutionDate <= today)))
  }).sort((a, b) => {
    const newest = Date.parse(b.createdAt) - Date.parse(a.createdAt)
    if (sort === 'resolution') return a.resolutionDate.localeCompare(b.resolutionDate) || newest
    return sort === 'oldest' ? -newest : newest
  })
  const hasFilters = !!search || status !== 'all' || shape !== null || category !== 'all' || resolution !== 'all' || sort !== 'newest'
  const clearFilters = () => {
    setQuery('')
    setStatus('all')
    setShape(null)
    setCategory('all')
    setResolution('all')
    setSort('newest')
  }
  const count = (s: StatusFilter) => s === 'archived' ? archived?.length :
    (scope === 'replay' ? replay ?? [] : list ?? []).filter((f) => s === 'all' || f.status === s).length
  const seeQuestions = () => {
    setScope('replay')
    if (status === 'archived') setStatus('all')
    listTop.current?.scrollIntoView?.({ behavior: 'smooth', block: 'start' })
  }

  return (
    <div className="mx-auto max-w-6xl px-4 py-8 md:px-8 md:py-10">
      <section className="grid gap-4 lg:grid-cols-[minmax(0,1.75fr)_minmax(0,1fr)]">
        <Hero total={list?.length} running={running} completed={completed} />
        <div className="grid grid-cols-3 gap-3 lg:grid-cols-1">
          <StatCard label="In flight" value={list && running} sub="live" live={running > 0} />
          <StatCard label="Completed" value={list && completed} sub="with a result" />
          <StatCard label="Total" value={list?.length} sub="on the desk" />
        </div>
      </section>

      <TrackRecordCard records={records} error={recordsError} onSeeQuestions={seeQuestions} />

      <div ref={listTop} className="scroll-mt-4" />
      {list === null && error ? (
        <EmptyState
          className="mt-10"
          icon={<IconAlert size={20} />}
          title="Couldn't load your forecasts."
          description={error}
          action={<Button variant="secondary" onClick={() => setError(null)}>Try again</Button>}
        />
      ) : (
        <>
          <div className="mt-10 grid gap-3 sm:grid-cols-2 lg:grid-cols-[minmax(0,1fr)_9rem_10rem_11rem]">
            <div className="min-w-0">
              <Label htmlFor="forecast-search">Search forecasts</Label>
              <div className="flex gap-2">
                <Input id="forecast-search" type="search" placeholder="Question or outcome…" value={query} onChange={(e) => setQuery(e.target.value)} className="min-w-0" />
                {query && <Button variant="secondary" aria-label="Clear search" onClick={() => setQuery('')}>Clear</Button>}
              </div>
            </div>
            <div className="min-w-0">
              <Label htmlFor="forecast-category">Category</Label>
              <Select id="forecast-category" value={category} onChange={(e) => setCategory(e.target.value)}>
                <option value="all">All categories</option>
                {CATEGORIES.map((c) => <option key={c.value} value={c.value}>{c.label}</option>)}
              </Select>
            </div>
            <div className="min-w-0">
              <Label htmlFor="forecast-resolution">Resolution</Label>
              <Select id="forecast-resolution" value={resolution} onChange={(e) => setResolution(e.target.value as ResolutionFilter)}>
                <option value="all">All</option>
                <option value="unresolved">Unresolved</option>
                <option value="due">Due</option>
                <option value="resolved">Resolved</option>
              </Select>
            </div>
            <div className="min-w-0">
              <Label htmlFor="forecast-sort">Sort forecasts</Label>
              <Select id="forecast-sort" value={sort} onChange={(e) => setSort(e.target.value as SortOrder)}>
                <option value="newest">Newest first</option>
                <option value="oldest">Oldest first</option>
                <option value="resolution">Resolution date</option>
              </Select>
            </div>
          </div>
          <div className="mt-4 mb-4 flex flex-wrap items-center gap-x-3 gap-y-2">
            <div role="group" aria-label="Filter by status" className="flex flex-wrap gap-1.5">
              {(
                [
                  ['all', 'All'],
                  ['running', 'In flight'],
                  ['completed', 'Completed'],
                  ['failed', 'Failed'],
                  ...(scope === 'mine' ? [['archived', 'Archived'] as const] : []),
                ] as const
              ).map(([v, label]) => (
                <Chip key={v} active={status === v} onClick={() => setStatus(v)} count={rows ? count(v) : undefined}>
                  {label}
                </Chip>
              ))}
            </div>
            <span aria-hidden="true" className="hidden h-5 w-px bg-border sm:block" />
            <div role="group" aria-label="Filter by shape" className="flex flex-wrap gap-1.5">
              {SHAPES.map((s) => (
                <Chip key={s.value} active={shape === s.value} onClick={() => setShape(shape === s.value ? null : s.value)}>
                  {s.label}
                </Chip>
              ))}
            </div>
            <span aria-hidden="true" className="hidden h-5 w-px bg-border sm:block" />
            <div role="group" aria-label="Filter by source" className="flex flex-wrap gap-1.5">
              <Chip active={scope === 'replay'} onClick={() => { setScope(scope === 'replay' ? 'mine' : 'replay'); if (status === 'archived') setStatus('all') }} count={records?.replay.n}>
                Track record
              </Chip>
            </div>
            {error && list && (
              <span role="status" className="ml-auto font-mono text-[11px] text-muted">
                Couldn't refresh · retrying
              </span>
            )}
          </div>

          {rows && (
            <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
              <p role="status" className="text-[12.5px] text-muted">
                Showing {shown.length} of {rows.length} forecast{rows.length === 1 ? '' : 's'}
                {scope === 'replay' && ' · Track record'}
              </p>
              {hasFilters && shown.length > 0 && <Button variant="ghost" size="sm" onClick={clearFilters}>Clear filters</Button>}
            </div>
          )}
          {resolution === 'due' && <p className="mb-3 text-[12.5px] text-muted">Due means unresolved questions with a resolution date of today or earlier (UTC).</p>}
          {archiveView && <p className="mb-3 text-[12.5px] text-muted">Open a forecast to read its saved report or restore it. Restoring uses no credits.</p>}

          {list?.length === 0 && scope === 'mine' && !archiveView && !hasFilters ? <FirstRun /> : (
          <Card className="overflow-hidden">
            {archiveView && archiveError ? (
              <EmptyState title="Couldn't load archived forecasts." description={archiveError}
                action={<Button variant="secondary" onClick={() => setArchiveAttempt((n) => n + 1)}>Retry archive</Button>} />
            ) : rows === null ? (
              <ul aria-busy="true" aria-label="Loading forecasts" className="divide-y divide-border">
                {[0, 1, 2, 3].map((i) => (
                  <RowSkeleton key={i} />
                ))}
              </ul>
            ) : rows.length === 0 && archiveView ? (
              <div className="px-6 py-14 text-center">
                <p className="font-display text-xl text-text">No archived forecasts.</p>
                <p className="mt-2 text-sm text-muted">Forecasts you archive will stay here until you restore them.</p>
                <Button variant="secondary" size="sm" className="mt-4" onClick={clearFilters}>Show your forecasts</Button>
              </div>
            ) : rows.length === 0 && scope === 'replay' ? (
              <div className="px-6 py-14 text-center">
                <p className="font-display text-xl text-text">No replay questions yet.</p>
                <p className="mt-2 text-sm text-muted">Historical questions will appear here when the track record is available.</p>
                <Button variant="secondary" size="sm" className="mt-4" onClick={() => setScope('mine')}>Show your forecasts</Button>
              </div>
            ) : shown.length === 0 ? (
              <div className="px-6 py-14 text-center">
                <p className="font-display text-xl text-text">No forecasts match these filters.</p>
                <p className="mt-2 text-sm text-muted">Try another question or outcome, or clear the filters to see all {scope === 'replay' ? 'replay questions' : 'your forecasts'}.</p>
                <Button
                  variant="secondary"
                  size="sm"
                  className="mt-4"
                  onClick={clearFilters}
                >
                  Clear filters
                </Button>
              </div>
            ) : (
              <ul className="divide-y divide-border">
                {shown.map((f) => (
                  <li key={f.id}>
                    <Row f={f} />
                  </li>
                ))}
              </ul>
            )}
          </Card>
          )}
        </>
      )}
    </div>
  )
}

function Hero({ total, running, completed }: { total?: number; running: number; completed: number }) {
  return (
    <div className="relative overflow-hidden rounded-[14px] bg-ink p-6 text-ink-fg md:p-8">
      {/* Decorative target rings, echoing the Forecasts icon. */}
      <svg
        aria-hidden="true"
        viewBox="0 0 200 200"
        className="pointer-events-none absolute -right-16 -bottom-20 size-72 text-accent opacity-25 md:-right-10 md:size-80"
      >
        {[96, 72, 48, 24].map((r) => (
          <circle key={r} cx="100" cy="100" r={r} fill="none" stroke="currentColor" strokeWidth="1" />
        ))}
        <circle cx="100" cy="100" r="3" fill="currentColor" />
      </svg>
      <div className="relative">
        <div className="label text-ink-fg/60">
          Foretrade · {total == null ? 'Forecast desk' : `${total} forecast${total === 1 ? '' : 's'}`}
        </div>
        <h1 className="mt-3 font-display text-[40px] leading-[1.02] font-medium tracking-[-0.02em] md:text-[54px]">
          Your forecasts.
        </h1>
        <p className="mt-3 max-w-md text-[15px] leading-relaxed text-ink-fg/70">
          Probability estimates from three specialist agents and an aggregator, with an evidence trail, scored against
          resolved outcomes in the track record below.
        </p>
        <div className="mt-7 flex flex-wrap items-center gap-x-5 gap-y-3">
          <Link to="/forecasts/new" className={buttonClass('accent', 'lg')}>
            <span aria-hidden="true" className="-mr-0.5 text-lg leading-none">+</span> New forecast
          </Link>
          <span className="font-mono text-[12px] tracking-wide text-ink-fg/60 tabular-nums">
            {total == null ? ' ' : `${running} in flight · ${completed} completed`}
          </span>
        </div>
      </div>
    </div>
  )
}

function StatCard({ label, value, sub, live }: { label: string; value: number | null | undefined; sub: string; live?: boolean }) {
  return (
    <Card className="flex min-w-0 flex-col justify-between px-3 py-3.5 sm:p-4 lg:flex-row lg:items-center lg:px-5">
      <div className="min-w-0">
        <div className="label flex items-center gap-1 truncate sm:gap-1.5">
          {live && <PulseDot />}
          {label}
        </div>
        <div className="mt-0.5 hidden text-[12.5px] text-muted lg:block">{sub}</div>
      </div>
      <div className="mt-2 font-display text-[32px] leading-none font-medium text-text tabular-nums lg:mt-0 lg:text-[36px]">
        {value == null ? <Skeleton className="h-8 w-10" /> : value}
      </div>
    </Card>
  )
}

function Chip({ active, onClick, count, children }: { active: boolean; onClick: () => void; count?: number; children: ReactNode }) {
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onClick}
      className={cx(
        'inline-flex h-8 items-center gap-1.5 rounded-full border px-3 text-[13px] font-medium transition-colors',
        active ? 'border-ink bg-ink text-ink-fg' : 'border-border bg-surface text-muted hover:border-muted/45 hover:text-text',
      )}
    >
      {children}
      {count != null && <span className={cx('font-mono text-[11px] tabular-nums', active ? 'text-ink-fg/65' : 'text-muted/80')}>{count}</span>}
    </button>
  )
}

function Row({ f }: { f: ForecastSummary }) {
  return (
    <Link
      to={`/forecasts/${f.id}`}
      className="group flex flex-col gap-4 px-5 py-4 transition-colors hover:bg-surface-2/60 focus-visible:bg-surface-2/60 focus-visible:outline-offset-[-2px] sm:flex-row sm:items-center sm:gap-6"
    >
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-2">
          <span className="label">{categoryLabel(f.category)}</span>
          <ShapeBadge shape={f.shape} />
          {f.replay && <Badge tone="accent">Replay · as of {fmtDay(f.createdAt)}</Badge>}
          {f.parentId && (
            <Badge tone="outline" title="A newer run of an earlier forecast; its probability history is on the report">
              Updated
            </Badge>
          )}
          {f.resolvedOutcome != null && <Happened f={f} />}
        </div>
        <p className="mt-1.5 font-display text-[17px] leading-snug text-pretty text-text group-hover:text-accent-ink">
          {f.question}
        </p>
        <p className="mt-1.5 font-mono text-[11px] text-muted">
          {f.replay ? `Resolved ${fmtDate(f.resolutionDate)}` : `Created ${timeAgo(f.createdAt)} · Resolves ${fmtDate(f.resolutionDate)}`}
        </p>
      </div>
      <div className="flex items-center justify-between gap-5 sm:w-[19rem] sm:shrink-0">
        <Headline f={f} />
        <div className="flex items-center gap-2">
          <StatusPill status={f.status} />
          <IconArrowRight size={15} className="hidden text-muted transition-transform group-hover:translate-x-0.5 group-hover:text-text sm:block" />
        </div>
      </div>
    </Link>
  )
}

/** What happened, ticked when it was the desk's top outcome. */
function Happened({ f }: { f: ForecastSummary }) {
  const right = topOutcome(f) === f.resolvedOutcome
  return (
    <Badge tone={right ? 'up' : 'down'} title={right ? "The desk's top outcome happened" : "The desk's top outcome did not happen"}>
      <span aria-hidden="true">{right ? '✓' : '✗'}</span> Happened: {f.resolvedOutcome}
    </Badge>
  )
}

/** Top outcome + a mini bar; same footprint in every state so rows never jump when a run completes. */
function Headline({ f }: { f: ForecastSummary }) {
  const done = f.status === 'completed' || f.status === 'archived'
  // A row whose view leads gives its median when the view states one; a flat one without, its 80% range (never the top
  // range of a distribution where none stands out). The chat's cards read a report by the same rule (viewReading).
  const reading = done ? viewReading(f) : null
  if (reading) return <MedianHeadline reading={reading} />
  const h = done ? f.headline : null
  const p = h && Number.isFinite(h.p) ? Math.max(0, Math.min(1, h.p)) : null
  return (
    <div className="w-40 min-w-0">
      <div className="flex items-baseline justify-between gap-2 text-[13px]">
        {h ? (
          <>
            <span className="truncate text-text" title={h.outcome}>
              {h.outcome}
            </span>
            <span className="font-mono font-semibold text-accent-ink tabular-nums">{fmtProb(p)}</span>
          </>
        ) : (
          <span className="truncate text-muted">
            {f.status === 'running' ? 'Specialists at work…' : f.status === 'failed' ? 'No result' : 'Result unavailable'}
          </span>
        )}
      </div>
      <div className="relative mt-1.5 h-1.5 overflow-hidden rounded-full bg-surface-2">
        {p != null && <span className="block h-full origin-left animate-bar rounded-full bg-accent" style={{ width: `${p * 100}%` }} />}
        {f.status === 'running' && (
          <span className="absolute inset-0 animate-shimmer bg-linear-to-r from-transparent via-accent/70 to-transparent" />
        )}
      </div>
    </div>
  )
}

/**
 * Round 9b: a numeric row whose view leads reads its median and, with a measured reference, the chance of ending at or
 * above today (the bar), never a range that does not stand out. A view that states no median reads its 80% range.
 */
function MedianHeadline({ reading }: { reading: NonNullable<ReturnType<typeof viewReading>> }) {
  const { view, up } = reading
  if (!view.anchored)
    return (
      <div className="w-40 min-w-0">
        <div className="truncate font-mono text-[13px] font-semibold text-accent-ink tabular-nums" title={reading.value}>
          {reading.value}
        </div>
        <div aria-hidden="true" className="mt-1.5 h-1.5" />
        <p className="mt-1 truncate text-[11px] text-muted">{reading.note}</p>
      </div>
    )
  return (
    <div className="w-40 min-w-0">
      <div className="flex items-baseline justify-between gap-2 text-[13px]">
        <span className="text-text">Median</span>
        <span className="font-mono font-semibold text-accent-ink tabular-nums">{fmtQ(view.median, view.unit)}</span>
      </div>
      <div className="relative mt-1.5 h-1.5 overflow-hidden rounded-full bg-surface-2">
        {up != null && <span className="block h-full origin-left animate-bar rounded-full bg-accent" style={{ width: `${up * 100}%` }} />}
      </div>
      <p className="mt-1 truncate font-mono text-[11px] text-muted tabular-nums">{reading.note}</p>
    </div>
  )
}

function RowSkeleton() {
  return (
    <li className="flex flex-col gap-4 px-5 py-4 sm:flex-row sm:items-center sm:gap-6">
      <div className="min-w-0 flex-1">
        <Skeleton className="h-4 w-36" />
        <Skeleton className="mt-2.5 h-5 w-4/5" />
        <Skeleton className="mt-2.5 h-3 w-52" />
      </div>
      <div className="flex items-center justify-between gap-5 sm:w-[19rem] sm:shrink-0">
        <div className="w-40">
          <Skeleton className="h-4 w-full" />
          <Skeleton className="mt-1.5 h-1.5 w-full" />
        </div>
        <Skeleton className="h-5 w-20" />
      </div>
    </li>
  )
}

function FirstRun() {
  return (
    <EmptyState
      className="mt-10"
      icon={<IconForecast size={20} />}
      title="No forecasts yet."
      description="Start from a rough idea. The desk drafts a precise question, then three specialists estimate the odds."
      action={
        <div className="flex flex-col items-center gap-5">
          <div className="flex max-w-2xl flex-wrap justify-center gap-2">
            {EXAMPLE_IDEAS.slice(0, 4).map((ex) => (
              <Link
                key={ex.idea}
                to="/forecasts/new"
                state={{ idea: ex.idea }}
                className="rounded-full border border-border bg-surface px-3.5 py-1.5 text-left text-[13px] text-text transition-colors hover:border-accent/50 hover:bg-accent-soft"
              >
                {ex.idea}
              </Link>
            ))}
          </div>
          <Link to="/forecasts/new" className={buttonClass('primary')}>
            + New forecast
          </Link>
        </div>
      }
    />
  )
}

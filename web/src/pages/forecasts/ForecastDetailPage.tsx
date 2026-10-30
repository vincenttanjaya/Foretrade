import { Fragment, useRef, useState, type ReactNode } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import { Badge } from '../../components/ui/Badge'
import { Button, buttonClass } from '../../components/ui/Button'
import { Card } from '../../components/ui/Card'
import { Disclaimer } from '../../components/ui/Disclaimer'
import { EmptyState } from '../../components/ui/EmptyState'
import { IconAlert, IconArrowRight, IconCheck, IconChevron, IconForecast } from '../../components/ui/icons'
import { ProbabilityBars } from '../../components/ui/ProbabilityBars'
import { Sparkline } from '../../components/ui/Sparkline'
import { Skeleton } from '../../components/ui/Spinner'
import { useToast } from '../../components/toast'
import { api, ApiError } from '../../lib/api'
import { useAssets } from '../../lib/assets'
import { useAuth } from '../../lib/auth'
import { cx } from '../../lib/cx'
import { errorMessage, isQuota } from '../../lib/errors'
import { fmtDate, fmtProb } from '../../lib/format'
import type { DeskMethods, DeskUnit, DeskView, Forecast, ForecastMaterial, ForecastPoint, ForecastResult, ForecastSource, OutcomeP, ResearchAngle, Specialist } from '../../lib/types'
import { usePoll } from '../../lib/usePoll'
import {
  anchored,
  angleKindLabel,
  categoryLabel,
  cleanDraft,
  daysUntil,
  deepResearchLine,
  draftProblem,
  dueLabel,
  evidenceLink,
  findingsOf,
  fmtChance,
  fmtChange,
  fmtDay,
  fmtLinearChange,
  fmtLevel,
  fmtPct,
  fmtPts,
  fmtQ,
  httpUrl,
  inOrder,
  isUpdatable,
  likelyRange,
  materialsOf,
  measuredOf,
  medianOn,
  mentionedAsset,
  METHOD_LABEL,
  methodOf,
  methodsOf,
  modelled,
  normalizeDraft,
  pct0,
  reportFlags,
  researchLabel,
  researchOf,
  researchStatus,
  roundPcts,
  ruleText,
  scenariosOf,
  SPECIALISTS,
  specialistSpread,
  specialistsHeader,
  statsOf,
  topIndex,
  viewOf,
  yesP,
} from './forecast'
import { DeskVsMarket, ExternalLink } from './DeskVsMarket'
import { BucketViewLine, DeskViewHero, MedianDots } from './DeskView'
import { EvidenceBase, VolatilityReference, WhatToWatch } from './EvidenceBase'
import { LinkedMarketDetails } from './LinkedMarketDetails'
import { YourMaterials } from './Materials'
import { MeasuredHistory } from './MeasuredHistory'
import { ConfidenceBadge, Monogram, ShapeBadge, StatusPill } from './parts'
import { Working } from './RunProgress'
import { InputChips, Scenarios, type IdTargets } from './Scenarios'
import { StrikeLadder } from './StrikeLadder'

export function ForecastDetailPage() {
  const { id = '' } = useParams()
  // Keyed so "Run again" (a new id on the same route) starts from a clean slate.
  return <Detail key={id} id={id} />
}

function Detail({ id }: { id: string }) {
  const { toast } = useToast()
  const { refresh } = useAuth()
  const [forecast, setForecast] = useState<Forecast | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [notFound, setNotFound] = useState(false)
  const loading = useRef(false)
  const lastStatus = useRef<string | null>(null)

  // First load (and Retry, by clearing the error), then every 3 s while the run is in flight.
  usePoll(
    async () => {
      if (loading.current) return
      loading.current = true
      try {
        const { forecast: f } = await api.forecast(id)
        if (lastStatus.current === 'running' && f.status === 'completed') toast('The desk has a number.', 'success')
        if (lastStatus.current === 'running' && f.status === 'failed') toast('The forecast run failed.', 'error')
        // A failed run refunds its credits on the server; re-read them so the top-bar pill catches up.
        if (lastStatus.current !== 'failed' && f.status === 'failed') refresh().catch(() => {})
        // A completed run refunds 1 credit for each new file the desk could not read: the same catch-up.
        else if (lastStatus.current === 'running' && f.status === 'completed' && materialsOf(f).some((m) => m.status === 'failed' && !m.reusedFrom))
          refresh().catch(() => {})
        lastStatus.current = f.status
        setForecast(f)
        setError(null)
      } catch (err) {
        if (err instanceof ApiError && (err.status === 404 || err.status === 400)) setNotFound(true)
        else setError(errorMessage(err))
      } finally {
        loading.current = false
      }
    },
    3000,
    !notFound && ((forecast === null && !error) || forecast?.status === 'running'),
  )

  let body: ReactNode
  if (notFound)
    body = (
      <EmptyState
        className="mt-6"
        icon={<IconForecast size={20} />}
        title="Forecast not found."
        description="The link may be wrong, or this forecast belongs to another account."
        action={
          <Link to="/forecasts" className={buttonClass('secondary')}>
            Back to your forecasts
          </Link>
        }
      />
    )
  else if (!forecast && error)
    body = (
      <EmptyState
        className="mt-6"
        icon={<IconAlert size={20} />}
        title="Couldn't load this forecast."
        description={error}
        action={<Button variant="secondary" onClick={() => setError(null)}>Try again</Button>}
      />
    )
  else if (!forecast) body = <DetailSkeleton />
  else
    body = (
      <>
        <Header f={forecast} onChange={setForecast} />
        {forecast.status === 'archived' && !forecast.result && (
          <>
            <EmptyState title={forecast.archivedFromStatus === 'running' ? 'Archived before the run finished.' : 'No saved report.'}
              description={forecast.archivedFromStatus === 'running' ? 'Restoring keeps this run marked as interrupted. You can review the question and start a new run afterward.' : forecast.error || 'Restore this forecast to review and run the question again.'} />
            <YourMaterials f={forecast} />
            <Submitted f={forecast} />
          </>
        )}
        {forecast.status === 'running' && (
          <>
            <Working f={forecast} reconnecting={!!error} />
            <YourMaterials f={forecast} />
            <Submitted f={forecast} />
          </>
        )}
        {forecast.status === 'failed' && (
          <>
            <Failed f={forecast} />
            <YourMaterials f={forecast} />
            <Submitted f={forecast} />
          </>
        )}
        {(forecast.status === 'completed' || (forecast.status === 'archived' && forecast.result)) &&
          (forecast.result ? (
            <Report f={forecast} r={forecast.result} onChange={setForecast} />
          ) : (
            <EmptyState title="Result unavailable." description="The run completed but returned no report. Try running it again." />
          ))}
      </>
    )

  return <div className="mx-auto max-w-6xl px-4 py-8 md:px-8 md:py-10 print:[print-color-adjust:exact]">{body}</div>
}

// ── Header ──────────────────────────────────────────────────────────────────

/** "3 specialists": the panel that actually ran (a dropped specialist leaves 2); 3 until a result exists. */
const panelSize = (f: Forecast) => {
  const n = f.result?.specialists?.length || 3
  return `${n} specialist${n === 1 ? '' : 's'}`
}

function Header({ f, onChange }: { f: Forecast; onChange: (f: Forecast) => void }) {
  const history = f.history ?? []
  const newest = history[history.length - 1]
  const superseded = f.status === 'completed' && !!newest && newest.id !== f.id
  const updatable = isUpdatable(f)
  return (
    <header className="mb-8">
      <nav aria-label="Breadcrumb" className="label flex flex-wrap items-center gap-x-2 gap-y-1">
        <Link to="/forecasts" className="rounded-sm transition-colors hover:text-text">
          Forecast desk
        </Link>
        <span aria-hidden="true">/</span>
        <span>{categoryLabel(f.category)}</span>
        <span aria-hidden="true">/</span>
        <span aria-current="page">#{f.id.slice(0, 8)}</span>
      </nav>
      <div className="mt-4 flex flex-wrap items-center gap-2">
        <ShapeBadge shape={f.shape} />
        <Badge tone="outline">{categoryLabel(f.category)}</Badge>
        <StatusPill status={f.status} />
        <span className="max-w-full rounded-full border border-border px-2.5 py-0.5 text-[12px] leading-snug text-muted">
          Panel: {panelSize(f)} + aggregator{f.result && ` · research: ${researchChip(f.result, !!f.replay)}`}
        </span>
      </div>
      <h1 className="mt-3 max-w-4xl font-display text-[30px] leading-[1.1] font-medium tracking-[-0.015em] text-balance text-text md:text-[42px]">
        {f.question}
      </h1>
      <div className="mt-4 flex flex-wrap items-center justify-between gap-3">
        <p className="font-mono text-[11.5px] text-muted">
          {f.replay ? (
            <>Run as of {fmtDay(f.replay.asOf, true)} · Resolves {fmtDate(f.resolutionDate)}</>
          ) : (
            <>
              Created {fmtDate(f.createdAt, { time: true })} · {f.linkedMarket ? 'Tracking date' : 'Resolves'} {fmtDate(f.resolutionDate)}
              {f.completedAt && f.result && <> · Completed {fmtDate(f.completedAt, { time: true })}</>}
            </>
          )}
        </p>
        <div className="flex flex-wrap items-center gap-2 print:hidden">
          {updatable && <UpdateButton id={f.id} />}
          {f.result && (f.status === 'completed' || f.status === 'archived') && (
            <Button variant="secondary" size="sm" onClick={() => window.print()}>
              Export PDF
            </Button>
          )}
          {!f.replay && (f.status === 'archived' ? <RestoreButton id={f.id} onChange={onChange} /> : <ArchiveButton id={f.id} />)}
        </div>
      </div>
      {f.status === 'archived' && (
        <p role="note" className="mt-4 rounded-xl border border-border bg-surface-2 px-4 py-3 text-[13.5px] text-muted">
          This forecast is archived. Its saved report is read-only. Restore it to return it to your desk; restoring uses no credits.
        </p>
      )}
      {superseded && (
        <p role="note" className="mt-4 rounded-xl border border-accent/40 bg-accent-soft/60 px-4 py-3 text-[13.5px] text-text print:hidden">
          <Link to={`/forecasts/${newest.id}`} className="font-medium text-accent-ink hover:underline">
            A newer run exists: {newest.outcome} {fmtChance(newest.p)} on {fmtDay(newest.at)} →
          </Link>
        </p>
      )}
      {f.replay && (
        <p role="note" className="mt-4 flex flex-wrap items-center gap-x-2 gap-y-1 rounded-xl border border-accent/40 bg-accent-soft/60 px-4 py-3 text-[13.5px] text-text">
          <span className="font-medium">Historical replay as of {fmtDay(f.replay.asOf, true)}</span>
          <span className="text-muted">· volatility-only reference {fmtChance(f.replay.anchorP)} Yes</span>
          <span className="text-muted">
            · run with only data published before that date.{' '}
            <Link to="/forecasts" className="font-medium text-accent-ink hover:underline">
              See the track record
            </Link>
          </span>
        </p>
      )}
    </header>
  )
}

/** Re-runs the panel on today's evidence as a new run of the same question; this one stays in the history. */
function UpdateButton({ id }: { id: string }) {
  const navigate = useNavigate()
  const { toast } = useToast()
  const { setCredits, refresh } = useAuth()
  const [busy, setBusy] = useState(false)

  async function update() {
    setBusy(true)
    try {
      const res = await api.updateForecast(id)
      if (res.credits) setCredits(res.credits)
      toast('Updating the forecast. The specialists are on it.', 'success')
      navigate(`/forecasts/${res.forecast.id}`)
    } catch (err) {
      toast(errorMessage(err), 'error')
      if (isQuota(err)) refresh().catch(() => {})
      setBusy(false)
    }
  }

  return (
    <Button
      variant="secondary"
      size="sm"
      loading={busy}
      onClick={update}
      title="Re-run the panel on today's evidence; this estimate stays in the history."
    >
      Update forecast <span className="font-mono text-[11.5px] font-normal opacity-70">· 5 credits</span>
    </Button>
  )
}

/** Restores the saved run; this never starts a new forecast. */
function RestoreButton({ id, onChange }: { id: string; onChange: (f: Forecast) => void }) {
  const { toast } = useToast()
  const [busy, setBusy] = useState(false)
  async function restore() {
    setBusy(true)
    try {
      const { forecast } = await api.restoreForecast(id)
      onChange(forecast)
      toast('Forecast restored to your desk.', 'success')
    } catch (err) {
      toast(errorMessage(err), 'error')
    } finally {
      setBusy(false)
    }
  }
  return <Button variant="secondary" size="sm" loading={busy} onClick={restore}>Restore forecast</Button>
}

/** Two-step archive: the first click asks, Escape or Cancel backs out. */
function ArchiveButton({ id }: { id: string }) {
  const navigate = useNavigate()
  const { toast } = useToast()
  const [confirming, setConfirming] = useState(false)
  const [busy, setBusy] = useState(false)

  async function archive() {
    setBusy(true)
    try {
      await api.deleteForecast(id)
      toast('Forecast archived. Find it in the Archived view to restore it.', 'success')
      navigate('/forecasts')
    } catch (err) {
      toast(errorMessage(err), 'error')
      setBusy(false)
    }
  }

  if (!confirming)
    return (
      <Button variant="ghost" size="sm" onClick={() => setConfirming(true)}>
        Archive
      </Button>
    )
  return (
    <div
      role="group"
      aria-label="Confirm archive"
      className="flex items-center gap-2"
      onKeyDown={(e) => e.key === 'Escape' && !busy && setConfirming(false)}
    >
      <span className="text-[13px] text-muted">Archive this forecast?</span>
      <Button variant="ghost" size="sm" autoFocus disabled={busy} onClick={() => setConfirming(false)}>
        Cancel
      </Button>
      <Button variant="danger" size="sm" loading={busy} onClick={archive}>
        Archive
      </Button>
    </div>
  )
}

// ── Running / failed ────────────────────────────────────────────────────────

function Failed({ f }: { f: Forecast }) {
  const navigate = useNavigate()
  const { toast } = useToast()
  const { setCredits, refresh } = useAuth()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const draft = cleanDraft(normalizeDraft(f))
  const materials = materialsOf(f)
  // A root run reuses the files the desk read (same question, so the server's rule holds); a failed update reruns as an
  // update of its parent, which keeps the parent's files. Either way, files this run added and never read are lost.
  const reuse = !f.parentId && materials.some((m) => m.status === 'read')
  const lost = f.parentId ? materials.filter((m) => !m.reusedFrom).length : materials.filter((m) => m.status !== 'read').length

  async function runAgain() {
    const problem = draftProblem(draft)
    if (problem) return setError(`${problem} Edit the question to run it again.`)
    setBusy(true)
    setError(null)
    try {
      // A failed update runs again as an update of the same run, so the lineage stays whole.
      const res = f.parentId
        ? await api.updateForecast(f.parentId)
        : reuse
          ? await api.createForecast(draft, { reuseMaterials: { forecastId: f.id } })
          : await api.createForecast(draft)
      if (res.credits) setCredits(res.credits)
      toast('Running it again. The specialists are on it.', 'success')
      navigate(`/forecasts/${res.forecast.id}`)
    } catch (err) {
      setError(errorMessage(err))
      if (isQuota(err)) refresh().catch(() => {})
      setBusy(false)
    }
  }

  return (
    <Card className="border-down/30 p-5 md:p-6">
      <div className="flex gap-4">
        <span className="grid size-10 shrink-0 place-items-center rounded-full bg-down/10 text-down">
          <IconAlert size={20} />
        </span>
        <div className="min-w-0">
          <h2 className="font-display text-[22px] leading-tight font-medium text-text">The desk couldn't finish this forecast.</h2>
          <p className="mt-1.5 text-[14px] leading-relaxed text-muted">
            {f.error || 'The run stopped before the aggregator returned a result.'}
          </p>
          <div className="mt-4 flex flex-wrap gap-2">
            <Button variant="primary" loading={busy} onClick={runAgain}>
              Run again <span className="font-mono text-[11.5px] font-normal opacity-70">· 5 cr</span>
            </Button>
            <Link to="/forecasts/new" state={{ draft, materials: materials.length }} className={buttonClass('secondary')}>
              Edit question
            </Link>
          </div>
          {lost > 0 && (
            <p className="mt-3 text-[12.5px] leading-snug text-muted">
              {lost === 1 ? "1 file wasn't read" : `${lost} files weren't read`}; attach {lost === 1 ? 'it' : 'them'} again with Edit question.
            </p>
          )}
          {error && (
            <p role="alert" className="mt-3 text-[13px] text-down">
              {error}
            </p>
          )}
        </div>
      </div>
    </Card>
  )
}

/** The question as submitted, shown while there is no report yet. */
function Submitted({ f }: { f: Forecast }) {
  return (
    <div className="mt-6 grid items-start gap-6 md:grid-cols-2">
      <Card className="p-5">
        <div className="label">Outcomes · {f.outcomes.length}</div>
        <ol className="mt-3 flex flex-col gap-2">
          {f.outcomes.map((o, i) => (
            <li key={`${o}-${i}`} className="flex items-baseline gap-3 text-[14px] text-text">
              <span className="font-mono text-[11px] text-muted tabular-nums">{String(i + 1).padStart(2, '0')}</span>
              {o}
            </li>
          ))}
        </ol>
      </Card>
      <Resolution f={f} />
    </div>
  )
}

/** What research ran, for the header: "Brave Search · 8 sources · 3 read", "deep · 22 sources · 12 read", … */
function researchChip(r: ForecastResult, replay: boolean) {
  const { sources, read } = researchOf(r)
  return researchLabel(researchStatus(r, replay), sources.length, read, r.research?.depth)
}

const RESEARCH_CARD_LABEL = { live: 'Live web research', wire: 'Stored news wire', carried: "Previous run's sources", none: 'Live web research' }

// ── Completed report ────────────────────────────────────────────────────────

const ROLE_ORDER = SPECIALISTS.map((s) => s.role)
const pct = (p: number) => `${Math.max(0, Math.min(1, p)) * 100}%`

function Report({ f, r, onChange }: { f: Forecast; r: ForecastResult; onChange: (f: Forecast) => void }) {
  const final = inOrder(r.probabilities, f.outcomes)
  const ensemble = Array.isArray(r.ensemble) && r.ensemble.length ? inOrder(r.ensemble, f.outcomes) : []
  const top = final[topIndex(final)]
  const specialists = (Array.isArray(r.specialists) ? r.specialists : [])
    .slice()
    .sort((a, b) => ROLE_ORDER.indexOf(a.role) - ROLE_ORDER.indexOf(b.role))
  const drivers = Array.isArray(r.drivers) ? r.drivers : []
  const evidence = Array.isArray(r.evidence) ? r.evidence : []
  const flags = reportFlags(r)
  const changes = Array.isArray(r.whatWouldChange) ? r.whatWouldChange : []
  const research = researchOf(r)
  const status = researchStatus(r, !!f.replay)
  const materials = materialsOf(f)
  const findings = findingsOf(r, materials).length
  const deep = status === 'live' && r.research?.depth === 'deep'
  const angles = deep && Array.isArray(r.research?.angles) ? r.research.angles.filter((a) => a && typeof a.id === 'string') : []
  // A numeric report reads its buckets as numbers. Older results, binary and categorical ones have no view.
  const view = f.shape === 'ordered' ? viewOf(r) : null
  const scenarios = view ? scenariosOf(r) : []
  const methods = view ? methodsOf(r) : null
  const measured = view ? measuredOf(r) : null
  const targets: IdTargets = {
    sources: new Map(research.sources.map((s) => [s.id, s])),
    findings: new Map(findingsOf(r, materials).map((x) => [x.id, x])),
    measured: !!measured,
  }

  return (
    <>
      <ResultHero f={f} r={r} final={final} view={view} />
      {!f.replay && (f.shape === 'binary' || f.shape === 'categorical') && <DeskVsMarket f={f} r={r} final={final} onChange={onChange} />}
      {!f.replay && f.shape === 'ordered' && <StrikeLadder f={f} r={r} onChange={onChange} />}
      {(f.history?.length ?? 0) >= 2 && <ProbabilityHistory id={f.id} points={f.history!} view={view} />}
      {!f.replay && f.status !== 'archived' && <Handoff f={f} final={final} view={view} />}
      <p className="mt-3 text-[12.5px] leading-relaxed text-muted">
        AI probability estimates, scored against resolved outcomes in the{' '}
        <Link to="/forecasts" className="font-medium text-accent-ink hover:underline">
          Track record
        </Link>{' '}
        on the Forecasts page.
      </p>

      {view && <Scenarios scenarios={scenarios} unit={view.unit} targets={targets} />}

      {specialists.length > 0 && (
        <section aria-labelledby="desk-views" className="mt-12">
          <div className="label">{specialistsHeader(specialists.length, methods)}</div>
          <h2 id="desk-views" className="mt-1.5 font-display text-[28px] leading-tight font-medium text-text">
            How the desk got there
          </h2>
          <p className="mt-1.5 max-w-2xl text-[14px] leading-relaxed text-muted">
            Each specialist forecasts on its own. The aggregator then weighs their views against the evidence and sets
            the final number.
          </p>
          <div className="mt-5 grid items-start gap-4 md:grid-cols-3 print:grid-cols-1">
            {specialists.map((s, i) => (
              <SpecialistCard key={`${s.role}-${i}`} s={s} outcomes={f.outcomes} binary={f.shape === 'binary'} view={view} targets={targets} />
            ))}
          </div>
        </section>
      )}

      <EvidenceBase f={f} r={r} />
      <YourMaterials f={f} />

      <div className="mt-6 grid items-start gap-6 lg:grid-cols-[minmax(0,1.55fr)_minmax(0,1fr)]">
        <div className="flex min-w-0 flex-col gap-6">
          <VolatilityReference r={r} />
          {drivers.length > 0 && (
            <Card className="p-5 print:break-inside-avoid">
              <div className="label">Drivers · {drivers.length}</div>
              <h3 className="mt-1 font-display text-[19px] font-medium text-text">What moves the number</h3>
              {top && (
                <p className="mt-1 text-[12.5px] text-muted">
                  {/* Contract: "up" raises the top outcome, which for binary is always Yes (as in the headline). */}
                  ▲ raises / ▼ lowers the chance of “{f.shape === 'binary' ? 'Yes' : top.outcome}”. Bars show weight.
                </p>
              )}
              <ul className="mt-4 flex flex-wrap gap-2">
                {drivers.map((d, i) => (
                  <li
                    key={i}
                    className="inline-flex max-w-full items-center gap-2 rounded-xl border border-border bg-surface px-3 py-1.5 text-[13px] leading-snug text-text"
                  >
                    <span aria-hidden="true" className={cx('text-[11px]', d.direction === 'down' ? 'text-down' : 'text-up')}>
                      {d.direction === 'down' ? '▼' : '▲'}
                    </span>
                    <span className="sr-only">{d.direction === 'down' ? 'Lowers:' : 'Raises:'}</span>
                    <span className="min-w-0">{d.factor}</span>
                    <Weight level={d.weight} />
                  </li>
                ))}
              </ul>
            </Card>
          )}

          {evidence.length > 0 && (
            <Card>
              <div className="px-5 pt-5">
                {/* With a findings ledger above, these are the aggregator's picks from it: the key items. */}
                <div className="label">
                  {findings ? 'Key evidence' : 'Evidence trail'} · {evidence.length}
                </div>
                <h3 className="mt-1 font-display text-[19px] font-medium text-text">{findings ? 'What the forecast rests on' : 'What the desk looked at'}</h3>
              </div>
              <ul className="mt-2 divide-y divide-border px-5 pb-1">
                {evidence.map((e, i) => (
                  <li key={i} className="flex gap-3 py-3.5 print:break-inside-avoid">
                    <Stance stance={e.stance} />
                    <div className="min-w-0">
                      <p className="text-[14px] leading-snug text-pretty text-text">{e.claim}</p>
                      <p className="mt-1 flex flex-wrap gap-x-2 font-mono text-[11px] text-muted">
                        <EvidenceSource e={e} sources={research.sources} materials={materials} />
                        {e.asOf && <span>· as of {asOfLabel(e.asOf, f.completedAt ?? f.createdAt)}</span>}
                      </p>
                    </div>
                  </li>
                ))}
              </ul>
            </Card>
          )}

          {(research.queries.length > 0 || research.sources.length > 0 || !!f.context) && (
            <Research
              {...research}
              context={f.context ?? ''}
              note={r.research?.note ?? ''}
              label={status === 'replay' ? `Dated news before ${fmtDay(f.replay?.asOf ?? f.createdAt, true)}` : RESEARCH_CARD_LABEL[status]}
              line={deep ? deepResearchLine(r) : undefined}
              angles={angles}
            />
          )}
        </div>

        <div className="flex min-w-0 flex-col gap-6">
          {view && methods ? (
            <PoolVsFinal final={final} methods={methods} outcomes={f.outcomes} view={view} specialists={specialists} />
          ) : (
            ensemble.length > 0 && (
              <EnsembleVsFinal
                final={final}
                ensemble={ensemble}
                spread={specialistSpread(specialists, f.shape === 'binary' ? 'Yes' : (top?.outcome ?? ''))}
              />
            )
          )}

          <section aria-label="Flags" className="flex flex-col gap-3">
            {flags.length ? (
              flags.map((fl) => (
                <div key={fl.type} role="note" className="flex gap-3 rounded-[14px] border border-accent/40 bg-accent-soft/60 p-4 print:break-inside-avoid">
                  <IconAlert size={18} className="mt-px shrink-0 text-accent-ink" />
                  <div className="min-w-0">
                    <div className="label text-accent-ink">{FLAG_LABEL[fl.type] ?? 'Flag'}</div>
                    {fl.notes.map((note, i) => (
                      <p key={i} className="mt-1 text-[13.5px] leading-snug text-text">
                        {note}
                      </p>
                    ))}
                  </div>
                </div>
              ))
            ) : (
              <Card className="flex items-center gap-2.5 px-4 py-3.5 text-[13px] text-muted">
                <IconCheck size={15} className="shrink-0 text-accent-ink" />
                No conflicting, stale or duplicate signals flagged.
              </Card>
            )}
          </section>

          {measured && <MeasuredHistory m={measured} />}
          <WhatToWatch f={f} r={r} />

          {changes.length > 0 && (
            <Card className="p-5 print:break-inside-avoid">
              <div className="label">What would change our mind</div>
              <ol className="mt-3 flex flex-col gap-2.5">
                {changes.map((c, i) => (
                  <li key={i} className="flex gap-3 text-[13.5px] leading-snug text-text">
                    <span className="font-mono text-[11px] leading-5 text-accent-ink tabular-nums">{String(i + 1).padStart(2, '0')}</span>
                    {c}
                  </li>
                ))}
              </ol>
            </Card>
          )}

          <Resolution f={f} onChange={onChange} />
        </div>
      </div>

      <footer className="mt-10 flex flex-col gap-2 border-t border-border pt-5">
        {r.knowledgeNote && <p className="text-[12.5px] leading-relaxed text-muted">{r.knowledgeNote}</p>}
        <Disclaimer />
      </footer>
    </>
  )
}

function ResultHero({ f, r, final, view }: { f: Forecast; r: ForecastResult; final: OutcomeP[]; view: DeskView | null }) {
  const ti = topIndex(final)
  const top = final[ti]
  const days = daysUntil(f.resolutionDate)
  // When the view leads, the server's numbers line comes first and the summary is the thesis under it.
  const lead = view?.headline === 'view' ? view.lead : ''
  const viewChange = typeof r.viewChange === 'string' ? r.viewChange.trim() : ''

  const summary = (
    <div className="flex min-w-0 flex-col p-6 md:p-8">
      <div className="flex flex-wrap items-center gap-2">
        <span className="label">Desk summary</span>
        <ConfidenceBadge level={r.confidence} />
      </div>
      {lead && <p className="mt-3 text-[16px] leading-relaxed font-medium text-pretty text-text">{lead}</p>}
      {lead ? (
        r.summary && <p className="mt-2.5 text-[15px] leading-relaxed text-pretty text-text">{r.summary}</p>
      ) : (
        <p className="mt-3 text-[16px] leading-relaxed text-pretty text-text">{r.summary || 'No summary was returned.'}</p>
      )}
      {(viewChange || r.changeNote) && (
        // The numbers first, as the server wrote them from the two stored views; then the desk's reasons, in words.
        <div className="mt-3 border-l-2 border-accent pl-3 text-[14px] leading-relaxed text-pretty text-text">
          <span className="label block text-accent-ink">Changed since last run</span>
          {viewChange && (
            // Each part stays whole; on a narrow screen the next wraps under it.
            <p className="font-mono text-[12.5px] leading-relaxed tabular-nums">
              {viewChange.split(' · ').map((part, i) => (
                <Fragment key={i}>
                  {i > 0 && ' '}
                  <span className="whitespace-nowrap">{i > 0 ? `· ${part}` : part}</span>
                </Fragment>
              ))}
            </p>
          )}
          {r.changeNote && <p className={cx(viewChange && 'mt-1')}>{r.changeNote}</p>}
        </div>
      )}
      <p className="mt-auto pt-6 font-mono text-[11px] text-muted">
        {f.linkedMarket ? 'Tracking date' : 'Resolves'} {fmtDate(f.resolutionDate)}
        {f.linkedMarket ? ' · manual resolution' : days != null && ` · ${dueLabel(days)}`}
      </p>
      <Disclaimer className="mt-2" />
    </div>
  )

  if (f.shape === 'binary') {
    const yes = yesP(final)
    const no = yes == null ? null : 1 - yes
    const [y, n] = roundPcts([yes ?? 0, no ?? 0])
    const yesLabel = yes == null ? fmtProb(null) : fmtPct(y, yes)
    const noLabel = no == null ? fmtProb(null) : fmtPct(n, no)
    const big = yesLabel
    return (
      <Card className="grid overflow-hidden lg:grid-cols-[minmax(0,1fr)_minmax(0,1.15fr)] print:break-inside-avoid">
        <div className="border-b border-border bg-accent-soft/35 p-6 md:p-8 lg:border-r lg:border-b-0">
          <div className="label">Probability estimate · Yes</div>
          <div className="mt-2 font-display text-[96px] leading-[0.95] font-medium tracking-[-0.04em] text-text tabular-nums md:text-[120px]">
            {big.replace('%', '')}
            <span className="ml-1 align-top text-[0.4em] tracking-normal text-muted">%</span>
          </div>
          <div
            role="img"
            aria-label={`Yes ${yesLabel}, No ${noLabel}`}
            className="mt-6 flex h-3 overflow-hidden rounded-full bg-surface-2"
          >
            <span className="h-full origin-left animate-bar bg-accent" style={{ width: pct(yes ?? 0) }} />
            <span className="h-full flex-1 bg-muted/30" />
          </div>
          <div className="mt-2 flex justify-between font-mono text-[12px] tabular-nums">
            <span className="font-semibold text-accent-ink">YES {yesLabel}</span>
            <span className="text-muted">NO {noLabel}</span>
          </div>
        </div>
        {summary}
      </Card>
    )
  }

  if (view?.headline === 'view')
    return (
      <Card className="grid overflow-hidden lg:grid-cols-[minmax(0,1.1fr)_minmax(0,1fr)] print:break-inside-avoid">
        <DeskViewHero view={view} final={final} resolutionDate={f.resolutionDate} />
        {summary}
      </Card>
    )

  const range = f.shape === 'ordered' ? likelyRange(final) : null
  const pcts = roundPcts(final.map((o) => o.p))
  return (
    <Card className="grid overflow-hidden lg:grid-cols-[minmax(0,1.1fr)_minmax(0,1fr)] print:break-inside-avoid">
      <div className="border-b border-border bg-accent-soft/35 p-6 md:p-8 lg:border-r lg:border-b-0">
        <div className="label">Most likely {f.shape === 'ordered' ? 'range' : 'outcome'}</div>
        {top ? (
          <div className="mt-2 flex flex-wrap items-baseline gap-x-4 gap-y-1">
            <span className="min-w-0 font-display text-[34px] leading-tight font-medium text-balance text-text md:text-[40px]">
              {top.outcome}
            </span>
            <span className="font-display text-[34px] leading-tight text-accent-ink tabular-nums md:text-[40px]">{fmtPct(pcts[ti], top.p)}</span>
          </div>
        ) : (
          <p className="mt-2 text-muted">No probabilities were returned.</p>
        )}
        {range && range.to > range.from && (
          <p className="mt-2 text-[13.5px] text-muted">
            <span className="font-medium text-text">
              {final[range.from].outcome} → {final[range.to].outcome}
            </span>{' '}
            together hold {pcts.slice(range.from, range.to + 1).reduce((a, b) => a + b, 0)}%.
          </p>
        )}
        {view?.headline === 'bucket' && <BucketViewLine view={view} />}
        <ProbabilityBars className="mt-6" items={final.map((o) => ({ label: o.outcome, p: o.p }))} />
      </div>
      {summary}
    </Card>
  )
}

function SpecialistCard({
  s,
  outcomes,
  binary,
  view,
  targets,
}: {
  s: Specialist
  outcomes: string[]
  binary: boolean
  view: DeskView | null
  targets: IdTargets
}) {
  const meta = SPECIALISTS.find((x) => x.role === s.role)
  const items = inOrder(s.probabilities, outcomes)
  const pcts = roundPcts(items.map((o) => o.p))
  const head = binary ? Math.max(0, items.findIndex((o) => o.outcome.trim().toLowerCase() === 'yes')) : topIndex(items) // yesP's pick
  const top = items[topIndex(items)]
  const factors = Array.isArray(s.keyFactors) ? s.keyFactors : []
  // Numeric reports: the method the server saw it use and, for a member that ran its method's tool, its median
  // and where that tool started it. A judgment or fallback member (and an older run's, with no method) keeps its top
  // range: its median would rest on no evidence.
  // A view no evidence anchors (basis none) states no median, so neither does its member: the top range line instead.
  const method = view ? methodOf(s) : null
  const stats = view && anchored(view) && method && modelled(method) ? statsOf(s) : null
  const anchor = view && stats && Array.isArray(s.anchor) ? medianOn(view, inOrder(s.anchor, outcomes)) : null
  const model = view && Array.isArray(s.model) ? s.model.filter((m) => m && typeof m.name === 'string' && Number.isFinite(m.price) && Number.isFinite(m.p)) : []
  return (
    <Card className="flex min-w-0 flex-col p-5">
      <div className="flex items-start justify-between gap-3">
        <div className="flex min-w-0 items-center gap-3">
          <Monogram glyph={meta?.glyph ?? '·'} />
          <h3 className="min-w-0 font-display text-[17px] leading-tight font-medium text-text">{s.name || meta?.name || 'Specialist'}</h3>
        </div>
        <ConfidenceBadge level={s.confidence} />
      </div>
      {method && (
        <div className="mt-3">
          <Badge tone={modelled(method) ? 'neutral' : 'outline'} className={modelled(method) ? 'text-text' : undefined} title="The method the server recorded from the tool calls that ran">
            {METHOD_LABEL[method]}
          </Badge>
        </div>
      )}
      {view && stats ? (
        <div className="mt-4 flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
          <span className="font-display text-[30px] leading-none text-text tabular-nums">{fmtQ(stats.median, view.unit)}</span>
          <span className="text-[12.5px] text-muted">
            median{view.reference && stats.pAbove && ` · ${fmtChance(stats.pAbove.value)} ≥ today`}
          </span>
        </div>
      ) : (
        <div className="mt-4 flex items-baseline gap-2">
          <span className="font-display text-[30px] leading-none text-text tabular-nums">
            {items[head] ? fmtPct(pcts[head], items[head].p) : fmtProb(null)}
          </span>
          <span className="min-w-0 truncate text-[12.5px] text-muted">{binary ? 'yes' : top?.outcome}</span>
        </div>
      )}
      {view && stats && anchor && (
        <p className="mt-1.5 font-mono text-[11.5px] text-muted tabular-nums">
          Started from its tool: median {fmtQ(anchor, view.unit)} → {fmtQ(stats.median, view.unit)}
        </p>
      )}
      <ProbabilityBars compact className="mt-3" items={items.map((o) => ({ label: o.outcome, p: o.p }))} />
      {view && model.length > 0 && <ValuationModel rows={model} unit={view.unit} targets={targets} />}
      {s.reasoning && <Reasoning text={s.reasoning} />}
      {factors.length > 0 && (
        <div className="mt-4 border-t border-border pt-3.5">
          <div className="label">Key factors</div>
          <ul className="mt-2 flex flex-col gap-1.5">
            {factors.map((k, i) => (
              <li key={i} className="flex gap-2 text-[13px] leading-snug text-text">
                <span aria-hidden="true" className="text-accent">
                  •
                </span>
                {k}
              </li>
            ))}
          </ul>
        </div>
      )}
    </Card>
  )
}

function Reasoning({ text }: { text: string }) {
  const [open, setOpen] = useState(false)
  const long = text.length > 280
  return (
    <div className="mt-4">
      <p className={cx('text-[13.5px] leading-relaxed whitespace-pre-line text-muted', long && !open && 'line-clamp-5 print:line-clamp-none')}>{text}</p>
      {long && (
        <button
          type="button"
          aria-expanded={open}
          onClick={() => setOpen(!open)}
          className="mt-1.5 rounded-sm text-[12.5px] font-medium text-accent-ink hover:underline print:hidden"
        >
          {open ? 'Show less' : 'Read full reasoning'}
        </button>
      )}
    </div>
  )
}

function EnsembleVsFinal({
  final,
  ensemble,
  spread,
}: {
  final: OutcomeP[]
  ensemble: OutcomeP[]
  spread: ReturnType<typeof specialistSpread>
}) {
  return (
    <Card className="p-5 print:break-inside-avoid">
      <div className="label">Ensemble vs final</div>
      <p className="mt-1 text-[12.5px] leading-snug text-muted">
        The ensemble is the plain average of the specialists. The aggregator adjusts it for evidence quality and overlap.
      </p>
      {spread && (
        <p className="mt-2 font-mono text-[12px] text-text tabular-nums">
          Specialist spread {spread.pts} pts — {spread.level} disagreement
        </p>
      )}
      <CompareBars final={final} base={ensemble} baseLabel="ENSEMBLE" />
    </Card>
  )
}

/** Each outcome's starting share (the ensemble, or the weighted pool) against the final, with the move in points. */
function CompareBars({ final, base, baseLabel }: { final: OutcomeP[]; base: OutcomeP[]; baseLabel: string }) {
  const fp = roundPcts(final.map((o) => o.p))
  const ep = roundPcts(base.map((o) => o.p))
  return (
    <>
      <ul className="mt-4 flex flex-col gap-3.5">
        {final.map((o, i) => {
          const e = base[i]?.p ?? 0
          const pp = fp[i] - (ep[i] ?? 0) // in the printed whole percents, so the arrow matches the numbers

          return (
            <li key={`${o.outcome}-${i}`}>
              <div className="flex items-baseline justify-between gap-3 text-[13px]">
                <span className="min-w-0 truncate text-text" title={o.outcome}>
                  {o.outcome}
                </span>
                <span className="shrink-0 font-mono text-[12px] text-muted tabular-nums">
                  {fmtPct(ep[i] ?? 0, e)} → <span className="font-semibold text-text">{fmtPct(fp[i], o.p)}</span>
                  <span className="ml-1.5 inline-block w-11 text-right">
                    {pp === 0 ? '±0' : `${pp > 0 ? '▲' : '▼'}${Math.abs(pp)}pp`}
                  </span>
                </span>
              </div>
              <div aria-hidden="true" className="mt-1.5 flex flex-col gap-1">
                <span className="h-1 overflow-hidden rounded-full bg-surface-2">
                  <span className="block h-full origin-left animate-bar rounded-full bg-muted/50" style={{ width: pct(e) }} />
                </span>
                <span className="h-1 overflow-hidden rounded-full bg-surface-2">
                  <span className="block h-full origin-left animate-bar rounded-full bg-accent" style={{ width: pct(o.p) }} />
                </span>
              </div>
            </li>
          )
        })}
      </ul>
      <div aria-hidden="true" className="mt-4 flex gap-4 font-mono text-[10.5px] tracking-wide text-muted">
        <span className="flex items-center gap-1.5">
          <span className="h-1 w-3.5 rounded-full bg-muted/50" /> {baseLabel}
        </span>
        <span className="flex items-center gap-1.5">
          <span className="h-1 w-3.5 rounded-full bg-accent" /> FINAL
        </span>
      </div>
    </>
  )
}

const AGREEMENT_LINE: Record<DeskMethods['agreement'], string> = {
  independent_agree: 'Independent methods agree',
  diverse: 'Methods disagree; the desk resolved them',
  duplicate: 'Members duplicated one view; counted once',
}
const GUARD_LINE: Record<NonNullable<DeskMethods['guard']>, string> = {
  sharpness: 'Server check: the final distribution was sharper than the methods support, so it was blended halfway back to the pool.',
  width: 'Server check: the final 80% range was narrower than the measured history and the scenarios support, so it was blended halfway back to the pool.',
  volatility: 'Server check: the final distribution was widened to the measured volatility: no cited input supports a narrower range.',
}
const SHORT_ROLE: Record<string, string> = { base_rate: 'Base rate', quant: 'Quant', skeptic: 'Skeptic' }
const methodName = (method: string, role: string) => METHOD_LABEL[method as keyof typeof METHOD_LABEL] ?? SPECIALISTS.find((x) => x.role === role)?.name ?? role
/** Each weight's name: its method, with the member's role when two members share one ("Quant, judgment (no model)"). */
function weightNames(weights: DeskMethods['weights']) {
  const names = weights.map((w) => methodName(w.method, w.role))
  return names.map((n, i) =>
    names.indexOf(n) === names.lastIndexOf(n) || !SHORT_ROLE[weights[i].role] ? n : `${SHORT_ROLE[weights[i].role]}, ${n.charAt(0).toLowerCase()}${n.slice(1)}`,
  )
}

/**
 * The weighted pool the aggregator started from (each specialist weighted by its method for this horizon),
 * against the final; whether the methods agreed, the server's guard, and each member's median on the hero's axis.
 */
function PoolVsFinal({ final, methods, outcomes, view, specialists }: { final: OutcomeP[]; methods: DeskMethods; outcomes: string[]; view: DeskView; specialists: Specialist[] }) {
  const pool = inOrder(methods.pool, outcomes)
  const weights = methods.weights.slice().sort((a, b) => ROLE_ORDER.indexOf(a.role) - ROLE_ORDER.indexOf(b.role))
  const wp = roundPcts(weights.map((w) => w.weight))
  const names = weightNames(weights)
  const guard = methods.guard && !/server check/i.test(methods.note) ? GUARD_LINE[methods.guard] : ''
  // The medians only where the view states one, and only for members that ran their method.
  const poolMedian = anchored(view) ? medianOn(view, pool) : null
  const rows = [
    ...specialists.flatMap((s) => {
      const m = methodOf(s)
      const st = m && modelled(m) ? statsOf(s) : null
      return st ? [{ label: SHORT_ROLE[s.role] ?? s.name, q: st.median }] : []
    }),
    ...(poolMedian ? [{ label: 'Pool', q: poolMedian }] : []),
    { label: 'Final', q: view.median, final: true },
  ]
  return (
    <Card className="p-5 print:break-inside-avoid">
      <div className="label">Weighted pool vs final</div>
      <p className="mt-1 text-[12.5px] leading-snug text-muted">
        The pool weighs each specialist by its method for this horizon. The aggregator moves away from it only for a stated reason.
      </p>
      {weights.length > 0 && (
        <>
          <p className="mt-2.5 font-mono text-[12px] leading-relaxed text-text tabular-nums">
            {weights.map((_, i) => `${names[i]} ${wp[i]}%`).join(' · ')}
          </p>
          {weights.some((w) => w.why) && (
            <ul className="mt-1.5 flex flex-col gap-1">
              {weights.map((w, i) =>
                w.why ? (
                  <li key={i} className="text-[12.5px] leading-snug text-muted">
                    <span className="text-text">{names[i]}:</span> {w.why}
                  </li>
                ) : null,
              )}
            </ul>
          )}
          {methods.weightsFrom === 'prior' && <p className="mt-1.5 text-[12px] leading-snug text-muted">Prior weights for this horizon: the desk set none of its own.</p>}
        </>
      )}
      <p className="mt-3 text-[13px] leading-snug font-medium text-text">{AGREEMENT_LINE[methods.agreement]}</p>
      {(methods.note || guard) && (
        <p className="mt-1 text-[12.5px] leading-snug text-pretty text-muted">{[methods.note, guard].filter(Boolean).join(' ')}</p>
      )}
      <CompareBars final={final} base={pool} baseLabel="POOL" />
      {poolMedian && <MedianDots view={view} rows={rows} />}
    </Card>
  )
}

/** The quant's valuation scenarios as the scenario model priced them, with each input's basis. */
function ValuationModel({ rows, unit, targets }: { rows: NonNullable<Specialist['model']>; unit: DeskUnit; targets: IdTargets }) {
  const pcts = roundPcts(rows.map((r) => r.p))
  return (
    <details className="group mt-4 rounded-xl border border-border">
      <summary className="flex cursor-pointer list-none items-center gap-2 px-3.5 py-2.5 text-[13px] font-medium text-text [&::-webkit-details-marker]:hidden">
        <IconChevron size={12} strokeWidth={2} className="shrink-0 text-muted transition-transform duration-150 group-open:rotate-90 print:hidden" />
        Valuation model
        <span className="ml-auto font-mono text-[11px] font-normal text-muted">{rows.length} scenarios</span>
      </summary>
      <table className="w-full text-[12.5px]">
        <caption className="sr-only">The scenario model's prices, before the desk weighed them</caption>
        <thead className="border-y border-border">
          <tr>
            <th scope="col" className="label px-3.5 py-1.5 text-left font-medium">
              Scenario
            </th>
            <th scope="col" className="label px-2 py-1.5 text-right font-medium">
              Weight
            </th>
            <th scope="col" className="label py-1.5 pr-3.5 pl-2 text-right font-medium">
              Price
            </th>
          </tr>
        </thead>
        <tbody className="divide-y divide-border">
          {rows.map((r, i) => (
            <tr key={i} className="align-top">
              <th scope="row" className="px-3.5 py-2 text-left font-normal">
                <span className="block text-text">{r.name}</span>
                <span className="font-mono text-[10.5px] tracking-wide text-muted uppercase">{r.kind}</span>
                {Array.isArray(r.inputs) && r.inputs.length > 0 && (
                  <div className="mt-1.5">
                    <InputChips inputs={r.inputs} unit={unit} targets={targets} label={`${r.name} inputs`} />
                  </div>
                )}
              </th>
              <td className="px-2 py-2 text-right font-mono text-text tabular-nums">{pcts[i]}%</td>
              <td className="py-2 pr-3.5 pl-2 text-right font-mono whitespace-nowrap text-text tabular-nums">
                {fmtLevel(r.price, unit)}
                {r.vsRefPct != null && Number.isFinite(r.vsRefPct) && <span className="block text-[11px] text-muted">{fmtChange(r.vsRefPct)}</span>}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </details>
  )
}

/** Hands a completed report to the agent: a prompt and the forecast as context (the agent page sends it). */
function Handoff({ f, final, view }: { f: Forecast; final: OutcomeP[]; view: DeskView | null }) {
  const navigate = useNavigate()
  const { assets } = useAssets()
  const symbol = f.priceRule?.symbol ?? mentionedAsset(f.question, assets)
  const top = f.shape === 'binary' ? { outcome: 'Yes', p: yesP(final) } : final[topIndex(final)]
  // A numeric report that leads with its view hands over the median (its 50% range when no evidence anchors one), not
  // a range that does not stand out.
  const read =
    view?.headline !== 'view'
      ? `${top?.outcome} ${fmtProb(top?.p)}`
      : anchored(view)
        ? `median ${fmtQ(view.median, view.unit)}${view.reference && view.pAbove ? `, ${fmtChance(view.pAbove.value)} at or above today` : ''}`
        : `50% range ${fmtQ(view.p25, view.unit)}–${fmtQ(view.p75, view.unit)}`
  const head = `I just read the forecast desk report "${f.question}" (${read}).`
  // No universe asset to trade: ask about the forecast instead of ending in "no trade".
  const prompt = symbol
    ? `${head} What's the trade? Analyze it and propose a paper position with a stop and target if the setup supports one.`
    : `${head} Explain what drives it${f.result?.market ? ' and how the desk compares with Polymarket' : ''}, and what would change the view.`
  return (
    <div className="mt-4 flex flex-wrap items-center gap-x-5 gap-y-2 print:hidden">
      <Button variant="primary" onClick={() => navigate('/', { state: { prompt, context: { forecastId: f.id, symbol } } })}>
        {symbol ? 'Discuss a trade with the agent' : 'Ask the agent about this forecast'} <IconArrowRight size={15} />
      </Button>
      {symbol && (
        <Link to={`/trade/${symbol}`} className="rounded-sm text-[13.5px] font-medium text-accent-ink hover:underline">
          Open {symbol} in Trade
        </Link>
      )}
    </div>
  )
}

function Resolution({ f, onChange }: { f: Forecast; onChange?: (f: Forecast) => void }) {
  const days = daysUntil(f.resolutionDate)
  return (
    <Card className="p-5 print:break-inside-avoid">
      <div className="label">{f.linkedMarket ? 'Tracking & manual resolution' : 'Resolution'}</div>
      <div className="mt-2 flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
        <span className="font-display text-[22px] leading-tight text-text">{fmtDate(f.resolutionDate)}</span>
        {!f.linkedMarket && days != null && (
          <span className="font-mono text-[11.5px] text-muted">
            {days > 0 ? `${days} day${days === 1 ? '' : 's'} remaining` : days === 0 ? 'Resolves today' : 'Resolution date passed'}
          </span>
        )}
      </div>
      <p className="mt-3 text-[13.5px] leading-relaxed whitespace-pre-line text-muted">
        {f.resolutionCriteria || 'No resolution criteria were given.'}
      </p>
      {f.linkedMarket && <LinkedMarketDetails market={f.linkedMarket} className="mt-4" />}
      <ResolutionState f={f} onChange={onChange} />
    </Card>
  )
}

/** Resolved, resolving itself from Binance, or (the user's own completed forecast) resolvable by hand. */
function ResolutionState({ f, onChange }: { f: Forecast; onChange?: (f: Forecast) => void }) {
  const { toast } = useToast()
  const [picked, setPicked] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const own = !f.replay && f.status !== 'archived' && !!onChange

  async function resolve(outcome: string | null) {
    setBusy(true)
    setError(null)
    try {
      const res = await api.resolveForecast(f.id, outcome)
      onChange?.(res.forecast)
      setPicked(null)
      toast(outcome ? `Resolved ${outcome}. It counts toward your track record.` : 'Resolution cleared.', 'success')
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setBusy(false)
    }
  }

  const wrap = (children: ReactNode) => (
    <div className="mt-4 border-t border-border pt-3.5">
      {children}
      {error && (
        <p role="alert" className="mt-2 text-[13px] text-down">
          {error}
        </p>
      )}
    </div>
  )

  if (f.resolution) {
    const final = f.result ? inOrder(f.result.probabilities, f.outcomes) : []
    const top = f.shape === 'binary' ? ((yesP(final) ?? 0) >= 0.5 ? 'Yes' : 'No') : final[topIndex(final)]?.outcome
    const right = top === f.resolution.outcome
    return wrap(
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="flex items-center gap-2 text-[13.5px] text-text">
          <Badge tone={right ? 'up' : 'down'} title={right ? "The desk's top outcome happened" : "The desk's top outcome did not happen"}>
            {right ? '✓' : '✗'}
          </Badge>
          Resolved {f.resolution.outcome} · {fmtDay(f.resolution.resolvedAt, true)} · {f.resolution.by}
        </p>
        {own && f.resolution.by === 'manual' && (
          <Button variant="ghost" size="sm" className="print:hidden" loading={busy} onClick={() => resolve(null)}>
            Clear
          </Button>
        )}
      </div>,
    )
  }
  if (f.status === 'archived') return wrap(<p className="text-[13.5px] text-muted">Resolution is paused while this forecast is archived.</p>)
  if (f.priceRule)
    return wrap(<p className="text-[13.5px] leading-relaxed text-text">Resolves automatically from the Binance {ruleText(f.priceRule, f.resolutionDate)}.</p>)
  if (!own || f.status !== 'completed') return null
  if (!picked)
    return wrap(
      <div className="print:hidden">
        <div className="label">Resolve</div>
        <p className="mt-1 text-[12.5px] text-muted">Mark what happened to score this forecast.</p>
        <div role="group" aria-label="What happened" className="mt-2.5 flex flex-wrap gap-1.5">
          {f.outcomes.map((o) => (
            <Button key={o} variant="secondary" size="sm" onClick={() => setPicked(o)}>
              {o}
            </Button>
          ))}
        </div>
      </div>,
    )
  return wrap(
    <div role="group" aria-label="Confirm resolution" className="print:hidden" onKeyDown={(e) => e.key === 'Escape' && !busy && setPicked(null)}>
      <p className="text-[13.5px] text-text">Resolve as {picked}? It counts toward your track record.</p>
      <div className="mt-2.5 flex gap-2">
        <Button variant="ghost" size="sm" autoFocus disabled={busy} onClick={() => setPicked(null)}>
          Cancel
        </Button>
        <Button variant="primary" size="sm" loading={busy} onClick={() => resolve(picked)}>
          Resolve as {picked}
        </Button>
      </div>
    </div>,
  )
}

// ── Small pieces ────────────────────────────────────────────────────────────

const FLAG_LABEL: Record<string, string> = {
  conflict: 'Conflicting signals',
  stale: 'Stale data',
  duplicate: 'Possible duplicate',
  low_information: 'Low information',
}

function Weight({ level }: { level: string }) {
  const n = level === 'high' ? 3 : level === 'medium' ? 2 : 1
  return (
    <span title={`${level} weight`} className="inline-flex shrink-0 items-end gap-[2px]">
      {[1, 2, 3].map((i) => (
        <span key={i} aria-hidden="true" className={cx('w-[3px] rounded-[1px]', i <= n ? 'bg-text/70' : 'bg-border')} style={{ height: 4 + i * 3 }} />
      ))}
      <span className="sr-only">{level} weight</span>
    </span>
  )
}

function Stance({ stance }: { stance: string }) {
  if (stance === 'supports')
    return (
      <Badge tone="up" className="mt-0.5 w-[5.6rem] justify-center">
        <span aria-hidden="true">▲</span> Supports
      </Badge>
    )
  if (stance === 'opposes')
    return (
      <Badge tone="down" className="mt-0.5 w-[5.6rem] justify-center">
        <span aria-hidden="true">▼</span> Opposes
      </Badge>
    )
  return (
    <Badge tone="neutral" className="mt-0.5 w-[5.6rem] justify-center">
      Neutral
    </Badge>
  )
}

/** Evidence "asOf": a date, a month, "training data", or "live" (= data read during the run). */
const asOfLabel = (asOf: string, runAt: string) =>
  /^\d{4}-\d{2}-\d{2}/.test(asOf) ? fmtDate(asOf.slice(0, 10)) : asOf.trim().toLowerCase() === 'live' ? fmtDate(runAt, { time: true }) : asOf

/**
 * An evidence item's source: its research URL when it has one, else the source text. The text is model-written
 * (and the model reads untrusted web pages), so it is never linked; only the server-mapped `url` is.
 */
function EvidenceSource({ e, sources, materials }: { e: ForecastResult['evidence'][number]; sources: ForecastSource[]; materials: ForecastMaterial[] }) {
  const link = evidenceLink(e, sources, materials)
  if (!link) return <span className="min-w-0 break-words">{e.source || 'Unsourced'}</span>
  return (
    <span className="min-w-0 break-words">
      {link.id && <span className="mr-1.5 text-accent-ink">{link.id}</span>}
      {link.internal ? (
        // The user's own file: its row in "Your materials" on this page, never a URL.
        <a href={link.href} className="rounded-sm text-accent-ink hover:underline">
          Your material · {link.label}
        </a>
      ) : (
        <ExternalLink href={link.href}>{link.label}</ExternalLink>
      )}
    </span>
  )
}

/** The web research pack the specialists and the aggregator worked from. A deep run lists its angles instead of the queries. */
function Research({
  queries,
  sources,
  read,
  label,
  line,
  angles,
  context,
  note,
}: ReturnType<typeof researchOf> & { label: string; line?: string; angles: ResearchAngle[]; context: string; note: string }) {
  return (
    <Card>
      <div className="px-5 pt-5">
        <div className="label">
          {line ?? (
            <>
              {label} · {sources.length} source{sources.length === 1 ? '' : 's'}
              {read > 0 && ` · ${read} read`}
            </>
          )}
        </div>
        <h3 className="mt-1 font-display text-[19px] font-medium text-text">Research</h3>
        {context && <YourContext text={context} />}
        {angles.length > 0 ? (
          <Angles angles={angles} />
        ) : queries.length > 0 && (
          <ul aria-label="Search queries" className="mt-3 flex flex-wrap items-center gap-1.5">
            <li className="text-[12.5px] text-muted">Searched:</li>
            {queries.map((q, i) => (
              <li key={i} className="max-w-full rounded-full border border-border bg-surface-2 px-2.5 py-0.5 text-[12.5px] break-words text-text">
                {q}
              </li>
            ))}
          </ul>
        )}
      </div>
      {sources.length > 0 ? (
        <ol className="mt-2 divide-y divide-border px-5 pb-1">
          {sources.map((s) => {
            const url = httpUrl(s.url)
            const title = s.title || url?.hostname.replace(/^www\./, '') || s.id
            return (
              <li key={s.id} className="flex items-start gap-3 py-3">
                <span className="w-6 shrink-0 font-mono text-[11px] leading-5 text-accent-ink tabular-nums">{s.id}</span>
                <div className="min-w-0 flex-1">
                  <p className="text-[13.5px] leading-snug text-pretty">
                    {url ? <ExternalLink href={url.href}>{title}</ExternalLink> : <span className="text-text">{title}</span>}
                  </p>
                  <p className="mt-0.5 font-mono text-[11px] text-muted">
                    {[s.source, s.publishedAt && fmtDate(s.publishedAt)].filter(Boolean).join(' · ')}
                  </p>
                </div>
                {s.read && (
                  <Badge tone="accent" className="mt-0.5" title="The desk read the full article">
                    <IconCheck size={11} strokeWidth={2.4} />
                    Read
                  </Badge>
                )}
              </li>
            )
          })}
        </ol>
      ) : (
        <p className="px-5 pt-3 pb-5 text-[13px] leading-relaxed text-muted">
          {note || 'The searches returned no usable sources, so the desk worked from model knowledge and market data.'}
        </p>
      )}
    </Card>
  )
}

/** A deep run's research plan: one disclosure per angle ("Comparables · Recent SGX debuts · 11 findings"), its queries inside. */
function Angles({ angles }: { angles: ResearchAngle[] }) {
  return (
    <div className="mt-3">
      <div className="label">Angles · {angles.length}</div>
      <ul aria-label="Research angles" className="mt-1.5 flex flex-col divide-y divide-border rounded-xl border border-border">
        {angles.map((a) => {
          const queries = (Array.isArray(a.queries) ? a.queries : []).filter((q) => typeof q === 'string' && q.trim())
          const n = Number.isFinite(a.findings) ? a.findings : 0
          return (
            <li key={a.id} className="min-w-0">
              <details className="group">
                <summary className="flex cursor-pointer list-none items-baseline gap-2 px-3.5 py-2.5 text-[13px] leading-snug [&::-webkit-details-marker]:hidden">
                  <IconChevron size={12} strokeWidth={2} className="shrink-0 self-center text-muted transition-transform duration-150 group-open:rotate-90 print:hidden" />
                  <span className="min-w-0 flex-1 break-words">
                    <span className="font-medium text-text">{angleKindLabel(a.kind)}</span>
                    <span className="text-muted"> · {a.label}</span>
                  </span>
                  <span className="shrink-0 font-mono text-[11px] text-muted tabular-nums">
                    {n} finding{n === 1 ? '' : 's'}
                  </span>
                </summary>
                {queries.length > 0 && (
                  <ul aria-label={`Queries for ${a.label}`} className="flex flex-wrap gap-1.5 px-3.5 pb-3 pl-8">
                    {queries.map((q, i) => (
                      <li key={i} className="max-w-full rounded-full border border-border bg-surface-2 px-2.5 py-0.5 text-[12px] break-words text-text">
                        {q}
                      </li>
                    ))}
                  </ul>
                )}
              </details>
            </li>
          )
        })}
      </ul>
    </div>
  )
}

const CONTEXT_PREVIEW = 300

/** The user's notes the desk saw as S0: shown as theirs and unverified, the first 300 characters until opened. */
function YourContext({ text }: { text: string }) {
  const [open, setOpen] = useState(false)
  const long = text.length > CONTEXT_PREVIEW
  return (
    <div className="mt-3 rounded-xl border border-border bg-surface-2 px-3.5 py-3">
      <div className="label">
        <span className="mr-1.5 text-accent-ink">S0</span>Your context · unverified, checked as a claim
      </div>
      <p className="mt-1.5 text-[13px] leading-relaxed break-words whitespace-pre-line text-text">
        {long && !open ? (
          <>
            <span className="print:hidden">{text.slice(0, CONTEXT_PREVIEW).trimEnd()}…</span>
            <span className="hidden print:inline">{text}</span>
          </>
        ) : (
          text
        )}
      </p>
      {long && (
        <button
          type="button"
          aria-expanded={open}
          onClick={() => setOpen(!open)}
          className="mt-1 rounded-sm text-[12.5px] font-medium text-accent-ink hover:underline print:hidden"
        >
          {open ? 'Show less' : 'Show all'}
        </button>
      )}
    </div>
  )
}

/**
 * The lineage's runs. A report whose view leads tracks the desk's median from run to run (its top range on a flat
 * distribution says nothing); otherwise the headline outcome's probability, with the median beside it when known.
 */
function ProbabilityHistory({ id, points, view }: { id: string; points: ForecastPoint[]; view: DeskView | null }) {
  const pcts = points.map((h) => pct0(h.p))
  const median = (h: ForecastPoint) => (typeof h.median === 'number' && Number.isFinite(h.median) && (view?.scale !== 'log' || h.median > 0) ? h.median : null)
  const medians = points.map(median)
  const byMedian = view?.headline === 'view' && anchored(view) && medians.every((m) => m != null)
  const move = (m: number, prev: number) => (view!.scale === 'log' ? fmtChange((m / prev - 1) * 100) : fmtLinearChange(m - prev, view!.unit))
  // Runs on the same day (an update right after the first run) need the time to tell them apart.
  const sameDay = new Set(points.map((h) => fmtDay(h.at))).size < points.length
  const when = (at: string) => (sameDay ? fmtDate(at, { time: true }) : fmtDay(at))
  return (
    <Card className="mt-4 p-5 md:p-6 print:break-inside-avoid">
      <section aria-labelledby="probability-history">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <h2 id="probability-history" className="label">
            {byMedian ? 'Median history' : 'Probability history'}
          </h2>
          <Sparkline values={byMedian ? (medians as number[]) : points.map((h) => h.p)} width={160} height={32} />
        </div>
        <ol className="mt-3 flex flex-col gap-1.5">
          {points.map((h, i) => (
            <li key={h.id} className="font-mono text-[12.5px] text-muted tabular-nums">
              <Link
                to={`/forecasts/${h.id}`}
                aria-current={h.id === id ? 'page' : undefined}
                className={cx('rounded-sm hover:underline', h.id === id ? 'font-semibold text-text' : 'text-accent-ink')}
              >
                {byMedian ? (
                  <>
                    {when(h.at)} · median {fmtLevel(medians[i]!, view!.unit)}
                    {i > 0 && ` (${move(medians[i]!, medians[i - 1]!)})`}
                  </>
                ) : (
                  <>
                    {when(h.at)} · {h.outcome} {fmtChance(h.p)}{i > 0 && ` (${fmtPts(pcts[i] - pcts[i - 1])})`}
                    {view && medians[i] != null && ` · median ${fmtLevel(medians[i]!, view.unit)}`}
                  </>
                )}
              </Link>
              {h.id === id && ' · this run'}
            </li>
          ))}
        </ol>
      </section>
    </Card>
  )
}

function DetailSkeleton() {
  return (
    <div aria-busy="true" aria-label="Loading forecast">
      <Skeleton className="h-3.5 w-56" />
      <div className="mt-5 flex gap-2">
        <Skeleton className="h-5 w-16" />
        <Skeleton className="h-5 w-16" />
        <Skeleton className="h-5 w-20" />
      </div>
      <Skeleton className="mt-4 h-9 w-full max-w-3xl md:h-11" />
      <Skeleton className="mt-2 h-9 w-2/3 max-w-xl md:h-11" />
      <Skeleton className="mt-5 h-3.5 w-72" />
      <Skeleton className="mt-8 h-72 w-full rounded-[14px]" />
      <div className="mt-6 grid gap-4 md:grid-cols-3">
        {[0, 1, 2].map((i) => (
          <Skeleton key={i} className="h-56 rounded-[14px]" />
        ))}
      </div>
    </div>
  )
}

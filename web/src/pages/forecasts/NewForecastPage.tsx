import { useEffect, useRef, useState, type ReactNode } from 'react'
import { Link, useLocation, useNavigate, useSearchParams } from 'react-router'
import { useAttachmentQueue, type AttachmentQueue } from '../../components/attachments/useAttachmentQueue'
import { Button } from '../../components/ui/Button'
import { Card } from '../../components/ui/Card'
import { Disclaimer } from '../../components/ui/Disclaimer'
import { buttonClass } from '../../components/ui/Button'
import { IconAlert, IconArrowRight, IconCheck, IconX } from '../../components/ui/icons'
import { Input, Label, Select, Textarea } from '../../components/ui/Input'
import { useToast } from '../../components/toast'
import { api, ATTACHMENT_BODY_LIMIT, bodyBytes } from '../../lib/api'
import { useAuth } from '../../lib/auth'
import { cx } from '../../lib/cx'
import { errorMessage, isQuota } from '../../lib/errors'
import { fmtDate } from '../../lib/format'
import type { DeskReference, ForecastDraft, ForecastShape, PredictionDraft } from '../../lib/types'
import {
  bucketBoundsTs,
  CATEGORIES,
  categoryLabel,
  cleanDraft,
  contextFiles,
  contextSummary,
  daysUntil,
  draftProblem,
  dueLabel,
  EXAMPLE_IDEAS,
  fmtChars,
  fmtDay,
  fmtToday,
  MAX_CONTEXT,
  MAX_IDEA,
  MAX_MATERIALS,
  materialsQuotaMessage,
  MAX_OUTCOMES,
  MAX_QUESTION,
  minDraftDate,
  normalizeDraft,
  qualityChecks,
  reshapeOutcomes,
  ruleText,
  runCost,
  SHAPES,
  shapeLabel,
  unitOf,
  updateDraft,
  vsRefHint,
} from './forecast'
import { LinkedMarketDetails } from './LinkedMarketDetails'
import { ShapeGlyph } from './parts'
import { MANUAL_STEPS, ManualDraftSummary, ManualProgress } from './ManualForecastFlow'
import { MaterialsField, pastedFiles } from './MaterialsField'

type Origin = 'ai' | 'manual' | 'edit' | 'market'

/**
 * Optional router state: an example idea from the dashboard, or a draft: to edit from a failed run, or one the desk
 * drafted in chat (origin 'ai'). `materials`: the run being edited had attached files (they are not carried over).
 */
type EntryState = { idea?: string; draft?: Partial<ForecastDraft>; origin?: 'ai'; materials?: number } | null

const TOO_LARGE = 'These files are too large to send together. Remove one and try again.'

export function NewForecastPage() {
  const [params] = useSearchParams()
  const marketId = params.get('marketId')
  const entry = useLocation().state as EntryState
  const navigate = useNavigate()
  const marketTarget = marketId ?? entry?.draft?.linkedMarket?.marketId
  const previousMarketId = useRef(marketId)
  const { credits, setCredits, refresh } = useAuth()
  const { toast } = useToast()
  const [idea, setIdea] = useState(typeof entry?.idea === 'string' ? entry.idea.slice(0, MAX_IDEA) : '')
  const [draft, setDraft] = useState<ForecastDraft | null>(entry?.draft ? normalizeDraft(entry.draft) : null)
  // Today's level a long-horizon stock draft's ranges are cut around (AI drafts only).
  const [reference, setReference] = useState<DeskReference | null>(null)
  const [origin, setOrigin] = useState<Origin>(entry?.draft?.linkedMarket ? 'market' : entry?.draft && entry.origin !== 'ai' ? 'edit' : 'ai')
  const [drafting, setDrafting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [marketLoading, setMarketLoading] = useState(false)
  const [fresh, setFresh] = useState<PredictionDraft | null>(null)
  const [reviewVersion, setReviewVersion] = useState(0)
  const [running, setRunning] = useState(false)
  const [runError, setRunError] = useState<string | null>(null)
  // The run's materials belong to the page, so a fresh market draft (a remounted review) keeps them. Never stored.
  const materials = useAttachmentQueue({ max: MAX_MATERIALS, disabled: running, per: 'per forecast' })
  const earlierFiles = typeof entry?.materials === 'number' && entry.materials > 0
  // The paid request belongs to the page, so a keyed review cannot reset its pending guard.
  const createPending = useRef<{ active: boolean } | null>(null)
  const live = useRef(true)
  const request = useRef<AbortController | null>(null)
  const generation = useRef(0)

  useEffect(() => {
    live.current = true
    return () => {
      live.current = false
      if (createPending.current) createPending.current.active = false
    }
  }, [])

  async function runForecast(d: ForecastDraft) {
    if (createPending.current || materials.processing) return
    const draft = cleanDraft(d)
    const attachments = materials.inputs()
    if (attachments.length && bodyBytes({ ...draft, attachments }) > ATTACHMENT_BODY_LIMIT) return setRunError(TOO_LARGE)
    const owner = { active: true }
    createPending.current = owner
    setRunning(true)
    setRunError(null)
    try {
      // A text-only run sends exactly the draft, as before materials existed.
      const res = attachments.length ? await api.createForecast(draft, { attachments }) : await api.createForecast(draft)
      if (!owner.active || !live.current) return
      if (res.credits) setCredits(res.credits)
      toast('Forecast is running. The specialists are on it.', 'success')
      navigate(`/forecasts/${res.forecast.id}`)
    } catch (err) {
      if (!owner.active || !live.current) return
      setRunError((isQuota(err) && materialsQuotaMessage(attachments.length, credits)) || errorMessage(err))
      if (isQuota(err)) refresh().catch(() => {})
    } finally {
      createPending.current = null
      if (live.current) setRunning(false)
    }
  }

  async function readMarket(review = false) {
    if (!marketTarget || (review && createPending.current)) return
    request.current?.abort()
    const ctrl = new AbortController()
    request.current = ctrl
    const gen = ++generation.current
    setMarketLoading(true)
    setError(null)
    setFresh(null)
    try {
      const res = await api.predictionDraft(marketTarget, ctrl.signal)
      if (ctrl.signal.aborted || generation.current !== gen) return
      if (review) setFresh(res)
      else {
        setReference(null)
        setDraft(normalizeDraft(res.draft))
        setOrigin('market')
      }
    } catch (err) {
      if (!ctrl.signal.aborted && generation.current === gen) setError(errorMessage(err))
    } finally {
      if (!ctrl.signal.aborted && generation.current === gen) setMarketLoading(false)
    }
  }

  useEffect(() => {
    const previous = previousMarketId.current
    previousMarketId.current = marketId
    const dispose = () => {
      generation.current++
      request.current?.abort()
      if (createPending.current) createPending.current.active = false
    }
    setRunError(null)
    if (!marketId) {
      if (previous && origin === 'market') {
        setDraft(null)
        setFresh(null)
        setError(null)
        setMarketLoading(false)
        setOrigin('ai')
      }
      return dispose
    }
    setDraft(null)
    setFresh(null)
    void readMarket()
    return dispose
  }, [marketId])

  // The two steps share one route, so reset the scroll the AppShell only resets on navigation.
  useEffect(() => {
    document.getElementById('main')?.scrollTo(0, 0)
  }, [draft === null])

  async function draftWithAI() {
    const text = idea.trim()
    if (text.length < 3 || drafting) return
    setDrafting(true)
    setError(null)
    try {
      const res = await api.draftForecast(text)
      if (res.credits) setCredits(res.credits)
      const ref = res.reference
      setReference(ref && Number.isFinite(ref.value) && ref.value > 0 ? ref : null)
      setDraft(normalizeDraft(res.draft))
      setOrigin('ai')
    } catch (err) {
      setError(errorMessage(err))
      if (isQuota(err)) refresh().catch(() => {})
    } finally {
      setDrafting(false)
    }
  }

  if (draft)
    return (
      <>
        {origin === 'market' && (fresh || error || marketLoading) && (
          <div className="mx-auto max-w-6xl px-4 pt-6 md:px-8">
            {marketLoading && <p role="status">Reading fresh market rules…</p>}
            {error && <p role="alert" className="text-down">{error}</p>}
            {fresh && (
              <Card className="p-4">
                <LinkedMarketDetails market={fresh.draft.linkedMarket!} />
                <p className="mt-3 text-sm text-muted">Review these rules before replacing your question and predicate. Your context notes are kept.</p>
                <Button className="mt-3" disabled={running} onClick={() => {
                  if (createPending.current) return
                  setDraft(normalizeDraft({ ...fresh.draft, context: draft.context, category: draft.category }))
                  setFresh(null)
                  setError(null)
                  setRunError(null)
                  setReviewVersion((v) => v + 1)
                }}>Use fresh market draft</Button>
                <Button variant="ghost" className="mt-3" disabled={running} onClick={() => setFresh(null)}>Keep my edits</Button>
              </Card>
            )}
          </div>
        )}
        <Review
          key={`${marketId ?? 'draft'}:${draft.linkedMarket?.asOf ?? 'independent'}:${reviewVersion}`}
          draft={draft}
          setDraft={setDraft}
          origin={origin}
          idea={origin === 'ai' ? idea.trim() : ''}
          onBack={origin === 'market' ? () => navigate('/forecasts?tab=markets') : () => { setDraft(null); setRunError(null) }}
          onFresh={origin === 'market' ? () => void readMarket(true) : undefined}
          marketLoading={marketLoading}
          running={running}
          error={runError}
          onRun={() => void runForecast(draft)}
          onClearError={() => setRunError(null)}
          materials={materials}
          materialsNote={earlierFiles ? "Your earlier files aren't carried over to an edited question. Attach them again." : undefined}
          reference={origin === 'ai' ? reference : null}
        />
      </>
    )

  if (marketId) return (
    <div className="mx-auto max-w-5xl px-4 py-8 md:px-8">
      <h1 className="font-display text-4xl">Review a market forecast.</h1>
      {marketLoading ? <p role="status" className="mt-4">Reading original market rules…</p> : error && <p role="alert" className="mt-4 text-down">{error}</p>}
      <div className="mt-4 flex flex-wrap gap-3">
        {!marketLoading && <Button onClick={() => void readMarket()}>Retry market draft</Button>}
        <Link to="/forecasts?tab=markets" className={buttonClass('secondary')}>Back to markets</Link>
      </div>
    </div>
  )

  return (
    <IdeaStep
      idea={idea}
      setIdea={setIdea}
      drafting={drafting}
      error={error}
      onDraft={draftWithAI}
      onManual={() => {
        setError(null)
        setOrigin('manual')
        setReference(null)
        setDraft(normalizeDraft({ question: idea.trim() }))
      }}
    />
  )
}

/** "Where will BTC close on Wed Oct 7?": five days out, inside the week Polymarket lists daily BTC price ladders for. */
function ladderIdea(now = new Date()) {
  const day = new Date(now.getFullYear(), now.getMonth(), now.getDate() + 5)
  const label = day.toLocaleDateString('en-US', { weekday: 'short', month: 'short', day: 'numeric' }).replace(',', '')
  return { category: 'crypto', shape: 'ordered', idea: `Where will BTC close on ${label}?` } as const
}

function IdeaStep({
  idea,
  setIdea,
  drafting,
  error,
  onDraft,
  onManual,
}: {
  idea: string
  setIdea: (s: string) => void
  drafting: boolean
  error: string | null
  onDraft: () => void
  onManual: () => void
}) {
  const box = useRef<HTMLTextAreaElement>(null)
  const ready = idea.trim().length >= 3

  return (
    <div className="mx-auto max-w-5xl px-4 py-8 md:px-8 md:py-12">
      <header className="max-w-3xl">
        <div className="label">Forecast desk · New forecast</div>
        <h1 className="mt-3 font-display text-[38px] leading-[1.04] font-medium tracking-[-0.02em] text-balance text-text md:text-[56px]">
          Point it at the future.
        </h1>
        <p className="mt-4 max-w-2xl text-[16px] leading-relaxed text-pretty text-muted">
          Sketch a rough idea — a market, an event, a threshold. The desk drafts the question, the outcomes and the
          resolution rules for you to review.
        </p>
      </header>

      <Card className="mt-8 p-2 transition-[border-color,box-shadow] has-[textarea:focus]:border-accent has-[textarea:focus]:ring-[3px] has-[textarea:focus]:ring-accent/20">
        <Textarea
          ref={box}
          aria-label="Your rough idea"
          value={idea}
          maxLength={MAX_IDEA}
          rows={3}
          disabled={drafting}
          placeholder="e.g. Will BTC trade above $150k before July 2027?"
          onChange={(e) => setIdea(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
              e.preventDefault()
              onDraft()
            }
          }}
          className="max-h-72 min-h-28 border-0 bg-transparent px-3 py-3 text-[17px] hover:border-0 focus:ring-0"
        />
        <div className="flex flex-wrap items-center justify-between gap-3 border-t border-border px-2 pt-2.5 pb-0.5 sm:px-3">
          <span className="font-mono text-[11px] text-muted tabular-nums">
            {idea.length}/{MAX_IDEA}
            <span className="hidden sm:inline"> · ⌘/Ctrl + Enter to draft</span>
          </span>
          <div className="flex flex-wrap items-center gap-2">
            <Button variant="ghost" onClick={onManual} disabled={drafting}>
              Create manually
            </Button>
            <Button variant="accent" onClick={onDraft} loading={drafting} disabled={!ready}>
              {drafting ? 'Drafting…' : 'Draft with AI'}
              <span className="font-mono text-[11px] font-normal opacity-70">1 cr</span>
            </Button>
          </div>
        </div>
      </Card>

      <div aria-live="polite" className="mt-3 min-h-6">
        {error ? (
          <p role="alert" className="flex items-start gap-2 text-[13.5px] text-down">
            <IconAlert size={16} className="mt-0.5 shrink-0" />
            {error}
          </p>
        ) : drafting ? (
          <p className="font-mono text-[11.5px] tracking-wide text-muted">
            Drafting the question, the outcomes and the resolution rules…
          </p>
        ) : null}
      </div>

      <section aria-labelledby="examples" className="mt-8">
        <h2 id="examples" className="label">
          Or start from an example
        </h2>
        <ul className="mt-3 grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          {[ladderIdea(), ...EXAMPLE_IDEAS].map((ex) => (
            <li key={ex.idea}>
              <button
                type="button"
                disabled={drafting}
                onClick={() => {
                  setIdea(ex.idea)
                  box.current?.focus()
                }}
                className={cx(
                  'card group flex h-full w-full flex-col p-4 text-left transition-colors hover:border-accent/50 hover:bg-accent-soft/40 disabled:opacity-55',
                  idea.trim() === ex.idea && 'border-accent bg-accent-soft/50',
                )}
              >
                <span className="flex items-center justify-between gap-2">
                  <span className="label text-accent-ink">{categoryLabel(ex.category)}</span>
                  <span className="font-mono text-[10px] tracking-wider text-muted uppercase">{shapeLabel(ex.shape)}</span>
                </span>
                <span className="mt-2.5 font-display text-[16.5px] leading-snug text-text">{ex.idea}</span>
                <span className="mt-auto inline-flex items-center gap-1 pt-4 text-[12.5px] font-medium text-muted group-hover:text-text">
                  Use this idea <IconArrowRight size={13} />
                </span>
              </button>
            </li>
          ))}
        </ul>
      </section>
    </div>
  )
}

const PLACEHOLDERS: Record<Exclude<ForecastShape, 'binary'>, string[]> = {
  ordered: ['e.g. Below $80k', 'e.g. $80k – $100k', 'e.g. $100k – $120k', 'e.g. Above $120k'],
  categorical: ['e.g. Nvidia', 'e.g. Microsoft', 'e.g. Apple', 'e.g. Any other company'],
}

function Review({
  draft,
  setDraft,
  origin,
  idea,
  onBack,
  onFresh,
  marketLoading = false,
  running,
  error,
  onRun,
  onClearError,
  materials,
  materialsNote,
  reference = null,
}: {
  draft: ForecastDraft
  setDraft: (d: ForecastDraft) => void
  origin: Origin
  idea: string
  onBack: () => void
  onFresh?: () => void
  marketLoading?: boolean
  running: boolean
  error: string | null
  onRun: () => void
  onClearError: () => void
  materials: AttachmentQueue
  materialsNote?: string
  reference?: DeskReference | null
}) {
  // Outcomes each list shape had before the user switched away, so switching back restores them.
  const [stash, setStash] = useState<Partial<Record<ForecastShape, string[]>>>({})
  const [step, setStep] = useState(0)
  const heading = useRef<HTMLHeadingElement>(null)
  const manual = origin === 'manual'

  useEffect(() => {
    if (!manual) return
    document.getElementById('main')?.scrollTo(0, 0)
    heading.current?.focus({ preventScroll: true })
  }, [manual, step])

  const minDate = minDraftDate(draft)
  const problem = draftProblem(draft, minDate) ?? (manual && !draft.resolutionCriteria.trim() ? 'Write the rule and source that will settle this forecast.' : null)
  // Ask validates the shared question/outcome contract; the date belongs to the next step.
  const stepProblem = !manual ? problem
    : step === 0 ? null
    : step === 1 ? draftProblem({ ...draft, resolutionDate: minDate }, minDate)
    : problem
  const checks = qualityChecks(draft, minDate)
  const passed = checks.filter((c) => c.ok).length
  const days = draft.resolutionDate ? daysUntil(draft.resolutionDate) : null
  const update = (patch: Partial<ForecastDraft>) => setDraft(updateDraft(draft, patch))
  const changeStep = (next: number) => {
    onClearError()
    setStep(next)
  }

  function changeShape(to: ForecastShape) {
    if (to === draft.shape) return
    const saved = { ...stash, [draft.shape]: draft.outcomes }
    setStash(saved)
    // A price rule only fits a binary question.
    update({ shape: to, outcomes: reshapeOutcomes(draft.shape, to, draft.outcomes, saved[to]), priceRule: to === 'binary' ? draft.priceRule : null })
  }

  const files = materials.files.length
  const preparing = materials.processing

  function run() {
    if (problem || running || preparing || (manual && step !== 3)) return
    onRun()
  }

  return (
    <div className="mx-auto max-w-6xl px-4 py-8 md:px-8 md:py-10">
      <button
        type="button"
        onClick={onBack}
        disabled={running}
        className="inline-flex items-center gap-1.5 rounded-md text-[13px] font-medium text-muted transition-colors hover:text-text"
      >
        <IconArrowRight size={14} className="rotate-180" /> {origin === 'market' ? 'Back to markets' : origin === 'edit' ? 'Start from a new idea' : 'Back to your idea'}
      </button>

      <header className="mt-5 mb-8 max-w-3xl">
        <div className="label">
          Forecast desk · {origin === 'ai' ? 'Drafted by the desk' : origin === 'edit' ? 'Edit and run again' : origin === 'market' ? 'From a Polymarket market' : 'Manual draft'}
        </div>
        <h1 ref={heading} tabIndex={manual ? -1 : undefined} className="mt-3 font-display text-[34px] leading-[1.06] font-medium tracking-[-0.02em] text-text outline-none md:text-[44px]">
          {manual ? MANUAL_STEPS[step].title : 'Review the question.'}
        </h1>
        <p className="mt-3 text-[15px] leading-relaxed text-pretty text-muted">
          {manual ? MANUAL_STEPS[step].description : origin === 'ai'
            ? 'Tighten anything vague: a good forecast question has one clear answer on one clear date.'
            : 'Write the question, the outcomes and how it resolves. The quality checklist keeps it sharp.'}
        </p>
        {idea && (
          <figure className="mt-5 inline-flex max-w-full flex-col rounded-xl rounded-tl-sm border border-border bg-surface-2 px-4 py-2.5">
            <figcaption className="label">Your idea</figcaption>
            <blockquote className="mt-0.5 text-[14px] text-text">“{idea}”</blockquote>
          </figure>
        )}
      </header>

      {manual && <ManualProgress step={step} disabled={running} onChange={changeStep} />}

      {origin === 'market' && (
        <div className="mb-5">
          {draft.linkedMarket ? <LinkedMarketDetails market={draft.linkedMarket} /> : (
            <p role="status" className="text-sm text-muted">
              Market link removed after a predicate edit. This draft is now independent; review fresh rules to reconfirm the contract.
            </p>
          )}
          <Button variant="secondary" className="mt-3" disabled={running || marketLoading} onClick={onFresh}>Review fresh market rules</Button>
        </div>
      )}

      <div className="grid items-start gap-6 lg:grid-cols-[minmax(0,1fr)_20rem]">
        <Card className="flex min-w-0 flex-col gap-7 p-5 md:p-6">
          {(!manual || step === 0) && (
            <fieldset disabled={running}>
              <legend className="label mb-2.5">Shape</legend>
              <div className="grid gap-2.5 sm:grid-cols-3">
                {SHAPES.map((s) => {
                  const active = draft.shape === s.value
                  return (
                    <label
                      key={s.value}
                      className={cx(
                        'relative flex cursor-pointer items-center gap-4 rounded-xl border p-3.5 transition-colors sm:flex-col sm:items-stretch sm:gap-3',
                        'has-[input:focus-visible]:outline-2 has-[input:focus-visible]:outline-offset-2 has-[input:focus-visible]:outline-accent-ink',
                        active ? 'border-accent bg-accent-soft/50' : 'border-border bg-surface hover:bg-surface-2',
                      )}
                    >
                      <input
                        type="radio"
                        name="shape"
                        value={s.value}
                        checked={active}
                        onChange={() => changeShape(s.value)}
                        className="sr-only"
                      />
                      <span className="w-20 shrink-0 sm:w-full">
                        <ShapeGlyph shape={s.value} active={active} />
                      </span>
                      <span className="min-w-0">
                        <span className="block text-sm font-semibold text-text">{s.label}</span>
                        <span className="mt-0.5 block text-[12.5px] leading-snug text-muted">{s.blurb}</span>
                      </span>
                    </label>
                  )
                })}
              </div>
            </fieldset>
          )}

          {manual && step <= 1 && (
            <div className="rounded-xl border border-border bg-surface-2 p-4">
              <div className="label">{shapeLabel(draft.shape)} example</div>
              <p className="mt-2 font-display text-lg leading-snug text-text">{EXAMPLE_IDEAS.find((example) => example.shape === draft.shape)?.idea}</p>
              <p className="mt-2 text-[12.5px] leading-relaxed text-muted">
                {draft.shape === 'binary' ? 'Use a clear condition that will resolve Yes or No.' : draft.shape === 'ordered' ? 'Use ranges from low to high, with no gaps or overlap.' : 'Name each possible winner and include “Other” when needed.'}
              </p>
            </div>
          )}

          {(!manual || step === 1) && (
            <>
              <div>
                <div className="flex items-baseline justify-between">
                  <Label htmlFor="fc-question">Question</Label>
                  <span className={cx('font-mono text-[11px] tabular-nums', draft.question.length >= MAX_QUESTION ? 'text-down' : 'text-muted')}>
                    {draft.question.length}/{MAX_QUESTION}
                  </span>
                </div>
                <Textarea
                  id="fc-question"
                  value={draft.question}
                  rows={2}
                  disabled={running}
                  placeholder="Will BTC close above $100,000 on Dec 31, 2026?"
                  onChange={(e) => update({ question: e.target.value })}
                  className="font-display text-[19px] leading-snug"
                />
                {draft.priceRule && (
                  <div className="mt-2.5 inline-flex max-w-full items-center gap-1 rounded-2xl border border-accent/45 bg-accent-soft/60 py-0.5 pr-1 pl-3 text-[12.5px] text-text">
                    <IconCheck size={12} strokeWidth={2.4} className="shrink-0 text-accent-ink" />
                    <span className="min-w-0">Auto-resolves from Binance · {ruleText(draft.priceRule, draft.resolutionDate)}</span>
                    <button
                      type="button"
                      aria-label="Remove automatic resolution"
                      title="Remove automatic resolution"
                      disabled={running}
                      onClick={() => update({ priceRule: null })}
                      className="grid size-6 shrink-0 place-items-center rounded-full text-muted transition-colors hover:bg-surface-2 hover:text-text"
                    >
                      <IconX size={12} />
                    </button>
                  </div>
                )}
              </div>

              <OutcomesEditor shape={draft.shape} outcomes={draft.outcomes} disabled={running} reference={reference} onChange={(outcomes) => update({ outcomes })} />
            </>
          )}

          {(!manual || step === 2) && (
            <>
              <div>
                <Label htmlFor="fc-criteria">Resolution criteria</Label>
                <Textarea
                  id="fc-criteria"
                  value={draft.resolutionCriteria}
                  rows={3}
                  disabled={running}
                  placeholder="e.g. Resolves Yes if the Binance BTCUSDT daily close on Dec 31, 2026 is above $100,000."
                  onChange={(e) => update({ resolutionCriteria: e.target.value })}
                />
                <p className="mt-1.5 text-[12.5px] text-muted">Name the source and the exact rule that settles it. {Array.from(draft.resolutionCriteria).length.toLocaleString('en-US')}/{draft.linkedMarket ? '8,000' : '2,000'} characters.</p>
              </div>

              <div className="grid gap-5 sm:grid-cols-2">
                <div>
                  <Label htmlFor="fc-date">{draft.linkedMarket ? 'Tracking date' : 'Resolution date'}</Label>
                  <Input
                    id="fc-date"
                    type="date"
                    min={minDate}
                    value={draft.resolutionDate}
                    disabled={running}
                    aria-invalid={!!draft.resolutionDate && draft.resolutionDate < minDate}
                    onChange={(e) => update({ resolutionDate: e.target.value })}
                    className="font-mono tabular-nums"
                  />
                  <p className="mt-1.5 font-mono text-[11px] text-muted">
                    {draft.linkedMarket ? 'Provider end day in UTC · resolved manually in Foretrade' : draft.resolutionDate ? (draft.resolutionDate < minDate ? 'Must be after today' : `Resolves ${dueLabel(days)}`) : 'Tomorrow or later'}
                  </p>
                </div>
                <div>
                  <Label htmlFor="fc-category">Category</Label>
                  <Select
                    id="fc-category"
                    value={draft.category}
                    disabled={running}
                    onChange={(e) => update({ category: e.target.value as ForecastDraft['category'] })}
                  >
                    {CATEGORIES.map((c) => (
                      <option key={c.value} value={c.value}>
                        {c.label}
                      </option>
                    ))}
                  </Select>
                </div>
              </div>

              <DeskContext value={draft.context ?? ''} disabled={running} onChange={(context) => update({ context })} onFiles={materials.attach} />
              <MaterialsField queue={materials} disabled={running} note={materialsNote} />
            </>
          )}

          {manual && step === 3 && <ManualDraftSummary draft={draft} materials={materials.files.map((f) => f.name)} />}
        </Card>

        <aside className="flex flex-col gap-4 lg:sticky lg:top-6">
          {(!manual || step > 0) && (
            <Card className="p-5">
              <div className="flex items-baseline justify-between">
                <h2 className="label">Question quality</h2>
                <span className="font-mono text-[12px] font-semibold text-text tabular-nums">
                  {passed}/{checks.length}
                </span>
              </div>
              <div aria-hidden="true" className="mt-2.5 grid grid-cols-4 gap-1">
                {checks.map((_, i) => (
                  <span key={i} className={cx('h-1 rounded-full transition-colors', i < passed ? 'bg-accent' : 'bg-surface-2')} />
                ))}
              </div>
              <ul className="mt-4 flex flex-col gap-3.5">
                {checks.map((c) => (
                  <li key={c.label} className="flex gap-3">
                    <span
                      className={cx(
                        'mt-px grid size-[18px] shrink-0 place-items-center rounded-full transition-colors',
                        c.ok ? 'bg-accent text-[#17120a]' : 'border border-border text-transparent',
                      )}
                    >
                      <IconCheck size={11} strokeWidth={2.6} />
                    </span>
                    <span className="min-w-0">
                      <span className={cx('block text-[13.5px] leading-snug', c.ok ? 'text-text' : 'text-muted')}>
                        {c.label}
                        <span className="sr-only">{c.ok ? ' — done' : ' — missing'}</span>
                      </span>
                      <span className="mt-0.5 block text-[12px] leading-snug text-muted">{c.hint}</span>
                    </span>
                  </li>
                ))}
              </ul>
            </Card>
          )}

          <Card className="p-5">
            {manual && step < 3 ? (
              <div>
                <h2 className="label">Step {step + 1} of {MANUAL_STEPS.length}</h2>
                <p className="mt-2 text-sm leading-relaxed text-muted">Your draft stays editable. Review all the details before spending credits.</p>
              </div>
            ) : (
              <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-[13px]">
                <dt className="label self-center">Shape</dt>
                <dd className="text-right text-text">{shapeLabel(draft.shape)}</dd>
                <dt className="label self-center">Outcomes</dt>
                <dd className="text-right text-text tabular-nums">{draft.outcomes.length}</dd>
                <dt className="label self-center">{draft.linkedMarket ? 'Tracking date' : 'Resolves'}</dt>
                <dd className="text-right text-text">{draft.resolutionDate ? fmtDate(draft.resolutionDate) : '—'}</dd>
                {contextSummary(draft.context ?? '') && (
                  <>
                    <dt className="label self-center">Context</dt>
                    <dd className="text-right text-text">{contextSummary(draft.context ?? '')}</dd>
                  </>
                )}
                {files > 0 && (
                  <>
                    <dt className="label self-center">Materials</dt>
                    <dd className="text-right text-text tabular-nums">{files}</dd>
                  </>
                )}
              </dl>
            )}
            {manual && step < 3 ? (
              <Button variant="primary" size="lg" className="mt-5 w-full" disabled={!!stepProblem} onClick={() => { if (!stepProblem) changeStep(step + 1) }}>
                Continue to {MANUAL_STEPS[step + 1].label} <IconArrowRight size={16} />
              </Button>
            ) : (
              <Button variant="primary" size="lg" className="mt-5 w-full" onClick={run} loading={running} disabled={!!problem || preparing}>
                {running ? 'Starting the run…' : 'Run forecast'}
                <span className="font-mono text-[11.5px] font-normal opacity-70">· {runCost(files)} cr</span>
              </Button>
            )}
            {manual && step > 0 && (
              <Button variant="ghost" className="mt-2 w-full" disabled={running} onClick={() => changeStep(step - 1)}>
                <IconArrowRight size={14} className="rotate-180" /> Back to {MANUAL_STEPS[step - 1].label}
              </Button>
            )}
            <div aria-live="polite">
              {error ? (
                <p role="alert" className="mt-3 flex items-start gap-2 text-[13px] leading-snug text-down">
                  <IconAlert size={15} className="mt-px shrink-0" />
                  {error}
                </p>
              ) : stepProblem ? (
                <p className="mt-3 text-[12.5px] leading-snug text-muted">{stepProblem}</p>
              ) : preparing && (!manual || step === 3) ? (
                <p role="status" className="mt-3 text-[12.5px] leading-snug text-muted">Preparing files…</p>
              ) : !manual || step === 3 ? (
                <p className="mt-3 text-[12.5px] leading-snug text-muted">
                  Deep web research, three specialists and an aggregator. Usually four to eight minutes.
                </p>
              ) : null}
            </div>
            {(!manual || step === 3) && <Disclaimer className="mt-4" />}
          </Card>
        </aside>
      </div>
    </div>
  )
}

/**
 * Optional notes for the desk (S0): collapsed until asked for, open when the draft already carries some. Files dropped
 * or pasted on the box go to the run's materials (`onFiles`); older drafts' `File: <name>` blocks still show their
 * removable S0 chips.
 */
function DeskContext({ value, disabled, onChange, onFiles }: { value: string; disabled: boolean; onChange: (v: string) => void; onFiles: (files: File[]) => void }) {
  const [open, setOpen] = useState(value !== '')
  const files = contextFiles(value)

  return (
    <div>
      <button
        type="button"
        aria-expanded={open}
        aria-controls="fc-context-panel"
        onClick={() => setOpen(!open)}
        className="inline-flex items-center gap-1.5 rounded-md text-[13.5px] font-medium text-text transition-colors hover:text-accent-ink"
      >
        <IconArrowRight size={13} className={cx('transition-transform', open && 'rotate-90')} />
        Add context for the desk (optional)
      </button>
      {open && (
        <div id="fc-context-panel" className="mt-2.5">
          <div className="flex items-baseline justify-between">
            <Label htmlFor="fc-context">Context</Label>
            <span className={cx('font-mono text-[11px] tabular-nums', value.length >= MAX_CONTEXT ? 'text-down' : 'text-muted')}>
              {value.length.toLocaleString('en-US')}/{MAX_CONTEXT.toLocaleString('en-US')}
            </span>
          </div>
          <Textarea
            id="fc-context"
            value={value}
            maxLength={MAX_CONTEXT}
            rows={4}
            disabled={disabled}
            placeholder="Your data, a thesis, constraints… The desk treats it as a claim to check, not as fact."
            className="max-h-72" // a long note scrolls inside the box instead of growing the page
            onChange={(e) => onChange(e.target.value)}
            onDragOver={(e) => {
              if (!disabled && e.dataTransfer.types.includes('Files')) e.preventDefault()
            }}
            onDrop={(e) => {
              if (disabled || !e.dataTransfer.files.length) return
              e.preventDefault()
              onFiles(Array.from(e.dataTransfer.files))
            }}
            onPaste={(e) => {
              if (disabled) return
              const picked = pastedFiles(e.clipboardData)
              if (!picked.length) return
              e.preventDefault()
              onFiles(picked)
            }}
          />
          {files.length > 0 && (
            <ul aria-label="Files in your notes" className="mt-2 flex flex-wrap gap-1.5">
              {files.map((f) => (
                <li
                  key={`${f.start}-${f.name}`}
                  className="inline-flex max-w-full items-center gap-1 rounded-full border border-border bg-surface-2 py-0.5 pr-1 pl-2.5 font-mono text-[11.5px] text-text"
                >
                  <span className="min-w-0 truncate">{`S0 · ${f.name} · ${fmtChars(f.chars)}`}</span>
                  <button
                    type="button"
                    aria-label={`Remove ${f.name}`}
                    title={`Remove ${f.name}`}
                    disabled={disabled}
                    onClick={() => onChange((value.slice(0, f.start) + value.slice(f.end)).replace(/^\n+/, ''))}
                    className="grid size-5 shrink-0 place-items-center rounded-full text-muted transition-colors hover:bg-surface hover:text-text"
                  >
                    <IconX size={11} />
                  </button>
                </li>
              ))}
            </ul>
          )}
          <p className="mt-1.5 text-[12.5px] text-muted">
            The specialists see it as source S0, alongside the web research. Files go in Materials below.
          </p>
        </div>
      )}
    </div>
  )
}

function OutcomesEditor({
  shape,
  outcomes,
  disabled,
  reference = null,
  onChange,
}: {
  shape: ForecastShape
  outcomes: string[]
  disabled: boolean
  reference?: DeskReference | null
  onChange: (outcomes: string[]) => void
}) {
  if (shape === 'binary')
    return (
      <div>
        <div className="label mb-1.5">Outcomes</div>
        <ol className="grid grid-cols-2 gap-2.5">
          {['Yes', 'No'].map((o, i) => (
            <li key={o} className="flex h-10 items-center gap-3 rounded-lg border border-border bg-surface-2 px-3 text-sm text-text">
              <span className="font-mono text-[11px] text-muted">{i + 1}</span>
              {o}
            </li>
          ))}
        </ol>
        <p className="mt-1.5 text-[12.5px] text-muted">Binary questions always resolve Yes or No. Switch to Categorical for named options.</p>
      </div>
    )

  const seen = new Map<string, number>()
  for (const o of outcomes) {
    const k = o.trim().toLowerCase()
    if (k) seen.set(k, (seen.get(k) ?? 0) + 1)
  }
  const isDup = (o: string) => (seen.get(o.trim().toLowerCase()) ?? 0) > 1
  // Ranges cut at multiples of today's level show how far each is from it, while they still parse.
  const bounds = reference && shape === 'ordered' ? bucketBoundsTs(outcomes) : null
  const vs = (x: number) => (Number.isFinite(x) && reference ? (x / reference.value - 1) * 100 : null)

  // After a move, keep focus on the moved row's arrow so repeated presses keep moving the same outcome.
  const focusLater = (id: string, fallback?: string) =>
    requestAnimationFrame(() => {
      const el = document.getElementById(id) as HTMLButtonElement | HTMLInputElement | null
      if (el && !el.disabled) el.focus()
      else if (fallback) document.getElementById(fallback)?.focus()
    })

  function move(i: number, dir: -1 | 1) {
    const next = [...outcomes]
    ;[next[i], next[i + dir]] = [next[i + dir], next[i]]
    onChange(next)
    const name = dir < 0 ? 'up' : 'down'
    focusLater(`outcome-${i + dir}-${name}`, `outcome-${i + dir}-${dir < 0 ? 'down' : 'up'}`)
  }

  return (
    <fieldset disabled={disabled} aria-labelledby="outcomes-label">
      <div className="mb-1.5 flex items-baseline justify-between">
        <span id="outcomes-label" className="label">
          Outcomes
        </span>
        <span className="font-mono text-[11px] text-muted tabular-nums">
          {outcomes.length}/{MAX_OUTCOMES}
        </span>
      </div>
      {bounds && reference && (
        <p className="mb-2 text-[12.5px] leading-snug text-muted">
          Ranges are multiples of today's {fmtToday(reference.value, unitOf(outcomes))} ({sourceName(reference)}, {fmtDay(reference.asOf)}).
        </p>
      )}
      <ol className="flex flex-col gap-2">
        {outcomes.map((o, i) => (
          <li key={i} className="flex flex-wrap items-center gap-x-1.5">
            <span className="w-5 shrink-0 font-mono text-[11px] text-muted tabular-nums">{i + 1}</span>
            <Input
              id={`outcome-${i}`}
              value={o}
              maxLength={120}
              aria-label={`Outcome ${i + 1}`}
              aria-invalid={isDup(o)}
              placeholder={PLACEHOLDERS[shape][i] ?? (shape === 'ordered' ? 'Next range' : 'Another outcome')}
              onChange={(e) => onChange(outcomes.map((x, j) => (j === i ? e.target.value : x)))}
              className="min-w-0 flex-1"
            />
            <IconButton id={`outcome-${i}-up`} label={`Move outcome ${i + 1} up`} disabled={i === 0} onClick={() => move(i, -1)}>
              <IconArrowRight size={14} className="-rotate-90" />
            </IconButton>
            <IconButton
              id={`outcome-${i}-down`}
              label={`Move outcome ${i + 1} down`}
              disabled={i === outcomes.length - 1}
              onClick={() => move(i, 1)}
            >
              <IconArrowRight size={14} className="rotate-90" />
            </IconButton>
            <IconButton
              label={`Remove outcome ${i + 1}`}
              disabled={outcomes.length <= 2}
              onClick={() => {
                onChange(outcomes.filter((_, j) => j !== i))
                focusLater(`outcome-${Math.min(i, outcomes.length - 2)}`)
              }}
            >
              <IconX size={14} />
            </IconButton>
            {bounds && (
              <span className="basis-full pt-1 pl-[26px]">
                <span className="inline-flex rounded-md bg-surface-2 px-1.5 py-px font-mono text-[10.5px] text-muted tabular-nums">
                  {vsRefHint(vs(bounds[i][0]), vs(bounds[i][1]))} vs today
                </span>
              </span>
            )}
          </li>
        ))}
      </ol>
      <div className="mt-2.5 flex flex-wrap items-center justify-between gap-2">
        <Button
          variant="secondary"
          size="sm"
          disabled={outcomes.length >= MAX_OUTCOMES}
          onClick={() => {
            onChange([...outcomes, ''])
            focusLater(`outcome-${outcomes.length}`)
          }}
        >
          + Add outcome
        </Button>
        <p className="text-[12.5px] text-muted">
          {shape === 'ordered'
            ? 'Order low → high. Ranges should not overlap and should cover every result.'
            : 'List the serious contenders; add a catch-all like “Other” to cover the rest.'}
        </p>
      </div>
    </fieldset>
  )
}

/** "Yahoo" from "Yahoo Finance regular-market price (NYSE: TSM)": the reference's provider, for a short note. */
const sourceName = (r: DeskReference) => r.source.trim().split(/[\s(,]/)[0] || r.label

function IconButton({
  id,
  label,
  disabled,
  onClick,
  children,
}: {
  id?: string
  label: string
  disabled?: boolean
  onClick: () => void
  children: ReactNode
}) {
  return (
    <button
      id={id}
      type="button"
      aria-label={label}
      title={label}
      disabled={disabled}
      onClick={onClick}
      className="grid size-8 shrink-0 place-items-center rounded-lg text-muted transition-colors hover:bg-surface-2 hover:text-text disabled:pointer-events-none disabled:opacity-30"
    >
      {children}
    </button>
  )
}

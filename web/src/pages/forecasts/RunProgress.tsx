// The running forecast card: the live pipeline trace (API contract "Live run progress, full panel (round 5)").
import { useEffect, useState, type ReactNode } from 'react'
import { Card } from '../../components/ui/Card'
import { IconCheck } from '../../components/ui/icons'
import { cx } from '../../lib/cx'
import { fmtProb } from '../../lib/format'
import type { Forecast, ForecastProgress, MaterialsStep, ResearchStepLog } from '../../lib/types'
import { fmtElapsed, SPECIALISTS, stepText } from './forecast'
import { Monogram, PulseDot } from './parts'

function useElapsed(since: string) {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(t)
  }, [])
  const start = Date.parse(since)
  return Number.isFinite(start) ? Math.max(0, Math.floor((now - start) / 1000)) : 0
}

/** A row's track: idle, busy (shimmer), or a solid fill at 0..1. */
type Bar = 'idle' | 'busy' | number

type Row = {
  key: string
  glyph: string
  name: string
  focus: string
  bar: Bar
  status: ReactNode
  chips?: string[]
  chipsLabel?: string
  steps?: ResearchStepLog[] // deep research: the stage's own phases, under the row
  muted?: boolean
}

const RESEARCH = { key: 'research', glyph: 'R', name: 'Researching the web', focus: 'Searches the news and reads the top articles' }
const DEEP = { name: 'Deep research', focus: 'Plans research angles, searches, screens, reads and extracts findings' }
const AGGREGATOR = { key: 'aggregator', glyph: 'A', name: 'Aggregator', focus: 'Weighs the three views, flags conflicts, estimates odds' }
const MARKET = { key: 'market', glyph: 'P', name: 'Polymarket', focus: 'Looks for a prediction market on the same question' }
const MATERIALS = { key: 'materials', glyph: 'D', name: 'Your materials', focus: 'Reads each file once and notes what bears on the question' }

const done = (text: string) => (
  <span className="inline-flex items-center gap-1">
    <IconCheck size={13} className="shrink-0 text-accent-ink" />
    {text}
  </span>
)

function researchDone(r: ForecastProgress['research']) {
  if (r.mode === 'wire') return `Stored news wire · ${r.found} headline${r.found === 1 ? '' : 's'}`
  if (r.mode === 'carried') return "Previous run's sources"
  if (r.mode === 'none' || r.found === 0) return 'No sources found'
  return `${r.found} source${r.found === 1 ? '' : 's'}${r.read ? ` · ${r.read} read` : ''}`
}

const plural = (n: number, noun: string) => `${n} ${noun}${n === 1 ? '' : 's'}`

/** Round 9: the digest stage's row, before research; it reads every new file at once, so it counts files done. */
function materialsRow(m: MaterialsStep): Row {
  const n = (x: unknown) => (typeof x === 'number' && Number.isFinite(x) && x > 0 ? Math.floor(x) : 0)
  const total = n(m.total)
  const reused = Math.min(n(m.reused), total)
  const read = Math.min(n(m.read), total)
  const failed = Math.min(n(m.failed), total - read)
  const onlyReused = total > 0 && reused === total
  const reusedText = `Using ${plural(reused, 'material')} from the previous run`
  if (m.status !== 'done')
    return {
      ...MATERIALS,
      bar: onlyReused ? 1 : m.status === 'running' ? 'busy' : 'idle',
      status: onlyReused ? reusedText : `Reading your materials… (${read + failed} of ${total})`,
    }
  return {
    ...MATERIALS,
    bar: total ? read / total : 1,
    status: onlyReused
      ? done(reusedText)
      : failed === 0
        ? done(`Read ${plural(read, 'material')}`)
        : read === 0
          ? (
              <span className="inline-flex items-center gap-1">
                <span aria-hidden="true" className="font-semibold text-down">!</span>
                {`Couldn't read ${total === 1 ? 'your file' : `any of ${total} files`}`}
              </span>
            )
          : done(`Read ${read} of ${total} · ${failed} couldn't be read`),
  }
}

/** A deep stage's summary once done: "34 findings · 22 sources · 12 read"; the fallbacks keep their own text. */
function deepDone(r: ForecastProgress['research']) {
  if (r.mode !== 'live' || r.found === 0) return researchDone(r)
  return [r.findings ? plural(r.findings, 'finding') : '', plural(r.found, 'source'), r.read ? `${r.read} read` : ''].filter(Boolean).join(' · ')
}

/**
 * A deep trace's steps, one per phase and round: the API appends a step and then updates it in place, so a repeated
 * phase/round (should one ever be appended twice) keeps its first slot and its latest state.
 */
function stepsOf(r: ForecastProgress['research']): ResearchStepLog[] {
  const out: ResearchStepLog[] = []
  for (const st of Array.isArray(r.steps) ? r.steps : []) {
    if (!st || typeof st.phase !== 'string') continue
    const i = out.findIndex((x) => x.phase === st.phase && x.round === st.round)
    if (i >= 0) out[i] = st
    else out.push(st)
  }
  return out
}

/** The research row: today's one-shot copy, or the deep stage's phases once the trace says it is deep. */
function researchRow(r: ForecastProgress['research']): Row {
  const steps = stepsOf(r)
  // A failed plan leaves its step in the trace and hands over to the quick stage (depth "quick"): that run reads as quick.
  if (r.depth === 'quick' || (!steps.length && r.depth !== 'deep'))
    return {
      ...RESEARCH,
      chips: r.queries.length ? r.queries : undefined,
      bar: r.status === 'done' ? 1 : r.status === 'running' ? 'busy' : 'idle',
      status:
        r.status === 'done'
          ? done(researchDone(r))
          : r.status === 'running'
            ? r.queries.length
              ? 'Searching…'
              : 'Writing search queries…'
            : 'Waiting',
    }
  const angles = (Array.isArray(r.angles) ? r.angles : []).filter((a) => typeof a === 'string' && a.trim())
  const current = steps.findLast((x) => x.status === 'running') ?? steps[steps.length - 1]
  return {
    ...RESEARCH,
    ...DEEP,
    chips: angles.length ? angles : r.queries.length ? r.queries : undefined,
    chipsLabel: angles.length ? 'Research angles' : undefined,
    steps,
    bar: r.status === 'done' ? 1 : r.status === 'running' ? 'busy' : 'idle',
    status: r.status === 'done' ? done(deepDone(r)) : r.status === 'pending' ? 'Waiting' : current ? stepText(current) : 'Planning…',
  }
}

/**
 * The rows a trace shows, in pipeline order; the Polymarket row only when the run looks a market up (an ordered run:
 * Polymarket's strike ladder for the day).
 */
function traced(p: ForecastProgress, ordered: boolean): Row[] {
  const rows: Row[] = [...(p.materials ? [materialsRow(p.materials)] : []), researchRow(p.research)]
  for (const meta of SPECIALISTS) {
    const s = p.specialists.find((x) => x.role === meta.role)
    const status = s?.status ?? 'pending'
    rows.push({
      key: meta.role,
      glyph: meta.glyph,
      name: meta.name,
      focus: meta.focus,
      muted: status === 'failed',
      bar: status === 'done' && s?.top ? s.top.p : status === 'running' ? 'busy' : 'idle',
      status:
        status === 'done' && s?.top
          ? `${s.top.outcome} ${fmtProb(s.top.p)}`
          : status === 'running'
            ? 'Thinking…'
            : status === 'failed'
              ? 'Unavailable'
              : status === 'done'
                ? 'Done'
                : 'Waiting',
    })
  }
  const a = p.aggregator.status
  const views = p.specialists.filter((s) => s.status !== 'failed').length
  rows.push({
    ...AGGREGATOR,
    focus: views < 3 ? `Weighs the ${['', 'one', 'two'][views] || views} view${views === 1 ? '' : 's'}, flags conflicts, estimates odds` : AGGREGATOR.focus,
    muted: a === 'pending',
    bar: a === 'done' ? 1 : a === 'running' ? 'busy' : 'idle',
    status: a === 'done' ? done('Done') : a === 'running' ? 'Weighing the views…' : 'Up next',
  })
  const m = p.market
  if (m.status !== 'skipped')
    rows.push({
      ...MARKET,
      ...(ordered && { focus: 'Looks for a Polymarket strike ladder ending the same day' }),
      bar: m.status === 'pending' ? 'busy' : m.matched ? 1 : 'idle',
      status: ordered
        ? m.status === 'pending'
          ? 'Looking for a strike ladder'
          : m.matched
            ? done('Strike ladder found')
            : 'No strike ladder'
        : m.status === 'pending'
          ? 'Searching…'
          : m.matched
            ? done('Market found')
            : 'No match',
    })
  return rows
}

// Runs stored before round 5 have no trace: every stage animates for the whole run.
const FALLBACK: Row[] = [
  { ...RESEARCH, bar: 'busy', status: 'Searching…' },
  ...SPECIALISTS.map((s) => ({ key: s.role, glyph: s.glyph, name: s.name, focus: s.focus, bar: 'busy' as const, status: 'Thinking…' })),
  { ...AGGREGATOR, bar: 'idle', status: 'Up next', muted: true },
]

/** The running card: elapsed timer and one row per pipeline stage, from `f.progress` when the run writes one. */
export function Working({ f, reconnecting }: { f: Forecast; reconnecting: boolean }) {
  const elapsed = useElapsed(f.createdAt)
  const rows = f.progress ? traced(f.progress, f.shape === 'ordered') : FALLBACK
  const deep = !!rows.find((r) => r.key === 'research')?.steps
  return (
    <Card className="overflow-hidden">
      <div className="flex flex-wrap items-end justify-between gap-4 border-b border-border px-5 pt-5 pb-4 md:px-6">
        <div className="min-w-0">
          <div className="label flex items-center gap-2 text-accent-ink">
            <PulseDot /> Live · running
          </div>
          <h2 className="mt-1.5 font-display text-[24px] leading-tight font-medium text-text">Specialists at work</h2>
          <p className="mt-1 max-w-xl text-[13.5px] leading-relaxed text-muted">
            {deep ? 'Deep web research' : 'Live web research'}, then three specialist views, then an aggregator estimates the
            final odds. Usually {deep ? 'four to eight' : 'one to three'} minutes; you can leave this page and come back.
          </p>
        </div>
        <div className="text-right">
          <div className="label">Elapsed</div>
          <div className="mt-1 font-mono text-[26px] leading-none text-text tabular-nums">{fmtElapsed(elapsed)}</div>
        </div>
      </div>
      <ul className="divide-y divide-border">
        {rows.map((s, i) => (
          <li
            key={s.key}
            data-stage={s.key}
            className={cx(
              'grid gap-3 px-5 py-4 sm:grid-cols-[minmax(0,17rem)_1fr_9.5rem] sm:items-center sm:gap-6 md:px-6',
              s.muted && 'opacity-65',
            )}
          >
            <div className="flex min-w-0 items-center gap-3">
              <Monogram glyph={s.glyph} muted={s.muted} />
              <div className="min-w-0">
                <div className="text-[14px] font-medium text-text">{s.name}</div>
                <div className="text-[12.5px] leading-snug text-muted">{s.focus}</div>
              </div>
            </div>
            {s.chips ? (
              <ul aria-label={s.chipsLabel ?? 'Search queries'} className="flex min-w-0 flex-wrap gap-1.5">
                {s.chips.map((q, j) => (
                  <li key={`${j}-${q}`} className="max-w-full rounded-full border border-border bg-surface-2 px-2 py-0.5 font-mono text-[11px] break-words text-text">
                    {q}
                  </li>
                ))}
              </ul>
            ) : (
              <Track bar={s.bar} delay={i * 0.4} />
            )}
            <span className="font-mono text-[11px] leading-snug tracking-wide text-muted sm:text-right">{s.status}</span>
            {s.steps && s.steps.length > 0 && <Steps steps={s.steps} />}
          </li>
        ))}
      </ul>
      <p role="status" className="min-h-9 px-5 pb-3 font-mono text-[11px] text-muted md:px-6">
        {reconnecting ? 'Connection hiccup · retrying…' : ''}
      </p>
    </Card>
  )
}

const STEP_GLYPH: Record<ResearchStepLog['status'], ReactNode> = {
  done: <IconCheck size={12} strokeWidth={2.4} className="text-accent-ink" />,
  running: <PulseDot />,
  skipped: <span className="text-muted">–</span>,
  failed: <span className="font-semibold text-down">!</span>,
}

/** The deep stage's phases as they ran, under the research row (spans the row on wide screens, indented past the monogram). */
function Steps({ steps }: { steps: ResearchStepLog[] }) {
  return (
    <ol aria-label="Research steps" className="flex min-w-0 flex-col gap-1 sm:col-span-3 sm:pl-12">
      {steps.map((st, i) => (
        <li
          key={`${st.phase}-${st.round}-${i}`}
          data-status={st.status}
          className={cx('flex min-w-0 items-center gap-2 font-mono text-[11px] leading-snug break-words', st.status === 'running' ? 'text-text' : 'text-muted')}
        >
          <span aria-hidden="true" className="grid w-3 shrink-0 place-items-center">
            {STEP_GLYPH[st.status] ?? null}
          </span>
          <span className="min-w-0">{stepText(st)}</span>
        </li>
      ))}
    </ol>
  )
}

function Track({ bar, delay }: { bar: Bar; delay: number }) {
  return (
    <div aria-hidden="true" className="relative h-1.5 overflow-hidden rounded-full bg-surface-2">
      {bar === 'busy' && (
        <span
          className="absolute inset-0 animate-shimmer bg-linear-to-r from-transparent via-accent to-transparent"
          style={{ animationDelay: `${delay}s` }}
        />
      )}
      {typeof bar === 'number' && (
        <span className="block h-full origin-left animate-bar rounded-full bg-accent" style={{ width: `${Math.round(bar * 100)}%` }} />
      )}
    </div>
  )
}

// The forecast desk's track record, for the historical replay and for the user's own resolved forecasts. It shows a
// score only when the server's claim backs one (the desk ahead of the volatility-only formula, or of an even guess,
// across the whole 90% range over enough questions); otherwise it says how far the record has come, never a number.
import { useEffect, useState } from 'react'
import { Link } from 'react-router'
import { Card } from '../../components/ui/Card'
import { IconArrowRight, IconInfo } from '../../components/ui/icons'
import { Skeleton } from '../../components/ui/Spinner'
import { Tabs } from '../../components/ui/Tabs'
import { api } from '../../lib/api'
import { fmtProb } from '../../lib/format'
import type { ForecastSummary, TrackClaim, TrackRecord } from '../../lib/types'

export type TrackRecords = { replay: TrackRecord; mine: TrackRecord }
type Tab = 'replay' | 'mine'

const f3 = (x: number | null | undefined) => (x == null ? '—' : x.toFixed(3))
const f2 = (x: number) => x.toFixed(2)

// The server's claimMinN, for older payloads that carry no claim.
const CLAIM_MIN_N = 50

export function TrackRecordCard({
  records,
  error,
  onSeeQuestions,
}: {
  records: TrackRecords | null
  error: string | null
  onSeeQuestions: () => void
}) {
  const [tab, setTab] = useState<Tab>('replay')
  const [calls, setCalls] = useState<ForecastSummary[] | null>(null)
  const r = records?.[tab]

  // The replay's latest resolved calls, once the record is in (after the page's own first load); the card works without them.
  const ready = records != null
  useEffect(() => {
    if (!ready) return
    api.forecasts({ status: 'completed', scope: 'replay' }).then(
      (res) => setCalls(recentCalls(Array.isArray(res.forecasts) ? res.forecasts : [])),
      () => setCalls([]),
    )
  }, [ready])
  return (
    <Card className="mt-6 overflow-hidden">
      <section aria-labelledby="track-record">
        <div className="flex flex-wrap items-end justify-between gap-3 border-b border-border px-5 pt-5 pb-4 md:px-6">
          <div>
            <div className="label">Track record</div>
            <h2 id="track-record" className="mt-1 font-display text-[24px] leading-tight font-medium text-text">
              Scored against what happened
            </h2>
          </div>
          <Tabs<Tab>
            aria-label="Track record"
            size="sm"
            value={tab}
            onChange={setTab}
            items={[
              { value: 'replay', label: `Historical replay · ${records?.replay.n ?? '…'}` },
              { value: 'mine', label: `Yours · ${records?.mine.n ?? '…'}` },
            ]}
          />
        </div>
        <div className="px-5 py-5 md:px-6">
          {!records ? (
            error ? (
              <p className="text-[13.5px] text-muted">Couldn't load the track record. {error}</p>
            ) : (
              <div aria-busy="true" aria-label="Loading the track record" className="grid gap-6 md:grid-cols-[minmax(0,1fr)_20rem]">
                <div>
                  <Skeleton className="h-14 w-36" />
                  <Skeleton className="mt-3 h-4 w-64" />
                  <Skeleton className="mt-2 h-4 w-56" />
                </div>
                <Skeleton className="h-48 w-full rounded-xl" />
              </div>
            )
          ) : !r || r.n === 0 ? (
            <p className="max-w-xl text-[14px] leading-relaxed text-muted">
              {tab === 'mine'
                ? 'Resolve your forecasts to build your own record. Price questions on Binance assets resolve automatically.'
                : 'The replay track record is being prepared.'}
            </p>
          ) : (
            <>
              <Record r={r} />
              {tab === 'replay' && calls && calls.length > 0 && <RecentCalls calls={calls} />}
            </>
          )}
        </div>
        {tab === 'replay' && records && records.replay.n > 0 && (
          <p className="border-t border-border px-5 py-3.5 text-[12.5px] leading-relaxed text-muted md:px-6">
            Historical replay: the same three-specialist panel re-run as of Jul 15, Aug 12 and Sep 9, 2026 (after the
            model's training cutoff) with only Binance data and news published before each date, on {records.replay.n}{' '}
            two-week crypto price questions. The volatility-only formula is the bar to beat: a coin flip is a weak one on
            these questions.
            {claimOf(records.replay).status !== 'ahead' && ' We show a score only once it is backed by enough resolved questions.'}{' '}
            <button type="button" onClick={onSeeQuestions} className="rounded-sm font-medium text-accent-ink hover:underline">
              See the questions
            </button>
          </p>
        )}
      </section>
    </Card>
  )
}

/** The volatility-only formula's side of the record: its Brier over the paired rows; null when none. */
export function anchorOf(r: TrackRecord) {
  // Older payloads carry no anchorN: their anchor was replay-only and covered every row.
  if (r.anchorBrier == null || (r.anchorN ?? r.n) <= 0) return null
  return { brier: r.anchorBrier, n: r.anchorN ?? r.n }
}

/**
 * The claim the card may show. Older payloads without one read as too_few, and an "ahead" that carries no positive
 * skill reads as no_edge: the card never shows a number the server has not backed.
 */
export function claimOf(r: TrackRecord): TrackClaim {
  const c = r.claim
  if (!c) return { status: 'too_few', basis: null, minN: CLAIM_MIN_N, n: r.anchorN || r.n, skill: null }
  if (c.status === 'ahead' && !(c.basis && c.skill != null && c.skill > 0)) return { ...c, status: 'no_edge', skill: null }
  return c
}

/** A positive skill as "12%", or "Under 1%" when it rounds to 0. */
const pctOf = (skill: number) => (Math.round(skill * 100) < 1 ? 'Under 1%' : `${Math.round(skill * 100)}%`)

/** The claim's state: a backed score, or how far the record has come with no number. */
function Record({ r }: { r: TrackRecord }) {
  const c = claimOf(r)
  if (c.status === 'ahead') return <Scores r={r} c={c} />
  // c.n counts only the price questions when the record mixes them with others; an older payload can pass minN.
  const scored = `${c.n}${c.status === 'too_few' && c.n < c.minN ? ` of ${c.minN}` : ''} ${c.n < r.n ? 'price questions' : 'questions'} scored.`
  const uniform = c.basis === 'uniform' // no price questions: measured against an even guess, not the formula
  // A record still building with no price question among its resolved ones has no formula to be measured against.
  const evenGuess = r.n > 0 && !anchorOf(r)
  const [hero, line] =
    c.status === 'too_few'
      ? [
          'Building a track record',
          `${scored} We show a score once there are enough resolved questions to tell skill from luck, ${evenGuess ? 'measured against an even guess across the outcomes' : 'measured against a simple volatility formula, not a coin flip'}.`,
        ]
      : [
          'No proven edge yet',
          `${scored} The desk has not yet beaten ${uniform ? 'an even guess' : 'a simple volatility formula'} by more than chance. We show a score once it does.`,
        ]
  return (
    <div data-claim={c.status} className="max-w-2xl">
      <p className="relative flex flex-wrap items-center gap-x-2 font-display text-[28px] leading-tight font-medium text-text md:text-[32px]">
        {hero}
        {!uniform && !evenGuess && <FormulaTip />}
      </p>
      <p className="mt-2 text-[14px] leading-relaxed text-muted">{line}</p>
      {c.status === 'too_few' && c.n < c.minN && (
        <div
          role="progressbar"
          aria-label="Questions scored"
          aria-valuemin={0}
          aria-valuemax={c.minN}
          aria-valuenow={Math.min(c.n, c.minN)}
          aria-valuetext={`${c.n} of ${c.minN}`}
          className="mt-4 h-1.5 w-full max-w-xs overflow-hidden rounded-full bg-border"
        >
          <div className="h-full rounded-full bg-accent" style={{ width: `${Math.min(1, c.n / c.minN) * 100}%` }} />
        </div>
      )}
    </div>
  )
}

/**
 * A backed score: skill against the volatility-only formula (or, for a record with no price questions, an even guess)
 * leads, the Brier score with its bootstrap range and the sample under it, the coin flip a muted second line.
 */
function Scores({ r, c }: { r: TrackRecord; c: TrackClaim }) {
  const hits = Math.round((r.hitRate ?? 0) * r.n)
  const baseline = r.baselineBrier === 0.25 ? 'coin flip' : 'uniform guess'
  const formula = c.basis === 'formula'
  const anchor = formula ? anchorOf(r) : null
  const coin = r.skill ?? (r.brier != null && r.baselineBrier ? 1 - r.brier / r.baselineBrier : null)
  const ci = r.brierCi90
  return (
    <div data-claim="ahead" className="grid items-center gap-6 md:grid-cols-[minmax(0,1fr)_20rem]">
      <div className="min-w-0">
        <p className="relative font-display text-[22px] leading-tight font-medium text-text">
          <span>
            <span className="text-[56px] leading-none tracking-[-0.03em] tabular-nums md:text-[64px]">{pctOf(c.skill!)}</span> better
            than {formula ? 'a volatility-only formula' : 'an even guess'}
          </span>
          {/* Inline after the words, so it wraps with the last one on a narrow screen. */}
          {formula && (
            <>
              {' '}
              <FormulaTip />
            </>
          )}
        </p>
        <p className="mt-3 font-mono text-[12.5px] leading-relaxed text-text tabular-nums">
          Brier {f3(r.brier)}
          {ci && ` (90% range ${f2(ci[0])}–${f2(ci[1])})`}
          {anchor && ` · formula ${f3(anchor.brier)}${anchor.n < r.n ? ` (n = ${anchor.n})` : ''}`} · {baseline} {f3(r.baselineBrier)} · n = {r.n}
        </p>
        {formula && coin != null && coin > 0 && <p className="mt-1.5 text-[13.5px] text-muted">{pctOf(coin)} better than a {baseline}</p>}
        <p className="mt-1.5 text-[13.5px] text-muted">
          Top outcome right {hits} of {r.n} ({fmtProb(r.hitRate)}) · log loss {f3(r.logLoss)}
        </p>
      </div>
      <ReliabilityChart bins={r.bins} />
    </div>
  )
}

/** What the volatility-only formula is, and why a coin flip is a weak bar, on hover or focus. Its line is `relative`. */
function FormulaTip() {
  return (
    // The tooltip spans the line, so it never runs off a narrow screen.
    <span className="group inline-flex self-center align-middle font-sans">
      <span
        tabIndex={0}
        aria-describedby="anchor-tip"
        aria-label="About the volatility-only formula"
        className="grid size-5 place-items-center rounded-full text-muted hover:text-text focus-visible:text-text"
      >
        <IconInfo size={14} />
      </span>
      <span
        id="anchor-tip"
        role="tooltip"
        className="pointer-events-none absolute bottom-full left-0 z-30 mb-2 w-full max-w-sm rounded-md bg-ink px-2.5 py-1.5 text-xs leading-snug font-normal tracking-normal text-ink-fg opacity-0 shadow-lg transition-opacity duration-150 group-focus-within:opacity-100 group-hover:opacity-100"
      >
        The volatility-only formula prices each question from a zero-drift lognormal outlook over 90 days of realized
        volatility, as of the question's date: no news, no judgment. It is the bar to beat. A coin flip is a weak one
        here: the thresholds sit near the day's price, so a question like "above 0.94 × today's price" is usually yes.
      </span>
    </span>
  )
}

/** The 3 latest resolved binary replay calls, by resolution date. */
export function recentCalls(rows: ForecastSummary[]) {
  return rows
    .filter((f) => f.shape === 'binary' && f.headline && f.resolvedOutcome != null)
    .sort((a, b) => b.resolutionDate.localeCompare(a.resolutionDate))
    .slice(0, 3)
}

function RecentCalls({ calls }: { calls: ForecastSummary[] }) {
  return (
    <section aria-labelledby="recent-calls" className="mt-6 border-t border-border pt-4">
      <h3 id="recent-calls" className="label">
        Recent desk calls
      </h3>
      <ul className="mt-2 divide-y divide-border">
        {calls.map((f) => (
          <li key={f.id}>
            <Link to={`/forecasts/${f.id}`} className="group flex flex-col gap-1 py-2.5 sm:flex-row sm:items-center sm:gap-4">
              <span className="min-w-0 flex-1 truncate text-[13.5px] text-text group-hover:text-accent-ink" title={f.question}>
                {f.question}
              </span>
              <span className="flex shrink-0 items-center gap-3 font-mono text-[11.5px] text-muted tabular-nums">
                <span>
                  Desk Yes {fmtProb(f.headline!.p)} → Resolved {f.resolvedOutcome}
                </span>
                <IconArrowRight size={13} className="transition-transform group-hover:translate-x-0.5" />
              </span>
            </Link>
          </li>
        ))}
      </ul>
    </section>
  )
}

// Chart geometry (SVG units): a 260×200 canvas with room for the axis labels.
const W = 260
const H = 200
const L = 38
const R = 10
const T = 10
const B = 36
const px = (p: number) => L + p * (W - L - R)
const py = (o: number) => T + (1 - o) * (H - T - B)

/** Reliability: one dot per non-empty bin at (mean forecast, observed frequency), sized by its count. */
export function ReliabilityChart({ bins }: { bins: TrackRecord['bins'] }) {
  const dots = bins.filter((b) => b.n > 0 && b.meanP != null && b.observed != null)
  const label = `Reliability chart. ${dots.map((b) => `Forecasts near ${fmtProb(b.meanP)} happened ${fmtProb(b.observed)} of the time (${b.n})`).join('. ')}.`
  return (
    <div className="w-full max-w-[22rem] justify-self-center">
      <svg viewBox={`0 0 ${W} ${H}`} role="img" aria-label={label} className="w-full text-muted">
        {[0, 0.5, 1].map((t) => (
          <g key={t} className="font-mono text-[8.5px]" fill="currentColor">
            <line x1={px(0)} x2={px(1)} y1={py(t)} y2={py(t)} strokeWidth="1" className="stroke-border" />
            <text x={L - 5} y={py(t) + 3} textAnchor="end">
              {t * 100}%
            </text>
            <text x={px(t)} y={H - B + 13} textAnchor="middle">
              {t * 100}%
            </text>
          </g>
        ))}
        <line x1={px(0)} x2={px(0)} y1={py(0)} y2={py(1)} strokeWidth="1" className="stroke-border" />
        <line x1={px(0)} y1={py(0)} x2={px(1)} y2={py(1)} strokeWidth="1.2" strokeDasharray="4 4" className="stroke-muted/60" />
        {dots.map((b) => {
          const cx = px(b.meanP!)
          const cy = py(b.observed!)
          const r = 3 + Math.sqrt(b.n)
          const right = b.meanP! < 0.8
          return (
            <g key={b.lo} data-bin={b.lo}>
              <circle cx={cx} cy={cy} r={r} strokeWidth="1.5" className="fill-accent stroke-surface" />
              <text x={right ? cx + r + 3 : cx - r - 3} y={cy + 3} textAnchor={right ? 'start' : 'end'} className="font-mono text-[8.5px] fill-text">
                {b.n}
              </text>
            </g>
          )
        })}
        <text x={px(0.5)} y={H - 6} textAnchor="middle" className="text-[9px]" fill="currentColor">
          Forecast probability
        </text>
        <text
          x={10}
          y={py(0.5)}
          textAnchor="middle"
          transform={`rotate(-90 10 ${py(0.5)})`}
          className="text-[9px]"
          fill="currentColor"
        >
          How often it happened
        </text>
      </svg>
      {/* Outside the plot, so it never sits on a dot. */}
      <div className="mt-1 flex flex-wrap justify-center gap-x-4 gap-y-1 font-mono text-[10.5px] text-muted">
        <span className="flex items-center gap-1.5">
          <svg aria-hidden="true" width="18" height="4">
            <line x1="0" y1="2" x2="18" y2="2" stroke="currentColor" strokeWidth="1.2" strokeDasharray="4 3" />
          </svg>
          perfectly calibrated
        </span>
        <span className="flex items-center gap-1.5">
          <span aria-hidden="true" className="size-2 rounded-full bg-accent" />
          forecasts, sized by count
        </span>
      </div>
    </div>
  )
}

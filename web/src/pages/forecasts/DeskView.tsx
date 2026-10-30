// The numeric report's hero (API contract "Desk view and methods"): the desk's median, its 50% and 80% ranges
// and the chance of ending at or above today, read from the final buckets by the server, on a log (or linear) axis.
// A flat distribution never reads as "most likely"; the buckets stay below as the scored distribution. The median and
// the chance above today show only when the view's basis anchors them (cited scenarios or the measured history at the
// capped drift); otherwise the hero leads with the ranges and says why.
import { Fragment, type ReactNode } from 'react'
import { ProbabilityBars } from '../../components/ui/ProbabilityBars'
import { cx } from '../../lib/cx'
import { fmtDate } from '../../lib/format'
import type { DeskQ, DeskView, OutcomeP } from '../../lib/types'
import { anchored, fmtAnnual, fmtChance, fmtChange, fmtLevel, fmtLinearChange, fmtPct, fmtQ, fmtToday, rangeHints, roundPcts, topIndex } from './forecast'

/** The strip's axis: the 80% range and today with 20% to spare (×1.2 on a log scale), so every mark sits inside. */
export function stripDomain(view: DeskView): [number, number] {
  const ref = view.reference?.value
  const lo = Math.min(view.p10.value, ref ?? Infinity)
  const hi = Math.max(view.p90.value, ref ?? -Infinity)
  if (view.scale === 'log') return [lo / 1.2, hi * 1.2]
  const pad = (hi - lo) * 0.2 || Math.abs(hi) * 0.1 || 1
  return [lo - pad, hi + pad]
}

/** Where a level sits on the strip, 0..100 (clamped). */
export function stripPos(view: DeskView, x: number) {
  const [lo, hi] = stripDomain(view)
  const t = (v: number) => (view.scale === 'log' ? Math.log(v) : v)
  const at = view.scale === 'log' && x <= 0 ? 0 : ((t(x) - t(lo)) / (t(hi) - t(lo))) * 100
  return Math.max(0, Math.min(100, Number.isFinite(at) ? at : 0))
}

const width = (view: DeskView, a: DeskQ, b: DeskQ) => Math.max(0.6, stripPos(view, b.value) - stripPos(view, a.value))

/** "about $242", for the strip's screen-reader text. */
const spoken = (q: DeskQ, view: DeskView) => `${q.approx ? 'about ' : ''}${fmtLevel(q.value, view.unit)}`

/**
 * The view on one axis: the 80% range (pale), the 50% range (darker), a median tick (when the basis anchors one) and a
 * dashed today marker. Percentage-positioned divs, so it stays legible at 320 px; screen readers get the numbers in one
 * sentence.
 */
export function RangeStrip({ view, className }: { view: DeskView; className?: string }) {
  const ref = view.reference
  const centred = anchored(view)
  const label = [
    centred && `Median ${spoken(view.median, view)}.`,
    `50% range ${spoken(view.p25, view)} to ${spoken(view.p75, view)}.`,
    `80% range ${spoken(view.p10, view)} to ${spoken(view.p90, view)}.`,
    ref && `Today ${fmtToday(ref.value, view.unit)}.`,
  ]
    .filter(Boolean)
    .join(' ')
  const p10 = stripPos(view, view.p10.value)
  const p90 = stripPos(view, view.p90.value)
  return (
    <div className={className}>
      <div role="img" aria-label={label} className="relative h-7">
        <span className="absolute inset-x-0 top-1/2 h-px -translate-y-1/2 bg-border" />
        <span className="absolute inset-y-1.5 rounded-full bg-accent/25" style={{ left: `${p10}%`, width: `${width(view, view.p10, view.p90)}%` }} />
        <span className="absolute inset-y-1.5 rounded-full bg-accent/60" style={{ left: `${stripPos(view, view.p25.value)}%`, width: `${width(view, view.p25, view.p75)}%` }} />
        {centred && (
          <span data-mark="median" className="absolute inset-y-0 w-[3px] -translate-x-1/2 rounded-full bg-text" style={{ left: `${stripPos(view, view.median.value)}%` }} />
        )}
        {ref && (
          <span data-mark="today" className="absolute -inset-y-1 w-0 -translate-x-1/2 border-l-2 border-dashed border-text/55" style={{ left: `${stripPos(view, ref.value)}%` }} />
        )}
      </div>
      {/* The 80% range's ends under it, each anchored toward its own side so neither runs off the strip. */}
      {p90 - p10 >= 30 && (
        <div aria-hidden="true" className="relative mt-1 h-4 font-mono text-[11px] text-muted tabular-nums">
          <span className="absolute whitespace-nowrap" style={{ left: `${p10}%`, transform: `translateX(-${p10}%)` }}>
            {fmtQ(view.p10, view.unit)}
          </span>
          <span className="absolute whitespace-nowrap" style={{ left: `${p90}%`, transform: `translateX(-${p90}%)` }}>
            {fmtQ(view.p90, view.unit)}
          </span>
        </div>
      )}
      <div aria-hidden="true" className="mt-2 flex flex-wrap gap-x-4 gap-y-1 font-mono text-[10.5px] tracking-wide text-muted uppercase">
        <span className="flex items-center gap-1.5">
          <span className="h-2 w-3.5 rounded-full bg-accent/60" /> 50% range
        </span>
        <span className="flex items-center gap-1.5">
          <span className="h-2 w-3.5 rounded-full bg-accent/25" /> 80% range
        </span>
        {centred && (
          <span className="flex items-center gap-1.5">
            <span className="h-3 w-[3px] rounded-full bg-text" /> median
          </span>
        )}
        {ref && (
          <span className="flex items-center gap-1.5">
            <span className="h-3 w-0 border-l-2 border-dashed border-text/55" /> today {fmtToday(ref.value, view.unit)}
          </span>
        )}
      </div>
    </div>
  )
}

/**
 * "+26% vs today's $485 · about +4.7% a year": only the parts the view carries, written as the lead writes them (today's
 * level at its own precision, "$472.20"). A median in an open range is an extrapolation, so, as in the lead, it gets no
 * change from today.
 */
export function changeLine(view: DeskView) {
  const ref = view.reference
  if (!ref || view.median.approx || !anchored(view)) return ''
  const today = `today's ${fmtToday(ref.value, view.unit)}`
  const move =
    view.scale === 'log' && view.changePct != null
      ? `${fmtChange(view.changePct)} vs ${today}`
      : view.scale === 'linear' && view.change != null
        ? `${fmtLinearChange(view.change, view.unit)} vs ${today}`
        : ''
  return [move, view.annualPct != null && `about ${fmtAnnual(view.annualPct)} a year`].filter(Boolean).join(' · ')
}

function Tile({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex min-w-0 items-baseline justify-between gap-3 rounded-lg bg-surface/80 px-3 py-2 sm:block">
      <dt className="label leading-tight">{label}</dt>
      <dd className="font-mono text-[14.5px] font-medium text-text tabular-nums sm:mt-1">{value}</dd>
    </div>
  )
}

/**
 * The left half of a numeric report's hero when the view leads (`headline: "view"`): the median, how far it is from
 * today, the strip, the ranges, and the buckets with their distance from today; under the median, the server's line on
 * what it rests on. A view no evidence anchors (basis `none`) leads with its 50% range instead and states no median.
 */
export function DeskViewHero({ view, final, resolutionDate }: { view: DeskView; final: OutcomeP[]; resolutionDate: string }) {
  const ref = view.reference
  const centred = anchored(view)
  const change = changeLine(view)
  const pcts = roundPcts(final.map((o) => o.p))
  const ti = topIndex(final)
  const hints = rangeHints(view, final.length)
  const tiles = [
    ref && view.pAbove && { label: 'Chance at or above today', value: fmtChance(view.pAbove.value, view.pAbove.approx) },
    centred && { label: '50% range', value: `${fmtQ(view.p25, view.unit)}–${fmtQ(view.p75, view.unit)}` },
    { label: '80% range', value: `${fmtQ(view.p10, view.unit)}–${fmtQ(view.p90, view.unit)}` },
  ].filter((t): t is { label: string; value: string } => !!t)
  const basis = view.basis.label.replace(/[.\s]+$/, '')
  return (
    <div className="min-w-0 border-b border-border bg-accent-soft/35 p-6 md:p-8 lg:border-r lg:border-b-0">
      <div className="label">
        Desk view · {ref ? 'close on' : 'for'} {fmtDate(resolutionDate)}
      </div>
      {centred ? (
        <div className="mt-2 flex flex-wrap items-baseline gap-x-3 gap-y-1">
          <span className="font-display text-[64px] leading-[0.95] font-medium tracking-[-0.035em] text-text tabular-nums md:text-[80px]">
            {fmtQ(view.median, view.unit)}
          </span>
          <span className="label">Median</span>
        </div>
      ) : (
        // The range's ends wrap apart on a narrow screen rather than overflow it.
        <div className="mt-2 flex flex-wrap items-baseline gap-x-3 gap-y-1">
          <span className="inline-flex flex-wrap font-display text-[44px] leading-[1.02] font-medium tracking-[-0.03em] text-text tabular-nums md:text-[56px]">
            <span>{fmtQ(view.p25, view.unit)}</span>
            <span>–{fmtQ(view.p75, view.unit)}</span>
          </span>
          <span className="label">50% range</span>
        </div>
      )}
      {change && (
        // Each part stays on one line; on a narrow screen the second wraps whole.
        <p
          className="mt-2 font-mono text-[13px] text-text tabular-nums"
          title={ref ? `${[ref.label, ref.source].filter(Boolean).join(' · ')}, as of ${fmtDate(ref.asOf, { time: true })}` : undefined}
        >
          {change.split(' · ').map((part, i) => (
            <Fragment key={i}>
              {i > 0 && ' '}
              <span className="whitespace-nowrap">{i > 0 ? `· ${part}` : part}</span>
            </Fragment>
          ))}
        </p>
      )}
      {basis && <p className="mt-2 text-[12.5px] leading-snug text-pretty text-muted">{basis}.</p>}
      <RangeStrip view={view} className="mt-6" />
      <dl className={cx('mt-5 grid gap-2', tiles.length === 3 ? 'sm:grid-cols-3' : tiles.length === 2 ? 'sm:grid-cols-2' : '')}>
        {tiles.map((t) => (
          <Tile key={t.label} {...t} />
        ))}
      </dl>
      <ProbabilityBars
        className="mt-6"
        highlight={!view.flat}
        relative
        items={final.map((o, i) => ({ label: o.outcome, p: o.p, hint: hints[i] }))}
      />
      {view.flat && final[ti] && (
        <p className="mt-3 text-[12.5px] leading-snug text-muted">
          No single range stands out (top range {final[ti].outcome} at {fmtPct(pcts[ti], final[ti].p)}).
        </p>
      )}
    </div>
  )
}

/**
 * Under a top-range hero (`headline: "bucket"`): "Median $67,700 · 80% $62,400–$73,500 · 64% above today"; a view no
 * evidence anchors gives its ranges only, "50% $65,100–$69,900 · 80% $62,400–$73,500".
 */
export function BucketViewLine({ view }: { view: DeskView }) {
  const centred = anchored(view)
  const parts: ReactNode[] = [
    centred ? `Median ${fmtQ(view.median, view.unit)}` : `50% ${fmtQ(view.p25, view.unit)}–${fmtQ(view.p75, view.unit)}`,
    `80% ${fmtQ(view.p10, view.unit)}–${fmtQ(view.p90, view.unit)}`,
    centred && view.reference && view.pAbove && `${fmtChance(view.pAbove.value, view.pAbove.approx)} above today`,
  ].filter(Boolean)
  return <p className="mt-3 font-mono text-[12.5px] text-text tabular-nums">{parts.join(' · ')}</p>
}

/**
 * Each member's median on the hero's axis, one row apiece, with the pool and the final: where the methods sit relative
 * to each other and to today.
 */
export function MedianDots({ view, rows }: { view: DeskView; rows: { label: string; q: DeskQ; final?: boolean }[] }) {
  const ref = view.reference
  return (
    <div className="mt-5">
      <div className="label">Medians</div>
      <ul className="mt-2 flex flex-col gap-1.5">
        {rows.map((r) => (
          <li key={r.label} className="grid grid-cols-[minmax(0,6.5rem)_1fr_4.25rem] items-center gap-2.5 text-[12.5px]">
            <span className={cx('min-w-0 truncate', r.final ? 'font-medium text-text' : 'text-muted')} title={r.label}>
              {r.label}
            </span>
            <span aria-hidden="true" className="relative h-3">
              <span className="absolute inset-x-0 top-1/2 h-px -translate-y-1/2 bg-border" />
              {ref && <span className="absolute -inset-y-0.5 w-0 border-l border-dashed border-text/45" style={{ left: `${stripPos(view, ref.value)}%` }} />}
              <span
                className={cx('absolute top-1/2 size-2.5 -translate-1/2 rounded-full', r.final ? 'bg-accent ring-2 ring-accent/30' : 'bg-text/70')}
                style={{ left: `${stripPos(view, r.q.value)}%` }}
              />
            </span>
            <span className={cx('text-right font-mono tabular-nums', r.final ? 'font-semibold text-text' : 'text-muted')}>{fmtQ(r.q, view.unit)}</span>
          </li>
        ))}
      </ul>
    </div>
  )
}

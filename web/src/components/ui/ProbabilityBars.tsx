import { cx } from '../../lib/cx'
import { fmtPct, roundPcts } from '../../pages/forecasts/forecast'

/**
 * Horizontal outcome bars: label · bar · %. The most likely outcome is highlighted in gold; the percents sum to 100.
 * An item's `hint` is a muted second line under its label; `baseline` (0..1) draws a thin tick on the bar at a
 * reference distribution's share, named for screen readers by `baselineLabel`. `relative` draws widths against the
 * largest share on the list (bars and ticks alike), for many-bucket distributions where no outcome nears 100%.
 * `highlight={false}` leaves every bar plain, for a distribution where no outcome stands out.
 */
export function ProbabilityBars({
  items,
  compact,
  className,
  baselineLabel = 'baseline',
  relative,
  highlight = true,
}: {
  items: { label: string; p: number; hint?: string; baseline?: number }[]
  compact?: boolean
  className?: string
  baselineLabel?: string
  relative?: boolean
  highlight?: boolean
}) {
  const top = highlight ? items.reduce((best, it, i) => (it.p > items[best].p ? i : best), 0) : -1
  const pcts = roundPcts(items.map((it) => it.p))
  const basePcts = items.some((it) => it.baseline != null) ? roundPcts(items.map((it) => it.baseline ?? 0)) : []
  const full = relative ? Math.max(...items.flatMap((it) => [it.p, it.baseline ?? 0]).filter(Number.isFinite), 0) || 1 : 1
  return (
    <ul className={cx('flex flex-col', compact ? 'gap-1.5' : 'gap-2.5', className)}>
      {items.map((it, i) => (
        <li
          key={`${it.label}-${i}`}
          className={cx(
            'grid items-center gap-3',
            // The label column narrows on phones so the bar keeps its share of the row.
            compact ? 'grid-cols-[minmax(0,7rem)_1fr_2.75rem]' : 'grid-cols-[minmax(0,7.5rem)_1fr_3rem] sm:grid-cols-[minmax(0,11rem)_1fr_3rem]',
          )}
        >
          <span className="min-w-0">
            <span className={cx('block truncate', compact ? 'text-xs' : 'text-[13.5px]', i === top ? 'font-medium text-text' : 'text-muted')} title={it.label}>
              {it.label}
            </span>
            {it.hint && <span className="block truncate font-mono text-[10.5px] text-muted tabular-nums">{it.hint}</span>}
          </span>
          <span aria-hidden="true" className={cx('relative overflow-hidden rounded-full bg-surface-2', compact ? 'h-1.5' : 'h-2.5')}>
            <span
              className={cx('block h-full origin-left animate-bar rounded-full', i === top ? 'bg-accent' : 'bg-muted/45')}
              style={{ width: `${Math.round(Math.max(0, Math.min(1, it.p / full)) * 1e4) / 100}%`, animationDelay: `${i * 50}ms` }}
            />
            {it.baseline != null && Number.isFinite(it.baseline) && (
              <span
                data-baseline
                className="absolute inset-y-0 w-0.5 -translate-x-1/2 rounded-full bg-text/75"
                style={{ left: `${Math.max(0.5, Math.min(99.5, Math.round((it.baseline / full) * 1e4) / 100))}%` }}
              />
            )}
          </span>
          <span className={cx('text-right font-mono tabular-nums', compact ? 'text-xs' : 'text-[13px]', i === top ? 'font-semibold text-accent-ink' : 'text-muted')}>
            {Number.isFinite(it.p) ? fmtPct(pcts[i], it.p) : '—'}
            {it.baseline != null && Number.isFinite(it.baseline) && <span className="sr-only">, {baselineLabel} {fmtPct(basePcts[i], it.baseline)}</span>}
          </span>
        </li>
      ))}
    </ul>
  )
}

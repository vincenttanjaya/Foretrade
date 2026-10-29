// Small presentational pieces shared by the forecast pages.
import { Badge } from '../../components/ui/Badge'
import { IconCheck } from '../../components/ui/icons'
import { cx } from '../../lib/cx'
import type { ForecastShape, ForecastStatus } from '../../lib/types'
import { shapeLabel } from './forecast'

export function ShapeBadge({ shape }: { shape: string }) {
  return <Badge tone="outline">{shapeLabel(shape)}</Badge>
}

/** Gold dot with a slow ping: "live". */
export function PulseDot({ className }: { className?: string }) {
  return (
    <span aria-hidden="true" className={cx('relative inline-flex size-1.5 shrink-0', className)}>
      <span className="absolute inset-0 animate-ping rounded-full bg-accent opacity-70" />
      <span className="relative size-1.5 rounded-full bg-accent" />
    </span>
  )
}

export function StatusPill({ status }: { status: ForecastStatus }) {
  if (status === 'archived') return <Badge tone="outline">Archived</Badge>
  if (status === 'running')
    return (
      <Badge tone="accent">
        <PulseDot />
        In flight
      </Badge>
    )
  if (status === 'failed') return <Badge tone="down">Failed</Badge>
  return (
    <Badge tone="neutral">
      <IconCheck size={11} strokeWidth={2.4} />
      Completed
    </Badge>
  )
}

export function ConfidenceBadge({ level }: { level: unknown }) {
  if (level !== 'low' && level !== 'medium' && level !== 'high') return null
  return <Badge tone={level === 'high' ? 'accent' : 'outline'}>{level} confidence</Badge>
}

/** Tiny illustration of each shape for the shape picker: a 58/42 split, a bell curve, category bars. */
export function ShapeGlyph({ shape, active }: { shape: ForecastShape; active?: boolean }) {
  const hi = active ? 'bg-accent' : 'bg-muted/55'
  if (shape === 'binary')
    return (
      <div aria-hidden="true" className="flex h-9 w-full flex-col justify-center gap-1.5">
        <div className="flex h-2 w-full overflow-hidden rounded-full">
          <span className={cx('w-[58%]', hi)} />
          <span className="flex-1 bg-muted/20" />
        </div>
        <div className="flex justify-between font-mono text-[9.5px] tracking-wider text-muted">
          <span>58 YES</span>
          <span>42 NO</span>
        </div>
      </div>
    )
  if (shape === 'ordered')
    return (
      <svg aria-hidden="true" viewBox="0 0 100 36" preserveAspectRatio="none" className="h-9 w-full">
        <path
          d="M0 34 C 22 34, 30 4, 50 4 S 78 34, 100 34 Z"
          className={active ? 'fill-accent/20' : 'fill-muted/12'}
        />
        <path
          d="M0 34 C 22 34, 30 4, 50 4 S 78 34, 100 34"
          fill="none"
          strokeWidth="2"
          vectorEffect="non-scaling-stroke"
          className={active ? 'stroke-accent' : 'stroke-muted/60'}
        />
        {[20, 40, 60, 80].map((x) => (
          <line key={x} x1={x} x2={x} y1="6" y2="34" strokeWidth="1" vectorEffect="non-scaling-stroke" className="stroke-border" strokeDasharray="2 2" />
        ))}
      </svg>
    )
  return (
    <div aria-hidden="true" className="flex h-9 w-full items-end gap-1.5">
      {[85, 55, 38, 20].map((h, i) => (
        <span key={h} className={cx('flex-1 rounded-t-sm', i === 0 ? hi : 'bg-muted/25')} style={{ height: `${h}%` }} />
      ))}
    </div>
  )
}

/** A specialist's (or pipeline stage's) round initial. */
export function Monogram({ glyph, muted }: { glyph: string; muted?: boolean }) {
  return (
    <span
      aria-hidden="true"
      className={cx(
        'grid size-9 shrink-0 place-items-center rounded-full font-display text-[16px] font-medium',
        muted ? 'border border-border text-muted' : 'bg-ink text-ink-fg',
      )}
    >
      {glyph}
    </span>
  )
}

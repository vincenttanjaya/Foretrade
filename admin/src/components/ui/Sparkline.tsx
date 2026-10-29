import { cx } from '../../lib/cx'

/** Tiny decorative SVG line in the accent colour; pair it with the numbers it summarises. */
export function Sparkline({
  values,
  width = 96,
  height = 24,
  className,
}: {
  values: number[]
  width?: number
  height?: number
  className?: string
}) {
  if (values.length < 2) return <svg width={width} height={height} className={className} aria-hidden="true" />
  const max = Math.max(...values) || 1
  const line = values
    .map((v, i) => `${((i / (values.length - 1)) * width).toFixed(1)},${(2 + (1 - v / max) * (height - 4)).toFixed(1)}`)
    .join(' ')
  return (
    <svg width={width} height={height} viewBox={`0 0 ${width} ${height}`} className={cx('overflow-visible', className)} aria-hidden="true">
      <polyline points={line} fill="none" stroke="var(--accent-ink)" strokeWidth="1.5" strokeLinejoin="round" strokeLinecap="round" />
    </svg>
  )
}

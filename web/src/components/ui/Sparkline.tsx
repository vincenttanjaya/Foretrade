import { useId } from 'react'
import { cx } from '../../lib/cx'

/** Tiny SVG line coloured by first → last value (gain = green, loss = red). */
export function Sparkline({
  values,
  width = 96,
  height = 28,
  className,
}: {
  values: number[]
  width?: number
  height?: number
  className?: string
}) {
  const gradient = `spark${useId().replace(/[^\w-]/g, '')}`
  if (values.length < 2) return <svg width={width} height={height} className={className} aria-hidden="true" />

  const min = Math.min(...values)
  const range = Math.max(...values) - min || 1
  const pts = values.map((v, i) => [
    (i / (values.length - 1)) * width,
    2 + (1 - (v - min) / range) * (height - 4), // 2px breathing room so the stroke is not clipped
  ])
  const line = pts.map(([x, y]) => `${x.toFixed(1)},${y.toFixed(1)}`).join(' ')
  const color = values[values.length - 1] >= values[0] ? 'var(--up)' : 'var(--down)'

  return (
    <svg width={width} height={height} viewBox={`0 0 ${width} ${height}`} className={cx('overflow-visible', className)} aria-hidden="true">
      <defs>
        <linearGradient id={gradient} x1="0" x2="0" y1="0" y2="1">
          <stop offset="0" stopColor={color} stopOpacity="0.18" />
          <stop offset="1" stopColor={color} stopOpacity="0" />
        </linearGradient>
      </defs>
      <polygon points={`0,${height} ${line} ${width},${height}`} fill={`url(#${gradient})`} />
      <polyline points={line} fill="none" stroke={color} strokeWidth="1.5" strokeLinejoin="round" strokeLinecap="round" />
    </svg>
  )
}

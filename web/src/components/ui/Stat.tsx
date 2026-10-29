import type { ReactNode } from 'react'
import { cx } from '../../lib/cx'

/** Label / big number / sub line. `tone` colours the value for gains and losses. */
export function Stat({
  label,
  value,
  sub,
  tone = 'neutral',
  className,
}: {
  label: ReactNode
  value: ReactNode
  sub?: ReactNode
  tone?: 'neutral' | 'up' | 'down' | 'accent'
  className?: string
}) {
  return (
    <div className={cx('min-w-0', className)}>
      <div className="label">{label}</div>
      <div
        className={cx(
          'mt-1.5 truncate text-xl leading-tight font-semibold tracking-tight tabular-nums',
          tone === 'up' ? 'text-up' : tone === 'down' ? 'text-down' : tone === 'accent' ? 'text-accent-ink' : 'text-text',
        )}
      >
        {value}
      </div>
      {sub && <div className="mt-1 text-[12.5px] text-muted">{sub}</div>}
    </div>
  )
}

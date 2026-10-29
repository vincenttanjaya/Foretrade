import type { ReactNode } from 'react'
import { cx } from '../../lib/cx'

type Item<T extends string> = T | { value: T; label: ReactNode }

/** Segmented control. `items` can be plain values ('1h') or { value, label }. */
export function Tabs<T extends string>({
  value,
  onChange,
  items,
  size = 'md',
  className,
  'aria-label': ariaLabel,
}: {
  value: T
  onChange: (value: T) => void
  items: readonly Item<T>[]
  size?: 'sm' | 'md'
  className?: string
  'aria-label'?: string
}) {
  return (
    <div
      role="group"
      aria-label={ariaLabel}
      className={cx('inline-flex rounded-lg border border-border bg-surface-2 p-0.5', className)}
    >
      {items.map((item) => {
        const v = typeof item === 'string' ? item : item.value
        const active = v === value
        return (
          <button
            key={v}
            type="button"
            aria-pressed={active}
            onClick={() => onChange(v)}
            className={cx(
              'rounded-md font-medium whitespace-nowrap transition-colors duration-150',
              size === 'sm' ? 'h-7 px-2.5 text-xs' : 'h-8 px-3.5 text-[13px]',
              active ? 'bg-surface text-text shadow-[0_1px_2px_rgb(0_0_0/0.08)]' : 'text-muted hover:text-text',
            )}
          >
            {typeof item === 'string' ? item : item.label}
          </button>
        )
      })}
    </div>
  )
}

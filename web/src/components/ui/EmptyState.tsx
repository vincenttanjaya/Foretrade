import type { ReactNode } from 'react'
import { cx } from '../../lib/cx'

export function EmptyState({
  icon,
  title,
  description,
  action,
  className,
}: {
  icon?: ReactNode
  title: ReactNode
  description?: ReactNode
  action?: ReactNode
  className?: string
}) {
  return (
    <div
      className={cx(
        'flex flex-col items-center rounded-[14px] border border-dashed border-border px-6 py-12 text-center',
        className,
      )}
    >
      {icon && (
        <div className="mb-4 grid size-11 place-items-center rounded-full bg-accent-soft text-accent-ink">{icon}</div>
      )}
      <h3 className="font-display text-xl font-medium text-text">{title}</h3>
      {description && <p className="mt-1.5 max-w-sm text-sm leading-relaxed text-muted">{description}</p>}
      {action && <div className="mt-5">{action}</div>}
    </div>
  )
}

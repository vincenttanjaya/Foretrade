import type { ReactNode } from 'react'
import { cx } from '../../lib/cx'
import { Button } from './Button'
import { IconAlert } from './icons'

export function EmptyState({
  icon,
  title,
  description,
  action,
  className,
  as: Heading = 'h3',
}: {
  as?: 'h1' | 'h3' // h1 when the empty state is the whole page (not found)
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
      <Heading className="font-display text-xl font-medium text-text">{title}</Heading>
      {description && <p className="mt-1.5 max-w-sm text-sm leading-relaxed text-muted">{description}</p>}
      {action && <div className="mt-5">{action}</div>}
    </div>
  )
}

/** A failed load: the API's message and a Retry. */
export function ErrorState({ message, onRetry, className }: { message: string; onRetry: () => void; className?: string }) {
  return (
    <div role="alert" className={className}>
      <EmptyState
        icon={<IconAlert size={20} />}
        title="Couldn't load this."
        description={message}
        action={
          <Button variant="secondary" onClick={onRetry}>
            Retry
          </Button>
        }
      />
    </div>
  )
}

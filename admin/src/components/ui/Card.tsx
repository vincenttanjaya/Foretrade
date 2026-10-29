import type { ComponentProps, ReactNode } from 'react'
import { cx } from '../../lib/cx'

/** Surface card (1px hairline, 14px radius). No padding: use `p-5`, or CardHeader + a `px-5 pb-5` body. */
export function Card({ className, ...rest }: ComponentProps<'div'>) {
  return <div className={cx('card', className)} {...rest} />
}

export function CardHeader({
  title,
  label,
  actions,
  className,
}: {
  title: ReactNode
  label?: ReactNode // mono micro-label above the title
  actions?: ReactNode
  className?: string
}) {
  return (
    <div className={cx('flex items-start justify-between gap-4 px-5 pt-5 pb-3', className)}>
      <div className="min-w-0">
        {label && <div className="label mb-1">{label}</div>}
        <h3 className="font-display text-[19px] leading-snug font-medium text-text">{title}</h3>
      </div>
      {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
    </div>
  )
}

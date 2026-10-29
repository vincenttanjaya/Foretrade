import type { ComponentProps } from 'react'
import { cx } from '../../lib/cx'

const field =
  'w-full rounded-lg border border-border bg-surface text-sm text-text placeholder:text-muted/75 ' +
  'transition-[border-color,box-shadow] duration-150 hover:border-muted/45 ' +
  'focus:border-accent focus:outline-none focus:ring-[3px] focus:ring-accent/20 ' +
  'aria-invalid:border-down disabled:cursor-not-allowed disabled:opacity-55'

export function Input({ className, ...rest }: ComponentProps<'input'>) {
  return <input className={cx(field, 'h-10 px-3', className)} {...rest} />
}

const chevron =
  "url(\"data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24' fill='none' stroke='%238B91A0' stroke-width='2' stroke-linecap='round'%3E%3Cpath d='M7 10l5 5 5-5'/%3E%3C/svg%3E\")"

export function Select({ className, style, ...rest }: ComponentProps<'select'>) {
  return (
    <select
      className={cx(field, 'h-10 appearance-none bg-[length:16px] bg-[right_10px_center] bg-no-repeat pr-9 pl-3', className)}
      style={{ backgroundImage: chevron, ...style }}
      {...rest}
    />
  )
}

/** Mono micro-label for form fields. */
export function Label({ className, ...rest }: ComponentProps<'label'>) {
  return <label className={cx('label mb-1.5 block', className)} {...rest} />
}

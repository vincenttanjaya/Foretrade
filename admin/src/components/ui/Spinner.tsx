import { cx } from '../../lib/cx'
import { LogoMark } from './Logo'

/** Indeterminate spinner. Give it a `label` when it is the only sign that something is loading. */
export function Spinner({ className, label }: { className?: string; label?: string }) {
  return (
    <span role={label ? 'status' : undefined} className={cx('inline-flex', className)}>
      <svg viewBox="0 0 24 24" fill="none" className="size-full animate-spin" aria-hidden="true">
        <circle cx="12" cy="12" r="9" stroke="currentColor" strokeOpacity="0.2" strokeWidth="2.5" />
        <path d="M21 12a9 9 0 0 0-9-9" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" />
      </svg>
      {label && <span className="sr-only">{label}</span>}
    </span>
  )
}

/** Shimmering placeholder block; size it with className (e.g. "h-4 w-32"). */
export function Skeleton({ className }: { className?: string }) {
  return (
    <span aria-hidden="true" className={cx('relative block overflow-hidden rounded-md bg-surface-2', className)}>
      <span className="absolute inset-0 animate-shimmer bg-linear-to-r from-transparent via-surface/60 to-transparent" />
    </span>
  )
}

/** Centered logo + spinner for the first auth check. */
export function FullPageLoader() {
  return (
    <div className="grid h-dvh place-items-center bg-bg">
      <div className="flex animate-fade-up flex-col items-center gap-4 text-muted">
        <LogoMark size={40} />
        <Spinner className="size-5" label="Loading" />
      </div>
    </div>
  )
}

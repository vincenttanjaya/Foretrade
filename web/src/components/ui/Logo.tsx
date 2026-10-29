import { cx } from '../../lib/cx'

/** Three rising gold candlesticks in a gold-bordered navy square (same mark as the favicon). */
export function LogoMark({ size = 32, className }: { size?: number; className?: string }) {
  return (
    <svg width={size} height={size} viewBox="0 0 64 64" className={cx('shrink-0', className)} aria-hidden="true">
      <rect x="3" y="3" width="58" height="58" rx="13" fill="#0F1B2D" stroke="var(--accent)" strokeWidth="4" />
      <path
        fill="#E0B252"
        d="M19 31h2v4h3v10h-3v4h-2v-4h-3V35h3zM31 23h2v4h3v10h-3v4h-2v-4h-3V27h3zM43 15h2v4h3v10h-3v4h-2v-4h-3V19h3z"
      />
    </svg>
  )
}

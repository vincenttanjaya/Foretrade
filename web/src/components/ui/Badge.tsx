import type { ComponentProps } from 'react'
import { cx } from '../../lib/cx'

export type BadgeTone = 'neutral' | 'accent' | 'up' | 'down' | 'outline'

const tones: Record<BadgeTone, string> = {
  neutral: 'bg-surface-2 text-muted',
  accent: 'bg-accent-soft text-accent-ink',
  up: 'bg-up/10 text-up',
  down: 'bg-down/10 text-down',
  outline: 'border border-border text-muted',
}

/** Small mono uppercase tag: shape, status, stance… */
export function Badge({ tone = 'neutral', className, ...rest }: ComponentProps<'span'> & { tone?: BadgeTone }) {
  return (
    <span
      className={cx(
        'inline-flex h-5 shrink-0 items-center gap-1 rounded-md px-1.5 font-mono text-[10.5px] font-medium tracking-[0.08em] whitespace-nowrap uppercase',
        tones[tone],
        className,
      )}
      {...rest}
    />
  )
}

import { cx } from '../../lib/cx'
import { fmtPct, fmtUsd } from '../../lib/format'

/** "▲ 2.31%" / "▼ 1.02%" in gain/loss colour (the glyph keeps it readable without colour). */
export function PriceChange({
  value,
  kind = 'pct',
  decimals,
  className,
}: {
  value: number | null | undefined // a percentage (already ×100) or USD when kind = 'usd'
  kind?: 'pct' | 'usd'
  decimals?: number
  className?: string
}) {
  const n = value ?? 0
  const shown = Number(n.toFixed(decimals ?? 2)) // a move that rounds to 0.00 reads as flat, not "▼ 0.00%"
  const dir = shown > 0 ? 'up' : shown < 0 ? 'down' : 'flat'
  const text = kind === 'usd' ? fmtUsd(Math.abs(n), { decimals }) : fmtPct(Math.abs(n), { decimals })
  return (
    <span
      className={cx(
        'inline-flex items-baseline gap-1 font-mono tabular-nums whitespace-nowrap',
        dir === 'up' ? 'text-up' : dir === 'down' ? 'text-down' : 'text-muted',
        className,
      )}
    >
      {dir !== 'flat' && (
        <span aria-hidden="true" className="text-[0.75em]">
          {dir === 'up' ? '▲' : '▼'}
        </span>
      )}
      <span className="sr-only">{dir === 'up' ? 'up ' : dir === 'down' ? 'down ' : ''}</span>
      {value == null ? '—' : text}
    </span>
  )
}

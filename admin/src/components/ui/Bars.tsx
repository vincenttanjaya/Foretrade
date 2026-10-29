import { useState, type KeyboardEvent } from 'react'
import { cx } from '../../lib/cx'
import { fmtDay, fmtNum } from '../../lib/format'
import type { DayValue } from '../../lib/types'

/**
 * Daily bar strip (30 UTC days, oldest first). One tab stop: arrow keys, Home and End move between bars, and each bar
 * reads "Oct 2: 42 credits". `limit` draws a dashed line; bars above it turn red.
 */
export function Bars({
  data,
  label,
  unit,
  limit,
  className,
}: {
  data: DayValue[]
  label: string
  unit: string // singular: "credit" → "42 credits"
  limit?: number | null
  className?: string
}) {
  const [active, setActive] = useState(data.length - 1)
  const max = Math.max(1, limit ?? 0, ...data.map((d) => d.value))
  const total = data.reduce((a, d) => a + d.value, 0)
  const text = (d: DayValue) => `${fmtDay(d.day)}: ${fmtNum(d.value)} ${unit}${d.value === 1 ? '' : 's'}`

  function onKeyDown(e: KeyboardEvent<HTMLDivElement>) {
    const next = { ArrowLeft: active - 1, ArrowRight: active + 1, Home: 0, End: data.length - 1 }[e.key]
    if (next === undefined) return
    e.preventDefault()
    const i = Math.max(0, Math.min(data.length - 1, next))
    setActive(i)
    ;(e.currentTarget.children[i + (limit != null ? 1 : 0)] as HTMLElement | undefined)?.focus()
  }

  if (!data.length) return <p className={cx('text-sm text-muted', className)}>No data yet.</p>

  return (
    <figure className={cx('min-w-0', className)}>
      <div role="group" aria-label={label} onKeyDown={onKeyDown} className="relative flex h-36 items-end gap-[3px]">
        {limit != null && (
          <div
            aria-hidden="true"
            className="pointer-events-none absolute inset-x-0 border-t border-dashed border-muted/60"
            style={{ bottom: `${(limit / max) * 100}%` }}
          />
        )}
        {data.map((d, i) => (
          <div
            key={d.day}
            role="img"
            tabIndex={i === active ? 0 : -1}
            aria-label={text(d)}
            title={text(d)}
            onFocus={() => setActive(i)}
            className="group relative flex h-full min-w-0 flex-1 items-end rounded-sm focus-visible:outline-offset-1"
          >
            <span
              className={cx(
                'block w-full rounded-t-[3px] transition-colors',
                limit != null && d.value > limit ? 'bg-down' : i === data.length - 1 ? 'bg-accent' : 'bg-ink/70 group-hover:bg-ink',
              )}
              style={{ height: d.value ? `max(2px, ${(d.value / max) * 100}%)` : '1px', opacity: d.value ? 1 : 0.35 }}
            />
          </div>
        ))}
      </div>
      <figcaption className="mt-2 flex justify-between gap-2 font-mono text-[11px] text-muted tabular-nums">
        <span>{fmtDay(data[0].day)}</span>
        <span>
          {fmtNum(total)} {unit}s · peak {fmtNum(Math.max(...data.map((d) => d.value)))}
          {limit != null && <> · limit {fmtNum(limit)}</>}
        </span>
        <span>{fmtDay(data[data.length - 1].day)}</span>
      </figcaption>
    </figure>
  )
}

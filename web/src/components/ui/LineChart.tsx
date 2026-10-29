import { useEffect, useRef, useState } from 'react'
import { ColorType, createChart, createSeriesMarkers, LineSeries, type AutoscaleInfo, type IChartApi, type UTCTimestamp } from 'lightweight-charts'
import { useTheme } from '../../lib/theme'
import type { Point } from '../../lib/types'

export type ChartSeries = { name: string; points: Point[]; color?: string; muted?: boolean /* a benchmark: muted colour */; width?: 1 | 2 }
/** A labelled arrow above series `series` at time `t` (one of its points), e.g. where a strategy was published. */
export type ChartMarker = { series: number; t: number; text: string }

// Extra hues for multi-line charts; green/red stay reserved for gains and losses.
const PALETTE = ['#3B6FD8', '#0E9F8E', '#7C5CD6', '#D9467A', '#E07B39', '#64748B', '#8A9A2B']

const cssVar = (name: string) => getComputedStyle(document.documentElement).getPropertyValue(name).trim()

/** Series 0 is gold; with two series the second (the benchmark) is muted; more get distinct hues. */
function seriesColor(series: ChartSeries[], i: number) {
  if (series[i].color) return series[i].color
  if (series[i].muted) return cssVar('--muted')
  if (i === 0) return cssVar('--accent')
  return series.length === 2 ? cssVar('--muted') : PALETTE[(i - 1) % PALETTE.length]
}

/** Widens an autoscale range to at least `span` around its midpoint, so a small wiggle doesn't fill the chart. */
function withMinSpan(info: AutoscaleInfo | null, span: number): AutoscaleInfo | null {
  const r = info?.priceRange
  if (!info || !r || r.maxValue - r.minValue >= span) return info
  const mid = (r.minValue + r.maxValue) / 2
  return { ...info, priceRange: { minValue: mid - span / 2, maxValue: mid + span / 2 } }
}

const LABEL_H = 15 // px: an end label's height
const LABEL_GAP = 17 // px: the least distance between two end-label centres, so stacked labels never touch

type EndLabel = { key: string; y: number; text: string; color: string }

/** Spreads label centres (`y`, px from the top) at least `gap` apart in value order, kept inside the chart's height. */
export function stackLabels<T extends { y: number }>(labels: T[], gap: number, height: number): T[] {
  const out = labels.map((l) => ({ ...l })).sort((a, b) => a.y - b.y)
  for (let i = 0; i < out.length; i++) out[i].y = Math.max(out[i].y, i ? out[i - 1].y + gap : gap / 2) // push down, off the top edge
  let limit = height - gap / 2
  for (let i = out.length - 1; i >= 0; i--) {
    out[i].y = Math.min(out[i].y, limit) // then up, off the bottom edge
    limit = out[i].y - gap
  }
  return out
}

/** Dark or white text, whichever reads on a #rrggbb background. */
const inkOn = (hex: string) => {
  const n = parseInt(hex.replace('#', ''), 16)
  return 0.299 * (n >> 16) + 0.587 * ((n >> 8) & 255) + 0.114 * (n & 255) > 150 ? '#17120a' : '#ffffff'
}

/**
 * Theme-aware line chart (lightweight-charts v5) that fills its container's width.
 * `points` are { t: unix seconds, v } in ascending time order.
 * `endLabels` moves each series' last value off the price scale (where it covers the tick labels) into a gutter to its
 * right, stacked at least 17 px apart. `markers` puts a labelled arrow on a series at one of its points.
 */
export function LineChart({
  series,
  height = 280,
  format,
  minSpan,
  endLabels,
  markers,
  className,
}: {
  series: ChartSeries[]
  height?: number
  format?: (v: number) => string // price-scale / crosshair labels
  minSpan?: number // smallest visible value range (e.g. 1% of an account), centred on the data
  endLabels?: boolean
  markers?: ChartMarker[]
  className?: string
}) {
  const el = useRef<HTMLDivElement>(null)
  const chart = useRef<IChartApi | null>(null)
  const clearSeries = useRef<(() => void) | null>(null)
  const { theme } = useTheme()
  const [ends, setEnds] = useState<EndLabel[]>([])

  useEffect(() => {
    const c = createChart(el.current!, {
      autoSize: true,
      handleScroll: false, // static card charts: never hijack page scroll on touch
      handleScale: false,
      // entireTextOnly: a tick label that would be cut by the pane's top or bottom edge is skipped, not half drawn.
      rightPriceScale: { borderVisible: false, entireTextOnly: true, scaleMargins: { top: 0.12, bottom: 0.08 } },
      timeScale: { borderVisible: false, fixLeftEdge: true, fixRightEdge: true, timeVisible: true }, // intraday ranges label hours, not the day number
      grid: { vertLines: { visible: false } },
      localization: format ? { priceFormatter: format } : undefined,
    })
    chart.current = c
    return () => {
      clearSeries.current?.() // detach primitives while their chart still exists
      chart.current = null
      c.remove()
    }
  }, []) // created once; `format` is read at creation

  useEffect(() => {
    const muted = cssVar('--muted')
    chart.current?.applyOptions({
      layout: {
        background: { type: ColorType.Solid, color: 'transparent' },
        textColor: muted,
        fontFamily: "'JetBrains Mono', ui-monospace, monospace",
        fontSize: 11,
      },
      grid: { horzLines: { color: cssVar('--border') } },
      crosshair: {
        vertLine: { color: muted, labelBackgroundColor: cssVar('--ink') },
        horzLine: { color: muted, labelBackgroundColor: cssVar('--ink') },
      },
    })
  }, [theme])

  // ponytail: series are rebuilt when this signature changes, so pages can pass inline arrays;
  // an edit that keeps every series' length and endpoints identical would not redraw.
  const signature =
    series.map((s) => `${s.name}|${s.color}|${s.muted}|${s.width}|${s.points.length}|${s.points[0]?.t}|${s.points.at(-1)?.t}|${s.points.at(-1)?.v}`).join(';') +
    (markers ?? []).map((m) => `|${m.series}@${m.t}:${m.text}`).join('')

  useEffect(() => {
    const c = chart.current
    if (!c) return
    const lasts: (Point | undefined)[] = []
    const markerHandles: { detach: () => void }[] = []
    const handles = series.map((s, i) => {
      const line = c.addSeries(LineSeries, {
        color: seriesColor(series, i),
        lineWidth: series[i].width ?? (i === 0 ? 2 : 1),
        title: series.length > 1 ? '' : s.name,
        priceLineVisible: false,
        crosshairMarkerRadius: 3,
        ...(endLabels && { lastValueVisible: false }),
        ...(minSpan && { autoscaleInfoProvider: (original: () => AutoscaleInfo | null) => withMinSpan(original(), minSpan) }),
      })
      // The library throws on non-increasing times; drop any point not after the last one kept instead of crashing.
      const kept: Point[] = []
      for (const p of s.points) if (!kept.length || p.t > kept[kept.length - 1].t) kept.push(p)
      line.setData(kept.map((p) => ({ time: p.t as UTCTimestamp, value: p.v })))
      lasts.push(kept.at(-1))
      const own = (markers ?? []).filter((m) => m.series === i && kept.some((p) => p.t === m.t))
      if (own.length)
        markerHandles.push(createSeriesMarkers(
          line,
          own.map((m) => ({ time: m.t as UTCTimestamp, position: 'aboveBar' as const, shape: 'arrowDown' as const, color: seriesColor(series, i), text: m.text })),
        ))
      return line
    })
    const ts = c.timeScale()
    ts.fitContent()
    // End labels follow the price scale, which settles when the chart paints (after this effect, on a frame) and
    // whenever the visible range moves (a resize can shift it), so they are placed one frame after either.
    let frame = 0
    const place = () => {
      cancelAnimationFrame(frame)
      frame = requestAnimationFrame(() => {
        const next = handles.flatMap((h, i): EndLabel[] => {
          const last = lasts[i]
          const y = last ? h.priceToCoordinate(last.v) : null
          return last && y != null ? [{ key: `${series[i].name}|${i}`, y, text: (format ?? String)(last.v), color: seriesColor(series, i) }] : []
        })
        setEnds(stackLabels(next, LABEL_GAP, height))
      })
    }
    if (endLabels) {
      place()
      ts.subscribeVisibleLogicalRangeChange(place)
    }
    let cleared = false
    const clear = () => {
      if (cleared) return
      cleared = true
      cancelAnimationFrame(frame)
      if (endLabels) ts.unsubscribeVisibleLogicalRangeChange(place)
      markerHandles.forEach((m) => m.detach())
      if (chart.current === c) handles.forEach((h) => c.removeSeries(h))
    }
    clearSeries.current = clear
    return clear
  }, [signature, theme, minSpan, endLabels, height]) // keyed on the signature, see above

  return (
    <div className={className}>
      {series.length > 1 && (
        <ul className="mb-3 flex flex-wrap gap-x-4 gap-y-1.5">
          {series.map((s, i) => (
            <li key={s.name + i} className="flex items-center gap-1.5 text-xs text-muted">
              <span className="h-0.5 w-3.5 rounded-full" style={{ background: seriesColor(series, i) }} />
              {s.name}
            </li>
          ))}
        </ul>
      )}
      {endLabels ? (
        <div className="relative pr-14">
          <div ref={el} style={{ height }} className="w-full" />
          {ends.map((l) => (
            <span
              key={l.key}
              className="pointer-events-none absolute right-0 w-[3.25rem] rounded-[3px] text-center font-mono text-[10.5px] tabular-nums"
              style={{ top: l.y - LABEL_H / 2, height: LABEL_H, lineHeight: `${LABEL_H}px`, background: l.color, color: inkOn(l.color) }}
            >
              {l.text}
            </span>
          ))}
        </div>
      ) : (
        <div ref={el} style={{ height }} className="w-full" />
      )}
    </div>
  )
}

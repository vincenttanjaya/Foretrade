import { useEffect, useEffectEvent, useRef, useState } from 'react'
import {
  CandlestickSeries,
  ColorType,
  createChart,
  CrosshairMode,
  HistogramSeries,
  LineSeries,
  LineStyle,
  TickMarkType,
  type AutoscaleInfo,
  type IChartApi,
  type ISeriesApi,
  type LogicalRange,
  type MouseEventParams,
  type Time,
  type UTCTimestamp,
} from 'lightweight-charts'
import { Button } from '../../components/ui/Button'
import { Spinner } from '../../components/ui/Spinner'
import { Tabs } from '../../components/ui/Tabs'
import { api, ApiError } from '../../lib/api'
import { cx } from '../../lib/cx'
import { errorMessage } from '../../lib/errors'
import { fmtCompact, fmtPct } from '../../lib/format'
import { useTheme } from '../../lib/theme'
import type { Candle, ChartInterval } from '../../lib/types'
import { foldTick, klineCandle, mergeCandle, pricePrecision, sma, type ChartBars, type Tick } from './util'

/** A horizontal level drawn on the chart; `color` is a CSS variable name such as '--up'. */
export type ChartLine = { price: number; title: string; color: string; dotted?: boolean }

const INTERVALS: readonly (ChartInterval | { value: ChartInterval; label: string })[] = ['15m', '1h', '4h', '1d', { value: '1w', label: '1W' }, { value: '1M', label: '1M' }]
const SMA_COLORS = { sma20: '--accent', sma50: '#3B6FD8' } // SMA50 borrows LineChart's first extra hue
type Feed = 'loading' | 'ready' | 'live' | 'delayed' | 'error' | 'polled' | 'paused' | 'stale'

const cssVar = (name: string) =>
  name.startsWith('--') ? getComputedStyle(document.documentElement).getPropertyValue(name).trim() : name
const time = (t: number) => t as UTCTimestamp
const bar = (c: Candle) => ({ time: time(c.t), open: c.o, high: c.h, low: c.l, close: c.c })
const priceNumber = (n: number, precision: number) => n.toLocaleString('en-US', { minimumFractionDigits: precision, maximumFractionDigits: precision })
// Tokens are #rrggbb, so a 2-digit alpha suffix works.
const volBar = (c: Candle, up: string, down: string) => ({ time: time(c.t), value: c.v, color: `${c.c >= c.o ? up : down}4D` })

// Times on the axis and crosshair in the viewer's zone (the library defaults to UTC).
const toDate = (t: Time) => new Date((t as number) * 1000)
const crosshairTime = (t: Time) =>
  toDate(t).toLocaleString('en-US', { month: 'short', day: 'numeric', year: 'numeric', hour: '2-digit', minute: '2-digit', hourCycle: 'h23' })
function tickLabel(t: Time, type: TickMarkType) {
  const d = toDate(t)
  if (type === TickMarkType.Year) return String(d.getFullYear())
  if (type === TickMarkType.Month) return d.toLocaleString('en-US', { month: 'short' })
  if (type === TickMarkType.DayOfMonth) return String(d.getDate())
  return d.toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' })
}

type Handles = {
  chart: IChartApi
  candles: ISeriesApi<'Candlestick'>
  volume: ISeriesApi<'Histogram'>
  sma20: ISeriesApi<'Line'>
  sma50: ISeriesApi<'Line'>
}

/**
 * Candlestick + volume chart for one symbol. Loads the latest 500 candles, pages older history near the left edge,
 * then follows the live Binance stream (or polls a listing). Refreshes retain history already loaded here.
 */
export function CandleChart({
  symbol,
  pair,
  venue = 'binance',
  interval,
  onInterval,
  lines,
  onPrice,
  tick,
  bars: barsLabel,
  priceScale = 1,
  currency = 'USD',
}: {
  symbol: string
  pair: string
  venue?: 'binance' | 'hyperliquid' | 'yahoo'
  interval: ChartInterval
  onInterval: (iv: ChartInterval) => void
  lines: ChartLine[]
  onPrice?: (price: number) => void
  /** A quote fresher than the bars (a real-time source's): it moves the last bar, or opens the next, at its trade time. */
  tick?: Tick | null
  bars?: ChartBars
  /** Listing units per USD for incoming ticks and levels; original candle history comes from the API. */
  priceScale?: number
  currency?: string
}) {
  const el = useRef<HTMLDivElement>(null)
  const h = useRef<Handles | null>(null)
  const data = useRef<Candle[]>([])
  const colors = useRef({ up: '', down: '' })
  const levels = useRef<number[]>([]) // price-line prices, kept inside the autoscaled range
  // The quotes folded into the bars since the page opened, oldest first; a reload drops those its bars now cover.
  const ticks = useRef<{ symbol: string; list: Tick[] }>({ symbol, list: [] })
  const { theme } = useTheme()
  const [feed, setFeed] = useState<Feed>('loading')
  const [error, setError] = useState('')
  const [reload, setReload] = useState(0)
  const [dataCurrency, setDataCurrency] = useState(currency)
  const [historyState, setHistoryState] = useState<'ready' | 'loading' | 'end' | 'error'>('ready')
  const [historyError, setHistoryError] = useState('')
  const [historyStart, setHistoryStart] = useState<number | null>(null)
  const requestHistory = useRef<() => void>(() => {})
  const [overlays, setOverlays] = useState({ sma20: false, sma50: false })
  const [legend, setLegend] = useState<{ hover: Candle | null; last: Candle | null; precision: number }>({
    hover: null,
    last: null,
    precision: 2,
  })
  const emitPrice = useEffectEvent((p: number) => onPrice?.(p))
  const displayScale = currency === 'USD' ? 1 : priceScale
  const displayTick = useEffectEvent((k: Tick) => ({
    ...k,
    // Capture a real quote's listing price so replaying it never applies a later FX rate to an older trade.
    price: currency === 'USD' ? k.price : k.localPrice != null && Number.isFinite(k.localPrice) && k.localPrice > 0 ? k.localPrice : k.price * priceScale,
  }))
  const displayCandle = useEffectEvent((c: Candle) => ({ ...c, o: c.o * displayScale, h: c.h * displayScale, l: c.l * displayScale, c: c.c * displayScale }))

  const formatPrices = useEffectEvent((price: number) => {
    const s = h.current!
    const precision = pricePrecision(price)
    const priceFormat = { type: 'price', precision, minMove: 10 ** -precision } as const
    s.candles.applyOptions({ priceFormat })
    s.sma20.applyOptions({ priceFormat })
    s.sma50.applyOptions({ priceFormat })
    s.chart.applyOptions({ localization: { priceFormatter: (p: number) => `${priceNumber(p, precision)} ${currency}` } })
    setLegend((l) => l.precision === precision ? l : { ...l, precision })
  })

  // History is retained in the fetched denomination, so native daily bars keep their historical FX basis.
  const paint = useEffectEvent(() => {
    const s = h.current
    if (!s) return
    const { up, down } = colors.current
    const cs = data.current
    formatPrices(cs.at(-1)?.c ?? 0)
    s.candles.setData(cs.map(bar))
    s.volume.setData(cs.map((c) => volBar(c, up, down)))
    s.sma20.setData(sma(cs, 20).map((p) => ({ time: time(p.t), value: p.v })))
    s.sma50.setData(sma(cs, 50).map((p) => ({ time: time(p.t), value: p.v })))
  })

  const update = useEffectEvent((c: Candle) => {
    const s = h.current!
    const { up, down } = colors.current
    formatPrices(c.c)
    s.candles.update(bar(c))
    s.volume.update(volBar(c, up, down))
    for (const [series, n] of [[s.sma20, 20], [s.sma50, 50]] as const) {
      const p = sma(data.current.slice(-n), n)[0]
      if (p) series.update({ time: time(p.t), value: p.v })
    }
    setLegend((l) => ({ ...l, last: c }))
  })

  const hover = useEffectEvent((param: MouseEventParams) => {
    const s = h.current!
    const c = param.seriesData.get(s.candles)
    const v = param.seriesData.get(s.volume)
    // Crosshair and retained history already use the selected denomination.
    const candle = c && 'open' in c
      ? { t: param.time as number, o: c.open, h: c.high, l: c.low, c: c.close, v: v && 'value' in v ? v.value : 0 }
      : null
    setLegend((l) => ({ ...l, hover: candle }))
  })

  useEffect(() => {
    const chart = createChart(el.current!, {
      autoSize: true,
      rightPriceScale: { borderVisible: false, scaleMargins: { top: 0.1, bottom: 0.24 } },
      timeScale: { borderVisible: false, timeVisible: true, secondsVisible: false, rightOffset: 6, tickMarkFormatter: tickLabel },
      localization: { timeFormatter: crosshairTime },
      crosshair: { mode: CrosshairMode.Normal },
      grid: { vertLines: { visible: false } },
      handleScroll: { vertTouchDrag: false }, // a vertical swipe on a phone scrolls the page, not the chart
    })
    const line = { lineWidth: 1, priceLineVisible: false, lastValueVisible: false, crosshairMarkerVisible: false, visible: false } as const
    const s: Handles = {
      chart,
      candles: chart.addSeries(CandlestickSeries, {
        borderVisible: false,
        autoscaleInfoProvider: (base: () => AutoscaleInfo | null) => {
          const info = base()
          const r = info?.priceRange
          if (!r || !levels.current.length) return info
          const all = [r.minValue, r.maxValue, ...levels.current]
          return { ...info, priceRange: { minValue: Math.min(...all), maxValue: Math.max(...all) } }
        },
      }),
      volume: chart.addSeries(HistogramSeries, {
        priceScaleId: 'volume',
        priceFormat: { type: 'volume' },
        lastValueVisible: false,
        priceLineVisible: false,
      }),
      sma20: chart.addSeries(LineSeries, line),
      sma50: chart.addSeries(LineSeries, line),
    }
    chart.priceScale('volume').applyOptions({ scaleMargins: { top: 0.82, bottom: 0 } })
    chart.subscribeCrosshairMove((param) => hover(param))
    h.current = s
    return () => {
      h.current = null
      chart.remove()
    }
  }, [])

  useEffect(() => {
    const s = h.current!
    const [up, down, muted, border, ink] = ['--up', '--down', '--muted', '--border', '--ink'].map(cssVar)
    colors.current = { up, down }
    s.chart.applyOptions({
      layout: {
        background: { type: ColorType.Solid, color: 'transparent' },
        textColor: muted,
        fontFamily: "'JetBrains Mono', ui-monospace, monospace",
        fontSize: 11,
      },
      grid: { horzLines: { color: border } },
      crosshair: {
        vertLine: { color: muted, labelBackgroundColor: ink },
        horzLine: { color: muted, labelBackgroundColor: ink },
      },
    })
    s.candles.applyOptions({ upColor: up, downColor: down, wickUpColor: up, wickDownColor: down })
    s.sma20.applyOptions({ color: cssVar(SMA_COLORS.sma20) })
    s.sma50.applyOptions({ color: cssVar(SMA_COLORS.sma50) })
    paint() // volume bars carry their colour per point
  }, [theme, currency])

  useEffect(() => {
    const s = h.current!
    s.sma20.applyOptions({ visible: overlays.sma20 })
    s.sma50.applyOptions({ visible: overlays.sma50 })
  }, [overlays])

  // Load + live stream for the current symbol/interval.
  useEffect(() => {
    const s = h.current!
    const ctrl = new AbortController()
    let disposed = false
    let loading = false
    let historyLoading = false
    let historyEnded = false
    let historyFailed = false
    let ws: WebSocket | undefined
    let retry = 0
    let retryTimer: ReturnType<typeof setTimeout> | undefined
    let pollTimer: ReturnType<typeof setInterval> | undefined

    data.current = []
    if (ticks.current.symbol !== symbol) ticks.current = { symbol, list: [] }
    paint()
    setFeed('loading')
    setError('')
    setHistoryState('ready')
    setHistoryError('')
    setHistoryStart(null)
    setLegend((l) => ({ ...l, hover: null, last: null }))

    const loadEarlier = async () => {
      if (disposed || document.hidden || historyLoading || historyEnded || !data.current.length) return
      historyLoading = true
      historyFailed = false
      setHistoryState('loading')
      setHistoryError('')
      const before = data.current[0].t
      try {
        const { candles } = await api.candles(symbol, interval, 500, ctrl.signal, currency === 'USD' ? undefined : 'original', before)
        if (disposed) return
        const first = data.current[0].t
        const unique = new Map<number, Candle>()
        for (const c of candles ?? []) {
          if (c.t > 0 && c.t < before && c.t < first && [c.t, c.o, c.h, c.l, c.c, c.v].every(Number.isFinite)) unique.set(c.t, c)
        }
        const earlier = [...unique.values()].sort((a, b) => a.t - b.t)
        if (!earlier.length) {
          historyEnded = true
          setHistoryState('end')
          return
        }
        // Read the current window and data now: the user and the live feed may have moved while awaiting history.
        const scale = s.chart.timeScale()
        const visible = scale.getVisibleLogicalRange()
        data.current = [...earlier, ...data.current]
        paint()
        if (visible) scale.setVisibleLogicalRange({ from: visible.from + earlier.length, to: visible.to + earlier.length })
        setHistoryStart(data.current[0].t)
        setHistoryState('ready')
      } catch (err) {
        if (disposed) return
        historyFailed = true
        setHistoryState('error')
        setHistoryError(errorMessage(err, 'Could not load earlier candles.'))
      } finally {
        historyLoading = false
      }
    }
    const onRange = (range: LogicalRange | null) => {
      if (range && range.from < 50 && !historyFailed) void loadEarlier()
    }
    requestHistory.current = () => void loadEarlier()
    s.chart.timeScale().subscribeVisibleLogicalRangeChange(onRange)

    const load = async (first: boolean) => {
      if (disposed || loading) return
      loading = true
      try {
        const { candles } = await (currency === 'USD'
          ? api.candles(symbol, interval, 500, ctrl.signal)
          : api.candles(symbol, interval, 500, ctrl.signal, 'original'))
        if (disposed) return
        // The library throws on non-increasing times; keep only strictly increasing bars.
        const cs: Candle[] = []
        for (const c of candles ?? []) if (!cs.length || c.t > cs[cs.length - 1].t) cs.push(c)
        if (!cs.length) throw new ApiError(502, 'upstream', 'No retained candles are available for this instrument.')
        // Quotes newer than the bars' last open time are not in them yet (the bars are delayed): fold them back in.
        const kept = ticks.current.list.filter((k) => k.t >= cs[cs.length - 1].t)
        ticks.current.list = kept
        for (const k of kept) foldTick(cs, displayTick(k), interval)
        const firstTime = cs[0].t
        const lastTime = cs.at(-1)!.t
        // A latest-page refresh must not erase older pages or a live bar that arrived after the snapshot.
        data.current = [...data.current.filter((c) => c.t < firstTime), ...cs, ...data.current.filter((c) => c.t > lastTime)]
        setHistoryStart(data.current[0].t)
        setDataCurrency(currency)
        const last = data.current.at(-1) ?? null
        paint()
        if (first) s.chart.timeScale().setVisibleLogicalRange({ from: cs.length - 120, to: cs.length + 6 })
        setLegend((l) => ({ ...l, last }))
        if (last && venue === 'binance') emitPrice(last.c / displayScale)
      } finally {
        loading = false
      }
    }

    const startPolling = () => {
      setFeed('delayed')
      pollTimer ??= setInterval(() => load(false).catch(() => {}), 30_000)
    }

    const connect = () => {
      try {
        ws = new WebSocket(`wss://data-stream.binance.vision/ws/${pair.toLowerCase()}@kline_${interval}`)
      } catch {
        startPolling() // blocked outright (CSP, policy): polling only
        return
      }
      ws.onopen = () => {
        if (retry > 0) load(false).catch(() => {}) // fill bars missed while disconnected
      }
      ws.onmessage = (e) => {
        const raw = typeof e.data === 'string' ? klineCandle(e.data) : null
        const c = raw && displayCandle(raw)
        if (disposed || !c || !mergeCandle(data.current, c)) return
        retry = 0
        clearInterval(pollTimer)
        pollTimer = undefined
        setFeed('live')
        update(c)
        emitPrice(raw!.c)
      }
      ws.onclose = () => {
        if (disposed) return
        startPolling()
        retryTimer = setTimeout(connect, Math.min(30_000, 1000 * 2 ** retry++))
      }
    }

    // HIP3 has no Binance stream. Quote marks come from /market/assets;
    // these closes update only the chart. One effect generation owns the
    // request, timer and visibility listener, including late responses.
    const refreshHip3 = async () => {
      if (disposed || document.hidden || loading) return
      try {
        await load(data.current.length === 0)
        if (disposed) return
        setError('')
        setFeed(document.hidden ? 'paused' : 'polled')
      } catch (err) {
        if (disposed) return
        setFeed(data.current.length ? 'stale' : 'error')
        setError(errorMessage(err, 'Could not refresh the chart.'))
      }
    }
    const visibility = () => {
      if (document.hidden) setFeed('paused')
      else void refreshHip3()
    }
    if (venue !== 'binance') { // Hyperliquid perps and Yahoo quote feeds: polled API candles
      document.addEventListener('visibilitychange', visibility)
      pollTimer = setInterval(() => void refreshHip3(), 30_000)
      if (document.hidden) setFeed('paused')
      else void refreshHip3()
    } else {
      load(true)
        .then(() => {
          if (disposed) return
          setFeed('ready')
          connect()
        })
        .catch((err) => {
          if (disposed) return
          setFeed('error')
          setError(errorMessage(err, 'Could not load the chart.'))
        })
    }

    return () => {
      disposed = true
      ctrl.abort()
      requestHistory.current = () => {}
      s.chart.timeScale().unsubscribeVisibleLogicalRangeChange(onRange)
      clearTimeout(retryTimer)
      clearInterval(pollTimer)
      document.removeEventListener('visibilitychange', visibility)
      if (ws) {
        ws.onclose = ws.onmessage = ws.onopen = null
        ws.close()
      }
    }
  }, [symbol, pair, venue, interval, reload, currency])

  // A new quote: kept for the next reload, and folded into the bars on screen like a socket's kline.
  const foldNew = useEffectEvent((k: Tick) => {
    const kept = ticks.current.list
    const prev = kept.at(-1)
    if (!(k.price > 0) || (prev && (k.t < prev.t || (k.t === prev.t && k.price === prev.price && k.localPrice === prev.localPrice)))) return
    kept.push(k)
    if (kept.length > 2000) kept.splice(0, kept.length - 2000)
    const s = h.current
    if (!s || !foldTick(data.current, displayTick(k), interval)) return
    const c = data.current[data.current.length - 1]
    update(c)
  })
  useEffect(() => {
    if (tick) foldNew(tick)
  }, [tick?.t, tick?.price, tick?.localPrice]) // keyed on the quote's values: a caller may pass a fresh object each render

  // Plan / position levels. Keyed on their values so callers can pass a fresh array each render.
  const lineKey = JSON.stringify(lines)
  useEffect(() => {
    const s = h.current!
    const valid = lines.filter((l) => Number.isFinite(l.price) && l.price > 0)
    levels.current = valid.map((l) => l.price * priceScale)
    const made = valid.map((l) =>
      s.candles.createPriceLine({
        price: l.price * priceScale,
        title: l.title,
        color: cssVar(l.color),
        lineWidth: 1,
        lineStyle: l.dotted ? LineStyle.Dotted : LineStyle.Dashed,
        axisLabelVisible: true,
      }),
    )
    return () => {
      levels.current = []
      if (h.current === s) made.forEach((pl) => s.candles.removePriceLine(pl))
    }
  }, [lineKey, theme, priceScale]) // keyed on the values, see above

  const changingCurrency = dataCurrency !== currency
  const shown = changingCurrency ? null : legend.hover ?? legend.last
  const num = (n: number) => priceNumber(n, legend.precision)

  return (
    <div>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2 border-b border-border px-3 py-2.5 sm:px-4">
        <Tabs value={interval} onChange={onInterval} items={INTERVALS} size="sm" aria-label="Chart interval" />
        <div className="flex items-center gap-1" role="group" aria-label="Overlays">
          {(['sma20', 'sma50'] as const).map((k) => (
            <button
              key={k}
              type="button"
              aria-pressed={overlays[k]}
              onClick={() => setOverlays((o) => ({ ...o, [k]: !o[k] }))}
              className={cx(
                'inline-flex h-7 items-center gap-1.5 rounded-md px-2 font-mono text-[11px] tracking-wide transition-colors',
                overlays[k] ? 'bg-surface-2 text-text' : 'text-muted hover:bg-surface-2 hover:text-text',
              )}
            >
              <span
                aria-hidden="true"
                className={cx('h-0.5 w-3 rounded-full transition-opacity', !overlays[k] && 'opacity-40')}
                style={{ background: SMA_COLORS[k].startsWith('--') ? `var(${SMA_COLORS[k]})` : SMA_COLORS[k] }}
              />
              {k.toUpperCase().replace('SMA', 'SMA ')}
            </button>
          ))}
        </div>
        <span className="font-mono text-[10.5px] text-muted" title={`Chart prices in ${currency}`}>{currency}</span>
        <FeedBadge feed={feed} venue={venue} bars={barsLabel} currency={currency} />
      </div>

      {feed === 'stale' && <p role="status" className="px-4 py-2 text-[12px] text-muted">{error} Showing the last measured candles; retrying every 30 seconds.</p>}
      <div className="relative h-[320px] sm:h-[400px] xl:h-[460px]">
        {shown && (
          <div className="pointer-events-none absolute top-2 left-3 z-10 flex flex-wrap gap-x-2.5 font-mono text-[11px] text-muted tabular-nums">
            {(['o', 'h', 'l', 'c'] as const).map((k) => (
              <span key={k} className={k === 'c' ? undefined : 'hidden sm:inline'}>
                {k.toUpperCase()} <span className="text-text">{num(shown[k])}</span>
              </span>
            ))}
            <span className={shown.c >= shown.o ? 'text-up' : 'text-down'}>
              {fmtPct(((shown.c - shown.o) / shown.o) * 100, { sign: true })}
            </span>
            <span className="hidden sm:inline">
              V <span className="text-text">{fmtCompact(shown.v)}</span>
            </span>
          </div>
        )}
        <div ref={el} className="absolute inset-0" style={{ visibility: changingCurrency ? 'hidden' : undefined }} />
        {feed === 'loading' && (
          <div className="absolute inset-0 z-20 grid place-items-center bg-surface/70">
            <Spinner className="size-5 text-muted" label={`Loading ${symbol} ${interval} candles`} />
          </div>
        )}
        {feed === 'error' && (
          <div role="alert" className="absolute inset-0 z-20 grid place-items-center bg-surface/90 px-6 text-center">
            <div>
              <p className="font-display text-lg text-text">The chart didn't load.</p>
              <p className="mt-1 max-w-xs text-sm text-muted">{error}</p>
              <Button variant="secondary" size="sm" className="mt-4" onClick={() => setReload((n) => n + 1)}>
                Try again
              </Button>
            </div>
          </div>
        )}
      </div>
      <div className="flex flex-wrap items-center justify-between gap-2 border-t border-border px-3 py-2 sm:px-4">
        <span aria-live="polite" className="text-[11.5px] text-muted">
          {historyState === 'error' ? historyError : historyState === 'end' ? 'No earlier candles available from this feed.' : historyStart != null ? `History from ${new Date(historyStart * 1000).toLocaleDateString('en-US', { month: 'short', day: 'numeric', year: 'numeric' })} · Pan left for more` : 'Earlier history loads as you pan left.'}
        </span>
        <Button variant="secondary" size="sm" disabled={historyState === 'loading' || historyState === 'end' || historyStart == null || changingCurrency} onClick={() => requestHistory.current()}>
          {historyState === 'loading' ? 'Loading earlier…' : historyState === 'error' ? 'Try loading earlier again' : 'Load earlier'}
        </Button>
      </div>
    </div>
  )
}

function FeedBadge({ feed, venue, bars, currency }: { feed: Feed; venue: string; bars?: ChartBars; currency: string }) {
  // A quote feed's bars say whose they are and how delayed, and what moves the last one; paused and stale still say so.
  if (bars && (feed === 'polled' || feed === 'loading' || feed === 'ready'))
    return (
      <span className="ml-auto flex flex-col items-end font-mono text-[10.5px] tracking-[0.12em] text-muted uppercase sm:flex-row sm:gap-1.5" title={bars.title.replace(/\bUSD\b/g, currency)}>
        <span>{bars.bars}</span>
        {bars.last && (
          <>
            <span aria-hidden="true" className="hidden sm:inline">
              ·
            </span>
            <span>{bars.last}</span>
          </>
        )}
      </span>
    )
  if (feed === 'polled' || feed === 'paused' || feed === 'stale')
    return <span className="ml-auto font-mono text-[10.5px] tracking-[0.12em] text-muted uppercase" title={venue === 'yahoo' ? `Yahoo candles in ${currency}; the candle close can differ from the latest (delayed) quote` : `Hyperliquid venue candles in ${currency}; the candle close is distinct from the live order mark`}>{feed === 'polled' ? 'Polled · 30s' : feed === 'paused' ? 'Paused' : 'Stale · retrying'}</span>
  if (feed === 'live')
    return (
      <span className="ml-auto inline-flex items-center gap-1.5 font-mono text-[10.5px] tracking-[0.12em] text-muted uppercase">
        <span className="relative flex size-1.5">
          <span className="absolute inline-flex size-full animate-ping rounded-full bg-up opacity-60" />
          <span className="relative inline-flex size-1.5 rounded-full bg-up" />
        </span>
        Live
      </span>
    )
  if (feed === 'delayed')
    return (
      <span className="ml-auto font-mono text-[10.5px] tracking-[0.12em] text-muted uppercase" title="Live stream unavailable; refreshing every 30 seconds">
        Delayed · 30s
      </span>
    )
  return <span className="ml-auto" />
}

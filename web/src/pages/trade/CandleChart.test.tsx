import { act, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError } from '../../lib/api'
import { ThemeProvider } from '../../lib/theme'
import type { Candle, Interval } from '../../lib/types'
import { charts } from '../../test/chart'
import { FakeWebSocket } from '../../test/socket'
import { CandleChart } from './CandleChart'
import type { Tick } from './util'

const bars = (price: number): Candle[] => [0, 1].map((i) => ({ t: 1_790_928_000 + i * 3600, o: price, h: price + 1, l: price - 1, c: price, v: 10 }))
const deferred = <T,>() => { let resolve!: (v: T) => void; return { promise: new Promise<T>((r) => { resolve = r }), resolve } }
const flush = () => act(async () => { await Promise.resolve(); await Promise.resolve() })
const chart = (symbol = 'AAPL', venue: 'binance' | 'hyperliquid' = 'hyperliquid', interval: Interval = '1h', onPrice = vi.fn()) =>
  <ThemeProvider><CandleChart symbol={symbol} pair={venue === 'binance' ? `${symbol}USDT` : `xyz:${symbol}`} venue={venue} interval={interval} onInterval={() => {}} lines={[]} onPrice={onPrice} /></ThemeProvider>
const plotted = () => charts.at(-1)!.series.find((s) => s.type === 'Candlestick')!.setData.mock.calls.at(-1)?.[0]

beforeEach(() => {
  vi.useFakeTimers()
  charts.length = 0
  Object.defineProperty(document, 'hidden', { configurable: true, value: false })
  vi.spyOn(api, 'candles').mockResolvedValue({ candles: bars(180) })
})
afterEach(() => { Object.defineProperty(document, 'hidden', { configurable: true, value: false }); vi.useRealTimers() })

describe('older chart history', () => {
  const T0 = 1_790_928_000
  const candleSeries = () => charts.at(-1)!.series.find((s) => s.type === 'Candlestick')!
  const rangeChanged = () => charts.at(-1)!.timeScale().subscribeVisibleLogicalRangeChange.mock.calls.at(-1)![0] as (range: { from: number; to: number } | null) => void

  it('pages before the left edge once, preserves live updates and anchors the viewport at response time', async () => {
    const price = vi.fn()
    const pending = deferred<{ candles: Candle[] }>()
    render(chart('BTC', 'binance', '1h', price)); await flush()
    const scale = charts.at(-1)!.timeScale()
    expect(scale.subscribeVisibleLogicalRangeChange).toHaveBeenCalledTimes(1)
    vi.mocked(api.candles).mockReturnValueOnce(pending.promise)
    act(() => { rangeChanged()({ from: 10, to: 110 }); rangeChanged()({ from: 5, to: 105 }) })
    expect(api.candles).toHaveBeenCalledTimes(2)
    expect(api.candles).toHaveBeenLastCalledWith('BTC', '1h', 500, expect.any(AbortSignal), undefined, T0)
    const ws = FakeWebSocket.instances[0]
    act(() => ws.onmessage!({ data: JSON.stringify({ k: { t: (T0 + 3600) * 1000, o: '180', h: '185', l: '179', c: '184', v: '12' } }) }))
    scale.getVisibleLogicalRange.mockReturnValue({ from: -11, to: 20 }) // user moved while the page loaded
    await act(async () => pending.resolve({ candles: [
      { t: T0 - 3600, o: 177, h: 180, l: 176, c: 178, v: 3 },
      { t: T0, o: 1, h: 1, l: 1, c: 1, v: 0 }, // overlap must not overwrite the current page
      { t: T0 - 7200, o: 176, h: 179, l: 175, c: 177, v: 2 },
      { t: T0 - 3600, o: 177, h: 180, l: 176, c: 178, v: 3 },
    ] }))
    expect(plotted()).toEqual([
      { time: T0 - 7200, open: 176, high: 179, low: 175, close: 177 },
      { time: T0 - 3600, open: 177, high: 180, low: 176, close: 178 },
      { time: T0, open: 180, high: 181, low: 179, close: 180 },
      { time: T0 + 3600, open: 180, high: 185, low: 179, close: 184 },
    ])
    expect(scale.setVisibleLogicalRange).toHaveBeenLastCalledWith({ from: -9, to: 22 })
    expect(price.mock.calls).toEqual([[180], [184]]) // history must never become the order price
    expect(charts.at(-1)!.series.find((s) => s.type === 'Histogram')!.setData.mock.calls.at(-1)![0]).toEqual([
      expect.objectContaining({ time: T0 - 7200, value: 2 }), expect.objectContaining({ time: T0 - 3600, value: 3 }),
      expect.objectContaining({ time: T0, value: 10 }), expect.objectContaining({ time: T0 + 3600, value: 12 }),
    ])
  })

  it('retains loaded history when the current page refreshes', async () => {
    render(chart()); await flush()
    vi.mocked(api.candles).mockResolvedValueOnce({ candles: [{ t: T0 - 3600, o: 175, h: 177, l: 174, c: 176, v: 2 }] })
    fireEvent.click(screen.getByRole('button', { name: 'Load earlier' })); await flush()
    expect(candleSeries().setData.mock.calls.at(-1)![0]).toHaveLength(3)
    vi.mocked(api.candles).mockResolvedValue({ candles: bars(181) })
    await act(() => vi.advanceTimersByTimeAsync(30_000))
    expect(plotted()).toEqual([
      { time: T0 - 3600, open: 175, high: 177, low: 174, close: 176 },
      { time: T0, open: 181, high: 182, low: 180, close: 181 },
      { time: T0 + 3600, open: 181, high: 182, low: 180, close: 181 },
    ])
  })

  it('keeps the chart usable on a history failure, retries explicitly, and stops at the feed boundary', async () => {
    render(chart()); await flush()
    vi.mocked(api.candles).mockRejectedValueOnce(new ApiError(502, 'upstream', 'Earlier history unavailable'))
    fireEvent.click(screen.getByRole('button', { name: 'Load earlier' })); await flush()
    expect(screen.getByText('Polled · 30s')).toBeInTheDocument()
    expect(plotted()).toHaveLength(2)
    act(() => rangeChanged()({ from: 0, to: 100 }))
    expect(api.candles).toHaveBeenCalledTimes(2) // no automatic error loop
    vi.mocked(api.candles).mockResolvedValueOnce({ candles: [] })
    fireEvent.click(screen.getByRole('button', { name: 'Try loading earlier again' })); await flush()
    expect(screen.getByText('No earlier candles available from this feed.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Load earlier' })).toBeDisabled()
    act(() => rangeChanged()({ from: -50, to: 50 }))
    expect(api.candles).toHaveBeenCalledTimes(3)
  })

  it('keeps native history requests in original currency and discards them after a currency switch', async () => {
    const display = (currency: string) => <ThemeProvider><CandleChart symbol="TOKYOELEC" pair="8035.T" venue="yahoo" interval="1h" onInterval={() => {}} lines={[]} currency={currency} priceScale={currency === 'USD' ? 1 : 100} /></ThemeProvider>
    const view = render(display('JPY')); await flush()
    const pending = deferred<{ candles: Candle[] }>()
    vi.mocked(api.candles).mockReturnValueOnce(pending.promise)
    fireEvent.click(screen.getByRole('button', { name: 'Load earlier' })); await flush()
    expect(api.candles).toHaveBeenLastCalledWith('TOKYOELEC', '1h', 500, expect.any(AbortSignal), 'original', T0)
    const signal = vi.mocked(api.candles).mock.calls.at(-1)![3]!
    const oldScale = charts.at(-1)!.timeScale()
    view.rerender(display('USD')); await flush()
    expect(signal.aborted).toBe(true)
    await act(async () => pending.resolve({ candles: [{ t: T0 - 3600, o: 999, h: 999, l: 999, c: 999, v: 1 }] }))
    expect(plotted()).toHaveLength(2)
    expect(plotted()?.[0]).toEqual({ time: T0, open: 180, high: 181, low: 179, close: 180 })
    expect(oldScale.unsubscribeVisibleLogicalRangeChange).toHaveBeenCalled()
    expect(screen.getByRole('button', { name: 'Load earlier' })).toBeEnabled()
  })
})

describe('calendar chart candles', () => {
  it.each([
    ['1w', '2024-02-19', '2024-02-25T23:59:00Z', '2024-02-26', '2024-02-12'],
    ['1M', '2024-02-01', '2024-02-29T23:59:00Z', '2024-03-01', '2024-01-01'],
  ] as const)('keeps %s native candles and live quotes aligned while paging history', async (interval, startDate, insideDate, nextDate, priorDate) => {
    const seconds = (iso: string) => Date.parse(iso.length === 10 ? `${iso}T00:00:00Z` : iso) / 1000
    const start = seconds(startDate), next = seconds(nextDate), prior = seconds(priorDate)
    vi.mocked(api.candles).mockResolvedValue({ candles: [{ t: start, o: 100, h: 101, l: 99, c: 100, v: 10 }] })
    const display = (tick: Tick | null) => <ThemeProvider><CandleChart symbol="6501.T" pair="6501.T" venue="yahoo" interval={interval} onInterval={() => {}} lines={[]} currency="JPY" priceScale={100} tick={tick} /></ThemeProvider>
    const view = render(display(null)); await flush()
    expect(api.candles).toHaveBeenCalledWith('6501.T', interval, 500, expect.any(AbortSignal), 'original')
    const series = charts.at(-1)!.series.find((s) => s.type === 'Candlestick')!
    view.rerender(display({ t: seconds(insideDate), price: 1.05, localPrice: 105 }))
    expect(series.update).toHaveBeenLastCalledWith({ time: start, open: 100, high: 105, low: 99, close: 105 })
    view.rerender(display({ t: next + 3600, price: 1.08, localPrice: 108 }))
    expect(series.update).toHaveBeenLastCalledWith({ time: next, open: 108, high: 108, low: 108, close: 108 })
    await act(() => vi.advanceTimersByTimeAsync(30_000))
    expect(plotted()).toEqual([
      { time: start, open: 100, high: 105, low: 99, close: 105 },
      { time: next, open: 108, high: 108, low: 108, close: 108 },
    ])
    vi.mocked(api.candles).mockResolvedValueOnce({ candles: [{ t: prior, o: 90, h: 95, l: 89, c: 94, v: 8 }] })
    fireEvent.click(screen.getByRole('button', { name: 'Load earlier' })); await flush()
    expect(api.candles).toHaveBeenLastCalledWith('6501.T', interval, 500, expect.any(AbortSignal), 'original', start)
    expect(plotted()).toHaveLength(3)
    expect(plotted()?.[0]).toEqual({ time: prior, open: 90, high: 95, low: 89, close: 94 })
    expect(plotted()?.at(-1)).toEqual({ time: next, open: 108, high: 108, low: 108, close: 108 })
  })
})

describe('venue-aware chart ownership', () => {
  it('polls HIP3 candles without a Binance socket or emitting a candle close as an order mark', async () => {
    const price = vi.fn()
    render(chart('AAPL', 'hyperliquid', '1h', price)); await flush()
    expect(FakeWebSocket.instances).toHaveLength(0)
    expect(price).not.toHaveBeenCalled()
    expect(plotted()).toEqual([{ time: 1_790_928_000, open: 180, high: 181, low: 179, close: 180 }, { time: 1_790_931_600, open: 180, high: 181, low: 179, close: 180 }])
    expect(screen.getByText('Polled · 30s')).toBeInTheDocument()
    await act(() => vi.advanceTimersByTimeAsync(30_000))
    expect(api.candles).toHaveBeenCalledTimes(2)
    expect(api.candles).toHaveBeenLastCalledWith('AAPL', '1h', 500, expect.any(AbortSignal))
  })

  it('admits at most one candle request while a slow refresh is in flight', async () => {
    const pending = deferred<{ candles: Candle[] }>()
    vi.mocked(api.candles).mockReturnValueOnce(pending.promise)
    render(chart()); await flush()
    await act(() => vi.advanceTimersByTimeAsync(90_000))
    expect(api.candles).toHaveBeenCalledTimes(1)
    await act(() => pending.resolve({ candles: bars(180) }))
    await act(() => vi.advanceTimersByTimeAsync(30_000))
    expect(api.candles).toHaveBeenCalledTimes(2)
  })

  it('pauses while hidden and admits one bounded refresh on becoming visible', async () => {
    render(chart()); await flush()
    Object.defineProperty(document, 'hidden', { configurable: true, value: true })
    fireEvent(document, new Event('visibilitychange'))
    await act(() => vi.advanceTimersByTimeAsync(90_000))
    expect(api.candles).toHaveBeenCalledTimes(1)
    expect(screen.getByText('Paused')).toBeInTheDocument()
    const pending = deferred<{ candles: Candle[] }>()
    vi.mocked(api.candles).mockReturnValueOnce(pending.promise)
    Object.defineProperty(document, 'hidden', { configurable: true, value: false })
    fireEvent(document, new Event('visibilitychange')); fireEvent(document, new Event('visibilitychange')); await flush()
    expect(api.candles).toHaveBeenCalledTimes(2)
    await act(() => vi.advanceTimersByTimeAsync(60_000))
    expect(api.candles).toHaveBeenCalledTimes(2)
    await act(() => pending.resolve({ candles: bars(181) }))
  })

  it('does not fetch initially hidden HIP3 charts and cleans up when only the venue changes', async () => {
    Object.defineProperty(document, 'hidden', { configurable: true, value: true })
    const view = render(chart()); await flush()
    await act(() => vi.advanceTimersByTimeAsync(60_000))
    expect(api.candles).not.toHaveBeenCalled()
    Object.defineProperty(document, 'hidden', { configurable: true, value: false })
    fireEvent(document, new Event('visibilitychange')); await flush()
    const signal = vi.mocked(api.candles).mock.calls[0][3]!
    view.rerender(chart('AAPL', 'binance')); await flush()
    expect(signal.aborted).toBe(true)
    expect(FakeWebSocket.instances).toHaveLength(1)
    await act(() => vi.advanceTimersByTimeAsync(60_000))
    expect(api.candles).toHaveBeenCalledTimes(2)
  })

  it('aborts the old symbol generation and rejects late HIP3 data after switching to crypto', async () => {
    const old = deferred<{ candles: Candle[] }>()
    vi.mocked(api.candles).mockReturnValueOnce(old.promise).mockResolvedValue({ candles: bars(84_000) })
    const price = vi.fn(); const view = render(chart('AAPL', 'hyperliquid', '1h', price)); await flush()
    const signal = vi.mocked(api.candles).mock.calls[0][3]!
    view.rerender(chart('BTC', 'binance', '1h', price)); await flush()
    expect(signal.aborted).toBe(true)
    expect(FakeWebSocket.instances.map((s) => s.url)).toEqual(['wss://data-stream.binance.vision/ws/btcusdt@kline_1h'])
    await act(() => old.resolve({ candles: bars(180) }))
    expect(plotted()).toEqual(expect.arrayContaining([expect.objectContaining({ close: 84_000 })]))
    expect(price.mock.calls).toEqual([[84_000]])
    await act(() => vi.advanceTimersByTimeAsync(60_000))
    expect(api.candles).toHaveBeenCalledTimes(2)
  })

  it('aborts and stops the old interval and unmounted HIP3 poller', async () => {
    const view = render(chart()); await flush()
    const first = vi.mocked(api.candles).mock.calls[0][3]!
    view.rerender(chart('AAPL', 'hyperliquid', '4h')); await flush()
    expect(first.aborted).toBe(true)
    expect(api.candles).toHaveBeenLastCalledWith('AAPL', '4h', 500, expect.any(AbortSignal))
    const second = vi.mocked(api.candles).mock.calls[1][3]!
    view.unmount(); await act(() => vi.advanceTimersByTimeAsync(90_000))
    expect(second.aborted).toBe(true)
    expect(api.candles).toHaveBeenCalledTimes(2)
  })

  it('retains measured candles as stale after a failed refresh, without emitting a price', async () => {
    const price = vi.fn(); render(chart('AAPL', 'hyperliquid', '1h', price)); await flush()
    vi.mocked(api.candles).mockRejectedValueOnce(new ApiError(502, 'upstream', 'Perp history unavailable'))
    await act(() => vi.advanceTimersByTimeAsync(30_000))
    expect(screen.getByText('Stale · retrying')).toBeInTheDocument()
    expect(screen.getByRole('status')).toHaveTextContent('Perp history unavailable')
    expect(plotted()).toEqual(expect.arrayContaining([expect.objectContaining({ close: 180 })]))
    expect(price).not.toHaveBeenCalled()
  })

  it('shows unavailable history when the provider returns no bars', async () => {
    vi.mocked(api.candles).mockResolvedValueOnce({ candles: [] })
    render(chart()); await flush()
    expect(screen.getByRole('alert')).toHaveTextContent('No retained candles are available')
    expect(FakeWebSocket.instances).toHaveLength(0)
  })

  it('preserves the crypto socket and its live close callback', async () => {
    const price = vi.fn(); render(chart('BTC', 'binance', '1h', price)); await flush()
    const ws = FakeWebSocket.instances[0]
    expect(ws.url).toBe('wss://data-stream.binance.vision/ws/btcusdt@kline_1h')
    act(() => ws.onmessage!({ data: JSON.stringify({ k: { t: 1_790_931_600_000, o: '180', h: '185', l: '179', c: '184', v: '12' } }) }))
    expect(price).toHaveBeenLastCalledWith(184)
    expect(screen.getByText('Live')).toBeInTheDocument()
  })
})

describe('a quote feed whose real-time quote moves the delayed bars', () => {
  const T0 = 1_790_928_000 // the bars' first open (hourly)
  const feedChart = (tick: { t: number; price: number } | null, interval: Interval = '1h') => (
    <ThemeProvider>
      <CandleChart symbol="SAMBIO" pair="207940.KS" venue="yahoo" interval={interval} onInterval={() => {}} lines={[]} tick={tick}
        bars={{ bars: 'Yahoo bars · 20 min delayed', last: 'last bar real-time', title: 'Bars are Yahoo’s, about 20 min behind the exchange.' }} />
    </ThemeProvider>
  )
  const candles = () => charts.at(-1)!.series.find((s) => s.type === 'Candlestick')!

  it('labels the bars as Yahoo’s delayed bars and moves the last one with each new quote, as the crypto socket does', async () => {
    const view = render(feedChart(null)); await flush()
    expect(screen.getByText('Yahoo bars · 20 min delayed')).toBeInTheDocument()
    expect(screen.getByText('Yahoo bars · 20 min delayed').parentElement).toHaveAttribute('title', 'Bars are Yahoo’s, about 20 min behind the exchange.')
    expect(screen.queryByText('Polled · 30s')).toBeNull()
    // Inside the last (delayed) bar: its close moves and its high stretches.
    view.rerender(feedChart({ t: T0 + 3600 + 1500, price: 183 }))
    expect(candles().update).toHaveBeenLastCalledWith({ time: T0 + 3600, open: 180, high: 183, low: 179, close: 183 })
    // Past it: the next bar opens at the quote, so the real-time price never sits beside a stale last bar.
    view.rerender(feedChart({ t: T0 + 7200 + 300, price: 184 }))
    expect(candles().update).toHaveBeenLastCalledWith({ time: T0 + 7200, open: 184, high: 184, low: 184, close: 184 })
    view.rerender(feedChart({ t: T0 + 7200 + 300, price: 184 })) // the same quote again: nothing to redraw
    expect(candles().update).toHaveBeenCalledTimes(2)
    // The 30 s reload brings the same delayed bars: the quotes since are folded back in, not lost.
    await act(() => vi.advanceTimersByTimeAsync(30_000))
    expect(api.candles).toHaveBeenCalledTimes(2)
    expect(plotted()).toEqual([
      { time: T0, open: 180, high: 181, low: 179, close: 180 },
      { time: T0 + 3600, open: 180, high: 183, low: 179, close: 183 },
      { time: T0 + 7200, open: 184, high: 184, low: 184, close: 184 },
    ])
    expect(screen.getByText('last bar real-time')).toBeInTheDocument()
  })

  it('drops the quotes a reload’s bars now cover, and starts afresh for another symbol', async () => {
    const view = render(feedChart({ t: T0 + 3600 + 1500, price: 183 })); await flush()
    expect(plotted()?.at(-1)).toEqual({ time: T0 + 3600, open: 180, high: 183, low: 179, close: 183 })
    // Yahoo's next bar has landed: the quote inside the older bar is in Yahoo's bars now, and is not folded again.
    vi.mocked(api.candles).mockResolvedValue({ candles: [...bars(180), { t: T0 + 7200, o: 181, h: 182, l: 180, c: 181, v: 4 }] })
    await act(() => vi.advanceTimersByTimeAsync(30_000))
    expect(plotted()?.at(-2)).toEqual({ time: T0 + 3600, open: 180, high: 181, low: 179, close: 180 })
    expect(plotted()?.at(-1)).toEqual({ time: T0 + 7200, open: 181, high: 182, low: 180, close: 181 })
    view.rerender(
      <ThemeProvider>
        <CandleChart symbol="KIA" pair="000270.KS" venue="yahoo" interval="1h" onInterval={() => {}} lines={[]} tick={null} />
      </ThemeProvider>,
    ); await flush()
    expect(plotted()?.at(-1)).toEqual({ time: T0 + 7200, open: 181, high: 182, low: 180, close: 181 })
    expect(screen.getByText('Polled · 30s')).toBeInTheDocument() // no bars label: today's badge
  })
})

describe('chart display currency', () => {
  const T0 = 1_790_928_000
  const currencyChart = (priceScale = 1, currency = 'USD', tick: Tick | null = null, venue: 'yahoo' | 'binance' = 'yahoo') => (
    <ThemeProvider>
      <CandleChart symbol="SAMBIO" pair="207940.KS" venue={venue} interval="1h" onInterval={() => {}}
        lines={[{ price: 20, title: 'Entry', color: '--up' }]} priceScale={priceScale} currency={currency} tick={tick}
        bars={{ bars: 'Yahoo bars · 20 min delayed', title: "Yahoo bars in USD, about 20 min behind the exchange." }} />
    </ThemeProvider>
  )
  const candles = () => charts.at(-1)!.series.find((s) => s.type === 'Candlestick')!
  const axisFormatter = () => {
    const options = charts.at(-1)!.applyOptions.mock.calls.map(([o]) => o as { localization?: { priceFormatter?: (price: number) => string } })
    return options.findLast((o) => o.localization?.priceFormatter)?.localization?.priceFormatter
  }

  it('uses original OHLC and moving averages from the provider while converting USD price levels and incoming quotes', async () => {
    // These are raw native bars: historical FX differs from the current 1500 multiplier.
    vi.mocked(api.candles).mockResolvedValue({ candles: Array.from({ length: 50 }, (_, i) => ({ t: T0 + i * 3600, o: 27_000, h: 28_000, l: 26_000, c: 27_500, v: 10 })) })
    const view = render(currencyChart(1500, 'KRW')); await flush()
    const s = candles()
    expect(api.candles).toHaveBeenCalledWith('SAMBIO', '1h', 500, expect.any(AbortSignal), 'original')
    expect(plotted()?.at(-1)).toEqual({ time: T0 + 49 * 3600, open: 27_000, high: 28_000, low: 26_000, close: 27_500 })
    const series = charts.at(-1)!.series
    expect(series.filter((v) => v.type === 'Line').map((v) => v.setData.mock.calls.at(-1)?.[0].at(-1))).toEqual([
      { time: T0 + 49 * 3600, value: 27_500 }, { time: T0 + 49 * 3600, value: 27_500 },
    ])
    expect(series.find((v) => v.type === 'Histogram')!.setData.mock.calls.at(-1)?.[0].at(-1)).toEqual(expect.objectContaining({ value: 10 }))
    expect(s.createPriceLine).toHaveBeenLastCalledWith(expect.objectContaining({ price: 30_000, title: 'Entry' }))
    const autoscale = s.options.autoscaleInfoProvider as (base: () => { priceRange: { minValue: number; maxValue: number } }) => unknown
    expect(autoscale(() => ({ priceRange: { minValue: 26_000, maxValue: 28_000 } }))).toEqual({ priceRange: { minValue: 26_000, maxValue: 30_000 } })
    expect(s.applyOptions).toHaveBeenCalledWith(expect.objectContaining({ priceFormat: { type: 'price', precision: 2, minMove: 0.01 } }))
    expect(axisFormatter()?.(27_000)).toBe('27,000.00 KRW')
    expect(screen.getByText('KRW')).toBeInTheDocument()
    expect(screen.getByText('27,500.00')).toBeInTheDocument()
    expect(screen.getByText('Yahoo bars · 20 min delayed').parentElement?.title).toContain('in KRW')
    const quote = { t: T0 + 49 * 3600 + 300, price: 20 }
    view.rerender(currencyChart(1500, 'KRW', quote))
    expect(series.filter((v) => v.type === 'Line').map((v) => v.update.mock.calls.at(-1)?.[0])).toEqual([
      { time: T0 + 49 * 3600, value: 27_625 }, { time: T0 + 49 * 3600, value: 27_550 },
    ])
    vi.mocked(api.candles).mockResolvedValue({ candles: Array.from({ length: 50 }, (_, i) => ({ t: T0 + i * 3600, o: 18, h: 19, l: 17, c: 18, v: 10 })) })
    view.rerender(currencyChart(1, 'USD', quote)); await flush()
    expect(s.applyOptions).toHaveBeenLastCalledWith({ priceFormat: { type: 'price', precision: 3, minMove: 0.001 } })
    expect(axisFormatter()?.(20)).toBe('20.000 USD')
    expect(screen.getAllByText('20.000')).toHaveLength(2)
  })

  it('refetches the selected denomination, clears the old legend and retains USD quote history across switches', async () => {
    const view = render(currencyChart()); await flush()
    view.rerender(currencyChart(1, 'USD', { t: T0 + 7200 + 300, price: 184 }))
    const instance = charts.at(-1)!
    const move = instance.subscribeCrosshairMove.mock.calls[0][0] as (param: unknown) => void
    act(() => move({ time: T0, seriesData: new Map([
      [candles(), { time: T0, open: 180, high: 181, low: 179, close: 180 }],
      [instance.series.find((s) => s.type === 'Histogram')!, { time: T0, value: 10 }],
    ]) }))
    const pending = deferred<{ candles: Candle[] }>()
    vi.mocked(api.candles).mockReturnValueOnce(pending.promise)
    view.rerender(currencyChart(1500, 'KRW', { t: T0 + 7200 + 300, price: 184 }))
    expect(charts).toHaveLength(1)
    expect(api.candles).toHaveBeenCalledTimes(2)
    expect(api.candles).toHaveBeenLastCalledWith('SAMBIO', '1h', 500, expect.any(AbortSignal), 'original')
    expect(plotted()).toEqual([])
    expect(screen.queryByText('180.00')).toBeNull()
    expect(screen.queryByText('270,000.00')).toBeNull()
    expect(screen.getByRole('status')).toHaveTextContent('Loading SAMBIO 1h candles')
    await act(async () => pending.resolve({ candles: bars(268_000) }))
    expect(plotted()).toEqual([
      { time: T0, open: 268_000, high: 268_001, low: 267_999, close: 268_000 },
      { time: T0 + 3600, open: 268_000, high: 268_001, low: 267_999, close: 268_000 },
      { time: T0 + 7200, open: 276_000, high: 276_000, low: 276_000, close: 276_000 },
    ])
    // Crosshair payloads are already in the selected denomination.
    act(() => move({ time: T0 + 7200, seriesData: new Map([[candles(), { time: T0 + 7200, open: 276_000, high: 276_000, low: 276_000, close: 276_000 }]]) }))
    expect(screen.getAllByText('276,000.00')).toHaveLength(4)
    vi.mocked(api.candles).mockResolvedValue({ candles: bars(18) })
    view.rerender(currencyChart(1, 'USD', { t: T0 + 7200 + 300, price: 184 }))
    expect(screen.queryByText('276,000.00')).toBeNull()
    await flush()
    expect(api.candles).toHaveBeenLastCalledWith('SAMBIO', '1h', 500, expect.any(AbortSignal))
    expect(plotted()?.at(-1)).toEqual({ time: T0 + 7200, open: 184, high: 184, low: 184, close: 184 })
    expect(screen.getAllByText('184.00')).toHaveLength(4)
  })

  it('aborts and ignores a late USD response after switching to original history', async () => {
    const pending = deferred<{ candles: Candle[] }>()
    vi.mocked(api.candles).mockReturnValueOnce(pending.promise).mockResolvedValue({ candles: bars(26_000) })
    const view = render(currencyChart()); await flush()
    const oldSignal = vi.mocked(api.candles).mock.calls[0][3]!
    view.rerender(currencyChart(1500, 'KRW'))
    await flush()
    expect(oldSignal.aborted).toBe(true)
    await act(async () => pending.resolve({ candles: bars(18) }))
    expect(plotted()?.at(-1)).toEqual({ time: T0 + 3600, open: 26_000, high: 26_001, low: 25_999, close: 26_000 })
    expect(screen.getAllByText('26,000.00')).toHaveLength(2)
    expect(screen.queryByText('18.000')).toBeNull()
  })

  it('keeps original historical prices unchanged when FX moves while converting later quotes and polling overlays at the latest FX', async () => {
    vi.mocked(api.candles).mockResolvedValue({ candles: bars(26_000) })
    const view = render(currencyChart(1500, 'KRW')); await flush()
    view.rerender(currencyChart(1600, 'KRW'))
    expect(api.candles).toHaveBeenCalledTimes(1)
    expect(plotted()?.at(-1)).toEqual({ time: T0 + 3600, open: 26_000, high: 26_001, low: 25_999, close: 26_000 })
    view.rerender(currencyChart(1600, 'KRW', { t: T0 + 3600 + 300, price: 20 }))
    expect(candles().update).toHaveBeenLastCalledWith({ time: T0 + 3600, open: 26_000, high: 32_000, low: 25_999, close: 32_000 })
    await act(() => vi.advanceTimersByTimeAsync(30_000))
    expect(plotted()?.at(-1)).toEqual({ time: T0 + 3600, open: 26_000, high: 32_000, low: 25_999, close: 32_000 })
    expect(candles().createPriceLine).toHaveBeenLastCalledWith(expect.objectContaining({ price: 32_000, title: 'Entry' }))
    Object.defineProperty(document, 'hidden', { configurable: true, value: true })
    fireEvent(document, new Event('visibilitychange'))
    expect(screen.getByText('Paused')).toHaveAttribute('title', expect.stringContaining('in KRW'))
    expect(screen.getByText('Paused').title).not.toContain('in USD')
  })

  it('replays captured native quotes across FX changes and accepts a native price change offset by FX', async () => {
    vi.mocked(api.candles).mockResolvedValue({ candles: bars(26_000) })
    const t = T0 + 3600 + 300
    const view = render(currencyChart(1500, 'KRW', { t, price: 18, localPrice: 27_000 })); await flush()
    view.rerender(currencyChart(1600, 'KRW', { t, price: 16.875, localPrice: 27_000 }))
    expect(candles().update).toHaveBeenLastCalledWith({ time: T0 + 3600, open: 26_000, high: 27_000, low: 25_999, close: 27_000 })
    await act(() => vi.advanceTimersByTimeAsync(30_000))
    // Replaying the older USD 18 with today's multiplier would invent a KRW 28,800 high.
    expect(plotted()?.at(-1)).toEqual({ time: T0 + 3600, open: 26_000, high: 27_000, low: 25_999, close: 27_000 })
    view.rerender(currencyChart(1660, 'KRW', { t, price: 16.875, localPrice: 28_000 }))
    expect(candles().update).toHaveBeenLastCalledWith({ time: T0 + 3600, open: 26_000, high: 28_000, low: 25_999, close: 28_000 })
    await act(() => vi.advanceTimersByTimeAsync(30_000))
    expect(plotted()?.at(-1)).toEqual({ time: T0 + 3600, open: 26_000, high: 28_000, low: 25_999, close: 28_000 })
    vi.mocked(api.candles).mockResolvedValue({ candles: bars(18) })
    view.rerender(currencyChart(1, 'USD', { t, price: 16.875, localPrice: 28_000 }))
    await flush()
    expect(plotted()?.at(-1)?.close).toBe(16.875)
  })

  it('keeps the default socket data and trading callback in USD', async () => {
    const price = vi.fn()
    const renderSocket = (priceScale: number, currency: string) => <ThemeProvider><CandleChart symbol="BTC" pair="BTCUSDT" interval="1h" onInterval={() => {}} lines={[]} onPrice={price} priceScale={priceScale} currency={currency} /></ThemeProvider>
    render(renderSocket(1, 'USD')); await flush()
    expect(api.candles).toHaveBeenLastCalledWith('BTC', '1h', 500, expect.any(AbortSignal))
    const ws = FakeWebSocket.instances[0]
    act(() => ws.onmessage!({ data: JSON.stringify({ k: { t: (T0 + 3600) * 1000, o: '180', h: '185', l: '179', c: '184', v: '12' } }) }))
    expect(price.mock.calls).toEqual([[180], [184]])
    expect(FakeWebSocket.instances).toHaveLength(1)
    expect(candles().update).toHaveBeenLastCalledWith({ time: T0 + 3600, open: 180, high: 185, low: 179, close: 184 })
    expect(charts.at(-1)!.series.find((s) => s.type === 'Histogram')!.update).toHaveBeenLastCalledWith(expect.objectContaining({ value: 12 }))
  })
})

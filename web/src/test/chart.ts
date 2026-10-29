// Recording stand-in for lightweight-charts (mocked for every test in setup.ts): charts and series keep their
// options and every call, so tests can assert what would have been drawn.
import { vi } from 'vitest'

export const ColorType = { Solid: 'solid' } as const
export const CrosshairMode = { Normal: 0 } as const
export const LineStyle = { Solid: 0, Dotted: 1, Dashed: 2 } as const
export const TickMarkType = { Year: 0, Month: 1, DayOfMonth: 2, Time: 3, TimeWithSeconds: 4 } as const
export const LineSeries = 'Line'
export const CandlestickSeries = 'Candlestick'
export const HistogramSeries = 'Histogram'

export type FakeSeries = ReturnType<typeof makeSeries>
export type FakeChart = ReturnType<typeof makeChart>

/** Every chart created since the test file started; use `charts.at(-1)`. */
export const charts: FakeChart[] = []

function makeSeries(type: string, options: Record<string, unknown>) {
  return {
    type,
    options,
    markers: [] as Record<string, unknown>[], // set by createSeriesMarkers
    setData: vi.fn(),
    update: vi.fn(),
    applyOptions: vi.fn(),
    createPriceLine: vi.fn((o: Record<string, unknown>) => ({ options: o })),
    removePriceLine: vi.fn(),
    priceToCoordinate: vi.fn((price: number): number | null => price), // identity: a price is its own y
  }
}

function makeChart(el: HTMLElement, options: Record<string, unknown>) {
  const series: FakeSeries[] = []
  const timeScale = { fitContent: vi.fn(), setVisibleLogicalRange: vi.fn(), getVisibleLogicalRange: vi.fn((): { from: number; to: number } | null => null), subscribeVisibleLogicalRangeChange: vi.fn(), unsubscribeVisibleLogicalRangeChange: vi.fn() }
  return {
    el,
    options,
    series,
    timeScale: () => timeScale,
    priceScale: vi.fn(() => ({ applyOptions: vi.fn() })),
    addSeries: vi.fn((type: string, opts: Record<string, unknown>) => {
      const s = makeSeries(type, opts)
      series.push(s)
      return s
    }),
    removeSeries: vi.fn((s: FakeSeries) => series.splice(series.indexOf(s), 1)),
    applyOptions: vi.fn(),
    subscribeCrosshairMove: vi.fn(),
    remove: vi.fn(),
  }
}

export const createChart = vi.fn((el: HTMLElement, options: Record<string, unknown>) => {
  const chart = makeChart(el, options)
  charts.push(chart)
  return chart
})

/** v5 series markers: recorded on the fake series. */
export const createSeriesMarkers = vi.fn((series: FakeSeries, markers: Record<string, unknown>[] = []) => {
  series.markers = markers
  return { setMarkers: vi.fn((m: Record<string, unknown>[]) => (series.markers = m)), markers: () => series.markers, detach: vi.fn() }
})

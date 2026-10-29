import { act, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it } from 'vitest'
import { charts, createSeriesMarkers } from '../../test/chart'
import { ThemeProvider, useTheme } from '../../lib/theme'
import { LineChart, stackLabels } from './LineChart'

function ThemeButton() {
  const { toggle } = useTheme()
  return <button onClick={toggle}>toggle theme</button>
}

const pts = (vals: number[]) => vals.map((v, i) => ({ t: 1000 + i * 60, v }))

describe('LineChart', () => {
  beforeEach(() => {
    charts.length = 0
  })

  it('creates a static, auto-sized chart and draws one titled series', () => {
    const format = (v: number) => `${v}!`
    render(
      <ThemeProvider>
        <LineChart series={[{ name: 'Equity', points: pts([100, 101, 103]) }]} height={200} format={format} className="c" />
      </ThemeProvider>,
    )
    const chart = charts.at(-1)!
    expect(chart.options).toMatchObject({ autoSize: true, handleScroll: false, handleScale: false, localization: { priceFormatter: format } })
    expect(chart.el.style.height).toBe('200px')
    expect(chart.series).toHaveLength(1)
    expect(chart.series[0].options).toMatchObject({ title: 'Equity', lineWidth: 2 })
    expect(chart.series[0].setData).toHaveBeenCalledWith([
      { time: 1000, value: 100 },
      { time: 1060, value: 101 },
      { time: 1120, value: 103 },
    ])
    expect(chart.timeScale().fitContent).toHaveBeenCalled()
    expect(screen.queryByRole('list')).toBeNull() // no legend for a single series
  })

  it('drops out-of-order points instead of letting the library throw', () => {
    render(
      <ThemeProvider>
        <LineChart series={[{ name: 'A', points: [{ t: 10, v: 1 }, { t: 5, v: 2 }, { t: 10, v: 3 }, { t: 20, v: 4 }] }]} />
      </ThemeProvider>,
    )
    expect(charts.at(-1)!.series[0].setData).toHaveBeenCalledWith([
      { time: 10, value: 1 },
      { time: 20, value: 4 },
    ])
  })

  it('shows a legend for several series, mutes the benchmark of two and honours explicit colours', () => {
    const { rerender } = render(
      <ThemeProvider>
        <LineChart series={[{ name: 'Strategy', points: pts([1, 2]) }, { name: 'Buy & hold', points: pts([1, 1.5]) }]} />
      </ThemeProvider>,
    )
    expect(screen.getAllByRole('listitem').map((li) => li.textContent)).toEqual(['Strategy', 'Buy & hold'])
    const chart = charts.at(-1)!
    expect(chart.series.map((s) => s.options.lineWidth)).toEqual([2, 1])
    expect(chart.series.every((s) => s.options.title === '')).toBe(true)

    rerender(
      <ThemeProvider>
        <LineChart
          series={[
            { name: 'a', points: pts([1, 2]), color: '#123456' },
            { name: 'b', points: pts([1, 2]) },
            { name: 'c', points: pts([1, 3]) },
          ]}
        />
      </ThemeProvider>,
    )
    expect(chart.removeSeries).toHaveBeenCalledTimes(2) // old series cleared on a new signature
    expect(chart.series.map((s) => s.options.color)).toEqual(['#123456', '#3B6FD8', '#0E9F8E'])
  })

  it('mutes a benchmark and widens a line on request', () => {
    document.documentElement.style.setProperty('--muted', '#62666F')
    render(
      <ThemeProvider>
        <LineChart
          series={[
            { name: 'Backtest', points: pts([1, 2]) },
            { name: 'Live forward', points: pts([2, 3]), color: '#3B6FD8', width: 2 },
            { name: 'Buy & hold', points: pts([1, 1.5]), muted: true },
          ]}
        />
      </ThemeProvider>,
    )
    const chart = charts.at(-1)!
    expect(chart.series.map((s) => [s.options.color, s.options.lineWidth])).toEqual([
      [expect.anything(), 2],
      ['#3B6FD8', 2],
      ['#62666F', 1],
    ])
    document.documentElement.style.removeProperty('--muted')
  })

  it('marks a series at one of its points, in its colour, and skips a marker off its data', () => {
    render(
      <ThemeProvider>
        <LineChart
          series={[
            { name: 'Backtest', points: pts([100, 104]) },
            { name: 'Live forward', points: pts([104, 104, 107]).slice(1), color: '#3B6FD8' },
          ]}
          markers={[
            { series: 1, t: 1060, text: 'Published' },
            { series: 1, t: 999, text: 'off the data' },
          ]}
        />
      </ThemeProvider>,
    )
    const [backtest, forward] = charts.at(-1)!.series
    expect(backtest.markers).toEqual([])
    expect(forward.options.lineWidth).toBe(1)
    expect(forward.markers).toEqual([{ time: 1060, position: 'aboveBar', shape: 'arrowDown', color: '#3B6FD8', text: 'Published' }])
  })

  it('detaches marker primitives when their series are rebuilt and on unmount', () => {
    const { rerender, unmount } = render(<ThemeProvider><LineChart series={[{ name: 'A', points: pts([1, 2]) }]} markers={[{ series: 0, t: 1000, text: 'Saved' }]} /></ThemeProvider>)
    const initialMarkers = createSeriesMarkers.mock.results.at(-1)!.value
    rerender(<ThemeProvider><LineChart series={[{ name: 'A', points: pts([1, 2]) }]} markers={[{ series: 0, t: 1060, text: 'Published' }]} /></ThemeProvider>)
    expect(initialMarkers.detach).toHaveBeenCalledOnce()
    const updatedMarkers = createSeriesMarkers.mock.results.at(-1)!.value
    expect(charts.at(-1)!.series[0].markers).toEqual([expect.objectContaining({ time: 1060, text: 'Published' })])
    unmount()
    expect(updatedMarkers.detach).toHaveBeenCalledOnce()
  })

  it('re-applies theme colours on toggle and removes the chart on unmount', async () => {
    const { unmount } = render(
      <ThemeProvider>
        <ThemeButton />
        <LineChart series={[{ name: 'A', points: pts([1, 2]) }]} />
      </ThemeProvider>,
    )
    const chart = charts.at(-1)!
    const before = chart.applyOptions.mock.calls.length
    await userEvent.click(screen.getByRole('button', { name: 'toggle theme' }))
    expect(chart.applyOptions.mock.calls.length).toBeGreaterThan(before)
    act(() => unmount())
    expect(chart.remove).toHaveBeenCalled()
  })
})

describe('LineChart minSpan', () => {
  beforeEach(() => {
    charts.length = 0
  })

  it('widens a narrow autoscale range around its midpoint, and leaves wide ranges and other charts alone', () => {
    render(
      <ThemeProvider>
        <LineChart series={[{ name: 'Equity', points: pts([100_000, 100_016]) }]} minSpan={1000} />
      </ThemeProvider>,
    )
    const provider = charts.at(-1)!.series[0].options.autoscaleInfoProvider as (original: () => unknown) => unknown
    // A $16 move on a $100k account keeps a $1,000 window instead of filling the chart.
    expect(provider(() => ({ priceRange: { minValue: 100_000, maxValue: 100_016 }, margins: { above: 1, below: 1 } }))).toEqual({
      priceRange: { minValue: 99_508, maxValue: 100_508 },
      margins: { above: 1, below: 1 },
    })
    const wide = { priceRange: { minValue: 95_000, maxValue: 101_000 } }
    expect(provider(() => wide)).toBe(wide)
    expect(provider(() => null)).toBeNull()

    render(
      <ThemeProvider>
        <LineChart series={[{ name: 'Plain', points: pts([1, 2]) }]} />
      </ThemeProvider>,
    )
    expect(charts.at(-1)!.series[0].options).not.toHaveProperty('autoscaleInfoProvider')
  })
})

describe('LineChart price scale and end labels', () => {
  beforeEach(() => {
    charts.length = 0
  })

  it('never half-draws an edge tick label: margins top and bottom, whole labels only', () => {
    render(
      <ThemeProvider>
        <LineChart series={[{ name: 'Equity', points: pts([100, 99]) }]} />
      </ThemeProvider>,
    )
    expect(charts.at(-1)!.options).toMatchObject({ rightPriceScale: { entireTextOnly: true, scaleMargins: { top: 0.12, bottom: 0.08 } } })
  })

  it('moves each last value into a gutter, stacked apart, after the chart paints', async () => {
    render(
      <ThemeProvider>
        <LineChart
          endLabels
          height={200}
          format={(v) => `${v}%`}
          series={[{ name: 'A', points: pts([0, 50]) }, { name: 'B', points: pts([0, 55]) }, { name: 'C', points: pts([0, 120]) }]}
        />
      </ThemeProvider>,
    )
    const chart = charts.at(-1)!
    expect(chart.series.every((s) => s.options.lastValueVisible === false)).toBe(true)
    expect(chart.timeScale().subscribeVisibleLogicalRangeChange).toHaveBeenCalled()
    // The fake maps a price to the same y: 50 and 55 would overlap, so B is pushed 17 px below A.
    const labels = await screen.findAllByText(/%$/)
    expect(labels.map((l) => [l.textContent, l.style.top])).toEqual([
      ['50%', '42.5px'],
      ['55%', '59.5px'],
      ['120%', '112.5px'],
    ])
  })

  it('stackLabels spreads centres at least the gap apart and keeps them inside the chart', () => {
    const ys = (labels: { y: number }[]) => labels.map((l) => l.y)
    expect(ys(stackLabels([{ y: 100 }, { y: 50 }, { y: 52 }], 12, 200))).toEqual([50, 62, 100])
    expect(ys(stackLabels([{ y: 0 }, { y: 1 }], 12, 200))).toEqual([6, 18]) // off the top edge
    expect(ys(stackLabels([{ y: 199 }, { y: 200 }, { y: 120 }], 12, 200))).toEqual([120, 182, 194]) // off the bottom edge
  })
})

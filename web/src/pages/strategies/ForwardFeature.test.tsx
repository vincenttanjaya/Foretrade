import { screen, waitFor, within } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError } from '../../lib/api'
import type { StrategyForward } from '../../lib/types'
import { charts } from '../../test/chart'
import * as fx from '../../test/fixtures'
import { renderPage, stubSession } from '../../test/utils'
import { CommunityBoard } from './Community'
import { StrategiesPage } from './StrategiesPage'
import { StrategyDetailPage } from './StrategyDetailPage'

const openDetail = (f: StrategyForward | null | undefined, createdAt = '2026-08-31T23:30:00Z') => {
  const endT = Math.floor(Date.parse(createdAt) / 1000 / 14400) * 14400 - 14400
  const m = fx.strategy().metrics
  vi.spyOn(api, 'strategy').mockResolvedValue({ strategy: fx.strategy({ forward: f, createdAt, community: fx.community(), metrics: { ...m, endT, startT: endT - (m.endT - m.startT) } }) })
  return renderPage(<StrategyDetailPage />, { route: '/strategies/s1', path: '/strategies/:id' })
}
const forwardRegion = () => screen.findByRole('region', { name: 'Live forward' })
const forwardChart = () => charts.find((c) => c.el.closest('[aria-label="Live forward"]') && !c.remove.mock.calls.length)!

beforeEach(() => {
  charts.length = 0
  stubSession()
  vi.spyOn(api, 'autopilots').mockResolvedValue({ autopilots: [] })
})

describe('saved forward detail', () => {
  it.each([
    ['published', 'Since publish', 'Published'],
    ['saved', 'Since saved', 'Saved'],
    ['launch', 'Since launch', 'Launched'],
  ] as const)('shows independent %s evidence and the precise boundary', async (basis, label, mark) => {
    openDetail(fx.forward({ basis, since: '2026-09-01T04:00:00Z' }))
    const region = await forwardRegion()
    expect(region).toHaveTextContent(label)
    expect(region).toHaveTextContent('2026-09-01 04:00:00 UTC')
    expect(region).toHaveTextContent('Source as of 2026-10-05 00:00:00 UTC')
    const table = within(region).getByRole('table', { name: 'Saved backtest and live forward metrics' })
    expect(within(table).getByRole('row', { name: /Return/ })).toHaveTextContent('24.5%')
    expect(within(table).getByRole('row', { name: /^Return/ })).toHaveTextContent('4.2%')
    expect(region).toHaveTextContent('Buy & hold SOL')
    await waitFor(() => expect(forwardChart()?.series[0].markers).toEqual([
      expect.objectContaining({ time: 1788235200, text: mark }),
    ]))
    const plotted = forwardChart().series[0].setData.mock.calls.at(-1)![0]
    expect(plotted).toEqual([{ time: 1788235200, value: 100.5 }, { time: 1791144000, value: 104.2 }])
    expect(region).toHaveTextContent('starts flat at 100')
  })

  it('marks the first plotted bar after an intrabar boundary and explains the unmeasured gap', async () => {
    openDetail(fx.forward(), '2026-09-01T01:00:00Z')
    const region = await forwardRegion()
    expect(region).toHaveTextContent('2026-09-01 02:30:00.123 UTC')
    expect(region).toHaveTextContent('First plotted bar: 2026-09-01 04:00:00 UTC')
    expect(region).toHaveTextContent('does not show a price at the publication instant')
    expect(region).toHaveTextContent('between saving and publishing is unmeasured')
    await waitFor(() => expect(forwardChart()?.series[0].markers[0]?.time).toBe(1788235200))
    // No connection to the saved backtest or pre-publication bookkeeping anchor.
    expect(forwardChart().series).toHaveLength(2)
    expect(forwardChart().series[0].setData.mock.calls.at(-1)![0][0]).toEqual({ time: 1788235200, value: 100.5 })
    expect(region).not.toHaveTextContent('rebased')
  })

  it('preserves a microsecond publication gap and excludes a point before that exact instant', async () => {
    openDetail(fx.forward({ since: '2026-09-01T04:00:00.000001Z', equity: [
      { t: 1788235200, v: 100 }, { t: 1788249600, v: 101 }, { t: 1791144000, v: 104.2 },
    ] }), '2026-09-01T04:00:00Z')
    const region = await forwardRegion()
    expect(region).toHaveTextContent('between saving and publishing is unmeasured')
    expect(region).toHaveTextContent('2026-09-01 04:00:00.000001 UTC')
    await waitFor(() => expect(forwardChart().series[0].markers[0]?.time).toBe(1788249600))
    expect(forwardChart().series[0].setData.mock.calls.at(-1)![0]).toEqual([
      { time: 1788249600, value: 101 }, { time: 1791144000, value: 104.2 },
    ])
  })

  it('keeps saved forward immutable through a free retune and reset', async () => {
    const original = fx.forward()
    const before = structuredClone(original)
    const { user } = openDetail(original)
    vi.spyOn(api, 'backtest').mockResolvedValue({ metrics: { ...fx.strategy().metrics, totalReturnPct: 80 }, equity: fx.series([100, 180]), benchmark: fx.series([100, 110]), trades: [] })
    const region = await forwardRegion()
    const stop = screen.getByLabelText('Stop loss')
    await user.clear(stop)
    await user.type(stop, '3')
    await user.click(screen.getByRole('button', { name: /Re-run backtest/ }))
    expect(await screen.findByText('Modified · not saved')).toBeInTheDocument()
    expect(region).toHaveTextContent('saved rules')
    expect(within(region).getByRole('row', { name: /^Return/ })).toHaveTextContent('24.5%')
    expect(within(region).getByRole('row', { name: /^Return/ })).toHaveTextContent('4.2%')
    expect(region).not.toHaveTextContent('80.0%')
    expect(screen.getByRole('heading', { name: 'Modified backtest vs buy & hold' })).toBeInTheDocument()
    expect(original).toEqual(before)
    await user.click(screen.getByRole('button', { name: 'Reset to saved' }))
    expect(region).not.toHaveTextContent('modified rules on screen')
    expect(screen.getByRole('heading', { name: 'Saved backtest vs buy & hold' })).toBeInTheDocument()
  })

  it('replaces publication metadata and marker using the saved publish response', async () => {
    const { user } = openDetail(fx.forward({ basis: 'saved', since: '2026-08-31T23:30:00Z' }))
    const published = fx.forward({ since: '2026-09-01T04:00:00.000001Z', equity: [{ t: 1788249600, v: 100 }, { t: 1791144000, v: 102 }], metrics: { ...fx.forward().metrics!, totalReturnPct: 2 } })
    vi.spyOn(api, 'publishStrategy').mockResolvedValue({ strategy: fx.strategy({ forward: published, community: fx.community({ published: true, publishedAt: published.since }) }) })
    await forwardRegion()
    await user.click(screen.getByRole('button', { name: 'Publish to community' }))
    const dialog = screen.getByRole('dialog')
    expect(dialog).toHaveTextContent('live forward')
    expect(dialog).toHaveTextContent('Autopilot results stay private')
    await user.click(within(dialog).getByRole('button', { name: 'Publish' }))
    await waitFor(() => expect(screen.getByRole('region', { name: 'Live forward' })).toHaveTextContent('2026-09-01 04:00:00.000001 UTC'))
    await waitFor(() => expect(forwardChart().series[0].markers[0]?.time).toBe(1788249600))
    expect(screen.getByRole('region', { name: 'Live forward' })).toHaveTextContent('2.0%')
  })

  it.each([
    ['too_new', 'Too new to measure'],
    ['history_unavailable', 'complete history'],
    ['market_unavailable', 'Market data'],
    ['timeout', 'time budget'],
    ['busy', 'busy'],
  ] as const)('explains unavailable %s without inventing a return', async (reason, text) => {
    openDetail(fx.forward({ metrics: null, equity: [], benchmark: [], trades: [], early: true, asOf: null, reason }))
    const region = await forwardRegion()
    expect(region).toHaveTextContent(text)
    expect(within(region).queryByRole('table')).toBeNull()
    expect(region).not.toHaveTextContent('0.0%')
    expect(region).not.toHaveTextContent('No trades yet')
    expect(region).toHaveTextContent('No measured window')
  })

  it('preserves a stale run and source window while explaining the failed refresh', async () => {
    openDetail(fx.forward({ stale: true, reason: 'history_unavailable', asOf: '2026-10-01T00:00:00Z' }))
    const region = await forwardRegion()
    expect(region).toHaveTextContent('Stale')
    expect(region).toHaveTextContent('The figures retain their original measured window')
    expect(region).toHaveTextContent('Source as of 2026-10-01 00:00:00 UTC')
    expect(region).toHaveTextContent('complete history')
    expect(within(region).getByRole('row', { name: /^Return/ })).toHaveTextContent('4.2%')
  })

  it('refreshes unavailable forward evidence without replacing a modified backtest', async () => {
    const { user } = openDetail(fx.forward({ metrics: null, early: true, reason: 'market_unavailable', equity: [], benchmark: [], trades: [] }))
    await forwardRegion()
    vi.spyOn(api, 'backtest').mockResolvedValue({ metrics: { ...fx.strategy().metrics, totalReturnPct: 80 }, equity: fx.series([100, 180]), benchmark: [], trades: [] })
    await user.clear(screen.getByLabelText('Stop loss'))
    await user.type(screen.getByLabelText('Stop loss'), '3')
    await user.click(screen.getByRole('button', { name: /Re-run backtest/ }))
    await screen.findByText('Modified · not saved')
    vi.mocked(api.strategy).mockResolvedValue({ strategy: fx.strategy({ forward: fx.forward(), community: fx.community() }) })
    await user.click(within(await forwardRegion()).getByRole('button', { name: 'Refresh live forward' }))
    await waitFor(() => expect(within(screen.getByRole('region', { name: 'Live forward' })).getByRole('row', { name: /^Return/ })).toHaveTextContent('4.2%'))
    expect(screen.getByRole('heading', { name: 'Modified backtest vs buy & hold' })).toBeInTheDocument()
    expect(screen.getByText('Modified · not saved')).toBeInTheDocument()
  })

  it.each([undefined, null])('does not present legacy missing data as a flat success (%s)', async (f) => {
    openDetail(f)
    await screen.findByRole('heading', { level: 1 })
    if (f === undefined) expect(screen.queryByRole('region', { name: 'Live forward' })).toBeNull()
    else {
      const region = await forwardRegion()
      expect(region).toHaveTextContent('unavailable')
      expect(region).not.toHaveTextContent('0.0%')
    }
  })

  it.each([
    [29, 4], [35, 2],
  ])('shows early evidence for %i days and %i trades', async (days, trades) => {
    const m = fx.forward().metrics!
    openDetail(fx.forward({ early: true, metrics: { ...m, endT: m.startT + days * 86400, trades } }))
    const region = await forwardRegion()
    expect(within(region).getByText('Early', { selector: 'span' })).toBeInTheDocument()
    expect(region).toHaveTextContent(`${days} days and ${trades} trades`)
  })

  it('distinguishes a measured flat zero-trade run from unavailable history', async () => {
    openDetail(fx.forward({ early: true, metrics: { ...fx.forward().metrics!, totalReturnPct: 0, trades: 0 }, equity: fx.forward().equity.map((p) => ({ ...p, v: 100 })) }))
    const region = await forwardRegion()
    expect(region).toHaveTextContent('No trades yet')
    expect(within(region).getByRole('row', { name: /^Return/ })).toHaveTextContent('0.0%')
  })
})

const orderedRows = () => [
  fx.strategySummary({ id: 'early', name: 'Early winner', forward: fx.forward({ early: true, metrics: { ...fx.forward().metrics!, totalReturnPct: 50 } }) }),
  fx.strategySummary({ id: 'negative', name: 'Full negative', forward: fx.forward({ metrics: { ...fx.forward().metrics!, totalReturnPct: -2 } }) }),
  fx.strategySummary({ id: 'unavailable', name: 'Missing history', forward: fx.forward({ metrics: null, reason: 'history_unavailable', early: true }) }),
  fx.strategySummary({ id: 'positive', name: 'Full positive', forward: fx.forward() }),
]

describe('forward evidence on listed rows', () => {
  it.each(['mine', 'community', 'top'] as const)('renders %s metadata and sorts only the displayed rows by forward tiers', async (scope) => {
    const source = orderedRows().map((s) => scope === 'community' ? { ...s, community: fx.publishedBy() } : s)
    const before = structuredClone(source)
    vi.spyOn(api, 'strategies').mockResolvedValue({ strategies: source })
    vi.spyOn(api, 'strategy').mockRejectedValue(new ApiError(502, 'upstream', 'No chart'))
    const { user } = scope === 'community'
      ? renderPage(<CommunityBoard />, { route: '/strategies', path: '/strategies' })
      : renderPage(<StrategiesPage />, { route: scope === 'top' ? '/strategies?tab=top' : '/strategies', path: '/strategies' })
    const firstLink = await screen.findByRole('link', { name: /Early winner/ })
    const row = firstLink.closest(scope === 'top' ? 'tr' : 'li')!
    expect(row).toHaveTextContent('Since publish')
    expect(row).toHaveTextContent('Sep 1, 2026')
    expect(row).toHaveTextContent('34 days')
    expect(row).toHaveTextContent('B&H SOL +1.3%')
    expect(row).toHaveTextContent('Early')
    const links = () => screen.getAllByRole('link').filter((a) => source.some((s) => a.getAttribute('href') === `/strategies/${s.id}`)).map((a) => a.textContent?.includes('Early winner') ? 'Early winner' : a.textContent?.includes('Full negative') ? 'Full negative' : a.textContent?.includes('Missing history') ? 'Missing history' : 'Full positive')
    expect(links()).toEqual(['Early winner', 'Full negative', 'Missing history', 'Full positive'])
    const label = scope === 'mine' ? 'My strategy order' : scope === 'community' ? 'Community strategy order' : 'Labs strategy order'
    await user.selectOptions(screen.getByRole('combobox', { name: label }), 'forward')
    expect(links()).toEqual(['Full positive', 'Full negative', 'Early winner', 'Missing history'])
    expect(source).toEqual(before)
    await user.selectOptions(screen.getByRole('combobox', { name: label }), 'default')
    expect(links()).toEqual(['Early winner', 'Full negative', 'Missing history', 'Full positive'])
  })

  it('labels stale source time on a community row and leaves Star usable', async () => {
    vi.spyOn(api, 'strategies').mockResolvedValue({ strategies: [fx.strategySummary({ community: fx.publishedBy(), forward: fx.forward({ stale: true, reason: 'market_unavailable', asOf: '2026-10-01T00:00:00Z' }) })] })
    const star = vi.spyOn(api, 'starStrategy').mockResolvedValue({ stars: 5, starred: true })
    const { user } = renderPage(<CommunityBoard />)
    const row = (await screen.findByRole('link', { name: /SOL EMA cross/ })).closest('li')!
    expect(row).toHaveTextContent('Stale')
    expect(row).toHaveTextContent('2026-10-01 00:00:00 UTC')
    await user.click(screen.getByRole('button', { name: /Star/ }))
    expect(star).toHaveBeenCalledWith('s1', true)
    expect(screen.getByRole('button', { name: /Unstar/ })).toHaveAttribute('aria-pressed', 'true')
  })

  it('reloads forward source metadata when returning to Labs with cached saved curves', async () => {
    const list = vi.spyOn(api, 'strategies').mockResolvedValue({ strategies: [fx.strategySummary({ id: 'cache-lab', name: 'Cached Labs rules', forward: fx.forward() })] })
    vi.spyOn(api, 'strategy').mockResolvedValue({ strategy: fx.strategy({ id: 'cache-lab' }) })
    const firstVisit = renderPage(<StrategiesPage />, { route: '/strategies?tab=top', path: '/strategies' })
    await screen.findByText('Source as of 2026-10-05 00:00:00 UTC')
    await waitFor(() => expect(charts.at(-1)?.series.length).toBe(1))
    firstVisit.unmount()
    list.mockResolvedValue({ strategies: [fx.strategySummary({ id: 'cache-lab', name: 'Cached Labs rules', forward: fx.forward({ asOf: '2026-10-06T00:00:00Z', stale: true, reason: 'market_unavailable' }) })] })
    renderPage(<StrategiesPage />, { route: '/strategies?tab=top', path: '/strategies' })
    expect(await screen.findByText(/Source as of 2026-10-06 00:00:00 UTC/)).toBeInTheDocument()
    expect(screen.getByText('Stale')).toBeInTheDocument()
    await waitFor(() => expect(charts.at(-1)?.series.length).toBe(1))
  })
})

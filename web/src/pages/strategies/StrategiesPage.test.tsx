import { screen, waitFor, within } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError } from '../../lib/api'
import { charts } from '../../test/chart'
import * as fx from '../../test/fixtures'
import { creditsUsed, currentPath, renderPage, stubSession } from '../../test/utils'
import { StrategiesPage } from './StrategiesPage'
import { StrategyDetailPage } from './StrategyDetailPage'

const open = (route = '/strategies') =>
  renderPage(<StrategiesPage />, { route, path: '/strategies', routes: { '/strategies/:id': <StrategyDetailPage /> } })

const m0 = fx.strategySummary().metrics
const labs = (id: string, name: string, sharpe: number, total: number, maxDD: number, more: Partial<typeof m0> = {}) =>
  fx.strategySummary({ id, name, author: 'Foretrade Labs', symbol: 'BTC', interval: '1d', metrics: { ...m0, sharpe, totalReturnPct: total, maxDrawdownPct: maxDD, ...more } })
// Windows: 126 days, 2.7 years, and 42 days (too short to annualize).
const top = [
  labs('a', 'Donchian breakout', 1.9, 40, 22),
  labs('b', 'RSI mean revert', 1.5, 55, 12, { endT: m0.startT + Math.round(2.7 * 365 * 86400) }),
  labs('c', 'MACD trend', 1.1, 20, 30, { annualizedReturnPct: null, endT: m0.startT + 42 * 86400 }),
]

beforeEach(() => {
  charts.length = 0
  stubSession()
  vi.spyOn(api, 'strategies').mockImplementation(async (scope) => ({ strategies: scope === 'top' ? top : [fx.strategySummary()] }))
  vi.spyOn(api, 'autopilots').mockResolvedValue({ autopilots: [] })
})

describe('StrategiesPage · autopilot', () => {
  it('shows the running autopilot as a strip above every tab', async () => {
    vi.spyOn(api, 'autopilots').mockResolvedValue({ autopilots: [fx.autopilot(), fx.autopilot({ id: 'old', status: 'stopped', stopReason: 'user' })] })
    vi.spyOn(api, 'autopilot').mockResolvedValue({ autopilot: fx.autopilot(), trades: [] })
    const { user } = open()
    const strip = await screen.findByRole('region', { name: 'Autopilot SOL EMA cross' })
    expect(strip).toHaveTextContent('Running')
    expect(strip).toHaveTextContent('Long 0.42 SOL @ $142.10')
    expect(within(strip).getByRole('link', { name: 'SOL EMA cross' })).toHaveAttribute('href', '/strategies/s1')
    expect(within(strip).getByRole('button', { name: 'Stop' })).toBeInTheDocument()
    expect(screen.getByRole('list', { name: 'How it works' })).toHaveTextContent('1Describe2AI rules3Backtest4Deploy')
    await user.click(screen.getByRole('button', { name: 'Top strategies' }))
    expect(screen.getByRole('region', { name: 'Autopilot SOL EMA cross' })).toBeInTheDocument()
  })

  it('shows no strip when nothing runs', async () => {
    open()
    await screen.findByRole('link', { name: /SOL EMA cross/ })
    expect(screen.queryByRole('region', { name: /^Autopilot/ })).toBeNull()
  })
})

describe('StrategiesPage · studio', () => {
  it('generates a strategy and opens it without refetching', async () => {
    let finish!: (v: Awaited<ReturnType<typeof api.generateStrategy>>) => void
    const gen = vi.spyOn(api, 'generateStrategy').mockReturnValue(new Promise((r) => (finish = r)))
    const detail = vi.spyOn(api, 'strategy')
    const { user } = open()
    expect(screen.getByRole('heading', { name: 'Turn ideas into strategies.' })).toBeInTheDocument()
    const go = screen.getByRole('button', { name: /Generate & backtest/ })
    await user.type(screen.getByLabelText('Describe your strategy'), 'buy')
    expect(go).toBeDisabled() // under 5 characters
    await user.type(screen.getByLabelText('Describe your strategy'), ' ETH dips below the 20 EMA')
    await user.selectOptions(screen.getByRole('combobox', { name: 'Market' }), 'ETH')
    await user.selectOptions(screen.getByRole('combobox', { name: 'Interval' }), '1h')
    await user.click(go)

    expect(gen).toHaveBeenCalledWith({ prompt: 'buy ETH dips below the 20 EMA', symbol: 'ETH', interval: '1h' })
    expect(screen.getByRole('button', { name: /Generating…/ })).toBeDisabled()
    expect(screen.getByText('Turning your idea into entry and exit rules…')).toBeInTheDocument()
    expect(within(screen.getByRole('list', { name: 'How it works' })).getByText('AI rules').closest('[aria-current]')).toHaveAttribute('aria-current', 'step')

    finish({ strategy: fx.strategy({ id: 's9' }), credits: { ...fx.credits, used: 20 } })
    expect(await screen.findByRole('heading', { level: 1, name: 'SOL EMA cross' })).toBeInTheDocument()
    expect(currentPath()).toBe('/strategies/s9')
    expect(creditsUsed()).toBe('20')
    expect(detail).not.toHaveBeenCalled() // the new strategy rode along in router state
  })

  it('shows a generate error and re-reads credits', async () => {
    vi.spyOn(api, 'generateStrategy').mockRejectedValue(new ApiError(502, 'upstream', 'The model returned an invalid spec'))
    const { user } = open()
    await user.type(screen.getByLabelText('Describe your strategy'), 'Something clever here')
    const calls = vi.mocked(api.me).mock.calls.length
    await user.keyboard('{Meta>}{Enter}{/Meta}')
    expect(await screen.findByRole('alert')).toHaveTextContent('The model returned an invalid spec')
    expect(vi.mocked(api.me).mock.calls.length).toBe(calls + 1)
  })

  it('fills prompt, market and interval from an example', async () => {
    const { user } = open()
    const chip = screen.getByRole('button', { name: /Mean-revert ETH/ })
    await user.click(chip)
    expect(chip).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByLabelText('Describe your strategy')).toHaveValue('Mean-revert ETH: long when RSI(14) < 30, exit when RSI > 55')
    expect(screen.getByRole('combobox', { name: 'Market' })).toHaveValue('ETH')
    expect(screen.getByRole('combobox', { name: 'Interval' })).toHaveValue('1h')
  })

  it('disables generating when today’s credits are used up', async () => {
    vi.mocked(api.me).mockResolvedValue({ user: fx.user, credits: { ...fx.credits, used: 499 } })
    const { user } = open()
    await user.type(screen.getByLabelText('Describe your strategy'), 'Buy BTC on RSI 30')
    expect(await screen.findByText('Daily AI credits used up. They reset at 00:00 UTC.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Generate & backtest/ })).toBeDisabled()
  })

  it('lists my strategies as cards', async () => {
    open()
    const card = await screen.findByRole('link', { name: /SOL EMA cross/ })
    expect(card).toHaveAttribute('href', '/strategies/s1')
    expect(card).toHaveTextContent('SOL · 4h')
    expect(card).toHaveTextContent('Return▲up 24.5%')
    expect(card).toHaveTextContent('Sharpe1.42')
    expect(card).toHaveTextContent('Max DD−18.4%')
    expect(screen.getByText('1 saved')).toBeInTheDocument()
  })

  it('points new users to the leaderboard', async () => {
    vi.mocked(api.strategies).mockImplementation(async (scope) => ({ strategies: scope === 'top' ? top : [] }))
    vi.spyOn(api, 'strategy').mockRejectedValue(new ApiError(502, 'upstream', 'x')) // keeps the leaderboard cache empty for later tests
    const { user } = open()
    expect(await screen.findByRole('heading', { name: 'No strategies yet.' })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /See the top strategies/ }))
    expect(currentPath()).toBe('/strategies?tab=top')
    expect(await screen.findByRole('heading', { name: 'Top strategies, measured honestly.' })).toBeInTheDocument()
    expect(await screen.findByText('Equity curves are unavailable right now.')).toBeInTheDocument()
  })

  it('retries my strategies after a failure', async () => {
    vi.mocked(api.strategies).mockRejectedValueOnce(new ApiError(500, 'internal', 'Database is down'))
    const { user } = open()
    expect(await screen.findByRole('heading', { name: "Couldn't load your strategies." })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Try again' }))
    expect(await screen.findByRole('link', { name: /SOL EMA cross/ })).toBeInTheDocument()
  })
})

// The leaderboard is cached for the page's lifetime (module state), so these run in order: failure first, then cached.
describe('StrategiesPage · top strategies', () => {
  it('marks short samples with a muted badge and says how the board is ranked', async () => {
    vi.mocked(api.strategies).mockResolvedValue({ strategies: [top[1], { ...top[0], shortSample: true }, { ...top[2], shortSample: true }] })
    open('/strategies?tab=top')
    const rows = await waitFor(() => {
      const r = within(screen.getByRole('table')).getAllByRole('row').slice(1)
      expect(r).toHaveLength(3)
      return r
    })
    expect(rows.map((r) => within(r).queryByText('Short sample') !== null)).toEqual([false, true, true])
    expect(within(rows[1]).getByText('Short sample')).toHaveAttribute('title', expect.stringContaining('fewer than 10 trades'))
    expect(screen.getByText('Ranked by Sharpe among strategies with 180+ days and 10+ trades; short samples follow.')).toBeInTheDocument()
  })

  it('recovers from a failed leaderboard and failed curves, then charts the top strategies', async () => {
    vi.mocked(api.strategies).mockRejectedValueOnce(new ApiError(500, 'internal', 'Leaderboard unavailable'))
    const detail = vi.spyOn(api, 'strategy').mockRejectedValue(new ApiError(502, 'upstream', 'x'))
    const { user } = open('/strategies?tab=top')
    expect(await screen.findByRole('heading', { name: "Couldn't load the leaderboard." })).toBeInTheDocument()
    await user.click(screen.getAllByRole('button', { name: 'Try again' })[1])

    expect(await screen.findByText('Equity curves are unavailable right now.')).toBeInTheDocument()
    expect(detail).toHaveBeenCalledTimes(3)
    detail.mockImplementation(async (id) => ({ strategy: fx.strategy({ id, name: top.find((s) => s.id === id)!.name }) }))
    await user.click(screen.getByRole('button', { name: 'Try again' }))
    await waitFor(() => expect(screen.getAllByRole('listitem').map((li) => li.textContent)).toEqual(expect.arrayContaining(['Donchian breakout', 'MACD trend'])))
    await waitFor(() => expect(charts.at(-1)!.series.map((s) => s.options.color)).toEqual(['#C8962E', '#3B6FD8', '#0E9F8E']))

    await user.click(screen.getByRole('button', { name: 'All' }))
    expect(screen.getByText('Daily closes; each curve starts at its first backtest day.')).toBeInTheDocument()
  })

  it('ranks by Sharpe and sorts by any column', async () => {
    const { user } = open('/strategies?tab=top')
    const names = () => within(screen.getByRole('table')).getAllByRole('row').slice(1).map((r) => within(r).getByRole('link').textContent)
    await waitFor(() => expect(names()).toEqual(['Donchian breakout', 'RSI mean revert', 'MACD trend']))
    expect(screen.getByText('Foretrade Labs · 3 built-in strategies')).toBeInTheDocument()
    expect(within(screen.getByRole('table')).getAllByText('Foretrade Labs')[0]).toBeInTheDocument()

    const header = (name: string) => screen.getByRole('button', { name: new RegExp(`^${name}`) })
    await user.click(header('Total return'))
    expect(names()).toEqual(['RSI mean revert', 'Donchian breakout', 'MACD trend']) // best first
    expect(header('Total return').closest('th')).toHaveAttribute('aria-sort', 'descending')
    await user.click(header('Total return'))
    expect(names()).toEqual(['MACD trend', 'Donchian breakout', 'RSI mean revert'])

    await user.click(header('Max DD'))
    expect(names()).toEqual(['RSI mean revert', 'Donchian breakout', 'MACD trend']) // smallest drawdown first
    expect(header('Max DD').closest('th')).toHaveAttribute('aria-sort', 'ascending')
    await user.click(header('Strategy'))
    expect(names()).toEqual(['Donchian breakout', 'MACD trend', 'RSI mean revert'])
  })

  it('shows each backtest window next to Sharpe, and "—" for a return too short to annualize', async () => {
    open('/strategies?tab=top')
    const heads = within(await screen.findByRole('table')).getAllByRole('columnheader').map((h) => h.textContent?.replace(/[▲▼]/g, ''))
    expect(heads.slice(heads.indexOf('Sharpe'), heads.indexOf('Sharpe') + 2)).toEqual(['Sharpe', 'Window'])
    const row = (name: string) => screen.getByRole('link', { name }).closest('tr')!
    await waitFor(() => expect(row('MACD trend')).toHaveTextContent('42 d'))
    expect(row('MACD trend').cells[4]).toHaveTextContent(/^—$/) // Ann. return
    expect(row('RSI mean revert')).toHaveTextContent('2.7 y')
    expect(row('Donchian breakout')).toHaveTextContent('126 d')
  })

  it('opens a strategy from its row and switches back to the studio', async () => {
    vi.spyOn(api, 'strategy').mockResolvedValue({ strategy: fx.strategy({ id: 'b', author: 'Foretrade Labs', name: 'RSI mean revert' }) })
    const { user } = open('/strategies?tab=top')
    const row = (await screen.findByRole('link', { name: 'RSI mean revert' })).closest('tr')!
    await user.click(row.querySelector('td')!)
    expect(currentPath()).toBe('/strategies/b')
    expect(await screen.findByRole('heading', { level: 1, name: 'RSI mean revert' })).toBeInTheDocument()
  })

  it('switches tabs through the URL', async () => {
    const { user } = open('/strategies?tab=top')
    await screen.findByRole('link', { name: 'Donchian breakout' })
    await user.click(screen.getByRole('button', { name: 'Studio' }))
    expect(currentPath()).toBe('/strategies')
    expect(screen.getByRole('heading', { name: 'Turn ideas into strategies.' })).toBeInTheDocument()
    await screen.findByRole('link', { name: /SOL EMA cross/ })
  })
})

describe('StrategiesPage · community', () => {
  it('switches the Top board between Foretrade Labs and Community through ?top=community', async () => {
    vi.mocked(api.strategies).mockImplementation(async (scope) => ({
      strategies: scope === 'community' ? [fx.strategySummary({ id: 'c1', name: 'ETH breakout', community: fx.publishedBy() })] : top,
    }))
    vi.spyOn(api, 'strategy').mockRejectedValue(new ApiError(502, 'upstream', 'x'))
    const { user } = open('/strategies?tab=top')
    expect(screen.getByRole('button', { name: 'Foretrade Labs' })).toHaveAttribute('aria-pressed', 'true')
    await user.click(screen.getByRole('button', { name: 'Community' }))
    expect(currentPath()).toBe('/strategies?tab=top&top=community')
    expect(await screen.findByRole('heading', { level: 1, name: 'Strategies other traders publish.' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'ETH breakout' }).closest('li')).toHaveTextContent('by Ann Lee')
    await user.click(screen.getByRole('button', { name: 'Foretrade Labs' }))
    expect(currentPath()).toBe('/strategies?tab=top')
    expect(await screen.findByRole('heading', { name: 'Top strategies, measured honestly.' })).toBeInTheDocument()
  })

  it('badges a published strategy in my studio', async () => {
    vi.mocked(api.strategies).mockImplementation(async (scope) => ({
      strategies: scope === 'top' ? top : [fx.strategySummary({ community: fx.community({ published: true }) }), fx.strategySummary({ id: 's2', name: 'Draft', community: fx.community() })],
    }))
    open()
    expect(await screen.findByRole('link', { name: /SOL EMA cross/ })).toHaveTextContent('Published')
    expect(screen.getByRole('link', { name: /Draft/ })).not.toHaveTextContent('Published')
  })
})

import { screen, waitFor, within } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError } from '../../lib/api'
import type { Metrics, Strategy } from '../../lib/types'
import { charts } from '../../test/chart'
import * as fx from '../../test/fixtures'
import { currentPath, renderPage, stubSession } from '../../test/utils'
import { StrategyDetailPage } from './StrategyDetailPage'

const open = (id = 's1') =>
  renderPage(<StrategyDetailPage />, { route: `/strategies/${id}`, path: '/strategies/:id', routes: { '/strategies': <p>strategy list</p> } })
const show = (s: Strategy) => vi.spyOn(api, 'strategy').mockResolvedValue({ strategy: s })
const tile = (label: string) => screen.getByText(label, { selector: '.label' }).parentElement!

beforeEach(() => {
  charts.length = 0
  stubSession()
  vi.spyOn(api, 'autopilots').mockResolvedValue({ autopilots: [] })
})

describe('StrategyDetailPage', () => {
  it('shows the header, metrics, chart, rules, trades and the AI read', async () => {
    const get = show(fx.strategy())
    open()
    expect(await screen.findByRole('heading', { level: 1, name: 'SOL EMA cross' })).toBeInTheDocument()
    expect(get).toHaveBeenCalledWith('s1', expect.any(AbortSignal))
    expect(screen.getByText('By Demo Trader')).toBeInTheDocument()
    expect(screen.getByText('Trend following on SOL 4h.')).toBeInTheDocument()
    expect(screen.getByText('You asked').parentElement).toHaveTextContent('“Buy SOL when EMA 20 crosses above EMA 50”')
    expect(screen.getByRole('button', { name: 'Deploy to autopilot' })).toBeEnabled()
    expect(screen.getByRole('link', { name: /Trade SOL manually/ })).toHaveAttribute('href', '/trade/SOL')
    expect(screen.getByText('Annualized +31.2% · total +24.5% over 126 days')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /Strategies/ })).toHaveAttribute('href', '/strategies')

    expect(tile('Total return')).toHaveTextContent('▲up 24.5%Buy & hold +12.1%')
    expect(tile('Sharpe')).toHaveTextContent('1.42')
    expect(tile('Max drawdown')).toHaveTextContent('−18.4%')
    expect(tile('Win rate')).toHaveTextContent('48%')
    expect(tile('Profit factor')).toHaveTextContent('1.80')
    expect(tile('Exposure')).toHaveTextContent('41%time in market')
    expect(screen.getByText(/1,000 × 4h bars/)).toBeInTheDocument()

    await waitFor(() => expect(charts.at(-1)?.series).toHaveLength(2)) // strategy + buy & hold
    expect(screen.getAllByRole('listitem').map((li) => li.textContent)).toEqual(expect.arrayContaining(['Strategy', 'Buy & hold SOL']))

    expect(screen.getByText('Enter long when EMA(20) crosses above EMA(50).')).toBeInTheDocument()
    expect(screen.getByText('Exit long when EMA(20) crosses below EMA(50).')).toBeInTheDocument()
    expect(screen.getByText('Stop loss 4%')).toBeInTheDocument()
    expect(screen.getByText('No take profit')).toBeInTheDocument()

    expect(screen.getByRole('heading', { name: '2 trades' })).toBeInTheDocument()
    const rows = within(screen.getByRole('table')).getAllByRole('row').slice(1)
    expect(rows[0]).toHaveTextContent('Short') // newest first
    expect(rows[0]).toHaveTextContent('Stop loss')
    expect(rows[1]).toHaveTextContent('Signal')
    expect(screen.getByText('beat', { selector: 'strong' })).toBeInTheDocument()
  })

  it('re-runs a tuned backtest for free and can reset to the saved one', async () => {
    show(fx.strategy())
    const bt = vi.spyOn(api, 'backtest').mockResolvedValue({
      metrics: { ...fx.strategy().metrics, totalReturnPct: 30, sharpe: 1.6 },
      equity: fx.series([100, 130]),
      benchmark: fx.series([100, 112]),
      trades: [],
    })
    const { user } = open()
    const stop = await screen.findByLabelText('Stop loss')
    expect(stop).toHaveValue('4')
    const rerun = screen.getByRole('button', { name: /Re-run backtest/ })
    expect(rerun).toBeDisabled()
    expect(screen.getByText('Edit a value to compare against the current backtest.')).toBeInTheDocument()

    await user.clear(stop)
    await user.type(stop, '3')
    await user.type(screen.getByLabelText('Take profit'), '9')
    await user.click(rerun)
    expect(bt).toHaveBeenCalledWith({ ...fx.strategy().spec, stopLossPct: 3, takeProfitPct: 9, positionPct: 100 })
    expect(await screen.findByText('Backtest re-run: +30.0% total return.')).toBeInTheDocument()
    expect(screen.getByText('Modified · not saved')).toBeInTheDocument()
    expect(tile('Total return')).toHaveTextContent('was +24.5%')
    expect(tile('Sharpe')).toHaveTextContent('was 1.42')
    expect(screen.getByText('Stop loss 3%')).toBeInTheDocument()
    expect(screen.getByText('No trades: the entry rules never fired in this window. Loosen a threshold or try a longer interval.')).toBeInTheDocument()
    expect(screen.getByText('Written for the saved backtest, not the modified one.')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Reset to saved' }))
    expect(screen.queryByText('Modified · not saved')).toBeNull()
    expect(screen.getByLabelText('Stop loss')).toHaveValue('4')
  })

  it('shows the exact rule spec as JSON and copies it', async () => {
    const spec = { ...fx.strategy().spec, trailingStopPct: 3 }
    show(fx.strategy({ spec }))
    const { user } = open()
    const details = (await screen.findByText('Rule spec (JSON)')).closest('details')!
    expect(details).not.toHaveAttribute('open')
    await user.click(screen.getByText('Rule spec (JSON)'))
    expect(details).toHaveAttribute('open')
    expect(within(details).getByText('This JSON is exactly what the backtester and Autopilot run. No hidden code.')).toBeInTheDocument()
    const pre = details.querySelector('pre')!
    expect(pre.textContent).toBe(JSON.stringify(spec, null, 2))
    expect(pre).toHaveClass('font-mono', 'overflow-auto', 'max-h-80')
    await user.click(within(details).getByRole('button', { name: 'Copy' }))
    expect(await navigator.clipboard.readText()).toBe(JSON.stringify(spec, null, 2))
    expect(await screen.findByText('Spec copied')).toBeInTheDocument()
    // The readout and the Tune form show the trailing stop too.
    expect(screen.getByText('Trailing stop 3%')).toBeInTheDocument()
    expect(screen.getByLabelText('Trailing stop')).toHaveValue('3')
  })

  it('tunes the trailing stop', async () => {
    show(fx.strategy())
    const bt = vi.spyOn(api, 'backtest').mockResolvedValue({ metrics: fx.strategy().metrics, equity: [], benchmark: [], trades: [] })
    const { user } = open()
    const trail = await screen.findByLabelText('Trailing stop')
    expect(trail).toHaveValue('')
    expect(trail).toHaveAttribute('placeholder', 'off')
    await user.type(trail, '0.2')
    expect(screen.getByText('Use 0.5–50%, or leave blank for none.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Re-run backtest/ })).toBeDisabled()
    await user.clear(trail)
    await user.type(trail, '2.5')
    await user.click(screen.getByRole('button', { name: /Re-run backtest/ }))
    expect(bt).toHaveBeenCalledWith({ ...fx.strategy().spec, stopLossPct: 4, positionPct: 100, trailingStopPct: 2.5 })
    expect(await screen.findByText('Trailing stop 2.5%')).toBeInTheDocument()
  })

  it('shows Sortino, volatility, Calmar and fees, and "—" where a short window has no annualized return', async () => {
    const m = fx.strategy().metrics
    show(fx.strategy({ metrics: { ...m, annualizedReturnPct: null, calmar: null, winRatePct: null, endT: m.startT + 42 * 86400 } }))
    open()
    await screen.findByRole('heading', { level: 1, name: 'SOL EMA cross' })
    expect(tile('Sortino')).toHaveTextContent('2.05downside risk')
    expect(tile('Volatility')).toHaveTextContent('39%annualized')
    expect(tile('Calmar')).toHaveTextContent('—ann. return ÷ max DD')
    expect(tile('Fees')).toHaveTextContent('2.31%of starting capital')
    expect(tile('Annualized')).toHaveTextContent('—needs 90+ days')
    expect(tile('Win rate')).toHaveTextContent('—no closed trades')
    expect(screen.getByText('Total return +24.5% over 42 days')).toBeInTheDocument()
  })

  it('says a quote-feed run is annualized on its trading sessions, and a crypto run says nothing of it', async () => {
    const m = fx.strategy().metrics
    show(fx.strategy({ metrics: { ...m, barsPerYear: 1309.6 } }))
    const { unmount } = open()
    await screen.findByRole('heading', { level: 1, name: 'SOL EMA cross' })
    expect(screen.getByText(/0% risk-free rate/)).toHaveTextContent(/0% risk-free rate · annualized on trading sessions, about 1,310 bars a year$/)
    unmount()
    show(fx.strategy())
    open()
    await screen.findByRole('heading', { level: 1, name: 'SOL EMA cross' })
    expect(screen.getByText(/0% risk-free rate/)).not.toHaveTextContent('trading sessions')
  })

  it('holds back the annualized return under 10 trades, keeping the 90-day rule first', async () => {
    const m = fx.strategy().metrics
    show(fx.strategy({ metrics: { ...m, trades: 9 } }))
    open()
    await screen.findByRole('heading', { level: 1, name: 'SOL EMA cross' })
    expect(tile('Annualized')).toHaveTextContent('—needs 10+ trades')
    expect(screen.getByText('Total return +24.5% over 126 days')).toBeInTheDocument()
  })

  it('deploys to the autopilot and shows its card, which a running one already has', async () => {
    show(fx.strategy())
    vi.spyOn(api, 'autopilot').mockResolvedValue({ autopilot: fx.autopilot(), trades: [] })
    vi.spyOn(api, 'deployAutopilot').mockResolvedValue({ autopilot: fx.autopilot(), trade: null, portfolio: fx.portfolio })
    const { user } = open()
    await user.click(await screen.findByRole('button', { name: 'Deploy to autopilot' }))
    await user.click(screen.getByRole('button', { name: 'Deploy (paper)' }))
    const card = await screen.findByRole('region', { name: 'Autopilot SOL EMA cross' })
    expect(within(card).getByText('Running')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Autopilot running' })).toBeDisabled()
  })

  it('shows the newest autopilot of this strategy only', async () => {
    show(fx.strategy())
    vi.spyOn(api, 'autopilots').mockResolvedValue({
      autopilots: [fx.autopilot({ id: 'ap2', strategyId: 'other' }), fx.autopilot({ status: 'stopped', stopReason: 'user', positionQty: 0 })],
    })
    vi.spyOn(api, 'autopilot').mockResolvedValue({ autopilot: fx.autopilot({ status: 'stopped', stopReason: 'user', positionQty: 0 }), trades: [] })
    open()
    expect(await screen.findByText('Stopped (user)')).toBeInTheDocument()
    expect(screen.getAllByRole('region', { name: 'Autopilot SOL EMA cross' })).toHaveLength(1)
    expect(screen.getByRole('button', { name: 'Deploy to autopilot' })).toBeEnabled()
  })

  it('shows "—" for the metrics a legacy strategy never stored, also after a re-tune', async () => {
    const { sortino, volatilityAnnPct, feesPct, ...rest } = fx.strategy().metrics
    void [sortino, volatilityAnnPct, feesPct]
    show(fx.strategy({ metrics: { ...rest, calmar: null } as Metrics })) // as the API sends it: absent, calmar null
    vi.spyOn(api, 'backtest').mockResolvedValue({ metrics: fx.strategy().metrics, equity: [], benchmark: [], trades: [] })
    const { user } = open()
    await screen.findByRole('heading', { level: 1, name: 'SOL EMA cross' })
    for (const label of ['Sortino', 'Volatility', 'Calmar', 'Fees']) expect(tile(label).children[1]).toHaveTextContent(/^—$/)
    expect(tile('Annualized')).toHaveTextContent('31.2%')
    expect(tile('Annualized')).not.toHaveTextContent('needs 90+ days')

    await user.type(screen.getByLabelText('Take profit'), '9')
    await user.click(screen.getByRole('button', { name: /Re-run backtest/ }))
    await screen.findByText('Modified · not saved')
    expect(tile('Sortino')).toHaveTextContent('2.05downside risk') // no "was —" for a value the saved run never had
    expect(tile('Calmar')).toHaveTextContent('1.70ann. return ÷ max DD')
  })

  it('validates tune fields and shows backtest errors', async () => {
    show(fx.strategy())
    vi.spyOn(api, 'backtest').mockRejectedValue(new ApiError(400, 'bad_request', 'positionPct out of range'))
    const { user } = open()
    const size = await screen.findByLabelText('Position')
    await user.clear(size)
    await user.type(size, '0')
    expect(screen.getByText('Use 1–100% of equity.')).toBeInTheDocument()
    expect(size).toHaveAttribute('aria-invalid', 'true')
    expect(screen.getByRole('button', { name: /Re-run backtest/ })).toBeDisabled()
    await user.clear(size)
    await user.type(size, '50')
    await user.click(screen.getByRole('button', { name: /Re-run backtest/ }))
    expect(await screen.findByRole('alert')).toHaveTextContent('positionPct out of range')
  })

  it('treats built-in strategies as read-only and links back to the leaderboard', async () => {
    show(
      fx.strategy({
        author: 'Foretrade Labs',
        prompt: '',
        spec: { ...fx.strategy().spec, interval: '1d', longEntry: undefined, longExit: undefined, stopLossPct: undefined, takeProfitPct: 6 },
        metrics: { ...fx.strategy().metrics, profitFactor: 100 },
        equity: [],
        trades: Array.from({ length: 100 }, (_, i) => ({ side: 'long' as const, entryT: 1_790_899_200 + i, exitT: 1_790_985_600, entry: 1, exit: 2, pnlPct: 1, reason: 'take_profit' as const })),
        explanation: '',
      }),
    )
    open()
    expect(await screen.findByText('Foretrade Labs')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Delete' })).toBeNull()
    expect(screen.queryByText('You asked')).toBeNull()
    expect(screen.getByRole('link', { name: /Strategies/ })).toHaveAttribute('href', '/strategies?tab=top')
    expect(tile('Profit factor')).toHaveTextContent('∞')
    expect(screen.getByText('This strategy has no readable entry rules.')).toBeInTheDocument()
    expect(screen.getByText('Take profit 6%')).toBeInTheDocument()
    expect(screen.getByText('No equity curve for this backtest.')).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Last 100 trades' })).toBeInTheDocument()
    expect(within(screen.getByRole('table')).getAllByText('Oct 2, 2026')[0]).toBeInTheDocument() // daily bars: calendar dates
    expect(screen.queryByText('What the numbers say')).toBeNull()
  })

  it('deletes an own strategy after a confirm', async () => {
    show(fx.strategy())
    const del = vi.spyOn(api, 'deleteStrategy').mockResolvedValue(undefined)
    const { user } = open()
    await user.click(await screen.findByRole('button', { name: 'Delete' }))
    const dialog = screen.getByRole('dialog', { name: 'Delete “SOL EMA cross”?' })
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    expect(dialog).not.toHaveAttribute('open')
    await user.click(screen.getByRole('button', { name: 'Delete' }))
    await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Delete strategy' }))
    expect(del).toHaveBeenCalledWith('s1')
    expect(await screen.findByText('strategy list')).toBeInTheDocument()
    expect(currentPath()).toBe('/strategies')
    expect(screen.getByText('Deleted "SOL EMA cross".')).toBeInTheDocument()
  })

  it('toasts a failed delete', async () => {
    show(fx.strategy())
    vi.spyOn(api, 'deleteStrategy').mockRejectedValue(new ApiError(500, 'internal', ''))
    const { user } = open()
    await user.click(await screen.findByRole('button', { name: 'Delete' }))
    await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Delete strategy' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Could not delete the strategy.')
  })

  it('says when a strategy does not exist', async () => {
    vi.spyOn(api, 'strategy').mockRejectedValue(new ApiError(404, 'not_found', 'Not found'))
    open('gone')
    expect(await screen.findByRole('heading', { name: "This strategy doesn't exist." })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Try again' })).toBeNull()
    expect(screen.getByRole('link', { name: 'Back to strategies' })).toHaveAttribute('href', '/strategies')
  })

  it('retries a failed load', async () => {
    const get = vi.spyOn(api, 'strategy').mockRejectedValueOnce(new ApiError(0, 'network', 'Cannot reach the server.'))
    get.mockResolvedValueOnce({ strategy: fx.strategy() })
    const { user } = open()
    expect(screen.getByRole('generic', { busy: true })).toHaveAccessibleName('Loading strategy')
    expect(await screen.findByRole('heading', { name: "Couldn't load this strategy." })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Try again' }))
    await waitFor(() => expect(screen.getByRole('heading', { level: 1, name: 'SOL EMA cross' })).toBeInTheDocument())
  })
})

describe('StrategyDetailPage · optimize stops', () => {
  it('draws the Sharpe heatmap and applies a top pair through the free backtest', async () => {
    show(fx.strategy())
    const opt = vi.spyOn(api, 'optimize').mockResolvedValue(fx.optimizeResult)
    const bt = vi.spyOn(api, 'backtest').mockResolvedValue({ metrics: { ...fx.strategy().metrics, sharpe: 1.64 }, equity: [], benchmark: [], trades: [] })
    const { user } = open()
    const toggle = await screen.findByRole('button', { name: 'Optimize stops' })
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    await user.click(toggle)
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    expect(opt).toHaveBeenCalledWith(fx.strategy().spec)

    const table = await screen.findByRole('table', { name: /Sharpe ratio of the same rules/ })
    expect(within(table).getAllByRole('columnheader').map((h) => h.textContent)).toEqual(['Target 2%', 'Target 4%', 'Target 6%', 'Target 10%', 'Target 15%'])
    expect(within(table).getAllByRole('rowheader').map((h) => h.textContent)).toEqual(['Stop 1%', 'Stop 2%', 'Stop 3%', 'Stop 5%', 'Stop 8%'])
    const best = within(table).getByRole('cell', { name: /^1\.64/ }) // jsdom has no CSS: the hidden tooltip joins the name
    expect(best).toHaveAccessibleDescription('Return +16.4% · Max DD −13.0% · 12 trades')
    expect(best).not.toHaveAttribute('aria-current') // the saved spec (4% stop, no target) is off the grid
    expect(screen.getByText('Best of 25 tries on the same history: in-sample, expect worse live.')).toBeInTheDocument()
    expect(screen.getByText(/^Before optimizing:/)).toHaveTextContent('Before optimizing: Stop 4% · Target none · Sharpe 1.12')

    const chips = within(screen.getByRole('list', { name: 'Best pairs' })).getAllByRole('listitem')
    expect(chips.map((c) => c.textContent)).toEqual([
      'Stop 3% · Target 10% · Sharpe 1.64Apply',
      'Stop 8% · Target 10% · Sharpe 0.75Apply',
      'Stop 5% · Target 15% · Sharpe 0.70Apply',
    ])
    await user.click(screen.getByRole('button', { name: 'Apply stop 3%, target 10%' }))
    expect(bt).toHaveBeenCalledWith({ ...fx.strategy().spec, stopLossPct: 3, takeProfitPct: 10, positionPct: 100 })
    expect(await screen.findByText('Modified · not saved')).toBeInTheDocument()
    expect(screen.getByLabelText('Stop loss')).toHaveValue('3') // the Tune form shows the applied pair
    expect(screen.getByLabelText('Take profit')).toHaveValue('10')
    expect(within(screen.getByRole('table', { name: /Sharpe ratio/ })).getByRole('cell', { name: /^1\.64/ })).toHaveAttribute('aria-current', 'true')
    expect(opt).toHaveBeenCalledTimes(1) // same position size: the grid is not re-run
  })

  it('shows optimizer and apply errors', async () => {
    show(fx.strategy())
    vi.spyOn(api, 'optimize').mockRejectedValueOnce(new ApiError(400, 'bad_request', 'not enough history'))
    const { user } = open()
    await user.click(await screen.findByRole('button', { name: 'Optimize stops' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('not enough history')

    await user.click(screen.getByRole('button', { name: 'Optimize stops' })) // close, then reopen: it loads again
    vi.spyOn(api, 'optimize').mockResolvedValue(fx.optimizeResult)
    vi.spyOn(api, 'backtest').mockRejectedValue(new ApiError(502, 'upstream', 'Market data is unavailable.'))
    await user.click(screen.getByRole('button', { name: 'Optimize stops' }))
    await user.click(await screen.findByRole('button', { name: 'Apply stop 8%, target 10%' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Market data is unavailable.')
    expect(screen.queryByText('Modified · not saved')).toBeNull()
  })

  it('says when no pair made enough trades to rank', async () => {
    show(fx.strategy())
    vi.spyOn(api, 'optimize').mockResolvedValue({ ...fx.optimizeResult, best: [] })
    const { user } = open()
    await user.click(await screen.findByRole('button', { name: 'Optimize stops' }))
    expect(await screen.findByText('No pair made 5 or more trades.')).toBeInTheDocument()
  })
})

describe('StrategyDetailPage · community', () => {
  it('publishes an own strategy after the confirm, then unpublishes it', async () => {
    show(fx.strategy({ community: fx.community() }))
    const publish = vi
      .spyOn(api, 'publishStrategy')
      .mockResolvedValueOnce({ strategy: fx.strategy({ community: fx.community({ published: true, publishedAt: '2026-10-04T09:00:00Z', stars: 3, copies: 1 }) }) })
      .mockResolvedValueOnce({ strategy: fx.strategy({ community: fx.community({ stars: 3, copies: 1 }) }) })
    const { user } = open()
    await user.click(await screen.findByRole('button', { name: 'Publish to community' }))
    const dialog = screen.getByRole('dialog', { name: 'Publish “SOL EMA cross”?' })
    expect(dialog).toHaveTextContent(
      "Publishing shows your display name, this strategy's rules, backtest and candle-derived live forward to every Foretrade user. Autopilot results stay private.",
    )
    await user.click(within(dialog).getByRole('button', { name: 'Publish' }))
    expect(publish).toHaveBeenCalledWith('s1', true)
    expect(await screen.findByText('Published · ★ 3 · 1 copy')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Unpublish' }))
    expect(publish).toHaveBeenLastCalledWith('s1', false)
    expect(await screen.findByRole('button', { name: 'Publish to community' })).toBeInTheDocument()
    expect(screen.queryByText(/^Published ·/)).toBeNull()
  })

  it('shows the 409 publish gate inline in the dialog', async () => {
    show(fx.strategy({ community: fx.community() }))
    vi.spyOn(api, 'publishStrategy').mockRejectedValue(new ApiError(409, 'conflict', 'Backtest at least 3 trades before publishing.'))
    const { user } = open()
    await user.click(await screen.findByRole('button', { name: 'Publish to community' }))
    const dialog = screen.getByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: 'Publish' }))
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('Backtest at least 3 trades before publishing.')
    expect(dialog).toHaveAttribute('open')
  })

  it("reads another user's published strategy: Star and Copy, no Deploy, Tune, Optimize or Delete", async () => {
    show(fx.strategy({ author: 'Ann Lee', community: fx.publishedBy() }))
    const copy = vi.spyOn(api, 'copyStrategy').mockResolvedValue({
      strategy: fx.strategy({ id: 's9', community: fx.community({ copiedFrom: { id: 's1', name: 'SOL EMA cross', owner: 'Ann Lee' } }) }),
    })
    const { user } = open()
    expect(await screen.findByText('by Ann Lee')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Star · 4 stars' })).toHaveAttribute('aria-pressed', 'false')
    for (const name of ['Deploy to autopilot', 'Optimize stops', /Re-run backtest/, 'Delete', 'Publish to community'])
      expect(screen.queryByRole('button', { name })).toBeNull()
    expect(screen.queryByText('You asked')).toBeNull()
    expect(screen.getByRole('link', { name: /Strategies/ })).toHaveAttribute('href', '/strategies?tab=top&top=community')

    await user.click(screen.getByRole('button', { name: 'Copy to my studio' }))
    expect(copy).toHaveBeenCalledWith('s1')
    expect(await screen.findByText('Copied to your studio. Backtest re-run on fresh data.')).toBeInTheDocument()
    // navigate() runs in a transition, so the toast can paint first.
    await waitFor(() => expect(currentPath()).toBe('/strategies/s9'))
    // The copy is mine: it deploys, and says where it came from.
    expect(await screen.findByRole('button', { name: 'Deploy to autopilot' })).toBeInTheDocument()
    expect(screen.getByText(/^Copied from/)).toHaveTextContent('Copied from SOL EMA cross by Ann Lee')
    expect(screen.getByRole('link', { name: 'SOL EMA cross' })).toHaveAttribute('href', '/strategies/s1')
  })

  it('copies a built-in Labs strategy too, next to Deploy', async () => {
    show(fx.strategy({ id: 'lab1', author: 'Foretrade Labs', prompt: '', community: null }))
    const copy = vi.spyOn(api, 'copyStrategy').mockResolvedValue({ strategy: fx.strategy({ id: 's7', community: fx.community() }) })
    const { user } = open('lab1')
    await user.click(await screen.findByRole('button', { name: 'Copy to my studio' }))
    expect(copy).toHaveBeenCalledWith('lab1')
    await waitFor(() => expect(currentPath()).toBe('/strategies/s7'))
    expect(screen.getByRole('button', { name: 'Deploy to autopilot' })).toBeInTheDocument()
  })

  it('toasts a failed copy', async () => {
    show(fx.strategy({ author: 'Ann Lee', community: fx.publishedBy() }))
    vi.spyOn(api, 'copyStrategy').mockRejectedValue(new ApiError(404, 'not_found', 'Not found.'))
    const { user } = open()
    await user.click(await screen.findByRole('button', { name: 'Copy to my studio' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Not found.')
    expect(currentPath()).toBe('/strategies/s1')
  })
})

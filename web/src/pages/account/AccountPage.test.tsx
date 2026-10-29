import { act, screen, waitFor, within } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError } from '../../lib/api'
import { fmtUsd } from '../../lib/format'
import { charts } from '../../test/chart'
import * as fx from '../../test/fixtures'
import { renderPage, stubSession } from '../../test/utils'
import type { MarketPosition } from '../../lib/types'
import { AccountPage, defaultRange } from './AccountPage'

const open = () => renderPage(<AccountPage />, { route: '/account', path: '/account' })
const dialog = () => screen.getByRole('dialog', { name: /Reset to a fresh/ })
const hoursAgo = (h: number) => new Date(Date.now() - h * 3600_000).toISOString()

beforeEach(() => {
  vi.spyOn(api, 'copyPlans').mockResolvedValue({ plans: [] })
  vi.spyOn(api, 'trades').mockResolvedValue({ trades: [fx.trade({ createdAt: hoursAgo(72) })] }) // 3 days old: opens on 7D
  vi.spyOn(api, 'portfolioEquity').mockResolvedValue(fx.equityCurve())
  vi.spyOn(api, 'portfolioAttribution').mockResolvedValue(fx.attribution)
  vi.spyOn(api, 'portfolioReview').mockResolvedValue(fx.review())
})

describe('AccountPage', () => {
  it('shows the equity hero with P&L and exposure', async () => {
    stubSession()
    open()
    const hero = screen.getByRole('region', { name: 'Account summary' })
    expect(await within(hero).findByText('$100,194.00')).toBeInTheDocument()
    expect(hero).toHaveTextContent('▲+$194.00 (+0.19%)')
    expect(hero).toHaveTextContent('all time, from $100,000')
    expect(within(hero).getByText('Buying power').nextSibling).toHaveTextContent('$194,388.00')
    expect(within(hero).getByText('Open positions').nextSibling).toHaveTextContent('1')
    expect(within(hero).getByText('Realized P&L').nextSibling).toHaveTextContent('-$6.00')
    expect(hero).toHaveTextContent('3% of the 2× limit')
    expect(screen.getByRole('heading', { name: '1 position' })).toBeInTheDocument()
    await waitFor(() => expect(screen.getAllByRole('table')).toHaveLength(4)) // stress, positions, P&L by source, trade history
    const history = screen.getByRole('heading', { name: 'Every simulated fill' }).closest('.card') as HTMLElement
    expect(within(history).getByRole('table')).toHaveTextContent('Manual')
  })

  it('shows placeholders while loading and empty states for a fresh account', async () => {
    stubSession({ portfolio: fx.emptyPortfolio })
    vi.mocked(api.trades).mockResolvedValue({ trades: [] })
    vi.mocked(api.portfolioAttribution).mockResolvedValue({ sources: [], totalPnl: 0 })
    open()
    expect(screen.getByRole('button', { name: 'Reset portfolio' })).toBeDisabled() // until the portfolio loads
    expect(await screen.findByText('No open positions')).toBeInTheDocument()
    expect(await screen.findByText('No trades yet')).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: '0 positions' })).toBeInTheDocument()
    expect(screen.getAllByRole('link', { name: /Open the trade terminal/ })[0]).toHaveAttribute('href', '/trade/BTC')
    expect(screen.getAllByRole('link', { name: /Ask the Agent/ })[0]).toHaveAttribute('href', '/')
  })

  it('resets the account after a confirm', async () => {
    stubSession()
    const reset = vi.spyOn(api, 'resetPortfolio').mockResolvedValue({ portfolio: { ...fx.emptyPortfolio, revision: 2 } })
    const { user } = open()
    await screen.findByText('$100,194.00', { selector: 'div' })
    await user.click(screen.getByRole('button', { name: 'Reset portfolio' }))
    expect(dialog()).toHaveAttribute('open')
    expect(dialog()).toHaveTextContent('This deletes your open position and your whole trade history')
    await user.click(within(dialog()).getByRole('button', { name: 'Reset portfolio' }))
    expect(reset).toHaveBeenCalledTimes(1)
    expect(await screen.findByText('Paper account reset. Cash is back to $100,000.00.')).toBeInTheDocument()
    expect(screen.getByRole('dialog', { hidden: true })).not.toHaveAttribute('open')
    await waitFor(() => expect(api.trades).toHaveBeenCalledTimes(2)) // history reloaded
    await waitFor(() => expect(api.portfolioReview).toHaveBeenCalledTimes(2)) // the health card follows the revision
    await waitFor(() => expect(api.portfolioEquity).toHaveBeenCalledTimes(2)) // a new revision: the curve and P&L reload
    expect(api.portfolioAttribution).toHaveBeenCalledTimes(2)
    expect(await screen.findByText('No open positions')).toBeInTheDocument()
  })

  it('can cancel the reset', async () => {
    stubSession()
    const reset = vi.spyOn(api, 'resetPortfolio')
    const { user } = open()
    await screen.findByText('$100,194.00', { selector: 'div' })
    await user.click(screen.getByRole('button', { name: 'Reset portfolio' }))
    await user.click(within(dialog()).getByRole('button', { name: 'Cancel' }))
    expect(screen.getByRole('dialog', { hidden: true })).not.toHaveAttribute('open')
    expect(reset).not.toHaveBeenCalled()
  })

  it('toasts a failed reset and keeps the dialog open', async () => {
    stubSession({ portfolio: { ...fx.portfolio, positions: [fx.position, { ...fx.position, symbol: 'SOL' }] } })
    vi.spyOn(api, 'resetPortfolio').mockRejectedValue(new ApiError(500, 'internal', ''))
    const { user } = open()
    await screen.findByRole('heading', { name: '2 positions' })
    await user.click(screen.getByRole('button', { name: 'Reset portfolio' }))
    expect(dialog()).toHaveTextContent('This deletes all 2 open positions and your whole trade history')
    await user.click(within(dialog()).getByRole('button', { name: 'Reset portfolio' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Could not reset the account. Please try again.')
    await waitFor(() => expect(within(dialog()).getByRole('button', { name: 'Reset portfolio' })).toBeEnabled())
  })

  it('draws the equity curve and switches its range', async () => {
    stubSession()
    const { user } = open()
    const card = (await screen.findByRole('heading', { name: 'Equity' })).closest('.card') as HTMLElement
    await waitFor(() => expect(charts.at(-1)?.series[0]?.setData).toHaveBeenCalledWith(fx.equityCurve().points.map((p) => ({ time: p.t, value: p.v }))))
    expect(charts.at(-1)!.series[0].options).toMatchObject({ title: 'Equity' })
    expect(charts.at(-1)!.options).toMatchObject({ localization: { priceFormatter: fmtUsd } })
    expect(card).toHaveTextContent('Rebuilt from your fills and venue candle closes (Binance crypto / Hyperliquid perps) · started at $100,000')
    expect(api.portfolioEquity).toHaveBeenCalledWith('7d', expect.any(AbortSignal))
    expect(within(card).getByRole('button', { name: '7D' })).toHaveAttribute('aria-pressed', 'true')

    vi.mocked(api.portfolioEquity).mockResolvedValue(fx.equityCurve({ range: '30d', interval: '4h' }))
    await user.click(within(card).getByRole('button', { name: '30D' }))
    expect(api.portfolioEquity).toHaveBeenLastCalledWith('30d', expect.any(AbortSignal))
    expect(within(card).getByRole('button', { name: '30D' })).toHaveAttribute('aria-pressed', 'true')
    await waitFor(() => expect(card.querySelector('.animate-shimmer')).toBeNull())
  })

  it('shows a retry row when the equity curve fails', async () => {
    stubSession()
    vi.mocked(api.portfolioEquity).mockRejectedValueOnce(new ApiError(502, 'upstream', 'Market data is unavailable right now.'))
    const { user } = open()
    expect(await screen.findByText('Market data is unavailable right now.')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(screen.queryByText('Market data is unavailable right now.')).toBeNull())
    expect(api.portfolioEquity).toHaveBeenCalledTimes(2)
  })

  it('splits P&L by source, summing to the total', async () => {
    stubSession()
    open()
    const card = (await screen.findByRole('heading', { name: 'P&L by source' })).closest('.card') as HTMLElement
    const rows = within(card).getAllByRole('row')
    expect(rows.map((r) => r.textContent)).toEqual([
      'SourceTradesRealizedOpenTotal',
      'Agent2▲up $150.50▲up $200.00▲up $350.50',
      'Manual1▼down $6.00▼down $150.50▼down $156.50',
      'All sources3▲up $144.50▲up $49.50▲up $194.00',
    ])
    expect(card).toHaveTextContent('Positions are split into lots by the source that opened them (first in, first out); stops and targets count toward that source. Fees included.')
  })

  it('hides P&L by source with no trades', async () => {
    stubSession({ portfolio: fx.emptyPortfolio })
    vi.mocked(api.portfolioAttribution).mockResolvedValue({ sources: [], totalPnl: 0 })
    open()
    await screen.findByRole('heading', { name: 'Equity' })
    await waitFor(() => expect(api.portfolioAttribution).toHaveBeenCalled())
    expect(screen.queryByRole('heading', { name: 'P&L by source' })).toBeNull()
  })
})

describe('AccountPage · history origins and the calm equity chart', () => {
  it('links each fill to the chat, AI portfolio or strategy it came from', async () => {
    stubSession()
    vi.mocked(api.trades).mockResolvedValue({
      trades: [
        fx.trade({ id: 'a', source: 'agent', ref: { kind: 'chat', id: 'c1', label: 'Long BTC into the ETF flows?' } }),
        fx.trade({ id: 'b', source: 'portfolio', note: 'Layer-1 core', ref: { kind: 'portfolio', id: 'p1', label: 'Layer-1 core' } }),
        fx.trade({ id: 'c', source: 'autopilot', ref: { kind: 'autopilot', id: 'lab-sol-ema', label: 'SOL EMA cross' } }),
        fx.trade({ id: 'd', source: 'portfolio', ref: null }), // a deleted portfolio
        fx.trade({ id: 'e', createdAt: hoursAgo(5) }),
      ],
    })
    open()
    const history = (await screen.findByRole('heading', { name: 'Every simulated fill' })).closest('.card') as HTMLElement
    const link = (name: string) => within(history).getByRole('link', { name })
    await waitFor(() => expect(link('Agent')).toHaveAttribute('href', '/chat/c1'))
    expect(link('Agent')).toHaveAttribute('title', 'Long BTC into the ETF flows?')
    expect(link('AI portfolio')).toHaveAttribute('href', '/portfolios/p1')
    expect(link('AI portfolio')).toHaveAttribute('title', 'Layer-1 core')
    expect(link('Autopilot')).toHaveAttribute('href', '/strategies/lab-sol-ema')
    expect(within(history).getAllByText('AI portfolio')).toHaveLength(2) // the unlinked one is a plain chip
    expect(within(history).queryByRole('link', { name: 'Manual' })).toBeNull()
  })

  it('opens a day-old account on 1D, spans at least 1% of the balance, and lets a tab click win', async () => {
    stubSession()
    vi.mocked(api.trades).mockResolvedValue({ trades: [fx.trade({ createdAt: hoursAgo(20) }), fx.trade({ id: 't2', createdAt: hoursAgo(2) })] })
    vi.mocked(api.portfolioEquity).mockImplementation(async (range) => fx.equityCurve({ range }))
    const { user } = open()
    const card = (await screen.findByRole('heading', { name: 'Equity' })).closest('.card') as HTMLElement
    await waitFor(() => expect(within(card).getByRole('button', { name: '1D' })).toHaveAttribute('aria-pressed', 'true'))
    expect(api.portfolioEquity).toHaveBeenCalledTimes(1)
    expect(api.portfolioEquity).toHaveBeenCalledWith('1d', expect.any(AbortSignal))

    // A $16 move on $100,000 keeps a $1,000 window instead of filling the chart like a crash.
    await waitFor(() => expect(charts.at(-1)?.series[0]?.options.autoscaleInfoProvider).toBeTypeOf('function'))
    const provider = charts.at(-1)!.series[0].options.autoscaleInfoProvider as (o: () => unknown) => unknown
    expect(provider(() => ({ priceRange: { minValue: 100_000, maxValue: 100_016 } }))).toEqual({ priceRange: { minValue: 99_508, maxValue: 100_508 } })

    await user.click(within(card).getByRole('button', { name: '30D' }))
    expect(api.portfolioEquity).toHaveBeenLastCalledWith('30d', expect.any(AbortSignal))
  })

  it('picks the shortest range that holds the oldest trade', () => {
    const now = Date.parse('2026-10-03T12:00:00Z')
    const at = (h: number) => fx.trade({ createdAt: new Date(now - h * 3600_000).toISOString() })
    expect(defaultRange([], now)).toBe('1d')
    expect(defaultRange([at(1), at(24)], now)).toBe('1d')
    expect(defaultRange([at(1), at(25)], now)).toBe('7d')
    expect(defaultRange([at(7 * 24)], now)).toBe('7d')
    expect(defaultRange([at(7 * 24 + 1)], now)).toBe('30d')
  })
})

describe('AccountPage · prediction markets (round 5)', () => {
  const position = (over: Partial<MarketPosition>): MarketPosition => ({
    id: 'p1', forecastId: 'f1234567-aaaa', forecastQuestion: 'Will the Fed cut rates in December 2026?',
    question: 'Fed decreases interest rates after the December 2026 meeting?', url: 'https://polymarket.com/event/fed-december-2026',
    outcome: 'Yes', side: 'yes', price: 0.86, contracts: 100 / 0.86, cost: 100, maxPayout: 100 / 0.86, status: 'open',
    payout: null, pnl: null, markPrice: 0.85, createdAt: hoursAgo(1), settledAt: null, ...over,
  })

  it('shows the open stakes in the summary, lists the positions and the Prediction source', async () => {
    stubSession({ portfolio: { ...fx.portfolio, cash: 93_900, predictionStakes: 150 } })
    vi.mocked(api.portfolioAttribution).mockResolvedValue({
      sources: [...fx.attribution.sources, { source: 'prediction', trades: 3, realizedPnl: 16.28, unrealizedPnl: 0, fees: 0, totalPnl: 16.28 }],
      totalPnl: fx.attribution.totalPnl + 16.28,
    })
    vi.spyOn(api, 'marketPositions').mockResolvedValue({
      positions: [
        position({}),
        position({ id: 'p2', forecastId: 'f-nvda', question: 'Will NVIDIA be the largest company in the world by market cap on December 31?',
          outcome: 'Nvidia', side: 'no', price: 0.16, contracts: 312.5, maxPayout: 312.5, cost: 50, markPrice: null }),
        position({ id: 'p3', status: 'won', payout: 116.28, pnl: 16.28, markPrice: null, settledAt: hoursAgo(0.5) }),
      ],
    })
    open()
    const hero = screen.getByRole('region', { name: 'Account summary' })
    expect(await within(hero).findByText('In prediction markets')).toBeInTheDocument()
    expect(within(hero).getByText('In prediction markets').nextSibling).toHaveTextContent('$150.00')

    const card = (await screen.findByRole('heading', { name: '3 paper positions' })).closest('.card') as HTMLElement
    const rows = within(card).getAllByRole('row').slice(1)
    expect(rows).toHaveLength(3)
    expect(within(rows[0]).getByRole('link', { name: 'Fed decreases interest rates after the December 2026 meeting?' })).toHaveAttribute('href', '/forecasts/f1234567-aaaa')
    expect(rows[0]).toHaveTextContent('Yes116.3 @ 86¢$100.00Open')
    expect(rows[1]).toHaveTextContent('No · Nvidia312.5 @ 16¢$50.00Open')
    expect(rows[2]).toHaveTextContent('Won +$16.28')
    await waitFor(() => expect(screen.getByText('Prediction')).toBeInTheDocument()) // P&L by source
  })

  it('hides the card and the stat without positions', async () => {
    stubSession()
    vi.spyOn(api, 'marketPositions').mockResolvedValue({ positions: [] })
    open()
    await screen.findByText('$100,194.00', { selector: 'div' })
    await waitFor(() => expect(api.marketPositions).toHaveBeenCalled())
    expect(screen.queryByText('Prediction markets')).toBeNull()
    expect(screen.queryByText('In prediction markets')).toBeNull()
  })
})

describe('Account copy plans', () => {
  it('shows active and recent plans before positions and preserves copy origins and attribution', async () => {
    stubSession()
    vi.spyOn(api, 'copyPlans').mockResolvedValue({ plans: [fx.copyPlan({ deployedUsd: null, pnl: null })] })
    vi.mocked(api.trades).mockResolvedValue({ trades: [fx.trade({ source: 'copy', ref: { kind: 'copy', id: 'plan/old', label: 'Copy · Trend Rider' } })] })
    vi.mocked(api.portfolioAttribution).mockResolvedValue({ sources: [{ source: 'copy', trades: 1, realizedPnl: -6, unrealizedPnl: 200, fees: 6, totalPnl: 194 }], totalPnl: 194 })
    open()
    const region = await screen.findByRole('region', { name: 'Copy plan Trend Rider' })
    expect(region).toHaveTextContent('since the latest account reset')
    expect(region).toHaveTextContent('Manual trades can change what the plan owns')
    expect(within(region).getAllByText('—')).toHaveLength(2)
    expect(screen.getByText(/Active and recent plans/)).toBeInTheDocument()
    const link = await screen.findByRole('link', { name: 'Copy' })
    expect(link).toHaveAttribute('href', '/account#copy-plans')
    expect(link).toHaveAttribute('title', 'Copy · Trend Rider')
  })
  it('refreshes plans and history when the portfolio revision advances, refusing late pre-stop reads', async () => {
    const session = stubSession()
    const plans = vi.spyOn(api, 'copyPlans').mockResolvedValue({ plans: [fx.copyPlan()] })
    const { user } = open()
    await screen.findByRole('region', { name: 'Copy plan Trend Rider' })
    const initialTrades = vi.mocked(api.trades).mock.calls.length
    const initialPlans = plans.mock.calls.length
    session.portfolio.mockResolvedValue({ portfolio: { ...fx.portfolio, revision: (fx.portfolio.revision ?? 0) + 1 } })
    vi.mocked(api.trades).mockResolvedValue({ trades: [fx.trade({ source: 'copy', note: 'Background copied fill' })] })
    window.dispatchEvent(new Event('visibilitychange'))
    // PortfolioProvider listens on document.
    document.dispatchEvent(new Event('visibilitychange'))
    await waitFor(() => expect(api.trades).toHaveBeenCalledTimes(initialTrades + 1))
    await waitFor(() => expect(plans.mock.calls.length).toBeGreaterThan(initialPlans))
    expect(await screen.findByText('Background copied fill')).toBeInTheDocument()
    const stopped = fx.copyPlan({ status: 'stopped', stopReason: 'user', positions: [{ symbol: 'BTC', qty: 0.01 }], log: [{ t: '2026-10-05T10:00:00Z', kind: 'skip', text: 'BTC quote unavailable; kept.' }] })
    let resolve!: (v: { plans: ReturnType<typeof fx.copyPlan>[] }) => void
    plans.mockReturnValueOnce(new Promise((r) => { resolve = r })).mockResolvedValue({ plans: [stopped] })
    document.dispatchEvent(new Event('visibilitychange'))
    vi.spyOn(api, 'stopCopyPlan').mockResolvedValue({ plan: stopped, trades: [], portfolio: null })
    await user.click(screen.getByRole('button', { name: 'Stop copying' }))
    await user.click(screen.getByRole('button', { name: 'Stop and close owned positions' }))
    expect(await screen.findByText(/Holdings remain in your account/)).toBeInTheDocument()
    resolve({ plans: [fx.copyPlan()] })
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Stop copying' })).toBeNull())
    expect(api.stopCopyPlan).toHaveBeenCalledWith('cp1', true)
  })
})

it('refuses an aborted older history response after a background revision refresh', async () => {
  stubSession({ portfolio: { ...fx.portfolio, revision: 1 } })
  let resolve!: (r: { trades: ReturnType<typeof fx.trade>[] }) => void
  vi.mocked(api.trades).mockReturnValueOnce(new Promise((r) => { resolve = r })).mockResolvedValue({ trades: [fx.trade({ id: 'future', note: 'New copied fill', source: 'copy' })] })
  const poll = vi.mocked(api.portfolio)
  open()
  await screen.findByRole('heading', { name: '1 position' })
  poll.mockResolvedValue({ portfolio: { ...fx.portfolio, revision: 2 } })
  document.dispatchEvent(new Event('visibilitychange'))
  expect(await screen.findByText('New copied fill')).toBeInTheDocument()
  const signal = vi.mocked(api.trades).mock.calls[0][1]!
  expect(signal.aborted).toBe(true)
  await act(async () => resolve({ trades: [fx.trade({ id: 'old', note: 'Old read' })] }))
  await waitFor(() => expect(screen.queryByText('Old read')).toBeNull())
  expect(screen.getByText('New copied fill')).toBeInTheDocument()
})

it('keeps health, prediction stakes, copy plans and fill history current together after a copied revision', async () => {
  const session = stubSession({ portfolio: { ...fx.portfolio, revision: 1, predictionStakes: 100 } })
  vi.mocked(api.copyPlans).mockResolvedValue({ plans: [fx.copyPlan()] })
  vi.spyOn(api, 'marketPositions').mockResolvedValue({ positions: [{
    id: 'prediction', forecastId: 'forecast', forecastQuestion: 'December Fed cut?',
    question: 'December Fed cut?', url: 'https://polymarket.com/event/fed', outcome: 'Yes',
    side: 'yes', price: .5, contracts: 200, cost: 100, maxPayout: 200, status: 'open',
    payout: null, pnl: null, markPrice: .5, createdAt: hoursAgo(1), settledAt: null,
  }] })
  vi.mocked(api.portfolioReview).mockResolvedValue(fx.review({ predictionStakes: 100, predictionStakesPct: .1 }))
  open()
  const health = (await screen.findByText('Portfolio health', { exact: true })).closest('.card') as HTMLElement
  const history = screen.getByRole('heading', { name: 'Every simulated fill' }).closest('.card') as HTMLElement
  expect(await within(health).findByText('High concentration')).toBeInTheDocument()
  expect(health).toHaveTextContent('revision 1')
  expect(health).toHaveTextContent('Prediction stakes$100')
  expect(await screen.findByRole('heading', { name: '1 paper position' })).toBeInTheDocument()
  expect(screen.getByRole('region', { name: 'Copy plan Trend Rider' })).toBeInTheDocument()
  expect(screen.getByRole('region', { name: 'Account summary' })).toHaveTextContent('In prediction markets')
  expect(await screen.findByRole('heading', { name: 'P&L by source' })).toBeInTheDocument()
  await waitFor(() => expect(within(history).getByRole('table')).toHaveTextContent('Manual'))
  const initialTrades = vi.mocked(api.trades).mock.calls.length
  session.portfolio.mockResolvedValue({ portfolio: { ...fx.portfolio, revision: 2, predictionStakes: 100 } })
  vi.mocked(api.portfolioReview).mockResolvedValue(fx.review({ revision: 2, asOf: '2026-10-05T12:01:00Z', predictionStakes: 100, predictionStakesPct: .1, grossExposure: 6100 }))
  vi.mocked(api.trades).mockResolvedValue({ trades: [fx.trade({ id: 'copied', source: 'copy', note: 'Combined copied fill', ref: { kind: 'copy', id: 'cp1', label: 'Copy · Trend Rider' } })] })
  document.dispatchEvent(new Event('visibilitychange'))
  await waitFor(() => expect(health).toHaveTextContent('revision 2'))
  expect(health).toHaveTextContent('Gross exposure$6,100')
  expect(health).toHaveTextContent('Prediction stakes$100')
  expect(await within(history).findByText('Combined copied fill')).toBeInTheDocument()
  expect(within(history).getByRole('link', { name: 'Copy' })).toHaveAttribute('href', '/account#copy-plans')
  expect(api.trades).toHaveBeenCalledTimes(initialTrades + 1)
  expect(screen.getByRole('region', { name: 'Copy plan Trend Rider' })).toBeInTheDocument()
  expect(screen.getByRole('heading', { name: '1 paper position' })).toBeInTheDocument()
})

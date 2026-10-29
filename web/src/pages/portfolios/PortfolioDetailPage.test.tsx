import { act, fireEvent, screen, waitFor, within } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError } from '../../lib/api'
import { Link } from 'react-router'
import { usePortfolio } from '../../lib/portfolio'
import { useAuth } from '../../lib/auth'
import type { AIPortfolio } from '../../lib/types'
import { charts } from '../../test/chart'
import * as fx from '../../test/fixtures'
import { renderPage, stubSession } from '../../test/utils'
import { PortfolioDetailPage } from './PortfolioDetailPage'

const open = (id = 'p1') => renderPage(<PortfolioDetailPage />, { route: `/portfolios/${id}`, path: '/portfolios/:id' })
const show = (p: AIPortfolio) => vi.spyOn(api, 'aiPortfolio').mockResolvedValue({ portfolio: p })
const stat = (label: string) => screen.getByText(label, { selector: 'dt' }).parentElement!

beforeEach(() => {
  charts.length = 0
  stubSession()
})

describe('PortfolioDetailPage', () => {
  it('shows the thesis, prompt, allocation and backtest', async () => {
    show(fx.aiPortfolio())
    open()
    expect(await screen.findByRole('heading', { level: 1, name: 'Layer-1 core' })).toBeInTheDocument()
    expect(screen.getByText('Majors with lower drawdown than BTC.')).toBeInTheDocument()
    expect(screen.getByText('Your prompt:').parentElement).toHaveTextContent('“Layer-1s with less drawdown than BTC”')
    expect(screen.getByRole('heading', { name: '3 holdings, weighted by the AI' })).toBeInTheDocument()
    expect(screen.getByRole('img', { name: 'Allocation: BTC 50%, ETH 30%, PAXG 20%' })).toBeInTheDocument()
    expect(screen.getByText('Gold hedge.')).toBeInTheDocument()
    expect(screen.getByText('PAX Gold')).toBeInTheDocument()

    expect(stat('Return · measured window')).toHaveTextContent('▲up 18.0%+9.0 pts vs BTC')
    expect(stat('BTC return')).toHaveTextContent('9.0%')
    await waitFor(() => expect(charts.at(-1)?.series).toHaveLength(2))
  })

  it('says what drives each holding: a forecast links to its report, sentiment and stats are plain chips', async () => {
    show(
      fx.aiPortfolio({
        holdings: [
          { symbol: 'SOL', weight: 0.2, rationale: 'Underweight: the desk leans No.', driver: { kind: 'forecast', id: 'f-sol', label: 'Forecast · Yes 39% (leans No)' } },
          { symbol: 'ETH', weight: 0.5, rationale: 'Upbeat wire.', driver: { kind: 'sentiment', id: null, label: 'News sentiment · bullish' } },
          { symbol: 'BTC', weight: 0.3, rationale: 'The anchor.', driver: { kind: 'stats', id: null, label: '180-day stats' } },
        ],
      }),
    )
    open()
    const chip = await screen.findByRole('link', { name: 'Driven by Forecast · Yes 39% (leans No)' })
    expect(chip).toHaveAttribute('href', '/forecasts/f-sol')
    expect(screen.getByText('Driven by News sentiment · bullish').tagName).toBe('SPAN')
    expect(screen.getByText('Driven by 180-day stats')).toBeInTheDocument()
    expect(screen.getAllByRole('link', { name: /^Driven by/ })).toHaveLength(1)
  })

  it('renders holdings built before drivers without a chip', async () => {
    show(fx.aiPortfolio()) // no driver on any holding
    open()
    expect(await screen.findByText('Gold hedge.')).toBeInTheDocument()
    expect(screen.queryByText(/^Driven by/)).toBeNull()
  })

  it('slices the backtest window client-side', async () => {
    show(fx.aiPortfolio())
    const { user } = open()
    await screen.findByText('Return · measured window')
    await user.click(screen.getByRole('button', { name: '1M' }))
    expect(stat('Return · measured window')).toHaveTextContent('▲up 2.6%') // 115 → 118 over the last 30 days
    await waitFor(() => expect(charts.at(-1)!.series[0].setData.mock.calls.at(-1)![0]).toHaveLength(31))
  })

  it('invests on paper and lists the fills and skipped holdings', async () => {
    show(fx.aiPortfolio())
    const invest = vi.spyOn(api, 'investPortfolio').mockResolvedValue({
      trades: [fx.trade({ id: 'a', symbol: 'BTC', qty: 0.0297, notional: 2500, fee: 2.5 }), fx.trade({ id: 'b', symbol: 'ETH', qty: 0.5, notional: 1500, fee: 1.5 })],
      portfolio: fx.portfolio,
      skipped: [{ symbol: 'PAXG', reason: 'below the $10 minimum' }],
    })
    const { user } = open()
    const amount = await screen.findByLabelText('Amount (USD)')
    await user.click(screen.getByRole('button', { name: '$5K' }))
    expect(amount).toHaveValue('5000')
    const preview = screen.getByRole('list', { name: 'Order preview' })
    expect(within(preview).getAllByRole('listitem').map((li) => li.textContent)).toEqual(['BTC50%$2,500.00', 'ETH30%$1,500.00', 'PAXG20%$1,000.00']) // the logos carry no text

    const before = vi.mocked(api.portfolio).mock.calls.length
    await user.click(screen.getByRole('button', { name: 'Invest $5,000 (paper)' }))
    expect(invest).toHaveBeenCalledWith('p1', 5000)
    expect(await screen.findByText('2 paper orders filled')).toBeInTheDocument()
    expect(screen.getByText('Skipped PAXG: below the $10 minimum')).toBeInTheDocument()
    expect(screen.getByText('Fees $4.00')).toBeInTheDocument()
    expect(screen.getByText('Invested $4,000.00 in 2 holdings (paper).')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /View paper account/ })).toHaveAttribute('href', '/account')
    await waitFor(() => expect(vi.mocked(api.portfolio).mock.calls.length).toBe(before + 1))
    // The receipt stays and the form does not re-arm: no accidental second invest.
    expect(screen.queryByLabelText('Amount (USD)')).toBeNull()
    expect(screen.queryByRole('button', { name: /Invest \$/ })).toBeNull()
    expect(screen.getByRole('button', { name: 'Invest more' })).toBeInTheDocument()
    await waitFor(() => expect(api.aiPortfolio).toHaveBeenCalledTimes(2)) // refetched for its invested state
  })

  it('shows an invested portfolio\'s paper position and folds the form behind Invest more', async () => {
    show(fx.aiPortfolio({ invested: fx.invested() }))
    const { user } = open()
    const panel = await screen.findByRole('region', { name: 'Your paper position' })
    expect(within(panel).getByText('Invested').nextSibling).toHaveTextContent('$10,000')
    expect(within(panel).getByText('Now').nextSibling).toHaveTextContent('$10,123 ▲up 1.23%')
    expect(within(panel).getByText('BTC since then').nextSibling).toHaveTextContent('▲up 0.8%')
    expect(within(panel).getByText('Since').nextSibling).toHaveTextContent('Oct 3')
    expect(screen.getByRole('heading', { name: 'You hold this basket' })).toBeInTheDocument()
    expect(screen.queryByLabelText('Amount (USD)')).toBeNull()

    await user.click(screen.getByRole('button', { name: 'Invest more' }))
    expect(screen.getByRole('heading', { name: 'Invest more' })).toBeInTheDocument()
    expect(screen.getByLabelText('Amount (USD)')).toHaveValue('10000')
    expect(screen.getByRole('button', { name: 'Invest $10,000 (paper)' })).toBeEnabled()
  })

  it('shows no position panel before the first invest, and a dash when BTC has no comparison', async () => {
    show(fx.aiPortfolio())
    const { unmount } = open()
    await screen.findByRole('heading', { name: 'Put it to work' })
    expect(screen.queryByRole('region', { name: 'Your paper position' })).toBeNull()
    unmount()
    show(fx.aiPortfolio({ invested: fx.invested({ btcReturnPct: null, benchmarkReturnPct: null, totalPnl: -50, returnPct: -0.5 }) }))
    open()
    const panel = await screen.findByRole('region', { name: 'Your paper position' })
    expect(within(panel).getByText('Now').nextSibling).toHaveTextContent('$9,950 ▼down 0.50%')
    expect(within(panel).getByText('BTC since then').nextSibling).toHaveTextContent('—')
  })

  it('validates the amount: empty, minimum, buying power and small slices', async () => {
    show(fx.aiPortfolio({ holdings: [{ symbol: 'BTC', weight: 0.6, rationale: '' }, { symbol: 'ETH', weight: 0.3, rationale: '' }, { symbol: 'PAXG', weight: 0.1, rationale: '' }] }))
    const invest = vi.spyOn(api, 'investPortfolio')
    const { user } = open()
    const amount = await screen.findByLabelText('Amount (USD)')
    await screen.findByText('Buying power $194,388.00')

    await user.clear(amount)
    expect(screen.getByText('Enter an amount.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Invest/ })).toBeDisabled()
    await user.type(amount, '20')
    expect(screen.getByText('The minimum is $50.')).toBeInTheDocument()
    await user.clear(amount)
    await user.type(amount, '60')
    expect(screen.getByText('Slices under $10 are skipped (minimum order).')).toBeInTheDocument()
    expect(screen.getByText('$6.00')).toHaveClass('line-through')
    await user.clear(amount)
    await user.type(amount, '200000')
    expect(screen.getByText('Above your buying power. The most you can invest is $193,416.06.')).toBeInTheDocument()
    expect(amount).toHaveAttribute('aria-invalid', 'true')
    expect(invest).not.toHaveBeenCalled()
  })

  it('toasts a failed investment and still refreshes the account', async () => {
    show(fx.aiPortfolio())
    vi.spyOn(api, 'investPortfolio').mockRejectedValue(new ApiError(502, 'upstream', ''))
    const { user } = open()
    await screen.findByText('Buying power $194,388.00')
    const before = vi.mocked(api.portfolio).mock.calls.length
    await user.click(screen.getByRole('button', { name: 'Invest $10,000 (paper)' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('The investment could not be completed. Refresh your account before retrying.')
    await waitFor(() => expect(vi.mocked(api.portfolio).mock.calls.length).toBe(before + 1))
  })

  it('handles a portfolio without backtest data', async () => {
    show(fx.aiPortfolio({ prompt: '', thesis: '', backtest: { ...fx.aiPortfolio().backtest, series: [], benchmark: [], bars: 0, windowStart: 0, windowEnd: 0, returnPct: 5, benchmarkReturnPct: 2, maxDrawdownPct: 3, volatilityAnnPct: null } }))
    open()
    expect(await screen.findByText('No backtest data for this portfolio.')).toBeInTheDocument()
    expect(stat('Return · measured window')).toHaveTextContent('5.0%') // falls back to the stored stats
    expect(stat('Max drawdown')).toHaveTextContent('−3.0%')
    expect(screen.queryByText('Your prompt:')).toBeNull()
  })

  it('says when a portfolio does not exist, and retries other failures', async () => {
    const get = vi.spyOn(api, 'aiPortfolio').mockRejectedValueOnce(new ApiError(404, 'not_found', 'Not found'))
    const { unmount } = open('gone')
    expect(await screen.findByRole('heading', { name: "This portfolio doesn't exist." })).toBeInTheDocument()
    unmount()

    get.mockRejectedValueOnce(new ApiError(500, 'internal', 'Database is down')).mockResolvedValueOnce({ portfolio: fx.aiPortfolio() })
    const { user } = open()
    expect(screen.getByRole('generic', { busy: true })).toHaveAccessibleName('Loading portfolio')
    expect(await screen.findByRole('heading', { name: "Couldn't load this portfolio." })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Try again' }))
    expect(await screen.findByRole('heading', { level: 1, name: 'Layer-1 core' })).toBeInTheDocument()
  })
})

describe('PortfolioDetailPage · actual vs BTC', () => {
  it('opens on Since invest with the paper series against BTC, and keeps the backtest windows', async () => {
    show(fx.aiPortfolio({ invested: fx.invested(fx.sinceInvest) }))
    const { user } = open()
    const tabs = await screen.findByRole('group', { name: 'Backtest window' })
    expect(within(tabs).getAllByRole('button').map((b) => b.textContent)).toEqual(['Since invest', '1M', '3M', '6M'])
    expect(within(tabs).getByRole('button', { name: 'Since invest' })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByRole('heading', { name: 'Actual vs BTC' })).toBeInTheDocument()
    await waitFor(() => expect(charts.at(-1)?.series).toHaveLength(2))
    expect(screen.getAllByRole('listitem').map((li) => li.textContent)).toEqual(expect.arrayContaining(['Actual (paper)', 'BTC']))
    expect(charts.at(-1)!.series[0].setData.mock.calls.at(-1)![0]).toHaveLength(4)
    expect(screen.getByText('Your invest buys, fees included, valued at each instrument’s venue closes. Retained history may begin after your first buy; sells are not replayed.')).toBeInTheDocument()
    expect(stat('Return · paper buys')).toHaveTextContent('▲up 0.9%+0.1 pts vs BTC') // 100.9 − 100 vs 100.8 − 100
    expect(stat('BTC return')).toHaveTextContent('0.8%')
    // The paper position row above is unchanged.
    expect(within(screen.getByRole('region', { name: 'Your paper position' })).getByText('Invested').nextSibling).toHaveTextContent('$10,000')

    await user.click(within(tabs).getByRole('button', { name: '6M' }))
    expect(screen.getByRole('heading', { name: 'Portfolio vs BTC' })).toBeInTheDocument()
    expect(stat('Return · measured window')).toHaveTextContent('▲up 18.0%')
    expect(screen.getByText('Closed daily candles from Binance spot and Hyperliquid perps, according to each instrument. Past performance is not a forecast.')).toBeInTheDocument()
  })

  it('keeps the backtest tabs when the invested series is not in yet', async () => {
    show(fx.aiPortfolio({ invested: fx.invested({ series: null, btcSeries: null }) }))
    open()
    const tabs = await screen.findByRole('group', { name: 'Backtest window' })
    expect(within(tabs).getAllByRole('button').map((b) => b.textContent)).toEqual(['1M', '3M', '6M'])
    expect(screen.queryByText(/Your paper fills/)).toBeNull()
  })
})

const overlap = (revision = 1) => ({ revision, asOf: '2026-10-05T12:00:00Z', coverage: { modeledGrossExposure: 6000, commonDays: 31, missingSymbols: ['APT'], reason: 'Entry-price estimates for APT; basket comparisons unavailable for APT.' }, holdings: [{ symbol: 'BTC', side: 'short' as const, bookPct: 40, basketPct: 50, shared: true }, { symbol: 'SOL', side: 'long' as const, bookPct: 60, basketPct: 0, shared: false }], correlations: [{ a: 'ETH', b: 'SOL', rho: 0.91, days: 31 }, { a: 'BTC', b: 'SOL', rho: null, days: 8 }] })
describe('PortfolioDetailPage · private overlap', () => {
  it('renders caller directions and independent current/basket percentage scales, supported and unknown pairs', async () => {
    show(fx.aiPortfolio({ overlap: overlap() })); open()
    const section = await screen.findByRole('region', { name: 'Overlap with what you hold' })
    expect(section).toHaveTextContent('BTC · short'); expect(section).toHaveTextContent('40% of your gross book'); expect(section).toHaveTextContent('50% of basket'); expect(section).toHaveTextContent('Shared symbol · may offset basket longs'); expect(section).toHaveTextContent('ETH · SOL'); expect(section).toHaveTextContent('ρ 0.91 · 31 shared days'); expect(section).toHaveTextContent('Unknown · 8 shared days'); expect(section).toHaveTextContent('Entry-price estimates for APT; basket comparisons unavailable for APT.'); expect(section).not.toHaveTextContent('5000%')
  })
  it('refetches seeded list/build detail without trusting seeded private projection', async () => {
    const spy = show(fx.aiPortfolio({ overlap: overlap() })); renderPage(<PortfolioDetailPage />, { route: '/portfolios/p1', path: '/portfolios/:id', state: { portfolio: fx.aiPortfolio({ overlap: { ...overlap(), holdings: [{ symbol: 'PRIVATE', side: 'long', bookPct: 100, basketPct: 0, shared: false }] } }) } });
    expect(screen.queryByText(/PRIVATE/)).toBeNull(); expect(await screen.findByText(/40% of your gross book/)).toBeInTheDocument(); expect(spy).toHaveBeenCalledTimes(1)
  })
  it('refreshes overlap after an invest failure that may have filled a leg', async () => {
    const spy = show(fx.aiPortfolio({ overlap: overlap() })); vi.spyOn(api, 'investPortfolio').mockRejectedValue(new ApiError(502, 'upstream', 'Partial fill.'));
    const { user } = open(); await screen.findByText(/40% of your gross book/); spy.mockResolvedValue({ portfolio: fx.aiPortfolio({ overlap: { ...overlap(2), holdings: [] } }) }); await user.click(screen.getByRole('button', { name: 'Invest $10,000 (paper)' })); expect(await screen.findByText('No current cash-asset holdings to compare.')).toBeInTheDocument()
  })
  it('shows stale overlap and Retry while retaining allocation after refresh failure', async () => {
    const spy = show(fx.aiPortfolio({ overlap: overlap() })); const { user } = open(); await screen.findByText(/40% of your gross book/); spy.mockRejectedValueOnce(new ApiError(502, 'upstream', 'Projection unavailable.')); fireEvent(document, new Event('visibilitychange')); const section = screen.getByRole('region', { name: 'Overlap with what you hold' }); expect(await within(section).findByRole('alert')).toHaveTextContent('Projection unavailable.'); expect(section).toHaveTextContent('Stale'); expect(screen.getByRole('img', { name: /^Allocation:/ })).toBeInTheDocument(); await user.click(within(section).getByRole('button', { name: 'Retry overlap' })); await waitFor(() => expect(section).not.toHaveTextContent('Stale'))
  })
})

function RevisionDetail() {
  const { setPortfolio } = usePortfolio()
  return <><PortfolioDetailPage /><button onClick={() => setPortfolio({ ...fx.portfolio, revision: 2 })}>Trade or reset</button></>
}
function pending<T>() { let resolve!: (v: T) => void; const promise = new Promise<T>((r) => resolve = r); return { promise, resolve } }
describe('PortfolioDetailPage · revision lifecycle', () => {
  it('clears old private overlap while retaining static allocation on trade/reset, then accepts current facts', async () => {
    const fresh = pending<{ portfolio: AIPortfolio }>(); const spy = vi.spyOn(api, 'aiPortfolio').mockResolvedValueOnce({ portfolio: fx.aiPortfolio({ overlap: overlap() }) }).mockReturnValue(fresh.promise)
    const { user } = renderPage(<RevisionDetail />, { route: '/portfolios/p1', path: '/portfolios/:id' }); await screen.findByText(/40% of your gross book/); await user.click(screen.getByRole('button', { name: 'Trade or reset' })); expect(screen.queryByText(/40% of your gross book/)).toBeNull(); expect(screen.getByRole('img', { name: /^Allocation:/ })).toBeInTheDocument(); await act(() => fresh.resolve({ portfolio: fx.aiPortfolio({ overlap: { ...overlap(2), holdings: [] } }) })); expect(await screen.findByText('No current cash-asset holdings to compare.')).toBeInTheDocument(); expect(spy).toHaveBeenCalledTimes(2)
  })
  it('polls same-revision overlap at sixty seconds and cleans up hidden/unmounted requests', async () => {
    vi.useFakeTimers(); const spy = show(fx.aiPortfolio({ overlap: overlap() })); const view = open(); await act(() => Promise.resolve()); await act(() => Promise.resolve()); spy.mockResolvedValue({ portfolio: fx.aiPortfolio({ overlap: { ...overlap(), asOf: '2026-10-05T12:01:00Z', holdings: [] } }) }); await act(() => vi.advanceTimersByTimeAsync(60000)); expect(screen.getByText('No current cash-asset holdings to compare.')).toBeInTheDocument(); Object.defineProperty(document, 'hidden', { configurable: true, value: true }); fireEvent(document, new Event('visibilitychange')); await act(() => vi.advanceTimersByTimeAsync(60000)); expect(spy).toHaveBeenCalledTimes(2); view.unmount(); expect(spy.mock.calls.at(-1)![1]?.aborted).toBe(true); Object.defineProperty(document, 'hidden', { configurable: true, value: false }); vi.useRealTimers()
  })
})

function NavigatingDetail() { return <><PortfolioDetailPage /><Link to="/portfolios/p2">Other basket</Link></> }
describe('PortfolioDetailPage · response ordering', () => {
  it('rejects reversed same-revision projection responses', async () => {
    const old = pending<{ portfolio: AIPortfolio }>(); const newer = pending<{ portfolio: AIPortfolio }>(); vi.spyOn(api, 'aiPortfolio').mockResolvedValueOnce({ portfolio: fx.aiPortfolio({ overlap: overlap() }) }).mockReturnValueOnce(old.promise).mockReturnValueOnce(newer.promise);
    open(); await screen.findByText(/40% of your gross book/); fireEvent(document, new Event('visibilitychange')); fireEvent(document, new Event('visibilitychange')); await act(() => newer.resolve({ portfolio: fx.aiPortfolio({ overlap: { ...overlap(), asOf: '2026-10-05T12:01:00Z', holdings: [] } }) })); await act(() => old.resolve({ portfolio: fx.aiPortfolio({ overlap: overlap() }) })); expect(screen.getByText('No current cash-asset holdings to compare.')).toBeInTheDocument(); expect(screen.queryByText(/40% of your gross book/)).toBeNull()
  })
  it('aborts old basket detail and does not combine it with the newly selected basket', async () => {
    const old = pending<{ portfolio: AIPortfolio }>(); const spy = vi.spyOn(api, 'aiPortfolio').mockReturnValueOnce(old.promise).mockResolvedValue({ portfolio: fx.aiPortfolio({ id: 'p2', name: 'Second basket', overlap: { ...overlap(), holdings: [] } }) }); const { user } = renderPage(<NavigatingDetail />, { route: '/portfolios/p1', path: '/portfolios/:id' }); await waitFor(() => expect(spy).toHaveBeenCalledTimes(1)); await user.click(screen.getByRole('link', { name: 'Other basket' })); await screen.findByRole('heading', { name: 'Second basket' }); await act(() => old.resolve({ portfolio: fx.aiPortfolio({ overlap: overlap() }) })); expect(screen.queryByText(/40% of your gross book/)).toBeNull(); expect(screen.getByText('No current cash-asset holdings to compare.')).toBeInTheDocument(); expect(spy.mock.calls[0][1]?.aborted).toBe(true)
  })
})

function RecoveringDetail() {
  const { refresh } = usePortfolio()
  const { refresh: caller } = useAuth()
  return <><PortfolioDetailPage /><button onClick={() => void refresh()}>Refresh Account</button><button onClick={() => void caller()}>Switch caller</button></>
}
describe('PortfolioDetailPage · unavailable Account', () => {
  it('loads an owned direct URL with null overlap despite initial and repeated Account failures', async () => {
    vi.mocked(api.portfolio).mockRejectedValue(new ApiError(502, 'upstream', 'Account marks unavailable.'))
    const detail = show(fx.aiPortfolio({ overlap: null }))
    open()
    expect(await screen.findByRole('heading', { name: 'Layer-1 core' })).toBeInTheDocument()
    expect(screen.getByRole('img', { name: /^Allocation:/ })).toBeInTheDocument()
    expect(screen.getByRole('group', { name: 'Backtest window' })).toBeInTheDocument()
    expect(screen.getByRole('region', { name: 'Overlap with what you hold' })).toHaveTextContent('Current overlap unavailable. No inference about diversification.')
    fireEvent(document, new Event('visibilitychange'))
    await waitFor(() => expect(api.portfolio).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(detail).toHaveBeenCalledTimes(2))
    expect(screen.getByRole('heading', { name: 'Layer-1 core' })).toBeInTheDocument()
  })
  it('exposes detail failure and Retry while Account remains unavailable', async () => {
    vi.mocked(api.portfolio).mockRejectedValue(new ApiError(502, 'upstream', 'Account marks unavailable.'))
    const detail = vi.spyOn(api, 'aiPortfolio').mockRejectedValueOnce(new ApiError(502, 'upstream', 'Saved basket unavailable.')).mockResolvedValue({ portfolio: fx.aiPortfolio({ overlap: null }) })
    const { user } = open()
    expect(await screen.findByText('Saved basket unavailable.')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Try again' }))
    expect(await screen.findByRole('heading', { name: 'Layer-1 core' })).toBeInTheDocument()
    expect(detail).toHaveBeenCalledTimes(2)
    expect(screen.getByRole('region', { name: 'Overlap with what you hold' })).toHaveTextContent('Current overlap unavailable.')
  })
  it('hides an unqualified projection then obtains fresh overlap when Account recovers', async () => {
    vi.mocked(api.portfolio).mockRejectedValueOnce(new ApiError(502, 'upstream', 'Account marks unavailable.')).mockRejectedValueOnce(new ApiError(502, 'upstream', 'Still unavailable.')).mockResolvedValue({ portfolio: { ...fx.portfolio, revision: 2 } })
    const detail = vi.spyOn(api, 'aiPortfolio').mockResolvedValueOnce({ portfolio: fx.aiPortfolio({ overlap: { ...overlap(), holdings: [{ symbol: 'UNQUALIFIED', side: 'long', bookPct: 100, basketPct: 0, shared: false }] } }) }).mockResolvedValue({ portfolio: fx.aiPortfolio({ overlap: overlap(2) }) })
    const { user } = renderPage(<RecoveringDetail />, { route: '/portfolios/p1', path: '/portfolios/:id' })
    await screen.findByRole('heading', { name: 'Layer-1 core' })
    expect(screen.queryByText(/UNQUALIFIED/)).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Refresh Account' }))
    expect(detail).toHaveBeenCalledTimes(1)
    await user.click(screen.getByRole('button', { name: 'Refresh Account' }))
    expect(await screen.findByText(/40% of your gross book/)).toBeInTheDocument()
    expect(detail).toHaveBeenCalledTimes(2)
    expect(screen.queryByText(/UNQUALIFIED/)).toBeNull()
  })
  it('aborts an old caller outage read and preserves current caller recovery', async () => {
    vi.mocked(api.portfolio).mockRejectedValue(new ApiError(502, 'upstream', 'Account marks unavailable.'))
    vi.mocked(api.me).mockResolvedValueOnce({ user: fx.user, credits: fx.credits }).mockResolvedValue({ user: { ...fx.user, id: 'other' }, credits: fx.credits })
    const old = pending<{ portfolio: AIPortfolio }>()
    const detail = vi.spyOn(api, 'aiPortfolio').mockReturnValueOnce(old.promise).mockResolvedValueOnce({ portfolio: fx.aiPortfolio({ name: 'Current caller basket', overlap: null }) }).mockResolvedValue({ portfolio: fx.aiPortfolio({ name: 'Current caller basket', overlap: { ...overlap(2), holdings: [] } }) })
    const { user } = renderPage(<RecoveringDetail />, { route: '/portfolios/p1', path: '/portfolios/:id' })
    await waitFor(() => expect(detail).toHaveBeenCalledTimes(1))
    await user.click(screen.getByRole('button', { name: 'Switch caller' }))
    await screen.findByRole('heading', { name: 'Current caller basket' })
    await act(() => old.resolve({ portfolio: fx.aiPortfolio({ name: 'Old caller basket', overlap: overlap() }) }))
    expect(detail.mock.calls[0][1]?.aborted).toBe(true)
    expect(screen.queryByText(/Old caller basket|40% of your gross book/)).toBeNull()
    vi.mocked(api.portfolio).mockResolvedValue({ portfolio: { ...fx.portfolio, revision: 2 } })
    await user.click(screen.getByRole('button', { name: 'Refresh Account' }))
    expect(await screen.findByText('No current cash-asset holdings to compare.')).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Current caller basket' })).toBeInTheDocument()
    expect(detail).toHaveBeenCalledTimes(3)
  })
})

import { act, fireEvent, screen, waitFor, within } from '@testing-library/react'
import { useState } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { useAuth } from '../../lib/auth'
import { api, ApiError } from '../../lib/api'
import * as fx from '../../test/fixtures'
import { currentPath, renderPage, stubSession } from '../../test/utils'
import { PortfolioHealth } from './PortfolioHealth'

function Harness({ revision: initial }: { revision: number | null }) {
  const [revision, setRevision] = useState(initial)
  return <><PortfolioHealth revision={revision} /><button onClick={() => setRevision((r) => (r ?? 0) + 1)}>Bump revision</button><button onClick={() => setRevision(null)}>Clear account</button></>
}
const open = (revision: number | null = 1) => renderPage(<Harness revision={revision} />, { route: '/account', path: '/account', routes: { '/': <div>Agent page</div> } })
const card = () => screen.getByText('Portfolio health').closest('.card') as HTMLElement
function deferred<T>() { let resolve!: (v: T) => void; const promise = new Promise<T>((r) => { resolve = r }); return { promise, resolve } }
const measuredBook = fx.review({
  positions: 3, grossExposure: 9000, netExposure: 5000, exposurePct: 9,
  coverage: { modeledGrossExposure: 9000, missingSymbols: [], commonDays: 31, reason: null },
  holdings: [
    { symbol: 'ETH', side: 'long', value: 6000, pct: 66.7, beta: 1.18, stopLoss: 2700, halted: false, markAvailable: true, stopStatus: 'valid' },
    { symbol: 'BTC', side: 'short', value: 2000, pct: 22.2, beta: 1, stopLoss: null, halted: false, markAvailable: true, stopStatus: 'missing' },
    { symbol: 'SOL', side: 'long', value: 1000, pct: 11.1, beta: 1.35, stopLoss: 120, halted: true, markAvailable: true, stopStatus: 'dormant' },
  ],
  concentration: { level: 'high', top1Pct: 66.7, top3Pct: 100, hhi: .506, effective: 2 },
  correlations: [{ a: 'ETH', b: 'SOL', rho: .91, days: 31 }, { a: 'BTC', b: 'ETH', rho: .6, days: 35 }, { a: 'BTC', b: 'SOL', rho: .5, days: 34 }],
  clusters: [{ symbols: ['ETH', 'SOL'], rho: .91, pct: 77.8, days: 31 }],
  stops: { covered: 2, uncovered: ['BTC'], uncoveredPct: 22.2, atRiskUsd: 800 },
  notes: ['ETH is 66.7% of gross exposure.'],
})
const missing = () => fx.review({ coverage: { modeledGrossExposure: 0, missingSymbols: ['APT'], commonDays: 12, reason: 'Missing marks and daily history for APT.' }, holdings: [{ ...fx.review().holdings[0], symbol: 'APT', side: 'short', beta: null, markAvailable: false, stopStatus: 'unknown' }], betaBtc: null, volAnnPct: null, var: null, worstDay: null, stress: [], drawdown30dPct: null, stops: { covered: 0, uncovered: ['APT'], uncoveredPct: 100, atRiskUsd: 0 }, predictionStakes: 250, predictionStakesPct: 0.25 })
afterEach(() => { vi.useRealTimers(); Object.defineProperty(document, 'hidden', { configurable: true, value: false }) })

describe('PortfolioHealth', () => {
  it('renders measured concentration, exposure, samples and supported covered stop distances', async () => {
    stubSession(); vi.spyOn(api, 'portfolioReview').mockResolvedValue(measuredBook); open()
    await screen.findByRole('heading', { name: 'Concentrated book' })
    expect(card()).toHaveTextContent('Gross exposure$9,000'); expect(card()).toHaveTextContent('Net exposure+$5,000')
    expect(card()).toHaveTextContent('90 requested daily returns'); expect(card()).toHaveTextContent('31 common days')
    expect(card()).toHaveTextContent('31 shared days'); expect(card()).toHaveTextContent('31 minimum shared days')
    expect(card()).toHaveTextContent('$800 distance to covered stop levels'); expect(card()).toHaveTextContent('fees, slippage'); expect(card()).not.toHaveTextContent('if all fire')
    expect(screen.getByText('Top holding').nextSibling).toHaveTextContent('ETH 67%')
    expect(screen.getByText('Top three').parentElement).toHaveTextContent('100%2 effective positions')
    expect(screen.getByText('Beta to BTC').nextSibling).toHaveTextContent('0.07')
    expect(screen.getByText('1-day VaR · 95%').parentElement).toHaveTextContent('$4130.4% of equity · CVaR $560')
    expect(screen.getByText('Volatility · ann.').nextSibling).toHaveTextContent('4%')
    expect(screen.getByText('Drawdown · 30d').nextSibling).toHaveTextContent('1.2%')
    expect(screen.getAllByRole('meter').map((b) => b.getAttribute('aria-label'))).toEqual(['ETH share of exposure', 'BTC share of exposure', 'SOL share of exposure'])
    expect(screen.getAllByRole('meter')[0]).toHaveAttribute('aria-valuenow', '66.7')
    expect(screen.getAllByRole('meter')[0].firstElementChild).toHaveStyle({ width: '66.7%' })
    expect(screen.getAllByRole('meter')[1].closest('li')).toHaveTextContent('BTC▼22%β 1.00Stop: missing')
    expect(screen.getAllByRole('meter')[2].closest('li')).toHaveTextContent('SOL▲11%haltedStop: dormant')
    expect(within(screen.getByRole('table', { name: 'BTC-reference stress' })).getAllByRole('row')).toHaveLength(5)
    expect(card()).toHaveTextContent('Current-book hypothetical worst day')
  })
  it('preserves signed short sensitivities and positive downside gains', async () => {
    stubSession(); vi.spyOn(api, 'portfolioReview').mockResolvedValue(fx.review({ netExposure: -6000, betaBtc: -0.02, stress: [{ scenario: 'BTC −10%', shockPct: -10, estPnl: 200, estPnlPct: 0.2 }] })); open()
    await screen.findByRole('heading', { name: 'Concentrated book' }); expect(card()).toHaveTextContent('Net exposure-$6,000'); expect(card()).toHaveTextContent('-0.02'); expect(card()).toHaveTextContent('up $200.00'); expect(card()).toHaveTextContent('+0.20%')
  })
  it('shows unavailable whole-book risk and null drawdown beside explicitly estimated facts', async () => {
    stubSession(); vi.spyOn(api, 'portfolioReview').mockResolvedValue(missing()); open()
    await screen.findByRole('heading', { name: 'Concentrated book' }); expect(card()).toHaveTextContent('Missing marks and daily history for APT.'); expect(card()).toHaveTextContent('Missing support: APT'); expect(card()).toHaveTextContent('Entry-price estimates'); expect(card()).toHaveTextContent('Prediction stakes$250'); expect(card()).toHaveTextContent('0.25%'); expect(screen.getByText('Drawdown · 30d').nextSibling).toHaveTextContent('—'); expect(screen.getByText('1-day VaR · 95%').nextSibling).toHaveTextContent('—'); expect(card()).not.toHaveTextContent('separate bets')
  })
  it.each(['Insufficient common daily observations.', 'BTC reference is unavailable.', 'Nonpositive account equity.'])('shows the server risk reason: %s', async (reason) => {
    stubSession(); vi.spyOn(api, 'portfolioReview').mockResolvedValue(fx.review({ ...missing(), coverage: { ...missing().coverage, reason }, exposurePct: null, predictionStakesPct: null })); open(); expect(await screen.findByText(reason)).toBeInTheDocument(); expect(card()).not.toHaveTextContent('0.00% of equity')
  })
  it('shows prediction-only facts and allows review, including stakes below ten percent', async () => {
    stubSession(); vi.spyOn(api, 'portfolioReview').mockResolvedValue(fx.review({ positions: 0, holdings: [], concentration: null, grossExposure: 0, netExposure: 0, exposurePct: 0, coverage: { modeledGrossExposure: 0, missingSymbols: [], commonDays: 0, reason: null }, stops: { covered: 0, uncovered: [], uncoveredPct: 0, atRiskUsd: 0 }, predictionStakes: 250, predictionStakesPct: 0.25, notes: ['No open positions: nothing to review.'], betaBtc: null, var: null, volAnnPct: null, stress: [], worstDay: null })); open(); await screen.findByRole('heading', { name: 'Outcome stakes to review' }); expect(card()).toHaveTextContent('Prediction stakes$250'); expect(card()).toHaveTextContent('outcome-contract tail risk'); expect(card()).not.toHaveTextContent('nothing to review.'); expect(screen.getByRole('button', { name: /Review with the agent/ })).toBeEnabled()
  })
  it('distinguishes a truly empty book', async () => { stubSession(); vi.spyOn(api, 'portfolioReview').mockResolvedValue(fx.review({ positions: 0, holdings: [], concentration: null, grossExposure: 0, netExposure: 0, exposurePct: 0, coverage: { modeledGrossExposure: 0, missingSymbols: [], commonDays: 0, reason: null }, stops: { covered: 0, uncovered: [], uncoveredPct: 0, atRiskUsd: 0 }, predictionStakes: 0 })); open(); await screen.findByRole('heading', { name: 'Nothing to review yet' }); expect(screen.getByRole('button', { name: /Review with the agent/ })).toBeDisabled() })
  it('renders supplied pair unknowns and groups without a transitive or independence claim', async () => {
    stubSession(); vi.spyOn(api, 'portfolioReview').mockResolvedValue(fx.review({ correlations: [{ a: 'A', b: 'B', rho: 0.9, days: 40 }, { a: 'B', b: 'C', rho: 0.9, days: 25 }, { a: 'A', b: 'C', rho: null, days: 8 }], clusters: [{ symbols: ['A', 'B'], rho: 0.9, pct: 60, days: 40 }] })); open(); await screen.findByRole('heading', { name: 'Concentrated book' }); expect(within(screen.getByRole('list', { name: 'Asset correlations' })).getByText(/A · C/).parentElement).toHaveTextContent('Unknown · 8 shared days'); expect(card()).not.toHaveTextContent('A · B · C'); expect(card()).toHaveTextContent('does not establish independence')
  })
  it.each([['valid', 'text-up'], ['dormant', 'text-accent-ink'], ['crossed', 'text-down'], ['missing', 'text-down'], ['unknown', 'text-muted']] as const)('makes the %s stop status readable with its actual status tone', async (stopStatus, tone) => {
    stubSession(); vi.spyOn(api, 'portfolioReview').mockResolvedValue(fx.review({ holdings: [{ ...fx.review().holdings[0], stopStatus, stopLoss: stopStatus === 'missing' ? null : 2700, halted: stopStatus === 'dormant' }] })); open()
    await screen.findByRole('heading', { name: 'Concentrated book' })
    const label = within(screen.getByRole('meter').closest('li')!).getByText(`Stop: ${stopStatus}`)
    expect(label).toHaveClass(tone)
    if (stopStatus !== 'valid') expect(label).not.toHaveClass('text-up')
  })
  it('polls at sixty seconds with unchanged revision and aborts on hide/unmount', async () => {
    vi.useFakeTimers(); stubSession(); const spy = vi.spyOn(api, 'portfolioReview').mockResolvedValue(fx.review()); const view = open(); await act(() => Promise.resolve()); await act(() => Promise.resolve()); spy.mockResolvedValue(fx.review({ grossExposure: 7000, asOf: '2026-10-05T12:01:00Z' })); await act(() => vi.advanceTimersByTimeAsync(59000)); expect(spy).toHaveBeenCalledTimes(1); await act(() => vi.advanceTimersByTimeAsync(1000)); expect(card()).toHaveTextContent('Gross exposure$7,000'); Object.defineProperty(document, 'hidden', { configurable: true, value: true }); fireEvent(document, new Event('visibilitychange')); expect(spy.mock.calls.at(-1)![0]?.aborted).toBe(true); await act(() => vi.advanceTimersByTimeAsync(60000)); expect(spy).toHaveBeenCalledTimes(2); Object.defineProperty(document, 'hidden', { configurable: true, value: false }); fireEvent(document, new Event('visibilitychange')); await act(() => Promise.resolve()); expect(spy).toHaveBeenCalledTimes(3); view.unmount(); expect(spy.mock.calls.at(-1)![0]?.aborted).toBe(true)
  })
  it('rejects reversed responses and strictly older revisions', async () => {
    stubSession(); const first = deferred<ReturnType<typeof fx.review>>(); const second = deferred<ReturnType<typeof fx.review>>(); vi.spyOn(api, 'portfolioReview').mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise).mockResolvedValue(fx.review({ revision: 1, grossExposure: 123 })); const { user } = open(); await user.click(screen.getByRole('button', { name: 'Bump revision' })); await act(() => { second.resolve(fx.review({ revision: 2, grossExposure: 8000 })) }); await act(() => { first.resolve(fx.review({ revision: 1, grossExposure: 123 })) }); expect(card()).toHaveTextContent('Gross exposure$8,000'); await user.click(screen.getByRole('button', { name: 'Bump revision' })); expect(await screen.findByRole('alert')).toHaveTextContent('older account revision'); expect(card()).not.toHaveTextContent('$123')
  })
  it('accepts ahead revisions with one bounded Account refresh', async () => { stubSession(); const provider = vi.spyOn(api, 'portfolio'); vi.spyOn(api, 'portfolioReview').mockResolvedValue(fx.review({ revision: 5 })); open(); await screen.findByRole('heading', { name: 'Concentrated book' }); await waitFor(() => expect(provider).toHaveBeenCalledTimes(2)) })
  it('retains same-revision stale facts on refresh failure and recovers with Retry', async () => {
    stubSession(); const spy = vi.spyOn(api, 'portfolioReview').mockResolvedValueOnce(fx.review()).mockRejectedValueOnce(new ApiError(502, 'upstream', 'Marks unavailable.')).mockResolvedValue(fx.review({ grossExposure: 8000, asOf: '2026-10-05T12:02:00Z' })); const { user } = open(); await screen.findByRole('heading', { name: 'Concentrated book' }); fireEvent(document, new Event('visibilitychange')); expect(await screen.findByRole('alert')).toHaveTextContent('Marks unavailable.'); expect(card()).toHaveTextContent('Stale'); expect(card()).toHaveTextContent('12:00:00'); await user.click(screen.getByRole('button', { name: 'Retry' })); await waitFor(() => expect(card()).toHaveTextContent('Gross exposure$8,000')); expect(card()).not.toHaveTextContent('Stale'); expect(spy).toHaveBeenCalledTimes(3)
  })
  it('clears private facts and rejects pending success after reset/null', async () => {
    stubSession(); const late = deferred<ReturnType<typeof fx.review>>(); vi.spyOn(api, 'portfolioReview').mockResolvedValueOnce(fx.review()).mockReturnValueOnce(late.promise); const { user } = open(); await screen.findByRole('heading', { name: 'Concentrated book' }); fireEvent(document, new Event('visibilitychange')); await user.click(screen.getByRole('button', { name: 'Clear account' })); await act(() => late.resolve(fx.review({ grossExposure: 9999 }))); expect(card()).not.toHaveTextContent('$9,999'); expect(card()).not.toHaveTextContent('$6,000')
  })
  it('waits for the portfolio before asking', () => { stubSession(); const spy = vi.spyOn(api, 'portfolioReview'); open(null); expect(spy).not.toHaveBeenCalled() })
  it('hands off only a plain prompt', async () => { stubSession(); vi.spyOn(api, 'portfolioReview').mockResolvedValue(fx.review()); const { user } = open(); const button = screen.getByRole('button', { name: /Review with the agent/ }); await waitFor(() => expect(button).toBeEnabled()); await user.click(button); expect(currentPath()).toBe('/'); expect(await screen.findByText('Agent page')).toBeInTheDocument() })
})

describe('PortfolioHealth · equal revisions', () => {
  it('refuses an older same-revision asOf and keeps the newer measured facts stale', async () => {
    stubSession(); vi.spyOn(api, 'portfolioReview').mockResolvedValueOnce(fx.review({ asOf: '2026-10-05T12:02:00Z', grossExposure: 8000 })).mockResolvedValueOnce(fx.review({ asOf: '2026-10-05T12:00:00Z', grossExposure: 123 })); open(); await screen.findByRole('heading', { name: 'Concentrated book' }); fireEvent(document, new Event('visibilitychange')); expect(await screen.findByRole('alert')).toHaveTextContent('older snapshot'); expect(card()).toHaveTextContent('Gross exposure$8,000'); expect(card()).not.toHaveTextContent('$123')
  })
  it('independently invalidates reversed same-revision reads despite later old response timestamps', async () => {
    stubSession(); const old = deferred<ReturnType<typeof fx.review>>(); const fresh = deferred<ReturnType<typeof fx.review>>(); vi.spyOn(api, 'portfolioReview').mockResolvedValueOnce(fx.review()).mockReturnValueOnce(old.promise).mockReturnValueOnce(fresh.promise); open(); await screen.findByRole('heading', { name: 'Concentrated book' }); fireEvent(document, new Event('visibilitychange')); fireEvent(document, new Event('visibilitychange')); await act(() => fresh.resolve(fx.review({ grossExposure: 8000, asOf: '2026-10-05T12:01:00Z' }))); await act(() => old.resolve(fx.review({ grossExposure: 123, asOf: '2026-10-05T12:03:00Z' }))); expect(card()).toHaveTextContent('Gross exposure$8,000'); expect(card()).not.toHaveTextContent('$123')
  })
})

function CallerHealth() {
  const { refresh } = useAuth()
  return <><PortfolioHealth revision={1} /><button onClick={() => void refresh()}>Switch caller</button></>
}
describe('PortfolioHealth · caller isolation', () => {
  it('aborts the previous caller and ignores its delayed result after an identity switch', async () => {
    const session = stubSession(); session.me.mockResolvedValueOnce({ user: fx.user, credits: fx.credits }).mockResolvedValue({ user: { ...fx.user, id: 'other' }, credits: fx.credits });
    const old = deferred<ReturnType<typeof fx.review>>(); const spy = vi.spyOn(api, 'portfolioReview').mockReturnValueOnce(old.promise).mockResolvedValue(fx.review({ grossExposure: 8000 }));
    const { user } = renderPage(<CallerHealth />); await waitFor(() => expect(spy).toHaveBeenCalledTimes(1)); await user.click(screen.getByRole('button', { name: 'Switch caller' })); await screen.findByRole('heading', { name: 'Concentrated book' }); await act(() => old.resolve(fx.review({ grossExposure: 123 }))); expect(card()).toHaveTextContent('Gross exposure$8,000'); expect(card()).not.toHaveTextContent('$123'); expect(spy.mock.calls[0][0]?.aborted).toBe(true)
  })
})

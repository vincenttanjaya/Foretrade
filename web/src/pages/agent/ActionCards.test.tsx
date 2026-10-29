import { act, fireEvent, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, api } from '../../lib/api'
import type { Card, ForecastDraft, ForecastSummary } from '../../lib/types'
import * as fx from '../../test/fixtures'
import { creditsUsed, renderPage, stubSession } from '../../test/utils'
import { AgentCard } from './cards'

const draft: ForecastDraft = {
  shape: 'binary',
  question: 'Will the Binance SOL/USDT daily close on 2026-11-28 be above $150?',
  outcomes: ['Yes', 'No'],
  resolutionCriteria: 'Resolves Yes if the SOLUSDT 1d candle of 2026-11-28 closes above $150.',
  resolutionDate: '2026-11-28',
  category: 'crypto',
  priceRule: { symbol: 'SOL', op: 'above', threshold: 150, mode: 'on_date' },
}
type Brief = NonNullable<ForecastSummary['view']>
const draftCard = (forecast: Extract<Card, { type: 'forecast_draft' }>['data']['forecast'] = null): Card => ({ type: 'forecast_draft', data: { draft, forecast } })
const buildCard = (over: Partial<Extract<Card, { type: 'build_action' }>['data']> = {}): Card => ({
  type: 'build_action',
  data: { kind: 'portfolio', prompt: 'a defensive crypto basket', symbol: null, interval: null, cost: 2, result: null, ...over },
})
const running = fx.forecast({ id: 'f9', ...draft, status: 'running', result: null, completedAt: null })
const completed = fx.forecast({
  id: 'f9',
  ...draft,
  result: { ...fx.forecastResult, probabilities: [{ outcome: 'Yes', p: 0.46 }, { outcome: 'No', p: 0.54 }] },
})

beforeEach(() => {
  stubSession()
})

describe('ForecastDraftCard', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it('shows the draft, runs the desk on Run and follows the run to its headline', async () => {
    vi.useFakeTimers()
    const create = vi.spyOn(api, 'createForecast').mockResolvedValue({ forecast: running, credits: { ...fx.credits, used: 26 } })
    const poll = vi.spyOn(api, 'forecast').mockResolvedValueOnce({ forecast: running }).mockResolvedValue({ forecast: completed })
    renderPage(<AgentCard card={draftCard()} />)
    expect(screen.getByText('Forecast desk draft')).toBeInTheDocument()
    expect(screen.getByText(draft.question)).toBeInTheDocument()
    expect(screen.getByText('Binary')).toBeInTheDocument()
    expect(screen.getByText('Resolves Nov 28, 2026')).toBeInTheDocument()
    expect(screen.getByText('Auto-resolves from Binance')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Open Forecast desk' })).toHaveAttribute('href', '/forecasts/new')

    fireEvent.click(screen.getByRole('button', { name: 'Run the desk · 5 cr' }))
    await act(async () => {})
    expect(create).toHaveBeenCalledWith(draft)
    expect(creditsUsed()).toBe('26')
    expect(screen.getByRole('status')).toHaveTextContent('Specialists at work… usually four to eight minutes')
    expect(screen.getByRole('link', { name: 'Open report' })).toHaveAttribute('href', '/forecasts/f9')
    expect(poll).toHaveBeenCalledTimes(1) // right away, then every 3 s

    await act(() => vi.advanceTimersByTimeAsync(3000))
    expect(screen.getByText('Yes 46%')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Open report' })).toHaveAttribute('href', '/forecasts/f9')
    expect(screen.queryByRole('button', { name: /Run the desk/ })).toBeNull()
    await act(() => vi.advanceTimersByTimeAsync(9000))
    expect(poll).toHaveBeenCalledTimes(2) // stops once completed
  })

  it('resumes a stored running forecast, and offers Retry when the run failed', async () => {
    const poll = vi.spyOn(api, 'forecast').mockResolvedValue({ forecast: { ...running, status: 'failed', error: 'The forecast timed out. Try again.' } })
    const create = vi.spyOn(api, 'createForecast').mockResolvedValue({ forecast: running, credits: fx.credits })
    const { user } = renderPage(<AgentCard card={draftCard({ id: 'f9', status: 'running', headline: null })} />)
    expect(screen.getByRole('status')).toHaveTextContent('Specialists at work')
    expect(await screen.findByText('The forecast timed out. Try again.')).toBeInTheDocument()
    expect(poll).toHaveBeenCalledWith('f9')
    await user.click(screen.getByRole('button', { name: 'Retry' }))
    expect(create).toHaveBeenCalledWith(draft)
  })

  it('shows a stored completed run without polling', () => {
    const poll = vi.spyOn(api, 'forecast')
    renderPage(<AgentCard card={draftCard({ id: 'f9', status: 'completed', headline: { outcome: 'Yes', p: 0.46 } })} />)
    expect(screen.getByText('Yes 46%')).toBeInTheDocument()
    expect(poll).not.toHaveBeenCalled()
  })

  it("states a completed numeric run by its view, as the forecast list does, never its top range", async () => {
    vi.useFakeTimers()
    const tsm = fx.tsmDeskForecast({ id: 'f9' })
    vi.spyOn(api, 'forecast').mockResolvedValue({ forecast: tsm })
    renderPage(<AgentCard card={draftCard({ id: 'f9', status: 'running', headline: null })} />)
    await act(async () => {})
    expect(screen.getByText('Median $611')).toBeInTheDocument()
    expect(screen.getByText('62% ≥ today')).toBeInTheDocument()
    expect(screen.queryByText(/≥ \$1,100/)).toBeNull()
  })

  it('reads a stored numeric run from its view brief: the median, or the 80% range of a view with no basis', () => {
    const brief: Brief = { median: { value: 610.9 }, p10: { value: 241.9, approx: true }, p90: { value: 1476.4, approx: true }, pAbove: { value: 0.621 }, unit: { prefix: '$', suffix: '' }, flat: true, headline: 'view', basis: 'scenarios' }
    const stored = (view: Brief) => draftCard({ id: 'f9', status: 'completed', headline: { outcome: '≥ $1,100', p: 0.2 }, view, viewHeadline: 'ignored by the page' })
    const { unmount } = renderPage(<AgentCard card={stored(brief)} />)
    expect(screen.getByText('Median $611')).toBeInTheDocument()
    expect(screen.getByText('62% ≥ today')).toBeInTheDocument()
    expect(screen.queryByText('≥ $1,100 20%')).toBeNull()
    unmount()
    renderPage(<AgentCard card={stored({ ...brief, pAbove: null, basis: 'none' })} />)
    expect(screen.getByText('≈$242–≈$1,480')).toBeInTheDocument()
    expect(screen.getByText('80% range')).toBeInTheDocument()
    expect(screen.queryByText(/Median/)).toBeNull()
  })

  it('keeps the top range of a run whose view does not lead, and of a binary run', () => {
    const bucket: Brief = { median: { value: 67710 }, p10: { value: 62353 }, p90: { value: 73488 }, pAbove: { value: 0.641 }, unit: { prefix: '$', suffix: '' }, flat: false, headline: 'bucket', basis: 'capped_drift' }
    renderPage(<AgentCard card={draftCard({ id: 'f9', status: 'completed', headline: { outcome: '$66,000 – $70,000', p: 0.46 }, view: bucket })} />)
    expect(screen.getByText('$66,000 – $70,000 46%')).toBeInTheDocument()
    expect(screen.queryByText(/Median/)).toBeNull()
  })
})

describe('BuildActionCard', () => {
  it('builds the portfolio once on tap, disabled while it runs, and shows the result', async () => {
    let resolve!: (v: Awaited<ReturnType<typeof api.buildPortfolio>>) => void
    const build = vi.spyOn(api, 'buildPortfolio').mockReturnValue(new Promise((r) => (resolve = r)))
    const { user } = renderPage(<AgentCard card={buildCard()} />)
    expect(screen.getByText('AI portfolio')).toBeInTheDocument()
    expect(screen.getByText('“a defensive crypto basket”')).toBeInTheDocument()
    const button = screen.getByRole('button', { name: 'Build portfolio · 2 cr' })
    await user.click(button)
    expect(button).toBeDisabled()
    await user.click(button)
    expect(build).toHaveBeenCalledTimes(1)
    expect(build).toHaveBeenCalledWith('a defensive crypto basket')

    await act(async () => resolve({ portfolio: fx.aiPortfolio(), credits: { ...fx.credits, used: 20 } }))
    expect(screen.getByText('Layer-1 core')).toBeInTheDocument()
    expect(screen.getAllByRole('listitem').map((li) => li.textContent)).toEqual(['BTC 50%', 'ETH 30%', 'PAXG 20%'])
    expect(screen.getByText(/Backtest \+18\.0% vs BTC \+9\.0% · 181 daily bars/)).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Open & invest' })).toHaveAttribute('href', '/portfolios/p1')
    expect(creditsUsed()).toBe('20')
  })

  it('generates the strategy with its symbol and interval and links to the Studio', async () => {
    const metrics = { ...fx.strategy().metrics, totalReturnPct: 8.2, buyHoldReturnPct: 5.1, sharpe: 1.32, maxDrawdownPct: 6.4 }
    const gen = vi.spyOn(api, 'generateStrategy').mockResolvedValue({ strategy: fx.strategy({ id: 's7', name: 'SOL RSI dip', metrics }), credits: fx.credits })
    const { user } = renderPage(<AgentCard card={buildCard({ kind: 'strategy', prompt: 'Buy SOL when RSI < 30', symbol: 'SOL', interval: '1h' })} />)
    expect(screen.getByText('Strategy backtest')).toBeInTheDocument()
    expect(screen.getByText('SOL · 1h')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Generate & backtest · 2 cr' }))
    expect(gen).toHaveBeenCalledWith({ prompt: 'Buy SOL when RSI < 30', symbol: 'SOL', interval: '1h' })
    expect(await screen.findByText('SOL RSI dip')).toBeInTheDocument()
    expect(screen.getByText('Return +8.2% vs buy & hold +5.1% · Sharpe 1.32 · Max DD 6.4%')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Open in Studio' })).toHaveAttribute('href', '/strategies/s7')
  })

  it('shows a stored result without a Build button', () => {
    const result = { id: 'p1', name: 'Defensive core', returnPct: 12.4, benchmarkReturnPct: 8.1, benchmarkSymbol: 'BTC' as const, benchmarkLabel: 'BTC', windowStart: null, windowEnd: null, bars: null, sharpe: null, maxDrawdownPct: 6, holdings: [{ symbol: 'BTC', weight: 0.4 }] }
    renderPage(<AgentCard card={buildCard({ result })} />)
    expect(screen.getByText(/Backtest \+12\.4% vs BTC \+8\.1% · Window unavailable/)).toBeInTheDocument()
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('shows a failure inline with Retry; out of credits is a toast instead', async () => {
    const build = vi.spyOn(api, 'buildPortfolio').mockRejectedValueOnce(new ApiError(502, 'upstream', 'The AI model is unavailable right now.'))
    build.mockRejectedValueOnce(new ApiError(429, 'ai_quota', 'Daily AI credit limit reached.'))
    const { user } = renderPage(<AgentCard card={buildCard()} />)
    await user.click(screen.getByRole('button', { name: 'Build portfolio · 2 cr' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('The AI model is unavailable right now.')
    await user.click(screen.getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('Daily AI credits used up. They reset at 00:00 UTC.'))
    expect(screen.getByRole('button', { name: 'Build portfolio · 2 cr' })).toBeEnabled()
  })
})

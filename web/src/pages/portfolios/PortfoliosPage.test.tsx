import { screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError } from '../../lib/api'
import * as fx from '../../test/fixtures'
import { creditsUsed, currentPath, renderPage, stubSession } from '../../test/utils'
import { PortfolioDetailPage } from './PortfolioDetailPage'
import { PortfoliosPage } from './PortfoliosPage'

const open = () => renderPage(<PortfoliosPage />, { route: '/portfolios', path: '/portfolios', routes: { '/portfolios/:id': <PortfolioDetailPage /> } })

beforeEach(() => {
  stubSession()
  vi.spyOn(api, 'aiPortfolios').mockResolvedValue({ portfolios: [fx.aiPortfolio()] })
})

describe('PortfoliosPage', () => {
  it('builds a portfolio from one sentence and opens it', async () => {
    const build = vi.spyOn(api, 'buildPortfolio').mockResolvedValue({ portfolio: fx.aiPortfolio({ id: 'p9' }), credits: { ...fx.credits, used: 20 } })
    const detail = vi.spyOn(api, 'aiPortfolio').mockResolvedValue({ portfolio: fx.aiPortfolio({ id: 'p9' }) })
    const { user } = open()
    expect(screen.getByRole('heading', { name: 'Describe a portfolio in one sentence.' })).toBeInTheDocument()
    await user.type(screen.getByLabelText('Describe your portfolio'), '  Gold-hedged majors ')
    await user.click(screen.getByRole('button', { name: /Build portfolio/ }))
    expect(build).toHaveBeenCalledWith('Gold-hedged majors')
    expect(await screen.findByRole('heading', { level: 1, name: 'Layer-1 core' })).toBeInTheDocument()
    expect(currentPath()).toBe('/portfolios/p9')
    expect(creditsUsed()).toBe('20')
    await waitFor(() => expect(detail).toHaveBeenCalledTimes(1))
    expect(detail.mock.calls[0][0]).toBe('p9') // seeded static allocation still fetches caller-local overlap
  })

  it('shows the build stage while waiting and errors after', async () => {
    let fail!: (e: unknown) => void
    vi.spyOn(api, 'buildPortfolio').mockReturnValue(new Promise((_, r) => (fail = r)))
    const { user } = open()
    await user.click(screen.getByRole('button', { name: 'Gold-hedged crypto core' }))
    expect(screen.getByLabelText('Describe your portfolio')).toHaveValue('Gold-hedged crypto core')
    await user.click(screen.getByRole('button', { name: /Build portfolio/ }))
    expect(screen.getByText('Screening every asset on available daily return, volatility and drawdown…')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Building…/ })).toBeDisabled()
    fail(new ApiError(429, 'ai_quota', 'quota'))
    expect(await screen.findByRole('alert')).toHaveTextContent('Daily AI credits used up. They reset at 00:00 UTC.')
  })

  it('starts from the forecast desk in one tap', async () => {
    const { user } = open()
    await user.click(screen.getByRole('button', { name: 'From my forecast desk' }))
    expect(screen.getByLabelText('Describe your portfolio')).toHaveValue(
      'Build a basket from what my forecast desk is most bullish on, equal-weighted, and skip anything it leans against.',
    )
    expect(screen.getByRole('button', { name: 'From my forecast desk' })).toHaveAttribute('aria-pressed', 'true')
  })

  it('badges a basket a desk forecast drives', async () => {
    const driven = fx.aiPortfolio().holdings.map((h, i) => (i ? h : { ...h, driver: { kind: 'forecast' as const, id: 'f1', label: 'Forecast · Yes 62%' } }))
    const stats = fx.aiPortfolio().holdings.map((h) => ({ ...h, driver: { kind: 'stats' as const, id: null, label: '180-day stats' } }))
    vi.mocked(api.aiPortfolios).mockResolvedValue({
      portfolios: [fx.aiPortfolio({ id: 'p1', name: 'Driven', holdings: driven }), fx.aiPortfolio({ id: 'p2', name: 'Stats only', holdings: stats })],
    })
    open()
    expect(await screen.findByRole('link', { name: /Driven/ })).toHaveTextContent('Desk-driven')
    expect(screen.getByRole('link', { name: /Stats only/ })).not.toHaveTextContent('Desk-driven')
  })

  it('lists earlier portfolios with weights and returns', async () => {
    open()
    const card = await screen.findByRole('link', { name: /Layer-1 core/ })
    expect(card).toHaveAttribute('href', '/portfolios/p1')
    expect(card).toHaveTextContent('Holdings: BTC, ETH, PAXG')
    expect(card).toHaveTextContent('“Layer-1s with less drawdown than BTC”')
    expect(card).toHaveTextContent('Measured return181 daily bars'); expect(card).toHaveTextContent('▲up 18.0%')
    expect(card).toHaveTextContent('BTC▲up 9.0%')
    expect(screen.getByText('1 built')).toBeInTheDocument()
  })

  it('has an empty state and a retry', async () => {
    vi.mocked(api.aiPortfolios).mockRejectedValueOnce(new ApiError(500, 'internal', 'Database is down'))
    const { user } = open()
    expect(await screen.findByRole('heading', { name: "Couldn't load your portfolios." })).toBeInTheDocument()
    vi.mocked(api.aiPortfolios).mockResolvedValueOnce({ portfolios: [] })
    await user.click(screen.getByRole('button', { name: 'Try again' }))
    expect(await screen.findByRole('heading', { name: 'No portfolios yet.' })).toBeInTheDocument()
  })

  it('badges the portfolios you hold with their paper return', async () => {
    vi.mocked(api.aiPortfolios).mockResolvedValue({
      portfolios: [fx.aiPortfolio({ id: 'p1', invested: fx.invested() }), fx.aiPortfolio({ id: 'p2', name: 'Not yet' }), fx.aiPortfolio({ id: 'p3', name: 'Down', invested: fx.invested({ returnPct: -2.04 }) })],
    })
    open()
    expect(await screen.findByText('Invested $10,000 · +1.2%')).toBeInTheDocument()
    expect(screen.getByText('Invested $10,000 · -2.0%')).toBeInTheDocument()
    expect(screen.getAllByText(/^Invested /)).toHaveLength(2)
  })
})

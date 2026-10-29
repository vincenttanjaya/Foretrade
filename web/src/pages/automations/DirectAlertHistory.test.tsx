import { screen, waitFor, within } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from '../../lib/api'
import * as fx from '../../test/fixtures'
import { linkedMarket } from '../../test/predictions'
import { renderPage, stubSession } from '../../test/utils'
import { AutomationsPage } from './AutomationsPage'

const direct = { ...fx.alert(), id: 'owned-direct', symbol: '', trigger: 'odds' as const, status: 'fired' as const,
  action: 'notify' as const, notional: null, stopLoss: null, takeProfit: null,
  forecastId: null, marketSlug: linkedMarket.marketSlug, marketId: linkedMarket.marketId, linkedMarket,
  market: { question: linkedMarket.question, url: 'https://polymarket.com/event/bitcoin-october', outcome: null, yesPrice: 0.6, asOf: linkedMarket.asOf },
  op: 'above' as const, price: 0.5, firedPrice: 0.6, firedAt: '2026-10-05T13:00:00Z' }
beforeEach(() => {
  stubSession()
  vi.spyOn(api, 'alerts').mockResolvedValue({ alerts: [direct] })
  vi.spyOn(api, 'forecast')
  vi.spyOn(api, 'predictionDraft')
})
describe('owned direct odds notification destinations', () => {
  it('opens History, selects and focuses the retained alert with its original source and no fabricated report', async () => {
    renderPage(<AutomationsPage />, { route: '/automations?tab=history&alert=owned-direct', path: '/automations' })
    const row = await screen.findByRole('region', { name: 'Selected automation' })
    await waitFor(() => expect(row).toHaveFocus())
    expect(row).toHaveTextContent(linkedMarket.question)
    expect(within(row).getByRole('link', { name: /View market source/ })).toHaveAttribute('href', 'https://polymarket.com/event/bitcoin-october')
    expect(within(row).queryByRole('link', { name: 'View forecast' })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: /History/ })).toHaveAttribute('aria-pressed', 'true')
    expect(api.forecast).not.toHaveBeenCalled()
  })

  it('explains unavailable retained history instead of selecting a different market', async () => {
    renderPage(<AutomationsPage />, { route: '/automations?tab=history&alert=unknown-alert', path: '/automations' })
    expect(await screen.findByText(/This alert is not available in your retained history/)).toBeInTheDocument()
    await waitFor(() => expect(screen.queryByRole('region', { name: 'Selected automation' })).not.toBeInTheDocument())
  })

  it('keeps a retained direct leaf and source when its live quote is unavailable, outside today’s board', async () => {
    vi.mocked(api.alerts).mockResolvedValue({ alerts: [{ ...direct, market: null, linkedMarket: { ...linkedMarket, endDate: '2026-10-05T16:00:00Z' } }] })
    renderPage(<AutomationsPage />, { route: '/automations?tab=history&alert=owned-direct', path: '/automations' })
    const row = await screen.findByRole('region', { name: 'Selected automation' })
    expect(row).toHaveTextContent(linkedMarket.question)
    expect(within(row).getByRole('link', { name: /View market source/ })).toHaveAttribute('href', 'https://polymarket.com/event/bitcoin-october')
    expect(api.predictionDraft).not.toHaveBeenCalled()
  })
})

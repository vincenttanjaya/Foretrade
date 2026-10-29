import { screen, waitFor, within } from '@testing-library/react'
import { useLocation } from 'react-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError } from '../../lib/api'
import type { Alert, PriceAlertProposal } from '../../lib/types'
import * as fx from '../../test/fixtures'
import { currentPath, renderPage, stubSession } from '../../test/utils'
import { AgentCard } from './cards'

function Prompt() {
  const { state } = useLocation() as { state: { prompt?: string } | null }
  return <p data-testid="prompt">{state?.prompt}</p>
}

/** A node of the pipeline by its label: Trigger, Action or Deliver. */
const node = (label: string) => screen.getByText(label, { selector: '.label' }).parentElement!

const show = (data: PriceAlertProposal = fx.priceAlert(), stored: Alert[] = []) => {
  const list = vi.spyOn(api, 'alerts').mockResolvedValue({ alerts: stored })
  return { list, ...renderPage(<AgentCard card={{ type: 'price_alert', data }} />, { route: '/chat/c1', path: '/chat/:id', routes: { '/': <Prompt /> } }) }
}

beforeEach(() => {
  stubSession()
})

describe('AlertCard', () => {
  it('shows and confirms a foreign price proposal in its original currency', async () => {
    const create = vi.spyOn(api, 'createAlert').mockResolvedValue({ alert: fx.alert() })
    const { user } = show(fx.priceAlert({ symbol: '6501.T', price: 4500, markPrice: 5000, currency: 'JPY',
      action: 'notify', notional: null, stopLoss: null, takeProfit: null, distancePct: -10 }))
    expect(node('Trigger')).toHaveTextContent('6501.T falls below JPY 4,500.00')
    await user.click(screen.getByRole('button', { name: 'Set alert' }))
    expect(create).toHaveBeenCalledWith(expect.objectContaining({ symbol: '6501.T', price: 4500, currency: 'JPY', action: 'notify' }))
    expect(await screen.findByText('Alert set')).toBeInTheDocument()
  })

  it('uses the stored alert currency for the fired level, including a normalized older proposal', async () => {
    show(fx.priceAlert({ symbol: '6501.T', action: 'notify', price: 45 }),
      [fx.alert({ symbol: '6501.T', status: 'fired', currency: 'JPY', firedPrice: 4490 })])
    expect(await screen.findByText('Fired at JPY 4,490.00')).toBeInTheDocument()
  })

  it('draws trigger → action → deliver and sets the alert with the card’s proposalId', async () => {
    const create = vi.spyOn(api, 'createAlert').mockResolvedValue({ alert: fx.alert() })
    const { user, list, container } = show()
    expect(node('Trigger')).toHaveTextContent('SOL falls below $142.50 · 5.0% below now')
    expect(node('Action')).toHaveTextContent('Paper buy $1,000 SOLstop $135.00 · target $165.00')
    expect(node('Deliver')).toHaveTextContent('Bell · in-app · Account')
    expect(screen.getByText('Buy SOL at a level')).toBeInTheDocument()
    expect(screen.getByText('Buy the retest of support.')).toBeInTheDocument()
    expect(screen.queryByText(/not financial advice/)).toBeNull() // the message's disclaimer covers it
    await waitFor(() => expect(list).toHaveBeenCalledTimes(1))
    expect(list.mock.calls[0][0]).toBe('all')

    await user.click(screen.getByRole('button', { name: 'Set alert' }))
    expect(create).toHaveBeenCalledWith({
      symbol: 'SOL',
      op: 'below',
      price: 142.5,
      action: 'buy',
      notional: 1000,
      stopLoss: 135,
      takeProfit: 165,
      note: 'Buy the retest of support.',
      source: 'agent',
      proposalId: fx.priceAlert().proposalId,
    })
    expect(await screen.findByText('Alert set')).toBeInTheDocument()
    expect(container).not.toHaveTextContent('✓') // the check icon says it
    expect(screen.getByRole('link', { name: /Manage in Automations/ })).toHaveAttribute('href', '/automations')
    expect(screen.queryByRole('button', { name: 'Set alert' })).toBeNull()
  })

  it('draws a schedule and sets it as an automation with trigger schedule', async () => {
    const create = vi.spyOn(api, 'createAlert').mockResolvedValue({ alert: fx.schedule() })
    const { user, container } = show(fx.scheduleCard())
    expect(screen.getByText('Scheduled automation · paper')).toBeInTheDocument()
    expect(screen.getByText('Buy BTC every week')).toBeInTheDocument()
    expect(node('Trigger')).toHaveTextContent('Mondays · 09:00 UTC')
    expect(node('Action')).toHaveTextContent(/^ActionPaper buy \$100 BTC$/)
    expect(node('Deliver')).toHaveTextContent('Bell · in-app · Account')
    await user.click(screen.getByRole('button', { name: 'Set automation' }))
    expect(create).toHaveBeenCalledWith({
      symbol: 'BTC',
      trigger: 'schedule',
      every: 'week',
      at: '09:00',
      weekday: 1,
      action: 'buy',
      notional: 100,
      note: 'Dollar-cost average into BTC.',
      source: 'agent',
      proposalId: fx.scheduleCard().proposalId,
    })
    expect(create.mock.calls[0][0]).not.toHaveProperty('op')
    expect(await screen.findByText('Automation set')).toBeInTheDocument()
    expect(container).not.toHaveTextContent('✓')
  })

  it('draws a daily brief: the bell only', () => {
    show(fx.scheduleCard({ every: 'day', at: '08:00', weekday: null, action: 'notify', notional: null }))
    expect(screen.getByText('Daily brief · BTC')).toBeInTheDocument()
    expect(within(node('Trigger')).getByText('Every day · 08:00 UTC')).toBeInTheDocument()
    expect(node('Action')).toHaveTextContent('Brief: price, 24h move, sentiment, top headline')
    expect(node('Deliver')).toHaveTextContent(/^DeliverBell · in-app$/)
    expect(screen.getByRole('button', { name: 'Set automation' })).toBeInTheDocument()
  })

  it('titles a weekdays schedule: a weekday brief, or an order every weekday', () => {
    const { unmount } = show(fx.scheduleCard({ every: 'weekdays', at: '08:00', weekday: null, action: 'notify', notional: null }))
    expect(screen.getByText('Weekday brief · BTC')).toBeInTheDocument()
    expect(within(node('Trigger')).getByText('Weekdays · 08:00 UTC')).toBeInTheDocument()
    unmount()
    show(fx.scheduleCard({ every: 'weekdays', at: '08:00', weekday: null }))
    expect(screen.getByText('Buy BTC every weekday')).toBeInTheDocument()
  })

  it('posts a notify card without an order, and reads a 409 "already set" as set', async () => {
    const create = vi.spyOn(api, 'createAlert').mockRejectedValue(new ApiError(409, 'conflict', 'This alert was already set.'))
    const { user } = show(fx.priceAlert({ op: 'above', price: 160, distancePct: 6.67, action: 'notify', notional: null, stopLoss: null, takeProfit: null }))
    expect(node('Trigger')).toHaveTextContent('SOL rises above $160.00 · 6.7% above now')
    expect(node('Action')).toHaveTextContent(/^ActionNotify me$/)
    expect(node('Deliver')).toHaveTextContent(/^DeliverBell · in-app$/)
    await user.click(screen.getByRole('button', { name: 'Set alert' }))
    expect(create).toHaveBeenCalledWith(expect.not.objectContaining({ notional: expect.anything() }))
    expect(await screen.findByText('Alert set')).toBeInTheDocument()
  })

  it('shows a refusal inline and offers to ask the agent for a new level', async () => {
    vi.spyOn(api, 'createAlert').mockRejectedValue(new ApiError(400, 'bad_request', 'SOL is already below $142.50 (now $141.90).'))
    const { user } = show()
    await user.click(screen.getByRole('button', { name: 'Set alert' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('SOL is already below $142.50 (now $141.90).')
    expect(screen.getByRole('button', { name: 'Set alert' })).toBeEnabled()
    await user.click(screen.getByRole('button', { name: 'Ask the agent for a new level' }))
    expect(currentPath()).toBe('/')
    expect(screen.getByTestId('prompt')).toHaveTextContent(
      'My SOL alert at $142.50 was refused: SOL is already below $142.50 (now $141.90). Suggest a new level from here.',
    )
  })

  it('after a reload, shows what became of its alert', async () => {
    const { unmount } = show(fx.priceAlert(), [fx.alert({ proposalId: 'someone-else' }), fx.alert()])
    expect(await screen.findByText('Alert set · Active')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Set alert' })).toBeNull()
    unmount()

    const fired = show(fx.priceAlert(), [fx.alert({ status: 'fired', firedPrice: 142.3, firedAt: '2026-10-02T14:05:00Z' })])
    expect(await screen.findByText('Fired at $142.30')).toBeInTheDocument()
    fired.unmount()

    show(fx.priceAlert(), [fx.alert({ status: 'cancelled' })])
    expect(await screen.findByText('Cancelled')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Set alert' })).toBeNull()
  })
  it('draws a prediction-market odds card and sets it with trigger odds', async () => {
    const create = vi.spyOn(api, 'createAlert').mockResolvedValue({ alert: fx.oddsAlert({ source: 'agent', proposalId: fx.oddsCard().proposalId }) })
    const { user } = show(fx.oddsCard())
    expect(screen.getByText('Prediction-market alert · paper')).toBeInTheDocument()
    expect(screen.getByText('Polymarket above 62%')).toBeInTheDocument()
    expect(node('Trigger')).toHaveTextContent('Polymarket “ETH outperforms BTC in Q4 2026?” rises above 62% · now 55%')
    expect(node('Action')).toHaveTextContent('Notify me')
    expect(node('Deliver')).toHaveTextContent('Bell · in-app')
    expect(screen.getByText(/Re-checked about once a minute, 0 credits\./)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Set alert' }))
    expect(create).toHaveBeenCalledWith({
      symbol: '',
      trigger: 'odds',
      forecastId: 'f1234567-aaaa',
      op: 'above',
      price: 0.62,
      action: 'notify',
      note: "When the market reaches the desk's number.",
      source: 'agent',
      proposalId: fx.oddsCard().proposalId,
    })
    expect(await screen.findByText('Alert set')).toBeInTheDocument()
  })

  it('a refused odds card asks the agent about the forecast; a fired one shows its Yes price', async () => {
    vi.spyOn(api, 'createAlert').mockRejectedValue(new ApiError(400, 'bad_request', 'The market is already at 63%. Pick a level above it.'))
    const { user, unmount } = show(fx.oddsCard())
    await user.click(screen.getByRole('button', { name: 'Set alert' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('The market is already at 63%. Pick a level above it.')
    await user.click(screen.getByRole('button', { name: 'Ask the agent for a new level' }))
    expect(screen.getByTestId('prompt')).toHaveTextContent(
      'My Polymarket alert at 62% was refused: The market is already at 63%. Pick a level above it. Suggest a new level from here.',
    )
    unmount()
    show(fx.oddsCard(), [fx.oddsAlert({ proposalId: fx.oddsCard().proposalId, status: 'fired', firedPrice: 0.625 })])
    expect(await screen.findByText('Fired at 62.5%')).toBeInTheDocument()
  })
})

import { screen, waitFor, within } from '@testing-library/react'
import { useState } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from '../../lib/api'
import * as fx from '../../test/fixtures'
import { renderPage, stubSession } from '../../test/utils'
import { blankTicket, OrderTicket, type Ticket } from './OrderTicket'
import { TradesTable } from './tables'

/** The ticket as TradePage holds it; "Use plan" stands in for the Copilot's pre-fill. */
function Harness() {
  const [ticket, setTicket] = useState<Ticket>(blankTicket())
  return (
    <>
      <OrderTicket symbol="SOL" price={150} ticket={ticket} onChange={setTicket} onFilled={() => {}} />
      <button type="button" onClick={() => setTicket({ ...blankTicket(), amount: '500', source: 'copilot', note: 'Copilot swing plan' })}>
        Use plan
      </button>
    </>
  )
}

beforeEach(() => {
  stubSession()
})

describe('OrderTicket · Limit', () => {
  it('places a limit buy at the default trigger, 1% under the price, and lists it under the ticket', async () => {
    const list = vi.spyOn(api, 'alerts').mockResolvedValue({ alerts: [] })
    const create = vi.spyOn(api, 'createAlert').mockResolvedValue({ alert: fx.alert({ price: 148.5, stopLoss: 140 }) })
    const { user } = renderPage(<Harness />)
    await screen.findByText('$194,388.00') // buying power: the portfolio has loaded
    expect(screen.queryByRole('region', { name: 'Open orders' })).toBeNull() // none yet
    await user.click(screen.getByRole('button', { name: 'Limit' }))
    expect(screen.getByLabelText('Trigger price')).toHaveValue(148.5)
    expect(screen.getByText('-1.00% from the live $150.00')).toBeInTheDocument()
    await user.type(screen.getByLabelText('Amount (USD)'), '1000')
    await user.type(screen.getByLabelText('Stop loss'), '140')
    await user.type(screen.getByLabelText('Take profit'), '165')
    expect(
      screen.getByText('Fills at market (± 2 bps) once SOL trades at or below $148.50, checked every 15 s. Buying power is checked when it fills.'),
    ).toBeInTheDocument()

    list.mockResolvedValue({ alerts: [fx.alert({ price: 148.5, stopLoss: 140 })] })
    await user.click(screen.getByRole('button', { name: 'Place limit buy · $1,000 at $148.50' }))
    expect(create).toHaveBeenCalledWith({
      symbol: 'SOL',
      op: 'below',
      price: 148.5,
      action: 'buy',
      notional: 1000,
      stopLoss: 140,
      takeProfit: 165,
      note: undefined,
      source: 'ticket',
    })
    expect(await screen.findByText('Limit buy placed')).toBeInTheDocument()
    const orders = await screen.findByRole('region', { name: 'Open orders' })
    expect(within(orders).getByText('Open orders · 1')).toBeInTheDocument()
    expect(list).toHaveBeenLastCalledWith('active', expect.any(AbortSignal))
    expect(screen.getByLabelText('Amount (USD)')).toHaveValue(null) // ticket reset
  })

  it('derives the side of the trigger, refuses one at the price, and goes back to Market for a Copilot plan', async () => {
    vi.spyOn(api, 'alerts').mockResolvedValue({ alerts: [] })
    const { user } = renderPage(<Harness />)
    await screen.findByText('$194,388.00')
    await user.click(screen.getByRole('button', { name: 'Limit' }))
    await user.click(screen.getByRole('button', { name: /Sell/ }))
    expect(screen.getByLabelText('Trigger price')).toHaveValue(151.5) // 1% over for a sell: a breakout entry
    await user.type(screen.getByLabelText('Amount (USD)'), '200')
    expect(screen.getByText(/once SOL trades at or above \$151\.50/)).toBeInTheDocument()
    await user.clear(screen.getByLabelText('Trigger price'))
    await user.type(screen.getByLabelText('Trigger price'), '150')
    expect(screen.getByText('Set a trigger price above or below the live price.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Place limit sell/ })).toBeDisabled()

    await user.click(screen.getByRole('button', { name: 'Use plan' }))
    expect(screen.getByRole('button', { name: 'Market' })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.queryByLabelText('Trigger price')).toBeNull()
  })
})

describe('OpenOrders', () => {
  it("lists this symbol's buy/sell price alerts (no schedules) and cancels one", async () => {
    vi.spyOn(api, 'alerts').mockResolvedValue({
      alerts: [
        fx.alert(),
        fx.alert({ id: 'al2', symbol: 'ETH' }),
        fx.alert({ id: 'al3', action: 'notify', notional: null, stopLoss: null, takeProfit: null }),
        fx.schedule({ id: 'sc2', symbol: 'SOL' }), // a weekly SOL buy: an automation, not an open order
      ],
    })
    const cancel = vi.spyOn(api, 'cancelAlert').mockResolvedValue(undefined)
    const { user } = renderPage(<Harness />)
    const orders = await screen.findByRole('region', { name: 'Open orders' })
    expect(within(orders).getByText('Open orders · 1')).toBeInTheDocument()
    expect(within(orders).getByRole('listitem')).toHaveTextContent('Buy $1,000 at ≤ $142.50 · SL $135.00 · TP $165.00')
    expect(within(orders).getByRole('link', { name: 'Automations' })).toHaveAttribute('href', '/automations?symbol=SOL')

    await user.click(within(orders).getByRole('button', { name: 'Cancel limit buy at $142.50' }))
    expect(cancel).toHaveBeenCalledWith('al1')
    expect(await screen.findByText('Limit buy cancelled.')).toBeInTheDocument()
    await waitFor(() => expect(screen.queryByRole('region', { name: 'Open orders' })).toBeNull())
  })
})

describe('TradesTable · alert fills', () => {
  it('badges the Alert source and links its fill to Automations', () => {
    const ref = { kind: 'alert' as const, id: 'al1', label: 'Alert: SOL below $142.50' }
    renderPage(<TradesTable trades={[fx.trade({ symbol: 'SOL', source: 'alert', note: ref.label, ref })]} error="" onRetry={() => {}} empty={null} />)
    const link = screen.getAllByRole('link').find((a) => a.textContent?.includes('Alert'))!
    expect(link).toHaveAttribute('href', '/automations')
    expect(link).toHaveAttribute('title', 'Alert: SOL below $142.50')
  })
})

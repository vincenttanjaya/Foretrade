import { act, screen, waitFor, within } from '@testing-library/react'
import { useLocation } from 'react-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError } from '../../lib/api'
import type { Alert, Asset } from '../../lib/types'
import * as fx from '../../test/fixtures'
import { currentPath, renderPage, stubSession } from '../../test/utils'
import { AutomationsPage } from './AutomationsPage'

function Prompt() {
  const { state } = useLocation() as { state: { prompt?: string } | null }
  return <p data-testid="prompt">{state?.prompt}</p>
}

const show = (alerts: Alert[] = [], route = '/automations') => {
  const list = vi.spyOn(api, 'alerts').mockResolvedValue({ alerts })
  return { list, ...renderPage(<AutomationsPage />, { route, path: '/automations', routes: { '/': <Prompt /> } }) }
}
const live = () => screen.findByText(/from the live|is at \$/) // assets loaded: the price hint reads the live price

beforeEach(() => {
  stubSession()
})

describe('AutomationsPage', () => {
  it('creates foreign-underlying perp notifications in USD', async () => {
    vi.mocked(api.assets).mockResolvedValue({ assets: fx.universeAssets })
    const create = vi.spyOn(api, 'createAlert').mockResolvedValue({ alert: fx.alert({ symbol: 'SOFTBANK' }) })
    const { user } = show([], '/automations?symbol=SOFTBANK')
    await waitFor(() => expect(screen.getByLabelText('Asset')).toHaveValue('SOFTBANK'))
    const price = screen.getByLabelText('Price (USD)')
    await waitFor(() => expect(price).toBeEnabled())
    await user.type(price, '100')
    await user.click(screen.getByRole('button', { name: 'Create automation' }))
    expect(create).toHaveBeenCalledWith({ symbol: 'SOFTBANK', op: 'below', price: 100, action: 'notify', note: undefined, source: 'manual' })
  })

  it('creates a notify alert from the form and lists it as active', async () => {
    const create = vi.spyOn(api, 'createAlert').mockResolvedValue({
      alert: fx.alert({ id: 'n1', symbol: 'ETH', op: 'above', price: 3100, action: 'notify', notional: null, stopLoss: null, takeProfit: null, note: 'Breakout', source: 'manual' }),
    })
    const { user } = show()
    expect(screen.getByRole('heading', { level: 1, name: 'Automations' })).toBeInTheDocument()
    expect(
      screen.getByText('Price triggers, schedules, prediction-market moves and drawdowns from the all-time high that notify you, brief you or place a paper order. Checked every 15 seconds.'),
    ).toBeInTheDocument()
    await live()
    await user.selectOptions(screen.getByLabelText('Asset'), 'ETH')
    await user.click(screen.getByRole('button', { name: 'rises above' }))
    await user.type(screen.getByLabelText('Price (USD)'), '3100')
    expect(screen.getByText('+3.3% from the live $3,000.00')).toBeInTheDocument()
    await user.type(screen.getByLabelText('Note'), 'Breakout')
    await user.click(screen.getByRole('button', { name: 'Create automation' }))
    expect(create).toHaveBeenCalledWith({ symbol: 'ETH', op: 'above', price: 3100, action: 'notify', note: 'Breakout', source: 'manual' })
    expect(await screen.findByText('Automation created. Checked every 15 seconds.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Active · 1' })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByText('Breakout')).toBeInTheDocument()
    expect(screen.getByLabelText('Price (USD)')).toHaveValue(null) // the form resets
  })

  it('posts a buy with its amount and levels, and shows a refusal inline', async () => {
    const create = vi
      .spyOn(api, 'createAlert')
      .mockRejectedValue(new ApiError(400, 'bad_request', 'For a long, the stop must be below the price ($140.00).'))
    const { user } = show([], '/automations?symbol=sol')
    await live()
    expect(screen.getByLabelText('Asset')).toHaveValue('SOL') // ?symbol= preselects
    await user.type(screen.getByLabelText('Price (USD)'), '140')
    await user.click(screen.getByRole('button', { name: 'Buy' }))
    expect(screen.getByRole('button', { name: 'Create automation' })).toBeDisabled() // no amount yet
    await user.type(screen.getByLabelText('Amount ($)'), '500')
    await user.type(screen.getByLabelText('Stop'), '145')
    await user.click(screen.getByRole('button', { name: 'Create automation' }))
    expect(create).toHaveBeenCalledWith({
      symbol: 'SOL',
      op: 'below',
      price: 140,
      action: 'buy',
      notional: 500,
      stopLoss: 145,
      takeProfit: undefined,
      note: undefined,
      source: 'manual',
    })
    expect(await screen.findByRole('alert')).toHaveTextContent('For a long, the stop must be below the price ($140.00).')
  })

  it('draws each alert as trigger → action → deliver with its status, and cancels an active one', async () => {
    const cancel = vi.spyOn(api, 'cancelAlert').mockResolvedValue(undefined)
    const { user } = show([
      fx.alert(),
      fx.alert({ id: 'f1', action: 'notify', notional: null, stopLoss: null, takeProfit: null, note: '', status: 'fired', firedPrice: 142.3, firedAt: '2026-10-02T21:05:00Z' }),
      fx.alert({ id: 'f2', status: 'fired', firedPrice: 142.4, firedAt: '2026-10-02T21:06:00Z', tradeId: 't1' }),
      fx.alert({ id: 'x1', symbol: 'ETH', status: 'failed', error: 'Not enough buying power when the alert fired.', firedAt: '2026-10-02T20:00:00Z', firedPrice: 2900 }),
      fx.alert({ id: 'c1', status: 'cancelled', source: 'ticket', note: '' }),
    ])
    const card = (await screen.findByText('stop $135.00 · target $165.00')).closest('.card') as HTMLElement
    expect(screen.getByRole('button', { name: 'Active · 1' })).toBeInTheDocument()
    expect(within(card).getByText('Trigger').parentElement).toHaveTextContent('SOL falls below $142.50')
    expect(within(card).getByText('Action').parentElement).toHaveTextContent('Paper buy $1,000 SOL')
    expect(within(card).getByText('Deliver').parentElement).toHaveTextContent('Bell · in-app · Account')
    await waitFor(() => expect(within(card).getByText('now $150.00 (5.0% away)')).toBeInTheDocument())
    expect(within(card).getByText('Active')).toBeInTheDocument()
    expect(within(card).getByText(/· From the agent/)).toHaveTextContent('Buy the retest of support. · From the agent')

    await user.click(screen.getByRole('button', { name: 'History · 4' }))
    expect(screen.getByText('Fired Oct 2, 14:05 at $142.30')).toBeInTheDocument() // the viewer's time zone (tests run in LA)
    expect(within(screen.getByRole('region', { name: 'Your automations' })).getByText('Notify me')).toBeInTheDocument()
    expect(screen.getByText('Fired Oct 2, 14:06 at $142.40').closest('span')!.querySelector('a')).toHaveAttribute('href', '/account')
    expect(screen.queryByText('Filling…')).toBeNull() // f2 has its tradeId
    expect(screen.getByText('Failed: Not enough buying power when the alert fired.')).toBeInTheDocument()
    expect(screen.getByText('Cancelled')).toBeInTheDocument()
    expect(screen.getByText('From the trade ticket')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Cancel' })).toBeNull()

    await user.click(screen.getByRole('button', { name: 'Active · 1' }))
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(cancel).toHaveBeenCalledWith('al1')
    expect(await screen.findByText('No active automations.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'History · 5' })).toBeInTheDocument()
  })

  it('refuses a level the price is already past before Create, with a way to trade now', async () => {
    const create = vi.spyOn(api, 'createAlert')
    const { user } = show([], '/automations?symbol=sol') // SOL at $150.00
    await live()
    await user.click(screen.getByRole('button', { name: 'rises above' }))
    await user.type(screen.getByLabelText('Price (USD)'), '140')
    const hint = screen.getByText(/is already above/).closest('p')!
    expect(hint).toHaveTextContent(
      'SOL is already above $140.00 (now $150.00). Pick a level above the price, or trade now on the Trade page.',
    )
    expect(within(hint).getByRole('link', { name: 'trade now on the Trade page' })).toHaveAttribute('href', '/trade/SOL')
    expect(screen.getByRole('button', { name: 'Create automation' })).toBeDisabled()
    await user.click(screen.getByRole('button', { name: 'falls below' }))
    expect(screen.getByText('−6.7% from the live $150.00')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Create automation' })).toBeEnabled()
    await user.clear(screen.getByLabelText('Price (USD)'))
    await user.type(screen.getByLabelText('Price (USD)'), '150')
    expect(screen.getByText(/is already below/).closest('p')).toHaveTextContent(
      'SOL is already below $150.00 (now $150.00). Pick a level below the price, or trade now on the Trade page.',
    )
    expect(screen.getByRole('button', { name: 'Create automation' })).toBeDisabled()
    expect(create).not.toHaveBeenCalled()
  })

  it('starts from an empty state whose chips prefill the form from live prices', async () => {
    const { user } = show()
    expect(await screen.findByRole('heading', { name: 'No automations yet' })).toBeInTheDocument()
    await live()
    await user.click(screen.getByRole('button', { name: 'Buy $500 of SOL if it dips 5%' }))
    expect(screen.getByLabelText('Asset')).toHaveValue('SOL')
    expect(screen.getByRole('button', { name: 'falls below' })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByLabelText('Price (USD)')).toHaveValue(142.5)
    expect(screen.getByRole('button', { name: 'Buy' })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByLabelText('Amount ($)')).toHaveValue(500)

    await user.click(screen.getByRole('button', { name: 'Alert me if BTC falls 5%' }))
    expect(screen.getByLabelText('Asset')).toHaveValue('BTC')
    expect(screen.getByLabelText('Price (USD)')).toHaveValue(80002.54)
    expect(screen.getByRole('button', { name: 'Notify me' })).toHaveAttribute('aria-pressed', 'true')

    await user.click(screen.getByRole('button', { name: 'Tell me when ETH breaks its 24h high' }))
    expect(screen.getByLabelText('Asset')).toHaveValue('ETH')
    expect(screen.getByRole('button', { name: 'rises above' })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByLabelText('Price (USD)')).toHaveValue(3060)
  })
})

describe('AutomationsPage · a level in local currency', () => {
  it('creates a foreign notify alert in the listing currency and validates against its listing price', async () => {
    const dbs = fx.feedAsset('DBS', 'DBS Group')
    vi.mocked(api.assets).mockResolvedValue({ assets: [...fx.assets, dbs] })
    const create = vi.spyOn(api, 'createAlert').mockResolvedValue({ alert: fx.alert({ symbol: 'DBS', price: 75, currency: 'SGD', action: 'notify' }) })
    const { user } = show([], '/automations?symbol=DBS')
    const input = await screen.findByLabelText('Price (SGD)')
    expect(screen.queryByLabelText('Price (USD)')).toBeNull()
    expect(screen.getByText('DBS is at SGD 78.56')).toBeInTheDocument()
    await user.type(input, '80')
    expect(screen.getByText(/DBS is already below SGD 80.00/)).toHaveTextContent('now SGD 78.56')
    expect(screen.getByRole('button', { name: 'Create automation' })).toBeDisabled()
    await user.clear(input)
    await user.type(input, '75')
    expect(screen.getByText('−4.5% from the live SGD 78.56')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Create automation' }))
    expect(create).toHaveBeenCalledWith({ symbol: 'DBS', op: 'below', price: 75, currency: 'SGD', action: 'notify', note: undefined, source: 'manual' })
    expect(await screen.findByText('Automation created. Checked every 15 seconds.')).toBeInTheDocument()
  })

  it('waits for the listing quote before enabling a foreign notify alert', async () => {
    let resolve!: (value: { assets: Asset[] }) => void
    vi.mocked(api.assets).mockReturnValue(new Promise((done) => { resolve = done }))
    const { user } = show([], '/automations?symbol=DBS')
    expect(screen.queryByLabelText('Price (USD)')).toBeNull()
    expect(screen.getByRole('spinbutton')).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Create automation' })).toBeDisabled()
    await act(async () => resolve({ assets: [...fx.assets, fx.feedAsset('DBS', 'DBS Group')] }))
    const input = await screen.findByLabelText('Price (SGD)')
    await user.type(input, '75')
    expect(screen.getByRole('button', { name: 'Create automation' })).toBeEnabled()
  })

  it('blocks a foreign notify alert when listing metadata is missing', async () => {
    const dbs = fx.feedAsset('DBS', 'DBS Group', 0, { localCurrency: null, quoteFeed: null })
    vi.mocked(api.assets).mockResolvedValue({ assets: [...fx.assets, dbs] })
    const create = vi.spyOn(api, 'createAlert')
    const { user } = show([], '/automations?symbol=DBS')
    expect(await screen.findByText('Listing price unavailable. Try again shortly.')).toBeInTheDocument()
    expect(screen.queryByLabelText('Price (USD)')).toBeNull()
    await user.type(screen.getByRole('spinbutton'), '50')
    expect(screen.getByRole('button', { name: 'Create automation' })).toBeDisabled()
    expect(create).not.toHaveBeenCalled()
  })

  it('clears a foreign level when switching between native alerts and USD paper orders', async () => {
    const dbs = fx.feedAsset('DBS', 'DBS Group')
    vi.mocked(api.assets).mockResolvedValue({ assets: [...fx.assets, dbs] })
    const create = vi.spyOn(api, 'createAlert').mockResolvedValue({ alert: fx.alert({ symbol: 'DBS', price: 50, action: 'buy' }) })
    const { user } = show([], '/automations?symbol=DBS')
    await user.type(await screen.findByLabelText('Price (SGD)'), '75')
    await user.click(screen.getByRole('button', { name: 'Buy' }))
    expect(screen.getByLabelText('Price (USD)')).toHaveValue(null)
    await user.type(screen.getByLabelText('Price (USD)'), '50')
    await user.type(screen.getByLabelText('Amount ($)'), '500')
    await user.click(screen.getByRole('button', { name: 'Notify me' }))
    expect(screen.getByLabelText('Price (SGD)')).toHaveValue(null)
    expect(screen.getByRole('button', { name: 'Create automation' })).toBeDisabled()
    await user.click(screen.getByRole('button', { name: 'Buy' }))
    await user.type(screen.getByLabelText('Price (USD)'), '50')
    await user.click(screen.getByRole('button', { name: 'Create automation' }))
    expect(create).toHaveBeenCalledWith({ symbol: 'DBS', op: 'below', price: 50, action: 'buy', notional: 500, stopLoss: undefined, takeProfit: undefined, note: undefined, source: 'manual' })
  })

  it("states a quote-feed alert's level, distance and fill in its own currency, against the local price", async () => {
    const dbs = fx.feedAsset('DBS', 'DBS Group') // SGD 78.56 on its listing
    vi.mocked(api.assets).mockImplementation(async () => ({ assets: [...fx.assets, dbs] }))
    const { user } = show([
      fx.alert({ id: 'l1', symbol: 'DBS', op: 'below', price: 75, currency: 'SGD', action: 'notify', notional: null, stopLoss: null, takeProfit: null, status: 'active' }),
      fx.alert({ id: 'l2', symbol: 'DBS', op: 'below', price: 76, currency: 'SGD', action: 'notify', notional: null, stopLoss: null, takeProfit: null, status: 'fired', firedPrice: 75.9, firedAt: '2026-10-13T03:00:00Z' }),
    ])
    expect(await screen.findByText('SGD 75.00')).toBeInTheDocument()
    expect(await screen.findByText(/^now SGD 78\.56 \(/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'History · 1' }))
    expect(screen.getByText(/^Fired .* at SGD 75\.90$/)).toBeInTheDocument()
  })
})

describe('AutomationsPage · workflows', () => {
  it('creates a weekly schedule from the form: weekday only for Week, no stop or target', async () => {
    const create = vi.spyOn(api, 'createAlert').mockResolvedValue({ alert: fx.schedule({ id: 'sc9', symbol: 'ETH', lastRun: null }) })
    const { user } = show()
    await live()
    await user.click(screen.getByRole('button', { name: 'Schedule' }))
    expect(screen.queryByLabelText('Price (USD)')).toBeNull()
    expect(screen.getByLabelText('Time (UTC)')).toHaveValue('08:00')
    expect(screen.getByRole('button', { name: 'Day' })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.queryByLabelText('Weekday')).toBeNull() // daily: no weekday
    expect(screen.getByRole('button', { name: 'Brief me' })).toHaveAttribute('aria-pressed', 'true')
    await user.click(screen.getByRole('button', { name: 'Week' }))
    expect(screen.getByLabelText('Weekday')).toHaveValue('1') // Monday
    await user.selectOptions(screen.getByLabelText('Asset'), 'ETH')
    await user.selectOptions(screen.getByLabelText('Weekday'), 'Friday')
    await user.clear(screen.getByLabelText('Time (UTC)'))
    await user.type(screen.getByLabelText('Time (UTC)'), '09:30')
    await user.click(screen.getByRole('button', { name: 'Buy' }))
    expect(screen.queryByLabelText('Stop')).toBeNull()
    expect(screen.getByRole('button', { name: 'Create automation' })).toBeDisabled() // no amount yet
    await user.type(screen.getByLabelText('Amount ($)'), '100')
    await user.click(screen.getByRole('button', { name: 'Create automation' }))
    expect(create).toHaveBeenCalledWith({
      symbol: 'ETH',
      trigger: 'schedule',
      every: 'week',
      at: '09:30',
      weekday: 5,
      action: 'buy',
      notional: 100,
      note: undefined,
      source: 'manual',
    })
    expect(await screen.findByText('Automation created. Next run Oct 5, 09:00 UTC.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Schedule' })).toHaveAttribute('aria-pressed', 'true') // the form keeps its trigger

    await user.click(screen.getByRole('button', { name: 'Day' }))
    await user.click(screen.getByRole('button', { name: 'Brief me' }))
    await user.click(screen.getByRole('button', { name: 'Create automation' }))
    expect(create).toHaveBeenLastCalledWith({ symbol: 'ETH', trigger: 'schedule', every: 'day', at: '08:00', action: 'notify', note: undefined, source: 'manual' })
  })

  it('creates a weekdays schedule: Monday to Friday, no weekday', async () => {
    const create = vi.spyOn(api, 'createAlert').mockResolvedValue({ alert: fx.schedule({ every: 'weekdays', weekday: null, lastRun: null }) })
    const { user } = show()
    await live()
    await user.click(screen.getByRole('button', { name: 'Schedule' }))
    await user.click(screen.getByRole('button', { name: 'Weekdays' }))
    expect(screen.queryByLabelText('Weekday')).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Create automation' }))
    expect(create).toHaveBeenCalledWith({ symbol: 'BTC', trigger: 'schedule', every: 'weekdays', at: '08:00', action: 'notify', note: undefined, source: 'manual' })
    expect(await screen.findByText('BTC · Weekdays · 09:00 UTC')).toBeInTheDocument()
  })

  it('draws a schedule row with its next and last run, a failed run, and Filling… before a fill is stored', async () => {
    const { user } = show([
      fx.schedule(),
      fx.schedule({
        id: 'sc2',
        symbol: 'SOL',
        every: 'day',
        at: '08:00',
        weekday: null,
        action: 'notify',
        notional: null,
        note: '',
        lastRun: { id: 'failed-sc2', at: '2026-10-03T08:00:00Z', status: 'failed', body: '', error: 'Not enough buying power when the alert fired.', reportChatId: null, excerpt: null, verdict: null },
      }),
      fx.alert({ id: 'f3', status: 'fired', firedPrice: 142.4, firedAt: '2026-10-02T21:06:00Z', tradeId: null }),
    ])
    const card = (await screen.findByText('BTC · Mondays · 09:00 UTC')).closest('.card') as HTMLElement
    expect(within(card).getByText('Action').parentElement).toHaveTextContent('Paper buy $100 BTC')
    expect(within(card).getByText('Next run Oct 5, 09:00 UTC')).toBeInTheDocument()
    expect(within(card).getByText('Last run Sep 28, 09:00 UTC · Bought $100 BTC at $61,240.50')).toBeInTheDocument()
    const brief = screen.getByText('SOL · Every day · 08:00 UTC').closest('.card') as HTMLElement
    expect(within(brief).getByText('Action').parentElement).toHaveTextContent('Brief: price, 24h move, sentiment, top headline')
    expect(within(brief).getByText('Deliver').parentElement).toHaveTextContent(/^DeliverBell · in-app$/)
    expect(within(brief).getByText('Last run Oct 3, 08:00 UTC · Not enough buying power when the alert fired.')).toHaveClass('text-down')

    await user.click(screen.getByRole('button', { name: 'History · 1' }))
    const fired = screen.getByText('Fired Oct 2, 14:06 at $142.40').closest('span')!
    expect(within(fired).getByText('Filling…')).toBeInTheDocument()
    expect(within(fired).queryByText('Filled')).toBeNull()
    expect(fired.querySelector('a')).toBeNull()
  })

  it('sends a described automation to the agent; example chips fill the box', async () => {
    const { user } = show()
    const box = screen.getByRole('textbox', { name: 'Describe an automation' })
    expect(box).toHaveAttribute('maxlength', '400')
    expect(screen.getByRole('button', { name: 'Build with the agent' })).toBeDisabled()
    await user.click(screen.getByRole('button', { name: 'Buy $100 of ETH every Monday at 09:00 UTC' }))
    expect(box).toHaveValue('Buy $100 of ETH every Monday at 09:00 UTC')
    await user.clear(box)
    await user.type(box, '  Every morning at 08:00 UTC, brief me on SOL ')
    await user.click(screen.getByRole('button', { name: 'Build with the agent' }))
    expect(currentPath()).toBe('/')
    expect(screen.getByTestId('prompt')).toHaveTextContent('Every morning at 08:00 UTC, brief me on SOL')
  })
  describe('Prediction market', () => {
    const reports = () => {
      vi.spyOn(api, 'forecasts').mockResolvedValue({
        forecasts: [
          fx.forecastSummary({ id: 'f1' }),
          fx.forecastSummary({ id: 'f2', question: 'Will SOL flip ETH by market cap?' }),
          fx.forecastSummary({ id: 'f3', shape: 'ordered', question: 'Where will BTC close?' }),
          fx.forecastSummary({ id: 'f4', replay: true }),
        ],
      })
      return vi.spyOn(api, 'forecast').mockImplementation(async (id) => ({
        forecast:
          id === 'f2'
            ? fx.forecast({ id, question: 'Will SOL flip ETH by market cap?', result: { ...fx.forecastResult, market: null } })
            : fx.forecast({ id }),
      }))
    }

    it('opens prefilled from the Desk vs market card, checks the level against the market and posts an odds trigger', async () => {
      const detail = reports()
      vi.spyOn(api, 'alerts').mockResolvedValue({ alerts: [] })
      const create = vi.spyOn(api, 'createAlert').mockResolvedValue({ alert: fx.oddsAlert({ id: 'od9', forecastId: 'f1', note: 'Desk number' }) })
      const { user } = renderPage(<AutomationsPage />, {
        route: '/automations',
        path: '/automations',
        state: { trigger: 'odds', forecastId: 'f1', op: 'above', pricePct: 62 },
      })
      expect(screen.getByRole('button', { name: 'Prediction market' })).toHaveAttribute('aria-pressed', 'true')
      expect(await screen.findByText('Polymarket 55% · desk 62%')).toBeInTheDocument()
      expect(screen.getByRole('link', { name: /View on Polymarket/ })).toHaveAttribute('href', 'https://polymarket.com/event/eth-vs-btc-q4-2026')
      expect(screen.getByLabelText('Forecast')).toHaveValue('f1')
      expect(screen.getByRole('option', { name: 'Will SOL flip ETH by market cap? (no market)' })).toBeDisabled()
      expect(screen.queryByRole('option', { name: /Where will BTC close/ })).toBeNull() // ordered and replay rows are left out
      expect(detail.mock.calls.map((c) => c[0]).sort()).toEqual(['f1', 'f2'])
      expect(screen.getByLabelText('Condition')).toHaveValue('above')
      expect(screen.getByLabelText('Level (%)')).toHaveValue(62)
      expect(screen.getByText("+7 pts from the market's 55%")).toBeInTheDocument()
      expect(screen.queryByLabelText('Asset')).toBeNull()
      expect(screen.queryByRole('button', { name: 'Buy' })).toBeNull() // notify only

      await user.clear(screen.getByLabelText('Level (%)'))
      await user.type(screen.getByLabelText('Level (%)'), '50')
      expect(screen.getByText('The market is already at 55%. Pick a level above it.')).toHaveClass('text-down')
      expect(screen.getByRole('button', { name: 'Create automation' })).toBeDisabled()
      await user.selectOptions(screen.getByLabelText('Condition'), 'below')
      expect(screen.getByText("−5 pts from the market's 55%")).toBeInTheDocument()
      await user.clear(screen.getByLabelText('Level (%)'))
      await user.type(screen.getByLabelText('Level (%)'), '120')
      expect(screen.getByText('Pick a level between 1% and 99%.')).toBeInTheDocument()
      expect(screen.getByRole('button', { name: 'Create automation' })).toBeDisabled()

      await user.selectOptions(screen.getByLabelText('Condition'), 'above')
      await user.clear(screen.getByLabelText('Level (%)'))
      await user.type(screen.getByLabelText('Level (%)'), '62.5')
      await user.type(screen.getByLabelText('Note'), 'Desk number')
      await user.click(screen.getByRole('button', { name: 'Create automation' }))
      expect(create).toHaveBeenCalledWith({
        symbol: '',
        trigger: 'odds',
        forecastId: 'f1',
        op: 'above',
        price: 0.625,
        action: 'notify',
        note: 'Desk number',
        source: 'manual',
      })
      expect(await screen.findByText("Alert set. You'll get a bell when Polymarket crosses 62.5%.")).toBeInTheDocument()
      expect(screen.getByRole('button', { name: 'Active · 1' })).toHaveAttribute('aria-pressed', 'true')
      expect(screen.getByLabelText('Forecast')).toHaveValue('f1') // the report stays picked
    })

    it('picks the first report with a market when the tab opens', async () => {
      reports()
      vi.spyOn(api, 'forecasts').mockResolvedValue({
        forecasts: [fx.forecastSummary({ id: 'f2', question: 'Will SOL flip ETH by market cap?' }), fx.forecastSummary({ id: 'f1' })],
      })
      const { user } = show()
      await user.click(screen.getByRole('button', { name: 'Prediction market' }))
      expect(await screen.findByText('Polymarket 55% · desk 62%')).toBeInTheDocument()
      expect(screen.getByLabelText('Forecast')).toHaveValue('f1')
      expect(screen.getByText('A Yes probability, 1–99%.')).toBeInTheDocument()
    })

    it('lists an odds trigger with its market, the live odds and the report', async () => {
      const { user } = show([fx.oddsAlert(), fx.oddsAlert({ id: 'od2', status: 'fired', firedPrice: 0.625, firedAt: '2026-10-02T21:06:00Z' })])
      const row = (await screen.findByText(/^Polymarket · ETH outperforms BTC in Q4 2026\? · above/)).closest('.card') as HTMLElement
      expect(row).toHaveTextContent('Polymarket · ETH outperforms BTC in Q4 2026? · above 62%')
      expect(within(row).getByText('now 55%')).toBeInTheDocument()
      expect(within(row).getByText('Action').parentElement).toHaveTextContent('Notify me')
      expect(within(row).getByRole('link', { name: 'View forecast' })).toHaveAttribute('href', '/forecasts/f1234567-aaaa')
      await user.click(screen.getByRole('button', { name: 'History · 1' }))
      expect(screen.getByText('Fired Oct 2, 14:06 at 62.5%')).toBeInTheDocument()
    })
  })
})

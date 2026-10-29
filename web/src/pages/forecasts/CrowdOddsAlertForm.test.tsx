import { act, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError } from '../../lib/api'
import type { Alert, PredictionEvent, PredictionMarket } from '../../lib/types'
import { renderPage, stubSession } from '../../test/utils'
import { CrowdOddsAlertForm } from './CrowdOddsAlertForm'

const event: Pick<PredictionEvent, 'slug' | 'title' | 'url'> = {
  slug: 'bitcoin-prices-october-2026',
  title: 'Bitcoin prices in October 2026',
  url: 'https://polymarket.com/event/bitcoin-prices-october-2026',
}

// Two independent leaves of one event: a below predicate still uses its own 30% Yes quote.
const belowMarket: PredictionMarket = {
  id: '651101',
  marketSlug: 'bitcoin-below-80000-on-october-20-2026',
  conditionId: '0x1111111111111111111111111111111111111111111111111111111111111111',
  question: 'Will Bitcoin be below $80,000 on October 20, 2026?',
  yesPrice: 0.3,
  change24hPp: -2.4,
  volumeUsd: 125000,
  volume24hUsd: 42000,
  endDate: '2026-10-20T16:00:00Z',
  thin: false,
  rule: null,
  estimateUnavailable: 'Market data is unavailable.',
  estimate: null,
  desk: null,
}

const touchMarket: PredictionMarket = {
  id: '651102',
  marketSlug: 'bitcoin-touch-100000-before-october-20-2026',
  conditionId: '0x2222222222222222222222222222222222222222222222222222222222222222',
  question: 'Will Bitcoin touch $100,000 before October 20, 2026?',
  yesPrice: 0.3,
  change24hPp: null,
  volumeUsd: null,
  volume24hUsd: null,
  endDate: '2026-10-20T16:00:00Z',
  thin: true,
  rule: { symbol: 'BTC', op: 'above', strike: 100000, kind: 'touch' },
  estimateUnavailable: 'Touch contracts have no quant estimate.',
  estimate: null,
  desk: null,
}

const created: { alert: Alert } = {
  alert: {
    id: 'd251c27a-900a-4f3e-86d3-7a77d505f937',
    symbol: '',
    prompt: null,
    thesis: null,
    notifyWhen: null,
    quotaStreak: 0,
    trigger: 'odds',
    op: 'above',
    price: 0.555,
    every: null,
    at: null,
    weekday: null,
    nextRunAt: null,
    lastRun: null,
    action: 'notify',
    notional: null,
    stopLoss: null,
    takeProfit: null,
    note: 'Watch the crowd, please.',
    source: 'manual',
    proposalId: null,
    status: 'active',
    tradeId: null,
    error: null,
    firedPrice: null,
    createdAt: '2026-10-05T02:00:00Z',
    firedAt: null,
    forecastId: null,
    marketSlug: 'bitcoin-below-80000-on-october-20-2026',
    marketId: '651101',
    linkedMarket: {
      marketId: '651101',
      conditionId: '0x1111111111111111111111111111111111111111111111111111111111111111',
      marketSlug: 'bitcoin-below-80000-on-october-20-2026',
      eventSlug: 'bitcoin-prices-october-2026',
      question: 'Will Bitcoin be below $80,000 on October 20, 2026?',
      description: 'Resolves Yes if the original market observation is below $80,000.',
      resolutionSource: 'https://www.binance.com/en/trade/BTC_USDT',
      endDate: '2026-10-20T16:00:00Z',
      asOf: '2026-10-05T02:00:00Z',
    },
    market: {
      question: 'Will Bitcoin be below $80,000 on October 20, 2026?',
      url: 'https://polymarket.com/event/bitcoin-prices-october-2026',
      outcome: null,
      yesPrice: 0.3,
      asOf: '2026-10-05T02:00:00Z',
    },
  },
}

const show = (market = belowMarket) =>
  renderPage(<CrowdOddsAlertForm market={market} event={event} onClose={vi.fn()} />, {
    route: '/forecasts?tab=markets',
  })

beforeEach(() => {
  stubSession()
})

describe('CrowdOddsAlertForm', () => {
  it('identifies the original leaf and its own Yes quote without offering an invented desk preset', () => {
    show()
    expect(screen.getByText('Will Bitcoin be below $80,000 on October 20, 2026?')).toBeInTheDocument()
    expect(screen.getByText(/\b30(?:\.0)?%/)).toBeInTheDocument()
    expect(screen.getByText(/notify[- ]only/i)).toBeInTheDocument()
    expect(screen.getByText(/free|0 credits/i)).toBeInTheDocument()
    expect(screen.getByLabelText('Condition')).toBeInTheDocument()
    expect(screen.getByLabelText('Yes probability (%)')).toBeInTheDocument()
    expect(screen.getByLabelText('Note')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /desk|edge widens/i })).not.toBeInTheDocument()
    expect(screen.queryByLabelText(/stake|notional|stop loss|take profit/i)).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /buy|sell|run desk/i })).not.toBeInTheDocument()
  })

  it('sets a free direct alert with a decimal Yes level and trimmed note, then links to owned Automations', async () => {
    const create = vi.spyOn(api, 'createAlert').mockResolvedValue(created)
    const { user } = show()
    await user.selectOptions(screen.getByLabelText('Condition'), 'above')
    await user.clear(screen.getByLabelText('Yes probability (%)'))
    await user.type(screen.getByLabelText('Yes probability (%)'), '55.5')
    await user.type(screen.getByLabelText('Note'), '  Watch the crowd, please.  ')
    await user.click(screen.getByRole('button', { name: 'Set alert' }))

    expect(create).toHaveBeenCalledTimes(1)
    expect(create.mock.calls[0][0]).toStrictEqual({
      symbol: '',
      trigger: 'odds',
      marketSlug: 'bitcoin-below-80000-on-october-20-2026',
      marketId: '651101',
      op: 'above',
      price: 0.555,
      action: 'notify',
      note: 'Watch the crowd, please.',
      source: 'manual',
    })
    expect(await screen.findByRole('link', { name: /Manage.*Automations/i })).toHaveAttribute('href', '/automations')
    expect(screen.getAllByRole('status').map((status) => status.textContent).join(' ')).toMatch(/alert set/i)
    // Any extra AI, trade or prediction-stake request would reach the unmocked external boundary.
    expect(fetch).not.toHaveBeenCalled()
  })

  it('uses the below leaf’s own Yes price and omits a whitespace-only note', async () => {
    const create = vi.spyOn(api, 'createAlert').mockResolvedValue({
      alert: { ...created.alert, op: 'below', price: 0.2, note: '' },
    })
    const { user } = show()
    await user.selectOptions(screen.getByLabelText('Condition'), 'below')
    await user.clear(screen.getByLabelText('Yes probability (%)'))
    await user.type(screen.getByLabelText('Yes probability (%)'), '20')
    await user.type(screen.getByLabelText('Note'), '   ')
    await user.click(screen.getByRole('button', { name: 'Set alert' }))

    expect(create).toHaveBeenCalledTimes(1)
    expect(create).toHaveBeenCalledWith({
      symbol: '',
      trigger: 'odds',
      marketSlug: 'bitcoin-below-80000-on-october-20-2026',
      marketId: '651101',
      op: 'below',
      price: 0.2,
      action: 'notify',
      note: undefined,
      source: 'manual',
    })
    expect(await screen.findByRole('link', { name: /Manage.*Automations/i })).toHaveAttribute('href', '/automations')
  })

  it.each([
    { name: 'an empty level', op: 'above', level: '' },
    { name: 'a level below 1%', op: 'below', level: '0.9' },
    { name: 'a level above 99%', op: 'above', level: '99.1' },
    { name: 'an above level equal to the current Yes quote', op: 'above', level: '30' },
    { name: 'an above level already crossed by the current Yes quote', op: 'above', level: '29' },
    { name: 'a below level equal to the current Yes quote', op: 'below', level: '30' },
    { name: 'a below level already crossed by the current Yes quote', op: 'below', level: '31' },
  ])('does not create an alert for $name', async ({ op, level }) => {
    const create = vi.spyOn(api, 'createAlert').mockResolvedValue(created)
    const { user } = show()
    await user.selectOptions(screen.getByLabelText('Condition'), op)
    await user.clear(screen.getByLabelText('Yes probability (%)'))
    if (level) await user.type(screen.getByLabelText('Yes probability (%)'), level)
    await user.click(screen.getByRole('button', { name: 'Set alert' }))

    expect(create).not.toHaveBeenCalled()
    expect(screen.queryByRole('link', { name: /Manage.*Automations/i })).not.toBeInTheDocument()
    expect(screen.getByText('Will Bitcoin be below $80,000 on October 20, 2026?')).toBeInTheDocument()
  })

  it.each([
    { op: 'below', level: '1', price: 0.01 },
    { op: 'above', level: '99', price: 0.99 },
  ] as const)('accepts the inclusive $level% boundary for a $op alert', async ({ op, level, price }) => {
    const create = vi.spyOn(api, 'createAlert').mockResolvedValue({
      alert: { ...created.alert, op, price, note: '' },
    })
    const { user } = show()
    await user.selectOptions(screen.getByLabelText('Condition'), op)
    await user.clear(screen.getByLabelText('Yes probability (%)'))
    await user.type(screen.getByLabelText('Yes probability (%)'), level)
    await user.click(screen.getByRole('button', { name: 'Set alert' }))

    expect(create).toHaveBeenCalledTimes(1)
    expect(create).toHaveBeenCalledWith({
      symbol: '',
      trigger: 'odds',
      marketSlug: 'bitcoin-below-80000-on-october-20-2026',
      marketId: '651101',
      op,
      price,
      action: 'notify',
      note: undefined,
      source: 'manual',
    })
    expect(await screen.findByRole('link', { name: /Manage.*Automations/i })).toBeInTheDocument()
  })

  it('keeps the exact target and entered condition after an upstream refusal so retry submits the same leaf', async () => {
    const create = vi.spyOn(api, 'createAlert')
      .mockRejectedValueOnce(new ApiError(502, 'upstream_error', 'Polymarket is unavailable. Try again.'))
      .mockResolvedValueOnce({ alert: { ...created.alert, op: 'below', price: 0.205, note: 'keep this leaf' } })
    const { user } = show()
    await user.selectOptions(screen.getByLabelText('Condition'), 'below')
    await user.clear(screen.getByLabelText('Yes probability (%)'))
    await user.type(screen.getByLabelText('Yes probability (%)'), '20.5')
    await user.type(screen.getByLabelText('Note'), '  keep this leaf  ')
    await user.click(screen.getByRole('button', { name: 'Set alert' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('Polymarket is unavailable. Try again.')
    expect(screen.getByText('Will Bitcoin be below $80,000 on October 20, 2026?')).toBeInTheDocument()
    expect(screen.getByLabelText('Condition')).toHaveValue('below')
    expect(screen.getByLabelText('Yes probability (%)')).toHaveValue(20.5)
    expect(screen.getByLabelText('Note')).toHaveValue('  keep this leaf  ')
    expect(screen.queryByRole('link', { name: /Manage.*Automations/i })).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Set alert' }))
    const expected = {
      symbol: '',
      trigger: 'odds',
      marketSlug: 'bitcoin-below-80000-on-october-20-2026',
      marketId: '651101',
      op: 'below',
      price: 0.205,
      action: 'notify',
      note: 'keep this leaf',
      source: 'manual',
    }
    expect(create).toHaveBeenCalledTimes(2)
    expect(create).toHaveBeenNthCalledWith(1, expected)
    expect(create).toHaveBeenNthCalledWith(2, expected)
    expect(await screen.findByRole('link', { name: /Manage.*Automations/i })).toHaveAttribute('href', '/automations')
  })

  it('reports a fresh already-crossed server rejection without replacing the target or claiming success', async () => {
    const create = vi.spyOn(api, 'createAlert').mockRejectedValue(
      new ApiError(400, 'bad_request', 'The market is already at 63%. Pick a level above it.'),
    )
    const { user } = show()
    await user.selectOptions(screen.getByLabelText('Condition'), 'above')
    await user.clear(screen.getByLabelText('Yes probability (%)'))
    await user.type(screen.getByLabelText('Yes probability (%)'), '55.5')
    await user.click(screen.getByRole('button', { name: 'Set alert' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('The market is already at 63%. Pick a level above it.')
    expect(create).toHaveBeenCalledTimes(1)
    expect(screen.getByText('Will Bitcoin be below $80,000 on October 20, 2026?')).toBeInTheDocument()
    expect(screen.getByLabelText('Yes probability (%)')).toHaveValue(55.5)
    expect(screen.queryByRole('link', { name: /Manage.*Automations/i })).not.toBeInTheDocument()
  })

  it('suppresses repeated submissions while the create request is pending', async () => {
    let finish!: (value: { alert: Alert }) => void
    const pending = new Promise<{ alert: Alert }>((resolve) => { finish = resolve })
    const create = vi.spyOn(api, 'createAlert').mockReturnValue(pending)
    const { user } = show()
    await user.selectOptions(screen.getByLabelText('Condition'), 'above')
    await user.clear(screen.getByLabelText('Yes probability (%)'))
    await user.type(screen.getByLabelText('Yes probability (%)'), '55.5')
    const submit = screen.getByRole('button', { name: 'Set alert' })
    await user.dblClick(submit)
    await user.click(submit)
    await user.keyboard('{Enter}')

    expect(create).toHaveBeenCalledTimes(1)
    expect(submit).toBeDisabled()
    expect(screen.queryByRole('link', { name: /Manage.*Automations/i })).not.toBeInTheDocument()

    await act(async () => { finish({ alert: { ...created.alert, note: '' } }) })
    expect(await screen.findByRole('link', { name: /Manage.*Automations/i })).toHaveAttribute('href', '/automations')
  })

  it('targets distinct leaf slugs from the same event rather than the parent event slug', async () => {
    const create = vi.spyOn(api, 'createAlert')
      .mockResolvedValueOnce({ alert: { ...created.alert, price: 0.4, note: '' } })
      .mockResolvedValueOnce({
        alert: {
          ...created.alert,
          id: '7a544bfd-a8c7-43e2-9b9d-8f20dedb6d63',
          price: 0.4,
          note: '',
          marketSlug: 'bitcoin-touch-100000-before-october-20-2026',
          marketId: '651102',
          linkedMarket: {
            marketId: '651102',
            conditionId: '0x2222222222222222222222222222222222222222222222222222222222222222',
            marketSlug: 'bitcoin-touch-100000-before-october-20-2026',
            eventSlug: 'bitcoin-prices-october-2026',
            question: 'Will Bitcoin touch $100,000 before October 20, 2026?',
            description: 'Resolves Yes if the original market observation touches $100,000 before the deadline.',
            resolutionSource: null,
            endDate: '2026-10-20T16:00:00Z',
            asOf: '2026-10-05T02:00:00Z',
          },
          market: {
            question: 'Will Bitcoin touch $100,000 before October 20, 2026?',
            url: 'https://polymarket.com/event/bitcoin-prices-october-2026',
            outcome: null,
            yesPrice: 0.3,
            asOf: '2026-10-05T02:00:00Z',
          },
        },
      })
    const first = show()
    await first.user.selectOptions(screen.getByLabelText('Condition'), 'above')
    await first.user.clear(screen.getByLabelText('Yes probability (%)'))
    await first.user.type(screen.getByLabelText('Yes probability (%)'), '40')
    await first.user.click(screen.getByRole('button', { name: 'Set alert' }))
    await screen.findByRole('link', { name: /Manage.*Automations/i })
    first.unmount()

    const second = show(touchMarket)
    expect(screen.getByText('Will Bitcoin touch $100,000 before October 20, 2026?')).toBeInTheDocument()
    await second.user.selectOptions(screen.getByLabelText('Condition'), 'above')
    await second.user.clear(screen.getByLabelText('Yes probability (%)'))
    await second.user.type(screen.getByLabelText('Yes probability (%)'), '40')
    await second.user.click(screen.getByRole('button', { name: 'Set alert' }))

    expect(create).toHaveBeenCalledTimes(2)
    expect(create).toHaveBeenNthCalledWith(1, {
      symbol: '', trigger: 'odds', marketSlug: 'bitcoin-below-80000-on-october-20-2026', marketId: '651101',
      op: 'above', price: 0.4, action: 'notify', note: undefined, source: 'manual',
    })
    expect(create).toHaveBeenNthCalledWith(2, {
      symbol: '', trigger: 'odds', marketSlug: 'bitcoin-touch-100000-before-october-20-2026', marketId: '651102',
      op: 'above', price: 0.4, action: 'notify', note: undefined, source: 'manual',
    })
    expect(await screen.findByRole('link', { name: /Manage.*Automations/i })).toBeInTheDocument()
    expect(fetch).not.toHaveBeenCalled()
  })

  it('caps the user-entered note at 200 characters while preserving the accepted note in the alert', async () => {
    const create = vi.spyOn(api, 'createAlert').mockResolvedValue({
      alert: { ...created.alert, price: 0.4, note: 'n'.repeat(200) },
    })
    const { user } = show()
    await user.selectOptions(screen.getByLabelText('Condition'), 'above')
    await user.clear(screen.getByLabelText('Yes probability (%)'))
    await user.type(screen.getByLabelText('Yes probability (%)'), '40')
    await user.type(screen.getByLabelText('Note'), 'n'.repeat(200))
    await user.type(screen.getByLabelText('Note'), 'x')
    expect(screen.getByLabelText('Note')).toHaveValue('n'.repeat(200))
    await user.click(screen.getByRole('button', { name: 'Set alert' }))

    await waitFor(() => expect(create).toHaveBeenCalledTimes(1))
    expect(create).toHaveBeenCalledWith({
      symbol: '', trigger: 'odds', marketSlug: 'bitcoin-below-80000-on-october-20-2026', marketId: '651101',
      op: 'above', price: 0.4, action: 'notify', note: 'n'.repeat(200), source: 'manual',
    })
    expect(await screen.findByRole('link', { name: /Manage.*Automations/i })).toBeInTheDocument()
  })
})

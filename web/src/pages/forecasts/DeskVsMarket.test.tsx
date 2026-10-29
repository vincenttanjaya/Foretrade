import { act, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError } from '../../lib/api'
import type { Forecast, MarketOdds, MarketPosition } from '../../lib/types'
import * as fx from '../../test/fixtures'
import { renderPage, stubSession } from '../../test/utils'
import { cents, evOf, fillPrice, winOf } from './DeskVsMarket'
import { ForecastDetailPage } from './ForecastDetailPage'

// Now is Oct 2, 2026 12:00 in Los Angeles (19:00 UTC): FRESH is half an hour old, STALE two hours.
const FRESH = '2026-10-02T18:30:00Z'
const STALE = '2026-10-02T17:00:00Z'
const ID = 'f1234567-aaaa'

const odds = (over: Partial<MarketOdds> = {}): MarketOdds => ({
  ...fx.marketOdds,
  yesPrice: 0.85,
  edgePp: -23,
  evYes: -0.23,
  evNo: 0.23,
  signal: 'no',
  asOf: FRESH,
  ...over,
})
const withMarket = (m: MarketOdds | null, over: Partial<Forecast> = {}) =>
  fx.forecast({ result: { ...fx.forecastResult, market: m }, ...over })

const nvda = (m: Partial<MarketOdds> = {}) =>
  fx.forecast({
    shape: 'categorical',
    question: 'Which company will be the largest by market cap on 2026-12-31?',
    outcomes: ['Nvidia', 'Apple', 'Another company'],
    result: {
      ...fx.forecastResult,
      probabilities: [{ outcome: 'Nvidia', p: 0.81 }, { outcome: 'Apple', p: 0.15 }, { outcome: 'Another company', p: 0.04 }],
      market: odds({ outcome: 'Nvidia', question: 'Will NVIDIA be the largest company in the world by market cap on December 31?', edgePp: -4, signal: 'wait', ...m }),
    },
  })

const position = (over: Partial<MarketPosition> = {}): MarketPosition => ({
  id: 'p1',
  forecastId: ID,
  forecastQuestion: fx.forecast().question,
  question: fx.marketOdds.question,
  url: fx.marketOdds.url,
  outcome: 'Yes',
  side: 'yes',
  price: 0.86,
  contracts: 100 / 0.86,
  cost: 100,
  maxPayout: 100 / 0.86,
  status: 'open',
  payout: null,
  pnl: null,
  markPrice: 0.85,
  createdAt: '2026-10-02T18:40:00Z',
  settledAt: null,
  ...over,
})

function open(f: Forecast) {
  vi.spyOn(api, 'forecast').mockResolvedValue({ forecast: f })
  return renderPage(<ForecastDetailPage />, { route: `/forecasts/${f.id}`, path: '/forecasts/:id' })
}
const card = () => screen.getByRole('heading', { name: 'Desk vs market' }).closest('section')!
const panel = () => screen.getByRole('group', { name: 'Take a paper position' })
const confirm = () => within(panel()).getByRole('button', { name: 'Confirm (paper)' })

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(new Date(2026, 9, 2, 12))
  stubSession()
})
afterEach(() => vi.useRealTimers())

describe('paper position math', () => {
  it('fills at the side price + 1¢ (at most 99¢) and wins stake / price − stake', () => {
    expect(fillPrice(0.85, 'yes')).toBeCloseTo(0.86)
    expect(fillPrice(0.85, 'no')).toBeCloseTo(0.16)
    expect(fillPrice(0.995, 'yes')).toBe(0.99)
    expect(winOf(100, fillPrice(0.85, 'yes'))).toBe(16.28)
    expect(evOf(100, 0.86, 0.62)).toBeCloseTo(-27.91, 2)
  })

  it('shows prices in tenths of a cent, rounded half up', () => {
    expect(cents(0.0145)).toBe('1.5¢')
    expect(cents(0.175)).toBe('17.5¢')
    expect(cents(0.86)).toBe('86¢')
  })
})

describe('DeskVsMarket · Take a paper position', () => {
  it('prices both sides with the slippage and previews the win, the loss and the EV for each stake', async () => {
    vi.spyOn(api, 'marketPositions').mockResolvedValue({ positions: [] })
    const { user } = open(withMarket(odds()))
    await screen.findByRole('group', { name: 'Take a paper position' })
    const yes = within(panel()).getByRole('button', { name: 'Yes · 86¢' })
    const no = within(panel()).getByRole('button', { name: 'No · 16¢' })
    expect(no).toHaveAttribute('aria-pressed', 'true') // the signal side
    expect(within(panel()).getByRole('button', { name: '$100' })).toHaveAttribute('aria-pressed', 'true')
    expect(panel()).toHaveTextContent('Win $525 if No · lose $100 if Yes')
    expect(panel()).toHaveTextContent("At the desk's 38%: EV +$137.50")

    await user.click(yes)
    expect(yes).toHaveAttribute('aria-pressed', 'true')
    expect(panel()).toHaveTextContent('Win $16.28 if Yes · lose $100 if No')
    expect(panel()).toHaveTextContent("At the desk's 62%: EV −$27.91")
    for (const [preset, preview] of [['$10', 'Win $1.63 if Yes · lose $10 if No'], ['$50', 'Win $8.14 if Yes · lose $50 if No'], ['$500', 'Win $81.40 if Yes · lose $500 if No']]) {
      await user.click(within(panel()).getByRole('button', { name: preset }))
      expect(panel()).toHaveTextContent(preview)
    }

    const custom = within(panel()).getByRole('spinbutton', { name: 'Stake in dollars' })
    await user.clear(custom)
    await user.type(custom, '5')
    expect(panel()).toHaveTextContent('Stake $10 to $1,000.')
    expect(confirm()).toBeDisabled()
    await user.type(custom, '0') // 50
    expect(panel()).toHaveTextContent('Win $8.14 if Yes · lose $50 if No')
    expect(confirm()).toBeEnabled()
  })

  it('starts with no side when the desk says WAIT and keeps Confirm disabled until one is picked', async () => {
    vi.spyOn(api, 'marketPositions').mockResolvedValue({ positions: [] })
    const { user } = open(withMarket(odds({ yesPrice: 0.59, edgePp: 3, evYes: 0.03, evNo: -0.03, signal: 'wait' })))
    await screen.findByRole('group', { name: 'Take a paper position' })
    expect(panel()).toHaveTextContent('The desk says WAIT: the edge is under 5 points or the market is thin. You can still pick a side.')
    expect(confirm()).toBeDisabled()
    await user.click(within(panel()).getByRole('button', { name: 'No · 42¢' }))
    expect(confirm()).toBeEnabled()
    expect(panel()).not.toHaveTextContent('The desk says WAIT')
  })

  it('confirms {side, stake}, updates the account and lists the position', async () => {
    const list = vi.spyOn(api, 'marketPositions').mockResolvedValueOnce({ positions: [] }).mockResolvedValue({ positions: [position()] })
    const buy = vi.spyOn(api, 'openMarketPosition').mockResolvedValue({ position: position(), portfolio: { ...fx.portfolio, cash: 93900, predictionStakes: 100, revision: 2 } })
    const { user } = open(withMarket(odds()))
    await screen.findByRole('group', { name: 'Take a paper position' })
    await user.click(within(panel()).getByRole('button', { name: 'Yes · 86¢' }))
    await user.click(confirm())
    expect(buy).toHaveBeenCalledWith(ID, 'yes', 100)
    expect(await screen.findByText('Bought 116.3 Yes contracts at 86¢ (paper).')).toBeInTheDocument()
    expect(list).toHaveBeenLastCalledWith(ID, expect.any(AbortSignal))
    expect(await within(card()).findByText('Yes · 116.3 contracts @ 86¢ · $100 · pays $116.28 · now 85¢')).toBeInTheDocument()
    expect(within(card()).getByText('Open')).toBeInTheDocument()
  })

  it('toasts a refusal', async () => {
    vi.spyOn(api, 'marketPositions').mockResolvedValue({ positions: [] })
    vi.spyOn(api, 'openMarketPosition').mockRejectedValue(new ApiError(400, 'insufficient_buying_power', 'Not enough buying power.'))
    const { user } = open(withMarket(odds()))
    await user.click(within(await screen.findByRole('group', { name: 'Take a paper position' })).getByRole('button', { name: 'Confirm (paper)' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Not enough buying power.')
  })

  it('names the option on a categorical market', async () => {
    vi.spyOn(api, 'marketPositions').mockResolvedValue({ positions: [position({ outcome: 'Nvidia', side: 'no', price: 0.16, contracts: 625, maxPayout: 625, markPrice: null })] })
    const { user } = open(nvda())
    await screen.findByRole('group', { name: 'Take a paper position' })
    expect(card()).toHaveTextContent('Desk · Nvidia 81%')
    await user.click(within(panel()).getByRole('button', { name: 'Nvidia · Yes 86¢' }))
    expect(panel()).toHaveTextContent('Win $16.28 if Nvidia · lose $100 otherwise')
    await user.click(within(panel()).getByRole('button', { name: 'Nvidia · No 16¢' }))
    expect(panel()).toHaveTextContent('Win $525 if not Nvidia · lose $100 if Nvidia')
    expect(panel()).toHaveTextContent("At the desk's 19%: EV +$18.75")
    expect(within(card()).getByText('No · Nvidia · 625.0 contracts @ 16¢ · $100 · pays $625')).toBeInTheDocument()
  })

  it('hides the panel on a resolved or superseded run and shows how settled positions ended', async () => {
    vi.spyOn(api, 'marketPositions').mockResolvedValue({
      positions: [
        position({ id: 'p2', side: 'no', status: 'lost', payout: 0, pnl: -100, markPrice: null, settledAt: '2026-10-02T18:50:00Z' }),
        position({ status: 'won', payout: 116.28, pnl: 16.28, markPrice: null, settledAt: '2026-10-02T18:50:00Z' }),
      ],
    })
    const first = open(withMarket(odds(), { resolution: { outcome: 'Yes', resolvedAt: '2026-10-02T18:50:00Z', by: 'manual' } }))
    expect(await within(await screen.findByRole('heading', { name: 'Desk vs market' }).then(() => card())).findByText('Won +$16.28')).toBeInTheDocument()
    expect(within(card()).getByText('Lost −$100')).toBeInTheDocument()
    expect(screen.queryByRole('group', { name: 'Take a paper position' })).toBeNull()
    first.unmount()

    open(withMarket(odds(), { history: [{ id: ID, outcome: 'Yes', p: 0.62, at: FRESH }, { id: 'newer', outcome: 'Yes', p: 0.7, at: FRESH }] }))
    await screen.findByRole('heading', { name: 'Desk vs market' })
    expect(screen.queryByRole('group', { name: 'Take a paper position' })).toBeNull()
  })
})

describe('DeskVsMarket · fresh odds', () => {
  it('refreshes odds over an hour old once on open and applies them', async () => {
    vi.spyOn(api, 'marketPositions').mockResolvedValue({ positions: [] })
    const refresh = vi.spyOn(api, 'refreshMarket').mockResolvedValue({ forecast: withMarket(odds({ yesPrice: 0.9, asOf: '2026-10-02T19:00:00Z' })) })
    open(withMarket(odds({ asOf: STALE })))
    await waitFor(() => expect(card()).toHaveTextContent('Market 90% · Polymarket · as of Oct 2, 19:00 UTC'))
    expect(refresh).toHaveBeenCalledOnce()
    expect(refresh).toHaveBeenCalledWith(ID, false)
  })

  it('leaves fresh odds alone and refreshes on demand', async () => {
    vi.spyOn(api, 'marketPositions').mockResolvedValue({ positions: [] })
    const refresh = vi.spyOn(api, 'refreshMarket').mockResolvedValue({ forecast: withMarket(odds({ yesPrice: 0.88 })) })
    const { user } = open(withMarket(odds()))
    const button = await screen.findByRole('button', { name: 'Refresh market odds' })
    expect(refresh).not.toHaveBeenCalled()
    await user.click(button)
    expect(refresh).toHaveBeenCalledWith(ID, false)
    await waitFor(() => expect(card()).toHaveTextContent('Market 88%'))
  })

  it('searches once for a report never matched, then offers Search again (rematch) outside the 10-minute gate', async () => {
    vi.spyOn(api, 'marketPositions').mockResolvedValue({ positions: [] })
    const refresh = vi.spyOn(api, 'refreshMarket').mockResolvedValue({ forecast: withMarket(null, { result: { ...fx.forecastResult, market: null, marketCheckedAt: '2026-10-02T18:57:00Z' } }) })
    const first = open(withMarket(null))
    await waitFor(() => expect(refresh).toHaveBeenCalledWith(ID, false))
    const again = await within(card()).findByRole('button', { name: 'Search again' })
    await waitFor(() => expect(again).toBeDisabled())
    expect(again).toHaveAttribute('title', 'Searched 3 min ago')
    first.unmount()

    refresh.mockClear()
    const { user } = open(fx.forecast({ result: { ...fx.forecastResult, market: null, marketCheckedAt: '2026-10-02T18:40:00Z' } }))
    const button = await within(await screen.findByRole('heading', { name: 'Desk vs market' }).then(() => card())).findByRole('button', { name: 'Search again' })
    expect(refresh).not.toHaveBeenCalled() // searched before: no automatic search
    expect(button).toBeEnabled()
    await user.click(button)
    expect(refresh).toHaveBeenCalledWith(ID, true)
  })

  it('re-enables Search again when the 10-minute gate lifts, without a reload', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date(2026, 9, 2, 12))
    vi.spyOn(api, 'marketPositions').mockResolvedValue({ positions: [] })
    open(fx.forecast({ result: { ...fx.forecastResult, market: null, marketCheckedAt: '2026-10-02T18:52:00Z' } }))
    await act(() => vi.advanceTimersByTimeAsync(0))
    const again = within(card()).getByRole('button', { name: 'Search again' })
    expect(again).toBeDisabled()
    await act(() => vi.advanceTimersByTimeAsync(60_000))
    expect(again).toBeDisabled()
    await act(() => vi.advanceTimersByTimeAsync(60_000))
    expect(again).toBeEnabled()
  })
})

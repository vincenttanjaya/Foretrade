import { act, screen, waitFor, within } from '@testing-library/react'
import { beforeEach, describe, expect, it, onTestFinished, vi } from 'vitest'
import { api, ApiError } from '../../lib/api'
import type { Candle } from '../../lib/types'
import { charts, LineStyle } from '../../test/chart'
import * as fx from '../../test/fixtures'
import { creditsUsed, currentPath, FakeWebSocket, renderPage, stubSession } from '../../test/utils'
import { TradePage } from './TradePage'

const T0 = 1_759_363_200
// The last close matches the BTC fixture price, which the chart reports as the live price.
const candles: Candle[] = Array.from({ length: 60 }, (_, i) => ({ t: T0 + i * 3600, o: 84000 + i, h: 84300 + i, l: 83900 + i, c: i === 59 ? 84213.2 : 84050 + i, v: 10 + i }))
const lastT = candles.at(-1)!.t

const open = (symbol = 'BTC') => renderPage(<TradePage />, { route: `/trade/${symbol}`, path: '/trade/:symbol' })
const panel = () => screen.getByRole('group', { name: 'Order panel' })
const candleSeries = () => charts.at(-1)!.series.find((s) => s.type === 'Candlestick')!
const hrefs = (el: HTMLElement) => within(el).queryAllByRole('link').map((l) => l.getAttribute('href'))
/** Below xl: open the market picker and pick `symbol` from its list. */
async function pickMarket(user: ReturnType<typeof open>['user'], symbol: string) {
  await user.click(screen.getByRole('button', { name: /^Switch market: / }))
  await user.click(within(screen.getByRole('dialog', { name: 'Switch market' })).getByRole('link', { name: new RegExp(`^${symbol}`) }))
}

beforeEach(() => {
  charts.length = 0
  stubSession()
  vi.spyOn(api, 'candles').mockResolvedValue({ candles })
  vi.spyOn(api, 'trades').mockResolvedValue({ trades: [fx.trade(), fx.trade({ id: 't2', side: 'sell', source: 'take_profit', realizedPnl: 120, note: 'auto' })] })
})

describe('TradePage · market header and list', () => {
  it('shows the asset, its 24h stats and the market list', async () => {
    const { user } = open()
    expect(await screen.findByRole('heading', { name: 'Bitcoin' })).toBeInTheDocument()
    expect(screen.getByText('BTC/USDT')).toBeInTheDocument()
    expect(screen.getByText('24h volume').nextSibling).toHaveTextContent('$1.2B')
    const markets = screen.getByRole('complementary', { name: 'Markets' })
    expect(within(markets).getByRole('link', { name: /^BTCBitcoin/ })).toHaveAttribute('aria-current', 'page')

    await user.type(within(markets).getByRole('searchbox', { name: 'Search markets' }), 'sol')
    expect(within(markets).getAllByRole('link')).toHaveLength(1)
    await user.keyboard('{Enter}')
    expect(currentPath()).toBe('/trade/SOL')
    expect(await screen.findByRole('heading', { name: 'Solana' })).toBeInTheDocument()

    await user.clear(within(markets).getByRole('searchbox'))
    await user.type(within(markets).getByRole('searchbox'), 'zzz')
    expect(within(markets).getByText('No market matches “zzz”.')).toBeInTheDocument()
  })

  it('caps the desktop list to the viewport and scrolls it inside, under the search and sticky group labels', async () => {
    open()
    await screen.findByRole('heading', { name: 'Bitcoin' })
    const markets = screen.getByRole('complementary', { name: 'Markets' })
    expect(markets).toHaveClass('hidden', 'xl:block', 'xl:sticky', 'xl:top-5')
    const list = within(markets).getByRole('list', { name: 'Market list' })
    expect(list).toHaveClass('overflow-y-auto', 'min-h-0', 'flex-1')
    expect(list.parentElement).toHaveClass('h-[min(100dvh_-_6rem,56rem)]', 'flex', 'flex-col', 'overflow-hidden') // the capped card
    expect(within(markets).getByRole('searchbox', { name: 'Search markets' }).closest('ul')).toBeNull() // outside the scroll area
    expect((await within(markets).findByText('Crypto')).parentElement).toHaveClass('sticky', 'top-0') // the group labels come with api.assets
    expect(hrefs(within(markets).getByRole('list', { name: 'Crypto' }))).toEqual(['/trade/BTC', '/trade/ETH', '/trade/SOL', '/trade/PAXG'])
  })

  it('searches the list by symbol or name, case-insensitively, with an empty state', async () => {
    const { user } = open()
    await screen.findByRole('heading', { name: 'Bitcoin' })
    const markets = screen.getByRole('complementary', { name: 'Markets' })
    const search = within(markets).getByRole('searchbox', { name: 'Search markets' })
    for (const [q, found] of [['ETH', ['/trade/ETH']], ['gold', ['/trade/PAXG']], ['  BiTcOiN ', ['/trade/BTC']], ['', ['/trade/BTC', '/trade/ETH', '/trade/SOL', '/trade/PAXG']]] as const) {
      await user.clear(search)
      if (q) await user.type(search, q)
      expect(hrefs(markets), q).toEqual(found)
    }
    await user.type(search, 'nope')
    expect(within(markets).getByText('No market matches “nope”.')).toBeInTheDocument()
    expect(within(markets).queryByText('Crypto')).toBeNull() // no empty group labels
    await user.keyboard('{Enter}') // nothing to open
    expect(currentPath()).toBe('/trade/BTC')
  })

  it('scrolls only the list to bring the selected market into view, on load and on navigation', async () => {
    let rowTop = 400 // px below the list's top edge
    vi.spyOn(Element.prototype, 'getBoundingClientRect').mockImplementation(function (this: Element) {
      return { top: this.getAttribute('aria-current') === 'page' ? rowTop : 0 } as DOMRect
    })
    vi.spyOn(Element.prototype, 'clientHeight', 'get').mockReturnValue(300)
    vi.spyOn(HTMLElement.prototype, 'offsetHeight', 'get').mockReturnValue(50)
    const scroll = vi.spyOn(Element.prototype, 'scrollTo')
    const { user } = open()
    await screen.findByRole('heading', { name: 'Bitcoin' })
    const list = within(screen.getByRole('complementary', { name: 'Markets' })).getByRole('list', { name: 'Market list' })
    await waitFor(() => expect(scroll).toHaveBeenCalledWith({ top: 275 })) // 400 − (300 − 50) / 2: centred
    expect(scroll.mock.contexts).toEqual([list])

    rowTop = 100 // already in full view: left alone
    await user.click(within(list).getByRole('link', { name: /^ETH/ }))
    expect(within(list).getByRole('link', { name: /^ETH/ })).toHaveAttribute('aria-current', 'page')
    expect(scroll).toHaveBeenCalledTimes(1)

    rowTop = 10 // under the sticky group label
    await user.click(within(list).getByRole('link', { name: /^SOL/ }))
    await waitFor(() => expect(scroll).toHaveBeenCalledTimes(2))
    expect(scroll).toHaveBeenLastCalledWith({ top: -115 })
    expect(scroll.mock.contexts).toEqual([list, list])
  })

  it('switches markets with a searchable, height-capped picker below xl', async () => {
    const reveal = vi.fn()
    Object.defineProperty(Element.prototype, 'scrollIntoView', { value: reveal, configurable: true })
    onTestFinished(() => void delete (Element.prototype as { scrollIntoView?: unknown }).scrollIntoView)
    const { user } = open()
    await screen.findByRole('heading', { name: 'Bitcoin' })
    const toggle = screen.getByRole('button', { name: 'Switch market: BTC Bitcoin' })
    expect(toggle.parentElement).toHaveClass('w-full', 'xl:hidden')
    expect(toggle).toHaveAttribute('aria-haspopup', 'dialog')
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    expect(screen.queryByRole('dialog')).toBeNull()

    await user.click(toggle)
    const dialog = screen.getByRole('dialog', { name: 'Switch market' })
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    expect(toggle).toHaveAttribute('aria-controls', dialog.id)
    expect(dialog).toHaveClass('absolute', 'inset-x-0', 'sm:w-80') // the button's width on phones: no sideways scroll at 375 px
    expect(reveal.mock.contexts).toEqual([dialog]) // scrolled into view whole on a short screen
    expect(reveal).toHaveBeenCalledWith({ block: 'nearest' })
    const search = within(dialog).getByRole('searchbox', { name: 'Search markets' })
    expect(search).toHaveFocus()
    expect(within(dialog).getByRole('list', { name: 'Market list' })).toHaveClass('overflow-y-auto', 'max-h-[min(22rem,50dvh)]')
    expect(within(dialog).getByRole('link', { name: /^BTCBitcoin/ })).toHaveAttribute('aria-current', 'page')
    await user.type(search, 'sol')
    expect(hrefs(dialog)).toEqual(['/trade/SOL'])
    await user.clear(search)
    await user.type(search, 'zzz')
    expect(within(dialog).getByText('No market matches “zzz”.')).toBeInTheDocument()

    await user.keyboard('{Escape}')
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(toggle).toHaveFocus()
    await user.click(toggle)
    expect(within(screen.getByRole('dialog')).getByRole('searchbox')).toHaveValue('') // a fresh search each time
    await user.click(screen.getByRole('heading', { name: 'Bitcoin' })) // a tap outside
    expect(screen.queryByRole('dialog')).toBeNull()

    await pickMarket(user, 'ETH')
    expect(currentPath()).toBe('/trade/ETH')
    expect(await screen.findByRole('heading', { name: 'Ethereum' })).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Switch market: ETH Ethereum' }))
    await user.keyboard('gold{Enter}')
    expect(currentPath()).toBe('/trade/PAXG')
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('reads the forecast desk under the price, once per symbol, or invites a question', async () => {
    const desk = vi.spyOn(api, 'getDesk').mockImplementation(async (symbol) => ({ desk: symbol === 'SOL' ? fx.deskRead() : null }))
    const { user } = open('SOL')
    const chip = await screen.findByRole('link', { name: /Forecast desk/ })
    expect(chip).toHaveTextContent('Forecast desk · Yes 28% (leans No) · SOL above $125 on Oct 9 →')
    expect(chip).toHaveAttribute('href', '/forecasts/f-sol')
    await pickMarket(user, 'ETH')
    expect(await screen.findByRole('link', { name: 'Ask the desk about ETH →' })).toHaveAttribute('href', '/forecasts/new')
    expect(desk.mock.calls.map((c) => c[0])).toEqual(['SOL', 'ETH'])
  })

  it('normalizes the symbol and sends unknown markets to BTC', async () => {
    const { unmount } = open('eth')
    await waitFor(() => expect(currentPath()).toBe('/trade/ETH'))
    unmount()
    open('NOPE')
    await waitFor(() => expect(currentPath()).toBe('/trade/BTC'))
  })
})

describe('TradePage · alerts', () => {
  it('sets a price alert in a dialog without leaving the page', async () => {
    const create = vi.spyOn(api, 'createAlert').mockResolvedValue({ alert: {} as never })
    const { user } = renderPage(<TradePage />, { route: '/trade/SOL', path: '/trade/:symbol' })
    await screen.findByRole('heading', { name: 'Solana' })
    await user.click(screen.getByRole('button', { name: 'Set alert' }))
    const dialog = screen.getByRole('dialog', { name: 'Alert me on SOL' })
    const price = within(dialog).getByLabelText('Price (USD)')
    await user.clear(price)
    await user.type(price, '100')
    await user.click(within(dialog).getByRole('button', { name: 'Set alert' }))
    expect(create).toHaveBeenCalledWith({ symbol: 'SOL', op: 'below', price: 100, note: undefined })
    expect(await screen.findByText('Alert set · SOL below $100.00')).toBeInTheDocument()
    expect(currentPath()).toBe('/trade/SOL')
  })
})

describe('TradePage · chart', () => {
  it('switches to weekly and monthly candles with the matching live stream', async () => {
    const { user } = open()
    await waitFor(() => expect(FakeWebSocket.instances).toHaveLength(1))
    const controls = screen.getByRole('group', { name: 'Chart interval' })
    for (const [label, interval] of [['1W', '1w'], ['1M', '1M']] as const) {
      const previous = FakeWebSocket.instances.at(-1)!
      await user.click(within(controls).getByRole('button', { name: label }))
      await waitFor(() => expect(api.candles).toHaveBeenLastCalledWith('BTC', interval, 500, expect.any(AbortSignal)))
      await waitFor(() => expect(FakeWebSocket.instances.at(-1)!.url).toBe(`wss://data-stream.binance.vision/ws/btcusdt@kline_${interval}`))
      expect(previous.closed).toBe(true)
      expect(within(controls).getByRole('button', { name: label })).toHaveAttribute('aria-pressed', 'true')
    }
  })

  it('loads 500 candles, then follows the live kline stream', async () => {
    const { user } = open()
    await waitFor(() => expect(candleSeries().setData).toHaveBeenLastCalledWith(expect.arrayContaining([{ time: lastT, open: 84059, high: 84359, low: 83959, close: 84213.2 }])))
    expect(api.candles).toHaveBeenCalledWith('BTC', '1h', 500, expect.any(AbortSignal))
    await waitFor(() => expect(FakeWebSocket.instances).toHaveLength(1))
    const ws = FakeWebSocket.instances[0]
    expect(ws.url).toBe('wss://data-stream.binance.vision/ws/btcusdt@kline_1h')

    act(() => ws.onmessage!({ data: JSON.stringify({ k: { t: lastT * 1000, o: '84059', h: '85100', l: '83959', c: '85000', v: '70' } }) }))
    expect(candleSeries().update).toHaveBeenCalledWith({ time: lastT, open: 84059, high: 85100, low: 83959, close: 85000 })
    expect(await screen.findByText('Live')).toBeInTheDocument()
    expect(screen.getAllByText('$85,000.00').length).toBeGreaterThan(0) // header price follows the stream
    act(() => ws.onmessage!({ data: 'not json' }))

    act(() => ws.onclose!())
    expect(screen.getByText('Delayed · 30s')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '4h' }))
    await waitFor(() => expect(api.candles).toHaveBeenLastCalledWith('BTC', '4h', 500, expect.any(AbortSignal)))
    expect(ws.closed).toBe(true)
  })

  it('toggles the SMA overlays', async () => {
    const { user } = open()
    const sma20 = screen.getByRole('button', { name: 'SMA 20' })
    expect(sma20).toHaveAttribute('aria-pressed', 'false')
    await user.click(sma20)
    expect(sma20).toHaveAttribute('aria-pressed', 'true')
    await waitFor(() => expect(charts.at(-1)!.series.filter((s) => s.type === 'Line')[0].applyOptions).toHaveBeenLastCalledWith({ visible: true }))
    await screen.findByRole('heading', { name: 'Bitcoin' })
  })

  it('formats axis labels, keeps price lines in range and shows a hover legend', async () => {
    open()
    await waitFor(() => expect(candleSeries().setData).toHaveBeenCalled())
    const chart = charts.at(-1)!
    const ts = chart.options.timeScale as { tickMarkFormatter: (t: number, type: number) => string }
    expect(ts.tickMarkFormatter(T0, 0)).toBe('2025')
    expect(ts.tickMarkFormatter(T0, 1)).toBe('Oct')
    expect(ts.tickMarkFormatter(T0, 2)).toBe('1')
    expect(ts.tickMarkFormatter(T0, 3)).toBe('17:00')
    expect((chart.options.localization as { timeFormatter: (t: number) => string }).timeFormatter(T0)).toBe('Oct 1, 2025, 17:00')

    const provider = candleSeries().options.autoscaleInfoProvider as (base: () => unknown) => unknown
    const base = { priceRange: { minValue: 100, maxValue: 200 } }
    expect(provider(() => base)).toBe(base) // no levels drawn yet

    const onMove = chart.subscribeCrosshairMove.mock.calls[0][0] as (p: unknown) => void
    const volume = chart.series.find((s) => s.type === 'Histogram')
    act(() => onMove({ time: T0, seriesData: new Map<unknown, unknown>([[candleSeries(), { open: 100, high: 110, low: 90, close: 105 }], [volume, { value: 1234567 }]]) }))
    expect(screen.getByText('+5.00%')).toBeInTheDocument()
    expect(screen.getByText('1.2M')).toBeInTheDocument()
    act(() => onMove({ seriesData: new Map() }))
    expect(screen.queryByText('+5.00%')).toBeNull()
  })

  it('shows a retry when candles fail to load', async () => {
    vi.mocked(api.candles).mockRejectedValueOnce(new ApiError(502, 'upstream', 'Binance is unavailable'))
    const { user } = open()
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent("The chart didn't load.Binance is unavailable")
    await user.click(within(alert).getByRole('button', { name: 'Try again' }))
    await waitFor(() => expect(screen.queryByRole('alert')).toBeNull())
    expect(api.candles).toHaveBeenCalledTimes(2)
  })
})

describe('TradePage · copilot', () => {
  const planRes = (plan = fx.plan()) => ({
    plan,
    analysis: fx.analysis,
    outlook: fx.outlook,
    headlines: [fx.news(), fx.news({ id: 'bad', url: 'javascript:alert(1)', title: 'Evil' })],
    credits: { ...fx.credits, used: 19 },
  })

  it('asks for a plan, draws it, then pre-fills and submits the ticket', async () => {
    const ask = vi.spyOn(api, 'copilotPlan').mockResolvedValue(planRes())
    const order = vi.spyOn(api, 'placeOrder').mockResolvedValue({
      trade: fx.trade({ symbol: 'BTC', qty: 0.0297, price: 84213.2, notional: 2500 }),
      portfolio: fx.portfolio,
    })
    const { user } = open()
    await screen.findByRole('heading', { name: 'Bitcoin' })
    await screen.findByRole('link', { name: 'ETH' }) // portfolio loaded, so the ticket can submit
    expect(screen.getByText('A plan in one tap.')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /Ask Long/ }))

    expect(ask).toHaveBeenCalledWith({ symbol: 'BTC', style: 'day', bias: 'long' })
    const card = (await screen.findByText('Confidence')).closest('article')!
    expect(creditsUsed()).toBe('19')
    expect(within(card).getByRole('meter', { name: 'Confidence' })).toHaveAttribute('aria-valuenow', '64')
    expect(card).toHaveTextContent('Long')
    expect(card).toHaveTextContent('Entry$84,213.20market')
    expect(card).toHaveTextContent('Stop$82,850.46-1.62%')
    expect(card).toHaveTextContent('R:R 1 : 2.0')
    expect(within(card).getByText('Higher lows on the 1h')).toBeInTheDocument()
    expect(within(card).getByText('ETF inflows extend a third day')).toBeInTheDocument()
    expect(within(card).getByText('A 1h close below $82,800')).toBeInTheDocument()
    expect(within(card).getByRole('link', { name: /Bitcoin ETFs log a third day of inflows/ })).toHaveAttribute('href', fx.news().url)
    expect(within(card).queryByText('Evil')).toBeNull() // non-http headlines never render
    expect(within(card).getByText('$80.0k – $88.0k')).toBeInTheDocument()
    await waitFor(() => expect(candleSeries().createPriceLine.mock.calls.map(([o]) => o.title)).toEqual(['Entry', 'Stop', 'Target']))

    await user.click(within(card).getByRole('button', { name: 'Use this plan' }))
    expect(within(panel()).getByRole('button', { name: 'Manual' })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByText('Pre-filled from the Copilot plan')).toBeInTheDocument()
    expect(screen.getByLabelText('Amount (USD)')).toHaveValue(2500)
    expect(screen.getByLabelText('Stop loss')).toHaveValue(82850.46)
    expect(screen.getByLabelText('Take profit')).toHaveValue(86900.1)

    await user.click(screen.getByRole('button', { name: 'Confirm buy (paper)' }))
    expect(order).toHaveBeenCalledWith({
      symbol: 'BTC',
      side: 'buy',
      notional: 2500,
      stopLoss: 82850.46,
      takeProfit: 86900.1,
      source: 'copilot',
      note: 'Copilot day plan',
    })
    expect(await screen.findByText('Bought 0.0297 BTC at $84,213.20 (paper).')).toBeInTheDocument()
    expect(screen.getByLabelText('Amount (USD)')).toHaveValue(null) // ticket reset
    expect(screen.queryByText('Pre-filled from the Copilot plan')).toBeNull()
  })

  it('sends the chosen style and bias; a short plan pre-fills a sell', async () => {
    const ask = vi.spyOn(api, 'copilotPlan').mockResolvedValue(planRes(fx.plan({ direction: 'short', style: 'swing', stopLoss: 86000, takeProfit: 80000, suggestedNotional: 1e9 })))
    const { user } = open()
    await screen.findByRole('heading', { name: 'Bitcoin' })
    await user.click(screen.getByRole('button', { name: 'Swing' }))
    expect(screen.getByText('4h chart · 14-day outlook')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /Ask Short/ }))
    expect(ask).toHaveBeenCalledWith({ symbol: 'BTC', style: 'swing', bias: 'short' })
    await screen.findByRole('link', { name: 'ETH' }) // portfolio loaded: buying power caps the amount
    await user.click(await screen.findByRole('button', { name: 'Use this plan' }))
    expect(screen.getByRole('button', { name: /Sell/, pressed: true })).toBeInTheDocument()
    expect(screen.getByLabelText('Amount (USD)')).toHaveValue(Math.floor((fx.portfolio.buyingPower * 0.995) / 50) * 50) // capped at buying power, in $50s
  })

  it('a plan pre-fills a round amount at 1×, whatever leverage the ticket had', async () => {
    vi.spyOn(api, 'copilotPlan').mockResolvedValue(planRes(fx.plan({ suggestedNotional: 4999 })))
    const { user } = open()
    await screen.findByRole('link', { name: 'ETH' }) // portfolio loaded
    await user.click(within(panel()).getByRole('button', { name: 'Manual' }))
    await user.click(within(screen.getByRole('group', { name: 'Leverage' })).getByRole('button', { name: '5×' }))
    expect(screen.getByLabelText('Margin (USD)')).toBeInTheDocument()
    await user.click(within(panel()).getByRole('button', { name: /Copilot/ }))
    await user.click(screen.getByRole('button', { name: /Ask Long/ }))
    await user.click(await screen.findByRole('button', { name: 'Use this plan' }))
    expect(screen.getByLabelText('Amount (USD)')).toHaveValue(5000) // $4,999 → $5,000
    expect(within(screen.getByRole('group', { name: 'Leverage' })).getByRole('button', { name: '1×' })).toHaveAttribute('aria-pressed', 'true')
  })

  it('a plan entry 1.5% below the price pre-fills a limit order at the entry; a near-price plan goes back to market', async () => {
    // fx.analysis.price is 84,213.20: 1.5% below is 82,950.00.
    const ask = vi.spyOn(api, 'copilotPlan').mockResolvedValue(planRes(fx.plan({ entry: 82950, stopLoss: 81623.004, takeProfit: 85600 })))
    vi.spyOn(api, 'alerts').mockResolvedValue({ alerts: [] })
    const create = vi.spyOn(api, 'createAlert').mockResolvedValue({ alert: fx.alert({ symbol: 'BTC', price: 82950 }) })
    const { user } = open()
    await screen.findByRole('link', { name: 'ETH' }) // portfolio loaded, so the ticket can submit
    await user.click(screen.getByRole('button', { name: /Ask Long/ }))
    await user.click(await screen.findByRole('button', { name: 'Place limit at $82,950.00' }))

    expect(within(panel()).getByRole('button', { name: 'Manual' })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByRole('button', { name: 'Limit' })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByLabelText('Trigger price')).toHaveValue(82950)
    expect(screen.getByLabelText('Amount (USD)')).toHaveValue(2500)
    expect(screen.getByLabelText('Stop loss')).toHaveValue(81623)
    expect(screen.getByLabelText('Take profit')).toHaveValue(85600)
    await user.click(screen.getByRole('button', { name: 'Place limit buy · $2,500 at $82,950.00' }))
    expect(create).toHaveBeenCalledWith({
      symbol: 'BTC',
      op: 'below',
      price: 82950,
      action: 'buy',
      notional: 2500,
      stopLoss: 81623,
      takeProfit: 85600,
      note: 'Copilot day plan · limit',
      source: 'ticket',
    })

    ask.mockResolvedValue(planRes(fx.plan({ generatedAt: '2026-10-02T08:31:00Z' }))) // entry at the price
    await user.click(within(panel()).getByRole('button', { name: /Copilot/ }))
    await user.click(screen.getByRole('button', { name: /Ask Long/ }))
    await user.click(await screen.findByRole('button', { name: 'Use this plan' }))
    expect(screen.getByRole('button', { name: 'Market' })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.queryByLabelText('Trigger price')).toBeNull()
    expect(screen.getByRole('button', { name: 'Confirm buy (paper)' })).toBeInTheDocument()
  })

  it('a neutral plan cannot be used', async () => {
    vi.spyOn(api, 'copilotPlan').mockResolvedValue(planRes(fx.plan({ direction: 'neutral', catalysts: [] })))
    const { user } = open()
    await user.click(screen.getByRole('button', { name: /Let AI decide/ }))
    expect(await screen.findByRole('button', { name: 'No trade · the copilot says wait' })).toBeDisabled()
    // Its setup still shows on the chart, dotted so it reads as a wait.
    await waitFor(() => expect(candleSeries().createPriceLine.mock.calls.map(([o]) => o.title)).toEqual(['Entry (wait)', 'Stop', 'Target']))
    expect(candleSeries().createPriceLine.mock.calls.every(([o]) => o.lineStyle === LineStyle.Dotted)).toBe(true)
  })

  it('shows the quota message and re-reads credits', async () => {
    vi.spyOn(api, 'copilotPlan').mockRejectedValue(new ApiError(429, 'ai_quota', 'quota'))
    const { user } = open()
    await screen.findByRole('heading', { name: 'Bitcoin' })
    const calls = vi.mocked(api.me).mock.calls.length
    await user.click(screen.getByRole('button', { name: /Ask Long/ }))
    expect(await screen.findByText('Daily AI credits used up. They reset at 00:00 UTC.')).toBeInTheDocument()
    expect(vi.mocked(api.me).mock.calls.length).toBe(calls + 1)
  })

  it('links to the manual ticket', async () => {
    const { user } = open()
    await user.click(screen.getByRole('button', { name: 'Open the manual ticket' }))
    expect(within(panel()).getByRole('button', { name: 'Manual' })).toHaveAttribute('aria-pressed', 'true')
    await screen.findByRole('heading', { name: 'Bitcoin' })
  })
})

describe('TradePage · manual ticket', () => {
  async function manual(symbol = 'BTC') {
    const r = open(symbol)
    await r.user.click(within(panel()).getByRole('button', { name: 'Manual' }))
    await screen.findByText('$194,388.00') // buying power loaded
    await screen.findByText(/· live$/) // and a price
    return r
  }

  it('validates the amount and the stop/target sides', async () => {
    const order = vi.spyOn(api, 'placeOrder')
    const { user } = await manual()
    const confirm = screen.getByRole('button', { name: 'Confirm buy (paper)' })
    expect(confirm).toBeDisabled()
    expect(screen.getByText(/^BTC at \$84,213\.20 · live$/)).toBeInTheDocument()

    await user.type(screen.getByLabelText('Amount (USD)'), '5')
    expect(screen.getByText('The minimum order is $10.')).toBeInTheDocument()
    expect(screen.getByLabelText('Amount (USD)')).toHaveAttribute('aria-invalid', 'true')
    expect(confirm).toBeDisabled()

    await user.type(screen.getByLabelText('Amount (USD)'), '00') // 500
    expect(screen.getByText('0.005937 BTC')).toBeInTheDocument()
    expect(screen.getByText('$0.50')).toBeInTheDocument() // 0.1% fee
    expect(confirm).toBeEnabled()

    await user.type(screen.getByLabelText('Stop loss'), '90000')
    expect(screen.getByText('For a long, set this below the price.')).toBeInTheDocument()
    expect(confirm).toBeDisabled()
    await user.clear(screen.getByLabelText('Stop loss'))

    await user.click(screen.getByRole('button', { name: /Sell/ }))
    await user.type(screen.getByLabelText('Take profit'), '90000')
    expect(screen.getByText('For a short, set this below the price.')).toBeInTheDocument()
    expect(order).not.toHaveBeenCalled()
  })

  it('fills from the buying-power chips', async () => {
    const { user } = await manual()
    await user.click(screen.getByRole('button', { name: '25%' }))
    expect(screen.getByLabelText('Amount (USD)')).toHaveValue(48597)
    await user.click(screen.getByRole('button', { name: 'Max' }))
    expect(screen.getByLabelText('Amount (USD)')).toHaveValue(193416.06) // keeps fee headroom
    await user.type(screen.getByLabelText('Amount (USD)'), '9')
    expect(screen.getByText(/Above your buying power/)).toBeInTheDocument()
  })

  it('shows the position before and after the order', async () => {
    vi.mocked(api.candles).mockResolvedValue({ candles: [{ t: T0, o: 3000, h: 3000, l: 3000, c: 3000, v: 1 }] }) // ETH at the fixture price
    const { user } = await manual('ETH')
    expect(screen.getByText('Position now').nextSibling).toHaveTextContent('2 ETH long')
    await user.click(screen.getByRole('button', { name: /Sell/ }))
    await user.type(screen.getByLabelText('Amount (USD)'), '6000')
    expect(screen.getByText('Position after').nextSibling).toHaveTextContent('Flat')
  })

  it('toasts a rejected order and refreshes the portfolio', async () => {
    vi.spyOn(api, 'placeOrder').mockRejectedValue(new ApiError(400, 'insufficient_buying_power', 'Not enough buying power'))
    const { user } = await manual()
    const before = vi.mocked(api.portfolio).mock.calls.length
    await user.type(screen.getByLabelText('Amount (USD)'), '100')
    await user.click(screen.getByRole('button', { name: 'Confirm buy (paper)' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Not enough buying power')
    expect(vi.mocked(api.portfolio).mock.calls.length).toBe(before + 1)
    expect(screen.getByLabelText('Amount (USD)')).toHaveValue(100) // kept for another try
  })

  // SOL at $100 on the chart: 5× = 20% to liquidation, less the 0.5% maintenance margin.
  const at100 = () => vi.mocked(api.candles).mockResolvedValue({ candles: [{ t: T0, o: 100, h: 100, l: 100, c: 100, v: 1 }] })
  const row = (label: string) => screen.getByText(label).nextSibling

  it('a 5× long sizes margin × 5 and stops at the est. liquidation unless the stop is tighter', async () => {
    at100()
    const order = vi.spyOn(api, 'placeOrder').mockResolvedValue({ trade: fx.trade({ symbol: 'SOL', qty: 50, price: 100, notional: 5000 }), portfolio: fx.portfolio })
    const { user } = await manual('SOL')
    expect(screen.queryByText('Est. liquidation')).toBeNull() // 1×: no margin, no liquidation
    await user.click(within(screen.getByRole('group', { name: 'Leverage' })).getByRole('button', { name: '5×' }))
    await user.click(screen.getByRole('button', { name: '25%' }))
    expect(screen.getByLabelText('Margin (USD)')).toHaveValue(9719.4) // 25% of buying power ÷ 5
    await user.clear(screen.getByLabelText('Margin (USD)'))
    await user.type(screen.getByLabelText('Margin (USD)'), '1000')
    expect(row('Position size')).toHaveTextContent('$5,000 (margin $1,000 × 5×)')
    expect(row('Est. liquidation')).toHaveTextContent('$80.50 (-19.5%)')
    expect(row('Est. quantity')).toHaveTextContent('50 SOL')
    expect(screen.getByText('Empty or past liquidation → the stop is set at the est. liquidation.')).toBeInTheDocument()
    expect(screen.getByText(/^Simulated isolated margin: the position closes at the est\. liquidation price/)).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Confirm buy (paper)' }))
    expect(order).toHaveBeenLastCalledWith({ symbol: 'SOL', side: 'buy', notional: 5000, stopLoss: 80.5, takeProfit: undefined, source: 'manual', note: '5× leverage (paper)' })
    expect(await screen.findByText('Bought 50 SOL at $100.00 · 5× · stop at liquidation $80.50 (paper).')).toBeInTheDocument()
    expect(within(screen.getByRole('group', { name: 'Leverage' })).getByRole('button', { name: '5×' })).toHaveAttribute('aria-pressed', 'true') // kept

    // A stop past the liquidation price is replaced by it; a tighter one is kept.
    for (const [typed, sent] of [['70', 80.5], ['90', 90]] as const) {
      await user.type(screen.getByLabelText('Margin (USD)'), '100')
      await user.type(screen.getByLabelText('Stop loss'), typed)
      await user.click(screen.getByRole('button', { name: 'Confirm buy (paper)' }))
      await waitFor(() => expect(order).toHaveBeenLastCalledWith(expect.objectContaining({ notional: 500, stopLoss: sent })))
      await waitFor(() => expect(screen.getByLabelText('Margin (USD)')).toHaveValue(null))
    }
  })

  it('a short liquidates above the price; 1× and limit orders have no margin', async () => {
    at100()
    const { user } = await manual('SOL')
    await user.click(screen.getByRole('button', { name: /Sell/ }))
    await user.click(within(screen.getByRole('group', { name: 'Leverage' })).getByRole('button', { name: '10×' }))
    await user.type(screen.getByLabelText('Margin (USD)'), '200')
    expect(row('Position size')).toHaveTextContent('$2,000 (margin $200 × 10×)')
    expect(row('Est. liquidation')).toHaveTextContent('$109.50 (+9.5%)')

    await user.click(within(screen.getByRole('group', { name: 'Leverage' })).getByRole('button', { name: '1×' }))
    expect(screen.getByLabelText('Amount (USD)')).toHaveValue(200)
    expect(screen.queryByText('Est. liquidation')).toBeNull()
    expect(screen.queryByText('Position size')).toBeNull()

    await user.click(within(screen.getByRole('group', { name: 'Leverage' })).getByRole('button', { name: '3×' }))
    await user.click(screen.getByRole('button', { name: 'Limit' }))
    expect(screen.queryByRole('group', { name: 'Leverage' })).toBeNull() // limit orders stay 1×
    expect(screen.getByLabelText('Amount (USD)')).toHaveValue(200)
    expect(screen.queryByText('Est. liquidation')).toBeNull()
  })

  it('a leveraged sell that only trims a long sends no liquidation stop', async () => {
    const order = vi.spyOn(api, 'placeOrder').mockResolvedValue({ trade: fx.trade({ symbol: 'ETH', side: 'sell' }), portfolio: fx.portfolio })
    const { user } = await manual('ETH') // 2 ETH long
    await user.click(screen.getByRole('button', { name: /Sell/ }))
    await user.click(within(screen.getByRole('group', { name: 'Leverage' })).getByRole('button', { name: '2×' }))
    await user.type(screen.getByLabelText('Margin (USD)'), '100')
    expect(screen.queryByText('Est. liquidation')).toBeNull()
    expect(screen.queryByText(/stop is set at the est\. liquidation/)).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Confirm sell (paper)' }))
    expect(order).toHaveBeenLastCalledWith(expect.objectContaining({ side: 'sell', notional: 200, stopLoss: undefined }))
  })

  it('can drop the copilot pre-fill to a manual order', async () => {
    vi.spyOn(api, 'copilotPlan').mockResolvedValue({ plan: fx.plan(), analysis: fx.analysis, outlook: fx.outlook, headlines: [], credits: fx.credits })
    const { user } = open()
    await user.click(screen.getByRole('button', { name: /Ask Long/ }))
    await user.click(await screen.findByRole('button', { name: 'Use this plan' }))
    await user.click(screen.getByRole('button', { name: 'Treat as a manual order' }))
    expect(screen.queryByText('Pre-filled from the Copilot plan')).toBeNull()
  })
})

describe('TradePage · mobile action bar', () => {
  it('sits above the tab bar below md only and jumps to the Copilot and the ticket', async () => {
    const scroll = vi.fn()
    Object.defineProperty(Element.prototype, 'scrollIntoView', { value: scroll, configurable: true })
    try {
      const { user } = open()
      const bar = screen.getByRole('navigation', { name: 'Trade actions' })
      expect(bar).toHaveClass('fixed', 'md:hidden', 'print:hidden')
      expect(bar.className).toContain('env(safe-area-inset-bottom)')
      await user.click(within(bar).getByRole('button', { name: 'Trade' }))
      expect(within(panel()).getByRole('button', { name: 'Manual' })).toHaveAttribute('aria-pressed', 'true')
      expect(scroll.mock.contexts.at(-1)).toHaveAttribute('id', 'order-ticket')
      expect(scroll).toHaveBeenLastCalledWith({ behavior: 'smooth', block: 'start' })
      await user.click(within(bar).getByRole('button', { name: 'Copilot' }))
      expect(within(panel()).getByRole('button', { name: /Copilot/ })).toHaveAttribute('aria-pressed', 'true')
      expect(scroll.mock.contexts.at(-1)).toHaveAttribute('id', 'copilot-panel')
      await screen.findByRole('heading', { name: 'Bitcoin' })
    } finally {
      delete (Element.prototype as { scrollIntoView?: unknown }).scrollIntoView
    }
  })
})

describe('TradePage · positions, history and news', () => {
  it('closes a position from the table', async () => {
    const close = vi.spyOn(api, 'closePosition').mockResolvedValue({
      trade: fx.trade({ side: 'sell', qty: 2, price: 3000, realizedPnl: 194 }),
      portfolio: fx.emptyPortfolio,
    })
    const { user } = open('ETH')
    const row = (await screen.findByRole('link', { name: 'ETH' })).closest('tr')!
    expect(row).toHaveTextContent('Long')
    expect(screen.getByRole('button', { name: 'Positions (1)' })).toBeInTheDocument()
    await waitFor(() => expect(candleSeries().createPriceLine.mock.calls.map(([o]) => o.title)).toContain('Avg long'))
    await user.click(within(row).getByRole('button', { name: 'Close ETH position at market' }))
    expect(close).toHaveBeenCalledWith('ETH')
    expect(await screen.findByText('Closed ETH: sold 2 at $3,000.00, realized +$194.00.')).toBeInTheDocument()
    expect(await screen.findByText('No open positions')).toBeInTheDocument()
  })

  it('opens a market by clicking its position row', async () => {
    const { user } = open()
    const row = (await screen.findByRole('link', { name: 'ETH' })).closest('tr')!
    await user.click(row.querySelector('td:nth-child(2)')!)
    expect(currentPath()).toBe('/trade/ETH')
  })

  it('toasts when a close fails', async () => {
    vi.spyOn(api, 'closePosition').mockRejectedValue(new ApiError(404, 'not_found', 'No open position'))
    const { user } = open()
    await user.click(await screen.findByRole('button', { name: 'Close ETH position at market' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('No open position')
  })

  it('lists the trade history with sources', async () => {
    const { user } = open()
    await user.click(screen.getByRole('button', { name: 'History' }))
    await waitFor(() => expect(within(screen.getByRole('table')).getAllByRole('row')).toHaveLength(3)) // header + 2 fills, not the skeleton
    const rows = within(screen.getByRole('table')).getAllByRole('row').slice(1)
    expect(rows).toHaveLength(2)
    expect(rows[0]).toHaveTextContent('Buy')
    expect(rows[0]).toHaveTextContent('Manual')
    expect(rows[1]).toHaveTextContent('Take profit')
    expect(rows[1]).toHaveTextContent('auto')
  })

  it('retries a failed history load', async () => {
    vi.mocked(api.trades).mockRejectedValueOnce(new ApiError(500, 'internal', 'Could not load your trades.'))
    const { user } = open()
    await user.click(screen.getByRole('button', { name: 'History' }))
    const alert = await screen.findByRole('alert')
    await user.click(within(alert).getByRole('button', { name: 'Try again' }))
    expect(await screen.findByRole('table')).toBeInTheDocument()
  })

  it('shows symbol headlines with the brief sentiment', async () => {
    const news = vi.spyOn(api, 'news').mockResolvedValue({ news: [fx.news(), fx.news({ id: 'x', url: 'ftp://nope', title: 'Bad link' })] })
    vi.spyOn(api, 'brief').mockResolvedValue({ brief: fx.brief })
    const { user } = open()
    await user.click(screen.getByRole('button', { name: 'News' }))
    const list = await screen.findByRole('list', { name: 'BTC news' })
    expect(news).toHaveBeenCalledWith({ symbol: 'BTC', limit: 20 }, expect.any(AbortSignal))
    await waitFor(() => expect(within(list).getAllByRole('link')).toHaveLength(1))
    expect(within(list).getByRole('link')).toHaveAttribute('href', fx.news().url)
    expect(within(list).getByRole('link')).toHaveAttribute('rel', 'noopener noreferrer')
    expect(await screen.findByText('BTC sentiment')).toBeInTheDocument()
    expect(screen.getByTitle('Score +0.40 on a −1 to +1 scale')).toHaveTextContent('Bullish+0.40')
    expect(screen.getByText('ETF inflows')).toBeInTheDocument()
  })

  it('has an empty state and a retry for the news tab', async () => {
    const news = vi.spyOn(api, 'news').mockRejectedValueOnce(new ApiError(502, 'upstream', 'Search is unavailable'))
    vi.spyOn(api, 'brief').mockRejectedValue(new ApiError(502, 'upstream', 'x'))
    const { user } = open()
    await user.click(screen.getByRole('button', { name: 'News' }))
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('Search is unavailable')
    news.mockResolvedValueOnce({ news: [] })
    await user.click(within(alert).getByRole('button', { name: 'Try again' }))
    expect(await screen.findByText('No BTC headlines yet')).toBeInTheDocument()
  })
})

describe('TradePage · perps row', () => {
  it('shows Hyperliquid funding, open interest and premium under the header', async () => {
    const perps = vi.spyOn(api, 'derivatives').mockResolvedValue({ derivatives: fx.derivatives() })
    open()
    const row = await screen.findByRole('region', { name: 'Perpetual futures' })
    expect(perps).toHaveBeenCalledWith('BTC')
    expect(row).toHaveTextContent('Perps · Hyperliquid')
    expect(row).toHaveTextContent('Funding +0.0013%/h (11.4% ann.)')
    expect(row).toHaveTextContent('Open interest $1.24B')
    expect(row).toHaveTextContent('Premium +0.02%')
    expect(within(row).getByText('+0.0013%/h')).toHaveClass('text-up')
    expect(within(row).getByRole('img', { name: 'Perpetual-futures data from Hyperliquid, for context. Paper trades here are spot.' })).toHaveAttribute(
      'title',
      'Perpetual-futures data from Hyperliquid, for context. Paper trades here are spot.',
    )
  })

  it('colours negative funding and hides the row when there is no perp', async () => {
    vi.spyOn(api, 'derivatives').mockResolvedValueOnce({ derivatives: fx.derivatives({ symbol: 'SOL', fundingRatePct: -0.002, fundingAnnualizedPct: -17.52 }) })
    const { unmount } = open('SOL')
    const row = await screen.findByRole('region', { name: 'Perpetual futures' })
    expect(within(row).getByText('-0.0020%/h')).toHaveClass('text-down')
    expect(row).toHaveTextContent('(-17.5% ann.)')
    unmount()

    vi.mocked(api.derivatives).mockResolvedValueOnce({ derivatives: null })
    open('PAXG')
    expect(await screen.findByRole('heading', { name: 'PAX Gold' })).toBeInTheDocument()
    await waitFor(() => expect(api.derivatives).toHaveBeenLastCalledWith('PAXG'))
    expect(screen.queryByRole('region', { name: 'Perpetual futures' })).toBeNull()
  })
})

describe('TradePage · editing stops and targets', () => {
  const withLevels = (stopLoss: number | null, takeProfit: number | null) => ({
    portfolio: { ...fx.portfolio, revision: 2, positions: [{ ...fx.position, stopLoss, takeProfit }] },
  })

  it('adds a target inline with Enter, without opening the market', async () => {
    const patch = vi.spyOn(api, 'setProtection').mockResolvedValue(withLevels(2700, 3300))
    const { user } = open()
    expect(await screen.findByRole('button', { name: 'Edit stop for ETH' })).toHaveTextContent('$2,700.00')
    await user.click(screen.getByRole('button', { name: 'Edit target for ETH' }))
    expect(currentPath()).toBe('/trade/BTC') // the row's click-through stays shut
    const input = screen.getByRole('textbox', { name: 'Target for ETH' })
    expect(input).toHaveValue('') // no target yet: a blank field
    expect(input).toHaveFocus()
    expect(screen.queryByRole('button', { name: 'Clear target for ETH' })).toBeNull()
    await user.type(input, '3,300{Enter}')
    expect(patch).toHaveBeenCalledWith('ETH', { takeProfit: 3300 })
    expect(await screen.findByRole('button', { name: 'Edit target for ETH' })).toHaveTextContent('$3,300.00')
    expect(screen.getByRole('button', { name: 'Edit target for ETH' }).textContent).not.toBe('Add')
  })

  it('cancels with Esc, saves with ✓ and clears with ×', async () => {
    const patch = vi.spyOn(api, 'setProtection').mockResolvedValueOnce(withLevels(2800, null)).mockResolvedValueOnce(withLevels(null, null))
    const { user } = open()
    await user.click(await screen.findByRole('button', { name: 'Edit stop for ETH' }))
    expect(screen.getByRole('textbox', { name: 'Stop for ETH' })).toHaveValue('2700') // prefilled
    await user.keyboard('{Escape}')
    expect(screen.queryByRole('textbox')).toBeNull()
    expect(patch).not.toHaveBeenCalled()

    await user.click(screen.getByRole('button', { name: 'Edit stop for ETH' }))
    await user.clear(screen.getByRole('textbox', { name: 'Stop for ETH' }))
    await user.type(screen.getByRole('textbox', { name: 'Stop for ETH' }), '2800')
    await user.click(screen.getByRole('button', { name: 'Save stop for ETH' }))
    expect(patch).toHaveBeenLastCalledWith('ETH', { stopLoss: 2800 })
    expect(await screen.findByRole('button', { name: 'Edit stop for ETH' })).toHaveTextContent('$2,800.00')

    await user.click(screen.getByRole('button', { name: 'Edit stop for ETH' }))
    await user.click(screen.getByRole('button', { name: 'Clear stop for ETH' }))
    expect(patch).toHaveBeenLastCalledWith('ETH', { stopLoss: null })
    expect(await screen.findByRole('button', { name: 'Edit stop for ETH' })).toHaveTextContent('Add')
  })

  it("shows the API's message under the cell and keeps the editor open", async () => {
    vi.spyOn(api, 'setProtection').mockRejectedValue(new ApiError(400, 'bad_request', 'For a long, the target must be above the price ($3,000).'))
    const { user } = open()
    await user.click(await screen.findByRole('button', { name: 'Edit target for ETH' }))
    await user.type(screen.getByRole('textbox', { name: 'Target for ETH' }), '2900{Enter}')
    expect(await screen.findByRole('alert')).toHaveTextContent('For a long, the target must be above the price ($3,000).')
    expect(screen.getByRole('textbox', { name: 'Target for ETH' })).toHaveAttribute('aria-invalid', 'true')
    expect(screen.getByRole('textbox', { name: 'Target for ETH' })).toHaveFocus() // back in the field: fix it or press Esc

    await user.clear(screen.getByRole('textbox', { name: 'Target for ETH' }))
    await user.keyboard('{Enter}')
    expect(screen.getByRole('alert')).toHaveTextContent('Enter a price above $0.')
    expect(api.setProtection).toHaveBeenCalledTimes(1)
  })
})

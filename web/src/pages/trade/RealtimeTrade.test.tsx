// The Trade page for a listing priced by a real-time source (RT-QUOTES §5): its labels, the 5 s read and the chart's
// last bar following the quote; SGX's snapshot "about N min behind"; a Yahoo listing "delayed", as before.
import { act, fireEvent, screen, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from '../../lib/api'
import type { Asset, Candle } from '../../lib/types'
import { charts } from '../../test/chart'
import * as fx from '../../test/fixtures'
import { renderPage, stubSession } from '../../test/utils'
import { TradePage } from './TradePage'

const sec = (iso: string) => Date.parse(iso) / 1000
/** Yahoo's delayed hourly bars, the last one opening at 04:00 UTC (in USD, as the API sends a quote feed's). */
const bars: Candle[] = [
  { t: sec('2026-10-15T03:00:00Z'), o: 980, h: 990, l: 975, c: 985, v: 100 },
  { t: sec('2026-10-15T04:00:00Z'), o: 985, h: 992, l: 984, c: 990, v: 40 },
]
type Quote = NonNullable<Asset['quoteFeed']>
function feed(symbol: string, name: string, price: number, over: Partial<Quote>, session: Asset['session'] = 'open'): Asset {
  const a = fx.feedAsset(symbol, name, 0, { session, price })
  return { ...a, quoteFeed: { ...a.quoteFeed!, marketState: 'open', ...over } }
}
const naver = (over: Partial<Quote> = {}): Partial<Quote> => ({
  source: 'naver', class: 'realtime', asOf: '2026-10-15T04:20:15Z', lastTradeAt: '2026-10-15T04:20:15Z', readAt: '2026-10-15T04:20:17Z',
  delaySeconds: 2, timeBasis: 'volume', label: 'real-time · 04:20:15 UTC', ...over,
})
const sambio = (price = 1000, over: Partial<Quote> = {}) => feed('SAMBIO', 'Samsung Biologics', price, naver(over))
const candles = () => charts.at(-1)!.series.find((s) => s.type === 'Candlestick')!
/** The newest bar the chart drew: from a full redraw (setData) or an in-place update, whichever came last. */
function drawnLast() {
  const s = candles()
  const [u, d] = [s.update.mock.invocationCallOrder.at(-1) ?? -1, s.setData.mock.invocationCallOrder.at(-1) ?? -1]
  return u > d ? s.update.mock.calls.at(-1)![0] : (s.setData.mock.calls.at(-1)![0] as unknown[]).at(-1)
}
const flush = () => act(async () => { for (let i = 0; i < 4; i++) await Promise.resolve() })
const open = (symbol: string) => renderPage(<TradePage />, { route: `/trade/${symbol}`, path: '/trade/:symbol' })

beforeEach(() => {
  charts.length = 0
  vi.spyOn(api, 'candles').mockResolvedValue({ candles: bars })
})
afterEach(() => vi.useRealTimers())

describe('a listing priced by a real-time source', () => {
  it('labels the price real-time with its own trade time everywhere it shows, and names the source', async () => {
    stubSession().assets.mockResolvedValue({ assets: [...fx.assets, sambio()] })
    const { user } = open('SAMBIO')
    await screen.findByRole('heading', { name: 'Samsung Biologics' })
    await user.click(within(screen.getByRole('group', { name: 'Display currency' })).getByRole('button', { name: 'USD' }))
    expect(screen.getByText(/^SAMBIO · 207940\.KS · Naver/)).toBeInTheDocument()
    const line = await screen.findByText('real-time · 04:20:15 UTC')
    expect(line.parentElement).toHaveAttribute('title', expect.stringMatching(/^Naver Finance, a real-time source; 2 s behind the exchange when read at 04:20:17 UTC; the source gives no trade time/))
    expect(screen.getByTitle(/^USD price used for paper fills: Naver's price × FX/)).toHaveTextContent('$1,000.00')
    expect(screen.getByText(/^Quote feed: prices follow KRX hours\. It is open now, so fills use Naver Finance's real-time price \(last trade Oct 15, 04:20:15 UTC\)/)).toBeInTheDocument()
    // The chart's bars are Yahoo's, 20 min behind; the last one follows the quote.
    expect(await screen.findByText('Yahoo bars · 20 min delayed')).toBeInTheDocument()
    expect(screen.getByText('last bar real-time')).toBeInTheDocument()
    expect(screen.getByText('Yahoo bars · 20 min delayed').closest('[title]')).toHaveAttribute('title', expect.stringContaining("The last bar moves with Naver Finance's price (real-time · 04:20:15 UTC), read every 5 s"))
    // The market list's KRX group is tagged real-time; the row says its own label.
    const markets = screen.getByRole('complementary', { name: 'Markets' })
    const krx = within(markets).getByRole('list', { name: 'South Korea · KRX' })
    expect(krx.previousElementSibling).toHaveTextContent('South Korea · KRXreal-time')
    expect(within(krx).getByRole('link', { name: /SAMBIO/ })).toHaveAttribute('title', 'real-time · 04:20:15 UTC')
    await user.click(screen.getByRole('button', { name: 'Manual' }))
    await user.type(screen.getByLabelText('Amount (USD)'), '100')
    expect(screen.getByText('SAMBIO at $1,000.00 · real-time · 04:20:15 UTC')).toBeInTheDocument()
    expect(document.body.textContent).not.toMatch(/\blive\b/i)
  })

  it('labels an open position’s mark with the quote it is marked at', async () => {
    const position = { ...fx.position, symbol: 'SAMBIO', qty: 0.1, avgPrice: 990, markPrice: 1000, marketValue: 100 }
    stubSession({ portfolio: { ...fx.portfolio, positions: [position] } }).assets.mockResolvedValue({ assets: [...fx.assets, sambio()] })
    open('SAMBIO')
    await screen.findByRole('heading', { name: 'Samsung Biologics' })
    expect(await screen.findByTitle('Mark: real-time · 04:20:15 UTC')).toHaveTextContent('$1,000.00')
  })

  it('reads it every 5 s and moves the chart’s last bar with each new price, as the crypto socket does', async () => {
    vi.useFakeTimers()
    const assets = stubSession().assets.mockResolvedValue({ assets: [...fx.assets, sambio(1000)] })
    open('SAMBIO')
    await flush()
    await act(async () => {
      fireEvent.click(within(screen.getByRole('group', { name: 'Display currency' })).getByRole('button', { name: 'USD' }))
    })
    await act(() => vi.advanceTimersByTimeAsync(0))
    // Inside Yahoo's 04:00 bar (delayed 20 min): its close is the real-time price.
    expect(drawnLast()).toEqual({ time: sec('2026-10-15T04:00:00Z'), open: 985, high: 1000, low: 984, close: 1000 })
    assets.mockClear()
    assets.mockResolvedValue({ assets: [...fx.assets, sambio(1004, { asOf: '2026-10-15T05:00:31Z', lastTradeAt: '2026-10-15T05:00:31Z', label: 'real-time · 05:00:31 UTC' })] })
    await act(() => vi.advanceTimersByTimeAsync(5_000))
    expect(assets).toHaveBeenCalledTimes(1) // 5 s, not 10
    // A trade past the last bar opens the next one, so the price never sits beside a 20-minute-old bar.
    expect(candles().update).toHaveBeenLastCalledWith({ time: sec('2026-10-15T05:00:00Z'), open: 1004, high: 1004, low: 1004, close: 1004 })
    expect(screen.getByText('real-time · 05:00:31 UTC')).toBeInTheDocument()
  })

  it('ends the chart’s last bar at the settled close after the session, while Yahoo’s delayed bars still lag it', async () => {
    vi.useFakeTimers()
    // 06:35 UTC: KRX closed at 06:30; Yahoo's bars (20 min behind) end with its 06:10 price.
    vi.mocked(api.candles).mockResolvedValue({
      candles: [
        { t: sec('2026-10-15T05:00:00Z'), o: 980, h: 990, l: 975, c: 985, v: 100 },
        { t: sec('2026-10-15T06:00:00Z'), o: 985, h: 992, l: 984, c: 988, v: 40 },
      ],
    })
    const closed = feed('SAMBIO', 'Samsung Biologics', 1001, naver({
      marketState: 'closed', asOf: '2026-10-15T06:30:00Z', lastTradeAt: '2026-10-15T06:30:00Z', readAt: '2026-10-15T06:32:01Z', delaySeconds: 0,
      label: 'real-time · 06:30:00 UTC',
    }), 'closed')
    const assets = stubSession().assets.mockResolvedValue({ assets: [...fx.assets, closed] })
    open('SAMBIO')
    await flush()
    await act(async () => {
      fireEvent.click(within(screen.getByRole('group', { name: 'Display currency' })).getByRole('button', { name: 'USD' }))
    })
    await act(() => vi.advanceTimersByTimeAsync(0))
    expect(screen.getByText('last close · Oct 15, 06:30 UTC')).toBeInTheDocument()
    // the bar holding 06:30 closes at the settled close, not at Yahoo's 20-minute-old price
    expect(drawnLast()).toEqual({ time: sec('2026-10-15T06:00:00Z'), open: 985, high: 1001, low: 984, close: 1001 })
    expect(screen.getByText('last bar at the last trade')).toBeInTheDocument()
    expect(screen.getByText('Yahoo bars · 20 min delayed').closest('[title]')).toHaveAttribute('title', expect.stringContaining("The last bar ends at Naver Finance's last trade (Oct 15, 06:30:00 UTC)"))
    assets.mockClear()
    await act(() => vi.advanceTimersByTimeAsync(9_000))
    expect(assets).not.toHaveBeenCalled() // closed: the 10 s read, not the 5 s one
  })

  it('goes back to Yahoo’s labels and the 10 s read when the venue falls back to Yahoo', async () => {
    vi.useFakeTimers()
    const yahoo = feed('SAMBIO', 'Samsung Biologics', 990, {
      source: 'yahoo', class: 'delayed', asOf: '2026-10-15T04:00:15Z', lastTradeAt: '2026-10-15T04:00:15Z', delaySeconds: 1202, timeBasis: 'trade', label: 'delayed · last trade 04:00 UTC',
    })
    const assets = stubSession().assets.mockResolvedValue({ assets: [...fx.assets, yahoo] })
    open('SAMBIO')
    await flush()
    await act(() => vi.advanceTimersByTimeAsync(0))
    expect(screen.getByText('delayed · last trade 04:00 UTC')).toBeInTheDocument()
    expect(screen.getByText(/^SAMBIO · 207940\.KS · Yahoo/)).toBeInTheDocument()
    expect(screen.getByText('Yahoo bars · 20 min delayed')).toBeInTheDocument()
    expect(screen.queryByText(/^last bar/)).toBeNull()
    expect(candles().update).not.toHaveBeenCalled()
    assets.mockClear()
    await act(() => vi.advanceTimersByTimeAsync(9_000))
    expect(assets).not.toHaveBeenCalled() // the 5 s step only while a real-time source serves
    await act(() => vi.advanceTimersByTimeAsync(1_000))
    expect(assets).toHaveBeenCalledTimes(1)
  })
})

describe('SGX’s once-a-minute snapshot and a delayed Yahoo listing', () => {
  it('reads about N min behind for SGX, with the snapshot’s time, never real-time', async () => {
    const dbs = feed('DBS', 'DBS Group', 61.4, {
      source: 'sgx', class: 'near-realtime', asOf: '2026-10-15T04:18:00Z', lastTradeAt: '2026-10-15T04:18:00Z', readAt: '2026-10-15T04:19:39Z',
      delaySeconds: 99, timeBasis: 'snapshot', label: 'about 2 min behind · 04:18 UTC',
    })
    stubSession().assets.mockResolvedValue({ assets: [...fx.assets, dbs] })
    const { user } = open('DBS')
    await screen.findByRole('heading', { name: 'DBS Group' })
    expect(await screen.findByText('about 2 min behind · 04:18 UTC')).toBeInTheDocument()
    expect(screen.getByText(/^DBS · D05\.SI · SGX/)).toBeInTheDocument()
    expect(screen.getAllByText(/fills use SGX's price, about 2 min behind the exchange \(snapshot of Oct 15, 04:18 UTC\)/)).toHaveLength(2) // header and ticket
    expect(await screen.findByText('Yahoo bars · 10 min delayed')).toBeInTheDocument()
    expect(screen.getByText('last bar about 2 min behind')).toBeInTheDocument()
    const sgx = within(screen.getByRole('complementary', { name: 'Markets' })).getByRole('list', { name: 'Singapore · SGX' })
    expect(sgx.previousElementSibling).toHaveTextContent('Singapore · SGXabout 2 min behind')
    expect(screen.queryByText(/real-time/)).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Manual' }))
    await user.type(screen.getByLabelText('Amount (USD)'), '100')
    expect(screen.getByText('DBS at $61.40 · about 2 min behind · 04:18 UTC')).toBeInTheDocument()
  })

  it('reads delayed with its last trade for a Yahoo listing (TSE), and the last close once closed', async () => {
    const tel = (over: Partial<Quote>, session: Asset['session'] = 'open') => feed('TOKYOELEC', 'Tokyo Electron', 210, {
      source: 'yahoo', class: 'delayed', asOf: '2026-10-15T04:05:00Z', lastTradeAt: '2026-10-15T04:05:00Z', delaySeconds: 905, timeBasis: 'trade',
      label: 'delayed · last trade 04:05 UTC', ...over,
    }, session)
    const assets = stubSession().assets.mockResolvedValue({ assets: [...fx.assets, tel({})] })
    const view = open('TOKYOELEC')
    await screen.findByRole('heading', { name: 'Tokyo Electron' })
    expect(await screen.findByText('delayed · last trade 04:05 UTC')).toBeInTheDocument()
    expect(screen.getByText('Yahoo bars · 15 min delayed')).toBeInTheDocument()
    expect(screen.queryByText(/^last bar/)).toBeNull()
    const tse = within(screen.getByRole('complementary', { name: 'Markets' })).getByRole('list', { name: 'Japan · TSE' })
    expect(tse.previousElementSibling).toHaveTextContent('Japan · TSEdelayed')
    view.unmount()
    assets.mockResolvedValue({ assets: [...fx.assets, tel({ marketState: 'closed', asOf: '2026-10-15T06:30:00Z', lastTradeAt: '2026-10-15T06:30:00Z' }, 'closed')] })
    open('TOKYOELEC')
    expect(await screen.findByText('last close · Oct 15, 06:30 UTC')).toBeInTheDocument()
    const closed = within(screen.getByRole('complementary', { name: 'Markets' })).getByRole('list', { name: 'Japan · TSE' })
    expect(closed.previousElementSibling).toHaveTextContent('Japan · TSEclosed')
  })
})

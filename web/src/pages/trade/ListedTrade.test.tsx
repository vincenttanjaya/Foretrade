// The Trade page for a listed Asian stock (symbol = its Yahoo ticker) and the market list grouped by venue.
import { act, screen, waitFor, within } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError } from '../../lib/api'
import type { Asset, Instrument } from '../../lib/types'
import * as fx from '../../test/fixtures'
import { charts } from '../../test/chart'
import { currentPath, FakeWebSocket, renderPage, stubSession } from '../../test/utils'
import { TradePage } from './TradePage'

const tokyoElec = fx.feedAsset('TOKYOELEC', 'Tokyo Electron')
const dbs = fx.feedAsset('DBS', 'DBS Group')
const hrefs = (el: HTMLElement) => within(el).queryAllByRole('link').map((l) => l.getAttribute('href'))
const open = (symbol: string) => renderPage(<TradePage />, { route: `/trade/${symbol}`, path: '/trade/:symbol' })
const markets = () => screen.getByRole('complementary', { name: 'Markets' })
const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })

/** The named poll answers the named rows plus its `with` symbols (when `withAnswer`); the listed poll, `listed`. */
function answer({ named = [...fx.assets, tokyoElec, dbs], listed = fx.listedAssets, withAnswer = true }: { named?: Asset[]; listed?: Asset[]; withAnswer?: boolean } = {}) {
  vi.mocked(api.assets).mockImplementation(async (scope, withSymbols) => ({
    assets: scope === 'listed' ? listed : [...named, ...(withAnswer ? listed.filter((a) => withSymbols?.includes(a.symbol)) : [])],
  }))
}

beforeEach(() => {
  stubSession({ listed: true })
  answer()
  vi.spyOn(api, 'candles').mockResolvedValue({ candles: [{ t: 1_791_000_000, o: 29.8, h: 30, l: 29.7, c: 29.9, v: 1000 }] })
  vi.spyOn(api, 'trades').mockResolvedValue({ trades: [] })
})

describe('Trade page · a listed stock', () => {
  it('defaults foreign quotes, session prices, market rows and candles to original currency and can switch to USD', async () => {
    const base = fx.listedAsset('6501.T')
    const asset = { ...base, price: 50, high24h: 55, low24h: 45, volume24hUsd: 120000,
      quoteFeed: { ...base.quoteFeed!, localPrice: 5000, fx: 0.01 } }
    answer({ listed: [asset] })
    vi.mocked(api.candles).mockImplementation(async (_symbol, _interval, _limit, _signal, currency) => ({
      candles: currency === 'original'
        ? [{ t: 1_791_000_000, o: 3100, h: 3200, l: 3000, c: 3150, v: 1000 }]
        : [{ t: 1_791_000_000, o: 29.8, h: 30, l: 29.7, c: 29.9, v: 1000 }],
    }))
    const order = vi.spyOn(api, 'placeOrder').mockResolvedValue({ trade: fx.trade(), portfolio: fx.portfolio })
    const { user } = open('6501.T')
    await screen.findByTitle(/Original JPY listing price/)
    const currency = screen.getByRole('group', { name: 'Display currency' })
    expect(within(currency).getByRole('button', { name: 'JPY' })).toHaveAttribute('aria-pressed', 'true')
    expect(within(currency).getByRole('button', { name: 'USD' })).toHaveAttribute('aria-pressed', 'false')
    expect(screen.getByTitle(/Original JPY listing price/)).toHaveTextContent('JPY 5,000.00')
    expect(screen.getByText('Session high').nextElementSibling).toHaveTextContent('JPY 5,500.00')
    expect(screen.getByText('Session low').nextElementSibling).toHaveTextContent('JPY 4,500.00')
    expect(screen.getByText('Session volume').nextElementSibling).toHaveTextContent('JPY 12M')
    expect(within(markets()).getByRole('link', { name: /^6501\.T/ })).toHaveTextContent('JPY 5,000.00')
    const candles = charts.at(-1)!.series.find((s) => s.type === 'Candlestick')!
    await waitFor(() => expect(candles.setData).toHaveBeenLastCalledWith([
      { time: 1_791_000_000, open: 3100, high: 3200, low: 3000, close: 3150 },
    ]))
    await user.click(screen.getByRole('button', { name: 'Manual' }))
    await user.type(screen.getByLabelText('Amount (USD)'), '500')
    expect(screen.getByText('6501.T at $50.00 · delayed · last trade 03:31 UTC')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Confirm buy (paper)' }))
    expect(order).toHaveBeenCalledWith(expect.objectContaining({ symbol: '6501.T', notional: 500 }))
    await user.click(within(currency).getByRole('button', { name: 'USD' }))
    expect(within(currency).getByRole('button', { name: 'USD' })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByTitle(/Yahoo's regular-market price × FX/)).toHaveTextContent('$50.00')
    expect(screen.getByText('Session high').nextElementSibling).toHaveTextContent('$55.00')
  })

  it('has no original currency switch for a foreign listing without usable FX', async () => {
    const base = fx.listedAsset('6501.T')
    answer({ listed: [{ ...base, quoteFeed: { ...base.quoteFeed!, fx: 0 } }] })
    open('6501.T')
    await screen.findByRole('heading', { name: 'Hitachi, Ltd.' })
    await screen.findByTitle(/Yahoo's regular-market price × FX/)
    expect(screen.queryByRole('group', { name: 'Display currency' })).toBeNull()
  })

  it('reads like any quote feed: the company, its ticker once, the index line, the venue session and the delayed hours note', async () => {
    const derivatives = vi.spyOn(api, 'derivatives')
    open('6501.T')
    expect(await screen.findByRole('heading', { name: 'Hitachi, Ltd.' })).toBeInTheDocument()
    expect(screen.getByText('Quote feed').parentElement).toHaveTextContent(/^6501\.T · Yahoo Quote feed$/) // no "6501.T · 6501.T"
    expect(screen.getByText('TSE: 6501 (Nikkei 225), JPY→USD at Yahoo FX')).toBeInTheDocument()
    expect(await screen.findByText('TSE session open')).toBeInTheDocument()
    expect(screen.getByTitle(/Original JPY listing price/)).toHaveTextContent('JPY 4,726.00')
    expect(screen.getByText(/^Quote feed: prices follow TSE hours\. It is open now, so fills use Yahoo's latest trade, which is delayed \(last trade Oct 13, 03:31 UTC\)/)).toBeInTheDocument()
    expect(api.assets).toHaveBeenCalledWith('named', ['6501.T']) // the 10 s poll reads the symbol on screen
    expect(api.candles).toHaveBeenCalledWith('6501.T', '1h', 500, expect.any(AbortSignal), 'original') // native Yahoo candles through the API
    expect(derivatives).not.toHaveBeenCalled()
    expect(FakeWebSocket.instances).toHaveLength(0)
    expect(screen.getByRole('button', { name: 'Set alert' })).toBeEnabled()
    expect(currentPath()).toBe('/trade/6501.T')
  })

  it('says delayed with the last trade time on the open ticket, and fills a paper order', async () => {
    const order = vi.spyOn(api, 'placeOrder').mockResolvedValue({
      trade: fx.trade({ symbol: '6501.T', qty: 16.74, price: 29.87, notional: 500 }), portfolio: fx.portfolio,
    })
    const { user } = open('6501.T')
    await screen.findByRole('heading', { name: 'Hitachi, Ltd.' })
    await screen.findByText('TSE session open')
    await user.click(screen.getByRole('button', { name: 'Manual' }))
    await user.type(screen.getByLabelText('Amount (USD)'), '500')
    expect(screen.getByText('6501.T at $29.8722 · delayed · last trade 03:31 UTC')).toBeInTheDocument()
    expect(screen.queryByText(/6501\.T at .* · live/)).toBeNull()
    expect(screen.getByText(/^Paper fills use the USD price/)).toHaveTextContent('JPY→USD at Yahoo FX: 1 JPY = $0.006321')
    await user.click(screen.getByRole('button', { name: 'Confirm buy (paper)' }))
    expect(order).toHaveBeenCalledWith({ symbol: '6501.T', side: 'buy', notional: 500, stopLoss: undefined, takeProfit: undefined, source: 'manual', note: undefined })
    expect(await screen.findByText('Bought 16.74 6501.T at $29.87 (paper).')).toBeInTheDocument()
  })

  it('keeps the crypto ticket live', async () => {
    const { user } = open('BTC')
    await screen.findByRole('heading', { name: 'Bitcoin' })
    await user.click(screen.getByRole('button', { name: 'Manual' }))
    expect(await screen.findByText(/^BTC at \$29\.90 · live$/)).toBeInTheDocument() // the chart's last close, live from Binance
  })

  it('waits for the first Yahoo quote of an unpriced listed symbol: no redirect, no socket, no order', async () => {
    answer({ listed: [], withAnswer: false })
    let release!: () => void
    vi.mocked(api.assets).mockImplementation(async (scope) => {
      if (scope === 'named') await new Promise<void>((r) => (release = r)) // the server is reading it
      return { assets: scope === 'listed' ? [] : fx.assets }
    })
    const { user } = open('000810.KS')
    expect(await screen.findByRole('heading', { name: 'Samsung Fire & Marine Insurance Co., Ltd.' })).toBeInTheDocument() // the catalogue's name
    expect(screen.getByText('Quote feed').parentElement).toHaveTextContent('000810.KS · Yahoo')
    expect(screen.getByText('KRX: 000810 (KOSPI 200), KRW→USD at Yahoo FX')).toBeInTheDocument()
    expect(screen.getByText('KRX session unknown')).toBeInTheDocument()
    expect(screen.getAllByText('Waiting for the first Yahoo quote')).toHaveLength(2) // the header, and the ticket behind its tab
    expect(FakeWebSocket.instances).toHaveLength(0) // never a Binance pair for a Yahoo venue
    expect(screen.queryByText('000810.KS/USDT')).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Manual' }))
    expect(screen.getByRole('button', { name: 'Confirm buy (paper)' })).toBeDisabled()
    expect(screen.getByText(/^Paper fills use the USD price/)).toHaveTextContent('Quote feed: prices follow KRX hours; outside them paper fills use the last close. KRW→USD at Yahoo FX.')

    await act(async () => release()) // answered, without the row: no fresh quote right now
    expect(await screen.findByText('Price unavailable')).toBeInTheDocument()
    expect(currentPath()).toBe('/trade/000810.KS')
  })

  it('waits for the catalogue before deciding about a symbol the bundle does not know', async () => {
    let load!: () => void
    vi.mocked(api.instruments).mockReturnValue(new Promise((r) => (load = () => r({ instruments: fx.instruments, etag: null }))))
    open('6501.T')
    expect(await screen.findByRole('status', { name: 'Loading the market' })).toBeInTheDocument()
    await flush()
    expect(currentPath()).toBe('/trade/6501.T')
    expect(FakeWebSocket.instances).toHaveLength(0)
    expect(api.candles).not.toHaveBeenCalled()
    await act(async () => load())
    expect(await screen.findByRole('heading', { name: 'Hitachi, Ltd.' })).toBeInTheDocument()
  })

  it('keeps a listed deep link while a failed catalogue read waits for its retry', async () => {
    vi.mocked(api.instruments).mockRejectedValue(new ApiError(502, 'upstream', 'Bad gateway'))
    answer({ listed: [], withAnswer: false })
    open('6501.T')
    await waitFor(() => expect(api.instruments).toHaveBeenCalled())
    await flush()
    expect(screen.getByRole('status', { name: 'Loading the market' })).toBeInTheDocument()
    expect(currentPath()).toBe('/trade/6501.T') // not BTC: the catalogue is retried every minute
    expect(FakeWebSocket.instances).toHaveLength(0)
  })

  it('sends a listed symbol to BTC against an API without the catalogue, as before', async () => {
    vi.mocked(api.instruments).mockRejectedValue(new ApiError(404, 'not_found', 'Not found'))
    open('6501.T')
    await waitFor(() => expect(currentPath()).toBe('/trade/BTC'))
  })

  it('opens a listed ticker typed in any case on its upper-case symbol', async () => {
    open('6501.t')
    await waitFor(() => expect(currentPath()).toBe('/trade/6501.T'))
    expect(await screen.findByRole('heading', { name: 'Hitachi, Ltd.' })).toBeInTheDocument()
  })
})

describe('Market list · by venue', () => {
  it('groups the local listings by venue with a muted delayed tag, and folds the listed rows behind "Show all"', async () => {
    const { user } = open('BTC')
    await screen.findByRole('heading', { name: 'Bitcoin' })
    const tse = await within(markets()).findByRole('list', { name: 'Japan · TSE' })
    await waitFor(() => expect(within(markets()).getByRole('button', { name: 'Show all 2' })).toBeInTheDocument())
    const labels = within(markets()).getAllByRole('list').slice(1).map((g) => document.getElementById(g.getAttribute('aria-labelledby')!)!.textContent)
    expect(labels).toEqual(['Crypto', 'Singapore · SGX', 'South Korea · KRX', 'Japan · TSE', 'Indonesia · IDX'])
    expect(hrefs(tse)).toEqual(['/trade/TOKYOELEC']) // the named row; Hitachi is folded
    expect(hrefs(within(markets()).getByRole('list', { name: 'South Korea · KRX' }))).toEqual([]) // only a listed row: just its toggle
    expect(within(markets()).getAllByText('delayed')).toHaveLength(4)
    expect(within(markets()).getAllByText('delayed')[0]).toHaveClass('normal-case', 'text-muted/75')

    const show = within(markets()).getByRole('button', { name: 'Show all 2' })
    expect(show).toHaveAttribute('aria-expanded', 'false')
    expect(show).toHaveAttribute('aria-controls', tse.id)
    await user.click(show)
    expect(hrefs(tse)).toEqual(['/trade/TOKYOELEC', '/trade/6501.T'])
    expect(within(tse).getByRole('link', { name: /^6501\.T/ })).toHaveTextContent('Hitachi, Ltd.')
    expect(within(markets()).getByRole('button', { name: 'Show fewer' })).toHaveAttribute('aria-expanded', 'true')
    await user.click(within(markets()).getByRole('button', { name: 'Show fewer' }))
    expect(hrefs(tse)).toEqual(['/trade/TOKYOELEC'])
  })

  it("opens the active row's group on its own, so the active row is in view", async () => {
    const melco = fx.listedAsset('6501.T', { symbol: '6503.T', pair: '6503.T', name: 'Mitsubishi Electric Corporation' })
    vi.mocked(api.instruments).mockResolvedValue({ instruments: [...fx.instruments, { ...fx.listedInstruments[0], symbol: '6503.T', pair: '6503.T', name: melco.name }], etag: null })
    answer({ listed: [...fx.listedAssets, melco] })
    open('6501.T')
    await screen.findByRole('heading', { name: 'Hitachi, Ltd.' })
    const tse = await within(markets()).findByRole('list', { name: 'Japan · TSE' })
    await waitFor(() => expect(hrefs(tse)).toEqual(['/trade/TOKYOELEC', '/trade/6501.T', '/trade/6503.T']))
    expect(within(tse).getByRole('link', { name: /^6501\.T/ })).toHaveAttribute('aria-current', 'page')
    expect(within(markets()).getByRole('button', { name: 'Show fewer' })).toHaveAttribute('aria-expanded', 'true')
    for (const other of within(markets()).getAllByRole('button', { name: 'Show all 1' })) expect(other).toHaveAttribute('aria-expanded', 'false') // KRX and IDX stay folded
  })

  it('scrolls the list to a listed active row that arrives after the named rows', async () => {
    vi.spyOn(Element.prototype, 'getBoundingClientRect').mockImplementation(function (this: Element) {
      return { top: this.getAttribute('aria-current') === 'page' ? 400 : 0 } as DOMRect
    })
    vi.spyOn(Element.prototype, 'clientHeight', 'get').mockReturnValue(300)
    vi.spyOn(HTMLElement.prototype, 'offsetHeight', 'get').mockReturnValue(50)
    const scroll = vi.spyOn(Element.prototype, 'scrollTo')
    let quote!: () => void
    answer({ listed: [] }) // the listed poll has no quote for it yet
    vi.mocked(api.assets).mockImplementation(async (scope, withSymbols) => {
      if (withSymbols?.length) await new Promise<void>((r) => (quote = r))
      return { assets: scope === 'listed' ? [] : [...fx.assets, tokyoElec, ...(withSymbols?.length ? [fx.listedAsset('6501.T')] : [])] }
    })
    open('6501.T')
    await screen.findByRole('heading', { name: 'Hitachi, Ltd.' }) // the catalogue's row
    await within(markets()).findByRole('list', { name: 'Japan · TSE' }) // the named rows are in, the active one is not
    expect(scroll).not.toHaveBeenCalled()
    await act(async () => quote())
    await waitFor(() => expect(scroll).toHaveBeenCalledWith({ top: 275 }))
  })

  it("keeps the user's held listed rows in a folded group", async () => {
    const held = { ...fx.position, symbol: '000810.KS', qty: 1.5, avgPrice: 300, markPrice: 307.7 }
    vi.mocked(api.portfolio).mockResolvedValue({ portfolio: { ...fx.portfolio, positions: [held] } })
    open('BTC')
    await screen.findByRole('heading', { name: 'Bitcoin' })
    const krx = await within(markets()).findByRole('list', { name: 'South Korea · KRX' })
    await waitFor(() => expect(hrefs(krx)).toEqual(['/trade/000810.KS']))
    expect(within(krx.parentElement!).queryByRole('button')).toBeNull() // nothing left to unfold there
    expect(within(markets()).getAllByRole('button', { name: /^Show all/ })).toHaveLength(2) // TSE's and IDX's
  })

  it('finds a listed stock with no price yet only by searching, marked "No price yet"', async () => {
    const mit: Instrument = { symbol: 'ME8U.SI', pair: 'ME8U.SI', name: 'Mapletree Industrial Trust', class: 'stock', venue: 'yahoo', region: 'asia',
      sessionVenue: 'SGX', localCurrency: 'SGD', underlying: 'SGX: ME8U (Straits Times Index), SGD→USD at Yahoo FX', yahoo: 'ME8U.SI', listed: true,
      index: 'Straits Times Index' }
    vi.mocked(api.instruments).mockResolvedValue({ instruments: [...fx.instruments, mit], etag: null })
    const { user } = open('BTC')
    await screen.findByRole('heading', { name: 'Bitcoin' })
    await within(markets()).findByRole('list', { name: 'Japan · TSE' })
    expect(hrefs(markets())).not.toContain('/trade/ME8U.SI') // the default list is the priced rows
    await user.click(within(markets()).getByRole('searchbox', { name: 'Search markets' }))
    await user.paste('mapletree industrial')
    expect(hrefs(markets())).toEqual(['/trade/ME8U.SI'])
    expect(within(markets()).getByRole('link', { name: /ME8U\.SI/ })).toHaveTextContent('No price yet')
  })

  it('finds a listed stock by ticker, name, country, venue and index; Enter opens the first match', async () => {
    const { user } = open('BTC')
    await screen.findByRole('heading', { name: 'Bitcoin' })
    await within(markets()).findByRole('list', { name: 'Japan · TSE' })
    const search = within(markets()).getByRole('searchbox', { name: 'Search markets' })
    const cases: [string, string[]][] = [
      ['6501', ['/trade/6501.T']],
      ['6501.t', ['/trade/6501.T']],
      ['hitachi', ['/trade/6501.T']],
      ['japan', ['/trade/TOKYOELEC', '/trade/6501.T']],
      ['tse', ['/trade/TOKYOELEC', '/trade/6501.T']],
      ['nikkei', ['/trade/6501.T']],
      ['kospi', ['/trade/000810.KS']],
      ['indonesia', ['/trade/BBNI.JK']],
      ['d05.si', ['/trade/DBS']], // a named feed by its Yahoo ticker
    ]
    for (const [q, found] of cases) {
      await user.clear(search)
      await user.paste(q)
      expect(hrefs(markets()), q).toEqual(found)
    }
    await user.clear(search)
    await user.paste('samsung fire')
    await user.keyboard('{Enter}')
    expect(currentPath()).toBe('/trade/000810.KS')
  })

  it('shows at most 50 rows a group while searching, then how many more', async () => {
    const many = Array.from({ length: 57 }, (_, i) => fx.listedAsset('6501.T', { symbol: `${7000 + i}.T`, name: `Sample Company ${i}` }))
    vi.mocked(api.instruments).mockResolvedValue({
      instruments: [...fx.instruments, ...many.map((a) => ({ ...fx.listedInstruments[0], symbol: a.symbol, pair: a.symbol, name: a.name }))], etag: null,
    })
    answer({ listed: many })
    const { user } = open('BTC')
    await screen.findByRole('heading', { name: 'Bitcoin' })
    await waitFor(() => expect(within(markets()).getByRole('button', { name: 'Show all 58' })).toBeInTheDocument())
    await user.click(within(markets()).getByRole('searchbox', { name: 'Search markets' }))
    await user.paste('sample')
    expect(hrefs(within(markets()).getByRole('list', { name: 'Japan · TSE' }))).toHaveLength(50)
    expect(within(markets()).getByText('7 more, keep typing')).toBeInTheDocument()
    await user.paste(' 5')
    expect(within(markets()).queryByText(/more, keep typing/)).toBeNull()
  })
})

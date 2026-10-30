import { act, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from '../../lib/api'
import type { Asset } from '../../lib/types'
import * as fx from '../../test/fixtures'
import { currentPath, FakeWebSocket, renderPage, stubSession } from '../../test/utils'
import { VENUE_GROUPS, venueGroup } from '../strategies/strategy'
import { TradePage } from './TradePage'

const stock = { symbol: 'AAPL', pair: 'xyz:AAPL', name: 'Apple', class: 'stock', venue: 'hyperliquid', price: 225, change24hPct: 2, high24h: 230, low24h: 220, volume24hUsd: 1e7, range24hSource: 'hourly_candles', session: 'closed', sessionChangeAt: '2026-10-05T13:30:00Z' } as Asset
const index = { ...stock, symbol: 'SP500', pair: 'xyz:SP500', name: 'S&P 500 perp', class: 'index' } as Asset
beforeEach(() => {
  stubSession().assets.mockResolvedValue({ assets: [...fx.assets, stock, index] })
  vi.spyOn(api, 'candles').mockResolvedValue({ candles: [{ t: 1_790_928_000, o: 180, h: 181, l: 179, c: 180, v: 1 }, { t: 1_790_931_600, o: 181, h: 182, l: 180, c: 181, v: 1 }] })
})
afterEach(() => vi.useRealTimers())
const open = () => renderPage(<TradePage />, { route: '/trade/AAPL', path: '/trade/:symbol' })
const hrefs = (el: HTMLElement) => within(el).queryAllByRole('link').map((l) => l.getAttribute('href'))
/** `symbols` as the market list shows them: by venue group (VENUE_GROUPS), then in the API's order inside a group. */
const inListOrder = (symbols: readonly string[]) =>
  VENUE_GROUPS.flatMap((g) => fx.universeAssets.filter((a) => venueGroup(a) === g.key && symbols.includes(a.symbol)).map((a) => `/trade/${a.symbol}`))
/** Below xl: the market picker's open panel. */
async function picker(user: ReturnType<typeof open>['user']) {
  await user.click(screen.getByRole('button', { name: /^Switch market: / }))
  return screen.getByRole('dialog', { name: 'Switch market' })
}

describe('stock trade presentation', () => {
  it('keeps the market mark in the header and ticket while chart closes differ', async () => {
    const { user } = open(); await screen.findByRole('heading', { name: 'Apple' })
    await act(async () => { await Promise.resolve() })
    expect(screen.getByTitle(/Hyperliquid perp mark used for paper fills/)).toHaveTextContent('$225.00')
    expect(FakeWebSocket.instances).toHaveLength(0)
    expect(screen.queryByText('AAPL/USDT')).toBeNull()
    expect(screen.getByText('US session closed')).toBeInTheDocument()
    expect(screen.getByText(/Approx\. 24h range · hourly candles/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Switch market: AAPL Apple' })).toBeInTheDocument()
    expect(within(await picker(user)).getByRole('list', { name: 'US stocks & indices' })).toHaveTextContent('SP500')
    await user.click(screen.getByRole('button', { name: 'Manual' }))
    await user.type(screen.getByLabelText('Amount (USD)'), '225')
    expect(screen.getByText('Est. quantity').nextSibling).toHaveTextContent('1 AAPL')
    expect(screen.getByText(/No funding accrual, dividends or split adjustment/)).toBeInTheDocument()
  })

  it('shows unavailable range dashes and unknown session without invented zero prices or boundaries', async () => {
    vi.mocked(api.assets).mockResolvedValue({ assets: [...fx.assets, { ...stock, high24h: 0, low24h: 0, range24hSource: 'unavailable', session: null, sessionChangeAt: null }] })
    open(); await screen.findByRole('heading', { name: 'Apple' })
    expect(screen.getByText('24h high').nextSibling).toHaveTextContent('—')
    expect(screen.getByText('24h low').nextSibling).toHaveTextContent('—')
    expect(screen.getByText(/24h range unavailable/)).toBeInTheDocument()
    expect(screen.getByText('US session unknown')).toBeInTheDocument()
    expect(screen.queryByText('$0.00')).toBeNull()
  })

  it('reads a changed session from the next asset response even when the mark is unchanged', async () => {
    vi.useFakeTimers()
    vi.mocked(api.assets).mockResolvedValueOnce({ assets: [stock] }).mockResolvedValue({ assets: [{ ...stock, session: 'open', sessionChangeAt: '2026-10-05T20:00:00Z' }] })
    open()
    await act(async () => { await Promise.resolve(); await Promise.resolve() })
    expect(screen.getByText('US session closed')).toBeInTheDocument()
    await act(() => vi.advanceTimersByTimeAsync(10_000))
    expect(screen.getByText('US session open')).toHaveAttribute('title', expect.stringContaining('2026'))
    expect(screen.getByTitle(/Hyperliquid perp mark used for paper fills/)).toHaveTextContent('$225.00')
  })

  it('keeps an unavailable configured stock selected without substituting its chart close for a quote', async () => {
    vi.mocked(api.assets).mockResolvedValue({ assets: fx.assets })
    const { user } = open()
    await screen.findByRole('heading', { name: 'Apple' })
    await act(async () => { await Promise.resolve() })
    expect(screen.getByText('Price unavailable')).toBeInTheDocument()
    expect(screen.queryByText('AAPL/USDT')).toBeNull()
    expect(FakeWebSocket.instances).toHaveLength(0)
    await user.click(screen.getByRole('button', { name: 'Manual' }))
    await user.type(screen.getByLabelText('Amount (USD)'), '225')
    expect(screen.getByText('Est. quantity').nextSibling).toHaveTextContent('—')
  })
})

describe('Asia stock presentation', () => {
  const tsm = { ...stock, symbol: 'TSM', pair: 'xyz:TSM', name: 'TSMC (Taiwan Semiconductor)', price: 485, region: 'asia', sessionVenue: 'NYSE', underlying: 'NYSE: TSM ADS = 5 TWSE 2330 shares', localCurrency: 'USD' } as Asset
  const smsn = { ...stock, symbol: 'SMSN', pair: 'xyz:SMSN', name: 'Samsung Electronics', price: 62, session: 'open', region: 'asia', sessionVenue: 'KRX', underlying: 'KRX 005930 common share, KRW→USD', localCurrency: 'KRW' } as Asset
  const usAapl = { ...stock, region: 'us', sessionVenue: 'NYSE', underlying: 'Nasdaq: AAPL common share', localCurrency: 'USD' } as Asset
  beforeEach(() => vi.mocked(api.assets).mockResolvedValue({ assets: [...fx.assets.map((a) => ({ ...a, region: null })), usAapl, index, tsm, smsn] }))

  it('groups markets as crypto, US and Asia, and finds TSMC by name or ticker', async () => {
    const { user } = renderPage(<TradePage />, { route: '/trade/BTC', path: '/trade/:symbol' })
    await screen.findByRole('heading', { name: 'Bitcoin' }) // the configured name: it shows before the asset list loads
    const selector = await picker(user)
    await within(selector).findByRole('list', { name: 'Asia perps & US-listed ADRs' }) // the groups, once the asset rows load
    const groups = within(selector).getAllByRole('list').slice(1) // after the whole scroll list
    expect(groups.map((g) => document.getElementById(g.getAttribute('aria-labelledby')!)!.textContent)).toEqual(['Crypto', 'US stocks & indices', 'Asia perps & US-listed ADRs'])
    expect(hrefs(within(selector).getByRole('list', { name: 'Asia perps & US-listed ADRs' }))).toEqual(['/trade/TSM', '/trade/SMSN']) // perps: a KRX underlying stays here
    // SP500 has no region on this row: its class and the configured table still put it with US.
    expect(within(selector).getByRole('list', { name: 'US stocks & indices' })).toHaveTextContent('SP500')
    expect(within(selector).queryByText('delayed')).toBeNull() // perps and crypto quote live
    const markets = screen.getByRole('complementary', { name: 'Markets' })
    expect(within(markets).getByText('Asia perps & US-listed ADRs')).toBeInTheDocument()
    const search = within(markets).getByRole('searchbox', { name: 'Search markets' })
    for (const q of ['TSMC', 'taiwan', 'TSM']) {
      await user.clear(search)
      await user.type(search, q)
      expect(within(markets).getAllByRole('link').map((l) => l.getAttribute('href')), q).toEqual(['/trade/TSM'])
    }
  })

  // Korea: the KRW perps and EWY, SK hynix's ADS, Coupang (no Korean listing), the KRX ADRs and the KRX quote feeds.
  const korea = ['SMSN', 'SKHX', 'HYUNDAI', 'EWY', 'SKHY', 'CPNG', 'KB', 'SHG', 'PKX', 'LGES', 'NAVER', 'SAMBIO', 'KIA', 'CELLTRION', 'SAMSDI', 'LGCHEM',
    'MOBIS', 'SAMCT', 'HANWHAAERO', 'HDHHI', 'HDKSOE', 'DOOSANENER', 'HANA', 'KAKAO', 'SAMLIFE', 'LGE', 'SKINNO', 'SKINC', 'KRAFTON', 'HYBE', 'KOREAZINC',
    'HANWHAOCEAN', 'SEMCO', 'KEP', 'SKM', 'KT', 'LPL']
  const openUniverse = async () => {
    vi.mocked(api.assets).mockResolvedValue({ assets: fx.universeAssets })
    const view = renderPage(<TradePage />, { route: '/trade/BTC', path: '/trade/:symbol' })
    await screen.findByRole('heading', { name: 'Bitcoin' }) // the configured name: it shows before the asset list loads
    const markets = screen.getByRole('complementary', { name: 'Markets' })
    // Every row stays in the capped, scrolling list, once api.assets resolves.
    await waitFor(() => expect(hrefs(markets)).toHaveLength(fx.universeAssets.length))
    return { ...view, markets }
  }

  it('finds listings by region, venue and local ticker across the full universe in the list', async () => {
    const { user, markets } = await openUniverse()
    const search = within(markets).getByRole('searchbox', { name: 'Search markets' })
    const found = {
      korea,
      'South Korea': korea,
      // HKEX lines, the ADRs on HKEX shares, Yum China (also HKEX 9987), Futu, and the Hong Kong issuers' SGX lines.
      'Hong Kong': ['BABA', 'MINIMAX', 'ZHIPU', 'JD', 'BIDU', 'NIO', 'LI', 'XPEV', 'NTES', 'TCOM', 'BILI', 'FUTU', 'TME', 'BEKE', 'ZTO', 'YUMC', 'TENCENT',
        'XIAOMI', 'MEITUAN', 'BYD', 'AIA', 'JARDINE', 'HKLAND'],
      // The SSE perps and KSTR, then the US- and HK-listed mainland issuers; China Steel (Taiwan) by its name.
      china: ['BABA', 'MINIMAX', 'ZHIPU', 'CXMT', 'UNITREE', 'GIGADEV', 'KSTR', 'PDD', 'JD', 'BIDU', 'NIO', 'LI', 'XPEV', 'NTES', 'TCOM', 'BILI', 'FUTU',
        'TME', 'BEKE', 'ZTO', 'YUMC', 'VIPS', 'TENCENT', 'XIAOMI', 'MEITUAN', 'BYD', 'CHINASTEEL'],
      japan: ['SOFTBANK', 'EWJ', 'KIOXIA', 'SONY', 'TM', 'MUFG', 'HMC', 'SMFG', 'TOKYOELEC', 'KEYENCE', 'ADVANTEST', 'NINTENDO', 'FASTRETAIL'],
      taiwan: ['TSM', 'EWT', 'UMC', 'ASX', 'MEDIATEK', 'HONHAI', 'DELTA', 'QUANTA', 'FUBON', 'CATHAY', 'CTBC', 'LARGAN', 'ASUS', 'ACCTON', 'WISTRON',
        'WIWYNN', 'EVERGREEN', 'FORMOSA', 'CHINASTEEL', 'NOVATEK', 'REALTEK', 'UNIMICRON', 'ADVANTECH', 'CHAILEASE', 'ESUN', 'MEGA', 'CHT', 'HIMX', 'SIMO',
        'NANYAPLAS', 'ELITEMAT', 'NANYATECH'],
      // Every Asia row past the 50 the perps and ADRs group shows (its 9 more wait for more letters).
      ASIA: fx.universeAssets.filter((a) => a.region === 'asia').map((a) => a.symbol),
      singapore: ['SE', 'GRAB', 'DBS', 'OCBC', 'UOB', 'SINGTEL', 'KEPPEL', 'SEMBCORP', 'STENG', 'WILMAR', 'SIA', 'SGXL', 'GENTING', 'CAPLAND', 'YZJ',
        'VENTURE', 'CITYDEV', 'THAIBEV', 'JARDINE', 'UOL', 'KARO', 'HKLAND', 'JARDINESEA', 'SEATRIUM', 'KLIC', 'SATS'],
      india: ['INFY', 'HDB', 'IBN'],
      krx: korea.filter((s) => !['EWY', 'CPNG'].includes(s)),
      '005930': ['SMSN'],
      nasdaq: ['NVDA', 'TSLA', 'AAPL', 'MSFT', 'AMZN', 'GOOGL', 'META', 'PLTR', 'COIN', 'HOOD', 'MSTR', 'INTC', 'AMD', 'NFLX', 'MU', 'SNDK', 'CRWV', 'HSDT',
        'SKHY', 'GRAB', 'PDD', 'JD', 'BIDU', 'LI', 'NTES', 'TCOM', 'BILI', 'FUTU', 'KARO', 'KLIC', 'HIMX', 'SIMO'],
    }
    for (const [q, symbols] of Object.entries(found)) {
      await user.clear(search) // leaves the search focused
      await user.paste(q) // one input event per query; typing re-filters every row per key and outran the timeout under load
      const shown = inListOrder(symbols)
      const capped = q === 'ASIA' ? [...shown.slice(0, 50), ...shown.slice(59)] : shown // at most 50 a group
      expect(hrefs(markets), q).toEqual(capped)
      expect(within(markets).queryByText('9 more, keep typing') != null, q).toBe(q === 'ASIA')
    }
    await user.type(search, '{Enter}') // the first match, in list order
    expect(currentPath()).toBe('/trade/NVDA')
  }, 45_000) // thirteen searches re-render the whole 245-row list: about 13 s alone, over 20 s on a loaded machine

  it('finds listings by region across the full universe in the picker below xl', async () => {
    const { user } = await openUniverse()
    const panel = await picker(user)
    await user.click(within(panel).getByRole('searchbox', { name: 'Search markets' }))
    await user.paste('korea') // one input event: typing re-filters the whole list per key
    expect(hrefs(panel)).toEqual(inListOrder(korea))
    expect(hrefs(panel).slice(0, 5)).toEqual(['/trade/SMSN', '/trade/SKHX', '/trade/HYUNDAI', '/trade/EWY', '/trade/SKHY']) // the perps' group first
    await user.keyboard('{Enter}')
    expect(currentPath()).toBe('/trade/SMSN')
    expect(await screen.findByRole('heading', { name: 'Samsung Electronics' })).toBeInTheDocument()
  }, 30_000) // the full list renders twice (the aside and the picker): 19.3 s under coverage on a loaded machine

  it('names the KRX session, the underlying and the listing-currency note for a KRW perp', async () => {
    const { user } = renderPage(<TradePage />, { route: '/trade/SMSN', path: '/trade/:symbol' })
    await screen.findByRole('heading', { name: 'Samsung Electronics' }) // the configured name, before the asset loads
    expect(await screen.findByText('KRX session open')).toHaveAttribute('title', expect.stringContaining('(KRX)'))
    expect(screen.queryByText(/US session/)).toBeNull()
    expect(screen.getByText('KRX 005930 common share, KRW→USD')).toBeInTheDocument()
    expect(screen.getByText(/including outside the regular KRX session/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Manual' }))
    expect(screen.getByText(/No funding accrual, dividends or split adjustment\. Regular KRX session closure does not stop paper trading\. Price is the USD perp mark: trade\.xyz's oracle converts the KRW listing to USD\. This app does no FX conversion\./)).toBeInTheDocument()
  })

  it('opens /trade/TSMC (any case) on TSM, as the API resolves the name', async () => {
    for (const route of ['/trade/TSMC', '/trade/tsmc']) {
      const { unmount } = renderPage(<TradePage />, { route, path: '/trade/:symbol' })
      await waitFor(() => expect(currentPath(), route).toBe('/trade/TSM'))
      expect(await screen.findByRole('heading', { name: 'TSMC (Taiwan Semiconductor)' })).toBeInTheDocument()
      unmount()
    }
  })

  it('keeps a USD-listed Asia perp on its NYSE session without an FX note', async () => {
    const { user } = renderPage(<TradePage />, { route: '/trade/TSM', path: '/trade/:symbol' })
    await screen.findByRole('heading', { name: 'TSMC (Taiwan Semiconductor)' }) // the configured name, before the asset loads
    expect(await screen.findByText('NYSE session closed')).toBeInTheDocument()
    expect(screen.getByText('NYSE: TSM ADS = 5 TWSE 2330 shares')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Manual' }))
    expect(screen.getByText(/Regular NYSE session closure does not stop paper trading\.$/)).toBeInTheDocument()
    expect(screen.queryByText(/FX conversion/)).toBeNull()
  })
})

describe('second-batch stock perps', () => {
  const cxmt = { ...stock, symbol: 'CXMT', pair: 'xyz:CXMT', name: 'CXMT (ChangXin Memory)', price: 8.4, session: 'closed', region: 'asia', sessionVenue: 'SSE', underlying: 'SSE STAR 688825 A-share, CNY→USD; early bars are pre-IPO implied prices', localCurrency: 'CNY' } as Asset
  const pltr = { ...stock, symbol: 'PLTR', pair: 'xyz:PLTR', name: 'Palantir', price: 190, region: 'us', sessionVenue: 'NYSE', underlying: 'Nasdaq: PLTR Class A common share', localCurrency: 'USD' } as Asset
  const kioxia = { ...stock, symbol: 'KIOXIA', pair: 'xyz:KIOXIA', name: 'Kioxia Holdings', price: 119, region: 'asia', sessionVenue: 'TSE', underlying: 'TSE 285A ordinary share, JPY→USD; bars before the 2026-09-28 3-for-1 split are divided by 3', localCurrency: 'JPY' } as Asset
  beforeEach(() => vi.mocked(api.assets).mockResolvedValue({ assets: [...fx.assets, pltr, cxmt, kioxia] }))

  it('groups the US names under US stocks and the Asia names under Asia', async () => {
    const { user } = renderPage(<TradePage />, { route: '/trade/BTC', path: '/trade/:symbol' })
    await screen.findByRole('heading', { name: 'Bitcoin' })
    const selector = await picker(user)
    await within(selector).findByRole('list', { name: 'Asia perps & US-listed ADRs' })
    expect(hrefs(within(selector).getByRole('list', { name: 'US stocks & indices' }))).toEqual(['/trade/PLTR'])
    expect(hrefs(within(selector).getByRole('list', { name: 'Asia perps & US-listed ADRs' }))).toEqual(['/trade/CXMT', '/trade/KIOXIA'])
    expect(within(selector).queryByRole('list', { name: 'Japan · TSE' })).toBeNull() // a perp on a TSE underlying is not a TSE quote feed
  })

  it('names the SSE session, the pre-IPO caveat and the offshore-yuan note for a CNY perp', async () => {
    const { user } = renderPage(<TradePage />, { route: '/trade/CXMT', path: '/trade/:symbol' })
    await screen.findByRole('heading', { name: 'CXMT (ChangXin Memory)' })
    // The heading can come from the configured listing before the assets read lands; the session comes with it.
    expect(await screen.findByText('SSE session closed')).toHaveAttribute('title', expect.stringContaining('(SSE)'))
    expect(screen.getByText(/early bars are pre-IPO implied prices/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Manual' }))
    expect(screen.getByText(/Regular SSE session closure does not stop paper trading\. Price is the USD perp mark: trade\.xyz's oracle converts the CNY listing to USD at the offshore USD\/CNH rate\./)).toBeInTheDocument()
  })

  it('states that Kioxia history is split-restated', async () => {
    renderPage(<TradePage />, { route: '/trade/KIOXIA', path: '/trade/:symbol' })
    await screen.findByRole('heading', { name: 'Kioxia Holdings' })
    expect(await screen.findByText(/bars before the 2026-09-28 3-for-1 split are divided by 3/)).toBeInTheDocument() // from the asset, after the heading
    expect(screen.getByText('TSE session closed')).toBeInTheDocument()
  })
})

describe('quote-feed presentation', () => {
  const dbs = fx.feedAsset('DBS', 'DBS Group')
  const hsdt = fx.feedAsset('HSDT', 'Solana Company')
  beforeEach(() => vi.mocked(api.assets).mockResolvedValue({ assets: [...fx.assets.map((a) => ({ ...a, region: null })), stock, hsdt, dbs] }))
  const openFeed = (symbol: string) => renderPage(<TradePage />, { route: `/trade/${symbol}`, path: '/trade/:symbol' })

  it('labels the Yahoo quote feed, its session and the last-close fills, with the FX rate', async () => {
    const derivatives = vi.spyOn(api, 'derivatives')
    const { user } = openFeed('DBS')
    await screen.findByRole('heading', { name: 'DBS Group' })
    await act(async () => { await Promise.resolve() })
    expect(screen.getByText('Quote feed')).toBeInTheDocument()
    expect(screen.getByText('SGX session closed')).toBeInTheDocument()
    expect(screen.getByText('SGX: D05 ordinary share, SGD→USD at Yahoo FX')).toBeInTheDocument()
    expect(screen.getByTitle(/Original SGD listing price/)).toHaveTextContent('SGD 78.56')
    expect(screen.getByText('vs prev. close')).toBeInTheDocument()
    expect(screen.getByText('Session high')).toBeInTheDocument()
    expect(screen.queryByText(/Hyperliquid perp/)).toBeNull()
    expect(derivatives).not.toHaveBeenCalled() // no perp context for a quote feed
    expect(FakeWebSocket.instances).toHaveLength(0) // polled API candles, not the Binance socket
    await user.click(screen.getByRole('button', { name: 'Manual' }))
    await user.type(screen.getByLabelText('Amount (USD)'), '100')
    expect(screen.getByText('DBS at $61.448 · last close')).toBeInTheDocument()
    const note = screen.getByText(/^Paper fills use the USD price with 2 bps slippage and 0\.1% fee/)
    expect(note).toHaveTextContent('Quote feed: prices follow SGX hours. Outside them, as now, paper fills use the last close (as of Oct 5, 20:00 UTC).')
    expect(note).toHaveTextContent('SGD→USD at Yahoo FX: 1 SGD = $0.7822 (as of Oct 6, 08:58 UTC).')
    expect(screen.queryByText(/No funding accrual/)).toBeNull()
  })

  it('names the configured listing and its venue before its quote is listed', async () => {
    vi.mocked(api.assets).mockResolvedValue({ assets: [stock] }) // the first read after a boot may not list every quote feed yet
    openFeed('BBCA')
    expect(await screen.findByText('Quote feed')).toBeInTheDocument()
    expect(screen.getByText('Quote feed').parentElement).toHaveTextContent('BBCA · BBCA.JK · Yahoo')
    expect(screen.getByText('IDX session unknown')).toBeInTheDocument()
    expect(screen.getByText(/^Quote feed: prices follow IDX hours; outside them paper fills use the last close\. IDR→USD at Yahoo FX\.$/)).toBeInTheDocument()
    expect(screen.queryByText(/Yahoo · Yahoo|Exchange session|exchange hours/)).toBeNull()
  })

  it("gives Yahoo's market state where the venue has no verified calendar (IDX, Bursa, HOSE)", async () => {
    const bbca = fx.feedAsset('BBCA', 'Bank Central Asia', 0, { session: null, quoteFeed: { ...fx.feedAsset('BBCA', 'Bank Central Asia').quoteFeed!, marketState: 'open' } })
    vi.mocked(api.assets).mockResolvedValue({ assets: [stock, bbca] })
    openFeed('BBCA')
    await screen.findByRole('heading', { name: 'Bank Central Asia' })
    expect(await screen.findByText('IDX session open · per Yahoo')).toHaveAttribute('title', "No verified IDX calendar here; the market state follows Yahoo's trading period.")
    expect(screen.queryByText('IDX session unknown')).toBeNull()
  })

  it('says delayed with the last trade time while the listing is open, groups HSDT under US and the local listings by venue', async () => {
    vi.mocked(api.assets).mockResolvedValue({
      assets: [stock, hsdt, { ...dbs, session: 'open', quoteFeed: { ...dbs.quoteFeed!, marketState: 'open', asOf: '2026-10-13T01:12:05Z' } }],
    })
    const { user } = openFeed('DBS')
    await screen.findByRole('heading', { name: 'DBS Group' })
    const selector = await picker(user)
    const sgx = await within(selector).findByRole('list', { name: 'Singapore · SGX' })
    expect(hrefs(within(selector).getByRole('list', { name: 'US stocks & indices' }))).toEqual(['/trade/AAPL', '/trade/HSDT'])
    expect(hrefs(sgx)).toEqual(['/trade/DBS'])
    expect(sgx.previousElementSibling).toHaveTextContent('Singapore · SGXdelayed') // the venue label and its muted tag
    expect(within(selector).getAllByText('delayed')).toHaveLength(1)
    await user.keyboard('{Escape}')
    await user.click(screen.getByRole('button', { name: 'Manual' }))
    await user.type(screen.getByLabelText('Amount (USD)'), '100')
    // Yahoo's Asian quotes reach us 10–20 minutes late: the ticket never calls them live.
    expect(screen.getByText('DBS at $61.448 · delayed · last trade 01:12 UTC')).toBeInTheDocument()
    expect(screen.queryByText(/DBS at .* · live/)).toBeNull()
    expect(screen.getByText(/^Paper fills use the USD price/)).toHaveTextContent(
      "It is open now, so fills use Yahoo's latest trade, which is delayed (last trade Oct 13, 01:12 UTC); outside its hours they use the last close.",
    )
  })

  it("calls the price the last quote, not the last close, when Yahoo gives no market state", async () => {
    vi.mocked(api.assets).mockResolvedValue({ assets: [stock, { ...dbs, quoteFeed: { ...dbs.quoteFeed!, marketState: 'unknown' } }] })
    const { user } = openFeed('DBS')
    await screen.findByRole('heading', { name: 'DBS Group' })
    await user.click(screen.getByRole('button', { name: 'Manual' }))
    await user.type(screen.getByLabelText('Amount (USD)'), '100')
    expect(await screen.findByText('DBS at $61.448 · last quote')).toBeInTheDocument()
  })
})

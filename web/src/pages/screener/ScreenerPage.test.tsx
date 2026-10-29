import { act, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError } from '../../lib/api'
import type { ScreenerPage as ScreenerPageData, ScreenerQuery, ScreenerQuote, ScreenerRow } from '../../lib/types'
import * as fx from '../../test/fixtures'
import { currentPath, renderPage, stubSession } from '../../test/utils'
import { fmtDuration, LIVE_METHOD, ScreenerPage } from './ScreenerPage'

const row = (ticker: string) => screen.getByRole('row', { name: new RegExp(`^${ticker.replace('.', '\\.')} `) })
const lastQuery = (spy: { mock: { calls: unknown[][] } }) => spy.mock.calls.at(-1)![0] as ScreenerQuery
const order = () => screen.getAllByRole('row').slice(1).map((r) => r.getAttribute('aria-label')!.split(' ')[0])
const minute = () => act(() => vi.advanceTimersByTimeAsync(60_000))

function setup(route = '/screener', page = fx.screenerPage()) {
  stubSession()
  const screener = vi.spyOn(api, 'screener').mockResolvedValue(page)
  const quotes = vi.spyOn(api, 'screenerQuotes').mockImplementation(async (tickers) => ({
    rows: tickers.map((t) => page.rows.find((r) => r.ticker === t)!).filter(Boolean), unknown: [], asOf: page.asOf, lastCompletedAt: page.lastCompletedAt, pollSeconds: 60,
  }))
  const r = renderPage(<ScreenerPage />, { route, path: '/screener', routes: { '/': <p>agent page</p>, '/trade/:symbol': <p>trade page</p> } })
  return { ...r, screener, quotes }
}

// The clock is fixed at Wednesday 07:10 UTC (the fixtures' time): freshness never depends on when the tests run. Only
// intervals and Date are fake, so Testing Library's own timeouts and the search debounce stay real.
let hidden = false
beforeEach(() => {
  vi.useFakeTimers({ toFake: ['Date', 'setInterval', 'clearInterval'] })
  vi.setSystemTime(new Date(fx.SCREENER_NOW))
  hidden = false
  Object.defineProperty(document, 'hidden', { configurable: true, get: () => hidden })
})
afterEach(() => {
  vi.useRealTimers()
  Reflect.deleteProperty(document, 'hidden')
})

describe('ScreenerPage', () => {
  it('lists each stock with local and USD price, distance from the 200WMA, direction, RSI, ATH drawdown and update', async () => {
    const { screener, user } = setup()
    expect(screen.getByRole('heading', { level: 1, name: 'Asia screener' })).toBeInTheDocument()
    const samsung = within(await screen.findByRole('row', { name: /^005930\.KS / }))
    expect(screener).toHaveBeenCalledWith({ filter: 'all', market: undefined, q: undefined, sort: 'pctVsSma', limit: 50 }, expect.any(AbortSignal))
    expect(samsung.getByText('Samsung Electronics Co., Ltd.')).toBeInTheDocument()
    expect(samsung.getByText('KR')).toBeInTheDocument()
    expect(samsung.getByText('KOSPI 200')).toBeInTheDocument()
    expect(samsung.getByText('₩272,000')).toBeInTheDocument()
    expect(samsung.getByText('$203.22')).toHaveAttribute('title', expect.stringMatching(/^KRWUSD=X 0\.000747133 · /))
    expect(samsung.getByText('+160.4%')).toHaveClass('text-up')
    expect(samsung.getByText('Deepening')).toBeInTheDocument()
    expect(samsung.getByText('↓')).toBeInTheDocument()
    expect(samsung.getByText('54.2')).toBeInTheDocument()
    expect(samsung.getByText('-27.4%')).toBeInTheDocument()
    expect(samsung.getByText('ATH wk of Jun 15, 2026')).toBeInTheDocument()
    // Updated: the quote's last trade, labelled with its delay (never "live"), and the session now.
    expect(samsung.getByText('delayed · last trade 06:30 UTC')).toHaveAttribute('title', 'Yahoo Finance · about 20 min behind the exchange (measured) · read 06:55 UTC, read again at the next open')
    expect(samsung.getByText('Closed · Oct 14')).toBeInTheDocument()
    expect(samsung.getByText('₩272,000')).toHaveAttribute('title', expect.stringMatching(/^delayed · last trade 06:30 UTC · Yahoo Finance/))
    expect(row('005930.KS')).not.toHaveTextContent(/live|Stale/i)

    const bbca = within(row('BBCA.JK'))
    expect(bbca.getByText('-29.3%')).toHaveClass('text-down')
    expect(bbca.getByText('Recovering')).toBeInTheDocument()
    expect(within(row('0000.T')).getByText('24.6')).toHaveClass('text-down') // oversold
    expect(within(row('D01.SI')).getByText('$3.15')).toBeInTheDocument()
    expect(within(row('D01.SI')).getByText('USD-quoted')).toBeInTheDocument()

    expect(screen.getByText(/Not financial advice\./)).toBeInTheDocument()
    expect(screen.getByText('Simulated · not financial advice')).toBeInTheDocument()
    expect(screen.getByText(/^Data as of Oct 13, 2026, 3:52 AM/)).toHaveTextContent( // the viewer's zone (tests run in Los Angeles)
      'Data as of Oct 13, 2026, 3:52 AM · last run completed Oct 13, 2026, 3:59 AM (750 read, 30 unavailable, 5 failed; took 14 min 12 s, 794 Yahoo calls) · next run Oct 14, 2026, 3:45 AM',
    )
    expect(screen.getByText(/^Prices: each listing's latest regular-market quote from Yahoo Finance/)).toHaveTextContent(
      /read by the latest run\. FX: Yahoo Finance \{CCY\}USD=X: the rate the quote feeds hold now/)
    expect(screen.getByText(/^Prices: /)).toHaveTextContent(`${LIVE_METHOD} Runs: every weekday at 10:45 UTC, after the last Asian close (SET's closing auction ends about 09:40 UTC), and at boot`)
    expect(screen.getByText(/^North Asia and Southeast Asia index stocks/)).toHaveTextContent(
      "North Asia and Southeast Asia index stocks against their 200-week moving average, with the 14-week RSI and the drawdown from each one's all-time high. " +
        "Prices are each listing's latest quote, refreshed through the trading day; the averages behind them are recomputed every weekday after the Asian close.",
    )
    expect(document.body).not.toHaveTextContent(/Saturday/)
    await user.click(screen.getByText('Index lists and sources'))
    const lists = screen.getByText('Index lists and sources').closest('details')!
    expect(within(lists).getByText('Nikkei 225').closest('li')).toHaveTextContent('Nikkei 225 · 225 stocks · as of Oct 1, 2026 · official')
    expect(within(lists).getAllByRole('link', { name: 'source' })[0]).toHaveAttribute('href', fx.screenerPage().source.lists[0].sources[0])
    expect(screen.getByText('Showing 9 of 9')).toBeInTheDocument()
  })

  it('labels rows without values: pending, no data feed, insufficient history, and a stale failed refresh', async () => {
    setup()
    await screen.findByRole('row', { name: /^005930\.KS / })
    expect(within(row('1332.T')).getByText('Not screened yet')).toBeInTheDocument()
    expect(within(row('1332.T')).getByText('Pending')).toBeInTheDocument()
    expect(within(row('SM.PS')).getByText('Unavailable (no data feed)')).toHaveAttribute('title', 'Yahoo serves no data for this ticker')
    expect(within(row('SM.PS')).getByText('No data feed')).toBeInTheDocument()
    const kioxia = within(row('285A.T'))
    expect(kioxia.getByText('n/a, insufficient history (95 wks)')).toHaveAttribute('title', '95 weeks of history; the 200-week average needs 200')
    expect(kioxia.getByText('-50.1%')).toBeInTheDocument() // the high and RSI are still measured
    const tencent = within(row('0700.HK'))
    expect(tencent.getByText('+0.6%')).toBeInTheDocument() // the live price on the averages kept from the run before
    expect(tencent.getByText('delayed · last trade 06:51 UTC')).toBeInTheDocument()
    expect(tencent.getByText('Old averages')).toHaveAttribute('title', 'Last refresh failed: rate limited by Yahoo; the averages are from Oct 12, 2026')
    expect(tencent.queryByText('Stale')).toBeNull() // its quote is fresh
    expect(row('0700.HK')).toHaveClass('opacity-75')
  })

  it('a row with no live quote keeps the run’s date, and a failed run’s values read Stale as before', async () => {
    const run = fx.screenerRow({ ticker: '7203.T', code: '7203', name: 'Toyota Motor Corporation', listName: 'TOYOTA MOTOR CORP.', market: 'JP', currency: 'JPY',
      price: 2930, quote: null, status: 'failed', statusNote: 'rate limited by Yahoo', updatedAt: '2026-10-12T10:51:00Z' })
    setup('/screener', fx.screenerPage({ rows: [run] }))
    const toyota = within(await screen.findByRole('row', { name: /^7203\.T / }))
    expect(toyota.getByText('Oct 12, 2026')).toBeInTheDocument()
    expect(toyota.getByText('Stale')).toHaveAttribute('title', 'Last refresh failed: rate limited by Yahoo; values from Oct 12, 2026')
    expect(toyota.getByText('¥2,930')).not.toHaveAttribute('title')
    expect(toyota.queryByText(/delayed|real-time/)).toBeNull()
  })

  it('withholds the drawdown of a high over 100× the price (a reverse-split history)', async () => {
    const split = fx.screenerRow({ ticker: '9999.HK', code: '09999', name: 'Split Holdings', listName: 'SPLIT', market: 'HK', currency: 'HKD',
      price: 1.2, priceUsd: 0.15, ath: 5400, athAt: '2007-10-29', drawdownPct: -99.98, tradableSymbol: null, tradeVia: null, tradeNote: null })
    setup('/screener', fx.screenerPage({ rows: [split, fx.screenerRow()] }))
    const cell = within(await screen.findByRole('row', { name: /^9999\.HK / })).getByText('n/a')
    expect(cell).toHaveAttribute('title', expect.stringMatching(/^All-time high unavailable: the split-adjusted high in this history is over 100× the price/))
    expect(row('9999.HK')).not.toHaveTextContent(/99\.98|-100|Oct 29, 2007/)
    expect(within(row('005930.KS')).getByText('-27.4%')).toBeInTheDocument()
  })

  it('explains the null high the API sends for such a row, and keeps a dash for a row not read yet', async () => {
    const split = fx.screenerRow({ ticker: '9999.HK', code: '09999', name: 'Split Holdings', listName: 'SPLIT', market: 'HK', currency: 'HKD',
      price: 1.2, priceUsd: 0.15, ath: null, athAt: null, drawdownPct: null, tradableSymbol: null, tradeVia: null, tradeNote: null })
    setup('/screener', fx.screenerPage({ rows: [split, fx.screenerRows[7]] }))
    expect(within(await screen.findByRole('row', { name: /^9999\.HK / })).getByText('n/a')).toHaveAttribute('title', expect.stringMatching(/^All-time high unavailable/))
    expect(within(row('1332.T')).queryByText('n/a')).toBeNull() // pending: nothing computed, nothing withheld
  })

  it('offers a drawdown alert and Trade when the stock trades here, "via ADR" when through its US listing', async () => {
    const { user } = setup()
    const samsung = within(await screen.findByRole('row', { name: /^005930\.KS / }))
    expect(samsung.getByRole('button', { name: 'Set drawdown alert on SMSN' })).toBeEnabled()
    // The stock cell and the Trade button both open the Trade page.
    expect(samsung.getAllByRole('link', { name: 'Trade SMSN' }).map((l) => l.getAttribute('href'))).toEqual(['/trade/SMSN', '/trade/SMSN'])
    expect(samsung.queryByRole('button', { name: /^View/ })).toBeNull()
    const tsmc = within(row('2330.TW'))
    const [cell, button] = tsmc.getAllByRole('link', { name: 'Trade TSM (via ADR)' })
    expect(button).toHaveAttribute('title', 'NYSE: TSM ADS = 5 TWSE 2330 shares')
    expect(cell).toHaveTextContent('2330.TWTaiwan Semiconductor Manufacturing Company Limited')
    expect(cell.closest('td')).toHaveClass('sticky', 'left-0')
    expect(tsmc.getByText('via ADR')).toBeInTheDocument()
    expect(tsmc.getByRole('button', { name: 'Set drawdown alert on TSM' })).toBeEnabled()
    await user.click(button)
    expect(currentPath()).toBe('/trade/TSM')
  })

  it('opens a listed stock\'s Trade page from its ticker and name, and keeps its drawdown alert', async () => {
    const hitachi = fx.screenerRow({ ticker: '6501.T', code: '6501', name: 'Hitachi, Ltd.', listName: 'HITACHI, LTD.', market: 'JP', currency: 'JPY',
      tradableSymbol: '6501.T', tradeVia: 'listing', tradeNote: 'TSE: 6501 (Nikkei 225), JPY→USD at Yahoo FX' })
    const { user } = setup('/screener', fx.screenerPage({ rows: [hitachi, ...fx.screenerRows] }))
    const r = within(await screen.findByRole('row', { name: /^6501\.T / }))
    const [cell, button] = r.getAllByRole('link', { name: 'Trade 6501.T' })
    expect(cell).toHaveAttribute('href', '/trade/6501.T')
    expect(cell).toHaveClass('focus-visible:ring-2')
    expect(cell).toHaveTextContent('6501.THitachi, Ltd.')
    expect(button).toHaveAttribute('href', '/trade/6501.T')
    expect(r.getByRole('button', { name: 'Set drawdown alert on 6501.T' })).toBeEnabled()
    expect(r.getByText('6501.T').closest('td')).toHaveTextContent(/^6501\.THitachi, Ltd\.$/)
    await user.click(cell)
    expect(currentPath()).toBe('/trade/6501.T')
  })

  it('says why a row is research only on its View button', async () => {
    const ph = fx.screenerRow({ ticker: 'SM.PS', code: 'SM', name: 'SM Investments', listName: 'SM Investments', market: 'PH', currency: 'PHP', status: 'unavailable',
      tradableSymbol: null, tradeVia: null, tradeNote: 'No keyless Yahoo data for this listing (Yahoo answered 404 on 2026-10-13)' })
    const thin = fx.screenerRow({ ticker: '268280.KS', code: '268280', name: 'Miwon Specialty Chemical Co., Ltd.', listName: 'MIWON SPECIALTY CHEMICAL', market: 'KR',
      currency: 'KRW', tradableSymbol: null, tradeVia: null, tradeNote: 'Trades under $1M a day (30-day mean $127k on 2026-10-13).' })
    setup('/screener', fx.screenerPage({ rows: [ph, thin] }))
    const view = within(await screen.findByRole('row', { name: /^SM\.PS / })).getByRole('button', { name: 'View SM.PS with the agent' })
    expect(view).toHaveAttribute('title', 'Research only: No keyless Yahoo data for this listing (Yahoo answered 404 on 2026-10-13). Ask the agent for an analysis.')
    expect(view).toHaveAttribute('aria-description', 'Research only: No keyless Yahoo data for this listing (Yahoo answered 404 on 2026-10-13)')
    expect(within(screen.getByRole('row', { name: /^268280\.KS / })).getByRole('button', { name: /^View/ })).toHaveAttribute(
      'title', 'Research only: Trades under $1M a day (30-day mean $127k on 2026-10-13). Ask the agent for an analysis.') // one full stop
    expect(within(screen.getByRole('row', { name: /^SM\.PS / })).queryByRole('link')).toBeNull() // no Trade page to open
  })

  it('opens the agent with an analysis prompt for a research-only stock', async () => {
    const { user } = setup()
    const bbca = within(await screen.findByRole('row', { name: /^BBCA\.JK / }))
    expect(bbca.queryByRole('link', { name: /Trade/ })).toBeNull()
    expect(bbca.getByRole('button', { name: 'View BBCA.JK with the agent' })).toHaveAttribute('title', 'Research only: ask the agent for an analysis') // no note: as before
    await user.click(bbca.getByRole('button', { name: 'View BBCA.JK with the agent' }))
    expect(await screen.findByText('agent page')).toBeInTheDocument()
    expect(currentPath()).toBe('/')
  })

  it('shows each count the API sends, a 0 included', async () => {
    setup('/screener', fx.screenerPage({ counts: { all: 785, below: 0, deep: 0, distressed: 0, approaching: 2, oversold: 0 } })) // as the API sends it
    await screen.findByRole('row', { name: /^005930\.KS / })
    const tabs = screen.getByRole('group', { name: 'Screen' })
    for (const name of ['All 785', 'Below 0', 'Deep value 0', 'Distressed 0', 'Approaching 2', 'Oversold 0'])
      expect(within(tabs).getByRole('button', { name })).toBeInTheDocument()
  })

  it('falls back to 0 for a filter an older response leaves out of counts', async () => {
    setup('/screener', fx.screenerPage({ counts: { all: 785, approaching: 2 } as ScreenerPageData['counts'] })) // before the API listed every filter
    await screen.findByRole('row', { name: /^005930\.KS / })
    const tabs = screen.getByRole('group', { name: 'Screen' })
    for (const name of ['All 785', 'Below 0', 'Deep value 0', 'Distressed 0', 'Approaching 2', 'Oversold 0'])
      expect(within(tabs).getByRole('button', { name })).toBeInTheDocument()
  })

  it('switches filter tabs, market and sort through the URL, with each tab’s count', async () => {
    const { user, screener } = setup()
    await screen.findByRole('row', { name: /^005930\.KS / })
    const tabs = screen.getByRole('group', { name: 'Screen' })
    expect(within(tabs).getByRole('button', { name: 'All 9' })).toHaveAttribute('aria-pressed', 'true')
    expect(within(tabs).getByRole('button', { name: 'Deep value 1' })).toBeInTheDocument()
    expect(within(tabs).getByRole('button', { name: 'Oversold 1' })).toBeInTheDocument()

    await user.click(within(tabs).getByRole('button', { name: 'Deep value 1' }))
    await waitFor(() => expect(lastQuery(screener)).toMatchObject({ filter: 'deep' }))
    expect(currentPath()).toBe('/screener?filter=deep')

    await user.selectOptions(screen.getByRole('combobox', { name: 'Market' }), 'South Korea (200)')
    await waitFor(() => expect(lastQuery(screener)).toMatchObject({ filter: 'deep', market: 'KR' }))
    expect(currentPath()).toBe('/screener?filter=deep&market=KR')

    const rsi = screen.getByRole('columnheader', { name: /RSI 14w/ })
    expect(rsi).toHaveAttribute('aria-sort', 'none')
    expect(screen.getByRole('columnheader', { name: /vs 200WMA/ })).toHaveAttribute('aria-sort', 'ascending')
    await user.click(within(rsi).getByRole('button'))
    await waitFor(() => expect(lastQuery(screener)).toMatchObject({ sort: 'rsi' }))
    await user.click(within(rsi).getByRole('button'))
    await waitFor(() => expect(lastQuery(screener)).toMatchObject({ sort: '-rsi' }))
    expect(rsi).toHaveAttribute('aria-sort', 'descending')
    await user.click(within(screen.getByRole('columnheader', { name: /Updated/ })).getByRole('button'))
    await waitFor(() => expect(lastQuery(screener)).toMatchObject({ sort: '-updated' })) // latest first
  })

  it('searches after typing pauses', async () => {
    const { user, screener } = setup()
    await screen.findByRole('row', { name: /^005930\.KS / })
    const calls = screener.mock.calls.length
    await user.type(screen.getByRole('searchbox', { name: 'Search stocks' }), 'toyota')
    await waitFor(() => expect(lastQuery(screener)).toMatchObject({ q: 'toyota' }))
    expect(screener.mock.calls.length).toBe(calls + 1) // one request, not one per key
    expect(currentPath()).toBe('/screener?q=toyota')
  })

  it('reads its query from the URL, ignoring values it does not know', () => {
    const { screener } = setup('/screener?filter=oversold&market=vn&q=bank&sort=-drawdown')
    expect(screener).toHaveBeenCalledWith({ filter: 'oversold', market: 'VN', q: 'bank', sort: '-drawdown', limit: 50 }, expect.any(AbortSignal))
    expect(screen.getByRole('searchbox', { name: 'Search stocks' })).toHaveValue('bank')
  })

  it('falls back to defaults for unknown query values', () => {
    const { screener } = setup('/screener?filter=cheap&market=US&sort=price;drop')
    expect(screener).toHaveBeenCalledWith({ filter: 'all', market: undefined, q: undefined, sort: 'pctVsSma', limit: 50 }, expect.any(AbortSignal))
  })

  it('loads the next page with the cursor and appends it', async () => {
    stubSession()
    const first = fx.screenerPage({ rows: fx.screenerRows.slice(0, 3), nextCursor: 'c1', total: 9 })
    const second = fx.screenerPage({ rows: fx.screenerRows.slice(3), nextCursor: null, total: 9 })
    const screener = vi.spyOn(api, 'screener').mockResolvedValueOnce(first).mockResolvedValueOnce(second)
    const { user } = renderPage(<ScreenerPage />, { route: '/screener', path: '/screener' })
    expect(await screen.findByText('Showing 3 of 9')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Load more' }))
    expect(await screen.findByText('Showing 9 of 9')).toBeInTheDocument()
    expect(screener).toHaveBeenLastCalledWith(expect.objectContaining({ cursor: 'c1', sort: 'pctVsSma', limit: 50 }))
    expect(screen.queryByRole('button', { name: 'Load more' })).toBeNull()
    expect(screen.getAllByRole('row')).toHaveLength(10) // header + 9
  })

  it('says when a band is empty, and when a search matches nothing', async () => {
    stubSession()
    vi.spyOn(api, 'screener').mockResolvedValue(fx.screenerPage({ rows: [], total: 0 }))
    renderPage(<ScreenerPage />, { route: '/screener?filter=distressed', path: '/screener' })
    expect(await screen.findByText('No stocks in this band right now.')).toBeInTheDocument()
  })

  it('explains a missing, running or failed run', async () => {
    stubSession()
    const screener = vi.spyOn(api, 'screener').mockResolvedValue(fx.screenerPage({ run: null, asOf: null, lastCompletedAt: null }))
    const { unmount } = renderPage(<ScreenerPage />, { route: '/screener', path: '/screener' })
    expect(await screen.findByRole('status')).toHaveTextContent("The first run hasn't started yet.")
    expect(screen.getByText(/^No data yet · next run/)).toBeInTheDocument()
    unmount()
    const running = { ...fx.screenerPage().run!, status: 'running' as const, done: 120, finishedAt: null, durationSeconds: 125, yahooCalls: 121 }
    screener.mockResolvedValue(fx.screenerPage({ run: running }))
    const second = renderPage(<ScreenerPage />, { route: '/screener', path: '/screener' })
    expect(await screen.findByRole('status')).toHaveTextContent('The averages are being recomputed: 120 of 785 read. Rows update as it goes.')
    expect(screen.getByText(/^Data as of/)).toHaveTextContent('last run running Oct 13, 2026, 3:45 AM (750 read, 30 unavailable, 5 failed; running for 2 min 5 s, 121 Yahoo calls)')
    second.unmount()
    screener.mockResolvedValue(fx.screenerPage({ run: { ...running, status: 'failed', error: 'Yahoo unavailable: 20 tickers failed in a row' } }))
    renderPage(<ScreenerPage />, { route: '/screener', path: '/screener' })
    expect(await screen.findByRole('status')).toHaveTextContent('The last run stopped early (Yahoo unavailable: 20 tickers failed in a row).')
  })

  it('shows an error with a retry', async () => {
    stubSession()
    const screener = vi
      .spyOn(api, 'screener')
      .mockRejectedValueOnce(new ApiError(502, 'upstream', 'Market data is unavailable right now. Try again shortly.'))
      .mockResolvedValue(fx.screenerPage())
    const { user } = renderPage(<ScreenerPage />, { route: '/screener', path: '/screener' })
    expect(await screen.findByRole('heading', { name: "Couldn't load the screener." })).toBeInTheDocument()
    expect(screen.getByText('Market data is unavailable right now. Try again shortly.')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Try again' }))
    expect(await screen.findByRole('row', { name: /^005930\.KS / })).toBeInTheDocument()
    expect(screener).toHaveBeenCalledTimes(2)
  })
})

describe('ScreenerPage · live prices', () => {
  const jp = (ticker: string, over: Partial<ScreenerRow> = {}) =>
    fx.screenerRow({ ticker, code: ticker.slice(0, 4), name: `Sample ${ticker}`, listName: `SAMPLE ${ticker}`, market: 'JP', currency: 'JPY', price: 2930, ...over })

  it('labels each quote from its own fields: delayed with its last trade, real-time only at a 0-minute delay, never "live"', async () => {
    const realtime = jp('7203.T', { quote: fx.screenerQuote({ source: 'exchange-feed', delayMinutes: 0, delayBasis: 'stated', lastTradeAt: '2026-10-14T06:29:58Z' }) })
    const unknown = jp('9432.T', { quote: fx.screenerQuote({ delayMinutes: null, delayBasis: null }) })
    setup('/screener', fx.screenerPage({ rows: [fx.screenerRows[0], realtime, unknown] }))
    const bbca = within(await screen.findByRole('row', { name: /^BBCA\.JK / }))
    expect(bbca.getByText('delayed · last trade 06:58 UTC')).toHaveAttribute('title', 'Yahoo Finance · about 10 min behind the exchange (measured) · read 07:00 UTC, re-read every 15 min')
    expect(bbca.getByText('Open · Oct 14')).toBeInTheDocument()
    expect(within(row('7203.T')).getByText('real-time · 06:29:58 UTC')).toHaveAttribute('title', 'exchange-feed · real-time · read 06:55 UTC, read again at the next open')
    expect(within(row('9432.T')).getByText('delayed · last trade 06:30 UTC')).toHaveAttribute('title', expect.stringContaining('· delay unknown ·'))
    expect(screen.getByRole('table')).not.toHaveTextContent(/\blive\b/i)
  })

  it('labels a real-time source’s quote by the API’s label, SGX’s snapshot about N min behind, and a fallback delayed', async () => {
    const kr = (ticker: string, quote: Partial<ScreenerQuote>) =>
      fx.screenerRow({ ticker, code: ticker.slice(0, 6), name: `Sample ${ticker}`, listName: `SAMPLE ${ticker}`, quote: fx.screenerQuote(quote) })
    const naver = kr('000660.KS', { source: 'naver', class: 'realtime', session: 'open', lastTradeAt: '2026-10-14T06:29:58Z', readAt: '2026-10-14T06:30:00Z',
      delaySeconds: 2, delayMinutes: 0.03, delayBasis: 'measured', timeBasis: 'volume', refreshSeconds: 10, label: 'real-time · 06:29:58 UTC' })
    const sgx = kr('D05.SI', { source: 'sgx', class: 'near-realtime', session: 'open', lastTradeAt: '2026-10-14T06:28:00Z', readAt: '2026-10-14T06:29:39Z',
      delaySeconds: 99, delayMinutes: 1.65, delayBasis: 'measured', timeBasis: 'snapshot', refreshSeconds: 30, label: 'about 2 min behind · 06:28 UTC' })
    const back = kr('009150.KS', { source: 'yahoo', class: 'delayed', session: 'open', lastTradeAt: '2026-10-14T06:10:00Z', delaySeconds: 1200, timeBasis: 'trade',
      label: 'delayed · last trade 06:10 UTC' })
    setup('/screener', fx.screenerPage({ rows: [naver, sgx, back] }))
    expect(within(await screen.findByRole('row', { name: /^000660\.KS / })).getByText('real-time · 06:29:58 UTC')).toHaveAttribute('title',
      'Naver Finance · real-time (2 s behind) · read 06:30 UTC, re-read every 10 s · the source gives no trade time: the time is the first read that showed the current volume')
    expect(within(row('D05.SI')).getByText('about 2 min behind · 06:28 UTC')).toHaveAttribute('title',
      "SGX · about 2 min behind the exchange (measured) · read 06:29 UTC, re-read every 30 s · the time is the source's snapshot minute, shared by every stock in it")
    expect(within(row('009150.KS')).getByText('delayed · last trade 06:10 UTC')).toBeInTheDocument()
    expect(screen.getByRole('table')).not.toHaveTextContent(/\blive\b/i)
  })

  it('marks a quote older than its tier Stale, and one that passes its stale time while the page is open', async () => {
    const { quotes } = setup()
    const dfi = within(await screen.findByRole('row', { name: /^D01\.SI / }))
    expect(dfi.getByText('Stale')).toHaveAttribute('title',
      'No newer quote since 06:35 UTC (re-read every 15 min): the price and the columns from it may be out of date. Open · Oct 14.')
    expect(dfi.getByText('delayed · last trade 06:24 UTC')).toHaveClass('text-muted')
    expect(row('D01.SI')).toHaveClass('opacity-75')
    // The badge takes the session line's place and the phone's note sits on the USD line: a stale row is no taller.
    expect(dfi.getByText('Stale').parentElement).toHaveTextContent(/^StaleOct 14$/)
    expect(dfi.queryByText('Open · Oct 14')).toBeNull()
    expect(dfi.getByText('stale quote')).toHaveClass('md:hidden') // beside the price on a phone, where Updated is a scroll away
    expect(dfi.getByText('stale quote').parentElement).toHaveTextContent(/^USD-quotedstale quote$/)
    expect(row('BBCA.JK')).not.toHaveClass('opacity-75')
    expect(within(row('BBCA.JK')).queryByText('stale quote')).toBeNull()
    const lines = () => within(row('BBCA.JK')).getByText(/^delayed · last trade/).parentElement!.children.length
    const fresh = lines()
    expect(within(row('BBCA.JK')).getByText('Open · Oct 14')).toBeInTheDocument()
    quotes.mockRejectedValue(new ApiError(0, 'network', 'The API could not be reached.'))
    for (let i = 0; i < 7; i++) await minute() // 07:17:00, 4 s before BBCA's stale time (its next read was due at 07:15:04)
    expect(within(row('BBCA.JK')).queryByText('Stale')).toBeNull()
    await minute() // 07:18: nothing newer came
    expect(quotes).toHaveBeenCalledTimes(8)
    expect(within(row('BBCA.JK')).getByText('Stale')).toBeInTheDocument()
    expect(row('BBCA.JK')).toHaveClass('opacity-75')
    expect(lines()).toBe(fresh) // the same lines as when fresh: nothing below it moves
    expect(screen.getByText(/^The API could not be reached\./)).toHaveTextContent(
      'The API could not be reached. Retrying every minute; old rows show Stale.')
    quotes.mockImplementation(async () => ({ rows: [{ ...fx.screenerRows[0], quote: { ...fx.screenerRows[0].quote!, staleAt: '2026-10-14T07:34:00Z' } }],
      unknown: [], asOf: null, lastCompletedAt: null, pollSeconds: 60 }))
    await minute()
    expect(within(row('BBCA.JK')).queryByText('Stale')).toBeNull() // read again: fresh
    expect(within(row('BBCA.JK')).getByText('Open · Oct 14')).toBeInTheDocument()
    expect(lines()).toBe(fresh)
    expect(screen.getByText(/^Prices refresh every minute/)).toBeInTheDocument()
  })

  it('refreshes the loaded rows in place every minute, keeping their order until the reader re-sorts', async () => {
    const page = fx.screenerPage()
    const { quotes, screener, user } = setup('/screener', page)
    await screen.findByRole('row', { name: /^BBCA\.JK / })
    const before = order()
    expect(screen.getByText(/^Prices refresh every minute/)).toHaveTextContent('Prices refresh every minute, in place; the order is from 07:10 UTC.')
    // 0000.T jumps past BBCA.JK: re-sorted it would move up, but it stays where the reader saw it.
    const [bbca0, deep0] = page.rows
    const deep = { ...deep0, price: 2600, pctVsSma: -23.53, quote: { ...deep0.quote!, price: 2600 } }
    const bbca = { ...bbca0, price: 6150, pctVsSma: -28.68, quote: { ...bbca0.quote!, price: 6150, lastTradeAt: '2026-10-14T07:00:12Z' } }
    quotes.mockResolvedValue({ rows: [bbca, deep], unknown: [], asOf: page.asOf, lastCompletedAt: page.lastCompletedAt, pollSeconds: 60 })
    expect(quotes).not.toHaveBeenCalled() // the page itself was just read
    await minute()
    expect(quotes).toHaveBeenCalledTimes(1)
    expect(quotes.mock.calls[0][0]).toEqual(page.rows.filter((r) => r.ticker !== 'SM.PS').map((r) => r.ticker)) // every loaded row with data, in order
    expect(within(row('0000.T')).getByText('-23.5%')).toBeInTheDocument()
    expect(within(row('0000.T')).getByText('¥2,600')).toBeInTheDocument()
    expect(within(row('BBCA.JK')).getByText('delayed · last trade 07:00 UTC')).toBeInTheDocument()
    expect(order()).toEqual(before)
    expect(screen.getByText(/^Prices moved since/)).toHaveTextContent('Prices moved since 07:10 UTC: the order and the tab counts are from then.')
    expect(screener).toHaveBeenCalledTimes(1)

    screener.mockResolvedValue({ ...page, rows: [bbca, deep, ...page.rows.slice(2)] })
    await user.click(screen.getByRole('button', { name: 'Re-sort' }))
    await waitFor(() => expect(order().slice(0, 2)).toEqual(['BBCA.JK', '0000.T']))
    expect(lastQuery(screener)).toMatchObject({ filter: 'all', sort: 'pctVsSma', limit: 50 })
    expect(screen.getByText(/^Prices refresh every minute/)).toHaveTextContent('the order is from 07:11 UTC.')
  })

  it('does not call a refresh that moved nothing in the sorted column a move', async () => {
    const page = fx.screenerPage()
    const { quotes } = setup('/screener?sort=ticker', page)
    await screen.findByRole('row', { name: /^BBCA\.JK / })
    quotes.mockResolvedValue({ rows: [{ ...page.rows[2] }], unknown: [], asOf: null, lastCompletedAt: null, pollSeconds: 60 })
    await minute()
    expect(quotes).toHaveBeenCalledTimes(1)
    expect(screen.getByText(/^Prices refresh every minute/)).toBeInTheDocument()
  })

  it('pauses while the tab is hidden, and refreshes at once when it comes back after a minute or more', async () => {
    const { quotes } = setup()
    await screen.findByRole('row', { name: /^BBCA\.JK / })
    const visibility = (h: boolean) => act(() => {
      hidden = h
      document.dispatchEvent(new Event('visibilitychange'))
    })
    await visibility(true)
    await minute()
    await minute()
    expect(quotes).not.toHaveBeenCalled()
    await visibility(false)
    await waitFor(() => expect(quotes).toHaveBeenCalledTimes(1))
    await visibility(true)
    await visibility(false) // back within the minute: no second call
    expect(quotes).toHaveBeenCalledTimes(1)
  })

  it('asks for at most 100 tickers a call, the rows Load more added included', async () => {
    const many = Array.from({ length: 150 }, (_, i) => jp(`${1000 + i}.T`))
    stubSession()
    vi.spyOn(api, 'screener')
      .mockResolvedValueOnce(fx.screenerPage({ rows: many.slice(0, 50), nextCursor: 'c1', total: 150 }))
      .mockResolvedValueOnce(fx.screenerPage({ rows: many.slice(50, 100), nextCursor: 'c2', total: 150 }))
      .mockResolvedValueOnce(fx.screenerPage({ rows: many.slice(100), nextCursor: null, total: 150 }))
    const quotes = vi.spyOn(api, 'screenerQuotes').mockImplementation(async (tickers) => ({ rows: [], unknown: tickers, asOf: null, lastCompletedAt: null, pollSeconds: 60 }))
    const { user } = renderPage(<ScreenerPage />, { route: '/screener', path: '/screener' })
    await screen.findByText('Showing 50 of 150')
    await user.click(screen.getByRole('button', { name: 'Load more' }))
    await screen.findByText('Showing 100 of 150')
    await user.click(screen.getByRole('button', { name: 'Load more' }))
    await screen.findByText('Showing 150 of 150')
    await minute()
    expect(quotes.mock.calls.map(([t]) => t.length)).toEqual([100, 50])
    expect(quotes.mock.calls.flatMap(([t]) => t)).toEqual(many.map((r) => r.ticker))
  })

  it('aborts a refresh in flight when the query changes, and restarts the minute from the new load', async () => {
    const { quotes, user, screener } = setup()
    await screen.findByRole('row', { name: /^BBCA\.JK / })
    let signal: AbortSignal | undefined
    quotes.mockImplementation((_t, s) => {
      signal = s
      return new Promise(() => {}) // never answers
    })
    await minute()
    expect(signal?.aborted).toBe(false)
    await user.click(within(screen.getByRole('group', { name: 'Screen' })).getByRole('button', { name: 'Deep value 1' }))
    await waitFor(() => expect(lastQuery(screener)).toMatchObject({ filter: 'deep' }))
    await waitFor(() => expect(signal?.aborted).toBe(true))
  })

  it('falls back to today’s behaviour against an API without the live screener', async () => {
    const { quotes } = setup('/screener', fx.screenerPageBeforeLive())
    const samsung = within(await screen.findByRole('row', { name: /^005930\.KS / }))
    expect(samsung.getByText('Oct 13, 2026')).toBeInTheDocument() // the run's date, as before
    expect(row('005930.KS')).not.toHaveTextContent(/delayed|real-time|Closed/)
    expect(within(row('D01.SI')).queryByText('Stale')).toBeNull()
    expect(within(row('2330.TW')).getByText('ATH wk of Oct 14, 2026')).toBeInTheDocument() // no quote: a run's high, by its week
    expect(screen.getByText(/^North Asia/)).toHaveTextContent(/drawdown from each one's all-time high\. Recomputed every Saturday\.$/)
    expect(screen.getByText(/^Prices: /)).toHaveTextContent(/Weekly closes and highs are each exchange's; USD prices are labelled with their rate\.$/)
    expect(screen.getByText(/^Data as of/)).toHaveTextContent('(750 read, 30 unavailable, 5 failed) · next run Oct 16, 2026, 7:00 PM')
    expect(screen.queryByRole('button', { name: 'Re-sort' })).toBeNull()
    await minute()
    await minute()
    expect(quotes).not.toHaveBeenCalled()
  })

  it('stops refreshing when the API answers 404 for the quotes, keeping the rows as loaded', async () => {
    const { quotes } = setup()
    await screen.findByRole('row', { name: /^BBCA\.JK / })
    quotes.mockRejectedValue(new ApiError(404, 'not_found', 'Not found'))
    await minute()
    expect(quotes).toHaveBeenCalledTimes(1)
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Re-sort' })).toBeNull())
    await minute()
    await minute()
    expect(quotes).toHaveBeenCalledTimes(1)
    expect(within(row('BBCA.JK')).getByText('delayed · last trade 06:58 UTC')).toBeInTheDocument()
    expect(screen.getByText(/^Prices: /)).toHaveTextContent(LIVE_METHOD) // the rows are still the live API's
  })

  it('shows a new high from the live price as 0% on its trading day', async () => {
    setup()
    const tsmc = within(await screen.findByRole('row', { name: /^2330\.TW / }))
    expect(tsmc.getByText('0.0%')).toHaveAttribute('title', expect.stringMatching(/^All-time high NT\$2,600/))
    expect(tsmc.getByText('New high Oct 14, 2026')).toBeInTheDocument()
    expect(tsmc.queryByText(/ATH wk of/)).toBeNull()
    expect(within(row('005930.KS')).getByText('ATH wk of Jun 15, 2026')).toBeInTheDocument() // a high the run found keeps its week
  })

  it('says "Prices:" once when the API’s text leads with it', async () => {
    const page = fx.screenerPage()
    setup('/screener', { ...page, source: { ...page.source, prices: `Prices: ${page.source.prices}` } })
    expect(await screen.findByText(/^Prices: each listing's latest/)).not.toHaveTextContent(/Prices: Prices/)
  })

  it('formats a run’s duration', () => {
    expect([fmtDuration(45), fmtDuration(852), fmtDuration(900), fmtDuration(3720), fmtDuration(3600), fmtDuration(-3)]).toEqual(
      ['45 s', '14 min 12 s', '15 min', '1 h 2 min', '1 h', '0 s'])
  })
})

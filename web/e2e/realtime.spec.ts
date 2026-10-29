// Real-time quotes (RT-QUOTES §5) on the Trade page, the market list and the screener: each price labelled from its own
// source and delay, the chart's last bar following a real-time quote, and review shots when RT_SHOTS names a directory.
// The mock API serves today's (Yahoo-only) quotes; these tests rewrite its answers into what the API sends once the
// real-time sources serve, with the label computed by the API's own rule (lib/quoteLabel mirrors it).
import { fmtPrice } from '../src/lib/format'
import { computedLabel } from '../src/lib/quoteLabel'
import type { Asset, Candle, ScreenerPage, ScreenerQuote, ScreenerQuotes, ScreenerRow } from '../src/lib/types'
import { SCREENER_NOW } from '../src/test/fixtures'
import { expect, expectAccessible, expectNoHorizontalScroll, test, type Page } from './fixtures'

// A 5 s poll can be in flight when a test ends: let its rewritten answer go, rather than fail on the closed page.
test.afterEach(async ({ page }) => {
  await page.unrouteAll({ behavior: 'ignoreErrors' })
})

const shots = process.env.RT_SHOTS
const shot = async (page: Page, name: string, fullPage = false) => {
  if (shots) await page.screenshot({ path: `${shots}/${name}.png`, fullPage })
}

// Wednesday 2026-10-14 04:03:50 UTC, 13:03:50 in Seoul: KRX, TWSE, HOSE, SGX and TSE trade; HKEX is in its lunch break.
const NOW = '2026-10-14T04:03:50Z'
const ms = (iso: string) => Date.parse(iso)

type Serve = { source: string; class: 'realtime' | 'near-realtime' | 'delayed'; trade: string; read: string; basis: 'trade' | 'volume' | 'snapshot'; open: boolean; closedAt?: string }
/** What serves each venue at NOW, as the API would send it: the source, its last trade (or snapshot) and our read. */
const SERVE: Record<string, Serve> = {
  KRX: { source: 'naver', class: 'realtime', trade: NOW, read: '2026-10-14T04:03:51Z', basis: 'volume', open: true },
  TWSE: { source: 'mis', class: 'realtime', trade: '2026-10-14T04:03:37Z', read: '2026-10-14T04:03:50Z', basis: 'trade', open: true },
  HOSE: { source: 'vietcap', class: 'realtime', trade: '2026-10-14T04:03:49Z', read: '2026-10-14T04:03:50Z', basis: 'trade', open: true },
  HKEX: { source: 'tencent', class: 'realtime', trade: '2026-10-14T03:59:58Z', read: '2026-10-14T04:03:48Z', basis: 'trade', open: false, closedAt: '2026-10-14T04:00:00Z' },
  SGX: { source: 'sgx', class: 'near-realtime', trade: '2026-10-14T04:02:00Z', read: '2026-10-14T04:03:10Z', basis: 'snapshot', open: true },
  TSE: { source: 'yahoo', class: 'delayed', trade: '2026-10-14T03:48:41Z', read: '2026-10-14T04:03:20Z', basis: 'trade', open: true },
  IDX: { source: 'yahoo', class: 'delayed', trade: '2026-10-14T03:53:12Z', read: '2026-10-14T04:03:20Z', basis: 'trade', open: true },
  SET: { source: 'yahoo', class: 'delayed', trade: '2026-10-14T03:33:40Z', read: '2026-10-14T04:03:20Z', basis: 'trade', open: true },
  BURSA: { source: 'yahoo', class: 'delayed', trade: '2026-10-14T03:48:30Z', read: '2026-10-14T04:03:20Z', basis: 'trade', open: true },
}
/** Local prices for the pages under review: SAMBIO is Naver's KRX price in the 2026-10-14 13:03:50 KST read (scratchpad/rt/data/kr/b3). */
const LOCAL: Record<string, number> = { SAMBIO: 1_184_000 }
/** Yahoo's delayed last close for the bars (KRX: 20 minutes earlier); the bars of a Yahoo-served listing end at its quote. */
const YAHOO_LOCAL: Record<string, number> = { SAMBIO: 1_179_000, DBS: 78.41 }
const DELAY_MIN: Record<string, number> = { KRX: 20, TWSE: 20, TSE: 15, HKEX: 15, BURSA: 15, HOSE: 15, SGX: 10, IDX: 10, SET: 30 }
const HOURS: Record<string, [number, number]> = { KRX: [0, 6.5], TWSE: [1, 5.5], SGX: [1, 9], TSE: [0, 6.5], HKEX: [1.5, 8], HOSE: [2, 7.75], IDX: [2, 9], SET: [3, 9.5], BURSA: [1, 9] }

/** A quote-feed asset as the API sends it once `venue`'s source serves it at `serve`'s moment. */
function served(a: Asset, over: Partial<Serve> & { local?: number } = {}): Asset {
  const venue = a.sessionVenue
  if (a.venue !== 'yahoo' || !a.quoteFeed || !venue || !(venue in SERVE)) return a
  const s = { ...SERVE[venue], ...over }
  const local = over.local ?? LOCAL[a.symbol] ?? a.quoteFeed.localPrice
  const end = Math.min(ms(s.read), s.closedAt ? ms(s.closedAt) : Infinity)
  const marketState = s.open ? 'open' : 'closed'
  const q = {
    ...a.quoteFeed, localPrice: local, asOf: s.trade, marketState, source: s.source, class: s.class, lastTradeAt: s.trade, readAt: s.read,
    delaySeconds: Math.max(0, Math.round((end - ms(s.trade)) / 1000)), timeBasis: s.basis,
  } as const
  const price = local * a.quoteFeed.fx
  const range = local === a.quoteFeed.localPrice ? {} : { high24h: price * 1.008, low24h: price * 0.991 } // the session's range around the new price
  return { ...a, ...range, price, session: a.session === null ? null : marketState, quoteFeed: { ...q, label: computedLabel(q) } }
}

/** Hourly bars inside `venue`'s weekday session hours (UTC), the last one holding `lastAt`, closing at `close` in the requested currency. */
function sessionBars(venue: string, lastAt: number, close: number, n = 160): Candle[] {
  const [from, to] = HOURS[venue]
  const times: number[] = []
  for (let t = Math.floor(lastAt / 3600) * 3600; times.length < n; t -= 3600) {
    const d = new Date(t * 1000)
    const h = d.getUTCHours() + d.getUTCMinutes() / 60
    if (d.getUTCDay() % 6 !== 0 && h >= Math.floor(from) && h < to) times.unshift(t)
  }
  const at = (i: number) => close * (1 - 0.0006 * (n - 1 - i) + (0.03 * Math.sin(i / 11) + 0.006 * Math.sin(i * 1.7)) * ((n - 1 - i) / n)) // ends at `close`
  return times.map((t, i) => {
    const c = at(i)
    const o = i ? at(i - 1) : c * 0.998
    return { t, o, h: Math.max(o, c) * 1.003, l: Math.min(o, c) * 0.997, c, v: 900 + ((i * 37) % 400) }
  })
}

/** Serve every quote feed as the real-time API would; `over` changes one symbol's quote (a newer read). */
async function realtimeMarket(page: Page, over: () => Record<string, Partial<Serve> & { local?: number }> = () => ({})) {
  const seen = new Map<string, Asset>() // every asset served so far (the named and the listed polls), by symbol
  await page.route(/\/api\/market\/assets(\?|$)/, async (route) => {
    const response = await route.fetch()
    const json = (await response.json()) as { assets: Asset[] }
    const changes = over()
    const out = json.assets.map((a) => served(a, changes[a.symbol]))
    for (const a of out) seen.set(a.symbol, a)
    await route.fulfill({ response, json: { assets: out } })
  })
  await page.route(/\/api\/market\/candles\?/, async (route) => {
    const url = new URL(route.request().url())
    const symbol = url.searchParams.get('symbol') ?? ''
    // The chart may ask for its candles before the poll that carries its quote has answered: wait for it (at most 5 s).
    for (let i = 0; i < 100 && !seen.has(symbol); i++) await new Promise((r) => setTimeout(r, 50))
    const a = seen.get(symbol)
    if (!a?.quoteFeed || !a.sessionVenue || !(a.sessionVenue in SERVE) || url.searchParams.get('interval') !== '1h') return route.fallback()
    // Yahoo's bars: they end DELAY_MIN before now, at Yahoo's delayed price, whichever source prices the quote.
    const yahooAt = ms(NOW) - DELAY_MIN[a.sessionVenue] * 60_000
    const local = YAHOO_LOCAL[symbol] ?? a.quoteFeed.localPrice
    const close = url.searchParams.get('currency') === 'original' ? local : local * a.quoteFeed.fx
    await route.fulfill({ json: { candles: sessionBars(a.sessionVenue, yahooAt / 1000, close) } })
  })
}

const KRW = 0.000746899 // the mock's Yahoo FX (src/test/fixtures FEED_FX)
const legendClose = (page: Page) => page.locator('.pointer-events-none.absolute.top-2').getByText(/^C /)

/** Back to the top of the page: the shell scrolls its <main>, not the document. */
const toTop = (page: Page) => page.evaluate(() => { document.getElementById('main')?.scrollTo(0, 0); window.scrollTo(0, 0) })

/**
 * A Trade page's review shots. At 1440 one, the ticket open beside the chart; on a phone the header and chart first
 * (`top`, before the ticket opens), then the ticket (`ticket`).
 */
async function tradeShot(page: Page, name: string, isMobile: boolean, scheme: string, phase: 'top' | 'ticket') {
  if (phase === 'top' && isMobile) {
    await toTop(page)
    await shot(page, `${name}-375-${scheme}`)
    await page.getByRole('group', { name: 'Overlays' }).evaluate((el) => el.closest('.card, [class*="overflow-hidden"]')?.scrollIntoView({ block: 'center' }))
    await shot(page, `${name}-chart-375-${scheme}`)
  } else if (phase === 'ticket') {
    if (!isMobile) await toTop(page)
    await shot(page, isMobile ? `${name}-ticket-375-${scheme}` : `${name}-1440-${scheme}`)
  }
}

async function openTicket(page: Page, isMobile: boolean) {
  if (isMobile) await page.getByRole('navigation', { name: 'Trade actions' }).getByRole('button', { name: 'Trade' }).click()
  else await page.getByRole('button', { name: 'Manual', exact: true }).click()
  await page.getByLabel('Amount (USD)').fill('100')
}

for (const scheme of ['light', 'dark'] as const) {
  test(`a real-time KRX listing: labelled real-time, read every 5 s, the chart's last bar moving with it (${scheme}) @mobile`, async ({ page, isMobile }) => {
    await page.clock.install({ time: new Date(NOW) })
    await page.emulateMedia({ colorScheme: scheme })
    let next: Record<string, Partial<Serve> & { local?: number }> = {}
    await realtimeMarket(page, () => next)
    await page.goto('/trade/SAMBIO')
    await expect(page.getByRole('heading', { level: 1, name: 'Samsung Biologics' })).toBeVisible()
    await page.getByRole('group', { name: 'Display currency' }).getByRole('button', { name: 'USD', exact: true }).click()
    await expect(page.getByText(/^SAMBIO · 207940\.KS · Naver/)).toBeVisible()
    await expect(page.getByText('real-time · 04:03:50 UTC', { exact: true })).toBeVisible()
    await expect(page.getByText('KRX session open', { exact: true })).toBeVisible()
    await expect(page.getByText('Yahoo bars · 20 min delayed', { exact: true })).toBeVisible()
    await expect(page.getByText('last bar real-time', { exact: true })).toBeVisible()
    const usd = 1_184_000 * KRW
    await expect(page.getByTitle(/^USD price used for paper fills: Naver's price × FX/)).toHaveText(fmtPrice(usd))
    await expect(legendClose(page)).toHaveText(`C ${usd.toFixed(2)}`) // the last bar's close is the real-time price, not Yahoo's
    await tradeShot(page, 'trade-krx-realtime', isMobile, scheme, 'top')
    await openTicket(page, isMobile)
    await expect(page.getByText(`SAMBIO at ${fmtPrice(usd)} · real-time · 04:03:50 UTC`, { exact: true })).toBeVisible()
    await expect(page.getByText(/fills use Naver Finance's real-time price \(last trade Oct 14, 04:03:50 UTC\)/).first()).toBeAttached()
    await expect(page.locator('body')).not.toContainText(/\blive\b/i)
    await expectAccessible(page)
    await expectNoHorizontalScroll(page)
    await tradeShot(page, 'trade-krx-realtime', isMobile, scheme, 'ticket')
    // Five seconds on, the next read: a new trade at 04:03:55 moves the header, the ticket and the chart's last bar.
    next = { SAMBIO: { trade: '2026-10-14T04:03:55Z', read: '2026-10-14T04:03:56Z', local: 1_185_000 } }
    const read = page.waitForResponse((r) => /\/api\/market\/assets\?scope=named/.test(r.url()))
    await page.clock.fastForward(5_000)
    await read
    const moved = 1_185_000 * KRW
    await expect(page.getByText('real-time · 04:03:55 UTC', { exact: true })).toBeVisible()
    await expect(legendClose(page)).toHaveText(`C ${moved.toFixed(2)}`)
    await expect(page.getByText(`SAMBIO at ${fmtPrice(moved)} · real-time · 04:03:55 UTC`, { exact: true })).toBeVisible()
  })

  test(`an SGX listing: SGX's snapshot, about 1 min behind, never real-time (${scheme}) @mobile`, async ({ page, isMobile }) => {
    await page.clock.install({ time: new Date(NOW) })
    await page.emulateMedia({ colorScheme: scheme })
    await realtimeMarket(page)
    await page.goto('/trade/DBS')
    await expect(page.getByRole('heading', { level: 1, name: 'DBS Group' })).toBeVisible()
    await expect(page.getByText(/^DBS · D05\.SI · SGX/)).toBeVisible()
    await expect(page.getByText('about 1 min behind · 04:02 UTC', { exact: true })).toBeVisible()
    await expect(page.getByText('Yahoo bars · 10 min delayed', { exact: true })).toBeVisible()
    await expect(page.getByText('last bar about 1 min behind', { exact: true })).toBeVisible()
    const usd = 78.56 * 0.782179
    await tradeShot(page, 'trade-sgx-1min', isMobile, scheme, 'top')
    await openTicket(page, isMobile)
    await expect(page.getByText(`DBS at ${fmtPrice(usd)} · about 1 min behind · 04:02 UTC`, { exact: true })).toBeVisible()
    await expect(page.getByText(/fills use SGX's price, about 1 min behind the exchange \(snapshot of Oct 14, 04:02 UTC\)/).first()).toBeAttached()
    await expect(page.getByText(/^real-time ·|· real-time ·|last bar real-time/)).toHaveCount(0) // other venues' tags in the list may say real-time
    await expect(page.locator('body')).not.toContainText(/\blive\b/i)
    await expectNoHorizontalScroll(page)
    await tradeShot(page, 'trade-sgx-1min', isMobile, scheme, 'ticket')
  })

  test(`a TSE listing stays on Yahoo: delayed with its last trade (${scheme}) @mobile`, async ({ page, isMobile }) => {
    await page.clock.install({ time: new Date(NOW) })
    await page.emulateMedia({ colorScheme: scheme })
    await realtimeMarket(page)
    await page.goto('/trade/TOKYOELEC')
    await expect(page.getByRole('heading', { level: 1, name: 'Tokyo Electron' })).toBeVisible()
    await expect(page.getByText(/^TOKYOELEC · 8035\.T · Yahoo/)).toBeVisible()
    await expect(page.getByText('delayed · last trade 03:48 UTC', { exact: true })).toBeVisible()
    await expect(page.getByText('Yahoo bars · 15 min delayed', { exact: true })).toBeVisible()
    await expect(page.getByText(/^last bar/)).toHaveCount(0)
    const usd = 12805 * 0.00632083
    await tradeShot(page, 'trade-tse-delayed', isMobile, scheme, 'top')
    await openTicket(page, isMobile)
    await expect(page.getByText(`TOKYOELEC at ${fmtPrice(usd)} · delayed · last trade 03:48 UTC`, { exact: true })).toBeVisible()
    await expect(page.getByText(/^real-time ·|· real-time ·|last bar/)).toHaveCount(0)
    await expect(page.locator('body')).not.toContainText(/\blive\b/i)
    await expectNoHorizontalScroll(page)
    await tradeShot(page, 'trade-tse-delayed', isMobile, scheme, 'ticket')
  })

  test(`the market list tags each venue by its prices: real-time, about 1 min behind, delayed, closed (${scheme}) @mobile`, async ({ page, isMobile }) => {
    await page.clock.install({ time: new Date(NOW) })
    await page.emulateMedia({ colorScheme: scheme })
    await realtimeMarket(page)
    await page.goto('/trade/SAMBIO')
    await expect(page.getByText('real-time · 04:03:50 UTC', { exact: true })).toBeVisible()
    let list = page.getByRole('complementary', { name: 'Markets' })
    if (isMobile) {
      await page.getByRole('button', { name: /^Switch market: / }).click()
      list = page.getByRole('dialog', { name: 'Switch market' })
    }
    const tag = (group: string) => list.getByRole('list', { name: group }).locator('xpath=preceding-sibling::div[1]')
    const want: [string, string][] = [
      ['Singapore · SGX', 'about 1 min behind'], ['Taiwan · TWSE', 'real-time'], ['South Korea · KRX', 'real-time'], ['Japan · TSE', 'delayed'],
      ['Hong Kong · HKEX', 'closed'], ['Indonesia · IDX', 'delayed'], ['Thailand · SET', 'delayed'], ['Malaysia · Bursa', 'delayed'], ['Vietnam · HOSE', 'real-time'],
    ]
    for (const [group, label] of want) await expect(tag(group), group).toHaveText(`${group}${label}`)
    await expect(list.getByRole('link', { name: /^SAMBIO/ })).toHaveAttribute('title', 'real-time · 04:03:50 UTC')
    await expect(list.getByRole('link', { name: /^TOKYOELEC/ })).toHaveAttribute('title', 'delayed · last trade 03:48 UTC')
    await expect(list.getByRole('link', { name: /^TENCENT/ })).toHaveAttribute('title', 'last close · Oct 14, 03:59 UTC') // HKEX's lunch break
    await expectNoHorizontalScroll(page)
    const px = isMobile ? 375 : 1440
    const box = list.getByRole('list', { name: 'Market list' })
    for (const [name, group] of [['sgx', 'Singapore · SGX'], ['krx', 'South Korea · KRX'], ['tse', 'Japan · TSE']] as const) {
      // Scroll the list (never the page) so the group's rows start just under its sticky label.
      await box.evaluate((el, g) => {
        const rows = [...el.querySelectorAll<HTMLElement>('ul[aria-labelledby]')].find((u) => document.getElementById(u.getAttribute('aria-labelledby')!)?.textContent === g)!
        el.scrollTop += rows.getBoundingClientRect().top - el.getBoundingClientRect().top - (rows.previousElementSibling as HTMLElement).offsetHeight
      }, group)
      await shot(page, `market-list-${name}-${px}-${scheme}`)
    }
  })
}

// ── The screener: the same labels at the fixtures' Wednesday 07:10 UTC (HKEX, SGX and IDX trade; KRX, TWSE, TSE closed)
/** A screener quote as the real-time API sends it for these tickers; the others stay Yahoo's, with class and label added. */
function screenerServed(r: ScreenerRow): ScreenerRow {
  const q = r.quote
  if (!q) return r
  const rt: Record<string, Partial<ScreenerQuote>> = {
    '0700.HK': { source: 'tencent', class: 'realtime', lastTradeAt: '2026-10-14T07:09:58Z', readAt: '2026-10-14T07:10:00Z', timeBasis: 'trade', refreshSeconds: 10, session: 'open', staleAt: '2026-10-14T07:12:00Z', stale: false },
    'D01.SI': { source: 'sgx', class: 'near-realtime', lastTradeAt: '2026-10-14T07:08:00Z', readAt: '2026-10-14T07:09:10Z', timeBasis: 'snapshot', refreshSeconds: 30, session: 'open', staleAt: '2026-10-14T07:12:00Z', stale: false },
    '005930.KS': { source: 'naver', class: 'realtime', lastTradeAt: '2026-10-14T06:30:00Z', readAt: '2026-10-14T06:32:01Z', timeBasis: 'volume', refreshSeconds: null, session: 'closed' },
    '2330.TW': { source: 'mis', class: 'realtime', lastTradeAt: '2026-10-14T05:30:00Z', readAt: '2026-10-14T05:32:00Z', timeBasis: 'trade', refreshSeconds: null, session: 'closed' },
  }
  const over = rt[r.ticker]
  const closes: Record<string, string> = { '005930.KS': '2026-10-14T06:30:00Z', '2330.TW': '2026-10-14T05:30:00Z' }
  const next = { ...q, ...(over ?? { class: 'delayed' as const, timeBasis: 'trade' as const }) }
  const end = Math.min(ms(next.readAt), closes[r.ticker] ? ms(closes[r.ticker]) : Infinity)
  const delaySeconds = Math.max(0, Math.round((end - ms(next.lastTradeAt)) / 1000))
  const quote = { ...next, delaySeconds, ...(over ? { delayMinutes: Math.round((delaySeconds / 60) * 100) / 100, delayBasis: 'measured' as const } : {}) }
  return { ...r, priceAt: quote.lastTradeAt, quote: { ...quote, label: computedLabel(quote) } }
}

for (const scheme of ['light', 'dark'] as const) {
  test(`the screener labels real-time, snapshot and delayed quotes from their own fields (${scheme}) @mobile`, async ({ page, isMobile }) => {
    await page.clock.install({ time: new Date(SCREENER_NOW) })
    await page.emulateMedia({ colorScheme: scheme })
    await page.route(/\/api\/screener\/asia(\?|$)/, async (route) => {
      const response = await route.fetch()
      const json = (await response.json()) as ScreenerPage
      await route.fulfill({ response, json: { ...json, rows: json.rows.map(screenerServed) } })
    })
    await page.route(/\/api\/screener\/asia\/quotes\?/, async (route) => {
      const response = await route.fetch()
      const json = (await response.json()) as ScreenerQuotes
      await route.fulfill({ response, json: { ...json, rows: json.rows.map(screenerServed) } })
    })
    await page.goto('/screener')
    const row = (ticker: string) => page.getByRole('row', { name: new RegExp(`^${ticker.replace('.', '\\.')} `) })
    await expect(row('0700.HK')).toContainText('real-time · 07:09:58 UTCOpen · Oct 14')
    await expect(row('D01.SI')).toContainText('about 1 min behind · 07:08 UTCOpen · Oct 14')
    await expect(row('005930.KS')).toContainText('real-time · 06:30:00 UTCClosed · Oct 14') // Naver's closing trade, settled after the close
    await expect(row('BBCA.JK')).toContainText('delayed · last trade 06:58 UTCOpen · Oct 14')
    await expect(page.getByRole('table')).not.toContainText(/\blive\b/i)
    await expectNoHorizontalScroll(page)
    const px = isMobile ? 375 : 1440
    const region = page.getByRole('region', { name: 'Screened stocks' })
    if (isMobile) {
      await region.locator('.overflow-x-auto').evaluate((el) => {
        const ths = [...el.querySelectorAll('th')]
        const th = ths.find((t) => t.textContent?.includes('Updated'))!
        el.scrollLeft = th.offsetLeft - ths[0].offsetWidth
      })
    }
    await region.evaluate((el) => el.scrollIntoView({ block: 'start' }))
    await shot(page, `screener-${px}-${scheme}`)
  })
}

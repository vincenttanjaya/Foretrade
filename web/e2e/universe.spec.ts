import { readFileSync } from 'node:fs'
import { expect, expectNoHorizontalScroll, httpError, test } from './fixtures'
import * as fx from '../src/test/fixtures'
import { ASIA_PERPS, US_PERPS, VENUE_GROUPS, venueGroup } from '../src/pages/strategies/strategy'
import { QUOTE_FEED_SYMBOLS } from '../src/pages/strategies/quoteFeeds'
import { fmtPrice } from '../src/lib/format'
import type { AIPortfolio, Asset, Portfolio, Trade } from '../src/lib/types'
import { API_URL } from './mock-api'

// Pinned MIT CRV source (84437effd1ecf46b3dfd82ce214bd95e799d543636869f9f1665f53d3ceecf9a).
// Test-only fixture; attribution and full notice are bundled under src/assets/coins.
const canonicalCRV = readFileSync(new URL('./fixtures/crv-canonical.svg', import.meta.url), 'utf8')

for (const theme of ['light', 'dark'] as const) {
  test(`Universe ${theme}: closed US session orders, stock mark and crypto transport @mobile`, async ({ page, binance }, info) => {
    await page.addInitScript((saved) => localStorage.setItem('theme', saved), theme)
    await page.goto('/trade/AAPL')
    await expect(page.getByRole('heading', { level: 1, name: 'Apple' })).toBeVisible()
    await expect(page.getByText('NYSE session closed', { exact: true })).toBeVisible()
    await expect(page.getByTitle(/Hyperliquid perp mark used for paper fills/)).toHaveText('$225.00')
    await expect(page.getByText('Polled · 30s', { exact: true })).toBeVisible()
    await expect(page.getByText(/Approx\. 24h range · hourly candles/)).toBeVisible()
    expect(binance).toHaveLength(0)
    await page.screenshot({ path: info.outputPath(`stock-header-session-${theme}.png`) })
    await page.getByRole('button', { name: 'Manual', exact: true }).click()
    await page.getByLabel('Amount (USD)').fill('225')
    await expect(page.getByText('Est. quantity').locator('..')).toContainText('1 AAPL')
    await expect(page.getByText(/No funding accrual, dividends or split adjustment/)).toBeVisible()
    const before = await (await page.request.get(`${API_URL}/api/portfolio`)).json() as { portfolio: Portfolio }
    const filled = page.waitForResponse((response) => response.url() === `${API_URL}/api/orders` && response.request().method() === 'POST')
    await page.getByRole('button', { name: 'Confirm buy (paper)' }).click()
    const response = await filled
    expect(response.status()).toBe(200)
    const fill = await response.json() as { trade: Trade; portfolio: Portfolio }
    expect(fill.trade.price).toBeCloseTo(225 * 1.0002, 8)
    expect(fill.trade.qty).toBeCloseTo(225 / fill.trade.price, 8)
    expect(fill.trade.fee).toBeCloseTo(0.225, 8)
    expect(fill.portfolio.cash).toBeCloseTo(before.portfolio.cash - 225.225, 8)
    expect(fill.portfolio.positions.find((position) => position.symbol === 'AAPL')).toMatchObject({ markPrice: 225, avgPrice: fill.trade.price, qty: fill.trade.qty })
    const ledger = await (await page.request.get(`${API_URL}/api/portfolio/trades`)).json() as { trades: Trade[] }
    expect(ledger.trades.find((trade) => trade.id === fill.trade.id)).toMatchObject(fill.trade)
    await expect(page.getByRole('status').filter({ hasText: /Bought .* AAPL at \$225\.045 \(paper\)\./ })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Close AAPL position at market' })).toBeVisible()
    await expectNoHorizontalScroll(page)
    await page.screenshot({ path: info.outputPath(`stock-closed-${theme}.png`), fullPage: true })
    if (info.project.name === 'mobile') {
      await page.getByRole('button', { name: 'Switch market: AAPL Apple' }).click()
      await page.getByRole('dialog', { name: 'Switch market' }).locator('a[href="/trade/BTC"]').click()
    } else await page.getByRole('complementary', { name: 'Markets' }).locator('a[href="/trade/BTC"]').click()
    await expect(page.getByRole('heading', { level: 1, name: 'Bitcoin' })).toBeVisible()
    await expect.poll(() => binance.length).toBe(1)
    expect(binance[0].url()).toBe('wss://data-stream.binance.vision/ws/btcusdt@kline_1h')
    binance[0].send(JSON.stringify({ k: { t: Date.now(), o: '84213.2', h: '90100', l: '84100', c: '90000', v: '12.5' } }))
    await expect(page.getByText('Live', { exact: true })).toBeVisible()
    await expect(page.getByText('$90,000.00', { exact: true })).toBeVisible()
    await expectNoHorizontalScroll(page)
    await page.goto('/account')
    const metadata = page.getByText(/venue candle history \(Binance crypto \/ Hyperliquid perps\)/)
    await expect(metadata).toBeVisible()
    await metadata.scrollIntoViewIfNeeded()
    await expectNoHorizontalScroll(page)
    await page.screenshot({ path: info.outputPath(`mixed-account-metadata-${theme}.png`) })
  })

  test(`Universe ${theme}: Asia stocks group, find TSMC and name their venue session @mobile`, async ({ page }, info) => {
    await page.addInitScript((saved) => localStorage.setItem('theme', saved), theme)
    await page.goto('/trade/SMSN')
    await expect(page.getByRole('heading', { level: 1, name: 'Samsung Electronics' })).toBeVisible()
    await expect(page.getByText('KRX session closed', { exact: true })).toBeVisible()
    await expect(page.getByText('KRX 005930 common share, KRW→USD', { exact: true })).toBeVisible()
    await page.getByRole('button', { name: 'Manual', exact: true }).click()
    await expect(page.getByText(/trade\.xyz's oracle converts the KRW listing to USD\. This app does no FX conversion\./)).toBeVisible()
    await expectNoHorizontalScroll(page)
    await page.screenshot({ path: info.outputPath(`asia-smsn-${theme}.png`), fullPage: true })
    if (info.project.name === 'mobile') {
      await page.getByRole('button', { name: 'Switch market: SMSN Samsung Electronics' }).click()
      const picker = page.getByRole('dialog', { name: 'Switch market' })
      const list = picker.getByRole('list', { name: 'Market list' })
      await expect(picker.getByRole('searchbox', { name: 'Search markets' })).toBeFocused()
      await expect(list.getByRole('list')).toHaveCount(VENUE_GROUPS.length) // crypto, US, the Asia perps and ADRs, then each local venue
      const adrs = fx.universeAssets.filter((a) => venueGroup(a) === 'asia')
      await expect(list.getByRole('list', { name: 'Asia perps & US-listed ADRs' }).getByRole('link')).toHaveCount(adrs.length) // perps, then US-hours quote feeds
      expect(adrs.length).toBe(17 + 42)
      expect(await list.evaluate((el) => el.scrollHeight > el.clientHeight && getComputedStyle(el).overflowY === 'auto')).toBe(true) // capped, scrolls inside
      await expect(list.locator('a[aria-current="page"]')).toBeInViewport() // SMSN, far down the list
      await expectNoHorizontalScroll(page)
      await page.screenshot({ path: info.outputPath(`market-picker-${theme}.png`) })
      await picker.getByRole('searchbox', { name: 'Search markets' }).fill('TSMC')
      await expect(picker.getByRole('link')).toHaveCount(1)
      await picker.getByRole('link', { name: /TSMC \(Taiwan Semiconductor\)/ }).click()
    } else {
      const markets = page.getByRole('complementary', { name: 'Markets' })
      await expect(markets.getByText('Asia perps & US-listed ADRs', { exact: true })).toBeVisible()
      // Capped under the top bar: the page does not grow with the universe, and SMSN, far down, is scrolled into view.
      const list = markets.getByRole('list', { name: 'Market list' })
      expect(await list.evaluate((el) => el.scrollHeight > el.clientHeight && getComputedStyle(el).overflowY === 'auto')).toBe(true)
      expect((await markets.boundingBox())!.height).toBeLessThanOrEqual(900 - 56) // fits under the top bar
      await expect(list.locator('a[aria-current="page"]')).toBeInViewport()
      await expect(markets.getByRole('searchbox', { name: 'Search markets' })).toBeInViewport()
      await markets.getByRole('searchbox', { name: 'Search markets' }).fill('TSMC')
      await expect(markets.getByRole('link')).toHaveCount(1)
      await markets.getByRole('link', { name: /TSMC \(Taiwan Semiconductor\)/ }).click()
    }
    await expect(page.getByRole('heading', { level: 1, name: 'TSMC (Taiwan Semiconductor)' })).toBeVisible()
    await expect(page.getByText('NYSE session closed', { exact: true })).toBeVisible()
    await expect(page.getByTitle(/Hyperliquid perp mark used for paper fills/)).toHaveText('$485.00')
    await expectNoHorizontalScroll(page)
    await page.screenshot({ path: info.outputPath(`asia-tsm-${theme}.png`), fullPage: true })
  })

  test(`Universe ${theme}: wave-2 perps group by region and CXMT names its SSE session and yuan note @mobile`, async ({ page }, info) => {
    await page.addInitScript((saved) => localStorage.setItem('theme', saved), theme)
    await page.goto('/trade/CXMT')
    await expect(page.getByRole('heading', { level: 1, name: 'CXMT (ChangXin Memory)' })).toBeVisible()
    await expect(page.getByText('SSE session closed', { exact: true })).toBeVisible()
    await expect(page.getByText('SSE STAR 688825 A-share, CNY→USD; early bars are pre-IPO implied prices', { exact: true })).toBeVisible()
    await page.getByRole('button', { name: 'Manual', exact: true }).click()
    await expect(page.getByText(/trade\.xyz's oracle converts the CNY listing to USD at the offshore USD\/CNH rate\./)).toBeVisible()
    await expectNoHorizontalScroll(page)
    await page.screenshot({ path: info.outputPath(`wave2-cxmt-${theme}.png`), fullPage: true })
    if (info.project.name === 'mobile') {
      await page.getByRole('button', { name: /^Switch market: / }).click()
      const picker = page.getByRole('dialog', { name: 'Switch market' })
      await expect(picker.getByRole('list', { name: 'US stocks & indices' }).locator('a[href="/trade/PLTR"]')).toHaveCount(1)
      await expect(picker.getByRole('list', { name: 'Asia perps & US-listed ADRs' }).locator('a[href="/trade/KIOXIA"]')).toHaveCount(1)
      await picker.getByRole('searchbox', { name: 'Search markets' }).fill('KIOXIA')
      await picker.locator('a[href="/trade/KIOXIA"]').click()
    } else {
      const markets = page.getByRole('complementary', { name: 'Markets' })
      await markets.getByRole('searchbox', { name: 'Search markets' }).fill('Kioxia')
      await expect(markets.getByRole('link')).toHaveCount(1)
      await markets.getByRole('link', { name: /Kioxia Holdings/ }).click()
    }
    await expect(page.getByRole('heading', { level: 1, name: 'Kioxia Holdings' })).toBeVisible()
    await expect(page.getByText('TSE session closed', { exact: true })).toBeVisible()
    await expect(page.getByText(/bars before the 2026-09-28 3-for-1 split are divided by 3/)).toBeVisible()
    await expectNoHorizontalScroll(page)
  })

  test(`Universe ${theme}: quote-feed stocks name the feed, session hours, FX and last-close fills @mobile`, async ({ page }, info) => {
    await page.addInitScript((saved) => localStorage.setItem('theme', saved), theme)
    await page.goto('/trade/DBS')
    await expect(page.getByRole('heading', { level: 1, name: 'DBS Group' })).toBeVisible()
    await expect(page.getByText('Quote feed', { exact: true })).toBeVisible()
    await expect(page.getByText('SGX session closed', { exact: true })).toBeVisible()
    await expect(page.getByText('SGX: D05 ordinary share, SGD→USD at Yahoo FX', { exact: true })).toBeVisible()
    const usd = 78.56 * 0.782179 // the fixture's local close × Yahoo FX
    await expect(page.getByTitle('Original SGD listing price from Yahoo.')).toHaveText('SGD 78.56')
    await expect(page.getByText('vs prev. close', { exact: true })).toBeVisible()
    await expect(page.getByText('Session high', { exact: true })).toBeVisible()
    await page.getByRole('button', { name: 'Manual', exact: true }).click()
    await page.getByLabel('Amount (USD)').fill('100')
    await expect(page.getByText(`DBS at ${fmtPrice(usd)} · last close`, { exact: true })).toBeVisible()
    const note = page.getByText(/^Paper fills use the USD price with 2 bps slippage and 0\.1% fee/)
    await expect(note).toContainText('Quote feed: prices follow SGX hours. Outside them, as now, paper fills use the last close (as of Oct 5, 20:00 UTC).')
    await expect(note).toContainText('SGD→USD at Yahoo FX: 1 SGD = $0.7822 (as of Oct 6, 08:58 UTC).')
    await expect(page.getByText(/No funding accrual, dividends or split adjustment/)).toHaveCount(0) // not a perp
    const filled = page.waitForResponse((response) => response.url() === `${API_URL}/api/orders` && response.request().method() === 'POST')
    await page.getByRole('button', { name: 'Confirm buy (paper)' }).click()
    const fill = await (await filled).json() as { trade: Trade }
    expect(fill.trade.price).toBeCloseTo(usd * 1.0002, 8)
    expect(fill.trade.fee).toBeCloseTo(0.1, 8)
    await expectNoHorizontalScroll(page)
    await page.screenshot({ path: info.outputPath(`quote-feed-dbs-${theme}.png`), fullPage: true })
    // HSDT is a US quote feed; the ADRs and local listings are Asian. A USD listing has no FX note.
    if (info.project.name === 'mobile') {
      await page.getByRole('button', { name: /^Switch market: / }).click()
      const picker = page.getByRole('dialog', { name: 'Switch market' })
      await expect(picker.getByRole('list', { name: 'US stocks & indices' }).locator('a[href="/trade/HSDT"]')).toHaveCount(1)
      await expect(picker.getByRole('list', { name: 'Asia perps & US-listed ADRs' }).locator('a[href="/trade/KARO"]')).toHaveCount(1)
      await expect(picker.getByRole('list', { name: 'Singapore · SGX' }).locator('a[href="/trade/DBS"]')).toHaveCount(1)
      await picker.getByRole('searchbox', { name: 'Search markets' }).fill('HSDT')
      await picker.locator('a[href="/trade/HSDT"]').click()
    } else {
      const markets = page.getByRole('complementary', { name: 'Markets' })
      await expect(markets.locator('li', { has: page.getByText('US stocks & indices', { exact: true }) }).locator('a[href="/trade/HSDT"]')).toHaveCount(1)
      await expect(markets.locator('li', { has: page.getByText('Asia perps & US-listed ADRs', { exact: true }) }).locator('a[href="/trade/KARO"]')).toHaveCount(1)
      await expect(markets.getByRole('list', { name: 'Singapore · SGX' }).locator('a[href="/trade/DBS"]')).toHaveCount(1)
      await markets.getByRole('searchbox', { name: 'Search markets' }).fill('HSDT')
      await expect(markets.getByRole('link')).toHaveCount(1)
      await markets.getByRole('link', { name: /Solana Company/ }).click()
    }
    await expect(page.getByRole('heading', { level: 1, name: 'Solana Company' })).toBeVisible()
    await expect(page.getByText('NYSE session closed', { exact: true })).toBeVisible()
    await page.getByRole('button', { name: 'Manual', exact: true }).click()
    await expect(page.getByText(/^Paper fills use the USD price/)).toContainText('Quote feed: prices follow US market hours.')
    await expect(page.getByText(/at Yahoo FX/)).toHaveCount(0)
    await expectNoHorizontalScroll(page)
    await page.screenshot({ path: info.outputPath(`quote-feed-hsdt-${theme}.png`), fullPage: true })
  })

  test(`Universe ${theme}: stock build card persists its saved comparison over SSE and reload @mobile`, async ({ page }, info) => {
    await page.addInitScript((saved) => localStorage.setItem('theme', saved), theme)
    await page.goto('/')
    await expect(page.getByText(/Live quotes: 69 crypto pairs · 19 US stock perps · 2 index perps · 17 Asia stock & ETF perps/)).toBeVisible()
    await page.getByLabel('Message the agent').fill('Build a stock basket with Apple and Nvidia')
    await page.keyboard.press('Enter')
    const card = page.locator('.card').filter({ hasText: 'AI portfolio' })
    await card.getByRole('button', { name: 'Build portfolio · 2 cr' }).click()
    await expect(card).toContainText('vs S&P 500 perp')
    await expect(card).toContainText('97 daily bars')
    await page.reload()
    await expect(card).toContainText('vs S&P 500 perp')
    await expect(card).toContainText('97 daily bars')
    await expect(card.getByRole('button', { name: /Build portfolio/ })).toHaveCount(0)
    await expectNoHorizontalScroll(page)
    await page.screenshot({ path: info.outputPath(`stock-card-restored-${theme}.png`) })
  })

  test.describe(() => {
    test.use({ allowedErrors: [httpError(409)] })
    test(`Universe ${theme}: stock conflict displays the returned reason and keeps confirmation retryable @mobile`, async ({ page }, info) => {
      await page.addInitScript((saved) => localStorage.setItem('theme', saved), theme)
      await page.route('**/api/chats/stock-proposal', (route) => route.fulfill({ json: { chat: { id: 'stock-proposal', title: 'AAPL paper proposal', pending: false, messages: [{ id: 'stock-answer', role: 'assistant', content: 'Controlled AAPL order proposal.', cards: [{ type: 'trade_proposal', data: fx.proposal({ symbol: 'AAPL', price: 225, stopLoss: 220, takeProfit: 240 }) }], createdAt: new Date().toISOString() }] } } }))
      await page.route('**/api/orders', (route) => route.fulfill({ status: 409, json: { error: { code: 'conflict', message: 'AAPL mark is unavailable; no order was filled.' } } }))
      await page.goto('/chat/stock-proposal')
      await page.getByRole('button', { name: 'Confirm buy (paper)' }).click()
      await expect(page.getByRole('alert')).toContainText('AAPL mark is unavailable; no order was filled.')
      await expect(page.getByText('Already filled', { exact: true })).toHaveCount(0)
      await expect(page.getByRole('button', { name: 'Confirm buy (paper)' })).toBeEnabled()
      await expectNoHorizontalScroll(page)
      await page.screenshot({ path: info.outputPath(`stock-conflict-${theme}.png`) })
    })
  })

  test(`Universe ${theme}: mixed saved comparison, actual window and unavailable retained history @mobile`, async ({ page }, info) => {
    await page.addInitScript((saved) => localStorage.setItem('theme', saved), theme)
    await page.goto('/portfolios')
    await page.getByLabel('Describe your portfolio').fill('A majority stock basket with Apple and Nvidia')
    await page.getByRole('button', { name: /Build portfolio/ }).click()
    await expect(page.getByRole('heading', { level: 1, name: 'US stock basket' })).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Portfolio vs S&P 500 perp' })).toBeVisible()
    await expect(page.getByText(/97 daily bars/)).toBeVisible()
    await page.getByRole('button', { name: 'Invest $10,000 (paper)' }).click()
    await expect(page.getByRole('button', { name: 'Invest more' })).toBeVisible()
    const savedPath = new URL(page.url()).pathname
    // Controlled missing comparison / shortened actual history. The real mock
    // server now returns null charts immediately after a buy rather than
    // inventing history; this response exercises the API's later retained case.
    await page.route(`**/api/ai-portfolios/${savedPath.split('/').at(-1)}`, async (route) => {
      const response = await route.fetch()
      const body = await response.json() as { portfolio: AIPortfolio }
      const inv = body.portfolio.invested!
      const end = Math.floor(Date.now() / 3_600_000) * 3600
      inv.series = Array.from({ length: 4 }, (_, i) => ({ t: end - (3 - i) * 3600, v: 99.6 + i / 10 }))
      inv.firstAt = new Date((end - 24 * 3600) * 1000).toISOString()
      inv.benchmarkSeries = null; inv.btcSeries = null; inv.benchmarkReturnPct = null; inv.btcReturnPct = null
      inv.windowStart = inv.series[0].t; inv.windowEnd = end; inv.bars = 4
      await route.fulfill({ response, json: body })
    })
    await page.reload()
    await page.getByRole('button', { name: 'Since invest', exact: true }).click()
    await expect(page.getByRole('heading', { name: 'Actual vs S&P 500 perp' })).toBeVisible()
    await expect(page.getByText('S&P 500 perp return').locator('..')).toContainText('—')
    await expect(page.getByText(/4 plotted bars/)).toBeVisible()
    await expect(page.getByText('Comparison unavailable for S&P 500 perp across the first investment and plotted window.')).toBeVisible()
    await expect(page.getByText('Volatility · ann.').locator('..')).toContainText('—')
    await expectNoHorizontalScroll(page)
    await page.screenshot({ path: info.outputPath(`portfolio-retained-${theme}.png`), fullPage: true })
    await page.getByText(/4 plotted bars/).scrollIntoViewIfNeeded()
    await page.screenshot({ path: info.outputPath(`portfolio-retained-metadata-${theme}.png`) })
    await page.goto('/portfolios')
    const card = page.getByRole('link', { name: /US stock basket/ })
    await expect(card).toContainText('97 daily bars')
    await expect(card).toContainText('S&P 500 perp')
    await expectNoHorizontalScroll(page)
  })

  test(`Universe ${theme}: 205 instruments, 176 stock marks on white discs, at 20 28 40 px @mobile`, async ({ page }, info) => {
    // decodes 615 marks and re-parses every SVG: the work grows with the universe, not with flakiness
    test.slow()
    await page.goto(`/e2e/artwork-gallery.html?theme=${theme}`)
    await expect(page.getByRole('heading', { name: `Universe artwork · ${theme}` })).toBeVisible()
    const rows = page.locator('tbody tr')
    await expect(rows).toHaveCount(205)
    await expect(page.locator('tbody img')).toHaveCount(615)
    // Every stock, ETF and index row draws its src/assets/stocks mark inside a white disc; no row falls back to a monogram.
    await expect(page.locator('tbody td > span:not(:has(img))')).toHaveCount(0)
    // one pass in the page: each stock row has its 3 marks on white discs, drawn from its own asset
    const markProblems = await page.evaluate((symbols) => symbols.flatMap((symbol) => {
      const marks = Array.from(document.querySelectorAll<HTMLImageElement>(`tr[data-symbol="${CSS.escape(symbol)}"] td > span.bg-white > img`))
      if (marks.length !== 3) return [`${symbol}: ${marks.length} marks`]
      return marks.filter((image) => !image.src.includes(`/assets/${symbol}-`)).map((image) => `${symbol}: ${image.src}`)
    }), [...US_PERPS, ...ASIA_PERPS, ...QUOTE_FEED_SYMBOLS.map(([s]) => s)])
    expect(markProblems).toEqual([])
    // Stock marks load lazily (a long market list must not fetch every one): load them all here to check each file.
    await page.locator('tbody img[loading="lazy"]').evaluateAll((images) => images.forEach((image) => ((image as HTMLImageElement).loading = 'eager')))
    await expect.poll(() => page.locator('tbody img').evaluateAll((images) => images.every((image) => (image as HTMLImageElement).complete && (image as HTMLImageElement).naturalWidth > 0))).toBe(true)
    for (const size of [20, 28, 40]) await expect(page.locator(`tbody td > img[width="${size}"][height="${size}"]`)).toHaveCount(29)
    const sources = await page.locator('tbody img').evaluateAll((images) => images.map((image) => (image as HTMLImageElement).src))
    expect(sources.every((src) => src.startsWith('http://localhost:4180/assets/'))).toBe(true)
    const paints = await page.locator('tbody img').evaluateAll(async (images) => {
      const sources = [...new Set(images.map((image) => (image as HTMLImageElement).src))].filter((src) => new URL(src).pathname.endsWith('.svg'))
      const results = []
      for (const src of sources) {
        const response = await fetch(src)
        if (!response.ok) throw new Error(`SVG source unavailable: ${src}`)
        const source = await response.text()
        const svg = new DOMParser().parseFromString(source, 'image/svg+xml')
        if (svg.querySelector('parsererror')) throw new Error(`Invalid SVG: ${src}`)
        const ids = Array.from(svg.querySelectorAll('[id]'), (element) => element.id)
        const references = Array.from(source.matchAll(/url\(\s*['"]?#([^'"\s)]+)['"]?\s*\)/g), (match) => match[1])
        for (const element of svg.querySelectorAll('*')) for (const attribute of element.attributes) {
          if (attribute.localName === 'href' && attribute.value.startsWith('#')) references.push(attribute.value.slice(1))
        }
        results.push({ src, ids, references, duplicateIds: ids.filter((id, index) => ids.indexOf(id) !== index), unresolved: references.filter((id) => !ids.includes(id)) })
      }
      return results
    })
    expect(paints).toHaveLength(23 + 125) // the coin SVGs, then every stock SVG
    for (const paint of paints) {
      expect(paint.duplicateIds, `${paint.src}: duplicate SVG IDs`).toEqual([])
      expect(paint.unresolved, `${paint.src}: unresolved SVG paint references`).toEqual([])
    }
    await info.attach(`artwork-paint-references-${theme}.json`, { body: JSON.stringify(paints, null, 2), contentType: 'application/json' })
    const crvPaint = await page.locator('tr[data-symbol="CRV"] img').evaluateAll(async (images, canonical) => {
      const reference = new Image()
      const url = URL.createObjectURL(new Blob([canonical], { type: 'image/svg+xml' }))
      try {
        reference.src = url
        await reference.decode()
        return images.map((element) => {
          const image = element as HTMLImageElement
          const canvas = document.createElement('canvas')
          canvas.width = image.width; canvas.height = image.height
          const ctx = canvas.getContext('2d')!
          const ground = getComputedStyle(image).backgroundColor
          ctx.fillStyle = ground; ctx.fillRect(0, 0, canvas.width, canvas.height)
          const background = ctx.getImageData(0, 0, canvas.width, canvas.height).data
          ctx.drawImage(reference, 0, 0, canvas.width, canvas.height)
          const expected = ctx.getImageData(0, 0, canvas.width, canvas.height).data
          ctx.clearRect(0, 0, canvas.width, canvas.height)
          ctx.fillStyle = ground; ctx.fillRect(0, 0, canvas.width, canvas.height)
          ctx.drawImage(image, 0, 0, canvas.width, canvas.height)
          const actual = ctx.getImageData(0, 0, canvas.width, canvas.height).data
          let differingPixels = 0, canonicalPaintedPixels = 0
          for (let index = 0; index < actual.length; index += 4) {
            if ([0, 1, 2, 3].some((channel) => actual[index + channel] !== expected[index + channel])) differingPixels++
            if ([0, 1, 2, 3].some((channel) => expected[index + channel] !== background[index + channel])) canonicalPaintedPixels++
          }
          return { size: image.width, ground, differingPixels, canonicalPaintedPixels }
        })
      } finally { URL.revokeObjectURL(url) }
    }, canonicalCRV)
    expect(crvPaint.map((paint) => paint.size)).toEqual([20, 28, 40])
    for (const paint of crvPaint) {
      expect(paint.canonicalPaintedPixels, `CRV canonical paint at ${paint.size}px`).toBeGreaterThan(0)
      expect(paint.differingPixels, `CRV paint versus pinned canonical source at ${paint.size}px`).toBe(0)
    }
    await info.attach(`crv-canonical-paint-${theme}.json`, { body: JSON.stringify(crvPaint, null, 2), contentType: 'application/json' })
    const rendered = await page.locator('tbody img').evaluateAll(async (images) => {
      await Promise.all(images.map((image) => (image as HTMLImageElement).decode()))
      return images.map((element) => {
        const image = element as HTMLImageElement
        const style = getComputedStyle(image)
        const badge = image.closest('td')!.firstElementChild as HTMLElement // the coin <img>, or a stock mark's white disc
        const box = badge.getBoundingClientRect()
        const cell = image.closest('td')!.getBoundingClientRect()
        const sample = document.createElement('canvas')
        sample.width = image.width; sample.height = image.height
        const ctx = sample.getContext('2d')!
        ctx.fillStyle = style.backgroundColor
        ctx.fillRect(0, 0, sample.width, sample.height)
        ctx.drawImage(image, 0, 0, sample.width, sample.height)
        const luminance = (x: number, y: number) => {
          const [r, g, b] = ctx.getImageData(Math.floor(x * sample.width), Math.floor(y * sample.height), 1, 1).data
          return [r, g, b].map((channel) => channel / 255).map((channel) => channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4).reduce((value, channel, index) => value + channel * [0.2126, 0.7152, 0.0722][index], 0)
        }
        const ground = luminance(0.2, 0.5), glyph = luminance(0.5, 0.25)
        return { symbol: image.closest('tr')!.dataset.symbol, size: parseFloat(badge.style.width), naturalWidth: image.naturalWidth, naturalHeight: image.naturalHeight, width: box.width, height: box.height, right: box.right, cellRight: cell.right, background: style.backgroundColor, objectFit: style.objectFit, clipPath: style.clipPath, maskImage: style.maskImage, badgeOverflow: style.overflow, contrast: (Math.max(ground, glyph) + 0.05) / (Math.min(ground, glyph) + 0.05) }
      })
    })
    await info.attach(`artwork-rendered-${theme}.json`, { body: JSON.stringify(rendered, null, 2), contentType: 'application/json' })
    for (const image of rendered) {
      expect(image.width).toBe(image.size)
      expect(image.height).toBe(image.size)
      expect(image.right).toBeLessThanOrEqual(image.cellRight)
      expect(image.clipPath).toBe('none')
      expect(image.maskImage).toBe('none')
    }
    if (theme === 'dark') for (const image of rendered.filter((image) => image.symbol === 'ZRO')) expect(image.contrast, `ZRO glyph/ground contrast at ${image.size}px`).toBeGreaterThanOrEqual(7)
    await expectNoHorizontalScroll(page)
    await page.screenshot({ path: info.outputPath(`artwork-38-${theme}.png`), fullPage: true })
  })
}

test('a quote feed on a venue with no verified calendar (IDX) gives Yahoo\'s market state @mobile', async ({ page }) => {
  await page.goto('/trade/BBCA')
  await expect(page.getByRole('heading', { level: 1, name: 'Bank Central Asia' })).toBeVisible()
  await expect(page.getByText('IDX session closed · per Yahoo', { exact: true })).toBeVisible()
  await expect(page.getByText('IDX session unknown', { exact: true })).toHaveCount(0)
  await expectNoHorizontalScroll(page)
})

for (const theme of ['light', 'dark'] as const)
  test(`an open quote feed's price reads delayed with its last trade time, never live, in ${theme} @mobile`, async ({ page }, info) => {
    await page.addInitScript((saved) => localStorage.setItem('theme', saved), theme)
    // SGX in session: Yahoo's latest trade, stamped 01:12 UTC (the mock's DBS row is a closed one).
    await page.route(`${API_URL}/api/market/assets`, async (route) => {
      const response = await route.fetch()
      const { assets } = (await response.json()) as { assets: Asset[] }
      const open = assets.map((a) => (a.symbol === 'DBS' ? { ...a, session: 'open' as const, quoteFeed: { ...a.quoteFeed!, marketState: 'open' as const, asOf: '2026-10-13T01:12:05Z' } } : a))
      await route.fulfill({ response, json: { assets: open } })
    })
    await page.goto('/trade/DBS')
    await expect(page.getByRole('heading', { level: 1, name: 'DBS Group' })).toBeVisible()
    await expect(page.getByText('SGX session open', { exact: true })).toBeVisible()
    await page.getByRole('button', { name: 'Manual', exact: true }).click()
    await page.getByLabel('Amount (USD)').fill('100')
    const usd = 78.56 * 0.782179
    await expect(page.getByText(`DBS at ${fmtPrice(usd)} · delayed · last trade 01:12 UTC`, { exact: true })).toBeVisible()
    await expect(page.getByText(/^DBS at .* · live$/)).toHaveCount(0)
    await expect(page.getByText(/^Paper fills use the USD price/)).toContainText(
      "Quote feed: prices follow SGX hours. It is open now, so fills use Yahoo's latest trade, which is delayed (last trade Oct 13, 01:12 UTC); outside its hours they use the last close.",
    )
    await expectNoHorizontalScroll(page)
    await page.screenshot({ path: info.outputPath(`quote-feed-open-${theme}.png`), fullPage: true })
  })

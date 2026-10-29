// Listed Asian index stocks (contract §2): every screener stock that passes the quote-feed rule trades under its Yahoo
// ticker. The mock lists 6501.T (TSE, open), 000810.KS (KRX, closed) and BBNI.JK (IDX, open per Yahoo).
import { expect, expectAccessible, expectNoHorizontalScroll, test } from './fixtures'
import type { Page } from './fixtures'
import type { Trade } from '../src/lib/types'
import { API_URL } from './mock-api'

const shots = process.env.ASIA3_SHOTS // a directory for the review screenshots, when set
const shot = async (page: Page, name: string, fullPage = false) => {
  if (shots) await page.screenshot({ path: `${shots}/${name}.png`, fullPage })
}
const hitachi = (page: Page) => page.getByRole('heading', { level: 1, name: 'Hitachi, Ltd.' })

test('a listed stock: screener row → Trade page → delayed ticket → paper fill → position', async ({ page, binance, isMobile }) => {
  test.skip(isMobile, 'the desktop journey; the phone list has its own test')
  await page.goto('/screener')
  const row = page.getByRole('row', { name: /^6501\.T / })
  await expect(row).toContainText('Hitachi, Ltd.')
  await expect(row.getByRole('button', { name: 'Set drawdown alert on 6501.T' })).toBeVisible()
  await row.getByRole('link', { name: 'Trade 6501.T' }).first().click() // the ticker and name cell
  await expect(page).toHaveURL(/\/trade\/6501\.T$/)
  await expect(hitachi(page)).toBeVisible()
  await expect(page.getByText('TSE: 6501 (Nikkei 225), JPY→USD at Yahoo FX', { exact: true })).toBeVisible()
  await expect(page.getByText('TSE session open', { exact: true })).toBeVisible()
  await expect(page.getByText('Yahoo bars · 15 min delayed', { exact: true })).toBeVisible() // Yahoo candles through the API, labelled as such
  await expect(page.getByText('delayed · last trade 03:31 UTC', { exact: true })).toBeVisible() // the header's line under the price
  await page.getByRole('button', { name: 'Manual', exact: true }).click()
  await page.getByLabel('Amount (USD)').fill('500')
  await expect(page.getByText('6501.T at $29.8722 · delayed · last trade 03:31 UTC', { exact: true })).toBeVisible()
  await expect(page.getByText(/6501\.T at .* · live/)).toHaveCount(0)
  const filled = page.waitForResponse((response) => response.url() === `${API_URL}/api/orders` && response.request().method() === 'POST')
  await page.getByRole('button', { name: 'Confirm buy (paper)' }).click()
  const fill = (await (await filled).json()) as { trade: Trade }
  expect(fill.trade).toMatchObject({ symbol: '6501.T', side: 'buy' })
  expect(fill.trade.price).toBeCloseTo(4726 * 0.00632083 * 1.0002, 8) // local × FX, plus 2 bps
  await expect(page.getByRole('status').filter({ hasText: /^Bought [\d.]+ 6501\.T at \$29\.8\d+ \(paper\)\.$/ })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Positions (2)' })).toBeVisible() // the seeded ETH long and this one
  await expect(page.getByRole('button', { name: 'Close 6501.T position at market' })).toBeVisible()
  expect(binance).toHaveLength(0) // never a Binance socket for a Yahoo venue
  await expectAccessible(page)
})

for (const scheme of ['light', 'dark'] as const) {
  test(`the market list groups the listed stocks by venue, without sideways scroll (${scheme}) @mobile`, async ({ page, isMobile }) => {
    await page.emulateMedia({ colorScheme: scheme })
    await page.goto('/trade/6501.T')
    await expect(hitachi(page)).toBeVisible()
    await expect(page.locator('html')).toHaveAttribute('data-theme', scheme)
    if (isMobile) await page.getByRole('button', { name: 'Switch market: 6501.T Hitachi, Ltd.' }).click()
    const markets = isMobile ? page.getByRole('dialog', { name: 'Switch market' }) : page.getByRole('complementary', { name: 'Markets' })
    const tse = markets.getByRole('list', { name: 'Japan · TSE' })
    await expect(tse.getByRole('link', { name: /^6501\.T/ })).toHaveAttribute('aria-current', 'page')
    await expect(tse.getByRole('link', { name: /^6501\.T/ })).toBeInViewport()
    await expect(markets.getByText('delayed', { exact: true })).toHaveCount(9) // one per local venue
    // KRX: its named rows show, the listed one folds behind "Show all".
    const krx = markets.getByRole('list', { name: 'South Korea · KRX' })
    await expect(krx.locator('a[href="/trade/000810.KS"]')).toHaveCount(0)
    await expectNoHorizontalScroll(page)
    await shot(page, `market-list-${isMobile ? 375 : 1440}-${scheme}`)
    const show = markets.getByRole('button', { name: /^Show all \d+$/ }).first()
    await expect(show).toHaveAttribute('aria-expanded', 'false')
    await show.click()
    await expect(krx.locator('a[href="/trade/000810.KS"]')).toHaveCount(1)
    await expect(markets.getByRole('button', { name: 'Show fewer' })).toHaveAttribute('aria-expanded', 'true')
    await markets.getByRole('searchbox', { name: 'Search markets' }).fill('nikkei')
    await expect(markets.getByRole('link')).toHaveCount(1)
    await markets.getByRole('searchbox', { name: 'Search markets' }).fill('indonesia')
    await expect(markets.locator('a[href="/trade/BBNI.JK"]')).toHaveCount(1)
    await expectNoHorizontalScroll(page)
    await markets.getByRole('searchbox', { name: 'Search markets' }).fill('samsung fire')
    await markets.getByRole('searchbox', { name: 'Search markets' }).press('Enter')
    await expect(page).toHaveURL(/\/trade\/000810\.KS$/)
    await expect(page.getByRole('heading', { level: 1, name: 'Samsung Fire & Marine Insurance Co., Ltd.' })).toBeVisible()
    await expectNoHorizontalScroll(page)
  })

  test(`a listed stock's Trade page while its session is open and closed (${scheme}) @mobile`, async ({ page, isMobile }) => {
    await page.emulateMedia({ colorScheme: scheme })
    await page.goto('/trade/6501.T')
    await expect(hitachi(page)).toBeVisible()
    await expect(page.getByText('TSE session open', { exact: true })).toBeVisible()
    await expect(page.getByText(/^Quote feed: prices follow TSE hours\. It is open now, so fills use Yahoo's latest trade, which is delayed/).first()).toBeVisible()
    await expectNoHorizontalScroll(page)
    const px = isMobile ? 375 : 1440
    await shot(page, `trade-open-6501T-${px}-${scheme}`, true)
    await page.getByRole('button', { name: 'Manual', exact: true }).click()
    await page.getByLabel('Amount (USD)').fill('100')
    await expect(page.getByText('6501.T at $29.8722 · delayed · last trade 03:31 UTC', { exact: true })).toBeVisible()
    await page.getByText(/^Paper fills use the USD price/).scrollIntoViewIfNeeded()
    await shot(page, `trade-open-6501T-ticket-${px}-${scheme}`)
    await page.goto('/trade/000810.KS')
    await expect(page.getByRole('heading', { level: 1, name: 'Samsung Fire & Marine Insurance Co., Ltd.' })).toBeVisible()
    await expect(page.getByText('KRX session closed', { exact: true })).toBeVisible()
    await expect(page.getByText('KRX: 000810 (KOSPI 200), KRW→USD at Yahoo FX', { exact: true })).toBeVisible()
    await expectNoHorizontalScroll(page)
    await shot(page, `trade-closed-000810KS-${px}-${scheme}`, true)
    await page.getByRole('button', { name: 'Manual', exact: true }).click()
    await page.getByLabel('Amount (USD)').fill('100')
    await expect(page.getByText(/^000810\.KS at \$[\d,.]+ · last close$/)).toBeVisible()
    await expect(page.getByText(/^Paper fills use the USD price/)).toContainText('Quote feed: prices follow KRX hours. Outside them, as now, paper fills use the last close (as of Oct 13, 06:30 UTC).')
    await page.getByText(/^Paper fills use the USD price/).scrollIntoViewIfNeeded()
    await expectNoHorizontalScroll(page)
    await shot(page, `trade-closed-000810KS-ticket-${px}-${scheme}`)
  })

  test(`the screener's tradable and research-only rows (${scheme}) @mobile`, async ({ page, isMobile }) => {
    await page.emulateMedia({ colorScheme: scheme })
    await page.goto('/screener')
    const listed = page.getByRole('row', { name: /^6501\.T / })
    await expect(listed.getByRole('link', { name: 'Trade 6501.T' })).toHaveCount(2) // the stock cell and the Trade button
    await expect(page.getByRole('row', { name: /^SM\.PS / }).getByRole('button', { name: 'View SM.PS with the agent' })).toHaveAttribute('title',
      'Research only: No keyless Yahoo data for this listing (Yahoo answered 404 on 2026-10-13). Ask the agent for an analysis.')
    await page.waitForLoadState('networkidle')
    await expectNoHorizontalScroll(page)
    const px = isMobile ? 375 : 1440
    await shot(page, `screener-${px}-${scheme}`)
    await listed.scrollIntoViewIfNeeded()
    await page.getByRole('region', { name: 'Screened stocks' }).locator('.overflow-x-auto').evaluate((el) => el.scrollTo({ left: el.scrollWidth }))
    await expectNoHorizontalScroll(page) // the table scrolls inside its card, never the page
    await shot(page, `screener-rows-${px}-${scheme}`)
  })
}

test('a deep link to a listed stock survives a reload of the built bundle', async ({ page }) => {
  await page.goto('/trade/6501.T') // ".T" looks like a file extension: the preview server must still answer index.html
  await expect(hitachi(page)).toBeVisible()
  await page.reload()
  await expect(hitachi(page)).toBeVisible()
  await expect(page).toHaveURL(/\/trade\/6501\.T$/)
  await page.goto('/trade/6501.t')
  await expect(page).toHaveURL(/\/trade\/6501\.T$/)
})

test('an alert in yen, set from the Trade page without leaving it, is listed in yen', { tag: '@mobile' }, async ({ page }) => {
  await page.goto('/trade/6501.T')
  await expect(hitachi(page)).toBeVisible()
  const display = page.getByRole('group', { name: 'Display currency' })
  await expect(display.getByRole('button', { name: 'JPY', exact: true })).toHaveAttribute('aria-pressed', 'true')
  await expect(page.getByTitle('Original JPY listing price from Yahoo.')).toHaveText('JPY 4,726.00')
  await expectNoHorizontalScroll(page)
  await display.getByRole('button', { name: 'USD', exact: true }).click()
  await page.getByRole('button', { name: 'Set alert' }).click()
  const dialog = page.getByRole('dialog', { name: 'Alert me on 6501.T' })
  await expect(dialog.getByLabel('Price (JPY)')).toBeVisible()
  await expect(dialog.getByRole('radiogroup', { name: 'Price currency' })).toHaveCount(0)
  await expect(dialog.getByText('Checked on the JPY listing price, not the USD paper mark.')).toBeVisible()
  await expectAccessible(page)
  await expectNoHorizontalScroll(page)
  await dialog.getByLabel('Price (JPY)').fill('3000')
  const created = page.waitForResponse((r) => r.url().endsWith('/api/alerts') && r.request().method() === 'POST')
  await dialog.getByRole('button', { name: 'Set alert' }).click()
  const res = await created
  expect(res.status()).toBe(201)
  expect(res.request().postDataJSON()).toEqual({ symbol: '6501.T', op: 'below', price: 3000, currency: 'JPY' })
  await expect(page.getByText('Alert set · 6501.T below JPY 3,000.00')).toBeVisible()
  await expect(dialog).toBeHidden()
  await expect(page).toHaveURL(/\/trade\/6501\.T$/)

  await page.goto('/automations')
  await expect(page.getByRole('region', { name: 'Your automations' })).toContainText('6501.T falls below JPY 3,000.00')
})

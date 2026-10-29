import { expect, expectAccessible, expectNoHorizontalScroll, test } from './fixtures'
import type { Page } from './fixtures'
import { SCREENER_NOW } from '../src/test/fixtures'

const row = (page: Page, ticker: string) => page.getByRole('row', { name: new RegExp(`^${ticker.replace('.', '\\.')} `) })
const order = (page: Page) => page.getByRole('region', { name: 'Screened stocks' }).getByRole('row').evaluateAll((rows) =>
  rows.slice(1).map((r) => r.getAttribute('aria-label')!.split(' ')[0]))
const shots = process.env.LIVE_SCREENER_SHOTS // a directory for the review screenshots, when set
const shot = async (page: Page, name: string, fullPage = false) => {
  if (shots) await page.screenshot({ path: `${shots}/${name}.png`, fullPage })
}

// The mock's quotes are Wednesday 07:10 UTC's (TSE, KRX and TWSE closed; HKEX, SGX and IDX open): the browser's clock
// starts there, so which rows are fresh or stale never depends on when the suite runs.
test.beforeEach(async ({ page }) => {
  await page.clock.install({ time: new Date(SCREENER_NOW) })
})

for (const scheme of ['light', 'dark'] as const) {
  test(`the Asia screener lists its stocks with sources and the advice note (${scheme})`, { tag: '@mobile' }, async ({ page, isMobile }) => {
    await page.emulateMedia({ colorScheme: scheme })
    await page.goto('/screener')
    await expect(page.locator('html')).toHaveAttribute('data-theme', scheme)
    await expect(page.getByRole('heading', { level: 1, name: 'Asia screener' })).toBeVisible()
    await expect(page).toHaveTitle('Asia screener · Foretrade')
    await expect(page.getByRole('region', { name: 'Screened stocks' }).getByRole('row')).toHaveCount(11) // header + 10
    await expect(row(page, '005930.KS')).toContainText('₩272,000')
    await expect(row(page, '005930.KS')).toContainText('+160.4%')
    await expect(row(page, '005930.KS')).toContainText('Deepening')
    await expect(row(page, '285A.T')).toContainText('n/a, insufficient history (95 wks)')
    await expect(row(page, 'SM.PS')).toContainText('Unavailable (no data feed)')
    await expect(page.getByText(/Not financial advice\./)).toBeVisible()
    await expect(page.getByText(/^Prices: each listing's latest regular-market quote from Yahoo Finance/)).toBeVisible()
    await expect(row(page, '005930.KS')).toContainText('delayed · last trade 06:30 UTCClosed · Oct 14')
    await expect(row(page, 'BBCA.JK')).toContainText('delayed · last trade 06:58 UTCOpen · Oct 14')
    await expect(row(page, 'D01.SI')).toContainText('Stale')
    await expect(page.getByText(/^Prices refresh every minute, in place; the order is from 07:10 UTC\.$/)).toBeVisible()
    await expect(page.getByRole('main')).not.toContainText(/\blive\b|Saturday/i)
    await page.waitForLoadState('networkidle')
    await expectAccessible(page)
    await expectNoHorizontalScroll(page)
    // Review screenshots: open (IDX, HKEX) and closed (TSE, KRX, TWSE) rows, a stale one (D01.SI), research-only ones
    // (View), a new high (2330.TW), no data feed (SM.PS). On a phone the table scrolls inside its card: one shot at the
    // price columns, one at Updated, each beside the sticky stock column.
    const px = isMobile ? 375 : 1440
    await shot(page, `screener-${px}-${scheme}`)
    const region = page.getByRole('region', { name: 'Screened stocks' })
    const scrollTo = (header: string) => region.locator('.overflow-x-auto').evaluate((el, name) => {
      const ths = [...el.querySelectorAll('th')]
      const th = ths.find((t) => t.textContent?.includes(name))!
      el.scrollLeft = th.offsetLeft - ths[0].offsetWidth
    }, header)
    for (const [name, header] of isMobile ? [['price', 'Price'], ['updated', 'Updated']] : [['rows', 'Stock']]) {
      await scrollTo(header)
      await region.evaluate((el) => el.scrollIntoView({ block: 'start' }))
      await expectNoHorizontalScroll(page) // the table scrolls inside its card, never the page
      await shot(page, `screener-${name}-${px}-${scheme}`)
      await row(page, 'SM.PS').evaluate((el) => el.scrollIntoView({ block: 'end' }))
      await shot(page, `screener-${name}-end-${px}-${scheme}`)
    }
    await page.getByText('How it is computed').click()
    await page.getByText('Simulated · not financial advice').scrollIntoViewIfNeeded()
    await shot(page, `screener-footer-${px}-${scheme}`)
  })
}

test('filter tabs, the market selector, search and sorting narrow and order the table', { tag: '@mobile' }, async ({ page }) => {
  await page.goto('/screener')
  const tabs = page.getByRole('group', { name: 'Screen' })
  await tabs.getByRole('button', { name: 'Deep value 1' }).click()
  await expect(page).toHaveURL(/\/screener\?filter=deep$/)
  await expect(row(page, '0000.T')).toBeVisible()
  await expect(page.getByText('Showing 1 of 1')).toBeVisible()
  await tabs.getByRole('button', { name: 'All 10' }).click()

  await page.getByRole('combobox', { name: 'Market' }).selectOption('KR')
  await expect(page).toHaveURL(/market=KR/)
  await expect(page.getByText('Showing 1 of 1')).toBeVisible()
  await page.getByRole('combobox', { name: 'Market' }).selectOption('')

  // typed straight after the market pick: the search keeps the cleared market
  await page.getByRole('searchbox', { name: 'Search stocks' }).fill('tencent')
  await expect(page).toHaveURL(/\/screener\?q=tencent$/)
  await expect(row(page, '0700.HK')).toContainText('Old averages') // the last run failed for it; its price is the quote's
  await page.getByRole('searchbox', { name: 'Search stocks' }).fill('korea') // the market's name, as the API matches it
  await expect(page).toHaveURL(/q=korea/)
  await expect(page.getByText('Showing 1 of 1')).toBeVisible()
  await expect(row(page, '005930.KS')).toBeVisible()
  await page.getByRole('searchbox', { name: 'Search stocks' }).fill('japan')
  await expect(page.getByText('Showing 4 of 4')).toBeVisible()
  await page.getByRole('searchbox', { name: 'Search stocks' }).fill('')
  await expect(page.getByText('Showing 10 of 10')).toBeVisible()

  await page.getByRole('columnheader', { name: /RSI 14w/ }).getByRole('button').click()
  await expect(page).toHaveURL(/sort=rsi/)
  await expect(page.getByRole('region', { name: 'Screened stocks' }).getByRole('row').nth(1)).toHaveAccessibleName(/^0000\.T /) // RSI 24.6
  await expectNoHorizontalScroll(page)
})

test('prices refresh in place every minute without moving a row; Re-sort orders them again', { tag: '@mobile' }, async ({ page, isMobile }) => {
  await page.goto('/screener')
  const tencent = row(page, '0700.HK')
  await expect(tencent).toContainText('HK$428.20')
  const before = await order(page)
  expect(before.slice(0, 5)).toEqual(['0000.T', 'BBCA.JK', '0700.HK', 'D01.SI', '6501.T'])
  // Where the rows sit on screen: the note above the table changing its message must not move them.
  const tops = () => Promise.all(['0000.T', 'D01.SI', '6501.T'].map(async (t) => (await row(page, t).boundingBox())!.y))
  const at = await tops()
  const refreshed = page.waitForResponse((r) => r.url().includes('/api/screener/asia/quotes?tickers='))
  await page.clock.fastForward(60_000)
  const res = await refreshed
  expect(new URL(res.url()).searchParams.get('tickers')!.split(',')).toEqual(before.filter((t) => t !== 'SM.PS')) // the loaded rows with data
  await expect(tencent).toContainText('HK$475.30')
  await expect(tencent).toContainText('+11.7%')
  await expect(tencent).toContainText('delayed · last trade 07:09 UTC')
  expect(await order(page)).toEqual(before) // +11.7% now sorts after D01.SI's +10.4%, but nothing moved under the reader
  await expect(page.getByText('Prices moved since 07:10 UTC: the order and the tab counts are from then.')).toBeVisible()
  expect(await tops()).toEqual(at) // not even by a line: the note kept its height
  await expectNoHorizontalScroll(page)
  await page.getByRole('region', { name: 'Screened stocks' }).evaluate((el) => el.scrollIntoView({ block: 'start' }))
  await shot(page, `screener-moved-${isMobile ? 375 : 1440}-light`)
  await page.getByRole('button', { name: 'Re-sort' }).click()
  await expect.poll(() => order(page).then((o) => o.slice(0, 5))).toEqual(['0000.T', 'BBCA.JK', 'D01.SI', '0700.HK', '6501.T'])
  await expect(page.getByText(/^Prices refresh every minute, in place; the order is from 07:11 UTC\.$/)).toBeVisible()
})

test('a tradable row opens the drawdown alert dialog and the terminal; a research-only row opens the agent', async ({ page }) => {
  await page.goto('/screener')
  await row(page, '005930.KS').getByRole('button', { name: 'Set drawdown alert on SMSN' }).click()
  const dialog = page.getByRole('dialog', { name: 'Alert me on SMSN' })
  await expect(dialog.getByRole('radio', { name: 'Drop from high' })).toHaveAttribute('aria-checked', 'true')
  await expect(page).toHaveURL(/\/screener$/) // set here, without leaving the screener
  await dialog.getByRole('button', { name: 'Cancel' }).click()
  await expect(dialog).toBeHidden()
  await row(page, '2330.TW').getByRole('link', { name: 'Trade TSM (via ADR)' }).last().click() // the Trade button (the stock cell links there too)
  await expect(page).toHaveURL(/\/trade\/TSM$/)
  await page.goBack()
  await row(page, 'BBCA.JK').getByRole('button', { name: 'View BBCA.JK with the agent' }).click()
  await expect(page.getByText(/Analyse PT Bank Central Asia Tbk \(BBCA\.JK, Indonesia, IDX LQ45\)/).first()).toBeVisible() // sent as the first message
  await expect(page).toHaveURL(/:\d+\/(chat\/[^/?]+)?$/)
})

test('the row actions stay in view on a desktop screen narrower than the table', async ({ page, isMobile }) => {
  test.skip(isMobile, 'desktop layout')
  for (const width of [1440, 1024]) {
    await page.setViewportSize({ width, height: 900 })
    await page.goto('/screener')
    const box = page.getByRole('region', { name: 'Screened stocks' }).locator('.overflow-x-auto')
    for (const [role, name] of [['button', 'Set drawdown alert on TSM'], ['link', 'Trade TSM (via ADR)']] as const) {
      const action = row(page, '2330.TW').getByRole(role, { name }).last() // the actions column, not the stock cell
      await expect(action).toBeVisible()
      const [a, b] = [(await action.boundingBox())!, (await box.boundingBox())!]
      expect(a.x + a.width, `${name} at ${width} px`).toBeLessThanOrEqual(b.x + b.width)
    }
  }
})

test('Screener sits right after Trade in the rail', async ({ page }) => {
  await page.goto('/news')
  const rail = page.getByRole('navigation', { name: 'Primary' }).first()
  await expect(rail.getByRole('link')).toHaveText(['Agent', 'Trade', 'Screener', 'News', 'Signals', 'Automations', 'Forecasts', 'Strategies', 'Portfolios', 'Account'])
  await rail.getByRole('link', { name: 'Screener' }).click()
  await expect(page).toHaveURL(/\/screener$/)
  await expect(rail.getByRole('link', { name: 'Screener' })).toHaveAttribute('aria-current', 'page')
})

test('on a phone the bottom bar carries Screener after Trade', { tag: '@mobile' }, async ({ page, isMobile }) => {
  test.skip(!isMobile, 'phone layout')
  await page.goto('/news')
  const bar = page.getByRole('navigation', { name: 'Primary' }).last()
  await expect(bar.getByRole('link')).toHaveCount(8)
  await expect(bar.getByRole('link').nth(2)).toHaveAccessibleName('Screener')
  await bar.getByRole('link', { name: 'Screener' }).click()
  await expect(page).toHaveURL(/\/screener$/)
  await expectNoHorizontalScroll(page)
})

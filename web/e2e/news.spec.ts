import { expect, expectAccessible, expectNoHorizontalScroll, test, type Locator, type Page } from './fixtures'
import { API_URL, type BriefFixture } from './mock-api'

// Names match exactly: Playwright's default substring match takes 'East Asia' for 'Southeast Asia' too.
const wire = (page: Page, name: string) => page.getByRole('region', { name, exact: true }).getByRole('link')
/** A region tab, or a region button in the brief's breakdown. */
const region = (scope: Locator, name: string) => scope.getByRole('button', { name, exact: true })

test('chips filter the wire by asset and macro, and a second click goes back to all', async ({ page }) => {
  await page.goto('/news')
  await expect(page.getByTitle('Crypto Fear & Greed Index by alternative.me, updated daily')).toContainText('Fear & Greed 71 · Greed')
  const chips = page.getByRole('group', { name: 'Filter headlines' })

  await expect(wire(page, 'All headlines')).toHaveCount(5)

  await chips.getByRole('button', { name: 'ETH' }).click()
  await expect(chips.getByRole('button', { name: 'ETH' })).toHaveAttribute('aria-pressed', 'true')
  await expect(wire(page, 'ETH headlines')).toHaveCount(1)
  await expect(wire(page, 'ETH headlines')).toContainText('Ether staking deposits hit a record')

  await chips.getByRole('button', { name: 'Macro' }).click()
  await expect(wire(page, 'Macro headlines')).toContainText(['Fed holds rates', 'Bank of Japan keeps rates on hold'])

  await chips.getByRole('button', { name: 'SOL' }).click()
  await expect(page.getByText('No SOL headlines right now.')).toBeVisible()

  await chips.getByRole('button', { name: 'SOL' }).click()
  await expect(chips.getByRole('button', { name: 'All' })).toHaveAttribute('aria-pressed', 'true')
  await expect(wire(page, 'All headlines')).toHaveCount(5)
})

test('region tabs filter the wire on the server, live in the URL and combine with the chips', async ({ page }) => {
  await page.goto('/news')
  const tabs = page.getByRole('group', { name: 'Filter by region' })
  const chips = page.getByRole('group', { name: 'Filter headlines' })
  await expect(wire(page, 'All headlines')).toHaveCount(5)
  await expect(region(tabs, 'All')).toHaveAttribute('aria-pressed', 'true')

  const asked = page.waitForRequest((r) => new URL(r.url()).pathname === '/api/news' && new URL(r.url()).searchParams.get('region') === 'east_asia')
  await region(tabs, 'East Asia').click()
  await asked
  await expect(page).toHaveURL(/\/news\?region=east_asia$/)
  await expect(wire(page, 'East Asia headlines')).toHaveText(['TSMC lifts its outlook on AI chip demand', 'Bank of Japan keeps rates on hold as the yen slides'])
  const tsmc = page.getByRole('listitem').filter({ has: page.getByRole('link', { name: 'TSMC lifts its outlook on AI chip demand' }) })
  await expect(tsmc.getByText('US', { exact: true })).toBeVisible() // one story, two regions
  await expect(tsmc.getByText('East Asia', { exact: true })).toBeVisible()

  await page.reload() // the choice survives a reload
  await expect(region(tabs, 'East Asia')).toHaveAttribute('aria-pressed', 'true')
  await expect(wire(page, 'East Asia headlines')).toHaveCount(2)

  await chips.getByRole('button', { name: 'Macro' }).click()
  await expect(wire(page, 'East Asia macro headlines')).toHaveText(['Bank of Japan keeps rates on hold as the yen slides'])

  await region(tabs, 'Southeast Asia').click()
  await expect(page).toHaveURL(/\/news\?region=southeast_asia$/)
  await expect(page.getByText('No Southeast Asia macro headlines in the last 14 days.')).toBeVisible()
  await page.getByRole('button', { name: 'Show all Southeast Asia headlines' }).click()
  await expect(page.getByText('No Southeast Asia headlines in the last 14 days.')).toBeVisible()
  await page.getByRole('button', { name: 'Show every region' }).click()
  await expect(page).toHaveURL(/\/news$/)
  await expect(wire(page, 'All headlines')).toHaveCount(5)
})

for (const theme of ['light', 'dark'] as const) {
  test(`News regions ${theme}: a shared link opens its tab, the brief breaks down by region, no sideways page scroll @mobile`, async ({ page }, info) => {
    await page.addInitScript((t) => localStorage.setItem('theme', t), theme)
    await page.goto('/news?region=us')
    const tabs = page.getByRole('group', { name: 'Filter by region' })
    await expect(region(tabs, 'US')).toHaveAttribute('aria-pressed', 'true')
    await expect(wire(page, 'US headlines')).toHaveText(['Fed holds rates and signals one more cut', 'TSMC lifts its outlook on AI chip demand'])

    // On a phone the tabs scroll inside their own strip; the last one is reachable.
    const last = region(tabs, 'Southeast Asia')
    await last.scrollIntoViewIfNeeded()
    await expect(last).toBeInViewport()
    await expectNoHorizontalScroll(page)

    const breakdown = page.getByRole('list', { name: 'Brief by region' })
    await expect(breakdown.getByRole('listitem')).toHaveCount(3)
    await expect(region(breakdown, 'US')).toHaveAttribute('aria-pressed', 'true')
    await region(breakdown, 'East Asia').click()
    await expect(page).toHaveURL(/\/news\?region=east_asia$/)
    await expect(region(tabs, 'East Asia')).toHaveAttribute('aria-pressed', 'true')
    await expect(wire(page, 'East Asia headlines')).toHaveCount(2)

    await expectNoHorizontalScroll(page)
    await expectAccessible(page)
    await page.screenshot({ path: info.outputPath(`news-regions-${theme}.png`), fullPage: true })
  })
}

const briefState = async (page: Page, scenario: BriefFixture) =>
  expect((await page.request.post(`${API_URL}/__e2e/brief-fixture`, { data: { scenario } })).ok()).toBe(true)

test('with no brief the headlines stay and the page says a new brief is coming; a stale brief is tagged', { tag: '@mobile' }, async ({ page }) => {
  await briefState(page, 'none')
  await page.goto('/news')
  await expect(page.getByRole('heading', { level: 1, name: 'No brief right now.' })).toBeVisible()
  await expect(page.getByText('Market wire · brief being rewritten')).toBeVisible()
  await expect(page.getByText('The wire is quiet.')).toHaveCount(0)
  await expect(wire(page, 'All headlines')).toHaveCount(5)
  await expect(page.getByRole('list', { name: 'Sentiment by asset' }).getByRole('button').first()).toHaveAttribute('title', 'No brief right now')
  await expectNoHorizontalScroll(page)
  await expectAccessible(page)

  await briefState(page, 'stale')
  await page.reload()
  await expect(page.getByText(/^Market wire · brief from 9h ago/)).toBeVisible()
  await expect(page.getByText('stale', { exact: true })).toBeVisible()
  // A bullet whose story has left the wire still links its source, from the brief's stored copy.
  const bok = page.getByRole('listitem').filter({ hasText: 'The Bank of Korea holds as the won firms' })
  await expect(bok.getByRole('link', { name: 'en.yna.co.kr' })).toHaveAttribute('href', 'https://en.yna.co.kr/view/bok-holds')
  await expectNoHorizontalScroll(page)
  await expectAccessible(page)
})

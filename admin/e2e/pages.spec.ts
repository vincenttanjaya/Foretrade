// Every route, signed in as the admin, at 1440 px and 375 px: it renders its data with no console errors (fixtures.ts),
// passes the basic a11y checks and never scrolls sideways.
import { expect, expectAccessible, expectNoHorizontalScroll, test } from './fixtures'
import { SEED } from './mock-api'

const PAGES: { path: string; h1: string }[] = [
  { path: '/', h1: 'Platform overview.' },
  { path: '/users', h1: 'Users.' },
  { path: `/users/${SEED.demo}`, h1: 'Demo Investor' },
  { path: '/audit', h1: 'Every admin change.' },
  { path: '/settings', h1: 'Settings.' },
  { path: '/no-such-page', h1: 'Page not found' },
]

for (const { path, h1 } of PAGES) {
  test(`${path} renders cleanly`, { tag: '@mobile' }, async ({ page }) => {
    await page.goto(path)
    await expect(page.getByRole('heading', { level: 1 })).toHaveText(h1)
    await page.waitForLoadState('networkidle') // the page's own data is in before the layout checks
    await expectAccessible(page)
    await expectNoHorizontalScroll(page)
  })
}

test('the detail page is complete and its usage bars work from the keyboard', async ({ page }) => {
  await page.goto(`/users/${SEED.demo}`)
  await expect(page.getByRole('heading', { level: 1, name: 'Demo Investor' })).toBeVisible()
  await expect(page.getByText('managed', { exact: true })).toBeVisible()
  await expect(page.getByRole('region', { name: 'Forecasts' })).toContainText('Will SOL close above $200 on Dec 31?')
  const bars = page.getByRole('group', { name: /AI credits per day for demo@example.com/ }).getByRole('img')
  await bars.last().focus()
  await page.keyboard.press('Home')
  await expect(bars.first()).toBeFocused()
  await expect(bars.first()).toHaveAttribute('aria-label', /^[A-Z][a-z]{2} \d{1,2}: \d+ credits?$/)
})

test('the theme choice survives a reload', async ({ page }) => {
  await page.emulateMedia({ colorScheme: 'light' })
  await page.goto('/users')
  const html = page.locator('html')
  await expect(html).toHaveAttribute('data-theme', 'light')
  await page.getByRole('button', { name: 'Switch to dark theme' }).click()
  await expect(html).toHaveAttribute('data-theme', 'dark')
  await page.reload()
  await expect(html).toHaveAttribute('data-theme', 'dark')
  await expect(page.getByRole('button', { name: 'Switch to light theme' })).toBeVisible()
})

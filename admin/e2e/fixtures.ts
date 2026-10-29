// Shared Playwright fixtures: every test signs in to its own mock world as `as` (the admin by default; `null` = signed
// out), nothing leaves localhost, and any console error fails the test.
import { test as base, expect, type Page } from '@playwright/test'
import { ADMIN, API_URL } from './mock-api'

type Fixtures = {
  as: { email: string; password: string } | null
  allowedErrors: RegExp[] // console errors a test expects, e.g. the 401 a signed-out visit logs
}

const isLocal = (url: URL) => url.hostname === 'localhost' || url.hostname === '127.0.0.1'

export const test = base.extend<Fixtures>({
  as: [ADMIN, { option: true }],
  allowedErrors: [[], { option: true }],

  page: async ({ page, as, allowedErrors }, use) => {
    // Google Fonts gets an empty stylesheet; anything else off-box is blocked and shows up as a console error.
    await page.context().route(
      (url) => !isLocal(url),
      (route) => (route.request().resourceType() === 'stylesheet' ? route.fulfill({ contentType: 'text/css', body: '' }) : route.abort()),
    )
    const errors: string[] = []
    page.on('console', (m) => void (m.type() === 'error' && errors.push(m.text())))
    page.on('pageerror', (e) => void errors.push(e.message))
    if (as) expect((await page.request.post(`${API_URL}/api/auth/login`, { data: as })).ok()).toBe(true)

    await use(page)

    expect(errors.filter((e) => !allowedErrors.some((re) => re.test(e))), 'console errors').toEqual([])
  },
})

export { expect }
export type { Page }

/** Chrome logs every 4xx/5xx response as a console error; tests that expect one allow it with this. */
export const httpError = (status: number) => new RegExp(`status of ${status}`)

/** Basic a11y: one h1, and a name on every visible button. */
export async function expectAccessible(page: Page, { main = true } = {}) {
  if (main) await expect(page.getByRole('main')).toBeVisible()
  await expect(page.getByRole('heading', { level: 1 })).toHaveCount(1)
  for (const button of await page.getByRole('button').all()) await expect(button).toHaveAccessibleName(/\S/)
}

/** Neither the page nor the shell's scrolling <main> may scroll sideways. */
export async function expectNoHorizontalScroll(page: Page) {
  const overflow = await page.evaluate(() =>
    [document.scrollingElement, document.getElementById('main')]
      .filter((el) => el != null)
      .map((el) => ({ id: el.id || 'document', extra: el.scrollWidth - el.clientWidth }))
      .filter((o) => o.extra > 0),
  )
  expect(overflow).toEqual([])
}

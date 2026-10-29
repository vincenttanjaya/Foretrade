import { expect, expectAccessible, httpError, test, type Page } from './fixtures'
import { API_URL, DEMO } from './mock-api'

// Signed out: every first load asks /api/me, which answers 401 (Chrome logs that as a console error).
test.use({ signedIn: false, allowedErrors: [httpError(401)] })

async function signIn(page: Page, password = DEMO.password) {
  await page.getByLabel('Email').fill(DEMO.email)
  await page.getByLabel('Password', { exact: true }).fill(password)
  await page.getByRole('button', { name: 'Sign in' }).click()
}

test('a wrong password shows the error on the form', { tag: '@mobile' }, async ({ page }) => {
  await page.goto('/')
  await expect(page).toHaveURL('/login')
  await expectAccessible(page, { main: false })
  await signIn(page, 'not-the-password')
  await expect(page.getByRole('alert')).toHaveText(/That email and password don't match/)
  await expect(page.getByLabel('Password', { exact: true })).toHaveAttribute('aria-invalid', 'true')
  await expect(page).toHaveURL('/login')
})

test('signing in returns to the page that asked for it', { tag: '@mobile' }, async ({ page }) => {
  await page.goto('/forecasts')
  await expect(page).toHaveURL('/login')
  await signIn(page)
  await expect(page).toHaveURL('/forecasts')
  await expect(page.getByRole('heading', { level: 1, name: 'Your forecasts.' })).toBeVisible()
})

test('an expired session goes back to sign-in, then returns to the page', async ({ page }) => {
  await page.request.post(`${API_URL}/api/auth/login`, { data: DEMO })
  await page.goto('/forecasts')
  await expect(page.getByRole('heading', { level: 1, name: 'Your forecasts.' })).toBeVisible()

  // The session ends on the way to Strategies: its API call answers 401. Clearing the cookie here instead would let
  // a background poll (assets every 10 s, forecasts every 5 s while one runs) hit the 401 first and leave for /login
  // before the click, taking the link with it.
  const strategies = `${API_URL}/api/strategies**`
  await page.route(strategies, (r) =>
    r.fulfill({ status: 401, contentType: 'application/json', body: JSON.stringify({ error: { code: 'unauthorized', message: 'Please sign in.' } }) }),
  )
  await page.getByRole('link', { name: 'Strategies' }).click()
  await expect(page).toHaveURL('/login')
  await page.unroute(strategies)
  await page.context().clearCookies() // and the old session is really gone: the sign-in below makes a new one

  await signIn(page)
  await expect(page).toHaveURL('/strategies')
  await expect(page.getByRole('heading', { level: 1, name: 'Turn ideas into strategies.' })).toBeVisible()
})

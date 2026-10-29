// Who gets in: admins only. Everyone else is signed out and sees "Access restricted".
import { expect, expectAccessible, expectNoHorizontalScroll, httpError, test, type Page } from './fixtures'
import { ADMIN, API_URL, DEMO } from './mock-api'

async function signIn(page: Page, who: { email: string; password: string }) {
  await page.getByLabel('Email').fill(who.email)
  await page.getByLabel('Password', { exact: true }).fill(who.password)
  await page.getByRole('button', { name: 'Sign in' }).click()
}

test.describe('signed out', () => {
  // Every first load asks /api/me, which answers 401 (Chrome logs that as a console error).
  test.use({ as: null, allowedErrors: [httpError(401)] })

  test('a wrong password shows the error on the form', { tag: '@mobile' }, async ({ page }) => {
    await page.goto('/')
    await expect(page).toHaveURL('/login')
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('Operator console.')
    await expectAccessible(page, { main: false })
    await expectNoHorizontalScroll(page)
    await signIn(page, { ...ADMIN, password: 'not-the-password' })
    await expect(page.getByRole('alert')).toHaveText('Wrong email or password.')
    await expect(page.getByLabel('Email')).toHaveAttribute('aria-invalid', 'true')
  })

  test('a non-admin account is refused inline and signed out at once', async ({ page }) => {
    await page.goto('/login')
    await signIn(page, DEMO)
    await expect(page.getByRole('alert')).toHaveText('Access restricted — this console is for administrators only.')
    await expect(page).toHaveURL('/login')
    expect((await page.request.get(`${API_URL}/api/me`)).status()).toBe(401) // the session cookie is gone
  })

  test('an admin signs in and lands on the page they asked for', { tag: '@mobile' }, async ({ page }) => {
    await page.goto('/audit')
    await expect(page).toHaveURL('/login')
    await signIn(page, ADMIN)
    await expect(page).toHaveURL('/audit')
    await expect(page.getByRole('heading', { level: 1, name: 'Every admin change.' })).toBeVisible()
  })
})

test.describe('a non-admin session', () => {
  test.use({ as: DEMO })

  test('is signed out and walled off with "Access restricted"', { tag: '@mobile' }, async ({ page }) => {
    await page.goto('/users')
    await expect(page.getByRole('heading', { level: 1, name: 'Access restricted' })).toBeVisible()
    await expect(page.getByText('This console is for Foretrade administrators. Your session has been signed out.')).toBeVisible()
    await expectAccessible(page)
    await expectNoHorizontalScroll(page)
    expect((await page.request.get(`${API_URL}/api/me`)).status()).toBe(401)
    await page.getByRole('button', { name: 'Back to sign in' }).click()
    await expect(page).toHaveURL('/login')
    await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible()
  })
})

test.describe('an admin demoted mid-session', () => {
  test.use({ allowedErrors: [httpError(403)] })

  test('hits the wall on the next admin request', async ({ page }) => {
    await page.goto('/users')
    await expect(page.getByRole('heading', { level: 1, name: 'Users.' })).toBeVisible()
    expect((await page.request.post(`${API_URL}/__e2e/role`, { data: { role: 'user' } })).status()).toBe(204) // mock-only control
    await page.getByRole('link', { name: 'Audit' }).click()
    await expect(page.getByRole('heading', { level: 1, name: 'Access restricted' })).toBeVisible()
    expect((await page.request.get(`${API_URL}/api/me`)).status()).toBe(401)
  })
})

test('logging out returns to sign-in', async ({ page }) => {
  await page.goto('/')
  await expect(page.getByRole('heading', { level: 1, name: 'Platform overview.' })).toBeVisible()
  await page.getByRole('button', { name: 'Account menu' }).click()
  await expect(page.getByText('Also signs this browser out of Foretrade')).toBeVisible()
  await page.getByRole('button', { name: 'Log out' }).click()
  await expect(page).toHaveURL('/login')
  expect((await page.request.get(`${API_URL}/api/me`)).status()).toBe(401)
})

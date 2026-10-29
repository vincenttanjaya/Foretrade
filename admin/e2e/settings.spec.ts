// First-run AI setup as a self-hoster does it: pick a provider, paste a key, load models, test, save; and the ChatGPT
// sign-in, which opens in a new tab and comes back through the API's /auth/callback.
import { expect, httpError, test } from './fixtures'
import { API_URL } from './mock-api'

test.describe('settings', () => {
  test.use({ allowedErrors: [httpError(400)] }) // the refused save

  test('OpenRouter: a bad key is refused, then a good one is tested, saved and audited', { tag: '@mobile' }, async ({ page }) => {
    await page.goto('/')
    await page.getByRole('link', { name: 'choose a provider' }).click() // the overview's Model row while AI is off
    await expect(page.getByRole('heading', { level: 1, name: 'Settings.' })).toBeVisible()
    const ai = page.getByRole('form', { name: 'AI provider' })
    const card = page.locator('.card', { has: page.getByRole('heading', { name: 'AI provider' }) })
    await expect(card).toContainText('AI status: Off')

    await ai.getByLabel('Provider').selectOption('openrouter')
    await expect(ai.getByRole('link', { name: 'openrouter.ai/keys' })).toBeVisible()
    const key = ai.getByLabel('API key', { exact: true })
    const model = ai.getByLabel('Model', { exact: true })
    await key.fill('bad-key-0000000000')
    await ai.getByRole('button', { name: 'Save' }).click()
    expect(await model.evaluate((el: HTMLInputElement) => el.validity.valueMissing)).toBe(true) // no default model: the browser asks first
    await model.fill('qwen/qwen4-72b')
    await ai.getByRole('button', { name: 'Save' }).click()
    await expect(ai.getByRole('alert')).toHaveText('OpenRouter: 401 Unauthorized (invalid API key).')
    await expect(card).toContainText('AI status: Off') // nothing saved

    await key.fill('sk-or-v1-e2e-only-0000c0de')
    await ai.getByRole('button', { name: 'Load models' }).click()
    await expect(ai.getByText('3 models loaded: pick one from the list or type any id.')).toBeVisible()
    await ai.getByRole('button', { name: 'Test connection' }).click()
    await expect(ai.getByText(/answered in 420 ms/)).toHaveText('Connected: qwen/qwen4-72b answered in 420 ms.')
    await ai.getByRole('button', { name: 'Save' }).click()
    await expect(page.getByText('Saved. AI now uses OpenRouter · qwen/qwen4-72b.')).toBeVisible()
    await expect(card).toContainText('AI status: Ready')
    await expect(ai.getByRole('group', { name: 'API key' })).toContainText('Set ••••c0de')

    await page.reload()
    await expect(model).toHaveValue('qwen/qwen4-72b')
    expect(await page.content()).not.toContain('sk-or-v1-e2e-only')

    await page.getByRole('link', { name: 'Audit', exact: true }).click()
    const row = page.getByRole('table').getByRole('row').nth(1)
    await expect(row).toContainText('Updated settings')
    await expect(row).toContainText('ai.provider, ai.model, ai.apiKey')
  })

  test('a new base URL needs the key again: the saved one never goes to another host', async ({ page }) => {
    const ai = { provider: 'openrouter', model: 'qwen/qwen4-72b', apiKey: 'sk-or-v1-e2e-only-0000c0de' }
    expect((await page.request.put(`${API_URL}/api/admin/settings`, { data: { ai } })).ok()).toBe(true)
    await page.goto('/settings')
    const form = page.getByRole('form', { name: 'AI provider' })
    await expect(form.getByRole('group', { name: 'API key' })).toContainText('Set ••••c0de')

    await form.getByLabel('Base URL').fill('https://proxy.example.com/v1')
    await expect(form.getByRole('group', { name: 'API key' })).toHaveCount(0)
    await expect(form.getByText(/The saved key isn't sent to a new base URL/)).toBeVisible()
    await form.getByRole('button', { name: 'Save' }).click()
    await expect(form.getByRole('alert')).toHaveText('OpenRouter needs an API key.') // what the field showed: no key

    await form.getByLabel('API key', { exact: true }).fill('sk-or-v1-e2e-proxy-0000beef')
    await form.getByRole('button', { name: 'Save' }).click()
    await expect(page.getByText('Saved. AI now uses OpenRouter · qwen/qwen4-72b.')).toBeVisible()
    await expect(form.getByRole('group', { name: 'API key' })).toContainText('Set ••••beef')
    await expect(form.getByLabel('Base URL')).toHaveValue('https://proxy.example.com/v1')
  })

  test('ChatGPT: sign in in a new tab, come back signed in, sign out', async ({ page }) => {
    await page.goto('/settings')
    const ai = page.getByRole('form', { name: 'AI provider' })
    await ai.getByLabel('Provider').selectOption('chatgpt')
    const box = ai.getByRole('region', { name: 'ChatGPT sign-in' })
    await expect(box).toContainText('owner-only mode')

    const opened = page.waitForEvent('popup')
    await box.getByRole('button', { name: 'Sign in with ChatGPT' }).click()
    const tab = await opened
    await expect(tab.getByText('Signed in with ChatGPT.')).toBeVisible()
    await expect(tab).toHaveURL(/\/settings$/) // the callback's ?chatgpt=ok, read and dropped
    await expect(tab.getByRole('region', { name: 'ChatGPT sign-in' })).toContainText('Signed in as owner@example.com')
    await tab.close()

    await expect(box).toContainText('Finish signing in in the new tab.')
    await page.evaluate(() => window.dispatchEvent(new Event('focus'))) // headless pages never lose focus
    await expect(box).toContainText('Signed in as owner@example.com')
    await expect(ai.getByLabel('Provider', { exact: true })).toHaveValue('chatgpt')

    await box.getByRole('button', { name: 'Sign out' }).click()
    await page.getByRole('dialog', { name: 'Sign out of ChatGPT?' }).getByRole('button', { name: 'Sign out' }).click()
    await expect(page.getByText('Signed out of ChatGPT.')).toBeVisible()
    await expect(box.getByRole('button', { name: 'Sign in with ChatGPT' })).toBeVisible()
    await expect(ai.getByRole('alert')).toContainText('Sign in with ChatGPT first.')
  })
})

import { expect, expectAccessible, expectNoHorizontalScroll, httpError, test } from './fixtures'
import { API_URL } from './mock-api'
import type { Chat, ConfirmMemoryRequest, Memory, Profile } from '../src/lib/types'

const profile = async (page: import('@playwright/test').Page): Promise<Profile> => (await (await page.request.get(`${API_URL}/api/me/profile`)).json()).profile
for (const theme of ['light', 'dark'] as const) {
  test(`Profile ${theme}: explicit preferences, retained OFF data and Copilot choice @mobile`, async ({ page }, info) => {
    await page.addInitScript((value) => localStorage.setItem('theme', value), theme)
    await page.goto('/account')
    const enabled = page.getByRole('checkbox', { name: 'Personalize AI responses' })
    await expect(enabled).not.toBeChecked()
    await enabled.check()
    await page.getByRole('combobox', { name: 'Risk profile' }).selectOption('conservative')
    await page.getByRole('combobox', { name: 'Preferred trading style' }).selectOption('swing')
    await page.getByRole('textbox', { name: 'Search focus symbols' }).fill('NVIDIA')
    await page.getByRole('checkbox', { name: 'Focus NVDA' }).check()
    await page.getByRole('textbox', { name: 'Custom instructions' }).fill('Prefer liquid assets. 🧭')
    expect(await profile(page)).toMatchObject({ enabled: false, customPrompt: '', revision: 0 })
    await page.getByRole('button', { name: 'Save preferences' }).click()
    await expect(page.getByText('Preferences saved.', { exact: true })).toBeVisible()
    expect(await profile(page)).toMatchObject({ enabled: true, riskProfile: 'conservative', style: 'swing', focusSymbols: ['NVDA'], customPrompt: 'Prefer liquid assets. 🧭', revision: 1 })
    await expectAccessible(page); await expectNoHorizontalScroll(page)
    await page.screenshot({ path: info.outputPath(`profile-editor-${theme}.png`), fullPage: true })
    await page.reload()
    await expect(page.getByRole('textbox', { name: 'Custom instructions' })).toHaveValue('Prefer liquid assets. 🧭')
    await page.goto('/trade/BTC')
    await expect(page.getByRole('button', { name: 'Swing', exact: true })).toHaveAttribute('aria-pressed', 'true')
    await page.getByRole('button', { name: 'Scalp', exact: true }).click()
    const plan = page.waitForRequest((r) => r.url() === `${API_URL}/api/copilot/plan`)
    await page.getByRole('button', { name: /Ask Long/ }).click()
    expect((await plan).postDataJSON()).toEqual({ symbol: 'BTC', style: 'scalp', bias: 'long' })
    await expect(page.getByRole('button', { name: 'Use this plan' })).toBeVisible()
    await page.goto('/account')
    await enabled.uncheck()
    await page.getByRole('button', { name: 'Save preferences' }).click()
    await expect(page.getByText('Preferences saved.', { exact: true })).toBeVisible()
    expect(await profile(page)).toMatchObject({ enabled: false, style: 'swing', customPrompt: 'Prefer liquid assets. 🧭' })
    await page.goto('/trade/BTC')
    await expect(page.getByRole('button', { name: 'Day', exact: true })).toHaveAttribute('aria-pressed', 'true')
    const stored = await page.evaluate(() => Object.fromEntries(Object.keys(localStorage).map((key) => [key, localStorage.getItem(key)])))
    expect(JSON.stringify(stored)).not.toContain('Prefer liquid assets')
    expect(Object.keys(stored).some((key) => /profile|memory/i.test(key))).toBe(false)
  })

  test(`Profile ${theme}: current durable Confirm, historical receipt, deletion and reset @mobile`, async ({ page }, info) => {
    await page.addInitScript((value) => localStorage.setItem('theme', value), theme)
    expect((await page.request.post(`${API_URL}/__e2e/profile-fixture`, { data: { scenario: 'slow' } })).ok()).toBe(true)
    await page.goto('/')
    await page.getByLabel('Message the agent').fill('Remember Prefer liquid assets.')
    await page.getByLabel('Message the agent').press('Enter')
    const confirm = page.getByRole('button', { name: 'Confirm memory' })
    await expect(confirm).toBeVisible()
    await expect(confirm).toBeDisabled()
    expect((await profile(page)).memories).toEqual([])
    await expect(confirm).toBeEnabled()
    const chatURL = page.url()
    await page.reload()
    await expect(confirm).toBeEnabled()
    const credits = (await (await page.request.get(`${API_URL}/api/me`)).json()).credits.used
    await confirm.click()
    await expect(page.getByText('Memory saved', { exact: true })).toBeVisible()
    await expect(confirm).toHaveCount(0)
    expect((await profile(page)).memories.map((m) => m.text)).toEqual(['Prefer liquid assets.'])
    expect((await (await page.request.get(`${API_URL}/api/me`)).json()).credits.used).toBe(credits)
    await page.screenshot({ path: info.outputPath(`profile-memory-${theme}.png`), fullPage: true })
    await page.goto('/account')
    await page.getByRole('textbox', { name: 'Custom instructions' }).fill('Keep this draft.')
    await page.getByRole('button', { name: 'Delete memory: Prefer liquid assets.' }).click()
    await expect(page.getByText('No saved memories.', { exact: true })).toBeVisible()
    await expect(page.getByRole('textbox', { name: 'Custom instructions' })).toHaveValue('Keep this draft.')
    await page.getByRole('button', { name: 'Save preferences' }).click()
    await expect(page.getByText('Preferences saved.', { exact: true })).toBeVisible()
    await page.request.post(`${API_URL}/api/portfolio/reset`, { data: {} })
    expect(await profile(page)).toMatchObject({ customPrompt: 'Keep this draft.', memories: [], revision: 3 })
    await page.goto(chatURL)
    await expect(page.getByText('Memory deleted', { exact: true })).toBeVisible()
    await expect(confirm).toHaveCount(0)
    await expectAccessible(page); await expectNoHorizontalScroll(page)
    await page.screenshot({ path: info.outputPath(`profile-deleted-${theme}.png`), fullPage: true })
  })

  test.describe(`Profile ${theme} lost Confirm response`, () => {
    test.use({ allowedErrors: [/^Failed to load resource: net::ERR_FAILED$/] })
    test(`recovers the owned saved receipt without another POST and preserves deletion @mobile`, async ({ page }, info) => {
      await page.addInitScript((value) => localStorage.setItem('theme', value), theme)
      await page.goto('/')
      await page.getByLabel('Message the agent').fill('Remember Prefer liquid assets.')
      await page.getByLabel('Message the agent').press('Enter')
      const confirm = page.getByRole('button', { name: 'Confirm memory' })
      await expect(confirm).toBeEnabled()
      const chatURL = page.url()
      const chatPath = new URL(chatURL).pathname
      expect(chatPath).toMatch(/^\/chat\/[^/]+$/)
      // Reload through App's real chat/:chatId route, using the owned saved assistant card.
      await page.reload()
      await expect(confirm).toBeEnabled()

      const memoryURL = `${API_URL}/api/me/profile/memories`
      const ownedChatURL = `${API_URL}/api/chats/${chatPath.split('/').pop()}`
      const posts: ConfirmMemoryRequest[] = []
      const failedPosts: string[] = []
      page.on('request', (request) => {
        if (request.url() === memoryURL && request.method() === 'POST') posts.push(request.postDataJSON())
      })
      page.on('requestfailed', (request) => {
        if (request.url() === memoryURL && request.method() === 'POST') failedPosts.push(request.failure()?.errorText ?? '')
      })
      let committed: { memory: Memory; profile: Profile } | null = null
      await page.route(memoryURL, async (route) => {
        expect(route.request().method()).toBe('POST')
        // Complete the real mock mutation before losing only its delivery to the browser.
        const response = await route.fetch()
        expect(response.status()).toBe(201)
        committed = await response.json()
        await route.abort('failed')
      })
      const recovery = page.waitForResponse((response) => response.url() === ownedChatURL && response.request().method() === 'GET')
      await confirm.click()
      const recoveredChat = (await (await recovery).json()).chat as Chat
      await expect(page.getByText('Memory saved', { exact: true })).toBeVisible()
      await expect(confirm).toHaveCount(0)
      await expect(page.getByRole('alert')).toHaveCount(0)
      expect(posts).toHaveLength(1)
      expect(failedPosts).toEqual(['net::ERR_FAILED'])
      const afterRecovery = await profile(page)
      expect(afterRecovery.memories).toHaveLength(1)
      const memory = afterRecovery.memories[0]
      expect(memory.text).toBe('Prefer liquid assets.')
      expect(committed).toMatchObject({ memory, profile: { memories: [memory], revision: afterRecovery.revision } })
      const recoveredCards = recoveredChat.messages.filter((message) => message.role === 'assistant').flatMap((message) => message.cards)
      expect(recoveredCards).toContainEqual({ type: 'memory', data: { ...posts[0], status: 'confirmed', memoryId: memory.id } })
      await expectAccessible(page); await expectNoHorizontalScroll(page)
      await page.screenshot({ path: info.outputPath(`profile-lost-response-saved-${theme}.png`), fullPage: true })

      await page.goto('/account')
      await page.getByRole('button', { name: 'Delete memory: Prefer liquid assets.' }).click()
      await expect(page.getByText('No saved memories.', { exact: true })).toBeVisible()
      expect((await profile(page)).memories).toEqual([])
      await page.goto(chatURL)
      await expect(page.getByText('Memory deleted', { exact: true })).toBeVisible()
      await expect(confirm).toHaveCount(0)
      const deletedChat = (await (await page.request.get(ownedChatURL)).json()).chat as Chat
      const deletedCards = deletedChat.messages.filter((message) => message.role === 'assistant').flatMap((message) => message.cards)
      expect(deletedCards).toContainEqual({ type: 'memory', data: { ...posts[0], status: 'deleted', memoryId: memory.id } })
      expect(posts).toHaveLength(1)
      expect(failedPosts).toEqual(['net::ERR_FAILED'])
      await expectAccessible(page); await expectNoHorizontalScroll(page)
      await page.screenshot({ path: info.outputPath(`profile-lost-response-deleted-${theme}.png`), fullPage: true })
      await info.attach(`lost-confirm-recovery-${theme}.json`, {
        body: JSON.stringify({ chatPath, posts, failedPosts, committed, recoveredChat, afterRecovery, deletedChat }, null, 2),
        contentType: 'application/json',
      })
    })
  })

  test.describe(`Profile ${theme} conflict`, () => {
    test.use({ allowedErrors: [httpError(409)] })
    test(`stale explicit Save preserves draft and refetches revision @mobile`, async ({ page }) => {
    test.setTimeout(30_000)
    await page.addInitScript((value) => localStorage.setItem('theme', value), theme)
    await page.goto('/account')
    await page.getByRole('textbox', { name: 'Custom instructions' }).fill('My private draft.')
    const saved = await profile(page)
    const other = { enabled: saved.enabled, riskProfile: saved.riskProfile, style: saved.style, focusSymbols: saved.focusSymbols, customPrompt: 'Another tab.', revision: saved.revision }
    expect((await page.request.put(`${API_URL}/api/me/profile`, { data: other })).ok()).toBe(true)
    await page.getByRole('button', { name: 'Save preferences' }).click()
    await expect(page.getByRole('alert')).toContainText('Your edits are kept.')
    await expect(page.getByRole('textbox', { name: 'Custom instructions' })).toHaveValue('My private draft.')
    expect((await profile(page)).customPrompt).toBe('Another tab.')
    await page.getByRole('button', { name: 'Save preferences' }).click()
    await expect(page.getByText('Preferences saved.', { exact: true })).toBeVisible()
    expect(await profile(page)).toMatchObject({ customPrompt: 'My private draft.', revision: 2 })
    })
  })
}

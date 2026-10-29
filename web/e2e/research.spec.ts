import type { Alert, AlertRunPage, Credits } from '../src/lib/types'
import { expect, expectAccessible, expectNoHorizontalScroll, httpError, test, type Page } from './fixtures'
import { API_URL, DEMO } from './mock-api'

async function read<T>(page: Page, path: string): Promise<T> {
  const response = await page.request.get(`${API_URL}${path}`)
  expect(response.ok()).toBe(true)
  return response.json()
}

const credits = async (page: Page) => (await read<{ credits: Credits }>(page, '/api/me')).credits
const alerts = async (page: Page) => (await read<{ alerts: Alert[] }>(page, '/api/alerts?status=all')).alerts
const history = (page: Page, id: string) => read<AlertRunPage>(page, `/api/alerts/${encodeURIComponent(id)}/runs?limit=20`)

async function remaining(page: Page, count: number) {
  expect((await page.request.post(`${API_URL}/__e2e/research-fixture`, { data: { scenario: 'credits', remaining: count } })).ok()).toBe(true)
}

async function seed(page: Page, scenario: 'history' | 'paused') {
  const response = await page.request.post(`${API_URL}/__e2e/research-fixture`, { data: { scenario } })
  expect(response.ok()).toBe(true)
  return (await response.json()).alert as Alert
}

async function step(page: Page, alert: Alert, answer?: string) {
  const response = await page.request.post(`${API_URL}/__e2e/research-step`, { data: { id: alert.id, at: alert.nextRunAt, ...(answer ? { answer } : {}) } })
  expect(response.ok()).toBe(true)
  return (await response.json()).alert as Alert
}

async function researchForm(page: Page, prompt = 'Review my portfolio and the latest market catalysts.') {
  await page.getByRole('button', { name: 'Schedule', exact: true }).click()
  await page.getByRole('button', { name: 'AI research', exact: true }).click()
  await page.getByLabel('Asset', { exact: true }).selectOption('')
  await page.getByLabel('Research prompt').fill(prompt)
}

async function createFromForm(page: Page) {
  const created = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/alerts' && r.request().method() === 'POST')
  await page.getByRole('button', { name: 'Create automation' }).click()
  const response = await created
  expect(response.status()).toBe(201)
  return { alert: (await response.json()).alert as Alert, payload: response.request().postDataJSON() as Record<string, unknown> }
}

for (const theme of ['light', 'dark'] as const) {
  test(`scheduled research ${theme}: free general creation, two-credit run and Bell report sources @mobile`, async ({ page }, info) => {
    await page.addInitScript((value) => localStorage.setItem('theme', value), theme)
    await remaining(page, 2)
    await page.goto('/automations')
    await researchForm(page, '  Review my portfolio and the latest market catalysts.  ')
    await page.getByRole('button', { name: 'Weekdays', exact: true }).click()
    await page.getByLabel('Time (UTC)').fill('09:15')
    await page.getByLabel('Notify when').selectOption('changed')
    await page.getByLabel('Note', { exact: true }).fill('Portfolio research')
    await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
    await expect(page.getByText(/2 AI credits per run/)).toBeVisible()
    await expect(page.getByText(/Creating the automation is free/)).toBeVisible()
    await expect(page.getByLabel('Amount ($)')).toHaveCount(0)
    await expect(page.getByLabel('Stop', { exact: true })).toHaveCount(0)
    await expect(page.getByLabel('Target', { exact: true })).toHaveCount(0)
    expect(await alerts(page)).toEqual([])
    const before = await credits(page)
    const { alert, payload } = await createFromForm(page)
    expect(payload).toMatchObject({ trigger: 'schedule', every: 'weekdays', at: '09:15', action: 'research', prompt: 'Review my portfolio and the latest market catalysts.', notifyWhen: 'changed', source: 'manual' })
    for (const key of ['symbol', 'weekday', 'op', 'price', 'notional', 'stopLoss', 'takeProfit']) expect(payload).not.toHaveProperty(key)
    expect(await credits(page)).toEqual(before)
    const list = page.getByRole('region', { name: 'Your automations' })
    await expect(list).toContainText('Portfolio research')
    await expect(list).toContainText('Weekdays · 09:15 UTC')
    await expect(list.getByText(/^Next run /)).toBeVisible()
    await expect(list).not.toContainText('Paper research')
    await expectAccessible(page)
    await expectNoHorizontalScroll(page)
    await page.screenshot({ path: info.outputPath(`research-created-${theme}.png`), fullPage: true })

    const completed = await step(page, alert, '**Current evidence** [1].\n\nPortfolio demand remains steady.')
    expect(completed.lastRun).toMatchObject({ status: 'ok', verdict: null })
    expect((await credits(page)).used).toBe(before.used + 2)
    const reportId = completed.lastRun!.reportChatId!
    expect(reportId).toBeTruthy()
    await page.reload()
    await list.getByRole('button', { name: 'Run history' }).click()
    const panel = page.getByRole('region', { name: 'Research run history' })
    await expect(panel.getByRole('link', { name: 'Open chat' })).toHaveAttribute('href', `/chat/${reportId}`)
    await page.getByRole('button', { name: /^Notifications/ }).click()
    const feed = page.locator('#notifications')
    const report = feed.getByRole('link', { name: 'Scheduled research · Portfolio research' })
    await expect(report).toHaveCount(1)
    await expect(report).toHaveAttribute('href', `/chat/${reportId}`)
    await expect(feed.getByRole('link', { name: /Daily brief|Weekday brief|Scheduled order not filled/ })).toHaveCount(0)
    await report.click()
    await expect(page).toHaveURL(new RegExp(`/chat/${reportId}$`))
    await expect(page.getByRole('list', { name: 'Conversation' })).toContainText('Current evidence')
    await expect(page.getByRole('link', { name: '1', exact: true })).toHaveAttribute('href', 'https://www.reuters.com/markets/btc-etf')
    await expect(page.getByRole('list', { name: 'Sources' }).getByRole('link')).toHaveCount(1)
    await expect(page.getByRole('button', { name: /Confirm .*paper|Set alert|Set automation|Confirm memory/ })).toHaveCount(0)
    await expectAccessible(page)
    await expectNoHorizontalScroll(page)
    await page.screenshot({ path: info.outputPath(`research-report-${theme}.png`), fullPage: true })
  })

  for (const proposal of ['general', 'thesis'] as const) {
    test(`scheduled research ${theme}: ${proposal} proposal requires one free explicit confirmation @mobile`, async ({ page }) => {
      await page.addInitScript((value) => localStorage.setItem('theme', value), theme)
      const posts: Record<string, unknown>[] = []
      page.on('request', (request) => {
        if (new URL(request.url()).pathname === '/api/alerts' && request.method() === 'POST') posts.push(request.postDataJSON())
      })
      await page.goto('/')
      await page.getByLabel('Message the agent').fill(proposal === 'thesis' ? 'Track my thesis on BTC every weekday' : 'Schedule research every weekday')
      await page.getByLabel('Message the agent').press('Enter')
      const card = page.locator('.card').filter({ hasText: proposal === 'thesis' ? 'Thesis tracker · BTC' : 'Scheduled AI research' })
      const confirm = card.getByRole('button', { name: 'Set automation' })
      await expect(confirm).toBeEnabled()
      await expect(card).toContainText('2 AI credits per run. Setting this automation is free.')
      await expect(card).toContainText('Nothing is set until you tap')
      await expect(card).not.toContainText('No model call, 0 credits')
      if (proposal === 'thesis') {
        await expect(card).toContainText('Thesis:')
        await expect(card).toContainText('Invalidation:')
      }
      expect(posts).toEqual([])
      expect(await alerts(page)).toEqual([])
      await expect(page.getByRole('list', { name: 'Conversation' })).toHaveAttribute('aria-busy', 'false')
      const before = await credits(page) // Proposal chat cost is settled before the free confirmation.

      let release!: () => void
      const gate = new Promise<void>((resolve) => { release = resolve })
      let submitted = false
      await page.route(`${API_URL}/api/alerts`, async (route) => {
        if (route.request().method() !== 'POST') return route.continue()
        const response = await route.fetch()
        submitted = true
        await gate
        await route.fulfill({ response })
      })
      const created = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/alerts' && r.request().method() === 'POST')
      try {
        await confirm.evaluate((button) => { (button as HTMLButtonElement).click(); (button as HTMLButtonElement).click() })
        await expect.poll(() => submitted).toBe(true)
        await expect(confirm).toBeDisabled()
        expect(posts).toHaveLength(1)
      } finally {
        release()
      }
      expect((await created).status()).toBe(201)
      await expect(card.getByRole('status')).toContainText('Automation set')
      await expect(confirm).toHaveCount(0)
      const stored = (await alerts(page)).filter((alert) => alert.action === 'research')
      expect(stored).toHaveLength(1)
      expect(posts[0]).toMatchObject({ trigger: 'schedule', action: 'research', source: 'agent', proposalId: stored[0].proposalId, prompt: stored[0].prompt, notifyWhen: stored[0].notifyWhen })
      for (const key of ['notional', 'stopLoss', 'takeProfit', 'op', 'price']) expect(posts[0]).not.toHaveProperty(key)
      if (proposal === 'thesis') expect(posts[0]).toMatchObject({ symbol: 'BTC', thesis: stored[0].thesis })
      else expect(posts[0]).not.toHaveProperty('symbol')
      expect(await credits(page)).toEqual(before)
      await page.reload()
      await expect(card.getByRole('status')).toContainText('Active')
      await expect(confirm).toHaveCount(0)
      expect(posts).toHaveLength(1)
      expect((await alerts(page)).filter((alert) => alert.action === 'research')).toHaveLength(1)
      await expectNoHorizontalScroll(page)
    })
  }

  test.describe(`scheduled research ${theme}: quota`, () => {
    test.use({ allowedErrors: [httpError(429)] })
    test('three quota refusals pause; Resume checks two credits without spending, and Cancel is final @mobile', async ({ page }, info) => {
      await page.addInitScript((value) => localStorage.setItem('theme', value), theme)
      await remaining(page, 2)
      await page.goto('/automations')
      await researchForm(page)
      await page.getByLabel('Note', { exact: true }).fill('Quota research')
      const { alert: created } = await createFromForm(page)
      const before = await credits(page)
      let alert = await step(page, created)
      expect((await credits(page)).used).toBe(before.used + 2)
      for (const streak of [1, 2, 3]) {
        alert = await step(page, alert)
        expect(alert.quotaStreak).toBe(streak)
        expect(alert.lastRun?.status).toBe('failed')
        expect(alert.status).toBe(streak === 3 ? 'paused' : 'active')
        expect((await credits(page)).used).toBe(before.used + 2)
      }
      const notices = await read<{ items: { kind: string }[] }>(page, '/api/notifications')
      expect(notices.items.filter((n) => n.kind === 'workflow_research')).toHaveLength(1)
      expect(notices.items.filter((n) => n.kind === 'workflow_failed')).toHaveLength(0)
      await page.goto(`/automations?tab=history&alert=${alert.id}`)
      const row = page.getByRole('region', { name: 'Selected automation' })
      await expect(row).toContainText('Paused')
      await expect(row).toContainText('3 consecutive runs')
      await expect(row.getByRole('button', { name: 'Cancel', exact: true })).toBeEnabled()
      for (const count of [0, 1]) {
        await remaining(page, count)
        const refused = page.waitForResponse((r) => new URL(r.url()).pathname === `/api/alerts/${alert.id}/resume` && r.request().method() === 'POST')
        await row.getByRole('button', { name: 'Resume' }).click()
        expect((await refused).status()).toBe(429)
        await expect(row.getByRole('alert')).toContainText(/credits|quota/i)
        await expect(row).toContainText('Paused')
        expect((await alerts(page)).find((a) => a.id === alert.id)?.status).toBe('paused')
      }
      await page.screenshot({ path: info.outputPath(`research-paused-${theme}.png`), fullPage: true })
      await remaining(page, 2)
      const beforeResume = await credits(page)
      const resumed = page.waitForResponse((r) => new URL(r.url()).pathname === `/api/alerts/${alert.id}/resume` && r.request().method() === 'POST')
      await row.getByRole('button', { name: 'Resume' }).click()
      expect((await resumed).status()).toBe(200)
      expect(await credits(page)).toEqual(beforeResume)
      await page.goto(`/automations?alert=${alert.id}`)
      await expect(row).toContainText('Active')
      await expect(row.getByRole('button', { name: 'Resume' })).toHaveCount(0)
      await row.getByRole('button', { name: 'Cancel', exact: true }).click()
      await expect(page.getByRole('status').filter({ hasText: 'Automation cancelled.' })).toBeVisible()
      await page.goto(`/automations?tab=history&alert=${alert.id}`)
      await expect(row).toContainText('Cancelled')
      await expect(row.getByRole('button', { name: 'Resume' })).toHaveCount(0)
      await expect(row.getByRole('button', { name: 'Cancel', exact: true })).toHaveCount(0)
      await expectAccessible(page)
      await expectNoHorizontalScroll(page)
    })
  })

  test(`scheduled research ${theme}: owned paginated history, private sources and deleted report replacement @mobile`, async ({ page, browser }, info) => {
    await page.addInitScript((value) => localStorage.setItem('theme', value), theme)
    const alert = await seed(page, 'history')
    await page.goto(`/automations?alert=${alert.id}`)
    const row = page.getByRole('region', { name: 'Selected automation' })
    await row.getByRole('button', { name: 'Run history' }).click()
    const panel = page.getByRole('region', { name: 'Research run history' })
    await expect(panel.getByRole('link', { name: 'Open chat' })).toHaveCount(20)
    const firstPage = await history(page, alert.id)
    expect(firstPage.runs).toHaveLength(20)
    expect(firstPage.nextCursor).toBeTruthy()
    const reportId = firstPage.runs[0].reportChatId!
    const reportPath = `/chat/${reportId}`
    const olderResponse = page.waitForResponse((r) => new URL(r.url()).pathname === `/api/alerts/${alert.id}/runs` && new URL(r.url()).searchParams.has('before'))
    await panel.getByRole('button', { name: 'Load older' }).click()
    const older = await olderResponse
    expect(new URL(older.url()).searchParams.get('before')).toBe(firstPage.nextCursor)
    const olderPage = await older.json() as AlertRunPage
    expect(olderPage.runs).toHaveLength(5)
    expect(olderPage.nextCursor).toBeNull()
    expect(new Set([...firstPage.runs, ...olderPage.runs].map((run) => run.id)).size).toBe(25)
    await expect(panel.getByRole('link', { name: 'Open chat' })).toHaveCount(25)
    await expect(panel.getByRole('button', { name: 'Load older' })).toHaveCount(0)
    await expectNoHorizontalScroll(page)
    await page.screenshot({ path: info.outputPath(`research-history-${theme}.png`), fullPage: true })

    const other = await browser.newContext()
    try {
      expect((await other.request.post(`${API_URL}/api/auth/login`, { data: DEMO })).ok()).toBe(true)
      expect((await other.request.get(`${API_URL}/api/alerts/${alert.id}/runs?limit=20`)).status()).toBe(404)
      expect((await other.request.get(`${API_URL}/api/chats/${reportId}`)).status()).toBe(404)
    } finally {
      await other.close()
    }
    await panel.getByRole('link', { name: 'Open chat' }).first().click()
    await expect(page).toHaveURL(new RegExp(`${reportPath}$`))
    const latestReport = page.getByRole('list', { name: 'Conversation' }).locator(':scope > li').last()
    await expect(latestReport).toContainText('Current evidence')
    await expect(latestReport.getByRole('link', { name: '1', exact: true })).toHaveAttribute('href', 'https://www.reuters.com/markets/btc-etf')
    await expect(latestReport.getByRole('list', { name: 'Sources' })).toBeVisible()
    expect((await page.request.delete(`${API_URL}/api/chats/${reportId}`, { headers: { 'Content-Type': 'application/json' } })).status()).toBe(204)
    await page.goto(`/automations?alert=${alert.id}`)
    await row.getByRole('button', { name: 'Run history' }).click()
    await expect(panel.getByRole('link', { name: 'Open chat' })).toHaveCount(0)
    await expect(panel.getByText(/Report chat unavailable/).first()).toBeVisible()
    expect((await history(page, alert.id)).runs.every((run) => run.reportChatId === null)).toBe(true)
    await page.getByRole('button', { name: /^Notifications/ }).click()
    await expect(page.locator('#notifications').locator(`a[href="${reportPath}"]`)).toHaveCount(0)
    await page.keyboard.press('Escape')
    const replacement = await step(page, alert, '**Fresh evidence** [1].\n\nVerdict: weakened — demand slowed')
    const replacementId = replacement.lastRun!.reportChatId!
    expect(replacementId).toBeTruthy()
    expect(replacementId).not.toBe(reportId)
    await page.reload()
    await row.getByRole('button', { name: 'Run history' }).click()
    await expect(panel.getByRole('link', { name: 'Open chat' })).toHaveCount(1)
    await expect(panel.getByRole('link', { name: 'Open chat' })).toHaveAttribute('href', `/chat/${replacementId}`)
    await panel.getByRole('link', { name: 'Open chat' }).click()
    await expect(page.getByRole('list', { name: 'Conversation' })).toContainText('Fresh evidence')
    await expect(page.getByRole('link', { name: '1', exact: true })).toHaveAttribute('href', 'https://www.reuters.com/markets/btc-etf')
    await expectAccessible(page)
    await expectNoHorizontalScroll(page)
  })
}

test('research prompt and both thesis fields use Unicode character bounds before one valid submission @mobile', async ({ page }) => {
  let posts = 0
  page.on('request', (request) => { if (new URL(request.url()).pathname === '/api/alerts' && request.method() === 'POST') posts++ })
  await page.goto('/automations')
  await researchForm(page)
  const prompt = page.getByLabel('Research prompt')
  const create = page.getByRole('button', { name: 'Create automation' })
  for (const [count, valid] of [[9, false], [10, true], [500, true], [501, false]] as const) {
    await prompt.fill('🪙'.repeat(count))
    await expect(prompt).toHaveValue('🪙'.repeat(count))
    if (valid) await expect(create).toBeEnabled()
    else await expect(create).toBeDisabled()
  }
  expect(posts).toBe(0)
  await prompt.fill('🪙'.repeat(500))
  await page.getByRole('checkbox', { name: 'Track a thesis' }).check()
  const thesis = page.getByLabel('Thesis', { exact: true })
  const invalidation = page.getByLabel('Invalidation', { exact: true })
  await thesis.fill('🪙'.repeat(300))
  await invalidation.fill('🪙'.repeat(300))
  await expect(create).toBeDisabled() // A thesis needs an integrated asset.
  await page.getByLabel('Asset', { exact: true }).selectOption('NVDA')
  await expect(create).toBeEnabled()
  for (const field of [thesis, invalidation]) {
    await field.fill('🪙'.repeat(301))
    await expect(field).toHaveValue('🪙'.repeat(301))
    await expect(create).toBeDisabled()
    await field.fill('🪙'.repeat(300))
    await expect(create).toBeEnabled()
  }
  await page.getByLabel('Notify when').selectOption('invalidated')
  // Valid unbroken text must fit the created pipeline as well as the textarea.
  await prompt.fill('x'.repeat(500))
  const { payload } = await createFromForm(page)
  expect(payload).toMatchObject({ symbol: 'NVDA', action: 'research', prompt: 'x'.repeat(500), thesis: { statement: '🪙'.repeat(300), invalidation: '🪙'.repeat(300) }, notifyWhen: 'invalidated' })
  expect(posts).toBe(1)
  await expectNoHorizontalScroll(page)
})

for (const [scenario, message] of [
  ['capacity', 'You have 3 active research schedules. Cancel one first.'],
  ['shared-capacity', 'You have 20 active alerts. Cancel one first.'],
] as const) {
  test.describe(`research ${scenario}`, () => {
    test.use({ allowedErrors: [httpError(409)] })
    test('Create and Resume show the active cap without spending; Cancel frees capacity @mobile', async ({ page }) => {
      const paused = await seed(page, 'paused')
      expect((await page.request.post(`${API_URL}/__e2e/research-fixture`, { data: { scenario } })).ok()).toBe(true)
      const before = await credits(page)
      await page.goto('/automations')
      await researchForm(page)
      const refused = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/alerts' && r.request().method() === 'POST')
      await page.getByRole('button', { name: 'Create automation' }).click()
      expect((await refused).status()).toBe(409)
      await expect(page.getByRole('alert')).toContainText(message)
      await expect(page.getByRole('button', { name: 'Create automation' })).toBeEnabled()
      expect(await credits(page)).toEqual(before)
      await page.goto(`/automations?tab=history&alert=${paused.id}`)
      const row = page.getByRole('region', { name: 'Selected automation' })
      const resumeRefused = page.waitForResponse((r) => new URL(r.url()).pathname === `/api/alerts/${paused.id}/resume` && r.request().method() === 'POST')
      await row.getByRole('button', { name: 'Resume' }).click()
      expect((await resumeRefused).status()).toBe(409)
      await expect(row.getByRole('alert')).toContainText(message)
      await expect(row).toContainText('Paused')
      expect(await credits(page)).toEqual(before)
      const active = (await alerts(page)).find((alert) => alert.status === 'active')!
      await page.goto(`/automations?alert=${active.id}`)
      await page.getByRole('region', { name: 'Selected automation' }).getByRole('button', { name: 'Cancel', exact: true }).click()
      await expect(page.getByRole('status').filter({ hasText: 'Automation cancelled.' })).toBeVisible()
      await page.goto(`/automations?tab=history&alert=${paused.id}`)
      const resumed = page.waitForResponse((r) => new URL(r.url()).pathname === `/api/alerts/${paused.id}/resume` && r.request().method() === 'POST')
      await row.getByRole('button', { name: 'Resume' }).click()
      expect((await resumed).status()).toBe(200)
      expect((await alerts(page)).find((alert) => alert.id === paused.id)?.status).toBe('active')
      expect(await credits(page)).toEqual(before)
      await expectNoHorizontalScroll(page)
    })
  })
}

test('a deterministic weekday brief still creates and runs for zero credits with AI quota exhausted @mobile', async ({ page }) => {
  await remaining(page, 0)
  const before = await credits(page)
  await page.goto('/automations')
  await page.getByRole('button', { name: 'Schedule', exact: true }).click()
  await page.getByRole('button', { name: 'Weekdays', exact: true }).click()
  await expect(page.getByRole('button', { name: 'Brief me', exact: true })).toHaveAttribute('aria-pressed', 'true')
  await expect(page.getByText(/No model call, 0 credits/)).toBeVisible()
  const { alert, payload } = await createFromForm(page)
  expect(payload).toMatchObject({ trigger: 'schedule', action: 'notify', every: 'weekdays', symbol: 'BTC' })
  for (const key of ['prompt', 'thesis', 'notifyWhen', 'notional']) expect(payload).not.toHaveProperty(key)
  expect(await credits(page)).toEqual(before)
  const completed = await step(page, alert)
  expect(completed.lastRun?.status).toBe('ok')
  expect(completed.lastRun?.reportChatId).toBeNull()
  expect(await credits(page)).toEqual(before)
  await page.reload()
  await expect(page.getByRole('region', { name: 'Your automations' })).toContainText('BTC $')
  await expect(page.getByRole('button', { name: 'Run history' })).toHaveCount(0)
  await page.getByRole('button', { name: /^Notifications/ }).click()
  await expect(page.locator('#notifications').getByRole('link', { name: 'Weekday brief · BTC' })).toHaveAttribute('href', '/automations')
  await expect(page.locator('#notifications').getByRole('link', { name: /^Scheduled research/ })).toHaveCount(0)
  await expectNoHorizontalScroll(page)
})

test('a late pre-Cancel automation read cannot revive cancelled research @mobile', async ({ page }) => {
  await page.goto('/automations')
  await researchForm(page)
  const { alert } = await createFromForm(page)
  let release!: () => void
  const gate = new Promise<void>((resolve) => { release = resolve })
  let fetched = false
  let interceptedRequest: import('@playwright/test').Request | undefined
  let intercepted = false
  await page.route(`${API_URL}/api/alerts?status=all`, async (route) => {
    if (intercepted) return route.continue()
    intercepted = true
    interceptedRequest = route.request()
    const response = await route.fetch()
    fetched = true
    await gate
    await route.fulfill({ response })
  })
  const staleRead = page.waitForResponse((response) => response.request() === interceptedRequest)
  try {
    await page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')))
    await expect.poll(() => fetched).toBe(true)
    await page.getByRole('region', { name: 'Your automations' }).getByRole('button', { name: 'Cancel', exact: true }).click()
    await expect(page.getByRole('status').filter({ hasText: 'Automation cancelled.' })).toBeVisible()
  } finally {
    release()
  }
  await (await staleRead).finished()
  await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => resolve())))
  await page.getByRole('region', { name: 'Your automations' }).getByRole('button', { name: 'History · 1' }).click()
  await expect(page.getByRole('region', { name: 'Your automations' })).toContainText('Cancelled')
  await expect(page.getByRole('button', { name: 'Cancel', exact: true })).toHaveCount(0)
  expect((await alerts(page)).find((a) => a.id === alert.id)?.status).toBe('cancelled')
  await expectNoHorizontalScroll(page)
})

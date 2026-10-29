import type { Alert, PredictionBoard, PredictionDraft } from '../src/lib/types'
import * as fx from '../src/test/fixtures'
import { expect, expectAccessible, expectNoHorizontalScroll, httpError, test } from './fixtures'
import { API_URL } from './mock-api'

test('prediction sample preserves category/sort controls and scoped ladders in both themes @mobile', async ({ page }, info) => {
  await page.goto('/forecasts?tab=markets')
  await expect(page.getByRole('heading', { name: 'What the crowd is pricing.' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Both', exact: true })).toHaveAttribute('aria-pressed', 'true')
  await expect(page.getByRole('heading', { name: 'Bitcoin weekly price' })).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Fed next meeting' })).toBeVisible()
  await expect(page.getByText('Prior touch history is unverified.')).toBeVisible()
  await expect(page.getByRole('button', { name: /stake|buy yes|buy no|Confirm \(paper\)/i })).toHaveCount(0)
  for (const theme of ['light', 'dark'] as const) {
    if ((await page.locator('html').getAttribute('data-theme')) !== theme) {
      if (info.project.name === 'mobile') {
        await page.getByRole('button', { name: 'Account menu' }).click()
        await page.getByRole('button', { name: `${theme === 'dark' ? 'Dark' : 'Light'} theme`, exact: true }).click()
        await page.getByRole('button', { name: 'Account menu' }).click()
      } else await page.getByRole('button', { name: `Switch to ${theme} theme` }).click()
    }
    await expectAccessible(page)
    await expectNoHorizontalScroll(page)
    await page.screenshot({ path: info.outputPath(`predictions-${theme}.png`), fullPage: true })
  }
  await page.getByRole('button', { name: 'Macro', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Fed next meeting' })).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Bitcoin weekly price' })).toHaveCount(0)
  const read = page.waitForRequest((r) => r.url().includes('/api/predictions/markets?category=macro&sort=ending'))
  await page.getByLabel('Sort markets').selectOption('ending')
  await read
  await page.reload()
  await expect(page.getByRole('button', { name: 'Markets', exact: true })).toHaveAttribute('aria-pressed', 'true')
})

test('canonical below deep link reloads free, runs once and preserves manual snapshot through update @mobile', async ({ page }) => {
  const calls: string[] = []
  const postedAsOf = '2026-10-01T12:00:00.000Z'
  await page.route(`${API_URL}/api/forecasts`, async (route) => {
    if (route.request().method() !== 'POST') return route.continue()
    const body = route.request().postDataJSON()
    await route.continue({ postData: JSON.stringify({ ...body, linkedMarket: { ...body.linkedMarket, asOf: postedAsOf } }) })
  })
  page.on('request', (r) => { if (r.method() === 'POST') calls.push(new URL(r.url()).pathname) })
  const canonical = await (await page.request.get(`${API_URL}/api/predictions/markets/124/draft`)).json() as PredictionDraft
  await page.goto('/forecasts/new?marketId=124')
  await expect(page.getByLabel('Question', { exact: true })).toHaveValue(canonical.draft.question)
  await expect(page.getByLabel('Resolution criteria')).toHaveValue(canonical.draft.resolutionCriteria)
  await expect(page.getByLabel('Tracking date', { exact: true })).toHaveValue(canonical.draft.resolutionDate)
  await expect(page.getByText(/Manual resolution in Foretrade/)).toBeVisible()
  await expect(page.getByText(/Auto-resolves from Binance/)).toHaveCount(0)
  await page.reload()
  await expect(page.getByLabel('Resolution criteria')).toHaveValue(canonical.draft.resolutionCriteria)
  expect(calls).not.toContain('/api/forecasts/draft')
  const used = await page.locator('[aria-describedby="credits-tip"]').textContent()
  await page.getByRole('button', { name: /Run forecast/ }).click()
  await expect(page).toHaveURL(/\/forecasts\/[\w-]+$/)
  await expect(page.getByRole('status').filter({ hasText: 'The desk has a number.' })).toBeVisible({ timeout: 10_000 })
  const report = page.getByRole('region', { name: 'Desk vs market' })
  await expect(report).toContainText('Market 20%')
  await expect(page.getByRole('region', { name: 'Linked Polymarket contract' })).toContainText(canonical.draft.linkedMarket!.endDate)
  expect(calls.filter((p) => p === '/api/forecasts')).toHaveLength(1)
  expect(await page.locator('[aria-describedby="credits-tip"]').textContent()).not.toBe(used)
  const firstId = new URL(page.url()).pathname.split('/').at(-1)!
  const firstSaved = await (await page.request.get(`${API_URL}/api/forecasts/${firstId}`)).json()
  expect(firstSaved.forecast.linkedMarket.asOf).not.toBe(postedAsOf)
  await page.getByRole('button', { name: /Update forecast/ }).click()
  await expect(page).not.toHaveURL(new RegExp(`/forecasts/${firstId}$`))
  await expect(page.getByRole('region', { name: 'Linked Polymarket contract' })).toContainText(canonical.draft.linkedMarket!.endDate)
  const saved = await (await page.request.get(`${API_URL}${new URL(page.url()).pathname.replace('/forecasts/', '/api/forecasts/')}`)).json()
  expect(saved.forecast.linkedMarket).toEqual(firstSaved.forecast.linkedMarket)
  expect(saved.forecast.priceRule).toBeNull()
  await expectNoHorizontalScroll(page)
})

for (const timing of ['existing', 'late'] as const) {
  test(`one paid create owns the review while an ${timing} fresh preview is available @mobile`, async ({ page }) => {
    let releaseCreate!: () => void
    const createGate = new Promise<void>((resolve) => { releaseCreate = resolve })
    let creates = 0
    await page.route(`${API_URL}/api/forecasts`, async (route) => {
      if (route.request().method() !== 'POST') return route.continue()
      creates++
      await createGate
      await route.continue()
    })
    await page.goto('/forecasts/new?marketId=123')
    await expect(page.getByLabel('Question', { exact: true })).toBeVisible()
    const rules = await page.getByLabel('Resolution criteria').inputValue()
    await page.getByRole('button', { name: 'Add context for the desk (optional)' }).click()
    await page.getByLabel('Context', { exact: true }).fill('Keep my notes while the request owns this review')
    let releaseRead!: () => void
    const readGate = new Promise<void>((resolve) => { releaseRead = resolve })
    await page.route(`${API_URL}/api/predictions/markets/123/draft`, async (route) => {
      if (timing === 'late') await readGate
      await route.continue()
    })
    const freshRead = page.waitForRequest(`${API_URL}/api/predictions/markets/123/draft`)
    await page.getByRole('button', { name: 'Review fresh market rules' }).click()
    await freshRead
    if (timing === 'existing') await expect(page.getByRole('button', { name: 'Use fresh market draft' })).toBeVisible()
    await page.getByRole('button', { name: /Run forecast/ }).click()
    await expect(page.getByRole('button', { name: /Starting/ })).toBeDisabled()
    if (timing === 'late') releaseRead()
    await expect(page.getByRole('button', { name: 'Use fresh market draft' })).toBeDisabled()
    await expect(page.getByLabel('Resolution criteria')).toHaveValue(rules)
    await expect(page.getByLabel('Context', { exact: true })).toHaveValue('Keep my notes while the request owns this review')
    await expect.poll(() => creates).toBe(1)
    releaseCreate()
    await expect(page).toHaveURL(/\/forecasts\/[\w-]+$/)
    expect(creates).toBe(1)
  })
}

test('predicate edit keeps full over-cap rules and requires explicit free canonical reconfirmation @mobile', async ({ page }) => {
  await page.goto('/forecasts/new?marketId=123')
  const rules = await page.getByLabel('Resolution criteria').inputValue()
  expect(Array.from(rules).length).toBeGreaterThan(2000)
  await page.getByLabel('Question', { exact: true }).fill('Will Bitcoin close below a different threshold?')
  await expect(page.getByText(/Market link removed/)).toBeVisible()
  await expect(page.getByLabel('Resolution criteria')).toHaveValue(rules)
  await expect(page.getByText(/within 2,000 characters/)).toBeVisible()
  await expect(page.getByRole('button', { name: /Run forecast/ })).toBeDisabled()
  await page.getByRole('button', { name: 'Review fresh market rules' }).click()
  await expect(page.getByRole('button', { name: 'Use fresh market draft' })).toBeVisible()
  await expect(page.getByLabel('Question', { exact: true })).toHaveValue('Will Bitcoin close below a different threshold?')
  await page.getByRole('button', { name: 'Use fresh market draft' }).click()
  await expect(page.getByRole('region', { name: 'Linked Polymarket contract' })).toBeVisible()
  await expect(page.getByRole('button', { name: /Run forecast/ })).toBeEnabled()
  await page.getByRole('button', { name: 'Back to markets' }).click()
  await expect(page).toHaveURL(/\/forecasts\?tab=markets$/)
})

test('a saved failed linked draft keeps provenance and free reconfirmation when edited @mobile', async ({ page }) => {
  const canonical = await (await page.request.get(`${API_URL}/api/predictions/markets/123/draft`)).json() as PredictionDraft
  const failed = fx.forecast({ ...canonical.draft, id: 'linked-failed', status: 'failed', result: null, error: 'The provider read failed.', context: 'Saved private notes' })
  await page.route('**/api/forecasts/linked-failed', (r) => r.fulfill({ json: { forecast: failed } }))
  await page.goto('/forecasts/linked-failed')
  await page.getByRole('link', { name: 'Edit question', exact: true }).click()
  await expect(page.getByRole('region', { name: 'Linked Polymarket contract' })).toContainText(canonical.draft.linkedMarket!.endDate)
  await expect(page.getByLabel('Resolution criteria')).toHaveValue(canonical.draft.resolutionCriteria)
  await expect(page.getByLabel('Context', { exact: true })).toHaveValue('Saved private notes')
  await page.getByRole('button', { name: 'Review fresh market rules' }).click()
  await page.getByRole('button', { name: 'Use fresh market draft' }).click()
  await expect(page.getByLabel('Context', { exact: true })).toHaveValue('Saved private notes')
  await page.getByRole('button', { name: 'Back to markets' }).click()
  await expect(page).toHaveURL(/\/forecasts\?tab=markets$/)
})

test.describe('prediction refusals', () => {
  // A two-item array is interpreted as a fixture tuple; one combined expression stays an array value.
  test.use({ allowedErrors: [new RegExp(`${httpError(409).source}|${httpError(502).source}`)] })
  test('changed rules and unavailable rereads preserve edits without charging or silently replacing them @mobile', async ({ page }) => {
    await page.route('**/api/forecasts', (r) => r.request().method() === 'POST'
      ? r.fulfill({ status: 409, json: { error: { code: 'conflict', message: 'The linked market rules changed. Review a fresh draft.' } } }) : r.continue())
    await page.goto('/forecasts/new?marketId=123')
    const rules = await page.getByLabel('Resolution criteria').inputValue()
    await page.getByRole('button', { name: 'Add context for the desk (optional)' }).click()
    await page.getByLabel('Context', { exact: true }).fill('Preserve my private notes')
    const credits = await page.locator('[aria-describedby="credits-tip"]').textContent()
    await page.getByRole('button', { name: /Run forecast/ }).click()
    await expect(page.getByRole('alert')).toContainText('rules changed')
    await expect(page.getByLabel('Resolution criteria')).toHaveValue(rules)
    await expect(page.locator('[aria-describedby="credits-tip"]')).toHaveText(credits!)
    await page.route('**/api/predictions/markets/123/draft', (r) => r.fulfill({ status: 502, json: { error: { code: 'upstream', message: 'Canonical provider unavailable' } } }))
    await page.getByRole('button', { name: 'Review fresh market rules' }).click()
    await expect(page.getByRole('alert').filter({ hasText: 'Canonical provider unavailable' })).toBeVisible()
    await expect(page.getByLabel('Resolution criteria')).toHaveValue(rules)
    await expect(page.getByLabel('Context', { exact: true })).toHaveValue('Preserve my private notes')
    await expect(page.locator('[aria-describedby="credits-tip"]')).toHaveText(credits!)
  })

  test('first-read retry and failed refresh retain only an explicitly stale sample @mobile', async ({ page }) => {
    let fail = true
    await page.route('**/api/predictions/markets?*', (r) => fail
      ? r.fulfill({ status: 502, json: { error: { code: 'upstream', message: 'Market provider unavailable' } } }) : r.continue())
    await page.goto('/forecasts?tab=markets')
    await expect(page.getByRole('alert')).toContainText('Market provider unavailable')
    fail = false
    await page.getByRole('button', { name: 'Retry markets' }).click()
    await expect(page.getByRole('heading', { name: 'Bitcoin weekly price' })).toBeVisible()
    const asOf = await page.getByRole('status').filter({ hasText: /^Read / }).textContent()
    fail = true
    await page.getByRole('button', { name: 'Refresh markets' }).click()
    await expect(page.getByRole('status').filter({ hasText: /^Read / })).toContainText(asOf!)
    await expect(page.getByText(/Showing an older sample/)).toBeVisible()
    await expect(page.getByText(/EV per \$100/)).toHaveCount(0)
  })
})

test('direct leaf alert is free and an explicit fired fixture follows Bell into its owned History @mobile', async ({ page }) => {
  const paid: string[] = []
  page.on('request', (r) => { if (r.method() === 'POST' && /\/api\/(forecasts|chat\/stream|orders)/.test(r.url())) paid.push(r.url()) })
  await page.goto('/forecasts?tab=markets')
  const board = await (await page.request.get(`${API_URL}/api/predictions/markets`)).json() as PredictionBoard
  const below = board.events.flatMap((e) => e.markets).find((m) => m.id === '124')!
  const credits = await page.locator('[aria-describedby="credits-tip"]').textContent()
  await page.getByRole('button', { name: `Set crowd alert: ${below.question}`, exact: true }).click()
  const form = page.getByRole('region', { name: `Crowd odds alert: ${below.question}`, exact: true })
  await expect(form).toContainText('own Yes 20%')
  await form.getByLabel('Yes probability (%)').fill('55.5')
  await form.getByRole('button', { name: 'Set alert', exact: true }).click()
  await expect(form.getByRole('status')).toContainText('Alert set')
  const { alerts } = await (await page.request.get(`${API_URL}/api/alerts?status=all`)).json() as { alerts: Alert[] }
  const a = alerts[0]
  expect(a).toMatchObject({ forecastId: null, marketSlug: below.marketSlug, marketId: '124', price: 0.555, action: 'notify' })
  await form.getByRole('link', { name: 'Manage in Automations' }).click()
  await expect(page.getByRole('region', { name: 'Your automations' })).toContainText(below.question)
  await expect(page.getByRole('link', { name: 'View forecast', exact: true })).toHaveCount(0)
  await expect(page.locator('[aria-describedby="credits-tip"]')).toHaveText(credits!)
  expect(paid).toEqual([])
  // This is a retained/fired transport fixture, not an assertion that the browser mock runs the real API checker.
  const fired = { ...a, status: 'fired', firedAt: new Date().toISOString(), firedPrice: 0.6, market: null }
  await page.route('**/api/alerts?*', (r) => r.fulfill({ json: { alerts: [fired] } }))
  await page.route('**/api/notifications?*', (r) => r.fulfill({ json: { items: [{ id: `odds:${a.id}`, kind: 'odds_alert', at: fired.firedAt, title: 'Crowd Yes rose above 55.5%', body: 'Now 60%', symbol: '', link: `/automations?tab=history&alert=${a.id}` }], unread: 1 } }))
  await page.reload()
  await page.getByRole('button', { name: /^Notifications/ }).click()
  await page.locator('#notifications').getByRole('link', { name: 'Crowd Yes rose above 55.5%' }).click()
  await expect(page).toHaveURL(new RegExp(`tab=history&alert=${a.id}`))
  const selected = page.getByRole('region', { name: 'Selected automation' })
  await expect(selected).toBeFocused()
  await expect(selected).toContainText(below.question)
  await expect(selected.getByRole('link', { name: /View market source/ })).toHaveAttribute('href', `https://polymarket.com/event/${a.linkedMarket!.eventSlug}`)
  await expectNoHorizontalScroll(page)
})

test('market handoff sends one bounded leaf prompt and reload does not resend it', async ({ page }) => {
  const messages: { message: string; context?: { symbol?: string; forecastId?: string } }[] = []
  page.on('request', (r) => { if (r.method() === 'POST' && new URL(r.url()).pathname === '/api/chat/stream') messages.push(r.postDataJSON()) })
  await page.goto('/forecasts?tab=markets')
  await page.getByRole('button', { name: /Ask the agent: .*above \$100,000/ }).click()
  await expect(page).toHaveURL(/\/chat\/[\w-]+$/)
  await expect(page.getByRole('list', { name: 'Suggested follow-ups' })).toBeVisible()
  expect(messages).toHaveLength(1)
  expect(messages[0].message.length).toBeLessThanOrEqual(4000)
  expect(messages[0].message).toContain('bitcoin-above-100000')
  expect(messages[0].message).toContain('Volatility proxy (not the desk)')
  expect(messages[0].context).toEqual({ symbol: 'BTC' })
  await page.reload()
  await expect(page.getByRole('list', { name: 'Suggested follow-ups' })).toBeVisible()
  expect(messages).toHaveLength(1)
})

import { expect, expectAccessible, expectNoHorizontalScroll, test } from './fixtures'
import { API_URL } from './mock-api'
import * as fx from '../src/test/fixtures'

const partial = fx.review({
  coverage: { modeledGrossExposure: 0, missingSymbols: ['APT'], commonDays: 12, reason: 'Missing marks and daily history for APT; weights are entry-price estimates.' },
  betaBtc: null, volAnnPct: null, var: null, worstDay: null, stress: [], drawdown30dPct: null,
  holdings: [{ ...fx.review().holdings[0], symbol: 'APT', side: 'short', markAvailable: false, stopStatus: 'unknown' }],
  predictionStakes: 250, predictionStakesPct: .25, notes: [],
  correlations: [{ a: 'A', b: 'B', rho: .9, days: 40 }, { a: 'B', b: 'C', rho: .9, days: 25 }, { a: 'A', b: 'C', rho: null, days: 8 }],
  clusters: [{ symbols: ['A', 'B'], rho: .9, pct: 60, days: 40 }],
  stops: { covered: 0, uncovered: ['APT'], uncoveredPct: 100, atRiskUsd: 0 },
})

for (const theme of ['light', 'dark'] as const) {
  test(`portfolio coverage and stop facts remain readable in ${theme}`, { tag: '@mobile' }, async ({ page }, info) => {
    await page.addInitScript((t) => localStorage.setItem('theme', t), theme)
    await page.route('**/api/portfolio/review', (route) => route.fulfill({ json: partial }))
    await page.goto('/account')
    const health = page.locator('.card').filter({ has: page.getByText('Portfolio health', { exact: true }) })
    await expect(health).toContainText('Entry-price estimates')
    await expect(health).toContainText('Stop: unknown')
    await expect(health.getByText('Stop: unknown', { exact: true })).toHaveClass(/text-muted/)
    await expect(health).toContainText('Prediction stakes$250')
    await expect(health).toContainText('0.25%')
    await expect(health.getByRole('list', { name: 'Clusters' })).toContainText('40 minimum shared days')
    await expect(health.getByRole('list', { name: 'Asset correlations' })).toContainText('Unknown · 8 shared days')
    await expect(health).not.toContainText('A · B · C')
    await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
    await expectAccessible(page); await expectNoHorizontalScroll(page)
    await health.evaluate((el) => el.scrollIntoView({ block: 'start' }))
    const overview = info.outputPath(`health-${theme}.png`)
    await page.screenshot({ path: overview })
    await info.attach('Portfolio health', { path: overview, contentType: 'image/png' })
    await health.getByRole('heading', { name: 'Exposure by holding' }).evaluate((el) => el.scrollIntoView({ block: 'start' }))
    const statuses = info.outputPath(`health-statuses-${theme}.png`)
    await page.screenshot({ path: statuses })
    await info.attach('Portfolio health statuses', { path: statuses, contentType: 'image/png' })
  })
  test(`caller overlap refreshes after reset and stays private in ${theme}`, { tag: '@mobile' }, async ({ page }, info) => {
    await page.addInitScript((t) => localStorage.setItem('theme', t), theme)
    await page.request.post(`${API_URL}/api/orders`, { data: { symbol: 'BTC', side: 'sell', notional: 2000 } })
    await page.goto('/portfolios/p-seed')
    const section = page.getByRole('region', { name: 'Overlap with what you hold' })
    await expect(section).toContainText('BTC · short'); await expect(section).toContainText('50% of basket'); await expect(section).toContainText('may offset basket longs'); await expect(section).toContainText('Unknown · 0 shared days')
    await expectNoHorizontalScroll(page); await expectAccessible(page)
    await section.evaluate((el) => el.scrollIntoView({ block: 'start' }))
    const screenshot = info.outputPath(`overlap-${theme}.png`)
    await page.screenshot({ path: screenshot })
    await info.attach('Caller overlap', { path: screenshot, contentType: 'image/png' })
    const storage = await page.evaluate(() => Object.values(localStorage).join(' ')); expect(storage).not.toContain('bookPct'); expect(storage).not.toContain('overlap')
    await page.request.post(`${API_URL}/api/portfolio/reset`, { data: {} })
    await page.reload(); await expect(section).toContainText('No current cash-asset holdings to compare.')
  })
}

test('same-revision visible polling renders fresh marks, retained stale facts and Retry', { tag: '@mobile' }, async ({ page }) => {
  let read = 0
  await page.route('**/api/portfolio/review', (route) => {
    read++
    if (read >= 3) return route.fulfill({ json: fx.review({ asOf: read === 3 ? '2026-10-05T12:00:00Z' : '2026-10-05T12:02:00Z', grossExposure: read === 3 ? 123 : 9000 }) })
    return route.fulfill({ json: fx.review({ asOf: read === 1 ? '2026-10-05T12:00:00Z' : '2026-10-05T12:01:00Z', grossExposure: read === 1 ? 6000 : 8000 }) })
  })
  await page.clock.install(); await page.goto('/account')
  const health = page.locator('.card').filter({ has: page.getByText('Portfolio health', { exact: true }) })
  await expect(health).toContainText('Gross exposure$6,000'); await page.clock.fastForward(60_000); await expect(health).toContainText('Gross exposure$8,000'); await page.clock.fastForward(60_000); await expect(health.getByRole('alert')).toContainText('older snapshot'); await expect(health).toContainText('Stale'); await expect(health).toContainText('Gross exposure$8,000'); await health.getByRole('button', { name: 'Retry', exact: true }).click(); await expect(health).toContainText('Gross exposure$9,000'); await expectNoHorizontalScroll(page)
})

test('review tool offers one existing optional card and executes only after confirmation', { tag: '@mobile' }, async ({ page }) => {
  const before = (await (await page.request.get(`${API_URL}/api/portfolio/trades`)).json()).trades.length
  await page.goto('/'); await page.getByLabel('Message the agent').fill('Review my paper portfolio: warranted trim fixture'); await page.getByLabel('Message the agent').press('Enter')
  await expect(page.getByText('Reviewing your portfolio', { exact: true })).toBeVisible()
  const confirm = page.getByRole('button', { name: 'Confirm sell (paper)' }); await expect(confirm).toHaveCount(1); await expect(confirm).toBeVisible()
  expect((await (await page.request.get(`${API_URL}/api/portfolio/trades`)).json()).trades).toHaveLength(before)
  await confirm.click(); await expect(page.getByText(/^Filled .* ETH @/).first()).toBeVisible(); expect((await (await page.request.get(`${API_URL}/api/portfolio/trades`)).json()).trades).toHaveLength(before + 1)
})

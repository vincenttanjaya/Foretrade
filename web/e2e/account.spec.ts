import { expect, expectNoHorizontalScroll, test } from './fixtures'
import { API_URL, MARKET_FORECAST, MARKET_QUESTION, SEED } from './mock-api'

test('the account shows an equity curve with ranges and P&L by source that sums to the total', { tag: '@mobile' }, async ({ page }) => {
  // The seeded session holds a manual ETH buy; add an agent fill.
  const order = await page.request.post(`${API_URL}/api/orders`, { data: { symbol: 'BTC', side: 'buy', notional: 1000, source: 'agent' } })
  expect(order.ok()).toBe(true)

  await page.goto('/account')
  const equity = page.locator('.card').filter({ has: page.getByRole('heading', { name: 'Equity', exact: true }) })
  await expect(equity.locator('canvas').first()).toBeVisible()
  await expect(equity).toContainText('Rebuilt from your fills and venue candle closes (Binance crypto / Hyperliquid perps) · started at $100,000')
  const thirty = page.waitForRequest((r) => r.url().includes('/api/portfolio/equity?range=30d'))
  await equity.getByRole('button', { name: '30D' }).click()
  await thirty
  await expect(equity.getByRole('button', { name: '30D' })).toHaveAttribute('aria-pressed', 'true')
  await expect(equity.locator('canvas').first()).toBeVisible()

  const pnl = page.locator('.card').filter({ has: page.getByRole('heading', { name: 'P&L by source' }) })
  const rows = pnl.getByRole('row')
  await expect(rows).toHaveCount(4) // header, Manual, Agent, All sources
  await expect(pnl.getByRole('rowheader').or(pnl.getByRole('cell')).filter({ hasText: /^Agent$/ })).toBeVisible()
  const total = (await rows.last().getByRole('cell').last().textContent())!.replace(/[^\d.]/g, '')
  await expect(page.getByRole('region', { name: 'Account summary' })).toContainText(`$${total}`) // the hero's total P&L
  await page.waitForLoadState('networkidle')
  await expectNoHorizontalScroll(page)
})

test('history links an AI portfolio fill back to its portfolio', async ({ page }) => {
  expect((await page.request.post(`${API_URL}/api/ai-portfolios/${SEED.portfolio}/invest`, { data: { amount: 5000 } })).ok()).toBe(true)
  await page.goto('/account')
  const history = page.getByRole('table').filter({ has: page.getByRole('columnheader', { name: 'Notional / Fee' }) })
  const link = history.getByRole('link', { name: 'AI portfolio' }).first()
  await expect(link).toHaveAttribute('href', `/portfolios/${SEED.portfolio}`)
  await expect(link).toHaveAttribute('title', 'Layer-1 core')
  await expect(history.getByRole('link', { name: 'AI portfolio' })).toHaveCount(3)
  await expect(history.getByRole('link', { name: 'Manual' })).toHaveCount(0) // the seeded manual buy has no origin
  await link.click()
  await expect(page).toHaveURL(new RegExp(`/portfolios/${SEED.portfolio}$`))
  await expect(page.getByRole('region', { name: 'Your paper position' })).toContainText('$5,000')
})

test('the account lists paper prediction-market positions and the stake in the summary', async ({ page }) => {
  expect((await page.request.get(`${API_URL}/api/forecasts/${MARKET_FORECAST}`)).ok()).toBe(true) // the matched report joins this session
  expect((await page.request.post(`${API_URL}/api/forecasts/${MARKET_FORECAST}/market-position`, { data: { side: 'no', stake: 50 } })).ok()).toBe(true)
  await page.goto('/account')
  await expect(page.getByRole('region', { name: 'Account summary' })).toContainText('In prediction markets$50.00')
  const card = page.locator('.card').filter({ has: page.getByRole('heading', { name: '1 paper position' }) })
  await expect(card.getByRole('link', { name: MARKET_QUESTION })).toHaveAttribute('href', `/forecasts/${MARKET_FORECAST}`)
  await expect(card.getByRole('row').nth(1)).toContainText('No')
  await expect(card.getByRole('row').nth(1)).toContainText('$50.00')
  await expect(card.getByRole('row').nth(1)).toContainText('Open')
  const pnl = page.locator('.card').filter({ has: page.getByRole('heading', { name: 'P&L by source' }) })
  await expect(pnl.getByText('Prediction', { exact: true })).toBeVisible()
})

test('portfolio health reads the book and hands it to the agent', { tag: '@mobile' }, async ({ page }) => {
  // The mock has current book facts but no measured daily history; no risk or independence claim.
  expect((await page.request.post(`${API_URL}/api/orders`, { data: { symbol: 'BTC', side: 'buy', notional: 2000 } })).ok()).toBe(true)
  await page.goto('/account')
  const health = page.locator('.card').filter({ has: page.getByText('Portfolio health', { exact: true }) })
  await expect(health.getByRole('heading', { name: /concentrated/i })).toBeVisible()
  await expect(health.getByText('Top holding').locator('..')).toContainText('ETH 75%')
  await expect(health.getByRole('meter')).toHaveCount(2)
  await expect(health).toContainText('Mock daily history unavailable')
  await expect(health).toContainText('2 of 2 uncovered · 100% of exposure uncovered')
  await expect(health).toContainText('does not establish independence')
  await expect(health).toContainText('Simulated · not financial advice')
  await page.waitForLoadState('networkidle')
  await expectNoHorizontalScroll(page)

  await health.getByRole('button', { name: 'Review with the agent' }).click()
  await expect(page).toHaveURL(/:\d+\/(chat\/[^/]+)?$/)
  await expect(page.getByRole('list', { name: 'Conversation' }).getByText('Review my paper portfolio: concentration', { exact: false }).first()).toBeVisible()
  await expect(page.getByText('Reviewing your portfolio', { exact: true })).toBeVisible()
  await expect(page.getByText(/Measured facts: Revision/)).toBeVisible()
  await expect(page.getByRole('button', { name: /Confirm .*paper/ })).toHaveCount(0)
})

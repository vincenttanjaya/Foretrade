import { expect, expectNoHorizontalScroll, httpError, test } from './fixtures'

test('the chart loads candles, then goes live on the kline socket', async ({ page, binance }) => {
  await page.goto('/trade/BTC')
  await expect(page.getByRole('heading', { level: 1, name: 'Bitcoin' })).toBeVisible()
  await expect(page.locator('canvas').first()).toBeVisible() // lightweight-charts drew the candles
  await expect(page.getByRole('alert')).toHaveCount(0)

  await expect.poll(() => binance.length).toBe(1)
  expect(binance[0].url()).toBe('wss://data-stream.binance.vision/ws/btcusdt@kline_1h')
  binance[0].send(JSON.stringify({ k: { t: Date.now(), o: '84213.2', h: '90100', l: '84100', c: '90000', v: '12.5' } }))
  await expect(page.getByText('Live', { exact: true })).toBeVisible()
  await expect(page.getByText('$90,000.00', { exact: true })).toBeVisible() // the header price follows the socket
})

test('a copilot plan pre-fills the ticket, and the fill lands in positions and history', { tag: '@mobile' }, async ({ page }) => {
  await page.goto('/trade/BTC')
  await page.getByRole('button', { name: 'Let AI decide' }).click()

  const plan = page.getByRole('article')
  await expect(plan.getByRole('meter', { name: 'Confidence' })).toHaveAttribute('aria-valuenow', '64')
  await expect(plan).toContainText('Higher lows on the 1h')
  await plan.getByRole('button', { name: 'Use this plan' }).click()

  await expect(page.getByText('Pre-filled from the Copilot plan')).toBeVisible()
  await expect(page.getByLabel('Amount (USD)')).toHaveValue('2500')
  await expect(page.getByLabel('Stop loss')).toHaveValue('82865.79') // 84,213.20 × 0.984
  await expect(page.getByLabel('Take profit')).toHaveValue('86908.02') // 84,213.20 × 1.032
  await page.getByRole('button', { name: 'Confirm buy (paper)' }).click()
  await expect(page.getByRole('status').filter({ hasText: /^Bought [\d.]+ BTC at \$84,2\d\d\.\d\d \(paper\)\.$/ })).toBeVisible()

  await expect(page.getByRole('button', { name: 'Positions (2)' })).toBeVisible() // the seeded ETH long + this BTC one
  await expect(page.getByRole('button', { name: 'Close BTC position at market' })).toBeVisible()
  await page.getByRole('button', { name: 'History' }).click()
  const fill = page.getByRole('row').filter({ hasText: 'Copilot day plan' })
  await expect(fill).toContainText('Copilot')
  await expect(fill).toContainText('Buy')
})

test('the News tab shows the symbol headlines and the brief sentiment', async ({ page }) => {
  await page.goto('/trade/BTC')
  await page.getByRole('button', { name: 'News' }).click()
  const list = page.getByRole('list', { name: 'BTC news' })
  await expect(list.getByRole('link')).toHaveCount(1)
  await expect(list.getByRole('link')).toContainText('Bitcoin ETFs log a third day of inflows')
  await expect(page.getByText('BTC sentiment')).toBeVisible()
  await expect(page.getByText('ETF inflows', { exact: true })).toBeVisible() // the brief's reason for BTC
})

test('resetting the account clears positions and history', async ({ page }) => {
  await page.goto('/account')
  await expect(page.getByRole('button', { name: 'Close ETH position at market' })).toBeVisible()
  await page.getByRole('button', { name: 'Reset portfolio' }).click()
  const dialog = page.getByRole('dialog', { name: 'Reset to a fresh $100,000?' })
  await dialog.getByRole('button', { name: 'Reset portfolio' }).click()

  await expect(page.getByRole('status').filter({ hasText: 'Paper account reset. Cash is back to $100,000.00.' })).toBeVisible()
  await expect(dialog).toBeHidden()
  await expect(page.getByText('No open positions')).toBeVisible()
  await expect(page.getByText('No trades yet')).toBeVisible()
  await expect(page.getByRole('link', { name: /Paper equity/ })).toContainText('$100,000.00')
})

test.describe('protecting a position', () => {
  test.use({ allowedErrors: [httpError(400)] }) // Chrome logs the refused target

  test('shows perps context, edits a stop inline, and a wrong-side target shows the API message', { tag: '@mobile' }, async ({ page }) => {
    await page.goto('/trade/BTC')
    const perps = page.getByRole('region', { name: 'Perpetual futures' })
    // the perps row loads on its own after the header; with the full universe the first page load shares the mock
    // with the other worker, so give its first read more than the 5 s default
    await expect(perps).toContainText('Perps · Hyperliquid', { timeout: 15_000 })
    await expect(perps).toContainText('Funding +0.0013%/h (11.4% ann.)')
    await expect(perps).toContainText('Open interest $1.24B')

    // The seeded ETH long (mark $3,000) has no stop yet.
    await page.getByRole('button', { name: 'Edit stop for ETH' }).click()
    await expect(page).toHaveURL(/\/trade\/BTC$/) // editing does not open the row's market
    await page.getByRole('textbox', { name: 'Stop for ETH' }).fill('2850')
    await page.keyboard.press('Enter')
    await expect(page.getByRole('button', { name: 'Edit stop for ETH' })).toHaveText('$2,850.00')

    await page.getByRole('button', { name: 'Edit target for ETH' }).click()
    await page.getByRole('textbox', { name: 'Target for ETH' }).fill('2900')
    await page.getByRole('button', { name: 'Save target for ETH' }).click()
    await expect(page.getByRole('alert').filter({ hasText: 'For a long, the target must be above the price ($3,000).' })).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(page.getByRole('button', { name: 'Edit target for ETH' })).toHaveText('Add')

    await page.goto('/account') // the level lives on the server
    await expect(page.getByRole('button', { name: 'Edit stop for ETH' })).toHaveText('$2,850.00')
  })
})

test('a limit buy from the ticket is listed under it as an open order, and cancels', async ({ page }) => {
  await page.goto('/trade/SOL')
  await page.getByRole('button', { name: 'Open the manual ticket' }).click()
  await page.getByRole('button', { name: 'Limit' }).click()
  await expect(page.getByLabel('Trigger price')).toHaveValue('148.5') // SOL $150 − 1%
  await page.getByLabel('Amount (USD)').fill('1000')
  await page.getByLabel('Stop loss').fill('140')
  await expect(page.getByText('Fills at market (± 2 bps) once SOL trades at or below $148.50, checked every 15 s.', { exact: false })).toBeVisible()
  await page.getByRole('button', { name: 'Place limit buy · $1,000 at $148.50' }).click()
  await expect(page.getByRole('status').filter({ hasText: 'Limit buy placed' })).toBeVisible()

  const orders = page.getByRole('region', { name: 'Open orders' })
  await expect(orders).toContainText('Open orders · 1')
  await expect(orders.getByRole('listitem')).toContainText('Buy $1,000 at ≤ $148.50 · SL $140.00')
  await orders.getByRole('button', { name: 'Cancel limit buy at $148.50' }).click()
  await expect(page.getByRole('status').filter({ hasText: 'Limit buy cancelled.' })).toBeVisible()
  await expect(orders).toBeHidden()
})

test('a copilot plan whose entry is 1.5% under the price pre-fills a limit order at the entry', async ({ page }) => {
  await page.goto('/trade/BTC')
  await page.getByRole('button', { name: 'Swing', exact: true }).click()
  await page.getByRole('button', { name: 'Ask Long' }).click()

  const plan = page.getByRole('article')
  await expect(plan).toContainText('limit · -1.50% from price') // the mock's swing long: entry $84,213.20 × 0.985
  await plan.getByRole('button', { name: 'Place limit at $82,950.00' }).click()

  await expect(page.getByText('Pre-filled from the Copilot plan')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Limit', exact: true })).toHaveAttribute('aria-pressed', 'true')
  await expect(page.getByLabel('Trigger price')).toHaveValue('82950')
  await expect(page.getByLabel('Stop loss')).toHaveValue('81622.8')
  await expect(page.getByLabel('Take profit')).toHaveValue('85604.4')
  await page.getByRole('button', { name: 'Place limit buy · $2,500 at $82,950.00' }).click()
  await expect(page.getByRole('status').filter({ hasText: 'Limit buy placed' })).toBeVisible()
})

test('a 5× buy sends margin × 5 as the notional with its stop at the est. liquidation', async ({ page }) => {
  await page.goto('/trade/SOL')
  await page.getByRole('button', { name: 'Open the manual ticket' }).click()
  await page.getByRole('group', { name: 'Leverage' }).getByRole('button', { name: '5×' }).click()
  await page.getByLabel('Margin (USD)').fill('1000')
  await expect(page.getByText('$5,000 (margin $1,000 × 5×)')).toBeVisible()
  await expect(page.getByText('$120.75 (-19.5%)')).toBeVisible() // SOL $150 × (1 − 1/5 + 0.5%)
  const order = page.waitForRequest((r) => r.method() === 'POST' && r.url().endsWith('/api/orders'))
  await page.getByRole('button', { name: 'Confirm buy (paper)' }).click()
  expect((await order).postDataJSON()).toMatchObject({ symbol: 'SOL', side: 'buy', notional: 5000, stopLoss: 120.75, note: '5× leverage (paper)' })
  await expect(page.getByRole('status').filter({ hasText: /^Bought [\d.]+ SOL at \$150\.\d\d · 5× · stop at liquidation \$120\.75 \(paper\)\.$/ })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Edit stop for SOL' })).toHaveText('$120.75')
})

test('on a phone, the Copilot / Trade bar reaches both panels', { tag: '@mobile' }, async ({ page }, info) => {
  await page.goto('/trade/SOL')
  const bar = page.getByRole('navigation', { name: 'Trade actions' })
  if (info.project.name !== 'mobile') return void (await expect(bar).toBeHidden()) // md and up: the panel is in view
  await bar.getByRole('button', { name: 'Trade' }).click()
  await expect(page.getByLabel('Amount (USD)')).toBeInViewport()
  await bar.getByRole('button', { name: 'Copilot' }).click()
  await expect(page.getByRole('button', { name: 'Let AI decide' })).toBeInViewport()
  await expectNoHorizontalScroll(page)
})

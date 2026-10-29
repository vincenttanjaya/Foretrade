import { expect, test } from './fixtures'

test('a one-sentence portfolio is built, then invested in on paper', { tag: '@mobile' }, async ({ page }) => {
  await page.goto('/portfolios')
  await page.getByLabel('Describe your portfolio').fill('Layer-1s with less drawdown than BTC')
  await page.getByRole('button', { name: /Build portfolio/ }).click()

  await expect(page).toHaveURL(/\/portfolios\/[\w-]+$/)
  await expect(page.getByRole('heading', { level: 1, name: 'Layer-1 core' })).toBeVisible()
  const preview = page.getByRole('list', { name: 'Order preview' })
  await expect(preview).toContainText('BTC50%$5,000.00')
  await expect(preview).toContainText('ETH30%$3,000.00')
  await expect(preview).toContainText('PAXG20%$2,000.00')

  await page.getByRole('button', { name: 'Invest $10,000 (paper)' }).click()
  await expect(page.getByRole('status').filter({ hasText: 'Invested $10,000.00 in 3 holdings (paper).' })).toBeVisible()
  // The receipt stays and the form does not re-arm; the portfolio now reports the paper position.
  await expect(page.getByText('3 paper orders filled')).toBeVisible()
  await expect(page.getByRole('button', { name: /Invest \$/ })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Invest more' })).toBeVisible()
  const position = page.getByRole('region', { name: 'Your paper position' })
  await expect(position).toContainText('Invested$10,000')
  await expect(position).toContainText('BTC since then')
  await page.getByRole('button', { name: 'Invest more' }).click()
  await expect(page.getByRole('button', { name: 'Invest $10,000 (paper)' })).toBeEnabled()

  await page.goto('/portfolios')
  await expect(page.getByText(/^Invested \$10,000 · [−-]0\.\d%$/)).toBeVisible() // down its fees and slippage on flat prices

  await page.goto('/account')
  for (const symbol of ['BTC', 'ETH', 'PAXG']) await expect(page.getByRole('button', { name: `Close ${symbol} position at market` })).toBeVisible()
  const history = page.getByRole('table').filter({ has: page.getByRole('columnheader', { name: 'Notional / Fee' }) })
  await expect(history.getByRole('row').filter({ hasText: 'AI portfolio' })).toHaveCount(3)
})

test('a basket built from the forecast desk says what drives each holding', async ({ page }) => {
  await page.goto('/portfolios')
  await page.getByRole('button', { name: 'From my forecast desk' }).click()
  await expect(page.getByLabel('Describe your portfolio')).toHaveValue(/^Build a basket from what my forecast desk is most bullish on/)
  await page.getByRole('button', { name: /Build portfolio/ }).click()

  await expect(page.getByRole('heading', { level: 1, name: 'Layer-1 core' })).toBeVisible()
  const chip = page.getByRole('link', { name: 'Driven by Forecast · Yes 62%' })
  await expect(chip).toHaveAttribute('href', '/forecasts/f-seed')
  await expect(page.getByText('Driven by News sentiment · bullish')).toBeVisible()
  await expect(page.getByText('Driven by Daily history stats')).toBeVisible()

  await page.goto('/portfolios')
  await expect(page.getByRole('link', { name: /Layer-1 core/ }).filter({ hasText: 'Desk-driven' })).toHaveCount(1) // the seeded basket predates drivers
})

test('an immediate investment waits for measured since-buy chart points without inventing history', async ({ page }) => {
  await page.goto('/portfolios/p-seed')
  await page.getByRole('button', { name: 'Invest $10,000 (paper)' }).click()
  await expect(page.getByRole('button', { name: 'Invest more' })).toBeVisible()
  await page.reload()
  const tabs = page.getByRole('group', { name: 'Backtest window' })
  await expect(tabs.getByRole('button', { name: 'Since invest' })).toHaveCount(0)
  await expect(page.getByRole('heading', { name: 'Portfolio vs BTC' })).toBeVisible()
  await expect(page.getByRole('region', { name: 'Your paper position' })).toContainText('BTC since then')
  await expect(page.getByText(/181 daily bars/)).toBeVisible()
})

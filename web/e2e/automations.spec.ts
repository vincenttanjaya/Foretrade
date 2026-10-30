import { expect, expectAccessible, expectNoHorizontalScroll, httpError, test } from './fixtures'

test('an automation created from the form is listed, and the bell shows the feed', { tag: '@mobile' }, async ({ page }) => {
  await page.goto('/automations')
  await expect(page.getByRole('heading', { level: 1, name: 'Automations' })).toBeVisible()
  await expect(page.getByRole('heading', { name: 'No automations yet' })).toBeVisible()
  await expect(page.getByText(/from the live|is at \$/)).toBeVisible() // live prices loaded

  await page.getByRole('button', { name: 'Buy $500 of SOL if it dips 5%' }).click()
  await expect(page.getByLabel('Price (USD)')).toHaveValue('142.5') // SOL $150 − 5%
  await expect(page.getByLabel('Amount ($)')).toHaveValue('500')
  await page.getByLabel('Note').fill('Dip into support')
  await page.getByRole('button', { name: 'Create automation' }).click()
  await expect(page.getByRole('status').filter({ hasText: 'Automation created. Checked every 15 seconds.' })).toBeVisible()
  const list = page.getByRole('region', { name: 'Your automations' })
  await expect(list.getByRole('button', { name: 'Active · 1' })).toHaveAttribute('aria-pressed', 'true')
  await expect(list).toContainText('SOL falls below $142.50')
  await expect(list).toContainText('Paper buy $500 SOL')
  await expect(list).toContainText('Dip into support')
  await page.waitForLoadState('networkidle')
  await expectAccessible(page)
  await expectNoHorizontalScroll(page)

  // The bell: three unread items from the mock feed; opening it marks them seen.
  await page.getByRole('button', { name: 'Notifications, 3 unread' }).click()
  const feed = page.locator('#notifications')
  await expect(feed).toBeVisible()
  await expect(feed.getByRole('link', { name: 'SOL fell below $145.00' })).toBeVisible()
  await expect(feed.getByRole('link', { name: 'Stop loss hit · ETH' })).toBeVisible()
  await expect(feed.getByRole('link', { name: 'Forecast ready · Yes 28%' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Notifications', exact: true })).toBeVisible()
  await expectNoHorizontalScroll(page)

  await feed.getByRole('link', { name: 'All automations' }).click() // the phone's way in
  await expect(page).toHaveURL(/\/automations$/)
  await expect(feed).toBeHidden()
})

test('a fired alert in the bell opens the agent with a prompt', async ({ page }) => {
  await page.goto('/automations')
  await page.getByRole('button', { name: /^Notifications/ }).click()
  await page.locator('#notifications').getByRole('button', { name: 'Ask the agent' }).click()
  await expect(page).toHaveURL(/:\d+\/(chat\/[^/]+)?$/)
  await expect(page.getByText('SOL just fell below $145.00 (now $144.80). Should I act?', { exact: false }).first()).toBeVisible()
})

test('a daily brief scheduled from the form is listed with its next run', { tag: '@mobile' }, async ({ page }) => {
  await page.goto('/automations')
  await expect(page.getByRole('heading', { name: 'No automations yet' })).toBeVisible()
  await expect(page.getByRole('textbox', { name: 'Describe an automation' })).toBeVisible()
  await page.getByRole('button', { name: 'Schedule', exact: true }).click()
  await expect(page.getByLabel('Time (UTC)')).toHaveValue('08:00')
  await expect(page.getByLabel('Weekday')).toHaveCount(0) // daily
  await expect(page.getByRole('button', { name: 'Brief me', exact: true })).toHaveAttribute('aria-pressed', 'true')
  await page.getByRole('button', { name: 'Create automation' }).click()
  await expect(page.getByRole('status').filter({ hasText: 'Automation created. Next run' })).toBeVisible()
  const list = page.getByRole('region', { name: 'Your automations' })
  await expect(list.getByRole('button', { name: 'Active · 1' })).toHaveAttribute('aria-pressed', 'true')
  await expect(list).toContainText('BTC · Every day · 08:00 UTC')
  await expect(list).toContainText('Brief: price, 24h move, sentiment, top headline')
  await expect(list).toContainText('Bell · in-app')
  await expect(list.getByText(/^Next run /)).toBeVisible() // the label only: the time is the clock's
  await page.waitForLoadState('networkidle')
  await expectAccessible(page)
  await expectNoHorizontalScroll(page)
})

test('a price trigger the market is already past cannot be created and points to the Trade page', async ({ page }) => {
  await page.goto('/automations?symbol=SOL') // the mock's SOL mark is fixed at $150.00
  await expect(page.getByText('SOL is at $150.00')).toBeVisible()
  await page.getByRole('button', { name: 'rises above' }).click()
  await page.getByLabel('Price (USD)').fill('140')
  const hint = page.locator('#auto-price-hint')
  await expect(hint).toHaveText('SOL is already above $140.00 (now $150.00). Pick a level above the price, or trade now on the Trade page.')
  await expect(page.getByRole('button', { name: 'Create automation' })).toBeDisabled()
  await page.getByLabel('Price (USD)').fill('160')
  await expect(hint).toHaveText('+6.7% from the live $150.00')
  await expect(page.getByRole('button', { name: 'Create automation' })).toBeEnabled()
  await page.getByLabel('Price (USD)').fill('140')
  await hint.getByRole('link', { name: 'trade now on the Trade page' }).click()
  await expect(page).toHaveURL(/\/trade\/SOL$/)
})

test('an odds trigger set from the Prediction market tab is listed with the market price', { tag: '@mobile' }, async ({ page }) => {
  await page.goto('/automations')
  await expect(page.getByRole('heading', { name: 'No automations yet' })).toBeVisible()
  await page.getByRole('button', { name: 'Prediction market' }).click()
  await expect(page.getByText('Polymarket 55% · desk 62%')).toBeVisible() // the seeded report's matched market
  await expect(page.getByLabel('Forecast')).toHaveValue('f-seed')
  await expect(page.getByLabel('Asset')).toHaveCount(0)
  const hint = page.locator('#auto-pct-hint')
  await page.getByLabel('Condition').selectOption('above')
  await page.getByLabel('Level (%)').fill('50')
  await expect(hint).toHaveText('The market is already at 55%. Pick a level above it.')
  await expect(page.getByRole('button', { name: 'Create automation' })).toBeDisabled()
  await page.getByLabel('Level (%)').fill('60')
  await expect(hint).toHaveText("+5 pts from the market's 55%")
  await page.getByLabel('Note').fill('Market catching up')

  const created = page.waitForResponse((r) => r.url().endsWith('/api/alerts') && r.request().method() === 'POST')
  await page.getByRole('button', { name: 'Create automation' }).click()
  const res = await created
  expect(res.status()).toBe(201)
  expect((await res.json()).alert).toMatchObject({ trigger: 'odds', symbol: '', forecastId: 'f-seed', op: 'above', price: 0.6, action: 'notify' })
  await expect(page.getByRole('status').filter({ hasText: "Alert set. You'll get a bell when Polymarket crosses 60%." })).toBeVisible()
  const list = page.getByRole('region', { name: 'Your automations' })
  await expect(list).toContainText('Polymarket · ETH outperforms BTC in Q4 2026? · above 60%')
  await expect(list).toContainText('now 55%')
  await expect(list.getByRole('link', { name: 'View forecast' })).toHaveAttribute('href', '/forecasts/f-seed')
  await page.waitForLoadState('networkidle')
  await expectAccessible(page)
  await expectNoHorizontalScroll(page)
})

// All-time-high drawdown triggers. The mock's high is 1.6 × the fixture mark (SOL $240 on Jan 19, 2025).
for (const theme of ['light', 'dark'] as const) {
  test(`a drawdown sell from the Automations form is reviewed once, warns when already past and is listed (${theme})`, { tag: '@mobile' }, async ({ page }, info) => {
    await page.addInitScript((value) => localStorage.setItem('theme', value), theme)
    await page.goto('/automations?create=drawdown&symbol=SOL') // a sell on a drawdown is set here; the Trade page's dialog notifies
    await expect(page.getByRole('heading', { name: 'New automation' })).toBeInViewport() // below the fold on a phone otherwise
    await expect(page.getByRole('button', { name: 'Drawdown', exact: true })).toHaveAttribute('aria-pressed', 'true')
    await expect(page.getByLabel('Asset')).toHaveValue('SOL')
    const high = page.locator('#auto-ath')
    await expect(high).toContainText('All-time high $240.00 · Jan 19, 2025')
    await expect(high).toContainText('Binance SOLUSDT daily highs since 2020-08-11')
    await expect(high).toContainText('Now $150.00 · 37.5% below the high')
    await page.getByLabel('Drawdown from the all-time high (%)').fill('30')
    await expect(high).toContainText('Fires at or below $168.00: the price is already past it, so it would fire on the next check.')
    await page.getByRole('button', { name: 'Notify + sell' }).click()
    await expect(page.getByLabel('Sell (% of your paper long)')).toHaveValue('100')
    await expect(page.getByLabel('Amount ($)')).toHaveCount(0)

    await page.getByRole('button', { name: 'Review drawdown alert' }).click()
    const confirm = page.getByRole('group', { name: 'Confirm drawdown alert' })
    await expect(confirm).toContainText('When SOL falls 30% below its all-time high ($240.00 on Jan 19, 2025), at or below $168.00: notify me and sell 100% of my paper SOL long.')
    await expect(confirm).toContainText('SOL is already 37.5% below its high, past this level: once confirmed, it fires on the next check (within 15 seconds).')
    await expectNoHorizontalScroll(page)
    await page.screenshot({ path: info.outputPath(`drawdown-confirm-${theme}.png`), fullPage: true })

    const created = page.waitForResponse((r) => r.url().endsWith('/api/alerts') && r.request().method() === 'POST')
    await confirm.getByRole('button', { name: 'Confirm and set' }).click()
    const res = await created
    expect(res.status()).toBe(201)
    expect(res.request().postDataJSON()).toEqual({ symbol: 'SOL', trigger: 'drawdown', drawdownPct: 30, action: 'sell', sellPct: 100, source: 'manual' })
    expect((await res.json()).alert).toMatchObject({ trigger: 'drawdown', drawdown: { pct: 30, sellPct: 100, ath: 240, threshold: 168 } })
    await expect(page.getByRole('status').filter({ hasText: 'Drawdown alert set. The price is already past it, so it fires on the next check.' })).toBeVisible()
    const list = page.getByRole('region', { name: 'Your automations' })
    await expect(list.getByRole('button', { name: 'Active · 1' })).toHaveAttribute('aria-pressed', 'true')
    await expect(list).toContainText('SOL falls 30% below its all-time high · ≤ $168.00')
    await expect(list).toContainText('ATH $240.00 · Jan 19, 2025 · now $150.00 (37.5% below)')
    await expect(list).toContainText('Binance SOLUSDT daily highs since 2020-08-11')
    await expect(list).toContainText('Notify + paper sell 100% of the SOL long')
    await expect(list).toContainText('Bell · in-app · Account')
    await page.waitForLoadState('networkidle')
    await expectAccessible(page)
    await expectNoHorizontalScroll(page)
    await page.screenshot({ path: info.outputPath(`drawdown-list-${theme}.png`), fullPage: true })
  })
}

test.describe(() => {
  test.use({ allowedErrors: [httpError(400)] }) // Chrome logs the refused HSDT high

  test('a quote feed measures its listing price, and a reverse-split high is refused with no alert set from it', { tag: '@mobile' }, async ({ page }, info) => {
    await page.goto('/automations?create=drawdown&symbol=DBS')
    const high = page.locator('#auto-ath')
    await expect(high).toContainText('All-time high SGD 125.70 · Jan 19, 2025')
    await expect(high).toContainText('Yahoo Finance D05.SI lifetime daily highs since 1999-01-22 (split-adjusted), in SGD')
    await expect(high).toContainText("Compared on the listing's own SGD price, not the USD paper mark.")

    // The mock's HSDT high is 10,000,000 × its price, standing in for a reverse-split history: the API refuses it.
    await page.getByLabel('Asset').selectOption('HSDT')
    await expect(high).toContainText("HSDT has no usable all-time high: Yahoo's split-adjusted lifetime high for HSDT is over 100× the price")
    await page.getByLabel('Drawdown from the all-time high (%)').fill('20')
    await expect(high).not.toContainText('26,500,000')
    await expect(high).not.toContainText('Fires at or below')
    await expect(page.getByRole('button', { name: 'Review drawdown alert' })).toBeDisabled()
    await expectNoHorizontalScroll(page)
    await page.screenshot({ path: info.outputPath('drawdown-withheld.png'), fullPage: true })
  })
})

test("the agent's drawdown card sets one alert from the chat", { tag: '@mobile' }, async ({ page }) => {
  const posts: unknown[] = []
  page.on('request', (r) => {
    if (new URL(r.url()).pathname === '/api/alerts' && r.method() === 'POST') posts.push(r.postDataJSON())
  })
  await page.goto('/')
  await page.getByLabel('Message the agent').fill('Tell me if SOL falls 40% from its all-time high')
  await page.getByLabel('Message the agent').press('Enter')
  const card = page.locator('.card').filter({ hasText: 'Watch SOL for a 40% drawdown' })
  await expect(card).toContainText('Drawdown alert · paper')
  await expect(card).toContainText('SOL falls 40% below its all-time high · ≤ $144.00')
  await expect(card).toContainText('Binance SOLUSDT daily highs since 2020-08-11')
  await expect(card).not.toContainText('already past this level')
  expect(posts).toEqual([]) // the agent proposes; nothing is set until the tap
  await card.getByRole('button', { name: 'Set alert' }).click()
  await expect(card.getByRole('status')).toContainText('Alert set')
  expect(posts).toEqual([expect.objectContaining({ symbol: 'SOL', trigger: 'drawdown', drawdownPct: 40, action: 'notify', source: 'agent' })])
  await expectNoHorizontalScroll(page)
  await card.getByRole('link', { name: /Manage in Automations/ }).click()
  await expect(page.getByRole('region', { name: 'Your automations' })).toContainText('SOL falls 40% below its all-time high')
})

import { expect, expectAccessible, expectNoHorizontalScroll, test, type Page } from './fixtures'
import { API_URL } from './mock-api'

/** A metrics tile on the strategy page: its label and the value under it. */
const tile = (page: Page, label: string) => page.getByText(label, { exact: true }).locator('xpath=..')

test('studio form validates rules and turns the reviewed prompt into a backtest @mobile', async ({ page }) => {
  await page.goto('/strategies')
  await page.getByRole('button', { name: 'Build with a form', exact: true }).click()
  await page.getByLabel('Market', { exact: true }).selectOption('SOL')
  await page.getByLabel('Fast period').fill('80')
  await page.getByLabel('Slow period').fill('50')
  await expect(page.getByRole('button', { name: /Generate & backtest/ })).toBeDisabled()
  await page.getByLabel('Fast period').fill('20')
  await page.getByLabel('Stop loss (%)', { exact: true }).fill('3')
  await expect(page.getByRole('region', { name: 'Strategy prompt preview' })).toContainText('SOL')
  await expect(page.getByRole('region', { name: 'Strategy prompt preview' })).toContainText('3%')
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  await page.getByRole('button', { name: 'Edit as text', exact: true }).click()
  await expect(page.getByLabel('Describe your strategy')).toHaveValue(/SOL/)
  await expect(page.getByLabel('Describe your strategy')).toHaveValue(/3%/)
  await page.getByRole('button', { name: /Generate & backtest/ }).click()
  await expect(page.getByRole('heading', { level: 1, name: 'SOL EMA cross' })).toBeVisible()
  await expect(page.getByText('You asked')).toBeVisible()
})

test('studio: one sentence becomes a backtested strategy with its metrics', async ({ page }) => {
  await page.goto('/strategies')
  await page.getByLabel('Describe your strategy').fill('Buy SOL when EMA 20 crosses above EMA 50, exit on the reverse cross')
  await page.getByLabel('Market').selectOption('SOL')
  await page.getByRole('button', { name: /Generate & backtest/ }).click()

  await expect(page).toHaveURL(/\/strategies\/[\w-]+$/)
  await expect(page.getByRole('heading', { level: 1, name: 'SOL EMA cross' })).toBeVisible()
  await expect(page.getByText('You asked')).toBeVisible()
  await expect(tile(page, 'Total return')).toContainText('24.5%')
  await expect(tile(page, 'Sortino')).toContainText('2.05')
  await expect(tile(page, 'Calmar')).toContainText('1.70')
  await expect(page.getByRole('heading', { name: 'Saved backtest vs buy & hold' })).toBeVisible()

  // No black box: the exact spec it runs, copied as JSON.
  await page.context().grantPermissions(['clipboard-read', 'clipboard-write'])
  await page.getByText('Rule spec (JSON)').click()
  await expect(page.getByText('This JSON is exactly what the backtester and Autopilot run. No hidden code.')).toBeVisible()
  await page.getByRole('button', { name: 'Copy', exact: true }).click()
  await expect(page.getByRole('status').filter({ hasText: 'Spec copied' })).toBeVisible()
  expect(JSON.parse(await page.evaluate(() => navigator.clipboard.readText()))).toMatchObject({ symbol: 'SOL', interval: '4h', stopLossPct: 4 })

  await page.getByRole('link', { name: 'Strategies' }).first().click()
  await expect(page.getByRole('region', { name: 'My strategies' }).getByRole('link', { name: /SOL EMA cross/ })).toBeVisible()
})

test('top strategies: the leaderboard opens a legacy strategy whose missing metrics read "—"', async ({ page }) => {
  await page.goto('/strategies')
  await page.getByRole('button', { name: 'Top strategies', exact: true }).click()
  await expect(page).toHaveURL(/\?tab=top$/)
  await expect(page.getByRole('heading', { level: 1, name: 'Top strategies, measured honestly.' })).toBeVisible()
  const leaderboard = page.getByRole('table').filter({ has: page.getByRole('columnheader', { name: 'Strategy' }) })
  await expect(leaderboard.getByRole('row')).toHaveCount(4) // header + 3 built-ins
  await expect(page.getByLabel('Loading equity curves')).toHaveCount(0) // every curve came in and the chart drew
  await expect(leaderboard.getByRole('columnheader', { name: 'Window' })).toBeVisible()
  // 42 days: no annualized return, and the window says why.
  const short = leaderboard.getByRole('row').filter({ hasText: 'BTC Donchian breakout' })
  await expect(short).toContainText('42 d')
  await expect(short.getByRole('cell').nth(4)).toHaveText('—')
  // Full samples (180+ days, 10+ trades) rank first; the rest carry a muted badge.
  await expect(leaderboard.getByRole('row').nth(1)).toContainText('SOL EMA cross')
  await expect(leaderboard.getByRole('row').nth(1).getByText('Short sample')).toHaveCount(0)
  await expect(short.getByText('Short sample')).toBeVisible()
  await expect(page.getByText('Ranked by Sharpe among strategies with 180+ days and 10+ trades; short samples follow.')).toBeVisible()

  await leaderboard.getByRole('link', { name: 'ETH RSI mean reversion' }).click()
  await expect(page.getByRole('heading', { level: 1, name: 'ETH RSI mean reversion' })).toBeVisible()
  await expect(tile(page, 'Sharpe')).toContainText('0.71')
  for (const label of ['Sortino', 'Volatility', 'Calmar', 'Fees']) await expect(tile(page, label)).toContainText('—')
})

async function startRun(page: Page) {
  await page.goto('/strategies?tab=arena&arena=historical')
  await expect(page.getByRole('heading', { level: 1, name: 'Let the agents trade the past.' })).toBeVisible()
  await page.getByRole('button', { name: /Start run/ }).click()
  await expect(page).toHaveURL(/\/strategies\/arena\/[\w-]+$/)
  const progress = page.getByRole('progressbar', { name: 'Steps finished' })
  await expect(progress).toHaveAttribute('aria-valuenow', '30') // the first poll: half of the 60 daily steps
  return progress
}

test('arena: a run polls to completion with a leaderboard and a decision log', async ({ page }) => {
  const progress = await startRun(page)
  await expect(page.getByRole('button', { name: 'Cancel run' })).toBeVisible()
  await expect(page.getByText(/claude|sonnet/i)).toHaveCount(0) // the model is never named

  await expect(progress).toHaveAttribute('aria-valuenow', '60', { timeout: 10_000 })
  await expect(page.getByRole('status').filter({ hasText: 'The arena run finished.' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Delete' })).toBeVisible()
  const leaderboard = page.getByRole('table').filter({ has: page.getByRole('columnheader', { name: 'Agent' }) })
  for (const name of ['Trend Rider', 'Mean Reverter', 'News Desk', 'BTC buy & hold']) await expect(leaderboard).toContainText(name)
  await expect(page.getByText('Step 60: bought BTC on the trend.')).toBeVisible()
})

test('arena: cancelling a run stops it and refunds the unrun steps', async ({ page }) => {
  await startRun(page)
  const credits = page.locator('[aria-describedby="credits-tip"]')
  await expect(credits).toHaveText('392 cr') // 482 left − ⌈3 agents × 60 steps ÷ 2⌉
  await page.getByRole('button', { name: 'Cancel run' }).click()
  await expect(page.getByText('Cancelled after 30 steps. Unrun steps were refunded.')).toBeVisible()
  await expect(credits).toHaveText('437 cr') // 3 agents × 30 unrun steps ÷ 2 back
  await expect(page.getByRole('button', { name: 'Cancel run' })).toHaveCount(0)
})

test('autopilot: deploy a strategy, watch it run, stop it at market', async ({ page }) => {
  await page.goto('/strategies/lab-sol-ema')
  await expect(page.getByRole('heading', { level: 1, name: 'SOL EMA cross' })).toBeVisible()
  await expect(page.getByRole('link', { name: 'Trade SOL manually' })).toHaveAttribute('href', '/trade/SOL')
  await page.getByRole('button', { name: 'Deploy to autopilot' }).click()
  const dialog = page.getByRole('dialog', { name: 'Deploy “SOL EMA cross”' })
  await dialog.getByRole('button', { name: '$10K' }).click()
  await dialog.getByRole('button', { name: '20%' }).click()
  // Every entry carries a stop and a target: the spec's 4% stop, a target at 2× it, and an optional trail.
  await expect(dialog.getByLabel('Stop loss %')).toHaveValue('4')
  await expect(dialog.getByLabel('Take profit %')).toHaveValue('8')
  await dialog.getByLabel('Take profit %').fill('')
  await expect(dialog.getByRole('button', { name: 'Deploy (paper)' })).toBeDisabled()
  await dialog.getByLabel('Take profit %').fill('8')
  await dialog.getByLabel('Trailing stop %').fill('3')
  await expect(dialog).toContainText('Checks every closed 4h bar · each entry uses 100% of the allocation · stop 4% / target 8% · trailing 3%')
  const deploy = page.waitForRequest((r) => r.method() === 'POST' && r.url().endsWith('/api/autopilots'))
  await dialog.getByRole('button', { name: 'Deploy (paper)' }).click()
  expect((await deploy).postDataJSON()).toMatchObject({ strategyId: 'lab-sol-ema', stopLossPct: 4, takeProfitPct: 8, trailingStopPct: 3 })

  const card = page.getByRole('region', { name: 'Autopilot SOL EMA cross' })
  await expect(card.getByText('Running')).toBeVisible()
  await expect(card.getByText('stop 4% · target 8% · trailing 3%')).toBeVisible() // echoed by the API
  await expect(card).toContainText(/Long [\d.]+ SOL @ \$150/)
  await expect(card).toContainText(/Next check \d\d:\d\d UTC/)
  await expect(card.getByRole('region', { name: 'Recent fills' })).toContainText(/Buy[\d.]+ SOL @ \$150/)
  await expect(page.getByRole('status').filter({ hasText: 'Autopilot running · SOL EMA cross' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Autopilot running' })).toBeDisabled()

  // The strategies page shows it as a strip while it runs.
  await page.goto('/strategies')
  const strip = page.getByRole('region', { name: 'Autopilot SOL EMA cross' })
  await expect(strip).toContainText('Running')
  await strip.getByRole('button', { name: 'Stop' }).click()
  await page.getByRole('dialog', { name: 'Stop “SOL EMA cross”?' }).getByRole('button', { name: 'Close it at market' }).click()
  await expect(strip).toContainText('Stopped (user)')
  await expect(page.getByRole('status').filter({ hasText: 'Autopilot stopped' })).toBeVisible()

  // Both fills landed in the account with their source.
  await page.goto('/account')
  const history = page.getByRole('table').filter({ has: page.getByRole('columnheader', { name: 'Notional / Fee' }) })
  await expect(history.getByText('Autopilot', { exact: true })).toHaveCount(2)
})

test('optimize stops: the heatmap ranks 25 stop/target pairs and Apply re-runs the backtest', async ({ page }) => {
  await page.goto('/strategies/lab-sol-ema')
  await expect(page.getByRole('heading', { level: 1, name: 'SOL EMA cross' })).toBeVisible()
  await page.getByRole('button', { name: 'Optimize stops' }).click()
  const heatmap = page.getByRole('table', { name: /Sharpe ratio of the same rules/ })
  await expect(heatmap.getByRole('cell', { name: /^-?\d/ })).toHaveCount(25) // the Sharpe cells, not the empty corner
  const best = heatmap.getByRole('cell', { name: /^1\.64/ })
  await best.focus()
  await expect(best.getByRole('tooltip')).toBeVisible()
  await expect(best).toHaveAccessibleDescription('Return +16.4% · Max DD −13.0% · 12 trades')
  await expect(page.getByText('Best of 25 tries on the same history: in-sample, expect worse live.')).toBeVisible()

  await page.getByRole('button', { name: 'Apply stop 3%, target 10%' }).click()
  await expect(page.getByText('Modified · not saved')).toBeVisible()
  await expect(page.getByLabel('Stop loss', { exact: true })).toHaveValue('3')
  await expect(page.getByLabel('Take profit', { exact: true })).toHaveValue('10')
  await expect(tile(page, 'Sharpe')).toContainText('1.64was 1.42')
  await expect(best).toHaveAttribute('aria-current', 'true')
})

test('community: star a published strategy, open it and copy it into the studio @mobile', async ({ page }) => {
  await page.goto('/strategies')
  await page.getByRole('button', { name: 'Top strategies', exact: true }).click()
  await page.getByRole('button', { name: 'Community', exact: true }).click()
  await expect(page).toHaveURL(/\?tab=top&top=community$/)
  const row = page.getByRole('list', { name: 'Community strategies' }).getByRole('listitem').filter({ hasText: 'ETH breakout retest' })
  await expect(row).toContainText('by Ann Lee')
  await expect(row).toContainText('2 copies · 1 deploy')
  await row.getByRole('button', { name: 'Star · 4 stars' }).click()
  await expect(row.getByRole('button', { pressed: true })).toHaveText('★ 5')
  await expectNoHorizontalScroll(page)

  await row.getByRole('link', { name: 'ETH breakout retest' }).click()
  await expect(page.getByRole('heading', { level: 1, name: 'ETH breakout retest' })).toBeVisible()
  await expect(page.getByText('by Ann Lee', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Unstar · 5 stars' })).toHaveAttribute('aria-pressed', 'true')
  await expect(page.getByRole('button', { name: 'Deploy to autopilot' })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Optimize stops' })).toHaveCount(0)
  await page.getByRole('button', { name: 'Copy to my studio' }).click()

  await expect(page.getByRole('status').filter({ hasText: 'Copied to your studio. Backtest re-run on fresh data.' })).toBeVisible()
  await expect(page).not.toHaveURL(/c-eth-breakout/)
  await expect(page.getByText(/^Copied from/)).toHaveText('Copied from ETH breakout retest by Ann Lee')
  await expect(page.getByRole('button', { name: 'Deploy to autopilot' })).toBeVisible()
  await page.getByRole('link', { name: 'Strategies' }).first().click()
  await expect(page.getByRole('region', { name: 'My strategies' }).getByRole('link', { name: /ETH breakout retest/ })).toBeVisible()
})

test('community: publish an own strategy, then find it on the board as yours', async ({ page }) => {
  await page.goto('/strategies')
  await page.getByLabel('Describe your strategy').fill('Buy SOL when EMA 20 crosses above EMA 50, exit on the reverse cross')
  await page.getByLabel('Market').selectOption('SOL')
  await page.getByRole('button', { name: /Generate & backtest/ }).click()
  await page.getByRole('button', { name: 'Publish to community' }).click()
  const dialog = page.getByRole('dialog', { name: 'Publish “SOL EMA cross”?' })
  await expect(dialog).toContainText('Autopilot results stay private.')
  await dialog.getByRole('button', { name: 'Publish', exact: true }).click()
  await expect(page.getByText('Published · ★ 0 · 0 copies')).toBeVisible()

  await page.goto('/strategies?tab=top&top=community')
  const mine = page.getByRole('list', { name: 'Community strategies' }).getByRole('listitem').filter({ hasText: 'SOL EMA cross' })
  await expect(mine).toContainText('by You')
  await page.goto('/strategies')
  await expect(page.getByRole('region', { name: 'My strategies' }).getByRole('link', { name: /SOL EMA cross/ })).toContainText('Published')
})

async function toggleTheme(page: Page, current: 'light' | 'dark') {
  const next = current === 'light' ? 'dark' : 'light'
  if (page.viewportSize()!.width < 640) {
    await page.getByRole('button', { name: 'Account menu', exact: true }).click()
    await page.getByRole('button', { name: next === 'light' ? 'Light theme' : 'Dark theme', exact: true }).click()
    await page.keyboard.press('Escape')
  } else {
    await page.getByRole('button', { name: `Switch to ${next} theme`, exact: true }).click()
  }
  await expect(page.locator('html')).toHaveAttribute('data-theme', next)
}

for (const theme of ['light', 'dark'] as const) {
  test(`forward detail: exact published boundary and independent metrics survive a ${theme} theme rebuild @mobile`, async ({ page }, testInfo) => {
    await page.addInitScript((value) => localStorage.setItem('theme', value), theme)
    await page.goto('/strategies/c-eth-breakout')
    await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
    const forward = page.getByRole('region', { name: 'Live forward', exact: true })
    await expect(forward).toBeVisible()
    await expect(forward).toContainText('Since publish')
    await expect(forward).toContainText('2026-09-01 02:30:00.123 UTC')
    await expect(forward).toContainText('Source as of 2026-10-05 00:00:00 UTC')
    // The intrabar publication instant is separate from the first eligible plotted point and the flat anchor.
    await expect(forward).toContainText('First plotted bar: 2026-09-01 03:00:00 UTC')
    await expect(forward).toContainText('does not show a price at the publication instant')
    await expect(forward).toContainText('starts flat at 100')
    await expect(forward.getByText('Live forward (saved rules)', { exact: true })).toBeVisible()
    await expect(forward.getByRole('listitem').filter({ hasText: 'Buy & hold ETH' })).toBeVisible()
    const metrics = forward.getByRole('table', { name: 'Saved backtest and live forward metrics' })
    await expect(metrics.getByRole('rowheader', { name: 'Buy & hold ETH', exact: true })).toBeVisible()
    const returns = metrics.getByRole('row', { name: /^Return/ }).getByRole('cell')
    await expect(returns.nth(0)).toHaveText(/24\.5%/)
    await expect(returns.nth(1)).toHaveText(/4\.2%/)
    await expect(metrics.getByRole('row', { name: /^Buy & hold/ }).getByRole('cell').nth(1)).toHaveText('+1.3%')
    await expect(metrics.getByRole('row', { name: /^Window/ }).getByRole('cell').nth(1)).toHaveText(/34 (?:d|days)/)
    await expectAccessible(page)
    await expectNoHorizontalScroll(page)

    // A narrow metrics table scrolls inside its own card, without widening main or the document.
    const scroll = await metrics.locator('xpath=..').evaluate((element) => ({
      overflowX: getComputedStyle(element).overflowX,
      extra: element.scrollWidth - element.clientWidth,
      right: element.getBoundingClientRect().right,
    }))
    expect(['auto', 'scroll']).toContain(scroll.overflowX)
    expect(scroll.right).toBeLessThanOrEqual(page.viewportSize()!.width)
    if (page.viewportSize()!.width === 375) expect(scroll.extra).toBeGreaterThan(0)

    await forward.evaluate((element) => element.scrollIntoView({ block: 'start' }))
    await page.screenshot({ path: testInfo.outputPath(`forward-${theme}-overview.png`) })
    if (page.viewportSize()!.width === 375) {
      await metrics.locator('xpath=..').evaluate((element) => element.scrollLeft = element.scrollWidth)
      await expect(returns.nth(1)).toBeInViewport({ ratio: 1 })
      await page.screenshot({ path: testInfo.outputPath(`forward-${theme}-metrics.png`) })
    }
    await forward.getByRole('list').evaluate((element) => element.scrollIntoView({ block: 'start' }))
    await page.screenshot({ path: testInfo.outputPath(`forward-${theme}-chart.png`) })
    await toggleTheme(page, theme)
    await expect(forward.getByText('Live forward (saved rules)', { exact: true })).toBeVisible()
    await expect(forward).toContainText('First plotted bar: 2026-09-01 03:00:00 UTC')
    await expect(returns.nth(1)).toHaveText(/4\.2%/)
    await expectNoHorizontalScroll(page)
  })
}

test('forward: retuning and resetting keep the saved-rule comparison immutable @mobile', async ({ page }) => {
  await page.goto('/strategies/lab-sol-ema')
  const forward = page.getByRole('region', { name: 'Live forward', exact: true })
  const metrics = forward.getByRole('table', { name: 'Saved backtest and live forward metrics' })
  await expect(metrics).toBeVisible()
  await expect(forward).toContainText('Since launch')
  await expect(forward).toContainText('2026-10-03 00:00:00 UTC')
  const originalMetrics = await metrics.innerText()
  const originalSource = await forward.getByText(/Source as of/).innerText()
  await page.getByLabel('Stop loss', { exact: true }).fill('3')
  await page.getByLabel('Take profit', { exact: true }).fill('10')
  const rerun = page.getByRole('button', { name: /Re-run backtest/ })
  await rerun.focus()
  await page.keyboard.press('Enter')
  await expect(page.getByText('Modified · not saved', { exact: true })).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Modified backtest vs buy & hold' })).toBeVisible()
  await expect(tile(page, 'Total return')).toContainText('16.4%')
  await expect(metrics).toHaveText(originalMetrics, { useInnerText: true })
  await expect(forward.getByText(/Source as of/)).toHaveText(originalSource)
  await expect(forward).toContainText('saved rules')
  await expect(forward).toContainText('modified rules on screen')
  await expect(forward).not.toContainText('16.4%')
  await expect(forward.getByText('Live forward (saved rules)', { exact: true })).toBeVisible()
  await expectNoHorizontalScroll(page)

  // A newer measured source refreshes only the saved-rule forward; the modified backtest stays on screen.
  await page.route('**/api/strategies/lab-sol-ema', async (route) => {
    const response = await route.fetch()
    const body = await response.json()
    const next = body.strategy.forward
    next.asOf = '2026-10-05T04:00:00Z'
    next.metrics.endT += 14400
    next.metrics.bars += 1
    for (const points of [next.equity, next.benchmark]) {
      const last = points[points.length - 1]
      points.push({ t: last.t + 14400, v: last.v })
    }
    await route.fulfill({ response, json: body })
  })
  await page.getByRole('button', { name: 'Refresh live forward', exact: true }).click()
  await expect(forward).toContainText('Source as of 2026-10-05 04:00:00 UTC')
  await expect(page.getByRole('heading', { name: 'Modified backtest vs buy & hold' })).toBeVisible()
  await expect(tile(page, 'Total return')).toContainText('16.4%')
  await expect(forward).not.toContainText('16.4%')
  const refreshedMetrics = await metrics.innerText()
  await expectNoHorizontalScroll(page)

  await page.getByRole('button', { name: 'Reset to saved', exact: true }).click()
  await expect(page.getByText('Modified · not saved', { exact: true })).toHaveCount(0)
  await expect(page.getByRole('heading', { name: 'Saved backtest vs buy & hold' })).toBeVisible()
  await expect(tile(page, 'Total return')).toContainText('24.5%')
  await expect(metrics).toHaveText(refreshedMetrics, { useInnerText: true })
  await expect(forward).toContainText('Source as of 2026-10-05 04:00:00 UTC')
  await expect(forward).not.toContainText('modified rules on screen')
  await expectAccessible(page)
  await expectNoHorizontalScroll(page)
})

test('forward: Own, Community and Labs rows expose their measured source and unavailable states @mobile', async ({ page }) => {
  const copyResponse = await page.request.post(`${API_URL}/api/strategies/lab-sol-ema/copy`, { data: {} })
  expect(copyResponse.ok()).toBe(true)
  const { strategy: copy } = await copyResponse.json()
  await page.goto('/strategies')
  const own = page.getByRole('region', { name: 'My strategies' }).getByRole('link', { name: /SOL EMA cross/ })
  await expect(own).toContainText('Since saved')
  await expect(own).toContainText('Too new to measure')
  await own.click()
  const saved = page.getByRole('region', { name: 'Live forward', exact: true })
  await expect(saved).toContainText('Since saved')
  await expect(saved).toContainText('Too new to measure')
  await expect(saved).not.toContainText('Since launch')
  await expect(saved).not.toContainText('0.0%')
  expect(copy.forward.since).toBe(copy.createdAt)
  expect(copy.forward.basis).toBe('saved')
  expect(copy.forward.equity).toEqual([])

  await page.goto('/strategies?tab=top&top=community')
  const board = page.getByRole('list', { name: 'Community strategies' })
  const fresh = board.getByRole('listitem').filter({ has: page.getByRole('link', { name: 'ETH breakout retest', exact: true }) })
  await expect(fresh).toContainText('Since publish')
  await expect(fresh).toContainText('Sep 1, 2026')
  await expect(fresh).toContainText('34 days')
  await expect(fresh).toContainText('4.2%')
  await expect(fresh).toContainText('B&H ETH +1.3%')
  const stale = board.getByRole('listitem').filter({ has: page.locator('a[href="/strategies/c-sol-rsi"]') })
  await expect(stale).toContainText('Stale')
  await expect(stale).toContainText('Early')
  await expect(stale).toContainText('2026-10-01 00:00:00 UTC')
  await expect(stale).toContainText('2.4%')
  const unavailable = board.getByRole('listitem').filter({ has: page.getByRole('link', { name: 'BTC dip ladder', exact: true }) })
  await expect(unavailable).toContainText('complete history')
  await expect(unavailable).not.toContainText('too new')
  await expect(unavailable).not.toContainText('B&H BTC +0.0%')
  await page.getByRole('combobox', { name: 'Community strategy order' }).selectOption('forward')
  await expect(board.getByRole('listitem').nth(0)).toContainText('ETH breakout retest')
  await expect(board.getByRole('listitem').nth(1)).toContainText('Stale')
  await expect(board.getByRole('listitem').nth(2)).toContainText('BTC dip ladder')
  await expectNoHorizontalScroll(page)

  await page.goto('/strategies?tab=top')
  const labs = page.getByRole('table').filter({ has: page.getByRole('columnheader', { name: 'Strategy', exact: true }) })
  const labSol = labs.getByRole('row').filter({ has: page.getByRole('link', { name: 'SOL EMA cross', exact: true }) })
  await expect(labSol).toContainText('Since launch')
  await expect(labSol).toContainText('Oct 3, 2026')
  await expect(labSol).toContainText('B&H SOL')
  await expect(labSol).toContainText('Early')
  const labBtc = labs.getByRole('row').filter({ has: page.getByRole('link', { name: 'BTC Donchian breakout', exact: true }) })
  await expect(labBtc).toContainText('Too new to measure')
  await expect(labBtc).not.toContainText('B&H BTC +0.0%')
  await expectAccessible(page)
  await expectNoHorizontalScroll(page)
})

test('forward detail: stale success retains its source while missing history shows no measured return @mobile', async ({ page }) => {
  await page.goto('/strategies/c-sol-rsi')
  const stale = page.getByRole('region', { name: 'Live forward', exact: true })
  await expect(stale).toContainText('Stale')
  await expect(stale).toContainText('Source as of 2026-10-01 00:00:00 UTC')
  await expect(stale).toContainText('Market data')
  await expect(stale).toContainText('Early')
  const metrics = stale.getByRole('table', { name: 'Saved backtest and live forward metrics' })
  await expect(metrics.getByRole('row', { name: /^Return/ }).getByRole('cell').nth(1)).toHaveText(/2\.4%/)
  await expect(metrics.getByRole('row', { name: /^Buy & hold/ }).getByRole('cell').nth(1)).toHaveText('+3.1%')
  await expectNoHorizontalScroll(page)

  await page.goto('/strategies/c-btc-dip')
  const missing = page.getByRole('region', { name: 'Live forward', exact: true })
  await expect(missing).toContainText('complete history')
  await expect(missing).toContainText('No measured window')
  await expect(missing).toContainText('Source as of 2026-10-05 00:00:00 UTC')
  await expect(missing.getByRole('table')).toHaveCount(0)
  await expect(missing).not.toContainText('0.0%')
  await expect(missing).not.toContainText('No trades yet')
  await expect(missing).not.toContainText('Too new to measure')
  await expectAccessible(page)
  await expectNoHorizontalScroll(page)

  await page.goto('/strategies/lab-btc-donchian')
  const tooNew = page.getByRole('region', { name: 'Live forward', exact: true })
  await expect(tooNew).toContainText('Too new to measure')
  await expect(tooNew).toContainText('No measured window')
  await expect(tooNew).not.toContainText('complete history')
  await expect(tooNew.getByRole('table')).toHaveCount(0)
  await expectNoHorizontalScroll(page)
})

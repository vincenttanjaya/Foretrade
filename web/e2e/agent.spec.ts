import * as fx from '../src/test/fixtures'
import { expect, expectAccessible, expectNoHorizontalScroll, httpError, test } from './fixtures'
import { ANSWER, API_URL, BTC_SHORT_ANSWER, LONG_OUTLOOK_ANSWER, NVDA_DRIFT_ANSWER, PENDING_ANSWER, PENDING_CHAT, SOL_AGENT_ANSWER, TSM_VIEW_ANSWER } from './mock-api'
import type { ChatStreamRequest } from '../src/lib/types'

test('an answer streams in with tool chips, [n] citations and a trade card that fills on confirm', { tag: '@mobile' }, async ({ page }) => {
  await page.goto('/')
  // Record every rendered state of the conversation, to prove the answer arrived in pieces.
  await page.evaluate(() => {
    const seen: string[] = ((window as unknown as { seen: string[] }).seen = [])
    new MutationObserver(() => {
      const text = document.querySelector('[aria-label="Conversation"]')?.textContent ?? ''
      if (text !== seen.at(-1)) seen.push(text)
    }).observe(document.body, { subtree: true, childList: true, characterData: true })
  })

  await page.getByLabel('Message the agent').fill('Should I buy BTC here?')
  await page.keyboard.press('Enter')

  await expect(page).toHaveURL(/\/chat\/[\w-]+$/) // the `chat` event names the new chat
  const tools = page.getByRole('list', { name: 'Agent activity' }).getByRole('listitem')
  await expect(tools.filter({ hasText: 'Searching the news: “BTC ETF flows”' })).toBeVisible()
  await expect(tools.filter({ hasText: 'Drafting a BTC trade' })).toBeVisible()

  // Deltas: the full answer, with [1] and [2] turned into links to the sources card's items.
  await expect(page.getByText(/BTC holds its uptrend as spot ETFs/)).toHaveText(ANSWER.replace(/\[(\d)\]/g, '$1'))
  await expect(page.getByRole('link', { name: '1', exact: true })).toHaveAttribute('href', 'https://www.reuters.com/markets/btc-etf')
  await expect(page.getByRole('link', { name: '2', exact: true })).toHaveAttribute('href', 'https://www.coindesk.com/markets/funding')
  await expect(page.getByRole('list', { name: 'Sources' }).getByRole('listitem')).toHaveCount(2)
  await expect(page.getByRole('status', { name: 'Running' })).toHaveCount(0) // every tool chip settled

  const seen = await page.evaluate(() => (window as unknown as { seen: string[] }).seen)
  expect(seen.some((t) => t.includes('BTC holds') && !t.includes('stop under support fits.')), 'a partial answer was on screen').toBe(true)

  // Trade card → paper order → the position shows up in the account.
  await page.getByRole('button', { name: 'Confirm buy (paper)' }).click()
  await expect(page.getByRole('status').filter({ hasText: 'simulated fill' })).toContainText(/^Filled [\d.]+ BTC @ \$84,2\d\d\.\d\d/)
  await expect(page.getByRole('status').filter({ hasText: '(paper)' })).toContainText(/Filled [\d.]+ BTC/) // the toast
  await page.getByRole('link', { name: 'View account' }).click()
  await expect(page.getByRole('heading', { level: 1, name: 'Paper account' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Close BTC position at market' })).toBeVisible()
  await expect(page.getByRole('row').filter({ hasText: 'Agent' }).filter({ hasText: 'Buy' })).toHaveCount(1) // the trade, not the P&L-by-source row
})

test('a finished chat reloads from history with its citations and cards', async ({ page }) => {
  await page.goto('/')
  await page.getByLabel('Message the agent').fill('Should I buy BTC here?')
  await page.keyboard.press('Enter')
  await expect(page.getByRole('list', { name: 'Sources' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Confirm buy (paper)' })).toBeVisible()
  await expect(page.getByRole('list', { name: 'Suggested follow-ups' })).toBeVisible() // the answer is done, so stored

  await page.reload()
  await expect(page.getByRole('heading', { level: 1, name: 'Should I buy BTC here?' })).toBeAttached()
  await expect(page.getByRole('link', { name: '1', exact: true })).toHaveAttribute('href', 'https://www.reuters.com/markets/btc-etf')
  await expect(page.getByRole('button', { name: 'Confirm buy (paper)' })).toBeVisible()
  await expect(page.getByRole('navigation', { name: 'Chat history' }).first()).toContainText('Should I buy BTC here?')
})

test('a confirmed trade card stays filled after a reload, and a follow-up chip asks the next question', { tag: '@mobile' }, async ({ page }) => {
  await page.goto('/')
  await page.getByLabel('Message the agent').fill('Should I buy BTC here?')
  await page.keyboard.press('Enter')
  const chips = page.getByRole('list', { name: 'Suggested follow-ups' })
  await expect(chips.getByRole('button')).toHaveText(['Size this for 1% account risk', 'What would invalidate this?', 'Compare with ETH'])
  await expect(page.getByRole('list', { name: 'Agent activity' })).not.toContainText('Suggesting next steps')

  await page.getByRole('button', { name: 'Confirm buy (paper)' }).click()
  const receipt = page.getByRole('status').filter({ hasText: 'simulated fill' })
  await expect(receipt).toContainText(/vs proposed \$84,2\d\d\.\d\d \(\+0\.02%\)/)
  await page.reload()
  await expect(receipt).toContainText(/^Filled [\d.]+ BTC @ \$84,2\d\d\.\d\d/)
  await expect(page.getByRole('button', { name: 'Confirm buy (paper)' })).toHaveCount(0)

  await chips.getByRole('button', { name: 'What would invalidate this?' }).click()
  await expect(page.getByRole('list', { name: 'Conversation' }).getByText('What would invalidate this?', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Confirm buy (paper)' })).toBeVisible() // the new answer's own card
  await expect(chips).toHaveCount(1) // chips only under the latest answer
})

test.describe(() => {
  test.use({ allowedErrors: [httpError(409)] }) // Chrome logs the refused repeat order

  test('confirming a card already filled in another tab says so instead of filling twice', async ({ page }) => {
    await page.goto('/')
    await page.getByLabel('Message the agent').fill('Should I buy BTC here?')
    await page.keyboard.press('Enter')
    await expect(page.getByRole('list', { name: 'Suggested follow-ups' })).toBeVisible()
    const other = await page.context().newPage()
    await other.goto(page.url())
    await other.getByRole('button', { name: 'Confirm buy (paper)' }).click()
    await expect(other.getByRole('status').filter({ hasText: 'simulated fill' })).toBeVisible()

    await page.getByRole('button', { name: 'Confirm buy (paper)' }).click() // this tab still shows it open
    await expect(page.getByRole('status').filter({ hasText: 'Already filled' })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Confirm buy (paper)' })).toHaveCount(0)
    const { trades } = await (await page.request.get(`${API_URL}/api/portfolio/trades`)).json()
    expect(trades.filter((t: { source: string }) => t.source === 'agent'), 'one fill for one card').toHaveLength(1)
  })
})

test('a forecast request drafts a desk question in the chat, and Run follows it to the headline', async ({ page }) => {
  await page.goto('/')
  await page.getByLabel('Message the agent').fill('Forecast whether SOL closes above $150 on Nov 28')
  await page.keyboard.press('Enter')
  const card = page.locator('.card').filter({ hasText: 'Forecast desk draft' })
  await expect(card).toContainText(/Will the Binance SOL\/USDT daily close on .* be above \$150\?/)
  await expect(card).toContainText('Auto-resolves from Binance')
  await expect(card.getByRole('link', { name: 'Open Forecast desk' })).toHaveAttribute('href', '/forecasts/new')

  await card.getByRole('button', { name: 'Run the desk · 5 cr' }).click()
  await expect(card.getByRole('status')).toContainText('Specialists at work… usually four to eight minutes')
  await expect(card.getByText('Yes 62%')).toBeVisible() // the mock completes on the second poll
  await card.getByRole('link', { name: 'Open report' }).click()
  await expect(page).toHaveURL(/\/forecasts\/[\w-]+$/)
})

test('a basket request builds an AI portfolio from its card', { tag: '@mobile' }, async ({ page }) => {
  await page.goto('/')
  await page.getByLabel('Message the agent').fill('Build me a defensive crypto basket')
  await page.keyboard.press('Enter')
  const card = page.locator('.card').filter({ hasText: 'AI portfolio' })
  await expect(card).toContainText('“a defensive crypto basket”')
  await card.getByRole('button', { name: 'Build portfolio · 2 cr' }).click()
  await expect(card.getByRole('listitem')).toHaveText(['BTC 50%', 'ETH 30%', 'PAXG 20%'])
  await expect(card.getByRole('link', { name: 'Open & invest' })).toHaveAttribute('href', /^\/portfolios\/[\w-]+$/)
  await expectNoHorizontalScroll(page)
})

test('the Latest pill shows only away from the bottom of the conversation', { tag: '@mobile' }, async ({ page, isMobile }) => {
  test.skip(!isMobile, 'one answer fits a desktop screen, so there is nothing to scroll')
  await page.goto('/')
  await page.getByLabel('Message the agent').fill('Should I buy BTC here?')
  await page.keyboard.press('Enter')
  await expect(page.getByRole('list', { name: 'Suggested follow-ups' })).toBeVisible() // the answer is done
  const pill = page.locator('button', { hasText: 'Latest' })
  const scroller = page.getByRole('list', { name: 'Conversation' }).locator('xpath=ancestor::div[contains(@class, "overflow-y-auto")][1]')
  await expect(pill).toHaveAttribute('aria-hidden', 'true') // at the bottom: it would only cover the last card
  await expect(pill).toHaveCSS('opacity', '0')

  await scroller.evaluate((el) => el.scrollTo({ top: 0 }))
  await expect(pill).toHaveAttribute('aria-hidden', 'false')
  await pill.click()
  await expect(pill).toHaveAttribute('aria-hidden', 'true')
  await expectNoHorizontalScroll(page)
})

test('an answer citing one source lists it first and folds the rest under Also read', { tag: '@mobile' }, async ({ page }) => {
  await page.goto('/')
  await expect(page.getByTitle('Crypto Fear & Greed Index by alternative.me, updated daily')).toContainText('Fear & Greed 71 · Greed')
  await page.getByLabel('Message the agent').fill('Research SOL for me: what is driving it this week?')
  await page.keyboard.press('Enter')

  await expect(page.getByText('Sources · 4 · 1 cited')).toBeVisible()
  const cited = page.getByRole('list', { name: 'Sources' }).getByRole('link')
  await expect(cited).toHaveText(['Funding rates stay neutral'])
  await expect(page.getByRole('link', { name: '2', exact: true })).toHaveAttribute('href', 'https://www.coindesk.com/markets/funding')
  await expect(page.getByRole('list', { name: 'Also read' })).toHaveCount(0)
  await page.getByRole('button', { name: 'Also read · 3' }).click()
  await expect(page.getByRole('list', { name: 'Also read' }).getByRole('link')).toHaveText([
    'Bitcoin ETFs log a third day of inflows',
    'Solana DEX volumes hit a record',
    'Fed holds rates and signals one more cut',
  ])
  await expectNoHorizontalScroll(page)
})

test('a chat reopened mid-answer says it is still working, then shows the stored answer', { tag: '@mobile' }, async ({ page }) => {
  await page.goto(`/chat/${PENDING_CHAT}`)
  await expect(page.getByRole('list', { name: 'Conversation' })).toContainText('Why is AVAX on the bullish board?')
  await expect(page.getByRole('status').filter({ hasText: 'Still working on this answer…' })).toBeVisible()
  await expect(page.getByLabel('Answer mode')).toBeDisabled() // locked as while streaming
  // The mock stores the answer on its third read: the load, the poll's first read, then the 2 s poll.
  await expect(page.getByText(/AVAX stays on the bullish board/)).toHaveText(PENDING_ANSWER.replace(/\*\*/g, ''), { timeout: 10_000 })
  await expect(page.getByText('Still working on this answer…')).toHaveCount(0)
  await expect(page.getByLabel('Answer mode')).toBeEnabled()
  await expectNoHorizontalScroll(page)
})

for (const theme of ['light', 'dark'] as const) {
  test(`a 10x question gets the 10-year outlook card with its multiples and drift assumption in ${theme}`, { tag: '@mobile' }, async ({ page }) => {
    await page.addInitScript((t) => localStorage.setItem('theme', t), theme)
    await page.goto('/')
    await page.getByLabel('Message the agent').fill('Can BTC 10x in 10 years?')
    await page.keyboard.press('Enter')

    await expect(page.getByText(/^Yes, it's possible, but only if/)).toHaveText(LONG_OUTLOOK_ANSWER)
    const card = page.locator('.card').filter({ hasText: '10-year outlook' })
    await expect(card.getByText('BTC in 10 years')).toBeVisible()
    await expect(card).toContainText('long-run volatility since Jul 17, 2018 (3,000 daily closes)')
    const odds = card.getByRole('table', { name: 'Odds of each multiple of spot over 10 years, zero drift' })
    await expect(odds.getByRole('columnheader')).toHaveText(['Level', 'At horizon', 'Touched at any point'])
    await expect(odds.getByRole('row')).toHaveCount(5)
    await expect(odds.getByRole('row').filter({ hasText: '$842,132' }).getByRole('cell')).toHaveText(['12%', '24%'])
    await expect(card.getByRole('region', { name: 'How long might a 10× take?' }).getByRole('definition')).toHaveText(['<1%', '3%', '10%', '24%'])
    const drift = card.getByRole('region', { name: 'Alternative assumption' })
    await expect(drift).toContainText('Historical drift, halved (assumption)')
    await expect(drift).toContainText('+15.0%/yr drift · median $377,417')
    await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
    await expectAccessible(page)
    await expectNoHorizontalScroll(page)
    // The table fits the card at 375 px instead of clipping its last column.
    expect(await odds.evaluate((t) => t.scrollWidth <= t.parentElement!.clientWidth)).toBe(true)
  })
}

for (const theme of ['light', 'dark'] as const) {
  test(`"can TSMC 10x, by when" gets the view card: median above today, the odds by year and a folded baseline in ${theme}`, { tag: '@mobile' }, async ({ page }) => {
    await page.addInitScript((t) => localStorage.setItem('theme', t), theme)
    await page.goto('/')
    await page.getByLabel('Message the agent').fill('can TSMC 10x, if yes by when')
    await page.keyboard.press('Enter')

    await expect(page.getByText(/^Yes, it's possible, but only if the AI-supercycle/)).toHaveText(TSM_VIEW_ANSWER.replace(/\[(\d)\]/g, '$1'))
    const card = page.locator('.card').filter({ hasText: '5-year view' })
    await expect(card.getByText('TSM in 5 years')).toBeVisible()
    const hero = card.getByRole('region', { name: 'Our view' })
    await expect(hero).toContainText('Our scenarios (subjective weights)')
    await expect(hero.getByText('Median $672.25')).toBeVisible()
    await expect(hero.getByRole('term')).toHaveText(['Above today', '5th percentile', '95th percentile'])
    await expect(card).not.toContainText(/Bear ·|Bull ·/)
    // The view leans up: the ranges above today outweigh the baseline's, and 2× beats 0.5×.
    const odds = card.getByRole('region', { name: 'Odds of a price multiple' }).getByRole('table')
    await expect(odds.getByRole('row').filter({ hasText: '0.5×' }).getByRole('cell')).toHaveText(['12%', '24%', '21%'])
    await expect(odds.getByRole('row').filter({ hasText: /^2×/ }).getByRole('cell')).toHaveText(['34%', '49%', '21%'])

    const when = card.getByRole('region', { name: 'By when' })
    await expect(when).toContainText('Median time to 10× ($4,850): not within 30 years')
    await expect(when.getByRole('row').filter({ hasText: '10 yrs' }).getByRole('cell')).toHaveText(['12%', '15%', '3%'])
    await expect(when).toContainText('Needs 115.4%/yr for 3 years · 58.5% for 5 · 25.9% for 10')
    await expect(card.getByRole('list', { name: 'AI supercycle inputs' }).getByRole('link', { name: '[1]' })).toHaveAttribute('href', fx.tsmSource.url)

    const baseline = card.locator('details')
    await expect(baseline).not.toHaveAttribute('open')
    await expect(baseline.getByText('Not a forecast of direction')).toBeHidden()
    await baseline.getByText('Volatility-only baseline (zero drift): median = today').click()
    await expect(baseline.getByText('Not a forecast of direction')).toBeVisible()

    await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
    await expectAccessible(page)
    await expectNoHorizontalScroll(page)
    for (const table of await card.getByRole('table').all()) expect(await table.evaluate((t) => t.scrollWidth <= t.parentElement!.clientWidth)).toBe(true)
  })
}

for (const theme of ['light', 'dark'] as const) {
  test(`"NVDA 10 years outlook" gets a 10-year card: the capped drift said as such and eight ranges that fit 375 px in ${theme}`, { tag: '@mobile' }, async ({ page }) => {
    await page.addInitScript((t) => localStorage.setItem('theme', t), theme)
    await page.goto('/')
    await page.getByLabel('Message the agent').fill('NVDA 10 years outlook')
    await page.keyboard.press('Enter')

    await expect(page.getByText(NVDA_DRIFT_ANSWER)).toBeVisible()
    const card = page.locator('.card').filter({ hasText: '10-year view' })
    await expect(card.getByText('NVDA in 10 years')).toBeVisible()
    await expect(card.getByText('From your question: "10 years outlook"')).toBeVisible()
    const hero = card.getByRole('region', { name: 'Our view' })
    await expect(hero.getByText('Assumption', { exact: true })).toBeVisible()
    await expect(hero).toContainText("Historical drift since 1999, capped at the market's long-run return + 2 pts (assumption)")
    await expect(hero.getByText('Median $591.60')).toBeVisible()
    await expect(hero).toContainText('drift capped at +9.7%/yr')
    await expect(hero).toContainText("S&P 500 +7.7%/yr since 1966 + 2 pts; NVDA's own history, halved: +15.7%/yr")
    const ranges = card.getByRole('region', { name: 'Price ranges' }).getByRole('listitem')
    await expect(ranges).toHaveCount(8)
    await expect(ranges.last()).toContainText('≥ $3,800')
    // The API's note to the model ("… not horizon_days 1825") stays in the tool result: the card names no tool parameter.
    await expect(card.getByText(/horizon_days|horizon_reason/)).toHaveCount(0)
    await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
    await expectAccessible(page)
    await expectNoHorizontalScroll(page)
    expect(await hero.evaluate((h) => h.scrollWidth <= h.clientWidth)).toBe(true)
    for (const table of await card.getByRole('table').all()) expect(await table.evaluate((t) => t.scrollWidth <= t.parentElement!.clientWidth)).toBe(true)
  })
}

test('a question with no timeframe gets the agent\'s horizon, named once', { tag: '@mobile' }, async ({ page }) => {
  await page.goto('/')
  await page.getByLabel('Message the agent').fill('Where could SOL go?')
  await page.keyboard.press('Enter')
  await expect(page.getByText(SOL_AGENT_ANSWER)).toBeVisible()
  const card = page.locator('.card').filter({ hasText: '3-year view' })
  await expect(card.getByText('Horizon chosen by the agent: 1,095 days (3 years)')).toBeVisible()
  await expect(card.getByText('Chosen by the agent', { exact: true })).toHaveCount(0)
  await expect(card.getByText(/horizon_reason/)).toHaveCount(0)
  await expectAccessible(page)
  await expectNoHorizontalScroll(page)
})

test('"BTC next 3 months" gets a 92-day view: six-figure levels fit 375 px and no multiples table', { tag: '@mobile' }, async ({ page }) => {
  await page.goto('/')
  await page.getByLabel('Message the agent').fill('where does BTC go over the next 3 months?')
  await page.keyboard.press('Enter')

  await expect(page.getByText(BTC_SHORT_ANSWER)).toBeVisible()
  const card = page.locator('.card').filter({ hasText: 'Price view · 92 days' })
  await expect(card.getByText('From your question: "next 3 months"')).toBeVisible()
  const hero = card.getByRole('region', { name: 'Our view' })
  await expect(hero.getByText('Median $101,777')).toBeVisible()
  await expect(hero.getByText('Assumption', { exact: true })).toBeVisible()
  await expect(card.getByRole('region', { name: 'Price ranges' })).toBeVisible()
  await expect(card.getByRole('region', { name: 'Odds of a price multiple' })).toHaveCount(0)
  await expect(card.getByText(/max drawdown -14% \(2025, recovered 2025\)/)).toBeVisible()
  await expectAccessible(page)
  await expectNoHorizontalScroll(page)
  const box = await hero.evaluate((h) => ({ scroll: h.scrollWidth, client: h.clientWidth }))
  expect(box.scroll).toBeLessThanOrEqual(box.client)
})

test('a 10x question looks up the stock quote and answers with a scenario memo, no trade card', async ({ page }) => {
  await page.goto('/')
  await page.getByLabel('Message the agent').fill('Can ACME 10x? I meet the chairman tomorrow.')
  await page.keyboard.press('Enter')
  const tools = page.getByRole('list', { name: 'Agent activity' }).getByRole('listitem')
  await expect(tools.filter({ hasText: 'Looking up ACME quote' })).toBeVisible()
  await expect(page.getByText(/my subjective scenario weights, not measured odds/)).toBeVisible()
  await expect(page.getByRole('table')).toContainText('Moonshot')
  await expect(page.getByRole('status', { name: 'Running' })).toHaveCount(0)
  await expect(page.getByRole('button', { name: /Confirm (buy|sell) \(paper\)/ })).toHaveCount(0)
})

test('the answer lens sits next to the mode, goes with every message and survives a reload', { tag: '@mobile' }, async ({ page }) => {
  const requests: ChatStreamRequest[] = []
  page.on('request', (req) => { if (req.url() === `${API_URL}/api/chat/stream`) requests.push(req.postDataJSON()) })
  await page.goto('/')
  const lens = page.getByLabel('Answer lens')
  await expect(lens).toHaveValue('auto')
  await expect(page.getByLabel('Answer mode')).toBeVisible()
  await lens.selectOption('investor')
  await page.getByLabel('Message the agent').fill('Can ACME 10x? I meet the chairman tomorrow.')
  await page.keyboard.press('Enter')
  await expect(page.getByText(/my subjective scenario weights, not measured odds/)).toBeVisible()
  await expect(page.getByRole('status', { name: 'Running' })).toHaveCount(0)
  expect(requests.map((r) => [r.mode, r.lens])).toEqual([['quality', 'investor']])
  await page.reload()
  await expect(page.getByLabel('Answer lens')).toHaveValue('investor')
  await expectAccessible(page)
  await expectNoHorizontalScroll(page)
})

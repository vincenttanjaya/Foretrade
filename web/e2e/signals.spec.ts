import { expect, expectAccessible, expectNoHorizontalScroll, test, type Page } from './fixtures'
import { API_URL, type SignalsFixture } from './mock-api'

const boardState = async (page: Page, scenario: SignalsFixture) =>
  expect((await page.request.post(`${API_URL}/__e2e/signals-fixture`, { data: { scenario } })).ok()).toBe(true)

test('the board shows both sides with live moves, and a pick opens the agent or the terminal', { tag: '@mobile' }, async ({ page }) => {
  await page.goto('/signals')
  await expect(page.getByRole('heading', { level: 1, name: 'Signals' })).toBeVisible()
  const bullish = page.getByRole('region', { name: 'Bullish' })
  await expect(bullish.getByRole('article')).toHaveCount(2)
  await expect(page.getByRole('region', { name: 'Bearish' }).getByRole('article')).toHaveCount(1)

  const btc = page.getByRole('article', { name: 'BTC bullish pick' })
  await expect(btc).toContainText('$84,213.20') // live price
  await expect(btc.getByText('Working +5.3%')).toBeVisible() // since the 80,000 pick, in words
  await expect(btc.getByText('Price +5.3%')).toBeVisible()
  await expect(btc.getByRole('progressbar')).toHaveAttribute('aria-valuenow', '53')
  await expect(btc).toContainText('Invalidation$72,000.00')
  await expect(btc).toContainText('Ends in 2d 5h')
  // A bearish pick whose price fell is working: green words, never a green down arrow.
  const eth = page.getByRole('article', { name: 'ETH bearish pick' })
  await expect(eth.getByText('Working +6.3%')).toBeVisible()
  await expect(eth.getByText('Price -6.3%')).toBeVisible()
  await expect(eth.getByText('Since pick').locator('xpath=..')).not.toContainText('▼')
  await expect(page.getByText('Signals board · momentum screen')).toBeVisible()
  await expect(page.getByText(/A screen, not a forecast: in testing, its picks did no better than random picks from the same list\.$/)).toBeVisible()
  await expect(page.getByText(/^Board computed 1 h 0 min ago · next refresh about/)).toBeVisible()
  await page.waitForLoadState('networkidle')
  await expectAccessible(page)
  await expectNoHorizontalScroll(page)

  await btc.getByRole('link', { name: 'Trade BTC' }).click()
  await expect(page).toHaveURL(/\/trade\/BTC$/)
  await page.goBack()
  await page.getByRole('button', { name: 'What would invalidate this ETH pick?' }).click()
  await expect(page).toHaveURL(/:\d+\/$/)
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible()
})

test('the volatility filter narrows the cards, and the recent exits show how they did', { tag: '@mobile' }, async ({ page }) => {
  await page.goto('/signals')
  const bullish = page.getByRole('region', { name: 'Bullish' })
  await expect(bullish.getByRole('article')).toHaveCount(2)
  const filter = page.getByRole('group', { name: 'Volatility' })
  await filter.getByRole('button', { name: 'High vol' }).click()
  await expect(bullish.getByRole('article')).toHaveCount(1)
  await expect(bullish.getByRole('article', { name: 'SOL bullish pick' })).toBeVisible()
  await expect(page.getByText('No high-volatility picks on this side right now.')).toBeVisible()
  await expect(page.getByRole('article', { name: 'SOL bullish pick' }).getByRole('list', { name: 'Ask the agent about SOL' }).getByRole('button')).toHaveCount(3)
  await filter.getByRole('button', { name: 'All' }).click()
  await expect(bullish.getByRole('article')).toHaveCount(2)

  const exits = page.getByRole('region', { name: 'Recent exits' })
  await expect(exits.getByRole('row')).toHaveCount(4) // header + 3
  await expect(exits.getByRole('row').nth(1)).toContainText('XRP')
  await expect(exits.getByRole('row').nth(1)).toContainText('Invalidated')
  await expect(exits.getByRole('row').nth(1)).toContainText(/Scored [A-Z][a-z]{2} \d{1,2}, \d{4}/) // waiting for its 72 h score
  await expect(exits.getByRole('row').nth(2)).toContainText('Target hit')
  await expect(exits.getByRole('row').nth(3)).toContainText('72 h ended')
  await expect(exits.getByRole('row').nth(3)).toContainText('Working +2.5%') // a bearish pick whose price fell 2.5%
  await expectNoHorizontalScroll(page)
  await expectAccessible(page)
})

test("the track record shows each side's verdict and the out-of-sample replay", { tag: '@mobile' }, async ({ page, isMobile }) => {
  await page.goto('/signals')
  const record = page.getByRole('region', { name: 'Track record' })
  await expect(record).toContainText('Every pick is scored 72 hours after it was made')
  for (const side of ['All picks', 'Bullish', 'Bearish'])
    await expect(record.getByRole('rowheader', { name: side })).toBeVisible()
  await expect(record.getByText('Not enough scored picks yet (46 of 200) to compare with a random pick.')).toBeVisible()
  await expect(record.getByText(/^In testing on data the rule was not tuned on \(Dec 24, 2025 – Oct 10, 2026, 864 picks\)/)).toBeVisible()
  await expect(record).toContainText('a difference of -0.03% (95% CI -0.97% to +0.72%), so no measurable edge.')
  await expectNoHorizontalScroll(page)
  await expectAccessible(page)

  // On a phone the wide table scrolls inside its card while each side's name and verdict line stay in view.
  await record.getByRole('rowheader', { name: 'Bearish' }).scrollIntoViewIfNeeded()
  const scrolled = await record.getByRole('table').evaluate((t) => {
    t.parentElement!.scrollBy({ left: 400 })
    return t.parentElement!.scrollLeft
  })
  if (isMobile) expect(scrolled).toBeGreaterThan(0)
  await expect(record.getByText('Not enough scored picks yet (21 of 200) to compare with a random pick.')).toBeInViewport({ ratio: 1 })
  await expect(record.getByRole('rowheader', { name: 'Bearish' })).toBeInViewport({ ratio: 1 })
  await expectNoHorizontalScroll(page)
})

test('a new ledger says nothing is scored yet, and a board stored before steady-v1 says it is the previous rule', { tag: '@mobile' }, async ({ page }) => {
  await boardState(page, 'new-ledger')
  await page.goto('/signals')
  await expect(page.getByRole('article', { name: 'BTC bullish pick' })).toBeVisible()
  await expect(page.getByRole('region', { name: 'Recent exits' })).toContainText('No pick has exited yet under the current rule.')
  const record = page.getByRole('region', { name: 'Track record' })
  await expect(record).toContainText('No pick has been scored yet. Picks are scored 72 hours after they are made.')
  await expect(record).toContainText('so no measurable edge') // the replay box carries the evidence meanwhile
  await expectNoHorizontalScroll(page)
  await expectAccessible(page)

  // A board stored before steady-v1, still shown until the first steady refresh: no invalidation, never scored, and
  // its chain's removals with the reasons those boards recorded, outside the empty ledger's record.
  await boardState(page, 'legacy')
  await page.reload()
  const btc = page.getByRole('article', { name: 'BTC bullish pick' })
  await expect(btc.getByText('Working +5.3%')).toBeVisible()
  await expect(btc.getByText('Invalidation')).toHaveCount(0)
  await expect(btc.getByText('Previous rule', { exact: true })).toBeVisible()
  await expect(btc).not.toContainText('Ends in')
  await expect(page.getByText(/^This board was made under the previous rule: its picks have no invalidation level and are not scored/)).toBeVisible()
  const exits = page.getByRole('region', { name: 'Recent exits' })
  await expect(exits.getByRole('row')).toHaveCount(7) // header + 6
  const reasons = exits.getByRole('row').locator('td:last-child')
  await expect(reasons).toHaveText(['Target hit', 'Validity ended', 'Outranked by stronger picks', 'Score no longer cleared ±20', 'No fresh data', 'Left the board'])
  await expect(exits.getByRole('row').nth(1).getByRole('cell').nth(4)).toHaveText('Not scoredPrevious rule')
  await expect(exits).toContainText('Previous rule: a pick of a board made before the current rule.')
  await expect(page.getByRole('region', { name: 'Track record' })).toContainText('No pick has been scored yet.')
  await expectNoHorizontalScroll(page)
  await expectAccessible(page)
})

test('the week after the switch lists the ledger exit first, then the previous rule\'s removals with their own reasons', { tag: '@mobile' }, async ({ page }) => {
  await boardState(page, 'switch')
  await page.goto('/signals')
  await expect(page.getByRole('article', { name: 'BTC bullish pick' }).getByText('Ends in 2d 5h')).toBeVisible() // a steady board
  await expect(page.getByText(/^This board was made under the previous rule/)).toHaveCount(0)
  const exits = page.getByRole('region', { name: 'Recent exits' })
  const rows = exits.getByRole('row')
  await expect(rows).toHaveCount(9) // header + 1 ledger exit + 7 legacy rows
  await expect(rows.nth(1)).toContainText('XRP')
  await expect(rows.nth(1)).toContainText('Invalidated')
  await expect(rows.nth(1)).toContainText(/Scored [A-Z][a-z]{2} \d{1,2}, \d{4}/) // the ledger exit waits for its 72 h score
  await expect(rows.nth(2)).toContainText('SOL')
  await expect(rows.nth(2)).toContainText('Left the board') // the first steady board carried no legacy pick
  await expect(rows.nth(2)).toContainText('Not scoredPrevious rule')
  await expect(rows.nth(4)).toContainText('Validity ended') // a legacy expiry, not the ledger's "72 h ended"
  await expect(exits).not.toContainText('72 h ended')
  await expect(exits).toContainText('Previous rule: a pick of a board made before the current rule.')
  await expect(page.getByRole('region', { name: 'Track record' })).toContainText('Since ')
  await expectNoHorizontalScroll(page)
  await expectAccessible(page)
})

test('the rail lists Signals on desktop', async ({ page }) => {
  await page.goto('/news')
  await page.getByRole('navigation', { name: 'Primary' }).first().getByRole('link', { name: 'Signals' }).click()
  await expect(page).toHaveURL(/\/signals$/)
  await expect(page).toHaveTitle('Signals board (momentum screen) · Foretrade')
})

test('on a phone the bottom bar keeps eight tabs and News links to the board', { tag: '@mobile' }, async ({ page, isMobile }) => {
  test.skip(!isMobile, 'phone layout')
  await page.goto('/news')
  const bar = page.getByRole('navigation', { name: 'Primary' }).last()
  await expect(bar.getByRole('link')).toHaveCount(8)
  await expect(bar.getByRole('link', { name: 'Signals' })).toHaveCount(0)
  await expectNoHorizontalScroll(page)
  await page.getByRole('link', { name: 'Signals board →' }).click()
  await expect(page).toHaveURL(/\/signals$/)
  await expect(page.getByRole('article', { name: 'ETH bearish pick' })).toBeVisible()
})

test('while a new board is computed the last valid one stays, and the fresh one replaces it on the next poll', { tag: '@mobile' }, async ({ page }) => {
  await page.clock.install()
  await boardState(page, 'refreshing')
  await page.goto('/signals')
  await expect(page.getByRole('article', { name: 'BTC bullish pick' })).toBeVisible()
  await expect(page.getByText(/^Board computed 3 h 55 min ago Refreshing · new board in a few minutes$/)).toBeVisible()
  await expect(page.getByText(/next refresh about/)).toHaveCount(0)
  await expect(page.getByText('The first board is being computed.')).toHaveCount(0)
  await expectNoHorizontalScroll(page)
  await expectAccessible(page)

  await boardState(page, 'ready')
  const poll = page.waitForResponse((r) => r.url().endsWith('/api/signals'))
  await page.clock.fastForward(30_000)
  await poll
  await expect(page.getByText(/^Board computed 1 h 0 min ago · next refresh about/)).toBeVisible()
  await expect(page.getByText('Refreshing · new board in a few minutes')).toHaveCount(0)
})

test('with no valid board it says the board is refreshing, and only a never-computed board is "first"', { tag: '@mobile' }, async ({ page }) => {
  await page.clock.install()
  await boardState(page, 'empty')
  await page.goto('/signals')
  await expect(page.getByText('Refreshing the board · usually 2–3 minutes')).toBeVisible()
  await expect(page.getByText('The first board is being computed.')).toHaveCount(0)
  await expect(page.getByRole('region', { name: 'Bullish' })).toHaveCount(0)
  await expectNoHorizontalScroll(page)
  await expectAccessible(page)

  await boardState(page, 'ready')
  const poll = page.waitForResponse((r) => r.url().endsWith('/api/signals'))
  await page.clock.fastForward(30_000)
  await poll
  await expect(page.getByRole('article', { name: 'ETH bearish pick' })).toBeVisible()

  await boardState(page, 'first')
  await page.reload()
  await expect(page.getByText('The first board is being computed.')).toBeVisible()
  await expect(page.getByText('Refreshing the board · usually 2–3 minutes')).toHaveCount(0)
})

test('after failed refreshes it says when the next try is, tags a stale board and keeps the exits', { tag: '@mobile' }, async ({ page }) => {
  await boardState(page, 'failing')
  await page.goto('/signals')
  await expect(page.getByRole('article', { name: 'BTC bullish pick' })).toBeVisible() // the last valid board stays
  await expect(page.getByText(/^Board computed 9 h 5 min ago Stale · Last refresh failed · next try about \d{1,2}:\d{2} [AP]M$/)).toBeVisible()
  await expect(page.getByText('Refreshing · new board in a few minutes')).toHaveCount(0)
  await expect(page.getByRole('region', { name: 'Recent exits' }).getByRole('row')).toHaveCount(4) // from the ledger, not candles
  await expectNoHorizontalScroll(page)
  await expectAccessible(page)

  await boardState(page, 'failing-empty')
  await page.reload()
  await expect(page.getByRole('heading', { name: "The last refresh didn't produce a board" })).toBeVisible()
  await expect(page.getByText(/^Next try about \d{1,2}:\d{2} [AP]M\. Picks appear here on their own\.$/)).toBeVisible()
  await expect(page.getByText('Refreshing the board · usually 2–3 minutes')).toHaveCount(0)
  await expectNoHorizontalScroll(page)
  await expectAccessible(page)
})

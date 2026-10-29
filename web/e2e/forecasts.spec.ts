import * as fx from '../src/test/fixtures'
import { expect, expectAccessible, expectNoHorizontalScroll, test } from './fixtures'
import { DEEP_FORECAST, DEEP_SOURCE, DERIVED_FORECAST, LADDER_FORECAST, LEGACY_FORECAST, MARKET_FORECAST, SEED, VIEW_FORECAST, WIRE_FORECAST } from './mock-api'

test('archive: saved reports remain readable and restore without credits @mobile', async ({ page }) => {
  await page.goto(`/forecasts/${SEED.forecast}`)
  await expect(page.getByText('Probability estimate · Yes')).toBeVisible()
  const credits = await page.locator('[aria-describedby="credits-tip"]').textContent()
  await page.getByRole('button', { name: 'Archive', exact: true }).click()
  await page.getByRole('group', { name: 'Confirm archive' }).getByRole('button', { name: 'Archive', exact: true }).click()
  await expect(page).toHaveURL(/\/forecasts$/)
  await expect(page.getByRole('heading', { name: 'No forecasts yet.' })).toBeVisible()
  await page.getByRole('group', { name: 'Filter by status' }).getByRole('button', { name: /^Archived/ }).click()
  const row = page.getByRole('link', { name: /Will ETH outperform BTC/ })
  await expect(row).toContainText('Archived')
  await expect(row).toContainText('62%')
  await row.click()
  await expect(page.getByText(/This forecast is archived/)).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Research' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Export PDF' })).toBeVisible()
  await expect(page.getByRole('button', { name: /Update forecast/ })).toHaveCount(0)
  await expect(page.getByRole('group', { name: 'What happened' })).toHaveCount(0)
  await page.getByRole('button', { name: 'Restore forecast' }).click()
  await expect(page.getByRole('button', { name: 'Archive', exact: true })).toBeVisible()
  await expect(page.locator('[aria-describedby="credits-tip"]')).toHaveText(credits!)
  await page.getByRole('link', { name: 'Forecast desk', exact: true }).click()
  await expect(page.getByRole('link', { name: /Will ETH outperform BTC/ })).toContainText('Completed')
})

test('manual forecast: guided steps preserve the draft and only run after review @mobile', async ({ page }) => {
  await page.goto('/forecasts/new')
  await page.getByRole('button', { name: 'Create manually' }).click()
  await expect(page.getByRole('heading', { level: 1, name: 'Choose the shape.' })).toBeVisible()
  await expect(page.getByRole('button', { name: /Run forecast/ })).toHaveCount(0)
  await page.getByRole('button', { name: 'Continue to Ask' }).click()
  await expect(page.getByRole('button', { name: 'Continue to Resolve' })).toBeDisabled()
  const question = 'Will the Binance BTC/USDT close exceed $150,000 on December 31, 2027?'
  await page.getByLabel('Question', { exact: true }).fill(question)
  await page.getByRole('button', { name: 'Continue to Resolve' }).click()
  await page.getByLabel('Resolution criteria').fill('Yes if the Binance BTCUSDT UTC daily close exceeds $150,000 on the resolution date; otherwise No.')
  await page.getByLabel('Resolution date', { exact: true }).fill('2027-12-31')
  await page.getByRole('button', { name: 'Continue to Review' }).click()
  await expect(page.getByRole('heading', { level: 1, name: 'Review your forecast.' })).toBeVisible()
  await expect(page.getByText(question, { exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Back to Resolve' }).click()
  await expect(page.getByLabel('Resolution date', { exact: true })).toHaveValue('2027-12-31')
  await page.getByRole('button', { name: 'Back to Ask' }).click()
  await expect(page.getByLabel('Question', { exact: true })).toHaveValue(question)
  await page.getByRole('button', { name: 'Continue to Resolve' }).click()
  await page.getByRole('button', { name: 'Continue to Review' }).click()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  await page.getByRole('button', { name: /Run forecast/ }).click()
  await expect(page).toHaveURL(/\/forecasts\/[\w-]+$/)
  await expect(page.getByRole('heading', { level: 1, name: question })).toBeVisible()
})

test('an idea is drafted, reviewed, run, and polled into a report with its research', async ({ page }) => {
  const idea = 'Will BTC trade above $150k before July 2027?'
  await page.goto('/forecasts')
  await page.getByRole('link', { name: /New forecast/ }).first().click()
  await page.getByLabel('Your rough idea').fill(idea)
  await page.getByRole('button', { name: /Draft with AI/ }).click()

  await expect(page.getByRole('heading', { level: 1, name: 'Review the question.' })).toBeVisible()
  await expect(page.getByLabel('Question', { exact: true })).toHaveValue(idea)
  await expect(page.getByLabel('Resolution criteria')).toHaveValue(/according to Binance data/)
  await expect(page.getByText(/Auto-resolves from Binance · BTC\/USDT daily close above \$150,000 by/)).toBeVisible()
  await page.getByRole('button', { name: 'Add context for the desk (optional)' }).click()
  await page.getByLabel('Context').fill('Our OTC desk saw $2bn of bids this week.')
  await page.getByRole('button', { name: /Run forecast/ }).click()

  await expect(page).toHaveURL(/\/forecasts\/[\w-]+$/)
  await expect(page.getByRole('heading', { level: 1, name: idea })).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Specialists at work' })).toBeVisible()

  // The next poll (3 s) finds it completed.
  await expect(page.getByRole('status').filter({ hasText: 'The desk has a number.' })).toBeVisible({ timeout: 10_000 })
  await expect(page.getByRole('heading', { name: 'Research' })).toBeVisible()
  await expect(page.getByText('Our OTC desk saw $2bn of bids this week.')).toBeVisible() // the user's context, S0
  await expect(page.getByRole('list', { name: 'Search queries' })).toContainText('eth etf flows')
  const market = page.getByRole('region', { name: 'Desk vs market' })
  await expect(market).toContainText('Desk 62%')
  await expect(market).toContainText('Market 55% · Polymarket')
  await expect(market).toContainText('Edge +7 pts')
  await expect(market).toContainText('EV +$0.07 per $1 Yes contract')
  await expect(market).toContainText('Edge on Yes')
  await expect(market.getByRole('link', { name: /View on Polymarket/ })).toHaveAttribute('href', 'https://polymarket.com/event/eth-vs-btc-q4-2026')
  await expect(page.getByRole('link', { name: /ETH ETF flows hit a record/ }).first()).toHaveAttribute('href', 'https://a.io/eth')
  await expect(page.getByRole('heading', { name: 'What the desk looked at' })).toBeVisible()
  await expect(page.getByText(/Resolves automatically from the Binance BTC\/USDT daily close above \$150,000 by/)).toBeVisible()
  await expect(page.getByRole('group', { name: 'What happened' })).toHaveCount(0)

  await page.getByRole('link', { name: 'Forecast desk' }).first().click()
  await expect(page.getByRole('link', { name: new RegExp(idea.replace(/[$?]/g, '\\$&')) })).toBeVisible()
})

test('the track record scores the replay and opens a replay forecast read-only', async ({ page }) => {
  await page.goto('/forecasts')
  const record = page.getByRole('region', { name: 'Scored against what happened' })
  // 24 replay questions are under the 50 a score needs: no percentage, no Brier, no chart.
  await expect(record.getByText('Building a track record')).toBeVisible()
  await expect(record).toContainText('24 of 50 questions scored. We show a score once there are enough resolved questions to tell skill from luck')
  await expect(record.getByRole('img', { name: /Reliability chart/ })).toHaveCount(0)
  await expect(record.locator('[data-claim]')).not.toContainText(/%|Brier/)
  const calls = record.getByRole('region', { name: 'Recent desk calls' })
  await expect(calls.getByRole('link')).toHaveCount(2)
  await expect(calls.getByRole('link').first()).toContainText('Desk Yes 30% → Resolved Yes')
  await expect(calls).not.toContainText('Brier')

  await record.getByRole('button', { name: 'See the questions' }).click()
  await expect(page.getByRole('group', { name: 'Filter by source' }).getByRole('button', { name: /Track record/ })).toHaveAttribute(
    'aria-pressed',
    'true',
  )
  const row = page.getByRole('link', { name: /SOL\/USDT daily close on 2026-08-26/ }).last() // the first is a recent desk call
  await expect(row).toContainText('Replay · as of Aug 12')
  await expect(row).toContainText('Happened: Yes')
  await row.click()

  await expect(page.getByText(/^Historical replay as of Aug 12, 2026/)).toBeVisible()
  await expect(page.getByText(/Resolved Yes · Aug 26, 2026 · auto/)).toBeVisible()
  await expect(page.getByRole('button', { name: 'Archive' })).toHaveCount(0)
  await expect(page.getByRole('button', { name: /Discuss a trade/ })).toHaveCount(0)
  await expect(page.getByRole('region', { name: 'Desk vs market' })).toHaveCount(0) // replays never see a live market
})

// A record the server backs (ahead of the formula over enough questions) and one it does not (no proven edge).
const { replay: REPLAY_RECORD } = fx.trackRecord()
const AHEAD = {
  replay: { ...REPLAY_RECORD, n: 72, anchorN: 72, brier: 0.162, skill: 0.352, brierCi90: [0.13, 0.19], anchorSkill: 0.107, anchorDiffCi90: [-0.04, -0.005],
    claim: { status: 'ahead', basis: 'formula', minN: 50, n: 72, skill: 0.107 } },
  mine: { ...REPLAY_RECORD, n: 60, anchorN: 60, claim: { status: 'no_edge', basis: 'formula', minN: 50, n: 60, skill: null } },
}

for (const theme of ['light', 'dark'] as const) {
  test(`the track record shows no score until one is backed, and a replay shows its reference and guard, in ${theme} @mobile`, async ({ page }, info) => {
    await page.addInitScript((t) => localStorage.setItem('theme', t), theme)
    await page.goto('/forecasts')
    const record = page.getByRole('region', { name: 'Scored against what happened' })
    await expect(record.getByText('Building a track record')).toBeVisible()
    await expect(record.getByRole('progressbar', { name: 'Questions scored' })).toHaveAttribute('aria-valuetext', '24 of 50')
    await expect(record).toContainText('We show a score only once it is backed by enough resolved questions.')
    await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
    await record.screenshot({ path: info.outputPath(`track-record-replay-${theme}.png`) })
    await record.getByLabel('About the volatility-only formula').focus()
    const tip = record.getByRole('tooltip')
    await expect(tip).toHaveCSS('opacity', '1')
    await expect(tip).toContainText('A coin flip is a weak one here')
    // The tooltip spans its line, inside the card, at 375 px too.
    const [tipBox, cardBox] = await Promise.all([tip.boundingBox(), record.boundingBox()])
    expect(tipBox!.x).toBeGreaterThanOrEqual(cardBox!.x)
    expect(tipBox!.x + tipBox!.width).toBeLessThanOrEqual(cardBox!.x + cardBox!.width)
    await expectAccessible(page)
    await expectNoHorizontalScroll(page)
    await page.locator('body').click({ position: { x: 1, y: 1 } })

    await record.getByRole('button', { name: 'Yours · 0' }).click()
    await expect(record).toContainText('Resolve your forecasts to build your own record.')
    await record.screenshot({ path: info.outputPath(`track-record-yours-${theme}.png`) })

    // Backed: the score, its range, the chart and the coin flip second. Yours, not backed: no number at all.
    await page.route('**/api/forecasts/track-record', (route) => route.fulfill({ json: AHEAD }))
    await page.reload()
    await expect(record.getByText(/better than a volatility-only formula$/)).toHaveText('11% better than a volatility-only formula')
    await expect(record).toContainText('Brier 0.162 (90% range 0.13–0.19) · formula 0.181 · coin flip 0.250 · n = 72')
    await expect(record.getByText(/^35% better than a coin flip$/)).toBeVisible()
    await expect(record.getByRole('img', { name: /Reliability chart/ })).toBeVisible()
    await expect(record).not.toContainText('We show a score only once')
    await expectAccessible(page)
    await expectNoHorizontalScroll(page)
    await record.screenshot({ path: info.outputPath(`track-record-replay-ahead-${theme}.png`) })
    await record.getByRole('button', { name: 'Yours · 60' }).click()
    await expect(record.getByText('No proven edge yet')).toBeVisible()
    await expect(record).toContainText('60 questions scored. The desk has not yet beaten a simple volatility formula by more than chance.')
    await expect(record.locator('[data-claim]')).not.toContainText(/%|Brier/)
    await expectNoHorizontalScroll(page)
    await record.screenshot({ path: info.outputPath(`track-record-yours-no-edge-${theme}.png`) })

    // The BTC replay hedged toward 50% without a cited reason: the server reset it to the reference.
    await page.goto('/forecasts/r2234567-cccc')
    const line = page.locator('p').filter({ hasText: /^Volatility-only reference:/ })
    await expect(line).toHaveText('Volatility-only reference: 73% Yes (90-day realized vol 61%/yr, 14 days, as of Jul 15, 2026)')
    const note = page.getByRole('note').filter({ hasText: "The server reset the desk's number to this reference" })
    await expect(note).toBeVisible()
    // Said once: the banner names the same reference, and the server's own flag for the reset is not repeated.
    await expect(page.getByRole('note').filter({ hasText: /^Historical replay as of/ })).toContainText('· volatility-only reference 73% Yes')
    await expect(page.getByText(/^Server check:/)).toHaveCount(0)
    await expect(page.getByRole('region', { name: 'Flags' })).toContainText('Stale dataOne source is a week old.')
    await expectAccessible(page)
    await expectNoHorizontalScroll(page)
    await line.locator('xpath=ancestor::*[contains(@class, "card")][1]').screenshot({ path: info.outputPath(`reference-${theme}.png`) })
    await note.scrollIntoViewIfNeeded()
    await page.screenshot({ path: info.outputPath(`reference-page-${theme}.png`) })
  })
}

test('Export PDF prints the report without the app chrome', async ({ page }) => {
  await page.goto(`/forecasts/${SEED.forecast}`)
  await page.evaluate(() => {
    window.print = () => void document.body.setAttribute('data-printed', '1')
  })
  await page.getByRole('button', { name: 'Export PDF' }).click()
  await expect(page.locator('body')).toHaveAttribute('data-printed', '1')

  await page.emulateMedia({ media: 'print' })
  await expect(page.getByRole('heading', { level: 1, name: /Will ETH outperform BTC/ })).toBeVisible()
  await expect(page.getByRole('region', { name: 'Desk vs market' })).toBeVisible()
  for (const nav of await page.getByRole('navigation', { name: 'Primary' }).all()) await expect(nav).toBeHidden()
  await expect(page.getByRole('banner')).toBeHidden()
  await expect(page.getByRole('button', { name: 'Export PDF' })).toBeHidden()
  await expect(page.getByRole('button', { name: /Discuss a trade/ })).toBeHidden()
  await expect(page.getByText('(https://a.io/eth)').first()).toBeVisible() // links print their address
})

test('a report resolves by hand into your record, then goes to the agent', async ({ page }) => {
  await page.goto(`/forecasts/${SEED.forecast}`)
  await page.getByRole('group', { name: 'What happened' }).getByRole('button', { name: 'Yes' }).click()
  await page.getByRole('button', { name: 'Resolve as Yes' }).click()
  await expect(page.getByText(/Resolved Yes · .+ · manual/)).toBeVisible()

  await page.getByRole('link', { name: 'Forecast desk' }).first().click()
  const record = page.getByRole('region', { name: 'Scored against what happened' })
  await record.getByRole('button', { name: 'Yours · 1' }).click()
  await expect(record).toContainText('1 of 50 questions scored.') // counted, but no score until 50
  await expect(record.locator('[data-claim]')).not.toContainText(/%|Brier/)
  await expect(page.getByRole('link', { name: /Will ETH outperform BTC/ })).toContainText('Happened: Yes')

  await page.goto(`/forecasts/${SEED.forecast}`)
  await expect(page.getByRole('link', { name: 'Open BTC in Trade' })).toHaveAttribute('href', '/trade/BTC')
  await page.getByRole('button', { name: 'Discuss a trade with the agent' }).click()
  await expect(page).toHaveURL(/\/(chat\/[\w-]+)?$/)
  await expect(page.getByText(/I just read the forecast desk report "Will ETH outperform BTC over Q4 2026\?" \(Yes 62%\)/).first()).toBeVisible()
})

test('an update re-runs the report on fresh evidence into a probability history', async ({ page }) => {
  await page.goto(`/forecasts/${SEED.forecast}`)
  await page.getByRole('button', { name: 'Update forecast · 5 credits' }).click()
  await expect(page.getByRole('heading', { name: 'Specialists at work' })).toBeVisible()

  // The next poll (3 s) finds the update completed: two points, and what moved.
  await expect(page.getByRole('status').filter({ hasText: 'The desk has a number.' })).toBeVisible({ timeout: 10_000 })
  const history = page.getByRole('region', { name: 'Probability history' })
  await expect(history.getByRole('link')).toHaveCount(2)
  await expect(history.getByRole('link').first()).toContainText('Yes 62%')
  await expect(history.getByRole('link').last()).toContainText('Yes 68% (+6 pts)')
  await expect(page.getByText('Fresh ETH ETF inflows since the last run moved the desk up.')).toBeVisible()
  await expect(page.getByRole('button', { name: /Update forecast/ })).toBeVisible() // the newest run can update again

  await history.getByRole('link').first().click()
  await expect(page).toHaveURL(new RegExp(`/forecasts/${SEED.forecast}$`))
  await expect(page.getByRole('link', { name: /^A newer run exists: Yes 68% on / })).toBeVisible()
  await expect(page.getByRole('button', { name: /Update forecast/ })).toHaveCount(0)

  await page.getByRole('link', { name: 'Forecast desk' }).first().click()
  const rows = page.getByRole('link', { name: /Will ETH outperform BTC/ })
  await expect(rows).toHaveCount(1) // the lineage lists as its newest run
  await expect(rows).toContainText('Updated')
})

test('a report fed by the stored news wire says so in its header and research card', async ({ page }) => {
  await page.goto(`/forecasts/${WIRE_FORECAST}`)
  await expect(page.getByText('Panel: 2 specialists + aggregator · research: stored news wire · 2 headlines')).toBeVisible()
  await expect(page.getByText('Stored news wire · 2 sources')).toBeVisible()
  await expect(page.getByRole('link', { name: /Solana validators ship Firedancer/ })).toHaveAttribute('href', 'https://www.coindesk.com/sol')
  await expect(page.getByText(/^Stored news wire \(2 headlines\)/)).toBeVisible() // the knowledge note
})

test('context notes stay S0 text, and a file dropped on the context box becomes a material @mobile', async ({ page }) => {
  await page.goto('/forecasts/new')
  await page.getByLabel('Your rough idea').fill('Will BTC trade above $150k before July 2027?')
  await page.getByRole('button', { name: /Draft with AI/ }).click()
  await expect(page.getByRole('heading', { level: 1, name: 'Review the question.' })).toBeVisible()
  await page.getByRole('button', { name: 'Add context for the desk (optional)' }).click()
  await expect(page.getByLabel('Attach file')).toHaveCount(0)
  await page.getByLabel('Context').fill('Our OTC desk saw bids.')
  await page.getByLabel('Context').evaluate((box) => {
    const transfer = new DataTransfer()
    transfer.items.add(new File(['date,close\n2026-09-30,114200\n2026-10-01,116900'], 'closes.csv', { type: 'text/csv' }))
    box.dispatchEvent(new DragEvent('drop', { bubbles: true, cancelable: true, dataTransfer: transfer }))
  })
  await expect(page.getByRole('list', { name: 'Attached files' })).toContainText('closes.csv')
  await expect(page.getByRole('list', { name: 'Attached files' })).toContainText('CSV · 1 kB')
  await expect(page.getByLabel('Context')).toHaveValue('Our OTC desk saw bids.')
  await expect(page.getByText('your notes', { exact: true })).toBeVisible() // the summary: notes only, no S0 file
  await expect(page.getByRole('button', { name: /Run forecast/ })).toContainText('· 6 cr')
  await expectNoHorizontalScroll(page)
  await page.getByRole('button', { name: 'Remove closes.csv' }).click()
  await expect(page.getByRole('button', { name: /Run forecast/ })).toContainText('· 5 cr')
})

test('a paper YES position on the matched Polymarket market settles when the forecast resolves @mobile', async ({ page }) => {
  await page.goto(`/forecasts/${MARKET_FORECAST}`) // completed, so no 5 s poll races the clicks
  const card = page.locator('section').filter({ has: page.getByRole('heading', { name: 'Desk vs market' }) })
  const panel = page.getByRole('group', { name: 'Take a paper position' })
  await expect(panel.getByRole('button', { name: 'Yes · 56¢' })).toHaveAttribute('aria-pressed', 'true') // the desk's edge side
  await expect(panel).toContainText('Win $78.57 if Yes · lose $100 if No')
  await expect(panel).toContainText("At the desk's 62%: EV +$10.71")
  await expect(page.getByRole('button', { name: /Ask the agent about this forecast/ })).toBeVisible() // no universe asset to trade
  await expectNoHorizontalScroll(page)

  await panel.getByRole('button', { name: 'Confirm (paper)' }).click()
  await expect(page.getByText('Bought 178.6 Yes contracts at 56¢ (paper).')).toBeVisible()
  await expect(card).toContainText('Yes · 178.6 contracts @ 56¢ · $100 · pays $178.57 · now 55¢')
  await expect(card.getByText('Open', { exact: true })).toBeVisible()

  await page.getByRole('group', { name: 'What happened' }).getByRole('button', { name: 'Yes' }).click()
  await page.getByRole('button', { name: 'Resolve as Yes' }).click()
  await expect(card.getByText('Won +$78.57')).toBeVisible()
  await expect(panel).toHaveCount(0)
})

test('an ordered report sets the desk against the Polymarket strike ladder @mobile', async ({ page }) => {
  await page.goto(`/forecasts/${LADDER_FORECAST}`) // completed, so no 5 s poll races the assertions
  const card = page.getByRole('region', { name: 'Desk vs market by strike' })
  await expect(card.getByRole('heading', { name: 'Desk vs market by strike' })).toBeVisible()
  await expect(card).toContainText('Polymarket · Bitcoin above ___ on Oct 9')
  const first = card.getByRole('row').nth(1)
  for (const cell of ['$80,000', '90%', '95%', '−5 pts', 'NO']) await expect(first).toContainText(cell)
  await expect(card.getByRole('row').nth(4)).toContainText('thin')
  await card.getByRole('button', { name: 'Refresh odds' }).click()
  await expect(card.getByRole('button', { name: 'Refresh odds' })).toBeEnabled()
  await expectNoHorizontalScroll(page)
})

for (const theme of ['light', 'dark'] as const) {
  test(`a deep-research report shows its evidence base by direction (${theme}) @mobile`, async ({ page }) => {
    await page.addInitScript((t) => localStorage.setItem('theme', t), theme)
    await page.goto(`/forecasts/${DEEP_FORECAST}`) // completed and fixed, so no poll races the clicks
    await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
    await expect(page.getByText('Panel: 2 specialists + aggregator · research: deep · 22 sources · 12 read')).toBeVisible()

    const section = page.getByRole('region', { name: 'What the research found' })
    await expect(section.getByText('Evidence base · 30', { exact: true })).toBeVisible()
    const group = (name: string) => section.getByRole('button', { name: new RegExp(`^${name}`) })
    for (const name of ['Upward', 'Downward', 'Center', 'Tail', 'Context']) await expect(group(name)).toBeVisible()

    const justco = section.locator('li[data-finding]').filter({ hasText: 'JustCo opened at S$0.835' })
    await expect(justco.getByText('slight', { exact: true })).toBeVisible()
    await expect(justco).toContainText('Comparables')
    await expect(justco).toContainText('as of May 22, 2026')
    await expect(justco.getByRole('link', { name: /JustCo slips below offer price/ })).toHaveAttribute('href', DEEP_SOURCE.url)

    // 30 findings: Context starts collapsed.
    const context = section.locator('li[data-finding]').filter({ hasText: 'risk sentiment stayed in check' })
    await expect(group('Context')).toHaveAttribute('aria-expanded', 'false')
    await expect(context).toBeHidden()
    await group('Context').click()
    await expect(context).toBeVisible()
    // Upward has more than six findings: the rest are a click away.
    const upward = section.locator('li[data-finding]').filter({ hasText: 'Centurion Accommodation REIT opened' })
    await expect(upward).toBeHidden()
    await section.getByRole('button', { name: /^Show all \d+$/ }).first().click()
    await expect(upward).toBeVisible()

    await expect(page.getByText('What to watch')).toBeVisible()
    await expect(page.getByText('Public tranche subscription rate')).toBeVisible()
    await expect(page.getByText('Deep research · 2 rounds · 18 searches · 22 sources · 12 read')).toBeVisible()
    await page.getByRole('list', { name: 'Research angles' }).getByText('Comparables').click()
    await expect(page.getByRole('list', { name: 'Queries for Recent SGX mainboard debuts: opening vs offer' })).toContainText('JustCo SGX debut opening price')
    await expect(page.getByRole('heading', { name: 'What the forecast rests on' })).toBeVisible()
    await expectNoHorizontalScroll(page)
  })
}

test('a report stored before deep research renders as it always did @mobile', async ({ page }) => {
  await page.goto(`/forecasts/${LEGACY_FORECAST}`)
  await expect(page.getByText('Panel: 2 specialists + aggregator · research: Brave Search · 8 sources · 6 read')).toBeVisible()
  await expect(page.getByText('Live web research · 8 sources · 6 read')).toBeVisible()
  await expect(page.getByRole('list', { name: 'Search queries' })).toContainText('Orchid Materials prospectus')
  await expect(page.getByRole('heading', { name: 'What the desk looked at' })).toBeVisible()
  await expect(page.getByRole('region', { name: 'What the research found' })).toHaveCount(0)
  await expect(page.getByText('What to watch')).toHaveCount(0)
  await expectNoHorizontalScroll(page)
})

// ── Round 9b: a numeric report leads with the desk's view ──
for (const theme of ['light', 'dark'] as const) {
  test(`a numeric report leads with the desk's median, its ranges and what has to happen, in ${theme} @mobile`, async ({ page }, info) => {
    await page.addInitScript((t) => localStorage.setItem('theme', t), theme)
    await page.goto(`/forecasts/${VIEW_FORECAST}`) // completed and fixed, so no poll races the assertions
    await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
    const hero = page.locator('.card').filter({ hasText: 'Desk view · close on Oct 6, 2031' }).first()
    await expect(hero.getByText('$611', { exact: true })).toBeVisible()
    await expect(hero.getByText('Median', { exact: true })).toBeVisible()
    await expect(hero).toContainText("+26% vs today's $485 · about +4.7% a year")
    await expect(hero.getByRole('img', { name: 'Median $611. 50% range $363 to $989. 80% range about $242 to about $1,480. Today $485.' })).toBeVisible()
    for (const tile of ['Chance at or above today62%', '50% range$363–$989', '80% range≈$242–≈$1,480']) await expect(hero.locator('dl')).toContainText(tile)
    await expect(hero).toContainText('No single range stands out (top range ≥ $1,100 at 20%).')
    await expect(hero).not.toContainText(/most likely/i)
    await expect(hero.getByText(/^Our median is \$611 for Oct 6, 2031/)).toBeVisible()
    await expect(hero.getByText('Valuation scenarios with cited inputs, weighed with the measured history.')).toBeVisible()
    await expectAccessible(page)
    await expectNoHorizontalScroll(page)
    await hero.screenshot({ path: info.outputPath(`desk-view-hero-${theme}.png`) })

    // Scenarios: four cards, stacked on a phone, a row of four on a wide screen; ids link to their sources.
    const section = page.getByRole('region', { name: 'What has to happen' })
    const cards = section.getByRole('listitem').filter({ has: page.getByRole('heading', { level: 3 }) })
    await expect(cards).toHaveCount(4)
    const boxes = await Promise.all((await cards.all()).map((c) => c.boundingBox()))
    const narrow = page.viewportSize()!.width < 768
    if (narrow) expect(boxes.every((b, i) => i === 0 || (Math.abs(b!.x - boxes[0]!.x) < 1 && b!.y > boxes[i - 1]!.y)), 'stacked').toBe(true)
    else expect(boxes.every((b) => Math.abs(b!.y - boxes[0]!.y) < 1), 'one row').toBe(true)
    const bull = cards.filter({ hasText: 'AI supercycle' })
    await expect(bull).toContainText('above $999')
    await expect(bull.getByRole('link', { name: /^S1/ })).toHaveAttribute('href', 'https://news.example/tsmc-guidance')
    await expect(cards.filter({ hasText: 'Taiwan blockade or conflict' })).toContainText('model 5%')
    await bull.getByRole('link', { name: 'H1' }).click()
    await expect(page).toHaveURL(/#measured$/)
    await expect(page.locator('#measured')).toContainText('Past 5-year windows: 74% ended higher (5 independent of 5,469 overlapping)')
    await expectNoHorizontalScroll(page)
    await section.screenshot({ path: info.outputPath(`desk-scenarios-${theme}.png`) })

    const pool = page.locator('.card').filter({ hasText: 'Weighted pool vs final' })
    await expect(pool).toContainText('Historical simulation 30% · Valuation scenarios 45% · Regime & tail risk 25%')
    await expect(pool).toContainText('Methods disagree; the desk resolved them')
    await expectNoHorizontalScroll(page)

    // The binary report's hero, for comparison: unchanged.
    await page.goto(`/forecasts/${SEED.forecast}`)
    const binary = page.locator('.card').filter({ hasText: 'Probability estimate · Yes' }).first()
    await expect(binary).toBeVisible()
    await expect(page.getByText(/Desk view/)).toHaveCount(0)
    await expectNoHorizontalScroll(page)
    await binary.screenshot({ path: info.outputPath(`binary-hero-${theme}.png`) })
  })
}

test('a numeric report stored before the desk view reads through its derived view, its ranges only @mobile', async ({ page }) => {
  await page.goto(`/forecasts/${DERIVED_FORECAST}`)
  const hero = page.locator('.card').filter({ hasText: 'Desk view · for Oct 6, 2031' }).first()
  await expect(hero).toBeVisible()
  await expect(hero.getByText('50% range', { exact: true }).first()).toBeVisible()
  await expect(hero).toContainText('$356–$917')
  await expect(page.getByText("The desk's 50% range for Oct 6, 2031 is $356–$917 and its 80% range ≈$247–≈$1,350.")).toBeVisible()
  await expect(hero.getByText(/^Read from the desk's ranges: no cited scenarios or measured history set its centre/)).toBeVisible()
  await expect(hero.getByText('Median', { exact: true })).toHaveCount(0)
  await expect(hero.getByText('$570')).toHaveCount(0)
  await expect(page.getByText('Chance at or above today')).toHaveCount(0)
  await expect(page.getByText('Most likely range')).toHaveCount(0)
  await expect(page.getByRole('region', { name: 'What has to happen' })).toHaveCount(0)
  await expect(page.getByText('Ensemble vs final')).toBeVisible()
  await expectNoHorizontalScroll(page)
})

test('a chat opened from a numeric report states the desk by its view, not its top range @mobile', async ({ page }) => {
  await page.goto(`/forecasts/${VIEW_FORECAST}`)
  await expect(page.getByText('Specialists · 3', { exact: true })).toBeVisible() // a count, no claim of independence
  await page.getByRole('button', { name: /Discuss a trade with the agent|Ask the agent about this forecast/ }).click()
  await expect(page.getByText(/I just read the forecast desk report ".*TSM ADR on 2031-10-06\?" \(median \$611, 62% at or above today\)/).first()).toBeVisible()
  const ref = page.locator('.card').filter({ hasText: 'Forecast desk' }).filter({ hasText: 'TSM ADR on 2031-10-06' }).first()
  await expect(ref.getByText('Median $611', { exact: true })).toBeVisible()
  await expect(ref.getByText('62% ≥ today', { exact: true })).toBeVisible()
  await expect(ref).not.toContainText(/< \$300|≥ \$1,100/)
  await expect(page.getByRole('link', { name: 'Informed by forecast desk · median $611 · 62% ≥ today' })).toHaveAttribute('href', `/forecasts/${VIEW_FORECAST}`)
  await expectNoHorizontalScroll(page)
  await expectAccessible(page)
})

test('a long-horizon stock draft shows each range against today @mobile', async ({ page }) => {
  await page.goto('/forecasts/new')
  await page.getByLabel('Your rough idea').fill('Where will TSM trade in 5 years?')
  await page.getByRole('button', { name: /Draft with AI/ }).click()
  await expect(page.getByText("Ranges are multiples of today's $485.12 (Yahoo, Oct 6).")).toBeVisible()
  await expect(page.getByText(/vs today$/)).toHaveText([
    'below -48% vs today', '-48%…-26% vs today', '-26%…0% vs today', '0%…+55% vs today', '+55%…+106% vs today', '+106%…+209% vs today', 'above +209% vs today',
  ])
  await expectNoHorizontalScroll(page)
})

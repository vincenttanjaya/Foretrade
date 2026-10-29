import { expect, expectNoHorizontalScroll, test } from './fixtures'
import { RUNNING_DEEP_FORECAST, RUNNING_FORECAST } from './mock-api'

// The mock returns one frozen trace on every poll, so nothing here races the page's 5 s refresh.
test('a running forecast shows its live pipeline trace @mobile', async ({ page }) => {
  await page.goto(`/forecasts/${RUNNING_FORECAST}`)
  await expect(page.getByRole('heading', { name: 'Specialists at work' })).toBeVisible()
  await expect(page.getByText('Panel: 3 specialists + aggregator', { exact: true })).toBeVisible()

  const stage = (key: string) => page.locator(`[data-stage="${key}"]`)
  const queries = stage('research').getByRole('list', { name: 'Search queries' }).getByRole('listitem')
  await expect(queries).toHaveText(['Nvidia Apple Microsoft market cap', 'largest company by market cap 2026'])
  await expect(stage('research')).toContainText('8 sources · 3 read')
  await expect(stage('base_rate')).toContainText('Nvidia 41%')
  await expect(stage('quant')).toContainText('Thinking…')
  await expect(stage('skeptic')).toContainText('Unavailable')
  await expect(stage('aggregator')).toContainText('Up next')
  await expect(stage('aggregator')).toContainText('Weighs the two views')
  await expect(stage('market')).toContainText('Market found')
})

for (const theme of ['light', 'dark'] as const) {
  test(`a deep run shows each research step as it goes (${theme}) @mobile`, async ({ page }) => {
    await page.addInitScript((t) => localStorage.setItem('theme', t), theme)
    await page.goto(`/forecasts/${RUNNING_DEEP_FORECAST}`)
    await expect(page.getByRole('heading', { name: 'Specialists at work' })).toBeVisible()
    await expect(page.getByText(/Usually four to eight minutes/)).toBeVisible()
    const research = page.locator('[data-stage="research"]')
    await expect(research).toContainText('Deep research')
    await expect(research.getByRole('list', { name: 'Research angles' }).getByRole('listitem')).toHaveCount(4)
    await expect(research.getByRole('list', { name: 'Research steps' }).getByRole('listitem')).toHaveText([
      'Planned 4 angles',
      'Round 1 · 12 searches · 58 results',
      '!Screening failed · kept what it had',
      'Read 7 of 10 pages',
      '11 new findings',
      'Looking for gaps…',
    ])
    await expectNoHorizontalScroll(page)
  })
}

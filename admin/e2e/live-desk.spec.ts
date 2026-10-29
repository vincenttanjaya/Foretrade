import type { AdminOverview, SystemStatus } from '../src/lib/types'
import { expect, expectAccessible, expectNoHorizontalScroll, httpError, test } from './fixtures'

type DeskStatus = SystemStatus['jobs']['liveArena']['status']

for (const theme of ['light', 'dark'] as const) {
  test.describe(`live desk in ${theme} theme`, () => {
    test.use({ allowedErrors: [httpError(500)] })

    test('health, UTC cadence and bounded claims survive a failed refresh', { tag: '@mobile' }, async ({ page }) => {
      await page.emulateMedia({ colorScheme: theme })
      let status: DeskStatus = 'live'
      let fail = false
      const error = `<strong>Decision save failed</strong> ${'unavailable'.repeat(20)}`
      await page.route('**/api/admin/overview', async (route) => {
        if (fail) {
          await route.fulfill({ status: 500, contentType: 'application/json', json: { error: { code: 'internal', message: 'Desk refresh unavailable.' } } })
          return
        }
        const response = await route.fetch()
        const body: { overview: AdminOverview } = await response.json()
        body.overview.system.jobs.liveArena = {
          ...body.overview.system.jobs.liveArena,
          status, enabled: status !== 'paused', stale: status === 'stale' || status === 'outage',
          startedAt: status === 'starting' ? null : '2026-10-01T00:00:00Z',
          lastStepAt: status === 'starting' ? null : '2026-10-03T04:00:00Z',
          nextStepAt: status === 'paused' || status === 'starting' ? null : '2026-10-03T08:00:00Z',
          lastError: status === 'stale' || status === 'outage' ? error : null,
        }
        await route.fulfill({ response, json: body })
      })

      await page.goto('/')
      await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
      const desk = page.getByRole('region', { name: 'Live paper desk' })
      await expect(desk).toContainText('StatusLive')
      await expect(desk).toContainText('Every 4 hours · closed bars')
      await expect(desk).toContainText('Oct 1, 2026, 12:00 AM UTC')
      await expect(desk).toContainText('Oct 3, 2026, 4:00 AM UTC')
      await expect(desk).toContainText('Oct 3, 2026, 8:00 AM UTC')
      await expect(desk).toContainText('Agents3 · 1 benched')
      await expect(desk).toContainText('Active copy plans8')
      await expect(desk).toContainText('Claimed decisions today3 / 18')
      await expect(desk).toContainText("Request ceiling for today's claims12")
      await expect(desk).toContainText('72 model requests/day · 4,096 output tokens/request · 90s/decision')
      await expect(desk).not.toContainText(/\$|requests used|requests billed|global\.anthropic/i)
      const features = page.getByRole('heading', { name: 'Features', exact: true }).locator('xpath=ancestor::div[contains(@class,"card")][1]')
      await expect(features.getByRole('row')).toHaveCount(8)
      await expect(features.getByRole('rowheader', { name: 'Copy plans', exact: true })).toBeVisible()
      await expect(features).toContainText('Copy plans counts created plans, including stopped plans.')
      await expectAccessible(page)
      await expectNoHorizontalScroll(page)

      for (const next of ['starting', 'paused', 'stale', 'outage'] as const) {
        status = next
        await page.getByRole('button', { name: 'Refresh', exact: true }).click()
        await expect(desk).toContainText({
          starting: 'Starting · awaiting first decision', paused: 'Paused · job paused',
          stale: 'Stale · last completed step is overdue', outage: 'Outage · live desk unavailable',
        }[next])
        if (next === 'starting' || next === 'paused') await expect(desk.getByText('Next step', { exact: true }).locator('..')).toContainText('—')
        if (next === 'stale' || next === 'outage') {
          await expect(desk).toContainText(error)
          await expect(desk.locator('strong')).toHaveCount(0)
        }
        await expectNoHorizontalScroll(page)
      }

      fail = true
      await page.getByRole('button', { name: 'Refresh', exact: true }).click()
      await expect(page.getByRole('alert')).toContainText("Couldn't refresh: Desk refresh unavailable.")
      await expect(desk).toContainText('Outage · live desk unavailable')
      await expect(desk).toContainText(error)
      await expectNoHorizontalScroll(page)
    })
  })
}

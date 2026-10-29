import { expect, test } from './fixtures'
import { API_URL } from './mock-api'

test('the trade page reads the forecast desk and links to its report; a market without one asks the desk @mobile', async ({ page }) => {
  // A BTC price forecast, run to completion (the mock finishes a run on its second poll).
  const idea = 'Will BTC trade above $150k before July 2027?'
  const { draft } = await (await page.request.post(`${API_URL}/api/forecasts/draft`, { data: { idea } })).json()
  const { forecast } = await (await page.request.post(`${API_URL}/api/forecasts`, { data: draft })).json()
  await expect
    .poll(async () => (await (await page.request.get(`${API_URL}/api/forecasts/${forecast.id}`)).json()).forecast.status)
    .toBe('completed')

  await page.goto('/trade/BTC')
  const chip = page.getByRole('link', { name: /Forecast desk/ })
  await expect(chip).toHaveText(/^Forecast desk · Yes 62% · BTC above \$150,000 by [A-Z][a-z]{2} \d{1,2} →$/)
  await chip.click()
  await expect(page).toHaveURL(`/forecasts/${forecast.id}`)
  await expect(page.getByRole('heading', { level: 1, name: idea })).toBeVisible()

  await page.goto('/trade/ETH')
  await page.getByRole('link', { name: 'Ask the desk about ETH →' }).click()
  await expect(page).toHaveURL('/forecasts/new')
  await expect(page.getByLabel('Your rough idea')).toHaveValue(/^Will ETH close above \$[\d,.]+ on [A-Z][a-z]{2} \d{1,2}\?$/)
})

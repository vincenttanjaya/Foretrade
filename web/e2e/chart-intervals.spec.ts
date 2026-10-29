import { expect, expectNoHorizontalScroll, test } from './fixtures'

for (const symbol of ['BTC', '6501.T', 'NVDA']) {
  test(`${symbol} weekly and monthly charts preserve currency and load earlier history @mobile`, async ({ page }) => {
    await page.goto(`/trade/${symbol}`)
    if (symbol === '6501.T') await expect(page.getByRole('group', { name: 'Display currency' }).getByRole('button', { name: 'JPY', exact: true })).toHaveAttribute('aria-pressed', 'true')
    const intervals = page.getByRole('group', { name: 'Chart interval' })
    for (const [label, interval] of [['1W', '1w'], ['1M', '1M']] as const) {
      const readEarlier = () => page.waitForResponse((response) => {
        const url = new URL(response.url())
        return url.pathname === '/api/market/candles' && url.searchParams.get('symbol') === symbol && url.searchParams.get('interval') === interval && url.searchParams.has('before') && response.ok()
      })
      // A perp has fewer than one screen of long bars, so the chart reaches its retained edge automatically.
      const automaticEarlier = symbol === 'NVDA' ? readEarlier() : null
      const loaded = page.waitForResponse((response) => {
        const url = new URL(response.url())
        return url.pathname === '/api/market/candles' && url.searchParams.get('symbol') === symbol && url.searchParams.get('interval') === interval && !url.searchParams.has('before') && response.ok()
      })
      await intervals.getByRole('button', { name: label, exact: true }).click()
      const response = await loaded
      expect(new URL(response.url()).searchParams.get('currency')).toBe(symbol === '6501.T' ? 'original' : null)
      const { candles } = await response.json() as { candles: { t: number }[] }
      expect(candles.length).toBeGreaterThan(1)
      for (const { t } of candles) {
        const date = new Date(t * 1000)
        expect(interval === '1w' ? date.getUTCDay() : date.getUTCDate()).toBe(1)
        expect(date.getUTCHours()).toBe(0)
      }
      await expect(intervals.getByRole('button', { name: label, exact: true })).toHaveAttribute('aria-pressed', 'true')
      await expectNoHorizontalScroll(page)
      const earlier = automaticEarlier ?? readEarlier()
      if (!automaticEarlier) await page.getByRole('button', { name: 'Load earlier', exact: true }).click()
      const historical = await earlier
      expect(new URL(historical.url()).searchParams.get('before')).toBe(String(candles[0].t))
      expect(new URL(historical.url()).searchParams.get('currency')).toBe(symbol === '6501.T' ? 'original' : null)
      const previous = await historical.json() as { candles: { t: number }[] }
      if (symbol === 'NVDA') {
        expect(previous.candles).toHaveLength(0)
        await expect(page.getByText('No earlier candles available from this feed.')).toBeVisible()
      } else expect(previous.candles.length).toBeGreaterThan(0)
      expect(previous.candles.every((c) => c.t < candles[0].t)).toBe(true)
      await expectNoHorizontalScroll(page)
    }
  })
}

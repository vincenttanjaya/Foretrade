// Every route, signed in, at 1440 px and 375 px: it renders its data with no console errors (fixtures.ts), passes the
// basic a11y checks and never scrolls sideways.
import { expect, expectAccessible, expectNoHorizontalScroll, test } from './fixtures'
import { LEGACY_STRATEGY, SEED } from './mock-api'

const PAGES: { path: string; h1: string | RegExp }[] = [
  { path: '/', h1: 'Which market is on your mind?' },
  { path: `/chat/${SEED.chat}`, h1: 'Is BTC overbought right now?' },
  { path: '/trade/BTC', h1: 'Bitcoin' },
  { path: '/news', h1: 'Risk-on into the Fed' },
  { path: '/screener', h1: 'Asia screener' },
  { path: '/account', h1: 'Paper account' },
  { path: '/forecasts', h1: 'Your forecasts.' },
  { path: '/forecasts?tab=markets', h1: 'What the crowd is pricing.' },
  { path: '/forecasts/new', h1: 'Point it at the future.' },
  { path: '/forecasts/new?marketId=123', h1: 'Review the question.' },
  { path: '/automations?tab=history&alert=unavailable', h1: 'Automations' },
  { path: `/forecasts/${SEED.forecast}`, h1: 'Will ETH outperform BTC over Q4 2026?' },
  { path: '/strategies', h1: 'Turn ideas into strategies.' },
  { path: '/strategies?tab=top', h1: 'Top strategies, measured honestly.' },
  { path: '/strategies?tab=arena', h1: 'The live paper desk.' },
  { path: '/strategies?tab=arena&arena=historical', h1: 'Let the agents trade the past.' },
  { path: `/strategies/${LEGACY_STRATEGY}`, h1: 'ETH RSI mean reversion' },
  { path: `/strategies/arena/${SEED.run}`, h1: /^Daily · Aug 3 → Oct 1, 2026$/ },
  { path: '/portfolios', h1: 'Describe a portfolio in one sentence.' },
  { path: `/portfolios/${SEED.portfolio}`, h1: 'Layer-1 core' },
  { path: '/no-such-page', h1: 'Page not found' },
]

for (const { path, h1 } of PAGES) {
  test(`${path} renders cleanly`, { tag: '@mobile' }, async ({ page }) => {
    await page.goto(path)
    await expect(page.getByRole('heading', { level: 1 })).toHaveText(h1)
    await page.waitForLoadState('networkidle') // the page's own data (tables, charts) is in before the layout checks
    await expectAccessible(page)
    await expectNoHorizontalScroll(page)
  })
}

test('the theme choice survives a reload', async ({ page }) => {
  await page.emulateMedia({ colorScheme: 'light' })
  await page.goto('/account')
  const html = page.locator('html')
  await expect(html).toHaveAttribute('data-theme', 'light')
  await page.getByRole('button', { name: 'Switch to dark theme' }).click()
  await expect(html).toHaveAttribute('data-theme', 'dark')
  await page.reload()
  await expect(html).toHaveAttribute('data-theme', 'dark')
  await expect(page.getByRole('button', { name: 'Switch to light theme' })).toBeVisible()
})

// Dictation rides on the browser's own speech recognition: the mic shows only where it exists.
test('the composer offers dictation only where the browser can transcribe', async ({ page }) => {
  await page.addInitScript(() => {
    const w = window as unknown as Record<string, unknown>
    delete w.SpeechRecognition
    delete w.webkitSpeechRecognition
  })
  await page.goto('/')
  await expect(page.getByRole('textbox', { name: 'Message the agent' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Dictate' })).toHaveCount(0)
})

test('dictation fills the composer and waits for Send', async ({ page }) => {
  await page.addInitScript(() => {
    class FakeRecognition {
      onresult: ((e: unknown) => void) | null = null
      onend: (() => void) | null = null
      start() {
        setTimeout(() => {
          this.onresult?.({ results: [Object.assign([{ transcript: 'buy $500 of SOL' }], { isFinal: true })] })
          this.onend?.()
        }, 50)
      }
      stop() {}
      abort() {}
    }
    const w = window as unknown as Record<string, unknown>
    w.SpeechRecognition = w.webkitSpeechRecognition = FakeRecognition // Chromium ships the unprefixed one too
  })
  await page.goto('/')
  await page.getByRole('button', { name: 'Dictate' }).click()
  await expect(page.getByRole('textbox', { name: 'Message the agent' })).toHaveValue('buy $500 of SOL')
  await expect(page.getByRole('button', { name: 'Dictate' })).toHaveAttribute('aria-pressed', 'false')
  await expect(page).toHaveURL('/') // nothing was sent
})

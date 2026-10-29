import { defineConfig, devices } from '@playwright/test'
import { WEB_PORT, WEB_URL } from './e2e/mock-api'

// End-to-end suite: the production build (`npm run e2e:build`, served by `vite preview`) against the mock API in
// e2e/mock-api.ts, which globalSetup starts on API_PORT. Chromium only; `@mobile` tests also run at 375 px.
export default defineConfig({
  testDir: 'e2e',
  testMatch: '*.spec.ts', // *.test.ts in e2e/ are Vitest unit tests
  globalSetup: './e2e/mock-api.ts',
  fullyParallel: true, // safe: every test signs in to its own mock session
  forbidOnly: !!process.env.CI,
  retries: 0, // a flaky test should fail, not pass on a second try
  workers: process.env.CI ? 2 : undefined,
  // Two workers share one mock API on a 2-vCPU runner, so a first paint after goto can pass 5 s there.
  expect: { timeout: process.env.CI ? 10_000 : 5_000 },
  reporter: process.env.CI ? [['github'], ['list'], ['html', { open: 'never' }]] : [['list'], ['html', { open: 'never' }]],
  use: {
    baseURL: WEB_URL,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  webServer: {
    command: `npx vite preview --outDir dist-e2e --port ${WEB_PORT} --strictPort`,
    url: WEB_URL,
    reuseExistingServer: false, // a stray preview of the real build would point at the real API
  },
  projects: [
    { name: 'desktop', use: { ...devices['Desktop Chrome'], viewport: { width: 1440, height: 900 } } },
    {
      name: 'mobile',
      grep: /@mobile/,
      use: { ...devices['Desktop Chrome'], viewport: { width: 375, height: 812 }, deviceScaleFactor: 2, isMobile: true, hasTouch: true },
    },
  ],
})

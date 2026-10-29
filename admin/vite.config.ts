/// <reference types="vitest/config" />
import dns from 'node:dns'
import { defineConfig, loadEnv, type Plugin } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

// `localhost` → 127.0.0.1, so the dev server listens where ChatGPT sign-in returns (it must be 127.0.0.1, not ::1).
dns.setDefaultResultOrder('ipv4first')

/**
 * The console's dev origin stays http://localhost:<port>: the API's default ALLOWED_ORIGINS/ADMIN_ORIGINS name it, and
 * its SameSite=Lax session cookie, set by the API on localhost, never reaches a page on 127.0.0.1 (another site). Only
 * ChatGPT sign-in's /auth/callback has to arrive at 127.0.0.1; every other request there moves to localhost.
 */
// ponytail: Vite still prints the 127.0.0.1 URL it listens on; the redirect makes it work rather than patch the banner.
const localhostOrigin = (): Plugin => ({
  name: 'localhost-origin',
  configureServer(server) {
    server.middlewares.use((req, res, next) => {
      const host = req.headers.host ?? ''
      if (!host.startsWith('127.0.0.1:') || req.url?.startsWith('/auth')) return next()
      res.writeHead(302, { Location: `http://${host.replace('127.0.0.1', 'localhost')}${req.url}` }).end()
    })
  },
})

export default defineConfig(({ mode }) => {
  // The API the client calls (src/lib/api.ts reads the same variable, with the same default).
  const api = loadEnv(mode, process.cwd(), 'VITE_').VITE_API_URL?.replace(/\/+$/, '') || 'http://localhost:8080'
  return {
    plugins: [react(), tailwindcss(), localhostOrigin()],
    server: {
      port: 5174,
      strictPort: true,
      // ChatGPT sign-in returns to http://127.0.0.1:5174/auth/callback; the API serves it.
      proxy: { '/auth': api },
    },
    test: {
      environment: 'jsdom',
      setupFiles: ['src/test/setup.ts'],
      include: ['src/**/*.test.{ts,tsx}', 'e2e/**/*.test.ts'], // e2e/*.spec.ts are Playwright's
      // West of UTC on purpose: UTC day labels ("Oct 2") must not shift a day back in a local time zone.
      env: { TZ: 'America/Los_Angeles' },
      testTimeout: 15_000, // above Testing Library's 5 s async timeout (setup.ts), so a stuck findBy reports its own error
      clearMocks: true,
      restoreMocks: true,
      unstubGlobals: true,
      coverage: {
        provider: 'v8',
        include: ['src/**/*.{ts,tsx}'],
        // main.tsx only mounts the app; types.ts and vite-env.d.ts hold no runtime code.
        exclude: ['src/**/*.test.{ts,tsx}', 'src/test/**', 'src/main.tsx', 'src/vite-env.d.ts', 'src/lib/types.ts'],
        reporter: ['text', 'html'],
        thresholds: {
          lines: 70,
          'src/lib/**': { lines: 80 },
          'src/components/**': { lines: 80 },
        },
      },
    },
  }
})

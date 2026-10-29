/// <reference types="vitest/config" />
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  // The artwork fixture is compiled only into the local mock build. It renders
  // the real AssetIcon at review sizes without adding an application route.
  build: process.env.VITE_API_URL === 'http://localhost:4010' ? {
    rollupOptions: { input: { app: 'index.html', artwork: 'e2e/artwork-gallery.html' } },
  } : undefined,
  server: { port: 5173, strictPort: true },
  test: {
    environment: 'jsdom',
    setupFiles: ['src/test/setup.ts'],
    include: ['src/**/*.test.{ts,tsx}', 'e2e/**/*.test.ts'], // e2e/*.spec.ts are Playwright's
    // West of UTC on purpose: date-only strings must not shift a day back, and timed labels stay deterministic.
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
})

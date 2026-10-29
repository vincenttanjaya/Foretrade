// Render helpers and fakes shared by the component and page tests.
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactElement } from 'react'
import { MemoryRouter, parsePath, Route, Routes, useLocation } from 'react-router'
import { vi } from 'vitest'
import { ToastProvider } from '../components/toast'
import { api, ApiError } from '../lib/api'
import { AssetsProvider } from '../lib/assets'
import { AuthProvider, useAuth } from '../lib/auth'
import { InstrumentsProvider } from '../lib/instruments'
import { PortfolioProvider } from '../lib/portfolio'
import { ThemeProvider } from '../lib/theme'
import * as fx from './fixtures'

export { FakeWebSocket } from './socket'

/**
 * Signed-in session: /api/me, /api/portfolio and /api/market/assets answer with the fixtures. The instrument catalogue
 * answers 404 like an API without it (SYMBOLS only) unless `listed`: then the named rows plus the listed fixtures, and
 * the assets poll answers `scope=listed` with the listed assets.
 */
export function stubSession(over: { portfolio?: typeof fx.portfolio; listed?: boolean } = {}) {
  const instruments = vi.spyOn(api, 'instruments')
  if (over.listed) instruments.mockResolvedValue({ instruments: fx.instruments, etag: null })
  else instruments.mockRejectedValue(new ApiError(404, 'not_found', 'Not found'))
  return {
    me: vi.spyOn(api, 'me').mockResolvedValue({ user: fx.user, credits: fx.credits }),
    portfolio: vi.spyOn(api, 'portfolio').mockResolvedValue({ portfolio: over.portfolio ?? fx.portfolio }),
    assets: vi.spyOn(api, 'assets').mockImplementation(async (scope) => ({ assets: scope === 'listed' ? fx.listedAssets : fx.assets })),
    instruments,
    notificationChannels: vi.spyOn(api, 'notificationChannels').mockResolvedValue({ channels: fx.notificationChannels() }),
  }
}

/** Where the router is, plus the auth state pages update (credits after AI calls). */
function Probe() {
  const { pathname, search } = useLocation()
  const { credits } = useAuth()
  return (
    <>
      <span hidden data-testid="location">{pathname + search}</span>
      <span hidden data-testid="credits-used">{credits?.used ?? ''}</span>
    </>
  )
}

export const currentPath = () => screen.getByTestId('location').textContent
export const creditsUsed = () => screen.getByTestId('credits-used').textContent

type Options = {
  route?: string // initial URL
  path?: string // route pattern the element is mounted on
  state?: unknown // router location state
  routes?: Record<string, ReactElement> // extra routes, e.g. the page a form navigates to
}

/** Renders `ui` inside the app's providers and a memory router. Returns a user-event instance too. */
export function renderPage(ui: ReactElement, { route = '/', path = '*', state, routes = {} }: Options = {}) {
  const user = userEvent.setup()
  const result = render(
    <MemoryRouter initialEntries={[{ ...parsePath(route), state }]}>
      <ThemeProvider>
        <ToastProvider>
          <AuthProvider>
            <PortfolioProvider>
              <InstrumentsProvider>
                <AssetsProvider>
                  <Routes>
                    <Route path={path} element={ui} />
                    {Object.entries(routes).map(([p, el]) => (
                      <Route key={p} path={p} element={el} />
                    ))}
                  </Routes>
                  <Probe />
                </AssetsProvider>
              </InstrumentsProvider>
            </PortfolioProvider>
          </AuthProvider>
        </ToastProvider>
      </ThemeProvider>
    </MemoryRouter>,
  )
  return { user, ...result }
}

/** One SSE frame as the API writes it. */
export const sseFrame = (event: string, data: unknown) => `event: ${event}\ndata: ${JSON.stringify(data)}\n\n`

/** A streaming response the test feeds chunk by chunk; aborting `signal` errors the body like a real fetch. */
export function sseStream(signal?: AbortSignal | null) {
  const enc = new TextEncoder()
  let ctrl!: ReadableStreamDefaultController<Uint8Array>
  const body = new ReadableStream<Uint8Array>({ start: (c) => void (ctrl = c) })
  signal?.addEventListener('abort', () => ctrl.error(new DOMException('The operation was aborted.', 'AbortError')))
  return {
    response: new Response(body, { status: 200, headers: { 'Content-Type': 'text/event-stream' } }),
    push: (text: string | Uint8Array) => ctrl.enqueue(typeof text === 'string' ? enc.encode(text) : text),
    close: () => ctrl.close(),
    fail: (err: unknown) => ctrl.error(err), // a dropped connection
  }
}

/** JSON response for fetch mocks. */
export const json = (body: unknown, status = 200) =>
  new Response(status === 204 ? null : JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

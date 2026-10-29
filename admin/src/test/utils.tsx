// Render helpers shared by the component and page tests.
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactElement } from 'react'
import { MemoryRouter, parsePath, Route, Routes, useLocation } from 'react-router'
import { vi } from 'vitest'
import { ToastProvider } from '../components/toast'
import { api } from '../lib/api'
import { AuthProvider } from '../lib/auth'
import { ThemeProvider } from '../lib/theme'
import type { User } from '../lib/types'
import * as fx from './fixtures'

/** Signed-in session: /api/me answers with `user` (the admin by default). */
export const stubSession = (user: User = fx.admin) => vi.spyOn(api, 'me').mockResolvedValue({ user, credits: fx.credits })

function Probe() {
  const { pathname, search } = useLocation()
  return (
    <span hidden data-testid="location">
      {pathname + search}
    </span>
  )
}

export const currentPath = () => screen.getByTestId('location').textContent

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
            <Routes>
              <Route path={path} element={ui} />
              {Object.entries(routes).map(([p, el]) => (
                <Route key={p} path={p} element={el} />
              ))}
            </Routes>
            <Probe />
          </AuthProvider>
        </ToastProvider>
      </ThemeProvider>
    </MemoryRouter>,
  )
  return { user, ...result }
}

/** JSON response for fetch mocks. */
export const json = (body: unknown, status = 200) =>
  new Response(status === 204 ? null : JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

// The auth gate (FRONTEND-LAYOUT.md → Admin console): who gets the console, the sign-in page, the "Access restricted"
// wall or the "Can't reach the API" screen.
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, useLocation } from 'react-router'
import { describe, expect, it, vi } from 'vitest'
import { App } from './App'
import { ToastProvider } from './components/toast'
import { ApiError, api } from './lib/api'
import { AuthProvider } from './lib/auth'
import { ThemeProvider } from './lib/theme'
import * as fx from './test/fixtures'
import { json, stubSession } from './test/utils'

function Where() {
  const { pathname } = useLocation()
  return <span data-testid="where" hidden>{pathname}</span>
}

function renderApp(route = '/') {
  const user = userEvent.setup()
  render(
    <MemoryRouter initialEntries={[route]}>
      <ThemeProvider>
        <ToastProvider>
          <AuthProvider>
            <App />
            <Where />
          </AuthProvider>
        </ToastProvider>
      </ThemeProvider>
    </MemoryRouter>,
  )
  return user
}

const where = () => screen.getByTestId('where').textContent
const signedOut = () => vi.spyOn(api, 'me').mockRejectedValue(new ApiError(401, 'unauthorized', 'Sign in to continue.'))
const stubLists = () => {
  vi.spyOn(api, 'overview').mockResolvedValue({ overview: fx.overview() })
  vi.spyOn(api, 'users').mockResolvedValue({ users: [fx.adminUser()], total: 1, page: 1, limit: 25, defaultCreditLimit: 500 })
}

async function signIn(user: ReturnType<typeof userEvent.setup>, password = 'correct horse battery') {
  await user.type(await screen.findByLabelText('Email'), 'someone@example.com')
  await user.type(screen.getByLabelText('Password'), password)
  await user.click(screen.getByRole('button', { name: 'Sign in' }))
}

describe('auth gate', () => {
  it('shows a loader while the session check runs', () => {
    vi.spyOn(api, 'me').mockReturnValue(new Promise(() => {}))
    renderApp()
    expect(screen.getByRole('status')).toHaveTextContent('Loading')
  })

  it('sends a signed-out visitor to /login, then back to the page they asked for', async () => {
    signedOut()
    stubLists()
    vi.spyOn(api, 'login').mockResolvedValue({ user: fx.admin })
    const user = renderApp('/users?role=admin')
    expect(await screen.findByRole('heading', { name: 'Sign in' })).toBeInTheDocument()
    expect(where()).toBe('/login')
    await signIn(user)
    expect(await screen.findByRole('heading', { level: 1, name: 'Users.' })).toBeInTheDocument()
    expect(where()).toBe('/users')
  })

  it('opens the console for an admin session, labelled ADMIN', async () => {
    stubSession()
    stubLists()
    renderApp()
    expect(await screen.findByRole('heading', { level: 1, name: 'Platform overview.' })).toBeInTheDocument()
    const banner = screen.getAllByRole('banner')[0] // the top bar; PageHeader is a <header> too
    expect(within(banner).getAllByText('ADMIN')[0]).toBeInTheDocument() // the badge (the account menu repeats it)
    expect(within(banner).getAllByText('admin@example.com')).toHaveLength(2) // top bar + account menu
    expect(within(banner).getByRole('link', { name: /Open Foretrade/ })).toHaveAttribute('target', '_blank')
    expect(screen.getAllByRole('navigation', { name: 'Primary' })[0]).toHaveTextContent('OverviewUsersAudit')
    expect(document.title).toBe('Overview · Foretrade Admin')
  })

  it('signs a non-admin session out and shows the Access restricted wall', async () => {
    stubSession(fx.member)
    const logout = vi.spyOn(api, 'logout').mockResolvedValue()
    const user = renderApp('/users')
    expect(await screen.findByRole('heading', { level: 1, name: 'Access restricted' })).toBeInTheDocument()
    expect(screen.getByText(/This console is for Foretrade administrators\. Your session has been signed out\./)).toBeInTheDocument()
    expect(logout).toHaveBeenCalledTimes(1)
    expect(screen.queryByRole('navigation')).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Back to sign in' }))
    expect(await screen.findByRole('heading', { name: 'Sign in' })).toBeInTheDocument()
    expect(where()).toBe('/login')
  })

  it('still shows the wall when the sign-out request fails', async () => {
    stubSession(fx.member)
    vi.spyOn(api, 'logout').mockRejectedValue(new ApiError(0, 'network', 'offline'))
    renderApp()
    expect(await screen.findByRole('heading', { name: 'Access restricted' })).toBeInTheDocument()
  })

  it("shows Can't reach the API with a Retry when /api/me fails without a 401", async () => {
    const me = vi.spyOn(api, 'me').mockRejectedValueOnce(new ApiError(0, 'network', 'offline'))
    stubLists()
    const user = renderApp()
    expect(await screen.findByRole('heading', { name: "Can't reach the API" })).toBeInTheDocument()
    me.mockResolvedValue({ user: fx.admin, credits: fx.credits })
    await user.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByRole('heading', { level: 1, name: 'Platform overview.' })).toBeInTheDocument()
  })

  it('walls off an admin demoted mid-session (a 403 from an admin route, then /api/me says user)', async () => {
    stubSession(fx.member).mockResolvedValueOnce({ user: fx.admin, credits: fx.credits })
    const logout = vi.spyOn(api, 'logout').mockResolvedValue()
    respond(403, 'forbidden', 'Admins only.') // the real client: GET /api/admin/overview → 403
    renderApp()
    expect(await screen.findByRole('heading', { name: 'Access restricted' })).toBeInTheDocument()
    expect(logout).toHaveBeenCalled()
  })

  it('keeps an admin signed in on an origin 403 (ADMIN_ORIGINS) and shows the API message', async () => {
    const me = stubSession()
    const logout = vi.spyOn(api, 'logout').mockResolvedValue()
    respond(403, 'forbidden', 'Cross-site request rejected.')
    renderApp()
    expect(await screen.findByText('Cross-site request rejected.')).toBeInTheDocument()
    await waitFor(() => expect(me).toHaveBeenCalledTimes(2)) // the re-check
    expect(screen.queryByRole('heading', { name: 'Access restricted' })).not.toBeInTheDocument()
    expect(logout).not.toHaveBeenCalled()
  })

  it('goes back to sign-in when the session expires mid-session (401)', async () => {
    stubSession()
    respond(401, 'unauthorized', 'Sign in to continue.')
    renderApp('/')
    expect(await screen.findByRole('heading', { name: 'Sign in' })).toBeInTheDocument()
  })

  it('logs out from the account menu, which notes the shared session', async () => {
    stubSession()
    stubLists()
    const logout = vi.spyOn(api, 'logout').mockResolvedValue()
    const user = renderApp()
    await screen.findByRole('heading', { level: 1, name: 'Platform overview.' })
    expect(screen.getByText(/Also signs this browser out of Foretrade/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Log out', hidden: true })) // in the closed popover
    expect(await screen.findByRole('heading', { name: 'Sign in' })).toBeInTheDocument()
    expect(logout).toHaveBeenCalledTimes(1)
  })

  it('opens Settings from the rail', async () => {
    stubSession()
    stubLists()
    vi.spyOn(api, 'settings').mockResolvedValue(fx.settings())
    const user = renderApp()
    await screen.findByRole('heading', { level: 1, name: 'Platform overview.' })
    await user.click(within(screen.getAllByRole('navigation', { name: 'Primary' })[0]).getByRole('link', { name: 'Settings' }))
    expect(await screen.findByRole('heading', { level: 1, name: 'Settings.' })).toBeInTheDocument()
    expect(where()).toBe('/settings')
    expect(screen.getAllByRole('link', { name: 'Settings' })[0]).toHaveAttribute('aria-current', 'page')
  })

  it('renders a not-found page inside the shell and toggles the theme', async () => {
    stubSession()
    const user = renderApp('/nope')
    expect(await screen.findByRole('heading', { name: 'Page not found' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Back to the overview' })).toHaveAttribute('href', '/')
    const toggle = screen.getAllByRole('button', { name: 'Switch to dark theme' })[0]
    await user.click(toggle)
    expect(document.documentElement.dataset.theme).toBe('dark')
    expect(localStorage.getItem('theme')).toBe('dark')
    await user.click(screen.getByRole('button', { name: 'Light theme', hidden: true })) // the menu's copy for small screens
    expect(document.documentElement.dataset.theme).toBe('light')
  })
})

/** Every fetch (here: only the page's admin request, /api/me is spied) answers with this error. */
function respond(status: number, code: string, message: string) {
  vi.mocked(fetch).mockImplementation(async () => json({ error: { code, message } }, status))
}

describe('LoginPage', () => {
  it('rejects a non-admin account inline and signs it out at once', async () => {
    signedOut()
    vi.spyOn(api, 'login').mockResolvedValue({ user: fx.member })
    const logout = vi.spyOn(api, 'logout').mockResolvedValue()
    const user = renderApp('/login')
    await signIn(user)
    expect(await screen.findByRole('alert')).toHaveTextContent('Access restricted — this console is for administrators only.')
    expect(logout).toHaveBeenCalledTimes(1)
    expect(where()).toBe('/login')
    expect(screen.getByLabelText('Password')).toHaveValue('') // cleared after a failed attempt
    expect(screen.getByLabelText('Email')).toHaveAttribute('aria-invalid', 'true')
  })

  it.each([
    [new ApiError(401, 'invalid_credentials', 'Wrong email or password.'), 'Wrong email or password.'],
    [new ApiError(429, 'rate_limited', 'Too many sign-in attempts. Try again later.'), 'Too many sign-in attempts. Try again later.'],
    [new ApiError(0, 'network', "Can't reach the API."), "Can't reach the API."],
    [
      new ApiError(403, 'forbidden', 'Cross-site request rejected.'),
      "Sign-in isn't allowed from this address. Add it to ALLOWED_ORIGINS and ADMIN_ORIGINS in .env.",
    ],
    [new ApiError(500, 'internal', 'boom'), 'Something went wrong while signing in. Please try again.'],
  ])('shows %s as "%s"', async (err, text) => {
    signedOut()
    vi.spyOn(api, 'login').mockRejectedValue(err)
    const user = renderApp('/login')
    await signIn(user)
    expect(await screen.findByRole('alert')).toHaveTextContent(text)
  })

  it('shows the operator hero and lets the password be revealed', async () => {
    signedOut()
    const user = renderApp('/login')
    expect(await screen.findByRole('heading', { level: 1 })).toHaveTextContent('Operator console.')
    const pw = screen.getByLabelText('Password')
    expect(pw).toHaveAttribute('type', 'password')
    await user.click(screen.getByRole('button', { name: 'Show password' }))
    expect(pw).toHaveAttribute('type', 'text')
  })

  it('redirects an already signed-in admin away from /login', async () => {
    stubSession()
    stubLists()
    renderApp('/login')
    await waitFor(() => expect(where()).toBe('/'))
  })
})

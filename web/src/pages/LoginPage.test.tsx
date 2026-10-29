import { screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { api, ApiError } from '../lib/api'
import * as fx from '../test/fixtures'
import { currentPath, renderPage } from '../test/utils'
import { LoginPage } from './LoginPage'

const signedOut = () => vi.spyOn(api, 'me').mockRejectedValue(new ApiError(401, 'unauthorized', 'Sign in'))
const open = (state?: unknown) => renderPage(<LoginPage />, { route: '/login', path: '/login', state, routes: { '*': <p>app</p> } })

async function fill(user: ReturnType<typeof open>['user'], email = 'demo@example.com', password = 'localdemo') {
  await user.type(await screen.findByLabelText('Email'), email)
  await user.type(screen.getByLabelText('Password'), password)
}

describe('LoginPage', () => {
  it('shows a loader until the session check answers', () => {
    vi.spyOn(api, 'me').mockReturnValue(new Promise(() => {}))
    open()
    expect(screen.getByRole('status')).toHaveTextContent('Loading')
  })

  it('renders the editorial hero and the sign-in form', async () => {
    signedOut()
    open()
    expect(await screen.findByRole('heading', { name: 'Sign in' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent('Trade with an AI desk.')
    expect(screen.getAllByRole('listitem').map((li) => li.querySelector('div > div')?.textContent)).toEqual([
      'Agent',
      'Forecast desk',
      'Strategies & portfolios',
    ])
    expect(screen.getByLabelText('Email')).toHaveAttribute('type', 'email')
    expect(screen.getByText(/Paper trading only\./)).toBeInTheDocument()
    // Quote-feed stocks fill at Yahoo's delayed price: the note never calls every price live (gap round 2, G17).
    const note = screen.getByText(/Paper trading only\./).closest('p')
    expect(note).toHaveTextContent('Orders fill against public market prices (for quote-feed stocks, each price says whether it is real-time or delayed) in a simulated ledger.')
    expect(note).not.toHaveTextContent(/\blive\b/)
  })

  it('relies on native validation: nothing is sent for empty or malformed fields', async () => {
    signedOut()
    const login = vi.spyOn(api, 'login')
    const { user } = open()
    await user.click(await screen.findByRole('button', { name: 'Sign in' }))
    expect(login).not.toHaveBeenCalled()
    await fill(user, 'not-an-email')
    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    expect(login).not.toHaveBeenCalled()
    expect(screen.getByLabelText('Email')).toBeRequired()
    expect(screen.getByLabelText('Password')).toBeRequired()
  })

  it.each([
    [new ApiError(401, 'invalid_credentials', 'Wrong'), "That email and password don't match. Check the demo credentials and try again."],
    [new ApiError(429, 'rate_limited', 'Slow down'), 'Too many sign-in attempts. Wait a few minutes, then try again.'],
    [new ApiError(0, 'network', 'Cannot reach the server. Check your connection and try again.'), 'Cannot reach the server. Check your connection and try again.'],
    [new ApiError(500, 'internal', 'boom'), 'Something went wrong while signing in. Please try again.'],
    [new Error('weird'), 'Something went wrong while signing in. Please try again.'],
  ])('shows an inline error for %s', async (err, copy) => {
    signedOut()
    vi.spyOn(api, 'login').mockRejectedValue(err)
    const { user } = open()
    await fill(user)
    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(copy)
    expect(screen.getByLabelText('Email')).toHaveAttribute('aria-invalid', 'true')
    expect(screen.getByLabelText('Password')).toHaveAttribute('aria-describedby', 'login-error')
    expect(screen.getByRole('button', { name: 'Sign in' })).toBeEnabled() // can try again
  })

  it('signs in and goes back to the page that sent the visitor here', async () => {
    const me = signedOut()
    let finish!: (v: { user: typeof fx.user }) => void
    const login = vi.spyOn(api, 'login').mockReturnValue(new Promise((r) => (finish = r)))
    const { user } = open({ from: '/forecasts?status=running' })
    await fill(user)
    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    expect(login).toHaveBeenCalledWith('demo@example.com', 'localdemo')
    const busy = screen.getByRole('button', { name: 'Signing in…' })
    expect(busy).toBeDisabled()
    expect(busy).toHaveAttribute('aria-busy', 'true')

    me.mockResolvedValue({ user: fx.user, credits: fx.credits })
    finish({ user: fx.user })
    await waitFor(() => expect(currentPath()).toBe('/forecasts?status=running'))
    expect(screen.getByText('app')).toBeInTheDocument()
  })

  it('sends a signed-in visitor straight to the app', async () => {
    vi.spyOn(api, 'me').mockResolvedValue({ user: fx.user, credits: fx.credits })
    open()
    await waitFor(() => expect(currentPath()).toBe('/'))
  })

  it('shows and hides the password', async () => {
    signedOut()
    const { user } = open()
    const pw = await screen.findByLabelText('Password')
    expect(pw).toHaveAttribute('type', 'password')
    await user.click(screen.getByRole('button', { name: 'Show password' }))
    expect(pw).toHaveAttribute('type', 'text')
    expect(screen.getByRole('button', { name: 'Hide password' })).toHaveAttribute('aria-pressed', 'true')
    await user.click(screen.getByRole('button', { name: 'Hide password' }))
    expect(pw).toHaveAttribute('type', 'password')
  })
})

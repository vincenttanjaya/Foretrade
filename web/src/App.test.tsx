import { act, screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { App } from './App'
import { api, ApiError } from './lib/api'
import { useAuth } from './lib/auth'
import * as fx from './test/fixtures'
import { currentPath, renderPage, stubSession } from './test/utils'

// Routes are lazy chunks: the first import in a cold worker transforms the whole page.
const lazyChunk = { timeout: 10_000 }

describe('App routes', () => {
  it('sends a signed-out visitor to /login and remembers where they were going', async () => {
    vi.spyOn(api, 'me').mockRejectedValue(new ApiError(401, 'unauthorized', ''))
    const login = vi.spyOn(api, 'login').mockResolvedValue({ user: fx.user })
    const { user } = renderPage(<App />, { route: '/news?f=SOL' })
    await waitFor(() => expect(currentPath()).toBe('/login'))

    stubSession()
    vi.spyOn(api, 'brief').mockResolvedValue({ brief: null })
    vi.spyOn(api, 'news').mockResolvedValue({ news: [] })
    await user.type(screen.getByLabelText('Email'), 'demo@example.com')
    await user.type(screen.getByLabelText('Password'), 'pw')
    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    expect(login).toHaveBeenCalled()
    // Navigations are transitions: the location commits together with the lazy page.
    expect(await screen.findByRole('heading', { name: 'The wire is quiet.' }, lazyChunk)).toBeInTheDocument()
    expect(currentPath()).toBe('/news?f=SOL')
  })

  it('redirects /trade to BTC', async () => {
    stubSession()
    vi.spyOn(api, 'candles').mockResolvedValue({ candles: [] })
    vi.spyOn(api, 'trades').mockResolvedValue({ trades: [] })
    renderPage(<App />, { route: '/trade' })
    expect(await screen.findByRole('heading', { name: 'Bitcoin' }, lazyChunk)).toBeInTheDocument()
    expect(currentPath()).toBe('/trade/BTC')
  })

  it('routes /strategies/arena/:id to the arena run, not a strategy', async () => {
    stubSession()
    const run = vi.spyOn(api, 'arenaRun').mockResolvedValue({ run: fx.arenaRun({ status: 'completed', stepsDone: 0, name: 'Arena run' }) })
    vi.spyOn(api, 'arenaSteps').mockResolvedValue({ steps: [] })
    const strategy = vi.spyOn(api, 'strategy')
    renderPage(<App />, { route: '/strategies/arena/r1' })
    expect(await screen.findByRole('heading', { level: 1, name: 'Arena run' }, lazyChunk)).toBeInTheDocument()
    expect(run).toHaveBeenCalledWith('r1')
    expect(strategy).not.toHaveBeenCalled()
  })

  it('routes /screener to the Asia screener', async () => {
    stubSession()
    const screener = vi.spyOn(api, 'screener').mockResolvedValue(fx.screenerPage())
    renderPage(<App />, { route: '/screener?market=JP' })
    expect(await screen.findByRole('heading', { level: 1, name: 'Asia screener' }, lazyChunk)).toBeInTheDocument()
    // The heading paints before the page's first read starts; under load the read can land a moment later.
    await waitFor(() => expect(screener).toHaveBeenCalledWith(expect.objectContaining({ market: 'JP', filter: 'all', sort: 'pctVsSma' }), expect.any(AbortSignal)))
  })

  it('shows a not-found page inside the shell for unknown routes', async () => {
    stubSession()
    const { user } = renderPage(<App />, { route: '/nope' })
    expect(await screen.findByRole('heading', { name: "This page doesn't exist." }, lazyChunk)).toBeInTheDocument()
    expect(screen.getByRole('heading', { level: 1, name: 'Page not found' })).toBeInTheDocument()
    vi.spyOn(api, 'chats').mockResolvedValue({ chats: [] })
    vi.spyOn(api, 'brief').mockResolvedValue({ brief: null })
    await user.click(screen.getByRole('link', { name: 'Back to the agent' }))
    expect(await screen.findByRole('heading', { name: 'Which market is on your mind?' }, lazyChunk)).toBeInTheDocument()
    expect(currentPath()).toBe('/')
  })

  it('starts the portfolio over when the session switches to another user', async () => {
    const me = vi.spyOn(api, 'me').mockResolvedValue({ user: fx.user, credits: fx.credits })
    const get = vi.spyOn(api, 'portfolio').mockResolvedValue({ portfolio: { ...fx.portfolio, revision: 50 } })
    vi.spyOn(api, 'assets').mockResolvedValue({ assets: fx.assets })
    // Another tab signed in as someone else; this tab's next /api/me sees the new user.
    function Recheck() {
      const { refresh } = useAuth()
      return <button onClick={() => void refresh()}>recheck</button>
    }
    const { user } = renderPage(
      <>
        <App />
        <Recheck />
      </>,
      { route: '/nope' },
    )
    const equity = await screen.findByRole('link', { name: /Paper equity/ }, lazyChunk)
    await waitFor(() => expect(equity).toHaveTextContent('$100,194.00'))

    me.mockResolvedValue({ user: { ...fx.user, id: 'u2', email: 'other@example.com' }, credits: fx.credits })
    get.mockResolvedValue({ portfolio: { ...fx.emptyPortfolio, revision: 1 } }) // their account, their revisions
    await user.click(screen.getByRole('button', { name: 'recheck' }))
    act(() => void document.dispatchEvent(new Event('visibilitychange'))) // and a poll
    await waitFor(() => expect(screen.getByRole('link', { name: /Paper equity/ })).toHaveTextContent('$100,000.00'))
  })
})

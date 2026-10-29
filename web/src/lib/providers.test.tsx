// AuthProvider, PortfolioProvider, AssetsProvider and ThemeProvider against a mocked API.
import { act, render, renderHook, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import * as fx from '../test/fixtures'
import { json } from '../test/utils'
import { api, ApiError } from './api'
import { AssetsProvider, useAssets } from './assets'
import { AuthProvider, useAuth } from './auth'
import { PortfolioProvider, usePortfolio } from './portfolio'
import { ThemeProvider, useTheme } from './theme'

const wrap = (Provider: (p: { children: ReactNode }) => ReactNode) => ({ wrapper: Provider })

describe('AuthProvider', () => {
  it('loads the user and credits, and is loading until /api/me answers', async () => {
    vi.spyOn(api, 'me').mockResolvedValue({ user: fx.user, credits: fx.credits })
    const { result } = renderHook(() => useAuth(), wrap(AuthProvider))
    expect(result.current.loading).toBe(true)
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.user).toEqual(fx.user)
    expect(result.current.credits).toEqual(fx.credits)
  })

  it('is signed out when /api/me fails', async () => {
    vi.spyOn(api, 'me').mockRejectedValue(new ApiError(401, 'unauthorized', 'Sign in'))
    const { result } = renderHook(() => useAuth(), wrap(AuthProvider))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.user).toBeNull()
    expect(result.current.credits).toBeNull()
  })

  it('logs in, then re-reads credits; a failed credits read keeps the session', async () => {
    const me = vi.spyOn(api, 'me').mockRejectedValueOnce(new ApiError(401, 'unauthorized', ''))
    const login = vi.spyOn(api, 'login').mockResolvedValue({ user: fx.user })
    const { result } = renderHook(() => useAuth(), wrap(AuthProvider))
    await waitFor(() => expect(result.current.loading).toBe(false))

    me.mockRejectedValueOnce(new ApiError(0, 'network', 'down'))
    await act(() => result.current.login('demo@example.com', 'pw'))
    expect(login).toHaveBeenCalledWith('demo@example.com', 'pw')
    expect(result.current.user).toEqual(fx.user)

    me.mockResolvedValueOnce({ user: fx.user, credits: fx.credits })
    await act(() => result.current.refresh())
    expect(result.current.credits).toEqual(fx.credits)
  })

  it('passes a login failure to the caller', async () => {
    vi.spyOn(api, 'me').mockRejectedValue(new ApiError(401, 'unauthorized', ''))
    vi.spyOn(api, 'login').mockRejectedValue(new ApiError(401, 'invalid_credentials', 'Wrong'))
    const { result } = renderHook(() => useAuth(), wrap(AuthProvider))
    await waitFor(() => expect(result.current.loading).toBe(false))
    await expect(result.current.login('a@b.c', 'x')).rejects.toMatchObject({ code: 'invalid_credentials' })
    expect(result.current.user).toBeNull()
  })

  it('logs out locally even when the API is unreachable', async () => {
    vi.spyOn(api, 'me').mockResolvedValue({ user: fx.user, credits: fx.credits })
    const logout = vi.spyOn(api, 'logout').mockRejectedValue(new ApiError(0, 'network', 'down'))
    const { result } = renderHook(() => useAuth(), wrap(AuthProvider))
    await waitFor(() => expect(result.current.user).not.toBeNull())
    await act(() => result.current.logout())
    expect(logout).toHaveBeenCalled()
    expect(result.current.user).toBeNull()
    expect(result.current.credits).toBeNull()
  })

  it('signs out when any request gets a 401', async () => {
    vi.spyOn(api, 'me').mockResolvedValue({ user: fx.user, credits: fx.credits })
    const { result } = renderHook(() => useAuth(), wrap(AuthProvider))
    await waitFor(() => expect(result.current.user).not.toBeNull())
    vi.mocked(fetch).mockResolvedValue(json({ error: { code: 'unauthorized', message: 'Session expired' } }, 401))
    await act(() => api.portfolio().catch(() => {}))
    expect(result.current.user).toBeNull()
  })

  it('updates credits from an AI response', async () => {
    vi.spyOn(api, 'me').mockResolvedValue({ user: fx.user, credits: fx.credits })
    const { result } = renderHook(() => useAuth(), wrap(AuthProvider))
    await waitFor(() => expect(result.current.credits).not.toBeNull())
    act(() => result.current.setCredits({ ...fx.credits, used: 20 }))
    expect(result.current.credits?.used).toBe(20)
  })
})

describe('PortfolioProvider', () => {
  afterEach(() => vi.useRealTimers())

  it('loads, polls every 30 s and keeps the last value when a poll fails', async () => {
    vi.useFakeTimers()
    const get = vi.spyOn(api, 'portfolio').mockResolvedValue({ portfolio: fx.portfolio })
    const { result } = renderHook(() => usePortfolio(), wrap(PortfolioProvider))
    expect(result.current.portfolio).toBeNull()
    await act(() => vi.advanceTimersByTimeAsync(0))
    expect(result.current.portfolio).toEqual(fx.portfolio)

    get.mockRejectedValueOnce(new ApiError(502, 'upstream', 'down'))
    await act(() => vi.advanceTimersByTimeAsync(30_000))
    expect(get).toHaveBeenCalledTimes(2)
    expect(result.current.portfolio).toEqual(fx.portfolio)

    get.mockResolvedValueOnce({ portfolio: fx.emptyPortfolio })
    await act(() => vi.advanceTimersByTimeAsync(30_000))
    expect(result.current.portfolio).toEqual(fx.emptyPortfolio)
  })

  it('takes the portfolio from an order response and refreshes on demand', async () => {
    const get = vi.spyOn(api, 'portfolio').mockResolvedValue({ portfolio: fx.portfolio })
    const { result } = renderHook(() => usePortfolio(), wrap(PortfolioProvider))
    await waitFor(() => expect(result.current.portfolio).not.toBeNull())
    act(() => result.current.setPortfolio(fx.emptyPortfolio))
    expect(result.current.portfolio).toEqual(fx.emptyPortfolio)
    await act(() => result.current.refresh())
    expect(get).toHaveBeenCalledTimes(2)
    expect(result.current.portfolio).toEqual(fx.portfolio)
  })

  it('keeps the later trade when overlapping order responses arrive out of order', async () => {
    type Res = Awaited<ReturnType<typeof api.placeOrder>>
    const settle: ((res: Res) => void)[] = []
    vi.spyOn(api, 'portfolio').mockResolvedValue({ portfolio: { ...fx.emptyPortfolio, revision: 1 } })
    vi.spyOn(api, 'placeOrder').mockImplementation(() => new Promise<Res>((resolve) => settle.push(resolve)))
    const { result } = renderHook(() => usePortfolio(), wrap(PortfolioProvider))
    await waitFor(() => expect(result.current.portfolio).not.toBeNull())
    // What OrderTicket and the agent's trade card do with an order response.
    const buy = () =>
      api.placeOrder({ symbol: 'BTC', side: 'buy', notional: 1000 }).then((r) => result.current.setPortfolio(r.portfolio))
    const first = buy()
    const second = buy()
    await act(async () => {
      settle[1]({ trade: fx.trade(), portfolio: { ...fx.emptyPortfolio, cash: 97998, revision: 3 } })
      await second
    })
    await act(async () => {
      settle[0]({ trade: fx.trade(), portfolio: { ...fx.emptyPortfolio, cash: 98999, revision: 2 } })
      await first
    })
    expect(result.current.portfolio?.cash).toBe(97998)
  })

  // `undefined` is an API that predates `revision`: request order alone decides there.
  it.each([1, undefined])('keeps a completed order response when an older poll arrives late (revision %s)', async (rev) => {
    const older = { ...fx.emptyPortfolio, revision: rev }
    let resolveOld!: (res: { portfolio: typeof fx.emptyPortfolio }) => void
    const old = new Promise<{ portfolio: typeof fx.emptyPortfolio }>((resolve) => { resolveOld = resolve })
    vi.spyOn(api, 'portfolio').mockResolvedValueOnce({ portfolio: older }).mockImplementationOnce(() => old)
    const { result } = renderHook(() => usePortfolio(), wrap(PortfolioProvider))
    await waitFor(() => expect(result.current.portfolio).not.toBeNull())
    const pending = result.current.refresh()
    act(() => result.current.setPortfolio({ ...fx.emptyPortfolio, cash: 90000, buyingPower: 90000, revision: rev && rev + 1 }))
    await act(async () => {
      resolveOld({ portfolio: older })
      await pending
    })
    expect(result.current.portfolio?.cash).toBe(90000)
    expect(result.current.portfolio?.buyingPower).toBe(90000)
  })

  it.each([1, undefined])('keeps the newer refresh when portfolio responses arrive out of order (revision %s)', async (rev) => {
    const older = { ...fx.emptyPortfolio, revision: rev }
    let resolveOld!: (res: { portfolio: typeof fx.emptyPortfolio }) => void
    const old = new Promise<{ portfolio: typeof fx.emptyPortfolio }>((resolve) => { resolveOld = resolve })
    vi.spyOn(api, 'portfolio')
      .mockResolvedValueOnce({ portfolio: older })
      .mockImplementationOnce(() => old)
      .mockResolvedValueOnce({ portfolio: { ...fx.emptyPortfolio, cash: 80000, revision: rev && rev + 1 } })
    const { result } = renderHook(() => usePortfolio(), wrap(PortfolioProvider))
    await waitFor(() => expect(result.current.portfolio).not.toBeNull())
    const pending = result.current.refresh()
    await act(() => result.current.refresh())
    await act(async () => {
      resolveOld({ portfolio: older })
      await pending
    })
    expect(result.current.portfolio?.cash).toBe(80000)
  })

  // Same account state, but the older read may carry staler marks: request order breaks the tie.
  it('keeps the newer marks when an older poll at the same revision arrives late', async () => {
    const at = (equity: number) => ({ portfolio: { ...fx.emptyPortfolio, equity, revision: 7 } })
    let resolveOld!: (res: ReturnType<typeof at>) => void
    const old = new Promise<ReturnType<typeof at>>((resolve) => { resolveOld = resolve })
    vi.spyOn(api, 'portfolio')
      .mockResolvedValueOnce(at(100000))
      .mockImplementationOnce(() => old)
      .mockResolvedValueOnce(at(101000))
    const { result } = renderHook(() => usePortfolio(), wrap(PortfolioProvider))
    await waitFor(() => expect(result.current.portfolio).not.toBeNull())
    const pending = result.current.refresh()
    await act(() => result.current.refresh())
    await act(async () => {
      resolveOld(at(99000))
      await pending
    })
    expect(result.current.portfolio?.equity).toBe(101000)
  })
})

describe('AssetsProvider', () => {
  afterEach(() => vi.useRealTimers())

  it('polls every 10 s, indexes by symbol and keeps prices when a poll fails', async () => {
    vi.useFakeTimers()
    const get = vi.spyOn(api, 'assets').mockResolvedValue({ assets: fx.assets })
    const { result } = renderHook(() => useAssets(), wrap(AssetsProvider))
    expect(result.current.assets).toEqual([])
    await act(() => vi.advanceTimersByTimeAsync(0))
    expect(result.current.bySymbol.BTC?.price).toBe(84213.2)
    expect(result.current.bySymbol.NOPE).toBeUndefined()

    get.mockRejectedValueOnce(new ApiError(0, 'network', 'down'))
    await act(() => vi.advanceTimersByTimeAsync(10_000))
    expect(get).toHaveBeenCalledTimes(2)
    expect(result.current.assets).toHaveLength(fx.assets.length)
  })
})

describe('ThemeProvider', () => {
  function Toggle() {
    const { theme, toggle, setTheme } = useTheme()
    return (
      <>
        <button onClick={toggle}>theme: {theme}</button>
        <button onClick={() => setTheme('light')}>light</button>
      </>
    )
  }

  it('starts from the theme index.html applied and persists a toggle', async () => {
    document.documentElement.dataset.theme = 'dark'
    render(<Toggle />, wrap(ThemeProvider))
    await userEvent.click(screen.getByRole('button', { name: 'theme: dark' }))
    expect(document.documentElement.dataset.theme).toBe('light')
    expect(localStorage.getItem('theme')).toBe('light')
    await userEvent.click(screen.getByRole('button', { name: 'theme: light' }))
    expect(document.documentElement.dataset.theme).toBe('dark')
  })

  it('still switches when storage is blocked', async () => {
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new DOMException('blocked', 'SecurityError')
    })
    render(<Toggle />, wrap(ThemeProvider))
    await userEvent.click(screen.getByRole('button', { name: 'theme: light' }))
    expect(document.documentElement.dataset.theme).toBe('dark')
  })
})

describe('hooks outside their provider', () => {
  it.each([
    ['useAuth', useAuth],
    ['usePortfolio', usePortfolio],
    ['useAssets', useAssets],
    ['useTheme', useTheme],
  ])('%s throws a clear error', (name, hook) => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    expect(() => renderHook(() => hook())).toThrow(`${name} must be used inside`)
  })
})

import { render, screen, waitFor, within } from '@testing-library/react'
import { lazy } from 'react'
import { Route, Routes } from 'react-router'
import { describe, expect, it, vi } from 'vitest'
import { api } from '../../lib/api'
import * as fx from '../../test/fixtures'
import { currentPath, renderPage, stubSession } from '../../test/utils'
import { AppShell, PageHeader } from './AppShell'

function Boom(): never {
  throw new Error('chunk failed')
}
const Never = lazy(() => new Promise<never>(() => {}))

const shell = (
  <Routes>
    <Route element={<AppShell />}>
      <Route path="boom" element={<Boom />} />
      <Route path="slow" element={<Never />} />
      <Route path="*" element={<p>page body</p>} />
    </Route>
  </Routes>
)

const rail = () => screen.getAllByRole('navigation', { name: 'Primary' })[0]
const creditsPill = () => document.querySelector('[aria-describedby="credits-tip"]')!
// jsdom keeps the native popover menu closed (no popover API), so its items count as hidden.
const menu = { hidden: true }

describe('AppShell', () => {
  it('lists every area in the rail and eight in the bottom bar (Signals and Automations are rail-only)', async () => {
    stubSession()
    renderPage(shell, { route: '/news' })
    const navs = screen.getAllByRole('navigation', { name: 'Primary' })
    expect(navs).toHaveLength(2) // rail (md+) and bottom tab bar (< md)
    const links = (nav: HTMLElement) =>
      within(nav)
        .getAllByRole('link')
        .map((a) => [a.textContent, a.getAttribute('href')])
    const tabs = [
      ['Agent', '/'],
      ['Trade', '/trade/BTC'],
      ['Screener', '/screener'],
      ['News', '/news'],
      ['Forecasts', '/forecasts'],
      ['Strategies', '/strategies'],
      ['Portfolios', '/portfolios'],
      ['Account', '/account'],
    ]
    expect(links(navs[0])).toEqual([...tabs.slice(0, 4), ['Signals', '/signals'], ['Automations', '/automations'], ...tabs.slice(4)])
    expect(links(navs[1])).toEqual(tabs) // grid-cols-8: no horizontal scroll on a phone
    expect(navs[1]).toHaveClass('grid-cols-8')
    expect(screen.getByText('page body')).toBeInTheDocument()
    await screen.findByText('$100,194.00')
  })

  it('puts the Asia screener right after Trade, titled and highlighted in the rail and the bottom bar', async () => {
    stubSession()
    renderPage(shell, { route: '/screener?filter=deep' })
    const [railNav, bar] = screen.getAllByRole('navigation', { name: 'Primary' })
    for (const nav of [railNav, bar]) {
      const names = within(nav).getAllByRole('link').map((a) => a.textContent)
      expect(names.indexOf('Screener')).toBe(names.indexOf('Trade') + 1)
      expect(within(nav).getByRole('link', { name: 'Screener' })).toHaveAttribute('aria-current', 'page')
      expect(within(nav).getByRole('link', { name: 'Screener' }).querySelector('svg')).not.toBeNull()
    }
    expect(document.title).toBe('Asia screener · Foretrade')
    expect(screen.getByRole('banner')).toHaveTextContent('Asia screener')
    await screen.findByText('$100,194.00')
  })

  it('titles the signals board and highlights it in the rail', async () => {
    stubSession()
    renderPage(shell, { route: '/signals' })
    expect(within(rail()).getByRole('link', { name: 'Signals' })).toHaveAttribute('aria-current', 'page')
    expect(document.title).toBe('Signals board (momentum screen) · Foretrade')
    await screen.findByText('$100,194.00')
  })

  it('highlights the current area and titles the page after it', async () => {
    stubSession()
    const { user } = renderPage(shell, { route: '/news' })
    expect(within(rail()).getByRole('link', { name: 'News' })).toHaveAttribute('aria-current', 'page')
    expect(within(rail()).getByRole('link', { name: 'Agent' })).not.toHaveAttribute('aria-current')
    expect(document.title).toBe('Market wire · Foretrade')
    expect(screen.getByRole('banner')).toHaveTextContent('Market wire')

    await user.click(within(rail()).getByRole('link', { name: 'Forecasts' }))
    expect(currentPath()).toBe('/forecasts')
    expect(document.title).toBe('Forecast desk · Foretrade')
    await screen.findByText('$100,194.00')
  })

  it('treats /chat/:id as the Agent and falls back to the app name elsewhere', async () => {
    stubSession()
    const { unmount } = renderPage(shell, { route: '/chat/c1' })
    expect(within(rail()).getByRole('link', { name: 'Agent' })).toHaveAttribute('aria-current', 'page')
    await screen.findByText('$100,194.00')
    unmount()
    expect(document.title).toBe('Foretrade')
    renderPage(shell, { route: '/elsewhere' })
    expect(document.title).toBe('Foretrade')
    await screen.findByText('$100,194.00')
  })

  it('shows the paper badge, the equity pill with total P&L, and the credits pill', async () => {
    stubSession()
    renderPage(shell, { route: '/' })
    expect(screen.getByTitle('Simulated fills only. Nothing executes on a real venue.')).toHaveTextContent('PAPER TRADING')
    const equity = (await screen.findByText('$100,194.00')).closest('a')!
    expect(equity).toHaveAttribute('href', '/account')
    expect(equity).toHaveTextContent('Paper equity$100,194.00▲up 0.19%')
    await waitFor(() => expect(creditsPill()).toHaveTextContent('482 cr'))
    expect(screen.getByRole('tooltip')).toHaveTextContent('Daily AI credits, resets 00:00 UTC')
    expect(creditsPill().querySelector('.bg-accent')).not.toBeNull() // plenty left
  })

  it('turns the credits dot red under 10% left', async () => {
    stubSession()
    vi.spyOn(api, 'me').mockResolvedValue({ user: fx.user, credits: { ...fx.credits, used: 460 } })
    renderPage(shell, { route: '/' })
    await waitFor(() => expect(creditsPill()).toHaveTextContent('40 cr'))
    expect(creditsPill().querySelector('.bg-down')).not.toBeNull()
    await screen.findByText('$100,194.00')
  })

  it('draws an empty credits bar when the daily limit is 0', async () => {
    stubSession()
    vi.spyOn(api, 'me').mockResolvedValue({ user: fx.user, credits: { ...fx.credits, limit: 0, used: 0 } })
    renderPage(shell, { route: '/' })
    expect(await screen.findByText('0 / 0')).toBeInTheDocument()
    expect(screen.getByText('0 / 0').closest('.rounded-lg')!.querySelector<HTMLElement>('.bg-accent')!.style.width).toBe('0%')
    await screen.findByText('$100,194.00')
  })

  it('shows placeholders until the portfolio and credits load', () => {
    vi.spyOn(api, 'me').mockReturnValue(new Promise(() => {}))
    vi.spyOn(api, 'portfolio').mockReturnValue(new Promise(() => {}))
    vi.spyOn(api, 'assets').mockReturnValue(new Promise(() => {}))
    renderPage(shell, { route: '/' })
    expect(screen.getByRole('link', { name: 'Paper equity' })).not.toHaveTextContent('$')
    expect(screen.getByText('—')).toBeInTheDocument()
  })

  it('toggles the theme', async () => {
    stubSession()
    const { user } = renderPage(shell, { route: '/' })
    await user.click(screen.getAllByRole('button', { name: 'Switch to dark theme' })[0])
    expect(document.documentElement.dataset.theme).toBe('dark')
    expect(screen.getAllByRole('button', { name: 'Switch to light theme' }).length).toBeGreaterThan(0)
    await user.click(screen.getByRole('button', { name: 'Light theme', ...menu })) // the phone menu entry
    expect(document.documentElement.dataset.theme).toBe('light')
    await screen.findByText('$100,194.00')
  })

  it('has a user menu with the account, today’s credits and log out', async () => {
    stubSession()
    const logout = vi.spyOn(api, 'logout').mockResolvedValue(undefined)
    const { user } = renderPage(shell, { route: '/' })
    expect(await screen.findByText('demo@example.com')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Account menu' })).toHaveTextContent('DT')
    expect(screen.getByText('482 / 500')).toBeInTheDocument()

    await user.click(screen.getByRole('link', { name: 'Paper account', ...menu }))
    expect(currentPath()).toBe('/account')

    await user.click(screen.getByRole('button', { name: 'Log out', ...menu }))
    expect(logout).toHaveBeenCalledTimes(1)
    await waitFor(() => expect(screen.queryByText('demo@example.com')).toBeNull())
    expect(screen.getByRole('button', { name: 'Account menu' })).toHaveTextContent('?')
  })

  it('keeps the shell up when a page crashes, and recovers on navigation', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    stubSession()
    const { user } = renderPage(shell, { route: '/boom' })
    expect(screen.getByRole('heading', { name: 'This page failed to load.' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Reload' })).toBeInTheDocument()
    await user.click(within(rail()).getByRole('link', { name: 'Trade' }))
    expect(await screen.findByText('page body')).toBeInTheDocument()
  })

  it('shows a page skeleton while a route chunk loads', async () => {
    stubSession()
    renderPage(shell, { route: '/slow' })
    expect(screen.getByRole('status', { name: 'Loading page' })).toBeInTheDocument()
    await screen.findByText('$100,194.00')
  })

  it('titles Automations and mounts the notifications bell, its unread count on the button', async () => {
    stubSession()
    vi.spyOn(api, 'notifications').mockResolvedValue({ items: fx.notifications, unread: 2 })
    renderPage(shell, { route: '/automations' })
    expect(within(rail()).getByRole('link', { name: 'Automations' })).toHaveAttribute('aria-current', 'page')
    expect(document.title).toBe('Automations · Foretrade')
    expect(await screen.findByRole('button', { name: 'Notifications, 2 unread' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'All automations', ...menu })).toHaveAttribute('href', '/automations') // the phone's way in
    await screen.findByText('$100,194.00')
  })

  it('offers a skip link to the content', async () => {
    stubSession()
    renderPage(shell, { route: '/' })
    expect(screen.getByRole('link', { name: 'Skip to content' })).toHaveAttribute('href', '#main')
    expect(screen.getByRole('main')).toHaveAttribute('id', 'main')
    await screen.findByText('$100,194.00')
  })
})

describe('PageHeader', () => {
  it('renders the label, serif title, subtitle and actions', () => {
    render(<PageHeader label="Paper trading" title="Paper account" subtitle="Equity and fills." actions={<button>Reset</button>} />)
    expect(screen.getByRole('heading', { level: 1, name: 'Paper account' })).toBeInTheDocument()
    expect(screen.getByText('Paper trading')).toHaveClass('label')
    expect(screen.getByText('Equity and fills.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Reset' })).toBeInTheDocument()
  })

  it('works with a title only', () => {
    render(<PageHeader title="Strategies" />)
    expect(screen.getByRole('heading', { name: 'Strategies' })).toBeInTheDocument()
  })
})

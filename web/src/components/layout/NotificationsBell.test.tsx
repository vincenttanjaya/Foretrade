import { fireEvent, screen, waitFor } from '@testing-library/react'
import { useLocation } from 'react-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from '../../lib/api'
import * as fx from '../../test/fixtures'
import { currentPath, renderPage, stubSession } from '../../test/utils'
import { NotificationsBell } from './NotificationsBell'

// jsdom has no popover API: the dropdown stays in the DOM but counts as hidden.
const menu = { hidden: true }

function Prompt() {
  const { state } = useLocation() as { state: { prompt?: string; context?: { symbol?: string } } | null }
  return (
    <p data-testid="prompt">
      {state?.prompt} [{state?.context?.symbol}]
    </p>
  )
}

const show = () => renderPage(<NotificationsBell />, { route: '/trade/SOL', path: '/trade/:symbol', routes: { '/': <Prompt /> } })

beforeEach(() => {
  stubSession()
})

describe('NotificationsBell', () => {
  it('opens a successful research report in its owned chat with an AI icon and no action prompt', async () => {
    const research = fx.notification({
      id: 'workflow_research:run-owned', kind: 'workflow_research',
      title: 'Scheduled research · Weekly portfolio review', body: 'Portfolio exposure improved; the thesis still holds.',
      symbol: null, link: '/chat/research-owned',
    })
    vi.spyOn(api, 'notifications').mockResolvedValue({ items: [research], unread: 1 })
    vi.spyOn(api, 'notificationsSeen').mockResolvedValue(undefined)
    const { user } = renderPage(<NotificationsBell />, { routes: { '/chat/:id': <p>Private research report</p> } })
    await user.click(await screen.findByRole('button', { name: 'Notifications, 1 unread' }))
    const link = screen.getByRole('link', { name: research.title, ...menu })
    expect(link).toHaveAttribute('href', '/chat/research-owned')
    expect(link.closest('li')).toHaveTextContent(research.body)
    expect(link.closest('li')!.querySelector('span svg')!.parentElement).toHaveClass('text-accent-ink')
    expect(screen.queryByRole('button', { name: 'Ask the agent', ...menu })).toBeNull()
    await user.click(link)
    expect(currentPath()).toBe('/chat/research-owned')
    expect(screen.getByText('Private research report')).toBeInTheDocument()
  })

  it('badges the unread count, lists the items and marks them seen when opened', async () => {
    const feed = vi.spyOn(api, 'notifications').mockResolvedValue({ items: fx.notifications, unread: 3 })
    const seen = vi.spyOn(api, 'notificationsSeen').mockResolvedValue(undefined)
    const { user } = show()
    const bell = await screen.findByRole('button', { name: 'Notifications, 3 unread' })
    expect(bell).toHaveTextContent('3')
    expect(feed).toHaveBeenCalledWith(20)
    expect(screen.getByRole('link', { name: 'SOL fell below $145.00', ...menu })).toHaveAttribute('href', '/automations')
    expect(screen.getByText('Now $144.80 · Dip zone')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Stop loss hit · ETH', ...menu })).toHaveAttribute('href', '/account')
    expect(screen.getByRole('link', { name: 'Forecast ready · Yes 28%', ...menu })).toHaveAttribute('href', '/forecasts/f1')
    expect(screen.getAllByRole('button', { name: 'Ask the agent', ...menu })).toHaveLength(1) // alert items only

    feed.mockResolvedValue({ items: fx.notifications, unread: 0 })
    await user.click(bell)
    expect(seen).toHaveBeenCalledTimes(1)
    expect(screen.getByRole('button', { name: 'Notifications' })).not.toHaveTextContent('3')
    await waitFor(() => expect(feed).toHaveBeenCalledTimes(2)) // reloaded after /seen
    await user.click(screen.getByRole('button', { name: 'Notifications' }))
    expect(seen).toHaveBeenCalledTimes(1) // nothing unread: no second /seen
  })

  it('shows 99+ for a full badge and polls again on window focus', async () => {
    const feed = vi.spyOn(api, 'notifications').mockResolvedValue({ items: [], unread: 99 })
    show()
    expect(await screen.findByRole('button', { name: 'Notifications, 99 unread' })).toHaveTextContent('99+')
    fireEvent.focus(window)
    await waitFor(() => expect(feed).toHaveBeenCalledTimes(2))
  })

  it('swallows a failing feed: no badge, no toast, the empty text', async () => {
    const feed = vi.spyOn(api, 'notifications').mockRejectedValue(new Error('404'))
    show()
    await waitFor(() => expect(feed).toHaveBeenCalled())
    expect(screen.getByRole('button', { name: 'Notifications' })).toHaveTextContent('')
    expect(
      screen.getByText('Nothing yet. Alerts, stop/target hits, autopilot and copied fills, stopped copy plans and forecast results show up here.'),
    ).toBeInTheDocument()
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('shows scheduled runs with their icons, linking to Automations', async () => {
    const run = (kind: 'workflow' | 'workflow_order' | 'workflow_failed', title: string, body: string) =>
      fx.notification({ id: `${kind}:r1`, kind, title, body, symbol: 'BTC', link: '/automations' })
    vi.spyOn(api, 'notifications').mockResolvedValue({
      items: [
        run('workflow', 'Daily brief · BTC', 'BTC $61,240.50 (+1.2% 24h) · Top headline: ETF inflows'),
        run('workflow_order', 'Scheduled buy filled · BTC', 'Bought $100 BTC at $61,240.50'),
        run('workflow_failed', 'Scheduled order not filled · BTC', 'Not enough buying power when the alert fired.'),
      ],
      unread: 0,
    })
    show()
    const item = async (title: string) => (await screen.findByRole('link', { name: title, ...menu })).closest('li')!
    for (const [title, tone] of [
      ['Daily brief · BTC', 'text-accent-ink'],
      ['Scheduled buy filled · BTC', 'text-up'],
      ['Scheduled order not filled · BTC', 'text-down'],
    ]) {
      const li = await item(title)
      expect(li.querySelector('a')).toHaveAttribute('href', '/automations')
      expect(li.querySelector('span svg')!.parentElement).toHaveClass(tone)
    }
    expect(screen.getByText('Bought $100 BTC at $61,240.50')).toBeInTheDocument()
    expect(screen.queryAllByRole('button', { name: 'Ask the agent', ...menu })).toHaveLength(0) // fired price alerts only
  })

  it('asks the agent about a fired alert', async () => {
    vi.spyOn(api, 'notifications').mockResolvedValue({ items: fx.notifications, unread: 0 })
    const { user } = show()
    await user.click(await screen.findByRole('button', { name: 'Ask the agent', ...menu }))
    expect(currentPath()).toBe('/')
    expect(screen.getByTestId('prompt')).toHaveTextContent(
      'SOL just fell below $145.00 (now $144.80). Should I act? Check the setup and draft a trade if it is there. [SOL]',
    )
  })
  it('shows a fired drawdown alert linking to its history row, and asks the agent about it', async () => {
    const sold = fx.notification({ id: 'drawdown_order:dd3', kind: 'drawdown_order', title: 'Drawdown sell filled · SOL', link: '/account',
      body: '50% below ATH $250.00 · sold 6.6657 SOL (100% of the long) at $124.78 · realized −$170.12' })
    vi.spyOn(api, 'notifications').mockResolvedValue({ items: [fx.drawdownNotification, sold], unread: 0 })
    const { user } = show()
    const link = await screen.findByRole('link', { name: 'SOL fell 50% below its all-time high', ...menu })
    expect(link).toHaveAttribute('href', '/automations?tab=history&alert=dd1')
    expect(link.closest('li')!.querySelector('span svg')!.parentElement).toHaveClass('text-accent-ink')
    expect(screen.getByRole('link', { name: 'Drawdown sell filled · SOL', ...menu }).closest('li')!.querySelector('span svg')!.parentElement).toHaveClass('text-up')
    await user.click(screen.getByRole('button', { name: 'Ask the agent', ...menu })) // the fired drawdown only, not its sale
    expect(screen.getByTestId('prompt')).toHaveTextContent(
      'SOL just fell 50% below its all-time high (now $124.80). Should I act? Check the setup and draft a trade if it is there. [SOL]',
    )
  })
  it('shows a fired odds alert with the market icon, linking to its forecast', async () => {
    vi.spyOn(api, 'notifications').mockResolvedValue({ items: [fx.oddsNotification], unread: 1 })
    show()
    const link = await screen.findByRole('link', { name: 'Polymarket moved: "ETH outperforms BTC in Q4 2026?" rose above 62%', ...menu })
    expect(link).toHaveAttribute('href', '/forecasts/f1234567-aaaa')
    const icon = link.closest('li')!.querySelector('span svg')!
    expect(icon.parentElement).toHaveClass('text-accent-ink')
    expect(icon.querySelector('path')).toHaveAttribute('d', 'M8 19.5v-15M4.5 8L8 4.5 11.5 8') // IconSignal
    expect(screen.getByText('Now 62.5% · desk 62%')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Ask the agent', ...menu })).toBeNull()
  })

  it('follows a direct crowd notification to its owned History row without inventing desk or forecast context', async () => {
    const direct = fx.notification({ id: 'direct:owned', kind: 'odds_alert', title: 'Polymarket moved: crowd Yes rose above 50%', body: 'Now 60%', symbol: '', link: '/automations?tab=history&alert=owned' })
    vi.spyOn(api, 'notifications').mockResolvedValue({ items: [direct], unread: 1 })
    const seen = vi.spyOn(api, 'notificationsSeen').mockResolvedValue(undefined)
    const { user } = renderPage(<NotificationsBell />, { routes: { '/automations': <p>Owned retained History</p> } })
    await user.click(await screen.findByRole('button', { name: 'Notifications, 1 unread' }))
    const link = screen.getByRole('link', { name: direct.title, ...menu })
    expect(link).toHaveAttribute('href', '/automations?tab=history&alert=owned')
    expect(link.closest('li')).not.toHaveTextContent('desk')
    await user.click(link)
    expect(currentPath()).toBe('/automations?tab=history&alert=owned')
    expect(seen).toHaveBeenCalledTimes(1)
  })
})

it('shows copied fills and stopped plans with their icon tones, account links and seen behavior', async () => {
  vi.spyOn(api, 'notifications').mockResolvedValue({ items: [
    fx.notification({ id: 'copy_order:t1', kind: 'copy_order', title: 'Copied Trend Rider · bought BTC', body: '$1,000 at $60,000', link: '/account' }),
    fx.notification({ id: 'copy_stopped:cp1', kind: 'copy_stopped', title: 'Stopped copying Trend Rider', body: 'Stopped by you; positions kept or closed.', link: '/account' }),
  ], unread: 2 })
  const seen = vi.spyOn(api, 'notificationsSeen').mockResolvedValue(undefined)
  const { user } = show()
  const fill = await screen.findByRole('link', { name: 'Copied Trend Rider · bought BTC', hidden: true })
  expect(fill).toHaveAttribute('href', '/account')
  expect(fill.closest('li')!.querySelector('.text-accent-ink')).toBeTruthy()
  expect(screen.getByRole('link', { name: 'Stopped copying Trend Rider', hidden: true }).closest('li')!.querySelector('.text-muted')).toBeTruthy()
  expect(screen.queryByRole('button', { name: 'Ask the agent', hidden: true })).toBeNull()
  await user.click(screen.getByRole('button', { name: 'Notifications, 2 unread' }))
  expect(seen).toHaveBeenCalledTimes(1)
})

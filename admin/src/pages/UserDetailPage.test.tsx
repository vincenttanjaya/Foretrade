import { screen, waitFor, within } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { ApiError, api } from '../lib/api'
import type { AdminUserDetail } from '../lib/types'
import * as fx from '../test/fixtures'
import { currentPath, renderPage, stubSession } from '../test/utils'
import { UserDetailPage } from './UserDetailPage'

function open(d: AdminUserDetail = fx.detail()) {
  stubSession()
  const user = vi.spyOn(api, 'user').mockResolvedValue(d)
  const r = renderPage(<UserDetailPage />, { route: `/users/${d.user.id}`, path: '/users/:id', routes: { '/users': <p>users list</p> } })
  return { ...r, load: user }
}
const ready = () => screen.findByRole('heading', { level: 1, name: 'Demo Investor' })
const card = (title: string) => screen.getByRole('heading', { name: title }).closest('.card, section') as HTMLElement
const dialog = (name: string | RegExp) => screen.getByRole('dialog', { name })

describe('UserDetailPage', () => {
  it('shows the profile, usage against the limit, counts, portfolio, trades, activity and audit', async () => {
    open()
    await ready()
    expect(screen.getAllByText('demo@example.com')[0]).toBeInTheDocument() // header (and the audit row)
    expect(card('Profile')).toHaveTextContent('Live sessions2')
    expect(card('Profile')).toHaveTextContent('Credits today12 / 500')
    expect(card('Profile')).toHaveTextContent('Daily limit500Default')
    const bars = within(screen.getByRole('group', { name: /AI credits per day for demo@example.com/ })).getAllByRole('img')
    expect(bars.at(-1)).toHaveAccessibleName('Oct 3: 12 credits')
    expect(screen.getByText(/limit 500/)).toBeInTheDocument()
    expect(card('Activity counts')).toHaveTextContent('Messages sent14')
    expect(card('Portfolio')).toHaveTextContent('Equity$100,194.00')
    expect(card('Portfolio')).toHaveTextContent('Total P&L+$194.00+0.19%')
    expect(within(screen.getByRole('table', { name: 'Open positions' })).getAllByRole('row')[1]).toHaveTextContent('ETH2$2,900.00$3,000.00$6,000.00+$200.00 (+3.45%)')
    expect(within(screen.getByRole('table', { name: 'Recent trades' })).getAllByRole('row')[1]).toHaveTextContent('Buy 2 ETH$2,900.00$5,800.00-$5.80manual')
    expect(screen.getByRole('region', { name: 'Chats' })).toHaveTextContent('Is BTC overbought right now?')
    expect(screen.getByRole('region', { name: 'Forecasts' })).toHaveTextContent('running')
    expect(screen.getByRole('region', { name: 'Strategies' })).toHaveTextContent('None.')
    expect(card('Audit')).toHaveTextContent('Updated userrole: user → adminby admin@example.com')
  })

  it('renders user-supplied text as text', async () => {
    open(fx.detail({ user: fx.adminUser({ name: '<img src=x onerror=alert(1)>' }), activity: { ...fx.detail().activity, chats: [{ id: 'c', title: '<script>x</script>', status: null, at: '2026-10-01T00:00:00Z' }] } }))
    expect(await screen.findByRole('heading', { level: 1, name: '<img src=x onerror=alert(1)>' })).toBeInTheDocument()
    expect(screen.getByText('<script>x</script>')).toBeInTheDocument()
    expect(document.querySelector('img, script')).toBeNull()
  })

  it('reads "Account deleted" for an unknown id, and shows other errors with Retry', async () => {
    stubSession()
    vi.spyOn(api, 'user').mockRejectedValue(new ApiError(404, 'not_found', 'User not found.'))
    const { unmount } = renderPage(<UserDetailPage />, { route: '/users/gone', path: '/users/:id' })
    expect(await screen.findByRole('heading', { level: 1, name: 'Account deleted' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Back to users' })).toHaveAttribute('href', '/users')
    unmount()

    vi.mocked(api.user).mockRejectedValueOnce(new ApiError(500, 'internal', 'Database unavailable.'))
    vi.mocked(api.user).mockResolvedValue(fx.detail())
    const { user } = renderPage(<UserDetailPage />, { route: '/users/u1', path: '/users/:id' })
    expect(await screen.findByRole('alert')).toHaveTextContent('Database unavailable.')
    await user.click(screen.getByRole('button', { name: 'Retry' }))
    await ready()
  })

  it('says when the portfolio cannot be valued and there are no trades', async () => {
    open(fx.detail({ portfolio: null, trades: [] }))
    await ready()
    expect(card('Portfolio')).toHaveTextContent("Market data is unavailable right now, so the portfolio can't be valued.")
    expect(card('Portfolio')).toHaveTextContent('No trades.')
  })

  it('disables only after confirming, then applies the returned user', async () => {
    const { user, load } = open()
    const update = vi.spyOn(api, 'updateUser').mockResolvedValue({ user: fx.adminUser({ status: 'disabled', disabledAt: '2026-10-03T00:00:00Z', sessions: 0 }) })
    await ready()
    await user.click(screen.getByRole('button', { name: 'Disable account' }))
    const d = dialog('Disable Demo Investor?')
    expect(d).toHaveTextContent("can't sign in until an admin enables the account")
    expect(update).not.toHaveBeenCalled()
    load.mockResolvedValue(fx.detail({ user: fx.adminUser({ status: 'disabled', disabledAt: '2026-10-03T00:00:00Z', sessions: 0 }) }))
    await user.click(within(d).getByRole('button', { name: 'Disable account' }))
    expect(update).toHaveBeenCalledWith('u1', { disabled: true })
    expect(await screen.findByText('Disabled demo@example.com.')).toBeInTheDocument()
    expect(await screen.findByRole('button', { name: 'Enable account' })).toBeInTheDocument()
    expect(load).toHaveBeenCalledTimes(2) // reloaded for the new audit row
  })

  it('enables a disabled account without a confirmation, and toasts a failure', async () => {
    const { user } = open(fx.detail({ user: fx.adminUser({ status: 'disabled', disabledAt: '2026-10-02T12:00:00Z' }) }))
    const update = vi.spyOn(api, 'updateUser').mockRejectedValueOnce(new ApiError(429, 'rate_limited', 'Too many admin changes. Try again in a few minutes.'))
    await ready()
    expect(card('Profile')).toHaveTextContent('DisabledOct 2, 2026')
    await user.click(screen.getByRole('button', { name: 'Enable account' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Too many admin changes. Try again in a few minutes.')
    update.mockResolvedValue({ user: fx.adminUser() })
    await user.click(screen.getByRole('button', { name: 'Enable account' }))
    expect(update).toHaveBeenLastCalledWith('u1', { disabled: false })
    expect(await screen.findByText('Enabled demo@example.com. They can sign in again.')).toBeInTheDocument()
  })

  it.each([
    ['Revoke sessions', 'Revoke all sessions?', 'revokeSessions', { user: fx.adminUser({ sessions: 0 }), revoked: 2 }, 'Revoked 2 sessions.'],
    ["Reset today's credits", "Reset today's AI credits?", 'resetCredits', { user: fx.adminUser({ creditsToday: 0 }) }, "Reset today's credits for demo@example.com."],
    ['Reset portfolio', 'Reset the paper portfolio?', 'resetPortfolio', { user: fx.adminUser(), portfolio: { ...fx.portfolio, positions: [] } }, 'Reset the paper portfolio of demo@example.com.'],
  ] as const)('%s asks first, then calls the API', async (button, title, method, res, toast) => {
    const { user } = open()
    const call = vi.spyOn(api, method).mockResolvedValue(res as never)
    await ready()
    await user.click(screen.getByRole('button', { name: button }))
    const d = dialog(title)
    await user.click(within(d).getByRole('button', { name: 'Cancel' }))
    expect(call).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: button }))
    const close = vi.spyOn(HTMLDialogElement.prototype, 'close')
    await user.click(within(dialog(title)).getAllByRole('button').at(-1)!)
    expect(call).toHaveBeenCalledWith('u1')
    expect(await screen.findByText(toast)).toBeInTheDocument()
    expect(close).toHaveBeenCalled() // closed, not unmounted: that is what returns focus to the button in a browser
  })

  it('copies showcase content from the account named in the dialog, showing a 400 verbatim', async () => {
    const { user, load } = open()
    const seed = vi.spyOn(api, 'seedUser').mockRejectedValueOnce(new ApiError(400, 'bad_request', 'Pick a different account to copy from.'))
    await ready()
    await user.click(screen.getByRole('button', { name: 'Copy showcase content…' }))
    const d = dialog('Copy showcase content')
    expect(d).toHaveTextContent(
      'its 3 newest forecasts with their update history plus up to 2 resolved ones, AI portfolios that beat their saved benchmark (up to 2), strategies with Sharpe above 0.5 that beat buy-and-hold (up to 3) and its best arena run vs BTC.',
    )
    expect(d).toHaveTextContent('Winners first; when none qualifies, the single best one.')
    expect(d).toHaveTextContent('skips forecasts already copied')
    expect(within(d).getByLabelText('Copy from')).toHaveValue('demo@example.com')
    expect(seed).not.toHaveBeenCalled()
    await user.click(within(d).getByRole('button', { name: 'Copy content' }))
    expect(seed).toHaveBeenCalledWith('u1', 'demo@example.com')
    expect(await within(d).findByRole('alert')).toHaveTextContent('Pick a different account to copy from.')

    seed.mockResolvedValue({ user: fx.adminUser(), seeded: { forecasts: 3, portfolios: 2, strategies: 3, arenaRuns: 1 } })
    await user.clear(within(d).getByLabelText('Copy from'))
    await user.type(within(d).getByLabelText('Copy from'), 'showcase@example.com')
    await user.click(within(d).getByRole('button', { name: 'Copy content' }))
    expect(seed).toHaveBeenLastCalledWith('u1', 'showcase@example.com')
    expect(await screen.findByText('Copied 3 forecasts · 2 AI portfolios · 3 strategies · 1 arena run into demo@example.com.')).toBeInTheDocument()
    expect(load).toHaveBeenCalledTimes(2) // reloaded for the new audit row and counts
  })

  it('warns when a seed found no strategy to copy', async () => {
    const { user } = open()
    vi.spyOn(api, 'seedUser').mockResolvedValue({ user: fx.adminUser(), seeded: { forecasts: 1, portfolios: 1, strategies: 0, arenaRuns: 0 } })
    await ready()
    await user.click(screen.getByRole('button', { name: 'Copy showcase content…' }))
    await user.click(within(dialog('Copy showcase content')).getByRole('button', { name: 'Copy content' }))
    expect(
      await screen.findByText(
        'Copied 1 forecast · 1 AI portfolio · 0 strategies · 0 arena runs into demo@example.com. The source account has no strategy to copy. Build one there first.',
      ),
    ).toBeInTheDocument()
  })

  it('keeps a confirmation open with the 409 message', async () => {
    const { user } = open(fx.detail({ user: fx.adminUser({ role: 'admin' }) }))
    vi.spyOn(api, 'updateUser').mockRejectedValue(new ApiError(409, 'conflict', 'At least one active admin must remain.'))
    await ready()
    await user.click(screen.getByRole('button', { name: 'Disable account' }))
    await user.click(within(dialog(/Disable/)).getByRole('button', { name: 'Disable account' }))
    expect(await within(dialog(/Disable/)).findByRole('alert')).toHaveTextContent('At least one active admin must remain.')
  })

  it('edits only the changed fields, and does nothing when nothing changed', async () => {
    const { user } = open()
    const update = vi.spyOn(api, 'updateUser').mockResolvedValue({ user: fx.adminUser({ name: 'Demo Two', creditLimit: 50 }) })
    await ready()
    await user.click(screen.getByRole('button', { name: 'Edit profile' }))
    await user.click(within(dialog('Edit profile')).getByRole('button', { name: 'Save changes' }))
    expect(update).not.toHaveBeenCalled()
    expect(await screen.findByText('Nothing changed.')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Edit profile' }))
    const d = dialog('Edit profile')
    await user.clear(within(d).getByLabelText('Name'))
    await user.type(within(d).getByLabelText('Name'), ' Demo Two ')
    await user.click(within(d).getByRole('checkbox', { name: 'Use default (500)' }))
    await user.clear(within(d).getByLabelText('Custom daily credit limit'))
    await user.type(within(d).getByLabelText('Custom daily credit limit'), '50')
    await user.click(within(d).getByRole('button', { name: 'Save changes' }))
    expect(update).toHaveBeenCalledWith('u1', { name: 'Demo Two', creditLimit: 50 })
    expect(await screen.findByText('Saved demo@example.com.')).toBeInTheDocument()
  })

  it('switches a custom limit back to the default and promotes, showing a 409 verbatim', async () => {
    const { user } = open(fx.detail({ user: fx.adminUser({ creditLimit: 100 }) }))
    const update = vi.spyOn(api, 'updateUser').mockRejectedValue(new ApiError(400, 'bad_request', 'role must be "user" or "admin".'))
    await ready()
    await user.click(screen.getByRole('button', { name: 'Edit profile' }))
    const d = dialog('Edit profile')
    await user.selectOptions(within(d).getByLabelText('Role'), 'admin')
    await user.click(within(d).getByRole('checkbox', { name: 'Use default (500)' }))
    await user.click(within(d).getByRole('button', { name: 'Save changes' }))
    expect(update).toHaveBeenCalledWith('u1', { role: 'admin', creditLimit: null })
    expect(await within(d).findByRole('alert')).toHaveTextContent('role must be "user" or "admin".')
  })

  it("won't let an admin disable, demote or delete their own account", async () => {
    const { user } = open(fx.detail({ user: fx.adminUser({ id: 'a1', email: 'admin@example.com', name: 'Demo Investor', role: 'admin' }) }))
    await ready()
    expect(screen.getByText('you')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Disable account' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Disable account' })).toHaveAccessibleDescription("You can't disable your own account.")
    expect(screen.getByText("You can't delete your own account.")).toBeInTheDocument()
    expect(screen.queryByLabelText(/to confirm/)).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Edit profile' }))
    expect(within(dialog('Edit profile')).getByLabelText('Role')).toBeDisabled()
    expect(dialog('Edit profile')).toHaveTextContent("You can't demote your own account.")
  })

  it('generates a password, shows it once and clears it on close', async () => {
    const { user } = open()
    const set = vi.spyOn(api, 'setPassword').mockResolvedValue({ user: fx.adminUser({ sessions: 0 }), password: 'ZYXWVTSRQPNMKJHGFEDCBA9876' })
    await ready()
    await user.click(screen.getByRole('button', { name: 'Set password' }))
    expect(dialog('Set a password')).toHaveTextContent('Signs demo@example.com out of every session.')
    await user.click(screen.getByRole('button', { name: 'Generate password' }))
    expect(set).toHaveBeenCalledWith('u1', undefined)
    expect(await screen.findByLabelText('Temporary password')).toHaveValue('ZYXWVTSRQPNMKJHGFEDCBA9876')
    await user.click(screen.getByRole('button', { name: "I've saved it" }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(document.body.innerHTML).not.toContain('ZYXWVTSRQPNMKJHGFEDCBA9876')

    await user.click(screen.getByRole('button', { name: 'Set password' }))
    expect(screen.queryByLabelText('Temporary password')).not.toBeInTheDocument() // a fresh form, not the old password
    expect(screen.getByRole('button', { name: 'Generate password' })).toBeInTheDocument()
  })

  it('sets a typed password after checking it, without a reveal', async () => {
    const { user } = open()
    const set = vi.spyOn(api, 'setPassword').mockResolvedValue({ user: fx.adminUser() })
    await ready()
    await user.click(screen.getByRole('button', { name: 'Set password' }))
    await user.click(screen.getByRole('radio', { name: 'Type one' }))
    await user.type(screen.getByLabelText('New password'), 'short')
    await user.click(within(dialog('Set a password')).getByRole('button', { name: 'Set password' }))
    expect(within(dialog('Set a password')).getByRole('alert')).toHaveTextContent('Use at least 12 characters.')
    expect(set).not.toHaveBeenCalled()
    await user.type(screen.getByLabelText('New password'), '-and-longer')
    await user.click(within(dialog('Set a password')).getByRole('button', { name: 'Set password' }))
    expect(set).toHaveBeenCalledWith('u1', 'short-and-longer')
    expect(await screen.findByText('Password set for demo@example.com.')).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('cannot set the password of a managed account', async () => {
    open(fx.detail({ user: fx.adminUser({ managed: true }) }))
    await ready()
    const button = screen.getByRole('button', { name: 'Set password' })
    expect(button).toBeDisabled()
    expect(button).toHaveAccessibleDescription("This account's password comes from the server config.")
    expect(screen.getByText('managed')).toBeInTheDocument()
    // Deleting it is not final: the API recreates it at boot.
    expect(card('Delete this account')).toHaveTextContent('recreated with the config password at the next API restart')
    expect(card('Delete this account')).not.toHaveTextContent("can't be undone")
  })

  it('deletes only after the exact email is typed', async () => {
    const { user } = open()
    const del = vi.spyOn(api, 'deleteUser').mockRejectedValueOnce(new ApiError(409, 'conflict', 'At least one active admin must remain.'))
    await ready()
    expect(card('Delete this account')).toHaveTextContent("This can't be undone.")
    const button = screen.getByRole('button', { name: 'Delete account' })
    const input = screen.getByLabelText(/Type demo@example.com to confirm/)
    expect(button).toBeDisabled()
    await user.type(input, 'DEMO@example.com')
    expect(button).toBeDisabled()
    await user.type(input, '{Enter}') // submitting with the wrong text does nothing
    expect(del).not.toHaveBeenCalled()
    await user.clear(input)
    await user.type(input, 'demo@example.com')
    expect(button).toBeEnabled()
    await user.click(button)
    expect(await screen.findByRole('alert')).toHaveTextContent('At least one active admin must remain.')

    del.mockResolvedValue()
    await user.click(button)
    expect(await screen.findByText('users list')).toBeInTheDocument()
    expect(currentPath()).toBe('/users')
    expect(del).toHaveBeenLastCalledWith('u1')
  })
})

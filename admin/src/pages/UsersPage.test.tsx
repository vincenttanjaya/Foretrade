import { screen, waitFor, within } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { ApiError, api } from '../lib/api'
import type { AdminUser, UserList } from '../lib/types'
import * as fx from '../test/fixtures'
import { currentPath, renderPage } from '../test/utils'
import { queryFromParams, UsersPage } from './UsersPage'

const list = (users: AdminUser[], over: Partial<UserList> = {}): UserList => ({ users, total: users.length, page: 1, limit: 25, defaultCreditLimit: 500, ...over })
const stubList = (l: UserList = list([fx.adminUser(), fx.adminUser({ id: 'u2', email: 'ops@example.com', name: 'Ops Lead', role: 'admin', creditLimit: 100, creditsToday: 3, equity: null })])) =>
  vi.spyOn(api, 'users').mockResolvedValue(l)
const open = (route = '/users') => renderPage(<UsersPage />, { route, path: '/users', routes: { '/users/:id': <p>detail page</p> } })
const lastQuery = () => vi.mocked(api.users).mock.calls.at(-1)![0]
const rows = () => within(screen.getByRole('table')).getAllByRole('row').slice(1)

describe('UsersPage', () => {
  it('lists accounts with badges, credits against the limit, equity and sessions', async () => {
    stubList()
    open()
    expect(await screen.findByText('Ops Lead')).toBeInTheDocument()
    expect(screen.getByRole('heading', { level: 1, name: 'Users.' })).toBeInTheDocument()
    expect(screen.getByText('2 accounts')).toBeInTheDocument()
    const [demo, ops] = rows()
    expect(demo).toHaveTextContent('Demo Investordemo@example.com')
    expect(demo).toHaveTextContent('useractive')
    expect(demo).toHaveTextContent('12 / 500$100,1942')
    expect(ops).toHaveTextContent('adminactive')
    expect(ops).toHaveTextContent('3 / 100—2')
    expect(within(demo).getByRole('link', { name: 'Demo Investor' })).toHaveAttribute('href', '/users/u1')
    expect(lastQuery()).toEqual({ limit: 25 })
    expect(screen.getByText('1–2 of 2')).toBeInTheDocument()
  })

  it('reads filters, sort and page from the URL, dropping values the API would reject', async () => {
    stubList(list([fx.adminUser()], { total: 60, page: 2 }))
    open('/users?q=ann&status=disabled&role=admin&sort=email&order=asc&page=2')
    await screen.findByText('Demo Investor')
    expect(lastQuery()).toEqual({ q: 'ann', status: 'disabled', role: 'admin', sort: 'email', order: 'asc', page: 2, limit: 25 })
    expect(screen.getByLabelText('Search')).toHaveValue('ann')
    expect(screen.getByLabelText('Status')).toHaveValue('disabled')
    expect(screen.getByLabelText('Role')).toHaveValue('admin')
    expect(queryFromParams(new URLSearchParams('status=gone&role=root&sort=password&order=up&page=-3'))).toEqual({
      q: undefined,
      status: undefined,
      role: undefined,
      sort: undefined,
      order: undefined,
      page: undefined,
    })
  })

  it('searches 300 ms after typing stops and goes back to page 1', async () => {
    stubList()
    const { user } = open('/users?page=2')
    await screen.findByText('Ops Lead')
    await user.type(screen.getByLabelText('Search'), 'ops')
    expect(currentPath()).toBe('/users?page=2') // still debouncing
    await waitFor(() => expect(currentPath()).toBe('/users?q=ops'))
    await waitFor(() => expect(lastQuery()).toEqual({ q: 'ops', limit: 25 }))
  })

  it('filters by status and role, sorts by a header, and pages', async () => {
    stubList(list([fx.adminUser()], { total: 30 }))
    const { user } = open()
    await screen.findByText('Demo Investor')
    await user.selectOptions(screen.getByLabelText('Status'), 'active')
    await user.selectOptions(screen.getByLabelText('Role'), 'user')
    expect(currentPath()).toBe('/users?status=active&role=user')

    const created = screen.getByRole('columnheader', { name: /Created/ })
    expect(created).toHaveAttribute('aria-sort', 'descending') // the default sort
    await user.click(within(created).getByRole('button'))
    expect(currentPath()).toBe('/users?status=active&role=user&sort=createdAt&order=asc')
    expect(screen.getByRole('columnheader', { name: /Created/ })).toHaveAttribute('aria-sort', 'ascending')
    await user.click(screen.getByRole('button', { name: 'User' }))
    expect(currentPath()).toBe('/users?status=active&role=user&sort=name&order=asc')
    await user.click(screen.getByRole('button', { name: 'Credits today' }))
    expect(currentPath()).toBe('/users?status=active&role=user&sort=creditsToday&order=desc')

    vi.mocked(api.users).mockResolvedValue(list([fx.adminUser()], { total: 30, page: 2 }))
    await user.click(screen.getByRole('button', { name: 'Next' }))
    expect(currentPath()).toMatch(/page=2$/)
    expect(await screen.findByText('26–26 of 30')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Next' })).toBeDisabled()
    await user.click(screen.getByRole('button', { name: 'Previous' }))
    expect(currentPath()).not.toMatch(/page=/)
  })

  it('opens a user from the row', async () => {
    stubList()
    const { user } = open()
    await user.click(await screen.findByText('ops@example.com'))
    expect(await screen.findByText('detail page')).toBeInTheDocument()
    expect(currentPath()).toBe('/users/u2')
  })

  it('has empty states for no matches and a page past the end', async () => {
    stubList(list([]))
    const { user, unmount } = open('/users?q=zzz')
    expect(await screen.findByText('No accounts match.')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Clear filters' }))
    expect(currentPath()).toBe('/users')
    unmount()

    stubList(list([], { total: 30, page: 9 }))
    const second = open('/users?page=9')
    expect(await screen.findByText('Nothing on this page.')).toBeInTheDocument()
    await second.user.click(screen.getByRole('button', { name: 'Go to page 1' }))
    expect(currentPath()).toBe('/users')
  })

  it('shows a load error with Retry', async () => {
    const users = vi.spyOn(api, 'users').mockRejectedValueOnce(new ApiError(400, 'bad_request', 'sort is not a sortable field.'))
    const { user } = open()
    expect(await screen.findByRole('alert')).toHaveTextContent('sort is not a sortable field.')
    users.mockResolvedValue(list([fx.adminUser()]))
    await user.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('Demo Investor')).toBeInTheDocument()
  })
})

describe('New user', () => {
  const created = fx.adminUser({ id: 'n1', email: 'ann@example.com', name: 'Ann Lee', creditsToday: 0 })

  async function fill(user: ReturnType<typeof open>['user'], { password = '', limit = '' } = {}) {
    await user.click(await screen.findByRole('button', { name: 'New user' }))
    const dialog = screen.getByRole('dialog', { name: 'Create a user.' })
    await user.type(within(dialog).getByLabelText('Email'), '  Ann@Example.com ')
    await user.type(within(dialog).getByLabelText('Name'), 'Ann Lee')
    if (limit) await user.type(within(dialog).getByLabelText('Daily credits'), limit)
    if (password) await user.type(within(dialog).getByLabelText('Password'), password)
    return dialog
  }

  it('shows a generated password once, with Copy, and clears it when closed', async () => {
    stubList()
    const create = vi.spyOn(api, 'createUser').mockResolvedValue({ user: created, password: 'ABCDEFGHJKMNPQRSTVWXYZ2345' })
    const { user } = open()
    const dialog = await fill(user)
    expect(within(dialog).getByLabelText('Daily credits')).toHaveAttribute('placeholder', 'Default (500)')
    await user.selectOptions(within(dialog).getByLabelText('Role'), 'admin')
    expect(dialog).toHaveTextContent('Admins can sign in to this console')
    await user.click(within(dialog).getByRole('button', { name: 'Create user' }))

    expect(create).toHaveBeenCalledWith({ email: 'Ann@Example.com', name: 'Ann Lee', role: 'admin' })
    expect(await screen.findByRole('dialog', { name: 'Account created.' })).toHaveTextContent('Shown once.')
    expect(screen.getByLabelText('Temporary password')).toHaveValue('ABCDEFGHJKMNPQRSTVWXYZ2345')
    expect(screen.getByText('Created ann@example.com.')).toBeInTheDocument()

    // Escape can't dismiss it by accident while the password is on screen.
    const cancel = new Event('cancel', { cancelable: true })
    screen.getByRole('dialog').dispatchEvent(cancel)
    expect(cancel.defaultPrevented).toBe(true)

    await user.click(screen.getByRole('button', { name: "I've saved it" }))
    expect(await screen.findByText('detail page')).toBeInTheDocument()
    expect(currentPath()).toBe('/users/n1')
    expect(screen.queryByDisplayValue('ABCDEFGHJKMNPQRSTVWXYZ2345')).not.toBeInTheDocument()
    expect(document.body.innerHTML).not.toContain('ABCDEFGHJKMNPQRSTVWXYZ2345')
    expect(sessionStorage.length + localStorage.length).toBe(0)
  })

  it('sends a typed password and a custom limit, and skips the reveal', async () => {
    stubList()
    const create = vi.spyOn(api, 'createUser').mockResolvedValue({ user: created })
    const { user } = open()
    const dialog = await fill(user, { password: 'correct horse battery', limit: '100' })
    await user.click(within(dialog).getByRole('button', { name: 'Create user' }))
    expect(create).toHaveBeenCalledWith({
      email: 'Ann@Example.com',
      name: 'Ann Lee',
      role: 'user',
      creditLimit: 100,
      password: 'correct horse battery',
    })
    expect(await screen.findByText('detail page')).toBeInTheDocument()
  })

  it('starts the account with showcase content from the chosen account and lists what was copied', async () => {
    stubList()
    const seeded = { forecasts: 3, portfolios: 2, strategies: 3, arenaRuns: 1 }
    const create = vi.spyOn(api, 'createUser').mockResolvedValue({ user: created, seeded })
    const { user } = open()
    const dialog = await fill(user, { password: 'correct horse battery' })
    await user.click(within(dialog).getByLabelText('Start with showcase content'))
    expect(within(dialog).getByText(/^Copies that account's showcase content/)).toHaveTextContent(
      "Copies that account's showcase content: its 3 newest forecasts with their update history plus up to 2 resolved ones, AI portfolios that beat their saved benchmark (up to 2), strategies with Sharpe above 0.5 that beat buy-and-hold (up to 3) and its best arena run vs BTC. Winners first; when none qualifies, the single best one. Never its chats or trades.",
    )
    const from = within(dialog).getByLabelText('Copy from')
    expect(from).toHaveValue('') // no default source: a fresh install has no demo account
    await user.type(from, 'showcase@example.com')
    await user.click(within(dialog).getByRole('button', { name: 'Create user' }))
    expect(create).toHaveBeenLastCalledWith(expect.objectContaining({ seedFrom: 'showcase@example.com' }))
    expect(await screen.findByText('Created ann@example.com. Copied 3 forecasts · 2 AI portfolios · 3 strategies · 1 arena run.')).toBeInTheDocument()
  })

  it('warns when the source had no AI portfolio or strategy to copy', async () => {
    stubList()
    vi.spyOn(api, 'createUser').mockResolvedValue({ user: created, seeded: { forecasts: 3, portfolios: 0, strategies: 0, arenaRuns: 1 } })
    const { user } = open()
    const dialog = await fill(user, { password: 'correct horse battery' })
    await user.click(within(dialog).getByLabelText('Start with showcase content'))
    await user.type(within(dialog).getByLabelText('Copy from'), 'demo@example.com')
    await user.click(within(dialog).getByRole('button', { name: 'Create user' }))
    expect(
      await screen.findByText(
        'Created ann@example.com. Copied 3 forecasts · 0 AI portfolios · 0 strategies · 1 arena run. The source account has no AI portfolio to copy. Build one there first. The source account has no strategy to copy. Build one there first.',
      ),
    ).toBeInTheDocument()
  })

  it('skips the showcase content by default', async () => {
    stubList()
    const create = vi.spyOn(api, 'createUser').mockResolvedValue({ user: created })
    const { user } = open()
    const dialog = await fill(user, { password: 'correct horse battery' })
    expect(within(dialog).getByLabelText('Start with showcase content')).not.toBeChecked()
    expect(within(dialog).queryByLabelText('Copy from')).toBeNull()
    await user.click(within(dialog).getByRole('button', { name: 'Create user' }))
    expect(create.mock.lastCall![0]).not.toHaveProperty('seedFrom')
  })

  it('checks the password before sending and shows API errors verbatim', async () => {
    stubList()
    const create = vi.spyOn(api, 'createUser').mockRejectedValue(new ApiError(409, 'conflict', 'An account with this email already exists.'))
    const { user } = open()
    const dialog = await fill(user, { password: 'short' })
    await user.click(within(dialog).getByRole('button', { name: 'Create user' }))
    expect(within(dialog).getByRole('alert')).toHaveTextContent('Use at least 12 characters.')
    expect(create).not.toHaveBeenCalled()

    await user.clear(within(dialog).getByLabelText('Password'))
    await user.click(within(dialog).getByRole('button', { name: 'Create user' }))
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('An account with this email already exists.')
    expect(screen.getByRole('dialog', { name: 'Create a user.' })).toBeInTheDocument()
  })

  it('cancels without sending, and a reopened form is empty', async () => {
    stubList()
    const create = vi.spyOn(api, 'createUser')
    const { user } = open()
    const dialog = await fill(user)
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'New user' }))
    expect(screen.getByLabelText('Email')).toHaveValue('')
    expect(create).not.toHaveBeenCalled()
  })
})

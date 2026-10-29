import { screen, within } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { ApiError, api } from '../lib/api'
import * as fx from '../test/fixtures'
import { currentPath, renderPage } from '../test/utils'
import { AuditPage } from './AuditPage'

const open = (route = '/audit') => renderPage(<AuditPage />, { route, path: '/audit' })

describe('AuditPage', () => {
  it('lists entries with labels, target links and detail summaries', async () => {
    vi.spyOn(api, 'audit').mockResolvedValue({
      entries: [
        fx.auditEntry(),
        fx.auditEntry({ id: 'e2', action: 'user.password', detail: { generated: true, sessionsRevoked: 1 } }),
        fx.auditEntry({ id: 'e3', action: 'user.delete', target: { id: 'gone', email: 'old@example.com' }, detail: { name: 'Old', role: 'user' } }),
        fx.auditEntry({ id: 'e4', action: 'user.enable', target: null, detail: {} }),
      ],
      total: 4,
      page: 1,
      limit: 50,
    })
    open()
    expect(await screen.findByText('Generated password')).toBeInTheDocument()
    const rows = within(screen.getByRole('table')).getAllByRole('row').slice(1)
    expect(rows[0]).toHaveTextContent('admin@example.comUpdated userdemo@example.comrole: user → admin')
    expect(rows[1]).toHaveTextContent('generated · 1 session revoked')
    expect(within(rows[2]).getByRole('link', { name: 'old@example.com' })).toHaveAttribute('href', '/users/gone')
    expect(rows[3]).toHaveTextContent('Enabled user——')
    expect(vi.mocked(api.audit).mock.calls[0][0]).toEqual({ page: 1, limit: 50 })
    expect(screen.getByText('Page 1 of 1 · 4 entries')).toBeInTheDocument()
  })

  it('pages through the log via the URL', async () => {
    const audit = vi.spyOn(api, 'audit').mockResolvedValue({ entries: [fx.auditEntry()], total: 120, page: 2, limit: 50 })
    const { user } = open('/audit?page=2')
    await screen.findByText('Page 2 of 3 · 120 entries')
    expect(audit).toHaveBeenLastCalledWith({ page: 2, limit: 50 }, expect.any(AbortSignal))
    await user.click(screen.getByRole('button', { name: 'Next' }))
    expect(currentPath()).toBe('/audit?page=3')
    await user.click(screen.getByRole('button', { name: 'Previous' }))
    await user.click(screen.getByRole('button', { name: 'Previous' }))
    expect(currentPath()).toBe('/audit')
  })

  it('has empty states and an error with Retry', async () => {
    const audit = vi.spyOn(api, 'audit').mockResolvedValue({ entries: [], total: 0, page: 1, limit: 50 })
    const { unmount } = open()
    expect(await screen.findByText('No admin changes yet.')).toBeInTheDocument()
    unmount()

    audit.mockResolvedValue({ entries: [], total: 3, page: 5, limit: 50 })
    const second = open('/audit?page=5')
    await second.user.click(await screen.findByRole('button', { name: 'Go to page 1' }))
    expect(currentPath()).toBe('/audit')
    second.unmount()

    audit.mockRejectedValueOnce(new ApiError(0, 'network', "Can't reach the API."))
    audit.mockResolvedValue({ entries: [fx.auditEntry()], total: 1, page: 1, limit: 50 })
    const third = open()
    expect(await screen.findByRole('alert')).toHaveTextContent("Can't reach the API.")
    await third.user.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('Updated user')).toBeInTheDocument()
  })
})

import { afterEach, describe, expect, it, vi } from 'vitest'
import { API_URL, api } from './api'
import { json } from '../test/utils'

afterEach(() => vi.unstubAllGlobals())
describe('research client transports', () => {
  it('encodes the owned ID and opaque exclusive cursor, and sends explicit free Resume JSON', async () => {
    const fetch = vi.fn().mockResolvedValueOnce(json({ runs: [], nextCursor: null })).mockResolvedValueOnce(json({ alert: { id: 'owned' } }))
    vi.stubGlobal('fetch', fetch)
    const ctrl = new AbortController()
    expect(await api.alertRuns('owned/id', { limit: 50, before: 'opaque+/=' }, ctrl.signal)).toEqual({ runs: [], nextCursor: null })
    expect(fetch.mock.calls[0]).toEqual([`${API_URL}/api/alerts/owned%2Fid/runs?limit=50&before=opaque%2B%2F%3D`, { method: 'GET', credentials: 'include', signal: ctrl.signal }])
    await api.resumeAlert('owned/id')
    expect(fetch.mock.calls[1]).toEqual([`${API_URL}/api/alerts/owned%2Fid/resume`, { method: 'POST', credentials: 'include', signal: undefined, headers: { 'Content-Type': 'application/json' }, body: '{}' }])
  })
})

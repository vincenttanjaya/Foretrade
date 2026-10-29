import { describe, expect, it, vi } from 'vitest'
import { json } from '../test/utils'
import { API_URL, ApiError, api, errorMessage, qs, setAuthHandlers } from './api'

const fetchMock = () => vi.mocked(fetch)
const lastCall = () => {
  const [url, init] = fetchMock().mock.calls.at(-1)!
  return { url: String(url), init: init as RequestInit }
}

describe('api client', () => {
  it('GETs with cookies, no body and the abort signal', async () => {
    fetchMock().mockResolvedValue(json({ overview: { asOf: 'x' } }))
    const signal = new AbortController().signal
    await expect(api.overview(signal)).resolves.toEqual({ overview: { asOf: 'x' } })
    const { url, init } = lastCall()
    expect(url).toBe(`${API_URL}/api/admin/overview`)
    expect(init).toMatchObject({ method: 'GET', credentials: 'include', signal })
    expect(init.body).toBeUndefined()
    expect(init.headers).toBeUndefined()
  })

  it.each([
    ['createUser', () => api.createUser({ email: 'a@b.co', name: 'A' }), 'POST', '/api/admin/users', { email: 'a@b.co', name: 'A' }],
    ['updateUser', () => api.updateUser('u 1', { disabled: true }), 'PATCH', '/api/admin/users/u%201', { disabled: true }],
    ['setPassword (generate)', () => api.setPassword('u1'), 'POST', '/api/admin/users/u1/password', {}],
    ['setPassword (typed)', () => api.setPassword('u1', 'correct horse battery'), 'POST', '/api/admin/users/u1/password', { password: 'correct horse battery' }],
    ['revokeSessions', () => api.revokeSessions('u1'), 'POST', '/api/admin/users/u1/sessions/revoke', {}],
    ['resetPortfolio', () => api.resetPortfolio('u1'), 'POST', '/api/admin/users/u1/reset-portfolio', {}],
    ['resetCredits', () => api.resetCredits('u1'), 'POST', '/api/admin/users/u1/reset-credits', {}],
    ['deleteUser', () => api.deleteUser('u1'), 'DELETE', '/api/admin/users/u1', {}],
    ['logout', () => api.logout(), 'POST', '/api/auth/logout', {}],
    ['updateSettings', () => api.updateSettings({ ai: { provider: 'ollama' } }), 'PUT', '/api/admin/settings', { ai: { provider: 'ollama' } }],
    ['testSettings', () => api.testSettings({ model: 'm' }), 'POST', '/api/admin/settings/test', { ai: { model: 'm' } }],
    ['models', () => api.models({}), 'POST', '/api/admin/settings/models', { ai: {} }],
    ['chatgptStart', () => api.chatgptStart(), 'POST', '/api/admin/settings/chatgpt/start', {}],
    ['chatgptSignOut', () => api.chatgptSignOut(), 'POST', '/api/admin/settings/chatgpt/signout', {}],
  ])('%s sends JSON with cookies (the CSRF rule)', async (_, call, method, path, body) => {
    fetchMock().mockResolvedValue(json({}, method === 'DELETE' ? 204 : 200))
    await call()
    const { url, init } = lastCall()
    expect(url).toBe(API_URL + path)
    expect(init).toMatchObject({ method, credentials: 'include', headers: { 'Content-Type': 'application/json' } })
    expect(JSON.parse(String(init.body))).toEqual(body)
  })

  it('returns undefined for 204', async () => {
    fetchMock().mockResolvedValue(new Response(null, { status: 204 }))
    await expect(api.deleteUser('u1')).resolves.toBeUndefined()
  })

  it('builds list queries without empty values', async () => {
    fetchMock().mockImplementation(async () => json({ users: [] }))
    await api.users({ q: 'ann lee', status: 'disabled', role: undefined, sort: 'email', order: 'asc', page: 2, limit: 25 })
    expect(lastCall().url).toBe(`${API_URL}/api/admin/users?q=ann+lee&status=disabled&sort=email&order=asc&page=2&limit=25`)
    await api.audit()
    expect(lastCall().url).toBe(`${API_URL}/api/admin/audit`)
    expect(qs({ a: '', b: undefined })).toBe('')
  })

  it('turns the error envelope into an ApiError', async () => {
    fetchMock().mockResolvedValue(json({ error: { code: 'conflict', message: 'At least one active admin must remain.' } }, 409))
    const err = await api.updateUser('u1', { role: 'user' }).catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect(err).toMatchObject({ status: 409, code: 'conflict', message: 'At least one active admin must remain.' })
    expect(errorMessage(err)).toBe('At least one active admin must remain.')
  })

  it('reads a bare string error as the message', async () => {
    fetchMock().mockResolvedValue(json({ error: 'invalid x-api-key' }, 400))
    await expect(api.updateSettings({ ai: { apiKey: 'k' } })).rejects.toMatchObject({ status: 400, code: 'internal', message: 'invalid x-api-key' })
  })

  it('falls back when the error body is not JSON, and reports network failures as status 0', async () => {
    fetchMock().mockResolvedValue(new Response('<html>bad gateway</html>', { status: 502 }))
    await expect(api.overview()).rejects.toMatchObject({ status: 502, code: 'internal', message: 'Request failed (502)' })
    fetchMock().mockRejectedValue(new TypeError('Failed to fetch'))
    await expect(api.overview()).rejects.toMatchObject({ status: 0, code: 'network' })
    expect(errorMessage(new Error('boom'), 'Fallback')).toBe('Fallback')
  })

  it('rethrows an abort as is', async () => {
    const ctrl = new AbortController()
    ctrl.abort()
    const abort = new DOMException('aborted', 'AbortError')
    fetchMock().mockRejectedValue(abort)
    await expect(api.overview(ctrl.signal)).rejects.toBe(abort)
  })

  it('calls the 401 handler (not for a failed login) and the 403 handler only for admin routes', async () => {
    const h = { unauthorized: vi.fn(), forbidden: vi.fn() }
    setAuthHandlers(h)
    fetchMock().mockResolvedValue(json({ error: { code: 'unauthorized', message: 'Sign in' } }, 401))
    await expect(api.users()).rejects.toMatchObject({ status: 401 })
    expect(h.unauthorized).toHaveBeenCalledTimes(1)
    fetchMock().mockResolvedValue(json({ error: { code: 'invalid_credentials', message: 'Wrong' } }, 401))
    await expect(api.login('a@b.co', 'x')).rejects.toMatchObject({ code: 'invalid_credentials' })
    expect(h.unauthorized).toHaveBeenCalledTimes(1)

    fetchMock().mockImplementation(async () => json({ error: { code: 'forbidden', message: 'Admins only.' } }, 403))
    await expect(api.overview()).rejects.toMatchObject({ status: 403 })
    expect(h.forbidden).toHaveBeenCalledTimes(1)
    await expect(api.logout()).rejects.toMatchObject({ status: 403 })
    expect(h.forbidden).toHaveBeenCalledTimes(1)
    setAuthHandlers({ unauthorized: () => {}, forbidden: () => {} })
  })
})

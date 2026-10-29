import { act, renderHook, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { api } from './api'
import { AuthProvider, useAuth } from './auth'
import * as fx from '../test/fixtures'
import { json } from '../test/utils'

function pending<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>((done) => { resolve = done }); return { promise, resolve } }
async function open() {
  vi.spyOn(api, 'me').mockResolvedValue({ user: fx.user, credits: fx.credits })
  const hook = renderHook(() => useAuth(), { wrapper: AuthProvider })
  await waitFor(() => expect(hook.result.current.user?.id).toBe('u1'))
  return hook
}
const memory = { id: 'c2d75208-a9a8-447e-a6c7-5f4fa161b6c0', text: 'Private.', at: '' }

describe('Profile owner and revision authority', () => {
  it('keeps replacement ME loading when the disposed StrictMode effect rejects its aborted request', async () => {
    const replacement = pending<Awaited<ReturnType<typeof api.me>>>()
    const me = vi.spyOn(api, 'me')
      .mockImplementationOnce((signal) => new Promise((_resolve, reject) => {
        signal?.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')), { once: true })
      }))
      .mockReturnValueOnce(replacement.promise)
    const { result } = renderHook(() => useAuth(), { wrapper: AuthProvider, reactStrictMode: true })
    expect(me).toHaveBeenCalledTimes(2)
    expect(me.mock.calls[0][0]?.aborted).toBe(true)
    await act(async () => {})
    expect(result.current.loading).toBe(true)
    expect(me.mock.calls[1][0]?.aborted).toBe(false)
    await act(async () => { replacement.resolve({ user: fx.user, credits: fx.credits }) })
    expect(result.current.user).toEqual(fx.user)
    expect(result.current.loading).toBe(false)
  })

  it('ignores disposed initial ME rejection after the StrictMode replacement has authenticated', async () => {
    let rejectDisposed!: (error: unknown) => void
    const disposed = new Promise<Awaited<ReturnType<typeof api.me>>>((_resolve, reject) => { rejectDisposed = reject })
    const replacement = pending<Awaited<ReturnType<typeof api.me>>>()
    const me = vi.spyOn(api, 'me').mockReturnValueOnce(disposed).mockReturnValueOnce(replacement.promise)
    const { result } = renderHook(() => useAuth(), { wrapper: AuthProvider, reactStrictMode: true })
    expect(me.mock.calls[0][0]?.aborted).toBe(true)
    await act(async () => { replacement.resolve({ user: fx.user, credits: fx.credits }) })
    expect(result.current.user).toEqual(fx.user)
    await act(async () => { rejectDisposed(new DOMException('Aborted', 'AbortError')) })
    expect(result.current.user).toEqual(fx.user)
    expect(result.current.credits).toEqual(fx.credits)
    expect(result.current.loading).toBe(false)
    expect(me.mock.calls[1][0]?.aborted).toBe(false)
    // The current generation still owns a real unauthorized response.
    vi.mocked(fetch).mockResolvedValue(json({ error: { code: 'unauthorized' } }, 401))
    await act(() => api.assets().catch(() => {}))
    expect(result.current.user).toBeNull()
  })

  it('rejects stale profile GET and ME revisions after a committed deletion', async () => {
    const { result } = await open()
    const old = pending<Awaited<ReturnType<typeof api.profile>>>()
    vi.spyOn(api, 'profile').mockReturnValue(old.promise)
    vi.spyOn(api, 'deleteMemory').mockResolvedValue({ profile: fx.profile({ revision: 2 }) })
    const read = result.current.refreshProfile()
    await act(() => result.current.deleteMemory(memory.id))
    await act(async () => { old.resolve({ profile: fx.profile({ revision: 1, memories: [memory] }) }); await read })
    expect(result.current.user?.profile).toMatchObject({ revision: 2, memories: [] })
    vi.mocked(api.me).mockResolvedValue({ user: { ...fx.user, profile: fx.profile({ revision: 1, memories: [memory] }) }, credits: fx.credits })
    await act(() => result.current.refresh())
    expect(result.current.user?.profile).toMatchObject({ revision: 2, memories: [] })
  })

  it('drops an old owner mutation and aborts its signal after a new login', async () => {
    const { result } = await open()
    const old = pending<Awaited<ReturnType<typeof api.confirmMemory>>>()
    const call = vi.spyOn(api, 'confirmMemory').mockReturnValue(old.promise)
    const request = { proposalId: 'cc09c736-a775-4871-8358-a0c7eeb44fba', text: 'Private.' }
    const save = result.current.confirmMemory(request)
    const bob = { ...fx.user, id: 'bob', profile: fx.profile() }
    vi.spyOn(api, 'login').mockResolvedValue({ user: bob })
    vi.mocked(api.me).mockResolvedValue({ user: bob, credits: fx.credits })
    await act(() => result.current.login('bob@example.test', 'pw'))
    expect(call.mock.calls[0][1]?.aborted).toBe(true)
    await act(async () => { old.resolve({ memory, profile: fx.profile({ revision: 1, memories: [memory] }) }); expect(await save).toBeNull() })
    expect(result.current.user?.id).toBe('bob')
    expect(result.current.user?.profile.memories).toEqual([])
    expect(result.current.memoryReceipts).toEqual({})
  })

  it('ignores an old request 401 after a different login while current 401 still signs out', async () => {
    const { result } = await open()
    const old = pending<Response>()
    vi.mocked(fetch).mockReturnValueOnce(old.promise)
    const read = api.assets().catch(() => {})
    const bob = { ...fx.user, id: 'bob' }
    vi.spyOn(api, 'login').mockResolvedValue({ user: bob })
    vi.mocked(api.me).mockResolvedValue({ user: bob, credits: fx.credits })
    await act(() => result.current.login('bob@example.test', 'pw'))
    await act(async () => { old.resolve(json({ error: { code: 'unauthorized' } }, 401)); await read })
    expect(result.current.user?.id).toBe('bob')
    vi.mocked(fetch).mockResolvedValue(json({ error: { code: 'unauthorized' } }, 401))
    await act(() => api.assets().catch(() => {}))
    expect(result.current.user).toBeNull()
  })

  it('signs out on the new session ME 401 even before React effects register its generation', async () => {
    const { result } = await open()
    vi.mocked(api.me).mockRestore()
    vi.spyOn(api, 'login').mockResolvedValue({ user: { ...fx.user, id: 'bob' } })
    vi.mocked(fetch).mockResolvedValue(json({ error: { code: 'unauthorized', message: 'Session expired.' } }, 401))
    await act(() => result.current.login('bob@example.test', 'pw'))
    expect(result.current.user).toBeNull()
  })

  it('tracks confirmed/deleted receipt state across profile refresh without restoring text from a stale mutation', async () => {
    const { result } = await open()
    const request = { proposalId: 'cc09c736-a775-4871-8358-a0c7eeb44fba', text: 'Private.' }
    vi.spyOn(api, 'confirmMemory').mockResolvedValue({ memory, profile: fx.profile({ revision: 1, memories: [memory] }) })
    await act(() => result.current.confirmMemory(request))
    expect(result.current.memoryReceipts[request.proposalId].status).toBe('confirmed')
    vi.mocked(api.me).mockResolvedValue({ user: { ...fx.user, profile: fx.profile({ revision: 2 }) }, credits: fx.credits })
    await act(() => result.current.refresh())
    expect(result.current.memoryReceipts[request.proposalId].status).toBe('deleted')
    vi.spyOn(api, 'profile').mockResolvedValue({ profile: fx.profile({ revision: 1, memories: [memory] }) })
    await act(() => result.current.refreshProfile())
    expect(result.current.memoryReceipts[request.proposalId].status).toBe('deleted')
    expect(result.current.user?.profile.memories).toEqual([])
  })
})

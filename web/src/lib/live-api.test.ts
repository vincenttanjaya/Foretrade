import { expect, it, vi } from 'vitest'
import { api, API_URL, ApiError } from './api'
import { json } from '../test/utils'

it('reads the bare live board and encoded descending step pages with zero cursor and abort signals', async () => {
  const signal = new AbortController().signal
  vi.mocked(fetch).mockResolvedValue(json({ desk: { status: 'starting' }, agents: [], benchmarks: [], myPlans: [] }))
  await expect(api.liveArena(signal)).resolves.toMatchObject({ desk: { status: 'starting' } })
  expect(fetch).toHaveBeenLastCalledWith(`${API_URL}/api/arena/live`, { method: 'GET', credentials: 'include', signal })
  vi.mocked(fetch).mockResolvedValue(json({ steps: [] }))
  await expect(api.liveArenaSteps('a/b', { limit: 20, before: 0 }, signal)).resolves.toEqual({ steps: [] })
  expect(fetch).toHaveBeenLastCalledWith(`${API_URL}/api/arena/live/agents/a%2Fb/steps?limit=20&before=0`, { method: 'GET', credentials: 'include', signal })
})
it('creates and stops encoded owned copy plans with exact payload and nullable portfolio', async () => {
  vi.mocked(fetch).mockResolvedValue(json({ plan: { id: 'p1' } }, 201))
  await expect(api.copyLiveAgent('a/b', { ratio: 0.1, maxUsd: 50000 })).resolves.toMatchObject({ plan: { id: 'p1' } })
  expect(fetch).toHaveBeenLastCalledWith(`${API_URL}/api/arena/live/agents/a%2Fb/copy`, expect.objectContaining({ method: 'POST', credentials: 'include', headers: { 'Content-Type': 'application/json' }, body: '{"ratio":0.1,"maxUsd":50000}' }))
  const signal = new AbortController().signal
  vi.mocked(fetch).mockResolvedValue(json({ plans: [] }))
  await expect(api.copyPlans(signal)).resolves.toEqual({ plans: [] })
  expect(fetch).toHaveBeenLastCalledWith(`${API_URL}/api/copy-plans`, { method: 'GET', credentials: 'include', signal })
  vi.mocked(fetch).mockResolvedValue(json({ plan: { status: 'stopped' }, trades: [], portfolio: null }))
  await expect(api.stopCopyPlan('p/1', true)).resolves.toMatchObject({ portfolio: null, trades: [] })
  expect(fetch).toHaveBeenLastCalledWith(`${API_URL}/api/copy-plans/p%2F1/stop`, expect.objectContaining({ body: '{"closePositions":true}' }))
})
it('preserves the actual pause/autopilot conflict reason', async () => {
  vi.mocked(fetch).mockResolvedValue(json({ error: { code: 'conflict', message: 'The live desk is paused on this server.' } }, 409))
  await expect(api.copyLiveAgent('a', { ratio: 1, maxUsd: 100 })).rejects.toEqual(new ApiError(409, 'conflict', 'The live desk is paused on this server.'))
})

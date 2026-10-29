import { describe, expect, it, vi } from 'vitest'
import { api, API_URL } from './api'
import { json } from '../test/utils'
import * as fx from '../test/fixtures'

describe('typed Profile transport', () => {
  it('uses exact cookie, CSRF, method, bodies and responses, including DELETE JSON', async () => {
    const signal = new AbortController().signal
    vi.mocked(fetch).mockResolvedValueOnce(json({ profile: fx.profile() }))
    expect(await api.profile(signal)).toEqual({ profile: fx.profile() })
    expect(fetch).toHaveBeenLastCalledWith(API_URL + '/api/me/profile', { method: 'GET', credentials: 'include', signal })
    const preferences = { enabled: true, riskProfile: 'balanced' as const, style: 'day' as const, focusSymbols: ['NVDA'], customPrompt: '🧭', revision: 3 }
    vi.mocked(fetch).mockResolvedValueOnce(json({ profile: fx.profile({ ...preferences, revision: 4 }) }))
    await api.updateProfile(preferences, signal)
    expect(fetch).toHaveBeenLastCalledWith(API_URL + '/api/me/profile', { method: 'PUT', credentials: 'include', signal, headers: { 'Content-Type': 'application/json' }, body: '{"enabled":true,"riskProfile":"balanced","style":"day","focusSymbols":["NVDA"],"customPrompt":"🧭","revision":3}' })
    const proposal = { proposalId: 'cc09c736-a775-4871-8358-a0c7eeb44fba', text: 'Prefer liquid assets.' }
    const memory = { id: 'c2d75208-a9a8-447e-a6c7-5f4fa161b6c0', text: proposal.text, at: '' }
    vi.mocked(fetch).mockResolvedValueOnce(json({ memory, profile: fx.profile({ revision: 5, memories: [memory] }) }, 201))
    expect(await api.confirmMemory(proposal, signal)).toMatchObject({ memory, profile: { revision: 5 } })
    expect(fetch).toHaveBeenLastCalledWith(API_URL + '/api/me/profile/memories', { method: 'POST', credentials: 'include', signal, headers: { 'Content-Type': 'application/json' }, body: '{"proposalId":"cc09c736-a775-4871-8358-a0c7eeb44fba","text":"Prefer liquid assets."}' })
    vi.mocked(fetch).mockResolvedValueOnce(json({ profile: fx.profile({ revision: 6 }) }))
    expect(await api.deleteMemory('memory / id', signal)).toEqual({ profile: fx.profile({ revision: 6 }) })
    expect(fetch).toHaveBeenLastCalledWith(API_URL + '/api/me/profile/memories/memory%20%2F%20id', { method: 'DELETE', credentials: 'include', signal, headers: { 'Content-Type': 'application/json' }, body: '{}' })
  })
})

import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { request } from 'node:http'
import { DEMO, startMockApi } from './mock-api'
import { parseSSE } from '../src/lib/api'
import type { MemoryProposal, Profile } from '../src/lib/types'

let url = ''
let stop: () => Promise<void>
beforeAll(async () => {
  const server = await startMockApi(0)
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('Missing loopback listener')
  url = `http://127.0.0.1:${address.port}`
  stop = () => new Promise<void>((done) => server.close(() => done()))
})
afterAll(async () => { await stop?.() })
const call = (path: string, cookie = '', body?: unknown, method = body === undefined ? 'GET' : 'POST') => new Promise<{ status: number; raw: string; body: any; cookie: string }>((resolve, reject) => {
  const payload = body === undefined ? undefined : JSON.stringify(body)
  const req = request(url + path, { agent: false, method, headers: { Cookie: cookie, 'Content-Type': 'application/json', ...(payload ? { 'Content-Length': Buffer.byteLength(payload) } : {}) } }, (res) => {
    let raw = ''
    res.on('data', (chunk) => { raw += chunk })
    res.on('end', () => { let parsed; try { parsed = JSON.parse(raw) } catch { parsed = null }; resolve({ status: res.statusCode!, raw, body: parsed, cookie: res.headers['set-cookie']?.[0]?.split(';')[0] ?? cookie }) })
  })
  req.on('error', reject)
  req.end(payload)
})
const login = async () => (await call('/api/auth/login', '', DEMO)).cookie
const prefs = (profile: Profile) => ({ enabled: profile.enabled, riskProfile: profile.riskProfile, style: profile.style, focusSymbols: profile.focusSymbols, customPrompt: profile.customPrompt, revision: profile.revision })
async function remember(cookie: string, text = 'Prefer liquid assets.') {
  const stream = await call('/api/chat/stream', cookie, { message: `Remember ${text}` })
  const frames = parseSSE(stream.raw).frames.map((f) => ({ event: f.event, data: JSON.parse(f.data) }))
  const proposal = frames.find((f) => f.event === 'card' && f.data.type === 'memory')?.data.data as MemoryProposal
  return { frames, proposal, chatId: frames.find((f) => f.event === 'chat')!.data.chatId }
}

describe('reviewed Profile contract over real mock HTTP', () => {
  it('serves the initialized demo profile and updates only explicit preferences with a revision', async () => {
    const cookie = await login()
    const get = await call('/api/me/profile', cookie)
    expect(get.status).toBe(200)
    expect(get.body.profile).toMatchObject({ enabled: false, riskProfile: null, style: null, memories: [], revision: 0 })
    const save = await call('/api/me/profile', cookie, { ...prefs(get.body.profile), enabled: true, riskProfile: 'conservative', style: 'swing', focusSymbols: ['nvda', 'BTC'], customPrompt: '🧭'.repeat(600) }, 'PUT')
    expect(save.status).toBe(200)
    expect(save.body.profile).toMatchObject({ enabled: true, riskProfile: 'conservative', style: 'swing', focusSymbols: ['NVDA', 'BTC'], revision: 1 })
    expect((await call('/api/me', cookie)).body.user.profile).toEqual(save.body.profile)
    expect((await call('/api/me/profile', cookie, prefs(get.body.profile), 'PUT')).status).toBe(409)
    expect((await call('/api/me/profile', cookie, { ...prefs(save.body.profile), memories: [] }, 'PUT')).status).toBe(400)
    expect((await call('/api/me/profile', cookie, { ...prefs(save.body.profile), customPrompt: '🧭'.repeat(601) }, 'PUT')).status).toBe(400)
  })

  it('makes remember proposal-only, binds durable owned assistant text, and retains receipts through deletion/reset/chat deletion', async () => {
    const cookie = await login(), foreign = await login()
    const result = await remember(cookie)
    expect(result.proposal).toMatchObject({ text: 'Prefer liquid assets.', status: 'open' })
    expect(result.frames.filter((f) => f.event === 'tool_call').map((f) => f.data.name)).toEqual(['remember'])
    expect(result.frames.at(-1)).toMatchObject({ event: 'done', data: { credits: { used: 20 } } })
    expect((await call('/api/me/profile', cookie)).body.profile.memories).toEqual([])
    const body = { proposalId: result.proposal.proposalId, text: result.proposal.text }
    expect((await call('/api/me/profile/memories', foreign, body)).status).toBe(404)
    expect((await call('/api/me/profile/memories', cookie, { ...body, text: 'Altered.' })).status).toBe(400)
    const confirm = await call('/api/me/profile/memories', cookie, body)
    expect(confirm.status).toBe(201)
    expect(confirm.body.profile).toMatchObject({ enabled: false, revision: 1, memories: [confirm.body.memory] })
    const restored = (await call('/api/chats/' + result.chatId, cookie)).body.chat.messages.at(-1).cards[0].data
    expect(restored).toMatchObject({ status: 'confirmed', memoryId: confirm.body.memory.id })
    expect((await call('/api/me/profile/memories', cookie, body)).status).toBe(409)
    expect((await call('/api/me/profile/memories/' + confirm.body.memory.id, foreign, {}, 'DELETE')).status).toBe(404)
    const deleted = await call('/api/me/profile/memories/' + confirm.body.memory.id, cookie, {}, 'DELETE')
    expect(deleted.status).toBe(200)
    expect(deleted.body.profile).toMatchObject({ revision: 2, memories: [] })
    expect((await call('/api/chats/' + result.chatId, cookie)).body.chat.messages.at(-1).cards[0].data.status).toBe('deleted')
    await call('/api/portfolio/reset', cookie, {})
    expect((await call('/api/me/profile', cookie)).body.profile.revision).toBe(2)
    await call('/api/chats/' + result.chatId, cookie, {}, 'DELETE')
    expect((await call('/api/me/profile/memories', cookie, body)).status).toBe(409)
    expect((await call('/api/me', cookie)).body.credits.used).toBe(20)
  })

  it('rejects a current streamed card before persistence and allows it after real done', async () => {
    const cookie = await login()
    await call('/__e2e/profile-fixture', cookie, { scenario: 'slow' })
    let early: Promise<{ status: number }> | undefined
    let proposal!: MemoryProposal
    await new Promise<void>((resolve, reject) => {
      const req = request(url + '/api/chat/stream', { agent: false, method: 'POST', headers: { Cookie: cookie, 'Content-Type': 'application/json' } }, (res) => {
        let raw = ''
        res.on('data', (chunk) => {
          raw += chunk
          const frames = parseSSE(raw).frames
          const frame = frames.find((f) => f.event === 'card')
          if (frame && !early) {
            proposal = JSON.parse(frame.data).data
            early = call('/api/me/profile/memories', cookie, { proposalId: proposal.proposalId, text: proposal.text })
          }
        })
        res.on('end', resolve)
      })
      req.on('error', reject)
      req.end(JSON.stringify({ message: 'Remember Only a completed answer can be confirmed.' }))
    })
    expect((await early)?.status).toBe(404)
    expect((await call('/api/me/profile/memories', cookie, { proposalId: proposal.proposalId, text: proposal.text })).status).toBe(201)
  })

  it('enforces assistant-role ownership and 20 capacity without changing preferences or credits on conflict', async () => {
    const cookie = await login()
    const userCard = (await call('/__e2e/profile-fixture', cookie, { scenario: 'user-card' })).body.proposal
    expect((await call('/api/me/profile/memories', cookie, { proposalId: userCard.proposalId, text: userCard.text })).status).toBe(404)
    await call('/__e2e/profile-fixture', cookie, { scenario: 'capacity' })
    const result = await remember(cookie, '🧭'.repeat(200))
    const before = (await call('/api/me/profile', cookie)).body.profile
    const body = { proposalId: result.proposal.proposalId, text: result.proposal.text }
    expect((await call('/api/me/profile/memories', cookie, body)).status).toBe(409)
    expect((await call('/api/me/profile', cookie)).body.profile).toEqual(before)
    expect((await call('/api/me/profile/memories', cookie, { ...body, text: '🧭'.repeat(201) })).status).toBe(400)
    await call('/api/me/profile/memories/' + before.memories[0].id, cookie, {}, 'DELETE')
    expect((await call('/api/me/profile/memories', cookie, body)).status).toBe(201)
    expect((await call('/api/me/profile', cookie)).body.profile.memories).toHaveLength(20)
    expect((await call('/api/me', cookie)).body.credits.used).toBe(20)
  })
})

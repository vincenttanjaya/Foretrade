// Research browser fixtures exercise real authenticated mock HTTP routes; no jobs or models run.
import { request as httpRequest } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { DEMO, startMockApi } from './mock-api'
import { parseSSE } from '../src/lib/api'

let origin = ''
let close: (() => Promise<void>) | undefined
beforeAll(async () => {
  const server = await startMockApi(0)
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('Missing mock listener address')
  origin = `http://127.0.0.1:${address.port}`
  close = () => new Promise<void>((resolve) => server.close(() => resolve()))
})
afterAll(async () => { await close?.() })

async function client() {
  let cookie = ''
  async function call(path: string, data?: unknown, method = data === undefined ? 'GET' : 'POST') {
    return new Promise<{ status: number; body: any; raw: string }>((resolve, reject) => {
      const req = httpRequest(origin + path, { agent: false, method, headers: { Cookie: cookie, 'Content-Type': 'application/json' } }, (res) => {
        let raw = ''
        res.on('data', (chunk) => { raw += chunk })
        res.on('end', () => {
          cookie = res.headers['set-cookie']?.[0]?.split(';')[0] ?? cookie
          const body = res.headers['content-type']?.includes('application/json') && raw ? JSON.parse(raw) : null
          resolve({ status: res.statusCode!, body, raw })
        })
      })
      req.on('error', reject)
      req.end(data === undefined ? undefined : JSON.stringify(data))
    })
  }
  expect((await call('/api/auth/login', DEMO)).status).toBe(200)
  return call
}

const draft = { trigger: 'schedule', every: 'weekdays', at: '08:00', action: 'research', prompt: 'Review market catalysts with current sources.' }
const thesis = { statement: 'BTC demand supports the uptrend.', invalidation: 'ETF demand reverses for a week.' }
const bells = (body: any) => body.items.filter((item: any) => item.kind === 'workflow_research')

describe('research mock contracts', () => {
  it('creates general research for free with canonical nullable fields and Unicode bounds', async () => {
    const call = await client()
    const before = (await call('/api/me')).body.credits.used
    const response = await call('/api/alerts', { ...draft, prompt: '📈'.repeat(500) })
    expect(response.status).toBe(201)
    expect(response.body.alert).toMatchObject({ symbol: '', action: 'research', prompt: '📈'.repeat(500), thesis: null, notifyWhen: 'always', quotaStreak: 0, every: 'weekdays', lastRun: null, notional: null })
    expect((await call('/api/me')).body.credits.used).toBe(before)
    for (const prompt of ['📈'.repeat(9), '📈'.repeat(501)]) expect((await call('/api/alerts', { ...draft, prompt })).status).toBe(400)
  })

  it('rejects mixed research shapes and enforces integrated assets, thesis bounds and UUID receipts', async () => {
    const call = await client()
    for (const extra of [{ symbol: 'NOTREAL' }, { trigger: 'price', symbol: 'BTC', op: 'above', price: 90000 }, { trigger: 'odds', forecastId: 'f-seed', op: 'above', price: .6 }, { notional: 10 }, { stopLoss: 20 }, { takeProfit: 40 }, { price: 0 }, { op: 'below' }, { thesis: { ...thesis, statement: '📈'.repeat(301) } }, { notifyWhen: 'sometimes' }, { proposalId: 'not-a-uuid' }]) {
      expect((await call('/api/alerts', { ...draft, ...extra })).status, JSON.stringify(extra)).toBe(400)
    }
    const created = await call('/api/alerts', { ...draft, symbol: 'NVDA', thesis: { ...thesis, statement: '📈'.repeat(300) }, proposalId: '00000000-0000-4000-8000-000000000001' })
    expect(created.status).toBe(201)
    expect((await call('/api/alerts', { ...draft, proposalId: '00000000-0000-4000-8000-000000000001' })).status).toBe(409)
  })

  it('applies the three research and shared twenty active limits across Create and Resume', async () => {
    const call = await client()
    const paused = (await call('/__e2e/research-fixture', { scenario: 'paused' })).body.alert
    for (let i = 0; i < 3; i++) expect((await call('/api/alerts', draft)).status).toBe(201)
    expect((await call('/api/alerts', draft)).status).toBe(409)
    expect((await call(`/api/alerts/${paused.id}/resume`, {})).status).toBe(409)
    for (let i = 0; i < 17; i++) expect((await call('/api/alerts', { trigger: 'schedule', symbol: 'BTC', every: 'day', at: '09:00' })).status).toBe(201)
    expect((await call('/api/alerts', { symbol: 'SOL', op: 'below', price: 100 })).status).toBe(409)
  })

  it('advances a due research slot once, charges only its owner and publishes a private full report with sources', async () => {
    const call = await client(), foreign = await client()
    const alert = (await call('/api/alerts', draft)).body.alert
    const before = (await call('/api/me')).body.credits.used
    const answer = '**Current evidence** [1]. ' + '📈'.repeat(400)
    const first = await call('/__e2e/research-step', { id: alert.id, at: alert.nextRunAt, answer })
    expect(first.status).toBe(200)
    const run = first.body.alert.lastRun
    expect(run).toMatchObject({ status: 'ok', verdict: null, error: null })
    expect([...run.body]).toHaveLength(300)
    expect([...run.excerpt]).toHaveLength(160)
    expect((await call('/api/me')).body.credits.used).toBe(before + 2)
    expect((await foreign('/api/me')).body.credits.used).toBe(before)
    const report = (await call(`/api/chats/${run.reportChatId}`)).body.chat
    expect(report.messages.at(-1).content).toBe(answer)
    expect(report.messages.at(-1).cards).toEqual([expect.objectContaining({ type: 'sources', data: { items: expect.arrayContaining([expect.objectContaining({ n: 1 })]) } })])
    expect((await foreign(`/api/chats/${run.reportChatId}`)).status).toBe(404)
    expect((await foreign(`/api/alerts/${alert.id}/runs`)).status).toBe(404)
    await call('/__e2e/research-step', { id: alert.id, at: alert.nextRunAt, answer })
    expect((await call('/api/me')).body.credits.used).toBe(before + 2)
    expect((await call(`/api/alerts/${alert.id}/runs`)).body.runs).toHaveLength(1)
    expect(bells((await call('/api/notifications')).body)).toHaveLength(1)
  })

  it('records quota refusals without spending or failure bells, pauses after three and resumes without charging', async () => {
    const call = await client()
    let alert = (await call('/api/alerts', draft)).body.alert
    const before = (await call('/api/me')).body.credits.used
    await call('/__e2e/research-fixture', { scenario: 'credits', remaining: 0 })
    for (let i = 1; i <= 3; i++) {
      alert = (await call('/__e2e/research-step', { id: alert.id, at: alert.nextRunAt })).body.alert
      expect(alert).toMatchObject({ quotaStreak: i, status: i === 3 ? 'paused' : 'active', lastRun: { status: 'failed', error: 'Out of AI credits today' } })
    }
    expect((await call('/api/me')).body.credits.used).toBe(before)
    expect(bells((await call('/api/notifications')).body)).toEqual([])
    expect((await call(`/api/alerts/${alert.id}/resume`, {})).status).toBe(429)
    await call('/__e2e/research-fixture', { scenario: 'credits', remaining: 2 })
    expect((await call(`/api/alerts/${alert.id}/resume`, {})).body.alert).toMatchObject({ status: 'active', quotaStreak: 0 })
    expect((await call('/api/me')).body.credits.used).toBe(before)
    expect((await call(`/api/alerts/${alert.id}/resume`, {})).status).toBe(409)
    expect((await call(`/api/alerts/${alert.id}`, undefined, 'DELETE')).status).toBe(204)
  })

  it('keeps unknown thesis and failed runs out of the changed baseline and persists Bell eligibility', async () => {
    const call = await client()
    let alert = (await call('/api/alerts', { ...draft, symbol: 'BTC', thesis, notifyWhen: 'changed' })).body.alert
    const step = async (answer: string, error?: string) => { alert = (await call('/__e2e/research-step', { id: alert.id, at: alert.nextRunAt, answer, error })).body.alert; return alert.lastRun }
    await step('Evidence stable [1].\n\nVerdict: holds — demand persists')
    await step('Evidence stable [1].\n\nVerdict: holds — demand persists')
    expect(bells((await call('/api/notifications')).body)).toHaveLength(1)
    expect((await step('Unparseable evidence')).verdict).toBe('unknown')
    await step('', 'Controlled report failure')
    await step('Evidence stable [1].\n\nVerdict: holds — demand persists')
    expect(bells((await call('/api/notifications')).body)).toHaveLength(2) // unknown success is changed, but is not the baseline
    await step('Demand reversed [1].\n\nVerdict: invalidated — persistent outflows')
    expect(bells((await call('/api/notifications')).body)).toHaveLength(3)
  })

  it('notifies invalidation transitions once and recreates deleted report chats on the next success', async () => {
    const call = await client()
    let alert = (await call('/api/alerts', { ...draft, symbol: 'BTC', thesis, notifyWhen: 'invalidated' })).body.alert
    const step = async (verdict: string) => { alert = (await call('/__e2e/research-step', { id: alert.id, at: alert.nextRunAt, answer: `Evidence [1].\n\nVerdict: ${verdict} — fixture` })).body.alert }
    await step('holds'); await step('invalidated'); await step('invalidated')
    expect(bells((await call('/api/notifications')).body)).toHaveLength(1)
    const deleted = alert.lastRun.reportChatId
    await call(`/api/chats/${deleted}`, undefined, 'DELETE')
    expect((await call(`/api/alerts/${alert.id}/runs`)).body.runs.every((r: any) => r.reportChatId === null)).toBe(true)
    expect(bells((await call('/api/notifications')).body)).toEqual([])
    await step('holds'); await step('invalidated')
    expect(alert.lastRun.reportChatId).not.toBe(deleted)
    expect(bells((await call('/api/notifications')).body)).toHaveLength(1)
  })

  it('bounds own run history to thirty days with exclusive opaque pages and validates limits/cursors', async () => {
    const call = await client(), foreign = await client()
    const alert = (await call('/__e2e/research-fixture', { scenario: 'history' })).body.alert
    const page = (await call(`/api/alerts/${alert.id}/runs`)).body
    expect(page.runs).toHaveLength(20)
    expect(page.nextCursor).toEqual(expect.any(String))
    const next = (await call(`/api/alerts/${alert.id}/runs?before=${encodeURIComponent(page.nextCursor)}`)).body
    expect(next.runs).toHaveLength(5)
    expect(next.nextCursor).toBeNull()
    expect(new Set([...page.runs, ...next.runs].map((r: any) => r.id)).size).toBe(25)
    expect((await call(`/api/alerts/${alert.id}/runs?limit=50`)).body.runs).toHaveLength(25)
    for (const query of ['limit=0', 'limit=51', 'limit=1.5', 'before=invalid', 'before=']) expect((await call(`/api/alerts/${alert.id}/runs?${query}`)).status).toBe(400)
    expect((await foreign(`/api/alerts/${alert.id}/runs`)).status).toBe(404)
    expect((await foreign(`/api/alerts/${alert.id}/resume`, {})).status).toBe(404)
  })

  it('scripted set_schedule and track_thesis turns only propose an ordinary once-confirmed research card', async () => {
    const call = await client()
    for (const message of ['Schedule research every weekday', 'Track my BTC thesis every Monday']) {
      const turn = await call('/api/chat/stream', { message })
      const events = parseSSE(turn.raw).frames.map((frame) => ({ event: frame.event, data: JSON.parse(frame.data) }))
      const card = events.find((e) => e.event === 'card' && e.data.type === 'price_alert')!.data.data
      expect(card).toMatchObject({ trigger: 'schedule', action: 'research', notional: null, notifyWhen: 'always' })
      expect(events.find((e) => e.event === 'delta')!.data.text).toContain('2 AI credits per run')
      expect((await call('/api/alerts')).body.alerts).toHaveLength(message.startsWith('Schedule') ? 0 : 1)
      expect((await call('/api/alerts', { ...card, source: 'agent' })).status).toBe(201)
      expect((await call('/api/alerts', { ...card, source: 'agent' })).status).toBe(409)
    }
  })

  it('preserves zero-credit weekday briefs and cancellation of paused schedules', async () => {
    const call = await client()
    const before = (await call('/api/me')).body.credits.used
    const alert = (await call('/api/alerts', { symbol: 'BTC', trigger: 'schedule', every: 'weekdays', at: '08:00' })).body.alert
    const result = await call('/__e2e/research-step', { id: alert.id, at: alert.nextRunAt })
    expect(result.body.alert.lastRun).toMatchObject({ status: 'ok', verdict: null, reportChatId: null })
    expect((await call('/api/me')).body.credits.used).toBe(before)
    expect((await call('/api/notifications')).body.items.filter((n: any) => n.kind === 'workflow')).toHaveLength(1)
    const paused = (await call('/__e2e/research-fixture', { scenario: 'paused' })).body.alert
    expect((await call(`/api/alerts/${paused.id}`, undefined, 'DELETE')).status).toBe(204)
    expect((await call(`/api/alerts/${paused.id}/resume`, {})).status).toBe(409)
  })

  it('keeps deterministic scheduled order Bells and cent-rounded notionals without AI credits', async () => {
    const call = await client()
    await call('/__e2e/research-fixture', { scenario: 'credits', remaining: 0 })
    const before = (await call('/api/me')).body.credits.used
    let alert = (await call('/api/alerts', { symbol: 'BTC', trigger: 'schedule', every: 'weekdays', at: '08:00', action: 'buy', notional: 100.126 })).body.alert
    expect(alert.notional).toBe(100.13)
    alert = (await call('/__e2e/research-step', { id: alert.id, at: alert.nextRunAt })).body.alert
    expect(alert.lastRun.status).toBe('ok')
    expect((await call('/api/me')).body.credits.used).toBe(before)
    const notifications = (await call('/api/notifications')).body.items
    expect(notifications.filter((n: any) => n.kind === 'workflow_order')).toHaveLength(1)
    expect(notifications.filter((n: any) => n.kind === 'workflow')).toHaveLength(0)
  })
})

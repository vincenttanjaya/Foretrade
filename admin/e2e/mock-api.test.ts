// Unit tests (Vitest) for the e2e mock API, over HTTP: the Playwright suite trusts it to behave like the contract.
import type { Server } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import type { AdminOverview, AdminUser, AdminUserDetail, AuditList, Settings, UserList } from '../src/lib/types'
import { ADMIN, DEMO, OPS, portOf, PRESETS, SEED, startMockApi, WEB_URL } from './mock-api'

const realFetch = globalThis.fetch // setup.ts stubs fetch per test; this runs before that
let server: Server
let base = ''
beforeAll(async () => {
  server = await startMockApi(0)
  base = `http://127.0.0.1:${portOf(server)}`
})
afterAll(() => new Promise<void>((resolve) => server.close(() => resolve())))

type Reply = { status: number; body: any; headers: Headers }

async function call(method: string, path: string, opts: { cookie?: string; body?: unknown; headers?: Record<string, string> } = {}): Promise<Reply> {
  const headers: Record<string, string> = { ...(opts.cookie && { Cookie: opts.cookie }), ...opts.headers }
  if (method !== 'GET' && !('Content-Type' in headers)) headers['Content-Type'] = 'application/json'
  const res = await realFetch(base + path, { method, headers, body: method === 'GET' ? undefined : JSON.stringify(opts.body ?? {}) })
  const text = await res.text()
  return { status: res.status, body: text ? JSON.parse(text) : undefined, headers: res.headers }
}

async function signIn(who = ADMIN) {
  const r = await call('POST', '/api/auth/login', { body: who })
  expect(r.status).toBe(200)
  return /tsid=[^;]+/.exec(r.headers.get('set-cookie') ?? '')![0]
}

describe('mock API: auth and access', () => {
  it('listens on loopback only, like the web mock', () => {
    expect(server.address()).toMatchObject({ address: '127.0.0.1' })
  })

  it('signs in, answers /api/me, and rejects wrong or missing credentials', async () => {
    const cookie = await signIn()
    expect((await call('GET', '/api/me', { cookie })).body.user).toEqual({ id: SEED.admin, email: ADMIN.email, name: 'Admin', role: 'admin' })
    expect((await call('POST', '/api/auth/login', { body: { ...ADMIN, password: 'nope-nope-nope' } })).body.error.code).toBe('invalid_credentials')
    expect((await call('GET', '/api/admin/overview')).status).toBe(401)
    expect((await call('POST', '/api/auth/logout', { cookie })).status).toBe(204)
    expect((await call('GET', '/api/me', { cookie })).status).toBe(401)
  })

  it('answers 403 "Admins only." to a non-admin, and no-store on admin routes', async () => {
    const cookie = await signIn(DEMO)
    const r = await call('GET', '/api/admin/users', { cookie })
    expect(r).toMatchObject({ status: 403, body: { error: { code: 'forbidden', message: 'Admins only.' } } })
    expect(r.headers.get('cache-control')).toBe('no-store')
  })

  it('enforces the CSRF rule and CORS for the console origin', async () => {
    const cookie = await signIn()
    expect((await call('POST', `/api/admin/users/${SEED.demo}/reset-credits`, { cookie, headers: { 'Content-Type': 'text/plain' } })).status).toBe(403)
    expect((await call('POST', `/api/admin/users/${SEED.demo}/reset-credits`, { cookie, headers: { Origin: 'https://evil.example' } })).status).toBe(403)
    const ok = await call('GET', '/api/me', { cookie, headers: { Origin: WEB_URL } })
    expect(ok.headers.get('access-control-allow-origin')).toBe(WEB_URL)
    expect(ok.headers.get('access-control-allow-credentials')).toBe('true')
  })
})

describe('mock API: users', () => {
  it('filters, sorts with nulls last, pages and validates the list query', async () => {
    const cookie = await signIn()
    const all: UserList = (await call('GET', '/api/admin/users', { cookie })).body
    expect(all).toMatchObject({ total: 31, page: 1, limit: 25, defaultCreditLimit: 500 })
    expect(all.users).toHaveLength(25)
    const page2: UserList = (await call('GET', '/api/admin/users?page=2', { cookie })).body
    expect(page2.users).toHaveLength(6)
    expect((await call('GET', '/api/admin/users?page=9', { cookie })).body).toMatchObject({ users: [], total: 31 })

    const admins: UserList = (await call('GET', '/api/admin/users?role=admin&sort=email&order=asc', { cookie })).body
    expect(admins.users.map((u) => u.email)).toEqual([ADMIN.email, OPS.email])
    const disabled: UserList = (await call('GET', '/api/admin/users?status=disabled', { cookie })).body
    expect(disabled.users.every((u) => u.status === 'disabled')).toBe(true)
    expect(disabled.total).toBeGreaterThan(0)
    const search: UserList = (await call('GET', '/api/admin/users?q=DEMO', { cookie })).body
    expect(search.users.map((u) => u.email)).toEqual([DEMO.email])

    const created = await call('POST', '/api/admin/users', { cookie, body: { email: 'new@example.com', name: 'New' } })
    for (const order of ['asc', 'desc']) {
      const sorted: UserList = (await call('GET', `/api/admin/users?sort=lastLoginAt&order=${order}&limit=100`, { cookie })).body
      expect(sorted.users.at(-1)!.id).toBe(created.body.user.id) // never signed in: last both ways
    }
    for (const q of ['status=gone', 'role=root', 'sort=password', 'order=up', 'page=0', 'limit=101', `q=${'x'.repeat(101)}`])
      expect((await call('GET', `/api/admin/users?${q}`, { cookie })).status, q).toBe(400)
  })

  it('creates a user, returns a generated password once, and audits it without the password', async () => {
    const cookie = await signIn()
    const r = await call('POST', '/api/admin/users', { cookie, body: { email: '  Ann@Example.com ', name: ' Ann ', role: 'admin', creditLimit: 50 } })
    expect(r.status).toBe(201)
    expect(r.body.user).toMatchObject({ email: 'ann@example.com', name: 'Ann', role: 'admin', status: 'active', creditLimit: 50, sessions: 0, lastLoginAt: null })
    expect(r.body.password).toMatch(/^[A-Z2-7]{26}$/)
    const typed = await call('POST', '/api/admin/users', { cookie, body: { email: 'bo@example.com', name: 'Bo', password: 'correct horse battery' } })
    expect(typed.body.password).toBeUndefined()
    const log: AuditList = (await call('GET', '/api/admin/audit', { cookie })).body
    expect(log.entries.map((e) => [e.action, e.detail])).toEqual([
      ['user.create', { role: 'user', creditLimit: null, passwordGenerated: false }],
      ['user.create', { role: 'admin', creditLimit: 50, passwordGenerated: true }],
    ])
    expect(JSON.stringify(log)).not.toContain(r.body.password)
    expect(JSON.stringify(log)).not.toContain('correct horse battery')

    expect((await call('POST', '/api/admin/users', { cookie, body: { email: 'ann@example.com', name: 'Again' } })).body.error).toEqual({
      code: 'conflict',
      message: 'An account with this email already exists.',
    })
    for (const body of [
      { email: 'Ann <ann@x.co>', name: 'A' },
      { email: 'nodot@localhost', name: 'A' },
      { email: 'a@b.co', name: '' },
      { email: 'a@b.co', name: 'A', role: 'root' },
      { email: 'a@b.co', name: 'A', creditLimit: 1.5 },
      { email: 'a@b.co', name: 'A', creditLimit: 100_001 },
      { email: 'a@b.co', name: 'A', password: 'short' },
      { email: 'a@b.co', name: 'A', password: ' '.repeat(12) },
    ])
      expect((await call('POST', '/api/admin/users', { cookie, body })).status, JSON.stringify(body)).toBe(400)
  })

  it('PATCH changes only what differs, audits per kind, and writes nothing for a no-op', async () => {
    const cookie = await signIn()
    const path = `/api/admin/users/${SEED.demo}`
    expect((await call('PATCH', path, { cookie, body: {} })).status).toBe(400)
    await call('PATCH', path, { cookie, body: { name: 'Demo Investor', creditLimit: null } }) // unchanged
    expect((await call('GET', '/api/admin/audit', { cookie })).body.total).toBe(0)

    const r = await call('PATCH', path, { cookie, body: { name: 'Demo 2', creditLimit: 0, disabled: true } })
    expect(r.body.user).toMatchObject({ name: 'Demo 2', creditLimit: 0, status: 'disabled', sessions: 0 })
    const d: AdminUserDetail = (await call('GET', path, { cookie })).body
    expect(d.audit.map((e) => [e.action, e.detail])).toEqual([
      ['user.update', { name: { from: 'Demo Investor', to: 'Demo 2' }, creditLimit: { from: null, to: 0 } }],
      ['user.disable', { sessionsRevoked: 2 }],
    ])
    expect((await call('PATCH', path, { cookie, body: { disabled: false } })).body.user.status).toBe('active')
    expect((await call('GET', `/api/admin/users/${'0'.repeat(8)}`, { cookie })).status).toBe(404)
  })

  it('applies the 409 safety rules', async () => {
    const cookie = await signIn()
    const self = `/api/admin/users/${SEED.admin}`
    const SELF = "You can't disable, demote or delete your own account."
    expect((await call('PATCH', self, { cookie, body: { disabled: true } })).body.error.message).toBe(SELF)
    expect((await call('PATCH', self, { cookie, body: { role: 'user' } })).body.error.message).toBe(SELF)
    expect((await call('DELETE', self, { cookie })).body.error.message).toBe(SELF)
    expect((await call('POST', `/api/admin/users/${SEED.demo}/password`, { cookie })).body.error).toEqual({
      code: 'conflict',
      message: "This account's password comes from the server config. Rotate it there and redeploy.",
    })
    // Demoting the other admin is fine (the caller remains)…
    expect((await call('PATCH', `/api/admin/users/${SEED.ops}`, { cookie, body: { role: 'user' } })).status).toBe(200)
    expect((await call('DELETE', `/api/admin/users/${SEED.ops}`, { cookie })).status).toBe(204)
    expect((await call('GET', `/api/admin/users/${SEED.ops}`, { cookie })).status).toBe(404)
  })

  it('sets passwords, revokes sessions (keeping the caller), resets credits and the portfolio', async () => {
    const cookie = await signIn()
    const u = (await call('GET', '/api/admin/users?q=ava', { cookie })).body.users[0] as AdminUser
    const pw = await call('POST', `/api/admin/users/${u.id}/password`, { cookie })
    expect(pw.body.password).toHaveLength(26)
    expect(pw.body.user.sessions).toBe(0)
    expect((await call('POST', `/api/admin/users/${u.id}/password`, { cookie, body: { password: 'short' } })).status).toBe(400)

    const own = await call('POST', `/api/admin/users/${SEED.admin}/sessions/revoke`, { cookie })
    expect(own.body).toMatchObject({ revoked: 0, user: { sessions: 1 } }) // this session stays
    expect((await call('GET', '/api/me', { cookie })).status).toBe(200)

    const demo = `/api/admin/users/${SEED.demo}`
    const before = ((await call('GET', demo, { cookie })).body as AdminUserDetail).user.creditsToday
    expect((await call('POST', `${demo}/reset-credits`, { cookie })).body.user.creditsToday).toBe(0)
    const reset = await call('POST', `${demo}/reset-portfolio`, { cookie })
    expect(reset.body.portfolio).toMatchObject({ cash: 100_000, equity: 100_000, positions: [] })
    const d: AdminUserDetail = (await call('GET', demo, { cookie })).body
    expect(d.trades).toEqual([])
    expect(d.audit.map((e) => [e.action, e.detail])).toEqual([
      ['user.portfolio_reset', {}],
      ['user.credits_reset', { creditsBefore: before }],
    ])
  })

  it('builds an overview whose windows agree with their daily series', async () => {
    const cookie = await signIn()
    const o: AdminOverview = (await call('GET', '/api/admin/overview', { cookie })).body.overview
    expect(o.users).toMatchObject({ total: 31, admins: 2 })
    for (const w of [o.credits, ...o.features]) {
      expect(w.daily).toHaveLength(30)
      expect(w.today).toBe(w.daily[29].value)
      expect(w.d7).toBe(w.daily.slice(23).reduce((a: number, d: { value: number }) => a + d.value, 0))
      expect(w.d30).toBe(w.daily.reduce((a: number, d: { value: number }) => a + d.value, 0))
    }
    expect(o.features.map((f) => [f.key, f.label])).toEqual([
      ['chat_messages', 'Chat messages'], ['forecasts', 'Forecasts'], ['strategies', 'Strategies'],
      ['ai_portfolios', 'AI portfolios'], ['arena_runs', 'Arena runs'], ['orders', 'Paper orders'], ['copy_plans', 'Copy plans'],
    ])
    expect(o.topUsers.length).toBeLessThanOrEqual(10)
    expect(o.credits.daily[29].day).toBe(new Date().toISOString().slice(0, 10))
  })

  it('returns live job claims and ceilings with every required health field over JSON', async () => {
    const cookie = await signIn()
    const o: AdminOverview = (await call('GET', '/api/admin/overview', { cookie })).body.overview
    expect(o.system.jobs.liveArena).toEqual({
      enabled: true, startedAt: expect.any(String), lastStepAt: expect.any(String), nextStepAt: expect.any(String),
      agents: 3, benched: 1, copyPlans: 8, status: 'live', stale: false, lastError: null,
      attemptsToday: 3, requestCeilingToday: 12,
      maxDecisionsPerDay: 18, maxRequestsPerDay: 72, maxTokensPerRequest: 4096, decisionTimeoutSec: 90,
    })
  })

  it('rate-limits admin mutations at 60 per 10 minutes, not reads', async () => {
    const cookie = await signIn()
    for (let i = 0; i < 60; i++) expect((await call('POST', `/api/admin/users/${SEED.demo}/reset-credits`, { cookie })).status).toBe(200)
    expect((await call('POST', `/api/admin/users/${SEED.demo}/reset-credits`, { cookie })).body.error).toEqual({
      code: 'rate_limited',
      message: 'Too many admin changes. Try again in a few minutes.',
    })
    expect((await call('GET', '/api/admin/overview', { cookie })).status).toBe(200)
  })
})

describe('mock API: settings', () => {
  it('starts with AI off and the Brave key from .env, and never returns a secret', async () => {
    const cookie = await signIn()
    const s: Settings = (await call('GET', '/api/admin/settings', { cookie })).body
    expect(s.ai).toMatchObject({ provider: '', status: 'off', source: 'none', readOnly: false, apiKey: { set: false, hint: '' } })
    expect(s.search.braveApiKey).toEqual({ set: true, hint: '…c0de', source: 'env' })
    expect(s.providers.map((p) => p.id)).toEqual(['openai', 'chatgpt', 'anthropic', 'bedrock', 'openrouter', 'gemini', 'groq', 'deepseek', 'xai', 'mistral', 'ollama', 'lmstudio', 'custom'])
    expect(s.providers).toEqual(PRESETS)
    expect((await call('GET', '/api/admin/overview', { cookie })).body.overview.system.model).toBe('')
    expect((await call('GET', '/api/admin/settings', { cookie: (await signIn(DEMO)) })).status).toBe(403)
  })

  it('tests before saving, keeps a key only for the same provider, and audits field names', async () => {
    const cookie = await signIn()
    const put = (ai: object) => call('PUT', '/api/admin/settings', { cookie, body: { ai } })
    expect((await put({ provider: 'openrouter' })).body.error.message).toBe('OpenRouter needs an API key.')
    expect((await put({ provider: 'openrouter', apiKey: 'bad-key-0000000000' })).status).toBe(400)
    expect((await put({ provider: 'nope' })).status).toBe(400)
    expect((await call('POST', '/api/admin/settings/test', { cookie, body: { ai: { provider: 'openrouter', apiKey: 'bad-key' } } })).body).toEqual({
      ok: false,
      error: 'OpenRouter: 401 Unauthorized (invalid API key).',
    })
    expect((await call('POST', '/api/admin/settings/models', { cookie, body: { ai: { provider: 'openrouter', apiKey: 'sk-or-v1-0000feed' } } })).body.models).toHaveLength(3)
    expect((await call('POST', '/api/admin/settings/test', { cookie, body: { ai: { provider: 'openrouter', apiKey: 'sk-or-v1-0000feed' } } })).body.error).toBe('Pick a model.')

    const key = 'sk-or-v1-e2e-only-0000feed'
    const saved: Settings = (await put({ provider: 'openrouter', model: 'qwen/qwen4-72b', apiKey: key })).body
    expect(saved.ai).toMatchObject({ provider: 'openrouter', status: 'ready', apiKey: { set: true, hint: '…feed' } })
    expect(JSON.stringify(saved)).not.toContain(key)
    expect((await call('POST', '/api/admin/settings/test', { cookie, body: { ai: {} } })).body).toEqual({ ok: true, provider: 'openrouter', model: 'qwen/qwen4-72b', latencyMs: 420 })
    expect((await put({ model: 'openrouter/auto' })).body.ai.apiKey.set).toBe(true) // same provider: kept
    expect((await put({ provider: 'ollama', model: 'llama4:8b' })).body.ai).toMatchObject({ provider: 'ollama', apiKey: { set: false } }) // switched: dropped
    expect((await put({ provider: '' })).body.ai.status).toBe('off')

    const log: AuditList = (await call('GET', '/api/admin/audit', { cookie })).body
    expect(log.entries.map((e) => [e.action, e.detail])).toEqual([
      ['settings.update', { fields: ['ai.provider'] }],
      ['settings.update', { fields: ['ai.provider', 'ai.model', 'ai.apiKey'] }],
      ['settings.update', { fields: ['ai.model'] }],
      ['settings.update', { fields: ['ai.provider', 'ai.model', 'ai.apiKey'] }],
    ])
    expect(JSON.stringify(log)).not.toContain(key)
  })

  it('drops a stored key or crawl token when its base URL changes, unless a new one comes with it', async () => {
    const cookie = await signIn()
    const put = (body: object) => call('PUT', '/api/admin/settings', { cookie, body })
    const key = 'sk-or-v1-e2e-only-0000feed'
    expect((await put({ ai: { provider: 'openrouter', model: 'qwen/qwen4-72b', baseUrl: 'https://proxy.example.com/v1', apiKey: key } })).status).toBe(200)
    expect((await put({ ai: { baseUrl: ' https://proxy.example.com/v1/ ' } })).body.ai).toMatchObject({ baseUrl: 'https://proxy.example.com/v1', apiKey: { set: true } })
    const other = { baseUrl: 'https://other.example.com/v1' }
    expect((await call('POST', '/api/admin/settings/test', { cookie, body: { ai: other } })).body.error).toBe('OpenRouter needs an API key.')
    expect((await put({ ai: other })).body.error.message).toBe('OpenRouter needs an API key.') // tested without the key: nothing saved
    expect((await put({ ai: { ...other, apiKey: key } })).body.ai).toMatchObject({ ...other, apiKey: { set: true, hint: '…feed' } })

    expect((await put({ search: { crawlBase: 'http://crawl:11235', crawlToken: 'tok' } })).body.search.crawlToken.set).toBe(true)
    expect((await put({ search: { crawlBase: 'http://crawl:11235/' } })).body.search.crawlToken.set).toBe(true)
    expect((await put({ search: { crawlBase: 'http://other:11235' } })).body.search).toMatchObject({ crawlBase: 'http://other:11235', crawlToken: { set: false } })
    expect((await put({ search: { crawlBase: 'http://third:11235', crawlToken: 'tok2' } })).body.search.crawlToken.set).toBe(true)
  })

  it('saves and clears search keys; a cleared one falls back to .env', async () => {
    const cookie = await signIn()
    const put = (search: object) => call('PUT', '/api/admin/settings', { cookie, body: { search } })
    const s: Settings = (await put({ braveApiKey: 'BSA-saved-here-0000beef', crawlBase: 'http://crawl:11235', crawlToken: 'tok' })).body
    expect(s.search).toEqual({
      braveApiKey: { set: true, hint: '…beef', source: 'db' },
      crawlBase: 'http://crawl:11235',
      crawlBaseSource: 'db',
      crawlToken: { set: true, hint: '', source: 'db' },
    })
    const cleared: Settings = (await put({ clearBraveApiKey: true, clearCrawlToken: true })).body
    expect(cleared.search.braveApiKey).toEqual({ set: true, hint: '…c0de', source: 'env' })
    expect(cleared.search.crawlToken).toEqual({ set: false, hint: '', source: '' })
  })

  it('signs in with ChatGPT through /auth/callback once per state, then signs out', async () => {
    const cookie = await signIn()
    const { url } = (await call('POST', '/api/admin/settings/chatgpt/start', { cookie })).body as { url: string }
    const path = new URL(url).pathname + new URL(url).search
    expect(path).toMatch(/^\/auth\/callback\?code=e2e&state=/)
    const back = await realFetch(base + path, { redirect: 'manual' }) // no cookie: the state says whose sign-in it is
    expect(back.status).toBe(302)
    expect(back.headers.get('location')).toBe(`${WEB_URL}/settings?chatgpt=ok`)
    const s: Settings = (await call('GET', '/api/admin/settings', { cookie })).body
    expect(s.ai).toMatchObject({ provider: 'chatgpt', status: 'ready', chatgpt: { signedIn: true, email: 'owner@example.com' } })

    const again = await realFetch(base + path, { redirect: 'manual' })
    expect(new URL(again.headers.get('location')!).searchParams.get('chatgpt')).toBe('error')

    const out: Settings = (await call('POST', '/api/admin/settings/chatgpt/signout', { cookie })).body
    expect(out.ai).toMatchObject({ provider: 'chatgpt', status: 'error', error: 'Sign in with ChatGPT first.', chatgpt: { signedIn: false } })
    const log: AuditList = (await call('GET', '/api/admin/audit', { cookie })).body
    expect(log.entries.map((e) => e.action)).toEqual(['settings.chatgpt.signout', 'settings.chatgpt.signin'])
  })
})

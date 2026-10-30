// Contract-faithful stand-in for the Go API's auth and admin routes (docs/API-CONTRACT.md §3 Auth
// and "Admin console") for the Playwright suite: tsid cookie sessions, CORS + the CSRF rule, the error envelope, 401/403
// on /api/admin/*, Cache-Control: no-store, the mutation rate limit, validation, the 409 safety rules, the audit log, and
// the AI/search settings with ChatGPT sign-in (/auth/callback).
// Responses are typed with the client's own signatures (src/lib/api.ts), so contract drift breaks `npm run typecheck`.
// Playwright runs it as globalSetup (default export); mock-api.test.ts exercises it over HTTP.
import { randomBytes, randomUUID } from 'node:crypto'
import { createServer, type IncomingMessage, type Server, type ServerResponse } from 'node:http'
import type { AddressInfo } from 'node:net'
import type {
  AISettings,
  ActivityItem,
  AdminOverview,
  AdminUser,
  AdminUserDetail,
  AuditAction,
  AuditEntry,
  DayValue,
  FeatureKey,
  Portfolio,
  ProviderInfo,
  Role,
  Trade,
  UserSort,
} from '../src/lib/types'

export const API_PORT = 4011
export const WEB_PORT = 4181
export const API_URL = `http://localhost:${API_PORT}` // also in package.json "e2e:build" (VITE_API_URL)
export const WEB_URL = `http://localhost:${WEB_PORT}`

// Test-only credentials for the mock; nothing real.
export const ADMIN = { email: 'admin@example.com', password: 'admin-e2e-only-pw' }
export const DEMO = { email: 'demo@example.com', password: 'demo-e2e-only-pw' }
export const OPS = { email: 'ops@example.com', password: 'ops-e2e-only-pass' }
/** Fixed ids of the seeded accounts, for direct links. */
export const SEED = {
  admin: '00000000-0000-4000-8000-000000000001',
  demo: '00000000-0000-4000-8000-000000000002',
  ops: '00000000-0000-4000-8000-000000000003',
}

type Api = typeof import('../src/lib/api').api
type Res<K extends keyof Api> = Awaited<ReturnType<Api[K]>>

class Fail extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
  ) {
    super(message)
  }
}

const DEFAULT_LIMIT = 500
const DAY = 86_400_000
const START = 100_000
const startedAt = new Date().toISOString()

const now = () => new Date().toISOString()
/** Like Go's crypto/rand.Text(): 26 base32 characters. */
const generatePassword = () => [...randomBytes(26)].map((b) => 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'[b % 32]).join('')
const ago = (ms: number) => new Date(Date.now() - ms).toISOString()
const midnight = () => Math.floor(Date.now() / DAY) * DAY
/** The 30 UTC days ending today, oldest first. */
const days = () => Array.from({ length: 30 }, (_, i) => new Date(midnight() - (29 - i) * DAY).toISOString().slice(0, 10))
const series = (values: number[]): DayValue[] => days().map((day, i) => ({ day, value: values[i] ?? 0 }))
const sum = (xs: number[]) => xs.reduce((a, b) => a + b, 0)
const windowed = (daily: number[]) => ({ today: daily[29], d7: sum(daily.slice(23)), d30: sum(daily) })

/** Deterministic pseudo-random numbers, so every fresh world looks the same. */
function rng(seed: number) {
  let s = seed
  return () => ((s = (s * 1103515245 + 12345) % 2 ** 31) / 2 ** 31)
}

// ── World ───────────────────────────────────────────────────────────────────
// ponytail: every sign-in gets a fresh copy of the seed world, so parallel tests never see each other's changes. The real
// API is one shared database.

type Account = {
  id: string
  email: string
  name: string
  role: Role
  password: string
  managed: boolean
  createdAt: string
  lastLoginAt: string | null
  lastActiveAt: string | null
  disabledAt: string | null
  creditLimit: number | null
  usage: number[] // 30 UTC days, oldest first
  otherSessions: number // live sessions besides the mock's own sign-ins
  cash: number
  positions: Portfolio['positions']
  trades: Trade[]
  activity: AdminUserDetail['activity']
  chatMessages: number
}

/** The saved AI config (key in clear: this is the "database"), the ChatGPT sign-in and the search keys saved here. */
type AIConfig = Pick<AISettings, 'provider' | 'model' | 'baseUrl' | 'effort' | 'awsRegion' | 'knowledgeCutoff'> & { key: string }
type SettingsState = {
  ai: AIConfig
  chatgpt: { email: string } | null
  search: { braveApiKey: string; crawlBase: string; crawlToken: string }
}

export type World = {
  accounts: Map<string, Account>
  settings: SettingsState // a fresh install: AI off, the Brave key from .env
  audit: AuditEntry[] // newest first
  sessions: Map<string, string> // token → account id (sessions belong to their world)
  mutations: Map<string, { start: number; count: number }> // per admin, fixed 10-minute window
}

const NAMES = [
  'Ava Tan', 'Ben Lim', 'Chloe Ng', 'Daniel Ong', 'Emma Goh', 'Felix Wong', 'Grace Lee', 'Henry Koh', 'Isla Chua',
  'Jack Teo', 'Kira Low', 'Liam Yeo', 'Maya Sim', 'Noah Ho', 'Olivia Foo', 'Peter Chan', 'Quinn Seah', 'Ruby Toh',
  'Sam Quek', 'Tara Lau', 'Umar Aziz', 'Vera Lin', 'Will Pang', 'Xena Kwok', 'Yusuf Rahman', 'Zoe Heng', 'Arjun Nair',
  'Bella Cruz',
]

const item = (title: string, at: string, status: string | null = null): ActivityItem => ({ id: randomUUID(), title, status, at })

function account(over: Partial<Account> & Pick<Account, 'id' | 'email' | 'name'>, r: () => number): Account {
  const heavy = r() > 0.5
  const usage = Array.from({ length: 30 }, (_, i) => (r() < (heavy ? 0.7 : 0.25) ? Math.round(r() * (heavy ? 60 : 20)) + (i === 29 ? 2 : 0) : 0))
  const eth = r() > 0.5
  const qty = eth ? Math.round(r() * 300) / 100 + 0.5 : 0
  return {
    role: 'user',
    password: randomBytes(16).toString('base64url'),
    managed: false,
    createdAt: ago(Math.round(r() * 60 * DAY)),
    lastLoginAt: ago(Math.round(r() * 5 * DAY)),
    lastActiveAt: ago(Math.round(r() * 9 * DAY)),
    disabledAt: null,
    creditLimit: null,
    usage,
    otherSessions: Math.round(r() * 2),
    cash: START - qty * 2900,
    positions: eth
      ? [{ symbol: 'ETH', qty, avgPrice: 2900, markPrice: 3000, marketValue: qty * 3000, unrealizedPnl: qty * 100, unrealizedPnlPct: 3.45, stopLoss: null, takeProfit: null }]
      : [],
    trades: eth
      ? [{ id: randomUUID(), symbol: 'ETH', side: 'buy', qty, price: 2900, notional: qty * 2900, fee: qty * 2.9, realizedPnl: -qty * 2.9, source: 'manual', note: '', createdAt: ago(2 * DAY) }]
      : [],
    activity: { chats: [], forecasts: [], strategies: [], aiPortfolios: [], arenaRuns: [] },
    chatMessages: 0,
    ...over,
  }
}

export function createWorld(): World {
  const r = rng(42)
  const list: Account[] = [
    account({ id: SEED.admin, email: ADMIN.email, name: 'Admin', role: 'admin', password: ADMIN.password, managed: true, otherSessions: 0 }, r),
    account(
      {
        id: SEED.demo,
        email: DEMO.email,
        name: 'Demo Investor',
        password: DEMO.password,
        managed: true,
        creditLimit: null,
        lastActiveAt: ago(10 * 60_000),
        otherSessions: 2,
        chatMessages: 14,
        activity: {
          chats: [item('Is BTC overbought right now?', ago(2 * 3600_000)), item('Layer-1s with less drawdown', ago(DAY))],
          forecasts: [item('Will ETH outperform BTC over Q4 2026?', ago(26 * 3600_000), 'completed'), item('Will SOL close above $200 on Dec 31?', ago(20 * 60_000), 'running')],
          strategies: [item('ETH RSI mean reversion', ago(3 * DAY))],
          aiPortfolios: [item('Layer-1 core', ago(30 * 3600_000))],
          arenaRuns: [item('Daily · Aug 3 → Oct 1, 2026', ago(5 * DAY), 'completed')],
        },
      },
      r,
    ),
    account({ id: SEED.ops, email: OPS.email, name: 'Ops Lead', role: 'admin', password: OPS.password }, r),
    ...NAMES.map((name, i) =>
      account(
        {
          id: `00000000-0000-4000-8000-${String(100 + i).padStart(12, '0')}`,
          email: `${name.toLowerCase().replace(' ', '.')}@example.com`,
          name,
          disabledAt: i % 9 === 4 ? ago(3 * DAY) : null,
          creditLimit: i % 7 === 3 ? 100 : null,
        },
        r,
      ),
    ),
  ]
  for (const a of list) if (a.disabledAt) a.otherSessions = 0
  return {
    accounts: new Map(list.map((a) => [a.id, a])),
    settings: {
      ai: { provider: '', model: '', baseUrl: '', effort: '', awsRegion: '', knowledgeCutoff: '2026-06-30', key: '' },
      chatgpt: null,
      search: { braveApiKey: '', crawlBase: '', crawlToken: '' },
    },
    audit: [],
    sessions: new Map(),
    mutations: new Map(),
  }
}

const portfolioOf = (a: Account): Portfolio => {
  const value = sum(a.positions.map((p) => p.marketValue))
  const unrealizedPnl = sum(a.positions.map((p) => p.unrealizedPnl))
  const realizedPnl = sum(a.trades.map((t) => t.realizedPnl))
  const equity = a.cash + value
  return {
    cash: a.cash,
    equity,
    startingBalance: START,
    unrealizedPnl,
    realizedPnl,
    totalPnl: equity - START,
    totalPnlPct: ((equity - START) / START) * 100,
    grossExposure: value,
    buyingPower: Math.max(0, 2 * equity - value),
    positions: a.positions,
  }
}

const liveSessions = (w: World, a: Account) => a.otherSessions + [...w.sessions.values()].filter((id) => id === a.id).length

export function adminUser(w: World, a: Account): AdminUser {
  return {
    id: a.id,
    email: a.email,
    name: a.name,
    role: a.role,
    status: a.disabledAt ? 'disabled' : 'active',
    managed: a.managed,
    createdAt: a.createdAt,
    lastLoginAt: a.lastLoginAt,
    lastActiveAt: a.lastActiveAt,
    disabledAt: a.disabledAt,
    creditsToday: a.usage[29],
    creditLimit: a.creditLimit,
    equity: portfolioOf(a).equity,
    sessions: liveSessions(w, a),
  }
}

// ── Validation (contract table "Validation") ────────────────────────────────

const bad = (message: string) => new Fail(400, 'bad_request', message)

function email(v: unknown) {
  const e = typeof v === 'string' ? v.trim().toLowerCase() : ''
  if (e.length < 3 || e.length > 254 || !/^[^\s@<>()[\]",;:\\]+@[^\s@<>()[\]",;:\\]+\.[^\s@<>()[\]",;:\\]+$/.test(e))
    throw bad('email must be a single valid address.')
  return e
}
function name(v: unknown) {
  const n = typeof v === 'string' ? v.trim() : ''
  if (n.length < 1 || [...n].length > 80) throw bad('name must be 1 to 80 characters.')
  return n
}
function password(v: unknown) {
  if (typeof v !== 'string') throw bad('password must be a string.')
  const bytes = Buffer.byteLength(v)
  if (bytes < 12 || bytes > 72 || !v.trim()) throw bad('password must be 12 to 72 bytes and not only spaces.')
  return v
}
function role(v: unknown): Role {
  if (v !== 'user' && v !== 'admin') throw bad('role must be "user" or "admin".')
  return v
}
function creditLimit(v: unknown) {
  if (v === null) return null
  if (typeof v !== 'number' || !Number.isInteger(v) || v < 0 || v > 100_000) throw bad('creditLimit must be an integer from 0 to 100000, or null.')
  return v
}
function intParam(q: URLSearchParams, key: string, def: number, max = Infinity) {
  const raw = q.get(key)
  if (raw === null) return def
  const n = Number(raw)
  if (!/^\d+$/.test(raw) || n < 1 || n > max) throw bad(`${key} must be an integer from 1${max < Infinity ? ` to ${max}` : ''}.`)
  return n
}

// ── Admin operations ────────────────────────────────────────────────────────

type Ctx = { w: World; me: Account; token: string; body: Record<string, unknown>; q: URLSearchParams; id: string }

const target = (w: World, id: string) => {
  const a = w.accounts.get(id)
  if (!a) throw new Fail(404, 'not_found', 'User not found.')
  return a
}

function audit(c: Pick<Ctx, 'w' | 'me'>, action: AuditAction, t: Account | null, detail: Record<string, unknown> = {}) {
  c.w.audit.unshift({
    id: randomUUID(),
    action,
    admin: { id: c.me.id, email: c.me.email },
    target: t && { id: t.id, email: t.email },
    detail,
    createdAt: now(),
  })
}

const SELF = "You can't disable, demote or delete your own account."
/** Throws when removing `t` from the active admins would leave none (the real API locks those rows first). */
function keepAnAdmin(w: World, t: Account) {
  const active = [...w.accounts.values()].filter((a) => a.role === 'admin' && !a.disabledAt && a.id !== t.id)
  if (t.role === 'admin' && !t.disabledAt && !active.length) throw new Fail(409, 'conflict', 'At least one active admin must remain.')
}

/** Deletes every session of `a` except the caller's current one; returns how many. */
function revoke(c: Ctx, a: Account) {
  let n = a.otherSessions
  a.otherSessions = 0
  for (const [tok, id] of c.w.sessions) if (id === a.id && tok !== c.token) (c.w.sessions.delete(tok), n++)
  return n
}

const SORT_KEYS: Record<UserSort, (u: AdminUser) => string | number | null> = {
  createdAt: (u) => u.createdAt,
  lastActiveAt: (u) => u.lastActiveAt,
  lastLoginAt: (u) => u.lastLoginAt,
  email: (u) => u.email,
  name: (u) => u.name.toLowerCase(),
  creditsToday: (u) => u.creditsToday,
}

export function listUsers(w: World, q: URLSearchParams): Res<'users'> {
  const text = (q.get('q') ?? '').trim().toLowerCase()
  if (text.length > 100) throw bad('q must be at most 100 characters.')
  const status = q.get('status') ?? 'all'
  if (!['all', 'active', 'disabled'].includes(status)) throw bad('status must be all, active or disabled.')
  const r = q.get('role')
  if (r !== null) role(r)
  const sort = (q.get('sort') ?? 'createdAt') as UserSort
  if (!(sort in SORT_KEYS)) throw bad('sort is not a sortable field.')
  const order = q.get('order') ?? 'desc'
  if (order !== 'asc' && order !== 'desc') throw bad('order must be asc or desc.')
  const page = intParam(q, 'page', 1)
  const limit = intParam(q, 'limit', 25, 100)

  const key = SORT_KEYS[sort]
  const dir = order === 'asc' ? 1 : -1
  const users = [...w.accounts.values()]
    .map((a) => adminUser(w, a))
    .filter((u) => !text || u.email.includes(text) || u.name.toLowerCase().includes(text))
    .filter((u) => status === 'all' || u.status === status)
    .filter((u) => r === null || u.role === r)
    .sort((a, b) => {
      const x = key(a)
      const y = key(b)
      if (x === y) return a.id < b.id ? -1 : 1
      if (x === null) return 1 // nulls last in both orders
      if (y === null) return -1
      return (x < y ? -1 : 1) * dir
    })
  return { users: users.slice((page - 1) * limit, page * limit), total: users.length, page, limit, defaultCreditLimit: DEFAULT_LIMIT }
}

const FEATURES: [FeatureKey, string][] = [
  ['chat_messages', 'Chat messages'],
  ['forecasts', 'Forecasts'],
  ['strategies', 'Strategies'],
  ['ai_portfolios', 'AI portfolios'],
  ['arena_runs', 'Arena runs'],
  ['orders', 'Paper orders'],
  ['copy_plans', 'Copy plans'],
]

export function overview(w: World): AdminOverview {
  const accounts = [...w.accounts.values()]
  const daily = Array.from({ length: 30 }, (_, i) => sum(accounts.map((a) => a.usage[i])))
  const since = (iso: string | null, n: number) => iso != null && Date.parse(iso) >= midnight() - (n - 1) * DAY
  const r = rng(7)
  const books = accounts.map(portfolioOf)
  const equity = sum(books.map((p) => p.equity))
  return {
    asOf: now(),
    users: {
      total: accounts.length,
      admins: accounts.filter((a) => a.role === 'admin').length,
      disabled: accounts.filter((a) => a.disabledAt).length,
      new30d: accounts.filter((a) => since(a.createdAt, 30)).length,
      active: {
        today: accounts.filter((a) => since(a.lastActiveAt, 1)).length,
        d7: accounts.filter((a) => since(a.lastActiveAt, 7)).length,
        d30: accounts.filter((a) => since(a.lastActiveAt, 30)).length,
      },
    },
    credits: { ...windowed(daily), daily: series(daily), defaultLimit: DEFAULT_LIMIT },
    features: FEATURES.map(([key, label], k) => {
      const values = Array.from({ length: 30 }, () => Math.round(r() * (k === 0 ? 80 : 12)))
      return { key, label, ...windowed(values), daily: series(values) }
    }),
    topUsers: accounts
      .map((a) => ({ id: a.id, email: a.email, name: a.name, credits: sum(a.usage.slice(23)) }))
      .filter((u) => u.credits > 0)
      .sort((a, b) => b.credits - a.credits || (a.email < b.email ? -1 : 1))
      .slice(0, 10),
    paper: {
      accounts: accounts.length,
      openPositions: sum(accounts.map((a) => a.positions.length)),
      equity,
      totalPnl: equity - START * accounts.length,
      notional30d: sum(accounts.flatMap((a) => a.trades.map((t) => t.notional))),
    },
    system: {
      version: 'e2e0mock0001',
      startedAt,
      uptimeSec: Math.round((Date.now() - Date.parse(startedAt)) / 1000),
      db: { ok: true, latencyMs: 1 },
      model: w.settings.ai.provider ? w.settings.ai.model || preset(w.settings.ai.provider)!.defaultModel : '',
      knowledgeCutoff: w.settings.ai.knowledgeCutoff,
      news: { enabled: true, lastIngestAt: ago(40 * 60_000), items: 412, briefAt: ago(40 * 60_000), briefAgeMin: 40 },
      brave: { used: 126, limit: 1500 },
      crawler: { base: 'https://crawl.example.com', configured: true, reachable: false },
      jobs: {
        forecastsRunning: 1,
        arenaRunsRunning: 0,
        liveArena: {
          enabled: true, startedAt: ago(2 * DAY),
          lastStepAt: new Date(midnight() + Math.floor((Date.now() - midnight()) / (4 * 3_600_000)) * 4 * 3_600_000).toISOString(),
          nextStepAt: new Date(midnight() + (Math.floor((Date.now() - midnight()) / (4 * 3_600_000)) + 1) * 4 * 3_600_000).toISOString(),
          agents: 3, benched: 1, copyPlans: 8, status: 'live', stale: false, lastError: null,
          attemptsToday: 3, requestCeilingToday: 12,
          maxDecisionsPerDay: 18, maxRequestsPerDay: 72, maxTokensPerRequest: 4096, decisionTimeoutSec: 90,
        },
      },
    },
  }
}

export function detail(w: World, a: Account): AdminUserDetail {
  const act = a.activity
  return {
    user: adminUser(w, a),
    defaultCreditLimit: DEFAULT_LIMIT,
    usage: series(a.usage),
    counts: {
      chats: act.chats.length,
      chatMessages: a.chatMessages,
      forecasts: act.forecasts.length,
      strategies: act.strategies.length,
      aiPortfolios: act.aiPortfolios.length,
      arenaRuns: act.arenaRuns.length,
      trades: a.trades.length,
    },
    portfolio: portfolioOf(a),
    trades: a.trades.slice(0, 20),
    activity: act,
    audit: w.audit.filter((e) => e.target?.id === a.id).slice(0, 20),
  }
}

// ── Routes ──────────────────────────────────────────────────────────────────

type Route = { method: string; re: RegExp; status: number; handler: (c: Ctx) => unknown }
const routes: Route[] = []
const on = (method: string, path: string, handler: Route['handler'], status = 200) =>
  routes.push({ method, re: new RegExp(`^${path.replace(':id', '([^/]+)')}$`), status, handler })

on('GET', '/api/admin/overview', ({ w }): Res<'overview'> => ({ overview: overview(w) }))
on('GET', '/api/admin/users', ({ w, q }): Res<'users'> => listUsers(w, q))
on('GET', '/api/admin/users/:id', ({ w, id }): Res<'user'> => detail(w, target(w, id)))

on(
  'POST',
  '/api/admin/users',
  (c): Res<'createUser'> => {
    const { body: b } = c
    const e = email(b.email)
    const n = name(b.name)
    const r = b.role === undefined ? 'user' : role(b.role)
    const limit = b.creditLimit === undefined ? null : creditLimit(b.creditLimit)
    const supplied = b.password === undefined ? undefined : password(b.password)
    if ([...c.w.accounts.values()].some((a) => a.email === e)) throw new Fail(409, 'conflict', 'An account with this email already exists.')
    const generated = supplied === undefined ? generatePassword() : undefined
    const a = account({ id: randomUUID(), email: e, name: n, role: r, creditLimit: limit, password: supplied ?? generated! }, rng(1))
    Object.assign(a, { createdAt: now(), lastLoginAt: null, lastActiveAt: null, usage: Array(30).fill(0), otherSessions: 0, cash: START, positions: [], trades: [] })
    c.w.accounts.set(a.id, a)
    audit(c, 'user.create', a, { role: r, creditLimit: limit, passwordGenerated: !!generated })
    return { user: adminUser(c.w, a), ...(generated && { password: generated }) }
  },
  201,
)

on('PATCH', '/api/admin/users/:id', (c): Res<'updateUser'> => {
  const t = target(c.w, c.id)
  const b = c.body
  const next = {
    name: b.name === undefined ? undefined : name(b.name),
    role: b.role === undefined ? undefined : role(b.role),
    creditLimit: b.creditLimit === undefined ? undefined : creditLimit(b.creditLimit),
    disabled: b.disabled === undefined ? undefined : b.disabled,
  }
  if (next.disabled !== undefined && typeof next.disabled !== 'boolean') throw bad('disabled must be true or false.')
  if (Object.values(next).every((v) => v === undefined)) throw bad('Send at least one of name, role, creditLimit or disabled.')
  const demote = next.role === 'user' && t.role === 'admin'
  const disable = next.disabled === true && !t.disabledAt
  if ((demote || disable) && t.id === c.me.id) throw new Fail(409, 'conflict', SELF)
  if (demote || disable) keepAnAdmin(c.w, t)

  const changes: Record<string, { from: unknown; to: unknown }> = {}
  for (const k of ['name', 'role', 'creditLimit'] as const) {
    if (next[k] !== undefined && next[k] !== t[k]) changes[k] = { from: t[k], to: next[k] }
  }
  if (next.disabled === true && !t.disabledAt) {
    t.disabledAt = now()
    audit(c, 'user.disable', t, { sessionsRevoked: revoke(c, t) })
  } else if (next.disabled === false && t.disabledAt) {
    t.disabledAt = null
    audit(c, 'user.enable', t)
  }
  if (Object.keys(changes).length) {
    Object.assign(t, Object.fromEntries(Object.entries(changes).map(([k, v]) => [k, v.to])))
    audit(c, 'user.update', t, changes)
  }
  return { user: adminUser(c.w, t) }
})

on('POST', '/api/admin/users/:id/password', (c): Res<'setPassword'> => {
  const t = target(c.w, c.id)
  const supplied = c.body.password === undefined ? undefined : password(c.body.password)
  if (t.managed) throw new Fail(409, 'conflict', "This account's password comes from the server config. Rotate it there and redeploy.")
  const generated = supplied === undefined ? generatePassword() : undefined
  t.password = supplied ?? generated!
  audit(c, 'user.password', t, { generated: !!generated, sessionsRevoked: revoke(c, t) })
  return { user: adminUser(c.w, t), ...(generated && { password: generated }) }
})

on('POST', '/api/admin/users/:id/sessions/revoke', (c): Res<'revokeSessions'> => {
  const t = target(c.w, c.id)
  const revoked = revoke(c, t)
  audit(c, 'user.sessions_revoke', t, { sessionsRevoked: revoked })
  return { user: adminUser(c.w, t), revoked }
})

on('POST', '/api/admin/users/:id/reset-portfolio', (c): Res<'resetPortfolio'> => {
  const t = target(c.w, c.id)
  Object.assign(t, { cash: START, positions: [], trades: [] })
  audit(c, 'user.portfolio_reset', t)
  return { user: adminUser(c.w, t), portfolio: portfolioOf(t) }
})

on('POST', '/api/admin/users/:id/reset-credits', (c): Res<'resetCredits'> => {
  const t = target(c.w, c.id)
  audit(c, 'user.credits_reset', t, { creditsBefore: t.usage[29] })
  t.usage[29] = 0
  return { user: adminUser(c.w, t) }
})

on(
  'DELETE',
  '/api/admin/users/:id',
  (c) => {
    const t = target(c.w, c.id)
    if (t.id === c.me.id) throw new Fail(409, 'conflict', SELF)
    keepAnAdmin(c.w, t)
    c.w.accounts.delete(t.id)
    for (const [tok, id] of c.w.sessions) if (id === t.id) c.w.sessions.delete(tok)
    audit(c, 'user.delete', t, { name: t.name, role: t.role })
  },
  204,
)

on('GET', '/api/admin/audit', ({ w, q }): Res<'audit'> => {
  const page = intParam(q, 'page', 1)
  const limit = intParam(q, 'limit', 50, 100)
  return { entries: w.audit.slice((page - 1) * limit, page * limit), total: w.audit.length, page, limit }
})

// ── Settings ────────────────────────────────────────────────────────────────
// ponytail: "testing" a provider is a rule on the merged draft (a key starting with "bad" is refused), not a model call.

const P = (id: string, label: string, wire: ProviderInfo['wire'], defaultBaseUrl: string, more: Partial<ProviderInfo> = {}): ProviderInfo => ({
  id, label, wire, defaultBaseUrl, defaultModel: '', needsKey: true, effortDefault: false, ...more,
})
export const PRESETS: ProviderInfo[] = [
  P('openai', 'OpenAI', 'responses', 'https://api.openai.com/v1', { defaultModel: 'gpt-5.5', effortDefault: true, keyUrl: 'https://platform.openai.com/api-keys' }),
  P('chatgpt', 'ChatGPT plan', 'responses', 'https://api.openai.com/v1', { needsKey: false, effortDefault: true }),
  P('anthropic', 'Anthropic', 'messages', 'https://api.anthropic.com', { defaultModel: 'claude-sonnet-5-5', effortDefault: true, keyUrl: 'https://console.anthropic.com/settings/keys' }),
  P('bedrock', 'Amazon Bedrock', 'converse', '', { needsKey: false, defaultModel: 'global.anthropic.claude-sonnet-5-5', effortDefault: true }),
  P('openrouter', 'OpenRouter', 'chat', 'https://openrouter.ai/api/v1', { effortDefault: true, keyUrl: 'https://openrouter.ai/keys' }),
  P('gemini', 'Google Gemini', 'chat', 'https://generativelanguage.googleapis.com/v1beta/openai', { effortDefault: true, keyUrl: 'https://aistudio.google.com/apikey' }),
  P('groq', 'Groq', 'chat', 'https://api.groq.com/openai/v1', { keyUrl: 'https://console.groq.com/keys' }),
  P('deepseek', 'DeepSeek', 'chat', 'https://api.deepseek.com', { keyUrl: 'https://platform.deepseek.com/api_keys' }),
  P('xai', 'xAI', 'chat', 'https://api.x.ai/v1', { keyUrl: 'https://console.x.ai' }),
  P('mistral', 'Mistral', 'chat', 'https://api.mistral.ai/v1', { keyUrl: 'https://console.mistral.ai/api-keys' }),
  P('ollama', 'Ollama', 'chat', 'http://host.docker.internal:11434/v1', { needsKey: false }),
  P('lmstudio', 'LM Studio', 'chat', 'http://host.docker.internal:1234/v1', { needsKey: false }),
  P('custom', 'Custom (OpenAI-compatible)', 'chat', '', { needsKey: false }),
]
const preset = (id: string) => PRESETS.find((p) => p.id === id)
const MODELS: Record<string, string[]> = {
  openrouter: ['openrouter/auto', 'qwen/qwen4-72b', 'deepseek/deepseek-v4'],
  chatgpt: ['gpt-5.5', 'gpt-5.5-mini'],
  ollama: ['llama4:8b', 'qwen4:14b'],
}
/** As if set in the server's .env. */
const ENV_SEARCH = { braveApiKey: 'BSA-from-env-0000c0de', crawlBase: '', crawlToken: '' }

const hint = (key: string) => (key.length >= 16 ? `…${key.slice(-4)}` : '')
/** A base URL as the API stores it: trimmed, without trailing slashes. */
const trimUrl = (u: string) => u.trim().replace(/\/+$/, '')
const secret = (saved: string, env: string) => ({ set: !!(saved || env), hint: hint(saved || env), source: saved ? 'db' : env ? 'env' : '' }) as const

/** Why `c` can't answer ("" = it can). */
function problem(w: World, c: AIConfig, needModel = true) {
  const p = preset(c.provider)
  if (!p) return 'No AI provider is configured.'
  if (p.id === 'chatgpt' && !w.settings.chatgpt) return 'Sign in with ChatGPT first.'
  if (p.needsKey && !c.key) return `${p.label} needs an API key.`
  if (c.key.startsWith('bad')) return `${p.label}: 401 Unauthorized (invalid API key).`
  if (p.wire !== 'converse' && !p.defaultBaseUrl && !c.baseUrl) return 'Enter the base URL of your server.'
  if (needModel && !p.defaultModel && !c.model) return 'Pick a model.'
  return ''
}

/** `draft` over the saved config. Like the API, a saved key never follows a request to another provider or base URL. */
function merge(w: World, draft: unknown): AIConfig {
  const d = (draft && typeof draft === 'object' ? draft : {}) as Record<string, unknown>
  const cur = w.settings.ai
  const field = (k: keyof AIConfig) => {
    if (d[k] === undefined) return cur[k]
    if (typeof d[k] !== 'string') throw bad(`ai.${k} must be a string.`)
    return d[k]
  }
  const provider = field('provider')
  if (provider && !preset(provider)) throw bad(`Unknown provider "${provider}".`)
  const effort = field('effort')
  if (effort !== '' && effort !== 'on' && effort !== 'off') throw bad('ai.effort must be "", "on" or "off".')
  const baseUrl = trimUrl(field('baseUrl'))
  const typed = typeof d.apiKey === 'string' ? d.apiKey.trim() : ''
  const key = typed || (d.clearKey === true || provider !== cur.provider || baseUrl !== cur.baseUrl ? '' : cur.key)
  return { provider, model: field('model'), baseUrl, effort, awsRegion: field('awsRegion'), knowledgeCutoff: field('knowledgeCutoff'), key }
}

export function settingsBody(w: World): Res<'settings'> {
  const { ai, chatgpt, search } = w.settings
  const issue = ai.provider ? problem(w, ai, false) : ''
  return {
    ai: {
      provider: ai.provider,
      model: ai.model,
      baseUrl: ai.baseUrl,
      effort: ai.effort,
      awsRegion: ai.awsRegion,
      knowledgeCutoff: ai.knowledgeCutoff,
      apiKey: { set: !!ai.key, hint: hint(ai.key) },
      source: ai.provider ? 'db' : 'none',
      readOnly: false,
      chatgpt: { signedIn: !!chatgpt, email: chatgpt?.email ?? '', error: '' },
      status: !ai.provider ? 'off' : issue ? 'error' : 'ready',
      error: issue,
    },
    search: {
      braveApiKey: secret(search.braveApiKey, ENV_SEARCH.braveApiKey),
      crawlBase: search.crawlBase || ENV_SEARCH.crawlBase,
      crawlBaseSource: search.crawlBase ? 'db' : ENV_SEARCH.crawlBase ? 'env' : '',
      crawlToken: secret(search.crawlToken, ENV_SEARCH.crawlToken),
    },
    providers: PRESETS,
    secretKeySet: true,
  }
}

on('GET', '/api/admin/settings', ({ w }): Res<'settings'> => settingsBody(w))

on('PUT', '/api/admin/settings', (c): Res<'updateSettings'> => {
  const { ai, search } = c.body as { ai?: unknown; search?: Record<string, unknown> }
  const fields: string[] = [] // names only, never values
  if (ai !== undefined) {
    const next = merge(c.w, ai)
    const issue = next.provider ? problem(c.w, next) : '' // tested before saving; "" (off) needs no test
    if (issue) throw bad(issue)
    for (const k of Object.keys(next) as (keyof AIConfig)[]) if (next[k] !== c.w.settings.ai[k]) fields.push(k === 'key' ? 'ai.apiKey' : `ai.${k}`)
    c.w.settings.ai = next
  }
  if (search !== undefined) {
    const s = c.w.settings.search
    const base = typeof search.crawlBase === 'string' ? trimUrl(search.crawlBase) : s.crawlBase
    for (const [k, clear] of [['braveApiKey', 'clearBraveApiKey'], ['crawlToken', 'clearCrawlToken']] as const) {
      const typed = typeof search[k] === 'string' ? search[k].trim() : ''
      // A new crawl base drops the token unless a new one comes with it.
      const v = typed || (search[clear] === true || (k === 'crawlToken' && base !== s.crawlBase) ? '' : s[k])
      if (v !== s[k]) (fields.push(`search.${k}`), (s[k] = v))
    }
    if (base !== s.crawlBase) (fields.push('search.crawlBase'), (s.crawlBase = base))
  }
  if (fields.length) audit(c, 'settings.update', null, { fields })
  return settingsBody(c.w)
})

on('POST', '/api/admin/settings/test', (c): Res<'testSettings'> => {
  const next = merge(c.w, c.body.ai)
  const issue = problem(c.w, next)
  return issue ? { ok: false, error: issue } : { ok: true, provider: next.provider, model: next.model || preset(next.provider)!.defaultModel, latencyMs: 420 }
})

on('POST', '/api/admin/settings/models', (c): Res<'models'> => {
  const next = merge(c.w, c.body.ai)
  const issue = problem(c.w, next, false)
  if (issue) throw bad(issue)
  return { models: (MODELS[next.provider] ?? [preset(next.provider)!.defaultModel || 'default']).map((id) => ({ id, label: id })) }
})

/** state → who started a ChatGPT sign-in; single use. The real API keys it by state too (the callback host differs). */
const pendingSignIns = new Map<string, Pick<Ctx, 'w' | 'me'>>()

on('POST', '/api/admin/settings/chatgpt/start', ({ w, me }): Res<'chatgptStart'> => {
  const state = randomBytes(16).toString('base64url')
  pendingSignIns.set(state, { w, me })
  // ponytail: no OpenAI consent page: the URL is the callback an approval would reach.
  return { url: `${API_URL}/auth/callback?code=e2e&state=${state}` }
})

on('POST', '/api/admin/settings/chatgpt/signout', (c): Res<'chatgptSignOut'> => {
  c.w.settings.chatgpt = null
  audit(c, 'settings.chatgpt.signout', null)
  return settingsBody(c.w)
})

/** GET /auth/callback: completes a pending sign-in (provider → chatgpt), then back to the console's Settings page. */
function callback(q: URLSearchParams) {
  const state = q.get('state') ?? ''
  const who = pendingSignIns.get(state)
  pendingSignIns.delete(state)
  if (!who) return `${WEB_URL}/settings?${new URLSearchParams({ chatgpt: 'error', message: 'This sign-in expired. Start again from Settings.' })}`
  who.w.settings.chatgpt = { email: 'owner@example.com' }
  who.w.settings.ai = { ...who.w.settings.ai, provider: 'chatgpt', key: '' }
  audit(who, 'settings.chatgpt.signin', null)
  return `${WEB_URL}/settings?chatgpt=ok`
}

// Mock-only control, never in the real API: change the signed-in account's role (e.g. an admin demoted mid-session).
on('POST', '/__e2e/role', (c) => {
  c.me.role = role(c.body.role)
}, 204)

// ── Server ──────────────────────────────────────────────────────────────────

/** token → its world. A world is created per sign-in (see World above). */
const worlds = new Map<string, World>()
const MUTATIONS = { max: 60, windowMs: 10 * 60_000 }

function cors(req: IncomingMessage, res: ServerResponse) {
  if (req.headers.origin !== WEB_URL) return
  res.setHeader('Access-Control-Allow-Origin', WEB_URL)
  res.setHeader('Access-Control-Allow-Credentials', 'true')
  res.setHeader('Vary', 'Origin')
}

const json = (res: ServerResponse, status: number, body?: unknown) => {
  if (status === 204) return void res.writeHead(204).end()
  res.writeHead(status, { 'Content-Type': 'application/json' }).end(JSON.stringify(body))
}

async function handle(req: IncomingMessage, res: ServerResponse) {
  cors(req, res)
  const url = new URL(req.url ?? '/', API_URL)
  const method = req.method ?? 'GET'
  if (method === 'OPTIONS') {
    res.setHeader('Access-Control-Allow-Methods', 'GET,POST,PUT,PATCH,DELETE,OPTIONS')
    res.setHeader('Access-Control-Allow-Headers', 'Content-Type')
    return void res.writeHead(204).end()
  }
  if (url.pathname === '/healthz') return void res.writeHead(200, { 'Content-Type': 'text/plain' }).end('ok')
  if (method === 'GET' && url.pathname === '/auth/callback') return void res.writeHead(302, { Location: callback(url.searchParams) }).end()
  if (url.pathname.startsWith('/api/admin/')) res.setHeader('Cache-Control', 'no-store')

  let raw = ''
  for await (const chunk of req) raw += chunk
  // CSRF rule: every non-GET carries a JSON content type, and a cross-origin request must come from an allowed origin.
  if (method !== 'GET' && (!req.headers['content-type']?.startsWith('application/json') || (req.headers.origin && req.headers.origin !== WEB_URL)))
    throw new Fail(403, 'forbidden', 'cross-site or non-JSON request')
  let body: Record<string, unknown>
  try {
    body = raw ? JSON.parse(raw) : {}
  } catch {
    throw bad('invalid JSON')
  }

  if (method === 'POST' && url.pathname === '/api/auth/login') {
    const w = createWorld()
    const a = [...w.accounts.values()].find((x) => x.email === String(body.email ?? '').trim().toLowerCase())
    if (!a || a.password !== body.password || a.disabledAt) throw new Fail(401, 'invalid_credentials', 'Wrong email or password.')
    const token = randomBytes(32).toString('base64url')
    w.sessions.set(token, a.id)
    worlds.set(token, w)
    a.lastLoginAt = a.lastActiveAt = now()
    res.setHeader('Set-Cookie', `tsid=${token}; Path=/; HttpOnly; SameSite=Lax; Max-Age=${7 * 86400}`)
    return json(res, 200, { user: { id: a.id, email: a.email, name: a.name, role: a.role } } satisfies Res<'login'>)
  }

  const token = /(?:^|;\s*)tsid=([^;]+)/.exec(req.headers.cookie ?? '')?.[1] ?? ''
  const w = worlds.get(token)
  const me = w?.sessions.has(token) ? w.accounts.get(w.sessions.get(token)!) : undefined
  if (!w || !me || me.disabledAt) throw new Fail(401, 'unauthorized', 'Sign in to continue.')
  me.lastActiveAt = now()

  if (method === 'POST' && url.pathname === '/api/auth/logout') {
    w.sessions.delete(token)
    worlds.delete(token)
    res.setHeader('Set-Cookie', 'tsid=; Path=/; HttpOnly; SameSite=Lax; Max-Age=0')
    return json(res, 204)
  }
  if (method === 'GET' && url.pathname === '/api/me') {
    const limit = me.creditLimit ?? DEFAULT_LIMIT
    const resetsAt = new Date(midnight() + DAY).toISOString()
    return json(res, 200, { user: { id: me.id, email: me.email, name: me.name, role: me.role }, credits: { used: me.usage[29], limit, resetsAt } } satisfies Res<'me'>)
  }

  if (url.pathname.startsWith('/api/admin/')) {
    if (me.role !== 'admin') throw new Fail(403, 'forbidden', 'Admins only.')
    if (method !== 'GET') {
      const m = w.mutations.get(me.id)
      const fresh = !m || Date.now() - m.start >= MUTATIONS.windowMs
      const count = fresh ? 1 : m.count + 1
      w.mutations.set(me.id, { start: fresh ? Date.now() : m.start, count })
      if (count > MUTATIONS.max) throw new Fail(429, 'rate_limited', 'Too many admin changes. Try again in a few minutes.')
    }
  }

  for (const r of routes) {
    const m = r.method === method && r.re.exec(url.pathname)
    if (!m) continue
    const out = r.handler({ w, me, token, body, q: url.searchParams, id: decodeURIComponent(m[1] ?? '') })
    return json(res, r.status, out)
  }
  throw new Fail(404, 'not_found', `mock API has no ${method} ${url.pathname}`)
}

/** Starts the mock on loopback only (it accepts the fixture admin's password); port 0 picks a free one (unit tests). */
export function startMockApi(port = API_PORT): Promise<Server> {
  const server = createServer((req, res) =>
    handle(req, res).catch((err: unknown) => {
      const e = err instanceof Fail ? err : new Fail(500, 'internal', String(err))
      if (res.headersSent) return void res.end()
      json(res, e.status, { error: { code: e.code, message: e.message } })
    }),
  )
  return new Promise((resolve, reject) => server.once('error', reject).listen(port, '127.0.0.1', () => resolve(server)))
}

export const portOf = (s: Server) => (s.address() as AddressInfo).port

/** Playwright globalSetup: start the mock API for the run; the returned function stops it. */
export default async function globalSetup() {
  const server = await startMockApi()
  return () => new Promise<void>((resolve) => server.close(() => resolve()))
}

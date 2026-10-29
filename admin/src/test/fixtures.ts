// Contract-shaped sample data (API-CONTRACT.md → Admin console) for component and page tests.
import type {
  AdminOverview,
  AdminUser,
  AdminUserDetail,
  AISettings,
  AuditEntry,
  DayValue,
  Portfolio,
  ProviderInfo,
  Settings,
  SystemStatus,
  Trade,
  User,
} from '../lib/types'

export const admin: User = { id: 'a1', email: 'admin@example.com', name: 'Admin', role: 'admin' }
export const member: User = { id: 'u1', email: 'demo@example.com', name: 'Demo Investor', role: 'user' }
export const credits = { used: 0, limit: 500, resetsAt: '2026-10-04T00:00:00Z' }

/** 30 UTC days ending 2026-10-03; `values` fill from the oldest. */
export const days = (values: number[] = []): DayValue[] =>
  Array.from({ length: 30 }, (_, i) => ({
    day: new Date(Date.UTC(2026, 9, 3) - (29 - i) * 86_400_000).toISOString().slice(0, 10),
    value: values[i] ?? 0,
  }))

export const adminUser = (over: Partial<AdminUser> = {}): AdminUser => ({
  id: 'u1',
  email: 'demo@example.com',
  name: 'Demo Investor',
  role: 'user',
  status: 'active',
  managed: false,
  createdAt: '2026-09-01T08:00:00Z',
  lastLoginAt: '2026-10-02T09:00:00Z',
  lastActiveAt: '2026-10-02T10:00:00Z',
  disabledAt: null,
  creditsToday: 12,
  creditLimit: null,
  equity: 100194,
  sessions: 2,
  ...over,
})

export const liveArena = (over: Partial<SystemStatus['jobs']['liveArena']> = {}): SystemStatus['jobs']['liveArena'] => ({
  enabled: true,
  startedAt: '2026-10-01T00:00:00Z',
  lastStepAt: '2026-10-03T04:00:00Z',
  nextStepAt: '2026-10-03T08:00:00Z',
  agents: 3,
  benched: 1,
  copyPlans: 8,
  status: 'live',
  stale: false,
  lastError: null,
  attemptsToday: 3,
  requestCeilingToday: 12,
  maxDecisionsPerDay: 18,
  maxRequestsPerDay: 72,
  maxTokensPerRequest: 4096,
  decisionTimeoutSec: 90,
  ...over,
})

export const system = (over: Partial<SystemStatus> = {}): SystemStatus => ({
  version: '627ee75abcde',
  startedAt: '2026-10-01T00:00:00Z',
  uptimeSec: 2 * 86400 + 4 * 3600,
  db: { ok: true, latencyMs: 3 },
  model: 'global.anthropic.claude-sonnet-5-5',
  knowledgeCutoff: '2026-06-30',
  news: { enabled: true, lastIngestAt: new Date(Date.now() - 3600_000).toISOString(), items: 412, briefAt: null, briefAgeMin: 60 },
  brave: { used: 126, limit: 1500 },
  crawler: { base: 'https://crawl.example.com', configured: true, reachable: true },
  jobs: { forecastsRunning: 1, arenaRunsRunning: 0, liveArena: liveArena() },
  ...over,
})

export const overview = (over: Partial<AdminOverview> = {}): AdminOverview => ({
  asOf: '2026-10-03T06:00:00Z',
  users: { total: 31, admins: 2, disabled: 3, new30d: 9, active: { today: 4, d7: 12, d30: 20 } },
  credits: { today: 42, d7: 300, d30: 1200, daily: days([...Array(29).fill(40), 42]), defaultLimit: 500 },
  features: [
    { key: 'chat_messages', label: 'Chat messages', today: 5, d7: 40, d30: 150, daily: days([1, 2, 3]) },
    { key: 'forecasts', label: 'Forecasts', today: 1, d7: 6, d30: 20, daily: days() },
    { key: 'strategies', label: 'Strategies', today: 0, d7: 3, d30: 9, daily: days() },
    { key: 'ai_portfolios', label: 'AI portfolios', today: 0, d7: 2, d30: 5, daily: days() },
    { key: 'arena_runs', label: 'Arena runs', today: 0, d7: 1, d30: 2, daily: days() },
    { key: 'orders', label: 'Paper orders', today: 7, d7: 30, d30: 99, daily: days() },
    { key: 'copy_plans', label: 'Copy plans', today: 2, d7: 6, d30: 11, daily: days([5, ...Array(22).fill(0), 4, 0, 0, 0, 0, 0, 2]) },
  ],
  topUsers: [{ id: 'u1', email: 'demo@example.com', name: 'Demo Investor', credits: 120 }],
  paper: { accounts: 20, openPositions: 7, equity: 2_001_234, totalPnl: 1234, notional30d: 250_000 },
  system: system(),
  ...over,
})

export const portfolio: Portfolio = {
  cash: 94000,
  equity: 100194,
  startingBalance: 100000,
  unrealizedPnl: 200,
  realizedPnl: -6,
  totalPnl: 194,
  totalPnlPct: 0.19,
  grossExposure: 6000,
  buyingPower: 194388,
  positions: [
    { symbol: 'ETH', qty: 2, avgPrice: 2900, markPrice: 3000, marketValue: 6000, unrealizedPnl: 200, unrealizedPnlPct: 3.45, stopLoss: null, takeProfit: null },
  ],
}

export const trade: Trade = {
  id: 't1',
  symbol: 'ETH',
  side: 'buy',
  qty: 2,
  price: 2900,
  notional: 5800,
  fee: 5.8,
  realizedPnl: -5.8,
  source: 'manual',
  note: '',
  createdAt: '2026-10-01T12:00:00Z',
}

export const auditEntry = (over: Partial<AuditEntry> = {}): AuditEntry => ({
  id: 'e1',
  action: 'user.update',
  admin: { id: 'a1', email: 'admin@example.com' },
  target: { id: 'u1', email: 'demo@example.com' },
  detail: { role: { from: 'user', to: 'admin' } },
  createdAt: '2026-10-02T09:30:00Z',
  ...over,
})

export const detail = (over: Partial<AdminUserDetail> = {}): AdminUserDetail => ({
  user: adminUser(),
  defaultCreditLimit: 500,
  usage: days([...Array(29).fill(10), 12]),
  counts: { chats: 2, chatMessages: 14, forecasts: 1, strategies: 1, aiPortfolios: 1, arenaRuns: 1, trades: 1 },
  portfolio,
  trades: [trade],
  activity: {
    chats: [{ id: 'c1', title: 'Is BTC overbought right now?', status: null, at: '2026-10-02T08:00:00Z' }],
    forecasts: [{ id: 'f1', title: 'Will ETH outperform BTC?', status: 'running', at: '2026-10-02T08:00:00Z' }],
    strategies: [],
    aiPortfolios: [],
    arenaRuns: [],
  },
  audit: [auditEntry()],
  ...over,
})

// ── Settings ──

const preset = (p: Pick<ProviderInfo, 'id' | 'label' | 'wire'> & Partial<ProviderInfo>): ProviderInfo => ({
  defaultBaseUrl: '',
  defaultModel: '',
  needsKey: false,
  effortDefault: false,
  ...p,
})

export const providers: ProviderInfo[] = [
  preset({ id: 'anthropic', label: 'Anthropic', wire: 'messages', defaultBaseUrl: 'https://api.anthropic.com', defaultModel: 'claude-sonnet-5-5', needsKey: true, effortDefault: true, keyUrl: 'https://console.anthropic.com/settings/keys' }),
  preset({ id: 'chatgpt', label: 'ChatGPT plan', wire: 'responses', defaultBaseUrl: 'https://api.openai.com/v1', effortDefault: true }),
  preset({ id: 'bedrock', label: 'Amazon Bedrock', wire: 'converse', defaultModel: 'global.anthropic.claude-sonnet-5-5', effortDefault: true }),
  preset({ id: 'openrouter', label: 'OpenRouter', wire: 'chat', defaultBaseUrl: 'https://openrouter.ai/api/v1', needsKey: true, effortDefault: true, keyUrl: 'https://openrouter.ai/keys' }),
  preset({ id: 'ollama', label: 'Ollama', wire: 'chat', defaultBaseUrl: 'http://host.docker.internal:11434/v1' }),
  preset({ id: 'custom', label: 'Custom server', wire: 'chat', note: 'Any OpenAI-compatible server: vLLM, llama.cpp, a gateway.' }),
]

export const aiSettings = (over: Partial<AISettings> = {}): AISettings => ({
  provider: 'openrouter',
  model: 'qwen/qwen4-72b',
  baseUrl: '',
  effort: '',
  awsRegion: '',
  knowledgeCutoff: '2026-06-30',
  apiKey: { set: true, hint: '…a1b2' },
  source: 'db',
  readOnly: false,
  chatgpt: { signedIn: false, email: '', error: '' },
  status: 'ready',
  error: '',
  ...over,
})

export const settings = (ai: Partial<AISettings> = {}, over: Partial<Settings> = {}): Settings => ({
  ai: aiSettings(ai),
  search: {
    braveApiKey: { set: true, hint: '…9f3c', source: 'env' },
    crawlBase: '',
    crawlBaseSource: '',
    crawlToken: { set: true, hint: '', source: 'db' },
  },
  providers,
  secretKeySet: true,
  ...over,
})

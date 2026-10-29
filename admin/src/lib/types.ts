// Types from docs/API-CONTRACT.md: the auth and paper shapes the console reads, and every type of
// §3 "Admin console". JSON keys are camelCase; timestamps are ISO-8601 strings; money is USD.

export type Role = 'user' | 'admin'

export type User = { id: string; email: string; name: string; role: Role }
export type Credits = { used: number; limit: number; resetsAt: string } // per user per UTC day

// ── Paper trading ───────────────────────────────────────────────────────────

export type Position = {
  symbol: string
  qty: number // signed: < 0 = short
  avgPrice: number
  markPrice: number
  marketValue: number
  unrealizedPnl: number
  unrealizedPnlPct: number
  stopLoss: number | null
  takeProfit: number | null
}

export type Portfolio = {
  cash: number
  equity: number
  startingBalance: number
  unrealizedPnl: number
  realizedPnl: number
  totalPnl: number
  totalPnlPct: number
  grossExposure: number
  buyingPower: number
  positions: Position[]
}

export type TradeSource = 'manual' | 'copilot' | 'agent' | 'portfolio' | 'stop_loss' | 'take_profit' | 'autopilot' | 'alert' | 'copy'

export type Trade = {
  id: string
  symbol: string
  side: 'buy' | 'sell'
  qty: number
  price: number
  notional: number
  fee: number
  realizedPnl: number // net of fee: opening trades show −fee
  source: TradeSource
  note: string
  createdAt: string
}

// ── Admin console ───────────────────────────────────────────────────────────

export type DayValue = { day: string /* YYYY-MM-DD, UTC */; value: number }
export type Windowed = { today: number; d7: number; d30: number }

export type UserStatus = 'active' | 'disabled'

export type AdminUser = {
  id: string
  email: string
  name: string
  role: Role
  status: UserStatus
  managed: boolean // DEMO_EMAIL / ADMIN_EMAIL account: its password comes from the server config
  createdAt: string
  lastLoginAt: string | null
  lastActiveAt: string | null // ±5 min
  disabledAt: string | null
  creditsToday: number
  creditLimit: number | null // null = the default (AI_DAILY_CREDITS)
  equity: number | null // null when market data is unavailable
  sessions: number
}

export type FeatureKey = 'chat_messages' | 'forecasts' | 'strategies' | 'ai_portfolios' | 'arena_runs' | 'orders' | 'copy_plans'

export type SystemStatus = {
  version: string
  startedAt: string
  uptimeSec: number
  db: { ok: boolean; latencyMs: number }
  model: string
  knowledgeCutoff: string // YYYY-MM-DD
  news: {
    enabled: boolean
    lastIngestAt: string | null
    items: number
    briefAt: string | null
    briefAgeMin: number | null
  }
  brave: { used: number; limit: number }
  crawler: { base: string; configured: boolean; reachable: boolean | null }
  jobs: {
    forecastsRunning: number
    arenaRunsRunning: number
    liveArena: {
      enabled: boolean
      startedAt: string | null
      lastStepAt: string | null
      nextStepAt: string | null
      agents: number
      benched: number
      copyPlans: number // active plans, all users
      status: 'live' | 'starting' | 'paused' | 'stale' | 'outage'
      stale: boolean
      lastError: string | null
      attemptsToday: number // durable decision-slot claims, not measured requests
      requestCeilingToday: number // claims × 4, not billed usage
      maxDecisionsPerDay: 18
      maxRequestsPerDay: 72
      maxTokensPerRequest: 4096
      decisionTimeoutSec: 90
    }
  }
}

export type FeatureUsage = Windowed & { key: FeatureKey; label: string; daily: DayValue[] }
export type TopUser = { id: string; email: string; name: string; credits: number }

export type AdminOverview = {
  asOf: string
  users: { total: number; admins: number; disabled: number; new30d: number; active: Windowed }
  credits: Windowed & { daily: DayValue[]; defaultLimit: number }
  features: FeatureUsage[] // all seven, in FeatureKey order; copy_plans counts created plans, any status
  topUsers: TopUser[] // ≤ 10 by credits over the last 7 days
  paper: { accounts: number; openPositions: number; equity: number | null; totalPnl: number | null; notional30d: number }
  system: SystemStatus
}

export type ActivityItem = { id: string; title: string; status: string | null; at: string }

export type AuditAction =
  | 'user.create'
  | 'user.update'
  | 'user.disable'
  | 'user.enable'
  | 'user.password'
  | 'user.sessions_revoke'
  | 'user.portfolio_reset'
  | 'user.credits_reset'
  | 'user.delete'
  | 'user.seed'
  | 'settings.update'
  | 'settings.chatgpt.signin'
  | 'settings.chatgpt.signout'

export type AuditEntry = {
  id: string
  action: AuditAction
  admin: { id: string; email: string } // as it was then
  target: { id: string; email: string } | null
  detail: Record<string, unknown> // per action; never a password
  createdAt: string
}

export type AdminUserDetail = {
  user: AdminUser
  defaultCreditLimit: number
  usage: DayValue[] // AI credits per UTC day, 30 points
  counts: {
    chats: number
    chatMessages: number
    forecasts: number
    strategies: number
    aiPortfolios: number
    arenaRuns: number
    trades: number
  }
  portfolio: Portfolio | null
  trades: Trade[] // ≤ 20, newest first
  activity: {
    chats: ActivityItem[]
    forecasts: ActivityItem[]
    strategies: ActivityItem[]
    aiPortfolios: ActivityItem[]
    arenaRuns: ActivityItem[]
  }
  audit: AuditEntry[] // ≤ 20 newest entries whose target is this user
}

export type UserSort = 'createdAt' | 'lastActiveAt' | 'lastLoginAt' | 'email' | 'name' | 'creditsToday'
export type SortOrder = 'asc' | 'desc'

export type UserQuery = {
  q?: string
  status?: 'all' | UserStatus
  role?: Role
  sort?: UserSort
  order?: SortOrder
  page?: number
  limit?: number
}

export type UserList = { users: AdminUser[]; total: number; page: number; limit: number; defaultCreditLimit: number }

export type CreateUserRequest = {
  email: string
  name: string
  role?: Role
  creditLimit?: number | null
  password?: string // omitted → the server generates one and returns it once
  seedFrom?: string // email of an account whose showcase content is copied in (same transaction)
}

/** Showcase rows copied into an account (POST /api/admin/users seedFrom, POST /api/admin/users/{id}/seed). */
export type Seeded = { forecasts: number; portfolios: number; strategies: number; arenaRuns: number }

export type UpdateUserRequest = { name?: string; role?: Role; creditLimit?: number | null; disabled?: boolean }

export type AuditList = { entries: AuditEntry[]; total: number; page: number; limit: number }

// ── Settings (AI provider and search keys) ──────────────────────────────────

/** One provider preset (llm.ProviderInfo). */
export type ProviderInfo = {
  id: string // "openrouter", "ollama", "chatgpt", "bedrock", …
  label: string
  wire: 'chat' | 'messages' | 'responses' | 'converse'
  defaultBaseUrl: string
  defaultModel: string
  keyEnv?: string // vendor env fallback for AI_API_KEY
  needsKey: boolean
  effortDefault: boolean
  keyUrl?: string // where to get a key
  note?: string
}

export type ModelInfo = { id: string; label: string }

/** A stored secret: never the value. `hint` is "…a1b2" for keys of 16+ characters, else "". */
export type Secret = { set: boolean; hint: string }
export type Source = 'db' | 'env' | ''
export type Effort = '' | 'on' | 'off' // "" = the preset's default

export type AISettings = {
  provider: string // "" = AI off
  model: string
  baseUrl: string
  effort: Effort
  awsRegion: string
  knowledgeCutoff: string // YYYY-MM-DD
  apiKey: Secret
  source: 'db' | 'env' | 'none'
  readOnly: boolean // source == "env": AI_PROVIDER is set
  chatgpt: { signedIn: boolean; email: string; error: string }
  status: 'off' | 'ready' | 'error'
  error: string
}

export type SearchSettings = {
  braveApiKey: Secret & { source: Source }
  crawlBase: string
  crawlBaseSource: Source
  crawlToken: Secret & { source: Source }
  error?: string // a stored search key can't be decrypted
}

export type Settings = { ai: AISettings; search: SearchSettings; providers: ProviderInfo[]; secretKeySet: boolean }

/** A partial AI config: PUT saves it (after a test), /test and /models merge it over the saved one. */
export type AIDraft = Partial<Pick<AISettings, 'provider' | 'model' | 'baseUrl' | 'effort' | 'awsRegion' | 'knowledgeCutoff'>> & {
  apiKey?: string // absent or "" keeps the saved key, unless the provider changes
  clearKey?: boolean
}

export type SearchDraft = {
  braveApiKey?: string
  clearBraveApiKey?: boolean
  crawlBase?: string
  crawlToken?: string
  clearCrawlToken?: boolean
}

export type TestResult = { ok: true; provider: string; model: string; latencyMs: number } | { ok: false; error: string }

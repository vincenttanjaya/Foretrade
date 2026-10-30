// Typed client for the Go API. Every function returns the exact JSON body from API-CONTRACT.md.
import type {
  AIPortfolio,
  AttachmentInput,
  Alert,
  AlertRequest,
  AlertRunPage,
  AthQuote,
  Notification,
  NotificationChannels,
  ChannelTestResult,
  UpdateNotificationChannels,
  Analysis,
  ArenaOptions,
  ArenaRun,
  ArenaRunSummary,
  ArenaStep,
  LiveBoard,
  CopyPlan,
  Asset,
  Autopilot,
  BacktestResult,
  Candle,
  ChartInterval,
  Derivatives,
  Chat,
  ChatStreamEvent,
  ChatStreamRequest,
  ChatSummary,
  CopilotPlanRequest,
  DeskRead,
  DeskReference,
  CreateArenaRunRequest,
  DeployAutopilotRequest,
  Credits,
  EquityCurve,
  EquityRange,
  Forecast,
  ForecastDraft,
  ForecastShape,
  ForecastStatus,
  ForecastSummary,
  GenerateStrategyRequest,
  Instrument,
  MarketBrief,
  MarketPulse,
  MarketPosition,
  NewsItem,
  NewsRegion,
  Interval,
  OptimizeResult,
  OrderRequest,
  Plan,
  Portfolio,
  PortfolioReview,
  PriceOutlook,
  PredictionBoard,
  PredictionDraft,
  PredictionCategory,
  PredictionSort,
  ProtectionRequest,
  ScreenerPage,
  ScreenerQuery,
  ScreenerQuotes,
  SignalsResponse,
  SourcePnl,
  Strategy,
  StrategySpec,
  StrategySummary,
  Trade,
  TrackRecord,
  User,
  Profile,
  UpdateProfileRequest,
  ConfirmMemoryRequest,
  Memory,
} from './types'

export const API_URL = (import.meta.env.VITE_API_URL || 'http://localhost:8080').replace(/\/+$/, '')

export class ApiError extends Error {
  readonly status: number // 0 = network failure
  readonly code: string

  constructor(status: number, code: string, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
  }
}

// AuthProvider registers this so a 401 anywhere sends the user back to /login.
let onUnauthorized: () => void = () => {}
export function setUnauthorizedHandler(fn: () => void) {
  onUnauthorized = fn
}

/** fetch with cookies + JSON; throws ApiError for network failures and non-2xx responses. */
async function send(method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<Response> {
  const unauthorized = onUnauthorized // A response belongs to the session that started it.
  const init: RequestInit = { method, credentials: 'include', signal }
  // The API's CSRF rule: every non-GET carries a JSON content type, even with an empty body.
  if (method !== 'GET') {
    init.headers = { 'Content-Type': 'application/json' }
    init.body = JSON.stringify(body ?? {})
  }
  let res: Response
  try {
    res = await fetch(API_URL + path, init)
  } catch (err) {
    if (signal?.aborted) throw err
    throw new ApiError(0, 'network', 'Cannot reach the server. Check your connection and try again.')
  }
  if (res.ok) return res

  const err = await res
    .json()
    .then((j: { error?: { code?: string; message?: string } }) => j.error)
    .catch(() => undefined)
  if (res.status === 401 && path !== '/api/auth/login') unauthorized()
  throw new ApiError(res.status, err?.code ?? 'internal', err?.message ?? `Request failed (${res.status})`)
}

async function request<T>(method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  const res = await send(method, path, body, signal)
  return (res.status === 204 ? undefined : await res.json()) as T
}

const get = <T>(path: string, signal?: AbortSignal) => request<T>('GET', path, undefined, signal)
const post = <T>(path: string, body: unknown = {}) => request<T>('POST', path, body)
const del = (path: string) => request<void>('DELETE', path)

const qs = (params: Record<string, string | number | undefined>) => {
  const q = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== '') q.set(k, String(v))
  const s = q.toString()
  return s ? `?${s}` : ''
}
const id = encodeURIComponent

type WithCredits<T> = T & { credits: Credits }

/** Files for a forecast run, and digests to copy from one of the user's earlier runs of the same question. */
export type CreateForecastOptions = { attachments?: AttachmentInput[]; reuseMaterials?: { forecastId: string; ids?: string[] } }
export type UpdateForecastOptions = { attachments?: AttachmentInput[]; keepMaterials?: string[] }

function forecastBody(base: object, opts: CreateForecastOptions & UpdateForecastOptions) {
  return {
    ...base,
    ...(opts.attachments?.length ? { attachments: opts.attachments } : {}),
    ...(opts.reuseMaterials ? { reuseMaterials: opts.reuseMaterials } : {}),
    ...(opts.keepMaterials ? { keepMaterials: opts.keepMaterials } : {}),
  }
}

/** The JSON body's size in bytes, as the API counts it against its upload cap. */
export const bodyBytes = (body: unknown) => new TextEncoder().encode(JSON.stringify(body)).byteLength

export const api = {
  // Auth
  login: (email: string, password: string) => post<{ user: User }>('/api/auth/login', { email, password }),
  logout: () => post<void>('/api/auth/logout'),
  me: (signal?: AbortSignal) => get<{ user: User; credits: Credits }>('/api/me', signal),
  profile: (signal?: AbortSignal) => get<{ profile: Profile }>('/api/me/profile', signal),
  updateProfile: (body: UpdateProfileRequest, signal?: AbortSignal) => request<{ profile: Profile }>('PUT', '/api/me/profile', body, signal),
  confirmMemory: (body: ConfirmMemoryRequest, signal?: AbortSignal) => request<{ memory: Memory; profile: Profile }>('POST', '/api/me/profile/memories', body, signal),
  deleteMemory: (memoryId: string, signal?: AbortSignal) => request<{ profile: Profile }>('DELETE', `/api/me/profile/memories/${id(memoryId)}`, {}, signal),

  // Market data
  // No scope: the named rows (an API without the listed instruments answers this whatever the scope). `named` adds the
  // listed rows named in `withSymbols` (at most 20); `listed` is the listed rows only.
  assets: (scope?: 'named' | 'listed', withSymbols?: string[], signal?: AbortSignal) =>
    get<{ assets: Asset[] }>(`/api/market/assets${qs({ scope, with: withSymbols?.slice(0, 20).join(',') })}`, signal),
  /** Every instrument, priced or not (contract §2); `etag` is the response's ETag when the API exposes it, else null. */
  instruments: async (signal?: AbortSignal): Promise<{ instruments: Instrument[]; etag: string | null }> => {
    const res = await send('GET', '/api/market/instruments', undefined, signal)
    const body = (await res.json()) as { instruments: Instrument[] }
    return { ...body, etag: res.headers.get('ETag') }
  },
  candles: (symbol: string, interval: ChartInterval, limit?: number, signal?: AbortSignal, currency?: 'original', before?: number) =>
    get<{ candles: Candle[] }>(`/api/market/candles${qs({ symbol, interval, limit, currency, before })}`, signal),
  analysis: (symbol: string, interval: Interval, signal?: AbortSignal) =>
    get<{ analysis: Analysis }>(`/api/market/analysis${qs({ symbol, interval })}`, signal),
  derivatives: (symbol: string, signal?: AbortSignal) =>
    get<{ derivatives: Derivatives | null }>(`/api/market/derivatives${qs({ symbol })}`, signal),

  // Paper trading
  portfolio: (signal?: AbortSignal) => get<{ portfolio: Portfolio }>('/api/portfolio', signal),
  trades: (limit?: number, signal?: AbortSignal) =>
    get<{ trades: Trade[] }>(`/api/portfolio/trades${qs({ limit })}`, signal),
  placeOrder: (order: OrderRequest) => post<{ trade: Trade; portfolio: Portfolio }>('/api/orders', order),
  closePosition: (symbol: string) => post<{ trade: Trade; portfolio: Portfolio }>('/api/portfolio/close', { symbol }),
  resetPortfolio: () => post<{ portfolio: Portfolio }>('/api/portfolio/reset'),
  portfolioEquity: (range: EquityRange, signal?: AbortSignal) =>
    get<EquityCurve>(`/api/portfolio/equity${qs({ range })}`, signal),
  portfolioAttribution: (signal?: AbortSignal) =>
    get<{ sources: SourcePnl[]; totalPnl: number }>('/api/portfolio/attribution', signal),
  portfolioReview: (signal?: AbortSignal) => get<PortfolioReview>('/api/portfolio/review', signal),
  setProtection: (symbol: string, body: ProtectionRequest) =>
    request<{ portfolio: Portfolio }>('PATCH', `/api/portfolio/positions/${id(symbol)}`, body),

  // Agent chat (streaming lives in streamChat below)
  chats: (signal?: AbortSignal) => get<{ chats: ChatSummary[] }>('/api/chats', signal),
  chat: (chatId: string, signal?: AbortSignal) => get<{ chat: Chat }>(`/api/chats/${id(chatId)}`, signal),
  deleteChat: (chatId: string) => del(`/api/chats/${id(chatId)}`),

  // Copilot
  copilotPlan: (req: CopilotPlanRequest) =>
    post<WithCredits<{ plan: Plan; analysis: Analysis; outlook: PriceOutlook; headlines: NewsItem[] }>>('/api/copilot/plan', req),
  getDesk: (symbol: string, signal?: AbortSignal) => get<{ desk: DeskRead | null }>(`/api/copilot/desk${qs({ symbol })}`, signal),

  // Market intelligence
  news: (filter: { symbol?: string; topic?: 'asset' | 'macro'; region?: NewsRegion; limit?: number } = {}, signal?: AbortSignal) =>
    get<{ news: NewsItem[] }>(`/api/news${qs(filter)}`, signal),
  brief: (signal?: AbortSignal) => get<{ brief: MarketBrief | null }>('/api/news/brief', signal),
  getPulse: (signal?: AbortSignal) => get<MarketPulse>('/api/news/pulse', signal),

  // Forecast desk
  predictionMarkets: (filter: { category?: PredictionCategory; sort?: PredictionSort } = {}, signal?: AbortSignal) =>
    get<PredictionBoard>(`/api/predictions/markets${qs(filter)}`, signal),
  predictionDraft: (marketId: string, signal?: AbortSignal) =>
    get<PredictionDraft>(`/api/predictions/markets/${id(marketId)}/draft`, signal),
  // A long-horizon stock draft whose ranges are cut at multiples of today's level carries that level.
  draftForecast: (idea: string) => post<WithCredits<{ draft: ForecastDraft; reference?: DeskReference | null }>>('/api/forecasts/draft', { idea }),
  // Attachments and reuseMaterials join the body only when set, so a text-only run sends today's JSON.
  createForecast: (draft: ForecastDraft, opts: CreateForecastOptions = {}) =>
    post<WithCredits<{ forecast: Forecast }>>('/api/forecasts', forecastBody(draft, opts)),
  forecasts: (filter: { status?: ForecastStatus; shape?: ForecastShape; scope?: 'mine' | 'replay' } = {}, signal?: AbortSignal) =>
    get<{ forecasts: ForecastSummary[] }>(`/api/forecasts${qs(filter)}`, signal),
  forecast: (forecastId: string, signal?: AbortSignal) =>
    get<{ forecast: Forecast }>(`/api/forecasts/${id(forecastId)}`, signal),
  deleteForecast: (forecastId: string) => del(`/api/forecasts/${id(forecastId)}`),
  restoreForecast: (forecastId: string) => post<{ forecast: Forecast }>(`/api/forecasts/${id(forecastId)}/restore`, {}),
  resolveForecast: (forecastId: string, outcome: string | null) =>
    post<{ forecast: Forecast }>(`/api/forecasts/${id(forecastId)}/resolve`, { outcome }),
  // {} keeps every material the desk read on the parent (5 credits); new files cost 1 each.
  updateForecast: (forecastId: string, opts: UpdateForecastOptions = {}) =>
    post<WithCredits<{ forecast: Forecast }>>(`/api/forecasts/${id(forecastId)}/update`, forecastBody({}, opts)),
  trackRecord: (signal?: AbortSignal) => get<{ replay: TrackRecord; mine: TrackRecord }>('/api/forecasts/track-record', signal),
  // Fresh Polymarket odds and paper prediction positions (0 credits).
  refreshMarket: (forecastId: string, rematch = false) =>
    post<{ forecast: Forecast }>(`/api/forecasts/${id(forecastId)}/market`, rematch ? { rematch } : {}),
  openMarketPosition: (forecastId: string, side: 'yes' | 'no', stake: number) =>
    post<{ position: MarketPosition; portfolio: Portfolio }>(`/api/forecasts/${id(forecastId)}/market-position`, { side, stake }),
  marketPositions: (forecastId?: string, signal?: AbortSignal) =>
    get<{ positions: MarketPosition[] }>(`/api/market-positions${qs({ forecastId })}`, signal),

  // Strategy studio
  generateStrategy: (req: GenerateStrategyRequest) =>
    post<WithCredits<{ strategy: Strategy }>>('/api/strategies/generate', req),
  backtest: (spec: StrategySpec) => post<BacktestResult>('/api/strategies/backtest', { spec }),
  optimize: (spec: StrategySpec) => post<OptimizeResult>('/api/strategies/optimize', { spec }),
  strategies: (scope: 'mine' | 'top' | 'community', signal?: AbortSignal) =>
    get<{ strategies: StrategySummary[] }>(`/api/strategies${qs({ scope })}`, signal),
  strategy: (strategyId: string, signal?: AbortSignal) =>
    get<{ strategy: Strategy }>(`/api/strategies/${id(strategyId)}`, signal),
  deleteStrategy: (strategyId: string) => del(`/api/strategies/${id(strategyId)}`),
  // Community strategies (0 credits)
  publishStrategy: (strategyId: string, published: boolean) =>
    post<{ strategy: Strategy }>(`/api/strategies/${id(strategyId)}/publish`, { published }),
  starStrategy: (strategyId: string, starred: boolean) =>
    post<{ stars: number; starred: boolean }>(`/api/strategies/${id(strategyId)}/star`, { starred }),
  copyStrategy: (strategyId: string) => post<{ strategy: Strategy }>(`/api/strategies/${id(strategyId)}/copy`),
  deployAutopilot: (req: DeployAutopilotRequest) =>
    post<{ autopilot: Autopilot; trade: Trade | null; portfolio: Portfolio | null }>('/api/autopilots', req),
  autopilots: (signal?: AbortSignal) => get<{ autopilots: Autopilot[] }>('/api/autopilots', signal),
  autopilot: (autopilotId: string, signal?: AbortSignal) =>
    get<{ autopilot: Autopilot; trades: Trade[] }>(`/api/autopilots/${id(autopilotId)}`, signal),
  stopAutopilot: (autopilotId: string, closePosition: boolean) =>
    post<{ autopilot: Autopilot; trade: Trade | null; portfolio: Portfolio }>(`/api/autopilots/${id(autopilotId)}/stop`, { closePosition }),

  // AI backtest arena
  arenaOptions: (signal?: AbortSignal) => get<ArenaOptions>('/api/arena/options', signal),
  createArenaRun: (req: CreateArenaRunRequest) =>
    post<WithCredits<{ run: ArenaRun; warnings: string[] }>>('/api/arena/runs', req),
  arenaRuns: (signal?: AbortSignal) => get<{ runs: ArenaRunSummary[] }>('/api/arena/runs', signal),
  arenaRun: (runId: string, signal?: AbortSignal) => get<{ run: ArenaRun }>(`/api/arena/runs/${id(runId)}`, signal),
  /** Steps of one agent after step `after` (-1 = from the start), ascending. */
  arenaSteps: (runId: string, agent: number, after: number, signal?: AbortSignal) =>
    get<{ steps: ArenaStep[] }>(`/api/arena/runs/${id(runId)}/steps${qs({ agent, after })}`, signal),
  cancelArenaRun: (runId: string) => post<WithCredits<{ run: ArenaRun }>>(`/api/arena/runs/${id(runId)}/cancel`),
  deleteArenaRun: (runId: string) => del(`/api/arena/runs/${id(runId)}`),

  // Shared live paper desk and owner-only copy plans
  liveArena: (signal?: AbortSignal) => get<LiveBoard>('/api/arena/live', signal),
  liveArenaSteps: (agentId: string, params: { limit?: number; before?: number } = {}, signal?: AbortSignal) =>
    get<{ steps: ArenaStep[] }>(`/api/arena/live/agents/${id(agentId)}/steps${qs(params)}`, signal),
  copyLiveAgent: (agentId: string, body: { ratio: number; maxUsd: number }) =>
    post<{ plan: CopyPlan }>(`/api/arena/live/agents/${id(agentId)}/copy`, body),
  copyPlans: (signal?: AbortSignal) => get<{ plans: CopyPlan[] }>('/api/copy-plans', signal),
  stopCopyPlan: (planId: string, closePositions: boolean) =>
    post<{ plan: CopyPlan; trades: Trade[]; portfolio: Portfolio | null }>(`/api/copy-plans/${id(planId)}/stop`, { closePositions }),

  // AI portfolios
  buildPortfolio: (prompt: string) => post<WithCredits<{ portfolio: AIPortfolio }>>('/api/ai-portfolios/build', { prompt }),
  aiPortfolios: (signal?: AbortSignal) => get<{ portfolios: AIPortfolio[] }>('/api/ai-portfolios', signal),
  aiPortfolio: (portfolioId: string, signal?: AbortSignal) =>
    get<{ portfolio: AIPortfolio }>(`/api/ai-portfolios/${id(portfolioId)}`, signal),
  investPortfolio: (portfolioId: string, amount: number) =>
    post<{ trades: Trade[]; portfolio: Portfolio; skipped?: { symbol: string; reason: string }[] }>(
      `/api/ai-portfolios/${id(portfolioId)}/invest`,
      { amount },
    ),

  // Signals board
  signals: (signal?: AbortSignal) => get<SignalsResponse>('/api/signals', signal),

  // Asia screener (live prices on the latest run's averages, 0 credits)
  screener: (query: ScreenerQuery = {}, signal?: AbortSignal) => get<ScreenerPage>(`/api/screener/asia${qs(query)}`, signal),
  // The loaded rows again, 1–100 tickers, for in-place refresh; an API before the live screener answers 404.
  screenerQuotes: (tickers: string[], signal?: AbortSignal) =>
    get<ScreenerQuotes>(`/api/screener/asia/quotes${qs({ tickers: tickers.join(',') })}`, signal),

  // Automations: price alerts, paper limit orders and the notifications bell (0 credits)
  alerts: (status: 'active' | 'all' = 'all', signal?: AbortSignal) => get<{ alerts: Alert[] }>(`/api/alerts${qs({ status })}`, signal),
  createAlert: (req: AlertRequest) => post<{ alert: Alert }>('/api/alerts', req),
  alertATH: (symbol: string, signal?: AbortSignal) => get<{ ath: AthQuote }>(`/api/alerts/ath${qs({ symbol })}`, signal), // a drawdown trigger's high
  cancelAlert: (alertId: string) => del(`/api/alerts/${id(alertId)}`),
  resumeAlert: (alertId: string) => post<{ alert: Alert }>(`/api/alerts/${id(alertId)}/resume`),
  alertRuns: (alertId: string, params: { limit?: number; before?: string } = {}, signal?: AbortSignal) =>
    get<AlertRunPage>(`/api/alerts/${id(alertId)}/runs${qs(params)}`, signal),
  notifications: (limit?: number, signal?: AbortSignal) =>
    get<{ items: Notification[]; unread: number }>(`/api/notifications${qs({ limit })}`, signal),
  notificationsSeen: () => post<void>('/api/notifications/seen'),
  notificationChannels: (signal?: AbortSignal) => get<{ channels: NotificationChannels }>('/api/notifications/channels', signal),
  updateNotificationChannels: (body: UpdateNotificationChannels) =>
    request<{ channels: NotificationChannels }>('PUT', '/api/notifications/channels', body),
  testNotificationChannels: () => post<ChannelTestResult>('/api/notifications/channels/test'),
}

export type SSEFrame = { event: string; data: string }

/**
 * Splits buffered SSE text into complete frames; `rest` is the unfinished tail to prepend to the
 * next chunk. Handles LF/CRLF/CR line endings (including a CRLF split across chunks), multi-line
 * `data:`, comments and frames without an `event:` field.
 */
export function parseSSE(input: string): { frames: SSEFrame[]; rest: string } {
  // A trailing \r may be the first half of a \r\n: leave it for the next call.
  const cut = input.endsWith('\r') ? input.length - 1 : input.length
  const blocks = input.slice(0, cut).replace(/\r\n?/g, '\n').split('\n\n')
  const rest = blocks.pop()! + input.slice(cut)
  const frames: SSEFrame[] = []
  for (const block of blocks) {
    let event = 'message'
    const data: string[] = []
    for (const line of block.split('\n')) {
      if (!line || line.startsWith(':')) continue
      const i = line.indexOf(':')
      const field = i < 0 ? line : line.slice(0, i)
      const value = i < 0 ? '' : line.slice(line[i + 1] === ' ' ? i + 2 : i + 1)
      if (field === 'event') event = value
      else if (field === 'data') data.push(value)
    }
    if (data.length) frames.push({ event, data: data.join('\n') })
  }
  return { frames, rest }
}

const STREAM_EVENTS = new Set<string>(['chat', 'tool_call', 'tool_result', 'delta', 'card', 'done', 'error'])

// The upload body cap the API enforces on chat messages and forecast runs: five 4.5 MB documents are 30,000,000 base64
// bytes, and 32 MiB leaves room for the text, names and JSON. Checked here so an oversize body gets a clear error.
export const ATTACHMENT_BODY_LIMIT = 33_554_432
export const CHAT_BODY_LIMIT = ATTACHMENT_BODY_LIMIT

/**
 * POST /api/chat/stream and deliver each event to `onEvent`. Never rejects: HTTP errors (e.g. 429
 * ai_quota), network failures and a stream that ends without `done` all arrive as an `error` event.
 * Aborting `signal` stops the stream quietly.
 */
export async function streamChat(
  body: ChatStreamRequest,
  onEvent: (ev: ChatStreamEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  let finished = false
  const emit = (ev: ChatStreamEvent) => {
    if (ev.event === 'done' || ev.event === 'error') finished = true
    onEvent(ev)
  }
  try {
    if (bodyBytes(body) > CHAT_BODY_LIMIT) throw new ApiError(400, 'bad_request', 'This message is too large to send. Remove an attachment and try again.')
    const res = await send('POST', '/api/chat/stream', body, signal)
    if (!res.body) throw new ApiError(0, 'upstream', 'Streaming is not supported by this browser.')
    const reader = res.body.getReader()
    const decoder = new TextDecoder()
    let buf = ''
    for (;;) {
      const { value, done } = await reader.read()
      buf += done ? decoder.decode() + '\n\n' : decoder.decode(value, { stream: true })
      const { frames, rest } = parseSSE(buf)
      buf = rest
      for (const f of frames) {
        if (!STREAM_EVENTS.has(f.event)) continue
        let data: unknown
        try {
          data = JSON.parse(f.data)
        } catch {
          continue // ponytail: a malformed frame is dropped; the server only writes JSON frames.
        }
        emit({ event: f.event, data } as ChatStreamEvent)
        if (finished) {
          await reader.cancel() // asynchronous cleanup failures go through the terminal/abort guard below
          return
        }
      }
      if (done) break
    }
    if (!finished) emit({ event: 'error', data: { code: 'upstream', message: 'The response ended unexpectedly.' } })
  } catch (err) {
    if (signal?.aborted || finished) return
    const e = err instanceof ApiError ? err : new ApiError(0, 'network', 'Lost connection to the server.')
    emit({ event: 'error', data: { code: e.code, message: e.message } })
  }
}

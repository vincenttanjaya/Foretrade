// Contract-faithful stand-in for the Go API (docs/API-CONTRACT.md) for the Playwright suite:
// cookie sessions, CORS + the CSRF rule, the error envelope, a paper-trading engine and a real SSE chat stream.
// Every response is typed with the client's own signatures (src/lib/api.ts), so contract drift breaks `npm run typecheck`.
// Playwright runs it as globalSetup (default export); mock-api.test.ts unit-tests the engine.
import { randomBytes, randomUUID } from 'node:crypto'
import { createServer, type IncomingMessage, type ServerResponse } from 'node:http'
import { isStockPerp } from '../src/pages/strategies/strategy'
import * as fx from '../src/test/fixtures'
import { linkedMarket, predictionMarket } from '../src/test/predictions'
import { attributeLots, EQUITY_GRIDS, replayPoints, summarizeEquity } from './account-ledger'
import type {
  AIPortfolio,
  Alert,
  AlertRequest,
  AlertRun,
  AlertEvery,
  AllTimeHigh,
  ArenaAgent,
  ArenaAgentRequest,
  ArenaRun,
  ArenaRunSummary,
  Autopilot,
  Candle,
  ChartInterval,
  Chat,
  ChatStreamEvent,
  ChatStreamRequest,
  ChatAttachmentMetadata,
  ForecastMaterial,
  CopilotPlanRequest,
  CreateArenaRunRequest,
  CopyPlan,
  LiveBoard,
  Notification,
  Credits,
  DeskRead,
  Forecast,
  ForecastDraft,
  ForecastPoint,
  ForecastResult,
  ForecastSummary,
  GenerateStrategyRequest,
  Interval,
  Metrics,
  LinkedMarket,
  PredictionBoard,
  PredictionDraft,
  NewsItem,
  OrderRequest,
  UpdateNotificationChannels,
  Point,
  Portfolio,
  PortfolioReview,
  PriceAlertProposal,
  Strategy,
  StrategySummary,
  StrategyForward,
  Trade,
  TradeRef,
  EquityRange,
  SourcePnl,
  Profile,
  MemoryProposal,
  ScreenerFilter,
  ScreenerQuote,
  ScreenerRow,
  User,
} from '../src/lib/types'

export const API_PORT = 4010
export const WEB_PORT = 4180
export const API_URL = `http://localhost:${API_PORT}` // also in package.json "e2e:build" (VITE_API_URL)
export const WEB_URL = `http://localhost:${WEB_PORT}`
export const DEMO = { email: fx.user.email, password: 'paper-only-e2e' } // test-only credentials, nothing real

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

const now = () => new Date().toISOString()
const hoursAgo = (h: number) => new Date(Date.now() - h * 3600_000).toISOString()
const round2 = (n: number) => Math.round(n * 100) / 100
// Reverse booking order first, then stable timestamp order: matches API created_at, seq FIFO capture.
const orderedTrades = (s: State) => [...s.trades].reverse().sort((a, b) => a.createdAt.localeCompare(b.createdAt))
// The named universe, then the listed Asian stocks (contract §2), as GET /api/market/instruments lists them.
const tradable = [...fx.universeAssets, ...fx.listedAssets]
const prices = Object.fromEntries(tradable.map((a) => [a.symbol, a.price]))
const mark = (symbol: string) => {
  const p = prices[symbol]
  if (!p) throw new Fail(400, 'bad_request', `unknown symbol ${symbol}`)
  return p
}

// ── Per-session state ───────────────────────────────────────────────────────
// ponytail: every login gets a fresh state, so parallel tests never share a book. The real API keys state by user;
// the demo has one shared login, which would make parallel tests race.

type Lot = { qty: number; avgPrice: number; stopLoss: number | null; takeProfit: number | null }

export type State = {
  ownerId: string
  profile: Profile
  memoryReceipts: Map<string, { memoryId: string; status: 'confirmed' | 'deleted'; at: string }>
  revision: number
  credits: Credits
  cash: number
  realized: number
  positions: Map<string, Lot>
  trades: Trade[] // newest first
  chats: Map<string, Chat & { updatedAt: string }>
  fills: Map<string, string> // agent card proposalId → the trade it filled (a reset drops the trade, so the card reopens)
  forecasts: Map<string, Forecast>
  forecastPolls: Map<string, number>
  strategies: Map<string, Strategy> // the user's own
  portfolios: AIPortfolio[] // newest first
  runs: Map<string, ArenaRun>
}

const START = 100_000
export const SEED = { chat: 'c-seed', forecast: 'f-seed', portfolio: 'p-seed', run: 'r-seed' } // fixed ids of the seeded items

export function createState(): State {
  const s: State = {
    ownerId: randomUUID(),
    profile: fx.profile(),
    memoryReceipts: new Map(),
    revision: 0,
    credits: { ...fx.credits },
    cash: START,
    realized: 0,
    positions: new Map(),
    trades: [],
    chats: new Map(),
    fills: new Map(),
    forecasts: new Map(),
    forecastPolls: new Map(),
    strategies: new Map(),
    portfolios: [],
    runs: new Map(),
  }
  // A lived-in account, so every detail page has something to show on its first visit.
  placeOrder(s, { symbol: 'ETH', side: 'buy', notional: 6000, source: 'manual' })
  s.chats.set(SEED.chat, { ...seedChat(), updatedAt: hoursAgo(2) })
  s.forecasts.set(SEED.forecast, fx.forecast({ id: SEED.forecast, createdAt: hoursAgo(26), completedAt: hoursAgo(25) }))
  s.portfolios.push(fx.aiPortfolio({ id: SEED.portfolio, createdAt: hoursAgo(30) }))
  s.runs.set(SEED.run, arenaRun(SEED.run, { cadence: '1d', start: '2026-08-03', end: '2026-10-01', agents: [{ personaId: 'momentum' }, { personaId: 'contrarian' }] }, 60))
  return s
}

// ── Paper engine (contract "Paper trading") ─────────────────────────────────

const SLIPPAGE = 0.0002
const FEE = 0.001

export function portfolioOf(s: State): Portfolio {
  const positions = [...s.positions].map(([symbol, l]) => {
    const markPrice = mark(symbol)
    const unrealizedPnl = l.qty * (markPrice - l.avgPrice)
    return {
      symbol,
      qty: l.qty,
      avgPrice: l.avgPrice,
      markPrice,
      marketValue: l.qty * markPrice,
      unrealizedPnl,
      unrealizedPnlPct: (unrealizedPnl / (Math.abs(l.qty) * l.avgPrice)) * 100,
      stopLoss: l.stopLoss,
      takeProfit: l.takeProfit,
    }
  })
  const equity = s.cash + positions.reduce((a, p) => a + p.marketValue, 0)
  const grossExposure = positions.reduce((a, p) => a + Math.abs(p.marketValue), 0)
  const unrealizedPnl = positions.reduce((a, p) => a + p.unrealizedPnl, 0)
  return {
    revision: s.revision,
    cash: s.cash,
    equity,
    startingBalance: START,
    unrealizedPnl,
    realizedPnl: s.realized,
    totalPnl: equity - START,
    totalPnlPct: ((equity - START) / START) * 100,
    grossExposure,
    buyingPower: Math.max(0, 2 * equity - grossExposure),
    positions,
  }
}

/** Fills a market order at the mark ± 2 bps with a 0.1% fee; average-cost accounting, signed positions. */
export function placeOrder(s: State, o: OrderRequest): Trade {
  const price = mark(o.symbol)
  if (o.side !== 'buy' && o.side !== 'sell') throw new Fail(400, 'bad_request', 'side must be buy or sell')
  if ((o.notional == null) === (o.qty == null)) throw new Fail(400, 'bad_request', 'send exactly one of notional or qty')
  if (o.notional != null && !(o.notional >= 10)) throw new Fail(400, 'bad_request', 'the minimum order is $10')
  if (o.qty != null && !(o.qty > 0)) throw new Fail(400, 'bad_request', 'qty must be positive')

  const px = price * (o.side === 'buy' ? 1 + SLIPPAGE : 1 - SLIPPAGE)
  const qty = o.qty ?? o.notional! / px
  const dq = o.side === 'buy' ? qty : -qty
  const lot = s.positions.get(o.symbol) ?? { qty: 0, avgPrice: 0, stopLoss: null, takeProfit: null }
  const after = lot.qty + dq
  const long = after > 0
  const below = (v: number) => (long ? v < px : v > px)
  if (o.stopLoss != null && !below(o.stopLoss)) throw new Fail(400, 'bad_request', 'stopLoss is on the wrong side of the price')
  if (o.takeProfit != null && below(o.takeProfit)) throw new Fail(400, 'bad_request', 'takeProfit is on the wrong side of the price')

  const reducing = lot.qty !== 0 && Math.sign(dq) !== Math.sign(lot.qty)
  const closed = reducing ? Math.min(Math.abs(dq), Math.abs(lot.qty)) : 0
  const realized = closed * (px - lot.avgPrice) * Math.sign(lot.qty)
  const notional = qty * px
  const fee = notional * FEE
  const next: Lot = {
    qty: after,
    avgPrice: !reducing ? (Math.abs(lot.qty) * lot.avgPrice + qty * px) / Math.abs(after) : Math.sign(after) === Math.sign(lot.qty) ? lot.avgPrice : px,
    stopLoss: o.stopLoss ?? lot.stopLoss,
    takeProfit: o.takeProfit ?? lot.takeProfit,
  }

  const before = { cash: s.cash, realized: s.realized, lot: s.positions.get(o.symbol) }
  s.cash -= dq * px + fee
  s.realized += realized - fee
  if (Math.abs(after * px) < 0.01) s.positions.delete(o.symbol)
  else s.positions.set(o.symbol, next)
  if (Math.abs(after) > Math.abs(lot.qty)) {
    const p = portfolioOf(s)
    if (p.grossExposure > 2 * p.equity || p.equity <= 0) {
      s.cash = before.cash
      s.realized = before.realized
      if (before.lot) s.positions.set(o.symbol, before.lot)
      else s.positions.delete(o.symbol)
      throw new Fail(400, 'insufficient_buying_power', 'This order would take gross exposure above 2× equity.')
    }
  }

  const trade: Trade = {
    id: randomUUID(),
    symbol: o.symbol,
    side: o.side,
    qty,
    price: px,
    notional,
    fee,
    realizedPnl: realized - fee,
    source: o.source ?? 'manual',
    note: o.note ?? '',
    createdAt: now(),
  }
  s.revision++
  s.trades.unshift(trade)
  return trade
}

function spend(s: State, cost: number) {
  if (s.credits.used + cost > s.credits.limit) throw new Fail(429, 'ai_quota', 'Daily AI credits used up.')
  s.credits = { ...s.credits, used: s.credits.used + cost }
  return s.credits
}

// ── Market data ─────────────────────────────────────────────────────────────

const STEP: Record<Interval, number> = { '15m': 900, '1h': 3600, '4h': 14_400, '1d': 86_400 }

function calendarOpen(t: number, interval: '1w' | '1M') {
  const d = new Date(t * 1000)
  d.setUTCHours(0, 0, 0, 0)
  d.setUTCDate(interval === '1M' ? 1 : d.getUTCDate() - (d.getUTCDay() + 6) % 7)
  return d.getTime() / 1000
}

function previousCalendarOpen(t: number, interval: '1w' | '1M') {
  const d = new Date(t * 1000)
  if (interval === '1M') d.setUTCMonth(d.getUTCMonth() - 1)
  else d.setUTCDate(d.getUTCDate() - 7)
  return d.getTime() / 1000
}

function calendarCandles(symbol: string, interval: '1w' | '1M', limit: number, before?: number): Candle[] {
  const latest = calendarOpen(Date.now() / 1000, interval)
  const last = before == null ? latest : Math.min(latest, calendarOpen(before - 1, interval))
  // A period containing the first retained daily bar remains available, including a partial opening week/month.
  const dailyFirst = Math.floor(Date.now() / 86_400_000) * 86_400 - ((symbol === 'SP500' ? 201 : 303) - 1) * 86_400
  const first = isStockPerp(symbol) ? calendarOpen(dailyFirst, interval) : 0
  const times: number[] = []
  for (let t = last; times.length < Math.floor(limit) && t >= first; t = previousCalendarOpen(t, interval)) times.unshift(t)
  return waveCandles(mark(symbol) * (isStockPerp(symbol) ? 0.997 : 1), times)
}

function waveCandles(p: number, times: number[]): Candle[] {
  const limit = times.length
  const close = (i: number) => p * (1 - 0.0003 * (limit - 1 - i) + 0.015 * Math.sin(i / 9) * ((limit - 1 - i) / limit))
  return times.map((t, i) => {
    const c = close(i)
    const o = i ? close(i - 1) : c * 0.999
    return { t, o, h: Math.max(o, c) * 1.002, l: Math.min(o, c) * 0.998, c, v: 100 + (i % 7) * 15 }
  })
}

/** A gentle wave that closes exactly at the mark, so the chart's last price agrees with the asset list. */
export function candles(symbol: string, interval: ChartInterval, limit: number, before?: number): Candle[] {
  if (interval === '1w' || interval === '1M') return calendarCandles(symbol, interval, limit, before)
  const p = mark(symbol) * (isStockPerp(symbol) ? 0.997 : 1)
  if (isStockPerp(symbol)) limit = Math.min(limit, interval === '1d' ? (symbol === 'SP500' ? 201 : 303) : 1000)
  const step = STEP[interval]
  const latest = Math.floor(Date.now() / 1000 / step) * step
  const last = before == null ? latest : Math.min(latest, Math.floor((before - 1) / step) * step)
  if (isStockPerp(symbol)) {
    const retained = interval === '1d' ? (symbol === 'SP500' ? 201 : 303) : 1000
    const first = latest - (retained - 1) * step
    limit = Math.min(limit, Math.max(0, Math.floor((last - first) / step) + 1))
  }
  if (last < 0) return []
  return waveCandles(p, Array.from({ length: limit }, (_, i) => last - (limit - 1 - i) * step))
}

const NEWS = (): NewsItem[] => [
  fx.news({ publishedAt: hoursAgo(1) }),
  fx.news({
    id: 'n2',
    title: 'Ether staking deposits hit a record',
    url: 'https://www.coindesk.com/markets/eth-staking',
    source: 'coindesk.com',
    description: 'Validators queue to stake.',
    publishedAt: hoursAgo(3),
    symbols: ['ETH'],
  }),
  fx.news({
    id: 'n3',
    title: 'Fed holds rates and signals one more cut',
    url: 'https://apnews.com/article/fed-rates',
    source: 'apnews.com',
    description: 'Policy makers keep the door open.',
    publishedAt: hoursAgo(5),
    symbols: [],
    topic: 'macro',
    regions: ['us'],
  }),
  // Regional wire: one story can touch several regions; none for Southeast Asia, so its tab shows the empty state.
  fx.news({
    id: 'n4',
    title: 'TSMC lifts its outlook on AI chip demand',
    url: 'https://asia.nikkei.com/business/tsmc-outlook',
    source: 'asia.nikkei.com',
    description: 'US cloud orders keep fabs full.',
    publishedAt: hoursAgo(6),
    symbols: ['TSM'],
    regions: ['us', 'east_asia'],
    hasContent: false,
  }),
  fx.news({
    id: 'n5',
    title: 'Bank of Japan keeps rates on hold as the yen slides',
    url: 'https://www.japantimes.co.jp/business/boj-hold',
    source: 'japantimes.co.jp',
    description: 'Governor signals patience.',
    publishedAt: hoursAgo(7),
    symbols: [],
    topic: 'macro',
    regions: ['east_asia'],
    hasContent: false,
  }),
]
const NEWS_REGIONS: readonly string[] = ['crypto', 'us', 'east_asia', 'southeast_asia']

// ── Agent chat ──────────────────────────────────────────────────────────────

const SOURCES = [
  { n: 1, title: 'Bitcoin ETFs log a third day of inflows', url: 'https://www.reuters.com/markets/btc-etf', source: 'reuters.com', publishedAt: hoursAgo(1) },
  { n: 2, title: 'Funding rates stay neutral', url: 'https://www.coindesk.com/markets/funding', source: 'coindesk.com', publishedAt: null },
]

function seedChat(): Chat {
  return {
    id: SEED.chat,
    title: 'Is BTC overbought right now?',
    messages: [
      { id: 'm1', role: 'user', attachments: [], content: 'Is BTC overbought right now?', cards: [], createdAt: hoursAgo(2) },
      {
        id: 'm2',
        role: 'assistant',
        attachments: [],
        content: 'Not yet: RSI(14) is **61** on the 4h, and spot ETFs keep buying [1].',
        cards: [
          { type: 'sources', data: { items: SOURCES.slice(0, 1) } },
          { type: 'price_outlook', data: fx.outlook },
        ],
        createdAt: hoursAgo(2),
      },
    ],
  }
}

const FOLLOWUPS = ['Size this for 1% account risk', 'What would invalidate this?', 'Compare with ETH']

/** The trade an agent card's proposalId filled, while it is still on the book. */
const filledBy = (s: State, proposalId: string) => s.trades.find((t) => t.id === s.fills.get(proposalId))

/** GET /api/chats/:id: a trade card with a proposalId reads filled (with its trade) or open. */
function withStatus(s: State, c: Chat['messages'][number]['cards'][number], since: string) {
  if (c.type === 'memory') {
    const receipt = s.memoryReceipts.get(c.data.proposalId)
    return receipt ? { ...c, data: { ...c.data, status: receipt.status, memoryId: receipt.memoryId } } : c
  }
  if (c.type === 'build_action') {
    const eligible = <T extends { prompt: string; createdAt: string }>(rows: T[]) => rows
      .filter((row) => row.prompt === c.data.prompt && row.createdAt >= since)
      .sort((a, b) => a.createdAt.localeCompare(b.createdAt))[0]
    const p = c.data.kind === 'portfolio' ? eligible(s.portfolios) : undefined
    const st = c.data.kind === 'strategy' ? eligible([...s.strategies.values()]) : undefined
    const result = p ? { id: p.id, name: p.name, returnPct: p.backtest.returnPct, benchmarkReturnPct: p.backtest.benchmarkReturnPct, benchmarkSymbol: p.backtest.benchmarkSymbol || 'BTC', benchmarkLabel: p.backtest.benchmarkLabel || 'BTC', windowStart: p.backtest.windowStart ?? null, windowEnd: p.backtest.windowEnd ?? null, bars: p.backtest.bars || null, sharpe: null, maxDrawdownPct: p.backtest.maxDrawdownPct, holdings: p.holdings.map(({ symbol, weight }) => ({ symbol, weight })) } : st ? { id: st.id, name: st.name, returnPct: st.metrics.totalReturnPct, benchmarkReturnPct: st.metrics.buyHoldReturnPct, benchmarkSymbol: null, benchmarkLabel: null, windowStart: null, windowEnd: null, bars: null, sharpe: st.metrics.sharpe, maxDrawdownPct: st.metrics.maxDrawdownPct, holdings: [] } : null
    return { ...c, data: { ...c.data, result } }
  }
  if (c.type !== 'trade_proposal' || !c.data.proposalId) return c
  const trade = filledBy(s, c.data.proposalId) ?? null
  return { ...c, data: { ...c.data, status: trade ? ('filled' as const) : ('open' as const), trade } }
}

/** An ordered report's view in brief (ForecastSummary.view), which the chat's cards carry (contract). */
function viewBriefOf(f?: Forecast) {
  const v = f?.shape === 'ordered' ? f.result?.view : undefined
  return v ? { view: { median: v.median, p10: v.p10, p90: v.p90, pAbove: v.pAbove, unit: v.unit, flat: v.flat, headline: v.headline, basis: v.basis.kind } } : {}
}

/** The forecast card a chat opened from a report starts with. */
function forecastCard(f?: Forecast): ChatStreamEvent[] {
  if (!f?.result) return []
  const data = { id: f.id, question: f.question, shape: f.shape, headline: f.result.probabilities[0], resolutionDate: f.resolutionDate, ...viewBriefOf(f) }
  return [{ event: 'card', data: { type: 'forecast', data } }]
}

export const ANSWER = 'BTC holds its uptrend as spot ETFs log a third day of inflows [1]. Funding is neutral [2], so a small long with a stop under support fits.'

/** The scripted turn: two tool calls, the answer in deltas, a sources card for the [n] marks, then a trade card. */
function agentTurn(chatId: string, title: string, s: State, forecast?: Forecast): ChatStreamEvent[] {
  const px = mark('BTC')
  const words = ANSWER.match(/\S+\s*/g)!
  const deltas: ChatStreamEvent[] = []
  for (let i = 0; i < words.length; i += 4) deltas.push({ event: 'delta', data: { text: words.slice(i, i + 4).join('') } })
  return [
    { event: 'chat', data: { chatId, title } },
    ...forecastCard(forecast),
    { event: 'tool_call', data: { id: 'tc1', name: 'search_news', label: 'Searching the news: “BTC ETF flows”', input: { query: 'BTC ETF flows' } } },
    { event: 'tool_result', data: { id: 'tc1', name: 'search_news', ok: true, summary: '2 headlines' } },
    { event: 'tool_call', data: { id: 'tc2', name: 'propose_trade', label: 'Drafting a BTC trade', input: { symbol: 'BTC' } } },
    { event: 'tool_result', data: { id: 'tc2', name: 'propose_trade', ok: true, summary: 'Buy $500 BTC' } },
    ...deltas,
    { event: 'card', data: { type: 'sources', data: { items: SOURCES } } },
    {
      event: 'card',
      data: {
        type: 'trade_proposal',
        data: {
          proposalId: randomUUID(),
          symbol: 'BTC',
          side: 'buy',
          notional: 500,
          price: px,
          stopLoss: round2(px * 0.97),
          takeProfit: round2(px * 1.06),
          rationale: 'Trend intact with ETF demand behind it.',
          forecast: forecast?.result ? { id: forecast.id, question: forecast.question, ...forecast.result.probabilities[0], ...viewBriefOf(forecast) } : null,
          status: 'open',
          trade: null,
        },
      },
    },
    { event: 'tool_call', data: { id: 'tc3', name: 'suggest_followups', label: 'Suggesting next steps', input: {} } },
    { event: 'tool_result', data: { id: 'tc3', name: 'suggest_followups', ok: true, summary: '3 prompts' } },
    { event: 'card', data: { type: 'followups', data: { prompts: FOLLOWUPS } } },
    { event: 'done', data: { messageId: randomUUID(), credits: s.credits } },
  ]
}

// ── Agent desk actions: scripted forecast_draft / build_action turns ── begin
/**
 * A message starting "Forecast …" gets a forecast-desk draft card (1 credit); one naming a "basket" an AI portfolio
 * card. (Not any "forecast": the report handoff's prompt mentions the forecast desk and wants the trade turn.)
 */
function actionTurn(chatId: string, title: string, s: State, message: string, allowForecast = true): ChatStreamEvent[] | undefined {
  const forecast = /^forecast\b/i.test(message)
  if ((forecast && !allowForecast) || (!forecast && !/\bbasket\b/i.test(message))) return undefined
  const due = new Date(Date.now() + 56 * 86_400_000).toISOString().slice(0, 10)
  const card: Extract<ChatStreamEvent, { event: 'card' }>['data'] = forecast
    ? {
        type: 'forecast_draft',
        data: {
          draft: {
            shape: 'binary',
            question: `Will the Binance SOL/USDT daily close on ${due} be above $150?`,
            outcomes: ['Yes', 'No'],
            resolutionCriteria: `Resolves Yes if the Binance SOLUSDT 1d candle that opens at 00:00 UTC on ${due} closes above $150; otherwise No.`,
            resolutionDate: due,
            category: 'crypto',
            priceRule: { symbol: 'SOL', op: 'above', threshold: 150, mode: 'on_date' },
          },
          forecast: null,
        },
      }
    : { type: 'build_action', data: { kind: 'portfolio', prompt: /\bstock\b|\bequity\b|\bAAPL\b|\bNVDA\b/i.test(message) ? message : 'a defensive crypto basket', symbol: null, interval: null, cost: 2, result: null } }
  const [tool, label, summary, text] = forecast
    ? ['draft_forecast', 'Drafting a forecast question', 'Drafted for the desk', 'Drafted for the forecast desk. Tap **Run** to send it to the specialists.']
    : ['build_portfolio', 'Preparing an AI portfolio card', 'Portfolio card ready', 'Here is the brief. Tap **Build** and the AI picks and backtests the basket.']
  const credits = forecast ? spend(s, 1) : s.credits
  return [
    { event: 'chat', data: { chatId, title } },
    { event: 'tool_call', data: { id: 'ta1', name: tool, label, input: {} } },
    { event: 'tool_result', data: { id: 'ta1', name: tool, ok: true, summary } },
    { event: 'delta', data: { text } },
    { event: 'card', data: card },
    { event: 'done', data: { messageId: randomUUID(), credits } },
  ]
}
// ── Agent desk actions ── end

// ── Agent sources: an answer citing only [2] of four sources ── begin
export const SOURCED_ANSWER = 'SOL leads the majors as DEX volumes hit a record [2]. **View:** bullish while $180 holds.'
const MORE_SOURCES = [
  ...SOURCES,
  { n: 3, title: 'Solana DEX volumes hit a record', url: 'https://www.theblock.co/sol-dex', source: 'theblock.co', publishedAt: hoursAgo(4) },
  { n: 4, title: 'Fed holds rates and signals one more cut', url: 'https://apnews.com/article/fed-rates', source: 'apnews.com', publishedAt: hoursAgo(5) },
]

/** A message starting "Research …" gets an answer that cites one source of four (the card lists it first, folds the rest). */
function sourcedTurn(chatId: string, title: string, s: State, message: string): ChatStreamEvent[] | undefined {
  if (!/^research\b/i.test(message)) return undefined
  return [
    { event: 'chat', data: { chatId, title } },
    { event: 'tool_call', data: { id: 'ts1', name: 'search_news', label: 'Searching news: Solana', input: { query: 'Solana' } } },
    { event: 'tool_result', data: { id: 'ts1', name: 'search_news', ok: true, summary: '4 articles' } },
    { event: 'delta', data: { text: SOURCED_ANSWER } },
    { event: 'card', data: { type: 'sources', data: { items: MORE_SOURCES } } },
    { event: 'done', data: { messageId: randomUUID(), credits: s.credits } },
  ]
}
// ── Agent sources ── end

// ── Long-horizon outlook: a 10x or multi-year question gets the 10-year price_outlook card ── begin
export const LONG_OUTLOOK_ANSWER = "Yes, it's possible, but only if BTC keeps compounding for years, and realistically over the full decade, not months. " +
  'The volatility model puts a 24% chance on touching 10× within 10 years and 12% on ending there; the drift case is an assumption.'

/** A message asking about a 10x or N years ("Can BTC 10x in 10 years?") gets fx.longOutlook, the model's 10-year card. */
function outlookTurn(chatId: string, title: string, s: State, message: string): ChatStreamEvent[] | undefined {
  if (!/\b10x\b|10×|\b\d+[- ]years?\b/i.test(message)) return undefined
  const o = fx.longOutlook
  return [
    { event: 'chat', data: { chatId, title } },
    { event: 'tool_call', data: { id: 'to1', name: 'price_outlook', label: 'Modeling BTC 10-year price range', input: { symbol: 'BTC', horizon_days: o.horizonDays } } },
    { event: 'tool_result', data: { id: 'to1', name: 'price_outlook', ok: true, summary: `BTC in ${o.horizonDays}d: 90% range $${o.p5.toFixed(2)} – $${o.p95.toFixed(2)} · 10x touch 24%` } },
    { event: 'delta', data: { text: LONG_OUTLOOK_ANSWER } },
    { event: 'card', data: { type: 'price_outlook', data: o } },
    { event: 'done', data: { messageId: randomUUID(), credits: s.credits } },
  ]
}
// ── Long-horizon outlook ── end

// ── Price outlook view: "can TSMC 10x, by when" gets the scenario view card with time to target ── begin
export const TSM_VIEW_ANSWER = "Yes, it's possible, but only if the AI-supercycle case plays out: about 20% EPS growth a year with the multiple near 25× [1]; " +
  'on that path 10× takes about 11 years. Under our weighted view the chance is about 12% within 10 years and about a third within 20 (extrapolated).'

/** "Can TSMC 10x, if yes by when": quote, one search, then price_outlook with scenarios and target_multiple 10 (fx.tsmOutlookView). */
function tsmViewTurn(chatId: string, title: string, s: State, message: string): ChatStreamEvent[] | undefined {
  if (!/\bTSMC?\b/i.test(message) || !/\b10x\b|10×|by when/i.test(message)) return undefined
  const o = fx.tsmOutlookView
  return [
    { event: 'chat', data: { chatId, title } },
    { event: 'tool_call', data: { id: 'tv1', name: 'get_stock_quote', label: 'Looking up TSM quote', input: { symbol: 'TSM' } } },
    { event: 'tool_result', data: { id: 'tv1', name: 'get_stock_quote', ok: true, summary: `TSM $${o.spot.toFixed(2)}` } },
    { event: 'tool_call', data: { id: 'tv2', name: 'web_search', label: 'Searching the web: “TSMC consensus EPS”', input: { query: 'TSMC consensus EPS' } } },
    { event: 'tool_result', data: { id: 'tv2', name: 'web_search', ok: true, summary: '1 result' } },
    { event: 'tool_call', data: { id: 'tv3', name: 'price_outlook', label: 'Modeling TSM by when 10×', input: { symbol: 'TSM', horizon_days: o.horizonDays, target_multiple: 10 } } },
    { event: 'tool_result', data: { id: 'tv3', name: 'price_outlook', ok: true, summary: 'TSM in 5y: median $672.25 (view), 65% above today · 10× median beyond 30 years' } },
    { event: 'delta', data: { text: TSM_VIEW_ANSWER } },
    { event: 'card', data: { type: 'price_outlook', data: o } },
    { event: 'card', data: { type: 'sources', data: { items: [fx.tsmSource] } } },
    { event: 'done', data: { messageId: randomUUID(), credits: s.credits } },
  ]
}
/** "BTC next 3 months": price_outlook at the question's 92 days, no scenarios (fx.btcShortView, the halved drift). */
function btcShortViewTurn(chatId: string, title: string, s: State, message: string): ChatStreamEvent[] | undefined {
  if (!/\bBTC\b/i.test(message) || !/\bnext 3 months\b/i.test(message)) return undefined
  const o = fx.btcShortView
  return [
    { event: 'chat', data: { chatId, title } },
    { event: 'tool_call', data: { id: 'tb1', name: 'price_outlook', label: 'Modeling BTC 92-day price range', input: { symbol: 'BTC', horizon_days: 92 } } },
    { event: 'tool_result', data: { id: 'tb1', name: 'price_outlook', ok: true, summary: 'BTC in 92d: median $101,777 (view), 58% above today' } },
    { event: 'delta', data: { text: BTC_SHORT_ANSWER } },
    { event: 'card', data: { type: 'price_outlook', data: o } },
    { event: 'done', data: { messageId: randomUUID(), credits: s.credits } },
  ]
}
export const BTC_SHORT_ANSWER = 'Over the next 3 months the halved historical drift puts the median near $101,800, a 58% chance above today; that drift is an assumption.'

// The owner's "NVDA 10 years outlook" is 10 years, and a drift-only headline is capped.
export const NVDA_DRIFT_ANSWER = 'Over 10 years, without scenarios of our own, the card leans on NVDA’s historical drift, capped at the S&P 500’s long-run return + 2 points: ' +
  'a median near $592 and a 70% chance above today. That is a backward-looking assumption, not a forecast; a real view needs scenarios with cited inputs.'

/** "NVDA 10 years outlook": the model passes horizon_days 1825, the API reads 10 years from the question and caps the drift (fx.nvdaOutlookView). */
function nvdaDriftTurn(chatId: string, title: string, s: State, message: string): ChatStreamEvent[] | undefined {
  if (!/\bNVDA\b/i.test(message) || !/\b10 years? outlook\b/i.test(message)) return undefined
  const o = fx.nvdaOutlookView
  return [
    { event: 'chat', data: { chatId, title } },
    { event: 'tool_call', data: { id: 'tn1', name: 'price_outlook', label: 'Modeling NVDA 10-year price range', input: { symbol: 'NVDA', horizon_days: 1825 } } },
    { event: 'tool_result', data: { id: 'tn1', name: 'price_outlook', ok: true, summary: 'NVDA in 10y: median $591.60 (view), 70% above today' } },
    { event: 'delta', data: { text: NVDA_DRIFT_ANSWER } },
    { event: 'card', data: { type: 'price_outlook', data: o } },
    { event: 'done', data: { messageId: randomUUID(), credits: s.credits } },
  ]
}

// "Where could SOL go?" names no timeframe and the model gives horizon_days 1095 without a reason: the API
// says "Horizon chosen by the agent" (fx.solAgentView).
export const SOL_AGENT_ANSWER = 'You named no timeframe, so this looks 3 years out: the halved drift puts the median near $247, an assumption, not a forecast.'
function solAgentTurn(chatId: string, title: string, s: State, message: string): ChatStreamEvent[] | undefined {
  if (!/^where could SOL go\??$/i.test(message.trim())) return undefined
  const o = fx.solAgentView
  return [
    { event: 'chat', data: { chatId, title } },
    { event: 'tool_call', data: { id: 'ts1', name: 'price_outlook', label: 'Modeling SOL 3-year price range', input: { symbol: 'SOL', horizon_days: 1095 } } },
    { event: 'tool_result', data: { id: 'ts1', name: 'price_outlook', ok: true, summary: 'SOL in 3y: median $246.66 (view), drift +7.0%/yr' } },
    { event: 'delta', data: { text: SOL_AGENT_ANSWER } },
    { event: 'card', data: { type: 'price_outlook', data: o } },
    { event: 'done', data: { messageId: randomUUID(), credits: s.credits } },
  ]
}
// ── Price outlook view ── end

// ── Agent scenario memo: get_stock_quote, then a memo with labelled subjective odds ── begin
export const MEMO_ANSWER = '**Bottom line:** possible but unlikely; about 5% odds of a 10x within 3–5 years (my subjective scenario weights, not measured odds).\n\n' +
  '| Scenario | Probability | Implied price | Return |\n|---|---|---|---|\n| Bear | 30% | $5 | −50% |\n| Base | 45% | $12 | +20% |\n| Bull | 20% | $30 | +200% |\n| Moonshot | 5% | $100 | +900% |\n\n' +
  'P(gain) 70%, P(loss) 30%; probability-weighted multiple 2.0×. **View:** neutral; a re-rating above $30 would change it.'

/** A message asking whether ACME (a fixture listing, outside the paper universe) can 10x looks up its quote, then answers with a scenario memo and no trade card. */
function memoTurn(chatId: string, title: string, s: State, message: string): ChatStreamEvent[] | undefined {
  if (!/\bACME\b/.test(message) || !/\b10x\b/i.test(message)) return undefined
  return [
    { event: 'chat', data: { chatId, title } },
    { event: 'tool_call', data: { id: 'tq1', name: 'get_stock_quote', label: 'Looking up ACME quote', input: { symbol: 'ACME' } } },
    { event: 'tool_result', data: { id: 'tq1', name: 'get_stock_quote', ok: true, summary: 'ACME $10.00 · -50.0% from ATH' } },
    { event: 'delta', data: { text: MEMO_ANSWER } },
    { event: 'done', data: { messageId: randomUUID(), credits: s.credits } },
  ]
}
// ── Agent scenario memo ── end

// ── Agent answer still running: a chat whose client left mid-stream ── begin
export const PENDING_CHAT = 'c-pending' // not in the history list: open /chat/c-pending
export const PENDING_ANSWER = 'AVAX stays on the bullish board: score 44 and 3.1% from its target. **View:** bullish while $24 holds.'
const PENDING_POLLS = 2 // GET /api/chats/c-pending reads pending: true this many times per session, then has the answer
const pendingReads = new WeakMap<State, number>()

function pendingChat(s: State): Res<'chat'> {
  const reads = (pendingReads.get(s) ?? 0) + 1
  pendingReads.set(s, reads)
  const question = { id: 'pm1', role: 'user' as const, attachments: [], content: 'Why is AVAX on the bullish board?', cards: [], createdAt: hoursAgo(0.01) }
  const answer = { id: 'pm2', role: 'assistant' as const, attachments: [], content: PENDING_ANSWER, cards: [], createdAt: now() }
  const pending = reads <= PENDING_POLLS
  return { chat: { id: PENDING_CHAT, title: question.content, messages: pending ? [question] : [question, answer], pending } }
}
// ── Agent answer still running ── end

const FRAME_DELAY_MS = 60 // slow enough to watch the answer stream in, fast enough for CI
const memoryDelays = new WeakMap<State, number>()
function memoryTurn(chatId: string, title: string, s: State, message: string): ChatStreamEvent[] | undefined {
  if (!/^remember\s/i.test(message)) return undefined
  const text = message.replace(/^remember\s+/i, '')
  if (!text.trim() || [...text].length > 200) return undefined
  const data: MemoryProposal = { proposalId: randomUUID(), text, status: 'open' }
  return [
    { event: 'chat', data: { chatId, title } },
    { event: 'tool_call', data: { id: 'remember-1', name: 'remember', label: 'Suggesting a memory', input: { text } } },
    { event: 'tool_result', data: { id: 'remember-1', name: 'remember', ok: true, summary: 'Memory suggestion ready; confirm to save.' } },
    { event: 'delta', data: { text: 'Here is a memory suggestion. Confirm it to save it.' } },
    { event: 'card', data: { type: 'memory', data } },
    { event: 'done', data: { messageId: randomUUID(), credits: s.credits } },
  ]
}

/** Scripted representative tool turn. API tests own model policy; this proves ordinary SSE/client rendering. */
function reviewTurn(chatId: string, title: string, s: State, message: string): ChatStreamEvent[] | null {
  if (!/review my paper portfolio/i.test(message)) return null
  const r = reviewOf(s)
  const summary = `Revision ${r.revision}; ${r.coverage.commonDays} common days; gross $${r.grossExposure.toFixed(2)}; daily history unavailable.`
  const events: ChatStreamEvent[] = [
    { event: 'chat', data: { chatId, title } },
    { event: 'tool_call', data: { id: 'review', name: 'review_portfolio', label: 'Reviewing your portfolio', input: {} } },
    { event: 'tool_result', data: { id: 'review', name: 'review_portfolio', ok: true, summary } },
    { event: 'delta', data: { text: `Measured facts: ${summary} Prediction stakes $${r.predictionStakes.toFixed(2)} are excluded from daily asset risk. Interpretation: unknown correlations do not establish independence. No action is required from these unknowns.` } },
  ]

  if (/warranted trim fixture/i.test(message) && s.positions.has('ETH')) {
    events.push({ event: 'card', data: { type: 'trade_proposal', data: {
      symbol: 'ETH', side: 'sell', notional: 500, price: mark('ETH'), stopLoss: null, takeProfit: null,
      rationale: 'Illustrative optional trim fixture. Confirm to place the paper order.',
      proposalId: randomUUID(), status: 'open', trade: null,
    } } })
  }
  events.push({ event: 'done', data: { messageId: randomUUID(), credits: s.credits } })
  return events
}

const BASE64 = /^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/
const DOCUMENT_TYPES: Record<string, 'pdf' | 'docx' | 'xlsx' | 'csv' | 'txt' | 'md' | 'html'> = {
  'application/pdf': 'pdf', 'text/csv': 'csv', 'text/plain': 'txt', 'text/markdown': 'md', 'text/html': 'html',
  'application/vnd.openxmlformats-officedocument.wordprocessingml.document': 'docx',
  'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet': 'xlsx',
}
// A known extension must agree with the mediaType (text formats agree with each other), like the API.
const EXTENSIONS: Record<string, string> = { pdf: 'pdf', docx: 'docx', xlsx: 'xlsx', csv: 'csv', txt: 'txt', md: 'md', markdown: 'md', html: 'html', htm: 'html' }
const TEXT_FORMATS = new Set(['csv', 'txt', 'md', 'html'])
const OFFICE_MAIN = { docx: 'wordprocessingml.document.main+xml', xlsx: 'spreadsheetml.sheet.main+xml' }

/** The API's attachment rules: at most 5 images and documents, decoded bounds, magic bytes matching the type. */
function chatAttachments(inputs: unknown): ChatAttachmentMetadata[] {
  if (!Array.isArray(inputs) || inputs.length > 5) throw new Fail(400, 'bad_request', 'Attach at most five files.')
  return inputs.map((input): ChatAttachmentMetadata => {
    const limit = input?.kind === 'document' ? 4_500_000 : 1_500_000
    if (!input || typeof input.name !== 'string' || typeof input.data !== 'string' || !input.data || input.data.length > Math.ceil(limit / 3) * 4 || !BASE64.test(input.data)) throw new Fail(400, 'bad_request', 'Invalid attachment input.')
    const bytes = Buffer.from(input.data, 'base64')
    if (!bytes.length || bytes.length > limit || bytes.toString('base64') !== input.data) throw new Fail(400, 'bad_request', 'Attachment is too large or invalid.')
    const name = (input.name.replace(/\\/g, '/').split('/').at(-1) ?? '').replace(/\p{Cc}/gu, '').trim()
    if (Array.from(name).length > 120) throw new Fail(400, 'bad_request', 'File name exceeds 120 characters.')
    const safe = !name || name === '.' || name === '..' ? (input.kind === 'document' ? 'document' : 'image') : name
    if (input.kind === 'image') {
      const magic = { 'image/png': '89504e47', 'image/jpeg': 'ffd8ff', 'image/webp': '52494646', 'image/gif': '47494638' }[input.mediaType as string]
      if (!magic || !bytes.toString('hex').startsWith(magic)) throw new Fail(400, 'bad_request', 'Images must be PNG, JPEG, WebP or GIF matching their type.')
      return { kind: 'image', mediaType: input.mediaType, name: safe, size: bytes.length }
    }
    const format = input.kind === 'document' ? DOCUMENT_TYPES[input.mediaType] : undefined
    if (!format) throw new Fail(400, 'bad_request', 'Unsupported attachment type.')
    const ext = EXTENSIONS[safe.includes('.') ? safe.split('.').at(-1)!.toLowerCase() : '']
    if (ext && ext !== format && !(TEXT_FORMATS.has(ext) && TEXT_FORMATS.has(format))) throw new Fail(400, 'bad_request', `${safe}: file extension does not match ${input.mediaType}`)
    // Synthetic fixtures store their ZIP entries uncompressed, so the main part's content type is visible as bytes.
    const ok = format === 'pdf' ? bytes.subarray(0, 5).toString('latin1') === '%PDF-'
      : format === 'docx' || format === 'xlsx' ? bytes.subarray(0, 4).toString('latin1') === 'PK\x03\x04' && bytes.includes('[Content_Types].xml') && bytes.includes(OFFICE_MAIN[format])
      : !bytes.includes(0) && Buffer.from(bytes.toString('utf8'), 'utf8').equals(bytes)
    if (!ok) throw new Fail(400, 'bad_request', `${safe}: file content does not match its type.`)
    const pages = format === 'pdf' ? Number(bytes.toString('latin1').match(/\/Type\s*\/Pages\b[^>]*?\/Count\s+(\d{1,6})/)?.[1] ?? 0) : undefined
    return { kind: 'document', mediaType: input.mediaType, name: safe, size: bytes.length, ...(pages ? { pages } : {}) }
  })
}

async function streamChat(s: State, req: IncomingMessage, res: ServerResponse, body: ChatStreamRequest) {
  const message = String(body.message ?? '').trim()
  if (!message || Array.from(message).length > 4000) throw new Fail(400, 'bad_request', 'message must be 1..4000 characters')
  if (body.lens !== undefined && !(['auto', 'investor', 'trader'] as unknown[]).includes(body.lens)) throw new Fail(400, 'bad_request', 'lens must be auto, investor or trader.')
  const attachments = chatAttachments(body.attachments ?? [])
  if (body.chatId && !s.chats.has(body.chatId)) throw new Fail(404, 'not_found', 'chat not found')
  const forecast = body.context?.forecastId ? (s.forecasts.get(body.context.forecastId) ?? FIXED_FORECASTS.get(body.context.forecastId)?.()) : undefined
  if (body.context?.forecastId && forecast?.status !== 'completed') throw new Fail(404, 'not_found', 'Unknown forecast.')
  spend(s, attachments.length ? 3 + Math.max(0, attachments.filter(a => a.kind === 'document').length - 1) : 2)
  const chat = s.chats.get(body.chatId ?? '') ?? { id: randomUUID(), title: message.slice(0, 60), messages: [], updatedAt: now() }
  chat.messages.push({ id: randomUUID(), role: 'user', content: message + attachments.map(a => `\n\n[${a.kind}: ${a.name}]`).join(''), cards: [], attachments, createdAt: now() })
  chat.updatedAt = now()
  s.chats.set(chat.id, chat)

  res.writeHead(200, { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache' })
  let open = true
  req.on('close', () => (open = false))
  const answer = { id: randomUUID() as string, role: 'assistant' as const, content: '', cards: [] as Chat['messages'][number]['cards'], attachments: [], createdAt: now() }
  for (const ev of memoryTurn(chat.id, chat.title, s, message) ?? researchProposalTurn(chat.id, chat.title, s, message) ?? accountTurn(chat.id, chat.title, s, message) ?? reviewTurn(chat.id, chat.title, s, message) ?? actionTurn(chat.id, chat.title, s, message, !attachments.length) ?? drawdownTurn(chat.id, chat.title, s, message) ??
    sourcedTurn(chat.id, chat.title, s, message) ?? memoTurn(chat.id, chat.title, s, message) ?? tsmViewTurn(chat.id, chat.title, s, message) ?? btcShortViewTurn(chat.id, chat.title, s, message) ?? nvdaDriftTurn(chat.id, chat.title, s, message) ?? solAgentTurn(chat.id, chat.title, s, message) ?? outlookTurn(chat.id, chat.title, s, message) ??
    agentTurn(chat.id, chat.title, s, body.chatId ? undefined : forecast)) {
    if (ev.event === 'delta') answer.content += ev.data.text
    if (ev.event === 'card') answer.cards.push(ev.data)
    if (ev.event === 'done') {
      if (answer.cards.some((c) => c.type === 'memory')) await new Promise((done) => setTimeout(done, memoryDelays.get(s) ?? 0))
      // Like the API, history is complete before the terminal event lets the client cancel its reader.
      answer.id = ev.data.messageId
      chat.messages.push(answer)
    }
    if (open) res.write(`event: ${ev.event}\ndata: ${JSON.stringify(ev.data)}\n\n`)
    await new Promise((r) => setTimeout(r, FRAME_DELAY_MS))
  }
  res.end()
}

// ── Strategies ──────────────────────────────────────────────────────────────

export const LEGACY_STRATEGY = 'lab-eth-rsi'

const LABS_LAUNCH = '2026-10-03T00:00:00Z'
const unavailableForward = (since: string, basis: StrategyForward['basis']): StrategyForward => ({
  since, basis, metrics: null, early: true, asOf: null, stale: false, reason: 'too_new', equity: [], benchmark: [], trades: [],
})

/** Fixed fixture returns, on a saved window ending before the last eligible closed bar. */
function savedBacktest(x: Strategy, savedAt: string): Strategy {
  const step = { '15m': 900, '1h': 3600, '4h': 14400, '1d': 86400 }[x.spec.interval]
  const endT = Math.floor(Date.parse(savedAt) / 1000 / step) * step - step
  const span = x.metrics.endT - x.metrics.startT
  const startT = endT - span
  const curve = (points: Point[]) => points.map((p, i) => ({ ...p, t: Math.floor((startT + span * i / Math.max(1, points.length - 1)) / step) * step }))
  return { ...x, metrics: { ...x.metrics, startT, endT }, equity: curve(x.equity), benchmark: curve(x.benchmark),
    trades: x.trades.map((t) => ({ ...t, entryT: startT + (t.entryT - x.metrics.startT), exitT: startT + (t.exitT - x.metrics.startT) })) }
}

const labForward = (interval: Interval): StrategyForward => {
  const first = Date.parse(LABS_LAUNCH) / 1000
  const step = interval === '1h' ? 3600 : 14400
  const end = Date.parse('2026-10-05T00:00:00Z') / 1000 - step
  return fx.forward({ since: LABS_LAUNCH, basis: 'launch', early: true,
    metrics: { ...fx.forward().metrics!, totalReturnPct: 1.2, buyHoldReturnPct: 0.8, trades: 2, startT: first - step, endT: end, bars: (end - first) / step + 2 },
    equity: [{ t: first - step, v: 100 }, { t: first, v: 100.1 }, { t: end, v: 101.2 }],
    benchmark: [{ t: first - step, v: 100 }, { t: first, v: 100.2 }, { t: end, v: 100.8 }],
  })
}

/** Built-ins as the Labs leaderboard serves them; the ETH one predates sortino / volatility / calmar / fees. */
function labStrategies(): Strategy[] {
  const legacy: Metrics = { ...fx.strategy().metrics, totalReturnPct: 9.8, sharpe: 0.71, calmar: null }
  delete legacy.sortino
  delete legacy.volatilityAnnPct
  delete legacy.feesPct
  // shortSample as the server computes it: a window under 180 days or fewer than 10 trades (ranked after full samples).
  const lab = (id: string, name: string, symbol: string, interval: Interval, metrics: Metrics) =>
    savedBacktest(fx.strategy({ id, name, author: 'Foretrade Labs', prompt: '', createdAt: LABS_LAUNCH, forward: interval === '1d' ? unavailableForward(LABS_LAUNCH, 'launch') : labForward(interval), spec: { ...fx.strategy().spec, name, symbol, interval }, metrics,
      shortSample: metrics.endT - metrics.startT < 180 * DAY || metrics.trades < 10 }), LABS_LAUNCH)
  // Too short to annualize (42 days) and nothing closed: annualized, Calmar and win rate are null.
  const m = fx.strategy().metrics
  const short: Metrics = { ...m, annualizedReturnPct: null, calmar: null, winRatePct: null, trades: 0, endT: m.startT + 42 * DAY }
  return [
    lab('lab-sol-ema', 'SOL EMA cross', 'SOL', '4h', { ...m, endT: m.startT + 400 * DAY }), // the one full sample: ranked first
    lab('lab-btc-donchian', 'BTC Donchian breakout', 'BTC', '1d', short),
    lab(LEGACY_STRATEGY, 'ETH RSI mean reversion', 'ETH', '1h', legacy),
  ]
}

const summary = (x: Strategy): StrategySummary => ({
  id: x.id,
  name: x.name,
  author: x.author,
  symbol: x.spec.symbol,
  interval: x.spec.interval,
  metrics: x.metrics,
  sparkline: x.equity.map((p) => p.v),
  shortSample: x.shortSample,
  createdAt: x.createdAt,
  forward: x.forward ? {
    since: x.forward.since, basis: x.forward.basis, metrics: x.forward.metrics, early: x.forward.early,
    asOf: x.forward.asOf, stale: x.forward.stale, reason: x.forward.reason,
  } : x.forward,
})

// ── Arena ───────────────────────────────────────────────────────────────────

const DAY = 86_400
const PERSONAS = Object.fromEntries(fx.arenaOptions.personas.map((p) => [p.id, p.name]))

function arenaRun(id: string, req: CreateArenaRunRequest, stepsDone = 0): ArenaRun {
  const days = (Date.parse(req.end) - Date.parse(req.start)) / 1000 / DAY + 1
  const steps = req.cadence === '1d' ? days : days * 6
  const md = (d: string) => new Date(`${d}T00:00:00Z`).toLocaleDateString('en-US', { month: 'short', day: 'numeric', timeZone: 'UTC' })
  const run: ArenaRun = {
    ...fx.arenaRun(),
    id,
    name: req.name || `${req.cadence === '1d' ? 'Daily' : '4-hour'} · ${md(req.start)} → ${md(req.end)}, ${req.end.slice(0, 4)}`,
    cadence: req.cadence,
    start: req.start,
    end: req.end,
    steps,
    stepsDone: 0,
    contaminated: req.start <= fx.arenaOptions.knowledgeCutoff,
    credits: Math.ceil((req.agents.length * steps) / 2),
    agents: req.agents.map((a: ArenaAgentRequest, index) =>
      fx.arenaAgent({ index, personaId: a.personaId, name: a.name || PERSONAS[a.personaId] || 'Custom agent', instruction: a.instruction ?? '', rank: index + 1 }),
    ),
    createdAt: now(),
  }
  return advance(run, stepsDone)
}

/** Moves a run to `stepsDone`, redrawing the curves; the last step completes it. */
function advance(run: ArenaRun, stepsDone: number): ArenaRun {
  const step = run.cadence === '1d' ? DAY : DAY / 6
  const t0 = Date.parse(`${run.start}T00:00:00Z`) / 1000
  const curve = (k: number): Point[] => Array.from({ length: stepsDone + 1 }, (_, i) => ({ t: t0 + i * step, v: run.startingCash * (1 + (k * i) / 1000) }))
  const total = (k: number) => (k * stepsDone) / 10
  run.stepsDone = stepsDone
  run.agents = run.agents.map((a, i): ArenaAgent => ({ ...a, equity: curve(2 - i), metrics: fx.arenaMetrics({ totalReturnPct: total(2 - i), vsBtcPct: total(1.5 - i) }) }))
  run.benchmarks = run.benchmarks.map((b, i) => ({ ...b, equity: curve(0.5 - i * 0.3), metrics: fx.arenaMetrics({ totalReturnPct: total(0.5 - i * 0.3), vsBtcPct: 0 }) }))
  if (stepsDone >= run.steps) Object.assign(run, { status: 'completed', completedAt: now() })
  return run
}

const runSummary = (r: ArenaRun): ArenaRunSummary => ({
  id: r.id,
  name: r.name,
  status: r.status,
  cadence: r.cadence,
  start: r.start,
  end: r.end,
  steps: r.steps,
  stepsDone: r.stepsDone,
  contaminated: r.contaminated,
  leader: r.status === 'completed' ? { name: r.agents[0].name, totalReturnPct: r.agents[0].metrics.totalReturnPct } : null,
  btcReturnPct: r.benchmarks[0].metrics.totalReturnPct,
  createdAt: r.createdAt,
})

// ── Routes ──────────────────────────────────────────────────────────────────

type Ctx = { s: State; body: any; q: URLSearchParams; id: string; req: IncomingMessage; res: ServerResponse }
type Route = { method: string; re: RegExp; status: number; handler: (c: Ctx) => unknown }
const routes: Route[] = []
/** Registers a handler; its return value is the JSON body (`undefined` for 204 or a stream it wrote itself). */
const on = (method: string, path: string, handler: Route['handler'], status = 200) =>
  routes.push({ method, re: new RegExp(`^${path.replace(':id', '([^/]+)')}$`), status, handler })

const sessions = new Map<string, State>()

const find = <T>(m: Map<string, T>, id: string, what: string) => {
  const v = m.get(id)
  if (!v) throw new Fail(404, 'not_found', `${what} not found`)
  return v
}

const userOf = (s: State): User => ({ ...fx.user, id: s.ownerId, profile: s.profile })
on('GET', '/api/me', ({ s }): Res<'me'> => ({ user: userOf(s), credits: s.credits }))
on('GET', '/api/me/profile', ({ s }): Res<'profile'> => ({ profile: s.profile }))
const badProfile = (message: string): never => { throw new Fail(400, 'bad_request', message) }
const conflictProfile = (): never => { throw new Fail(409, 'conflict', 'Profile changed, memory limit reached, or proposal already confirmed. Refetch your profile.') }
const missingMemory = (): never => { throw new Fail(404, 'not_found', 'Profile memory proposal not found.') }
const uuid = (value: unknown): value is string => typeof value === 'string' && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(value)
function exactObject(body: unknown, fields: string[]) {
  if (!body || typeof body !== 'object' || Array.isArray(body) || Object.keys(body).length !== fields.length || !fields.every((key) => Object.hasOwn(body, key))) badProfile('Body must contain exactly the documented fields.')
}
on('PUT', '/api/me/profile', ({ s, body }): Res<'updateProfile'> => {
  exactObject(body, ['enabled', 'riskProfile', 'style', 'focusSymbols', 'customPrompt', 'revision'])
  if (typeof body.enabled !== 'boolean' || !Number.isInteger(body.revision) || body.revision < 0 || typeof body.customPrompt !== 'string' || [...body.customPrompt].length > 600) badProfile('Invalid preference field or character limit.')
  if (body.riskProfile !== null && !['conservative', 'balanced', 'aggressive'].includes(body.riskProfile)) badProfile('Invalid riskProfile.')
  if (body.style !== null && !['scalp', 'day', 'swing'].includes(body.style)) badProfile('Invalid style.')
  if (!Array.isArray(body.focusSymbols) || body.focusSymbols.length > 10 || body.focusSymbols.some((x: unknown) => typeof x !== 'string' || !Object.hasOwn(prices, x.trim().toUpperCase()))) badProfile('focusSymbols must contain at most 10 configured tickers.')
  if (body.revision !== s.profile.revision) conflictProfile()
  s.profile = { ...s.profile, enabled: body.enabled, riskProfile: body.riskProfile, style: body.style, focusSymbols: [...new Set<string>(body.focusSymbols.map((x: string) => x.trim().toUpperCase()))], customPrompt: body.customPrompt, revision: s.profile.revision + 1, updatedAt: now() }
  return { profile: s.profile }
})
on('POST', '/api/me/profile/memories', ({ s, body }): Res<'confirmMemory'> => {
  exactObject(body, ['proposalId', 'text'])
  if (!uuid(body.proposalId) || typeof body.text !== 'string' || !body.text.trim() || [...body.text].length > 200) badProfile('Memory text must contain 1 to 200 characters and a valid proposalId.')
  if (s.memoryReceipts.has(body.proposalId)) conflictProfile()
  const card = [...s.chats.values()].flatMap((chat) => chat.messages.filter((m) => m.role === 'assistant').flatMap((m) => m.cards)).find((c) => c.type === 'memory' && c.data.proposalId === body.proposalId)
  if (!card || card.type !== 'memory' || card.data.status !== 'open') return missingMemory()
  if (card.data.text !== body.text) badProfile('Memory text must exactly match its saved proposal.')
  if (s.profile.memories.length >= 20) conflictProfile()
  const memory = { id: randomUUID(), text: body.text, at: now() }
  s.memoryReceipts.set(body.proposalId, { memoryId: memory.id, status: 'confirmed', at: memory.at })
  s.profile = { ...s.profile, memories: [...s.profile.memories, memory], revision: s.profile.revision + 1, updatedAt: now() }
  return { memory, profile: s.profile }
}, 201)
on('DELETE', '/api/me/profile/memories/:id', ({ s, id }): Res<'deleteMemory'> => {
  if (!s.profile.memories.some((m) => m.id === id)) missingMemory()
  for (const receipt of s.memoryReceipts.values()) if (receipt.memoryId === id) receipt.status = 'deleted'
  s.profile = { ...s.profile, memories: s.profile.memories.filter((m) => m.id !== id), revision: s.profile.revision + 1, updatedAt: now() }
  return { profile: s.profile }
})
// Explicit authenticated mock-only control. Application flows never call it.
on('POST', '/__e2e/profile-fixture', ({ s, body }) => {
  if (body.scenario === 'slow') {
    memoryDelays.set(s, 1500)
    return { ready: true }
  }
  if (body.scenario === 'capacity') {
    s.profile = { ...s.profile, memories: Array.from({ length: 20 }, (_, i) => ({ id: randomUUID(), text: `Saved fixture ${i + 1}`, at: now() })), revision: s.profile.revision + 1, updatedAt: now() }
    return { profile: s.profile }
  }
  if (body.scenario === 'user-card') {
    const data: MemoryProposal = { proposalId: randomUUID(), text: 'User supplied.', status: 'open' }
    const chat = s.chats.get(SEED.chat)!
    chat.messages.push({ id: randomUUID(), role: 'user', content: '', cards: [{ type: 'memory', data }], attachments: [], createdAt: now() })
    return { proposal: data }
  }
  return badProfile('Unknown Profile fixture.')
})

// No scope (or `named`): the named rows, plus with `scope=named` the listed rows `with` names (at most 20, in its order,
// anything else ignored); `listed`: the listed rows only. The API's errors, as it words them.
on('GET', '/api/market/assets', ({ q }): Res<'assets'> => {
  const scope = q.get('scope')
  if (scope && scope !== 'named' && scope !== 'listed') throw new Fail(400, 'bad_request', 'scope must be named or listed.')
  if (q.has('with') && scope !== 'named') throw new Fail(400, 'bad_request', 'with needs scope=named.')
  const asked = (q.get('with') ?? '').split(',').map((s) => s.trim().toUpperCase()).filter(Boolean)
  if (asked.length > 20) throw new Fail(400, 'bad_request', 'with takes at most 20 symbols.')
  if (scope === 'listed') return { assets: fx.listedAssets }
  return { assets: [...fx.universeAssets, ...asked.flatMap((s) => fx.listedAssets.filter((a) => a.symbol === s))] }
})
on('GET', '/api/market/instruments', ({ res }): Omit<Res<'instruments'>, 'etag'> => {
  res.setHeader('Cache-Control', 'private, max-age=3600')
  res.setHeader('ETag', `"mock-${fx.instruments.length}"`)
  return { instruments: fx.instruments }
})
on('GET', '/api/market/candles', ({ q }): Res<'candles'> => {
  const interval = q.get('interval') as ChartInterval
  if (!Object.hasOwn(STEP, interval) && interval !== '1w' && interval !== '1M') throw new Fail(400, 'bad_request', 'unknown interval')
  const symbol = q.get('symbol') ?? ''
  const currency = q.get('currency') || 'USD'
  if (currency !== 'USD' && currency !== 'original') throw new Fail(400, 'bad_request', 'unknown candle currency')
  const before = q.has('before') ? Number(q.get('before')) : undefined
  if (before != null && (!/^\d+$/.test(q.get('before')!) || !Number.isSafeInteger(before) || before <= 0)) throw new Fail(400, 'bad_request', 'before must be positive Unix seconds.')
  const cs = candles(symbol, interval, Math.min(1000, Number(q.get('limit') ?? 500)), before)
  if (currency === 'USD') return { candles: cs }
  const quote = [...fx.universeAssets, ...fx.listedAssets].find((a) => a.symbol === symbol)?.quoteFeed
  if (!quote) throw new Fail(400, 'bad_request', 'Original currency candles require a quote-feed listing.')
  return { candles: cs.map((c) => ({ ...c, o: c.o / quote.fx, h: c.h / quote.fx, l: c.l / quote.fx, c: c.c / quote.fx })) }
})

on('GET', '/api/portfolio', ({ s }): Res<'portfolio'> => ({ portfolio: portfolioOf(s) }))
on('GET', '/api/portfolio/trades', ({ s, q }): Res<'trades'> => {
  const limit = Number(q.get('limit') ?? 50)
  if (!Number.isInteger(limit) || limit < 1 || limit > 200) throw new Fail(400, 'bad_request', 'limit must be 1..200')
  return { trades: s.trades.slice(0, limit).map(t => ({ ...t, ref: ownedRef(s, t) })) }
})
on('POST', '/api/orders', ({ s, body }): Res<'placeOrder'> => {
  const o = body as OrderRequest
  if (o.proposalId && filledBy(s, o.proposalId)) throw new Fail(409, 'conflict', 'This proposal was already filled.')
  const trade = placeOrder(s, o)
  if (o.proposalId) s.fills.set(o.proposalId, trade.id)
  if (o.proposalId) trade.ref = chatRef(s, o.proposalId)
  return { trade, portfolio: portfolioOf(s) }
})
on('POST', '/api/portfolio/close', ({ s, body }): Res<'closePosition'> => {
  const lot = find(s.positions, body.symbol, 'position')
  const trade = placeOrder(s, { symbol: body.symbol, side: lot.qty > 0 ? 'sell' : 'buy', qty: Math.abs(lot.qty), source: 'manual' })
  return { trade, portfolio: portfolioOf(s) }
})
// ── Trade desk: protection edits, perps context, trade refs, invested state ── begin
/** The chat whose stored message holds the trade card with this proposalId. */
function chatRef(s: State, proposalId: string): TradeRef | null {
  for (const c of s.chats.values())
    if (c.messages.some((m) => m.cards.some((card) => card.type === 'trade_proposal' && card.data.proposalId === proposalId)))
      return { kind: 'chat', id: c.id, label: c.title }
  return null
}

on('PATCH', '/api/portfolio/positions/:id', ({ s, id, body }): Res<'setProtection'> => {
  const symbol = id.toUpperCase()
  const px = mark(symbol)
  const lot = s.positions.get(symbol)
  if (!lot) throw new Fail(404, 'not_found', `No open ${symbol} position.`)
  if (!('stopLoss' in body) && !('takeProfit' in body)) throw new Fail(400, 'bad_request', 'Send stopLoss or takeProfit.')
  const long = lot.qty > 0
  const side = long ? 'long' : 'short'
  const price = `$${Math.round(px).toLocaleString('en-US')}`
  const { stopLoss: stop, takeProfit: take } = body as { stopLoss?: number | null; takeProfit?: number | null }
  for (const v of [stop, take]) if (v != null && !(v > 0)) throw new Fail(400, 'bad_request', 'stopLoss and takeProfit must be positive prices.')
  if (stop != null && (long ? stop >= px : stop <= px)) throw new Fail(400, 'bad_request', `For a ${side}, the stop must be ${long ? 'below' : 'above'} the price (${price}).`)
  if (take != null && (long ? take <= px : take >= px)) throw new Fail(400, 'bad_request', `For a ${side}, the target must be ${long ? 'above' : 'below'} the price (${price}).`)
  if (stop !== undefined) lot.stopLoss = stop
  if (take !== undefined) lot.takeProfit = take
  s.revision++
  return { portfolio: portfolioOf(s) }
})

on('GET', '/api/market/derivatives', ({ q }): Res<'derivatives'> => {
  const symbol = q.get('symbol') ?? ''
  const markPrice = mark(symbol) // unknown → 400
  return { derivatives: symbol === 'PAXG' ? null : fx.derivatives({ symbol, markPrice }) }
})

/** The caller's paper position in a portfolio: its invest legs at the mock's flat marks (null before any). */
function investedIn(s: State, p: AIPortfolio): AIPortfolio {
  const legs = s.trades.filter((t) => t.source === 'portfolio' && t.side === 'buy' && t.ref?.kind === 'portfolio' && t.ref.id === p.id)
  if (!legs.length) return { ...p, invested: null }
  if (accountUnavailable.has(s)) return { ...p, invested: null }
  const amount = round2(legs.reduce((a, t) => a + t.notional, 0))
  const attribution = attributeLots(orderedTrades(s), mark, t => t.ref?.kind === 'portfolio' ? t.ref.id : '').find(r => r.source === p.id)
  const realizedPnl = round2(attribution?.realizedPnl ?? 0)
  const unrealizedPnl = round2(attribution?.unrealizedPnl ?? 0)
  const totalPnl = round2((attribution?.realizedPnl ?? 0) + (attribution?.unrealizedPnl ?? 0))
  // Mock prices are flat, but a comparison still needs its first-buy anchor
  // within the same bounded retained candle window used by the detail.
  const firstAt = legs.at(-1)!.createdAt
  const benchmarkSymbol = p.backtest.benchmarkSymbol || 'BTC'
  const benchmarkLabel = p.backtest.benchmarkLabel || (benchmarkSymbol === 'SP500' ? 'S&P 500 perp' : 'BTC')
  const retainedStart = (Math.floor(Date.now() / 3_600_000) - 999) * 3600
  const benchmarkReturnPct = Date.parse(firstAt) / 1000 >= retainedStart ? 0 : null
  return { ...p, invested: { benchmarkSymbol, benchmarkLabel, benchmarkReturnPct, benchmarkSeries: null, windowStart: null, windowEnd: null, bars: 0, series: null, btcSeries: null, amount, trades: legs.length, firstAt, realizedPnl, unrealizedPnl, totalPnl, returnPct: round2((totalPnl / amount) * 100), btcReturnPct: benchmarkSymbol === 'BTC' ? benchmarkReturnPct : null } }
}
// ── Trade desk ── end

on('POST', '/api/portfolio/reset', ({ s }): Res<'resetPortfolio'> => {
  Object.assign(s, { cash: START, realized: 0, positions: new Map(), trades: [] })
  s.revision++
  for (const p of liveState(s).plans) { p.positions = []; p.log.unshift({ t: now(), kind: 'sync', text: 'Account reset; reconciled holdings and attribution cleared.' }); valueCopyPlan(s, p) }
  return { portfolio: portfolioOf(s) }
})

on('GET', '/api/chats', ({ s }): Res<'chats'> => ({
  chats: [...s.chats.values()].sort((a, b) => b.updatedAt.localeCompare(a.updatedAt)).map(({ id, title, updatedAt }) => ({ id, title, updatedAt })),
}))
on('GET', '/api/chats/:id', ({ s, id }): Res<'chat'> => {
  if (id === PENDING_CHAT) return pendingChat(s)
  const { title, messages } = find(s.chats, id, 'chat')
  return { chat: { id, title, messages: messages.map((m, i) => ({ ...m, cards: m.cards.map((c) => withStatus(s, c, messages[Math.max(0, i - 1)].createdAt)) })), pending: false } }
})
on('DELETE', '/api/chats/:id', ({ s, id }) => void (find(s.chats, id, 'chat'), s.chats.delete(id)), 204)
on('POST', '/api/chat/stream', ({ s, req, res, body }) => streamChat(s, req, res, body))

on('POST', '/api/copilot/plan', ({ s, body }): Res<'copilotPlan'> => {
  const { symbol, style, bias } = body as CopilotPlanRequest
  const px = mark(symbol)
  const short = bias === 'short'
  const plan = fx.plan({
    symbol,
    style,
    interval: ({ scalp: '15m', day: '1h', swing: '4h' } as const)[style],
    direction: short ? 'short' : 'long',
    entry: px,
    stopLoss: px * (short ? 1.016 : 0.984),
    takeProfit: px * (short ? 0.968 : 1.032),
    generatedAt: now(),
    desk: deskOf(s, symbol, short ? 'short' : 'long'),
  })
  return { plan, analysis: { ...fx.analysis, symbol, price: px }, outlook: { ...fx.outlook, symbol, spot: px }, headlines: [fx.news()], credits: spend(s, 1) }
})

/**
 * The desk read (contract "Desk read"): the newest completed, unresolved, unexpired forecast with a price rule on the
 * symbol; stance for `side`. ponytail: price rules only; the server also matches a question that names the asset.
 */
function deskOf(s: State, symbol: string, side: 'long' | 'short'): DeskRead | null {
  const f = [...s.forecasts.values()]
    .reverse()
    .find((f) => f.status === 'completed' && f.result && !f.resolution && f.priceRule?.symbol === symbol && f.resolutionDate >= now().slice(0, 10))
  if (!f?.result || !f.priceRule) return null
  const p = f.result.probabilities[0].p
  const bullish = f.priceRule.op === 'above' ? p : 1 - p
  const sideP = side === 'long' ? bullish : 1 - bullish
  return {
    forecastId: f.id,
    question: f.question,
    resolutionDate: f.resolutionDate,
    outcome: 'Yes',
    p,
    lean: p < 0.5 ? `No ${Math.round((1 - p) * 100)}%` : `Yes ${Math.round(p * 100)}%`,
    priceRule: f.priceRule,
    stance: sideP >= 0.53 ? 'supports' : sideP <= 0.47 ? 'opposes' : 'split',
  }
}
on('GET', '/api/copilot/desk', ({ s, q }): Res<'getDesk'> => {
  const symbol = (q.get('symbol') ?? '').toUpperCase()
  mark(symbol) // 400 for an unknown symbol
  return { desk: deskOf(s, symbol, 'long') }
})

on('GET', '/api/news', ({ q }): Res<'news'> => {
  const symbol = q.get('symbol')
  const topic = q.get('topic')
  const region = q.get('region')
  if (region !== null && !NEWS_REGIONS.includes(region)) throw new Fail(400, 'bad_request', 'region must be crypto, us, east_asia or southeast_asia')
  const news = NEWS().filter(
    (n) => (!symbol || n.symbols.includes(symbol)) && (!topic || n.topic === topic) && (!region || (n.regions as string[]).includes(region)),
  )
  return { news: news.slice(0, Number(q.get('limit') ?? 30)) }
})
// A session's brief (contract "Market intelligence"): a fresh one unless a test switches it. "none" has no brief that
// validates while the wire is live; "stale" is 9 h old, and one bullet cites a story stored with the brief only.
const BRIEF_FIXTURES = ['fresh', 'none', 'stale'] as const
export type BriefFixture = (typeof BRIEF_FIXTURES)[number]
const briefFixtures = new WeakMap<State, BriefFixture>()
export function briefFor(s: State): Res<'brief'> {
  const at = briefFixtures.get(s) ?? 'fresh'
  if (at === 'none') return { brief: null }
  const n1 = NEWS()[0]
  const bok = { id: 'n-bok', title: 'Bank of Korea holds rates as the won firms', url: 'https://en.yna.co.kr/view/bok-holds', source: 'en.yna.co.kr', publishedAt: hoursAgo(30) }
  return {
    brief: {
      ...fx.brief,
      bullets: at === 'stale' ? [...fx.brief.bullets, { text: 'The Bank of Korea holds as the won firms', symbols: [], sources: [bok.id] }] : fx.brief.bullets,
      sources: [{ id: n1.id, title: n1.title, url: n1.url, source: n1.source, publishedAt: n1.publishedAt }, ...(at === 'stale' ? [bok] : [])],
      regions: [
        { region: 'crypto', summary: 'ETF inflows extend while gold slips.' },
        { region: 'us', summary: 'The Fed holds and signals one more cut.' },
        { region: 'east_asia', summary: 'TSMC guides higher; the BoJ stays on hold.' },
      ],
      createdAt: hoursAgo(at === 'stale' ? 9 : 1),
    },
  }
}
export function setBriefFixture(s: State, scenario: BriefFixture) {
  if (!(BRIEF_FIXTURES as readonly string[]).includes(scenario)) throw new Fail(400, 'bad_request', 'Unknown brief fixture.')
  briefFixtures.set(s, scenario)
  return briefFor(s)
}
on('GET', '/api/news/brief', ({ s }) => briefFor(s))
// Explicit authenticated mock-only control. Application flows never call it.
on('POST', '/__e2e/brief-fixture', ({ s, body }) => setBriefFixture(s, body.scenario))
on('GET', '/api/news/pulse', (): Res<'getPulse'> => fx.pulse)

// Free discovery fixtures: canonical leaf IDs, complete manual predicates, and caller-local exact overlays.
function predictionLeaf(id: string): PredictionDraft {
  if (!['123', '124', '125', '201'].includes(id)) throw new Fail(409, 'conflict', 'The pinned market is missing or closed.')
  const date = new Date(Date.now() + 7 * DAY * 1000).toISOString().slice(0, 10)
  const endDate = `${date}T16:00:00Z`
  const below = id === '124'
  const touch = id === '125'
  const macro = id === '201'
  const question = macro ? 'Will the Fed cut rates at its next meeting?' : touch ? `Will Bitcoin touch $120,000 by ${date}?` : `Will Bitcoin be ${below ? 'below $80,000' : 'above $100,000'} at noon ET on ${date}?`
  const slug = macro ? 'fed-next-meeting-cut' : touch ? 'bitcoin-touch-120000' : below ? 'bitcoin-below-80000' : linkedMarket.marketSlug
  const eventSlug = macro ? 'fed-next-meeting' : 'bitcoin-weekly-price'
  const description = macro
    ? 'Resolves Yes if the Federal Reserve lowers its target range at its next scheduled meeting. See the official statement; otherwise No.'
    : `${touch ? 'Observe the entire provider window; prior touch history must be verified.' : `Use the one-minute Binance BTCUSDT candle at noon ET on ${date}; not a UTC daily close.`}\n${'Complete provider observation and fallback rules are preserved. '.repeat(38)}\nOtherwise No.`
  const snapshot: LinkedMarket = {
    ...linkedMarket, marketId: id, conditionId: `0x${(macro ? 'b' : touch ? 'c' : below ? 'e' : 'a').repeat(64)}`,
    marketSlug: slug, eventSlug, question, description, endDate, asOf: now(), resolutionSource: macro ? null : linkedMarket.resolutionSource,
  }
  const market: PredictionDraft['market'] = {
    ...predictionMarket, id, marketSlug: slug, conditionId: snapshot.conditionId, question, endDate, yesPrice: below ? 0.2 : macro ? 0.55 : touch ? 0.15 : 0.3,
    desk: null, thin: touch, volumeUsd: touch ? 500 : 10000, rule: macro ? null : { symbol: 'BTC', op: below ? 'below' : 'above', strike: below ? 80000 : touch ? 120000 : 100000, kind: touch ? 'touch' : 'close' },
    estimate: macro || touch ? null : { ...predictionMarket.estimate!, p: below ? 0.25 : 0.45, edgePp: below ? 5 : 15, signal: below ? 'wait' : 'yes', evPer100: below ? null : 50, spotAsOf: now(), windowEnd: hoursAgo(24), windowStart: hoursAgo(90 * 24) },
    estimateUnavailable: touch ? 'Prior touch history is unverified.' : macro ? 'Crowd odds only: no supported price model.' : null,
  }
  return {
    draft: { shape: 'binary', question, outcomes: ['Yes', 'No'], resolutionCriteria: description, resolutionDate: date, category: macro ? 'macro' : 'crypto', priceRule: null, context: '', linkedMarket: snapshot },
    market, event: { slug: eventSlug, title: macro ? 'Fed next meeting' : 'Bitcoin weekly price', url: `https://polymarket.com/event/${eventSlug}` },
  }
}

export function predictionDraftOf(id: string): PredictionDraft {
  const d = predictionLeaf(id)
  if (d.draft.resolutionDate <= now().slice(0, 10)) throw new Fail(409, 'conflict', 'This market ends today; the desk needs a future tracking date.')
  return d
}

const sameLinkedPredicate = (a: LinkedMarket, b: LinkedMarket) =>
  (['marketId', 'conditionId', 'marketSlug', 'eventSlug', 'question', 'description', 'resolutionSource', 'endDate'] as const).every((k) => a[k] === b[k])

export function predictionBoardOf(s: State, category = 'all', sort = 'edge'): PredictionBoard {
  if (!['all', 'crypto', 'macro'].includes(category) || !['edge', 'volume', 'ending'].includes(sort)) throw new Fail(400, 'bad_request', 'Invalid market category or sort.')
  const leaves = ['123', '124', '125', '201'].map(predictionLeaf)
  for (const leaf of leaves) {
    const canonical = leaf.draft.linkedMarket!
    const f = [...s.forecasts.values()].reverse().find((f) => f.status === 'completed' && f.result && !f.resolution && !f.replay && !superseded(s, f)
      && f.linkedMarket && sameLinkedPredicate(f.linkedMarket, canonical) && f.question.trim() === canonical.question
      && f.resolutionCriteria.trim() === canonical.description && f.resolutionDate === leaf.draft.resolutionDate && !f.priceRule
      && f.shape === 'binary' && f.outcomes.join(',') === 'Yes,No')
    if (f?.result) {
      const p = f.result.probabilities.find((o) => o.outcome === 'Yes')!.p
      const edgePp = round2((p - leaf.market.yesPrice) * 100)
      leaf.market.desk = { forecastId: f.id, question: f.question, p, match: 'exact', edgePp, evPer100: round2((p / leaf.market.yesPrice - 1) * 100), signal: leaf.market.thin || Math.abs(edgePp) < 5 ? 'wait' : edgePp > 0 ? 'yes' : 'no' }
    }
  }
  const events = ['crypto', 'macro'].filter((c) => category === 'all' || c === category).map((c) => {
    const rows = leaves.filter((d) => (c === 'macro') === (d.draft.category === 'macro'))
    return { ...rows[0].event, category: c as 'crypto' | 'macro', endDate: rows[0].market.endDate, volume24hUsd: c === 'crypto' ? 6000 : 2000, markets: rows.map((d) => d.market) }
  })
  if (sort === 'volume') events.sort((a, b) => b.volume24hUsd - a.volume24hUsd)
  else if (sort === 'ending') events.sort((a, b) => a.endDate.localeCompare(b.endDate))
  else events.sort((a, b) => Math.max(...b.markets.map((m) => Math.abs(m.estimate?.edgePp ?? 0))) - Math.max(...a.markets.map((m) => Math.abs(m.estimate?.edgePp ?? 0))))
  return { events, asOf: now(), stale: false }
}
on('GET', '/api/predictions/markets', ({ s, q }): Res<'predictionMarkets'> => predictionBoardOf(s, q.get('category') ?? 'all', q.get('sort') ?? 'edge'))
on('GET', '/api/predictions/markets/:id/draft', ({ id }): Res<'predictionDraft'> => predictionDraftOf(id))

// ── Forecast materials: files read once into a digest; the mock "reads" them when the run completes ── begin
type MaterialsBody = { attachments?: unknown; reuseMaterials?: { forecastId?: unknown; ids?: unknown }; keepMaterials?: unknown }
const readDigests = (list: ForecastMaterial[]) => list.filter((m) => m.status === 'read')
/** The run's materials, checked before any credit is spent: kept or reused digests first (free), then the new files. */
function materialsFor(s: State, body: MaterialsBody, draft: ForecastDraft, parent?: Forecast): { materials: ForecastMaterial[]; fresh: number } {
  const files = chatAttachments(body.attachments ?? [])
  let carried: ForecastMaterial[] = []
  if (parent) {
    const read = readDigests(parent.materials ?? [])
    if (body.keepMaterials !== undefined) {
      if (!Array.isArray(body.keepMaterials)) throw new Fail(400, 'bad_request', 'keepMaterials must be a list of material ids.')
      for (const id of body.keepMaterials) if (!read.some((m) => m.id === id)) throw new Fail(400, 'bad_request', `keepMaterials: ${id} is not a material of this forecast that the desk read.`)
      carried = read.filter((m) => (body.keepMaterials as string[]).includes(m.id))
    } else carried = read
    carried = carried.map((m) => ({ ...m, reusedFrom: m.reusedFrom ?? parent.id }))
  } else if (body.reuseMaterials) {
    const source = s.forecasts.get(String(body.reuseMaterials.forecastId ?? ''))
    if (!source) throw new Fail(400, 'bad_request', 'reuseMaterials.forecastId is not one of your forecasts.')
    if (source.question.trim().toLowerCase() !== draft.question.trim().toLowerCase() || source.shape !== draft.shape || source.outcomes.join('\0') !== draft.outcomes.join('\0'))
      throw new Fail(400, 'bad_request', 'Materials can only be reused for the same question. Attach the files again.')
    const ids = Array.isArray(body.reuseMaterials.ids) ? (body.reuseMaterials.ids as string[]) : null
    carried = readDigests(source.materials ?? []).filter((m) => !ids || ids.includes(m.id)).map((m) => ({ ...m, reusedFrom: m.reusedFrom ?? source.id }))
  }
  if (carried.length + files.length > 5) throw new Fail(400, 'bad_request', 'A forecast can carry at most 5 materials. Remove one first.')
  const fresh = files.map((f): ForecastMaterial => ({ ...f, id: '', status: 'pending', summary: '', asOf: null, findings: [], excerpt: '', readAt: null }))
  return { materials: [...carried, ...fresh].map((m, i) => ({ ...m, id: `D${i + 1}` })), fresh: files.length }
}
/** What the mock desk "reads" from a file: a fixed digest naming it; a file named unreadable.* fails (and is refunded). */
function readMaterials(s: State, f: Forecast, result: ForecastResult): ForecastResult {
  const list = (f.materials ?? []).map((m): ForecastMaterial => {
    if (m.status !== 'pending') return m
    if (/^unreadable\./i.test(m.name)) {
      s.credits = { ...s.credits, used: Math.max(0, s.credits.used - 1) }
      return { ...m, status: 'failed', error: 'unreadable' }
    }
    return {
      ...m, status: 'read', relevance: 'direct', asOf: now().slice(0, 7), readAt: now(),
      summary: `${m.name}: the user's file on this question. It states figures that point to Yes, which the desk checked against its research.`,
      findings: [{ claim: `${m.name} states that ETH ETF inflows reached $1.4bn in the last 30 days.`, ref: m.kind === 'image' ? '' : 'p. 1', asOf: now().slice(0, 10), direction: 'yes', strength: 'moderate' }],
      excerpt: `"ETH ETF inflows reached $1.4bn in the last 30 days" … (${m.name})`,
    }
  })
  f.materials = list
  const read = readDigests(list)
  if (!list.length) return result
  const base = result.findings ?? []
  const findings = [...base, ...read.flatMap((m) => m.findings.map((x) => ({ ...x, sourceIds: [m.id], angle: '', origin: 'material' as const })))]
    .map((x, i) => ({ ...x, id: `F${i + 1}` }))
  const failed = list.filter((m) => m.status === 'failed')
  return {
    ...result,
    findings,
    evidence: [...result.evidence, ...read.slice(0, 1).map((m) => ({ claim: m.findings[0].claim, source: `Your material · ${m.name}`, stance: 'supports' as const, asOf: m.findings[0].asOf ?? '', url: null, materialId: m.id }))],
    flags: [...result.flags, ...failed.map((m) => ({ type: 'low_information' as const, note: `The desk couldn't read your file ${m.name}, so this forecast does not use it.` }))],
  }
}
// ── Forecast materials ── end

/** Refusals happen before credits or any persistent report, including the canonical snapshot comparison. */
export function createForecastFromDraft(s: State, d: ForecastDraft, preserveSnapshot = false, body: MaterialsBody = {}, parent?: Forecast): Forecast {
  const question = d.question?.trim() ?? ''
  const criteria = d.resolutionCriteria?.trim() ?? ''
  let linkedMarket = d.linkedMarket
  if (Array.from(question).length < 10 || Array.from(question).length > 280 || d.resolutionDate <= now().slice(0, 10)
    || Array.from(criteria).length > (d.linkedMarket ? 8000 : 2000)) throw new Fail(400, 'bad_request', 'Invalid forecast draft.')
  if (d.linkedMarket) {
    const l = d.linkedMarket
    if (question !== l.question || criteria !== l.description || d.resolutionDate !== l.endDate.slice(0, 10) || d.priceRule
      || d.shape !== 'binary' || d.outcomes.map((o) => o.trim()).join(',') !== 'Yes,No') throw new Fail(400, 'bad_request', 'The posted predicate does not match its linked snapshot.')
    const fresh = predictionDraftOf(l.marketId).draft.linkedMarket!
    if (!sameLinkedPredicate(l, fresh)) throw new Fail(409, 'conflict', 'The linked market rules changed. Review a fresh draft.')
    // A new report pins the fresh server read; an update validates afresh but retains its parent's provenance.
    if (!preserveSnapshot) linkedMarket = fresh
  }
  if (d.priceRule && (!prices[d.priceRule.symbol] || isStockPerp(d.priceRule.symbol))) throw new Fail(400, 'bad_request', 'priceRule is invalid.')
  const { materials, fresh } = materialsFor(s, body, d, parent)
  spend(s, 5 + fresh)
  const { attachments: _a, reuseMaterials: _r, keepMaterials: _k, ...draft } = d as ForecastDraft & MaterialsBody
  void [_a, _r, _k]
  const forecast: Forecast = { ...draft, ...(linkedMarket ? { linkedMarket } : {}), question, resolutionCriteria: criteria, priceRule: d.priceRule ?? null, context: (d.context ?? '').trim(),
    id: randomUUID(), status: 'running', error: null, result: null, createdAt: now(), completedAt: null,
    resolution: null, replay: null, parentId: null, history: [], materials }
  s.forecasts.set(forecast.id, forecast)
  return forecast
}

on('POST', '/api/forecasts/draft', ({ s, body }): Res<'draftForecast'> => {
  const idea = String(body.idea ?? '').trim()
  if (idea.length < 3 || idea.length > 400) throw new Fail(400, 'bad_request', 'idea must be 3..400 characters')
  const due = new Date(Date.now() + 90 * DAY * 1000).toISOString().slice(0, 10)
  const draft: ForecastDraft = {
    shape: 'binary',
    question: idea.endsWith('?') ? idea : `${idea}?`,
    outcomes: ['Yes', 'No'],
    resolutionCriteria: `Resolves Yes if the Binance BTCUSDT daily close on ${due} confirms it, according to Binance data.`,
    resolutionDate: due,
    category: 'crypto',
    // ponytail: any BTC idea gets the same rule; the real draft model reads the threshold off the idea.
    priceRule: /\bBTC\b/i.test(idea) ? { symbol: 'BTC', op: 'above', threshold: 150_000, mode: 'by_date' } : null,
    context: '',
  }
  // A long-horizon stock idea gets ranges cut at multiples of today's level, and that level, as the API's draft does.
  if (/\bTSM\b/i.test(idea) && /\byears?\b|\b20[3-9]\d\b/i.test(idea)) return { ...TSM_DRAFT, credits: spend(s, 1) }
  return { draft, credits: spend(s, 1) }
})
on(
  'POST',
  '/api/forecasts',
  ({ s, body }): Res<'createForecast'> => {
    return { forecast: createForecastFromDraft(s, body as ForecastDraft, false, body as MaterialsBody), credits: s.credits }
  },
  201,
)

// The shared historical replay (contract "Track record"): read-only rows every session sees. Each carries
// the volatility-only reference (= its anchorP); the BTC row's desk hedged toward 50% uncited, so the guard reset it.
const GUARD_NOTE =
  "Server check: the desk moved toward 50% from the volatility-only reference without citing evidence, so the final probability is the reference's."
const REPLAY: Forecast[] = fx.replaySummaries.map((r, i) => {
  const base = fx.replayForecast().result!
  const anchorP = i === 0 ? base.reference!.pYes : r.headline!.p
  return fx.replayForecast({
    id: r.id,
    question: r.question,
    resolutionDate: r.resolutionDate,
    createdAt: r.createdAt,
    completedAt: r.completedAt,
    result: {
      ...base,
      probabilities: [{ outcome: 'Yes', p: r.headline!.p }, { outcome: 'No', p: 1 - r.headline!.p }],
      reference: { ...base.reference!, pYes: anchorP, asOf: `${r.createdAt.slice(0, 10)}T00:00:00Z` },
      // The server adds its own note when it resets a run, as the API does; the report shows the reset once, on the card.
      flags: i === 0 ? base.flags : [...base.flags, { type: 'low_information', note: GUARD_NOTE }],
      guard: i === 0 ? null : 'shrink',
    },
    replay: { asOf: r.createdAt.slice(0, 10), anchorP },
  })
})
const summaryOf = (f: Forecast): ForecastSummary => ({
  id: f.id,
  shape: f.shape,
  question: f.question,
  category: f.category,
  status: f.status,
  archivedFromStatus: f.archivedFromStatus ?? null,
  resolutionDate: f.resolutionDate,
  createdAt: f.createdAt,
  completedAt: f.completedAt,
  headline: f.result ? { outcome: 'Yes', p: f.result.probabilities[0].p } : null,
  resolvedOutcome: f.resolution?.outcome ?? null,
  replay: f.replay !== null,
  parentId: f.parentId ?? null,
  ...(f.result?.view
    ? { view: (({ median, p10, p90, pAbove, unit, flat, headline, basis }) => ({ median, p10, p90, pAbove, unit, flat, headline, basis: basis.kind }))(f.result.view) }
    : {}),
})
// ── Forecast updates: a lineage is a run and the runs that update it ── begin
/** The lineage's completed runs, oldest first: from the root down (Map order puts every parent before its updates). */
function lineage(s: State, f: Forecast): ForecastPoint[] {
  let root = f
  while (root.parentId && s.forecasts.has(root.parentId)) root = s.forecasts.get(root.parentId)!
  const tree = new Set([root.id])
  for (const g of s.forecasts.values()) if (g.parentId && tree.has(g.parentId)) tree.add(g.id)
  return [...s.forecasts.values()]
    .filter((g) => tree.has(g.id) && g.status === 'completed' && g.result && g.completedAt)
    .sort((a, b) => a.completedAt!.localeCompare(b.completedAt!))
    .map((g) => ({ id: g.id, outcome: 'Yes', p: g.result!.probabilities[0].p, at: g.completedAt! }))
}
const superseded = (s: State, f: Forecast) => [...s.forecasts.values()].some((g) => g.parentId === f.id && g.status === 'completed')
export function updateForecastFromParent(s: State, id: string, body: MaterialsBody = {}): Forecast {
  const f = find(s.forecasts, id, 'forecast')
  if (f.status !== 'completed' || f.resolution || f.resolutionDate <= now().slice(0, 10))
    throw new Fail(409, 'conflict', 'Only a completed, unresolved forecast can be updated.')
  if ([...s.forecasts.values()].some((g) => g.parentId === id && g.status === 'running')) throw new Fail(409, 'conflict', 'An update is already running.')
  const { shape, question, outcomes, resolutionCriteria, resolutionDate, category, priceRule, context, linkedMarket } = f
  const forecast = createForecastFromDraft(s, { shape, question, outcomes, resolutionCriteria, resolutionDate, category, priceRule, context, linkedMarket }, true, body, f)
  forecast.parentId = id
  forecast.history = lineage(s, f)
  return forecast
}
on(
  'POST',
  '/api/forecasts/:id/update',
  ({ s, id, body }): Res<'updateForecast'> => {
    return { forecast: updateForecastFromParent(s, id, body as MaterialsBody), credits: s.credits }
  },
  201,
)
/** What an update's run finishes with: the desk moved up 6 points, and says why. */
const UPDATED: ForecastResult = {
  ...fx.forecastResult,
  probabilities: [
    { outcome: 'Yes', p: 0.68 },
    { outcome: 'No', p: 0.32 },
  ],
  changeNote: 'Fresh ETH ETF inflows since the last run moved the desk up.',
}
// ── Forecast updates ── end
on('GET', '/api/forecasts', ({ s, q }): Res<'forecasts'> => ({
  forecasts: (q.get('scope') === 'replay' ? REPLAY : [...s.forecasts.values()].filter((f) => q.get('status') === 'archived'
    ? f.status === 'archived' : f.status !== 'archived' && !superseded(s, f)).reverse()).map(summaryOf),
}))
on('GET', '/api/forecasts/track-record', ({ s }): Res<'trackRecord'> => {
  // ponytail: the replay record is the fixture; yours scores binary P(Yes) only, like the e2e forecasts. Both stay under
  // claimMinN, so their claim is too_few.
  const mine = [...s.forecasts.values()].filter((f) => f.status === 'completed' && f.resolution && f.result)
  const brier = mine.reduce((sum, f) => sum + (f.result!.probabilities[0].p - (f.resolution!.outcome === 'Yes' ? 1 : 0)) ** 2, 0)
  return fx.trackRecord(mine.length ? { n: mine.length, brier: brier / mine.length, baselineBrier: 0.25, skill: 1 - brier / mine.length / 0.25 } : {})
})
on('POST', '/api/forecasts/:id/resolve', ({ s, id, body }): Res<'resolveForecast'> => {
  const f = find(s.forecasts, id, 'forecast')
  if (f.status !== 'completed') throw new Fail(409, 'conflict', 'Only a completed forecast can be resolved.')
  if (f.priceRule) throw new Fail(409, 'conflict', 'This forecast resolves automatically from Binance data.')
  const outcome = body.outcome as string | null
  if (outcome !== null && !f.outcomes.includes(outcome)) throw new Fail(400, 'bad_request', "outcome must be one of the forecast's outcomes.")
  f.resolution = outcome === null ? null : { outcome, resolvedAt: now(), by: 'manual' }
  return { forecast: f }
})
// ── Research provenance: a report the stored news wire fed (live search was off) ── begin
export const WIRE_FORECAST = 'f-wire'
on('GET', `/api/forecasts/${WIRE_FORECAST}`, (): Res<'forecast'> => ({
  forecast: {
    ...fx.forecast({ id: WIRE_FORECAST, question: 'Will the Binance SOL/USDT daily close on 2026-10-09 be above $200?', createdAt: hoursAgo(3), completedAt: hoursAgo(3) }),
    result: {
      ...fx.forecastResult,
      market: null,
      knowledgeNote: 'Stored news wire (2 headlines) + live Binance spot data + model knowledge (training data) as of 2026-10-02 08:30 UTC.',
      sources: [
        { id: 'S1', title: 'Solana validators ship Firedancer', url: 'https://www.coindesk.com/sol', source: 'CoinDesk', publishedAt: hoursAgo(5), read: false },
        { id: 'S2', title: 'Solana DEX volumes hit a record', url: 'https://blog.example/sol', source: 'Blog', publishedAt: hoursAgo(1), read: false },
      ],
      research: { queries: [], status: 'wire', note: fx.wireNote },
    },
    parentId: null,
    history: [],
  },
}))
// ── Research provenance ── end

on('GET', '/api/forecasts/:id', ({ s, id }): Res<'forecast'> => {
  const replay = REPLAY.find((f) => f.id === id)
  if (replay) return { forecast: replay }
  const f = find(s.forecasts, id, 'forecast')
  // ponytail: a run finishes on its second poll (~3 s) instead of the real 1–3 minutes.
  const polls = (s.forecastPolls.get(id) ?? 0) + 1
  s.forecastPolls.set(id, polls)
  if (f.status === 'running' && polls >= 2) {
    const result = f.parentId ? UPDATED : fx.forecastResult
    const linked = f.linkedMarket ? predictionLeaf(f.linkedMarket.marketId) : null
    const read = readMaterials(s, f, result)
    Object.assign(f, { status: 'completed', result: linked ? { ...read, market: { ...fx.marketOdds, question: linked.market.question, url: linked.event.url, yesPrice: linked.market.yesPrice, endDate: linked.market.endDate, match: 'exact', linkStatus: 'current', asOf: now(), edgePp: round2((read.probabilities[0].p - linked.market.yesPrice) * 100), evYes: read.probabilities[0].p - linked.market.yesPrice } } : read, completedAt: now() })
  }
  return { forecast: { ...f, parentId: f.parentId ?? null, history: lineage(s, f) } }
})
on('DELETE', '/api/forecasts/:id', ({ s, id }) => {
  const f = find(s.forecasts, id, 'forecast')
  if (f.status === 'archived') throw new Fail(404, 'not_found', 'Forecast not found.')
  f.archivedFromStatus = f.status
  f.status = 'archived'
}, 204)
on('POST', '/api/forecasts/:id/restore', ({ s, id }): Res<'restoreForecast'> => {
  const f = find(s.forecasts, id, 'forecast')
  if (f.status !== 'archived') throw new Fail(409, 'conflict', 'Forecast is not archived.')
  f.status = f.archivedFromStatus === 'completed' || (!f.archivedFromStatus && f.result) ? 'completed' : 'failed'
  if (f.archivedFromStatus === 'running') f.error = 'This run was interrupted when it was archived. Start a new run to try again.'
  f.archivedFromStatus = null
  return { forecast: f }
})

on(
  'POST',
  '/api/strategies/generate',
  ({ s, body }): Res<'generateStrategy'> => {
    const { prompt, symbol = 'SOL', interval = '4h' } = body as GenerateStrategyRequest
    if (!prompt || prompt.trim().length < 5) throw new Fail(400, 'bad_request', 'prompt must be 5..1000 characters')
    const name = `${symbol} EMA cross`
    const createdAt = now()
    const strategy = savedBacktest(fx.strategy({ id: randomUUID(), name, prompt, createdAt, spec: { ...fx.strategy().spec, name, symbol, interval: interval as Interval } }), createdAt)
    s.strategies.set(strategy.id, strategy)
    return { strategy, credits: spend(s, 2) }
  },
  201,
)
on('GET', '/api/strategies', ({ s, q }): Res<'strategies'> => ({
  strategies: (q.get('scope') === 'top' ? labStrategies() : [...s.strategies.values()].reverse()).map(summary),
}))
on('GET', '/api/strategies/:id', ({ s, id }): Res<'strategy'> => ({
  strategy: s.strategies.get(id) ?? find(new Map(labStrategies().map((x) => [x.id, x])), id, 'strategy'),
}))
on('DELETE', '/api/strategies/:id', ({ s, id }) => void (find(s.strategies, id, 'strategy'), s.strategies.delete(id)), 204)
// ponytail: the backtest echoes fx.strategy()'s metrics with its Sharpe from the optimizer grid when the pair is on it.
on('POST', '/api/strategies/backtest', ({ body }): Res<'backtest'> => {
  const { stopLossPct = null, takeProfitPct = null } = (body.spec ?? {}) as { stopLossPct?: number; takeProfitPct?: number }
  const cell = fx.optimizeResult.grid.find((c) => c.stopLossPct === stopLossPct && c.takeProfitPct === takeProfitPct)
  const { metrics, equity, benchmark, trades } = fx.strategy()
  return { metrics: cell ? { ...metrics, sharpe: cell.sharpe, totalReturnPct: cell.totalReturnPct } : metrics, equity, benchmark, trades }
})
on('POST', '/api/strategies/optimize', ({ body }): Res<'optimize'> => {
  if (!body.spec) throw new Fail(400, 'bad_request', 'spec is required.')
  return fx.optimizeResult
})

// ── Autopilot (contract "Autopilot"): the mock enters at once on deploy and never ticks ──
const pilots = new WeakMap<State, Autopilot[]>() // newest first
const pilotsOf = (s: State) => pilots.get(s) ?? (pilots.set(s, []), pilots.get(s)!)
const valued = (a: Autopilot): Autopilot => {
  const markPrice = mark(a.symbol)
  const equity = round2(a.equity! + a.positionQty * (markPrice - (a.markPrice ?? markPrice)))
  return { ...a, markPrice, equity, pnl: round2(equity - a.allocationUsd), pnlPct: round2(((equity - a.allocationUsd) / a.allocationUsd) * 100) }
}
const pilot = (s: State, id: string) => find(new Map(pilotsOf(s).map((a) => [a.id, a])), id, 'autopilot')
on(
  'POST',
  '/api/autopilots',
  ({ s, body }): Res<'deployAutopilot'> => {
    const { strategyId, allocationUsd, maxDrawdownPct } = body ?? {}
    if (!(allocationUsd >= 100 && allocationUsd <= 100_000)) throw new Fail(400, 'bad_request', 'allocationUsd must be 100..100,000.')
    if (!(maxDrawdownPct >= 1 && maxDrawdownPct <= 50)) throw new Fail(400, 'bad_request', 'maxDrawdownPct must be 1..50.')
    const st = s.strategies.get(strategyId) ?? find(new Map(labStrategies().map((x) => [x.id, x])), strategyId, 'strategy')
    if (CORE_SYMBOLS.includes(st.spec.symbol) && liveState(s).plans.some((p) => p.status === 'active')) throw new Fail(409, 'conflict', 'An active copy plan conflicts with an autopilot on a core symbol.')
    const running = pilotsOf(s).find((a) => a.status === 'running')
    if (running) throw new Fail(409, 'conflict', `You already have an autopilot running (${running.name}). Stop it first.`)
    const t = new Date()
    const startedAt = t.toISOString() // before the entry fill, as the server's started_at is
    const trade = placeOrder(s, { symbol: st.spec.symbol, side: 'buy', notional: (allocationUsd * st.spec.positionPct) / 100, source: 'autopilot', note: `Autopilot · ${st.name}` })
    trade.ref = { kind: 'autopilot', id: strategyId, label: st.name }
    const bar = Math.floor(t.getTime() / 1000 / (4 * 3600)) * 4 * 3600 - 4 * 3600 // the last closed 4h bar
    const a: Autopilot = {
      id: randomUUID(),
      strategyId,
      name: st.name,
      symbol: st.spec.symbol,
      interval: st.spec.interval,
      status: 'running',
      stopReason: null,
      allocationUsd,
      maxDrawdownPct,
      positionQty: trade.qty,
      avgPrice: trade.price,
      markPrice: mark(st.spec.symbol),
      equity: allocationUsd - trade.fee + trade.qty * (mark(st.spec.symbol) - trade.price),
      pnl: null,
      pnlPct: null,
      drawdownPct: 0,
      lastBarT: bar,
      nextCheckAt: new Date((bar + 8 * 3600) * 1000).toISOString(),
      log: [
        { t: now(), kind: 'entry', text: `Opened Long ${round2(trade.qty)} ${trade.symbol} at $${trade.price.toFixed(2)}.` },
        { t: now(), kind: 'deploy', text: `Deployed with $${allocationUsd.toLocaleString('en-US')}; checks every closed ${st.spec.interval} bar.` },
      ],
      startedAt,
      stoppedAt: null,
    }
    pilotsOf(s).unshift(a)
    return { autopilot: valued(a), trade, portfolio: portfolioOf(s) }
  },
  201,
)
on('GET', '/api/autopilots', ({ s }): Res<'autopilots'> => ({ autopilots: pilotsOf(s).map(valued) }))
on('GET', '/api/autopilots/:id', ({ s, id }): Res<'autopilot'> => {
  const a = pilot(s, id)
  return { autopilot: valued(a), trades: s.trades.filter((t) => t.source === 'autopilot' && t.symbol === a.symbol && t.createdAt >= a.startedAt) }
})
on('POST', '/api/autopilots/:id/stop', ({ s, id, body }): Res<'stopAutopilot'> => {
  const a = pilot(s, id)
  if (a.status !== 'running') throw new Fail(409, 'conflict', 'This autopilot is not running.')
  const trade =
    body?.closePosition && a.positionQty
      ? placeOrder(s, { symbol: a.symbol, side: 'sell', qty: a.positionQty, source: 'autopilot', note: `Autopilot · ${a.name}` })
      : null
  Object.assign(a, valued(a), { status: 'stopped', stopReason: 'user', positionQty: 0, nextCheckAt: null, stoppedAt: now() })
  a.log = [{ t: now(), kind: 'stop', text: 'Stopped by you.' }, ...a.log]
  return { autopilot: a, trade, portfolio: portfolioOf(s) }
})

// ── Live paper arena: explicit mock-only future-step control; GET never generates a fill. ──
const CORE_SYMBOLS = ['BTC', 'ETH', 'SOL', 'BNB', 'XRP', 'ADA', 'DOGE', 'AVAX', 'LINK', 'PAXG', 'TON', 'SUI']
type LiveMock = { plans: CopyPlan[]; cursors: Map<string, number>; deliveries: Set<string>; step: number; events: Notification[]; enabled: boolean }
const liveMocks = new WeakMap<State, LiveMock>()
const liveState = (s: State): LiveMock => {
  if (!liveMocks.has(s)) liveMocks.set(s, { plans: [], cursors: new Map(), deliveries: new Set(), step: 26, events: [], enabled: true })
  return liveMocks.get(s)!
}
function reconcileCopyPlans(s: State) {
  const plans = liveState(s).plans.filter((p) => p.status === 'active')
  for (const symbol of new Set(plans.flatMap((p) => p.positions.map((h) => h.symbol)))) {
    const owned = plans.reduce((n, p) => n + (p.positions.find((h) => h.symbol === symbol)?.qty ?? 0), 0)
    const available = Math.max(0, s.positions.get(symbol)?.qty ?? 0)
    if (owned > available) for (const p of plans) {
      const h = p.positions.find((h) => h.symbol === symbol)
      if (h) { h.qty *= available / owned; p.log.unshift({ t: now(), kind: 'sync', text: `${symbol} ownership reduced after a manual trade.` }) }
      p.positions = p.positions.filter((h) => h.qty > 1e-10)
    }
  }
}
function valueCopyPlan(s: State, p: CopyPlan) {
  p.deployedUsd = p.positions.reduce((n, h) => n + h.qty * mark(h.symbol), 0)
  const trades = s.trades.filter((t) => t.ref?.kind === 'copy' && t.ref.id === p.id)
  p.invested = trades.filter((t) => t.side === 'buy').reduce((n, t) => n + t.notional, 0)
  p.trades = trades.length
  const attribution = attributeLots(orderedTrades(s), mark, t => t.ref?.kind === 'copy' ? t.ref.id : '').find(r => r.source === p.id)
  p.realizedPnl = attribution?.realizedPnl ?? 0
  p.unrealizedPnl = attribution?.unrealizedPnl ?? 0
  p.pnl = p.realizedPnl + p.unrealizedPnl
  return p
}
export function createCopyPlan(s: State, agentId: string, input: { ratio: number; maxUsd: number }): CopyPlan {
  const state = liveState(s), a = fx.liveBoard().agents.find((a) => a.id === agentId)
  if (!a) throw new Fail(404, 'not_found', 'Agent not found.')
  if (!Number.isFinite(input.ratio) || input.ratio < 0.1 || input.ratio > 1 || !Number.isFinite(input.maxUsd) || input.maxUsd < 100 || input.maxUsd > 50000) throw new Fail(400, 'bad_request', 'Use a ratio of 0.1..1 and a cap of $100..$50,000.')
  if (!state.enabled) throw new Fail(409, 'conflict', 'The live desk is paused on this server.')
  if (state.plans.some((p) => p.agentId === agentId && p.status === 'active')) throw new Fail(409, 'conflict', 'An active copy plan already exists for this agent.')
  if (pilotsOf(s).some((a) => a.status === 'running' && CORE_SYMBOLS.includes(a.symbol))) throw new Fail(409, 'conflict', 'A core-symbol autopilot is running. Stop it before copying.')
  const p = fx.copyPlan({ id: randomUUID(), agentId, agentName: a.name, personaId: a.personaId, ratio: input.ratio, maxUsd: input.maxUsd, createdAt: now() })
  p.log = [{ t: p.createdAt, kind: 'start', text: 'Copies future fills only; no current holdings bought.' }]
  state.plans.unshift(p); state.cursors.set(p.id, state.step)
  return p
}
export function liveBoardOf(s: State): LiveBoard {
  reconcileCopyPlans(s)
  const state = liveState(s), board = fx.liveBoard()
  board.myPlans = state.plans.filter((p) => p.status === 'active').map((p) => valueCopyPlan(s, p))
  board.desk = { ...board.desk, enabled: state.enabled, status: state.enabled ? 'live' : 'paused', nextStepAt: state.enabled ? board.desk.nextStepAt : null, steps: state.step }
  board.agents = board.agents.map((a) => ({ ...a, steps: state.step, followers: a.followers + board.myPlans.filter((p) => p.agentId === a.id).length }))
  return board
}
function copyEvent(s: State, n: Notification) { liveState(s).events.unshift(n); bellSeen.delete(s) }
export function advanceLiveStep(s: State, step = liveState(s).step + 1, agentId = 'live-momentum') {
  const state = liveState(s)
  if (!state.enabled) return
  reconcileCopyPlans(s)
  for (const p of state.plans.filter((p) => p.status === 'active' && p.agentId === agentId)) {
    const key = `${p.id}:${agentId}:${step}:BTC:buy`
    if (step <= state.cursors.get(p.id)! || state.deliveries.has(key)) continue
    state.deliveries.add(key)
    valueCopyPlan(s, p)
    const notional = Math.min(1000 * p.ratio, p.maxUsd - p.deployedUsd!)
    if (notional < 10 || (s.positions.get('BTC')?.qty ?? 0) < 0) { p.log.unshift({ t: now(), kind: 'skip', text: 'BTC buy skipped: cap, minimum leg or external short.' }); continue }
    const trade = placeOrder(s, { symbol: 'BTC', side: 'buy', notional, source: 'copy', note: `Copy · ${p.agentName}` })
    trade.ref = { kind: 'copy', id: p.id, label: `Copy · ${p.agentName}` }
    const h = p.positions.find((h) => h.symbol === 'BTC')
    if (h) h.qty += trade.qty; else p.positions.push({ symbol: 'BTC', qty: trade.qty })
    p.log.unshift({ t: now(), kind: 'order', text: `Bought ${trade.notional} USD BTC from future decision ${step}.` })
    valueCopyPlan(s, p)
    copyEvent(s, { id: `copy_order:${trade.id}`, kind: 'copy_order', title: `Copied ${p.agentName} · bought BTC`, body: `${fmtDollar(trade.notional)} at ${fmtDollar(trade.price)}`, symbol: 'BTC', link: '/account', at: now() })
  }
  state.step = Math.max(state.step, step)
}
const fmtDollar = (n: number) => `$${n.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`
export function stopCopyPlan(s: State, id: string, closePositions: boolean, quotesAvailable = true): Res<'stopCopyPlan'> {
  const p = liveState(s).plans.find((p) => p.id === id)
  if (!p) throw new Fail(404, 'not_found', 'Copy plan not found.')
  if (p.status !== 'active') throw new Fail(409, 'conflict', 'This copy plan is not active.')
  reconcileCopyPlans(s)
  const trades: Trade[] = []
  if (closePositions) for (const h of [...p.positions]) {
    if (!quotesAvailable) { p.log.unshift({ t: now(), kind: 'skip', text: `${h.symbol} quote unavailable; position kept.` }); continue }
    const reserved = liveState(s).plans.filter((q) => q.status === 'active' && q.id !== p.id).reduce((n,q) => n + (q.positions.find((x) => x.symbol === h.symbol)?.qty ?? 0), 0)
    const qty = Math.min(h.qty, Math.max(0, (s.positions.get(h.symbol)?.qty ?? 0) - reserved))
    if (qty > 1e-10) {
      const t = placeOrder(s, { symbol: h.symbol, side: 'sell', qty, source: 'copy', note: `Copy · ${p.agentName}` })
      t.ref = { kind: 'copy', id: p.id, label: `Copy · ${p.agentName}` }; trades.push(t); h.qty -= qty
    }
  }
  p.positions = p.positions.filter((h) => h.qty > 1e-10)
  p.status = 'stopped'; p.stopReason = 'user'; p.stoppedAt = now()
  p.log.unshift({ t: now(), kind: 'stop', text: closePositions ? `Stopped; ${trades.length} closing legs filled; ${p.positions.length} residual holdings remain.` : 'Stopped; positions kept in the account.' })
  valueCopyPlan(s, p)
  copyEvent(s, { id: `copy_stopped:${p.id}`, kind: 'copy_stopped', title: `Stopped copying ${p.agentName}`, body: 'Stopped by you; positions kept or closed.', symbol: null, link: '/account', at: now() })
  return { plan: p, trades, portfolio: quotesAvailable ? portfolioOf(s) : null }
}
on('GET', '/api/arena/live', ({s}): Res<'liveArena'> => liveBoardOf(s))
on('GET', '/api/arena/live/agents/:id/steps', ({id,q}): Res<'liveArenaSteps'> => {
  if (!fx.liveBoard().agents.some((a) => a.id === id)) throw new Fail(404, 'not_found', 'Agent not found.')
  const limit = Number(q.get('limit') ?? 20), before = q.has('before') ? Number(q.get('before')) : 26
  if (!Number.isInteger(limit) || limit < 1 || limit > 50 || !Number.isInteger(before) || before < 0) throw new Fail(400, 'bad_request', 'Invalid decision page.')
  return { steps: Array.from({ length: Math.min(limit, before) }, (_,i) => fx.arenaStep(before-1-i, { t: 1791187200 - (26-before+1+i)*14400, reasoning: `${id} decision ${before-i}: closed-bar paper reasoning.` })) }
})
on('POST', '/api/arena/live/agents/:id/copy', ({s,id,body}): Res<'copyLiveAgent'> => ({ plan: createCopyPlan(s,id,body) }), 201)
on('GET', '/api/copy-plans', ({s}): Res<'copyPlans'> => { reconcileCopyPlans(s); return { plans: [...liveState(s).plans].sort((a,b) => Number(b.status === 'active') - Number(a.status === 'active')).slice(0,20).map((p) => valueCopyPlan(s,p)) } })
on('POST', '/api/copy-plans/:id/stop', ({s,id,body}): Res<'stopCopyPlan'> => stopCopyPlan(s,id,body.closePositions))
on('POST', '/__e2e/live-step', ({s,body}) => { advanceLiveStep(s, body.step, body.agentId); return { portfolio: portfolioOf(s) } })
on('POST', '/__e2e/live-enabled', ({s,body}) => { liveState(s).enabled = !!body.enabled; return liveBoardOf(s) })

on('GET', '/api/arena/options', (): Res<'arenaOptions'> => fx.arenaOptions)
on(
  'POST',
  '/api/arena/runs',
  ({ s, body }): Res<'createArenaRun'> => {
    const req = body as CreateArenaRunRequest
    if (!req.agents?.length || req.agents.length > fx.arenaOptions.maxAgents) throw new Fail(400, 'bad_request', 'pick 1..4 agents')
    if ([...s.runs.values()].some((r) => r.status === 'running')) throw new Fail(409, 'conflict', 'You already have a run in progress.')
    const run = arenaRun(randomUUID(), req)
    spend(s, run.credits)
    s.runs.set(run.id, run)
    return { run, warnings: [], credits: s.credits }
  },
  201,
)
on('GET', '/api/arena/runs', ({ s }): Res<'arenaRuns'> => ({ runs: [...s.runs.values()].reverse().map(runSummary) }))
on('GET', '/api/arena/runs/:id', ({ s, id }): Res<'arenaRun'> => {
  const run = find(s.runs, id, 'run')
  // ponytail: each poll finishes half the steps, so a run completes on its second poll (~3 s after the first).
  if (run.status === 'running') advance(run, Math.min(run.steps, run.stepsDone + Math.ceil(run.steps / 2)))
  return { run }
})
on('GET', '/api/arena/runs/:id/steps', ({ s, id, q }): Res<'arenaSteps'> => {
  const run = find(s.runs, id, 'run')
  const agent = run.agents[Number(q.get('agent'))]
  if (!agent) throw new Fail(400, 'bad_request', 'unknown agent')
  const after = Number(q.get('after') ?? -1)
  const from = after + 1
  return { steps: Array.from({ length: Math.max(0, Math.min(run.stepsDone - from, 100)) }, (_, i) => fx.arenaStep(from + i)) }
})
on('POST', '/api/arena/runs/:id/cancel', ({ s, id }): Res<'cancelArenaRun'> => {
  const run = find(s.runs, id, 'run')
  if (run.status !== 'running') throw new Fail(409, 'conflict', 'The run is not running.')
  const refund = Math.floor((run.agents.length * (run.steps - run.stepsDone)) / 2)
  s.credits = { ...s.credits, used: Math.max(0, s.credits.used - refund) }
  Object.assign(run, { status: 'cancelled', completedAt: now(), credits: run.credits - refund })
  return { run, credits: s.credits }
})
on('DELETE', '/api/arena/runs/:id', ({ s, id }) => {
  if (find(s.runs, id, 'run').status === 'running') throw new Fail(409, 'conflict', 'Cancel the run first.')
  s.runs.delete(id)
}, 204)

on('POST', '/api/ai-portfolios/build', ({ s, body }): Res<'buildPortfolio'> => {
  const prompt = String(body.prompt ?? '').trim()
  if (prompt.length < 3) throw new Fail(400, 'bad_request', 'prompt must be 3..500 characters')
  const portfolio = (/\bstock\b|\bequity\b|\bAAPL\b|\bNVDA\b/i.test(prompt) ? fx.stockPortfolio : fx.aiPortfolio)({ id: randomUUID(), prompt, createdAt: now() })
  s.portfolios.unshift(portfolio)
  return { portfolio, credits: spend(s, 2) }
})
on('GET', '/api/ai-portfolios', ({ s }): Res<'aiPortfolios'> => ({ portfolios: s.portfolios.map((p) => investedIn(s, p)) }))
on('GET', '/api/ai-portfolios/:id', ({ s, id }): Res<'aiPortfolio'> => ({
  portfolio: overlapIn(s, investedIn(s, find(new Map(s.portfolios.map((p) => [p.id, p])), id, 'portfolio'))),
}))
on('POST', '/api/ai-portfolios/:id/invest', ({ s, id, body }): Res<'investPortfolio'> => {
  const p = find(new Map(s.portfolios.map((x) => [x.id, x])), id, 'portfolio')
  const amount = Number(body.amount)
  if (!(amount >= 50)) throw new Fail(400, 'bad_request', 'the minimum is $50')
  const trades: Trade[] = []
  const skipped: { symbol: string; reason: string }[] = []
  for (const h of p.holdings) {
    const notional = round2(amount * h.weight)
    if (notional < 10) skipped.push({ symbol: h.symbol, reason: 'below the $10 minimum order' })
    else trades.push(Object.assign(placeOrder(s, { symbol: h.symbol, side: 'buy', notional, source: 'portfolio', note: p.name }), { ref: { kind: 'portfolio' as const, id: p.id, label: p.name } }))
  }
  return { trades, portfolio: portfolioOf(s), skipped }
})

// ── Signals board & account performance ─────────────────────────────────────

// A session's board state (contract "Signals"): a fresh board unless a test switches it. "refreshing" serves the newest
// board that still validates, 3 h 55 min old and so past its 3 h 50 min refill start; "empty" has none that validates;
// "first" has never had one. "failing" has failed 3 refreshes in a row and waits 40 min for the next, under a 9 h old
// board; "failing-empty" is the same with no board that validates. "new-ledger" is the first steady-v1 board: no exit
// or score yet. "legacy" still shows a board stored before steady-v1 (picks with invalidation 0; the first steady
// refresh replaces it): an empty ledger, and the history its board chain gives (legacy rows, with the reasons their boards recorded).
// "switch" is 30 h after the switch: the ledger's first exit, then the legacy rows of the week's older boards.
const SIGNALS_FIXTURES = ['ready', 'refreshing', 'empty', 'first', 'failing', 'failing-empty', 'legacy', 'new-ledger', 'switch'] as const
export type SignalsFixture = (typeof SIGNALS_FIXTURES)[number]
const signalsFixtures = new WeakMap<State, SignalsFixture>()
export function signalsFor(s: State): Res<'signals'> {
  const at = signalsFixtures.get(s) ?? 'ready'
  const failing = at === 'failing' || at === 'failing-empty'
  const created = Date.now() - (at === 'failing' ? 545 : 235) * 60_000
  if (at === 'legacy' || at === 'switch') {
    const legacy = at === 'legacy'
    const board = legacy ? fx.signalBoardLegacy() : fx.signalBoard()
    return {
      board, history: legacy ? fx.signalHistoryLegacy : fx.signalHistorySwitch(), historyUnavailable: false, refreshing: false,
      asOf: board.createdAt, firstBoard: false, refresh: { state: 'idle', failures: 0, lastErrorAt: null, retryAt: null },
      record: legacy ? fx.signalRecordEmpty() : fx.signalRecordSwitch(),
    }
  }
  const settled = at === 'ready' || at === 'new-ledger'
  const board = settled ? fx.signalBoard()
    : at === 'refreshing' || at === 'failing' ? fx.signalBoard({ id: 'sb0', createdAt: new Date(created).toISOString(), nextAt: new Date(created + 230 * 60_000).toISOString() })
    : null
  const refresh = failing
    ? { state: 'retrying' as const, failures: 3, lastErrorAt: new Date(Date.now() - 20 * 60_000).toISOString(), retryAt: new Date(Date.now() + 40 * 60_000).toISOString() }
    : { state: settled ? ('idle' as const) : ('running' as const), failures: 0, lastErrorAt: null, retryAt: null }
  // The history and record come from the ledger, so a failing refresh leaves them in place (no legacy board is retained
  // here, so no candle is read and historyUnavailable is false).
  const record = at === 'new-ledger' ? fx.signalRecordEmpty() : fx.signalRecord()
  return {
    board, history: board && at !== 'new-ledger' ? fx.signalHistory : [], historyUnavailable: false, refreshing: !settled,
    asOf: board?.createdAt ?? null, firstBoard: at === 'first', refresh, record,
  }
}
export function setSignalsFixture(s: State, scenario: SignalsFixture) {
  if (!(SIGNALS_FIXTURES as readonly string[]).includes(scenario)) throw new Fail(400, 'bad_request', 'Unknown Signals fixture.')
  signalsFixtures.set(s, scenario)
  return signalsFor(s)
}
on('GET', '/api/signals', ({ s }) => signalsFor(s))
// Explicit authenticated mock-only control. Application flows never call it.
on('POST', '/__e2e/signals-fixture', ({ s, body }) => setSignalsFixture(s, body.scenario))

// Asia screener (contract "Asia screener"): the fixture rows filtered, sorted and paged as the API does. The
// cursor is an offset here; the client treats it as opaque either way.
const SCREENER_MARKETS = ['JP', 'KR', 'TW', 'HK', 'SG', 'ID', 'TH', 'MY', 'PH', 'VN'] as const
const pct = (r: ScreenerRow) => r.pctVsSma
const SCREENER_BANDS: Record<string, (r: ScreenerRow) => boolean> = {
  all: () => true,
  below: (r) => pct(r) != null && pct(r)! > -50 && pct(r)! <= -5,
  deep: (r) => pct(r) != null && pct(r)! > -70 && pct(r)! <= -50,
  distressed: (r) => pct(r) != null && pct(r)! <= -70,
  approaching: (r) => pct(r) != null && pct(r)! >= 0 && pct(r)! <= 15,
  oversold: (r) => r.rsi14w != null && r.rsi14w < 30 && pct(r) != null && pct(r)! < 0,
}
const SCREENER_SORTS: Record<string, (r: ScreenerRow) => number | string | null> = {
  ticker: (r) => r.ticker, name: (r) => r.name.toLowerCase(), market: (r) => SCREENER_MARKETS.indexOf(r.market), price: (r) => r.price,
  priceUsd: (r) => r.priceUsd, pctVsSma: pct, rsi: (r) => r.rsi14w, drawdown: (r) => r.drawdownPct, athAt: (r) => r.athAt,
  updated: (r) => r.quote?.lastTradeAt ?? r.updatedAt, direction: (r) => (r.pctVsSma != null && r.pctVsSmaPrev != null ? r.pctVsSma - r.pctVsSmaPrev : null),
}
// Live prices (contract "Asia screener", live columns): the fixtures as of Wednesday 07:10 UTC, until the session's
// first in-place refresh (GET /api/screener/asia/quotes). From then on Tencent (HKEX, open) trades at HK$475.30, read
// at 07:10, which puts it past D01.SI in the default order: the page keeps its rows where they were until a re-sort.
const screenerReads = new WeakMap<State, number>()
const round = (n: number, d: number) => Math.round(n * 10 ** d) / 10 ** d
/** A row at a new live price, its columns derived as the API derives them, against the run's averages. */
export function repriced(r: ScreenerRow, price: number, quote: Partial<ScreenerQuote> & { lastTradeAt: string }): ScreenerRow {
  const pct = r.sma200w != null ? round((price / r.sma200w - 1) * 100, 2) : null
  const high = r.ath != null && price > r.ath
  const ath = r.ath == null ? null : Math.max(r.ath, price)
  return {
    ...r, price, priceAt: quote.lastTradeAt, priceUsd: r.fx ? round(price * r.fx.usdPerUnit, 4) : price,
    pctVsSma: pct, direction: pct == null || r.pctVsSmaPrev == null ? r.direction : pct < r.pctVsSmaPrev ? 'deepening' : 'recovering',
    ath, athAt: high ? quote.lastTradeAt.slice(0, 10) : r.athAt, drawdownPct: ath != null ? round((price / ath - 1) * 100, 2) : r.drawdownPct,
    quote: { ...r.quote!, ...quote, price },
  }
}
export function screenerRowsFor(s?: State): ScreenerRow[] {
  const rows = [...fx.screenerRows, fx.listedScreenerRow]
  if (!s || !screenerReads.get(s)) return rows
  return rows.map((r) => (r.ticker === '0700.HK'
    ? repriced(r, 475.3, { lastTradeAt: '2026-10-14T07:09:10Z', readAt: '2026-10-14T07:10:04Z', staleAt: '2026-10-14T07:27:04Z' })
    : r))
}
export function screenerQuotesFor(q: URLSearchParams, s?: State): Res<'screenerQuotes'> {
  // Comma-separated and/or repeated, case-insensitive, duplicates folded: 1 to 100, as the API takes them.
  const asked = [...new Set(q.getAll('tickers').flatMap((v) => v.split(',')).map((t) => t.trim().toUpperCase()).filter(Boolean))]
  if (!asked.length) throw new Fail(400, 'bad_request', 'tickers is required: up to 100 constituent tickers, comma-separated (7203.T,005930.KS).')
  if (asked.length > 100) throw new Fail(400, 'bad_request', 'tickers is limited to 100.')
  if (s) screenerReads.set(s, (screenerReads.get(s) ?? 0) + 1)
  const rows = new Map(screenerRowsFor(s).map((r) => [r.ticker.toUpperCase(), r]))
  const page = fx.screenerPage()
  return {
    rows: asked.flatMap((t) => rows.get(t) ?? []), unknown: asked.filter((t) => !rows.has(t)),
    asOf: page.asOf, run: page.run, lastCompletedAt: page.lastCompletedAt, pollSeconds: 60,
  }
}

export function screenerPageFor(q: URLSearchParams, s?: State): Res<'screener'> {
  const bad = (msg: string): never => { throw new Fail(400, 'bad_request', msg) }
  const filter = q.get('filter') || 'all'
  const market = (q.get('market') ?? '').toUpperCase()
  const text = (q.get('q') ?? '').trim().toLowerCase()
  const sort = q.get('sort') || 'pctVsSma'
  const key = sort.replace(/^-/, '')
  const limit = q.has('limit') ? Number(q.get('limit')) : 50
  const offset = q.get('cursor') ? Number(Buffer.from(q.get('cursor')!, 'base64url').toString()) : 0
  if (!SCREENER_BANDS[filter]) bad('Unknown filter.')
  if (market && !(SCREENER_MARKETS as readonly string[]).includes(market)) bad('Unknown market.')
  if (!SCREENER_SORTS[key]) bad('Unknown sort.')
  if (!Number.isInteger(limit) || limit < 1 || limit > 100) bad('limit must be an integer from 1 to 100.')
  if (!Number.isInteger(offset) || offset < 0) bad('Invalid cursor.')
  const rows = screenerRowsFor(s)
  const names = new Map(fx.screenerPage().markets.map((m) => [m.code, m.name.toLowerCase()]))
  // As the API matches: ticker, code, names, index, the market's name ("korea"), or exactly its code ("kr").
  const inScope = rows.filter((r) => (!market || r.market === market) &&
    (!text || [r.ticker, r.code, r.name, r.listName, ...r.indices.map((i) => i.index)].some((v) => v.toLowerCase().includes(text)) ||
      names.get(r.market)!.includes(text) || r.market.toLowerCase() === text))
  const value = SCREENER_SORTS[key]
  const matched = inScope.filter(SCREENER_BANDS[filter]).sort((a, b) => {
    const [x, y] = [value(a), value(b)]
    if ((x == null) !== (y == null)) return x == null ? 1 : -1 // rows without the value come last either way
    const c = x == null || x === y ? 0 : (x < y! ? -1 : 1) * (sort.startsWith('-') ? -1 : 1)
    return c || (a.ticker < b.ticker ? -1 : a.ticker > b.ticker ? 1 : 0)
  })
  const page = fx.screenerPage()
  return {
    ...page,
    rows: matched.slice(offset, offset + limit),
    nextCursor: offset + limit < matched.length ? Buffer.from(String(offset + limit)).toString('base64url') : null,
    total: matched.length,
    counts: Object.fromEntries(Object.entries(SCREENER_BANDS).map(([k, f]) => [k, inScope.filter(f).length])) as Record<ScreenerFilter, number>, // every filter, 0 included, as the API sends them
    markets: page.markets.map((m) => ({ ...m, count: rows.filter((r) => r.market === m.code).length })),
  }
}
on('GET', '/api/screener/asia', ({ q, s }) => screenerPageFor(q, s))
on('GET', '/api/screener/asia/quotes', ({ q, s }) => screenerQuotesFor(q, s))

export function equityOf(s: State, range: EquityRange): Res<'portfolioEquity'> {
  const [window, interval, step] = EQUITY_GRIDS[range]
  const end = Math.floor(Date.now() / 1000)
  const start = Math.floor((end - window) / step) * step
  const trades = orderedTrades(s), grid: number[] = []
  if (!trades.length && !stakes(s).length) return { range, interval, startingBalance: START, points: [{ t: start, v: START }, { t: end, v: portfolioOf(s).equity }] }
  for (let t = start; t < end; t += step) grid.push(t)
  const histories = new Map([...new Set(trades.map(t => t.symbol))].map(symbol => [symbol, candles(symbol, interval, Math.min(window / step + 2, 1000))]))
  const points = replayPoints(trades, stakes(s), grid, (symbol, t) => {
    const bars = histories.get(symbol)!
    return accountHistoryPrices.get(s)?.[symbol] ?? bars.findLast(bar => bar.t + step <= t)?.c ?? bars[0].o
  })
  points.push({ t: end, v: portfolioOf(s).equity + openStakes(s) })
  return { range, interval, startingBalance: START, points }
}
on('GET', '/api/portfolio/equity', ({ s, q }): Res<'portfolioEquity'> => {
  const range = q.get('range') ?? '7d'
  if (!Object.hasOwn(EQUITY_GRIDS, range)) throw new Fail(400, 'bad_request', 'range must be 1d, 7d or 30d')
  return equityOf(s, range as EquityRange)
})

export function attributionOf(s: State): Res<'portfolioAttribution'> {
  const sources = attributeLots(orderedTrades(s), mark)
  if (stakes(s).length) {
    const realizedPnl = stakes(s).reduce((sum, p) => sum + (p.pnl ?? 0), 0)
    sources.push({ source: 'prediction', trades: stakes(s).length, realizedPnl, unrealizedPnl: 0, fees: 0, totalPnl: realizedPnl })
    sources.sort((a, b) => Math.abs(b.totalPnl) - Math.abs(a.totalPnl) || a.source.localeCompare(b.source))
  }
  return { sources, totalPnl: sources.reduce((sum, r) => sum + r.totalPnl, 0) }
}
on('GET', '/api/portfolio/attribution', ({ s }): Res<'portfolioAttribution'> => attributionOf(s))

// Portfolio review mock: book facts are factual; no statistical history is invented.
export function reviewOf(s: State): PortfolioReview {
  const p = portfolioOf(s)
  const stake = openStakes(s)
  const equity = p.equity + stake
  const gross = p.grossExposure
  const holdings: PortfolioReview['holdings'] = [...p.positions].sort((a, b) => Math.abs(b.marketValue) - Math.abs(a.marketValue)).map((x) => {
    const side = x.qty < 0 ? 'short' as const : 'long' as const
    const crossed = x.stopLoss != null && (side === 'long' ? x.stopLoss >= x.markPrice : x.stopLoss <= x.markPrice)
    return { symbol: x.symbol, side, value: Math.abs(x.marketValue), pct: gross ? Math.abs(x.marketValue) / gross * 100 : 0, beta: null, stopLoss: x.stopLoss, halted: false, markAvailable: true, stopStatus: x.stopLoss == null ? 'missing' : crossed ? 'crossed' : 'valid' }
  })
  const weights = holdings.map((h) => h.pct / 100)
  const hhi = weights.reduce((n, w) => n + w * w, 0)
  const uncovered = holdings.filter((h) => h.stopStatus !== 'valid')
  const distance = p.positions.reduce((n, x) => n + (holdings.find((h) => h.symbol === x.symbol)?.stopStatus === 'valid' ? Math.abs(x.qty) * Math.abs(x.markPrice - x.stopLoss!) : 0), 0)
  return { asOf: now(), revision: s.revision, window: { days: 90, bars: 0 }, coverage: { modeledGrossExposure: 0, missingSymbols: holdings.map((h) => h.symbol), commonDays: 0, reason: holdings.length ? 'Mock daily history unavailable; whole-book risk is unknown.' : null }, positions: holdings.length, equity, grossExposure: gross, netExposure: p.positions.reduce((n, x) => n + x.marketValue, 0), exposurePct: equity > 0 ? gross / equity * 100 : null, predictionStakes: stake, predictionStakesPct: equity > 0 ? stake / equity * 100 : null, holdings, concentration: hhi ? { level: hhi >= .4 ? 'high' : hhi >= .25 ? 'medium' : 'low', top1Pct: holdings[0].pct, top3Pct: holdings.slice(0, 3).reduce((n, h) => n + h.pct, 0), hhi, effective: 1 / hhi } : null, betaBtc: null, volAnnPct: null, var: null, worstDay: null, drawdown30dPct: null, stress: [], correlations: [], clusters: [], stops: { covered: holdings.length - uncovered.length, uncovered: uncovered.map((h) => h.symbol), uncoveredPct: uncovered.reduce((n, h) => n + h.pct, 0), atRiskUsd: distance }, notes: holdings.length ? ['Known gross weights and stops remain useful; daily asset risk is unavailable.'] : stake ? ['Outcome stakes are separate from daily asset risk.'] : [] }
}
function overlapIn(s: State, p: AIPortfolio): AIPortfolio {
  const r = reviewOf(s)
  return { ...p, overlap: { revision: r.revision, asOf: r.asOf, coverage: r.coverage, holdings: r.holdings.map((h) => ({ symbol: h.symbol, side: h.side, bookPct: h.pct, basketPct: (p.holdings.find((x) => x.symbol === h.symbol)?.weight ?? 0) * 100, shared: p.holdings.some((x) => x.symbol === h.symbol) })), correlations: p.holdings.flatMap((a) => r.holdings.map((b) => ({ a: a.symbol, b: b.symbol, rho: null, days: 0 }))) } }
}
on('GET', '/api/portfolio/review', ({ s }): Res<'portfolioReview'> => reviewOf(s))

// ── Automations: alerts and the bell. The mock never fires an alert; the bell serves fixed items. ──
const alertsOf = new WeakMap<State, Alert[]>() // newest first
const myAlerts = (s: State) => alertsOf.get(s) ?? (alertsOf.set(s, []), alertsOf.get(s)!)
const bellSeen = new WeakSet<State>()
on('GET', '/api/alerts', ({ s, q }): Res<'alerts'> => {
  if (q.has('status') && !['active', 'all'].includes(q.get('status')!)) throw new Fail(400, 'bad_request', 'status must be active or all.')
  return { alerts: myAlerts(s)
    .filter((a) => q.get('status') !== 'active' || a.status === 'active')
    .sort((a, b) => Number(b.status === 'active') - Number(a.status === 'active') || b.createdAt.localeCompare(a.createdAt))
    .slice(0, 100).map(a => ({ ...a, lastRun: a.lastRun ? availableRun(s, a.lastRun) : null })), }
})
on(
  'POST',
  '/api/alerts',
  ({ s, body }): Res<'createAlert'> => {
    const r = body as AlertRequest
    if (r.action === 'research' && r.trigger !== 'schedule') throw new Fail(400, 'bad_request', 'Research is a schedule-only action.')
    if (r.trigger === 'schedule') return createSchedule(s, r)
    if (r.trigger === 'odds') return createOddsAlert(s, r)
    if (r.trigger === 'drawdown') return createDrawdown(s, r)
    let px = mark(r.symbol ?? '') // unknown → 400
    const action = r.action ?? 'notify'
    const price = r.price ?? 0
    // A level in a quote-feed stock's own currency (notify only) compares its local price, as the API does.
    const ccy = r.currency && r.currency !== 'USD' ? r.currency : null
    if (ccy) {
      const feed = tradable.find((a) => a.symbol === r.symbol)?.quoteFeed
      if (!feed) throw new Fail(400, 'bad_request', `${r.symbol} alerts are set in USD.`)
      if (feed.currency !== ccy) throw new Fail(400, 'bad_request', `${r.symbol} alerts are set in USD or ${feed.currency}.`)
      if (action !== 'notify') throw new Fail(400, 'bad_request', `A buy or sell alert is set in USD; only a notify alert takes ${ccy}.`)
      px = feed.localPrice
    }
    const money = (n: number) => (ccy ? `${ccy} ${n.toFixed(2)}` : `$${n.toFixed(2)}`)
    if (r.op === 'above' ? !(price > px) : !(price < px))
      throw new Fail(400, 'bad_request', `${r.symbol} is already ${r.op} ${money(price)} (now ${money(px)}).`)
    if (action !== 'notify' && !((r.notional ?? 0) >= 10)) throw new Fail(400, 'bad_request', 'notional must be at least $10')
    if (r.proposalId && myAlerts(s).some((a) => a.proposalId === r.proposalId)) throw new Fail(409, 'conflict', 'This alert was already set.')
    validateAlertReceipt(s, r)
    const alert: Alert = {
      id: randomUUID(),
      symbol: r.symbol ?? '',
      ...priceTrigger,
      op: r.op ?? null,
      price,
      action,
      notional: r.notional ?? null,
      stopLoss: r.stopLoss ?? null,
      takeProfit: r.takeProfit ?? null,
      note: (r.note ?? '').trim(),
      source: r.source ?? 'manual',
      proposalId: r.proposalId ?? null,
      status: 'active',
      tradeId: null,
      error: null,
      firedPrice: null,
      createdAt: now(),
      firedAt: null,
      currency: ccy,
    }
    myAlerts(s).unshift(alert)
    return { alert }
  },
  201,
)
on('DELETE', '/api/alerts/:id', ({ s, id }) => {
  const a = find(new Map(myAlerts(s).map((x) => [x.id, x])), id, 'alert')
  if (a.status !== 'active' && a.status !== 'paused') throw new Fail(409, 'conflict', 'This alert already fired.')
  a.status = 'cancelled'
}, 204)
on('GET', '/api/notifications', ({ s, q }): Res<'notifications'> => {
  const limit = Number(q.get('limit') ?? 30)
  if (!Number.isInteger(limit) || limit < 1 || limit > 50) throw new Fail(400, 'bad_request', 'limit must be 1..50')
  const events = [...workflowNotifications(s), ...liveState(s).events, ...fx.notifications.map((n, i) => ({ ...n, at: hoursAgo(i + 1) }))]
    .sort((a, b) => b.at.localeCompare(a.at))
  return { items: events.slice(0, limit), unread: bellSeen.has(s) ? 0 : Math.min(99, events.length) }
})
on('POST', '/api/notifications/seen', ({ s }) => void bellSeen.add(s), 204)

// Notification channels: email "available" like a server with SES; Lark accepts a custom-bot URL and never posts.
type Channels = Res<'notificationChannels'>['channels']
const channelState = new WeakMap<State, Channels>()
const channelsOf = (s: State): Channels => {
  let c = channelState.get(s)
  if (!c) channelState.set(s, (c = { email: { enabled: false, address: fx.user.email, available: true }, lark: { enabled: false, webhook: null, signed: false } }))
  return c
}
on('GET', '/api/notifications/channels', ({ s }): Res<'notificationChannels'> => ({ channels: channelsOf(s) }))
on('PUT', '/api/notifications/channels', ({ s, body }): Res<'updateNotificationChannels'> => {
  const b = body as UpdateNotificationChannels
  const c = structuredClone(channelsOf(s))
  if (b.larkWebhook !== undefined) {
    if (b.larkWebhook && !/^https:\/\/open\.(larksuite\.com|feishu\.cn)\/open-apis\/bot\/v2\/hook\/[^/?#\s]+$/.test(b.larkWebhook))
      throw new Fail(400, 'bad_request', 'Paste the webhook URL of a Lark custom bot (https://open.larksuite.com/open-apis/bot/v2/hook/…).')
    c.lark.webhook = b.larkWebhook ? b.larkWebhook.replace(/hook\/.*?(.{0,4})$/, 'hook/…$1') : null
  }
  if (b.larkSecret !== undefined) c.lark.signed = !!b.larkSecret
  if (b.email !== undefined) c.email.enabled = b.email
  if (b.lark !== undefined) c.lark.enabled = b.lark
  if (c.lark.enabled && !c.lark.webhook) throw new Fail(400, 'bad_request', "Add your Lark bot's webhook URL to turn Lark on.")
  channelState.set(s, c)
  return { channels: c }
})
on('POST', '/api/notifications/channels/test', ({ s }): Res<'testNotificationChannels'> => {
  const c = channelsOf(s)
  return { email: c.email.enabled ? 'sent' : 'off', lark: c.lark.enabled ? 'sent' : 'off' }
})

// ── All-time-high drawdown triggers. The mock high is a fixed 1.6 × the fixture price on Jan 19, 2025 (test
// data, labelled like the API's sources); the mock never fires a trigger. A quote feed's high and price are its
// listing's, in the listing's currency (basis "listing"). HSDT's high is 10,000,000 × its price, standing in for the
// reverse-split case: a Yahoo high over 100× the price is refused with 400, as the API does. ── begin
function highOf(symbol: string): { high: AllTimeHigh; price: number; fromAthPct: number } {
  const a = tradable.find((x) => x.symbol === symbol)
  if (!a) throw new Fail(400, 'bad_request', `unknown symbol "${symbol}"`)
  const crypto = a.class === 'crypto'
  const feed = a.quoteFeed ?? null
  const yahoo = !!feed || (!crypto && a.localCurrency === 'USD' && symbol !== 'XYZ100')
  const from = crypto ? '2020-08-11' : yahoo ? '1999-01-22' : '2026-02-20'
  const ticker = crypto ? a.pair : feed ? feed.symbol : yahoo ? symbol : `xyz:${symbol}`
  const currency = feed?.currency ?? 'USD'
  const sourceLabel = crypto
    ? `Binance ${ticker} daily highs since ${from}`
    : yahoo
      ? `Yahoo Finance ${ticker} lifetime daily highs since ${from} (split-adjusted)${currency === 'USD' ? '' : `, in ${currency}`}`
      : `perp history since ${from}, not the listing's lifetime high`
  const price = feed ? feed.localPrice : a.price
  const ath = round2(price * (symbol === 'HSDT' ? 1e7 : 1.6))
  if (yahoo && ath > 100 * price)
    throw new Fail(400, 'bad_request', `${symbol} has no usable all-time high: Yahoo's split-adjusted lifetime high for ${ticker} is over 100× the price, which reverse splits produce, so a drawdown alert can't measure from it.`)
  return {
    high: { ath, athAt: '2025-01-19T00:00:00Z', athLive: false, coveredFrom: `${from}T00:00:00Z`, source: crypto ? 'binance' : yahoo ? 'yahoo' : 'perp',
      sourceLabel, ticker, currency, basis: feed ? 'listing' : 'mark' },
    price,
    fromAthPct: round2((price / ath - 1) * 100),
  }
}
const drawdownLevel = (ath: number, pct: number) => (ath * (10000 - Math.round(pct * 100))) / 10000 // as the API rounds it

on('GET', '/api/alerts/ath', ({ q }): Res<'alertATH'> => {
  const symbol = (q.get('symbol') ?? '').trim().toUpperCase()
  const { high, price, fromAthPct } = highOf(symbol)
  return { ath: { symbol, ...high, price, fromAthPct } }
})

function createDrawdown(s: State, r: AlertRequest): Res<'createAlert'> {
  const symbol = (r.symbol ?? '').trim().toUpperCase()
  if (!tradable.some((x) => x.symbol === symbol)) throw new Fail(400, 'bad_request', `unknown symbol "${symbol}"`)
  const action = r.action ?? 'notify'
  const bps = Math.round((r.drawdownPct ?? NaN) * 100)
  if (r.op != null || r.price != null || r.every != null || r.at != null || r.weekday != null || r.notional != null || r.stopLoss != null || r.takeProfit != null)
    throw new Fail(400, 'bad_request', 'A drawdown alert takes no op, price, schedule, notional, stopLoss or takeProfit.')
  if (!(bps >= 1 && bps <= 9999)) throw new Fail(400, 'bad_request', 'drawdownPct must be 0.01–99.99 (% below the all-time high).')
  if (action !== 'notify' && action !== 'sell') throw new Fail(400, 'bad_request', 'A drawdown alert notifies or sells: action must be notify or sell.')
  if (action === 'notify' && r.sellPct != null) throw new Fail(400, 'bad_request', 'sellPct is for action sell only.')
  if (r.sellPct != null && !(Number.isInteger(r.sellPct) && r.sellPct >= 1 && r.sellPct <= 100))
    throw new Fail(400, 'bad_request', 'sellPct must be a whole number from 1 to 100 (% of the paper long).')
  validateAlertReceipt(s, r)
  const { high, price, fromAthPct } = highOf(symbol) // after the rules, as the API resolves the high last
  const alert: Alert = {
    id: randomUUID(),
    symbol,
    ...priceTrigger,
    trigger: 'drawdown',
    op: null,
    price: null,
    action,
    notional: null,
    stopLoss: null,
    takeProfit: null,
    note: (r.note ?? '').trim(),
    source: r.source ?? 'manual',
    proposalId: r.proposalId ?? null,
    status: 'active',
    tradeId: null,
    error: null,
    firedPrice: null,
    createdAt: now(),
    firedAt: null,
    drawdown: { ...high, pct: bps / 100, sellPct: action === 'sell' ? r.sellPct ?? 100 : null, threshold: drawdownLevel(high.ath, bps / 100),
      outcome: null, price, fromAthPct },
  }
  myAlerts(s).unshift(alert)
  return { alert }
}

/** "Tell me if SOL falls 30% from its all-time high" (add "sell" to sell the long): the agent's set_drawdown_alert card. */
function drawdownTurn(chatId: string, title: string, s: State, message: string): ChatStreamEvent[] | null {
  const m = /(\d+(?:\.\d+)?)\s*%\s+(?:from|below|off)\s+(?:its|the)\s+(?:all-time\s+)?high/i.exec(message)
  if (!m) return null
  const words = message.toUpperCase().split(/[^A-Z0-9]+/)
  const symbol = fx.universeAssets.find((a) => words.includes(a.symbol))?.symbol ?? 'SOL'
  const pct = Number(m[1])
  const sell = /\bsell\b/i.test(message)
  const call: ChatStreamEvent = { event: 'tool_call', data: { id: 'dd1', name: 'set_drawdown_alert', label: `Drafting drawdown alert: ${symbol} ${pct}% below its high`, input: { symbol, drawdown_pct: pct } } }
  let resolved: ReturnType<typeof highOf>
  try {
    resolved = highOf(symbol)
  } catch (err) {
    // The API's tool refuses an unusable high (HSDT's stand-in) and the answer goes on: a failed chip, no card.
    if (!(err instanceof Fail)) throw err
    return [
      { event: 'chat', data: { chatId, title } },
      call,
      { event: 'tool_result', data: { id: 'dd1', name: 'set_drawdown_alert', ok: false, summary: err.message.slice(0, 140) } },
      { event: 'delta', data: { text: `I can't set that alert: ${err.message}` } },
      { event: 'done', data: { messageId: randomUUID(), credits: s.credits } },
    ]
  }
  const { high, price, fromAthPct } = resolved
  const threshold = drawdownLevel(high.ath, pct)
  const data: PriceAlertProposal = {
    proposalId: randomUUID(), symbol, trigger: 'drawdown', op: null, price: null, markPrice: price,
    distancePct: round2(((threshold - price) / price) * 100), action: sell ? 'sell' : 'notify', notional: null, stopLoss: null, takeProfit: null,
    note: 'A one-shot alert at your drawdown from the all-time high.', drawdownPct: pct, ...(sell ? { sellPct: 100 } : {}), ath: high, threshold, fromAthPct,
  }
  return [
    { event: 'chat', data: { chatId, title } },
    call,
    { event: 'tool_result', data: { id: 'dd1', name: 'set_drawdown_alert', ok: true, summary: `${symbol} ${pct}% below ATH ${money(high.ath)}` } },
    { event: 'delta', data: { text: 'Here is the drawdown alert. Nothing is set until you tap **Set alert** (paper only).' } },
    { event: 'card', data: { type: 'price_alert', data } },
    { event: 'done', data: { messageId: randomUUID(), credits: s.credits } },
  ]
}
// ── Drawdown triggers ── end

// ── Workflows: only authenticated test controls advance slots; there are no jobs or models. ──
const priceTrigger = { trigger: 'price', every: null, at: null, weekday: null, nextRunAt: null, lastRun: null,
  prompt: null, thesis: null, notifyWhen: null, quotaStreak: 0 } as const

/** The first instant strictly after now at `at` (HH:MM UTC), every day or on `weekday` (0 = Sunday). */
function firstRun(at: string, every: AlertEvery = 'day', weekday?: number, clock = Date.now()) {
  const [h, m] = at.split(':').map(Number)
  const t = new Date(clock)
  t.setUTCHours(h, m, 0, 0)
  if (weekday != null) t.setUTCDate(t.getUTCDate() + ((weekday - t.getUTCDay() + 7) % 7))
  if (t.getTime() <= clock) t.setUTCDate(t.getUTCDate() + (weekday != null ? 7 : 1))
  if (every === 'weekdays') while ([0, 6].includes(t.getUTCDay())) t.setUTCDate(t.getUTCDate() + 1)
  return t.toISOString()
}

function createSchedule(s: State, r: AlertRequest): Res<'createAlert'> {
  const action = r.action ?? 'notify'
  const symbol = (r.symbol ?? '').trim().toUpperCase()
  if (symbol || action !== 'research') mark(symbol)
  const week = r.every === 'week'
  if (r.op != null || r.price != null) throw new Fail(400, 'bad_request', 'A scheduled automation takes no op or price.')
  if (!['day', 'weekdays', 'week'].includes(r.every ?? '')) throw new Fail(400, 'bad_request', 'every must be day, weekdays or week.')
  if (!/^([01]\d|2[0-3]):[0-5]\d$/.test(r.at ?? '')) throw new Fail(400, 'bad_request', 'at must be HH:MM (UTC).')
  if (week !== (r.weekday != null) || (r.weekday != null && (!Number.isInteger(r.weekday) || !(r.weekday >= 0 && r.weekday <= 6))))
    throw new Fail(400, 'bad_request', 'weekday (0–6, 0 = Sunday) is required for a weekly schedule and only then.')
  if (!['notify', 'buy', 'sell', 'research'].includes(action)) throw new Fail(400, 'bad_request', 'Unknown schedule action.')
  if (action === 'research') {
    if (typeof r.prompt !== 'string' || [...r.prompt.trim()].length < 10 || [...r.prompt.trim()].length > 500) throw new Fail(400, 'bad_request', 'Research prompt must contain 10 to 500 characters.')
    if (r.notional != null || r.stopLoss != null || r.takeProfit != null) throw new Fail(400, 'bad_request', 'Research takes no notional, stopLoss or takeProfit.')
    if (r.thesis != null && (typeof r.thesis !== 'object' || typeof r.thesis.statement !== 'string' || typeof r.thesis.invalidation !== 'string' || [...r.thesis.statement].length > 300 || [...r.thesis.invalidation].length > 300)) throw new Fail(400, 'bad_request', 'Thesis and invalidation must each contain at most 300 characters.')
    if (r.notifyWhen != null && !['always', 'changed', 'invalidated'].includes(r.notifyWhen)) throw new Fail(400, 'bad_request', 'notifyWhen must be always, changed or invalidated.')
  } else {
    if (r.prompt != null || r.thesis != null || r.notifyWhen != null) throw new Fail(400, 'bad_request', 'Research fields require action research.')
    if (action !== 'notify' && !((r.notional ?? 0) >= 10)) throw new Fail(400, 'bad_request', 'notional must be at least $10')
    if (r.stopLoss != null || r.takeProfit != null) throw new Fail(400, 'bad_request', 'A scheduled order takes no stopLoss or takeProfit.')
    if (action === 'notify' && r.notional != null) throw new Fail(400, 'bad_request', 'A notify alert takes no notional, stopLoss or takeProfit.')
  }
  validateAlertReceipt(s, r)
  if (action === 'research') researchCapacity(s)
  const alert: Alert = {
    id: randomUUID(),
    symbol,
    prompt: action === 'research' ? r.prompt!.trim() : null,
    thesis: action === 'research' ? r.thesis ?? null : null,
    notifyWhen: action === 'research' ? r.notifyWhen ?? 'always' : null,
    quotaStreak: 0,
    trigger: 'schedule',
    op: null,
    price: null,
    every: r.every!,
    at: r.at!,
    weekday: r.weekday ?? null,
    nextRunAt: firstRun(r.at!, r.every!, r.weekday),
    lastRun: null,
    action,
    notional: r.notional == null ? null : round2(r.notional),
    stopLoss: null,
    takeProfit: null,
    note: (r.note ?? '').trim(),
    source: r.source ?? 'manual',
    proposalId: r.proposalId ?? null,
    status: 'active',
    tradeId: null,
    error: null,
    firedPrice: null,
    createdAt: now(),
    firedAt: null,
  }
  myAlerts(s).unshift(alert)
  return { alert }
}

function validateAlertReceipt(s: State, r: AlertRequest) {
  if (r.proposalId != null && !uuid(r.proposalId)) throw new Fail(400, 'bad_request', 'proposalId must be a canonical UUID.')
  if (r.proposalId && myAlerts(s).some(a => a.proposalId === r.proposalId)) throw new Fail(409, 'conflict', 'This alert was already set.')
  if ([...(r.note ?? '')].length > 200) throw new Fail(400, 'bad_request', 'note must contain at most 200 characters.')
  if (r.source != null && !['manual', 'ticket', 'agent'].includes(r.source)) throw new Fail(400, 'bad_request', 'Unknown alert source.')
  if (myAlerts(s).filter(a => a.status === 'active').length >= 20) throw new Fail(409, 'conflict', 'You have 20 active alerts. Cancel one first.')
}
function researchCapacity(s: State) {
  if (myAlerts(s).filter(a => a.status === 'active' && a.action === 'research').length >= 3) throw new Fail(409, 'conflict', 'You have 3 active research schedules. Cancel one first.')
}

type WorkflowRow = { run: AlertRun; answer: string; eligible: boolean }
type WorkflowState = { rows: Map<string, WorkflowRow[]>; reportChats: Map<string, string>; enabled: boolean }
const workflows = new WeakMap<State, WorkflowState>()
const workflowState = (s: State) => workflows.get(s) ?? (workflows.set(s, { rows: new Map(), reportChats: new Map(), enabled: true }), workflows.get(s)!)
const workflowRows = (s: State, id: string) => workflowState(s).rows.get(id) ?? (workflowState(s).rows.set(id, []), workflowState(s).rows.get(id)!)
const ownAlert = (s: State, id: string) => find(new Map(myAlerts(s).map(a => [a.id, a])), id, 'alert')
const clipResearch = (text: string, size: number) => [...text].length > size ? [...text].slice(0, size - 1).join('') + '…' : text
const availableRun = (s: State, run: AlertRun): AlertRun => ({ ...run, reportChatId: run.reportChatId && s.chats.has(run.reportChatId) ? run.reportChatId : null })
const normalizedPrefix = (text: string) => [...text.trim().replace(/\s+/gu, ' ').toLowerCase()].slice(0, 200).join('')
const parsedVerdict = (answer: string): AlertRun['verdict'] => /(?:^|\n)Verdict: (holds|weakened|invalidated) — \S[^\n]*\s*$/u.exec(answer)?.[1] as AlertRun['verdict'] ?? 'unknown'

on('POST', '/api/alerts/:id/resume', ({ s, id }): Res<'resumeAlert'> => {
  const alert = ownAlert(s, id)
  if (alert.action !== 'research' || alert.status !== 'paused') throw new Fail(409, 'conflict', 'Only a paused research schedule can be resumed.')
  if (!workflowState(s).enabled) throw new Fail(403, 'forbidden', 'Account is disabled.')
  validateAlertReceipt(s, {})
  researchCapacity(s)
  if (s.credits.limit - s.credits.used < 2) throw new Fail(429, 'quota_exceeded', 'Out of AI credits today')
  Object.assign(alert, { status: 'active', quotaStreak: 0, error: null, nextRunAt: firstRun(alert.at!, alert.every!, alert.weekday ?? undefined) })
  return { alert: { ...alert, lastRun: alert.lastRun ? availableRun(s, alert.lastRun) : null } }
})

on('GET', '/api/alerts/:id/runs', ({ s, id, q }): Res<'alertRuns'> => {
  ownAlert(s, id)
  const limit = Number(q.get('limit') ?? 20)
  if (!Number.isInteger(limit) || limit < 1 || limit > 50) throw new Fail(400, 'bad_request', 'limit must be 1..50')
  let before: [string, string] | null = null
  if (q.has('before')) {
    try {
      const encoded = q.get('before')!
      const cursor = JSON.parse(Buffer.from(encoded, 'base64url').toString())
      if (!encoded || Buffer.from(JSON.stringify(cursor)).toString('base64url') !== encoded || !Array.isArray(cursor) || cursor.length !== 4 || cursor[0] !== s.ownerId || cursor[1] !== id || typeof cursor[2] !== 'string' || !Number.isFinite(Date.parse(cursor[2])) || !uuid(cursor[3])) throw new Error('cursor')
      before = [cursor[2], cursor[3]]
    } catch { throw new Fail(400, 'bad_request', 'Invalid run history cursor.') }
  }
  const ordered = [...workflowRows(s, id)].sort((a, b) => b.run.at.localeCompare(a.run.at) || b.run.id.localeCompare(a.run.id))
    .filter(({ run }) => Date.parse(run.at) >= Date.now() - 30 * 86_400_000 && (!before || run.at < before[0] || (run.at === before[0] && run.id < before[1])))
  const rows = ordered.slice(0, limit), last = rows.at(-1)?.run
  return { runs: rows.map(row => availableRun(s, row.run)), nextCursor: ordered.length > limit && last ? Buffer.from(JSON.stringify([s.ownerId, id, last.at, last.id])).toString('base64url') : null }
})

function workflowNotifications(s: State): Notification[] {
  return myAlerts(s).flatMap(alert => workflowRows(s, alert.id).flatMap<Notification>(({ run, eligible }) => {
    if (!eligible || Date.parse(run.at) < Date.now() - 14 * 86_400_000) return []
    const report = availableRun(s, run).reportChatId
    if (alert.action === 'research') return report ? [{ id: `workflow_research:${run.id}`, kind: 'workflow_research' as const, title: `Scheduled research · ${alert.note || alert.prompt}`, body: run.excerpt ?? '', symbol: alert.symbol || null, link: `/chat/${report}`, at: run.at }] : []
    if (alert.action === 'buy' || alert.action === 'sell') {
      const failed = run.status === 'failed', kind = failed ? 'workflow_failed' : 'workflow_order'
      return [{ id: `${kind}:${run.id}`, kind, title: failed ? `Scheduled order not filled · ${alert.symbol}` : `Scheduled ${alert.action} filled · ${alert.symbol}`, body: run.error ?? run.body, symbol: alert.symbol, link: '/automations', at: run.at }]
    }
    const period = alert.every === 'weekdays' ? 'Weekday' : alert.every === 'week' ? 'Weekly' : 'Daily'
    return [{ id: `workflow:${run.id}`, kind: 'workflow' as const, title: `${period} brief · ${alert.symbol}`, body: run.body, symbol: alert.symbol, link: '/automations', at: run.at }]
  }))
}

/** Controlled scheduler stand-in: claim a due slot once, skip backlog, then publish a supplied report. */
export function advanceResearchSlot(s: State, id: string, input: { at?: string; answer?: string; error?: string; produced?: boolean; status?: AlertRun['status'] } = {}) {
  const alert = ownAlert(s, id), state = workflowState(s)
  const clock = Date.parse(input.at ?? alert.nextRunAt ?? now())
  if (!Number.isFinite(clock)) throw new Fail(400, 'bad_request', 'Invalid fixture time.')
  if (!state.enabled || alert.status !== 'active' || alert.trigger !== 'schedule' || !alert.nextRunAt || Date.parse(alert.nextRunAt) > clock || alert.lastRun?.status === 'running') return { alert: { ...alert, lastRun: alert.lastRun ? availableRun(s, alert.lastRun) : null } }
  const rows = workflowRows(s, id), at = new Date(clock).toISOString()
  alert.nextRunAt = firstRun(alert.at!, alert.every!, alert.weekday ?? undefined, clock)
  alert.firedAt = at
  const run: AlertRun = { id: randomUUID(), at, status: input.status ?? 'ok', body: '', error: null, reportChatId: null, excerpt: null, verdict: null }
  let eligible = false, answer = ''
  if (alert.action === 'research') {
    if (s.credits.limit - s.credits.used < 2) {
      alert.quotaStreak++
      if (alert.quotaStreak >= 3) alert.status = 'paused'
      run.status = 'failed'; run.error = 'Out of AI credits today'
    } else {
      spend(s, 2); alert.quotaStreak = 0
      if (input.error || ['failed', 'interrupted', 'cancelled'].includes(run.status)) {
        run.status = input.status ?? 'failed'; run.error = input.error ?? 'Research run interrupted'
        if (input.produced === false) s.credits.used -= 2
      } else if (run.status !== 'running') {
        answer = clipResearch(input.answer ?? '**Current evidence** [1].\n\nVerdict: holds — demand persists', 16000)
        run.body = clipResearch(answer, 300); run.excerpt = clipResearch(answer, 160)
        run.verdict = alert.thesis ? parsedVerdict(answer) : null
        const prior = rows.find(row => row.run.status === 'ok' && row.run.verdict !== 'unknown')
        const hadSuccess = rows.some(row => row.run.status === 'ok')
        eligible = alert.notifyWhen === 'always' || (alert.notifyWhen === 'changed' && (!hadSuccess || (prior ? run.verdict !== prior.run.verdict || normalizedPrefix(answer) !== normalizedPrefix(prior.answer) : run.verdict !== 'unknown'))) || (alert.notifyWhen === 'invalidated' && run.verdict === 'invalidated' && prior?.run.verdict !== 'invalidated')
        let chatId = state.reportChats.get(id)
        if (!chatId || !s.chats.has(chatId)) {
          chatId = randomUUID(); state.reportChats.set(id, chatId)
          s.chats.set(chatId, { id: chatId, title: `Workflow · ${clipResearch(alert.note || alert.prompt!, 80)}`, messages: [], updatedAt: at })
        }
        const chat = s.chats.get(chatId)!
        chat.messages.push({ id: randomUUID(), role: 'assistant', content: answer, cards: [{ type: 'sources', data: { items: SOURCES.map(source => ({ ...source })) } }], attachments: [], createdAt: at })
        chat.updatedAt = at; run.reportChatId = chatId
      }
    }
  } else if (alert.action === 'notify') {
    run.body = `${alert.symbol} $${mark(alert.symbol).toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 })} · Top headline: Bitcoin ETFs log a third day of inflows`
    eligible = true
  } else {
    try {
      const trade = placeOrder(s, { symbol: alert.symbol, side: alert.action, notional: alert.notional!, source: 'alert', note: `Workflow: ${alert.action} $${alert.notional} ${alert.symbol} ${alert.every} ${alert.at} UTC` })
      trade.ref = { kind: 'alert', id, label: alert.note }; run.body = `${alert.action === 'buy' ? 'Bought' : 'Sold'} $${alert.notional} ${alert.symbol} at $${trade.price.toFixed(2)}`
    } catch (error) { run.status = 'failed'; run.error = error instanceof Error ? error.message : String(error) }
    eligible = true
  }
  rows.unshift({ run, answer, eligible }); alert.lastRun = run
  return { alert: { ...alert, lastRun: availableRun(s, run) } }
}

on('POST', '/__e2e/research-step', ({ s, body }) => advanceResearchSlot(s, body.id, body))
on('POST', '/__e2e/research-fixture', ({ s, body }) => {
  if (body.scenario === 'credits') {
    if (!Number.isInteger(body.remaining ?? 0) || (body.remaining ?? 0) < 0) throw new Fail(400, 'bad_request', 'Invalid fixture credits.')
    s.credits.limit = s.credits.used + (body.remaining ?? 0); return { credits: s.credits }
  }
  if (body.scenario === 'enabled') { workflowState(s).enabled = !!body.enabled; return { enabled: workflowState(s).enabled } }
  if (body.scenario === 'capacity' || body.scenario === 'shared-capacity') {
    const count = body.scenario === 'capacity' ? 3 : 20
    for (let i = 0; i < count; i++) createSchedule(s, { trigger: 'schedule', symbol: 'BTC', every: 'day', at: '08:00', ...(body.scenario === 'capacity' ? { action: 'research', prompt: 'Review market catalysts with current sources.' } : {}) })
    return { alerts: myAlerts(s) }
  }
  if (body.scenario !== 'history' && body.scenario !== 'paused' && body.scenario !== 'working') throw new Fail(400, 'bad_request', 'Unknown Research fixture.')
  const alert = body.scenario === 'paused'
    ? fx.researchSchedule({ id: randomUUID(), status: 'paused', createdAt: now(), nextRunAt: firstRun('08:00', 'weekdays') })
    : createSchedule(s, { trigger: 'schedule', every: 'weekdays', at: '08:00', action: 'research', prompt: 'Review BTC demand and current market catalysts with sources.', symbol: 'BTC', thesis: { statement: 'BTC demand supports the uptrend.', invalidation: 'ETF demand reverses for a week.' }, note: 'Research history' }).alert
  if (body.scenario === 'paused') {
    myAlerts(s).unshift(alert)
    alert.status = 'paused'; alert.quotaStreak = 3
    alert.lastRun = fx.researchRun({ id: randomUUID(), at: hoursAgo(1), status: 'failed', body: '', error: 'Out of AI credits today', reportChatId: null, excerpt: null, verdict: null })
    workflowRows(s, alert.id).push({ run: alert.lastRun, answer: '', eligible: false })
  } else if (body.scenario === 'working') {
    alert.lastRun = fx.researchRun({ id: randomUUID(), at: now(), status: 'running', body: '', reportChatId: null, excerpt: null, verdict: null })
    workflowRows(s, alert.id).push({ run: alert.lastRun, answer: '', eligible: false })
  } else {
    for (let i = 25; i >= 0; i--) {
      alert.nextRunAt = i === 25 ? hoursAgo(31 * 24) : hoursAgo(i + 1)
      advanceResearchSlot(s, alert.id, { at: alert.nextRunAt })
    }
    alert.nextRunAt = firstRun(alert.at!, alert.every!)
  }
  return { alert }
})

function researchProposalTurn(chatId: string, title: string, s: State, message: string): ChatStreamEvent[] | null {
  const thesis = /^track.*thesis/i.test(message)
  if (!thesis && !/^schedule.*research|^research schedule/i.test(message)) return null
  const symbol = thesis ? 'BTC' : ''
  const data = fx.researchCard({ proposalId: randomUUID(), symbol,
    ...(thesis ? { prompt: 'Review whether BTC demand supports the uptrend using current evidence.', thesis: { statement: 'BTC demand supports the uptrend.', invalidation: 'ETF demand reverses for a week.' }, note: 'BTC thesis tracker' } : {}) })
  const name = thesis ? 'track_thesis' : 'set_schedule'
  return [
    { event: 'chat', data: { chatId, title } },
    { event: 'tool_call', data: { id: 'research-proposal', name, label: 'Preparing recurring research', input: { symbol, action: 'research' } } },
    { event: 'tool_result', data: { id: 'research-proposal', name, ok: true, summary: 'Card shown. Nothing is set until Set automation.' } },
    { event: 'delta', data: { text: 'Review this research schedule. It uses 2 AI credits per run. Tap **Set automation** to confirm.' } },
    { event: 'card', data: { type: 'price_alert', data } },
    { event: 'card', data: { type: 'followups', data: { prompts: ['Review my automations', 'Research BTC demand'] } } },
    { event: 'done', data: { messageId: randomUUID(), credits: s.credits } },
  ]
}
// ── Workflows ── end

// ── Showcase desk: forecast-driven holdings, a Copilot plan with a limit entry ── begin
/** Puts a wrapper in front of an earlier route (the first match wins): it runs the original, then edits its body. */
function around<K extends keyof Api>(method: string, path: string, edit: (out: Res<K>, c: Ctx) => Res<K>) {
  const base = routes.find((r) => r.method === method && r.re.test(path))!
  routes.unshift({ ...base, handler: async (c) => edit((await base.handler(c)) as Res<K>, c) })
}
/** A built basket says what drives each holding: the seeded forecast (Yes 62%) for BTC, the brief's sentiment for ETH, else the stats. */
around<'buildPortfolio'>('POST', '/api/ai-portfolios/build', (out) => {
  const drivers: Record<string, AIPortfolio['holdings'][number]['driver']> = {
    BTC: { kind: 'forecast', id: SEED.forecast, label: 'Forecast · Yes 62%' },
    ETH: { kind: 'sentiment', id: null, label: 'News sentiment · bullish' },
  }
  // The stored basket is the same object, so list and detail carry the drivers too.
  out.portfolio.holdings = out.portfolio.holdings.map((h) => ({ ...h, driver: drivers[h.symbol] ?? { kind: 'stats', id: null, label: 'Daily history stats' } }))
  return out
})
/** A swing long waits for a pullback: its entry is 1.5% under the price, so "Place limit" pre-fills a limit order. */
around<'copilotPlan'>('POST', '/api/copilot/plan', (out, { body }) => {
  const { style, bias } = body as CopilotPlanRequest
  if (style !== 'swing' || bias !== 'long') return out
  const entry = round2(out.analysis.price * 0.985)
  return { ...out, plan: { ...out.plan, entry, stopLoss: round2(entry * 0.984), takeProfit: round2(entry * 1.032) } }
})
// ── Showcase desk ── end

// ── Trade and autopilot ── begin
/** Deploy takes stop / target / trailing overrides of the spec, refuses an autopilot without a stop and a target, and echoes the levels. */
{
  const base = routes.find((r) => r.method === 'POST' && r.re.test('/api/autopilots'))!
  routes.unshift({
    ...base,
    handler: async (c) => {
      const b = c.body ?? {}
      for (const [k, lo] of [['stopLossPct', 0.1], ['takeProfitPct', 0.1], ['trailingStopPct', 0.5]] as const)
        if (b[k] != null && !(b[k] >= lo && b[k] <= 50)) throw new Fail(400, 'bad_request', `${k} must be ${lo} to 50.`)
      const spec = (c.s.strategies.get(b.strategyId) ?? labStrategies().find((x) => x.id === b.strategyId))?.spec
      const levels = {
        stopLossPct: b.stopLossPct ?? spec?.stopLossPct ?? null,
        takeProfitPct: b.takeProfitPct ?? spec?.takeProfitPct ?? null,
        trailingStopPct: b.trailingStopPct ?? spec?.trailingStopPct ?? null,
      }
      if (spec && (!(levels.stopLossPct || levels.trailingStopPct) || !levels.takeProfitPct))
        throw new Fail(400, 'bad_request', 'Autopilot entries always carry a stop and a target: set stopLossPct (or trailingStopPct) and takeProfitPct.')
      const out = (await base.handler(c)) as Res<'deployAutopilot'>
      Object.assign(pilotsOf(c.s)[0], levels) // the stored one, so list and detail echo them too
      return { ...out, autopilot: { ...out.autopilot, ...levels } }
    },
  })
}
// ── Trade and autopilot ── end

// ── Live run progress ── begin
/** A categorical run frozen mid-pipeline: research done, one specialist in, one thinking, one dropped. Never changes, so polls cannot race. */
export const RUNNING_FORECAST = 'f-running'
on('GET', `/api/forecasts/${RUNNING_FORECAST}`, (): Res<'forecast'> => ({
  forecast: {
    ...fx.forecast({ id: RUNNING_FORECAST, shape: 'categorical', question: 'Which company will be the largest by market cap on 2026-12-31?',
      outcomes: ['Nvidia', 'Apple', 'Microsoft'], status: 'running', result: null, completedAt: null, createdAt: hoursAgo(0) }),
    parentId: null,
    history: [],
    progress: {
      research: { status: 'done', queries: ['Nvidia Apple Microsoft market cap', 'largest company by market cap 2026'], found: 8, read: 3, mode: 'live' },
      specialists: [
        { role: 'base_rate', status: 'done', top: { outcome: 'Nvidia', p: 0.41 } },
        { role: 'quant', status: 'running', top: null },
        { role: 'skeptic', status: 'failed', top: null },
      ],
      aggregator: { status: 'pending' },
      market: { status: 'done', matched: true },
      updatedAt: hoursAgo(0),
    },
  },
}))
routes.unshift(routes.pop()!) // ahead of the generic GET /api/forecasts/:id
// ── Live run progress ── end

// ── Prediction markets ── begin
type MarketPosition = import('../src/lib/types').MarketPosition
/** A completed binary report matched to Polymarket (Yes 55¢ against the desk's 62%), added to a session on first use. */
export const MARKET_FORECAST = 'f-market'
export const MARKET_QUESTION = 'Fed decreases interest rates after the December 2026 meeting?'
const marketForecasts = (s: State, id: string) => {
  if (id === MARKET_FORECAST && !s.forecasts.has(MARKET_FORECAST))
    s.forecasts.set(MARKET_FORECAST, fx.forecast({
      id: MARKET_FORECAST, question: 'Will the Fed cut rates at its December 2026 meeting?', createdAt: hoursAgo(2), completedAt: hoursAgo(2),
      result: { ...fx.forecastResult, market: { ...fx.marketOdds, question: MARKET_QUESTION, url: 'https://polymarket.com/event/fed-decision-in-december', asOf: now() } },
    }))
  return s.forecasts
}
const stakesOf = new WeakMap<State, MarketPosition[]>() // newest first
const stakes = (s: State) => stakesOf.get(s) ?? (stakesOf.set(s, []), stakesOf.get(s)!)
const openStakes = (s: State) => stakes(s).reduce((a, p) => a + (p.status === 'open' ? p.cost : 0), 0)
{
  const base = routes.find((r) => r.method === 'GET' && r.re.test('/api/forecasts/x'))!
  routes.unshift({ ...base, re: new RegExp(`^/api/forecasts/(${MARKET_FORECAST})$`), handler: (c) => (marketForecasts(c.s, c.id), base.handler(c)) })
}
/** Fresh odds: the mock's never move, so a re-price only stamps asOf; a search finds nothing and stamps marketCheckedAt. */
on('POST', '/api/forecasts/:id/market', (c): Res<'refreshMarket'> | Promise<Res<'refreshMarket'>> => {
  const { s, id, body } = c
  const f = marketForecasts(s, id).get(id)
  // A fixed fixture (the wire report, a replay) is served by its own GET route: its odds stay as they are.
  if (!f) return routes.find((r) => r.method === 'GET' && r.re.test(`/api/forecasts/${id}`))!.handler(c) as Promise<Res<'refreshMarket'>>
  const r = f.result
  if (f.status !== 'completed' || !r || f.shape === 'ordered') throw new Fail(409, 'conflict', 'Only a completed binary or multiple-choice report has market odds.')
  if (r.market && !body.rematch) f.result = { ...r, market: { ...r.market, asOf: now() } }
  else if (!(r.marketCheckedAt && Date.now() - Date.parse(r.marketCheckedAt) < 600_000)) f.result = { ...r, marketCheckedAt: now() }
  return { forecast: { ...f, parentId: f.parentId ?? null, history: lineage(s, f) } }
})
on('POST', '/api/forecasts/:id/market-position', ({ s, id, body }): Res<'openMarketPosition'> => {
  const f = find(marketForecasts(s, id), id, 'forecast')
  if (body.side !== 'yes' && body.side !== 'no') throw new Fail(400, 'bad_request', 'side must be yes or no.')
  const stake = body.stake
  if (!(typeof stake === 'number' && stake >= 10 && stake <= 1000)) throw new Fail(400, 'bad_request', 'stake must be $10 to $1,000.')
  if (f.status !== 'completed' || !f.result) throw new Fail(409, 'conflict', 'Only a completed forecast takes positions.')
  if (f.resolution) throw new Fail(409, 'conflict', 'This forecast has resolved.')
  if (superseded(s, f)) throw new Fail(409, 'conflict', 'A newer run of this forecast exists; take positions on it.')
  const m = f.result.market
  if (!m) throw new Fail(409, 'conflict', 'This forecast has no matched market.')
  if (stake > s.cash) throw new Fail(400, 'insufficient_buying_power', 'Not enough cash for this stake.')
  const side = body.side === 'yes' ? m.yesPrice : 1 - m.yesPrice
  const price = Math.min(side + 0.01, 0.99)
  const position: MarketPosition = {
    id: randomUUID(), forecastId: f.id, forecastQuestion: f.question, question: m.question, url: m.url, outcome: m.outcome ?? 'Yes', side: body.side,
    price, contracts: stake / price, cost: stake, maxPayout: stake / price, status: 'open', payout: null, pnl: null, markPrice: side, createdAt: now(), settledAt: null,
  }
  stakes(s).unshift(position)
  s.cash -= stake
  s.revision++
  return { position, portfolio: portfolioOf(s) }
}, 201)
// ponytail: positions on this run only; the API also lists the runs it updates.
on('GET', '/api/market-positions', ({ s, q }): Res<'marketPositions'> => ({ positions: stakes(s).filter((p) => !q.get('forecastId') || p.forecastId === q.get('forecastId')) }))
/** A resolution settles the open positions ($1 a winning contract); once settled it can't be cleared or changed. */
{
  const base = routes.find((r) => r.method === 'POST' && r.re.test('/api/forecasts/x/resolve'))!
  routes.unshift({
    ...base,
    handler: async (c) => {
      const mine = stakes(c.s).filter((p) => p.forecastId === c.id)
      const current = c.s.forecasts.get(c.id)?.resolution?.outcome
      if (current && c.body.outcome !== current && mine.some((p) => p.status !== 'open'))
        throw new Fail(409, 'conflict', "Paper positions on this forecast have settled, so its resolution can't change.")
      const out = await base.handler(c)
      const outcome = c.body.outcome as string | null
      for (const p of outcome ? mine.filter((p) => p.status === 'open') : []) {
        const won = (p.side === 'yes') === (outcome === p.outcome)
        Object.assign(p, { status: won ? 'won' : 'lost', payout: won ? p.contracts : 0, pnl: (won ? p.contracts : 0) - p.cost, markPrice: null, settledAt: now() })
        c.s.cash += p.payout!
        c.s.realized += p.pnl!
        c.s.revision++
      }
      return out
    },
  })
}
around<'resetPortfolio'>('POST', '/api/portfolio/reset', (out, { s }) => (stakesOf.delete(s), out))
/** Every Portfolio the mock returns counts the open stakes: in equity at cost, and as `predictionStakes`. */
for (const r of routes) {
  const base = r.handler
  r.handler = async (c) => {
    const out = (await base(c)) as { portfolio?: Portfolio } | undefined
    const p = out?.portfolio
    if (!p || !('cash' in p)) return out
    const staked = openStakes(c.s)
    const equity = p.equity + staked
    return { ...out, portfolio: { ...p, equity, totalPnl: equity - START, totalPnlPct: ((equity - START) / START) * 100, buyingPower: Math.max(0, 2 * equity - p.grossExposure), predictionStakes: staked } }
  }
}
// ── Prediction markets ── end

// ── Desk vs market by strike: a completed ordered report with a Polymarket ladder (fixed, so no 5 s poll) ── begin
export const LADDER_FORECAST = 'f-ladder'
routes.unshift({
  method: 'GET',
  re: new RegExp(`^/api/forecasts/(${LADDER_FORECAST})$`),
  status: 200,
  handler: (): Res<'forecast'> => ({ forecast: { ...fx.ladderForecast({ createdAt: hoursAgo(2), completedAt: hoursAgo(2) }), parentId: null, history: [] } }),
})
// ── Desk vs market by strike ── end

// ── Deep research ── begin
// A completed deep report with a 30-finding ledger over 22 sources, the same question stored before deep research (no ledger,
// no depth: it must render as it always did), and a deep run frozen mid-research. All fixed, so no poll races a test.
type Finding = import('../src/lib/types').ForecastFinding
const deepSource = (id: string, title: string, source: string, day: string | null, read = false) =>
  ({ id, title, url: `https://${source.toLowerCase().replace(/[^a-z]+/g, '-')}.example/${id.toLowerCase()}`, source, publishedAt: day && `${day}T00:00:00Z`, read })
const DEEP_SOURCES = [
  ...fx.deepSources,
  deepSource('S11', 'Info-Tech Systems opens above its IPO price on SGX Catalist', 'The Edge Singapore', '2026-03-12', true),
  deepSource('S12', 'Kin Global listing announcement: first day of trading', 'SGX Group', '2026-04-23'),
  deepSource('S13', 'EGP Energy opens 7.8% above IPO price in SGX debut', 'The Business Times', '2026-07-29', true),
  deepSource('S14', 'UltraGreen.ai shares jump as much as 11.7% over IPO price in debut', 'Bloomberg', '2025-12-03'),
  deepSource('S15', "Centurion Accommodation REIT jumps 11% in Singapore's biggest listing", 'Reuters', '2025-09-25'),
  deepSource('S16', 'Data centre REIT NTT DC closes flat after lukewarm debut', 'The Edge Singapore', '2025-07-14'),
  deepSource('S17', 'Orchid Materials Holding Limited has filed an IPO in the amount of SGD 149.94 million', 'MarketScreener', '2026-10-05', true),
  deepSource('S18', 'Orchid Group proposes share distribution ahead of SGX listing', 'Synthesis Geopolitics', '2026-07-15'),
  deepSource('S19', 'Monthly Singapore strategy: tech names retreat from June peaks', 'DBS Group Research', '2026-08-05', true),
  deepSource('S20', 'Global chip sales rise for a ninth straight month', 'Semiconductor Industry Association', '2026-10-02'),
  deepSource('S21', 'S&P 500 up, Nasdaq at record high as megacap tech stocks rise', 'Kitco', '2026-10-05'),
  deepSource('S22', 'Orchid Group listing announcement: proposed spin-off of Orchid Materials', 'Bursa Malaysia', '2026-06-24', true),
]
const extra = (angle: string, direction: string, strength: Finding['strength'], asOf: string | null, sourceIds: string[], claim: string): Finding =>
  ({ id: '', claim, sourceIds, asOf, angle, direction, strength })
const STRENGTH_ORDER = ['strong', 'moderate', 'slight', 'minimal']
/** The API's ledger order (angle, then strength, then date, newest first) with F ids assigned after it. */
const DEEP_FINDINGS: Finding[] = [
  ...fx.deepFindings,
  extra('A1', 'higher', 'slight', '2026-03-12', ['S11'], 'Info-Tech Systems opened at S$0.95 against its S$0.87 IPO price, a 9.2% premium.'),
  extra('A1', 'higher', 'slight', '2026-04-23', ['S12'], 'Kin Global opened at S$0.27 on its 23 April 2026 debut, 17% above the S$0.23 offer price.'),
  extra('A1', 'higher', 'slight', '2026-07-29', ['S13'], 'EGP Energy opened at S$0.55 on its 29 July 2026 SGX debut, 7.8% above its S$0.51 IPO price.'),
  extra('A1', 'higher', 'slight', '2025-12-03', ['S14'], 'UltraGreen.ai opened at US$1.51 on its December 2025 SGX mainboard debut, about 4.1% above its US$1.45 offer price.'),
  extra('A1', 'higher', 'minimal', '2025-09-25', ['S15'], 'Centurion Accommodation REIT opened at S$0.98 on 25 September 2025, 11.4% above its S$0.88 IPO price.'),
  extra('A1', 'center', 'minimal', '2025-07-14', ['S16'], 'NTT DC REIT opened at US$1.02 on its July 2025 SGX debut against a US$1.00 IPO price, a 2% premium.'),
  extra('A2', 'center', 'moderate', '2026-10-05', ['S17'], 'An S&P Capital IQ filing summary records an IPO amount of S$149.94 million with every tranche priced at S$1.80.'),
  extra('A2', 'lower', 'minimal', '2026-07', ['S18'], 'Orchid Group plans to distribute up to six million Orchid Materials shares to its own shareholders, one for every 150 held.'),
  extra('A2', 'lower', 'minimal', '2026-07', ['S18'], 'Unclaimed distribution shares are to be sold on the open market within a month after the expiry date.'),
  extra('A2', 'tail', 'minimal', '2026-06-24', ['S22'], "Orchid Group's June listing announcement said the offering price would be set by bookbuilding before the prospectus."),
  extra('A2', 'context', 'minimal', '2026-10-05', ['S1'], 'The preliminary prospectus proposes a 28 October listing, with trading expected to begin at 2 p.m.'),
  extra('A3', 'higher', 'moderate', '2026-10-02', ['S20'], 'Global semiconductor sales rose for a ninth straight month in August, led by memory and logic chips.'),
  extra('A3', 'lower', 'slight', '2026-08-05', ['S19'], 'DBS said local technology names such as AEM, UMS and Frencken had retreated 20–30% from their June peaks.'),
  extra('A3', 'context', 'minimal', '2026-06-30', ['S1', 'S17'], "Orchid Materials' semiconductor-materials gross margin declined from 38.5% in FY2024 to 34.1% in FY2025."),
  extra('A4', 'context', 'minimal', '2026-10-05', ['S21'], 'Reuters, republished by Kitco, said risk sentiment stayed in check as US tech stocks hit records.'),
  extra('A4', 'context', 'minimal', '2026-09-07', ['S10'], 'An SMU academic told CNA that weak aftermarket performance can deter future SGX issuers.'),
]
  .map((f, i) => ({ f, i }))
  .sort((a, b) => a.f.angle.localeCompare(b.f.angle) || STRENGTH_ORDER.indexOf(a.f.strength) - STRENGTH_ORDER.indexOf(b.f.strength) ||
    (b.f.asOf ?? '').localeCompare(a.f.asOf ?? '') || a.i - b.i)
  .map(({ f }, i) => ({ ...f, id: `F${i + 1}` }))
const perAngle = (id: string) => DEEP_FINDINGS.filter((f) => f.angle === id).length

export const DEEP_FORECAST = 'f-deep'
export const LEGACY_FORECAST = 'f-legacy'
export const RUNNING_DEEP_FORECAST = 'f-running-deep'
export const DEEP_SOURCE = DEEP_SOURCES[2] // JustCo's debut: a comparables finding links to it
const deepResult: ForecastResult = {
  ...fx.deepForecastResult,
  sources: DEEP_SOURCES,
  findings: DEEP_FINDINGS,
  research: {
    ...fx.deepForecastResult.research,
    searches: 18,
    candidates: 96,
    angles: fx.deepAngles.map((a) => ({ ...a, findings: perAngle(a.id) })),
  },
  knowledgeNote: `Deep web research via Brave Search (2 rounds, 18 searches, ${DEEP_SOURCES.length} sources, ${DEEP_SOURCES.filter((x) => x.read).length} articles read, ${DEEP_FINDINGS.length} findings) + model knowledge (training data) as of ${now().slice(0, 16).replace('T', ' ')} UTC.`,
}
// What the same report looked like before deep research: one-shot research, 8 sources, no ledger, depth or watch list.
const { findings: _f, watch: _w, ...legacyBase } = fx.deepForecastResult
const legacyResult: ForecastResult = {
  ...legacyBase,
  sources: fx.deepSources.slice(0, 8),
  research: { queries: ['Orchid Materials SGX IPO', 'SGX IPO first day 2026', 'Orchid Materials prospectus'], status: 'live' },
  knowledgeNote: 'Live web research via Brave Search (8 sources, 6 articles read) + model knowledge (training data).',
}
/** The fixed reports deepRoute serves, by id, so a chat opened from one (context.forecastId) can find it too. */
const FIXED_FORECASTS = new Map<string, () => Forecast>()
const deepRoute = (id: string, forecast: () => Forecast) => {
  FIXED_FORECASTS.set(id, forecast)
  routes.unshift({ method: 'GET', re: new RegExp(`^/api/forecasts/(${id})$`), status: 200, handler: (): Res<'forecast'> => ({ forecast: { ...forecast(), parentId: null, history: [] } }) })
}
deepRoute(DEEP_FORECAST, () => fx.deepForecast({ id: DEEP_FORECAST, result: deepResult, createdAt: hoursAgo(1), completedAt: hoursAgo(1) }))
deepRoute(LEGACY_FORECAST, () => fx.deepForecast({ id: LEGACY_FORECAST, result: legacyResult, createdAt: hoursAgo(30), completedAt: hoursAgo(30) }))
deepRoute(RUNNING_DEEP_FORECAST, () =>
  fx.deepForecast({ id: RUNNING_DEEP_FORECAST, status: 'running', result: null, completedAt: null, createdAt: hoursAgo(0), progress: { ...fx.deepProgress, updatedAt: now() } }))
// The deep report with three of the user's files (one reused, one unread), and a run still reading them.
export const MATERIALS_FORECAST = 'f-materials'
export const RUNNING_MATERIALS_FORECAST = 'f-running-materials'
const materialsResult: ForecastResult = {
  ...deepResult,
  findings: [...DEEP_FINDINGS, ...fx.materialsForecastResult.findings!.filter((x) => x.origin).map((x, i) => ({ ...x, id: `F${DEEP_FINDINGS.length + i + 1}` }))],
  evidence: fx.materialsForecastResult.evidence,
  flags: fx.materialsForecastResult.flags,
}
deepRoute(MATERIALS_FORECAST, () => fx.materialsForecast({ id: MATERIALS_FORECAST, result: materialsResult, resolutionDate: '2099-10-28', createdAt: hoursAgo(1), completedAt: hoursAgo(1) }))
deepRoute(RUNNING_MATERIALS_FORECAST, () =>
  fx.materialsForecast({
    id: RUNNING_MATERIALS_FORECAST, status: 'running', result: null, completedAt: null, createdAt: hoursAgo(0),
    materials: fx.materials.map((m) => (m.reusedFrom ? m : { ...m, status: 'pending', error: undefined, summary: '', relevance: undefined, findings: [], excerpt: '', readAt: null })),
    progress: { ...fx.materialsProgress, materials: { status: 'running', total: 3, reused: 1, read: 1, failed: 0 }, updatedAt: now() },
  }))
// ── Deep research ── end

// ── Desk view and methods ── begin
// The TSM question re-run with three methods (view, scenarios, methods, measured history) and the same question stored
// before desk views, whose view GET derives from the old buckets. Fixed, so no poll races a test.
export const VIEW_FORECAST = 'f-view'
export const DERIVED_FORECAST = 'f-view-derived'
deepRoute(VIEW_FORECAST, () => fx.tsmDeskForecast({ id: VIEW_FORECAST, createdAt: hoursAgo(1), completedAt: hoursAgo(1) }))
deepRoute(DERIVED_FORECAST, () => fx.tsmDeskForecast({ id: DERIVED_FORECAST, result: fx.tsmLegacyResult, createdAt: hoursAgo(30), completedAt: hoursAgo(30) }))
// The draft of "Where will TSM trade in 5 years?": seven ranges at multiples of $485 and the level they are cut around.
const TSM_DRAFT = {
  draft: {
    shape: 'ordered' as const,
    question: 'What will be the NYSE closing price of the TSM ADR on 2031-10-06?',
    outcomes: fx.tsmDraftBuckets,
    resolutionCriteria: 'Resolves to the range holding the NYSE closing price of the TSM ADR on 2031-10-06 (Yahoo Finance history); if not a trading day, the last close before.',
    resolutionDate: '2031-10-06',
    category: 'markets' as const,
    priceRule: null,
    context: '',
  },
  reference: fx.tsmDraftReference,
}
// ── Desk view and methods ── end

// ── Community strategies (publish / star / copy) and an invested portfolio's actual vs BTC ── begin
type Community = NonNullable<Strategy['community']>
/** Two strategies other users published: the board's fixtures. Stars are per session, like everything else here. */
export const COMMUNITY = { eth: 'c-eth-breakout', btc: 'c-btc-dip', sol: 'c-sol-rsi' }
const communityFixtures = (): Strategy[] => [
  fx.strategy({ id: COMMUNITY.eth, name: 'ETH breakout retest', author: 'Ann Lee', prompt: 'Buy ETH breakouts that retest the prior high',
    spec: { ...fx.strategy().spec, name: 'ETH breakout retest', symbol: 'ETH', interval: '1h', takeProfitPct: 8 },
    createdAt: '2026-08-31T23:00:00Z',
    forward: fx.forward({ metrics: { ...fx.forward().metrics!, trades: 5, startT: 1788228000, endT: 1791154800, bars: 814 },
      equity: [{ t: 1788228000, v: 100 }, { t: 1788231600, v: 100.5 }, { t: 1791154800, v: 104.2 }],
      benchmark: [{ t: 1788228000, v: 100 }, { t: 1788231600, v: 100.2 }, { t: 1791154800, v: 101.3 }],
    }),
    community: fx.publishedBy('Ann Lee', { publishedAt: '2026-09-01T02:30:00.123Z' }) }),
  fx.strategy({ id: COMMUNITY.btc, name: 'BTC dip ladder', author: 'Ravi Shah', prompt: 'Buy BTC dips under the 50 EMA',
    createdAt: '2026-08-31T23:00:00Z',
    spec: { ...fx.strategy().spec, name: 'BTC dip ladder', symbol: 'BTC', interval: '4h' },
    metrics: { ...fx.strategy().metrics, sharpe: 0.94, totalReturnPct: 11.2, trades: 6 }, shortSample: true,
    forward: fx.forward({ metrics: null, early: true, reason: 'history_unavailable', equity: [], benchmark: [], trades: [] }),
    community: fx.publishedBy('Ravi Shah', { stars: 1, copies: 0, deploys: 0, publishedAt: '2026-09-01T02:30:00.123Z' }) }),
  fx.strategy({ id: COMMUNITY.sol, name: 'SOL RSI replay', author: 'Mei Tan', prompt: 'Buy SOL RSI dips', createdAt: '2026-08-31T23:00:00Z',
    forward: fx.forward({ stale: true, early: true, reason: 'market_unavailable', asOf: '2026-10-01T00:00:00Z',
      metrics: { ...fx.forward().metrics!, totalReturnPct: 2.4, buyHoldReturnPct: 3.1, trades: 4, endT: 1790798400, bars: 180 },
      equity: [{ t: 1788220800, v: 100 }, { t: 1788235200, v: 100.4 }, { t: 1790798400, v: 102.4 }],
      benchmark: [{ t: 1788220800, v: 100 }, { t: 1788235200, v: 100.5 }, { t: 1790798400, v: 103.1 }],
    }), community: fx.publishedBy('Mei Tan', { stars: 2, publishedAt: '2026-09-01T02:30:00.123Z' }) }),
].map((x) => savedBacktest(x, x.createdAt))
const boards = new WeakMap<State, Map<string, Strategy>>()
const othersOf = (s: State) => boards.get(s) ?? (boards.set(s, new Map(communityFixtures().map((x) => [x.id, x]))), boards.get(s)!)
const own = (x: Strategy): Strategy => ({ ...x, community: x.community ?? fx.community({ owner: fx.user.name }) })
const withSummary = (x: Strategy): StrategySummary => ({ ...summary(x), community: x.community ?? null })
/** A published strategy the caller may star or read: another user's, or their own once published. */
const published = (s: State, id: string) => {
  const x = othersOf(s).get(id) ?? s.strategies.get(id)
  if (!x?.community?.published) throw new Fail(404, 'not_found', 'Not found.')
  return x
}

// Every own strategy carries its community info (the generated one rides along in router state, so it has it too).
around<'generateStrategy'>('POST', '/api/strategies/generate', (out, { s }) => {
  s.strategies.set(out.strategy.id, own({ ...out.strategy, forward: unavailableForward(out.strategy.createdAt, 'saved') }))
  return { ...out, strategy: s.strategies.get(out.strategy.id)! }
})
{
  const list = routes.find((r) => r.method === 'GET' && r.re.test('/api/strategies'))!
  routes.unshift({
    ...list,
    handler: async (c): Promise<Res<'strategies'>> => {
      const scope = c.q.get('scope')
      if (scope === 'community') {
        const all = [...othersOf(c.s).values(), ...[...c.s.strategies.values()].filter((x) => x.community?.published)]
        return { strategies: all.sort((a, b) => Number(a.shortSample) - Number(b.shortSample) || b.community!.stars - a.community!.stars).map(withSummary) }
      }
      if (scope === 'top') return list.handler(c) as Res<'strategies'>
      return { strategies: [...c.s.strategies.values()].reverse().map((x) => withSummary(own(x))) }
    },
  })
  const detail = routes.find((r) => r.method === 'GET' && r.re.test('/api/strategies/x'))!
  routes.unshift({
    ...detail,
    handler: async (c): Promise<Res<'strategy'>> => {
      const other = othersOf(c.s).get(c.id)
      if (other) return { strategy: { ...other, prompt: '' } }
      const out = (await detail.handler(c)) as Res<'strategy'>
      return { strategy: c.s.strategies.has(c.id) ? own(out.strategy) : out.strategy } // Labs carry community null
    },
  })
}
on('POST', '/api/strategies/:id/publish', ({ s, id, body }): Res<'publishStrategy'> => {
  const x = own(find(s.strategies, id, 'strategy'))
  if (typeof body.published !== 'boolean') throw new Fail(400, 'bad_request', 'published must be true or false.')
  if (body.published && x.metrics.trades < 3) throw new Fail(409, 'conflict', 'Backtest at least 3 trades before publishing.')
  const community: Community = { ...x.community!, published: body.published, publishedAt: body.published ? (x.community!.publishedAt ?? now()) : null }
  const since = community.publishedAt ?? x.createdAt
  const basis = community.published ? 'published' : 'saved'
  s.strategies.set(id, { ...x, community, forward: x.forward?.since === since && x.forward.basis === basis ? x.forward : unavailableForward(since, basis) })
  return { strategy: s.strategies.get(id)! }
})
on('POST', '/api/strategies/:id/star', ({ s, id, body }): Res<'starStrategy'> => {
  const x = published(s, id)
  const was = x.community!
  const starred = body.starred === true
  x.community = { ...was, starred, stars: was.stars + (starred === was.starred ? 0 : starred ? 1 : -1) }
  return { stars: x.community.stars, starred }
})
on(
  'POST',
  '/api/strategies/:id/copy',
  ({ s, id }): Res<'copyStrategy'> => {
    const lab = labStrategies().find((x) => x.id === id)
    const src = lab ?? othersOf(s).get(id) ?? find(s.strategies, id, 'strategy')
    if (!lab && src.community && !src.community.mine && !src.community.published) throw new Fail(404, 'not_found', 'Not found.')
    if (src.community && src.community.mine === false) src.community = { ...src.community, copies: src.community.copies + 1 }
    const copiedFrom = { id: src.id, name: src.name, owner: lab ? 'Foretrade Labs' : (src.community?.owner ?? fx.user.name) }
    const createdAt = now()
    const copy = savedBacktest({ ...src, id: randomUUID(), author: fx.user.name, prompt: src.community?.mine === false ? '' : src.prompt,
      createdAt, forward: unavailableForward(createdAt, 'saved'), community: fx.community({ owner: fx.user.name, copiedFrom }) }, createdAt)
    s.strategies.set(copy.id, copy)
    return { strategy: copy }
  },
  201,
)

/** Controlled hourly venue closes; no points before the buy or after now, and
 * no comparison pretending to cover an investment outside retained history. */
around<'aiPortfolio'>('GET', '/api/ai-portfolios/x', (out, { s, id }) => {
  const inv = out.portfolio.invested
  if (!inv) return out
  const legs = s.trades.filter((t) => t.ref?.kind === 'portfolio' && t.ref.id === id && t.side === 'buy')
  const v = round2((legs.reduce((a, t) => a + t.qty * mark(t.symbol) * (isStockPerp(t.symbol) ? 0.997 : 1), 0) / legs.reduce((a, t) => a + t.notional + t.fee, 0)) * 100)
  const last = Math.floor(Date.now() / 3_600_000) * 3600
  const t0 = Math.max(Math.ceil(Date.parse(inv.firstAt) / 3_600_000) * 3600, last - 999 * 3600)
  const ts = Array.from({ length: Math.max(0, (last - t0) / 3600 + 1) }, (_, i) => t0 + i * 3600)
  if (ts.length < 2) return out
  const benchmarkSeries = inv.benchmarkReturnPct == null ? null : ts.map((t) => ({ t, v: 100 }))
  return { portfolio: { ...out.portfolio, invested: { ...inv, series: ts.map((t) => ({ t, v })), benchmarkSeries, btcSeries: inv.benchmarkSymbol === 'BTC' ? benchmarkSeries : null, windowStart: ts[0], windowEnd: ts.at(-1)!, bars: ts.length } } }
})
// ── Community strategies ── end

// ── Server ──────────────────────────────────────────────────────────────────

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

  let raw = ''
  let size = 0
  // The upload cap (chat messages, forecast runs and updates with materials) is the API's 32 MiB.
  const bodyLimit = url.pathname === '/api/chat/stream' || url.pathname === '/api/forecasts' || /^\/api\/forecasts\/[^/]+\/update$/.test(url.pathname) ? 33_554_432 : 65_536
  for await (const chunk of req) {
    size += Buffer.byteLength(chunk)
    if (size > bodyLimit) throw new Fail(400, 'bad_request', 'Request body too large.')
    raw += chunk
  }
  // CSRF rule: every non-GET carries a JSON content type, and a cross-origin request must come from the web origin.
  if (method !== 'GET' && (!req.headers['content-type']?.startsWith('application/json') || (req.headers.origin && req.headers.origin !== WEB_URL)))
    throw new Fail(403, 'forbidden', 'cross-site or non-JSON request')
  let body
  try {
    body = raw ? JSON.parse(raw) : {}
  } catch {
    throw new Fail(400, 'bad_request', 'invalid JSON')
  }

  if (method === 'POST' && url.pathname === '/api/auth/login') {
    if (body.email !== DEMO.email || body.password !== DEMO.password) throw new Fail(401, 'invalid_credentials', 'Invalid email or password.')
    const token = randomBytes(32).toString('base64url')
    const state = createState()
    sessions.set(token, state)
    res.setHeader('Set-Cookie', `tsid=${token}; Path=/; HttpOnly; SameSite=Lax; Max-Age=${7 * DAY}`)
    return json(res, 200, { user: userOf(state) } satisfies Res<'login'>)
  }

  const token = /(?:^|;\s*)tsid=([^;]+)/.exec(req.headers.cookie ?? '')?.[1] ?? ''
  const s = sessions.get(token)
  if (!s) throw new Fail(401, 'unauthorized', 'Sign in to continue.')
  if (method === 'POST' && url.pathname === '/api/auth/logout') {
    sessions.delete(token)
    res.setHeader('Set-Cookie', 'tsid=; Path=/; HttpOnly; SameSite=Lax; Max-Age=0')
    return json(res, 204)
  }

  for (const r of routes) {
    const m = r.method === method && r.re.exec(url.pathname)
    if (!m) continue
    const out = await r.handler({ s, body, q: url.searchParams, id: decodeURIComponent(m[1] ?? ''), req, res })
    if (!res.headersSent) json(res, r.status, out)
    return
  }
  throw new Fail(404, 'not_found', `mock API has no ${method} ${url.pathname}`)
}

export function startMockApi(port = API_PORT) {
  const server = createServer((req, res) =>
    handle(req, res).catch((err: unknown) => {
      const e = err instanceof Fail ? err : new Fail(500, 'internal', String(err))
      if (res.headersSent) return void res.end()
      json(res, e.status, { error: { code: e.code, message: e.message } })
    }),
  )
  return new Promise<typeof server>((resolve, reject) => server.once('error', reject).listen(port, '127.0.0.1', () => resolve(server)))
}

/** Playwright globalSetup: start the mock API for the run; the returned function stops it. */
export default async function globalSetup() {
  const server = await startMockApi()
  return () => new Promise<void>((resolve) => server.close(() => resolve()))
}

// ── Prediction-market odds triggers: checked against the session's report and its stored market; the mock never
// re-prices or fires one. ── begin
export function createOddsAlert(s: State, r: AlertRequest): Res<'createAlert'> {
  if (!!r.forecastId === !!r.marketSlug) throw new Fail(400, 'bad_request', 'Provide exactly one odds alert target.')
  const direct = r.marketSlug ? ['123', '124', '125', '201'].map(predictionLeaf).find((d) => d.market.marketSlug === r.marketSlug) : null
  if (r.marketSlug && (!direct || (r.marketId && r.marketId !== direct.market.id))) throw new Fail(400, 'bad_request', 'Use the canonical leaf market slug and matching ID.')
  const f = r.forecastId ? s.forecasts.get(r.forecastId) : undefined
  if (!direct && (f?.status !== 'completed' || f.replay || f.resolution)) throw new Fail(400, 'bad_request', 'Unknown forecast.')
  const m = direct ? { ...direct.market, url: direct.event.url, asOf: now(), outcome: null } : f?.result?.market
  if (!m) throw new Fail(400, 'bad_request', 'This forecast has no matched Polymarket market.')
  if ((r.action ?? 'notify') !== 'notify') throw new Fail(400, 'bad_request', 'Odds alerts notify only.')
  if (r.notional != null || r.stopLoss != null || r.takeProfit != null) throw new Fail(400, 'bad_request', 'A notify alert takes no notional, stopLoss or takeProfit.')
  if (r.op !== 'above' && r.op !== 'below') throw new Fail(400, 'bad_request', 'Choose above or below.')
  const price = r.price ?? 0
  if (!(price >= 0.01 && price <= 0.99)) throw new Fail(400, 'bad_request', 'Pick a level between 1% and 99%.')
  if (r.op === 'above' ? m.yesPrice >= price : m.yesPrice <= price)
    throw new Fail(400, 'bad_request', `The market is already at ${Math.round(m.yesPrice * 1000) / 10}%. Pick a level ${r.op} it.`)
  if (r.proposalId && myAlerts(s).some((a) => a.proposalId === r.proposalId)) throw new Fail(409, 'conflict', 'This alert was already set.')
  validateAlertReceipt(s, r)
  const alert: Alert = {
    id: randomUUID(),
    symbol: '',
    ...priceTrigger,
    trigger: 'odds',
    op: r.op ?? 'above',
    price,
    action: 'notify',
    notional: null,
    stopLoss: null,
    takeProfit: null,
    note: (r.note ?? '').trim(),
    source: r.source ?? 'manual',
    proposalId: r.proposalId ?? null,
    status: 'active',
    tradeId: null,
    error: null,
    firedPrice: null,
    createdAt: now(),
    firedAt: null,
    forecastId: f?.id ?? null,
    marketSlug: direct?.market.marketSlug ?? null,
    marketId: direct?.market.id ?? null,
    linkedMarket: direct?.draft.linkedMarket ?? null,
    market: { question: m.question, url: m.url, outcome: m.outcome ?? null, yesPrice: m.yesPrice, asOf: m.asOf },
  }
  myAlerts(s).unshift(alert)
  return { alert }
}

// Account reads mirror the five approved compact tool results. No helper mutates a book or charges credits.
const accountUnavailable = new WeakSet<State>()
const accountHistoryPrices = new WeakMap<State, Record<string, number>>()
const accountLabels = {
  get_trades: 'Reading your paper trades', get_pnl_attribution: 'Checking your P&L attribution',
  get_automations: 'Reviewing your automations', get_autopilots: 'Checking your autopilots', get_ai_portfolios: 'Reviewing your AI portfolios',
} as const
const newest = <T>(rows: T[], at: (row: T) => string, id: (row: T) => string) => [...rows].sort((a, b) => at(b).localeCompare(at(a)) || id(b).localeCompare(id(a)))
const plainData = (text: string) => text.replace(/<\s*\/?\s*untrusted_web_content\s*>/gi, '').trim()
function bounded(text: string, limit: number, omitted: { truncated: boolean }) {
  const runes = Array.from(text)
  if (runes.length > limit) { omitted.truncated = true; text = runes.slice(0, limit - 1).join('') + '…' }
  if (!text) return ''
  let old = ''
  while (old !== text) { old = text; text = text.replace(/<\s*\/?\s*untrusted_web_content\s*>/gi, '') }
  return `<untrusted_web_content>\n${text}\n</untrusted_web_content>`
}
function ownedRef(s: State, trade: Trade): TradeRef | null {
  const ref = trade.ref
  if (!ref) return null
  switch (ref.kind) {
    case 'chat': return s.chats.has(ref.id) ? { ...ref, label: s.chats.get(ref.id)!.title } : null
    case 'portfolio': { const p = s.portfolios.find(p => p.id === ref.id); return p ? { ...ref, label: p.name } : null }
    case 'alert': { const a = myAlerts(s).find(a => a.id === ref.id); return a ? { ...ref, label: a.note } : null }
    case 'copy': { const p = liveState(s).plans.find(p => p.id === ref.id); return p ? { ...ref, label: p.agentName } : null }
    case 'autopilot': { const p = pilotsOf(s).find(p => p.strategyId === ref.id && p.symbol === trade.symbol && trade.createdAt >= p.startedAt && (!p.stoppedAt || Date.parse(trade.createdAt) <= Date.parse(p.stoppedAt) + 60000)); return p ? { ...ref, label: p.name } : null }
  }
}
export function accountTradesOf(s: State, args: { limit?: number; symbol?: string } = {}) {
  const limit = args.limit ?? 20
  if (args.limit === null || !Number.isInteger(limit) || limit < 1 || limit > 50) throw new Fail(400, 'bad_request', 'limit must be an integer from 1 to 50')
  if ('symbol' in args && typeof args.symbol !== 'string') throw new Fail(400, 'bad_request', 'symbol must be a configured ticker')
  const symbol = args.symbol?.trim().toUpperCase()
  if ('symbol' in args && (!symbol || !Object.hasOwn(prices, symbol))) throw new Fail(400, 'bad_request', 'symbol must be a configured ticker')
  // s.trades retains descending booking order, including equal timestamps; stable sorting preserves that tie order.
  const rows = [...s.trades].sort((a, b) => b.createdAt.localeCompare(a.createdAt)).filter(t => !symbol || t.symbol === symbol)
  const out = { trades: [] as Trade[], truncated: rows.length > limit }
  out.trades = rows.slice(0, limit).map(t => {
    const ref = ownedRef(s, t)
    return { ...t, note: bounded(t.note, 200, out), ref: ref ? { ...ref, label: bounded(ref.label, 120, out) } : null }
  })
  return out
}
export function accountPnlOf(s: State, range?: EquityRange) {
  if (range !== undefined && !Object.hasOwn(EQUITY_GRIDS, range)) throw new Fail(400, 'bad_request', 'range must be 1d, 7d or 30d')
  const attribution = { scope: 'since_reset' as const, ...attributionOf(s) }
  return range === undefined ? { attribution } : { attribution, equitySummary: summarizeEquity(range, equityOf(s, range).points) }
}
export function accountAutomationsOf(s: State) {
  const rows = newest(myAlerts(s).filter(a => a.status === 'active'), a => a.createdAt, a => a.id)
  const out = { automations: [] as Alert[], truncated: rows.length > 20 }
  out.automations = rows.slice(0, 20).map(a => {
    const ownsForecast = !a.forecastId || s.forecasts.has(a.forecastId)
    const ownsProposal = !a.proposalId || [...s.chats.values()].some(c => c.messages.some(m => m.cards.some(card => card.type === 'price_alert' && card.data.proposalId === a.proposalId)))
    let market = ownsForecast && a.market ? { ...a.market, question: bounded(a.market.question, 120, out), outcome: a.market.outcome == null ? null : bounded(a.market.outcome, 120, out) } : null
    if (market?.url) { out.truncated = true; market = { ...market, url: '' } }
    if (a.linkedMarket) out.truncated = true
    return { ...a, note: bounded(a.note, 200, out), error: a.error == null ? null : bounded(a.error, 160, out),
      forecastId: ownsForecast ? a.forecastId ?? null : null, market, linkedMarket: null,
      proposalId: ownsProposal ? a.proposalId : null, tradeId: s.trades.some(t => t.id === a.tradeId) ? a.tradeId : null,
      lastRun: a.lastRun ? { ...a.lastRun, body: bounded(a.lastRun.body, 300, out), error: a.lastRun.error == null ? null : bounded(a.lastRun.error, 160, out) } : null }
  })
  return out
}
export function accountAutopilotsOf(s: State) {
  const rows = newest(pilotsOf(s), p => p.startedAt, p => p.id)
  const out = { autopilots: [] as Autopilot[], truncated: rows.length > 10 }
  out.autopilots = rows.slice(0, 10).map(p => {
    if (p.log.length > 5) out.truncated = true
    return { ...(accountUnavailable.has(s) ? { ...p, markPrice: null, equity: null, pnl: null, pnlPct: null, drawdownPct: null } : valued(p)),
      name: bounded(p.name, 120, out), log: p.log.slice(0, 5).map(log => ({ ...log, text: bounded(log.text, 200, out) })) }
  })
  return out
}
type CompactInvested = Pick<NonNullable<AIPortfolio['invested']>, 'amount' | 'trades' | 'firstAt' | 'realizedPnl' | 'unrealizedPnl' | 'totalPnl' | 'returnPct' | 'benchmarkSymbol' | 'benchmarkLabel' | 'benchmarkReturnPct' | 'btcReturnPct'>
export function accountPortfoliosOf(s: State) {
  const eligible = s.portfolios.filter(p => s.trades.some(t => t.source === 'portfolio' && t.side === 'buy' && t.qty > 0 && t.ref?.kind === 'portfolio' && t.ref.id === p.id))
  const rows = newest(eligible, p => p.createdAt, p => p.id)
  const out = { portfolios: [] as { id: string; name: string; createdAt: string; benchmarkSymbol: string; benchmarkLabel: string; invested: CompactInvested | null }[], truncated: rows.length > 10 }
  out.portfolios = rows.slice(0, 10).map(p => {
    const value = investedIn(s, p).invested
    const invested = value ? Object.fromEntries(['amount', 'trades', 'firstAt', 'realizedPnl', 'unrealizedPnl', 'totalPnl', 'returnPct', 'benchmarkSymbol', 'benchmarkLabel', 'benchmarkReturnPct', 'btcReturnPct'].map(k => [k, value[k as keyof typeof value]])) as CompactInvested : null
    return { id: p.id, name: bounded(p.name, 120, out), createdAt: p.createdAt, benchmarkSymbol: p.backtest.benchmarkSymbol || 'BTC', benchmarkLabel: p.backtest.benchmarkLabel || 'BTC', invested }
  })
  return out
}
const money = (n: number) => `$${n.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`
const pnlCopy = (rows: SourcePnl[]) => rows.map(r => `${r.source}: realized ${money(r.realizedPnl)}, unrealized ${money(r.unrealizedPnl)}, fees ${money(r.fees)}, total ${money(r.totalPnl)}`).join('; ')
function accountTurn(chatId: string, title: string, s: State, message: string): ChatStreamEvent[] | null {
  const tradeReview = /review my (last trade|trades)/i.test(message)
  const automationReview = /how are my automations|review my automations/i.test(message)
  if (!tradeReview && !automationReview) return null
  const events: ChatStreamEvent[] = [{ event: 'chat', data: { chatId, title } }]
  const read = (name: keyof typeof accountLabels, summary: string, input: Record<string, unknown> = {}) => {
    events.push({ event: 'tool_call', data: { id: name, name, label: accountLabels[name], input } }, { event: 'tool_result', data: { id: name, name, ok: true, summary } })
  }
  let text = ''
  if (tradeReview) {
    const trades = accountTradesOf(s, { limit: /last trade/i.test(message) ? 1 : 20 }), p = portfolioOf(s)
    const range = /\b(1d|7d|30d)\b/.exec(message)?.[1] as EquityRange | undefined
    const pnl = accountPnlOf(s, range)
    read('get_trades', trades.trades.length ? `Last ${trades.trades.length} fills · ${new Set(trades.trades.map(t => t.symbol)).size} symbols` : 'No paper fills', { limit: /last trade/i.test(message) ? 1 : 20 })
    events.push({ event: 'tool_call', data: { id: 'account', name: 'get_portfolio', label: 'Reading your portfolio', input: {} } }, { event: 'tool_result', data: { id: 'account', name: 'get_portfolio', ok: true, summary: `Equity ${money(p.equity + openStakes(s))}` } })
    read('get_pnl_attribution', 'Since-reset P&L by source', range ? { range } : {})
    text = trades.trades.length ? trades.trades.map(t => {
      const held = p.positions.find(pos => pos.symbol === t.symbol)
      const equity = p.equity + openStakes(s)
      const refPath = t.ref ? { chat: `/chat/${t.ref.id}`, portfolio: `/portfolios/${t.ref.id}`, autopilot: `/strategies/${t.ref.id}`, alert: '/automations', copy: '/account#copy-plans' }[t.ref.kind] : null
      const origin = refPath ? `Origin [${plainData(t.ref!.label).replace(/[\[\]\\]/g, '')}](${refPath}).` : 'Origin unavailable; booked fill retained.'
      const heldMinutes = Math.max(0, Math.floor((Date.now() - Date.parse(t.createdAt)) / 60000))
      return `**${t.side.toUpperCase()} ${t.symbol}** · Entry ${money(t.price)} · quantity ${t.qty.toFixed(6)} · size ${money(t.notional)} (${equity > 0 ? (t.notional / equity * 100).toFixed(2) + '% of equity' : 'equity percentage unavailable'}) · Fees ${money(t.fee)} · ${heldMinutes} minutes since fill. No recorded entry plan; do not infer historical intent. Current protection: stop ${held?.stopLoss == null ? 'not set' : money(held.stopLoss)}, target ${held?.takeProfit == null ? 'not set' : money(held.takeProfit)}. ${origin}`
    }).join('\n\n') : 'No paper fills since your last reset. There is no last trade to judge.'
    text += `\n\nSince-reset P&L ${money(pnl.attribution.totalPnl)}. FIFO opening-lot attribution includes all fills since reset, shared manual/copy lots and fees. ${pnlCopy(pnl.attribution.sources)}.`
    if ('equitySummary' in pnl && pnl.equitySummary) {
      const summary = pnl.equitySummary
      text += `\n\nSelected ${summary.range} equity window: ${money(summary.startEquity)} → ${money(summary.endEquity)}; return ${summary.returnPct == null ? 'unavailable' : summary.returnPct.toFixed(2) + '%'}; maximum drawdown ${summary.maxDrawdownPct.toFixed(2)}%. This is a selected-window equity change, separate from since-reset source P&L.`
    }
    if (trades.truncated) text += '\n\nShowing a bounded recent-fill sample; older fills remain in since-reset accounting.'
    text += '\n\nHolding is valid; no change is required from these facts. Nothing executes from this review.'
    if (/warranted trim fixture/i.test(message) && s.positions.has('ETH')) events.push({ event: 'card', data: { type: 'trade_proposal', data: { symbol: 'ETH', side: 'sell', notional: 500, price: mark('ETH'), stopLoss: null, takeProfit: null, rationale: 'Optional paper trim. Nothing executes until you confirm.', proposalId: randomUUID(), status: 'open', trade: null } } })
  } else {
    const a = accountAutomationsOf(s), pilots = accountAutopilotsOf(s), portfolios = accountPortfoliosOf(s)
    read('get_automations', `${a.automations.length} active automations`); read('get_autopilots', `${pilots.autopilots.length} paper autopilots`); read('get_ai_portfolios', `${portfolios.portfolios.length} invested AI portfolios`)
    text = `${a.automations.length} active automations · ${pilots.autopilots.length} paper autopilots · ${portfolios.portfolios.length} invested AI portfolios since reset.`
    for (const row of a.automations) text += `\n\n${row.symbol || 'Market odds'}: ${row.trigger === 'schedule' ? `${row.every} ${row.at} UTC` : row.trigger === 'drawdown' ? `${row.drawdown?.pct}% below the all-time high` : `${row.op} ${row.trigger === 'odds' ? ((row.price ?? 0) * 100).toFixed(1) + '%' : money(row.price ?? 0)}`} → ${row.action}${row.notional == null ? '' : ' ' + money(row.notional)}. Latest run: ${row.lastRun ? `${row.lastRun.status}; ${plainData(row.lastRun.body)}${row.lastRun.error ? '; ' + plainData(row.lastRun.error) : ''}` : 'none recorded'}.`
    for (const row of pilots.autopilots) text += `\n\n${plainData(row.name)} (${row.symbol}, ${row.status}): allocation ${money(row.allocationUsd)}, P&L ${row.pnl == null ? 'unavailable; live valuation is null' : money(row.pnl)}, drawdown ${row.drawdownPct == null ? 'unavailable' : row.drawdownPct.toFixed(2) + '%'}.`
    for (const row of portfolios.portfolios) text += `\n\n${plainData(row.name)}: saved benchmark ${row.benchmarkLabel}; ${row.invested ? `invested ${money(row.invested.amount)}, FIFO P&L ${money(row.invested.totalPnl)}, hourly-proxy comparison ${row.invested.benchmarkReturnPct == null ? 'unavailable' : row.invested.benchmarkReturnPct.toFixed(2) + '%'}` : 'eligible investment retained; live valuation unavailable'}.`
    if (a.truncated || pilots.truncated || portfolios.truncated) text += '\n\nBounded read: additional rows or text were omitted.'
    text += '\n\nNo automation was created, cancelled, deployed, stopped or invested by this review. Any proposed change still needs your explicit confirmation.'
  }
  events.push({ event: 'delta', data: { text } }, { event: 'done', data: { messageId: randomUUID(), credits: s.credits } })
  return events
}

/** Authenticated local-only fixture control; never called by a read or by an application flow. */
export function seedAccountFixture(s: State, scenario: 'ledger' | 'references' | 'automations' | 'unavailable' | 'bounded') {
  if (!['ledger', 'references', 'automations', 'unavailable', 'bounded'].includes(scenario)) throw new Fail(400, 'bad_request', 'Unknown account fixture.')
  accountUnavailable.delete(s); accountHistoryPrices.delete(s)
  Object.assign(s, { cash: START, realized: 0, positions: new Map(), trades: [] })
  stakesOf.delete(s); alertsOf.delete(s); pilots.delete(s)
  liveMocks.delete(s)
  s.portfolios = []
  if (scenario === 'ledger') {
    // The approved API test's booked 1@100, 1@110, sell1.5@120 ledger, here at the mock's actual SOL150 mark.
    // Historical source closes are explicitly110; current live mark150 is a separate final observation.
    const manual = placeOrder(s, { symbol: 'SOL', side: 'buy', qty: 1, source: 'manual' })
    Object.assign(manual, { price: 100, notional: 100, fee: .1, realizedPnl: -.1, createdAt: hoursAgo(72) })
    const copy = placeOrder(s, { symbol: 'SOL', side: 'buy', qty: 1, source: 'copy' })
    Object.assign(copy, { price: 110, notional: 110, fee: .11, realizedPnl: -.11, createdAt: hoursAgo(48), ref: { kind: 'copy', id: 'missing-copy-plan', label: 'Unavailable plan' } })
    const exit = placeOrder(s, { symbol: 'SOL', side: 'sell', qty: 1.5, source: 'manual' })
    Object.assign(exit, { price: 120, notional: 180, fee: .18, realizedPnl: 22.32, createdAt: hoursAgo(1), ref: { kind: 'chat', id: SEED.chat, label: 'stale label' } })
    s.positions.set('SOL', { qty: .5, avgPrice: 105, stopLoss: 95, takeProfit: 180 })
    s.cash = 99982.61; s.realized = 42.11
    stakes(s).push({ id: 'settled-account-stake', forecastId: SEED.forecast, forecastQuestion: 'Fixture outcome', question: 'Fixture outcome', url: '', outcome: 'Yes', side: 'yes', price: .5, contracts: 30, cost: 10, maxPayout: 30, status: 'won', payout: 30, pnl: 20, markPrice: null, createdAt: hoursAgo(48), settledAt: hoursAgo(.5) },
      { id: 'open-account-stake', forecastId: SEED.forecast, forecastQuestion: 'Fixture outcome', question: 'Fixture outcome', url: '', outcome: 'Yes', side: 'yes', price: .5, contracts: 14, cost: 7, maxPayout: 14, status: 'open', payout: null, pnl: null, markPrice: .5, createdAt: hoursAgo(2), settledAt: null })
    accountHistoryPrices.set(s, { SOL: 110 })
    return
  }
  if (scenario === 'references') {
    const owned = placeOrder(s, { symbol: 'SOL', side: 'buy', notional: 100, source: 'agent' })
    owned.ref = { kind: 'chat', id: SEED.chat, label: 'stale label' }
    const missing = placeOrder(s, { symbol: 'NVDA', side: 'buy', notional: 500, source: 'alert', note: 'Booked stock fill' })
    missing.ref = { kind: 'alert', id: 'account-alert-00', label: 'PRIVATE FOREIGN LABEL' }
    return
  }
  const boundedFixture = scenario === 'bounded', counts = boundedFixture ? 22 : 3
  for (let i = 0; i < counts; i++) {
    const kind = i % 3
    myAlerts(s).push(fx.alert({ id: `account-alert-${String(i).padStart(2, '0')}`, source: 'manual', proposalId: null, createdAt: hoursAgo(i + 1),
      note: boundedFixture ? '🧭'.repeat(220) : 'Review fixture',
      ...(kind === 1 ? { trigger: 'schedule', symbol: 'BTC', op: null, price: null, every: 'day', at: '08:00', weekday: null, nextRunAt: now(), notional: 100, stopLoss: null, takeProfit: null, lastRun: fx.researchRun({ id: randomUUID(), at: hoursAgo(1), status: 'failed', body: boundedFixture ? '📄'.repeat(320) : 'Scheduled buy $100 BTC', error: boundedFixture ? '⚠'.repeat(180) : 'Not enough buying power when the alert fired.', reportChatId: null, excerpt: null, verdict: null }) } :
      kind === 2 ? { trigger: 'odds', symbol: '', forecastId: SEED.forecast, op: 'above', price: .75, action: 'notify', notional: null, stopLoss: null, takeProfit: null, market: { question: 'Fixture market', url: 'https://polymarket.com/event/fixture', outcome: 'Yes', yesPrice: .3, asOf: now() } } : {}) }))
  }
  myAlerts(s).push(fx.alert({ id: 'cancelled-newer', status: 'cancelled', createdAt: now() }))
  for (let i = 0; i < (boundedFixture ? 12 : 1); i++) pilotsOf(s).push(fx.autopilot({ id: `account-pilot-${String(i).padStart(2, '0')}`, name: boundedFixture ? '🚦'.repeat(130) : 'SOL review pilot', startedAt: hoursAgo(i + 1), log: Array.from({ length: boundedFixture ? 7 : 2 }, (_, j) => ({ t: hoursAgo(j + 1), kind: 'error', text: boundedFixture ? '📝'.repeat(210) : 'Order refused; position kept.' })) }))
  for (let i = 0; i < (boundedFixture ? 12 : 1); i++) {
    const p = fx.aiPortfolio({ id: `account-basket-${String(i).padStart(2, '0')}`, name: boundedFixture ? '🧺'.repeat(130) : 'Stock review basket', createdAt: hoursAgo(i + 30) })
    p.backtest = { ...p.backtest, benchmarkSymbol: i % 3 === 1 ? 'BTC' : 'SP500', benchmarkLabel: i % 3 === 1 ? 'BTC' : 'S&P 500 perp' }
    if (i % 3 === 2) { delete (p.backtest as Partial<typeof p.backtest>).benchmarkSymbol; delete (p.backtest as Partial<typeof p.backtest>).benchmarkLabel }
    s.portfolios.push(p)
    const t = placeOrder(s, { symbol: 'AAPL', side: 'buy', notional: 1000, source: 'portfolio', note: p.name })
    t.ref = { kind: 'portfolio', id: p.id, label: p.name }
  }
  if (boundedFixture) for (let i = 0; i < 55; i++) s.portfolios.unshift(fx.aiPortfolio({ id: `uninvested-${i}`, createdAt: now() }))
  if (scenario === 'unavailable') accountUnavailable.add(s)
}
on('POST', '/__e2e/account-fixture', ({ s, body }) => { seedAccountFixture(s, body.scenario); return { revision: s.revision } })
// ── Prediction-market odds triggers ── end

// Unit tests (Vitest) for the e2e mock API's paper engine: the Playwright suite trusts its numbers.
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { request as httpRequest } from 'node:http'
import { startMockApi, DEMO, candles, createState, placeOrder, portfolioOf } from './mock-api'
import * as mock from './mock-api'
import { parseSSE } from '../src/lib/api'
import { JPEG_BYTES } from '../src/test/image-fixtures'
import { CSV_TEXT, DOCX_BYTES, DOCX_TYPE, PDF_BYTES, XLSX_BYTES, XLSX_TYPE } from '../src/test/documents'
import * as fx from '../src/test/fixtures'
import type { Asset, Candle, Instrument, Card, Chat, ChatStreamEvent, Trade, AIPortfolio, CopyPlan, Forecast, ForecastDraft, MarketBrief, NewsItem, Portfolio, PortfolioReview, PredictionBoard, PredictionDraft, Strategy, StrategySummary } from '../src/lib/types'

// Unit HTTP proofs use their own ephemeral loopback listener; browser setup retains its reserved port.
let API_URL = ''
async function globalSetup() {
  const server = await startMockApi(0)
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('Missing mock listener address')
  API_URL = `http://127.0.0.1:${address.port}`
  return () => new Promise<void>((resolve) => server.close(() => resolve()))
}

/** A fresh $100,000 book (createState seeds an ETH long for the pages). */
function flat() {
  const s = createState()
  Object.assign(s, { cash: 100_000, realized: 0, positions: new Map(), trades: [] })
  return s
}

const SOL = 150 // fixture mark
const BUY_PX = SOL * 1.0002 // 2 bps slippage
const SELL_PX = SOL * 0.9998

describe('mock paper engine', () => {
  it('fills a buy at the mark plus slippage, charges 0.1% and books −fee as realized', () => {
    const s = flat()
    const t = placeOrder(s, { symbol: 'SOL', side: 'buy', notional: 1500 })
    expect(t.price).toBeCloseTo(BUY_PX)
    expect(t.qty).toBeCloseTo(1500 / BUY_PX)
    expect(t.fee).toBeCloseTo(1.5)
    expect(t.realizedPnl).toBeCloseTo(-1.5)
    const p = portfolioOf(s)
    expect(p.cash).toBeCloseTo(100_000 - 1500 - 1.5)
    expect(p.positions[0]).toMatchObject({ symbol: 'SOL', avgPrice: expect.closeTo(BUY_PX, 9) })
    expect(p.buyingPower).toBeCloseTo(2 * p.equity - p.grossExposure)
    expect(s.trades[0]).toBe(t) // newest first
  })

  it('realizes P&L on a partial sell at the average cost and keeps the rest open', () => {
    const s = flat()
    placeOrder(s, { symbol: 'SOL', side: 'buy', qty: 10 })
    const t = placeOrder(s, { symbol: 'SOL', side: 'sell', qty: 4 })
    expect(t.realizedPnl).toBeCloseTo(4 * (SELL_PX - BUY_PX) - t.fee)
    expect(portfolioOf(s).positions[0]).toMatchObject({ qty: 6, avgPrice: expect.closeTo(BUY_PX, 9) })
  })

  it('flips through flat into a short at the fill price, and a full close removes the position', () => {
    const s = flat()
    placeOrder(s, { symbol: 'SOL', side: 'buy', qty: 2 })
    placeOrder(s, { symbol: 'SOL', side: 'sell', qty: 5 })
    expect(portfolioOf(s).positions[0]).toMatchObject({ qty: -3, avgPrice: SELL_PX })
    placeOrder(s, { symbol: 'SOL', side: 'buy', qty: 3 })
    expect(portfolioOf(s).positions).toEqual([])
  })

  it('rejects orders the contract rejects, leaving the book untouched', () => {
    const s = flat()
    const reject = (o: Parameters<typeof placeOrder>[1], code: string) => {
      let err: unknown
      try {
        placeOrder(s, o)
      } catch (e) {
        err = e
      }
      expect(err).toMatchObject({ code })
    }
    reject({ symbol: 'SOL', side: 'buy', notional: 300_000 }, 'insufficient_buying_power') // above 2× equity
    reject({ symbol: 'SOL', side: 'buy', notional: 5 }, 'bad_request') // under $10
    reject({ symbol: 'SOL', side: 'buy', notional: 100, qty: 1 }, 'bad_request') // both sizes
    reject({ symbol: 'SOL', side: 'buy', notional: 100, stopLoss: 160 }, 'bad_request') // long stop above the price
    reject({ symbol: 'SOL', side: 'sell', notional: 100, takeProfit: 160 }, 'bad_request') // short target above the price
    reject({ symbol: 'NOPE', side: 'buy', notional: 100 }, 'bad_request')
    expect(portfolioOf(s)).toMatchObject({ cash: 100_000, positions: [] })
    expect(s.trades).toEqual([])
  })

  it('draws strictly increasing candles that close exactly at the mark', () => {
    const cs = candles('SOL', '1h', 50)
    expect(cs).toHaveLength(50)
    expect(cs.at(-1)!.c).toBe(SOL)
    expect(cs.every((c, i) => i === 0 || c.t - cs[i - 1].t === 3600)).toBe(true)
    expect(cs.every((c) => c.l <= Math.min(c.o, c.c) && c.h >= Math.max(c.o, c.c))).toBe(true)
  })

  it('opens weekly candles on UTC Mondays, including a Sunday latest bar and exclusive backward pages', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2024-03-03T12:34:56Z'))
    const recent = candles('SOL', '1w', 2)
    expect(recent.map((c) => new Date(c.t * 1000).toISOString())).toEqual(['2024-02-19T00:00:00.000Z', '2024-02-26T00:00:00.000Z'])
    const older = candles('SOL', '1w', 2, recent[0].t)
    expect(older.map((c) => new Date(c.t * 1000).toISOString())).toEqual(['2024-02-05T00:00:00.000Z', '2024-02-12T00:00:00.000Z'])
    expect(recent.at(-1)!.c).toBe(SOL)
    expect([...older, ...recent].every((c) => c.l <= Math.min(c.o, c.c) && c.h >= Math.max(c.o, c.c))).toBe(true)
  })

  it('uses calendar months across leap February and backward pages crossing a year', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2024-03-15T12:34:56Z'))
    const recent = candles('SOL', '1M', 3)
    expect(recent.map((c) => new Date(c.t * 1000).toISOString())).toEqual(['2024-01-01T00:00:00.000Z', '2024-02-01T00:00:00.000Z', '2024-03-01T00:00:00.000Z'])
    expect((recent[2].t - recent[1].t) / 86400).toBe(29)
    const older = candles('SOL', '1M', 3, recent[0].t)
    expect(older.map((c) => new Date(c.t * 1000).toISOString())).toEqual(['2023-10-01T00:00:00.000Z', '2023-11-01T00:00:00.000Z', '2023-12-01T00:00:00.000Z'])
    expect(recent.at(-1)!.c).toBe(SOL)
  })

  it('keeps calendar stock-perp history finite at its existing daily retention boundary', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2024-10-06T12:00:00Z'))
    const monthly = candles('AAPL', '1M', 500)
    expect(new Date(monthly[0].t * 1000).toISOString()).toBe('2023-12-01T00:00:00.000Z')
    expect(candles('AAPL', '1M', 500, monthly[0].t)).toEqual([])
    const weekly = candles('AAPL', '1w', 500)
    expect(new Date(weekly[0].t * 1000).toISOString()).toBe('2023-12-04T00:00:00.000Z')
    expect(candles('AAPL', '1w', 500, weekly[0].t)).toEqual([])
    expect(candles('AAPL', '1d', 500)).toHaveLength(303)
    expect(candles('SP500', '1d', 500)).toHaveLength(201)
  })
})

// Browser fixtures must preserve the same canonical/ownership boundary as the real API.
const predictions = mock as typeof mock & {
  predictionDraftOf: (id: string) => PredictionDraft
  predictionBoardOf: (s: ReturnType<typeof createState>, category?: string, sort?: string) => PredictionBoard
  createForecastFromDraft: (s: ReturnType<typeof createState>, d: ForecastDraft) => ReturnType<typeof fx.forecast>
  updateForecastFromParent: (s: ReturnType<typeof createState>, id: string) => Forecast
  createOddsAlert: (s: ReturnType<typeof createState>, r: import('../src/lib/types').AlertRequest) => { alert: import('../src/lib/types').Alert }
}
afterEach(() => vi.useRealTimers())
describe('mock prediction contracts', () => {
  it('lists grouped leaf identities, null unverified touch and macro crowd-only rows without a fabricated desk', () => {
    const s = createState()
    const board = predictions.predictionBoardOf(s)
    expect(board.events.map((e) => e.category)).toContain('macro')
    const crypto = board.events.find((e) => e.category === 'crypto')!
    expect(crypto.markets.map((m) => m.id)).toEqual(['123', '124', '125'])
    expect(crypto.markets[2]).toMatchObject({ estimate: null, estimateUnavailable: expect.stringMatching(/unverified/), desk: null })
    expect(crypto.markets[1].estimate).toMatchObject({ signal: 'wait', evPer100: null })
    expect(crypto.markets.every((m) => m.desk === null)).toBe(true)
    expect(predictions.predictionBoardOf(s, 'macro', 'volume').events.every((e) => e.category === 'macro')).toBe(true)
  })

  it('preserves below own-Yes, complete manual rules and snapshot through a paid creation exactly once', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-10-05T12:00:00Z'))
    const s = createState()
    const used = s.credits.used
    const d = predictions.predictionDraftOf('124')
    expect(d.market.yesPrice).toBe(0.2)
    expect(d.draft).toMatchObject({ priceRule: null, linkedMarket: { marketId: '124', question: d.draft.question, description: d.draft.resolutionCriteria } })
    expect(Array.from(d.draft.resolutionCriteria).length).toBeGreaterThan(2000)
    const f = predictions.createForecastFromDraft(s, d.draft)
    expect(f.linkedMarket).toEqual(d.draft.linkedMarket)
    expect(s.credits.used).toBe(used + 5)
  })

  it('persists the fresh server linked snapshot timestamp instead of the older client timestamp on creation', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-10-05T12:00:00Z'))
    const s = createState()
    const d = predictions.predictionDraftOf('123').draft
    const posted = { ...d, linkedMarket: { ...d.linkedMarket!, asOf: '2026-10-01T12:00:00.000Z' } }

    const f = predictions.createForecastFromDraft(s, posted)

    expect(s.forecasts.get(f.id)?.linkedMarket?.asOf).toBe('2026-10-05T12:00:00.000Z')
  })

  it('preserves the parent linked snapshot when an update validates a later same-day read', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-10-05T11:00:00Z'))
    const s = createState()
    const parent = predictions.createForecastFromDraft(s, predictions.predictionDraftOf('123').draft)
    parent.status = 'completed'
    parent.result = fx.forecastResult
    parent.completedAt = '2026-10-05T11:00:00.000Z'
    const original = { ...parent.linkedMarket! }
    vi.setSystemTime(new Date('2026-10-05T12:00:00Z'))

    const updated = predictions.updateForecastFromParent(s, parent.id)

    expect(updated.parentId).toBe(parent.id)
    expect(s.forecasts.get(updated.id)?.linkedMarket).toEqual(original)
    expect(updated.linkedMarket?.asOf).toBe('2026-10-05T11:00:00.000Z')
    expect(s.forecasts.get(parent.id)?.linkedMarket).toEqual(original)
  })

  it('re-reads the current canonical predicate before updating and refuses changed rules before charging', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-10-05T11:00:00Z'))
    const s = createState()
    const parent = predictions.createForecastFromDraft(s, predictions.predictionDraftOf('123').draft)
    parent.status = 'completed'
    parent.result = fx.forecastResult
    parent.completedAt = '2026-10-05T11:00:00.000Z'
    const before = { credits: s.credits.used, count: s.forecasts.size }
    // The fixture's current rules change with the day; the parent's tracking date is still future.
    vi.setSystemTime(new Date('2026-10-06T12:00:00Z'))

    let error: unknown
    try {
      predictions.updateForecastFromParent(s, parent.id)
    } catch (e) {
      error = e
    }

    expect(error).toMatchObject({ status: 409, code: 'conflict' })
    expect({ credits: s.credits.used, count: s.forecasts.size }).toEqual(before)
  })

  it('rejects inconsistent or changed linked predicates before spending or creating a report', () => {
    const s = createState()
    const before = { credits: s.credits.used, count: s.forecasts.size }
    const d = predictions.predictionDraftOf('123').draft
    expect(() => predictions.createForecastFromDraft(s, { ...d, question: 'An edited predicate that is different?' })).toThrow()
    expect(() => predictions.createForecastFromDraft(s, { ...d, linkedMarket: { ...d.linkedMarket!, description: 'Changed canonical rules' }, resolutionCriteria: 'Changed canonical rules' })).toThrow()
    expect({ credits: s.credits.used, count: s.forecasts.size }).toEqual(before)
  })

  it('attaches only the newest exact owned unresolved head and never leaks it to another session or decoy', () => {
    const owner = createState()
    const d = predictions.predictionDraftOf('123').draft
    owner.forecasts.set('mine', fx.forecast({ ...d, id: 'mine', createdAt: new Date().toISOString(), completedAt: new Date().toISOString() }))
    owner.forecasts.set('decoy', fx.forecast({ ...d, id: 'decoy', linkedMarket: { ...d.linkedMarket!, marketId: '777', conditionId: `0x${'d'.repeat(64)}` } }))
    const row = (s: ReturnType<typeof createState>) => predictions.predictionBoardOf(s).events.flatMap((e) => e.markets).find((m) => m.id === '123')!
    expect(row(owner).desk).toMatchObject({ forecastId: 'mine', p: 0.62, match: 'exact' })
    expect(row(createState()).desk).toBeNull()
    owner.forecasts.set('head', fx.forecast({ ...d, id: 'head', parentId: 'mine', createdAt: new Date().toISOString(), completedAt: new Date().toISOString(), result: { ...fx.forecastResult, probabilities: [{ outcome: 'Yes', p: 0.7 }, { outcome: 'No', p: 0.3 }] } }))
    expect(row(owner).desk).toMatchObject({ forecastId: 'head', p: 0.7 })
    owner.forecasts.get('head')!.resolution = { outcome: 'No', resolvedAt: new Date().toISOString(), by: 'manual' }
    expect(row(owner).desk).toBeNull()
  })

  it('creates a free notify-only direct leaf alert without a forecast and rejects event slug, mixed targets and crossed levels', () => {
    const s = createState()
    const d = predictions.predictionDraftOf('124')
    const used = s.credits.used
    const r = { symbol: '', trigger: 'odds' as const, marketSlug: d.market.marketSlug, marketId: d.market.id, op: 'above' as const, price: 0.6, action: 'notify' as const }
    expect(predictions.createOddsAlert(s, r).alert).toMatchObject({ forecastId: null, marketSlug: r.marketSlug, marketId: '124', linkedMarket: { marketId: '124' }, notional: null, market: { yesPrice: 0.2 } })
    expect(s.credits.used).toBe(used)
    expect(() => predictions.createOddsAlert(s, { ...r, marketSlug: d.event.slug })).toThrow()
    expect(() => predictions.createOddsAlert(s, { ...r, forecastId: 'f-seed' })).toThrow()
    expect(() => predictions.createOddsAlert(s, { ...r, price: 0.1 })).toThrow()
    expect(() => predictions.createOddsAlert(s, { ...r, marketId: '123' })).toThrow()
    expect(() => predictions.createOddsAlert(s, { ...r, notional: 100 })).toThrow()
    expect(() => predictions.createOddsAlert(s, { ...r, stopLoss: 0.1 })).toThrow()
    expect(() => predictions.createOddsAlert(s, { ...r, takeProfit: 0.7 })).toThrow()
  })

  it('does not create or charge a report after the daily quota is exhausted', () => {
    const s = createState()
    s.credits.used = s.credits.limit
    const count = s.forecasts.size
    expect(() => predictions.createForecastFromDraft(s, predictions.predictionDraftOf('123').draft)).toThrow()
    expect(s.forecasts.size).toBe(count)
    expect(s.credits.used).toBe(s.credits.limit)
  })
})

// The browser journeys trust these HTTP responses, including the router-state bodies returned by mutations.
describe('mock strategy forward contract', () => {
  let stop: () => Promise<void>
  beforeAll(async () => { stop = await globalSetup() })
  afterAll(async () => { await stop?.() })
  afterEach(() => { vi.useRealTimers() })

  function http<T>(path: string, body?: unknown, cookie?: string) {
    return new Promise<{ status: number; cookie: string | undefined; body: T }>((resolve, reject) => {
      const request = httpRequest(`${API_URL}${path}`, {
        method: body === undefined ? 'GET' : 'POST',
        headers: { 'Content-Type': 'application/json', ...(cookie ? { Cookie: cookie } : {}) },
      }, (response) => {
        let raw = ''
        response.setEncoding('utf8')
        response.on('data', (chunk) => { raw += chunk })
        response.on('end', () => resolve({
          status: response.statusCode!, cookie: response.headers['set-cookie']?.[0]?.split(';')[0], body: JSON.parse(raw) as T,
        }))
      })
      request.on('error', reject)
      request.end(body === undefined ? undefined : JSON.stringify(body))
    })
  }

  async function session() {
    const response = await http('/api/auth/login', DEMO)
    expect(response.status).toBe(200)
    const cookie = response.cookie!
    return async <T>(path: string, body?: unknown): Promise<T> => {
      const response = await http<T>(path, body, cookie)
      expect(response.status, path).toBeGreaterThanOrEqual(200)
      expect(response.status, path).toBeLessThan(300)
      return response.body
    }
  }

  describe('Universe contract and saved comparison', () => {
    it('does not invent pre-buy or future chart points, and withholds comparison outside retained history', async () => {
      at('2026-10-05T12:15:00Z')
      const request = await session()
      const built = await request<{ portfolio: AIPortfolio }>('/api/ai-portfolios/build', { prompt: 'Apple stock basket' })
      await request(`/api/ai-portfolios/${built.portfolio.id}/invest`, { amount: 1000 })
      const immediate = await request<{ portfolio: AIPortfolio }>(`/api/ai-portfolios/${built.portfolio.id}`)
      expect(immediate.portfolio.invested).toMatchObject({ series: null, benchmarkSeries: null, bars: 0, windowStart: null, windowEnd: null })
      at('2027-01-05T12:15:00Z')
      const later = await request<{ portfolio: AIPortfolio }>(`/api/ai-portfolios/${built.portfolio.id}`)
      expect(later.portfolio.invested).toMatchObject({ benchmarkSeries: null, benchmarkReturnPct: null, btcReturnPct: null, bars: 1000 })
      expect(later.portfolio.invested!.windowStart!).toBeGreaterThan(Date.parse(immediate.portfolio.invested!.firstAt) / 1000)
      expect(later.portfolio.invested!.windowEnd!).toBeLessThanOrEqual(Date.now() / 1000)
    })

    it('restores only the first matching build created since the action could be tapped', async () => {
      at('2026-10-05T12:00:00Z')
      const login = await http('/api/auth/login', DEMO)
      const message = 'Build a stock basket with Apple and Nvidia'
      await http('/api/ai-portfolios/build', { prompt: message }, login.cookie)
      at('2026-10-05T12:01:00Z')
      await new Promise<void>((resolve, reject) => {
        const r = httpRequest(`${API_URL}/api/chat/stream`, { method: 'POST', headers: { 'Content-Type': 'application/json', Cookie: login.cookie! } }, (res) => {
          res.resume(); res.on('end', resolve)
        }); r.on('error', reject); r.end(JSON.stringify({ message }))
      })
      const chats = await http<{ chats: { id: string }[] }>('/api/chats', undefined, login.cookie)
      const restore = () => http<{ chat: Chat }>(`/api/chats/${chats.body.chats[0].id}`, undefined, login.cookie)
      const action = (chat: Chat) => chat.messages.flatMap((m) => m.cards).find((c) => c.type === 'build_action')!
      const before = action((await restore()).body.chat)
      expect(before.type === 'build_action' && before.data.result).toBeNull()
      const first = await http<{ portfolio: AIPortfolio }>('/api/ai-portfolios/build', { prompt: message }, login.cookie)
      at('2026-10-05T12:02:00Z')
      await http('/api/ai-portfolios/build', { prompt: message }, login.cookie)
      const after = action((await restore()).body.chat)
      expect(after.type === 'build_action' && after.data.result?.id).toBe(first.body.portfolio.id)
    })

    it('serves the exact live mock classes and HIP3 identities, with distinct mark and retained closes', async () => {
      const request = await session()
      const { assets } = await request<{ assets: Asset[] }>('/api/market/assets')
      const perps = assets.filter((a) => a.venue !== 'yahoo')
      expect(perps.filter((a) => a.class === 'crypto')).toHaveLength(69)
      expect(perps.filter((a) => a.class === 'stock')).toHaveLength(36)
      expect(perps.filter((a) => a.class === 'index')).toHaveLength(2)
      expect(perps.filter((a) => a.region === 'asia').map((a) => a.symbol)).toEqual(['TSM', 'BABA', 'SMSN', 'SKHX', 'HYUNDAI', 'SOFTBANK', 'MINIMAX', 'ZHIPU', 'EWJ', 'EWY',
        'SKHY', 'CXMT', 'UNITREE', 'GIGADEV', 'KIOXIA', 'EWT', 'KSTR'])
      expect(perps.filter((a) => a.region === 'us' && a.class === 'stock')).toHaveLength(19)
      // The quote feeds follow every perp: HSDT under US, the ADRs and local listings under Asia, each with its quote.
      const feeds = assets.slice(perps.length)
      expect(feeds).toHaveLength(138)
      expect(feeds.every((a) => a.venue === 'yahoo' && a.class === 'stock' && a.quoteFeed?.symbol === a.pair)).toBe(true)
      expect(feeds.filter((a) => a.region === 'us').map((a) => a.symbol)).toEqual(['HSDT'])
      expect(feeds.find((a) => a.symbol === 'DBS')).toMatchObject({ pair: 'D05.SI', sessionVenue: 'SGX', localCurrency: 'SGD', quoteFeed: { fxSymbol: 'SGDUSD=X', marketState: 'closed' } })
      expect(feeds.find((a) => a.symbol === 'JARDINE')).toMatchObject({ pair: 'J36.SI', sessionVenue: 'SGX', localCurrency: 'USD', quoteFeed: { fx: 1, fxSymbol: '' } })
      const apple = assets.find((a) => a.symbol === 'AAPL')!
      expect(apple).toMatchObject({ pair: 'xyz:AAPL', venue: 'hyperliquid', session: 'closed', range24hSource: 'hourly_candles', region: 'us', sessionVenue: 'NYSE', localCurrency: 'USD' })
      expect(assets.find((a) => a.symbol === 'TSM')).toMatchObject({ pair: 'xyz:TSM', name: 'TSMC (Taiwan Semiconductor)', class: 'stock', region: 'asia', sessionVenue: 'NYSE', localCurrency: 'USD' })
      expect(assets.find((a) => a.symbol === 'SOFTBANK')).toMatchObject({ sessionVenue: 'TSE', localCurrency: 'JPY' })
      expect(assets.find((a) => a.symbol === 'CXMT')).toMatchObject({ pair: 'xyz:CXMT', region: 'asia', sessionVenue: 'SSE', localCurrency: 'CNY' })
      expect(assets.find((a) => a.symbol === 'PLTR')).toMatchObject({ pair: 'xyz:PLTR', region: 'us', sessionVenue: 'NYSE', localCurrency: 'USD' })
      const { candles: cs } = await request<{ candles: Candle[] }>('/api/market/candles?symbol=AAPL&interval=1h&limit=500')
      expect(cs.at(-1)!.c).not.toBe(apple.price)
      const index = await request<{ candles: Candle[] }>('/api/market/candles?symbol=SP500&interval=1d&limit=500')
      expect(index.candles).toHaveLength(201)
      const native = await request<{ candles: Candle[] }>('/api/market/candles?symbol=6501.T&interval=1d&limit=50&currency=original')
      expect(native.candles.at(-1)!.c).toBeCloseTo(4726)
      const recent = await request<{ candles: Candle[] }>('/api/market/candles?symbol=BTC&interval=1h&limit=500')
      const earliest = recent.candles[0].t
      const older = await request<{ candles: Candle[] }>(`/api/market/candles?symbol=BTC&interval=1h&limit=500&before=${earliest}`)
      expect(older.candles.at(-1)!.t).toBe(earliest - 3600)
      expect(older.candles).toHaveLength(500)
    })

    it.each([
      ['1w', ['2024-02-12', '2024-02-19', '2024-02-26'], ['2024-01-29', '2024-02-05']],
      ['1M', ['2024-01-01', '2024-02-01', '2024-03-01'], ['2023-11-01', '2023-12-01']],
    ])('serves %s chart candles, original currency and exclusive historical pages over HTTP', async (interval, recentDates, olderDates) => {
      vi.useFakeTimers({ toFake: ['Date'] })
      vi.setSystemTime(new Date('2024-03-03T12:34:56Z'))
      const request = await session()
      const recent = await request<{ candles: Candle[] }>(`/api/market/candles?symbol=BTC&interval=${interval}&limit=3`)
      expect(recent.candles.map((c) => new Date(c.t * 1000).toISOString().slice(0, 10))).toEqual(recentDates)
      const older = await request<{ candles: Candle[] }>(`/api/market/candles?symbol=BTC&interval=${interval}&limit=2&before=${recent.candles[0].t}`)
      expect(older.candles.map((c) => new Date(c.t * 1000).toISOString().slice(0, 10))).toEqual(olderDates)
      const native = await request<{ candles: Candle[] }>(`/api/market/candles?symbol=6501.T&interval=${interval}&limit=3&currency=original`)
      expect(native.candles.at(-1)!.c).toBeCloseTo(4726)
    })

    it('serves the catalogue and the listed rows by scope, as the API does (contract §2)', async () => {
      const request = await session()
      const { instruments } = await request<{ instruments: Instrument[] }>('/api/market/instruments')
      expect(instruments).toHaveLength(248)
      expect(instruments.filter((r) => r.listed).map((r) => r.symbol)).toEqual(['6501.T', '000810.KS', 'BBNI.JK'])
      const symbols = async (q: string) => (await request<{ assets: Asset[] }>(`/api/market/assets${q}`)).assets.map((a) => a.symbol)
      expect(await symbols('')).toHaveLength(245) // no scope: the named rows only, as before
      expect(await symbols('?scope=named')).toHaveLength(245)
      expect((await symbols('?scope=named&with=6501.t,NOPE')).slice(244)).toEqual(['LPL', '6501.T'])
      expect(await symbols('?scope=listed')).toEqual(['6501.T', '000810.KS', 'BBNI.JK'])
      expect((await symbols('?scope=named&with=BBNI.JK,6501.T,DBS,MER.PS')).slice(245)).toEqual(['BBNI.JK', '6501.T']) // in its order; the rest ignored
      const { cookie } = await http('/api/auth/login', DEMO)
      for (const q of ['?scope=all', '?with=6501.T', `?scope=named&with=${Array.from({ length: 21 }, (_, i) => `${7000 + i}.T`).join(',')}`])
        expect((await http(`/api/market/assets${q}`, undefined, cookie)).status, q).toBe(400)
      const bought = await request<{ trade: Trade }>('/api/orders', { symbol: '6501.T', side: 'buy', notional: 500, source: 'manual' })
      expect(bought.trade).toMatchObject({ symbol: '6501.T', side: 'buy' })
      const { ath } = await request<{ ath: { symbol: string; ticker: string; currency: string } }>('/api/alerts/ath?symbol=6501.T')
      expect(ath).toMatchObject({ symbol: '6501.T', ticker: '6501.T', currency: 'JPY' })
    })

    it('persists SP500 metadata through builds, top-ups, list and invested detail', async () => {
      const request = await session()
      const built = await request<{ portfolio: AIPortfolio }>('/api/ai-portfolios/build', { prompt: 'A majority stock basket with Apple and Nvidia' })
      expect(built.portfolio.backtest).toMatchObject({ benchmarkSymbol: 'SP500', benchmarkLabel: 'S&P 500 perp', bars: 97 })
      expect(built.portfolio.backtest.series).toHaveLength(97)
      expect(built.portfolio.backtest.windowStart).toBe(built.portfolio.backtest.series[0].t)
      await request(`/api/ai-portfolios/${built.portfolio.id}/invest`, { amount: 1000 })
      await request(`/api/ai-portfolios/${built.portfolio.id}/invest`, { amount: 1000 })
      const listed = await request<{ portfolios: AIPortfolio[] }>('/api/ai-portfolios')
      expect(listed.portfolios[0].invested).toMatchObject({ benchmarkSymbol: 'SP500', btcReturnPct: null, benchmarkReturnPct: 0, benchmarkSeries: null, windowStart: null, windowEnd: null, bars: 0 })
      const detail = await request<{ portfolio: AIPortfolio }>(`/api/ai-portfolios/${built.portfolio.id}`)
      expect(detail.portfolio.invested?.benchmarkSymbol).toBe('SP500')
      expect(detail.portfolio.invested?.btcSeries).toBeNull()
    })

    it('keeps portfolio metadata consistent from the real mock SSE card to restored GET cards', async () => {
      const login = await http('/api/auth/login', DEMO)
      const message = 'Build a stock basket with Apple and Nvidia'
      const raw = await new Promise<string>((resolve, reject) => {
        const r = httpRequest(`${API_URL}/api/chat/stream`, { method: 'POST', headers: { 'Content-Type': 'application/json', Cookie: login.cookie! } }, (res) => {
          let body = ''; res.setEncoding('utf8'); res.on('data', (chunk) => { body += chunk }); res.on('end', () => resolve(body))
        }); r.on('error', reject); r.end(JSON.stringify({ message }))
      })
      const frames = raw.split('\n\n').filter((f) => f.startsWith('event: card\n')).map((f) => JSON.parse(f.slice(f.indexOf('data: ') + 6)) as Card)
      const card = frames.find((c) => c.type === 'build_action')!
      expect(card.type).toBe('build_action')
      if (card.type !== 'build_action') throw new Error('Expected portfolio action')
      expect(card.data.result).toBeNull()
      const built = await http<{ portfolio: AIPortfolio }>('/api/ai-portfolios/build', { prompt: card.data.prompt }, login.cookie)
      const chats = await http<{ chats: { id: string }[] }>('/api/chats', undefined, login.cookie)
      const restored = await http<{ chat: Chat }>(`/api/chats/${chats.body.chats[0].id}`, undefined, login.cookie)
      const result = restored.body.chat.messages.flatMap((m) => m.cards).find((c) => c.type === 'build_action')!
      expect(result.type).toBe('build_action')
      if (result.type !== 'build_action') throw new Error('Expected restored portfolio action')
      expect(result.data.result).toMatchObject({ id: built.body.portfolio.id, benchmarkSymbol: 'SP500', benchmarkLabel: 'S&P 500 perp', bars: 97, windowStart: built.body.portfolio.backtest.windowStart, windowEnd: built.body.portfolio.backtest.windowEnd })
    })

    it('refuses a stock Binance price rule before spending or storing', async () => {
      const login = await http('/api/auth/login', DEMO)
      const draft: ForecastDraft = { shape: 'binary', question: 'Will Apple close above $250?', outcomes: ['Yes', 'No'], resolutionCriteria: 'Manual future resolution.', resolutionDate: '2099-12-31', category: 'markets', priceRule: { symbol: 'AAPL', op: 'above', threshold: 250, mode: 'on_date' } }
      const response = await http('/api/forecasts', draft, login.cookie)
      expect(response.status).toBe(400)
      const me = await http<{ credits: { used: number } }>('/api/me', undefined, login.cookie)
      expect(me.body.credits.used).toBe(fx.credits.used)
    })
  })

  describe('forecast materials', () => {
    const draft: ForecastDraft = { shape: 'binary', question: 'Will ETH ETF inflows top $2bn in October?', outcomes: ['Yes', 'No'], resolutionCriteria: 'Farside data.', resolutionDate: '2099-12-31', category: 'crypto', priceRule: null }
    const b64 = (bytes: Uint8Array | string) => Buffer.from(bytes).toString('base64')
    const pdf = { kind: 'document', mediaType: 'application/pdf', name: 'note.pdf', data: b64(PDF_BYTES) }
    const csv = { kind: 'document', mediaType: 'text/csv', name: 'unreadable.csv', data: b64(CSV_TEXT) }
    const used = async (cookie?: string) => (await http<{ credits: { used: number } }>('/api/me', undefined, cookie)).body.credits.used

    it('refuses bad files before spending, charges 5 + 1 per file, reads them, refunds an unread one, then reuses and keeps digests', async () => {
      const { cookie } = await http('/api/auth/login', DEMO)
      const start = await used(cookie)
      for (const attachments of [[pdf, pdf, pdf, pdf, pdf, pdf], [{ ...pdf, data: b64('hello') }], [{ ...pdf, name: 'note.csv' }]])
        expect((await http('/api/forecasts', { ...draft, attachments }, cookie)).status).toBe(400)
      expect(await used(cookie)).toBe(start)
      const created = await http<{ forecast: Forecast }>('/api/forecasts', { ...draft, attachments: [pdf, csv] }, cookie)
      expect(created.status).toBe(201)
      expect(created.body.forecast.materials?.map((m) => [m.id, m.name, m.status])).toEqual([['D1', 'note.pdf', 'pending'], ['D2', 'unreadable.csv', 'pending']])
      expect(created.body.forecast).not.toHaveProperty('attachments')
      expect(await used(cookie)).toBe(start + 7)
      const id = created.body.forecast.id
      await http(`/api/forecasts/${id}`, undefined, cookie)
      const done = (await http<{ forecast: Forecast }>(`/api/forecasts/${id}`, undefined, cookie)).body.forecast
      expect(done.materials?.map((m) => m.status)).toEqual(['read', 'failed'])
      expect(done.result?.findings?.filter((f) => f.origin === 'material').map((f) => f.sourceIds)).toEqual([['D1']])
      expect(done.result?.evidence.at(-1)).toMatchObject({ materialId: 'D1', url: null })
      expect(done.result?.flags.at(-1)?.note).toContain('unreadable.csv')
      expect(await used(cookie)).toBe(start + 6) // the unread file is refunded

      const other = await http('/api/forecasts', { ...draft, question: 'Something else entirely?', reuseMaterials: { forecastId: id } }, cookie)
      expect(other.status).toBe(400)
      const again = await http<{ forecast: Forecast }>('/api/forecasts', { ...draft, reuseMaterials: { forecastId: id } }, cookie)
      expect(again.body.forecast.materials?.map((m) => [m.id, m.status, m.reusedFrom])).toEqual([['D1', 'read', id]])
      expect(await used(cookie)).toBe(start + 11)

      expect((await http(`/api/forecasts/${id}/update`, { keepMaterials: ['D2'] }, cookie)).status).toBe(400) // D2 was never read
      const update = await http<{ forecast: Forecast }>(`/api/forecasts/${id}/update`, { attachments: [{ ...pdf, name: 'fresh.pdf' }] }, cookie)
      expect(update.body.forecast.materials?.map((m) => [m.id, m.name, m.status])).toEqual([['D1', 'note.pdf', 'read'], ['D2', 'fresh.pdf', 'pending']])
      expect(await used(cookie)).toBe(start + 17)
    })

    it('serves the fixed materials report and the run still reading', async () => {
      const { cookie } = await http('/api/auth/login', DEMO)
      const report = (await http<{ forecast: Forecast }>(`/api/forecasts/${mock.MATERIALS_FORECAST}`, undefined, cookie)).body.forecast
      expect(report.materials?.map((m) => m.status)).toEqual(['read', 'read', 'failed'])
      const ids = new Set(report.result?.findings?.map((f) => f.id))
      expect(ids.size).toBe(report.result?.findings?.length)
      const running = (await http<{ forecast: Forecast }>(`/api/forecasts/${mock.RUNNING_MATERIALS_FORECAST}`, undefined, cookie)).body.forecast
      expect([running.status, running.progress?.materials?.status, running.materials?.map((m) => m.status)]).toEqual(['running', 'running', ['pending', 'read', 'pending']])
    })
  })

  function at(instant: string) {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date(instant))
  }

  const generateBody = { prompt: 'PRIVATE: buy SOL on the 20/50 EMA cross', symbol: 'SOL', interval: '4h' }

  // Omitting forward from either projection would make a board or a directly opened detail silently lose evidence.
  it.each(['mine', 'community', 'top'] as const)('returns contract-shaped forward in every %s summary', async (scope) => {
    const request = await session()
    await request('/api/strategies/generate', generateBody)
    const { strategies } = await request<{ strategies: StrategySummary[] }>(`/api/strategies?scope=${scope}`)
    expect(strategies.length).toBeGreaterThan(0)
    for (const strategy of strategies) {
      expect(strategy.forward, strategy.id).toEqual(expect.objectContaining({
        since: expect.any(String), basis: scope === 'mine' ? 'saved' : scope === 'top' ? 'launch' : 'published',
        metrics: expect.toSatisfy((value: unknown) => value === null || typeof value === 'object'),
        early: expect.any(Boolean), asOf: expect.toSatisfy((value: unknown) => value === null || typeof value === 'string'),
        stale: expect.any(Boolean), reason: expect.toSatisfy((value: unknown) =>
          value === null || ['too_new', 'history_unavailable', 'market_unavailable', 'timeout', 'busy'].includes(String(value))),
      }))
      expect(strategy.forward).not.toHaveProperty('equity')
      expect(strategy.forward).not.toHaveProperty('benchmark')
      expect(strategy.forward).not.toHaveProperty('trades')
    }
  })

  it.each(['mine', 'community', 'top'] as const)('returns contract-shaped forward on every %s full detail', async (scope) => {
    const request = await session()
    await request('/api/strategies/generate', generateBody)
    const { strategies } = await request<{ strategies: StrategySummary[] }>(`/api/strategies?scope=${scope}`)
    expect(strategies.length).toBeGreaterThan(0)
    for (const summary of strategies) {
      const { strategy } = await request<{ strategy: Strategy }>(`/api/strategies/${summary.id}`)
      expect(strategy.forward, summary.id).toEqual(expect.objectContaining({
        since: expect.any(String), basis: scope === 'mine' ? 'saved' : scope === 'top' ? 'launch' : 'published',
        metrics: expect.toSatisfy((value: unknown) => value === null || typeof value === 'object'),
        early: expect.any(Boolean), asOf: expect.toSatisfy((value: unknown) => value === null || typeof value === 'string'),
        stale: expect.any(Boolean), reason: expect.toSatisfy((value: unknown) =>
          value === null || ['too_new', 'history_unavailable', 'market_unavailable', 'timeout', 'busy'].includes(String(value))),
        equity: expect.any(Array), benchmark: expect.any(Array), trades: expect.any(Array),
      }))
      const fixedAt = strategy.forward!.basis === 'saved' ? strategy.createdAt : strategy.forward!.since
      expect(strategy.metrics.endT, `${summary.id}: saved backtest ends before the forward boundary`).toBeLessThan(Date.parse(fixedAt) / 1000)
      if (strategy.forward?.metrics === null) {
        expect(strategy.forward).toMatchObject({ early: true, equity: [], benchmark: [], trades: [] })
      }
    }
  })

  it('keeps another owner’s published prompt private in full community detail', async () => {
    const request = await session()
    const { strategy } = await request<{ strategy: Strategy }>('/api/strategies/c-eth-breakout')
    expect(strategy.community).toMatchObject({ mine: false, published: true })
    expect(strategy.prompt).toBe('')
  })

  it.each([
    ['lab-sol-ema', 2, '2026-10-03T00:00:00Z'],
    ['lab-eth-rsi', 2, '2026-10-03T00:00:00Z'],
    ['c-eth-breakout', 5, '2026-09-01T03:00:00Z'],
    ['c-sol-rsi', 4, '2026-09-01T04:00:00Z'],
  ] as const)('retains the measured closed forward trades for %s', async (id, count, firstEntry) => {
    const request = await session()
    const { strategy } = await request<{ strategy: Strategy }>(`/api/strategies/${id}`)
    const forward = strategy.forward!
    expect(forward.metrics!.trades).toBe(count)
    expect(forward.trades).toHaveLength(count)
    expect(forward.trades[0].entryT).toBe(Date.parse(firstEntry) / 1000)
    for (const trade of forward.trades) {
      expect(trade.entryT).toBeGreaterThanOrEqual(Date.parse(forward.since) / 1000)
      expect(trade.exitT).toBeGreaterThanOrEqual(trade.entryT)
      expect(trade.exitT).toBeLessThanOrEqual(forward.metrics!.endT)
    }
    expect(forward.trades.map((trade) => trade.entryT)).toEqual([...forward.trades].map((trade) => trade.entryT).sort((a, b) => a - b))
  })

  it.each([
    { id: 'c-eth-breakout', scope: 'community', since: '2026-09-01T02:30:00.123Z', basis: 'published',
      asOf: '2026-10-05T00:00:00Z', stale: false, reason: null, early: false,
      metrics: { totalReturnPct: 4.2, buyHoldReturnPct: 1.3, trades: 5 } },
    { id: 'c-sol-rsi', scope: 'community', since: '2026-09-01T02:30:00.123Z', basis: 'published',
      asOf: '2026-10-01T00:00:00Z', stale: true, reason: 'market_unavailable', early: true,
      metrics: { totalReturnPct: 2.4, buyHoldReturnPct: 3.1, trades: 4 } },
    { id: 'c-btc-dip', scope: 'community', since: '2026-09-01T02:30:00.123Z', basis: 'published',
      asOf: '2026-10-05T00:00:00Z', stale: false, reason: 'history_unavailable', early: true, metrics: null },
    { id: 'lab-btc-donchian', scope: 'top', since: '2026-10-03T00:00:00Z', basis: 'launch',
      asOf: null, stale: false, reason: 'too_new', early: true, metrics: null },
  ])('preserves source state for $id in both full and summary responses', async ({ id, scope, ...expected }) => {
    const request = await session()
    const { strategy } = await request<{ strategy: Strategy }>(`/api/strategies/${id}`)
    const { strategies } = await request<{ strategies: StrategySummary[] }>(`/api/strategies?scope=${scope}`)
    const summary = strategies.find((strategy) => strategy.id === id)
    expect(summary, `${id} is listed`).toBeDefined()
    expect(strategy.forward).toMatchObject(expected)
    expect(summary!.forward).toMatchObject(expected)
    if (expected.metrics === null) expect(strategy.forward).toMatchObject({ equity: [], benchmark: [], trades: [] })
    else {
      expect(strategy.forward!.equity.length).toBeGreaterThan(1)
      expect(strategy.forward!.benchmark.length).toBeGreaterThan(1)
      expect(strategy.forward!.equity[0].v).toBe(100)
      expect(strategy.forward!.benchmark[0].v).toBe(100)
    }
  })

  it('seeds generate with the exact saved boundary and an honest unavailable run', async () => {
    at('2026-10-05T10:07:30.123Z')
    const request = await session()
    const { strategy } = await request<{ strategy: Strategy }>('/api/strategies/generate', generateBody)
    expect(strategy.createdAt).toBe('2026-10-05T10:07:30.123Z')
    expect(strategy.metrics.endT).toBeLessThan(Date.parse('2026-10-05T10:07:30.123Z') / 1000)
    expect(strategy.forward).toEqual({
      since: '2026-10-05T10:07:30.123Z', basis: 'saved', metrics: null, early: true,
      asOf: null, stale: false, reason: 'too_new', equity: [], benchmark: [], trades: [],
    })
    const detail = await request<{ strategy: Strategy }>(`/api/strategies/${strategy.id}`)
    expect(detail.strategy.forward).toEqual({
      since: '2026-10-05T10:07:30.123Z', basis: 'saved', metrics: null, early: true,
      asOf: null, stale: false, reason: 'too_new', equity: [], benchmark: [], trades: [],
    })
  })

  it('replaces the saved forward boundary with the exact publish instant', async () => {
    at('2026-10-05T10:07:30.123Z')
    const request = await session()
    const { strategy } = await request<{ strategy: Strategy }>('/api/strategies/generate', generateBody)
    vi.setSystemTime(new Date('2026-10-06T11:08:40.456Z'))
    const published = await request<{ strategy: Strategy }>(`/api/strategies/${strategy.id}/publish`, { published: true })
    expect(published.strategy.community?.publishedAt).toBe('2026-10-06T11:08:40.456Z')
    expect(published.strategy.forward).toEqual({
      since: '2026-10-06T11:08:40.456Z', basis: 'published', metrics: null, early: true,
      asOf: null, stale: false, reason: 'too_new', equity: [], benchmark: [], trades: [],
    })
    const { strategies } = await request<{ strategies: StrategySummary[] }>('/api/strategies?scope=community')
    expect(strategies.find((s) => s.id === strategy.id)?.forward).toEqual({
      since: '2026-10-06T11:08:40.456Z', basis: 'published', metrics: null, early: true,
      asOf: null, stale: false, reason: 'too_new',
    })
  })

  it('restores the original saved boundary on unpublish', async () => {
    at('2026-10-05T10:07:30.123Z')
    const request = await session()
    const { strategy } = await request<{ strategy: Strategy }>('/api/strategies/generate', generateBody)
    vi.setSystemTime(new Date('2026-10-05T11:08:40.456Z'))
    await request(`/api/strategies/${strategy.id}/publish`, { published: true })
    vi.setSystemTime(new Date('2026-10-05T12:09:50.789Z'))
    const unpublished = await request<{ strategy: Strategy }>(`/api/strategies/${strategy.id}/publish`, { published: false })
    expect(unpublished.strategy.community).toMatchObject({ published: false, publishedAt: null })
    expect(unpublished.strategy.forward).toEqual({
      since: '2026-10-05T10:07:30.123Z', basis: 'saved', metrics: null, early: true,
      asOf: null, stale: false, reason: 'too_new', equity: [], benchmark: [], trades: [],
    })
    const { strategies } = await request<{ strategies: StrategySummary[] }>('/api/strategies?scope=community')
    expect(strategies.some((s) => s.id === strategy.id)).toBe(false)
  })

  it('uses a new exact boundary when an unpublished strategy is published again', async () => {
    at('2026-10-05T10:07:30.123Z')
    const request = await session()
    const { strategy } = await request<{ strategy: Strategy }>('/api/strategies/generate', generateBody)
    vi.setSystemTime(new Date('2026-10-05T11:08:40.456Z'))
    await request(`/api/strategies/${strategy.id}/publish`, { published: true })
    await request(`/api/strategies/${strategy.id}/publish`, { published: false })
    vi.setSystemTime(new Date('2026-10-05T12:09:50.789Z'))
    const republished = await request<{ strategy: Strategy }>(`/api/strategies/${strategy.id}/publish`, { published: true })
    expect(republished.strategy.community?.publishedAt).toBe('2026-10-05T12:09:50.789Z')
    expect(republished.strategy.forward).toMatchObject({ since: '2026-10-05T12:09:50.789Z', basis: 'published' })
  })

  it.each([
    ['Labs', ''],
    ['community', ''],
    ['own', 'PRIVATE: buy SOL on the 20/50 EMA cross'],
  ] as const)('starts a %s copy at the new save instant with prompt privacy scoped to source ownership', async (source, expectedPrompt) => {
    at('2026-10-05T10:07:30.123Z')
    const request = await session()
    const id = source === 'Labs' ? 'lab-sol-ema' : source === 'community' ? 'c-eth-breakout'
      : (await request<{ strategy: Strategy }>('/api/strategies/generate', generateBody)).strategy.id
    vi.setSystemTime(new Date('2026-10-06T11:08:40.456Z'))
    const { strategy } = await request<{ strategy: Strategy }>(`/api/strategies/${id}/copy`, {})
    expect(strategy.id).not.toBe(id)
    expect(strategy.prompt).toBe(expectedPrompt)
    expect(strategy.createdAt).toBe('2026-10-06T11:08:40.456Z')
    expect(strategy.metrics.endT).toBeLessThan(Date.parse('2026-10-06T11:08:40.456Z') / 1000)
    expect(strategy.community).toMatchObject({ mine: true, published: false, publishedAt: null, copiedFrom: { id } })
    expect(strategy.forward).toEqual({
      since: '2026-10-06T11:08:40.456Z', basis: 'saved', metrics: null, early: true,
      asOf: null, stale: false, reason: 'too_new', equity: [], benchmark: [], trades: [],
    })
    const detail = await request<{ strategy: Strategy }>(`/api/strategies/${strategy.id}`)
    expect(detail.strategy.prompt).toBe(expectedPrompt)
    expect(detail.strategy.forward).toMatchObject({ since: '2026-10-06T11:08:40.456Z', basis: 'saved', equity: [], benchmark: [], trades: [] })
  })
})

// These HTTP checks prove caller projection and SSE wiring, never mock statistical math or model policy.
describe('portfolio review HTTP contract', () => {
  it('keeps overlap detail-only and caller-local across two sessions, reads and reset', async () => {
    const stop = await globalSetup()
    try {
      const http = await import('node:http')
      async function call(path: string, cookie = '', data?: unknown) {
        return new Promise<{ body: any; cookie: string; status: number }>((resolve, reject) => {
          const req = http.request(`${API_URL}${path}`, { agent: false, method: data ? 'POST' : 'GET', headers: { Cookie: cookie, 'Content-Type': 'application/json' } }, (res) => { let body = ''; res.on('data', (x) => body += x); res.on('end', () => resolve({ body: body ? JSON.parse(body) : null, cookie: res.headers['set-cookie']?.[0]?.split(';')[0] ?? cookie, status: res.statusCode! })) }); req.on('error', reject); if (data) req.write(JSON.stringify(data)); req.end()
        })
      }
      const a = await call('/api/auth/login', '', DEMO); const b = await call('/api/auth/login', '', DEMO);
      const pa = (await call('/api/ai-portfolios/build', a.cookie, { prompt: 'test core basket' })).body.portfolio
      const pb = (await call('/api/ai-portfolios/build', b.cookie, { prompt: 'test core basket' })).body.portfolio
      expect(pa.overlap).toBeNull(); expect(pb.overlap).toBeNull()
      await call('/api/orders', a.cookie, { symbol: 'BTC', side: 'sell', notional: 2000 })
      const first = (await call(`/api/ai-portfolios/${pa.id}`, a.cookie)).body.portfolio
      const second = (await call(`/api/ai-portfolios/${pb.id}`, b.cookie)).body.portfolio
      expect(first.overlap.holdings).toEqual(expect.arrayContaining([expect.objectContaining({ symbol: 'BTC', side: 'short', basketPct: 50 })])); expect(second.overlap.holdings).not.toEqual(first.overlap.holdings)
      expect((await call(`/api/ai-portfolios/${pa.id}`, b.cookie)).status).toBe(404)
      expect((await call('/api/ai-portfolios', a.cookie)).body.portfolios[0].overlap).toBeNull()
      expect((await call(`/api/ai-portfolios/${pa.id}`, a.cookie)).body.portfolio.overlap.holdings).toEqual(first.overlap.holdings)
      await call('/api/portfolio/reset', a.cookie, {})
      const reset = (await call(`/api/ai-portfolios/${pa.id}`, a.cookie)).body.portfolio.overlap
      expect(reset.holdings).toEqual([]); expect(reset.revision).toBeGreaterThan(first.overlap.revision)
    } finally { await stop() }
  })
  it('streams real review tool frames with unknown facts and no forced execution/card', async () => {
    const stop = await globalSetup()
    try {
      const http = await import('node:http')
      const request = (path: string, cookie: string, data: unknown) => new Promise<{ text: string; cookie: string }>((resolve, reject) => { const req = http.request(`${API_URL}${path}`, { agent: false, method: 'POST', headers: { Cookie: cookie, 'Content-Type': 'application/json' } }, (res) => { let text = ''; res.on('data', (x) => text += x); res.on('end', () => resolve({ text, cookie: res.headers['set-cookie']?.[0]?.split(';')[0] ?? cookie })) }); req.on('error', reject); req.end(JSON.stringify(data)) })
      const login = await request('/api/auth/login', '', DEMO)
      const response = await request('/api/chat/stream', login.cookie, { message: 'Review my paper portfolio: concentration and coverage.' })
      expect(response.text).toContain('"name":"review_portfolio"'); expect(response.text).toContain('Reviewing your portfolio'); expect(response.text).toContain('event: tool_result'); expect(response.text).toContain('daily history unavailable'); expect(response.text).not.toContain('event: card'); expect(response.text).toContain(`"used":${18 + 2}`)
    } finally { await stop() }
  })
})

describe('portfolio review optional confirmation', () => {
  it('emits one warranted ordinary card without executing it, then fills only on confirmation', async () => {
    const stop = await globalSetup()
    try {
      const http = await import('node:http')
      const call = (path: string, cookie: string, data?: unknown) => new Promise<{ text: string; cookie: string }>((resolve, reject) => {
        const req = http.request(`${API_URL}${path}`, { agent: false, method: data ? 'POST' : 'GET', headers: { Cookie: cookie, 'Content-Type': 'application/json' } }, (res) => { let text = ''; res.on('data', (x) => text += x); res.on('end', () => resolve({ text, cookie: res.headers['set-cookie']?.[0]?.split(';')[0] ?? cookie })) }); req.on('error', reject); if (data) req.write(JSON.stringify(data)); req.end()
      })
      const { cookie } = await call('/api/auth/login', '', DEMO)
      const before = JSON.parse((await call('/api/portfolio/trades', cookie)).text).trades
      const turn = await call('/api/chat/stream', cookie, { message: 'Review my paper portfolio: warranted trim fixture' })
      const frames = turn.text.split('\n\n').filter((x) => x.startsWith('event: card'))
      expect(frames).toHaveLength(1)
      const card = JSON.parse(frames[0].split('data: ')[1]); expect(card.type).toBe('trade_proposal')
      expect(JSON.parse((await call('/api/portfolio/trades', cookie)).text).trades).toEqual(before)
      const fill = await call('/api/orders', cookie, { symbol: card.data.symbol, side: card.data.side, notional: card.data.notional, proposalId: card.data.proposalId, source: 'agent' })
      expect(JSON.parse(fill.text).trade.symbol).toBe('ETH'); expect(JSON.parse((await call('/api/portfolio/trades', cookie)).text).trades).toHaveLength(before.length + 1)
    } finally { await stop() }
  })
})

describe('combined live, prediction and portfolio snapshots', () => {
  it('advances health and private overlap revisions after both a copied fill and an outcome stake', async () => {
    const stop = await globalSetup()
    try {
      let cookie = ''
      async function call<T>(path: string, body?: unknown): Promise<T> {
        return new Promise((resolve, reject) => {
          const request = httpRequest(`${API_URL}${path}`, {
            agent: false, method: body === undefined ? 'GET' : 'POST',
            headers: { Cookie: cookie, 'Content-Type': 'application/json' },
          }, (response) => {
            let raw = ''
            response.setEncoding('utf8')
            response.on('data', (chunk) => { raw += chunk })
            response.on('end', () => {
              cookie = response.headers['set-cookie']?.[0]?.split(';')[0] ?? cookie
              expect(response.statusCode, path).toBeGreaterThanOrEqual(200)
              expect(response.statusCode, path).toBeLessThan(300)
              resolve(JSON.parse(raw) as T)
            })
          })
          request.on('error', reject)
          request.end(body === undefined ? undefined : JSON.stringify(body))
        })
      }
      await call('/api/auth/login', DEMO)
      const initial = await call<PortfolioReview>('/api/portfolio/review')
      const { plan } = await call<{ plan: CopyPlan }>('/api/arena/live/agents/live-momentum/copy', { ratio: .1, maxUsd: 100 })
      await call('/__e2e/live-step', { step: 27 })
      const copied = await call<PortfolioReview>('/api/portfolio/review')
      expect(copied.revision).toBeGreaterThan(initial.revision)
      expect(copied.holdings.map((h) => h.symbol)).toContain('BTC')
      const { portfolio: staked } = await call<{ portfolio: Portfolio }>('/api/forecasts/f-seed/market-position', { side: 'yes', stake: 100 })
      expect(staked.revision).toBe(copied.revision + 1)
      expect(staked.predictionStakes).toBe(100)
      const reviewed = await call<PortfolioReview>('/api/portfolio/review')
      const detail = await call<{ portfolio: AIPortfolio }>('/api/ai-portfolios/p-seed')
      expect(reviewed.revision).toBe(staked.revision)
      expect(reviewed.predictionStakes).toBe(100)
      expect(reviewed.betaBtc).toBeNull()
      expect(detail.portfolio.overlap?.revision).toBe(staked.revision)
      expect(detail.portfolio.overlap?.holdings).toEqual(expect.arrayContaining([expect.objectContaining({ symbol: 'BTC', basketPct: 50, shared: true })]))
      await call('/api/forecasts/f-seed/resolve', { outcome: 'Yes' })
      const { portfolio: settled } = await call<{ portfolio: Portfolio }>('/api/portfolio')
      expect(settled.revision).toBe(staked.revision! + 1)
      const settledReview = await call<PortfolioReview>('/api/portfolio/review')
      const settledDetail = await call<{ portfolio: AIPortfolio }>('/api/ai-portfolios/p-seed')
      expect(settledReview).toMatchObject({ revision: settled.revision, predictionStakes: 0 })
      expect(settledDetail.portfolio.overlap?.revision).toBe(settled.revision)
      await call('/api/forecasts/f-seed/resolve', { outcome: 'Yes' })
      expect((await call<{ portfolio: Portfolio }>('/api/portfolio')).portfolio.revision).toBe(settled.revision)
      expect((await call<PortfolioReview>('/api/portfolio/review')).revision).toBe(settled.revision)
      const { portfolio: reset } = await call<{ portfolio: Portfolio }>('/api/portfolio/reset', {})
      const resetReview = await call<PortfolioReview>('/api/portfolio/review')
      const resetDetail = await call<{ portfolio: AIPortfolio }>('/api/ai-portfolios/p-seed')
      expect(reset.revision).toBe(settled.revision! + 1)
      expect(resetReview).toMatchObject({ revision: reset.revision, positions: 0, predictionStakes: 0, concentration: null })
      expect(resetDetail.portfolio.overlap).toMatchObject({ revision: reset.revision, holdings: [] })
      const plans = await call<{ plans: CopyPlan[] }>('/api/copy-plans')
      expect(plans.plans.find((p) => p.id === plan.id)?.positions).toEqual([])
    } finally { await stop() }
  })
})

describe('mock image chat HTTP/SSE contract', () => {
  let stop: () => Promise<void>
  beforeAll(async () => { stop = await globalSetup() })
  afterAll(async () => { await stop?.() })
  function http(path: string, cookie = '', body?: unknown) {
    return new Promise<{ raw: string; cookie: string; status: number }>((resolve, reject) => {
      const req = httpRequest(API_URL + path, { agent: false, method: body ? 'POST' : 'GET', headers: { Cookie: cookie, 'Content-Type': 'application/json' } }, res => {
        let raw = ''; res.on('data', chunk => raw += chunk)
        res.on('end', () => resolve({ raw, cookie: res.headers['set-cookie']?.[0]?.split(';')[0] ?? cookie, status: res.statusCode! }))
      })
      req.on('error', reject); req.end(body ? JSON.stringify(body) : undefined)
    })
  }
  async function atDone(cookie: string, body: unknown) {
    return new Promise<{ chatId: string; done: unknown }>((resolve, reject) => {
      const req = httpRequest(API_URL + '/api/chat/stream', { agent: false, method: 'POST', headers: { Cookie: cookie, 'Content-Type': 'application/json' } }, res => {
        let buffer = '', chatId = ''
        res.on('data', chunk => {
          buffer += chunk
          const { frames, rest } = parseSSE(buffer); buffer = rest
          for (const frame of frames) {
            const data = JSON.parse(frame.data)
            if (frame.event === 'chat') chatId = data.chatId
            if (frame.event === 'done') { resolve({ chatId, done: data }); res.destroy() }
          }
        })
        res.on('error', reject)
      })
      req.on('error', reject); req.end(JSON.stringify(body))
    })
  }
  it.each([null, { kind: 'image', mediaType: 'image/gif', name: 'chart.gif', data: 'eA==' }, { kind: 'image', mediaType: 'image/jpeg', name: 'chart.jpg', data: 'eA' }, { kind: 'image', mediaType: 'image/jpeg', name: 'chart.jpg', data: 'A'.repeat(2_000_004) }])('rejects invalid attachment case %# before creating a chat or spending credits', async image => {
    const login = await http('/api/auth/login', '', DEMO)
    const before = await http('/api/chats', login.cookie)
    const rejected = await http('/api/chat/stream', login.cookie, { message: 'Read this chart', attachments: [image] })
    expect(rejected.status).toBe(400)
    expect(JSON.parse(rejected.raw).error.code).toBe('bad_request')
    expect((await http('/api/chats', login.cookie)).raw).toBe(before.raw)
    expect(JSON.parse((await http('/api/me', login.cookie)).raw).credits.used).toBe(18)
  })
  it.each(['hybrid', 'Investor', 1])('rejects lens %j before creating a chat or spending credits, like the API', async lens => {
    const login = await http('/api/auth/login', '', DEMO)
    const before = await http('/api/chats', login.cookie)
    const rejected = await http('/api/chat/stream', login.cookie, { message: 'Is NVDA a hold?', lens })
    expect(rejected.status).toBe(400)
    expect(JSON.parse(rejected.raw).error).toMatchObject({ code: 'bad_request', message: 'lens must be auto, investor or trader.' })
    expect((await http('/api/chats', login.cookie)).raw).toBe(before.raw)
    expect(JSON.parse((await http('/api/me', login.cookie)).raw).credits.used).toBe(18)
  })
  it.each(['auto', 'investor', 'trader'])('streams an answer with lens %s', async lens => {
    const login = await http('/api/auth/login', '', DEMO)
    expect((await atDone(login.cookie, { message: 'Is NVDA a hold?', lens })).done).toMatchObject({ credits: { used: 20 } })
  })
  it.each(['Make a stock basket', 'Forecast SOL above $150'])('preserves allowed image tools and the total three-credit charge: %s', async message => {
    const login = await http('/api/auth/login', '', DEMO)
    const image = { kind: 'image', mediaType: 'image/jpeg', name: 'chart.jpg', data: Buffer.from(JPEG_BYTES).toString('base64') }
    const turn = await atDone(login.cookie, { message, attachments: [image] })
    expect(turn.done).toMatchObject({ credits: { used: 21 } })
    const restored = JSON.parse((await http('/api/chats/' + turn.chatId, login.cookie)).raw).chat
    const cards = restored.messages.at(-1).cards
    if (message.includes('basket')) expect(cards.some((c: { type: string }) => c.type === 'build_action')).toBe(true)
    else expect(cards.some((c: { type: string }) => c.type === 'forecast_draft')).toBe(false)
  })
  it('persists the complete assistant before emitting done, even when the client cancels its reader', async () => {
    const login = await http('/api/auth/login', '', DEMO)
    const turn = await atDone(login.cookie, { message: 'Should I buy BTC here?' })
    const restored = JSON.parse((await http('/api/chats/' + turn.chatId, login.cookie)).raw).chat
    expect(restored.messages.at(-1).role).toBe('assistant')
    expect(restored.messages.at(-1).cards.some((c: { type: string }) => c.type === 'followups')).toBe(true)
  })
  it.each([1, 2])('charges three for %s images and stores metadata / markers without bytes', async count => {
    const login = await http('/api/auth/login', '', DEMO)
    const image = { kind: 'image', mediaType: 'image/jpeg', name: 'chart.jpg', data: Buffer.from(JPEG_BYTES).toString('base64') }
    const turn = await atDone(login.cookie, { message: 'Read this chart', attachments: Array.from({ length: count }, () => image) })
    expect(turn.done).toMatchObject({ credits: { used: 21 } })
    const restored = JSON.parse((await http('/api/chats/' + turn.chatId, login.cookie)).raw).chat
    expect(restored.messages[0].attachments).toEqual(Array.from({ length: count }, () => ({ kind: 'image', mediaType: 'image/jpeg', name: 'chart.jpg', size: JPEG_BYTES.length })))
    expect(restored.messages[0].content).toContain('[image: chart.jpg]')
    expect(restored.messages[1].attachments).toEqual([])
    expect(JSON.stringify(restored)).not.toContain(image.data)
  })
  const doc = (mediaType: string, name: string, bytes: Uint8Array | string) => ({ kind: 'document', mediaType, name, data: Buffer.from(bytes).toString('base64') })
  it('charges 3 plus 1 per document after the first and stores document metadata (pages) without bytes', async () => {
    const login = await http('/api/auth/login', '', DEMO)
    const image = { kind: 'image', mediaType: 'image/jpeg', name: 'photo.jpg', data: Buffer.from(JPEG_BYTES).toString('base64') }
    const docs = [doc('application/pdf', 'q3.pdf', PDF_BYTES), doc(DOCX_TYPE, 'memo.docx', DOCX_BYTES), doc(XLSX_TYPE, 'model.xlsx', XLSX_BYTES), doc('text/csv', 'prices.csv', CSV_TEXT)]
    const turn = await atDone(login.cookie, { message: 'Read the attached files', attachments: [image, ...docs] })
    expect(turn.done).toMatchObject({ credits: { used: 18 + 3 + 3 } })
    const restored = JSON.parse((await http('/api/chats/' + turn.chatId, login.cookie)).raw).chat
    expect(restored.messages[0].attachments).toEqual([
      { kind: 'image', mediaType: 'image/jpeg', name: 'photo.jpg', size: JPEG_BYTES.length },
      { kind: 'document', mediaType: 'application/pdf', name: 'q3.pdf', size: PDF_BYTES.length, pages: 1 },
      { kind: 'document', mediaType: DOCX_TYPE, name: 'memo.docx', size: DOCX_BYTES.length },
      { kind: 'document', mediaType: XLSX_TYPE, name: 'model.xlsx', size: XLSX_BYTES.length },
      { kind: 'document', mediaType: 'text/csv', name: 'prices.csv', size: CSV_TEXT.length },
    ])
    expect(restored.messages[0].content).toContain('[document: q3.pdf]')
    for (const d of docs) expect(JSON.stringify(restored)).not.toContain(d.data)
  })
  it.each([
    [[...Array(6)].map(() => doc('text/plain', 'a.txt', 'x'))],
    [[doc('application/pdf', 'fake.pdf', 'hello')]],
    [[doc(DOCX_TYPE, 'model.docx', XLSX_BYTES)]],
    [[doc(DOCX_TYPE, 'memo.xlsx', DOCX_BYTES)]],
    [[doc('text/plain', 'nul.txt', new Uint8Array([104, 0]))]],
    [[doc('application/zip', 'a.zip', 'PK')]],
    [[doc('text/plain', 'big.txt', 'a'.repeat(4_500_001))]],
  ])('rejects invalid document case %# before creating a chat or spending credits', async attachments => {
    const login = await http('/api/auth/login', '', DEMO)
    const rejected = await http('/api/chat/stream', login.cookie, { message: 'Read', attachments })
    expect(rejected.status).toBe(400)
    expect(JSON.parse((await http('/api/me', login.cookie)).raw).credits.used).toBe(18)
  })
})

async function accountSession() {
  let cookie = ''
  const call = (path: string, body?: unknown) => new Promise<string>((resolve, reject) => {
    const req = httpRequest(`${API_URL}${path}`, { agent: false, method: body === undefined ? 'GET' : 'POST', headers: { Cookie: cookie, 'Content-Type': 'application/json' } }, res => {
      let text = ''; res.setEncoding('utf8'); res.on('data', x => { text += x })
      res.on('end', () => {
        cookie = res.headers['set-cookie']?.[0]?.split(';')[0] ?? cookie
        if (res.statusCode! >= 400) reject(new Error(`${path}: ${res.statusCode} ${text}`)); else resolve(text)
      })
    })
    req.on('error', reject); req.end(body === undefined ? undefined : JSON.stringify(body))
  })
  await call('/api/auth/login', DEMO)
  return call
}
const accountFrames = (text: string) => parseSSE(text).frames.map(frame => ({ event: frame.event, data: JSON.parse(frame.data) })) as ChatStreamEvent[]
const accountAnswer = (events: ChatStreamEvent[]) => events.filter(e => e.event === 'delta').map(e => e.event === 'delta' ? e.data.text : '').join('')

describe('owned account reviews over real HTTP/SSE', () => {
  let stop: Awaited<ReturnType<typeof globalSetup>>
  beforeAll(async () => { stop = await globalSetup() })
  afterAll(async () => { await stop?.() })
  it('returns null HTTP origins for missing/foreign targets while keeping actual fills', async () => {
    const call = await accountSession()
    const foreign = await accountSession()
    await foreign('/__e2e/account-fixture', { scenario: 'automations' })
    const ownedElsewhere = JSON.parse(await foreign('/api/alerts')).alerts
    expect(ownedElsewhere.some((a: { id: string }) => a.id === 'account-alert-00')).toBe(true)
    await call('/__e2e/account-fixture', { scenario: 'references' })
    const trades = JSON.parse(await call('/api/portfolio/trades')).trades as Trade[]
    expect(trades[0]).toMatchObject({ symbol: 'NVDA', source: 'alert', notional: 500, ref: null })
    expect(trades[1].ref).toMatchObject({ kind: 'chat', id: mock.SEED.chat })
    expect(JSON.stringify(trades)).not.toContain('PRIVATE FOREIGN LABEL')
    expect(JSON.parse(await call('/api/alerts')).alerts).toEqual([])
    expect(JSON.parse(await foreign('/api/alerts')).alerts).toEqual(ownedElsewhere)
  })
  it('attributes a manual/copy overlap to FIFO opening sources and allocates exit fees', async () => {
    const call = await accountSession()
    await call('/api/portfolio/reset', {})
    await call('/api/orders', { symbol: 'SOL', side: 'buy', qty: 1, source: 'manual' })
    await call('/api/orders', { symbol: 'SOL', side: 'buy', qty: 1, source: 'copy' })
    await call('/api/orders', { symbol: 'SOL', side: 'sell', qty: 1.5, source: 'manual' })
    const attribution = JSON.parse(await call('/api/portfolio/attribution'))
    expect(attribution.sources.find((r: { source: string }) => r.source === 'manual')).toMatchObject({ trades: 1, realizedPnl: expect.closeTo(-.36, 9), unrealizedPnl: 0, fees: expect.closeTo(.3, 9), totalPnl: expect.closeTo(-.36, 9) })
    expect(attribution.sources.find((r: { source: string }) => r.source === 'copy')).toMatchObject({ trades: 1, realizedPnl: expect.closeTo(-.255015, 9), unrealizedPnl: expect.closeTo(-.015, 9), fees: expect.closeTo(.225015, 9), totalPnl: expect.closeTo(-.270015, 9) })
    expect(attribution.totalPnl).toBeCloseTo(-.630015, 9)
  })
  it('reviews the actual last fill and account equity, costs only two and executes nothing', async () => {
    const call = await accountSession()
    const before = JSON.parse(await call('/api/portfolio/trades')).trades as Trade[]
    const events = accountFrames(await call('/api/chat/stream', { message: 'Review my last trade' }))
    expect(events.filter(e => e.event === 'tool_call').map(e => e.data.name)).toEqual(['get_trades', 'get_portfolio', 'get_pnl_attribution'])
    expect(accountAnswer(events)).toContain('ETH')
    expect(accountAnswer(events)).toContain('Entry $3,000.60')
    expect(accountAnswer(events)).toContain('Fees $6.00')
    expect(accountAnswer(events)).toContain('No recorded entry plan')
    expect(accountAnswer(events)).toContain('Holding is valid')
    expect(events.filter(e => e.event === 'card')).toEqual([])
    expect(events.at(-1)).toMatchObject({ event: 'done', data: { credits: { used: 20 } } })
    expect(JSON.parse(await call('/api/portfolio/trades')).trades).toEqual(before)
  })

  it('reports an empty since-reset book without fabricating a trade or action', async () => {
    const call = await accountSession()
    await call('/api/portfolio/reset', {})
    const events = accountFrames(await call('/api/chat/stream', { message: 'Review my trades and suggest improvements' }))
    expect(accountAnswer(events)).toContain('No paper fills since your last reset')
    expect(accountAnswer(events)).toContain('Since-reset P&L $0.00')
    expect(events.filter(e => e.event === 'card')).toEqual([])
    expect(JSON.parse(await call('/api/portfolio/trades')).trades).toEqual([])
  })

  it('reviews empty automation, autopilot and invested basket lists without creating anything', async () => {
    const call = await accountSession()
    const events = accountFrames(await call('/api/chat/stream', { message: 'How are my automations doing?' }))
    expect(events.filter(e => e.event === 'tool_call').map(e => e.data.name)).toEqual(['get_automations', 'get_autopilots', 'get_ai_portfolios'])
    expect(accountAnswer(events)).toContain('0 active automations')
    expect(accountAnswer(events)).toContain('0 paper autopilots')
    expect(accountAnswer(events)).toContain('0 invested AI portfolios since reset')
    expect(events.filter(e => e.event === 'card')).toEqual([])
    expect(JSON.parse(await call('/api/alerts')).alerts).toEqual([])
    expect(JSON.parse(await call('/api/autopilots')).autopilots).toEqual([])
  })
  it('keeps an image account read at three credits and stores metadata without bytes or a paid draft', async () => {
    const call = await accountSession()
    const data = Buffer.from(JPEG_BYTES).toString('base64')
    const events = accountFrames(await call('/api/chat/stream', { message: 'Review my last trade and draft a forecast', mode: 'fast', attachments: [{ kind: 'image', mediaType: 'image/jpeg', name: 'entry.jpg', data }] }))
    expect(events.at(-1)).toMatchObject({ event: 'done', data: { credits: { used: 21 } } })
    expect(events.filter(e => e.event === 'card')).toEqual([])
    const routing = events.find(e => e.event === 'chat')!
    if (routing.event !== 'chat') throw new Error('Missing chat frame')
    const stored = JSON.parse(await call(`/api/chats/${routing.data.chatId}`)).chat as Chat
    expect(stored.messages[0].attachments).toEqual([{ kind: 'image', mediaType: 'image/jpeg', name: 'entry.jpg', size: JPEG_BYTES.length }])
    expect(stored.messages[1].attachments).toEqual([])
    expect(JSON.stringify(stored)).not.toContain(data)
    expect(JSON.stringify(stored)).not.toContain('data:image')
    expect(stored.messages[1].content).toContain('Entry $3,000.60')
  })
  it('retains optional proposals as manual cards and fills only after the explicit order', async () => {
    const call = await accountSession(), before = JSON.parse(await call('/api/portfolio/trades')).trades
    const events = accountFrames(await call('/api/chat/stream', { message: 'Review my last trade: warranted trim fixture' }))
    const cards = events.filter(e => e.event === 'card')
    expect(cards).toHaveLength(1)
    expect(JSON.parse(await call('/api/portfolio/trades')).trades).toEqual(before)
    const card = cards[0]
    if (card.event !== 'card' || card.data.type !== 'trade_proposal') throw new Error('Expected ordinary proposal')
    const fill = JSON.parse(await call('/api/orders', { symbol: card.data.data.symbol, side: card.data.data.side, notional: card.data.data.notional, source: 'agent', proposalId: card.data.data.proposalId }))
    expect(fill.trade).toMatchObject({ symbol: 'ETH', side: 'sell', source: 'agent' })
    expect(JSON.parse(await call('/api/portfolio/trades')).trades).toHaveLength(before.length + 1)
  })
})

describe('long-horizon outlook over real HTTP/SSE', () => {
  let stop: Awaited<ReturnType<typeof globalSetup>>
  beforeAll(async () => { stop = await globalSetup() })
  afterAll(async () => { await stop?.() })
  it('answers a 10x question with the 10-year price_outlook card and stores it with the answer', async () => {
    const call = await accountSession()
    const events = accountFrames(await call('/api/chat/stream', { message: 'Can BTC 10x in 10 years?' }))
    expect(events.filter(e => e.event === 'tool_call').map(e => e.data.name)).toEqual(['price_outlook'])
    expect(accountAnswer(events)).toBe(mock.LONG_OUTLOOK_ANSWER)
    const cards = events.filter(e => e.event === 'card')
    expect(cards).toEqual([{ event: 'card', data: { type: 'price_outlook', data: fx.longOutlook } }])
    expect(fx.longOutlook.longHorizon?.tenX.map(t => t.years)).toEqual([1, 3, 5, 10])
    const routing = events.find(e => e.event === 'chat')!
    if (routing.event !== 'chat') throw new Error('Missing chat frame')
    const stored = JSON.parse(await call(`/api/chats/${routing.data.chatId}`)).chat as Chat
    expect(stored.messages[1].cards).toEqual([{ type: 'price_outlook', data: fx.longOutlook }])
  })
  it('answers "NVDA 10 years outlook" with the capped-drift 10-year view card, not the BTC 10-year card', async () => {
    const call = await accountSession()
    const events = accountFrames(await call('/api/chat/stream', { message: 'NVDA 10 years outlook' }))
    const tool = events.find(e => e.event === 'tool_call')
    expect(tool?.event === 'tool_call' && tool.data.input).toEqual({ symbol: 'NVDA', horizon_days: 1825 })
    expect(accountAnswer(events)).toBe(mock.NVDA_DRIFT_ANSWER)
    expect(events.filter(e => e.event === 'card')).toEqual([{ event: 'card', data: { type: 'price_outlook', data: fx.nvdaOutlookView } }])
    expect(fx.nvdaOutlookView.horizon).toEqual({ days: 3653, basis: 'explicit', reason: 'From your question: "10 years outlook"' })
    expect(fx.nvdaOutlookView.view?.driftCap?.applied).toBe(true)
  })
  it('keeps other questions on the scripted trade turn', async () => {
    const call = await accountSession()
    const events = accountFrames(await call('/api/chat/stream', { message: 'Is BTC overbought right now?' }))
    expect(events.filter(e => e.event === 'card').map(e => e.event === 'card' && e.data.type)).toEqual(['sources', 'trade_proposal', 'followups'])
  })
})

describe('mock news wire regions', () => {
  let stop: Awaited<ReturnType<typeof globalSetup>>
  beforeAll(async () => { stop = await globalSetup() })
  afterAll(async () => { await stop?.() })
  it('filters by region alongside topic, and rejects an unknown region with the error envelope', async () => {
    const call = await accountSession()
    const ids = async (q: string) => (JSON.parse(await call(`/api/news${q}`)).news as NewsItem[]).map((n) => n.id)
    expect(await ids('')).toEqual(['n1', 'n2', 'n3', 'n4', 'n5'])
    expect(await ids('?region=east_asia')).toEqual(['n4', 'n5'])
    expect(await ids('?region=us&topic=macro')).toEqual(['n3'])
    expect(await ids('?region=crypto&symbol=ETH')).toEqual(['n2'])
    expect(await ids('?region=southeast_asia')).toEqual([])
    await expect(call('/api/news?region=europe')).rejects.toThrow(/400 .*"code":"bad_request"/)
    const brief = JSON.parse(await call('/api/news/brief')).brief as MarketBrief
    expect(brief.regions?.map((r) => r.region)).toEqual(['crypto', 'us', 'east_asia'])
  })
})

describe('mock drawdown triggers', () => {
  let stop: () => Promise<void>
  beforeAll(async () => { stop = await globalSetup() })
  afterAll(async () => { await stop?.() })
  function http(path: string, cookie = '', body?: unknown) {
    return new Promise<{ raw: string; cookie: string; status: number }>((resolve, reject) => {
      const req = httpRequest(API_URL + path, { agent: false, method: body ? 'POST' : 'GET', headers: { Cookie: cookie, 'Content-Type': 'application/json' } }, res => {
        let raw = ''; res.on('data', chunk => raw += chunk)
        res.on('end', () => resolve({ raw, cookie: res.headers['set-cookie']?.[0]?.split(';')[0] ?? cookie, status: res.statusCode! }))
      })
      req.on('error', reject); req.end(body ? JSON.stringify(body) : undefined)
    })
  }

  it('reads the labelled mock high, creates notify and sell triggers with their threshold and refuses bad rules', async () => {
    const { cookie } = await http('/api/auth/login', '', DEMO)
    const ath = JSON.parse((await http('/api/alerts/ath?symbol=sol', cookie)).raw).ath
    expect(ath).toMatchObject({ symbol: 'SOL', ath: 240, price: 150, fromAthPct: -37.5, source: 'binance', ticker: 'SOLUSDT', basis: 'mark',
      sourceLabel: 'Binance SOLUSDT daily highs since 2020-08-11' })
    expect(JSON.parse((await http('/api/alerts/ath?symbol=SMSN', cookie)).raw).ath.sourceLabel).toBe("perp history since 2026-02-20, not the listing's lifetime high")
    // A quote feed measures its listing in the listing's currency (contract: basis "listing"), not the USD mark.
    expect(JSON.parse((await http('/api/alerts/ath?symbol=DBS', cookie)).raw).ath).toMatchObject({ ath: 125.7, price: 78.56, currency: 'SGD',
      basis: 'listing', source: 'yahoo', ticker: 'D05.SI', sourceLabel: 'Yahoo Finance D05.SI lifetime daily highs since 1999-01-22 (split-adjusted), in SGD' })
    // HSDT stands in for a reverse-split high: a Yahoo high over 100× the price is refused, for the form and a trigger.
    const message = "HSDT has no usable all-time high: Yahoo's split-adjusted lifetime high for HSDT is over 100× the price, which reverse splits produce, so a drawdown alert can't measure from it."
    for (const res of [await http('/api/alerts/ath?symbol=HSDT', cookie), await http('/api/alerts', cookie, { symbol: 'HSDT', trigger: 'drawdown', drawdownPct: 20 })])
      expect([res.status, JSON.parse(res.raw).error.message]).toEqual([400, message])
    // The API resolves the high after the rules, so a malformed HSDT trigger hears about its own rule first.
    const malformed = await http('/api/alerts', cookie, { symbol: 'HSDT', trigger: 'drawdown', drawdownPct: 0 })
    expect([malformed.status, JSON.parse(malformed.raw).error.message]).toEqual([400, 'drawdownPct must be 0.01–99.99 (% below the all-time high).'])
    expect((await http('/api/alerts/ath?symbol=PEPE', cookie)).status).toBe(400)

    const created = await http('/api/alerts', cookie, { symbol: 'SOL', trigger: 'drawdown', drawdownPct: 30, action: 'sell', source: 'manual' })
    expect(created.status).toBe(201)
    expect(JSON.parse(created.raw).alert).toMatchObject({ trigger: 'drawdown', op: null, price: null, action: 'sell', notional: null,
      drawdown: { pct: 30, sellPct: 100, ath: 240, threshold: 168, outcome: null, price: 150 } })
    for (const [body, message] of [
      [{ drawdownPct: 0 }, 'drawdownPct must be 0.01–99.99 (% below the all-time high).'],
      [{ drawdownPct: 20, action: 'buy' }, 'A drawdown alert notifies or sells: action must be notify or sell.'],
      [{ drawdownPct: 20, sellPct: 50 }, 'sellPct is for action sell only.'],
      [{ drawdownPct: 20, action: 'sell', sellPct: 0.5 }, 'sellPct must be a whole number from 1 to 100 (% of the paper long).'],
      [{ drawdownPct: 20, notional: 100 }, 'A drawdown alert takes no op, price, schedule, notional, stopLoss or takeProfit.'],
    ] as const) {
      const refused = await http('/api/alerts', cookie, { symbol: 'SOL', trigger: 'drawdown', ...body })
      expect([refused.status, JSON.parse(refused.raw).error.message]).toEqual([400, message])
    }
    const list = JSON.parse((await http('/api/alerts', cookie)).raw).alerts
    expect(list.filter((a: { trigger: string }) => a.trigger === 'drawdown')).toHaveLength(1)
  })

  it('answers "falls 30% from its all-time high" with a set_drawdown_alert card and sets nothing', async () => {
    const { cookie } = await http('/api/auth/login', '', DEMO)
    const res = await http('/api/chat/stream', cookie, { message: 'Tell me if ETH falls 30% from its all-time high and sell it' })
    const cards = parseSSE(res.raw).frames.filter((f) => f.event === 'card').map((f) => JSON.parse(f.data))
    expect(cards).toEqual([{ type: 'price_alert', data: expect.objectContaining({ symbol: 'ETH', trigger: 'drawdown', drawdownPct: 30, action: 'sell', sellPct: 100,
      markPrice: 3000, threshold: 3360, ath: expect.objectContaining({ ath: 4800, source: 'binance' }) }) }])
    expect(JSON.parse((await http('/api/alerts', cookie)).raw).alerts).toEqual([])
  })

  it('answers a drawdown alert on an unusable high with a failed set_drawdown_alert chip, no card, like the API tool', async () => {
    const { cookie } = await http('/api/auth/login', '', DEMO)
    const res = await http('/api/chat/stream', cookie, { message: 'Tell me if HSDT falls 40% from its all-time high' })
    expect(res.status).toBe(200)
    const frames = parseSSE(res.raw).frames
    expect(frames.map((f) => f.event)).toEqual(['chat', 'tool_call', 'tool_result', 'delta', 'done'])
    expect(JSON.parse(frames[2].data)).toMatchObject({ name: 'set_drawdown_alert', ok: false, summary: expect.stringMatching(/^HSDT has no usable all-time high: /) })
    expect(JSON.parse((await http('/api/alerts', cookie)).raw).alerts).toEqual([])
  })
})

describe('mock Asia screener', () => {
  const page = (q: string) => mock.screenerPageFor(new URLSearchParams(q))
  const tickers = (q: string) => page(q).rows.map((r) => r.ticker)

  it('filters by band, market and search, with each band counted within market and search', () => {
    expect(tickers('filter=deep')).toEqual(['0000.T'])
    expect(tickers('filter=oversold')).toEqual(['0000.T'])
    expect(tickers('filter=approaching')).toEqual(['0700.HK', 'D01.SI'])
    expect(tickers('filter=below')).toEqual(['BBCA.JK'])
    expect(page('').counts).toEqual({ all: 10, below: 1, deep: 1, distressed: 0, approaching: 2, oversold: 1 }) // the fixtures and the listed 6501.T
    expect(page('market=jp').counts.all).toBe(4)
    expect(tickers('q=kospi')).toEqual(['005930.KS'])
    expect(tickers('q=korea')).toEqual(['005930.KS']) // the market's name, as the API matches it
    expect(tickers('q=japan').sort()).toEqual(['0000.T', '1332.T', '285A.T', '6501.T'])
    expect(page('q=hitachi').rows.map((r) => [r.ticker, r.tradableSymbol, r.tradeVia])).toEqual([['6501.T', '6501.T', 'listing']])
    expect(tickers('q=kr')).toEqual(['005930.KS']) // exactly the market's code
    expect(page('').markets.find((m) => m.code === 'PH')!.count).toBe(1)
  })

  it('sorts with rows lacking the value last either way, and pages by cursor', () => {
    expect(tickers('')).toEqual(['0000.T', 'BBCA.JK', '0700.HK', 'D01.SI', '6501.T', '2330.TW', '005930.KS', '1332.T', '285A.T', 'SM.PS'])
    expect(tickers('sort=-rsi').slice(0, 2)).toEqual(['2330.TW', '005930.KS'])
    expect(tickers('sort=-rsi').at(-1)).toBe('SM.PS')
    const first = page('limit=4')
    expect(first.rows).toHaveLength(4)
    const second = page(`limit=4&cursor=${first.nextCursor}`)
    expect(second.rows[0].ticker).toBe('6501.T')
    expect(page(`limit=4&cursor=${second.nextCursor}`).nextCursor).toBeNull()
  })

  it('rejects what the API rejects', () => {
    for (const q of ['filter=cheap', 'market=US', 'sort=drop', 'limit=0', 'limit=101']) expect(() => page(q)).toThrow()
  })

  it('serves the loaded rows again by ticker, in the order asked, and moves Tencent from the first refresh on', () => {
    const s = createState()
    const quotes = (tickers: string) => mock.screenerQuotesFor(new URLSearchParams({ tickers }), s)
    expect(mock.screenerPageFor(new URLSearchParams(''), s).rows.find((r) => r.ticker === '0700.HK')!.price).toBe(428.2)
    const first = quotes('0700.hk,BBCA.JK,0700.HK,NOPE.T')
    expect(first.rows.map((r) => r.ticker)).toEqual(['0700.HK', 'BBCA.JK']) // case-insensitive, duplicates folded
    expect(first.unknown).toEqual(['NOPE.T'])
    expect(first.pollSeconds).toBe(60)
    const tencent = first.rows[0]
    expect(tencent).toMatchObject({ price: 475.3, pctVsSma: 11.7, direction: 'recovering', drawdownPct: -33.52, priceUsd: 60.5651,
      quote: { price: 475.3, lastTradeAt: '2026-10-14T07:09:10Z', readAt: '2026-10-14T07:10:04Z', stale: false } })
    // The page now prices it the same, so a re-sort moves it past D01.SI (+10.4%).
    expect(tickers('').slice(0, 5)).toEqual(['0000.T', 'BBCA.JK', '0700.HK', 'D01.SI', '6501.T']) // another session: unchanged
    expect(mock.screenerPageFor(new URLSearchParams(''), s).rows.map((r) => r.ticker).slice(0, 5)).toEqual(['0000.T', 'BBCA.JK', 'D01.SI', '0700.HK', '6501.T'])
    expect(mock.screenerPageFor(new URLSearchParams('sort=-updated'), s).rows[0].ticker).toBe('0700.HK') // its last trade is the latest
    expect(mock.screenerQuotesFor(new URLSearchParams('tickers=2330.TW&tickers=d01.si,2330.tw'), s).rows.map((r) => r.ticker)).toEqual(['2330.TW', 'D01.SI']) // repeated too
    expect(() => quotes('')).toThrow('tickers is required: up to 100 constituent tickers, comma-separated (7203.T,005930.KS).')
    expect(() => quotes(Array.from({ length: 101 }, (_, i) => `${i}.T`).join(','))).toThrow('tickers is limited to 100.')
  })

  it('derives a new high from a price above the run’s', () => {
    const tsmc = fx.screenerRows[3]
    expect(mock.repriced({ ...tsmc, ath: 2590, athAt: '2026-10-05' }, 2600, { lastTradeAt: '2026-10-14T05:30:00Z' })).toMatchObject({ ath: 2600, athAt: '2026-10-14', drawdownPct: 0 })
    expect(mock.repriced(tsmc, 2500, { lastTradeAt: '2026-10-14T05:30:00Z' })).toMatchObject({ ath: 2600, athAt: '2026-10-14', drawdownPct: -3.85 })
  })
})

describe('mock brief states', () => {
  it('serves a fresh brief with its stored sources, none, or a 9 h old one citing a story off the wire', () => {
    const s = createState()
    const fresh = mock.briefFor(s).brief!
    expect(fresh.sources!.map((x) => x.id)).toEqual(['n1'])
    expect(Date.now() - Date.parse(fresh.createdAt)).toBeLessThan(2 * 3_600_000)
    expect(mock.setBriefFixture(s, 'none')).toEqual({ brief: null })
    const stale = mock.setBriefFixture(s, 'stale').brief!
    expect(Math.round((Date.now() - Date.parse(stale.createdAt)) / 3_600_000)).toBe(9)
    const cited = stale.bullets.flatMap((b) => b.sources)
    expect(cited.every((id) => stale.sources!.some((x) => x.id === id))).toBe(true) // every cited id is stored with the brief
    expect(mock.briefFor(createState()).brief).not.toBeNull() // per session
    expect(() => mock.setBriefFixture(s, 'gone' as mock.BriefFixture)).toThrow('Unknown brief fixture.')
  })
})

describe('mock signals board states', () => {
  it('serves a fresh board, the newest valid one while refreshing, then none or the first board', () => {
    const s = createState()
    expect(mock.signalsFor(s)).toMatchObject({ board: { id: 'sb1' }, refreshing: false, firstBoard: false })
    const stale = mock.setSignalsFixture(s, 'refreshing')
    expect(stale).toMatchObject({ board: { id: 'sb0' }, refreshing: true, firstBoard: false })
    expect(stale.asOf).toBe(stale.board!.createdAt)
    expect(Date.parse(stale.board!.nextAt)).toBe(Date.parse(stale.board!.createdAt) + 230 * 60_000) // the refill starts 10 min before 4 h
    expect(mock.setSignalsFixture(s, 'empty')).toMatchObject({ board: null, history: [], refreshing: true, asOf: null, firstBoard: false })
    expect(mock.setSignalsFixture(s, 'first')).toMatchObject({ board: null, refreshing: true, firstBoard: true })
    expect(mock.signalsFor(createState()).board).not.toBeNull() // per session
    expect(() => mock.setSignalsFixture(s, 'gone' as mock.SignalsFixture)).toThrow('Unknown Signals fixture.')
  })

  it('serves the refresh state, and the failing boards with their retry time and the ledger history kept', () => {
    const s = createState()
    expect(mock.signalsFor(s)).toMatchObject({ refresh: { state: 'idle', failures: 0, retryAt: null }, historyUnavailable: false })
    expect(mock.setSignalsFixture(s, 'refreshing').refresh).toMatchObject({ state: 'running', retryAt: null })
    const failing = mock.setSignalsFixture(s, 'failing')
    expect(failing).toMatchObject({ board: { id: 'sb0' }, historyUnavailable: false, refreshing: true, refresh: { state: 'retrying', failures: 3 } })
    expect(failing.history).toHaveLength(3) // from the ledger, not candles: a failing refresh keeps it
    expect(Date.parse(failing.refresh!.retryAt!)).toBeGreaterThan(Date.now())
    expect(Date.now() - Date.parse(failing.board!.createdAt)).toBeGreaterThan(8 * 3_600_000) // stale
    expect(mock.setSignalsFixture(s, 'failing-empty')).toMatchObject({ board: null, historyUnavailable: false, refresh: { state: 'retrying' } })
  })

  it('serves the steady-v1 record, a new ledger, a legacy board with its legacy rows, and the week after the switch', () => {
    const s = createState()
    const ready = mock.signalsFor(s)
    expect(ready.record).toMatchObject({ rule: 'steady-v1', all: { verdict: 'insufficient' }, replay: { picks: 864 } })
    expect(ready.board!.bullish[0].invalidation).toBe(72000)
    expect(ready.history.map((r) => r.reason)).toEqual(['invalidated', 'target_hit', 'expired'])
    expect(ready.history.some((r) => r.legacy)).toBe(false)
    const fresh = mock.setSignalsFixture(s, 'new-ledger')
    expect(fresh).toMatchObject({ board: { id: 'sb1' }, history: [], refreshing: false, record: { since: null, all: { scored: 0 } } })
    const legacy = mock.setSignalsFixture(s, 'legacy')
    expect(legacy.record).toMatchObject({ since: null, all: { scored: 0 } }) // nothing of a legacy board enters the ledger
    expect([...legacy.board!.bullish, ...legacy.board!.bearish].every((p) => p.invalidation === 0)).toBe(true)
    expect(legacy.history.map((r) => r.reason)).toEqual(['target_hit', 'expired', 'outranked', 'score_fell', 'no_data', 'left'])
    for (const r of legacy.history) {
      expect(r).toMatchObject({ legacy: true, invalidation: 0 })
      expect(r).not.toHaveProperty('result72Pct')
      expect(r).not.toHaveProperty('scoredAt')
    }
    const after = mock.setSignalsFixture(s, 'switch')
    expect(after.history.map((r) => [r.symbol, r.reason, r.legacy ?? false])).toEqual([
      ['XRP', 'invalidated', false], ['SOL', 'left', true], ['AVAX', 'target_hit', true], ['DOGE', 'expired', true],
      ['LINK', 'outranked', true], ['XRP', 'score_fell', true], ['ADA', 'no_data', true], ['DOT', 'left', true],
    ])
    const removed = after.history.map((r) => Date.parse(r.removedAt))
    expect(removed).toEqual([...removed].sort((a, b) => b - a)) // newest removedAt first, as the API sorts
    expect(after.record).toMatchObject({ open: 4, all: { scored: 0 } })
  })
})

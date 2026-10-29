import { afterEach, describe, expect, it, vi } from 'vitest'
import * as mock from './mock-api'
import { attributeLots, replayPoints, summarizeEquity } from './account-ledger'
import type { Trade } from '../src/lib/types'

afterEach(() => { vi.useRealTimers() })
const plain = (s: string) => s.replace(/<\/?untrusted_web_content>/g, '').trim()
const fixture = (scenario: Parameters<typeof mock.seedAccountFixture>[1]) => {
  vi.useFakeTimers({ toFake: ['Date'] }); vi.setSystemTime(new Date('2026-10-05T12:00:00Z'))
  const state = mock.createState(); mock.seedAccountFixture(state, scenario); return state
}
const snapshot = (s: mock.State) => JSON.stringify({ cash: s.cash, realized: s.realized, revision: s.revision, credits: s.credits, positions: [...s.positions], trades: s.trades, chats: [...s.chats], portfolios: s.portfolios })

describe('approved compact owning account reads in the browser mock', () => {
  it('uses actual pre-window FIFO lots, fees and settled/open stakes, with a separate selected window', () => {
    const s = fixture('ledger'), before = snapshot(s)
    const result = mock.accountPnlOf(s, '1d')
    expect(result.attribution.scope).toBe('since_reset')
    expect(result.attribution.sources).toEqual([
      { source: 'copy', trades: 1, realizedPnl: 4.83, unrealizedPnl: 20, fees: expect.closeTo(.17, 12), totalPnl: 24.83 },
      { source: 'prediction', trades: 2, realizedPnl: 20, unrealizedPnl: 0, fees: 0, totalPnl: 20 },
      { source: 'manual', trades: 1, realizedPnl: 19.78, unrealizedPnl: 0, fees: .22, totalPnl: 19.78 },
    ])
    expect(result.attribution.totalPnl).toBeCloseTo(64.61, 8)
    expect(result.equitySummary).toEqual({ scope: 'selected_range', range: '1d', startEquity: 100009.79, endEquity: 100064.61, returnPct: .05, maxDrawdownPct: 0 })
    expect(mock.equityOf(s, '1d').points[0].v).toBe(100009.79)
    expect(mock.equityOf(s, '1d').points.at(-1)!.v).toBe(100064.61)
    expect(Object.keys(mock.accountPnlOf(s))).toEqual(['attribution'])
    expect(JSON.stringify(result)).not.toMatch(/points|series|holdings/)
    expect(snapshot(s)).toBe(before)
  })

  it('filters stock fills before the bound, retains equal-time reverse booking order, validates caps and clips Unicode data', () => {
    const s = fixture('references')
    const stock = s.trades[0]
    stock.note = '🧭'.repeat(220)
    stock.createdAt = '2026-10-01T00:00:00Z'
    for (let i = 0; i < 55; i++) mock.placeOrder(s, { symbol: 'ETH', side: 'buy', notional: 10 })
    const out = mock.accountTradesOf(s, { symbol: 'nvda', limit: 1 })
    expect(out.trades.map(t => t.id)).toEqual([stock.id])
    expect(out.trades[0]).toMatchObject({ symbol: 'NVDA', ref: null, notional: 500 })
    expect(Array.from(plain(out.trades[0].note))).toHaveLength(200)
    expect(out.truncated).toBe(true)
    expect(mock.accountTradesOf(s).trades).toHaveLength(20)
    expect(mock.accountTradesOf(s, { limit: 50 }).trades).toHaveLength(50)
    expect(mock.accountTradesOf(s, { limit: 2 }).trades.map(t => t.id)).toEqual(s.trades.slice(0, 2).map(t => t.id))
    for (const limit of [0, 51, 1.5, null, '1']) expect(() => mock.accountTradesOf(s, { limit: limit as number })).toThrow(/limit/)
    for (const symbol of ['', 'NOPE', null]) expect(() => mock.accountTradesOf(s, { symbol: symbol as string })).toThrow(/symbol/)
  })

  it('verifies extant caller-local references and never loses a booked fill when an origin disappears', () => {
    const s = fixture('references'), before = snapshot(s)
    const out = mock.accountTradesOf(s)
    expect(out.trades[0].ref).toBeNull()
    expect(out.trades[1].ref).toMatchObject({ kind: 'chat', id: mock.SEED.chat })
    expect(JSON.stringify(out)).not.toContain('PRIVATE FOREIGN LABEL')
    expect(snapshot(s)).toBe(before)
    s.chats.get(mock.SEED.chat)!.title = '🗂'.repeat(130)
    const bounded = mock.accountTradesOf(s)
    expect(Array.from(plain(bounded.trades[1].ref!.label))).toHaveLength(120)
    expect(bounded.truncated).toBe(true)
    s.chats.delete(mock.SEED.chat)
    expect(mock.accountTradesOf(s).trades.every(t => t.ref === null)).toBe(true)
    expect(mock.accountTradesOf(s).trades).toHaveLength(2)
    expect(mock.accountPnlOf(s).attribution.sources.some(r => r.source === 'alert')).toBe(true)
  })

  it('bounds active automations, newest pilots/logs and eligible investments before newer uninvested drafts', () => {
    const s = fixture('bounded'), before = snapshot(s)
    const alerts = mock.accountAutomationsOf(s), pilots = mock.accountAutopilotsOf(s), portfolios = mock.accountPortfoliosOf(s)
    expect(alerts.automations).toHaveLength(20); expect(alerts.truncated).toBe(true)
    expect(alerts.automations.every(a => a.status === 'active')).toBe(true)
    const schedule = alerts.automations.find(a => a.trigger === 'schedule')!
    expect(Array.from(plain(schedule.note))).toHaveLength(200)
    expect(Array.from(plain(schedule.lastRun!.body))).toHaveLength(300)
    expect(Array.from(plain(schedule.lastRun!.error!))).toHaveLength(160)
    const odds = alerts.automations.find(a => a.trigger === 'odds')!
    expect(odds.market?.url).toBe('')
    expect(odds.linkedMarket).toBeNull()
    expect(pilots.autopilots).toHaveLength(10); expect(pilots.truncated).toBe(true)
    expect(pilots.autopilots.every(p => p.log.length === 5 && Array.from(plain(p.name)).length === 120 && p.log.every(l => Array.from(plain(l.text)).length === 200))).toBe(true)
    expect(portfolios.portfolios).toHaveLength(10); expect(portfolios.truncated).toBe(true)
    expect(portfolios.portfolios.map(p => p.id)).toEqual(Array.from({ length: 10 }, (_, i) => `account-basket-${String(i).padStart(2, '0')}`))
    expect(portfolios.portfolios[0]).toMatchObject({ benchmarkSymbol: 'SP500', benchmarkLabel: 'S&P 500 perp', invested: { amount: 1000, benchmarkSymbol: 'SP500', btcReturnPct: null } })
    expect(portfolios.portfolios[1]).toMatchObject({ benchmarkSymbol: 'BTC', invested: { benchmarkSymbol: 'BTC', btcReturnPct: 0 } })
    expect(portfolios.portfolios[2]).toMatchObject({ benchmarkSymbol: 'BTC', invested: { benchmarkSymbol: 'BTC' } })
    expect(JSON.stringify(portfolios)).not.toMatch(/holdings|backtest|series|windowStart|thesis/)
    expect(snapshot(s)).toBe(before)
    s.trades.find(t => t.ref?.id === 'account-basket-01')!.createdAt = '2026-01-01T00:00:00Z'
    expect(mock.accountPortfoliosOf(s).portfolios[1].invested).toMatchObject({ benchmarkSymbol: 'BTC', benchmarkReturnPct: null, btcReturnPct: null })
  })

  it('keeps all five pilot valuations null and eligible saved stock investment during a quote outage', () => {
    const s = fixture('unavailable')
    expect(mock.accountAutopilotsOf(s).autopilots[0]).toMatchObject({ markPrice: null, equity: null, pnl: null, pnlPct: null, drawdownPct: null })
    expect(mock.accountPortfoliosOf(s).portfolios).toEqual([expect.objectContaining({ benchmarkSymbol: 'SP500', benchmarkLabel: 'S&P 500 perp', invested: null })])
    expect(mock.accountAutomationsOf(s).automations).toHaveLength(3)
    s.trades = []
    expect(mock.accountPortfoliosOf(s)).toEqual({ portfolios: [], truncated: false })
  })

  it('reuses shared FIFO for portfolio/copy refs without assigning a manual exit to its own isolated book', () => {
    const s = fixture('ledger')
    const copy = s.trades.find(t => t.source === 'copy')!
    expect(attributeLots([...s.trades].reverse(), () => 150, t => t.ref?.id ?? '').find(r => r.source === copy.ref!.id)).toMatchObject({ realizedPnl: 4.83, unrealizedPnl: 20, totalPnl: 24.83 })
  })
})

describe('ledger boundaries', () => {
  it('allocates a flip exit fee to the opening lot and opens only the remainder under the closing source', () => {
    const trade = (side: Trade['side'], qty: number, price: number, fee: number, source: Trade['source']): Trade => ({ id: '', symbol: 'SOL', side, qty, price, notional: qty * price, fee, source, note: '', realizedPnl: 0, createdAt: '2026-10-01T00:00:00Z' })
    expect(attributeLots([trade('buy', 1, 100, .1, 'manual'), trade('sell', 2, 120, .24, 'copy')], () => 110)).toEqual([
      { source: 'manual', trades: 1, realizedPnl: 19.78, unrealizedPnl: 0, fees: .22, totalPnl: 19.78 },
      { source: 'copy', trades: 1, realizedPnl: -.12, unrealizedPnl: 10, fees: .12, totalPnl: 9.88 },
    ])
  })
  it('replays pre-window cash/fills and never returns a percentage from nonpositive starting equity', () => {
    const s = fixture('ledger')
    expect(summarizeEquity('1d', [{ t: 1, v: 0 }, { t: 2, v: 100 }]).returnPct).toBeNull()
    expect(summarizeEquity('7d', [{ t: 1, v: -10 }, { t: 2, v: 100 }]).returnPct).toBeNull()
    const points = replayPoints([...s.trades].reverse(), [], [Date.parse('2026-10-05T10:00:00Z') / 1000, Date.parse('2026-10-05T11:00:00Z') / 1000], () => 110)
    expect(points.map(p => p.v)).toEqual([100009.79, 100024.61])
  })
})

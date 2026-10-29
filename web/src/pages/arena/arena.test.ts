import { describe, expect, it } from 'vitest'
import type { ArenaAgent, ArenaBenchmark, ArenaMetrics, ArenaOptions, ArenaStep } from '../../lib/types'
import {
  addDays,
  BASELINE_COLOR,
  cost,
  daysIn,
  etaMin,
  isContaminated,
  lastStepAt,
  rankRows,
  setupProblem,
  simAt,
  stepsFor,
  targetDiff,
  toolLabel,
  windowLabel,
  windowText,
} from './arena'

const m = (totalReturnPct: number, maxDrawdownPct: number) => ({ totalReturnPct, maxDrawdownPct }) as ArenaMetrics
const agent = (index: number, name: string, metrics: ArenaMetrics) => ({ index, name, personaId: 'momentum', metrics }) as ArenaAgent
const bench = (id: 'btc' | 'basket', ret: number) => ({ id, name: id, equity: [], metrics: m(ret, 1) }) as ArenaBenchmark

const step = (k: number, over: Partial<ArenaStep> = {}): ArenaStep => ({
  step: k,
  t: 0,
  equity: 100000,
  cash: 100000,
  positions: [],
  targets: null,
  fills: [],
  tools: [],
  reasoning: '',
  error: null,
  ...over,
})

const opts: ArenaOptions = {
  personas: [],
  knowledgeCutoff: '2026-06-30',
  latestEnd: '2026-10-01',
  cadences: [
    { cadence: '1d', minDays: 30, maxDays: 90, defaultStart: '2026-08-03', defaultEnd: '2026-10-01' },
    { cadence: '4h', minDays: 7, maxDays: 14, defaultStart: '2026-09-18', defaultEnd: '2026-10-01' },
  ],
  maxAgents: 4,
  maxSteps: 90,
  creditsPerAgentStep: 0.5,
}

describe('arena helpers', () => {
  it('prices a run at half a credit per agent-step, rounded up', () => {
    expect(cost(3, 60)).toBe(90)
    expect(cost(1, 30)).toBe(15)
    expect(cost(1, 45)).toBe(23)
    expect(cost(4, 90)).toBe(180)
    expect(cost(0, 60)).toBe(0)
  })

  it('counts window days inclusively and steps per cadence', () => {
    expect(daysIn('2026-08-03', '2026-10-01')).toBe(60)
    expect(daysIn('2026-10-01', '2026-10-01')).toBe(1)
    expect(daysIn('2026-10-02', '2026-10-01')).toBe(0)
    expect(daysIn('', '2026-10-01')).toBe(0)
    expect(stepsFor('1d', '2026-08-03', '2026-10-01')).toBe(60)
    expect(stepsFor('4h', '2026-09-18', '2026-10-01')).toBe(84) // 14 days × 6 bars
    expect(stepsFor('4h', 'nope', '2026-10-01')).toBe(0)
    expect(etaMin(60)).toBe(4)
    expect(etaMin(1)).toBe(1)
  })

  it('adds UTC calendar days across month ends', () => {
    expect(addDays('2026-08-30', 3)).toBe('2026-09-02')
    expect(addDays('2026-10-01', -29)).toBe('2026-09-02')
    expect(addDays('', 1)).toBe('')
  })

  it('flags a window that starts on or before the knowledge cutoff', () => {
    expect(isContaminated('2026-06-30', '2026-06-30')).toBe(true)
    expect(isContaminated('2026-05-01', '2026-06-30')).toBe(true)
    expect(isContaminated('2026-07-01', '2026-06-30')).toBe(false)
    expect(isContaminated('', '2026-06-30')).toBe(false)
  })

  it('ranks agents by return, ties by smaller drawdown, and never ranks the baselines', () => {
    const rows = rankRows(
      [agent(0, 'A', m(5, 9)), agent(1, 'B', m(12, 4)), agent(2, 'C', m(5, 3))],
      [bench('btc', 40), bench('basket', 20)],
    )
    expect(rows.map((r) => [r.name, r.rank])).toEqual([
      ['B', 1],
      ['C', 2],
      ['A', 3],
      ['btc', null],
      ['basket', null],
    ])
    expect(rows[3].color).toBe(BASELINE_COLOR.btc)
    expect(rows[0].agent?.index).toBe(1)
  })

  it('writes target chips against the weights of the previous close', () => {
    const prev = step(4, { positions: [{ symbol: 'BTC', qty: 1, avgPrice: 1, mark: 1, weightPct: 33.4 }] })
    const s = step(5, { targets: [{ symbol: 'BTC', weightPct: 50 }, { symbol: 'ETH', weightPct: 20 }] })
    expect(targetDiff(s, prev)).toEqual(['BTC 33→50%', 'ETH 0→20%'])
    expect(targetDiff(s)).toEqual(['BTC 0→50%', 'ETH 0→20%'])
    expect(targetDiff(step(6))).toEqual([]) // held
  })

  it('validates the setup, first problem only', () => {
    const ok = { cadence: '1d' as const, start: '2026-08-03', end: '2026-10-01', agents: 3, instruction: null }
    expect(setupProblem(ok, opts)).toBeNull()
    expect(setupProblem({ ...ok, start: '' }, opts)).toBe('Pick a start and an end date.')
    expect(setupProblem({ ...ok, end: '2026-10-02' }, opts)).toBe('The window must end by Oct 1, 2026, the last full day.')
    expect(setupProblem({ ...ok, start: '2026-10-01', end: '2026-09-01' }, opts)).toBe('The start must come before the end.')
    expect(setupProblem({ ...ok, start: '2026-09-10' }, opts)).toBe('Daily runs cover 30–90 days; this window is 22.')
    expect(setupProblem({ ...ok, cadence: '4h' }, opts)).toBe('4-hour runs cover 7–14 days; this window is 60.')
    expect(setupProblem({ ...ok, agents: 0 }, opts)).toBe('Pick at least one agent.')
    expect(setupProblem({ ...ok, agents: 5 }, opts)).toBe('Up to 4 agents per run.')
    expect(setupProblem({ ...ok, instruction: '  short  ' }, opts)).toBe('Give your agent an instruction of at least 10 characters.')
    expect(setupProblem({ ...ok, instruction: 'Hold BTC only, always.' }, opts)).toBeNull()
    expect(setupProblem(ok, { ...opts, maxSteps: 30 })).toBe('A run has at most 30 steps.')
  })

  it('labels windows, simulated times and tool calls', () => {
    expect(windowLabel('2026-08-03', '2026-10-01')).toBe('Aug 3 → Oct 1, 2026')
    expect(windowLabel('2025-12-20', '2026-01-31')).toBe('Dec 20, 2025 → Jan 31, 2026')
    expect(windowText({ name: 'Daily · Aug 3 → Oct 1, 2026', cadence: '1d', start: '2026-08-03', end: '2026-10-01' })).toBeNull()
    expect(windowText({ name: 'My run', cadence: '4h', start: '2026-09-18', end: '2026-10-01' })).toBe('4-hour · Sep 18 → Oct 1, 2026')

    const t = Date.parse('2026-08-25T16:00:00Z') / 1000
    expect(simAt(t, '1d')).toBe('2026-08-25')
    expect(simAt(t, '4h')).toBe('2026-08-25 16:00')
    expect(simAt(NaN, '1d')).toBe('—')
    expect(lastStepAt({ cadence: '1d', start: '2026-08-03', stepsDone: 23 })).toBe(Date.parse('2026-08-25T00:00:00Z') / 1000)
    expect(lastStepAt({ cadence: '4h', start: '2026-09-18', stepsDone: 2 })).toBe(Date.parse('2026-09-18T04:00:00Z') / 1000)
    expect(lastStepAt({ cadence: '1d', start: '2026-08-03', stepsDone: 0 })).toBeNull()

    expect(toolLabel({ name: 'get_news', input: { query: 'ETF flows', days: 2 }, ok: true, summary: '' })).toBe('get_news "ETF flows" 2')
    expect(toolLabel({ name: 'get_prices', input: { symbol: 'BTC', interval: '4h', bars: 30 }, ok: true, summary: '' })).toBe('get_prices BTC 4h 30')
    expect(toolLabel({ name: 'place_orders', input: { targets: [{ symbol: 'BTC', weightPct: 50 }] }, ok: true, summary: '' })).toBe('place_orders')
  })
})

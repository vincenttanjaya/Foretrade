import { describe, expect, it } from 'vitest'
import { allocate, rangeStats, shares, sinceStats } from './portfolio'

describe('rangeStats', () => {
  it('withholds annualized volatility for fewer than 31 closed daily bars', () => {
    expect(rangeStats([100, 102, 101].map((v, i) => ({ t: 86400 * i, v })))?.volatilityAnnPct).toBeNull()
  })
  // 100 → 120 → 90 → 108: +8% total, 25% drawdown from the 120 peak
  const pts = [100, 120, 90, 108].map((v, i) => ({ t: i * 86400, v }))
  const s = rangeStats(pts)!

  it('computes return and max drawdown', () => {
    expect(s.returnPct).toBeCloseTo(8, 4)
    expect(s.maxDrawdownPct).toBeCloseTo(25, 4)
  })

  it('computes volatility like the API: sample stdev of daily log returns × √365', () => {
    const r = Array.from({ length: 30 }, (_, i) => Math.log(i % 2 ? 0.99 : 1.02))
    const mean = r.reduce((a, x) => a + x, 0) / r.length
    const sd = Math.sqrt(r.reduce((a, x) => a + (x - mean) ** 2, 0) / 29)
    let v = 100
    const long = [{ t: 0, v }, ...r.map((x, i) => ({ t: (i + 1) * 86400, v: v *= Math.exp(x) }))]
    expect(rangeStats(long)!.volatilityAnnPct).toBeCloseTo(sd * Math.sqrt(365) * 100, 4)
  })

  it('needs two positive points', () => {
    expect(rangeStats(pts.slice(0, 1))).toBeNull()
    expect(rangeStats([{ t: 0, v: 100 }, { t: 1, v: 0 }])).toBeNull()
    expect(rangeStats([{ t: 0, v: 100 }, { t: 1, v: 110 }])?.volatilityAnnPct).toBeNull()
  })
})

describe('allocate', () => {
  // Cents rounding like the API, slices under $10 flagged as skipped
  const holdings = [
    { symbol: 'BTC', weight: 0.5, rationale: '' },
    { symbol: 'ETH', weight: 0.3333, rationale: '' },
    { symbol: 'PAXG', weight: 0.1667, rationale: '' },
  ]

  it('splits the amount by weight', () => {
    expect(allocate(10000, holdings).map((x) => [x.symbol, x.notional, x.skipped])).toEqual([
      ['BTC', 5000, false],
      ['ETH', 3333, false],
      ['PAXG', 1667, false],
    ])
  })

  it('flags slices under the minimum order and zeroes a bad amount', () => {
    expect(allocate(50, holdings).map((x) => x.skipped)).toEqual([false, false, true])
    expect(allocate(NaN, holdings).map((x) => x.notional)).toEqual([0, 0, 0])
  })
})

describe('shares', () => {
  it('renormalizes and ignores non-positive weights', () => {
    expect(
      shares([
        { symbol: 'A', weight: 2, rationale: '' },
        { symbol: 'B', weight: 2, rationale: '' },
        { symbol: 'C', weight: -1, rationale: '' },
      ]),
    ).toEqual([0.5, 0.5, 0])
    expect(shares([{ symbol: 'A', weight: 0, rationale: '' }])).toEqual([0])
  })
})

describe('sinceStats', () => {
  it('reads the return off the 100 cost line and annualizes volatility for hourly bars', () => {
    const series = Array.from({ length: 31 }, (_, i) => ({ t: i * 3600, v: 99.8 + i / 10 }))
    const btc = series.map(({ t }, i) => ({ t, v: 100 - i / 30 }))
    const s = sinceStats(series, btc)!
    expect(s.returnPct).toBeCloseTo(2.8, 6) // fees included: not last / first − 1
    expect(s.benchmarkReturnPct).toBeCloseTo(-1, 6)
    expect(s.volatilityAnnPct).toBeCloseTo(rangeStats(series)!.volatilityAnnPct! * Math.sqrt(24), 6)
    expect(sinceStats(series.slice(0, 1), btc)).toBeNull()
  })

  it('keeps actual returns when comparison is missing or dates differ and withholds volatility across a gap', () => {
    const series = Array.from({ length: 31 }, (_, i) => ({ t: i * 86400, v: 100 + i }))
    expect(sinceStats(series, [])!.benchmarkReturnPct).toBeNull()
    expect(sinceStats(series, [])!.returnPct).toBe(30)
    expect(sinceStats(series, series.map((p) => ({ ...p, t: p.t + 3600 })))!.benchmarkReturnPct).toBeNull()
    series[30].t += 86400
    expect(sinceStats(series, series)!.volatilityAnnPct).toBeNull()
  })
})

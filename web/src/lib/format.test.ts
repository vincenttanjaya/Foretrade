import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import levelCases from './level-format-cases.json'
import { fmtCompact, fmtDate, fmtLevelPrice, fmtPct, fmtPrice, fmtProb, fmtQty, fmtUsd, timeAgo } from './format'

describe('fmtUsd', () => {
  it('formats dollars with cents, sign and compact options', () => {
    expect(fmtUsd(100000)).toBe('$100,000.00')
    expect(fmtUsd(1234.5, { sign: true })).toBe('+$1,234.50')
    expect(fmtUsd(-1234.5, { sign: true })).toBe('-$1,234.50')
    expect(fmtUsd(0, { sign: true })).toBe('$0.00') // zero gets no sign
    expect(fmtUsd(1.23e9, { compact: true })).toBe('$1.2B')
    expect(fmtUsd(100000, { decimals: 0 })).toBe('$100,000')
  })

  it.each([NaN, Infinity, null, undefined])('shows a dash for %s', (n) => {
    expect(fmtUsd(n)).toBe('—')
  })
})

describe('fmtPrice', () => {
  it('follows magnitude', () => {
    expect(fmtPrice(84213.2)).toBe('$84,213.20')
    expect(fmtPrice(2.45131)).toBe('$2.4513')
    expect(fmtPrice(2.5)).toBe('$2.50')
    expect(fmtPrice(0.08123456)).toBe('$0.0812346')
    expect(fmtPrice(-1500)).toBe('-$1,500.00')
    expect(fmtPrice(null)).toBe('—')
  })
})

describe('fmtLevelPrice', () => {
  // The level rule the API shares: level-format-cases.json is the same table the API's TestFormatLevel reads
  // (internal/market), so the card's own levels read like the API's bucket labels, summaries and warnings.
  it.each(levelCases.cases as [number, string][])('%s → %s (the table shared with the API)', (n, s) => {
    expect(fmtLevelPrice(n)).toBe(s)
  })

  it.each([
    [-1500.4, '-$1,500'],
    [2.5e9, '$2.50B'],
    [1.2e9, '$1.20B'], // the API's own extra edges past the shared table: billions and trillions
    [999.7e6, '$1.00B'],
    [4.5e12, '$4.50T'],
    [1.2e15, '$1,200T'],
    [1.2849999999999998e17, '$128,000T'], // a float just under a tie rounds down, as the API's exact roundSig does
    [-0.0882, '-$0.0882'],
    [1.23e-7, '$0.000000123'],
    [NaN, '—'],
    [Infinity, '—'],
    [null, '—'],
    [undefined, '—'],
  ])('%s → %s (outside the shared table: B/T, signs, a dash for no number)', (n, s) => {
    expect(fmtLevelPrice(n)).toBe(s)
  })
})

describe('fmtPct / fmtCompact / fmtQty', () => {
  it('formats percentages that are already ×100', () => {
    expect(fmtPct(12.5, { sign: true })).toBe('+12.50%')
    expect(fmtPct(-3.456, { decimals: 1 })).toBe('-3.5%')
    expect(fmtPct(0, { sign: true })).toBe('0.00%')
    expect(fmtPct(undefined)).toBe('—')
  })

  it('compacts large numbers', () => {
    expect(fmtCompact(1234567)).toBe('1.2M')
    expect(fmtCompact(950)).toBe('950')
    expect(fmtCompact(NaN)).toBe('—')
  })

  it('keeps 4 decimals for whole units and 6 below one', () => {
    expect(fmtQty(1.23456789)).toBe('1.2346')
    expect(fmtQty(0.000123456)).toBe('0.000123')
    expect(fmtQty(2)).toBe('2')
    expect(fmtQty(null)).toBe('—')
  })
})

describe('fmtProb', () => {
  it('rounds to whole percent, with edges for near-certain outcomes', () => {
    expect(fmtProb(0.62)).toBe('62%')
    expect(fmtProb(0.001)).toBe('<1%')
    expect(fmtProb(0.005)).toBe('1%')
    expect(fmtProb(0.996)).toBe('>99%')
    expect(fmtProb(0)).toBe('0%')
    expect(fmtProb(1)).toBe('100%')
    expect(fmtProb(null)).toBe('—')
  })
})

describe('timeAgo', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-10-02T12:00:00Z'))
  })
  afterEach(() => vi.useRealTimers())

  it('reads relative time, then falls back to a date', () => {
    expect(timeAgo('2026-10-02T11:59:30Z')).toBe('just now')
    expect(timeAgo('2026-10-02T11:48:00Z')).toBe('12m ago')
    expect(timeAgo('2026-10-02T09:00:00Z')).toBe('3h ago')
    expect(timeAgo('2026-09-28T12:00:00Z')).toBe('4d ago')
    expect(timeAgo('2026-09-01T12:00:00Z')).toBe('Sep 1, 2026')
  })

  it.each([null, undefined, '', 'not a date'])('shows a dash for %s', (iso) => {
    expect(timeAgo(iso)).toBe('—')
  })
})

describe('fmtDate', () => {
  // The suite runs in America/Los_Angeles (vite.config.ts), west of UTC.
  it('does not shift date-only strings by time zone', () => {
    expect(fmtDate('2026-10-02')).toBe('Oct 2, 2026')
  })

  it('shows local date and time for instants', () => {
    expect(fmtDate('2026-10-02T00:30:00Z')).toBe('Oct 1, 2026')
    expect(fmtDate('2026-10-02T15:05:00Z', { time: true })).toBe('Oct 2, 2026, 8:05 AM')
    expect(fmtDate('2026-10-02', { time: true })).toBe('Oct 2, 2026') // a calendar date has no time
  })

  it.each([null, undefined, 'garbage'])('shows a dash for %s', (iso) => {
    expect(fmtDate(iso)).toBe('—')
  })
})

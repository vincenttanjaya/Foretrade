import { describe, expect, it } from 'vitest'
import type { ScreenerSortKey } from '../../lib/types'
import * as fx from '../../test/fixtures'
import {
  breakBeforeTime,
  chunk,
  delayText,
  everyText,
  liveHigh,
  mergeRows,
  orderMoved,
  pollMs,
  quoteLabel,
  quoteStale,
  quoteTitle,
  refreshText,
  refreshTickers,
  sessionLine,
  sortValue,
  staleTitle,
  utcDay,
  utcSeconds,
} from './live'

const at = (iso: string) => Date.parse(iso)

describe('screener live quotes', () => {
  it('labels a quote from its delay: real-time only at 0 minutes, delayed otherwise, unknown included', () => {
    expect(quoteLabel({ delayMinutes: 0, lastTradeAt: '2026-10-14T06:29:58Z' })).toBe('real-time · 06:29:58 UTC')
    expect(quoteLabel({ delayMinutes: 15, lastTradeAt: '2026-10-14T06:29:58Z' })).toBe('delayed · last trade 06:29 UTC')
    expect(quoteLabel({ delayMinutes: null, lastTradeAt: '2026-10-14T06:29:58Z' })).toBe('delayed · last trade 06:29 UTC')
    expect(quoteLabel({ delayMinutes: 0.4, lastTradeAt: '2026-10-14T06:29:58Z' })).toBe('delayed · last trade 06:29 UTC')
    expect(quoteLabel({ delayMinutes: 0, lastTradeAt: 'not a time' })).toBe('real-time')
    expect(quoteLabel({ delayMinutes: 20, lastTradeAt: '' })).toBe('delayed')
  })

  it('wraps a label only before its time', () => {
    expect(breakBeforeTime('delayed · last trade 06:30 UTC')).toBe('delayed\u00a0·\u00a0last\u00a0trade 06:30\u00a0UTC')
    expect(breakBeforeTime('real-time · 06:29:58 UTC')).toBe('real-time\u00a0· 06:29:58\u00a0UTC')
    expect(breakBeforeTime('delayed')).toBe('delayed')
  })

  it('is stale when the API said so, or once this clock passes staleAt', () => {
    const q = fx.screenerQuote({ staleAt: '2026-10-14T07:32:00Z' })
    expect(quoteStale(q, at('2026-10-14T07:31:59Z'))).toBe(false)
    expect(quoteStale(q, at('2026-10-14T07:32:00Z'))).toBe(true)
    expect(quoteStale({ ...q, stale: true }, at('2026-10-14T07:00:00Z'))).toBe(true)
    expect(quoteStale({ ...q, staleAt: 'garbled' }, at('2030-01-01T00:00:00Z'))).toBe(false) // only the API's flag then
  })

  it('says the session and the last trade’s day under the label', () => {
    expect(sessionLine({ session: 'open', lastTradeAt: '2026-10-14T06:58:41Z' })).toBe('Open · Oct 14')
    expect(sessionLine({ session: 'closed', lastTradeAt: '2026-10-09T06:30:00Z' })).toBe('Closed · Oct 9')
    expect(sessionLine({ session: 'unknown', lastTradeAt: 'x' })).toBe('Session unknown')
    expect(sessionLine({ session: 'halted' as never, lastTradeAt: '2026-10-14T06:58:41Z' })).toBe('Session unknown · Oct 14')
    expect([utcDay(null), utcSeconds(undefined)]).toEqual(['', ''])
  })

  it('explains the source, its delay and the tier in the tooltip, and since when a stale quote was not read', () => {
    const q = fx.screenerQuote()
    expect(quoteTitle(q)).toBe('Yahoo Finance · about 20 min behind the exchange (measured) · read 06:55 UTC, read again at the next open')
    expect(quoteTitle({ ...q, source: 'jpx', readAt: '', refreshSeconds: 60 })).toBe('jpx · about 20 min behind the exchange (measured) · re-read every minute')
    expect(staleTitle({ ...q, readAt: '2026-10-14T06:35:01Z', refreshSeconds: 900 })).toBe(
      'No newer quote since 06:35 UTC (re-read every 15 min): the price and the columns from it may be out of date.')
    expect(staleTitle({ ...q, readAt: '' })).toBe('No newer quote (read again at the next open): the price and the columns from it may be out of date.')
    expect([refreshText(300), refreshText(30), refreshText(0), refreshText(null)]).toEqual(['re-read every 5 min', 're-read every 30 s', 'read again at the next open', 'read again at the next open'])
    expect([
      delayText({ delayMinutes: 10, delayBasis: 'stated' }),
      delayText({ delayMinutes: 15.4, delayBasis: null }),
      delayText({ delayMinutes: 0.3, delayBasis: 'measured' }),
      delayText({ delayMinutes: 0, delayBasis: 'stated' }),
      delayText({ delayMinutes: null, delayBasis: null }),
    ]).toEqual(['about 10 min behind the exchange (as the source states)', 'about 15 min behind the exchange', 'under a minute behind the exchange (measured)', 'real-time', 'delay unknown'])
  })

  it('reads a new high from the live price by its trading day, not the run’s week', () => {
    const [, , samsung, tsmc] = fx.screenerRows
    expect(liveHigh(tsmc)).toBe(true)
    expect(liveHigh(samsung)).toBe(false)
    expect(liveHigh({ ...tsmc, quote: null })).toBe(false)
    expect(liveHigh({ ...tsmc, athAt: '2026-10-12' })).toBe(false) // the run's own high at exactly the price keeps its week
  })

  it('refreshes every loaded row with data, in chunks, and merges the answers in place', () => {
    expect(refreshTickers(fx.screenerRows)).toEqual(fx.screenerRows.filter((r) => r.status !== 'unavailable').map((r) => r.ticker))
    expect(chunk([1, 2, 3, 4, 5], 2)).toEqual([[1, 2], [3, 4], [5]])
    expect(chunk([], 100)).toEqual([])
    const rows = fx.screenerRows.slice(0, 3)
    const moved = { ...rows[2], price: 280000 }
    expect(mergeRows(rows, [moved, fx.listedScreenerRow])).toEqual([rows[0], rows[1], moved]) // a row not loaded is not added
    expect(mergeRows(rows, [])).toBe(rows)
  })

  it('knows when a refresh could change the order or the counts', () => {
    const rows = fx.screenerRows
    const samsung = rows[2]
    const keys: ScreenerSortKey[] = ['price', 'priceUsd', 'pctVsSma', 'direction', 'rsi', 'drawdown', 'athAt', 'updated', 'ticker', 'name', 'market']
    expect(keys.map((k) => sortValue(samsung, k))).toEqual([272000, 203.22, 160.39, 160.39 - 166.93, 54.22, -27.37, '2026-06-15', '2026-10-14T06:30:00Z',
      '005930.KS', '005930.KS', '005930.KS'])
    expect(sortValue({ ...samsung, quote: null }, 'updated')).toBe('2026-10-13T10:52:00Z')
    expect(sortValue({ ...samsung, pctVsSmaPrev: null }, 'direction')).toBeNull()
    expect(orderMoved(rows, [{ ...samsung }], 'pctVsSma')).toBe(false)
    expect(orderMoved(rows, [{ ...samsung, priceUsd: 204 }], 'priceUsd')).toBe(true)
    expect(orderMoved(rows, [{ ...samsung, priceUsd: 204 }], 'ticker')).toBe(false)
    expect(orderMoved(rows, [{ ...samsung, pctVsSma: 161 }], 'ticker')).toBe(true) // the bands, so the tab counts, read it
    expect(orderMoved(rows, [{ ...fx.listedScreenerRow, pctVsSma: 1 }], 'ticker')).toBe(false) // not loaded
  })

  it('reads the page’s refresh interval, within 15 s and 10 min', () => {
    expect([pollMs(60), pollMs(1), pollMs(3600), pollMs(0), pollMs(undefined), pollMs(Number.NaN), pollMs(null)]).toEqual([60_000, 15_000, 600_000, null, null, null, null])
    expect([everyText(60), everyText(120), everyText(45)]).toEqual(['every minute', 'every 2 min', 'every 45 s'])
  })
})

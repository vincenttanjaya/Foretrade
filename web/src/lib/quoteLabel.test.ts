import { describe, expect, it } from 'vitest'
import { feedAsset } from '../test/fixtures'
import { basisText, behindMinutes, computedLabel, delaySeconds, feedQuote, freshness, freshnessTag, priceLabel, realtimeOpen, sourceName, utcTime } from './quoteLabel'

const at = '2026-10-15T04:20:15Z'

describe('computedLabel: the API’s rule from the quote’s class and measured delay', () => {
  it('says real-time only for class realtime under a minute behind, with the trade’s seconds', () => {
    expect(computedLabel({ class: 'realtime', source: 'naver', lastTradeAt: at, delaySeconds: 2 })).toBe('real-time · 04:20:15 UTC')
    expect(computedLabel({ class: 'realtime', source: 'tencent', lastTradeAt: at, delaySeconds: 59.9 })).toBe('real-time · 04:20:15 UTC')
    expect(computedLabel({ class: 'realtime', source: 'vietcap', lastTradeAt: at, delaySeconds: 0 })).toBe('real-time · 04:20:15 UTC')
    expect(computedLabel({ class: 'realtime', source: 'mis', lastTradeAt: at, delayMinutes: 0.25 })).toBe('real-time · 04:20:15 UTC') // minutes when no seconds
  })

  it('says about N min behind up to 5 minutes, and for near-realtime (SGX’s snapshot) even under a minute', () => {
    expect(computedLabel({ class: 'near-realtime', source: 'sgx', lastTradeAt: at, delaySeconds: 99 })).toBe('about 2 min behind · 04:20 UTC')
    expect(computedLabel({ class: 'near-realtime', source: 'sgx', lastTradeAt: at, delaySeconds: 20 })).toBe('about 1 min behind · 04:20 UTC')
    expect(computedLabel({ class: 'realtime', source: 'tencent', lastTradeAt: at, delaySeconds: 60 })).toBe('about 1 min behind · 04:20 UTC')
    expect(computedLabel({ class: 'realtime', source: 'mis', lastTradeAt: at, delaySeconds: 300 })).toBe('about 5 min behind · 04:20 UTC')
  })

  it('says delayed with the last trade past 5 minutes, for class delayed (Yahoo), and when the delay is unknown; never live', () => {
    expect(computedLabel({ class: 'realtime', source: 'naver', lastTradeAt: at, delaySeconds: 301 })).toBe('delayed · last trade 04:20 UTC')
    expect(computedLabel({ class: 'delayed', source: 'yahoo', lastTradeAt: at, delaySeconds: 2 })).toBe('delayed · last trade 04:20 UTC')
    expect(computedLabel({ class: 'delayed', source: 'yahoo', lastTradeAt: at, delayMinutes: 20 })).toBe('delayed · last trade 04:20 UTC')
    expect(computedLabel({ class: 'realtime', source: 'naver', lastTradeAt: at, delaySeconds: null })).toBe('delayed · last trade 04:20 UTC')
    for (const q of [{ class: 'realtime', delaySeconds: 1 }, { class: 'near-realtime', delaySeconds: 70 }, { class: 'delayed', delaySeconds: 1200 }] as const)
      expect(computedLabel({ ...q, lastTradeAt: at })).not.toMatch(/\blive\b/i)
  })

  it('keeps the live screener’s rule for a quote without a class, and drops a time that does not parse', () => {
    expect(computedLabel({ source: 'exchange-feed', lastTradeAt: at, delayMinutes: 0 })).toBe('real-time · 04:20:15 UTC')
    expect(computedLabel({ lastTradeAt: at, delayMinutes: 0 })).toBe('real-time · 04:20:15 UTC')
    expect(computedLabel({ source: 'yahoo', lastTradeAt: at, delayMinutes: 0 })).toBe('delayed · last trade 04:20 UTC')
    expect(computedLabel({ lastTradeAt: at, delayMinutes: 0.4 })).toBe('delayed · last trade 04:20 UTC')
    expect(computedLabel({ class: 'realtime', lastTradeAt: 'not a time', delaySeconds: 1 })).toBe('real-time')
    expect(computedLabel({ class: 'near-realtime', lastTradeAt: '', delaySeconds: 70 })).toBe('about 1 min behind')
    expect(computedLabel({ class: 'delayed', lastTradeAt: '', delaySeconds: 70 })).toBe('delayed')
  })
})

describe('priceLabel', () => {
  it('shows the API’s own label as sent, and computes one only when none came', () => {
    expect(priceLabel({ class: 'realtime', lastTradeAt: at, delaySeconds: 2, label: 'real-time · 04:20:15 UTC' })).toBe('real-time · 04:20:15 UTC')
    expect(priceLabel({ class: 'realtime', lastTradeAt: at, delaySeconds: 2, label: ' about 1 min behind · 04:20 UTC ' })).toBe('about 1 min behind · 04:20 UTC')
    expect(priceLabel({ class: 'realtime', lastTradeAt: at, delaySeconds: 2, label: '' })).toBe('real-time · 04:20:15 UTC')
    expect(priceLabel({ class: 'delayed', lastTradeAt: at, label: null })).toBe('delayed · last trade 04:20 UTC')
  })
})

describe('the label’s parts', () => {
  it('reads the delay from seconds first, then minutes, never below zero', () => {
    expect(delaySeconds({ delaySeconds: 4, delayMinutes: 20 })).toBe(4)
    expect(delaySeconds({ delayMinutes: 1.5 })).toBe(90)
    expect(delaySeconds({ delaySeconds: -2 })).toBe(0)
    expect(delaySeconds({ delaySeconds: Number.NaN, delayMinutes: null })).toBeNull()
    expect(delaySeconds({})).toBeNull()
    expect([behindMinutes({ delaySeconds: 151 }), behindMinutes({ delaySeconds: 20 }), behindMinutes({})]).toEqual([3, 1, 1])
  })

  it('classes freshness and tags it', () => {
    expect(freshness({ class: 'realtime', lastTradeAt: at, delaySeconds: 3 })).toBe('realtime')
    expect(freshness({ class: 'near-realtime', lastTradeAt: at, delaySeconds: 3 })).toBe('behind')
    expect(freshness({ class: 'delayed', lastTradeAt: at, delaySeconds: 3 })).toBe('delayed')
    expect([freshnessTag('realtime'), freshnessTag('behind', 2), freshnessTag('behind'), freshnessTag('delayed')]).toEqual([
      'real-time', 'about 2 min behind', 'about 1 min behind', 'delayed',
    ])
  })

  it('names the sources and the time basis, and prints UTC times', () => {
    expect(['naver', 'tencent', 'vietcap', 'sgx', 'mis', 'yahoo-tw', 'yahoo', 'other', null].map(sourceName)).toEqual([
      'Naver Finance', 'Tencent', 'Vietcap', 'SGX', 'TWSE MIS', 'Yahoo Taiwan', 'Yahoo Finance', 'other', 'Yahoo Finance',
    ])
    expect(basisText('trade')).toBe("the time is the source's own last trade")
    expect(basisText('volume')).toMatch(/^the source gives no trade time/)
    expect(basisText('snapshot')).toMatch(/snapshot minute/)
    expect(basisText(null)).toBe('')
    expect([utcTime(at), utcTime(at, true), utcTime('x'), utcTime(null)]).toEqual(['04:20 UTC', '04:20:15 UTC', '', ''])
  })

  it('reads a market asset’s quote, today’s API (no source) as Yahoo’s delayed trade', () => {
    const q = feedAsset('DBS', 'DBS Group').quoteFeed!
    expect(feedQuote(q)).toEqual({ source: 'yahoo', class: 'delayed', lastTradeAt: q.asOf, delaySeconds: null, timeBasis: null, label: null, readAt: null })
    const rt = { ...q, marketState: 'open' as const, source: 'naver', class: 'realtime' as const, lastTradeAt: at, readAt: at, delaySeconds: 1, timeBasis: 'volume' as const, label: 'real-time · 04:20:15 UTC' }
    expect(feedQuote(rt)).toMatchObject({ source: 'naver', class: 'realtime', delaySeconds: 1, timeBasis: 'volume', label: 'real-time · 04:20:15 UTC', readAt: at })
    expect([realtimeOpen(rt), realtimeOpen({ ...rt, marketState: 'closed' }), realtimeOpen({ ...rt, class: 'near-realtime' }), realtimeOpen(q), realtimeOpen(null)]).toEqual([
      true, false, false, false, false,
    ])
  })
})

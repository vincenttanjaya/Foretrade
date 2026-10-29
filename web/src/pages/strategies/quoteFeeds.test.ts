import { describe, expect, it } from 'vitest'
import type { Asset } from '../../lib/types'
import { feedAsset } from '../../test/fixtures'
import { chartBars, feedPriceLine, feedPriceState, feedQuote, feedQuoteTitle, groupFreshness, isQuoteFeed, movesBars, openQuoteLabel, realtimeOpen, sourceShort, YAHOO_DELAY_MINUTES, QUOTE_FEED_LISTINGS, QUOTE_FEED_SYMBOLS, quoteFeedAsset, quoteFeedFx, quoteFeedNote, quoteFeedSession, utcClock, venueName } from './quoteFeeds'
import { isStockPerp, SYMBOLS } from './strategy'

describe('quote-feed rows', () => {
  it('follow every perp in the API order, unique, with their listing', () => {
    expect(QUOTE_FEED_SYMBOLS).toHaveLength(138)
    expect(SYMBOLS.slice(-138)).toEqual(QUOTE_FEED_SYMBOLS)
    expect(new Set(SYMBOLS.map(([s]) => s)).size).toBe(SYMBOLS.length)
    expect([QUOTE_FEED_SYMBOLS[0][0], QUOTE_FEED_SYMBOLS[66][0], QUOTE_FEED_SYMBOLS[67][0], QUOTE_FEED_SYMBOLS[137][0]]).toEqual(['HSDT', 'DELTA', 'KEPPEL', 'LPL'])
    expect(QUOTE_FEED_SYMBOLS.some(([s]) => isStockPerp(s))).toBe(false)
    const listings = Object.values(QUOTE_FEED_LISTINGS)
    expect(listings.filter((l) => l.region === 'us')).toEqual([QUOTE_FEED_LISTINGS.HSDT])
    expect(new Set(listings.map((l) => l.yahoo)).size).toBe(138)
  })

  it('take each venue’s currency, except the SGX lines SGX quotes in USD', () => {
    const ccy: Record<string, string> = { NYSE: 'USD', SGX: 'SGD', IDX: 'IDR', SET: 'THB', BURSA: 'MYR', HOSE: 'VND', HKEX: 'HKD', TSE: 'JPY', KRX: 'KRW', TWSE: 'TWD' }
    const odd = Object.entries(QUOTE_FEED_LISTINGS).filter(([, l]) => ccy[l.sessionVenue] !== l.localCurrency).map(([s]) => s)
    expect(odd).toEqual(['JARDINE', 'HKLAND'])
    // Nasdaq listings follow the NYSE (US) session.
    expect(['KARO', 'KLIC', 'HIMX', 'SIMO'].map((s) => QUOTE_FEED_LISTINGS[s].sessionVenue)).toEqual(['NYSE', 'NYSE', 'NYSE', 'NYSE'])
  })

  it('count the Singapore, Taiwan and Korea focus rows', () => {
    const focus = QUOTE_FEED_SYMBOLS.slice(67).map(([s]) => QUOTE_FEED_LISTINGS[s].yahoo)
    expect([focus.filter((y) => y.endsWith('.SI')).length, focus.filter((y) => y.endsWith('.TW')).length, focus.filter((y) => y.endsWith('.KS')).length]).toEqual([18, 22, 22])
    expect(focus.filter((y) => !y.includes('.'))).toEqual(['KARO', 'KLIC', 'CHT', 'HIMX', 'SIMO', 'KEP', 'SKM', 'KT', 'LPL'])
  })
})

describe('quote-feed labels', () => {
  const closed = feedAsset('DBS', 'DBS Group')
  it('names the feed by the API venue, else the configured table', () => {
    expect([quoteFeedAsset('DBS', closed), quoteFeedAsset('DBS'), quoteFeedAsset('BTC'), quoteFeedAsset('DBS', { venue: 'binance' })]).toEqual([true, true, false, false])
    expect([isQuoteFeed('HSDT'), isQuoteFeed('TSM')]).toEqual([true, false])
    expect([venueName('NYSE'), venueName('BURSA'), venueName('SGX'), venueName(null)]).toEqual(['US market', 'Bursa Malaysia', 'SGX', 'exchange'])
  })

  it('says when fills use the last close, with the quote time', () => {
    expect(quoteFeedNote(closed)).toBe('Quote feed: prices follow SGX hours. Outside them, as now, paper fills use the last close (as of Oct 5, 20:00 UTC).')
    const open: Asset = { ...closed, quoteFeed: { ...closed.quoteFeed!, marketState: 'open', asOf: '2026-10-06T03:15:00Z' } }
    expect(quoteFeedNote(open)).toBe(
      "Quote feed: prices follow SGX hours. It is open now, so fills use Yahoo's latest trade, which is delayed (last trade Oct 6, 03:15 UTC); outside its hours they use the last close.",
    )
    expect(quoteFeedNote(open)).not.toMatch(/\blive\b/)
    expect(quoteFeedNote({ sessionVenue: 'IDX', quoteFeed: { ...closed.quoteFeed!, marketState: 'unknown' } })).toContain('prices follow IDX hours; outside them paper fills use the last close')
    expect(quoteFeedNote(undefined)).toBe('Quote feed: prices follow exchange hours; outside them paper fills use the last close.')
  })

  it('calls an open quote from an API without sources delayed with its last trade time, never live', () => {
    const q = { ...closed.quoteFeed!, marketState: 'open' as const, asOf: '2026-10-13T01:12:40Z' }
    expect(openQuoteLabel(q)).toBe('delayed · last trade 01:12 UTC')
    expect(openQuoteLabel({ ...q, asOf: '' })).toBe('delayed')
    expect(feedQuote(q)).toMatchObject({ source: 'yahoo', class: 'delayed', lastTradeAt: '2026-10-13T01:12:40Z', delaySeconds: null, label: null })
    expect([feedPriceState(q), feedPriceState(closed.quoteFeed!), feedPriceState({ ...q, marketState: 'unknown' })]).toEqual([
      'delayed · last trade 01:12 UTC', 'last close', 'last quote',
    ])
    expect([realtimeOpen(q), sourceShort(q)]).toEqual([false, 'Yahoo'])
    expect([utcClock('2026-10-13T23:59:59Z'), utcClock('not a time'), utcClock(null)]).toEqual(['23:59 UTC', '', ''])
  })

  it('labels a real-time source by its own trade time and delay, and says what open fills use', () => {
    const krx = feedAsset('SAMBIO', 'Samsung Biologics', 0, { session: 'open' })
    const rt = { ...krx.quoteFeed!, marketState: 'open' as const, source: 'naver', class: 'realtime' as const, asOf: '2026-10-15T04:20:15Z', lastTradeAt: '2026-10-15T04:20:15Z', readAt: '2026-10-15T04:20:17Z', delaySeconds: 2, timeBasis: 'volume' as const }
    expect(openQuoteLabel(rt)).toBe('real-time · 04:20:15 UTC')
    expect([realtimeOpen(rt), realtimeOpen({ ...rt, marketState: 'closed' }), realtimeOpen(null), sourceShort(rt)]).toEqual([true, false, false, 'Naver'])
    expect(quoteFeedNote({ ...krx, quoteFeed: rt })).toBe(
      "Quote feed: prices follow KRX hours. It is open now, so fills use Naver Finance's real-time price (last trade Oct 15, 04:20:15 UTC); outside its hours they use the last close.",
    )
    // SGX's once-a-minute snapshot: about N min behind, never real-time; its time is the snapshot's.
    const sgx = { ...closed.quoteFeed!, marketState: 'open' as const, source: 'sgx', class: 'near-realtime' as const, lastTradeAt: '2026-10-15T04:18:00Z', delaySeconds: 99, timeBasis: 'snapshot' as const }
    expect(openQuoteLabel(sgx)).toBe('about 2 min behind · 04:18 UTC')
    expect([realtimeOpen(sgx), sourceShort(sgx)]).toEqual([false, 'SGX'])
    expect(quoteFeedNote({ ...closed, quoteFeed: sgx })).toBe(
      "Quote feed: prices follow SGX hours. It is open now, so fills use SGX's price, about 2 min behind the exchange (snapshot of Oct 15, 04:18 UTC); outside its hours they use the last close.",
    )
    // A real-time source that fell back to Yahoo says Yahoo's delayed trade again; a late one says how late.
    expect(quoteFeedNote({ ...krx, quoteFeed: { ...rt, source: 'yahoo', class: 'delayed', delaySeconds: 1203 } })).toContain("fills use Yahoo's latest trade, which is delayed (last trade Oct 15, 04:20 UTC)")
    expect(openQuoteLabel({ ...rt, delaySeconds: 200 })).toBe('about 3 min behind · 04:20 UTC')
    expect(openQuoteLabel({ ...rt, delaySeconds: 200, label: 'about 3 min behind · 04:20 UTC' })).toBe('about 3 min behind · 04:20 UTC') // the API's, as sent
    expect(quoteFeedNote({ ...krx, quoteFeed: { ...rt, delaySeconds: 200 } })).toContain("Naver Finance's price, about 3 min behind the exchange (last trade Oct 15, 04:20 UTC)")
    expect(sourceShort({ ...rt, source: 'yahoo-tw' })).toBe('Yahoo Taiwan')
    expect(sourceShort(undefined)).toBe('Yahoo')
    expect(YAHOO_DELAY_MINUTES).toMatchObject({ KRX: 20, TWSE: 20, HKEX: 15, HOSE: 15, SGX: 10, TSE: 15, SET: 30 })
  })

  it("names the session from the venue's calendar, else Yahoo's state where no calendar is verified", () => {
    expect(quoteFeedSession(closed)).toBe('SGX session closed')
    const bbca = feedAsset('BBCA', 'Bank Central Asia', 0, { session: null })
    expect(quoteFeedSession(bbca)).toBe('IDX session closed · per Yahoo') // IDX has no verified calendar: session is always null
    expect(quoteFeedSession({ ...bbca, quoteFeed: { ...bbca.quoteFeed!, marketState: 'open' } })).toBe('IDX session open · per Yahoo')
    expect(quoteFeedSession({ ...bbca, quoteFeed: { ...bbca.quoteFeed!, marketState: 'unknown' } })).toBe('IDX session unknown')
    expect(quoteFeedSession(undefined)).toBe('Exchange session unknown')
  })

  it('states the Yahoo FX rate for a non-USD listing only', () => {
    expect(quoteFeedFx(closed)).toBe('SGD→USD at Yahoo FX: 1 SGD = $0.7822 (as of Oct 6, 08:58 UTC)')
    expect(quoteFeedFx(feedAsset('HSDT', 'Solana Company'))).toBe('')
    expect(quoteFeedFx(feedAsset('JARDINE', 'Jardine Matheson Holdings'))).toBe('') // SGX quotes it in USD
    expect(quoteFeedFx({ localCurrency: 'KRW', quoteFeed: null })).toBe('KRW→USD at Yahoo FX')
  })
})

describe('real-time quote lines, the chart’s bars and the venue tags', () => {
  const base = feedAsset('SAMBIO', 'Samsung Biologics', 0, { session: 'open' })
  const q = (over: Partial<NonNullable<Asset['quoteFeed']>> = {}) => ({
    ...base.quoteFeed!, marketState: 'open' as const, source: 'naver', class: 'realtime' as const, asOf: '2026-10-15T04:20:15Z',
    lastTradeAt: '2026-10-15T04:20:15Z', readAt: '2026-10-15T04:20:17Z', delaySeconds: 2, timeBasis: 'volume' as const, label: 'real-time · 04:20:15 UTC', ...over,
  })
  const sgx = (over = {}) => q({ source: 'sgx', class: 'near-realtime', lastTradeAt: '2026-10-15T04:18:00Z', delaySeconds: 99, timeBasis: 'snapshot', label: 'about 2 min behind · 04:18 UTC', ...over })
  const yahoo = (over = {}) => q({ source: 'yahoo', class: 'delayed', lastTradeAt: '2026-10-15T04:00:15Z', delaySeconds: 1202, timeBasis: 'trade', label: 'delayed · last trade 04:00 UTC', ...over })

  it('puts the label under the price in session and the last close out of it, with a tooltip on the source', () => {
    expect(feedPriceLine(q())).toBe('real-time · 04:20:15 UTC')
    expect(feedPriceLine(q({ marketState: 'closed', asOf: '2026-10-14T06:30:00Z' }))).toBe('last close · Oct 14, 06:30 UTC')
    expect(feedPriceLine(q({ marketState: 'unknown', asOf: '' }))).toBe('last quote')
    expect(feedQuoteTitle(q())).toBe(
      'Naver Finance, a real-time source; 2 s behind the exchange when read at 04:20:17 UTC; the source gives no trade time: the time is the first read that showed the current volume',
    )
    expect(feedQuoteTitle(sgx())).toBe("SGX, a snapshot about once a minute; about 2 min behind the exchange when read at 04:20:17 UTC; the time is the source's snapshot minute, shared by every stock in it")
    expect(feedQuoteTitle(base.quoteFeed!)).toBe('Yahoo Finance, a delayed feed') // today's API: nothing more to say
  })

  it('moves the chart’s bars with a real-time or snapshot quote, in session and after it, never with Yahoo’s', () => {
    expect([movesBars(q()), movesBars(sgx()), movesBars(yahoo()), movesBars(q({ marketState: 'closed' })), movesBars(base.quoteFeed), movesBars(null)]).toEqual([
      true, true, false, true, false, false,
    ])
    // after the close the settled close is still ahead of Yahoo's delayed bars: the last bar ends at it
    expect(chartBars({ ...base, quoteFeed: q({ marketState: 'closed', lastTradeAt: '2026-10-15T06:30:00Z', delaySeconds: 0, label: 'real-time · 06:30:00 UTC' }) }, '1h', true)).toEqual({
      bars: 'Yahoo bars · 20 min delayed',
      last: 'last bar at the last trade',
      title: "The bars are Yahoo's 1h bars in USD, about 20 min behind the exchange (measured), reloaded every 30 s. The last bar ends at Naver Finance's last trade (Oct 15, 06:30:00 UTC), which Yahoo's delayed bars may not show yet.",
    })
    expect(chartBars({ ...base, quoteFeed: q() }, '15m', true)).toEqual({
      bars: 'Yahoo bars · 20 min delayed',
      last: 'last bar real-time',
      title: "The bars are Yahoo's 15m bars in USD, about 20 min behind the exchange (measured), reloaded every 30 s. The last bar moves with Naver Finance's price (real-time · 04:20:15 UTC), read every 5 s: its high and low are only the prices read here.",
    })
    const dbs = feedAsset('DBS', 'DBS Group')
    expect(chartBars({ ...dbs, quoteFeed: sgx() }, '1h', true)).toMatchObject({ bars: 'Yahoo bars · 10 min delayed', last: 'last bar about 2 min behind' })
    expect(chartBars({ ...dbs, quoteFeed: sgx() }, '1h', true)!.title).not.toContain('every 5 s') // SGX is read at the 10 s pace
    expect(chartBars(feedAsset('TOKYOELEC', 'Tokyo Electron'), '1d', false)).toEqual({
      bars: 'Yahoo bars · 15 min delayed',
      title: "The bars are Yahoo's 1d bars in USD, about 15 min behind the exchange (measured), reloaded every 30 s.",
    })
    expect(chartBars(feedAsset('HSDT', 'Solana Company'), '1h', false)).toBeUndefined() // no measured delay for the US: today's badge
    expect(chartBars(undefined, '1h', false)).toBeUndefined()
  })

  it('tags a venue group by what most of its open prices are, closed when none is, and as before on today’s API', () => {
    const rows = (...quotes: NonNullable<Asset['quoteFeed']>[]) => quotes.map((quoteFeed) => ({ quoteFeed }))
    expect(groupFreshness(rows(q(), q(), q({ delaySeconds: 200 })))).toEqual({
      tag: 'real-time', title: "2 of 3 open prices here are real-time (from Naver Finance). Each row's tooltip has its own label.",
    })
    expect(groupFreshness(rows(sgx(), sgx({ delaySeconds: 41 })))).toMatchObject({ tag: 'about 1 min behind' })
    expect(groupFreshness(rows(yahoo(), yahoo()))).toMatchObject({ tag: 'delayed', title: "2 of 2 open prices here are delayed (from Yahoo Finance). Each row's tooltip has its own label." })
    expect(groupFreshness(rows(q({ marketState: 'closed' }), q({ marketState: 'closed' })))).toEqual({
      tag: 'closed', title: "Closed now: these prices are the last close (Naver Finance). Each row's tooltip has its own label.",
    })
    expect(groupFreshness(rows(q(), yahoo()))).toMatchObject({ tag: 'real-time' }) // a tie goes to the fresher
    expect(groupFreshness(rows(base.quoteFeed!))).toEqual({ tag: 'delayed', title: 'Yahoo quotes reach us delayed; each one shows its last trade time' })
    expect(groupFreshness([{ quoteFeed: null }])).toBeNull()
  })
})

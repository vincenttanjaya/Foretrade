import { describe, expect, it } from 'vitest'
import type { Candle, MarketBrief, NewsItem } from '../../lib/types'
import { listedAsset, universeAssets } from '../../test/fixtures'
import {
  checkTicket,
  FEE_RATE,
  isHttpUrl,
  klineCandle,
  linkable,
  marketSearchText,
  MAX_FILL,
  BAR_SECONDS,
  foldTick,
  mergeCandle,
  tickBarTime,
  planAmount,
  pricePrecision,
  resultingQty,
  roundPrice,
  sentimentFor,
  sma,
  type TicketInput,
} from './util'

describe('klineCandle', () => {
  it('parses a Binance kline frame', () => {
    const frame = '{"e":"kline","k":{"t":1759363200000,"o":"100.5","h":"101","l":"99","c":"100.9","v":"12.5","x":false}}'
    expect(klineCandle(frame)).toEqual({ t: 1759363200, o: 100.5, h: 101, l: 99, c: 100.9, v: 12.5 })
  })

  it.each([
    ['missing fields', '{"e":"kline","k":{"t":1759363200000,"o":"x"}}'],
    ['garbage', 'not json'],
    ['non-kline frame', '{"result":null,"id":1}'],
    ['zero time', '{"k":{"t":0,"o":"1","h":"1","l":"1","c":"1","v":"1"}}'],
  ])('rejects %s', (_, raw) => {
    expect(klineCandle(raw)).toBeNull()
  })
})

describe('foldTick', () => {
  const bar = (t: number, c: number): Candle => ({ t, o: c, h: c + 1, l: c - 1, c, v: 5 })
  const H = 3600

  it('moves the last bar inside its span and opens a new bar after it, as a socket kline would', () => {
    const series = [bar(0, 10), bar(H, 11)]
    expect(foldTick(series, { t: H + 600, price: 13 }, H)).toBe(true) // inside the last bar: close, and the high stretches
    expect(series.at(-1)).toEqual({ t: H, o: 11, h: 13, l: 10, c: 13, v: 5 })
    expect(foldTick(series, { t: H + 700, price: 9 }, H)).toBe(true)
    expect(series.at(-1)).toEqual({ t: H, o: 11, h: 13, l: 9, c: 9, v: 5 })
    // 20 minutes past a delayed last bar: a new bar at the tick's own bar time, no volume (the quote has none).
    expect(foldTick(series, { t: 2 * H + 1200, price: 12 }, H)).toBe(true)
    expect(series.at(-1)).toEqual({ t: 2 * H, o: 12, h: 12, l: 12, c: 12, v: 0 })
    expect(series).toHaveLength(3)
  })

  it('ignores a tick before the last bar, a bad price, or no bars at all', () => {
    const series = [bar(H, 11)]
    expect(foldTick(series, { t: H - 1, price: 12 }, H)).toBe(false)
    expect(foldTick(series, { t: H + 1, price: 0 }, H)).toBe(false)
    expect(foldTick(series, { t: H + 1, price: Number.NaN }, H)).toBe(false)
    expect(foldTick(series, { t: Number.NaN, price: 12 }, H)).toBe(false)
    expect(series).toEqual([bar(H, 11)])
    expect(foldTick([], { t: 1, price: 1 }, H)).toBe(false)
  })

  it("keeps the bars' alignment: Hong Kong's half-hour bars, the reopening after lunch, UTC 4h buckets and daily bars", () => {
    const at = (iso: string) => Date.parse(iso) / 1000
    expect(tickBarTime(at('2026-10-14T01:30:00Z'), at('2026-10-14T02:41:00Z'), H)).toBe(at('2026-10-14T02:30:00Z'))
    expect(tickBarTime(at('2026-10-14T03:30:00Z'), at('2026-10-14T05:10:00Z'), H)).toBe(at('2026-10-14T05:00:00Z')) // 13:00 HKT
    expect(tickBarTime(at('2026-10-14T00:00:00Z'), at('2026-10-14T05:10:00Z'), 4 * H)).toBe(at('2026-10-14T04:00:00Z'))
    expect(tickBarTime(at('2026-10-09T00:00:00Z'), at('2026-10-12T01:00:00Z'), 24 * H)).toBe(at('2026-10-12T00:00:00Z')) // Friday → Monday
    expect(BAR_SECONDS).toEqual({ '15m': 900, '1h': 3600, '4h': 14_400, '1d': 86_400 })
  })

  it('accepts the existing interval names without changing their session alignment', () => {
    const at = (iso: string) => Date.parse(iso) / 1000
    const start = at('2026-10-14T01:30:00Z')
    const series = [bar(start, 10)]
    expect(foldTick(series, { t: at('2026-10-14T02:41:00Z'), price: 12 }, '1h')).toBe(true)
    expect(series.at(-1)).toEqual({ t: at('2026-10-14T02:30:00Z'), o: 12, h: 12, l: 12, c: 12, v: 0 })
  })

  it.each([
    ['January into February', '2024-01-31T00:00:00Z', '2024-02-01T00:00:00Z', '2024-02-01T00:00:00Z'],
    ['leap February into March', '2024-02-29T00:00:00Z', '2024-03-01T00:00:00Z', '2024-03-01T00:00:00Z'],
    ['December into a new year', '2024-12-01T00:00:00Z', '2025-01-01T00:00:00Z', '2025-01-01T00:00:00Z'],
    ['a gap with no synthetic intervening candles', '2024-12-15T00:00:00Z', '2025-03-18T12:30:00Z', '2025-03-01T00:00:00Z'],
  ])('opens the actual calendar month: %s', (_, last, tick, expected) => {
    const at = (iso: string) => Date.parse(iso) / 1000
    const previous = bar(at(last), 10)
    const series = [previous]
    expect(foldTick(series, { t: at(tick), price: 12 }, '1M')).toBe(true)
    expect(series).toEqual([previous, { t: at(expected), o: 12, h: 12, l: 12, c: 12, v: 0 }])
  })

  it('keeps leap day and an initially partial monthly candle in the same calendar month', () => {
    const at = (iso: string) => Date.parse(iso) / 1000
    const start = at('2024-02-15T00:00:00Z')
    const series = [bar(start, 10)]
    expect(foldTick(series, { t: at('2024-02-29T23:59:59Z'), price: 12 }, '1M')).toBe(true)
    expect(series).toEqual([{ t: start, o: 10, h: 12, l: 9, c: 12, v: 5 }])
    expect(foldTick(series, { t: at('2024-02-14T23:59:59Z'), price: 1 }, '1M')).toBe(false)
    expect(series).toEqual([{ t: start, o: 10, h: 12, l: 9, c: 12, v: 5 }])
  })

  it('keeps Sunday in its Monday week and rolls over on Monday across a year boundary', () => {
    const at = (iso: string) => Date.parse(iso) / 1000
    const start = at('2024-12-25T00:00:00Z') // a listing's partial first week
    const series = [bar(start, 10)]
    expect(foldTick(series, { t: at('2024-12-29T23:59:59Z'), price: 12 }, '1w')).toBe(true)
    expect(series).toEqual([{ t: start, o: 10, h: 12, l: 9, c: 12, v: 5 }])
    expect(foldTick(series, { t: at('2024-12-30T00:00:00Z'), price: 13 }, '1w')).toBe(true)
    expect(series.at(-1)).toEqual({ t: at('2024-12-30T00:00:00Z'), o: 13, h: 13, l: 13, c: 13, v: 0 })
    expect(foldTick(series, { t: at('2025-01-05T23:59:59Z'), price: 14 }, '1w')).toBe(true)
    expect(series.at(-1)).toEqual({ t: at('2024-12-30T00:00:00Z'), o: 13, h: 14, l: 13, c: 14, v: 0 })
    expect(foldTick(series, { t: at('2025-01-21T12:00:00Z'), price: 15 }, '1w')).toBe(true)
    expect(series.at(-1)).toEqual({ t: at('2025-01-20T00:00:00Z'), o: 15, h: 15, l: 15, c: 15, v: 0 })
    expect(series).toHaveLength(3)
  })
})

describe('mergeCandle', () => {
  // Live candle merge: replace same bar, append newer, ignore older
  const bar = (t: number, c: number): Candle => ({ t, o: c, h: c, l: c, c, v: 1 })

  it('replaces the open bar, appends newer and ignores older', () => {
    const series = [bar(60, 1), bar(120, 2)]
    expect(mergeCandle(series, bar(120, 3))).toBe(true)
    expect(series.map((c) => c.c)).toEqual([1, 3])
    mergeCandle(series, bar(180, 4))
    expect(series.map((c) => c.t)).toEqual([60, 120, 180])
    expect(mergeCandle(series, bar(60, 9))).toBe(false)
    expect(series).toHaveLength(3)
    const empty: Candle[] = []
    expect(mergeCandle(empty, bar(1, 1))).toBe(true)
    expect(empty).toHaveLength(1)
  })

  it('computes simple moving averages', () => {
    expect(sma([1, 2, 3, 4, 5].map((c, i) => bar(i, c)), 3)).toEqual([
      { t: 2, v: 2 },
      { t: 3, v: 3 },
      { t: 4, v: 4 },
    ])
    expect(sma([bar(0, 1)], 3)).toEqual([])
  })
})

describe('prices', () => {
  it('picks chart precision by magnitude and rounds levels to it', () => {
    expect([84213, 150, 25, 2.5, 0.12].map(pricePrecision)).toEqual([2, 2, 3, 4, 5])
    expect([roundPrice(82850.456), roundPrice(0.1234567)]).toEqual([82850.46, 0.12346])
  })

  it('MAX_FILL leaves room for fee + slippage', () => {
    // A buy of N costs N·(1 + 2·fee + 2 bps slippage) of buying power (the equity lost to fee and slippage counts
    // twice under the 2× rule), so a full-size order or a multi-leg invest must stay under 100%.
    expect(MAX_FILL * (1 + 2 * FEE_RATE + 0.0002)).toBeLessThan(1)
  })

  it('projects the position after an order', () => {
    expect(resultingQty(1, 'buy', 100, 50)).toBe(3)
    expect(resultingQty(1, 'sell', 100, 50)).toBe(-1)
    expect(resultingQty(1, 'buy', 100, 0)).toBe(1)
  })
})

describe('checkTicket', () => {
  const base: TicketInput = { side: 'buy', amount: 1000, price: 100, posQty: 0, buyingPower: 5000, stop: null, take: null }
  const t = (o: Partial<TicketInput>) => checkTicket({ ...base, ...o })

  it.each<[string, Partial<TicketInput>, string[]]>([
    ['plain buy ok', {}, []],
    ['below minimum', { amount: 5 }, ['amount']],
    ['empty amount', { amount: NaN }, ['amount']],
    ['full buying power leaves no room for the fee', { amount: 5000 }, ['amount']],
    ['reducing a long needs no buying power', { side: 'sell', amount: 6000, posQty: 100, buyingPower: 0 }, []],
    ['long stop below, target above', { stop: 95, take: 110 }, []],
    ['long levels on the wrong side', { stop: 105, take: 90 }, ['stop', 'take']],
    ['short stop above, target below', { side: 'sell', stop: 110, take: 90 }, []],
    ['short stop below the price', { side: 'sell', stop: 90 }, ['stop']],
    ['partial sell keeps a long: long rules apply', { side: 'sell', amount: 1000, posQty: 20, stop: 95 }, []],
    ['closing order cannot carry a stop', { side: 'sell', amount: 1000, posQty: 10, stop: 95 }, ['stop']],
    ['zero stop', { stop: 0 }, ['stop']],
    ['no price yet: only the amount is checked', { price: 0, amount: 5, stop: 999 }, ['amount']],
  ])('%s', (_, patch, keys) => {
    expect(Object.keys(t(patch))).toEqual(keys)
  })

  it('explains each problem', () => {
    expect(t({ amount: 5 }).amount).toBe('The minimum order is $10.')
    expect(t({ amount: NaN }).amount).toBe('Enter an amount in USD.')
    expect(t({ amount: 5000 }).amount).toBe('Above your buying power. The most you can add is about $4,975.00.')
    expect(t({ amount: 5000, leverage: 5 }).amount, 'the field is margin').toBe('Above your buying power. The most you can add is about $995.00 of margin.')
    expect(t({ stop: 105 }).stop).toBe('For a long, set this below the price.')
    expect(t({ side: 'sell', take: 110 }).take).toBe('For a short, set this below the price.')
    expect(t({ side: 'sell', stop: 90 }).stop).toBe('For a short, set this above the price.')
    expect(t({ stop: -1 }).stop).toBe('Enter a price above zero.')
    expect(t({ side: 'sell', amount: 1000, posQty: 10, stop: 95 }).stop).toBe('This order closes the position, so there is nothing to protect.')
  })
})

describe('news helpers', () => {
  // Only http(s) links render; missing arrays from a backend that has not caught up count as empty.
  it('accepts http(s) URLs only', () => {
    expect(['https://a.io/x', 'HTTP://b.io', 'javascript:alert(1)', 'data:text/html,x', '//evil.io', 'https:// x', '', null].map(isHttpUrl)).toEqual([
      true,
      true,
      false,
      false,
      false,
      false,
      false,
      false,
    ])
  })

  it('keeps safe, titled headlines up to a max', () => {
    const item = (url: string, title = 't'): NewsItem => ({ id: url, title, url, source: 's', description: '', publishedAt: null, symbols: [], topic: 'asset', regions: [], hasContent: false })
    expect(
      linkable([item('https://a.io/1'), item('javascript:x'), item('https://a.io/2', ''), item('https://a.io/3'), item('https://a.io/4'), item('https://a.io/5')], 3).map((n) => n.url),
    ).toEqual(['https://a.io/1', 'https://a.io/3', 'https://a.io/4'])
    expect(linkable(undefined)).toEqual([])
  })

  it("reads the brief's sentiment for one symbol", () => {
    const brief = { sentiment: [{ symbol: 'BTC', score: 0.4, label: 'bullish', reason: 'ETF inflows' }] } as MarketBrief
    expect(sentimentFor(brief, 'BTC')?.label).toBe('bullish')
    expect([sentimentFor(brief, 'ETH'), sentimentFor(null, 'BTC'), sentimentFor({} as MarketBrief, 'BTC')]).toEqual([null, null, null])
  })
})

describe('marketSearchText', () => {
  const text = (symbol: string) => marketSearchText(universeAssets.find((a) => a.symbol === symbol)!)

  it('is the symbol, names and group for crypto', () => {
    expect(text('BTC')).toBe('btc bitcoin bitcoin crypto')
  })

  it('adds the venue, the underlying and the places its codes name for stock perps', () => {
    expect(text('SMSN')).toBe('smsn samsung electronics samsung electronics asia krx krx 005930 common share, krw→usd south korea')
    expect(text('TSM')).toContain('taiwan') // TWSE in the underlying, though the session venue is NYSE
    expect(text('BABA')).toContain('hong kong')
    expect(text('SOFTBANK')).toContain('japan tokyo')
    expect(text('EWJ')).toMatch(/japan/)
    expect(text('EWJ')).not.toMatch(/korea/)
    expect(text('AAPL')).toBe('aapl apple apple us nyse nasdaq: aapl common share')
  })

  it('names the places of the Chinese and Southeast Asian venues and currencies', () => {
    expect(text('CXMT')).toContain('china shanghai')
    expect(text('DBS')).toContain('singapore')
    expect(text('BBCA')).toContain('indonesia')
    expect(text('PTT')).toContain('thailand')
    expect(text('MAYBANK')).toContain('malaysia')
    expect(text('VCB')).toContain('vietnam')
    expect(text('QUANTA')).toContain('taiwan')
    expect(text('JARDINE')).toContain('singapore') // an SGX line quoted in USD
    expect(text('DBS')).not.toMatch(/korea|japan|taiwan/)
  })

  // The search over the whole universe, as the market list filters it (in the API's order).
  const find = (q: string) => universeAssets.filter((a) => marketSearchText(a).includes(q)).map((a) => a.symbol)

  it("names the issuer's country where the venue, currency and underlying don't", () => {
    expect(find('india')).toEqual(['INFY', 'HDB', 'IBN']) // NSE in the ADR's underlying
    expect(find('philippines')).toEqual(['PHI']) // PSE
    expect(find('china')).toEqual(['BABA', 'MINIMAX', 'ZHIPU', 'CXMT', 'UNITREE', 'GIGADEV', 'KSTR', 'PDD', 'JD', 'BIDU', 'NIO', 'LI', 'XPEV', 'NTES',
      'TCOM', 'BILI', 'FUTU', 'TME', 'BEKE', 'ZTO', 'YUMC', 'VIPS', 'TENCENT', 'XIAOMI', 'MEITUAN', 'BYD', 'CHINASTEEL']) // China Steel by its name
    expect(find('singapore')).toEqual(expect.arrayContaining(['SE', 'GRAB', 'KARO', 'KLIC']))
    expect(find('taiwan')).toEqual(expect.arrayContaining(['HIMX', 'SIMO', 'UMC', 'ASX', 'CHT']))
    expect(find('thailand')).toEqual(['PTT', 'ADVANC', 'CPALL', 'AOT', 'THAIBEV']) // Thai Beverage's SGX line
    expect(find('indonesia')).toEqual(['TLK', 'BBCA', 'BBRI', 'BMRI', 'TLKM'])
    expect(find('malaysia')).toEqual(['MAYBANK', 'PBBANK', 'TENAGA'])
    expect(find('vietnam')).toEqual(['VCB', 'VHM', 'FPT'])
    expect(find('south korea')).toEqual(find('korea'))
    expect(find('korea')).toEqual(expect.arrayContaining(['CPNG', 'KB', 'SHG', 'PKX', 'KEP', 'SKM', 'KT', 'LPL']))
    expect(find('hong kong')).toEqual(expect.arrayContaining(['FUTU', 'JARDINE', 'HKLAND', 'YUMC']))
    expect(text('BTC')).not.toMatch(/china|india|korea/)
  })

  it("adds a quote feed's Yahoo ticker, and finds a listed stock by ticker, name, country, venue and index", () => {
    expect(text('DBS')).toContain('d05.si')
    expect(text('AAPL')).not.toContain('xyz:aapl') // a perp's pair is not a ticker anyone types
    const hitachi = marketSearchText(listedAsset('6501.T'))
    for (const q of ['6501', '6501.t', 'hitachi', 'japan', 'tse', 'nikkei 225']) expect(hitachi, q).toContain(q)
    const bni = marketSearchText(listedAsset('BBNI.JK'))
    for (const q of ['bbni', 'bank negara', 'indonesia', 'idx', 'lq45']) expect(bni, q).toContain(q)
  })

  it('falls back to the configured group without a region and tolerates missing metadata', () => {
    const tsm = universeAssets.find((a) => a.symbol === 'TSM')!
    expect(marketSearchText({ ...tsm, region: undefined, sessionVenue: undefined, underlying: undefined, localCurrency: undefined })).toBe('tsm tsmc (taiwan semiconductor) tsmc (taiwan semiconductor) asia')
  })
})

describe('planAmount', () => {
  it('rounds a plan to $50 within the room, or to whole dollars under $50 of room', () => {
    expect(planAmount(4999, 100_000)).toBe(5000)
    expect(planAmount(2500.4, 100_000)).toBe(2500)
    expect(planAmount(5000, 4990)).toBe(4950) // never above the room
    expect(planAmount(30.7, 40.9)).toBe(30)
    expect(planAmount(45.7, 40.9)).toBe(40)
    expect(planAmount(100, 0)).toBe(0)
  })
})

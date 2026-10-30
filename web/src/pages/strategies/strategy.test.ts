import { describe, expect, it } from 'vitest'
import { fmtDate } from '../../lib/format'
import type { Asset, Indicator, StrategySpec } from '../../lib/types'
import * as fx from '../../test/fixtures'
import { QUOTE_FEED_SYMBOLS } from './quoteFeeds'
import {
  ASIA_PERPS,
  assetName,
  fxNote,
  isStockPerp,
  MARKET_GROUPS,
  marketGroup,
  VENUE_GROUPS,
  venueGroup,
  sessionLabel,
  symbolGroup,
  dailyCloses,
  describeIndicator,
  describeRules,
  describeRuleSet,
  fmtDD,
  fmtIndex,
  fmtNum,
  fmtWindow,
  isoAt,
  parseTune,
  rebaseSince,
  tuneChanged,
  tuneText,
  returnHeadline,
  SYMBOLS,
  utcTime,
} from './strategy'

const ema20 = { kind: 'ema', period: 20 } as const
const ema50 = { kind: 'ema', period: 50 } as const
const rsi = { kind: 'rsi', period: 14 } as const

describe('describeRuleSet', () => {
  // all = AND, any = OR, both = all AND (any)
  it('joins all with and', () => {
    expect(
      describeRuleSet({ all: [{ left: ema20, op: 'crosses_above', right: ema50 }, { left: rsi, op: '>', right: { kind: 'const', value: 50 } }] }),
    ).toBe('EMA(20) crosses above EMA(50) and RSI(14) > 50')
  })

  it('nests any inside all', () => {
    expect(
      describeRuleSet({
        all: [{ left: { kind: 'price' }, op: '>', right: { kind: 'sma', period: 200 } }],
        any: [
          { left: rsi, op: '<', right: { kind: 'const', value: 30 } },
          { left: { kind: 'price' }, op: 'crosses_below', right: { kind: 'bb_lower', period: 20, stdDev: 2 } },
        ],
      }),
    ).toBe('price > SMA(200) and (RSI(14) < 30 or price crosses below the lower Bollinger band (20, 2σ))')
    expect(
      describeRuleSet({ all: [{ left: rsi, op: '>', right: { kind: 'const', value: 50 } }], any: [{ left: { kind: 'volume' }, op: '>', right: { kind: 'volume_sma', period: 20 } }] }),
    ).toBe('RSI(14) > 50 and volume > the 20-bar average volume')
  })

  it('reads any alone, and nothing for an empty or missing set', () => {
    expect(describeRuleSet({ any: [{ left: { kind: 'price' }, op: 'crosses_above', right: { kind: 'highest', period: 20 } }] })).toBe(
      'price crosses above the prior 20-bar high',
    )
    expect(describeRuleSet({ all: [], any: [] })).toBeNull()
    expect(describeRuleSet(undefined)).toBeNull()
  })
})

describe('describeIndicator', () => {
  it.each<[Indicator, string]>([
    [{ kind: 'lowest', period: 10 }, 'the prior 10-bar low'],
    [{ kind: 'bb_upper', period: 20, stdDev: 2.5 }, 'the upper Bollinger band (20, 2.5σ)'],
    [{ kind: 'atr', period: 14 }, 'ATR(14)'],
    [{ kind: 'roc', period: 9 }, 'ROC(9)'],
    [{ kind: 'const', value: 0.12345 }, '0.1235'],
    [{ kind: 'sma', period: NaN }, 'SMA(?)'],
    [{ kind: 'wobble' } as never, 'wobble'],
    [null as never, '?'],
  ])('%j → %s', (ind, text) => {
    expect(describeIndicator(ind)).toBe(text)
  })
})

describe('describeRules', () => {
  const spec: StrategySpec = {
    name: 'T',
    symbol: 'BTC',
    interval: '4h',
    bars: 1000,
    longEntry: { all: [{ left: ema20, op: 'crosses_above', right: ema50 }] },
    longExit: { all: [{ left: ema20, op: 'crosses_below', right: ema50 }] },
    shortEntry: {
      all: [{ left: { kind: 'macd', fast: 12, slow: 26, signal: 9 }, op: 'crosses_below', right: { kind: 'macd_signal', fast: 12, slow: 26, signal: 9 } }],
    },
    stopLossPct: 4,
    positionPct: 100,
    feePct: 0.05,
  }

  it('writes entries, exits, and the fallback sentence when an exit rule is missing', () => {
    expect(describeRules(spec)).toEqual([
      { label: 'Long entry', text: 'Enter long when EMA(20) crosses above EMA(50).' },
      { label: 'Long exit', text: 'Exit long when EMA(20) crosses below EMA(50).' },
      { label: 'Short entry', text: 'Enter short when MACD(12,26,9) crosses below the MACD signal line (12,26,9).' },
      { label: 'Short exit', text: 'No exit rule: the short closes on the 4% stop, an opposite entry signal or the end of the test.' },
    ])
  })

  it('covers a short exit rule and a long-only spec with a target', () => {
    expect(describeRules({ ...spec, shortExit: { all: [{ left: rsi, op: '<', right: { kind: 'const', value: 30 } }] } })[3]).toEqual({
      label: 'Short exit',
      text: 'Cover the short when RSI(14) < 30.',
    })
    expect(describeRules({ ...spec, longExit: undefined, shortEntry: undefined, stopLossPct: undefined, takeProfitPct: 8 })).toEqual([
      { label: 'Long entry', text: 'Enter long when EMA(20) crosses above EMA(50).' },
      { label: 'Long exit', text: 'No exit rule: the long closes on the 8% target or the end of the test.' },
    ])
  })
})

describe('tune card', () => {
  const spec = { stopLossPct: 4, positionPct: 100 } as StrategySpec

  it('parses fields with the API bounds; blank stop or target means none', () => {
    expect(parseTune({ stop: '3', take: '', size: '50' })).toEqual({ values: { stopLossPct: 3, positionPct: 50 }, errors: {} })
    expect(parseTune({ stop: '0', take: '60', size: '' }).errors).toEqual({
      stop: 'Use 0.1–50%, or leave blank for none.',
      take: 'Use 0.1–50%, or leave blank for none.',
      size: 'Use 1–100% of equity.',
    })
    expect(parseTune({ stop: 'abc', take: '5', size: '100' }).values).toBeNull()
  })

  it('detects a change (0 and absent both mean none)', () => {
    expect(tuneChanged(spec, { stopLossPct: 4, positionPct: 100 }), 'absent target = none').toBe(false)
    expect(tuneChanged({ ...spec, takeProfitPct: 0 }, { stopLossPct: 4, positionPct: 100 })).toBe(false)
    expect(tuneChanged(spec, { stopLossPct: 4, takeProfitPct: 8, positionPct: 100 })).toBe(true)
    expect(tuneChanged(spec, { stopLossPct: 4, positionPct: 50 })).toBe(true)
  })

  it('shows none as an empty field', () => {
    expect([tuneText(undefined), tuneText(0), tuneText(4)]).toEqual(['', '', '4'])
  })
})

describe('series helpers', () => {
  const day = 86400
  const pts = [0, 1, 2, 3].map((i) => ({ t: 1_000_000 + i * day, v: [100, 110, 121, 133.1][i] }))

  it('rebases the last N days to 100', () => {
    expect(rebaseSince(pts, 2).map((p) => +p.v.toFixed(2))).toEqual([100, 110, 121])
    expect(rebaseSince(pts, Infinity)).toHaveLength(4)
    expect(rebaseSince([], 30)).toEqual([])
    expect(rebaseSince([{ t: 1, v: 0 }, { t: 2, v: 5 }], Infinity), 'a wiped-out curve is left alone').toEqual([
      { t: 1, v: 0 },
      { t: 2, v: 5 },
    ])
  })

  it('keeps the last value per UTC day, stamped at midnight', () => {
    expect(dailyCloses([{ t: 3600, v: 100 }, { t: 7200, v: 101 }, { t: 86400 + 60, v: 99 }])).toEqual([
      { t: 0, v: 101 },
      { t: 86400, v: 99 },
    ])
    expect(dailyCloses([])).toEqual([])
  })
})

describe('formatting', () => {
  it('labels index values, ratios and drawdowns', () => {
    expect([fmtIndex(112.34), fmtIndex(95), fmtIndex(100)]).toEqual(['+12.3%', '−5.0%', '0.0%'])
    expect([fmtDD(18.42), fmtDD(0), fmtDD(undefined)]).toEqual(['−18.4%', '0.0%', '—'])
    expect([fmtNum(1.416), fmtNum(null), fmtNum(Infinity)]).toEqual(['1.42', '—', '—'])
    expect([assetName('SOL'), assetName('AAVE'), assetName('ZZZ')]).toEqual(['Solana', 'Aave', 'ZZZ'])
    expect(SYMBOLS).toHaveLength(107 + 138) // crypto and perps, then the quote feeds
    expect([SYMBOLS[11][0], SYMBOLS[39][0]]).toEqual(['PAXG', 'STX'])
    expect([SYMBOLS[40][0], SYMBOLS[68][0], SYMBOLS[69][0], SYMBOLS[77][0]]).toEqual(['ZEC', 'PENDLE', 'NVDA', 'XYZ100'])
    expect([assetName('SP500'), assetName('XYZ100')]).toEqual(['S&P 500 perp', 'XYZ100 perp'])
    expect([SYMBOLS[78][0], SYMBOLS[87][0], assetName('TSM')]).toEqual(['TSM', 'EWY', 'TSMC (Taiwan Semiconductor)'])
    // The second batch appends twelve US stocks, then seven Asia perps; nothing before them moves.
    expect([SYMBOLS[88][0], SYMBOLS[99][0], SYMBOLS[100][0], SYMBOLS[106][0]]).toEqual(['PLTR', 'CRWV', 'SKHY', 'KSTR'])
    expect([assetName('MSTR'), assetName('KIOXIA'), assetName('CXMT')]).toEqual(['Strategy Inc (MicroStrategy)', 'Kioxia Holdings', 'CXMT (ChangXin Memory)'])
    expect([SYMBOLS[107][0], SYMBOLS[174][0], SYMBOLS[244][0], assetName('DBS')]).toEqual(['HSDT', 'KEPPEL', 'LPL', 'DBS Group'])
  })
})

describe('market list groups by venue', () => {
  it('lists crypto, US, the Asia perps and ADRs, then each local venue, Singapore, Taiwan and Korea first, with a delayed tag', () => {
    expect(VENUE_GROUPS.map((g) => g.label)).toEqual(['Crypto', 'US stocks & indices', 'Asia perps & US-listed ADRs', 'Singapore · SGX', 'Taiwan · TWSE',
      'South Korea · KRX', 'Japan · TSE', 'Hong Kong · HKEX', 'Indonesia · IDX', 'Thailand · SET', 'Malaysia · Bursa', 'Vietnam · HOSE'])
    expect(VENUE_GROUPS.filter((g) => g.delayed).map((g) => g.key)).toEqual(['SGX', 'TWSE', 'KRX', 'TSE', 'HKEX', 'IDX', 'SET', 'BURSA', 'HOSE'])
  })

  it('puts a local quote feed in its venue group, and perps and US-hours feeds with the ADRs', () => {
    const group = (symbol: string) => venueGroup(fx.universeAssets.find((a) => a.symbol === symbol)!)
    expect(['BTC', 'NVDA', 'HSDT', 'TSM', 'SMSN', 'KIOXIA', 'SONY', 'KARO'].map(group)).toEqual(['crypto', 'us', 'us', 'asia', 'asia', 'asia', 'asia', 'asia'])
    expect(['DBS', 'MEDIATEK', 'NAVER', 'TOKYOELEC', 'TENCENT', 'BBCA', 'PTT', 'MAYBANK', 'VCB', 'JARDINE'].map(group)).toEqual(['SGX', 'TWSE', 'KRX', 'TSE', 'HKEX', 'IDX', 'SET', 'BURSA', 'HOSE', 'SGX'])
    expect(['6501.T', '000810.KS', 'BBNI.JK'].map((s) => venueGroup(fx.listedAsset(s)))).toEqual(['TSE', 'KRX', 'IDX'])
    expect(venueGroup(fx.listedInstruments[0])).toBe('TSE') // a catalogue row groups the same
    // An API without sessionVenue: the configured listing's venue; an unknown one stays with the ADRs.
    expect(venueGroup({ symbol: 'DBS', class: 'stock', region: 'asia', venue: 'yahoo' })).toBe('SGX')
    expect(venueGroup({ symbol: 'NEWCO.PS', class: 'stock', region: 'asia', venue: 'yahoo', sessionVenue: null })).toBe('asia')
  })
})

describe('markets by region', () => {
  const a = (symbol: string, cls: Asset['class'], extra: Partial<Asset> = {}) => ({ symbol, class: cls, ...extra })
  it('groups crypto, US and Asia by region, falling back to class and the configured table', () => {
    expect(MARKET_GROUPS.map((g) => g.label)).toEqual(['Crypto', 'US stocks & indices', 'Asia stocks & ETFs'])
    expect([marketGroup(a('BTC', 'crypto', { region: null })), marketGroup(a('NVDA', 'stock', { region: 'us' })), marketGroup(a('TSM', 'stock', { region: 'asia' }))]).toEqual(['crypto', 'us', 'asia'])
    // An API without `region`: crypto by class; stock/index perps by the configured table, US otherwise.
    expect([marketGroup(a('ETH', 'crypto')), marketGroup(a('SP500', 'index')), marketGroup(a('EWY', 'stock')), marketGroup(a('NEWCO', 'stock'))]).toEqual(['crypto', 'us', 'asia', 'us'])
    // Quote feeds group by their listing: HSDT under US, every ADR and local listing under Asia.
    expect(SYMBOLS.filter(([s]) => symbolGroup(s) === 'asia').map(([s]) => s)).toEqual([...ASIA_PERPS, ...QUOTE_FEED_SYMBOLS.slice(1).map(([s]) => s)])
    expect([symbolGroup('HSDT'), marketGroup(a('HSDT', 'stock')), marketGroup(a('DBS', 'stock')), marketGroup(a('KARO', 'stock'))]).toEqual(['us', 'us', 'asia', 'asia'])
    expect([symbolGroup('BTC'), symbolGroup('XYZ100'), isStockPerp('SMSN'), isStockPerp('AAPL'), isStockPerp('SOL')]).toEqual(['crypto', 'us', true, true, false])
    // The second batch: US names under US stocks, Asia names under Asia, also for an API without `region`.
    expect(SYMBOLS.slice(88, 100).map(([s]) => symbolGroup(s))).toEqual(Array(12).fill('us'))
    expect(SYMBOLS.slice(100, 107).map(([s]) => symbolGroup(s))).toEqual(Array(7).fill('asia'))
    expect([marketGroup(a('KIOXIA', 'stock')), marketGroup(a('PLTR', 'stock')), marketGroup(a('CXMT', 'stock', { region: 'asia' }))]).toEqual(['asia', 'us', 'asia'])
    expect(SYMBOLS.filter(([s]) => isStockPerp(s))).toHaveLength(38)
  })

  it('labels the regular session by its venue, keeping the US wording for an API without one', () => {
    expect(sessionLabel('SMSN', { session: 'open', sessionVenue: 'KRX' })).toEqual({ venue: 'KRX', text: 'KRX session open' })
    expect(sessionLabel('TSM', { session: 'closed', sessionVenue: 'NYSE' }).text).toBe('NYSE session closed')
    expect(sessionLabel('ZHIPU', { session: null, sessionVenue: 'HKEX' }).text).toBe('HKEX session unknown')
    expect(sessionLabel('AAPL', { session: 'closed' }).text).toBe('US session closed') // older API
    expect(sessionLabel('AAPL', undefined).text).toBe('US session unknown')
    expect(sessionLabel('SOFTBANK', { session: null, sessionVenue: null })).toEqual({ venue: null, text: 'Regular session unknown' })
    expect(sessionLabel('CXMT', { session: 'closed', sessionVenue: 'SSE' })).toEqual({ venue: 'SSE', text: 'SSE session closed' })
    expect(sessionLabel('GIGADEV', { session: null, sessionVenue: 'SSE' }).text).toBe('SSE session unknown') // past the verified 2026 calendar
    expect(sessionLabel('KIOXIA', undefined).text).toBe('Regular session unknown') // an Asia perp never falls back to the US wording
  })

  it('adds the FX note only for perps that list in a non-USD currency', () => {
    expect(fxNote({ localCurrency: 'KRW' })).toBe("Price is the USD perp mark: trade.xyz's oracle converts the KRW listing to USD. This app does no FX conversion.")
    expect(fxNote({ localCurrency: 'JPY' })).toContain('JPY listing')
    expect(fxNote({ localCurrency: 'CNY' })).toBe("Price is the USD perp mark: trade.xyz's oracle converts the CNY listing to USD at the offshore USD/CNH rate. This app does no FX conversion.")
    expect([fxNote({ localCurrency: 'USD' }), fxNote({ localCurrency: null }), fxNote({}), fxNote(undefined)]).toEqual(['', '', '', ''])
  })

  it('a daily bar shows its UTC calendar date in every time zone', () => {
    expect(isoAt(1790899200), 'intraday bar keeps the instant').toBe('2026-10-02T00:00:00.000Z')
    expect(isoAt(1790899200, true)).toBe('2026-10-02')
    expect(fmtDate(isoAt(1790899200, true)), 'not shifted west of UTC').toBe('Oct 2, 2026')
    expect(isoAt(NaN, true)).toBeNull()
  })
})

describe('backtest windows and headlines', () => {
  const m = { ...fx.strategy().metrics, startT: 1_780_000_000 }
  it('formats the window in days under a year, else years', () => {
    expect(fmtWindow({ startT: m.startT, endT: m.startT + 42 * 86400 })).toBe('42 d')
    expect(fmtWindow({ startT: m.startT, endT: m.startT + Math.round(2.7 * 365 * 86400) })).toBe('2.7 y')
    expect(fmtWindow(undefined)).toBe('—')
  })

  it('leads with the total return when the window is too short to annualize', () => {
    expect(returnHeadline({ ...m, totalReturnPct: 10.94, annualizedReturnPct: null, endT: m.startT + 42 * 86400 })).toBe('Total return +10.9% over 42 days')
    expect(returnHeadline({ ...m, totalReturnPct: 18, annualizedReturnPct: 28.4, endT: m.startT + 242 * 86400 })).toBe(
      'Annualized +28.4% · total +18.0% over 242 days',
    )
    expect(returnHeadline({ ...m, totalReturnPct: 18, annualizedReturnPct: 28.4, trades: 9, endT: m.startT + 242 * 86400 })).toBe('Total return +18.0% over 242 days')
    expect(returnHeadline(undefined)).toBe('')
  })

  it('prints UTC clock times', () => {
    expect(utcTime('2026-10-02T12:05:59Z')).toBe('12:05 UTC')
    expect(utcTime('2026-10-02T12:05:59Z', true)).toBe('Oct 2, 12:05 UTC')
    expect(utcTime(null)).toBe('—')
  })
})

import { describe, expect, it } from 'vitest'
import * as fx from '../../test/fixtures'
import { fmtLocal, nextSort, readQuery, statusLabel, viewPrompt } from './screener'

describe('screener helpers', () => {
  it('formats local prices in their own currency', () => {
    expect(fmtLocal(2930.5, 'JPY')).toBe('¥2,930.5')
    expect(fmtLocal(272000, 'KRW')).toBe('₩272,000')
    expect(fmtLocal(2585, 'TWD')).toBe('NT$2,585')
    expect(fmtLocal(78.56, 'SGD')).toBe('SGD\u00a078.56') // Intl puts a no-break space after an ISO code
    expect(fmtLocal(428.2, 'HKD')).toBe('HK$428.20')
    expect(fmtLocal(0.0832, 'MYR')).toBe('MYR\u00a00.0832')
    expect(fmtLocal(3.15, 'USD')).toBe('$3.15')
    expect(fmtLocal(12, 'NOT A CODE')).toBe('12.00 NOT A CODE')
    expect(fmtLocal(null, 'JPY')).toBe('—')
  })

  it('sorts a new column its natural way first, then flips it', () => {
    expect(nextSort('pctVsSma', 'rsi')).toBe('rsi')
    expect(nextSort('rsi', 'rsi')).toBe('-rsi')
    expect(nextSort('-rsi', 'rsi')).toBe('rsi')
    expect(nextSort('pctVsSma', 'updated')).toBe('-updated')
    expect(nextSort('pctVsSma', 'pctVsSma')).toBe('-pctVsSma')
  })

  it('reads the query, keeping only known values', () => {
    expect(readQuery(new URLSearchParams('filter=deep&market=jp&q=%20bank&sort=-rsi'))).toEqual({ filter: 'deep', market: 'JP', q: ' bank', sort: '-rsi' })
    expect(readQuery(new URLSearchParams('filter=x&market=US&sort=--rsi'))).toEqual({ filter: 'all', market: '', q: '', sort: 'pctVsSma' })
    expect(readQuery(new URLSearchParams(`q=${'a'.repeat(80)}`)).q).toHaveLength(64)
  })

  it('labels a row by its status', () => {
    const [, , , , , kioxia, tencent, nissui, sm] = fx.screenerRows
    expect(statusLabel(kioxia)).toBe('n/a, insufficient history (95 wks)')
    expect(statusLabel(tencent)).toBe('Last refresh failed')
    expect(statusLabel(nissui)).toBe('Not screened yet')
    expect(statusLabel(sm)).toBe('Unavailable (no data feed)')
  })

  it('builds the agent prompt from the measured facts only', () => {
    const [bbca, , , , , kioxia, , nissui] = fx.screenerRows
    expect(viewPrompt(bbca, 'Indonesia')).toBe(
      'Analyse PT Bank Central Asia Tbk (BBCA.JK, Indonesia, IDX LQ45). The Asia screener shows a price of IDR\u00a06,100 (delayed · last trade 06:58 UTC on Oct 14), ' +
        '-29.3% vs its 200-week moving average and recovering week over week, a 14-week RSI of 42.5, 44.3% below its all-time high from the week of Sep 23, 2024. ' +
        'Is it a long-term opportunity or a value trap? Cover the business, valuation, catalysts and risks. It is research only here: there is no paper market for it.',
    )
    expect(viewPrompt({ ...bbca, quote: null }, 'Indonesia')).toContain('The Asia screener shows -29.3% vs its 200-week moving average') // the run's price: no label
    expect(viewPrompt({ ...bbca, quote: { ...bbca.quote!, lastTradeAt: '' } }, 'Indonesia')).toContain('a price of IDR\u00a06,100 (delayed), ')
    expect(viewPrompt(fx.screenerRows[3], 'Taiwan')).toContain('a 14-week RSI of 72.9, a new all-time high on Oct 14, 2026.')
    expect(viewPrompt(kioxia, 'Japan')).toContain('listed on Yahoo for only 95 weeks, too short for a 200-week average, a 14-week RSI of 53.2')
    expect(viewPrompt(nissui, 'Japan')).toBe(
      'Analyse NISSUI CORP. (1332.T, Japan, Nikkei 225). Is it a long-term opportunity or a value trap? Cover the business, valuation, ' +
        'catalysts and risks. It is research only here: there is no paper market for it.',
    )
  })

  it('leaves a high over 100× the price (a reverse-split history) out of the prompt', () => {
    const [bbca] = fx.screenerRows
    const split = { ...bbca, ath: 6100 * 101, drawdownPct: -99.01, athAt: '2008-01-07' }
    expect(viewPrompt(split, 'Indonesia')).not.toMatch(/all-time high|99\.0%/)
    expect(viewPrompt({ ...split, ath: 6100 * 100, drawdownPct: -99 }, 'Indonesia')).toContain('99.0% below its all-time high') // exactly 100× is kept
  })
})

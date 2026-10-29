import { screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { charts } from '../../test/chart'
import { api } from '../../lib/api'
import type { AIPortfolio, BuildResult } from '../../lib/types'
import * as fx from '../../test/fixtures'
import { renderPage, stubSession } from '../../test/utils'
import { BuildActionCard } from '../agent/ActionCards'
import { PortfolioDetailPage } from './PortfolioDetailPage'
import { PortfoliosPage } from './PortfoliosPage'
import { sinceStats } from './portfolio'

const ts = [1_790_812_800, 1_790_899_200, 1_790_985_600, 1_791_072_000, 1_791_158_400]
const basket = (): AIPortfolio => fx.aiPortfolio({ name: 'Stock basket', holdings: [{ symbol: 'AAPL', weight: 1, rationale: 'Stock exposure.' }], backtest: { series: ts.map((t, i) => ({ t, v: 100 + i })), benchmark: ts.map((t, i) => ({ t, v: 100 + i / 2 })), benchmarkSymbol: 'SP500', benchmarkLabel: 'S&P 500 perp', windowStart: ts[0], windowEnd: ts[4], bars: 5, returnPct: 4, benchmarkReturnPct: 2, maxDrawdownPct: 0, volatilityAnnPct: null } })
beforeEach(() => stubSession())
const open = (p: AIPortfolio) => { vi.spyOn(api, 'aiPortfolio').mockResolvedValue({ portfolio: p }); return renderPage(<PortfolioDetailPage />, { route: '/portfolios/p1', path: '/portfolios/:id' }) }

describe('saved comparison and actual window', () => {
  it('uses the saved SP500 comparison and measured short sample on the detail', async () => {
    open(basket()); await screen.findByRole('heading', { name: 'Stock basket' })
    expect(screen.getByRole('heading', { name: 'Portfolio vs S&P 500 perp' })).toBeInTheDocument()
    expect(screen.getByText(/5 daily bars/)).toBeInTheDocument()
    expect(screen.getByText('Volatility · ann.').nextSibling).toHaveTextContent('—')
    expect(screen.queryByText('BTC return')).toBeNull()
    expect(screen.queryByText(/Daily closes from Binance\./)).toBeNull()
  })

  it('uses measured metadata on portfolio list cards', async () => {
    vi.spyOn(api, 'aiPortfolios').mockResolvedValue({ portfolios: [basket()] })
    renderPage(<PortfoliosPage />); await screen.findByText('Stock basket')
    expect(screen.getByText('S&P 500 perp')).toBeInTheDocument()
    expect(screen.getByText(/5 daily bars/)).toBeInTheDocument()
    expect(screen.queryByText('180-day return')).toBeNull()
  })

  it('keeps a saved BTC comparison even if the current holdings are entirely stocks', async () => {
    const p = basket(); p.backtest = fx.aiPortfolio().backtest
    open(p); await screen.findByRole('heading', { name: 'Stock basket' })
    expect(screen.getByRole('heading', { name: 'Portfolio vs BTC' })).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'Portfolio vs S&P 500 perp' })).toBeNull()
  })

  it('defaults a legacy saved result to BTC and derives its measured bounds from retained points', async () => {
    const p = basket()
    p.backtest = { ...p.backtest, benchmarkSymbol: undefined, benchmarkLabel: undefined, windowStart: undefined, windowEnd: undefined, bars: undefined } as unknown as AIPortfolio['backtest']
    open(p); await screen.findByRole('heading', { name: 'Stock basket' })
    expect(screen.getByRole('heading', { name: 'Portfolio vs BTC' })).toBeInTheDocument()
    expect(screen.getByText(/5 daily bars/)).toBeInTheDocument()
  })

  it('never relabels a BTC-only compatibility curve as a saved SP500 comparison', async () => {
    const p = basket()
    p.invested = fx.invested({ benchmarkSymbol: 'SP500', benchmarkLabel: 'S&P 500 perp', benchmarkReturnPct: null, benchmarkSeries: undefined, series: fx.sinceInvest.series, btcSeries: fx.sinceInvest.btcSeries, bars: 4, windowStart: fx.sinceInvest.windowStart, windowEnd: fx.sinceInvest.windowEnd }) as AIPortfolio['invested']
    open(p); await screen.findByRole('heading', { name: 'Stock basket' })
    expect(screen.getByText('S&P 500 perp return').nextSibling).toHaveTextContent('—')
    await waitFor(() => {
      const plotted = charts.findLast((chart) => document.contains(chart.el))
      expect(plotted?.series).toHaveLength(1)
      expect(plotted!.series[0].setData).toHaveBeenLastCalledWith(fx.sinceInvest.series.map(({ t, v }) => ({ time: t, value: v })))
    })
  })

  it('keeps the actual paper line and neutral availability copy when a null comparison has no cause metadata', async () => {
    const p = basket(); p.invested = fx.invested({ benchmarkSymbol: 'SP500', benchmarkLabel: 'S&P 500 perp', benchmarkReturnPct: null, benchmarkSeries: null, btcReturnPct: null, btcSeries: null, series: fx.sinceInvest.series, windowStart: 1_790_928_000, windowEnd: 1_790_938_800, bars: 4 })
    open(p); await screen.findByRole('heading', { name: 'Stock basket' })
    expect(screen.getByRole('heading', { name: 'Actual vs S&P 500 perp' })).toBeInTheDocument()
    expect(screen.getByText('S&P 500 perp return').nextSibling).toHaveTextContent('—')
    expect(screen.getByText('Comparison unavailable for S&P 500 perp across the first investment and plotted window.')).toBeInTheDocument()
    expect(screen.queryByText(/retained .* history does not cover/)).toBeNull()
    expect(screen.getByText(/4 plotted bars/)).toBeInTheDocument()
    expect(sinceStats(fx.sinceInvest.series, [])?.returnPct).toBeCloseTo(0.9)
  })

  it('carries selected identity and window through a fresh build card and a restored card', async () => {
    vi.spyOn(api, 'buildPortfolio').mockResolvedValue({ portfolio: basket(), credits: fx.credits })
    const data = { kind: 'portfolio' as const, prompt: 'stock basket', symbol: null, interval: null, cost: 2, result: null }
    const { user, unmount } = renderPage(<BuildActionCard data={data} />)
    await user.click(screen.getByRole('button', { name: /Build portfolio/ }))
    expect(await screen.findByText(/vs S&P 500 perp/)).toBeInTheDocument()
    expect(screen.getByText(/5 daily bars/)).toBeInTheDocument()
    expect(screen.queryByText(/6M backtest/)).toBeNull()
    unmount()
    const result = { id: 'p1', name: 'Stock basket', returnPct: 4, benchmarkReturnPct: 2, benchmarkSymbol: 'SP500', benchmarkLabel: 'S&P 500 perp', windowStart: ts[0], windowEnd: ts[4], bars: 5, sharpe: null, maxDrawdownPct: 0, holdings: [{ symbol: 'AAPL', weight: 1 }] } as BuildResult
    renderPage(<BuildActionCard data={{ ...data, result }} />)
    expect(screen.getByText(/vs S&P 500 perp/)).toBeInTheDocument()
    expect(screen.getByText(/5 daily bars/)).toBeInTheDocument()
  })
})

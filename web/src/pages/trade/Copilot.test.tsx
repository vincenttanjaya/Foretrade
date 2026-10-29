import { screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { api } from '../../lib/api'
import type { Plan } from '../../lib/types'
import * as fx from '../../test/fixtures'
import { renderPage, stubSession } from '../../test/utils'
import { Copilot } from './Copilot'

function ask(plan: Plan) {
  stubSession()
  const call = vi.spyOn(api, 'copilotPlan').mockResolvedValue({ plan, analysis: fx.analysis, outlook: fx.outlook, headlines: [], credits: fx.credits })
  return { call, ...renderPage(<Copilot symbol="BTC" onPlan={vi.fn()} onUse={vi.fn()} />) }
}

describe('Copilot', () => {
  it('heads the plan with its timeframe and the daily trend', async () => {
    const { user } = ask(fx.plan())
    await user.click(screen.getByRole('button', { name: /Let AI decide/ }))
    expect(await screen.findByText('Day · 1h chart · 3-day horizon')).toBeInTheDocument()
    expect(screen.getByText('1d uptrend')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Use this plan' })).toBeEnabled()
  })

  it('reads an entry away from the price as a limit order, and one at the price as market', async () => {
    // fx.analysis.price is 84,213.20: 1.5% below is 82,950.00.
    const { user, call } = ask(fx.plan({ entry: 82950.0, stopLoss: 81623, takeProfit: 85600 }))
    await user.click(screen.getByRole('button', { name: /Ask Long/ }))
    const entry = (await screen.findByText('Entry')).parentElement!
    expect(entry).toHaveTextContent('Entry$82,950.00limit · -1.50% from price')
    expect(screen.getByRole('button', { name: 'Place limit at $82,950.00' })).toBeEnabled()

    // 0.2% away is within the 0.25% band: take the market.
    call.mockResolvedValue({ plan: fx.plan({ entry: 84044.77, generatedAt: '2026-10-02T08:31:00Z' }), analysis: fx.analysis, outlook: fx.outlook, headlines: [], credits: fx.credits })
    await user.click(screen.getByRole('button', { name: /Ask Long/ }))
    expect(await screen.findByRole('button', { name: 'Use this plan' })).toBeEnabled()
    expect(screen.getByText('Entry').parentElement).toHaveTextContent('Entry$84,044.77market')

    // A short waiting for a bounce 1% above; a neutral plan stays a wait whatever its entry.
    call.mockResolvedValue({ plan: fx.plan({ direction: 'short', entry: 85055.33, generatedAt: '2026-10-02T08:32:00Z' }), analysis: fx.analysis, outlook: fx.outlook, headlines: [], credits: fx.credits })
    await user.click(screen.getByRole('button', { name: /Ask Short/ }))
    expect(await screen.findByRole('button', { name: 'Place limit at $85,055.33' })).toBeInTheDocument()
    expect(screen.getByText('Entry').parentElement).toHaveTextContent('limit · +1.00% from price')
    call.mockResolvedValue({ plan: fx.plan({ direction: 'neutral', entry: 82950, generatedAt: '2026-10-02T08:33:00Z' }), analysis: fx.analysis, outlook: fx.outlook, headlines: [], credits: fx.credits })
    await user.click(screen.getByRole('button', { name: /Let AI decide/ }))
    expect(await screen.findByRole('button', { name: 'No trade · the copilot says wait' })).toBeDisabled()
  })

  it('shows a weak Ask Long as a wait, with a hint when the daily trend agrees', async () => {
    const { user, call } = ask(fx.plan({ direction: 'neutral', confidence: 32 }))
    await user.click(screen.getByRole('button', { name: /Ask Long/ }))
    expect(call).toHaveBeenCalledWith({ symbol: 'BTC', style: 'day', bias: 'long' })
    expect(await screen.findByRole('button', { name: 'No long setup on 1h · wait' })).toBeDisabled()
    expect(screen.getByText(/Daily trend is up/)).toHaveTextContent('Daily trend is up: try Swing')

    await user.click(screen.getByRole('button', { name: 'try Swing' }))
    expect(screen.getByRole('button', { name: 'Swing' })).toHaveAttribute('aria-pressed', 'true')
  })

  it('gives no hint when the daily trend disagrees or is unknown, and keeps the plain wait for Auto', async () => {
    const { user, call } = ask(fx.plan({ direction: 'neutral', confidence: 30, higherTrend: { interval: '1d', trend: 'uptrend' } }))
    await user.click(screen.getByRole('button', { name: /Ask Short/ }))
    expect(await screen.findByRole('button', { name: 'No short setup on 1h · wait' })).toBeDisabled()
    expect(screen.getByText('Short · wait')).toBeInTheDocument() // the asked side, not a bare "Neutral"
    expect(screen.queryByText('Neutral')).toBeNull()
    expect(screen.queryByText(/Daily trend is/)).toBeNull()

    call.mockResolvedValue({ plan: fx.plan({ direction: 'neutral', higherTrend: null, generatedAt: '2026-10-02T08:31:00Z' }), analysis: fx.analysis, outlook: fx.outlook, headlines: [], credits: fx.credits })
    await user.click(screen.getByRole('button', { name: /Let AI decide/ }))
    const wait = await screen.findByRole('button', { name: 'No trade · the copilot says wait' })
    expect(wait).toBeDisabled()
    expect(wait).toHaveClass('whitespace-normal!', 'h-auto!') // may wrap on a phone
    expect(screen.queryByText(/^1d (?!ago)/)).toBeNull() // the daily-trend chip, not the "1d ago" timestamp
  })

  it('shows reward:risk to 2 decimals and outlook percentiles to 5 significant figures', async () => {
    const { user, call } = ask(fx.plan({ riskReward: 1.85 }))
    call.mockResolvedValue({ plan: fx.plan({ riskReward: 1.85 }), analysis: fx.analysis, outlook: { ...fx.outlook, p5: 100.0369, p50: 119.38, p95: 142.4632 }, headlines: [], credits: fx.credits })
    await user.click(screen.getByRole('button', { name: /Let AI decide/ }))
    expect(await screen.findByText('1 : 1.85')).toBeInTheDocument()
    expect(screen.getByText('$100.04')).toBeInTheDocument()
    expect(screen.getByText('$142.46')).toBeInTheDocument()
  })

  it('reads the forecast desk under the plan header: amber when it leans against the plan', async () => {
    const { user, call } = ask(fx.plan({ symbol: 'SOL', desk: fx.deskRead() }))
    await user.click(screen.getByRole('button', { name: /Ask Long/ }))
    expect(await screen.findByRole('link', { name: /Forecast desk/ })).toHaveAttribute('href', '/forecasts/f-sol')
    expect(screen.getByRole('link', { name: /Forecast desk/ })).toHaveTextContent('Forecast desk · Yes 28% (leans No) · SOL above $125 on Oct 9 →')
    expect(screen.getByText(/The desk leans against this plan/)).toHaveTextContent('The desk leans against this plan · No 72% for SOL above $125 on Oct 9')

    call.mockResolvedValue({ plan: fx.plan({ desk: fx.deskRead({ p: 0.64, lean: 'Yes 64%', stance: 'supports' }), generatedAt: '2026-10-02T08:31:00Z' }), analysis: fx.analysis, outlook: fx.outlook, headlines: [], credits: fx.credits })
    await user.click(screen.getByRole('button', { name: /Ask Long/ }))
    expect(await screen.findByText('The desk agrees · Yes 64%')).toBeInTheDocument()
    expect(screen.queryByText(/leans against/)).toBeNull()

    call.mockResolvedValue({ plan: fx.plan({ desk: fx.deskRead({ p: 0.51, lean: 'Yes 51%', stance: 'split' }), generatedAt: '2026-10-02T08:32:00Z' }), analysis: fx.analysis, outlook: fx.outlook, headlines: [], credits: fx.credits })
    await user.click(screen.getByRole('button', { name: /Ask Long/ }))
    expect(await screen.findByText('The desk is split · 51%')).toBeInTheDocument()

    // A neutral plan (stance null) shows the chip alone; no desk read, no chip.
    call.mockResolvedValue({ plan: fx.plan({ direction: 'neutral', desk: fx.deskRead({ stance: null }), generatedAt: '2026-10-02T08:33:00Z' }), analysis: fx.analysis, outlook: fx.outlook, headlines: [], credits: fx.credits })
    await user.click(screen.getByRole('button', { name: /Let AI decide/ }))
    expect(await screen.findByRole('button', { name: 'No trade · the copilot says wait' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /Forecast desk/ })).toBeInTheDocument()
    expect(screen.queryByText(/The desk (leans|agrees|is split)/)).toBeNull()

    call.mockResolvedValue({ plan: fx.plan({ generatedAt: '2026-10-02T08:34:00Z' }), analysis: fx.analysis, outlook: fx.outlook, headlines: [], credits: fx.credits })
    await user.click(screen.getByRole('button', { name: /Let AI decide/ }))
    expect(await screen.findByRole('button', { name: 'Use this plan' })).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: /Forecast desk/ })).toBeNull()
  })
})

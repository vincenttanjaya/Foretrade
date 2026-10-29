import { screen } from '@testing-library/react'
import { useLocation } from 'react-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError } from '../../lib/api'
import * as fx from '../../test/fixtures'
import { renderPage, stubSession } from '../../test/utils'
import { deskOf, OddsAlertButton, oddsPresets, pctLabel } from './OddsAlertButton'

function Entry() {
  const { state } = useLocation()
  return <p data-testid="entry">{JSON.stringify(state)}</p>
}

const show = (desk = 0.62) =>
  renderPage(<OddsAlertButton forecastId="f1" m={fx.marketOdds} desk={desk} />, {
    route: '/forecasts/f1',
    path: '/forecasts/:id',
    routes: { '/automations': <Entry /> },
  })

beforeEach(() => {
  stubSession()
})

describe('oddsPresets', () => {
  it('desk above the market: rises to the desk, or falls 10 pts further away', () => {
    expect(oddsPresets(0.55, 0.62)).toEqual([
      { key: 'desk', label: "When the market reaches the desk's 62%", op: 'above', level: 0.62 },
      { key: 'edge', label: 'If the edge widens by 10 pts', op: 'below', level: 0.45 },
    ])
  })

  it('desk below the market: falls to the desk, or rises 10 pts further away', () => {
    expect(oddsPresets(0.231, 0.1934)).toEqual([
      { key: 'desk', label: "When the market reaches the desk's 19.3%", op: 'below', level: 0.193 },
      { key: 'edge', label: 'If the edge widens by 10 pts', op: 'above', level: 0.331 },
    ])
  })

  it('clamps to 1–99% and leaves out a level the market is already at or past', () => {
    expect(oddsPresets(0.95, 0.5).find((p) => p.key === 'edge')).toEqual({ key: 'edge', label: 'If the edge widens by 10 pts', op: 'above', level: 0.99 })
    expect(oddsPresets(0.05, 0.4).find((p) => p.key === 'edge')).toEqual({ key: 'edge', label: 'If the edge widens by 10 pts', op: 'below', level: 0.01 })
    expect(oddsPresets(0.995, 0.5).map((p) => p.key)).toEqual(['desk']) // 99% is not above 99.5%
    expect(oddsPresets(0.4, 0.4).map((p) => p.key)).toEqual(['edge']) // the market is at the desk already
  })

  it('reads the desk side of the market and prints percentages like the API', () => {
    expect(deskOf(fx.forecastResult, fx.marketOdds)).toBe(0.62)
    expect(deskOf({ probabilities: [{ outcome: 'Apple', p: 0.15 }, { outcome: 'Nvidia', p: 0.81 }] }, { outcome: 'Nvidia' })).toBe(0.81)
    expect(deskOf({ probabilities: [{ outcome: 'Apple', p: 0.15 }] }, { outcome: 'Nvidia' })).toBeNull()
    expect([pctLabel(0.25), pctLabel(0.265), pctLabel(0.231)]).toEqual(['25%', '26.5%', '23.1%'])
  })
})

describe('OddsAlertButton', () => {
  it('sets the desk preset as a notify-only odds alert and says so', async () => {
    const create = vi.spyOn(api, 'createAlert').mockResolvedValue({ alert: fx.oddsAlert() })
    const { user } = show()
    await user.click(screen.getByRole('button', { name: 'Alert me' }))
    const panel = screen.getByRole('group', { name: 'Market alert' })
    expect(panel).toHaveTextContent('Polymarket now 55%')
    expect(screen.getByRole('button', { name: /When the market reaches the desk's 62%/ })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByRole('button', { name: /If the edge widens by 10 pts/ })).toHaveTextContent('falls below 45%')
    await user.click(screen.getByRole('button', { name: 'Set alert' }))
    expect(create).toHaveBeenCalledWith({ symbol: '', trigger: 'odds', forecastId: 'f1', op: 'above', price: 0.62, action: 'notify', source: 'manual' })
    expect(await screen.findByText("Alert set. You'll get a bell when Polymarket crosses 62%.")).toBeInTheDocument()
    expect(screen.getAllByRole('status').map((s) => s.textContent)).toContain('Alert set: Polymarket rises above 62%.Manage in Automations ')
    expect(screen.getByRole('link', { name: /Manage in Automations/ })).toHaveAttribute('href', '/automations')
    expect(screen.queryByRole('group', { name: 'Market alert' })).toBeNull()
  })

  it('shows a refusal inline and keeps the panel open', async () => {
    vi.spyOn(api, 'createAlert').mockRejectedValue(new ApiError(400, 'bad_request', 'The market is already at 63%. Pick a level above it.'))
    const { user } = show()
    await user.click(screen.getByRole('button', { name: 'Alert me' }))
    await user.click(screen.getByRole('button', { name: /If the edge widens/ }))
    await user.click(screen.getByRole('button', { name: 'Set alert' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('The market is already at 63%. Pick a level above it.')
    expect(screen.getByRole('group', { name: 'Market alert' })).toBeInTheDocument()
  })

  it('opens Automations prefilled for a custom level', async () => {
    const { user } = show(0.4) // desk below the 55% market
    await user.click(screen.getByRole('button', { name: 'Alert me' }))
    await user.click(screen.getByRole('button', { name: 'Custom level…' }))
    expect(JSON.parse(screen.getByTestId('entry').textContent!)).toEqual({ trigger: 'odds', forecastId: 'f1', op: 'below', pricePct: 40 })
  })
})

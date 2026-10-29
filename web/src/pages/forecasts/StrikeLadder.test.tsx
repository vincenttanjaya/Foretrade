import { act, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from '../../lib/api'
import type { Forecast } from '../../lib/types'
import * as fx from '../../test/fixtures'
import { renderPage, stubSession } from '../../test/utils'
import { ForecastDetailPage } from './ForecastDetailPage'

function open(f: Forecast) {
  vi.spyOn(api, 'forecast').mockResolvedValue({ forecast: f })
  return renderPage(<ForecastDetailPage />, { route: `/forecasts/${f.id}`, path: '/forecasts/:id' })
}
const card = () => screen.getByRole('region', { name: 'Desk vs market by strike' })
const cells = () =>
  within(card())
    .getAllByRole('row')
    .slice(1)
    .map((r) => [...r.querySelectorAll('td')].map((c) => c.textContent))
const noLadder = (over: Partial<Forecast> = {}) =>
  fx.ladderForecast({ resolutionDate: '2026-12-31', result: { ...fx.ladderForecast().result!, ladder: undefined }, ...over })

// Now is Oct 2, 2026 12:00 in Los Angeles (19:00 UTC).
beforeEach(() => {
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(new Date(2026, 9, 2, 12))
  stubSession()
})
afterEach(() => vi.useRealTimers())

describe('StrikeLadder · Desk vs market by strike', () => {
  it('lists each strike with the desk P(above), the market, the edge in colour and the lean', async () => {
    open(fx.ladderForecast())
    await screen.findByRole('heading', { name: 'Desk vs market by strike' })
    expect(card()).toHaveTextContent('Polymarket · Bitcoin above ___ on Oct 9 · ends Oct 9, 16:00 UTC · $150.4K volume')
    expect(within(card()).getByRole('link', { name: /View on Polymarket/ })).toHaveAttribute('href', 'https://polymarket.com/event/bitcoin-above-on-october-9-2026')
    expect(cells()).toEqual([
      ['$80,000', '90%', '95%', '−5 pts', 'NO'],
      ['$85,000', '75%', '72.5%', '+2.5 pts', 'WAIT'],
      ['$90,000', '60%', '50%', '+10 pts', 'YES'],
      ['$95,000', '40%', '38.5%', '+1.5 pts', 'thinWAIT'],
    ])
    expect(within(card()).getByText('−5 pts')).toHaveClass('text-down')
    expect(within(card()).getByText('+2.5 pts')).toHaveClass('text-up')
    expect(within(card()).getByText('+1.5 pts')).toHaveClass('text-muted') // a thin market's edge is not read
    expect(within(card()).getByText('thin')).toBeInTheDocument()
    expect(card()).toHaveTextContent(
      "Desk P(above) adds up the report's buckets, linear inside a bucket. Polymarket prices are crowd odds, not facts. Simulated · not financial advice.",
    )
    expect(card()).toHaveTextContent('as of Oct 2, 14:05 UTC')
    expect(screen.queryByRole('heading', { name: 'Desk vs market' })).toBeNull() // no single-market card on an ordered report
  })

  it('refreshes the odds and searches again', async () => {
    const moved = fx.ladderForecast()
    moved.result = { ...moved.result!, ladder: [fx.ladderRow(80000, 0.9, 0, 'wait'), fx.ladderRow(90000, 0.55, 5, 'yes')] }
    const refresh = vi.spyOn(api, 'refreshMarket').mockResolvedValue({ forecast: moved })
    const { user } = open(fx.ladderForecast())
    await user.click(await within(await screen.findByRole('region', { name: 'Desk vs market by strike' })).findByRole('button', { name: 'Refresh odds' }))
    expect(refresh).toHaveBeenCalledWith('f-ladder', false)
    await waitFor(() => expect(cells()).toEqual([['$80,000', '90%', '90%', '±0 pts', 'WAIT'], ['$90,000', '60%', '55%', '+5 pts', 'YES']]))
    await user.click(within(card()).getByRole('button', { name: 'Search again' }))
    expect(refresh).toHaveBeenLastCalledWith('f-ladder', true)
  })

  it('says why there is no ladder and keeps Search again gated for 10 minutes, re-enabling it on time', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date(2026, 9, 2, 12))
    const refresh = vi.spyOn(api, 'refreshMarket')
    open(noLadder({ result: { ...noLadder().result!, marketCheckedAt: '2026-10-02T18:52:00Z' } }))
    await act(() => vi.advanceTimersByTimeAsync(0))
    expect(card()).toHaveTextContent(
      'No Polymarket strike ladder ends within a day of Dec 31, 2026. Polymarket lists daily BTC price ladders about a week ahead.',
    )
    const again = within(card()).getByRole('button', { name: 'Search again' })
    expect(again).toBeDisabled()
    expect(again).toHaveAttribute('title', 'Searched 8 min ago')
    await act(() => vi.advanceTimersByTimeAsync(60_000))
    expect(again).toBeDisabled()
    await act(() => vi.advanceTimersByTimeAsync(60_000))
    expect(again).toBeEnabled()
    expect(refresh).not.toHaveBeenCalled() // nothing searches on its own
  })

  it('shows nothing on an archived report without a ladder', async () => {
    open(noLadder({ status: 'archived' }))
    await screen.findByRole('heading', { level: 1 })
    expect(screen.queryByRole('region', { name: 'Desk vs market by strike' })).toBeNull()
  })
})

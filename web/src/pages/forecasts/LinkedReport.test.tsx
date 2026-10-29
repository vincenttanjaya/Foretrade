import { screen, within } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from '../../lib/api'
import * as fx from '../../test/fixtures'
import { linkedDraft, linkedMarket } from '../../test/predictions'
import { renderPage, stubSession } from '../../test/utils'
import { ForecastDetailPage } from './ForecastDetailPage'

beforeEach(() => { stubSession(); vi.spyOn(api, 'marketPositions').mockResolvedValue({ positions: [] }) })
describe('saved linked reports', () => {
  it.each(['changed', 'unavailable', 'legacy'] as const)('explains %s quotes and blocks fresh stake/odds actions while preserving stored odds', async (linkStatus) => {
    const f = fx.forecast({ ...linkedDraft, result: { ...fx.forecastResult, market: { ...fx.marketOdds, question: linkedMarket.question, asOf: new Date().toISOString(), linkStatus, signal: 'wait' } } })
    vi.spyOn(api, 'forecast').mockResolvedValue({ forecast: f })
    renderPage(<ForecastDetailPage />, { route: `/forecasts/${f.id}`, path: '/forecasts/:id' })
    const section = (await screen.findByRole('heading', { name: 'Desk vs market' })).closest('section')!
    expect(within(section).getByText(/Fresh actions paused/)).toHaveTextContent(linkStatus)
    expect(within(section).queryByRole('group', { name: 'Take a paper position' })).not.toBeInTheDocument()
    expect(within(section).queryByRole('button', { name: /Set.*alert|reaches desk|edge widens/i })).not.toBeInTheDocument()
    expect(within(section).queryByText(/per \$1 .* contract/)).not.toBeInTheDocument()
    expect(screen.getByRole('region', { name: 'Linked Polymarket contract' })).toHaveTextContent('Manual resolution in Foretrade')
    expect(screen.getByText(/Provider ends 2026-10-12T16:00:00Z/)).toBeInTheDocument()
  })
})

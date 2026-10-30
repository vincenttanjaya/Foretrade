// The upload body cap is checked before sending (API contract: the server answers an oversize body with a 400).
// Five files at the per-file limits stay under the real 32 MiB, so this file lowers the cap to reach the check.
import { fireEvent, screen } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from '../../lib/api'
import type { ForecastDraft } from '../../lib/types'
import * as fx from '../../test/fixtures'
import { renderPage, stubSession } from '../../test/utils'
import { YourMaterials } from './Materials'
import { NewForecastPage } from './NewForecastPage'

vi.mock('../../lib/api', async (importOriginal) => ({ ...(await importOriginal<typeof import('../../lib/api')>()), ATTACHMENT_BODY_LIMIT: 2_000 }))

const draft: ForecastDraft = {
  shape: 'binary',
  question: 'Will BTC trade above $150k before July 2027?',
  outcomes: ['Yes', 'No'],
  resolutionCriteria: 'Resolves Yes if the Binance BTCUSDT price trades above $150,000 before Jul 1, 2027.',
  resolutionDate: '2027-07-01',
  category: 'crypto',
  priceRule: null,
  context: '',
}
const notes = () => new File(['Inflows slowed in week 2. '.repeat(60)], 'notes.txt', { type: 'text/plain' }) // 1,560 bytes, 2,080 in base64

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(new Date(2026, 9, 2, 12))
  stubSession()
  return () => vi.useRealTimers()
})

describe('materials · the upload body cap', () => {
  it('stops a new forecast whose files are too large to send together, and sends nothing', async () => {
    const create = vi.spyOn(api, 'createForecast')
    const { user } = renderPage(<NewForecastPage />, { route: '/forecasts/new', path: '/forecasts/new', state: { draft } })
    await screen.findByRole('heading', { name: 'Review the question.' })
    fireEvent.change(screen.getByLabelText('Add material'), { target: { files: [notes()] } })
    await screen.findByText(/^Text · 2 kB$/)
    await user.click(screen.getByRole('button', { name: /Run forecast/ }))
    expect(await screen.findByRole('alert')).toHaveTextContent('These files are too large to send together. Remove one and try again.')
    expect(create).not.toHaveBeenCalled()
  })

  it('stops an update whose files are too large to send together', async () => {
    const update = vi.spyOn(api, 'updateForecast')
    vi.setSystemTime(new Date('2026-10-06T12:00:00Z')) // before the fixture's resolution date: it can update
    const f = fx.materialsForecast()
    const { user } = renderPage(<YourMaterials f={f} />, { route: `/forecasts/${f.id}`, path: '/forecasts/:id' })
    await user.click(screen.getByText('Add material to the next update'))
    fireEvent.change(screen.getByLabelText('Add material'), { target: { files: [notes()] } })
    await screen.findByText(/^Text · 2 kB$/)
    await user.click(screen.getByRole('button', { name: /Update with these materials/ }))
    expect(await screen.findByRole('alert')).toHaveTextContent('These files are too large to send together. Remove one and try again.')
    expect(update).not.toHaveBeenCalled()
  })
})

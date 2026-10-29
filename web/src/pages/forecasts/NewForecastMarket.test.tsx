import { act, fireEvent, screen, waitFor } from '@testing-library/react'
import { Link } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError } from '../../lib/api'
import * as fx from '../../test/fixtures'
import { linkedDraft, linkedMarket, predictionDraft } from '../../test/predictions'
import { creditsUsed, currentPath, renderPage, stubSession } from '../../test/utils'
import { NewForecastPage } from './NewForecastPage'

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(new Date('2026-10-05T12:00:00Z'))
  stubSession()
  vi.spyOn(api, 'predictionDraft').mockResolvedValue(predictionDraft)
})
afterEach(() => vi.useRealTimers())
const open = () => renderPage(<NewForecastPage />, { route: '/forecasts/new?marketId=123', path: '/forecasts/new', routes: { '/forecasts/:id': <p>report</p> } })

describe('canonical forecast review', () => {
  it('loads a free canonical ID draft preserving full rules and manual tracking before one paid run', async () => {
    const rules = `${linkedMarket.description}\n${'🪙'.repeat(2200)}`
    vi.mocked(api.predictionDraft).mockResolvedValue({ ...predictionDraft, draft: { ...linkedDraft, resolutionCriteria: rules, linkedMarket: { ...linkedMarket, description: rules } } })
    const paidDraft = vi.spyOn(api, 'draftForecast')
    const create = vi.spyOn(api, 'createForecast').mockResolvedValue({ forecast: fx.forecast({ id: 'canonical' }), credits: { ...fx.credits, used: 22 } })
    const { user } = open()
    expect(await screen.findByLabelText('Question')).toHaveValue(linkedMarket.question)
    expect(screen.getByLabelText('Resolution criteria')).toHaveValue(rules)
    expect(screen.getByText(/Manual resolution in Foretrade/)).toBeInTheDocument()
    expect(screen.getByLabelText('Tracking date')).toHaveValue('2026-10-12')
    expect(screen.queryByText(/Auto-resolves from Binance/)).not.toBeInTheDocument()
    expect(paidDraft).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: /Run forecast.*5 cr/ }))
    await screen.findByText('report')
    expect(create).toHaveBeenCalledTimes(1)
    expect(create.mock.calls[0][0]).toMatchObject({ resolutionCriteria: rules, priceRule: null, linkedMarket: { description: rules, marketId: '123' } })
    expect(creditsUsed()).toBe('22')
  })

  it('predicate edits remove provenance and preserve over-cap rules with a visible ordinary limit', async () => {
    const rules = 'x'.repeat(2400)
    vi.mocked(api.predictionDraft).mockResolvedValue({ ...predictionDraft, draft: { ...linkedDraft, resolutionCriteria: rules, linkedMarket: { ...linkedMarket, description: rules } } })
    open()
    await screen.findByLabelText('Question')
    fireEvent.change(screen.getByLabelText('Question'), { target: { value: 'Will Bitcoin be below $100,000 on October 12?' } })
    expect(screen.getByText(/Market link removed/)).toBeInTheDocument()
    expect(screen.getByLabelText('Resolution criteria')).toHaveValue(rules)
    expect(screen.getByText(/within 2,000 characters/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Run forecast/ })).toBeDisabled()
  })

  it('keeps edited notes and complete rules after changed upstream rejects a run without credits', async () => {
    vi.spyOn(api, 'createForecast').mockRejectedValue(new ApiError(409, 'conflict', 'The linked market rules changed. Review a fresh draft.'))
    const { user } = open()
    await screen.findByLabelText('Question')
    await user.click(screen.getByRole('button', { name: /Run forecast/ }))
    expect(await screen.findByRole('alert')).toHaveTextContent('linked market rules changed')
    expect(screen.getByLabelText('Resolution criteria')).toHaveValue(linkedMarket.description)
    expect(creditsUsed()).toBe(String(fx.credits.used))
    expect(screen.getByRole('button', { name: 'Review fresh market rules' })).toBeInTheDocument()
    expect(api.predictionDraft).toHaveBeenCalledTimes(1)
  })

  it('shows same-day refusal without changing its date or invoking paid drafting', async () => {
    vi.mocked(api.predictionDraft).mockRejectedValue(new ApiError(409, 'conflict', 'This market ends today; the desk needs a future tracking date.'))
    const paidDraft = vi.spyOn(api, 'draftForecast')
    open()
    expect(await screen.findByRole('alert')).toHaveTextContent('ends today')
    expect(screen.getByRole('link', { name: 'Back to markets' })).toHaveAttribute('href', '/forecasts?tab=markets')
    expect(paidDraft).not.toHaveBeenCalled()
    await waitFor(() => expect(api.predictionDraft).toHaveBeenCalledWith('123', expect.any(AbortSignal)))
  })

  it('clears a canonical review when the same route navigates to an ordinary new forecast', async () => {
    const { user } = renderPage(<><Link to="/forecasts/new">New ordinary forecast</Link><NewForecastPage /></>, {
      route: '/forecasts/new?marketId=123', path: '/forecasts/new',
    })
    await screen.findByLabelText('Question')
    await user.click(screen.getByRole('link', { name: 'New ordinary forecast' }))
    expect(await screen.findByLabelText('Your rough idea')).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'Linked Polymarket contract' })).not.toBeInTheDocument()
  })

  it('ignores a late prior ID response after same-route selection changes', async () => {
    let finish!: (d: typeof predictionDraft) => void
    vi.mocked(api.predictionDraft).mockImplementationOnce(() => new Promise((r) => { finish = r }))
    const { user } = renderPage(<><Link to="/forecasts/new?marketId=124">Other leaf</Link><NewForecastPage /></>, {
      route: '/forecasts/new?marketId=123', path: '/forecasts/new',
    })
    await waitFor(() => expect(api.predictionDraft).toHaveBeenCalledTimes(1))
    const signal = vi.mocked(api.predictionDraft).mock.calls[0][1]!
    vi.mocked(api.predictionDraft).mockResolvedValue({ ...predictionDraft, draft: { ...linkedDraft, question: 'A different exact leaf question?', linkedMarket: { ...linkedMarket, marketId: '124', question: 'A different exact leaf question?' } } })
    await user.click(screen.getByRole('link', { name: 'Other leaf' }))
    expect(signal.aborted).toBe(true)
    expect(await screen.findByLabelText('Question')).toHaveValue('A different exact leaf question?')
    await act(async () => finish(predictionDraft))
    expect(screen.getByLabelText('Question')).toHaveValue('A different exact leaf question?')
  })

  it('requires explicit acceptance of fresh canonical rules and keeps context notes', async () => {
    const { user } = open()
    await screen.findByLabelText('Question')
    await user.click(screen.getByRole('button', { name: 'Add context for the desk (optional)' }))
    fireEvent.change(screen.getByLabelText('Context'), { target: { value: 'My private notes' } })
    const updated = { ...predictionDraft, draft: { ...linkedDraft, resolutionCriteria: 'Fresh original complete rules', linkedMarket: { ...linkedMarket, description: 'Fresh original complete rules', asOf: '2026-10-05T12:05:00Z' } } }
    vi.mocked(api.predictionDraft).mockResolvedValue(updated)
    await user.click(screen.getByRole('button', { name: 'Review fresh market rules' }))
    await screen.findByRole('button', { name: 'Use fresh market draft' })
    expect(screen.getByLabelText('Resolution criteria')).toHaveValue(linkedMarket.description)
    expect(screen.getByLabelText('Context')).toHaveValue('My private notes')
    await user.click(screen.getByRole('button', { name: 'Use fresh market draft' }))
    expect(screen.getByLabelText('Resolution criteria')).toHaveValue('Fresh original complete rules')
    expect(screen.getByLabelText('Context')).toHaveValue('My private notes')
    expect(creditsUsed()).toBe(String(fx.credits.used))
  })

  it('clears the prior run refusal after accepting a fresh read even within the same timestamp second', async () => {
    vi.spyOn(api, 'createForecast').mockRejectedValue(new ApiError(409, 'conflict', 'The linked market rules changed. Review a fresh draft.'))
    const { user } = open()
    await screen.findByLabelText('Question')
    await user.click(screen.getByRole('button', { name: /Run forecast/ }))
    expect(await screen.findByRole('alert')).toHaveTextContent('rules changed')
    await user.click(screen.getByRole('button', { name: 'Review fresh market rules' }))
    await user.click(await screen.findByRole('button', { name: 'Use fresh market draft' }))
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(api.createForecast).toHaveBeenCalledTimes(1)
  })

  it.each(['existing', 'late'] as const)('keeps one paid-create owner while an %s fresh preview is available', async (timing) => {
    let finishCreate!: (r: Awaited<ReturnType<typeof api.createForecast>>) => void
    const create = vi.spyOn(api, 'createForecast').mockImplementation(() => new Promise((r) => { finishCreate = r }))
    const { user } = open()
    await screen.findByLabelText('Question')
    await user.click(screen.getByRole('button', { name: 'Add context for the desk (optional)' }))
    fireEvent.change(screen.getByLabelText('Context'), { target: { value: 'Keep these private notes' } })
    const updated = { ...predictionDraft, draft: { ...linkedDraft, resolutionCriteria: 'Fresh complete original rules', linkedMarket: { ...linkedMarket, description: 'Fresh complete original rules', asOf: '2026-10-05T12:05:00Z' } } }
    let finishRead!: (r: typeof predictionDraft) => void
    vi.mocked(api.predictionDraft).mockImplementationOnce(() => timing === 'existing' ? Promise.resolve(updated) : new Promise((r) => { finishRead = r }))
    await user.click(screen.getByRole('button', { name: 'Review fresh market rules' }))
    if (timing === 'existing') await screen.findByRole('button', { name: 'Use fresh market draft' })
    await user.click(screen.getByRole('button', { name: /Run forecast/ }))
    if (timing === 'late') await act(async () => finishRead(updated))
    const accept = await screen.findByRole('button', { name: 'Use fresh market draft' })
    expect(accept).toBeDisabled()
    await user.click(accept)
    expect(screen.getByRole('button', { name: /Starting/ })).toBeDisabled()
    expect(screen.getByLabelText('Resolution criteria')).toHaveValue(linkedMarket.description)
    expect(screen.getByLabelText('Context')).toHaveValue('Keep these private notes')
    expect(create).toHaveBeenCalledTimes(1)
    expect(creditsUsed()).toBe(String(fx.credits.used))
    await act(async () => finishCreate({ forecast: fx.forecast({ id: 'owned-create' }), credits: { ...fx.credits, used: 22 } }))
    expect(await screen.findByText('report')).toBeInTheDocument()
    expect(currentPath()).toBe('/forecasts/owned-create')
    expect(creditsUsed()).toBe('22')
  })

  it('retains the pending owner across an ID remount and ignores the obsolete create response', async () => {
    let finishCreate!: (r: Awaited<ReturnType<typeof api.createForecast>>) => void
    const create = vi.spyOn(api, 'createForecast').mockImplementationOnce(() => new Promise((r) => { finishCreate = r }))
    const { user } = renderPage(<><Link to="/forecasts/new?marketId=124">Other leaf</Link><NewForecastPage /></>, {
      route: '/forecasts/new?marketId=123', path: '/forecasts/new', routes: { '/forecasts/:id': <p>report</p> },
    })
    await screen.findByLabelText('Question')
    await user.click(screen.getByRole('button', { name: /Run forecast/ }))
    const question = 'A different exact leaf question?'
    vi.mocked(api.predictionDraft).mockResolvedValue({ ...predictionDraft, draft: { ...linkedDraft, question, linkedMarket: { ...linkedMarket, marketId: '124', question } } })
    await user.click(screen.getByRole('link', { name: 'Other leaf' }))
    expect(await screen.findByLabelText('Question')).toHaveValue(question)
    expect(screen.getByRole('button', { name: /Starting/ })).toBeDisabled()
    expect(create).toHaveBeenCalledTimes(1)
    await act(async () => finishCreate({ forecast: fx.forecast({ id: 'obsolete-create' }), credits: { ...fx.credits, used: 22 } }))
    expect(currentPath()).toBe('/forecasts/new?marketId=124')
    expect(creditsUsed()).toBe(String(fx.credits.used))
    create.mockResolvedValue({ forecast: fx.forecast({ id: 'current-create' }), credits: { ...fx.credits, used: 22 } })
    await user.click(screen.getByRole('button', { name: /Run forecast/ }))
    expect(await screen.findByText('report')).toBeInTheDocument()
    expect(currentPath()).toBe('/forecasts/current-create')
    expect(create).toHaveBeenCalledTimes(2)
  })

  it('releases a refused create for explicit fresh acceptance and a normal retry', async () => {
    let refuse!: (e: ApiError) => void
    const create = vi.spyOn(api, 'createForecast').mockImplementationOnce(() => new Promise((_, reject) => { refuse = reject }))
    const { user } = open()
    await screen.findByLabelText('Question')
    await user.click(screen.getByRole('button', { name: 'Review fresh market rules' }))
    await screen.findByRole('button', { name: 'Use fresh market draft' })
    await user.click(screen.getByRole('button', { name: /Run forecast/ }))
    await act(async () => refuse(new ApiError(409, 'conflict', 'The linked market rules changed.')))
    expect(await screen.findByRole('alert')).toHaveTextContent('rules changed')
    expect(screen.getByRole('button', { name: 'Use fresh market draft' })).toBeEnabled()
    await user.click(screen.getByRole('button', { name: 'Use fresh market draft' }))
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    create.mockResolvedValue({ forecast: fx.forecast({ id: 'retry-create' }), credits: { ...fx.credits, used: 22 } })
    await user.click(screen.getByRole('button', { name: /Run forecast/ }))
    expect(await screen.findByText('report')).toBeInTheDocument()
    expect(create).toHaveBeenCalledTimes(2)
  })

  it('preserves provenance and offers free reconfirmation when editing a saved linked draft', async () => {
    const { user } = renderPage(<NewForecastPage />, {
      route: '/forecasts/new', path: '/forecasts/new', state: { draft: { ...linkedDraft, context: 'Saved notes' } },
    })
    expect(await screen.findByRole('region', { name: 'Linked Polymarket contract' })).toHaveTextContent(linkedMarket.endDate)
    expect(screen.getByLabelText('Tracking date')).toHaveValue('2026-10-12')
    expect(api.predictionDraft).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'Review fresh market rules' }))
    await screen.findByRole('button', { name: 'Use fresh market draft' })
    expect(api.predictionDraft).toHaveBeenCalledWith('123', expect.any(AbortSignal))
    expect(screen.getByLabelText('Context')).toHaveValue('Saved notes')
  })

  it('returns a linked review to Markets and aborts a pending free reconfirmation', async () => {
    const { user } = renderPage(<NewForecastPage />, {
      route: '/forecasts/new?marketId=123', path: '/forecasts/new', routes: { '/forecasts': <p>Market sample</p> },
    })
    await screen.findByLabelText('Question')
    vi.mocked(api.predictionDraft).mockImplementationOnce(() => new Promise(() => {}))
    await user.click(screen.getByRole('button', { name: 'Review fresh market rules' }))
    const signal = vi.mocked(api.predictionDraft).mock.calls.at(-1)![1]!
    await user.click(screen.getByRole('button', { name: 'Back to markets' }))
    expect(currentPath()).toBe('/forecasts?tab=markets')
    expect(signal.aborted).toBe(true)
  })
})

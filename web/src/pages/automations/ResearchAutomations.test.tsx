import { act, fireEvent, screen, waitFor, within } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError } from '../../lib/api'
import type { Alert, AlertRun } from '../../lib/types'
import * as fx from '../../test/fixtures'
import { renderPage, stubSession } from '../../test/utils'
import { AutomationsPage } from './AutomationsPage'

const run = (over: Partial<AlertRun> = {}): AlertRun => ({ id: 'r1', at: '2026-10-06T08:00:00Z', status: 'ok', body: 'Evidence changed [1].', error: null, reportChatId: 'owned-report', excerpt: 'Evidence changed.', verdict: 'weakened', ...over })
const research = (over: Partial<Alert> = {}): Alert => ({ ...fx.schedule(), id: 'research1', symbol: '', action: 'research', notional: null, prompt: 'Review the market evidence', thesis: null, notifyWhen: 'changed', quotaStreak: 0, lastRun: run(), ...over })
const show = (alerts: Alert[] = []) => {
  const list = vi.spyOn(api, 'alerts').mockResolvedValue({ alerts })
  return { list, ...renderPage(<AutomationsPage />, { route: '/automations', path: '/automations' }) }
}
async function chooseResearch(user: ReturnType<typeof renderPage>['user']) {
  await user.click(screen.getByRole('button', { name: 'Schedule' }))
  await user.click(screen.getByRole('button', { name: 'AI research' }))
}
beforeEach(() => stubSession())

describe('scheduled research form', () => {
  it('discloses two credits per run before free creation and sends a general weekday prompt without order fields', async () => {
    const create = vi.spyOn(api, 'createAlert').mockResolvedValue({ alert: research({ lastRun: null }) })
    const { user } = show()
    await chooseResearch(user)
    expect(screen.getByText(/2 AI credits per run/)).toBeInTheDocument()
    expect(screen.getByText(/Creating the automation is free/)).toBeInTheDocument()
    expect(screen.queryByLabelText('Amount ($)')).not.toBeInTheDocument()
    await user.selectOptions(screen.getByLabelText('Asset'), '')
    await user.click(screen.getByRole('button', { name: 'Weekdays' }))
    await user.type(screen.getByLabelText('Research prompt'), '  Review the market evidence  ')
    await user.selectOptions(screen.getByLabelText('Notify when'), 'changed')
    expect(create).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'Create automation' }))
    expect(create).toHaveBeenCalledWith({ trigger: 'schedule', every: 'weekdays', at: '08:00', action: 'research', prompt: 'Review the market evidence', notifyWhen: 'changed', note: undefined, source: 'manual' })
    expect(await screen.findByText('Review the market evidence')).toBeInTheDocument()
  })

  it('counts Unicode code points at 10 and 500, rejecting 9 and 501 without submitting', async () => {
    const create = vi.spyOn(api, 'createAlert')
    const { user } = show()
    await chooseResearch(user)
    const prompt = screen.getByLabelText('Research prompt')
    const createButton = screen.getByRole('button', { name: 'Create automation' })
    for (const [n, allowed] of [[9, false], [10, true], [500, true], [501, false]] as const) {
      fireEvent.change(prompt, { target: { value: '🪙'.repeat(n) } })
      expect((createButton as HTMLButtonElement).disabled, `${n} Unicode code points`).toBe(!allowed)
    }
    fireEvent.submit(createButton.closest('form')!)
    expect(create).not.toHaveBeenCalled()
  })

  it('requires a configured thesis asset and bounds each thesis field to 300 code points', async () => {
    const create = vi.spyOn(api, 'createAlert').mockResolvedValue({ alert: research({ symbol: 'NVDA', thesis: { statement: '🪙'.repeat(300), invalidation: 'Demand deteriorates' } }) })
    const { user } = show()
    await chooseResearch(user)
    await user.selectOptions(screen.getByLabelText('Asset'), '')
    await user.type(screen.getByLabelText('Research prompt'), 'Review the NVIDIA thesis')
    await user.click(screen.getByRole('checkbox', { name: 'Track a thesis' }))
    fireEvent.change(screen.getByLabelText('Thesis'), { target: { value: '🪙'.repeat(300) } })
    await user.type(screen.getByLabelText('Invalidation'), 'Demand deteriorates')
    expect(screen.getByRole('button', { name: 'Create automation' })).toBeDisabled()
    expect(screen.getByRole('option', { name: 'NVDA' })).toBeInTheDocument()
    await user.selectOptions(screen.getByLabelText('Asset'), 'NVDA')
    expect(screen.getByRole('button', { name: 'Create automation' })).toBeEnabled()
    fireEvent.change(screen.getByLabelText('Thesis'), { target: { value: '🪙'.repeat(301) } })
    expect(screen.getByRole('button', { name: 'Create automation' })).toBeDisabled()
    fireEvent.change(screen.getByLabelText('Thesis'), { target: { value: '🪙'.repeat(300) } })
    await user.click(screen.getByRole('button', { name: 'Create automation' }))
    expect(create.mock.calls[0][0]).toMatchObject({ symbol: 'NVDA', action: 'research', thesis: { statement: '🪙'.repeat(300), invalidation: 'Demand deteriorates' }, notifyWhen: 'always' })
  })

  it('removes research payloads when changing to a deterministic price trigger', async () => {
    const create = vi.spyOn(api, 'createAlert').mockResolvedValue({ alert: fx.alert() })
    const { user } = show()
    await chooseResearch(user)
    await user.type(screen.getByLabelText('Research prompt'), 'Review the market evidence')
    await user.click(screen.getByRole('button', { name: 'Price' }))
    await user.type(screen.getByLabelText('Price (USD)'), '50000')
    await user.click(screen.getByRole('button', { name: 'Create automation' }))
    expect(create.mock.calls[0][0]).toMatchObject({ action: 'notify', symbol: 'BTC', op: 'below', price: 50000 })
    expect(create.mock.calls[0][0]).not.toHaveProperty('prompt')
    expect(create.mock.calls[0][0]).not.toHaveProperty('thesis')
  })

  it('does not submit an unsupported symbol supplied by a URL until the user chooses a configured asset or general research', async () => {
    const create = vi.spyOn(api, 'createAlert').mockResolvedValue({ alert: research() })
    vi.spyOn(api, 'alerts').mockResolvedValue({ alerts: [] })
    const { user } = renderPage(<AutomationsPage />, { route: '/automations?symbol=unknown', path: '/automations' })
    await chooseResearch(user)
    await user.type(screen.getByLabelText('Research prompt'), 'Review the market evidence')
    expect(screen.getByRole('button', { name: 'Create automation' })).toBeDisabled()
    await user.selectOptions(screen.getByLabelText('Asset'), '')
    await user.click(screen.getByRole('button', { name: 'Create automation' }))
    expect(create.mock.calls[0][0]).not.toHaveProperty('symbol')
  })

  it('resets successful general research to a valid deterministic brief and includes its displayed BTC asset on the next Create', async () => {
    const create = vi.spyOn(api, 'createAlert').mockResolvedValueOnce({ alert: research({ symbol: '', lastRun: null }) }).mockResolvedValueOnce({ alert: fx.schedule({ symbol: 'BTC', every: 'day', at: '08:00', weekday: null }) })
    const { user } = show()
    await chooseResearch(user)
    await user.selectOptions(screen.getByLabelText('Asset'), '')
    await user.type(screen.getByLabelText('Research prompt'), 'Review the market evidence')
    await user.click(screen.getByRole('button', { name: 'Create automation' }))
    expect(await screen.findByText('Review the market evidence')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Brief me' })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByLabelText('Asset')).toHaveValue('BTC')
    await user.click(screen.getByRole('button', { name: 'Create automation' }))
    expect(create).toHaveBeenCalledTimes(2)
    expect(create.mock.calls[1][0]).toEqual({ symbol: 'BTC', trigger: 'schedule', every: 'day', at: '08:00', action: 'notify', note: undefined, source: 'manual' })
  })
})

describe('owned research state and history', () => {
  it('shows paused quota state with explicit Resume and Cancel, and a quota refusal stays paused', async () => {
    const resume = vi.spyOn(api, 'resumeAlert').mockRejectedValue(new ApiError(429, 'ai_quota', 'Out of AI credits today.'))
    vi.spyOn(api, 'cancelAlert').mockResolvedValue(undefined)
    const { user } = show([research({ status: 'paused', quotaStreak: 3, lastRun: run({ status: 'failed', error: 'Out of AI credits today.', reportChatId: null, verdict: null }) })])
    await user.click(await screen.findByRole('button', { name: 'History · 1' }))
    expect(await screen.findByText('Paused')).toBeInTheDocument()
    expect(screen.getByText(/3 consecutive runs/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Resume' }))
    expect(resume).toHaveBeenCalledWith('research1')
    expect(await screen.findByRole('alert')).toHaveTextContent('Daily AI credits used up. They reset at 00:00 UTC.')
    expect(screen.getByText('Paused')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(await screen.findByText('Cancelled')).toBeInTheDocument()
  })

  it('loads owned bounded history using an exclusive cursor, renders unavailable reports and keeps failed older loads retryable', async () => {
    const history = vi.spyOn(api, 'alertRuns').mockResolvedValueOnce({ runs: [run()], nextCursor: 'opaque+/cursor' }).mockRejectedValueOnce(new Error('offline')).mockResolvedValueOnce({ runs: [run({ id: 'r1' }), run({ id: 'r0', status: 'interrupted', error: 'Interrupted.', reportChatId: null, verdict: 'unknown' })], nextCursor: null })
    const { user } = show([research()])
    await user.click(await screen.findByRole('button', { name: 'Run history' }))
    const panel = await screen.findByRole('region', { name: 'Research run history' })
    expect(history).toHaveBeenCalledWith('research1', { limit: 20 }, expect.any(AbortSignal))
    expect(within(panel).getByRole('link', { name: 'Open chat' })).toHaveAttribute('href', '/chat/owned-report')
    await user.click(within(panel).getByRole('button', { name: 'Load older' }))
    expect(await within(panel).findByRole('alert')).toHaveTextContent(/load/i)
    await user.click(within(panel).getByRole('button', { name: 'Load older' }))
    expect(history).toHaveBeenLastCalledWith('research1', { limit: 20, before: 'opaque+/cursor' }, expect.any(AbortSignal))
    expect(within(panel).getAllByRole('link', { name: 'Open chat' })).toHaveLength(1)
    expect(within(panel).getByText(/Report chat unavailable/)).toBeInTheDocument()
    expect(within(panel).getByText('Interrupted')).toBeInTheDocument()
    expect(within(panel).queryByRole('button', { name: 'Load older' })).not.toBeInTheDocument()
  })

  it('ignores a delayed list read after Cancel succeeds', async () => {
    let resolve!: (value: { alerts: Alert[] }) => void
    const list = vi.spyOn(api, 'alerts').mockResolvedValueOnce({ alerts: [research()] }).mockImplementationOnce(() => new Promise(done => { resolve = done }))
    vi.spyOn(api, 'cancelAlert').mockResolvedValue(undefined)
    const { user } = renderPage(<AutomationsPage />, { route: '/automations', path: '/automations' })
    await screen.findByRole('button', { name: 'Cancel' })
    act(() => document.dispatchEvent(new Event('visibilitychange')))
    await waitFor(() => expect(list).toHaveBeenCalledTimes(2))
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    await screen.findByText('No active automations.')
    await act(async () => resolve({ alerts: [research()] }))
    expect(screen.getByText('No active automations.')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Cancel' })).not.toBeInTheDocument()
  })

  it('keeps one successful Create when an older list read arrives and the form is submitted twice', async () => {
    let resolve!: (value: { alerts: Alert[] }) => void
    const list = vi.spyOn(api, 'alerts').mockResolvedValueOnce({ alerts: [] }).mockImplementationOnce(() => new Promise(done => { resolve = done }))
    const create = vi.spyOn(api, 'createAlert').mockResolvedValue({ alert: research({ lastRun: null }) })
    const { user } = renderPage(<AutomationsPage />, { route: '/automations', path: '/automations' })
    await waitFor(() => expect(list).toHaveBeenCalledTimes(1))
    await chooseResearch(user)
    await user.type(screen.getByLabelText('Research prompt'), 'Review the market evidence')
    act(() => document.dispatchEvent(new Event('visibilitychange')))
    await waitFor(() => expect(list).toHaveBeenCalledTimes(2))
    const form = screen.getByRole('button', { name: 'Create automation' }).closest('form')!
    act(() => { fireEvent.submit(form); fireEvent.submit(form) })
    expect(await screen.findByRole('button', { name: 'Cancel' })).toBeInTheDocument()
    expect(create).toHaveBeenCalledTimes(1)
    await act(async () => resolve({ alerts: [] }))
    expect(screen.getByRole('button', { name: 'Cancel' })).toBeInTheDocument()
    expect(screen.getByText('Active · 1')).toBeInTheDocument()
  })

  it('keeps a successful Resume and its previous report when an older paused list read arrives', async () => {
    const paused = research({ status: 'paused', quotaStreak: 3 })
    let resolve!: (value: { alerts: Alert[] }) => void
    const list = vi.spyOn(api, 'alerts').mockResolvedValueOnce({ alerts: [paused] }).mockImplementationOnce(() => new Promise(done => { resolve = done }))
    const resume = vi.spyOn(api, 'resumeAlert').mockResolvedValue({ alert: research({ quotaStreak: 0, lastRun: null, nextRunAt: '2026-10-07T08:00:00Z' }) })
    const { user } = renderPage(<AutomationsPage />, { route: '/automations?tab=history', path: '/automations' })
    await screen.findByRole('button', { name: 'Resume' })
    act(() => document.dispatchEvent(new Event('visibilitychange')))
    await waitFor(() => expect(list).toHaveBeenCalledTimes(2))
    await user.click(screen.getByRole('button', { name: 'Resume' }))
    expect(resume).toHaveBeenCalledTimes(1)
    await user.click(await screen.findByRole('button', { name: 'Active · 1' }))
    expect(screen.getByRole('link', { name: 'Open chat' })).toHaveAttribute('href', '/chat/owned-report')
    await act(async () => resolve({ alerts: [paused] }))
    expect(screen.queryByText('Paused')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Resume' })).not.toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Open chat' })).toBeInTheDocument()
  })
})

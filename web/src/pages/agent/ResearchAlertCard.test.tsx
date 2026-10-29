import { act, fireEvent, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, api } from '../../lib/api'
import type { Alert, PriceAlertProposal } from '../../lib/types'
import * as fx from '../../test/fixtures'
import { renderPage, stubSession } from '../../test/utils'
import { AgentCard } from './cards'

const researchCard = (over: Partial<PriceAlertProposal> = {}): PriceAlertProposal =>
  fx.scheduleCard({
    symbol: '',
    action: 'research',
    notional: null,
    note: 'Weekly portfolio review',
    prompt: 'Review my portfolio and the latest market catalysts.',
    notifyWhen: 'changed',
    ...over,
  })

const node = (label: string) => screen.getByText(label, { selector: '.label' }).parentElement!

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => { resolve = done })
  return { promise, resolve }
}

const show = (data: PriceAlertProposal = researchCard(), stored: Alert[] = []) => {
  vi.spyOn(api, 'alerts').mockResolvedValue({ alerts: stored })
  return renderPage(<AgentCard card={{ type: 'price_alert', data }} />)
}

beforeEach(() => {
  stubSession()
})

describe('research confirmation card', () => {
  it('shows the scheduled prompt, conditional delivery and recurring cost before confirmation', async () => {
    const create = vi.spyOn(api, 'createAlert').mockResolvedValue({ alert: fx.schedule({ action: 'research' }) })
    const { container } = show()
    expect(screen.getByText('Scheduled AI research')).toBeInTheDocument()
    expect(node('Schedule')).toHaveTextContent('Mondays · 09:00 UTC')
    expect(node('AI research')).toHaveTextContent('Review my portfolio and the latest market catalysts.')
    expect(node('Notify')).toHaveTextContent('First report, then changes to the verdict or report')
    expect(node('Notify')).toHaveTextContent('Bell · in-app')
    expect(screen.getByText(/2 AI credits per run\. Setting this automation is free\./)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Set automation' })).toBeEnabled()
    expect(container).not.toHaveTextContent('Paper research')
    expect(container).not.toHaveTextContent('No model call, 0 credits')
    expect(container).not.toHaveTextContent('Account')
    await waitFor(() => expect(api.alerts).toHaveBeenCalled())
    expect(create).not.toHaveBeenCalled()
  })

  it('confirms general research with its proposal UUID and no symbol or order fields', async () => {
    const create = vi.spyOn(api, 'createAlert').mockResolvedValue({ alert: fx.schedule({ action: 'research' }) })
    const data = researchCard({ notional: 999, stopLoss: 123, takeProfit: 321 })
    const { user, container } = show(data)
    expect(container).not.toHaveTextContent('$999')
    expect(container).not.toHaveTextContent('stop $123')
    await user.click(screen.getByRole('button', { name: 'Set automation' }))
    expect(await screen.findByText('Automation set')).toBeInTheDocument()
    expect(create).toHaveBeenCalledExactlyOnceWith({
      trigger: 'schedule', every: 'week', at: '09:00', weekday: 1,
      action: 'research', prompt: data.prompt, notifyWhen: 'changed', note: data.note,
      source: 'agent', proposalId: data.proposalId,
    })
    expect(screen.getByRole('link', { name: /Manage in Automations/ })).toHaveAttribute('href', '/automations')
    expect(screen.queryByRole('button', { name: 'Set automation' })).toBeNull()
  })

  it('displays and confirms the thesis statement, invalidation and symbol', async () => {
    const thesis = { statement: 'BTC demand will outpace new supply.', invalidation: 'Sustained ETF outflows for four weeks.' }
    const data = researchCard({ symbol: 'BTC', thesis, notifyWhen: 'invalidated' })
    const create = vi.spyOn(api, 'createAlert').mockResolvedValue({ alert: fx.schedule({ action: 'research' }) })
    const { user } = show(data)
    expect(screen.getByText('Thesis tracker · BTC')).toBeInTheDocument()
    expect(node('AI research')).toHaveTextContent(thesis.statement)
    expect(node('AI research')).toHaveTextContent(thesis.invalidation)
    expect(node('Notify')).toHaveTextContent('When the thesis becomes invalidated')
    expect(create).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'Set automation' }))
    expect(await screen.findByText('Automation set')).toBeInTheDocument()
    expect(create).toHaveBeenCalledWith(expect.objectContaining({ symbol: 'BTC', action: 'research', prompt: data.prompt, thesis, notifyWhen: 'invalidated', proposalId: data.proposalId }))
  })

  it('defaults to notification for every successful report', () => {
    show(researchCard({ notifyWhen: undefined }))
    expect(node('Notify')).toHaveTextContent('Every successful report')
  })

  it('keeps a repeated confirmation terminal when the proposal is already set', async () => {
    vi.spyOn(api, 'createAlert').mockRejectedValue(new ApiError(409, 'conflict', 'This alert was already set.'))
    const { user } = show()
    await user.click(screen.getByRole('button', { name: 'Set automation' }))
    expect(await screen.findByText('Automation set')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Set automation' })).toBeNull()
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('leaves a research cap refusal visible and permits a deliberate retry', async () => {
    const create = vi.spyOn(api, 'createAlert')
      .mockRejectedValueOnce(new ApiError(409, 'conflict', 'You have 3 active research schedules. Cancel one first.'))
      .mockResolvedValue({ alert: fx.schedule({ action: 'research' }) })
    const { user } = show()
    await user.click(screen.getByRole('button', { name: 'Set automation' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('You have 3 active research schedules. Cancel one first.')
    expect(screen.getByRole('button', { name: 'Set automation' })).toBeEnabled()
    expect(screen.queryByText('Automation set')).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Set automation' }))
    expect(await screen.findByText('Automation set')).toBeInTheDocument()
    expect(create).toHaveBeenCalledTimes(2)
  })

  it('shows a paused research schedule with a Manage link after reload', async () => {
    show(researchCard(), [fx.schedule({ proposalId: researchCard().proposalId, action: 'research', status: 'paused' })])
    expect(await screen.findByRole('status')).toHaveTextContent('Paused')
    expect(screen.getByRole('status')).not.toHaveTextContent('Cancelled')
    expect(screen.getByRole('link', { name: /Manage in Automations/ })).toHaveAttribute('href', '/automations')
    expect(screen.queryByRole('button', { name: 'Set automation' })).toBeNull()
  })

  it('ignores a late pre-confirmation GET result after local confirmation completes', async () => {
    const lookup = deferred<{ alerts: Alert[] }>()
    vi.spyOn(api, 'alerts').mockReturnValue(lookup.promise)
    vi.spyOn(api, 'createAlert').mockResolvedValue({ alert: fx.schedule({ action: 'research' }) })
    const { user } = renderPage(<AgentCard card={{ type: 'price_alert', data: researchCard() }} />)
    await user.click(screen.getByRole('button', { name: 'Set automation' }))
    expect(await screen.findByText('Automation set')).toBeInTheDocument()
    await act(async () => lookup.resolve({ alerts: [fx.schedule({ proposalId: researchCard().proposalId, status: 'cancelled' })] }))
    expect(screen.getByRole('status')).toHaveTextContent('Automation set')
    expect(screen.getByRole('status')).not.toHaveTextContent('Cancelled')
  })

  it('submits once when confirmation is invoked twice before the pending render', async () => {
    const created = deferred<{ alert: Alert }>()
    const create = vi.spyOn(api, 'createAlert').mockReturnValue(created.promise)
    show()
    const button = screen.getByRole('button', { name: 'Set automation' })
    act(() => {
      fireEvent.click(button)
      fireEvent.click(button)
    })
    expect(button).toBeDisabled()
    await act(async () => created.resolve({ alert: fx.schedule({ action: 'research' }) }))
    expect(screen.getByRole('status')).toHaveTextContent('Automation set')
    expect(create).toHaveBeenCalledTimes(1)
  })
})

import { act, screen, waitFor, within } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { api } from './api'
import { useAuth } from './auth'
import { AccountPage } from '../pages/account/AccountPage'
import { Copilot } from '../pages/trade/Copilot'
import { applyEvent, fromMessage, isCard, pendingAssistant } from '../pages/agent/turns'
import * as fx from '../test/fixtures'
import { renderPage, stubSession } from '../test/utils'

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => { resolve = done })
  return { promise, resolve }
}

const suppliedProfile = () => ({ enabled: true, riskProfile: null, style: 'swing' as const, focusSymbols: ['BTC', 'NVDA'], customPrompt: 'Avoid leverage.', memories: [], revision: 4, updatedAt: '2026-10-05T12:00:00Z' })
const proposal = { type: 'memory', data: { proposalId: 'cc09c736-a775-4871-8358-a0c7eeb44fba', text: 'Prefer liquid assets.', status: 'open' } }

describe('selected Profile WEB foundation behavior', () => {
  it('exposes server personalization and retained instructions on the real Account page', async () => {
    stubSession()
    vi.mocked(api.me).mockResolvedValue({ user: { ...fx.user, profile: suppliedProfile() }, credits: fx.credits })
    vi.spyOn(api, 'copyPlans').mockResolvedValue({ plans: [] })
    vi.spyOn(api, 'trades').mockResolvedValue({ trades: [] })
    vi.spyOn(api, 'portfolioEquity').mockResolvedValue(fx.equityCurve())
    vi.spyOn(api, 'portfolioAttribution').mockResolvedValue({ sources: [], totalPnl: 0 })
    vi.spyOn(api, 'portfolioReview').mockResolvedValue(fx.review())
    renderPage(<AccountPage />, { route: '/account', path: '/account' })
    await screen.findByText('$100,194.00', { selector: 'div' })
    expect(screen.getByRole('textbox', { name: 'Custom instructions' })).toHaveValue('Avoid leverage.')
    expect(screen.getByRole('combobox', { name: 'Risk profile' })).toHaveValue('')
    expect(screen.getByRole('combobox', { name: 'Preferred trading style' })).toHaveValue('swing')
    expect(screen.getByRole('button', { name: 'Save preferences' })).toBeDisabled()
  })

  it('uses the enabled server trading style for a Copilot request', async () => {
    stubSession()
    vi.mocked(api.me).mockResolvedValue({ user: { ...fx.user, profile: suppliedProfile() }, credits: fx.credits })
    const call = vi.spyOn(api, 'copilotPlan').mockResolvedValue({ plan: fx.plan({ style: 'swing' }), analysis: fx.analysis, outlook: fx.outlook, headlines: [], credits: fx.credits })
    const { user } = renderPage(<Copilot symbol="BTC" onPlan={vi.fn()} onUse={vi.fn()} />)
    // Wait for the profile's style to reach the panel, not only for the request: a click before it lands keeps "day".
    await waitFor(() => expect(within(screen.getByRole('group', { name: 'Trading style' })).getByRole('button', { name: 'Swing' })).toHaveAttribute('aria-pressed', 'true'))
    await user.click(screen.getByRole('button', { name: /Ask Long/ }))
    expect(call).toHaveBeenCalledWith({ symbol: 'BTC', style: 'swing', bias: 'long' })
  })

  it('retains memory cards and only records durable assistant provenance from real done or stored messages', () => {
    expect(isCard(proposal)).toBe(true)
    const live = applyEvent(pendingAssistant(), { event: 'card', data: proposal as never })
    expect(live.cards).toEqual([proposal])
    expect(live).not.toHaveProperty('persistedMessageId')
    const done = applyEvent(live, { event: 'done', data: { messageId: '5e8712e2-19d8-4ef1-a389-e84b0e753db9', credits: fx.credits } })
    expect(done).toHaveProperty('persistedMessageId', '5e8712e2-19d8-4ef1-a389-e84b0e753db9')
    expect(fromMessage({ id: 'saved', role: 'assistant', content: '', cards: [proposal as never], attachments: [], createdAt: '' })).toHaveProperty('persistedMessageId', 'saved')
    expect(fromMessage({ id: 'user', role: 'user', content: '', cards: [proposal as never], attachments: [], createdAt: '' })).not.toHaveProperty('persistedMessageId')
  })

  it('keeps a new login owner when an older initial ME response arrives late', async () => {
    stubSession()
    const old = deferred<Awaited<ReturnType<typeof api.me>>>()
    const bob = { ...fx.user, id: 'bob', name: 'Bob', profile: suppliedProfile() }
    vi.mocked(api.me).mockReturnValueOnce(old.promise).mockResolvedValue({ user: bob, credits: fx.credits })
    vi.spyOn(api, 'login').mockResolvedValue({ user: bob })
    function Probe() {
      const auth = useAuth()
      return <><output aria-label="Owner">{auth.user?.id}</output><button onClick={() => void auth.login('bob@example.test', 'pass')}>Login Bob</button></>
    }
    const { user } = renderPage(<Probe />)
    await user.click(screen.getByRole('button', { name: 'Login Bob' }))
    await waitFor(() => expect(screen.getByLabelText('Owner')).toHaveTextContent('bob'))
    await act(async () => old.resolve({ user: fx.user, credits: fx.credits }))
    expect(screen.getByLabelText('Owner')).toHaveTextContent('bob')
  })
})

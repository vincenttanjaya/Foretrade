import { act, screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { api } from '../../lib/api'
import { useAuth } from '../../lib/auth'
import * as fx from '../../test/fixtures'
import { renderPage, stubSession } from '../../test/utils'
import { Copilot } from './Copilot'
function pending<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>((done) => { resolve = done }); return { promise, resolve } }
function Host({ onPlan = vi.fn() }: { onPlan?: Parameters<typeof Copilot>[0]['onPlan'] }) {
  const { refreshProfile } = useAuth()
  return <><Copilot symbol="BTC" onPlan={onPlan} onUse={vi.fn()} /><button onClick={() => void refreshProfile()}>Reload profile</button></>
}

describe('Copilot profile ordering', () => {
  it('allows a late enabled default only before any user choice', async () => {
    stubSession()
    vi.spyOn(api, 'profile').mockResolvedValue({ profile: fx.profile({ enabled: true, style: 'swing', revision: 1 }) })
    const { user } = renderPage(<Host />)
    await user.click(screen.getByRole('button', { name: 'Reload profile' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Swing' })).toHaveAttribute('aria-pressed', 'true'))
  })

  it('keeps an explicit tab when a previously started profile read arrives late', async () => {
    stubSession()
    const read = pending<Awaited<ReturnType<typeof api.profile>>>()
    vi.spyOn(api, 'profile').mockReturnValue(read.promise)
    const { user } = renderPage(<Host />)
    await user.click(screen.getByRole('button', { name: 'Reload profile' }))
    await user.click(screen.getByRole('button', { name: 'Scalp' }))
    await act(async () => read.resolve({ profile: fx.profile({ enabled: true, style: 'swing', revision: 1 }) }))
    expect(screen.getByRole('button', { name: 'Scalp' })).toHaveAttribute('aria-pressed', 'true')
  })

  it('captures request style and interval before late profile arrival, preserving suggested size', async () => {
    stubSession()
    const read = pending<Awaited<ReturnType<typeof api.profile>>>()
    const plan = pending<Awaited<ReturnType<typeof api.copilotPlan>>>()
    vi.spyOn(api, 'profile').mockReturnValue(read.promise)
    const call = vi.spyOn(api, 'copilotPlan').mockReturnValue(plan.promise)
    const onPlan = vi.fn()
    const { user } = renderPage(<Host onPlan={onPlan} />)
    await user.click(screen.getByRole('button', { name: 'Reload profile' }))
    await user.click(screen.getByRole('button', { name: /Ask Long/ }))
    await act(async () => read.resolve({ profile: fx.profile({ enabled: true, style: 'swing', riskProfile: 'aggressive', revision: 1 }) }))
    expect(call).toHaveBeenCalledWith({ symbol: 'BTC', style: 'day', bias: 'long' })
    expect(screen.getByRole('button', { name: 'Day' })).toHaveAttribute('aria-pressed', 'true')
    const response = { plan: fx.plan({ suggestedNotional: 5000 }), analysis: fx.analysis, outlook: fx.outlook, headlines: [], credits: fx.credits }
    await act(async () => plan.resolve(response))
    expect(onPlan).toHaveBeenCalledWith(response.plan, '1h')
    expect(screen.getByRole('button', { name: 'Use this plan' })).toBeEnabled()
  })

  it('keeps try Swing as an explicit choice and ignores the retained OFF style', async () => {
    stubSession()
    vi.mocked(api.me).mockResolvedValue({ user: { ...fx.user, profile: fx.profile({ style: 'scalp' }) }, credits: fx.credits })
    vi.spyOn(api, 'copilotPlan').mockResolvedValue({ plan: fx.plan({ direction: 'neutral', confidence: 30 }), analysis: fx.analysis, outlook: fx.outlook, headlines: [], credits: fx.credits })
    vi.spyOn(api, 'profile').mockResolvedValue({ profile: fx.profile({ enabled: true, style: 'scalp', revision: 1 }) })
    const { user } = renderPage(<Host />)
    await user.click(screen.getByRole('button', { name: /Ask Long/ }))
    await user.click(await screen.findByRole('button', { name: 'try Swing' }))
    await user.click(screen.getByRole('button', { name: 'Reload profile' }))
    expect(screen.getByRole('button', { name: 'Swing' })).toHaveAttribute('aria-pressed', 'true')
  })
})

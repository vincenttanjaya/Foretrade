import { act, screen, waitFor, within } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { api, ApiError } from '../../lib/api'
import { useAuth } from '../../lib/auth'
import type { Profile } from '../../lib/types'
import * as fx from '../../test/fixtures'
import { renderPage, stubSession } from '../../test/utils'
import { Personalization } from './Personalization'

const memory = { id: 'c2d75208-a9a8-447e-a6c7-5f4fa161b6c0', text: 'Prefer liquid assets.', at: '2026-10-06T00:00:00Z' }
function open(profile = fx.profile()) {
  stubSession()
  vi.mocked(api.me).mockResolvedValue({ user: { ...fx.user, profile }, credits: fx.credits })
  return renderPage(<Personalization />)
}
const prompt = () => screen.getByRole('textbox', { name: 'Custom instructions' })
const save = () => screen.getByRole('button', { name: 'Save preferences' })

describe('Account personalization', () => {
  it('saves all and only explicit preference fields with the server revision, including retained OFF data', async () => {
    const profile = fx.profile({ enabled: true, memories: [memory], revision: 7 })
    const request = vi.spyOn(api, 'updateProfile').mockResolvedValue({ profile: { ...profile, enabled: false, riskProfile: 'aggressive', style: 'scalp', focusSymbols: ['NVDA'], customPrompt: 'Less noise.', revision: 8 } })
    const { user } = open(profile)
    await user.click(await screen.findByRole('checkbox', { name: 'Personalize AI responses' }))
    await user.selectOptions(screen.getByRole('combobox', { name: 'Risk profile' }), 'aggressive')
    await user.selectOptions(screen.getByRole('combobox', { name: 'Preferred trading style' }), 'scalp')
    await user.click(screen.getByRole('checkbox', { name: 'Focus NVDA' }))
    await user.type(prompt(), 'Less noise.')
    expect(request).not.toHaveBeenCalled()
    await user.click(save())
    expect(request).toHaveBeenCalledWith({ enabled: false, riskProfile: 'aggressive', style: 'scalp', focusSymbols: ['NVDA'], customPrompt: 'Less noise.', revision: 7 }, expect.any(AbortSignal))
    expect(await screen.findByText('Preferences saved.')).toBeInTheDocument()
    expect(screen.getByRole('region', { name: 'Saved memories' })).toHaveTextContent(memory.text)
    expect(prompt()).toBeEnabled()
    expect(save()).toBeDisabled()
    expect(localStorage.length).toBe(0)
  })

  it('provides all 245 configured symbols (quote feeds included), filters by name, and caps ten while allowing deselection', async () => {
    const { user } = open()
    await screen.findByRole('textbox', { name: 'Custom instructions' })
    // By label throughout: a role query walks the accessibility tree of all 245 boxes each time, which is slow under load.
    const boxes = () => screen.getAllByLabelText(/^Focus /)
    expect(boxes()).toHaveLength(107 + 138)
    expect(boxes().every((b) => b.getAttribute('type') === 'checkbox')).toBe(true)
    const ten = ['BTC', 'ETH', 'SOL', 'BNB', 'XRP', 'ADA', 'DOGE', 'AVAX', 'LINK', 'PAXG']
    for (const symbol of ten) await user.click(screen.getByLabelText(`Focus ${symbol}`))
    expect(screen.getByLabelText('Focus NVDA')).toBeDisabled()
    expect(screen.getByLabelText('Focus BTC')).toBeEnabled()
    await user.click(screen.getByLabelText('Focus BTC'))
    expect(screen.getByLabelText('Focus NVDA')).toBeEnabled()
    await user.click(screen.getByRole('textbox', { name: 'Search focus symbols' }))
    await user.paste('NVIDIA') // one input event: typing re-filters all 245 rows per key
    expect(boxes()).toHaveLength(1)
    expect(screen.getByLabelText('Focus NVDA')).toBeInTheDocument()
    expect(screen.getByText(/Selected:/)).toHaveTextContent('ETH')
  }, 30_000) // ten clicks and a filter re-render 245 rows: 15-16.5 s under coverage on a loaded machine

  it('counts Unicode codepoints: accepts 600 emoji, blocks 601, and discards only on explicit click', async () => {
    const { user } = open(fx.profile({ customPrompt: '🧭'.repeat(600) }))
    await screen.findByText('600/600 characters')
    await user.type(prompt(), '🧭')
    expect(screen.getByText('601/600 characters')).toBeInTheDocument()
    expect(prompt()).toHaveAttribute('aria-invalid', 'true')
    expect(save()).toBeDisabled()
    await user.click(screen.getByRole('button', { name: 'Discard changes' }))
    expect(prompt()).toHaveValue('🧭'.repeat(600))
  })

  it('preserves dirty preferences while deletion changes memories and the revision', async () => {
    const profile = fx.profile({ memories: [memory], revision: 3 })
    const deletion = vi.spyOn(api, 'deleteMemory').mockResolvedValue({ profile: { ...profile, memories: [], revision: 4 } })
    const update = vi.spyOn(api, 'updateProfile').mockResolvedValue({ profile: { ...profile, memories: [], revision: 5, customPrompt: 'My draft.' } })
    const { user } = open(profile)
    await screen.findByRole('textbox', { name: 'Custom instructions' })
    await user.type(prompt(), 'My draft.')
    await user.click(screen.getByRole('button', { name: `Delete memory: ${memory.text}` }))
    expect(deletion).toHaveBeenCalledWith(memory.id, expect.any(AbortSignal))
    await screen.findByText('No saved memories.')
    expect(prompt()).toHaveValue('My draft.')
    await user.click(save())
    expect(update).toHaveBeenCalledWith(expect.objectContaining({ customPrompt: 'My draft.', revision: 4 }), expect.any(AbortSignal))
  })

  it('refetches a stale 409 without retry or overwriting the draft, then explicitly saves the latest revision', async () => {
    const refreshed = fx.profile({ revision: 8, customPrompt: 'Other tab.', memories: [memory] })
    const request = vi.spyOn(api, 'updateProfile').mockRejectedValueOnce(new ApiError(409, 'conflict', 'Stale.')).mockResolvedValue({ profile: { ...refreshed, customPrompt: 'My draft.', revision: 9 } })
    vi.spyOn(api, 'profile').mockResolvedValue({ profile: refreshed })
    const { user } = open(fx.profile({ revision: 7 }))
    await screen.findByRole('textbox', { name: 'Custom instructions' })
    await user.type(prompt(), 'My draft.')
    await user.click(save())
    expect(await screen.findByRole('alert')).toHaveTextContent('Your edits are kept.')
    await waitFor(() => expect(save()).toBeEnabled())
    expect(request).toHaveBeenCalledTimes(1)
    expect(prompt()).toHaveValue('My draft.')
    expect(screen.getByRole('region', { name: 'Saved memories' })).toHaveTextContent(memory.text)
    await user.click(save())
    expect(request).toHaveBeenLastCalledWith(expect.objectContaining({ revision: 8, customPrompt: 'My draft.' }), expect.any(AbortSignal))
  })

  it('keeps newer unsaved typing when a previous Save completes', async () => {
    let settle!: (value: { profile: Profile }) => void
    vi.spyOn(api, 'updateProfile').mockImplementation(() => new Promise((done) => { settle = done }))
    const { user } = open()
    await screen.findByRole('textbox', { name: 'Custom instructions' })
    await user.type(prompt(), 'First.')
    await user.click(save())
    expect(save()).toBeDisabled()
    await user.type(prompt(), ' Later.')
    await act(async () => settle({ profile: fx.profile({ revision: 1, customPrompt: 'First.' }) }))
    expect(prompt()).toHaveValue('First. Later.')
    expect(save()).toBeEnabled()
  })

  it('keeps the memory and draft on failure and retries only the explicit deletion', async () => {
    const deletion = vi.spyOn(api, 'deleteMemory').mockRejectedValueOnce(new ApiError(0, 'network', 'Offline.')).mockResolvedValue({ profile: fx.profile({ revision: 2 }) })
    const { user } = open(fx.profile({ revision: 1, memories: [memory] }))
    const remove = await screen.findByRole('button', { name: `Delete memory: ${memory.text}` })
    await user.click(remove)
    expect(await screen.findByRole('alert')).toHaveTextContent('Offline.')
    expect(screen.getByRole('region', { name: 'Saved memories' })).toHaveTextContent(memory.text)
    await user.click(remove)
    await screen.findByText('No saved memories.')
    expect(deletion).toHaveBeenCalledTimes(2)
  })

  it('clears a private dirty draft on owner change', async () => {
    stubSession()
    vi.spyOn(api, 'login').mockResolvedValue({ user: { ...fx.user, id: 'bob', profile: fx.profile() } })
    function Switch() { const auth = useAuth(); return <><Personalization /><button onClick={() => void auth.login('bob@example.test', 'pw')}>Switch owner</button></> }
    const { user } = renderPage(<Switch />)
    await screen.findByRole('textbox', { name: 'Custom instructions' })
    await user.type(prompt(), 'Private Alice.')
    vi.mocked(api.me).mockResolvedValue({ user: { ...fx.user, id: 'bob', profile: fx.profile() }, credits: fx.credits })
    await user.click(screen.getByRole('button', { name: 'Switch owner' }))
    await waitFor(() => expect(prompt()).toHaveValue(''))
    expect(within(screen.getByRole('region', { name: 'Saved memories' })).getByText('No saved memories.')).toBeInTheDocument()
    expect(localStorage.length).toBe(0)
  })
})

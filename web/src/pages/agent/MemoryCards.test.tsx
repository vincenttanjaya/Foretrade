import { act, screen, waitFor } from '@testing-library/react'
import { useState } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { api, ApiError } from '../../lib/api'
import * as fx from '../../test/fixtures'
import { renderPage, stubSession } from '../../test/utils'
import type { Card, Profile } from '../../lib/types'
import { TurnView } from './Conversation'
import { Personalization } from '../account/Personalization'
import { applyEvent, fromMessage, pendingAssistant } from './turns'

const data = { proposalId: 'cc09c736-a775-4871-8358-a0c7eeb44fba', text: 'Prefer liquid assets.', status: 'open' as const }
const card: Card = { type: 'memory', data }
const memory = { id: 'c2d75208-a9a8-447e-a6c7-5f4fa161b6c0', text: data.text, at: '2026-10-06T00:00:00Z' }
const saved = () => fromMessage({ id: 'saved-assistant', role: 'assistant', content: 'A suggestion.', cards: [card], attachments: [], createdAt: '' })
function session(profile: Profile = fx.profile()) {
  stubSession()
  vi.mocked(api.me).mockResolvedValue({ user: { ...fx.user, profile }, credits: fx.credits })
  vi.spyOn(api, 'profile').mockResolvedValue({ profile })
}

describe('owned durable memory cards', () => {
  it('renders a historical proposal and saves only on an explicit Confirm', async () => {
    session()
    const call = vi.spyOn(api, 'confirmMemory').mockResolvedValue({ memory, profile: fx.profile({ revision: 1, memories: [memory] }) })
    const { user } = renderPage(<TurnView turn={saved()} />, { route: '/chat/c1', path: '/chat/:chatId' })
    const confirm = await screen.findByRole('button', { name: 'Confirm memory' })
    expect(confirm).toBeEnabled()
    expect(call).not.toHaveBeenCalled()
    await user.click(confirm)
    expect(call).toHaveBeenCalledTimes(1)
    expect(call).toHaveBeenCalledWith({ proposalId: data.proposalId, text: data.text }, expect.any(AbortSignal))
    expect(await screen.findByText('Memory saved')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Confirm memory' })).toBeNull()
  })

  it('disables current-turn Confirm until the real durable done event', async () => {
    session()
    const call = vi.spyOn(api, 'confirmMemory')
    let turn = applyEvent(pendingAssistant(), { event: 'delta', data: { text: 'Suggested.' } })
    turn = applyEvent(turn, { event: 'card', data: card })
    const realDone = applyEvent(turn, { event: 'done', data: { messageId: 'saved-message', credits: fx.credits } })
    let finish!: () => void
    function ChangingTurn() {
      const [current, setCurrent] = useState(turn)
      finish = () => setCurrent(realDone)
      return <TurnView turn={current} />
    }
    const view = renderPage(<ChangingTurn />)
    expect(await screen.findByRole('button', { name: 'Confirm memory' })).toBeDisabled()
    await view.user.click(screen.getByRole('button', { name: 'Confirm memory' }))
    expect(call).not.toHaveBeenCalled()
    act(() => finish())
    expect(screen.getByRole('button', { name: 'Confirm memory' })).toBeEnabled()
  })

  it.each(['error', 'stopped', 'pending', 'done'] as const)('does not treat synthetic %s as persistence', async (status) => {
    session()
    renderPage(<TurnView turn={{ ...pendingAssistant(), status, content: 'A suggestion.', cards: [card] }} />)
    expect(await screen.findByRole('button', { name: 'Confirm memory' })).toBeDisabled()
  })

  it('reconciles a lost response through owned chat hydration without resubmitting', async () => {
    session()
    const call = vi.spyOn(api, 'confirmMemory').mockRejectedValue(new ApiError(0, 'network', 'Connection lost.'))
    vi.mocked(api.profile).mockResolvedValue({ profile: fx.profile({ revision: 1, memories: [memory] }) })
    const history = saved()
    history.cards = [{ type: 'memory', data: { ...data, status: 'confirmed', memoryId: memory.id } }]
    vi.spyOn(api, 'chat').mockResolvedValue({ chat: { id: 'c1', title: 'Memory', messages: [{ id: 'saved-assistant', role: 'assistant', content: '', cards: history.cards, attachments: [], createdAt: '' }] } })
    const { user } = renderPage(<TurnView turn={saved()} />, { route: '/chat/c1', path: '/chat/:chatId' })
    await user.click(await screen.findByRole('button', { name: 'Confirm memory' }))
    expect(await screen.findByText('Memory saved')).toBeInTheDocument()
    expect(call).toHaveBeenCalledTimes(1)
    expect(api.chat).toHaveBeenCalledWith('c1', expect.any(AbortSignal))
  })

  it('keeps a durably deleted proposal deleted on a replay conflict', async () => {
    session(fx.profile({ revision: 2 }))
    const call = vi.spyOn(api, 'confirmMemory').mockRejectedValue(new ApiError(409, 'conflict', 'Already confirmed.'))
    vi.spyOn(api, 'chat').mockResolvedValue({ chat: { id: 'c1', title: '', messages: [{ id: 'saved-assistant', role: 'assistant', content: '', cards: [{ type: 'memory', data: { ...data, status: 'deleted', memoryId: memory.id } }], attachments: [], createdAt: '' }] } })
    const { user } = renderPage(<TurnView turn={saved()} />, { route: '/chat/c1', path: '/chat/:chatId' })
    await user.click(await screen.findByRole('button', { name: 'Confirm memory' }))
    expect(await screen.findByText('Memory deleted')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Confirm memory' })).toBeNull()
    expect(call).toHaveBeenCalledTimes(1)
  })

  it('keeps an open cap conflict retryable after authoritative reads', async () => {
    session()
    vi.spyOn(api, 'confirmMemory').mockRejectedValue(new ApiError(409, 'conflict', 'Memory limit reached.'))
    vi.spyOn(api, 'chat').mockResolvedValue({ chat: { id: 'c1', title: '', messages: [{ id: 'saved-assistant', role: 'assistant', content: '', cards: [card], attachments: [], createdAt: '' }] } })
    const { user } = renderPage(<TurnView turn={saved()} />, { route: '/chat/c1', path: '/chat/:chatId' })
    await user.click(await screen.findByRole('button', { name: 'Confirm memory' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Memory limit reached.')
    await waitFor(() => expect(screen.getByRole('button', { name: 'Confirm memory' })).toBeEnabled())
  })

  it('lets a refreshed confirmed card supersede an older open hydration cache', async () => {
    session()
    vi.spyOn(api, 'confirmMemory').mockRejectedValue(new ApiError(409, 'conflict', 'Memory limit reached.'))
    vi.spyOn(api, 'chat').mockResolvedValue({ chat: { id: 'c1', title: '', messages: [{ id: 'saved-assistant', role: 'assistant', content: '', cards: [card], attachments: [], createdAt: '' }] } })
    let reload!: () => void
    function Host() {
      const [turn, setTurn] = useState(saved())
      reload = () => setTurn({ ...saved(), cards: [{ type: 'memory', data: { ...data, status: 'confirmed', memoryId: memory.id } }] })
      return <TurnView turn={turn} />
    }
    const { user } = renderPage(<Host />, { route: '/chat/c1', path: '/chat/:chatId' })
    await user.click(await screen.findByRole('button', { name: 'Confirm memory' }))
    await screen.findByRole('alert')
    act(() => reload())
    expect(screen.getByText('Memory saved', { exact: true })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Confirm memory' })).toBeNull()
  })

  it('updates an already confirmed historical card when its memory is deleted in the editor', async () => {
    session(fx.profile({ revision: 1, memories: [memory] }))
    vi.spyOn(api, 'deleteMemory').mockResolvedValue({ profile: fx.profile({ revision: 2 }) })
    const turn = { ...saved(), cards: [{ type: 'memory' as const, data: { ...data, status: 'confirmed' as const, memoryId: memory.id } }] }
    const { user } = renderPage(<><TurnView turn={turn} /><Personalization /></>)
    expect(await screen.findByText('Memory saved', { exact: true })).toBeInTheDocument()
    await user.click(await screen.findByRole('button', { name: `Delete memory: ${memory.text}` }))
    await screen.findByText('No saved memories.')
    expect(screen.getByText('Memory deleted', { exact: true })).toBeInTheDocument()
  })
})

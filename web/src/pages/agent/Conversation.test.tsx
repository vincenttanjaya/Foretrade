import { screen, within } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { Card } from '../../lib/types'
import * as fx from '../../test/fixtures'
import { renderPage, stubSession } from '../../test/utils'
import { TurnView } from './Conversation'
import { pendingAssistant, type Turn } from './turns'

const turn = (over: Partial<Turn>): Turn => ({ ...pendingAssistant(), ...over })
const view = (t: Turn, onFollowup?: (p: string) => void) =>
  renderPage(
    <ol>
      <TurnView turn={t} onFollowup={onFollowup} />
    </ol>,
  )

beforeEach(() => {
  stubSession()
})

describe('TurnView', () => {
  it('holds cards that arrive before any text, so the answer cannot push them down', () => {
    const { unmount } = view(turn({ cards: [fx.forecastCard] }))
    expect(screen.queryByText('Forecast desk')).toBeNull()
    expect(screen.getByRole('status')).toHaveTextContent('Thinking')
    expect(screen.queryByText('Simulated · not financial advice')).toBeNull()
    unmount()
    view(turn({ content: 'The desk says', cards: [fx.forecastCard] })) // the first text arrived
    expect(screen.getByText('Forecast desk')).toBeInTheDocument()
  })

  it('shows held cards when the turn ends without text', () => {
    view(turn({ status: 'done', cards: [fx.forecastCard] }))
    expect(screen.getByText('Forecast desk')).toBeInTheDocument()
  })

  it('renders follow-up chips only for the latest finished turn', async () => {
    const onFollowup = vi.fn()
    const done = turn({ status: 'done', content: 'Done. **View:** bullish.', cards: [fx.followupsCard] })
    const { unmount, user } = view(done, onFollowup)
    const chips = screen.getByRole('list', { name: 'Suggested follow-ups' })
    expect(within(chips).getAllByRole('listitem')).toHaveLength(3)
    await user.click(within(chips).getByRole('button', { name: 'Compare with ETH' }))
    expect(onFollowup).toHaveBeenCalledWith('Compare with ETH')
    unmount()

    view(done) // an earlier turn: no handler
    expect(screen.queryByRole('list', { name: 'Suggested follow-ups' })).toBeNull()
  })

  it('sums a finished live turn up in the Show work header', () => {
    const tools = Array.from({ length: 5 }, (_, i) => ({ id: `t${i}`, label: `Step ${i}`, ok: true, summary: 'ok' }))
    const items = Array.from({ length: 13 }, (_, i) => ({ n: i + 1, title: `S${i}`, url: `https://s${i}.example/`, source: 's.example', publishedAt: null }))
    const sources: Card = { type: 'sources', data: { items } }
    const { unmount } = view(turn({ status: 'done', content: 'Done.', tools, cards: [sources], elapsedMs: 22_400 }))
    expect(screen.getByRole('list', { name: 'Agent activity' })).toHaveTextContent('Done in 22 s · 5 tools · 13 sources')
    unmount()

    const { unmount: again } = view(turn({ status: 'done', content: 'Done.', tools: tools.slice(0, 1), elapsedMs: 72_000 }))
    expect(screen.getByText('Done in 1 min 12 s · 1 tool')).toBeInTheDocument() // no sources: omitted
    again()

    view(turn({ content: 'Writing', tools, elapsedMs: undefined })) // still streaming: no summary yet
    expect(screen.queryByText(/^Done in/)).toBeNull()
  })

  it('shows no chips while the turn streams', () => {
    view(turn({ content: 'Writing', cards: [fx.followupsCard] }), vi.fn())
    expect(screen.queryByRole('list', { name: 'Suggested follow-ups' })).toBeNull()
  })
})

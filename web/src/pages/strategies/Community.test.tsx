import { screen, waitFor, within } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError } from '../../lib/api'
import * as fx from '../../test/fixtures'
import { renderPage, stubSession } from '../../test/utils'
import { byline, CommunityBoard, usage } from './Community'

const m0 = fx.strategySummary().metrics
const board = [
  fx.strategySummary({ id: 'c1', name: 'ETH breakout', author: 'Ann Lee', symbol: 'ETH', interval: '1h', community: fx.publishedBy() }),
  fx.strategySummary({ id: 'c2', name: 'My SOL trend', metrics: { ...m0, totalReturnPct: 5, buyHoldReturnPct: 9 }, community: fx.community({ published: true, stars: 1, copies: 1 }) }),
]
const open = () => renderPage(<CommunityBoard />, { route: '/strategies?tab=top&top=community', path: '/strategies' })
const row = (name: string) => screen.getByRole('link', { name }).closest('li')!

beforeEach(() => {
  stubSession()
  vi.spyOn(api, 'strategies').mockResolvedValue({ strategies: board })
})

describe('Community board', () => {
  it('lists published strategies with their owner, market, numbers, stars and usage', async () => {
    open()
    expect(await screen.findByRole('link', { name: 'ETH breakout' })).toHaveAttribute('href', '/strategies/c1')
    expect(api.strategies).toHaveBeenCalledWith('community', expect.any(AbortSignal))
    const theirs = row('ETH breakout')
    expect(theirs).toHaveTextContent('by Ann Lee')
    expect(theirs).toHaveTextContent('ETH · 1h')
    expect(theirs).toHaveTextContent('Sharpe1.42')
    expect(theirs).toHaveTextContent('+12.4 pts vs buy & hold') // 24.5 vs 12.1
    expect(theirs).toHaveTextContent('2 copies · 1 deploy')
    expect(within(theirs).getByRole('button', { name: 'Star · 4 stars' })).toHaveAttribute('aria-pressed', 'false')
    const mine = row('My SOL trend')
    expect(mine).toHaveTextContent('by You')
    expect(mine).toHaveTextContent('−4.0 pts vs buy & hold')
    expect(mine).toHaveTextContent('1 copy · 0 deploys')
  })

  it('says how to get listed when nothing is published', async () => {
    vi.mocked(api.strategies).mockResolvedValue({ strategies: [] })
    open()
    expect(await screen.findByText('No community strategies yet. Publish one from your studio to list it here.')).toBeInTheDocument()
  })

  it('stars optimistically and keeps the server count', async () => {
    let answer!: (v: { stars: number; starred: boolean }) => void
    const star = vi.spyOn(api, 'starStrategy').mockReturnValue(new Promise((r) => (answer = r)))
    const { user } = open()
    await user.click(await screen.findByRole('button', { name: 'Star · 4 stars' }))
    const btn = within(row('ETH breakout')).getByRole('button', { pressed: true })
    expect(btn).toHaveTextContent('★ 5') // before the API answers
    expect(btn).toHaveAccessibleName('Unstar · 5 stars')
    expect(star).toHaveBeenCalledWith('c1', true)
    answer({ stars: 6, starred: true }) // someone else starred it meanwhile
    await waitFor(() => expect(btn).toHaveTextContent('★ 6'))
  })

  it('reverts a refused star and says why', async () => {
    vi.spyOn(api, 'starStrategy').mockRejectedValue(new ApiError(404, 'not_found', 'Not found.'))
    const { user } = open()
    await user.click(await screen.findByRole('button', { name: 'Star · 4 stars' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Not found.')
    expect(within(row('ETH breakout')).getByRole('button', { name: 'Star · 4 stars' })).toHaveAttribute('aria-pressed', 'false')
  })

  it('retries a failed load', async () => {
    vi.mocked(api.strategies).mockRejectedValueOnce(new ApiError(500, 'internal', 'Database is down'))
    const { user } = open()
    expect(await screen.findByRole('heading', { name: "Couldn't load the community board." })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Try again' }))
    expect(await screen.findByRole('link', { name: 'ETH breakout' })).toBeInTheDocument()
  })
})

describe('community helpers', () => {
  it('words owners and usage', () => {
    expect(byline({ owner: 'Ann', mine: false })).toBe('by Ann')
    expect(byline({ owner: 'Ann', mine: true })).toBe('by You')
    expect(usage({ copies: 0, deploys: 1 })).toBe('0 copies · 1 deploy')
    expect(usage({ copies: 1, deploys: 2 })).toBe('1 copy · 2 deploys')
  })
})

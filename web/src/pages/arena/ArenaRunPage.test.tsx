import { act, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError } from '../../lib/api'
import type { ArenaRun, ArenaStep } from '../../lib/types'
import { charts } from '../../test/chart'
import * as fx from '../../test/fixtures'
import { creditsUsed, currentPath, renderPage, stubSession } from '../../test/utils'
import { ArenaRunPage } from './ArenaRunPage'

const open = (id = 'r1') =>
  renderPage(<ArenaRunPage />, { route: `/strategies/arena/${id}`, path: '/strategies/arena/:id', routes: { '/strategies': <p>arena tab</p> } })

// A fake server: the run as it is now, and each agent's steps up to its stepsDone.
let current: ArenaRun
let stepOf: (agent: number, k: number) => ArenaStep
const serve = (run: ArenaRun) => (current = run)

const decisions = (agent = 'Trend Rider') => [...screen.getByRole('list', { name: `${agent} decisions` }).children] as HTMLElement[]
const rowOf = (name: string) => screen.getByText(name, { selector: 'td span' }).closest('tr')!

beforeEach(() => {
  charts.length = 0
  stubSession()
  stepOf = (_agent, k) => fx.arenaStep(k)
  serve(fx.arenaRun())
  vi.spyOn(api, 'arenaRun').mockImplementation(async () => ({ run: current }))
  vi.spyOn(api, 'arenaSteps').mockImplementation(async (_id, agent, after) => ({
    steps: Array.from({ length: Math.max(0, current.stepsDone - after - 1) }, (_, i) => stepOf(agent, after + 1 + i)),
  }))
})
afterEach(() => vi.useRealTimers())

describe('ArenaRunPage · live run', () => {
  it('polls every 3 s, appends new steps with after = last step, and stops when the run completes', async () => {
    vi.useFakeTimers()
    open()
    await act(() => vi.advanceTimersByTimeAsync(0))
    expect(screen.getByRole('heading', { level: 1, name: 'Daily · Aug 3 → Oct 1, 2026' })).toBeInTheDocument()
    expect(screen.getByText('Step 2 / 60').parentElement).toHaveTextContent('Step 2 / 60 · sim 2026-08-04')
    expect(screen.getByRole('progressbar', { name: 'Steps finished' })).toHaveAttribute('aria-valuenow', '2')
    expect(api.arenaSteps).toHaveBeenLastCalledWith('r1', 0, -1)
    expect(decisions()).toHaveLength(2)

    serve(fx.arenaRun({ stepsDone: 4 }))
    await act(() => vi.advanceTimersByTimeAsync(3000))
    expect(api.arenaSteps).toHaveBeenLastCalledWith('r1', 0, 1)
    expect(decisions().map((li) => li.textContent?.match(/Step \d+/)?.[0])).toEqual(['Step 4', 'Step 3', 'Step 2', 'Step 1']) // newest first

    serve(fx.arenaRun({ stepsDone: 6, status: 'completed', completedAt: '2026-10-02T08:14:00Z' }))
    await act(() => vi.advanceTimersByTimeAsync(3000))
    expect(screen.getByText('The arena run finished.')).toBeInTheDocument()
    expect(api.arenaSteps).toHaveBeenLastCalledWith('r1', 0, 3) // one last page after the final poll
    expect(decisions()).toHaveLength(6)
    expect(screen.getByRole('button', { name: 'Delete' })).toBeInTheDocument()

    const polls = vi.mocked(api.arenaRun).mock.calls.length
    await act(() => vi.advanceTimersByTimeAsync(9000))
    expect(api.arenaRun).toHaveBeenCalledTimes(polls) // polling stopped
    expect(api.arenaSteps).toHaveBeenCalledTimes(3)
  })

  it('toasts a run that fails while open, shows why and re-reads the refunded credits', async () => {
    vi.useFakeTimers()
    open()
    await act(() => vi.advanceTimersByTimeAsync(0))
    const me = vi.mocked(api.me).mock.calls.length
    serve(fx.arenaRun({ status: 'failed', error: 'The AI model is unavailable right now.', credits: 2 }))
    await act(() => vi.advanceTimersByTimeAsync(3000))
    expect(screen.getByText('The arena run failed.')).toBeInTheDocument()
    expect(screen.getByText(/The AI model is unavailable right now\. Unrun steps were refunded\./)).toBeInTheDocument()
    expect(vi.mocked(api.me).mock.calls.length).toBe(me + 1)
  })

  it('races the agents against muted baselines', async () => {
    open()
    await waitFor(() => expect(charts.at(-1)?.series).toHaveLength(4))
    const colors = charts.at(-1)!.series.map((s) => s.options.color)
    expect(colors).toEqual(['#C8962E', '#3B6FD8', '#64748B', '#A8A29E']) // agents in the palette, BTC and basket grey
    expect(screen.getAllByRole('listitem').map((li) => li.textContent)).toEqual(
      expect.arrayContaining(['Trend Rider', 'Gold bug', 'BTC buy & hold', 'Equal-weight basket']),
    )
  })

  it('cancels a running run and updates the credits', async () => {
    const cancel = vi
      .spyOn(api, 'cancelArenaRun')
      .mockResolvedValue({ run: fx.arenaRun({ status: 'cancelled', credits: 2 }), credits: { ...fx.credits, used: 12 } })
    const { user } = open()
    await user.click(await screen.findByRole('button', { name: 'Cancel run' }))
    expect(cancel).toHaveBeenCalledWith('r1')
    expect(await screen.findByText('Run cancelled. Unrun steps were refunded.')).toBeInTheDocument()
    expect(screen.getByText('Cancelled after 2 steps. Unrun steps were refunded.')).toBeInTheDocument()
    expect(creditsUsed()).toBe('12')
    expect(screen.queryByRole('button', { name: 'Cancel run' })).toBeNull()
  })

  it('says so when a cancel fails', async () => {
    vi.spyOn(api, 'cancelArenaRun').mockRejectedValue(new ApiError(409, 'conflict', 'The run is not running.'))
    const { user } = open()
    await user.click(await screen.findByRole('button', { name: 'Cancel run' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('The run is not running.')
  })
})

describe('ArenaRunPage · leaderboard', () => {
  it('ranks agents, leaves the baselines unranked and shows "—" for null metrics', async () => {
    serve(
      fx.arenaRun({
        status: 'completed',
        model: 'global.anthropic.claude-sonnet-5-5',
        agents: [fx.arenaAgent({ metrics: fx.arenaMetrics({ annualizedReturnPct: null, calmar: null, winRatePct: null }) })],
      }),
    )
    open()
    const agent = await waitFor(() => rowOf('Trend Rider'))
    expect(screen.queryByText(/claude|sonnet/i)).toBeNull() // the model is never named
    const head = within(agent.closest('table')!).getAllByRole('columnheader').map((th) => th.textContent)
    expect(head).toEqual(['#', 'Agent', 'Return', 'Ann. return', 'vs BTC', 'Sharpe', 'Sortino', 'Calmar', 'Max DD', 'Win rate', 'Turnover', 'Exposure', 'Fees'])
    const cells = within(agent).getAllByRole('cell').map((td) => td.textContent)
    expect(cells).toEqual(['1', 'Trend RiderMomentum', '▲up 4.2%', '—', '▲up 1.5%', '1.31', '1.92', '—', '−6.2%', '—', '140%', '72%', '$312'])
    const btc = rowOf('BTC buy & hold')
    expect(within(btc).getAllByRole('cell')[0]).toHaveTextContent('—')
    expect(btc).toHaveTextContent('Baseline')
    expect(screen.getByText(/Single run — small gaps are noise\./)).toHaveTextContent('Annualized return and Calmar need 90+ days')
  })

  it('leaves out annualized return and Calmar for 4-hour runs and marks benched agents', async () => {
    serve(
      fx.arenaRun({
        cadence: '4h',
        start: '2026-09-18',
        end: '2026-10-01',
        steps: 84,
        agents: [fx.arenaAgent({ status: 'benched', errors: 3 })],
        contaminated: true,
      }),
    )
    open()
    const agent = await waitFor(() => rowOf('Trend Rider'))
    const head = within(agent.closest('table')!).getAllByRole('columnheader').map((th) => th.textContent)
    expect(head).not.toContain('Ann. return')
    expect(head).not.toContain('Calmar')
    expect(agent).toHaveTextContent('Benched')
    expect(screen.getByText('Step 2 / 84').parentElement).toHaveTextContent('sim 2026-09-18 04:00')
    expect(screen.getByText('Contaminated')).toBeInTheDocument()
    expect(screen.getByText(/overlaps the model's training data \(cutoff Jun 30, 2026\)/)).toBeInTheDocument()
  })
})

describe('ArenaRunPage · decision log', () => {
  it('shows target chips, fills, tool calls, held and error rows', async () => {
    stepOf = (_a, k) =>
      [
        fx.arenaStep(0),
        fx.arenaStep(1, {
          targets: [{ symbol: 'BTC', weightPct: 30 }, { symbol: 'ETH', weightPct: 20 }],
          positions: [{ symbol: 'BTC', qty: 0.5, avgPrice: 61200, mark: 64000, weightPct: 30 }],
        }),
        fx.arenaStep(2, { targets: null, fills: [], tools: [], reasoning: 'Nothing changed.' }),
        fx.arenaStep(3, { targets: null, fills: [], tools: [], reasoning: '', error: 'The AI model is unavailable right now.' }),
      ][k]
    serve(fx.arenaRun({ stepsDone: 4 }))
    const { user } = open()
    await waitFor(() => expect(decisions()).toHaveLength(4))
    const [s4, s3, s2, s1] = decisions()

    expect(within(s1).getByRole('list', { name: 'Target changes' })).toHaveTextContent('BTC 0→50%')
    expect(within(s2).getByRole('list', { name: 'Target changes' })).toHaveTextContent('BTC 50→30%ETH 0→20%')
    expect(s1).toHaveTextContent('sim 2026-08-03')
    expect(s1).toHaveTextContent('$100,500▲up 0.50%')
    expect(within(s1).getByRole('table')).toHaveTextContent('▲ Buy BTC0.8$61,212.24$48,970$48.97')
    expect(s1).toHaveTextContent('Step 1: bought BTC on the trend.')

    await user.click(within(s1).getByText('1 tool call'))
    expect(within(s1).getByText('get_news "ETF flows" 2', { exact: false })).toHaveTextContent('get_news "ETF flows" 2 → 4 headlines')

    expect(s3).toHaveTextContent('Held — no orders.')
    expect(s3).toHaveTextContent('Nothing changed.')
    expect(s4).toHaveTextContent('Held — model error · The AI model is unavailable right now.')
    expect(within(s4).queryByRole('table')).toBeNull()
  })

  it('renders the reasoning as literal text, never as markup', async () => {
    const reasoning = 'Headline: <img src=x onerror=alert(1)> **bold** [link](https://evil.example)\nSecond line.'
    stepOf = (_a, k) => fx.arenaStep(k, { reasoning })
    serve(fx.arenaRun({ stepsDone: 1 }))
    const { container } = open()
    const p = await screen.findByText((_, el) => el?.tagName === 'P' && el.textContent === reasoning)
    expect(p).toHaveClass('whitespace-pre-wrap')
    expect(container.querySelector('img')).toBeNull()
    expect(container.querySelector('strong')).toBeNull()
    expect(screen.queryByRole('link', { name: 'link' })).toBeNull()
  })

  it('loads each agent on first view and shows a custom mandate', async () => {
    stepOf = (agent, k) => fx.arenaStep(k, { reasoning: `agent ${agent} step ${k}` })
    const { user } = open()
    expect(await screen.findByText('agent 0 step 1')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Gold bug' }))
    expect(await screen.findByText('agent 1 step 1')).toBeInTheDocument()
    expect(api.arenaSteps).toHaveBeenLastCalledWith('r1', 1, -1)
    expect(screen.getByText('Mandate').parentElement).toHaveTextContent('Hold PAXG as the core.')
    await user.click(screen.getByRole('button', { name: 'Trend Rider' }))
    expect(screen.getByText('agent 0 step 1')).toBeInTheDocument()
    expect(api.arenaSteps).toHaveBeenCalledTimes(2) // agent 0 came from the cache
  })

  it('says when no step has finished yet', async () => {
    const r = fx.arenaRun({ stepsDone: 0 })
    const first = <T extends { equity: unknown[] }>(x: T) => ({ ...x, equity: x.equity.slice(0, 1) }) // only the starting point
    serve({ ...r, agents: r.agents.map(first), benchmarks: r.benchmarks.map(first) })
    open()
    expect(await screen.findByText('No decisions yet. The first step is being decided…')).toBeInTheDocument()
    expect(screen.getByText('The curves start after the first step.')).toBeInTheDocument()
  })

  it('retries the decisions after a failed load', async () => {
    vi.mocked(api.arenaSteps).mockRejectedValueOnce(new ApiError(500, 'internal', 'Steps failed.'))
    serve(fx.arenaRun({ status: 'completed' }))
    const { user } = open()
    expect(await screen.findByRole('alert')).toHaveTextContent('Steps failed.')
    await user.click(screen.getByRole('button', { name: 'Try again' }))
    await waitFor(() => expect(decisions()).toHaveLength(2))
  })

  it("keeps one agent's failed load off another agent's log", async () => {
    serve(fx.arenaRun({ status: 'completed' }))
    const { user } = open()
    await waitFor(() => expect(decisions()).toHaveLength(2))
    vi.mocked(api.arenaSteps).mockRejectedValueOnce(new ApiError(500, 'internal', 'Steps failed.'))
    await user.click(screen.getByRole('button', { name: 'Gold bug' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Steps failed.')
    await user.click(screen.getByRole('button', { name: 'Trend Rider' }))
    expect(screen.queryByRole('alert')).toBeNull()
    expect(decisions()).toHaveLength(2) // still from the cache
  })
})

describe('ArenaRunPage · finished runs', () => {
  it('deletes a finished run after confirming', async () => {
    serve(fx.arenaRun({ status: 'completed' }))
    const del = vi.spyOn(api, 'deleteArenaRun').mockResolvedValue(undefined)
    const { user } = open()
    await user.click(await screen.findByRole('button', { name: 'Delete' }))
    const dialog = screen.getByRole('dialog', { name: 'Delete “Daily · Aug 3 → Oct 1, 2026”?' })
    await user.click(within(dialog).getByRole('button', { name: 'Keep it' }))
    expect(del).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'Delete' }))
    await user.click(within(dialog).getByRole('button', { name: 'Delete run' }))
    expect(del).toHaveBeenCalledWith('r1')
    expect(await screen.findByText('arena tab')).toBeInTheDocument()
    expect(currentPath()).toBe('/strategies?tab=arena&arena=historical')
  })

  it('keeps the run when the delete fails', async () => {
    serve(fx.arenaRun({ status: 'cancelled' }))
    vi.spyOn(api, 'deleteArenaRun').mockRejectedValue(new ApiError(409, 'conflict', 'The run is still running.'))
    const { user } = open()
    await user.click(await screen.findByRole('button', { name: 'Delete' }))
    await user.click(screen.getByRole('button', { name: 'Delete run' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('The run is still running.')
    expect(currentPath()).toBe('/strategies/arena/r1')
  })

  it('shows a not-found state for a missing run', async () => {
    vi.mocked(api.arenaRun).mockRejectedValue(new ApiError(404, 'not_found', 'Not found.'))
    open('nope')
    expect(await screen.findByRole('heading', { name: "This run doesn't exist." })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Back to the arena' })).toHaveAttribute('href', '/strategies?tab=arena&arena=historical')
  })

  it('retries a failed first load', async () => {
    vi.mocked(api.arenaRun).mockRejectedValueOnce(new ApiError(0, 'network', 'Cannot reach the server.'))
    const { user } = open()
    expect(await screen.findByRole('heading', { name: "Couldn't load this run." })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Try again' }))
    expect(await screen.findByRole('heading', { level: 1, name: 'Daily · Aug 3 → Oct 1, 2026' })).toBeInTheDocument()
  })
})

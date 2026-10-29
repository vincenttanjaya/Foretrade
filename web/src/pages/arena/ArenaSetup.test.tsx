import { act, fireEvent, screen, waitFor, within } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError } from '../../lib/api'
import * as fx from '../../test/fixtures'
import { creditsUsed, currentPath, renderPage, stubSession } from '../../test/utils'
import { StrategiesPage } from '../strategies/StrategiesPage'

const open = (route = '/strategies?tab=arena&arena=historical') =>
  renderPage(<StrategiesPage />, { route, path: '/strategies', routes: { '/strategies/arena/:id': <p>run page</p> } })

const costLine = () => screen.getByText(/ steps · ≈ /)
const startButton = () => screen.getByRole('button', { name: /Start run/ })
const date = (label: 'Start' | 'End', value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } })

beforeEach(() => {
  stubSession()
  vi.spyOn(api, 'arenaOptions').mockResolvedValue(fx.arenaOptions)
  vi.spyOn(api, 'arenaRuns').mockResolvedValue({ runs: [] })
})

describe('Arena setup', () => {
  it('opens from the Strategies tabs', async () => {
    vi.spyOn(api, 'strategies').mockResolvedValue({ strategies: [] })
    const { user } = open('/strategies')
    await user.click(screen.getByRole('button', { name: 'Arena' }))
    await user.click(screen.getByRole('button', { name: 'Historical' }))
    expect(await screen.findByRole('heading', { name: 'Let the agents trade the past.' })).toBeInTheDocument()
    expect(currentPath()).toBe('/strategies?tab=arena&arena=historical')
  })

  it('defaults to the daily window with the built-in agents and prices it live', async () => {
    const { user } = open()
    expect(await screen.findByLabelText('Start')).toHaveValue('2026-08-03')
    expect(screen.getByLabelText('End')).toHaveValue('2026-10-01')
    expect(screen.getByLabelText('End')).toHaveAttribute('max', '2026-10-01')
    expect(screen.getByLabelText('Start')).toHaveAttribute('max', '2026-09-02') // a 30-day window must still fit
    expect(screen.getAllByRole('checkbox', { checked: true })).toHaveLength(3)
    expect(costLine()).toHaveTextContent('60 steps · ≈ 4 min · 90 credits')
    expect(startButton()).toBeEnabled()
    expect(screen.queryByRole('note')).toBeNull() // starts after the cutoff

    await user.click(screen.getByRole('checkbox', { name: /Mean Reverter/ }))
    expect(costLine()).toHaveTextContent('60 steps · ≈ 4 min · 60 credits')

    await user.click(screen.getByRole('button', { name: '4-hour · 7–14 days' }))
    expect(screen.getByLabelText('Start')).toHaveValue('2026-09-18')
    expect(costLine()).toHaveTextContent('84 steps · ≈ 6 min · 84 credits')
  })

  it('warns about a window inside the training data and blocks invalid spans', async () => {
    open()
    await screen.findByLabelText('Start')
    date('Start', '2026-06-15')
    expect(screen.getByRole('note')).toHaveTextContent(
      "This window overlaps the model's training data (cutoff Jun 30, 2026). The model may remember what happened; treat results as optimistic.",
    )
    expect(screen.getByText('Daily runs cover 30–90 days; this window is 109.')).toBeInTheDocument()
    expect(startButton()).toBeDisabled()

    date('End', '2026-08-13')
    expect(costLine()).toHaveTextContent('60 steps')
    expect(startButton()).toBeEnabled()
    expect(screen.getByRole('note')).toBeInTheDocument() // allowed, just flagged

    date('End', '2026-10-05')
    expect(screen.getByText('The window must end by Oct 1, 2026, the last full day.')).toBeInTheDocument()
  })

  it('needs at least one agent and an instruction for your own', async () => {
    const { user } = open()
    for (const name of [/Trend Rider/, /Mean Reverter/, /News Desk/]) await user.click(await screen.findByRole('checkbox', { name }))
    expect(screen.getByText('Pick at least one agent.')).toBeInTheDocument()
    expect(startButton()).toBeDisabled()

    await user.click(screen.getByRole('checkbox', { name: /Your agent/ }))
    expect(screen.getByText('Give your agent an instruction of at least 10 characters.')).toBeInTheDocument()
    await user.type(screen.getByLabelText('Instruction'), 'Hold PAXG')
    expect(screen.getByText('9/1000')).toBeInTheDocument()
    await user.type(screen.getByLabelText('Instruction'), ' as the core.')
    expect(startButton()).toBeEnabled()
    expect(costLine()).toHaveTextContent('30 credits')
  })

  it('caps the agents at the maximum', async () => {
    vi.mocked(api.arenaOptions).mockResolvedValue({ ...fx.arenaOptions, maxAgents: 3 })
    open()
    expect(await screen.findByRole('checkbox', { name: /Your agent/ })).toBeDisabled()
    expect(screen.getByText(/3\/3 · same model/)).toBeInTheDocument()
  })

  it('starts a run with the chosen agents and opens it', async () => {
    const create = vi
      .spyOn(api, 'createArenaRun')
      .mockResolvedValue({ run: fx.arenaRun({ id: 'r9' }), warnings: [], credits: { ...fx.credits, used: 108 } })
    const { user } = open()
    await user.click(await screen.findByRole('checkbox', { name: /Mean Reverter/ }))
    await user.click(screen.getByRole('checkbox', { name: /Your agent/ }))
    await user.type(screen.getByLabelText('Agent name'), 'Gold bug')
    await user.type(screen.getByLabelText('Instruction'), '  Hold PAXG as the core.  ')
    await user.click(startButton())

    expect(create).toHaveBeenCalledWith({
      cadence: '1d',
      start: '2026-08-03',
      end: '2026-10-01',
      agents: [
        { personaId: 'momentum' },
        { personaId: 'macro' },
        { personaId: 'custom', name: 'Gold bug', instruction: 'Hold PAXG as the core.' },
      ],
    })
    expect(await screen.findByText('run page')).toBeInTheDocument()
    expect(currentPath()).toBe('/strategies/arena/r9')
    expect(creditsUsed()).toBe('108')
  })

  it('points at the running run when one is already in flight (409)', async () => {
    vi.spyOn(api, 'createArenaRun').mockRejectedValue(new ApiError(409, 'conflict', 'You already have a running arena run.'))
    const { user } = open()
    vi.mocked(api.arenaRuns).mockResolvedValue({ runs: [fx.arenaSummary({ id: 'r-live', name: 'Live run', status: 'running', stepsDone: 12, leader: null })] })
    await user.click(await screen.findByRole('button', { name: /Start run/ }))
    const toast = await screen.findByRole('alert')
    expect(toast).toHaveTextContent('You already have a run in progress')
    await user.click(within(toast).getByRole('link', { name: 'Open “Live run”' }))
    expect(currentPath()).toBe('/strategies/arena/r-live')
  })

  it('shows any other start error inline and re-reads credits', async () => {
    vi.spyOn(api, 'createArenaRun').mockRejectedValue(new ApiError(502, 'upstream', 'Market data is unavailable.'))
    const { user } = open()
    await screen.findByLabelText('Start')
    const calls = vi.mocked(api.me).mock.calls.length
    await user.click(startButton())
    expect(await screen.findByRole('alert')).toHaveTextContent('Market data is unavailable.')
    expect(vi.mocked(api.me).mock.calls.length).toBe(calls + 1)
  })

  it('blocks a run the remaining credits cannot pay for', async () => {
    vi.mocked(api.me).mockResolvedValue({ user: fx.user, credits: { ...fx.credits, used: 450 } })
    open()
    expect(await screen.findByText('Needs 90 credits; 50 left today. Credits reset at 00:00 UTC.')).toBeInTheDocument()
    expect(startButton()).toBeDisabled()
  })

  it('offers a retry when the options fail to load', async () => {
    vi.mocked(api.arenaOptions).mockRejectedValueOnce(new ApiError(500, 'internal', 'Boom.'))
    const { user } = open()
    expect(await screen.findByText("Couldn't load this arena.")).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Try again' }))
    expect(await screen.findByLabelText('Start')).toBeInTheDocument()
  })
})

describe('Arena past runs', () => {
  it('lists runs with status, progress, leader and the contamination flag', async () => {
    vi.mocked(api.arenaRuns).mockResolvedValue({
      runs: [
        fx.arenaSummary({ id: 'a', name: 'Live run', status: 'running', stepsDone: 23, leader: null }),
        fx.arenaSummary({ id: 'b' }),
        fx.arenaSummary({ id: 'c', name: 'Old window', status: 'failed', contaminated: true, cadence: '4h', start: '2026-05-01', end: '2026-05-14', steps: 84, leader: null }),
      ],
    })
    open()
    const rows = await screen.findAllByRole('link', { name: /run|window|Aug/ })
    expect(rows.map((r) => r.getAttribute('href'))).toEqual(['/strategies/arena/a', '/strategies/arena/b', '/strategies/arena/c'])
    expect(rows[0]).toHaveTextContent('Running 38%')
    expect(rows[0]).toHaveTextContent('Daily · Aug 3 → Oct 1, 2026 · 60 steps') // a custom name keeps the window
    expect(rows[1]).toHaveTextContent('Completed')
    expect(rows[1]).toHaveTextContent('Trend Rider▲up 12.4%')
    expect(rows[1]).toHaveTextContent('BTC▲up 8.1%')
    expect(rows[1]).not.toHaveTextContent('Aug 3 → Oct 1, 2026 · 60') // the default name already says it
    expect(rows[2]).toHaveTextContent('Failed')
    expect(rows[2]).toHaveTextContent('Contaminated')
    expect(rows[2]).toHaveTextContent('4-hour · May 1 → May 14, 2026 · 84 steps')
  })

  it('polls while a run is in flight', async () => {
    vi.useFakeTimers()
    try {
      vi.mocked(api.arenaRuns).mockResolvedValue({ runs: [fx.arenaSummary({ status: 'running', stepsDone: 3, leader: null })] })
      open()
      await act(() => vi.advanceTimersByTimeAsync(0))
      vi.mocked(api.arenaRuns).mockResolvedValue({ runs: [fx.arenaSummary()] })
      await act(() => vi.advanceTimersByTimeAsync(5000))
      expect(api.arenaRuns).toHaveBeenCalledTimes(2)
      await act(() => vi.advanceTimersByTimeAsync(15000))
      expect(api.arenaRuns).toHaveBeenCalledTimes(2) // nothing running any more
    } finally {
      vi.useRealTimers()
    }
  })

  it('shows the empty state', async () => {
    open()
    expect(await screen.findByRole('heading', { name: 'No runs yet.' })).toBeInTheDocument()
  })

  it('retries a failed list load', async () => {
    vi.mocked(api.arenaRuns).mockRejectedValueOnce(new ApiError(500, 'internal', 'List failed.'))
    const { user } = open()
    expect(await screen.findByRole('heading', { name: "Couldn't load your runs." })).toBeInTheDocument()
    await user.click(screen.getAllByRole('button', { name: 'Try again' }).at(-1)!)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'No runs yet.' })).toBeInTheDocument())
  })
})

import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import { describe, expect, it, vi } from 'vitest'
import { api } from '../../lib/api'
import type { AlertRun, AlertRunPage } from '../../lib/types'
import { schedule } from '../../test/fixtures'
import { ResearchHistory, RunSummary } from './ResearchHistory'

const run = (over: Partial<AlertRun> = {}): AlertRun => ({ id: 'r1', at: '2026-10-06T08:00:00Z', status: 'ok', body: 'Latest bounded body', error: null, excerpt: 'Bounded evidence', verdict: null, reportChatId: 'owned/report', ...over })

describe('research run states', () => {
  it('shows a working run without suggesting that a report or action is ready', () => {
    render(<MemoryRouter><RunSummary run={run({ status: 'running', reportChatId: null, excerpt: null, body: '' })} /></MemoryRouter>)
    expect(screen.getByText('Working')).toBeInTheDocument()
    expect(screen.getByText(/Working on the research report/)).toBeInTheDocument()
    expect(screen.queryByRole('link')).not.toBeInTheDocument()
    expect(screen.queryByText(/unavailable/)).not.toBeInTheDocument()
    expect(screen.queryByRole('button')).not.toBeInTheDocument()
  })

  it('renders unknown evidence and text as text, with an encoded owned report link', () => {
    render(<MemoryRouter><RunSummary run={run({ verdict: 'unknown', excerpt: '<img src=x onerror=alert(1)>' })} /></MemoryRouter>)
    expect(screen.getByText('Verdict unknown')).toBeInTheDocument()
    expect(screen.getByText('<img src=x onerror=alert(1)>')).toBeInTheDocument()
    expect(screen.queryByRole('img')).not.toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Open chat' })).toHaveAttribute('href', '/chat/owned%2Freport')
  })

  it('lets an initial failed history load retry to the bounded empty state', async () => {
    const history = vi.spyOn(api, 'alertRuns').mockRejectedValueOnce(new Error('offline')).mockResolvedValueOnce({ runs: [], nextCursor: null })
    render(<MemoryRouter><ResearchHistory alert={schedule({ id: 'owned1', action: 'research' })} /></MemoryRouter>)
    expect(await screen.findByRole('alert')).toHaveTextContent(/load the run history/)
    await userEvent.setup().click(screen.getByRole('button', { name: 'Try again' }))
    expect(await screen.findByText('No research runs in the last 30 days.')).toBeInTheDocument()
    expect(history).toHaveBeenLastCalledWith('owned1', { limit: 20 }, expect.any(AbortSignal))
    expect(screen.queryByRole('button', { name: 'Load older' })).not.toBeInTheDocument()
  })

  it('aborts an old owner-bound history read and ignores it after selecting another automation', async () => {
    let resolve!: (page: AlertRunPage) => void
    const history = vi.spyOn(api, 'alertRuns').mockImplementationOnce(() => new Promise(done => { resolve = done })).mockResolvedValueOnce({ runs: [run({ id: 'new', excerpt: 'New selected report', reportChatId: 'new-report' })], nextCursor: null })
    const view = render(<MemoryRouter><ResearchHistory alert={schedule({ id: 'owned1', action: 'research' })} /></MemoryRouter>)
    await waitFor(() => expect(history).toHaveBeenCalledTimes(1))
    const previousSignal = history.mock.calls[0][2]!
    view.rerender(<MemoryRouter><ResearchHistory alert={schedule({ id: 'owned2', action: 'research' })} /></MemoryRouter>)
    expect(await screen.findByText('New selected report')).toBeInTheDocument()
    expect(previousSignal.aborted).toBe(true)
    await act(async () => resolve({ runs: [run({ excerpt: 'Stale other report' })], nextCursor: 'stale-cursor' }))
    expect(screen.queryByText('Stale other report')).not.toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Open chat' })).toHaveAttribute('href', '/chat/new-report')
    expect(screen.queryByRole('button', { name: 'Load older' })).not.toBeInTheDocument()
  })
})

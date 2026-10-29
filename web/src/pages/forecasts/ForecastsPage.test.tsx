import { act, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError } from '../../lib/api'
import * as fx from '../../test/fixtures'
import { renderPage, stubSession } from '../../test/utils'
import { ForecastsPage } from './ForecastsPage'

const list = [
  fx.forecastSummary(),
  fx.forecastSummary({ id: 'f2', shape: 'ordered', question: 'Where will BTC close on Dec 31 2026?', status: 'running', completedAt: null, headline: null }),
  fx.forecastSummary({ id: 'f3', shape: 'categorical', category: 'tech', question: 'Which lab ships first?', status: 'failed', headline: null }),
]

const open = () => renderPage(<ForecastsPage />, { route: '/forecasts', path: '/forecasts' })
const rows = () => within(screen.getAllByRole('list').at(-1)!).getAllByRole('link')

beforeEach(() => {
  stubSession()
  vi.spyOn(api, 'trackRecord').mockResolvedValue(fx.trackRecord())
})

describe('ForecastsPage', () => {
  it('keeps archived forecasts accessible when the active workspace is empty', async () => {
    vi.spyOn(api, 'forecasts').mockImplementation(async (filter = {}) => ({
      forecasts: filter.status === 'archived' ? [fx.forecastSummary({ status: 'archived', archivedFromStatus: 'completed' })] : [],
    }))
    const { user } = open()
    await screen.findByRole('heading', { name: 'No forecasts yet.' })
    await user.click(screen.getByRole('button', { name: /^Archived/ }))
    const row = await screen.findByRole('link', { name: /Will ETH outperform BTC/ })
    expect(row).toHaveTextContent('Archived')
    expect(row).toHaveTextContent('42%')
    expect(row).toHaveAttribute('href', '/forecasts/f1')
    expect(screen.queryByRole('heading', { name: 'No forecasts yet.' })).not.toBeInTheDocument()
    expect(screen.getByText(/Open a forecast to read its saved report or restore it/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /^All/ }))
    expect(await screen.findByRole('heading', { name: 'No forecasts yet.' })).toBeInTheDocument()
  })

  it('retries an archive loading error and shows an honest empty archive', async () => {
    let attempts = 0
    vi.spyOn(api, 'forecasts').mockImplementation(async (filter = {}) => {
      if (filter.status === 'archived') {
        if (attempts++ === 0) throw new ApiError(503, 'unavailable', 'Archive unavailable')
        return { forecasts: [] }
      }
      return { forecasts: [fx.forecastSummary()] }
    })
    const { user } = open()
    await screen.findByText('Foretrade · 1 forecast')
    await user.click(screen.getByRole('button', { name: /^Archived/ }))
    expect(await screen.findByText('Archive unavailable')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Retry archive' }))
    expect(await screen.findByText('No archived forecasts.')).toBeInTheDocument()
  })

  it('shows the desk hero, counts and every forecast row', async () => {
    vi.spyOn(api, 'forecasts').mockResolvedValue({ forecasts: list })
    open()
    expect(await screen.findByText('Foretrade · 3 forecasts')).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Your forecasts.' })).toBeInTheDocument()
    expect(screen.getByText('1 in flight · 1 completed')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /New forecast/ })).toHaveAttribute('href', '/forecasts/new')

    const [done, running, failed] = rows()
    expect(done).toHaveAttribute('href', '/forecasts/f1')
    expect(done).toHaveTextContent('Crypto')
    expect(done).toHaveTextContent('Binary')
    expect(done).toHaveTextContent('Yes42%')
    expect(done).toHaveTextContent('Completed')
    expect(done).toHaveTextContent('Resolves Dec 31, 2026')
    expect(running).toHaveTextContent('Specialists at work…')
    expect(running).toHaveTextContent('In flight')
    expect(failed).toHaveTextContent('No result')
    expect(failed).toHaveTextContent('Failed')
  })

  it('badges an updated forecast', async () => {
    vi.spyOn(api, 'forecasts').mockResolvedValue({ forecasts: [fx.forecastSummary({ parentId: 'f0' }), fx.forecastSummary({ id: 'f2', parentId: null })] })
    open()
    await screen.findByText('Foretrade · 2 forecasts')
    expect(within(rows()[0]).getByText('Updated')).toBeInTheDocument()
    expect(within(rows()[1]).queryByText('Updated')).toBeNull()
  })

  it('leads a numeric row with its median when the view leads, and with the top range otherwise', async () => {
    const brief = ({ median, p10, p90, pAbove, unit, flat, headline, basis }: NonNullable<typeof fx.tsmDeskResult.view>) => ({ median, p10, p90, pAbove, unit, flat, headline, basis: basis.kind })
    const ordered = { shape: 'ordered' as const, question: 'What will be the NYSE closing price of TSM ADR on 2031-10-06?', headline: { outcome: '≥ $1,100', p: 0.2 } }
    vi.spyOn(api, 'forecasts').mockResolvedValue({ forecasts: [
      fx.forecastSummary({ id: 'tsm', ...ordered, view: brief(fx.tsmDeskResult.view!) }),
      fx.forecastSummary({ id: 'old', ...ordered, headline: { outcome: '$300 – $450', p: 0.19 }, view: brief(fx.tsmLegacyResult.view!) }),
      fx.forecastSummary({ id: 'btc', shape: 'ordered', question: 'Where will BTC close on Oct 14, 2026?', headline: { outcome: '$66,000 – $70,000', p: 0.46 }, view: brief(fx.btcBucketResult.view!) }),
      fx.forecastSummary({ id: 'run', ...ordered, status: 'running', completedAt: null, headline: null, view: brief(fx.tsmDeskResult.view!) }),
      fx.forecastSummary({ id: 'drift', ...ordered, view: { ...brief(fx.tsmDeskResult.view!), pAbove: null, basis: 'capped_drift' } }),
      fx.forecastSummary({ id: 'peaked', ...ordered, headline: { outcome: '$450 – $600', p: 0.31 }, view: { ...brief(fx.tsmLegacyResult.view!), flat: false } }),
      fx.forecastSummary({ id: 'sure', ...ordered, view: { ...brief(fx.tsmDeskResult.view!), pAbove: { value: 0.9975 } } }),
    ] })
    open()
    await screen.findByText('Foretrade · 7 forecasts')
    const [tsm, old, btc, running, drift, peaked, sure] = rows()
    expect(sure, 'a chance that rounds to 100 is never stated as a certainty').toHaveTextContent('>99% ≥ today')
    expect(sure).not.toHaveTextContent('100%')
    expect(tsm).toHaveTextContent('Median$611')
    expect(tsm).toHaveTextContent('62% ≥ today')
    expect(tsm).not.toHaveTextContent('≥ $1,100')
    expect(old, 'a view that states no median: its 80% range, never the flat top range').toHaveTextContent('≈$247–≈$1,35080% range')
    expect(old).not.toHaveTextContent(/Median|\$300 – \$450|≥ today/)
    expect(btc, 'a peaked short ladder keeps its top range').toHaveTextContent('$66,000 – $70,00046%')
    expect(btc).not.toHaveTextContent('Median')
    expect(running).toHaveTextContent('Specialists at work…')
    expect(drift, 'a stated median without a reference: the 80% range under it').toHaveTextContent('Median$61180% ≈$242–≈$1,480')
    expect(peaked, 'no median, but a range that stands out: the top range as before').toHaveTextContent('$450 – $60031%')
  })

  it('filters by status and shape, and clears filters', async () => {
    vi.spyOn(api, 'forecasts').mockResolvedValue({ forecasts: list })
    const { user } = open()
    await screen.findByText('Foretrade · 3 forecasts')
    const status = screen.getByRole('group', { name: 'Filter by status' })
    expect(within(status).getAllByRole('button').map((b) => b.textContent)).toEqual(['All3', 'In flight1', 'Completed1', 'Failed1', 'Archived'])

    await user.click(within(status).getByRole('button', { name: /In flight/ }))
    expect(rows()).toHaveLength(1)
    expect(rows()[0]).toHaveAttribute('href', '/forecasts/f2')

    const shape = screen.getByRole('group', { name: 'Filter by shape' })
    await user.click(within(shape).getByRole('button', { name: 'Binary' }))
    expect(screen.getByText('No forecasts match these filters.')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Clear filters' }))
    expect(rows()).toHaveLength(3)

    await user.click(within(shape).getByRole('button', { name: 'Categorical' }))
    expect(rows()).toHaveLength(1)
    await user.click(within(shape).getByRole('button', { name: 'Categorical' })) // again: off
    expect(rows()).toHaveLength(3)
  })

  it('searches questions and available outcome labels, trims spaces and clears a search', async () => {
    vi.spyOn(api, 'forecasts').mockResolvedValue({ forecasts: [
      fx.forecastSummary({ id: 'eth', question: 'Will ETH finish above $4,000?' }),
      fx.forecastSummary({ id: 'lab', shape: 'categorical', question: 'Which lab ships first?', category: 'tech', headline: { outcome: 'OpenAI', p: 0.6 } }),
      fx.forecastSummary({ id: 'resolved', shape: 'categorical', question: 'Which company leads?', category: 'tech', headline: { outcome: 'Apple', p: 0.4 }, resolvedOutcome: 'Google' }),
    ] })
    const { user } = open()
    await screen.findByText('Foretrade · 3 forecasts')
    const search = screen.getByRole('searchbox', { name: 'Search forecasts' })
    await user.type(search, '  eth  ')
    expect(rows().map((row) => row.getAttribute('href'))).toEqual(['/forecasts/eth'])
    expect(screen.getByText('Showing 1 of 3 forecasts')).toBeInTheDocument()
    await user.clear(search)
    await user.type(search, 'openai')
    expect(rows().map((row) => row.getAttribute('href'))).toEqual(['/forecasts/lab'])
    await user.clear(search)
    await user.type(search, 'GOOGLE')
    expect(rows().map((row) => row.getAttribute('href'))).toEqual(['/forecasts/resolved'])
    await user.click(screen.getByRole('button', { name: 'Clear search' }))
    expect(search).toHaveValue('')
    expect(rows()).toHaveLength(3)
  })

  it('combines category, failed status and search, then resets every control from an empty result', async () => {
    vi.spyOn(api, 'forecasts').mockResolvedValue({ forecasts: list })
    const { user } = open()
    await screen.findByText('Foretrade · 3 forecasts')
    await user.selectOptions(screen.getByRole('combobox', { name: 'Category' }), 'tech')
    await user.click(within(screen.getByRole('group', { name: 'Filter by status' })).getByRole('button', { name: /Failed/ }))
    expect(rows().map((row) => row.getAttribute('href'))).toEqual(['/forecasts/f3'])
    await user.selectOptions(screen.getByRole('combobox', { name: 'Sort forecasts' }), 'oldest')
    await user.selectOptions(screen.getByRole('combobox', { name: 'Resolution' }), 'resolved')
    await user.type(screen.getByRole('searchbox', { name: 'Search forecasts' }), 'No such question')
    expect(screen.getByText('No forecasts match these filters.')).toBeInTheDocument()
    expect(screen.getByText('Showing 0 of 3 forecasts')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Clear filters' }))
    expect(rows()).toHaveLength(3)
    expect(screen.getByRole('searchbox', { name: 'Search forecasts' })).toHaveValue('')
    expect(screen.getByRole('combobox', { name: 'Category' })).toHaveValue('all')
    expect(screen.getByRole('combobox', { name: 'Resolution' })).toHaveValue('all')
    expect(screen.getByRole('combobox', { name: 'Sort forecasts' })).toHaveValue('newest')
  })

  it('orders forecasts by creation time or earliest resolution date without losing their links', async () => {
    vi.spyOn(api, 'forecasts').mockResolvedValue({ forecasts: [
      fx.forecastSummary({ id: 'middle', createdAt: '2026-09-15T12:00:00Z', resolutionDate: '2026-12-31' }),
      fx.forecastSummary({ id: 'oldest', createdAt: '2026-09-01T12:00:00Z', resolutionDate: '2026-11-01' }),
      fx.forecastSummary({ id: 'newest', createdAt: '2026-10-01T12:00:00Z', resolutionDate: '2026-11-01' }),
    ] })
    const { user } = open()
    await screen.findByText('Foretrade · 3 forecasts')
    expect(rows().map((row) => row.getAttribute('href'))).toEqual(['/forecasts/newest', '/forecasts/middle', '/forecasts/oldest'])
    const sort = screen.getByRole('combobox', { name: 'Sort forecasts' })
    await user.selectOptions(sort, 'oldest')
    expect(rows().map((row) => row.getAttribute('href'))).toEqual(['/forecasts/oldest', '/forecasts/middle', '/forecasts/newest'])
    await user.selectOptions(sort, 'resolution')
    expect(rows().map((row) => row.getAttribute('href'))).toEqual(['/forecasts/newest', '/forecasts/oldest', '/forecasts/middle'])
  })

  it('distinguishes completed runs from resolved questions and includes today among due questions', async () => {
    const today = new Date().toISOString().slice(0, 10)
    vi.spyOn(api, 'forecasts').mockResolvedValue({ forecasts: [
      fx.forecastSummary({ id: 'future', resolutionDate: '2100-01-01' }),
      fx.forecastSummary({ id: 'today', resolutionDate: today }),
      fx.forecastSummary({ id: 'overdue', resolutionDate: '2000-01-01', status: 'failed', headline: null }),
      fx.forecastSummary({ id: 'settled', resolutionDate: '2000-01-01', resolvedOutcome: 'No' }),
    ] })
    const { user } = open()
    await screen.findByText('Foretrade · 4 forecasts')
    const resolution = screen.getByRole('combobox', { name: 'Resolution' })
    await user.selectOptions(resolution, 'unresolved')
    expect(rows().map((row) => row.getAttribute('href'))).toEqual(['/forecasts/future', '/forecasts/today', '/forecasts/overdue'])
    await user.selectOptions(resolution, 'due')
    expect(rows().map((row) => row.getAttribute('href'))).toEqual(['/forecasts/today', '/forecasts/overdue'])
    expect(screen.getByText(/Due means unresolved questions/)).toBeInTheDocument()
    await user.selectOptions(resolution, 'resolved')
    expect(rows().map((row) => row.getAttribute('href'))).toEqual(['/forecasts/settled'])
    await user.selectOptions(resolution, 'all')
    expect(rows()).toHaveLength(4)
  })

  it('keeps the selected replay source when clearing filters', async () => {
    vi.spyOn(api, 'forecasts').mockImplementation(async (filter = {}) => ({
      forecasts: filter.scope === 'replay' ? fx.replaySummaries : [fx.forecastSummary()],
    }))
    const { user } = open()
    await screen.findByText('Foretrade · 1 forecast')
    const source = screen.getByRole('group', { name: 'Filter by source' })
    await user.click(within(source).getByRole('button', { name: /Track record/ }))
    await waitFor(() => expect(rows()).toHaveLength(2))
    await user.type(screen.getByRole('searchbox', { name: 'Search forecasts' }), 'missing')
    await user.click(screen.getByRole('button', { name: 'Clear filters' }))
    expect(rows()).toHaveLength(2)
    expect(within(source).getByRole('button', { name: /Track record/ })).toHaveAttribute('aria-pressed', 'true')
  })

  it('offers a return to your forecasts when the replay library is empty', async () => {
    vi.spyOn(api, 'forecasts').mockImplementation(async (filter = {}) => ({
      forecasts: filter.scope === 'replay' ? [] : [fx.forecastSummary()],
    }))
    const { user } = open()
    await screen.findByText('Foretrade · 1 forecast')
    await user.click(within(screen.getByRole('group', { name: 'Filter by source' })).getByRole('button', { name: /Track record/ }))
    expect(await screen.findByText('No replay questions yet.')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Show your forecasts' }))
    expect(rows().map((row) => row.getAttribute('href'))).toEqual(['/forecasts/f1'])
  })

  it('shows the track record and lists the replay questions behind it', async () => {
    const get = vi.spyOn(api, 'forecasts').mockImplementation(async (filter = {}) => ({
      forecasts: filter.scope === 'replay' ? fx.replaySummaries : [fx.forecastSummary({ resolvedOutcome: 'No' })],
    }))
    const { user } = open()
    const card = (await screen.findByRole('heading', { name: 'Scored against what happened' })).closest('section')!
    expect(await within(card).findByText(/^24 of 50 questions scored\./)).toBeInTheDocument() // too few for a score
    expect(await within(card).findByRole('heading', { name: 'Recent desk calls' })).toBeInTheDocument()
    expect(rows()).toHaveLength(1)
    // An own resolved row: the desk's top outcome (No at 42% Yes) happened.
    expect(rows()[0]).toHaveTextContent('✓ Happened: No')

    const source = screen.getByRole('group', { name: 'Filter by source' })
    await user.click(within(source).getByRole('button', { name: /Track record/ }))
    expect(get).toHaveBeenLastCalledWith({ scope: 'replay' })
    await waitFor(() => expect(rows()[0]).toHaveTextContent(fx.replaySummaries[0].question)) // also among the recent desk calls
    const [sol, btc] = rows()
    expect(sol).toHaveAttribute('href', '/forecasts/r1234567-bbbb')
    expect(sol).toHaveTextContent('Replay · as of Aug 12')
    expect(sol).toHaveTextContent('✗ Happened: Yes') // the desk said 30%
    expect(sol).toHaveTextContent('Resolved Aug 26, 2026')
    expect(sol).not.toHaveTextContent('Created')
    expect(btc).toHaveTextContent('✓ Happened: Yes')
    expect(within(source).getByRole('button', { name: /Track record/ })).toHaveAttribute('aria-pressed', 'true')

    await user.click(within(source).getByRole('button', { name: /Track record/ })) // off: back to your own
    expect(rows()).toHaveLength(1)
    await user.click(within(card).getByRole('button', { name: 'See the questions' }))
    expect(rows()).toHaveLength(2)
    expect(get).toHaveBeenCalledTimes(3) // yours, the recent desk calls, and the replay list once
  })

  it("lets a new user browse the replay questions from the track record", async () => {
    vi.spyOn(api, 'forecasts').mockImplementation(async (filter = {}) => ({ forecasts: filter.scope === 'replay' ? fx.replaySummaries : [] }))
    const { user } = open()
    expect(await screen.findByRole('heading', { name: 'No forecasts yet.' })).toBeInTheDocument()
    await user.click(await screen.findByRole('button', { name: 'See the questions' }))
    await waitFor(() => expect(rows()[1]).toHaveTextContent(fx.replaySummaries[1].question))
    expect(screen.queryByRole('heading', { name: 'No forecasts yet.' })).not.toBeInTheDocument()
  })

  it('starts new users from example ideas', async () => {
    vi.spyOn(api, 'forecasts').mockResolvedValue({ forecasts: [] })
    open()
    expect(await screen.findByRole('heading', { name: 'No forecasts yet.' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Will ETH outperform BTC over Q4 2026?' })).toHaveAttribute('href', '/forecasts/new')
    expect(screen.getByRole('link', { name: '+ New forecast' })).toBeInTheDocument()
  })

  it('shows a retry when the first load fails', async () => {
    let down = true // your list only: the track record's replay calls must not take the scripted failure
    vi.spyOn(api, 'forecasts').mockImplementation(async (filter = {}) => {
      if (filter.scope) return { forecasts: [] }
      if (down) throw new ApiError(500, 'internal', 'Database is down')
      return { forecasts: list }
    })
    const { user } = open()
    expect(await screen.findByRole('heading', { name: "Couldn't load your forecasts." })).toBeInTheDocument()
    expect(screen.getByText('Database is down')).toBeInTheDocument()
    down = false
    await user.click(screen.getByRole('button', { name: 'Try again' }))
    expect(await screen.findByText('Foretrade · 3 forecasts')).toBeInTheDocument()
  })

  describe('polling', () => {
    afterEach(() => vi.useRealTimers())

    it('refreshes every 5 s while a run is in flight and stops once none are', async () => {
      vi.useFakeTimers()
      const get = vi.spyOn(api, 'forecasts').mockResolvedValue({ forecasts: list })
      const own = () => get.mock.calls.filter(([filter]) => !filter?.scope).length // not the track record's replay calls
      open()
      await act(() => vi.advanceTimersByTimeAsync(0))
      expect(own()).toBe(1)

      get.mockRejectedValueOnce(new ApiError(0, 'network', 'offline'))
      await act(() => vi.advanceTimersByTimeAsync(5000))
      expect(screen.getByText("Couldn't refresh · retrying")).toBeInTheDocument()

      get.mockResolvedValue({ forecasts: [fx.forecastSummary()] })
      await act(() => vi.advanceTimersByTimeAsync(5000))
      expect(screen.getByText('Foretrade · 1 forecast')).toBeInTheDocument()
      await act(() => vi.advanceTimersByTimeAsync(20_000))
      expect(own()).toBe(3)
    })
  })
})

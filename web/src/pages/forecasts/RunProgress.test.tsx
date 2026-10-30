import { render, screen, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from '../../lib/api'
import type { Forecast, ForecastProgress } from '../../lib/types'
import * as fx from '../../test/fixtures'
import { renderPage, stubSession } from '../../test/utils'
import { ForecastDetailPage } from './ForecastDetailPage'
import { Working } from './RunProgress'

const running = (progress?: ForecastProgress | null): Forecast =>
  fx.forecast({ status: 'running', result: null, completedAt: null, shape: 'categorical', outcomes: ['Nvidia', 'Apple', 'Microsoft'], progress })

const trace = (over: Partial<ForecastProgress> = {}): ForecastProgress => ({
  research: { status: 'pending', queries: [], found: 0, read: 0, mode: null },
  specialists: [
    { role: 'base_rate', status: 'pending', top: null },
    { role: 'quant', status: 'pending', top: null },
    { role: 'skeptic', status: 'pending', top: null },
  ],
  aggregator: { status: 'pending' },
  market: { status: 'pending', matched: false },
  updatedAt: '2026-10-02T08:00:05Z',
  ...over,
})

const row = (name: string) => screen.getByText(name).closest('li')!
const status = (name: string) => row(name).querySelector(':scope > span')!.textContent // the row's status column

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(new Date(2026, 9, 2, 12))
})
afterEach(() => vi.useRealTimers())

describe('Working · live run trace', () => {
  it('keeps every stage animated for a run without a trace', () => {
    render(<Working f={running()} reconnecting={false} />)
    expect(screen.getByRole('heading', { name: 'Specialists at work' })).toBeInTheDocument()
    expect(status('Researching the web')).toBe('Searching…')
    expect(screen.getAllByText('Thinking…')).toHaveLength(3)
    expect(status('Aggregator')).toBe('Up next')
    expect(row('Aggregator')).toHaveClass('opacity-65')
    expect(screen.queryByText('Polymarket')).toBeNull()
  })

  it("says what each specialist works from: the quant's models, not a lognormal it may not fit", () => {
    render(<Working f={running()} reconnecting={false} />)
    expect(within(row('Market & quant analyst')).getByText('Live market data, valuation and volatility models')).toBeInTheDocument()
    expect(within(row('Base-rate statistician')).getByText('Reference classes and historical frequencies')).toBeInTheDocument()
    expect(screen.queryByText(/lognormal/i)).toBeNull()
  })

  it('starts with the query writer while everything else waits and the market lookup runs', () => {
    render(<Working f={running(trace({ research: { status: 'running', queries: [], found: 0, read: 0, mode: null } }))} reconnecting />)
    expect(status('Researching the web')).toBe('Writing search queries…')
    expect(screen.getAllByText('Waiting')).toHaveLength(3)
    expect(status('Aggregator')).toBe('Up next')
    expect(status('Polymarket')).toBe('Searching…')
    expect(screen.getByRole('status')).toHaveTextContent('Connection hiccup · retrying…')
  })

  it('shows the queries as chips while it searches', () => {
    const queries = ['Nvidia Apple Microsoft market cap', 'largest company by market cap']
    render(<Working f={running(trace({ research: { status: 'running', queries, found: 0, read: 0, mode: null } }))} reconnecting={false} />)
    const chips = within(screen.getByRole('list', { name: 'Search queries' })).getAllByRole('listitem')
    expect(chips.map((c) => c.textContent)).toEqual(queries)
    expect(chips[0]).toHaveClass('font-mono')
    expect(status('Researching the web')).toBe('Searching…')
  })

  it('pops in each specialist number, marks a dropped one and weighs what is left', () => {
    const p = trace({
      research: { status: 'done', queries: ['Nvidia market cap'], found: 8, read: 3, mode: 'live' },
      specialists: [
        { role: 'base_rate', status: 'done', top: { outcome: 'Nvidia', p: 0.41 } },
        { role: 'quant', status: 'running', top: null },
        { role: 'skeptic', status: 'failed', top: null },
      ],
      aggregator: { status: 'running' },
      market: { status: 'done', matched: true },
    })
    render(<Working f={running(p)} reconnecting={false} />)
    expect(status('Researching the web')).toBe('8 sources · 3 read')
    expect(screen.getByText('Nvidia market cap')).toBeInTheDocument() // the chips stay once done
    expect(status('Base-rate statistician')).toBe('Nvidia 41%')
    expect(row('Base-rate statistician').querySelector('.animate-bar')).toHaveStyle({ width: '41%' })
    expect(status('Market & quant analyst')).toBe('Thinking…')
    expect(row('Market & quant analyst').querySelector('.animate-shimmer')).not.toBeNull()
    expect(status('Narrative & macro skeptic')).toBe('Unavailable')
    expect(row('Narrative & macro skeptic')).toHaveClass('opacity-65')
    expect(status('Aggregator')).toBe('Weighing the views…')
    expect(screen.getByText('Weighs the two views, flags conflicts, estimates odds')).toBeInTheDocument()
    expect(status('Polymarket')).toBe('Market found')
  })

  it.each([
    ['wire', 6, 'Stored news wire · 6 headlines'],
    ['carried', 5, "Previous run's sources"],
    ['none', 0, 'No sources found'],
    ['live', 1, '1 source'],
  ] as const)('says what research ran: %s', (mode, found, text) => {
    const p = trace({ research: { status: 'done', queries: [], found, read: 0, mode }, aggregator: { status: 'done' }, market: { status: 'done', matched: false } })
    render(<Working f={running(p)} reconnecting={false} />)
    expect(status('Researching the web')).toBe(text)
    expect(status('Aggregator')).toBe('Done')
    expect(status('Polymarket')).toBe('No match')
  })

  it('has no Polymarket row when the run never looks one up', () => {
    render(<Working f={running(trace({ market: { status: 'skipped', matched: false } }))} reconnecting={false} />)
    expect(screen.queryByText('Polymarket')).toBeNull()
    expect(screen.getByText('Weighs the three views, flags conflicts, estimates odds')).toBeInTheDocument()
  })
})

describe('ForecastDetailPage · panel size', () => {
  beforeEach(() => stubSession())

  it('counts the specialists that actually ran', async () => {
    const twoRan: Forecast = fx.forecast({ result: { ...fx.forecastResult, specialists: fx.forecastResult.specialists.slice(0, 2) } })
    vi.spyOn(api, 'forecast').mockResolvedValue({ forecast: twoRan })
    renderPage(<ForecastDetailPage />, { route: '/forecasts/f1234567-aaaa', path: '/forecasts/:id' })
    expect(await screen.findByText(/^Panel: 2 specialists \+ aggregator · research: /)).toBeInTheDocument()
    expect(screen.queryByText(/3 specialists/)).toBeNull()
  })
})

describe('Working · ordered runs', () => {
  it('looks for the strike ladder in the Polymarket row', () => {
    const ordered = (market: ForecastProgress['market']) =>
      fx.forecast({ status: 'running', result: null, completedAt: null, shape: 'ordered', outcomes: fx.ladderBuckets, progress: trace({ market }) })
    const { rerender } = render(<Working f={ordered({ status: 'pending', matched: false })} reconnecting={false} />)
    expect(status('Polymarket')).toBe('Looking for a strike ladder')
    expect(row('Polymarket')).toHaveTextContent('Looks for a Polymarket strike ladder ending the same day')
    rerender(<Working f={ordered({ status: 'done', matched: true })} reconnecting={false} />)
    expect(status('Polymarket')).toBe('Strike ladder found')
    rerender(<Working f={ordered({ status: 'done', matched: false })} reconnecting={false} />)
    expect(status('Polymarket')).toBe('No strike ladder')
  })
})

describe('Working · deep research', () => {
  const deep = (research: Partial<ForecastProgress['research']>) =>
    fx.deepForecast({ status: 'running', result: null, completedAt: null, progress: { ...fx.deepProgress, research: { ...fx.deepProgress.research, ...research } } })
  const steps = () => within(screen.getByRole('list', { name: 'Research steps' })).getAllByRole('listitem')

  it('names the stage, shows the angles and one line per step with the current one as the status', () => {
    render(<Working f={deep({})} reconnecting={false} />)
    expect(row('Deep research')).toHaveTextContent('Plans research angles, searches, screens, reads and extracts findings')
    expect(screen.getByText(/Usually four to eight minutes/)).toBeInTheDocument()
    const angles = within(screen.getByRole('list', { name: 'Research angles' })).getAllByRole('listitem')
    expect(angles.map((a) => a.textContent)).toEqual(fx.deepAngles.map((a) => a.label))
    expect(screen.queryByRole('list', { name: 'Search queries' })).toBeNull()
    expect(steps().map((li) => [li.dataset.status, li.textContent])).toEqual([
      ['done', 'Planned 4 angles'],
      ['done', 'Round 1 · 12 searches · 58 results'],
      ['failed', '!Screening failed · kept what it had'],
      ['done', 'Read 7 of 10 pages'],
      ['done', '11 new findings'],
      ['running', 'Looking for gaps…'],
    ])
    expect(steps()[5]).toHaveClass('text-text')
    expect(steps()[5].querySelector('.animate-ping')).not.toBeNull()
    expect(status('Deep research')).toBe('Looking for gaps…')
  })

  it('plans first, falls back to the queries for chips and shows a skipped phase', () => {
    const { rerender } = render(<Working f={deep({ steps: [], angles: [], findings: 0 })} reconnecting={false} />)
    expect(screen.getByText('Planning…')).toBeInTheDocument()
    expect(screen.queryByRole('list', { name: 'Research steps' })).toBeNull()
    expect(screen.getByRole('list', { name: 'Search queries' })).toBeInTheDocument()
    rerender(<Working f={deep({ steps: [{ phase: 'plan', round: 0, status: 'done', count: 4, of: 0 }, { phase: 'gap', round: 1, status: 'skipped', count: 0, of: 0 }] })} reconnecting={false} />)
    expect(steps()[1]).toHaveTextContent('–Gap review skipped (time or search budget)')
    expect(status('Deep research'), 'nothing running: the last step').toBe('Gap review skipped (time or search budget)')
  })

  it('keeps one line per phase and round when a step is written twice', () => {
    render(<Working f={deep({ steps: [{ phase: 'read', round: 1, status: 'running', count: 0, of: 0 }, { phase: 'read', round: 1, status: 'done', count: 9, of: 14 }] })} reconnecting={false} />)
    expect(steps().map((li) => li.textContent)).toEqual(['Read 9 of 14 pages'])
  })

  it('sums up the ledger once done, and keeps the fallback copy when the pack came from elsewhere', () => {
    const { rerender } = render(<Working f={deep({ status: 'done', mode: 'live', found: 22, read: 12, findings: 34 })} reconnecting={false} />)
    expect(screen.getByText('34 findings · 22 sources · 12 read')).toBeInTheDocument()
    rerender(<Working f={deep({ status: 'done', mode: 'live', found: 1, read: 0, findings: 0 })} reconnecting={false} />)
    expect(screen.getByText('1 source')).toBeInTheDocument()
    rerender(<Working f={deep({ status: 'done', mode: 'wire', found: 6, read: 0, findings: 0 })} reconnecting={false} />)
    expect(screen.getByText('Stored news wire · 6 headlines')).toBeInTheDocument()
    rerender(<Working f={deep({ status: 'pending', steps: [] })} reconnecting={false} />)
    expect(status('Deep research')).toBe('Waiting')
  })

  it('leaves a quick or older trace exactly as before', () => {
    render(<Working f={running(trace({ research: { status: 'running', queries: ['q'], found: 0, read: 0, mode: null, depth: 'quick', angles: [], searches: 0, findings: 0, steps: [] } }))} reconnecting={false} />)
    expect(status('Researching the web')).toBe('Searching…')
    expect(screen.queryByRole('list', { name: 'Research steps' })).toBeNull()
    expect(screen.getByText(/Usually one to three minutes/)).toBeInTheDocument()
  })

  it('reads as quick when a failed plan handed over to the quick stage', () => {
    const plan = [{ phase: 'plan' as const, round: 0, status: 'failed' as const, count: 0, of: 0 }]
    render(<Working f={deep({ depth: 'quick', steps: plan, angles: [], queries: ['Orchid Materials SGX IPO'], findings: 0 })} reconnecting={false} />)
    expect(status('Researching the web')).toBe('Searching…')
    expect(screen.queryByText('Deep research')).toBeNull()
    expect(screen.queryByRole('list', { name: 'Research steps' })).toBeNull()
    expect(screen.getByRole('list', { name: 'Search queries' })).toHaveTextContent('Orchid Materials SGX IPO')
    expect(screen.getByText(/Usually one to three minutes/)).toBeInTheDocument()
  })

  it('shows angles that share a label once each, without key clashes', () => {
    const err = vi.spyOn(console, 'error').mockImplementation(() => {})
    render(<Working f={deep({ angles: ['Other factors', 'Other factors'] })} reconnecting={false} />)
    expect(within(screen.getByRole('list', { name: 'Research angles' })).getAllByRole('listitem')).toHaveLength(2)
    expect(err.mock.calls.flat().join(' ')).not.toMatch(/same key/)
    err.mockRestore()
  })
})

describe('Working · your materials', () => {
  const step = (materials: ForecastProgress['materials']) => render(<Working f={running(trace({ materials }))} reconnecting={false} />)
  const rows = () => [...document.querySelectorAll('li[data-stage]')].map((li) => li.getAttribute('data-stage'))

  it('puts the digest row first while reading, and none on a run without materials', () => {
    step({ status: 'running', total: 3, reused: 1, read: 2, failed: 0 })
    expect(rows()[0]).toBe('materials')
    expect(row('Your materials')).toHaveTextContent('Reads each file once and notes what bears on the question')
    expect(status('Your materials')).toBe('Reading your materials… (2 of 3)')
  })

  it.each([
    [{ status: 'pending', total: 2, reused: 2, read: 2, failed: 0 }, 'Using 2 materials from the previous run'],
    [{ status: 'done', total: 1, reused: 1, read: 1, failed: 0 }, 'Using 1 material from the previous run'],
    [{ status: 'done', total: 3, reused: 0, read: 3, failed: 0 }, 'Read 3 materials'],
    [{ status: 'done', total: 3, reused: 0, read: 2, failed: 1 }, "Read 2 of 3 · 1 couldn't be read"],
    [{ status: 'done', total: 2, reused: 0, read: 0, failed: 2 }, "!Couldn't read any of 2 files"],
    [{ status: 'done', total: 1, reused: 0, read: 0, failed: 1 }, "!Couldn't read your file"],
  ] as const)('reads %j as "%s"', (m, text) => {
    step(m)
    expect(status('Your materials')).toBe(text)
  })

  it('renders an older trace exactly as before', () => {
    render(<Working f={running(trace())} reconnecting={false} />)
    expect(rows()).toEqual(['research', 'base_rate', 'quant', 'skeptic', 'aggregator', 'market'])
    expect(screen.queryByText('Your materials')).toBeNull()
  })

  it('still reads a deep trace as deep behind the materials row', () => {
    render(<Working f={fx.materialsForecast({ status: 'running', result: null, progress: { ...fx.deepProgress, materials: fx.materialsProgress.materials } })} reconnecting={false} />)
    expect(screen.getByText(/Deep web research, then three specialist views/)).toBeInTheDocument()
  })
})

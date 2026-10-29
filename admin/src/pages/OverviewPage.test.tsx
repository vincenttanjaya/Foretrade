import { act, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError, api } from '../lib/api'
import * as fx from '../test/fixtures'
import { renderPage } from '../test/utils'
import { newsStale, OverviewPage } from './OverviewPage'

const open = () => renderPage(<OverviewPage />)
const card = (title: string) => screen.getByRole('heading', { name: title }).closest('.card') as HTMLElement
const row = (label: string) => within(card('System status')).getByText(label, { selector: 'dt' }).nextElementSibling as HTMLElement

afterEach(() => vi.useRealTimers())

describe('OverviewPage', () => {
  it('shows KPIs, the credit bars, feature usage, top users, paper totals and system status', async () => {
    vi.spyOn(api, 'overview').mockResolvedValue({ overview: fx.overview() })
    open()
    expect(await screen.findByRole('heading', { level: 1, name: 'Platform overview.' })).toBeInTheDocument()
    const kpis = await screen.findByRole('region', { name: 'Key numbers' })
    expect(kpis).toHaveTextContent('Users31')
    expect(kpis).toHaveTextContent('2 admins · 3 disabled')
    expect(kpis).toHaveTextContent('Active today47d 12 · 30d 20')
    expect(kpis).toHaveTextContent('New users · 30d9')
    expect(kpis).toHaveTextContent('AI credits today427d 300 · 30d 1,200')

    const bars = within(screen.getByRole('group', { name: 'AI credits per day, last 30 days' })).getAllByRole('img')
    expect(bars.at(-1)).toHaveAccessibleName('Oct 3: 42 credits')
    expect(screen.getByText(/Default limit 500 credits/)).toBeInTheDocument()

    const features = within(card('Features')).getAllByRole('row').slice(1)
    expect(features.map((r) => r.textContent)).toEqual([
      'Chat messages540150',
      'Forecasts1620',
      'Strategies039',
      'AI portfolios025',
      'Arena runs012',
      'Paper orders73099',
      'Copy plans2611',
    ])
    expect(card('Features')).toHaveTextContent('Copy plans counts created plans, including stopped plans.')

    expect(within(card('Top users')).getByRole('link', { name: /Demo Investor/ })).toHaveAttribute('href', '/users/u1')
    expect(card('Paper totals')).toHaveTextContent('Equity$2,001,234')
    expect(card('Paper totals')).toHaveTextContent('Total P&L+$1,234')
    expect(card('Paper totals')).toHaveTextContent('Notional · 30d$250K')

    expect(row('Version')).toHaveTextContent('627ee75abcde')
    expect(row('Uptime')).toHaveTextContent('2d 4h')
    expect(row('Database')).toHaveTextContent('OK · 3 ms')
    expect(row('Model')).toHaveTextContent('global.anthropic.claude-sonnet-5-5Knowledge cutoff Jun 30, 2026')
    expect(row('News')).toHaveTextContent('Last ingest 1h ago · 412 items · brief 1h 0m old')
    expect(row('News')).not.toHaveTextContent('Stale')
    expect(screen.getByRole('meter', { name: 'Brave calls used today' })).toHaveAttribute('aria-valuenow', '126')
    expect(row('Crawler')).toHaveTextContent('Reachable')
    expect(row('Jobs')).toHaveTextContent('1 forecasts · 0 arena runs running')
  })

  it('shows live paper desk health, UTC cadence and conservative request limits separately from credits', async () => {
    vi.spyOn(api, 'overview').mockResolvedValue({ overview: fx.overview() })
    open()
    const desk = await screen.findByRole('region', { name: 'Live paper desk' })
    expect(desk).toHaveTextContent('StatusLive')
    expect(desk).toHaveTextContent('CadenceEvery 4 hours · closed bars')
    expect(desk).toHaveTextContent('LaunchedOct 1, 2026, 12:00 AM UTC')
    expect(desk).toHaveTextContent('Last stepOct 3, 2026, 4:00 AM UTC')
    expect(desk).toHaveTextContent('Next stepOct 3, 2026, 8:00 AM UTC')
    expect(desk).toHaveTextContent('Agents3 · 1 benched')
    expect(desk).toHaveTextContent('Active copy plans8')
    expect(desk).toHaveTextContent('Claimed decisions today3 / 18')
    expect(desk).toHaveTextContent("Request ceiling for today's claims12")
    expect(desk).toHaveTextContent('4 requests per claimed decision')
    expect(desk).toHaveTextContent('72 model requests/day · 4,096 output tokens/request · 90s/decision')
    expect(desk).toHaveTextContent('Shared desk uses no user AI credits.')
    expect(desk).not.toHaveTextContent(/\$|requests used|requests billed|spend|global\.anthropic/i)
    expect(within(desk).queryByRole('meter')).not.toBeInTheDocument()
  })

  it.each([
    ['starting', 'Starting · awaiting first decision'],
    ['paused', 'Paused · job paused'],
    ['stale', 'Stale · last completed step is overdue'],
    ['outage', 'Outage · live desk unavailable'],
  ] as const)('shows the loaded %s desk state with nullable times and its retained error', async (status, text) => {
    const liveArena = fx.liveArena({
      status, enabled: status !== 'paused', stale: status === 'stale' || status === 'outage',
      startedAt: null, lastStepAt: null, nextStepAt: null,
      lastError: status === 'stale' || status === 'outage' ? '<strong>Decision save failed</strong>' : null,
    })
    vi.spyOn(api, 'overview').mockResolvedValue({
      overview: fx.overview({ system: fx.system({ jobs: { forecastsRunning: 0, arenaRunsRunning: 0, liveArena } }) }),
    })
    open()
    const desk = await screen.findByRole('region', { name: 'Live paper desk' })
    const deskRow = (label: string) => within(desk).getByText(label, { selector: 'dt' }).nextElementSibling
    expect(deskRow('Status')).toHaveTextContent(text)
    for (const label of ['Launched', 'Last step', 'Next step']) expect(deskRow(label)).toHaveTextContent('—')
    expect(deskRow('Freshness')).toHaveTextContent(liveArena.stale ? 'Stale' : 'Current')
    expect(deskRow('Last error')).toHaveTextContent(liveArena.lastError ?? '—')
    expect(desk.querySelector('strong')).not.toBeInTheDocument()
  })

  it('renders unavailable values as a dash and flags degraded systems', async () => {
    vi.spyOn(api, 'overview').mockResolvedValue({
      overview: fx.overview({
        topUsers: [],
        paper: { accounts: 0, openPositions: 0, equity: null, totalPnl: null, notional30d: 0 },
        system: fx.system({
          model: '', // AI off
          db: { ok: false, latencyMs: 900 },
          news: { enabled: false, lastIngestAt: null, items: 0, briefAt: null, briefAgeMin: null },
          crawler: { base: 'https://crawl.example.com', configured: false, reachable: null },
        }),
      }),
    })
    open()
    await screen.findByRole('heading', { name: 'System status' })
    expect(card('Paper totals')).toHaveTextContent('Equity—')
    expect(card('Paper totals')).toHaveTextContent('Total P&L—')
    expect(screen.getByText('No AI usage in the last 7 days.')).toBeInTheDocument()
    expect(row('Database')).toHaveTextContent('Failing')
    expect(row('Model')).toHaveTextContent('AI is off · choose a provider')
    expect(within(row('Model')).getByRole('link', { name: 'choose a provider' })).toHaveAttribute('href', '/settings')
    expect(row('News')).toHaveTextContent('Disabled: no Brave API key configured.')
    expect(row('Crawler')).toHaveTextContent('Not configured')
  })

  it('flags stale news and an unreachable crawler', async () => {
    vi.spyOn(api, 'overview').mockResolvedValue({
      overview: fx.overview({
        system: fx.system({
          news: { enabled: true, lastIngestAt: new Date(Date.now() - 4 * 3600_000).toISOString(), items: 3, briefAt: null, briefAgeMin: null },
          crawler: { base: 'https://crawl.example.com', configured: true, reachable: false },
        }),
      }),
    })
    open()
    await screen.findByRole('heading', { name: 'System status' })
    expect(row('News')).toHaveTextContent('brief —Stale: check the ingestion job.')
    expect(row('Crawler')).toHaveTextContent('Unreachable')
  })

  it('shows the error with a Retry, then the data', async () => {
    const overview = vi.spyOn(api, 'overview').mockRejectedValueOnce(new ApiError(500, 'internal', 'Database unavailable.'))
    const { user } = open()
    expect(await screen.findByRole('alert')).toHaveTextContent('Database unavailable.')
    overview.mockResolvedValue({ overview: fx.overview() })
    await user.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByRole('region', { name: 'Key numbers' })).toBeInTheDocument()
  })

  it('refreshes every minute and on demand, keeping the data when a refresh fails', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const overview = vi.spyOn(api, 'overview').mockResolvedValue({ overview: fx.overview() })
    const { user } = open()
    await screen.findByRole('region', { name: 'Key numbers' })
    expect(overview).toHaveBeenCalledTimes(1)
    const hidden = vi.spyOn(document, 'hidden', 'get').mockReturnValue(true)
    await act(() => vi.advanceTimersByTimeAsync(60_000))
    expect(overview).toHaveBeenCalledTimes(1)
    hidden.mockReturnValue(false)
    await act(() => vi.advanceTimersByTimeAsync(60_000))
    expect(overview).toHaveBeenCalledTimes(2)

    overview.mockRejectedValueOnce(new ApiError(0, 'network', "Can't reach the API."))
    await user.click(screen.getByRole('button', { name: 'Refresh' }))
    expect(await screen.findByRole('alert')).toHaveTextContent("Couldn't refresh: Can't reach the API.")
    expect(screen.getByRole('region', { name: 'Key numbers' })).toBeInTheDocument()
    expect(screen.getByRole('region', { name: 'Live paper desk' })).toHaveTextContent('StatusLive')
  })
})

describe('newsStale', () => {
  const now = Date.parse('2026-10-03T12:00:00Z')
  const news = (lastIngestAt: string | null, briefAgeMin: number | null, enabled = true) => ({ enabled, lastIngestAt, items: 1, briefAt: null, briefAgeMin })
  it.each([
    [news('2026-10-03T10:00:00Z', 120), false],
    [news('2026-10-03T08:59:00Z', 120), true], // ingest > 3 h
    [news('2026-10-03T10:00:00Z', 361), true], // brief > 6 h
    [news('2026-10-03T10:00:00Z', null), true],
    [news(null, 10), true],
    [news('2026-10-03T10:00:00Z', 10, false), true],
  ])('%j → %s', (n, stale) => expect(newsStale(n, now)).toBe(stale))
})

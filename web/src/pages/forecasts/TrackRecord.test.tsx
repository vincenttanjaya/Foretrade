import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from '../../lib/api'
import type { TrackRecord } from '../../lib/types'
import * as fx from '../../test/fixtures'
import { anchorOf, claimOf, recentCalls, TrackRecordCard, type TrackRecords } from './TrackRecord'

const setup = (records: TrackRecords | null = fx.trackRecord(), error: string | null = null) => {
  const onSeeQuestions = vi.fn()
  const user = userEvent.setup()
  const view = render(
    <MemoryRouter>
      <TrackRecordCard records={records} error={error} onSeeQuestions={onSeeQuestions} />
    </MemoryRouter>,
  )
  return { user, onSeeQuestions, ...view }
}

const replay = (over: Partial<TrackRecord>) => ({ ...fx.trackRecord(), replay: { ...fx.trackRecord().replay, ...over } })

/** No score claim anywhere on the card: no percentage, no Brier, no chart (the card has no recent calls here). */
const expectNoScore = () => {
  const card = screen.getByRole('region', { name: 'Scored against what happened' })
  expect(card.textContent).not.toMatch(/%|Brier/)
  expect(within(card).queryByRole('img')).not.toBeInTheDocument()
}

beforeEach(() => {
  vi.spyOn(api, 'forecasts').mockResolvedValue({ forecasts: fx.replaySummaries })
})

describe('TrackRecordCard', () => {
  it('shows no score while the replay is too small to tell skill from luck', async () => {
    vi.mocked(api.forecasts).mockResolvedValue({ forecasts: [] })
    const { user, onSeeQuestions } = setup()
    expect(screen.getByRole('button', { name: 'Historical replay · 24' })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByRole('button', { name: 'Yours · 0' })).toHaveAttribute('aria-pressed', 'false')
    expect(screen.getByText('Building a track record')).toBeInTheDocument()
    expect(
      screen.getByText(
        '24 of 50 questions scored. We show a score once there are enough resolved questions to tell skill from luck, measured against a simple volatility formula, not a coin flip.',
      ),
    ).toBeInTheDocument()
    expect(screen.getByRole('progressbar', { name: 'Questions scored' })).toHaveAttribute('aria-valuetext', '24 of 50')
    expect(screen.queryByRole('img')).not.toBeInTheDocument()
    expectNoScore()
    const tip = screen.getByRole('tooltip')
    expect(tip).toHaveTextContent(/zero-drift lognormal outlook over 90 days of realized volatility/)
    expect(tip).toHaveTextContent(/It is the bar to beat\. A coin flip is a weak one here: the thresholds sit near the day's price/)
    expect(screen.getByLabelText('About the volatility-only formula')).toHaveAttribute('aria-describedby', 'anchor-tip')
    expect(screen.getByText(/Historical replay: the same three-specialist panel/)).toHaveTextContent(
      "Historical replay: the same three-specialist panel re-run as of Jul 15, Aug 12 and Sep 9, 2026 (after the model's training cutoff) with only Binance data and news published before each date, on 24 two-week crypto price questions. The volatility-only formula is the bar to beat: a coin flip is a weak one on these questions. We show a score only once it is backed by enough resolved questions.",
    )
    await user.click(screen.getByRole('button', { name: 'See the questions' }))
    expect(onSeeQuestions).toHaveBeenCalledOnce()
  })

  it('shows the same no-number copy when the desk is behind or level with the formula', () => {
    for (const status of ['no_edge', 'behind'] as const) {
      vi.mocked(api.forecasts).mockResolvedValue({ forecasts: [] })
      const { unmount } = setup(replay({ n: 60, anchorN: 60, claim: { status, basis: 'formula', minN: 50, n: 60, skill: null } }))
      expect(screen.getByText('No proven edge yet')).toBeInTheDocument()
      expect(
        screen.getByText('60 questions scored. The desk has not yet beaten a simple volatility formula by more than chance. We show a score once it does.'),
      ).toBeInTheDocument()
      expect(screen.queryByRole('progressbar')).not.toBeInTheDocument()
      expect(screen.queryByRole('img')).not.toBeInTheDocument()
      expectNoScore()
      expect(screen.getByText(/We show a score only once it is backed by enough resolved questions\./)).toBeInTheDocument()
      unmount()
    }
  })

  it('says what the record is measured against: an even guess with no price questions, price questions when mixed', () => {
    vi.mocked(api.forecasts).mockResolvedValue({ forecasts: [] })
    const uniform = { n: 60, anchorBrier: null, anchorN: 0, claim: { status: 'no_edge' as const, basis: 'uniform' as const, minN: 50, n: 60, skill: null } }
    const { unmount } = setup(replay(uniform))
    expect(
      screen.getByText('60 questions scored. The desk has not yet beaten an even guess by more than chance. We show a score once it does.'),
    ).toBeInTheDocument()
    expect(screen.queryByText(/volatility formula/)).not.toBeInTheDocument()
    expect(screen.queryByRole('tooltip')).not.toBeInTheDocument() // the formula is not this record's bar
    expectNoScore()
    unmount()

    // 80 resolved, 12 of them price questions with a reference: the count toward the formula's 50 says so.
    const { unmount: u2 } = setup(replay({ n: 80, anchorN: 12, claim: { status: 'too_few', basis: null, minN: 50, n: 12, skill: null } }))
    expect(screen.getByText(/^12 of 50 price questions scored\. We show a score once/)).toBeInTheDocument()
    expect(screen.getByRole('progressbar', { name: 'Questions scored' })).toHaveAttribute('aria-valuetext', '12 of 50')
    expectNoScore()
    u2()

    setup(replay({ n: 80, anchorN: 60, claim: { status: 'behind', basis: 'formula', minN: 50, n: 60, skill: null } }))
    expect(screen.getByText(/^60 price questions scored\. The desk has not yet beaten a simple volatility formula/)).toBeInTheDocument()
    expect(screen.getByRole('tooltip')).toBeInTheDocument()
    expectNoScore()
  })

  it('says a record still building with no price question is measured against an even guess, else the formula', () => {
    vi.mocked(api.forecasts).mockResolvedValue({ forecasts: [] })
    const building = (anchorN: number, anchorBrier: number | null) =>
      replay({ n: 20, anchorN, anchorBrier, claim: { status: 'too_few', basis: null, minN: 50, n: anchorN || 20, skill: null } })
    const { unmount } = setup(building(0, null))
    expect(
      screen.getByText(
        '20 of 50 questions scored. We show a score once there are enough resolved questions to tell skill from luck, measured against an even guess across the outcomes.',
      ),
    ).toBeInTheDocument()
    expect(screen.queryByText(/volatility formula/)).not.toBeInTheDocument()
    expect(screen.queryByRole('tooltip')).not.toBeInTheDocument()
    expectNoScore()
    unmount()

    setup(building(8, 0.21))
    expect(screen.getByText(/^8 of 50 price questions scored\. .*measured against a simple volatility formula, not a coin flip\.$/)).toBeInTheDocument()
    expect(screen.getByRole('tooltip')).toBeInTheDocument()
  })

  it('reads an older payload without a claim as too few, never the coin-flip percentage', () => {
    vi.mocked(api.forecasts).mockResolvedValue({ forecasts: [] })
    const { anchorN, anchorSkill, claim, ...old } = fx.trackRecord().replay
    void [anchorN, anchorSkill, claim]
    expect(anchorOf(old)).toEqual({ brier: 0.1814, n: 24 }) // older payloads paired every replay row
    expect(claimOf(old)).toEqual({ status: 'too_few', basis: null, minN: 50, n: 24, skill: null })
    setup({ ...fx.trackRecord(), replay: { ...old, n: 80 } })
    expect(screen.getByText('Building a track record')).toBeInTheDocument()
    // Past the minimum but with no server verdict: the count alone, never "80 of 50", and no full progress bar.
    expect(screen.getByText(/^80 questions scored\. We show a score once/)).toBeInTheDocument()
    expect(screen.queryByRole('progressbar')).not.toBeInTheDocument()
    expectNoScore()
    // An "ahead" without a positive skill is not a backed score.
    expect(claimOf(replay({ claim: { status: 'ahead', basis: 'formula', minN: 50, n: 60, skill: null } }).replay).status).toBe('no_edge')
    expect(claimOf(replay({ claim: { status: 'ahead', basis: null, minN: 50, n: 60, skill: 0.1 } }).replay).status).toBe('no_edge')
  })

  it('shows the backed score, its range, the chart and the coin flip second when the desk is ahead of the formula', () => {
    const ahead = { n: 64, anchorN: 64, brier: 0.162, anchorBrier: 0.1814, skill: 0.352, brierCi90: [0.13, 0.19] as [number, number] }
    setup(replay({ ...ahead, anchorSkill: 0.107, claim: { status: 'ahead', basis: 'formula', minN: 50, n: 64, skill: 0.107 } }))
    const hero = screen.getByText(/better than a volatility-only formula$/)
    expect(hero).toHaveTextContent(/^11% better than a volatility-only formula$/)
    expect(hero.closest('[data-claim]')).toHaveAttribute('data-claim', 'ahead')
    expect(screen.getByText(/^Brier/)).toHaveTextContent('Brier 0.162 (90% range 0.13–0.19) · formula 0.181 · coin flip 0.250 · n = 64')
    expect(screen.getByText(/than a coin flip/)).toHaveTextContent(/^35% better than a coin flip$/)
    expect(screen.getByText(/than a coin flip/)).toHaveClass('text-muted')
    expect(screen.getByRole('img', { name: /^Reliability chart\./ })).toBeInTheDocument()
    expect(screen.getByRole('tooltip')).toBeInTheDocument()
    expect(screen.getByText(/Top outcome right/)).toHaveTextContent('Top outcome right 48 of 64 (75%) · log loss 0.574')
    expect(screen.getByText(/Historical replay: the same three-specialist panel/)).toHaveTextContent(/a coin flip is a weak one on these questions\. See the questions$/)
    expect(screen.queryByText(/We show a score only once/)).not.toBeInTheDocument()
    expect(screen.queryByText('Building a track record')).not.toBeInTheDocument()
  })

  it('rounds a tiny backed skill to under 1%, and pairs a partial anchor on Yours', async () => {
    const { unmount } = setup(replay({ n: 70, anchorN: 70, claim: { status: 'ahead', basis: 'formula', minN: 50, n: 70, skill: 0.004 } }))
    expect(screen.getByText(/better than a volatility-only formula$/)).toHaveTextContent(/^Under 1% better than a volatility-only formula$/)
    unmount()

    // Yours: 55 of 80 resolved forecasts were price questions with a reference.
    const mine = { n: 80, brier: 0.15, baselineBrier: 0.25, skill: 0.4, hitRate: 0.5, logLoss: 0.5, anchorBrier: 0.2, anchorN: 55, anchorSkill: 0.25 }
    const { user } = setup(fx.trackRecord({ ...mine, claim: { status: 'ahead', basis: 'formula', minN: 50, n: 55, skill: 0.25 } }))
    await user.click(screen.getByRole('button', { name: 'Yours · 80' }))
    expect(screen.getByText(/better than a volatility-only formula$/)).toHaveTextContent('25% better than a volatility-only formula')
    expect(screen.getByText(/^Brier/)).toHaveTextContent('Brier 0.150 · formula 0.200 (n = 55) · coin flip 0.250 · n = 80')
    expect(screen.getByText(/than a coin flip/)).toHaveTextContent('40% better than a coin flip')
    expect(screen.queryByText(/Historical replay:/)).not.toBeInTheDocument()
  })

  it('compares a record with no price questions with an even guess, and counts Yours toward the minimum', async () => {
    const mine = { n: 52, brier: 0.2, baselineBrier: 0.25, skill: 0.2, hitRate: 0.5, logLoss: 0.6, anchorBrier: null, anchorN: 0 }
    const { user, unmount } = setup(fx.trackRecord({ ...mine, claim: { status: 'ahead', basis: 'uniform', minN: 50, n: 52, skill: 0.2 } }))
    await user.click(screen.getByRole('button', { name: 'Yours · 52' }))
    expect(screen.getByText(/better than an even guess$/)).toHaveTextContent(/^20% better than an even guess$/)
    expect(screen.getByText(/^Brier/)).toHaveTextContent('Brier 0.200 · coin flip 0.250 · n = 52')
    expect(screen.queryByText(/than a coin flip$/)).not.toBeInTheDocument() // the even guess is the coin flip here
    expect(screen.queryByRole('tooltip')).not.toBeInTheDocument()
    unmount()

    const { user: u2 } = setup(fx.trackRecord({ n: 3, brier: 0.1, baselineBrier: 0.25, skill: 0.6, hitRate: 1, logLoss: 0.3 }))
    await u2.click(screen.getByRole('button', { name: 'Yours · 3' }))
    expect(screen.getByText(/^3 of 50 questions scored\./)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'See the questions' })).not.toBeInTheDocument()
    expectNoScore()
  })

  it('lists the 3 most recent resolved replay calls, without a per-call score', async () => {
    const call = (id: string, resolutionDate: string, p: number, resolvedOutcome: string | null) =>
      fx.forecastSummary({ id, resolutionDate, headline: { outcome: 'Yes', p }, resolvedOutcome, replay: true })
    const rows = [
      call('a', '2026-09-23', 0.71, 'Yes'),
      ...fx.replaySummaries,
      call('b', '2026-09-22', 0.4, 'No'),
      call('c', '2026-09-30', 0.5, null), // unresolved: skipped
    ]
    expect(recentCalls(rows).map((f) => f.id)).toEqual(['a', 'b', 'r1234567-bbbb'])
    vi.mocked(api.forecasts).mockResolvedValue({ forecasts: rows })
    setup()
    const list = (await screen.findByRole('heading', { name: 'Recent desk calls' })).closest('section')!
    expect(api.forecasts).toHaveBeenCalledWith({ status: 'completed', scope: 'replay' })
    const links = within(list).getAllByRole('link')
    expect(links.map((l) => l.getAttribute('href'))).toEqual(['/forecasts/a', '/forecasts/b', '/forecasts/r1234567-bbbb'])
    expect(links[0]).toHaveTextContent('Desk Yes 71% → Resolved Yes')
    expect(links[1]).toHaveTextContent('Desk Yes 40% → Resolved No')
    expect(links[2]).toHaveTextContent('Desk Yes 30% → Resolved Yes')
    expect(list.textContent).not.toMatch(/Brier/)
  })

  it('keeps the calibration label in a legend under the chart, off the dots', () => {
    const { container } = setup(replay({ n: 60, anchorN: 60, claim: { status: 'ahead', basis: 'formula', minN: 50, n: 60, skill: 0.1 } }))
    const chart = screen.getByRole('img', { name: /^Reliability chart\./ })
    expect(chart).toHaveAccessibleName(expect.stringContaining('Forecasts near 31% happened 43% of the time (7)'))
    expect(chart).not.toHaveTextContent('perfectly calibrated')
    expect(chart.nextElementSibling).toHaveTextContent('perfectly calibratedforecasts, sized by count')
    expect(chart).toHaveTextContent('Forecast probability')
    expect(chart).toHaveTextContent('How often it happened')
    const dots = [...container.querySelectorAll('[data-bin] circle')]
    expect(dots).toHaveLength(3)
    expect(dots.map((c) => Number(c.getAttribute('r')))).toEqual([3 + Math.sqrt(7), 3 + Math.sqrt(9), 3 + Math.sqrt(8)])
    // Higher observed frequency sits higher (smaller y); the 0.69 bin that always happened is at the top line.
    const ys = dots.map((c) => Number(c.getAttribute('cy')))
    expect(ys[0] > ys[1] && ys[1] > ys[2]).toBe(true)
    expect(container.querySelector('[data-bin="0.6"]')).toHaveTextContent('8')
  })

  it('shows the empty states and a load failure', async () => {
    const { user, unmount } = setup()
    await user.click(screen.getByRole('button', { name: 'Yours · 0' }))
    expect(
      screen.getByText('Resolve your forecasts to build your own record. Price questions on Binance assets resolve automatically.'),
    ).toBeInTheDocument()
    expect(screen.queryByRole('img')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'See the questions' })).not.toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'Recent desk calls' })).not.toBeInTheDocument()
    unmount()

    const empty = fx.trackRecord()
    setup({ replay: { ...empty.mine }, mine: empty.mine })
    expect(screen.getByText('The replay track record is being prepared.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Historical replay · 0' })).toBeInTheDocument()
  })

  it("labels a user's multi-outcome baseline as a uniform guess, with no anchor", async () => {
    const mine = { ...fx.trackRecord().replay, n: 60, brier: 0.2, baselineBrier: 2 / 9, anchorBrier: null, anchorN: 0, hitRate: 0.5, logLoss: 1.204 }
    Object.assign(mine, { skill: 0.1, brierCi90: [0.18, 0.215], anchorDiffCi90: null, claim: { status: 'ahead', basis: 'uniform', minN: 50, n: 60, skill: 0.1 } })
    const { user } = setup(fx.trackRecord(mine))
    await user.click(screen.getByRole('button', { name: 'Yours · 60' }))
    expect(screen.getByText(/better than an even guess$/)).toHaveTextContent(/^10% better than an even guess$/)
    expect(screen.getByText(/^Brier/)).toHaveTextContent('Brier 0.200 (90% range 0.18–0.21) · uniform guess 0.222 · n = 60')
    expect(screen.queryByText(/than a volatility-only formula/)).not.toBeInTheDocument()
    expect(screen.getByText(/Top outcome right/)).toHaveTextContent('Top outcome right 30 of 60 (50%) · log loss 1.204')
  })

  it('loads, then reports a failure', () => {
    const { unmount } = setup(null)
    expect(screen.getByLabelText('Loading the track record')).toBeInTheDocument()
    expect(api.forecasts).not.toHaveBeenCalled() // the recent calls wait for the record
    unmount()
    setup(null, 'Database is down')
    expect(screen.getByText("Couldn't load the track record. Database is down")).toBeInTheDocument()
  })
})

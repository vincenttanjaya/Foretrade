import { act, screen, waitFor, within } from '@testing-library/react'
import { useLocation } from 'react-router'
import { describe, expect, it, vi } from 'vitest'
import { api, ApiError } from '../../lib/api'
import type { RemovedPick, SignalPick } from '../../lib/types'
import * as fx from '../../test/fixtures'
import { json, renderPage, stubSession } from '../../test/utils'
import { SignalsPage } from './SignalsPage'
import {
  computedAgo,
  factorChips,
  favourSincePick,
  isLegacyPick,
  isLegacyRow,
  isStale,
  nextTry,
  pendingScore,
  pickPrompts,
  priceText,
  reasonText,
  replayText,
  sincePick,
  standing,
  standingLabel,
  standingText,
  targetProgress,
  validity,
  verdictText,
} from './signals'

function AgentState() {
  return <pre data-testid="agent-state">{JSON.stringify(useLocation().state)}</pre>
}
const open = () => renderPage(<SignalsPage />, { route: '/signals', path: '/signals', routes: { '/': <AgentState /> } })
const card = (name: string) => screen.getByRole('article', { name })
const sinceCell = (article: HTMLElement) => within(article).getByText('Since pick').nextElementSibling as HTMLElement

/** The paragraph §10.2 of the design states for today's replay constants, word for word. */
const IN_TESTING =
  'In testing on data the rule was not tuned on (Dec 24, 2025 – Oct 10, 2026, 864 picks), picks moved their way 49.3% of the time, ' +
  'against 49.3% for random picks from the same list. The average 72-hour move in the pick\'s direction was -0.12% (-0.22% after ' +
  'costs), against -0.09% for a random pick: a difference of -0.03% (95% CI -0.97% to +0.72%), so no measurable edge. Bullish picks ' +
  'reached their target first 20.3% of the time (random 12.1%), but also hit their invalidation first 11.8% of the time (random 8.1%): ' +
  'bigger swings, not better direction. Bearish picks reached their target first 7.2% of the time (random 7.5%).'

describe('signals math', () => {
  it('measures progress from the pick price to the target, clamped to 0..1', () => {
    const long = { priceAtPick: 100, target: 120 }
    expect(targetProgress(110, long)).toBe(0.5)
    expect(targetProgress(90, long)).toBe(0) // moved away
    expect(targetProgress(130, long)).toBe(1) // past the target
    expect(targetProgress(95, { priceAtPick: 100, target: 90 })).toBe(0.5) // bearish: down is progress
    expect(targetProgress(undefined, long)).toBe(0)
    expect(targetProgress(100, { priceAtPick: 100, target: 100 })).toBe(0)
    expect(sincePick(105, 100)).toBeCloseTo(5)
    expect(sincePick(undefined, 100)).toBeNull()
  })

  it('counts down to the end of the pick', () => {
    const now = Date.parse('2026-10-03T08:00:00Z')
    expect(validity('2026-10-05T13:59:00Z', now)).toBe('Ends in 2d 5h')
    expect(validity('2026-10-03T11:20:30Z', now)).toBe('Ends in 3h 20m')
    expect(validity('2026-10-03T08:12:00Z', now)).toBe('Ends in 12m')
    expect(validity('2026-10-03T08:00:30Z', now)).toBe('Ended') // under a minute left
    expect(validity('2026-10-03T07:00:00Z', now)).toBe('Ended')
  })

  it('says in words whether a pick is working for its side, with the raw price move beside it', () => {
    const btc = standing(favourSincePick(84213.2, { priceAtPick: 80000, side: 'bullish' }), 'bullish')
    expect(btc).toEqual({ favour: 5.3, price: 5.3, state: 'working' })
    expect([standingText(btc), priceText(btc!)]).toEqual(['Working +5.3%', 'Price +5.3%'])
    expect(standingLabel(btc, 'bullish')).toBe('Working for this bullish pick: +5.3%. Price up 5.3% since the pick.')

    const eth = standing(favourSincePick(3000, { priceAtPick: 3200, side: 'bearish' }), 'bearish') // the price fell 6.25%
    expect([standingText(eth), priceText(eth!)]).toEqual(['Working +6.3%', 'Price -6.3%'])
    expect(standingLabel(eth, 'bearish')).toBe('Working for this bearish pick: +6.3%. Price down 6.3% since the pick.')

    const strk = standing(favourSincePick(150, { priceAtPick: 181.82, side: 'bullish' }), 'bullish')
    expect([standingText(strk), priceText(strk!)]).toEqual(['Against -17.5%', 'Price -17.5%'])
    expect(standingLabel(strk, 'bullish')).toBe('Against this bullish pick: -17.5%. Price down 17.5% since the pick.')
    const short = standing(favourSincePick(3000, { priceAtPick: 2926.83, side: 'bearish' }), 'bearish') // the price rose 2.5%
    expect([standingText(short), priceText(short!)]).toEqual(['Against -2.5%', 'Price +2.5%'])

    const flat = standing(-0.04, 'bearish') // rounds to 0.0: flat, never "-0.0%"
    expect(flat).toEqual({ favour: 0, price: 0, state: 'flat' })
    expect([standingText(flat), priceText(flat!)]).toEqual(['Flat 0.0%', 'Price 0.0%'])
    expect(standingLabel(flat, 'bearish')).toBe('Flat since the pick.')
    expect(standingLabel(standing(2.5, 'bearish'), 'bearish', 'at exit')).toBe('Working for this bearish pick: +2.5%. Price down 2.5% at exit.')

    expect(standing(null, 'bullish')).toBeNull()
    expect(standing(Number.NaN, 'bullish')).toBeNull()
    expect(standingText(null)).toBe('—')
    expect(standingLabel(null, 'bullish')).toBe('Move since the pick unknown.')
    expect(favourSincePick(undefined, { priceAtPick: 100, side: 'bullish' })).toBeNull()
    // Half a step rounds away from zero on both lines, so they never disagree by a rounding step.
    expect(standing(0.25, 'bearish')).toEqual({ favour: 0.3, price: -0.3, state: 'working' })
    expect(standing(-0.25, 'bearish')).toEqual({ favour: -0.3, price: 0.3, state: 'against' })
  })

  it('times the pending 72 h score, and gives up after 7 days as the ledger does', () => {
    const now = Date.parse('2026-10-13T20:00:00Z')
    expect(pendingScore('2026-10-12T16:00:00Z', now)).toBe('Scored Oct 15, 2026, 9:00 AM') // + 72 h, Los Angeles
    expect(pendingScore('2026-10-10T16:00:00Z', now)).toBe('Scored at the next board') // due 4 h ago, not scored yet
    expect(pendingScore('2026-10-10T20:00:00Z', now)).toBe('Scored at the next board') // due right now
    expect(pendingScore('2026-10-06T19:00:00Z', now)).toBe('Not scored')
    expect(pendingScore('not a date', now)).toBe('—')
  })

  it("words each reason by its row's rule: the ledger's exits, and the previous rule's removals", () => {
    const steady = { result72Pct: null }
    const legacy = { legacy: true as const }
    expect(reasonText({ ...steady, reason: 'target_hit' })).toBe('Target hit')
    expect(reasonText({ ...steady, reason: 'invalidated' })).toBe('Invalidated')
    expect(reasonText({ ...steady, reason: 'expired' })).toBe('72 h ended')
    expect(reasonText({ ...legacy, reason: 'target_hit' })).toBe('Target hit')
    expect(reasonText({ ...legacy, reason: 'expired' })).toBe('Validity ended')
    expect(reasonText({ ...legacy, reason: 'score_fell' })).toBe('Score no longer cleared ±20')
    expect(reasonText({ ...legacy, reason: 'outranked' })).toBe('Outranked by stronger picks')
    expect(reasonText({ ...legacy, reason: 'no_data' })).toBe('No fresh data')
    expect(reasonText({ ...legacy, reason: 'left' })).toBe('Left the board')
    expect(reasonText({ reason: 'score_fell' })).toBe('Score no longer cleared ±20') // an API before steady-v1: no score keys
    expect(reasonText({ ...steady, reason: 'left' })).toBe('left') // never sent on a ledger exit: the raw word
    expect(isLegacyRow({ result72Pct: 2.5 })).toBe(false)
    expect(isLegacyRow({ result72Pct: null })).toBe(false)
    expect(isLegacyRow({ legacy: true })).toBe(true)
    expect(isLegacyRow({})).toBe(true)
    expect([0, undefined, 72000, -0.05].map((invalidation) => isLegacyPick({ invalidation: invalidation as number }))).toEqual([true, true, false, false])
  })

  it('words the four verdicts', () => {
    expect(verdictText({ verdict: 'insufficient', scored: 46, blocks: 8 })).toBe('Not enough scored picks yet (46 of 200) to compare with a random pick.')
    // Enough picks but under 30 blocks: it names the blocks, never "230 of 200".
    expect(verdictText({ verdict: 'insufficient', scored: 230, blocks: 26 })).toBe(
      'Not enough time covered yet (26 of 30 three-day blocks) to compare with a random pick.',
    )
    expect(verdictText({ verdict: 'no_difference', scored: 240, blocks: 31 })).toBe('No measurable difference from a random pick.')
    expect(verdictText({ verdict: 'better', scored: 240, blocks: 31 })).toBe('Better than a random pick so far (99% confidence).')
    expect(verdictText({ verdict: 'worse', scored: 240, blocks: 31 })).toBe('Worse than a random pick so far (99% confidence).')
  })

  it('writes the "In testing" paragraph from the replay, and only the claims its numbers support', () => {
    expect(replayText(fx.signalReplay)).toBe(IN_TESTING)
    const up = replayText({ ...fx.signalReplay, excessPct: 0.9, excessLo95: 0.1, excessHi95: 1.7 })
    expect(up).toContain('a difference of +0.90% (95% CI +0.10% to +1.70%), so better than random picks.')
    expect(replayText({ ...fx.signalReplay, excessLo95: -1.5, excessHi95: -0.2 })).toContain('so worse than random picks.')
    const calm = replayText({ ...fx.signalReplay, bullish: { ...fx.signalReplay.bullish, invalidatedFirstPct: 6.2 } })
    expect(calm).toContain('Bullish picks reached their target first 20.3% of the time (random 12.1%) and hit their invalidation first 6.2% of the time (random 8.1%).')
    expect(calm).not.toContain('bigger swings')
  })

  it('floors the board age to hours and minutes', () => {
    const now = Date.parse('2026-10-03T12:00:00Z')
    expect(computedAgo('2026-10-03T08:26:30Z', now)).toBe('3 h 33 min ago')
    expect(computedAgo('2026-10-03T11:00:00Z', now)).toBe('1 h 0 min ago')
    expect(computedAgo('2026-10-03T11:47:59Z', now)).toBe('12 min ago')
    expect(computedAgo('2026-10-03T11:59:30Z', now)).toBe('just now')
    expect(computedAgo('2026-10-03T12:05:00Z', now)).toBe('just now') // a clock a little behind the server's
    expect(pickPrompts({ symbol: 'SOL', side: 'bullish' })).toEqual([
      'Why is SOL on the bullish side of the screen?',
      'What would invalidate this SOL pick?',
      'Size a trade in SOL for my account',
    ])
  })

  it('times the next try and flags a board over 8 h old', () => {
    const now = Date.parse('2026-10-08T22:00:00Z') // 3:00 PM in Los Angeles (the tests' zone)
    expect(nextTry('2026-10-08T22:40:00Z', now)).toBe('next try about 3:40 PM')
    expect(nextTry('2026-10-08T21:59:00Z', now)).toBe('next try shortly') // already due
    expect(nextTry(null, now)).toBe('next try shortly')
    expect(isStale('2026-10-08T13:59:00Z', now)).toBe(true) // 8 h 1 min
    expect(isStale('2026-10-08T14:00:00Z', now)).toBe(false) // exactly 8 h
  })

  it('labels the factors', () => {
    expect(factorChips(fx.signalPick().factors)).toEqual(['30d +12.4%', 'RSI 61', 'Uptrend', 'News bullish'])
    expect(factorChips({ ...fx.signalPick().factors, return30dPct: -3.04, trend: 'range', sentiment: -0.1 })).toEqual([
      '30d -3.0%',
      'RSI 61',
      'Range-bound',
      'News neutral',
    ])
    expect(factorChips({ ...fx.signalPick().factors, trend: 'downtrend', sentiment: null })).toHaveLength(3)
  })
})

describe('SignalsPage', () => {
  it('shows both sides with live prices, whether each pick is working for its side, its levels and when it ends', async () => {
    stubSession()
    vi.spyOn(api, 'signals').mockResolvedValue({ board: fx.signalBoard(), history: [], record: fx.signalRecord() })
    open()
    expect(screen.getByRole('heading', { level: 1, name: 'Signals' })).toBeInTheDocument()
    expect(screen.getByText('Signals board · momentum screen')).toBeInTheDocument()
    expect(
      screen.getByText(
        'Up to 4 picks a side, taken from the assets with the strongest and weakest recent momentum, trend and RSI at the time of the pick. Recomputed every 4 hours. A screen, not a forecast: in testing, its picks did no better than random picks from the same list.',
      ),
    ).toBeInTheDocument()
    const bullish = screen.getByRole('region', { name: /Bullish/ })
    await waitFor(() => expect(within(bullish).getAllByRole('article')).toHaveLength(2))
    expect(bullish).toHaveTextContent('Strongest momentum')
    const bearish = screen.getByRole('region', { name: /Bearish/ })
    expect(within(bearish).getAllByRole('article')).toHaveLength(1)
    expect(bearish).toHaveTextContent('Weakest momentum')
    expect(
      screen.getByText(
        "A pick stays until it reaches its target (2 ATR in its favour), hits its invalidation (2 ATR against it) or 72 hours pass. An asset can't switch sides within 7 days.",
      ),
    ).toHaveClass('text-muted', 'text-[13px]')

    const btc = card('BTC bullish pick')
    await waitFor(() => expect(btc).toHaveTextContent('$84,213.20')) // live, from useAssets
    expect(btc).toHaveTextContent('Bitcoin')
    const btcSince = sinceCell(btc)
    expect(btcSince).toHaveTextContent(/^Working \+5\.3%Price \+5\.3%$/) // no arrow
    expect(within(btcSince).getByText('Working +5.3%')).toHaveClass('text-up')
    expect(within(btcSince).getByText('Price +5.3%')).toHaveClass('text-muted')
    expect(btcSince).toHaveAttribute('aria-label', 'Working for this bullish pick: +5.3%. Price up 5.3% since the pick.')
    expect(btcSince.textContent).not.toMatch(/[▲▼]/)
    // The 24 h change is neutral: green and red on a card only say whether the pick is working.
    expect(within(btc).getByText('24h +2.3%')).toHaveClass('text-muted')
    expect(btc.textContent).not.toMatch(/[▲▼]/)
    expect(btc).toHaveTextContent('Medium vol')
    expect(within(btc).getByText('Ends in 2d 5h')).toHaveAttribute('title', expect.stringMatching(/^Scored 72 h after the pick · /))
    expect(within(btc).getByRole('progressbar', { name: 'Progress to the $88,000.00 target' })).toHaveAttribute('aria-valuenow', '53')
    expect(within(btc).getByText('Invalidation').parentElement).toHaveTextContent('Invalidation$72,000.00')
    expect(within(btc).getByText('Invalidation').parentElement).toHaveAttribute('title', 'The pick ends if the price reaches this level')
    expect(within(btc).getByRole('list', { name: 'Factors now' })).toHaveTextContent('30d +12.4%RSI 61UptrendNews bullish')
    expect(within(btc).getByText('Now:')).toBeInTheDocument()
    expect(btc).toHaveTextContent('ETF inflows lead the wire')
    const links = within(btc).getAllByRole('link', { name: /Example Wire|Desk/ })
    expect(links).toHaveLength(2)
    expect(links[0]).toHaveAttribute('href', 'https://example.com/btc-etf')
    expect(links[0]).toHaveAttribute('rel', 'noopener noreferrer')
    expect(within(btc).getByRole('link', { name: 'Trade BTC' })).toHaveAttribute('href', '/trade/BTC')

    // ETH fell on a bearish pick: it is working, in words; the raw move stays grey.
    const ethSince = sinceCell(card('ETH bearish pick'))
    expect(ethSince).toHaveTextContent(/^Working \+6\.3%Price -6\.3%$/)
    expect(within(ethSince).getByText('Working +6.3%')).toHaveClass('text-up')
    expect(within(ethSince).getByText('Price -6.3%')).toHaveClass('text-muted')
    expect(ethSince.textContent).not.toMatch(/[▲▼]/)
    expect(within(card('ETH bearish pick')).getByRole('progressbar')).toHaveAttribute('aria-valuenow', '50') // 3200 → 3000 of the way to 2800
    expect(within(card('ETH bearish pick')).getByText('Invalidation').parentElement).toHaveTextContent('$3,600.00')

    const sol = card('SOL bullish pick')
    expect(sol).toHaveTextContent('Ended')
    expect(sol).toHaveTextContent('High vol')
    expect(within(sinceCell(sol)).getByText('Flat 0.0%')).toHaveClass('text-muted')
    expect(sinceCell(sol)).toHaveAttribute('aria-label', 'Flat since the pick.')

    expect(screen.getByText(/Board computed 1 h 0 min ago · next refresh about/)).toHaveAttribute('title', expect.stringMatching(/^Momentum screen: /))
    expect(screen.getByText('Simulated · not financial advice')).toBeInTheDocument()
  })

  it('reads a pick against its side as "Against", and an unknown live price as a dash', async () => {
    stubSession()
    vi.spyOn(api, 'signals').mockResolvedValue({
      board: fx.signalBoard({
        bullish: [
          fx.signalPick({ symbol: 'SOL', priceAtPick: 181.82, target: 200, invalidation: 163.6 }), // STRK-like: -17.5% since the pick
          fx.signalPick({ symbol: 'STRK', priceAtPick: 0.0666, target: 0.088, invalidation: 0.04452 }), // no live price in the list
        ],
        bearish: [fx.signalPick({ symbol: 'ETH', side: 'bearish', priceAtPick: 2926.83, target: 2600, invalidation: 3250 })], // up 2.5%
      }),
      history: [],
    })
    open()
    const sol = await screen.findByRole('article', { name: 'SOL bullish pick' })
    await waitFor(() => expect(sinceCell(sol)).toHaveTextContent(/^Against -17\.5%Price -17\.5%$/))
    expect(within(sinceCell(sol)).getByText('Against -17.5%')).toHaveClass('text-down')
    expect(sinceCell(sol)).toHaveAttribute('aria-label', 'Against this bullish pick: -17.5%. Price down 17.5% since the pick.')

    const eth = sinceCell(card('ETH bearish pick'))
    expect(eth).toHaveTextContent(/^Against -2\.5%Price \+2\.5%$/)
    expect(within(eth).getByText('Against -2.5%')).toHaveClass('text-down')

    const strk = card('STRK bullish pick')
    expect(sinceCell(strk)).toHaveTextContent(/^—$/)
    expect(within(sinceCell(strk)).getByText('—')).toHaveClass('text-muted')
    expect(within(strk).getByText('Invalidation').parentElement).toHaveTextContent('Invalidation$0.04452')
    expect(within(strk).getByText(/^24h /)).toHaveTextContent('24h —')
  })

  it('says a board stored before steady-v1 is the previous rule: no invalidation, not scored, gone at the next refresh', async () => {
    stubSession()
    vi.spyOn(api, 'signals').mockResolvedValue({ board: fx.signalBoardLegacy(), history: fx.signalHistoryLegacy, record: fx.signalRecordEmpty() })
    open()
    const btc = await screen.findByRole('article', { name: 'BTC bullish pick' })
    await waitFor(() => expect(sinceCell(btc)).toHaveTextContent('Working +5.3%'))
    expect(within(btc).queryByText('Invalidation')).not.toBeInTheDocument()
    expect(within(btc).getByText(/^Target /)).toBeInTheDocument()
    const badge = within(btc).getByText('Previous rule')
    expect(badge).toHaveClass('text-muted')
    expect(badge.getAttribute('title')).toMatch(/^Valid until [A-Z][a-z]{2} \d{1,2}, \d{4}, .+ under the previous rule: it leaves at the next refresh and is not scored$/)
    expect(btc).not.toHaveTextContent('Ends in')
    expect(screen.getByText(/^This board was made under the previous rule/)).toHaveTextContent(
      "This board was made under the previous rule: its picks have no invalidation level and are not scored, and they leave at the next refresh, which starts the current rule. Under it, a pick stays until it reaches its target (2 ATR in its favour), hits its invalidation (2 ATR against it) or 72 hours pass. An asset can't switch sides within 7 days.",
    )
    expect(screen.getByRole('region', { name: 'Track record' })).toHaveTextContent('No pick has been scored yet.')
  })

  it('renders an older API: no invalidation, its validity as before, and no track record', async () => {
    stubSession()
    const strip = ({ invalidation: _, ...p }: SignalPick) => p as SignalPick // no invalidation key before steady-v1
    const b = fx.signalBoardLegacy()
    vi.spyOn(api, 'signals').mockResolvedValue({ board: { ...b, bullish: b.bullish.map(strip), bearish: b.bearish.map(strip) }, history: [] })
    open()
    const btc = await screen.findByRole('article', { name: 'BTC bullish pick' })
    await waitFor(() => expect(sinceCell(btc)).toHaveTextContent('Working +5.3%'))
    expect(within(btc).queryByText('Invalidation')).not.toBeInTheDocument()
    expect(btc).toHaveTextContent('Ends in 2d 5h') // its board is that API's current rule
    expect(screen.queryByText(/previous rule/i)).not.toBeInTheDocument()
    expect(screen.getByRole('region', { name: 'Recent exits' })).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'Track record' })).not.toBeInTheDocument()
    expect(screen.queryByText(/In testing/)).not.toBeInTheDocument()
  })

  it('hides a zero invalidation level (a pre-steady pick as the new API decodes it)', async () => {
    stubSession()
    vi.spyOn(api, 'signals').mockResolvedValue({ board: fx.signalBoard({ bullish: [fx.signalPick({ invalidation: 0 })], bearish: [] }), history: [] })
    open()
    const btc = await screen.findByRole('article', { name: 'BTC bullish pick' })
    expect(within(btc).queryByText('Invalidation')).not.toBeInTheDocument()
  })

  it('sends a prompt chip to the agent with the symbol as context', async () => {
    stubSession()
    vi.spyOn(api, 'signals').mockResolvedValue({ board: fx.signalBoard(), history: [] })
    const { user } = open()
    const chips = within(await screen.findByRole('list', { name: 'Ask the agent about ETH' })).getAllByRole('button')
    expect(chips.map((c) => c.textContent)).toEqual([
      'Why is ETH on the bearish side of the screen?',
      'What would invalidate this ETH pick?',
      'Size a trade in ETH for my account',
    ])
    await user.click(screen.getByRole('button', { name: 'What would invalidate this ETH pick?' }))
    expect(JSON.parse(screen.getByTestId('agent-state').textContent!)).toEqual({
      prompt: 'What would invalidate this ETH pick?',
      context: { symbol: 'ETH' },
    })
  })

  it('filters the cards by volatility tier', async () => {
    stubSession()
    vi.spyOn(api, 'signals').mockResolvedValue({ board: fx.signalBoard(), history: [] })
    const { user } = open()
    const bullish = screen.getByRole('region', { name: /Bullish/ })
    await waitFor(() => expect(within(bullish).getAllByRole('article')).toHaveLength(2))
    const filter = screen.getByRole('group', { name: 'Volatility' })
    expect(within(filter).getAllByRole('button').map((b) => b.textContent)).toEqual(['All', 'Low vol', 'Medium vol', 'High vol'])
    expect(within(filter).getByRole('button', { name: 'All' })).toHaveAttribute('aria-pressed', 'true')

    await user.click(within(filter).getByRole('button', { name: 'High vol' }))
    expect(within(bullish).getAllByRole('article').map((a) => a.getAttribute('aria-label'))).toEqual(['SOL bullish pick'])
    expect(screen.getByText('No high-volatility picks on this side right now.')).toBeInTheDocument() // ETH is low vol

    await user.click(within(filter).getByRole('button', { name: 'Low vol' }))
    expect(screen.getByRole('article', { name: 'ETH bearish pick' })).toBeInTheDocument()
    expect(within(bullish).queryAllByRole('article')).toHaveLength(0)
    expect(screen.getByText('No low-volatility picks on this side right now.')).toBeInTheDocument()
  })

  it('lists the recent exits with how each left, its 72 h score against a random pick, and the reason', async () => {
    stubSession()
    vi.spyOn(api, 'signals').mockResolvedValue({ board: fx.signalBoard(), history: fx.signalHistory, record: fx.signalRecord() })
    open()
    const exits = await screen.findByRole('region', { name: 'Recent exits' })
    expect(within(exits).getAllByRole('columnheader').map((h) => h.textContent)).toEqual([
      'Asset', 'Side', 'Picked → exit', 'At exit', '72 h result', 'Random pick', 'Reason',
    ])
    const rows = within(exits).getAllByRole('row').slice(1)
    expect(rows).toHaveLength(3)
    const cells = (r: HTMLElement) => within(r).getAllByRole('cell')

    // An invalidated pick still waiting for its 72 h score.
    expect(rows[0]).toHaveTextContent('XRP')
    expect(cells(rows[0])[3]).toHaveTextContent(/^Against -12\.0%Price -12\.0%$/)
    expect(within(cells(rows[0])[3]).getByText('Against -12.0%')).toHaveClass('text-down')
    expect(cells(rows[0])[4]).toHaveTextContent(/^Scored [A-Z][a-z]{2} \d{1,2}, \d{4}, \d{1,2}:\d{2} [AP]M$/)
    expect(cells(rows[0])[4]).toHaveClass('text-muted')
    expect(cells(rows[0])[5]).toHaveTextContent(/^—$/)
    expect(within(rows[0]).getByText('Invalidated')).toHaveClass('text-down')

    expect(rows[1]).toHaveTextContent('AVAX')
    expect(cells(rows[1])[1]).toHaveTextContent('Bullish')
    expect(cells(rows[1])[1]).not.toHaveClass('text-up') // the side is plain text: colour only says for or against
    expect(cells(rows[1])[2]).toHaveTextContent('Sep 29, 2026 → Oct 1, 2026')
    expect(cells(rows[1])[2]).toHaveAttribute('title', '$30.00 → $33.90, target $33.90, invalidation $26.10')
    expect(cells(rows[1])[3]).toHaveTextContent(/^Working \+13\.0%Price \+13\.0%$/)
    expect(cells(rows[1])[4]).toHaveTextContent(/^Working \+9\.8%Price \+9\.8%$/)
    expect(cells(rows[1])[4]).toHaveAttribute('aria-label', 'Working for this bullish pick: +9.8%. Price up 9.8% after 72 hours.')
    expect(cells(rows[1])[5]).toHaveTextContent(/^\+1\.2%$/)
    expect(cells(rows[1])[5]).toHaveClass('text-muted')
    expect(within(rows[1]).getByText('Target hit')).toHaveClass('text-up')

    // A bearish pick whose price fell 2.5%: returnPct +2.5 is already in its direction, so it reads "Working".
    expect(cells(rows[2])[1]).toHaveTextContent('Bearish')
    expect(cells(rows[2])[3]).toHaveTextContent(/^Working \+2\.5%Price -2\.5%$/)
    expect(cells(rows[2])[3]).toHaveAttribute('aria-label', 'Working for this bearish pick: +2.5%. Price down 2.5% at exit.')
    expect(cells(rows[2])[5]).toHaveTextContent(/^-0\.4%$/)
    expect(within(rows[2]).getByText('72 h ended')).toHaveClass('text-muted')
    expect(exits.textContent).not.toMatch(/[▲▼]\s?\d/) // no arrow next to a number
  })

  it("lists a legacy board chain's removals with the reasons those boards recorded, never scored", async () => {
    stubSession()
    vi.spyOn(api, 'signals').mockResolvedValue({ board: fx.signalBoardLegacy(), history: fx.signalHistoryLegacy, record: fx.signalRecordEmpty() })
    open()
    const exits = await screen.findByRole('region', { name: 'Recent exits' })
    const rows = within(exits).getAllByRole('row').slice(1)
    const cells = (r: HTMLElement) => within(r).getAllByRole('cell')
    // Gap round G22's reasons: outranked, no fresh data and "left" (a board stored before reasons) say so, not "score fell".
    expect(rows.map((r) => cells(r)[6].textContent)).toEqual([
      'Target hit', 'Validity ended', 'Outranked by stronger picks', 'Score no longer cleared ±20', 'No fresh data', 'Left the board',
    ])
    expect(within(rows[0]).getByText('Target hit')).toHaveClass('text-up')
    expect(within(rows[2]).getByText('Outranked by stronger picks')).toHaveClass('text-muted')
    for (const r of rows) {
      expect(cells(r)[4]).toHaveTextContent(/^Not scoredPrevious rule$/) // never in the ledger: no 72 h score, ever
      expect(cells(r)[4]).toHaveAttribute('title', 'Picked under the previous rule: never scored at 72 hours, and not in the track record')
      expect(cells(r)[5]).toHaveTextContent(/^—$/)
    }
    expect(cells(rows[1])[3]).toHaveTextContent(/^Against -2\.5%Price \+2\.5%$/) // a bearish pick that rose
    expect(cells(rows[3])[2]).toHaveAttribute('title', '$2.50 → $2.52, target $2.80') // invalidation 0: not named
    expect(exits).toHaveTextContent(
      'Previous rule: a pick of a board made before the current rule. It left for the reason that board recorded, was never scored at 72 hours, and is not in the track record.',
    )
    expect(exits).not.toHaveTextContent('No pick has exited')
  })

  it('lists the ledger exits and the legacy rows together, each with its own rule\'s words', async () => {
    stubSession()
    const steadyExpired = fx.signalHistory[2]
    const legacyExpired = fx.signalHistoryLegacy[1]
    vi.spyOn(api, 'signals').mockResolvedValue({
      board: fx.signalBoard(),
      history: [fx.signalHistory[1], steadyExpired, fx.signalHistoryLegacy[0], legacyExpired],
      record: fx.signalRecord(),
    })
    open()
    const exits = await screen.findByRole('region', { name: 'Recent exits' })
    const rows = within(exits).getAllByRole('row').slice(1)
    const cells = (r: HTMLElement) => within(r).getAllByRole('cell')
    expect(rows.map((r) => cells(r)[6].textContent)).toEqual(['Target hit', '72 h ended', 'Target hit', 'Validity ended'])
    expect(cells(rows[0])[4]).toHaveTextContent(/^Working \+9\.8%Price \+9\.8%$/) // the ledger's 72 h score
    expect(cells(rows[2])[4]).toHaveTextContent(/^Not scoredPrevious rule$/)
    expect(exits).toHaveTextContent('Previous rule: a pick of a board made before the current rule.')
  })

  it("still lists an older API's removals with the old rule's words and no 72 h score", async () => {
    stubSession()
    // An API before steady-v1: no exitAt, levels, 72 h score keys or legacy flag, and no record.
    const older = fx.signalHistoryLegacy.slice(0, 4).map(({ legacy: _, invalidation: __, exitAt: ___, ...r }) => r) as unknown as RemovedPick[]
    vi.spyOn(api, 'signals').mockResolvedValue({ board: fx.signalBoardLegacy(), history: older })
    open()
    const exits = await screen.findByRole('region', { name: 'Recent exits' })
    const rows = within(exits).getAllByRole('row').slice(1)
    const cells = (r: HTMLElement) => within(r).getAllByRole('cell')
    expect(rows.map((r) => cells(r)[6].textContent)).toEqual(['Target hit', 'Validity ended', 'Outranked by stronger picks', 'Score no longer cleared ±20'])
    expect(cells(rows[1])[4]).toHaveTextContent(/^—$/) // the old rule kept no 72 h score
    expect(cells(rows[0])[5]).toHaveTextContent(/^—$/)
    expect(cells(rows[3])[2]).toHaveAttribute('title', '$2.50 → $2.52, target $2.80')
    expect(cells(rows[3])[2].textContent).toMatch(/ → [A-Z][a-z]{2} \d{1,2}, \d{4}$/) // removedAt stands in for exitAt
    expect(exits).not.toHaveTextContent('Previous rule') // nothing says it is the previous rule: on that API it is the rule
    expect(screen.queryByText('Previous rule')).not.toBeInTheDocument()
  })

  it("keeps the ledger's exits when the legacy rows can't be read, and says what is missing", async () => {
    stubSession()
    vi.spyOn(api, 'signals').mockResolvedValue({ board: fx.signalBoard(), history: fx.signalHistory, historyUnavailable: true, record: fx.signalRecord() })
    open()
    const exits = await screen.findByRole('region', { name: 'Recent exits' })
    expect(within(exits).getAllByRole('row')).toHaveLength(fx.signalHistory.length + 1)
    expect(exits).toHaveTextContent("How the previous rule's picks left is unavailable right now.")
    expect(exits).not.toHaveTextContent('Previous rule: a pick')
  })

  it('says "Not scored" for a ledger pick no board scored within 7 days', async () => {
    stubSession()
    const lost = { ...fx.signalHistory[2], pickedAt: '2026-09-01T00:00:00Z', result72Pct: null, random72Pct: null, scoredAt: null }
    vi.spyOn(api, 'signals').mockResolvedValue({ board: fx.signalBoard(), history: [lost] })
    open()
    const exits = await screen.findByRole('region', { name: 'Recent exits' })
    expect(within(exits).getAllByRole('row')[1].querySelectorAll('td')[4]).toHaveTextContent(/^Not scored$/)
  })

  it('shows a reason this build does not know as the raw word', async () => {
    stubSession()
    const unknown = { ...fx.signalHistory[1], reason: 'delisted' } as unknown as (typeof fx.signalHistory)[number]
    vi.spyOn(api, 'signals').mockResolvedValue({ board: fx.signalBoard(), history: [unknown] })
    open()
    const exits = await screen.findByRole('region', { name: 'Recent exits' })
    expect(within(exits).getByText('delisted')).toHaveClass('text-muted')
  })

  it('shows the track record per side with its verdict, and the out-of-sample replay', async () => {
    stubSession()
    vi.spyOn(api, 'signals').mockResolvedValue({ board: fx.signalBoard(), history: fx.signalHistory, record: fx.signalRecord() })
    open()
    const record = await screen.findByRole('region', { name: 'Track record' })
    expect(record).toHaveTextContent(
      'Every pick is scored 72 hours after it was made, whatever happened in between, and compared with a random pick from the same list at the same time. Costs: 0.10% per round trip.',
    )
    expect(record).toHaveTextContent('Since Sep 19, 2026 · 5 picks waiting for the 72-hour score') // midnight UTC, Los Angeles
    expect(record).not.toHaveTextContent('could not be scored')
    expect(within(record).getAllByRole('columnheader').map((h) => h.textContent!.replace(/\s/g, ' '))).toEqual([
      'Picks', 'Scored', 'Moved its way', 'Random', 'Avg 72 h move', 'After costs', 'Random pick', 'Difference (99% CI)', 'Target / invalidated / 72 h ended',
    ])
    const row = (name: string) => within(record).getByRole('rowheader', { name }).closest('tr')!
    expect(within(row('All picks')).getAllByRole('cell').map((c) => c.textContent)).toEqual([
      '46', '47.8%', '49.1%', '-0.31%', '-0.41%', '-0.12%', '-0.19% (-1.62% to +1.24%)', '7 / 6 / 33',
    ])
    expect(within(row('Bullish')).getAllByRole('cell')[6]).toHaveTextContent('+0.21% (-1.90% to +2.32%)')
    expect(within(row('Bearish')).getAllByRole('cell')[0]).toHaveTextContent('21')
    // The verdict sits in the row under each side's figures.
    for (const [name, n] of [['All picks', 46], ['Bullish', 25], ['Bearish', 21]] as const)
      expect(row(name).nextElementSibling).toHaveTextContent(`Not enough scored picks yet (${n} of 200) to compare with a random pick.`)
    expect(within(record).getByRole('heading', { name: 'In testing' }).nextElementSibling).toHaveTextContent(IN_TESTING)
  })

  it('prints the verdict the record earns, and no interval before two blocks', async () => {
    stubSession()
    const r = fx.signalRecord()
    vi.spyOn(api, 'signals').mockResolvedValue({
      board: fx.signalBoard(), history: [],
      record: fx.signalRecord({
        unscored: 1,
        all: { ...r.all, scored: 240, blocks: 31, verdict: 'no_difference' },
        bullish: { ...r.bullish, scored: 130, blocks: 30, excessLo99: 0.12, verdict: 'better' },
        bearish: { ...r.bearish, scored: 2, blocks: 1, excessLo99: null, excessHi99: null, verdict: 'worse' },
      }),
    })
    open()
    const record = await screen.findByRole('region', { name: 'Track record' })
    const row = (name: string) => within(record).getByRole('rowheader', { name }).closest('tr')!
    expect(row('All picks').nextElementSibling).toHaveTextContent('No measurable difference from a random pick.')
    expect(row('Bullish').nextElementSibling).toHaveTextContent('Better than a random pick so far (99% confidence).')
    expect(row('Bearish').nextElementSibling).toHaveTextContent('Worse than a random pick so far (99% confidence).')
    expect(within(row('Bearish')).getAllByRole('cell')[6]).toHaveTextContent(/^-0\.66%$/)
    expect(record).toHaveTextContent('5 picks waiting for the 72-hour score · 1 pick could not be scored')
  })

  it('names the 3-day blocks still missing once a side has its 200 scored picks', async () => {
    stubSession()
    const r = fx.signalRecord()
    vi.spyOn(api, 'signals').mockResolvedValue({
      board: fx.signalBoard(), history: [],
      record: fx.signalRecord({ minBlocks: 30, all: { ...r.all, scored: 231, blocks: 27 } }),
    })
    open()
    const record = await screen.findByRole('region', { name: 'Track record' })
    const all = within(record).getByRole('rowheader', { name: 'All picks' }).closest('tr')!
    expect(all.nextElementSibling).toHaveTextContent(/^Not enough time covered yet \(27 of 30 three-day blocks\) to compare with a random pick\.$/)
    expect(record).not.toHaveTextContent('231 of 200')
  })

  it('says no pick has been scored yet on a new ledger, and keeps the replay box', async () => {
    stubSession()
    vi.spyOn(api, 'signals').mockResolvedValue({ board: fx.signalBoard(), history: [], record: fx.signalRecordEmpty() })
    open()
    const record = await screen.findByRole('region', { name: 'Track record' })
    expect(record).toHaveTextContent('No pick has been scored yet. Picks are scored 72 hours after they are made.')
    expect(within(record).queryByRole('table')).not.toBeInTheDocument()
    expect(within(record).getByRole('heading', { name: 'In testing' }).nextElementSibling).toHaveTextContent(IN_TESTING)
    expect(screen.getByRole('region', { name: 'Recent exits' })).toHaveTextContent('No pick has exited yet under the current rule.')
  })

  it('shows dashes for a side with nothing scored yet', async () => {
    stubSession()
    const r = fx.signalRecordEmpty()
    vi.spyOn(api, 'signals').mockResolvedValue({ board: fx.signalBoard(), history: [], record: { ...r, since: '2026-10-13T08:00:00Z', open: 1 } })
    open()
    const record = await screen.findByRole('region', { name: 'Track record' })
    expect(record).toHaveTextContent('1 pick waiting for the 72-hour score')
    const all = within(record).getByRole('rowheader', { name: 'All picks' }).closest('tr')!
    expect(within(all).getAllByRole('cell').map((c) => c.textContent)).toEqual(['0', '—', '—', '—', '—', '—', '—', '0 / 0 / 0'])
    expect(all.nextElementSibling).toHaveTextContent('Not enough scored picks yet (0 of 200) to compare with a random pick.')
  })

  it('says when the first board is still being computed, and when a side is empty', async () => {
    stubSession()
    const signals = vi.spyOn(api, 'signals').mockResolvedValue({ board: null, history: [], refreshing: true, firstBoard: true })
    const { unmount } = open()
    expect(await screen.findByText('The first board is being computed.')).toBeInTheDocument()
    expect(screen.getByText('Check back in a few minutes.')).toBeInTheDocument()
    unmount()

    signals.mockResolvedValue({ board: fx.signalBoard({ bullish: [], bearish: [] }), history: [], historyUnavailable: false })
    open()
    expect(await screen.findByText('No asset scores 20 or more right now.')).toBeInTheDocument()
    expect(screen.getByText('No asset scores −20 or less right now.')).toBeInTheDocument()
    expect(screen.getByText('No pick has exited yet under the current rule.')).toBeInTheDocument()
    expect(screen.queryByText('Outcomes are unavailable right now.')).not.toBeInTheDocument()
  })

  it("says no pick has exited under the current rule, and that the previous rule's removals can't be read", async () => {
    stubSession()
    vi.spyOn(api, 'signals').mockResolvedValue({ board: fx.signalBoardLegacy(), history: [], historyUnavailable: true, record: fx.signalRecordEmpty() })
    open()
    const removed = await screen.findByRole('region', { name: 'Recent exits' })
    expect(removed).toHaveTextContent(/No pick has exited yet under the current rule\. How the previous rule's picks left is unavailable right now\.$/)
    expect(within(removed).queryAllByRole('row')).toHaveLength(0)
  })

  it('says the last refresh failed and when the next try is, under a stale board, without the pulse', async () => {
    stubSession()
    const old = fx.signalBoard({ id: 'sb0', createdAt: new Date(Date.now() - (9 * 60 + 5) * 60_000).toISOString() })
    const retryAt = new Date(Date.now() + 40 * 60_000).toISOString()
    vi.spyOn(api, 'signals').mockResolvedValue({
      board: old, history: fx.signalHistory, refreshing: true, asOf: old.createdAt, firstBoard: false,
      refresh: { state: 'retrying', failures: 3, lastErrorAt: new Date(Date.now() - 20 * 60_000).toISOString(), retryAt },
    })
    open()
    const status = await screen.findByRole('status')
    expect(status).toHaveTextContent(/^Last refresh failed · next try about \d{1,2}:\d{2} [AP]M$/)
    const footer = status.closest('p')!
    expect(footer).toHaveTextContent(/^Board computed 9 h 5 min ago Stale · Last refresh failed · next try about/)
    expect(within(footer).getByText('Stale')).toHaveAttribute('title', expect.stringMatching(/^Over 8 hours old/))
    expect(footer.querySelector('.animate-pulse')).toBeNull()
    expect(screen.queryByText(/Refreshing · new board in a few minutes/)).not.toBeInTheDocument()
    expect(screen.getByRole('article', { name: 'BTC bullish pick' })).toBeInTheDocument() // the last valid board stays
  })

  it('keeps the refreshing copy while a refresh runs, and no stale tag on a fresh board', async () => {
    stubSession()
    vi.spyOn(api, 'signals').mockResolvedValue({
      board: fx.signalBoard(), history: [], refreshing: true, refresh: { state: 'running', failures: 0, lastErrorAt: null, retryAt: null },
    })
    open()
    expect(await screen.findByRole('status')).toHaveTextContent('Refreshing · new board in a few minutes')
    expect(screen.getByText(/^Board computed 1 h 0 min ago/).querySelector('.animate-pulse')).not.toBeNull()
    expect(screen.queryByText('Stale')).not.toBeInTheDocument()
    expect(screen.queryByText(/Last refresh failed/)).not.toBeInTheDocument()
  })

  it("says the last refresh didn't produce a board, and when the next try is, with none to show", async () => {
    stubSession()
    const retryAt = new Date(Date.now() + 55 * 60_000).toISOString()
    vi.spyOn(api, 'signals').mockResolvedValue({
      board: null, history: [], refreshing: true, asOf: null, firstBoard: true, // a first board that keeps failing reads the same
      refresh: { state: 'retrying', failures: 7, lastErrorAt: new Date().toISOString(), retryAt },
    })
    open()
    expect(await screen.findByRole('heading', { name: "The last refresh didn't produce a board" })).toBeInTheDocument()
    expect(screen.getByText(/^Next try about \d{1,2}:\d{2} [AP]M\. Picks appear here on their own\.$/)).toBeInTheDocument()
    expect(screen.queryByText(/Refreshing the board/)).not.toBeInTheDocument()
    expect(screen.queryByText('The first board is being computed.')).not.toBeInTheDocument()
  })

  it('keeps showing the last valid board while a new one is computed, and polls every 30 s until it arrives', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      stubSession()
      const old = fx.signalBoard({ id: 'sb0', createdAt: new Date(Date.now() - 235 * 60_000).toISOString() })
      const signals = vi.spyOn(api, 'signals').mockResolvedValue({ board: old, history: fx.signalHistory, refreshing: true, asOf: old.createdAt, firstBoard: false })
      open()
      expect(await screen.findByRole('status')).toHaveTextContent('Refreshing · new board in a few minutes')
      expect(screen.getByText(/^Board computed 3 h 55 min ago/)).toHaveTextContent(/^Board computed 3 h 55 min ago Refreshing · new board in a few minutes$/)
      expect(screen.queryByText(/next refresh about/)).not.toBeInTheDocument()
      expect(screen.getByRole('article', { name: 'BTC bullish pick' })).toBeInTheDocument()
      expect(screen.getAllByRole('row')).toHaveLength(fx.signalHistory.length + 1) // the removed picks stay too
      expect(signals).toHaveBeenCalledTimes(1) // switching to the 30 s poll does not fetch again at once

      await vi.advanceTimersByTimeAsync(30_000)
      expect(signals).toHaveBeenCalledTimes(2)
      signals.mockResolvedValue({ board: fx.signalBoard({ bearish: [] }), history: fx.signalHistory, refreshing: false })
      await vi.advanceTimersByTimeAsync(30_000)
      expect(signals).toHaveBeenCalledTimes(3)
      expect(await screen.findByText(/^Board computed 1 h 0 min ago · next refresh about/)).toBeInTheDocument()
      expect(screen.queryByRole('status')).not.toBeInTheDocument()
      expect(screen.queryByRole('article', { name: 'ETH bearish pick' })).not.toBeInTheDocument() // the fresh board

      await vi.advanceTimersByTimeAsync(30_000)
      expect(signals).toHaveBeenCalledTimes(3) // back to once a minute
      await vi.advanceTimersByTimeAsync(30_000)
      expect(signals).toHaveBeenCalledTimes(4)
    } finally {
      vi.useRealTimers()
    }
  })

  it('drops a slow read answered after a newer one, so the old board never comes back', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      stubSession()
      const old = fx.signalBoard({ id: 'sb0', createdAt: new Date(Date.now() - 235 * 60_000).toISOString() })
      const stale = { board: old, history: fx.signalHistory, refreshing: true, asOf: old.createdAt, firstBoard: false }
      let answerSlow!: (r: typeof stale) => void
      let failSlow!: (e: Error) => void
      const signals = vi.spyOn(api, 'signals')
        .mockResolvedValueOnce(stale)
        .mockReturnValueOnce(new Promise((resolve) => (answerSlow = resolve)))
        .mockReturnValueOnce(new Promise((_, reject) => (failSlow = reject)))
        .mockResolvedValue({ board: fx.signalBoard({ bearish: [] }), history: fx.signalHistory, refreshing: false })
      open()
      expect(await screen.findByRole('status')).toHaveTextContent('Refreshing · new board in a few minutes')

      await vi.advanceTimersByTimeAsync(90_000) // three reads while refreshing; the third answers first
      expect(signals).toHaveBeenCalledTimes(4)
      expect(await screen.findByText(/^Board computed 1 h \d+ min ago · next refresh about/)).toBeInTheDocument()

      answerSlow(stale)
      failSlow(new ApiError(0, 'network', 'offline'))
      await act(() => vi.advanceTimersByTimeAsync(0))
      expect(screen.getByText(/^Board computed 1 h \d+ min ago · next refresh about/)).toBeInTheDocument()
      expect(screen.queryByText('Refreshing · new board in a few minutes')).not.toBeInTheDocument()
      expect(screen.queryByText("Couldn't refresh · retrying")).not.toBeInTheDocument()
      expect(screen.queryByRole('article', { name: 'ETH bearish pick' })).not.toBeInTheDocument()
      await vi.advanceTimersByTimeAsync(30_000)
      expect(signals).toHaveBeenCalledTimes(4) // settled: once a minute again, not the 30 s refreshing poll
    } finally {
      vi.useRealTimers()
    }
  })

  it('says the board is refreshing, not "first board", when none validates', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      stubSession()
      const signals = vi.spyOn(api, 'signals').mockResolvedValue({ board: null, history: [], refreshing: true, asOf: null, firstBoard: false })
      open()
      expect(await screen.findByRole('heading', { name: 'Refreshing the board · usually 2–3 minutes' })).toBeInTheDocument()
      expect(screen.queryByText('The first board is being computed.')).not.toBeInTheDocument()
      expect(screen.queryByRole('group', { name: 'Volatility' })).not.toBeInTheDocument()

      // An API without the flags reads the same: only an explicit firstBoard says "first board".
      signals.mockResolvedValue({ board: null, history: [] })
      await vi.advanceTimersByTimeAsync(30_000)
      expect(signals).toHaveBeenCalledTimes(2)
      expect(screen.getByRole('heading', { name: 'Refreshing the board · usually 2–3 minutes' })).toBeInTheDocument()

      signals.mockResolvedValue({ board: fx.signalBoard(), history: [], refreshing: false })
      await vi.advanceTimersByTimeAsync(30_000)
      expect(await screen.findByRole('article', { name: 'BTC bullish pick' })).toBeInTheDocument()
      expect(screen.queryByRole('heading', { name: /Refreshing the board/ })).not.toBeInTheDocument()
    } finally {
      vi.useRealTimers()
    }
  })

  it('offers a retry when the board cannot load, and polls every minute', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      stubSession()
      const signals = vi.spyOn(api, 'signals').mockRejectedValueOnce(new ApiError(502, 'upstream', 'Market data is unavailable.'))
      const { user } = open()
      expect(await screen.findByText('Market data is unavailable.')).toBeInTheDocument()
      signals.mockResolvedValue({ board: fx.signalBoard(), history: [] })
      await user.click(screen.getByRole('button', { name: 'Try again' }))
      expect(await screen.findByRole('article', { name: 'BTC bullish pick' })).toBeInTheDocument()

      signals.mockRejectedValueOnce(new ApiError(0, 'network', 'offline'))
      await vi.advanceTimersByTimeAsync(60_000)
      expect(await screen.findByText("Couldn't refresh · retrying")).toBeInTheDocument()
      expect(screen.getByRole('article', { name: 'BTC bullish pick' })).toBeInTheDocument() // the last board stays
      expect(signals).toHaveBeenCalledTimes(3)
    } finally {
      vi.useRealTimers()
    }
  })

  it('reads GET /api/signals', async () => {
    stubSession()
    const fetch = vi.fn((url: string) => Promise.resolve(json(url.endsWith('/api/signals') ? { board: fx.signalBoard(), history: fx.signalHistory } : {})))
    vi.stubGlobal('fetch', fetch)
    open()
    expect(await screen.findByRole('article', { name: 'ETH bearish pick' })).toBeInTheDocument()
    expect(fetch).toHaveBeenCalledWith(expect.stringMatching(/\/api\/signals$/), expect.objectContaining({ method: 'GET', credentials: 'include' }))
  })
})

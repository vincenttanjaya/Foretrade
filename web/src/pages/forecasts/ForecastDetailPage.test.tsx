import { act, cleanup, screen, waitFor, within } from '@testing-library/react'
import { useLocation } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError } from '../../lib/api'
import type { Forecast, ForecastResult, MarketOdds } from '../../lib/types'
import { fmtDate } from '../../lib/format'
import * as fx from '../../test/fixtures'
import { currentPath, renderPage, stubSession } from '../../test/utils'
import { ForecastDetailPage } from './ForecastDetailPage'

const open = (id = 'f1234567-aaaa') =>
  renderPage(<ForecastDetailPage />, {
    route: `/forecasts/${id}`,
    path: '/forecasts/:id',
    routes: { '/forecasts': <p>forecast list</p>, '/forecasts/new': <p>new forecast</p> },
  })

const show = (f: Forecast) => vi.spyOn(api, 'forecast').mockResolvedValue({ forecast: f })

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(new Date(2026, 9, 2, 12))
  stubSession()
})
afterEach(() => vi.useRealTimers())

describe('ForecastDetailPage · completed binary report', () => {
  it('reads an archived report without mutating actions and restores it without running AI', async () => {
    show(fx.forecast({ status: 'archived', archivedFromStatus: 'completed' }))
    const restore = vi.spyOn(api, 'restoreForecast').mockResolvedValue({ forecast: fx.forecast() })
    const { user } = open()
    expect(await screen.findByText('Probability estimate · Yes')).toBeInTheDocument()
    expect(screen.getByText(/This forecast is archived/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Export PDF' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Archive' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Update forecast/ })).not.toBeInTheDocument()
    expect(screen.queryByRole('group', { name: 'What happened' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Discuss a trade/ })).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Restore forecast' }))
    expect(restore).toHaveBeenCalledWith('f1234567-aaaa')
    expect(await screen.findByRole('button', { name: 'Archive' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Restore forecast' })).not.toBeInTheDocument()
  })

  it('explains an archived interrupted run and offers restore again after an error', async () => {
    show(fx.forecast({ status: 'archived', archivedFromStatus: 'running', result: null, completedAt: null }))
    vi.spyOn(api, 'restoreForecast').mockRejectedValue(new ApiError(503, 'unavailable', 'Try restoring later'))
    const { user } = open()
    expect(await screen.findByText(/Archived before the run finished/)).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'Specialists at work' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Run again/ })).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Restore forecast' }))
    expect(await screen.findByText('Try restoring later')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Restore forecast' })).toBeEnabled()
  })

  it('leads with the Yes probability estimate and the desk summary', async () => {
    show(fx.forecast())
    open()
    expect(await screen.findByRole('heading', { level: 1, name: fx.forecast().question })).toBeInTheDocument()
    expect(screen.getByRole('navigation', { name: 'Breadcrumb' })).toHaveTextContent('Forecast desk/Crypto/#f1234567')
    expect(screen.getByText('Probability estimate · Yes').nextSibling).toHaveTextContent('62%')
    // Scored against resolved outcomes (the line under the hero), never presented as calibrated.
    expect(screen.getByText(/AI probability estimates, scored against resolved outcomes in the/)).toHaveTextContent(
      'AI probability estimates, scored against resolved outcomes in the Track record on the Forecasts page.',
    )
    expect(screen.getByRole('link', { name: 'Track record' })).toHaveAttribute('href', '/forecasts')
    expect(screen.queryByText(/calibrated/i)).not.toBeInTheDocument()
    expect(screen.getByRole('img', { name: 'Yes 62%, No 38%' })).toBeInTheDocument()
    expect(screen.getByText('YES 62%')).toBeInTheDocument()
    expect(screen.getByText('NO 38%')).toBeInTheDocument()
    expect(screen.getByText('ETH has the edge on flows and seasonality.')).toBeInTheDocument()
    expect(screen.getAllByText('medium confidence').length).toBeGreaterThan(0)
    expect(screen.getByText(/Resolves Dec 31, 2026 · in 90 days/)).toBeInTheDocument()
  })

  it('shows the specialists in desk order with their own odds', async () => {
    show(fx.forecast())
    open()
    const section = (await screen.findByRole('heading', { name: 'How the desk got there' })).closest('section')!
    const names = within(section).getAllByRole('heading', { level: 3 }).map((h) => h.textContent)
    expect(names).toEqual(['Base-rate statistician', 'Narrative & macro skeptic'])
    expect(section).toHaveTextContent('60%yes')
    expect(within(section).getByText('ETH beat BTC in 6 of the last 10 Q4s.')).toBeInTheDocument()
    expect(within(section).getByText('Seasonality')).toBeInTheDocument()
  })

  it('lists drivers, evidence with sources, flags, ensemble vs final and what would change', async () => {
    show(fx.forecast())
    open()
    await screen.findByText('What moves the number')
    expect(screen.getByText('ETF inflows').closest('li')).toHaveTextContent('▲Raises:ETF inflowshigh weight')
    expect(screen.getByText('Rising yields').closest('li')).toHaveTextContent('Lowers:')

    const evidence = screen.getByText('ETH ETFs took in $1.2bn in September').closest('li')!
    expect(evidence).toHaveTextContent('Supports')
    expect(evidence).toHaveTextContent('S1')
    expect(within(evidence).getByRole('link', { name: /ETH ETF flows hit a record/ })).toHaveAttribute('href', 'https://a.io/eth')
    expect(evidence).toHaveTextContent('as of Sep 30, 2026')
    const unsourced = screen.getByText('Funding is elevated').closest('li')!
    expect(unsourced).toHaveTextContent('Opposes')
    expect(unsourced).toHaveTextContent('Coinglass')
    expect(unsourced).toHaveTextContent('as of Oct 2, 2026, 1:02 AM') // "live" = the run time

    expect(screen.getByRole('note')).toHaveTextContent('Stale dataOne source is a week old.')
    expect(screen.getByText('Ensemble vs final').closest('.card')).toHaveTextContent('55% → 62%▲7pp')
    expect(screen.getByText('A hawkish Fed surprise')).toBeInTheDocument()
    expect(screen.getByText(fx.forecastResult.knowledgeNote)).toBeInTheDocument()
    expect(screen.getByText('90 days remaining')).toBeInTheDocument()
  })

  it('shows the web research pack', async () => {
    show(fx.forecast())
    open()
    expect(await screen.findByText('Live web research · 2 sources · 1 read')).toBeInTheDocument()
    const queries = screen.getByRole('list', { name: 'Search queries' })
    expect(within(queries).getAllByRole('listitem').map((li) => li.textContent)).toEqual(['Searched:', 'eth etf flows', 'eth btc ratio'])
    const s1 = screen.getAllByRole('link', { name: /ETH ETF flows hit a record/ }).at(-1)!.closest('li')!
    expect(s1).toHaveTextContent('The Block · Sep 29, 2026') // shown in Los Angeles time
    expect(within(s1).getByTitle('The desk read the full article')).toHaveTextContent('Read')
  })

  it('says when the research found nothing and when nothing is flagged', async () => {
    show(fx.forecast({ result: { ...fx.forecastResult, sources: [], flags: [], ensemble: [] } }))
    open()
    expect(await screen.findByText('The searches returned no usable sources, so the desk worked from model knowledge and market data.')).toBeInTheDocument()
    expect(screen.getByText('No conflicting, stale or duplicate signals flagged.')).toBeInTheDocument()
    expect(screen.queryByText('Ensemble vs final')).toBeNull()
  })

  it('never links a URL the model wrote as evidence source text', async () => {
    const evidence = [
      { claim: 'Injected claim', source: 'https://evil.example/login', stance: 'supports' as const, asOf: 'live', url: null },
      { claim: 'Bare claim', source: '', stance: 'neutral' as const, asOf: 'live', url: null },
    ]
    show(fx.forecast({ result: { ...fx.forecastResult, evidence } }))
    open()
    const injected = (await screen.findByText('Injected claim')).closest('li')!
    expect(injected).toHaveTextContent('https://evil.example/login') // shown as inert text
    expect(within(injected).queryByRole('link')).toBeNull()
    expect(screen.queryByRole('link', { name: /evil/ })).toBeNull()
    expect(screen.getByText('Bare claim').closest('li')).toHaveTextContent('Unsourced')
  })

  it('collapses long reasoning', async () => {
    const long = 'Reasoning. '.repeat(40)
    show(fx.forecast({ result: { ...fx.forecastResult, specialists: [{ ...fx.forecastResult.specialists[0], reasoning: long }] } }))
    const { user } = open()
    await user.click(await screen.findByRole('button', { name: 'Read full reasoning' }))
    expect(screen.getByRole('button', { name: 'Show less' })).toHaveAttribute('aria-expanded', 'true')
  })

  it('handles a completed run with no report', async () => {
    show(fx.forecast({ result: null }))
    open()
    expect(await screen.findByRole('heading', { name: 'Result unavailable.' })).toBeInTheDocument()
  })
})

describe('ForecastDetailPage · desk vs market, context and export', () => {
  const market = () => screen.getByRole('heading', { name: 'Desk vs market' }).closest('section')!
  const withMarket = (m: Partial<MarketOdds> | null, over: Partial<Forecast> = {}) =>
    fx.forecast({ result: { ...fx.forecastResult, market: m && { ...fx.marketOdds, ...m } }, ...over })

  it('compares the desk with an exact Polymarket match: edge, EV and lean', async () => {
    show(withMarket({}))
    open()
    await screen.findByRole('heading', { name: 'Desk vs market' })
    const card = market()
    expect(card).toHaveTextContent('Desk 62%')
    expect(card).toHaveTextContent('Market 55% · Polymarket · as of Oct 2, 14:05 UTC · $1.2M volume')
    expect(card).toHaveTextContent('Edge +7 pts')
    expect(card).toHaveTextContent('EV +$0.07 per $1 Yes contract')
    expect(within(card).getByText('Edge on Yes')).toBeInTheDocument()
    expect(card).toHaveTextContent('Matched: “ETH outperforms BTC in Q4 2026?”')
    const link = within(card).getByRole('link', { name: /View on Polymarket/ })
    expect(link).toHaveAttribute('href', 'https://polymarket.com/event/eth-vs-btc-q4-2026')
    expect(link).toHaveAttribute('target', '_blank')
    expect(link).toHaveAttribute('rel', 'noopener noreferrer')
    expect(card).not.toHaveTextContent('Close match')
    expect(card).toHaveTextContent('Paper positions only: each contract pays $1 if its side wins when this forecast resolves. Nothing is sent to Polymarket.')
  })

  it('shows the No side, a wait and a close match honestly', async () => {
    show(withMarket({ yesPrice: 0.7, edgePp: -8, evYes: -0.08, evNo: 0.08, signal: 'no', match: 'close', volumeUsd: 50_000 }))
    const { unmount } = open()
    await screen.findByRole('heading', { name: 'Desk vs market' })
    expect(market()).toHaveTextContent('Market 70% · Polymarket · as of Oct 2, 14:05 UTC · $50K volume')
    expect(market()).toHaveTextContent('Edge −8 pts')
    expect(market()).toHaveTextContent('EV +$0.08 per $1 No contract')
    expect(within(market()).getByText('Edge on No')).toBeInTheDocument()
    expect(market()).toHaveTextContent("Close match: the market's wording or resolution time differs — read its rules.")
    unmount()

    show(withMarket({ yesPrice: 0.59, edgePp: 3, evYes: 0.03, evNo: -0.03, signal: 'wait' }))
    open()
    await screen.findByRole('heading', { name: 'Desk vs market' })
    expect(within(market()).getByText('Wait')).toBeInTheDocument()
    expect(market()).toHaveTextContent('Edge +3 pts')
  })

  it('never reads an edge from a thin market: flagged, or derived for an older result', async () => {
    show(withMarket({ yesPrice: 0.3, volumeUsd: 65.7, edgePp: 32, evYes: 0.32, evNo: -0.32, signal: 'wait', thin: true }))
    const first = open()
    await screen.findByRole('heading', { name: 'Desk vs market' })
    expect(within(market()).getByText('Thin market')).toBeInTheDocument()
    expect(market()).toHaveTextContent('Only $66 traded on this market — too thin to read an edge.')
    expect(market()).toHaveTextContent('Edge +32 pts') // still shown, muted
    expect(within(market()).getByText('Edge +32 pts')).toHaveClass('text-muted')
    expect(market()).not.toHaveTextContent(/Edge on (Yes|No)/)
    first.unmount()

    // An older result: no thin flag, a lean of yes on $65.70 of volume.
    show(withMarket({ yesPrice: 0.55, volumeUsd: 65.7, signal: 'yes', thin: undefined }))
    const legacy = open()
    await screen.findByRole('heading', { name: 'Desk vs market' })
    expect(within(market()).getByText('Thin market')).toBeInTheDocument()
    expect(market()).not.toHaveTextContent(/Edge on (Yes|No)/)
    legacy.unmount()

    show(withMarket({ volumeUsd: null, signal: 'no', thin: undefined }))
    open()
    await screen.findByRole('heading', { name: 'Desk vs market' })
    expect(market()).toHaveTextContent('Volume unknown — too thin to read an edge.')
    expect(market()).not.toHaveTextContent('Edge on No')
  })

  it('says when no market matches (binary and categorical), and hides the card for replays', async () => {
    vi.spyOn(api, 'refreshMarket').mockRejectedValue(new ApiError(0, 'network', 'offline'))
    show(withMarket(null))
    const { unmount } = open()
    expect(await screen.findByText('No matching Polymarket market found.')).toBeInTheDocument()
    expect(within(market()).getByRole('button', { name: 'Search again' })).toBeInTheDocument()
    unmount()

    // A categorical result whose market names no option predates option markets: nothing to compare.
    const outcomes = ['Fed', 'ECB']
    show(withMarket({}, { shape: 'categorical', outcomes, result: { ...fx.forecastResult, probabilities: [{ outcome: 'Fed', p: 0.6 }, { outcome: 'ECB', p: 0.4 }] } }))
    const other = open()
    await screen.findByText('Most likely outcome')
    expect(within(market()).getByText('No matching Polymarket market found.')).toBeInTheDocument()
    other.unmount()

    show(fx.replayForecast())
    open('r1234567-bbbb')
    await screen.findByText(/^Historical replay as of Aug/)
    expect(screen.queryByRole('heading', { name: 'Desk vs market' })).toBeNull()
    expect(screen.queryByText('No matching Polymarket market found.')).toBeNull()
  })

  it("compares a categorical report's top option with that option's market", async () => {
    const outcomes = ['Nvidia', 'Apple', 'Another company']
    const probabilities = [{ outcome: 'Nvidia', p: 0.86 }, { outcome: 'Apple', p: 0.1 }, { outcome: 'Another company', p: 0.04 }]
    const m = { ...fx.marketOdds, outcome: 'Nvidia', yesPrice: 0.85, edgePp: 1, evYes: 0.01, evNo: -0.01, signal: 'wait' as const,
      question: 'Will NVIDIA be the largest company in the world by market cap on December 31?' }
    show(fx.forecast({ shape: 'categorical', outcomes, result: { ...fx.forecastResult, probabilities, market: m } }))
    open()
    await screen.findByRole('heading', { name: 'Desk vs market' })
    expect(market()).toHaveTextContent('Desk · Nvidia 86%')
    expect(market()).toHaveTextContent('Market · Nvidia 85%')
    expect(market()).toHaveTextContent('Edge +1 pts')
    expect(market()).toHaveTextContent('Matched: “Will NVIDIA be the largest company in the world by market cap on December 31?”')
  })

  it("shows the user's context as S0 in the research card, clipped to 300 characters", async () => {
    const context = 'Our OTC desk saw heavy bids. '.repeat(15).trim()
    show(fx.forecast({ context }))
    const { user } = open()
    const block = (await screen.findByText(/Your context/)).parentElement!
    expect(block).toHaveTextContent('S0Your context · unverified, checked as a claim')
    expect(within(block).getByText(`${context.slice(0, 300).trimEnd()}…`)).toBeInTheDocument()
    await user.click(within(block).getByRole('button', { name: 'Show all' }))
    expect(within(block).getByText(context)).toBeInTheDocument()
    expect(within(block).getByRole('button', { name: 'Show less' })).toHaveAttribute('aria-expanded', 'true')
  })

  it('shows a short context whole, even when the run found no web sources', async () => {
    show(fx.forecast({ context: 'Short note.', result: { ...fx.forecastResult, sources: [], research: { queries: [] } } }))
    open()
    expect(await screen.findByText('Short note.')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Show all' })).toBeNull()
  })

  it('states the panel, the specialist spread and exports the report to PDF', async () => {
    const print = vi.spyOn(window, 'print').mockImplementation(() => {})
    show(fx.forecast())
    const { user } = open()
    expect(await screen.findByText('Panel: 2 specialists + aggregator · research: Brave Search · 2 sources · 1 read')).toBeInTheDocument() // the fixture's panel ran 2
    // Skeptic 48% vs base rate 60% on Yes.
    expect(screen.getByText(/^Specialist spread/)).toHaveTextContent('Specialist spread 12 pts — moderate disagreement')
    await user.click(screen.getByRole('button', { name: 'Export PDF' }))
    expect(print).toHaveBeenCalledOnce()
    expect(screen.getByRole('button', { name: 'Export PDF' }).parentElement).toHaveClass('print:hidden')
  })

  it('offers no export before there is a report', async () => {
    show(fx.forecast({ status: 'running', result: null }))
    open()
    await screen.findByRole('heading', { name: 'Specialists at work' })
    expect(screen.queryByRole('button', { name: 'Export PDF' })).toBeNull()
    expect(screen.getByText('Panel: 3 specialists + aggregator')).toBeInTheDocument() // no research claim yet
  })
})

describe('ForecastDetailPage · research provenance', () => {
  const panel = (label: string) => `Panel: 2 specialists + aggregator · research: ${label}` // the fixture's 2 specialists
  const headlines = (n: number) =>
    Array.from({ length: n }, (_, i) => ({ id: `S${i + 1}`, title: `Headline ${i + 1}`, url: `https://news.io/${i}`, source: 'Wire', publishedAt: null, read: false }))

  it('labels the header by what research actually ran', async () => {
    const cases: [Forecast, string][] = [
      [fx.forecast({ result: { ...fx.forecastResult, research: { queries: ['q'], status: 'live' } } }), 'Brave Search · 2 sources · 1 read'],
      [fx.forecast({ result: { ...fx.forecastResult, sources: headlines(6), research: { queries: [], status: 'wire', note: fx.wireNote } } }), 'stored news wire · 6 headlines'],
      [fx.forecast({ result: { ...fx.forecastResult, sources: headlines(5), research: { queries: [], status: 'carried', note: 'Old.' } } }), "previous run's sources · 5"],
      [fx.replayForecast({ result: { ...fx.forecastResult, sources: headlines(7), research: { queries: ['q'], status: 'replay' } } }), 'dated headlines · 7'],
      [fx.forecast({ result: { ...fx.forecastResult, sources: [], research: { queries: [], status: 'none', note: 'Nothing.' } } }), 'none available'],
      // An older result: no status, derived.
      [fx.forecast({ result: { ...fx.forecastResult, sources: headlines(1).map((s) => ({ ...s, read: true })) } }), 'Brave Search · 1 source · 1 read'],
      [fx.forecast({ result: { ...fx.forecastResult, sources: [], research: { queries: [] } } }), 'none available'],
      [fx.replayForecast({ result: { ...fx.forecastResult, sources: headlines(8) } }), 'dated headlines · 8'],
    ]
    for (const [f, label] of cases) {
      show(f)
      const { unmount } = open(f.id)
      expect(await screen.findByText(panel(label))).toBeInTheDocument()
      unmount()
    }
  })

  it('names the stored wire in the research card and explains an empty pack with its note', async () => {
    show(fx.forecast({ result: { ...fx.forecastResult, sources: headlines(2), research: { queries: [], status: 'wire', note: fx.wireNote } } }))
    const { unmount } = open()
    expect(await screen.findByText('Stored news wire · 2 sources')).toBeInTheDocument()
    unmount()
    show(fx.forecast({ result: { ...fx.forecastResult, sources: [], research: { queries: ['fed cut odds'], status: 'none', note: 'Live search found no usable sources and the stored wire had nothing on this question.' } } }))
    open()
    expect(await screen.findByText('Live web research · 0 sources')).toBeInTheDocument()
    expect(screen.getByText('Live search found no usable sources and the stored wire had nothing on this question.')).toBeInTheDocument()
    expect(screen.queryByText(/The searches returned no usable sources/)).toBeNull()
  })

  it('shows one banner per flag type, with each distinct note once', async () => {
    const flags: ForecastResult['flags'] = [
      { type: 'low_information', note: 'Live search found no usable sources and the stored wire had nothing on this question.' },
      { type: 'stale', note: 'One source is a week old.' },
      { type: 'low_information', note: '  live search found no usable sources and the stored  wire had nothing on this question. ' },
      { type: 'low_information', note: 'Skeptic unavailable for this run; the forecast combines 2 of 3 specialists.' },
    ]
    show(fx.forecast({ result: { ...fx.forecastResult, flags } }))
    open()
    await screen.findByText('What moves the number')
    const notes = screen.getAllByRole('note')
    expect(notes).toHaveLength(2)
    expect(notes[0]).toHaveTextContent(
      'Low informationLive search found no usable sources and the stored wire had nothing on this question.Skeptic unavailable for this run; the forecast combines 2 of 3 specialists.',
    )
    expect(within(notes[0]).getAllByText(/./, { selector: 'p' })).toHaveLength(2)
    expect(notes[1]).toHaveTextContent('Stale dataOne source is a week old.')
  })
})

describe('ForecastDetailPage · deep research', () => {
  it('shows the evidence base after the specialists, the deep research line and the angles', async () => {
    show(fx.deepForecast())
    const { user } = open('d1234567-cccc')
    expect(await screen.findByText('Panel: 2 specialists + aggregator · research: deep · 10 sources · 7 read')).toBeInTheDocument()
    const section = screen.getByRole('region', { name: 'What the research found' })
    expect(section).toHaveTextContent('Evidence base · 14')
    const specialists = screen.getByRole('heading', { name: 'How the desk got there' })
    expect(specialists.compareDocumentPosition(section) & Node.DOCUMENT_POSITION_FOLLOWING, 'after the specialists').toBeTruthy()
    expect(section.compareDocumentPosition(screen.getByText('What moves the number')) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()

    expect(screen.getByText('Deep research · 2 rounds · 17 searches · 10 sources · 7 read')).toBeInTheDocument()
    expect(screen.queryByRole('list', { name: 'Search queries' }), 'the angles replace the flat list').toBeNull()
    const angles = within(screen.getByRole('list', { name: 'Research angles' })).getAllByRole('listitem').filter((li) => li.parentElement?.getAttribute('aria-label') === 'Research angles')
    expect(angles.map((li) => li.querySelector('summary')!.textContent)).toEqual([
      'Comparables · Recent SGX mainboard debuts: opening vs offer4 findings',
      'Primary sources · Prospectus terms and the opening auction5 findings',
      'Drivers · Semiconductor materials backdrop2 findings',
      'Local market · Singapore market and IPO sentiment3 findings',
    ])
    const details = angles[0].querySelector('details')!
    expect(details.open).toBe(false)
    await user.click(within(angles[0]).getByText('Comparables'))
    expect(details.open).toBe(true)
    expect(within(screen.getByRole('list', { name: 'Queries for Recent SGX mainboard debuts: opening vs offer' })).getAllByRole('listitem').map((li) => li.textContent)).toEqual(
      fx.deepAngles[0].queries,
    )

    expect(screen.getByText('Key evidence · 2')).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'What the forecast rests on' })).toBeInTheDocument()
    expect(screen.queryByText('What the desk looked at')).toBeNull()
  })

  it('puts What to watch above What would change our mind', async () => {
    show(fx.deepForecast())
    open('d1234567-cccc')
    const watch = await screen.findByText('What to watch')
    expect(watch.compareDocumentPosition(screen.getByText('What would change our mind')) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(screen.getByText('Final offer price and prospectus terms')).toBeInTheDocument()
  })

  it('renders an older result as before: no evidence base, the flat queries and the evidence trail', async () => {
    const { findings: _f, watch: _w, ...old } = fx.deepForecastResult
    show(fx.deepForecast({ result: { ...old, research: { queries: ['Orchid Materials SGX IPO'], status: 'live' } } }))
    open('d1234567-cccc')
    expect(await screen.findByText('Live web research · 10 sources · 7 read')).toBeInTheDocument()
    expect(screen.getByText(/research: Brave Search · 10 sources · 7 read$/)).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'What the research found' })).toBeNull()
    expect(screen.queryByText('What to watch')).toBeNull()
    expect(within(screen.getByRole('list', { name: 'Search queries' })).getAllByRole('listitem').map((li) => li.textContent)).toEqual(['Searched:', 'Orchid Materials SGX IPO'])
    expect(screen.getByText('Evidence trail · 2')).toBeInTheDocument()
  })

  it('keeps the wire card for a deep run that fell back to the stored wire', async () => {
    show(fx.deepForecast({ result: { ...fx.deepForecastResult, findings: [], research: { ...fx.deepForecastResult.research, status: 'wire', note: fx.wireNote } } }))
    open('d1234567-cccc')
    expect(await screen.findByText('Stored news wire · 10 sources · 7 read')).toBeInTheDocument()
    expect(screen.getByRole('list', { name: 'Search queries' })).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'What the research found' })).toBeNull()
  })
})

describe('ForecastDetailPage · ordered report', () => {
  it('names the most likely range and draws the distribution', async () => {
    const outcomes = ['< $80k', '$80k – $100k', '$100k – $120k', '> $120k']
    show(
      fx.forecast({
        shape: 'ordered',
        outcomes,
        result: {
          ...fx.forecastResult,
          probabilities: [0.1, 0.35, 0.3, 0.25].map((p, i) => ({ outcome: outcomes[i], p })),
          ensemble: [],
          specialists: [],
        },
      }),
    )
    open()
    expect(await screen.findByText('Most likely range')).toBeInTheDocument()
    expect(screen.getByText('$80k – $100k → $100k – $120k')).toBeInTheDocument()
    expect(screen.getByText(/together hold 65%/)).toBeInTheDocument()
    expect(screen.getAllByRole('listitem').some((li) => li.textContent === '> $120k25%')).toBe(true)
    expect(screen.queryByRole('heading', { name: 'How the desk got there' })).toBeNull()
  })

  it('handles a categorical report without probabilities', async () => {
    show(fx.forecast({ shape: 'categorical', outcomes: [], result: { ...fx.forecastResult, probabilities: [], summary: '', confidence: 'x' as never } }))
    open()
    expect(await screen.findByText('No probabilities were returned.')).toBeInTheDocument()
    expect(screen.getByText('No summary was returned.')).toBeInTheDocument()
  })
})

describe('ForecastDetailPage · running and failed', () => {
  it('polls every 3 s while specialists work, then shows the report', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-10-02T08:01:05Z'))
    const get = show(fx.forecast({ status: 'running', result: null, completedAt: null }))
    open()
    await act(() => vi.advanceTimersByTimeAsync(0))
    expect(screen.getByRole('heading', { name: 'Specialists at work' })).toBeInTheDocument()
    expect(screen.getByText('1:05')).toBeInTheDocument() // elapsed since createdAt
    expect(screen.getByText('Researching the web')).toBeInTheDocument()
    expect(screen.getByText('Aggregator')).toBeInTheDocument()
    expect(screen.getByText('Outcomes · 2')).toBeInTheDocument()

    get.mockRejectedValueOnce(new ApiError(0, 'network', 'offline'))
    await act(() => vi.advanceTimersByTimeAsync(3000))
    expect(screen.getByText('Connection hiccup · retrying…')).toBeInTheDocument()

    get.mockResolvedValue({ forecast: fx.forecast() })
    await act(() => vi.advanceTimersByTimeAsync(3000))
    expect(screen.getByText('The desk has a number.')).toBeInTheDocument()
    expect(screen.getByText('YES 62%')).toBeInTheDocument()
    await act(() => vi.advanceTimersByTimeAsync(9000))
    expect(get).toHaveBeenCalledTimes(3) // polling stopped
  })

  it('toasts a run that fails while open and re-reads the refunded credits', async () => {
    vi.useFakeTimers()
    const get = show(fx.forecast({ status: 'running', result: null }))
    open()
    await act(() => vi.advanceTimersByTimeAsync(0))
    const me = vi.mocked(api.me).mock.calls.length
    get.mockResolvedValue({ forecast: fx.forecast({ status: 'failed', result: null, error: 'Aggregator timed out' }) })
    await act(() => vi.advanceTimersByTimeAsync(3000))
    expect(screen.getByText('The forecast run failed.')).toBeInTheDocument()
    expect(screen.getByText('Aggregator timed out')).toBeInTheDocument()
    expect(vi.mocked(api.me).mock.calls.length).toBe(me + 1)
  })

  it('runs a failed forecast again', async () => {
    show(fx.forecast({ status: 'failed', result: null, error: null }))
    const create = vi.spyOn(api, 'createForecast').mockResolvedValue({ forecast: fx.forecast({ id: 'f-new', status: 'running' }), credits: fx.credits })
    const { user } = open()
    expect(await screen.findByRole('heading', { name: "The desk couldn't finish this forecast." })).toBeInTheDocument()
    expect(screen.getByText('The run stopped before the aggregator returned a result.')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Edit question' })).toHaveAttribute('href', '/forecasts/new')
    vi.spyOn(api, 'forecast').mockResolvedValue({ forecast: fx.forecast({ id: 'f-new', status: 'running', result: null }) })
    await user.click(screen.getByRole('button', { name: /Run again/ }))
    const { id, status, error, result, createdAt, completedAt, resolution, replay, ...draft } = fx.forecast()
    void [id, status, error, result, createdAt, completedAt, resolution, replay]
    expect(create).toHaveBeenCalledWith(draft)
    await waitFor(() => expect(currentPath()).toBe('/forecasts/f-new'))
    expect(await screen.findByRole('heading', { name: 'Specialists at work' })).toBeInTheDocument()
  })

  it('asks for an edit when the failed draft no longer validates', async () => {
    show(fx.forecast({ status: 'failed', result: null, resolutionDate: '2026-09-01' }))
    const create = vi.spyOn(api, 'createForecast')
    const { user } = open()
    await user.click(await screen.findByRole('button', { name: /Run again/ }))
    expect(screen.getByRole('alert')).toHaveTextContent('The resolution date must be in the future. Edit the question to run it again.')
    expect(create).not.toHaveBeenCalled()
    expect(screen.getByText('Resolution date passed')).toBeInTheDocument()
  })

  it('shows a run-again error inline', async () => {
    show(fx.forecast({ status: 'failed', result: null }))
    vi.spyOn(api, 'createForecast').mockRejectedValue(new ApiError(429, 'ai_quota', 'quota'))
    const { user } = open()
    await user.click(await screen.findByRole('button', { name: /Run again/ }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Daily AI credits used up. They reset at 00:00 UTC.')
  })
})

describe('ForecastDetailPage · your materials', () => {
  it('mounts the materials card after the evidence base and links material evidence to its row', async () => {
    show(fx.materialsForecast())
    open('m1234567-dddd')
    expect(await screen.findByText('Your materials · 3')).toBeInTheDocument()
    const evidence = screen.getByText('A broker note expects the public tranche about 3x covered').closest('li')!
    const link = within(evidence).getByRole('link', { name: 'Your material · Q3 broker note (final).pdf' })
    expect(link).toHaveAttribute('href', '#material-D1')
    expect(link).not.toHaveAttribute('target')
    expect(evidence).toHaveTextContent('D1')
    const order = [...document.querySelectorAll('#evidence-base, #your-materials')].map((h) => h.id)
    expect(order).toEqual(['evidence-base', 'your-materials'])
    expect(screen.getByText(/couldn't read your file order-book-photo\.jpg/)).toBeInTheDocument() // its low-information flag
  })

  it('re-reads the credits when a run completes without reading one of its new files (refunded), and only then', async () => {
    vi.useFakeTimers()
    const finish = async (done: Forecast) => {
      const get = show(fx.materialsForecast({ status: 'running', result: null, completedAt: null }))
      const page = open('m1234567-dddd')
      await act(() => vi.advanceTimersByTimeAsync(0))
      const me = vi.mocked(api.me).mock.calls.length
      get.mockResolvedValue({ forecast: done })
      await act(() => vi.advanceTimersByTimeAsync(3000))
      expect(screen.getByText('The desk has a number.')).toBeInTheDocument()
      page.unmount()
      return vi.mocked(api.me).mock.calls.length - me
    }
    expect(await finish(fx.materialsForecast())).toBe(1) // D3 failed: its credit came back
    const [d1, d2, d3] = fx.materials
    expect(await finish(fx.materialsForecast({ materials: [d1, d2] }))).toBe(0)
    expect(await finish(fx.materialsForecast({ materials: [d1, { ...d3, reusedFrom: 'f-old' }] }))).toBe(0) // never charged here
  })

  it('runs a failed root forecast again with the files the desk read, and says which were not', async () => {
    show(fx.materialsForecast({ status: 'failed', result: null, error: 'Aggregator timed out' }))
    const create = vi.spyOn(api, 'createForecast').mockResolvedValue({ forecast: fx.forecast({ id: 'f-new', status: 'running' }), credits: fx.credits })
    const { user } = open('m1234567-dddd')
    expect(await screen.findByText("1 file wasn't read; attach it again with Edit question.")).toBeInTheDocument()
    expect(screen.getByText('Your materials · 3')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Run again/ })).toHaveTextContent('· 5 cr')
    vi.spyOn(api, 'forecast').mockResolvedValue({ forecast: fx.forecast({ id: 'f-new', status: 'running', result: null }) })
    await user.click(screen.getByRole('button', { name: /Run again/ }))
    expect(create).toHaveBeenCalledWith(expect.objectContaining({ question: fx.materialsForecast().question }), { reuseMaterials: { forecastId: 'm1234567-dddd' } })
  })

  it('reruns a failed update as an update of its parent and tells the user its own files need attaching again', async () => {
    show(fx.materialsForecast({ status: 'failed', result: null, parentId: 'parent-1' }))
    const update = vi.spyOn(api, 'updateForecast').mockResolvedValue({ forecast: fx.forecast({ id: 'f-new', status: 'running' }), credits: fx.credits })
    const { user } = open('m1234567-dddd')
    expect(await screen.findByText("2 files weren't read; attach them again with Edit question.")).toBeInTheDocument() // D1 and D3 were this run's own
    await user.click(screen.getByRole('button', { name: /Run again/ }))
    expect(update).toHaveBeenCalledWith('parent-1')
  })
})

describe('ForecastDetailPage · loading, missing and archive', () => {
  it('shows not-found for an unknown id', async () => {
    vi.spyOn(api, 'forecast').mockRejectedValue(new ApiError(404, 'not_found', 'Not found'))
    const { user } = open('nope')
    expect(await screen.findByRole('heading', { name: 'Forecast not found.' })).toBeInTheDocument()
    await user.click(screen.getByRole('link', { name: 'Back to your forecasts' }))
    expect(screen.getByText('forecast list')).toBeInTheDocument()
  })

  it('retries a failed load', async () => {
    const get = vi.spyOn(api, 'forecast').mockRejectedValueOnce(new ApiError(500, 'internal', 'Database is down'))
    get.mockResolvedValueOnce({ forecast: fx.forecast() })
    const { user } = open()
    expect(screen.getByRole('generic', { busy: true })).toHaveAccessibleName('Loading forecast')
    expect(await screen.findByRole('heading', { name: "Couldn't load this forecast." })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Try again' }))
    expect(await screen.findByText('YES 62%')).toBeInTheDocument()
  })

  it('archives after a confirm; Escape backs out', async () => {
    show(fx.forecast())
    const del = vi.spyOn(api, 'deleteForecast').mockResolvedValue(undefined)
    const { user } = open()
    await user.click(await screen.findByRole('button', { name: 'Archive' }))
    const confirm = screen.getByRole('group', { name: 'Confirm archive' })
    expect(confirm).toHaveTextContent('Archive this forecast?')
    await user.keyboard('{Escape}')
    expect(screen.queryByRole('group', { name: 'Confirm archive' })).toBeNull()

    await user.click(screen.getByRole('button', { name: 'Archive' }))
    await user.click(within(screen.getByRole('group', { name: 'Confirm archive' })).getByRole('button', { name: 'Archive' }))
    expect(del).toHaveBeenCalledWith('f1234567-aaaa')
    expect(await screen.findByText('forecast list')).toBeInTheDocument()
    expect(screen.getByText('Forecast archived. Find it in the Archived view to restore it.')).toBeInTheDocument()
  })

  it('toasts a failed archive', async () => {
    show(fx.forecast())
    vi.spyOn(api, 'deleteForecast').mockRejectedValue(new ApiError(500, 'internal', 'Could not archive'))
    const { user } = open()
    await user.click(await screen.findByRole('button', { name: 'Archive' }))
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    await user.click(screen.getByRole('button', { name: 'Archive' }))
    await user.click(within(screen.getByRole('group', { name: 'Confirm archive' })).getByRole('button', { name: 'Archive' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Could not archive')
  })
})

/** The agent page stand-in: shows the router state the forecast page handed over. */
function EntryProbe() {
  return <pre data-testid="entry-state">{JSON.stringify(useLocation().state)}</pre>
}

describe('ForecastDetailPage · resolution and track record', () => {
  const sol = { symbol: 'SOL', op: 'above', threshold: 130, mode: 'on_date' } as const
  const resolution = () => screen.getByText('Resolution', { selector: '.label' }).closest('.card') as HTMLElement

  it('says a price rule resolves itself and offers no manual control', async () => {
    show(fx.forecast({ priceRule: sol }))
    open()
    expect(await screen.findByText(/Resolves automatically from the Binance/)).toHaveTextContent(
      'Resolves automatically from the Binance SOL/USDT daily close above $130 on Dec 31, 2026.',
    )
    expect(screen.queryByRole('group', { name: 'What happened' })).not.toBeInTheDocument()
  })

  it('resolves an own forecast by hand after a confirm, and clears it', async () => {
    show(fx.forecast())
    const resolve = vi
      .spyOn(api, 'resolveForecast')
      .mockResolvedValueOnce({ forecast: fx.forecast({ resolution: { outcome: 'No', resolvedAt: '2026-10-02T12:00:00Z', by: 'manual' } }) })
      .mockResolvedValueOnce({ forecast: fx.forecast() })
    const { user } = open()
    const choices = await screen.findByRole('group', { name: 'What happened' })
    await user.click(within(choices).getByRole('button', { name: 'No' }))
    const confirm = screen.getByRole('group', { name: 'Confirm resolution' })
    expect(confirm).toHaveTextContent('Resolve as No? It counts toward your track record.')
    await user.keyboard('{Escape}')
    expect(screen.queryByRole('group', { name: 'Confirm resolution' })).toBeNull()

    await user.click(within(screen.getByRole('group', { name: 'What happened' })).getByRole('button', { name: 'No' }))
    await user.click(screen.getByRole('button', { name: 'Resolve as No' }))
    expect(resolve).toHaveBeenCalledWith('f1234567-aaaa', 'No')
    const card = resolution()
    expect(await within(card).findByText(/Resolved No/)).toHaveTextContent('✗Resolved No · Oct 2, 2026 · manual') // the desk leaned Yes
    expect(screen.getByText('Resolved No. It counts toward your track record.')).toBeInTheDocument()

    await user.click(within(card).getByRole('button', { name: 'Clear' }))
    expect(resolve).toHaveBeenLastCalledWith('f1234567-aaaa', null)
    expect(await screen.findByRole('group', { name: 'What happened' })).toBeInTheDocument()
  })

  it('shows a failed resolution', async () => {
    show(fx.forecast())
    vi.spyOn(api, 'resolveForecast').mockRejectedValue(new ApiError(409, 'conflict', 'This forecast resolves automatically from Binance data.'))
    const { user } = open()
    await user.click(within(await screen.findByRole('group', { name: 'What happened' })).getByRole('button', { name: 'Yes' }))
    await user.click(screen.getByRole('button', { name: 'Resolve as Yes' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('This forecast resolves automatically from Binance data.')
  })

  it('opens a replay forecast read-only with its banner', async () => {
    show(fx.replayForecast())
    open('r1234567-bbbb')
    const banner = (await screen.findByText(/^Historical replay as of Aug/)).closest('[role="note"]')!
    expect(banner).toHaveTextContent('Historical replay as of Aug 12, 2026· volatility-only reference 28% Yes')
    expect(screen.getByText(/Resolved Yes/)).toHaveTextContent('✗Resolved Yes · Aug 26, 2026 · auto') // the desk said 30%
    expect(screen.getByText(/^Run as of Aug 12, 2026/)).toHaveTextContent('Run as of Aug 12, 2026 · Resolves Aug 26, 2026')
    expect(screen.getByText(/^Dated news before Aug 12, 2026/)).toHaveTextContent('Dated news before Aug 12, 2026 · 2 sources · 1 read')
    expect(screen.queryByRole('button', { name: 'Archive' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Discuss a trade/ })).not.toBeInTheDocument()
    expect(screen.queryByRole('group', { name: 'What happened' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Clear' })).not.toBeInTheDocument()
  })

  it('shows the volatility-only reference, and the server note when the guard reset the desk to it', async () => {
    show(fx.replayForecast())
    const { unmount } = open('r1234567-bbbb')
    const line = await screen.findByText(/^Volatility-only reference:/)
    expect(line.parentElement).toHaveTextContent('Volatility-only reference: 28% Yes (90-day realized vol 61%/yr, 14 days, as of Aug 12, 2026)')
    expect(screen.queryByText(/The server reset the desk's number/)).not.toBeInTheDocument()
    unmount()

    const base = fx.replayForecast().result!
    const reference = { ...base.reference!, pYes: 0.81, volAnnPct: 52.2, horizonDays: 1 }
    const flags: ForecastResult['flags'] = [
      { type: 'low_information', note: 'Skeptic unavailable for this run; the forecast combines 2 of 3 specialists.' },
      {
        type: 'low_information',
        note: "Server check: the desk moved toward 50% from the volatility-only reference without citing evidence, so the final probability is the reference's.",
      },
    ]
    const guarded = { ...base, reference, flags, guard: 'shrink' as const, probabilities: [{ outcome: 'Yes', p: 0.81 }, { outcome: 'No', p: 0.19 }] }
    show(fx.replayForecast({ result: guarded, replay: { asOf: '2026-08-12', anchorP: 0.81 } }))
    const again = open('r1234567-bbbb')
    expect((await screen.findByText(/^Volatility-only reference:/)).parentElement).toHaveTextContent(
      'Volatility-only reference: 81% Yes (90-day realized vol 52%/yr, 1 day, as of Aug 12, 2026)',
    )
    // The banner names the same reference the same way.
    expect(screen.getByText(/^Historical replay as of Aug/).closest('[role="note"]')).toHaveTextContent('· volatility-only reference 81% Yes')
    expect(screen.getByText(/The server reset the desk's number/).closest('[role="note"]')).toHaveTextContent(
      "The server reset the desk's number to this reference: the desk moved toward 50% from it without citing evidence the formula cannot see.",
    )
    // The reset is said once: the server's own flag for it is dropped; the run's other notes stay.
    expect(screen.queryByText(/^Server check:/)).not.toBeInTheDocument()
    expect(screen.getByText(/^Skeptic unavailable/).closest('[role="note"]')).toHaveTextContent(
      'Low informationSkeptic unavailable for this run; the forecast combines 2 of 3 specialists.',
    )
    again.unmount()

    // Without a reset (an older payload, say), a server note is just a flag and shows.
    show(fx.replayForecast({ result: { ...guarded, guard: null } }))
    const kept = open('r1234567-bbbb')
    expect(await screen.findByText(/^Server check:/)).toBeInTheDocument()
    kept.unmount()

    // No reference (a question without a price rule, or an older result): nothing.
    show(fx.forecast())
    open()
    expect(await screen.findByText('Probability estimate · Yes')).toBeInTheDocument()
    expect(screen.queryByText(/Volatility-only reference/)).not.toBeInTheDocument()
  })

  it('hands the report to the agent with its forecast id and asset', async () => {
    show(fx.forecast())
    const { user } = renderPage(<ForecastDetailPage />, {
      route: '/forecasts/f1234567-aaaa',
      path: '/forecasts/:id',
      routes: { '/': <EntryProbe /> },
    })
    // The question names ETH and BTC: the first universe asset it mentions is BTC.
    expect(await screen.findByRole('link', { name: 'Open BTC in Trade' })).toHaveAttribute('href', '/trade/BTC')
    await user.click(screen.getByRole('button', { name: /Discuss a trade with the agent/ }))
    await waitFor(() => expect(currentPath()).toBe('/'))
    expect(JSON.parse(screen.getByTestId('entry-state').textContent!)).toEqual({
      prompt:
        'I just read the forecast desk report "Will ETH outperform BTC over Q4 2026?" (Yes 62%). What\'s the trade? Analyze it and propose a paper position with a stop and target if the setup supports one.',
      context: { forecastId: 'f1234567-aaaa', symbol: 'BTC' },
    })
  })

  it('asks the agent about a forecast that names no universe asset', async () => {
    show(fx.forecast({ question: 'Will the Fed cut rates in December 2026?', result: { ...fx.forecastResult, market: fx.marketOdds } }))
    const { user } = renderPage(<ForecastDetailPage />, {
      route: '/forecasts/f1234567-aaaa',
      path: '/forecasts/:id',
      routes: { '/': <EntryProbe /> },
    })
    await user.click(await screen.findByRole('button', { name: /Ask the agent about this forecast/ }))
    expect(screen.queryByRole('link', { name: /in Trade$/ })).toBeNull()
    await waitFor(() => expect(currentPath()).toBe('/'))
    expect(JSON.parse(screen.getByTestId('entry-state').textContent!)).toEqual({
      prompt:
        'I just read the forecast desk report "Will the Fed cut rates in December 2026?" (Yes 62%). Explain what drives it and how the desk compares with Polymarket, and what would change the view.',
      context: { forecastId: 'f1234567-aaaa' },
    })
  })

  it("takes the asset from the price rule", async () => {
    show(fx.forecast({ priceRule: sol }))
    open()
    expect(await screen.findByRole('link', { name: 'Open SOL in Trade' })).toHaveAttribute('href', '/trade/SOL')
  })
})

describe('ForecastDetailPage · updates and probability history', () => {
  const history = [
    { id: 'f0000000-0000', outcome: 'Yes', p: 0.28, at: '2026-09-30T08:02:00Z' },
    { id: 'f1234567-aaaa', outcome: 'Yes', p: 0.335, at: '2026-10-02T08:02:00Z' },
  ]

  it('updates a completed forecast: 5 credits, then the new run', async () => {
    show(fx.forecast())
    const update = vi.spyOn(api, 'updateForecast').mockResolvedValue({
      forecast: fx.forecast({ id: 'f-update', status: 'running', result: null, parentId: 'f1234567-aaaa' }),
      credits: { ...fx.credits, used: 30 },
    })
    const { user } = open()
    const button = await screen.findByRole('button', { name: 'Update forecast · 5 credits' })
    expect(button).toHaveAttribute('title', "Re-run the panel on today's evidence; this estimate stays in the history.")
    vi.spyOn(api, 'forecast').mockResolvedValue({ forecast: fx.forecast({ id: 'f-update', status: 'running', result: null, parentId: 'f1234567-aaaa' }) })
    await user.click(button)
    expect(update).toHaveBeenCalledWith('f1234567-aaaa')
    await waitFor(() => expect(currentPath()).toBe('/forecasts/f-update'))
    expect(await screen.findByRole('heading', { name: 'Specialists at work' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Update forecast/ })).toBeNull() // nothing to update while it runs
  })

  it('toasts a refused update', async () => {
    show(fx.forecast())
    vi.spyOn(api, 'updateForecast').mockRejectedValue(new ApiError(409, 'conflict', 'An update is already running.'))
    const { user } = open()
    await user.click(await screen.findByRole('button', { name: /Update forecast/ }))
    expect(await screen.findByText('An update is already running.')).toBeInTheDocument()
    expect(currentPath()).toBe('/forecasts/f1234567-aaaa')
  })

  it('offers no update on a resolved, due, replay or superseded run', async () => {
    for (const f of [
      fx.forecast({ resolution: { outcome: 'Yes', resolvedAt: '2026-10-02T09:00:00Z', by: 'manual' } }),
      fx.forecast({ resolutionDate: '2026-10-02' }),
      fx.replayForecast(),
      fx.forecast({ history: [history[1], { ...history[1], id: 'f-newer' }] }),
    ]) {
      show(f)
      const { unmount } = open(f.id)
      await screen.findByRole('button', { name: 'Export PDF' })
      expect(screen.queryByRole('button', { name: /Update forecast/ })).toBeNull()
      unmount()
    }
  })

  it('shows the probability history and what changed since the last run', async () => {
    show(fx.forecast({ parentId: 'f0000000-0000', history, result: { ...fx.forecastResult, changeNote: 'Fresh ETF inflows moved us up.' } }))
    open()
    const block = (await screen.findByRole('heading', { name: 'Probability history' })).closest('section')!
    const rows = within(block).getAllByRole('link')
    expect(rows.map((r) => r.textContent)).toEqual(['Sep 30 · Yes 28%', 'Oct 2 · Yes 34% (+6 pts)'])
    expect(rows[0]).toHaveAttribute('href', '/forecasts/f0000000-0000')
    expect(rows[1]).toHaveAttribute('aria-current', 'page')
    expect(block).toHaveTextContent('this run')
    expect(screen.getByText('Changed since last run').parentElement).toHaveTextContent('Changed since last runFresh ETF inflows moved us up.')
    expect(screen.queryByText(/A newer run exists/)).toBeNull()
  })

  it('puts the server-written change in the view above the desk’s reasons, and either alone', async () => {
    const tsmUpdate = (over: Partial<ForecastResult>) =>
      fx.tsmDeskForecast({ id: 'f1234567-aaaa', parentId: 'f0000000-0000', result: { ...fx.tsmDeskResult, ...over } })
    const viewChange = 'Median $570 → $611 (+7.2%) · chance at or above today 58% → 62%'
    show(tsmUpdate({ viewChange, changeNote: 'Stronger guidance from the Q3 call lifted the base case.' }))
    open()
    const block = (await screen.findByText('Changed since last run')).parentElement!
    const [numbers, reasons] = within(block).getAllByRole('paragraph')
    expect(numbers).toHaveTextContent(viewChange)
    expect(numbers).toHaveClass('font-mono')
    expect(reasons).toHaveTextContent('Stronger guidance from the Q3 call lifted the base case.')
    expect(numbers.compareDocumentPosition(reasons) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    cleanup()
    // The server dropped every changeNote sentence that stated a level: the numbers stand alone.
    show(tsmUpdate({ viewChange, changeNote: '' }))
    open()
    const alone = (await screen.findByText('Changed since last run')).parentElement!
    expect(alone).toHaveTextContent(`Changed since last run${viewChange}`)
    expect(within(alone).getAllByRole('paragraph')).toHaveLength(1)
    cleanup()
    show(tsmUpdate({ viewChange: undefined, changeNote: undefined }))
    open()
    await screen.findByText('Desk summary')
    expect(screen.queryByText('Changed since last run')).toBeNull()
  })

  it('never states a run in the history as 0% or 100%', async () => {
    const sure = [
      { ...history[0], p: 0.004 },
      { ...history[1], p: 0.9975 },
    ]
    show(fx.forecast({ parentId: 'f0000000-0000', history: sure }))
    open()
    const block = (await screen.findByRole('heading', { name: 'Probability history' })).closest('section')!
    expect(within(block).getAllByRole('link').map((r) => r.textContent)).toEqual(['Sep 30 · Yes <1%', 'Oct 2 · Yes >99% (+100 pts)'])
  })

  it('tells same-day runs apart by their time', async () => {
    show(fx.forecast({ history: [{ ...history[0], at: '2026-10-02T06:00:00Z' }, history[1]] }))
    open()
    const block = (await screen.findByRole('heading', { name: 'Probability history' })).closest('section')!
    expect(within(block).getAllByRole('link').map((r) => r.textContent)).toEqual([
      `${fmtDate('2026-10-02T06:00:00Z', { time: true })} · Yes 28%`,
      `${fmtDate('2026-10-02T08:02:00Z', { time: true })} · Yes 34% (+6 pts)`,
    ])
  })

  it('points a superseded run at the newer one, and hides the history for a single run', async () => {
    show(fx.forecast({ id: 'f0000000-0000', history }))
    const { unmount } = open('f0000000-0000')
    const banner = await screen.findByRole('link', { name: 'A newer run exists: Yes 34% on Oct 2 →' })
    expect(banner).toHaveAttribute('href', '/forecasts/f1234567-aaaa')
    unmount()

    show(fx.forecast({ history: [history[1]] }))
    open()
    await screen.findByRole('button', { name: 'Export PDF' })
    expect(screen.queryByRole('heading', { name: 'Probability history' })).toBeNull()
    expect(screen.queryByText('Changed since last run')).toBeNull()
  })

  it('runs a failed update again as an update of the same run', async () => {
    show(fx.forecast({ status: 'failed', result: null, parentId: 'f0000000-0000' }))
    const update = vi.spyOn(api, 'updateForecast').mockResolvedValue({ forecast: fx.forecast({ id: 'f-again', status: 'running', result: null }), credits: fx.credits })
    const create = vi.spyOn(api, 'createForecast')
    const { user } = open()
    vi.spyOn(api, 'forecast').mockResolvedValue({ forecast: fx.forecast({ id: 'f-again', status: 'running', result: null }) })
    await user.click(await screen.findByRole('button', { name: /Run again/ }))
    expect(update).toHaveBeenCalledWith('f0000000-0000')
    expect(create).not.toHaveBeenCalled()
    await waitFor(() => expect(currentPath()).toBe('/forecasts/f-again'))
  })

  it('prints whole percents that sum to 100 on every specialist card, the ensemble and the final', async () => {
    const split = (yes: number) => [
      { outcome: 'Yes', p: yes },
      { outcome: 'No', p: 1 - yes },
    ]
    show(
      fx.forecast({
        result: {
          ...fx.forecastResult,
          probabilities: split(0.625),
          ensemble: split(0.235),
          specialists: fx.forecastResult.specialists.map((s, i) => ({ ...s, probabilities: split([0.235, 0.345][i]) })),
        },
      }),
    )
    open()
    const section = (await screen.findByRole('heading', { name: 'How the desk got there' })).closest('section')!
    for (const card of within(section).getAllByRole('heading', { level: 3 }).map((h) => h.closest<HTMLElement>('.card')!)) {
      const pcts = within(card).getAllByText(/^\d+%$/).slice(1).map((el) => Number(el.textContent!.replace('%', ''))) // the bars, after the big number
      expect(pcts.reduce((a, b) => a + b, 0)).toBe(100)
    }
    expect(section).toHaveTextContent('24%yes')
    expect(section).toHaveTextContent('35%yes')
    expect(screen.getByText('Ensemble vs final').closest('.card')).toHaveTextContent('24% → 63%▲39pp')
    expect(screen.getByText('Ensemble vs final').closest('.card')).toHaveTextContent('76% → 37%▼39pp')
    expect(screen.getByRole('img', { name: 'Yes 63%, No 37%' })).toBeInTheDocument()
  })
})

describe('ForecastDetailPage · desk view and methods', () => {
  const tsm = (over: Partial<ForecastResult> = {}) => fx.tsmDeskForecast({ id: 'f1234567-aaaa', result: { ...fx.tsmDeskResult, ...over } })
  const specialist = (name: string) => screen.getByRole('heading', { level: 3, name }).closest('.card') as HTMLElement

  it('shows each specialist’s method, median and where its tool started it', async () => {
    show(tsm())
    open()
    await screen.findByRole('heading', { name: 'How the desk got there' })
    const base = specialist('Base-rate statistician')
    expect(within(base).getByText('Historical simulation')).toHaveAttribute('title', 'The method the server recorded from the tool calls that ran')
    expect(base).toHaveTextContent('$580median · 60% ≥ today')
    expect(base).toHaveTextContent('Started from its tool: median $580 → $580')
    const quant = specialist('Market & quant analyst')
    expect(within(quant).getByText('Valuation scenarios').className).toMatch(/bg-surface-2/)
    expect(quant).toHaveTextContent('$693median · 68% ≥ today')
    expect(quant).toHaveTextContent('Started from its tool: median $677 → $693')
    expect(specialist('Narrative & macro skeptic')).toHaveTextContent('Regime & tail risk')
  })

  it('states no member median on a view that states none: each member keeps its top range', async () => {
    show(tsm({ view: { ...fx.tsmDeskResult.view!, basis: { kind: 'none', label: '' } } }))
    open()
    await screen.findByRole('heading', { name: 'How the desk got there' })
    expect(specialist('Base-rate statistician')).toHaveTextContent('19%$300 – $450')
    expect(specialist('Market & quant analyst')).toHaveTextContent('25%≥ $1,100')
    expect(specialist('Narrative & macro skeptic')).toHaveTextContent('21%< $300')
    for (const name of ['Base-rate statistician', 'Market & quant analyst', 'Narrative & macro skeptic']) {
      expect(specialist(name)).not.toHaveTextContent(/median|≥ today|Started from its tool/)
      expect(within(specialist(name)).getByText(/Historical simulation|Valuation scenarios|Regime & tail risk/), 'the method chip stays').toBeInTheDocument()
    }
  })

  it('heads the panel by count, and says so when the server counted duplicated members once', async () => {
    show(tsm())
    open()
    await screen.findByRole('heading', { name: 'How the desk got there' })
    expect(screen.getByText('Specialists · 3')).toBeInTheDocument()
    expect(screen.queryByText(/independent views/)).not.toBeInTheDocument()
    cleanup()
    show(tsm({ methods: { ...fx.tsmDeskResult.methods!, agreement: 'duplicate' } }))
    open()
    await screen.findByRole('heading', { name: 'How the desk got there' })
    expect(screen.getByText('3 specialists · counted as 1 view')).toBeInTheDocument()
  })

  it('never states a chance as 0% or 100%: ">99%" on the hero and the hand-off', async () => {
    const view = { ...fx.tsmDeskResult.view!, pAbove: { value: 0.9975 } }
    show(tsm({ view }))
    const { user } = renderPage(<ForecastDetailPage />, { route: '/forecasts/f1234567-aaaa', path: '/forecasts/:id', routes: { '/': <EntryProbe /> } })
    const tile = (await screen.findByText('Chance at or above today')).parentElement!
    expect(tile).toHaveTextContent(/^Chance at or above today>99%$/)
    expect(screen.queryByText(/100%/)).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /Discuss a trade with the agent|Ask the agent about this forecast/ }))
    await waitFor(() => expect(currentPath()).toBe('/'))
    expect(screen.getByTestId('entry-state').textContent).toContain('(median $611, >99% at or above today)')
  })

  it('outlines a member that ran no model and gives it no tool line', async () => {
    const [b, q, sk] = fx.tsmDeskResult.specialists
    show(tsm({ specialists: [{ ...b, method: 'judgment', anchor: undefined }, { ...q, method: 'fallback' }, { ...sk, method: 'astrology' as never }] }))
    open()
    await screen.findByRole('heading', { name: 'How the desk got there' })
    expect(within(specialist('Base-rate statistician')).getByText('Judgment (no model)').className).toMatch(/border-border/)
    expect(within(specialist('Market & quant analyst')).getByText('Fallback (data unavailable)').className).toMatch(/border-border/)
    expect(specialist('Market & quant analyst')).not.toHaveTextContent('Started from its tool')
    expect(specialist('Base-rate statistician'), 'a member without its method keeps its top range, no median').toHaveTextContent('19%$300 – $450')
    expect(specialist('Base-rate statistician')).not.toHaveTextContent('median')
    expect(specialist('Narrative & macro skeptic'), 'an unknown method gets no chip').not.toHaveTextContent(/Historical simulation|Valuation scenarios|Volatility model|Regime & tail risk|Judgment|Fallback/)
    expect(specialist('Narrative & macro skeptic')).not.toHaveTextContent('median')
    const medians = within(within(screen.getByText('Weighted pool vs final').closest('.card')!).getByText('Medians').parentElement!).getAllByRole('listitem')
    expect(medians.map((li) => li.textContent), 'only members that ran their method').toEqual(['Pool$609', 'Final$611'])
  })

  it('folds the quant’s valuation model into a table with each input’s basis', async () => {
    show(tsm())
    const { user } = open()
    await screen.findByRole('heading', { name: 'How the desk got there' })
    const quant = specialist('Market & quant analyst')
    const fold = within(quant).getByText('Valuation model').closest('details')!
    expect(fold).not.toHaveAttribute('open')
    await user.click(within(quant).getByText('Valuation model'))
    const rows = within(within(fold).getByRole('table')).getAllByRole('row').slice(1)
    expect(rows.map((r) => [...r.children].map((c) => c.textContent))).toEqual([
      ['AI capex digestionbearFY2027 EPS $21.33 · F3 (opens in a new tab)EPS growth 2028–2032 3%/yr · assumptionExit P/E 14× · assumption', '30%', '$346-29%'],
      ['Compounding foundry leaderbaseFY2027 EPS $21.33 · F3 (opens in a new tab)EPS growth 2028–2032 12%/yr · assumptionExit P/E 20× · assumption', '50%', '$752+55%'],
      ['AI supercyclebullFY2027 EPS $21.33 · F3 (opens in a new tab)EPS growth 2028–2032 20%/yr · assumptionExit P/E 25× · model knowledge 2025-06', '20%', '$1,330+174%'],
    ])
    expect(specialist('Base-rate statistician')).not.toHaveTextContent('Valuation model')
  })

  it('weighs the methods into a pool and says how they agreed', async () => {
    show(tsm())
    open()
    const card = (await screen.findByText('Weighted pool vs final')).closest('.card') as HTMLElement
    expect(screen.queryByText('Ensemble vs final')).not.toBeInTheDocument()
    expect(within(card).getByText('Historical simulation 30% · Valuation scenarios 45% · Regime & tail risk 25%')).toBeInTheDocument()
    expect(card).toHaveTextContent('Valuation scenarios: Earnings and the exit multiple explain most of a five-year move.')
    expect(within(card).getByText('Methods disagree; the desk resolved them')).toBeInTheDocument()
    expect(card).toHaveTextContent('We trusted the valuation scenarios for the centre and the measured history for the width.')
    expect(card).toHaveTextContent('≥ $1,10020% → 20%±0')
    expect(card).toHaveTextContent('POOL')
    const medians = within(within(card).getByText('Medians').parentElement!).getAllByRole('listitem').map((li) => li.textContent)
    expect(medians).toEqual(['Base rate$580', 'Quant$693', 'Skeptic$539', 'Pool$609', 'Final$611'])
    expect(card).not.toHaveTextContent('Server check')
  })

  it('names a duplicate, prior weights and the server guard', async () => {
    const methods = fx.tsmDeskResult.methods!
    show(tsm({ methods: { ...methods, agreement: 'duplicate', weightsFrom: 'prior', guard: 'sharpness', note: '', weights: methods.weights.map((w) => ({ ...w, why: null })) } }))
    open()
    const card = (await screen.findByText('Weighted pool vs final')).closest('.card') as HTMLElement
    expect(within(card).getByText('Members duplicated one view; counted once')).toBeInTheDocument()
    expect(within(card).getByText('Prior weights for this horizon: the desk set none of its own.')).toBeInTheDocument()
    expect(within(card).getByText(/^Server check: the final distribution was sharper than the methods support/)).toBeInTheDocument()
    expect(card).not.toHaveTextContent('Valuation scenarios:')
    cleanup()
    show(tsm({ methods: { ...methods, agreement: 'independent_agree', guard: 'width', note: 'History and valuation agree. Server check: blended halfway back.' } }))
    open()
    const again = (await screen.findByText('Weighted pool vs final')).closest('.card') as HTMLElement
    expect(within(again).getByText('Independent methods agree')).toBeInTheDocument()
    expect(again.textContent!.match(/Server check/g), 'the note already carries it').toHaveLength(1)
    cleanup()
    // The volatility floor, after the pool guard: the API's note says both, once each.
    const floored = 'We trusted the history. Server check: the final distribution was sharper than the methods support, so it was blended halfway back to the pool. Server check: the final distribution was widened to the measured volatility: no cited input supports a narrower range.'
    show(tsm({ methods: { ...methods, agreement: 'independent_agree', guard: 'volatility', note: floored } }))
    open()
    const wide = (await screen.findByText('Weighted pool vs final')).closest('.card') as HTMLElement
    expect(within(wide).getByText(floored)).toBeInTheDocument()
    expect(wide.textContent!.match(/Server check/g)).toHaveLength(2)
    cleanup()
    // A note without the server's sentence (none is stored so): the page's own line says what the floor did.
    show(tsm({ methods: { ...methods, guard: 'volatility', note: '' } }))
    open()
    const bare = (await screen.findByText('Weighted pool vs final')).closest('.card') as HTMLElement
    expect(within(bare).getByText('Server check: the final distribution was widened to the measured volatility: no cited input supports a narrower range.')).toBeInTheDocument()
    cleanup()
    // Two members on one method (both judgment) are told apart by their roles.
    const weights = [
      { role: 'base_rate' as const, method: 'history', weight: 0.4, why: 'The reference class.' },
      { role: 'quant' as const, method: 'judgment', weight: 0.3, why: 'Earnings drive the price.' },
      { role: 'skeptic' as const, method: 'judgment', weight: 0.3, why: null },
    ]
    show(tsm({ methods: { ...methods, agreement: 'duplicate', weights } }))
    open()
    const dup = (await screen.findByText('Weighted pool vs final')).closest('.card') as HTMLElement
    expect(within(dup).getByText('Historical simulation 40% · Quant, judgment (no model) 30% · Skeptic, judgment (no model) 30%')).toBeInTheDocument()
    expect(dup).toHaveTextContent('Quant, judgment (no model): Earnings drive the price.')
  })

  it('keeps "Ensemble vs final" for a numeric report without methods', async () => {
    show(tsm({ methods: undefined }))
    open()
    expect(await screen.findByText('Ensemble vs final')).toBeInTheDocument()
    expect(screen.queryByText('Weighted pool vs final')).not.toBeInTheDocument()
  })

  it('shows the measured history above what to watch, its source linked', async () => {
    show(tsm({ watch: [{ signal: 'Hyperscaler capex guidance', detail: '', branches: [] }] }))
    open()
    const card = (await screen.findByText('Measured history · H1')).closest('.card') as HTMLElement
    expect(card).toHaveAttribute('id', 'measured')
    expect(within(card).getByRole('heading', { name: 'How TSM has moved' })).toBeInTheDocument()
    expect(within(card).getByRole('link', { name: /Yahoo Finance daily closes \(NYSE: TSM\)/ })).toHaveAttribute('href', 'https://finance.yahoo.com/quote/TSM/history')
    expect(card).toHaveTextContent('Oct 9, 1997 – Oct 6, 2026 · 7,298 sessions')
    expect(card).toHaveTextContent('Volatility35% 1y · 36% 5y · 38% full a year')
    expect(card).toHaveTextContent('Price CAGR+21.4% 5y · +24.9% 10y · +13.2% since 1997')
    expect(card).toHaveTextContent('Max drawdown-83% (peak Mar 2000, trough Oct 2002, recovered Jan 2010)')
    expect(card).toHaveTextContent('Past 5-year windows: 74% ended higher (5 independent of 5,469 overlapping)')
    expect(card).toHaveTextContent('ending multiple ×1.71 median · ×0.62–×4.05 for 80%')
    // A survivor's past windows are no promise: the card says so.
    expect(card).toHaveTextContent('come from an asset that survived to be asked about, so they flatter what comes next')
    const watch = screen.getByText('What to watch').closest('.card')!
    expect(card.compareDocumentPosition(watch) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('reads an unrecovered drawdown and a short window, and has no history card without H1', async () => {
    show(tsm({ measured: { ...fx.tsmMeasured, url: null, volAnnPct: { y1: null, y5: 36.2, all: 38.1 }, maxDrawdown: { pct: -61.2, peak: '2024-07-10', trough: '', recovered: null }, window: { ...fx.tsmMeasured.window!, days: 90 } } }))
    open()
    const card = (await screen.findByText('Measured history · H1')).closest('.card') as HTMLElement
    expect(within(card).queryByRole('link')).not.toBeInTheDocument()
    expect(card).toHaveTextContent('Volatility— 1y · 36% 5y')
    expect(card).toHaveTextContent('-61% (peak Jul 2024, not recovered)')
    expect(card).toHaveTextContent('Past 90-day windows')
    cleanup()
    show(tsm({ measured: undefined }))
    open()
    await screen.findByText('Weighted pool vs final')
    expect(screen.queryByText('Measured history · H1')).not.toBeInTheDocument()
    expect(within(screen.getByRole('heading', { level: 3, name: 'AI supercycle' }).closest('li')!).queryByRole('link', { name: 'H1' }), 'H1 links only to a card that exists').toBeNull()
  })

  it('shows the scenarios under the hero and hands the agent the median, or the 50% range when none is stated', async () => {
    const handoff = async (f: Forecast) => {
      show(f)
      const { user } = renderPage(<ForecastDetailPage />, { route: `/forecasts/${f.id}`, path: '/forecasts/:id', routes: { '/': <EntryProbe /> } })
      const section = (await screen.findByRole('heading', { level: 2, name: 'What has to happen' })).closest('section')!
      expect(section.compareDocumentPosition(screen.getByRole('heading', { name: 'How the desk got there' })) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
      await user.click(screen.getByRole('button', { name: /Discuss a trade with the agent|Ask the agent about this forecast/ }))
      await waitFor(() => expect(currentPath()).toBe('/'))
      return (JSON.parse(screen.getByTestId('entry-state').textContent!) as { prompt: string }).prompt
    }
    expect(await handoff(tsm())).toMatch(/^I just read the forecast desk report "What will be the NYSE closing price of TSM ADR on 2031-10-06\?" \(median \$611, 62% at or above today\)\./)
    cleanup()
    const none = { ...fx.tsmDeskResult.view!, basis: { kind: 'none' as const, label: '' } }
    expect(await handoff(tsm({ view: none }))).toMatch(/\(50% range \$363–\$989\)\./)
  })

  it('tracks the median from run to run when the view leads, and keeps the top range otherwise', async () => {
    const history = [
      { id: 'f0000000-0000', outcome: '$300 – $450', p: 0.19, at: '2026-09-30T08:02:00Z', median: 570.3 },
      { id: 'f1234567-aaaa', outcome: '≥ $1,100', p: 0.2, at: '2026-10-06T21:06:00Z', median: 610.9 },
    ]
    show({ ...tsm(), history })
    open()
    const card = (await screen.findByRole('heading', { name: 'Median history' })).closest('section')!
    expect(within(card).getAllByRole('listitem').map((li) => li.textContent)).toEqual(['Sep 30 · median $570', 'Oct 6 · median $611 (+7.1%) · this run'])
    cleanup()
    // A run without a median (it could not be read as numbers) keeps the probability rows, median beside where known.
    show({ ...tsm(), history: [{ ...history[0], median: undefined }, history[1]] })
    open()
    const again = (await screen.findByRole('heading', { name: 'Probability history' })).closest('section')!
    expect(within(again).getAllByRole('listitem').map((li) => li.textContent)).toEqual([
      'Sep 30 · $300 – $450 19%',
      'Oct 6 · ≥ $1,100 20% (+1 pts) · median $611 · this run',
    ])
  })

  it('renders an older ordered report stored without a view as it always did', async () => {
    show(fx.deepForecast())
    open()
    expect(await screen.findByText('Most likely range')).toBeInTheDocument()
    expect(screen.queryByText(/Desk view|Weighted pool|What has to happen|Measured history|Started from its tool/)).not.toBeInTheDocument()
    expect(screen.getByText('Ensemble vs final')).toBeInTheDocument()
    const hero = screen.getByText('Most likely range').closest('.card')!
    expect(hero.querySelector('p.mt-3.text-\\[16px\\]')).toHaveTextContent(fx.deepForecastResult.summary)
    expect(hero.querySelectorAll('p.font-medium')).toHaveLength(0)
  })
})

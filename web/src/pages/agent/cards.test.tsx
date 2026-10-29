import { screen, within } from '@testing-library/react'
import { useLocation } from 'react-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, api } from '../../lib/api'
import type { Card as AgentCardType, PriceOutlook, SourceRef, TradeProposal } from '../../lib/types'
import * as fx from '../../test/fixtures'
import { currentPath, renderPage, stubSession } from '../../test/utils'
import { AgentCard } from './cards'

const fill = fx.trade({ id: 'tr1', symbol: 'BTC', qty: 0.005937, price: 84213.2, notional: 500, fee: 0.5, source: 'agent' })
const proposal = (over: Partial<TradeProposal> = {}) => renderPage(<AgentCard card={{ type: 'trade_proposal', data: fx.proposal(over) }} />)

function Idea() {
  const state = useLocation().state as { idea?: string } | null
  return <p>idea: {state?.idea}</p>
}

beforeEach(() => {
  stubSession()
})

describe('TradeProposalCard', () => {
  it('shows the actual stock refusal reason and leaves an unfilled proposal retryable', async () => {
    vi.spyOn(api, 'placeOrder').mockRejectedValue(new ApiError(409, 'conflict', 'AAPL mark is unavailable; no order was filled.'))
    const { user } = proposal({ symbol: 'AAPL', price: 225 })
    await user.click(screen.getByRole('button', { name: 'Confirm buy (paper)' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('AAPL mark is unavailable; no order was filled.')
    expect(screen.queryByText('Already filled')).toBeNull()
    expect(screen.getByRole('button', { name: 'Confirm buy (paper)' })).toBeEnabled()
  })

  it('shows the fill of a stored, filled card instead of Confirm', async () => {
    proposal({ status: 'filled', trade: fill })
    expect(await screen.findByText('Filled 0.005937 BTC @ $84,213.20')).toBeInTheDocument()
    expect(screen.getByText(/^vs proposed \$84,000\.00 \(\+0\.25%\) · \$500\.00 notional/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Confirm/ })).toBeNull()
  })

  it('says Already filled for a filled card without its trade', () => {
    proposal({ status: 'filled', trade: null })
    expect(screen.getByRole('status')).toHaveTextContent('Already filled·View account')
    expect(screen.queryByRole('button', { name: /Confirm/ })).toBeNull()
  })

  it('turns a 409 on Confirm into Already filled', async () => {
    vi.spyOn(api, 'placeOrder').mockRejectedValue(new ApiError(409, 'conflict', 'This proposal was already filled.'))
    const { user } = proposal()
    await user.click(screen.getByRole('button', { name: 'Confirm buy (paper)' }))
    expect(await screen.findByText('Already filled')).toBeInTheDocument()
    expect(screen.getByRole('status')).toHaveTextContent('This proposal was already filled.')
    expect(screen.getByRole('link', { name: /View account/ })).toHaveAttribute('href', '/account')
    expect(screen.queryByRole('button', { name: /Confirm/ })).toBeNull()
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('sends the proposalId, and a forecast note when a forecast informed it', async () => {
    const order = vi.spyOn(api, 'placeOrder').mockResolvedValue({ trade: fill, portfolio: fx.portfolio })
    const question = `Will BTC close above $120k on Dec 31? ${'x'.repeat(120)}`
    const { user } = proposal({ forecast: { id: 'f1', question, outcome: 'Yes', p: 0.3 } })
    const link = screen.getByRole('link', { name: 'Informed by forecast desk · Yes 30%' })
    expect(link).toHaveAttribute('href', '/forecasts/f1')
    expect(link).toHaveAttribute('title', question)
    await user.click(screen.getByRole('button', { name: 'Confirm buy (paper)' }))
    const sent = order.mock.calls[0][0]
    expect(sent).toMatchObject({ symbol: 'BTC', side: 'buy', notional: 500, source: 'agent', proposalId: fx.proposal().proposalId })
    expect(sent.note).toHaveLength(120)
    expect(sent.note).toMatch(/^Forecast: Will BTC close above \$120k on Dec 31\? x+…$/)
    expect(await screen.findByText('Filled 0.005937 BTC @ $84,213.20')).toBeInTheDocument()
  })

  it('shows reward to risk from the proposed price, to 2 decimals', () => {
    proposal({ price: 84000, stopLoss: 80000, takeProfit: 91400 })
    expect(screen.getByText('R:R 1 : 1.85')).toBeInTheDocument()
  })

  it('shows no reward to risk without both levels', () => {
    proposal({ stopLoss: null })
    expect(screen.queryByText(/^R:R/)).toBeNull()
  })

  it('confirms a legacy card (no proposalId, no forecast) as before', async () => {
    const order = vi.spyOn(api, 'placeOrder').mockResolvedValue({ trade: fill, portfolio: fx.portfolio })
    const { user } = proposal({ proposalId: undefined, forecast: undefined, status: undefined, trade: undefined })
    expect(screen.queryByText(/Informed by forecast desk/)).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Confirm buy (paper)' }))
    expect(order.mock.calls[0][0]).toEqual({ symbol: 'BTC', side: 'buy', notional: 500, stopLoss: 80000, takeProfit: 92000, source: 'agent' })
  })
})

describe('TradeProposalCard · desk stance', () => {
  const forecast = (p: number, stance: 'supports' | 'opposes' | 'split' | null) => ({ id: 'f1', question: 'Will BTC close above $120k?', outcome: 'Yes', p, stance })

  it.each([
    ['supports', 0.62, 'Desk agrees · Yes 62%'],
    ['opposes', 0.46, 'Desk leans against this trade · No 54%'], // a 46% Yes leans No
    ['split', 0.48, 'Desk is split · Yes 48%'],
  ] as const)('%s reads the side the desk leans to', (stance, p, name) => {
    proposal({ forecast: forecast(p, stance) })
    const link = screen.getByRole('link', { name })
    expect(link).toHaveAttribute('href', '/forecasts/f1')
    expect(link).toHaveAttribute('title', 'Will BTC close above $120k?')
    expect(screen.queryByText(/Informed by forecast desk/)).toBeNull()
  })

  it('keeps the plain line on cards without a stance', () => {
    proposal({ forecast: forecast(0.46, null) })
    expect(screen.getByRole('link', { name: 'Informed by forecast desk · Yes 46%' })).toBeInTheDocument()
  })

  it("states a numeric report by its view on the plain line, not its top range", () => {
    const ref = { id: 'v1', question: 'What will be the NYSE closing price of TSM ADR on 2031-10-06?', outcome: '≥ $1,100', p: 0.2, stance: null }
    const { unmount } = proposal({ symbol: 'TSM', forecast: { ...ref, view: fx.tsmViewBrief } })
    expect(screen.getByRole('link', { name: 'Informed by forecast desk · median $611 · 62% ≥ today' })).toHaveAttribute('href', '/forecasts/v1')
    unmount()
    proposal({ symbol: 'TSM', forecast: { ...ref, view: { ...fx.tsmViewBrief, pAbove: null, basis: 'none' } } })
    expect(screen.getByRole('link', { name: 'Informed by forecast desk · 80% range ≈$242–≈$1,480' })).toBeInTheDocument()
  })
})

describe('TradeProposalCard · quote feeds', () => {
  it.each([
    ['open', 'Delayed price'],
    ['closed', 'Last close'],
    ['unknown', 'Last quote'],
  ] as const)('names a %s quote feed’s price as Yahoo serves it, never live', async (marketState, label) => {
    const dbs = fx.feedAsset('DBS', 'DBS Group', 0, {})
    vi.mocked(api.assets).mockResolvedValue({ assets: [...fx.assets, { ...dbs, quoteFeed: { ...dbs.quoteFeed!, marketState } }] })
    proposal({ symbol: 'DBS', price: 61.4, stopLoss: null, takeProfit: null })
    expect(await screen.findByText(label)).toBeInTheDocument()
    expect(screen.queryByText('Live price')).toBeNull()
  })

  it.each([
    ['naver', 'realtime', 3, 'Real-time price'],
    ['sgx', 'near-realtime', 99, 'Price, 2 min behind'],
    ['yahoo', 'delayed', 1201, 'Delayed price'],
  ] as const)('names an open %s quote by its own freshness', async (source, cls, delaySeconds, label) => {
    const dbs = fx.feedAsset('DBS', 'DBS Group', 0, {})
    const quoteFeed = { ...dbs.quoteFeed!, marketState: 'open' as const, source, class: cls, lastTradeAt: '2026-10-15T04:20:15Z', delaySeconds }
    vi.mocked(api.assets).mockResolvedValue({ assets: [...fx.assets, { ...dbs, quoteFeed }] })
    proposal({ symbol: 'DBS', price: 61.4, stopLoss: null, takeProfit: null })
    expect(await screen.findByText(label)).toBeInTheDocument()
    expect(screen.queryByText(/live/i)).toBeNull()
  })

  it('keeps Live price for crypto', () => {
    proposal()
    expect(screen.getByText('Live price')).toBeInTheDocument()
  })
})

describe('SourcesCard', () => {
  const item = (n: number, kind?: SourceRef['kind']): SourceRef => ({ n, title: `Story ${n}`, url: `https://s${n}.io/a`, source: `s${n}.io`, publishedAt: null, kind })

  it.each([
    [['wire', 'wire'], 'Market wire · stored headlines, refreshed every 2 h'],
    [['news', 'web', 'page'], 'Live web search'],
    [['wire', 'news'], 'Live search + market wire'],
    [[undefined, 'news'], 'Sources'], // stored before kinds
    [['market', 'market'], 'Prediction markets'],
    [['news', 'market'], 'Live search + prediction markets'],
    [['wire', 'market'], 'Prediction markets + market wire'],
  ] as const)('%j → %s', (kinds, header) => {
    renderPage(<AgentCard card={{ type: 'sources', data: { items: kinds.map((k, i) => item(i + 1, k)) } }} />)
    expect(screen.getByText(header, { exact: true })).toBeInTheDocument()
    expect(screen.getByText(`Sources · ${kinds.length}`)).toBeInTheDocument()
  })

  it('lists a prediction market under its publisher, Polymarket', () => {
    const market: SourceRef = { n: 1, title: 'Fed decreases rates by 25 bps after December meeting?', url: 'https://polymarket.com/event/fed#q', source: 'Polymarket', publishedAt: null, kind: 'market' }
    renderPage(<AgentCard card={{ type: 'sources', data: { items: [market] } }} answer="A cut is priced at 82% [1]." />)
    const row = within(screen.getByRole('list', { name: 'Sources' })).getByRole('listitem')
    expect(within(row).getByRole('link', { name: /Fed decreases rates/ })).toHaveAttribute('href', 'https://polymarket.com/event/fed#q')
    expect(within(row).getByText('Polymarket')).toBeInTheDocument()
  })

  const items = Array.from({ length: 8 }, (_, i) => item(i + 1, 'news'))
  const titles = (name: string) => within(screen.getByRole('list', { name })).getAllByRole('link').map((a) => a.textContent)

  it('lists the cited sources first, in number order, and folds the rest under Also read', async () => {
    const { user } = renderPage(<AgentCard card={{ type: 'sources', data: { items } }} answer="ETF flows [5]. Funding is flat [2, 7]; see [5] again and [12]." />)
    expect(screen.getByText('Sources · 8 · 3 cited')).toBeInTheDocument()
    expect(titles('Sources')).toEqual(['Story 2', 'Story 5', 'Story 7'])
    expect(screen.queryByRole('list', { name: 'Also read' })).toBeNull()
    const fold = screen.getByRole('button', { name: 'Also read · 5' })
    expect(fold).toHaveAttribute('aria-expanded', 'false')
    await user.click(fold)
    expect(fold).toHaveAttribute('aria-expanded', 'true')
    expect(titles('Also read')).toEqual(['Story 1', 'Story 3', 'Story 4', 'Story 6', 'Story 8'])
  })

  it('keeps the first five and Show all when the answer cites nothing on the card', async () => {
    const { user } = renderPage(<AgentCard card={{ type: 'sources', data: { items } }} answer="No citations here, only [42]." />)
    expect(screen.getByText('Sources · 8')).toBeInTheDocument()
    expect(titles('Sources')).toEqual(['Story 1', 'Story 2', 'Story 3', 'Story 4', 'Story 5'])
    expect(screen.queryByRole('button', { name: /Also read/ })).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Show all 8 sources' }))
    expect(titles('Sources')).toHaveLength(8)
  })
})

describe('PriceOutlookCard', () => {
  it('opens a forecast draft with the outlook as the idea', async () => {
    const { user } = renderPage(<AgentCard card={{ type: 'price_outlook', data: fx.outlook }} />, { routes: { '/forecasts/new': <Idea /> } })
    await user.click(screen.getByRole('link', { name: 'Run a full forecast' }))
    expect(currentPath()).toBe('/forecasts/new')
    expect(screen.getByText('idea: BTC price in 3 days (spot $84,213.20): where will it trade?')).toBeInTheDocument()
  })

  it('links to the report instead in a forecast handoff', () => {
    renderPage(<AgentCard card={{ type: 'price_outlook', data: fx.outlook }} forecastId="f1234567-aaaa" />)
    expect(screen.getByRole('link', { name: 'View report' })).toHaveAttribute('href', '/forecasts/f1234567-aaaa')
    expect(screen.queryByRole('link', { name: 'Run a full forecast' })).toBeNull()
  })

  it('keeps the short-horizon card as it was, even with a 90-day volBasis', () => {
    const plain = renderPage(<AgentCard card={{ type: 'price_outlook', data: fx.outlook }} />)
    const html = plain.container.innerHTML
    plain.unmount()
    const { container } = renderPage(<AgentCard card={{ type: 'price_outlook', data: { ...fx.outlook, volBasis: { window: '90d', days: 90, since: '2026-07-05' } } }} />)
    expect(container.innerHTML).toBe(html)
    expect(screen.getByText('Price outlook · 3 days')).toBeInTheDocument()
    expect(screen.getByText('BTC in 3 days')).toBeInTheDocument()
    expect(screen.getByText('49% annualized vol · lognormal, zero drift')).toBeInTheDocument()
    expect(screen.queryByRole('table')).toBeNull()
    expect(screen.queryByText(/year/)).toBeNull()
  })
})

describe('PriceOutlookCard · long horizon', () => {
  const rows = (table: HTMLElement) => within(table).getAllByRole('row').slice(1).map((r) => [...r.querySelectorAll('th, td')].map((c) => c.textContent))
  const cells = (region: HTMLElement) => within(region).getAllByRole('term').map((t) => `${t.textContent} ${t.nextElementSibling?.textContent}`)

  it('labels a 10-year card by years with its long-run vol basis and multi-year quantiles', async () => {
    const { user } = renderPage(<AgentCard card={{ type: 'price_outlook', data: fx.longOutlook }} />, { routes: { '/forecasts/new': <Idea /> } })
    expect(screen.getByText('10-year outlook')).toBeInTheDocument()
    expect(screen.getByText('BTC in 10 years')).toBeInTheDocument()
    expect(screen.getByText('62% annualized vol · long-run volatility since Jul 17, 2018 (3,000 daily closes) · lognormal, zero drift')).toBeInTheDocument()
    // Cents are noise at these levels; the bull case runs to millions.
    expect(['$3,348', '$84,213', '$2.12M'].map((v) => screen.getByText(v).nextElementSibling?.textContent)).toEqual(['-96.0%', '0.0%', '+2,415.2%'])
    await user.click(screen.getByRole('link', { name: 'Run a full forecast' }))
    expect(screen.getByText('idea: BTC price in 10 years (spot $84,213.20): where will it trade?')).toBeInTheDocument()
  })

  it('shows the multiples at horizon and touched, how long a 10× might take, and the drift case as an assumption', () => {
    renderPage(<AgentCard card={{ type: 'price_outlook', data: fx.longOutlook }} />)
    const odds = screen.getByRole('region', { name: 'Odds of a price multiple' })
    expect(within(odds).getByText('Volatility model · not a forecast of direction')).toBeInTheDocument()
    const baseline = within(odds).getByRole('table', { name: 'Odds of each multiple of spot over 10 years, zero drift' })
    expect(within(baseline).getAllByRole('columnheader').map((h) => h.textContent)).toEqual(['Level', 'At horizon', 'Touched at any point'])
    expect(rows(baseline)).toEqual([
      ['0.5×$42,107', '36%', '72%'],
      ['2×$168,426', '36%', '72%'],
      ['5×$421,066', '21%', '41%'],
      ['10×$842,132', '12%', '24%'],
    ])

    const tenX = screen.getByRole('region', { name: 'How long might a 10× take?' })
    expect(within(tenX).getByText('Chance of touching $842,132 (10× spot) within')).toBeInTheDocument()
    expect(cells(tenX)).toEqual(['1 yr <1%', '3 yrs 3%', '5 yrs 10%', '10 yrs 24%'])

    const drift = screen.getByRole('region', { name: 'Alternative assumption' })
    expect(within(drift).getByText('Assumption')).toBeInTheDocument()
    expect(within(drift).getByText('Historical drift, halved (assumption)')).toBeInTheDocument()
    expect(within(drift).getByText('+15.0%/yr drift · median $377,417 (+348.2%) in 10 years')).toBeInTheDocument()
    expect(within(drift).getByText(/historical drift since Jul 17, 2018, halved and capped at ±25%\/yr/)).toBeInTheDocument()
    expect(rows(within(drift).getByRole('table'))).toEqual([
      ['0.5×$42,107', '13%', '52%'],
      ['2×$168,426', '66%', '89%'],
      ['5×$421,066', '48%', '68%'],
      ['10×$842,132', '34%', '50%'],
    ])
  })

  it('shows a 2-year card on short history without a drift case, with only the 10× windows inside the horizon', () => {
    renderPage(<AgentCard card={{ type: 'price_outlook', data: fx.twoYearOutlook }} />)
    expect(screen.getByText('2-year outlook')).toBeInTheDocument()
    expect(screen.getByText('NVDA in 2 years')).toBeInTheDocument()
    expect(screen.getByText(/long-run volatility since Sep 18, 2025 \(380 daily closes\)/)).toBeInTheDocument()
    expect(rows(screen.getByRole('table'))).toEqual([
      ['0.5×$91.25', '15%', '31%'],
      ['2×$365', '15%', '31%'],
      ['5×$912.50', '1%', '2%'],
      ['10×$1,825', '<1%', '<1%'],
    ])
    expect(cells(screen.getByRole('region', { name: 'How long might a 10× take?' }))).toEqual(['1 yr <1%'])
    expect(screen.queryByRole('region', { name: 'Alternative assumption' })).toBeNull()
    expect(screen.queryByText('Assumption')).toBeNull()
  })

  it('still reads by years when an older long card lacks the new fields', () => {
    const { volBasis: _, longHorizon: __, ...bare } = fx.longOutlook
    renderPage(<AgentCard card={{ type: 'price_outlook', data: bare }} />)
    expect(screen.getByText('10-year outlook')).toBeInTheDocument()
    expect(screen.getByText('62% annualized vol · lognormal, zero drift')).toBeInTheDocument()
    expect(screen.queryByRole('table')).toBeNull()
    expect(screen.queryByRole('region')).toBeNull()
  })
})

describe('PriceOutlookCard · legacy cards', () => {
  it('relabels the zero-drift percentiles and rounds the levels: $96.44, not $96.4379', () => {
    renderPage(<AgentCard card={{ type: 'price_outlook', data: fx.legacyOutlook }} />)
    expect(screen.getByText('5-year outlook')).toBeInTheDocument()
    const tiles = within(screen.getByText('5th percentile').closest('dl')!)
    expect(tiles.getAllByRole('term').map((t) => t.textContent)).toEqual(['5th percentile', 'Median (zero drift)', '95th percentile'])
    expect(tiles.getAllByRole('definition').map((d) => d.textContent)).toEqual(['$96.44', '-80.1%', '$483.52', '0.0%', '$2,424', '+401.4%'])
    expect(screen.queryByText(/96\.4379/)).toBeNull()
    expect(screen.queryByText(/Bear|Bull/)).toBeNull()
    expect(screen.getByText('Volatility model · not a forecast of direction')).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'Our view' })).toBeNull()
  })
})

describe('PriceOutlookCard · view', () => {
  const card = (data: PriceOutlook = fx.tsmOutlookView, sources?: Map<number, SourceRef>) =>
    renderPage(<AgentCard card={{ type: 'price_outlook', data }} sources={sources} />, { routes: { '/forecasts/new': <Idea /> } })
  const rows = (table: HTMLElement) => within(table).getAllByRole('row').slice(1).map((r) => [...r.querySelectorAll('th, td')].map((c) => c.textContent))

  it('leads with the view: median above today, the 5th/95th percentiles, never Bear/Bull', () => {
    card()
    expect(screen.getByText('5-year view')).toBeInTheDocument()
    expect(screen.getByText('TSM in 5 years')).toBeInTheDocument()
    expect(screen.getByText('You asked by when it could reach 10×: odds by year, with the view at 5 years')).toBeInTheDocument()
    const hero = screen.getByRole('region', { name: 'Our view' })
    expect(within(hero).getByText('Our scenarios (subjective weights)')).toBeInTheDocument()
    expect(within(hero).getByText('Median $672.25')).toBeInTheDocument()
    expect(within(hero).getByText('+39% · +6.7%/yr')).toHaveClass('text-up')
    expect(within(hero).getAllByRole('term').map((t) => `${t.textContent} ${t.nextElementSibling?.textContent}`)).toEqual([
      'Above today 65%',
      '5th percentile $161.90',
      '95th percentile $2,690',
    ])
    expect(within(hero).getByText('-67%')).toBeInTheDocument()
    expect(within(hero).getByText('+455%')).toBeInTheDocument()
    expect(within(hero).getByText('Middle half $372.89 – $1,200')).toBeInTheDocument()
    expect(within(hero).queryByText('Assumption')).toBeNull()
    expect(screen.queryByText(/Bear ·|Bull ·/)).toBeNull()
  })

  it('weights the buckets by the view with a hint per range and a baseline tick on each bar', () => {
    const { container } = card()
    const ranges = screen.getByRole('region', { name: 'Price ranges' })
    expect(within(ranges).getByText('Where it ends in 5 years')).toBeInTheDocument()
    expect(within(ranges).getByText('volatility-only baseline')).toBeInTheDocument()
    const items = within(ranges).getAllByRole('listitem')
    expect(items.map((li) => li.querySelector('[title]')?.textContent)).toEqual(['< $250', '$250 – $360', '$360 – $485', '$485 – $750', '$750 – $1,000', '$1,000 – $1,500', '≥ $1,500'])
    expect(within(items[0]).getByText('below -48%')).toBeInTheDocument()
    expect(within(items[3]).getByText('0.0% … +55%')).toBeInTheDocument()
    expect(within(items[6]).getByText('above +209%')).toBeInTheDocument()
    // Upside ranges hold more than the baseline, downside ranges less.
    expect(items[0]).toHaveTextContent(/below -48%13%, volatility-only baseline 22%$/)
    expect(items[6]).toHaveTextContent(/above \+209%17%, volatility-only baseline 9%$/)
    const ticks = [...container.querySelectorAll<HTMLElement>('[data-baseline]')]
    // Widths and ticks are drawn against the largest share (the baseline's 21.9%), so a 7-bucket view still reads.
    expect(ticks.map((t) => t.style.left)).toEqual(['99.5%', '66.07%', '62.32%', '89.12%', '48.62%', '48.12%', '42.53%'])
    const bars = [...ranges.querySelectorAll<HTMLElement>('.animate-bar')]
    expect(bars.map((b) => b.style.width)).toEqual(['58.6%', '49.83%', '53.34%', '89.54%', '58.08%', '67.94%', '79.44%'])
  })

  it('shows the multiples under the view beside the baseline: 2× more likely than 0.5×', () => {
    card()
    const table = within(screen.getByRole('region', { name: 'Odds of a price multiple' })).getByRole('table')
    expect(within(table).getAllByRole('columnheader').map((h) => h.textContent)).toEqual(['Multiple', 'Ends beyond', 'Ever trades there', 'Baseline'])
    expect(rows(table)).toEqual([
      ['0.5×$242.50', '12%', '24%', '21%'],
      ['2×$970', '34%', '49%', '21%'],
      ['5×$2,425', '6%', '10%', '3%'],
      ['10×$4,850', '1%', '1%', '<1%'],
    ])
  })

  it('answers by when: the median time, the odds by year, years per scenario and the required-CAGR ladder', () => {
    card()
    const when = screen.getByRole('region', { name: 'By when' })
    expect(within(when).getByText('Median time to 10× ($4,850): not within 30 years')).toBeInTheDocument()
    const table = within(when).getByRole('table', { name: 'Chance of reaching 10× TSM within each number of years' })
    expect(rows(table)).toEqual([
      ['1 yr', '<1%', '<1%', '<1%'],
      ['3 yrs', '<1%', '<1%', '<1%'],
      ['5 yrs', '1%', '1%', '<1%'],
      ['10 yrs', '12%', '15%', '3%'],
      ['15 yrsextrapolated', '≈25%', '≈31%', '6%'],
      ['20 yrsextrapolated', '≈35%', '≈42%', '9%'],
    ])
    expect(within(when).getByText(/≈: past twice the scenarios’ horizon/)).toBeInTheDocument()
    expect(within(within(when).getByRole('list', { name: 'Years to the target per scenario' })).getAllByRole('listitem').map((li) => li.textContent)).toEqual([
      'bear -6.5%/yr → never',
      'base +9.2%/yr → 26 years',
      'bull +22.3%/yr → 11 years',
    ])
    expect(within(when).getByText(/^Needs/).parentElement).toHaveTextContent(
      "Needs 115.4%/yr for 3 years · 58.5% for 5 · 25.9% for 10 · 16.6% for 15 · 12.2% for 20 · TSM's own CAGR since 1997: +13.2%/yr",
    )
  })

  it('marks a median time past twice the scenarios’ horizon as extrapolated', () => {
    const tsm = fx.tsmOutlookView
    const ttt = { ...tsm.timeToTarget!, medianYears: 20.2, medianExtrapolated: true }
    const { unmount } = card({ ...tsm, timeToTarget: ttt })
    const when = screen.getByRole('region', { name: 'By when' })
    expect(within(when).getByText(/^Median time to 10×/)).toHaveTextContent('Median time to 10× ($4,850): ≈20 years (extrapolated)')
    expect(when).toHaveTextContent('≈: past twice the scenarios’ horizon, each scenario’s annual return is assumed to persist.')
    unmount()
    card({ ...tsm, timeToTarget: { ...ttt, medianYears: 8.4, medianExtrapolated: false } })
    expect(screen.getByText(/^Median time to 10×/)).toHaveTextContent('Median time to 10× ($4,850): about 8.4 years')
  })

  it('says not within 30 years when the median time is past the grid', () => {
    card({ ...fx.tsmOutlookView, timeToTarget: { ...fx.tsmOutlookView.timeToTarget!, medianYears: null, ownCagrPct: null, byScenario: [], requiredCagr: [] } })
    const when = screen.getByRole('region', { name: 'By when' })
    expect(within(when).getByText('Median time to 10× ($4,850): not within 30 years')).toBeInTheDocument()
    expect(within(when).queryByText(/^Needs/)).toBeNull()
    expect(within(when).queryByRole('list')).toBeNull()
  })

  it('lists the scenarios with their inputs: a cited source links to it, assumptions and model knowledge are marked', () => {
    card(fx.tsmOutlookView, new Map([[1, fx.tsmSource]]))
    const list = screen.getByRole('region', { name: 'Scenarios' })
    expect(within(list).getByText('Scenarios · subjective weights')).toBeInTheDocument()
    const bear = within(list).getByRole('list', { name: 'AI capex digestion inputs' })
    const chips = within(bear).getAllByRole('listitem')
    expect(chips.map((c) => c.textContent)).toEqual(['FY2027 EPS $21.33 · [1]', 'EPS growth 3%/yr to FY2032 · assumption', 'P/E 14× · model knowledge 2025-06'])
    expect(within(chips[0]).getByRole('link', { name: '[1]' })).toHaveAttribute('href', 'https://news.example.com/tsmc-eps')
    expect(chips[0]).toHaveClass('bg-surface-2')
    expect(chips[1]).not.toHaveClass('border-dashed')
    expect(chips[2]).toHaveClass('border-dashed')
    expect(within(list).getByText('$346.18').parentElement).toHaveTextContent('$346.1830%')
    expect(within(list).getByText('+174% · +22.3%/yr')).toBeInTheDocument()
    expect(within(list).getByText('-29% · -6.5%/yr')).toBeInTheDocument()
  })

  it('keeps an uncited [n] as plain text', () => {
    card()
    const chip = within(screen.getByRole('list', { name: 'AI supercycle inputs' })).getAllByRole('listitem')[0]
    expect(chip).toHaveTextContent('FY2027 EPS $21.33 · [1]')
    expect(within(chip).queryByRole('link')).toBeNull()
  })

  it('states the history line from the listing and folds the volatility-only baseline, closed', async () => {
    const { user } = card()
    expect(screen.getByText(/^Volatility 38%\/yr/)).toHaveTextContent(
      'Volatility 38%/yr · CAGR since 1997 +13.2%/yr · max drawdown -92% (2016–2026, not recovered) · Yahoo Finance daily closes (NYSE: TSM), 1997–2026',
    )
    const details = screen.getByText('Volatility-only baseline (zero drift): median = today').closest('details')!
    expect(details).not.toHaveAttribute('open')
    await user.click(screen.getByText('Volatility-only baseline (zero drift): median = today'))
    expect(details).toHaveAttribute('open')
    expect(within(details).getByText('Not a forecast of direction')).toBeInTheDocument()
    expect(within(details).getAllByRole('term').map((t) => t.textContent)).toEqual(['5th percentile', 'Median', '95th percentile'])
    expect(within(details).getByText('$119')).toBeInTheDocument()
    expect(rows(within(details).getByRole('table', { name: 'Odds of each multiple of spot over 5 years, zero drift' }))[0]).toEqual(['0.5×$242.50', '21%', '42%'])
    expect(within(details).getByText(/^38% annualized vol · long-run volatility since Oct 1, 1997 \(7,569 daily closes\) · Yahoo Finance daily closes \(NYSE: TSM\) · lognormal, zero drift$/)).toBeInTheDocument()
  })

  it('lists the warnings and offers a full forecast of the same horizon', async () => {
    const { user } = card()
    expect(within(screen.getByRole('list', { name: 'Notes' })).getByText('P/E 25× is not stated in source [1]: shown as an assumption')).toBeInTheDocument()
    await user.click(screen.getByRole('link', { name: 'Run a full forecast' }))
    expect(screen.getByText('idea: TSM price in 5 years (spot $485.00): where will it trade?')).toBeInTheDocument()
  })

  it('labels a historical-drift view as an assumption and notes a listing price apart from the perp', () => {
    const view = { ...fx.tsmOutlookView.view!, kind: 'historical_drift' as const, label: 'Historical drift since 1997, halved (assumption)', muAnnPct: 6.2, listingSpot: 497.3 }
    card({ ...fx.tsmOutlookView, view, scenarios: undefined, timeToTarget: undefined, warnings: undefined })
    const hero = screen.getByRole('region', { name: 'Our view' })
    expect(within(hero).getByText('Assumption')).toBeInTheDocument()
    expect(within(hero).getByText('Historical drift since 1997, halved (assumption)')).toBeInTheDocument()
    expect(within(hero).getByText(/· drift \+6\.2%\/yr$/)).toBeInTheDocument()
    expect(screen.getByText('NYSE $497.30')).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'Scenarios' })).toBeNull()
    expect(screen.queryByRole('region', { name: 'By when' })).toBeNull()
    expect(screen.queryByRole('list', { name: 'Notes' })).toBeNull()
  })

  it('marks a volatility-only view as no view, at zero change', () => {
    const view = { ...fx.tsmOutlookView.view!, kind: 'none' as const, label: 'No directional view: volatility-only', median: 485, changePct: 0, annualPct: 0 }
    card({ ...fx.tsmOutlookView, view, history: null })
    const hero = screen.getByRole('region', { name: 'Our view' })
    expect(within(hero).getByText('No view')).toBeInTheDocument()
    expect(within(hero).getByText('0.0% · 0.0%/yr')).toHaveClass('text-muted')
    expect(screen.queryByText(/^Volatility \d/)).toBeNull()
  })

  it('reads a short view by days, and a bearish one in red', () => {
    const view = { ...fx.tsmOutlookView.view!, changePct: -3.2, annualPct: null, buckets: undefined, multiples: undefined }
    card({ ...fx.tsmOutlookView, horizonDays: 90, horizon: { days: 90, basis: 'explicit', reason: 'From your question: "next 3 months"' }, view, timeToTarget: undefined })
    expect(screen.getByText('Price view · 90 days')).toBeInTheDocument()
    expect(screen.getByText('TSM in 90 days')).toBeInTheDocument()
    expect(within(screen.getByRole('region', { name: 'Our view' })).getByText('-3.2%')).toHaveClass('text-down')
    expect(screen.queryByRole('region', { name: 'Price ranges' })).toBeNull()
    expect(screen.queryByRole('region', { name: 'Odds of a price multiple' })).toBeNull()
  })

  it('shows only the odds by year for a by-when question without a horizon', async () => {
    const { user } = card({ ...fx.tsmOutlookView, horizon: { days: null, basis: 'by_when', reason: 'You asked "how long until": odds by year' }, scenarios: undefined, view: { ...fx.tsmOutlookView.view!, buckets: undefined } })
    expect(screen.getByText('By when 10×')).toBeInTheDocument()
    expect(screen.getByText('TSM to 10×')).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'Our view' })).toBeNull()
    expect(screen.queryByText(/Volatility-only baseline/)).toBeNull()
    expect(screen.getByRole('region', { name: 'By when' })).toBeInTheDocument()
    await user.click(screen.getByRole('link', { name: 'Run a full forecast' }))
    expect(screen.getByText('idea: Can TSM reach 10× ($4,850), and by when? (spot $485.00): where will it trade?')).toBeInTheDocument()
  })
})

describe('PriceOutlookCard review fixes', () => {
  const card = (data: PriceOutlook, sources?: Map<number, SourceRef>) => renderPage(<AgentCard card={{ type: 'price_outlook', data }} sources={sources} />)
  const tsm = fx.tsmOutlookView

  it('reads the drawdown peak to trough, a same-year one once, and an older card by its peak alone', () => {
    const { unmount } = card({ ...tsm, history: { ...tsm.history!, maxDrawdownPeak: '2008-01-02', maxDrawdownTrough: '2008-11-20', recovered: null } })
    expect(screen.getByText(/^Volatility 38%\/yr/)).toHaveTextContent('max drawdown -92% (2008, not recovered)')
    unmount()
    card({ ...tsm, history: { ...tsm.history!, maxDrawdownPeak: '2000-03-08', maxDrawdownTrough: undefined, recovered: '2010-12-23' } })
    expect(screen.getByText(/^Volatility 38%\/yr/)).toHaveTextContent('max drawdown -92% (peak 2000, recovered 2010)')
  })

  it('says when the view is spread by the assumed volatility, not a measured one', () => {
    const { unmount } = card({ ...tsm, view: { ...tsm.view!, volAnnPct: 30, volAssumed: true } })
    expect(within(screen.getByRole('region', { name: 'Our view' })).getByText(/assumed 30%\/yr volatility \(no measured history\)$/)).toBeInTheDocument()
    unmount()
    card(tsm)
    expect(screen.queryByText(/assumed .*volatility/)).toBeNull()
  })

  it('leaves the multiples off a view under a year, unless the question names a multiple', () => {
    const short = { ...tsm, horizonDays: 92, horizon: { days: 92, basis: 'explicit' as const, reason: 'From your question: "next 3 months"' }, longHorizon: undefined }
    const { unmount } = card({ ...short, timeToTarget: undefined })
    expect(screen.getByText('Price view · 92 days')).toBeInTheDocument()
    expect(screen.getByRole('region', { name: 'Price ranges' })).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'Odds of a price multiple' })).toBeNull()
    unmount()
    card(short)
    expect(screen.getByRole('region', { name: 'Odds of a price multiple' })).toBeInTheDocument()
  })

  it('names the view a by-when card without a horizon rests on, and its one path, and leaves its multiples off', () => {
    const view = { ...tsm.view!, kind: 'historical_drift' as const, label: 'Historical drift since 1997, halved (assumption)', muAnnPct: 6.2, buckets: undefined }
    const timeToTarget = { ...tsm.timeToTarget!, medianYears: null, byScenario: [{ name: 'drift', kind: 'base', annualPct: 6.39, years: 37.17 }] }
    card({ ...tsm, horizonDays: 3650, horizon: { days: null, basis: 'by_when', reason: 'You asked whether and when it could reach 10×: odds by year, not one horizon' }, scenarios: undefined, view, timeToTarget })
    const when = screen.getByRole('region', { name: 'By when' })
    expect(within(when).getByText(/^Under/)).toHaveTextContent('Under Assumption Historical drift since 1997, halved (assumption)')
    expect(within(within(when).getByRole('list', { name: 'Years to the target per scenario' })).getByRole('listitem')).toHaveTextContent('halved drift +6.4%/yr → 37 years')
    expect(screen.queryByRole('region', { name: 'Odds of a price multiple' })).toBeNull()
    expect(screen.queryByRole('region', { name: 'Our view' })).toBeNull()
  })

  it('does not repeat the view in the by-when section when the hero states it', () => {
    card(tsm)
    expect(within(screen.getByRole('region', { name: 'By when' })).queryByText(/^Under/)).toBeNull()
  })

  it('never shows an unverified source number as sourced', () => {
    const [bear, ...rest] = tsm.scenarios!
    const inputs = [{ ...bear.inputs[0], verified: false }, ...bear.inputs.slice(1)]
    card({ ...tsm, scenarios: [{ ...bear, inputs }, ...rest] }, new Map([[1, fx.tsmSource]]))
    const chip = within(screen.getByRole('list', { name: 'AI capex digestion inputs' })).getAllByRole('listitem')[0]
    expect(chip).toHaveTextContent('FY2027 EPS $21.33 · assumption')
    expect(chip).not.toHaveClass('bg-surface-2')
    expect(within(chip).queryByRole('link')).toBeNull()
  })

  it('lists a warning the API repeats (two scenarios citing the same missing P/E) twice, without a key clash', () => {
    const errors = vi.spyOn(console, 'error').mockImplementation(() => {})
    const w = 'P/E 25× is not stated in source [1]: shown as an assumption'
    card({ ...tsm, warnings: [w, w] })
    expect(within(screen.getByRole('list', { name: 'Notes' })).getAllByText(w)).toHaveLength(2)
    expect(errors.mock.calls.flat().join(' ')).not.toMatch(/same key/)
    errors.mockRestore()
  })
})

describe('PriceOutlookCard · fix round', () => {
  const card = (data: PriceOutlook) => renderPage(<AgentCard card={{ type: 'price_outlook', data }} />)
  const nvda = fx.nvdaOutlookView

  it('names a horizon the agent chose without a reason once, and keeps the API’s note to the model off the card', () => {
    card(fx.solAgentView)
    expect(screen.getByText('3-year view')).toBeInTheDocument()
    const line = screen.getByText('Horizon chosen by the agent: 1,095 days (3 years)').parentElement!
    expect(line).toHaveTextContent(/^Horizon chosen by the agent: 1,095 days \(3 years\)$/) // the API's line says it: no second tag
    expect(screen.queryByRole('list', { name: 'Notes' }), 'the API’s note to the model is not the reader’s').toBeNull()
    // Crypto has no benchmark: the 10%/yr ceiling did not bind, so the halved drift stands, uncapped.
    expect(within(screen.getByRole('region', { name: 'Our view' })).getByText('Middle half $203.49 – $298.99 · drift +7.0%/yr')).toBeInTheDocument()
    expect(screen.queryByText(/capped/)).toBeNull()
  })

  it('tags the agent’s own reasoned choice too, and leaves the user’s timeframe untagged', () => {
    const { unmount } = card({ ...fx.solAgentView, horizon: { days: 1095, basis: 'chosen', reason: 'No timeframe given: a long-term hold, 3 years' } })
    expect(screen.getByText('No timeframe given: a long-term hold, 3 years').parentElement).toHaveTextContent(/^Chosen by the agent/)
    unmount()
    card(nvda)
    expect(screen.getByText('From your question: "10 years outlook"')).toBeInTheDocument()
    expect(screen.queryByText('Chosen by the agent')).toBeNull()
  })

  it('leaves the user’s own timeframe untagged: a "user" horizon, and a stored "chosen" one whose reason quotes the user', () => {
    const line = (horizon: PriceOutlook['horizon']) => {
      const { unmount } = card({ ...fx.solAgentView, horizon })
      const tagged = screen.queryByText('Chosen by the agent') != null
      unmount()
      return tagged
    }
    expect(line({ days: 79, basis: 'user', reason: 'The user asked by the year-end close' })).toBe(false)
    expect(line({ days: 79, basis: 'chosen', reason: 'The user asked by December' })).toBe(false)
    expect(line({ days: 1825, basis: 'chosen', reason: 'long run: 5 years' })).toBe(true)
    expect(line({ days: 1095, basis: 'chosen', reason: "No timeframe given: the user didn't say, so a 3-year hold" })).toBe(true)
  })

  it('reads "NVDA 10 years outlook" as 10 years and says how the drift-only headline was capped', () => {
    card(nvda)
    expect(screen.getByText('10-year view')).toBeInTheDocument()
    expect(screen.getByText('NVDA in 10 years')).toBeInTheDocument()
    const hero = screen.getByRole('region', { name: 'Our view' })
    expect(within(hero).getByText('Assumption')).toBeInTheDocument()
    expect(within(hero).getByText("Historical drift since 1999, capped at the market's long-run return + 2 pts (assumption)")).toBeInTheDocument()
    expect(within(hero).getByText('Median $591.60')).toBeInTheDocument()
    expect(within(hero).getByText('+152% · +9.7%/yr')).toBeInTheDocument()
    // The capped return replaces the halved drift; the uncapped figure stays in view, named as the listing's own.
    expect(within(hero).getByText('Middle half $179 – $1,955 · drift capped at +9.7%/yr')).toBeInTheDocument()
    expect(within(hero).getByText("S&P 500 +7.7%/yr since 1966 + 2 pts; NVDA's own history, halved: +15.7%/yr")).toBeInTheDocument()
    expect(screen.queryByText(/horizon_days/)).toBeNull()
  })

  it('shows the ladder grown to eight ranges so a strong view is not one open top bucket', () => {
    card(nvda)
    const items = within(screen.getByRole('region', { name: 'Price ranges' })).getAllByRole('listitem')
    expect(items.map((li) => li.querySelector('[title]')?.textContent)).toEqual([
      '< $60',
      '$60 – $120',
      '$120 – $235',
      '$235 – $470',
      '$470 – $940',
      '$940 – $1,900',
      '$1,900 – $3,800',
      '≥ $3,800',
    ])
    expect(within(items[6]).getByText('+709% … +1,517%')).toBeInTheDocument()
    expect(items[7]).toHaveTextContent(/above \+1,517%15%, volatility-only baseline 6%$/)
  })

  it('names the 10%/yr ceiling when it binds or when no benchmark history was there', () => {
    const cap = nvda.view!.driftCap!
    const { unmount } = card({ ...nvda, view: { ...nvda.view!, driftCap: { ...cap, benchmarkCagrPct: 9.5, capPct: 10 } } })
    expect(screen.getByText("The 10%/yr ceiling (S&P 500 +9.5%/yr since 1966 + 2 pts is above it); NVDA's own history, halved: +15.7%/yr")).toBeInTheDocument()
    expect(screen.getByText(/drift capped at \+10\.0%\/yr$/)).toBeInTheDocument()
    unmount()
    card({ ...nvda, view: { ...nvda.view!, driftCap: { ...cap, benchmark: '', source: '', since: null, benchmarkCagrPct: null, capPct: 10 } } })
    expect(screen.getByText("The 10%/yr ceiling; NVDA's own history, halved: +15.7%/yr")).toBeInTheDocument()
  })

  it('names an unknown benchmark by its key, and drops the cap line on a scenarios view', () => {
    const cap = nvda.view!.driftCap!
    const { unmount } = card({ ...nvda, view: { ...nvda.view!, driftCap: { ...cap, benchmark: 'XYZ', since: null } } })
    expect(screen.getByText("XYZ +7.7%/yr + 2 pts; NVDA's own history, halved: +15.7%/yr")).toBeInTheDocument()
    unmount()
    card({ ...nvda, view: { ...nvda.view!, kind: 'scenarios', label: 'Our scenarios (subjective weights)', muAnnPct: null } })
    expect(screen.queryByText(/capped|own history, halved/)).toBeNull()
  })

  it('lists the thin-downside warning the API now gives only when the view’s tail is really too narrow', () => {
    card({ ...fx.tsmOutlookView, warnings: [fx.thinTailWarning] })
    expect(within(screen.getByRole('list', { name: 'Notes' })).getByText(fx.thinTailWarning)).toBeInTheDocument()
    expect(fx.thinTailWarning).toBe("The scenarios' downside is thinner than the volatility baseline's: 5th percentile 58% below the median, against 75%")
  })

  it('shows none of the notes a stored card carries for the model, and keeps the thin-downside numbers', () => {
    card({ ...fx.tsmOutlookView, warnings: [...fx.legacyModelHints, 'FX history unavailable: drift and CAGR measured in TWD, not USD'] })
    const notes = within(screen.getByRole('list', { name: 'Notes' })).getAllByRole('listitem').map((li) => li.textContent)
    expect(notes).toEqual([
      "The view's downside is thin: its 5th percentile ($267.38) is 58% below its median ($636.75), against 75% for the volatility baseline",
      'FX history unavailable: drift and CAGR measured in TWD, not USD',
    ])
    for (const hint of ['horizon_days', 'horizon_reason', 'pass scenarios', 'consider a tail', 'cited [', 'Your view', 'labelled assumption'])
      expect(notes.join(' '), hint).not.toContain(hint)
  })
})

describe('ForecastRefCard', () => {
  const tsmCard = (view?: typeof fx.tsmViewBrief): AgentCardType => ({
    type: 'forecast',
    data: { id: 'v1', question: 'What will be the NYSE closing price of TSM ADR on 2031-10-06?', shape: 'ordered', headline: { outcome: '≥ $1,100', p: 0.2 }, resolutionDate: '2031-10-06', view },
  })

  it('states a numeric report by its median and chance at or above today when its view leads', () => {
    renderPage(<AgentCard card={tsmCard(fx.tsmViewBrief)} />)
    expect(screen.getByText('Median $611')).toBeInTheDocument()
    expect(screen.getByText('62% ≥ today')).toBeInTheDocument()
    expect(screen.queryByText(/≥ \$1,100/)).toBeNull()
  })

  it('states a flat view with no basis by its 80% range, and keeps the top range without a view or when the top range leads', () => {
    const { unmount } = renderPage(<AgentCard card={tsmCard({ ...fx.tsmViewBrief, pAbove: null, basis: 'none' })} />)
    expect(screen.getByText('≈$242–≈$1,480')).toBeInTheDocument()
    expect(screen.getByText('80% range')).toBeInTheDocument()
    expect(screen.queryByText(/Median|≥ today/)).toBeNull()
    unmount()
    const { unmount: u2 } = renderPage(<AgentCard card={tsmCard()} />)
    expect(screen.getByText('≥ $1,100 20%')).toBeInTheDocument()
    u2()
    renderPage(<AgentCard card={tsmCard({ ...fx.tsmViewBrief, headline: 'bucket', flat: false })} />)
    expect(screen.getByText('≥ $1,100 20%')).toBeInTheDocument()
    expect(screen.queryByText(/Median/)).toBeNull()
  })

  it('shows the headline and links to the report', () => {
    renderPage(<AgentCard card={fx.forecastCard} />)
    expect(screen.getByText('Forecast desk')).toBeInTheDocument()
    expect(screen.getByText('Will ETH outperform BTC over Q4 2026?')).toHaveClass('line-clamp-2')
    expect(screen.getByText('Yes 62%')).toBeInTheDocument()
    expect(screen.getByText(/^Resolves Dec 31, 2026/)).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'View report' })).toHaveAttribute('href', '/forecasts/f1234567-aaaa')
  })

  it('renders nothing for a followups card', () => {
    const { container } = renderPage(<AgentCard card={fx.followupsCard} />)
    expect(container.querySelector('button, a, p')).toBeNull()
  })
})

import { screen, waitFor, within } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError } from '../../lib/api'
import type { NewsItem } from '../../lib/types'
import * as fx from '../../test/fixtures'
import { currentPath, renderPage, stubSession } from '../../test/utils'
import { NewsPage } from './NewsPage'
import { FearGreedTile } from './parts'

const btc = fx.news()
const macro = fx.news({ id: 'n2', title: 'Fed signals a pause', url: 'https://apnews.com/fed', source: 'AP', symbols: [], topic: 'macro', regions: ['us'], hasContent: false, description: '' })
const sol = fx.news({ id: 'n3', title: 'Solana DEX volume jumps', url: 'https://theblock.co/sol', source: 'The Block', symbols: ['SOL'], publishedAt: null })
const tsmc = fx.news({ id: 'n4', title: 'TSMC lifts its outlook', url: 'https://asia.nikkei.com/tsmc', source: 'Nikkei Asia', symbols: ['TSM'], regions: ['us', 'east_asia'] })

/** The unfiltered call answers [btc, macro, sol]; a filtered one answers byFilter["<region> <symbol|topic>"] (e.g. "us macro"). */
function stubWire(byFilter: Record<string, NewsItem[]> = {}) {
  return vi.spyOn(api, 'news').mockImplementation(async (f = {}) => {
    const key = [f.region, f.symbol ?? f.topic].filter(Boolean).join(' ')
    return { news: key ? (byFilter[key] ?? []) : [btc, macro, sol] }
  })
}

const open = (route = '/news') => renderPage(<NewsPage />, { route, path: '/news' })
const headlines = () => within(screen.getAllByRole('list').at(-1)!).getAllByRole('heading', { level: 3 }).map((h) => h.textContent)
const filters = () => screen.getByRole('group', { name: 'Filter headlines' })
const regions = () => screen.getByRole('group', { name: 'Filter by region' })
const pressed = (group: HTMLElement) => within(group).getByRole('button', { pressed: true }).textContent
const row = (title: string) => screen.getByRole('link', { name: title }).closest('li')!

const hoursAgo = (h: number) => new Date(Date.now() - h * 3_600_000).toISOString()
const fresh = { ...fx.brief, createdAt: hoursAgo(2) }

beforeEach(() => {
  stubSession()
  vi.spyOn(api, 'brief').mockResolvedValue({ brief: fresh })
})

describe('NewsPage', () => {
  it('leads with the market brief and its sourced bullets', async () => {
    stubWire()
    open()
    expect(screen.getByLabelText('Loading the market brief')).toHaveAttribute('aria-busy', 'true')
    expect(await screen.findByRole('heading', { level: 1, name: 'Risk-on into the Fed' })).toBeInTheDocument()
    expect(screen.getByText('Crypto firm as ETF inflows extend.')).toBeInTheDocument()
    const bullet = screen.getByText('BTC ETF inflows extend').closest('li')!
    expect(bullet).toHaveTextContent('01')
    expect(within(bullet).getByRole('link', { name: 'reuters.com' })).toHaveAttribute('href', btc.url) // source id → headline
    expect(within(bullet).getByRole('button', { name: 'BTC' })).toBeInTheDocument()
    expect(screen.getByText('AI summary of 42 headlines · refreshes every 2 hours')).toBeInTheDocument()
    expect(screen.getByText('Simulated · not financial advice')).toBeInTheDocument()
    expect(screen.queryByRole('list', { name: 'Brief by region' }), 'no breakdown in this brief').toBeNull()
  })

  it('links to the signals board (its entry point on phones)', async () => {
    stubWire()
    open()
    expect(screen.getByRole('link', { name: 'Signals board →' })).toHaveAttribute('href', '/signals')
    await screen.findByRole('heading', { level: 1 })
  })

  it('shows the whole universe in the sentiment strip', async () => {
    stubWire()
    open()
    await screen.findByRole('heading', { level: 1, name: 'Risk-on into the Fed' })
    const cells = within(screen.getByRole('list', { name: 'Sentiment by asset' })).getAllByRole('button')
    expect(cells).toHaveLength(12)
    expect(cells[0]).toHaveTextContent('BTC▲bullish')
    expect(cells[0]).toHaveAttribute('title', 'ETF inflows')
    expect(cells[1]).toHaveTextContent('ETH·no news')
    expect(cells[11]).toHaveTextContent('PAXG▼bearish')
  })

  it('lists every headline with its badges and asset chips', async () => {
    stubWire()
    open()
    await waitFor(() => expect(headlines()).toEqual([btc.title, macro.title, sol.title]))
    const first = screen.getByRole('link', { name: btc.title })
    expect(first).toHaveAttribute('href', btc.url)
    expect(first).toHaveAttribute('target', '_blank')
    expect(first).toHaveAttribute('rel', 'noopener noreferrer')
    const row = first.closest('li')!
    expect(within(row).getByTitle('The desk read the full article')).toHaveTextContent('Read')
    expect(row).toHaveTextContent('Spot funds took in $420m.')
    expect(screen.getByRole('link', { name: macro.title }).closest('li')).toHaveTextContent('AP·')
    expect(screen.getByRole('link', { name: macro.title }).closest('li')).toHaveTextContent('Macro')
  })

  it('badges each headline with its regions', async () => {
    vi.spyOn(api, 'news').mockResolvedValue({ news: [btc, macro, tsmc, fx.news({ id: 'g', title: 'Global story', url: 'https://x.io/g', regions: [] })] })
    open()
    await waitFor(() => expect(headlines()).toHaveLength(4))
    expect(within(row(btc.title)).getByText('Crypto')).toBeInTheDocument()
    expect(within(row(macro.title)).getByText('US')).toBeInTheDocument()
    expect(within(row(tsmc.title)).getByText('US')).toBeInTheDocument()
    expect(within(row(tsmc.title)).getByText('East Asia')).toBeInTheDocument()
    for (const label of ['Crypto', 'US', 'East Asia', 'Southeast Asia']) expect(within(row('Global story')).queryByText(label)).toBeNull()
  })

  it('switches region tabs on the server and keeps the choice in the URL', async () => {
    const news = stubWire({ east_asia: [tsmc], us: [macro, tsmc] })
    const { user } = open()
    await waitFor(() => expect(headlines()).toHaveLength(3))
    expect(within(regions()).getAllByRole('button').map((b) => b.textContent)).toEqual(['All', 'Crypto', 'US', 'East Asia', 'Southeast Asia'])
    expect(pressed(regions())).toBe('All')
    expect(news.mock.calls.every(([f]) => !f?.region), 'All sends no region').toBe(true)

    await user.click(within(regions()).getByRole('button', { name: 'East Asia' }))
    expect(currentPath()).toBe('/news?region=east_asia')
    expect(news).toHaveBeenCalledWith({ region: 'east_asia', limit: 100 })
    await waitFor(() => expect(headlines()).toEqual([tsmc.title]))
    expect(pressed(regions())).toBe('East Asia')
    expect(screen.getByRole('heading', { level: 2, name: 'East Asia headlines' })).toBeInTheDocument()

    await user.click(within(regions()).getByRole('button', { name: 'US' }))
    expect(currentPath()).toBe('/news?region=us')
    expect(news).toHaveBeenCalledWith({ region: 'us', limit: 100 })
    await waitFor(() => expect(headlines()).toEqual([macro.title, tsmc.title]))

    news.mockClear()
    await user.click(within(regions()).getByRole('button', { name: 'All' }))
    expect(currentPath()).toBe('/news')
    await waitFor(() => expect(headlines()).toHaveLength(3))
    expect(news.mock.calls.map(([f]) => f)).toEqual([{ limit: 100 }])
  })

  it('opens a shared region link on that region', async () => {
    const news = stubWire({ east_asia: [tsmc] })
    open('/news?region=east_asia')
    await waitFor(() => expect(headlines()).toEqual([tsmc.title]))
    expect(pressed(regions())).toBe('East Asia')
    expect(news).toHaveBeenCalledWith({ region: 'east_asia', limit: 100 })
  })

  it('treats an unknown ?region= as All', async () => {
    const news = stubWire()
    open('/news?region=europe')
    await waitFor(() => expect(headlines()).toHaveLength(3))
    expect(pressed(regions())).toBe('All')
    expect(news.mock.calls.every(([f]) => !f?.region)).toBe(true)
  })

  it('combines the region with the macro and asset chips', async () => {
    const news = stubWire({ 'us macro': [macro], east_asia: [tsmc], 'east_asia TSM': [tsmc] })
    const { user } = open('/news?region=us')
    await waitFor(() => expect(pressed(regions())).toBe('US'))

    await user.click(within(filters()).getByRole('button', { name: 'Macro' }))
    expect(news).toHaveBeenCalledWith({ topic: 'macro', region: 'us', limit: 50 })
    await waitFor(() => expect(headlines()).toEqual([macro.title]))
    expect(screen.getByRole('heading', { level: 2, name: 'US macro headlines' })).toBeInTheDocument()

    await user.click(within(regions()).getByRole('button', { name: 'East Asia' })) // the chip stays on
    expect(news).toHaveBeenCalledWith({ topic: 'macro', region: 'east_asia', limit: 50 })
    expect(await screen.findByText('No East Asia macro headlines in the last 14 days.')).toBeInTheDocument()
    expect(pressed(filters())).toBe('Macro')

    await user.click(screen.getByRole('button', { name: 'Show all East Asia headlines' }))
    await waitFor(() => expect(headlines()).toEqual([tsmc.title]))
    expect(pressed(filters())).toBe('All')
    expect(currentPath()).toBe('/news?region=east_asia')

    await user.click(within(row(tsmc.title)).getByTitle('Show TSM headlines'))
    expect(news).toHaveBeenCalledWith({ symbol: 'TSM', region: 'east_asia', limit: 50 })
    await waitFor(() => expect(screen.getByRole('heading', { level: 2, name: 'East Asia TSM headlines' })).toBeInTheDocument())
  })

  it('says when a region has no headlines and offers every region', async () => {
    stubWire()
    const { user } = open('/news?region=southeast_asia')
    expect(await screen.findByText('No Southeast Asia headlines in the last 14 days.')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Show all headlines' })).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Show every region' }))
    expect(currentPath()).toBe('/news')
    await waitFor(() => expect(headlines()).toHaveLength(3))
  })

  it("shows the brief's region breakdown, and a region there picks the tab", async () => {
    vi.spyOn(api, 'brief').mockResolvedValue({
      brief: { ...fx.brief, regions: [{ region: 'east_asia', summary: 'TSMC guides higher.' }, { region: 'us', summary: 'The Fed holds.' }] },
    })
    stubWire({ east_asia: [tsmc] })
    const { user } = open()
    const breakdown = await screen.findByRole('list', { name: 'Brief by region' })
    expect(within(breakdown).getAllByRole('listitem').map((li) => li.textContent)).toEqual(['USThe Fed holds.', 'East AsiaTSMC guides higher.'])

    await user.click(within(breakdown).getByRole('button', { name: 'East Asia' }))
    expect(currentPath()).toBe('/news?region=east_asia')
    expect(within(breakdown).getByRole('button', { name: 'East Asia' })).toHaveAttribute('aria-pressed', 'true')
    expect(pressed(regions())).toBe('East Asia')
    await waitFor(() => expect(headlines()).toEqual([tsmc.title]))
  })

  it('filters by asset and macro on the server, and toggles back to all', async () => {
    const news = stubWire({ SOL: [sol], macro: [macro] })
    const { user } = open()
    await waitFor(() => expect(headlines()).toHaveLength(3))

    await user.click(within(filters()).getByRole('button', { name: 'SOL' }))
    expect(news).toHaveBeenCalledWith({ symbol: 'SOL', limit: 50 })
    await waitFor(() => expect(headlines()).toEqual([sol.title]))
    expect(within(filters()).getByRole('button', { name: 'SOL' })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByRole('heading', { level: 2, name: 'SOL headlines' })).toBeInTheDocument()

    await user.click(within(filters()).getByRole('button', { name: 'Macro' }))
    expect(news).toHaveBeenCalledWith({ topic: 'macro', limit: 50 })
    await waitFor(() => expect(headlines()).toEqual([macro.title]))

    await user.click(within(filters()).getByRole('button', { name: 'SOL' }))
    await waitFor(() => expect(headlines()).toEqual([sol.title]))
    await user.click(screen.getByTitle('Show SOL headlines')) // the row's chip for the active asset → back to all
    await waitFor(() => expect(headlines()).toHaveLength(3))
    expect(within(filters()).getByRole('button', { name: 'All' })).toHaveAttribute('aria-pressed', 'true')
  })

  it('filters from a sentiment cell and offers a way back from an empty filter', async () => {
    stubWire()
    const { user } = open()
    await screen.findByRole('heading', { level: 1, name: 'Risk-on into the Fed' })
    await user.click(within(screen.getByRole('list', { name: 'Sentiment by asset' })).getAllByRole('button')[1]) // ETH
    expect(await screen.findByText('No ETH headlines right now.')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Show all headlines' }))
    await waitFor(() => expect(headlines()).toHaveLength(3))
  })

  it('says when the wire is quiet', async () => {
    vi.spyOn(api, 'brief').mockResolvedValue({ brief: null })
    vi.spyOn(api, 'news').mockResolvedValue({ news: [] })
    open()
    expect(await screen.findByRole('heading', { name: 'The wire is quiet.' })).toBeInTheDocument()
    expect(await screen.findByText('No headlines on the wire yet.')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Show all headlines' })).toBeNull()
    expect(screen.queryByText('No brief right now.')).toBeNull()
    expect(screen.getByRole('heading', { level: 2, name: 'Sentiment · no brief right now' })).toBeInTheDocument()
  })

  it('says there is no brief right now while the headlines keep coming, and the strip says "no brief"', async () => {
    vi.spyOn(api, 'brief').mockResolvedValue({ brief: null })
    stubWire()
    open()
    expect(await screen.findByRole('heading', { level: 1, name: 'No brief right now.' })).toBeInTheDocument()
    expect(screen.getByText('Market wire · brief being rewritten')).toBeInTheDocument()
    expect(screen.getByText('The latest headlines are below; a new brief is written from them every 2 hours.')).toBeInTheDocument()
    expect(screen.queryByText('The wire is quiet.')).toBeNull()
    expect(screen.queryByText(/Nothing has come in yet/)).toBeNull()
    await waitFor(() => expect(headlines()).toEqual([btc.title, macro.title, sol.title]))
    // The strip's heading says the same as its cells: there is no brief to read the sentiment from.
    expect(screen.getByRole('heading', { level: 2, name: 'Sentiment · no brief right now' })).toBeInTheDocument()
    expect(screen.queryByText('Sentiment · from the latest brief')).toBeNull()
    const cells = within(screen.getByRole('list', { name: 'Sentiment by asset' })).getAllByRole('button')
    expect(cells).toHaveLength(12)
    for (const c of cells) {
      expect(c).toHaveTextContent(/·no brief$/)
      expect(c).toHaveAttribute('title', 'No brief right now')
    }
  })

  it('marks a brief over 6 h old as stale, without the live dot', async () => {
    vi.spyOn(api, 'brief').mockResolvedValue({ brief: { ...fx.brief, createdAt: hoursAgo(9) } })
    stubWire()
    open()
    const hero = (await screen.findByRole('heading', { level: 1, name: 'Risk-on into the Fed' })).closest('section')!
    const label = within(hero).getByText(/^Market wire · brief from 9h ago/)
    expect(within(label).getByText('stale')).toHaveAttribute('title', expect.stringMatching(/^Over 6 hours old/))
    expect(hero.querySelector('.animate-pulse')).toBeNull()
    expect(within(hero).queryByText(/updated/)).toBeNull()
    expect(within(screen.getByRole('list', { name: 'Sentiment by asset' })).getAllByRole('button')[0]).toHaveTextContent('BTC▲bullish') // still its read
  })

  it('keeps the live dot on a fresh brief', async () => {
    stubWire()
    open()
    const hero = (await screen.findByRole('heading', { level: 1, name: 'Risk-on into the Fed' })).closest('section')!
    expect(within(hero).getByText('Market wire · updated 2h ago')).toBeInTheDocument()
    expect(hero.querySelector('.animate-pulse')).not.toBeNull()
    expect(within(hero).queryByText('stale')).toBeNull()
    expect(screen.getByRole('heading', { level: 2, name: 'Sentiment · from the latest brief' })).toBeInTheDocument()
  })

  it("cites a bullet's source from the brief's stored sources when the wire no longer lists it", async () => {
    const old = { id: 'n-old', title: 'Bank of Korea holds', url: 'https://www.yna.co.kr/boj', source: 'Yonhap', publishedAt: hoursAgo(30) }
    vi.spyOn(api, 'brief').mockResolvedValue({
      brief: {
        ...fresh,
        bullets: [{ text: 'BOK on hold as the won firms', symbols: [], sources: ['n-old', 'n1'] }],
        // n1 is also on the wire: the brief's stored copy wins.
        sources: [old, { id: 'n1', title: 'Spot BTC ETFs extend inflows', url: 'https://www.reuters.com/btc-etf-stored', source: 'Reuters', publishedAt: null }],
      },
    })
    stubWire()
    open()
    const bullet = (await screen.findByText('BOK on hold as the won firms')).closest('li')!
    const links = within(bullet).getAllByRole('link')
    expect(links.map((l) => [l.textContent, l.getAttribute('href'), l.getAttribute('title')])).toEqual([
      ['Yonhap', 'https://www.yna.co.kr/boj', 'Bank of Korea holds'],
      ['Reuters', 'https://www.reuters.com/btc-etf-stored', 'Spot BTC ETFs extend inflows'],
    ])
  })

  it('shows a full-page retry when the first load fails', async () => {
    const news = vi.spyOn(api, 'news').mockRejectedValueOnce(new ApiError(502, 'upstream', 'Search is unavailable'))
    const { user } = open()
    expect(await screen.findByRole('heading', { name: "Couldn't load the market wire." })).toBeInTheDocument()
    expect(screen.getByText('Search is unavailable')).toBeInTheDocument()
    news.mockResolvedValue({ news: [btc] })
    await user.click(screen.getByRole('button', { name: 'Try again' }))
    expect(await screen.findByRole('heading', { level: 1, name: 'Risk-on into the Fed' })).toBeInTheDocument()
  })

  it('keeps the wire on screen when a filter load fails', async () => {
    const news = stubWire()
    const { user } = open()
    await waitFor(() => expect(headlines()).toHaveLength(3))
    news.mockRejectedValueOnce(new ApiError(0, 'network', 'Cannot reach the server.'))
    await user.click(within(filters()).getByRole('button', { name: 'ADA' }))
    expect(await screen.findByText('Cannot reach the server.')).toBeInTheDocument()
    expect(screen.getByRole('status')).toHaveTextContent("Couldn't refresh · retrying")
    expect(screen.getByRole('heading', { level: 1, name: 'Risk-on into the Fed' })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Try again' }))
    expect(await screen.findByText('No ADA headlines right now.')).toBeInTheDocument()
  })
})

describe('FearGreedTile', () => {
  const TIP = 'Crypto Fear & Greed Index by alternative.me, updated daily'

  it('shows the index with its label and a source tooltip', async () => {
    vi.spyOn(api, 'getPulse').mockResolvedValue(fx.pulse)
    renderPage(<FearGreedTile />)
    expect(await screen.findByTitle(TIP)).toHaveTextContent('Fear & Greed 71 · Greed')
  })

  it.each([
    ['null', () => vi.spyOn(api, 'getPulse').mockResolvedValue({ fearGreed: null })],
    ['an error', () => vi.spyOn(api, 'getPulse').mockRejectedValue(new ApiError(500, 'internal', 'down'))],
  ])('is hidden on %s', async (_, stub) => {
    const spy = stub()
    renderPage(<FearGreedTile />)
    await waitFor(() => expect(spy).toHaveBeenCalled())
    expect(screen.queryByTitle(TIP)).toBeNull()
    expect(screen.queryByText(/Fear & Greed/)).toBeNull()
  })

  it('sits in the News page header', async () => {
    vi.spyOn(api, 'getPulse').mockResolvedValue(fx.pulse)
    vi.spyOn(api, 'news').mockResolvedValue({ news: [] })
    renderPage(<NewsPage />)
    expect(await screen.findByTitle(TIP)).toHaveTextContent('Fear & Greed 71 · Greed')
  })
})

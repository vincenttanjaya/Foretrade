import { screen, within } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from '../../lib/api'
import * as fx from '../../test/fixtures'
import { renderPage, stubSession } from '../../test/utils'
import { Welcome } from './Welcome'

beforeEach(() => {
  stubSession()
  vi.spyOn(api, 'brief').mockResolvedValue({ brief: null })
})

const open = () => {
  const onSend = vi.fn()
  return { onSend, ...renderPage(<Welcome onSend={onSend} onStop={() => {}} streaming={false} mode="quality" onModeChange={() => {}} lens="auto" onLensChange={() => {}} />) }
}

describe('Welcome', () => {
  it.each(['Review my trades and suggest improvements', 'How are my automations doing?'])('sends the account review prompt %s once without attachments', async (prompt) => {
    const { user, onSend } = open()
    await user.click(screen.getByRole('button', { name: 'Portfolio' }))
    await user.click(screen.getByRole('button', { name: prompt }))
    expect(onSend.mock.calls).toEqual([[prompt]])
  })
  it('says what paper trading covers, counting the live universe once it loads', async () => {
    const twelve = Array.from({ length: 12 }, (_, i) => ({ ...fx.assets[0], symbol: `A${i}` }))
    vi.spyOn(api, 'assets').mockResolvedValue({ assets: twelve })
    open()
    expect(screen.getByText(/Loading crypto, US and Asia stock, ETF and index perp quotes/)).toBeInTheDocument()
    expect(await screen.findByText(/Live quotes: 12 crypto pairs · 0 US stock perps · 0 index perps · 0 Asia stock & ETF perps/)).toBeInTheDocument()
  })

  it('counts crypto, stock and index quote classes without presenting configured coverage as live availability', async () => {
    vi.spyOn(api, 'assets').mockResolvedValue({ assets: [...fx.assets, { ...fx.assets[0], symbol: 'AAPL', name: 'Apple', pair: 'xyz:AAPL', class: 'stock', venue: 'hyperliquid' }, { ...fx.assets[0], symbol: 'SP500', name: 'S&P 500 perp', pair: 'xyz:SP500', class: 'index', venue: 'hyperliquid' }, { ...fx.assets[0], symbol: 'TSM', name: 'TSMC (Taiwan Semiconductor)', pair: 'xyz:TSM', class: 'stock', venue: 'hyperliquid', region: 'asia' }, { ...fx.assets[0], symbol: 'EWY', name: 'iShares MSCI South Korea ETF', pair: 'xyz:EWY', class: 'stock', venue: 'hyperliquid', region: 'asia' }, fx.feedAsset('HSDT', 'Solana Company'), fx.feedAsset('DBS', 'DBS Group')] })
    open()
    // Quote feeds are counted apart from the perps: they follow market hours.
    expect(await screen.findByText(/Live quotes: 4 crypto pairs · 1 US stock perp · 1 index perp · 2 Asia stock & ETF perps · 2 US and Asia quote-feed stocks \(market hours, delayed\)\./)).toBeInTheDocument()
    expect(screen.getByText(/Configured coverage: 245 instruments/)).toBeInTheDocument()
  })

  it('stops calling every quote feed delayed once a real-time source prices some of them', async () => {
    const dbs = fx.feedAsset('DBS', 'DBS Group')
    const krx = fx.feedAsset('SAMBIO', 'Samsung Biologics')
    vi.spyOn(api, 'assets').mockResolvedValue({ assets: [...fx.assets, { ...dbs, quoteFeed: { ...dbs.quoteFeed!, source: 'yahoo', class: 'delayed' } }, { ...krx, quoteFeed: { ...krx.quoteFeed!, source: 'naver', class: 'realtime' } }] })
    open()
    expect(await screen.findByText(/· 2 US and Asia quote-feed stocks \(market hours; real-time or delayed, as each price says\)\./)).toBeInTheDocument()
  })

  it('counts the catalogue once it is in: the named instruments and the listed Asian index stocks', async () => {
    stubSession({ listed: true })
    open()
    expect(await screen.findByText(/Configured coverage: 248 instruments, 3 of them Asian index stocks by Yahoo ticker; live availability can vary\./)).toBeInTheDocument()
  })

  it('lists the day’s movers, gainers first, and asks about one on tap', async () => {
    const { user, onSend } = open()
    const movers = await screen.findByRole('list', { name: 'Market now' })
    // fx.assets: SOL +4.5, BTC +2.31, PAXG +0.1 lead; ETH −1.02 is the only one left for the losers.
    expect(within(movers).getAllByRole('button').map((b) => b.textContent)).toEqual(['SOL+4.5%', 'BTC+2.3%', 'PAXG+0.1%', 'ETH-1.0%'])
    await user.click(within(movers).getByRole('button', { name: 'ETH-1.0%' }))
    expect(onSend).toHaveBeenCalledWith("What's the setup on ETH after today's -1.0% move?")
  })

  it('keeps the top 3 gainers and the 3 weakest when the universe is wide', async () => {
    const wide = [1, -2, 3, -4, 5, -6, 7, 0.5].map((c, i) => ({ ...fx.assets[0], symbol: `A${i}`, change24hPct: c }))
    vi.spyOn(api, 'assets').mockResolvedValue({ assets: wide })
    open()
    const movers = await screen.findByRole('list', { name: 'Market now' })
    expect(within(movers).getAllByRole('button').map((b) => b.textContent?.slice(0, 2))).toEqual(['A6', 'A4', 'A2', 'A1', 'A3', 'A5'])
  })

  it('keeps the listed Asian stocks out of the movers, as the agent keeps them out of its market overview', async () => {
    stubSession({ listed: true })
    const big = fx.listedAssets.map((a, i) => ({ ...a, change24hPct: i % 2 ? -30 : 30 }))
    vi.mocked(api.assets).mockImplementation(async (scope) => ({ assets: scope === 'listed' ? big : fx.assets }))
    open()
    await screen.findByText(/Configured coverage: 248 instruments/)
    await screen.findByText(/3 US and Asia quote-feed stocks/) // the listed rows are in, and counted
    const movers = screen.getByRole('list', { name: 'Market now' })
    expect(within(movers).getAllByRole('button').map((b) => b.textContent)).toEqual(['SOL+4.5%', 'BTC+2.3%', 'PAXG+0.1%', 'ETH-1.0%'])
  })

  it('shows the Fear & Greed index beside the movers', async () => {
    vi.spyOn(api, 'getPulse').mockResolvedValue(fx.pulse)
    open()
    await screen.findByRole('list', { name: 'Market now' })
    expect(await screen.findByTitle('Crypto Fear & Greed Index by alternative.me, updated daily')).toHaveTextContent('Fear & Greed 71 · Greed')
  })

  it('has a Portfolio tab whose prompts go straight to the agent', async () => {
    const { user, onSend } = open()
    await user.click(screen.getByRole('button', { name: 'Portfolio' }))
    for (const p of ["How's my portfolio today?", 'Review my last trade', 'Where are my stops?']) expect(screen.getByRole('button', { name: p })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Where are my stops?' }))
    expect(onSend).toHaveBeenCalledWith('Where are my stops?')
  })
})

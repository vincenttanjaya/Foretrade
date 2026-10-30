import { describe, expect, it, vi } from 'vitest'
import type { ChatStreamEvent } from '../../lib/types'
import { applyEvent, errorCopy, fromMessage, isCard, parseAmount, pendingAssistant, toolLabel, userTurn } from './turns'

const outlook = { symbol: 'ETH', spot: 3000, horizonDays: 14, volAnnPct: 60, buckets: [], p5: 2500, p50: 3000, p95: 3600 }
const sources = { type: 'sources', data: { items: [{ n: 1, title: 'Fed', url: 'https://reuters.com/x', source: 'reuters.com', publishedAt: null }] } }

describe('applyEvent', () => {
  it('folds a whole stream into one assistant turn', () => {
    const events: ChatStreamEvent[] = [
      { event: 'tool_call', data: { id: 'a', name: 'get_asset_analysis', label: 'Analyzing ETH (4h)', input: {} } },
      { event: 'tool_call', data: { id: 'b', name: 'price_outlook', label: '', input: {} } },
      { event: 'tool_result', data: { id: 'a', name: 'get_asset_analysis', ok: true, summary: 'Uptrend, RSI 61' } },
      { event: 'card', data: { type: 'price_outlook', data: outlook } },
      { event: 'card', data: { type: 'mystery', data: {} } as never }, // unknown card types are dropped
      { event: 'delta', data: { text: 'ETH looks ' } },
      { event: 'delta', data: { text: 'firm.' } },
      { event: 'done', data: { messageId: 'm1', credits: { used: 2, limit: 500, resetsAt: '' } } },
    ]
    const turn = events.reduce(applyEvent, pendingAssistant())
    expect(turn.content, 'deltas concatenate').toBe('ETH looks firm.')
    expect(turn.status, 'done settles the turn').toBe('done')
    expect(turn.cards, 'only known cards kept').toHaveLength(1)
    expect(turn.tools.map((t) => [t.label, t.ok ?? null, t.summary ?? null])).toEqual([
      ['Analyzing ETH (4h)', true, 'Uptrend, RSI 61'],
      ['price_outlook', null, null], // no label → tool name; no result yet → still running
    ])
  })

  it('marks a failed tool and an error event', () => {
    const t = applyEvent(
      applyEvent(pendingAssistant(), { event: 'tool_call', data: { id: 'x', name: 'web_search', label: 'Searching', input: {} } }),
      { event: 'tool_result', data: { id: 'x', name: 'web_search', ok: false, summary: 'search unavailable' } },
    )
    expect(t.tools[0]).toMatchObject({ ok: false, summary: 'search unavailable' })
    const failed = applyEvent(pendingAssistant(), { event: 'error', data: { code: 'ai_quota', message: 'Daily AI credits used up' } })
    expect([failed.status, failed.error?.code]).toEqual(['error', 'ai_quota'])
  })

  it('ignores routing-only and unknown events', () => {
    const t = pendingAssistant()
    expect(applyEvent(t, { event: 'chat', data: { chatId: 'c1', title: 'x' } })).toBe(t)
    expect(applyEvent(t, { event: 'delta', data: {} } as never).content).toBe('')
  })

  it('times a live turn from send to done', () => {
    const now = vi.spyOn(Date, 'now').mockReturnValue(1_000_000)
    const t = pendingAssistant()
    now.mockReturnValue(1_022_400)
    const done = applyEvent(t, { event: 'done', data: { messageId: 'm1', credits: { used: 2, limit: 500, resetsAt: '' } } })
    expect([t.startedAt, done.elapsedMs]).toEqual([1_000_000, 22_400])
    expect(fromMessage({ id: 'x', role: 'assistant', attachments: [], content: 'hi', cards: [], createdAt: '' }).startedAt).toBeUndefined() // stored: untimed
    now.mockRestore()
  })

  it('keeps a sources card from the stream', () => {
    expect(applyEvent(pendingAssistant(), { event: 'card', data: sources as never }).cards).toHaveLength(1)
  })

  it('records the answer length each tool chip and card arrived at, for the paced reveal', () => {
    const events = [
      { event: 'card', data: { type: 'price_outlook', data: outlook } },
      { event: 'delta', data: { text: 'ETH looks ' } },
      { event: 'tool_call', data: { id: 'a', name: 'get_asset_analysis', label: 'Analyzing ETH (4h)', input: {} } },
      { event: 'delta', data: { text: 'firm.' } },
      { event: 'card', data: sources },
    ]
    const t = events.reduce((turn, ev) => applyEvent(turn, ev as ChatStreamEvent), pendingAssistant())
    expect([t.tools.map((s) => s.at), t.cardsAt]).toEqual([[10], [0, 15]])
    const stored = fromMessage({ id: 'x', role: 'assistant', attachments: [], content: 'hi', cards: [sources as never], createdAt: '' })
    expect(applyEvent(stored, { event: 'card', data: sources as never }).cardsAt).toEqual([0, 2]) // earlier cards count from the start
  })
})

describe('turn builders', () => {
  it('stored messages: null cards and unknown roles are tolerated', () => {
    const stored = fromMessage({ id: 'x', role: 'assistant', attachments: [], content: 'hi', cards: null as never, createdAt: '' })
    expect([stored.key, stored.cards, stored.status]).toEqual(['x', [], 'done'])
    expect(fromMessage({ id: 'y', role: 'assistant', attachments: [], content: 'a [1]', cards: [sources as never], createdAt: '' }).cards).toHaveLength(1)
    const odd = fromMessage({ id: '', role: 'system' as never, attachments: [], content: null as never, cards: [], createdAt: '' })
    expect([odd.role, odd.content, odd.key]).toEqual(['assistant', '', expect.stringMatching(/^t\d+$/)])
  })

  it('user and pending turns get unique keys', () => {
    const u = userTurn('hi')
    const p = pendingAssistant()
    expect(u).toMatchObject({ role: 'user', content: 'hi', status: 'done' })
    expect(p).toMatchObject({ role: 'assistant', status: 'streaming' })
    expect(u.key).not.toBe(p.key)
  })
})

describe('isCard', () => {
  it('accepts the contract card types only', () => {
    expect(isCard(sources), 'sources card accepted').toBe(true)
    expect(isCard({ type: 'price_outlook', data: outlook })).toBe(true)
    expect(isCard({ type: 'sources', data: { items: 'nope' } }), 'sources card without an items list').toBe(false)
    expect(isCard({ type: 'sources', data: null }), 'sources card without data').toBe(false)
    expect(isCard(null)).toBe(false)
    expect(isCard({ type: 'trade_proposal', data: 'x' })).toBe(false)
  })

  it('accepts action cards with a draft or a known build kind', () => {
    expect(isCard({ type: 'forecast_draft', data: { draft: { question: 'Will SOL close above $150?' }, forecast: null } })).toBe(true)
    expect(isCard({ type: 'forecast_draft', data: { draft: null, forecast: null } })).toBe(false)
    expect(isCard({ type: 'build_action', data: { kind: 'portfolio', prompt: 'a basket', result: null } })).toBe(true)
    expect(isCard({ type: 'build_action', data: { kind: 'arena', prompt: 'x', result: null } })).toBe(false)
    expect(isCard({ type: 'price_alert', data: { proposalId: 'p1', symbol: 'SOL', op: 'below', price: 175 } }), 'the alert card').toBe(true)
  })
})

describe('toolLabel', () => {
  it.each([
    ['get_trades', 'Reading your paper trades'],
    ['get_pnl_attribution', 'Checking your P&L attribution'],
    ['get_automations', 'Reviewing your automations'],
    ['get_autopilots', 'Checking your autopilots'],
    ['get_ai_portfolios', 'Reviewing your AI portfolios'],
  ])('keeps %s human when a label is absent or equals the tool name', (name, expected) => {
    expect(toolLabel(name, '', {})).toBe(expected)
    expect(toolLabel(name, name, {})).toBe(expected)
    expect(toolLabel(name, 'Server-provided account label', {})).toBe('Server-provided account label')
  })
  // The server's label wins; the research tools get friendly text when it sends none (or just the name).
  it.each([
    ['get_asset_analysis', 'Analyzing ETH (4h)', {}, 'Analyzing ETH (4h)'],
    ['search_news', 'search_news', { query: ' fed rate decision ', freshness: 'day' }, 'Searching the news: “fed rate decision”'],
    ['web_search', '', { query: 'x'.repeat(80) }, `Searching the web: “${'x'.repeat(47)}…”`],
    ['web_search', '', null, 'Searching the web'],
    ['read_page', '', { url: 'https://www.theblock.co/post/1' }, 'Reading theblock.co'],
    ['read_page', '', { url: 'javascript:void(0)' }, 'Reading a page'],
    ['get_market_wire', '', { symbol: 'sol' }, 'Checking the market wire for SOL'],
    ['get_market_wire', '', {}, 'Checking the market wire'],
    ['get_stock_quote', 'Looking up D05.SI quote', { symbol: 'D05.SI' }, 'Looking up D05.SI quote'],
    ['get_stock_quote', 'get_stock_quote', { symbol: ' 2330.tw ' }, 'Looking up 2330.TW quote'],
    ['get_stock_quote', '', {}, 'Looking up a stock quote'],
    ['mystery_tool', '', {}, 'mystery_tool'],
  ])('%s %j → %s', (name, label, input, expected) => {
    expect(toolLabel(name, label, input)).toBe(expected)
  })
})

describe('errorCopy', () => {
  it('has friendly copy per code, and no retry for an exhausted quota', () => {
    expect(errorCopy('ai_quota', 'x')).toMatchObject({ title: 'Daily AI credits used up', retry: false })
    expect(errorCopy('ai_off', 'AI is not set up on this server yet.')).toEqual({ title: "AI isn't set up yet", detail: 'AI is not set up on this server yet.', retry: false })
    expect(errorCopy('unreadable_file', "The AI on this server can't read this file.")).toEqual({ title: "This file can't be read here", detail: "The AI on this server can't read this file.", retry: false })
    expect(errorCopy('rate_limited', '')).toMatchObject({ title: 'Too many requests', retry: true })
    expect(errorCopy('upstream', '')).toEqual({ title: "The desk couldn't reach its data", detail: 'Market data or the model is unavailable right now.', retry: true })
    expect(errorCopy('upstream', 'Bedrock timed out').detail).toBe('Bedrock timed out')
    expect(errorCopy('network', 'Lost connection')).toEqual({ title: 'Connection lost', detail: 'Lost connection', retry: true })
    expect(errorCopy('internal', '')).toEqual({ title: 'Something went wrong', detail: 'Please try again.', retry: true })
  })
})

describe('parseAmount', () => {
  it('reads plain positive amounts', () => {
    expect(parseAmount('$2,000'), 'dollar and comma').toBe(2000)
    expect(parseAmount(' 12.5 '), 'decimal').toBe(12.5)
    expect(parseAmount('.5'), 'leading dot').toBe(0.5)
  })

  it.each(['', 'abc', '-50', '1e3', '1.2.3'])('rejects "%s"', (bad) => {
    expect(parseAmount(bad)).toBeNaN()
  })
})

describe('agent loop cards', () => {
  it('keeps the suggest_followups call out of the tool strip', () => {
    const t = [
      { event: 'tool_call', data: { id: 's', name: 'suggest_followups', label: 'Suggesting next steps', input: {} } },
      { event: 'tool_result', data: { id: 's', name: 'suggest_followups', ok: true, summary: '3 prompts' } },
      { event: 'card', data: { type: 'followups', data: { prompts: ['Compare with ETH'] } } },
    ].reduce((turn, ev) => applyEvent(turn, ev as ChatStreamEvent), pendingAssistant())
    expect(t.tools).toEqual([])
    expect(t.cards).toHaveLength(1)
  })

  it('accepts forecast and followups cards', () => {
    expect(isCard({ type: 'forecast', data: { id: 'f1', question: 'q', shape: 'binary', headline: { outcome: 'Yes', p: 0.3 }, resolutionDate: '2026-12-31' } })).toBe(true)
    expect(isCard({ type: 'followups', data: { prompts: [] } })).toBe(true)
    expect(isCard({ type: 'followups', data: { prompts: 'Compare with ETH' } }), 'prompts must be a list').toBe(false)
  })
})
describe('memory card contract boundaries', () => {
  const data = { proposalId: 'cc09c736-a775-4871-8358-a0c7eeb44fba', text: '🧭'.repeat(200), status: 'open' }
  it('accepts 200 Unicode codepoints and the known terminal states only', () => {
    for (const status of ['open', 'confirmed', 'deleted']) expect(isCard({ type: 'memory', data: { ...data, status } })).toBe(true)
    expect(toolLabel('remember', undefined, {})).toBe('Suggesting a memory')
    expect(toolLabel('remember', 'A saved server label', {})).toBe('A saved server label')
  })
  it.each([
    { proposalId: 'fabricated' }, { proposalId: null }, { text: '🧭'.repeat(201) }, { text: ' ' }, { text: null }, { status: 'pending' }, { status: null }, { memoryId: {} },
  ])('rejects malformed memory data %j', (change) => {
    expect(isCard({ type: 'memory', data: { ...data, ...change } })).toBe(false)
  })
  it('does not manufacture persistence from an empty or non-string done id', () => {
    for (const messageId of ['', ' ', 3, null, undefined]) {
      const turn = applyEvent(pendingAssistant(), { event: 'done', data: { messageId, credits: { used: 0, limit: 500, resetsAt: '' } } } as never)
      expect(turn.persistedMessageId).toBeUndefined()
    }
  })
})

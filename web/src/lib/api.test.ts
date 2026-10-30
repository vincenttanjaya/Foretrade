import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { json, sseFrame, sseStream } from '../test/utils'
import { API_URL, ATTACHMENT_BODY_LIMIT, ApiError, CHAT_BODY_LIMIT, api, bodyBytes, parseSSE, setUnauthorizedHandler, streamChat, type SSEFrame } from './api'
import type { ChatStreamEvent, ForecastDraft } from './types'

const fetchMock = () => vi.mocked(fetch)
const lastCall = () => {
  const [url, init] = fetchMock().mock.calls.at(-1)!
  return { url: String(url), init: init as RequestInit }
}

describe('parseSSE', () => {
  // Comments, CRLF, multi-line data; any chunking must yield the same frames.
  const stream =
    'event: chat\ndata: {"chatId":"c1","title":"Hi"}\n\n' +
    ': keep-alive\n\n' +
    'event: delta\r\ndata: {"text":"a"}\r\n\r\n' +
    'event: delta\ndata: {"text":\ndata: "b"}\n\n' +
    'event: done\ndata: {"messageId":"m1"}\n\n'
  const whole = parseSSE(stream)

  it('splits a whole stream into frames with no leftover', () => {
    expect(whole.rest).toBe('')
    expect(whole.frames.map((f) => f.event)).toEqual(['chat', 'delta', 'delta', 'done'])
  })

  it('joins multi-line data', () => {
    expect(JSON.parse(whole.frames[2].data)).toEqual({ text: 'b' })
  })

  it.each([1, 2, 3, 4, 5, 6, 7, 8, 9])('yields the same frames at chunk size %i', (size) => {
    const frames: SSEFrame[] = []
    let buf = ''
    for (let i = 0; i < stream.length; i += size) {
      const r = parseSSE(buf + stream.slice(i, i + size))
      frames.push(...r.frames)
      buf = r.rest
    }
    expect(frames).toEqual(whole.frames)
  })

  it('keeps a trailing CR for the next chunk and defaults the event name', () => {
    const r = parseSSE('data: {"x":1}\r')
    expect(r.frames).toEqual([])
    expect(r.rest).toBe('data: {"x":1}\r')
    expect(parseSSE(`${r.rest}\n\r\n`).frames).toEqual([{ event: 'message', data: '{"x":1}' }])
  })

  it('ignores frames without data and fields without a value', () => {
    expect(parseSSE('event: ping\n\nretry\ndata\n\n').frames).toEqual([{ event: 'message', data: '' }])
  })
})

describe('api client', () => {
  it('reads the free prediction sample and canonical draft with encoded queries, cookies and abort ownership', async () => {
    fetchMock().mockImplementation(async () => json({}))
    const signal = new AbortController().signal
    await api.predictionMarkets({ category: 'macro', sort: 'ending' }, signal)
    expect(lastCall().url).toBe(`${API_URL}/api/predictions/markets?category=macro&sort=ending`)
    expect(lastCall().init).toEqual({ method: 'GET', credentials: 'include', signal })
    await api.predictionDraft('123/4', signal)
    expect(lastCall().url).toBe(`${API_URL}/api/predictions/markets/123%2F4/draft`)
    expect(lastCall().init).toEqual({ method: 'GET', credentials: 'include', signal })
  })
  it('GETs with cookies, no body and the abort signal', async () => {
    fetchMock().mockResolvedValue(json({ user: { id: 'u1' }, credits: {} }))
    const signal = new AbortController().signal
    await expect(api.me(signal)).resolves.toEqual({ user: { id: 'u1' }, credits: {} })
    const { url, init } = lastCall()
    expect(url).toBe(`${API_URL}/api/me`)
    expect(init).toEqual({ method: 'GET', credentials: 'include', signal })
  })

  it('sends JSON with a JSON content type on every non-GET, even an empty body', async () => {
    fetchMock().mockResolvedValue(json(null, 204))
    await expect(api.logout()).resolves.toBeUndefined()
    expect(lastCall().init).toMatchObject({
      method: 'POST',
      credentials: 'include',
      headers: { 'Content-Type': 'application/json' },
      body: '{}',
    })

    fetchMock().mockResolvedValue(json({ trade: {}, portfolio: {} }))
    await api.placeOrder({ symbol: 'BTC', side: 'buy', notional: 100, source: 'manual' })
    expect(lastCall().url).toBe(`${API_URL}/api/orders`)
    expect(JSON.parse(lastCall().init.body as string)).toEqual({ symbol: 'BTC', side: 'buy', notional: 100, source: 'manual' })
  })

  it('DELETEs with a JSON body and returns nothing for 204', async () => {
    fetchMock().mockResolvedValue(json(null, 204))
    await expect(api.deleteChat('c/1')).resolves.toBeUndefined()
    expect(lastCall().url).toBe(`${API_URL}/api/chats/c%2F1`) // ids are path-encoded
    expect(lastCall().init).toMatchObject({ method: 'DELETE', body: '{}' })
  })

  it('builds query strings and skips empty values', async () => {
    fetchMock().mockImplementation(async () => json({}))
    await api.candles('BTC', '1h', 500)
    expect(lastCall().url).toBe(`${API_URL}/api/market/candles?symbol=BTC&interval=1h&limit=500`)
    await api.candles('6501.T', '1d', 500, undefined, 'original')
    expect(lastCall().url).toBe(`${API_URL}/api/market/candles?symbol=6501.T&interval=1d&limit=500&currency=original`)
    await api.candles('BTC', '1h', 500, undefined, undefined, 1704067200)
    expect(lastCall().url).toBe(`${API_URL}/api/market/candles?symbol=BTC&interval=1h&limit=500&before=1704067200`)
    await api.candles('6501.T', '1d', 500, undefined, 'original', 1704067200)
    expect(lastCall().url).toBe(`${API_URL}/api/market/candles?symbol=6501.T&interval=1d&limit=500&currency=original&before=1704067200`)
    await api.candles('BTC', '1w', 500)
    expect(lastCall().url).toBe(`${API_URL}/api/market/candles?symbol=BTC&interval=1w&limit=500`)
    await api.candles('6501.T', '1M', 500, undefined, 'original', 1704067200)
    expect(lastCall().url).toBe(`${API_URL}/api/market/candles?symbol=6501.T&interval=1M&limit=500&currency=original&before=1704067200`)
    await api.trades()
    expect(lastCall().url).toBe(`${API_URL}/api/portfolio/trades`)
    await api.news({ symbol: 'SOL', topic: undefined, limit: 20 })
    expect(lastCall().url).toBe(`${API_URL}/api/news?symbol=SOL&limit=20`)
    await api.news({ topic: 'macro', region: 'southeast_asia', limit: 50 })
    expect(lastCall().url).toBe(`${API_URL}/api/news?topic=macro&region=southeast_asia&limit=50`)
    await api.forecasts({ status: 'running' })
    expect(lastCall().url).toBe(`${API_URL}/api/forecasts?status=running`)
    await api.forecasts({ status: 'completed', scope: 'replay' }) // the track record's recent desk calls
    expect(lastCall().url).toBe(`${API_URL}/api/forecasts?status=completed&scope=replay`)
    await api.strategies('top')
    expect(lastCall().url).toBe(`${API_URL}/api/strategies?scope=top`)
  })

  it('posts each AI endpoint body as the contract defines', async () => {
    fetchMock().mockImplementation(async () => json({}))
    const bodies: [() => Promise<unknown>, string, unknown][] = [
      [() => api.login('a@b.c', 'pw'), '/api/auth/login', { email: 'a@b.c', password: 'pw' }],
      [() => api.copilotPlan({ symbol: 'BTC', style: 'day', bias: 'auto' }), '/api/copilot/plan', { symbol: 'BTC', style: 'day', bias: 'auto' }],
      [() => api.draftForecast('btc 150k'), '/api/forecasts/draft', { idea: 'btc 150k' }],
      [() => api.buildPortfolio('l1s'), '/api/ai-portfolios/build', { prompt: 'l1s' }],
      [() => api.investPortfolio('p1', 500), '/api/ai-portfolios/p1/invest', { amount: 500 }],
      [() => api.closePosition('ETH'), '/api/portfolio/close', { symbol: 'ETH' }],
      [() => api.resetPortfolio(), '/api/portfolio/reset', {}],
      [() => api.generateStrategy({ prompt: 'x', symbol: 'SOL' }), '/api/strategies/generate', { prompt: 'x', symbol: 'SOL' }],
      [
        () => api.createArenaRun({ cadence: '1d', start: '2026-08-03', end: '2026-10-01', agents: [{ personaId: 'momentum' }] }),
        '/api/arena/runs',
        { cadence: '1d', start: '2026-08-03', end: '2026-10-01', agents: [{ personaId: 'momentum' }] },
      ],
      [() => api.cancelArenaRun('r1'), '/api/arena/runs/r1/cancel', {}],
    ]
    for (const [call, path, body] of bodies) {
      await call()
      expect(lastCall().url).toBe(API_URL + path)
      expect(JSON.parse(lastCall().init.body as string)).toEqual(body)
    }
  })

  it('maps every remaining endpoint to its contract path and method', async () => {
    fetchMock().mockImplementation(async () => json({}))
    const spec = { name: 'x', symbol: 'BTC', interval: '1h', bars: 500, positionPct: 100, feePct: 0.05 } as const
    const draft: ForecastDraft = { shape: 'binary', question: 'Will it?', outcomes: ['Yes', 'No'], resolutionCriteria: '', resolutionDate: '2027-01-01', category: 'other' }
    const calls: [() => Promise<unknown>, string, string][] = [
      [() => api.assets(), 'GET', '/api/market/assets'],
      [() => api.analysis('ETH', '4h'), 'GET', '/api/market/analysis?symbol=ETH&interval=4h'],
      [() => api.chats(), 'GET', '/api/chats'],
      [() => api.chat('c1'), 'GET', '/api/chats/c1'],
      [() => api.brief(), 'GET', '/api/news/brief'],
      [() => api.news(), 'GET', '/api/news'],
      [() => api.forecast('f1'), 'GET', '/api/forecasts/f1'],
      [() => api.forecasts(), 'GET', '/api/forecasts'],
      [() => api.createForecast(draft), 'POST', '/api/forecasts'],
      [() => api.deleteForecast('f1'), 'DELETE', '/api/forecasts/f1'],
      [() => api.restoreForecast('f1'), 'POST', '/api/forecasts/f1/restore'],
      [() => api.backtest(spec), 'POST', '/api/strategies/backtest'],
      [() => api.strategy('s1'), 'GET', '/api/strategies/s1'],
      [() => api.deleteStrategy('s1'), 'DELETE', '/api/strategies/s1'],
      [() => api.aiPortfolios(), 'GET', '/api/ai-portfolios'],
      [() => api.aiPortfolio('p1'), 'GET', '/api/ai-portfolios/p1'],
      [() => api.trades(50), 'GET', '/api/portfolio/trades?limit=50'],
      [() => api.arenaOptions(), 'GET', '/api/arena/options'],
      [() => api.arenaRuns(), 'GET', '/api/arena/runs'],
      [() => api.arenaRun('r1'), 'GET', '/api/arena/runs/r1'],
      [() => api.arenaSteps('r1', 0, -1), 'GET', '/api/arena/runs/r1/steps?agent=0&after=-1'],
      [() => api.deleteArenaRun('r1'), 'DELETE', '/api/arena/runs/r1'],
      [() => api.alertATH('BRK.B'), 'GET', '/api/alerts/ath?symbol=BRK.B'],
      [() => api.assets('named', ['6501.T']), 'GET', '/api/market/assets?scope=named&with=6501.T'],
      [() => api.assets('named', []), 'GET', '/api/market/assets?scope=named'],
      [() => api.assets('listed'), 'GET', '/api/market/assets?scope=listed'],
      [() => api.instruments(), 'GET', '/api/market/instruments'],
    ]
    for (const [call, method, path] of calls) {
      await call()
      expect(lastCall().url, path).toBe(API_URL + path)
      expect(lastCall().init.method, path).toBe(method)
    }
    expect(JSON.parse(fetchMock().mock.calls[8][1]!.body as string)).toEqual(draft)
    expect(JSON.parse(fetchMock().mock.calls[10][1]!.body as string)).toEqual({}) // restore never starts an AI run
    expect(JSON.parse(fetchMock().mock.calls[11][1]!.body as string)).toEqual({ spec })
  })

  it('sends forecast materials only when there are some, and {} for a plain update', async () => {
    fetchMock().mockImplementation(async () => json({}))
    const draft: ForecastDraft = { shape: 'binary', question: 'Will it?', outcomes: ['Yes', 'No'], resolutionCriteria: '', resolutionDate: '2027-01-01', category: 'other' }
    const file = { kind: 'document', mediaType: 'text/csv', name: 'a.csv', data: 'YSxi' } as const
    const sent = () => JSON.parse(lastCall().init.body as string)
    await api.createForecast(draft, { attachments: [] })
    expect(sent()).toEqual(draft)
    await api.createForecast(draft, { attachments: [file] })
    expect(sent()).toEqual({ ...draft, attachments: [file] })
    await api.createForecast(draft, { reuseMaterials: { forecastId: 'f0' } })
    expect(sent()).toEqual({ ...draft, reuseMaterials: { forecastId: 'f0' } })
    await api.updateForecast('f1')
    expect([lastCall().url, sent()]).toEqual([`${API_URL}/api/forecasts/f1/update`, {}])
    await api.updateForecast('f1', { attachments: [file], keepMaterials: [] })
    expect(sent()).toEqual({ attachments: [file], keepMaterials: [] })
    expect(ATTACHMENT_BODY_LIMIT).toBe(33_554_432)
    expect(CHAT_BODY_LIMIT).toBe(ATTACHMENT_BODY_LIMIT)
    expect(bodyBytes({ a: 'é' })).toBe(10)
  })

  it('PATCHes a position\'s protection with only the keys given, and reads its perps context', async () => {
    fetchMock().mockImplementation(async () => json({}))
    await api.setProtection('BTC', { stopLoss: 82000, takeProfit: null })
    expect(lastCall().url).toBe(`${API_URL}/api/portfolio/positions/BTC`)
    expect(lastCall().init).toMatchObject({ method: 'PATCH', credentials: 'include', headers: { 'Content-Type': 'application/json' } })
    expect(JSON.parse(lastCall().init.body as string)).toEqual({ stopLoss: 82000, takeProfit: null })
    await api.derivatives('SOL')
    expect(lastCall().url).toBe(`${API_URL}/api/market/derivatives?symbol=SOL`)
    expect(lastCall().init.method).toBe('GET')
  })

  it('maps the error body to ApiError', async () => {
    fetchMock().mockResolvedValue(json({ error: { code: 'insufficient_buying_power', message: 'Not enough buying power' } }, 400))
    const err = await api.placeOrder({ symbol: 'BTC', side: 'buy', notional: 1e9 }).catch((e) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect(err).toMatchObject({ name: 'ApiError', status: 400, code: 'insufficient_buying_power', message: 'Not enough buying power' })
  })

  it('falls back to code "internal" when the error body is not JSON', async () => {
    fetchMock().mockResolvedValue(new Response('<html>Bad gateway</html>', { status: 502 }))
    await expect(api.assets()).rejects.toMatchObject({ status: 502, code: 'internal', message: 'Request failed (502)' })
  })

  it('turns a network failure into ApiError status 0', async () => {
    fetchMock().mockRejectedValue(new TypeError('Failed to fetch'))
    await expect(api.portfolio()).rejects.toMatchObject({
      status: 0,
      code: 'network',
      message: 'Cannot reach the server. Check your connection and try again.',
    })
  })

  it('rethrows the abort error untouched when the caller aborted', async () => {
    const ctrl = new AbortController()
    const abort = new DOMException('The operation was aborted.', 'AbortError')
    fetchMock().mockImplementation(() => {
      ctrl.abort()
      return Promise.reject(abort)
    })
    await expect(api.chats(ctrl.signal)).rejects.toBe(abort)
  })

  describe('401 handling', () => {
    const onUnauthorized = vi.fn()
    beforeEach(() => setUnauthorizedHandler(onUnauthorized))
    afterEach(() => setUnauthorizedHandler(() => {}))

    it('calls the unauthorized handler for a 401 anywhere', async () => {
      fetchMock().mockResolvedValue(json({ error: { code: 'unauthorized', message: 'Sign in' } }, 401))
      await expect(api.portfolio()).rejects.toMatchObject({ status: 401, code: 'unauthorized' })
      expect(onUnauthorized).toHaveBeenCalledTimes(1)
    })

    it('does not treat bad login credentials as a lost session', async () => {
      fetchMock().mockResolvedValue(json({ error: { code: 'invalid_credentials', message: 'Wrong password' } }, 401))
      await expect(api.login('a@b.c', 'x')).rejects.toMatchObject({ code: 'invalid_credentials' })
      expect(onUnauthorized).not.toHaveBeenCalled()
    })
  })
})

describe('streamChat', () => {
  const credits = { used: 2, limit: 500, resetsAt: '2026-10-03T00:00:00Z' }

  function collect() {
    const events: ChatStreamEvent[] = []
    return { events, onEvent: (ev: ChatStreamEvent) => events.push(ev) }
  }

  it.each([0, 1])('checks the complete UTF-8 JSON body at the 33,554,432-byte boundary (+%s)', async extra => {
    // Transport boundary: Unicode and escaping count as wire bytes, including JSON property overhead.
    const message = '😀"\\'
    const data = 'A'.repeat(33_554_432 - new TextEncoder().encode(JSON.stringify({ message, attachments: [{ kind: 'image', mediaType: 'image/jpeg', name: 'chart.jpg', data: '' }] })).byteLength + extra)
    const body = { message, attachments: [{ kind: 'image' as const, mediaType: 'image/jpeg' as const, name: 'chart.jpg', data }] }
    expect(new TextEncoder().encode(JSON.stringify(body))).toHaveLength(33_554_432 + extra)
    const stream = sseStream(); stream.push(sseFrame('done', { messageId: 'm', credits }))
    fetchMock().mockResolvedValue(stream.response)
    const { events, onEvent } = collect()
    await streamChat(body, onEvent)
    if (extra) {
      expect(fetchMock()).not.toHaveBeenCalled()
      expect(events).toEqual([{ event: 'error', data: { code: 'bad_request', message: 'This message is too large to send. Remove an attachment and try again.' } }])
    } else {
      expect(fetchMock()).toHaveBeenCalledOnce()
      expect(JSON.parse(lastCall().init.body as string)).toEqual(body)
      expect(events[0].event).toBe('done')
    }
  })

  it('POSTs the message and delivers events across arbitrary chunk boundaries', async () => {
    const text =
      sseFrame('chat', { chatId: 'c1', title: 'Hi' }) +
      ': keep-alive\n\n' +
      sseFrame('tool_call', { id: 'a', name: 'get_asset_analysis', label: 'Analyzing BTC (4h)', input: { symbol: 'BTC' } }) +
      sseFrame('tool_result', { id: 'a', name: 'get_asset_analysis', ok: true, summary: 'Uptrend' }) +
      sseFrame('delta', { text: 'BTC looks firm — €1 ≈ ' }) + // multi-byte characters
      sseFrame('mystery', { x: 1 }) + // unknown event names are ignored
      'event: delta\ndata: {not json\n\n' + // malformed frames are dropped
      sseFrame('done', { messageId: 'm1', credits })
    const bytes = new TextEncoder().encode(text)
    const s = sseStream()
    fetchMock().mockResolvedValue(s.response)
    for (let i = 0; i < bytes.length; i += 7) s.push(bytes.slice(i, i + 7)) // splits UTF-8 sequences too
    s.close()

    const { events, onEvent } = collect()
    await streamChat({ message: 'How is BTC?', context: { symbol: 'BTC' } }, onEvent)

    expect(lastCall().url).toBe(`${API_URL}/api/chat/stream`)
    expect(lastCall().init).toMatchObject({ method: 'POST', credentials: 'include', headers: { 'Content-Type': 'application/json' } })
    expect(JSON.parse(lastCall().init.body as string)).toEqual({ message: 'How is BTC?', context: { symbol: 'BTC' } })
    expect(events.map((e) => e.event)).toEqual(['chat', 'tool_call', 'tool_result', 'delta', 'done'])
    expect(events[3].data).toEqual({ text: 'BTC looks firm — €1 ≈ ' })
    expect(events[4].data).toEqual({ messageId: 'm1', credits })
  })

  it('stops reading at done and cancels the body', async () => {
    const s = sseStream()
    fetchMock().mockResolvedValue(s.response)
    s.push(sseFrame('done', { messageId: 'm1', credits }) + sseFrame('delta', { text: 'late' }))
    const { events, onEvent } = collect()
    await streamChat({ message: 'x' }, onEvent) // resolves without close(): the reader was cancelled
    expect(events.map((e) => e.event)).toEqual(['done'])
  })

  it.each(['done', 'error'])('settles quietly if terminal %s reader cancellation rejects', async terminal => {
    const payload = terminal === 'done' ? { messageId: 'm1', credits } : { code: 'upstream', message: 'Model unavailable' }
    const cancel = vi.fn().mockRejectedValue(new Error('Reader cleanup failed'))
    const body = new ReadableStream<Uint8Array>({
      start(controller) { controller.enqueue(new TextEncoder().encode(sseFrame(terminal, payload) + sseFrame('delta', { text: 'late' }))) },
      cancel,
    })
    fetchMock().mockResolvedValue(new Response(body))
    const { events, onEvent } = collect()
    await expect(streamChat({ message: 'x' }, onEvent)).resolves.toBeUndefined()
    expect(cancel).toHaveBeenCalledOnce()
    expect(events).toEqual([{ event: terminal, data: payload }])
  })

  it('handles a final frame with no trailing blank line', async () => {
    const s = sseStream()
    fetchMock().mockResolvedValue(s.response)
    s.push('event: done\ndata: {"messageId":"m1","credits":{}}')
    s.close()
    const { events, onEvent } = collect()
    await streamChat({ message: 'x' }, onEvent)
    expect(events.map((e) => e.event)).toEqual(['done'])
  })

  it('reports a stream that ends without done as an error event', async () => {
    const s = sseStream()
    fetchMock().mockResolvedValue(s.response)
    s.push(sseFrame('delta', { text: 'half an answ' }))
    s.close()
    const { events, onEvent } = collect()
    await streamChat({ message: 'x' }, onEvent)
    expect(events.at(-1)).toEqual({ event: 'error', data: { code: 'upstream', message: 'The response ended unexpectedly.' } })
  })

  it('passes a server error event through and stops', async () => {
    const s = sseStream()
    fetchMock().mockResolvedValue(s.response)
    s.push(sseFrame('error', { code: 'upstream', message: 'Model unavailable' }))
    const { events, onEvent } = collect()
    await streamChat({ message: 'x' }, onEvent)
    expect(events).toEqual([{ event: 'error', data: { code: 'upstream', message: 'Model unavailable' } }])
  })

  it('turns an HTTP error (429 ai_quota) into an error event instead of rejecting', async () => {
    fetchMock().mockResolvedValue(json({ error: { code: 'ai_quota', message: 'Daily AI credits used up' } }, 429))
    const { events, onEvent } = collect()
    await expect(streamChat({ message: 'x' }, onEvent)).resolves.toBeUndefined()
    expect(events).toEqual([{ event: 'error', data: { code: 'ai_quota', message: 'Daily AI credits used up' } }])
  })

  it('turns a network failure into an error event', async () => {
    fetchMock().mockRejectedValue(new TypeError('Failed to fetch'))
    const { events, onEvent } = collect()
    await streamChat({ message: 'x' }, onEvent)
    expect(events).toEqual([{ event: 'error', data: { code: 'network', message: 'Cannot reach the server. Check your connection and try again.' } }])
  })

  it('reports a dropped connection mid-stream', async () => {
    const s = sseStream()
    fetchMock().mockResolvedValue(s.response)
    s.push(sseFrame('delta', { text: 'a' }))
    const { events, onEvent } = collect()
    const done = streamChat({ message: 'x' }, onEvent)
    await vi.waitFor(() => expect(events).toHaveLength(1)) // an errored stream drops unread chunks
    s.fail(new TypeError('socket hang up'))
    await done
    expect(events).toEqual([
      { event: 'delta', data: { text: 'a' } },
      { event: 'error', data: { code: 'network', message: 'Lost connection to the server.' } },
    ])
  })

  it('stays quiet when the caller aborts', async () => {
    const ctrl = new AbortController()
    fetchMock().mockImplementation(async (_url, init) => {
      const s = sseStream(init?.signal)
      s.push(sseFrame('delta', { text: 'a' }))
      return s.response
    })
    const { events, onEvent } = collect()
    const done = streamChat({ message: 'x' }, onEvent, ctrl.signal)
    await vi.waitFor(() => expect(events).toHaveLength(1))
    ctrl.abort()
    await done
    expect(events.map((e) => e.event)).toEqual(['delta']) // no error event after an abort
  })

  it('reports a browser without streaming support', async () => {
    fetchMock().mockResolvedValue(new Response(null, { status: 200 }))
    const { events, onEvent } = collect()
    await streamChat({ message: 'x' }, onEvent)
    expect(events).toEqual([{ event: 'error', data: { code: 'upstream', message: 'Streaming is not supported by this browser.' } }])
  })
})

describe('api · signals history and optimize', () => {
  it('posts the spec to the optimizer and reads the board with its history', async () => {
    fetchMock().mockImplementation(async () => json({}))
    const spec = { name: 'x', symbol: 'BTC', interval: '1h', bars: 2500, positionPct: 100, feePct: 0.05 } as const
    await api.optimize(spec)
    expect(lastCall().url).toBe(API_URL + '/api/strategies/optimize')
    expect(lastCall().init.method).toBe('POST')
    expect(JSON.parse(lastCall().init.body as string)).toEqual({ spec })
    await api.signals()
    expect(lastCall().url).toBe(API_URL + '/api/signals')
    expect(lastCall().init.method).toBe('GET')
  })
})

describe('api · screener quotes', () => {
  it('asks for the loaded tickers, comma-separated, with the caller’s signal', async () => {
    fetchMock().mockImplementation(async () => json({ rows: [], unknown: [], asOf: null, lastCompletedAt: null, pollSeconds: 60 }))
    const signal = new AbortController().signal
    expect(await api.screenerQuotes(['7203.T', '005930.KS'], signal)).toEqual({ rows: [], unknown: [], asOf: null, lastCompletedAt: null, pollSeconds: 60 })
    expect(lastCall().url).toBe(`${API_URL}/api/screener/asia/quotes?tickers=7203.T%2C005930.KS`)
    expect(lastCall().init).toEqual({ method: 'GET', credentials: 'include', signal })
  })
})

describe('portfolio review frozen metadata', () => {
  it('forwards cancellation on the free read and preserves null risks and measured samples', async () => {
    const fx = await import('../test/fixtures')
    const review = fx.review({ coverage: { modeledGrossExposure: 0, missingSymbols: ['APT'], commonDays: 8, reason: 'Missing daily history.' }, betaBtc: null, var: null, volAnnPct: null, drawdown30dPct: null, exposurePct: null, predictionStakesPct: null, correlations: [{ a: 'APT', b: 'BTC', rho: null, days: 8 }] })
    fetchMock().mockResolvedValue(json(review)); const ctrl = new AbortController(); const result = await api.portfolioReview(ctrl.signal)
    expect(lastCall().url).toBe(`${API_URL}/api/portfolio/review`); expect(lastCall().init.method ?? 'GET').toBe('GET'); expect(lastCall().init.signal).toBe(ctrl.signal); expect(result).toEqual(review)
  })
})

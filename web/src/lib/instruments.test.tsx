// The instrument catalogue: one read per session, a sessionStorage copy by ETag, and SYMBOLS only without the route.
import { act, renderHook, waitFor } from '@testing-library/react'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as fx from '../test/fixtures'
import { json } from '../test/utils'
import { api, API_URL, ApiError } from './api'
import { InstrumentsProvider, isListedIn, useInstruments } from './instruments'

const wrapper = ({ children }: { children: ReactNode }) => <InstrumentsProvider>{children}</InstrumentsProvider>
const load = () => renderHook(() => useInstruments(), { wrapper })

beforeEach(() => sessionStorage.clear())
afterEach(() => vi.useRealTimers())

describe('InstrumentsProvider', () => {
  it('reads GET /api/market/instruments once and indexes the named and listed rows', async () => {
    const get = vi.spyOn(api, 'instruments').mockResolvedValue({ instruments: fx.instruments, etag: '"v1-248"' })
    const { result } = load()
    expect(result.current).toMatchObject({ status: 'loading', loaded: false, rows: [], listed: [] })
    await waitFor(() => expect(result.current.status).toBe('ready'))
    expect(get).toHaveBeenCalledTimes(1)
    expect(result.current.loaded).toBe(true)
    expect(result.current.rows).toHaveLength(fx.instruments.length)
    expect(result.current.listed.map((r) => r.symbol)).toEqual(['6501.T', '000810.KS', 'BBNI.JK'])
    expect(result.current.bySymbol['6501.T']).toMatchObject({ name: 'Hitachi, Ltd.', venue: 'yahoo', sessionVenue: 'TSE', index: 'Nikkei 225' })
    expect(isListedIn(result.current, '6501.T')).toBe(true)
    expect(isListedIn(result.current, 'DBS')).toBe(false) // a named quote feed
    expect(isListedIn(result.current, 'NOPE')).toBe(false)
    expect(JSON.parse(sessionStorage.getItem('instruments')!)).toMatchObject({ etag: '"v1-248"', instruments: expect.any(Array) })
  })

  it('sends the request the client builds: GET with cookies, and the ETag only when the API exposes it', async () => {
    const fetch = vi.mocked(globalThis.fetch).mockResolvedValueOnce(json({ instruments: fx.listedInstruments }))
    await expect(api.instruments()).resolves.toEqual({ instruments: fx.listedInstruments, etag: null })
    expect(fetch).toHaveBeenCalledWith(`${API_URL}/api/market/instruments`, expect.objectContaining({ method: 'GET', credentials: 'include' }))
    const res = json({ instruments: [] })
    res.headers.set('ETag', 'W/"abc"')
    fetch.mockResolvedValueOnce(res)
    await expect(api.instruments()).resolves.toEqual({ instruments: [], etag: 'W/"abc"' })
  })

  it('shows the stored copy at once and keeps it when the ETag is unchanged', async () => {
    sessionStorage.setItem('instruments', JSON.stringify({ etag: '"v1-248"', instruments: fx.instruments }))
    let answer!: (v: { instruments: typeof fx.instruments; etag: string | null }) => void
    vi.spyOn(api, 'instruments').mockReturnValue(new Promise((r) => (answer = r)))
    const { result } = load()
    expect(result.current.status).toBe('ready') // before the API answers
    expect(result.current.listed).toHaveLength(3)
    const before = result.current
    await act(async () => answer({ instruments: [], etag: '"v1-248"' })) // same version: the body is not even read
    expect(result.current).toBe(before)
  })

  it('replaces the stored copy when the ETag changed', async () => {
    sessionStorage.setItem('instruments', JSON.stringify({ etag: '"v1-248"', instruments: fx.instruments }))
    vi.spyOn(api, 'instruments').mockResolvedValue({ instruments: fx.listedInstruments.slice(0, 1), etag: '"v2-1"' })
    const { result } = load()
    await waitFor(() => expect(result.current.rows).toHaveLength(1))
    expect(JSON.parse(sessionStorage.getItem('instruments')!).etag).toBe('"v2-1"')
  })

  it('falls back to SYMBOLS only on a 404 (an API without the catalogue) and drops a stale copy', async () => {
    sessionStorage.setItem('instruments', JSON.stringify({ etag: null, instruments: fx.instruments }))
    const get = vi.spyOn(api, 'instruments').mockRejectedValue(new ApiError(404, 'not_found', 'Not found'))
    vi.useFakeTimers()
    const { result } = load()
    await act(() => vi.advanceTimersByTimeAsync(0))
    expect(result.current).toMatchObject({ status: 'absent', loaded: true, retrying: false, rows: [], listed: [] })
    expect(result.current.bySymbol).toEqual({})
    expect(sessionStorage.getItem('instruments')).toBeNull()
    await act(() => vi.advanceTimersByTimeAsync(5 * 60_000))
    expect(get).toHaveBeenCalledTimes(1) // a 404 is an answer: no retry
  })

  it('falls back on a failed read too, keeps a stored copy, and retries every minute', async () => {
    const get = vi.spyOn(api, 'instruments').mockRejectedValueOnce(new ApiError(0, 'network', 'down')).mockResolvedValue({ instruments: fx.instruments, etag: null })
    vi.useFakeTimers()
    const { result } = load()
    await act(() => vi.advanceTimersByTimeAsync(0))
    expect(result.current).toMatchObject({ status: 'absent', retrying: true }) // absent for now; a deep link waits for the retry
    await act(() => vi.advanceTimersByTimeAsync(60_000))
    expect(get).toHaveBeenCalledTimes(2)
    expect(result.current).toMatchObject({ status: 'ready', retrying: false })
    expect(result.current.listed).toHaveLength(3)
  })

  it('keeps a stored copy through a failed read', async () => {
    sessionStorage.setItem('instruments', JSON.stringify({ etag: null, instruments: fx.instruments }))
    vi.spyOn(api, 'instruments').mockRejectedValue(new ApiError(502, 'upstream', 'bad gateway'))
    const { result } = load()
    await act(async () => {})
    expect(result.current.status).toBe('ready')
    expect(result.current.listed).toHaveLength(3)
  })

  it('works when storage is blocked, and ignores a malformed copy and malformed rows', async () => {
    sessionStorage.setItem('instruments', '{not json')
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new DOMException('blocked', 'SecurityError')
    })
    vi.spyOn(api, 'instruments').mockResolvedValue({ instruments: [null, { symbol: '' }, { symbol: 'X' }, ...fx.listedInstruments] as never, etag: null })
    const { result } = load()
    expect(result.current.status).toBe('loading')
    await waitFor(() => expect(result.current.status).toBe('ready'))
    expect(result.current.rows.map((r) => r.symbol)).toEqual(['6501.T', '000810.KS', 'BBNI.JK'])
  })

  it('treats an empty catalogue as absent', async () => {
    vi.spyOn(api, 'instruments').mockResolvedValue({ instruments: [], etag: null })
    const { result } = load()
    await waitFor(() => expect(result.current.status).toBe('absent'))
  })

  it('is absent outside a provider: SYMBOLS only', () => {
    const { result } = renderHook(() => useInstruments())
    expect(result.current).toMatchObject({ status: 'absent', loaded: true, listed: [] })
  })
})

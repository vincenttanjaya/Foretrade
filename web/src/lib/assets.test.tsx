// AssetsProvider's two polls: the named rows every 10 s (with the listed symbol on screen) and the listed rows every 60 s.
import { act, renderHook } from '@testing-library/react'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi, type MockInstance } from 'vitest'
import * as fx from '../test/fixtures'
import { api, ApiError } from './api'
import { AssetsProvider, useActiveSymbol, useAssets } from './assets'
import { InstrumentsProvider } from './instruments'
import type { Asset } from './types'

const wrapper = ({ children }: { children: ReactNode }) => (
  <InstrumentsProvider>
    <AssetsProvider>{children}</AssetsProvider>
  </InstrumentsProvider>
)
/** The provider's view, with `symbol` on screen as the Trade page names it. */
function mount(symbol: string | null = null) {
  return renderHook(
    ({ s }) => {
      useActiveSymbol(s ?? '')
      return useAssets()
    },
    { wrapper, initialProps: { s: symbol } },
  )
}
const flush = () => act(() => vi.advanceTimersByTimeAsync(0))
const symbols = (assets: Asset[]) => assets.map((a) => a.symbol)

beforeEach(() => {
  sessionStorage.clear()
  vi.useFakeTimers()
})
afterEach(() => vi.useRealTimers())

describe('AssetsProvider against an API without the catalogue', () => {
  it('sends only the named poll, exactly as before', async () => {
    vi.spyOn(api, 'instruments').mockRejectedValue(new ApiError(404, 'not_found', 'Not found'))
    const get = vi.spyOn(api, 'assets').mockResolvedValue({ assets: fx.assets })
    const { result } = mount('6501.T')
    await flush()
    expect(get.mock.calls).toEqual([[]]) // no scope, no with
    expect(symbols(result.current.assets)).toEqual(['BTC', 'ETH', 'SOL', 'PAXG'])
    await act(() => vi.advanceTimersByTimeAsync(60_000))
    expect(get).toHaveBeenCalledTimes(7) // every 10 s, never `scope=listed`
    expect(get.mock.calls.every((c) => c.length === 0)).toBe(true)
  })
})

describe('AssetsProvider with listed rows', () => {
  let get: MockInstance<typeof api.assets>
  beforeEach(() => {
    vi.spyOn(api, 'instruments').mockResolvedValue({ instruments: fx.instruments, etag: null })
    get = vi.spyOn(api, 'assets').mockImplementation(async (scope, withSymbols) => ({
      assets: scope === 'listed' ? fx.listedAssets : [...fx.assets, ...(withSymbols ?? []).map((s) => fx.listedAsset(s, { price: 31 }))],
    }))
  })

  it('polls the named rows every 10 s and the listed rows every 60 s, then merges them by symbol', async () => {
    const { result } = mount()
    await flush()
    expect(get).toHaveBeenCalledWith('listed')
    expect(symbols(result.current.assets)).toEqual(['BTC', 'ETH', 'SOL', 'PAXG', '6501.T', '000810.KS', 'BBNI.JK']) // named first
    expect(result.current.bySymbol['6501.T']?.price).toBeCloseTo(4726 * 0.00632083)
    get.mockClear()
    await act(() => vi.advanceTimersByTimeAsync(60_000))
    expect(get.mock.calls.filter(([scope]) => scope === 'listed')).toHaveLength(1)
    expect(get.mock.calls.filter(([scope]) => scope !== 'listed')).toHaveLength(6)
  })

  it('asks for the listed symbol on screen with the named poll (`with`), at once, and lets that answer win', async () => {
    const { result, rerender } = mount('6501.T')
    await flush()
    expect(get).toHaveBeenCalledWith('named', ['6501.T'])
    expect(result.current.bySymbol['6501.T']?.price).toBe(31) // the named answer's read, not the minute-old listed one
    expect(result.current.answeredWith).toBe('6501.T')

    get.mockClear()
    rerender({ s: 'BBNI.JK' })
    await flush()
    expect(get.mock.calls).toEqual([['named', ['BBNI.JK']]]) // read now, not on the next tick
    expect(result.current.bySymbol['BBNI.JK']?.price).toBe(31)

    get.mockClear()
    rerender({ s: 'BTC' }) // a named symbol: no `with`
    await flush()
    expect(get.mock.calls).toEqual([['named', undefined]])
    expect(result.current.answeredWith).toBeNull()
  })

  it('drops a listed row missing from the newest listed answer (halted), unless the named answer carries it', async () => {
    const { result } = mount('6501.T')
    await flush()
    get.mockImplementation(async (scope, withSymbols) => ({
      assets: scope === 'listed' ? [fx.listedAsset('000810.KS')] : [...fx.assets, ...(withSymbols ?? []).map((s) => fx.listedAsset(s, { price: 31 }))],
    }))
    await act(() => vi.advanceTimersByTimeAsync(60_000))
    expect(symbols(result.current.assets)).toEqual(['BTC', 'ETH', 'SOL', 'PAXG', '000810.KS', '6501.T'])
    expect(result.current.bySymbol['BBNI.JK']).toBeUndefined()
  })

  it('keeps the last prices when a poll fails', async () => {
    const { result } = mount()
    await flush()
    get.mockRejectedValue(new ApiError(0, 'network', 'down'))
    await act(() => vi.advanceTimersByTimeAsync(60_000))
    expect(result.current.assets).toHaveLength(7)
  })

  it('applies only answers newer than the last applied one', async () => {
    let late!: (v: { assets: Asset[] }) => void
    get.mockImplementation(async (scope, withSymbols) => {
      if (scope === 'named' && withSymbols?.[0] === '6501.T') return new Promise((r) => (late = r))
      return { assets: scope === 'listed' ? fx.listedAssets : [...fx.assets, ...(withSymbols ?? []).map((s) => fx.listedAsset(s, { price: 32 }))] }
    })
    const { result, rerender } = mount('6501.T')
    await flush()
    rerender({ s: '000810.KS' })
    await flush()
    expect(result.current.answeredWith).toBe('000810.KS')
    await act(async () => late({ assets: [fx.assets[0]] })) // the older request answers last
    expect(result.current.answeredWith).toBe('000810.KS')
    expect(result.current.bySymbol.ETH).toBeDefined()
  })

  it('reads every 5 s while the listing on screen is priced by a real-time source in session, else every 10 s', async () => {
    const rt = (over: Partial<NonNullable<Asset['quoteFeed']>> = {}) => (s: string) => {
      const a = fx.listedAsset(s, { price: 31 })
      return { ...a, quoteFeed: { ...a.quoteFeed!, marketState: 'open' as const, source: 'naver', class: 'realtime' as const, delaySeconds: 2, ...over } }
    }
    let quote = rt()
    get.mockImplementation(async (scope, withSymbols) => ({
      assets: scope === 'listed' ? fx.listedAssets : [...fx.assets, ...(withSymbols ?? []).map((s) => quote(s))],
    }))
    const named = () => get.mock.calls.filter(([scope]) => scope === 'named').length
    mount('000810.KS')
    await flush()
    get.mockClear()
    await act(() => vi.advanceTimersByTimeAsync(30_000))
    expect(named()).toBeGreaterThanOrEqual(6) // every 5 s (plus the read at the switch)
    expect(named()).toBeLessThanOrEqual(7)
    // The venue fell back to Yahoo (or its session closed): back to every 10 s.
    for (const over of [{ source: 'yahoo', class: 'delayed' as const }, { marketState: 'closed' as const }, { class: 'near-realtime' as const }]) {
      quote = rt(over)
      await act(() => vi.advanceTimersByTimeAsync(10_000))
      get.mockClear()
      await act(() => vi.advanceTimersByTimeAsync(30_000))
      expect(named(), JSON.stringify(over)).toBeLessThanOrEqual(4)
      quote = rt()
      await act(() => vi.advanceTimersByTimeAsync(10_000))
    }
  })

  it('never starts the listed poll without listed rows in the catalogue', async () => {
    vi.mocked(api.instruments).mockResolvedValue({ instruments: fx.instruments.filter((r) => !r.listed), etag: null })
    mount('6501.T')
    await flush()
    await act(() => vi.advanceTimersByTimeAsync(120_000))
    expect(get.mock.calls.some(([scope]) => scope === 'listed')).toBe(false)
    expect(get.mock.calls.every((c) => c.length === 0)).toBe(true)
  })
})

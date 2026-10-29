import { act, fireEvent, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError } from '../../lib/api'
import type { PredictionBoard } from '../../lib/types'
import { useAuth } from '../../lib/auth'
import * as fx from '../../test/fixtures'
import { predictionBoard, predictionEvent, predictionMarket } from '../../test/predictions'
import { currentPath, renderPage, stubSession } from '../../test/utils'
import { ForecastsPage } from './ForecastsPage'

beforeEach(() => {
  stubSession()
  vi.spyOn(api, 'predictionMarkets').mockResolvedValue(predictionBoard)
  vi.spyOn(api, 'forecasts').mockResolvedValue({ forecasts: [] })
  vi.spyOn(api, 'trackRecord').mockResolvedValue(fx.trackRecord())
})
const open = () => renderPage(<ForecastsPage />, { route: '/forecasts?tab=markets', path: '/forecasts', routes: { '/forecasts/new': <p>canonical review</p>, '/': <p>agent</p> } })
afterEach(() => vi.useRealTimers())

describe('prediction market sample', () => {
  it('mounts only Markets from the URL and distinguishes crowd, proxy and private desk without discovery stakes', async () => {
    const { user } = open()
    expect(await screen.findByRole('heading', { name: 'What the crowd is pricing.' })).toBeInTheDocument()
    expect(api.forecasts).not.toHaveBeenCalled()
    expect(api.trackRecord).not.toHaveBeenCalled()
    expect(screen.getByText(/Crypto \+ Macro/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Both' })).toHaveAttribute('aria-pressed', 'true')
    expect(await screen.findByRole('columnheader', { name: 'Volatility proxy' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /Your desk/ })).toHaveAttribute('href', '/forecasts/f-own')
    expect(screen.queryByRole('button', { name: /stake|buy yes|buy no|Confirm \(paper\)/i })).not.toBeInTheDocument()
    await user.click(screen.getByRole('link', { name: /Run the desk/ }))
    expect(currentPath()).toBe('/forecasts/new?marketId=123')
  })

  it('shows null unverified touch estimates honestly and thin/stale state with immutable timestamps', async () => {
    vi.mocked(api.predictionMarkets).mockResolvedValue({ ...predictionBoard, stale: true, events: [{ ...predictionEvent, markets: [{ ...predictionMarket, thin: true, desk: null, estimate: null, estimateUnavailable: 'Prior touch history is unverified.', rule: { ...predictionMarket.rule!, kind: 'touch' } }] }] })
    open()
    expect(await screen.findByText('Prior touch history is unverified.')).toBeInTheDocument()
    expect(screen.getByText('Thin market')).toBeInTheDocument()
    expect(screen.getByText(/Showing an older sample/)).toBeInTheDocument()
    expect(screen.queryByText('0%')).not.toBeInTheDocument()
    expect(screen.queryByText(/EV per/)).not.toBeInTheDocument()
    expect(screen.getByText(/2026-10-05/)).toBeInTheDocument()
  })

  it('retries first-load failure and preserves an empty result as a usable state', async () => {
    // Authentication changes the read key; reject the signed-in read rather than an obsolete null-user request. The
    // signed-in read starts in an effect after the session's credits render, however late that effect runs, so a read
    // counts only when the credits are on screen as it starts.
    const signedIn: { reject?: (error: ApiError) => void } = {}
    vi.mocked(api.predictionMarkets).mockImplementation(() => new Promise((_resolve, reject) => {
      if (screen.queryByTestId('credits-used')?.textContent === String(fx.credits.used)) signedIn.reject = reject
    }))
    const { user } = open()
    await waitFor(() => expect(signedIn.reject).toBeDefined())
    vi.mocked(api.predictionMarkets).mockResolvedValue({ ...predictionBoard, events: [] })
    await act(async () => signedIn.reject!(new ApiError(502, 'upstream', 'Gamma unavailable')))
    expect(await screen.findByRole('alert')).toHaveTextContent('Gamma unavailable')
    await user.click(screen.getByRole('button', { name: 'Retry markets' }))
    expect(await screen.findByText('No eligible markets in this sample.')).toBeInTheDocument()
  })

  it('aborts obsolete categories and ignores a late response even when the upstream ignores abort', async () => {
    let finish!: (b: PredictionBoard) => void
    vi.mocked(api.predictionMarkets).mockImplementationOnce(() => new Promise((r) => { finish = r }))
    const { user } = open()
    await waitFor(() => expect(api.predictionMarkets).toHaveBeenCalledTimes(1))
    const signal = vi.mocked(api.predictionMarkets).mock.calls[0][1]!
    await user.click(screen.getByRole('button', { name: 'Macro' }))
    expect(signal.aborted).toBe(true)
    await screen.findByText('Bitcoin in October')
    await act(async () => finish({ ...predictionBoard, events: [{ ...predictionEvent, title: 'Obsolete private overlay' }] }))
    expect(screen.queryByText('Obsolete private overlay')).not.toBeInTheDocument()
    expect(api.predictionMarkets).toHaveBeenLastCalledWith({ category: 'macro', sort: 'edge' }, expect.any(AbortSignal))
    fireEvent.change(screen.getByLabelText('Sort markets'), { target: { value: 'ending' } })
    await waitFor(() => expect(api.predictionMarkets).toHaveBeenLastCalledWith({ category: 'macro', sort: 'ending' }, expect.any(AbortSignal)))
  })

  it('renders grouped ladder children with distinct canonical actions', async () => {
    vi.mocked(api.predictionMarkets).mockResolvedValue({ ...predictionBoard, events: [{ ...predictionEvent, markets: [predictionMarket, { ...predictionMarket, id: '124', marketSlug: 'bitcoin-below-80000', question: 'Will Bitcoin be below $80,000?', rule: { symbol: 'BTC', op: 'below', strike: 80000, kind: 'close' }, desk: null }] }] })
    open()
    const table = await screen.findByRole('table', { name: 'Bitcoin in October markets' })
    expect(within(table).getAllByRole('link', { name: /Run the desk/ }).map((a) => a.getAttribute('href'))).toEqual(['/forecasts/new?marketId=123', '/forecasts/new?marketId=124'])
    expect(within(table).getAllByRole('button', { name: /Set crowd alert/ })).toHaveLength(2)
  })

  it('polls only while visible, serializes focus/timer reads, and stops on leaving Markets', async () => {
    const { unmount } = open()
    await screen.findByText('Bitcoin in October')
    const initial = vi.mocked(api.predictionMarkets).mock.calls.length
    vi.useFakeTimers()
    const visible = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden')
    await act(async () => vi.advanceTimersByTime(60_000))
    expect(api.predictionMarkets).toHaveBeenCalledTimes(initial)
    let finish!: (b: PredictionBoard) => void
    vi.mocked(api.predictionMarkets).mockImplementationOnce(() => new Promise((r) => { finish = r }))
    visible.mockReturnValue('visible')
    fireEvent(document, new Event('visibilitychange'))
    expect(api.predictionMarkets).toHaveBeenCalledTimes(initial + 1)
    const signal = vi.mocked(api.predictionMarkets).mock.calls.at(-1)![1]!
    fireEvent(window, new Event('focus'))
    await act(async () => vi.advanceTimersByTime(120_000))
    expect(api.predictionMarkets).toHaveBeenCalledTimes(initial + 1)
    unmount()
    expect(signal.aborted).toBe(true)
    await act(async () => finish(predictionBoard))
    await act(async () => vi.advanceTimersByTime(120_000))
    expect(api.predictionMarkets).toHaveBeenCalledTimes(initial + 1)
  })

  it('keeps a failed refresh explicitly stale with the original timestamp and no proxy EV', async () => {
    const { user } = open()
    await screen.findByText('Bitcoin in October')
    vi.mocked(api.predictionMarkets).mockRejectedValue(new ApiError(502, 'upstream', 'Fresh odds unavailable'))
    await user.click(screen.getByRole('button', { name: 'Refresh markets' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Fresh odds unavailable')
    expect(screen.getByText('Bitcoin in October')).toBeInTheDocument()
    expect(screen.getByRole('status')).toHaveTextContent(predictionBoard.asOf)
    expect(screen.getByRole('status')).toHaveTextContent('Showing an older sample')
    expect(screen.queryByText(/EV per/)).not.toBeInTheDocument()
  })

  it('keeps a selected alert quote with its original read time across board refreshes', async () => {
    const { user } = open()
    await screen.findByText('Bitcoin in October')
    await user.click(screen.getByRole('button', { name: /Set crowd alert/ }))
    const form = screen.getByRole('region', { name: `Crowd odds alert: ${predictionMarket.question}` })
    expect(form).toHaveTextContent(`Displayed quote read ${predictionBoard.asOf}`)
    vi.mocked(api.predictionMarkets).mockResolvedValue({ ...predictionBoard, asOf: '2026-10-05T13:00:00Z', events: [{ ...predictionEvent, markets: [{ ...predictionMarket, yesPrice: 0.4 }] }] })
    await user.click(screen.getByRole('button', { name: 'Refresh markets' }))
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('2026-10-05T13:00:00Z'))
    expect(form).toHaveTextContent(`Displayed quote read ${predictionBoard.asOf}`)
    expect(form).toHaveTextContent('own Yes 30%')
  })

  it('clears a prior account’s private overlay and aborts its in-flight response on account changes', async () => {
    function AccountControl() {
      const { refresh } = useAuth()
      return <button onClick={() => void refresh()}>Switch account</button>
    }
    const { user } = renderPage(<><AccountControl /><ForecastsPage /></>, { route: '/forecasts?tab=markets', path: '/forecasts' })
    await screen.findByRole('link', { name: /Your desk/ })
    let finishOld!: (b: PredictionBoard) => void
    let finishNew!: (b: PredictionBoard) => void
    vi.mocked(api.predictionMarkets).mockImplementationOnce(() => new Promise((r) => { finishOld = r }))
    fireEvent(window, new Event('focus'))
    const oldSignal = vi.mocked(api.predictionMarkets).mock.calls.at(-1)![1]!
    vi.mocked(api.predictionMarkets).mockImplementationOnce(() => new Promise((r) => { finishNew = r }))
    vi.mocked(api.me).mockResolvedValue({ user: { ...fx.user, id: 'other-owner' }, credits: fx.credits })
    await user.click(screen.getByRole('button', { name: 'Switch account' }))
    expect(oldSignal.aborted).toBe(true)
    expect(screen.queryByRole('link', { name: /Your desk/ })).not.toBeInTheDocument()
    await act(async () => finishOld(predictionBoard))
    expect(screen.queryByRole('link', { name: /Your desk/ })).not.toBeInTheDocument()
    await act(async () => finishNew({ ...predictionBoard, events: [{ ...predictionEvent, markets: [{ ...predictionMarket, desk: null }] }] }))
    expect(screen.queryByRole('link', { name: /Your desk/ })).not.toBeInTheDocument()
    expect(screen.getByText('Bitcoin in October')).toBeInTheDocument()
  })
})

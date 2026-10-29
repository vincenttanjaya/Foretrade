import { screen, waitFor, within } from '@testing-library/react'
import { beforeEach, describe, expect, it, onTestFinished, vi } from 'vitest'
import { api, ApiError } from '../../lib/api'
import type { Alert } from '../../lib/types'
import * as fx from '../../test/fixtures'
import { renderPage, stubSession } from '../../test/utils'
import { AutomationsPage } from './AutomationsPage'
import { fmtIn } from './pipeline'

const show = (alerts: Alert[] = [], route = '/automations?create=drawdown&symbol=sol') => {
  vi.spyOn(api, 'alerts').mockResolvedValue({ alerts })
  return renderPage(<AutomationsPage />, { route, path: '/automations' })
}
const hint = () => document.getElementById('auto-ath')!

beforeEach(() => {
  stubSession()
})

describe('Drawdown automations', () => {
  it('opens from the deep link with the high, its source and the current drawdown, and warns before a past level is set', async () => {
    const ath = vi.spyOn(api, 'alertATH').mockResolvedValue({ ath: fx.athQuote() })
    const create = vi.spyOn(api, 'createAlert').mockResolvedValue({ alert: fx.drawdownAlert({ action: 'notify' }, { pct: 20, sellPct: null, threshold: 200 }) })
    const { user } = show()
    expect(screen.getByRole('button', { name: 'Drawdown' })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByLabelText('Asset')).toHaveValue('SOL')
    expect(await within(hint()).findByText('Binance SOLUSDT daily highs since 2020-08-11')).toBeInTheDocument()
    expect(ath).toHaveBeenCalledWith('SOL', expect.any(AbortSignal))
    expect(hint()).toHaveTextContent('All-time high $250.00 · Jan 19, 2025')
    expect(hint()).toHaveTextContent('Now $150.00 · 40.0% below the high')
    expect(screen.getByRole('button', { name: 'Review drawdown alert' })).toBeDisabled() // no level yet

    await user.type(screen.getByLabelText('Drawdown from the all-time high (%)'), '20')
    expect(hint()).toHaveTextContent('Fires at or below $200.00: the price is already past it, so it would fire on the next check.')
    await user.click(screen.getByRole('button', { name: 'Review drawdown alert' }))
    expect(create).not.toHaveBeenCalled() // confirmed once, after the review
    const confirm = screen.getByRole('group', { name: 'Confirm drawdown alert' })
    expect(confirm).toHaveTextContent('When SOL falls 20% below its all-time high ($250.00 on Jan 19, 2025), at or below $200.00: notify me. One-shot, paper only.')
    expect(confirm).toHaveTextContent('SOL is already 40.0% below its high, past this level: once confirmed, it fires on the next check (within 15 seconds).')
    await user.click(within(confirm).getByRole('button', { name: 'Confirm and set' }))
    expect(create).toHaveBeenCalledWith({ symbol: 'SOL', trigger: 'drawdown', drawdownPct: 20, action: 'notify', note: undefined, source: 'manual' })
    expect(await screen.findByText('Drawdown alert set. The price is already past it, so it fires on the next check.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Review drawdown alert' })).toBeInTheDocument() // the form resets to the review step
  })

  it('brings the form into view on arrival from a "Set drawdown alert" link, and only then', async () => {
    vi.spyOn(api, 'alertATH').mockResolvedValue({ ath: fx.athQuote() })
    const scrolled: Element[] = []
    Object.defineProperty(Element.prototype, 'scrollIntoView', { value: function (this: Element) { scrolled.push(this) }, configurable: true })
    onTestFinished(() => void delete (Element.prototype as { scrollIntoView?: unknown }).scrollIntoView)
    const { unmount } = show()
    await within(hint()).findByText(/All-time high/)
    expect(scrolled).toEqual([document.getElementById('new-automation')])
    unmount()
    scrolled.length = 0
    show([], '/automations')
    await screen.findByRole('heading', { name: 'New automation' })
    expect(scrolled).toEqual([])
  })

  it('says a high a checked price raised was set by the latest price, which for a quote feed is not live', async () => {
    vi.spyOn(api, 'alertATH').mockResolvedValue({ ath: fx.athQuote({ ath: 252, athAt: '2026-10-13T01:12:05Z', athLive: true }) })
    show()
    const line = await within(hint()).findByText(/All-time high/)
    expect(line).toHaveTextContent(/· set by the latest price$/)
    expect(line).not.toHaveTextContent(/\blive\b/)
  })

  it('sells a whole percent of the paper long, and Back or an edit returns to the form', async () => {
    vi.spyOn(api, 'alertATH').mockResolvedValue({ ath: fx.athQuote() })
    const create = vi.spyOn(api, 'createAlert').mockResolvedValue({ alert: fx.drawdownAlert({}, { sellPct: 25 }) })
    const { user } = show()
    await within(hint()).findByText(/All-time high/)
    await user.type(screen.getByLabelText('Drawdown from the all-time high (%)'), '50')
    expect(hint()).toHaveTextContent('Fires at or below $125.00')
    expect(hint()).not.toHaveTextContent('already past')
    await user.click(screen.getByRole('button', { name: 'Notify + sell' }))
    const sell = screen.getByLabelText('Sell (% of your paper long)')
    expect(sell).toHaveValue(100)
    expect(screen.getByText(/It never opens or adds to a short: with no long, you're only notified\./)).toBeInTheDocument()
    expect(screen.queryByLabelText('Amount ($)')).toBeNull() // the long's size decides, not a notional
    await user.clear(sell)
    await user.type(sell, '33.5')
    expect(screen.getByText('A whole number from 1 to 100.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Review drawdown alert' })).toBeDisabled()
    await user.clear(sell)
    await user.type(sell, '25')
    await user.type(screen.getByLabelText('Note'), 'Trim')

    await user.click(screen.getByRole('button', { name: 'Review drawdown alert' }))
    await user.click(screen.getByRole('button', { name: 'Back' }))
    expect(screen.queryByRole('group', { name: 'Confirm drawdown alert' })).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Review drawdown alert' }))
    expect(screen.getByRole('group', { name: 'Confirm drawdown alert' })).toHaveTextContent('notify me and sell 25% of my paper SOL long.')
    await user.type(screen.getByLabelText('Note'), '!') // an edit after the review needs a new review
    expect(screen.queryByRole('group', { name: 'Confirm drawdown alert' })).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Review drawdown alert' }))
    await user.click(screen.getByRole('button', { name: 'Confirm and set' }))
    expect(create).toHaveBeenCalledWith({ symbol: 'SOL', trigger: 'drawdown', drawdownPct: 50, action: 'sell', sellPct: 25, note: 'Trim!', source: 'manual' })
    expect(await screen.findByText('Drawdown alert set. Checked every 15 seconds.')).toBeInTheDocument()
  })

  it('says when the high is unavailable and blocks the review; a level outside 0.01–99.99% is refused', async () => {
    const ath = vi.spyOn(api, 'alertATH').mockRejectedValue(new ApiError(502, 'upstream', 'Market data is unavailable right now. Try again shortly.'))
    const { user } = show([], '/automations?create=drawdown&symbol=ETH')
    expect(await within(hint()).findByText('Market data is unavailable right now. Try again shortly.')).toBeInTheDocument()
    await user.type(screen.getByLabelText('Drawdown from the all-time high (%)'), '20')
    expect(screen.getByRole('button', { name: 'Review drawdown alert' })).toBeDisabled()

    ath.mockResolvedValue({ ath: fx.athQuote({ symbol: 'BTC', ath: 120_000, price: 84213.2, fromAthPct: -29.82, sourceLabel: 'Binance BTCUSDT daily highs since 2017-08-17' }) })
    await user.selectOptions(screen.getByLabelText('Asset'), 'BTC')
    expect(await within(hint()).findByText('Binance BTCUSDT daily highs since 2017-08-17')).toBeInTheDocument()
    expect(hint()).toHaveTextContent('Fires at or below $96,000.00')
    await user.clear(screen.getByLabelText('Drawdown from the all-time high (%)'))
    await user.type(screen.getByLabelText('Drawdown from the all-time high (%)'), '100')
    expect(hint()).toHaveTextContent('Pick a drawdown from 0.01% to 99.99%.')
    expect(screen.getByRole('button', { name: 'Review drawdown alert' })).toBeDisabled()
  })

  it('withholds a high over 100× the price (a reverse-split history) and sets no level from it', async () => {
    vi.spyOn(api, 'alertATH').mockResolvedValue({
      ath: fx.athQuote({ symbol: 'HSDT', ath: 27_168_750, athAt: '2017-10-11T00:00:00Z', source: 'yahoo', ticker: 'HSDT', basis: 'listing', price: 2.66,
        fromAthPct: -100, sourceLabel: 'Yahoo Finance HSDT lifetime daily highs since 2014-06-27 (split-adjusted)' }),
    })
    const { user } = show([], '/automations?create=drawdown&symbol=HSDT')
    expect(await within(hint()).findByText(/^All-time high unavailable: the split-adjusted high in this history is over 100× the price/)).toBeInTheDocument()
    expect(hint()).toHaveTextContent('No drawdown alert can be set from it.')
    expect(hint()).toHaveTextContent('Yahoo Finance HSDT lifetime daily highs since 2014-06-27 (split-adjusted)')
    expect(hint()).toHaveTextContent('Now $2.66')
    await user.type(screen.getByLabelText('Drawdown from the all-time high (%)'), '20')
    expect(hint()).not.toHaveTextContent(/27,168,750|Fires at or below|below the high|Pick a drawdown/)
    expect(screen.getByRole('button', { name: 'Review drawdown alert' })).toBeDisabled()
  })

  it('keeps a Binance high over 100× the price: crypto highs are not split-adjusted', async () => {
    vi.spyOn(api, 'alertATH').mockResolvedValue({ ath: fx.athQuote({ ath: 250, price: 2, fromAthPct: -99.2 }) })
    show()
    expect(await within(hint()).findByText(/All-time high/)).toHaveTextContent('All-time high $250.00 · Jan 19, 2025')
    expect(hint()).toHaveTextContent('Now $2.00 · 99.2% below the high')
  })

  it('lists an existing trigger on a withheld high without the high or its threshold', async () => {
    vi.spyOn(api, 'alertATH').mockResolvedValue({ ath: fx.athQuote() })
    show(
      [
        fx.drawdownAlert({ id: 'dd5', symbol: 'HSDT', action: 'notify' }, { pct: 20, sellPct: null, ath: 27_168_750, threshold: 21_735_000, price: 2.66, fromAthPct: -100,
          source: 'yahoo', sourceLabel: 'Yahoo Finance HSDT lifetime daily highs since 2014-06-27 (split-adjusted)' }),
        fx.drawdownAlert({ id: 'dd6', symbol: 'HSDT', action: 'notify', status: 'fired', firedAt: '2026-10-03T14:05:00Z', firedPrice: 2.66 },
          { pct: 30, sellPct: null, ath: 27_168_750, threshold: 19_018_125, price: null, fromAthPct: null, outcome: 'notified', source: 'yahoo' }),
      ],
      '/automations',
    )
    const region = screen.getByRole('region', { name: 'Your automations' })
    const trigger = (await within(region).findByText('HSDT falls 20% below its all-time high')).closest('div')!.parentElement!
    expect(trigger).toHaveTextContent('All-time high unavailable: the split-adjusted high in this history is over 100× the price')
    expect(trigger).toHaveTextContent('Now $2.66.')
    expect(region).not.toHaveTextContent(/27,168,750|21,735,000|19,018,125/)
  })

  it('compares a quote feed on its listing price and says so', async () => {
    vi.spyOn(api, 'alertATH').mockResolvedValue({
      ath: fx.athQuote({ ath: 79.05, currency: 'SGD', basis: 'listing', source: 'yahoo', ticker: 'D05.SI', price: 78.56, fromAthPct: -0.62,
        sourceLabel: 'Yahoo Finance D05.SI lifetime daily highs since 2000-01-03 (split-adjusted), in SGD' }),
    })
    show()
    expect(await within(hint()).findByText("Compared on the listing's own SGD price, not the USD paper mark.")).toBeInTheDocument()
    expect(hint()).toHaveTextContent('All-time high SGD 79.05')
    expect(hint()).toHaveTextContent('Now SGD 78.56 · 0.6% below the high')
    expect(fmtIn(1234.5, 'SGD')).toBe('SGD 1,234.50')
    expect(fmtIn(null, 'SGD')).toBe('—')
  })

  it('lists a trigger with its high, threshold, current drawdown, source and outcome', async () => {
    vi.spyOn(api, 'alertATH').mockResolvedValue({ ath: fx.athQuote() })
    const { user } = show(
      [
        fx.drawdownAlert(),
        fx.drawdownAlert({ id: 'dd2', status: 'fired', firedAt: '2026-10-03T14:05:00Z', firedPrice: 124.8 }, { outcome: 'nothing_to_sell', price: null, fromAthPct: null }),
        fx.drawdownAlert({ id: 'dd3', status: 'fired', firedAt: '2026-10-03T14:05:00Z', firedPrice: 124.8, tradeId: 't1' }, { outcome: 'sold', price: null, fromAthPct: null }),
        fx.drawdownAlert(
          { id: 'dd4', symbol: 'DBS', action: 'notify', status: 'fired', firedAt: '2026-10-03T14:05:00Z', firedPrice: 37.8 },
          { pct: 10, sellPct: null, ath: 42, threshold: 37.8, currency: 'SGD', basis: 'listing', outcome: 'notified', price: null, fromAthPct: null,
            sourceLabel: 'Yahoo Finance D05.SI lifetime daily highs since 2000-01-03 (split-adjusted), in SGD' },
        ),
      ],
      '/automations',
    )
    const region = screen.getByRole('region', { name: 'Your automations' })
    await within(region).findByText(/SOL falls 50% below its all-time high/)
    const [active] = within(region).getAllByText('Trigger', { selector: '.label' }).map((l) => l.closest('.card') as HTMLElement)
    expect(within(active).getByText('Trigger', { selector: '.label' }).parentElement).toHaveTextContent(
      'SOL falls 50% below its all-time high · ≤ $125.00ATH $250.00 · Jan 19, 2025 · now $150.00 (40.0% below)Binance SOLUSDT daily highs since 2020-08-11',
    )
    expect(within(active).getByText('Action', { selector: '.label' }).parentElement).toHaveTextContent('Notify + paper sell 100% of the SOL longNever shorts · no long, nothing to sell')
    expect(within(active).getByText('Deliver', { selector: '.label' }).parentElement).toHaveTextContent('Bell · in-app · Account')

    await user.click(within(region).getByRole('button', { name: 'History · 3' }))
    expect(await within(region).findByText('Nothing to sell: no paper long')).toBeInTheDocument()
    expect(within(region).getByRole('link', { name: /Filled/ })).toHaveAttribute('href', '/account')
    expect(region).toHaveTextContent('DBS falls 10% below its all-time high · ≤ SGD 37.80')
    expect(region).toHaveTextContent('SGD listing price')
    await waitFor(() => expect(within(region).getAllByText(/^Fired .* at SGD 37\.80$/)).toHaveLength(1))
  })
})

describe('Drawdown automations · listed Asian stocks', () => {
  it('preselects a listed preset (the screener\'s drawdown link) before its price arrives, in its venue group', async () => {
    stubSession({ listed: true })
    vi.mocked(api.assets).mockImplementation(async (scope) => ({ assets: scope === 'listed' ? [] : fx.assets })) // no listed price yet
    const ath = vi.spyOn(api, 'alertATH').mockResolvedValue({ ath: fx.athQuote({ symbol: '6501.T' }) })
    show([], '/automations?create=drawdown&symbol=6501.T')
    const select = screen.getByLabelText('Asset')
    await waitFor(() => expect(within(select).getByRole('option', { name: '6501.T · Hitachi, Ltd.' })).toBeInTheDocument())
    expect(select).toHaveValue('6501.T')
    expect(ath).toHaveBeenCalledWith('6501.T', expect.any(AbortSignal))
    const groups = Array.from(select.querySelectorAll('optgroup'), (g) => [g.label, Array.from(g.querySelectorAll('option'), (o) => o.value)])
    expect(groups).toEqual([['Crypto', ['BTC', 'ETH', 'SOL', 'PAXG']], ['Japan · TSE', ['6501.T']]])
  })

  it('groups every priced instrument by venue', async () => {
    stubSession({ listed: true })
    vi.mocked(api.assets).mockImplementation(async (scope) => ({ assets: scope === 'listed' ? fx.listedAssets : [...fx.assets, fx.feedAsset('DBS', 'DBS Group')] }))
    vi.spyOn(api, 'alertATH').mockResolvedValue({ ath: fx.athQuote() })
    show()
    const select = screen.getByLabelText('Asset')
    await waitFor(() => expect(select.querySelectorAll('optgroup')).toHaveLength(5))
    expect(Array.from(select.querySelectorAll('optgroup'), (g) => g.label)).toEqual(['Crypto', 'Singapore · SGX', 'South Korea · KRX', 'Japan · TSE', 'Indonesia · IDX'])
    expect(within(select).getByRole('option', { name: 'DBS' })).toBeInTheDocument() // a named row keeps its plain symbol
    expect(within(select).getByRole('option', { name: 'BBNI.JK · PT Bank Negara Indonesia (Persero) Tbk' })).toBeInTheDocument()
  })

  it('keeps an unknown preset as the only choice until the prices load, as before', async () => {
    vi.mocked(api.assets).mockReturnValue(new Promise(() => {}))
    vi.spyOn(api, 'alertATH').mockResolvedValue({ ath: fx.athQuote() })
    show([], '/automations?create=drawdown&symbol=6501.T') // an API without the catalogue
    expect(screen.getByLabelText('Asset')).toHaveValue('6501.T')
    expect(Array.from((screen.getByLabelText('Asset') as HTMLSelectElement).options, (o) => o.value)).toEqual(['6501.T'])
  })
})

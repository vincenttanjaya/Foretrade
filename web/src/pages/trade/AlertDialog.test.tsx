import { act, screen, within } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { api, ApiError } from '../../lib/api'
import type { Asset, AthQuote } from '../../lib/types'
import * as fx from '../../test/fixtures'
import { renderPage, stubSession } from '../../test/utils'
import { AlertDialog } from './AlertDialog'

const hitachi = fx.listedAsset('6501.T') // a TSE listing quoted in JPY
const jpy = hitachi.quoteFeed!.localPrice

function open(ui = <AlertDialog symbol="6501.T" asset={hitachi} />) {
  stubSession()
  const view = renderPage(ui)
  return { ...view, dialog: async () => { await view.user.click(screen.getByRole('button', { name: /Set (drawdown )?alert/ })); return within(screen.getByRole('dialog')) } }
}

describe('Alert dialog', () => {
  it('keeps foreign-underlying perps in their USD trading currency', async () => {
    const asset = fx.universeAssets.find((a) => a.symbol === 'SOFTBANK')!
    const create = vi.spyOn(api, 'createAlert').mockResolvedValue({ alert: {} as never })
    const { user, dialog } = open(<AlertDialog symbol="SOFTBANK" asset={asset} />)
    const d = await dialog()
    const price = d.getByLabelText('Price (USD)')
    expect(price).toBeEnabled()
    await user.clear(price)
    await user.type(price, '100')
    await user.click(d.getByRole('button', { name: 'Set alert' }))
    expect(create).toHaveBeenCalledWith({ symbol: 'SOFTBANK', op: 'below', price: 100, note: undefined })
  })

  it("sets a quote-feed stock's level only in its original currency", async () => {
    const create = vi.spyOn(api, 'createAlert').mockResolvedValue({ alert: {} as never })
    const { user, dialog } = open()
    const d = await dialog()
    expect(d.queryByRole('radiogroup', { name: 'Price currency' })).toBeNull()
    expect(d.queryByLabelText('Price (USD)')).toBeNull()
    expect(d.getByText(/^Now JPY /)).toBeInTheDocument()
    expect(d.getByText('Checked on the JPY listing price, not the USD paper mark.')).toBeInTheDocument()
    const price = d.getByLabelText('Price (JPY)')
    expect(Number((price as HTMLInputElement).value)).toBeLessThan(jpy) // a suggested level 5% below
    await user.clear(price)
    await user.type(price, String(Math.round(jpy * 0.9)))
    await user.type(d.getByLabelText('Note (optional)'), 'Add on dips')
    await user.click(d.getByRole('button', { name: 'Set alert' }))
    expect(create).toHaveBeenCalledWith({ symbol: '6501.T', op: 'below', price: Math.round(jpy * 0.9), currency: 'JPY', note: 'Add on dips' })
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('waits for the original listing quote before accepting a price level', async () => {
    const create = vi.spyOn(api, 'createAlert').mockResolvedValue({ alert: {} as never })
    const { user, dialog } = open(<AlertDialog symbol="6501.T" />)
    let resolve!: (value: { assets: Asset[] }) => void
    const quote = new Promise<{ assets: Asset[] }>((done) => { resolve = done })
    vi.mocked(api.assets).mockReturnValue(quote)
    const d = await dialog()
    expect(d.queryByLabelText('Price (USD)')).toBeNull()
    expect(d.getByRole('spinbutton')).toBeDisabled()
    expect(d.getByRole('button', { name: 'Set alert' })).toBeDisabled()
    await act(async () => resolve({ assets: [hitachi] }))
    const price = await d.findByLabelText('Price (JPY)')
    expect(Number((price as HTMLInputElement).value)).toBeGreaterThan(hitachi.price)
    await user.clear(price)
    await user.type(price, '3000')
    await user.click(d.getByRole('button', { name: 'Set alert' }))
    expect(create).toHaveBeenCalledWith({ symbol: '6501.T', op: 'below', price: 3000, currency: 'JPY', note: undefined })
  })

  it.each([
    ['missing', { ...hitachi, localCurrency: null, quoteFeed: null }],
    ['zero', { ...hitachi, quoteFeed: { ...hitachi.quoteFeed!, localPrice: 0 } }],
    ['non-finite', { ...hitachi, quoteFeed: { ...hitachi.quoteFeed!, localPrice: Number.NaN } }],
  ] as const)('blocks a foreign alert with a %s listing quote instead of falling back to USD', async (_label, asset) => {
    const create = vi.spyOn(api, 'createAlert')
    const { user, dialog } = open(<AlertDialog symbol="6501.T" asset={asset} />)
    const d = await dialog()
    expect(d.queryByLabelText('Price (USD)')).toBeNull()
    await user.type(d.getByRole('spinbutton'), '1')
    expect(d.getByRole('button', { name: 'Set alert' })).toBeDisabled()
    await user.click(d.getByRole('button', { name: 'Set alert' }))
    expect(create).not.toHaveBeenCalled()
  })

  it('keeps a USD listing in its original USD currency', async () => {
    const usd = fx.feedAsset('SE', 'Sea Limited')
    const create = vi.spyOn(api, 'createAlert').mockResolvedValue({ alert: {} as never })
    const { user, dialog } = open(<AlertDialog symbol="SE" asset={usd} />)
    const d = await dialog()
    const price = d.getByLabelText('Price (USD)')
    await user.clear(price)
    await user.type(price, '80')
    await user.click(d.getByRole('button', { name: 'Set alert' }))
    expect(create).toHaveBeenCalledWith({ symbol: 'SE', op: 'below', price: 80, note: undefined })
  })

  it('refuses a level the price has already crossed, and shows the server refusal', async () => {
    const create = vi.spyOn(api, 'createAlert').mockRejectedValue(new ApiError(400, 'bad_request', '6501.T alerts are set in JPY.'))
    const { user, dialog } = open()
    const d = await dialog()
    await user.clear(d.getByLabelText('Price (JPY)'))
    await user.type(d.getByLabelText('Price (JPY)'), String(jpy * 2))
    expect(d.getByText(/^6501\.T is already below that/)).toBeInTheDocument()
    expect(d.getByRole('button', { name: 'Set alert' })).toBeDisabled()
    await user.clear(d.getByLabelText('Price (JPY)'))
    await user.type(d.getByLabelText('Price (JPY)'), String(Math.round(jpy / 2)))
    await user.click(d.getByRole('button', { name: 'Set alert' }))
    expect(create).toHaveBeenCalled()
    expect(await d.findByRole('alert')).toHaveTextContent('6501.T alerts are set in JPY.')
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })

  it('sets a drawdown alert from the all-time high, opening on that tab from the screener', async () => {
    const quote: AthQuote = { symbol: '6501.T', price: 3000, fromAthPct: -25, ath: 4000, athAt: '2026-01-05T00:00:00Z', athLive: false, coveredFrom: '2000-01-04T00:00:00Z',
      source: 'yahoo', sourceLabel: 'Yahoo lifetime high', ticker: '6501.T', currency: 'JPY', basis: 'listing' }
    vi.spyOn(api, 'alertATH').mockResolvedValue({ ath: quote })
    const create = vi.spyOn(api, 'createAlert').mockResolvedValue({ alert: {} as never })
    const { user, dialog } = open(<AlertDialog symbol="6501.T" kind="drawdown" label="Set drawdown alert" ariaLabel="Set drawdown alert on 6501.T" />)
    vi.mocked(api.assets).mockResolvedValue({ assets: [hitachi] }) // no row given: read on open
    const d = await dialog()
    expect(d.getByRole('radio', { name: 'Drop from high' })).toBeChecked()
    expect(await d.findByText('JPY 4,000.00')).toBeInTheDocument()
    await user.type(d.getByLabelText('Drop from the all-time high (%)'), '30')
    expect(d.getByText('JPY 2,800.00')).toBeInTheDocument() // fires at 4,000 × 0.7
    await user.click(d.getByRole('button', { name: 'Set alert' }))
    expect(create).toHaveBeenCalledWith({ symbol: '6501.T', trigger: 'drawdown', drawdownPct: 30, note: undefined })
    expect(api.assets).toHaveBeenCalledWith('named', ['6501.T'], expect.any(AbortSignal))
  })

  it('keeps crypto in USD with no currency choice', async () => {
    const { dialog } = open(<AlertDialog symbol="BTC" asset={fx.assets.find((a) => a.symbol === 'BTC')} />)
    const d = await dialog()
    expect(d.queryByRole('radiogroup', { name: 'Price currency' })).toBeNull()
    expect(d.getByLabelText('Price (USD)')).toBeInTheDocument()
  })
})

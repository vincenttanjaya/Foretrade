import { screen } from '@testing-library/react'
import { useLocation } from 'react-router'
import { describe, expect, it, vi } from 'vitest'
import { api } from '../../lib/api'
import * as fx from '../../test/fixtures'
import { renderPage } from '../../test/utils'
import { DeskChip, deskIdea, useDesk } from './DeskChip'

function Header({ symbol = 'SOL', price = 200 }: { symbol?: string; price?: number }) {
  const desk = useDesk(symbol)
  return desk === undefined ? null : <DeskChip symbol={symbol} price={price} read={desk} />
}

function NewForecast() {
  return <p>idea: {(useLocation().state as { idea?: string } | null)?.idea}</p>
}

describe('DeskChip', () => {
  it('links the desk read to its report, once per symbol', async () => {
    const call = vi.spyOn(api, 'getDesk').mockResolvedValue({ desk: fx.deskRead() })
    renderPage(<Header />)
    const chip = await screen.findByRole('link', { name: /Forecast desk/ })
    expect(chip).toHaveTextContent('Forecast desk · Yes 28% (leans No) · SOL above $125 on Oct 9 →')
    expect(chip).toHaveAttribute('href', '/forecasts/f-sol')
    expect(call).toHaveBeenCalledTimes(1)
    expect(call.mock.calls[0][0]).toBe('SOL')
  })

  it('reads a non-binary or rule-less desk by its question, clipped to 60 characters', () => {
    const question = 'Which layer-1 has the best month in October 2026, by close-to-close return on Binance?'
    renderPage(<DeskChip symbol="SOL" read={fx.deskRead({ priceRule: null, outcome: 'Solana', p: 0.41, lean: 'Solana 41%', question })} />)
    expect(screen.getByRole('link')).toHaveTextContent(`Forecast desk · Solana 41% · ${question.slice(0, 59)}… →`)
  })

  it('without a read, asks the desk about the symbol with a drafted idea', async () => {
    vi.spyOn(api, 'getDesk').mockResolvedValue({ desk: null })
    const { user } = renderPage(<Header />, { routes: { '/forecasts/new': <NewForecast /> } })
    const ask = await screen.findByRole('link', { name: 'Ask the desk about SOL →' })
    expect(ask).toHaveAttribute('href', '/forecasts/new')
    await user.click(ask)
    expect(await screen.findByText(/^idea: Will SOL close above \$210 on [A-Z][a-z]{2} \d{1,2}\?$/)).toBeInTheDocument()
  })

  it('treats a failed read as no read', async () => {
    renderPage(<Header />) // fetch is not mocked: the call rejects
    expect(await screen.findByRole('link', { name: 'Ask the desk about SOL →' })).toBeInTheDocument()
  })

  it('drafts the idea at price × 1.05 to 2 significant figures, a week out', () => {
    const now = Date.UTC(2026, 9, 3, 12)
    expect(deskIdea('SOL', 200, now)).toBe('Will SOL close above $210 on Oct 10?')
    expect(deskIdea('BTC', 84_213.2, now)).toBe('Will BTC close above $88,000 on Oct 10?')
    expect(deskIdea('DOGE', 0.2034, now)).toBe('Will DOGE close above $0.21 on Oct 10?')
  })
})

import { screen, waitFor, within } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { usePortfolio } from '../../lib/portfolio'
import { api, ApiError } from '../../lib/api'
import * as fx from '../../test/fixtures'
import { renderPage, stubSession } from '../../test/utils'
import { AgentCard } from './cards'

/** The provider's view of the ETH stop, so a test can see the confirm's portfolio land. */
function Stop() {
  const { portfolio } = usePortfolio()
  return <output aria-label="ETH stop">{portfolio?.positions.find((p) => p.symbol === 'ETH')?.stopLoss ?? 'none'}</output>
}

const show = (data = fx.protection()) =>
  renderPage(
    <>
      <AgentCard card={{ type: 'protection', data }} />
      <Stop />
    </>,
  )

beforeEach(() => {
  stubSession()
})

describe('ProtectionCard', () => {
  it('shows the position, the level changes from the mark and reward:risk', async () => {
    show()
    expect(screen.getByText('Protect ETH long · 2 ETH')).toBeInTheDocument()
    expect(screen.getAllByRole('term').map((dt) => dt.parentElement!.textContent)).toEqual([
      'Stop$2,700.00 → $2,850.00(-5.0% from mark)',
      'Targetnone → $3,300.00(+10.0% from mark)',
      'R:R2.0 : 1', // +$300 to the target for −$150 to the stop
    ])
    expect(screen.getByText('Trail the stop under the 4h higher low.')).toBeInTheDocument()
    await waitFor(() => expect(screen.getByLabelText('ETH stop')).toHaveTextContent('2700'))
  })

  it('confirms with only the proposed levels and applies the returned portfolio', async () => {
    const patch = vi.spyOn(api, 'setProtection').mockResolvedValue({
      portfolio: { ...fx.portfolio, revision: 5, positions: [{ ...fx.position, stopLoss: 2850 }] },
    })
    const { user } = show(fx.protection({ proposed: { stopLoss: 2850, takeProfit: null } }))
    expect(screen.getByText('(unchanged)').parentElement).toHaveTextContent('None (unchanged)')
    expect(screen.queryByText('R:R')).toBeNull() // no target, no reward:risk
    await waitFor(() => expect(screen.getByLabelText('ETH stop')).toHaveTextContent('2700'))
    await user.click(screen.getByRole('button', { name: 'Confirm protection' }))
    // A kept level is not sent; expect is the card's current levels, so a stale card is refused (409).
    expect(patch).toHaveBeenCalledWith('ETH', { stopLoss: 2850, expect: { stopLoss: 2700, takeProfit: null } })
    expect(await screen.findByText('Protection set')).toBeInTheDocument()
    expect(screen.getByText('Protection set').parentElement).not.toHaveTextContent('✓') // the check icon is enough
    expect(screen.getByLabelText('ETH stop')).toHaveTextContent('2850')
    expect(screen.getByRole('link', { name: /View on Trade/ })).toHaveAttribute('href', '/trade/ETH')
    expect(screen.queryByRole('button', { name: 'Confirm protection' })).toBeNull()
  })

  it('shows a refused confirm inline and lets the user retry', async () => {
    const patch = vi
      .spyOn(api, 'setProtection')
      .mockRejectedValueOnce(new ApiError(400, 'bad_request', 'For a long, the stop must be below the price ($2,840).'))
      .mockResolvedValueOnce({ portfolio: { ...fx.portfolio, revision: 3 } })
    const { user } = show()
    await user.click(screen.getByRole('button', { name: 'Confirm protection' }))
    expect(patch).toHaveBeenCalledWith('ETH', { stopLoss: 2850, takeProfit: 3300, expect: { stopLoss: 2700, takeProfit: null } })
    const alert = await screen.findByRole('alert')
    expect(within(alert.parentElement!).getByRole('button', { name: 'Confirm protection' })).toBeEnabled()
    expect(alert).toHaveTextContent('For a long, the stop must be below the price ($2,840).')
    await user.click(screen.getByRole('button', { name: 'Confirm protection' }))
    expect(await screen.findByText('Protection set')).toBeInTheDocument()
  })

  it('reads as set after a reload while the position carries its levels', async () => {
    vi.spyOn(api, 'portfolio').mockResolvedValue({
      portfolio: { ...fx.portfolio, positions: [{ ...fx.position, stopLoss: 2850, takeProfit: 3300 }] },
    })
    show()
    expect(await screen.findByText('Protection set')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Confirm protection' })).toBeNull()
  })
})

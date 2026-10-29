import { screen, waitFor } from '@testing-library/react'
import { useLocation } from 'react-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError } from '../../lib/api'
import type { Alert, PriceAlertProposal } from '../../lib/types'
import * as fx from '../../test/fixtures'
import { renderPage, stubSession } from '../../test/utils'
import { AgentCard } from './cards'

function Prompt() {
  const { state } = useLocation() as { state: { prompt?: string } | null }
  return <p data-testid="prompt">{state?.prompt}</p>
}

const node = (label: string) => screen.getByText(label, { selector: '.label' }).parentElement!

const show = (data: PriceAlertProposal = fx.drawdownCard(), stored: Alert[] = []) => {
  const list = vi.spyOn(api, 'alerts').mockResolvedValue({ alerts: stored })
  return { list, ...renderPage(<AgentCard card={{ type: 'price_alert', data }} />, { route: '/chat/c1', path: '/chat/:id', routes: { '/': <Prompt /> } }) }
}

beforeEach(() => {
  stubSession()
})

describe('AlertCard · drawdown (set_drawdown_alert)', () => {
  it('draws the high, threshold, source and sale, and sets it with trigger drawdown and the card’s proposalId', async () => {
    const create = vi.spyOn(api, 'createAlert').mockResolvedValue({ alert: fx.drawdownAlert() })
    const { user, list } = show()
    expect(screen.getByText('Drawdown alert · paper')).toBeInTheDocument()
    expect(screen.getByText('Sell 100% of SOL on a 50% drawdown')).toBeInTheDocument()
    expect(node('Trigger')).toHaveTextContent(
      'SOL falls 50% below its all-time high · ≤ $125.00ATH $250.00 · Jan 19, 2025 · now $150.00 (40.0% below)Binance SOLUSDT daily highs since 2020-08-11',
    )
    expect(node('Action')).toHaveTextContent('Notify + paper sell 100% of the SOL longNever shorts · no long, nothing to sell')
    expect(node('Deliver')).toHaveTextContent('Bell · in-app · Account')
    expect(screen.getByText(/Checked every 15 s\. The sale never opens or adds to a short\. Nothing is set until you tap\./)).toBeInTheDocument()
    expect(screen.queryByText(/already past this level/)).toBeNull()
    await waitFor(() => expect(list).toHaveBeenCalledTimes(1))

    await user.click(screen.getByRole('button', { name: 'Set alert' }))
    expect(create).toHaveBeenCalledWith({
      symbol: 'SOL',
      trigger: 'drawdown',
      drawdownPct: 50,
      sellPct: 100,
      action: 'sell',
      note: 'Protect the position on a deep drawdown.',
      source: 'agent',
      proposalId: fx.drawdownCard().proposalId,
    })
    expect(await screen.findByText('Alert set')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Set alert' })).toBeNull()
  })

  it('warns when the price is already past the level, and a notify card posts no sellPct', async () => {
    const create = vi.spyOn(api, 'createAlert').mockResolvedValue({ alert: fx.drawdownAlert({ action: 'notify' }) })
    const { user } = show(fx.drawdownCard({ action: 'notify', sellPct: undefined, drawdownPct: 20, threshold: 200, distancePct: 33.33 }))
    expect(screen.getByText('Watch SOL for a 20% drawdown')).toBeInTheDocument()
    expect(node('Action')).toHaveTextContent('Notify me')
    expect(screen.getByText('SOL is already past this level: once set, it fires on the next check (within 15 s).')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Set alert' }))
    expect(create.mock.calls[0][0]).toEqual({
      symbol: 'SOL', trigger: 'drawdown', drawdownPct: 20, action: 'notify', note: 'Protect the position on a deep drawdown.', source: 'agent',
      proposalId: fx.drawdownCard().proposalId,
    })
    expect(await screen.findByText('Alert set')).toBeInTheDocument()
    expect(screen.queryByText(/already past this level/)).toBeNull()
  })

  it('withholds a high over 100× the price (a reverse-split history) and offers no Set alert from it', async () => {
    const create = vi.spyOn(api, 'createAlert')
    const hsdt = { ...fx.drawdownCard().ath!, ath: 27_168_750, ticker: 'HSDT', source: 'yahoo' as const, sourceLabel: 'Yahoo Finance HSDT lifetime daily highs since 2014-06-27 (split-adjusted)' }
    const { user } = show(fx.drawdownCard({ symbol: 'HSDT', action: 'notify', sellPct: undefined, drawdownPct: 20, markPrice: 2.66, ath: hsdt, threshold: 21_735_000, fromAthPct: -100 }))
    expect(node('Trigger')).toHaveTextContent('HSDT falls 20% below its all-time high')
    expect(node('Trigger')).toHaveTextContent('All-time high unavailable: the split-adjusted high in this history is over 100× the price')
    expect(node('Trigger')).toHaveTextContent('Now $2.66.')
    expect(document.body).not.toHaveTextContent(/27,168,750|21,735,000|100\.0% below/)
    expect(screen.queryByText(/already past this level/)).toBeNull()
    expect(screen.getByText("A drawdown alert needs a usable all-time high, so this one can't be set for HSDT.")).toBeInTheDocument()
    const set = screen.getByRole('button', { name: 'Set alert' })
    expect(set).toBeDisabled()
    await user.click(set)
    expect(create).not.toHaveBeenCalled()
  })

  it('shows what became of a stored card and asks the agent for a fix after a refusal', async () => {
    const { unmount } = show(fx.drawdownCard(), [fx.drawdownAlert({ proposalId: fx.drawdownCard().proposalId, status: 'fired', firedPrice: 124.8 })])
    expect(await screen.findByText('Fired at $124.80')).toBeInTheDocument()
    unmount()

    vi.spyOn(api, 'createAlert').mockRejectedValue(new ApiError(502, 'upstream', 'Market data is unavailable right now. Try again shortly.'))
    const { user } = show()
    await user.click(screen.getByRole('button', { name: 'Set alert' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Market data is unavailable right now. Try again shortly.')
    await user.click(screen.getByRole('button', { name: 'Ask the agent for a new level' }))
    expect(screen.getByTestId('prompt')).toHaveTextContent(
      'My SOL drawdown alert (50% below its all-time high) was refused: Market data is unavailable right now. Try again shortly. Suggest a fix.',
    )
  })
})

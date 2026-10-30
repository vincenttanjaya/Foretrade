import { screen, waitFor, within } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError } from '../../lib/api'
import type { Autopilot } from '../../lib/types'
import * as fx from '../../test/fixtures'
import { renderPage, stubSession } from '../../test/utils'
import { atrStop, AutopilotCard, checkText, DeployButton, deploySummary, parseAllocation, parseRisk, positionText, riskText, statusText } from './autopilot'

const solTrade = (over = {}) => fx.trade({ id: 'f1', symbol: 'SOL', qty: 0.42, price: 142.1, source: 'autopilot', note: 'Autopilot · SOL EMA cross', ...over })

beforeEach(() => {
  stubSession()
})

describe('autopilot helpers', () => {
  it('parses the allocation within $100–$100,000', () => {
    expect(parseAllocation('5000')).toBe(5000)
    expect(parseAllocation('$12,500')).toBe(12500)
    for (const bad of ['', '99', '100001', 'abc']) expect(parseAllocation(bad)).toBeNull()
  })

  it('reads positions, status and the next check', () => {
    expect(positionText(fx.autopilot())).toBe('Long 0.42 SOL @ $142.10')
    expect(positionText(fx.autopilot({ positionQty: -1200, avgPrice: 0.2013, symbol: 'DOGE' }))).toBe('Short 1,200 DOGE @ $0.2013')
    expect(positionText(fx.autopilot({ positionQty: 0 }))).toBe('Flat')
    expect(statusText(fx.autopilot())).toBe('Running')
    expect(statusText(fx.autopilot({ status: 'stopped', stopReason: 'user' }))).toBe('Stopped (user)')
    expect(statusText(fx.autopilot({ status: 'stopped', stopReason: 'max_drawdown' }))).toBe('Stopped (max drawdown)')
    expect(statusText(fx.autopilot({ status: 'stopped', stopReason: 'account_disabled' }))).toBe('Stopped: account disabled')
    expect(checkText(fx.autopilot())).toBe('Next check 12:00 UTC')
    expect(checkText(fx.autopilot({ positionQty: 0 }))).toBe('Watching for entry · next check 12:00 UTC')
    expect(checkText(fx.autopilot({ nextCheckAt: null }))).toMatch(/Waiting for market data/)
    expect(checkText(fx.autopilot({ status: 'stopped' }))).toBeNull()
  })

  it('summarises what each check does, with the levels it deploys with', () => {
    expect(deploySummary(fx.strategy().spec)).toBe(
      'Checks every closed 4h bar · each entry uses 100% of the allocation · stop 4% / no target · paper only, stop any time',
    )
    expect(deploySummary(fx.strategy().spec, { stopLossPct: 3.2, takeProfitPct: 6.4, trailingStopPct: 3 })).toBe(
      'Checks every closed 4h bar · each entry uses 100% of the allocation · stop 3.2% / target 6.4% · trailing 3% · paper only, stop any time',
    )
    expect(riskText({ stopLossPct: 3.2, takeProfitPct: 6.4, trailingStopPct: 3 })).toBe('stop 3.2% · target 6.4% · trailing 3%')
    expect(riskText({ stopLossPct: null, takeProfitPct: 8, trailingStopPct: 3 })).toBe('target 8% · trailing 3%')
    expect(riskText({})).toBe('')
  })

  it('requires a stop and a target, the trailing stop optional', () => {
    expect(parseRisk({ stop: '3', take: '6', trail: '' })).toEqual({ values: { stopLossPct: 3, takeProfitPct: 6 }, errors: {} })
    expect(parseRisk({ stop: '3', take: '6', trail: '2' }).values).toEqual({ stopLossPct: 3, takeProfitPct: 6, trailingStopPct: 2 })
    const bad = parseRisk({ stop: '', take: '60', trail: '0.2' })
    expect(bad.values).toBeNull()
    expect(bad.errors).toEqual({ stop: 'Required: every entry carries one.', take: 'Use 0.1–50%.', trail: 'Use 0.5–50%.' })
  })

  it('suggests a stop at 2× the ATR, within 0.5–20%', () => {
    expect(atrStop(2.4, 150)).toBe(3.2)
    expect(atrStop(0.1, 150)).toBe(0.5)
    expect(atrStop(40, 150)).toBe(20)
  })
})

describe('DeployButton', () => {
  const open = (running = false, onDeployed = vi.fn()) => {
    const r = renderPage(<DeployButton s={fx.strategy()} running={running} onDeployed={onDeployed} />)
    return { ...r, onDeployed }
  }

  it('deploys with a preset allocation and drawdown cap', async () => {
    const deploy = vi.spyOn(api, 'deployAutopilot').mockResolvedValue({ autopilot: fx.autopilot(), trade: solTrade(), portfolio: fx.portfolio })
    const { user, onDeployed } = open()
    await user.click(screen.getByRole('button', { name: 'Deploy to autopilot' }))
    const dialog = screen.getByRole('dialog', { name: 'Deploy “SOL EMA cross”' })
    expect(within(dialog).getByRole('button', { name: '$5K' })).toHaveAttribute('aria-pressed', 'true') // the default
    expect(within(dialog).getByRole('button', { name: '10%' })).toHaveAttribute('aria-pressed', 'true')
    expect(within(dialog).getByText(/each entry uses 100% of the allocation/)).toBeInTheDocument()

    await user.click(within(dialog).getByRole('button', { name: '$10K' }))
    expect(within(dialog).getByLabelText('Allocation in USD')).toHaveValue('10000')
    await user.click(within(dialog).getByRole('button', { name: '20%' }))
    await user.click(within(dialog).getByRole('button', { name: 'Deploy (paper)' }))
    expect(deploy).toHaveBeenCalledWith({ strategyId: 's1', allocationUsd: 10000, maxDrawdownPct: 20, stopLossPct: 4, takeProfitPct: 8 }) // the spec's stop, 2× as target
    await waitFor(() => expect(onDeployed).toHaveBeenCalledWith(fx.autopilot()))
    expect(dialog).not.toHaveAttribute('open')
    expect(await screen.findByText('Autopilot running · SOL EMA cross')).toBeInTheDocument()
  })

  it('prefills the levels from the spec, a missing target at 2× the stop', async () => {
    const analysis = vi.spyOn(api, 'analysis')
    const { user } = open()
    await user.click(screen.getByRole('button', { name: 'Deploy to autopilot' }))
    expect(screen.getByLabelText('Stop loss %')).toHaveValue('4')
    expect(screen.getByLabelText('Take profit %')).toHaveValue('8')
    expect(screen.getByLabelText('Trailing stop %')).toHaveValue('')
    expect(screen.getByLabelText('Trailing stop %')).toHaveAttribute('placeholder', 'Off')
    expect(screen.getByText('Target suggested at 2× the stop. Edit as you like.')).toBeInTheDocument()
    expect(analysis).not.toHaveBeenCalled()
  })

  it('suggests the stop and target from the ATR when the spec has none, and validates them', async () => {
    const analysis = vi.spyOn(api, 'analysis').mockResolvedValue({ analysis: { ...fx.analysis, symbol: 'SOL', interval: '4h', price: 150, atr14: 2.4 } })
    const deploy = vi.spyOn(api, 'deployAutopilot').mockResolvedValue({ autopilot: fx.autopilot(), trade: null, portfolio: null })
    const bare = fx.strategy({ spec: { ...fx.strategy().spec, stopLossPct: undefined } })
    const { user } = renderPage(<DeployButton s={bare} running={false} onDeployed={vi.fn()} />)
    await user.click(screen.getByRole('button', { name: 'Deploy to autopilot' }))
    expect(analysis).toHaveBeenCalledWith('SOL', '4h')
    expect(await screen.findByText('Suggested from 2× the 4h ATR (1.6%). Edit as you like.')).toBeInTheDocument()
    const stop = screen.getByLabelText('Stop loss %')
    expect(stop).toHaveValue('3.2')
    expect(screen.getByLabelText('Take profit %')).toHaveValue('6.4')
    await user.type(screen.getByLabelText('Trailing stop %'), '3')
    expect(screen.getByText(/stop 3\.2% \/ target 6\.4% · trailing 3% · paper only/)).toBeInTheDocument()

    const button = screen.getByRole('button', { name: 'Deploy (paper)' })
    await user.clear(stop)
    expect(screen.getByText('Required: every entry carries one.')).toBeInTheDocument()
    expect(stop).toHaveAttribute('aria-invalid', 'true')
    expect(button).toBeDisabled()
    await user.type(stop, '60')
    expect(screen.getByText('Use 0.1–50%.')).toBeInTheDocument()
    expect(button).toBeDisabled()
    await user.clear(stop)
    await user.type(stop, '3.2')
    await user.click(button)
    expect(deploy).toHaveBeenCalledWith({ strategyId: 's1', allocationUsd: 5000, maxDrawdownPct: 10, stopLossPct: 3.2, takeProfitPct: 6.4, trailingStopPct: 3 })
    expect(analysis).toHaveBeenCalledTimes(1)
  })

  it('validates the allocation', async () => {
    const deploy = vi.spyOn(api, 'deployAutopilot')
    const { user } = open()
    await user.click(screen.getByRole('button', { name: 'Deploy to autopilot' }))
    const input = screen.getByLabelText('Allocation in USD')
    await user.clear(input)
    await user.type(input, '50')
    expect(screen.getByText('Use $100–$100,000.')).toBeInTheDocument()
    expect(input).toHaveAttribute('aria-invalid', 'true')
    expect(screen.getByRole('button', { name: 'Deploy (paper)' })).toBeDisabled()
    for (const preset of ['$1K', '$5K', '$10K']) expect(screen.getByRole('button', { name: preset })).toHaveAttribute('aria-pressed', 'false')
    await user.clear(input)
    await user.type(input, '2500')
    expect(screen.queryByText('Use $100–$100,000.')).toBeNull()
    expect(screen.getByRole('button', { name: 'Deploy (paper)' })).toBeEnabled()
    expect(deploy).not.toHaveBeenCalled()
  })

  it('shows a 409 inline with a link to the running autopilot', async () => {
    vi.spyOn(api, 'deployAutopilot').mockRejectedValue(
      new ApiError(409, 'conflict', 'You already have an autopilot running (BTC EMA Trend Rider). Stop it first.'),
    )
    vi.spyOn(api, 'autopilots').mockResolvedValue({ autopilots: [fx.autopilot({ id: 'ap9', strategyId: 'lab1', name: 'BTC EMA Trend Rider' })] })
    const { user, onDeployed } = open()
    await user.click(screen.getByRole('button', { name: 'Deploy to autopilot' }))
    await user.click(screen.getByRole('button', { name: 'Deploy (paper)' }))
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('You already have an autopilot running (BTC EMA Trend Rider). Stop it first.')
    expect(within(alert).getByRole('link', { name: 'Open BTC EMA Trend Rider' })).toHaveAttribute('href', '/strategies/lab1')
    expect(onDeployed).not.toHaveBeenCalled()
  })

  it('is disabled while this strategy already runs', () => {
    open(true)
    expect(screen.getByRole('button', { name: 'Autopilot running' })).toBeDisabled()
  })
})

describe('AutopilotCard', () => {
  const show = (a: Autopilot, trades = [solTrade()]) => vi.spyOn(api, 'autopilot').mockResolvedValue({ autopilot: a, trades })

  it('shows a running autopilot with its books, log and fills', async () => {
    const get = show(fx.autopilot())
    renderPage(<AutopilotCard initial={fx.autopilot()} />)
    const card = screen.getByRole('region', { name: 'Autopilot SOL EMA cross' })
    expect(within(card).getByText('Running')).toBeInTheDocument()
    expect(within(card).getByText('Long 0.42 SOL @ $142.10')).toBeInTheDocument()
    expect(within(card).getByText('$5,003.32')).toBeInTheDocument()
    expect(within(card).getByText('(+0.07%)')).toBeInTheDocument()
    expect(within(card).getByRole('meter', { name: 'Drawdown against the cap' })).toHaveAttribute('aria-valuenow', '2.5')
    expect(within(card).getByText('Next check 12:00 UTC')).toBeInTheDocument()
    expect(within(card).getByText('Deployed with $5,000; checks every closed 4h bar.')).toBeInTheDocument()
    expect(within(card).queryByText(/^stop /)).toBeNull() // an older autopilot has no levels
    expect(get).toHaveBeenCalledWith('ap1')
    expect(await within(card).findByText('0.42 SOL @ $142.10')).toBeInTheDocument() // the fill
  })

  it("shows the levels every entry carries", async () => {
    const a = fx.autopilot({ stopLossPct: 3.2, takeProfitPct: 6.4, trailingStopPct: 3 })
    show(a)
    renderPage(<AutopilotCard initial={a} />)
    expect(screen.getByText('stop 3.2% · target 6.4% · trailing 3%')).toBeInTheDocument()
    expect(await screen.findByText('0.42 SOL @ $142.10')).toBeInTheDocument()
  })

  it('keeps at most 6 log lines and reads a stopped one', async () => {
    const log = Array.from({ length: 9 }, (_, i) => ({ t: '2026-10-02T08:00:00Z', kind: 'sync' as const, text: `Line ${i}` }))
    const stopped = fx.autopilot({ status: 'stopped', stopReason: 'max_drawdown', positionQty: 0, log, nextCheckAt: null })
    show(stopped, [])
    renderPage(<AutopilotCard initial={stopped} />)
    expect(screen.getByText('Stopped (max drawdown)')).toBeInTheDocument()
    expect(screen.getByText('Flat')).toBeInTheDocument()
    expect(screen.getByText('Line 5')).toBeInTheDocument()
    expect(screen.queryByText('Line 6')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Stop' })).toBeNull()
    expect(screen.queryByText(/Next check/)).toBeNull()
    expect(await screen.findByText('No fills yet.')).toBeInTheDocument()
  })

  it('stops and closes at market from the dialog', async () => {
    show(fx.autopilot())
    const closed = fx.autopilot({ status: 'stopped', stopReason: 'user', positionQty: 0, nextCheckAt: null })
    const stop = vi.spyOn(api, 'stopAutopilot').mockResolvedValue({
      autopilot: closed,
      trade: solTrade({ id: 'f2', side: 'sell', price: 150 }),
      portfolio: fx.portfolio,
    })
    const onChange = vi.fn()
    const { user } = renderPage(<AutopilotCard initial={fx.autopilot()} onChange={onChange} />)
    await user.click(screen.getByRole('button', { name: 'Stop' }))
    const dialog = screen.getByRole('dialog', { name: 'Stop “SOL EMA cross”?' })
    expect(dialog).toHaveTextContent('It holds Long 0.42 SOL @ $142.10.')
    expect(within(dialog).getByRole('button', { name: 'Keep the position' })).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: 'Close it at market' }))
    expect(stop).toHaveBeenCalledWith('ap1', true)
    expect(await screen.findByText('Stopped (user)')).toBeInTheDocument()
    expect(screen.getByText('Autopilot stopped')).toBeInTheDocument()
    expect(dialog).not.toHaveAttribute('open')
    expect(onChange).toHaveBeenLastCalledWith(closed)
    expect(await screen.findByText('0.42 SOL @ $150.00')).toBeInTheDocument() // the closing fill, newest first
  })

  it('keeps the position, and shows a failed stop in the dialog', async () => {
    show(fx.autopilot())
    const stop = vi.spyOn(api, 'stopAutopilot').mockRejectedValueOnce(new ApiError(502, 'upstream', 'Market data is unavailable right now.'))
    stop.mockResolvedValueOnce({ autopilot: fx.autopilot({ status: 'stopped', stopReason: 'user', positionQty: 0 }), trade: null, portfolio: fx.portfolio })
    const { user } = renderPage(<AutopilotCard initial={fx.autopilot()} />)
    await user.click(screen.getByRole('button', { name: 'Stop' }))
    const dialog = screen.getByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: 'Keep the position' }))
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('Market data is unavailable right now.')
    await user.click(within(dialog).getByRole('button', { name: 'Keep the position' }))
    expect(stop).toHaveBeenLastCalledWith('ap1', false)
    expect(await screen.findByText('Stopped (user)')).toBeInTheDocument()
  })

  it('offers a single Stop when flat, and a compact strip linking to the strategy', async () => {
    const flat = fx.autopilot({ positionQty: 0 })
    show(flat, [])
    const { user } = renderPage(<AutopilotCard initial={flat} compact />)
    expect(screen.getByRole('link', { name: 'SOL EMA cross' })).toHaveAttribute('href', '/strategies/s1')
    expect(screen.getByText('Watching for entry · next check 12:00 UTC')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Stop' }))
    const dialog = screen.getByRole('dialog')
    expect(dialog).toHaveTextContent('It holds no position')
    expect(within(dialog).queryByRole('button', { name: 'Close it at market' })).toBeNull()
    expect(within(dialog).getByRole('button', { name: 'Stop autopilot' })).toBeInTheDocument()
  })
})

describe('AutopilotCard · disabled account', () => {
  it('reads an autopilot an admin disable stopped', async () => {
    const stopped = fx.autopilot({
      status: 'stopped',
      stopReason: 'account_disabled',
      nextCheckAt: null,
      log: [{ t: '2026-10-02T09:00:00Z', kind: 'stop', text: 'Account disabled by an admin; position kept.' }, ...fx.autopilot().log],
    })
    vi.spyOn(api, 'autopilot').mockResolvedValue({ autopilot: stopped, trades: [] })
    renderPage(<AutopilotCard initial={stopped} />)
    expect(screen.getByText('Stopped: account disabled')).toBeInTheDocument()
    expect(screen.getByText('Account disabled by an admin; position kept.')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Stop' })).toBeNull()
    expect(await screen.findByText('No fills yet.')).toBeInTheDocument()
  })
})

import { fireEvent, screen } from '@testing-library/react'
import { beforeEach, expect, it, vi } from 'vitest'
import { api, ApiError } from '../../lib/api'
import * as fx from '../../test/fixtures'
import { creditsUsed, currentPath, renderPage, stubSession } from '../../test/utils'
import { StrategiesPage } from './StrategiesPage'
import { StrategyDetailPage } from './StrategyDetailPage'

beforeEach(() => {
  stubSession()
  vi.spyOn(api, 'strategies').mockResolvedValue({ strategies: [] })
  vi.spyOn(api, 'autopilots').mockResolvedValue({ autopilots: [] })
})

const open = () => renderPage(<StrategiesPage />, {
  route: '/strategies', path: '/strategies', routes: { '/strategies/:id': <StrategyDetailPage /> },
})
const change = (label: string, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } })
const preview = () => screen.getByRole('region', { name: 'Strategy prompt preview' })

it('keeps text and form drafts plus the shared market and interval when switching', async () => {
  const { user } = open()
  await user.type(screen.getByLabelText('Describe your strategy'), 'My original strategy idea')
  await user.selectOptions(screen.getByLabelText('Market'), 'ETH')
  await user.selectOptions(screen.getByLabelText('Interval'), '1h')
  await user.click(screen.getByRole('button', { name: 'Build with a form' }))
  expect(screen.getByLabelText('Market')).toHaveValue('ETH')
  expect(preview()).toHaveTextContent('ETH on 1h bars')
  change('Fast period', '12')
  await user.type(screen.getByLabelText('Additional instructions'), 'Avoid adding other indicators.')
  await user.click(screen.getByRole('button', { name: 'Describe in text' }))
  expect(screen.getByLabelText('Describe your strategy')).toHaveValue('My original strategy idea')
  await user.click(screen.getByRole('button', { name: 'Build with a form' }))
  expect(screen.getByLabelText('Fast period')).toHaveValue(12)
  expect(screen.getByLabelText('Additional instructions')).toHaveValue('Avoid adding other indicators.')
  await user.click(screen.getByRole('button', { name: 'Edit as text' }))
  const text = screen.getByLabelText<HTMLTextAreaElement>('Describe your strategy').value
  expect(text).toContain('EMA(12) crosses above EMA(50)')
  expect(text).toContain('Avoid adding other indicators.')
  expect(screen.getByLabelText('Interval')).toHaveValue('1h')
})

it('generates the reviewed mean-reversion prompt and opens the returned strategy with updated credits', async () => {
  let finish!: (value: Awaited<ReturnType<typeof api.generateStrategy>>) => void
  const generate = vi.spyOn(api, 'generateStrategy').mockReturnValue(new Promise((resolve) => { finish = resolve }))
  const { user } = open()
  await user.click(screen.getByRole('button', { name: 'Build with a form' }))
  await user.selectOptions(screen.getByLabelText('Market'), 'SOL')
  await user.selectOptions(screen.getByLabelText('Interval'), '15m')
  await user.selectOptions(screen.getByLabelText('Strategy style'), 'mean_reversion')
  await user.selectOptions(screen.getByLabelText('Direction'), 'both')
  change('RSI period', '9')
  change('Oversold level', '25')
  change('Overbought level', '75')
  change('Equity per trade (%)', '20')
  const prompt = preview().textContent!
  expect(prompt).toContain('Enter long when RSI(9) < 25; exit long when RSI(9) > 50.')
  expect(prompt).toContain('Enter short when RSI(9) > 75; exit short when RSI(9) < 50.')
  expect(prompt).toContain('Allocate 20% of equity per trade')
  await user.click(screen.getByRole('button', { name: /Generate & backtest/ }))
  expect(generate).toHaveBeenCalledWith({ prompt, symbol: 'SOL', interval: '15m' })
  expect(screen.getByLabelText('Strategy style')).toBeDisabled()
  expect(screen.getByRole('button', { name: 'Describe in text' })).toBeDisabled()
  expect(screen.getByRole('button', { name: 'Edit as text' })).toBeDisabled()
  finish({ strategy: fx.strategy({ id: 'from-form' }), credits: { ...fx.credits, used: 28 } })
  expect(await screen.findByRole('heading', { level: 1, name: 'SOL EMA cross' })).toBeInTheDocument()
  expect(currentPath()).toBe('/strategies/from-form')
  expect(creditsUsed()).toBe('28')
})

it('composes supported trend and breakout rules for the selected direction', async () => {
  const { user } = open()
  await user.click(screen.getByRole('button', { name: 'Build with a form' }))
  await user.selectOptions(screen.getByLabelText('Moving average'), 'sma')
  await user.selectOptions(screen.getByLabelText('Direction'), 'short')
  expect(preview()).toHaveTextContent('Enter short when SMA(20) crosses below SMA(50); exit short on the reverse cross.')
  expect(preview()).not.toHaveTextContent('Enter long')
  await user.selectOptions(screen.getByLabelText('Strategy style'), 'breakout')
  change('Breakout lookback', '40')
  expect(preview()).toHaveTextContent('Enter short when close < the prior 40-bar low; exit short when close > the prior 40-bar high.')
  expect(preview()).toHaveTextContent('Exclude the current bar from highs and lows.')
  change('Stop loss (%)', '')
  change('Take profit (%)', '')
  expect(preview()).toHaveTextContent('No fixed stop loss. No fixed take profit.')
  expect(preview()).not.toHaveTextContent('Trail the stop')
  expect(screen.getByLabelText('Trailing stop (%)')).toHaveAttribute('placeholder', 'Off')
  change('Trailing stop (%)', '2.5')
  expect(preview()).toHaveTextContent('No fixed take profit. Trail the stop by 2.5%.')
})

it.each([
  ['Fast period', '0', /whole number from 1 to 400/],
  ['Fast period', '20.5', /whole number from 1 to 400/],
  ['Slow period', '401', /whole number from 1 to 400/],
  ['Slow period', '10', /Slow period must be greater/],
  ['Stop loss (%)', '0.05', /0.1–50%/],
  ['Take profit (%)', '51', /0.1–50%/],
  ['Trailing stop (%)', '0.4', /0.5–50%/],
  ['Trailing stop (%)', '51', /0.5–50%/],
  ['Equity per trade (%)', '0', /1–100%/],
  ['Equity per trade (%)', '101', /1–100%/],
])('blocks invalid %s = %s before generation', async (label, value, error) => {
  const { user } = open()
  await user.click(screen.getByRole('button', { name: 'Build with a form' }))
  change(label, value)
  expect(screen.getByRole('alert')).toHaveTextContent(error)
  expect(screen.getByRole('button', { name: /Generate & backtest/ })).toBeDisabled()
  expect(screen.getByRole('button', { name: 'Edit as text' })).toBeDisabled()
})

it('blocks contradictory RSI levels and oversized composed prompts', async () => {
  const { user } = open()
  await user.click(screen.getByRole('button', { name: 'Build with a form' }))
  await user.selectOptions(screen.getByLabelText('Strategy style'), 'mean_reversion')
  change('Oversold level', '60')
  expect(screen.getByRole('alert')).toHaveTextContent('Oversold must be below 50 and overbought above 50')
  change('Oversold level', '30')
  change('Additional instructions', 'x'.repeat(1000))
  expect(screen.getByRole('alert')).toHaveTextContent('1,000 characters')
  expect(screen.getByRole('button', { name: /Generate & backtest/ })).toBeDisabled()
})

it('retains form values on generation failure and uses the existing error display', async () => {
  vi.spyOn(api, 'generateStrategy').mockRejectedValue(new ApiError(502, 'upstream', 'The model returned an invalid spec'))
  const { user } = open()
  await user.click(screen.getByRole('button', { name: 'Build with a form' }))
  change('Fast period', '15')
  await user.click(screen.getByRole('button', { name: /Generate & backtest/ }))
  expect(await screen.findByRole('alert')).toHaveTextContent('The model returned an invalid spec')
  expect(screen.getByLabelText('Fast period')).toHaveValue(15)
  expect(screen.getByRole('button', { name: /Generate & backtest/ })).toBeEnabled()
})

it('allows preview and editing but blocks form generation when credits are exhausted', async () => {
  vi.mocked(api.me).mockResolvedValue({ user: fx.user, credits: { ...fx.credits, used: 499 } })
  const { user } = open()
  await user.click(screen.getByRole('button', { name: 'Build with a form' }))
  expect(await screen.findByText('Daily AI credits used up. They reset at 00:00 UTC.')).toBeInTheDocument()
  expect(screen.getByRole('button', { name: /Generate & backtest/ })).toBeDisabled()
  expect(screen.getByRole('button', { name: 'Edit as text' })).toBeEnabled()
})

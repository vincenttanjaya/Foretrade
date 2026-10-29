import type { Interval } from '../../lib/types'
import { parseTune } from './strategy'

export type StrategyFormValue = {
  style: 'trend' | 'mean_reversion' | 'breakout'
  direction: 'long' | 'short' | 'both'
  average: 'ema' | 'sma'
  fast: string
  slow: string
  period: string
  low: string
  high: string
  lookback: string
  stop: string
  take: string
  size: string
  instructions: string
  trail: string // trailing stop %, '' = off
}

export const defaultStrategyForm = (): StrategyFormValue => ({
  style: 'trend', direction: 'long', average: 'ema', fast: '20', slow: '50',
  period: '14', low: '30', high: '70', lookback: '20', stop: '3', take: '6', size: '25', instructions: '', trail: '',
})

/** These templates use only conditions supported by the rule engine (API contract, Strategy studio). */
export function strategyFormPrompt(form: StrategyFormValue, symbol: string, interval: Interval) {
  const errors: Partial<Record<keyof StrategyFormValue | 'prompt', string>> = {}
  const period = (key: 'fast' | 'slow' | 'period' | 'lookback', label: string) => {
    const n = Number(form[key])
    if (!Number.isInteger(n) || n < 1 || n > 400) errors[key] = `${label}: use a whole number from 1 to 400.`
    return n
  }
  let long: string
  let short: string
  let style: string
  if (form.style === 'trend') {
    const fast = period('fast', 'Fast period')
    const slow = period('slow', 'Slow period')
    if (!errors.fast && !errors.slow && slow <= fast) errors.slow = 'Slow period must be greater than fast period.'
    const ma = form.average.toUpperCase()
    style = 'trend-following'
    long = `Enter long when ${ma}(${fast}) crosses above ${ma}(${slow}); exit long on the reverse cross.`
    short = `Enter short when ${ma}(${fast}) crosses below ${ma}(${slow}); exit short on the reverse cross.`
  } else if (form.style === 'mean_reversion') {
    const p = period('period', 'RSI period')
    const low = Number(form.low)
    const high = Number(form.high)
    if (!(low > 0 && low < 50 && high > 50 && high < 100)) {
      errors.low = 'Oversold must be below 50 and overbought above 50, both strictly between 0 and 100.'
      errors.high = errors.low
    }
    style = 'RSI mean-reversion'
    long = `Enter long when RSI(${p}) < ${low}; exit long when RSI(${p}) > 50.`
    short = `Enter short when RSI(${p}) > ${high}; exit short when RSI(${p}) < 50.`
  } else {
    const p = period('lookback', 'Breakout lookback')
    style = 'Donchian breakout'
    long = `Enter long when close > the prior ${p}-bar high; exit long when close < the prior ${p}-bar low.`
    short = `Enter short when close < the prior ${p}-bar low; exit short when close > the prior ${p}-bar high.`
  }
  Object.assign(errors, parseTune(form).errors)
  const prompt = [
    `Create a ${style} strategy for ${symbol} on ${interval} bars. ${form.direction === 'both' ? 'Long and short' : form.direction === 'long' ? 'Long only' : 'Short only'}.`,
    form.direction !== 'short' ? long : '',
    form.direction !== 'long' ? short : '',
    form.style === 'breakout' ? 'Exclude the current bar from highs and lows.' : '',
    `Allocate ${Number(form.size)}% of equity per trade, one position at a time.`,
    form.stop.trim() ? `Stop loss ${Number(form.stop)}%.` : 'No fixed stop loss.',
    form.take.trim() ? `Take profit ${Number(form.take)}%.` : 'No fixed take profit.',
    form.trail.trim() ? `Trail the stop by ${Number(form.trail)}%.` : '',
    'Evaluate signals on bar close; fill at the next bar open. Include fees and slippage.',
    form.instructions.trim() ? `Additional instructions: ${form.instructions.trim()}` : '',
  ].filter(Boolean).join('\n')
  if (prompt.length > 1000) errors.prompt = 'Keep the complete prompt within 1,000 characters. Shorten the additional instructions.'
  return { prompt, errors, valid: Object.keys(errors).length === 0 }
}

import type { ReactNode } from 'react'
import { Button } from '../../components/ui/Button'
import { Card } from '../../components/ui/Card'
import { Input, Label, Select, Textarea } from '../../components/ui/Input'
import { QUOTA_MESSAGE } from '../../lib/errors'
import type { Interval } from '../../lib/types'
import { useCreditsLeft } from './parts'
import { AI_COST } from './strategy'
import { strategyFormPrompt, type StrategyFormValue } from './strategyFormModel'

/** A guided prompt composer; generation, credits and result navigation remain owned by Studio. */
export function StrategyForm({ value, onChange, symbol, interval, marketControls, busy, onGenerate, onEdit }: {
  value: StrategyFormValue
  onChange: (value: StrategyFormValue) => void
  symbol: string
  interval: Interval
  marketControls: ReactNode
  busy: boolean
  onGenerate: (prompt: string) => void
  onEdit: (prompt: string) => void
}) {
  const set = (patch: Partial<StrategyFormValue>) => onChange({ ...value, ...patch })
  const { prompt, errors, valid } = strategyFormPrompt(value, symbol, interval)
  const left = useCreditsLeft()
  const outOfCredits = left != null && left < AI_COST
  const field = (key: 'fast' | 'slow' | 'period' | 'low' | 'high' | 'lookback' | 'stop' | 'take' | 'size' | 'trail', label: string, min: number, max: number, step = 1) => (
    <div className="min-w-0">
      <Label htmlFor={`strategy-form-${key}`}>{label}</Label>
      <Input id={`strategy-form-${key}`} type="number" inputMode="decimal" min={min} max={max} step={step}
        value={value[key]} onChange={(e) => set({ [key]: e.target.value })}
        aria-invalid={!!errors[key] || undefined} aria-describedby={errors[key] ? 'strategy-form-errors' : undefined}
        placeholder={key === 'stop' || key === 'take' ? 'None' : key === 'trail' ? 'Off' : undefined} className="font-mono tabular-nums" />
    </div>
  )

  return (
    <Card className="p-4 sm:p-5">
      <form noValidate onSubmit={(e) => {
        e.preventDefault()
        if (valid && !busy && !outOfCredits) onGenerate(prompt)
      }}>
        <fieldset disabled={busy} className="min-w-0 space-y-5">
          <legend className="sr-only">Build a strategy with a form</legend>
          <p className="text-[13.5px] leading-relaxed text-muted">Choose the rules and risk settings, then review the prompt. The AI turns it into a strategy you can inspect and backtest.</p>
          <div className="flex flex-wrap gap-3">{marketControls}</div>
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="min-w-0">
              <Label htmlFor="strategy-form-style">Strategy style</Label>
              <Select id="strategy-form-style" value={value.style} onChange={(e) => set({ style: e.target.value as StrategyFormValue['style'] })}>
                <option value="trend">Trend following</option>
                <option value="mean_reversion">Mean reversion</option>
                <option value="breakout">Breakout</option>
              </Select>
            </div>
            <div className="min-w-0">
              <Label htmlFor="strategy-form-direction">Direction</Label>
              <Select id="strategy-form-direction" value={value.direction} onChange={(e) => set({ direction: e.target.value as StrategyFormValue['direction'] })}>
                <option value="long">Long only</option>
                <option value="short">Short only</option>
                <option value="both">Long and short</option>
              </Select>
            </div>
          </div>
          <div>
            <h3 className="label mb-3">Entry and exit rules</h3>
            {value.style === 'trend' ? (
              <div className="grid gap-3 sm:grid-cols-3">
                <div className="min-w-0">
                  <Label htmlFor="strategy-form-average">Moving average</Label>
                  <Select id="strategy-form-average" value={value.average} onChange={(e) => set({ average: e.target.value as StrategyFormValue['average'] })}>
                    <option value="ema">EMA · exponential</option>
                    <option value="sma">SMA · simple</option>
                  </Select>
                </div>
                {field('fast', 'Fast period', 1, 400)}
                {field('slow', 'Slow period', 1, 400)}
              </div>
            ) : value.style === 'mean_reversion' ? (
              <>
                <div className="grid gap-3 sm:grid-cols-3">
                  {field('period', 'RSI period', 1, 400)}
                  {field('low', 'Oversold level', 1, 49, 0.1)}
                  {field('high', 'Overbought level', 51, 99, 0.1)}
                </div>
                <p className="mt-2 text-[12.5px] text-muted">Exit as RSI returns through 50. Long entries use the oversold level; short entries use the overbought level.</p>
              </>
            ) : (
              <>
                <div className="max-w-sm">{field('lookback', 'Breakout lookback', 1, 400)}</div>
                <p className="mt-2 text-[12.5px] text-muted">Break through the prior high or low, excluding the current bar. Exit at the opposite boundary.</p>
              </>
            )}
          </div>
          <div>
            <h3 className="label mb-3">Risk settings</h3>
            <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
              {field('stop', 'Stop loss (%)', 0.1, 50, 0.1)}
              {field('take', 'Take profit (%)', 0.1, 50, 0.1)}
              {field('trail', 'Trailing stop (%)', 0.5, 50, 0.1)}
              {field('size', 'Equity per trade (%)', 1, 100, 1)}
            </div>
            <p className="mt-2 text-[12.5px] text-muted">Stop and target are percentages of entry price. Leave either blank for none. A trailing stop follows the best price since entry; the tighter of it and the stop applies.</p>
          </div>
          <div>
            <Label htmlFor="strategy-form-instructions">Additional instructions</Label>
            <Textarea id="strategy-form-instructions" rows={2} maxLength={1000} value={value.instructions}
              placeholder="Optional preferences for the strategy"
              onChange={(e) => set({ instructions: e.target.value })}
              aria-invalid={!!errors.prompt || undefined} aria-describedby={errors.prompt ? 'strategy-form-errors' : undefined} />
          </div>
          <div>
            <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
              <h3 className="label">Prompt preview</h3>
              <span className="font-mono text-[11px] text-muted tabular-nums">{prompt.length}/1000 characters</span>
            </div>
            <p role="region" aria-label="Strategy prompt preview" className="rounded-xl border border-border bg-surface-2 p-4 text-[13px] leading-relaxed whitespace-pre-line [overflow-wrap:anywhere]">{prompt}</p>
          </div>
          {!valid && <p id="strategy-form-errors" role="alert" className="text-[13px] text-down">{[...new Set(Object.values(errors))].join(' ')}</p>}
          <div className="flex flex-wrap justify-end gap-2 border-t border-border pt-4">
            <Button variant="secondary" disabled={!valid} onClick={() => onEdit(prompt)}>Edit as text</Button>
            <Button type="submit" variant="accent" loading={busy} disabled={!valid || outOfCredits} title={outOfCredits ? QUOTA_MESSAGE : undefined}>
              {busy ? 'Generating…' : 'Generate & backtest'}
              <span className="font-mono text-[11px] font-normal opacity-70">· {AI_COST} cr</span>
            </Button>
          </div>
        </fieldset>
      </form>
    </Card>
  )
}

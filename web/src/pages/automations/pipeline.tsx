// Every automation reads as Trigger → Action → Deliver (a workflow canvas flattened to three nodes):
// the agent's card and the Automations rows share these.
import { Fragment, type ReactNode } from 'react'
import { IconArrowRight } from '../../components/ui/icons'
import { cx } from '../../lib/cx'
import { fmtPrice, fmtUsd } from '../../lib/format'
import type { AlertAction, AlertEvery, AlertOp, AlertTrigger, NotifyWhen } from '../../lib/types'
import { pctLabel } from '../forecasts/OddsAlertButton'

export const WEEKDAYS = ['Sundays', 'Mondays', 'Tuesdays', 'Wednesdays', 'Thursdays', 'Fridays', 'Saturdays']

/** 'Every day · 08:00 UTC', 'Weekdays · 08:00 UTC' or 'Mondays · 09:00 UTC'. */
export const scheduleText = (every?: AlertEvery | null, at?: string | null, weekday?: number | null) =>
  `${every === 'week' ? WEEKDAYS[weekday ?? 0] : every === 'weekdays' ? 'Weekdays' : 'Every day'} · ${at} UTC`

/** 'Polymarket "Will the Fed cut in December?" rises above 25%': an odds trigger, level 0..1. */
export const oddsText = (question: string | undefined, op: AlertOp | null, level: number | null) =>
  `Polymarket “${question ?? 'market'}” ${op === 'below' ? 'falls below' : 'rises above'} ${pctLabel(level ?? 0)}`

/** A price in its currency: fmtPrice for USD, else 'SGD 37.80' (a quote feed's listing price). */
export function fmtIn(n: number | null | undefined, currency = 'USD') {
  if (currency === 'USD') return fmtPrice(n)
  const s = fmtPrice(n)
  return s === '—' ? s : `${currency} ${s.replace('$', '')}`
}

/** 'Jan 19, 2025': a high's daily bar, in UTC like the bars. */
export const athDate = (iso: string) => new Date(iso).toLocaleDateString('en-US', { month: 'short', day: 'numeric', year: 'numeric', timeZone: 'UTC' })

/** 'SOL falls 20% below its all-time high': a drawdown trigger. */
export const drawdownText = (symbol: string, pct: number) => `${symbol} falls ${pct}% below its all-time high`

/**
 * True when a Yahoo all-time high is over 100× the price, the rule the API applies (get_stock_quote, drawdown triggers,
 * the screener): Yahoo's split adjustment scales every bar before a reverse split up (HSDT's high reads in the
 * millions), so the figure is no usable level and a drawdown from it reads −100%. Such a high is never shown, and no
 * drawdown alert is set from it. The API refuses or nulls it; this guards a response that still carries one. Binance
 * and perp-history highs are not split-adjusted, so drawdown callers check `source === 'yahoo'` first.
 */
export const athWithheld = (ath: number | null | undefined, price: number | null | undefined) =>
  typeof ath === 'number' && typeof price === 'number' && price > 0 && ath > 100 * price

/** Shown in place of a withheld high. */
export const ATH_WITHHELD = 'All-time high unavailable: the split-adjusted high in this history is over 100× the price, as reverse splits produce, so it is not a usable level.'

type Runs = {
  trigger?: AlertTrigger
  symbol: string
  action: AlertAction
  notional: number | null
  stopLoss: number | null
  takeProfit: number | null
  prompt?: string | null
  sellPct?: number | null // drawdown sell
}

/**
 * What it does: 'Notify me', the brief, 'Paper buy $100 SOL' or a drawdown's 'Notify + paper sell 100% of the SOL
 * long'; then a price order's stop/target line or the drawdown sale's limits, if any.
 */
export function actionLines(a: Runs): string[] {
  if (a.action === 'research') return [a.prompt || 'AI research']
  if (a.trigger === 'drawdown' && a.action === 'sell')
    return [`Notify + paper sell ${a.sellPct ?? 100}% of the ${a.symbol} long`, 'Never shorts · no long, nothing to sell']
  if (a.action === 'notify') return [a.trigger === 'schedule' ? 'Brief: price, 24h move, sentiment, top headline' : 'Notify me']
  const levels = [a.stopLoss != null && `stop ${fmtPrice(a.stopLoss)}`, a.takeProfit != null && `target ${fmtPrice(a.takeProfit)}`]
  return [`Paper ${a.action} ${fmtUsd(a.notional, { decimals: 0 })} ${a.symbol}`, levels.filter(Boolean).join(' · ')].filter(Boolean)
}

/** Where it lands: the bell, and the account for an order. */
export const deliverText = (action: AlertAction) => (action === 'notify' || action === 'research' ? 'Bell · in-app' : 'Bell · in-app · Account')

/** Research notifications are based on successful reports, with invalidation delivered only on transition. */
export const notifyWhenText = (condition?: NotifyWhen | null) =>
  condition === 'changed'
    ? 'First report, then changes to the verdict or report'
    : condition === 'invalidated'
      ? 'When the thesis becomes invalidated'
      : 'Every successful report'

/** Three nodes, side by side from sm up and stacked below, joined by a line and an arrow. */
export function Pipeline({ trigger, action, deliver, compact, research }: { trigger: ReactNode; action: ReactNode; deliver: ReactNode; compact?: boolean; research?: boolean }) {
  const nodes: [string, ReactNode][] = [
    [research ? 'Schedule' : 'Trigger', trigger],
    [research ? 'AI research' : 'Action', action],
    [research ? 'Notify' : 'Deliver', deliver],
  ]
  return (
    <ol className="flex flex-col sm:flex-row sm:items-stretch">
      {nodes.map(([label, body], i) => (
        <Fragment key={label}>
          {i > 0 && (
            <li aria-hidden="true" className="flex flex-col items-center justify-center text-muted sm:flex-row">
              <span className="h-2 w-px bg-border sm:h-px sm:w-2" />
              <IconArrowRight size={14} className="rotate-90 sm:rotate-0" />
            </li>
          )}
          <li
            className={cx(
              'min-w-0 flex-1 rounded-xl border border-border bg-surface-2 text-text',
              research && '[overflow-wrap:anywhere]',
              compact ? 'px-3 py-2 text-[13px]' : 'px-3.5 py-2.5 text-[13.5px]',
            )}
          >
            <div className="label mb-1">{label}</div>
            {body}
          </li>
        </Fragment>
      ))}
    </ol>
  )
}

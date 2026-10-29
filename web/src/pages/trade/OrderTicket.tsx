import { useEffect, useState, type FormEvent, type ReactNode } from 'react'
import { useToast } from '../../components/toast'
import { Button } from '../../components/ui/Button'
import { IconAgent, IconX } from '../../components/ui/icons'
import { Input } from '../../components/ui/Input'
import { Skeleton } from '../../components/ui/Spinner'
import { Tabs } from '../../components/ui/Tabs'
import { api } from '../../lib/api'
import { useAssets } from '../../lib/assets'
import { cx } from '../../lib/cx'
import { errorMessage } from '../../lib/errors'
import { fmtPct, fmtPrice, fmtQty, fmtUsd } from '../../lib/format'
import { usePortfolio } from '../../lib/portfolio'
import type { Side } from '../../lib/types'
import { feedPriceState } from '../strategies/quoteFeeds'
import { OpenOrders } from './OpenOrders'
import { checkTicket, FEE_RATE, MAX_FILL, MIN_NOTIONAL, resultingQty, roundPrice } from './util'

/**
 * Ticket fields as typed (strings), so the Copilot can pre-fill them. `limit` is a Copilot plan's entry ('' = market).
 * `leverage` (market orders only) makes `amount` the margin: the order's notional is margin × leverage.
 */
export type Ticket = {
  side: Side
  amount: string
  stop: string
  take: string
  source: 'manual' | 'copilot'
  note: string
  limit: string
  leverage: number
}
export const blankTicket = (side: Side = 'buy'): Ticket => ({ side, amount: '', stop: '', take: '', source: 'manual', note: '', limit: '', leverage: 1 })

// ponytail: leverage lives on the ticket; positions don't store it (the Account shows the stop, not the leverage). Store positions.leverage and accrue funding from the Hyperliquid rate when margin accounting matters
export const LEVERAGES = [1, 2, 3, 5, 10]

/** Est. liquidation of simulated isolated margin opened at `price` (0.5% maintenance), rounded like a chart level. */
export const liquidation = (side: Side, price: number, leverage: number) =>
  roundPrice(price * (side === 'buy' ? 1 - 1 / leverage + 0.005 : 1 + 1 / leverage - 0.005))

const optionalNum = (s: string) => (s.trim() ? Number(s) : null)
const round2 = (x: number) => Math.round(x * 100) / 100
const usd = (x: number) => fmtUsd(x, { decimals: Number.isInteger(x) ? 0 : 2 })

function positionText(qty: number, symbol: string) {
  return Math.abs(qty) < 1e-9 ? 'Flat' : `${fmtQty(Math.abs(qty))} ${symbol} ${qty > 0 ? 'long' : 'short'}`
}

/** Paper market order in USD with optional stop / target, or a limit / stop-entry order (a buy/sell price alert). */
export function OrderTicket({
  symbol,
  price,
  ticket,
  onChange,
  onFilled,
  waiting,
}: {
  symbol: string
  price: number | undefined // live price; undefined until known
  ticket: Ticket
  onChange: (t: Ticket) => void
  onFilled: () => void
  waiting?: string // what the price line says until the first price, e.g. a listed stock's first Yahoo quote
}) {
  const { portfolio, setPortfolio, refresh } = usePortfolio()
  const quote = useAssets().bySymbol[symbol]?.quoteFeed // a quote feed outside its session fills at the last close; an unknown state, at the last quote
  // Crypto and perps quote live; a quote feed's open-session price says how fresh it is from its own source and delay
  // ("real-time · 04:20:15 UTC", "about 2 min behind · 04:18 UTC", "delayed · last trade 01:12 UTC"), never "live".
  const priceState = !quote ? 'live' : feedPriceState(quote)
  const livePrice = quote ? 'latest price' : 'live price'
  const { toast } = useToast()
  const [busy, setBusy] = useState(false)
  const [mode, setMode] = useState<'market' | 'limit'>('market')
  const [trigger, setTrigger] = useState<string | null>(null) // null = the default: last price −1% for a buy, +1% for a sell
  const [placed, setPlaced] = useState(0) // bumps OpenOrders after a limit order
  const set = (patch: Partial<Ticket>) => onChange({ ...ticket, ...patch })
  useEffect(() => {
    if (ticket.source !== 'copilot') return
    // "Use this plan" is a market order; "Place limit at …" a limit order at the plan's entry.
    setMode(ticket.limit ? 'limit' : 'market')
    if (ticket.limit) setTrigger(ticket.limit)
  }, [ticket.source, ticket.limit])

  const px = price ?? 0
  const limit = mode === 'limit'
  const triggerText = trigger ?? (px > 0 ? String(roundPrice(px * (ticket.side === 'buy' ? 0.99 : 1.01))) : '')
  const trig = Number(triggerText)
  const op = trig < px ? 'below' : 'above' // a trigger under the mark waits for a dip, over it for a breakout
  const triggerError = limit && px > 0 && !(trig > 0 && trig !== px) ? `Set a trigger price above or below the ${livePrice}.` : undefined
  const fillPx = limit ? (trig > 0 ? trig : 0) : px // the price the order is sized and checked at
  const posQty = portfolio?.positions.find((p) => p.symbol === symbol)?.qty ?? 0
  const bp = portfolio?.buyingPower ?? 0
  const lev = limit ? 1 : ticket.leverage // limit orders stay 1×
  const margin = parseFloat(ticket.amount)
  const amount = lev > 1 ? round2(margin * lev) : margin // the order's notional
  // An order that only trims or closes the position leaves nothing on its side to liquidate (or to stop there).
  const dir = ticket.side === 'buy' ? 1 : -1
  const trims = posQty * dir < 0 && resultingQty(posQty, ticket.side, amount > 0 ? amount : 0, fillPx) * dir <= 0
  const liq = lev > 1 && fillPx > 0 && !trims ? liquidation(ticket.side, fillPx, lev) : null
  const stop = optionalNum(ticket.stop)
  const take = optionalNum(ticket.take)
  // Empty, or past the liquidation price: the order's stop goes at the est. liquidation.
  const stopAtLiq = liq != null && (stop == null || (ticket.side === 'buy' ? stop < liq : stop > liq))
  // A limit order's buying power is checked when it fills, so the ticket doesn't cap it.
  const errors = checkTicket({ side: ticket.side, amount, price: fillPx, posQty, buyingPower: limit ? Infinity : bp, stop, take })
  const ready = portfolio != null && px > 0 && Object.keys(errors).length === 0 && !triggerError
  const amountError = ticket.amount !== '' ? errors.amount : undefined
  const qty = fillPx > 0 && amount > 0 ? amount / fillPx : NaN

  const chip = (pct: number) => String(Math.floor(((bp * (pct / 100) * (pct === 100 ? MAX_FILL : 1)) / lev) * 100) / 100)

  async function submit(e: FormEvent) {
    e.preventDefault()
    if (!ready || busy) return
    setBusy(true)
    try {
      if (limit) {
        await api.createAlert({
          symbol,
          op,
          price: trig,
          action: ticket.side,
          notional: Math.round(amount * 100) / 100,
          stopLoss: stop ?? undefined,
          takeProfit: take ?? undefined,
          note: ticket.note || undefined,
          source: 'ticket',
        })
        toast(`Limit ${ticket.side} placed`)
        onChange(blankTicket(ticket.side))
        setTrigger(null)
        setPlaced((n) => n + 1)
        return
      }
      const { trade, portfolio: next } = await api.placeOrder({
        symbol,
        side: ticket.side,
        notional: round2(amount),
        stopLoss: (stopAtLiq ? liq : stop) ?? undefined,
        takeProfit: take ?? undefined,
        source: ticket.source,
        note: ticket.note || (lev > 1 ? `${lev}× leverage (paper)` : undefined),
      })
      setPortfolio(next)
      const levText = lev > 1 ? ` · ${lev}×${stopAtLiq ? ` · stop at liquidation ${fmtPrice(liq)}` : ''}` : ''
      toast(`${trade.side === 'buy' ? 'Bought' : 'Sold'} ${fmtQty(trade.qty)} ${symbol} at ${fmtPrice(trade.price)}${levText} (paper).`)
      onChange({ ...blankTicket(ticket.side), leverage: ticket.leverage })
      onFilled()
    } catch (err) {
      toast(errorMessage(err, 'The order did not go through. Please try again.'), 'error')
      void refresh() // buying power may have moved since the last poll
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <form onSubmit={submit} noValidate className="space-y-4 p-4">
        {ticket.source === 'copilot' && (
          <div className="flex items-center gap-2 rounded-lg bg-accent-soft py-1.5 pr-1.5 pl-3 text-[12.5px] text-accent-ink">
            <IconAgent size={14} />
            Pre-filled from the Copilot plan
            <button
              type="button"
              onClick={() => set({ source: 'manual', note: '' })}
              aria-label="Treat as a manual order"
              title="Treat as a manual order"
              className="ml-auto grid size-6 place-items-center rounded-md transition-colors hover:bg-accent/15"
            >
              <IconX size={13} />
            </button>
          </div>
        )}

        <Tabs
          aria-label="Order type"
          size="sm"
          value={mode}
          onChange={setMode}
          items={[
            { value: 'market', label: 'Market' },
            { value: 'limit', label: 'Limit' },
          ]}
        />

        <div role="group" aria-label="Side" className="grid grid-cols-2 gap-1 rounded-lg border border-border bg-surface-2 p-1">
          {(['buy', 'sell'] as const).map((s) => {
            const active = ticket.side === s
            return (
              <button
                key={s}
                type="button"
                aria-pressed={active}
                onClick={() => set({ side: s })}
                className={cx(
                  'inline-flex h-9 items-center justify-center gap-1.5 rounded-md text-sm font-medium transition-colors',
                  active ? 'bg-surface text-text shadow-[0_1px_2px_rgb(0_0_0/0.08)]' : 'text-muted hover:text-text',
                )}
              >
                <span aria-hidden="true" className={cx('text-[0.75em]', s === 'buy' ? 'text-up' : 'text-down', !active && 'opacity-50')}>
                  {s === 'buy' ? '▲' : '▼'}
                </span>
                {s === 'buy' ? 'Buy' : 'Sell'}
              </button>
            )
          })}
        </div>

        {!limit && (
          <div className="flex items-center justify-between gap-3">
            <span className="label">Leverage</span>
            <Tabs
              aria-label="Leverage"
              size="sm"
              value={String(ticket.leverage)}
              onChange={(v) => set({ leverage: Number(v) })}
              items={LEVERAGES.map((l) => ({ value: String(l), label: `${l}×` }))}
            />
          </div>
        )}

        <div>
          <div className="mb-1.5 flex items-baseline justify-between gap-2">
            <label htmlFor="ticket-amount" className="label">
              {lev > 1 ? 'Margin (USD)' : 'Amount (USD)'}
            </label>
            <span className="flex items-center gap-1.5 text-[11.5px] text-muted">
              Buying power
              {portfolio ? <span className="font-mono text-text tabular-nums">{fmtUsd(bp)}</span> : <Skeleton className="h-3 w-16" />}
            </span>
          </div>
          <div className="relative">
            <span aria-hidden="true" className="pointer-events-none absolute top-1/2 left-3 -translate-y-1/2 font-mono text-sm text-muted">
              $
            </span>
            <Input
              id="ticket-amount"
              type="number"
              inputMode="decimal"
              min={MIN_NOTIONAL}
              step="any"
              placeholder="0.00"
              value={ticket.amount}
              onChange={(e) => set({ amount: e.target.value })}
              aria-invalid={amountError ? true : undefined}
              aria-describedby="ticket-amount-hint"
              className="h-11 pl-7 font-mono text-[15px] tabular-nums"
            />
          </div>
          <div className="mt-2 grid grid-cols-3 gap-2">
            {[25, 50, 100].map((p) => (
              <button
                key={p}
                type="button"
                disabled={!portfolio || bp < MIN_NOTIONAL}
                onClick={() => set({ amount: chip(p) })}
                className="h-7 rounded-md border border-border font-mono text-[11.5px] text-muted transition-colors hover:border-muted/50 hover:bg-surface-2 hover:text-text disabled:pointer-events-none disabled:opacity-45"
              >
                {p === 100 ? 'Max' : `${p}%`}
              </button>
            ))}
          </div>
          <p id="ticket-amount-hint" className={cx('mt-1.5 min-h-[18px] text-[12px]', amountError ? 'text-down' : 'text-muted')}>
            {amountError ?? (px > 0 ? `${symbol} at ${fmtPrice(px)} · ${priceState}` : (waiting ?? 'Waiting for a price…'))}
          </p>
        </div>

        {limit && (
          <div>
            <label htmlFor="ticket-trigger" className="label mb-1.5 block">
              Trigger price
            </label>
            <Input
              id="ticket-trigger"
              type="number"
              inputMode="decimal"
              min={0}
              step="any"
              value={triggerText}
              onChange={(e) => setTrigger(e.target.value)}
              aria-invalid={triggerError ? true : undefined}
              aria-describedby="ticket-trigger-hint"
              className="font-mono tabular-nums"
            />
            <p id="ticket-trigger-hint" className={cx('mt-1.5 text-[12px]', triggerError ? 'text-down' : 'text-muted')}>
              {triggerError ?? (trig > 0 && px > 0 ? `${fmtPct((trig / px - 1) * 100, { sign: true })} from the ${quote ? 'latest' : 'live'} ${fmtPrice(px)}` : '')}
            </p>
          </div>
        )}

        <div className="grid grid-cols-2 gap-3">
          <LevelInput id="ticket-stop" label="Stop loss" value={ticket.stop} error={errors.stop} onChange={(v) => set({ stop: v })} />
          <LevelInput id="ticket-take" label="Take profit" value={ticket.take} error={errors.take} onChange={(v) => set({ take: v })} />
        </div>
        {liq != null && <p className="-mt-2 text-[12px] text-muted">Empty or past liquidation → the stop is set at the est. liquidation.</p>}

        <dl className="space-y-1.5 rounded-lg bg-surface-2 px-3 py-2.5 text-[12.5px]">
          {lev > 1 && <Row label="Position size">{margin > 0 ? `${usd(amount)} (margin ${usd(margin)} × ${lev}×)` : '—'}</Row>}
          {liq != null && (
            <Row label="Est. liquidation">
              {fmtPrice(liq)} ({fmtPct((liq / fillPx - 1) * 100, { sign: true, decimals: 1 })})
            </Row>
          )}
          <Row label="Est. quantity">{Number.isFinite(qty) ? `${fmtQty(qty)} ${symbol}` : '—'}</Row>
          <Row label="Est. fee (0.1%)">{amount > 0 ? fmtUsd(amount * FEE_RATE) : '—'}</Row>
          {posQty !== 0 && <Row label="Position now">{positionText(posQty, symbol)}</Row>}
          <Row label="Position after">
            {amount > 0 && fillPx > 0 ? positionText(resultingQty(posQty, ticket.side, amount, fillPx), symbol) : '—'}
          </Row>
        </dl>

        <div>
          <Button type="submit" size="lg" className="w-full" loading={busy} disabled={!ready}>
            {limit
              ? `Place limit ${ticket.side}${amount > 0 && trig > 0 ? ` · ${fmtUsd(amount, { decimals: 0 })} at ${fmtPrice(trig)}` : ''}`
              : `Confirm ${ticket.side} (paper)`}
          </Button>
          <p className="mt-2 text-center text-[11.5px] leading-snug text-muted">
            {limit && trig > 0
              ? `Fills at market (± 2 bps) once ${symbol} trades at or ${op} ${fmtPrice(trig)}, checked every 15 s. Buying power is checked when it fills.`
              : liq != null
                ? 'Simulated isolated margin: the position closes at the est. liquidation price (0.5% maintenance). No funding accrues. Paper only.'
                : 'Simulated market order at the latest price ± 2 bps. Nothing reaches a real venue.'}
          </p>
        </div>
      </form>
      <OpenOrders symbol={symbol} version={placed} />
    </>
  )
}

function LevelInput({
  id,
  label,
  value,
  error,
  onChange,
}: {
  id: string
  label: string
  value: string
  error?: string
  onChange: (v: string) => void
}) {
  return (
    <div className="min-w-0">
      <label htmlFor={id} className="label mb-1.5 block">
        {label}
      </label>
      <Input
        id={id}
        type="number"
        inputMode="decimal"
        min={0}
        step="any"
        placeholder="Optional"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        aria-invalid={error ? true : undefined}
        aria-describedby={error ? `${id}-error` : undefined}
        className="font-mono tabular-nums"
      />
      {error && (
        <p id={`${id}-error`} className="mt-1 text-[11.5px] leading-snug text-down">
          {error}
        </p>
      )}
    </div>
  )
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex items-baseline justify-between gap-3">
      <dt className="text-muted">{label}</dt>
      <dd className="truncate font-mono text-text tabular-nums">{children}</dd>
    </div>
  )
}

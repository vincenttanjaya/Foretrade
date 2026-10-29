// Positions and trade-history tables plus their data hooks, shared by the Trade and Account pages.
import { useCallback, useEffect, useRef, useState, type FormEvent, type ReactNode } from 'react'
import { Link, useNavigate } from 'react-router'
import { useToast } from '../../components/toast'
import { AssetIcon } from '../../components/ui/AssetIcon'
import { Badge, type BadgeTone } from '../../components/ui/Badge'
import { Button } from '../../components/ui/Button'
import { IconArrowRight, IconCheck, IconX } from '../../components/ui/icons'
import { PriceChange } from '../../components/ui/PriceChange'
import { Skeleton } from '../../components/ui/Spinner'
import { api } from '../../lib/api'
import { useAssetsIfAny } from '../../lib/assets'
import { cx } from '../../lib/cx'
import { errorMessage } from '../../lib/errors'
import { fmtDate, fmtPct, fmtPrice, fmtQty, fmtUsd, timeAgo } from '../../lib/format'
import { usePortfolio } from '../../lib/portfolio'
import type { Position, Trade, TradeRef, TradeSource } from '../../lib/types'
import { feedPriceLine } from '../strategies/quoteFeeds'

/** Recent fills, newest first. `trades` stays null until the first answer; a reload keeps the old rows on screen. */
export function useTrades(limit: number) {
  const [trades, setTrades] = useState<Trade[] | null>(null)
  const [error, setError] = useState('')
  const [nonce, setNonce] = useState(0)

  useEffect(() => {
    const ctrl = new AbortController()
    api
      .trades(limit, ctrl.signal)
      .then((r) => {
        if (ctrl.signal.aborted) return
        setTrades(r.trades ?? [])
        setError('')
      })
      .catch((err) => {
        if (!ctrl.signal.aborted) setError(errorMessage(err, 'Could not load your trades.'))
      })
    return () => ctrl.abort()
  }, [limit, nonce])

  const reload = useCallback(() => setNonce((n) => n + 1), [])
  return { trades, error, reload }
}

/** Close a whole position at market, then update the portfolio and toast the fill. */
export function useClosePosition(onClosed?: () => void) {
  const { setPortfolio, refresh } = usePortfolio()
  const { toast } = useToast()
  const [closing, setClosing] = useState<string | null>(null)

  const close = async (symbol: string) => {
    setClosing(symbol)
    try {
      const { trade, portfolio } = await api.closePosition(symbol)
      setPortfolio(portfolio)
      const pnl = trade.realizedPnl ? `, realized ${fmtUsd(trade.realizedPnl, { sign: true })}` : ''
      toast(`Closed ${symbol}: ${trade.side === 'sell' ? 'sold' : 'bought back'} ${fmtQty(trade.qty)} at ${fmtPrice(trade.price)}${pnl}.`)
      onClosed?.()
    } catch (err) {
      toast(errorMessage(err, `Could not close ${symbol}.`), 'error')
      void refresh() // e.g. 404: a stop already closed it
    } finally {
      setClosing(null)
    }
  }
  return { closing, close }
}

const th = 'label px-3 py-2.5 font-medium whitespace-nowrap first:pl-4 last:pr-4'
const td = 'px-3 py-3 whitespace-nowrap first:pl-4 last:pr-4'
const num = 'text-right font-mono tabular-nums'

function Direction({ long, children }: { long: boolean; children: ReactNode }) {
  return (
    <span className="inline-flex items-center gap-1.5 text-text">
      <span aria-hidden="true" className={cx('text-[0.7em]', long ? 'text-up' : 'text-down')}>
        {long ? '▲' : '▼'}
      </span>
      {children}
    </span>
  )
}

function Market({ symbol, sub }: { symbol: string; sub?: ReactNode }) {
  return (
    <span className="flex items-center gap-2.5">
      <AssetIcon symbol={symbol} size={24} />
      <span className="min-w-0">
        <Link
          to={`/trade/${symbol}`}
          onClick={(e) => e.stopPropagation()}
          className="font-mono text-[13px] font-medium text-text hover:text-accent-ink"
        >
          {symbol}
        </Link>
        {sub && <span className="block max-w-[180px] truncate text-[11.5px] text-muted">{sub}</span>}
      </span>
    </span>
  )
}

function SkeletonRows({ cols, rows = 3 }: { cols: number; rows?: number }) {
  return Array.from({ length: rows }, (_, i) => (
    <tr key={i}>
      {Array.from({ length: cols }, (_, j) => (
        <td key={j} className={td}>
          <Skeleton className={cx('h-4', j === 0 ? 'w-20' : 'ml-auto w-14')} />
        </td>
      ))}
    </tr>
  ))
}

/**
 * Open positions. `positions` null = loading. Clicking a row opens that market; the symbol is also a
 * real link for keyboard users.
 */
export function PositionsTable({
  positions,
  closing,
  onClose,
  empty,
  active,
}: {
  positions: Position[] | null
  closing: string | null
  onClose: (symbol: string) => void
  empty: ReactNode
  active?: string
}) {
  const navigate = useNavigate()
  const quotes = useAssetsIfAny()?.bySymbol // a quote feed's mark says how fresh it is, as the Trade page does
  if (positions && positions.length === 0) return <div className="p-4">{empty}</div>
  return (
    <div className="relative overflow-x-auto">
      <table className="w-full min-w-[600px] text-[13px]">
        <thead className="bg-surface-2 text-left">
          <tr>
            <th className={th}>Market</th>
            <th className={cx(th, 'text-right')}>Qty</th>
            <th className={cx(th, 'text-right')}>Avg / Mark</th>
            <th className={cx(th, 'text-right')}>Unrealized P&amp;L</th>
            <th className={cx(th, 'text-right')}>Stop / Target</th>
            <th className={th}>
              <span className="sr-only">Actions</span>
            </th>
          </tr>
        </thead>
        <tbody className="divide-y divide-border">
          {!positions ? (
            <SkeletonRows cols={6} />
          ) : (
            positions.map((p) => (
              <tr
                key={p.symbol}
                onClick={() => navigate(`/trade/${p.symbol}`)}
                className={cx('cursor-pointer transition-colors hover:bg-surface-2/60', p.symbol === active && 'bg-accent-soft/40')}
              >
                <td className={td}>
                  <Market symbol={p.symbol} sub={<Direction long={p.qty > 0}>{p.qty > 0 ? 'Long' : 'Short'}</Direction>} />
                </td>
                <td className={cx(td, num)}>{fmtQty(Math.abs(p.qty))}</td>
                <td className={cx(td, num)}>
                  <span className="block">{fmtPrice(p.avgPrice)}</span>
                  <span className="block text-[11.5px] text-muted" title={quotes?.[p.symbol]?.quoteFeed ? `Mark: ${feedPriceLine(quotes[p.symbol]!.quoteFeed!)}` : undefined}>
                    {fmtPrice(p.markPrice)}
                  </span>
                </td>
                <td className={cx(td, 'text-right')}>
                  <PriceChange value={p.unrealizedPnl} kind="usd" className="text-[13px]" />
                  <span className="block font-mono text-[11.5px] text-muted tabular-nums">
                    {fmtPct(p.unrealizedPnlPct, { sign: true })}
                  </span>
                </td>
                <td className={cx(td, num, 'text-muted')}>
                  <LevelEdit position={p} level="stopLoss" />
                  <LevelEdit position={p} level="takeProfit" className="text-[11.5px]" />
                </td>
                <td className={cx(td, 'w-0 text-right')}>
                  <Button
                    variant="secondary"
                    size="sm"
                    loading={closing === p.symbol}
                    disabled={closing != null}
                    aria-label={`Close ${p.symbol} position at market`}
                    onClick={(e) => {
                      e.stopPropagation()
                      onClose(p.symbol)
                    }}
                  >
                    Close
                  </Button>
                </td>
              </tr>
            ))
          )}
        </tbody>
      </table>
    </div>
  )
}

type Level = 'stopLoss' | 'takeProfit'
const LEVEL_NAME: Record<Level, string> = { stopLoss: 'stop', takeProfit: 'target' }

/**
 * One protective level of a position: a button ("Add" when unset) that opens an inline price editor. Enter or ✓ saves
 * through PATCH /api/portfolio/positions/:symbol, Esc cancels, × clears; the API's 400 shows under the cell.
 */
function LevelEdit({ position: p, level, className }: { position: Position; level: Level; className?: string }) {
  const { setPortfolio } = usePortfolio()
  const value = p[level]
  const name = LEVEL_NAME[level]
  const [text, setText] = useState<string | null>(null) // null = not editing
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const input = useRef<HTMLInputElement>(null)
  // A refused save hands focus back to the field (the disabled ✓ dropped it), so the user can fix it or press Esc.
  useEffect(() => {
    if (error && !busy) input.current?.focus()
  }, [error, busy])

  async function save(next: number | null) {
    if (next !== null && !(next > 0)) return setError('Enter a price above $0.')
    setBusy(true)
    setError('')
    try {
      const { portfolio } = await api.setProtection(p.symbol, { [level]: next })
      setPortfolio(portfolio)
      setText(null)
    } catch (err) {
      setError(errorMessage(err, `Could not update the ${name}.`))
    } finally {
      setBusy(false)
    }
  }
  const submit = (e: FormEvent) => {
    e.preventDefault()
    void save(Number(text!.replace(/[$,\s]/g, ''))) // '' → 0, refused like any non-positive price
  }
  const cancel = () => {
    setText(null)
    setError('')
  }

  if (text === null)
    return (
      <span className={cx('block', className)}>
        <button
          type="button"
          aria-label={`Edit ${name} for ${p.symbol}`}
          onClick={(e) => {
            e.stopPropagation()
            setText(value == null ? '' : String(value))
          }}
          className="rounded-sm underline decoration-border decoration-dotted underline-offset-[3px] transition-colors hover:text-accent-ink hover:decoration-accent"
        >
          {value == null ? 'Add' : fmtPrice(value)}
        </button>
      </span>
    )
  return (
    <span className={cx('block py-0.5', className)} onClick={(e) => e.stopPropagation()}>
      <form onSubmit={submit} onKeyDown={(e) => e.key === 'Escape' && cancel()} className="inline-flex items-center gap-1">
        <input
          ref={input}
          autoFocus
          aria-label={`${name === 'stop' ? 'Stop' : 'Target'} for ${p.symbol}`}
          inputMode="decimal"
          autoComplete="off"
          value={text}
          disabled={busy}
          aria-invalid={!!error || undefined}
          onChange={(e) => setText(e.target.value)}
          className="h-7 w-24 rounded-md border border-border bg-surface px-2 text-right font-mono text-[12.5px] text-text outline-none focus:border-accent"
        />
        <button type="submit" disabled={busy} aria-label={`Save ${name} for ${p.symbol}`} className="grid size-7 place-items-center rounded-md text-up hover:bg-up/10 disabled:opacity-50">
          <IconCheck size={14} strokeWidth={2.4} />
        </button>
        {value != null && (
          <button type="button" disabled={busy} aria-label={`Clear ${name} for ${p.symbol}`} onClick={() => void save(null)} className="grid size-7 place-items-center rounded-md text-muted hover:bg-surface-2 hover:text-down disabled:opacity-50">
            <IconX size={14} />
          </button>
        )}
      </form>
      {error && (
        <span role="alert" className="mt-1 block max-w-[240px] text-left text-[11.5px] leading-snug whitespace-normal text-down">
          {error}
        </span>
      )}
    </span>
  )
}

const SOURCES: Record<TradeSource | 'prediction', [label: string, tone: BadgeTone]> = {
  manual: ['Manual', 'neutral'],
  copilot: ['Copilot', 'accent'],
  agent: ['Agent', 'accent'],
  portfolio: ['AI portfolio', 'outline'],
  stop_loss: ['Stop loss', 'down'],
  take_profit: ['Take profit', 'up'],
  autopilot: ['Autopilot', 'accent'],
  alert: ['Alert', 'outline'],
  copy: ['Copy', 'accent'],
  prediction: ['Prediction', 'accent'], // round 5: P&L by source only (paper prediction positions are not trades)
}

export function SourceBadge({ source }: { source: TradeSource | 'prediction' }) {
  const [label, tone] = SOURCES[source] ?? [source, 'neutral']
  return <Badge tone={tone}>{label}</Badge>
}

const ORIGIN: Record<TradeRef['kind'], string> = { chat: '/chat/', portfolio: '/portfolios/', autopilot: '/strategies/', alert: '/automations', copy: '/account#copy-plans' }

/** The source chip, linked to the chat, AI portfolio, strategy or alert the fill came from when the API knows it. */
function Origin({ trade: t }: { trade: Trade }) {
  const to = t.ref && ORIGIN[t.ref.kind]
  if (!t.ref || !to) return <SourceBadge source={t.source} />
  return (
    <Link
      to={t.ref.kind === 'alert' || t.ref.kind === 'copy' ? to : to + encodeURIComponent(t.ref.id)}
      title={t.ref.label}
      className="group inline-flex items-center gap-1 rounded-md"
    >
      <SourceBadge source={t.source} />
      <IconArrowRight size={12} className="text-muted transition-colors group-hover:text-accent-ink" />
    </Link>
  )
}

/** Fill history. `trades` null = loading; an error with no rows shows a retry. */
export function TradesTable({
  trades,
  error,
  onRetry,
  empty,
}: {
  trades: Trade[] | null
  error: string
  onRetry: () => void
  empty: ReactNode
}) {
  if (!trades && error)
    return (
      <div role="alert" className="flex flex-wrap items-center justify-between gap-3 p-4 text-sm text-muted">
        {error}
        <Button variant="secondary" size="sm" onClick={onRetry}>
          Try again
        </Button>
      </div>
    )
  if (trades && trades.length === 0) return <div className="p-4">{empty}</div>
  return (
    <div className="relative overflow-x-auto">
      <table className="w-full min-w-[620px] text-[13px]">
        <thead className="bg-surface-2 text-left">
          <tr>
            <th className={th}>Time</th>
            <th className={th}>Market</th>
            <th className={th}>Side</th>
            <th className={cx(th, 'text-right')}>Qty / Price</th>
            <th className={cx(th, 'text-right')}>Notional / Fee</th>
            <th className={cx(th, 'text-right')}>Realized</th>
            <th className={th}>Source</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-border">
          {!trades ? (
            <SkeletonRows cols={7} />
          ) : (
            trades.map((t) => (
              <tr key={t.id}>
                <td className={cx(td, 'text-muted')} title={fmtDate(t.createdAt, { time: true })}>
                  {timeAgo(t.createdAt)}
                </td>
                <td className={td}>
                  <Market symbol={t.symbol} />
                </td>
                <td className={td}>
                  <Direction long={t.side === 'buy'}>{t.side === 'buy' ? 'Buy' : 'Sell'}</Direction>
                </td>
                <td className={cx(td, num)}>
                  <span className="block">{fmtQty(t.qty)}</span>
                  <span className="block text-[11.5px] text-muted">@ {fmtPrice(t.price)}</span>
                </td>
                <td className={cx(td, num)}>
                  <span className="block">{fmtUsd(t.notional)}</span>
                  <span className="block text-[11.5px] text-muted">{fmtUsd(t.fee)}</span>
                </td>
                <td className={cx(td, 'text-right')}>
                  {t.realizedPnl ? (
                    <PriceChange value={t.realizedPnl} kind="usd" className="text-[13px]" />
                  ) : (
                    <span className="text-muted">—</span>
                  )}
                </td>
                <td className={td}>
                  <Origin trade={t} />
                  {t.note && (
                    <span className="mt-0.5 block max-w-[150px] truncate text-[11.5px] text-muted" title={t.note}>
                      {t.note}
                    </span>
                  )}
                </td>
              </tr>
            ))
          )}
        </tbody>
      </table>
    </div>
  )
}

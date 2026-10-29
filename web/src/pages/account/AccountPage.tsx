import { useEffect, useRef, useState, type ReactNode } from 'react'
import { Link } from 'react-router'
import { PageHeader } from '../../components/layout/AppShell'
import { useToast } from '../../components/toast'
import { Button, buttonClass } from '../../components/ui/Button'
import { Card, CardHeader } from '../../components/ui/Card'
import { EmptyState } from '../../components/ui/EmptyState'
import { IconAccount, IconAgent, IconTrade } from '../../components/ui/icons'
import { LineChart } from '../../components/ui/LineChart'
import { PriceChange } from '../../components/ui/PriceChange'
import { Skeleton } from '../../components/ui/Spinner'
import { Tabs } from '../../components/ui/Tabs'
import { api } from '../../lib/api'
import { cx } from '../../lib/cx'
import { fmtPct, fmtUsd } from '../../lib/format'
import { usePortfolio } from '../../lib/portfolio'
import type { EquityCurve, EquityRange, MarketPosition, Portfolio, SourcePnl, Trade, TradeSource } from '../../lib/types'
import { PositionsTable, SourceBadge, TradesTable, useClosePosition, useTrades } from '../trade/tables'
import { errorMessage } from '../../lib/errors'
import { AccountCopyPlans } from '../arena/copy'
import { PortfolioHealth } from './PortfolioHealth'
import { Personalization } from './Personalization'
import { NotificationSettings } from './NotificationSettings'
import { cents, PositionChip } from '../forecasts/DeskVsMarket'

// On the ink hero the regular --up/--down lack contrast, so it borrows the other theme's values
// (ink is navy in light mode and near-white in dark mode).
const heroTone = (n: number) =>
  n > 0 ? 'text-[#4ADE80] dark:text-[#15803D]' : n < 0 ? 'text-[#F87171] dark:text-[#DC2626]' : 'text-ink-fg/70'

/** /account — equity hero, open positions, fill history, reset. */
export function AccountPage() {
  const { portfolio, setPortfolio } = usePortfolio()
  const { toast } = useToast()
  const trades = useTrades(100)
  const revision = portfolio?.revision ?? null
  const previousRevision = useRef<number | null>(null)
  useEffect(() => {
    if (revision != null && previousRevision.current != null && revision > previousRevision.current) trades.reload()
    previousRevision.current = revision
  }, [revision, trades.reload])
  const { closing, close } = useClosePosition()
  const dialog = useRef<HTMLDialogElement>(null)
  const [resetting, setResetting] = useState(false)
  const count = portfolio?.positions.length ?? 0

  async function reset() {
    setResetting(true)
    try {
      const res = await api.resetPortfolio()
      setPortfolio(res.portfolio)
      dialog.current?.close()
      toast(`Paper account reset. Cash is back to ${fmtUsd(res.portfolio.cash)}.`)
    } catch (err) {
      toast(errorMessage(err, 'Could not reset the account. Please try again.'), 'error')
    } finally {
      setResetting(false)
    }
  }

  const goTrade = (
    <div className="flex flex-wrap justify-center gap-2">
      <Link to="/trade/BTC" className={buttonClass('primary', 'sm')}>
        <IconTrade size={16} /> Open the trade terminal
      </Link>
      <Link to="/" className={buttonClass('secondary', 'sm')}>
        <IconAgent size={16} /> Ask the Agent
      </Link>
    </div>
  )

  return (
    <div className="mx-auto max-w-6xl px-4 py-8 md:px-8 md:py-10">
      <PageHeader
        label="Paper trading · simulated fills"
        title="Paper account"
        subtitle="Equity, open positions and every simulated fill."
        actions={
          <Button variant="danger" disabled={!portfolio} onClick={() => dialog.current?.showModal()}>
            Reset portfolio
          </Button>
        }
      />

      <Hero portfolio={portfolio} />
      <Personalization />
      <NotificationSettings />
      <PortfolioHealth revision={portfolio ? (portfolio.revision ?? 0) : null} />
      <Performance revision={portfolio ? (portfolio.revision ?? 0) : null} trades={trades.trades} tradesFailed={!!trades.error} />

      <AccountCopyPlans revision={revision} onStopped={trades.reload} />

      <Card className="mt-6 overflow-hidden">
        <CardHeader
          label="Open positions"
          title={portfolio ? `${count} ${count === 1 ? 'position' : 'positions'}` : 'Positions'}
        />
        <PositionsTable
          positions={portfolio?.positions ?? null}
          closing={closing}
          onClose={close}
          empty={
            <EmptyState
              icon={<IconAccount size={20} />}
              title="No open positions"
              description="Trade from the terminal with the Copilot, or let the Agent propose a trade you confirm."
              action={goTrade}
            />
          }
        />
      </Card>

      <PredictionMarkets revision={portfolio ? (portfolio.revision ?? 0) : null} />

      <Card className="mt-6 overflow-hidden">
        <CardHeader label="Trade history" title="Every simulated fill" />
        <TradesTable
          trades={trades.trades}
          error={trades.error}
          onRetry={trades.reload}
          empty={
            <EmptyState
              title="No trades yet"
              description="Fills from the ticket, the Agent and AI portfolios all land here, with their source."
              action={goTrade}
            />
          }
        />
      </Card>

      <dialog
        ref={dialog}
        aria-labelledby="reset-title"
        aria-describedby="reset-body"
        onCancel={(e) => resetting && e.preventDefault()}
        className="card m-auto w-[calc(100%-2rem)] max-w-md animate-fade-up p-6 text-text backdrop:bg-[rgb(15_27_45/0.45)] backdrop:backdrop-blur-[2px]"
      >
        <div className="label mb-2">Paper account</div>
        <h2 id="reset-title" className="font-display text-2xl leading-tight font-medium">
          Reset to a fresh {fmtUsd(portfolio?.startingBalance ?? 100_000, { decimals: 0 })}?
        </h2>
        <p id="reset-body" className="mt-2.5 text-sm leading-relaxed text-muted">
          This deletes {count === 0 ? '' : count === 1 ? 'your open position and ' : `all ${count} open positions and `}
          your whole trade history, and restores the starting cash. It can't be undone.
        </p>
        <div className="mt-6 flex justify-end gap-2">
          <Button variant="secondary" disabled={resetting} onClick={() => dialog.current?.close()}>
            Cancel
          </Button>
          <Button variant="danger" loading={resetting} onClick={reset}>
            Reset portfolio
          </Button>
        </div>
      </dialog>
    </div>
  )
}

const RANGES = [
  { value: '1d', label: '1D' },
  { value: '7d', label: '7D' },
  { value: '30d', label: '30D' },
] as const

const DAY_MS = 86_400_000

/** The shortest range that reaches back to the oldest loaded trade (no trades → 1D), so a young account isn't a flat line. */
export function defaultRange(trades: Trade[], now = Date.now()): EquityRange {
  const age = now - Math.min(now, ...trades.map((t) => Date.parse(t.createdAt)).filter(Number.isFinite))
  return age <= DAY_MS ? '1d' : age <= 7 * DAY_MS ? '7d' : '30d'
}

/**
 * Equity curve and P&L by source, loaded with the portfolio and again whenever the account changes (its revision).
 * The range starts at defaultRange once the trades are in (7D if they failed); a tab click wins from then on. The
 * chart always spans at least 1% of the starting balance, so a $16 wiggle on $100k doesn't read as a crash.
 */
function Performance({ revision, trades, tradesFailed }: { revision: number | null; trades: Trade[] | null; tradesFailed: boolean }) {
  const [picked, setPicked] = useState<EquityRange | null>(null)
  const range = picked ?? (trades ? defaultRange(trades) : tradesFailed ? '7d' : null)
  const [curve, setCurve] = useState<EquityCurve | null>(null)
  const [error, setError] = useState('')
  const [sources, setSources] = useState<SourcePnl[]>([])
  const [nonce, setNonce] = useState(0)

  useEffect(() => {
    if (revision == null || range == null) return
    const ctrl = new AbortController()
    setError('')
    api
      .portfolioEquity(range, ctrl.signal)
      .then(setCurve)
      .catch((err) => {
        if (!ctrl.signal.aborted) setError(errorMessage(err, 'Could not load the equity curve.'))
      })
    return () => ctrl.abort()
  }, [range, nonce, revision])

  useEffect(() => {
    if (revision == null) return
    const ctrl = new AbortController()
    api
      .portfolioAttribution(ctrl.signal)
      .then((r) => setSources(r.sources ?? []))
      .catch(() => {}) // ponytail: the card just stays hidden; the next account change retries
    return () => ctrl.abort()
  }, [revision])

  const shown = curve?.range === range ? curve : null
  const total = (k: keyof Omit<SourcePnl, 'source'>) => sources.reduce((a, s) => a + s[k], 0)

  return (
    <>
      <Card className="mt-6">
        <CardHeader
          label="Performance"
          title="Equity"
          actions={<Tabs aria-label="Equity range" size="sm" value={range ?? '7d'} onChange={setPicked} items={RANGES} />}
        />
        <div className="px-5 pb-5">
          {shown ? (
            <LineChart
              series={[{ name: 'Equity', points: shown.points }]}
              format={fmtUsd}
              height={240}
              minSpan={(shown.startingBalance || 100_000) / 100}
            />
          ) : error ? (
            <div role="alert" className="flex h-[240px] flex-col items-center justify-center gap-3 text-sm text-muted">
              {error}
              <Button variant="secondary" size="sm" onClick={() => setNonce((n) => n + 1)}>
                Retry
              </Button>
            </div>
          ) : (
            <Skeleton className="h-[240px] w-full" />
          )}
          <p className="mt-3 font-mono text-[11px] text-muted">
            Rebuilt from your fills and venue candle closes (Binance crypto / Hyperliquid perps) · started at {fmtUsd(curve?.startingBalance ?? 100_000, { decimals: 0 })}
          </p>
        </div>
      </Card>

      {sources.length > 0 && (
        <Card className="mt-6 overflow-hidden">
          <CardHeader label="Attribution" title="P&L by source" />
          {/* `relative` keeps the sr-only ▲/▼ words inside the scroller, so they can't widen the page. */}
          <div className="relative overflow-x-auto">
            <table className="w-full text-[13px]">
              <thead className="bg-surface-2 text-left">
                <tr>
                  <th className={th}>Source</th>
                  <th className={cx(th, 'hidden text-right sm:table-cell')}>Trades</th>
                  <th className={cx(th, 'text-right')}>Realized</th>
                  <th className={cx(th, 'text-right')}>Open</th>
                  <th className={cx(th, 'text-right')}>Total</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-border">
                {sources.map((s) => (
                  <tr key={s.source}>
                    <td className={td}>
                      <SourceBadge source={s.source as TradeSource} />
                    </td>
                    <td className={cx(td, 'hidden text-right font-mono tabular-nums sm:table-cell')}>{s.trades}</td>
                    <td className={cx(td, 'text-right')} title={`Fees ${fmtUsd(s.fees)}`}>
                      <PriceChange value={s.realizedPnl} kind="usd" className="text-[12px] sm:text-[13px]" />
                    </td>
                    <td className={cx(td, 'text-right')}>
                      <PriceChange value={s.unrealizedPnl} kind="usd" className="text-[12px] sm:text-[13px]" />
                    </td>
                    <td className={cx(td, 'text-right font-medium')}>
                      <PriceChange value={s.totalPnl} kind="usd" className="text-[12px] sm:text-[13px]" />
                    </td>
                  </tr>
                ))}
              </tbody>
              <tfoot className="border-t border-border">
                <tr>
                  <td className={cx(td, 'label whitespace-normal')}>All sources</td>
                  <td className={cx(td, 'hidden text-right font-mono tabular-nums sm:table-cell')}>{total('trades')}</td>
                  <td className={cx(td, 'text-right')}>
                    <PriceChange value={total('realizedPnl')} kind="usd" className="text-[12px] sm:text-[13px]" />
                  </td>
                  <td className={cx(td, 'text-right')}>
                    <PriceChange value={total('unrealizedPnl')} kind="usd" className="text-[12px] sm:text-[13px]" />
                  </td>
                  <td className={cx(td, 'text-right font-medium')}>
                    <PriceChange value={total('totalPnl')} kind="usd" className="text-[12px] sm:text-[13px]" />
                  </td>
                </tr>
              </tfoot>
            </table>
          </div>
          <p className="px-5 py-3 text-[12px] leading-relaxed text-muted">
            Positions are split into lots by the source that opened them (first in, first out); stops and targets count
            toward that source. Fees included.
          </p>
        </Card>
      )}
    </>
  )
}

// Tighter on phones, so all four money columns fit at 375 px without scrolling.
const th = 'label px-2 py-2.5 font-medium whitespace-nowrap first:pl-4 last:pr-4 sm:px-3 sm:first:pl-5 sm:last:pr-5'
const td = 'px-2 py-3 whitespace-nowrap first:pl-4 last:pr-4 sm:px-3 sm:first:pl-5 sm:last:pr-5'

function Hero({ portfolio: p }: { portfolio: Portfolio | null }) {
  // Share of the 2× gross-exposure limit in use.
  const used = p && p.equity > 0 ? Math.min(1, p.grossExposure / (2 * p.equity)) : 0

  return (
    <section
      aria-label="Account summary"
      className="relative overflow-hidden rounded-[18px] bg-ink px-6 py-7 text-ink-fg sm:px-8 sm:py-8"
    >
      <div aria-hidden="true" className="pointer-events-none absolute -top-32 -right-24 size-80 rounded-full bg-accent/25 blur-[90px]" />
      <div className="relative grid gap-8 lg:grid-cols-[minmax(0,1.15fr)_minmax(0,1fr)] lg:items-end">
        <div className="min-w-0">
          <div className="label text-ink-fg/60">Paper equity · USD</div>
          {p ? (
            <div className="mt-3 font-display text-[44px] leading-none font-medium tracking-[-0.02em] tabular-nums sm:text-[60px]">
              {fmtUsd(p.equity)}
            </div>
          ) : (
            <Bone className="mt-3 h-[44px] w-72 max-w-full sm:h-[60px]" />
          )}
          <div className="mt-4 flex min-h-6 flex-wrap items-center gap-x-3 gap-y-1 text-sm">
            {p ? (
              <>
                <span className={cx('inline-flex items-baseline gap-1.5 font-mono font-medium tabular-nums', heroTone(p.totalPnl))}>
                  {p.totalPnl !== 0 && (
                    <span aria-hidden="true" className="text-[0.75em]">
                      {p.totalPnl > 0 ? '▲' : '▼'}
                    </span>
                  )}
                  {fmtUsd(p.totalPnl, { sign: true })} ({fmtPct(p.totalPnlPct, { sign: true })})
                </span>
                <span className="text-ink-fg/60">all time, from {fmtUsd(p.startingBalance, { decimals: 0 })}</span>
              </>
            ) : (
              <Bone className="h-5 w-56" />
            )}
          </div>
        </div>

        <dl className="grid grid-cols-2 gap-x-6 gap-y-5 sm:grid-cols-3 lg:grid-cols-2">
          <HeroStat label="Cash" value={p && fmtUsd(p.cash)} />
          <HeroStat label="Buying power" value={p && fmtUsd(p.buyingPower)} />
          <HeroStat label="Gross exposure" value={p && fmtUsd(p.grossExposure)} />
          <HeroStat label="Open positions" value={p && String(p.positions.length)} />
          <HeroStat label="Unrealized P&L" value={p && fmtUsd(p.unrealizedPnl, { sign: true })} tone={p && heroTone(p.unrealizedPnl)} />
          <HeroStat label="Realized P&L" value={p && fmtUsd(p.realizedPnl, { sign: true })} tone={p && heroTone(p.realizedPnl)} />
          {!!p?.predictionStakes && <HeroStat label="In prediction markets" value={fmtUsd(p.predictionStakes)} />}
        </dl>
      </div>

      <div className="relative mt-7 border-t border-ink-fg/15 pt-4">
        <div className="flex items-baseline justify-between gap-3 text-[12.5px]">
          <span className="label text-ink-fg/60">Exposure used</span>
          <span className="font-mono text-ink-fg/80 tabular-nums">{p ? `${Math.round(used * 100)}% of the 2× limit` : '—'}</span>
        </div>
        <div className="mt-2 h-1.5 overflow-hidden rounded-full bg-ink-fg/12">
          <div className="h-full rounded-full bg-accent transition-[width] duration-500" style={{ width: `${used * 100}%` }} />
        </div>
      </div>
    </section>
  )
}

function HeroStat({ label, value, tone }: { label: string; value: ReactNode; tone?: string | null }) {
  return (
    <div className="min-w-0">
      <dt className="label text-ink-fg/60">{label}</dt>
      <dd className={cx('mt-1.5 truncate font-mono text-[15px] font-medium tabular-nums', tone || 'text-ink-fg')}>
        {value || <Bone className="h-5 w-24" />}
      </dd>
    </div>
  )
}

/** Skeleton for the ink hero (the shared Skeleton is tuned for light surfaces). */
function Bone({ className }: { className?: string }) {
  return <span aria-hidden="true" className={cx('block animate-pulse rounded-md bg-ink-fg/12', className)} />
}

/** Paper YES/NO positions on forecasts' matched Polymarket markets (round 5), reloaded when the account changes. */
function PredictionMarkets({ revision }: { revision: number | null }) {
  const [positions, setPositions] = useState<MarketPosition[]>([])

  useEffect(() => {
    if (revision == null) return
    const ctrl = new AbortController()
    api
      .marketPositions(undefined, ctrl.signal)
      .then((r) => setPositions(r.positions ?? []))
      .catch(() => {}) // ponytail: the card keeps its last rows; the next account change retries
    return () => ctrl.abort()
  }, [revision])

  if (!positions.length) return null
  return (
    <Card className="mt-6 overflow-hidden">
      <CardHeader label="Prediction markets" title={`${positions.length} paper ${positions.length === 1 ? 'position' : 'positions'}`} />
      <div className="relative overflow-x-auto">
        <table className="w-full text-[13px]">
          <thead className="bg-surface-2 text-left">
            <tr>
              <th className={th}>Market</th>
              <th className={th}>Side</th>
              <th className={cx(th, 'hidden text-right sm:table-cell')}>Contracts @ price</th>
              <th className={cx(th, 'text-right')}>Stake</th>
              <th className={cx(th, 'text-right')}>Status</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-border">
            {positions.map((p) => (
              <tr key={p.id}>
                <td className={cx(td, 'max-w-[16rem] whitespace-normal! sm:max-w-md')}>
                  <Link to={`/forecasts/${encodeURIComponent(p.forecastId)}`} className="text-text hover:text-accent-ink hover:underline">
                    {p.question}
                  </Link>
                </td>
                <td className={td}>
                  {p.side === 'yes' ? 'Yes' : 'No'}
                  {p.outcome !== 'Yes' && <span className="text-muted"> · {p.outcome}</span>}
                </td>
                <td className={cx(td, 'hidden text-right font-mono tabular-nums sm:table-cell')}>
                  {p.contracts.toFixed(1)} @ {cents(p.price)}
                </td>
                <td className={cx(td, 'text-right font-mono tabular-nums')}>{fmtUsd(p.cost)}</td>
                <td className={cx(td, 'text-right')}>
                  <PositionChip p={p} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <p className="px-5 py-3 text-[12px] leading-relaxed text-muted">
        Bought from paper cash and held at cost until the forecast resolves; a winning contract pays $1. Nothing is sent to Polymarket.
      </p>
    </Card>
  )
}

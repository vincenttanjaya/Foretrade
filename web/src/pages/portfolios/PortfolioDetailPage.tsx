import { useEffect, useState, type FormEvent } from 'react'
import { Link, useLocation, useParams } from 'react-router'
import { useToast } from '../../components/toast'
import { AssetIcon } from '../../components/ui/AssetIcon'
import { Badge } from '../../components/ui/Badge'
import { Button } from '../../components/ui/Button'
import { Card, CardHeader } from '../../components/ui/Card'
import { Disclaimer } from '../../components/ui/Disclaimer'
import { IconArrowRight, IconCheck, IconPlus } from '../../components/ui/icons'
import { Input, Label } from '../../components/ui/Input'
import { LineChart } from '../../components/ui/LineChart'
import { PriceChange } from '../../components/ui/PriceChange'
import { Skeleton } from '../../components/ui/Spinner'
import { Tabs } from '../../components/ui/Tabs'
import { api } from '../../lib/api'
import { useAuth } from '../../lib/auth'
import { usePrivateSnapshot } from '../../lib/usePrivateSnapshot'
import { cx } from '../../lib/cx'
import { fmtDate, fmtPct, fmtPrice, fmtQty, fmtUsd } from '../../lib/format'
import { usePortfolio } from '../../lib/portfolio'
import type { AIPortfolio, AIPortfolioInvested, HoldingDriver, Trade } from '../../lib/types'
import { BackLink, LoadError } from '../strategies/parts'
import { errorMessage } from '../../lib/errors'
import { assetName, fmtDD, fmtIndex, PALETTE, rebaseSince } from '../strategies/strategy'
import { MAX_FILL } from '../trade/util'
import { allocate, comparison, measuredWindow, MIN_INVEST, RANGE_DAYS, rangeStats, shares, sinceStats, type Range } from './portfolio'

/** /portfolios/:id — thesis, allocation, backtest vs BTC and a paper Invest card. */
export function PortfolioDetailPage() {
  const { id = '' } = useParams()
  return <Detail key={id} id={id} />
}

function Detail({ id }: { id: string }) {
  const seeded = (useLocation().state as { portfolio?: AIPortfolio } | null)?.portfolio
  const { user } = useAuth()
  const { portfolio, refresh } = usePortfolio()
  // -1 enables the owner's static detail before Account is ready; no private projection is accepted then.
  const revision = user ? (portfolio ? portfolio.revision ?? 0 : -1) : null
  const { value, error, failure, busy, retry } = usePrivateSnapshot(`${user?.id ?? ''}:${id}`, revision, (signal) => api.aiPortfolio(id, signal).then(({ portfolio: p }) => portfolio ? p : { ...p, overlap: null }), (p) => p.overlap, refresh)
  // Retain allocation/backtest through revision refreshes, without retaining the old private projection.
  const [basket, setBasket] = useState<{ owner: string; value: AIPortfolio } | null>(null)
  useEffect(() => { if (value && user) setBasket({ owner: user.id, value: { ...value, overlap: null } }) }, [value, user])
  // Router list/build seeds only the static basket. Private caller facts always come from detail.
  const p = value ? (portfolio ? value : { ...value, overlap: null }) : (basket && basket.owner === user?.id ? basket.value : seeded?.id === id ? { ...seeded, overlap: null } : null)

  return (
    <div className="mx-auto max-w-6xl px-4 py-8 md:px-8 md:py-10">
      <BackLink to="/portfolios">AI portfolios</BackLink>
      {p ? (
        <Loaded p={p} onInvested={retry} overlapError={error} overlapBusy={busy} />
      ) : error ? (
        <LoadError error={failure} what="portfolio" back={{ to: '/portfolios', label: 'Back to portfolios' }} onRetry={retry} />
      ) : (
        <DetailSkeleton />
      )}
    </div>
  )
}

function Loaded({ p, onInvested, overlapError, overlapBusy }: { p: AIPortfolio; onInvested: () => void; overlapError: string; overlapBusy: boolean }) {
  const holdings = p.holdings ?? []
  return (
    <>
      <header className="max-w-3xl">
        <div className="label">AI portfolio · built {fmtDate(p.createdAt)}</div>
        <h1 className="mt-3 font-display text-[34px] leading-[1.06] font-medium tracking-[-0.015em] text-balance text-text md:text-[46px]">
          {p.name}
        </h1>
        {p.thesis && <p className="mt-3 text-[15.5px] leading-relaxed text-pretty text-muted">{p.thesis}</p>}
      </header>

      {p.prompt && (
        <figure className="mt-5 flex max-w-2xl items-end gap-2.5">
          <span aria-hidden="true" className="grid size-7 shrink-0 place-items-center rounded-full bg-ink text-[11px] font-semibold text-ink-fg">
            You
          </span>
          <blockquote className="relative rounded-2xl rounded-bl-sm border border-border bg-surface px-4 py-2.5 text-[14.5px] leading-relaxed text-text shadow-[0_1px_2px_rgb(15_27_45/0.04)]">
            <span className="sr-only">Your prompt: </span>“{p.prompt}”
          </blockquote>
        </figure>
      )}

      {p.invested && <PaperPosition inv={p.invested} />}

      <div className="mt-8 grid items-start gap-6 lg:grid-cols-[minmax(0,1fr)_22rem]">
        <div className="flex min-w-0 flex-col gap-6">
          <Allocation holdings={holdings} />
          <Overlap value={p.overlap} error={overlapError} busy={overlapBusy} retry={onInvested} />
          <Backtest p={p} />
        </div>
        <Invest p={p} onInvested={onInvested} />
      </div>
    </>
  )
}

function Overlap({ value: o, error, busy, retry }: { value: AIPortfolio['overlap']; error: string; busy: boolean; retry: () => void }) {
  return <section aria-label="Overlap with what you hold" aria-busy={busy} className="card p-5">
    <h2 className="font-display text-[19px] font-medium text-text">Overlap with what you hold</h2>
    {o && <p className="mt-2 break-words font-mono text-[11px] text-muted">{error ? 'Stale · ' : busy ? 'Refreshing · ' : ''}{o.asOf} · revision {o.revision} (request time)</p>}
    {error && <div role="alert" className="mt-3 text-sm text-muted">{error} <Button size="sm" variant="secondary" onClick={retry}>Retry overlap</Button></div>}
    {!o ? <p className="mt-3 text-sm text-muted">{busy ? 'Loading current overlap…' : 'Current overlap unavailable. No inference about diversification.'}</p> : <>
      {o.coverage.reason && <p className="mt-3 text-[12px] text-muted">{o.coverage.reason}</p>}
      {!!o.coverage.missingSymbols.length && <p className="mt-2 text-[12px] text-muted">Missing support: {o.coverage.missingSymbols.join(', ')}</p>}
      <p className="mt-2 text-[12px] text-muted">Modeled gross: {fmtUsd(o.coverage.modeledGrossExposure)} · {o.coverage.commonDays} book/BTC common days. Pair samples below describe each comparison.</p>
      {!o.holdings.length && <p className="mt-3 text-sm text-muted">No current cash-asset holdings to compare.</p>}
      <ul className="mt-3 space-y-3 text-[13px] text-text">{o.holdings.map((h) => <li key={h.symbol}><span className="font-mono font-medium">{h.symbol} · {h.side}</span><span className="block text-muted">{fmtPct(h.bookPct, { decimals: 0 })} of your gross book · {fmtPct(h.basketPct, { decimals: 0 })} of basket</span><span className="text-[12px] text-muted">{h.shared ? h.side === 'short' ? 'Shared symbol · may offset basket longs' : 'Shared symbol · same long direction' : 'Different symbol · correlation can still overlap'}</span></li>)}</ul>
      <ul aria-label="Basket to book correlations" className="mt-4 space-y-2 text-[12px] text-muted">{o.correlations.map((p) => <li key={`${p.a}-${p.b}`}><span className="font-mono text-text">{p.a} · {p.b}</span> · {p.rho == null ? 'Unknown' : `ρ ${p.rho.toFixed(2)}`} · {p.days} shared days</li>)}</ul>
      <p className="mt-3 text-[12px] text-muted">Raw asset-return correlations; shorts reverse position direction. Missing shared tickers or measurements do not establish independence. Outcome stakes are excluded.</p>
    </>}
  </section>
}

function Allocation({ holdings }: { holdings: AIPortfolio['holdings'] }) {
  const w = shares(holdings)
  return (
    <Card>
      <CardHeader label="Allocation" title={`${holdings.length} ${holdings.length === 1 ? 'holding' : 'holdings'}, weighted by the AI`} />
      <div className="grid items-start gap-6 px-5 pb-5 sm:grid-cols-[176px_1fr]">
        <Donut holdings={holdings} w={w} />
        <ul className="flex flex-col divide-y divide-border">
          {holdings.map((h, i) => (
            <li key={h.symbol} className="py-3 first:pt-0 last:pb-0">
              <div className="flex items-center gap-2.5">
                <span aria-hidden="true" className="h-6 w-1 shrink-0 rounded-full" style={{ background: PALETTE[i % PALETTE.length] }} />
                <AssetIcon symbol={h.symbol} size={26} />
                <div className="min-w-0 flex-1">
                  <div className="flex items-baseline gap-2">
                    <span className="font-mono text-[13px] font-semibold text-text">{h.symbol}</span>
                    <span className="truncate text-[12.5px] text-muted">{assetName(h.symbol)}</span>
                  </div>
                </div>
                <span className="font-mono text-[15px] font-semibold text-text tabular-nums">{fmtPct(w[i] * 100, { decimals: 0 })}</span>
              </div>
              {h.rationale && <p className="mt-1.5 pl-[46px] text-[13px] leading-relaxed text-muted">{h.rationale}</p>}
              {h.driver && <DriverChip driver={h.driver} />}
            </li>
          ))}
        </ul>
      </div>
      <Disclaimer className="px-5 pb-4" />
    </Card>
  )
}

/** Why the AI holds it: a forecast driver links to its report; sentiment and stats are muted labels. */
function DriverChip({ driver }: { driver: HoldingDriver }) {
  const chip = 'ml-[46px] mt-2 inline-flex max-w-[calc(100%-46px)] items-center rounded-md border px-2 py-0.5 text-[12px]'
  const text = `Driven by ${driver.label}`
  if (driver.kind === 'forecast' && driver.id)
    return (
      <Link
        to={`/forecasts/${driver.id}`}
        className={cx(chip, 'border-accent/40 bg-accent-soft text-accent-ink transition-colors hover:border-accent')}
      >
        {text}
        <span aria-hidden="true">&nbsp;→</span>
      </Link>
    )
  return <span className={cx(chip, 'border-border text-muted')}>{text}</span>
}

/** SVG donut, one arc per holding in PALETTE order, with a hairline gap between arcs. */
function Donut({ holdings, w }: { holdings: AIPortfolio['holdings']; w: number[] }) {
  const r = 52
  const c = 2 * Math.PI * r
  const gap = holdings.length > 1 ? 2 : 0
  let offset = 0
  const top = w.reduce((best, x, i) => (x > w[best] ? i : best), 0)

  return (
    <figure className="relative mx-auto size-44 sm:sticky sm:top-6">
      <svg viewBox="0 0 120 120" className="size-full -rotate-90" role="img" aria-label={`Allocation: ${holdings.map((h, i) => `${h.symbol} ${Math.round(w[i] * 100)}%`).join(', ')}`}>
        <circle cx="60" cy="60" r={r} fill="none" strokeWidth="14" className="stroke-surface-2" />
        {holdings.map((h, i) => {
          const len = Math.max(0, w[i] * c - gap)
          const arc = (
            <circle
              key={h.symbol}
              cx="60"
              cy="60"
              r={r}
              fill="none"
              stroke={PALETTE[i % PALETTE.length]}
              strokeWidth="14"
              strokeDasharray={`${len} ${c - len}`}
              strokeDashoffset={-offset}
            />
          )
          offset += w[i] * c
          return arc
        })}
      </svg>
      {holdings[top] && (
        <figcaption aria-hidden="true" className="absolute inset-0 grid place-content-center text-center">
          <span className="label">Largest</span>
          <span className="mt-0.5 font-display text-[26px] leading-none font-medium text-text">{holdings[top].symbol}</span>
          <span className="mt-1 font-mono text-[12px] text-muted tabular-nums">{Math.round(w[top] * 100)}%</span>
        </figcaption>
      )}
    </figure>
  )
}

const SINCE = 'Since invest'
type View = Range | typeof SINCE

function Backtest({ p }: { p: AIPortfolio }) {
  // Once invested, the detail carries the actual paper series since the first buy: shown first.
  const inv = p.invested
  const selected = comparison(p.backtest)
  const actualBenchmark = inv?.benchmarkSeries !== undefined ? inv.benchmarkSeries : comparison(inv ?? undefined).symbol === 'BTC' ? inv?.btcSeries : null
  const actual = inv?.series && inv.series.length > 1 ? { series: inv.series, benchmark: actualBenchmark ?? [] } : null
  const [picked, setView] = useState<View>(actual ? SINCE : '6M')
  const view: View = picked === SINCE && !actual ? '6M' : picked
  const since = view === SINCE && actual
  const range = since ? '6M' : (view as Range)
  const bt = p.backtest
  const series = since ? actual.series : rebaseSince(bt?.series ?? [], RANGE_DAYS[range])
  const bench = since ? actual.benchmark : rebaseSince(bt?.benchmark ?? [], RANGE_DAYS[range])
  const fallback = { returnPct: bt?.returnPct, maxDrawdownPct: bt?.maxDrawdownPct, volatilityAnnPct: bt?.volatilityAnnPct }
  const live = since ? sinceStats(actual.series, actual.benchmark) : null
  const stats = live ?? rangeStats(series) ?? fallback
  const btc = since ? (live?.benchmarkReturnPct ?? null) : (rangeStats(bench)?.returnPct ?? (bench.length ? bt?.benchmarkReturnPct : null))
  const edge = stats.returnPct != null && btc != null ? stats.returnPct - btc : null

  return (
    <Card className="p-5">
      <div className="mb-4 flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="label">{since ? 'Your paper buys · measured retained window' : 'Backtest · buy & hold, no rebalancing'}</div>
          <h2 className="mt-1 font-display text-[19px] leading-snug font-medium text-text">{`${since ? 'Actual' : 'Portfolio'} vs ${selected.label}`}</h2>
        </div>
        <Tabs
          size="sm"
          value={view}
          onChange={setView}
          items={actual ? ([SINCE, '1M', '3M', '6M'] as const) : (['1M', '3M', '6M'] as const)}
          aria-label="Backtest window"
        />
      </div>
      {series.length > 1 ? (
        <LineChart
          height={280}
          format={fmtIndex}
          series={[
            { name: since ? 'Actual (paper)' : p.name || 'Portfolio', points: series },
            ...(bench.length ? [{ name: selected.label, points: bench }] : []),
          ]}
        />
      ) : (
        <p className="grid h-[308px] place-items-center rounded-lg bg-surface-2/60 text-sm text-muted">No backtest data for this portfolio.</p>
      )}
      <dl className="mt-5 grid grid-cols-2 gap-x-4 gap-y-4 border-t border-border pt-4 sm:grid-cols-4">
        <div>
          <dt className="label">Return · {since ? 'paper buys' : 'measured window'}</dt>
          <dd className="mt-1.5 text-[17px] font-medium">
            <PriceChange value={stats.returnPct} decimals={1} />
          </dd>
          <dd className="mt-0.5 text-[12px] text-muted">{edge == null ? ' ' : `${edge >= 0 ? '+' : '−'}${Math.abs(edge).toFixed(1)} pts vs ${selected.label}`}</dd>
        </div>
        <div>
          <dt className="label">{selected.label} return</dt>
          <dd className="mt-1.5 text-[17px] font-medium">
            <PriceChange value={btc} decimals={1} />
          </dd>
        </div>
        <div>
          <dt className="label">Max drawdown</dt>
          <dd className="mt-1.5 font-mono text-[17px] font-medium text-text tabular-nums">{fmtDD(stats.maxDrawdownPct)}</dd>
        </div>
        <div>
          <dt className="label">Volatility · ann.</dt>
          <dd className="mt-1.5 font-mono text-[17px] font-medium text-text tabular-nums">{fmtPct(stats.volatilityAnnPct, { decimals: 1 })}</dd>
        </div>
      </dl>
      <p className="mt-4 break-words text-[12px] text-muted">{since && inv ? measuredWindow({ ...inv, series }, 'plotted') : measuredWindow({ series, windowStart: series[0]?.t, windowEnd: series.at(-1)?.t, bars: series.length })}</p>
      {since && !bench.length && <p className="mt-2 text-[12px] text-muted">Comparison unavailable for {selected.label} across the first investment and plotted window.</p>}
      <p className="mt-4 text-[12px] text-muted">
        {since ? 'Your invest buys, fees included, valued at each instrument’s venue closes. Retained history may begin after your first buy; sells are not replayed.' : 'Closed daily candles from Binance spot and Hyperliquid perps, according to each instrument. Past performance is not a forecast.'}
      </p>
    </Card>
  )
}

/** How the caller's paper buys of this basket are doing (the API's FIFO lots of its invest legs, at live marks). */
function PaperPosition({ inv }: { inv: AIPortfolioInvested }) {
  const since = new Date(inv.firstAt).toLocaleDateString('en-US', { month: 'short', day: 'numeric' })
  const stats = [
    { label: 'Invested', value: fmtUsd(inv.amount, { decimals: 0 }) },
    {
      label: 'Now',
      value: (
        <>
          {fmtUsd(inv.amount + inv.totalPnl, { decimals: 0 })} <PriceChange value={inv.returnPct} decimals={2} className="text-[13px]" />
        </>
      ),
    },
    { label: `${comparison(inv).label} since then`, value: <PriceChange value={inv.benchmarkReturnPct !== undefined ? inv.benchmarkReturnPct : comparison(inv).symbol === 'BTC' ? inv.btcReturnPct : null} decimals={1} className="text-[13px]" /> },
    { label: 'Since', value: since },
  ]
  return (
    <section aria-label="Your paper position" className="card mt-6 flex flex-wrap items-center gap-x-8 gap-y-3 px-5 py-4">
      <div className="label w-full sm:w-auto">Your paper position</div>
      <dl className="flex flex-wrap gap-x-8 gap-y-3">
        {stats.map((s) => (
          <div key={s.label}>
            <dt className="label">{s.label}</dt>
            <dd className="mt-1 flex items-baseline gap-1.5 font-mono text-[15px] font-medium text-text tabular-nums">{s.value}</dd>
          </div>
        ))}
      </dl>
    </section>
  )
}

type Filled = { trades: Trade[]; skipped: { symbol: string; reason: string }[] }
const PICKS = [1000, 5000, 10000, 25000]

/**
 * The paper Invest card. Once the basket is held the form folds behind "Invest more"; after a fill the receipt stays
 * and the form does not re-arm, so a second tap cannot invest twice.
 */
function Invest({ p, onInvested }: { p: AIPortfolio; onInvested: () => void }) {
  const { toast } = useToast()
  const { portfolio, refresh } = usePortfolio()
  const [text, setText] = useState('10000')
  const [busy, setBusy] = useState(false)
  const [filled, setFilled] = useState<Filled | null>(null)
  const [open, setOpen] = useState(!p.invested)
  const amount = Number(text.replace(/[$,\s]/g, ''))
  const holdings = p.holdings ?? []
  const slices = allocate(amount, holdings)
  const bp = portfolio?.buyingPower
  // Each leg's fee and slippage use buying power too: investing all of it fails a late leg after earlier ones fill.
  // Floored to cents so the cap the message shows is itself accepted.
  const max = bp != null ? Math.max(0, Math.floor(bp * MAX_FILL * 100) / 100) : Infinity
  const problem = !text.trim()
    ? 'Enter an amount.'
    : !Number.isFinite(amount) || amount < MIN_INVEST
      ? `The minimum is ${fmtUsd(MIN_INVEST, { decimals: 0 })}.`
      : amount > max
        ? `Above your buying power. The most you can invest is ${fmtUsd(max)}.`
        : null

  async function invest(e: FormEvent) {
    e.preventDefault()
    if (problem || busy || !holdings.length) return
    setBusy(true)
    try {
      const res = await api.investPortfolio(p.id, amount)
      const skipped = res.skipped ?? [] // holdings under the $10 minimum order
      const trades = res.trades ?? []
      setFilled({ trades, skipped })
      setOpen(false)
      const spent = trades.reduce((a, t) => a + t.notional, 0)
      toast(`Invested ${fmtUsd(spent)} in ${trades.length} ${trades.length === 1 ? 'holding' : 'holdings'} (paper).`)
    } catch (err) {
      toast(errorMessage(err, 'The investment could not be completed. Refresh your account before retrying.'), 'error')
    } finally {
      setBusy(false)
      onInvested() // also after partial failure: an earlier leg may have filled
      void refresh()
    }
  }

  if (!open)
    return (
      <Card className="lg:sticky lg:top-6">
        <CardHeader label="Invest · paper" title={filled ? 'Orders filled' : 'You hold this basket'} />
        <div className="px-5 pb-5">
          {filled ? (
            <FilledList filled={filled} />
          ) : (
            <p className="text-[13.5px] leading-relaxed text-muted">
              Your paper position is live; it is tracked above, and every leg is in your{' '}
              <Link to="/account" className="font-medium text-accent-ink hover:underline">
                paper account
              </Link>
              .
            </p>
          )}
          <Button
            variant="secondary"
            className="mt-4 w-full"
            onClick={() => {
              setFilled(null)
              setOpen(true)
            }}
          >
            <IconPlus size={16} /> Invest more
          </Button>
        </div>
      </Card>
    )

  return (
    <Card className="lg:sticky lg:top-6">
      <CardHeader label="Invest · paper" title={p.invested ? 'Invest more' : 'Put it to work'} />
      <form onSubmit={invest} className="px-5 pb-5">
        <Label htmlFor="invest-amount">Amount (USD)</Label>
        <div className="relative">
          <span aria-hidden="true" className="pointer-events-none absolute top-1/2 left-3 -translate-y-1/2 font-mono text-[15px] text-muted">
            $
          </span>
          <Input
            id="invest-amount"
            inputMode="decimal"
            autoComplete="off"
            value={text}
            disabled={busy}
            aria-invalid={!!problem || undefined}
            aria-describedby="invest-hint"
            onChange={(e) => setText(e.target.value)}
            className="h-12 pl-7 font-mono text-[18px] tabular-nums"
          />
        </div>
        <div className="mt-2 flex flex-wrap gap-1.5">
          {PICKS.map((v) => (
            <button
              key={v}
              type="button"
              disabled={busy}
              onClick={() => setText(String(v))}
              aria-pressed={amount === v}
              className={cx(
                'h-7 rounded-md border px-2.5 font-mono text-[12px] transition-colors disabled:opacity-55',
                amount === v ? 'border-ink bg-ink text-ink-fg' : 'border-border bg-surface text-text hover:bg-surface-2',
              )}
            >
              {fmtUsd(v, { compact: true })}
            </button>
          ))}
        </div>
        <p id="invest-hint" className={cx('mt-2 min-h-5 text-[12.5px]', problem ? 'text-down' : 'text-muted')}>
          {problem ?? (bp != null ? `Buying power ${fmtUsd(bp)}` : ' ')}
        </p>

        <ul aria-label="Order preview" className="mt-3 flex flex-col gap-2 rounded-xl bg-surface-2/70 p-3">
          {slices.map((s) => (
            <li key={s.symbol} className="flex items-center gap-2 text-[13px]">
              <AssetIcon symbol={s.symbol} size={20} />
              <span className="font-mono font-medium text-text">{s.symbol}</span>
              <span className="font-mono text-[11.5px] text-muted">{fmtPct(s.weight * 100, { decimals: 0 })}</span>
              <span className={cx('ml-auto font-mono tabular-nums', s.skipped ? 'text-muted line-through' : 'text-text')}>
                {problem && !(amount > 0) ? '—' : fmtUsd(s.notional)}
              </span>
            </li>
          ))}
          {!problem && slices.some((s) => s.skipped) && (
            <li className="text-[11.5px] text-muted">Slices under $10 are skipped (minimum order).</li>
          )}
        </ul>

        <Button type="submit" variant="accent" size="lg" loading={busy} disabled={!!problem || !holdings.length} className="mt-4 w-full">
          {busy ? 'Placing paper orders…' : `Invest ${problem ? '' : fmtUsd(amount, { decimals: amount % 1 ? 2 : 0 })} (paper)`}
        </Button>
        <p className="mt-2.5 text-center text-[11.5px] leading-relaxed text-muted">
          Market buys at the live price ± 2 bps, 0.1% fee. Simulated, nothing leaves this demo.
        </p>
      </form>
    </Card>
  )
}

function FilledList({ filled }: { filled: Filled }) {
  const fees = filled.trades.reduce((a, t) => a + (t.fee || 0), 0)
  return (
    <div aria-live="polite" className="animate-fade-up">
      <div className="flex items-center gap-2">
        <span className="grid size-5 place-items-center rounded-full bg-up/12 text-up">
          <IconCheck size={12} strokeWidth={2.6} />
        </span>
        <span className="text-[13.5px] font-medium text-text">
          {filled.trades.length} paper {filled.trades.length === 1 ? 'order' : 'orders'} filled
        </span>
        <Badge tone="outline" className="ml-auto">
          Paper
        </Badge>
      </div>
      <ul className="mt-3 flex flex-col gap-2">
        {filled.trades.map((t) => (
          <li key={t.id} className="flex items-center gap-2 text-[12.5px]">
            <AssetIcon symbol={t.symbol} size={18} />
            <span className="min-w-0 truncate text-muted">
              <span className="font-mono text-text">{fmtQty(t.qty)}</span> {t.symbol} @ {fmtPrice(t.price)}
            </span>
            <span className="ml-auto font-mono text-text tabular-nums">{fmtUsd(t.notional)}</span>
          </li>
        ))}
        {filled.skipped.map((s) => (
          <li key={s.symbol} className="text-[12px] text-muted">
            Skipped {s.symbol}: {s.reason}
          </li>
        ))}
      </ul>
      <div className="mt-3 flex items-center justify-between text-[12px] text-muted">
        <span>Fees {fmtUsd(fees)}</span>
        <Link to="/account" className="inline-flex items-center gap-1 font-medium text-accent-ink hover:underline">
          View paper account <IconArrowRight size={13} />
        </Link>
      </div>
    </div>
  )
}

function DetailSkeleton() {
  return (
    <div aria-busy="true" aria-label="Loading portfolio">
      <Skeleton className="h-4 w-48" />
      <Skeleton className="mt-4 h-11 w-2/3 max-w-lg" />
      <Skeleton className="mt-4 h-4 w-full max-w-2xl" />
      <Skeleton className="mt-2 h-4 w-1/2 max-w-md" />
      <Skeleton className="mt-5 h-11 w-80 max-w-full rounded-2xl" />
      <div className="mt-8 grid items-start gap-6 lg:grid-cols-[minmax(0,1fr)_22rem]">
        <div className="flex flex-col gap-6">
          <Skeleton className="h-80 rounded-[14px]" />
          <Skeleton className="h-[440px] rounded-[14px]" />
        </div>
        <Skeleton className="h-[460px] rounded-[14px]" />
      </div>
    </div>
  )
}

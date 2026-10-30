// Inline cards the agent attaches to an answer (contract "Agent chat": trade_proposal, price_outlook, sources, forecast;
// the tap-to-run forecast_draft and build_action live in ActionCards). followups renders as chips under the latest
// answer (Conversation), not here.
import { useId, useState, type FormEvent, type ReactNode } from 'react'
import { Link } from 'react-router'
import { useToast } from '../../components/toast'
import { AssetIcon } from '../../components/ui/AssetIcon'
import { Badge, type BadgeTone } from '../../components/ui/Badge'
import { Button, buttonClass } from '../../components/ui/Button'
import { Card } from '../../components/ui/Card'
import { IconAlert, IconArrowRight, IconCheck, IconExternal } from '../../components/ui/icons'
import { Input, Label } from '../../components/ui/Input'
import { PriceChange } from '../../components/ui/PriceChange'
import { ProbabilityBars } from '../../components/ui/ProbabilityBars'
import { ApiError, api } from '../../lib/api'
import { useAssets } from '../../lib/assets'
import { cx } from '../../lib/cx'
import { fmtDate, fmtLevelPrice, fmtPct, fmtPrice, fmtProb, fmtQty, fmtUsd, timeAgo } from '../../lib/format'
import { usePortfolio } from '../../lib/portfolio'
import type { Asset, Card as AgentCardType, OutlookDriftCap, OutlookHistory, OutlookHorizon, OutlookLongHorizon, OutlookView, OutlookViewMultiple, PriceOutlook, SourceRef, Trade, TradeProposal } from '../../lib/types'
import { feedPriceCaption } from '../strategies/quoteFeeds'
import { viewReading } from '../forecasts/forecast'
import { monogram } from '../news/news'
import { BuildActionCard, DeskReading, ForecastDraftCard } from './ActionCards'
import { ProtectionCard } from './ProtectionCard'
import { AlertCard } from './AlertCard'
import { citationSources, citedNumbers } from './citations'
import { clip, parseAmount } from './turns'
import { MemoryCard } from './MemoryCard'
import {
  BaselineDetails,
  distance,
  fmtMove,
  fmtX,
  horizonYears,
  moveFrom,
  MultiplesTable,
  plural,
  QuantileTiles,
  ScenarioTable,
  TimeToTargetTable,
  volBasisText,
} from './OutlookParts'

/**
 * `answer` is the turn's text: the sources card lists the sources it cites first. `forecastId` is the report the turn is
 * about (its forecast card): the outlook then links to it instead of offering a new forecast. `sources` are the turn's
 * numbered sources, which the outlook's scenario inputs cite as [n].
 */
export function AgentCard({
  card,
  answer,
  forecastId,
  durable = false,
  sources,
}: {
  card: AgentCardType
  answer?: string
  forecastId?: string
  durable?: boolean
  sources?: Map<number, SourceRef>
}) {
  switch (card.type) {
    case 'memory':
      return <MemoryCard data={card.data} durable={durable} />
    case 'trade_proposal':
      return <TradeProposalCard data={card.data} />
    case 'price_outlook':
      return <PriceOutlookCard data={card.data} forecastId={forecastId} sources={sources} />
    case 'sources':
      return <SourcesCard card={card} answer={answer} />
    case 'forecast':
      return <ForecastRefCard data={card.data} />
    case 'followups':
      return null
    case 'forecast_draft':
      return <ForecastDraftCard data={card.data} />
    case 'build_action':
      return <BuildActionCard data={card.data} />
    case 'protection':
      return <ProtectionCard data={card.data} />
    case 'price_alert':
      return <AlertCard data={card.data} />
  }
}

/**
 * A quote feed's price says how fresh it is from its own source in session (real-time, a minute or two behind, or
 * Yahoo's delayed trade) and is the last close outside it; crypto and perps quote live.
 */
const priceLabel = (q: Asset['quoteFeed'] | undefined) => (!q ? 'Live price' : feedPriceCaption(q))

/** Reward per unit of risk from the proposed price, e.g. 1.85; null without both levels or with no risk. */
function rewardRisk(price: number, stop: number | null, take: number | null) {
  if (stop == null || take == null) return null
  const r = Math.abs(take - price) / Math.abs(price - stop)
  return Number.isFinite(r) && r > 0 ? r : null
}

function Cell({ label, children, className }: { label: string; children: ReactNode; className?: string }) {
  return (
    <div className={cx('min-w-0 bg-surface px-4 py-3', className)}>
      <dt className="label">{label}</dt>
      <dd className="mt-1 flex flex-wrap items-baseline gap-x-1.5 font-mono text-[13.5px] text-text tabular-nums">{children}</dd>
    </div>
  )
}

/**
 * "Tell the agent, tap Confirm": the agent never executes; Confirm places a paper market order carrying the card's
 * proposalId, so the server fills it once. A stored chat says whether it was filled (status, trade).
 */
function TradeProposalCard({ data }: { data: TradeProposal }) {
  const { bySymbol } = useAssets()
  const { setPortfolio } = usePortfolio()
  const { toast } = useToast()
  const amountId = useId()

  const symbol = String(data.symbol ?? '').toUpperCase()
  const asset = bySymbol[symbol]
  const live = asset?.price ?? data.price
  const buy = data.side !== 'sell'
  const stop = data.stopLoss ?? null
  const take = data.takeProfit ?? null

  const [amount, setAmount] = useState(() => (Number.isFinite(data.notional) ? String(Math.round(data.notional * 100) / 100) : ''))
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const filled = data.status === 'filled'
  const [fill, setFill] = useState<Trade | null>(filled ? (data.trade ?? null) : null)
  const [already, setAlready] = useState(filled && !data.trade) // filled elsewhere: no receipt to show
  const [dismissed, setDismissed] = useState(false)

  const notional = parseAmount(amount)
  const valid = notional >= 10 // contract minimum
  const rr = rewardRisk(data.price, stop, take)

  async function confirm(e: FormEvent) {
    e.preventDefault()
    if (!valid || pending) return
    setPending(true)
    setError(null)
    try {
      const res = await api.placeOrder({
        symbol,
        side: buy ? 'buy' : 'sell',
        notional,
        stopLoss: stop ?? undefined,
        takeProfit: take ?? undefined,
        source: 'agent',
        proposalId: data.proposalId,
        note: data.forecast ? clip(`Forecast: ${data.forecast.question}`, 120) : undefined,
      })
      setFill(res.trade)
      setPortfolio(res.portfolio) // the order response already carries the updated portfolio
      toast(`Filled ${fmtQty(res.trade.qty)} ${symbol} @ ${fmtPrice(res.trade.price)} (paper)`, 'success')
    } catch (err) {
      if (err instanceof ApiError && err.status === 409 && err.message === 'This proposal was already filled.') setAlready(true)
      setError(err instanceof ApiError ? err.message : 'The order could not be placed. Please try again.') // shown inline, next to the button
    } finally {
      setPending(false)
    }
  }

  if (dismissed) {
    return (
      <div className="flex items-center gap-3 rounded-xl border border-dashed border-border px-4 py-2.5 text-[13px] text-muted">
        <span className="min-w-0 truncate">
          Proposal dismissed · {buy ? 'Buy' : 'Sell'} {fmtUsd(notional, { decimals: 0 })} {symbol}
        </span>
        <button type="button" onClick={() => setDismissed(false)} className="ml-auto font-medium text-accent-ink hover:underline">
          Undo
        </button>
      </div>
    )
  }

  return (
    <Card className="overflow-hidden">
      <div className="flex items-start justify-between gap-3 px-4 pt-4">
        <div className="flex min-w-0 items-center gap-3">
          <AssetIcon symbol={symbol} size={38} />
          <div className="min-w-0">
            <div className="label">Trade proposal · paper</div>
            <div className="flex min-w-0 items-baseline gap-2">
              <span className="font-display text-[19px] leading-snug font-medium text-text">
                {buy ? 'Buy' : 'Sell'} {symbol}
              </span>
              {asset && <span className="truncate text-[13px] text-muted">{asset.name}</span>}
            </div>
          </div>
        </div>
        <Badge tone={buy ? 'up' : 'down'}>
          <span aria-hidden="true">{buy ? '▲' : '▼'}</span>
          {buy ? 'Buy' : 'Sell'}
        </Badge>
      </div>

      <dl className="mt-4 grid grid-cols-2 gap-px border-y border-border bg-border sm:grid-cols-4">
        <Cell label={priceLabel(asset?.quoteFeed)}>
          {fmtPrice(live)}
          {asset && <PriceChange value={asset.change24hPct} decimals={1} className="text-[11px]" />}
        </Cell>
        <Cell label="Proposed at">{fmtPrice(data.price)}</Cell>
        <Cell label="Stop loss">
          {stop != null ? (
            <>
              {fmtPrice(stop)} <span className="text-[11px] text-muted">{distance(stop, live)}</span>
            </>
          ) : (
            <span className="text-muted">None</span>
          )}
        </Cell>
        <Cell label="Take profit">
          {take != null ? (
            <>
              {fmtPrice(take)} <span className="text-[11px] text-muted">{distance(take, live)}</span>
            </>
          ) : (
            <span className="text-muted">None</span>
          )}
        </Cell>
      </dl>

      {rr != null && (
        <p className="px-4 pt-3 font-mono text-[11.5px] text-muted tabular-nums" title="Reward to risk from the proposed price">
          R:R 1 : {rr.toFixed(2)}
        </p>
      )}
      {data.rationale && <p className="px-4 pt-3.5 text-[14px] leading-relaxed text-pretty text-text">{data.rationale}</p>}
      {data.forecast && <DeskLine forecast={data.forecast} />}

      {already ? (
        <div role="status" className="m-4 flex flex-wrap items-center gap-x-2 gap-y-1 rounded-xl bg-surface-2 px-4 py-3 text-[13.5px]">
          <IconCheck size={15} strokeWidth={2.4} className="shrink-0 text-up" />
          <span className="font-medium text-text">Already filled</span>
          {error && <span className="w-full text-muted">{error}</span>}
          <span aria-hidden="true" className="text-muted">
            ·
          </span>
          <Link to="/account" className="inline-flex items-center gap-1 font-medium text-accent-ink hover:underline">
            View account <IconArrowRight size={14} />
          </Link>
        </div>
      ) : fill ? (
        <div role="status" className="m-4 flex flex-wrap items-center gap-x-3 gap-y-2 rounded-xl bg-up/8 px-4 py-3">
          <span className="grid size-7 shrink-0 place-items-center rounded-full bg-up/15 text-up">
            <IconCheck size={15} strokeWidth={2.4} />
          </span>
          <div className="min-w-0 flex-1 basis-56">
            <p className="text-[14px] font-medium text-text tabular-nums">
              Filled {fmtQty(fill.qty)} {fill.symbol} @ {fmtPrice(fill.price)}
            </p>
            <p className="mt-0.5 font-mono text-[11px] text-muted tabular-nums">
              vs proposed {fmtPrice(data.price)} ({fmtPct((fill.price / data.price - 1) * 100, { sign: true })}) · {fmtUsd(fill.notional)} notional ·{' '}
              {fmtUsd(fill.fee)} fee · simulated fill
            </p>
          </div>
          <Link to="/account" className="inline-flex items-center gap-1 text-[13px] font-medium text-accent-ink hover:underline">
            View account <IconArrowRight size={14} />
          </Link>
        </div>
      ) : (
        <form onSubmit={confirm} className="flex flex-wrap items-start gap-x-3 gap-y-3 px-4 pt-4 pb-4">
          <div className="w-full sm:w-44">
            <Label htmlFor={amountId}>Amount (USD)</Label>
            <div className="relative">
              <span aria-hidden="true" className="pointer-events-none absolute top-1/2 left-3 -translate-y-1/2 font-mono text-sm text-muted">
                $
              </span>
              <Input
                id={amountId}
                inputMode="decimal"
                autoComplete="off"
                value={amount}
                onChange={(e) => setAmount(e.target.value)}
                disabled={pending}
                aria-invalid={!valid || undefined}
                aria-describedby={`${amountId}-hint`}
                className="pl-6 font-mono tabular-nums"
              />
            </div>
            <p id={`${amountId}-hint`} className={cx('mt-1.5 h-4 truncate font-mono text-[11px] tabular-nums', valid ? 'text-muted' : 'text-down')}>
              {valid ? `≈ ${fmtQty(notional / live)} ${symbol}` : 'Minimum $10'}
            </p>
          </div>
          <div className="flex flex-1 flex-wrap items-center justify-end gap-2 sm:pt-[22px]">
            <Button type="button" variant="ghost" onClick={() => setDismissed(true)} disabled={pending}>
              Dismiss
            </Button>
            <Button type="submit" variant="primary" loading={pending} disabled={!valid} className="flex-1 sm:flex-none">
              Confirm {buy ? 'buy' : 'sell'} (paper)
            </Button>
          </div>
          {error && (
            <p role="alert" className="flex w-full items-start gap-2 rounded-lg bg-down/8 px-3 py-2 text-[13px] text-down">
              <IconAlert size={15} className="mt-px shrink-0" />
              {error}
            </p>
          )}
        </form>
      )}
    </Card>
  )
}

const STANCE: Record<'supports' | 'opposes' | 'split', { tone: BadgeTone; text: string }> = {
  supports: { tone: 'up', text: 'Desk agrees' },
  opposes: { tone: 'down', text: 'Desk leans against this trade' },
  split: { tone: 'neutral', text: 'Desk is split' },
}

/**
 * How the forecast desk reads the card's side. A stance (a binary price rule on the card's symbol) names the side the
 * desk leans to (a 46% Yes reads "No 54%"); split shows the headline as is; cards without a stance keep the plain
 * "Informed by" link, which states a numeric report by its view when the view leads ("median $611 · 62% ≥ today").
 */
function DeskLine({ forecast: f }: { forecast: NonNullable<TradeProposal['forecast']> }) {
  const to = `/forecasts/${encodeURIComponent(f.id)}`
  const stance = f.stance ? STANCE[f.stance] : undefined
  if (!stance)
    return (
      <p className="px-4 pt-2 text-[12.5px] text-muted">
        <Link to={to} title={f.question} className="font-medium text-accent-ink decoration-accent/50 underline-offset-2 hover:underline">
          Informed by forecast desk · {viewReading(f)?.text ?? `${f.outcome} ${fmtProb(f.p)}`}
        </Link>
      </p>
    )
  const leansNo = f.stance !== 'split' && f.outcome === 'Yes' && f.p < 0.5
  return (
    <p className="px-4 pt-2.5">
      <Link to={to} title={f.question} className="inline-flex max-w-full rounded-md hover:opacity-85">
        <Badge tone={stance.tone} className="max-w-full truncate">
          {stance.text} · {leansNo ? 'No' : f.outcome} {fmtProb(leansNo ? 1 - f.p : f.p)}
        </Badge>
      </Link>
    </p>
  )
}

/**
 * Price distribution at the horizon (computed in Go, not by the model). A card with `view` leads with the directional
 * view (the agent's scenarios or the historical drift, halved) and folds the zero-drift volatility model away as a
 * labelled baseline; older cards keep the volatility-only layout, its percentiles no longer called Bear/Bull.
 */
function PriceOutlookCard({ data, forecastId, sources }: { data: PriceOutlook; forecastId?: string; sources?: Map<number, SourceRef> }) {
  const symbol = String(data.symbol ?? '').toUpperCase()
  if (data.view) return <OutlookViewCard data={data} view={data.view} symbol={symbol} forecastId={forecastId} sources={sources} />
  const days = data.horizonDays
  const long = days > 365
  const years = horizonYears(days)
  const buckets = Array.isArray(data.buckets) ? data.buckets : []
  const basis = long ? volBasisText(data.volBasis) : null

  return (
    <Card className="overflow-hidden">
      <OutlookHeader
        symbol={symbol}
        spot={data.spot}
        label={long ? `${years}-year outlook` : `Price outlook · ${plural(days, 'day')}`}
        title={`${symbol} in ${long ? plural(years, 'year') : plural(days, 'day')}`}
      />

      {buckets.length > 0 && <ProbabilityBars className="px-4 pt-4" items={buckets.map((b) => ({ label: b.label, p: b.p }))} />}

      <QuantileTiles
        className="mx-4 mt-4"
        spot={data.spot}
        items={[
          { label: '5th percentile', v: data.p5 },
          { label: 'Median (zero drift)', v: data.p50 },
          { label: '95th percentile', v: data.p95 },
        ]}
      />

      {long && data.longHorizon && <LongHorizonOdds data={data.longHorizon} spot={data.spot} years={years} since={data.volBasis?.since} />}

      <OutlookFooter symbol={symbol} spot={data.spot} forecastId={forecastId} idea={`${symbol} price in ${long ? plural(years, 'year') : `${days} days`}`}>
        <p className="font-mono text-[11px] text-muted">
          {fmtPct(data.volAnnPct, { decimals: 0 })} annualized vol{basis && ` · ${basis}`} · lognormal, zero drift
        </p>
      </OutlookFooter>
    </Card>
  )
}

function OutlookHeader({ symbol, spot, label, title, listing }: { symbol: string; spot: number; label: string; title: string; listing?: ReactNode }) {
  return (
    <div className="flex items-center gap-3 px-4 pt-4">
      <AssetIcon symbol={symbol} size={38} />
      <div className="min-w-0">
        <div className="label">{label}</div>
        <div className="font-display text-[19px] leading-snug font-medium text-text">{title}</div>
      </div>
      <div className="ml-auto shrink-0 text-right">
        <div className="label">Spot</div>
        <div className="mt-0.5 font-mono text-[13.5px] text-text tabular-nums">{fmtPrice(spot)}</div>
        {listing}
      </div>
    </div>
  )
}

/** The footnote (left) and the buttons: the report this chat came from, or a full forecast of the same idea; Trade. */
function OutlookFooter({ symbol, spot, forecastId, idea, children }: { symbol: string; spot: number; forecastId?: string; idea: string; children?: ReactNode }) {
  return (
    <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2 px-4 pt-3 pb-4">
      {children ?? <span />}
      {symbol && (
        <div className="flex flex-wrap gap-2">
          {forecastId ? (
            <Link to={`/forecasts/${encodeURIComponent(forecastId)}`} className={buttonClass('secondary', 'sm')}>
              View report
            </Link>
          ) : (
            <Link to="/forecasts/new" state={{ idea: `${idea} (spot ${fmtPrice(spot)}): where will it trade?` }} className={buttonClass('secondary', 'sm')}>
              Run a full forecast
            </Link>
          )}
          <Link to={`/trade/${encodeURIComponent(symbol)}`} className={buttonClass('secondary', 'sm')}>
            Open in Trade <IconArrowRight size={14} />
          </Link>
        </div>
      )}
    </div>
  )
}

/** "−48% … −26%" for a bucket, "below −48%" / "above +209%" for the open tails. */
function bucketHint(lo: number | null, hi: number | null, spot: number) {
  if (lo == null && hi != null) return `below ${moveFrom(hi, spot)}`
  if (hi == null && lo != null) return `above ${moveFrom(lo, spot)}`
  if (lo != null && hi != null) return `${moveFrom(lo, spot)} … ${moveFrom(hi, spot)}`
  return undefined
}

/** "NYSE" from "Yahoo Finance daily closes (NYSE: TSM)"; "Listing" when the source names no exchange. */
const listingVenue = (source: string | undefined) => source?.match(/\(([A-Za-z][\w .]*):/)?.[1] ?? 'Listing'

const historyYear = (iso: string | null | undefined) => (iso && /^\d{4}/.test(iso) ? iso.slice(0, 4) : null)

/** "Volatility 38%/yr · CAGR since 1997 +14.6%/yr · max drawdown −83% (2000–2002, recovered 2005) · Yahoo …, 1997–2026". */
function historyLine(h: OutlookHistory) {
  const parts: string[] = []
  if (h.volAnnPct != null) parts.push(`Volatility ${fmtPct(h.volAnnPct, { decimals: 0 })}/yr`)
  if (h.cagrPct?.all != null) parts.push(`CAGR since ${historyYear(h.from)} ${fmtPct(h.cagrPct.all, { sign: true, decimals: 1 })}/yr`)
  if (h.maxDrawdownPct != null) {
    // Peak to trough when the card carries the trough (older view cards have only the peak), then the recovery.
    const peak = historyYear(h.maxDrawdownPeak)
    const trough = historyYear(h.maxDrawdownTrough)
    const span = peak && trough ? (peak === trough ? peak : `${peak}–${trough}`) : peak && `peak ${peak}`
    const when = [span, h.recovered ? `recovered ${historyYear(h.recovered)}` : span && 'not recovered']
    parts.push(`max drawdown ${fmtPct(h.maxDrawdownPct, { decimals: 0 })}${when.some(Boolean) ? ` (${when.filter(Boolean).join(', ')})` : ''}`)
  }
  parts.push(`${h.source}, ${historyYear(h.from)}–${historyYear(h.to)}`)
  return parts.join(' · ')
}

function OutlookViewCard({ data, view, symbol, forecastId, sources }: { data: PriceOutlook; view: OutlookView; symbol: string; forecastId?: string; sources?: Map<number, SourceRef> }) {
  const ttt = data.timeToTarget
  // A "by when" question with no horizon has no at-horizon panel: only the odds by year.
  const days = data.horizon ? data.horizon.days : data.horizonDays
  const atHorizon = days != null
  const years = atHorizon ? horizonYears(days) : 0
  const long = atHorizon && days > 365
  const span = !atHorizon ? '' : long ? plural(years, 'year') : plural(days, 'day')
  const label = !atHorizon ? `By when ${ttt ? fmtX(ttt.multiple) : ''}`.trim() : long ? `${years}-year view` : `Price view · ${plural(days, 'day')}`
  const title = !atHorizon ? (ttt ? `${symbol} to ${fmtX(ttt.multiple)}` : symbol) : `${symbol} in ${span}`
  const buckets = Array.isArray(view.buckets) ? view.buckets : []
  // The view's multiples are at-horizon odds: under a year they read "<1%" across the board (the older card showed
  // them only past a year too), so they show past a year or when the question names a multiple.
  const multiples = atHorizon && (long || ttt) && Array.isArray(view.multiples) ? view.multiples : []
  const scenarios = Array.isArray(data.scenarios) ? data.scenarios : []
  const warnings = readerNotes(data.warnings)
  // A drift-only view capped so its backward-looking return does not overpromise (the label says so; this says how).
  const cap = view.kind === 'historical_drift' && view.driftCap?.applied ? view.driftCap : undefined
  const listing =
    view.listingSpot != null ? (
      <div className="font-mono text-[11px] text-muted tabular-nums">
        {listingVenue(data.history?.source)} {fmtLevelPrice(view.listingSpot)}
      </div>
    ) : undefined
  const idea = !atHorizon && ttt ? `Can ${symbol} reach ${fmtX(ttt.multiple)} (${fmtLevelPrice(ttt.target)}), and by when?` : `${symbol} price in ${long ? span : `${days} days`}`

  return (
    <Card className="overflow-hidden">
      <OutlookHeader symbol={symbol} spot={data.spot} label={label} title={title} listing={listing} />
      {data.horizon?.reason && <HorizonLine horizon={data.horizon} />}

      {atHorizon && (
        <section aria-label="Our view" className="mx-4 mt-4 rounded-xl border border-border bg-surface-2/40 px-3.5 pt-3 pb-3.5">
          <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
            {view.kind !== 'scenarios' && <Badge tone="outline">{view.kind === 'none' ? 'No view' : 'Assumption'}</Badge>}
            <span className="label">{view.label}</span>
          </div>
          <p className="mt-1.5 flex flex-wrap items-baseline gap-x-2.5 gap-y-0.5">
            <span className="font-display text-[34px] leading-none font-medium text-text tabular-nums">Median {fmtLevelPrice(view.median)}</span>
            <span className={cx('font-mono text-[13px] tabular-nums', view.changePct > 0 ? 'text-up' : view.changePct < 0 ? 'text-down' : 'text-muted')}>
              {fmtMove(view.changePct)}
              {view.annualPct != null && ` · ${fmtPct(view.annualPct, { sign: true, decimals: 1 })}/yr`}
            </span>
          </p>
          <dl className="mt-3 grid grid-cols-3 gap-2 max-[360px]:grid-cols-1">
            <ViewTile label="Above today" value={fmtProb(view.pAbove)} />
            <ViewTile label="5th percentile" value={fmtLevelPrice(view.p5)} sub={moveFrom(view.p5, data.spot)} />
            <ViewTile label="95th percentile" value={fmtLevelPrice(view.p95)} sub={moveFrom(view.p95, data.spot)} />
          </dl>
          <p className="mt-2 font-mono text-[11px] text-muted tabular-nums">
            Middle half {fmtLevelPrice(view.p25)} – {fmtLevelPrice(view.p75)}
            {view.muAnnPct != null && !cap && ` · drift ${fmtPct(view.muAnnPct, { sign: true, decimals: 1 })}/yr`}
            {cap && ` · drift capped at ${fmtPct(cap.capPct, { sign: true, decimals: 1 })}/yr`}
            {view.volAssumed && ` · assumed ${fmtPct(view.volAnnPct ?? 30, { decimals: 0 })}/yr volatility (no measured history)`}
          </p>
          {cap && <p className="mt-1 font-mono text-[11px] leading-snug text-pretty text-muted tabular-nums">{driftCapText(cap, symbol)}</p>}
        </section>
      )}

      {buckets.length > 0 && (
        <section aria-label="Price ranges" className="mx-4 mt-4">
          <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-0.5">
            <div className="label">Where it ends{span && ` in ${span}`}</div>
            <span className="font-mono text-[10.5px] text-muted">
              <span aria-hidden="true" className="mr-1 inline-block h-2.5 w-0.5 rounded-full bg-text/75 align-[-1px]" />
              volatility-only baseline
            </span>
          </div>
          <ProbabilityBars
            className="pt-2.5"
            baselineLabel="volatility-only baseline"
            relative
            items={buckets.map((b) => ({ label: b.label, p: b.p, hint: bucketHint(b.lo, b.hi, data.spot), baseline: b.pBaseline }))}
          />
        </section>
      )}

      {multiples.length > 0 && <ViewMultiples rows={multiples} spot={data.spot} span={span} />}
      {ttt && <TimeToTargetTable data={ttt} symbol={symbol} view={view} lead={!atHorizon} />}
      {scenarios.length > 0 && <ScenarioTable scenarios={scenarios} spot={data.spot} sources={sources} />}
      {data.history && <p className="mx-4 mt-4 font-mono text-[11px] leading-relaxed text-pretty text-muted tabular-nums">{historyLine(data.history)}</p>}
      {atHorizon && <BaselineDetails data={data} span={span} />}

      {warnings.length > 0 && (
        <ul aria-label="Notes" className="mx-4 mt-3 list-disc space-y-0.5 pl-4 text-[12px] leading-snug text-pretty text-muted">
          {warnings.map((w, i) => (
            <li key={i}>{w}</li>
          ))}
        </ul>
      )}

      <OutlookFooter symbol={symbol} spot={data.spot} forecastId={forecastId} idea={idea} />
    </Card>
  )
}

// Notes the API once wrote for the model, which it now sends only in the tool result: they name a tool
// parameter, ask for scenarios, or flag a cited number by its parameter ("pe 25 cited [1] not found in [1]: labelled
// assumption", and the same for a quote or an unknown source id). A card stored before then still carries them.
const MODEL_HINT = /\bhorizon_(?:days|reason)\b|\bpass scenarios\b|\bcited\b.*: labelled assumption$/

/**
 * The card's notes for the reader: a stored card's notes to the model are dropped, and its thin-downside note keeps the
 * numbers without the advice to the model ("consider a tail scenario").
 */
function readerNotes(warnings: unknown): string[] {
  return (Array.isArray(warnings) ? warnings : [])
    .filter((w): w is string => typeof w === 'string' && !!w.trim() && !MODEL_HINT.test(w))
    .map((w) => w.replace(/^Your view's downside is thin:/, "The view's downside is thin:").replace(/:\s*consider a tail scenario\.?$/, ''))
}

/** The benchmarks a drift cap can rest on (the API's market.Benchmarks keys). */
const BENCHMARK_NAME: Record<string, string> = {
  SPX: 'S&P 500',
  TWII: 'Taiwan Weighted',
  N225: 'Nikkei 225',
  KOSPI: 'KOSPI',
  HSI: 'Hang Seng',
  STI: 'Straits Times',
  JKSE: 'Jakarta Composite',
}

/**
 * How the drift was capped: "S&P 500 +7.7%/yr since 1966 + 2 pts; NVDA's own history, halved: +15.7%/yr", or the
 * 10%/yr ceiling alone when no benchmark history was available.
 */
function driftCapText(cap: OutlookDriftCap, symbol: string) {
  const own = `${symbol}'s own history, halved: ${fmtPct(cap.uncappedPct, { sign: true, decimals: 1 })}/yr`
  if (!cap.benchmark || cap.benchmarkCagrPct == null) return `The 10%/yr ceiling; ${own}`
  const since = historyYear(cap.since)
  const base = `${BENCHMARK_NAME[cap.benchmark] ?? cap.benchmark} ${fmtPct(cap.benchmarkCagrPct, { sign: true, decimals: 1 })}/yr${since ? ` since ${since}` : ''}`
  // The cap is min(benchmark + 2, 10): name whichever bound it is.
  return `${cap.capPct < cap.benchmarkCagrPct + 2 - 1e-9 ? `The 10%/yr ceiling (${base} + 2 pts is above it)` : `${base} + 2 pts`}; ${own}`
}

// The API's quotesUserRe and noTimeframe (internal/chat/outlook_view.go), copied: a horizon_reason that refers to the
// user or the question, and one that says the question named no timeframe. Keep them in step with the API.
const QUOTES_USER = /\b(?:user|users|you|your|question|asked|asks|asking|named|names|said|says|wants|wanted|requested|mentioned|specified|stated|wrote)\b/i
const NO_TIMEFRAME = /\bno\s+(?:timeframe|time\s*frame|date|horizon|period)\b|\bnot\s+(?:given|named|stated|specified)\b|\bdid(?:n't| not)\b|\bwithout\s+a\s+(?:date|timeframe|horizon)\b/i

/** A reason that quotes the user's own timeframe ("The user asked by December"), not the agent's choice. */
const quotesUser = (reason: string) => QUOTES_USER.test(reason) && !NO_TIMEFRAME.test(reason)

/**
 * Where the horizon came from, under the header: the user's words as the API quotes them, or, when the question named
 * no timeframe, a "Chosen by the agent" tag before the agent's reason. The API's own line for a horizon given without a
 * reason ("Horizon chosen by the agent: 1,826 days (5 years)") already says so, so it goes untagged. A "user" horizon
 * (the reason quotes the user, in words the parser cannot read) is the user's, and so is a stored
 * "chosen" one whose reason quotes the user.
 */
function HorizonLine({ horizon }: { horizon: OutlookHorizon }) {
  const chosen = (horizon.basis === 'chosen' || horizon.basis === 'agent') && !/chosen by the agent/i.test(horizon.reason) && !quotesUser(horizon.reason)
  return (
    <p className="flex flex-wrap items-center gap-x-2 gap-y-1 px-4 pt-2 font-mono text-[11px] leading-snug text-pretty text-muted">
      {chosen && <Badge tone="outline">Chosen by the agent</Badge>}
      <span className="min-w-0">{horizon.reason}</span>
    </p>
  )
}

function ViewTile({ label, value, sub }: { label: string; value: string; sub?: string }) {
  return (
    <div className="min-w-0 rounded-lg bg-surface px-2.5 py-2">
      <dt className="label leading-tight">{label}</dt>
      <dd className="mt-0.5 truncate font-mono text-[14px] font-medium text-text tabular-nums">{value}</dd>
      {sub && <dd className="font-mono text-[11px] text-muted tabular-nums">{sub}</dd>}
    </div>
  )
}

/** The view's odds of each multiple: ends beyond it, ever trades there, and the volatility-only baseline's ending odds. */
function ViewMultiples({ rows, spot, span }: { rows: OutlookViewMultiple[]; spot: number; span: string }) {
  return (
    <section aria-label="Odds of a price multiple" className="mx-4 mt-5">
      <div className="label">Odds of a multiple</div>
      <table className="mt-1.5 w-full text-[13px]">
        <caption className="sr-only">{`Odds of each multiple of spot over ${span}, under the view and the volatility-only baseline`}</caption>
        <thead className="border-b border-border">
          <tr>
            <th scope="col" className={cx(oddsTh, 'pr-2 text-left')}>
              Multiple
            </th>
            <th scope="col" className={cx(oddsTh, 'px-2 text-right')}>
              Ends beyond
            </th>
            <th scope="col" className={cx(oddsTh, 'px-2 text-right')}>
              Ever trades there
            </th>
            <th scope="col" className={cx(oddsTh, 'pl-2 text-right')}>
              Baseline
            </th>
          </tr>
        </thead>
        <tbody className="divide-y divide-border">
          {rows.map((m) => (
            <tr key={m.x}>
              <th scope="row" className="py-2 pr-2 text-left font-normal">
                <span className="font-mono font-medium text-text">{fmtX(m.x)}</span>
                <span className="block font-mono text-[11px] text-muted tabular-nums">{fmtLevelPrice(m.x * spot)}</span>
              </th>
              <td className="px-2 py-2 text-right font-mono font-medium text-text tabular-nums">{fmtProb(m.pEnd)}</td>
              <td className="px-2 py-2 text-right font-mono text-text tabular-nums">{fmtProb(m.pTouch)}</td>
              <td className="py-2 pl-2 text-right font-mono text-muted tabular-nums">{fmtProb(m.baselinePEnd)}</td>
            </tr>
          ))}
        </tbody>
      </table>
      <p className="mt-1.5 text-[12px] leading-snug text-pretty text-muted">
        Ends beyond: at or above the level{span && ` in ${span}`} (at or below for multiples under 1×). Ever trades there: at any point before then.
        Baseline: ends beyond under the volatility-only model.
      </p>
    </section>
  )
}

const oddsTh = 'label py-1.5 font-medium align-bottom'

/**
 * The long-horizon odds of an older card, under the same zero-drift model (median = spot): a volatility model, not a
 * call on direction. The drift case is shown apart, as the labelled assumption it is.
 */
function LongHorizonOdds({ data, spot, years, since }: { data: OutlookLongHorizon; spot: number; years: number; since?: string }) {
  const multiples = Array.isArray(data.multiples) ? data.multiples : []
  const tenX = Array.isArray(data.tenX) ? data.tenX : []
  const drift = data.driftCase
  const span = plural(years, 'year')
  return (
    <>
      {multiples.length > 0 && (
        <section aria-label="Odds of a price multiple" className="mx-4 mt-4">
          <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-0.5">
            <div className="label">Odds of a multiple</div>
            <span className="font-mono text-[10.5px] text-muted">Volatility model · not a forecast of direction</span>
          </div>
          <MultiplesTable rows={multiples} spot={spot} caption={`Odds of each multiple of spot over ${span}, zero drift`} />
          <p className="mt-2 text-[12px] leading-snug text-pretty text-muted">
            At horizon: ends at or above the level (at or below for 0.5×) in {span}. Touched: trades there at any point before then.
          </p>
        </section>
      )}

      {tenX.length > 0 && (
        <section aria-label="How long might a 10× take?" className="mx-4 mt-4">
          <p className="text-[13.5px] font-medium text-text">How long might a 10× take?</p>
          <p className="mt-0.5 text-[12px] leading-snug text-muted">Chance of touching {fmtLevelPrice(10 * spot)} (10× spot) within</p>
          <dl className="mt-2 flex divide-x divide-border rounded-xl border border-border bg-surface-2/60">
            {tenX.map((t) => (
              <div key={t.years} className="min-w-0 flex-1 basis-0 px-2 py-2 text-center">
                <dt className="label truncate">{plural(t.years, 'yr')}</dt>
                <dd className="mt-0.5 font-mono text-[13.5px] font-medium text-text tabular-nums">{fmtProb(t.pTouch)}</dd>
              </div>
            ))}
          </dl>
        </section>
      )}

      {drift && (
        <section aria-label="Alternative assumption" className="mx-4 mt-4 rounded-xl border border-dashed border-border px-3 pt-3 pb-2.5">
          <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
            <Badge tone="outline">Assumption</Badge>
            <span className="text-[13.5px] font-medium text-text">{drift.label.charAt(0).toUpperCase() + drift.label.slice(1)}</span>
          </div>
          <p className="mt-1.5 font-mono text-[11px] text-muted tabular-nums">
            {fmtPct(drift.muAnnPct, { sign: true, decimals: 1 })}/yr drift · median {fmtLevelPrice(drift.p50)} ({distance(drift.p50, spot)}) in {span}
          </p>
          <MultiplesTable rows={Array.isArray(drift.multiples) ? drift.multiples : []} spot={spot} caption={`Odds of each multiple of spot over ${span}, ${drift.label}`} />
          <p className="mt-2 text-[12px] leading-snug text-pretty text-muted">
            The same model with the asset&apos;s historical drift{since && ` since ${fmtDate(since)}`}, halved and capped at ±25%/yr. An
            assumption that the past trend partly persists, not the baseline.
          </p>
        </section>
      )}
    </>
  )
}

type ForecastRef = Extract<AgentCardType, { type: 'forecast' }>['data']

/**
 * A forecast-desk report the agent read (or the chat was opened from): its headline and a link to the full report. A
 * numeric report whose view leads reads as the list row does: its median and chance at or above today, or its 80% range.
 */
function ForecastRefCard({ data }: { data: ForecastRef }) {
  const reading = viewReading(data)
  return (
    <Card className="overflow-hidden">
      <div className="flex items-start gap-4 px-4 pt-4">
        <div className="min-w-0 flex-1">
          <div className="label">Forecast desk</div>
          <p className="mt-1 line-clamp-2 font-display text-[17px] leading-snug font-medium text-pretty text-text">{data.question}</p>
        </div>
        {reading ? (
          <DeskReading reading={reading} className="max-w-[45%] shrink-0 text-right" />
        ) : (
          <div className="shrink-0 text-right font-mono text-[20px] leading-tight font-medium text-text tabular-nums">
            {data.headline?.outcome} {fmtProb(data.headline?.p)}
          </div>
        )}
      </div>
      <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2 px-4 pt-3 pb-4">
        <p className="font-mono text-[11px] text-muted">Resolves {fmtDate(data.resolutionDate)} · AI estimate</p>
        <Link to={`/forecasts/${encodeURIComponent(data.id)}`} className={buttonClass('secondary', 'sm')}>
          View report <IconArrowRight size={14} />
        </Link>
      </div>
    </Card>
  )
}

const FIRST_SOURCES = 5

/**
 * Where the sources came from, by the tool that numbered each; cards stored before kinds say just "Sources".
 * Prediction markets (get_market_odds) count as live data: "Live search + prediction markets", "Prediction markets".
 */
export function sourcesHeader(sources: SourceRef[]) {
  if (sources.some((s) => !s.kind)) return 'Sources'
  const wire = sources.some((s) => s.kind === 'wire')
  const market = sources.some((s) => s.kind === 'market')
  const search = sources.some((s) => s.kind !== 'wire' && s.kind !== 'market')
  if (!search && !market) return 'Market wire · stored headlines, refreshed every 2 h'
  if (!market) return wire ? 'Live search + market wire' : 'Live web search'
  const parts = [search && 'Live search', 'prediction markets', wire && 'market wire'].filter(Boolean).join(' + ')
  return parts[0].toUpperCase() + parts.slice(1)
}

/**
 * Numbered citation list matching the [n] marks in the answer: the sources it cites first, in number order, the rest
 * folded under "Also read". An answer citing none keeps the first five plus "Show all". Monograms stand in for
 * favicons (no third-party images).
 */
function SourcesCard({ card, answer = '' }: { card: AgentCardType; answer?: string }) {
  const [open, setOpen] = useState(false)
  const sources: SourceRef[] = [...citationSources([card]).values()]
  if (sources.length === 0) return null
  const nums = citedNumbers(answer)
  const cited = sources.filter((s) => nums.has(s.n))
  const rest = sources.filter((s) => !nums.has(s.n))

  if (cited.length === 0)
    return (
      <Card className="overflow-hidden">
        <SourcesHead label={`Sources · ${sources.length}`} sources={sources} />
        <SourceList label="Sources" items={open ? sources : sources.slice(0, FIRST_SOURCES)} />
        {sources.length > FIRST_SOURCES && (
          <FoldButton open={open} onClick={() => setOpen(!open)}>
            {open ? 'Show fewer' : `Show all ${sources.length} sources`}
          </FoldButton>
        )}
      </Card>
    )
  return (
    <Card className="overflow-hidden">
      <SourcesHead label={`Sources · ${sources.length} · ${cited.length} cited`} sources={sources} />
      <SourceList label="Sources" items={cited} />
      {rest.length > 0 && (
        <>
          <FoldButton open={open} onClick={() => setOpen(!open)}>
            Also read · {rest.length}
          </FoldButton>
          {open && <SourceList label="Also read" items={rest} className="border-t border-border" />}
        </>
      )}
    </Card>
  )
}

function SourcesHead({ label, sources }: { label: string; sources: SourceRef[] }) {
  return (
    <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-0.5 px-4 pt-3.5 pb-1.5">
      <span className="label">{label}</span>
      <span className="font-mono text-[10.5px] text-muted">{sourcesHeader(sources)}</span>
    </div>
  )
}

function FoldButton({ open, onClick, children }: { open: boolean; onClick: () => void; children: ReactNode }) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-expanded={open}
      className="w-full border-t border-border px-4 py-2 text-[12.5px] font-medium text-muted transition-colors hover:bg-surface-2 hover:text-text"
    >
      {children}
    </button>
  )
}

function SourceList({ label, items, className }: { label: string; items: SourceRef[]; className?: string }) {
  return (
    <ol aria-label={label} className={cx('divide-y divide-border', className)}>
      {items.map((s) => (
        <li key={s.n} className="flex items-start gap-3 px-4 py-2.5">
          <span className="w-4 shrink-0 pt-1 text-right font-mono text-[11px] text-muted tabular-nums">{s.n}</span>
          <span
            aria-hidden="true"
            className="grid size-7 shrink-0 place-items-center rounded-md border border-border bg-surface-2 font-mono text-[12px] font-semibold text-muted"
          >
            {monogram(s.source, s.url)}
          </span>
          <div className="min-w-0 flex-1">
            <a
              href={s.url}
              target="_blank"
              rel="noopener noreferrer"
              className="line-clamp-2 text-[13.5px] leading-snug font-medium text-text decoration-accent/50 underline-offset-2 hover:text-accent-ink hover:underline"
            >
              {s.title}
              <IconExternal size={12} className="ml-1 inline-block align-[-1px] text-muted" />
            </a>
            <p className="mt-0.5 truncate font-mono text-[11px] text-muted">
              {s.source}
              {s.publishedAt && ` · ${timeAgo(s.publishedAt)}`}
            </p>
          </div>
        </li>
      ))}
    </ol>
  )
}

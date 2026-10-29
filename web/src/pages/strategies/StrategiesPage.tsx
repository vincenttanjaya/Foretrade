import { useEffect, useRef, useState, type ReactNode } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router'
import { useToast } from '../../components/toast'
import { AssetIcon } from '../../components/ui/AssetIcon'
import { Badge } from '../../components/ui/Badge'
import { Button } from '../../components/ui/Button'
import { Card } from '../../components/ui/Card'
import { EmptyState } from '../../components/ui/EmptyState'
import { IconAlert, IconArrowRight, IconCheck, IconStrategy } from '../../components/ui/icons'
import { Select } from '../../components/ui/Input'
import { LineChart } from '../../components/ui/LineChart'
import { PriceChange } from '../../components/ui/PriceChange'
import { Sparkline } from '../../components/ui/Sparkline'
import { Skeleton } from '../../components/ui/Spinner'
import { Tabs } from '../../components/ui/Tabs'
import { api } from '../../lib/api'
import { useAuth } from '../../lib/auth'
import { cx } from '../../lib/cx'
import { timeAgo } from '../../lib/format'
import type { Interval, Point, StrategySummary } from '../../lib/types'
import { ExampleChips, PromptCard, PromptStatus, stageAt, useAlive, useElapsed, useLoad, type Stage } from './parts'
import { errorMessage } from '../../lib/errors'
import { LiveArena } from '../arena/LiveArena'
import { ArenaSetup } from '../arena/ArenaSetup'
import { AutopilotCard } from './autopilot'
import { CommunityBoard } from './Community'
import { ForwardLine, ForwardSort } from './forward'
import { StrategyForm } from './StrategyForm'
import { defaultStrategyForm } from './strategyFormModel'
import { compareForward, dailyCloses, fmtDD, fmtIndex, fmtNum, fmtWindow, INTERVALS, LABS, MARKET_GROUPS, PALETTE, rebaseSince, symbolGroup, SYMBOLS } from './strategy'

type Tab = 'studio' | 'top' | 'arena'

/** /strategies — Studio (prompt → strategy), Top strategies (leaderboard) and the AI Arena, synced to ?tab=. */
export function StrategiesPage() {
  const [params, setParams] = useSearchParams()
  const t = params.get('tab')
  const tab: Tab = t === 'top' || t === 'arena' ? t : 'studio'

  return (
    <div className="mx-auto max-w-6xl px-4 py-8 md:px-8 md:py-10">
      <RunningAutopilot />
      <Tabs
        value={tab}
        onChange={(t) => setParams(t === 'studio' ? {} : { tab: t }, { replace: true })}
        items={[
          { value: 'studio', label: 'Studio' },
          { value: 'top', label: 'Top strategies' },
          { value: 'arena', label: 'Arena' },
        ]}
        aria-label="Strategy views"
        className="mb-8"
      />
      {tab === 'studio' ? (
        <Studio onBrowseTop={() => setParams({ tab: 'top' }, { replace: true })} />
      ) : tab === 'top' ? (
        <Top />
      ) : (
        <>
          <Tabs aria-label="Arena views" className="mb-6" size="sm" value={params.get('arena') === 'historical' ? 'historical' : 'live'} items={[{ value: 'live', label: 'Live' }, { value: 'historical', label: 'Historical' }]} onChange={(view) => {
            const next = new URLSearchParams(params)
            if (view === 'historical') next.set('arena', 'historical'); else next.delete('arena')
            setParams(next)
          }} />
          {params.get('arena') === 'historical' ? <ArenaSetup /> : <LiveArena />}
        </>
      )}
    </div>
  )
}

/** While an autopilot runs, a strip above every tab: what it holds, its P&L, the next check and Stop. */
function RunningAutopilot() {
  const { data } = useLoad((signal) => api.autopilots(signal).then((r) => r.autopilots?.find((a) => a.status === 'running') ?? null))
  return data ? <AutopilotCard key={data.id} initial={data} compact className="mb-6" /> : null
}

// ── Studio ──────────────────────────────────────────────────────────────────

const EXAMPLES: { prompt: string; symbol: string; interval: Interval }[] = [
  { prompt: 'Buy SOL when EMA 20 crosses above EMA 50 on 4h, exit on the reverse cross, 4% stop', symbol: 'SOL', interval: '4h' },
  { prompt: 'Mean-revert ETH: long when RSI(14) < 30, exit when RSI > 55', symbol: 'ETH', interval: '1h' },
  { prompt: 'Donchian 20-bar breakout on BTC daily, long and short', symbol: 'BTC', interval: '1d' },
  { prompt: 'Bollinger squeeze breakout on LINK 1h with 2:1 take profit', symbol: 'LINK', interval: '1h' },
]

const STEPS = ['Describe', 'AI rules', 'Backtest', 'Deploy']

// What the server is doing, roughly by elapsed time; `step` lights up the matching chip.
const STAGES: (Stage & { step: number })[] = [
  { at: 0, step: 1, text: 'Turning your idea into entry and exit rules…' },
  { at: 6, step: 1, text: 'Checking indicators, periods and risk settings…' },
  { at: 12, step: 2, text: 'Backtesting on venue candles, fees included…' },
  { at: 20, step: 2, text: 'Writing a plain-English read of the results…' },
]

const MIN_PROMPT = 5
const MAX_PROMPT = 1000

function Studio({ onBrowseTop }: { onBrowseTop: () => void }) {
  const navigate = useNavigate()
  const { toast } = useToast()
  const { setCredits, refresh } = useAuth()
  const alive = useAlive()
  const box = useRef<HTMLTextAreaElement>(null)
  const [prompt, setPrompt] = useState('')
  const [symbol, setSymbol] = useState('BTC')
  const [interval, setIv] = useState<Interval>('4h')
  const [entryMode, setEntryMode] = useState<'text' | 'form'>('text')
  const [form, setForm] = useState(defaultStrategyForm)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const elapsed = useElapsed(busy)
  const step = busy ? stageAt(STAGES, elapsed).step : 0 // lights the step chip matching the status line

  async function generate(value = prompt) {
    const text = value.trim()
    if (text.length < MIN_PROMPT || text.length > MAX_PROMPT || busy) return
    setBusy(true)
    setError(null)
    try {
      const res = await api.generateStrategy({ prompt: text, symbol, interval })
      if (res.credits) setCredits(res.credits)
      if (alive.current) navigate(`/strategies/${res.strategy.id}`, { state: { strategy: res.strategy } })
      else toast(`"${res.strategy.name}" is ready under My strategies.`)
    } catch (err) {
      if (alive.current) setError(errorMessage(err))
      else toast(errorMessage(err, 'Strategy generation failed.'), 'error')
      refresh().catch(() => {}) // credits may have been refunded or exhausted
    } finally {
      setBusy(false)
    }
  }

  const marketControls = (
    <>
      <div className="w-[10.5rem]">
        <Select aria-label="Market" value={symbol} disabled={busy} onChange={(e) => setSymbol(e.target.value)}>
          {MARKET_GROUPS.map((g) => <optgroup key={g.key} label={g.label}>{SYMBOLS.filter(([s]) => symbolGroup(s) === g.key).map(([s, name]) => <option key={s} value={s}>{s} · {name}</option>)}</optgroup>)}
        </Select>
      </div>
      <div className="w-[7.25rem]">
        <Select aria-label="Interval" value={interval} disabled={busy} onChange={(e) => setIv(e.target.value as Interval)}>
          {INTERVALS.map((iv) => <option key={iv.value} value={iv.value}>{iv.label}</option>)}
        </Select>
      </div>
    </>
  )

  return (
    <>
      <header className="max-w-3xl">
        <div className="label">Strategy studio · rule-based backtests</div>
        <h1 className="mt-3 font-display text-[38px] leading-[1.04] font-medium tracking-[-0.02em] text-balance text-text md:text-[56px]">
          Turn ideas into strategies.
        </h1>
        <p className="mt-4 max-w-2xl text-[16px] leading-relaxed text-pretty text-muted">
          Describe a setup or build one with a form. The AI writes it as explicit rules, then the engine backtests them on real
          venue candles: Binance crypto or Hyperliquid stock and index perps. Every rule is readable and every number is computed. Deploy one
          to a paper autopilot and it trades by itself.
        </p>
      </header>

      <ol aria-label="How it works" className="mt-6 flex flex-wrap items-center gap-1.5">
        {STEPS.map((s, i) => (
          <li key={s} className="flex items-center gap-1.5">
            <span
              aria-current={i === step ? 'step' : undefined}
              className={cx(
                'inline-flex h-7 items-center gap-1.5 rounded-full border px-2.5 text-[12.5px] font-medium transition-colors duration-300',
                i < step && 'border-accent/40 bg-accent-soft text-accent-ink',
                i === step && 'border-ink bg-ink text-ink-fg',
                i > step && 'border-border bg-surface text-muted',
              )}
            >
              {i < step ? (
                <IconCheck size={12} strokeWidth={2.4} />
              ) : (
                <span className="font-mono text-[10.5px] opacity-70">{i + 1}</span>
              )}
              {s}
            </span>
            {i < STEPS.length - 1 && <IconArrowRight size={12} className="text-muted/70" />}
          </li>
        ))}
      </ol>

      <div className="mt-6">
        <fieldset disabled={busy} className="mb-4 min-w-0">
          <legend className="sr-only">Strategy entry method</legend>
          <Tabs
            value={entryMode}
            onChange={(mode) => { setEntryMode(mode); setError(null) }}
            aria-label="Strategy entry method"
            items={[{ value: 'text', label: 'Describe in text' }, { value: 'form', label: 'Build with a form' }]}
          />
        </fieldset>
        {entryMode === 'form' ? (
          <StrategyForm
            value={form}
            onChange={setForm}
            symbol={symbol}
            interval={interval}
            marketControls={marketControls}
            busy={busy}
            onGenerate={(text) => void generate(text)}
            onEdit={(text) => { setPrompt(text); setEntryMode('text'); setError(null) }}
          />
        ) : <PromptCard
          inputRef={box}
          label="Describe your strategy"
          value={prompt}
          onChange={setPrompt}
          onSubmit={() => void generate()}
          busy={busy}
          min={MIN_PROMPT}
          max={MAX_PROMPT}
          placeholder="e.g. Buy BTC when RSI(14) drops below 30 above the 200 EMA, 6% target, 3% stop"
          submitLabel="Generate & backtest"
          busyLabel="Generating…"
          controls={marketControls}
        />}
        <PromptStatus
          error={error}
          busy={busy}
          stages={STAGES}
          hint={<p className="text-muted">{entryMode === 'text' ? "Market and interval apply when your idea doesn't name them." : 'Review the generated rules and backtest before deploying a paper autopilot.'}</p>}
        />
      </div>

      {entryMode === 'text' && <section aria-labelledby="strategy-examples" className="mt-6">
        <h2 id="strategy-examples" className="label mb-3">
          Try an example
        </h2>
        <ExampleChips
          items={EXAMPLES.map((e) => e.prompt)}
          selected={prompt}
          disabled={busy}
          onPick={(text) => {
            const ex = EXAMPLES.find((e) => e.prompt === text)!
            setPrompt(ex.prompt)
            setSymbol(ex.symbol)
            setIv(ex.interval)
            setError(null)
            box.current?.focus()
          }}
        />
      </section>}

      <MyStrategies onBrowseTop={onBrowseTop} />
    </>
  )
}

function MyStrategies({ onBrowseTop }: { onBrowseTop: () => void }) {
  const { data, error, retry } = useLoad((signal) => api.strategies('mine', signal).then((r) => r.strategies ?? []))
  const [order, setOrder] = useState<'default' | 'forward'>('default')
  const listed = data && order === 'forward' ? [...data].sort((a, b) => compareForward(a.forward, b.forward)) : data

  return (
    <section aria-labelledby="my-strategies" className="mt-14">
      <div className="mb-4 flex flex-wrap items-baseline justify-between gap-4">
        <h2 id="my-strategies" className="font-display text-[26px] leading-tight font-medium text-text">
          My strategies
        </h2>
        {data && data.length > 0 && <div className="flex flex-wrap items-center gap-3">
          <span className="font-mono text-[11px] text-muted">{data.length} saved</span>
          <ForwardSort label="My strategy order" value={order} onChange={setOrder} />
        </div>}
      </div>

      {!data && error ? (
        <EmptyState
          icon={<IconAlert size={20} />}
          title="Couldn't load your strategies."
          description={errorMessage(error)}
          action={<Button variant="secondary" onClick={retry}>Try again</Button>}
        />
      ) : data?.length === 0 ? (
        <EmptyState
          icon={<IconStrategy size={20} />}
          title="No strategies yet."
          description="Describe an idea above or pick an example. Each strategy is saved here with its backtest."
          action={
            <Button variant="secondary" onClick={onBrowseTop}>
              See the top strategies <IconArrowRight size={15} />
            </Button>
          }
        />
      ) : (
        <ul aria-busy={!data} className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {listed ? listed.map((s) => <StrategyCard key={s.id} s={s} />) : [0, 1, 2].map((i) => <CardSkeleton key={i} />)}
        </ul>
      )}
    </section>
  )
}

function StrategyCard({ s }: { s: StrategySummary }) {
  const m = s.metrics
  return (
    <li>
      <Link
        to={`/strategies/${s.id}`}
        className="card group flex h-full flex-col p-5 transition-[border-color,box-shadow] hover:border-accent/50 hover:shadow-[0_8px_24px_-16px_rgb(15_27_45/0.35)]"
      >
        <div className="flex items-center gap-2">
          <AssetIcon symbol={s.symbol} size={20} />
          <span className="font-mono text-[11.5px] text-muted">
            {s.symbol} · {s.interval}
          </span>
          {s.community?.published && <Badge tone="accent">Published</Badge>}
          <span className="ml-auto font-mono text-[11px] text-muted">{timeAgo(s.createdAt)}</span>
        </div>
        <h3 className="mt-3 line-clamp-2 font-display text-[19px] leading-snug font-medium text-text group-hover:text-accent-ink">
          {s.name}
        </h3>
        <Sparkline values={s.sparkline ?? []} width={280} height={52} className="mt-4 h-auto w-full" />
        <dl className="mt-4 grid grid-cols-3 gap-3 border-t border-border pt-3.5">
          <Metric label="Return">
            <PriceChange value={m?.totalReturnPct} decimals={1} />
          </Metric>
          <Metric label="Sharpe">{fmtNum(m?.sharpe)}</Metric>
          <Metric label="Max DD">{fmtDD(m?.maxDrawdownPct)}</Metric>
        </dl>
        <ForwardLine f={s.forward} symbol={s.symbol} className="mt-4 border-t border-border pt-3" />
      </Link>
    </li>
  )
}

function Metric({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="label">{label}</dt>
      <dd className="mt-1 truncate font-mono text-[13.5px] text-text tabular-nums">{children}</dd>
    </div>
  )
}

function CardSkeleton() {
  return (
    <li className="card flex flex-col p-5">
      <Skeleton className="h-5 w-28" />
      <Skeleton className="mt-3 h-6 w-4/5" />
      <Skeleton className="mt-4 aspect-[280/52] w-full" />
      <div className="mt-4 grid grid-cols-3 gap-3 border-t border-border pt-3.5">
        {[0, 1, 2].map((i) => (
          <Skeleton key={i} className="h-9" />
        ))}
      </div>
    </li>
  )
}

// ── Top strategies ──────────────────────────────────────────────────────────

/** Top strategies: the built-in Foretrade Labs leaderboard or the Community board, synced to ?top=community. */
function Top() {
  const [params, setParams] = useSearchParams()
  const board = params.get('top') === 'community' ? 'community' : 'labs'
  return (
    <>
      <div className="-mt-3 mb-6">
        <Tabs
          size="sm"
          value={board}
          onChange={(b) => setParams(b === 'community' ? { tab: 'top', top: 'community' } : { tab: 'top' }, { replace: true })}
          items={[
            { value: 'labs', label: 'Foretrade Labs' },
            { value: 'community', label: 'Community' },
          ]}
          aria-label="Top strategies board"
        />
      </div>
      {board === 'community' ? <CommunityBoard /> : <TopStrategies />}
    </>
  )
}

// Keep the fixed Labs backtest curves; forward source metadata is reloaded on every visit.
let topCache: { curves: Record<string, Point[]> | null } = { curves: null }

type SortKey = 'rank' | 'name' | 'author' | 'market' | 'annualized' | 'total' | 'sharpe' | 'window' | 'maxDD' | 'trades'
type Row = StrategySummary & { rank: number }

const SORT_VALUE: Record<SortKey, (r: Row) => number | string> = {
  rank: (r) => r.rank,
  name: (r) => r.name.toLowerCase(),
  author: (r) => r.author.toLowerCase(),
  market: (r) => `${r.symbol} ${r.interval}`,
  annualized: (r) => r.metrics?.annualizedReturnPct ?? -Infinity,
  total: (r) => r.metrics?.totalReturnPct ?? -Infinity,
  sharpe: (r) => r.metrics?.sharpe ?? -Infinity,
  window: (r) => (r.metrics ? r.metrics.endT - r.metrics.startT : -Infinity),
  maxDD: (r) => r.metrics?.maxDrawdownPct ?? Infinity,
  trades: (r) => r.metrics?.trades ?? -Infinity,
}
// Clicking a new column sorts best-first: high returns, small drawdowns, A→Z.
const ASC_FIRST = new Set<SortKey>(['rank', 'name', 'author', 'market', 'maxDD'])

type ChartWindow = '3M' | '1Y' | 'All'
const WINDOW_DAYS: Record<ChartWindow, number> = { '3M': 91, '1Y': 365, All: Infinity }

function TopStrategies() {
  const navigate = useNavigate()
  const list = useLoad((signal) => api.strategies('top', signal).then((r) => r.strategies ?? []))
  const [curves, setCurves] = useState(topCache.curves)
  const [curvesFailed, setCurvesFailed] = useState(false)
  const [curvesNonce, setCurvesNonce] = useState(0)
  const [sort, setSort] = useState<{ key: SortKey; asc: boolean }>({ key: 'rank', asc: true })
  const [order, setOrder] = useState<'default' | 'forward'>('default')
  const [win, setWin] = useState<ChartWindow>('1Y')

  // Equity curves only come with the full strategy: fetch all of them in parallel once the summaries are in.
  useEffect(() => {
    const items = list.data
    if (!items?.length || (curves && items.every((s) => curves[s.id]))) return
    const ctrl = new AbortController()
    setCurvesFailed(false)
    Promise.allSettled(items.map((s) => api.strategy(s.id, ctrl.signal))).then((results) => {
      if (ctrl.signal.aborted) return
      const got: Record<string, Point[]> = {}
      results.forEach((r) => {
        if (r.status === 'fulfilled' && r.value.strategy?.equity?.length) got[r.value.strategy.id] = r.value.strategy.equity
      })
      if (!Object.keys(got).length) return setCurvesFailed(true)
      setCurves(got)
      topCache = { curves: got }
    })
    return () => ctrl.abort()
  }, [list.data, curvesNonce]) // `curves` is read only as a cache check

  const rows: Row[] = (list.data ?? []).map((s, i) => ({ ...s, rank: i + 1 }))
  const colorOf = (id: string) => PALETTE[Math.max(0, rows.findIndex((r) => r.id === id)) % PALETTE.length]
  const get = SORT_VALUE[sort.key]
  const sorted = [...rows].sort((a, b) => {
    if (order === 'forward') return compareForward(a.forward, b.forward)
    const x = get(a)
    const y = get(b)
    const c = typeof x === 'string' ? x.localeCompare(String(y)) : x - (y as number)
    return sort.asc ? c : -c
  })
  const series = rows
    .filter((r) => curves?.[r.id])
    .map((r) => ({ name: r.name, color: colorOf(r.id), points: rebaseSince(dailyCloses(curves![r.id]), WINDOW_DAYS[win]) }))
    .filter((s) => s.points.length > 1)

  function toggle(key: SortKey) {
    setOrder('default')
    setSort((s) => (s.key === key ? { key, asc: !s.asc } : { key, asc: ASC_FIRST.has(key) }))
  }

  const head = (key: SortKey, label: string, num = false) => (
    <th scope="col" aria-sort={order === 'default' && sort.key === key ? (sort.asc ? 'ascending' : 'descending') : 'none'} className={cx('px-3 py-2.5 first:pl-5 last:pr-5', num ? 'text-right' : 'text-left')}>
      <button
        type="button"
        onClick={() => toggle(key)}
        className={cx('label inline-flex items-center gap-1 rounded transition-colors hover:text-text', order === 'default' && sort.key === key && 'text-text', num && 'flex-row-reverse')}
      >
        {label}
        <span aria-hidden="true" className={cx('text-[9px]', (order === 'forward' || sort.key !== key) && 'opacity-0')}>
          {sort.asc ? '▲' : '▼'}
        </span>
      </button>
    </th>
  )

  return (
    <>
      <header className="max-w-3xl">
        <div className="label">Foretrade Labs · {rows.length || 8} built-in strategies</div>
        <h1 className="mt-3 font-display text-[38px] leading-[1.04] font-medium tracking-[-0.02em] text-balance text-text md:text-[56px]">
          Top strategies, measured honestly.
        </h1>
        <p className="mt-4 max-w-2xl text-[16px] leading-relaxed text-pretty text-muted">
          Rule-based strategies backtested on venue candles (Binance crypto / Hyperliquid perps) with fees on both sides, ranked by risk-adjusted return
          (Sharpe), not by the luckiest headline number. Open one to read its rules and tune it.
        </p>
      </header>

      <Card className="mt-8 p-5">
        <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
          <div>
            <div className="label">Backtest · cumulative return</div>
            <p className="mt-1 text-[13px] text-muted">
              {win === 'All' ? 'Daily closes; each curve starts at its first backtest day.' : `Daily closes, rebased to 0% at the start of the last ${win === '3M' ? '3 months' : 'year'}.`}
            </p>
          </div>
          <Tabs size="sm" value={win} onChange={setWin} items={['3M', '1Y', 'All'] as const} aria-label="Chart window" />
        </div>
        {series.length ? (
          <LineChart series={series} height={320} format={fmtIndex} endLabels />
        ) : curvesFailed || (!list.data && list.error) ? (
          <div className="grid h-[350px] place-items-center rounded-lg bg-surface-2/60 text-center">
            <div>
              <p className="text-sm text-muted">Equity curves are unavailable right now.</p>
              <Button variant="secondary" size="sm" className="mt-3" onClick={() => (list.data ? setCurvesNonce((n) => n + 1) : list.retry())}>
                Try again
              </Button>
            </div>
          </div>
        ) : (
          <div aria-busy="true" aria-label="Loading equity curves">
            <div className="mb-3 flex flex-wrap gap-x-4 gap-y-1.5">
              {[0, 1, 2, 3, 4].map((i) => (
                <Skeleton key={i} className="h-3.5 w-24" />
              ))}
            </div>
            <Skeleton className="h-[320px] w-full" />
          </div>
        )}
      </Card>

      <div className="mt-6 flex justify-end"><ForwardSort label="Labs strategy order" value={order} onChange={setOrder} /></div>
      <Card className="mt-3 overflow-hidden">
        {!list.data && list.error ? (
          <EmptyState
            className="m-5"
            icon={<IconAlert size={20} />}
            title="Couldn't load the leaderboard."
            description={errorMessage(list.error)}
            action={<Button variant="secondary" onClick={list.retry}>Try again</Button>}
          />
        ) : list.data?.length === 0 ? (
          <p className="px-5 py-12 text-center text-sm text-muted">No built-in strategies are available right now.</p>
        ) : (
          <div className="relative overflow-x-auto">
            <table className="w-full min-w-[920px] text-[13.5px]">
              <thead className="border-b border-border bg-surface-2/70">
                <tr>
                  {head('rank', '#')}
                  {head('name', 'Strategy')}
                  {head('author', 'Author')}
                  {head('market', 'Market')}
                  {head('annualized', 'Ann. return', true)}
                  {head('total', 'Total return', true)}
                  {head('sharpe', 'Sharpe', true)}
                  {head('window', 'Window', true)}
                  {head('maxDD', 'Max DD', true)}
                  {head('trades', 'Trades', true)}
                </tr>
              </thead>
              <tbody className="divide-y divide-border">
                {list.data
                  ? sorted.map((r) => (
                      <tr
                        key={r.id}
                        onClick={() => navigate(`/strategies/${r.id}`)}
                        className="group cursor-pointer transition-colors hover:bg-surface-2/60"
                      >
                        <td className="py-3 pr-3 pl-5 font-mono text-muted tabular-nums">
                          <span className="inline-flex items-center gap-2">
                            <span aria-hidden="true" className="h-2.5 w-1 rounded-full" style={{ background: colorOf(r.id) }} />
                            {r.rank}
                          </span>
                        </td>
                        <td className="max-w-[20rem] px-3 py-3">
                          <span className="flex min-w-0 items-center gap-2">
                            <Link
                              to={`/strategies/${r.id}`}
                              onClick={(e) => e.stopPropagation()}
                              className="truncate font-medium text-text group-hover:text-accent-ink"
                            >
                              {r.name}
                            </Link>
                            {r.shortSample && <Badge title="Under 180 days or fewer than 10 trades: the Sharpe says little">Short sample</Badge>}
                          </span>
                          <ForwardLine f={r.forward} symbol={r.symbol} className="mt-2" />
                        </td>
                        <td className="px-3 py-3 whitespace-nowrap">
                          {r.author === LABS ? <Badge tone="accent">{r.author}</Badge> : <span className="text-muted">{r.author}</span>}
                        </td>
                        <td className="px-3 py-3 whitespace-nowrap">
                          <span className="inline-flex items-center gap-2 font-mono text-[12.5px] text-text">
                            <AssetIcon symbol={r.symbol} size={18} />
                            {r.symbol} · {r.interval}
                          </span>
                        </td>
                        <td className="px-3 py-3 text-right">
                          <PriceChange value={r.metrics?.annualizedReturnPct} decimals={1} />
                        </td>
                        <td className="px-3 py-3 text-right">
                          <PriceChange value={r.metrics?.totalReturnPct} decimals={1} />
                        </td>
                        <td className="px-3 py-3 text-right font-mono font-medium text-text tabular-nums">{fmtNum(r.metrics?.sharpe)}</td>
                        <td className="px-3 py-3 text-right font-mono text-muted tabular-nums">{fmtWindow(r.metrics)}</td>
                        <td className="px-3 py-3 text-right font-mono text-text tabular-nums">{fmtDD(r.metrics?.maxDrawdownPct)}</td>
                        <td className="py-3 pr-5 pl-3 text-right font-mono text-muted tabular-nums">{r.metrics?.trades ?? '—'}</td>
                      </tr>
                    ))
                  : Array.from({ length: 8 }, (_, i) => (
                      <tr key={i}>
                        <td colSpan={10} className="px-5 py-3">
                          <Skeleton className="h-5 w-full" />
                        </td>
                      </tr>
                    ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
      <p className="mt-3 text-[12.5px] text-muted">Ranked by Sharpe among strategies with 180+ days and 10+ trades; short samples follow.</p>
      <p className="mt-1 text-[12.5px] text-muted">
        Past performance in a backtest is not a forecast. Signals act on bar closes and fill at the next open; one position
        at a time.
      </p>
    </>
  )
}

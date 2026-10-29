import { useRef, useState, type FormEvent, type ReactNode } from 'react'
import { Link, useLocation, useNavigate, useParams } from 'react-router'
import { useToast } from '../../components/toast'
import { AssetIcon } from '../../components/ui/AssetIcon'
import { Badge } from '../../components/ui/Badge'
import { Button, buttonClass } from '../../components/ui/Button'
import { Card, CardHeader } from '../../components/ui/Card'
import { Disclaimer } from '../../components/ui/Disclaimer'
import { IconTrade } from '../../components/ui/icons'
import { Input, Label } from '../../components/ui/Input'
import { LineChart } from '../../components/ui/LineChart'
import { Markdown } from '../../components/ui/Markdown'
import { PriceChange } from '../../components/ui/PriceChange'
import { Skeleton } from '../../components/ui/Spinner'
import { api } from '../../lib/api'
import { cx } from '../../lib/cx'
import { errorMessage } from '../../lib/errors'
import { fmtDate, fmtPct, fmtPrice } from '../../lib/format'
import type { Autopilot, BacktestResult, BacktestTrade, Metrics, OptimizeCell, Strategy, StrategySpec } from '../../lib/types'
import { AutopilotCard, DeployButton } from './autopilot'
import { byline, CopiedFrom, CopyButton, PublishControl, StarButton } from './Community'
import { BackLink, LoadError, useLoad } from './parts'
import { ForwardCard } from './forward'
import {
  describeRules,
  fmtDD,
  fmtIndex,
  fmtNum,
  isoAt,
  LABS,
  parseTune,
  returnHeadline,
  tuneChanged,
  tuneText,
  type TuneInput,
} from './strategy'

/** /strategies/:id — metrics, equity vs buy & hold, readable rules, a free re-tune and the trade log. */
export function StrategyDetailPage() {
  const { id = '' } = useParams()
  return <Detail key={id} id={id} />
}

function Detail({ id }: { id: string }) {
  // Coming straight from Generate, the new strategy rides along in router state: no refetch.
  const seeded = (useLocation().state as { strategy?: Strategy } | null)?.strategy
  const { data: s, error, retry } = useLoad(
    (signal) => api.strategy(id, signal).then((r) => r.strategy),
    seeded?.id === id ? seeded : null,
  )

  // Back to where it is listed: another user's on Community, a built-in on the Labs board, an own one in the studio.
  const back =
    s?.community && !s.community.mine ? '/strategies?tab=top&top=community' : s && !s.community && s.author === LABS ? '/strategies?tab=top' : '/strategies'

  return (
    <div className="mx-auto max-w-6xl px-4 py-8 md:px-8 md:py-10">
      <BackLink to={back}>Strategies</BackLink>
      {s ? (
        <Loaded s={s} />
      ) : error ? (
        <LoadError error={error} what="strategy" back={{ to: '/strategies', label: 'Back to strategies' }} onRetry={retry} />
      ) : (
        <DetailSkeleton />
      )}
    </div>
  )
}

type Run = BacktestResult & { spec: StrategySpec }

function Loaded({ s: initial }: { s: Strategy }) {
  const { toast } = useToast()
  const [s, setS] = useState(initial) // a publish or unpublish replaces it
  const publicationVersion = useRef(0) // completed mutations invalidate older saved-state reads
  const [run, setRun] = useState<Run | null>(null) // an unsaved re-tune, shown in place of the saved backtest
  const [optimizing, setOptimizing] = useState(false)
  const [refreshing, setRefreshing] = useState(false)
  const saved: Run = { spec: s.spec, metrics: s.metrics, equity: s.equity ?? [], benchmark: s.benchmark ?? [], trades: s.trades ?? [] }
  const shown = run ?? saved
  const c = s.community
  const own = c ? c.mine : s.author !== LABS
  const theirs = !!c && !c.mine // another user's published strategy: read-only, Star and Copy
  const symbol = s.spec?.symbol ?? ''
  const interval = s.spec?.interval ?? ''
  // The newest autopilot of this strategy (a failed load just shows none); a deploy or a poll replaces it.
  const pilots = useLoad((signal) => api.autopilots(signal).then((r) => r.autopilots ?? []))
  const [latest, setLatest] = useState<Autopilot | null>(null)
  const pilot = latest ?? pilots.data?.find((a) => a.strategyId === s.id) ?? null

  // The free re-run behind Tune and the optimizer's Apply; it throws for the caller to show.
  async function backtest(spec: StrategySpec) {
    const res = await api.backtest(spec)
    setRun({ spec, metrics: res.metrics, equity: res.equity ?? [], benchmark: res.benchmark ?? [], trades: res.trades ?? [] })
    toast(`Backtest re-run: ${fmtPct(res.metrics?.totalReturnPct, { sign: true, decimals: 1 })} total return.`, 'info')
  }

  async function refreshForward() {
    const version = publicationVersion.current
    setRefreshing(true)
    try {
      const res = await api.strategy(s.id)
      if (version === publicationVersion.current) setS(res.strategy)
    } catch (err) {
      toast(errorMessage(err, 'Could not refresh live forward.'), 'error')
    } finally {
      setRefreshing(false)
    }
  }

  return (
    <>
      <header className="flex flex-wrap items-start justify-between gap-x-8 gap-y-5">
        <div className="max-w-3xl min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            {theirs ? (
              <Badge tone="outline">{byline(c)}</Badge>
            ) : (
              <Badge tone={own ? 'outline' : 'accent'}>{own ? `By ${s.author || 'you'}` : s.author}</Badge>
            )}
            <span className="inline-flex items-center gap-1.5 font-mono text-[12px] text-text">
              <AssetIcon symbol={symbol} size={18} />
              {symbol} · {interval}
            </span>
            <span className="font-mono text-[11.5px] text-muted">Created {fmtDate(s.createdAt)}</span>
          </div>
          <h1 className="mt-3 font-display text-[34px] leading-[1.06] font-medium tracking-[-0.015em] text-balance text-text md:text-[46px]">
            {s.name}
          </h1>
          {c?.copiedFrom && <CopiedFrom from={c.copiedFrom} />}
          {s.description && <p className="mt-3 text-[15.5px] leading-relaxed text-pretty text-muted">{s.description}</p>}
          <p className="mt-3 font-mono text-[13px] text-text">{returnHeadline(s.metrics)}</p>
          {own && s.prompt && !c?.copiedFrom && (
            <blockquote className="mt-4 rounded-xl border-l-2 border-accent bg-surface-2/70 px-4 py-2.5 text-[14px] leading-relaxed text-text">
              <span className="label mr-2">You asked</span>“{s.prompt}”
            </blockquote>
          )}
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {theirs ? (
            <>
              <StarButton id={s.id} stars={c.stars} starred={c.starred} className="h-10" />
              <CopyButton id={s.id} />
            </>
          ) : (
            <DeployButton s={s} running={pilot?.status === 'running'} onDeployed={setLatest} />
          )}
          {!c && <CopyButton id={s.id} variant="secondary" />}
          <Link to={`/trade/${symbol}`} className={buttonClass('secondary')}>
            <IconTrade size={16} /> Trade {symbol} manually
          </Link>
          {own && <PublishControl s={s} onChange={(strategy) => {
            publicationVersion.current += 1
            setS(strategy)
          }} />}
          {own && <DeleteButton s={s} />}
        </div>
      </header>

      {pilot && <AutopilotCard key={pilot.id} initial={pilot} onChange={setLatest} className="mt-8" />}

      <MetricsRow m={shown.metrics} saved={run ? s.metrics : null} interval={interval} />

      <Card className="mt-6 p-5">
        <div className="mb-4 flex flex-wrap items-start justify-between gap-3">
          <div>
            <div className="label">Backtest · growth of 100</div>
            <h2 className="mt-1 font-display text-[19px] leading-snug font-medium text-text">{run ? 'Modified' : 'Saved'} backtest vs buy & hold</h2>
          </div>
          {run && <Badge tone="accent">Modified · not saved</Badge>}
        </div>
        {shown.equity.length > 1 ? (
          <LineChart
            height={300}
            format={fmtIndex}
            series={[
              { name: run ? 'Strategy (modified)' : 'Strategy', points: shown.equity },
              { name: `Buy & hold ${symbol}`, points: shown.benchmark },
            ]}
          />
        ) : (
          <p className="grid h-[300px] place-items-center rounded-lg bg-surface-2/60 text-sm text-muted">No equity curve for this backtest.</p>
        )}
      </Card>

      <ForwardCard s={s} modified={!!run} onRefresh={refreshForward} refreshing={refreshing} />

      <div className={cx('mt-6 grid gap-6', !theirs && 'lg:grid-cols-[minmax(0,1.35fr)_minmax(0,1fr)]')}>
        <Rules spec={shown.spec} modified={!!run} />
        {!theirs && <Tune
          // remounts on each new run, so an applied optimizer cell shows in the form
          key={run ? `${run.spec.stopLossPct}/${run.spec.takeProfitPct}/${run.spec.trailingStopPct}/${run.spec.positionPct}` : 'saved'}
          base={s.spec}
          current={shown.spec}
          onRun={backtest}
          onReset={run ? () => setRun(null) : undefined}
          optimizing={optimizing}
          onOptimize={() => setOptimizing(!optimizing)}
        />}
      </div>

      {optimizing && (
        <Optimize
          key={shown.spec.positionPct} // the grid is backtested at the current position size
          spec={shown.spec}
          onApply={(c) =>
            backtest({
              ...s.spec,
              positionPct: shown.spec.positionPct,
              trailingStopPct: shown.spec.trailingStopPct,
              stopLossPct: c.stopLossPct ?? undefined,
              takeProfitPct: c.takeProfitPct ?? undefined,
            })
          }
        />
      )}

      <TradesCard trades={shown.trades} daily={interval === '1d'} modified={!!run} />

      {s.explanation && (
        <Card className="mt-6">
          <CardHeader label="AI read of the backtest" title="What the numbers say" />
          <div className="px-5 pb-5">
            <Markdown>{s.explanation}</Markdown>
            {run && <p className="mt-3 text-[12.5px] text-muted">Written for the saved backtest, not the modified one.</p>}
            <Disclaimer className="mt-4" />
          </div>
        </Card>
      )}
    </>
  )
}

function MetricsRow({ m, saved, interval }: { m: Metrics | undefined; saved: Metrics | null; interval: string }) {
  // `saved` is set while a re-tune is shown, so each tile can say what it was. Strategies saved before
  // sortino, volatility, calmar and fees existed lack them (calmar comes back null): "—" and no "was".
  const was = (k: keyof Metrics, fmt: (x: number | null) => string) =>
    saved && m && saved[k] != null && saved[k] !== m[k] ? `was ${fmt(saved[k])}` : undefined
  const signed = (x: number | null) => fmtPct(x, { sign: true, decimals: 1 })
  const plain = (x: number | null) => fmtPct(x, { decimals: 0 })
  const pf = (x: number | null | undefined) => (x === 100 ? '∞' : fmtNum(x)) // contract: capped at 100, 100 = no losing trades
  const start = isoAt(m?.startT ?? NaN, interval === '1d')
  const end = isoAt(m?.endT ?? NaN, interval === '1d')

  return (
    <Card className="mt-8 overflow-hidden">
      {/* gap-px over a border-coloured grid draws the hairlines between tiles at every breakpoint */}
      <div className="grid grid-cols-2 gap-px border-b border-border bg-border sm:grid-cols-4">
        <Tile label="Total return" sub={was('totalReturnPct', signed) ?? `Buy & hold ${signed(m?.buyHoldReturnPct ?? NaN)}`} accent>
          <PriceChange value={m?.totalReturnPct} decimals={1} />
        </Tile>
        {/* Under 90 days the server sends null; under 10 trades the figure is noise, so it is held back here too. */}
        {m && m.annualizedReturnPct !== null && m.trades < 10 ? (
          <Tile label="Annualized" sub="needs 10+ trades">
            —
          </Tile>
        ) : (
          <Tile label="Annualized" sub={was('annualizedReturnPct', signed) ?? (m?.annualizedReturnPct === null ? 'needs 90+ days' : undefined)}>
            <PriceChange value={m?.annualizedReturnPct} decimals={1} />
          </Tile>
        )}
        <Tile label="Sharpe" sub={was('sharpe', fmtNum)}>{fmtNum(m?.sharpe)}</Tile>
        <Tile label="Max drawdown" sub={was('maxDrawdownPct', (x) => fmtDD(x))}>{fmtDD(m?.maxDrawdownPct)}</Tile>
        <Tile label="Win rate" sub={was('winRatePct', plain) ?? (m?.winRatePct === null ? 'no closed trades' : undefined)}>
          {fmtPct(m?.winRatePct, { decimals: 0 })}
        </Tile>
        <Tile label="Trades" sub={was('trades', String)}>{m?.trades ?? '—'}</Tile>
        <Tile label="Profit factor" sub={was('profitFactor', pf)}>{pf(m?.profitFactor)}</Tile>
        <Tile label="Exposure" sub={was('exposurePct', plain) ?? 'time in market'}>{fmtPct(m?.exposurePct, { decimals: 0 })}</Tile>
        <Tile label="Sortino" sub={was('sortino', fmtNum) ?? 'downside risk'}>{fmtNum(m?.sortino)}</Tile>
        <Tile label="Volatility" sub={was('volatilityAnnPct', plain) ?? 'annualized'}>{fmtPct(m?.volatilityAnnPct, { decimals: 0 })}</Tile>
        <Tile label="Calmar" sub={was('calmar', fmtNum) ?? 'ann. return ÷ max DD'}>{fmtNum(m?.calmar)}</Tile>
        <Tile label="Fees" sub={was('feesPct', (x) => fmtPct(x)) ?? 'of starting capital'}>{fmtPct(m?.feesPct)}</Tile>
      </div>
      <p className="px-5 py-3 font-mono text-[11px] text-muted">
        Backtest {fmtDate(start)} – {fmtDate(end)} · {m?.bars ? `${m.bars.toLocaleString('en-US')} × ${interval} bars` : '—'} · 0%
        risk-free rate
        {/* A quote feed trades only in its sessions: its Sharpe, Sortino and volatility are annualized on those bars. */}
        {m?.barsPerYear != null && m.barsPerYear > 0 && ` · annualized on trading sessions, about ${Math.round(m.barsPerYear).toLocaleString('en-US')} bars a year`}
      </p>
    </Card>
  )
}

function Tile({ label, sub, accent, children }: { label: string; sub?: string; accent?: boolean; children: ReactNode }) {
  return (
    <div className={cx('min-w-0 px-5 py-4', accent ? 'bg-accent-soft' : 'bg-surface')}>
      <div className="label truncate">{label}</div>
      <div className="mt-1.5 truncate font-mono text-[19px] leading-tight font-medium text-text tabular-nums">{children}</div>
      <div className="mt-1 truncate text-[12px] text-muted">{sub ?? '\u00a0'}</div>
    </div>
  )
}

function Rules({ spec, modified }: { spec: StrategySpec | undefined; modified: boolean }) {
  const { toast } = useToast()
  const rules = spec ? describeRules(spec) : []
  const json = JSON.stringify(spec, null, 2)
  const risk = [
    spec?.stopLossPct ? `Stop loss ${spec.stopLossPct}%` : 'No stop loss',
    spec?.takeProfitPct ? `Take profit ${spec.takeProfitPct}%` : 'No take profit',
    ...(spec?.trailingStopPct ? [`Trailing stop ${spec.trailingStopPct}%`] : []),
    `${spec?.positionPct ?? '—'}% of equity per trade`,
    `${spec?.feePct ?? 0.05}% fee per side`,
  ]
  return (
    <Card>
      <CardHeader label="The spec, in plain English" title="Rules" actions={modified && <Badge tone="accent">Modified</Badge>} />
      <div className="px-5 pb-5">
        {rules.length ? (
          <ol className="flex flex-col gap-3">
            {rules.map((r) => (
              <li key={r.label} className="grid gap-1 sm:grid-cols-[6.5rem_1fr] sm:gap-4">
                <span className="label pt-0.5">{r.label}</span>
                <span className="text-[14.5px] leading-relaxed text-text">{r.text}</span>
              </li>
            ))}
          </ol>
        ) : (
          <p className="text-sm text-muted">This strategy has no readable entry rules.</p>
        )}
        <ul className="mt-5 flex flex-wrap gap-1.5 border-t border-border pt-4">
          {risk.map((r) => (
            <li key={r} className="rounded-md bg-surface-2 px-2 py-1 font-mono text-[11.5px] text-text">
              {r}
            </li>
          ))}
        </ul>
        <p className="mt-3 text-[12.5px] leading-relaxed text-muted">
          Signals are read on bar closes and fill at the next bar's open, one position at a time; stops and targets are
          checked on each bar's high and low, stop first.
        </p>
        {/* No black box: the exact spec, not a paraphrase. */}
        <details className="mt-4 border-t border-border pt-3">
          <summary className="cursor-pointer text-[13px] font-medium text-text marker:text-muted">Rule spec (JSON)</summary>
          <div className="mt-2 flex flex-wrap items-center justify-between gap-2">
            <p className="text-[12.5px] text-muted">This JSON is exactly what the backtester and Autopilot run. No hidden code.</p>
            <Button
              size="sm"
              variant="secondary"
              onClick={() =>
                navigator.clipboard.writeText(json).then(
                  () => toast('Spec copied'),
                  () => toast('Could not copy the spec.', 'error'),
                )
              }
            >
              Copy
            </Button>
          </div>
          <pre className="mt-2 max-h-80 overflow-auto rounded-lg bg-surface-2 p-3 font-mono text-[11.5px] leading-relaxed text-text">{json}</pre>
        </details>
      </div>
    </Card>
  )
}

function Tune({
  base,
  current,
  onRun,
  onReset,
  optimizing,
  onOptimize,
}: {
  base: StrategySpec
  current: StrategySpec
  onRun: (spec: StrategySpec) => Promise<void>
  onReset?: () => void
  optimizing: boolean
  onOptimize: () => void
}) {
  const [input, setInput] = useState<TuneInput>({
    stop: tuneText(current?.stopLossPct),
    take: tuneText(current?.takeProfitPct),
    size: tuneText(current?.positionPct),
    trail: tuneText(current?.trailingStopPct),
  })
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const { values, errors } = parseTune(input)
  const changed = values != null && current != null && tuneChanged(current, values)

  async function rerun(e: FormEvent) {
    e.preventDefault()
    if (!values || !changed || busy) return
    setBusy(true)
    setError(null)
    try {
      await onRun({ ...base, ...values }) // undefined stop/target are dropped from the JSON = none
    } catch (err) {
      setError(errorMessage(err, 'The backtest failed. Please try again.'))
    } finally {
      setBusy(false)
    }
  }

  const field = (key: keyof TuneInput, label: string, placeholder: string) => (
    <div className="min-w-0">
      <Label htmlFor={`tune-${key}`}>{label}</Label>
      <div className="relative">
        <Input
          id={`tune-${key}`}
          inputMode="decimal"
          value={input[key] ?? ''}
          placeholder={placeholder}
          disabled={busy}
          aria-invalid={!!errors[key] || undefined}
          aria-describedby={errors[key] ? `tune-${key}-err` : undefined}
          onChange={(e) => setInput({ ...input, [key]: e.target.value })}
          className="pr-8 font-mono tabular-nums"
        />
        <span aria-hidden="true" className="pointer-events-none absolute top-1/2 right-3 -translate-y-1/2 font-mono text-[13px] text-muted">
          %
        </span>
      </div>
      <p id={`tune-${key}-err`} className="mt-1 min-h-4 text-[11.5px] leading-tight text-down">
        {errors[key]}
      </p>
    </div>
  )

  return (
    <Card>
      <CardHeader
        label="What if?"
        title="Tune"
        actions={
          <Button variant="secondary" size="sm" aria-expanded={optimizing} aria-controls="optimize-panel" onClick={onOptimize}>
            Optimize stops
          </Button>
        }
      />
      <form onSubmit={rerun} className="px-5 pb-5">
        <p className="-mt-1 mb-4 text-[13px] leading-relaxed text-muted">
          Change the risk settings and re-run the same rules. Free, instant and not saved.
        </p>
        <div className="grid grid-cols-2 gap-x-3">
          {field('stop', 'Stop loss', 'none')}
          {field('take', 'Take profit', 'none')}
          {field('trail', 'Trailing stop', 'off')}
          {field('size', 'Position', '100')}
        </div>
        <div className="mt-2 flex flex-wrap items-center gap-2">
          <Button type="submit" variant="primary" loading={busy} disabled={!changed} className="flex-1 sm:flex-none">
            {busy ? 'Backtesting…' : 'Re-run backtest'}
            <span className="font-mono text-[11px] font-normal opacity-70">· free</span>
          </Button>
          {onReset && (
            <Button variant="ghost" onClick={onReset} disabled={busy}>
              Reset to saved
            </Button>
          )}
        </div>
        <div aria-live="polite" className="mt-2 min-h-5 text-[12.5px]">
          {error ? (
            <p role="alert" className="text-down">
              {error}
            </p>
          ) : (
            values && !changed && <p className="text-muted">Edit a value to compare against the current backtest.</p>
          )}
        </div>
      </form>
    </Card>
  )
}

/** "Optimize stops": Sharpe over the 5×5 stop × target grid, and the top 3 to apply. Loads once per mount. */
function Optimize({ spec, onApply }: { spec: StrategySpec; onApply: (c: OptimizeCell) => Promise<void> }) {
  const { data, error } = useLoad(() => api.optimize(spec))
  const [applying, setApplying] = useState<OptimizeCell | null>(null)
  const [applyError, setApplyError] = useState<string | null>(null)
  const grid = data?.grid ?? []
  const stops = [...new Set(grid.map((c) => c.stopLossPct))]
  const targets = [...new Set(grid.map((c) => c.takeProfitPct))]
  const top = Math.max(0.01, ...grid.map((c) => Math.abs(c.sharpe)))
  const pct = (x: number | null) => (x == null ? 'none' : `${x}%`)
  const isCurrent = (c: OptimizeCell) => c.stopLossPct === (spec.stopLossPct ?? null) && c.takeProfitPct === (spec.takeProfitPct ?? null)

  async function apply(c: OptimizeCell) {
    setApplying(c)
    setApplyError(null)
    try {
      await onApply(c)
    } catch (err) {
      setApplyError(errorMessage(err, 'The backtest failed. Please try again.'))
    } finally {
      setApplying(null)
    }
  }

  return (
    <Card id="optimize-panel" className="mt-6">
      <CardHeader label="Optimize stops · free" title="Sharpe by stop and target" />
      <div className="px-5 pb-5">
        {error ? (
          <p role="alert" className="text-[13px] text-down">
            {errorMessage(error, 'The optimizer failed. Please try again.')}
          </p>
        ) : !data ? (
          <div aria-busy="true" aria-label="Running 25 backtests">
            <Skeleton className="h-[220px] w-full" />
          </div>
        ) : (
          <>
            <div className="overflow-x-auto">
              <table className="w-full min-w-[440px] border-separate border-spacing-1 text-center font-mono text-[12.5px] tabular-nums">
                <caption className="mb-2 text-left font-sans text-[12.5px] text-muted">
                  Sharpe ratio of the same rules re-run with each stop loss (rows) and take profit (columns).
                </caption>
                <thead>
                  <tr>
                    <td />
                    {targets.map((t) => (
                      <th key={t} scope="col" className="label pb-1 font-medium">
                        Target {pct(t)}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {stops.map((st) => (
                    <tr key={st}>
                      <th scope="row" className="label pr-2 text-right font-medium whitespace-nowrap">
                        Stop {pct(st)}
                      </th>
                      {targets.map((t) => {
                        const c = grid.find((x) => x.stopLossPct === st && x.takeProfitPct === t)
                        if (!c) return <td key={t} />
                        const id = `opt-${st}-${t}`
                        const tone = c.sharpe >= 0 ? 'var(--up)' : 'var(--down)'
                        return (
                          <td
                            key={t}
                            tabIndex={0}
                            aria-describedby={id}
                            aria-current={isCurrent(c) || undefined}
                            className={cx(
                              'group relative rounded-md px-2 py-2.5 text-text outline-offset-1 focus-visible:outline-2 focus-visible:outline-accent',
                              isCurrent(c) && 'outline-2 outline-text',
                            )}
                            style={{ background: `color-mix(in oklab, ${tone} ${Math.round((Math.abs(c.sharpe) / top) * 55)}%, var(--surface))` }}
                          >
                            {fmtNum(c.sharpe)}
                            {isCurrent(c) && <span className="sr-only"> (current)</span>}
                            <span
                              id={id}
                              role="tooltip"
                              className="pointer-events-none invisible absolute bottom-full left-1/2 z-10 mb-1 -translate-x-1/2 rounded-md bg-ink px-2 py-1 text-[11px] whitespace-nowrap text-ink-fg group-hover:visible group-focus:visible"
                            >
                              Return {fmtPct(c.totalReturnPct, { sign: true, decimals: 1 })} · Max DD {fmtDD(c.maxDrawdownPct)} · {c.trades} trades
                            </span>
                          </td>
                        )
                      })}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>

            <ul aria-label="Best pairs" className="mt-4 flex flex-wrap gap-2">
              {data.best.map((c) => (
                <li
                  key={`${c.stopLossPct}/${c.takeProfitPct}`}
                  className="flex items-center gap-2 rounded-full border border-border py-1 pr-1 pl-3 text-[12.5px]"
                >
                  <span className="font-mono tabular-nums">
                    Stop {pct(c.stopLossPct)} · Target {pct(c.takeProfitPct)} · Sharpe {fmtNum(c.sharpe)}
                  </span>
                  <Button
                    size="sm"
                    variant="secondary"
                    loading={applying === c}
                    disabled={applying != null}
                    aria-label={`Apply stop ${pct(c.stopLossPct)}, target ${pct(c.takeProfitPct)}`}
                    onClick={() => apply(c)}
                  >
                    Apply
                  </Button>
                </li>
              ))}
              {data.best.length === 0 && <li className="text-[13px] text-muted">No pair made 5 or more trades.</li>}
            </ul>
            <p className="mt-3 font-mono text-[12px] text-muted tabular-nums">
              Before optimizing: Stop {pct(data.current.stopLossPct)} · Target {pct(data.current.takeProfitPct)} · Sharpe {fmtNum(data.current.sharpe)}
            </p>
            <p className="mt-1 text-[12.5px] text-muted">Best of 25 tries on the same history: in-sample, expect worse live.</p>
            {applyError && (
              <p role="alert" className="mt-2 text-[12.5px] text-down">
                {applyError}
              </p>
            )}
          </>
        )}
      </div>
    </Card>
  )
}

const REASON: Record<BacktestTrade['reason'], string> = {
  signal: 'Signal',
  stop_loss: 'Stop loss',
  trailing_stop: 'Trailing stop',
  take_profit: 'Take profit',
  end: 'End of test',
}

function TradesCard({ trades, daily, modified }: { trades: BacktestTrade[]; daily: boolean; modified: boolean }) {
  const rows = [...trades].reverse() // newest first
  const when = (t: number) => fmtDate(isoAt(t, daily), { time: !daily })
  const th = 'label px-3 py-2.5 font-medium whitespace-nowrap first:pl-5 last:pr-5'
  const td = 'px-3 py-3 whitespace-nowrap first:pl-5 last:pr-5'

  return (
    <Card className="mt-6 overflow-hidden">
      <CardHeader
        label={modified ? 'Trade log · modified backtest' : 'Trade log'}
        title={trades.length ? `${trades.length === 100 ? 'Last 100' : trades.length} ${trades.length === 1 ? 'trade' : 'trades'}` : 'Trades'}
      />
      {rows.length ? (
        <div className="relative max-h-[480px] overflow-auto border-t border-border">
          <table className="w-full min-w-[720px] text-[13px]">
            <thead className="sticky top-0 z-10 bg-surface-2">
              <tr>
                <th className={cx(th, 'text-left')}>Side</th>
                <th className={cx(th, 'text-left')}>Entry</th>
                <th className={cx(th, 'text-left')}>Exit</th>
                <th className={cx(th, 'text-right')}>Entry price</th>
                <th className={cx(th, 'text-right')}>Exit price</th>
                <th className={cx(th, 'text-right')}>P&L</th>
                <th className={cx(th, 'text-left')}>Reason</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {rows.map((t, i) => (
                <tr key={`${t.entryT}-${i}`}>
                  <td className={td}>
                    <span className="inline-flex items-center gap-1.5 font-medium text-text">
                      <span aria-hidden="true" className={cx('text-[0.7em]', t.side === 'long' ? 'text-up' : 'text-down')}>
                        {t.side === 'long' ? '▲' : '▼'}
                      </span>
                      {t.side === 'long' ? 'Long' : 'Short'}
                    </span>
                  </td>
                  <td className={cx(td, 'text-muted')}>{when(t.entryT)}</td>
                  <td className={cx(td, 'text-muted')}>{when(t.exitT)}</td>
                  <td className={cx(td, 'text-right font-mono tabular-nums')}>{fmtPrice(t.entry)}</td>
                  <td className={cx(td, 'text-right font-mono tabular-nums')}>{fmtPrice(t.exit)}</td>
                  <td className={cx(td, 'text-right')}>
                    <PriceChange value={t.pnlPct} />
                  </td>
                  <td className={td}>
                    <Badge tone={t.reason === 'stop_loss' || t.reason === 'trailing_stop' ? 'down' : t.reason === 'take_profit' ? 'up' : 'neutral'}>
                      {REASON[t.reason] ?? t.reason}
                    </Badge>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <p className="border-t border-border px-5 py-10 text-center text-sm text-muted">
          No trades: the entry rules never fired in this window. Loosen a threshold or try a longer interval.
        </p>
      )}
    </Card>
  )
}

function DeleteButton({ s }: { s: Strategy }) {
  const navigate = useNavigate()
  const { toast } = useToast()
  const dialog = useRef<HTMLDialogElement>(null)
  const [busy, setBusy] = useState(false)

  async function remove() {
    setBusy(true)
    try {
      await api.deleteStrategy(s.id)
      toast(`Deleted "${s.name}".`)
      navigate('/strategies', { replace: true })
    } catch (err) {
      toast(errorMessage(err, 'Could not delete the strategy.'), 'error')
      setBusy(false)
    }
  }

  return (
    <>
      <Button variant="danger" onClick={() => dialog.current?.showModal()}>
        Delete
      </Button>
      <dialog
        ref={dialog}
        aria-labelledby="delete-title"
        onCancel={(e) => busy && e.preventDefault()}
        className="card m-auto w-[calc(100%-2rem)] max-w-md animate-fade-up p-6 text-text backdrop:bg-[rgb(15_27_45/0.45)] backdrop:backdrop-blur-[2px]"
      >
        <div className="label mb-2">Strategy studio</div>
        <h2 id="delete-title" className="font-display text-2xl leading-tight font-medium">
          Delete “{s.name}”?
        </h2>
        <p className="mt-2.5 text-sm leading-relaxed text-muted">The rules and the saved backtest are removed. This can't be undone.</p>
        <div className="mt-6 flex justify-end gap-2">
          <Button variant="ghost" onClick={() => dialog.current?.close()} disabled={busy}>
            Cancel
          </Button>
          <Button variant="danger" onClick={remove} loading={busy}>
            Delete strategy
          </Button>
        </div>
      </dialog>
    </>
  )
}

function DetailSkeleton() {
  return (
    <div aria-busy="true" aria-label="Loading strategy">
      <Skeleton className="h-5 w-56" />
      <Skeleton className="mt-4 h-11 w-3/4 max-w-xl" />
      <Skeleton className="mt-4 h-4 w-full max-w-2xl" />
      <Skeleton className="mt-2 h-4 w-2/3 max-w-xl" />
      <Skeleton className="mt-8 h-[236px] w-full rounded-[14px] sm:h-[200px]" />
      <Skeleton className="mt-6 h-[382px] w-full rounded-[14px]" />
      <div className="mt-6 grid gap-6 lg:grid-cols-[minmax(0,1.35fr)_minmax(0,1fr)]">
        <Skeleton className="h-64 rounded-[14px]" />
        <Skeleton className="h-64 rounded-[14px]" />
      </div>
    </div>
  )
}

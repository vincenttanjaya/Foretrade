// Tap-to-run cards the agent attaches (contract "Agent chat": forecast_draft, build_action). The agent only drafts or
// describes; the user's tap spends the credits and runs the forecast desk, the portfolio builder or the strategy studio.
import { useState, type ReactNode } from 'react'
import { Link } from 'react-router'
import { useToast } from '../../components/toast'
import { Badge } from '../../components/ui/Badge'
import { Button } from '../../components/ui/Button'
import { Card } from '../../components/ui/Card'
import { IconAlert, IconArrowRight, IconForecast, IconPortfolio, IconStrategy } from '../../components/ui/icons'
import { Spinner } from '../../components/ui/Spinner'
import { api } from '../../lib/api'
import { useAuth } from '../../lib/auth'
import { cx } from '../../lib/cx'
import { errorMessage, isQuota } from '../../lib/errors'
import { fmtDate, fmtPct, fmtProb } from '../../lib/format'
import type { AIPortfolio, BuildResult, Card as AgentCardType, Credits, DraftRun, Forecast, OutcomeP, Strategy } from '../../lib/types'
import { usePoll } from '../../lib/usePoll'
import { comparison, measuredWindow } from '../portfolios/portfolio'
import { briefOf, shapeLabel, viewOf, viewReading } from '../forecasts/forecast'

type DraftData = Extract<AgentCardType, { type: 'forecast_draft' }>['data']
type BuildData = Extract<AgentCardType, { type: 'build_action' }>['data']
type Run = DraftRun & { error?: string | null }

const RUN_COST = 5 // contract: forecast run
const POLL_MS = 3000

/**
 * The run's top outcome: Yes (with P(Yes)) for a binary question, else the likeliest outcome. The card states it unless
 * the run's view leads (viewReading), as the forecast list does.
 */
function headlineOf(f: Forecast): OutcomeP | null {
  const ps = f.result?.probabilities ?? []
  if (ps.length === 0) return null
  return f.shape === 'binary' ? ps[0] : ps.reduce((a, b) => (b.p > a.p ? b : a))
}

/** A polled run as the card keeps it: its headline and, for a numeric report, its view in brief. */
function runOf(f: Forecast): Run {
  const view = f.shape === 'ordered' ? viewOf(f.result) : null
  return { id: f.id, status: f.status, headline: headlineOf(f), error: f.error, ...(view ? { view: briefOf(view) } : {}) }
}

/** A report's view reading, stacked: "Median $611" over "62% ≥ today", or the 80% range over its label. */
export function DeskReading({ reading, className }: { reading: NonNullable<ReturnType<typeof viewReading>>; className?: string }) {
  return (
    <div className={cx('min-w-0', className)}>
      {/* 17 px on a phone, so a range ("≈$242–≈$1,480") stays on one line beside the question. */}
      <div className="font-mono text-[17px] leading-tight font-medium text-text tabular-nums sm:text-[20px]">{reading.value}</div>
      <div className="mt-0.5 font-mono text-[11px] text-muted tabular-nums">{reading.note}</div>
    </div>
  )
}

// The same shape GET /api/chats/{id} resolves on reload, built from the POST response.
const fromPortfolio = (p: AIPortfolio): BuildResult => ({
  benchmarkSymbol: comparison(p.backtest).symbol,
  benchmarkLabel: comparison(p.backtest).label,
  windowStart: p.backtest.windowStart ?? p.backtest.series[0]?.t ?? null,
  windowEnd: p.backtest.windowEnd ?? p.backtest.series.at(-1)?.t ?? null,
  bars: p.backtest.bars || p.backtest.series.length || null,
  id: p.id,
  name: p.name,
  returnPct: p.backtest.returnPct,
  benchmarkReturnPct: p.backtest.benchmarkReturnPct,
  sharpe: null,
  maxDrawdownPct: p.backtest.maxDrawdownPct,
  holdings: p.holdings.map(({ symbol, weight }) => ({ symbol, weight })),
})

const fromStrategy = (s: Strategy): BuildResult => ({
  benchmarkSymbol: null, benchmarkLabel: null, windowStart: null, windowEnd: null, bars: null,
  id: s.id,
  name: s.name,
  returnPct: s.metrics.totalReturnPct,
  benchmarkReturnPct: s.metrics.buyHoldReturnPct,
  sharpe: s.metrics.sharpe,
  maxDrawdownPct: s.metrics.maxDrawdownPct,
  holdings: [],
})

const signed = (x: number) => fmtPct(x, { sign: true, decimals: 1 })

/** One credit-spending tap: credits update on success; ai_quota gets the usual toast, anything else an inline error. */
function useTap() {
  const { setCredits, refresh } = useAuth()
  const { toast } = useToast()
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function tap<T extends { credits?: Credits }>(call: () => Promise<T>): Promise<T | null> {
    if (pending) return null
    setPending(true)
    setError(null)
    try {
      const res = await call()
      if (res.credits) setCredits(res.credits)
      return res
    } catch (err) {
      if (isQuota(err)) {
        toast(errorMessage(err), 'error')
        refresh().catch(() => {})
      } else setError(errorMessage(err))
      return null
    } finally {
      setPending(false)
    }
  }
  return { pending, error, tap }
}

function Header({ icon, label, children }: { icon: ReactNode; label: string; children: ReactNode }) {
  return (
    <div className="px-4 pt-4">
      <div className="label flex items-center gap-1.5">
        {icon}
        {label}
      </div>
      {children}
    </div>
  )
}

function ErrorRetry({ message, onRetry, pending }: { message: string; onRetry: () => void; pending: boolean }) {
  return (
    <div role="alert" className="flex flex-wrap items-center gap-x-3 gap-y-2 text-[13px] text-down">
      <IconAlert size={15} className="shrink-0" />
      <span className="min-w-0 flex-1">{message}</span>
      <Button size="sm" variant="secondary" loading={pending} onClick={onRetry}>
        Retry
      </Button>
    </div>
  )
}

function Go({ to, children }: { to: string; children: ReactNode }) {
  return (
    <Link to={to} className="inline-flex items-center gap-1 text-[13px] font-medium whitespace-nowrap text-accent-ink hover:underline">
      {children} <IconArrowRight size={14} />
    </Link>
  )
}

/** A forecast-desk draft from the chat: Run posts it as is (5 credits), then the card follows the run to its headline. */
export function ForecastDraftCard({ data }: { data: DraftData }) {
  const { draft } = data
  const [run, setRun] = useState<Run | null>(data.forecast ?? null)
  const { pending, error, tap } = useTap()

  usePoll(
    () => {
      if (!run) return
      api.forecast(run.id).then(
        ({ forecast: f }) => setRun(runOf(f)),
        () => {}, // the next poll retries
      )
    },
    POLL_MS,
    run?.status === 'running',
  )

  async function start() {
    const res = await tap(() => api.createForecast(draft))
    if (res) setRun({ id: res.forecast.id, status: res.forecast.status, headline: null })
  }

  let footer: ReactNode
  if (!run) {
    footer = error ? (
      <ErrorRetry message={error} onRetry={start} pending={pending} />
    ) : (
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        <Button variant="primary" size="sm" loading={pending} onClick={start}>
          Run the desk · {RUN_COST} cr
        </Button>
        <Link to="/forecasts/new" state={{ draft, origin: 'ai' }} className="text-[13px] font-medium text-muted hover:text-text hover:underline">
          Open Forecast desk
        </Link>
      </div>
    )
  } else if (run.status === 'running') {
    footer = (
      <div role="status" className="flex flex-wrap items-center gap-x-3 gap-y-1.5 text-[13.5px] text-text">
        <Spinner className="size-4 text-accent" />
        <span className="flex-1">Specialists at work… usually four to eight minutes</span>
        <Go to={`/forecasts/${encodeURIComponent(run.id)}`}>Open report</Go>
      </div>
    )
  } else if (run.status === 'completed') {
    // A numeric report whose view leads states its median (or its 80% range), never a top range that does not stand out.
    const reading = viewReading(run)
    footer = (
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div className="min-w-0">
          <div className="label">Desk estimate</div>
          {reading ? (
            <DeskReading reading={reading} className="mt-0.5" />
          ) : (
            <div className="mt-0.5 font-mono text-[20px] leading-tight font-medium text-text tabular-nums">
              {run.headline ? `${run.headline.outcome} ${fmtProb(run.headline.p)}` : '—'}
            </div>
          )}
        </div>
        <Go to={`/forecasts/${encodeURIComponent(run.id)}`}>Open report</Go>
      </div>
    )
  } else {
    footer = <ErrorRetry message={run.error || error || 'The forecast could not be completed.'} onRetry={start} pending={pending} />
  }

  return (
    <Card className="overflow-hidden">
      <Header icon={<IconForecast size={13} />} label="Forecast desk draft">
        <p className="mt-1.5 font-display text-[17px] leading-snug font-medium text-pretty text-text">{draft.question}</p>
        <div className="mt-2.5 flex flex-wrap gap-1.5">
          <Badge>{shapeLabel(draft.shape)}</Badge>
          <Badge tone="outline">Resolves {fmtDate(draft.resolutionDate)}</Badge>
          {draft.priceRule && <Badge tone="accent">Auto-resolves from Binance</Badge>}
        </div>
        {draft.shape !== 'binary' && draft.outcomes.length > 0 && (
          <ul aria-label="Outcomes" className="mt-3 flex flex-wrap gap-1.5">
            {draft.outcomes.map((o) => (
              <li key={o} className="rounded-md border border-border px-2 py-0.5 font-mono text-[11.5px] text-text">
                {o}
              </li>
            ))}
          </ul>
        )}
      </Header>
      <div className="mt-3.5 border-t border-border px-4 py-3">{footer}</div>
    </Card>
  )
}

/** An AI portfolio or a strategy backtest the agent set up: Build runs it (2 credits) and shows the result inline. */
export function BuildActionCard({ data }: { data: BuildData }) {
  const portfolio = data.kind === 'portfolio'
  const [result, setResult] = useState<BuildResult | null>(data.result ?? null)
  const { pending, error, tap } = useTap()

  async function build() {
    if (portfolio) {
      const res = await tap(() => api.buildPortfolio(data.prompt))
      if (res) setResult(fromPortfolio(res.portfolio))
    } else {
      const res = await tap(() => api.generateStrategy({ prompt: data.prompt, symbol: data.symbol ?? undefined, interval: data.interval ?? undefined }))
      if (res) setResult(fromStrategy(res.strategy))
    }
  }

  const market = [data.symbol, data.interval].filter(Boolean).join(' · ')
  let footer: ReactNode
  if (result && portfolio) {
    footer = (
      <>
        <p className="text-[14.5px] font-medium text-text">{result.name}</p>
        <ul aria-label="Holdings" className="mt-2 flex flex-wrap gap-1.5">
          {result.holdings.map((h) => (
            <li key={h.symbol}>
              <Badge tone="outline">
                {h.symbol} {fmtProb(h.weight)}
              </Badge>
            </li>
          ))}
        </ul>
        <div className="mt-2.5 flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
          <p className="font-mono text-[12px] text-muted tabular-nums">
            Backtest {signed(result.returnPct)} vs {comparison(result).label} {signed(result.benchmarkReturnPct)} · {measuredWindow(result)}
          </p>
          <Go to={`/portfolios/${encodeURIComponent(result.id)}`}>Open &amp; invest</Go>
        </div>
      </>
    )
  } else if (result) {
    footer = (
      <>
        <p className="text-[14.5px] font-medium text-text">{result.name}</p>
        <div className="mt-1.5 flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
          <p className="font-mono text-[12px] text-muted tabular-nums">
            Return {signed(result.returnPct)} vs buy &amp; hold {signed(result.benchmarkReturnPct)}
            {result.sharpe != null && ` · Sharpe ${result.sharpe.toFixed(2)}`} · Max DD {fmtPct(result.maxDrawdownPct, { decimals: 1 })}
          </p>
          <Go to={`/strategies/${encodeURIComponent(result.id)}`}>Open in Studio</Go>
        </div>
      </>
    )
  } else if (error) {
    footer = <ErrorRetry message={error} onRetry={build} pending={pending} />
  } else {
    footer = (
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <Button variant="primary" size="sm" loading={pending} onClick={build}>
          {portfolio ? 'Build portfolio' : 'Generate & backtest'} · {data.cost} cr
        </Button>
        {pending && <span className="text-[12.5px] text-muted">Usually 10–30 seconds</span>}
      </div>
    )
  }

  return (
    <Card className="overflow-hidden">
      <Header icon={portfolio ? <IconPortfolio size={13} /> : <IconStrategy size={13} />} label={portfolio ? 'AI portfolio' : 'Strategy backtest'}>
        <p className="mt-1.5 font-display text-[17px] leading-snug text-pretty text-text">“{data.prompt}”</p>
        {!portfolio && market && (
          <div className="mt-2.5 flex gap-1.5">
            <Badge>{market}</Badge>
          </div>
        )}
      </Header>
      <div className="mt-3.5 border-t border-border px-4 py-3">{footer}</div>
    </Card>
  )
}

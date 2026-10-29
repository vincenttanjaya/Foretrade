import { useEffect, useRef, useState } from 'react'
import { Badge } from '../../components/ui/Badge'
import { Button } from '../../components/ui/Button'
import { Disclaimer } from '../../components/ui/Disclaimer'
import { IconAgent, IconAlert, IconArrowRight } from '../../components/ui/icons'
import { ProbabilityBars } from '../../components/ui/ProbabilityBars'
import { Skeleton, Spinner } from '../../components/ui/Spinner'
import { Tabs } from '../../components/ui/Tabs'
import { api } from '../../lib/api'
import { useAuth } from '../../lib/auth'
import { cx } from '../../lib/cx'
import { errorMessage, isQuota } from '../../lib/errors'
import { fmtPct, fmtPrice, timeAgo } from '../../lib/format'
import type { Analysis, CopilotBias, CopilotStyle, DeskRead, Interval, NewsItem, Plan, PriceOutlook } from '../../lib/types'
import { DeskChip, deskSubject } from './DeskChip'
import { linkable } from './util'

// Contract: style → analysis interval / outlook horizon.
const STYLES: Record<CopilotStyle, { label: string; interval: Interval; days: number; hint: string }> = {
  scalp: { label: 'Scalp', interval: '15m', days: 1, hint: '15m chart · 1-day outlook' },
  day: { label: 'Day', interval: '1h', days: 3, hint: '1h chart · 3-day outlook' },
  swing: { label: 'Swing', interval: '4h', days: 14, hint: '4h chart · 14-day outlook' },
}

type Result = { plan: Plan; analysis: Analysis; outlook: PriceOutlook; headlines: NewsItem[]; bias: CopilotBias }

/** The entry's distance from the price (−0.015 = 1.5% below) when it is far enough (> 0.25%) to wait for as a limit order; null = take the market. */
export function limitGap(entry: number, mark: number | undefined): number | null {
  const gap = mark && mark > 0 ? entry / mark - 1 : 0
  return Math.abs(gap) > 0.0025 ? gap : null
}

/** Style picker + Ask Long / Ask Short / Let AI decide → plan card. Each plan costs 1 AI credit. */
export function Copilot({
  symbol,
  onPlan,
  onUse,
}: {
  symbol: string
  onPlan: (plan: Plan, interval: Interval) => void
  onUse: (plan: Plan, mark: number) => void
}) {
  const { user, setCredits, refresh } = useAuth()
  const preferred = user?.profile.enabled ? user.profile.style : null
  const [style, setStyle] = useState<CopilotStyle>(preferred ?? 'day')
  const chosen = useRef(false)
  useEffect(() => { if (!chosen.current) setStyle(preferred ?? 'day') }, [preferred])
  const chooseStyle = (value: CopilotStyle) => { chosen.current = true; setStyle(value) }
  const [pending, setPending] = useState<CopilotBias | null>(null)
  const [error, setError] = useState('')
  const [result, setResult] = useState<Result | null>(null)
  const alive = useRef(true) // the panel remounts per symbol; a late answer must not touch the new chart
  useEffect(() => {
    alive.current = true
    return () => {
      alive.current = false
    }
  }, [])

  async function ask(bias: CopilotBias) {
    chosen.current = true // Capture the selected style before awaiting a profile or plan response.
    setPending(bias)
    setError('')
    try {
      const res = await api.copilotPlan({ symbol, style, bias })
      setCredits(res.credits)
      if (!alive.current) return
      const plan = { ...res.plan, symbol }
      setResult({ plan, analysis: res.analysis, outlook: res.outlook, headlines: linkable(res.headlines, 3), bias })
      onPlan(plan, STYLES[style].interval)
    } catch (err) {
      setError(errorMessage(err, 'The copilot could not build a plan. Please try again.'))
      if (isQuota(err)) void refresh().catch(() => {})
    } finally {
      setPending(null)
    }
  }

  return (
    <div className="space-y-4 p-4">
      <div>
        <div className="mb-2 flex items-baseline justify-between gap-2">
          <span className="label">Trading style</span>
          <span className="font-mono text-[11px] text-muted">{STYLES[style].hint}</span>
        </div>
        <Tabs
          value={style}
          onChange={chooseStyle}
          items={(Object.keys(STYLES) as CopilotStyle[]).map((v) => ({ value: v, label: STYLES[v].label }))}
          aria-label="Trading style"
        />
      </div>

      <div className="grid grid-cols-2 gap-2">
        <Button variant="secondary" loading={pending === 'long'} disabled={pending != null} onClick={() => ask('long')}>
          {pending !== 'long' && (
            <span aria-hidden="true" className="text-[0.75em] text-up">
              ▲
            </span>
          )}
          Ask Long
        </Button>
        <Button variant="secondary" loading={pending === 'short'} disabled={pending != null} onClick={() => ask('short')}>
          {pending !== 'short' && (
            <span aria-hidden="true" className="text-[0.75em] text-down">
              ▼
            </span>
          )}
          Ask Short
        </Button>
        <Button variant="accent" className="col-span-2" loading={pending === 'auto'} disabled={pending != null} onClick={() => ask('auto')}>
          {pending !== 'auto' && <IconAgent size={16} />}
          Let AI decide
        </Button>
      </div>
      <p className="-mt-1 text-center font-mono text-[10.5px] tracking-[0.06em] text-muted">1 AI credit per plan</p>

      {error && (
        <div role="alert" className="flex gap-2.5 rounded-lg border border-down/30 bg-down/6 px-3 py-2.5 text-[13px] text-text">
          <IconAlert size={16} className="mt-px shrink-0 text-down" />
          {error}
        </div>
      )}

      <div aria-live="polite">
        {pending ? (
          <PlanSkeleton symbol={symbol} interval={STYLES[style].interval} />
        ) : result ? (
          <PlanCard key={result.plan.generatedAt} {...result} onUse={() => onUse(result.plan, result.analysis?.price ?? 0)} onSwing={result.plan.style === 'swing' ? undefined : () => chooseStyle('swing')} />
        ) : (
          <div className="rounded-xl border border-dashed border-border px-4 py-6 text-center">
            <div className="mx-auto mb-3 grid size-10 place-items-center rounded-full bg-accent-soft text-accent-ink">
              <IconAgent size={18} />
            </div>
            <p className="font-display text-[17px] text-text">A plan in one tap.</p>
            <p className="mx-auto mt-1 max-w-[17rem] text-[13px] leading-relaxed text-muted">
              Pick a style and a bias. You get entry, stop and target levels, the reasoning, and a price outlook for {symbol}.
            </p>
          </div>
        )}
      </div>
    </div>
  )
}

function PlanSkeleton({ symbol, interval }: { symbol: string; interval: Interval }) {
  return (
    <div className="rounded-xl border border-border p-4">
      <div className="flex items-center gap-2 text-[13px] text-muted">
        <Spinner className="size-4" />
        Reading the {symbol} {interval} chart and pricing the outlook…
      </div>
      <Skeleton className="mt-4 h-2 w-full" />
      <div className="mt-4 grid grid-cols-3 gap-2">
        {[0, 1, 2].map((i) => (
          <Skeleton key={i} className="h-12" />
        ))}
      </div>
      <Skeleton className="mt-4 h-3 w-11/12" />
      <Skeleton className="mt-2 h-3 w-4/5" />
      <Skeleton className="mt-2 h-3 w-3/5" />
      <Skeleton className="mt-4 h-24 w-full" />
    </div>
  )
}

const DIRECTION = {
  long: { label: 'Long', glyph: '▲', tone: 'up' },
  short: { label: 'Short', glyph: '▼', tone: 'down' },
  neutral: { label: 'Neutral', glyph: '■', tone: 'neutral' },
} as const

const TREND_WORD = { uptrend: 'up', downtrend: 'down', range: 'flat' } as const

function PlanCard({ plan, analysis, outlook, headlines, bias, onUse, onSwing }: Result & { onUse: () => void; onSwing?: () => void }) {
  const style = STYLES[plan.style as CopilotStyle]
  const interval = plan.interval ?? style?.interval
  const daily = plan.higherTrend?.trend
  // Ask Long/Short on a weak setup comes back neutral: say which side has no setup, and point at a daily trend that agrees.
  const waitSide = plan.direction === 'neutral' && bias !== 'auto' ? bias : null
  // The badge keeps the side that was asked for, marked as a wait, instead of a bare "Neutral".
  const dir = waitSide
    ? { ...DIRECTION[waitSide], label: `${DIRECTION[waitSide].label} · wait`, tone: 'neutral' as const }
    : (DIRECTION[plan.direction] ?? DIRECTION.neutral)
  const dailyAgrees = (waitSide === 'long' && daily === 'uptrend') || (waitSide === 'short' && daily === 'downtrend')
  const confidence = Math.max(0, Math.min(100, Math.round(plan.confidence || 0)))
  const dist = (level: number) => (plan.entry > 0 ? fmtPct(((level - plan.entry) / plan.entry) * 100, { sign: true }) : '')
  const gap = limitGap(plan.entry, analysis?.price)
  const levels = [
    { label: 'Entry', value: plan.entry, sub: gap == null ? 'market' : `limit · ${fmtPct(gap * 100, { sign: true })} from price`, dot: 'bg-accent' },
    { label: 'Stop', value: plan.stopLoss, sub: dist(plan.stopLoss), dot: 'bg-down' },
    { label: 'Target', value: plan.takeProfit, sub: dist(plan.takeProfit), dot: 'bg-up' },
  ]
  const buckets = outlook?.buckets ?? []
  const catalysts = (Array.isArray(plan.catalysts) ? plan.catalysts : []).filter(Boolean)

  return (
    <article className="animate-fade-up overflow-hidden rounded-xl border border-border">
      <header className="flex flex-wrap items-center gap-2 border-b border-border bg-surface-2/60 px-4 py-3">
        <Badge tone={dir.tone}>
          <span aria-hidden="true">{dir.glyph}</span> {dir.label}
        </Badge>
        <Badge tone="outline" title={plan.horizon}>
          {style ? `${style.label} · ${interval} chart · ${style.days}-day horizon` : `${plan.style} · ${plan.horizon}`}
        </Badge>
        {daily && (
          <span className="rounded-md border border-border px-1.5 py-0.5 font-mono text-[10.5px] text-muted" title="Higher timeframe: the daily trend">
            1d {daily}
          </span>
        )}
        <span className="ml-auto font-mono text-[11px] text-muted">{timeAgo(plan.generatedAt)}</span>
      </header>

      <div className="space-y-4 p-4">
        {plan.desk && (
          <div className="space-y-2">
            <DeskChip symbol={plan.symbol} read={plan.desk} />
            <DeskStance desk={plan.desk} />
          </div>
        )}
        <div>
          <div className="flex items-baseline justify-between">
            <span className="label">Confidence</span>
            <span className="font-mono text-[13px] font-semibold text-text tabular-nums">
              {confidence}
              <span className="text-muted">/100</span>
            </span>
          </div>
          <div
            role="meter"
            aria-label="Confidence"
            aria-valuemin={0}
            aria-valuemax={100}
            aria-valuenow={confidence}
            className="mt-1.5 h-1.5 overflow-hidden rounded-full bg-surface-2"
          >
            <div className="h-full origin-left animate-bar rounded-full bg-accent" style={{ width: `${confidence}%` }} />
          </div>
        </div>

        <dl className="divide-y divide-border rounded-lg bg-surface-2">
          {levels.map((l) => (
            <div
              key={l.label}
              // A limit entry's longer note ("limit · -1.50% from price") gets a wider column on every row, so the prices stay aligned.
              className={cx('grid items-baseline gap-3 px-3 py-2', gap == null ? 'grid-cols-[1fr_auto_4.5rem]' : 'grid-cols-[1fr_auto_6.5rem]')}
            >
              <dt className="flex items-center gap-2 text-[13px] text-muted">
                <span aria-hidden="true" className={cx('size-1.5 rounded-full', l.dot)} />
                {l.label}
              </dt>
              <dd className="font-mono text-[13.5px] font-medium text-text tabular-nums">{fmtPrice(l.value)}</dd>
              <dd className="text-right font-mono text-[11.5px] text-muted tabular-nums">{l.sub}</dd>
            </div>
          ))}
        </dl>

        <div className="flex flex-wrap gap-x-4 gap-y-1 font-mono text-[11.5px] text-muted tabular-nums">
          <span>
            R:R <span className="text-text">1 : {Number.isFinite(plan.riskReward) ? plan.riskReward.toFixed(2) : '—'}</span>
          </span>
          {analysis && (
            <>
              <span className="capitalize">{analysis.trend}</span>
              <span>
                RSI <span className="text-text">{Math.round(analysis.rsi14)}</span>
              </span>
              <span>
                Vol <span className="text-text">{fmtPct(analysis.realizedVolAnnPct, { decimals: 0 })}</span>
              </span>
            </>
          )}
        </div>

        {plan.rationale?.length > 0 && (
          <ul className="space-y-1.5 text-[13.5px] leading-snug text-text">
            {plan.rationale.map((r, i) => (
              <li key={i} className="flex gap-2">
                <span aria-hidden="true" className="mt-[7px] size-1 shrink-0 rounded-full bg-accent" />
                {r}
              </li>
            ))}
          </ul>
        )}

        {catalysts.length > 0 && (
          <div>
            <div className="label mb-1.5">Catalysts</div>
            <ul className="space-y-1.5 text-[13px] leading-snug text-text">
              {catalysts.map((c, i) => (
                <li key={i} className="flex gap-2">
                  <span aria-hidden="true" className="mt-[6px] size-1 shrink-0 rounded-full bg-text/50" />
                  {c}
                </li>
              ))}
            </ul>
          </div>
        )}

        {headlines.length > 0 && (
          <div>
            <div className="label mb-1.5">In the news</div>
            <ul className="space-y-2">
              {headlines.map((h) => (
                <li key={h.id || h.url}>
                  <a
                    href={h.url}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="text-[13px] leading-snug text-text hover:text-accent-ink hover:underline"
                  >
                    {h.title}
                    <IconArrowRight size={11} className="ml-1 inline -rotate-45 align-baseline text-muted" />
                    <span className="sr-only"> (opens in a new tab)</span>
                  </a>
                  <div className="font-mono text-[10.5px] text-muted">
                    {[h.source, h.publishedAt && timeAgo(h.publishedAt)].filter(Boolean).join(' · ')}
                  </div>
                </li>
              ))}
            </ul>
          </div>
        )}

        {plan.invalidation && (
          <div className="rounded-lg bg-surface-2 px-3 py-2.5">
            <div className="label mb-1">Invalidation</div>
            <p className="text-[13px] leading-snug text-muted">{plan.invalidation}</p>
          </div>
        )}

        {buckets.length > 0 && (
          <div>
            <div className="mb-2 flex items-baseline justify-between gap-2">
              <span className="label">
                Price in {outlook.horizonDays} {outlook.horizonDays === 1 ? 'day' : 'days'}
              </span>
              <span className="font-mono text-[10.5px] text-muted">lognormal · vol {fmtPct(outlook.volAnnPct, { decimals: 0 })}</span>
            </div>
            <ProbabilityBars compact items={buckets.map((b) => ({ label: b.label, p: b.p }))} />
            <dl className="mt-3 grid grid-cols-3 gap-2 text-center">
              {(
                [
                  ['p5', outlook.p5],
                  ['Median', outlook.p50],
                  ['p95', outlook.p95],
                ] as const
              ).map(([k, v]) => (
                <div key={k}>
                  <dt className="font-mono text-[10px] tracking-[0.1em] text-muted uppercase">{k}</dt>
                  {/* 5 significant figures: a model percentile, not a quote ($100.04, not $100.0369) */}
                  <dd className="font-mono text-[12.5px] text-text tabular-nums">{fmtPrice(Number(v.toPrecision(5)))}</dd>
                </div>
              ))}
            </dl>
          </div>
        )}

        <Button
          // A wait label may wrap at 375 px; `!` because Button's own h-10 / whitespace-nowrap would otherwise win in the stylesheet.
          className={cx('w-full', plan.direction === 'neutral' && 'h-auto! py-2.5 text-center whitespace-normal!')}
          disabled={plan.direction === 'neutral'}
          onClick={onUse}
        >
          {waitSide
            ? `No ${waitSide} setup on ${interval} · wait`
            : plan.direction === 'neutral'
              ? 'No trade · the copilot says wait'
              : gap != null
                ? `Place limit at ${fmtPrice(plan.entry)}`
                : 'Use this plan'}
        </Button>
        {dailyAgrees && daily && (
          <p className="-mt-2 text-center text-[12.5px] text-muted">
            Daily trend is {TREND_WORD[daily]}
            {onSwing ? (
              <>
                :{' '}
                <button type="button" onClick={onSwing} className="font-medium text-accent-ink underline-offset-2 hover:underline">
                  try Swing
                </button>
              </>
            ) : (
              '.'
            )}
          </p>
        )}
        <Disclaimer />
      </div>
    </article>
  )
}

/** How the desk reads this plan's side: amber when it leans against it, muted when it agrees or is split. */
function DeskStance({ desk }: { desk: DeskRead }) {
  if (desk.stance === 'opposes')
    return (
      <p className="flex gap-2 rounded-lg border border-accent/40 bg-accent-soft px-3 py-2 text-[12.5px] leading-snug text-text">
        <IconAlert size={15} className="mt-px shrink-0 text-accent-ink" />
        <span>
          The desk leans against this plan · {desk.lean} for {deskSubject(desk)}
        </span>
      </p>
    )
  if (desk.stance === 'supports') return <p className="text-[12.5px] text-muted">The desk agrees · {desk.lean}</p>
  if (desk.stance === 'split') return <p className="text-[12.5px] text-muted">The desk is split · {Math.round(desk.p * 100)}%</p>
  return null
}

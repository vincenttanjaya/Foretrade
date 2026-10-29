// Presentational pieces of the AI backtest arena: status badge, leaderboard and decision log.
import { type ReactNode } from 'react'
import { Badge } from '../../components/ui/Badge'
import { Button } from '../../components/ui/Button'
import { Card, CardHeader } from '../../components/ui/Card'
import { IconCheck } from '../../components/ui/icons'
import { PriceChange } from '../../components/ui/PriceChange'
import { Skeleton } from '../../components/ui/Spinner'
import { Tabs } from '../../components/ui/Tabs'
import { cx } from '../../lib/cx'
import { fmtPct, fmtPrice, fmtQty, fmtUsd } from '../../lib/format'
import type { ArenaAgent, ArenaBenchmark, ArenaPersonaId, ArenaRun, ArenaStatus, ArenaStep } from '../../lib/types'
import { PulseDot } from '../forecasts/parts'
import { fmtDD, fmtNum } from '../strategies/strategy'
import { agentColor, rankRows, simAt, targetDiff, toolLabel } from './arena'

export function StatusBadge({ status, pct }: { status: ArenaStatus; pct?: number }) {
  if (status === 'running')
    return (
      <Badge tone="accent">
        <PulseDot />
        Running{pct != null && ` ${Math.floor(pct)}%`}
      </Badge>
    )
  if (status === 'failed') return <Badge tone="down">Failed</Badge>
  if (status === 'cancelled') return <Badge tone="outline">Cancelled</Badge>
  return (
    <Badge tone="neutral">
      <IconCheck size={11} strokeWidth={2.4} />
      Completed
    </Badge>
  )
}

export function ContaminatedBadge() {
  return (
    <Badge tone="accent" title="The window starts on or before the model's training cutoff, so it may remember what happened.">
      Contaminated
    </Badge>
  )
}

const PERSONA: Record<ArenaPersonaId, string> = { momentum: 'Momentum', contrarian: 'Contrarian', macro: 'News', custom: 'Custom' }

// ── Leaderboard ─────────────────────────────────────────────────────────────

export function Leaderboard({ agents, benchmarks, daily }: { agents: ArenaAgent[]; benchmarks: ArenaBenchmark[]; daily: boolean }) {
  const rows = rankRows(agents, benchmarks)
  const th = 'label px-2.5 py-2.5 font-medium whitespace-nowrap text-right first:pl-5 first:text-left last:pr-5'
  const td = 'px-2.5 py-3 text-right font-mono tabular-nums whitespace-nowrap first:pl-5 last:pr-5'
  const cols: [string, (m: ArenaAgent['metrics']) => ReactNode, boolean?][] = [
    ['Return', (m) => <PriceChange value={m?.totalReturnPct} decimals={1} />],
    ['Ann. return', (m) => <PriceChange value={m?.annualizedReturnPct} decimals={1} />, daily],
    ['vs BTC', (m) => <PriceChange value={m?.vsBtcPct} decimals={1} />],
    ['Sharpe', (m) => fmtNum(m?.sharpe)],
    ['Sortino', (m) => fmtNum(m?.sortino)],
    ['Calmar', (m) => fmtNum(m?.calmar), daily],
    ['Max DD', (m) => fmtDD(m?.maxDrawdownPct)],
    ['Win rate', (m) => fmtPct(m?.winRatePct, { decimals: 0 })],
    ['Turnover', (m) => fmtPct(m?.turnoverPct, { decimals: 0 })],
    ['Exposure', (m) => fmtPct(m?.exposurePct, { decimals: 0 })],
    ['Fees', (m) => fmtUsd(m?.feesUsd, { decimals: 0 })],
  ]
  const shown = cols.filter(([, , on]) => on !== false)

  return (
    <Card className="mt-6 overflow-hidden">
      <CardHeader label="Leaderboard · ranked by total return" title="Who traded it best" />
      <div className="relative overflow-x-auto border-t border-border">
        <table className="w-full min-w-[880px] text-[12.5px]">
          <thead className="bg-surface-2/70">
            <tr>
              <th className={th}>#</th>
              <th className={cx(th, 'text-left!')}>Agent</th>
              {shown.map(([label]) => (
                <th key={label} className={th}>
                  {label}
                </th>
              ))}
            </tr>
          </thead>
          <tbody className="divide-y divide-border">
            {rows.map((r) => (
              <tr key={r.key} className={cx(r.rank == null && 'bg-surface-2/35 text-muted')}>
                <td className={cx(td, 'text-left! text-muted')}>{r.rank ?? '—'}</td>
                <td className="px-2.5 py-3 whitespace-nowrap">
                  <span className="inline-flex items-center gap-2">
                    <span aria-hidden="true" className="h-2.5 w-1 rounded-full" style={{ background: r.color }} />
                    <span className={cx('font-medium', r.rank == null ? 'text-muted' : 'text-text')}>{r.name}</span>
                    {r.agent ? <Badge tone="outline">{PERSONA[r.agent.personaId] ?? r.agent.personaId}</Badge> : <Badge>Baseline</Badge>}
                    {r.agent?.status === 'benched' && (
                      <Badge tone="down" title={`Benched after ${r.agent.errors} failed steps; it holds from then on.`}>
                        Benched
                      </Badge>
                    )}
                  </span>
                </td>
                {shown.map(([label, cell]) => (
                  <td key={label} className={cx(td, r.rank != null && 'text-text')}>
                    {cell(r.metrics)}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <p className="border-t border-border px-5 py-3 text-[12.5px] text-muted">
        Single run — small gaps are noise. Baselines buy at the first open and hold; they are not ranked.
        {daily
          ? ' Annualized return and Calmar need 90+ days, and win rate a closed sell; until then they read —.'
          : ' Annualized return and Calmar need 90+ days, so 4-hour runs leave them out.'}
      </p>
    </Card>
  )
}

// ── Decision log ────────────────────────────────────────────────────────────

export function DecisionLog({
  run,
  agent,
  onAgent,
  steps,
  error,
  onRetry,
}: {
  run: ArenaRun
  agent: number
  onAgent: (i: number) => void
  steps: ArenaStep[] | undefined // undefined until the first page arrives
  error: string | null
  onRetry: () => void
}) {
  const a = run.agents[agent]
  const newest = steps ? [...steps].reverse() : []

  return (
    <Card className="mt-6 overflow-hidden">
      <CardHeader label="Decision log · newest first" title="What each agent did" />
      {run.agents.length > 1 && (
        <div className="overflow-x-auto px-5 pb-3">
          <Tabs
            size="sm"
            value={String(agent)}
            onChange={(v) => onAgent(Number(v))}
            items={run.agents.map((x) => ({
              value: String(x.index),
              label: (
                <span className="inline-flex items-center gap-1.5">
                  <span aria-hidden="true" className="size-2 rounded-full" style={{ background: agentColor(x.index) }} />
                  {x.name}
                </span>
              ),
            }))}
            aria-label="Agent"
          />
        </div>
      )}
      {a?.instruction && (
        <blockquote className="mx-5 mb-3 rounded-xl border-l-2 border-accent bg-surface-2/70 px-4 py-2.5 text-[13.5px] leading-relaxed whitespace-pre-wrap text-text">
          <span className="label mr-2">Mandate</span>
          {a.instruction}
        </blockquote>
      )}
      {error ? (
        <div role="alert" className="flex flex-wrap items-center gap-3 border-t border-border px-5 py-4 text-[13px] text-down">
          {error}
          <Button size="sm" variant="secondary" onClick={onRetry}>
            Try again
          </Button>
        </div>
      ) : !steps ? (
        <div aria-busy="true" aria-label="Loading decisions" className="border-t border-border px-5 py-4">
          <Skeleton className="h-4 w-48" />
          <Skeleton className="mt-3 h-12 w-full" />
        </div>
      ) : !newest.length ? (
        <p className="border-t border-border px-5 py-8 text-center text-sm text-muted">
          {run.status === 'running' ? 'No decisions yet. The first step is being decided…' : 'This agent made no decisions.'}
        </p>
      ) : (
        <ol key={agent} aria-label={`${a?.name ?? 'Agent'} decisions`} className="divide-y divide-border border-t border-border">
          {newest.map((s, i) => (
            <StepCard key={s.step} s={s} prev={steps[steps.length - 2 - i]} run={run} />
          ))}
        </ol>
      )}
    </Card>
  )
}

function StepCard({ s, prev, run }: { s: ArenaStep; prev: ArenaStep | undefined; run: ArenaRun }) {
  const base = prev?.equity ?? run.startingCash
  const chips = targetDiff(s, prev)
  const fills = s.fills ?? []
  const tools = s.tools ?? []

  return (
    <li className="px-5 py-4">
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <span className="font-mono text-[12px] font-medium text-text">Step {s.step + 1}</span>
        <span className="font-mono text-[11.5px] text-muted">sim {simAt(s.t, run.cadence)}</span>
        <span className="ml-auto inline-flex items-baseline gap-2 font-mono text-[12.5px] tabular-nums">
          {fmtUsd(s.equity, { decimals: 0 })}
          <PriceChange value={base > 0 ? (s.equity / base - 1) * 100 : null} />
        </span>
      </div>

      {s.error ? (
        <p className="mt-2 text-[13px] text-down">
          Held — model error <span className="text-muted">· {s.error}</span>
        </p>
      ) : s.targets == null ? (
        <p className="mt-2 text-[13px] text-muted">Held — no orders.</p>
      ) : (
        <ul aria-label="Target changes" className="mt-2 flex flex-wrap gap-1.5">
          {chips.map((c) => (
            <li key={c} className="rounded-md bg-accent-soft px-2 py-0.5 font-mono text-[11.5px] text-accent-ink">
              {c}
            </li>
          ))}
        </ul>
      )}

      {fills.length > 0 && (
        <div className="mt-3 max-w-2xl overflow-x-auto rounded-lg border border-border">
          <table className="w-full min-w-[440px] text-[12px]">
            <thead className="bg-surface-2/70">
              <tr className="label text-right [&>th]:px-2.5 [&>th]:py-1.5 [&>th]:font-medium">
                <th className="text-left">Fill</th>
                <th>Qty</th>
                <th>Price</th>
                <th>Notional</th>
                <th>Fee</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border font-mono tabular-nums">
              {fills.map((f, i) => (
                <tr key={`${f.symbol}-${i}`} className="text-right [&>td]:px-2.5 [&>td]:py-1.5">
                  <td className="text-left">
                    <span className={f.side === 'buy' ? 'text-up' : 'text-down'}>{f.side === 'buy' ? '▲ Buy' : '▼ Sell'}</span> {f.symbol}
                  </td>
                  <td>{fmtQty(f.qty)}</td>
                  <td>{fmtPrice(f.price)}</td>
                  <td>{fmtUsd(f.notional, { decimals: 0 })}</td>
                  <td className="text-muted">{fmtUsd(f.fee)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {tools.length > 0 && (
        <details className="group mt-3">
          <summary className="label inline-flex list-none items-center gap-1 rounded hover:text-text [&::-webkit-details-marker]:hidden">
            <span aria-hidden="true" className="transition-transform group-open:rotate-90">
              ›
            </span>
            {tools.length} tool {tools.length === 1 ? 'call' : 'calls'}
          </summary>
          <ul className="mt-2 flex flex-wrap gap-1.5">
            {tools.map((t, i) => (
              <li
                key={i}
                className={cx(
                  'max-w-full rounded-md border px-2 py-0.5 font-mono text-[11px] break-words',
                  t.ok ? 'border-border text-text' : 'border-down/40 text-down',
                )}
              >
                {toolLabel(t)} <span className="text-muted">→ {t.summary}</span>
              </li>
            ))}
          </ul>
        </details>
      )}

      {/* Plain text on purpose: the model can echo untrusted headlines, links or markup. */}
      {s.reasoning && <p className="mt-3 text-[13.5px] leading-relaxed break-words whitespace-pre-wrap text-text">{s.reasoning}</p>}
    </li>
  )
}

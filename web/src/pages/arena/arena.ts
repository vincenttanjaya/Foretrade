// Pure helpers for the AI backtest arena. Checked by arena.test.ts.
import { fmtDate } from '../../lib/format'
import type { ArenaAgent, ArenaBenchmark, ArenaCadence, ArenaMetrics, ArenaOptions, ArenaStep } from '../../lib/types'
import { PALETTE } from '../strategies/strategy'

const DAY = 86400
export const STEP_SECONDS: Record<ArenaCadence, number> = { '1d': DAY, '4h': 4 * 3600 }
export const CADENCE_LABEL: Record<ArenaCadence, string> = { '1d': 'Daily', '4h': '4-hour' }
export const MIN_INSTRUCTION = 10
export const MAX_INSTRUCTION = 1000

/** Agents take the strategy palette by index; the baselines stay grey so the agents read first. */
export const agentColor = (index: number) => PALETTE[index % PALETTE.length]
export const BASELINE_COLOR: Record<ArenaBenchmark['id'], string> = { btc: '#64748B', basket: '#A8A29E' }

/** Credits a run costs: ⌈agents × steps ÷ 2⌉, half a credit per agent-step (contract). */
export const cost = (agents: number, steps: number) => Math.ceil((agents * steps) / 2)

/** Rough wall-clock minutes, at about 4 s per step (measured 1.3–5.4 s); agents decide a step in parallel. */
export const etaMin = (steps: number) => Math.max(1, Math.round(steps / 15))

const dayOf = (iso: string) => (/^\d{4}-\d{2}-\d{2}$/.test(iso) ? Date.parse(`${iso}T00:00:00Z`) / 1000 : NaN)

/** "2026-08-03" + 3 → "2026-08-06" (UTC calendar days); "" for a bad date. */
export function addDays(iso: string, n: number) {
  const t = dayOf(iso)
  return Number.isFinite(t) ? new Date((t + n * DAY) * 1000).toISOString().slice(0, 10) : ''
}

/** Days in the window, both ends inclusive; 0 when a date is missing or the end is before the start. */
export function daysIn(start: string, end: string) {
  const d = (dayOf(end) - dayOf(start)) / DAY + 1
  return d >= 1 ? d : 0
}

/** Steps (= decisions per agent) in the window: one per day, or six per day for 4h. */
export const stepsFor = (cadence: ArenaCadence, start: string, end: string) => (daysIn(start, end) * DAY) / STEP_SECONDS[cadence]

/** The model may remember a window that starts on or before its training cutoff. */
export const isContaminated = (start: string, cutoff: string) => !!start && !!cutoff && start <= cutoff

/** What blocks the Start button, or null. Only the first problem is shown. */
export function setupProblem(
  f: { cadence: ArenaCadence; start: string; end: string; agents: number; instruction: string | null },
  opts: ArenaOptions,
): string | null {
  const cad = opts.cadences.find((c) => c.cadence === f.cadence)
  const days = daysIn(f.start, f.end)
  if (!dayOf(f.start) || !dayOf(f.end)) return 'Pick a start and an end date.'
  if (f.end > opts.latestEnd) return `The window must end by ${fmtDate(opts.latestEnd)}, the last full day.`
  if (!days) return 'The start must come before the end.'
  if (cad && (days < cad.minDays || days > cad.maxDays))
    return `${CADENCE_LABEL[f.cadence]} runs cover ${cad.minDays}–${cad.maxDays} days; this window is ${days}.`
  if (stepsFor(f.cadence, f.start, f.end) > opts.maxSteps) return `A run has at most ${opts.maxSteps} steps.`
  if (f.agents < 1) return 'Pick at least one agent.'
  if (f.agents > opts.maxAgents) return `Up to ${opts.maxAgents} agents per run.`
  if (f.instruction !== null && f.instruction.trim().length < MIN_INSTRUCTION)
    return `Give your agent an instruction of at least ${MIN_INSTRUCTION} characters.`
  return null
}

/** "Aug 3 → Oct 1, 2026"; the first year is dropped when both dates share it. */
export function windowLabel(start: string, end: string) {
  const a = fmtDate(start)
  const b = fmtDate(end)
  return `${start.slice(0, 4) === end.slice(0, 4) ? a.replace(/, \d{4}$/, '') : a} → ${b}`
}

/** "Daily · Aug 3 → Oct 1, 2026", or null when the run's name already says it (the server's default name does). */
export function windowText(r: { name: string; cadence: ArenaCadence; start: string; end: string }) {
  const w = windowLabel(r.start, r.end)
  return r.name.includes(w) ? null : `${CADENCE_LABEL[r.cadence] ?? r.cadence} · ${w}`
}

/** A simulated time as UTC text: "2026-08-25" for daily runs, "2026-08-25 16:00" for 4h. */
export const simAt = (t: number, cadence: ArenaCadence) =>
  Number.isFinite(t) ? new Date(t * 1000).toISOString().slice(0, cadence === '1d' ? 10 : 16).replace('T', ' ') : '—'

/** Decision time of the last finished step, or null before the first one finishes. */
export const lastStepAt = (run: { cadence: ArenaCadence; start: string; stepsDone: number }) =>
  run.stepsDone > 0 ? dayOf(run.start) + (run.stepsDone - 1) * STEP_SECONDS[run.cadence] : null

export type Row = { key: string; name: string; metrics: ArenaMetrics; rank: number | null; color: string; agent?: ArenaAgent }

/**
 * Leaderboard rows: agents by total return (ties: smaller drawdown), ranked 1..n, then the baselines,
 * which are never ranked.
 */
export function rankRows(agents: ArenaAgent[], benchmarks: ArenaBenchmark[]): Row[] {
  const ret = (a: ArenaAgent) => a.metrics?.totalReturnPct ?? 0
  const dd = (a: ArenaAgent) => a.metrics?.maxDrawdownPct ?? 0
  const ranked = [...agents].sort((a, b) => ret(b) - ret(a) || dd(a) - dd(b))
  return [
    ...ranked.map((a, i) => ({ key: `a${a.index}`, name: a.name, metrics: a.metrics, rank: i + 1, color: agentColor(a.index), agent: a })),
    ...benchmarks.map((b) => ({ key: b.id, name: b.name, metrics: b.metrics, rank: null, color: BASELINE_COLOR[b.id] ?? BASELINE_COLOR.btc })),
  ]
}

/**
 * Target-change chips ("BTC 33→50%"): each target weight against the weight the agent saw, i.e. the
 * previous step's close (0 before the first step).
 */
export function targetDiff(step: ArenaStep, prev?: ArenaStep): string[] {
  return (step.targets ?? []).map((t) => {
    const from = prev?.positions?.find((p) => p.symbol === t.symbol)?.weightPct ?? 0
    return `${t.symbol} ${Math.round(from)}→${Math.round(t.weightPct)}%`
  })
}

/** Tool call as a chip: `get_news "ETF flows" 2`, `get_prices BTC 4h 30`; nested arguments are left out. */
export function toolLabel(t: ArenaStep['tools'][number]) {
  const args = Object.entries(t.input ?? {}).flatMap(([k, v]) =>
    k === 'query' ? [`"${v}"`] : typeof v === 'string' || typeof v === 'number' ? [String(v)] : [],
  )
  return [t.name, ...args].join(' ')
}

// "What has to happen" (API contract round 9b): the valuation and tail scenarios behind a numeric report. Levels and
// inputs come from the tools' own records, odds from the desk's final distribution over each scenario's range, so a
// scenario can never contradict the scored buckets; the model's own weight shows only when it differs. Thesis and
// conditions are model-written: plain text, and only ids that resolve in this run become links.
import type { ReactNode } from 'react'
import { Badge, type BadgeTone } from '../../components/ui/Badge'
import { Card } from '../../components/ui/Card'
import { cx } from '../../lib/cx'
import type { DeskScenario, DeskUnit, ForecastFinding, ForecastSource, ScenarioInput } from '../../lib/types'
import { fmtAnnual, fmtChange, fmtLevel, httpUrl, rangeText, roundPcts } from './forecast'

const KIND_TONE: Record<string, BadgeTone> = { tail: 'outline', bear: 'down', base: 'neutral', bull: 'up' }

/** Where an id in a scenario points: a source's article, a finding's first linked source, or the measured history. */
export type IdTargets = { sources: Map<string, ForecastSource>; findings: Map<string, ForecastFinding>; measured: boolean }

function target(id: string, t: IdTargets): { href: string; title: string; external: boolean } | null {
  if (id === 'H1') return t.measured ? { href: '#measured', title: 'Measured history', external: false } : null
  if (id.startsWith('S')) {
    const s = t.sources.get(id)
    const url = httpUrl(s?.url)
    return s && url ? { href: url.href, title: s.title || s.source || id, external: true } : null
  }
  const f = t.findings.get(id)
  const src = f?.sourceIds.map((sid) => t.sources.get(sid)).find((s) => httpUrl(s?.url))
  return f && src ? { href: httpUrl(src.url)!.href, title: f.claim, external: true } : null
}

/** An id as a link when it resolves, else as text. */
export function IdLink({ id, targets }: { id: string; targets: IdTargets }) {
  const t = target(id, targets)
  if (!t) return <>{id}</>
  if (!t.external)
    return (
      <a href={t.href} title={t.title} className="rounded-sm font-medium text-accent-ink hover:underline">
        {id}
      </a>
    )
  return (
    <a href={t.href} target="_blank" rel="noopener noreferrer" title={t.title} className="rounded-sm font-medium text-accent-ink hover:underline">
      {id}
      <span className="sr-only"> (opens in a new tab)</span>
    </a>
  )
}

/** Model-written text (a thesis, a condition) with the ids the scenario cites (and the run has) turned into links. */
function Linked({ text, ids, targets }: { text: string; ids: Set<string>; targets: IdTargets }) {
  const parts = text.split(/\b([SFH]\d{1,3})\b/)
  return (
    <>
      {parts.map((p, i): ReactNode => (i % 2 === 1 && ids.has(p) ? <IdLink key={i} id={p} targets={targets} /> : p))}
    </>
  )
}

const plain = (n: number, d = 2) => n.toLocaleString('en-US', { maximumFractionDigits: d })

/** "FY2027 EPS $21.33", "EPS growth 2028–2032 12%/yr", "Exit P/E 20×", "Level $752", "Odds 5% over the horizon", "Move -60%". */
export function inputText(i: ScenarioInput, siblings: ScenarioInput[], unit: DeskUnit) {
  switch (i.field) {
    case 'eps':
      return `${i.year ? `FY${i.year} ` : ''}EPS ${unit.prefix}${i.value.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`
    case 'growthPct': {
      const from = siblings.find((x) => x.field === 'eps')?.year
      const span = i.year ? (from && from < i.year ? ` ${from + 1}–${i.year}` : ` to FY${i.year}`) : ''
      return `EPS growth${span} ${plain(i.value, 1)}%/yr`
    }
    case 'pe':
      return `Exit P/E ${plain(i.value, 1)}×`
    case 'level':
      return `Level ${fmtLevel(i.value, unit)}`
    case 'cagrPct':
      return `Return ${fmtAnnual(i.value)}/yr`
    case 'eventP': // a stress event's probability, 0..1
      return `Odds ${plain(i.value * 100, 1)}% over the horizon`
    case 'movePct':
      return `Move ${fmtChange(i.value, true)}`
    default:
      return `${i.field} ${plain(i.value)}`
  }
}

const CHIP: Record<ScenarioInput['kind'], string> = {
  source: 'border-accent/40 bg-accent-soft/60 text-text',
  model_knowledge: 'border-dashed border-border text-muted',
  assumption: 'border-border text-muted',
}

/** How an input reads: a source only when the server verified the cited text states it; anything else is an assumption. */
const chipKind = (i: ScenarioInput): ScenarioInput['kind'] => (i.kind === 'source' ? (i.verified ? 'source' : 'assumption') : CHIP[i.kind] ? i.kind : 'assumption')

function Basis({ input, targets }: { input: ScenarioInput; targets: IdTargets }) {
  const kind = chipKind(input)
  if (kind === 'model_knowledge') {
    const asOf = input.basis.match(/\b(?:19|20)\d\d(?:-\d\d)?\b/)?.[0]
    return <>model knowledge{asOf && ` ${asOf}`}</>
  }
  if (kind === 'assumption') return <>assumption</>
  return /^[SFH]\d{1,3}$/.test(input.basis) ? <IdLink id={input.basis} targets={targets} /> : <>{input.basis}</>
}

export function InputChips({ inputs, unit, targets, label }: { inputs: ScenarioInput[]; unit: DeskUnit; targets: IdTargets; label: string }) {
  if (!inputs.length) return null
  return (
    <ul aria-label={label} className="flex flex-wrap gap-1">
      {inputs.map((i, k) => (
        <li key={k} className={cx('max-w-full rounded-md border px-1.5 py-0.5 font-mono text-[11px] break-words tabular-nums', CHIP[chipKind(i)])}>
          {inputText(i, inputs, unit)} · <Basis input={i} targets={targets} />
        </li>
      ))}
    </ul>
  )
}

const GRID: Record<number, string> = { 2: 'md:grid-cols-2', 3: 'md:grid-cols-3', 4: 'md:grid-cols-2 xl:grid-cols-4', 5: 'md:grid-cols-3 xl:grid-cols-5' }

export function Scenarios({ scenarios, unit, targets }: { scenarios: DeskScenario[]; unit: DeskUnit; targets: IdTargets }) {
  if (scenarios.length < 2) return null
  const pcts = roundPcts(scenarios.map((s) => s.p))
  return (
    <section aria-labelledby="desk-scenarios" className="mt-12">
      <div className="label">Scenarios · {scenarios.length}</div>
      <h2 id="desk-scenarios" className="mt-1.5 font-display text-[28px] leading-tight font-medium text-text">
        What has to happen
      </h2>
      <p className="mt-1.5 max-w-2xl text-[14px] leading-relaxed text-muted">
        Odds come from the desk's final distribution over each scenario's range; the model's own weights are shown when they differ.
      </p>
      <ul className={cx('mt-5 grid gap-4', GRID[scenarios.length] ?? 'md:grid-cols-3')}>
        {scenarios.map((s, i) => (
          <ScenarioCard key={`${s.kind}-${i}`} s={s} pct={pcts[i]} unit={unit} targets={targets} />
        ))}
      </ul>
    </section>
  )
}

function ScenarioCard({ s, pct, unit, targets }: { s: DeskScenario; pct: number; unit: DeskUnit; targets: IdTargets }) {
  const model = s.modelP == null ? null : Math.round(s.modelP * 100)
  const ids = new Set(s.sourceIds)
  const range = rangeText(s.lo, s.hi, unit)
  const level = [`≈ ${fmtLevel(s.level, unit)}`, s.vsRefPct != null && fmtChange(s.vsRefPct), s.annualPct != null && `${fmtAnnual(s.annualPct)}/yr`].filter(Boolean).join(' · ')
  return (
    <li className="min-w-0 break-words print:break-inside-avoid">
      <Card className="flex h-full min-w-0 flex-col p-5">
        <div className="flex items-center justify-between gap-2">
          <Badge tone={KIND_TONE[s.kind] ?? 'neutral'}>{s.kind}</Badge>
          {range && <span className="min-w-0 truncate font-mono text-[11px] text-muted tabular-nums">{range}</span>}
        </div>
        <h3 className="mt-2.5 font-display text-[18px] leading-snug font-medium text-pretty text-text">{s.name}</h3>
        <div className="mt-2 flex flex-wrap items-baseline gap-x-2.5">
          <span className="font-display text-[40px] leading-none text-text tabular-nums">
            {pct}
            <span className="ml-0.5 align-top text-[0.45em] text-muted">%</span>
          </span>
          {model != null && Math.abs(model - pct) >= 5 && (
            <span className="font-mono text-[11.5px] text-muted tabular-nums" title="The scenario model's own weight">
              model {model}%
            </span>
          )}
        </div>
        <p className="mt-1.5 font-mono text-[12px] text-text tabular-nums">{level}</p>
        {s.thesis && (
          <p className="mt-3 text-[13.5px] leading-relaxed text-pretty text-text">
            <Linked text={s.thesis} ids={ids} targets={targets} />
          </p>
        )}
        {s.mustBeTrue.length > 0 && (
          <div className="mt-3.5 border-t border-border pt-3">
            <div className="label">What must be true</div>
            <ul className="mt-1.5 flex flex-col gap-1.5">
              {s.mustBeTrue.map((m, i) => (
                <li key={i} className="flex gap-2 text-[13px] leading-snug text-text">
                  <span aria-hidden="true" className="text-accent">
                    •
                  </span>
                  <span className="min-w-0">
                    <Linked text={m} ids={ids} targets={targets} />
                  </span>
                </li>
              ))}
            </ul>
          </div>
        )}
        {s.inputs.length > 0 && (
          <div className="mt-3.5">
            <div className="label mb-1.5">Inputs</div>
            <InputChips inputs={s.inputs} unit={unit} targets={targets} label={`${s.name} inputs`} />
          </div>
        )}
      </Card>
    </li>
  )
}

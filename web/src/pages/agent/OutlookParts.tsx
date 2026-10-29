// Parts of the price_outlook card (contract "Price outlook view", round 9a): the by-when table, the scenario table and
// the folded volatility-only baseline. Kept apart so cards.tsx stays readable.
import type { ReactNode } from 'react'
import { Badge, type BadgeTone } from '../../components/ui/Badge'
import { cx } from '../../lib/cx'
import { fmtDate, fmtLevelPrice, fmtPct, fmtProb } from '../../lib/format'
import type { OutlookMultiple, OutlookScenario, OutlookView, PriceOutlook, ScenarioInput, SourceRef, TimeToTarget } from '../../lib/types'

/** A horizon past a year in 365-day years (as the API's longHorizon), to one decimal: 3650 → 10, 548 → 1.5. */
export const horizonYears = (days: number) => Math.round((days / 365) * 10) / 10
export const plural = (n: number, unit: string) => `${n} ${n === 1 ? unit : `${unit}s`}`

/** "0.5×", "10×". */
export const fmtX = (x: number) => `${x.toLocaleString('en-US')}×`

/** A % move, whole percents from ±10%: "+327%", "−57%", "+4.2%". */
export const fmtMove = (pct: number | null | undefined) => fmtPct(pct, { sign: true, decimals: pct != null && Math.abs(pct) >= 10 ? 0 : 1 })

/** % distance of a level from the reference price, to one decimal: "−4.2%". */
export const distance = (level: number, ref: number) => fmtPct((level / ref - 1) * 100, { sign: true, decimals: 1 })

/** % distance of a level from the reference price, whole percents from ±10%: "+327%". */
export const moveFrom = (level: number, ref: number) => fmtMove((level / ref - 1) * 100)

/** "about 11 years", "about 2.8 years": whole years from 10. */
export const aboutYears = (y: number) => `about ${plural(y >= 10 ? Math.round(y) : Math.round(y * 10) / 10, 'year')}`

const year = (iso: string | null | undefined) => (iso && /^\d{4}/.test(iso) ? iso.slice(0, 4) : null)

const th = 'label py-1.5 font-medium align-bottom'

/** A by-scenario path's name: its kind for the agent's scenarios, else the one path the view is (halved or zero drift). */
const pathName = (kind: string, view: OutlookView['kind'] | undefined) => (view === 'historical_drift' ? 'halved drift' : view === 'none' ? 'zero drift' : kind)

/**
 * "By when": the odds of ending at or touching multiple × spot within each number of years under the view, the
 * volatility-only baseline's alongside, the years under each scenario and the annual return each horizon needs.
 * `lead` names the view the odds rest on, for a card with no at-horizon hero to say it.
 */
export function TimeToTargetTable({ data, symbol, view, lead }: { data: TimeToTarget; symbol: string; view?: OutlookView; lead?: boolean }) {
  const rows = Array.isArray(data.rows) ? data.rows : []
  const byScenario = Array.isArray(data.byScenario) ? data.byScenario : []
  const ladder = Array.isArray(data.requiredCagr) ? data.requiredCagr : []
  const x = fmtX(data.multiple)
  // The median past twice the scenarios' horizon assumes their returns go on (the API's medianExtrapolated): "≈20 years".
  const medianX = data.medianExtrapolated === true && data.medianYears != null
  const extrapolated = medianX || rows.some((r) => r.extrapolated)
  return (
    <section aria-label="By when" className="mx-4 mt-5">
      <div className="label">By when · {x}</div>
      {lead && view?.label && (
        <p className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-[12px] text-muted">
          Under{' '}
          {view.kind !== 'scenarios' && <Badge tone="outline">{view.kind === 'none' ? 'No view' : 'Assumption'}</Badge>}{' '}
          <span className="font-medium text-text">{view.label}</span>
        </p>
      )}
      <p className="mt-1 text-[14.5px] leading-snug font-medium text-pretty text-text">
        Median time to {x} ({fmtLevelPrice(data.target)}):{' '}
        {data.medianYears == null ? 'not within 30 years' : medianX ? `${aboutYears(data.medianYears).replace('about ', '≈')} (extrapolated)` : aboutYears(data.medianYears)}
      </p>
      {rows.length > 0 && (
        <table className="mt-2 w-full text-[13px]">
          <caption className="sr-only">{`Chance of reaching ${x} ${symbol} within each number of years`}</caption>
          <thead className="border-b border-border">
            <tr>
              <th scope="col" className={cx(th, 'pr-2 text-left')}>
                Within
              </th>
              <th scope="col" className={cx(th, 'px-2 text-right')}>
                Ends ≥ {x}
              </th>
              <th scope="col" className={cx(th, 'px-2 text-right')}>
                Touches
              </th>
              <th scope="col" className={cx(th, 'pl-2 text-right')}>
                Baseline
              </th>
            </tr>
          </thead>
          <tbody className="divide-y divide-border">
            {rows.map((r) => {
              const approx = r.extrapolated ? '≈' : ''
              return (
                <tr key={r.years}>
                  <th scope="row" className="py-2 pr-2 text-left font-normal whitespace-nowrap">
                    <span className="font-mono text-text">{plural(r.years, 'yr')}</span>
                    {r.extrapolated && <span className="block font-mono text-[10.5px] text-muted">extrapolated</span>}
                  </th>
                  <td className="px-2 py-2 text-right font-mono font-medium text-text tabular-nums">
                    {approx}
                    {fmtProb(r.pEnd)}
                  </td>
                  <td className="px-2 py-2 text-right font-mono text-text tabular-nums">
                    {approx}
                    {fmtProb(r.pTouch)}
                  </td>
                  <td className="py-2 pl-2 text-right font-mono text-muted tabular-nums">{fmtProb(r.baselinePEnd)}</td>
                </tr>
              )
            })}
          </tbody>
        </table>
      )}
      <p className="mt-1.5 text-[12px] leading-snug text-pretty text-muted">
        Ends: at or above {fmtLevelPrice(data.target)} at that date. Touches: trades there at any point before. Baseline: ends there under the
        volatility-only model.{extrapolated && ' ≈: past twice the scenarios’ horizon, each scenario’s annual return is assumed to persist.'}
      </p>

      {byScenario.length > 0 && (
        <ul aria-label="Years to the target per scenario" className="mt-2.5 flex flex-wrap gap-1.5">
          {byScenario.map((s, i) => (
            <li key={i} className="rounded-lg border border-border bg-surface-2/60 px-2 py-1 font-mono text-[11.5px] text-text tabular-nums">
              <span className="font-medium capitalize">{pathName(s.kind, view?.kind)}</span> {fmtPct(s.annualPct, { sign: true, decimals: 1 })}/yr →{' '}
              {s.years == null ? 'never' : aboutYears(s.years).replace('about ', '')}
            </li>
          ))}
        </ul>
      )}

      {ladder.length > 0 && (
        <p className="mt-2.5 font-mono text-[11.5px] leading-relaxed text-muted tabular-nums">
          <span className="text-text">Needs</span>{' '}
          {ladder.map((r, i) => (i === 0 ? `${fmtPct(r.pct, { decimals: 1 })}/yr for ${plural(r.years, 'year')}` : `${fmtPct(r.pct, { decimals: 1 })} for ${r.years}`)).join(' · ')}
          {data.ownCagrPct != null && (
            <>
              {' · '}
              <span className="text-text">
                {symbol}&apos;s own CAGR{year(data.ownSince) && ` since ${year(data.ownSince)}`}: {fmtPct(data.ownCagrPct, { sign: true, decimals: 1 })}/yr
              </span>
            </>
          )}
        </p>
      )}
    </section>
  )
}

const KIND_TONE: Record<string, BadgeTone> = { bull: 'up', base: 'neutral', bear: 'down', tail: 'down' }

/** A tail's tone is its side: above every other scenario (a multi-bagger's super-cycle) it is up, else down (a war, a bust). */
const kindTone = (s: OutlookScenario, all: OutlookScenario[]): BadgeTone =>
  s.kind === 'tail' && all.some((o) => o.kind !== 'tail') && all.every((o) => o.kind === 'tail' || s.level > o.level)
    ? 'up'
    : (KIND_TONE[s.kind] ?? 'neutral')

/** "FY2027 EPS $21.33", "EPS growth 12%/yr", "P/E 20×", "Level $752", "Return +9%/yr". */
function inputText(i: ScenarioInput) {
  switch (i.field) {
    case 'eps':
      return `${i.year ? `FY${i.year} ` : ''}EPS ${fmtLevelPrice(i.value)}`
    case 'growthPct':
      return `EPS growth ${fmtPct(i.value, { decimals: Number.isInteger(i.value) ? 0 : 1 })}/yr${i.year ? ` to FY${i.year}` : ''}`
    case 'pe':
      return `P/E ${fmtX(i.value)}`
    case 'level':
      return `Level ${fmtLevelPrice(i.value)}`
    case 'cagrPct':
      return `Return ${fmtPct(i.value, { sign: true, decimals: Number.isInteger(i.value) ? 0 : 1 })}/yr`
    default:
      return `${i.field} ${i.value.toLocaleString('en-US')}`
  }
}

const CHIP: Record<ScenarioInput['kind'], string> = {
  source: 'border-transparent bg-surface-2 text-text',
  model_knowledge: 'border-dashed border-border text-muted',
  assumption: 'border-border text-muted',
}

/** How an input reads: a source only when the API verified the cited text states it, else an assumption. */
const chipKind = (i: ScenarioInput): ScenarioInput['kind'] => (i.kind === 'source' && !i.verified ? 'assumption' : CHIP[i.kind] ? i.kind : 'assumption')

/** The basis of one input: a source number links to that source; model knowledge keeps its as-of month. */
function Basis({ input, sources }: { input: ScenarioInput; sources?: Map<number, SourceRef> }) {
  const kind = chipKind(input)
  if (kind === 'model_knowledge') {
    const asOf = input.basis.match(/\b(?:19|20)\d\d(?:-\d\d)?\b/)?.[0]
    return <>model knowledge{asOf && ` ${asOf}`}</>
  }
  if (kind === 'assumption') return <>assumption</>
  const n = Number(input.basis.match(/^\[(\d{1,2})\]$/)?.[1])
  const src = n ? sources?.get(n) : undefined
  if (!src) return <>{input.basis}</>
  return (
    <a href={src.url} target="_blank" rel="noopener noreferrer" title={`${src.n}. ${src.title} (${src.source})`} className="font-medium text-accent-ink hover:underline">
      [{src.n}]
    </a>
  )
}

/** The agent's scenarios at the horizon: weight, level and what went into it, each input with its basis. */
export function ScenarioTable({ scenarios, spot, sources }: { scenarios: OutlookScenario[]; spot: number; sources?: Map<number, SourceRef> }) {
  return (
    <section aria-label="Scenarios" className="mx-4 mt-5">
      <div className="label">Scenarios · subjective weights</div>
      <ul className="mt-1.5 divide-y divide-border border-y border-border">
        {scenarios.map((s, i) => (
          <li key={i} className="py-2.5">
            <div className="flex items-start gap-2">
              <Badge tone={kindTone(s, scenarios)} className="mt-px">
                {s.kind}
              </Badge>
              <span className="min-w-0 flex-1 text-[13.5px] leading-snug font-medium text-pretty text-text">{s.name}</span>
              <span className="shrink-0 text-right font-mono text-[13px] text-text tabular-nums">
                <span className="font-medium">{fmtLevelPrice(s.level)}</span>
                <span className="ml-1.5 text-muted">{fmtProb(s.p)}</span>
              </span>
            </div>
            <p className="mt-0.5 text-right font-mono text-[11px] text-muted tabular-nums">
              {fmtMove(Number.isFinite(s.changePct) ? s.changePct : (s.level / spot - 1) * 100)}
              {s.annualPct != null && ` · ${fmtPct(s.annualPct, { sign: true, decimals: 1 })}/yr`}
            </p>
            {Array.isArray(s.inputs) && s.inputs.length > 0 && (
              <ul aria-label={`${s.name} inputs`} className="mt-1 flex flex-wrap gap-1">
                {s.inputs.map((i, k) => (
                  <li key={k} className={cx('max-w-full rounded-md border px-1.5 py-0.5 font-mono text-[11px] tabular-nums', CHIP[chipKind(i)])}>
                    {inputText(i)} · <Basis input={i} sources={sources} />
                  </li>
                ))}
              </ul>
            )}
          </li>
        ))}
      </ul>
    </section>
  )
}

/** Where volAnnPct came from: "long-run volatility since Jul 17, 2018 (3,000 daily closes)". */
export function volBasisText(basis: PriceOutlook['volBasis']) {
  if (!basis) return null
  if (basis.window === '90d') return '90-day realized volatility'
  const closes = Number.isFinite(basis.days) ? ` (${basis.days.toLocaleString('en-US')} daily closes)` : ''
  return `long-run volatility since ${fmtDate(basis.since)}${closes}`
}

export function MultiplesTable({ rows, spot, caption }: { rows: OutlookMultiple[]; spot: number; caption: string }) {
  return (
    <table className="mt-1.5 w-full text-[13px]">
      <caption className="sr-only">{caption}</caption>
      <thead className="border-b border-border">
        <tr>
          <th scope="col" className={cx(th, 'pr-3 text-left')}>
            Level
          </th>
          <th scope="col" className={cx(th, 'px-2 text-right')}>
            At horizon
          </th>
          <th scope="col" className={cx(th, 'pl-2 text-right')}>
            Touched at any point
          </th>
        </tr>
      </thead>
      <tbody className="divide-y divide-border">
        {rows.map((m) => (
          <tr key={m.x}>
            <th scope="row" className="py-2 pr-3 text-left font-normal whitespace-nowrap">
              <span className="font-mono font-medium text-text">{fmtX(m.x)}</span>
              <span className="ml-1.5 font-mono text-[11px] text-muted tabular-nums">{fmtLevelPrice(m.x * spot)}</span>
            </th>
            <td className="px-2 py-2 text-right font-mono text-text tabular-nums">{fmtProb(m.pEnd)}</td>
            <td className="py-2 pl-2 text-right font-mono text-text tabular-nums">{fmtProb(m.pTouch)}</td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}

/** The three percentile tiles both card layouts use: label, level and its move from spot. */
export function QuantileTiles({ items, spot, className }: { items: { label: string; v: number }[]; spot: number; className?: string }) {
  return (
    <dl className={cx('grid grid-cols-3 divide-x divide-border rounded-xl border border-border bg-surface-2/60', className)}>
      {items.map((q) => (
        <div key={q.label} className="min-w-0 px-3 py-2.5 text-center">
          <dt className="label leading-tight">{q.label}</dt>
          <dd className="mt-1 truncate font-mono text-[13.5px] font-medium text-text tabular-nums">{fmtLevelPrice(q.v)}</dd>
          <dd className="font-mono text-[11px] text-muted tabular-nums">{distance(q.v, spot)}</dd>
        </div>
      ))}
    </dl>
  )
}

/**
 * The zero-drift volatility-only model, folded: median = today, its percentiles and multiples. A reference for how
 * wide the asset swings, never the answer.
 */
export function BaselineDetails({ data, span, children }: { data: PriceOutlook; span: string; children?: ReactNode }) {
  const multiples = Array.isArray(data.longHorizon?.multiples) ? data.longHorizon.multiples : []
  const basis = volBasisText(data.volBasis)
  return (
    <details className="group mx-4 mt-5 rounded-xl border border-dashed border-border">
      <summary className="flex cursor-pointer list-none items-center gap-2 px-3 py-2.5 text-[13px] font-medium text-text [&::-webkit-details-marker]:hidden">
        <span aria-hidden="true" className="font-mono text-muted transition-transform group-open:rotate-90">
          ›
        </span>
        Volatility-only baseline (zero drift): median = today
      </summary>
      <div className="px-3 pb-3">
        <p className="font-mono text-[10.5px] text-muted">Not a forecast of direction</p>
        <QuantileTiles
          className="mt-2"
          spot={data.spot}
          items={[
            { label: '5th percentile', v: data.p5 },
            { label: 'Median', v: data.p50 },
            { label: '95th percentile', v: data.p95 },
          ]}
        />
        {multiples.length > 0 && <MultiplesTable rows={multiples} spot={data.spot} caption={`Odds of each multiple of spot over ${span}, zero drift`} />}
        {children}
        <p className="mt-2 font-mono text-[11px] text-muted">
          {fmtPct(data.volAnnPct, { decimals: 0 })} annualized vol{basis && ` · ${basis}`}
          {data.volBasis?.source && ` · ${data.volBasis.source}`} · lognormal, zero drift
        </p>
      </div>
    </details>
  )
}

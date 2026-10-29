// The research ledger on a report (API contract round 8, "Deep research"): findings grouped by the direction they
// point, the aggregator's "What to watch" signals and (round 8b) the volatility-only reference the desk is checked
// against. Findings are model-written from untrusted web text, so they render as plain text; only the pack's own source
// URLs become links. Findings from the user's own files (round 9) link to their row in "Your materials" on this page
// instead.
import { useId, useState } from 'react'
import { Badge, type BadgeTone } from '../../components/ui/Badge'
import { Card } from '../../components/ui/Card'
import { IconAlert, IconChevron } from '../../components/ui/icons'
import { cx } from '../../lib/cx'
import type { Forecast, ForecastFinding, ForecastMaterial, ForecastResult, ForecastSource } from '../../lib/types'
import { ExternalLink } from './DeskVsMarket'
import { angleKindLabel, directionGroupOf, findingsOf, fmtAsOf, fmtChance, fmtDay, groupFindings, httpUrl, materialHref, materialsOf, type DirectionGroup } from './forecast'

const SHOWN = 6 // items a group shows before "Show all"
const COLLAPSE_CONTEXT_OVER = 12 // findings in total above which Context starts collapsed

export function EvidenceBase({ f, r }: { f: Pick<Forecast, 'shape' | 'outcomes' | 'materials'>; r: ForecastResult }) {
  const materials = materialsOf(f)
  const findings = findingsOf(r, materials)
  if (!findings.length) return null
  const groups = groupFindings(findings, f.shape, f.outcomes)
  const sources = new Map((r.sources ?? []).map((s) => [s.id, s]))
  const files = new Map(materials.map((m) => [m.id, m]))
  const angles = new Map((Array.isArray(r.research?.angles) ? r.research.angles : []).map((a) => [a.id, angleKindLabel(a.kind)]))
  const cited = new Set(findings.filter((x) => x.origin !== 'material').flatMap((x) => x.sourceIds)).size
  const yours = new Set(findings.filter((x) => x.origin === 'material').flatMap((x) => x.sourceIds)).size
  const from = [
    (cited > 0 || !yours) && `${cited} source${cited === 1 ? '' : 's'}`,
    yours > 0 && `${yours} of your materials`,
  ].filter(Boolean).join(' and ')
  return (
    <section aria-labelledby="evidence-base" className="mt-12">
      <div className="label">Evidence base · {findings.length}</div>
      <h2 id="evidence-base" className="mt-1.5 font-display text-[28px] leading-tight font-medium text-text">
        What the research found
      </h2>
      <p className="mt-1.5 max-w-2xl text-[14px] leading-relaxed text-muted">
        Atomic findings from {from}, grouped by the direction they point. Strongest first.
        {r.research?.status === 'carried' && ' From the previous run; may be stale.'}
      </p>
      <Mix groups={groups} total={findings.length} />
      <div className="mt-5 flex flex-col gap-4">
        {groups.map((g) => (
          <Group
            key={g.key}
            group={g}
            items={g.items}
            sources={sources}
            files={files}
            angles={angles}
            defaultOpen={!(g.key === 'context' && findings.length > COLLAPSE_CONTEXT_OVER)}
          />
        ))}
      </div>
    </section>
  )
}

/** A swatch per group: up and down in their tones, Context faint, any other bucket in accent shades. */
function swatches(groups: DirectionGroup[]) {
  const shades = ['bg-accent', 'bg-accent/55', 'bg-accent/30', 'bg-ink/45', 'bg-ink/25']
  let n = 0
  return new Map(
    groups.map((g) => [g.key, g.tone === 'up' ? 'bg-up' : g.tone === 'down' ? 'bg-down' : g.key === 'context' ? 'bg-muted/30' : shades[n++ % shades.length]]),
  )
}

/** One bar across the ledger: how many findings point each way. */
function Mix({ groups, total }: { groups: (DirectionGroup & { items: ForecastFinding[] })[]; total: number }) {
  const swatch = swatches(groups)
  const text = groups.map((g) => `${g.label} ${g.items.length}`).join(', ')
  return (
    <div className="mt-5 max-w-2xl">
      <div role="img" aria-label={`Findings by direction: ${text}`} className="flex h-2 gap-px overflow-hidden rounded-full bg-surface-2">
        {groups.map((g) => (
          <span key={g.key} className={cx('h-full origin-left animate-bar', swatch.get(g.key))} style={{ width: `${(g.items.length / total) * 100}%` }} />
        ))}
      </div>
      <ul aria-hidden="true" className="mt-2 flex flex-wrap gap-x-4 gap-y-1 font-mono text-[10.5px] tracking-wide text-muted uppercase">
        {groups.map((g) => (
          <li key={g.key} className="flex min-w-0 items-center gap-1.5">
            <span className={cx('h-1.5 w-3 shrink-0 rounded-full', swatch.get(g.key))} />
            <span className="break-words">{g.label}</span>
            <span className="text-text tabular-nums">{g.items.length}</span>
          </li>
        ))}
      </ul>
    </div>
  )
}

/** ▲ / ▼ on the up and down groups only; the group label already says it, so screen readers skip the glyph. */
function ToneGlyph({ tone }: { tone: DirectionGroup['tone'] }) {
  if (tone === 'neutral') return null
  return (
    <span aria-hidden="true" className={cx('text-[11px]', tone === 'up' ? 'text-up' : 'text-down')}>
      {tone === 'up' ? '▲' : '▼'}
    </span>
  )
}

function Group({
  group,
  items,
  sources,
  files,
  angles,
  defaultOpen,
}: {
  group: DirectionGroup
  items: ForecastFinding[]
  sources: Map<string, ForecastSource>
  files: Map<string, ForecastMaterial>
  angles: Map<string, string>
  defaultOpen: boolean
}) {
  const [open, setOpen] = useState(defaultOpen)
  const [all, setAll] = useState(false)
  const id = useId() // not from the key: categorical outcomes can differ only in punctuation or script
  return (
    <Card className="min-w-0 print:break-inside-avoid-page">
      <button
        type="button"
        aria-expanded={open}
        aria-controls={id}
        onClick={() => setOpen(!open)}
        className="flex w-full min-w-0 items-center gap-2.5 rounded-[14px] px-5 py-3.5 text-left transition-colors hover:bg-surface-2/60"
      >
        <ToneGlyph tone={group.tone} />
        <span className="min-w-0 font-display text-[17px] leading-tight font-medium break-words text-text">{group.label}</span>
        <Badge tone="neutral" className="tabular-nums" title={`${items.length} finding${items.length === 1 ? '' : 's'}`}>
          {items.length}
        </Badge>
        <IconChevron size={14} strokeWidth={2} className={cx('ml-auto shrink-0 text-muted transition-transform duration-150 print:hidden', open && 'rotate-90')} />
      </button>
      {/* Collapsed groups and the overflow stay in the DOM, hidden on screen, so Export PDF prints the whole ledger. */}
      <div id={id} className={cx('border-t border-border', !open && 'hidden print:block')}>
        <ol className="divide-y divide-border px-5">
          {items.map((x, i) => (
            <Item key={x.id} f={x} sources={sources} files={files} angle={angles.get(x.angle)} hidden={!all && i >= SHOWN} />
          ))}
        </ol>
        {items.length > SHOWN && (
          <div className="border-t border-border px-5 py-2.5 print:hidden">
            <button type="button" onClick={() => setAll(!all)} className="rounded-sm text-[12.5px] font-medium text-accent-ink hover:underline">
              {all ? 'Show fewer' : `Show all ${items.length}`}
            </button>
          </div>
        )}
      </div>
    </Card>
  )
}

const STRENGTH_TONE: Record<ForecastFinding['strength'], { tone: BadgeTone; className?: string }> = {
  strong: { tone: 'accent' },
  moderate: { tone: 'neutral', className: 'text-text' },
  slight: { tone: 'neutral' },
  minimal: { tone: 'outline' },
}

export function Strength({ level }: { level: ForecastFinding['strength'] }) {
  const s = STRENGTH_TONE[level]
  return (
    <Badge tone={s.tone} className={cx('mr-2 align-[1px]', s.className)} title={`${level} strength`}>
      {level}
    </Badge>
  )
}

function Item({
  f,
  sources,
  files,
  angle,
  hidden,
}: {
  f: ForecastFinding
  sources: Map<string, ForecastSource>
  files: Map<string, ForecastMaterial>
  angle?: string
  hidden: boolean
}) {
  const material = f.origin === 'material'
  const links = material ? [] : f.sourceIds.map((id) => sources.get(id)).filter((s): s is ForecastSource => !!s && !!httpUrl(s.url))
  const mine = material ? f.sourceIds.map((id) => files.get(id)).filter((m): m is ForecastMaterial => !!m) : []
  return (
    <li data-finding={f.id} className={cx('flex gap-3 py-3.5 print:break-inside-avoid', hidden && 'hidden print:flex')}>
      <span className="w-7 shrink-0 font-mono text-[11px] leading-5 text-accent-ink tabular-nums">{f.id}</span>
      <div className="min-w-0 flex-1">
        <p className="text-[14px] leading-snug text-pretty break-words text-text">
          <Strength level={f.strength} />
          {f.claim}
        </p>
        <p className="mt-1.5 flex min-w-0 flex-wrap gap-x-2 gap-y-0.5 font-mono text-[11px] leading-snug text-muted">
          {material && (
            <Badge tone="outline" title="From a file you attached: unverified">
              Your material
            </Badge>
          )}
          {angle && <span>{angle}</span>}
          {material && f.ref && <span>{f.ref}</span>}
          {f.asOf && <span>{(angle || (material && f.ref)) && '· '}as of {fmtAsOf(f.asOf)}</span>}
          {mine.map((m) => (
            <span key={m.id} className="min-w-0 break-words">
              <a href={materialHref(m.id)} className="rounded-sm text-accent-ink hover:underline">
                {m.id} · {m.name}
              </a>
            </span>
          ))}
          {links.map((s) => (
            <span key={s.id} className="min-w-0 break-words">
              <span className="mr-1.5 text-accent-ink">{s.id}</span>
              <ExternalLink href={httpUrl(s.url)!.href}>{s.title || s.source || s.id}</ExternalLink>
            </span>
          ))}
        </p>
      </div>
    </li>
  )
}

/** The aggregator's pending signals, each with if/then branches and the direction they would move the forecast. */
export function WhatToWatch({ f, r }: { f: Pick<Forecast, 'shape' | 'outcomes'>; r: ForecastResult }) {
  const items = (Array.isArray(r.watch) ? r.watch : []).filter((w) => w && typeof w.signal === 'string' && w.signal.trim())
  if (!items.length) return null
  return (
    <Card className="p-5 print:break-inside-avoid">
      <div className="label">What to watch</div>
      <ol className="mt-3 flex flex-col gap-4">
        {items.map((w, i) => (
          <li key={i} className="flex gap-3">
            <span className="font-mono text-[11px] leading-5 text-accent-ink tabular-nums">{String(i + 1).padStart(2, '0')}</span>
            <div className="min-w-0 flex-1">
              <p className="text-[13.5px] leading-snug font-medium break-words text-text">{w.signal}</p>
              {w.detail && <p className="mt-0.5 text-[13px] leading-snug break-words text-muted">{w.detail}</p>}
              {(Array.isArray(w.branches) ? w.branches : []).length > 0 && (
                <ul className="mt-2 flex flex-col gap-1.5">
                  {w.branches.map((b, j) => (
                    <li key={j} className="text-[13px] leading-snug break-words text-text">
                      <span className="text-muted">If</span> {b.if} <span aria-hidden="true" className="text-muted">→</span>
                      <span className="sr-only">, then</span> {b.then}{' '}
                      <DirectionChip group={directionGroupOf(b.direction ?? '', f.shape, f.outcomes)} />
                    </li>
                  ))}
                </ul>
              )}
            </div>
          </li>
        ))}
      </ol>
    </Card>
  )
}

export function DirectionChip({ group }: { group: DirectionGroup }) {
  return (
    <Badge tone={group.tone} className="ml-0.5 h-auto max-w-full py-px align-[1px] whitespace-normal">
      {group.tone !== 'neutral' && <span aria-hidden="true">{group.tone === 'up' ? '▲' : '▼'}</span>}
      {group.label}
    </Badge>
  )
}

/**
 * Round 8b: the volatility-only P(Yes) for this price question, as of the run, and the server's note when it reset the
 * desk's number to it (the desk moved toward 50% without citing evidence).
 */
export function VolatilityReference({ r }: { r: ForecastResult }) {
  const ref = r.reference
  if (!ref || !Number.isFinite(ref.pYes)) return null
  const vol = Number.isFinite(ref.volAnnPct) ? `90-day realized vol ${Math.round(ref.volAnnPct)}%/yr, ` : ''
  const days = Math.round(ref.horizonDays)
  return (
    <Card className="px-5 py-4 print:break-inside-avoid">
      <p className="text-[13.5px] leading-snug text-text">
        <span className="font-medium">Volatility-only reference:</span> {fmtChance(ref.pYes)} Yes{' '}
        <span className="text-muted">
          ({vol}
          {days} day{days === 1 ? '' : 's'}, as of {fmtDay(ref.asOf, true)})
        </span>
      </p>
      {r.guard === 'shrink' && (
        <p role="note" className="mt-2.5 flex gap-2.5 rounded-xl border border-accent/40 bg-accent-soft/60 px-3.5 py-2.5 text-[13px] leading-snug text-text">
          <IconAlert size={16} className="mt-px shrink-0 text-accent-ink" />
          <span>
            The server reset the desk's number to this reference: the desk moved toward 50% from it without citing
            evidence the formula cannot see.
          </span>
        </p>
      )}
    </Card>
  )
}

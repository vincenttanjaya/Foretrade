// "Your materials" on a forecast (API contract round 9): each file the user attached and what the desk read from it,
// and, on a forecast that can update, the files for the next update. Summaries, findings and excerpts are
// model-written from the user's files, so they render as plain text only.
import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router'
import { useAttachmentQueue } from '../../components/attachments/useAttachmentQueue'
import { Badge } from '../../components/ui/Badge'
import { Button } from '../../components/ui/Button'
import { Card } from '../../components/ui/Card'
import { IconChevron } from '../../components/ui/icons'
import { useToast } from '../../components/toast'
import { api, ATTACHMENT_BODY_LIMIT, bodyBytes } from '../../lib/api'
import { useAuth } from '../../lib/auth'
import { cx } from '../../lib/cx'
import { errorMessage, isQuota } from '../../lib/errors'
import { fmtDate } from '../../lib/format'
import type { Forecast, ForecastFinding, ForecastMaterial } from '../../lib/types'
import { FileGlyph } from '../agent/FileGlyph'
import { DirectionChip, Strength } from './EvidenceBase'
import { directionGroupOf, findingsOf, fmtAsOf, isUpdatable, MAX_MATERIALS, materialLine, materialsOf, materialsQuotaMessage, runCost } from './forecast'
import { MaterialsField } from './MaterialsField'
import { PulseDot } from './parts'

const HASH = /^#material-(D\d{1,2})$/

const FAILED_TEXT: Record<NonNullable<ForecastMaterial['error']>, string> = {
  unreadable: "Couldn't read this file",
  timeout: 'Ran out of time',
  interrupted: 'Run stopped before it was read',
}

const RELEVANCE: Record<NonNullable<ForecastMaterial['relevance']>, { text: string; tone: 'accent' | 'neutral' | 'outline' }> = {
  direct: { text: 'About this question', tone: 'accent' },
  background: { text: 'Background', tone: 'neutral' },
  unrelated: { text: 'Not about this question', tone: 'outline' },
}

/**
 * The card: one row per material (`#material-D2`, opened by any link to it), then the next update's files when the
 * forecast can update. Nothing when there are no materials and no update to add them to.
 */
export function YourMaterials({ f }: { f: Forecast }) {
  const materials = materialsOf(f)
  const updatable = isUpdatable(f)
  const ledger = f.result ? findingsOf(f.result, materials).filter((x) => x.origin === 'material') : []
  const [open, setOpen] = useState<Set<string>>(() => new Set(materials.length <= 2 ? materials.map((m) => m.id) : []))

  // A link to a row (an evidence item, a finding, the initial URL) opens it and moves focus to its summary.
  useEffect(() => {
    const reveal = (hash: string) => {
      const id = HASH.exec(hash)?.[1]
      const row = id && document.getElementById(`material-${id}`)
      if (!id || !row) return
      setOpen((s) => new Set(s).add(id))
      row.querySelector<HTMLElement>('summary, [data-row-head]')?.focus({ preventScroll: true })
      row.scrollIntoView?.({ block: 'start' })
    }
    const onHash = () => reveal(window.location.hash)
    // A second click on the same link changes no hash, so clicks are caught too.
    const onClick = (e: MouseEvent) => {
      const a = (e.target as Element | null)?.closest?.('a[href^="#material-"]')
      if (a) requestAnimationFrame(() => reveal(a.getAttribute('href') ?? ''))
    }
    onHash()
    window.addEventListener('hashchange', onHash)
    document.addEventListener('click', onClick)
    return () => {
      window.removeEventListener('hashchange', onHash)
      document.removeEventListener('click', onClick)
    }
  }, [])

  if (!materials.length && !updatable) return null
  if (!materials.length)
    return (
      <section aria-label="Add material" className="mt-6 print:hidden">
        <NextUpdate f={f} materials={[]} title="Add material" />
      </section>
    )

  const toggle = (id: string, isOpen: boolean) =>
    setOpen((s) => {
      const next = new Set(s)
      if (isOpen) next.add(id)
      else next.delete(id)
      return next
    })

  return (
    <section id="materials" aria-labelledby="your-materials" className="mt-12 scroll-mt-20">
      <div className="label">Your materials · {materials.length}</div>
      <h2 id="your-materials" className="mt-1.5 font-display text-[28px] leading-tight font-medium text-text">
        What you gave the desk
      </h2>
      <p className="mt-1.5 max-w-2xl text-[14px] leading-relaxed text-muted">
        Read once when the run started; we keep these notes, not the files. The desk checks them against its research.
      </p>
      <Card className="mt-5 min-w-0">
        <ul className="divide-y divide-border">
          {materials.map((m) => (
            <MaterialRow
              key={m.id}
              m={m}
              f={f}
              ledger={ledger.filter((x) => x.sourceIds.includes(m.id))}
              open={open.has(m.id)}
              onToggle={(isOpen) => toggle(m.id, isOpen)}
            />
          ))}
        </ul>
        {updatable && (
          <div className="border-t border-border print:hidden">
            <NextUpdate f={f} materials={materials} title="Add material to the next update" flush />
          </div>
        )}
      </Card>
    </section>
  )
}

function StatusBadge({ m }: { m: ForecastMaterial }) {
  if (m.status === 'pending')
    return (
      <span className="inline-flex items-center gap-1.5 font-mono text-[11px] text-accent-ink">
        <PulseDot /> Reading…
      </span>
    )
  if (m.status === 'failed') return <span className="font-mono text-[11px] text-down">{FAILED_TEXT[m.error ?? 'unreadable']}</span>
  const r = m.relevance && RELEVANCE[m.relevance]
  return (
    <span className="inline-flex flex-wrap items-center gap-x-2 gap-y-1">
      {r && (
        <Badge tone={r.tone} className={cx(m.relevance === 'unrelated' && 'text-muted')}>
          {r.text}
        </Badge>
      )}
      {m.reusedFrom && m.readAt && <span className="font-mono text-[11px] text-muted">Read on {fmtDate(m.readAt)}</span>}
    </span>
  )
}

function MaterialRow({
  m,
  f,
  ledger,
  open,
  onToggle,
}: {
  m: ForecastMaterial
  f: Pick<Forecast, 'shape' | 'outcomes'>
  ledger: ForecastFinding[]
  open: boolean
  onToggle: (open: boolean) => void
}) {
  // The ledger numbers each digest finding (F15, F16…) in the same order; match by claim, so a gap never shifts ids.
  const fid = (claim: string) => ledger.find((x) => x.claim === claim)?.id
  const body = m.status === 'read' && (m.summary || m.findings.length > 0 || m.excerpt)
  const head = (
    <>
      <span className="w-6 shrink-0 pt-2.5 font-mono text-[11px] text-accent-ink tabular-nums">{m.id}</span>
      <FileGlyph attachment={m} className="text-accent" />
      <span className="flex min-w-0 flex-1 flex-col gap-1">
        <span className="text-[14.5px] leading-snug font-medium break-words text-text">{m.name}</span>
        <span className="font-mono text-[11px] text-muted">{materialLine(m)}</span>
        <StatusBadge m={m} />
      </span>
    </>
  )
  const HEAD = 'flex items-start gap-3 rounded-[14px] px-5 py-4 outline-offset-[-2px]'
  // A file still being read, or one the desk could not read, has nothing to expand: a plain row, not a disclosure.
  if (!body)
    return (
      <li id={`material-${m.id}`} className="min-w-0 scroll-mt-20">
        <div data-row-head tabIndex={-1} className={HEAD}>
          {head}
        </div>
      </li>
    )
  return (
    <li id={`material-${m.id}`} className="min-w-0 scroll-mt-20">
      <details open={open} onToggle={(e) => onToggle(e.currentTarget.open)} className="group">
        <summary className={cx(HEAD, 'cursor-pointer list-none [&::-webkit-details-marker]:hidden')}>
          {head}
          <IconChevron size={14} strokeWidth={2} className="mt-3 shrink-0 text-muted transition-transform duration-150 group-open:rotate-90 print:hidden" />
        </summary>
        <div className="flex flex-col gap-3 px-5 pb-5 sm:pl-[6.75rem]">
          {m.summary && <p className="text-[14px] leading-relaxed break-words whitespace-pre-line text-text">{m.summary}</p>}
          {m.asOf && <p className="font-mono text-[11px] text-muted">as of {fmtAsOf(m.asOf)}</p>}
          {m.findings.length > 0 && (
            <ol aria-label={`Findings from ${m.name}`} className="divide-y divide-border rounded-xl border border-border">
              {m.findings.map((x, i) => {
                const id = fid(x.claim)
                return (
                  <li key={i} className="flex gap-3 px-3.5 py-3">
                    <span className="w-7 shrink-0 font-mono text-[11px] leading-5 text-accent-ink tabular-nums">{id ?? '·'}</span>
                    <div className="min-w-0 flex-1">
                      <p className="text-[13.5px] leading-snug text-pretty break-words text-text">
                        <Strength level={x.strength} />
                        {x.claim}
                      </p>
                      <p className="mt-1.5 flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 font-mono text-[11px] leading-snug text-muted">
                        {[x.ref, x.asOf && `as of ${fmtAsOf(x.asOf)}`].filter(Boolean).join(' · ')}
                        <DirectionChip group={directionGroupOf(x.direction, f.shape, f.outcomes)} />
                      </p>
                    </div>
                  </li>
                )
              })}
            </ol>
          )}
          {m.excerpt && (
            <details className="group/excerpt rounded-xl border border-border bg-surface-2/60">
              <summary className="flex cursor-pointer list-none items-center gap-2 px-3.5 py-2.5 text-[12.5px] font-medium text-text [&::-webkit-details-marker]:hidden">
                <IconChevron size={12} strokeWidth={2} className="shrink-0 text-muted transition-transform duration-150 group-open/excerpt:rotate-90" />
                Quoted passages
              </summary>
              <p className="px-3.5 pb-3.5 font-mono text-[12px] leading-relaxed break-words whitespace-pre-wrap text-muted">{m.excerpt}</p>
            </details>
          )}
        </div>
      </details>
    </li>
  )
}

/**
 * "Add material to the next update": new files, and the read materials to keep (each removable). Sends
 * keepMaterials only after a removal, so an untouched list keeps every read file, as the one-click Update does.
 */
function NextUpdate({ f, materials, title, flush }: { f: Forecast; materials: ForecastMaterial[]; title: string; flush?: boolean }) {
  const navigate = useNavigate()
  const { toast } = useToast()
  const { credits, setCredits, refresh } = useAuth()
  const read = materials.filter((m) => m.status === 'read')
  const [kept, setKept] = useState(() => read.map((m) => m.id))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const queue = useAttachmentQueue({ max: MAX_MATERIALS - kept.length, disabled: busy, per: 'per forecast' })
  const removed = kept.length < read.length
  const changed = queue.files.length > 0 || removed

  async function update() {
    if (busy || queue.processing || !changed) return
    const attachments = queue.inputs()
    const opts = { attachments, ...(removed ? { keepMaterials: kept } : {}) }
    if (bodyBytes(opts) > ATTACHMENT_BODY_LIMIT) return setError('These files are too large to send together. Remove one and try again.')
    setBusy(true)
    setError(null)
    try {
      const res = await api.updateForecast(f.id, opts)
      if (res.credits) setCredits(res.credits)
      toast('Updating the forecast with your materials. The specialists are on it.', 'success')
      navigate(`/forecasts/${res.forecast.id}`)
    } catch (err) {
      setError((isQuota(err) && materialsQuotaMessage(attachments.length, credits)) || errorMessage(err))
      if (isQuota(err)) refresh().catch(() => {})
      setBusy(false)
    }
  }

  return (
    <details className={cx('group/next', !flush && 'rounded-[14px] border border-border bg-surface')}>
      <summary className="flex cursor-pointer list-none items-center gap-2 px-5 py-3.5 text-[13.5px] font-medium text-text [&::-webkit-details-marker]:hidden">
        <IconChevron size={13} strokeWidth={2} className="shrink-0 text-muted transition-transform duration-150 group-open/next:rotate-90" />
        {title}
      </summary>
      <div className="flex flex-col gap-3 px-5 pb-5">
        <p className="text-[13px] leading-relaxed text-muted">
          The update re-runs the panel on today's evidence with these files. Files the desk already read are kept at no
          cost unless you remove them.
        </p>
        <MaterialsField
          queue={queue}
          kept={read.filter((m) => kept.includes(m.id))}
          onRemoveKept={(id) => setKept((list) => list.filter((x) => x !== id))}
          disabled={busy}
        />
        <div className="flex flex-wrap items-center gap-3">
          <Button variant="primary" loading={busy} disabled={!changed || queue.processing} onClick={update}>
            Update with these materials <span className="font-mono text-[11.5px] font-normal opacity-70">· {runCost(queue.files.length)} cr</span>
          </Button>
          {queue.processing ? (
            <span role="status" className="text-[12.5px] text-muted">
              Preparing files…
            </span>
          ) : (
            !changed && <span className="text-[12.5px] text-muted">Add a file, or remove one, to update with different materials.</span>
          )}
        </div>
        {error && (
          <p role="alert" className="text-[13px] text-down">
            {error}
          </p>
        )}
      </div>
    </details>
  )
}

import { useEffect, useRef, useState } from 'react'
import { Link } from 'react-router'
import { Badge } from '../../components/ui/Badge'
import { Button } from '../../components/ui/Button'
import { api } from '../../lib/api'
import { errorMessage } from '../../lib/errors'
import type { Alert, AlertRun } from '../../lib/types'

const STATES: Record<AlertRun['status'], string> = {
  ok: 'Completed', failed: 'Failed', running: 'Working', interrupted: 'Interrupted', cancelled: 'Cancelled',
}
const VERDICTS = { holds: 'Holds', weakened: 'Weakened', invalidated: 'Invalidated', unknown: 'Verdict unknown' }
const time = (at: string) => new Date(at).toLocaleString('en-US', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', hourCycle: 'h23', timeZone: 'UTC' })

/** The bounded excerpt links to the complete owned markdown report and its sources. */
export function RunSummary({ run }: { run: AlertRun }) {
  return <div className="min-w-0 space-y-1.5 text-[13px] [overflow-wrap:anywhere]">
    <div className="flex flex-wrap items-center gap-2">
      <Badge tone={run.status === 'failed' || run.status === 'interrupted' ? 'down' : run.status === 'ok' ? 'up' : 'neutral'}>{STATES[run.status]}</Badge>
      {run.verdict && <Badge tone={run.verdict === 'invalidated' ? 'down' : run.verdict === 'holds' ? 'up' : 'neutral'}>{VERDICTS[run.verdict]}</Badge>}
      <time dateTime={run.at} className="text-[12px] text-muted">{time(run.at)} UTC</time>
    </div>
    {run.status === 'running' ? <p className="text-muted">Working on the research report…</p> : <>
      {(run.error || run.excerpt || run.body) && <p className={run.error ? 'text-down' : 'text-text'}>{run.error || run.excerpt || run.body}</p>}
      {run.reportChatId ? <Link to={`/chat/${encodeURIComponent(run.reportChatId)}`} className="font-medium text-accent-ink hover:underline">Open chat</Link>
        : <p className="text-[12px] text-muted">Report chat unavailable{run.status === 'ok' ? ' · it may have been deleted.' : '.'}</p>}
    </>}
  </div>
}

export function ResearchHistory({ alert }: { alert: Alert }) {
  const [runs, setRuns] = useState<AlertRun[] | null>(null)
  const [nextCursor, setNextCursor] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const pending = useRef(false)
  const generation = useRef(0)
  const controller = useRef<AbortController | null>(null)
  const cursor = useRef<string | null>(null)
  const currentId = useRef(alert.id)
  currentId.current = alert.id

  async function load(older = false) {
    if (older && (pending.current || !cursor.current)) return
    controller.current?.abort()
    const ctrl = new AbortController()
    controller.current = ctrl
    const request = ++generation.current
    const alertId = alert.id
    pending.current = true
    setBusy(true)
    setError(null)
    try {
      const page = await api.alertRuns(alertId, { limit: 20, ...(older ? { before: cursor.current! } : {}) }, ctrl.signal)
      if (ctrl.signal.aborted || request !== generation.current || currentId.current !== alertId) return
      setRuns((previous) => {
        const combined = older ? [...(previous ?? []), ...page.runs] : page.runs
        return combined.filter((run, index) => combined.findIndex((item) => item.id === run.id) === index)
      })
      cursor.current = page.nextCursor
      setNextCursor(page.nextCursor)
    } catch (err) {
      if (!ctrl.signal.aborted && request === generation.current) setError(errorMessage(err, "Couldn't load the run history. Try again."))
    } finally {
      if (request === generation.current) { pending.current = false; setBusy(false) }
    }
  }

  useEffect(() => {
    setRuns(null)
    cursor.current = null
    setNextCursor(null)
    void load()
    return () => { generation.current++; controller.current?.abort(); pending.current = false }
  }, [alert.id, alert.status, alert.lastRun?.id, alert.lastRun?.status, alert.lastRun?.reportChatId])

  return <section aria-label="Research run history" className="mt-4 space-y-3 rounded-xl border border-border bg-surface-2 p-3">
    <div className="flex flex-wrap items-center justify-between gap-2"><h3 className="text-sm font-semibold text-text">Run history</h3><span className="text-[12px] text-muted">Last 30 days · 20 per page</span></div>
    {error && <p role="alert" className="text-[13px] text-down">{error}</p>}
    {runs === null ? busy ? <p role="status" className="text-[13px] text-muted">Loading run history…</p> : <Button size="sm" variant="secondary" onClick={() => void load()}>Try again</Button>
      : runs.length ? <ol className="space-y-3">{runs.map(run => <li key={run.id} className="border-b border-border pb-3 last:border-b-0 last:pb-0"><RunSummary run={run} /></li>)}</ol>
        : <p className="text-[13px] text-muted">No research runs in the last 30 days.</p>}
    {nextCursor && <Button variant="secondary" size="sm" loading={busy} onClick={() => void load(true)}>Load older</Button>}
  </section>
}

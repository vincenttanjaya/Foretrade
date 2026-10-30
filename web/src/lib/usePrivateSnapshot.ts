import { useEffect, useEffectEvent, useRef, useState } from 'react'
import { errorMessage } from './errors'
import { ApiError } from './api'

type Snapshot = { revision: number; asOf: string }
/** Response-local private facts: revisions order mutations, generations order reads, asOf orders equal revisions. */
export function usePrivateSnapshot<T>(identity: string, revision: number | null, load: (signal: AbortSignal) => Promise<T>, snapshot: (value: T) => Snapshot | null, ahead: () => unknown) {
  const [state, setState] = useState<{ identity: string; value: T | null; error: string; busy: boolean; failure?: unknown }>({ identity, value: null, error: '', busy: false })
  const latest = useRef(revision)
  latest.current = revision
  const generation = useRef(0)
  const accepted = useRef<Snapshot | null>(null)
  const value = useRef<T | null>(null)
  const active = useRef<AbortController | null>(null)
  const refreshed = useRef(-1)
  const read = useEffectEvent(async () => {
    active.current?.abort()
    const ctrl = new AbortController(); active.current = ctrl
    const request = ++generation.current
    setState({ identity, value: value.current, error: '', busy: true })
    try {
      const result = await load(ctrl.signal)
      if (ctrl.signal.aborted || request !== generation.current || latest.current == null) return
      const s = snapshot(result)
      if (s && s.revision < latest.current) throw new ApiError(409, 'stale_review', 'Review returned an older account revision. Retry for current facts.')
      if (s && accepted.current && (s.revision < accepted.current.revision || (s.revision === accepted.current.revision && Date.parse(s.asOf) < Date.parse(accepted.current.asOf)))) throw new ApiError(409, 'stale_review', 'Review returned an older snapshot. Retry for current facts.')
      accepted.current = s; value.current = result
      setState({ identity, value: result, error: '', busy: false })
      if (s && s.revision > latest.current && s.revision > refreshed.current) { refreshed.current = s.revision; void ahead() }
    } catch (err) {
      if (!ctrl.signal.aborted && request === generation.current) setState({ identity, value: value.current, error: errorMessage(err, 'Could not refresh current facts.'), busy: false, failure: err })
    }
  })
  useEffect(() => {
    generation.current++; active.current?.abort(); accepted.current = null; value.current = null; refreshed.current = -1
    setState({ identity, value: null, error: '', busy: false })
  }, [identity])
  useEffect(() => {
    if (revision == null) {
      generation.current++; active.current?.abort(); accepted.current = null; value.current = null
      setState({ identity, value: null, error: '', busy: false }); return
    }
    if (accepted.current && accepted.current.revision < revision) { accepted.current = null; value.current = null }
    if (!document.hidden) void read()
    const tick = () => { if (!document.hidden) void read() }
    const visible = () => { if (document.hidden) { generation.current++; active.current?.abort(); setState((s) => ({ ...s, busy: false })) } else tick() }
    const timer = setInterval(tick, 60_000)
    document.addEventListener('visibilitychange', visible)
    return () => { clearInterval(timer); document.removeEventListener('visibilitychange', visible); generation.current++; active.current?.abort() }
  }, [identity, revision])
  const s = state.value ? snapshot(state.value) : null
  const current = revision != null && state.identity === identity && (!s || s.revision >= revision)
  return { value: current ? state.value : null, error: state.error, failure: state.failure, busy: state.busy, retry: () => { if (latest.current != null) void read() } }
}

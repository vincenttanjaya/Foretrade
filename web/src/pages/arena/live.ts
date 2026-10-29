import { useCallback, useEffect, useRef, useState } from 'react'
import type { ArenaStep, LiveAgent } from '../../lib/types'
import { usePoll } from '../../lib/usePoll'
import { errorMessage } from '../../lib/errors'

export function copyInput(percent: string, cap: string) {
  const ratio = Number(percent), maxUsd = Number(cap)
  return percent.trim() && cap.trim() && Number.isFinite(ratio) && ratio >= 10 && ratio <= 100 && Number.isFinite(maxUsd) && maxUsd >= 100 && maxUsd <= 50000
    ? { ratio: ratio / 100, maxUsd } : null
}
const COLORS = { momentum: '#3B82F6', contrarian: '#A855F7', macro: '#C8962E' }
export const personaColor = (id: LiveAgent['personaId']) => COLORS[id]
export const utc = (iso: string | null) => iso ? `${iso.replace('T', ' ').slice(0, 16)} UTC` : '—'
export const targetChange = (next: number, before?: number) => before == null ? `${next}% target` : `${before}% → ${next}%`
export function mergeSteps(current: ArenaStep[], incoming: ArenaStep[]) {
  const byStep = new Map(current.map((s) => [s.step, s]))
  incoming.forEach((s) => byStep.set(s.step, s))
  return [...byStep.values()].sort((a, b) => b.step - a.step).slice(0, 100)
}

/** One visible-tab read at a time. Mutations invalidate and abort all earlier snapshots, even if an API fake ignores abort. */
export function useLiveRead<T>(load: (signal: AbortSignal) => Promise<T>) {
  const [data, setData] = useState<T | null>(null)
  const [error, setError] = useState('')
  const generation = useRef(0)
  const current = useRef<AbortController | null>(null)
  const alive = useRef(true)
  const loader = useRef(load)
  loader.current = load
  useEffect(() => {
    alive.current = true
    return () => { alive.current = false; generation.current++; current.current?.abort(); current.current = null }
  }, [])
  const invalidate = useCallback(() => { generation.current++; current.current?.abort(); current.current = null }, [])
  const refresh = useCallback(async () => {
    if (current.current || !alive.current) return
    const ctrl = new AbortController(), request = ++generation.current
    current.current = ctrl
    try {
      const next = await loader.current(ctrl.signal)
      if (alive.current && !ctrl.signal.aborted && request === generation.current) { setData(next); setError('') }
    } catch (err) {
      if (alive.current && !ctrl.signal.aborted && request === generation.current) setError(errorMessage(err, 'Could not load this data.'))
    } finally { if (request === generation.current) current.current = null }
  }, [])
  usePoll(refresh, 30000)
  const commit = useCallback((update: (previous: T | null) => T | null) => {
    invalidate(); setData(update); setError(''); void refresh()
  }, [invalidate, refresh])
  const reload = useCallback(() => { invalidate(); void refresh() }, [invalidate, refresh])
  return { data, error, refresh: reload, commit }
}

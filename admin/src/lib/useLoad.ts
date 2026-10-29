import { useCallback, useEffect, useState } from 'react'
import { errorMessage } from './api'

/**
 * Loads `load` now and whenever `deps` change or `reload()` is called; the previous request is aborted.
 * `data` survives a failed reload (pages show the error next to the stale data); `setData` takes a mutation's result.
 * `failure` is the thrown value (e.g. an ApiError with status 404), `error` its message.
 */
export function useLoad<T>(load: (signal: AbortSignal) => Promise<T>, deps: readonly unknown[]) {
  const [data, setData] = useState<T | null>(null)
  const [failure, setFailure] = useState<unknown>(null)
  const [loading, setLoading] = useState(true)
  const [tick, setTick] = useState(0)

  useEffect(() => {
    const ctrl = new AbortController()
    setLoading(true)
    load(ctrl.signal).then(
      (d) => {
        if (ctrl.signal.aborted) return
        setData(d)
        setFailure(null)
        setLoading(false)
      },
      (err: unknown) => {
        if (ctrl.signal.aborted) return
        setFailure(err ?? new Error('failed'))
        setLoading(false)
      },
    )
    return () => ctrl.abort()
  }, [...deps, tick]) // not `load`: it is a fresh closure every render; `deps` say when it changes

  const reload = useCallback(() => setTick((t) => t + 1), [])
  const error = failure ? errorMessage(failure, "Couldn't load this page. Please try again.") : null
  return { data, setData, error, failure, loading, reload }
}

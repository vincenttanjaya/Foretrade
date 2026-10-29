import { useEffect, useEffectEvent, useRef, useState } from 'react'
import { api, ApiError } from '../../lib/api'
import { errorMessage } from '../../lib/errors'
import type { ScreenerRow } from '../../lib/types'
import { chunk, pollMs, QUOTES_PER_CALL, refreshTickers } from './live'

type Options = {
  rows: ScreenerRow[] // the loaded rows, in their order
  pollSeconds: number | null | undefined // the page's: absent from an API before the live screener (no refresh then)
  loadKey: string | number // a new page load: the next refresh is one interval after it
  onRows: (fresh: ScreenerRow[]) => void // one call's rows, to merge in place
}

/**
 * Refreshes the loaded rows in place through the light endpoint (at most 100 tickers a call), every `pollSeconds`
 * while the tab is visible, and at once when it comes back after an interval or more. An API without the endpoint
 * (404) stops it: the page then keeps the run's values, as before the live screener. `now` follows each tick, so a
 * quote that passes its stale time looks stale even while refreshes fail.
 */
export function useLiveRows({ rows, pollSeconds, loadKey, onRows }: Options) {
  const [now, setNow] = useState(() => Date.now())
  const [error, setError] = useState<string | null>(null)
  const [missing, setMissing] = useState(false) // the API has no light endpoint
  const loaded = useRef(rows)
  loaded.current = rows
  const apply = useEffectEvent(onRows)
  const ms = missing ? null : pollMs(pollSeconds)

  useEffect(() => {
    if (ms == null) return
    let ctrl: AbortController | null = null
    let last = Date.now() // the page was just read
    const refresh = async () => {
      if (ctrl) return // one refresh at a time
      const c = (ctrl = new AbortController())
      last = Date.now()
      try {
        for (const tickers of chunk(refreshTickers(loaded.current), QUOTES_PER_CALL)) {
          const res = await api.screenerQuotes(tickers, c.signal)
          if (c.signal.aborted) return
          apply(res.rows)
        }
        setError(null)
      } catch (err) {
        if (c.signal.aborted) return
        if (err instanceof ApiError && err.status === 404) setMissing(true)
        else setError(errorMessage(err, "Couldn't refresh the prices."))
      } finally {
        if (ctrl === c) ctrl = null
        if (!c.signal.aborted) setNow(Date.now())
      }
    }
    const tick = () => {
      setNow(Date.now())
      if (!document.hidden) void refresh()
    }
    const visible = () => {
      if (document.hidden) return
      setNow(Date.now())
      if (Date.now() - last >= ms) void refresh()
    }
    const timer = setInterval(tick, ms)
    document.addEventListener('visibilitychange', visible)
    return () => {
      clearInterval(timer)
      document.removeEventListener('visibilitychange', visible)
      ctrl?.abort()
    }
  }, [ms, loadKey])

  return { live: ms != null, now, error }
}

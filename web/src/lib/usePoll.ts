import { useEffect, useEffectEvent } from 'react'

/**
 * Runs `fn` now (even in a hidden tab, so a page reloaded in the background still has data), then
 * every `ms` while the tab is visible, and again as soon as the tab becomes visible.
 * Pass `enabled = false` to pause (e.g. once a forecast stops running). A new `restartKey` runs `fn` at once and
 * restarts the interval (e.g. a poll whose request changed).
 */
export function usePoll(fn: () => unknown, ms: number, enabled = true, restartKey?: string | number) {
  const run = useEffectEvent(() => {
    fn()
  })
  useEffect(() => {
    if (!enabled) return
    run()
    const tick = () => {
      if (!document.hidden) run()
    }
    const timer = setInterval(tick, ms)
    document.addEventListener('visibilitychange', tick)
    return () => {
      clearInterval(timer)
      document.removeEventListener('visibilitychange', tick)
    }
  }, [ms, enabled, restartKey])
}

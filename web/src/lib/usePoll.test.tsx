import { act, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { usePoll } from './usePoll'

let hidden = false
const setHidden = (h: boolean) => {
  hidden = h
  document.dispatchEvent(new Event('visibilitychange'))
}

describe('usePoll', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    hidden = false
    Object.defineProperty(document, 'hidden', { configurable: true, get: () => hidden })
  })
  afterEach(() => {
    vi.useRealTimers()
    Reflect.deleteProperty(document, 'hidden')
  })

  it('runs now, then every interval while visible', () => {
    const fn = vi.fn()
    renderHook(() => usePoll(fn, 1000))
    expect(fn).toHaveBeenCalledTimes(1)
    vi.advanceTimersByTime(3000)
    expect(fn).toHaveBeenCalledTimes(4)
  })

  it('runs the first time even in a hidden tab, then pauses until the tab is visible again', () => {
    hidden = true
    const fn = vi.fn()
    renderHook(() => usePoll(fn, 1000))
    expect(fn).toHaveBeenCalledTimes(1)
    vi.advanceTimersByTime(5000)
    expect(fn).toHaveBeenCalledTimes(1)
    act(() => setHidden(false)) // coming back refreshes at once
    expect(fn).toHaveBeenCalledTimes(2)
    vi.advanceTimersByTime(1000)
    expect(fn).toHaveBeenCalledTimes(3)
  })

  it('ignores a visibilitychange to hidden', () => {
    const fn = vi.fn()
    renderHook(() => usePoll(fn, 1000))
    act(() => setHidden(true))
    expect(fn).toHaveBeenCalledTimes(1)
  })

  it('does nothing while disabled and starts when enabled', () => {
    const fn = vi.fn()
    const { rerender } = renderHook(({ on }) => usePoll(fn, 1000, on), { initialProps: { on: false } })
    vi.advanceTimersByTime(3000)
    expect(fn).not.toHaveBeenCalled()
    rerender({ on: true })
    expect(fn).toHaveBeenCalledTimes(1)
    rerender({ on: false })
    vi.advanceTimersByTime(3000)
    expect(fn).toHaveBeenCalledTimes(1)
  })

  it('always calls the latest callback without restarting the timer', () => {
    const first = vi.fn()
    const second = vi.fn()
    const { rerender } = renderHook(({ fn }) => usePoll(fn, 1000), { initialProps: { fn: first } })
    rerender({ fn: second })
    vi.advanceTimersByTime(1000)
    expect(first).toHaveBeenCalledTimes(1)
    expect(second).toHaveBeenCalledTimes(1)
  })

  it('stops the timer and the visibility listener on unmount', () => {
    const fn = vi.fn()
    const { unmount } = renderHook(() => usePoll(fn, 1000))
    unmount()
    vi.advanceTimersByTime(5000)
    setHidden(false)
    expect(fn).toHaveBeenCalledTimes(1)
  })
})

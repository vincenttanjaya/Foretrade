import { act, renderHook, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { usePrivateSnapshot } from './usePrivateSnapshot'

type Snap = { revision: number; asOf: string }

describe('usePrivateSnapshot', () => {
  // Go trims trailing zeros from RFC3339Nano fractions, so equal-second timestamps differ in length.
  it.each([
    ['a longer fraction in the same second is newer', '2026-01-01T00:00:43.12Z', '2026-01-01T00:00:43.123Z', ''],
    ['a shorter fraction in the same second is older', '2026-01-01T00:00:43.123Z', '2026-01-01T00:00:43.12Z', 'Review returned an older snapshot. Retry for current facts.'],
  ])('orders equal revisions by asOf time: %s', async (_, first, second, error) => {
    const load = vi.fn<(signal: AbortSignal) => Promise<Snap>>()
    load.mockResolvedValueOnce({ revision: 1, asOf: first }).mockResolvedValueOnce({ revision: 1, asOf: second })
    const { result } = renderHook(() => usePrivateSnapshot('u1', 1, load, (s) => s, () => {}))
    await waitFor(() => expect(result.current.value?.asOf).toBe(first))
    act(() => result.current.retry())
    await waitFor(() => expect(result.current.busy).toBe(false))
    expect(load).toHaveBeenCalledTimes(2)
    expect(result.current.error).toBe(error)
    expect(result.current.value?.asOf).toBe(error ? first : second)
  })
})

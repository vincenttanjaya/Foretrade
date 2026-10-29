import { act, render, renderHook, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ToastProvider, useToast, type ToastKind } from './toast'

function Fire({ msg, kind }: { msg: string; kind?: ToastKind }) {
  const { toast } = useToast()
  return <button onClick={() => toast(msg, kind)}>fire {msg}</button>
}

describe('ToastProvider', () => {
  afterEach(() => vi.useRealTimers())

  it('shows success, info and error toasts with the right roles', async () => {
    render(
      <ToastProvider>
        <Fire msg="Order filled" />
        <Fire msg="Heads up" kind="info" />
        <Fire msg="Order rejected" kind="error" />
      </ToastProvider>,
    )
    await userEvent.click(screen.getByRole('button', { name: 'fire Order filled' }))
    await userEvent.click(screen.getByRole('button', { name: 'fire Heads up' }))
    await userEvent.click(screen.getByRole('button', { name: 'fire Order rejected' }))
    expect(screen.getAllByRole('status').map((s) => s.textContent)).toEqual(['Order filled', 'Heads up'])
    expect(screen.getByRole('alert')).toHaveTextContent('Order rejected')
  })

  it('dismisses on click', async () => {
    render(
      <ToastProvider>
        <Fire msg="Saved" />
      </ToastProvider>,
    )
    await userEvent.click(screen.getByRole('button', { name: 'fire Saved' }))
    await userEvent.click(screen.getByRole('button', { name: 'Dismiss notification' }))
    expect(screen.queryByRole('status')).toBeNull()
  })

  it('auto-dismisses after 4 s (errors after 7 s) and keeps at most four', () => {
    vi.useFakeTimers()
    const { result } = renderHook(() => useToast(), { wrapper: ToastProvider })
    act(() => {
      result.current.toast('ok')
      result.current.toast('bad', 'error')
    })
    act(() => vi.advanceTimersByTime(4000))
    expect(screen.queryByText('ok')).toBeNull()
    expect(screen.getByText('bad')).toBeInTheDocument()
    act(() => vi.advanceTimersByTime(3000))
    expect(screen.queryByText('bad')).toBeNull()

    act(() => ['1', '2', '3', '4', '5'].forEach((m) => result.current.toast(m)))
    expect(screen.getAllByRole('status').map((s) => s.textContent)).toEqual(['2', '3', '4', '5'])
  })

  it('clears its pending auto-dismiss timers on unmount', () => {
    vi.useFakeTimers()
    const { result, unmount } = renderHook(() => useToast(), { wrapper: ToastProvider })
    act(() => {
      result.current.toast('ok')
      result.current.toast('bad', 'error')
    })
    expect(vi.getTimerCount()).toBe(2)
    act(() => vi.advanceTimersByTime(4000)) // a fired timer leaves the set
    expect(vi.getTimerCount()).toBe(1)
    unmount()
    expect(vi.getTimerCount()).toBe(0) // nothing left to fire into a torn-down DOM
  })

  it('useToast throws outside the provider', () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    expect(() => renderHook(() => useToast())).toThrow('useToast must be used inside <ToastProvider>')
  })
})

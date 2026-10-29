import { act, fireEvent, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { ApiError } from '../lib/api'
import * as fx from '../test/fixtures'
import { ToastProvider, useToast } from './toast'
import { CreditsUsed, UserBadges } from './UserBadges'
import { Bars } from './ui/Bars'
import { ErrorState } from './ui/EmptyState'
import { ConfirmDialog, Modal } from './ui/Modal'
import { PasswordReveal } from './ui/PasswordReveal'
import { Sparkline } from './ui/Sparkline'

describe('Bars', () => {
  const data = fx.days([1, ...Array(28).fill(0), 42])

  it('labels every bar with its UTC day and value, and sums the window', () => {
    render(<Bars data={data} label="Credits per day" unit="credit" />)
    const group = screen.getByRole('group', { name: 'Credits per day' })
    const bars = within(group).getAllByRole('img')
    expect(bars).toHaveLength(30)
    expect(bars[0]).toHaveAccessibleName('Sep 4: 1 credit')
    expect(bars[29]).toHaveAccessibleName('Oct 3: 42 credits')
    expect(screen.getByText(/43 credits · peak 42/)).toBeInTheDocument()
  })

  it('is one tab stop; arrows, Home and End move between bars', async () => {
    const user = userEvent.setup()
    render(<Bars data={data} label="Usage" unit="credit" limit={40} />)
    const bars = screen.getAllByRole('img')
    expect(bars.filter((b) => b.tabIndex === 0)).toEqual([bars[29]]) // the newest day
    await user.tab()
    expect(bars[29]).toHaveFocus()
    await user.keyboard('{ArrowRight}') // already at the end
    expect(bars[29]).toHaveFocus()
    await user.keyboard('{ArrowLeft}')
    expect(bars[28]).toHaveFocus()
    await user.keyboard('{Home}')
    expect(bars[0]).toHaveFocus()
    await user.keyboard('{End}')
    expect(bars[29]).toHaveFocus()
    await user.keyboard('a') // other keys do nothing
    expect(bars[29]).toHaveFocus()
    expect(bars[29].firstElementChild).toHaveClass('bg-down') // over the 40 limit
    expect(screen.getByText(/limit 40/)).toBeInTheDocument()
  })

  it('says so when there is no data', () => {
    render(<Bars data={[]} label="Usage" unit="credit" />)
    expect(screen.getByText('No data yet.')).toBeInTheDocument()
  })
})

describe('Sparkline', () => {
  it('draws a decorative line, or an empty box for fewer than two points', () => {
    const { container, rerender } = render(<Sparkline values={[0, 2, 1]} />)
    expect(container.querySelector('polyline')?.getAttribute('points')).toBe('0.0,22.0 48.0,2.0 96.0,12.0')
    expect(container.querySelector('svg')).toHaveAttribute('aria-hidden', 'true')
    rerender(<Sparkline values={[5]} />)
    expect(container.querySelector('polyline')).toBeNull()
  })
})

describe('Modal and ConfirmDialog', () => {
  function Harness({ onConfirm }: { onConfirm: () => Promise<unknown> }) {
    const [open, setOpen] = useState(false)
    return (
      <>
        <button onClick={() => setOpen(true)}>Open</button>
        <ConfirmDialog open={open} onClose={() => setOpen(false)} title="Reset it?" body="It can't be undone." confirmLabel="Reset" onConfirm={onConfirm} />
      </>
    )
  }

  it('runs the action only on confirm, then closes', async () => {
    const user = userEvent.setup()
    const onConfirm = vi.fn().mockResolvedValue(undefined)
    render(<Harness onConfirm={onConfirm} />)
    await user.click(screen.getByRole('button', { name: 'Open' }))
    const dialog = screen.getByRole('dialog', { name: 'Reset it?' })
    expect(dialog).toHaveTextContent("It can't be undone.")
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    expect(onConfirm).not.toHaveBeenCalled()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Open' }))
    await user.click(screen.getByRole('button', { name: 'Reset' }))
    expect(onConfirm).toHaveBeenCalledTimes(1)
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('keeps the dialog open with the API message when the action fails', async () => {
    const user = userEvent.setup()
    render(<Harness onConfirm={() => Promise.reject(new ApiError(409, 'conflict', 'At least one active admin must remain.'))} />)
    await user.click(screen.getByRole('button', { name: 'Open' }))
    await user.click(screen.getByRole('button', { name: 'Reset' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('At least one active admin must remain.')
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })

  it('blocks Escape while locked and closes on it otherwise', () => {
    const onClose = vi.fn()
    const { rerender } = render(
      <Modal open onClose={onClose} title="T" locked>
        body
      </Modal>,
    )
    const dialog = screen.getByRole('dialog', { name: 'T' }) as HTMLDialogElement
    const cancel = new Event('cancel', { cancelable: true })
    dialog.dispatchEvent(cancel)
    expect(cancel.defaultPrevented).toBe(true)
    expect(fireEvent.keyDown(document.body, { key: 'Escape' })).toBe(false) // swallowed: Chrome's 2nd Escape can't close it
    rerender(
      <Modal open onClose={onClose} title="T">
        body
      </Modal>,
    )
    const cancel2 = new Event('cancel', { cancelable: true })
    dialog.dispatchEvent(cancel2)
    expect(cancel2.defaultPrevented).toBe(false)
    expect(fireEvent.keyDown(document.body, { key: 'Escape' })).toBe(true)
    act(() => dialog.close()) // what the browser does after an unprevented cancel
    expect(onClose).toHaveBeenCalledTimes(1)
  })
})

describe('PasswordReveal', () => {
  it('shows the password with a warning and copies it', async () => {
    const user = userEvent.setup()
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal('navigator', { ...navigator, clipboard: { writeText } })
    const onDone = vi.fn()
    render(
      <ToastProvider>
        <PasswordReveal email="ann@example.com" password="ABCDEFGHJKMNPQRSTVWXYZ2345" onDone={onDone} />
      </ToastProvider>,
    )
    expect(screen.getByText(/Shown once\./)).toBeInTheDocument()
    expect(screen.getByLabelText('Temporary password')).toHaveValue('ABCDEFGHJKMNPQRSTVWXYZ2345')
    expect(screen.getByLabelText('Temporary password')).toHaveFocus()
    await user.click(screen.getByRole('button', { name: 'Copy' }))
    expect(writeText).toHaveBeenCalledWith('ABCDEFGHJKMNPQRSTVWXYZ2345')
    expect(screen.getByRole('button', { name: 'Copied' })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: "I've saved it" }))
    expect(onDone).toHaveBeenCalled()
  })

  it('tells the admin to copy by hand when the clipboard is blocked', async () => {
    const user = userEvent.setup()
    vi.stubGlobal('navigator', { ...navigator, clipboard: { writeText: vi.fn().mockRejectedValue(new Error('denied')) } })
    render(
      <ToastProvider>
        <PasswordReveal email="ann@example.com" password="pw-pw-pw-pw-pw" onDone={() => {}} />
      </ToastProvider>,
    )
    await user.click(screen.getByRole('button', { name: 'Copy' }))
    expect(await screen.findByRole('alert')).toHaveTextContent("Couldn't copy.")
  })
})

describe('toasts', () => {
  function Fire() {
    const { toast } = useToast()
    return (
      <>
        <button onClick={() => toast('Saved.')}>ok</button>
        <button onClick={() => toast('Nope.', 'error')}>bad</button>
        <button onClick={() => toast('FYI.', 'info')}>info</button>
      </>
    )
  }

  it('shows success, error and info toasts, dismissable and timed', async () => {
    vi.useFakeTimers()
    render(
      <ToastProvider>
        <Fire />
      </ToastProvider>,
    )
    act(() => screen.getByRole('button', { name: 'ok' }).click())
    act(() => screen.getByRole('button', { name: 'bad' }).click())
    act(() => screen.getByRole('button', { name: 'info' }).click())
    expect(screen.getAllByRole('status').map((s) => s.textContent)).toEqual(['Saved.', 'FYI.'])
    expect(screen.getByRole('alert')).toHaveTextContent('Nope.')
    act(() => screen.getAllByRole('button', { name: 'Dismiss notification' })[0].click())
    expect(screen.queryByText('Saved.')).not.toBeInTheDocument()
    act(() => vi.advanceTimersByTime(7000))
    expect(screen.queryByText('Nope.')).not.toBeInTheDocument()
    vi.useRealTimers()
  })

  it('needs its provider', () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    expect(() => render(<Fire />)).toThrow('useToast must be used inside <ToastProvider>')
  })
})

describe('user badges and states', () => {
  it('shows role, status and managed badges, and credits against the limit', () => {
    render(
      <>
        <UserBadges user={fx.adminUser({ role: 'admin', status: 'disabled', managed: true })} />
        <CreditsUsed user={fx.adminUser({ creditsToday: 12, creditLimit: null })} defaultLimit={500} />
        <CreditsUsed user={fx.adminUser({ creditsToday: 3, creditLimit: 100 })} defaultLimit={500} />
      </>,
    )
    expect(screen.getByText('admin')).toBeInTheDocument()
    expect(screen.getByText('disabled')).toBeInTheDocument()
    expect(screen.getByText('managed')).toBeInTheDocument()
    expect(screen.getByTitle('Default limit')).toHaveTextContent('500')
    expect(screen.getByTitle('Custom limit')).toHaveTextContent('100')
  })

  it('ErrorState shows the message and retries', async () => {
    const onRetry = vi.fn()
    render(<ErrorState message="Request failed (500)" onRetry={onRetry} />)
    expect(screen.getByRole('alert')).toHaveTextContent('Request failed (500)')
    await userEvent.setup().click(screen.getByRole('button', { name: 'Retry' }))
    expect(onRetry).toHaveBeenCalled()
  })
})

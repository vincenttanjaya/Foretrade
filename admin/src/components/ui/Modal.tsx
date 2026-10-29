import { useEffect, useId, useRef, useState, type ReactNode } from 'react'
import { errorMessage } from '../../lib/api'
import { cx } from '../../lib/cx'
import { Button } from './Button'
import { IconAlert } from './icons'

/**
 * Native modal <dialog>: focus trap, Escape and the backdrop come free. The body only renders while open, so form
 * state inside it is gone after a close. `locked` blocks Escape (a request in flight, a password still on screen).
 */
export function Modal({
  open,
  onClose,
  title,
  label,
  locked,
  className,
  children,
}: {
  open: boolean
  onClose: () => void
  title: ReactNode
  label?: ReactNode
  locked?: boolean
  className?: string
  children: ReactNode
}) {
  const ref = useRef<HTMLDialogElement>(null)
  const titleId = useId()

  useEffect(() => {
    const d = ref.current
    if (open && !d?.open) d?.showModal()
    if (!open && d?.open) d.close()
  }, [open])

  // Chrome ignores a second prevented `cancel` in a row and closes anyway, so while locked swallow the Escape key itself.
  // On the document: focus can sit on <body> (the control that had it was replaced, e.g. a form by PasswordReveal).
  useEffect(() => {
    if (!open || !locked) return
    const swallow = (e: KeyboardEvent) => e.key === 'Escape' && e.preventDefault()
    document.addEventListener('keydown', swallow)
    return () => document.removeEventListener('keydown', swallow)
  }, [open, locked])

  return (
    <dialog
      ref={ref}
      aria-labelledby={titleId}
      onClose={() => open && onClose()}
      onCancel={(e) => locked && e.preventDefault()}
      className={cx(
        'card m-auto max-h-[calc(100dvh-2rem)] w-[calc(100%-2rem)] max-w-md animate-fade-up overflow-y-auto p-6 text-text backdrop:bg-[rgb(15_27_45/0.45)] backdrop:backdrop-blur-[2px]',
        className,
      )}
    >
      {open && (
        <>
          {label && <div className="label mb-2">{label}</div>}
          <h2 id={titleId} className="font-display text-2xl leading-tight font-medium text-balance">
            {title}
          </h2>
          {children}
        </>
      )}
    </dialog>
  )
}

/** Inline error inside a form or dialog; API messages are shown verbatim. */
export function FormError({ children, id }: { children: ReactNode; id?: string }) {
  return (
    <div
      id={id}
      role="alert"
      className="mt-4 flex items-start gap-2.5 rounded-lg border border-down/25 bg-down/6 px-3 py-2.5 text-[13.5px] leading-snug text-down"
    >
      <IconAlert size={16} className="mt-px shrink-0" />
      <span className="min-w-0 break-words">{children}</span>
    </div>
  )
}

/** "Are you sure?" for a destructive action: runs `onConfirm`, closes on success, shows the error inline on failure. */
export function ConfirmDialog({
  open,
  onClose,
  title,
  body,
  confirmLabel,
  onConfirm,
  label = 'Confirm',
}: {
  open: boolean
  onClose: () => void
  title: ReactNode
  body: ReactNode
  confirmLabel: string
  onConfirm: () => Promise<unknown>
  label?: ReactNode
}) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const close = () => {
    setError(null)
    onClose()
  }

  async function run() {
    setBusy(true)
    setError(null)
    try {
      await onConfirm()
      close()
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal open={open} onClose={close} title={title} label={label} locked={busy}>
      <div className="mt-2.5 text-sm leading-relaxed text-muted">{body}</div>
      {error && <FormError>{error}</FormError>}
      <div className="mt-6 flex flex-wrap justify-end gap-2">
        <Button variant="secondary" disabled={busy} onClick={close}>
          Cancel
        </Button>
        <Button variant="danger" loading={busy} onClick={run}>
          {confirmLabel}
        </Button>
      </div>
    </Modal>
  )
}

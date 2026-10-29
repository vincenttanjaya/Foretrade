import { createContext, use, useCallback, useMemo, useState, type ReactNode } from 'react'
import { cx } from '../lib/cx'
import { IconAlert, IconCheck, IconInfo, IconX } from './ui/icons'

export type ToastKind = 'success' | 'error' | 'info'
type Toast = { id: number; msg: ReactNode; kind: ToastKind } // msg may hold a link, e.g. to a running run

const ToastContext = createContext<{ toast: (msg: ReactNode, kind?: ToastKind) => void } | null>(null)

let nextId = 1

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([])

  const dismiss = useCallback((id: number) => setToasts((ts) => ts.filter((t) => t.id !== id)), [])

  const toast = useCallback(
    (msg: ReactNode, kind: ToastKind = 'success') => {
      const id = nextId++
      setToasts((ts) => [...ts.slice(-3), { id, msg, kind }]) // at most 4 on screen
      setTimeout(() => dismiss(id), kind === 'error' ? 7000 : 4000)
    },
    [dismiss],
  )

  const value = useMemo(() => ({ toast }), [toast])

  return (
    <ToastContext value={value}>
      {children}
      <div
        aria-live="polite"
        className="pointer-events-none fixed inset-x-4 bottom-[calc(76px+env(safe-area-inset-bottom))] z-50 flex flex-col items-center gap-2 md:inset-x-auto md:right-6 md:bottom-6 md:items-end"
      >
        {toasts.map((t) => (
          <div
            key={t.id}
            role={t.kind === 'error' ? 'alert' : 'status'}
            className="card pointer-events-auto flex w-full max-w-sm animate-fade-up items-start gap-3 py-3 pr-2 pl-3.5 shadow-[0_12px_32px_-12px_rgb(15_27_45/0.28)]"
          >
            <span
              className={cx(
                'mt-px grid size-5 shrink-0 place-items-center rounded-full',
                t.kind === 'success' && 'bg-up/12 text-up',
                t.kind === 'error' && 'bg-down/12 text-down',
                t.kind === 'info' && 'bg-accent-soft text-accent-ink',
              )}
            >
              {t.kind === 'success' ? <IconCheck size={13} strokeWidth={2.4} /> : t.kind === 'error' ? <IconAlert size={14} strokeWidth={2} /> : <IconInfo size={14} strokeWidth={2} />}
            </span>
            <p className="min-w-0 flex-1 py-px text-[13.5px] leading-snug text-text">{t.msg}</p>
            <button
              type="button"
              onClick={() => dismiss(t.id)}
              aria-label="Dismiss notification"
              className="grid size-6 shrink-0 place-items-center rounded-md text-muted transition-colors hover:bg-surface-2 hover:text-text"
            >
              <IconX size={14} />
            </button>
          </div>
        ))}
      </div>
    </ToastContext>
  )
}

/** const { toast } = useToast(); toast('Order filled', 'success') */
export function useToast() {
  const ctx = use(ToastContext)
  if (!ctx) throw new Error('useToast must be used inside <ToastProvider>')
  return ctx
}

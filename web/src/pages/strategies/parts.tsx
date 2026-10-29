// Hooks and pieces shared by the strategy studio and the AI portfolio pages.
import { useEffect, useEffectEvent, useRef, useState, type ReactNode, type RefObject } from 'react'
import { Link } from 'react-router'
import { buttonClass, Button } from '../../components/ui/Button'
import { Card } from '../../components/ui/Card'
import { EmptyState } from '../../components/ui/EmptyState'
import { IconAlert } from '../../components/ui/icons'
import { Textarea } from '../../components/ui/Input'
import { Spinner } from '../../components/ui/Spinner'
import { ApiError } from '../../lib/api'
import { useAuth } from '../../lib/auth'
import { cx } from '../../lib/cx'
import { errorMessage, QUOTA_MESSAGE } from '../../lib/errors'
import { AI_COST } from './strategy'

/**
 * Loads once on mount (skipped when `initial` is given, e.g. from router state) and again on retry().
 * `data` stays null until the first answer; `error` is the raw thrown value.
 */
export function useLoad<T>(load: (signal: AbortSignal) => Promise<T>, initial: T | null = null) {
  const [data, setData] = useState<T | null>(initial)
  const [error, setError] = useState<unknown>(null)
  const [nonce, setNonce] = useState(0)
  const [seeded] = useState(initial !== null)
  const run = useEffectEvent(load)

  useEffect(() => {
    if (nonce === 0 && seeded) return
    const ctrl = new AbortController()
    setError(null)
    run(ctrl.signal).then(
      (d) => setData(() => d),
      (err) => !ctrl.signal.aborted && setError(err),
    )
    return () => ctrl.abort()
  }, [nonce])

  return { data, error, retry: () => setNonce((x) => x + 1) }
}

/** Whole seconds since `active` turned true (0 while inactive). */
export function useElapsed(active: boolean) {
  const [s, setS] = useState(0)
  useEffect(() => {
    if (!active) return setS(0)
    const start = Date.now()
    const timer = setInterval(() => setS(Math.floor((Date.now() - start) / 1000)), 1000)
    return () => clearInterval(timer)
  }, [active])
  return s
}

/** False once the component unmounted: a slow AI call must not navigate a user who already left. */
export function useAlive() {
  const alive = useRef(true)
  useEffect(() => {
    alive.current = true
    return () => {
      alive.current = false
    }
  }, [])
  return alive
}

/** Credits left today, or null before /api/me answers. */
export function useCreditsLeft() {
  const { credits } = useAuth()
  return credits ? Math.max(0, credits.limit - credits.used) : null
}

export type Stage = { at: number; text: string } // seconds since start → message

/** The stage showing after `elapsed` seconds (the last one whose `at` has passed). */
export const stageAt = <S extends Stage>(stages: S[], elapsed: number) =>
  stages[Math.max(0, stages.findLastIndex((st) => elapsed >= st.at))]

/** Prompt box: textarea, a footer for extra controls, and the AI submit button (⌘/Ctrl+Enter submits). */
export function PromptCard({
  value,
  onChange,
  onSubmit,
  busy,
  min,
  max,
  placeholder,
  label,
  submitLabel,
  busyLabel,
  controls,
  inputRef,
}: {
  value: string
  onChange: (v: string) => void
  onSubmit: () => void
  busy: boolean
  min: number
  max: number
  placeholder: string
  label: string
  submitLabel: string
  busyLabel: string
  controls?: ReactNode
  inputRef?: RefObject<HTMLTextAreaElement | null>
}) {
  const left = useCreditsLeft()
  const outOfCredits = left != null && left < AI_COST
  const ready = value.trim().length >= min && !outOfCredits

  return (
    <Card className="p-2 transition-[border-color,box-shadow] has-[textarea:focus]:border-accent has-[textarea:focus]:ring-[3px] has-[textarea:focus]:ring-accent/20">
      <Textarea
        ref={inputRef}
        aria-label={label}
        value={value}
        maxLength={max}
        rows={3}
        disabled={busy}
        placeholder={placeholder}
        onChange={(e) => onChange(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
            e.preventDefault()
            if (ready) onSubmit()
          }
        }}
        className="max-h-64 min-h-28 border-0! bg-transparent! px-3 py-3 text-[16.5px] focus:ring-0!"
      />
      <div className="flex flex-wrap items-center gap-2 border-t border-border px-1 pt-2.5 pb-0.5 sm:px-2">
        {controls}
        <span className="font-mono text-[11px] text-muted tabular-nums">
          {value.length}/{max}
          <span className="hidden lg:inline"> · ⌘/Ctrl + Enter</span>
        </span>
        <Button
          variant="accent"
          onClick={onSubmit}
          loading={busy}
          disabled={!ready}
          title={outOfCredits ? QUOTA_MESSAGE : undefined}
          className="w-full sm:ml-auto sm:w-auto"
        >
          {busy ? busyLabel : submitLabel}
          <span className="font-mono text-[11px] font-normal opacity-70">· {AI_COST} cr</span>
        </Button>
      </div>
    </Card>
  )
}

/** Live line under a prompt box: the error, the current stage while busy, or a hint. Fixed height. */
export function PromptStatus({ error, busy, stages, hint }: { error: string | null; busy: boolean; stages: Stage[]; hint?: ReactNode }) {
  const elapsed = useElapsed(busy)
  const left = useCreditsLeft()
  const stage = stageAt(stages, elapsed)
  const quota = !busy && !error && left != null && left < AI_COST

  return (
    <div aria-live="polite" className="mt-3 min-h-6 text-[13px]">
      {error ? (
        <p role="alert" className="flex items-start gap-2 text-down">
          <IconAlert size={16} className="mt-px shrink-0" />
          {error}
        </p>
      ) : busy ? (
        <p className="flex items-center gap-2 font-mono text-[11.5px] tracking-wide text-muted">
          <Spinner className="size-3.5 text-accent" />
          <span key={stage.text} className="animate-fade-up">
            {stage.text}
          </span>
          <span aria-hidden="true" className="tabular-nums opacity-70">
            · {elapsed}s
          </span>
        </p>
      ) : quota ? (
        <p className="text-muted">{QUOTA_MESSAGE}</p>
      ) : (
        hint
      )}
    </div>
  )
}

/** Example prompt chips; the selected one is highlighted. */
export function ExampleChips({
  items,
  selected,
  disabled,
  onPick,
}: {
  items: readonly string[]
  selected: string
  disabled: boolean
  onPick: (text: string) => void
}) {
  return (
    <ul className="flex flex-wrap gap-2">
      {items.map((ex) => (
        <li key={ex} className="max-w-full">
          <button
            type="button"
            disabled={disabled}
            onClick={() => onPick(ex)}
            aria-pressed={selected.trim() === ex}
            className={cx(
              'max-w-full rounded-full border px-3.5 py-1.5 text-left text-[13px] leading-snug transition-colors disabled:opacity-55',
              selected.trim() === ex
                ? 'border-accent bg-accent-soft text-text'
                : 'border-border bg-surface text-text hover:border-accent/50 hover:bg-accent-soft/40',
            )}
          >
            {ex}
          </button>
        </li>
      ))}
    </ul>
  )
}

/** Full-width load failure: 404 gets "not found" copy and a way back, anything else a retry. */
export function LoadError({ error, what, back, onRetry }: { error: unknown; what: string; back: { to: string; label: string }; onRetry: () => void }) {
  const missing = error instanceof ApiError && error.status === 404
  return (
    <EmptyState
      icon={<IconAlert size={20} />}
      title={missing ? `This ${what} doesn't exist.` : `Couldn't load this ${what}.`}
      description={missing ? 'It may have been deleted, or the link is wrong.' : errorMessage(error)}
      action={
        <div className="flex flex-wrap justify-center gap-2">
          {!missing && (
            <Button variant="primary" onClick={onRetry}>
              Try again
            </Button>
          )}
          <Link to={back.to} className={buttonClass('secondary')}>
            {back.label}
          </Link>
        </div>
      }
    />
  )
}

/** "← Strategies" style link above a detail page. */
export function BackLink({ to, children }: { to: string; children: ReactNode }) {
  return (
    <Link
      to={to}
      className="mb-6 inline-flex items-center gap-1.5 rounded-md text-[13px] font-medium text-muted transition-colors hover:text-text"
    >
      <span aria-hidden="true">←</span> {children}
    </Link>
  )
}

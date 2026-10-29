import type { ComponentProps } from 'react'
import { cx } from '../../lib/cx'
import { Spinner } from './Spinner'

export type ButtonVariant = 'primary' | 'secondary' | 'ghost' | 'accent' | 'danger'
export type ButtonSize = 'sm' | 'md' | 'lg'

const variants: Record<ButtonVariant, string> = {
  primary: 'bg-ink text-ink-fg shadow-sm hover:bg-ink/88',
  secondary: 'border border-border bg-surface text-text hover:border-muted/50 hover:bg-surface-2',
  ghost: 'text-muted hover:bg-surface-2 hover:text-text',
  accent: 'bg-accent text-[#17120a] shadow-sm hover:brightness-[1.06]',
  danger: 'border border-down/40 bg-surface text-down hover:border-down/70 hover:bg-down/8',
}

const sizes: Record<ButtonSize, string> = {
  sm: 'h-8 px-3 text-[13px] rounded-lg',
  md: 'h-10 px-4 text-sm rounded-lg',
  lg: 'h-12 px-6 text-[15px] rounded-xl',
}

/** Button classes for links styled as buttons: <Link className={buttonClass('secondary')}>. */
export function buttonClass(variant: ButtonVariant = 'primary', size: ButtonSize = 'md', className?: string) {
  return cx(
    'inline-flex items-center justify-center gap-2 font-medium whitespace-nowrap select-none',
    'transition-[background-color,border-color,color,filter,transform] duration-150 active:translate-y-px',
    'disabled:pointer-events-none [&:disabled:not([aria-busy])]:opacity-45',
    variants[variant],
    sizes[size],
    className,
  )
}

type ButtonProps = ComponentProps<'button'> & { variant?: ButtonVariant; size?: ButtonSize; loading?: boolean }

export function Button({ variant, size, loading, disabled, className, children, type = 'button', ...rest }: ButtonProps) {
  return (
    <button
      type={type}
      disabled={disabled || loading}
      aria-busy={loading || undefined}
      className={buttonClass(variant, size, className)}
      {...rest}
    >
      {loading && <Spinner className="size-4" />}
      {children}
    </button>
  )
}

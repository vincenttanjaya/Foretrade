import { useState, type FormEvent } from 'react'
import { Navigate, useLocation } from 'react-router'
import { AdminBadge, ThemeToggle } from '../components/layout/AdminShell'
import { Button } from '../components/ui/Button'
import { IconArrowRight, IconEye, IconEyeOff } from '../components/ui/icons'
import { Input, Label } from '../components/ui/Input'
import { LogoMark } from '../components/ui/Logo'
import { FormError } from '../components/ui/Modal'
import { ApiError } from '../lib/api'
import { useAuth } from '../lib/auth'

function loginError(err: unknown) {
  if (err instanceof ApiError) {
    if (err.code === 'invalid_credentials') return 'Wrong email or password.'
    if (err.code === 'restricted' || err.code === 'rate_limited' || err.status === 0) return err.message
  }
  return 'Something went wrong while signing in. Please try again.'
}

export function LoginPage() {
  const { user, login } = useAuth()
  const location = useLocation()
  const from = (location.state as { from?: string } | null)?.from ?? '/'
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [showPassword, setShowPassword] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  if (user) return <Navigate to={from} replace />

  async function onSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    setBusy(true)
    try {
      await login(email.trim(), password) // on success `user` is set and the <Navigate> above takes over
    } catch (err) {
      setError(loginError(err))
      setPassword('')
      setBusy(false)
    }
  }

  const invalid = error ? { 'aria-invalid': true, 'aria-describedby': 'login-error' } : {}

  return (
    <div className="min-h-dvh bg-bg text-text lg:grid lg:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
      {/* Editorial hero: fixed navy in both themes, as in the trade app. */}
      <section className="relative flex flex-col overflow-hidden bg-[#0F1B2D] px-6 py-7 text-[#F4F1EA] sm:px-10 sm:py-9 lg:min-h-dvh lg:px-14 lg:py-12">
        <div
          aria-hidden="true"
          className="pointer-events-none absolute inset-0 opacity-[0.07]"
          style={{
            backgroundImage: 'linear-gradient(#fff 1px, transparent 1px), linear-gradient(90deg, #fff 1px, transparent 1px)',
            backgroundSize: '56px 56px',
            maskImage: 'radial-gradient(ellipse 80% 70% at 30% 40%, #000 25%, transparent 75%)',
          }}
        />
        <div aria-hidden="true" className="pointer-events-none absolute -top-48 -right-40 size-[560px] rounded-full bg-[#C8962E]/18 blur-[120px]" />

        <header className="relative flex items-center gap-3">
          <LogoMark size={36} />
          <div>
            <div className="font-display text-[20px] leading-none tracking-[-0.01em]">Foretrade</div>
            <div className="mt-1 font-mono text-[9.5px] font-medium tracking-[0.2em] text-[#E0B252]">ADMIN CONSOLE</div>
          </div>
        </header>

        <div className="relative max-w-xl py-10 sm:py-14 lg:my-auto lg:py-10">
          <div className="font-mono text-[10.5px] font-medium tracking-[0.18em] text-[#E0B252]">ADMINISTRATORS ONLY</div>
          <h1 className="mt-5 font-display text-[44px] leading-[0.98] font-normal tracking-[-0.025em] text-balance sm:text-[58px] lg:text-[68px]">
            Operator <em className="text-[#E0B252]">console.</em>
          </h1>
          <p className="mt-6 max-w-md text-[15.5px] leading-relaxed text-[#F4F1EA]/70">
            Manage Foretrade accounts, credit limits and sessions, and watch usage and system health in one place.
          </p>
        </div>

        <footer className="relative hidden justify-between font-mono text-[10px] tracking-[0.16em] text-[#F4F1EA]/45 lg:flex">
          <span>EVERY CHANGE IS AUDITED</span>
          <span>OPEN SOURCE · SELF-HOSTED</span>
        </footer>
      </section>

      <section className="flex min-h-[60dvh] flex-col px-6 py-6 sm:px-10 lg:min-h-dvh lg:py-8">
        <div className="flex justify-end">
          <ThemeToggle />
        </div>
        <div className="m-auto w-full max-w-[380px] animate-fade-up py-8">
          <AdminBadge />
          <h2 className="mt-4 font-display text-[36px] leading-tight font-medium tracking-[-0.015em]">Sign in</h2>
          <p className="mt-2 text-[14.5px] leading-relaxed text-muted">
            Use your Foretrade administrator account. Other accounts are signed out.
          </p>

          <form className="mt-8 space-y-5" onSubmit={onSubmit}>
            <div>
              <Label htmlFor="email">Email</Label>
              <Input
                id="email"
                type="email"
                autoComplete="username"
                autoFocus
                required
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                className="h-11"
                placeholder="admin@example.com"
                {...invalid}
              />
            </div>
            <div>
              <Label htmlFor="password">Password</Label>
              <div className="relative">
                <Input
                  id="password"
                  type={showPassword ? 'text' : 'password'}
                  autoComplete="current-password"
                  required
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  className="h-11 pr-11"
                  {...invalid}
                />
                <button
                  type="button"
                  onClick={() => setShowPassword((s) => !s)}
                  aria-label={showPassword ? 'Hide password' : 'Show password'}
                  aria-pressed={showPassword}
                  className="absolute top-1/2 right-1.5 grid size-8 -translate-y-1/2 place-items-center rounded-md text-muted transition-colors hover:bg-surface-2 hover:text-text"
                >
                  {showPassword ? <IconEyeOff size={17} /> : <IconEye size={17} />}
                </button>
              </div>
            </div>

            {error && <FormError id="login-error">{error}</FormError>}

            <Button type="submit" size="lg" loading={busy} className="w-full">
              {busy ? 'Signing in…' : 'Sign in'}
              {!busy && <IconArrowRight size={17} />}
            </Button>
          </form>
        </div>
        <p className="text-center font-mono text-[10px] tracking-[0.16em] text-muted lg:hidden">OPEN SOURCE · SELF-HOSTED</p>
      </section>
    </div>
  )
}

import { useState, type FormEvent } from 'react'
import { Navigate, useLocation } from 'react-router'
import { ThemeToggle } from '../components/layout/AppShell'
import { Button } from '../components/ui/Button'
import { IconAlert, IconArrowRight, IconEye, IconEyeOff } from '../components/ui/icons'
import { Input, Label } from '../components/ui/Input'
import { LogoMark } from '../components/ui/Logo'
import { FullPageLoader } from '../components/ui/Spinner'
import { ApiError } from '../lib/api'
import { useAuth } from '../lib/auth'

const FEATURES = [
  {
    title: 'Agent',
    body: 'Ask about any market. Answers come with levels, a price outlook and a trade card you confirm.',
  },
  {
    title: 'Forecast desk',
    body: 'Three specialist agents and an aggregator estimate probabilities for the questions you care about.',
  },
  {
    title: 'Strategies & portfolios',
    body: 'One sentence becomes a rule-based strategy or a weighted basket, backtested on real prices.',
  },
]

function loginError(err: unknown) {
  if (err instanceof ApiError) {
    if (err.code === 'invalid_credentials') return "That email and password don't match."
    if (err.code === 'rate_limited') return 'Too many sign-in attempts. Wait a few minutes, then try again.'
    // The API's origin check (ALLOWED_ORIGINS): e.g. the app opened at a LAN IP the server doesn't list.
    if (err.code === 'forbidden') return "Sign-in isn't allowed from this address. Open the app at the address your administrator set up."
    if (err.status === 0) return err.message
  }
  return 'Something went wrong while signing in. Please try again.'
}

export function LoginPage() {
  const { user, loading, login } = useAuth()
  const location = useLocation()
  const from = (location.state as { from?: string } | null)?.from ?? '/'
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [showPassword, setShowPassword] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  if (loading) return <FullPageLoader />
  if (user) return <Navigate to={from} replace />

  async function onSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    setBusy(true)
    try {
      await login(email.trim(), password) // on success `user` is set and the <Navigate> above takes over
    } catch (err) {
      setError(loginError(err))
      setBusy(false)
    }
  }

  return (
    <div className="min-h-dvh bg-bg text-text lg:grid lg:grid-cols-[minmax(0,1.08fr)_minmax(0,1fr)]">
      {/* Editorial hero: fixed navy in both themes. */}
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
        <svg
          aria-hidden="true"
          viewBox="0 0 640 220"
          preserveAspectRatio="none"
          className="pointer-events-none absolute right-0 bottom-0 hidden h-[24%] w-[58%] opacity-70 lg:block"
        >
          <defs>
            <linearGradient id="login-area" x1="0" x2="0" y1="0" y2="1">
              <stop offset="0" stopColor="#E0B252" stopOpacity="0.16" />
              <stop offset="1" stopColor="#E0B252" stopOpacity="0" />
            </linearGradient>
          </defs>
          <path
            d="M0 188 C 50 182 80 146 128 150 S 210 186 262 150 S 340 92 392 104 S 470 70 520 58 S 600 30 640 18 L 640 220 L 0 220 Z"
            fill="url(#login-area)"
          />
          <path
            d="M0 188 C 50 182 80 146 128 150 S 210 186 262 150 S 340 92 392 104 S 470 70 520 58 S 600 30 640 18"
            fill="none"
            stroke="#E0B252"
            strokeOpacity="0.6"
            strokeWidth="1.5"
            vectorEffect="non-scaling-stroke"
          />
        </svg>

        <header className="relative flex items-center gap-3">
          <LogoMark size={36} />
          <div>
            <div className="font-display text-[20px] leading-none tracking-[-0.01em]">Foretrade</div>
            <div className="mt-1 font-mono text-[9.5px] font-medium tracking-[0.2em] text-[#E0B252]">OPEN SOURCE</div>
          </div>
        </header>

        <div className="relative max-w-xl py-10 sm:py-14 lg:my-auto lg:py-10">
          <div className="font-mono text-[10.5px] font-medium tracking-[0.18em] text-[#E0B252]">AI TRADING DESK · PAPER</div>
          <h1 className="mt-5 font-display text-[44px] leading-[0.98] font-normal tracking-[-0.025em] text-balance sm:text-[58px] lg:text-[68px]">
            Trade with an <em className="text-[#E0B252]">AI desk.</em>
          </h1>
          <p className="mt-6 hidden max-w-md text-[15.5px] leading-relaxed text-[#F4F1EA]/70 sm:block">
            Research, forecasts and strategies in one workspace. The AI proposes; you decide, and every order is a paper
            trade.
          </p>
          <ol className="mt-10 hidden gap-0 sm:grid sm:max-w-lg">
            {FEATURES.map((f, i) => (
              <li key={f.title} className="flex gap-5 border-t border-white/10 py-5">
                <span className="pt-0.5 font-mono text-[11px] text-[#E0B252]">0{i + 1}</span>
                <div>
                  <div className="text-[15px] font-medium">{f.title}</div>
                  <p className="mt-1 text-[14px] leading-relaxed text-[#F4F1EA]/60">{f.body}</p>
                </div>
              </li>
            ))}
          </ol>
        </div>

        <footer className="relative hidden justify-between font-mono text-[10px] tracking-[0.16em] text-[#F4F1EA]/45 lg:flex">
          <span>PAPER TRADING · SIMULATED FILLS</span>
          <span>SELF-HOSTED</span>
        </footer>
      </section>

      {/* Sign-in */}
      <section className="flex min-h-[60dvh] flex-col px-6 py-6 sm:px-10 lg:min-h-dvh lg:py-8">
        <div className="flex justify-end">
          <ThemeToggle />
        </div>
        <div className="m-auto w-full max-w-[380px] animate-fade-up py-8">
          <div className="label">Welcome back</div>
          <h2 className="mt-3 font-display text-[36px] leading-tight font-medium tracking-[-0.015em]">Sign in</h2>
          <p className="mt-2 text-[14.5px] leading-relaxed text-muted">Use the account your administrator gave you.</p>

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
                aria-invalid={error ? true : undefined}
                aria-describedby={error ? 'login-error' : undefined}
                className="h-11"
                placeholder="you@example.com"
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
                  aria-invalid={error ? true : undefined}
                  aria-describedby={error ? 'login-error' : undefined}
                  className="h-11 pr-11"
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

            {error && (
              <div
                id="login-error"
                role="alert"
                className="flex items-start gap-2.5 rounded-lg border border-down/25 bg-down/6 px-3 py-2.5 text-[13.5px] leading-snug text-down"
              >
                <IconAlert size={16} className="mt-px shrink-0" />
                {error}
              </div>
            )}

            <Button type="submit" size="lg" loading={busy} className="w-full">
              {busy ? 'Signing in…' : 'Sign in'}
              {!busy && <IconArrowRight size={17} />}
            </Button>
          </form>

          <div className="mt-8 flex gap-3 rounded-xl border border-border bg-surface px-4 py-3.5">
            <span aria-hidden="true" className="mt-[7px] size-1.5 shrink-0 rounded-full bg-accent" />
            <p className="text-[13px] leading-relaxed text-muted">
              {/* Quote-feed stocks fill at a real-time or a delayed price (or the last close out of session), so not "live" for all. */}
              <span className="font-medium text-text">Paper trading only.</span> Orders fill against public market prices
              (for quote-feed stocks, each price says whether it is real-time or delayed) in a simulated ledger. Nothing executes on a real venue, and nothing here is
              financial advice.
            </p>
          </div>
        </div>
        <p className="text-center font-mono text-[10px] tracking-[0.16em] text-muted lg:hidden">OPEN SOURCE · PAPER TRADING ONLY</p>
      </section>
    </div>
  )
}

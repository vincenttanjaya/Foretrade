import { Component, Suspense, useEffect, useRef, type ComponentType, type ReactNode } from 'react'
import { Link, Outlet, useLocation } from 'react-router'
import { useAuth } from '../../lib/auth'
import { cx } from '../../lib/cx'
import { fmtUsd } from '../../lib/format'
import { usePortfolio } from '../../lib/portfolio'
import { useTheme } from '../../lib/theme'
import { Button } from '../ui/Button'
import { EmptyState } from '../ui/EmptyState'
import {
  IconAccount,
  IconAgent,
  IconAlert,
  IconBolt,
  IconForecast,
  IconLogout,
  IconMoon,
  IconNews,
  IconPortfolio,
  IconScreener,
  IconSignal,
  IconStrategy,
  IconSun,
  IconTrade,
} from '../ui/icons'
import { LogoMark } from '../ui/Logo'
import { NotificationsBell } from './NotificationsBell'
import { PriceChange } from '../ui/PriceChange'
import { Skeleton } from '../ui/Spinner'

type NavItem = {
  to: string
  label: string
  title: string
  icon: ComponentType<{ size?: number }>
  match: (path: string) => boolean
  mobile?: false // rail only: the bottom bar keeps eight tabs (Signals is linked from News there, Automations from the bell)
}

const NAV: NavItem[] = [
  { to: '/', label: 'Agent', title: 'Agent', icon: IconAgent, match: (p) => p === '/' || p.startsWith('/chat') },
  { to: '/trade/BTC', label: 'Trade', title: 'Trade', icon: IconTrade, match: (p) => p.startsWith('/trade') },
  { to: '/screener', label: 'Screener', title: 'Asia screener', icon: IconScreener, match: (p) => p.startsWith('/screener') },
  { to: '/news', label: 'News', title: 'Market wire', icon: IconNews, match: (p) => p.startsWith('/news') },
  { to: '/signals', label: 'Signals', title: 'Signals board (momentum screen)', icon: IconSignal, match: (p) => p.startsWith('/signals'), mobile: false },
  { to: '/automations', label: 'Automations', title: 'Automations', icon: IconBolt, match: (p) => p.startsWith('/automations'), mobile: false },
  { to: '/forecasts', label: 'Forecasts', title: 'Forecast desk', icon: IconForecast, match: (p) => p.startsWith('/forecasts') },
  { to: '/strategies', label: 'Strategies', title: 'Strategy studio', icon: IconStrategy, match: (p) => p.startsWith('/strategies') },
  { to: '/portfolios', label: 'Portfolios', title: 'AI portfolios', icon: IconPortfolio, match: (p) => p.startsWith('/portfolios') },
  { to: '/account', label: 'Account', title: 'Paper account', icon: IconAccount, match: (p) => p.startsWith('/account') },
]

/** Rail (bottom tab bar under 768px) + top bar + scrolling content. Pages set their own max-width. */
export function AppShell() {
  const { pathname } = useLocation()
  const main = useRef<HTMLElement>(null)
  const current = NAV.find((n) => n.match(pathname))

  useEffect(() => {
    main.current?.scrollTo(0, 0)
    document.title = current ? `${current.title} · Foretrade` : 'Foretrade'
    return () => {
      document.title = 'Foretrade'
    }
  }, [pathname, current])

  return (
    <div className="flex h-dvh bg-bg text-text print:block print:h-auto">
      <a
        href="#main"
        className="sr-only z-50 rounded-md bg-ink px-3 py-2 text-sm text-ink-fg focus:not-sr-only focus:fixed focus:top-3 focus:left-3"
      >
        Skip to content
      </a>

      <aside className="hidden w-[76px] shrink-0 flex-col items-center border-r border-border bg-surface md:flex print:hidden">
        <Link to="/" aria-label="Foretrade home" className="flex h-[76px] flex-col items-center justify-center gap-1.5 rounded-lg">
          <LogoMark size={34} />
          <span className="hidden font-mono text-[8.5px] font-medium tracking-[0.16em] text-muted lg:block">FORETRADE</span>
        </Link>
        <div className="mb-3 h-px w-8 bg-border" />
        <nav aria-label="Primary" className="flex flex-col gap-1">
          {NAV.map((item) => {
            const active = item.match(pathname)
            return (
              <Link
                key={item.to}
                to={item.to}
                aria-current={active ? 'page' : undefined}
                className="group relative flex w-16 flex-col items-center gap-1 rounded-xl py-2"
              >
                {active && <span className="absolute top-1/2 -left-1.5 h-6 w-[3px] -translate-y-1/2 rounded-r-full bg-accent" />}
                <span
                  className={cx(
                    'grid h-8 w-11 place-items-center rounded-lg transition-colors duration-150',
                    active ? 'bg-accent-soft text-accent-ink' : 'text-muted group-hover:bg-surface-2 group-hover:text-text',
                  )}
                >
                  <item.icon size={19} />
                </span>
                <span
                  className={cx(
                    'text-[10.5px] leading-none transition-colors',
                    active ? 'font-semibold text-text' : 'text-muted group-hover:text-text',
                  )}
                >
                  {item.label}
                </span>
              </Link>
            )
          })}
        </nav>
      </aside>

      <div className="flex min-w-0 flex-1 flex-col">
        <TopBar title={current?.title} />
        {/* `relative` contains absolutely-positioned sr-only text, so it can't widen the page on mobile. */}
        <main id="main" ref={main} tabIndex={-1} className="relative min-h-0 flex-1 overflow-y-auto focus:outline-none print:overflow-visible">
          <PageErrorBoundary pathname={pathname}>
            <Suspense fallback={<PageSkeleton />}>
              <Outlet />
            </Suspense>
          </PageErrorBoundary>
        </main>
        <nav
          aria-label="Primary"
          className="grid grid-cols-8 border-t border-border bg-surface pb-[env(safe-area-inset-bottom)] md:hidden print:hidden"
        >
          {NAV.filter((item) => item.mobile !== false).map((item) => {
            const active = item.match(pathname)
            return (
              <Link
                key={item.to}
                to={item.to}
                aria-current={active ? 'page' : undefined}
                className={cx(
                  'relative flex min-h-14 min-w-0 flex-col items-center justify-center gap-1 text-[9.5px] leading-none tracking-[-0.01em] transition-colors',
                  active ? 'font-semibold text-accent-ink' : 'text-muted',
                )}
              >
                {active && <span className="absolute top-0 h-[2px] w-8 rounded-b-full bg-accent" />}
                <item.icon size={20} />
                {/* Eight tabs: labels go screen-reader-only on the narrowest phones. */}
                <span className="max-w-full truncate max-[359px]:sr-only">{item.label}</span>
              </Link>
            )
          })}
        </nav>
      </div>
    </div>
  )
}

/** In-shell placeholder while a route's code chunk loads. */
function PageSkeleton() {
  return (
    <div role="status" aria-label="Loading page" className="mx-auto max-w-6xl px-4 py-8 md:px-8 md:py-10">
      <Skeleton className="h-3 w-28" />
      <Skeleton className="mt-4 h-10 w-80 max-w-full" />
      <Skeleton className="mt-3 h-4 w-[28rem] max-w-full" />
      <Skeleton className="mt-10 h-72 w-full rounded-[14px]" />
    </div>
  )
}

/**
 * Keeps the shell up when a page throws while rendering or its code chunk fails to load. Not keyed by
 * pathname (that would remount pages on /chat/:id or /trade/:symbol changes); a navigation clears it instead.
 */
class PageErrorBoundary extends Component<{ pathname: string; children: ReactNode }, { failed: boolean }> {
  state = { failed: false }
  static getDerivedStateFromError() {
    return { failed: true }
  }
  componentDidUpdate(prev: { pathname: string }) {
    if (this.state.failed && prev.pathname !== this.props.pathname) this.setState({ failed: false })
  }
  render() {
    if (!this.state.failed) return this.props.children
    return (
      <div className="mx-auto max-w-xl px-4 py-20">
        <EmptyState
          icon={<IconAlert size={20} />}
          title="This page failed to load."
          description="The app may have been updated since you opened it. Reloading usually fixes it."
          action={<Button onClick={() => location.reload()}>Reload</Button>}
        />
      </div>
    )
  }
}

function TopBar({ title }: { title?: string }) {
  const { credits } = useAuth()
  const { portfolio } = usePortfolio()
  const left = credits ? Math.max(0, credits.limit - credits.used) : null
  const low = credits != null && left != null && left < credits.limit * 0.1

  return (
    <header className="flex h-14 shrink-0 items-center gap-3 border-b border-border bg-bg/85 px-4 backdrop-blur-md md:px-6 print:hidden">
      <Link to="/" aria-label="Foretrade home" className="rounded-md md:hidden">
        <LogoMark size={28} />
      </Link>
      <div className="hidden min-w-0 items-center gap-2.5 sm:flex">
        <span className="label hidden lg:inline">Foretrade</span>
        {title && (
          <>
            <span aria-hidden="true" className="hidden text-border lg:inline">
              /
            </span>
            <span className="truncate text-[15px] font-medium text-text">{title}</span>
          </>
        )}
      </div>

      <div className="ml-auto flex items-center gap-2 sm:gap-2.5">
        <span
          className="inline-flex h-6 items-center gap-1.5 rounded-md border border-accent/35 bg-accent-soft px-2 font-mono text-[10px] font-semibold tracking-[0.14em] text-accent-ink"
          title="Simulated fills only. Nothing executes on a real venue."
        >
          <span aria-hidden="true" className="size-1.5 rounded-full bg-accent" />
          PAPER<span className="hidden sm:inline"> TRADING</span>
        </span>

        <Link
          to="/account"
          className="inline-flex h-8 items-center gap-2 rounded-full border border-border bg-surface px-3 font-mono text-[12.5px] tabular-nums transition-colors hover:border-muted/45 hover:bg-surface-2"
        >
          <span className="sr-only">Paper equity</span>
          {portfolio ? (
            <>
              <span className="font-medium text-text">{fmtUsd(portfolio.equity)}</span>
              <span className="hidden sm:inline">
                <PriceChange value={portfolio.totalPnlPct} className="text-[11.5px]" />
              </span>
            </>
          ) : (
            <Skeleton className="h-3 w-20" />
          )}
        </Link>

        <div className="group relative hidden sm:block">
          <span
            tabIndex={0}
            aria-describedby="credits-tip"
            className="inline-flex h-8 items-center gap-1.5 rounded-full border border-border bg-surface px-3 font-mono text-[12.5px] text-text tabular-nums"
          >
            <span aria-hidden="true" className={cx('size-1.5 rounded-full', low ? 'bg-down' : 'bg-accent')} />
            {left ?? '—'} <span className="text-muted">cr</span>
          </span>
          <span
            id="credits-tip"
            role="tooltip"
            className="pointer-events-none absolute top-full right-0 z-30 mt-2 w-max rounded-md bg-ink px-2.5 py-1.5 text-xs text-ink-fg opacity-0 shadow-lg transition-opacity duration-150 group-focus-within:opacity-100 group-hover:opacity-100"
          >
            Daily AI credits, resets 00:00 UTC
          </span>
        </div>

        <NotificationsBell />
        <ThemeToggle className="hidden sm:grid" />
        <UserMenu creditsLeft={left} />
      </div>
    </header>
  )
}

export function ThemeToggle({ className }: { className?: string }) {
  const { theme, toggle } = useTheme()
  const next = theme === 'dark' ? 'light' : 'dark'
  return (
    <button
      type="button"
      onClick={toggle}
      aria-label={`Switch to ${next} theme`}
      title={`Switch to ${next} theme`}
      className={cx(
        'size-8 place-items-center rounded-full text-muted transition-colors hover:bg-surface-2 hover:text-text',
        className ?? 'grid',
      )}
    >
      {theme === 'dark' ? <IconSun size={17} /> : <IconMoon size={17} />}
    </button>
  )
}

function UserMenu({ creditsLeft }: { creditsLeft: number | null }) {
  const { user, credits, logout } = useAuth()
  const { theme, toggle } = useTheme()
  const menu = useRef<HTMLDivElement>(null)
  const close = () => menu.current?.hidePopover()
  const initials = (user?.name || user?.email || '?')
    .split(/[\s@.]+/)
    .slice(0, 2)
    .map((w) => w.charAt(0).toUpperCase())
    .join('')

  return (
    <>
      <button
        type="button"
        popoverTarget="user-menu"
        aria-label="Account menu"
        className="grid size-8 place-items-center rounded-full bg-ink text-[11.5px] font-semibold tracking-wide text-ink-fg transition-transform hover:scale-105"
      >
        {initials}
      </button>
      {/* Native popover: light-dismiss and Escape come free; it always sits under the top-right corner. */}
      <div
        id="user-menu"
        ref={menu}
        popover="auto"
        className="card fixed inset-auto top-[60px] right-3 m-0 w-72 animate-fade-up p-1.5 text-text shadow-[0_16px_40px_-16px_rgb(15_27_45/0.35)]"
      >
        <div className="px-3 pt-2.5 pb-3">
          <div className="truncate text-sm font-semibold">{user?.name}</div>
          <div className="truncate text-[13px] text-muted">{user?.email}</div>
        </div>
        {credits && (
          <div className="mx-1.5 mb-1.5 rounded-lg bg-surface-2 px-3 py-2.5">
            <div className="flex items-baseline justify-between">
              <span className="label">AI credits · daily</span>
              <span className="font-mono text-xs tabular-nums">
                {creditsLeft} / {credits.limit}
              </span>
            </div>
            <div className="mt-2 h-1 overflow-hidden rounded-full bg-border">
              <div className="h-full rounded-full bg-accent" style={{ width: `${credits.limit > 0 ? ((creditsLeft ?? 0) / credits.limit) * 100 : 0}%` }} />
            </div>
            <div className="mt-1.5 text-[11.5px] text-muted">Resets 00:00 UTC</div>
          </div>
        )}
        <div className="h-px bg-border" />
        <div className="flex flex-col py-1">
          <Link to="/account" onClick={close} className={menuItem}>
            <IconAccount size={16} /> Paper account
          </Link>
          <button type="button" onClick={toggle} className={cx(menuItem, 'sm:hidden')}>
            {theme === 'dark' ? <IconSun size={16} /> : <IconMoon size={16} />}
            {theme === 'dark' ? 'Light theme' : 'Dark theme'}
          </button>
          <button
            type="button"
            onClick={() => {
              close()
              void logout()
            }}
            className={menuItem}
          >
            <IconLogout size={16} /> Log out
          </button>
        </div>
      </div>
    </>
  )
}

const menuItem =
  'flex h-9 w-full items-center gap-2.5 rounded-lg px-3 text-left text-[13.5px] text-text transition-colors hover:bg-surface-2'

/** Serif page headline with optional micro-label, subtitle and actions. */
export function PageHeader({
  title,
  subtitle,
  actions,
  label,
}: {
  title: ReactNode
  subtitle?: ReactNode
  actions?: ReactNode
  label?: ReactNode
}) {
  return (
    <header className="mb-8 flex flex-wrap items-end justify-between gap-x-6 gap-y-4">
      <div className="max-w-3xl min-w-0">
        {label && <div className="label mb-2.5">{label}</div>}
        <h1 className="font-display text-[32px] leading-[1.08] font-medium tracking-[-0.015em] text-balance text-text md:text-[40px]">
          {title}
        </h1>
        {subtitle && <p className="mt-2.5 text-[15px] leading-relaxed text-pretty text-muted">{subtitle}</p>}
      </div>
      {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
    </header>
  )
}

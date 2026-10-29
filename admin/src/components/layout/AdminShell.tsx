import { Component, useEffect, useRef, type ComponentType, type ReactNode } from 'react'
import { Link, Outlet, useLocation } from 'react-router'
import { APP_URL } from '../../lib/api'
import { useAuth } from '../../lib/auth'
import { cx } from '../../lib/cx'
import { useTheme } from '../../lib/theme'
import { Button } from '../ui/Button'
import { EmptyState } from '../ui/EmptyState'
import { IconAlert, IconAudit, IconExternal, IconLogout, IconMoon, IconOverview, IconSettings, IconSun, IconUsers } from '../ui/icons'
import { LogoMark } from '../ui/Logo'

type NavItem = { to: string; label: string; icon: ComponentType<{ size?: number }>; match: (path: string) => boolean }

const NAV: NavItem[] = [
  { to: '/', label: 'Overview', icon: IconOverview, match: (p) => p === '/' },
  { to: '/users', label: 'Users', icon: IconUsers, match: (p) => p.startsWith('/users') },
  { to: '/audit', label: 'Audit', icon: IconAudit, match: (p) => p.startsWith('/audit') },
  { to: '/settings', label: 'Settings', icon: IconSettings, match: (p) => p.startsWith('/settings') },
]

/** Rail (bottom tab bar under 768px) + top bar + scrolling content, as in the trade app, labelled ADMIN. */
export function AdminShell() {
  const { pathname } = useLocation()
  const main = useRef<HTMLElement>(null)
  const current = NAV.find((n) => n.match(pathname))

  useEffect(() => {
    main.current?.scrollTo(0, 0)
    document.title = current ? `${current.label} · Foretrade Admin` : 'Foretrade Admin'
    return () => {
      document.title = 'Foretrade Admin'
    }
  }, [pathname, current])

  return (
    <div className="flex h-dvh bg-bg text-text">
      <a
        href="#main"
        className="sr-only z-50 rounded-md bg-ink px-3 py-2 text-sm text-ink-fg focus:not-sr-only focus:fixed focus:top-3 focus:left-3"
      >
        Skip to content
      </a>

      <aside className="hidden w-[76px] shrink-0 flex-col items-center border-r border-border bg-surface md:flex">
        <Link to="/" aria-label="Foretrade Admin home" className="flex h-[76px] flex-col items-center justify-center gap-1.5 rounded-lg">
          <LogoMark size={34} />
          <span className="hidden font-mono text-[8.5px] font-medium tracking-[0.16em] text-muted lg:block">ADMIN</span>
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
        <TopBar title={current?.label} />
        {/* `relative` contains absolutely-positioned sr-only text, so it can't widen the page on mobile. */}
        <main id="main" ref={main} tabIndex={-1} className="relative min-h-0 flex-1 overflow-y-auto focus:outline-none">
          <PageErrorBoundary pathname={pathname}>
            <Outlet />
          </PageErrorBoundary>
        </main>
        <nav aria-label="Primary" className="grid grid-cols-4 border-t border-border bg-surface pb-[env(safe-area-inset-bottom)] md:hidden">
          {NAV.map((item) => {
            const active = item.match(pathname)
            return (
              <Link
                key={item.to}
                to={item.to}
                aria-current={active ? 'page' : undefined}
                className={cx(
                  'relative flex min-h-14 flex-col items-center justify-center gap-1 text-[10.5px] leading-none transition-colors',
                  active ? 'font-semibold text-accent-ink' : 'text-muted',
                )}
              >
                {active && <span className="absolute top-0 h-[2px] w-8 rounded-b-full bg-accent" />}
                <item.icon size={20} />
                {item.label}
              </Link>
            )
          })}
        </nav>
      </div>
    </div>
  )
}

/** Keeps the shell up when a page throws while rendering; a navigation clears it. */
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
        <h1 className="sr-only">Error</h1>
        <EmptyState
          icon={<IconAlert size={20} />}
          title="This page failed to load."
          description="The console may have been updated since you opened it. Reloading usually fixes it."
          action={<Button onClick={() => location.reload()}>Reload</Button>}
        />
      </div>
    )
  }
}

/** The ADMIN tag: ink background, mono uppercase, letter-spaced. */
export function AdminBadge({ className }: { className?: string }) {
  return (
    <span
      className={cx(
        'inline-flex h-6 items-center rounded-md bg-ink px-2 font-mono text-[10px] font-semibold tracking-[0.16em] text-ink-fg',
        className,
      )}
    >
      ADMIN
    </span>
  )
}

function TopBar({ title }: { title?: string }) {
  const { user } = useAuth()
  return (
    <header className="flex h-14 shrink-0 items-center gap-3 border-b border-border bg-bg/85 px-4 backdrop-blur-md md:px-6">
      <Link to="/" aria-label="Foretrade Admin home" className="rounded-md md:hidden">
        <LogoMark size={28} />
      </Link>
      <div className="flex min-w-0 items-center gap-2.5">
        <span className="label hidden lg:inline">Foretrade · Admin</span>
        {title && (
          <>
            <span aria-hidden="true" className="hidden text-border lg:inline">
              /
            </span>
            <span className="truncate text-[15px] font-medium text-text">{title}</span>
          </>
        )}
      </div>

      <div className="ml-auto flex min-w-0 items-center gap-2 sm:gap-3">
        <AdminBadge />
        <span className="hidden max-w-56 truncate text-[13px] text-muted lg:inline">{user?.email}</span>
        <a
          href={APP_URL}
          target="_blank"
          rel="noreferrer"
          className="hidden h-8 items-center gap-1 rounded-full border border-border bg-surface px-3 text-[13px] text-text transition-colors hover:bg-surface-2 sm:inline-flex"
        >
          Open Foretrade <IconExternal size={14} />
          <span className="sr-only">(opens in a new tab)</span>
        </a>
        <ThemeToggle className="hidden sm:grid" />
        <UserMenu />
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

function UserMenu() {
  const { user, logout } = useAuth()
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
        className="grid size-8 shrink-0 place-items-center rounded-full bg-ink text-[11.5px] font-semibold tracking-wide text-ink-fg transition-transform hover:scale-105"
      >
        {initials}
      </button>
      {/* Native popover: light-dismiss and Escape come free; it always sits under the top-right corner. */}
      <div
        id="user-menu"
        ref={menu}
        popover="auto"
        className="card fixed inset-auto top-[60px] right-3 m-0 w-72 max-w-[calc(100vw-1.5rem)] animate-fade-up p-1.5 text-text shadow-[0_16px_40px_-16px_rgb(15_27_45/0.35)]"
      >
        <div className="px-3 pt-2.5 pb-3">
          <div className="flex items-center gap-2">
            <span className="truncate text-sm font-semibold">{user?.name}</span>
            <AdminBadge className="h-5 text-[9px]" />
          </div>
          <div className="truncate text-[13px] text-muted">{user?.email}</div>
        </div>
        <div className="h-px bg-border" />
        <div className="flex flex-col py-1">
          <a href={APP_URL} target="_blank" rel="noreferrer" onClick={close} className={cx(menuItem, 'sm:hidden')}>
            <IconExternal size={16} /> Open Foretrade
          </a>
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
          <p className="px-3 pt-0.5 pb-2 text-[12px] leading-snug text-muted">
            Also signs this browser out of Foretrade: the two apps share one session.
          </p>
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
        <h1 className="font-display text-[30px] leading-[1.08] font-medium tracking-[-0.015em] text-balance break-words text-text md:text-[38px]">
          {title}
        </h1>
        {subtitle && <div className="mt-2.5 text-[15px] leading-relaxed text-pretty text-muted">{subtitle}</div>}
      </div>
      {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
    </header>
  )
}

/** Page container: ~1200px wide, the trade app's gutters. */
export function Page({ children }: { children: ReactNode }) {
  return <div className="mx-auto max-w-[1200px] px-4 py-8 md:px-8 md:py-10">{children}</div>
}

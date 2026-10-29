import type { ReactNode } from 'react'
import { Link, Navigate, Route, Routes, useLocation, useNavigate } from 'react-router'
import { AdminBadge, AdminShell, Page } from './components/layout/AdminShell'
import { Button, buttonClass } from './components/ui/Button'
import { EmptyState } from './components/ui/EmptyState'
import { IconAlert, IconShield } from './components/ui/icons'
import { LogoMark } from './components/ui/Logo'
import { FullPageLoader } from './components/ui/Spinner'
import { useAuth } from './lib/auth'
import { AuditPage } from './pages/AuditPage'
import { LoginPage } from './pages/LoginPage'
import { OverviewPage } from './pages/OverviewPage'
import { SettingsPage } from './pages/SettingsPage'
import { UserDetailPage } from './pages/UserDetailPage'
import { UsersPage } from './pages/UsersPage'

// ponytail: no lazy route chunks; six small pages and no chart library, so one bundle loads fast enough.
export function App() {
  const { state } = useAuth()
  if (state === 'loading') return <FullPageLoader />
  if (state === 'restricted') return <AccessRestricted />
  if (state === 'error') return <CantReach />
  return (
    <Routes>
      <Route path="/login" element={<LoginPage />} />
      <Route
        element={
          <RequireAdmin>
            <AdminShell />
          </RequireAdmin>
        }
      >
        <Route index element={<OverviewPage />} />
        <Route path="users" element={<UsersPage />} />
        <Route path="users/:id" element={<UserDetailPage />} />
        <Route path="audit" element={<AuditPage />} />
        <Route path="settings" element={<SettingsPage />} />
        <Route path="*" element={<NotFound />} />
      </Route>
    </Routes>
  )
}

/** Signed-out visitors go to /login and come back to where they were after signing in. */
function RequireAdmin({ children }: { children: ReactNode }) {
  const { user } = useAuth()
  const location = useLocation()
  if (!user) return <Navigate to="/login" replace state={{ from: location.pathname + location.search }} />
  return children
}

function Wall({ icon, title, children }: { icon: ReactNode; title: string; children: ReactNode }) {
  return (
    <main className="grid min-h-dvh place-items-center bg-bg px-4 py-10 text-text">
      <div className="card w-full max-w-md animate-fade-up p-8 text-center">
        <div className="flex items-center justify-center gap-2.5">
          <LogoMark size={30} />
          <AdminBadge />
        </div>
        <div className="mx-auto mt-7 grid size-12 place-items-center rounded-full bg-accent-soft text-accent-ink">{icon}</div>
        <h1 className="mt-4 font-display text-[30px] leading-tight font-medium">{title}</h1>
        {children}
      </div>
    </main>
  )
}

/** A non-admin session (signed out by AuthProvider), or an admin route answered 403. */
function AccessRestricted() {
  const { dismissRestricted } = useAuth()
  const navigate = useNavigate()
  return (
    <Wall icon={<IconShield size={22} />} title="Access restricted">
      <p className="mt-3 text-[15px] leading-relaxed text-muted">
        This console is for Foretrade administrators. Your session has been signed out.
      </p>
      <Button
        className="mt-7 w-full"
        size="lg"
        onClick={() => {
          dismissRestricted()
          navigate('/login', { replace: true })
        }}
      >
        Back to sign in
      </Button>
    </Wall>
  )
}

function CantReach() {
  const { retry } = useAuth()
  return (
    <Wall icon={<IconAlert size={22} />} title="Can't reach the API">
      <p className="mt-3 text-[15px] leading-relaxed text-muted">
        The console couldn't check your session. Check your connection, then try again.
      </p>
      <Button className="mt-7 w-full" size="lg" onClick={retry}>
        Retry
      </Button>
    </Wall>
  )
}

function NotFound() {
  return (
    <Page>
      <EmptyState
        as="h1"
        className="mx-auto max-w-xl"
        title="Page not found"
        description="This page doesn't exist. The link may be old."
        action={
          <Link to="/" className={buttonClass('secondary')}>
            Back to the overview
          </Link>
        }
      />
    </Page>
  )
}

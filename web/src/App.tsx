import { Fragment, lazy, type ComponentType, type ReactNode } from 'react'
import { Link, Navigate, Route, Routes, useLocation } from 'react-router'
import { AppShell } from './components/layout/AppShell'
import { buttonClass } from './components/ui/Button'
import { EmptyState } from './components/ui/EmptyState'
import { FullPageLoader } from './components/ui/Spinner'
import { AssetsProvider } from './lib/assets'
import { InstrumentsProvider } from './lib/instruments'
import { useAuth } from './lib/auth'
import { PortfolioProvider } from './lib/portfolio'
import { LoginPage } from './pages/LoginPage'

// Route pages load on demand (AppShell wraps the outlet in Suspense), so charts and markdown stay out of the
// first chunk. The login page stays eager: it is the first screen for signed-out visitors.
const page = <K extends string>(load: () => Promise<Record<K, ComponentType>>, name: K) =>
  lazy(() => load().then((m) => ({ default: m[name] })))

const AgentPage = page(() => import('./pages/agent/AgentPage'), 'AgentPage')
const TradePage = page(() => import('./pages/trade/TradePage'), 'TradePage')
const NewsPage = page(() => import('./pages/news/NewsPage'), 'NewsPage')
const SignalsPage = page(() => import('./pages/signals/SignalsPage'), 'SignalsPage')
const ScreenerPage = page(() => import('./pages/screener/ScreenerPage'), 'ScreenerPage')
const AccountPage = page(() => import('./pages/account/AccountPage'), 'AccountPage')
const ForecastsPage = page(() => import('./pages/forecasts/ForecastsPage'), 'ForecastsPage')
const NewForecastPage = page(() => import('./pages/forecasts/NewForecastPage'), 'NewForecastPage')
const ForecastDetailPage = page(() => import('./pages/forecasts/ForecastDetailPage'), 'ForecastDetailPage')
const StrategiesPage = page(() => import('./pages/strategies/StrategiesPage'), 'StrategiesPage')
const StrategyDetailPage = page(() => import('./pages/strategies/StrategyDetailPage'), 'StrategyDetailPage')
const ArenaRunPage = page(() => import('./pages/arena/ArenaRunPage'), 'ArenaRunPage')
const PortfoliosPage = page(() => import('./pages/portfolios/PortfoliosPage'), 'PortfoliosPage')
const PortfolioDetailPage = page(() => import('./pages/portfolios/PortfolioDetailPage'), 'PortfolioDetailPage')
const AutomationsPage = page(() => import('./pages/automations/AutomationsPage'), 'AutomationsPage')

export function App() {
  return (
    <Routes>
      <Route path="/login" element={<LoginPage />} />
      <Route
        element={
          <RequireAuth>
            <PortfolioProvider>
              <InstrumentsProvider>
                <AssetsProvider>
                  <AppShell />
                </AssetsProvider>
              </InstrumentsProvider>
            </PortfolioProvider>
          </RequireAuth>
        }
      >
        <Route index element={<AgentPage />} />
        <Route path="chat/:chatId" element={<AgentPage />} />
        <Route path="trade" element={<Navigate to="/trade/BTC" replace />} />
        <Route path="trade/:symbol" element={<TradePage />} />
        <Route path="news" element={<NewsPage />} />
        <Route path="signals" element={<SignalsPage />} />
        <Route path="screener" element={<ScreenerPage />} />
        <Route path="account" element={<AccountPage />} />
        <Route path="forecasts" element={<ForecastsPage />} />
        <Route path="forecasts/new" element={<NewForecastPage />} />
        <Route path="forecasts/:id" element={<ForecastDetailPage />} />
        <Route path="strategies" element={<StrategiesPage />} />
        <Route path="strategies/arena/:id" element={<ArenaRunPage />} />
        <Route path="strategies/:id" element={<StrategyDetailPage />} />
        <Route path="portfolios" element={<PortfoliosPage />} />
        <Route path="portfolios/:id" element={<PortfolioDetailPage />} />
        <Route path="automations" element={<AutomationsPage />} />
        <Route path="*" element={<NotFound />} />
      </Route>
    </Routes>
  )
}

/** Signed-out visitors go to /login and come back to where they were after signing in. */
function RequireAuth({ children }: { children: ReactNode }) {
  const { user, loading } = useAuth()
  const location = useLocation()
  if (loading) return <FullPageLoader />
  if (!user) return <Navigate to="/login" replace state={{ from: location.pathname + location.search }} />
  // A session that switches user (another tab signed in as someone else) starts the shell over: the
  // portfolio's applied revision and every page's state belong to the previous account.
  return <Fragment key={user.id}>{children}</Fragment>
}

function NotFound() {
  return (
    <div className="mx-auto max-w-xl px-4 py-20">
      <h1 className="sr-only">Page not found</h1>
      <EmptyState
        title="This page doesn't exist."
        description="The link may be old, or the item was deleted."
        action={
          <Link to="/" className={buttonClass('secondary')}>
            Back to the agent
          </Link>
        }
      />
    </div>
  )
}

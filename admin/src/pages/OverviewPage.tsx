import { useEffect, type ReactNode } from 'react'
import { Link } from 'react-router'
import { Page, PageHeader } from '../components/layout/AdminShell'
import { Bars } from '../components/ui/Bars'
import { Button } from '../components/ui/Button'
import { Card, CardHeader } from '../components/ui/Card'
import { ErrorState } from '../components/ui/EmptyState'
import { FormError } from '../components/ui/Modal'
import { Skeleton } from '../components/ui/Spinner'
import { Sparkline } from '../components/ui/Sparkline'
import { Stat } from '../components/ui/Stat'
import { IconRefresh } from '../components/ui/icons'
import { api } from '../lib/api'
import { cx } from '../lib/cx'
import { fmtDate, fmtDuration, fmtNum, fmtUsd, plural, timeAgo } from '../lib/format'
import type { AdminOverview, SystemStatus } from '../lib/types'
import { useLoad } from '../lib/useLoad'

const REFRESH_MS = 60_000

export function OverviewPage() {
  const { data, error, loading, reload } = useLoad((signal) => api.overview(signal).then((r) => r.overview), [])

  useEffect(() => {
    const timer = setInterval(() => !document.hidden && reload(), REFRESH_MS)
    return () => clearInterval(timer)
  }, [reload])

  return (
    <Page>
      <PageHeader
        label={<>Overview{data && <> · as of {fmtDate(data.asOf, { time: true })}</>}</>}
        title="Platform overview."
        subtitle="Accounts, AI usage and system health. Refreshes every minute."
        actions={
          <Button variant="secondary" loading={loading && !!data} disabled={loading} onClick={reload}>
            {!(loading && data) && <IconRefresh size={16} />} Refresh
          </Button>
        }
      />
      {data && error && <FormError>Couldn't refresh: {error} Showing data from {timeAgo(data.asOf)}.</FormError>}
      {data ? (
        <Overview o={data} />
      ) : error ? (
        <ErrorState message={error} onRetry={reload} />
      ) : (
        <div role="status" aria-label="Loading overview" className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
          {[0, 1, 2, 3].map((i) => (
            <Skeleton key={i} className="h-28 rounded-[14px]" />
          ))}
          <Skeleton className="h-64 rounded-[14px] sm:col-span-2 lg:col-span-4" />
        </div>
      )}
    </Page>
  )
}

function Overview({ o }: { o: AdminOverview }) {
  return (
    <div className="mt-6 space-y-6">
      <section aria-label="Key numbers" className="grid grid-cols-2 gap-4 lg:grid-cols-4">
        <Kpi label="Users" value={fmtNum(o.users.total)} sub={`${plural(o.users.admins, 'admin')} · ${fmtNum(o.users.disabled)} disabled`} />
        <Kpi label="Active today" value={fmtNum(o.users.active.today)} sub={`7d ${fmtNum(o.users.active.d7)} · 30d ${fmtNum(o.users.active.d30)}`} />
        <Kpi label="New users · 30d" value={fmtNum(o.users.new30d)} />
        <Kpi label="AI credits today" value={fmtNum(o.credits.today)} sub={`7d ${fmtNum(o.credits.d7)} · 30d ${fmtNum(o.credits.d30)}`} />
      </section>

      <Card>
        <CardHeader label="AI credits · last 30 days (UTC)" title="Credit spend" />
        <div className="px-5 pb-5">
          <Bars data={o.credits.daily} label="AI credits per day, last 30 days" unit="credit" />
          <p className="mt-3 text-[12.5px] text-muted">Default limit {fmtNum(o.credits.defaultLimit)} credits per user per UTC day.</p>
        </div>
      </Card>

      <div className="grid grid-cols-1 gap-6 lg:grid-cols-[minmax(0,1.5fr)_minmax(0,1fr)]">
        <Card>
          <CardHeader label="Usage" title="Features" />
          <div className="overflow-x-auto">
            <table className="data-table">
              <thead>
                <tr>
                  <th scope="col">Feature</th>
                  <th scope="col" className="text-right">Today</th>
                  <th scope="col" className="text-right">7d</th>
                  <th scope="col" className="text-right">30d</th>
                  <th scope="col">30 days</th>
                </tr>
              </thead>
              <tbody>
                {o.features.map((f) => (
                  <tr key={f.key}>
                    <th scope="row" className="text-left font-medium">
                      {f.label}
                    </th>
                    <td className="text-right">{fmtNum(f.today)}</td>
                    <td className="text-right">{fmtNum(f.d7)}</td>
                    <td className="text-right">{fmtNum(f.d30)}</td>
                    <td>
                      <Sparkline values={f.daily.map((d) => d.value)} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <p className="px-5 pt-3 pb-5 text-[12.5px] text-muted">Copy plans counts created plans, including stopped plans.</p>
        </Card>

        <Card>
          <CardHeader label="AI credits · 7 days" title="Top users" />
          {o.topUsers.length ? (
            <ol className="px-2 pb-3">
              {o.topUsers.map((u, i) => (
                <li key={u.id}>
                  <Link to={`/users/${u.id}`} className="flex items-center gap-3 rounded-lg px-3 py-2 transition-colors hover:bg-surface-2">
                    <span className="w-5 font-mono text-[11px] text-muted">{String(i + 1).padStart(2, '0')}</span>
                    <span className="min-w-0 flex-1">
                      <span className="block truncate text-[13.5px] font-medium text-text">{u.name}</span>
                      <span className="block truncate text-[12px] text-muted">{u.email}</span>
                    </span>
                    <span className="font-mono text-[13px] tabular-nums">{fmtNum(u.credits)}</span>
                  </Link>
                </li>
              ))}
            </ol>
          ) : (
            <p className="px-5 pb-5 text-sm text-muted">No AI usage in the last 7 days.</p>
          )}
        </Card>
      </div>

      <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
        <Card>
          <CardHeader label="Paper trading · all accounts" title="Paper totals" />
          <div className="grid grid-cols-2 gap-x-4 gap-y-5 px-5 pb-5 sm:grid-cols-3">
            <Stat label="Accounts" value={fmtNum(o.paper.accounts)} />
            <Stat label="Open positions" value={fmtNum(o.paper.openPositions)} />
            <Stat label="Equity" value={fmtUsd(o.paper.equity, { decimals: 0 })} />
            <Stat
              label="Total P&L"
              value={fmtUsd(o.paper.totalPnl, { decimals: 0, sign: true })}
              tone={o.paper.totalPnl == null || o.paper.totalPnl === 0 ? 'neutral' : o.paper.totalPnl > 0 ? 'up' : 'down'}
            />
            <Stat label="Notional · 30d" value={fmtUsd(o.paper.notional30d, { compact: true })} />
          </div>
        </Card>
        <SystemCard s={o.system} />
      </div>
    </div>
  )
}

function Kpi({ label, value, sub }: { label: string; value: string; sub?: string }) {
  return (
    <Card className="p-5">
      <Stat label={label} value={<span className="font-display text-[30px] font-medium">{value}</span>} sub={sub} />
    </Card>
  )
}

type Tone = 'ok' | 'warn' | 'bad' | 'off'

function Dot({ tone }: { tone: Tone }) {
  const color = { ok: 'bg-up', warn: 'bg-accent', bad: 'bg-down', off: 'bg-muted/50' }[tone]
  return <span aria-hidden="true" className={cx('inline-block size-2 shrink-0 rounded-full', color)} />
}

function Row({ label, tone, children }: { label: string; tone?: Tone; children: ReactNode }) {
  return (
    <div className="grid grid-cols-[7.5rem_minmax(0,1fr)] gap-3 border-t border-border py-2.5 first:border-t-0">
      <dt className="label pt-0.5">{label}</dt>
      <dd className="flex min-w-0 items-start gap-2 text-[13.5px] text-text">
        {tone && (
          <span className="pt-1.5">
            <Dot tone={tone} />
          </span>
        )}
        <span className="min-w-0 break-words">{children}</span>
      </dd>
    </div>
  )
}

/** News is stale when the last ingest is over 3 h old or the brief over 6 h (ingestion runs every 2 h). */
export function newsStale(n: SystemStatus['news'], now = Date.now()) {
  if (!n.enabled) return true
  const ingestAge = n.lastIngestAt ? (now - Date.parse(n.lastIngestAt)) / 3_600_000 : Infinity
  return ingestAge > 3 || n.briefAgeMin == null || n.briefAgeMin > 360
}

function SystemCard({ s }: { s: SystemStatus }) {
  const stale = newsStale(s.news)
  const braveShare = s.brave.limit ? Math.min(1, s.brave.used / s.brave.limit) : 0
  const crawler: [Tone, string] =
    s.crawler.reachable == null ? ['off', 'Not configured'] : s.crawler.reachable ? ['ok', 'Reachable'] : ['bad', 'Unreachable']

  return (
    <Card>
      <CardHeader label="API process" title="System status" />
      <dl className="px-5 pb-4">
        <Row label="Version">
          <span className="font-mono">{s.version}</span>
        </Row>
        <Row label="Uptime">
          {fmtDuration(s.uptimeSec)} <span className="text-muted">· since {fmtDate(s.startedAt, { time: true })}</span>
        </Row>
        <Row label="Database" tone={s.db.ok ? 'ok' : 'bad'}>
          {s.db.ok ? 'OK' : 'Failing'} <span className="text-muted">· {fmtNum(s.db.latencyMs)} ms</span>
        </Row>
        <Row label="Model" tone={s.model ? undefined : 'off'}>
          {s.model ? (
            <span className="font-mono text-[12.5px]">{s.model}</span>
          ) : (
            <>
              AI is off ·{' '}
              <Link to="/settings" className="text-accent-ink hover:underline">
                choose a provider
              </Link>
            </>
          )}
          <span className="block text-muted">Knowledge cutoff {fmtDate(s.knowledgeCutoff)}</span>
        </Row>
        <Row label="News" tone={stale ? 'warn' : 'ok'}>
          {s.news.enabled ? (
            <>
              Last ingest {timeAgo(s.news.lastIngestAt)} · {fmtNum(s.news.items)} items · brief{' '}
              {s.news.briefAgeMin == null ? '—' : `${fmtDuration(s.news.briefAgeMin * 60)} old`}
              {stale && <span className="block text-accent-ink">Stale: check the ingestion job.</span>}
            </>
          ) : (
            'Disabled: no Brave API key configured.'
          )}
        </Row>
        <Row label="Brave">
          <span className="font-mono tabular-nums">
            {fmtNum(s.brave.used)} / {fmtNum(s.brave.limit)}
          </span>{' '}
          <span className="text-muted">calls today</span>
          <span
            role="meter"
            aria-label="Brave calls used today"
            aria-valuemin={0}
            aria-valuemax={s.brave.limit}
            aria-valuenow={s.brave.used}
            className="mt-1.5 block h-1.5 w-full max-w-60 overflow-hidden rounded-full bg-surface-2"
          >
            <span className={cx('block h-full rounded-full', braveShare > 0.9 ? 'bg-down' : 'bg-accent')} style={{ width: `${braveShare * 100}%` }} />
          </span>
        </Row>
        <Row label="Crawler" tone={crawler[0]}>
          {crawler[1]} <span className="block truncate font-mono text-[12px] text-muted">{s.crawler.base}</span>
        </Row>
        <Row label="Jobs">
          {fmtNum(s.jobs.forecastsRunning)} forecasts · {fmtNum(s.jobs.arenaRunsRunning)} arena runs running
        </Row>
      </dl>
      <LiveDeskStatus live={s.jobs.liveArena} />
    </Card>
  )
}

function LiveDeskStatus({ live }: { live: SystemStatus['jobs']['liveArena'] }) {
  const states: Record<typeof live.status, [Tone, string]> = {
    live: ['ok', 'Live'],
    starting: ['warn', 'Starting · awaiting first decision'],
    paused: ['off', 'Paused · job paused'],
    stale: ['warn', 'Stale · last completed step is overdue'],
    outage: ['bad', 'Outage · live desk unavailable'],
  }
  const [tone, status] = states[live.status]
  const utc = (iso: string | null) => iso == null ? '—' : new Date(iso).toLocaleString('en-US', {
    month: 'short', day: 'numeric', year: 'numeric', hour: 'numeric', minute: '2-digit', timeZone: 'UTC', timeZoneName: 'short',
  })

  return (
    <section aria-label="Live paper desk" className="border-t border-border px-5 pt-4 pb-5">
      <h3 className="font-display text-lg font-medium text-text">Live paper desk</h3>
      <dl className="mt-2">
        <Row label="Status" tone={tone}>{status}</Row>
        <Row label="Cadence">Every 4 hours · closed bars</Row>
        <Row label="Launched">{utc(live.startedAt)}</Row>
        <Row label="Last step">{utc(live.lastStepAt)}</Row>
        <Row label="Next step">{utc(live.nextStepAt)}</Row>
        <Row label="Agents">{fmtNum(live.agents)} · {fmtNum(live.benched)} benched</Row>
        <Row label="Active copy plans">{fmtNum(live.copyPlans)}</Row>
        <Row label="Freshness" tone={live.stale ? 'warn' : undefined}>{live.stale ? 'Stale' : 'Current'}</Row>
        <Row label="Last error">{live.lastError ?? '—'}</Row>
        <Row label="Claimed decisions today">{fmtNum(live.attemptsToday)} / {fmtNum(live.maxDecisionsPerDay)}</Row>
        <Row label="Request ceiling for today's claims">
          {fmtNum(live.requestCeilingToday)}
          <span className="block text-muted">Conservative ceiling: {fmtNum(live.maxRequestsPerDay / live.maxDecisionsPerDay)} requests per claimed decision.</span>
        </Row>
        <Row label="Hard limits">
          {fmtNum(live.maxRequestsPerDay)} model requests/day · {fmtNum(live.maxTokensPerRequest)} output tokens/request · {fmtNum(live.decisionTimeoutSec)}s/decision
        </Row>
      </dl>
      <p className="mt-3 text-[12.5px] text-muted">These are limits, not measured usage. Shared desk uses no user AI credits.</p>
    </section>
  )
}

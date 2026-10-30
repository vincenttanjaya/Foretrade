import { useState, type FormEvent, type ReactNode } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import { Page, PageHeader } from '../components/layout/AdminShell'
import { useToast } from '../components/toast'
import { CreditsUsed, UserBadges } from '../components/UserBadges'
import { Badge } from '../components/ui/Badge'
import { Bars } from '../components/ui/Bars'
import { Button, buttonClass } from '../components/ui/Button'
import { Card, CardHeader } from '../components/ui/Card'
import { EmptyState, ErrorState } from '../components/ui/EmptyState'
import { IconTrash } from '../components/ui/icons'
import { Input, Label, Select } from '../components/ui/Input'
import { ConfirmDialog, FormError, Modal } from '../components/ui/Modal'
import { PasswordReveal } from '../components/ui/PasswordReveal'
import { Skeleton } from '../components/ui/Spinner'
import { Stat } from '../components/ui/Stat'
import { ApiError, api, errorMessage } from '../lib/api'
import { auditLabel, auditSummary, seededSummary, seededWarning } from '../lib/audit'
import { useAuth } from '../lib/auth'
import { cx } from '../lib/cx'
import { fmtDate, fmtNum, fmtPct, fmtPrice, fmtQty, fmtUsd, plural, timeAgo } from '../lib/format'
import { passwordProblem } from '../lib/password'
import type { ActivityItem, AdminUser, AdminUserDetail, AuditEntry, Portfolio, Role, Trade, UpdateUserRequest } from '../lib/types'
import { useLoad } from '../lib/useLoad'

type Confirm = 'disable' | 'revoke' | 'credits' | 'portfolio'

export function UserDetailPage() {
  const { id = '' } = useParams()
  const { user: me } = useAuth()
  const { toast } = useToast()
  const { data, setData, error, failure, reload } = useLoad((signal) => api.user(id, signal), [id])
  const [confirm, setConfirm] = useState<Confirm | null>(null)
  const [editing, setEditing] = useState(false)
  const [settingPassword, setSettingPassword] = useState(false)
  const [enabling, setEnabling] = useState(false)
  const [seeding, setSeeding] = useState(false)

  const detail = data?.user.id === id ? data : null // not the previous user's page while the next one loads
  if (!detail) {
    if (failure instanceof ApiError && failure.status === 404)
      return (
        <Page>
          <EmptyState
            as="h1"
            className="mx-auto max-w-xl"
            title="Account deleted"
            description="This account no longer exists, or the link is wrong. Its audit entries are kept."
            action={
              <Link to="/users" className={buttonClass('secondary')}>
                Back to users
              </Link>
            }
          />
        </Page>
      )
    return (
      <Page>
        {error ? (
          <>
            <h1 className="sr-only">User</h1>
            <ErrorState message={error} onRetry={reload} />
          </>
        ) : (
          <div role="status" aria-label="Loading user">
            <Skeleton className="h-3 w-24" />
            <Skeleton className="mt-4 h-10 w-80 max-w-full" />
            <Skeleton className="mt-10 h-64 w-full rounded-[14px]" />
          </div>
        )}
      </Page>
    )
  }

  const u = detail.user
  const self = me?.id === u.id
  const limit = u.creditLimit ?? detail.defaultCreditLimit
  /** A mutation's returned user replaces local state at once; the reload brings the new audit row and usage. */
  const applied = (user: AdminUser) => {
    setData((d) => d && { ...d, user })
    reload()
  }

  async function enable() {
    setEnabling(true)
    try {
      applied((await api.updateUser(u.id, { disabled: false })).user)
      toast(`Enabled ${u.email}. They can sign in again.`)
    } catch (err) {
      toast(errorMessage(err), 'error')
    } finally {
      setEnabling(false)
    }
  }

  const confirms: Record<Confirm, { title: string; body: ReactNode; label: string; run: () => Promise<void> }> = {
    disable: {
      title: `Disable ${u.name}?`,
      body: (
        <p>
          <span className="break-all">{u.email}</span> is signed out of every session and can't sign in until an admin
          enables the account. A running forecast or arena run still finishes.
        </p>
      ),
      label: 'Disable account',
      run: async () => {
        applied((await api.updateUser(u.id, { disabled: true })).user)
        toast(`Disabled ${u.email}.`)
      },
    },
    revoke: {
      title: 'Revoke all sessions?',
      body: (
        <p>
          Signs <span className="break-all">{u.email}</span> out of {plural(u.sessions, 'live session')}
          {self ? ', except this one' : ''}. They can sign in again with their password.
        </p>
      ),
      label: 'Revoke sessions',
      run: async () => {
        const res = await api.revokeSessions(u.id)
        applied(res.user)
        toast(`Revoked ${plural(res.revoked, 'session')}.`)
      },
    },
    credits: {
      title: "Reset today's AI credits?",
      body: (
        <p>
          Sets today's usage for <span className="break-all">{u.email}</span> ({fmtNum(u.creditsToday)} credits) back to 0, so
          they get their full {fmtNum(limit)} again until 00:00 UTC.
        </p>
      ),
      label: 'Reset credits',
      run: async () => {
        applied((await api.resetCredits(u.id)).user)
        toast(`Reset today's credits for ${u.email}.`)
      },
    },
    portfolio: {
      title: 'Reset the paper portfolio?',
      body: (
        <p>
          Cash goes back to the starting balance, and every position and trade of <span className="break-all">{u.email}</span>{' '}
          is deleted. It can't be undone.
        </p>
      ),
      label: 'Reset portfolio',
      run: async () => {
        const res = await api.resetPortfolio(u.id)
        setData((d) => d && { ...d, user: res.user, portfolio: res.portfolio, trades: [] })
        reload()
        toast(`Reset the paper portfolio of ${u.email}.`)
      },
    },
  }
  const c = confirm && confirms[confirm]

  return (
    <Page>
      <PageHeader
        label={
          <Link to="/users" className="hover:text-text">
            ← Users
          </Link>
        }
        title={u.name}
        subtitle={
          <span className="flex flex-wrap items-center gap-x-3 gap-y-2">
            <span className="break-all">{u.email}</span>
            <UserBadges user={u} />
            {self && <Badge tone="outline">you</Badge>}
          </span>
        }
      />

      <div className="grid grid-cols-1 gap-6 lg:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
        <Card>
          <CardHeader label="Account" title="Profile" />
          <div className="grid grid-cols-2 gap-x-4 gap-y-5 px-5 pb-5 sm:grid-cols-3">
            <Stat label="Created" value={fmtDate(u.createdAt)} />
            <Stat label="Last login" value={timeAgo(u.lastLoginAt)} />
            <Stat label="Last active" value={timeAgo(u.lastActiveAt)} />
            <Stat label="Live sessions" value={fmtNum(u.sessions)} />
            <Stat label="Credits today" value={<CreditsUsed user={u} defaultLimit={detail.defaultCreditLimit} />} />
            <Stat label="Daily limit" value={fmtNum(limit)} sub={u.creditLimit == null ? 'Default' : 'Custom'} />
            {u.disabledAt && <Stat label="Disabled" value={fmtDate(u.disabledAt)} tone="down" />}
          </div>
        </Card>

        <Card>
          <CardHeader label="Manage" title="Actions" />
          <div className="grid gap-x-3 gap-y-4 px-5 pb-5 sm:grid-cols-2">
            <Action>
              <Button variant="secondary" className="w-full" onClick={() => setEditing(true)}>
                Edit profile
              </Button>
            </Action>
            <Action note={self ? "You can't disable your own account." : undefined} id="disable-note">
              {u.status === 'active' ? (
                <Button variant="danger" className="w-full" disabled={self} aria-describedby={self ? 'disable-note' : undefined} onClick={() => setConfirm('disable')}>
                  Disable account
                </Button>
              ) : (
                <Button variant="secondary" className="w-full" loading={enabling} onClick={enable}>
                  Enable account
                </Button>
              )}
            </Action>
            <Action note={u.managed ? "This account's password comes from the server config." : undefined} id="password-note">
              <Button
                variant="secondary"
                className="w-full"
                disabled={u.managed}
                aria-describedby={u.managed ? 'password-note' : undefined}
                title={u.managed ? "This account's password comes from the server config. Rotate it there and redeploy." : undefined}
                onClick={() => setSettingPassword(true)}
              >
                Set password
              </Button>
            </Action>
            <Action>
              <Button variant="secondary" className="w-full" onClick={() => setConfirm('revoke')}>
                Revoke sessions
              </Button>
            </Action>
            <Action>
              <Button variant="secondary" className="w-full" onClick={() => setConfirm('credits')}>
                Reset today's credits
              </Button>
            </Action>
            <Action>
              <Button variant="danger" className="w-full" onClick={() => setConfirm('portfolio')}>
                Reset portfolio
              </Button>
            </Action>
            <Action>
              <Button variant="secondary" className="w-full" onClick={() => setSeeding(true)}>
                Copy showcase content…
              </Button>
            </Action>
          </div>
        </Card>
      </div>

      <div className="mt-6 grid grid-cols-1 gap-6 lg:grid-cols-[minmax(0,1.4fr)_minmax(0,1fr)]">
        <Card>
          <CardHeader label="AI credits · last 30 days (UTC)" title="Usage" />
          <div className="px-5 pb-5">
            <Bars data={detail.usage} label={`AI credits per day for ${u.email}, last 30 days`} unit="credit" limit={limit} />
          </div>
        </Card>
        <Card>
          <CardHeader label="Lifetime" title="Activity counts" />
          <Counts counts={detail.counts} />
        </Card>
      </div>

      <PortfolioCard portfolio={detail.portfolio} trades={detail.trades} />

      <Card className="mt-6">
        <CardHeader label="Newest first · up to 10 each" title="Recent activity" />
        <div className="grid gap-x-6 gap-y-5 px-5 pb-5 md:grid-cols-2 xl:grid-cols-3">
          <ActivityList title="Chats" items={detail.activity.chats} />
          <ActivityList title="Forecasts" items={detail.activity.forecasts} />
          <ActivityList title="Strategies" items={detail.activity.strategies} />
          <ActivityList title="AI portfolios" items={detail.activity.aiPortfolios} />
          <ActivityList title="Arena runs" items={detail.activity.arenaRuns} />
        </div>
      </Card>

      <Card className="mt-6">
        <CardHeader label="Admin changes to this account" title="Audit" actions={<Link to="/audit" className="text-[13px] text-accent-ink hover:underline">Full log</Link>} />
        <AuditList entries={detail.audit} />
      </Card>

      <DangerZone user={u} self={self} />

      {/* Always mounted: closing a <dialog> (not removing it) is what makes the browser return focus to the button. */}
      <ConfirmDialog open={!!c} onClose={() => setConfirm(null)} title={c?.title} body={c?.body} confirmLabel={c?.label ?? ''} onConfirm={async () => c?.run()} label={u.email} />
      <EditDialog
        open={editing}
        user={u}
        self={self}
        defaultLimit={detail.defaultCreditLimit}
        onClose={() => setEditing(false)}
        onSaved={applied}
      />
      <PasswordDialog open={settingPassword} user={u} self={self} onClose={() => setSettingPassword(false)} onSaved={applied} />
      <Modal open={seeding} onClose={() => setSeeding(false)} label={u.email} title="Copy showcase content">
        <SeedForm user={u} onClose={() => setSeeding(false)} onSaved={applied} />
      </Modal>
    </Page>
  )
}

function Action({ note, id, children }: { note?: string; id?: string; children: ReactNode }) {
  return (
    <div>
      {children}
      {note && (
        <p id={id} className="mt-1.5 text-[12px] leading-snug text-muted">
          {note}
        </p>
      )}
    </div>
  )
}

function Counts({ counts: c }: { counts: AdminUserDetail['counts'] }) {
  const rows: [string, number][] = [
    ['Chats', c.chats],
    ['Messages sent', c.chatMessages],
    ['Forecasts', c.forecasts],
    ['Strategies', c.strategies],
    ['AI portfolios', c.aiPortfolios],
    ['Arena runs', c.arenaRuns],
    ['Paper trades', c.trades],
  ]
  return (
    <div className="grid grid-cols-2 gap-x-4 gap-y-4 px-5 pb-5 sm:grid-cols-3 lg:grid-cols-2 xl:grid-cols-3">
      {rows.map(([label, n]) => (
        <Stat key={label} label={label} value={fmtNum(n)} />
      ))}
    </div>
  )
}

const tone = (n: number | null | undefined) => (n == null || n === 0 ? 'neutral' : n > 0 ? 'up' : 'down')

function PortfolioCard({ portfolio: p, trades }: { portfolio: Portfolio | null; trades: Trade[] }) {
  return (
    <Card className="mt-6">
      <CardHeader label="Paper trading" title="Portfolio" />
      {p ? (
        <>
          <div className="grid grid-cols-2 gap-x-4 gap-y-5 px-5 pb-5 sm:grid-cols-4">
            <Stat label="Equity" value={fmtUsd(p.equity)} />
            <Stat label="Total P&L" value={fmtUsd(p.totalPnl, { sign: true })} sub={fmtPct(p.totalPnlPct, { sign: true })} tone={tone(p.totalPnl)} />
            <Stat label="Cash" value={fmtUsd(p.cash)} />
            <Stat label="Gross exposure" value={fmtUsd(p.grossExposure)} />
          </div>
          {p.positions.length ? (
            <div className="overflow-x-auto border-t border-border">
              <table className="data-table">
                <caption className="sr-only">Open positions</caption>
                <thead>
                  <tr>
                    <th scope="col">Symbol</th>
                    <th scope="col" className="text-right">Qty</th>
                    <th scope="col" className="text-right">Avg price</th>
                    <th scope="col" className="text-right">Mark</th>
                    <th scope="col" className="text-right">Value</th>
                    <th scope="col" className="text-right">Unrealized P&L</th>
                  </tr>
                </thead>
                <tbody>
                  {p.positions.map((x) => (
                    <tr key={x.symbol}>
                      <th scope="row" className="text-left font-mono font-medium">
                        {x.symbol}
                      </th>
                      <td className="text-right">{fmtQty(x.qty)}</td>
                      <td className="text-right">{fmtPrice(x.avgPrice)}</td>
                      <td className="text-right">{fmtPrice(x.markPrice)}</td>
                      <td className="text-right">{fmtUsd(x.marketValue)}</td>
                      <td className={cx('text-right whitespace-nowrap', x.unrealizedPnl > 0 ? 'text-up' : x.unrealizedPnl < 0 && 'text-down')}>
                        {fmtUsd(x.unrealizedPnl, { sign: true })} ({fmtPct(x.unrealizedPnlPct, { sign: true })})
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <p className="border-t border-border px-5 py-4 text-sm text-muted">No open positions.</p>
          )}
        </>
      ) : (
        <p className="px-5 pb-5 text-sm text-muted">Market data is unavailable right now, so the portfolio can't be valued.</p>
      )}

      <h4 className="label border-t border-border px-5 pt-4 pb-2">Recent trades · up to 20</h4>
      {trades.length ? (
        <div className="overflow-x-auto">
          <table className="data-table">
            <caption className="sr-only">Recent trades</caption>
            <thead>
              <tr>
                <th scope="col">Time</th>
                <th scope="col">Trade</th>
                <th scope="col" className="text-right">Price</th>
                <th scope="col" className="text-right">Notional</th>
                <th scope="col" className="text-right">Realized</th>
                <th scope="col">Source</th>
              </tr>
            </thead>
            <tbody>
              {trades.map((t) => (
                <tr key={t.id}>
                  <td className="whitespace-nowrap">{fmtDate(t.createdAt, { time: true })}</td>
                  <td className="whitespace-nowrap">
                    <span className={t.side === 'buy' ? 'text-up' : 'text-down'}>{t.side === 'buy' ? 'Buy' : 'Sell'}</span>{' '}
                    {fmtQty(t.qty)} <span className="font-mono">{t.symbol}</span>
                  </td>
                  <td className="text-right">{fmtPrice(t.price)}</td>
                  <td className="text-right">{fmtUsd(t.notional)}</td>
                  <td className={cx('text-right', t.realizedPnl > 0 ? 'text-up' : t.realizedPnl < 0 && 'text-down')}>
                    {fmtUsd(t.realizedPnl, { sign: true })}
                  </td>
                  <td className="whitespace-nowrap text-muted">{t.source.replace('_', ' ')}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <p className="px-5 pb-5 text-sm text-muted">No trades.</p>
      )}
    </Card>
  )
}

function ActivityList({ title, items }: { title: string; items: ActivityItem[] }) {
  return (
    <section aria-label={title} className="min-w-0">
      <h4 className="label mb-2">
        {title} · {items.length}
      </h4>
      {items.length ? (
        <ul className="divide-y divide-border">
          {items.map((a) => (
            <li key={a.id} className="flex items-baseline gap-2 py-1.5">
              <span className="min-w-0 flex-1 truncate text-[13.5px]" title={a.title}>
                {a.title}
              </span>
              {a.status && <Badge tone={a.status === 'failed' ? 'down' : a.status === 'running' ? 'accent' : 'neutral'}>{a.status}</Badge>}
              <span className="shrink-0 text-[12px] text-muted">{timeAgo(a.at)}</span>
            </li>
          ))}
        </ul>
      ) : (
        <p className="text-[13px] text-muted">None.</p>
      )}
    </section>
  )
}

/** This user's audit entries (≤ 20). */
function AuditList({ entries }: { entries: AuditEntry[] }) {
  if (!entries.length) return <p className="px-5 pb-5 text-sm text-muted">No admin changes yet.</p>
  return (
    <ul className="divide-y divide-border border-t border-border">
      {entries.map((e) => {
        const summary = auditSummary(e)
        return (
          <li key={e.id} className="flex flex-wrap items-baseline gap-x-3 gap-y-0.5 px-5 py-2.5 text-[13.5px]">
            <span className="font-medium">{auditLabel(e)}</span>
            {summary && <span className="min-w-0 break-words text-muted">{summary}</span>}
            <span className="ml-auto text-[12px] text-muted">
              by <span className="break-all">{e.admin.email}</span> · {fmtDate(e.createdAt, { time: true })}
            </span>
          </li>
        )
      })}
    </ul>
  )
}

function DangerZone({ user: u, self }: { user: AdminUser; self: boolean }) {
  const navigate = useNavigate()
  const { toast } = useToast()
  const [typed, setTyped] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function remove(e: FormEvent) {
    e.preventDefault()
    if (typed !== u.email || self) return
    setBusy(true)
    setError(null)
    try {
      await api.deleteUser(u.id)
      toast(`Deleted ${u.email}.`)
      navigate('/users', { replace: true })
    } catch (err) {
      setError(errorMessage(err))
      setBusy(false)
    }
  }

  return (
    <section aria-labelledby="danger-title" className="card mt-6 border-down/35 p-5">
      <div className="label text-down">Danger zone</div>
      <h3 id="danger-title" className="mt-1 font-display text-[19px] font-medium">
        Delete this account
      </h3>
      <p className="mt-2 max-w-2xl text-sm leading-relaxed text-muted">
        Deletes <span className="font-medium break-all text-text">{u.email}</span> and everything it owns: sessions, paper
        account, trades, AI usage, chats, forecasts, strategies, AI portfolios, arena runs and automations. Audit entries
        are kept.{' '}
        {u.managed
          ? 'This account comes from the server config and is recreated with the config password at the next API restart. To revoke this login, disable it here, or rotate the secret and redeploy.'
          : "This can't be undone."}
      </p>
      {self ? (
        <p className="mt-4 text-sm text-muted">You can't delete your own account.</p>
      ) : (
        <form onSubmit={remove} className="mt-4 flex flex-wrap items-end gap-3">
          <div className="min-w-0 flex-1 basis-64">
            <Label htmlFor="delete-confirm" className="normal-case">
              Type <span className="font-semibold break-all text-text">{u.email}</span> to confirm
            </Label>
            <Input id="delete-confirm" autoComplete="off" spellCheck={false} value={typed} onChange={(e) => setTyped(e.target.value)} />
          </div>
          <Button type="submit" variant="danger" disabled={typed !== u.email} loading={busy}>
            <IconTrash size={16} /> Delete account
          </Button>
        </form>
      )}
      {error && <FormError>{error}</FormError>}
    </section>
  )
}

function EditDialog({
  open,
  user: u,
  self,
  defaultLimit,
  onClose,
  onSaved,
}: {
  open: boolean
  user: AdminUser
  self: boolean
  defaultLimit: number
  onClose: () => void
  onSaved: (u: AdminUser) => void
}) {
  return (
    <Modal open={open} onClose={onClose} label={u.email} title="Edit profile">
      <EditForm user={u} self={self} defaultLimit={defaultLimit} onClose={onClose} onSaved={onSaved} />
    </Modal>
  )
}

function EditForm({
  user: u,
  self,
  defaultLimit,
  onClose,
  onSaved,
}: {
  user: AdminUser
  self: boolean
  defaultLimit: number
  onClose: () => void
  onSaved: (u: AdminUser) => void
}) {
  const { toast } = useToast()
  const [name, setName] = useState(u.name)
  const [role, setRole] = useState<Role>(u.role)
  const [useDefault, setUseDefault] = useState(u.creditLimit == null)
  const [limit, setLimit] = useState(String(u.creditLimit ?? defaultLimit))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function submit(e: FormEvent) {
    e.preventDefault()
    const patch: UpdateUserRequest = {}
    if (name.trim() !== u.name) patch.name = name.trim()
    if (role !== u.role) patch.role = role
    const creditLimit = useDefault ? null : Number(limit)
    if (creditLimit !== u.creditLimit) patch.creditLimit = creditLimit
    if (!Object.keys(patch).length) {
      toast('Nothing changed.', 'info')
      return onClose()
    }
    setBusy(true)
    setError(null)
    try {
      onSaved((await api.updateUser(u.id, patch)).user)
      toast(`Saved ${u.email}.`)
      onClose()
    } catch (err) {
      setError(errorMessage(err))
      setBusy(false)
    }
  }

  return (
    <form onSubmit={submit} className="mt-5 space-y-4">
      <div>
        <Label htmlFor="edit-name">Name</Label>
        <Input id="edit-name" required maxLength={80} value={name} onChange={(e) => setName(e.target.value)} />
      </div>
      <div>
        <Label htmlFor="edit-role">Role</Label>
        <Select id="edit-role" value={role} disabled={self} aria-describedby={self ? 'edit-role-note' : undefined} onChange={(e) => setRole(e.target.value as Role)}>
          <option value="user">User</option>
          <option value="admin">Admin</option>
        </Select>
        {self && (
          <p id="edit-role-note" className="mt-1.5 text-[12px] text-muted">
            You can't demote your own account.
          </p>
        )}
      </div>
      <fieldset>
        <legend className="label mb-1.5">Daily AI credits</legend>
        <label className="mb-2 flex items-center gap-2 text-sm">
          <input type="checkbox" checked={useDefault} onChange={(e) => setUseDefault(e.target.checked)} className="size-4 accent-[var(--accent-ink)]" />
          Use default ({fmtNum(defaultLimit)})
        </label>
        <Input
          aria-label="Custom daily credit limit"
          type="number"
          inputMode="numeric"
          required={!useDefault}
          min={0}
          max={100000}
          step={1}
          disabled={useDefault}
          value={useDefault ? '' : limit}
          placeholder={String(defaultLimit)}
          onChange={(e) => setLimit(e.target.value)}
        />
        <p className="mt-1.5 text-[12px] text-muted">0 blocks every AI call. Applies from their next AI call.</p>
      </fieldset>
      {error && <FormError>{error}</FormError>}
      <div className="flex justify-end gap-2 pt-2">
        <Button variant="secondary" disabled={busy} onClick={onClose}>
          Cancel
        </Button>
        <Button type="submit" loading={busy}>
          Save changes
        </Button>
      </div>
    </form>
  )
}

function PasswordDialog({
  open,
  user: u,
  self,
  onClose,
  onSaved,
}: {
  open: boolean
  user: AdminUser
  self: boolean
  onClose: () => void
  onSaved: (u: AdminUser) => void
}) {
  // The generated password lives here only while the dialog is open; closing clears it.
  const [generated, setGenerated] = useState<string | null>(null)
  const close = () => {
    setGenerated(null)
    onClose()
  }
  return (
    <Modal open={open} onClose={close} locked={!!generated} label={u.email} title={generated ? 'New password set.' : 'Set a password'}>
      {generated ? (
        <PasswordReveal email={u.email} password={generated} onDone={close} />
      ) : (
        <PasswordForm
          user={u}
          self={self}
          onCancel={close}
          onSet={(user, password) => {
            onSaved(user)
            if (password) setGenerated(password)
            else close()
          }}
        />
      )}
    </Modal>
  )
}

function PasswordForm({
  user: u,
  self,
  onCancel,
  onSet,
}: {
  user: AdminUser
  self: boolean
  onCancel: () => void
  onSet: (u: AdminUser, generated?: string) => void
}) {
  const { toast } = useToast()
  const [mode, setMode] = useState<'generate' | 'type'>('generate')
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function submit(e: FormEvent) {
    e.preventDefault()
    const problem = mode === 'type' ? passwordProblem(password) : null
    if (problem) return setError(problem)
    setBusy(true)
    setError(null)
    try {
      const res = await api.setPassword(u.id, mode === 'type' ? password : undefined)
      toast(`Password set for ${u.email}.`)
      onSet(res.user, res.password)
    } catch (err) {
      setError(errorMessage(err))
      setBusy(false)
    }
  }

  return (
    <form onSubmit={submit} className="mt-3">
      <p className="text-sm leading-relaxed text-muted">
        Signs <span className="break-all">{u.email}</span> out of every session{self ? ' except this one' : ''}. A disabled
        account stays disabled.
      </p>
      <fieldset className="mt-5 space-y-2">
        <legend className="label mb-1.5">Password</legend>
        <label className="flex items-center gap-2 text-sm">
          <input type="radio" name="pw-mode" checked={mode === 'generate'} onChange={() => setMode('generate')} className="accent-[var(--accent-ink)]" />
          Generate one (shown once)
        </label>
        <label className="flex items-center gap-2 text-sm">
          <input type="radio" name="pw-mode" checked={mode === 'type'} onChange={() => setMode('type')} className="accent-[var(--accent-ink)]" />
          Type one
        </label>
      </fieldset>
      {mode === 'type' && (
        <div className="mt-3">
          <Label htmlFor="set-password">New password</Label>
          <Input id="set-password" type="password" autoComplete="new-password" required value={password} onChange={(e) => setPassword(e.target.value)} />
          <p className="mt-1.5 text-[12px] text-muted">12–72 characters.</p>
        </div>
      )}
      {error && <FormError>{error}</FormError>}
      <div className="mt-6 flex justify-end gap-2">
        <Button variant="secondary" disabled={busy} onClick={onCancel}>
          Cancel
        </Button>
        <Button type="submit" loading={busy}>
          {mode === 'generate' ? 'Generate password' : 'Set password'}
        </Button>
      </div>
    </form>
  )
}

/** Copies another account's showcase content into this one: POST /api/admin/users/{id}/seed. Submitting is the confirm. */
function SeedForm({ user: u, onClose, onSaved }: { user: AdminUser; onClose: () => void; onSaved: (u: AdminUser) => void }) {
  const { toast } = useToast()
  const [from, setFrom] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function submit(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError(null)
    try {
      const res = await api.seedUser(u.id, from.trim())
      onSaved(res.user)
      toast(`Copied ${seededSummary(res.seeded)} into ${u.email}.${seededWarning(res.seeded)}`)
      onClose()
    } catch (err) {
      setError(errorMessage(err))
      setBusy(false)
    }
  }

  return (
    <form onSubmit={submit} className="mt-3">
      <p className="text-sm leading-relaxed text-muted">
        Adds a copy of that account's showcase content to <span className="break-all">{u.email}</span>: its 3 newest forecasts
        with their update history plus up to 2 resolved ones, AI portfolios that beat their saved benchmark (up to 2), strategies with Sharpe above
        0.5 that beat buy-and-hold (up to 3) and its best arena run vs BTC. Winners first; when none qualifies, the single best
        one. Nothing is replaced: running it again adds the
        portfolios, strategies and arena run again, but skips forecasts already copied. Never its chats or trades.
      </p>
      <div className="mt-5">
        <Label htmlFor="seed-from">Copy from</Label>
        <Input id="seed-from" type="email" required maxLength={254} autoComplete="off" value={from} onChange={(e) => setFrom(e.target.value)} />
      </div>
      {error && <FormError>{error}</FormError>}
      <div className="mt-6 flex justify-end gap-2">
        <Button variant="secondary" disabled={busy} onClick={onClose}>
          Cancel
        </Button>
        <Button type="submit" loading={busy}>
          Copy content
        </Button>
      </div>
    </form>
  )
}

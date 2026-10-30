import { useEffect, useState, type FormEvent } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router'
import { Page, PageHeader } from '../components/layout/AdminShell'
import { useToast } from '../components/toast'
import { CreditsUsed, UserBadges } from '../components/UserBadges'
import { Button } from '../components/ui/Button'
import { Card } from '../components/ui/Card'
import { EmptyState, ErrorState } from '../components/ui/EmptyState'
import { IconArrowDown, IconArrowUp, IconPlus, IconSearch } from '../components/ui/icons'
import { Input, Label, Select } from '../components/ui/Input'
import { FormError, Modal } from '../components/ui/Modal'
import { PasswordReveal } from '../components/ui/PasswordReveal'
import { Skeleton } from '../components/ui/Spinner'
import { api, errorMessage } from '../lib/api'
import { seededSummary, seededWarning } from '../lib/audit'
import { fmtDate, fmtNum, fmtUsd, timeAgo } from '../lib/format'
import { passwordProblem } from '../lib/password'
import type { AdminUser, Role, SortOrder, UserQuery, UserSort } from '../lib/types'
import { useLoad } from '../lib/useLoad'

const LIMIT = 25
const SORTS: readonly UserSort[] = ['createdAt', 'lastActiveAt', 'lastLoginAt', 'email', 'name', 'creditsToday']
const pick = <T extends string>(v: string | null, allowed: readonly T[]) => (allowed.includes(v as T) ? (v as T) : undefined)

/** The list query from the URL; unknown values are dropped so a hand-edited link never turns into a 400. */
export function queryFromParams(p: URLSearchParams): UserQuery {
  const page = Number(p.get('page'))
  return {
    q: p.get('q') || undefined,
    status: pick(p.get('status'), ['all', 'active', 'disabled'] as const),
    role: pick(p.get('role'), ['user', 'admin'] as const),
    sort: pick(p.get('sort'), SORTS),
    order: pick(p.get('order'), ['asc', 'desc'] as const),
    page: Number.isInteger(page) && page > 1 ? page : undefined,
  }
}

export function UsersPage() {
  const [params, setParams] = useSearchParams()
  const query = queryFromParams(params)
  const key = JSON.stringify(query)
  const { data, error, loading, reload } = useLoad((signal) => api.users({ ...query, limit: LIMIT }, signal), [key])
  const [search, setSearch] = useState(query.q ?? '')
  const [creating, setCreating] = useState(false)

  /** Sets (or with undefined, removes) query params; any filter change goes back to page 1. */
  const update = (next: Record<string, string | undefined>) =>
    setParams(
      (p) => {
        const out = new URLSearchParams(p)
        for (const [k, v] of Object.entries(next)) if (v) out.set(k, v); else out.delete(k)
        if (!('page' in next)) out.delete('page')
        return out
      },
      { replace: 'q' in next }, // typing doesn't fill the history; filters and pages do
    )

  // The search box writes `q` 300 ms after the last keystroke; a back/forward navigation writes the box.
  useEffect(() => setSearch(query.q ?? ''), [query.q])
  useEffect(() => {
    if (search === (query.q ?? '')) return
    const t = setTimeout(() => update({ q: search }), 300)
    return () => clearTimeout(t)
  }, [search, query.q]) // not `update`: a fresh closure every render

  const sortBy = (sort: UserSort) => {
    const same = (query.sort ?? 'createdAt') === sort
    const order: SortOrder = same ? ((query.order ?? 'desc') === 'desc' ? 'asc' : 'desc') : sort === 'name' || sort === 'email' ? 'asc' : 'desc'
    update({ sort, order })
  }
  const filtered = !!(query.q || (query.status && query.status !== 'all') || query.role)
  const page = data?.page ?? query.page ?? 1
  const pages = data ? Math.max(1, Math.ceil(data.total / data.limit)) : 1

  return (
    <Page>
      <PageHeader
        label="Accounts"
        title="Users."
        subtitle={data ? `${fmtNum(data.total)} ${filtered ? 'matching ' : ''}account${data.total === 1 ? '' : 's'}` : 'Every Foretrade account.'}
        actions={
          <Button onClick={() => setCreating(true)}>
            <IconPlus size={16} /> New user
          </Button>
        }
      />

      <div className="mb-4 flex flex-wrap items-end gap-3">
        <div className="min-w-0 flex-1 basis-60">
          <Label htmlFor="user-search">Search</Label>
          <div className="relative">
            <IconSearch size={16} className="pointer-events-none absolute top-1/2 left-3 -translate-y-1/2 text-muted" />
            <Input
              id="user-search"
              type="search"
              maxLength={100}
              placeholder="Name or email"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              className="pl-9"
            />
          </div>
        </div>
        <div className="w-[calc(50%-0.375rem)] sm:w-40">
          <Label htmlFor="user-status">Status</Label>
          <Select id="user-status" value={query.status ?? 'all'} onChange={(e) => update({ status: e.target.value === 'all' ? undefined : e.target.value })}>
            <option value="all">All</option>
            <option value="active">Active</option>
            <option value="disabled">Disabled</option>
          </Select>
        </div>
        <div className="w-[calc(50%-0.375rem)] sm:w-40">
          <Label htmlFor="user-role">Role</Label>
          <Select id="user-role" value={query.role ?? ''} onChange={(e) => update({ role: e.target.value || undefined })}>
            <option value="">All roles</option>
            <option value="user">Users</option>
            <option value="admin">Admins</option>
          </Select>
        </div>
      </div>

      {error && !data ? (
        <ErrorState message={error} onRetry={reload} />
      ) : (
        <Card className="overflow-hidden">
          {error && <FormError>{error}</FormError>}
          <div className="overflow-x-auto" aria-busy={loading || undefined}>
            <table className="data-table">
              <caption className="sr-only">Users, page {page} of {pages}</caption>
              <thead>
                <tr>
                  <SortHeader label="User" sort="name" query={query} onSort={sortBy} />
                  <th scope="col">Role · status</th>
                  <SortHeader label="Created" sort="createdAt" query={query} onSort={sortBy} />
                  <SortHeader label="Last login" sort="lastLoginAt" query={query} onSort={sortBy} />
                  <SortHeader label="Last active" sort="lastActiveAt" query={query} onSort={sortBy} />
                  <SortHeader label="Credits today" sort="creditsToday" query={query} onSort={sortBy} />
                  <th scope="col" className="text-right">Equity</th>
                  <th scope="col" className="text-right">Sessions</th>
                </tr>
              </thead>
              <tbody>
                {!data
                  ? [0, 1, 2, 3, 4].map((i) => (
                      <tr key={i}>
                        <td colSpan={8}>
                          <Skeleton className="h-5 w-full" />
                        </td>
                      </tr>
                    ))
                  : data.users.map((u) => <UserRow key={u.id} user={u} defaultLimit={data.defaultCreditLimit} />)}
              </tbody>
            </table>
          </div>
          {data && !data.users.length && (
            <EmptyState
              className="m-5"
              title={data.total ? 'Nothing on this page.' : filtered ? 'No accounts match.' : 'No accounts yet.'}
              description={data.total ? `There are ${fmtNum(data.total)} matching accounts.` : filtered ? 'Try another search or clear the filters.' : undefined}
              action={
                data.total ? (
                  <Button variant="secondary" onClick={() => update({ page: undefined })}>
                    Go to page 1
                  </Button>
                ) : filtered ? (
                  <Button variant="secondary" onClick={() => setParams({})}>
                    Clear filters
                  </Button>
                ) : undefined
              }
            />
          )}
          {data && data.total > 0 && (
            <nav aria-label="Pagination" className="flex flex-wrap items-center justify-between gap-3 border-t border-border px-5 py-3">
              <span className="text-[13px] text-muted tabular-nums">
                {data.users.length ? `${fmtNum((page - 1) * data.limit + 1)}–${fmtNum((page - 1) * data.limit + data.users.length)} of ${fmtNum(data.total)}` : `Page ${page}`}
              </span>
              <div className="flex gap-2">
                <Button variant="secondary" size="sm" disabled={page <= 1} onClick={() => update({ page: page > 2 ? String(page - 1) : undefined })}>
                  Previous
                </Button>
                <Button variant="secondary" size="sm" disabled={page >= pages} onClick={() => update({ page: String(page + 1) })}>
                  Next
                </Button>
              </div>
            </nav>
          )}
        </Card>
      )}

      <NewUserDialog open={creating} onClose={() => setCreating(false)} defaultLimit={data?.defaultCreditLimit} />
    </Page>
  )
}

function SortHeader({ label, sort, query, onSort }: { label: string; sort: UserSort; query: UserQuery; onSort: (s: UserSort) => void }) {
  const active = (query.sort ?? 'createdAt') === sort
  const order = query.order ?? 'desc'
  const Arrow = order === 'asc' ? IconArrowUp : IconArrowDown
  return (
    <th scope="col" aria-sort={active ? (order === 'asc' ? 'ascending' : 'descending') : undefined}>
      <button type="button" onClick={() => onSort(sort)} className="inline-flex items-center gap-1 rounded uppercase hover:text-text">
        {label}
        {active && <Arrow size={12} />}
      </button>
    </th>
  )
}

function UserRow({ user: u, defaultLimit }: { user: AdminUser; defaultLimit: number }) {
  const navigate = useNavigate()
  return (
    <tr onClick={() => navigate(`/users/${u.id}`)} className="cursor-pointer transition-colors hover:bg-surface-2/60">
      <td className="max-w-72">
        <Link to={`/users/${u.id}`} onClick={(e) => e.stopPropagation()} className="block truncate font-medium text-text hover:underline">
          {u.name}
        </Link>
        <span className="block truncate text-[12.5px] text-muted">{u.email}</span>
      </td>
      <td className="min-w-52">
        <UserBadges user={u} />
      </td>
      <td className="whitespace-nowrap">{fmtDate(u.createdAt)}</td>
      <td className="whitespace-nowrap">{timeAgo(u.lastLoginAt)}</td>
      <td className="whitespace-nowrap">{timeAgo(u.lastActiveAt)}</td>
      <td className="whitespace-nowrap">
        <CreditsUsed user={u} defaultLimit={defaultLimit} />
      </td>
      <td className="text-right whitespace-nowrap">{fmtUsd(u.equity, { decimals: 0 })}</td>
      <td className="text-right">{fmtNum(u.sessions)}</td>
    </tr>
  )
}

type Created = { user: AdminUser; password?: string }

function NewUserDialog({ open, onClose, defaultLimit }: { open: boolean; onClose: () => void; defaultLimit?: number }) {
  const navigate = useNavigate()
  // The generated password lives here only while the dialog is open; closing clears it.
  const [created, setCreated] = useState<Created | null>(null)

  const finish = (c: Created | null = created) => {
    setCreated(null)
    onClose()
    if (c) navigate(`/users/${c.user.id}`)
  }

  return (
    <Modal
      open={open}
      onClose={() => finish()}
      locked={!!created?.password}
      label="New account"
      title={created?.password ? 'Account created.' : 'Create a user.'}
    >
      {created?.password ? (
        <PasswordReveal email={created.user.email} password={created.password} onDone={() => finish()} />
      ) : (
        <NewUserForm defaultLimit={defaultLimit} onCancel={() => finish(null)} onCreated={(c) => (c.password ? setCreated(c) : finish(c))} />
      )}
    </Modal>
  )
}

function NewUserForm({ defaultLimit, onCancel, onCreated }: { defaultLimit?: number; onCancel: () => void; onCreated: (c: Created) => void }) {
  const { toast } = useToast()
  const [email, setEmail] = useState('')
  const [name, setName] = useState('')
  const [role, setRole] = useState<Role>('user')
  const [limit, setLimit] = useState('')
  const [password, setPassword] = useState('')
  // ponytail: off and blank, as a fresh install has no demo account (DEMO_EMAIL is opt-in); prefill from the API if needed.
  const [showcase, setShowcase] = useState(false)
  const [seedFrom, setSeedFrom] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  async function submit(e: FormEvent) {
    e.preventDefault()
    const problem = password ? passwordProblem(password) : null
    if (problem) return setError(problem)
    setError(null)
    setBusy(true)
    try {
      const res = await api.createUser({
        email: email.trim(),
        name: name.trim(),
        role,
        ...(limit !== '' && { creditLimit: Number(limit) }),
        ...(password && { password }),
        ...(showcase && { seedFrom: seedFrom.trim() }),
      })
      toast(res.seeded ? `Created ${res.user.email}. Copied ${seededSummary(res.seeded)}.${seededWarning(res.seeded)}` : `Created ${res.user.email}.`)
      onCreated(res)
    } catch (err) {
      setError(errorMessage(err))
      setBusy(false)
    }
  }

  return (
    <form onSubmit={submit} className="mt-5 space-y-4">
      <div>
        <Label htmlFor="new-email">Email</Label>
        <Input id="new-email" type="email" required maxLength={254} autoComplete="off" value={email} onChange={(e) => setEmail(e.target.value)} />
      </div>
      <div>
        <Label htmlFor="new-name">Name</Label>
        <Input id="new-name" required maxLength={80} autoComplete="off" value={name} onChange={(e) => setName(e.target.value)} />
      </div>
      <div className="grid grid-cols-2 gap-3">
        <div>
          <Label htmlFor="new-role">Role</Label>
          <Select id="new-role" value={role} onChange={(e) => setRole(e.target.value as Role)}>
            <option value="user">User</option>
            <option value="admin">Admin</option>
          </Select>
        </div>
        <div>
          <Label htmlFor="new-limit">Daily credits</Label>
          <Input
            id="new-limit"
            type="number"
            inputMode="numeric"
            min={0}
            max={100000}
            step={1}
            placeholder={defaultLimit != null ? `Default (${fmtNum(defaultLimit)})` : 'Default'}
            value={limit}
            onChange={(e) => setLimit(e.target.value)}
          />
        </div>
      </div>
      <div>
        <Label htmlFor="new-password">Password</Label>
        <Input
          id="new-password"
          type="password"
          autoComplete="new-password"
          placeholder="Leave blank to generate one"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          aria-describedby="new-password-hint"
        />
        <p id="new-password-hint" className="mt-1.5 text-[12.5px] text-muted">
          12–72 characters. A generated password is shown once, after you create the account.
        </p>
      </div>
      <fieldset>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={showcase} onChange={(e) => setShowcase(e.target.checked)} className="size-4 accent-[var(--accent-ink)]" />
          Start with showcase content
        </label>
        {showcase && (
          <div className="mt-2">
            <Label htmlFor="new-seed-from">Copy from</Label>
            <Input
              id="new-seed-from"
              type="email"
              required
              maxLength={254}
              autoComplete="off"
              value={seedFrom}
              onChange={(e) => setSeedFrom(e.target.value)}
              aria-describedby="new-seed-hint"
            />
            <p id="new-seed-hint" className="mt-1.5 text-[12.5px] text-muted">
              Copies that account's showcase content: its 3 newest forecasts with their update history plus up to 2 resolved ones, AI
              portfolios that beat their saved benchmark (up to 2), strategies with Sharpe above 0.5 that beat buy-and-hold (up to 3) and its best arena run
              vs BTC. Winners first; when none qualifies, the single best one. Never its chats or trades.
            </p>
          </div>
        )}
      </fieldset>
      {role === 'admin' && <p className="text-[12.5px] text-accent-ink">Admins can sign in to this console and manage every account.</p>}
      {error && <FormError>{error}</FormError>}
      <div className="flex justify-end gap-2 pt-2">
        <Button variant="secondary" disabled={busy} onClick={onCancel}>
          Cancel
        </Button>
        <Button type="submit" loading={busy}>
          Create user
        </Button>
      </div>
    </form>
  )
}


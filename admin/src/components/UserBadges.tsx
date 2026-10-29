import { fmtNum } from '../lib/format'
import type { AdminUser } from '../lib/types'
import { Badge } from './ui/Badge'

/** Role, status and managed badges for one account. */
export function UserBadges({ user }: { user: Pick<AdminUser, 'role' | 'status' | 'managed'> }) {
  return (
    <span className="inline-flex flex-wrap gap-1.5">
      <Badge tone={user.role === 'admin' ? 'accent' : 'outline'}>{user.role}</Badge>
      <Badge tone={user.status === 'active' ? 'up' : 'down'}>{user.status}</Badge>
      {user.managed && (
        <Badge tone="neutral" title="Bootstrap account: its password comes from the server config">
          managed
        </Badge>
      )}
    </span>
  )
}

/** "12 / 500": the limit is muted when it is the default. */
export function CreditsUsed({ user, defaultLimit }: { user: Pick<AdminUser, 'creditsToday' | 'creditLimit'>; defaultLimit: number }) {
  const custom = user.creditLimit != null
  return (
    <span className="font-mono tabular-nums">
      {fmtNum(user.creditsToday)} /{' '}
      <span className={custom ? 'text-text' : 'text-muted'} title={custom ? 'Custom limit' : 'Default limit'}>
        {fmtNum(user.creditLimit ?? defaultLimit)}
      </span>
    </span>
  )
}

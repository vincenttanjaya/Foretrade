// Human labels for admin_audit rows (API-CONTRACT.md → Admin console, AuditEntry). `detail` comes from the API as
// untyped JSON, so every field is read defensively and rendered as plain text.
import type { AuditEntry } from './types'

const LABELS: Record<string, string> = {
  'user.create': 'Created user',
  'user.update': 'Updated user',
  'user.disable': 'Disabled user',
  'user.enable': 'Enabled user',
  'user.password': 'Set password',
  'user.sessions_revoke': 'Revoked sessions',
  'user.portfolio_reset': 'Reset portfolio',
  'user.credits_reset': "Reset today's credits",
  'user.delete': 'Deleted user',
  'user.seed': 'Copied showcase content',
  'settings.update': 'Updated settings',
  'settings.chatgpt.signin': 'Signed in with ChatGPT',
  'settings.chatgpt.signout': 'Signed out of ChatGPT',
}

export function auditLabel(e: Pick<AuditEntry, 'action' | 'detail'>) {
  if (e.action === 'user.password' && e.detail.generated === true) return 'Generated password'
  return LABELS[e.action] ?? e.action
}

const str = (v: unknown) => (v === null || v === undefined ? '' : typeof v === 'object' ? JSON.stringify(v) : String(v))
const limit = (v: unknown) => (v === null ? 'default' : str(v))
const plural = (n: unknown, one: string) => `${str(n)} ${one}${n === 1 ? '' : 's'}`

const SEEDED: [key: string, one: string, many: string][] = [
  ['forecasts', 'forecast', 'forecasts'],
  ['portfolios', 'AI portfolio', 'AI portfolios'],
  ['strategies', 'strategy', 'strategies'],
  ['arenaRuns', 'arena run', 'arena runs'],
]

/** "3 forecasts · 2 AI portfolios · 3 strategies · 1 arena run" for a `seeded` count (an API body or audit detail). */
export function seededSummary(seeded: unknown) {
  const d = (seeded && typeof seeded === 'object' ? seeded : {}) as Record<string, unknown>
  return SEEDED.map(([k, one, many]) => `${str(d[k] ?? 0)} ${d[k] === 1 ? one : many}`).join(' · ')
}

const EMPTY: [key: string, what: string][] = [
  ['portfolios', 'AI portfolio'],
  ['strategies', 'strategy'],
]

/** " The source account has no AI portfolio to copy. Build one there first." per showcase tab a seed left empty; "" otherwise. */
export function seededWarning(seeded: unknown) {
  const d = (seeded && typeof seeded === 'object' ? seeded : {}) as Record<string, unknown>
  return EMPTY.filter(([k]) => d[k] === 0)
    .map(([, what]) => ` The source account has no ${what} to copy. Build one there first.`)
    .join('')
}

const FIELDS: [key: string, label: string, show: (v: unknown) => string][] = [
  ['name', 'name', str],
  ['role', 'role', str],
  ['creditLimit', 'credit limit', limit],
]

/** One line for the detail column: "role: user → admin", "2 sessions revoked", "" when there is nothing to say. */
export function auditSummary({ action, detail: d }: Pick<AuditEntry, 'action' | 'detail'>) {
  switch (action) {
    case 'user.create':
      return [
        `role ${str(d.role)}`,
        `credit limit ${limit(d.creditLimit ?? null)}`,
        d.passwordGenerated ? 'password generated' : 'password set by admin',
        ...(d.seededFrom ? [`showcase from ${str(d.seededFrom)}: ${seededSummary(d.seeded)}`] : []),
      ].join(' · ')
    case 'user.seed':
      return `from ${str(d.seededFrom)}: ${seededSummary(d.seeded)}`
    case 'user.update':
      return FIELDS.filter(([k]) => d[k] && typeof d[k] === 'object')
        .map(([k, label, show]) => {
          const c = d[k] as { from?: unknown; to?: unknown }
          return `${label}: ${show(c.from)} → ${show(c.to)}`
        })
        .join(' · ')
    case 'user.disable':
    case 'user.sessions_revoke':
      return `${plural(d.sessionsRevoked ?? 0, 'session')} revoked`
    case 'user.password':
      return `${d.generated ? 'generated' : 'set by admin'} · ${plural(d.sessionsRevoked ?? 0, 'session')} revoked`
    case 'user.credits_reset':
      return `${str(d.creditsBefore ?? 0)} credits used before`
    case 'user.delete':
      return [str(d.name), str(d.role)].filter(Boolean).join(' · ')
    case 'settings.update': // changed field names only, never a value
      return ((Object.values(d).find(Array.isArray) ?? []) as unknown[]).map(str).join(', ')
    default:
      return ''
  }
}

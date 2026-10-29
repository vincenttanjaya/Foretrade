// Display formatters. All return '—' for missing / non-finite numbers.

const DASH = '—'
const ok = (n: number | null | undefined): n is number => typeof n === 'number' && Number.isFinite(n)

/** "$100,000.00"; `sign` adds "+" to gains, `compact` gives "$1.2B". */
export function fmtUsd(n: number | null | undefined, opts: { decimals?: number; sign?: boolean; compact?: boolean } = {}) {
  if (!ok(n)) return DASH
  const d = opts.decimals ?? (opts.compact ? 1 : 2)
  return n.toLocaleString('en-US', {
    style: 'currency',
    currency: 'USD',
    notation: opts.compact ? 'compact' : 'standard',
    minimumFractionDigits: opts.compact ? 0 : d,
    maximumFractionDigits: d,
    signDisplay: opts.sign ? 'exceptZero' : 'auto',
  })
}

/** "$84,213.20", "$2.4513", "$0.081234" — precision follows magnitude. */
export function fmtPrice(n: number | null | undefined) {
  if (!ok(n)) return DASH
  const a = Math.abs(n)
  const digits: Intl.NumberFormatOptions =
    a >= 1000
      ? { minimumFractionDigits: 2, maximumFractionDigits: 2 }
      : a >= 1
        ? { minimumFractionDigits: 2, maximumFractionDigits: 4 }
        : { minimumSignificantDigits: 4, maximumSignificantDigits: 6 }
  return n.toLocaleString('en-US', { style: 'currency', currency: 'USD', ...digits })
}

/** `n` is already a percentage: fmtPct(12.5) → "12.50%", with sign → "+12.50%". */
export function fmtPct(n: number | null | undefined, opts: { sign?: boolean; decimals?: number } = {}) {
  if (!ok(n)) return DASH
  const d = opts.decimals ?? 2
  const s = n.toLocaleString('en-US', {
    minimumFractionDigits: d,
    maximumFractionDigits: d,
    signDisplay: opts.sign ? 'exceptZero' : 'auto',
  })
  return `${s}%`
}

/** Position / order size: up to 6 decimals, trailing zeros dropped. */
export function fmtQty(n: number | null | undefined) {
  if (!ok(n)) return DASH
  return n.toLocaleString('en-US', { maximumFractionDigits: Math.abs(n) >= 1 ? 4 : 6 })
}

/** "just now", "12m ago", "3h ago", "4d ago", then a date. */
export function timeAgo(iso: string | null | undefined) {
  if (!iso) return DASH
  const s = (Date.now() - new Date(iso).getTime()) / 1000
  if (!Number.isFinite(s)) return DASH
  if (s < 45) return 'just now'
  if (s < 3600) return `${Math.round(s / 60)}m ago`
  if (s < 86400) return `${Math.round(s / 3600)}h ago`
  if (s < 7 * 86400) return `${Math.round(s / 86400)}d ago`
  return fmtDate(iso)
}

/** "Oct 2, 2026". Date-only strings ("2026-10-02") are calendar dates, so they are not shifted by time zone. */
export function fmtDate(iso: string | null | undefined, opts: { time?: boolean } = {}) {
  if (!iso) return DASH
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return DASH
  const dateOnly = /^\d{4}-\d{2}-\d{2}$/.test(iso)
  return d.toLocaleString('en-US', {
    month: 'short',
    day: 'numeric',
    year: 'numeric',
    ...(opts.time && !dateOnly ? { hour: 'numeric', minute: '2-digit' } : {}),
    ...(dateOnly ? { timeZone: 'UTC' } : {}),
  })
}

/** Whole counts: 12345 → "12,345". */
export function fmtNum(n: number | null | undefined) {
  if (!ok(n)) return DASH
  return n.toLocaleString('en-US', { maximumFractionDigits: 0 })
}

/** "1 admin", "2 admins", "1,204 sessions". */
export const plural = (n: number, one: string) => `${fmtNum(n)} ${one}${n === 1 ? '' : 's'}`

/** A UTC calendar day ("2026-10-02") → "Oct 2". */
export function fmtDay(day: string) {
  const d = new Date(`${day}T00:00:00Z`)
  if (Number.isNaN(d.getTime())) return DASH
  return d.toLocaleString('en-US', { month: 'short', day: 'numeric', timeZone: 'UTC' })
}

/** Seconds → "3d 4h", "5h 12m", "8m". */
export function fmtDuration(sec: number | null | undefined) {
  if (!ok(sec) || sec < 0) return DASH
  const d = Math.floor(sec / 86400)
  const h = Math.floor((sec % 86400) / 3600)
  const m = Math.floor((sec % 3600) / 60)
  if (d) return `${d}d ${h}h`
  if (h) return `${h}h ${m}m`
  return `${m}m`
}

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

/**
 * Outlook and forecast levels, the rule the API's market.FormatLevel shares (both test the same table,
 * level-format-cases.json): from $1M three significant figures ("$2.12M", "$12.3M", "$123M"); from $1,000 whole
 * dollars with grouping ("$2,424"); from $1 two decimals ("$482.38", "$96.44") unless the level is whole to the cent
 * ("$60", "$250"); below $1 three significant figures with at least two decimals ("$0.0882", "$0.50", "$0.03"), never
 * an exponent. The rounded value picks the tier: $999.996 reads "$1,000", $999,999.7 "$1.00M" and $0.9996 "$1"; a tie
 * rounds up as the API's does ($99.995 reads "$100", $1.045 "$1.05").
 * Trade tickets keep fmtPrice's venue precision.
 */
export function fmtLevelPrice(n: number | null | undefined) {
  if (!ok(n)) return DASH
  // The API's arithmetic, step for step (x·10^d rounded half up, then printed), so a binary tie such as $99.995 or
  // $1.045 rounds the same way on both sides: toFixed/toPrecision round the double's exact decimal instead.
  const sign = n < 0 ? '-' : ''
  const a = Math.abs(n)
  if (a === 0) return '$0'
  if (a >= 999_999.5) {
    let [unit, suffix] = [1e6, 'M']
    if (roundSig(a / 1e6, 3) >= 1000) {
      ;[unit, suffix] = [1e9, 'B']
      if (roundSig(a / 1e9, 3) >= 1000) [unit, suffix] = [1e12, 'T']
    }
    const m = roundSig(a / unit, 3)
    const d = Math.max(2 - Math.floor(Math.log10(m)), 0)
    return `${sign}$${d > 0 ? m.toFixed(d) : Math.round(m).toLocaleString('en-US')}${suffix}`
  }
  if (a < 1) {
    const d = 2 - Math.floor(Math.log10(a)) // decimals for three significant figures
    const p = Math.pow(10, d)
    const r = Math.round(a * p) / p
    if (r >= 1) return `${sign}$1`
    let s = r.toFixed(d)
    while (s.endsWith('0') && s.length - s.indexOf('.') > 3) s = s.slice(0, -1) // "0.500" → "0.50"
    return `${sign}$${s}`
  }
  const cents = Math.round(a * 100)
  if (cents >= 100_000) return `${sign}$${Math.round(a).toLocaleString('en-US')}`
  if (cents % 100 === 0) return `${sign}$${cents / 100}`
  return `${sign}$${(cents / 100).toFixed(2)}`
}

/**
 * x > 0 to `figs` significant figures, as the API's roundSig computes it: a whole-number step (10^e, e ≥ 0) divides
 * and multiplies by that exact power, so a million stays 1000000 (multiplying by the fractional 10^−e, which floats
 * cannot hold, gave 999999.9999999999); finer steps multiply by the exact 10^−e.
 */
function roundSig(x: number, figs: number) {
  const e = Math.floor(Math.log10(x)) - figs + 1
  if (e >= 0) {
    const p = Math.pow(10, e)
    return Math.round(x / p) * p
  }
  const p = Math.pow(10, -e)
  return Math.round(x * p) / p
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

/** 1234567 → "1.2M". */
export function fmtCompact(n: number | null | undefined) {
  if (!ok(n)) return DASH
  return n.toLocaleString('en-US', { notation: 'compact', maximumFractionDigits: 1 })
}

/** Position / order size: up to 6 decimals, trailing zeros dropped. */
export function fmtQty(n: number | null | undefined) {
  if (!ok(n)) return DASH
  return n.toLocaleString('en-US', { maximumFractionDigits: Math.abs(n) >= 1 ? 4 : 6 })
}

/** Probability 0..1 → "62%". */
export function fmtProb(p: number | null | undefined) {
  if (!ok(p)) return DASH
  if (p > 0 && p < 0.005) return '<1%'
  if (p < 1 && p > 0.995) return '>99%'
  return `${Math.round(p * 100)}%`
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

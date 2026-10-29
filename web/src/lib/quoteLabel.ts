// How a price from a quote reads beside it, everywhere one shows (Trade page header and ticket, market list, screener,
// agent cards). The API sends each quote's honest label (RT-QUOTES §5) and the web shows it as sent; the same rule,
// from the quote's class and measured delay, labels a quote whose API sent no label:
//   - class realtime, under 60 s behind:                     "real-time · 04:20:15 UTC" (the last trade's time)
//   - class realtime or near-realtime, at most 5 min behind: "about 2 min behind · 04:18 UTC" (SGX's snapshot)
//   - otherwise, Yahoo always and an unknown delay included: "delayed · last trade 04:00 UTC"
// Nothing says "live". An API before the real-time sources sends no class: Yahoo's quotes are delayed then, and the live
// screener's quote reads real-time only at a 0-minute delay.
import type { QuoteClass, QuoteFeed, QuoteTimeBasis } from './types'

/** The quote fields a label reads. */
export type LabelQuote = {
  source?: string | null
  class?: QuoteClass | null
  lastTradeAt: string
  delaySeconds?: number | null
  delayMinutes?: number | null // the screener's: the venue's measured Yahoo delay, or a real-time quote's delaySeconds / 60
  timeBasis?: QuoteTimeBasis | null
  label?: string | null
}
export type Freshness = 'realtime' | 'behind' | 'delayed'

const finite = (n: unknown): n is number => typeof n === 'number' && Number.isFinite(n)

/** How far behind the exchange the quote ran when read, in seconds (never below 0); null when the quote does not say. */
export function delaySeconds(q: Pick<LabelQuote, 'delaySeconds' | 'delayMinutes'>) {
  if (finite(q.delaySeconds)) return Math.max(0, q.delaySeconds)
  if (finite(q.delayMinutes)) return Math.max(0, q.delayMinutes * 60)
  return null
}

/** Which of the three labels a quote earns (the API's rule, RT-QUOTES §5). */
export function freshness(q: LabelQuote): Freshness {
  const d = delaySeconds(q)
  if (d == null) return 'delayed'
  if (q.class == null) return q.source !== 'yahoo' && d === 0 ? 'realtime' : 'delayed' // before classes: the live screener's rule
  if (q.class === 'realtime' && d < 60) return 'realtime'
  return (q.class === 'realtime' || q.class === 'near-realtime') && d <= 300 ? 'behind' : 'delayed'
}

/** "about N min behind": N = round(delay / 60), at least 1. */
export const behindMinutes = (q: Pick<LabelQuote, 'delaySeconds' | 'delayMinutes'>) => Math.max(1, Math.round((delaySeconds(q) ?? 60) / 60))

/** ISO time → "04:20:15 UTC" (seconds) or "04:20 UTC"; '' when it does not parse. */
export function utcTime(iso: string | null | undefined, seconds = false) {
  const d = new Date(iso ?? '')
  return Number.isNaN(d.getTime()) ? '' : `${d.toISOString().slice(11, seconds ? 19 : 16)} UTC`
}

/** The freshness as a short tag, for a group of prices: "real-time", "about 2 min behind", "delayed". */
export function freshnessTag(kind: Freshness, minutes = 1) {
  return kind === 'realtime' ? 'real-time' : kind === 'behind' ? `about ${minutes} min behind` : 'delayed'
}

/** The label from the quote's own fields: "real-time · 04:20:15 UTC", "about 2 min behind · 04:18 UTC", "delayed · last trade 04:00 UTC". */
export function computedLabel(q: LabelQuote) {
  const kind = freshness(q)
  const at = utcTime(q.lastTradeAt, kind === 'realtime')
  const head = freshnessTag(kind, behindMinutes(q))
  if (!at) return head
  return kind === 'delayed' ? `${head} · last trade ${at}` : `${head} · ${at}`
}

/** The label to show: the API's own, as sent, else the same rule from the quote's fields. */
export const priceLabel = (q: LabelQuote) => (typeof q.label === 'string' && q.label.trim() ? q.label.trim() : computedLabel(q))

/** Display names of the quote sources; an unknown one shows as sent. */
const SOURCE_NAMES: Record<string, string> = {
  yahoo: 'Yahoo Finance',
  naver: 'Naver Finance',
  tencent: 'Tencent',
  vietcap: 'Vietcap',
  sgx: 'SGX',
  mis: 'TWSE MIS',
  'yahoo-tw': 'Yahoo Taiwan',
}
export const sourceName = (source: string | null | undefined) => (source ? (SOURCE_NAMES[source] ?? source) : SOURCE_NAMES.yahoo)

/** Where the quote's time comes from, for a tooltip; '' when the quote does not say. */
export function basisText(basis: QuoteTimeBasis | null | undefined) {
  if (basis === 'trade') return "the time is the source's own last trade"
  if (basis === 'volume') return 'the source gives no trade time: the time is the first read that showed the current volume'
  if (basis === 'snapshot') return "the time is the source's snapshot minute, shared by every stock in it"
  return ''
}

/**
 * A market asset's quote (`quoteFeed`) as the labels read it. An API before the real-time sources sends none of the new
 * fields: the price is Yahoo's delayed last trade at `asOf`.
 */
export function feedQuote(q: QuoteFeed): LabelQuote & { source: string; readAt: string | null } {
  return {
    source: q.source || 'yahoo',
    class: q.class ?? 'delayed',
    lastTradeAt: q.lastTradeAt || q.asOf,
    delaySeconds: q.delaySeconds ?? null,
    timeBasis: q.timeBasis ?? null,
    label: q.label ?? null,
    readAt: q.readAt ?? null,
  }
}

/** A real-time source serves the listing and its session is open: the Trade page reads it every 5 s and moves its chart. */
export const realtimeOpen = (q: QuoteFeed | null | undefined) => !!q && q.marketState === 'open' && q.class === 'realtime'

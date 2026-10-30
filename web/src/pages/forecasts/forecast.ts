// Forecast desk helpers shared by the three pages: labels, draft rules (mirroring the API contract)
// and the small bits of result maths. Pure functions only; forecast.test.ts checks them.
import type {
  Asset,
  Credits,
  DeskMeasured,
  DeskMethods,
  DeskQ,
  DeskReference,
  DeskScenario,
  DeskUnit,
  DeskView,
  DeskViewBasis,
  Forecast,
  ForecastCategory,
  ForecastDraft,
  ForecastFinding,
  ForecastMaterial,
  ForecastResult,
  ForecastShape,
  ForecastSource,
  ForecastSummary,
  MarketOdds,
  OutcomeP,
  PriceRule,
  ResearchAngleKind,
  ResearchStepLog,
  ScenarioInput,
  Specialist,
  SpecialistMethod,
  SpecialistStats,
} from '../../lib/types'
import { fmtLevelPrice } from '../../lib/format'
import { DOCUMENTS, formatBytes } from '../agent/attachments'

export const MAX_IDEA = 400
export const MIN_QUESTION = 10
export const MAX_QUESTION = 280
export const MAX_OUTCOMES = 8
export const MAX_CONTEXT = 4000

export const SHAPES: { value: ForecastShape; label: string; blurb: string }[] = [
  { value: 'binary', label: 'Binary', blurb: 'Yes or no. One event, one probability.' },
  { value: 'ordered', label: 'Ordered', blurb: 'Numeric ranges, low to high. Shows skew and tails.' },
  { value: 'categorical', label: 'Categorical', blurb: 'One of several named outcomes.' },
]
export const shapeLabel = (s: string) => SHAPES.find((x) => x.value === s)?.label ?? s

export const CATEGORIES: { value: ForecastCategory; label: string }[] = [
  { value: 'markets', label: 'Markets' },
  { value: 'crypto', label: 'Crypto' },
  { value: 'macro', label: 'Macro' },
  { value: 'tech', label: 'Tech' },
  { value: 'politics', label: 'Politics' },
  { value: 'sports', label: 'Sports' },
  { value: 'other', label: 'Other' },
]
export const categoryLabel = (c: string) => CATEGORIES.find((x) => x.value === c)?.label ?? 'Other'

export const EXAMPLE_IDEAS: { category: ForecastCategory; shape: ForecastShape; idea: string }[] = [
  { category: 'crypto', shape: 'binary', idea: 'Will ETH outperform BTC over Q4 2026?' },
  { category: 'crypto', shape: 'ordered', idea: 'Where will BTC close on Dec 31 2026?' },
  { category: 'macro', shape: 'binary', idea: 'Will the Fed cut by 50bp+ by year end?' },
  { category: 'markets', shape: 'categorical', idea: 'Which company will be the largest by market cap at end of 2026?' },
  { category: 'macro', shape: 'ordered', idea: 'What will US CPI inflation (YoY) print for June 2027?' },
  { category: 'tech', shape: 'binary', idea: 'Will an open-weights model take #1 on a major LLM leaderboard by mid-2027?' },
  { category: 'politics', shape: 'categorical', idea: 'Which party will win the 2028 US presidential election?' },
  { category: 'sports', shape: 'categorical', idea: 'Who will win the 2026–27 UEFA Champions League?' },
]

/** The three specialists, in the order the desk presents them. */
export const SPECIALISTS: { role: Specialist['role']; name: string; glyph: string; focus: string }[] = [
  { role: 'base_rate', name: 'Base-rate statistician', glyph: 'B', focus: 'Reference classes and historical frequencies' },
  { role: 'quant', name: 'Market & quant analyst', glyph: 'Q', focus: 'Live market data, valuation and volatility models' },
  { role: 'skeptic', name: 'Narrative & macro skeptic', glyph: 'S', focus: 'Catalysts, regime risk and what consensus misses' },
]

const pad = (n: number) => String(n).padStart(2, '0')

/**
 * Earliest resolution date (YYYY-MM-DD): the later of tomorrow in local time and tomorrow in UTC,
 * so the server's "in the future" check passes from any time zone.
 */
export function minResolutionDate(now = new Date()) {
  const local = new Date(now.getFullYear(), now.getMonth(), now.getDate() + 1)
  const localISO = `${local.getFullYear()}-${pad(local.getMonth() + 1)}-${pad(local.getDate())}`
  const utcISO = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate() + 1)).toISOString().slice(0, 10)
  return localISO > utcISO ? localISO : utcISO
}

/** Linked dates track the provider's UTC end day; never move it to a local-day boundary. */
export function minDraftDate(d: ForecastDraft, now = new Date()) {
  return d.linkedMarket ? new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate() + 1)).toISOString().slice(0, 10) : minResolutionDate(now)
}

/** Calendar days from today (local) to a YYYY-MM-DD date; negative once it has passed, null if unparseable. */
export function daysUntil(date: string, now = new Date()) {
  const t = Date.parse(`${date.slice(0, 10)}T00:00:00Z`)
  if (Number.isNaN(t)) return null
  return Math.round((t - Date.UTC(now.getFullYear(), now.getMonth(), now.getDate())) / 86_400_000)
}

export function dueLabel(days: number | null) {
  if (days == null) return ''
  if (days === 0) return 'today'
  if (days > 0) return `in ${days} day${days === 1 ? '' : 's'}`
  return `${-days} day${days === -1 ? '' : 's'} ago`
}

/** "0:42", "3:05", "1:02:09". */
export function fmtElapsed(sec: number) {
  const h = Math.floor(sec / 3600)
  const m = Math.floor((sec % 3600) / 60)
  const s = sec % 60
  return h ? `${h}:${pad(m)}:${pad(s)}` : `${m}:${pad(s)}`
}

const SHAPE_VALUES: string[] = SHAPES.map((s) => s.value)
const CATEGORY_VALUES: string[] = CATEGORIES.map((c) => c.value)

/** Fills gaps in a draft from the API (or a blank one) so the form always has every field. */
export function normalizeDraft(d: Partial<ForecastDraft> = {}): ForecastDraft {
  const shape = SHAPE_VALUES.includes(d.shape ?? '') ? (d.shape as ForecastShape) : 'binary'
  const outcomes = Array.isArray(d.outcomes) ? d.outcomes.filter((o) => typeof o === 'string').slice(0, MAX_OUTCOMES) : []
  while (outcomes.length < 2) outcomes.push('')
  const date = typeof d.resolutionDate === 'string' && /^\d{4}-\d{2}-\d{2}/.test(d.resolutionDate) ? d.resolutionDate.slice(0, 10) : ''
  return {
    shape,
    question: typeof d.question === 'string' ? Array.from(d.question).slice(0, MAX_QUESTION).join('') : '',
    outcomes: shape === 'binary' ? ['Yes', 'No'] : outcomes,
    resolutionCriteria: typeof d.resolutionCriteria === 'string' ? d.resolutionCriteria : '',
    resolutionDate: date,
    category: CATEGORY_VALUES.includes(d.category ?? '') ? (d.category as ForecastCategory) : 'other',
    priceRule: shape === 'binary' && isRule(d.priceRule) ? d.priceRule : null,
    context: typeof d.context === 'string' ? d.context.slice(0, MAX_CONTEXT) : '',
    ...(d.linkedMarket ? { linkedMarket: d.linkedMarket } : {}),
  }
}

/** Notes and category do not change a contract; predicate edits require fresh confirmation. */
export function updateDraft(d: ForecastDraft, patch: Partial<ForecastDraft>): ForecastDraft {
  const next = { ...d, ...patch }
  if (d.linkedMarket && (next.question.trim() !== d.question.trim() || next.resolutionCriteria.trim() !== d.resolutionCriteria.trim() ||
    next.resolutionDate !== d.resolutionDate || next.shape !== d.shape || next.outcomes.map((o) => o.trim()).join('\0') !== d.outcomes.map((o) => o.trim()).join('\0') ||
    JSON.stringify(next.priceRule ?? null) !== JSON.stringify(d.priceRule ?? null))) next.linkedMarket = null
  return next
}

const isRule = (r: unknown): r is PriceRule => {
  const x = r as PriceRule | null | undefined
  return !!x && typeof x.symbol === 'string' && (x.op === 'above' || x.op === 'below') && (x.mode === 'on_date' || x.mode === 'by_date') &&
    Number.isFinite(x.threshold) && x.threshold > 0
}

/** "$130", "$61,100", "$0.215": a rule threshold as the question states it. */
export const fmtThreshold = (x: number) => `$${x.toLocaleString('en-US', { maximumFractionDigits: 8 })}`

/** "SOL/USDT daily close above $130 on Dec 31, 2026" (by_date: "by"). */
export const ruleText = (r: PriceRule, date: string) =>
  `${r.symbol}/USDT daily close ${r.op} ${fmtThreshold(r.threshold)} ${r.mode === 'by_date' ? 'by' : 'on'} ${fmtDay(date, true)}`

/** "Aug 12" (or "Aug 12, 2026") for the UTC calendar day of a date or timestamp. */
export function fmtDay(iso: string, year = false) {
  const d = new Date(`${iso.slice(0, 10)}T00:00:00Z`)
  if (Number.isNaN(d.getTime())) return iso
  return d.toLocaleString('en-US', { month: 'short', day: 'numeric', ...(year ? { year: 'numeric' } : {}), timeZone: 'UTC' })
}

/** The desk's top outcome on a list row: binary Yes at ≥ 50% else No, otherwise the headline (argmax). */
export function topOutcome(f: Pick<ForecastSummary, 'shape' | 'headline'>) {
  if (!f.headline) return null
  return f.shape === 'binary' ? (f.headline.p >= 0.5 ? 'Yes' : 'No') : f.headline.outcome
}

// Meaningful aliases from the configured names. Descriptive words such as
// "proxy" and "group", or a name's article, are not independent asset aliases.
const assetMentionAliases: Partial<Record<string, readonly string[]>> = {
  PAXG: ['gold'], SAND: ['sandbox'], GOOGL: ['google'], POL: ['matic'],
}
const mentionWords = (value: string) => value.toLowerCase().match(/[\p{L}\p{N}]+/gu)?.join(' ') ?? ''

/** The first universe asset a question names by whole symbol, full name or known alias. */
export function mentionedAsset(text: string, assets: Pick<Asset, 'symbol' | 'name'>[]) {
  const words = ` ${mentionWords(text)} `
  return assets.find(({ symbol, name }) => [symbol, name.replace(/\([^)]*\)/g, ''), ...(assetMentionAliases[symbol.toUpperCase()] ?? [])].some((label) => {
    const phrase = mentionWords(label)
    return !!phrase && words.includes(` ${phrase} `)
  }))?.symbol
}

/** The draft as sent to POST /api/forecasts: text trimmed, nothing else changed. */
export function cleanDraft(d: ForecastDraft): ForecastDraft {
  return {
    ...d,
    question: d.question.trim(),
    outcomes: d.outcomes.map((o) => o.trim()),
    resolutionCriteria: d.resolutionCriteria.trim(),
    context: (d.context ?? '').trim(),
  }
}

/**
 * Outcomes after switching shape. Binary is always Yes/No. A list shape gets back what it had before
 * (`saved`), else keeps the other list shape's outcomes, else starts with three blanks to fill in.
 */
export function reshapeOutcomes(from: ForecastShape, to: ForecastShape, outcomes: string[], saved?: string[]) {
  if (to === 'binary') return ['Yes', 'No']
  if (saved) return saved
  if (from !== 'binary') return outcomes
  return ['', '', '']
}

const distinct = (outcomes: string[]) => new Set(outcomes.map((o) => o.trim().toLowerCase()).filter(Boolean)).size

/** The contract's validation rules, checked before spending credits. First problem, or null. */
export function draftProblem(d: ForecastDraft, minDate = minDraftDate(d)) {
  const q = d.question.trim()
  if (Array.from(q).length < MIN_QUESTION) return `Write a question of at least ${MIN_QUESTION} characters.`
  if (Array.from(q).length > MAX_QUESTION) return `Keep the question under ${MAX_QUESTION} characters.`
  const criteriaLimit = d.linkedMarket ? 8000 : 2000
  if (Array.from(d.resolutionCriteria.trim()).length > criteriaLimit) return `Keep resolution criteria within ${criteriaLimit.toLocaleString('en-US')} characters.`
  const outs = d.outcomes.map((o) => o.trim())
  if (d.shape === 'binary' && outs.join('|') !== 'Yes|No') return 'Binary questions resolve Yes or No.'
  if (outs.length < 2) return 'Add at least two outcomes.'
  if (outs.length > MAX_OUTCOMES) return `Use at most ${MAX_OUTCOMES} outcomes.`
  if (outs.some((o) => !o)) return 'Fill in every outcome, or remove the empty ones.'
  if (distinct(outs) !== outs.length) return 'Each outcome must be different.'
  if (!d.resolutionDate) return 'Pick a resolution date.'
  if (d.resolutionDate < minDate) return 'The resolution date must be in the future.'
  return null
}

// ponytail: keyword heuristics, not NLP; they nudge the writer and never block a run.
const NUMBER = /\d/
const SOURCE =
  /according to|reported by|published|official|source|data from|\bper\b|https?:\/\/|\.(com|org|gov|io)\b|binance|coinbase|coingecko|coinmarketcap|bloomberg|reuters|federal reserve|fomc|\bfed\b|\bbls\b|\bsec\b|nasdaq|nyse|uefa|fifa|associated press|electoral|exchange|close price|closing price/i

export type QualityCheck = { label: string; hint: string; ok: boolean }

/** Client-side checklist for a well-posed question (FRONTEND-LAYOUT "quality checklist"). */
export function qualityChecks(d: ForecastDraft, minDate = minResolutionDate()): QualityCheck[] {
  const numbers = d.question + (d.shape === 'ordered' ? ' ' + d.outcomes.join(' ') : '')
  return [
    { label: 'Names a number or threshold', hint: 'e.g. "above $100k", "50bp+", "top 3"', ok: NUMBER.test(numbers) },
    { label: 'Has a deadline', hint: 'A resolution date the desk can check against', ok: !!d.resolutionDate && d.resolutionDate >= minDate },
    { label: 'Cites a resolution source', hint: 'Who settles it: an exchange close, an official release', ok: SOURCE.test(d.resolutionCriteria) },
    { label: 'Two or more distinct outcomes', hint: 'Every result should land in exactly one outcome', ok: distinct(d.outcomes) >= 2 },
  ]
}

const key = (s: string) => s.trim().toLowerCase()

/** Probabilities lined up with the question's outcomes (missing ones count as 0); unknown extras are appended. */
export function inOrder(probs: OutcomeP[] | null | undefined, outcomes: string[]): OutcomeP[] {
  const list = (Array.isArray(probs) ? probs : []).filter((x) => x && typeof x.outcome === 'string')
  const p = (x: OutcomeP) => (Number.isFinite(x.p) ? x.p : 0)
  const byKey = new Map(list.map((x) => [key(x.outcome), p(x)]))
  const known = new Set(outcomes.map(key))
  return [
    ...outcomes.map((o) => ({ outcome: o, p: byKey.get(key(o)) ?? 0 })),
    ...list.filter((x) => !known.has(key(x.outcome))).map((x) => ({ outcome: x.outcome, p: p(x) })),
  ]
}

/** Index of the most likely outcome (first on ties), -1 for an empty list. */
export const topIndex = (items: OutcomeP[]) => items.reduce((best, it, i) => (best < 0 || it.p > items[best].p ? i : best), -1)

/**
 * Integer percentages that sum to 100, by largest remainder (ties go to the earlier outcome), for printing a
 * distribution: [0.235, 0.765] → [24, 76]. Input that does not sum to 1 is scaled; nothing to show gives zeros.
 */
export function roundPcts(ps: number[]): number[] {
  const clean = ps.map((p) => (Number.isFinite(p) && p > 0 ? p : 0))
  const sum = clean.reduce((a, b) => a + b, 0)
  if (!(sum > 0)) return clean.map(() => 0)
  // Rounded to 1e-9 first so float noise (0.235 × 100 = 23.4999…) cannot decide a tie.
  const exact = clean.map((p) => Math.round((p / sum) * 1e11) / 1e9)
  const out = exact.map(Math.floor)
  const byRemainder = exact.map((_, i) => i).sort((a, b) => exact[b] - out[b] - (exact[a] - out[a])) // stable: ties keep order
  const left = 100 - out.reduce((a, b) => a + b, 0)
  for (let k = 0; k < left; k++) out[byRemainder[k % out.length]]++
  return out
}

/** A rounded percentage as printed: "24%", or "<1%" / ">99%" when rounding would hide a non-zero chance. */
export const fmtPct = (n: number, p: number) => (n === 0 && p > 0 ? '<1%' : n === 100 && p < 1 ? '>99%' : `${n}%`)

/** One probability as a whole percent, rounded the way its binary pair is (so 0.235 → 24, like [24, 76]). */
export const pct0 = (p: number) => roundPcts([p, 1 - p])[0]

/**
 * A chance as the desk states it, the API's chanceWords: "62%", "≈62%" in an extrapolated tail; one whose
 * whole percent rounds to 0 or 100 reads "<1%" or ">99%", never a certainty, and takes no "≈" (the bound says it).
 */
export function fmtChance(p: number, approx = false) {
  const n = pct0(p)
  return n === 0 ? '<1%' : n === 100 ? '>99%' : `${approx ? '≈' : ''}${n}%`
}

/** P(Yes) of a binary forecast; falls back to the first outcome. */
export const yesP = (items: OutcomeP[]) => (items.find((x) => key(x.outcome) === 'yes') ?? items[0])?.p ?? null

/**
 * For ordered outcomes: the run of neighbouring buckets around the most likely one that holds at
 * least `mass` of the probability, grown one bucket at a time toward the likelier neighbour.
 */
export function likelyRange(items: OutcomeP[], mass = 0.5) {
  let lo = topIndex(items)
  if (lo < 0) return null
  let hi = lo
  let p = items[lo].p
  while (p < mass && (lo > 0 || hi < items.length - 1)) {
    const left = lo > 0 ? items[lo - 1].p : -1
    const right = hi < items.length - 1 ? items[hi + 1].p : -1
    p += right > left ? items[++hi].p : items[--lo].p
  }
  return { from: lo, to: hi, p }
}

/** A plain http(s) URL, or null: research text comes from the open web, so nothing else becomes a link. */
export function httpUrl(s: unknown): URL | null {
  if (typeof s !== 'string' || !/^https?:\/\//i.test(s)) return null
  try {
    return new URL(s)
  } catch {
    return null
  }
}

/** The run's web research. Reports from before research existed (or when search was down) have none: empty lists. */
export function researchOf(r: Pick<ForecastResult, 'research' | 'sources'>) {
  const queries = (Array.isArray(r.research?.queries) ? r.research.queries : []).filter((q) => typeof q === 'string' && q.trim())
  const sources = (Array.isArray(r.sources) ? r.sources : []).filter((s) => s && typeof s.id === 'string')
  return { queries, sources, read: sources.filter((s) => s.read).length }
}

export type ResearchStatus = NonNullable<ForecastResult['research']['status']>
const RESEARCH_STATUSES: ResearchStatus[] = ['live', 'wire', 'carried', 'replay', 'none']

/** What research the panel saw. Older results have no status: a replay → replay, any source → live, else none. */
export function researchStatus(r: Pick<ForecastResult, 'research' | 'sources'>, replay: boolean): ResearchStatus {
  const s = r.research?.status
  if (s && RESEARCH_STATUSES.includes(s)) return s
  return replay ? 'replay' : Array.isArray(r.sources) && r.sources.length ? 'live' : 'none'
}

const count = (n: number, noun: string) => `${n} ${noun}${n === 1 ? '' : 's'}`

/** The report header's research label: "Brave Search · 8 sources · 3 read", "deep · 22 sources · 12 read", "stored news wire · 6 headlines", … */
export function researchLabel(status: ResearchStatus, sources: number, read: number, depth?: string): string {
  switch (status) {
    case 'live':
      return `${depth === 'deep' ? 'deep' : 'Brave Search'} · ${count(sources, 'source')}${read > 0 ? ` · ${read} read` : ''}`
    case 'wire':
      return `stored news wire · ${count(sources, 'headline')}`
    case 'carried':
      return `previous run's sources · ${sources}`
    case 'replay':
      return `dated headlines · ${sources}`
    case 'none':
      return 'none available'
  }
}

/** One banner per flag type, in first-seen order; distinct notes (ignoring case and whitespace) are its lines. */
export function groupFlags(flags: ForecastResult['flags']) {
  const norm = (s: string) => s.toLowerCase().replace(/\s+/g, ' ').trim()
  const groups: { type: ForecastResult['flags'][number]['type']; notes: string[] }[] = []
  for (const f of flags) {
    let g = groups.find((x) => x.type === f.type)
    if (!g) groups.push((g = { type: f.type, notes: [] }))
    if (norm(f.note) && !g.notes.some((n) => norm(n) === norm(f.note))) g.notes.push(f.note)
  }
  return groups
}

/**
 * The report's flag banners. When the shrink guard reset the desk, the reference card already says so, so the
 * server's own "Server check: …" note is dropped rather than shown twice; every other flag stays.
 */
export function reportFlags(r: Pick<ForecastResult, 'flags' | 'guard'>) {
  const flags = Array.isArray(r.flags) ? r.flags : []
  const guardNote = (f: ForecastResult['flags'][number]) => f.type === 'low_information' && /^\s*server check:/i.test(f.note ?? '')
  return groupFlags(r.guard === 'shrink' ? flags.filter((f) => !guardNote(f)) : flags)
}

// ── Deep research: the findings ledger, its groups and the live step copy ─────────────

export const STRENGTHS: ForecastFinding['strength'][] = ['strong', 'moderate', 'slight', 'minimal']

/**
 * The run's findings ledger, read defensively: malformed items are dropped, a sourceId that is not in the pack is
 * dropped (so it is never linked), and a finding with no source left goes. Older results have no ledger: [].
 * A material finding (`origin: "material"`) keeps only the ids of `materials` the desk read.
 */
export function findingsOf(r: Pick<ForecastResult, 'findings' | 'sources'>, materials: ForecastMaterial[] = []): ForecastFinding[] {
  const known = new Set((Array.isArray(r.sources) ? r.sources : []).filter((s) => s && typeof s.id === 'string').map((s) => s.id))
  const read = new Set(materials.filter((m) => m.status === 'read').map((m) => m.id))
  const out: ForecastFinding[] = []
  for (const f of Array.isArray(r.findings) ? r.findings : []) {
    if (!f || typeof f.id !== 'string' || typeof f.claim !== 'string' || !f.claim.trim()) continue
    const material = f.origin === 'material'
    const ids = material ? read : known
    const sourceIds = [...new Set((Array.isArray(f.sourceIds) ? f.sourceIds : []).filter((id) => typeof id === 'string' && ids.has(id)))]
    if (!sourceIds.length) continue
    out.push({
      id: f.id,
      claim: f.claim.trim(),
      sourceIds,
      asOf: typeof f.asOf === 'string' && f.asOf ? f.asOf : null,
      angle: typeof f.angle === 'string' ? f.angle : '',
      direction: typeof f.direction === 'string' ? f.direction.trim() : 'context',
      strength: STRENGTHS.includes(f.strength) ? f.strength : 'slight',
      ...(material ? { origin: 'material' as const, ref: typeof f.ref === 'string' ? clamp(f.ref, 60) : '' } : {}),
    })
  }
  return out
}

// ── Materials: the user's files, read once into a digest ─────────────

export const MAX_MATERIALS = 5 // files and images per forecast, reused ones included
export const CREDITS_PER_MATERIAL = 1

/** A run's credits: 5, plus 1 for each newly attached file (reused digests are free). */
export const runCost = (newFiles: number) => 5 + newFiles * CREDITS_PER_MATERIAL

/**
 * The 429 `ai_quota` line for a run with new files. The server refuses a user short of the whole charge, so one whose
 * credits still cover the run without some of its files is told what the files cost. Null when the files are not why
 * (none, credits unknown, or too few left for even a text-only run): the daily-limit line applies.
 */
export function materialsQuotaMessage(newFiles: number, credits: Pick<Credits, 'used' | 'limit'> | null) {
  if (newFiles <= 0 || !credits) return null
  const left = Math.max(0, credits.limit - credits.used)
  const cost = runCost(newFiles)
  if (left < runCost(0) || left >= cost) return null
  const over = cost - left
  return `This run needs ${cost} credits and you have ${left} left today. Remove ${over === 1 ? 'a file' : `${over} files`}, or run it after the 00:00 UTC reset.`
}

const clamp = (s: string, n: number) => Array.from(s.trim()).slice(0, n).join('')
const MATERIAL_STATUSES = ['pending', 'read', 'failed'] as const
const MATERIAL_ERRORS = ['unreadable', 'timeout', 'interrupted'] as const
const RELEVANCE = ['direct', 'background', 'unrelated'] as const
const isDate = (s: unknown): s is string => typeof s === 'string' && /^\d{4}-\d{2}(-\d{2})?$/.test(s)
const oneOf = <T extends string>(list: readonly T[], v: unknown): T | undefined => (list as readonly unknown[]).includes(v) ? (v as T) : undefined

/**
 * A forecast's materials, read defensively (model-written text from the user's files): malformed items are dropped,
 * strings clamped to the contract's bounds, at most five. Older forecasts have none: [].
 */
export function materialsOf(f: { materials?: unknown }): ForecastMaterial[] {
  const out: ForecastMaterial[] = []
  for (const m of Array.isArray(f.materials) ? (f.materials as Partial<ForecastMaterial>[]) : []) {
    if (out.length >= MAX_MATERIALS) break
    if (!m || typeof m.id !== 'string' || !/^D\d{1,2}$/.test(m.id) || typeof m.name !== 'string' || out.some((x) => x.id === m.id)) continue
    const status = oneOf(MATERIAL_STATUSES, m.status) ?? 'failed'
    const read = status === 'read'
    const error = status === 'failed' ? (oneOf(MATERIAL_ERRORS, m.error) ?? 'unreadable') : undefined
    const relevance = read ? oneOf(RELEVANCE, m.relevance) : undefined
    out.push({
      id: m.id,
      kind: m.kind === 'image' ? 'image' : 'document',
      mediaType: typeof m.mediaType === 'string' ? m.mediaType : '',
      name: clamp(m.name, 120) || (m.kind === 'image' ? 'image' : 'document'),
      size: Number.isFinite(m.size) && m.size! > 0 ? Math.floor(m.size!) : 0,
      ...(Number.isInteger(m.pages) && m.pages! > 0 ? { pages: m.pages } : {}),
      status,
      ...(error ? { error } : {}),
      summary: read && typeof m.summary === 'string' ? clamp(m.summary, 600) : '',
      ...(relevance ? { relevance } : {}),
      asOf: isDate(m.asOf) ? m.asOf : null,
      findings: read && Array.isArray(m.findings)
        ? m.findings
            .filter((x) => x && typeof x.claim === 'string' && x.claim.trim())
            .slice(0, 6)
            .map((x) => ({
              claim: clamp(x.claim, 300),
              ref: typeof x.ref === 'string' ? clamp(x.ref, 60) : '',
              asOf: isDate(x.asOf) ? x.asOf : null,
              direction: typeof x.direction === 'string' ? x.direction.trim() : 'context',
              strength: STRENGTHS.includes(x.strength) ? x.strength : 'slight',
            }))
        : [],
      excerpt: read && typeof m.excerpt === 'string' ? clamp(m.excerpt, 2000) : '',
      readAt: typeof m.readAt === 'string' && !Number.isNaN(Date.parse(m.readAt)) ? m.readAt : null,
      ...(typeof m.reusedFrom === 'string' && m.reusedFrom ? { reusedFrom: m.reusedFrom } : {}),
    })
  }
  return out
}

/** "PDF · 1.2 MB · 12 pages", "Image · 340 kB": a material's type, size and (when counted) pages. */
export function materialLine(m: Pick<ForecastMaterial, 'kind' | 'mediaType' | 'size' | 'pages'>) {
  const type = m.kind === 'image' ? 'Image' : (DOCUMENTS[m.mediaType as keyof typeof DOCUMENTS]?.label ?? 'File')
  return [type, m.size > 0 && formatBytes(m.size), m.pages && count(m.pages, 'page')].filter(Boolean).join(' · ')
}

/**
 * The server's update rule: completed, unresolved, resolving after today (UTC), not a replay, and the newest run of its
 * lineage.
 */
export function isUpdatable(f: Pick<Forecast, 'id' | 'status' | 'result' | 'resolution' | 'replay' | 'history' | 'resolutionDate'>, today = new Date().toISOString().slice(0, 10)) {
  const history = f.history ?? []
  const newest = history[history.length - 1]
  const superseded = f.status === 'completed' && !!newest && newest.id !== f.id
  return f.status === 'completed' && !!f.result && !f.resolution && !f.replay && !superseded && f.resolutionDate > today
}

/** The in-page anchor of a material's row on the report. */
export const materialHref = (id: string) => `#material-${id}`

export type DirectionGroup = { key: string; label: string; tone: 'up' | 'down' | 'neutral' }

/** The direction buckets a question's findings fall into, in display order; Context always last. */
export function directionGroups(shape: ForecastShape, outcomes: string[]): DirectionGroup[] {
  const context: DirectionGroup = { key: 'context', label: 'Context', tone: 'neutral' }
  if (shape === 'binary')
    return [{ key: 'yes', label: 'Points to Yes', tone: 'up' }, { key: 'no', label: 'Points to No', tone: 'down' }, context]
  if (shape === 'ordered')
    return [
      { key: 'higher', label: 'Upward', tone: 'up' },
      { key: 'lower', label: 'Downward', tone: 'down' },
      { key: 'center', label: 'Center', tone: 'neutral' },
      { key: 'tail', label: 'Tail', tone: 'neutral' },
      context,
    ]
  const seen = new Set<string>(['context'])
  const groups: DirectionGroup[] = []
  for (const o of outcomes) {
    if (!o.trim() || seen.has(key(o))) continue
    seen.add(key(o))
    groups.push({ key: o, label: `Favours ${o}`, tone: 'neutral' })
  }
  return [...groups, context]
}

/** The group one direction value belongs to ("higher" → Upward); unknown values read as Context. */
export function directionGroupOf(direction: string, shape: ForecastShape, outcomes: string[]): DirectionGroup {
  const groups = directionGroups(shape, outcomes)
  return groups.find((g) => key(g.key) === key(direction)) ?? groups[groups.length - 1]
}

/**
 * Findings bucketed by direction, groups in `directionGroups` order with empty ones left out. An unknown direction
 * goes to Context; within a group, strongest first, then ledger order.
 */
export function groupFindings(findings: ForecastFinding[], shape: ForecastShape, outcomes: string[]) {
  const groups = directionGroups(shape, outcomes)
  const items = new Map<string, ForecastFinding[]>(groups.map((g) => [g.key, []]))
  for (const f of findings) items.get(directionGroupOf(f.direction, shape, outcomes).key)!.push(f)
  const rank = (f: ForecastFinding) => STRENGTHS.indexOf(f.strength)
  return groups
    .map((g) => ({ ...g, items: items.get(g.key)!.slice().sort((a, b) => rank(a) - rank(b)) })) // stable: ties keep ledger order
    .filter((g) => g.items.length > 0)
}

/** A finding's asOf: "Oct 5, 2026" for a day, "Jun 2026" for a month, the raw text otherwise. */
export function fmtAsOf(asOf: string) {
  if (/^\d{4}-\d{2}-\d{2}$/.test(asOf)) return fmtDay(asOf, true)
  const d = /^\d{4}-\d{2}$/.test(asOf) ? new Date(`${asOf}-01T00:00:00Z`) : null
  return d && !Number.isNaN(d.getTime()) ? d.toLocaleString('en-US', { month: 'short', year: 'numeric', timeZone: 'UTC' }) : asOf
}

export const ANGLE_KIND_LABEL: Record<ResearchAngleKind, string> = {
  comparables: 'Comparables',
  primary: 'Primary sources',
  catalysts: 'Catalysts',
  resolution: 'Resolution',
  drivers: 'Drivers',
  sentiment: 'Local market',
  other: 'Other',
}
export const angleKindLabel = (kind: string) => ANGLE_KIND_LABEL[kind as ResearchAngleKind] ?? 'Other'

/** The research card's label for a deep run: "Deep research · 2 rounds · 18 searches · 22 sources · 12 read" (zeros left out). */
export function deepResearchLine(r: Pick<ForecastResult, 'research' | 'sources'>) {
  const { sources, read } = researchOf(r)
  const n = (x: unknown) => (typeof x === 'number' && Number.isFinite(x) && x > 0 ? x : 0)
  const rounds = n(r.research?.rounds)
  const searches = n(r.research?.searches)
  return [
    'Deep research',
    rounds && count(rounds, 'round'),
    searches && searchCount(searches),
    sources.length && count(sources.length, 'source'),
    read && `${read} read`,
  ]
    .filter(Boolean)
    .join(' · ')
}

const searchCount = (n: number) => `${n} search${n === 1 ? '' : 'es'}`

const PHASE_NAME: Record<ResearchStepLog['phase'], string> = {
  plan: 'Planning',
  search: 'Search',
  screen: 'Screening',
  read: 'Reading',
  extract: 'Extraction',
  gap: 'Gap review',
}

/** One line of the live research step list ("Read 10 of 14 pages", "Screening results…"). */
export function stepText(s: ResearchStepLog): string {
  if (s.status === 'failed') return `${PHASE_NAME[s.phase] ?? 'Step'} failed · kept what it had`
  if (s.status === 'skipped') return `${PHASE_NAME[s.phase] ?? 'Step'} skipped (time or search budget)`
  const running = s.status === 'running'
  switch (s.phase) {
    case 'plan':
      return running ? 'Planning the research…' : `Planned ${count(s.count, 'angle')}`
    case 'search':
      return running ? `Searching · round ${s.round}…` : `Round ${s.round} · ${searchCount(s.of)} · ${count(s.count, 'result')}`
    case 'screen':
      return running ? 'Screening results…' : `${s.count} of ${s.of} relevant`
    case 'read':
      return running ? 'Reading pages…' : `Read ${s.count} of ${count(s.of, 'page')}`
    case 'extract':
      return running ? 'Extracting findings…' : `${s.count} new finding${s.count === 1 ? '' : 's'}`
    case 'gap':
      return running ? 'Looking for gaps…' : s.count ? `${s.count} follow-up quer${s.count === 1 ? 'y' : 'ies'}` : 'No gaps left'
    default:
      return running ? 'Working…' : 'Done'
  }
}

/**
 * The files in a context: each `File: <name>` header (at the start or after a blank line) opens a block that runs to
 * the next header or the end. `start` includes the blank line before it; `chars` counts the file's text.
 * ponytail: parsed from the text, so notes that start a paragraph with "File: …" read as a file too.
 */
export function contextFiles(text: string) {
  const heads = [...text.matchAll(/(?:^|\n\n)File: ([^\n]+)\n/g)]
  return heads.map((m, i) => {
    const start = m.index
    const end = heads[i + 1]?.index ?? text.length
    return { name: m[1], start, end, chars: end - start - m[0].length }
  })
}

/** "Context: your notes + 1 file" — what the desk will see as S0 ("" when nothing). */
export function contextSummary(text: string) {
  const files = contextFiles(text)
  const notes = (files.length ? text.slice(0, files[0].start) : text).trim() !== ''
  return [notes && 'your notes', files.length && count(files.length, 'file')].filter(Boolean).join(' + ')
}

/** "1.2k chars", "512 chars". */
export const fmtChars = (n: number) => (n >= 1000 ? `${+(n / 1000).toFixed(1)}k chars` : count(n, 'char'))

/**
 * Where an evidence item links: its URL, labelled with the matching research source's title when there is one; or,
 * for a claim from the user's material (`materialId`), that material's row on this page (`internal`).
 */
export function evidenceLink(e: { url?: string | null; source: string; materialId?: string }, sources: ForecastSource[], materials: ForecastMaterial[] = []) {
  const material = typeof e.materialId === 'string' ? materials.find((m) => m.id === e.materialId) : undefined
  if (material) return { href: materialHref(material.id), id: material.id, label: material.name, internal: true }
  const url = httpUrl(e.url)
  if (!url) return null
  const src = sources.find((s) => s.url === e.url)
  return { href: url.href, id: src?.id ?? null, label: src?.title || e.source || url.hostname.replace(/^www\./, ''), internal: false }
}

// ── Desk vs market and the track record ─────────────────────────────────────

/** "+5 pts", "−4.9 pts", "±0 pts": an edge in probability points. */
export const fmtPts = (pp: number) => `${pp > 0 ? '+' : pp < 0 ? '−' : '±'}${Math.abs(pp)} pts`

/** "+$0.05", "−$0.05": EV per $1 contract. */
export const fmtEv = (x: number) => `${x > 0 ? '+' : x < 0 ? '−' : '±'}$${Math.abs(x).toFixed(2)}`

export const SIGNAL_LABEL = { yes: 'Edge on Yes', no: 'Edge on No', wait: 'Wait' } as const // vs the market, not the desk's own lean

/** Under $1,000 traded (or unknown) is too thin to read an edge from. Older results have no `thin`. */
export const isThin = (m: Pick<MarketOdds, 'thin' | 'volumeUsd'>) => m.thin ?? (m.volumeUsd == null || m.volumeUsd < 1000)

/**
 * The panel's header: "Specialists · 3"; when the server found the members duplicated one view (methods.agreement),
 * "3 specialists · counted as 1 view". It claims no independence: only the methods' agreement can say that.
 */
export const specialistsHeader = (n: number, methods: Pick<DeskMethods, 'agreement'> | null) =>
  methods?.agreement === 'duplicate' ? `${n} specialists · counted as 1 view` : `Specialists · ${n}`

/** Max − min of the specialists' P(top outcome) in points (Yes for binary), and how much they disagree. */
export function specialistSpread(specialists: Specialist[], top: string) {
  const ps = specialists.map((s) => inOrder(s.probabilities, [top])[0].p)
  if (ps.length < 2) return null
  const pts = Math.round((Math.max(...ps) - Math.min(...ps)) * 100)
  return { pts, level: pts < 10 ? 'low' : pts < 25 ? 'moderate' : 'high' }
}

// ── Desk view: the numbers a numeric report leads with ─────────────
// The API derives these from the final buckets (contract "Desk view and methods"); the page reads them defensively and
// ports two pieces of the maths: the bucket parser (bucketBoundsTs, for the % chips on a draft) and the bucket
// distribution (quantileOf, for a specialist's tool anchor and the pool's median, which the API does not summarize).

export const METHOD_LABEL: Record<SpecialistMethod, string> = {
  history: 'Historical simulation',
  fundamentals: 'Valuation scenarios',
  volatility: 'Volatility model',
  regime: 'Regime & tail risk',
  judgment: 'Judgment (no model)',
  fallback: 'Fallback (data unavailable)',
}
const METHODS = Object.keys(METHOD_LABEL)

/** The method the server recorded for a specialist; null on binary and categorical runs, older results and unknown values. */
export const methodOf = (s: { method?: unknown }): SpecialistMethod | null =>
  typeof s.method === 'string' && METHODS.includes(s.method) ? (s.method as SpecialistMethod) : null

/** judgment and fallback ran no model of their own. */
export const modelled = (m: SpecialistMethod) => m !== 'judgment' && m !== 'fallback'

const num = (x: unknown): x is number => typeof x === 'number' && Number.isFinite(x)
const numOrNull = (x: unknown) => (num(x) ? x : null)
const str = (x: unknown) => (typeof x === 'string' ? x : '')
const isProb = (q: DeskQ | null): q is DeskQ => !!q && q.value >= 0 && q.value <= 1

/** A quantile as the API sends it ({ value, approx? }), or null when the value is not a finite number. */
export function qOf(x: unknown): DeskQ | null {
  const q = x as Partial<DeskQ> | null | undefined
  if (!q || typeof q !== 'object' || !num(q.value)) return null
  return q.approx === true ? { value: q.value, approx: true } : { value: q.value }
}

const unitIn = (x: unknown): DeskUnit => {
  const u = x as Partial<DeskUnit> | null | undefined
  return { prefix: str(u?.prefix), suffix: str(u?.suffix) }
}

const BASES: DeskViewBasis['kind'][] = ['scenarios', 'capped_drift', 'none']

/** The server's line for a view no evidence anchors (its noBasis), for a view that came without one. */
export const NO_BASIS_LABEL = "Read from the desk's ranges: no cited scenarios or measured history set its centre, so it states no median."

/** What a view's centre rests on; a missing or unknown kind is none, so the page never states a centre it was not given. */
function basisIn(x: unknown): DeskViewBasis {
  const b = x as Partial<DeskViewBasis> | null | undefined
  const kind = BASES.includes(b?.kind as DeskViewBasis['kind']) ? (b!.kind as DeskViewBasis['kind']) : 'none'
  const label = str(b?.label).trim()
  return { kind, label: kind === 'none' && (!label || b?.kind !== 'none') ? NO_BASIS_LABEL : label }
}

/** A basis the desk may state a median, a change and P(at or above today) on. */
export const anchored = (v: { basis: DeskViewBasis } | null | undefined) => v?.basis.kind === 'scenarios' || v?.basis.kind === 'capped_drift'

function referenceIn(x: unknown): DeskReference | null {
  const r = x as Partial<DeskReference> | null | undefined
  if (!r || typeof r !== 'object' || !num(r.value) || r.value <= 0) return null
  return { value: r.value, label: str(r.label), asOf: str(r.asOf), source: str(r.source) }
}

/**
 * A result's desk view, or null (render today's report): absent, an unknown scale or headline, a quantile that is not a
 * number, quantiles out of order, or a log-scale level at or below zero. Edges that are malformed or out of order are
 * dropped as a whole (the bars lose their % hints, nothing else). Without an anchoring basis the view keeps no
 * P(above) or change, whatever came with it.
 */
export function viewOf(r: { view?: unknown } | null | undefined): DeskView | null {
  const v = r?.view as Partial<DeskView> | null | undefined
  if (!v || typeof v !== 'object' || (v.scale !== 'log' && v.scale !== 'linear') || (v.headline !== 'view' && v.headline !== 'bucket')) return null
  const qs = [v.p10, v.p25, v.median, v.p75, v.p90].map(qOf)
  if (!qs.every((q): q is DeskQ => !!q)) return null
  if (qs.some((q, i) => i > 0 && q.value < qs[i - 1].value) || (v.scale === 'log' && qs[0].value <= 0)) return null
  const [p10, p25, median, p75, p90] = qs
  const raw = Array.isArray(v.edges) ? v.edges : []
  const edges = raw.every((e) => e && num(e.value)) ? raw.map((e) => ({ value: e.value, vsRefPct: numOrNull(e.vsRefPct) })) : []
  const basis = basisIn(v.basis)
  const centred = anchored({ basis })
  const pAbove = qOf(v.pAbove)
  return {
    scale: v.scale,
    unit: unitIn(v.unit),
    horizonDays: num(v.horizonDays) ? v.horizonDays : 0,
    reference: referenceIn(v.reference),
    median,
    p10,
    p25,
    p75,
    p90,
    pAbove: centred && isProb(pAbove) ? pAbove : null,
    changePct: centred ? numOrNull(v.changePct) : null,
    change: centred ? numOrNull(v.change) : null,
    annualPct: centred ? numOrNull(v.annualPct) : null,
    flat: v.flat === true,
    headline: v.headline,
    edges: edges.every((e, i) => i === 0 || e.value > edges[i - 1].value) ? edges : [],
    lead: str(v.lead).trim(),
    basis,
    ...(v.derived === true ? { derived: true } : {}),
  }
}

/**
 * A list row's view in brief, or null. `anchored`: the view states its median (its basis is not none); a missing or
 * unknown basis is none.
 */
export function summaryViewOf(f: Pick<ForecastSummary, 'view'>) {
  const v = f.view
  const [median, p10, p90] = [v?.median, v?.p10, v?.p90].map(qOf)
  if (!v || !median || !p10 || !p90 || p10.value > median.value || median.value > p90.value) return null
  const centred = v.basis === 'scenarios' || v.basis === 'capped_drift'
  const pAbove = qOf(v.pAbove)
  return {
    median,
    p10,
    p90,
    pAbove: centred && isProb(pAbove) ? pAbove : null,
    unit: unitIn(v.unit),
    flat: v.flat === true,
    headline: v.headline === 'view' ? ('view' as const) : ('bucket' as const),
    anchored: centred,
  }
}

/** A full desk view in brief (ForecastSummary.view), as a list row and the chat cards carry it. */
export const briefOf = (v: DeskView): NonNullable<ForecastSummary['view']> => ({
  median: v.median,
  p10: v.p10,
  p90: v.p90,
  pAbove: v.pAbove,
  unit: v.unit,
  flat: v.flat,
  headline: v.headline,
  basis: v.basis.kind,
})

/**
 * How a numeric report is stated wherever it is quoted (the list row, the chat's forecast and trade cards, the draft
 * card; the API's ViewHeadline): when its view leads, its median with the chance of ending at or above today (or its
 * 80% range when no reference was measured), or, flat with no basis for a centre, its 80% range. Null: the report
 * states its top outcome, as binary, categorical, top-range (bucket) and older rows always have.
 */
export function viewReading(f: Pick<ForecastSummary, 'view'>) {
  const v = summaryViewOf(f)
  if (!v || v.headline !== 'view' || !(v.anchored || v.flat)) return null
  const range = `${fmtQ(v.p10, v.unit)}–${fmtQ(v.p90, v.unit)}`
  if (!v.anchored) return { view: v, value: range, note: '80% range', text: `80% range ${range}`, up: null }
  const up = v.pAbove ? Math.max(0, Math.min(1, v.pAbove.value)) : null
  const median = fmtQ(v.median, v.unit)
  const note = up != null ? `${fmtChance(up)} ≥ today` : `80% ${range}`
  return { view: v, value: `Median ${median}`, note, text: `median ${median} · ${note}`, up }
}

const INPUT_KINDS: ScenarioInput['kind'][] = ['source', 'model_knowledge', 'assumption']

/** Scenario inputs read defensively: a kind the page does not know reads as an assumption. */
export function inputsOf(x: unknown): ScenarioInput[] {
  return (Array.isArray(x) ? (x as Partial<ScenarioInput>[]) : []).flatMap((i): ScenarioInput[] =>
    i && typeof i.field === 'string' && num(i.value)
      ? [{
          field: i.field,
          value: i.value,
          ...(Number.isInteger(i.year) ? { year: i.year } : {}),
          basis: str(i.basis),
          kind: INPUT_KINDS.includes(i.kind as ScenarioInput['kind']) ? (i.kind as ScenarioInput['kind']) : 'assumption',
          verified: i.verified === true,
        }]
      : [],
  )
}

const SCENARIO_KINDS = ['tail', 'bear', 'base', 'bull']

/** The result's scenarios, low → high, read defensively; fewer than two usable ones is none. */
export function scenariosOf(r: { scenarios?: unknown }): DeskScenario[] {
  const list = (Array.isArray(r.scenarios) ? (r.scenarios as Partial<DeskScenario>[]) : []).flatMap((s): DeskScenario[] => {
    if (!s || !num(s.level) || !num(s.p) || s.p < 0 || s.p > 1 || !str(s.name).trim()) return []
    return [{
      kind: SCENARIO_KINDS.includes(str(s.kind)) ? str(s.kind) : 'base',
      name: clamp(str(s.name), 60),
      p: s.p,
      modelP: num(s.modelP) && s.modelP >= 0 && s.modelP <= 1 ? s.modelP : null,
      lo: numOrNull(s.lo),
      hi: numOrNull(s.hi),
      level: s.level,
      vsRefPct: numOrNull(s.vsRefPct),
      annualPct: numOrNull(s.annualPct),
      inputs: inputsOf(s.inputs),
      thesis: clamp(str(s.thesis), 240),
      mustBeTrue: (Array.isArray(s.mustBeTrue) ? s.mustBeTrue : []).filter((m) => typeof m === 'string' && m.trim()).slice(0, 3).map((m) => clamp(m, 160)),
      sourceIds: [...new Set((Array.isArray(s.sourceIds) ? s.sourceIds : []).filter((id) => typeof id === 'string' && /^[SFH]\d{1,3}$/.test(id)))],
    }]
  })
  return list.length < 2 ? [] : list.sort((a, b) => a.level - b.level)
}

const AGREEMENTS: DeskMethods['agreement'][] = ['independent_agree', 'diverse', 'duplicate']

/** How the methods were weighed, or null (older results, event questions, a pool that is not a distribution). */
export function methodsOf(r: { methods?: unknown }): DeskMethods | null {
  const m = r.methods as Partial<DeskMethods> | null | undefined
  if (!m || typeof m !== 'object' || !AGREEMENTS.includes(m.agreement as DeskMethods['agreement'])) return null
  const pool = (Array.isArray(m.pool) ? m.pool : []).filter((x) => x && typeof x.outcome === 'string' && num(x.p))
  if (!pool.length) return null
  return {
    plan: str(m.plan),
    weights: (Array.isArray(m.weights) ? m.weights : [])
      .filter((w) => w && typeof w.role === 'string' && num(w.weight) && w.weight >= 0)
      .map((w) => ({ role: w.role, method: str(w.method), weight: w.weight, why: typeof w.why === 'string' && w.why.trim() ? clamp(w.why, 200) : null })),
    weightsFrom: m.weightsFrom === 'desk' ? 'desk' : 'prior',
    pool,
    agreement: m.agreement!,
    guard: m.guard === 'sharpness' || m.guard === 'width' || m.guard === 'volatility' ? m.guard : null,
    note: clamp(str(m.note), 600),
  }
}

const numbers = <K extends string>(o: unknown, keys: K[]) =>
  Object.fromEntries(keys.map((k) => [k, numOrNull((o as Record<string, unknown> | null | undefined)?.[k])])) as Record<K, number | null>

/** The subject's measured history (H1), or null when the run had none or it is malformed. */
export function measuredOf(r: { measured?: unknown }): DeskMeasured | null {
  const m = r.measured as Partial<DeskMeasured> | null | undefined
  if (!m || typeof m !== 'object' || !str(m.source).trim() || !isDate(m.from) || !isDate(m.to)) return null
  const dd = m.maxDrawdown
  const w = m.window
  return {
    symbol: str(m.symbol),
    name: str(m.name),
    source: m.source!.trim(),
    currency: str(m.currency),
    from: m.from,
    to: m.to,
    sessions: num(m.sessions) ? m.sessions : 0,
    volAnnPct: numbers(m.volAnnPct, ['y1', 'y5', 'all']),
    cagrPct: numbers(m.cagrPct, ['y5', 'y10', 'all']),
    maxDrawdown: dd && num(dd.pct) ? { pct: dd.pct, peak: str(dd.peak), trough: str(dd.trough), recovered: isDate(dd.recovered) ? dd.recovered : null } : null,
    window: w && [w.days, w.n, w.independent, w.p10, w.p50, w.p90, w.pUp].every(num)
      ? { days: w.days, n: w.n, independent: w.independent, p10: w.p10, p50: w.p50, p90: w.p90, pUp: w.pUp }
      : null,
    url: httpUrl(m.url)?.href ?? null,
  }
}

/** A specialist's numbers, or null when it has none or they are not numbers. */
export function statsOf(s: { stats?: unknown }): SpecialistStats | null {
  const st = s.stats as Partial<SpecialistStats> | null | undefined
  const [median, p10, p90] = [st?.median, st?.p10, st?.p90].map(qOf)
  if (!st || !median || !p10 || !p90) return null
  const pAbove = qOf(st.pAbove)
  return { median, p10, p90, pAbove: isProb(pAbove) ? pAbove : null, statedMedian: numOrNull(st.statedMedian) }
}

/**
 * The bucket distribution of the API's desk view: uniform within each closed range in t (ln x on a log scale, x on a
 * linear one) and exponential open tails whose density meets the neighbouring range's (rate clamped to ¼–2× that
 * range's width), so no mass falls below zero and no volatility is assumed. `edges` are the K − 1 finite edges, `probs`
 * the K range probabilities (renormalized). Null when they do not fit together.
 */
function bucketModel(edges: number[], probs: number[], scale: DeskView['scale']) {
  const K = probs.length
  if (K < 3 || edges.length !== K - 1 || (scale === 'log' && !edges.every((e) => e > 0)) || !probs.every((p) => num(p) && p >= 0)) return null
  const u = edges.map((e) => (scale === 'log' ? Math.log(e) : e))
  if (!u.every((x, i) => Number.isFinite(x) && (i === 0 || x > u[i - 1]))) return null
  // Every range keeps a sliver of mass (1e-9, as the API's), so a tail next to an empty range is still defined.
  const floored = probs.map((p) => Math.max(p, 1e-9))
  const sum = floored.reduce((a, b) => a + b, 0)
  const p = floored.map((x) => x / sum)
  // λ = clamp(p_tail · w / p_neighbour, ¼w, 2w), w the neighbouring range's width.
  const rate = (tail: number, next: number, w: number) => Math.min(2 * w, Math.max(0.25 * w, (tail * w) / next))
  const lo = rate(p[0], p[1], u[1] - u[0])
  const hi = rate(p[K - 1], p[K - 2], u[K - 2] - u[K - 3])
  return { u, p, K, lo, hi, x: (t: number) => (scale === 'log' ? Math.exp(t) : t), t: (x: number) => (scale === 'log' ? Math.log(x) : x) }
}

/** P(X < x) under the bucket distribution, or null when the edges and probabilities do not fit. */
export function cdfOf(edges: number[], probs: number[], scale: DeskView['scale'], x: number): number | null {
  const m = bucketModel(edges, probs, scale)
  if (!m || !num(x)) return null
  if (scale === 'log' && x <= 0) return 0
  const { u, p, K } = m
  const t = m.t(x)
  if (t < u[0]) return p[0] * Math.exp((t - u[0]) / m.lo)
  if (t >= u[K - 2]) return 1 - p[K - 1] * Math.exp(-(t - u[K - 2]) / m.hi)
  let c = p[0]
  for (let i = 1; i < K - 1; i++) {
    if (t < u[i]) return c + (p[i] * (t - u[i - 1])) / (u[i] - u[i - 1])
    c += p[i]
  }
  return c
}

/** The q-quantile (0 < q < 1) by piecewise inversion; approx when it falls in an open-ended range. */
export function quantileOf(edges: number[], probs: number[], scale: DeskView['scale'], q: number): DeskQ | null {
  const m = bucketModel(edges, probs, scale)
  if (!m || !(q > 0 && q < 1)) return null
  const { u, p, K } = m
  if (q < p[0]) return { value: m.x(u[0] + m.lo * Math.log(q / p[0])), approx: true }
  if (q > 1 - p[K - 1]) return { value: m.x(u[K - 2] - m.hi * Math.log((1 - q) / p[K - 1])), approx: true }
  let c = p[0]
  for (let i = 1; i < K - 1; i++) {
    if (q <= c + p[i]) return { value: m.x(u[i - 1] + ((q - c) / p[i]) * (u[i] - u[i - 1])) }
    c += p[i]
  }
  return { value: m.x(u[K - 2]) }
}

// The API's bucket parser (internal/forecast ladder.go), ported: an amount with an optional scale word, which multiplies
// by its value (k, m/mn/million, b/bn/billion, t/tn/trillion); any other word is ×1, so "300 to 450" stays 300 and 450.
const AMOUNT = /\$?\s*(\d[\d,]*(?:\.\d+)?)\s*([a-zA-Z]+)?/g
const SCALE_WORD: Record<string, number> = { k: 1e3, m: 1e6, mn: 1e6, million: 1e6, b: 1e9, bn: 1e9, billion: 1e9, t: 1e12, tn: 1e12, trillion: 1e12 }

/** A minus right before a number ("-1%", "< −0.5%", "-$3"), never a range's dash ("2.5-3", "2.5 - 3"): the API's negative. */
function negative(before: string) {
  const b = before.endsWith('$') ? before.slice(0, -1) : before
  const last = b.at(-1)
  if (last !== '-' && last !== '−') return false
  const prev = b.at(-2) ?? ''
  return b.length === 1 || /\s/.test(prev) || '<>≥≤=('.includes(prev)
}

/** Every number in the lower-cased label, scaled by its word and signed, the API's amounts. */
const amountsIn = (l: string) =>
  [...l.matchAll(AMOUNT)].map((m) => {
    const x = Number(m[1].replaceAll(',', '')) * (SCALE_WORD[m[2] ?? ''] ?? 1)
    return negative(l.slice(0, m.index + m[0].indexOf(m[1]))) ? -x : x
  })

/**
 * Ordered outcomes ("< $250", "$250 – $360", …, "≥ $1,500") as [lo, hi) bounds, the first open below and the last open
 * above; null when a label does not parse, there are fewer than 3, or they are not contiguous (each hi within 0.01% of
 * the next lo) and increasing. The same rule as the API's bucketBounds.
 */
export function bucketBoundsTs(outcomes: string[]): [number, number][] | null {
  const n = outcomes.length
  if (n < 3) return null
  const out: [number, number][] = []
  for (const [i, o] of outcomes.entries()) {
    const l = o.trim().toLowerCase()
    const xs = amountsIn(l)
    const prefix = (...ps: string[]) => ps.some((p) => l.startsWith(p))
    if (i === 0 && xs.length === 1 && prefix('<', 'under', 'below')) out.push([-Infinity, xs[0]])
    else if (i === n - 1 && xs.length === 1 && (prefix('≥', '>', 'above', 'over') || l.endsWith('+'))) out.push([xs[0], Infinity])
    else if (i > 0 && i < n - 1 && xs.length === 2 && xs[0] < xs[1]) out.push([xs[0], xs[1]])
    else return null
  }
  for (let i = 1; i < n; i++) {
    const [hi, lo] = [out[i - 1][1], out[i][0]]
    if (Math.abs(hi - lo) > 1e-4 * Math.max(Math.abs(hi), Math.abs(lo))) return null
  }
  return out
}

// The currency marks a label may put before its first number, longest first (the API's unitPrefixes).
const UNIT_PREFIXES = ['US$', 'HK$', 'NT$', 'S$', 'A$', 'C$', '$', '€', '£', '¥', '₩']

/**
 * The question's unit, the API's unitOf: in the first label with a number, the currency mark right before it and "%"
 * or "bp" right after it.
 */
export function unitOf(outcomes: string[]): DeskUnit {
  for (const o of outcomes) {
    const at = o.search(/\d/)
    if (at < 0) continue
    const before = o.slice(0, at).trimEnd()
    const rest = o.slice(at).replace(/^[\d.,]+/, '')
    return {
      prefix: UNIT_PREFIXES.find((p) => before.endsWith(p)) ?? '',
      suffix: rest.trim().startsWith('%') ? '%' : /^\s*(bps?|basis points?)\b/i.test(rest) ? 'bp' : '',
    }
  }
  return { prefix: '', suffix: '' }
}

const NO_UNIT: DeskUnit = { prefix: '', suffix: '' }

/** x > 0 to `figs` significant figures, the API's roundSig (exact for whole numbers). */
function roundSig(x: number, figs: number) {
  if (!(x > 0)) return x
  const e = Math.floor(Math.log10(x)) - figs + 1
  return e >= 0 ? Math.round(x / 10 ** e) * 10 ** e : Math.round(x * 10 ** -e) / 10 ** -e
}

/** Decimals that print a 3-significant-figure value below 1,000 in full: 611 → 0, 2.75 → 2, 0.552 → 3. */
const figs3 = (r: number) => r.toFixed(Math.max(0, 2 - Math.floor(Math.log10(r))))

/**
 * A level in the question's unit, the API's fmtLevel (the lead's numbers): 3 significant figures, grouped, compact
 * (M, B, T) from a million: "$611", "$1,480", "$2.12M", "S$0.552", "S$1.80", "2.5%". A currency level keeps its
 * trailing zeros; a percentage or a bare number drops them.
 */
export function fmtLevel(x: number, unit: DeskUnit = NO_UNIT) {
  if (!num(x)) return '—'
  const a = Math.abs(x)
  const r = roundSig(a, 3)
  let s: string
  if (a === 0) s = '0'
  else if (r >= 1e6) {
    const [size, suffix] = r >= 1e12 ? [1e12, 'T'] : r >= 1e9 ? [1e9, 'B'] : [1e6, 'M']
    const m = roundSig(a / size, 3)
    s = (m >= 1000 ? Math.round(m).toLocaleString('en-US') : figs3(m)) + suffix
  } else if (r >= 1000) s = Math.round(r).toLocaleString('en-US')
  else {
    s = figs3(r)
    if ((unit.suffix || !unit.prefix) && s.includes('.')) s = s.replace(/0+$/, '').replace(/\.$/, '')
  }
  return `${x < 0 ? '-' : ''}${unit.prefix}${s}${unit.suffix}`
}

/**
 * Today's level in the desk's prose, the API's todayLevel: a price at its own precision, the shared level rule
 * (fmtLevelPrice) in the question's currency ("$472.20", "S$73.85", "$83,048", "₩262,000"; whole units from a million
 * too, "₩1,047,300", where fmtLevelPrice turns compact, up to 10^15), so the change and the chance at or above today
 * read against the level they were computed at; a level in % or bp, or not above zero, as fmtLevel writes the desk's
 * levels.
 */
export function fmtToday(x: number, unit: DeskUnit = NO_UNIT) {
  if (!num(x) || unit.suffix || !(x > 0)) return fmtLevel(x, unit)
  if (x >= 999_999.5 && x < 1e15) return `${unit.prefix}${Math.round(x).toLocaleString('en-US')}`
  return `${unit.prefix}${fmtLevelPrice(x).slice(1)}`
}

/** A quantile with its "≈" when it lies in an extrapolated tail. */
export const fmtQ = (q: DeskQ, unit: DeskUnit = NO_UNIT) => `${q.approx ? '≈' : ''}${fmtLevel(q.value, unit)}`

/** "below $510", "$510–$999", "above $999": a scenario's range. */
export function rangeText(lo: number | null, hi: number | null, unit: DeskUnit) {
  if (lo == null && hi != null) return `below ${fmtLevel(hi, unit)}`
  if (hi == null && lo != null) return `above ${fmtLevel(lo, unit)}`
  if (lo != null && hi != null) return `${fmtLevel(lo, unit)}–${fmtLevel(hi, unit)}`
  return ''
}

/** A change in percent (already ×100), the API's signedPct: "+26%", "-29%", whole from ±10%, else one decimal ("+4.7%"). */
export function fmtChange(pct: number, whole = false) {
  return `${pct < 0 ? '-' : '+'}${Math.abs(pct).toFixed(whole || Math.abs(pct) >= 9.95 ? 0 : 1)}%`
}

/** An annual return in percent, always one decimal as the lead writes it: "+4.7%", "-13.7%". */
export const fmtAnnual = (pct: number) => `${pct < 0 ? '-' : '+'}${Math.abs(pct).toFixed(1)}%`

/** A linear-scale change in the question's unit, the API's signedLevel: "+0.3%", "-25bp", "+$12". */
export const fmtLinearChange = (d: number, unit: DeskUnit) => `${d < 0 ? '-' : '+'}${fmtLevel(Math.abs(d), unit)}`

/** A range's distance from today as a hint under its bar: "-38%…-7%", "below -48%", "above +209%" (whole percents). */
export function vsRefHint(lo: number | null, hi: number | null) {
  const f = (x: number) => (Math.round(x) === 0 ? '0%' : fmtChange(x, true))
  if (lo == null && hi != null) return `below ${f(hi)}`
  if (hi == null && lo != null) return `above ${f(lo)}`
  if (lo != null && hi != null) return `${f(lo)}…${f(hi)}`
  return undefined
}

/** Each range's hint from the view's edges (K − 1 edges for K ranges), or nothing when they do not line up. */
export function rangeHints(view: Pick<DeskView, 'edges'>, ranges: number) {
  const e = view.edges
  if (e.length !== ranges - 1 || e.some((x) => x.vsRefPct == null)) return []
  return Array.from({ length: ranges }, (_, i) => vsRefHint(i > 0 ? e[i - 1].vsRefPct : null, i < ranges - 1 ? e[i].vsRefPct : null))
}

/** The median of a distribution over the question's ranges (a specialist's anchor, the pool) on the view's edges. */
export function medianOn(view: Pick<DeskView, 'edges' | 'scale'>, items: OutcomeP[]) {
  return quantileOf(view.edges.map((e) => e.value), items.map((o) => o.p), view.scale, 0.5)
}

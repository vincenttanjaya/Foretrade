// Signals board math and copy, kept pure for the tests.
import { fmtDate, fmtPct } from '../../lib/format'
import type { RemovedPick, SignalPick, SignalRecord, SignalRecordSide, SignalSide } from '../../lib/types'

/** Share of the way from the pick price to the target, 0..1 (a move away from the target is 0). */
export function targetProgress(live: number | undefined, pick: Pick<SignalPick, 'priceAtPick' | 'target'>) {
  const span = pick.target - pick.priceAtPick
  if (live == null || span === 0) return 0
  return Math.min(1, Math.max(0, (live - pick.priceAtPick) / span))
}

/** "Ends in 2d 5h", "Ends in 3h 20m", "Ends in 12m" or "Ended": the pick leaves and is scored at validUntil. */
export function validity(validUntil: string, now = Date.now()) {
  const min = Math.floor((new Date(validUntil).getTime() - now) / 60_000)
  if (!(min > 0)) return 'Ended'
  const d = Math.floor(min / 1440)
  const h = Math.floor((min % 1440) / 60)
  return `Ends in ${d ? `${d}d ${h}h` : h ? `${h}h ${min % 60}m` : `${min}m`}`
}

/** Percent move from the pick price to the live price. */
export const sincePick = (live: number | undefined, priceAtPick: number) =>
  live == null || !priceAtPick ? null : (live / priceAtPick - 1) * 100

const dir = (side: SignalSide) => (side === 'bullish' ? 1 : -1)
const round1 = (n: number) => (Math.sign(n) * Math.round(Math.abs(n) * 10)) / 10 + 0 // half away from zero, as fmtPct; + 0 turns -0 into 0

/**
 * Whether a pick is working for its side: `favour` is the move in the pick's direction (positive = working), `price`
 * the raw price move, both in % and rounded to 0.1. Null when the move is unknown.
 */
export type Standing = { favour: number; price: number; state: 'working' | 'against' | 'flat' }
export function standing(favourPct: number | null | undefined, side: SignalSide): Standing | null {
  if (favourPct == null || !Number.isFinite(favourPct)) return null
  const favour = round1(favourPct)
  const price = dir(side) * favour + 0 // the same magnitude, so the two lines never disagree by a rounding step
  return { favour, price, state: favour > 0 ? 'working' : favour < 0 ? 'against' : 'flat' }
}

/** The live move since the pick, in the pick's direction. */
export const favourSincePick = (live: number | undefined, p: Pick<SignalPick, 'priceAtPick' | 'side'>) => {
  const raw = sincePick(live, p.priceAtPick)
  return raw == null ? null : dir(p.side) * raw
}

const STATE_WORD = { working: 'Working', against: 'Against', flat: 'Flat' } as const
const signed = (n: number) => fmtPct(n, { sign: true, decimals: 1 })

/** "Working +7.2%", "Against -17.5%", "Flat 0.0%" or "—". */
export const standingText = (s: Standing | null) => (s ? `${STATE_WORD[s.state]} ${signed(s.favour)}` : '—')

/** "Price +7.2%" / "Price -7.2%": the raw move, never coloured. */
export const priceText = (s: Standing) => `Price ${signed(s.price)}`

/** "Working for this bearish pick: +7.2%. Price down 7.2% since the pick." / "Flat since the pick." */
export function standingLabel(s: Standing | null, side: SignalSide, since = 'since the pick') {
  if (!s) return `Move ${since} unknown.`
  if (s.state === 'flat') return `Flat ${since}.`
  const word = s.state === 'working' ? 'Working for' : 'Against'
  const move = s.price > 0 ? 'up' : 'down' // never 0 here: the price moves by as much as the favour
  return `${word} this ${side} pick: ${signed(s.favour)}. Price ${move} ${fmtPct(Math.abs(s.price), { decimals: 1 })} ${since}.`
}

const TREND = { uptrend: 'Uptrend', downtrend: 'Downtrend', range: 'Range-bound' } as const

/** "30d +12.4%", "RSI 61", "Uptrend", "News bullish". */
export function factorChips(f: SignalPick['factors']) {
  const chips = [`30d ${fmtPct(f.return30dPct, { sign: true, decimals: 1 })}`, `RSI ${Math.round(f.rsi14)}`, TREND[f.trend]]
  if (f.sentiment != null) chips.push(`News ${f.sentiment >= 0.15 ? 'bullish' : f.sentiment <= -0.15 ? 'bearish' : 'neutral'}`)
  return chips
}

/** The card's three hand-offs to the agent. */
export const pickPrompts = (p: Pick<SignalPick, 'symbol' | 'side'>) => [
  `Why is ${p.symbol} on the ${p.side} side of the screen?`,
  `What would invalidate this ${p.symbol} pick?`,
  `Size a trade in ${p.symbol} for my account`,
]

/** "3 h 33 min ago" (floored), "12 min ago" or "just now". */
export function computedAgo(iso: string, now = Date.now()) {
  const min = Math.max(0, Math.floor((now - Date.parse(iso)) / 60_000))
  const h = Math.floor(min / 60)
  return h ? `${h} h ${min % 60} min ago` : min ? `${min} min ago` : 'just now'
}

/** "3:40 PM", in the viewer's time zone. */
export const clockTime = (iso: string) => new Date(iso).toLocaleTimeString('en-US', { hour: 'numeric', minute: '2-digit' })

/** "next try about 3:40 PM" while the retry is ahead; "next try shortly" once its time has passed or is unknown. */
export function nextTry(retryAt: string | null | undefined, now = Date.now()) {
  const at = retryAt ? Date.parse(retryAt) : NaN
  return at > now ? `next try about ${clockTime(retryAt!)}` : 'next try shortly'
}

/** A board is stale past 8 h: two missed 4-hour refreshes. */
export const STALE_AFTER_MS = 8 * 3_600_000
export const isStale = (createdAt: string, now = Date.now()) => now - Date.parse(createdAt) > STALE_AFTER_MS

/**
 * A pick of a board stored before steady-v1: it has no invalidation level (0, or absent from an API before steady-v1).
 * A steady level is never exactly 0 (it may be negative when 2 ATR is over half the price).
 */
export const isLegacyPick = (p: Pick<SignalPick, 'invalidation'>) => !p.invalidation

/**
 * A row the previous rule made: a pick of a board stored before steady-v1 (`legacy`), or a row from an API before
 * steady-v1, which sends no 72 h score. It reads the previous rule's reasons, was never scored and is not in the record.
 */
export const isLegacyRow = (r: Pick<RemovedPick, 'legacy' | 'result72Pct'>) => r.legacy === true || r.result72Pct === undefined

/** How a ledger pick (steady-v1) exited. */
export const EXIT_REASON: Partial<Record<RemovedPick['reason'], string>> = {
  target_hit: 'Target hit',
  invalidated: 'Invalidated',
  expired: '72 h ended',
}

/**
 * How a pick of a board stored before steady-v1 left (the reasons that board recorded): outranked,
 * it still cleared the threshold but stronger picks filled its side; no_data, no fresh analysis or bar at the refresh;
 * left, the board recorded no reason (stored before reasons were recorded, or the switch to the current rule).
 */
export const LEGACY_REASON: Partial<Record<RemovedPick['reason'], string>> = {
  target_hit: 'Target hit',
  expired: 'Validity ended',
  score_fell: 'Score no longer cleared ±20',
  outranked: 'Outranked by stronger picks',
  no_data: 'No fresh data',
  left: 'Left the board',
}

/** The row's reason in words, by its rule; a reason this build does not know reads as the raw word. */
export const reasonText = (r: Pick<RemovedPick, 'legacy' | 'result72Pct' | 'reason'>) =>
  (isLegacyRow(r) ? LEGACY_REASON : EXIT_REASON)[r.reason] ?? r.reason

export const reasonTone = (r: RemovedPick['reason']) => (r === 'target_hit' ? 'up' : r === 'invalidated' ? 'down' : 'neutral')

export const HORIZON_MS = 72 * 3_600_000
/** The ledger stops trying to score a pick 7 days after it was made. */
export const SCORE_GIVE_UP_MS = 7 * 24 * 3_600_000

/** The pending 72 h score's cell: "Scored Oct 16, 2026, 4:00 PM" while due, "Not scored" once the ledger gave up. */
export function pendingScore(pickedAt: string, now = Date.now()) {
  const at = Date.parse(pickedAt)
  if (!Number.isFinite(at)) return '—'
  if (now - at > SCORE_GIVE_UP_MS) return 'Not scored'
  // Past 72 h and not scored yet (the scoring board is a few minutes behind, or lacked the price): never a past "Scored".
  if (now >= at + HORIZON_MS) return 'Scored at the next board'
  return `Scored ${fmtDate(new Date(at + HORIZON_MS).toISOString(), { time: true })}`
}

/**
 * The verdict line under a track-record row. While the record is "insufficient" it names the bar still missing: the
 * scored picks, or (once those are in) the 3-day blocks they span, so it never reads "230 of 200".
 */
export function verdictText(r: Pick<SignalRecordSide, 'verdict' | 'scored' | 'blocks'>, minPicks = 200, minBlocks = 30) {
  switch (r.verdict) {
    case 'better':
      return 'Better than a random pick so far (99% confidence).'
    case 'worse':
      return 'Worse than a random pick so far (99% confidence).'
    case 'no_difference':
      return 'No measurable difference from a random pick.'
    default:
      return r.scored < minPicks
        ? `Not enough scored picks yet (${r.scored} of ${minPicks}) to compare with a random pick.`
        : `Not enough time covered yet (${r.blocks} of ${minBlocks} three-day blocks) to compare with a random pick.`
  }
}

const pct2 = (n: number) => fmtPct(n, { sign: true, decimals: 2 })
const share = (n: number) => fmtPct(n, { decimals: 1 })

/**
 * The "In testing" paragraph from the replay reference. The clauses that judge the numbers (no measurable edge;
 * bigger swings, not better direction) follow the numbers, so a new replay can't print a claim it doesn't support.
 */
export function replayText(r: SignalRecord['replay']) {
  const ci = `95% CI ${pct2(r.excessLo95)} to ${pct2(r.excessHi95)}`
  const edge = r.excessLo95 > 0 ? 'better than random picks' : r.excessHi95 < 0 ? 'worse than random picks' : 'no measurable edge'
  const b = r.bullish
  const swings = b.invalidatedFirstPct > b.randomInvalidatedFirstPct && b.targetFirstPct > b.randomTargetFirstPct
  const bull =
    `Bullish picks reached their target first ${share(b.targetFirstPct)} of the time (random ${share(b.randomTargetFirstPct)})` +
    (swings
      ? `, but also hit their invalidation first ${share(b.invalidatedFirstPct)} of the time (random ${share(b.randomInvalidatedFirstPct)}): bigger swings, not better direction.`
      : ` and hit their invalidation first ${share(b.invalidatedFirstPct)} of the time (random ${share(b.randomInvalidatedFirstPct)}).`)
  return (
    `In testing on data the rule was not tuned on (${fmtDate(r.from)} – ${fmtDate(r.to)}, ${r.picks.toLocaleString('en-US')} picks), ` +
    `picks moved their way ${share(r.movedItsWayPct)} of the time, against ${share(r.randomMovedItsWayPct)} for random picks from the same list. ` +
    `The average 72-hour move in the pick's direction was ${pct2(r.avgResultPct)} (${pct2(r.avgResultNetPct)} after costs), ` +
    `against ${pct2(r.avgRandomPct)} for a random pick: a difference of ${pct2(r.excessPct)} (${ci}), so ${edge}. ` +
    `${bull} Bearish picks reached their target first ${share(r.bearish.targetFirstPct)} of the time (random ${share(r.bearish.randomTargetFirstPct)}).`
  )
}

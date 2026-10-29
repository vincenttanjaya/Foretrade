import { useRef, useState } from 'react'
import { Link, useNavigate } from 'react-router'
import { PageHeader } from '../../components/layout/AppShell'
import { AssetIcon } from '../../components/ui/AssetIcon'
import { Badge } from '../../components/ui/Badge'
import { Button, buttonClass } from '../../components/ui/Button'
import { Disclaimer } from '../../components/ui/Disclaimer'
import { EmptyState } from '../../components/ui/EmptyState'
import { IconAgent, IconAlert, IconExternal, IconSignal, IconTrade } from '../../components/ui/icons'
import { Skeleton } from '../../components/ui/Spinner'
import { Tabs } from '../../components/ui/Tabs'
import { api } from '../../lib/api'
import { useAssets } from '../../lib/assets'
import { cx } from '../../lib/cx'
import { errorMessage } from '../../lib/errors'
import { fmtDate, fmtPct, fmtPrice } from '../../lib/format'
import type { RemovedPick, SignalBoard, SignalPick, SignalRecord, SignalRecordSide, SignalsRefresh, SignalSide } from '../../lib/types'
import { usePoll } from '../../lib/usePoll'
import {
  computedAgo,
  factorChips,
  favourSincePick,
  isLegacyPick,
  isStale,
  nextTry,
  pendingScore,
  pickPrompts,
  priceText,
  reasonText,
  reasonTone,
  replayText,
  type Standing,
  standing,
  standingLabel,
  standingText,
  targetProgress,
  validity,
  verdictText,
} from './signals'

const SIDES: { side: SignalSide; title: string; glyph: string; tone: string; caption: string; empty: string }[] = [
  { side: 'bullish', title: 'Bullish', glyph: '▲', tone: 'text-up', caption: 'Strongest momentum', empty: 'No asset scores 20 or more right now.' },
  { side: 'bearish', title: 'Bearish', glyph: '▼', tone: 'text-down', caption: 'Weakest momentum', empty: 'No asset scores −20 or less right now.' },
]

const VOL = { low: 'Low vol', medium: 'Medium vol', high: 'High vol' } as const
type Tier = 'all' | SignalPick['volTier']
const TIERS: { value: Tier; label: string }[] = [
  { value: 'all', label: 'All' },
  ...(Object.keys(VOL) as SignalPick['volTier'][]).map((v) => ({ value: v, label: VOL[v] })),
]

// The page ticks every 30 s and reads the board on each tick while a refresh is pending or no board validates;
// otherwise only once 45 s have passed since the last read, i.e. once a minute.
const TICK_MS = 30_000
const SETTLED_MS = 45_000

/** "/signals": the shared momentum screen and its track record, polled every minute (every 30 s while it refreshes). */
export function SignalsPage() {
  const [board, setBoard] = useState<SignalBoard | null | undefined>() // undefined = loading, null = none that validates
  const [history, setHistory] = useState<RemovedPick[]>([])
  const [historyUnavailable, setHistoryUnavailable] = useState(false)
  const [refreshing, setRefreshing] = useState(false)
  const [refresh, setRefresh] = useState<SignalsRefresh | null>(null) // null from APIs older than the refresh state
  const [firstBoard, setFirstBoard] = useState(false) // no board has ever been stored
  const [record, setRecord] = useState<SignalRecord | null>(null) // null from APIs before steady-v1
  const [tier, setTier] = useState<Tier>('all')
  const [error, setError] = useState<string | null>(null)

  const lastLoad = useRef(0)
  // Reads are numbered so a slow one answered after a newer one is dropped: it would put the old board back.
  const sent = useRef(0)
  const answered = useRef(0)

  async function load() {
    lastLoad.current = Date.now()
    const n = ++sent.current
    try {
      const res = await api.signals()
      if (n < answered.current) return
      answered.current = n
      setBoard(res.board)
      setHistory(res.history ?? [])
      setHistoryUnavailable(res.historyUnavailable === true)
      setRefreshing(res.refreshing === true)
      setRefresh(res.refresh ?? null)
      setFirstBoard(res.firstBoard === true)
      setRecord(res.record ?? null)
      setError(null)
    } catch (err) {
      if (n < answered.current) return
      setError(errorMessage(err, "Couldn't load the signals board."))
    }
  }
  usePoll(() => {
    if (refreshing || board === null || Date.now() - lastLoad.current >= SETTLED_MS) return load()
  }, TICK_MS)

  // The last refresh failed and the next one waits for its backoff: say so rather than promise a board in minutes.
  const retrying = refresh?.state === 'retrying'
  // A board stored before steady-v1, still shown until the first steady refresh replaces it (an API with a ledger only:
  // an older one's board is its current rule's).
  const legacyBoard = record !== null && !!board && [...board.bullish, ...board.bearish].some(isLegacyPick)

  return (
    <div className="mx-auto max-w-6xl px-4 py-8 md:px-8 md:py-10">
      <PageHeader
        label="Signals board · momentum screen"
        title="Signals"
        subtitle="Up to 4 picks a side, taken from the assets with the strongest and weakest recent momentum, trend and RSI at the time of the pick. Recomputed every 4 hours. A screen, not a forecast: in testing, its picks did no better than random picks from the same list."
        actions={
          error && board !== undefined ? (
            <span role="status" className="font-mono text-[11px] text-muted">
              Couldn't refresh · retrying
            </span>
          ) : undefined
        }
      />

      {board === undefined && error ? (
        <EmptyState
          icon={<IconAlert size={20} />}
          title="Couldn't load the signals board."
          description={error}
          action={
            <Button variant="secondary" onClick={load}>
              Try again
            </Button>
          }
        />
      ) : board === null && retrying ? (
        <EmptyState
          icon={<IconSignal size={20} />}
          title={
            <>
              The last refresh didn't produce <span className="whitespace-nowrap">a board</span>
            </>
          }
          description={`${capitalize(nextTry(refresh.retryAt))}. Picks appear here on their own.`}
        />
      ) : board === null && firstBoard ? (
        <EmptyState
          icon={<IconSignal size={20} />}
          title="The first board is being computed."
          description="Check back in a few minutes."
        />
      ) : board === null ? (
        <EmptyState
          icon={<IconSignal size={20} />}
          title={
            <>
              Refreshing the board · <span className="whitespace-nowrap">usually 2–3 minutes</span>
            </>
          }
          description="The new picks appear here on their own."
        />
      ) : (
        <>
          {board && (
            <>
              <Tabs value={tier} onChange={setTier} items={TIERS} size="sm" aria-label="Volatility" className="mb-3" />
              <p className="mb-6 text-[13px] leading-relaxed text-pretty text-muted">
                {legacyBoard && (
                  <>
                    This board was made under the previous rule: its picks have no invalidation level and are not scored, and they
                    leave at the next refresh, which starts the current rule. Under it,{' '}
                  </>
                )}
                {legacyBoard ? 'a' : 'A'} pick stays until it reaches its target (2 ATR in its favour), hits its invalidation (2 ATR
                against it) or 72 hours pass. An asset can't switch sides within 7 days.
              </p>
            </>
          )}
          <div className="grid gap-8 md:grid-cols-2 md:gap-6">
            {SIDES.map((s) => {
              const picks = board?.[s.side].filter((p) => tier === 'all' || p.volTier === tier) ?? []
              return (
                <section key={s.side} aria-labelledby={`${s.side}-title`} className="min-w-0">
                  <h2 id={`${s.side}-title`} className="flex items-center gap-2 font-display text-[22px] font-medium">
                    {s.title}
                    <span aria-hidden="true" className={cx('text-[0.7em]', s.tone)}>
                      {s.glyph}
                    </span>
                  </h2>
                  <p className="mt-0.5 mb-3 text-[13px] text-muted">{s.caption}</p>
                  <div className="flex flex-col gap-4">
                    {!board ? (
                      [0, 1].map((i) => <CardSkeleton key={i} />)
                    ) : picks.length === 0 ? (
                      <p className="rounded-[14px] border border-dashed border-border px-5 py-10 text-center text-sm text-muted">
                        {tier === 'all' ? s.empty : `No ${tier}-volatility picks on this side right now.`}
                      </p>
                    ) : (
                      picks.map((p) => <PickCard key={p.symbol} pick={p} legacy={legacyBoard && isLegacyPick(p)} />)
                    )}
                  </div>
                </section>
              )
            })}
          </div>

          {board && <Exits rows={history} unavailable={historyUnavailable} />}
          {board && record && <TrackRecord record={record} />}

          {board && (
            <footer className="mt-8 flex flex-wrap items-center justify-between gap-x-6 gap-y-2 border-t border-border pt-4">
              <p className="font-mono text-[11px] text-muted tabular-nums" title={board.method}>
                Board computed {computedAgo(board.createdAt)}{' '}
                {isStale(board.createdAt) && (
                  <>
                    <Badge className="align-[1px]" title="Over 8 hours old: two scheduled refreshes have not replaced it">
                      Stale
                    </Badge>{' '}
                  </>
                )}
                {retrying ? (
                  <>
                    ·{' '}
                    <span role="status" className="whitespace-nowrap">
                      Last refresh failed · {nextTry(refresh.retryAt)}
                    </span>
                  </>
                ) : refreshing ? (
                  <span role="status" className="inline-flex items-center gap-1.5 whitespace-nowrap">
                    <span aria-hidden="true" className="size-1.5 animate-pulse rounded-full bg-accent" />
                    Refreshing · new board in a few minutes
                  </span>
                ) : (
                  <>· next refresh about {new Date(board.nextAt).toLocaleTimeString('en-US', { hour: 'numeric', minute: '2-digit' })}</>
                )}
              </p>
              <Disclaimer />
            </footer>
          )}
        </>
      )}
    </div>
  )
}

/** One pick; `legacy` for a pick of a board stored before steady-v1, which the next refresh drops unscored. */
function PickCard({ pick: p, legacy }: { pick: SignalPick; legacy: boolean }) {
  const navigate = useNavigate()
  const asset = useAssets().bySymbol[p.symbol]
  const live = asset?.price
  const progress = targetProgress(live, p)
  const since = standing(favourSincePick(live, p), p.side)
  const valid = validity(p.validUntil)
  const invalidation = p.invalidation ?? 0 // absent from an API before steady-v1

  return (
    <article aria-label={`${p.symbol} ${p.side} pick`} className="card @container flex flex-col gap-4 p-5">
      <header className="flex items-start gap-3">
        <AssetIcon symbol={p.symbol} size={36} />
        <div className="min-w-0 flex-1">
          <div className="flex items-baseline gap-2">
            <span className="font-mono text-[15px] font-semibold text-text">{p.symbol}</span>
            <span className="truncate text-[13px] text-muted">{asset?.name}</span>
          </div>
          <div className="mt-1 flex flex-wrap items-center gap-1.5">
            <Badge tone="outline">{VOL[p.volTier]}</Badge>
            {legacy ? (
              <Badge title={`Valid until ${fmtDate(p.validUntil, { time: true })} under the previous rule: it leaves at the next refresh and is not scored`}>
                Previous rule
              </Badge>
            ) : (
              <Badge tone={valid === 'Ended' ? 'neutral' : 'accent'} title={`Scored 72 h after the pick · ${fmtDate(p.validUntil, { time: true })}`}>
                {valid}
              </Badge>
            )}
          </div>
        </div>
        <div className="shrink-0 text-right">
          <div className="font-mono text-[15px] font-medium text-text tabular-nums">{fmtPrice(live)}</div>
          {/* Neutral on purpose: green and red on a card only ever say whether the pick is working for its side. */}
          <span className="font-mono text-[12px] text-muted tabular-nums">24h {fmtPct(asset?.change24hPct, { sign: true, decimals: 1 })}</span>
        </div>
      </header>

      <dl className="grid grid-cols-2 gap-3 rounded-lg bg-surface-2 px-3.5 py-3 text-[12.5px] @md:grid-cols-3">
        <div className="min-w-0">
          <dt className="label">Since pick</dt>
          <dd
            className="mt-1"
            aria-label={standingLabel(since, p.side)}
            title={`Picked ${fmtDate(p.pickedAt, { time: true })} at ${fmtPrice(p.priceAtPick)}`}
          >
            <StandingLines s={since} className="text-[13px]" />
          </dd>
        </div>
        <div className="min-w-0">
          <dt className="label">Target {fmtPrice(p.target)}</dt>
          <dd className="mt-2">
            <div
              role="progressbar"
              aria-label={`Progress to the ${fmtPrice(p.target)} target`}
              aria-valuemin={0}
              aria-valuemax={100}
              aria-valuenow={Math.round(progress * 100)}
              className="h-1.5 overflow-hidden rounded-full bg-border"
            >
              <div className="h-full rounded-full bg-accent transition-[width] duration-500" style={{ width: `${progress * 100}%` }} />
            </div>
          </dd>
        </div>
        {invalidation > 0 && (
          <div className="min-w-0" title="The pick ends if the price reaches this level">
            <dt className="label">Invalidation</dt>
            <dd className="mt-1 font-mono text-[13px] text-text tabular-nums">{fmtPrice(invalidation)}</dd>
          </div>
        )}
      </dl>

      <p className="text-[14px] leading-relaxed text-pretty text-text">{p.rationale}</p>

      {/* The rationale is from pick time; the chips are this board's. */}
      <div className="flex items-start gap-2">
        <span aria-hidden="true" className="label pt-[3px]">
          Now:
        </span>
        <ul aria-label="Factors now" className="flex flex-wrap gap-1.5">
          {factorChips(p.factors).map((c) => (
            <li key={c} className="rounded-md border border-border px-2 py-0.5 font-mono text-[11px] text-muted">
              {c}
            </li>
          ))}
        </ul>
      </div>

      {p.headlines.length > 0 && (
        <ul className="flex flex-col gap-1.5 border-t border-border pt-3">
          {p.headlines.map((h) => (
            <li key={h.url} className="min-w-0 text-[12.5px] leading-snug">
              <a
                href={h.url}
                target="_blank"
                rel="noopener noreferrer"
                className="group inline text-text decoration-accent/50 underline-offset-2 hover:text-accent-ink hover:underline"
              >
                <span className="font-mono text-[11px] text-muted">{h.source} · </span>
                {h.title}
                <IconExternal size={11} className="ml-1 inline-block align-[-1px] text-muted" />
              </a>
            </li>
          ))}
        </ul>
      )}

      <ul aria-label={`Ask the agent about ${p.symbol}`} className="mt-auto flex flex-col items-start gap-1.5">
        {pickPrompts(p).map((prompt) => (
          <li key={prompt}>
            <button
              type="button"
              onClick={() => navigate('/', { state: { prompt, context: { symbol: p.symbol } } })}
              className="inline-flex items-center gap-1.5 rounded-full border border-border px-3 py-1 text-left text-[12.5px] text-text transition-colors hover:border-accent hover:text-accent-ink"
            >
              <IconAgent size={13} className="shrink-0 text-muted" />
              {prompt}
            </button>
          </li>
        ))}
      </ul>
      <Link to={`/trade/${p.symbol}`} className={cx(buttonClass('primary', 'sm'), 'self-start')} aria-label={`Trade ${p.symbol}`}>
        <IconTrade size={15} /> Trade
      </Link>
    </article>
  )
}

const th = 'label px-3 py-2.5 text-left font-medium whitespace-nowrap first:pl-5 last:pr-5'
const td = 'px-3 py-3 whitespace-nowrap first:pl-5 last:pr-5'
// The first column stays put while a wide table scrolls sideways on a phone; the edge line shows where rows pass under it.
const pinned = 'sticky left-0 z-10 max-md:shadow-[inset_-1px_0_0_var(--color-border)]'

/**
 * The newest exits (12 at most) with how each left and its fixed 72 h score, and, for a week after the switch, how the
 * picks of boards made before the current rule left (legacy rows: their boards' reasons, never scored). `unavailable`:
 * the legacy rows could not be read (a candle read failed) and are left out; the ledger's exits still show.
 */
function Exits({ rows, unavailable }: { rows: RemovedPick[]; unavailable: boolean }) {
  const legacy = rows.some((r) => r.legacy)
  const missing = "How the previous rule's picks left is unavailable right now."
  return (
    <section aria-labelledby="exits-title" className="card mt-10 overflow-hidden">
      <h2 id="exits-title" className="px-5 pt-5 pb-3 font-display text-[19px] font-medium">
        Recent exits
      </h2>
      {rows.length === 0 ? (
        <p className="border-t border-border px-5 py-8 text-center text-sm text-pretty text-muted">
          No pick has exited yet under the current rule.{unavailable && <> {missing}</>}
        </p>
      ) : (
        <div className="relative overflow-x-auto border-t border-border">
          <table className="w-full min-w-[820px] text-[13px]">
            <thead className="bg-surface-2">
              <tr>
                <th className={cx(th, pinned, 'bg-surface-2')}>Asset</th>
                <th className={th}>Side</th>
                <th className={th}>Picked → exit</th>
                <th className={th}>At exit</th>
                <th className={th}>72 h result</th>
                <th className={th}>Random pick</th>
                <th className={th}>Reason</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {rows.map((r) => {
                const atExit = standing(r.returnPct, r.side) // already signed in the pick's direction
                const result = r.result72Pct == null ? null : standing(r.result72Pct, r.side)
                const levels = [`target ${fmtPrice(r.target)}`, ...(r.invalidation ? [`invalidation ${fmtPrice(r.invalidation)}`] : [])]
                return (
                  <tr key={`${r.symbol}-${r.side}-${r.pickedAt}`}>
                    <td className={cx(td, pinned, 'bg-surface')}>
                      <span className="inline-flex items-center gap-2 font-mono font-semibold text-text">
                        <AssetIcon symbol={r.symbol} size={20} />
                        {r.symbol}
                      </span>
                    </td>
                    {/* The side in plain text: green and red in this table only say for or against the pick. */}
                    <td className={cx(td, 'text-text')}>
                      <span aria-hidden="true" className="mr-1 text-[0.7em] text-muted">
                        {r.side === 'bullish' ? '▲' : '▼'}
                      </span>
                      {r.side === 'bullish' ? 'Bullish' : 'Bearish'}
                    </td>
                    <td className={cx(td, 'text-muted')} title={`${fmtPrice(r.priceAtPick)} → ${fmtPrice(r.exitPrice)}, ${levels.join(', ')}`}>
                      {fmtDate(r.pickedAt)} → {fmtDate(r.exitAt ?? r.removedAt) /* no exitAt from an API before steady-v1 */}
                    </td>
                    <td className={td} aria-label={standingLabel(atExit, r.side, 'at exit')}>
                      <StandingLines s={atExit} />
                    </td>
                    {result ? (
                      <td className={td} aria-label={standingLabel(result, r.side, 'after 72 hours')}>
                        <StandingLines s={result} />
                      </td>
                    ) : r.legacy ? (
                      <td className={cx(td, 'text-muted')} title="Picked under the previous rule: never scored at 72 hours, and not in the track record">
                        <span className="flex flex-col">
                          <span>Not scored</span>
                          <span className="mt-0.5 text-[11.5px]">Previous rule</span>
                        </span>
                      </td>
                    ) : r.result72Pct === undefined ? (
                      <td className={cx(td, 'text-muted')}>—</td> // an API before steady-v1: no fixed-horizon score
                    ) : (
                      <td className={cx(td, 'text-muted')} title="Each pick is scored at the first board 72 hours after it was made">
                        {pendingScore(r.pickedAt)}
                      </td>
                    )}
                    <td className={cx(td, 'font-mono text-muted tabular-nums')}>
                      {r.random72Pct == null ? '—' : fmtPct(r.random72Pct, { sign: true, decimals: 1 })}
                    </td>
                    <td className={td}>
                      <Badge tone={reasonTone(r.reason)}>{reasonText(r)}</Badge>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
      {rows.length > 0 && (legacy || unavailable) && (
        <div className="flex flex-col gap-1 border-t border-border px-5 py-3 text-[12.5px] leading-relaxed text-pretty text-muted">
          {legacy && (
            <p>
              Previous rule: a pick of a board made before the current rule. It left for the reason that board recorded, was
              never scored at 72 hours, and is not in the track record.
            </p>
          )}
          {unavailable && <p>{missing}</p>}
        </div>
      )}
    </section>
  )
}

// No-break spaces keep "72 h" (and "72 h ended") on one line when a header wraps.
const RECORD_COLUMNS = [
  'Scored',
  'Moved its way',
  'Random',
  'Avg 72\u00a0h move',
  'After costs',
  'Random pick',
  'Difference (99% CI)',
  'Target / invalidated / 72\u00a0h\u00a0ended',
]
const RECORD_ROWS: { key: 'all' | SignalSide; label: string }[] = [
  { key: 'all', label: 'All picks' },
  { key: 'bullish', label: 'Bullish' },
  { key: 'bearish', label: 'Bearish' },
]
const share = (n: number | null) => (n == null ? '—' : fmtPct(n, { decimals: 1 }))
const move = (n: number | null) => (n == null ? '—' : fmtPct(n, { sign: true, decimals: 2 }))
const plural = (n: number, one: string, many: string) => `${n.toLocaleString('en-US')} ${n === 1 ? one : many}`

/** "+0.45% (-0.63% to +1.54%)"; the interval is left out while fewer than two 3-day blocks hold a pick. */
function difference(r: SignalRecordSide) {
  if (r.excessPct == null) return '—'
  return r.excessLo99 == null || r.excessHi99 == null ? move(r.excessPct) : `${move(r.excessPct)} (${move(r.excessLo99)} to ${move(r.excessHi99)})`
}

/**
 * The live record: every pick scored 72 h after it was made against a random pick from the same list, per side, with
 * the verdict the record itself earns at 99%; and the out-of-sample replay that the rule was chosen on.
 */
function TrackRecord({ record }: { record: SignalRecord }) {
  // The section is the container: each verdict line stays in view while the wide table scrolls under it on a phone.
  return (
    <section aria-labelledby="record-title" className="card @container mt-10 overflow-hidden">
      <div className="px-5 pt-5 pb-4">
        <h2 id="record-title" className="font-display text-[19px] font-medium">
          Track record
        </h2>
        <p className="mt-1.5 text-[13.5px] leading-relaxed text-pretty text-muted">
          Every pick is scored 72 hours after it was made, whatever happened in between, and compared with a random pick from the
          same list at the same time. Costs: 0.10% per round trip.
        </p>
      </div>
      {record.since === null ? (
        <p className="border-t border-border px-5 py-8 text-center text-sm text-muted">
          No pick has been scored yet. Picks are scored 72 hours after they are made.
        </p>
      ) : (
        <>
          <p className="border-t border-border px-5 py-2.5 font-mono text-[11px] text-muted tabular-nums">
            Since {fmtDate(record.since)} · {plural(record.open, 'pick', 'picks')} waiting for the 72-hour score
            {record.unscored > 0 && <> · {plural(record.unscored, 'pick', 'picks')} could not be scored</>}
          </p>
          <div className="relative overflow-x-auto border-t border-border">
            <table className="w-full min-w-[760px] text-[13px]">
              <thead className="bg-surface-2">
                <tr>
                  <th className={cx(th, pinned, 'bg-surface-2')}>
                    <span className="sr-only">Picks</span>
                  </th>
                  {RECORD_COLUMNS.map((c) => (
                    <th key={c} className="label px-3 py-2.5 text-right align-bottom font-medium last:pr-5">
                      {c}
                    </th>
                  ))}
                </tr>
              </thead>
              {RECORD_ROWS.map(({ key, label }) => {
                const r = record[key]
                const num = cx(td, 'text-right font-mono text-text tabular-nums')
                return (
                  <tbody key={key} className="border-t border-border first-of-type:border-t-0">
                    <tr>
                      <th scope="row" className={cx(td, pinned, 'bg-surface text-left font-medium text-text')}>
                        {label}
                      </th>
                      <td className={num}>{r.scored.toLocaleString('en-US')}</td>
                      <td className={num}>{share(r.movedItsWayPct)}</td>
                      <td className={cx(num, 'text-muted')}>{share(r.randomMovedItsWayPct)}</td>
                      <td className={num}>{move(r.avgResultPct)}</td>
                      <td className={num}>{move(r.avgResultNetPct)}</td>
                      <td className={cx(num, 'text-muted')}>{move(r.avgRandomPct)}</td>
                      <td className={num}>{difference(r)}</td>
                      <td className={num}>
                        {r.targetHit} / {r.invalidated} / {r.expired}
                      </td>
                    </tr>
                    <tr>
                      <td colSpan={9} className="px-5 pt-0 pb-3">
                        <p className="sticky left-5 max-w-[calc(100cqw-2.5rem)] text-[12.5px] text-pretty text-muted">{verdictText(r, record.minPicks, record.minBlocks)}</p>
                      </td>
                    </tr>
                  </tbody>
                )
              })}
            </table>
          </div>
        </>
      )}
      <div className="border-t border-border px-5 py-4">
        <h3 className="label">In testing</h3>
        <p className="mt-1.5 text-[13.5px] leading-relaxed text-pretty text-text">{replayText(record.replay)}</p>
      </div>
    </section>
  )
}

/** "Working +7.2%" (up tone) / "Against -17.5%" (down tone) / "Flat 0.0%" over the raw price move in neutral grey. */
function StandingLines({ s, className }: { s: Standing | null; className?: string }) {
  const tone = s?.state === 'working' ? 'text-up' : s?.state === 'against' ? 'text-down' : 'text-muted'
  return (
    <span className={cx('flex flex-col font-mono tabular-nums whitespace-nowrap', className)}>
      <span className={cx('font-medium', tone)}>{standingText(s)}</span>
      {s && <span className="mt-0.5 text-[11.5px] text-muted">{priceText(s)}</span>}
    </span>
  )
}

const capitalize = (s: string) => s.charAt(0).toUpperCase() + s.slice(1)

function CardSkeleton() {
  return (
    <div aria-hidden="true" className="card flex flex-col gap-4 p-5">
      <div className="flex items-center gap-3">
        <Skeleton className="size-9 rounded-full" />
        <Skeleton className="h-4 w-32" />
        <Skeleton className="ml-auto h-4 w-20" />
      </div>
      <Skeleton className="h-14 w-full" />
      <Skeleton className="h-4 w-full" />
      <Skeleton className="h-4 w-3/4" />
    </div>
  )
}

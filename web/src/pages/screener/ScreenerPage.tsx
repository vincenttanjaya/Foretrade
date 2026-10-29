import { useEffect, useRef, useState, type ReactNode } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router'
import { PageHeader } from '../../components/layout/AppShell'
import { Badge } from '../../components/ui/Badge'
import { Button, buttonClass } from '../../components/ui/Button'
import { Disclaimer } from '../../components/ui/Disclaimer'
import { EmptyState } from '../../components/ui/EmptyState'
import { IconAgent, IconAlert, IconBell, IconExternal, IconScreener, IconTrade } from '../../components/ui/icons'
import { Input, Select } from '../../components/ui/Input'
import { Skeleton } from '../../components/ui/Spinner'
import { Tabs } from '../../components/ui/Tabs'
import { api } from '../../lib/api'
import { cx } from '../../lib/cx'
import { errorMessage } from '../../lib/errors'
import { fmtDate, fmtPct, fmtPrice } from '../../lib/format'
import type { ScreenerFilter, ScreenerMarket, ScreenerPage as Page, ScreenerRow, ScreenerSort, ScreenerSortKey } from '../../lib/types'
import { ATH_WITHHELD } from '../automations/pipeline'
import { utcClock } from '../strategies/quoteFeeds'
import { breakBeforeTime, everyText, liveHigh, mergeRows, orderMoved, quoteLabel, quoteStale, quoteTitle, sessionLine, staleTitle, utcDay } from './live'
import {
  athHidden,
  FILTERS,
  fmtLocal,
  indexNames,
  nextSort,
  PAGE_SIZE,
  readQuery,
  sortParts,
  statusLabel,
  viewPrompt,
  type Query,
} from './screener'
import { useLiveRows } from './useLiveRows'
import { AlertDialog } from '../trade/AlertDialog'
import { useAssetsIfAny } from '../../lib/assets'

/**
 * "/screener": North Asia and SEA index constituents against their 200-week moving average. Prices are each listing's
 * current quote, refreshed in place; the averages come from the latest run (every weekday after the Asian close).
 */
export function ScreenerPage() {
  const [params, setParams] = useSearchParams()
  const query = readQuery(params)
  const key = `${query.filter}|${query.market}|${query.q}|${query.sort}`
  const [page, setPage] = useState<Page | undefined>() // the first page of the current query, then each page's meta
  const [rows, setRows] = useState<ScreenerRow[]>([])
  const [loadedKey, setLoadedKey] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [moreError, setMoreError] = useState<string | null>(null)
  const [loadingMore, setLoadingMore] = useState(false)
  const [attempt, setAttempt] = useState(0)
  // Each first-page load: its number restarts the in-place refresh, its time is what the order is from.
  const [load, setLoad] = useState<{ seq: number; at: number }>({ seq: 0, at: 0 })
  const [moved, setMoved] = useState(false) // prices moved since the order was set: it and the counts may be out of date
  const [resorting, setResorting] = useState(false)
  const current = useRef(key)
  current.current = key
  // The query as last set: the router renders a URL change in a transition, so a change made before that render (a
  // search typed right after a market pick) must build on the pending one, not on the URL this render read.
  const intended = useRef(query)
  const seen = useRef(key)
  if (seen.current !== key) {
    seen.current = key
    intended.current = query
  }

  useEffect(() => {
    const ctrl = new AbortController()
    setError(null)
    setMoreError(null)
    api
      .screener({ filter: query.filter, market: query.market || undefined, q: query.q || undefined, sort: query.sort, limit: PAGE_SIZE }, ctrl.signal)
      .then((res) => {
        setPage(res)
        setRows(res.rows)
        setLoadedKey(key)
        setLoad((l) => ({ seq: l.seq + 1, at: Date.now() }))
        setMoved(false)
      })
      .catch((err) => {
        if (!ctrl.signal.aborted) setError(errorMessage(err, "Couldn't load the screener."))
      })
      .finally(() => {
        if (!ctrl.signal.aborted) setResorting(false)
      })
    return () => ctrl.abort()
  }, [key, attempt]) // `key` is the query

  // Rows refresh in place: the order and the counts stay as loaded until the reader sorts, re-sorts or reloads.
  const shown = useRef(rows)
  shown.current = rows
  const sortKey = sortParts(query.sort).key
  const { live, now, error: refreshError } = useLiveRows({
    rows,
    pollSeconds: page?.pollSeconds,
    loadKey: load.seq,
    onRows: (fresh) => {
      if (orderMoved(shown.current, fresh, sortKey)) setMoved(true)
      setRows((prev) => mergeRows(prev, fresh))
    },
  })
  const resort = () => {
    setResorting(true)
    setAttempt((n) => n + 1)
  }

  async function loadMore() {
    if (!page?.nextCursor) return
    const asked = key
    setLoadingMore(true)
    setMoreError(null)
    try {
      const res = await api.screener({ filter: query.filter, market: query.market || undefined, q: query.q || undefined, sort: query.sort,
        limit: PAGE_SIZE, cursor: page.nextCursor })
      if (current.current !== asked) return // the query changed meanwhile
      setRows((prev) => [...prev, ...res.rows.filter((r) => !prev.some((p) => p.ticker === r.ticker))])
      setPage(res)
    } catch (err) {
      if (current.current === asked) setMoreError(errorMessage(err, "Couldn't load more rows."))
    } finally {
      setLoadingMore(false)
    }
  }

  const set = (next: Partial<Query>) => {
    const merged = { ...intended.current, ...next }
    intended.current = merged
    const p = new URLSearchParams()
    if (merged.filter !== 'all') p.set('filter', merged.filter)
    if (merged.market) p.set('market', merged.market)
    if (merged.q) p.set('q', merged.q)
    if (merged.sort !== 'pctVsSma') p.set('sort', merged.sort)
    setParams(p, { replace: true })
  }

  const loading = loadedKey !== key && !error
  const marketName = (code: string) => page?.markets.find((m) => m.code === code)?.name ?? code

  return (
    // Wider than the other pages: the table needs about 1,240 px for every column and its actions.
    <div className="mx-auto max-w-[90rem] px-4 py-8 md:px-8 md:py-10">
      <PageHeader
        label="Asia screener · 200-week moving average"
        title="Asia screener"
        subtitle={`North Asia and Southeast Asia index stocks against their 200-week moving average, with the 14-week RSI and the drawdown from each one's all-time high. ${
          page && page.pollSeconds == null ? 'Recomputed every Saturday.' : LIVE_SUBTITLE
        }`}
      />

      <div className="mb-5 flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
        <div className="-mx-4 overflow-x-auto px-4 lg:mx-0 lg:px-0">
          <Tabs
            value={query.filter}
            onChange={(filter: ScreenerFilter) => set({ filter })}
            aria-label="Screen"
            items={FILTERS.map((f) => ({
              value: f.value,
              label: (
                <span title={f.hint}>
                  {f.label}
                  {page && (
                    <>
                      {' '}
                      {/* The API lists every filter, 0 included; a filter missing from an older response reads 0 as a fallback. */}
                      <span className="ml-0.5 font-mono text-[11px] text-muted tabular-nums">{page.counts[f.value] ?? 0}</span>
                    </>
                  )}
                </span>
              ),
            }))}
          />
        </div>
        <div className="flex gap-2">
          <Select
            aria-label="Market"
            value={query.market}
            onChange={(e) => set({ market: e.target.value as ScreenerMarket | '' })}
            className="w-auto min-w-0 shrink"
          >
            <option value="">All markets</option>
            {(page?.markets ?? []).map((m) => (
              <option key={m.code} value={m.code}>
                {m.name} ({m.count})
              </option>
            ))}
          </Select>
          <SearchBox value={query.q} onChange={(q) => set({ q })} />
        </div>
      </div>

      <RunNote page={page} />

      {error && !page ? (
        <EmptyState
          icon={<IconAlert size={20} />}
          title="Couldn't load the screener."
          description={error}
          action={
            <Button variant="secondary" onClick={() => setAttempt((n) => n + 1)}>
              Try again
            </Button>
          }
        />
      ) : (
        <section aria-label="Screened stocks" aria-busy={loading} className="card overflow-hidden">
          {error && (
            <p role="alert" className="border-b border-border px-5 py-2.5 text-[13px] text-down">
              {error}{' '}
              <button type="button" className="underline" onClick={() => setAttempt((n) => n + 1)}>
                Try again
              </button>
            </p>
          )}
          {live && rows.length > 0 && (
            <LiveNote seconds={page!.pollSeconds!} at={load.at} moved={moved} error={refreshError} resorting={resorting} onResort={resort} />
          )}
          <div className="relative overflow-x-auto">
            <table className="w-full min-w-[1100px] text-[13px]">
              <thead className="bg-surface-2">
                <tr>
                  <Th sort={query.sort} col="ticker" onSort={(s) => set({ sort: s })} sticky>
                    Stock
                  </Th>
                  <Th sort={query.sort} col="market" onSort={(s) => set({ sort: s })}>
                    Market · index
                  </Th>
                  <Th sort={query.sort} col="priceUsd" onSort={(s) => set({ sort: s })} right>
                    Price · USD
                  </Th>
                  <Th sort={query.sort} col="pctVsSma" onSort={(s) => set({ sort: s })} right>
                    vs 200WMA
                  </Th>
                  <Th sort={query.sort} col="direction" onSort={(s) => set({ sort: s })}>
                    Direction
                  </Th>
                  <Th sort={query.sort} col="rsi" onSort={(s) => set({ sort: s })} right>
                    RSI 14w
                  </Th>
                  <Th sort={query.sort} col="drawdown" onSort={(s) => set({ sort: s })} right>
                    From ATH
                  </Th>
                  <Th sort={query.sort} col="updated" onSort={(s) => set({ sort: s })}>
                    Updated
                  </Th>
                  <th className={cx(th, 'text-right', stickyEnd, 'bg-surface-2')}>
                    <span className="sr-only">Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody className="divide-y divide-border">
                {loading && rows.length === 0
                  ? Array.from({ length: 8 }, (_, i) => <SkeletonRow key={i} />)
                  : rows.map((r) => <Row key={r.ticker} row={r} marketName={marketName(r.market)} now={now} />)}
              </tbody>
            </table>
          </div>
          {!loading && rows.length === 0 && (
            <p className="border-t border-border px-5 py-10 text-center text-sm text-muted">
              {query.q || query.market ? 'No stocks match this search.' : 'No stocks in this band right now.'}
            </p>
          )}
          {page && rows.length > 0 && (
            <div className="flex flex-wrap items-center justify-between gap-3 border-t border-border px-5 py-3">
              <span className="font-mono text-[11.5px] text-muted tabular-nums">
                Showing {rows.length} of {page.total}
              </span>
              {moreError && (
                <span role="alert" className="text-[12.5px] text-down">
                  {moreError}
                </span>
              )}
              {page.nextCursor && (
                <Button variant="secondary" size="sm" loading={loadingMore} onClick={loadMore}>
                  Load more
                </Button>
              )}
            </div>
          )}
        </section>
      )}

      {/* The copy follows what the API is (a live one sends pollSeconds), the note above the table whether rows refresh. */}
      {page && <Footer page={page} live={page.pollSeconds != null} />}
    </div>
  )
}

const th = 'label px-2.5 py-2.5 text-left font-medium whitespace-nowrap first:pl-5 last:pr-5'
const td = 'px-2.5 py-3 align-top whitespace-nowrap first:pl-5 last:pr-5'
const sticky = 'sticky left-0 z-10 bg-surface'
// The actions stay in view from md up: the table is wider than the page, and they are what a row is for.
const stickyEnd = 'md:sticky md:right-0 md:z-10'

function Th({ sort, col, onSort, right, sticky: stick, children }: {
  sort: ScreenerSort
  col: ScreenerSortKey
  onSort: (s: ScreenerSort) => void
  right?: boolean
  sticky?: boolean
  children: ReactNode
}) {
  const { key, desc } = sortParts(sort)
  const active = key === col
  return (
    <th
      aria-sort={active ? (desc ? 'descending' : 'ascending') : 'none'}
      className={cx(th, right && 'text-right', stick && cx(sticky, 'bg-surface-2'))}
    >
      <button
        type="button"
        onClick={() => onSort(nextSort(sort, col))}
        className={cx('inline-flex items-center gap-1 uppercase hover:text-text', active && 'text-text')}
      >
        {children}
        <span aria-hidden="true" className={cx('text-[9px]', !active && 'opacity-0')}>
          {desc ? '▼' : '▲'}
        </span>
      </button>
    </th>
  )
}

/** A research-only row's View tooltip: why it cannot be traded here (the API's tradeNote), then what to do instead. */
export const researchTitle = (note: string | null) =>
  note?.trim() ? `Research only: ${note.trim().replace(/\.$/, '')}. Ask the agent for an analysis.` : 'Research only: ask the agent for an analysis'

function Row({ row: r, marketName, now }: { row: ScreenerRow; marketName: string; now: number }) {
  const bySymbol = useAssetsIfAny()?.bySymbol ?? {}
  const navigate = useNavigate()
  const hasValues = r.updatedAt != null
  // Stale: the last run failed (earlier values are kept), or the live quote is older than its tier allows.
  const quoteOld = r.quote != null && quoteStale(r.quote, now)
  const stale = (r.status === 'failed' && hasValues) || quoteOld
  const tradeLabel = r.tradableSymbol && `Trade ${r.tradableSymbol}${r.tradeVia === 'adr' ? ' (via ADR)' : ''}`
  const stock = (
    <>
      {/* A dotted underline marks the ticker that opens the Trade page (no hover on a phone). */}
      <div className={cx('font-mono font-semibold text-text', r.tradableSymbol && 'underline decoration-muted/45 decoration-dotted underline-offset-[3px] group-hover:text-accent-ink')}>{r.ticker}</div>
      <div className="truncate text-[12px] text-muted" title={r.listName !== r.name ? `${r.name} · listed as ${r.listName}` : r.name}>
        {r.name}
      </div>
    </>
  )
  return (
    <tr aria-label={`${r.ticker} ${r.name}`} className={cx(stale && 'opacity-75')}>
      <td className={cx(td, sticky, 'max-w-[9.5rem] sm:max-w-[12rem]')}>
        {r.tradableSymbol ? (
          // The stock cell opens its Trade page too, like the Trade button at the row's end.
          <Link
            to={`/trade/${r.tradableSymbol}`}
            aria-label={tradeLabel!}
            className="group -mx-1 -my-0.5 block rounded-md px-1 py-0.5 focus-visible:ring-2 focus-visible:ring-accent/40 focus-visible:outline-none"
          >
            {stock}
          </Link>
        ) : (
          stock
        )}
      </td>
      <td className={td}>
        <div className="flex items-center gap-1.5">
          <Badge tone="outline" title={marketName}>
            {r.market}
          </Badge>
          <span className="max-w-[10rem] truncate text-[12px] text-muted" title={indexNames(r)}>
            {indexNames(r)}
          </span>
        </div>
      </td>
      <td className={cx(td, 'text-right font-mono tabular-nums')}>
        {r.price != null ? (
          <>
            <div className="text-text" title={r.quote ? `${quoteLabel(r.quote)} · ${quoteTitle(r.quote)}` : undefined}>
              {fmtLocal(r.price, r.currency)}
            </div>
            <div className="text-[11.5px] text-muted">
              {r.currency === 'USD' ? (
                'USD-quoted'
              ) : (
                <span title={r.fx ? `${r.fx.pair} ${r.fx.usdPerUnit.toPrecision(6)} · ${fmtDate(r.fx.at, { time: true })}` : 'FX unavailable this week'}>
                  {r.priceUsd != null ? fmtPrice(r.priceUsd) : 'USD n/a'}
                </span>
              )}
              {/* On a phone the Updated column (and its Stale badge) is a scroll away: say it beside the price too, on the
                  same line, so a quote going stale never makes its row taller. */}
              {quoteOld && (
                <span className="ml-1.5 text-[11px] text-down md:hidden" title={staleTitle(r.quote!)}>
                  stale quote
                </span>
              )}
            </div>
          </>
        ) : (
          <span className="text-muted">—</span>
        )}
      </td>
      <td className={cx(td, 'text-right font-mono tabular-nums')}>
        {r.pctVsSma != null ? (
          <span className={r.pctVsSma < 0 ? 'text-down' : 'text-up'} title={r.sma200w != null ? `200-week average ${fmtLocal(r.sma200w, r.currency)}` : undefined}>
            {fmtPct(r.pctVsSma, { sign: true, decimals: 1 })}
          </span>
        ) : (
          <span className="text-[12px] whitespace-normal text-muted" title={r.statusNote ?? undefined}>
            {statusLabel(r)}
          </span>
        )}
      </td>
      <td className={td}>
        <Direction row={r} />
      </td>
      <td className={cx(td, 'text-right font-mono tabular-nums')}>
        {r.rsi14w != null ? (
          <span className={r.rsi14w < 30 ? 'font-semibold text-down' : r.rsi14w > 70 ? 'text-up' : 'text-text'}>{r.rsi14w.toFixed(1)}</span>
        ) : (
          <span className="text-muted">—</span>
        )}
      </td>
      <td className={cx(td, 'text-right font-mono tabular-nums')}>
        {athHidden(r) ? (
          <span className="text-[12px] text-muted" title={ATH_WITHHELD}>
            n/a
          </span>
        ) : r.drawdownPct != null ? (
          <>
            <div className="text-text" title={r.ath != null ? `All-time high ${fmtLocal(r.ath, r.currency)}${r.athFrom ? `, Yahoo history from ${fmtDate(r.athFrom)}` : ''}` : undefined}>
              {fmtPct(r.drawdownPct, { decimals: 1 })}
            </div>
            {r.athAt && (
              // A new high from the live price is dated by its trading day, a high the run found by its week.
              <div className="text-[11.5px] text-muted">{liveHigh(r) ? `New high ${fmtDate(r.athAt)}` : `ATH wk of ${fmtDate(r.athAt)}`}</div>
            )}
          </>
        ) : (
          <span className="text-muted">—</span>
        )}
      </td>
      <td className={td}>
        <Updated row={r} now={now} />
      </td>
      <td className={cx(td, 'text-right', stickyEnd, 'bg-surface')}>
        <div className="flex flex-col items-stretch gap-1.5">
          {r.tradableSymbol ? (
            <>
              <AlertDialog
                symbol={r.tradableSymbol}
                asset={bySymbol[r.tradableSymbol]}
                kind="drawdown"
                label="Set drawdown alert"
                ariaLabel={`Set drawdown alert on ${r.tradableSymbol}`}
                className="h-7 px-2.5 text-[12px]"
                icon={<IconBell size={13} />}
              />
              <Link
                to={`/trade/${r.tradableSymbol}`}
                className={buttonClass('primary', 'sm', 'h-7 px-2.5 text-[12px]')}
                aria-label={tradeLabel!}
                title={r.tradeNote ?? undefined}
              >
                <IconTrade size={13} /> Trade
                {r.tradeVia === 'adr' && <span className="font-mono text-[10px] opacity-80">via ADR</span>}
              </Link>
            </>
          ) : (
            <button
              type="button"
              onClick={() => navigate('/', { state: { prompt: viewPrompt(r, marketName) } })}
              className={buttonClass('ghost', 'sm', 'h-7 px-2.5 text-[12px]')}
              aria-label={`View ${r.ticker} with the agent`}
              aria-description={r.tradeNote?.trim() ? `Research only: ${r.tradeNote.trim()}` : undefined}
              title={researchTitle(r.tradeNote)}
            >
              <IconAgent size={13} /> View
            </button>
          )}
        </div>
      </td>
    </tr>
  )
}

function Direction({ row: r }: { row: ScreenerRow }) {
  if (!r.direction) return <span className="text-muted">—</span>
  const deep = r.direction === 'deepening'
  const change = r.pctVsSma != null && r.pctVsSmaPrev != null ? r.pctVsSma - r.pctVsSmaPrev : null
  return (
    <span
      className={cx('inline-flex items-center gap-1 text-[12.5px]', deep ? 'text-down' : 'text-up')}
      title={change != null ? `${fmtPct(change, { sign: true, decimals: 2 })} pts vs last week` : undefined}
    >
      <span aria-hidden="true">{deep ? '↓' : '↑'}</span>
      {deep ? 'Deepening' : 'Recovering'}
    </span>
  )
}

/** When the row's price is from: its quote's label and session for a live row, else the run's date. */
function Updated({ row: r, now }: { row: ScreenerRow; now: number }) {
  if (r.status === 'pending') return <Badge tone="neutral">Pending</Badge>
  if (r.status === 'unavailable')
    return (
      <Badge tone="outline" title={r.statusNote ?? undefined}>
        No data feed
      </Badge>
    )
  const q = r.quote
  const failed = r.status === 'failed' && (
    <Badge tone="down" title={`Last refresh failed${r.statusNote ? `: ${r.statusNote}` : ''}; ${q ? 'the averages are' : 'values'} from ${fmtDate(r.updatedAt)}`}>
      {q ? 'Old averages' : 'Stale'}
    </Badge>
  )
  if (!q)
    return (
      <div className="flex flex-col items-start gap-1">
        <span className="text-[12.5px] text-text">{fmtDate(r.updatedAt)}</span>
        {failed}
      </div>
    )
  const old = quoteStale(q, now)
  return (
    <div className="flex flex-col items-start gap-0.5">
      <span className={cx('text-[12px] leading-snug whitespace-normal', old ? 'text-muted' : 'text-text')} title={quoteTitle(q)}>
        {breakBeforeTime(quoteLabel(q))}
      </span>
      {/* One line, the same height either way: a stale quote's badge takes the session's place (its title keeps it),
          so a row never grows or shrinks under the reader when its quote ages or is read again. */}
      <span className="flex min-h-5 items-center gap-1.5 text-[11.5px] text-muted">
        {old ? (
          <>
            <Badge tone="down" title={`${staleTitle(q)} ${sessionLine(q)}.`}>
              Stale
            </Badge>
            {utcDay(q.lastTradeAt)}
          </>
        ) : (
          sessionLine(q)
        )}
      </span>
      {failed && <span className="mt-0.5 flex gap-1">{failed}</span>}
    </div>
  )
}

function SkeletonRow() {
  return (
    <tr aria-hidden="true">
      {Array.from({ length: 9 }, (_, i) => (
        <td key={i} className={td}>
          <Skeleton className={cx('h-3.5', i === 0 ? 'w-24' : 'w-14')} />
        </td>
      ))}
    </tr>
  )
}

/** Debounced search: the URL follows 300 ms after typing stops. */
function SearchBox({ value, onChange }: { value: string; onChange: (q: string) => void }) {
  const [text, setText] = useState(value)
  const latest = useRef(onChange)
  latest.current = onChange
  useEffect(() => setText(value), [value])
  useEffect(() => {
    if (text.trim() === value) return
    const t = setTimeout(() => latest.current(text.trim()), 300)
    return () => clearTimeout(t)
  }, [text, value])
  return (
    <Input
      type="search"
      aria-label="Search stocks"
      placeholder="Ticker or country"
      maxLength={64}
      value={text}
      onChange={(e) => setText(e.target.value)}
      className="w-full min-w-0 sm:w-64"
    />
  )
}

/** The run's state above the table when it matters: no run yet, a refresh in progress, a failed run. */
function RunNote({ page }: { page?: Page }) {
  const run = page?.run
  if (!page) return null
  let text: string | null = null
  if (!run) text = `The first run hasn't started yet. Rows fill in as it reads them; the next run is ${fmtDate(page.nextRunAt, { time: true })}.`
  else if (run.status === 'running') text = `The averages are being recomputed: ${run.done} of ${run.tickers} read. Rows update as it goes.`
  else if (run.status === 'failed') text = `The last run stopped early (${run.error ?? 'unknown error'}). Earlier values are kept; it retries within 3 hours.`
  if (!text) return null
  return (
    <p role="status" className="mb-4 flex items-start gap-2 rounded-lg border border-border bg-surface-2 px-3.5 py-2.5 text-[13px] text-muted">
      <IconScreener size={15} className="mt-0.5 shrink-0" />
      {text}
    </p>
  )
}

/** "45 s", "14 min 12 s", "1 h 2 min": how long a run took. */
export function fmtDuration(seconds: number) {
  const s = Math.max(0, Math.round(seconds))
  if (s < 60) return `${s} s`
  if (s < 3600) return `${Math.floor(s / 60)} min${s % 60 ? ` ${s % 60} s` : ''}`
  return `${Math.floor(s / 3600)} h${Math.floor((s % 3600) / 60) ? ` ${Math.floor((s % 3600) / 60)} min` : ''}`
}

/** A run's cost after its counts: "; took 14 min 12 s, 794 Yahoo calls" (each part only when the API sends it). */
function runCost(run: NonNullable<Page['run']>) {
  const parts = [
    run.durationSeconds != null && `${run.status === 'running' ? 'running for' : 'took'} ${fmtDuration(run.durationSeconds)}`,
    run.yahooCalls != null && `${run.yahooCalls.toLocaleString('en-US')} Yahoo call${run.yahooCalls === 1 ? '' : 's'}`,
  ].filter(Boolean)
  return parts.length ? `; ${parts.join(', ')}` : ''
}

const LIVE_SUBTITLE =
  "Prices are each listing's latest quote, refreshed through the trading day; the averages behind them are recomputed every weekday after the Asian close."

/**
 * How the live columns are computed (the API's method text says the same): the price against the latest run's
 * averages, the RSI as the run found it.
 */
export const LIVE_METHOD =
  "Each price is that listing's own latest quote (never its ADR's or its perp's), labelled with its delay and last trade. " +
  "% vs 200WMA compares it with the latest run's 200-week mean, the direction compares that with last week's, and the drawdown " +
  "is from the higher of the run's all-time high and the price, so a new high shows 0% and today's date. The 14-week RSI is " +
  "the run's weekly value. Filters, tab counts, sorting and the market list use the same values; USD prices are labelled with their rate."

/**
 * The in-place refresh above the table, the same height whatever it says (it reserves its longest message's lines), so
 * nothing under the reader moves: how often prices refresh and what the order is from, that they moved since (the order
 * and counts may be out of date), or why a refresh failed. Re-sort reloads the first page in the current order.
 */
function LiveNote({ seconds, at, moved, error, resorting, onResort }: {
  seconds: number
  at: number
  moved: boolean
  error: string | null
  resorting: boolean
  onResort: () => void
}) {
  const time = at ? utcClock(new Date(at).toISOString()) : ''
  const since = time ? ` since ${time}` : ''
  return (
    <div className="flex items-center justify-between gap-3 border-b border-border px-5 py-1.5 text-[12.5px] text-muted">
      {/* As many lines as its longest message takes at this width (three on a phone, two on a small tablet, one from
          lg), so the table under it never moves when the message changes. */}
      <p aria-live="polite" className={cx('min-h-[3lh] min-w-0 sm:min-h-[2lh] lg:min-h-0', error && 'text-down')}>
        {error
          ? `${error.replace(/\.?$/, '.')} Retrying ${everyText(seconds)}; old rows show Stale.`
          : moved
            ? `Prices moved${since}: the order and the tab counts are from then.`
            : `Prices refresh ${everyText(seconds)}, in place; the order is from ${time || 'this load'}.`}
      </p>
      <Button variant="ghost" size="sm" loading={resorting} onClick={onResort} className="h-7 shrink-0 px-2.5 text-[12px]" title="Sort again with the current prices">
        Re-sort
      </Button>
    </div>
  )
}

/** "Every weekday…" → "every weekday…", after a label's colon; an acronym ("SET …") keeps its capitals. */
const lowerFirst = (t: string) => (/^[A-Z][a-z]/.test(t) ? t[0].toLowerCase() + t.slice(1) : t)

function Footer({ page, live }: { page: Page; live: boolean }) {
  const run = page.run
  return (
    <footer className="mt-6 flex flex-col gap-3 border-t border-border pt-4 text-[12.5px] text-muted">
      <p className="text-text">
        Information only, not a recommendation: a stock far below its average can keep falling. Not financial advice.
      </p>
      <p className="font-mono text-[11px] tabular-nums">
        {page.asOf ? `Data as of ${fmtDate(page.asOf, { time: true })}` : 'No data yet'}
        {run && ` · last run ${run.status} ${fmtDate(run.finishedAt ?? run.startedAt, { time: true })} (${run.ok + run.insufficient} read, ${run.unavailable} unavailable, ${run.failed} failed${runCost(run)})`}
        {` · next run ${fmtDate(page.nextRunAt, { time: true })}`}
      </p>
      <p>
        {/* The live API's text may lead with its own "Prices:". */}
        Prices: {page.source.prices.replace(/^Prices:\s*/, '')}. FX: {page.source.fx}.{' '}
        {live ? LIVE_METHOD : "Weekly closes and highs are each exchange's; USD prices are labelled with their rate."}
        {page.schedule && ` Runs: ${lowerFirst(page.schedule)}`}
      </p>
      <details>
        <summary className="cursor-pointer text-text">Index lists and sources</summary>
        <ul className="mt-2 flex flex-col gap-1.5">
          {page.source.lists.map((l) => (
            <li key={l.index}>
              <span className="text-text">{l.index}</span> · {l.count} stocks · as of {fmtDate(l.asOf)} · {l.sourceKind}
              {l.sources.map((s, i) => (
                <a key={s} href={s} target="_blank" rel="noopener noreferrer" className="ml-2 inline-flex items-center gap-0.5 text-accent-ink hover:underline">
                  source{l.sources.length > 1 ? ` ${i + 1}` : ''}
                  <IconExternal size={11} />
                </a>
              ))}
            </li>
          ))}
        </ul>
      </details>
      <details>
        <summary className="cursor-pointer text-text">How it is computed</summary>
        <p className="mt-2 leading-relaxed">{page.method}</p>
      </details>
      <Disclaimer />
    </footer>
  )
}

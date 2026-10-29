import { useEffect, useEffectEvent, useMemo, useRef, useState } from 'react'
import { Link, useSearchParams } from 'react-router'
import { Badge } from '../../components/ui/Badge'
import { Button } from '../../components/ui/Button'
import { Card } from '../../components/ui/Card'
import { Disclaimer } from '../../components/ui/Disclaimer'
import { EmptyState } from '../../components/ui/EmptyState'
import { IconAlert, IconExternal, IconNews, IconSignal } from '../../components/ui/icons'
import { Skeleton } from '../../components/ui/Spinner'
import { Tabs } from '../../components/ui/Tabs'
import { api } from '../../lib/api'
import { cx } from '../../lib/cx'
import { errorMessage } from '../../lib/errors'
import { fmtDate, timeAgo } from '../../lib/format'
import type { MarketBrief, NewsItem, NewsRegion } from '../../lib/types'
import { usePoll } from '../../lib/usePoll'
import { asBrief, asNews, briefStale, citations, CORE, monogram, newsQuery, REGION_LABEL, regionParam, REGIONS, resolveSources, wireScope, type Cited } from './news'
import { AssetChip, Chip, FearGreedTile, SentimentStrip } from './parts'

// `all` (newest 100, every region) feeds the "All" list and, after the brief's stored sources, resolves its bullets' ids;
// `list` is the current filter and region's server-side result, so an asset chip shows that asset's headlines rather than
// whatever made the newest 100.
type Wire = { brief: MarketBrief | null; all: NewsItem[]; list: NewsItem[]; filter: string; region: NewsRegion | null }

const REFRESH_MS = 5 * 60_000 // the wire itself only changes every 2 h
const REGION_TABS = [{ value: 'all', label: 'All' }, ...REGIONS.map((r) => ({ value: r, label: REGION_LABEL[r] }))] as const

/** "/news": the latest AI market brief, per-asset sentiment and the stored headline wire, by region (?region=). */
export function NewsPage() {
  const [filter, setFilter] = useState('all') // 'all' | 'macro' | a universe symbol
  const [params, setParams] = useSearchParams()
  const region = regionParam(params.get('region')) // null = All; shareable and kept across reloads
  const [wire, setWire] = useState<Wire | null>(null)
  const [error, setError] = useState<string | null>(null)
  const seq = useRef(0) // only the newest load may write state

  async function load(f: string, r: NewsRegion | null) {
    const n = ++seq.current
    const q = newsQuery(f, r)
    try {
      const [b, all, list] = await Promise.all([
        api.brief(),
        api.news({ limit: 100 }),
        f === 'all' && !r ? null : api.news({ ...q, limit: f === 'all' ? 100 : 50 }),
      ])
      if (n !== seq.current) return
      const items = asNews(all?.news)
      setWire({ brief: asBrief(b?.brief), all: items, list: list ? asNews(list.news) : items, filter: f, region: r })
      setError(null)
    } catch (err) {
      if (n === seq.current) setError(errorMessage(err, "Couldn't load the market wire."))
    }
  }

  usePoll(() => load(filter, region), REFRESH_MS)

  // A region tab, the brief's breakdown or a shared link changes ?region= and reloads; usePoll does the first load.
  const loadedRegion = useRef(region)
  const reload = useEffectEvent(() => void load(filter, region))
  useEffect(() => {
    if (loadedRegion.current === region) return
    loadedRegion.current = region
    reload()
  }, [region])

  /** Chips and sentiment cells: picking the active asset again goes back to All. */
  function choose(f: string) {
    const next = f === filter && f !== 'all' ? 'all' : f
    setFilter(next)
    void load(next, region)
  }

  function pickRegion(r: NewsRegion | null) {
    const q = new URLSearchParams(params)
    if (r) q.set('region', r)
    else q.delete('region')
    setParams(q, { replace: true })
  }

  const byId = useMemo(() => citations(wire?.brief ?? null, wire?.all ?? [], wire?.list ?? []), [wire])

  if (!wire && error) {
    return (
      <div className="mx-auto max-w-xl px-4 py-20">
        <EmptyState
          icon={<IconAlert size={20} />}
          title="Couldn't load the market wire."
          description={error}
          action={<Button variant="secondary" onClick={() => load(filter, region)}>Try again</Button>}
        />
      </div>
    )
  }

  const listReady = wire?.filter === filter && wire.region === region
  const scope = wireScope(filter, region)
  const filterLabel = scope ? `${scope[0].toUpperCase()}${scope.slice(1)} headlines` : 'All headlines'

  return (
    <div className="mx-auto max-w-6xl px-4 py-8 md:px-8 md:py-10">
      <div className="mb-3 flex flex-wrap items-center justify-end gap-2">
        <FearGreedTile className="mr-auto" />
        <Link
          to="/signals"
          className="inline-flex h-8 items-center gap-1.5 rounded-full border border-border bg-surface px-3 text-[13px] font-medium text-text transition-colors hover:border-muted/45 hover:bg-surface-2"
        >
          <IconSignal size={15} />
          Signals board →
        </Link>
      </div>
      {wire ? (
        <BriefHero brief={wire.brief} headlines={wire.all.length > 0} byId={byId} onPick={choose} region={region} onRegion={pickRegion} />
      ) : (
        <HeroSkeleton />
      )}

      <section aria-labelledby="sentiment-title" className="mt-8">
        <div className="mb-3 flex items-baseline justify-between gap-3">
          <h2 id="sentiment-title" className="label">
            {wire && !wire.brief ? 'Sentiment · no brief right now' : 'Sentiment · from the latest brief'}
          </h2>
          {error && wire && (
            <span role="status" className="font-mono text-[11px] text-muted">
              Couldn't refresh · retrying
            </span>
          )}
        </div>
        <SentimentStrip sentiment={wire && !wire.brief ? null : (wire?.brief?.sentiment ?? [])} active={filter} onPick={choose} />
      </section>

      <section aria-labelledby="wire-title" className="mt-8">
        <h2 id="wire-title" className="sr-only">
          {filterLabel}
        </h2>
        <div className="-mx-4 mb-3 flex overflow-x-auto px-4 pb-1 sm:mx-0 sm:px-0">
          <Tabs
            aria-label="Filter by region"
            className="shrink-0"
            value={region ?? 'all'}
            onChange={(v) => pickRegion(v === 'all' ? null : v)}
            items={REGION_TABS}
          />
        </div>
        <div role="group" aria-label="Filter headlines" className="-mx-4 mb-4 flex gap-1.5 overflow-x-auto px-4 pb-1 sm:mx-0 sm:flex-wrap sm:px-0">
          <Chip active={filter === 'all'} onClick={() => choose('all')}>
            All
          </Chip>
          <Chip active={filter === 'macro'} onClick={() => choose('macro')}>
            Macro
          </Chip>
          {CORE.map((s) => (
            <Chip key={s} active={filter === s} onClick={() => choose(s)}>
              {s}
            </Chip>
          ))}
        </div>

        <Card className="overflow-hidden">
          {!listReady ? (
            error ? (
              <div className="px-6 py-12 text-center">
                <p className="text-[14px] text-muted">{error}</p>
                <Button variant="secondary" size="sm" className="mt-4" onClick={() => load(filter, region)}>
                  Try again
                </Button>
              </div>
            ) : (
              <ul aria-busy="true" aria-label="Loading headlines" className="divide-y divide-border">
                {[0, 1, 2, 3, 4].map((i) => (
                  <RowSkeleton key={i} />
                ))}
              </ul>
            )
          ) : wire.list.length === 0 ? (
            <div className="px-6 py-14 text-center">
              <p className="font-display text-xl text-text">
                {!scope
                  ? 'No headlines on the wire yet.'
                  : region
                    ? `No ${scope} headlines in the last 14 days.`
                    : `No ${scope} headlines right now.`}
              </p>
              <p className="mx-auto mt-2 max-w-md text-[13.5px] leading-relaxed text-muted">
                The wire refreshes every 2 hours and keeps two weeks of headlines.
              </p>
              {filter !== 'all' ? (
                <Button variant="secondary" size="sm" className="mt-4" onClick={() => choose('all')}>
                  {region ? `Show all ${REGION_LABEL[region]} headlines` : 'Show all headlines'}
                </Button>
              ) : (
                region && (
                  <Button variant="secondary" size="sm" className="mt-4" onClick={() => pickRegion(null)}>
                    Show every region
                  </Button>
                )
              )}
            </div>
          ) : (
            <ul className="divide-y divide-border">
              {wire.list.map((item) => (
                <Row key={item.id} item={item} onPick={choose} />
              ))}
            </ul>
          )}
        </Card>
      </section>
    </div>
  )
}

function BriefHero({
  brief,
  headlines,
  byId,
  onPick,
  region,
  onRegion,
}: {
  brief: MarketBrief | null
  headlines: boolean // the wire has items, whatever the brief's state
  byId: Map<string, Cited>
  onPick: (s: string) => void
  region: NewsRegion | null
  onRegion: (r: NewsRegion) => void
}) {
  if (!brief && headlines) {
    // No brief validates right now (the newest was rejected, or none is under a day old), but the wire is live.
    return (
      <section className="card border-t-2 border-t-accent px-6 py-8 md:px-10 md:py-10">
        <div className="label flex items-center gap-2">
          <IconNews size={14} /> Market wire · brief being rewritten
        </div>
        <h1 className="mt-3 font-display text-[34px] leading-[1.05] font-medium tracking-[-0.02em] text-text md:text-[46px]">No brief right now.</h1>
        <p className="mt-3 max-w-2xl text-[15px] leading-relaxed text-pretty text-muted">
          The latest headlines are below; a new brief is written from them every 2 hours.
        </p>
      </section>
    )
  }
  if (!brief) {
    return (
      <section className="card border-t-2 border-t-accent px-6 py-8 md:px-10 md:py-10">
        <div className="label flex items-center gap-2">
          <IconNews size={14} /> Market wire · no brief yet
        </div>
        <h1 className="mt-3 font-display text-[34px] leading-[1.05] font-medium tracking-[-0.02em] text-text md:text-[46px]">
          The wire is quiet.
        </h1>
        <p className="mt-3 max-w-2xl text-[15px] leading-relaxed text-pretty text-muted">
          Every 2 hours the desk pulls fresh headlines for each asset, the macro calendar and US and Asian markets, reads the
          key articles and writes a short brief. Nothing has come in yet, or market search is unavailable right now. Check
          back after the next refresh.
        </p>
      </section>
    )
  }

  return (
    <section aria-labelledby="brief-title" className="card border-t-2 border-t-accent px-6 py-7 md:px-10 md:py-9">
      {briefStale(brief) ? (
        <div className="label flex flex-wrap items-center gap-2">
          Market wire · brief from {timeAgo(brief.createdAt)}
          <Badge title="Over 6 hours old: the scheduled rewrites have not replaced it yet">stale</Badge>
        </div>
      ) : (
        <div className="label flex items-center gap-2">
          <span aria-hidden="true" className="size-1.5 animate-pulse rounded-full bg-accent" />
          Market wire · updated {timeAgo(brief.createdAt)}
        </div>
      )}
      <h1
        id="brief-title"
        className="mt-3 max-w-4xl font-display text-[30px] leading-[1.08] font-medium tracking-[-0.02em] text-balance text-text md:text-[44px]"
      >
        {brief.headline}
      </h1>
      {brief.summary && <p className="mt-3.5 max-w-3xl text-[15.5px] leading-relaxed text-pretty text-muted">{brief.summary}</p>}

      {brief.bullets.length > 0 && (
        <ol className="mt-7 grid gap-x-10 gap-y-5 border-t border-border pt-6 md:grid-cols-2">
          {brief.bullets.map((b, i) => {
            const sources = resolveSources(b.sources, byId)
            return (
              <li key={i} className="flex min-w-0 gap-3.5">
                <span className="pt-0.5 font-mono text-[11px] font-medium text-accent-ink tabular-nums">{String(i + 1).padStart(2, '0')}</span>
                <div className="min-w-0">
                  <p className="text-[14.5px] leading-relaxed text-text">{b.text}</p>
                  {(b.symbols.length > 0 || sources.length > 0) && (
                    <div className="mt-2 flex flex-wrap items-center gap-x-2.5 gap-y-1.5">
                      {b.symbols.map((s) => (
                        <AssetChip key={s} symbol={s} onClick={() => onPick(s)} />
                      ))}
                      {sources.map((n) => (
                        <a
                          key={n.id}
                          href={n.url}
                          target="_blank"
                          rel="noopener noreferrer"
                          title={n.title}
                          className="inline-flex items-center gap-0.5 font-mono text-[11px] text-muted underline decoration-border underline-offset-2 hover:text-accent-ink hover:decoration-current"
                        >
                          {n.source}
                          <IconExternal size={11} />
                        </a>
                      ))}
                    </div>
                  )}
                </div>
              </li>
            )
          })}
        </ol>
      )}

      {brief.regions && brief.regions.length > 0 && (
        <ul aria-label="Brief by region" className="mt-6 grid gap-x-8 gap-y-4 border-t border-border pt-5 sm:grid-cols-2 lg:grid-cols-4">
          {brief.regions.map((r) => (
            <li key={r.region} className="min-w-0">
              <button
                type="button"
                aria-pressed={region === r.region}
                onClick={() => onRegion(r.region)}
                title={`Show ${REGION_LABEL[r.region]} headlines`}
                className={cx(
                  'label transition-colors hover:text-accent-ink',
                  region === r.region && 'text-accent-ink underline decoration-current underline-offset-4',
                )}
              >
                {REGION_LABEL[r.region]}
              </button>
              <p title={r.summary} className="mt-1 line-clamp-3 text-[13.5px] leading-relaxed text-pretty text-muted">
                {r.summary}
              </p>
            </li>
          ))}
        </ul>
      )}

      <div className="mt-7 flex flex-wrap items-center justify-between gap-x-6 gap-y-2 border-t border-border pt-4">
        <p className="font-mono text-[11px] text-muted tabular-nums">
          AI summary of {brief.articleCount} headline{brief.articleCount === 1 ? '' : 's'} · refreshes every 2 hours
        </p>
        <Disclaimer />
      </div>
    </section>
  )
}

function Row({ item, onPick }: { item: NewsItem; onPick: (s: string) => void }) {
  return (
    <li className="flex gap-4 px-5 py-4">
      <span
        aria-hidden="true"
        className="mt-0.5 grid size-8 shrink-0 place-items-center rounded-lg border border-border bg-surface-2 font-mono text-[12px] font-semibold text-muted"
      >
        {monogram(item.source, item.url)}
      </span>
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1 font-mono text-[11px] text-muted">
          <span className="max-w-[16rem] truncate text-text/80">{item.source}</span>
          {item.publishedAt && (
            <>
              <span aria-hidden="true">·</span>
              <time dateTime={item.publishedAt} title={fmtDate(item.publishedAt, { time: true })}>
                {timeAgo(item.publishedAt)}
              </time>
            </>
          )}
          {item.topic === 'macro' && <Badge tone="outline">Macro</Badge>}
          {item.regions.map((r) => (
            <Badge key={r}>{REGION_LABEL[r]}</Badge>
          ))}
          {item.hasContent && (
            <Badge tone="accent" title="The desk read the full article">
              Read
            </Badge>
          )}
        </div>
        <h3 className="mt-1 font-display text-[17px] leading-snug text-pretty">
          <a
            href={item.url}
            target="_blank"
            rel="noopener noreferrer"
            className="text-text decoration-accent/50 underline-offset-[3px] hover:text-accent-ink hover:underline"
          >
            {item.title}
            <IconExternal size={13} className="ml-1 inline-block align-[-1px] text-muted" />
          </a>
        </h3>
        {item.description && <p className="mt-1 line-clamp-2 text-[13.5px] leading-relaxed text-muted">{item.description}</p>}
        {item.symbols.length > 0 && (
          <div className="mt-2.5 flex flex-wrap gap-1.5">
            {item.symbols.map((s) => (
              <AssetChip key={s} symbol={s} onClick={() => onPick(s)} />
            ))}
          </div>
        )}
      </div>
    </li>
  )
}

function HeroSkeleton() {
  return (
    <div aria-busy="true" aria-label="Loading the market brief" className="card border-t-2 border-t-accent px-6 py-8 md:px-10 md:py-10">
      <Skeleton className="h-3 w-48" />
      <Skeleton className="mt-4 h-10 w-4/5" />
      <Skeleton className="mt-3 h-4 w-3/5" />
      <div className="mt-8 grid gap-5 md:grid-cols-2">
        {[0, 1, 2, 3].map((i) => (
          <Skeleton key={i} className="h-12 w-full" />
        ))}
      </div>
    </div>
  )
}

function RowSkeleton() {
  return (
    <li className="flex gap-4 px-5 py-4">
      <Skeleton className="size-8 rounded-lg" />
      <div className="min-w-0 flex-1">
        <Skeleton className="h-3 w-40" />
        <Skeleton className="mt-2.5 h-5 w-4/5" />
        <Skeleton className="mt-2 h-3.5 w-full" />
      </div>
    </li>
  )
}

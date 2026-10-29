// Trade terminal "News" tab: stored headlines for the symbol plus the wire brief's sentiment read on it.
import { useEffect, useRef, useState } from 'react'
import { Badge } from '../../components/ui/Badge'
import { Button } from '../../components/ui/Button'
import { EmptyState } from '../../components/ui/EmptyState'
import { IconArrowRight } from '../../components/ui/icons'
import { Skeleton } from '../../components/ui/Spinner'
import { api } from '../../lib/api'
import { errorMessage } from '../../lib/errors'
import { fmtDate, timeAgo } from '../../lib/format'
import type { MarketBrief, NewsItem, Sentiment } from '../../lib/types'
import { usePoll } from '../../lib/usePoll'
import { linkable, sentimentFor } from './util'

const REFRESH_MS = 5 * 60_000

/** Mount with `key={symbol}`: a symbol switch remounts, which aborts the old symbol's requests and starts clean. */
export function NewsFeed({ symbol }: { symbol: string }) {
  const [news, setNews] = useState<NewsItem[] | null>(null)
  const [brief, setBrief] = useState<MarketBrief | null>(null)
  const [error, setError] = useState('')
  const ctrl = useRef<AbortController | null>(null)

  const load = () => {
    ctrl.current?.abort() // a slow answer must never land on top of a newer one
    const c = (ctrl.current = new AbortController())
    api
      .news({ symbol, limit: 20 }, c.signal)
      .then((r) => {
        setNews(linkable(r.news))
        setError('')
      })
      .catch((err) => {
        if (!c.signal.aborted) setError(errorMessage(err, 'Could not load the news.'))
      })
    // Sentiment is garnish: if the brief fails, the headlines still show.
    api
      .brief(c.signal)
      .then((r) => setBrief(r.brief ?? null))
      .catch(() => {})
  }
  usePoll(load, REFRESH_MS)
  useEffect(() => () => ctrl.current?.abort(), [])

  const sentiment = sentimentFor(brief, symbol)

  return (
    <div>
      {sentiment && <SentimentRow s={sentiment} at={brief?.createdAt} />}
      {!news && error ? (
        <div role="alert" className="flex flex-wrap items-center justify-between gap-3 p-4 text-sm text-muted">
          {error}
          <Button variant="secondary" size="sm" onClick={load}>
            Try again
          </Button>
        </div>
      ) : news && news.length === 0 ? (
        <div className="p-4">
          <EmptyState
            title={`No ${symbol} headlines yet`}
            description="The news wire refreshes every two hours. Check back soon, or ask the Agent to search the web."
          />
        </div>
      ) : (
        <ul aria-label={`${symbol} news`} aria-busy={!news} className="relative divide-y divide-border lg:max-h-[30rem] lg:overflow-y-auto">
          {!news
            ? Array.from({ length: 4 }, (_, i) => (
                <li key={i} className="px-4 py-3">
                  <Skeleton className="h-3 w-32" />
                  <Skeleton className="mt-2 h-4 w-11/12" />
                </li>
              ))
            : news.map((n) => (
                <li key={n.id || n.url}>
                  <a
                    href={n.url}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="group block px-4 py-3 transition-colors hover:bg-surface-2/60"
                  >
                    <span className="flex flex-wrap gap-x-1.5 font-mono text-[11px] text-muted">
                      {n.source && <span className="max-w-[16rem] truncate">{n.source}</span>}
                      {n.publishedAt && (
                        <span title={fmtDate(n.publishedAt, { time: true })}>
                          {n.source && '· '}
                          {timeAgo(n.publishedAt)}
                        </span>
                      )}
                    </span>
                    <span className="mt-0.5 block text-[13.5px] leading-snug text-pretty text-text group-hover:text-accent-ink">
                      {n.title}
                      <IconArrowRight size={11} className="ml-1 inline -rotate-45 align-baseline text-muted" />
                      <span className="sr-only"> (opens in a new tab)</span>
                    </span>
                  </a>
                </li>
              ))}
        </ul>
      )}
    </div>
  )
}

const MOOD = {
  bullish: { label: 'Bullish', glyph: '▲', tone: 'up' },
  bearish: { label: 'Bearish', glyph: '▼', tone: 'down' },
  neutral: { label: 'Neutral', glyph: '■', tone: 'neutral' },
} as const

function SentimentRow({ s, at }: { s: Sentiment; at?: string }) {
  const mood = MOOD[s.label] ?? MOOD.neutral
  const score = Number.isFinite(s.score) ? `${s.score > 0 ? '+' : ''}${s.score.toFixed(2)}` : null
  return (
    <div className="flex flex-wrap items-start gap-x-3 gap-y-1.5 border-b border-border bg-surface-2/50 px-4 py-3">
      <span className="label pt-0.5">{s.symbol} sentiment</span>
      <Badge tone={mood.tone} title={score ? `Score ${score} on a −1 to +1 scale` : undefined}>
        <span aria-hidden="true">{mood.glyph}</span> {mood.label}
        {score && <span className="opacity-70">{score}</span>}
      </Badge>
      {at && <span className="ml-auto font-mono text-[11px] text-muted">Wire brief · {timeAgo(at)}</span>}
      {s.reason && <p className="w-full text-[13px] leading-snug text-muted">{s.reason}</p>}
    </div>
  )
}

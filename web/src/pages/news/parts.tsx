// Market wire building blocks, shared by the News page and the agent's empty state.
import { useState, type ReactNode } from 'react'
import { AssetIcon } from '../../components/ui/AssetIcon'
import { api } from '../../lib/api'
import { cx } from '../../lib/cx'
import type { MarketPulse, Sentiment } from '../../lib/types'
import { usePoll } from '../../lib/usePoll'
import { CORE, scoreBar } from './news'

export const TONE = {
  bullish: { glyph: '▲', text: 'text-up', bar: 'bg-up' },
  bearish: { glyph: '▼', text: 'text-down', bar: 'bg-down' },
  neutral: { glyph: '—', text: 'text-muted', bar: 'bg-muted' },
} as const

/**
 * The 12 core assets with the brief's call (▲/▼/— and a diverging score bar); a cell filters the headlines. `sentiment`
 * null means there is no brief right now, so a cell says "no brief" rather than "no news".
 */
export function SentimentStrip({
  sentiment,
  active,
  onPick,
  className,
}: {
  sentiment: Sentiment[] | null
  active: string
  onPick: (symbol: string) => void
  className?: string
}) {
  const bySymbol = new Map((sentiment ?? []).map((s) => [s.symbol, s]))
  const none = sentiment ? { word: 'no news', tip: 'No headlines for this asset in the latest brief' } : { word: 'no brief', tip: 'No brief right now' }
  return (
    <ul
      aria-label="Sentiment by asset"
      className={cx('grid grid-cols-3 gap-px overflow-hidden rounded-[14px] border border-border bg-border sm:grid-cols-4 lg:grid-cols-6', className)}
    >
      {CORE.map((sym) => {
        const s = bySymbol.get(sym)
        const tone = TONE[s?.label ?? 'neutral']
        const bar = scoreBar(s?.score ?? 0)
        const on = active === sym
        return (
          <li key={sym} className="min-w-0">
            <button
              type="button"
              aria-pressed={on}
              onClick={() => onPick(sym)}
              title={s?.reason || none.tip}
              className={cx(
                'flex w-full flex-col gap-2 px-3 py-2.5 text-left transition-colors',
                on ? 'bg-accent-soft' : 'bg-surface hover:bg-surface-2',
              )}
            >
              <span className="flex min-w-0 items-center gap-1.5">
                <AssetIcon symbol={sym} size={18} />
                <span className="font-mono text-[12px] font-medium text-text">{sym}</span>
                <span className={cx('ml-auto flex items-center gap-1 font-mono text-[11px]', s ? tone.text : 'text-muted/70')}>
                  <span aria-hidden="true">{s ? tone.glyph : '·'}</span>
                  <span className={s ? 'sr-only sm:not-sr-only' : 'sr-only'}>{s ? s.label : none.word}</span>
                </span>
              </span>
              <span aria-hidden="true" className="relative block h-1 rounded-full bg-surface-2">
                <span className="absolute top-[-2px] left-1/2 h-2 w-px bg-border" />
                {s && <span className={cx('absolute top-0 h-full rounded-full', tone.bar)} style={{ left: `${bar.left}%`, width: `${bar.width}%` }} />}
              </span>
            </button>
          </li>
        )
      })}
    </ul>
  )
}

/** Compact read-only version: "BTC ▲ ETH ▼ …" for the assets the brief covers. */
export function MiniSentiment({ sentiment, className }: { sentiment: Sentiment[]; className?: string }) {
  return (
    <ul aria-label="Sentiment by asset" className={cx('flex flex-wrap gap-1', className)}>
      {sentiment.map((s) => (
        <li key={s.symbol} className="inline-flex h-6 items-center gap-1 rounded-md bg-surface-2 px-1.5 font-mono text-[11px]">
          <span className="text-text">{s.symbol}</span>
          <span aria-hidden="true" className={TONE[s.label].text}>
            {TONE[s.label].glyph}
          </span>
          <span className="sr-only">{s.label}</span>
        </li>
      ))}
    </ul>
  )
}

/** Small asset pill; clicking it filters the wire to that asset. */
export function AssetChip({ symbol, onClick }: { symbol: string; onClick: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      title={`Show ${symbol} headlines`}
      className="inline-flex h-6 items-center gap-1 rounded-full border border-border bg-surface pr-2 pl-0.5 font-mono text-[11px] font-medium text-text transition-colors hover:border-muted/45 hover:bg-surface-2"
    >
      <AssetIcon symbol={symbol} size={18} />
      {symbol}
    </button>
  )
}

export function Chip({ active, onClick, children }: { active: boolean; onClick: () => void; children: ReactNode }) {
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onClick}
      className={cx(
        'inline-flex h-8 shrink-0 items-center gap-1.5 rounded-full border px-3 text-[13px] font-medium transition-colors',
        active ? 'border-ink bg-ink text-ink-fg' : 'border-border bg-surface text-muted hover:border-muted/45 hover:text-text',
      )}
    >
      {children}
    </button>
  )
}

/**
 * "Fear & Greed 71 · Greed" with a 0–100 meter, in neutral tones (green and red mean gains and losses here).
 * Hidden while the index is unknown or the request fails.
 */
export function FearGreedTile({ className }: { className?: string }) {
  const [fg, setFg] = useState<MarketPulse['fearGreed']>(null)
  usePoll(() => api.getPulse().then((r) => setFg(r?.fearGreed ?? null), () => setFg(null)), 60 * 60_000) // the index moves daily
  if (!fg || !Number.isFinite(fg.value)) return null
  const v = Math.round(Math.min(100, Math.max(0, fg.value)))
  return (
    <span
      title="Crypto Fear & Greed Index by alternative.me, updated daily"
      className={cx('inline-flex h-7 items-center gap-2 rounded-full border border-border bg-surface px-2.5 font-mono text-[11.5px] tabular-nums', className)}
    >
      <span className="text-muted">
        Fear &amp; Greed <span className="font-medium text-text">{v}</span> · {fg.label}
      </span>
      <span aria-hidden="true" className="relative h-1 w-12 overflow-hidden rounded-full bg-surface-2">
        <span className="absolute inset-y-0 left-0 rounded-full bg-accent" style={{ width: `${v}%` }} />
      </span>
    </span>
  )
}

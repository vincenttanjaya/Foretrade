import { useState } from 'react'
import { Link } from 'react-router'
import { AssetIcon } from '../../components/ui/AssetIcon'
import { IconArrowRight, IconNews } from '../../components/ui/icons'
import { PriceChange } from '../../components/ui/PriceChange'
import { Skeleton } from '../../components/ui/Spinner'
import { Tabs } from '../../components/ui/Tabs'
import { api } from '../../lib/api'
import { useAssets } from '../../lib/assets'
import { isListedIn, useInstruments } from '../../lib/instruments'
import { fmtPct, fmtPrice, timeAgo } from '../../lib/format'
import type { Asset, ChatAttachmentInput, ChatLens, ChatMode, MarketBrief } from '../../lib/types'
import { usePoll } from '../../lib/usePoll'
import { asBrief } from '../news/news'
import { FearGreedTile, MiniSentiment } from '../news/parts'
import { marketGroup, SYMBOLS } from '../strategies/strategy'
import { Composer } from './Composer'

// Each set nudges a different agent tool: market overview, analysis, price outlook, portfolio, trade proposal.
const PROMPTS = {
  Trending: [
    "What's moving in crypto today? Show gainers and losers in a table.",
    'Compare BTC, ETH and SOL momentum in a table',
    'Is BTC overbought right now? Check RSI and trend on the 4h.',
    'Give me a 60-second market brief to start the day.',
  ],
  Crypto: [
    "What's the 2-week price outlook for ETH?",
    "Where are SOL's key support and resistance levels on the 4h?",
    'Is the DOGE move running out of steam?',
    'Compare LINK and AVAX on trend, volatility and RSI.',
  ],
  Macro: [
    'How is gold (PAXG) holding up against BTC this month?',
    'In a risk-off week, which majors have held up best over 30 days?',
    "What does BTC's realized volatility imply for the next 7 days?",
    'Should I rotate some BTC into gold to cut portfolio volatility?',
  ],
  'Trade ideas': [
    "I want to buy $2,000 of SOL — what's a sensible stop?",
    'How is my paper portfolio positioned?',
    'Buy $500 of ETH',
    'Find me a short setup with a tight stop and a 2:1 target.',
  ],
  Portfolio: ["How's my portfolio today?", 'Review my last trade', 'Review my trades and suggest improvements', 'How are my automations doing?', 'Where are my stops?'],
} as const

// Nudge the research tools (search_news, web_search, read_page, get_market_wire); answers cite [n] sources.
const NEWS_PROMPTS = [
  'What moved crypto today? Cite sources.',
  'Summarize the latest Fed news and what it means for BTC',
  'Any regulatory headlines I should know about?',
]

type Topic = keyof typeof PROMPTS
const TOPICS = Object.keys(PROMPTS) as Topic[]
const TICKERS = ['BTC', 'ETH', 'SOL', 'PAXG']

/** The day's top 3 gainers and the 3 weakest of the rest, by 24h change. */
/**
 * The day's top 3 gainers and 3 weakest of the named instruments: a listed Asian stock is left out, as the agent's
 * market overview leaves it out (it quotes one but makes no trade card for it yet).
 */
function movers(assets: Asset[]) {
  const sorted = [...assets].sort((a, b) => b.change24hPct - a.change24hPct)
  return [...sorted.slice(0, 3), ...sorted.slice(3).slice(-3)]
}

/** Empty chat: headline, composer, prompt ideas by topic and a live mini-ticker. */
export function Welcome({ onSend, onStop, streaming, mode, onModeChange, lens, onLensChange }: {
  onSend: (text: string, attachments?: ChatAttachmentInput[]) => void
  onStop: () => void
  streaming: boolean
  mode: ChatMode
  onModeChange: (mode: ChatMode) => void
  lens: ChatLens
  onLensChange: (lens: ChatLens) => void
}) {
  const [topic, setTopic] = useState<Topic>('Trending')
  const [brief, setBrief] = useState<MarketBrief | null>(null)
  const { assets, bySymbol } = useAssets()
  const catalogue = useInstruments()
  // The catalogue's count once it is in (named plus the listed Asian index stocks); SYMBOLS against an API without it.
  const coverage = catalogue.rows.length
    ? `${catalogue.rows.length} instruments, ${catalogue.listed.length} of them Asian index stocks by Yahoo ticker`
    : `${SYMBOLS.length} instruments`
  const perpsOnly = assets.filter((a) => a.venue !== 'yahoo')
  const counts = {
    crypto: perpsOnly.filter((a) => marketGroup(a) === 'crypto').length,
    stock: perpsOnly.filter((a) => a.class === 'stock' && marketGroup(a) === 'us').length,
    index: perpsOnly.filter((a) => a.class === 'index' && marketGroup(a) === 'us').length,
    asia: perpsOnly.filter((a) => marketGroup(a) === 'asia').length,
    feed: assets.length - perpsOnly.length, // quote-feed stocks (USD, market hours)
    // priced by a real-time source (KRX, HKEX, HOSE, TWSE) or SGX's snapshot: not all of them delayed then
    fresh: assets.filter((a) => a.quoteFeed?.class === 'realtime' || a.quoteFeed?.class === 'near-realtime').length,
  }
  const feedNote = counts.fresh ? 'market hours; real-time or delayed, as each price says' : 'market hours, delayed'
  const perps = (n: number) => (n === 1 ? 'perp' : 'perps')

  // The wire card is a bonus: if there's no brief or the request fails, it simply isn't shown.
  usePoll(() => api.brief().then((r) => setBrief(asBrief(r?.brief)), () => {}), 5 * 60_000)

  return (
    <div className="mx-auto flex min-h-full w-full max-w-3xl flex-col justify-center px-4 py-8 md:py-14">
      <div className="label flex items-center justify-center gap-2">
        <span aria-hidden="true" className="size-1.5 animate-pulse rounded-full bg-accent" />
        AI desk · live market data
      </div>
      <h1 className="mt-3.5 text-center font-display text-[34px] leading-[1.05] font-medium tracking-[-0.02em] text-balance text-text md:text-[50px]">
        Which market is on your mind?
      </h1>
      <p className="mx-auto mt-3 hidden max-w-lg text-center text-[15px] leading-relaxed text-pretty text-muted sm:block">
        Research in plain language. Every idea can end in a paper trade you confirm.
      </p>
      <p className="mx-auto mt-2 max-w-lg text-center text-[12.5px] leading-relaxed text-pretty text-muted">
        {assets.length
          ? `Live quotes: ${counts.crypto} crypto pairs · ${counts.stock} US stock ${perps(counts.stock)} · ${counts.index} index ${perps(counts.index)} · ${counts.asia} Asia stock & ETF ${perps(counts.asia)}${counts.feed ? ` · ${counts.feed} US and Asia quote-feed ${counts.feed === 1 ? 'stock' : 'stocks'} (${feedNote})` : ''}.`
          : 'Loading crypto, US and Asia stock, ETF and index perp quotes.'}
        {' '}Configured coverage: {coverage}; live availability can vary. Research and forecasts can be about anything.
      </p>

      <Composer className="mt-7 md:mt-9" onSend={onSend} onStop={onStop} streaming={streaming} mode={mode} onModeChange={onModeChange} lens={lens} onLensChange={onLensChange} autoFocus />

      <div className="mt-7 flex justify-center">
        <Tabs size="sm" value={topic} onChange={setTopic} items={TOPICS} aria-label="Prompt ideas" className="max-w-full overflow-x-auto" />
      </div>
      <ul className="mt-3.5 grid gap-2 sm:grid-cols-2">
        {PROMPTS[topic].map((p) => (
          <li key={p}>
            <button
              type="button"
              onClick={() => onSend(p)}
              disabled={streaming}
              className="group flex h-full min-h-[58px] w-full items-center gap-3 rounded-xl border border-border bg-surface px-4 py-3 text-left text-[13.5px] leading-snug text-text transition-colors duration-150 hover:border-accent/50 hover:bg-accent-soft/40 disabled:opacity-50"
            >
              <span className="flex-1">{p}</span>
              <IconArrowRight size={15} className="shrink-0 text-muted transition-[color,transform] group-hover:translate-x-0.5 group-hover:text-accent-ink" />
            </button>
          </li>
        ))}
      </ul>

      <ul aria-label="Live prices" className="mt-6 flex flex-wrap justify-center gap-2">
        {TICKERS.map((sym) => {
          const a = bySymbol[sym]
          return (
            <li key={sym}>
              <button
                type="button"
                onClick={() => onSend(`What's the setup on ${sym} right now?`)}
                disabled={streaming}
                title={`Ask about ${sym}`}
                className="inline-flex h-8 items-center gap-2 rounded-full border border-border bg-surface pr-3 pl-1.5 font-mono text-[12px] tabular-nums transition-colors hover:bg-surface-2 disabled:opacity-50"
              >
                <AssetIcon symbol={sym} size={20} />
                <span className="font-medium text-text">{sym}</span>
                {a ? (
                  <>
                    <span className="text-text">{fmtPrice(a.price)}</span>
                    <PriceChange value={a.change24hPct} decimals={1} className="text-[11px]" />
                  </>
                ) : (
                  <Skeleton className="h-3 w-24" />
                )}
              </button>
            </li>
          )
        })}
      </ul>

      {assets.length > 0 && (
        <div className="mt-4 flex flex-wrap items-center justify-center gap-2">
          <span className="label">Market now</span>
          <ul aria-label="Market now" className="flex flex-wrap justify-center gap-2">
            {movers(assets.filter((a) => !isListedIn(catalogue, a.symbol))).map((a) => {
              const move = fmtPct(a.change24hPct, { sign: true, decimals: 1 })
              return (
                <li key={a.symbol}>
                  <button
                    type="button"
                    onClick={() => onSend(`What's the setup on ${a.symbol} after today's ${move} move?`)}
                    disabled={streaming}
                    className="inline-flex h-7 items-center gap-1.5 rounded-full border border-border bg-surface px-2.5 font-mono text-[11.5px] tabular-nums transition-colors hover:bg-surface-2 disabled:opacity-50"
                  >
                    <span className="font-medium text-text">{a.symbol}</span>
                    <span className={a.change24hPct >= 0 ? 'text-up' : 'text-down'}>{move}</span>
                  </button>
                </li>
              )
            })}
          </ul>
          <FearGreedTile />
        </div>
      )}

      <section aria-label="From the market wire" className="mt-8">
        {brief && <WireCard brief={brief} />}
        <ul aria-label="Ask about the news" className="mt-3 flex flex-wrap justify-center gap-2">
          {NEWS_PROMPTS.map((p) => (
            <li key={p}>
              <button
                type="button"
                onClick={() => onSend(p)}
                disabled={streaming}
                className="inline-flex min-h-8 items-center gap-1.5 rounded-full border border-border bg-surface px-3 py-1.5 text-left text-[12.5px] leading-snug text-text transition-colors hover:border-accent/50 hover:bg-accent-soft/40 disabled:opacity-50"
              >
                <IconNews size={14} className="shrink-0 text-muted" />
                {p}
              </button>
            </li>
          ))}
        </ul>
      </section>
    </div>
  )
}

/** Compact latest brief: headline, three bullets and the sentiment row; the whole card opens /news. */
function WireCard({ brief }: { brief: MarketBrief }) {
  return (
    <div className="card group relative px-4 py-3.5 transition-colors hover:border-muted/45 sm:px-5 sm:py-4">
      <div className="flex items-center justify-between gap-3">
        <span className="label flex items-center gap-1.5">
          <span aria-hidden="true" className="size-1.5 rounded-full bg-accent" />
          Market wire · updated {timeAgo(brief.createdAt)}
        </span>
        <span aria-hidden="true" className="inline-flex shrink-0 items-center gap-1 text-[12.5px] font-medium whitespace-nowrap text-accent-ink">
          Open wire <IconArrowRight size={13} className="transition-transform group-hover:translate-x-0.5" />
        </span>
      </div>
      <h2 className="mt-2 font-display text-[18px] leading-snug font-medium text-pretty text-text">
        {/* Stretched link: the card is one target, named by the headline. */}
        <Link to="/news" className="after:absolute after:inset-0 after:rounded-[14px] after:content-['']">
          {brief.headline}
        </Link>
      </h2>
      {brief.bullets.length > 0 && (
        <ul className="mt-2 space-y-1">
          {brief.bullets.slice(0, 3).map((b, i) => (
            <li key={i} className="flex gap-2 text-[13px] leading-snug text-muted">
              <span aria-hidden="true" className="mt-[7px] size-1 shrink-0 rounded-full bg-accent" />
              <span className="line-clamp-2">{b.text}</span>
            </li>
          ))}
        </ul>
      )}
      {brief.sentiment.length > 0 && <MiniSentiment className="mt-3" sentiment={brief.sentiment} />}
    </div>
  )
}

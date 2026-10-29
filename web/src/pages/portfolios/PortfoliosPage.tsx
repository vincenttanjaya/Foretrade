import { useRef, useState } from 'react'
import { Link, useNavigate } from 'react-router'
import { useToast } from '../../components/toast'
import { AssetIcon } from '../../components/ui/AssetIcon'
import { Badge } from '../../components/ui/Badge'
import { Button } from '../../components/ui/Button'
import { EmptyState } from '../../components/ui/EmptyState'
import { IconAlert, IconForecast, IconPortfolio } from '../../components/ui/icons'
import { PriceChange } from '../../components/ui/PriceChange'
import { Sparkline } from '../../components/ui/Sparkline'
import { Skeleton } from '../../components/ui/Spinner'
import { api } from '../../lib/api'
import { useAuth } from '../../lib/auth'
import { cx } from '../../lib/cx'
import { fmtPct, fmtUsd, timeAgo } from '../../lib/format'
import type { AIPortfolio } from '../../lib/types'
import { ExampleChips, PromptCard, PromptStatus, useAlive, useLoad, type Stage } from '../strategies/parts'
import { errorMessage } from '../../lib/errors'
import { PALETTE } from '../strategies/strategy'
import { comparison, measuredWindow, shares } from './portfolio'

const EXAMPLES = [
  'Layer-1s with less drawdown than BTC',
  'Gold-hedged crypto core',
  'High-beta altcoin basket for a bull run',
  'Equal-weight top 5 by liquidity',
]

/** The quick start that builds from the user's own forecasts (the API offers them to the model as F1..F6). */
const DESK_PROMPT = 'Build a basket from what my forecast desk is most bullish on, equal-weighted, and skip anything it leans against.'

const STAGES: Stage[] = [
  { at: 0, text: 'Screening every asset on available daily return, volatility and drawdown…' },
  { at: 6, text: 'Choosing 3–6 holdings and their weights…' },
  { at: 13, text: 'Backtesting buy-and-hold over common dates against the selected benchmark…' },
]

const MIN_PROMPT = 3
const MAX_PROMPT = 500

/** /portfolios — one-sentence builder plus every portfolio built so far. */
export function PortfoliosPage() {
  const navigate = useNavigate()
  const { toast } = useToast()
  const { setCredits, refresh } = useAuth()
  const alive = useAlive()
  const box = useRef<HTMLTextAreaElement>(null)
  const [prompt, setPrompt] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const list = useLoad((signal) => api.aiPortfolios(signal).then((r) => r.portfolios ?? []))

  function pick(text: string) {
    setPrompt(text)
    setError(null)
    box.current?.focus()
  }

  async function build() {
    const text = prompt.trim()
    if (text.length < MIN_PROMPT || busy) return
    setBusy(true)
    setError(null)
    try {
      const res = await api.buildPortfolio(text)
      if (res.credits) setCredits(res.credits)
      if (alive.current) navigate(`/portfolios/${res.portfolio.id}`, { state: { portfolio: res.portfolio } })
      else toast(`"${res.portfolio.name}" is ready under AI portfolios.`)
    } catch (err) {
      if (alive.current) setError(errorMessage(err))
      else toast(errorMessage(err, 'Portfolio build failed.'), 'error')
      refresh().catch(() => {}) // credits may have been refunded or exhausted
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="mx-auto max-w-6xl px-4 py-8 md:px-8 md:py-10">
      <header className="max-w-3xl">
        <div className="label">AI portfolios · one sentence in, a basket out</div>
        <h1 className="mt-3 font-display text-[38px] leading-[1.04] font-medium tracking-[-0.02em] text-balance text-text md:text-[56px]">
          Describe a portfolio in one sentence.
        </h1>
        <p className="mt-4 max-w-2xl text-[16px] leading-relaxed text-pretty text-muted">
          The AI picks 3–6 holdings from the asset universe, weights them and explains each choice. The engine then
          backtests on up to 180 closed daily bars. Stock-majority baskets compare with S&P 500 perp; other baskets compare with BTC. The saved result discloses its actual common window. Invest on paper in one tap.
        </p>
      </header>

      <div className="mt-8">
        <PromptCard
          inputRef={box}
          label="Describe your portfolio"
          value={prompt}
          onChange={setPrompt}
          onSubmit={build}
          busy={busy}
          min={MIN_PROMPT}
          max={MAX_PROMPT}
          placeholder="e.g. Layer-1s with less drawdown than BTC"
          submitLabel="Build portfolio"
          busyLabel="Building…"
        />
        <PromptStatus error={error} busy={busy} stages={STAGES} />
      </div>

      <section aria-labelledby="portfolio-examples" className="mt-4">
        <h2 id="portfolio-examples" className="label mb-3">
          Try an example
        </h2>
        <button
          type="button"
          disabled={busy}
          onClick={() => pick(DESK_PROMPT)}
          aria-pressed={prompt.trim() === DESK_PROMPT}
          title={DESK_PROMPT}
          className={cx(
            'mb-2 inline-flex items-center gap-1.5 rounded-full border px-3.5 py-1.5 text-[13px] leading-snug text-accent-ink transition-colors disabled:opacity-55',
            prompt.trim() === DESK_PROMPT ? 'border-accent bg-accent-soft' : 'border-accent/40 bg-surface hover:bg-accent-soft/40',
          )}
        >
          <IconForecast size={14} />
          From my forecast desk
        </button>
        <ExampleChips items={EXAMPLES} selected={prompt} disabled={busy} onPick={pick} />
      </section>

      <section aria-labelledby="my-portfolios" className="mt-14">
        <div className="mb-4 flex items-baseline justify-between gap-4">
          <h2 id="my-portfolios" className="font-display text-[26px] leading-tight font-medium text-text">
            Your portfolios
          </h2>
          {list.data && list.data.length > 0 && <span className="font-mono text-[11px] text-muted">{list.data.length} built</span>}
        </div>
        {!list.data && list.error ? (
          <EmptyState
            icon={<IconAlert size={20} />}
            title="Couldn't load your portfolios."
            description={errorMessage(list.error)}
            action={<Button variant="secondary" onClick={list.retry}>Try again</Button>}
          />
        ) : list.data?.length === 0 ? (
          <EmptyState
            icon={<IconPortfolio size={20} />}
            title="No portfolios yet."
            description="Describe one above or pick an example. Every basket you build is kept here with its backtest."
          />
        ) : (
          <ul aria-busy={!list.data} className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            {list.data ? list.data.map((p) => <PortfolioCard key={p.id} p={p} />) : [0, 1, 2].map((i) => <CardSkeleton key={i} />)}
          </ul>
        )}
      </section>
    </div>
  )
}

function PortfolioCard({ p }: { p: AIPortfolio }) {
  const holdings = p.holdings ?? []
  const w = shares(holdings)
  const bt = p.backtest
  const deskDriven = holdings.some((h) => h.driver?.kind === 'forecast')
  return (
    <li>
      <Link
        to={`/portfolios/${p.id}`}
        state={{ portfolio: p }}
        className="card group flex h-full flex-col p-5 transition-[border-color,box-shadow] hover:border-accent/50 hover:shadow-[0_8px_24px_-16px_rgb(15_27_45/0.35)]"
      >
        <div className="flex items-center justify-between gap-3">
          <span className="flex">
            {holdings.slice(0, 6).map((h, i) => (
              <AssetIcon key={h.symbol} symbol={h.symbol} size={26} className={i ? '-ml-2 ring-2 ring-surface' : 'ring-2 ring-surface'} />
            ))}
            <span className="sr-only">Holdings: {holdings.map((h) => h.symbol).join(', ')}</span>
          </span>
          <span className="font-mono text-[11px] text-muted">{timeAgo(p.createdAt)}</span>
        </div>
        <h3 className="mt-3.5 line-clamp-2 font-display text-[19px] leading-snug font-medium text-text group-hover:text-accent-ink">{p.name}</h3>
        {p.prompt && <p className="mt-1 line-clamp-2 text-[13px] leading-snug text-muted">“{p.prompt}”</p>}
        {(p.invested || deskDriven) && (
          <div className="mt-3 flex flex-wrap gap-1.5">
            {p.invested && (
              <Badge tone={p.invested.returnPct < 0 ? 'down' : 'up'}>
                Invested {fmtUsd(p.invested.amount, { decimals: 0 })} · {fmtPct(p.invested.returnPct, { sign: true, decimals: 1 })}
              </Badge>
            )}
            {deskDriven && (
              <Badge tone="accent" title="At least one holding is sized by a forecast on your desk">
                Desk-driven
              </Badge>
            )}
          </div>
        )}
        <div aria-hidden="true" className="mt-4 flex h-1.5 gap-px overflow-hidden rounded-full">
          {w.map((x, i) => (
            <span key={holdings[i].symbol} style={{ width: `${x * 100}%`, background: PALETTE[i % PALETTE.length] }} />
          ))}
        </div>
        <Sparkline values={(bt?.series ?? []).map((pt) => pt.v)} width={280} height={44} className="mt-4 h-auto w-full" />
        <div className="mt-auto flex items-end justify-between gap-3 border-t border-border pt-3.5">
          <div>
            <div className="label">Measured return</div>
            <p className="mt-1 break-words text-[11px] text-muted">{bt ? measuredWindow(bt) : 'Window unavailable'}</p>
            <PriceChange value={bt?.returnPct} decimals={1} className="mt-1 text-[15px] font-medium" />
          </div>
          <div className="text-right">
            <div className="label">{comparison(bt).label}</div>
            <PriceChange value={bt?.benchmarkReturnPct} decimals={1} className="mt-1 text-[13px]" />
          </div>
        </div>
      </Link>
    </li>
  )
}

function CardSkeleton() {
  return (
    <li className="card flex flex-col p-5">
      <Skeleton className="h-[26px] w-28 rounded-full" />
      <Skeleton className="mt-3.5 h-6 w-3/4" />
      <Skeleton className="mt-2 h-4 w-full" />
      <Skeleton className="mt-4 h-1.5 w-full" />
      <Skeleton className="mt-4 aspect-[280/44] w-full" />
      <Skeleton className="mt-4 h-10 w-full" />
    </li>
  )
}

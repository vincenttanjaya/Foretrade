import { useEffect, useId, useMemo, useRef, useState } from 'react'
import { flushSync } from 'react-dom'
import { Link, Navigate, useNavigate, useParams } from 'react-router'
import { AssetIcon } from '../../components/ui/AssetIcon'
import { Badge } from '../../components/ui/Badge'
import { Button } from '../../components/ui/Button'
import { Card } from '../../components/ui/Card'
import { EmptyState } from '../../components/ui/EmptyState'
import { IconAgent, IconChevron, IconTrade } from '../../components/ui/icons'
import { Input } from '../../components/ui/Input'
import { PriceChange } from '../../components/ui/PriceChange'
import { Skeleton } from '../../components/ui/Spinner'
import { Tabs } from '../../components/ui/Tabs'
import { useActiveSymbol, useAssets } from '../../lib/assets'
import { cx } from '../../lib/cx'
import { isListedIn, useInstruments } from '../../lib/instruments'
import { fmtCompact, fmtPrice, fmtUsd } from '../../lib/format'
import { usePortfolio } from '../../lib/portfolio'
import type { Asset, ChartInterval, Instrument, Interval, Plan } from '../../lib/types'
import { CandleChart, type ChartLine } from './CandleChart'
import { DerivativesRow } from './Derivatives'
import { Copilot, limitGap } from './Copilot'
import { DeskChip, useDesk } from './DeskChip'
import { AlertDialog } from './AlertDialog'
import { NewsFeed } from './News'
import { blankTicket, OrderTicket, type Ticket } from './OrderTicket'
import { PositionsTable, TradesTable, useClosePosition, useTrades } from './tables'
import { marketSearchText, MAX_FILL, planAmount, roundPrice } from './util'
import { assetName, fxNote, isStockPerp, sessionLabel, SYMBOLS, VENUE_GROUPS, venueGroup, type VenueGroup } from '../strategies/strategy'
import { chartBars, feedListing, feedPriceLine, feedQuote, feedQuoteTitle, groupFreshness, movesBars, QUOTE_FEED_LISTINGS, quoteFeedAsset, quoteFeedFx, quoteFeedNote, quoteFeedSession, sourceShort } from '../strategies/quoteFeeds'
import { freshness } from '../../lib/quoteLabel'
import { fmtIn } from '../automations/pipeline'

/** Names people type for a listing whose ticker differs (the API resolves them the same way): /trade/TSMC opens TSM. */
const SYMBOL_ALIAS: Readonly<Record<string, string>> = { TSMC: 'TSM' }

/** Only offer local display when the quote has a usable conversion for the chart and session range. */
function foreignQuote(asset?: Asset) {
  const q = asset?.venue === 'yahoo' ? asset.quoteFeed : null
  return q && q.currency !== 'USD' && Number.isFinite(q.fx) && q.fx > 0 && Number.isFinite(q.localPrice) && q.localPrice > 0 ? q : null
}

/** /trade/:symbol — market list · chart + positions/history · Copilot / Manual ticket. */
export function TradePage() {
  const { symbol: raw = 'BTC' } = useParams()
  const symbol = SYMBOL_ALIAS[raw.toUpperCase()] ?? raw.toUpperCase()
  const { assets, bySymbol, answeredWith } = useAssets()
  const catalogue = useInstruments()
  const row = catalogue.bySymbol[symbol] // the catalogue's row: a listed stock's name and venue before its first quote
  useActiveSymbol(symbol) // a listed symbol on screen rides the 10 s price poll
  const { portfolio } = usePortfolio()
  const [interval, setChartInterval] = useState<ChartInterval>('1h')
  const [originalCurrency, setOriginalCurrency] = useState(true)
  const [plan, setPlan] = useState<Plan | null>(null)
  const [live, setLive] = useState<{ symbol: string; price: number } | null>(null)
  const [bottom, setBottom] = useState<'positions' | 'history' | 'news'>('positions')
  const trades = useTrades(50)
  const { closing, close } = useClosePosition(trades.reload)
  // The panel (and its plan card) remounts per symbol, so drop the old plan's chart lines too.
  if (plan && plan.symbol !== symbol) setPlan(null)

  if (raw !== symbol) return <Navigate to={`/trade/${symbol}`} replace />
  // Unknown here: wait for the catalogue (a listed stock's deep link), also through a failed read it retries, then send
  // what it does not know to BTC.
  if (!SYMBOLS.some(([s]) => s === symbol) && !row && !bySymbol[symbol]) return catalogue.loaded && !catalogue.retrying ? <Navigate to="/trade/BTC" replace /> : <TradeLoading />

  const asset = bySymbol[symbol]
  const feed = quoteFeedAsset(symbol, asset, row)
  const perp = !feed && (asset?.venue === 'hyperliquid' || isStockPerp(symbol))
  // A listed stock with no quote yet: the 10 s poll has not answered for it, or answered without it (no fresh quote).
  const waiting = feed && !asset && !!row?.listed && answeredWith !== symbol
  const quoted = !perp && !feed && live?.symbol === symbol ? live.price : asset?.price
  const price = quoted != null && Number.isFinite(quoted) && quoted > 0 ? quoted : undefined
  const positions = portfolio?.positions ?? null
  const position = positions?.find((p) => p.symbol === symbol)

  const lines: ChartLine[] = []
  // A neutral plan still carries the setup to wait for: draw it dotted, so it reads as a wait, not a trade.
  if (plan) {
    const wait = plan.direction === 'neutral'
    lines.push(
      { price: plan.entry, title: wait ? 'Entry (wait)' : 'Entry', color: '--accent', dotted: wait },
      { price: plan.stopLoss, title: 'Stop', color: '--down', dotted: wait },
      { price: plan.takeProfit, title: 'Target', color: '--up', dotted: wait },
    )
  }
  if (position) lines.push({ price: position.avgPrice, title: `Avg ${position.qty > 0 ? 'long' : 'short'}`, color: '--text', dotted: true })
  // A quote fresher than Yahoo's delayed bars (a real-time source's, SGX's snapshot) moves the last bar at its own trade time.
  const quote = feed ? asset?.quoteFeed : null
  const local = foreignQuote(asset)
  const currency = originalCurrency && local ? local.currency : 'USD'
  const priceScale = currency !== 'USD' && local ? 1 / local.fx : 1
  const lastTrade = quote ? Date.parse(feedQuote(quote).lastTradeAt) / 1000 : Number.NaN
  const tick = quote && movesBars(quote) && price != null && Number.isFinite(lastTrade) ? { t: lastTrade, price, localPrice: quote.localPrice } : null

  return (
    // pb-24 below md: room for the fixed Copilot / Trade bar above the tab bar
    <div className="mx-auto max-w-[1680px] px-4 pt-5 pb-24 md:px-6 md:pb-5 print:pb-5">
      <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_340px] lg:grid-rows-[auto_1fr] xl:grid-cols-[240px_minmax(0,1fr)_360px]">
        {/* Sticky under the top bar: the capped list stays in reach while the page scrolls to positions. */}
        <aside className="hidden self-start xl:sticky xl:top-5 xl:col-start-1 xl:row-span-2 xl:row-start-1 xl:block" aria-label="Markets">
          <MarketList active={symbol} assets={assets} originalCurrency={originalCurrency} />
        </aside>

        <div className="min-w-0 space-y-4 lg:col-start-1 lg:row-start-1 xl:col-start-2">
          <AssetHeader key={symbol} symbol={symbol} asset={asset} row={row} price={price} waiting={waiting} assets={assets} originalCurrency={originalCurrency} onCurrency={setOriginalCurrency} />
          {!feed && <DerivativesRow key={`perps-${symbol}`} symbol={symbol} />}
          <Card className="overflow-hidden">
            <CandleChart
              symbol={symbol}
              pair={asset?.pair ?? (feed ? (row?.pair ?? symbol) : perp ? `xyz:${symbol}` : `${symbol}USDT`)}
              venue={feed ? 'yahoo' : perp ? 'hyperliquid' : 'binance'}
              interval={interval}
              onInterval={setChartInterval}
              lines={lines}
              onPrice={(p) => setLive({ symbol, price: p })}
              tick={tick}
              priceScale={priceScale}
              currency={currency}
              bars={feed ? chartBars(feedListing(symbol, asset, row), interval, tick != null) : undefined}
            />
          </Card>
        </div>

        <div className="self-start lg:col-start-2 lg:row-span-2 lg:row-start-1 xl:col-start-3">
          <TradePanel
            key={symbol}
            symbol={symbol}
            price={price}
            row={row}
            waiting={waiting}
            onPlan={(p, iv) => {
              setPlan(p)
              setChartInterval(iv) // show the plan on the chart it was read from
            }}
            onFilled={trades.reload}
          />
        </div>

        <Card className="min-w-0 self-start overflow-hidden lg:col-start-1 lg:row-start-2 xl:col-start-2">
          <div className="flex items-center justify-between gap-3 border-b border-border px-3 py-2.5 sm:px-4">
            <Tabs
              value={bottom}
              onChange={setBottom}
              size="sm"
              aria-label="Activity and news"
              items={[
                { value: 'positions', label: `Positions${positions?.length ? ` (${positions.length})` : ''}` },
                { value: 'history', label: 'History' },
                { value: 'news', label: 'News' },
              ]}
            />
            <Link to="/account" className="text-[12.5px] text-muted hover:text-accent-ink">
              Account →
            </Link>
          </div>
          {bottom === 'positions' ? (
            <PositionsTable
              positions={positions}
              closing={closing}
              onClose={close}
              active={symbol}
              empty={
                <EmptyState
                  icon={<IconTrade size={20} />}
                  title="No open positions"
                  description="Place a paper order from the ticket, or ask the Copilot for a plan first."
                />
              }
            />
          ) : bottom === 'news' ? (
            <NewsFeed key={symbol} symbol={symbol} />
          ) : (
            <TradesTable
              trades={trades.trades}
              error={trades.error}
              onRetry={trades.reload}
              empty={
                <EmptyState
                  title="No trades yet"
                  description="Every paper fill shows up here, from the ticket, the Agent, or an AI portfolio."
                />
              }
            />
          )}
        </Card>
      </div>
    </div>
  )
}

/** A symbol the bundle does not know, while the instrument catalogue loads: no chart, no socket, no ticket yet. */
function TradeLoading() {
  return (
    <div className="mx-auto max-w-[1680px] px-4 pt-5 pb-24 md:px-6 md:pb-5" role="status" aria-label="Loading the market">
      <Card className="flex items-center gap-3 p-4 sm:px-5">
        <Skeleton className="size-10 rounded-full" />
        <div className="min-w-0 flex-1 space-y-2">
          <Skeleton className="h-5 w-48 max-w-full" />
          <Skeleton className="h-3.5 w-32 max-w-full" />
        </div>
      </Card>
    </div>
  )
}

/** Briefly tints a number green/red when it ticks up/down. */
function useFlash(value: number | undefined) {
  const prev = useRef(value)
  const [dir, setDir] = useState<'up' | 'down' | null>(null)
  useEffect(() => {
    const p = prev.current
    prev.current = value
    if (p == null || value == null || p === value) return
    setDir(value > p ? 'up' : 'down')
    const t = setTimeout(() => setDir(null), 700)
    return () => clearTimeout(t)
  }, [value])
  return dir
}

function AssetHeader({ symbol, asset, row, price, waiting, assets, originalCurrency, onCurrency }: { symbol: string; asset?: Asset; row?: Instrument; price?: number; waiting: boolean; assets: Asset[]; originalCurrency: boolean; onCurrency: (original: boolean) => void }) {
  const flash = useFlash(price)
  const feed = quoteFeedAsset(symbol, asset, row)
  const listing = feedListing(symbol, asset, row) // a quote feed's venue before its asset loads
  const name = asset?.name ?? row?.name ?? assetName(symbol)
  const underlying = asset?.underlying ?? row?.underlying
  // A listed row's symbol is its Yahoo ticker: print the ticker once.
  const yahoo = asset?.quoteFeed?.symbol ?? QUOTE_FEED_LISTINGS[symbol]?.yahoo ?? row?.yahoo ?? asset?.pair
  const perp = !feed && (asset?.venue === 'hyperliquid' || isStockPerp(symbol))
  const unavailableRange = !asset || asset.range24hSource === 'unavailable'
  const desk = useDesk(symbol) // the header remounts per symbol: one read each
  const session = sessionLabel(symbol, feed ? listing : asset)
  const span = feed ? 'Session' : '24h' // a quote feed's range and volume are its latest regular session's
  const local = foreignQuote(asset)
  const currency = originalCurrency && local ? local.currency : 'USD'
  const scale = currency !== 'USD' && local ? 1 / local.fx : 1
  const stats = [
    { label: `${span} high`, value: unavailableRange ? '—' : asset && fmtIn(asset.high24h * scale, currency) },
    { label: `${span} low`, value: unavailableRange ? '—' : asset && fmtIn(asset.low24h * scale, currency) },
    { label: `${span} volume`, value: feed && asset && !(asset.volume24hUsd > 0) ? '—' : asset && (currency === 'USD' ? fmtUsd(asset.volume24hUsd, { compact: true }) : `${currency} ${fmtCompact(asset.volume24hUsd * scale)}`) },
  ]

  return (
    <Card className="flex flex-wrap items-center gap-x-8 gap-y-4 p-4 sm:px-5">
      <div className="flex min-w-0 items-center gap-3">
        <AssetIcon symbol={symbol} name={name} size={40} />
        <div className="min-w-0">
          <h1 className="truncate font-display text-[22px] leading-tight font-medium text-text">
            {name}
          </h1>
          <div className="font-mono text-[12px] text-muted">
            {feed ? <>{symbol}{yahoo && yahoo !== symbol && ` · ${yahoo}`} · {sourceShort(asset?.quoteFeed)} <Badge tone="outline" className="ml-1 align-middle">Quote feed</Badge></> : perp ? `${symbol} · Hyperliquid perp` : <><span>{symbol}/USDT</span> · Binance</>}
          </div>
          {(perp || feed) && underlying && <div className="text-[11.5px] break-words text-muted" title={feed ? 'The listing this quote feed prices' : 'Underlying listing this perp references'}>{underlying}</div>}
          {feed && <div className="mt-1 text-[11.5px] text-muted" title={asset?.sessionChangeAt ? `Next regular-session boundary (${session.venue}): ${new Date(asset.sessionChangeAt).toLocaleString('en-US')}.` : `No verified ${session.venue ?? 'exchange'} calendar here; the market state follows Yahoo's trading period.`}>{quoteFeedSession(listing)}</div>}
          {perp && <div className="mt-1 text-[11.5px] text-muted" title={asset?.sessionChangeAt ? `Next regular-session boundary${session.venue ? ` (${session.venue})` : ''}: ${new Date(asset.sessionChangeAt).toLocaleString('en-US')}. Perps trade 24/7; this is not an oracle update timestamp.` : `Regular ${session.venue ? `${session.venue} ` : ''}session status or next boundary unavailable. Perps trade 24/7.`}>{session.text}</div>}
        </div>
      </div>

      <div className="min-w-[10rem]">
        <div
          className={cx(
            'font-mono text-[28px] leading-none font-semibold tracking-tight tabular-nums transition-colors duration-500',
            flash === 'up' ? 'text-up' : flash === 'down' ? 'text-down' : 'text-text',
          )}
        >
          {price != null ? <span title={currency !== 'USD' ? `Original ${currency} listing price from ${sourceShort(asset?.quoteFeed)}.` : feed ? feedPriceTitle(asset?.quoteFeed) : perp ? 'Hyperliquid perp mark used for paper fills; distinct from the chart close. Oracle update age and underlying-price tracking are not verified.' : 'Binance spot last price'}>{fmtIn(currency !== 'USD' && local ? local.localPrice : price, currency)}</span> : <span className="text-[16px] text-muted">{waiting ? 'Waiting for the first Yahoo quote' : 'Price unavailable'}</span>}
        </div>
        <div className="mt-1.5 flex items-center gap-1.5 text-[12.5px]">
          {asset ? <PriceChange value={asset.change24hPct} /> : <Skeleton className="h-3.5 w-14" />}
          <span className="text-muted">{feed ? 'vs prev. close' : '24h'}</span>
        </div>
        {/* How fresh a quote feed's price is, from its own source and delay: never "live" for a delayed one. */}
        {feed && asset?.quoteFeed && <FeedFreshness quote={asset.quoteFeed} />}
        {local && <Tabs value={currency} onChange={(c) => onCurrency(c !== 'USD')} items={[local.currency, 'USD']} size="sm" aria-label="Display currency" className="mt-2" />}
      </div>

      <dl className="grid grid-cols-3 gap-x-6 gap-y-1">
        {stats.map((s) => (
          <div key={s.label} className="min-w-0">
            <dt className="label">{s.label}</dt>
            <dd className="mt-1 font-mono text-[13px] text-text tabular-nums">{s.value ?? <Skeleton className="h-4 w-16" />}</dd>
          </div>
        ))}
      </dl>

      {/* A price or drawdown alert, set in a dialog without leaving the page. */}
      <AlertDialog symbol={symbol} asset={asset} />

      {perp && <p className="w-full text-[11.5px] text-muted">{unavailableRange ? '24h range unavailable' : 'Approx. 24h range · hourly candles'} · Paper perps trade 24/7, including outside the regular {session.venue ?? 'exchange'} session.</p>}
      {feed && <p className="w-full text-[11.5px] text-muted">{quoteFeedNote(listing)}{quoteFeedFx(listing) && ` ${quoteFeedFx(listing)}.`}</p>}

      {/* Below xl the market list is hidden; this searchable picker switches markets instead. */}
      <MarketPicker symbol={symbol} name={name} assets={assets} originalCurrency={originalCurrency} />

      {desk !== undefined && (
        <div className="-mt-2 w-full min-w-0">
          <DeskChip symbol={symbol} price={price} read={desk} />
        </div>
      )}
    </Card>
  )
}

/** The fill price's tooltip: whose price makes the USD one. */
function feedPriceTitle(q: Asset['quoteFeed']) {
  const source = q?.source && q.source !== 'yahoo' ? `${sourceShort(q)}'s price` : "Yahoo's regular-market price"
  return `USD price used for paper fills: ${source} × FX; outside the session it is the last close.`
}

/** "real-time · 04:20:15 UTC" under a quote feed's price (a dot marks a real-time one), "last close · Oct 14, 06:30 UTC" out of session. */
function FeedFreshness({ quote }: { quote: NonNullable<Asset['quoteFeed']> }) {
  const realtime = quote.marketState === 'open' && freshness(feedQuote(quote)) === 'realtime'
  return (
    <div className="mt-1 flex items-center gap-1.5 font-mono text-[11.5px] text-muted tabular-nums" title={feedQuoteTitle(quote)}>
      {realtime && <span aria-hidden="true" className="size-1.5 shrink-0 rounded-full bg-up" />}
      <span>{feedPriceLine(quote)}</span>
    </div>
  )
}

function MarketList({ active, assets, originalCurrency }: { active: string; assets: Asset[]; originalCurrency: boolean }) {
  return (
    // Capped to the viewport under the top bar (56 px + the page's 20 px padding above and below), so the page
    // never grows with the universe: the search stays put and the rows scroll inside.
    <Card className="flex h-[min(100dvh_-_6rem,56rem)] flex-col overflow-hidden">
      <MarketSearch active={active} assets={assets} originalCurrency={originalCurrency} listClassName="min-h-0 flex-1" />
    </Card>
  )
}

/** Below xl: a button that opens the market search in a height-capped panel. Escape or a tap outside closes it. */
function MarketPicker({ symbol, name, assets, originalCurrency }: { symbol: string; name: string; assets: Asset[]; originalCurrency: boolean }) {
  const [open, setOpen] = useState(false)
  const root = useRef<HTMLDivElement>(null)
  const button = useRef<HTMLButtonElement>(null)
  const panel = useId()
  useEffect(() => {
    if (!open) return
    document.getElementById(panel)?.scrollIntoView?.({ block: 'nearest' }) // the whole panel on a short phone screen
    const away = (e: PointerEvent) => {
      if (!root.current?.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener('pointerdown', away)
    return () => document.removeEventListener('pointerdown', away)
  }, [open, panel])

  return (
    <div
      ref={root}
      className="relative w-full sm:ml-auto sm:w-56 xl:hidden"
      onKeyDown={(e) => {
        if (e.key !== 'Escape' || !open) return
        setOpen(false)
        button.current?.focus()
      }}
    >
      <button
        ref={button}
        type="button"
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-controls={open ? panel : undefined}
        onClick={() => setOpen(!open)}
        className="flex h-10 w-full items-center gap-2 rounded-lg border border-border bg-surface px-3 text-left text-sm text-text transition-[border-color,box-shadow] duration-150 hover:border-muted/45 focus-visible:border-accent focus-visible:ring-[3px] focus-visible:ring-accent/20 focus-visible:outline-none"
      >
        {/* The spaces keep the accessible name "Switch market: BTC Bitcoin"; flex layout ignores them. */}
        <span className="sr-only">Switch market:</span>{' '}
        <span className="font-mono font-medium">{symbol}</span>{' '}
        <span className="min-w-0 flex-1 truncate text-muted">{name}</span>
        <IconChevron size={16} className={cx('shrink-0 text-muted transition-transform duration-150', open ? '-rotate-90' : 'rotate-90')} />
      </button>
      {open && (
        <div
          id={panel}
          role="dialog"
          aria-label="Switch market"
          className="card absolute inset-x-0 top-full z-40 mt-1.5 flex animate-fade-up flex-col overflow-hidden shadow-[0_16px_40px_-16px_rgb(15_27_45/0.35)] sm:left-auto sm:w-80"
        >
          <MarketSearch active={symbol} assets={assets} originalCurrency={originalCurrency} autoFocus onPick={() => setOpen(false)} listClassName="max-h-[min(22rem,50dvh)]" />
        </div>
      )}
    </div>
  )
}

const GROUP_LABEL_PX = 32 // a sticky group label's height: a row under it is hidden
const SEARCH_CAP = 50 // rows a search shows per group; the rest wait for more letters

/**
 * Search box over a scrolling list grouped by VENUE_GROUPS (sticky labels). With an empty search a venue group shows
 * its named rows, the user's held rows and the active row, then "Show all N" for the listed Asian stocks; the active
 * row's group opens on its own. A search matches ticker, name, country, venue and index, at most 50 rows a group.
 * The caller caps the list's height; Enter opens the first match, and the active market is scrolled into view on load,
 * on navigation and on open.
 */
function MarketSearch({
  active,
  assets,
  originalCurrency,
  autoFocus,
  onPick,
  listClassName,
}: {
  active: string
  assets: Asset[]
  originalCurrency: boolean
  autoFocus?: boolean
  onPick?: () => void
  listClassName: string
}) {
  const navigate = useNavigate()
  const catalogue = useInstruments()
  const { portfolio } = usePortfolio()
  const [q, setQ] = useState('')
  const [open, setOpen] = useState<Partial<Record<VenueGroup, boolean>>>({})
  const list = useRef<HTMLUListElement>(null)
  const id = useId()
  const needle = q.trim().toLowerCase()
  // A search also finds listed stocks with no price yet (a stock before its first trade, a halted line):
  // the catalogue lists them, the assets poll does not.
  const unpriced = useMemo(() => {
    const priced = new Set(assets.map((a) => a.symbol))
    return catalogue.listed.filter((r) => !priced.has(r.symbol)).map(unpricedAsset)
  }, [assets, catalogue.listed])
  const indexed = useMemo(
    () => [...assets, ...(needle ? unpriced : [])].map((a) => ({ a, group: venueGroup(a), text: marketSearchText(a) })),
    [assets, unpriced, needle],
  )
  const held = new Set(portfolio?.positions.map((p) => p.symbol))
  const activeGroup = indexed.find(({ a }) => a.symbol === active)?.group
  // Each local venue's tag says what most of its open prices are (real-time, about N min behind, delayed), from all its rows.
  const tags = useMemo(
    () => Object.fromEntries(VENUE_GROUPS.filter((g) => g.delayed).map((g) => [g.key, groupFreshness(indexed.filter((r) => r.group === g.key).map((r) => r.a))])),
    [indexed],
  )
  const groups = VENUE_GROUPS.map((g) => {
    const all = indexed.filter((r) => r.group === g.key && (!needle || r.text.includes(needle))).map((r) => r.a)
    if (needle) return { ...g, total: all.length, items: all.slice(0, SEARCH_CAP), folded: 0, expanded: true }
    const pinned = all.filter((a) => !isListedIn(catalogue, a.symbol) || held.has(a.symbol) || a.symbol === active)
    const expanded = open[g.key] ?? g.key === activeGroup
    return { ...g, total: all.length, items: expanded ? all : pinned, folded: all.length - pinned.length, expanded }
  })
  const first = groups.find((g) => g.items.length > 0)?.items[0] // in display order
  const ready = assets.length > 0
  // A listed active row can arrive after the named rows (its first quote, or the catalogue): scroll to it then too.
  const shown = groups.some((g) => g.items.some((a) => a.symbol === active))

  // Centre the active row unless it already shows in full; scrolls only the list, never the page.
  useEffect(() => {
    const box = list.current
    const row = box?.querySelector<HTMLElement>('[aria-current="page"]')
    if (!box || !row) return
    const top = row.getBoundingClientRect().top - box.getBoundingClientRect().top + box.scrollTop
    if (top >= box.scrollTop + GROUP_LABEL_PX && top + row.offsetHeight <= box.scrollTop + box.clientHeight) return
    box.scrollTo({ top: top - (box.clientHeight - row.offsetHeight) / 2 })
  }, [active, ready, shown])

  return (
    <>
      <div className="shrink-0 border-b border-border p-3">
        <Input
          type="search"
          placeholder="Search markets"
          aria-label="Search markets"
          autoFocus={autoFocus}
          value={q}
          onChange={(e) => setQ(e.target.value)}
          onKeyDown={(e) => {
            if (e.key !== 'Enter' || !first) return
            onPick?.()
            navigate(`/trade/${first.symbol}`)
          }}
        />
      </div>
      <ul ref={list} aria-label="Market list" className={cx('overflow-y-auto overscroll-contain pb-1', listClassName)}>
        {!ready &&
          Array.from({ length: 8 }, (_, i) => (
            <li key={i} className="flex items-center gap-2.5 px-3 py-2">
              <Skeleton className="size-[26px]" />
              <Skeleton className="h-4 flex-1" />
              <Skeleton className="h-4 w-14" />
            </li>
          ))}
        {ready && !first && <li className="px-3 py-6 text-center text-[13px] text-muted">No market matches “{q}”.</li>}
        {groups.map(({ key, label, delayed, total, items, folded, expanded }) => {
          const tag = delayed ? (tags[key] ?? { tag: 'delayed', title: 'Yahoo quotes reach us delayed; each one shows its last trade time' }) : null
          return items.length + folded > 0 && <li key={key}>
          <div className="label sticky top-0 z-10 flex items-baseline gap-1.5 bg-surface px-3 pt-3 pb-1">
            <span id={`${id}${key}`}>{label}</span>
            {tag && <span className={cx('font-sans text-[10.5px] font-normal tracking-normal normal-case', tag.tag === 'real-time' ? 'text-up' : 'text-muted/75')} title={tag.title}>{tag.tag}</span>}
          </div>
          <ul id={`${id}${key}-rows`} aria-labelledby={`${id}${key}`}>{items.map((a) => {
          const on = a.symbol === active
          return (
            <li key={a.symbol}>
              <Link
                to={`/trade/${a.symbol}`}
                aria-current={on ? 'page' : undefined}
                onClick={onPick}
                title={a.quoteFeed ? feedPriceLine(a.quoteFeed) : undefined}
                className={cx('relative flex items-center gap-2.5 px-3 py-2 transition-colors', on ? 'bg-accent-soft/70' : 'hover:bg-surface-2')}
              >
                {on && <span aria-hidden="true" className="absolute inset-y-1.5 left-0 w-[3px] rounded-r-full bg-accent" />}
                <AssetIcon symbol={a.symbol} name={a.name} size={26} />
                <span className="min-w-0 flex-1">
                  <span className="block font-mono text-[13px] font-medium text-text">{a.symbol}</span>
                  <span className="block truncate text-[11.5px] text-muted">{a.name}</span>
                </span>
                {Number.isFinite(a.price) ? (
                  <span className="text-right">
                    <span className="block font-mono text-[12.5px] text-text tabular-nums">{originalCurrency && foreignQuote(a) ? fmtIn(a.quoteFeed!.localPrice, a.quoteFeed!.currency) : fmtPrice(a.price)}</span>
                    <PriceChange value={a.change24hPct} className="text-[11px]" />
                  </span>
                ) : (
                  <span className="text-right text-[11.5px] text-muted">No price yet</span>
                )}
              </Link>
            </li>
          )
        })}</ul>
          {total > items.length && needle && <p className="px-3 pt-1 pb-2 text-[11.5px] text-muted">{total - items.length} more, keep typing</p>}
          {!needle && folded > 0 && (
            <button
              type="button"
              aria-expanded={expanded}
              aria-controls={`${id}${key}-rows`}
              onClick={() => setOpen((o) => ({ ...o, [key]: !expanded }))}
              className="mx-3 mt-0.5 mb-1.5 rounded-md px-0 py-1 text-[12px] font-medium text-accent-ink hover:underline focus-visible:ring-2 focus-visible:ring-accent/40 focus-visible:outline-none"
            >
              {expanded ? 'Show fewer' : `Show all ${total}`}
            </button>
          )}
        </li>
        })}
      </ul>
    </>
  )
}

/** Right panel: Copilot and Manual tabs. Both stay mounted so a pending plan or a half-typed ticket survives a tab switch. */
function TradePanel({
  symbol,
  price,
  row,
  waiting,
  onPlan,
  onFilled,
}: {
  symbol: string
  price: number | undefined
  row?: Instrument
  waiting: boolean
  onPlan: (plan: Plan, interval: Interval) => void
  onFilled: () => void
}) {
  const { portfolio } = usePortfolio()
  const asset = useAssets().bySymbol[symbol]
  const listing = feedListing(symbol, asset, row)
  const [tab, setTab] = useState<'copilot' | 'manual'>('copilot')
  const [ticket, setTicket] = useState<Ticket>(() => blankTicket())

  // An entry away from the price (Copilot.limitGap) pre-fills a limit order at the entry; otherwise a market order.
  function applyPlan(p: Plan, mark: number) {
    const max = portfolio ? Math.floor(portfolio.buyingPower * MAX_FILL) : Infinity
    const limit = limitGap(p.entry, mark) != null
    setTicket({
      side: p.direction === 'short' ? 'sell' : 'buy',
      amount: String(planAmount(p.suggestedNotional, max)),
      stop: String(roundPrice(p.stopLoss)),
      take: String(roundPrice(p.takeProfit)),
      source: 'copilot',
      note: `Copilot ${p.style} plan${limit ? ' · limit' : ''}`,
      limit: limit ? String(roundPrice(p.entry)) : '',
      leverage: 1, // a plan sizes the notional
    })
    setTab('manual')
  }

  // The mobile action bar: show that tab, then bring it into view.
  function jump(to: 'copilot' | 'manual') {
    flushSync(() => setTab(to))
    document.getElementById(to === 'copilot' ? 'copilot-panel' : 'order-ticket')?.scrollIntoView?.({ behavior: 'smooth', block: 'start' })
  }

  return (
    <Card>
      <div className="flex items-center justify-between gap-2 border-b border-border px-4 py-3">
        <Tabs
          value={tab}
          onChange={setTab}
          aria-label="Order panel"
          items={[
            {
              value: 'copilot',
              label: (
                <span className="inline-flex items-center gap-1.5">
                  <IconAgent size={14} /> Copilot
                </span>
              ),
            },
            { value: 'manual', label: 'Manual' },
          ]}
        />
        <Badge tone="accent">Paper</Badge>
      </div>
      {/* scroll-mt keeps the tab row in view when the mobile bar scrolls here */}
      <div id="copilot-panel" hidden={tab !== 'copilot'} className="scroll-mt-16">
        <Copilot symbol={symbol} onPlan={onPlan} onUse={applyPlan} />
      </div>
      <div id="order-ticket" hidden={tab !== 'manual'} className="scroll-mt-16">
        <OrderTicket symbol={symbol} price={price} ticket={ticket} onChange={setTicket} onFilled={onFilled} waiting={waiting ? 'Waiting for the first Yahoo quote' : undefined} />
        {quoteFeedAsset(symbol, asset, row) && <p className="px-4 pb-4 text-[12px] text-muted">Paper fills use the USD price with 2 bps slippage and 0.1% fee; nothing reaches a real venue. {quoteFeedNote(listing)}{quoteFeedFx(listing) && ` ${quoteFeedFx(listing)}.`}</p>}
        {isStockPerp(symbol) && <p className="px-4 pb-4 text-[12px] text-muted">Paper fills use the fresh Hyperliquid perp mark with 2 bps slippage and 0.1% fee. No funding accrual, dividends or split adjustment. Regular {sessionLabel(symbol, asset).venue ?? 'exchange'} session closure does not stop paper trading.{fxNote(asset) && ` ${fxNote(asset)}`}</p>}
      </div>
      {tab === 'copilot' && (
        <div className="border-t border-border px-4 py-3 text-center text-[12.5px] text-muted">
          Know what you want?{' '}
          <button type="button" onClick={() => setTab('manual')} className="font-medium text-accent-ink hover:underline">
            Open the manual ticket
          </button>
        </div>
      )}
      {/* Phones: the panel sits under the chart, so a fixed bar just above the tab bar (56 px + its border) jumps to it. */}
      <nav
        aria-label="Trade actions"
        className="fixed inset-x-0 bottom-[calc(57px+env(safe-area-inset-bottom))] z-30 grid grid-cols-2 gap-2 border-t border-border bg-surface/95 px-4 py-2 backdrop-blur-md md:hidden print:hidden"
      >
        <Button variant="secondary" onClick={() => jump('copilot')}>
          <IconAgent size={16} /> Copilot
        </Button>
        <Button variant="primary" onClick={() => jump('manual')}>
          <IconTrade size={16} /> Trade
        </Button>
      </nav>
    </Card>
  )
}

/** A catalogue row with no quote yet, as a market-list row: price NaN ("No price yet"). */
function unpricedAsset(r: Instrument): Asset {
  return {
    symbol: r.symbol, pair: r.pair, name: r.name, price: NaN, change24hPct: NaN, high24h: 0, low24h: 0, volume24hUsd: 0,
    class: r.class, venue: r.venue, session: null, sessionChangeAt: null, range24hSource: 'unavailable',
    region: r.region, sessionVenue: r.sessionVenue, underlying: r.underlying, localCurrency: r.localCurrency, quoteFeed: null,
  }
}

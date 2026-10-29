import { useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router'
import { Button, buttonClass } from '../../components/ui/Button'
import { Card } from '../../components/ui/Card'
import { Badge } from '../../components/ui/Badge'
import { Disclaimer } from '../../components/ui/Disclaimer'
import { Label, Select } from '../../components/ui/Input'
import { Tabs } from '../../components/ui/Tabs'
import { api } from '../../lib/api'
import { useAuth } from '../../lib/auth'
import { errorMessage } from '../../lib/errors'
import { fmtCompact, fmtPrice } from '../../lib/format'
import type { PredictionBoard, PredictionCategory, PredictionEvent, PredictionMarket, PredictionSort } from '../../lib/types'
import { CrowdOddsAlertForm } from './CrowdOddsAlertForm'
import { ExternalLink } from './DeskVsMarket'
import { fmtPts, httpUrl } from './forecast'
import { pctLabel } from './OddsAlertButton'

/** Bounded text handoff; full authoritative rules belong to the free canonical review. */
export function marketPrompt(m: PredictionMarket, event: PredictionEvent, asOf: string) {
  return `Analyze the Polymarket leaf "${m.question}" (ID ${m.id}; slug ${m.marketSlug}; condition ${m.conditionId}). Source: ${event.url}. Crowd own-Yes price ${pctLabel(m.yesPrice)}, read ${asOf}, ends ${m.endDate}. ${m.estimate ? `Volatility proxy (not the desk): ${pctLabel(m.estimate.p)}; ${m.estimate.assumptions}; spot timestamp ${m.estimate.spotAsOf}.` : `Volatility proxy unavailable: ${m.estimateUnavailable ?? 'not a supported price model'}.`} ${m.desk ? `My exact desk report ${m.desk.forecastId} gives own Yes ${pctLabel(m.desk.p)}.` : 'No exact owned desk overlay.'} Explain the evidence, original observation rules and limitations. Crowd odds are not facts. Nothing executes from this board.`.slice(0, 4000)
}

export function PredictionMarkets() {
  const { user } = useAuth()
  const [category, setCategory] = useState<PredictionCategory>('all')
  const [sort, setSort] = useState<PredictionSort>('edge')
  const [attempt, setAttempt] = useState(0)
  const [result, setResult] = useState<{ key: string; data: PredictionBoard } | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)
  const key = `${user?.id}:${category}:${sort}`
  const board = result?.key === key ? result.data : null
  useEffect(() => {
    let active = true
    let busy = false
    let ctrl: AbortController | null = null
    setError(null)
    async function load() {
      if (!active || busy) return
      busy = true
      ctrl = new AbortController()
      setLoading(true)
      try {
        const data = await api.predictionMarkets({ category, sort }, ctrl.signal)
        if (!active || ctrl.signal.aborted) return
        setResult({ key, data })
        setError(null)
      } catch (err) {
        if (active && !ctrl.signal.aborted) {
          setError(errorMessage(err))
          setResult((old) => old?.key === key ? { key, data: { ...old.data, stale: true } } : old)
        }
      } finally {
        if (active) {
          busy = false
          setLoading(false)
        }
      }
    }
    void load()
    const timer = setInterval(() => { if (document.visibilityState === 'visible') void load() }, 60_000)
    const resume = () => { if (document.visibilityState === 'visible') void load() }
    document.addEventListener('visibilitychange', resume)
    window.addEventListener('focus', resume)
    return () => {
      active = false
      ctrl?.abort()
      clearInterval(timer)
      document.removeEventListener('visibilitychange', resume)
      window.removeEventListener('focus', resume)
    }
  }, [key, category, sort, attempt])
  return (
    <div className="mx-auto max-w-6xl px-4 py-8 md:px-8 md:py-10">
      <div className="label">Forecast desk · Polymarket sample</div>
      <h1 className="mt-3 font-display text-4xl leading-tight md:text-5xl">What the crowd is pricing.</h1>
      <p className="mt-3 max-w-3xl text-sm leading-relaxed text-muted">
        A top-volume sample of Crypto + Macro events, up to 30 per category and 20 markets per event. Browse free.
        Crowd prices, a volatility proxy and your own desk estimate are separate reads.
      </p>
      <div className="mt-6 flex flex-wrap items-end justify-between gap-4">
        <Tabs<PredictionCategory>
          aria-label="Market category"
          value={category}
          onChange={setCategory}
          items={[{ value: 'all', label: 'Both' }, { value: 'crypto', label: 'Crypto' }, { value: 'macro', label: 'Macro' }]}
        />
        <div>
          <Label htmlFor="prediction-sort">Sort markets</Label>
          <Select id="prediction-sort" value={sort} onChange={(e) => setSort(e.target.value as PredictionSort)}>
            <option value="edge">Biggest edge</option>
            <option value="volume">24h volume</option>
            <option value="ending">Ending soon</option>
          </Select>
        </div>
        <Button variant="secondary" disabled={loading} onClick={() => setAttempt((a) => a + 1)}>Refresh markets</Button>
      </div>
      {board && (
        <p role="status" className="mt-4 text-[13px] text-muted">
          Read {board.asOf} · {board.events.length} events
          {board.stale && ' · Showing an older sample; quotes and proxies may be stale.'}
        </p>
      )}
      {loading && !board && <p role="status" className="mt-6 text-sm text-muted">Reading prediction markets…</p>}
      {error && (
        <div className="mt-4">
          <p role="alert" className="text-sm text-down">{error}</p>
          <Button variant="secondary" className="mt-2" disabled={loading} onClick={() => setAttempt((a) => a + 1)}>Retry markets</Button>
        </div>
      )}
      {board?.events.length === 0 && <p className="mt-8 text-sm text-muted">No eligible markets in this sample.</p>}
      <div className="mt-5 grid gap-5">
        {board?.events.map((event) => <EventCard key={`${key}:${event.slug}`} event={event} asOf={board.asOf} stale={board.stale} />)}
      </div>
      <p className="mt-5 text-[12px] leading-relaxed text-muted">
        Volatility proxy: a zero-drift model of completed daily closes, not the desk or a guarantee of external resolution.
        Touch contracts stay unavailable when their prior observation history is unverified. Polymarket prices are crowd odds, not facts.
      </p>
      <Disclaimer className="mt-2" />
    </div>
  )
}

function EventCard({ event, asOf, stale }: { event: PredictionEvent; asOf: string; stale: boolean }) {
  const [alert, setAlert] = useState<{ market: PredictionMarket; asOf: string } | null>(null)
  const url = httpUrl(event.url)
  return (
    <Card className="min-w-0 overflow-hidden p-4 md:p-5">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <h2 className="font-display text-2xl [overflow-wrap:anywhere]">{event.title}</h2>
          <p className="mt-1 text-[12px] text-muted">
            {event.category === 'crypto' ? 'Crypto' : 'Macro'} · ends {event.endDate} · {event.volume24hUsd == null ? 'Volume unknown' : `$${fmtCompact(event.volume24hUsd)} 24h volume`}
          </p>
        </div>
        {url && <ExternalLink href={url.href}>View on Polymarket</ExternalLink>}
      </header>
      <div className="mt-4 overflow-x-auto rounded-lg border border-border" role="region" aria-label={`${event.title} ladder`} tabIndex={0}>
        <table aria-label={`${event.title} markets`} className="w-full min-w-[780px] text-left text-[13px]">
          <thead className="bg-surface-2 text-muted">
            <tr>{['Market', 'Crowd · Yes', 'Volatility proxy', 'Your desk', 'Actions'].map((h) => <th key={h} scope="col" className="p-3 font-medium">{h}</th>)}</tr>
          </thead>
          <tbody className="divide-y divide-border">
            {event.markets.map((m) => <MarketRow key={m.id} market={m} event={event} asOf={asOf} stale={stale} onAlert={() => setAlert({ market: m, asOf })} />)}
          </tbody>
        </table>
      </div>
      {alert && <CrowdOddsAlertForm key={alert.market.id} market={alert.market} event={event} asOf={alert.asOf} onClose={() => setAlert(null)} />}
    </Card>
  )
}

function MarketRow({ market: m, event, asOf, stale, onAlert }: {
  market: PredictionMarket; event: PredictionEvent; asOf: string; stale: boolean; onAlert: () => void
}) {
  const navigate = useNavigate()
  return (
    <tr>
      <th scope="row" className="max-w-64 p-3 align-top font-normal">
        <span className="block font-medium">{m.question}</span>
        <span className="mt-1 block text-[11px] text-muted">Ends {m.endDate}</span>
        {m.thin && <Badge className="mt-2">Thin market</Badge>}
      </th>
      <td className="p-3 align-top">
        <span className="font-mono tabular-nums">{pctLabel(m.yesPrice)}</span>
        <span className="mt-1 block text-[11px] text-muted">{m.change24hPp == null ? '24h change unknown' : `${fmtPts(m.change24hPp)} / 24h`}</span>
      </td>
      <td className="max-w-64 p-3 align-top"><ProxyRead market={m} stale={stale} /></td>
      <td className="p-3 align-top">
        {m.desk ? (
          <Link to={`/forecasts/${encodeURIComponent(m.desk.forecastId)}`} className="text-accent-ink hover:underline">
            Your desk · {pctLabel(m.desk.p)} · {fmtPts(m.desk.edgePp)}
          </Link>
        ) : <span className="text-muted">—</span>}
      </td>
      <td className="p-3 align-top">
        <div className="flex flex-col items-start gap-2">
          <Link to={`/forecasts/new?marketId=${encodeURIComponent(m.id)}`} className={buttonClass('secondary', 'sm')} aria-label={`Run the desk · 5 cr: ${m.question}`}>Run the desk · 5 cr</Link>
          <Button variant="ghost" size="sm" aria-label={`Ask the agent: ${m.question}`} onClick={() => navigate('/', { state: { prompt: marketPrompt(m, event, asOf), context: m.rule ? { symbol: m.rule.symbol } : undefined } })}>Ask the agent · 2 cr</Button>
          <Button variant="ghost" size="sm" aria-label={`Set crowd alert: ${m.question}`} onClick={onAlert}>Set crowd alert · free</Button>
        </div>
      </td>
    </tr>
  )
}

function ProxyRead({ market: m, stale }: { market: PredictionMarket; stale: boolean }) {
  const e = m.estimate
  if (!e) return <span className="text-[12px] text-muted">{m.estimateUnavailable || 'Crowd odds only'}</span>
  return (
    <>
      <span className="font-mono tabular-nums">{pctLabel(e.p)} · {fmtPts(e.edgePp)}</span>
      <span className="mt-1 block text-[11px] text-muted">{stale || m.thin || e.signal === 'wait' ? 'Wait' : `Proxy lean ${e.signal}`}</span>
      {!stale && !m.thin && e.evPer100 != null && <span className="mt-1 block text-[11px]">EV per $100: {fmtPrice(e.evPer100)}</span>}
      <details className="mt-2 text-[11px] text-muted">
        <summary className="cursor-pointer">Model inputs</summary>
        <p className="mt-1">
          {e.method === 'lognormal' ? 'Lognormal' : 'Touch'} proxy. Spot {fmtPrice(e.spot)} at {e.spotAsOf}; annualized volatility {e.volAnnPct}%;
          {' '}{e.days} days to end. {e.closes} closes, {e.windowStart} → {e.windowEnd}. Source: {e.source}. {e.assumptions}
        </p>
      </details>
    </>
  )
}

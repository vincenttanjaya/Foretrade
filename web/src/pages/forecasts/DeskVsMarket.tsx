// Desk vs market and the Prediction Market Copilot on paper: the desk against Polymarket's live odds, a
// paper YES/NO position on the matched market, and the positions on this forecast's lineage.
import { useEffect, useRef, useState, type ReactNode } from 'react'
import { Badge, type BadgeTone } from '../../components/ui/Badge'
import { Button } from '../../components/ui/Button'
import { Card } from '../../components/ui/Card'
import { IconAlert, IconArrowRight } from '../../components/ui/icons'
import { Input } from '../../components/ui/Input'
import { useToast } from '../../components/toast'
import { api } from '../../lib/api'
import { cx } from '../../lib/cx'
import { errorMessage } from '../../lib/errors'
import { fmtUsd } from '../../lib/format'
import { usePortfolio } from '../../lib/portfolio'
import type { Forecast, ForecastResult, MarketOdds, MarketPosition, OutcomeP } from '../../lib/types'
import { fmtDay, fmtEv, fmtPts, httpUrl, isThin, SIGNAL_LABEL, yesP } from './forecast'
import { OddsAlertButton } from './OddsAlertButton'

type Side = 'yes' | 'no'

const HOUR = 3_600_000
const REMATCH_GAP = 10 * 60_000 // the API answers a search within 10 min of the last from the stored result
export const STAKES = [10, 50, 100, 500]
export const MIN_STAKE = 10
export const MAX_STAKE = 1000

/** What a $1 contract on side costs: its price + 1¢ slippage, at most 99¢ (as the API fills). */
export const fillPrice = (yesPrice: number, side: Side) => Math.min((side === 'yes' ? yesPrice : 1 - yesPrice) + 0.01, 0.99)
/** Profit if side wins: stake / price − stake, to the cent. */
export const winOf = (stake: number, price: number) => Math.round((stake / price - stake) * 100) / 100
/** EV of the stake at the desk's probability of that side. */
export const evOf = (stake: number, price: number, pSide: number) => stake * (pSide / price - 1)

/** "86¢", "17.5¢": Polymarket prices move in tenths of a cent (half up: 0.0145 → "1.5¢"). */
export const cents = (p: number) => `${Math.round(p * 1000) / 10}¢`
/** "$100", "$16.28": cents only when there are some. */
const money = (n: number) => fmtUsd(n, { decimals: Math.abs(n - Math.round(n)) < 0.005 ? 0 : 2 })
const pct = (p: number) => `${Math.max(0, Math.min(1, p)) * 100}%`
/** "57%", "64.5%": one decimal, so the two bars add up to the edge shown under them. */
const pct1 = (p: number | null) => (p == null ? '—' : `${Math.round(p * 1000) / 10}%`)
const sideLabel = (side: Side) => (side === 'yes' ? 'Yes' : 'No')

/**
 * Desk vs market for a live binary or categorical report: the desk's P(Yes) (categorical: its top option's P, against
 * that option's market) against Polymarket's price for the same question, the edge, the EV of a $1 contract on the
 * side the desk favours and a lean. A thin market (under $1,000 traded) shows no lean and mutes the edge. Odds over an
 * hour old refresh once on open; a report never searched (older or seeded) searches once. Then a paper position on
 * the matched market, settled when this forecast resolves.
 */
export function DeskVsMarket({ f, r, final, onChange }: { f: Forecast; r: ForecastResult; final: OutcomeP[]; onChange: (f: Forecast) => void }) {
  const { toast } = useToast()
  const { refresh: refreshPortfolio } = usePortfolio()
  const live = !f.replay && f.status === 'completed'
  // A categorical result without `outcome` predates option markets: it has no comparable market.
  const m = f.shape === 'categorical' && !r.market?.outcome ? null : (r.market ?? null)
  const desk = m?.outcome ? (final.find((x) => x.outcome === m.outcome)?.p ?? null) : yesP(final)
  const [busy, setBusy] = useState(false)
  const [positions, setPositions] = useState<MarketPosition[]>([])
  const [reload, setReload] = useState(0)
  const opened = useRef(false)
  const resolved = f.resolution?.outcome ?? null

  async function refresh(rematch: boolean, silent = false) {
    setBusy(true)
    try {
      onChange((await api.refreshMarket(f.id, rematch)).forecast)
    } catch (err) {
      if (!silent) toast(errorMessage(err), 'error')
    } finally {
      setBusy(false)
    }
  }

  useEffect(() => {
    if (opened.current || !live) return
    opened.current = true
    const stale = m && Date.now() - Date.parse(m.asOf) > HOUR
    if (stale || (!r.market && !r.marketCheckedAt)) void refresh(false, true)
  }, []) // once per open

  useEffect(() => {
    if (f.replay) return
    const ctrl = new AbortController()
    api
      .marketPositions(f.id, ctrl.signal)
      .then((res) => setPositions(res.positions ?? []))
      .catch(() => {}) // ponytail: the list stays as it was; the next change reloads it
    return () => ctrl.abort()
  }, [f.id, f.replay, resolved, reload])

  // A resolution settles the positions on the server: re-read cash.
  useEffect(() => {
    if (resolved) void refreshPortfolio()
  }, [resolved, refreshPortfolio])

  const superseded = !!f.history?.length && f.history[f.history.length - 1].id !== f.id
  const actionReady = !m?.linkStatus || m.linkStatus === 'current'

  return (
    <Card className="mt-4 p-5 md:p-6 print:break-inside-avoid">
      <section aria-labelledby="desk-vs-market">
        {m ? (
          <Odds m={m} desk={desk} refresh={live ? () => refresh(false) : undefined} busy={busy} />
        ) : (
          <>
            <h2 id="desk-vs-market" className="label">
              Desk vs market
            </h2>
            <div className="mt-3 flex flex-wrap items-center gap-x-4 gap-y-2">
              <p className="text-[13px] text-muted">No matching Polymarket market found.</p>
              {live && <SearchAgain checkedAt={r.marketCheckedAt} busy={busy} onClick={() => refresh(true)} />}
            </div>
          </>
        )}
        {m && !actionReady && (
          <p role="status" className="mt-3 rounded-lg border border-border bg-surface-2 px-3 py-2 text-[13px] text-muted">
            Fresh actions paused: linked market {m.linkStatus}. This is the stored quote at its original timestamp; refresh to check the canonical contract.
          </p>
        )}
        {m && actionReady && live && !resolved && !superseded && desk != null && (
          <PositionPanel key={m.question} f={f} m={m} desk={desk} onBought={() => setReload((n) => n + 1)} />
        )}
        {m && actionReady && live && !resolved && !superseded && desk != null && !isThin(m) && <OddsAlertButton key={`alert ${m.question}`} forecastId={f.id} m={m} desk={desk} />}
        {positions.length > 0 && <Positions positions={positions} />}
        {m && (
          <p className="mt-4 border-t border-border pt-3 text-[12px] text-muted">
            Paper positions only: each contract pays $1 if its side wins when this forecast resolves. Nothing is sent to Polymarket.
          </p>
        )}
      </section>
    </Card>
  )
}

/**
 * "Search again" (a rematch), disabled for 10 minutes after the last search ("Searched 3 min ago") and re-enabled on
 * time without a reload.
 */
export function SearchAgain({ checkedAt, busy, onClick }: { checkedAt?: string; busy: boolean; onClick: () => void }) {
  const [, rerender] = useState(0)
  const at = checkedAt ? Date.parse(checkedAt) : NaN
  useEffect(() => {
    const left = at + REMATCH_GAP - Date.now()
    if (!(left > 0)) return
    const t = setTimeout(() => rerender((n) => n + 1), left)
    return () => clearTimeout(t)
  }, [at])
  const ago = Number.isFinite(at) ? Date.now() - at : Infinity
  const gated = ago < REMATCH_GAP
  return (
    <Button
      variant="secondary"
      size="sm"
      className="print:hidden"
      loading={busy}
      disabled={gated}
      title={gated ? `Searched ${Math.max(1, Math.round(ago / 60_000))} min ago` : undefined}
      onClick={onClick}
    >
      Search again
    </Button>
  )
}

function Odds({ m, desk, refresh, busy }: { m: MarketOdds; desk: number | null; refresh?: () => void; busy: boolean }) {
  const thin = isThin(m)
  const current = !m.linkStatus || m.linkStatus === 'current'
  const url = httpUrl(m.url)
  const asOf = new Date(m.asOf)
  const meta = [
    'Polymarket',
    !Number.isNaN(asOf.getTime()) && `as of ${fmtDay(m.asOf)}, ${asOf.toISOString().slice(11, 16)} UTC`,
    m.volumeUsd != null && `${fmtUsd(m.volumeUsd, { compact: true })} volume`,
  ]
    .filter(Boolean)
    .join(' · ')
  const yes = m.edgePp >= 0
  const option = m.outcome ? ` · ${m.outcome}` : ''
  return (
    <>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h2 id="desk-vs-market" className="label">
          Desk vs market
        </h2>
        {!current ? (
          <Badge>Wait</Badge>
        ) : thin ? (
          <Badge tone="accent">Thin market</Badge>
        ) : (
          <Badge tone={m.signal === 'yes' ? 'up' : m.signal === 'no' ? 'down' : 'neutral'}>{SIGNAL_LABEL[m.signal] ?? 'Wait'}</Badge>
        )}
      </div>
      <ul className="mt-3 flex flex-col gap-3">
        <OddsBar label={`Desk${option} ${pct1(desk)}`} p={desk ?? 0} fill="bg-accent" />
        <OddsBar
          label={`Market${option} ${pct1(m.yesPrice)}`}
          meta={
            <>
              {meta}
              {refresh && (
                <span className="print:hidden">
                  {' · '}
                  <button
                    type="button"
                    aria-label="Refresh market odds"
                    disabled={busy}
                    onClick={refresh}
                    className="rounded-sm text-accent-ink hover:underline disabled:text-muted disabled:no-underline"
                  >
                    {busy ? 'Refreshing…' : 'Refresh'}
                  </button>
                </span>
              )}
            </>
          }
          p={m.yesPrice}
          fill="bg-muted/55"
        />
      </ul>
      {current && <p className="mt-4 flex flex-wrap items-baseline gap-x-4 gap-y-1">
        <span className={cx('font-display text-[22px] leading-none tabular-nums', thin ? 'text-muted' : 'text-text')}>Edge {fmtPts(m.edgePp)}</span>
        <span className="font-mono text-[12.5px] text-muted tabular-nums">
          EV {fmtEv(yes ? m.evYes : m.evNo)} per $1 {yes ? 'Yes' : 'No'} contract
        </span>
      </p>}
      {thin && (
        <p className="mt-2 flex gap-2 text-[13px] leading-snug text-text">
          <IconAlert size={15} className="mt-px shrink-0 text-accent-ink" />
          {m.volumeUsd == null ? 'Volume unknown' : `Only ${fmtUsd(m.volumeUsd, { compact: true, decimals: 0 })} traded on this market`} — too thin to
          read an edge.
        </p>
      )}
      <p className="mt-3 text-[13px] leading-snug text-muted">
        Matched: <span className="text-text">“{m.question}”</span>
        {url && (
          <>
            {' '}
            <ExternalLink href={url.href}>View on Polymarket</ExternalLink>
          </>
        )}
      </p>
      {m.match === 'close' && (
        <p className="mt-2 flex gap-2 text-[13px] leading-snug text-text">
          <IconAlert size={15} className="mt-px shrink-0 text-accent-ink" />
          Close match: the market's wording or resolution time differs — read its rules.
        </p>
      )}
    </>
  )
}

function OddsBar({ label, meta, p, fill }: { label: string; meta?: ReactNode; p: number; fill: string }) {
  return (
    <li>
      <div className="flex flex-wrap items-baseline gap-x-1.5 text-[13.5px]">
        <span className="font-medium text-text tabular-nums">{label}</span>
        {meta && <span className="font-mono text-[11px] text-muted"> · {meta}</span>}
      </div>
      <div aria-hidden="true" className="mt-1.5 h-2 overflow-hidden rounded-full bg-surface-2">
        <span className={cx('block h-full origin-left animate-bar rounded-full', fill)} style={{ width: pct(p) }} />
      </div>
    </li>
  )
}

/** Yes or No on the matched market at the live price + 1¢, $10 to $1,000 from paper cash, with a win/lose preview. */
function PositionPanel({ f, m, desk, onBought }: { f: Forecast; m: MarketOdds; desk: number; onBought: () => void }) {
  const { toast } = useToast()
  const { setPortfolio } = usePortfolio()
  const wait = isThin(m) || m.signal === 'wait'
  const [side, setSide] = useState<Side | null>(wait ? null : m.signal === 'no' ? 'no' : 'yes')
  const [text, setText] = useState('100')
  const [busy, setBusy] = useState(false)
  const stake = Number(text)
  const valid = text.trim() !== '' && Number.isFinite(stake) && stake >= MIN_STAKE && stake <= MAX_STAKE
  const option = m.outcome ? `${m.outcome} · ` : ''
  const price = side && fillPrice(m.yesPrice, side)

  async function confirm() {
    if (!side || !valid) return
    setBusy(true)
    try {
      const res = await api.openMarketPosition(f.id, side, stake)
      setPortfolio(res.portfolio)
      toast(`Bought ${res.position.contracts.toFixed(1)} ${sideLabel(side)} contracts at ${cents(res.position.price)} (paper).`)
      onBought()
    } catch (err) {
      toast(errorMessage(err), 'error')
    } finally {
      setBusy(false)
    }
  }

  let preview: ReactNode = null
  if (side && price && valid) {
    const win = money(winOf(stake, price))
    const lose = money(stake)
    const pSide = side === 'yes' ? desk : 1 - desk
    preview = (
      <>
        <p className="text-[13.5px] text-text tabular-nums">
          {m.outcome
            ? side === 'yes'
              ? `Win ${win} if ${m.outcome} · lose ${lose} otherwise`
              : `Win ${win} if not ${m.outcome} · lose ${lose} if ${m.outcome}`
            : side === 'yes'
              ? `Win ${win} if Yes · lose ${lose} if No`
              : `Win ${win} if No · lose ${lose} if Yes`}
        </p>
        <p className="mt-1 font-mono text-[12px] text-muted tabular-nums">
          At the desk's {pct1(pSide)}: EV {fmtEv(Math.round(evOf(stake, price, pSide) * 100) / 100)}
        </p>
      </>
    )
  }

  return (
    <div role="group" aria-labelledby="take-position" className="mt-5 border-t border-border pt-4 print:hidden">
      <h3 id="take-position" className="label">
        Take a paper position
      </h3>
      <div role="group" aria-label="Side" className="mt-2.5 grid grid-cols-2 gap-2">
        {(['yes', 'no'] as const).map((s) => (
          <button
            key={s}
            type="button"
            aria-pressed={side === s}
            disabled={busy}
            onClick={() => setSide(s)}
            className={cx(
              'h-10 rounded-lg border px-3 text-[13.5px] font-medium tabular-nums transition-colors disabled:opacity-55',
              side === s
                ? s === 'yes'
                  ? 'border-up bg-up/10 text-up'
                  : 'border-down bg-down/10 text-down'
                : 'border-border bg-surface text-text hover:bg-surface-2',
            )}
          >
            {option ? `${option}${sideLabel(s)} ${cents(fillPrice(m.yesPrice, s))}` : `${sideLabel(s)} · ${cents(fillPrice(m.yesPrice, s))}`}
          </button>
        ))}
      </div>
      {wait && !side && (
        <p className="mt-2 text-[12.5px] leading-snug text-muted">
          The desk says WAIT: the edge is under 5 points or the market is thin. You can still pick a side.
        </p>
      )}
      <div className="mt-3 flex flex-wrap items-center gap-1.5">
        {STAKES.map((v) => (
          <button
            key={v}
            type="button"
            disabled={busy}
            aria-pressed={valid && stake === v}
            onClick={() => setText(String(v))}
            className={cx(
              'h-8 rounded-md border px-2.5 font-mono text-[12.5px] transition-colors disabled:opacity-55',
              valid && stake === v ? 'border-ink bg-ink text-ink-fg' : 'border-border bg-surface text-text hover:bg-surface-2',
            )}
          >
            ${v}
          </button>
        ))}
        <div className="relative w-28">
          <span aria-hidden="true" className="pointer-events-none absolute top-1/2 left-2.5 -translate-y-1/2 font-mono text-[12.5px] text-muted">
            $
          </span>
          <Input
            type="number"
            inputMode="decimal"
            min={MIN_STAKE}
            max={MAX_STAKE}
            step="any"
            aria-label="Stake in dollars"
            aria-invalid={!valid || undefined}
            disabled={busy}
            value={text}
            onChange={(e) => setText(e.target.value)}
            className="h-8 pl-6 font-mono text-[12.5px] tabular-nums"
          />
        </div>
      </div>
      <div className="mt-3 min-h-10">
        {!valid ? (
          <p className="text-[12.5px] text-down">Stake $10 to $1,000.</p>
        ) : (
          (preview ?? <p className="text-[12.5px] text-muted">Pick a side to see what it pays.</p>)
        )}
      </div>
      <Button variant="accent" className="mt-3 w-full sm:w-auto" loading={busy} disabled={!side || !valid} onClick={confirm}>
        Confirm (paper)
      </Button>
    </div>
  )
}

const STATUS_CHIP: Record<MarketPosition['status'], BadgeTone> = { open: 'neutral', won: 'up', lost: 'down' }

/** "Yes · 116.3 contracts @ 86¢ · $100 · pays $116.28 · now 88¢" with an Open / Won / Lost chip. */
export function positionLine(p: MarketPosition) {
  const option = p.outcome !== 'Yes' ? ` · ${p.outcome}` : ''
  const now = p.status === 'open' && p.markPrice != null ? ` · now ${cents(p.markPrice)}` : ''
  return `${sideLabel(p.side)}${option} · ${p.contracts.toFixed(1)} contracts @ ${cents(p.price)} · ${money(p.cost)} · pays ${money(p.maxPayout)}${now}`
}

export function PositionChip({ p }: { p: MarketPosition }) {
  const pnl = p.pnl ?? 0
  const label = p.status === 'open' ? 'Open' : `${p.status === 'won' ? 'Won' : 'Lost'} ${pnl < 0 ? '−' : '+'}${money(Math.abs(pnl))}`
  return <Badge tone={STATUS_CHIP[p.status]}>{label}</Badge>
}

function Positions({ positions }: { positions: MarketPosition[] }) {
  return (
    <div className="mt-5 border-t border-border pt-4">
      <h3 className="label">Your positions</h3>
      <ul className="mt-2 flex flex-col gap-2">
        {positions.map((p) => (
          <li key={p.id} className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1 text-[13px]">
            <span className="min-w-0 font-mono text-[12.5px] text-text tabular-nums">{positionLine(p)}</span>
            <PositionChip p={p} />
          </li>
        ))}
      </ul>
    </div>
  )
}

export function ExternalLink({ href, children }: { href: string; children: ReactNode }) {
  return (
    <>
      <a href={href} target="_blank" rel="noopener noreferrer" className="min-w-0 break-words text-accent-ink hover:underline">
        {children}
        {/* The no-break space glues the icon to the last word, so it never wraps onto a line of its own. */}
        {' '}
        <IconArrowRight size={11} className="inline -rotate-45 align-baseline" />
        <span className="sr-only"> (opens in a new tab)</span>
      </a>
      {/* On paper the reader cannot click: print the address. */}
      <span className="hidden text-[11px] break-all text-muted print:inline"> ({href})</span>
    </>
  )
}

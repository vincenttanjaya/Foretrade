import { useAuth } from '../../lib/auth'
import { usePortfolio } from '../../lib/portfolio'
import { usePrivateSnapshot } from '../../lib/usePrivateSnapshot'
import { useNavigate } from 'react-router'
import { Badge, type BadgeTone } from '../../components/ui/Badge'
import { Button } from '../../components/ui/Button'
import { Card, CardHeader } from '../../components/ui/Card'
import { Disclaimer } from '../../components/ui/Disclaimer'
import { IconAgent } from '../../components/ui/icons'
import { PriceChange } from '../../components/ui/PriceChange'
import { Skeleton } from '../../components/ui/Spinner'
import { Stat } from '../../components/ui/Stat'
import { api } from '../../lib/api'
import { cx } from '../../lib/cx'
import { fmtDate, fmtPct, fmtUsd } from '../../lib/format'
import type { ConcentrationLevel, PortfolioReview, ReviewHolding } from '../../lib/types'

/** What "Review with the agent" sends: facts first, interpretation second, at most one suggested action. */
export const REVIEW_PROMPT =
  'Review my paper portfolio: concentration, which holdings move together, beta to BTC, and stop coverage. ' +
  'Separate what the numbers show from your interpretation, then suggest at most one trim or protective stop.'

const LEVEL: Record<ConcentrationLevel, { title: string; tone: BadgeTone; label: string }> = {
  high: { title: 'Concentrated book', tone: 'down', label: 'High concentration' },
  medium: { title: 'Moderately concentrated', tone: 'accent', label: 'Medium concentration' },
  low: { title: 'Low concentration', tone: 'up', label: 'Low concentration' },
}
const STOP_TONE: Record<ReviewHolding['stopStatus'], string> = {
  valid: 'text-up', dormant: 'text-accent-ink', crossed: 'text-down', missing: 'text-down', unknown: 'text-muted',
}

const MAX_BARS = 8 // the positions table below lists every holding

/**
 * "Is my paper book diversified?" (round 7): the server's review of the open positions, reloaded whenever the
 * account changes (its revision). Numbers are Go facts; the agent link hands the interpretation to the chat.
 */
export function PortfolioHealth({ revision }: { revision: number | null }) {
  const navigate = useNavigate()
  const { user } = useAuth()
  const { refresh } = usePortfolio()
  const { value: review, error, busy, retry } = usePrivateSnapshot(user?.id ?? '', user ? revision : null, (signal) => api.portfolioReview(signal), (r) => r, refresh)

  const level = review?.concentration ? LEVEL[review.concentration.level] : null
  return (
    <Card className="mt-6">
      <CardHeader
        label="Portfolio health"
        title={review ? (level?.title ?? (review.predictionStakes > 0 ? 'Outcome stakes to review' : 'Nothing to review yet')) : 'Reviewing your book'}
        actions={level && <Badge tone={level.tone}>{level.label}</Badge>}
      />
      <div className="px-5 pb-5" aria-busy={busy}>
        {review && <p className="mb-3 break-words font-mono text-[11px] text-muted">{error ? 'Stale · ' : busy ? 'Refreshing · ' : 'Reviewed · '}{review.asOf} · revision {review.revision} (request time, not a market observation)</p>}
        {error ? (
          <div role="alert" className="flex flex-wrap items-center gap-3 text-sm text-muted">
            {error}
            <Button variant="secondary" size="sm" onClick={retry}>
              Retry
            </Button>
          </div>
        ) : null}
        {!review ? (
          <Skeleton className="h-32 w-full" />
        ) : review.positions === 0 && review.predictionStakes === 0 ? (
          <p className="text-sm text-muted">Open a position and this card reads its concentration, what moves together, beta to BTC and stop coverage.</p>
        ) : (
          <Body review={review} />
        )}
        <div className="mt-5 flex flex-wrap items-center justify-between gap-3 border-t border-border pt-4">
          <div className="font-mono text-[11px] text-muted">
            {review?.window.days ?? 90} requested daily returns · {review?.window.bars ?? 0} aligned dates · {review?.coverage.commonDays ?? 0} common days · venue candle history (Binance crypto / Hyperliquid perps) · BTC is a limited reference · <Disclaimer className="inline-flex" />
          </div>
          <Button variant="secondary" size="sm" disabled={!review || (review.positions === 0 && review.predictionStakes === 0)} onClick={() => navigate('/', { state: { prompt: REVIEW_PROMPT } })}>
            <IconAgent size={16} /> Review with the agent
          </Button>
        </div>
      </div>
    </Card>
  )
}

function Body({ review: r }: { review: PortfolioReview }) {
  const top = r.holdings[0]
  const c = r.concentration
  const uncovered = r.stops.uncovered.length
  const notes = r.notes.map((n) => r.predictionStakes > 0 && n === 'No open positions: nothing to review.' ? 'No cash-asset positions; outcome stakes remain to review.' : n)
  return (
    <>
      <div className="mb-5 text-[12.5px] leading-relaxed text-muted">
        {r.coverage.reason && <p>{r.coverage.reason}</p>}
        {r.coverage.missingSymbols.length > 0 && <p>Missing support: {r.coverage.missingSymbols.join(', ')}</p>}
        <p>Modeled gross exposure: {fmtUsd(r.coverage.modeledGrossExposure)} · {r.coverage.commonDays} common days.</p>
        {r.holdings.some((h) => !h.markAvailable) && <p>Entry-price estimates for unmarked holdings, equity, exposure and concentration.</p>}
      </div>
      <dl className="mb-6 grid grid-cols-2 gap-5 sm:grid-cols-4">
        <Stat label="Gross exposure" value={fmtUsd(r.grossExposure, { decimals: 0 })} sub={r.exposurePct == null ? 'Equity share unavailable' : `${fmtPct(r.exposurePct)} of equity`} />
        <Stat label="Net exposure" value={`${r.netExposure >= 0 ? '+' : '-'}${fmtUsd(Math.abs(r.netExposure), { decimals: 0 })}`} sub="signed long minus short" />
        <Stat label="Equity" value={fmtUsd(r.equity, { decimals: 0 })} />
        <Stat label="Prediction stakes" value={fmtUsd(r.predictionStakes, { decimals: 0 })} sub={r.predictionStakesPct == null ? 'Equity share unavailable' : `${fmtPct(r.predictionStakesPct, { decimals: 2 })} of equity`} />
      </dl>
      <p className="mb-5 text-[12px] text-muted">Daily cash-asset risk excludes outcome-contract tail risk. Concentration describes gross weights, not diversification.</p>
      <dl className="grid grid-cols-2 gap-x-6 gap-y-5 sm:grid-cols-3 lg:grid-cols-6">
        <Stat label="Top holding" value={top ? `${top.symbol} ${fmtPct(top.pct, { decimals: 0 })}` : '—'} sub="of gross exposure" />
        <Stat label="Top three" value={c ? fmtPct(c.top3Pct, { decimals: 0 }) : '—'} sub={c ? `${c.effective} effective positions` : undefined} />
        <Stat label="Beta to BTC" value={r.betaBtc == null ? '—' : r.betaBtc.toFixed(2)} sub="1% BTC move → % of equity" />
        <Stat label="1-day VaR · 95%" value={r.var ? fmtUsd(r.var.usd, { decimals: 0 }) : '—'} sub={r.var ? `${fmtPct(r.var.pct, { decimals: 1 })} of equity · CVaR ${fmtUsd(r.var.cvarUsd, { decimals: 0 })}` : 'Whole-book risk unavailable'} />
        <Stat label="Volatility · ann." value={r.volAnnPct == null ? '—' : fmtPct(r.volAnnPct, { decimals: 0 })} sub="of equity, from daily moves" />
        <Stat label="Drawdown · 30d" value={r.drawdown30dPct == null ? '—' : fmtPct(r.drawdown30dPct, { decimals: 1 })} sub="largest fall of your equity" tone={(r.drawdown30dPct ?? 0) >= 10 ? 'down' : 'neutral'} />
      </dl>

      <div className="mt-6 grid gap-6 lg:grid-cols-[minmax(0,1.2fr)_minmax(0,1fr)]">
        <section aria-labelledby="exposure-by-holding">
          <h4 id="exposure-by-holding" className="label">
            Exposure by holding
          </h4>
          <ul className="mt-3 space-y-2">
            {r.holdings.slice(0, MAX_BARS).map((h) => (
              <HoldingBar key={h.symbol} holding={h} />
            ))}
          </ul>
          {r.holdings.length > MAX_BARS && (
            <p className="mt-2 text-[12px] text-muted">+ {r.holdings.length - MAX_BARS} more in the positions table below.</p>
          )}
          <div className="mt-4">
            <h4 className="label">Move together</h4>
            {r.clusters.length > 0 ? (
              <ul className="mt-2 flex flex-wrap gap-2" aria-label="Clusters">
                {r.clusters.map((cl) => (
                  <li key={cl.symbols.join('-')} className="inline-flex items-center gap-2 rounded-lg bg-accent-soft px-2.5 py-1.5 text-[12.5px] text-text">
                    <span className="font-mono font-medium">{cl.symbols.join(' · ')}</span>
                    <span className="text-muted">
                      directional ρ ≥ {cl.rho.toFixed(2)} · {fmtPct(cl.pct, { decimals: 0 })} of exposure · {cl.days} minimum shared days
                    </span>
                  </li>
                ))}
              </ul>
            ) : (
              <p className="mt-2 text-[12.5px] text-muted">
                {r.positions < 2 ? 'Needs at least two positions.' : 'No measured high-correlation group supplied.'}
              </p>
            )}
            <p className="mt-2 text-[12px] text-muted">Groups use every measured pair in position direction. An absent group does not establish independence.</p>
            <h4 className="label mt-4">Asset-return correlations</h4>
            <ul aria-label="Asset correlations" className="mt-2 space-y-2 text-[12px] text-muted">
              {r.correlations.map((p) => <li key={`${p.a}-${p.b}`}><span className="font-mono text-text">{p.a} · {p.b}</span> · {p.rho == null ? 'Unknown' : `ρ ${p.rho.toFixed(2)}`} · {p.days} shared days</li>)}
            </ul>
            {!r.correlations.length && <p className="mt-2 text-[12px] text-muted">No pair measurements supplied.</p>}
          </div>
        </section>

        <section aria-labelledby="stress-heading">
          <h4 id="stress-heading" className="label">
            Stress · via betas
          </h4>
          <table aria-label="BTC-reference stress" className="mt-2 w-full text-[13px]">
            <thead className="text-left">
              <tr>
                <th className={th}>Scenario</th>
                <th className={cx(th, 'text-right')}>Est. P&amp;L</th>
                <th className={cx(th, 'text-right')}>Of equity</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {r.stress.map((s) => (
                <tr key={s.scenario}>
                  <td className="py-2 pr-3 whitespace-normal">{s.scenario}</td>
                  <td className={cx(td, 'text-right')}>
                    <PriceChange value={s.estPnl} kind="usd" className="text-[13px]" />
                  </td>
                  <td className={cx(td, 'text-right font-mono text-muted tabular-nums')}>{fmtPct(s.estPnlPct, { sign: true })}</td>
                </tr>
              ))}
              {r.worstDay && (
                <tr>
                  <td className="py-2 pr-3 whitespace-normal">
                    Current-book hypothetical worst day <span className="text-muted">· {fmtDate(r.worstDay.date)}</span>
                  </td>
                  <td className={cx(td, 'text-right')}>
                    <PriceChange value={r.worstDay.pnl} kind="usd" className="text-[13px]" />
                  </td>
                  <td className={cx(td, 'text-right font-mono text-muted tabular-nums')}>{fmtPct(r.worstDay.pnlPct, { sign: true })}</td>
                </tr>
              )}
            </tbody>
          </table>

          <h4 className="label mt-4">Stop coverage</h4>
          <p className="mt-2 text-[13px] text-text">{r.stops.covered} covered · {uncovered} of {r.positions} uncovered · {fmtPct(r.stops.uncoveredPct, { decimals: 0 })} of exposure uncovered.</p>
          {uncovered > 0 && <p className="mt-1 text-[12px] text-muted">{r.stops.uncovered.join(', ')} — missing, crossed or unknown stop levels.</p>}
          <p className="mt-2 text-[13px] text-text">{fmtUsd(r.stops.atRiskUsd, { decimals: 0 })} distance to covered stop levels.</p>
          <p className="mt-1 text-[12px] text-muted">Before fees, slippage and execution limitations; dormant halted levels may not fill. This is not a guaranteed loss.</p>
          {!r.stress.length && <p className="mt-3 text-[12px] text-muted">BTC stress unavailable for this book.</p>}
        </section>
      </div>

      {notes.length > 0 && (
        <section aria-labelledby="notes-heading" className="mt-6">
          <h4 id="notes-heading" className="label">
            What the numbers say
          </h4>
          <ul className="mt-2 space-y-1.5 text-[13px] leading-relaxed text-text">
            {notes.map((n) => (
              <li key={n} className="flex gap-2">
                <span aria-hidden="true" className="text-accent-ink">
                  —
                </span>
                {n}
              </li>
            ))}
          </ul>
        </section>
      )}
    </>
  )
}

/** One thin bar per holding (one hue; the status dot is the only colour that means something, and it has a label). */
function HoldingBar({ holding: h }: { holding: ReviewHolding }) {
  const title = `${h.symbol} ${h.side}: ${fmtUsd(h.value)} · ${fmtPct(h.pct, { decimals: 1 })} of exposure${h.beta == null ? '' : ` · β ${h.beta.toFixed(2)}`}`
  return (
    <li className="grid grid-cols-[4.5rem_minmax(0,1fr)_3rem] items-center gap-x-3 gap-y-1 text-[12.5px]" title={title}>
      <span className="font-mono font-medium text-text">
        {h.symbol}
        <span className={cx('ml-1 text-[10px]', h.side === 'long' ? 'text-up' : 'text-down')} aria-label={h.side}>
          {h.side === 'long' ? '▲' : '▼'}
        </span>
      </span>
      <span className="h-2 overflow-hidden rounded-r-[4px] bg-surface-2" role="meter" aria-valuenow={h.pct} aria-valuemin={0} aria-valuemax={100} aria-label={`${h.symbol} share of exposure`}>
        <span className="block h-full rounded-r-[4px] bg-ink" style={{ width: `${Math.max(1, Math.min(100, h.pct))}%` }} />
      </span>
      <span className="text-right font-mono text-text tabular-nums">{fmtPct(h.pct, { decimals: 0 })}</span>
      <span className="col-span-3 break-words font-mono text-[11px] text-muted tabular-nums">
        {h.halted ? 'halted' : h.beta == null ? 'β —' : `β ${h.beta.toFixed(2)}`}
        <span className={cx('ml-1.5', STOP_TONE[h.stopStatus])}>{`Stop: ${h.stopStatus}`}</span>
      </span>
    </li>
  )
}

const th = 'label py-1.5 pr-3 font-medium whitespace-nowrap last:pr-0'
const td = 'py-2 pr-3 whitespace-nowrap last:pr-0'

// The agent's "price_alert" card: an automation the user sets with one tap (POST /api/alerts, source agent, the card's
// proposalId, so a card sets one alert). A price trigger, a daily/weekday/weekly schedule (set_schedule), a
// prediction-market odds trigger (set_odds_alert) or an all-time-high drawdown trigger that notifies or sells
// part of the paper long (set_drawdown_alert). Research and thesis proposals share this confirmation card.
import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate } from 'react-router'
import { AssetIcon } from '../../components/ui/AssetIcon'
import { Badge } from '../../components/ui/Badge'
import { Button } from '../../components/ui/Button'
import { Card } from '../../components/ui/Card'
import { IconAgent, IconAlert, IconArrowRight, IconBolt, IconCheck, IconSignal } from '../../components/ui/icons'
import { ApiError, api } from '../../lib/api'
import { cx } from '../../lib/cx'
import { fmtPct } from '../../lib/format'
import type { Alert, PriceAlertProposal } from '../../lib/types'
import { actionLines, ATH_WITHHELD, athDate, athWithheld, deliverText, drawdownText, fmtIn, notifyWhenText, oddsText, Pipeline, scheduleText } from '../automations/pipeline'
import { pctLabel } from '../forecasts/OddsAlertButton'

export function AlertCard({ data }: { data: PriceAlertProposal }) {
  const navigate = useNavigate()
  const [stored, setStored] = useState<Alert['status'] | 'set' | null>(null) // 'set' = set from this card just now
  const [firedPrice, setFiredPrice] = useState<number | null>(null)
  const [firedCurrency, setFiredCurrency] = useState<string | null>(null)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const confirming = useRef(false)
  const confirmed = useRef(false)
  const symbol = data.symbol
  const scheduled = data.trigger === 'schedule'
  const odds = data.trigger === 'odds'
  const drawdown = data.trigger === 'drawdown'
  const research = data.action === 'research'
  const order = !drawdown && (data.action === 'buy' || data.action === 'sell')
  const ccy = data.ath?.currency ?? data.currency ?? 'USD'
  const withheld = drawdown && data.ath?.source === 'yahoo' && athWithheld(data.ath.ath, data.markPrice) // an unusable high: not shown, and no alert from it
  const past = drawdown && !withheld && data.markPrice != null && data.threshold != null && data.markPrice <= data.threshold
  const when = scheduleText(data.every, data.at, data.weekday)

  // A reloaded chat shows what became of the card's alert.
  useEffect(() => {
    const ctrl = new AbortController()
    api
      .alerts('all', ctrl.signal)
      .then(({ alerts }) => {
        if (ctrl.signal.aborted || confirmed.current) return
        const mine = alerts.find((a) => a.proposalId === data.proposalId)
        if (mine) {
          setStored(mine.status)
          setFiredPrice(mine.firedPrice)
          setFiredCurrency(mine.drawdown?.currency ?? mine.currency ?? 'USD')
        }
      })
      .catch(() => {}) // the card still works: a repeat Set answers 409 "already set"
    return () => ctrl.abort()
  }, [data.proposalId])

  async function set() {
    if (confirming.current || confirmed.current || stored || withheld) return
    confirming.current = true
    setPending(true)
    setError(null)
    try {
      await api.createAlert({
        ...(!research || symbol ? { symbol } : {}),
        ...(scheduled
          ? { trigger: 'schedule', every: data.every ?? undefined, at: data.at ?? undefined, weekday: data.weekday ?? undefined }
          : odds
            ? { trigger: 'odds', forecastId: data.forecastId, op: data.op ?? undefined, price: data.price ?? undefined }
            : drawdown
              ? { trigger: 'drawdown', drawdownPct: data.drawdownPct, ...(data.action === 'sell' ? { sellPct: data.sellPct ?? 100 } : {}) }
              : { op: data.op ?? undefined, price: data.price ?? undefined, ...(data.currency && { currency: data.currency }) }),
        action: data.action,
        ...(research ? { prompt: data.prompt, thesis: data.thesis, notifyWhen: data.notifyWhen } : {}),
        ...(order ? { notional: data.notional ?? undefined } : {}),
        ...(order && !scheduled ? { stopLoss: data.stopLoss ?? undefined, takeProfit: data.takeProfit ?? undefined } : {}),
        note: data.note || undefined,
        source: 'agent',
        proposalId: data.proposalId,
      })
      confirmed.current = true
      setStored('set')
    } catch (err) {
      if (err instanceof ApiError && err.status === 409 && /already set/i.test(err.message)) {
        confirmed.current = true
        setStored('set')
      } else setError(err instanceof ApiError ? err.message : 'The automation could not be set. Please try again.')
    } finally {
      confirming.current = false
      setPending(false)
    }
  }

  // ponytail: opens a fresh chat with the prompt; AgentCard has no hook into the current thread's composer.
  const askAgain = () =>
    navigate('/', {
      state: {
        prompt: scheduled
          ? `My ${symbol || 'research'} automation (${when}) was refused: ${error} Suggest a fix.`
          : odds
            ? `My Polymarket alert at ${pctLabel(data.price ?? 0)} was refused: ${error} Suggest a new level from here.`
            : drawdown
              ? `My ${symbol} drawdown alert (${data.drawdownPct}% below its all-time high) was refused: ${error} Suggest a fix.`
              : `My ${symbol} alert at ${fmtIn(data.price, ccy)} was refused: ${error} Suggest a new level from here.`,
        context: odds ? { forecastId: data.forecastId } : symbol ? { symbol } : undefined,
      },
    })

  const verb = data.action === 'buy' ? 'Buy' : 'Sell'
  const title = research
    ? data.thesis
      ? `Thesis tracker${symbol ? ` · ${symbol}` : ''}`
      : `Scheduled AI research${symbol ? ` · ${symbol}` : ''}`
    : odds
      ? `Polymarket ${data.op ?? 'above'} ${pctLabel(data.price ?? 0)}`
      : drawdown
        ? data.action === 'sell'
          ? `Sell ${data.sellPct ?? 100}% of ${symbol} on a ${data.drawdownPct}% drawdown`
          : `Watch ${symbol} for a ${data.drawdownPct}% drawdown`
      : scheduled
        ? order
          ? `${verb} ${symbol} every ${data.every === 'weekdays' ? 'weekday' : data.every}`
          : `${{ day: 'Daily', weekdays: 'Weekday', week: 'Weekly' }[data.every ?? 'day']} brief · ${symbol}`
        : order
          ? `${verb} ${symbol} at a level`
          : `Watch ${symbol}`
  const [action, levels] = actionLines({ ...data, trigger: drawdown ? 'drawdown' : scheduled ? 'schedule' : 'price' })
  const noun = scheduled ? 'Automation' : 'Alert'
  const done = stored === 'set' || stored === 'active'

  return (
    <Card className="overflow-hidden">
      <div className="flex items-start justify-between gap-3 px-4 pt-4">
        <div className="flex min-w-0 items-center gap-3">
          {odds || (research && !symbol) ? (
            <span aria-hidden="true" className="grid size-[38px] shrink-0 place-items-center rounded-full bg-surface-2 text-accent-ink">
              {odds ? <IconSignal size={18} /> : <IconAgent size={18} />}
            </span>
          ) : (
            <AssetIcon symbol={symbol} size={38} />
          )}
          <div className="min-w-0">
            <div className="label">{scheduled ? 'Scheduled automation' : odds ? 'Prediction-market alert' : drawdown ? 'Drawdown alert' : 'Price alert'} · paper</div>
            <div className="font-display text-[19px] leading-snug font-medium text-text">{title}</div>
          </div>
        </div>
        <Badge tone="accent">
          <IconBolt size={11} /> Automation
        </Badge>
      </div>

      <div className="mx-4 mt-4">
        <Pipeline
          research={research}
          trigger={
            scheduled ? (
              when
            ) : drawdown && data.ath ? (
              <>
                {drawdownText(symbol, data.drawdownPct ?? 0)}
                {!withheld && <> · ≤ <b className="font-mono font-semibold tabular-nums">{fmtIn(data.threshold, ccy)}</b></>}
                {withheld ? (
                  <span className="mt-0.5 block text-[12px] text-down">
                    {ATH_WITHHELD}
                    {data.markPrice != null && <span className="font-mono tabular-nums"> Now {fmtIn(data.markPrice, ccy)}.</span>}
                  </span>
                ) : (
                  <span className="mt-0.5 block font-mono text-[12px] text-muted tabular-nums">
                    ATH {fmtIn(data.ath.ath, ccy)} · {athDate(data.ath.athAt)}
                    {data.markPrice != null && data.fromAthPct != null &&
                      ` · now ${fmtIn(data.markPrice, ccy)} (${fmtPct(Math.abs(data.fromAthPct), { decimals: 1 })} below)`}
                  </span>
                )}
                <span className="mt-0.5 block text-[12px] text-muted [overflow-wrap:anywhere]">
                  {data.ath.sourceLabel}
                  {data.ath.basis === 'listing' && ` · ${ccy} listing price`}
                </span>
              </>
            ) : odds ? (
              <>
                {oddsText(data.market?.question, data.op, data.price)}
                {data.markPrice != null && (
                  <span className="font-mono text-[12px] text-muted tabular-nums"> · now {pctLabel(data.markPrice)}</span>
                )}
              </>
            ) : (
              <>
                {symbol} {data.op === 'below' ? 'falls below' : 'rises above'}{' '}
                <b className="font-mono font-semibold tabular-nums">{fmtIn(data.price, ccy)}</b>
                {data.distancePct != null && (
                  <span className="font-mono text-[12px] text-muted tabular-nums">
                    {' '}
                    · {fmtPct(Math.abs(data.distancePct), { decimals: 1 })} {data.distancePct < 0 ? 'below' : 'above'} now
                  </span>
                )}
              </>
            )
          }
          action={
            <>
              {action}
              {levels && <span className="mt-0.5 block font-mono text-[12px] text-muted tabular-nums">{levels}</span>}
              {research && data.thesis && (
                <div className="mt-2 space-y-1 text-[13px] text-muted">
                  <p><b className="font-medium text-text">Thesis:</b> {data.thesis.statement}</p>
                  <p><b className="font-medium text-text">Invalidation:</b> {data.thesis.invalidation}</p>
                </div>
              )}
            </>
          }
          deliver={
            <>
              {deliverText(data.action)}
              {research && <span className="mt-1 block text-[12px] text-muted">{notifyWhenText(data.notifyWhen)}</span>}
            </>
          }
        />
      </div>

      {data.note && <p className="px-4 pt-3.5 text-[14px] leading-relaxed text-pretty text-text">{data.note}</p>}
      {past && !stored && (
        <p className="mx-4 mt-3.5 flex items-start gap-2 rounded-lg bg-down/8 px-3 py-2 text-[13px] text-down">
          <IconAlert size={15} className="mt-px shrink-0" />
          {symbol} is already past this level: once set, it fires on the next check (within 15 s).
        </p>
      )}

      {done ? (
        <div role="status" className="m-4 flex flex-wrap items-center gap-x-3 gap-y-2 rounded-xl bg-up/8 px-4 py-3">
          <span className="grid size-7 shrink-0 place-items-center rounded-full bg-up/15 text-up">
            <IconCheck size={15} strokeWidth={2.4} />
          </span>
          <span className="flex-1 text-[14px] font-medium text-text">
            {noun} set{stored === 'active' && ' · Active'}
          </span>
          <Link to="/automations" className="inline-flex items-center gap-1 text-[13px] font-medium text-accent-ink hover:underline">
            Manage in Automations <IconArrowRight size={14} />
          </Link>
        </div>
      ) : stored ? (
        <div
          role="status"
          className="m-4 flex flex-wrap items-center gap-x-3 gap-y-2 rounded-xl bg-surface-2 px-4 py-3 text-[14px] text-text"
        >
          <span className="flex-1">
            {stored === 'fired'
              ? `Fired at ${odds ? pctLabel(firedPrice ?? 0) : fmtIn(firedPrice, firedCurrency ?? ccy)}`
              : stored === 'failed'
                ? 'Fired, but the order failed'
                : stored === 'paused'
                  ? 'Paused'
                  : 'Cancelled'}
          </span>
          <Link to="/automations" className="inline-flex items-center gap-1 text-[13px] font-medium text-accent-ink hover:underline">
            {stored === 'paused' ? 'Manage in Automations' : 'Automations'} <IconArrowRight size={14} />
          </Link>
        </div>
      ) : (
        <div className="flex flex-wrap items-center justify-end gap-x-3 gap-y-2 px-4 pt-4 pb-4">
          <p className={cx('mr-auto text-[12px]', withheld ? 'text-down' : 'text-muted')}>
            {withheld
              ? `A drawdown alert needs a usable all-time high, so this one can't be set for ${symbol}.`
              : research
                ? '2 AI credits per run. Setting this automation is free.'
                : scheduled
                  ? 'No model call, 0 credits a run.'
                  : odds
                    ? 'Re-checked about once a minute, 0 credits.'
                    : drawdown && data.action === 'sell'
                      ? 'Checked every 15 s. The sale never opens or adds to a short.'
                      : 'Checked every 15 s.'}
            {!withheld && ' Nothing is set until you tap.'}
          </p>
          <Button variant="primary" loading={pending} onClick={set} disabled={withheld} className="w-full sm:w-auto">
            Set {noun.toLowerCase()}
          </Button>
          {error && (
            <div role="alert" className="flex w-full flex-wrap items-start gap-2 rounded-lg bg-down/8 px-3 py-2 text-[13px] text-down">
              <IconAlert size={15} className="mt-px shrink-0" />
              <span className="min-w-0 flex-1">{error}</span>
              <button type="button" onClick={askAgain} className="font-medium text-accent-ink hover:underline">
                {scheduled ? 'Ask the agent for a fix' : 'Ask the agent for a new level'}
              </button>
            </div>
          )}
        </div>
      )}
    </Card>
  )
}

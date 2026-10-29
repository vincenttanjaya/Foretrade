// Paper autopilot: deploy a strategy, watch it trade, stop it (contract "Autopilot"). Checked by autopilot.test.tsx.
import { useRef, useState, type FormEvent, type ReactNode } from 'react'
import { Link } from 'react-router'
import { useToast } from '../../components/toast'
import { Badge, type BadgeTone } from '../../components/ui/Badge'
import { Button } from '../../components/ui/Button'
import { Card } from '../../components/ui/Card'
import { IconAutopilot } from '../../components/ui/icons'
import { Input } from '../../components/ui/Input'
import { PriceChange } from '../../components/ui/PriceChange'
import { api, ApiError } from '../../lib/api'
import { cx } from '../../lib/cx'
import { errorMessage } from '../../lib/errors'
import { fmtPct, fmtPrice, fmtQty, fmtUsd } from '../../lib/format'
import { usePortfolio } from '../../lib/portfolio'
import type { Autopilot, AutopilotLogEntry, Strategy, StrategySpec, Trade } from '../../lib/types'
import { usePoll } from '../../lib/usePoll'
import { tuneText, utcTime } from './strategy'

export const ALLOCATIONS = [1000, 5000, 10_000]
export const DRAWDOWNS = [5, 10, 20]

/** The allocation field as dollars, or null outside $100–$100,000. "$5,000" and "5000" both parse. */
export function parseAllocation(text: string): number | null {
  const v = Number(text.replace(/[$,\s]/g, ''))
  return text.trim() !== '' && v >= 100 && v <= 100_000 ? v : null
}

type Levels = Pick<StrategySpec, 'stopLossPct' | 'takeProfitPct' | 'trailingStopPct'>

/** What every check of a deployed spec does, in one line, with the levels it deploys with (the spec's by default). */
export function deploySummary(spec: StrategySpec, r: Levels = spec) {
  const risk = `${r.stopLossPct ? `stop ${r.stopLossPct}%` : 'no stop'} / ${r.takeProfitPct ? `target ${r.takeProfitPct}%` : 'no target'}`
  return `Checks every closed ${spec.interval} bar · each entry uses ${spec.positionPct}% of the allocation · ${risk}${r.trailingStopPct ? ` · trailing ${r.trailingStopPct}%` : ''} · paper only, stop any time`
}

/** "stop 3.2% · target 6.4% · trailing 3%" (each part only when set). */
export const riskText = (a: Pick<Autopilot, 'stopLossPct' | 'takeProfitPct' | 'trailingStopPct'>) =>
  [a.stopLossPct && `stop ${a.stopLossPct}%`, a.takeProfitPct && `target ${a.takeProfitPct}%`, a.trailingStopPct && `trailing ${a.trailingStopPct}%`]
    .filter(Boolean)
    .join(' · ')

export type RiskInput = { stop: string; take: string; trail: string }

/** The deploy dialog's levels: a stop and a target are required (0.1–50%), a trailing stop optional (0.5–50%). */
export function parseRisk(r: RiskInput) {
  const errors: Partial<Record<keyof RiskInput, string>> = {}
  const pct = (k: keyof RiskInput, lo: number, required: boolean) => {
    const text = r[k].trim()
    const v = Number(text)
    if (!text) {
      if (required) errors[k] = 'Required: every entry carries one.'
    } else if (v >= lo && v <= 50) return v
    else errors[k] = `Use ${lo}–50%.`
  }
  const values = { stopLossPct: pct('stop', 0.1, true), takeProfitPct: pct('take', 0.1, true), trailingStopPct: pct('trail', 0.5, false) }
  return { values: Object.keys(errors).length ? null : values, errors }
}

/** A suggested stop: 2× the ATR in percent of price, 0.5–20%, to one decimal. */
export const atrStop = (atr14: number, price: number) => Math.min(20, Math.max(0.5, Math.round((2000 * atr14) / price) / 10))

/** "Long 0.42 SOL @ $142.10", "Short 1,200 DOGE @ $0.2013" or "Flat". */
export function positionText(a: Pick<Autopilot, 'positionQty' | 'avgPrice' | 'symbol'>) {
  if (!a.positionQty) return 'Flat'
  return `${a.positionQty > 0 ? 'Long' : 'Short'} ${fmtQty(Math.abs(a.positionQty))} ${a.symbol} @ ${fmtPrice(a.avgPrice)}`
}

export const statusText = (a: Pick<Autopilot, 'status' | 'stopReason'>) =>
  a.status === 'running'
    ? 'Running'
    : a.stopReason === 'account_disabled'
      ? 'Stopped: account disabled'
      : `Stopped (${a.stopReason === 'max_drawdown' ? 'max drawdown' : 'user'})`

/** The check line under a running autopilot. */
export function checkText(a: Pick<Autopilot, 'status' | 'positionQty' | 'nextCheckAt'>) {
  if (a.status !== 'running') return null
  if (!a.nextCheckAt) return 'Waiting for market data · it retries every 30 s'
  return `${a.positionQty ? 'Next check' : 'Watching for entry · next check'} ${utcTime(a.nextCheckAt)}`
}

function Chip({ pressed, onClick, children }: { pressed: boolean; onClick: () => void; children: ReactNode }) {
  return (
    <button
      type="button"
      aria-pressed={pressed}
      onClick={onClick}
      className={cx(
        'h-9 rounded-full border px-3.5 font-mono text-[13px] tabular-nums transition-colors',
        pressed ? 'border-accent bg-accent-soft text-text' : 'border-border bg-surface text-text hover:border-accent/50',
      )}
    >
      {children}
    </button>
  )
}

const dialogClass =
  'card m-auto w-[calc(100%-2rem)] max-w-lg animate-fade-up p-6 text-text backdrop:bg-[rgb(15_27_45/0.45)] backdrop:backdrop-blur-[2px]'

/** "Deploy to autopilot" and its dialog: allocation, drawdown cap, what it will do, Deploy (paper). */
export function DeployButton({ s, running, onDeployed }: { s: Strategy; running: boolean; onDeployed: (a: Autopilot) => void }) {
  const dialog = useRef<HTMLDialogElement>(null)
  const { toast } = useToast()
  const { setPortfolio } = usePortfolio()
  const [alloc, setAlloc] = useState('5000')
  const [dd, setDd] = useState(10)
  const { stopLossPct: sl, takeProfitPct: tp, trailingStopPct: trail } = s.spec
  // The spec's levels; a missing target defaults to 2× the stop, a missing stop to 2× the ATR (fetched on open).
  const [risk, setRisk] = useState<RiskInput>({ stop: tuneText(sl), take: tp ? String(tp) : sl ? String(Math.round(sl * 20) / 10) : '', trail: tuneText(trail) })
  const [hint, setHint] = useState(sl && !tp ? 'Target suggested at 2× the stop. Edit as you like.' : '')
  const asked = useRef(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<ReactNode>(null)
  const amount = parseAllocation(alloc)
  const levels = parseRisk(risk)

  function open() {
    dialog.current?.showModal()
    if (sl || asked.current) return
    asked.current = true
    api.analysis(s.spec.symbol, s.spec.interval).then(
      ({ analysis: a }) => {
        const stop = atrStop(a.atr14, a.price)
        setRisk((r) => ({ ...r, stop: r.stop || String(stop), take: r.take || String(Math.round(stop * 20) / 10) }))
        setHint(`Suggested from 2× the ${s.spec.interval} ATR (${fmtPct((a.atr14 / a.price) * 100, { decimals: 1 })}). Edit as you like.`)
      },
      () => setHint('Set a stop and a target: every autopilot entry carries both.'),
    )
  }

  async function deploy(e: FormEvent) {
    e.preventDefault()
    if (amount == null || !levels.values || busy) return
    setBusy(true)
    setError(null)
    try {
      const r = await api.deployAutopilot({ strategyId: s.id, allocationUsd: amount, maxDrawdownPct: dd, ...levels.values })
      if (r.portfolio) setPortfolio(r.portfolio)
      dialog.current?.close()
      onDeployed(r.autopilot)
      toast(`Autopilot running · ${r.autopilot.name}`)
    } catch (err) {
      if (err instanceof ApiError && err.status === 409) {
        // One runs at a time: point at the one that does.
        const other = await api
          .autopilots()
          .then((r) => r.autopilots.find((a) => a.status === 'running'))
          .catch(() => undefined)
        setError(
          <>
            {err.message}{' '}
            {other && (
              <Link to={`/strategies/${other.strategyId}`} onClick={() => dialog.current?.close()} className="font-medium text-text underline">
                Open {other.name}
              </Link>
            )}
          </>,
        )
      } else setError(errorMessage(err, 'Could not deploy the autopilot. Please try again.'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <Button variant="primary" disabled={running} onClick={open}>
        <IconAutopilot size={16} /> {running ? 'Autopilot running' : 'Deploy to autopilot'}
      </Button>
      <dialog ref={dialog} aria-labelledby="deploy-title" onCancel={(e) => busy && e.preventDefault()} className={dialogClass}>
        <form onSubmit={deploy}>
          <div className="label mb-2">Autopilot · paper trading</div>
          <h2 id="deploy-title" className="font-display text-2xl leading-tight font-medium text-balance">
            Deploy “{s.name}”
          </h2>
          <p className="mt-2 text-sm leading-relaxed text-muted">
            It trades your paper account by itself with the same rules as the backtest. No AI calls, no credits.
          </p>

          <fieldset className="mt-5">
            <legend className="label mb-2">Allocation</legend>
            <div className="flex flex-wrap items-center gap-2">
              {ALLOCATIONS.map((v) => (
                <Chip key={v} pressed={amount === v} onClick={() => setAlloc(String(v))}>
                  {fmtUsd(v, { compact: true })}
                </Chip>
              ))}
              <div className="relative w-36">
                <span aria-hidden="true" className="pointer-events-none absolute top-1/2 left-3 -translate-y-1/2 font-mono text-[13px] text-muted">
                  $
                </span>
                <Input
                  aria-label="Allocation in USD"
                  inputMode="numeric"
                  value={alloc}
                  disabled={busy}
                  aria-invalid={amount == null || undefined}
                  aria-describedby="alloc-err"
                  onChange={(e) => setAlloc(e.target.value)}
                  className="h-9 pl-6 font-mono tabular-nums"
                />
              </div>
            </div>
            <p id="alloc-err" className="mt-1 min-h-4 text-[11.5px] leading-tight text-down">
              {amount == null ? 'Use $100–$100,000.' : ''}
            </p>
          </fieldset>

          <fieldset className="mt-2">
            <legend className="label mb-2">Max drawdown cap</legend>
            <div className="flex flex-wrap gap-2">
              {DRAWDOWNS.map((v) => (
                <Chip key={v} pressed={dd === v} onClick={() => setDd(v)}>
                  {v}%
                </Chip>
              ))}
            </div>
            <p className="mt-2 text-[12.5px] text-muted">A {dd}% fall from its best equity closes the position and stops it.</p>
          </fieldset>

          <fieldset className="mt-4">
            <legend className="label mb-2">Every entry's stop and target</legend>
            <div className="grid grid-cols-3 gap-2">
              {(
                [
                  ['stop', 'Stop loss %', ''],
                  ['take', 'Take profit %', ''],
                  ['trail', 'Trailing stop %', 'Off'],
                ] as const
              ).map(([k, label, placeholder]) => (
                <div key={k} className="min-w-0">
                  <label htmlFor={`deploy-${k}`} className="mb-1 block text-[12px] text-muted">
                    {label}
                  </label>
                  <Input
                    id={`deploy-${k}`}
                    inputMode="decimal"
                    value={risk[k]}
                    placeholder={placeholder}
                    disabled={busy}
                    aria-invalid={levels.errors[k] ? true : undefined}
                    aria-describedby={`deploy-${k}-err`}
                    onChange={(e) => setRisk({ ...risk, [k]: e.target.value })}
                    className="h-9 font-mono tabular-nums"
                  />
                  <p id={`deploy-${k}-err`} className="mt-1 min-h-4 text-[11.5px] leading-tight text-down">
                    {levels.errors[k]}
                  </p>
                </div>
              ))}
            </div>
            {hint && <p className="text-[12.5px] text-muted">{hint}</p>}
          </fieldset>

          <p className="mt-5 rounded-lg bg-surface-2 px-3.5 py-2.5 font-mono text-[11.5px] leading-relaxed text-text">
            {deploySummary(s.spec, levels.values ?? s.spec)}
          </p>

          <div aria-live="polite" className="mt-3 min-h-5 text-[13px]">
            {error && (
              <p role="alert" className="text-down">
                {error}
              </p>
            )}
          </div>
          <div className="mt-3 flex flex-wrap justify-end gap-2">
            <Button variant="secondary" disabled={busy} onClick={() => dialog.current?.close()}>
              Cancel
            </Button>
            <Button type="submit" variant="primary" loading={busy} disabled={amount == null || !levels.values}>
              Deploy (paper)
            </Button>
          </div>
        </form>
      </dialog>
    </>
  )
}

const STATUS_TONE = (a: Autopilot): BadgeTone => (a.status === 'running' ? 'up' : a.stopReason === 'max_drawdown' ? 'down' : 'neutral')

const LOG_DOT: Record<AutopilotLogEntry['kind'], string> = {
  deploy: 'bg-ink',
  entry: 'bg-accent',
  exit: 'bg-accent',
  sync: 'bg-muted',
  stop: 'bg-down',
  error: 'bg-down',
}

/**
 * A deployed autopilot: status, position, equity, P&L, drawdown against its cap, the next check, recent log and
 * fills, and Stop. Polls every 15 s while running. `compact` is the one-line strip on the strategies page.
 */
export function AutopilotCard({
  initial,
  compact = false,
  onChange,
  className,
}: {
  initial: Autopilot
  compact?: boolean
  onChange?: (a: Autopilot) => void
  className?: string
}) {
  const { toast } = useToast()
  const { setPortfolio } = usePortfolio()
  const dialog = useRef<HTMLDialogElement>(null)
  const [a, setA] = useState(initial)
  const [trades, setTrades] = useState<Trade[] | null>(null)
  const [busy, setBusy] = useState<'keep' | 'close' | null>(null)
  const [error, setError] = useState<string | null>(null)
  const running = a.status === 'running'
  const update = (next: Autopilot) => {
    setA(next)
    onChange?.(next)
  }

  usePoll(
    () =>
      api.autopilot(a.id).then(
        (r) => {
          update(r.autopilot)
          setTrades(r.trades ?? [])
        },
        () => {}, // keep the last state; the next poll retries
      ),
    15_000,
    running || trades === null,
  )

  async function stop(closePosition: boolean) {
    setBusy(closePosition ? 'close' : 'keep')
    setError(null)
    try {
      const r = await api.stopAutopilot(a.id, closePosition)
      update(r.autopilot)
      if (r.trade) setTrades((t) => [r.trade!, ...(t ?? [])])
      if (r.portfolio) setPortfolio(r.portfolio)
      dialog.current?.close()
      toast('Autopilot stopped')
    } catch (err) {
      setError(errorMessage(err, 'Could not stop the autopilot. Please try again.'))
    } finally {
      setBusy(null)
    }
  }

  const status = (
    <Badge tone={STATUS_TONE(a)}>
      {running && <span aria-hidden="true" className="mr-1 inline-block size-1.5 animate-pulse rounded-full bg-current" />}
      {statusText(a)}
    </Badge>
  )
  const stopButton = running && (
    <Button variant="danger" size="sm" onClick={() => dialog.current?.showModal()}>
      Stop
    </Button>
  )
  const pnl = (
    <span className="inline-flex flex-wrap items-baseline gap-x-1.5">
      <PriceChange value={a.pnl} kind="usd" />
      <span className="text-muted">({fmtPct(a.pnlPct, { sign: true })})</span>
    </span>
  )
  const check = checkText(a)
  const ddShare = a.drawdownPct != null && a.maxDrawdownPct > 0 ? Math.min(1, a.drawdownPct / a.maxDrawdownPct) : 0

  return (
    <Card className={cx('overflow-hidden', className)} aria-label={`Autopilot ${a.name}`} role="region">
      {compact ? (
        <div className="flex flex-wrap items-center gap-x-4 gap-y-2 px-5 py-3.5">
          <span className="inline-flex items-center gap-2 text-accent-ink">
            <IconAutopilot size={18} />
            <span className="label text-accent-ink">Autopilot</span>
          </span>
          {status}
          <Link to={`/strategies/${a.strategyId}`} className="min-w-0 truncate font-medium text-text hover:text-accent-ink">
            {a.name}
          </Link>
          <span className="font-mono text-[12.5px] text-text">{positionText(a)}</span>
          <span className="font-mono text-[12.5px]">{pnl}</span>
          {check && <span className="font-mono text-[11.5px] text-muted">{check}</span>}
          <span className="ml-auto">{stopButton}</span>
        </div>
      ) : (
        <>
          <div className="flex flex-wrap items-start justify-between gap-3 px-5 pt-5 pb-3">
            <div className="min-w-0">
              <div className="label mb-1 inline-flex items-center gap-1.5">
                <IconAutopilot size={14} /> Autopilot · paper · {fmtUsd(a.allocationUsd, { decimals: 0 })} · {a.maxDrawdownPct}% drawdown cap
              </div>
              <h2 className="flex flex-wrap items-center gap-2 font-display text-[19px] leading-snug font-medium text-text">
                {a.symbol} · {a.interval} {status}
              </h2>
              {riskText(a) && <p className="mt-1 font-mono text-[12px] text-muted">{riskText(a)}</p>}
            </div>
            {stopButton}
          </div>
          <dl className="grid grid-cols-2 gap-px border-y border-border bg-border sm:grid-cols-4">
            <Stat label="Position">{positionText(a)}</Stat>
            <Stat label="Equity">{fmtUsd(a.equity)}</Stat>
            <Stat label="P&L">{pnl}</Stat>
            <Stat label="Drawdown">
              <span>
                {fmtPct(a.drawdownPct, { decimals: 1 })}
                <span className="text-[12px] text-muted"> of {a.maxDrawdownPct}%</span>
              </span>
              <span
                role="meter"
                aria-label="Drawdown against the cap"
                aria-valuemin={0}
                aria-valuemax={a.maxDrawdownPct}
                aria-valuenow={a.drawdownPct ?? 0}
                className="mt-2 block h-1.5 overflow-hidden rounded-full bg-surface-2"
              >
                <span
                  className={cx('block h-full rounded-full transition-[width] duration-500', ddShare >= 0.75 ? 'bg-down' : 'bg-accent')}
                  style={{ width: `${ddShare * 100}%` }}
                />
              </span>
            </Stat>
          </dl>
          {check && <p className="border-b border-border px-5 py-2.5 font-mono text-[11.5px] text-muted">{check}</p>}
          <div className="grid gap-6 px-5 py-4 md:grid-cols-[minmax(0,1.4fr)_minmax(0,1fr)]">
            <section aria-label="Recent log">
              <h3 className="label mb-2">Log</h3>
              <ol className="flex flex-col gap-2">
                {a.log.slice(0, 6).map((e, i) => (
                  <li key={`${e.t}-${i}`} className="grid grid-cols-[auto_1fr] gap-x-2.5 text-[13px] leading-snug">
                    <span aria-hidden="true" className={cx('mt-1.5 size-1.5 rounded-full', LOG_DOT[e.kind] ?? 'bg-muted')} />
                    <span className="min-w-0">
                      <span className="text-text">{e.text}</span>{' '}
                      <span className="font-mono text-[11px] whitespace-nowrap text-muted">{utcTime(e.t, true)}</span>
                    </span>
                  </li>
                ))}
              </ol>
            </section>
            <section aria-label="Recent fills">
              <h3 className="label mb-2">Fills</h3>
              {trades?.length ? (
                <ul className="flex flex-col divide-y divide-border">
                  {trades.slice(0, 5).map((t) => (
                    <li key={t.id} className="flex flex-wrap items-baseline justify-between gap-x-3 py-1.5 font-mono text-[12.5px] tabular-nums">
                      <span className={t.side === 'buy' ? 'text-up' : 'text-down'}>{t.side === 'buy' ? 'Buy' : 'Sell'}</span>
                      <span className="flex-1 text-text">
                        {fmtQty(t.qty)} {t.symbol} @ {fmtPrice(t.price)}
                      </span>
                      <span className="text-[11px] text-muted">{utcTime(t.createdAt, true)}</span>
                    </li>
                  ))}
                </ul>
              ) : (
                <p className="text-[13px] text-muted">{trades ? 'No fills yet.' : 'Loading fills…'}</p>
              )}
            </section>
          </div>
        </>
      )}

      <dialog ref={dialog} aria-labelledby={`stop-${a.id}`} onCancel={(e) => busy && e.preventDefault()} className={dialogClass}>
        <div className="label mb-2">Autopilot</div>
        <h2 id={`stop-${a.id}`} className="font-display text-2xl leading-tight font-medium text-balance">
          Stop “{a.name}”?
        </h2>
        <p className="mt-2.5 text-sm leading-relaxed text-muted">
          {a.positionQty
            ? `It holds ${positionText(a)}. Keep it in your paper account, or close it at market now.`
            : 'It holds no position, so nothing is bought or sold.'}
        </p>
        <div aria-live="polite" className="mt-3 min-h-5 text-[13px]">
          {error && (
            <p role="alert" className="text-down">
              {error}
            </p>
          )}
        </div>
        <div className="mt-3 flex flex-wrap justify-end gap-2">
          <Button variant="ghost" disabled={!!busy} onClick={() => dialog.current?.close()}>
            Cancel
          </Button>
          {a.positionQty ? (
            <>
              <Button variant="secondary" loading={busy === 'keep'} disabled={!!busy} onClick={() => stop(false)}>
                Keep the position
              </Button>
              <Button variant="danger" loading={busy === 'close'} disabled={!!busy} onClick={() => stop(true)}>
                Close it at market
              </Button>
            </>
          ) : (
            <Button variant="danger" loading={busy === 'keep'} disabled={!!busy} onClick={() => stop(false)}>
              Stop autopilot
            </Button>
          )}
        </div>
      </dialog>
    </Card>
  )
}

function Stat({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="min-w-0 bg-surface px-5 py-3.5">
      <dt className="label truncate">{label}</dt>
      <dd className="mt-1.5 font-mono text-[15px] leading-tight font-medium text-text tabular-nums">{children}</dd>
    </div>
  )
}

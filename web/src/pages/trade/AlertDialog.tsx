import { useEffect, useId, useRef, useState, type FormEvent, type ReactNode, type RefObject } from 'react'
import { Link } from 'react-router'
import { useToast } from '../../components/toast'
import { Button } from '../../components/ui/Button'
import { IconBolt } from '../../components/ui/icons'
import { Input, Label, Select } from '../../components/ui/Input'
import { api } from '../../lib/api'
import { cx } from '../../lib/cx'
import { errorMessage } from '../../lib/errors'
import { fmtPct } from '../../lib/format'
import type { AlertOp, AlertRequest, Asset, AthQuote } from '../../lib/types'
import { ATH_WITHHELD, athDate, athWithheld, fmtIn } from '../automations/pipeline'

const dialogClass =
  'card m-auto w-[calc(100%-2rem)] max-w-md animate-fade-up p-6 text-text backdrop:bg-[rgb(15_27_45/0.45)] backdrop:backdrop-blur-[2px]'

type Kind = 'price' | 'drawdown'

type Props = {
  symbol: string
  asset?: Asset // the market row when the page has it; else read on open
  kind?: Kind // the tab it opens on
  label?: string
  ariaLabel?: string
  className?: string
  icon?: ReactNode
}

/**
 * "Set alert": a price or drawdown alert on symbol, set in a dialog without leaving the page (Trade page, screener).
 * A quote-feed stock's price level uses its original listing currency (JPY for 6501.T). Orders on a trigger,
 * schedules and research stay on the Automations page. The form mounts only while the dialog is open.
 */
export function AlertDialog({ symbol, asset, kind = 'price', label = 'Set alert', ariaLabel, className, icon = <IconBolt size={13} /> }: Props) {
  const id = useId()
  const dialog = useRef<HTMLDialogElement>(null)
  const [open, setOpen] = useState(false)
  return (
    <>
      <Button
        variant="secondary"
        size="sm"
        aria-label={ariaLabel}
        className={cx('h-8 gap-1.5 text-[12.5px]', className)}
        onClick={() => {
          setOpen(true)
          dialog.current?.showModal()
        }}
      >
        {icon} {label}
      </Button>
      <dialog ref={dialog} aria-labelledby={`${id}-title`} onClose={() => setOpen(false)} className={dialogClass}>
        {open && <AlertForm id={id} symbol={symbol} given={asset} initialKind={kind} dialog={dialog} />}
      </dialog>
    </>
  )
}

function AlertForm({ id, symbol, given, initialKind, dialog }: { id: string; symbol: string; given?: Asset; initialKind: Kind; dialog: RefObject<HTMLDialogElement | null> }) {
  const { toast } = useToast()
  const close = () => dialog.current?.close()
  const [read, setRead] = useState<Asset | undefined>()
  const asset = given ?? read
  const local = asset?.quoteFeed
  const currency = asset && asset.venue !== 'yahoo' ? 'USD' : local?.currency || asset?.localCurrency || undefined
  const [kind, setKind] = useState<Kind>(initialKind)
  const [op, setOp] = useState<AlertOp>('below')
  const [price, setPrice] = useState('')
  const [pct, setPct] = useState('')
  const [note, setNote] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [ath, setAth] = useState<{ quote: AthQuote | null; error: string | null } | null>(null)

  // A listed stock the page has no row for: read its price (and currency) on open.
  useEffect(() => {
    if (given) return
    const ctl = new AbortController()
    api.assets('named', [symbol], ctl.signal).then(
      ({ assets }) => setRead(assets.find((a) => a.symbol === symbol)),
      () => {},
    )
    return () => ctl.abort()
  }, [given, symbol])
  useEffect(() => {
    if (kind !== 'drawdown') return
    const ctl = new AbortController()
    setAth(null)
    api.alertATH(symbol, ctl.signal).then(
      ({ ath }) => setAth({ quote: ath, error: null }),
      (err) => !ctl.signal.aborted && setAth({ quote: null, error: errorMessage(err, `Could not read ${symbol}'s all-time high.`) }),
    )
    return () => ctl.abort()
  }, [kind, symbol])
  // Escape does not close the dialog mid-request.
  useEffect(() => {
    const d = dialog.current
    const guard = (e: Event) => busy && e.preventDefault()
    d?.addEventListener('cancel', guard)
    return () => d?.removeEventListener('cancel', guard)
  }, [busy, dialog])

  const quotePrice = currency === 'USD' ? asset?.price : local?.currency === currency ? local?.localPrice : undefined
  const now = quotePrice != null && Number.isFinite(quotePrice) && quotePrice > 0 ? quotePrice : undefined
  // Suggest a level 5% away once the price is known, until the user types one.
  const known = now != null
  useEffect(() => {
    if (known) setPrice((p) => p || String(roundLevel(now * (op === 'below' ? 0.95 : 1.05))))
  }, [known, currency]) // eslint-disable-line react-hooks/exhaustive-deps

  const level = Number(price)
  const validPrice = price.trim() !== '' && level > 0 && Number.isFinite(level)
  const crossed = validPrice && now != null && (op === 'above' ? level <= now : level >= now)
  const bps = Math.round(Number(pct) * 100)
  const validPct = pct.trim() !== '' && bps >= 1 && bps <= 9999
  const q = ath?.quote
  const withheld = q?.source === 'yahoo' && athWithheld(q.ath, q.price)
  const threshold = q && validPct && !withheld ? (q.ath * (10000 - bps)) / 10000 : null
  const ready = kind === 'price' ? !!currency && now != null && validPrice && !crossed : threshold != null

  async function submit(e: FormEvent) {
    e.preventDefault()
    if (!ready || busy) return
    setBusy(true)
    setError('')
    const req: AlertRequest =
      kind === 'price'
        ? { symbol, op, price: level, ...(currency !== 'USD' && { currency }), note: note.trim() || undefined }
        : { symbol, trigger: 'drawdown', drawdownPct: bps / 100, note: note.trim() || undefined }
    try {
      await api.createAlert(req)
      close()
      toast(kind === 'price' ? `Alert set · ${symbol} ${op} ${fmtIn(level, currency)}` : `Alert set · ${symbol} ${bps / 100}% below its all-time high`)
    } catch (err) {
      setError(errorMessage(err, 'Could not set the alert. Please try again.'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <form onSubmit={submit} className="space-y-4">
      <div>
        <div className="label mb-2">Alert · paper trading</div>
        <h2 id={`${id}-title`} className="font-display text-2xl leading-tight font-medium">
          Alert me on {symbol}
        </h2>
        <p className="mt-1 text-[13px] text-muted">It shows in the bell, and by email or Lark if you turned them on in Account.</p>
      </div>

      <div role="radiogroup" aria-label="Alert type" className="flex gap-2">
        {(['price', 'drawdown'] as const).map((k) => (
          <button
            key={k}
            type="button"
            role="radio"
            aria-checked={kind === k}
            onClick={() => {
              setKind(k)
              setError('')
            }}
            className={cx(
              'h-8 rounded-lg border px-3 text-[12.5px] font-medium transition-colors',
              kind === k ? 'border-accent bg-accent/10 text-accent-ink' : 'border-border text-text hover:bg-surface-2',
            )}
          >
            {k === 'price' ? 'Price level' : 'Drop from high'}
          </button>
        ))}
      </div>

      {kind === 'price' ? (
        <div className="space-y-3">
          <div>
            <Label htmlFor={`${id}-op`}>When {symbol}</Label>
            <Select id={`${id}-op`} value={op} onChange={(e) => setOp(e.target.value as AlertOp)}>
              <option value="below">falls to or below</option>
              <option value="above">rises to or above</option>
            </Select>
          </div>
          <div>
            <Label htmlFor={`${id}-price`}>{currency ? `Price (${currency})` : 'Price'}</Label>
            <Input
              id={`${id}-price`}
              type="number"
              inputMode="decimal"
              min={0}
              step="any"
              value={price}
              disabled={now == null || !currency}
              onChange={(e) => setPrice(e.target.value)}
              aria-invalid={crossed || undefined}
              aria-describedby={`${id}-now`}
              className="font-mono tabular-nums"
            />
            <p id={`${id}-now`} aria-live="polite" className={cx('mt-1.5 text-[12px]', crossed ? 'text-down' : 'text-muted')}>
              {now == null
                ? asset ? 'Listing price unavailable. Try again shortly.' : 'Price loading…'
                : crossed
                  ? `${symbol} is already ${op} that (now ${fmtIn(now, currency)}).`
                  : `Now ${fmtIn(now, currency)}${local && currency !== 'USD' && asset ? ` · ${fmtIn(asset.price)}` : ''}${validPrice ? ` · ${fmtPct(((level - now) / now) * 100, { sign: true, decimals: 1 })} away` : ''}`}
            </p>
            {currency && currency !== 'USD' && <p className="mt-0.5 text-[12px] text-muted">Checked on the {currency} listing price, not the USD paper mark.</p>}
          </div>
        </div>
      ) : (
        <div>
          <Label htmlFor={`${id}-pct`}>Drop from the all-time high (%)</Label>
          <Input
            id={`${id}-pct`}
            type="number"
            inputMode="decimal"
            min={0.01}
            max={99.99}
            step="any"
            placeholder="20"
            value={pct}
            onChange={(e) => setPct(e.target.value)}
            aria-describedby={`${id}-ath`}
            className="font-mono tabular-nums"
          />
          <div id={`${id}-ath`} aria-live="polite" className="mt-1.5 space-y-0.5 text-[12px] text-muted">
            {!ath ? (
              <p>Reading {symbol}'s all-time high…</p>
            ) : !q ? (
              <p className="text-down">{ath.error}</p>
            ) : withheld ? (
              <p className="text-down">{ATH_WITHHELD}</p>
            ) : (
              <>
                <p className="text-text">
                  All-time high <b className="font-mono font-semibold tabular-nums">{fmtIn(q.ath, q.currency)}</b> · {athDate(q.athAt)}
                </p>
                <p className="font-mono tabular-nums">
                  Now {fmtIn(q.price, q.currency)} · {fmtPct(Math.abs(q.fromAthPct), { decimals: 1 })} below the high
                </p>
                {threshold != null && (
                  <p className={q.price <= threshold ? 'text-down' : 'text-text'}>
                    Fires at or below <b className="font-mono font-semibold tabular-nums">{fmtIn(threshold, q.currency)}</b>
                    {q.price <= threshold && ': already past it, so it fires on the next check.'}
                  </p>
                )}
              </>
            )}
          </div>
        </div>
      )}

      <div>
        <Label htmlFor={`${id}-note`}>Note (optional)</Label>
        <Input id={`${id}-note`} maxLength={200} value={note} onChange={(e) => setNote(e.target.value)} placeholder="Why this level" />
      </div>

      {error && (
        <p role="alert" className="text-[13px] text-down">
          {error}
        </p>
      )}
      <div className="flex flex-wrap items-center justify-between gap-2 pt-1">
        <Link to={`/automations?symbol=${encodeURIComponent(symbol)}`} onClick={close} className="text-[12.5px] text-accent-ink hover:underline">
          Orders and schedules in Automations
        </Link>
        <div className="flex gap-2">
          <Button type="button" variant="secondary" disabled={busy} onClick={close}>
            Cancel
          </Button>
          <Button type="submit" loading={busy} disabled={!ready}>
            Set alert
          </Button>
        </div>
      </div>
    </form>
  )
}

/** A suggested level to 3 significant figures, so "5% away" reads as a round number in any currency. */
function roundLevel(x: number) {
  if (!(x > 0)) return 0
  const step = 10 ** (Math.floor(Math.log10(x)) - 2)
  return Number((Math.round(x / step) * step).toPrecision(3))
}

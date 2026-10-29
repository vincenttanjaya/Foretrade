// "/automations": workflows on paper (contract "Automations"). Price triggers that notify or place a paper
// market order, daily, weekday or weekly schedules that post a brief or place a paper order (DCA),
// prediction-market odds triggers on a forecast's matched Polymarket market and all-time-high drawdown
// triggers that notify or sell part of the paper long, each drawn as Trigger → Action → Deliver; the server checks them
// every 15 s. A sentence goes to the agent, which answers with a card. ?create=drawdown&symbol=X opens the drawdown form.
import { useEffect, useRef, useState, type FormEvent } from 'react'
import { Link, useLocation, useNavigate, useSearchParams } from 'react-router'
import { PageHeader } from '../../components/layout/AppShell'
import { useToast } from '../../components/toast'
import { AssetIcon } from '../../components/ui/AssetIcon'
import { Badge } from '../../components/ui/Badge'
import { Button } from '../../components/ui/Button'
import { Card } from '../../components/ui/Card'
import { EmptyState } from '../../components/ui/EmptyState'
import { IconAlert, IconArrowRight, IconBolt, IconSignal } from '../../components/ui/icons'
import { Input, Label, Select, Textarea } from '../../components/ui/Input'
import { Skeleton } from '../../components/ui/Spinner'
import { Tabs } from '../../components/ui/Tabs'
import { api } from '../../lib/api'
import { useAssets } from '../../lib/assets'
import { useInstruments } from '../../lib/instruments'
import { cx } from '../../lib/cx'
import { errorMessage } from '../../lib/errors'
import { fmtPct } from '../../lib/format'
import type { Alert, AlertAction, AlertEvery, AlertOp, AlertTrigger, AthQuote, MarketOdds, NotifyWhen } from '../../lib/types'
import { usePoll } from '../../lib/usePoll'
import { ExternalLink } from '../forecasts/DeskVsMarket'
import { fmtPts, httpUrl } from '../forecasts/forecast'
import { deskOf, pctLabel } from '../forecasts/OddsAlertButton'
import { roundPrice } from '../trade/util'
import { SYMBOLS, symbolVenueGroup, VENUE_GROUPS, venueGroup } from '../strategies/strategy'
import { actionLines, ATH_WITHHELD, athDate, athWithheld, deliverText, drawdownText, fmtIn, notifyWhenText, Pipeline, scheduleText, WEEKDAYS } from './pipeline'
import { ResearchHistory, RunSummary } from './ResearchHistory'

/**
 * A symbol picker's options, grouped like the trade page's market list (<optgroup> per VENUE_GROUPS group); a listed
 * Asian stock reads "6501.T · Hitachi, Ltd.", since its ticker alone names nothing.
 */
function SymbolOptions({ symbols }: { symbols: string[] }) {
  const { bySymbol } = useAssets()
  const catalogue = useInstruments()
  const group = (s: string) => {
    const known = bySymbol[s] ?? catalogue.bySymbol[s]
    return known ? venueGroup(known) : symbolVenueGroup(s)
  }
  const keyed = symbols.map((s) => ({ s, group: group(s) }))
  return VENUE_GROUPS.map(({ key, label }) => {
    const items = keyed.filter((k) => k.group === key)
    return items.length > 0 && (
      <optgroup key={key} label={label}>
        {items.map(({ s }) => {
          const row = catalogue.bySymbol[s]
          return (
            <option key={s} value={s}>
              {row?.listed ? `${s} · ${row.name}` : s}
            </option>
          )
        })}
      </optgroup>
    )
  })
}

/** "Oct 3, 14:05" in the viewer's time zone. */
export const shortTime = (iso: string) =>
  new Date(iso).toLocaleString('en-US', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', hourCycle: 'h23' })

/** "Oct 4, 08:00" in UTC, the clock schedules run on. */
const utcTime = (iso: string) =>
  new Date(iso).toLocaleString('en-US', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', hourCycle: 'h23', timeZone: 'UTC' })

/** "6.2%", or "0.02%" under 1% so a level next to the price doesn't read as 0. */
const pctAway = (price: number, mark: number) => {
  const pct = Math.abs(price / mark - 1) * 100
  return fmtPct(pct, { decimals: pct < 1 ? 2 : 1 })
}

type Form = {
  trigger: AlertTrigger
  symbol: string
  op: AlertOp
  price: string
  every: AlertEvery
  at: string // 'HH:MM' UTC
  weekday: string // '0'..'6'
  action: AlertAction
  amount: string
  stop: string
  take: string
  note: string
  forecastId: string // odds
  pct: string // odds: the Yes level in %, 1..99
  drawdownPct: string // drawdown: % below the all-time high, 0.01..99.99
  sellPct: string // drawdown sell: % of the paper long, 1..100
  researchPrompt: string
  trackThesis: boolean
  thesis: string
  invalidation: string
  notifyWhen: NotifyWhen
}
const blank = (symbol: string, trigger: AlertTrigger = 'price'): Form => ({
  trigger,
  symbol,
  op: 'below',
  price: '',
  every: 'day',
  at: '08:00',
  weekday: '1',
  action: 'notify',
  amount: '',
  stop: '',
  take: '',
  note: '',
  forecastId: '',
  pct: '',
  drawdownPct: '',
  sellPct: '100',
  researchPrompt: '',
  trackThesis: false,
  thesis: '',
  invalidation: '',
  notifyWhen: 'always',
})
const num = (s: string) => (s.trim() ? Number(s) : undefined)

/** A report the Prediction market tab can watch: its matched market (null: none) and the desk's P of that market's option. */
type OddsChoice = { id: string; question: string; m: MarketOdds | null; desk: number | null }

/**
 * Router state the Desk vs market card sends for a custom level: { trigger: 'odds', forecastId, op, pricePct }; or the
 * trade page's ?create=drawdown deep link.
 */
function entryForm(symbol: string, state: unknown, create: string | null): Form {
  if (create === 'drawdown') return blank(symbol, 'drawdown')
  const e = state as { trigger?: unknown; forecastId?: unknown; op?: unknown; pricePct?: unknown } | null
  if (e?.trigger !== 'odds') return blank(symbol)
  return {
    ...blank(symbol, 'odds'),
    forecastId: typeof e.forecastId === 'string' ? e.forecastId : '',
    op: e.op === 'below' ? 'below' : 'above',
    pct: typeof e.pricePct === 'number' ? String(e.pricePct) : '',
  }
}

/** Sentences the agent turns into an alert or schedule card. */
const EXAMPLES = ['Every morning at 08:00 UTC, brief me on BTC', 'Buy $100 of ETH every Monday at 09:00 UTC', 'Tell me if SOL falls below $110']

export function AutomationsPage() {
  const [params, setParams] = useSearchParams()
  const { state: entry } = useLocation()
  const navigate = useNavigate()
  const [prompt, setPrompt] = useState('')
  const { assets, bySymbol } = useAssets()
  const catalogue = useInstruments()
  const { toast } = useToast()
  const [alerts, setAlerts] = useState<Alert[] | null>(null)
  const [loadError, setLoadError] = useState<string | null>(null)
  const tab = params.get('tab') === 'history' ? 'history' : 'active'
  const selectedId = params.get('alert')
  const selectedRow = useRef<HTMLDivElement>(null)
  const selected = alerts?.find((a) => a.id === selectedId)
  const setTab = (next: 'active' | 'history') => {
    const query = new URLSearchParams(params)
    if (next === 'history') query.set('tab', next)
    else query.delete('tab')
    query.delete('alert')
    setParams(query)
  }
  useEffect(() => {
    if (selected?.id) selectedRow.current?.focus()
  }, [selected?.id, tab])
  const [form, setForm] = useState<Form>(() => entryForm(params.get('symbol')?.toUpperCase() || 'BTC', entry, params.get('create')))
  const [busy, setBusy] = useState(false)
  const busyRef = useRef(false)
  const listGeneration = useRef(0)
  const mutationVersion = useRef(0)
  const pendingMutations = useRef(0)
  const mounted = useRef(true)
  useEffect(() => {
    mounted.current = true
    return () => { mounted.current = false; listGeneration.current++ }
  }, [])
  const [formError, setFormError] = useState<string | null>(null)
  const [confirming, setConfirming] = useState(false) // a drawdown trigger is confirmed once, after a review
  // The trade page's and the screener's "Set drawdown alert" land on this form, which sits below the fold on a phone.
  // Deferred: the shell scrolls to the top on a new path in the same commit, after this page's effects.
  useEffect(() => {
    if (params.get('create') !== 'drawdown') return
    const t = setTimeout(() => document.getElementById('new-automation')?.scrollIntoView?.({ block: 'start' }))
    return () => clearTimeout(t)
  }, []) // once, on arrival
  function priceCurrency(f: Form) {
    if (f.trigger !== 'price' || f.action !== 'notify') return 'USD'
    const asset = bySymbol[f.symbol]
    const instrument = asset ?? catalogue.bySymbol[f.symbol]
    if (instrument && instrument.venue !== 'yahoo') return 'USD'
    return asset?.quoteFeed?.currency || instrument?.localCurrency || undefined
  }
  const set = (patch: Partial<Form>) => {
    setConfirming(false)
    setForm((f) => {
      const next = { ...f, ...patch }
      return priceCurrency(f) !== priceCurrency(next) && patch.price == null ? { ...next, price: '' } : next
    })
  }

  async function load() {
    const generation = ++listGeneration.current
    const version = mutationVersion.current
    try {
      const response = await api.alerts('all')
      if (!mounted.current || generation !== listGeneration.current || version !== mutationVersion.current || pendingMutations.current) return
      setAlerts(response.alerts)
      setLoadError(null)
    } catch (err) {
      if (!mounted.current || generation !== listGeneration.current || version !== mutationVersion.current || pendingMutations.current) return
      setLoadError(errorMessage(err, "Couldn't load your automations."))
    }
  }
  usePoll(load, 15_000) // the checker's cadence: a fired alert shows within a tick

  const odds = form.trigger === 'odds'
  const [choices, setChoices] = useState<OddsChoice[] | null>(null)
  useEffect(() => {
    if (!odds || choices) return
    const ctrl = new AbortController()
    ;(async () => {
      // ponytail: reads the 10 newest reports one by one for their market; a hasMarket flag on the list would save the requests.
      const { forecasts } = await api.forecasts({ status: 'completed' }, ctrl.signal)
      const ids = forecasts.filter((f) => !f.replay && (f.shape === 'binary' || f.shape === 'categorical')).slice(0, 10).map((f) => f.id)
      const wanted = form.forecastId
      if (wanted && !ids.includes(wanted)) ids.unshift(wanted) // a custom level from an older report
      const rows = await Promise.all(
        ids.map((id) =>
          api.forecast(id, ctrl.signal).then(
            ({ forecast: f }): OddsChoice => {
              const m = f.status === 'completed' && !f.replay && f.result?.market ? f.result.market : null
              return { id, question: f.question, m, desk: m && f.result ? deskOf(f.result, m) : null }
            },
            () => null,
          ),
        ),
      )
      const list = rows.filter((r): r is OddsChoice => r != null)
      setChoices(list)
      setForm((f) => ({ ...f, forecastId: list.find((o) => o.id === f.forecastId && o.m)?.id ?? list.find((o) => o.m)?.id ?? '' }))
    })().catch(() => {
      if (!ctrl.signal.aborted) setChoices([])
    })
    return () => ctrl.abort()
  }, [odds]) // once, when the tab first opens

  // A drawdown trigger's high (GET /api/alerts/ath), read for the chosen asset.
  const drawdown = form.trigger === 'drawdown'
  const [high, setHigh] = useState<{ symbol: string; quote: AthQuote | null; error: string | null } | null>(null)
  useEffect(() => {
    if (!drawdown || !form.symbol) return
    const ctrl = new AbortController()
    api.alertATH(form.symbol, ctrl.signal).then(
      ({ ath }) => setHigh({ symbol: form.symbol, quote: ath, error: null }),
      (err) => {
        if (!ctrl.signal.aborted) setHigh({ symbol: form.symbol, quote: null, error: errorMessage(err, "Couldn't read the all-time high. Try again shortly.") })
      },
    )
    return () => ctrl.abort()
  }, [drawdown, form.symbol])
  const athRead = high?.symbol === form.symbol ? high : null
  const bps = Math.round(Number(form.drawdownPct) * 100)
  const validDrawdown = form.drawdownPct.trim() !== '' && bps >= 1 && bps <= 9999
  const sellPct = Number(form.sellPct)
  const validSell = form.action !== 'sell' || (Number.isInteger(sellPct) && sellPct >= 1 && sellPct <= 100)
  const withheld = athRead?.quote?.source === 'yahoo' && athWithheld(athRead.quote.ath, athRead.quote.price) // no level from an unusable high
  const level = athRead?.quote && validDrawdown && !withheld ? (athRead.quote.ath * (10000 - bps)) / 10000 : null // as the API rounds it
  const past = level != null && athRead!.quote!.price <= level

  const nativePrice = form.trigger === 'price' && form.action === 'notify'
  const currency = priceCurrency(form)
  const asset = bySymbol[form.symbol]
  const quotePrice = currency === 'USD' ? asset?.price : asset?.quoteFeed?.currency === currency ? asset?.quoteFeed?.localPrice : undefined
  const mark = quotePrice != null && Number.isFinite(quotePrice) && quotePrice > 0 ? quotePrice : undefined
  const price = Number(form.price)
  const scheduled = form.trigger === 'schedule'
  const research = scheduled && form.action === 'research'
  const order = !odds && !drawdown && (form.action === 'buy' || form.action === 'sell')
  const researchLength = Array.from(form.researchPrompt.trim()).length
  const thesisLength = Array.from(form.thesis.trim()).length
  const invalidationLength = Array.from(form.invalidation.trim()).length
  // A configured instrument: a named one (SYMBOLS) or a listed Asian stock the catalogue knows.
  const known = (symbol: string) => SYMBOLS.some(([s]) => s === symbol) || !!catalogue.bySymbol[symbol]
  const configuredSymbol = known(form.symbol)
  // The priced instruments, plus a known preset (?symbol=, the screener's drawdown link) before its first price.
  const priced = assets.map((a) => a.symbol)
  const tradeSymbols = !assets.length ? [form.symbol] : configuredSymbol && !priced.includes(form.symbol) ? [...priced, form.symbol] : priced
  const researchSymbols = [...SYMBOLS.map(([s]) => s), ...catalogue.listed.map((r) => r.symbol)]
  const validResearch = (!form.symbol || configuredSymbol) && researchLength >= 10 && researchLength <= 500 && (!form.trackThesis || (
    configuredSymbol && thesisLength > 0 && thesisLength <= 300 && invalidationLength > 0 && invalidationLength <= 300
  ))
  const chosen = choices?.find((o) => o.id === form.forecastId) ?? null
  const yes = chosen?.m?.yesPrice
  const pct = Number(form.pct)
  // A level the live price (or the market's Yes price) is already past would be refused (the API checks the same): say so before Create.
  const crossed = odds
    ? yes != null && pct > 0 && (form.op === 'above' ? pct / 100 <= yes : pct / 100 >= yes)
    : !scheduled && !drawdown && mark != null && price > 0 && (form.op === 'above' ? price <= mark : price >= mark)
  const ready = odds
    ? !!chosen?.m && pct >= 1 && pct <= 99 && !crossed
    : drawdown
      ? level != null && validSell && (form.action === 'notify' || form.action === 'sell')
      : (scheduled ? /^([01]\d|2[0-3]):[0-5]\d$/.test(form.at) : Number.isFinite(price) && price > 0 && !crossed && (!nativePrice || (!!currency && mark != null))) && (!order || Number(form.amount) >= 10) && (!research || validResearch)

  function beginMutation() {
    pendingMutations.current++
    mutationVersion.current++
    listGeneration.current++
  }
  function endMutation() {
    pendingMutations.current--
    mutationVersion.current++
    listGeneration.current++
  }

  async function submit(e: FormEvent) {
    e.preventDefault()
    if (!ready || busyRef.current) return
    if (drawdown && !confirming) return setConfirming(true) // review first: the action is confirmed once, here
    busyRef.current = true
    beginMutation()
    setBusy(true)
    setFormError(null)
    try {
      const level = Math.round(pct * 100) / 10000
      const { alert } = await api.createAlert(
        odds
          ? { symbol: '', trigger: 'odds', forecastId: form.forecastId, op: form.op, price: level, action: 'notify', note: form.note.trim() || undefined, source: 'manual' }
          : drawdown
            ? {
                symbol: form.symbol, trigger: 'drawdown', drawdownPct: bps / 100, action: form.action,
                ...(form.action === 'sell' ? { sellPct } : {}), note: form.note.trim() || undefined, source: 'manual',
              }
          : {
              ...(form.symbol ? { symbol: form.symbol } : {}),
              ...(scheduled
                ? { trigger: 'schedule', every: form.every, at: form.at, ...(form.every === 'week' ? { weekday: Number(form.weekday) } : {}) }
                : { op: form.op, price, ...(nativePrice && currency !== 'USD' ? { currency } : {}) }),
              action: form.action,
              ...(research ? {
                prompt: form.researchPrompt.trim(), notifyWhen: form.notifyWhen,
                ...(form.trackThesis ? { thesis: { statement: form.thesis.trim(), invalidation: form.invalidation.trim() } } : {}),
              } : {}),
              ...(order ? { notional: Number(form.amount) } : {}),
              ...(order && !scheduled ? { stopLoss: num(form.stop), takeProfit: num(form.take) } : {}),
              note: form.note.trim() || undefined,
              source: 'manual',
            },
      )
      setAlerts((list) => [alert, ...(list ?? [])])
      setForm({ ...blank(form.symbol || 'BTC', form.trigger), forecastId: form.forecastId })
      setConfirming(false)
      setTab('active')
      toast(
        odds
          ? `Alert set. You'll get a bell when Polymarket crosses ${pctLabel(level)}.`
          : drawdown
            ? past
              ? 'Drawdown alert set. The price is already past it, so it fires on the next check.'
              : 'Drawdown alert set. Checked every 15 seconds.'
          : alert.nextRunAt
            ? `Automation created. Next run ${utcTime(alert.nextRunAt)} UTC.`
            : 'Automation created. Checked every 15 seconds.',
      )
    } catch (err) {
      setFormError(errorMessage(err, "The automation wasn't created. Please try again."))
    } finally {
      busyRef.current = false
      endMutation()
      setBusy(false)
    }
  }

  async function cancel(a: Alert) {
    beginMutation()
    let failed = false
    try {
      await api.cancelAlert(a.id)
      setAlerts((list) => list?.map((x) => (x.id === a.id ? { ...x, status: 'cancelled' } : x)) ?? null)
      toast('Automation cancelled.')
    } catch (err) {
      failed = true
      toast(errorMessage(err, "Couldn't cancel it. Please try again."), 'error')
    } finally {
      endMutation()
    }
    if (failed) void load()
  }

  async function resume(a: Alert) {
    beginMutation()
    try {
      const { alert } = await api.resumeAlert(a.id)
      setAlerts((list) => list?.map((x) => x.id === a.id ? { ...alert, lastRun: x.lastRun } : x) ?? null)
      toast('Research resumed. Each run uses 2 AI credits.')
    } finally {
      endMutation()
    }
  }

  // Empty-state starters, priced off the live market.
  const starters: { label: string; form: () => Partial<Form> | null }[] = [
    { label: 'Alert me if BTC falls 5%', form: () => preset('BTC', 'below', 0.95) },
    { label: 'Buy $500 of SOL if it dips 5%', form: () => ({ ...preset('SOL', 'below', 0.95), action: 'buy', amount: '500' }) },
    {
      label: 'Tell me when ETH breaks its 24h high',
      form: () => (bySymbol.ETH ? { symbol: 'ETH', op: 'above', price: String(roundPrice(bySymbol.ETH.high24h)) } : null),
    },
  ]
  function preset(symbol: string, op: AlertOp, factor: number): Partial<Form> {
    const p = bySymbol[symbol]?.price
    return { symbol, op, price: p ? String(roundPrice(p * factor)) : '' }
  }

  const shown = (alerts ?? []).filter((a) => (tab === 'active') === (a.status === 'active'))
  const active = (alerts ?? []).filter((a) => a.status === 'active').length

  return (
    <div className="mx-auto max-w-6xl px-4 py-8 md:px-8 md:py-10">
      <PageHeader
        label="Automations · paper"
        title="Automations"
        subtitle="Price triggers, schedules, prediction-market moves and drawdowns from the all-time high that notify you, brief you or place a paper order. Checked every 15 seconds."
      />
      <Card className="mb-6 p-5">
        <form
          onSubmit={(e) => {
            e.preventDefault()
            if (prompt.trim()) navigate('/', { state: { prompt: prompt.trim() } })
          }}
        >
          <h2 id="nl-title" className="font-display text-[19px] font-medium text-text">
            Describe an automation
          </h2>
          <p className="mt-1 text-[13px] text-muted">
            The agent answers with an alert or schedule card (one chat message). Nothing is set until you tap Set.
          </p>
          <Textarea
            aria-labelledby="nl-title"
            maxLength={400}
            rows={2}
            placeholder={EXAMPLES[0]}
            value={prompt}
            onChange={(e) => setPrompt(e.target.value)}
            className="mt-3 max-h-40"
          />
          <div className="mt-3 flex flex-wrap items-center gap-2">
            {EXAMPLES.map((x) => (
              <button
                key={x}
                type="button"
                onClick={() => setPrompt(x)}
                className="rounded-full border border-border bg-surface px-3 py-1.5 text-left text-[13px] text-text transition-colors hover:border-muted/50 hover:bg-surface-2"
              >
                {x}
              </button>
            ))}
            <Button type="submit" disabled={!prompt.trim()} className="w-full sm:ml-auto sm:w-auto">
              Build with the agent
            </Button>
          </div>
        </form>
      </Card>
      <div className="grid items-start gap-6 lg:grid-cols-[360px_minmax(0,1fr)]">
        <Card id="new-automation" className="scroll-mt-4 p-5">
          <h2 className="font-display text-[19px] font-medium text-text">New automation</h2>
          <form onSubmit={submit} noValidate className="mt-4 space-y-4">
            <div>
              <span className="label mb-1.5 block">Trigger</span>
              <Tabs<AlertTrigger>
                aria-label="Trigger"
                value={form.trigger}
                onChange={(trigger) => set({
                  trigger,
                  ...(trigger !== 'schedule' && form.action === 'research' ? { action: 'notify', symbol: form.symbol || 'BTC' } : {}),
                  ...(trigger === 'drawdown' && form.action === 'buy' ? { action: 'notify' } : {}),
                })}
                className="flex-wrap"
                items={[
                  { value: 'price', label: 'Price' },
                  { value: 'schedule', label: 'Schedule' },
                  { value: 'odds', label: 'Prediction market' },
                  { value: 'drawdown', label: 'Drawdown' },
                ]}
              />
            </div>
            {odds ? (
              <OddsFields choices={choices} chosen={chosen} form={form} set={set} crossed={crossed} />
            ) : drawdown ? (
              <DrawdownFields form={form} set={set} symbols={tradeSymbols} read={athRead} level={level} past={past} withheld={withheld} />
            ) : (
              <>
            <div className="grid grid-cols-[minmax(0,1fr)_auto] items-end gap-3">
              <div>
                <Label htmlFor="auto-symbol">Asset</Label>
                <Select id="auto-symbol" value={form.symbol} onChange={(e) => set({ symbol: e.target.value })}>
                  {research && <option value="">General research</option>}
                  <SymbolOptions symbols={research ? researchSymbols : tradeSymbols} />
                </Select>
                {research && form.symbol && !configuredSymbol && <p className="mt-1 text-[12px] text-down">Choose a configured asset or General research.</p>}
              </div>
              {scheduled ? (
                <Tabs<AlertEvery>
                  aria-label="Every"
                  value={form.every}
                  onChange={(every) => set({ every })}
                  items={[
                    { value: 'day', label: 'Day' },
                    { value: 'weekdays', label: 'Weekdays' },
                    { value: 'week', label: 'Week' },
                  ]}
                />
              ) : (
                <Tabs<AlertOp>
                  aria-label="Condition"
                  value={form.op}
                  onChange={(op) => set({ op })}
                  items={[
                    { value: 'above', label: 'rises above' },
                    { value: 'below', label: 'falls below' },
                  ]}
                />
              )}
            </div>
            {scheduled ? (
              <div className="grid grid-cols-2 gap-3">
                <div className={form.every === 'week' ? '' : 'col-span-2'}>
                  <Label htmlFor="auto-at">Time (UTC)</Label>
                  <Input id="auto-at" type="time" required value={form.at} onChange={(e) => set({ at: e.target.value })} className="font-mono" />
                </div>
                {form.every === 'week' && (
                  <div>
                    <Label htmlFor="auto-weekday">Weekday</Label>
                    <Select id="auto-weekday" value={form.weekday} onChange={(e) => set({ weekday: e.target.value })}>
                      {WEEKDAYS.map((d, i) => (
                        <option key={d} value={i}>
                          {d.slice(0, -1)}
                        </option>
                      ))}
                    </Select>
                  </div>
                )}
              </div>
            ) : (
              <div>
                <Label htmlFor="auto-price">{currency ? `Price (${currency})` : 'Price'}</Label>
                <Input
                  id="auto-price"
                  type="number"
                  inputMode="decimal"
                  min={0}
                  step="any"
                  placeholder={mark ? String(roundPrice(mark)) : '0.00'}
                  value={form.price}
                  disabled={nativePrice && (!currency || mark == null)}
                  onChange={(e) => set({ price: e.target.value })}
                  aria-describedby="auto-price-hint"
                  className="font-mono tabular-nums"
                />
                <p id="auto-price-hint" className={cx('mt-1.5 text-[12px]', crossed ? 'text-down' : 'text-muted')}>
                  {mark == null ? (
                    asset && nativePrice ? 'Listing price unavailable. Try again shortly.' : 'Waiting for a price…'
                  ) : crossed ? (
                    <>
                      {form.symbol} is already {form.op} {fmtIn(price, currency)} (now {fmtIn(mark, currency)}). Pick a level {form.op} the price, or{' '}
                      <Link to={`/trade/${form.symbol}`} className="font-medium text-accent-ink underline-offset-2 hover:underline">
                        trade now on the Trade page
                      </Link>
                      .
                    </>
                  ) : price > 0 ? (
                    `${price > mark ? '+' : '−'}${pctAway(price, mark)} from the live ${fmtIn(mark, currency)}`
                  ) : (
                    `${form.symbol} is at ${fmtIn(mark, currency)}`
                  )}
                </p>
              </div>
            )}
              </>
            )}
            {odds ? (
              <p className="text-[13px] text-text">
                <span className="label mr-2">Then</span>Notify me <span className="text-muted">· Bell · in-app</span>
              </p>
            ) : (
            <div>
              <span className="label mb-1.5 block">Then</span>
              <Tabs<AlertAction>
                aria-label="Action"
                value={form.action}
                onChange={(action) => set({ action, ...(action !== 'research' && !form.symbol ? { symbol: 'BTC' } : {}) })}
                items={drawdown ? [
                  { value: 'notify', label: 'Notify me' },
                  { value: 'sell', label: 'Notify + sell' },
                ] : [
                  { value: 'notify', label: scheduled ? 'Brief me' : 'Notify me' },
                  { value: 'buy', label: 'Buy' },
                  { value: 'sell', label: 'Sell' },
                  ...(scheduled ? [{ value: 'research' as const, label: 'AI research' }] : []),
                ]}
              />
            </div>
            )}
            {drawdown && form.action === 'sell' && (
              <div>
                <Label htmlFor="auto-sell-pct">Sell (% of your paper long)</Label>
                <Input
                  id="auto-sell-pct"
                  type="number"
                  inputMode="numeric"
                  min={1}
                  max={100}
                  step={1}
                  value={form.sellPct}
                  onChange={(e) => set({ sellPct: e.target.value })}
                  aria-describedby="auto-sell-hint"
                  className="font-mono tabular-nums"
                />
                <p id="auto-sell-hint" className={cx('mt-1.5 text-[12px]', validSell ? 'text-muted' : 'text-down')}>
                  {validSell
                    ? `Sells ${sellPct}% of your paper ${form.symbol} long at market when it fires (± 2 bps, 0.1% fee). It never opens or adds to a short: with no long, you're only notified.`
                    : 'A whole number from 1 to 100.'}
                </p>
              </div>
            )}
            {research && (
              <div className="space-y-3 rounded-xl border border-border bg-surface-2 p-3">
                <div>
                  <Label htmlFor="auto-research-prompt">Research prompt</Label>
                  <Textarea id="auto-research-prompt" rows={3} value={form.researchPrompt} onChange={(e) => set({ researchPrompt: e.target.value })} aria-describedby="auto-research-bounds" />
                  <p id="auto-research-bounds" className={cx('mt-1 text-[12px]', researchLength > 500 ? 'text-down' : 'text-muted')}>10–500 characters · {researchLength}/500</p>
                </div>
                <label className="flex items-center gap-2 text-[13px] text-text">
                  <input type="checkbox" checked={form.trackThesis} onChange={(e) => set({ trackThesis: e.target.checked })} /> Track a thesis
                </label>
                {form.trackThesis && <>
                  <p className="text-[12px] text-muted">Choose an asset to track its thesis against the evidence.</p>
                  <div><Label htmlFor="auto-thesis">Thesis</Label><Textarea id="auto-thesis" rows={2} value={form.thesis} onChange={(e) => set({ thesis: e.target.value })} /><p className={cx('mt-1 text-[12px]', thesisLength > 300 ? 'text-down' : 'text-muted')}>{thesisLength}/300 characters</p></div>
                  <div><Label htmlFor="auto-invalidation">Invalidation</Label><Textarea id="auto-invalidation" rows={2} value={form.invalidation} onChange={(e) => set({ invalidation: e.target.value })} /><p className={cx('mt-1 text-[12px]', invalidationLength > 300 ? 'text-down' : 'text-muted')}>{invalidationLength}/300 characters</p></div>
                </>}
                <div><Label htmlFor="auto-notify-when">Notify when</Label><Select id="auto-notify-when" value={form.notifyWhen} onChange={(e) => set({ notifyWhen: e.target.value as NotifyWhen })}><option value="always">Always</option><option value="changed">Changed</option><option value="invalidated">Invalidated</option></Select><p className="mt-1 text-[12px] text-muted">{notifyWhenText(form.notifyWhen)}</p></div>
                <p className="text-[13px] font-medium text-text">2 AI credits per run from your daily allowance. Creating the automation is free.</p>
                <p className="text-[12px] text-muted">Up to 3 active research schedules. Three consecutive runs without enough credits pause the automation until you Resume it.</p>
              </div>
            )}
            {order &&
              (scheduled ? (
                <Field id="auto-amount" label="Amount ($)" value={form.amount} onChange={(amount) => set({ amount })} placeholder="100" />
              ) : (
                <div className="grid grid-cols-3 gap-2">
                  <Field id="auto-amount" label="Amount ($)" value={form.amount} onChange={(amount) => set({ amount })} placeholder="1000" />
                  <Field id="auto-stop" label="Stop" value={form.stop} onChange={(stop) => set({ stop })} placeholder="Optional" />
                  <Field id="auto-take" label="Target" value={form.take} onChange={(take) => set({ take })} placeholder="Optional" />
                </div>
              ))}
            <div>
              <Label htmlFor="auto-note">Note</Label>
              <Input
                id="auto-note"
                maxLength={200}
                placeholder="Why this level?"
                value={form.note}
                onChange={(e) => set({ note: e.target.value })}
              />
            </div>
            {formError && (
              <p role="alert" className="flex items-start gap-2 rounded-lg bg-down/8 px-3 py-2 text-[13px] text-down">
                <IconAlert size={15} className="mt-px shrink-0" />
                {formError}
              </p>
            )}
            {drawdown && confirming && level != null && athRead?.quote ? (
              <div role="group" aria-label="Confirm drawdown alert" className="space-y-3 rounded-xl border border-border bg-surface-2 p-3 text-[13px]">
                <p className="text-text">
                  When {form.symbol} falls {bps / 100}% below its all-time high ({fmtIn(athRead.quote.ath, athRead.quote.currency)} on{' '}
                  {athDate(athRead.quote.athAt)}), at or below <b className="font-mono font-semibold tabular-nums">{fmtIn(level, athRead.quote.currency)}</b>:{' '}
                  {form.action === 'sell' ? `notify me and sell ${sellPct}% of my paper ${form.symbol} long.` : 'notify me.'} One-shot, paper only.
                </p>
                {past && (
                  <p className="flex items-start gap-2 text-down">
                    <IconAlert size={15} className="mt-px shrink-0" />
                    {form.symbol} is already {fmtPct(Math.abs(athRead.quote.fromAthPct), { decimals: 1 })} below its high, past this level: once
                    confirmed, it fires on the next check (within 15 seconds).
                  </p>
                )}
                <div className="flex flex-wrap gap-2">
                  <Button type="submit" loading={busy} className="flex-1">
                    Confirm and set
                  </Button>
                  <Button type="button" variant="secondary" onClick={() => setConfirming(false)}>
                    Back
                  </Button>
                </div>
              </div>
            ) : (
              <Button type="submit" className="w-full" loading={busy} disabled={!ready}>
                {drawdown ? 'Review drawdown alert' : 'Create automation'}
              </Button>
            )}
            <p className="text-center text-[11.5px] leading-snug text-muted">
              {odds
                ? 'A bell when Polymarket crosses your level. The market is re-checked about once a minute, 0 credits.'
                : drawdown
                ? 'A bell when it fires, checked every 15 seconds, 0 credits. One-shot: set a new one to re-arm.'
                : scheduled
                ? research
                  ? 'Private research reports appear in a chat. Your notification condition controls the bell.'
                  : order
                  ? 'A paper market order at that time, every time. Buying power is checked then.'
                  : 'A brief on price, 24h move, sentiment and the top headline lands in the bell. No model call, 0 credits.'
                : order
                  ? 'Fills at market (± 2 bps) when it fires. Buying power is checked then.'
                  : 'You get a notification in the bell.'}
            </p>
          </form>
        </Card>

        <section aria-label="Your automations" className="min-w-0">
          <Tabs
            aria-label="Automations"
            value={tab}
            onChange={setTab}
            items={[
              { value: 'active', label: `Active · ${active}` },
              { value: 'history', label: `History · ${(alerts?.length ?? 0) - active}` },
            ]}
          />
          <div className="mt-4 space-y-3">
            {selectedId && alerts !== null && !selected && (
              <p role="status" className="rounded-lg border border-border px-4 py-3 text-sm text-muted">
                This alert is not available in your retained history. It may have been removed or belong to another account.
              </p>
            )}
            {alerts === null ? (
              loadError ? (
                <EmptyState
                  icon={<IconAlert size={20} />}
                  title="Couldn't load your automations."
                  description={loadError}
                  action={
                    <Button variant="secondary" onClick={load}>
                      Try again
                    </Button>
                  }
                />
              ) : (
                <Skeleton className="h-24 w-full rounded-[14px]" />
              )
            ) : alerts.length === 0 ? (
              <EmptyState
                icon={<IconBolt size={20} />}
                title="No automations yet"
                description="Set a level once; the desk watches it for you and notifies you or places the paper order."
                action={
                  <div className="flex flex-wrap justify-center gap-2">
                    {starters.map((s) => (
                      <button
                        key={s.label}
                        type="button"
                        onClick={() => {
                          const f = s.form()
                          if (f) setForm({ ...blank(form.symbol), ...f })
                        }}
                        className="rounded-full border border-border bg-surface px-3 py-1.5 text-[13px] text-text transition-colors hover:border-muted/50 hover:bg-surface-2"
                      >
                        {s.label}
                      </button>
                    ))}
                  </div>
                }
              />
            ) : shown.length === 0 ? (
              <p className="rounded-[14px] border border-dashed border-border px-4 py-8 text-center text-sm text-muted">
                {tab === 'active' ? 'No active automations.' : 'Nothing has fired yet.'}
              </p>
            ) : (
              shown.map((a) => (
                <div
                  key={a.id}
                  ref={a.id === selectedId ? selectedRow : undefined}
                  role={a.id === selectedId ? 'region' : undefined}
                  aria-label={a.id === selectedId ? 'Selected automation' : undefined}
                  tabIndex={a.id === selectedId ? -1 : undefined}
                  className={a.id === selectedId ? 'rounded-[14px] outline-2 outline-offset-2 outline-accent' : undefined}
                >
                  <AlertRow alert={a} mark={a.currency ? (bySymbol[a.symbol]?.quoteFeed?.currency === a.currency ? bySymbol[a.symbol]?.quoteFeed?.localPrice : undefined) : bySymbol[a.symbol]?.price} onCancel={() => cancel(a)} onResume={() => resume(a)} />
                </div>
              ))
            )}
          </div>
        </section>
      </div>
    </div>
  )
}

function Field({
  id,
  label,
  value,
  onChange,
  placeholder,
}: {
  id: string
  label: string
  value: string
  onChange: (v: string) => void
  placeholder: string
}) {
  return (
    <div className="min-w-0">
      <Label htmlFor={id}>{label}</Label>
      <Input
        id={id}
        type="number"
        inputMode="decimal"
        min={0}
        step="any"
        placeholder={placeholder}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className="px-2.5 font-mono tabular-nums"
      />
    </div>
  )
}

/** The Prediction market tab: which report's market, the market line, the condition and the level (a Yes probability). */
function OddsFields({
  choices,
  chosen,
  form,
  set,
  crossed,
}: {
  choices: OddsChoice[] | null
  chosen: OddsChoice | null
  form: Form
  set: (patch: Partial<Form>) => void
  crossed: boolean
}) {
  const m = chosen?.m
  const pct = Number(form.pct)
  const url = m && httpUrl(m.url)
  return (
    <>
      <div>
        <Label htmlFor="auto-forecast">Forecast</Label>
        <Select id="auto-forecast" value={form.forecastId} disabled={!choices?.length} onChange={(e) => set({ forecastId: e.target.value })}>
          {!choices?.length && <option value="">{choices ? 'No completed forecasts yet' : 'Loading your forecasts…'}</option>}
          {choices?.map((o) => (
            <option key={o.id} value={o.id} disabled={!o.m}>
              {o.question.length > 70 ? `${o.question.slice(0, 69)}…` : o.question}
              {o.m ? '' : ' (no market)'}
            </option>
          ))}
        </Select>
        <p className="mt-1.5 text-[12px] text-muted">
          {m ? (
            <>
              <span className="font-mono text-text tabular-nums">
                Polymarket {pctLabel(m.yesPrice)}
                {chosen.desk != null && ` · desk ${pctLabel(chosen.desk)}`}
              </span>
              {m.outcome && ` · ${m.outcome}`}
              {url && (
                <>
                  {' · '}
                  <ExternalLink href={url.href}>View on Polymarket</ExternalLink>
                </>
              )}
            </>
          ) : choices?.length === 0 ? (
            <>
              Run a forecast first; one matched to Polymarket can be watched here.{' '}
              <Link to="/forecasts/new" className="font-medium text-accent-ink hover:underline">
                New forecast
              </Link>
            </>
          ) : choices ? (
            'None of your recent forecasts has a matched Polymarket market.'
          ) : null}
        </p>
      </div>
      <div className="grid grid-cols-2 gap-3">
        <div className="min-w-0">
          <Label htmlFor="auto-odds-op">Condition</Label>
          <Select id="auto-odds-op" value={form.op} onChange={(e) => set({ op: e.target.value as AlertOp })}>
            <option value="above">Rises above</option>
            <option value="below">Falls below</option>
          </Select>
        </div>
        <div className="min-w-0">
          <Label htmlFor="auto-pct">Level (%)</Label>
          <Input
            id="auto-pct"
            type="number"
            inputMode="decimal"
            min={1}
            max={99}
            step={0.5}
            placeholder={m ? String(Math.round(m.yesPrice * 1000) / 10) : '50'}
            value={form.pct}
            onChange={(e) => set({ pct: e.target.value })}
            aria-describedby="auto-pct-hint"
            className="font-mono tabular-nums"
          />
        </div>
      </div>
      <p id="auto-pct-hint" className={cx('-mt-2 text-[12px]', crossed || (form.pct && !(pct >= 1 && pct <= 99)) ? 'text-down' : 'text-muted')}>
        {form.pct && !(pct >= 1 && pct <= 99)
          ? 'Pick a level between 1% and 99%.'
          : crossed && m
            ? `The market is already at ${pctLabel(m.yesPrice)}. Pick a level ${form.op} it.`
            : m && pct > 0
              ? `${fmtPts(Math.round((pct - m.yesPrice * 100) * 10) / 10)} from the market's ${pctLabel(m.yesPrice)}`
              : 'A Yes probability, 1–99%.'}
      </p>
    </>
  )
}

/**
 * The Drawdown tab (wave 3): the asset, the drawdown in % and what it measures from: the all-time high, its date and
 * source (what the history covers), the current price, the trigger level and, when the price is already past it, that
 * it fires on the next check. A high over 100× the price (`withheld`, athWithheld) is not shown and sets no level.
 */
function DrawdownFields({
  form,
  set,
  symbols,
  read,
  level,
  past,
  withheld,
}: {
  form: Form
  set: (patch: Partial<Form>) => void
  symbols: string[]
  read: { quote: AthQuote | null; error: string | null } | null
  level: number | null
  past: boolean
  withheld: boolean
}) {
  const q = read?.quote
  const bad = form.drawdownPct.trim() !== '' && level == null && !!q && !withheld
  return (
    <>
      <div>
        <Label htmlFor="auto-symbol">Asset</Label>
        <Select id="auto-symbol" value={form.symbol} onChange={(e) => set({ symbol: e.target.value })}>
          <SymbolOptions symbols={symbols} />
        </Select>
      </div>
      <div>
        <Label htmlFor="auto-drawdown">Drawdown from the all-time high (%)</Label>
        <Input
          id="auto-drawdown"
          type="number"
          inputMode="decimal"
          min={0.01}
          max={99.99}
          step="any"
          placeholder="20"
          value={form.drawdownPct}
          onChange={(e) => set({ drawdownPct: e.target.value })}
          aria-describedby="auto-ath"
          className="font-mono tabular-nums"
        />
        <div id="auto-ath" aria-live="polite" className="mt-1.5 space-y-0.5 text-[12px] text-muted">
          {!read ? (
            <p>Reading {form.symbol}'s all-time high…</p>
          ) : !q ? (
            <p className="text-down">{read.error}</p>
          ) : withheld ? (
            <>
              <p className="text-down">{ATH_WITHHELD} No drawdown alert can be set from it.</p>
              <p className="[overflow-wrap:anywhere]">{q.sourceLabel}</p>
              <p className="font-mono tabular-nums">Now {fmtIn(q.price, q.currency)}</p>
            </>
          ) : (
            <>
              <p className="text-text">
                All-time high <b className="font-mono font-semibold tabular-nums">{fmtIn(q.ath, q.currency)}</b> · {athDate(q.athAt)}
                {/* A checked price: a quote feed's is Yahoo's delayed latest trade, so not "live". */}
                {q.athLive && ' · set by the latest price'}
              </p>
              <p className="[overflow-wrap:anywhere]">{q.sourceLabel}</p>
              <p className="font-mono tabular-nums">
                Now {fmtIn(q.price, q.currency)} · {fmtPct(Math.abs(q.fromAthPct), { decimals: 1 })} below the high
              </p>
              {q.basis === 'listing' && <p>Compared on the listing's own {q.currency} price, not the USD paper mark.</p>}
              {bad ? (
                <p className="text-down">Pick a drawdown from 0.01% to 99.99%.</p>
              ) : level != null ? (
                <p className={past ? 'text-down' : 'text-text'}>
                  Fires at or below <b className="font-mono font-semibold tabular-nums">{fmtIn(level, q.currency)}</b>
                  {past && ': the price is already past it, so it would fire on the next check.'}
                </p>
              ) : null}
            </>
          )}
        </div>
      </div>
    </>
  )
}

const SOURCE = { manual: '', ticket: 'From the trade ticket', agent: 'From the agent' }

/** One automation as its pipeline (Trigger → Action → Deliver), then its status and, for a schedule, the next and last run. */
function AlertRow({ alert: a, mark, onCancel, onResume }: { alert: Alert; mark: number | undefined; onCancel: () => Promise<void>; onResume: () => Promise<void> }) {
  const [busy, setBusy] = useState(false)
  const busyRef = useRef(false)
  const [history, setHistory] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const research = a.action === 'research'
  const dd = a.trigger === 'drawdown' ? a.drawdown : null
  const hidden = dd?.source === 'yahoo' && athWithheld(dd.ath, dd.price ?? a.firedPrice) // its high and threshold are not shown
  const [action, levels] = actionLines({ ...a, sellPct: dd?.sellPct })
  const run = a.lastRun
  const source = httpUrl(a.market?.url) ?? (a.linkedMarket ? httpUrl(`https://polymarket.com/event/${encodeURIComponent(a.linkedMarket.eventSlug)}`) : null)
  return (
    <Card className="p-4">
      <Pipeline
        compact
        research={research}
        trigger={
          dd ? (
            <>
              <span className="flex items-center gap-2">
                <AssetIcon symbol={a.symbol} size={20} />
                <span>
                  {drawdownText(a.symbol, dd.pct)}
                  {!hidden && <> · ≤ <b className="font-mono font-semibold tabular-nums">{fmtIn(dd.threshold, dd.currency)}</b></>}
                </span>
              </span>
              {hidden ? (
                <span className="mt-0.5 block text-[11.5px] text-down">
                  {ATH_WITHHELD}
                  {a.status === 'active' && dd.price != null && <span className="font-mono tabular-nums"> Now {fmtIn(dd.price, dd.currency)}.</span>}
                </span>
              ) : (
                <span className="mt-0.5 block font-mono text-[11.5px] text-muted tabular-nums">
                  ATH {fmtIn(dd.ath, dd.currency)} · {athDate(dd.athAt)}
                  {a.status === 'active' && dd.price != null && dd.fromAthPct != null &&
                    ` · now ${fmtIn(dd.price, dd.currency)} (${fmtPct(Math.abs(dd.fromAthPct), { decimals: 1 })} below)`}
                </span>
              )}
              <span className="mt-0.5 block text-[11.5px] text-muted [overflow-wrap:anywhere]">
                {dd.sourceLabel}
                {dd.basis === 'listing' && ` · ${dd.currency} listing price`}
              </span>
            </>
          ) : a.trigger === 'odds' ? (
            <>
              <span className="flex items-center gap-2">
                <span aria-hidden="true" className="grid size-5 shrink-0 place-items-center rounded-full bg-surface text-accent-ink">
                  <IconSignal size={12} />
                </span>
                <span className="min-w-0 [overflow-wrap:anywhere]">
                  Polymarket · {a.market?.question ?? a.linkedMarket?.question ?? 'market'} · {a.op}{' '}
                  <b className="font-mono font-semibold tabular-nums">{pctLabel(a.price ?? 0)}</b>
                </span>
              </span>
              {a.status === 'active' && a.market && (
                <span className="mt-0.5 block font-mono text-[11.5px] text-muted tabular-nums">now {pctLabel(a.market.yesPrice)}</span>
              )}
            </>
          ) : (
          <>
            <span className="flex items-center gap-2">
              {a.symbol ? <AssetIcon symbol={a.symbol} size={20} /> : <IconBolt size={18} />}
              {a.trigger === 'schedule' ? (
                <span>
                  {a.symbol ? `${a.symbol} · ` : ''}{scheduleText(a.every, a.at, a.weekday)}
                </span>
              ) : (
                <span>
                  {a.symbol} {a.op === 'above' ? 'rises above' : 'falls below'}{' '}
                  <b className="font-mono font-semibold tabular-nums">{fmtIn(a.price, a.currency ?? 'USD')}</b>
                </span>
              )}
            </span>
            {a.trigger === 'price' && a.status === 'active' && mark != null && a.price != null && (
              <span className="mt-0.5 block font-mono text-[11.5px] text-muted tabular-nums">
                now {fmtIn(mark, a.currency ?? 'USD')} ({pctAway(a.price, mark)} away)
              </span>
            )}
          </>
          )
        }
        action={
          <>
            {action}
            {levels && <span className="mt-0.5 block font-mono text-[11.5px] text-muted tabular-nums">{levels}</span>}
          </>
        }
        deliver={deliverText(a.action)}
      />
      <div className="mt-3 flex flex-wrap items-center gap-x-3 gap-y-1.5 text-[12.5px] text-muted">
        <Status alert={a} />
        {a.status === 'active' && a.nextRunAt && <span>Next run {utcTime(a.nextRunAt)} UTC</span>}
        {a.forecastId && (
          <Link to={`/forecasts/${encodeURIComponent(a.forecastId)}`} className="font-medium text-accent-ink hover:underline">
            View forecast
          </Link>
        )}
        {!a.forecastId && a.trigger === 'odds' && source && (
          <ExternalLink href={source.href}>View market source</ExternalLink>
        )}
        {run && !research && (
          <span className={run.status === 'failed' ? 'text-down' : undefined}>
            Last run {utcTime(run.at)} UTC · {run.status === 'failed' ? run.error : run.body}
          </span>
        )}
        {research && <Button size="sm" variant="ghost" aria-expanded={history} onClick={() => setHistory(v => !v)}>Run history</Button>}
        {research && a.status === 'paused' && <Button size="sm" variant="secondary" loading={busy} onClick={async () => {
          if (busyRef.current) return
          busyRef.current = true; setBusy(true); setError(null)
          try { await onResume() } catch (err) { setError(errorMessage(err, "Couldn't resume the research. Try again.")) }
          finally { busyRef.current = false; setBusy(false) }
        }}>Resume</Button>}
        {(a.status === 'active' || a.status === 'paused') && (
          <Button
            variant="ghost"
            size="sm"
            className="ml-auto"
            loading={busy}
            onClick={async () => {
              if (busyRef.current) return
              busyRef.current = true
              setBusy(true)
              setError(null)
              try { await onCancel() } finally { busyRef.current = false; setBusy(false) }
            }}
          >
            Cancel
          </Button>
        )}
      </div>
      {research && <div className="mt-3 space-y-2 text-[13px] text-muted">
        <p>2 AI credits per run · {notifyWhenText(a.notifyWhen)}</p>
        {a.thesis && <div className="space-y-1 [overflow-wrap:anywhere]"><p><span className="font-medium text-text">Thesis: </span>{a.thesis.statement}</p><p><span className="font-medium text-text">Invalidation: </span>{a.thesis.invalidation}</p></div>}
        {a.status === 'paused' && <p>Paused after {a.quotaStreak} consecutive runs without enough AI credits. Resume when your allowance can cover a run, or Cancel.</p>}
        {run && <RunSummary run={run} />}
      </div>}
      {error && <p role="alert" className="mt-3 text-[13px] text-down">{error}</p>}
      {research && history && <ResearchHistory alert={a} />}
      {(a.note || SOURCE[a.source]) && (
        <p className="mt-2 text-[12.5px] text-muted">
          {a.note && <span className="text-text">{a.note}</span>}
          {a.note && SOURCE[a.source] && ' · '}
          {SOURCE[a.source]}
        </p>
      )}
    </Card>
  )
}

function Status({ alert: a }: { alert: Alert }) {
  switch (a.status) {
    case 'active':
      return <Badge tone="accent">Active</Badge>
    case 'paused':
      return <Badge tone="down">Paused</Badge>
    case 'cancelled':
      return <Badge>Cancelled</Badge>
    case 'failed':
      return <span className="text-[12.5px] leading-snug text-down">Failed: {a.error}</span>
  }
  const dd = a.trigger === 'drawdown' ? a.drawdown : null
  const at = a.trigger === 'odds' ? pctLabel(a.firedPrice ?? 0) : dd ? fmtIn(a.firedPrice, dd.currency) : fmtIn(a.firedPrice, a.currency ?? 'USD')
  const fired = `Fired ${a.firedAt ? shortTime(a.firedAt) : ''} at ${at}`
  return (
    <span className="flex flex-wrap items-center gap-1.5 text-[12.5px] text-muted">
      {fired}
      {dd?.outcome === 'nothing_to_sell' ? (
        <Badge>Nothing to sell: no paper long</Badge>
      ) : a.action !== 'notify' &&
        (a.tradeId ? (
          <Link to="/account" className="inline-flex items-center gap-1 font-medium text-accent-ink hover:underline">
            <Badge tone="up">Filled</Badge>
            <IconArrowRight size={12} />
          </Link>
        ) : dd ? (
          <Badge tone="up">Sold</Badge> // a drawdown's sale commits with its claim: no trade here means a reset removed it
        ) : (
          <Badge>Filling…</Badge> // fired, its trade not stored yet (the next poll shows Filled)
        ))}
    </span>
  )
}

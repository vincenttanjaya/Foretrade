// Under the order ticket: this symbol's active limit / stop-entry orders (buy/sell price alerts), each with Cancel.
import { useEffect, useState } from 'react'
import { Link } from 'react-router'
import { useToast } from '../../components/toast'
import { Button } from '../../components/ui/Button'
import { api } from '../../lib/api'
import { errorMessage } from '../../lib/errors'
import { fmtPrice, fmtUsd } from '../../lib/format'
import { usePortfolio } from '../../lib/portfolio'
import type { Alert } from '../../lib/types'

/** `version` bumps when the ticket places one; otherwise it refreshes with the 30 s portfolio poll. */
export function OpenOrders({ symbol, version }: { symbol: string; version: number }) {
  const { portfolio } = usePortfolio()
  const { toast } = useToast()
  const [orders, setOrders] = useState<Alert[]>([])
  const [reload, setReload] = useState(0)

  useEffect(() => {
    const ctrl = new AbortController()
    api
      .alerts('active', ctrl.signal)
      .then(({ alerts }) => setOrders(alerts.filter((a) => a.trigger === 'price' && a.symbol === symbol && a.action !== 'notify'))) // schedules live in Automations
      .catch(() => {}) // keep the last list; the next poll retries
    return () => ctrl.abort()
  }, [symbol, version, portfolio, reload])

  async function cancel(a: Alert) {
    try {
      await api.cancelAlert(a.id)
      setOrders((list) => list.filter((x) => x.id !== a.id))
      toast(`Limit ${a.action} cancelled.`)
    } catch (err) {
      toast(errorMessage(err, "Couldn't cancel the order. Please try again."), 'error')
      setReload((n) => n + 1)
    }
  }

  if (orders.length === 0) return null
  return (
    <section aria-label="Open orders" className="border-t border-border px-4 py-3">
      <div className="mb-2 flex items-baseline justify-between">
        <h3 className="label">Open orders · {orders.length}</h3>
        <Link to={`/automations?symbol=${encodeURIComponent(symbol)}`} className="text-[12px] text-accent-ink hover:underline">
          Automations
        </Link>
      </div>
      <ul className="space-y-1.5">
        {orders.map((a) => (
          <li key={a.id} className="flex items-center gap-2 rounded-lg bg-surface-2 py-1.5 pr-1.5 pl-3 text-[12.5px]">
            <span className="min-w-0 flex-1 truncate font-mono text-text tabular-nums">
              <span className={a.action === 'buy' ? 'text-up' : 'text-down'}>{a.action === 'buy' ? 'Buy' : 'Sell'}</span>{' '}
              {fmtUsd(a.notional, { decimals: 0 })} at {a.op === 'below' ? '≤' : '≥'} {fmtPrice(a.price)}
              {a.stopLoss != null && <span className="text-muted"> · SL {fmtPrice(a.stopLoss)}</span>}
              {a.takeProfit != null && <span className="text-muted"> · TP {fmtPrice(a.takeProfit)}</span>}
            </span>
            <Button
              variant="ghost"
              size="sm"
              className="h-7 px-2"
              onClick={() => cancel(a)}
              aria-label={`Cancel limit ${a.action} at ${fmtPrice(a.price)}`}
            >
              Cancel
            </Button>
          </li>
        ))}
      </ul>
    </section>
  )
}

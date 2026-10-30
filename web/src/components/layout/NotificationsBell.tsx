// The top bar's notifications bell: fired alerts (price, prediction-market odds and all-time-high drawdowns), scheduled briefs and orders, stop/target hits,
// autopilot fills and forecast results (GET /api/notifications, derived server-side). Errors are swallowed so pages and tests
// without the route still work.
import { useCallback, useEffect, useRef, useState, type ComponentType } from 'react'
import { Link, useNavigate } from 'react-router'
import { api } from '../../lib/api'
import { cx } from '../../lib/cx'
import { timeAgo } from '../../lib/format'
import type { Notification, NotificationKind } from '../../lib/types'
import { usePoll } from '../../lib/usePoll'
import {
  IconAlert,
  IconAgent,
  IconArrowDown,
  IconArrowRight,
  IconArrowUp,
  IconAutopilot,
  IconBell,
  IconBolt,
  IconCheck,
  IconForecast,
  IconSignal,
} from '../ui/icons'

const ICONS: Record<NotificationKind, [ComponentType<{ size?: number }>, string]> = {
  alert: [IconBolt, 'text-accent-ink'],
  alert_order: [IconCheck, 'text-up'],
  alert_failed: [IconAlert, 'text-down'],
  odds_alert: [IconSignal, 'text-accent-ink'], // Polymarket moved past an odds trigger's level
  drawdown_alert: [IconArrowDown, 'text-accent-ink'], // a drawdown trigger fired
  drawdown_order: [IconCheck, 'text-up'],
  drawdown_failed: [IconAlert, 'text-down'],
  workflow: [IconBolt, 'text-accent-ink'],
  workflow_order: [IconCheck, 'text-up'],
  workflow_failed: [IconAlert, 'text-down'],
  workflow_research: [IconAgent, 'text-accent-ink'],
  stop_loss: [IconArrowDown, 'text-down'],
  take_profit: [IconArrowUp, 'text-up'],
  autopilot: [IconAutopilot, 'text-accent-ink'],
  copy_order: [IconCheck, 'text-accent-ink'],
  copy_stopped: [IconAutopilot, 'text-muted'],
  forecast_completed: [IconForecast, 'text-accent-ink'],
  forecast_failed: [IconForecast, 'text-down'],
  forecast_resolved: [IconForecast, 'text-muted'],
}

/** "SOL fell below $175.00" + "Now $174.80 · note" → the agent prompt for a fired alert (a drawdown's reads the same). */
export const alertPrompt = (n: Notification) =>
  `${n.title.replace(' ', ' just ')} (${n.body.split(' · ')[0].replace(/^Now/, 'now')}). Should I act? Check the setup and draft a trade if it is there.`

export function NotificationsBell() {
  const navigate = useNavigate()
  const panel = useRef<HTMLDivElement>(null)
  const [items, setItems] = useState<Notification[]>([])
  const [unread, setUnread] = useState(0)

  const load = useCallback(async () => {
    try {
      const res = await api.notifications(20)
      setItems(res.items)
      setUnread(res.unread)
    } catch {
      // no route, offline or signed out: keep what we have, no toast
    }
  }, [])
  usePoll(load, 30_000)
  useEffect(() => {
    window.addEventListener('focus', load)
    return () => window.removeEventListener('focus', load)
  }, [load])

  // The native popover opens on the button itself; this marks the feed seen.
  function opened() {
    if (!unread) return
    setUnread(0)
    api.notificationsSeen().then(load, () => {})
  }
  const close = () => panel.current?.hidePopover()

  return (
    <>
      <button
        type="button"
        popoverTarget="notifications"
        onClick={opened}
        aria-label={unread ? `Notifications, ${unread} unread` : 'Notifications'}
        className="relative grid size-8 place-items-center rounded-full text-muted transition-colors hover:bg-surface-2 hover:text-text"
      >
        <IconBell size={18} />
        {unread > 0 && (
          <span
            aria-hidden="true"
            className="absolute -top-0.5 -right-1 grid h-4 min-w-4 place-items-center rounded-full bg-down px-1 font-mono text-[9.5px] leading-none font-semibold text-white tabular-nums"
          >
            {unread >= 99 ? '99+' : unread}
          </span>
        )}
      </button>
      <div
        id="notifications"
        ref={panel}
        popover="auto"
        aria-label="Notifications"
        className="card fixed inset-auto top-[60px] right-3 left-3 m-0 animate-fade-up p-0 text-text shadow-[0_16px_40px_-16px_rgb(15_27_45/0.35)] sm:left-auto sm:w-96"
      >
        <div className="flex items-center justify-between border-b border-border px-4 py-3">
          <span className="text-sm font-semibold">Notifications</span>
          <span className="label">Last 14 days</span>
        </div>
        {items.length === 0 ? (
          <p className="px-4 py-6 text-[13px] leading-relaxed text-muted">
            Nothing yet. Alerts, stop/target hits, autopilot and copied fills, stopped copy plans and forecast results show up here.
          </p>
        ) : (
          <ul className="max-h-[min(60vh,440px)] divide-y divide-border overflow-y-auto">
            {items.map((n) => {
              const [Icon, tone] = ICONS[n.kind] ?? [IconBell, 'text-muted']
              return (
                <li key={n.id} className="flex gap-3 px-4 py-3">
                  <span className={cx('mt-0.5 grid size-7 shrink-0 place-items-center rounded-full bg-surface-2', tone)}>
                    <Icon size={15} />
                  </span>
                  <div className="min-w-0 flex-1">
                    <Link to={n.link} onClick={close} className="block text-[13.5px] leading-snug font-medium text-text hover:underline">
                      {n.title}
                    </Link>
                    <p className="mt-0.5 text-[12.5px] leading-snug text-pretty text-muted">{n.body}</p>
                    <div className="mt-1 flex items-center gap-3 text-[11.5px] text-muted">
                      <time dateTime={n.at}>{timeAgo(n.at)}</time>
                      {(n.kind === 'alert' || n.kind === 'drawdown_alert') && (
                        <button
                          type="button"
                          onClick={() => {
                            close()
                            navigate('/', { state: { prompt: alertPrompt(n), context: n.symbol ? { symbol: n.symbol } : undefined } })
                          }}
                          className="font-medium text-accent-ink hover:underline"
                        >
                          Ask the agent
                        </button>
                      )}
                    </div>
                  </div>
                </li>
              )
            })}
          </ul>
        )}
        <Link
          to="/automations"
          onClick={close}
          className="flex items-center justify-center gap-1 border-t border-border px-4 py-2.5 text-[13px] font-medium text-accent-ink hover:bg-surface-2"
        >
          All automations <IconArrowRight size={14} />
        </Link>
      </div>
    </>
  )
}

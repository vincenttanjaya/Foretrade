// Community strategies: the board under Top strategies → Community, and the publish / star / copy controls
// the strategy page uses. 0 credits, no model call anywhere.
import { useRef, useState } from 'react'
import { Link, useNavigate } from 'react-router'
import { useToast } from '../../components/toast'
import { AssetIcon } from '../../components/ui/AssetIcon'
import { Badge } from '../../components/ui/Badge'
import { Button } from '../../components/ui/Button'
import { Card } from '../../components/ui/Card'
import { EmptyState } from '../../components/ui/EmptyState'
import { IconAlert } from '../../components/ui/icons'
import { PriceChange } from '../../components/ui/PriceChange'
import { Sparkline } from '../../components/ui/Sparkline'
import { Skeleton } from '../../components/ui/Spinner'
import { api } from '../../lib/api'
import { cx } from '../../lib/cx'
import { errorMessage } from '../../lib/errors'
import type { CommunityInfo, Strategy, StrategySummary } from '../../lib/types'
import { useLoad } from './parts'
import { compareForward, fmtNum } from './strategy'
import { ForwardLine, ForwardSort } from './forward'

const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`

/** "3 copies · 1 deploy". */
export const usage = (c: Pick<CommunityInfo, 'copies' | 'deploys'>) => `${plural(c.copies, 'copy', 'copies')} · ${plural(c.deploys, 'deploy')}`

/** "by Ann", or "by You" on the caller's own. */
export const byline = (c: Pick<CommunityInfo, 'owner' | 'mine'>) => `by ${c.mine ? 'You' : c.owner}`

/** '★ n' toggle: optimistic, reverts with a toast when the API refuses. */
export function StarButton({ id, stars, starred, className }: { id: string; stars: number; starred: boolean; className?: string }) {
  const { toast } = useToast()
  const [state, setState] = useState({ stars, starred })
  const [busy, setBusy] = useState(false)

  async function toggle() {
    if (busy) return
    const before = state
    const on = !before.starred
    setState({ stars: before.stars + (on ? 1 : -1), starred: on })
    setBusy(true)
    try {
      setState(await api.starStrategy(id, on))
    } catch (err) {
      setState(before)
      toast(errorMessage(err, 'Could not update the star.'), 'error')
    } finally {
      setBusy(false)
    }
  }

  return (
    <button
      type="button"
      aria-pressed={state.starred}
      aria-label={`${state.starred ? 'Unstar' : 'Star'} · ${plural(state.stars, 'star')}`}
      onClick={toggle}
      className={cx(
        'relative z-10 inline-flex h-8 items-center gap-1 rounded-lg border px-2.5 font-mono text-[12.5px] tabular-nums transition-colors',
        state.starred ? 'border-accent/50 bg-accent-soft text-accent-ink' : 'border-border bg-surface text-muted hover:text-text',
        className,
      )}
    >
      <span aria-hidden="true">★</span> {state.stars}
    </button>
  )
}

/** Top strategies → Community: every user's published strategies, full samples first, then stars and Sharpe. */
export function CommunityBoard() {
  const { data, error, retry } = useLoad((signal) => api.strategies('community', signal).then((r) => r.strategies ?? []))
  const [order, setOrder] = useState<'default' | 'forward'>('default')
  const listed = data && order === 'forward' ? [...data].sort((a, b) => compareForward(a.forward, b.forward)) : data

  return (
    <>
      <header className="max-w-3xl">
        <div className="label">Community · published by Foretrade users</div>
        <h1 className="mt-3 font-display text-[38px] leading-[1.04] font-medium tracking-[-0.02em] text-balance text-text md:text-[56px]">
          Strategies other traders publish.
        </h1>
        <p className="mt-4 max-w-2xl text-[16px] leading-relaxed text-pretty text-muted">
          Readable rules and their backtests, shared from other studios. Star the ones you like, or copy one into your studio:
          its backtest re-runs on fresh data and you can deploy it to a paper autopilot.
        </p>
      </header>
      <div className="mt-6 flex justify-end"><ForwardSort label="Community strategy order" value={order} onChange={setOrder} /></div>
      <Card className="mt-3 overflow-hidden">
        {!data && error ? (
          <EmptyState
            className="m-5"
            icon={<IconAlert size={20} />}
            title="Couldn't load the community board."
            description={errorMessage(error)}
            action={<Button variant="secondary" onClick={retry}>Try again</Button>}
          />
        ) : data?.length === 0 ? (
          <p className="px-5 py-12 text-center text-sm text-muted">No community strategies yet. Publish one from your studio to list it here.</p>
        ) : (
          <ul aria-label="Community strategies" aria-busy={!data} className="divide-y divide-border">
            {listed ? listed.map((s) => <Row key={s.id} s={s} />) : [0, 1, 2].map((i) => <li key={i} className="px-5 py-4"><Skeleton className="h-12 w-full" /></li>)}
          </ul>
        )}
      </Card>
      <p className="mt-3 text-[12.5px] text-muted">Ranked by stars and Sharpe among strategies with 180+ days and 10+ trades; short samples follow.</p>
    </>
  )
}

function Row({ s }: { s: StrategySummary }) {
  const c = s.community
  const m = s.metrics
  const edge = m ? m.totalReturnPct - m.buyHoldReturnPct : null
  return (
    <li className="group relative grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-4 gap-y-2 px-5 py-4 transition-colors hover:bg-surface-2/60 md:grid-cols-[minmax(0,1.6fr)_120px_70px_minmax(0,1fr)_auto]">
      <div className="min-w-0">
        <Link to={`/strategies/${s.id}`} className="font-medium text-text group-hover:text-accent-ink after:absolute after:inset-0">
          {s.name}
        </Link>
        <div className="mt-0.5 flex flex-wrap items-center gap-x-2 text-[12.5px] text-muted">
          {c && <span>{byline(c)}</span>}
          <span className="inline-flex items-center gap-1.5 font-mono text-[12px]">
            <AssetIcon symbol={s.symbol} size={16} />
            {s.symbol} · {s.interval}
          </span>
          {s.shortSample && <Badge title="Under 180 days or fewer than 10 trades: the Sharpe says little">Short sample</Badge>}
        </div>
        <ForwardLine f={s.forward} symbol={s.symbol} className="mt-2" />
      </div>
      <Sparkline values={s.sparkline ?? []} width={120} height={32} className="hidden md:block" />
      <div className="hidden text-right md:block">
        <div className="label">Sharpe</div>
        <div className="font-mono text-[13.5px] font-medium text-text tabular-nums">{fmtNum(m?.sharpe)}</div>
      </div>
      <div className="col-span-2 flex flex-wrap items-baseline gap-x-2 text-[13px] md:col-span-1">
        <span className="font-mono text-muted md:hidden">Sharpe {fmtNum(m?.sharpe)} ·</span>
        <PriceChange value={m?.totalReturnPct} decimals={1} />
        <span className="text-[12px] text-muted">{edge == null ? '' : `${edge >= 0 ? '+' : '−'}${Math.abs(edge).toFixed(1)} pts vs buy & hold`}</span>
      </div>
      {c && (
        <div className="col-span-2 flex items-center gap-3 md:col-span-1 md:justify-end">
          <StarButton id={s.id} stars={c.stars} starred={c.starred} />
          <span className="font-mono text-[11.5px] whitespace-nowrap text-muted">{usage(c)}</span>
        </div>
      )}
    </li>
  )
}

const PUBLISH_COPY =
  "Publishing shows your display name, this strategy's rules, backtest and candle-derived live forward to every Foretrade user. Autopilot results stay private."

/** Own strategy: 'Publish to community' behind a confirm; once published, a chip with its stars and copies, and Unpublish. */
export function PublishControl({ s, onChange }: { s: Strategy; onChange: (s: Strategy) => void }) {
  const { toast } = useToast()
  const dialog = useRef<HTMLDialogElement>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const c = s.community

  async function set(published: boolean) {
    setBusy(true)
    setError(null)
    try {
      const res = await api.publishStrategy(s.id, published)
      onChange(res.strategy)
      dialog.current?.close()
      toast(published ? 'Published to the community.' : 'Unpublished. Stars and copies are kept.')
    } catch (err) {
      setError(errorMessage(err, published ? 'Could not publish the strategy.' : 'Could not unpublish the strategy.'))
    } finally {
      setBusy(false)
    }
  }

  if (!c) return null
  if (c.published)
    return (
      <div className="flex flex-wrap items-center gap-2">
        <Badge tone="accent">Published · ★ {c.stars} · {plural(c.copies, 'copy', 'copies')}</Badge>
        <Button variant="ghost" size="sm" loading={busy} onClick={() => set(false)}>
          Unpublish
        </Button>
        {error && <p role="alert" className="w-full text-[12.5px] text-down">{error}</p>}
      </div>
    )

  return (
    <>
      <Button variant="secondary" onClick={() => { setError(null); dialog.current?.showModal() }}>
        Publish to community
      </Button>
      <dialog
        ref={dialog}
        aria-labelledby="publish-title"
        onCancel={(e) => busy && e.preventDefault()}
        className="card m-auto w-[calc(100%-2rem)] max-w-md animate-fade-up p-6 text-text backdrop:bg-[rgb(15_27_45/0.45)] backdrop:backdrop-blur-[2px]"
      >
        <div className="label mb-2">Community</div>
        <h2 id="publish-title" className="font-display text-2xl leading-tight font-medium">
          Publish “{s.name}”?
        </h2>
        <p className="mt-2.5 text-sm leading-relaxed text-muted">{PUBLISH_COPY}</p>
        {error && <p role="alert" className="mt-3 text-[13px] text-down">{error}</p>}
        <div className="mt-6 flex justify-end gap-2">
          <Button variant="ghost" onClick={() => dialog.current?.close()} disabled={busy}>
            Cancel
          </Button>
          <Button variant="primary" onClick={() => set(true)} loading={busy}>
            Publish
          </Button>
        </div>
      </dialog>
    </>
  )
}

/** 'Copy to my studio': re-backtests on fresh data server-side, then opens the copy. */
export function CopyButton({ id, variant = 'primary' }: { id: string; variant?: 'primary' | 'secondary' }) {
  const navigate = useNavigate()
  const { toast } = useToast()
  const [busy, setBusy] = useState(false)

  async function copy() {
    setBusy(true)
    try {
      const res = await api.copyStrategy(id)
      toast('Copied to your studio. Backtest re-run on fresh data.')
      navigate(`/strategies/${res.strategy.id}`, { state: { strategy: res.strategy } })
    } catch (err) {
      toast(errorMessage(err, 'Could not copy the strategy.'), 'error')
      setBusy(false)
    }
  }

  return (
    <Button variant={variant} loading={busy} onClick={copy}>
      {busy ? 'Copying…' : 'Copy to my studio'}
    </Button>
  )
}

/** "Copied from <name> by <owner>", linked: the API only names a source the caller can still open. */
export function CopiedFrom({ from }: { from: NonNullable<CommunityInfo['copiedFrom']> }) {
  return (
    <p className="mt-2 text-[13px] text-muted">
      Copied from{' '}
      <Link to={`/strategies/${from.id}`} className="font-medium text-accent-ink hover:underline">
        {from.name}
      </Link>{' '}
      by {from.owner}
    </p>
  )
}

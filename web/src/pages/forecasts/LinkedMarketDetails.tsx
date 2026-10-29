import type { LinkedMarket } from '../../lib/types'
import { cx } from '../../lib/cx'
import { httpUrl } from './forecast'
import { ExternalLink } from './DeskVsMarket'

/** Preserve the external predicate while making Foretrade's manual tracking boundary explicit. */
export function LinkedMarketDetails({ market: m, className }: { market: LinkedMarket; className?: string }) {
  const source = httpUrl(m.resolutionSource)
  return (
    <section aria-label="Linked Polymarket contract" className={cx('min-w-0 rounded-xl border border-accent/40 bg-accent-soft/40 p-4 text-[13px] [overflow-wrap:anywhere]', className)}>
      <h2 className="label">Original Polymarket contract</h2>
      <p className="mt-2 font-medium">{m.question}</p>
      <p className="mt-2 text-muted">Manual resolution in Foretrade. The tracking date records the provider's UTC end day; it does not change the observation time or promise Polymarket settlement.</p>
      <p className="mt-2 text-muted">Provider ends {m.endDate} · snapshot {m.asOf}</p>
      <p className="mt-1 text-muted">Leaf {m.marketSlug} · ID {m.marketId}</p>
      <div className="mt-2 flex flex-wrap gap-x-4 gap-y-2">
        <ExternalLink href={`https://polymarket.com/event/${encodeURIComponent(m.eventSlug)}`}>View original market</ExternalLink>
        {source ? <ExternalLink href={source.href}>Resolution source</ExternalLink> : <span className="text-muted">Resolution source: {m.resolutionSource || 'Not supplied; see complete rules.'}</span>}
      </div>
      <details className="mt-3">
        <summary className="cursor-pointer font-medium text-accent-ink">Complete original rules</summary>
        <p className="mt-2 whitespace-pre-wrap text-muted">{m.description}</p>
      </details>
    </section>
  )
}

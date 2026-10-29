import { useCallback, useState, type ReactNode } from 'react'
import { IconInfo } from '../../components/ui/icons'
import { api } from '../../lib/api'
import { fmtPct, fmtUsd } from '../../lib/format'
import type { Derivatives } from '../../lib/types'
import { usePoll } from '../../lib/usePoll'
import { isStockPerp } from '../strategies/strategy'

const ABOUT = 'Perpetual-futures data from Hyperliquid, for context. Paper trades here are spot.'

/** Perps positioning under the price header: funding, open interest, premium. Hidden when Hyperliquid has nothing. */
export function DerivativesRow({ symbol }: { symbol: string }) {
  const about = isStockPerp(symbol) ? 'Hyperliquid perp context. Paper fills use its mark; no funding accrual, dividends or split adjustment.' : ABOUT
  const [d, setD] = useState<Derivatives | null>(null)
  const load = useCallback(() => {
    api.derivatives(symbol).then(
      (r) => setD(r.derivatives),
      () => {}, // ponytail: decoration only; the last value stays and the next poll retries
    )
  }, [symbol])
  usePoll(load, 60_000)
  if (!d) return null

  const tone = d.fundingRatePct > 0 ? 'text-up' : d.fundingRatePct < 0 ? 'text-down' : 'text-text'
  return (
    <section
      aria-label="Perpetual futures"
      className="flex flex-wrap items-center gap-x-5 gap-y-1.5 rounded-xl border border-border bg-surface px-4 py-2.5 text-[12.5px] sm:px-5"
    >
      <span className="inline-flex items-center gap-1.5">
        <span className="label">Perps · Hyperliquid</span>
        <span title={about} aria-label={about} role="img" className="inline-flex text-muted">
          <IconInfo size={13} />
        </span>
      </span>
      <Stat label="Funding">
        <span className={tone}>{fmtPct(d.fundingRatePct, { sign: true, decimals: 4 })}/h</span>
        <span className="text-muted"> ({fmtPct(d.fundingAnnualizedPct, { decimals: 1 })} ann.)</span>
      </Stat>
      <Stat label="Open interest">{fmtUsd(d.openInterestUsd, { compact: true, decimals: 2 })}</Stat>
      <Stat label="Premium">{fmtPct(d.premiumPct, { sign: true, decimals: 2 })}</Stat>
    </section>
  )
}

function Stat({ label, children }: { label: string; children: ReactNode }) {
  return (
    <span className="whitespace-nowrap text-muted">
      {label} <span className="font-mono text-text tabular-nums">{children}</span>
    </span>
  )
}

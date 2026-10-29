import { createContext, use, useCallback, useRef, useState, type ReactNode } from 'react'
import { api } from './api'
import type { Portfolio } from './types'
import { usePoll } from './usePoll'

type PortfolioState = {
  portfolio: Portfolio | null // null until the first load
  refresh: () => Promise<void>
  setPortfolio: (p: Portfolio) => void // order / close / reset responses already carry the new portfolio
}

const PortfolioContext = createContext<PortfolioState | null>(null)

export function PortfolioProvider({ children }: { children: ReactNode }) {
  const [portfolio, updatePortfolio] = useState<Portfolio | null>(null)
  const version = useRef(0)
  const revision = useRef(-1) // highest server revision applied; the provider remounts per user (App's RequireAuth)

  // Every snapshot lands here: polls, order / close / reset / invest responses, trade-card confirms.
  // A server revision orders account states, so overlapping mutations can't roll the balance back.
  // `current` is false for a read that a newer read or a mutation has overtaken: at an equal revision
  // (or with none) its marks may be older, so request order breaks the tie.
  const apply = useCallback((p: Portfolio, current = true) => {
    if (p.revision != null && p.revision < revision.current) return
    if (!current && (p.revision == null || p.revision === revision.current)) return
    if (p.revision != null) revision.current = p.revision
    updatePortfolio(p)
  }, [])

  const setPortfolio = useCallback(
    (p: Portfolio) => {
      version.current++ // invalidate reads started before this committed mutation
      apply(p)
    },
    [apply],
  )

  const refresh = useCallback(async () => {
    const request = ++version.current
    try {
      const { portfolio: p } = await api.portfolio()
      apply(p, request === version.current)
    } catch {
      // keep the last value; the next poll retries
    }
  }, [apply])

  usePoll(refresh, 30_000)

  return <PortfolioContext value={{ portfolio, refresh, setPortfolio }}>{children}</PortfolioContext>
}

export function usePortfolio() {
  const ctx = use(PortfolioContext)
  if (!ctx) throw new Error('usePortfolio must be used inside <PortfolioProvider>')
  return ctx
}

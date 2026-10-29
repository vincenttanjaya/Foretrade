import { createContext, use, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { api } from './api'
import { isListedIn, useInstruments } from './instruments'
import { realtimeOpen } from './quoteLabel'
import type { Asset } from './types'
import { usePoll } from './usePoll'

type AssetsState = {
  assets: Asset[] // empty until the first load: the named rows in the API's order, then the listed rows
  bySymbol: Record<string, Asset | undefined>
  /** The listed symbol the newest named answer was asked about (`with`), so a page can tell "not read yet" from "no quote". */
  answeredWith: string | null
}

const AssetsContext = createContext<AssetsState | null>(null)
const ActiveContext = createContext<(symbol: string | null) => void>(() => {})

const NAMED_MS = 10_000
const REALTIME_MS = 5_000 // the listing on screen is priced by a real-time source in session: follow it closely
const LISTED_MS = 60_000 // the server re-reads a listed row about every 5 min unless someone views or holds it

/**
 * Prices for every instrument, from two polls:
 * - every 10 s, the named rows plus the listed symbol on screen (`scope=named&with=…`: the server reads it within a
 *   minute while it is viewed); every 5 s while the symbol on screen is priced by a real-time source in its session
 *   (`quoteFeed.class` realtime, open), so the Trade page follows it (the server polls the source every 10 s);
 * - every 60 s, the listed rows (`scope=listed`), only once the catalogue has listed rows. Against an API without them
 *   it never starts, and the named poll sends today's request.
 * Merged by symbol: a listed row missing from the newest listed answer is halted and dropped; the named answer wins for
 * its `with` symbol.
 */
export function AssetsProvider({ children }: { children: ReactNode }) {
  const catalogue = useInstruments()
  const [named, setNamed] = useState<{ assets: Asset[]; with: string | null }>({ assets: [], with: null })
  const [listed, setListed] = useState<Asset[]>([])
  const [active, setActive] = useState<string | null>(null)
  const hasListed = catalogue.listed.length > 0
  const withSymbol = active && isListedIn(catalogue, active) ? active : null
  // Answers can overtake each other when `with` changes mid-poll: apply only one newer than the last applied.
  const seq = useRef({ named: 0, namedApplied: 0, listed: 0, listedApplied: 0 })
  const onScreen = active ? (named.assets.find((a) => a.symbol === active) ?? listed.find((a) => a.symbol === active)) : undefined
  const namedMs = realtimeOpen(onScreen?.quoteFeed) ? REALTIME_MS : NAMED_MS

  usePoll(
    async () => {
      const n = ++seq.current.named
      try {
        const { assets } = hasListed ? await api.assets('named', withSymbol ? [withSymbol] : undefined) : await api.assets()
        if (n < seq.current.namedApplied) return
        seq.current.namedApplied = n
        setNamed({ assets, with: withSymbol })
      } catch {
        // keep the last prices; the next poll retries
      }
    },
    namedMs,
    true,
    withSymbol ?? '', // a new listed symbol on screen is read now, not on the next tick
  )

  usePoll(
    async () => {
      const n = ++seq.current.listed
      try {
        const { assets } = await api.assets('listed')
        if (n < seq.current.listedApplied) return
        seq.current.listedApplied = n
        setListed(assets)
      } catch {
        // keep the last listed prices; a halted row drops out with the next good answer
      }
    },
    LISTED_MS,
    hasListed,
  )

  // An API rolled back to one without the listed rows: drop the ones the last listed answer carried.
  useEffect(() => {
    if (!hasListed) setListed([])
  }, [hasListed])

  const value = useMemo(() => {
    const isListed = (s: string) => isListedIn(catalogue, s)
    const overlay = new Map(named.assets.filter((a) => isListed(a.symbol)).map((a) => [a.symbol, a]))
    const listedRows = listed.filter((a) => isListed(a.symbol)).map((a) => overlay.get(a.symbol) ?? a)
    const seen = new Set(listedRows.map((a) => a.symbol))
    for (const [symbol, a] of overlay) if (!seen.has(symbol)) listedRows.push(a)
    const assets = [...named.assets.filter((a) => !isListed(a.symbol)), ...listedRows]
    return { assets, bySymbol: Object.fromEntries(assets.map((a) => [a.symbol, a])), answeredWith: named.with }
  }, [named, listed, catalogue])

  return (
    <ActiveContext value={setActive}>
      <AssetsContext value={value}>{children}</AssetsContext>
    </ActiveContext>
  )
}

export function useAssets() {
  const ctx = use(AssetsContext)
  if (!ctx) throw new Error('useAssets must be used inside <AssetsProvider>')
  return ctx
}

/** The assets when a provider is mounted, else null: for components that only add a label from them. */
export const useAssetsIfAny = () => use(AssetsContext)

/** The Trade page names its symbol, so the 10 s poll carries a listed one (`with`) while it is on screen. */
export function useActiveSymbol(symbol: string) {
  const set = use(ActiveContext)
  useEffect(() => {
    set(symbol)
    return () => set(null)
  }, [set, symbol])
}

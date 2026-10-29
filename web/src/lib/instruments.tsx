import { createContext, use, useEffect, useState, type ReactNode } from 'react'
import { api, ApiError } from './api'
import type { Instrument } from './types'

/**
 * The instrument catalogue (GET /api/market/instruments): every instrument the API trades, priced or not, with the
 * listed Asian index stocks the named universe (SYMBOLS) does not carry.
 * - `loading`: the first read is in flight and nothing is cached yet. Pages wait rather than send a listed symbol away.
 * - `ready`: the catalogue is in.
 * - `absent`: no catalogue (an API without the route answers 404, or the read failed): SYMBOLS only, today's behaviour.
 *   `retrying` says it is absent only because a read failed (not a 404) and the next try is due: a deep link to a symbol
 *   the bundle does not know waits for it rather than leaving.
 */
export type Catalogue = {
  status: 'loading' | 'ready' | 'absent'
  loaded: boolean // status !== 'loading'
  retrying: boolean
  rows: Instrument[]
  bySymbol: Readonly<Record<string, Instrument | undefined>>
  listed: Instrument[] // the rows with `listed`, in the API's order
}

const STORAGE_KEY = 'instruments'
const RETRY_MS = 60_000 // a failed read (not a 404) is tried again this often while the page is open

function catalogue(status: Catalogue['status'], rows: Instrument[] = [], retrying = false): Catalogue {
  return {
    status,
    loaded: status !== 'loading',
    retrying,
    rows,
    bySymbol: Object.fromEntries(rows.map((r) => [r.symbol, r])),
    listed: rows.filter((r) => r.listed),
  }
}

/** Outside a provider (and in tests that mount none) the catalogue is absent: SYMBOLS only. */
const ABSENT = catalogue('absent')
const InstrumentsContext = createContext<Catalogue>(ABSENT)

/** Rows with the fields the pages read; anything else is dropped rather than trusted. */
function clean(rows: unknown): Instrument[] {
  if (!Array.isArray(rows)) return []
  return rows.filter(
    (r): r is Instrument =>
      !!r && typeof r === 'object' && typeof (r as Instrument).symbol === 'string' && (r as Instrument).symbol !== '' &&
      typeof (r as Instrument).name === 'string' && typeof (r as Instrument).venue === 'string',
  ).map((r) => ({ ...r, listed: r.listed === true }))
}

type Stored = { etag: string | null; instruments: Instrument[] }

function readStored(): Stored | null {
  try {
    const raw = sessionStorage.getItem(STORAGE_KEY)
    if (!raw) return null
    const s = JSON.parse(raw) as Partial<Stored>
    const instruments = clean(s?.instruments)
    return instruments.length ? { etag: typeof s.etag === 'string' ? s.etag : null, instruments } : null
  } catch {
    return null // storage blocked or a malformed copy: read the API instead
  }
}

function writeStored(s: Stored | null) {
  try {
    if (s) sessionStorage.setItem(STORAGE_KEY, JSON.stringify(s))
    else sessionStorage.removeItem(STORAGE_KEY)
  } catch {
    // storage blocked or full: the catalogue still works for this page
  }
}

/**
 * Reads the catalogue once per session. A copy kept in sessionStorage shows at once; the API is read anyway (the
 * browser's HTTP cache answers it from its ETag and max-age), and the copy is replaced only when the ETag changed.
 */
export function InstrumentsProvider({ children }: { children: ReactNode }) {
  const [stored] = useState(readStored)
  const [state, setState] = useState<Catalogue>(() => (stored ? catalogue('ready', stored.instruments) : catalogue('loading')))
  const [attempt, setAttempt] = useState(0)

  useEffect(() => {
    const ctrl = new AbortController()
    let retry: ReturnType<typeof setTimeout> | undefined
    api.instruments(ctrl.signal).then(
      ({ instruments, etag }) => {
        if (etag && stored?.etag === etag) return // the copy on screen is this version
        const rows = clean(instruments)
        setState(catalogue(rows.length ? 'ready' : 'absent', rows))
        writeStored(rows.length ? { etag, instruments: rows } : null)
      },
      (err) => {
        if (ctrl.signal.aborted) return
        if (err instanceof ApiError && err.status === 404) {
          // An API without the catalogue: named instruments only, and no stale copy of a newer one.
          setState(ABSENT)
          writeStored(null)
          return
        }
        setState((s) => (s.status === 'ready' ? s : catalogue('absent', [], true))) // keep a stored copy; else SYMBOLS only until a read works
        retry = setTimeout(() => setAttempt((n) => n + 1), RETRY_MS)
      },
    )
    return () => {
      ctrl.abort()
      clearTimeout(retry)
    }
  }, [attempt, stored])

  return <InstrumentsContext value={state}>{children}</InstrumentsContext>
}

export const useInstruments = () => use(InstrumentsContext)

/** True for a listed Asian stock the catalogue knows (not a named instrument). */
export const isListedIn = (c: Pick<Catalogue, 'bySymbol'>, symbol: string) => c.bySymbol[symbol]?.listed === true


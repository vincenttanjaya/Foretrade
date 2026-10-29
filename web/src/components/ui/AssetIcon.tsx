import { useState } from 'react'
import { cx } from '../../lib/cx'
import { useInstruments } from '../../lib/instruments'

// Bundled logos keyed by universe symbol (file name, upper-cased). `?no-inline` keeps each one a content-hashed
// file instead of a data URI in the JS: the bundle gains local asset URLs, and the logos stay cached across
// deploys that do not touch them.
const bySymbol = (files: Record<string, string>): Record<string, string> =>
  Object.fromEntries(Object.entries(files).map(([path, url]) => [path.slice(path.lastIndexOf('/') + 1).replace(/\.(svg|png)$/i, '').toUpperCase(), url]))

// Round coin logos from src/assets/coins (sources and licences in LICENSE.md there).
const COINS = bySymbol(import.meta.glob<string>('../../assets/coins/*.{svg,png}', { eager: true, query: '?no-inline', import: 'default' }))
// Plain stock, ETF and index marks from src/assets/stocks/{ID}.svg|png (convention and provenance in LICENSE.md there).
const STOCKS = bySymbol(import.meta.glob<string>('../../assets/stocks/*.{svg,png}', { eager: true, query: '?no-inline', import: 'default' }))

// Legal and group words a monogram skips (LOGO-PLAN §6): "PT Bank Negara Indonesia (Persero) Tbk" reads BN, not PB.
const LEGAL_PHRASES = /\b(public company limited|joint stock company)\b/gi
const LEGAL_WORDS = new Set(['PT', 'TBK', 'BERHAD', 'BHD', 'CTCP', 'CO', 'LTD', 'LIMITED', 'INC', 'CORP', 'CORPORATION', 'HOLDINGS', 'HOLDING', 'GROUP', 'THE', 'PLC'])

/**
 * Up to three letters for a company with no bundled mark, from its English name: legal words dropped, then an all-caps
 * 2–3 letter first word as is ("SD Guthrie" → SD), else the first two initials ("Gold Circuit Electronics" → GC), else
 * the one word's initial. Empty when the name has no word left.
 */
export function monogram(name: string) {
  const words = name
    .replace(/\([^)]*\)/g, ' ') // "(Persero)"
    .replace(LEGAL_PHRASES, ' ')
    .split(/[\s,.]+/)
    .filter((w) => /^[\p{L}\p{N}]/u.test(w) && !LEGAL_WORDS.has(w.toUpperCase()))
  const first = words[0]
  if (!first) return ''
  if (/^[\p{Lu}\p{N}]{2,3}$/u.test(first) && /\p{Lu}/u.test(first)) return first
  return words.slice(0, 2).map((w) => w.charAt(0).toUpperCase()).join('')
}

// Padding around a stock mark on its white disc, as a fraction of the icon size.
const STOCK_PADDING = 0.14

// An outline paints over the image, so this rim gives the near-black discs (XRP, DOT,
// HBAR, ENA) an edge on the dark theme and the white ones (TAO, WLD, XLM, every stock disc) on the light.
const RIM = 'outline -outline-offset-1 outline-black/10 dark:outline-white/20'

/**
 * Round coin logo, or a stock mark on a white disc, per symbol (bundled, no external images). The neutral
 * monogram is only a fallback: for a symbol with no file, or a logo that fails to load. It reads the company's
 * initials from `name`, else from the instrument catalogue's name (a listed stock's ticker starts with a digit, so a
 * position or an alert row shows "SF" for 000810.KS, not "0"); with neither, the symbol's first character.
 */
export function AssetIcon({ symbol, name, size = 28, className }: { symbol: string; name?: string; size?: number; className?: string }) {
  const listedName = useInstruments().bySymbol[symbol]?.name
  const stock = STOCKS[symbol]
  const src = stock ?? COINS[symbol]
  const [failed, setFailed] = useState<string>() // keyed by URL, so a reused icon tries its next symbol's logo
  if (src && failed !== src) {
    const image = { src, alt: '', draggable: false, decoding: 'async', onError: () => setFailed(src) } as const
    if (stock) {
      // The disc is a wrapper, not padding on the <img>: a browser trims a replaced element's content to its
      // rounded content box, which would cut square marks and flags into circles.
      return (
        <span
          aria-hidden="true"
          className={cx('inline-block shrink-0 rounded-full bg-white select-none', RIM, className)}
          style={{ width: size, height: size, padding: size * STOCK_PADDING }}
        >
          <img {...image} loading="lazy" className="block size-full object-contain" />
        </span>
      )
    }
    return (
      <img
        {...image}
        aria-hidden="true"
        width={size}
        height={size}
        // bg-surface-2 is the placeholder disc until the file paints (first view only; then it is cached)
        className={cx('inline-block shrink-0 rounded-full bg-surface-2 select-none', RIM, className)}
        style={{ width: size, height: size }}
      />
    )
  }
  const company = name ?? listedName
  const letters = (company && monogram(company)) || symbol.charAt(0)
  return (
    <span
      aria-hidden="true"
      className={cx('inline-grid shrink-0 place-items-center rounded-full bg-ink font-semibold text-ink-fg select-none', RIM, className)}
      style={{ width: size, height: size, fontSize: size * (letters.length > 1 ? 0.38 : 0.46) }}
    >
      {letters}
    </span>
  )
}

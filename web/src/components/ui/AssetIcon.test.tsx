import { fireEvent, render, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { api } from '../../lib/api'
import { InstrumentsProvider } from '../../lib/instruments'
import * as fx from '../../test/fixtures'
import { SYMBOLS, symbolGroup } from '../../pages/strategies/strategy'
import stockLicense from '../../assets/stocks/LICENSE.md?raw'
import { AssetIcon, monogram } from './AssetIcon'

const svgSources = import.meta.glob<string>('../../assets/{coins,stocks}/*.svg', { query: '?raw', import: 'default', eager: true })
const coinFiles = Object.keys(import.meta.glob('../../assets/coins/*.{svg,png}'))
const stockFiles = Object.keys(import.meta.glob('../../assets/stocks/*.{svg,png}'))
const stockPngs = import.meta.glob<string>('../../assets/stocks/*.png', { query: '?inline', import: 'default', eager: true })
const fileName = (path: string) => path.slice(path.lastIndexOf('/') + 1)
const fileSymbol = (path: string) => fileName(path).replace(/\.(svg|png)$/i, '')
// The listed Asian stocks' logo record (generated with the logo files): each listed symbol has a file or a
// monogram with its reason. Absent until the first listed logo ships.
type ListedLogo = { file?: string; monogram?: string; checked?: string }
const listedLogos = Object.values(import.meta.glob<Record<string, ListedLogo>>('../../assets/stocks/listed-logos.json', { eager: true, import: 'default' }))[0] ?? {}

// Every non-crypto row of the instrument table (US and Asia stocks, ETFs and indices) must ship a real logo.
const STOCK_SYMBOLS = SYMBOLS.map(([symbol]) => symbol).filter((symbol) => symbolGroup(symbol) !== 'crypto')
const COIN_SYMBOLS = SYMBOLS.map(([symbol]) => symbol).filter((symbol) => symbolGroup(symbol) === 'crypto')

describe('bundled SVG paint references', () => {
  it.each(Object.entries(svgSources))('resolves local paint and clip references in %s', (file, source) => {
    const svg = new DOMParser().parseFromString(source, 'image/svg+xml')
    expect(svg.querySelector('parsererror'), `${file}: invalid SVG`).toBeNull()
    const ids = Array.from(svg.querySelectorAll('[id]'), (element) => element.id)
    expect(new Set(ids).size, `${file}: duplicate fragment IDs`).toBe(ids.length)
    const references = Array.from(source.matchAll(/url\(\s*['"]?#([^'"\s)]+)['"]?\s*\)/g), (match) => match[1])
    for (const element of svg.querySelectorAll('*')) for (const attribute of element.attributes) {
      if ((attribute.name === 'href' || attribute.localName === 'href') && attribute.value.startsWith('#')) references.push(attribute.value.slice(1))
    }
    expect(references.filter((id) => !ids.includes(id)), `${file}: unresolved local SVG references`).toEqual([])
  })
})

describe('stock logo files (src/assets/stocks, convention in LICENSE.md there)', () => {
  it('has a logo file for every stock, ETF and index row in the instrument table', () => {
    expect(STOCK_SYMBOLS.length).toBeGreaterThan(0)
    const bundled = new Set(stockFiles.map(fileSymbol))
    expect(STOCK_SYMBOLS.filter((symbol) => !bundled.has(symbol)), 'stock rows without src/assets/stocks/{ID}.svg|png').toEqual([])
  })

  it('names each file after a configured stock symbol or a listed stock with a file in listed-logos.json, with no coin duplicate', () => {
    const listedFiles = Object.entries(listedLogos).filter(([, logo]) => logo.file).map(([symbol]) => symbol)
    for (const file of stockFiles) expect([...STOCK_SYMBOLS, ...listedFiles], `${fileName(file)}: neither a stock row of SYMBOLS nor a listed-logos.json file`).toContain(fileSymbol(file))
    const coins = new Set(coinFiles.map((file) => fileSymbol(file).toUpperCase()))
    expect(STOCK_SYMBOLS.filter((symbol) => coins.has(symbol)), 'stocks with a leftover src/assets/coins file').toEqual([])
  })

  it('records every listed logo as one file that exists or one monogram with its reason, never both', () => {
    const bundled = new Set(stockFiles.map(fileName))
    for (const [symbol, logo] of Object.entries(listedLogos)) {
      expect(!!logo.file !== !!logo.monogram, `${symbol}: exactly one of file and monogram`).toBe(true)
      if (logo.file) expect(bundled.has(logo.file) && fileSymbol(logo.file) === symbol, `${symbol}: ${logo.file} is not its bundled file`).toBe(true)
      else expect([...bundled].some((f) => fileSymbol(f) === symbol), `${symbol}: a monogram with a file`).toBe(false)
      expect(STOCK_SYMBOLS, `${symbol}: a named symbol in the listed record`).not.toContain(symbol)
    }
  })

  it('lists every file in LICENSE.md with its source', () => {
    for (const file of stockFiles) expect(stockLicense, `${fileName(file)} missing from stocks/LICENSE.md`).toContain(`\`${fileName(file)}\``)
  })

  it.each(Object.entries(svgSources).filter(([file]) => file.includes('/stocks/')))('%s has no script, event handler, external reference or editor metadata', (file, source) => {
    const svg = new DOMParser().parseFromString(source, 'image/svg+xml')
    expect(svg.querySelectorAll('script, foreignObject, image, style, metadata, title').length, `${file}: disallowed element`).toBe(0)
    for (const element of svg.querySelectorAll('*')) {
      expect(element.namespaceURI, `${file}: non-SVG element <${element.tagName}>`).toBe('http://www.w3.org/2000/svg')
      for (const attribute of element.attributes) {
        expect(attribute.name, `${file}: event handler`).not.toMatch(/^on/i)
        if (attribute.localName === 'href') expect(attribute.value, `${file}: external href`).toMatch(/^#/)
        for (const [, target] of attribute.value.matchAll(/url\(\s*['"]?([^'")]*)/g)) expect(target, `${file}: external url()`).toMatch(/^#/)
      }
    }
    expect(source).not.toMatch(/sodipodi|inkscape|<!--|<\?xml|<!DOCTYPE/i)
  })

  it('keeps native stock PNGs large enough for icons and bounded in file size', () => {
    for (const [file, dataUri] of Object.entries(stockPngs)) {
      const bytes = Uint8Array.from(atob(dataUri.slice(dataUri.indexOf(',') + 1)), (c) => c.charCodeAt(0))
      const view = new DataView(bytes.buffer)
      const [width, height] = [view.getUint32(16), view.getUint32(20)] // IHDR
      expect(Math.max(width, height), `${file}: longest side under 80 px`).toBeGreaterThanOrEqual(80)
      expect(Math.min(width, height), `${file}: shortest side under 16 px`).toBeGreaterThanOrEqual(16)
      expect(bytes.length, `${file}: over 128 KB`).toBeLessThanOrEqual(128 * 1024)
    }
  })
})

describe('AssetIcon', () => {
  it.each(COIN_SYMBOLS)('draws the bundled round %s coin logo, not a monogram', (symbol) => {
    const { container } = render(<AssetIcon symbol={symbol} size={24} className="ring-2" />)
    const logo = container.firstElementChild as HTMLImageElement
    expect(logo.tagName).toBe('IMG')
    expect(logo.getAttribute('src')).toMatch(new RegExp(`/assets/coins/${symbol.toLowerCase()}\\.(svg|png)\\b`))
    expect(logo).toHaveAttribute('alt', '')
    expect(logo).toHaveAttribute('aria-hidden', 'true')
    expect(logo.style.width).toBe('24px') // fixed box, so nothing shifts while it loads
    expect(logo.style.height).toBe('24px')
    expect(logo).toHaveClass('rounded-full', 'ring-2')
  })

  it.each(STOCK_SYMBOLS)('draws the bundled %s stock logo on a padded white disc, not a monogram', (symbol) => {
    const { container } = render(<AssetIcon symbol={symbol} size={40} className="ring-2" />)
    const disc = container.firstElementChild as HTMLElement
    expect(disc.tagName).toBe('SPAN')
    expect(disc).toHaveClass('rounded-full', 'bg-white', 'ring-2')
    expect(disc).toHaveAttribute('aria-hidden', 'true')
    expect([disc.style.width, disc.style.height]).toEqual(['40px', '40px'])
    expect(parseFloat(disc.style.padding)).toBeCloseTo(40 * 0.14)
    expect(disc.children).toHaveLength(1)
    const logo = disc.firstElementChild as HTMLImageElement
    expect(logo.tagName).toBe('IMG')
    expect(logo.getAttribute('src')).toMatch(new RegExp(`/assets/stocks/${symbol}\\.(svg|png)\\b`))
    expect(logo).toHaveAttribute('alt', '')
    expect(logo).toHaveClass('object-contain', 'size-full')
    expect(logo).not.toHaveClass('rounded-full') // a rounded <img> would trim square marks and flags into circles
    expect(disc.textContent).toBe('')
  })

  it('reads initials from the company name for a listed stock with no file', () => {
    const cases: [string, string][] = [
      ['SD Guthrie Berhad', 'SD'],
      ['SSI Securities Corporation', 'SSI'],
      ['Gold Circuit Electronics Ltd.', 'GC'],
      ['Sands China Ltd.', 'SC'],
      ['Vinpearl Joint Stock Company', 'V'],
      ['Hitachi, Ltd.', 'H'],
      ['Samsung Fire & Marine Insurance Co., Ltd.', 'SF'],
      ['PT Bank Negara Indonesia (Persero) Tbk', 'BN'],
      ['Kasikornbank Public Company Limited', 'K'],
      ['The Siam Cement Public Company Limited', 'SC'],
      ['KB Financial Group Inc.', 'KB'],
      ['Sony Group Corporation', 'S'],
      ['NAVER Corporation', 'N'],
      ['Vietnam Dairy Products Joint Stock Company', 'VD'],
      ['CTCP Vinhomes', 'V'],
      ['Ltd.', ''],
    ]
    for (const [name, letters] of cases) expect(monogram(name), name).toBe(letters)

    const { container, rerender } = render(<AssetIcon symbol="2UNBUNDLED.TW" name="Gold Circuit Electronics Ltd." size={40} />)
    const chip = container.firstElementChild as HTMLElement
    expect(chip.textContent).toBe('GC')
    expect(chip).toHaveClass('bg-ink', 'text-ink-fg', 'rounded-full') // the neutral disc: never brand colours
    expect(parseFloat(chip.style.fontSize)).toBeCloseTo(40 * 0.38)
    rerender(<AssetIcon symbol="2UNBUNDLED.TW" size={40} />) // no name: the first character, as before
    expect(container.textContent).toBe('2')
    expect(parseFloat((container.firstElementChild as HTMLElement).style.fontSize)).toBeCloseTo(40 * 0.46)
    rerender(<AssetIcon symbol="2UNBUNDLED.TW" name="Ltd." size={40} />) // nothing left of the name
    expect(container.textContent).toBe('2')
  })

  it("reads a listed stock's initials from the instrument catalogue where no name is passed", async () => {
    const unbundled = { ...fx.listedInstruments[1], symbol: 'UNBUNDLED.KS' }
    vi.spyOn(api, 'instruments').mockResolvedValue({ instruments: [unbundled], etag: null })
    const { container } = render(<AssetIcon symbol="UNBUNDLED.KS" size={24} />, { wrapper: InstrumentsProvider })
    await waitFor(() => expect(container.textContent).toBe('SF')) // Samsung Fire & Marine Insurance Co., Ltd.
  })

  it('keeps a bundled mark over the name, and loads stock marks lazily', () => {
    const { container } = render(<AssetIcon symbol="DBS" name="DBS Group" size={26} />)
    const logo = container.querySelector('img')!
    expect(logo.getAttribute('src')).toMatch(/\/assets\/stocks\/DBS\.svg\b/)
    expect(logo).toHaveAttribute('loading', 'lazy') // a long market list does not load every mark at once
  })

  it('keeps the neutral monogram for an unknown symbol', () => {
    const { container } = render(<AssetIcon symbol="ZZZ" size={20} />)
    const chip = container.firstElementChild as HTMLElement
    expect(chip.tagName).toBe('SPAN')
    expect(chip.textContent).toBe('Z')
    expect(chip).toHaveClass('bg-ink', 'text-ink-fg', 'rounded-full')
    expect(chip).toHaveAttribute('aria-hidden', 'true')
    expect(chip.style.width).toBe('20px')
    expect(container.querySelector('img')).toBeNull()
  })

  it('falls back to the monogram if a logo fails to load, and tries the next symbol', () => {
    const { container, rerender } = render(<AssetIcon symbol="BTC" />)
    fireEvent.error(container.querySelector('img')!)
    expect(container.querySelector('img')).toBeNull()
    expect(container.textContent).toBe('B')
    rerender(<AssetIcon symbol="ETH" />)
    expect(container.querySelector('img')?.getAttribute('src')).toMatch(/\/eth\.svg\b/)
  })

  it('falls back to the monogram if a stock logo fails to load', () => {
    const { container, rerender } = render(<AssetIcon symbol="TSM" size={26} />)
    fireEvent.error(container.querySelector('img')!)
    expect(container.querySelector('img')).toBeNull()
    expect(container.firstElementChild).toHaveClass('bg-ink', 'rounded-full')
    expect(container.textContent).toBe('T')
    rerender(<AssetIcon symbol="SKHX" size={26} />)
    expect(container.querySelector('img')?.getAttribute('src')).toMatch(/\/assets\/stocks\/SKHX\.svg\b/)
  })
})

import { render, screen, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from '../../lib/api'
import type { DeskView, Forecast } from '../../lib/types'
import * as fx from '../../test/fixtures'
import { renderPage, stubSession } from '../../test/utils'
import { BucketViewLine, changeLine, DeskViewHero, MedianDots, RangeStrip, stripDomain, stripPos } from './DeskView'
import { ForecastDetailPage } from './ForecastDetailPage'
import { inOrder } from './forecast'

const view = fx.tsmDeskResult.view!
const final = inOrder(fx.tsmDeskResult.probabilities, fx.tsmBuckets)
const hero = (v: DeskView = view) => render(<DeskViewHero view={v} final={final} resolutionDate="2031-10-06" />)

const openReport = (f: Forecast) => {
  vi.spyOn(api, 'forecast').mockResolvedValue({ forecast: f })
  return renderPage(<ForecastDetailPage />, { route: `/forecasts/${f.id}`, path: '/forecasts/:id' })
}

describe('DeskViewHero · a numeric report that leads with its view', () => {
  it('leads with the median, how far it is from today and the two ranges', () => {
    hero()
    expect(screen.getByText('Desk view · close on Oct 6, 2031')).toBeInTheDocument()
    expect(screen.getByText('$611').nextSibling).toHaveTextContent('Median')
    const sub = screen.getByText((_, el) => el?.tagName === 'P' && el.textContent === "+26% vs today's $485 · about +4.7% a year")
    expect([...sub.children].map((c) => c.textContent), 'each part on one line').toEqual(["+26% vs today's $485", '· about +4.7% a year'])
    expect(sub).toHaveAttribute('title', expect.stringMatching(/^TSM, NYSE regular-market price · Yahoo Finance \(delayed\), as of Oct 6, 2026/))
    const tiles = [...document.querySelectorAll('dl > div')].map((d) => d.textContent)
    expect(tiles).toEqual(['Chance at or above today62%', '50% range$363–$989', '80% range≈$242–≈$1,480'])
    expect(screen.getByText('Valuation scenarios with cited inputs, weighed with the measured history.'), 'what the median rests on').toBeInTheDocument()
  })

  it('names the capped drift when the measured history sets the centre', () => {
    const label = "Measured TSM history since 1997; drift +9.4%/yr (half the history's own CAGR, under the cap of +10.0%/yr: an assumption)"
    hero({ ...view, basis: { kind: 'capped_drift', label } })
    expect(screen.getByText('$611').nextSibling).toHaveTextContent('Median')
    expect(screen.getByText(`${label}.`)).toBeInTheDocument()
  })

  it('leads a view no evidence anchors with its 50% range and states no median, change or chance above today', () => {
    hero(fx.tsmLegacyResult.view!)
    expect(screen.getByText('Desk view · for Oct 6, 2031')).toBeInTheDocument()
    const big = screen.getByText('$356').parentElement!
    expect(big).toHaveTextContent('$356–$917')
    expect(big.nextSibling).toHaveTextContent('50% range')
    expect(screen.queryByText('$570')).not.toBeInTheDocument()
    expect(screen.queryByText('Median')).not.toBeInTheDocument()
    expect([...document.querySelectorAll('dl > div')].map((d) => d.textContent)).toEqual(['80% range≈$247–≈$1,350'])
    expect(screen.getByText("Read from the desk's ranges: no cited scenarios or measured history set its centre, so it states no median.")).toBeInTheDocument()
    expect(screen.getByRole('img').getAttribute('aria-label')).toBe('50% range $356 to $917. 80% range about $247 to about $1,350.')
    expect(document.querySelector('[data-mark="median"]')).toBeNull()
    expect(screen.getByText('< $300').nextSibling, 'no reference, no hints').toBeNull()
    // A reference and a change that came with an unanchored view are not stated either.
    document.body.innerHTML = ''
    hero({ ...view, basis: { kind: 'none', label: 'No basis.' } })
    expect(screen.queryByText(/vs today/)).not.toBeInTheDocument()
    expect(screen.queryByText('Median')).not.toBeInTheDocument()
    expect(screen.getByText('No basis.')).toBeInTheDocument()
  })

  it('shows each range with its distance from today and never calls a flat one "most likely"', () => {
    hero()
    const bars = screen.getAllByRole('listitem').filter((li) => li.textContent?.includes('%'))
    expect(bars.map((li) => li.textContent)).toEqual([
      '< $300below -38%17%',
      '$300 – $450-38%…-7%17%',
      '$450 – $600-7%…+24%15%',
      '$600 – $800+24%…+65%16%',
      '$800 – $1,100+65%…+127%15%',
      '≥ $1,100above +127%20%',
    ])
    expect(screen.getByText('No single range stands out (top range ≥ $1,100 at 20%).')).toBeInTheDocument()
    expect(screen.queryByText(/most likely/i)).not.toBeInTheDocument()
    expect(document.querySelector('.text-accent-ink.font-semibold'), 'no range in gold when none stands out').toBeNull()
  })

  it('names a range when one does stand out', () => {
    hero({ ...view, flat: false })
    expect(screen.queryByText(/No single range stands out/)).not.toBeInTheDocument()
    expect(screen.getByText('≥ $1,100').className).toMatch(/font-medium/)
  })

  it('marks quantiles in an extrapolated tail with ≈ and drops what needs a reference', () => {
    hero({ ...view, median: { value: 1250, approx: true }, p75: { value: 1300, approx: true }, p90: { value: 1600, approx: true }, pAbove: { value: 0.93, approx: true } })
    expect(screen.getByText('≈$1,250')).toBeInTheDocument()
    expect(screen.getByText('$363–≈$1,300')).toBeInTheDocument()
    expect(screen.getByText('≈93%')).toBeInTheDocument()
    expect(screen.queryByText(/vs today/)).not.toBeInTheDocument()
  })

  it('writes a linear change in the question’s unit', () => {
    const cpi: DeskView = {
      ...view, scale: 'linear', unit: { prefix: '', suffix: '%' }, horizonDays: 200, changePct: null, annualPct: null, change: 0.3,
      reference: { ...view.reference!, value: 2.4 },
    }
    expect(changeLine(cpi)).toBe("+0.3% vs today's 2.4%")
    expect(changeLine({ ...view, annualPct: null })).toBe("+26% vs today's $485")
    expect(changeLine({ ...view, reference: { ...view.reference!, value: 472.2 } }), "today's level at its own precision, as the lead").toBe(
      "+26% vs today's $472.20 · about +4.7% a year",
    )
    expect(changeLine({ ...view, reference: null })).toBe('')
    expect(changeLine({ ...view, median: { value: 1250, approx: true } }), 'an extrapolated median gets no change').toBe('')
    expect(changeLine({ ...view, basis: { kind: 'none', label: '' } }), 'nor one no evidence anchors').toBe('')
  })
})

describe('RangeStrip', () => {
  it('speaks its numbers and places the bands on a log axis', () => {
    render(<RangeStrip view={view} />)
    const strip = screen.getByRole('img', { name: 'Median $611. 50% range $363 to $989. 80% range about $242 to about $1,480. Today $485.' })
    expect(stripDomain(view).map((x) => Math.round(x))).toEqual([202, 1772])
    expect(stripPos(view, 485)).toBeCloseTo(40.4, 0)
    expect(strip.querySelector<HTMLElement>('[data-mark="median"]')!.style.left).toMatch(/^51\./)
    expect(strip.querySelector('[data-mark="today"]')).not.toBeNull()
    expect(screen.getByText('≈$242')).toBeInTheDocument()
    expect(screen.getByText('today $485')).toBeInTheDocument()
  })

  it('has no today marker without a reference, and stays in range on a linear axis', () => {
    render(<RangeStrip view={{ ...view, reference: null }} />)
    expect(screen.getByRole('img').getAttribute('aria-label')).not.toMatch(/Today/)
    expect(document.querySelector('[data-mark="today"]')).toBeNull()
    const lin: DeskView = { ...view, scale: 'linear', reference: null, p10: { value: -1 }, p25: { value: 0 }, median: { value: 1 }, p75: { value: 2 }, p90: { value: 3 } }
    expect(stripDomain(lin)).toEqual([-1.8, 3.8])
    expect([stripPos(lin, -50), stripPos(lin, 1), stripPos(lin, 50)].map(Math.round)).toEqual([0, 50, 100])
    expect(stripPos(view, -1), 'nothing below zero on a log axis').toBe(0)
  })
})

describe('BucketViewLine and MedianDots', () => {
  it('puts the view under a top-range hero, with a median only on an anchoring basis', () => {
    render(<BucketViewLine view={fx.btcBucketResult.view!} />)
    expect(screen.getByText('Median $67,700 · 80% $62,400–$73,500 · 64% above today')).toBeInTheDocument()
    document.body.innerHTML = ''
    render(<BucketViewLine view={{ ...fx.btcBucketResult.view!, basis: { kind: 'none', label: '' } }} />)
    expect(screen.getByText('50% $65,100–$69,900 · 80% $62,400–$73,500')).toBeInTheDocument()
  })

  it('places each median on the hero axis', () => {
    render(<MedianDots view={view} rows={[{ label: 'Quant', q: { value: 692.8 } }, { label: 'Final', q: view.median, final: true }]} />)
    const rows = within(screen.getByText('Medians').parentElement!).getAllByRole('listitem')
    expect(rows.map((li) => li.textContent)).toEqual(['Quant$693', 'Final$611'])
  })
})

describe('ForecastDetailPage · the hero switch', () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date(2026, 9, 7, 12))
    stubSession()
  })
  afterEach(() => vi.useRealTimers())

  it('leads a numeric report with the view and puts the lead before the summary', async () => {
    openReport(fx.tsmDeskForecast())
    expect(await screen.findByText('Desk view · close on Oct 6, 2031')).toBeInTheDocument()
    expect(screen.queryByText(/Most likely/)).not.toBeInTheDocument()
    const lead = screen.getByText(/^Our median is \$611 for Oct 6, 2031/)
    expect(lead.nextElementSibling).toHaveTextContent(/^We expect TSM to compound with earnings/)
    expect(lead.className).toMatch(/font-medium/)
  })

  it('keeps the top-range hero for a short peaked ladder and adds the view line', async () => {
    openReport(fx.btcBucketForecast())
    expect(await screen.findByText('Most likely range')).toBeInTheDocument()
    expect(screen.getByText('$66,000 – $70,000', { selector: 'span.font-display' })).toBeInTheDocument()
    expect(screen.getByText('Median $67,700 · 80% $62,400–$73,500 · 64% above today')).toBeInTheDocument()
    expect(screen.queryByText(/^Our median is/), 'the lead is for the view hero').not.toBeInTheDocument()
  })

  it('states no median for a run whose methods found no evidence, nor for a view without a basis', async () => {
    const judged = fx.tsmDeskResult.specialists.map((s, i) => ({ ...s, method: i === 1 ? ('fallback' as const) : ('judgment' as const) }))
    const none = { kind: 'none' as const, label: "Read from the desk's ranges: no cited scenarios or measured history set its centre, so it states no median." }
    openReport(fx.tsmDeskForecast({ result: { ...fx.tsmDeskResult, specialists: judged, view: { ...view, basis: none } } }))
    expect(await screen.findByText(`${none.label}`)).toBeInTheDocument()
    expect(screen.queryByText('Median')).not.toBeInTheDocument()
    expect(screen.queryByText('Chance at or above today')).not.toBeInTheDocument()
    expect(screen.queryByText('Medians'), 'no median plot').not.toBeInTheDocument()
    document.body.innerHTML = ''
    const { basis: _b, ...bare } = view
    openReport(fx.tsmDeskForecast({ id: 'v-bare', result: { ...fx.tsmDeskResult, view: bare as DeskView } }))
    expect(await screen.findByText(none.label), 'a view that came without a basis is read as none').toBeInTheDocument()
    expect(screen.queryByText('Median')).not.toBeInTheDocument()
  })

  it('reads an old report through its derived view, and a binary one as before', async () => {
    openReport(fx.tsmDeskForecast({ result: fx.tsmLegacyResult }))
    expect(await screen.findByText('Desk view · for Oct 6, 2031')).toBeInTheDocument()
    expect(screen.getByText("The desk's 50% range for Oct 6, 2031 is $356–$917 and its 80% range ≈$247–≈$1,350.")).toBeInTheDocument()
    expect(screen.getByText(/^We stay close to the ensemble/), 'the stored summary is shown as stored').toBeInTheDocument()
    expect(screen.queryByText('Median')).not.toBeInTheDocument()
  })

  it('ignores a view on a binary result', async () => {
    openReport(fx.forecast({ result: { ...fx.forecastResult, view } }))
    expect(await screen.findByText('Probability estimate · Yes')).toBeInTheDocument()
    expect(screen.queryByText(/Desk view/)).not.toBeInTheDocument()
  })
})

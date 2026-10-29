import { render, screen, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import type { OutlookScenario } from '../../lib/types'
import * as fx from '../../test/fixtures'
import { aboutYears, fmtMove, horizonYears, ScenarioTable, TimeToTargetTable, volBasisText } from './OutlookParts'

describe('outlook helpers', () => {
  it.each([
    [30, 'about 30 years'],
    [11.44, 'about 11 years'],
    [2.75, 'about 2.8 years'],
    [1, 'about 1 year'],
  ])('aboutYears(%s) → %s', (y, s) => {
    expect(aboutYears(y)).toBe(s)
  })

  it('rounds moves to whole percents from ±10%', () => {
    expect([fmtMove(326.6), fmtMove(-57.2), fmtMove(4.24), fmtMove(-0.04), fmtMove(null)]).toEqual(['+327%', '-57%', '+4.2%', '0.0%', '—'])
  })

  it('reads horizons in 365-day years and names the vol basis', () => {
    expect([horizonYears(1825), horizonYears(548), horizonYears(7300)]).toEqual([5, 1.5, 20])
    expect(volBasisText(undefined)).toBeNull()
    expect(volBasisText({ window: '90d', days: 90, since: '2026-07-05' })).toBe('90-day realized volatility')
  })
})

describe('ScenarioTable', () => {
  const scenario = (over: Partial<OutlookScenario>): OutlookScenario => ({ name: 'Steady', kind: 'base', p: 0.5, level: 120, changePct: 20, annualPct: null, inputs: [], ...over })

  it('names level, return and unknown inputs, and keeps an unknown kind neutral', () => {
    render(
      <ScenarioTable
        spot={100}
        scenarios={[
          scenario({
            name: 'Range',
            kind: 'sideways',
            changePct: Number.NaN,
            inputs: [
              { field: 'level', value: 95, basis: 'quote', kind: 'source', verified: true },
              { field: 'cagrPct', value: 8.5, basis: 'model knowledge, as of 2025-06', kind: 'model_knowledge', verified: false },
              { field: 'cagrPct', value: -4, basis: 'model knowledge', kind: 'model_knowledge', verified: false },
              { field: 'eps', value: 3.1, basis: 'assumption', kind: 'assumption', verified: false },
              { field: 'beta', value: 1.25, basis: 'assumption', kind: 'assumption', verified: false },
            ],
          }),
        ]}
      />,
    )
    expect(within(screen.getByRole('list', { name: 'Range inputs' })).getAllByRole('listitem').map((li) => li.textContent)).toEqual([
      'Level $95 · quote',
      'Return +8.5%/yr · model knowledge 2025-06',
      'Return -4%/yr · model knowledge',
      'EPS $3.10 · assumption',
      'beta 1.25 · assumption',
    ])
    expect(screen.getByText('sideways')).toHaveClass('bg-surface-2')
    expect(screen.getByText('+20%')).toBeInTheDocument() // from the level when changePct is missing
  })

  it('tones a tail by its side: up above every other scenario (a 10x question’s super-cycle), down below them', () => {
    render(
      <ScenarioTable
        spot={425}
        scenarios={[
          scenario({ name: 'Strait blockade', kind: 'tail', level: 110 }),
          scenario({ name: 'Capex digestion', kind: 'bear', level: 411 }),
          scenario({ name: 'Foundry leader', kind: 'base', level: 1233 }),
          scenario({ name: 'AI demand stays hot', kind: 'bull', level: 2493 }),
          scenario({ name: 'AI super-cycle', kind: 'tail', level: 7030 }),
        ]}
      />,
    )
    const [down, up] = screen.getAllByText('tail')
    expect(down).toHaveClass('text-down')
    expect(up).toHaveClass('text-up')
    expect(screen.getByText('bull')).toHaveClass('text-up')
    expect(screen.getByText('bear')).toHaveClass('text-down')
  })
})

describe('TimeToTargetTable', () => {
  it('drops the table, ladder and per-scenario years when the API sends none', () => {
    const t = fx.tsmOutlookView.timeToTarget!
    render(<TimeToTargetTable symbol="TSM" data={{ ...t, rows: [], byScenario: [], requiredCagr: [], medianYears: 2.75, ownSince: null }} />)
    expect(screen.getByText('Median time to 10× ($4,850): about 2.8 years')).toBeInTheDocument()
    expect(screen.queryByRole('table')).toBeNull()
    expect(screen.queryByText(/≈:/)).toBeNull()
    expect(screen.queryByText(/^Needs/)).toBeNull()
  })

  it('omits the since year when the own CAGR has no start date', () => {
    const t = fx.tsmOutlookView.timeToTarget!
    render(<TimeToTargetTable symbol="TSM" data={{ ...t, ownSince: null }} />)
    expect(screen.getByText("TSM's own CAGR: +13.2%/yr")).toBeInTheDocument()
  })
})

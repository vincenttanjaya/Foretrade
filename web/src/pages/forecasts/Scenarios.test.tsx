import { cleanup, render, screen, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import type { DeskScenario } from '../../lib/types'
import * as fx from '../../test/fixtures'
import { scenariosOf } from './forecast'
import { inputText, Scenarios, type IdTargets } from './Scenarios'

const $ = { prefix: '$', suffix: '' }
const targets = (measured = true): IdTargets => ({
  sources: new Map(fx.tsmSources.map((s) => [s.id, s])),
  findings: new Map(fx.tsmFindings.map((f) => [f.id, f])),
  measured,
})
const show = (list: DeskScenario[] = scenariosOf({ scenarios: [...fx.tsmScenarios].reverse() }), t = targets()) =>
  render(<Scenarios scenarios={list} unit={$} targets={t} />)
const card = (name: string) => screen.getByRole('heading', { level: 3, name }).closest('li')!

describe('Scenarios · what has to happen', () => {
  it('lists the scenarios low to high under its heading', () => {
    show()
    expect(screen.getByText('Scenarios · 4')).toBeInTheDocument()
    expect(screen.getByRole('heading', { level: 2, name: 'What has to happen' })).toBeInTheDocument()
    expect(screen.getByText(/Odds come from the desk's final distribution over each scenario's range/)).toBeInTheDocument()
    expect(screen.getAllByRole('heading', { level: 3 }).map((h) => h.textContent)).toEqual([
      'Taiwan blockade or conflict',
      'AI capex digestion',
      'Compounding foundry leader',
      'AI supercycle',
    ])
  })

  it('shows each kind, its range, the desk odds (whole percents summing to 100) and the level', () => {
    show()
    const kinds = ['tail', 'bear', 'base', 'bull'].map((k) => screen.getByText(k, { selector: 'span' }))
    expect(kinds.map((k) => k.className.match(/border-border|text-down|bg-surface-2|text-up/)?.[0])).toEqual(['border-border', 'text-down', 'bg-surface-2', 'text-up'])
    expect(card('Taiwan blockade or conflict')).toHaveTextContent('below $283')
    expect(card('AI capex digestion')).toHaveTextContent('$283–$510')
    expect(card('AI supercycle')).toHaveTextContent('above $999')
    const pcts = ['Taiwan blockade or conflict', 'AI capex digestion', 'Compounding foundry leader', 'AI supercycle'].map(
      (n) => card(n).querySelector('.font-display.text-\\[40px\\]')!.textContent,
    )
    expect(pcts).toEqual(['15%', '26%', '35%', '24%'])
    expect(card('Compounding foundry leader')).toHaveTextContent('≈ $752 · +55% · +9.2%/yr')
  })

  it("shows the model's own weight only when it differs by 5 points or more", () => {
    show()
    expect(within(card('Taiwan blockade or conflict')).getByText('model 5%')).toBeInTheDocument()
    expect(within(card('Compounding foundry leader')).getByText('model 50%')).toBeInTheDocument()
    expect(within(card('AI capex digestion')).queryByText(/^model/), '26% vs 30%').not.toBeInTheDocument()
    expect(within(card('AI supercycle')).queryByText(/^model/), '24% vs 20%').not.toBeInTheDocument()
  })

  it('links the ids a condition cites: S to its article, F to its source, H1 to the measured history', () => {
    show()
    const base = card('Compounding foundry leader')
    const [inCondition, inInput] = within(base).getAllByRole('link', { name: /^F3/ })
    expect(inCondition.closest('ul')).toHaveTextContent(/^•FY2032 EPS near \$38/)
    expect(inCondition).toHaveAttribute('href', 'https://estimates.example/tsm')
    expect(inCondition).toHaveAttribute('title', fx.tsmFindings[2].claim)
    expect(inInput).toHaveAttribute('href', 'https://estimates.example/tsm')
    const bull = card('AI supercycle')
    expect(within(bull).getByRole('link', { name: /^S1/ })).toHaveAttribute('href', 'https://news.example/tsmc-guidance')
    expect(within(bull).getByRole('link', { name: 'H1' })).toHaveAttribute('href', '#measured')
    expect(within(bull).getByRole('link', { name: /^S1/ })).toHaveAttribute('target', '_blank')
    // A thesis's ids link the same way; one the run does not have stays text.
    cleanup()
    show(scenariosOf({ scenarios: [{ ...fx.tsmScenarios[1], thesis: 'Capex pauses (F1, F9).', sourceIds: ['F1'] }, fx.tsmScenarios[2]] }))
    const thesis = screen.getByText(/^Capex pauses/)
    expect(within(thesis).getByRole('link', { name: /^F1/ })).toHaveAttribute('href', 'https://news.example/tsmc-guidance')
    expect(thesis).toHaveTextContent('Capex pauses (F1 (opens in a new tab), F9).')
  })

  it('leaves ids as text when they do not resolve or the scenario does not cite them', () => {
    const list = scenariosOf({
      scenarios: [
        { ...fx.tsmScenarios[1], mustBeTrue: ['As F9 and S3 and F2 say'], sourceIds: ['F9', 'S3'] },
        { ...fx.tsmScenarios[3], mustBeTrue: ['Per H1'], sourceIds: ['H1'] },
      ],
    })
    show(list, { ...targets(false), sources: new Map(fx.tsmSources.filter((s) => s.id !== 'S3').map((s) => [s.id, s])) })
    expect(screen.queryAllByRole('link').map((a) => a.closest('ul')!.getAttribute('aria-label'))).toEqual(['AI capex digestion inputs', 'AI supercycle inputs'])
    expect(screen.getByText('As F9 and S3 and F2 say')).toBeInTheDocument()
    expect(screen.getByText('Per H1')).toBeInTheDocument()
  })

  it('shows each input with its basis: a verified source linked, model knowledge dashed, an assumption outlined', () => {
    show()
    const chips = within(card('AI supercycle')).getByRole('list', { name: 'AI supercycle inputs' })
    const items = within(chips).getAllByRole('listitem')
    expect(items.map((li) => li.textContent)).toEqual([
      'FY2027 EPS $21.33 · F3 (opens in a new tab)',
      'EPS growth 2028–2032 20%/yr · assumption',
      'Exit P/E 25× · model knowledge 2025-06',
    ])
    expect(within(items[0]).getByRole('link', { name: /^F3/ })).toHaveAttribute('href', 'https://estimates.example/tsm')
    expect(items[0].className).toMatch(/bg-accent-soft/)
    expect(items[1].className).toMatch(/border-border/)
    expect(items[2].className).toMatch(/border-dashed/)
    expect(within(card('Taiwan blockade or conflict')).getByRole('list', { name: /inputs/ })).toHaveTextContent('Odds 5% over the horizon · assumptionMove -60% · assumption')
  })

  it('never shows an unverified source as sourced', () => {
    const list = scenariosOf({
      scenarios: [
        { ...fx.tsmScenarios[1], inputs: [{ field: 'pe', value: 29.3, basis: 'S2', kind: 'source', verified: false }] },
        fx.tsmScenarios[2],
      ],
    })
    show(list)
    const [chip] = within(within(card('AI capex digestion')).getByRole('list', { name: 'AI capex digestion inputs' })).getAllByRole('listitem')
    expect(chip).toHaveTextContent('Exit P/E 29.3× · assumption')
    expect(within(chip).queryByRole('link')).not.toBeInTheDocument()
  })

  it('writes the other inputs and renders nothing under two scenarios', () => {
    expect(inputText({ field: 'growthPct', value: 12, year: 2032, basis: 'assumption', kind: 'assumption', verified: false }, [], $)).toBe('EPS growth to FY2032 12%/yr')
    expect(inputText({ field: 'level', value: 752.4, basis: 'F1', kind: 'source', verified: true }, [], $)).toBe('Level $752')
    expect(inputText({ field: 'cagrPct', value: 9.2, basis: 'assumption', kind: 'assumption', verified: false }, [], $)).toBe('Return +9.2%/yr')
    expect(inputText({ field: 'eps', value: 16.5, basis: 'F3', kind: 'source', verified: true }, [], $)).toBe('EPS $16.50')
    expect(inputText({ field: 'ratio', value: 5, basis: 'H1', kind: 'source', verified: true }, [], $)).toBe('ratio 5')
    const { container } = show(scenariosOf({ scenarios: fx.tsmScenarios.slice(0, 1) }))
    expect(container).toBeEmptyDOMElement()
  })
})

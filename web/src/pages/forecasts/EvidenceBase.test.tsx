import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import type { ForecastFinding, ForecastResult } from '../../lib/types'
import * as fx from '../../test/fixtures'
import { EvidenceBase, WhatToWatch } from './EvidenceBase'

const ordered = { shape: 'ordered' as const, outcomes: fx.deepBuckets }
const r = fx.deepForecastResult

const group = (name: RegExp | string) => screen.getByRole('button', { name })
const panel = (name: RegExp | string) => document.getElementById(group(name).getAttribute('aria-controls')!)!
const shown = (name: RegExp | string) => [...panel(name).querySelectorAll('li[data-finding]')].filter((li) => !li.classList.contains('hidden'))

const many = (n: number, direction: string, strength: ForecastFinding['strength'] = 'slight'): ForecastFinding[] =>
  Array.from({ length: n }, (_, i) => ({ id: `F${i + 1}`, claim: `Finding number ${i + 1} on ${direction}`, sourceIds: ['S1'], asOf: '2026-10-01', angle: 'A1', direction, strength }))

describe('EvidenceBase · the research ledger', () => {
  it('heads the section with the count and groups an ordered ledger by direction', () => {
    render(<EvidenceBase f={ordered} r={r} />)
    expect(screen.getByText('Evidence base · 14')).toBeInTheDocument()
    expect(screen.getByRole('heading', { level: 2, name: 'What the research found' })).toBeInTheDocument()
    expect(screen.getByText(/^Atomic findings from 10 sources, grouped by the direction they point\. Strongest first\.$/)).toBeInTheDocument()
    expect(screen.getAllByRole('button', { expanded: true }).map((b) => b.textContent)).toEqual(['▲Upward4', '▼Downward4', 'Center3', 'Tail1'])
    expect(group(/Context/), 'more than 12 findings: Context starts collapsed').toHaveAttribute('aria-expanded', 'false')
    expect(screen.getByRole('img', { name: 'Findings by direction: Upward 4, Downward 4, Center 3, Tail 1, Context 2' })).toBeInTheDocument()
  })

  it('shows each finding with its id, strength, claim, angle, date and source links', () => {
    render(<EvidenceBase f={ordered} r={r} />)
    const f1 = document.querySelector('li[data-finding="F1"]') as HTMLElement
    expect(f1).toHaveTextContent('F1')
    expect(within(f1).getByText('slight')).toHaveAttribute('title', 'slight strength')
    expect(f1).toHaveTextContent('On 22 May 2026, JustCo opened at S$0.835')
    expect(f1).toHaveTextContent('Comparables· as of May 22, 2026')
    expect(within(f1).getByRole('link', { name: /JustCo slips below offer price/ })).toHaveAttribute('href', fx.deepSources[2].url)
    expect(within(document.querySelector('li[data-finding="F5"]') as HTMLElement).getAllByRole('link')).toHaveLength(2)
    expect(within(document.querySelector('li[data-finding="F5"]') as HTMLElement).getByText('moderate')).toHaveClass('text-text')
    // F12 also cites S99, which is not in the pack: only S10 is linked.
    const f12 = document.querySelector('li[data-finding="F12"]') as HTMLElement
    expect(within(f12).getAllByRole('link')).toHaveLength(1)
    expect(f12).not.toHaveTextContent('S99')
    // F14 has no date; F8 is dated to a month.
    expect(document.querySelector('li[data-finding="F14"]')).not.toHaveTextContent('as of')
    expect(document.querySelector('li[data-finding="F8"]')).toHaveTextContent('as of Jun 2026')
  })

  it('orders a group strongest first and sends unknown directions to Context', () => {
    const findings = [...many(2, 'higher', 'minimal'), { ...many(1, 'higher', 'strong')[0], id: 'F9' }, { ...many(1, 'sideways')[0], id: 'F7' }]
    render(<EvidenceBase f={ordered} r={{ ...r, findings }} />)
    expect([...panel(/Upward/).querySelectorAll('li[data-finding]')].map((li) => li.getAttribute('data-finding'))).toEqual(['F9', 'F1', 'F2'])
    expect(within(panel(/Upward/)).getByText('strong')).toHaveClass('text-accent-ink')
    expect(within(panel(/Upward/)).getAllByText('minimal')[0]).toHaveClass('border')
    expect(panel(/Context/)).toHaveTextContent('Finding number 1 on sideways')
  })

  it('labels binary and categorical groups', () => {
    const { unmount } = render(
      <EvidenceBase f={{ shape: 'binary', outcomes: ['Yes', 'No'] }} r={{ ...r, findings: [...many(1, 'yes'), { ...many(1, 'no')[0], id: 'F2' }] }} />,
    )
    expect(group(/Points to Yes/)).toHaveTextContent('▲')
    expect(group(/Points to No/)).toHaveTextContent('▼')
    unmount()
    render(<EvidenceBase f={{ shape: 'categorical', outcomes: ['Nvidia', 'Apple'] }} r={{ ...r, findings: [...many(1, 'Apple'), { ...many(1, 'nvidia')[0], id: 'F2' }] }} />)
    expect(screen.getAllByRole('button', { expanded: true }).map((b) => b.textContent)).toEqual(['Favours Nvidia1', 'Favours Apple1'])
  })

  it('gives each group its own panel even when outcomes differ only in punctuation or script', () => {
    const outcomes = ['中国', '日本', 'Above $100', 'Above 100']
    const findings = outcomes.map((o, i) => ({ ...many(1, o)[0], id: `F${i + 1}` }))
    render(<EvidenceBase f={{ shape: 'categorical', outcomes }} r={{ ...r, findings }} />)
    const buttons = screen.getAllByRole('button', { expanded: true })
    expect(buttons.map((b) => b.textContent)).toEqual(outcomes.map((o) => `Favours ${o}1`))
    expect(new Set(buttons.map((b) => b.getAttribute('aria-controls'))).size).toBe(4)
    buttons.forEach((b, i) => expect(document.getElementById(b.getAttribute('aria-controls')!)).toHaveTextContent(`Finding number 1 on ${outcomes[i]}`))
  })

  it('collapses Context on a long ledger, toggles groups and shows the rest of a long group', async () => {
    const user = userEvent.setup()
    const findings = [...many(8, 'lower'), ...many(6, 'context')].map((f, i) => ({ ...f, id: `F${i + 1}` }))
    render(<EvidenceBase f={ordered} r={{ ...r, findings }} />)
    expect(group(/Context/)).toHaveAttribute('aria-expanded', 'false')
    expect(panel(/Context/)).toHaveClass('hidden', 'print:block') // still printed
    await user.click(group(/Context/))
    expect(group(/Context/)).toHaveAttribute('aria-expanded', 'true')
    expect(panel(/Context/)).not.toHaveClass('hidden')

    expect(shown(/Downward/)).toHaveLength(6)
    await user.click(screen.getByRole('button', { name: 'Show all 8' }))
    expect(shown(/Downward/)).toHaveLength(8)
    await user.click(screen.getByRole('button', { name: 'Show fewer' }))
    expect(shown(/Downward/)).toHaveLength(6)

    await user.click(group(/Downward/))
    expect(group(/Downward/)).toHaveAttribute('aria-expanded', 'false')
  })

  it('keeps Context open on a short ledger', () => {
    render(<EvidenceBase f={ordered} r={{ ...r, findings: many(3, 'context') }} />)
    expect(group(/Context/)).toHaveAttribute('aria-expanded', 'true')
    expect(screen.queryByRole('button', { name: /Show all/ })).toBeNull()
  })

  it('renders nothing without findings, for older results and when no finding cites a known source', () => {
    const { container, rerender } = render(<EvidenceBase f={ordered} r={{ ...r, findings: [] }} />)
    expect(container).toBeEmptyDOMElement()
    const old: ForecastResult = { ...fx.forecastResult }
    rerender(<EvidenceBase f={ordered} r={old} />)
    expect(container).toBeEmptyDOMElement()
    rerender(<EvidenceBase f={ordered} r={{ ...r, findings: [{ ...many(1, 'higher')[0], sourceIds: ['S99'] }] }} />)
    expect(container).toBeEmptyDOMElement()
  })

  it('says when the ledger was carried from the previous run', () => {
    render(<EvidenceBase f={ordered} r={{ ...r, sources: r.sources.slice(0, 1), findings: many(1, 'higher'), research: { ...r.research, status: 'carried' } }} />)
    expect(screen.getByText(/^Atomic findings from 1 source, .* Strongest first\. From the previous run; may be stale\.$/)).toBeInTheDocument()
  })

  it('omits the angle when the plan does not name it and the link when a source has no web address', () => {
    const sources = [{ ...r.sources[0], url: 'javascript:alert(1)' }, r.sources[1]]
    render(<EvidenceBase f={ordered} r={{ ...r, sources, research: { queries: [] }, findings: [{ ...many(1, 'higher')[0], sourceIds: ['S1', 'S2'], angle: 'A9' }] }} />)
    const item = document.querySelector('li[data-finding="F1"]') as HTMLElement
    expect(within(item).getAllByRole('link')).toHaveLength(1)
    expect(item).toHaveTextContent(/^F1slightFinding number 1 on higheras of Oct 1, 2026S2/)
  })
})

describe('WhatToWatch · pending signals', () => {
  it('numbers each signal with its branches and their direction', () => {
    render(<WhatToWatch f={ordered} r={r} />)
    expect(screen.getByText('What to watch')).toBeInTheDocument()
    const items = screen.getAllByRole('listitem').filter((li) => li.parentElement?.tagName === 'OL')
    expect(items).toHaveLength(2)
    expect(items[0]).toHaveTextContent(/^01Final offer price and prospectus terms/)
    expect(items[1]).toHaveTextContent(/^02Public tranche subscription rate/)
    const branch = within(items[0]).getAllByRole('listitem')[0]
    expect(branch).toHaveTextContent('If The final price is set below S$1.80 →, then The buckets shift down with the reference price ▼Downward')
    expect(within(branch).getByText('Downward').closest('span')).toHaveClass('text-down')
    expect(within(items[0]).getAllByRole('listitem')[1]).toHaveTextContent(/Center$/)
    expect(within(items[1]).getByText('Upward').closest('span')).toHaveClass('text-up')
  })

  it('maps branch directions per shape and hides when there is nothing to watch', () => {
    const watch = [{ signal: 'Vote count', detail: '', branches: [{ if: 'A wins', then: 'A gains', direction: 'nonsense' }] }, { signal: ' ', detail: 'x', branches: [] }]
    const { container, rerender } = render(<WhatToWatch f={{ shape: 'binary', outcomes: ['Yes', 'No'] }} r={{ ...r, watch }} />)
    expect(screen.getAllByText(/Vote count/)).toHaveLength(1)
    expect(screen.getByText('Context')).toBeInTheDocument()
    expect(screen.queryByText('01Vote count')).toBeNull()
    rerender(<WhatToWatch f={ordered} r={{ ...r, watch: undefined }} />)
    expect(container).toBeEmptyDOMElement()
    rerender(<WhatToWatch f={ordered} r={{ ...r, watch: [] }} />)
    expect(container).toBeEmptyDOMElement()
  })
})

describe('EvidenceBase · the user\'s materials (round 9)', () => {
  const f = { ...ordered, materials: fx.materials }
  it('marks material findings as yours, links them to their row, and counts them apart from web sources', () => {
    render(<EvidenceBase f={f} r={fx.materialsForecastResult} />)
    expect(screen.getByText('Evidence base · 18')).toBeInTheDocument()
    expect(screen.getByText(/^Atomic findings from 10 sources and 2 of your materials, grouped/)).toBeInTheDocument()
    const f15 = document.querySelector('li[data-finding="F15"]') as HTMLElement
    expect(within(f15).getByText('Your material')).toHaveAttribute('title', 'From a file you attached: unverified')
    expect(f15).toHaveTextContent('p. 4· as of Sep 30, 2026')
    expect(within(f15).getByRole('link', { name: 'D1 · Q3 broker note (final).pdf' })).toHaveAttribute('href', '#material-D1')
    expect(within(f15).queryByRole('link', { name: /S\d/ })).toBeNull()
    const f1 = document.querySelector('li[data-finding="F1"]') as HTMLElement
    expect(within(f1).queryByText('Your material')).toBeNull()
  })

  it('counts only materials when the ledger has nothing else, and drops findings of unread materials', () => {
    const only = { ...fx.materialsForecastResult, findings: fx.materialsForecastResult.findings!.filter((x) => x.origin) }
    render(<EvidenceBase f={{ ...f, materials: fx.materials.slice(0, 1) }} r={only} />)
    expect(screen.getByText('Evidence base · 3')).toBeInTheDocument() // F18's D2 is not in the list
    expect(screen.getByText(/^Atomic findings from 1 of your materials, grouped/)).toBeInTheDocument()
  })
})

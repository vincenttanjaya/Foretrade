import { describe, expect, it } from 'vitest'
import * as fx from '../../test/fixtures'
import { SYMBOLS } from '../strategies/strategy'
import {
  anchored,
  angleKindLabel,
  bucketBoundsTs,
  cdfOf,
  fmtAnnual,
  fmtChance,
  fmtChange,
  fmtLevel,
  fmtToday,
  fmtLinearChange,
  fmtQ,
  measuredOf,
  medianOn,
  methodOf,
  methodsOf,
  NO_BASIS_LABEL,
  quantileOf,
  rangeHints,
  rangeText,
  scenariosOf,
  statsOf,
  summaryViewOf,
  unitOf,
  viewOf,
  vsRefHint,
  categoryLabel,
  cleanDraft,
  daysUntil,
  deepResearchLine,
  directionGroupOf,
  directionGroups,
  draftProblem,
  dueLabel,
  evidenceLink,
  findingsOf,
  fmtAsOf,
  fmtDay,
  fmtElapsed,
  fmtEv,
  fmtPct,
  fmtPts,
  fmtThreshold,
  groupFindings,
  httpUrl,
  inOrder,
  isThin,
  isUpdatable,
  materialHref,
  materialLine,
  materialsOf,
  materialsQuotaMessage,
  runCost,
  likelyRange,
  mentionedAsset,
  minResolutionDate,
  normalizeDraft,
  qualityChecks,
  pct0,
  reportFlags,
  researchLabel,
  researchOf,
  reshapeOutcomes,
  roundPcts,
  ruleText,
  shapeLabel,
  specialistSpread,
  specialistsHeader,
  stepText,
  topIndex,
  topOutcome,
  yesP,
} from './forecast'

describe('dates', () => {
  // Local noon on Oct 2 2026 → tomorrow is Oct 3 in any practical time zone.
  const noon = new Date(2026, 9, 2, 12)

  it('earliest resolution date is tomorrow', () => {
    expect(minResolutionDate(noon)).toBe('2026-10-03')
  })

  it('counts calendar days both ways', () => {
    expect(daysUntil('2026-10-12', noon)).toBe(10)
    expect(daysUntil('2026-09-30', noon)).toBe(-2)
    expect(daysUntil('not a date', noon)).toBeNull()
  })

  it('labels due dates and elapsed time', () => {
    expect([dueLabel(0), dueLabel(1), dueLabel(9), dueLabel(-1), dueLabel(-3), dueLabel(null)]).toEqual([
      'today',
      'in 1 day',
      'in 9 days',
      '1 day ago',
      '3 days ago',
      '',
    ])
    expect([fmtElapsed(42), fmtElapsed(185), fmtElapsed(3729)]).toEqual(['0:42', '3:05', '1:02:09'])
  })
})

describe('labels', () => {
  it('maps shapes and categories, with fallbacks', () => {
    expect([shapeLabel('ordered'), shapeLabel('mystery')]).toEqual(['Ordered', 'mystery'])
    expect([categoryLabel('macro'), categoryLabel('nope')]).toEqual(['Macro', 'Other'])
  })
})

describe('drafts', () => {
  it('shape switching keeps work: binary is locked, list shapes keep or restore their outcomes', () => {
    expect(reshapeOutcomes('ordered', 'binary', ['a', 'b'])).toEqual(['Yes', 'No'])
    expect(reshapeOutcomes('binary', 'ordered', ['Yes', 'No']), 'binary to list starts blank').toEqual(['', '', ''])
    expect(reshapeOutcomes('ordered', 'categorical', ['a', 'b']), 'list to list keeps outcomes').toEqual(['a', 'b'])
    expect(reshapeOutcomes('binary', 'categorical', ['Yes', 'No'], ['x', 'y']), 'restores saved outcomes').toEqual(['x', 'y'])
  })

  it('normalizeDraft tolerates junk from the model', () => {
    const junk = normalizeDraft({ shape: 'weird' as never, outcomes: ['A'], category: 'nope' as never, resolutionDate: '2027-01-01T00:00:00Z' })
    expect([junk.shape, junk.outcomes, junk.category, junk.resolutionDate]).toEqual(['binary', ['Yes', 'No'], 'other', '2027-01-01'])
    expect(normalizeDraft({ shape: 'categorical', outcomes: ['A'] }).outcomes, 'pads lists to two').toEqual(['A', ''])
    expect(normalizeDraft()).toEqual({
      shape: 'binary',
      question: '',
      outcomes: ['Yes', 'No'],
      resolutionCriteria: '',
      resolutionDate: '',
      category: 'other',
      priceRule: null,
      context: '',
    })
    expect(normalizeDraft({ shape: 'ordered', outcomes: Array.from({ length: 10 }, (_, i) => `o${i}`) }).outcomes).toHaveLength(8)
  })

  it('cleanDraft trims the text fields only', () => {
    const d = normalizeDraft({ shape: 'categorical', question: '  Who wins?  ', outcomes: [' A ', 'B '], resolutionCriteria: ' AP call ', context: ' notes ' })
    expect(cleanDraft(d)).toEqual({ ...d, question: 'Who wins?', outcomes: ['A', 'B'], resolutionCriteria: 'AP call', context: 'notes' })
    expect(normalizeDraft({ context: 'x'.repeat(4100) }).context, 'caps context').toHaveLength(4000)
  })

  it('formats the desk-vs-market edge and EV, and the specialist spread', () => {
    expect([fmtPts(5), fmtPts(-4.9), fmtPts(0)]).toEqual(['+5 pts', '−4.9 pts', '±0 pts'])
    expect([fmtEv(0.05), fmtEv(-0.1), fmtEv(0)]).toEqual(['+$0.05', '−$0.10', '±$0.00'])
    const spec = (yes: number) => ({ ...fx.forecastResult.specialists[0], probabilities: [{ outcome: 'Yes', p: yes }, { outcome: 'No', p: 1 - yes }] })
    expect(specialistSpread([spec(0.48), spec(0.5), spec(0.51)], 'Yes')).toEqual({ pts: 3, level: 'low' })
    expect(specialistSpread([spec(0.4), spec(0.55)], 'Yes')).toEqual({ pts: 15, level: 'moderate' })
    expect(specialistSpread([spec(0.2), spec(0.6)], 'Yes')).toEqual({ pts: 40, level: 'high' })
    expect(specialistSpread([spec(0.2)], 'Yes'), 'one specialist has no spread').toBeNull()
  })
})

describe('validation mirrors the contract', () => {
  const good = {
    shape: 'ordered' as const,
    question: 'Where will BTC close on Dec 31 2026?',
    outcomes: ['Below $80k', '$80k – $120k', 'Above $120k'],
    resolutionCriteria: 'Binance BTCUSDT daily close on Dec 31 2026.',
    resolutionDate: '2026-12-31',
    category: 'crypto' as const,
  }
  const min = '2026-10-03'

  it.each([
    [{}, null],
    [{ question: 'BTC?' }, 'Write a question of at least 10 characters.'],
    [{ question: 'x'.repeat(281) }, 'Keep the question under 280 characters.'],
    [{ outcomes: ['A', ' a '] }, 'Each outcome must be different.'],
    [{ outcomes: ['A', ''] }, 'Fill in every outcome, or remove the empty ones.'],
    [{ outcomes: ['A'] }, 'Add at least two outcomes.'],
    [{ outcomes: Array.from({ length: 9 }, (_, i) => `o${i}`) }, 'Use at most 8 outcomes.'],
    [{ shape: 'binary', outcomes: ['No', 'Yes'] }, 'Binary questions resolve Yes or No.'],
    [{ resolutionDate: '' }, 'Pick a resolution date.'],
    [{ resolutionDate: '2026-10-02' }, 'The resolution date must be in the future.'],
  ])('draftProblem(%j) → %s', (patch, problem) => {
    expect(draftProblem({ ...good, ...patch } as typeof good, min)).toBe(problem)
  })

  it('quality checklist', () => {
    expect(qualityChecks(good, min).map((c) => c.ok)).toEqual([true, true, true, true])
    expect(
      qualityChecks({ ...good, question: 'Will it rain?', outcomes: ['Wet', 'Dry'], resolutionCriteria: '', resolutionDate: '' }, min).map((c) => c.ok),
    ).toEqual([false, false, false, true])
  })
})

describe('results', () => {
  // Align by outcome name (case-insensitive), pick the top, grow the likely range.
  const probs = [
    { outcome: 'no', p: 0.38 },
    { outcome: 'YES', p: 0.62 },
  ]

  it('lines probabilities up with the outcomes', () => {
    expect(inOrder(probs, ['Yes', 'No'])).toEqual([
      { outcome: 'Yes', p: 0.62 },
      { outcome: 'No', p: 0.38 },
    ])
    expect(inOrder([{ outcome: 'Other', p: 1 }], ['A']), 'extras appended').toEqual([
      { outcome: 'A', p: 0 },
      { outcome: 'Other', p: 1 },
    ])
    expect(inOrder(null, ['A'])).toEqual([{ outcome: 'A', p: 0 }])
    expect(inOrder([{ outcome: 'A', p: NaN }, null as never], ['A'])).toEqual([{ outcome: 'A', p: 0 }])
  })

  it('reads yes, the top outcome and the likely range', () => {
    expect(yesP(inOrder(probs, ['Yes', 'No']))).toBe(0.62)
    expect(yesP([{ outcome: 'Up', p: 0.3 }]), 'falls back to the first outcome').toBe(0.3)
    expect(yesP([])).toBeNull()
    expect(topIndex([])).toBe(-1)
    const buckets = [0.05, 0.15, 0.3, 0.25, 0.2, 0.05].map((p, i) => ({ outcome: `b${i}`, p }))
    expect(topIndex(buckets)).toBe(2)
    const range = likelyRange(buckets)!
    expect([range.from, range.to, Math.round(range.p * 100)], 'grows toward the likelier side').toEqual([2, 3, 55])
    expect(likelyRange([{ outcome: 'a', p: 0.7 }, { outcome: 'b', p: 0.3 }]), 'single bucket range').toEqual({ from: 0, to: 0, p: 0.7 })
    expect(likelyRange([])).toBeNull()
  })
})

describe('research', () => {
  // http(s) only, missing fields (pre-research reports) are empty, evidence links borrow the source title.
  const src = (id: string, url: string, read = false) => ({ id, title: `T${id}`, url, source: 'Reuters', publishedAt: null, read })
  const sources = [src('S1', 'https://a.io/1', true), src('S2', 'https://b.io/2')]

  it('links plain http(s) URLs only', () => {
    expect(['https://a.io/x', 'javascript:alert(1)', 'ftp://a.io', 'https://', undefined].map((u) => httpUrl(u)?.href ?? null)).toEqual([
      'https://a.io/x',
      null,
      null,
      null,
      null,
    ])
  })

  it('reads the research pack, or nothing from older reports', () => {
    expect(researchOf({ research: { queries: ['btc etf', ' ', 7 as never] }, sources })).toEqual({ queries: ['btc etf'], sources, read: 1 })
    expect(researchOf({} as never)).toEqual({ queries: [], sources: [], read: 0 })
  })

  it('labels evidence links with the matching source', () => {
    expect(evidenceLink({ url: 'https://b.io/2', source: 'S2' }, sources)).toEqual({ href: 'https://b.io/2', id: 'S2', label: 'TS2', internal: false })
    expect(evidenceLink({ url: 'https://www.c.io/x', source: '' }, sources), 'unmatched url falls back to hostname').toEqual({
      href: 'https://www.c.io/x',
      id: null,
      label: 'c.io',
      internal: false,
    })
    expect([
      evidenceLink({ url: null, source: 'x' }, sources),
      evidenceLink({ source: 'x' }, sources),
      evidenceLink({ url: 'javascript:x', source: 'x' }, sources),
    ]).toEqual([null, null, null])
  })
})

describe('track record helpers', () => {
  const rule = { symbol: 'SOL', op: 'above', threshold: 130, mode: 'on_date' } as const

  it('keeps a well-formed price rule on binary drafts only', () => {
    expect(normalizeDraft({ priceRule: rule }).priceRule).toEqual(rule)
    expect(normalizeDraft({ shape: 'categorical', outcomes: ['A', 'B'], priceRule: rule }).priceRule).toBeNull()
    expect(normalizeDraft({ priceRule: { ...rule, threshold: 0 } }).priceRule).toBeNull()
    expect(normalizeDraft({ priceRule: { ...rule, op: 'crosses' as never } }).priceRule).toBeNull()
  })

  it('words rules, thresholds and days like the questions do', () => {
    expect([fmtThreshold(130), fmtThreshold(61100), fmtThreshold(0.215)]).toEqual(['$130', '$61,100', '$0.215'])
    expect(ruleText(rule, '2026-12-31')).toBe('SOL/USDT daily close above $130 on Dec 31, 2026')
    expect(ruleText({ ...rule, op: 'below', mode: 'by_date' }, '2026-12-31')).toBe('SOL/USDT daily close below $130 by Dec 31, 2026')
    expect([fmtDay('2026-08-12T00:07:00Z'), fmtDay('2026-08-26T23:59:59.999Z', true)]).toEqual(['Aug 12', 'Aug 26, 2026'])
  })

  it("finds the desk's top outcome and the asset a question names", () => {
    expect(topOutcome({ shape: 'binary', headline: { outcome: 'Yes', p: 0.3 } })).toBe('No')
    expect(topOutcome({ shape: 'binary', headline: { outcome: 'Yes', p: 0.5 } })).toBe('Yes')
    expect(topOutcome({ shape: 'categorical', headline: { outcome: 'ECB', p: 0.4 } })).toBe('ECB')
    expect(topOutcome({ shape: 'binary', headline: null })).toBeNull()
    const assets = [
      { symbol: 'BTC', name: 'Bitcoin' },
      { symbol: 'SOL', name: 'Solana' },
      { symbol: 'PAXG', name: 'PAX Gold (gold proxy)' },
    ]
    expect(mentionedAsset('Will Solana flip ETH?', assets)).toBe('SOL')
    expect(mentionedAsset('Will ETH outperform BTC?', assets)).toBe('BTC')
    expect(mentionedAsset('Will gold hit $3,000?', assets)).toBe('PAXG')
    expect(mentionedAsset('Will the Fed cut?', assets)).toBeUndefined()
  })

  it("drops the server's guard note only when the guard reset the desk, keeping every other flag", () => {
    const guard = {
      type: 'low_information',
      note: "Server check: the desk moved toward 50% from the volatility-only reference without citing evidence, so the final probability is the reference's.",
    } as const
    const thin = { type: 'low_information', note: 'Skeptic unavailable for this run; the forecast combines 2 of 3 specialists.' } as const
    const stale = { type: 'stale', note: 'One source is a week old.' } as const
    expect(reportFlags({ flags: [stale, guard, thin], guard: 'shrink' })).toEqual([
      { type: 'stale', notes: [stale.note] },
      { type: 'low_information', notes: [thin.note] },
    ])
    expect(reportFlags({ flags: [guard], guard: 'shrink' })).toEqual([])
    expect(reportFlags({ flags: [{ ...guard, type: 'conflict' }], guard: 'shrink' })).toEqual([{ type: 'conflict', notes: [guard.note] }])
    expect(reportFlags({ flags: [guard], guard: null })).toEqual([{ type: 'low_information', notes: [guard.note] }])
    expect(reportFlags({ flags: undefined as never })).toEqual([])
  })
})

describe('expanded universe asset mentions', () => {
  const assets = SYMBOLS.map(([symbol, name]) => ({ symbol, name }))

  it('does not turn an unrelated Fed question into The Sandbox', () => {
    expect(mentionedAsset('Will the Fed cut rates at its December 2026 meeting?', assets)).toBeUndefined()
  })

  it.each(['SAND', 'The Sandbox', 'Sandbox'])('recognizes the legitimate %s mention', (name) => {
    expect(mentionedAsset(`Will ${name} recover?`, assets)).toBe('SAND')
  })

  it('keeps meaningful gold/PAXG mentions without treating a descriptive proxy word as an asset', () => {
    const described = assets.map((a) => a.symbol === 'PAXG' ? { ...a, name: 'PAX Gold (gold proxy)' } : a)
    expect(mentionedAsset('Will gold reach $3,000?', described)).toBe('PAXG')
    expect(mentionedAsset('Will PAXG reach $3,000?', described)).toBe('PAXG')
    expect(mentionedAsset('Will proxy server failures increase?', described)).toBeUndefined()
  })

  it('keeps known company and former-ticker aliases and full names without generic name-word matches', () => {
    expect(mentionedAsset('Will Google grow?', assets)).toBe('GOOGL')
    expect(mentionedAsset('Will MATIC recover?', assets)).toBe('POL')
    expect(mentionedAsset('Will Official Trump recover?', assets)).toBe('TRUMP')
    expect(mentionedAsset('Will official network access change?', assets)).toBeUndefined()
    expect(mentionedAsset('Will the group meet next month?', assets)).toBeUndefined()
  })

  it('preserves universe-order ties and whole-word boundaries', () => {
    expect(mentionedAsset('Will ETH outperform BTC?', assets)).toBe('BTC')
    expect(mentionedAsset('Will ETH outperform BTC?', [...assets].reverse())).toBe('ETH')
    expect(mentionedAsset('Will sandboxed sandwich sales rise?', assets)).toBeUndefined()
  })
})

describe('rounding and market depth', () => {
  it('rounds a distribution to whole percents that sum to 100', () => {
    expect(roundPcts([0.235, 0.765])).toEqual([24, 76])
    expect(roundPcts([1 / 3, 1 / 3, 1 / 3])).toEqual([34, 33, 33])
    expect(roundPcts([0.125, 0.125, 0.25, 0.5])).toEqual([13, 12, 25, 50])
    expect(roundPcts([0.004, 0.996])).toEqual([0, 100])
    expect(roundPcts([0.2, 0.3])).toEqual([40, 60]) // scaled when it does not sum to 1
    expect(roundPcts([0, Number.NaN])).toEqual([0, 0])
    expect(roundPcts([])).toEqual([])
    for (const ps of [[0.62, 0.38], [0.155, 0.155, 0.69], [0.01, 0.02, 0.03, 0.94], [1 / 7, 2 / 7, 4 / 7]]) {
      expect(roundPcts(ps).reduce((a, b) => a + b, 0)).toBe(100)
    }
    expect(pct0(0.235)).toBe(24)
    expect([fmtPct(0, 0.004), fmtPct(100, 0.996), fmtPct(0, 0), fmtPct(34, 0.34)]).toEqual(['<1%', '>99%', '0%', '34%'])
  })

  // The shared table (the API's chanceWords test has the same rows): no chance prints as 0% or 100%. Ties stay out.
  it.each([
    [0.001, '<1%'],
    [0.0049, '<1%'],
    [0.006, '1%'],
    [0.62, '62%'],
    [0.994, '99%'],
    [0.996, '>99%'],
    [0.9975, '>99%'],
  ])('states the chance %s as %s', (p, text) => {
    expect(fmtChance(p)).toBe(text)
  })

  it('keeps the ≈ of an extrapolated chance, but not on a bound', () => {
    expect([fmtChance(0.62, true), fmtChance(0.001, true), fmtChance(0.9975, true)]).toEqual(['≈62%', '<1%', '>99%'])
  })

  it('heads the specialist panel by count, never claiming independence', () => {
    expect([specialistsHeader(3, null), specialistsHeader(3, { agreement: 'diverse' }), specialistsHeader(3, { agreement: 'independent_agree' })]).toEqual([
      'Specialists · 3',
      'Specialists · 3',
      'Specialists · 3',
    ])
    expect(specialistsHeader(3, { agreement: 'duplicate' })).toBe('3 specialists · counted as 1 view')
  })

  it('reads a thin market from the flag, or from the volume on older results', () => {
    expect(isThin({ thin: true, volumeUsd: 5e6 })).toBe(true)
    expect(isThin({ thin: false, volumeUsd: 10 })).toBe(false)
    expect(isThin({ volumeUsd: 65.7 })).toBe(true)
    expect(isThin({ volumeUsd: null })).toBe(true)
    expect(isThin({ volumeUsd: 1000 })).toBe(false)
  })
})

describe('deep research', () => {
  const r = fx.deepForecastResult

  it('reads the ledger defensively: unknown source ids, malformed items and older results', () => {
    const findings = findingsOf(r)
    expect(findings).toHaveLength(14)
    expect(findings.find((f) => f.id === 'F12')!.sourceIds, 'S99 is not in the pack').toEqual(['S10'])
    expect(findings.find((f) => f.id === 'F14')!.asOf).toBeNull()
    const junk = [
      null,
      { id: 'F1', claim: ' ', sourceIds: ['S1'] },
      { id: 'F2', claim: 'Only unknown sources', sourceIds: ['S99'] },
      { id: 'F3', claim: 'No sources at all' },
      { id: 'F4', claim: '  Kept, repaired  ', sourceIds: ['S1', 'S1', 7], asOf: '', angle: 3, direction: 5, strength: 'huge' },
    ] as never
    expect(findingsOf({ sources: r.sources, findings: junk })).toEqual([
      { id: 'F4', claim: 'Kept, repaired', sourceIds: ['S1'], asOf: null, angle: '', direction: 'context', strength: 'slight' },
    ])
    expect(findingsOf({ sources: r.sources })).toEqual([])
    expect(findingsOf({} as never)).toEqual([])
  })

  it('names the direction groups per shape, Context last', () => {
    expect(directionGroups('binary', ['Yes', 'No']).map((g) => [g.key, g.label, g.tone])).toEqual([
      ['yes', 'Points to Yes', 'up'],
      ['no', 'Points to No', 'down'],
      ['context', 'Context', 'neutral'],
    ])
    expect(directionGroups('ordered', fx.deepBuckets).map((g) => g.label)).toEqual(['Upward', 'Downward', 'Center', 'Tail', 'Context'])
    expect(directionGroups('categorical', ['Nvidia', 'Apple', 'nvidia', ' ']).map((g) => g.label)).toEqual(['Favours Nvidia', 'Favours Apple', 'Context'])
    expect(directionGroupOf('NVIDIA', 'categorical', ['Nvidia', 'Apple']).label).toBe('Favours Nvidia')
    expect(directionGroupOf('sideways', 'ordered', fx.deepBuckets).label).toBe('Context')
  })

  it('groups findings by direction, strongest first, empty groups left out', () => {
    const groups = groupFindings(findingsOf(r), 'ordered', fx.deepBuckets)
    expect(groups.map((g) => [g.label, g.items.length])).toEqual([
      ['Upward', 4],
      ['Downward', 4],
      ['Center', 3],
      ['Tail', 1],
      ['Context', 2],
    ])
    expect(groups[2].items.map((f) => f.id), 'moderate before minimal, ties in ledger order').toEqual(['F5', 'F6', 'F4'])
    expect(groups[0].items.map((f) => f.id)).toEqual(['F2', 'F3', 'F9', 'F7'])
    const odd = findingsOf({ sources: r.sources, findings: [{ ...r.findings![0], direction: 'up' }] })
    expect(groupFindings(odd, 'ordered', fx.deepBuckets).map((g) => g.label), 'unknown direction → Context').toEqual(['Context'])
    expect(groupFindings([], 'binary', ['Yes', 'No'])).toEqual([])
  })

  it('labels deep research for the header and the research card', () => {
    expect(researchLabel('live', 22, 12, 'deep')).toBe('deep · 22 sources · 12 read')
    expect(researchLabel('live', 8, 3, 'quick')).toBe('Brave Search · 8 sources · 3 read')
    expect(deepResearchLine(r)).toBe('Deep research · 2 rounds · 17 searches · 10 sources · 7 read')
    expect(deepResearchLine({ sources: r.sources.slice(0, 1).map((s) => ({ ...s, read: false })), research: { queries: [], rounds: 1, searches: 0 } })).toBe(
      'Deep research · 1 round · 1 source',
    )
    expect(deepResearchLine({ sources: [], research: { queries: [], searches: 1 } })).toBe('Deep research · 1 search')
    expect(angleKindLabel('sentiment')).toBe('Local market')
    expect(angleKindLabel('mystery')).toBe('Other')
  })

  it('formats as-of days and months', () => {
    expect(fmtAsOf('2026-10-05')).toBe('Oct 5, 2026')
    expect(fmtAsOf('2026-06')).toBe('Jun 2026')
    expect(fmtAsOf('late 2026')).toBe('late 2026')
  })

  it('writes each research step', () => {
    const st = (phase: string, status: string, count = 0, of = 0, round = 1) => stepText({ phase, status, count, of, round } as never)
    expect([
      st('plan', 'running'),
      st('plan', 'done', 5),
      st('search', 'running', 0, 0, 2),
      st('search', 'done', 58, 12),
      st('search', 'done', 1, 1, 2),
      st('screen', 'running'),
      st('screen', 'done', 10, 58),
      st('read', 'running'),
      st('read', 'done', 10, 14),
      st('extract', 'running'),
      st('extract', 'done', 1, 3),
      st('extract', 'done', 22, 3),
      st('gap', 'running'),
      st('gap', 'done', 6),
      st('gap', 'done', 1),
      st('gap', 'done', 0),
      st('screen', 'failed'),
      st('read', 'skipped'),
      st('mystery', 'done'),
      st('mystery', 'running'),
      st('mystery', 'failed'),
    ]).toEqual([
      'Planning the research…',
      'Planned 5 angles',
      'Searching · round 2…',
      'Round 1 · 12 searches · 58 results',
      'Round 2 · 1 search · 1 result',
      'Screening results…',
      '10 of 58 relevant',
      'Reading pages…',
      'Read 10 of 14 pages',
      'Extracting findings…',
      '1 new finding',
      '22 new findings',
      'Looking for gaps…',
      '6 follow-up queries',
      '1 follow-up query',
      'No gaps left',
      'Screening failed · kept what it had',
      'Reading skipped (time or search budget)',
      'Done',
      'Working…',
      'Step failed · kept what it had',
    ])
  })
})

describe('materials', () => {
  it('reads materials defensively: drops malformed items, clamps text, keeps digests only for read files, caps five', () => {
    const long = 'x'.repeat(700)
    const raw = [
      ...fx.materials,
      { id: 'D4', kind: 'document', mediaType: 'text/plain', name: 'n'.repeat(130), size: -3, status: 'read', summary: long, relevance: 'odd',
        asOf: 'soon', findings: [{ claim: ' ' }, { claim: 'c'.repeat(320), ref: 'r'.repeat(70), asOf: '2026-09', direction: 'higher', strength: 'huge' }], excerpt: 'e'.repeat(2100), readAt: 'never' },
      { id: 'D1', name: 'duplicate.pdf', status: 'read' },
      { id: 'S1', name: 'not a material' },
      null,
      { id: 'D5', name: 'chart.png', kind: 'image', status: 'failed', summary: 'leaked', findings: [{ claim: 'should not show' }] },
      { id: 'D6', name: 'sixth.pdf', status: 'pending' },
    ]
    const out = materialsOf({ materials: raw })
    expect(out.map((m) => m.id)).toEqual(['D1', 'D2', 'D3', 'D4', 'D5'])
    expect(out.slice(0, 3)).toEqual(fx.materials)
    const d4 = out[3]
    expect([d4.name.length, d4.size, d4.summary.length, d4.relevance, d4.asOf, d4.excerpt.length, d4.readAt]).toEqual([120, 0, 600, undefined, null, 2000, null])
    expect(d4.findings).toEqual([{ claim: 'c'.repeat(300), ref: 'r'.repeat(60), asOf: '2026-09', direction: 'higher', strength: 'slight' }])
    expect(out[4]).toMatchObject({ kind: 'image', status: 'failed', error: 'unreadable', summary: '', findings: [] })
    expect(materialsOf({})).toEqual([])
    expect(materialsOf({ materials: 'x' })).toEqual([])
  })

  it('describes a material and its anchor, and prices a run at 5 plus 1 per new file', () => {
    expect(fx.materials.map(materialLine)).toEqual(['PDF · 1.2 MB · 12 pages', 'CSV · 5 kB', 'Image · 880 kB'])
    expect(materialLine({ kind: 'document', mediaType: 'x/unknown', size: 0 })).toBe('File')
    expect(materialHref('D2')).toBe('#material-D2')
    expect([runCost(0), runCost(3), runCost(5)]).toEqual([5, 8, 10])
  })

  it('explains a quota refusal by the files only when the credits left cover the run without some of them', () => {
    const left = (n: number) => ({ used: 500 - n, limit: 500 })
    expect(materialsQuotaMessage(3, left(6))).toBe('This run needs 8 credits and you have 6 left today. Remove 2 files, or run it after the 00:00 UTC reset.')
    expect(materialsQuotaMessage(1, left(5))).toBe('This run needs 6 credits and you have 5 left today. Remove a file, or run it after the 00:00 UTC reset.')
    expect(materialsQuotaMessage(0, left(4))).toBeNull() // no files: the daily-limit line
    expect(materialsQuotaMessage(2, left(4))).toBeNull() // not even a text-only run fits
    expect(materialsQuotaMessage(2, left(7))).toBeNull() // enough as far as we know: the server's line
    expect(materialsQuotaMessage(2, null)).toBeNull()
    expect(materialsQuotaMessage(2, { used: 520, limit: 500 })).toBeNull()
  })

  it('keeps material findings only for materials the desk read, with their origin and ref', () => {
    const r = fx.materialsForecastResult
    const all = findingsOf(r, fx.materials)
    expect(all.map((f) => f.id)).toEqual([...fx.deepFindings.map((f) => f.id), 'F15', 'F16', 'F17', 'F18'])
    expect(all.find((f) => f.id === 'F15')).toMatchObject({ origin: 'material', ref: 'p. 4', sourceIds: ['D1'], angle: '' })
    expect(all.find((f) => f.id === 'F1')).not.toHaveProperty('origin')
    // without materials (or with D1 unread) the D ids resolve nowhere; a web finding never resolves a D id either
    expect(findingsOf(r).map((f) => f.id)).toEqual(fx.deepFindings.map((f) => f.id))
    const unread = fx.materials.map((m) => (m.id === 'D1' ? { ...m, status: 'failed' as const } : m))
    expect(findingsOf(r, unread).filter((f) => f.origin).map((f) => f.id)).toEqual(['F18'])
    const sneaky = { ...r, findings: [{ ...fx.deepFindings[0], id: 'F99', sourceIds: ['D1'] }] }
    expect(findingsOf(sneaky, fx.materials)).toEqual([])
  })

  it('links material evidence to its row on the page, never to a URL', () => {
    const e = fx.materialsForecastResult.evidence.at(-1)!
    expect(evidenceLink(e, fx.deepSources, fx.materials)).toEqual({ href: '#material-D1', id: 'D1', label: 'Q3 broker note (final).pdf', internal: true })
    expect(evidenceLink(e, fx.deepSources), 'unknown material: plain source text').toBeNull()
    expect(evidenceLink({ ...e, materialId: 'D9' }, fx.deepSources, fx.materials)).toBeNull()
  })

  it('applies the server update rule', () => {
    const today = '2026-10-07'
    const f = fx.materialsForecast({ resolutionDate: '2026-10-28' })
    expect(isUpdatable(f, today)).toBe(true)
    expect(isUpdatable({ ...f, resolutionDate: today }, today)).toBe(false)
    expect(isUpdatable({ ...f, status: 'running' }, today)).toBe(false)
    expect(isUpdatable({ ...f, history: [{ id: 'newer', outcome: 'Yes', p: 0.5, at: '2026-10-07T00:00:00Z' }] }, today)).toBe(false)
  })
})

describe('desk view', () => {
  const edges = [300, 450, 600, 800, 1100]
  const q = (ps: number[], x: number) => quantileOf(edges, ps, 'log', x)!
  const old = [0.17, 0.19, 0.17, 0.16, 0.14, 0.17] // the stored TSM report
  const final = [0.17, 0.17, 0.15, 0.16, 0.15, 0.2] // §6.1's re-run

  it('reads the stored TSM buckets as the API does (§4.8 fixture)', () => {
    expect(q(old, 0.1).value).toBeCloseTo(247.5, 0)
    expect(q(old, 0.1).approx).toBe(true)
    expect(q(old, 0.25)).toEqual({ value: expect.closeTo(355.8, 0) })
    expect(q(old, 0.5).value).toBeCloseTo(570.3, 0)
    expect(q(old, 0.75).value).toBeCloseTo(917.0, 0)
    expect(q(old, 0.9)).toEqual({ value: expect.closeTo(1350.5, 0), approx: true })
    expect(1 - cdfOf(edges, old, 'log', 485)!).toBeCloseTo(0.596, 3)
  })

  it('reads the re-run (§6.1): median 610.9, 80% ≈241.9–≈1,476, 62% above $485', () => {
    expect(q(final, 0.5).value).toBeCloseTo(610.9, 0)
    expect(q(final, 0.1).value).toBeCloseTo(241.9, 0)
    expect(q(final, 0.9).value).toBeCloseTo(1476.4, 0)
    expect(1 - cdfOf(edges, final, 'log', 485)!).toBeCloseTo(0.621, 3)
    // the scenario partition's odds: geometric means of 232/346/752/1,327
    const cuts = [Math.sqrt(232 * 346), Math.sqrt(346 * 752), Math.sqrt(752 * 1327)].map((x) => cdfOf(edges, final, 'log', x)!)
    expect([cuts[0], cuts[1] - cuts[0], cuts[2] - cuts[1], 1 - cuts[2]].map((p) => Math.round(p * 1000) / 1000)).toEqual([0.148, 0.258, 0.349, 0.245])
  })

  it('inverts its own CDF everywhere, monotone, with tails inside the clamp', () => {
    let seed = 7
    const rnd = () => ((seed = (seed * 16807) % 2147483647) / 2147483647)
    for (let n = 0; n < 300; n++) {
      const ps = Array.from({ length: 6 }, () => rnd() + 0.01)
      let last = -Infinity
      for (const x of [0.02, 0.1, 0.25, 0.5, 0.75, 0.9, 0.98]) {
        const v = quantileOf(edges, ps, 'log', x)!.value
        expect(v).toBeGreaterThan(last)
        expect(cdfOf(edges, ps, 'log', v)!).toBeCloseTo(x, 9)
        last = v
      }
    }
  })

  it('handles a linear scale, three ranges, an empty neighbour and bad input', () => {
    // CPI-like: −1%, 2%, 5% edges, uniform in x
    expect(quantileOf([-1, 2], [0.25, 0.5, 0.25], 'linear', 0.5)).toEqual({ value: 0.5 })
    expect(cdfOf([-1, 2], [0.25, 0.5, 0.25], 'linear', -1)).toBeCloseTo(0.25, 12)
    expect(quantileOf([-1, 2], [0.25, 0.5, 0.25], 'linear', 0.1)!.approx).toBe(true)
    // an empty middle range: still defined (the API floors every range at 1e-9)
    expect(Number.isFinite(quantileOf(edges, [0.5, 0, 0, 0, 0, 0.5], 'log', 0.3)!.value)).toBe(true)
    expect(quantileOf(edges, [0.5, 0.5], 'log', 0.5)).toBeNull()
    expect(quantileOf([300, 200], [0.3, 0.4, 0.3], 'log', 0.5)).toBeNull()
    expect(quantileOf([-1, 2], [0.3, 0.4, 0.3], 'log', 0.5)).toBeNull()
    expect(quantileOf(edges, old, 'log', 1)).toBeNull()
    expect(quantileOf(edges, [-0.1, 0.3, 0.2, 0.2, 0.2, 0.2], 'log', 0.5)).toBeNull()
    expect(cdfOf(edges, old, 'log', 0)).toBe(0)
  })

  it('takes a specialist anchor or the pool median on the view edges', () => {
    const view = viewOf(fx.tsmDeskResult)!
    expect(medianOn(view, fx.tsmDeskResult.specialists[1].anchor!)!.value).toBeCloseTo(676.5, 0)
    expect(medianOn({ ...view, edges: [] }, fx.tsmDeskResult.specialists[1].anchor!)).toBeNull()
  })

  it('parses ranges as the API bucketBounds does, scale words included', () => {
    expect(bucketBoundsTs(fx.tsmDraftBuckets)).toEqual([[-Infinity, 250], [250, 360], [360, 485], [485, 750], [750, 1000], [1000, 1500], [1500, Infinity]])
    expect(bucketBoundsTs(['< $800B', '$800B – $1.2T', '$1.2T – $2T', '≥ $2T'])).toEqual([[-Infinity, 8e11], [8e11, 1.2e12], [1.2e12, 2e12], [2e12, Infinity]])
    expect(bucketBoundsTs(['under $950bn', '$950bn to 1.5 trillion', 'over 1.5 trillion'])).toEqual([[-Infinity, 9.5e11], [9.5e11, 1.5e12], [1.5e12, Infinity]])
    expect(bucketBoundsTs(['Below 300', '300 to 450', '450+'])).toEqual([[-Infinity, 300], [300, 450], [450, Infinity]])
    expect(bucketBoundsTs(['< $80k', '$80k – $90k', '≥ $90k'])).toEqual([[-Infinity, 8e4], [8e4, 9e4], [9e4, Infinity]])
    expect(bucketBoundsTs(['< -1%', '-1% – 0%', '0% – 2.5%', '≥ 2.5%']), 'signed').toEqual([[-Infinity, -1], [-1, 0], [0, 2.5], [2.5, Infinity]])
    expect(bucketBoundsTs(['Below −0.5%', '−0.5% to 0.5%', 'Above 0.5%']), 'a minus sign').toEqual([[-Infinity, -0.5], [-0.5, 0.5], [0.5, Infinity]])
    expect(bucketBoundsTs(['< 2.5', '2.5-3', '≥ 3']), 'a range dash is no sign').toEqual([[-Infinity, 2.5], [2.5, 3], [3, Infinity]])
    expect(bucketBoundsTs(['< -$3', '-$3 - $3', '≥ $3'])).toEqual([[-Infinity, -3], [-3, 3], [3, Infinity]])
    expect(bucketBoundsTs(fx.deepBuckets), '"or higher" is not an open top').toBeNull()
    expect(bucketBoundsTs(['< $300', '$300 – $450'])).toBeNull()
    expect(bucketBoundsTs(['< $300', '$310 – $450', '≥ $450']), 'a gap').toBeNull()
    expect(bucketBoundsTs(['< $300', '$450 – $300', '≥ $450'])).toBeNull()
    expect(bucketBoundsTs(['Yes', 'No', 'Maybe'])).toBeNull()
  })

  it('formats levels as the API fmtLevel does', () => {
    const $ = { prefix: '$', suffix: '' }
    expect([611.2, 1476.4, 241.9, 485.12, 67412, 2.12e6, 1.2e12, 999_600, 0.5523].map((x) => fmtLevel(x, $))).toEqual([
      '$611', '$1,480', '$242', '$485', '$67,400', '$2.12M', '$1.20T', '$1.00M', '$0.552',
    ])
    expect(fmtLevel(1.8, { prefix: 'S$', suffix: '' }), 'a currency keeps its zeros').toBe('S$1.80')
    expect(fmtLevel(2.5, { prefix: '', suffix: '%' })).toBe('2.5%')
    expect(fmtLevel(2.0, { prefix: '', suffix: '%' })).toBe('2%')
    expect(fmtLevel(25, { prefix: '', suffix: 'bp' })).toBe('25bp')
    expect(fmtLevel(-0.25, { prefix: '', suffix: '%' })).toBe('-0.25%')
    expect([fmtLevel(0, $), fmtLevel(Number.NaN, $), fmtLevel(3.14159)]).toEqual(['$0', '—', '3.14'])
    expect(fmtQ({ value: 241.9, approx: true }, $)).toBe('≈$242')
  })

  it("writes today's level at its own precision, as the API's todayLevel does", () => {
    const u = (prefix: string, suffix = '') => ({ prefix, suffix })
    expect(
      [
        [472.2, u('$')], [83_048, u('$')], [73.85, u('S$')], [262_000, u('₩')], [1_047_300, u('₩')], [0.06152, u('$')],
        [485, u('$')], [999.996, u('$')], [9.5e6, u('IDR ')], [2e15, u('$')], [2.4, u('', '%')], [-3, u('', 'bp')],
      ].map(([x, unit]) => fmtToday(x as number, unit as { prefix: string; suffix: string })),
    ).toEqual(['$472.20', '$83,048', 'S$73.85', '₩262,000', '₩1,047,300', '$0.0615', '$485', '$1,000', 'IDR 9,500,000', '$2,000T', '2.4%', '-3bp'])
    expect(fmtToday(Number.NaN, u('$'))).toBe('—')
  })

  it('writes changes as the lead does', () => {
    expect([fmtChange(25.96), fmtChange(-28.62), fmtChange(4.72), fmtChange(-0.4), fmtChange(9.96)]).toEqual(['+26%', '-29%', '+4.7%', '-0.4%', '+10%'])
    expect([fmtAnnual(4.723), fmtAnnual(22.3), fmtAnnual(-13.71)]).toEqual(['+4.7%', '+22.3%', '-13.7%'])
    expect(fmtLinearChange(0.3, { prefix: '', suffix: '%' })).toBe('+0.3%')
    expect(fmtLinearChange(-25, { prefix: '', suffix: 'bp' })).toBe('-25bp')
    expect([vsRefHint(null, -38.14), vsRefHint(-38.14, -7.22), vsRefHint(126.8, null), vsRefHint(-0.02, 54.6), vsRefHint(null, null)]).toEqual([
      'below -38%', '-38%…-7%', 'above +127%', '0%…+55%', undefined,
    ])
    expect(rangeText(null, 283.3, { prefix: '$', suffix: '' })).toBe('below $283')
    expect(rangeText(510.1, 999, { prefix: '$', suffix: '' })).toBe('$510–$999')
    expect(rangeText(999, null, { prefix: '$', suffix: '' })).toBe('above $999')
  })

  it('finds the unit as the API unitOf does', () => {
    expect(unitOf(fx.tsmBuckets)).toEqual({ prefix: '$', suffix: '' })
    expect(unitOf(fx.deepBuckets)).toEqual({ prefix: 'S$', suffix: '' })
    expect(unitOf(['Below NT$600', 'NT$600 – NT$800', 'NT$800 or more'])).toEqual({ prefix: 'NT$', suffix: '' })
    expect(unitOf(['Under 2.5%', '2.5% – 3%', 'Above 3%'])).toEqual({ prefix: '', suffix: '%' })
    expect(unitOf(['Cut 50 bps or more', 'Cut 25 bp', 'Hold'])).toEqual({ prefix: '', suffix: 'bp' })
    expect(unitOf(['Low', 'High'])).toEqual({ prefix: '', suffix: '' })
  })

  it('reads a view defensively', () => {
    const v = viewOf(fx.tsmDeskResult)!
    expect(v).toMatchObject({ scale: 'log', headline: 'view', flat: true, median: { value: expect.closeTo(610.9, 1) }, pAbove: { value: expect.closeTo(0.621, 3) } })
    expect(v.edges.map((e) => e.value)).toEqual([300, 450, 600, 800, 1100])
    expect(rangeHints(v, 6)).toEqual(['below -38%', '-38%…-7%', '-7%…+24%', '+24%…+65%', '+65%…+127%', 'above +127%'])
    expect(rangeHints(v, 5), 'edges that do not line up give no hints').toEqual([])
    expect(rangeHints(viewOf(fx.tsmLegacyResult)!, 6), 'no reference, no hints').toEqual([])
    const view = fx.tsmDeskResult.view!
    const bad = (over: object) => viewOf({ view: { ...view, ...over } })
    expect(viewOf({})).toBeNull()
    expect(viewOf(null)).toBeNull()
    expect(bad({ scale: 'sqrt' })).toBeNull()
    expect(bad({ headline: 'most_likely' })).toBeNull()
    expect(bad({ median: { value: 'x' } })).toBeNull()
    expect(bad({ p75: null })).toBeNull()
    expect(bad({ p25: { value: 700 } }), 'quantiles out of order').toBeNull()
    expect(bad({ p10: { value: -5 } }), 'a log view below zero').toBeNull()
    expect(bad({ edges: [{ value: 300 }, { value: 'x' }] })!.edges).toEqual([])
    expect(bad({ edges: [{ value: 450 }, { value: 300 }] })!.edges).toEqual([])
    expect(bad({ pAbove: { value: 1.4 } })!.pAbove).toBeNull()
    expect(bad({ reference: { value: 0 } })!.reference).toBeNull()
    expect(bad({ unit: null })!.unit).toEqual({ prefix: '', suffix: '' })
    expect(viewOf(fx.tsmLegacyResult)).toMatchObject({ derived: true, reference: null, pAbove: null, basis: { kind: 'none' } })
    expect(viewOf(fx.tsmDeskResult)).not.toHaveProperty('derived')
  })

  it('states a centre only on a basis the server named', () => {
    const view = fx.tsmDeskResult.view!
    const v = (basis: unknown) => viewOf({ view: { ...view, basis } })!
    expect(v(view.basis)).toMatchObject({ basis: view.basis, pAbove: { value: expect.closeTo(0.621, 3) }, changePct: expect.closeTo(25.96, 1), annualPct: expect.closeTo(4.72, 1) })
    expect(anchored(v(view.basis))).toBe(true)
    expect(anchored(v({ kind: 'capped_drift', label: 'Measured TSM history since 1997' }))).toBe(true)
    for (const basis of [undefined, null, { kind: 'oracle', label: 'Trust me' }, { kind: 'none', label: '' }]) {
      const read = v(basis)
      expect(read.basis, JSON.stringify(basis)).toEqual({ kind: 'none', label: NO_BASIS_LABEL })
      expect(read, 'no chance above today or change without a basis').toMatchObject({ pAbove: null, changePct: null, change: null, annualPct: null })
      expect(anchored(read)).toBe(false)
    }
    expect(v({ kind: 'none', label: 'The server says why.' }).basis.label).toBe('The server says why.')
    expect(v({ kind: 'scenarios', label: 7 }).basis).toEqual({ kind: 'scenarios', label: '' })
    expect(anchored(null)).toBe(false)
  })

  it('reads list rows, scenarios, methods, measured history and stats defensively', () => {
    const { median, p10, p90, pAbove, unit, flat, headline } = fx.tsmDeskResult.view!
    const brief = { median, p10, p90, pAbove, unit, flat, headline, basis: 'scenarios' as const }
    expect(summaryViewOf({ view: brief })).toMatchObject({ headline: 'view', anchored: true, median: { value: expect.closeTo(610.9, 1) }, pAbove: { value: expect.closeTo(0.621, 3) } })
    expect(summaryViewOf({ view: { ...brief, basis: 'none' } }), 'no basis, no chance above today').toMatchObject({ anchored: false, pAbove: null })
    expect(summaryViewOf({ view: { ...brief, basis: undefined } })).toMatchObject({ anchored: false })
    expect(summaryViewOf({ view: { ...brief, headline: 'bucket', basis: 'capped_drift' } })).toMatchObject({ headline: 'bucket', anchored: true })
    expect(summaryViewOf({})).toBeNull()
    expect(summaryViewOf({ view: { ...brief, median: { value: Number.NaN } } })).toBeNull()
    expect(summaryViewOf({ view: { ...brief, median: { value: 2000 } } }), 'a median outside its 80% range').toBeNull()

    const sc = scenariosOf({ scenarios: [...fx.tsmScenarios].reverse() })
    expect(sc.map((s) => s.kind)).toEqual(['tail', 'bear', 'base', 'bull'])
    expect(scenariosOf({ scenarios: fx.tsmScenarios.slice(0, 1) }), 'fewer than two is none').toEqual([])
    const odd = scenariosOf({ scenarios: [
      { ...fx.tsmScenarios[1], kind: 'moon', sourceIds: ['F3', 'X9', 3], mustBeTrue: ['a', '', 'b', 'c', 'd'], inputs: [{ field: 'pe', value: 'x' }, { field: 'pe', value: 14, kind: 'rumour' }] },
      { ...fx.tsmScenarios[2], p: 1.5 },
      fx.tsmScenarios[3],
    ] })
    expect(odd.map((s) => s.name)).toEqual(['AI capex digestion', 'AI supercycle'])
    expect(odd[0]).toMatchObject({ kind: 'base', sourceIds: ['F3'], mustBeTrue: ['a', 'b', 'c'], inputs: [{ field: 'pe', value: 14, kind: 'assumption', verified: false }] })

    expect(methodsOf(fx.tsmDeskResult)).toMatchObject({ agreement: 'diverse', weightsFrom: 'desk', guard: null })
    expect(methodsOf({ methods: { ...fx.tsmDeskResult.methods!, agreement: 'unanimous' } })).toBeNull()
    expect(methodsOf({ methods: { ...fx.tsmDeskResult.methods!, pool: [] } })).toBeNull()
    expect(methodsOf({ methods: { ...fx.tsmDeskResult.methods!, weightsFrom: 'x', guard: 'x', weights: [{ role: 'quant', weight: -1 }] } })).toMatchObject({ weightsFrom: 'prior', guard: null, weights: [] })
    // The volatility floor: a final on the capped drift mixed toward the measured history's lognormal.
    expect(methodsOf({ methods: { ...fx.tsmDeskResult.methods!, guard: 'volatility' } })).toMatchObject({ guard: 'volatility' })

    expect(measuredOf(fx.tsmDeskResult)).toMatchObject({ symbol: 'TSM', url: 'https://finance.yahoo.com/quote/TSM/history', window: { independent: 5 } })
    expect(measuredOf({ measured: { ...fx.tsmMeasured, url: 'javascript:alert(1)', window: { days: 1825 }, maxDrawdown: { pct: 'x' } } })).toMatchObject({ url: null, window: null, maxDrawdown: null })
    expect(measuredOf({ measured: { ...fx.tsmMeasured, from: 'long ago' } })).toBeNull()

    const [b, quant] = fx.tsmDeskResult.specialists
    expect(statsOf(quant)).toMatchObject({ median: { value: 692.8 }, statedMedian: 690 })
    expect(statsOf({ stats: { ...b.stats!, pAbove: { value: 2 } } })!.pAbove).toBeNull()
    expect(statsOf({ stats: { ...b.stats!, p90: undefined } })).toBeNull()
    expect([methodOf(b), methodOf({ method: 'astrology' }), methodOf({})]).toEqual(['history', null, null])
  })
})

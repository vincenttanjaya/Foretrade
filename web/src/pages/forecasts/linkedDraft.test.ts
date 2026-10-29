import { describe, expect, it, vi } from 'vitest'
import { linkedDraft, linkedMarket } from '../../test/predictions'
import { cleanDraft, draftProblem, minDraftDate, minResolutionDate, normalizeDraft, updateDraft } from './forecast'

describe('canonical market drafts', () => {
  it('keeps full Unicode rules and the canonical manual predicate through normalization and submission', () => {
    const rules = '🪙'.repeat(8000)
    const draft = { ...linkedDraft, resolutionCriteria: rules, linkedMarket: { ...linkedMarket, description: rules } }
    expect(normalizeDraft(draft)).toEqual(draft)
    expect(cleanDraft(normalizeDraft(draft))).toEqual(draft)
    expect(draftProblem(draft, '2026-10-06')).toBeNull()
    expect(draftProblem({ ...draft, resolutionCriteria: rules + 'x' }, '2026-10-06')).toMatch(/8,000/)
  })

  it('unlinks meaningful predicate edits and retains over-cap rules rather than clipping', () => {
    const rules = 'x'.repeat(2400)
    const draft = { ...linkedDraft, resolutionCriteria: rules, linkedMarket: { ...linkedMarket, description: rules } }
    const changed = updateDraft(draft, { question: 'Will Bitcoin be below $100,000 on October 12?' })
    expect(changed.linkedMarket).toBeNull()
    expect(changed.resolutionCriteria).toBe(rules)
    expect(draftProblem(changed, '2026-10-06')).toMatch(/2,000/)
    expect(updateDraft(changed, { question: draft.question }).linkedMarket).toBeNull()
  })

  it.each([
    { resolutionCriteria: 'Another rule' }, { resolutionDate: '2026-10-13' },
    { shape: 'categorical' as const }, { outcomes: ['True', 'False'] },
    { priceRule: { symbol: 'BTC', op: 'above' as const, threshold: 100000, mode: 'on_date' as const } },
  ])('unlinks the edited predicate %j', (patch) => {
    expect(updateDraft(linkedDraft, patch).linkedMarket).toBeNull()
  })

  it('keeps provenance for notes/category/outer whitespace without changing internal rules', () => {
    expect(updateDraft(linkedDraft, { context: 'My view', category: 'markets', question: ` ${linkedDraft.question} ` }).linkedMarket).toEqual(linkedMarket)
    expect(updateDraft(linkedDraft, { resolutionCriteria: linkedDraft.resolutionCriteria.replace('\n', ' ') }).linkedMarket).toBeNull()
  })

  it('uses UTC future tracking dates for linked drafts near local midnight', () => {
    const now = new Date('2026-10-05T16:30:00Z')
    // Simulate Singapore's local calendar day without changing the worker's deliberate Los Angeles timezone.
    vi.spyOn(now, 'getFullYear').mockReturnValue(2026)
    vi.spyOn(now, 'getMonth').mockReturnValue(9)
    vi.spyOn(now, 'getDate').mockReturnValue(6)
    expect(minResolutionDate(now)).toBe('2026-10-07')
    expect(minDraftDate(linkedDraft, now)).toBe('2026-10-06')
  })
})

import { expect, it } from 'vitest'
import { copyInput, mergeSteps, personaColor, targetChange } from './live'
import * as fx from '../../test/fixtures'
it('validates inclusive copy ratio/cap endpoints and blocks blank nonfinite and out-of-range input', () => {
  expect(copyInput('10', '100')).toEqual({ ratio: 0.1, maxUsd: 100 })
  expect(copyInput('100', '50000')).toEqual({ ratio: 1, maxUsd: 50000 })
  for (const ratio of ['', '9', '101', 'Infinity', 'NaN']) expect(copyInput(ratio, '100')).toBeNull()
  for (const cap of ['', '99', '50001', 'Infinity', 'NaN']) expect(copyInput('10', cap)).toBeNull()
})
it('merges descending exclusive-before history without duplicates and bounds local cache to 100', () => {
  expect(mergeSteps([fx.arenaStep(2), fx.arenaStep(1)], [fx.arenaStep(3), fx.arenaStep(2, { reasoning: 'updated' })]).map((s) => [s.step, s.reasoning])).toEqual([[3, fx.arenaStep(3).reasoning], [2, 'updated'], [1, fx.arenaStep(1).reasoning]])
  const merged = mergeSteps([], Array.from({ length: 150 }, (_, i) => fx.arenaStep(i)))
  expect(merged).toHaveLength(100)
  expect(merged[0].step).toBe(149)
  expect(merged[99].step).toBe(50)
})
it('does not invent a zero-weight transition at the end of a bounded page', () => {
  expect(targetChange(25, undefined)).toBe('25% target')
  expect(targetChange(25, 10)).toBe('10% → 25%')
  expect(personaColor('momentum')).not.toBe(personaColor('contrarian'))
  expect(personaColor(fx.liveAgent({ rank: 3 }).personaId)).toBe(personaColor(fx.liveAgent({ rank: 1 }).personaId))
})

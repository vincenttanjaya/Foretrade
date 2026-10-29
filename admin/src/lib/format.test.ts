import { afterEach, describe, expect, it, vi } from 'vitest'
import { auditLabel, auditSummary, seededWarning } from './audit'
import { fmtDate, fmtDay, fmtDuration, fmtNum, fmtPct, fmtPrice, fmtQty, fmtUsd, plural, timeAgo } from './format'
import { passwordProblem } from './password'
import type { AuditAction } from './types'

afterEach(() => vi.useRealTimers())

describe('formatters', () => {
  it('formats money, percentages and counts, with a dash for missing values', () => {
    expect(fmtUsd(100194)).toBe('$100,194.00')
    expect(fmtUsd(1234, { sign: true, decimals: 0 })).toBe('+$1,234')
    expect(fmtUsd(-6)).toBe('-$6.00')
    expect(fmtUsd(250_000, { compact: true })).toBe('$250K')
    expect(fmtPct(0.19, { sign: true })).toBe('+0.19%')
    expect(fmtNum(12345.6)).toBe('12,346')
    expect(fmtQty(0.1234567)).toBe('0.123457')
    expect(fmtPrice(84213.2)).toBe('$84,213.20')
    expect(fmtPrice(2.45)).toBe('$2.45')
    expect(fmtPrice(0.081234)).toBe('$0.081234')
    for (const f of [fmtUsd, fmtPct, fmtNum, fmtQty, fmtPrice]) expect(f(null)).toBe('—')
    expect(fmtNum(Number.NaN)).toBe('—')
    expect(plural(1, 'admin')).toBe('1 admin')
    expect(plural(1204, 'session')).toBe('1,204 sessions')
  })

  it('labels UTC days without shifting them into the local zone', () => {
    expect(fmtDay('2026-10-02')).toBe('Oct 2')
    expect(fmtDay('nope')).toBe('—')
    expect(fmtDate('2026-06-30')).toBe('Jun 30, 2026')
    expect(fmtDate('2026-10-02T09:30:00Z', { time: true })).toBe('Oct 2, 2026, 2:30 AM') // TZ = America/Los_Angeles
    expect(fmtDate(null)).toBe('—')
    expect(fmtDate('garbage')).toBe('—')
  })

  it('formats durations and relative times', () => {
    expect(fmtDuration(2 * 86400 + 4 * 3600 + 5)).toBe('2d 4h')
    expect(fmtDuration(5 * 3600 + 12 * 60)).toBe('5h 12m')
    expect(fmtDuration(480)).toBe('8m')
    expect(fmtDuration(-1)).toBe('—')
    vi.useFakeTimers({ now: new Date('2026-10-03T12:00:00Z') })
    expect(timeAgo('2026-10-03T11:59:30Z')).toBe('just now')
    expect(timeAgo('2026-10-03T11:48:00Z')).toBe('12m ago')
    expect(timeAgo('2026-10-03T09:00:00Z')).toBe('3h ago')
    expect(timeAgo('2026-09-29T12:00:00Z')).toBe('4d ago')
    expect(timeAgo('2026-09-01T12:00:00Z')).toBe('Sep 1, 2026')
    expect(timeAgo(null)).toBe('—')
    expect(timeAgo('garbage')).toBe('—')
  })
})

describe('passwordProblem', () => {
  it('follows the API rule: 12..72 bytes, not only whitespace', () => {
    expect(passwordProblem('short')).toMatch(/at least 12/)
    expect(passwordProblem('x'.repeat(73))).toMatch(/at most 72 bytes/)
    expect(passwordProblem('é'.repeat(37))).toMatch(/at most 72 bytes/) // 74 bytes in 37 characters
    expect(passwordProblem(' '.repeat(12))).toMatch(/only spaces/)
    expect(passwordProblem('correct horse battery')).toBeNull()
  })
})

describe('audit labels', () => {
  const e = (action: AuditAction | string, detail: Record<string, unknown> = {}) => ({ action: action as AuditAction, detail })

  it.each([
    ['user.create', { role: 'admin', creditLimit: null, passwordGenerated: true }, 'Created user', 'role admin · credit limit default · password generated'],
    ['user.create', { role: 'user', creditLimit: 100, passwordGenerated: false }, 'Created user', 'role user · credit limit 100 · password set by admin'],
    ['user.update', { name: { from: 'A', to: 'B' }, creditLimit: { from: null, to: 50 } }, 'Updated user', 'name: A → B · credit limit: default → 50'],
    ['user.disable', { sessionsRevoked: 1 }, 'Disabled user', '1 session revoked'],
    ['user.enable', {}, 'Enabled user', ''],
    ['user.password', { generated: true, sessionsRevoked: 2 }, 'Generated password', 'generated · 2 sessions revoked'],
    ['user.password', { generated: false, sessionsRevoked: 0 }, 'Set password', 'set by admin · 0 sessions revoked'],
    ['user.sessions_revoke', { sessionsRevoked: 3 }, 'Revoked sessions', '3 sessions revoked'],
    ['user.portfolio_reset', {}, 'Reset portfolio', ''],
    ['user.credits_reset', { creditsBefore: 42 }, "Reset today's credits", '42 credits used before'],
    ['user.delete', { name: 'Ann', role: 'user' }, 'Deleted user', 'Ann · user'],
    [
      'user.create',
      { role: 'user', creditLimit: null, passwordGenerated: true, seededFrom: 'demo@example.com', seeded: { forecasts: 3, portfolios: 2, strategies: 3, arenaRuns: 1 } },
      'Created user',
      'role user · credit limit default · password generated · showcase from demo@example.com: 3 forecasts · 2 AI portfolios · 3 strategies · 1 arena run',
    ],
    ['user.seed', { seededFrom: 'demo@example.com', seeded: { forecasts: 1, portfolios: 0, strategies: 2 } }, 'Copied showcase content', 'from demo@example.com: 1 forecast · 0 AI portfolios · 2 strategies · 0 arena runs'],
    ['settings.update', { fields: ['ai.provider', 'ai.apiKey'] }, 'Updated settings', 'ai.provider, ai.apiKey'],
    ['settings.update', {}, 'Updated settings', ''],
    ['settings.chatgpt.signin', {}, 'Signed in with ChatGPT', ''],
    ['settings.chatgpt.signout', {}, 'Signed out of ChatGPT', ''],
    ['user.future_thing', { x: 1 }, 'user.future_thing', ''],
  ])('%s %j', (action, detail, label, summary) => {
    expect(auditLabel(e(action, detail))).toBe(label)
    expect(auditSummary(e(action, detail))).toBe(summary)
  })

  it('warns per showcase tab a seed left empty', () => {
    expect(seededWarning({ forecasts: 3, portfolios: 0, strategies: 2, arenaRuns: 0 })).toBe(' The source account has no AI portfolio to copy. Build one there first.')
    expect(seededWarning({ portfolios: 1, strategies: 0 })).toBe(' The source account has no strategy to copy. Build one there first.')
    expect(seededWarning({ portfolios: 2, strategies: 3 })).toBe('')
    expect(seededWarning(undefined)).toBe('')
  })

  it('reads odd detail values as text, never as markup', () => {
    expect(auditSummary(e('user.update', { name: { from: '<b>x</b>', to: { nested: true } }, role: 'not-an-object' }))).toBe(
      'name: <b>x</b> → {"nested":true}',
    )
    expect(auditSummary(e('user.disable'))).toBe('0 sessions revoked')
  })
})

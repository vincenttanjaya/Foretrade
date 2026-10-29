import { describe, expect, it } from 'vitest'
import { ApiError } from './api'
import { errorMessage, isQuota, QUOTA_MESSAGE } from './errors'

describe('errorMessage', () => {
  it('maps API errors to user copy', () => {
    expect(errorMessage(new ApiError(429, 'ai_quota', 'quota'))).toBe('Daily AI credits used up. They reset at 00:00 UTC.')
    expect(errorMessage(new ApiError(429, 'rate_limited', 'slow'))).toBe('Too many requests. Wait a moment and try again.')
    expect(errorMessage(new ApiError(400, 'insufficient_buying_power', 'Not enough buying power'))).toBe('Not enough buying power')
    expect(errorMessage(new ApiError(500, 'internal', ''))).toBe('Something went wrong. Please try again.')
  })

  it('uses the fallback for anything that is not an ApiError', () => {
    expect(errorMessage(new Error('boom'), 'fallback')).toBe('fallback')
    expect(errorMessage('nope')).toBe('Something went wrong. Please try again.')
  })
})

describe('isQuota', () => {
  it('is true only for ai_quota API errors', () => {
    expect(isQuota(new ApiError(429, 'ai_quota', ''))).toBe(true)
    expect(isQuota(new ApiError(429, 'rate_limited', ''))).toBe(false)
    expect(isQuota(new Error('x'))).toBe(false)
    expect(QUOTA_MESSAGE).toBe(errorMessage(new ApiError(429, 'ai_quota', '')))
  })
})

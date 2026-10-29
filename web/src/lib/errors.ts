// User-facing copy for failed API calls, shared by every page.
import { ApiError } from './api'

export const QUOTA_MESSAGE = 'Daily AI credits used up. They reset at 00:00 UTC.'

export const isQuota = (err: unknown) => err instanceof ApiError && err.code === 'ai_quota'

/** Human message for any thrown value; `ai_quota` gets the friendly credits line. */
export function errorMessage(err: unknown, fallback = 'Something went wrong. Please try again.') {
  if (isQuota(err)) return QUOTA_MESSAGE
  if (!(err instanceof ApiError)) return fallback
  if (err.code === 'rate_limited') return 'Too many requests. Wait a moment and try again.'
  return err.message || fallback
}

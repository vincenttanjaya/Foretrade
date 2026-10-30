// Typed client for the Go API's auth and /api/admin/* routes. Every function returns the exact JSON body from
// API-CONTRACT.md (§3 Auth, Admin console).
import type {
  AIDraft,
  AdminOverview,
  AdminUser,
  AdminUserDetail,
  AuditList,
  CreateUserRequest,
  Credits,
  ModelInfo,
  Portfolio,
  SearchDraft,
  Seeded,
  Settings,
  TestResult,
  UpdateUserRequest,
  User,
  UserList,
  UserQuery,
} from './types'

export const API_URL = (import.meta.env.VITE_API_URL || 'http://localhost:8080').replace(/\/+$/, '')
/** The trade app, for the "Open Foretrade" link. */
export const APP_URL = (import.meta.env.VITE_APP_URL || 'http://localhost:5173').replace(/\/+$/, '')

export class ApiError extends Error {
  readonly status: number // 0 = network failure
  readonly code: string

  constructor(status: number, code: string, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
  }
}

// AuthProvider registers these: a 401 anywhere sends the admin to /login; a 403 from /api/admin/* re-checks /api/me and
// shows the "Access restricted" wall only for an admin demoted mid-session (an ADMIN_ORIGINS 403 stays a page error).
const handlers = { unauthorized: () => {}, forbidden: () => {} }
export function setAuthHandlers(h: typeof handlers) {
  Object.assign(handlers, h)
}

/** fetch with cookies + JSON; throws ApiError for network failures and non-2xx responses. */
async function request<T>(method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  const init: RequestInit = { method, credentials: 'include', signal }
  // The API's CSRF rule: every non-GET (DELETE included) carries a JSON content type, even with an empty body.
  if (method !== 'GET') {
    init.headers = { 'Content-Type': 'application/json' }
    init.body = JSON.stringify(body ?? {})
  }
  let res: Response
  try {
    res = await fetch(API_URL + path, init)
  } catch (err) {
    if (signal?.aborted) throw err
    throw new ApiError(0, 'network', "Can't reach the API. Check your connection and try again.")
  }
  if (res.ok) return (res.status === 204 ? undefined : await res.json()) as T

  // The envelope is {"error":{"code","message"}}; a bare {"error":"…"} is read as the message.
  const err = await res
    .json()
    .then((j: { error?: string | { code?: string; message?: string } }) => (typeof j.error === 'string' ? { message: j.error } : j.error))
    .catch(() => undefined)
  if (res.status === 401 && path !== '/api/auth/login') handlers.unauthorized()
  if (res.status === 403 && path.startsWith('/api/admin/')) handlers.forbidden()
  throw new ApiError(res.status, err?.code ?? 'internal', err?.message ?? `Request failed (${res.status})`)
}

const get = <T>(path: string, signal?: AbortSignal) => request<T>('GET', path, undefined, signal)
const post = <T>(path: string, body: unknown = {}) => request<T>('POST', path, body)

/** "?a=1&b=x", skipping undefined and empty values. */
export const qs = (params: Record<string, string | number | undefined>) => {
  const q = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== '') q.set(k, String(v))
  const s = q.toString()
  return s ? `?${s}` : ''
}
const user = (id: string) => `/api/admin/users/${encodeURIComponent(id)}`

type WithPassword<T> = T & { password?: string } // only when the server generated it: shown once

export const api = {
  // Auth (shared with the trade app: one tsid cookie on the API host)
  login: (email: string, password: string) => post<{ user: User }>('/api/auth/login', { email, password }),
  logout: () => post<void>('/api/auth/logout'),
  me: (signal?: AbortSignal) => get<{ user: User; credits: Credits }>('/api/me', signal),

  // Admin console
  overview: (signal?: AbortSignal) => get<{ overview: AdminOverview }>('/api/admin/overview', signal),
  users: (query: UserQuery = {}, signal?: AbortSignal) => get<UserList>(`/api/admin/users${qs(query)}`, signal),
  createUser: (body: CreateUserRequest) => post<WithPassword<{ user: AdminUser; seeded?: Seeded }>>('/api/admin/users', body),
  user: (id: string, signal?: AbortSignal) => get<AdminUserDetail>(user(id), signal),
  updateUser: (id: string, patch: UpdateUserRequest) => request<{ user: AdminUser }>('PATCH', user(id), patch),
  /** No `password` → the server generates one. */
  setPassword: (id: string, password?: string) =>
    post<WithPassword<{ user: AdminUser }>>(`${user(id)}/password`, password === undefined ? {} : { password }),
  revokeSessions: (id: string) => post<{ user: AdminUser; revoked: number }>(`${user(id)}/sessions/revoke`),
  resetPortfolio: (id: string) => post<{ user: AdminUser; portfolio: Portfolio | null }>(`${user(id)}/reset-portfolio`),
  resetCredits: (id: string) => post<{ user: AdminUser }>(`${user(id)}/reset-credits`),
  /** Copies `from`'s showcase content into the account again (no dedupe). */
  seedUser: (id: string, from: string) => post<{ user: AdminUser; seeded: Seeded }>(`${user(id)}/seed`, { from }),
  deleteUser: (id: string) => request<void>('DELETE', user(id)),
  audit: (query: { page?: number; limit?: number } = {}, signal?: AbortSignal) =>
    get<AuditList>(`/api/admin/audit${qs(query)}`, signal),

  // Settings: secrets go in, never come back out
  settings: (signal?: AbortSignal) => get<Settings>('/api/admin/settings', signal),
  /** A change to `ai` is tested first: a failed test is a 400 with the provider's message, and nothing is saved. */
  updateSettings: (body: { ai?: AIDraft; search?: SearchDraft }) => request<Settings>('PUT', '/api/admin/settings', body),
  /** Always 200: `ok` says whether the draft (merged over the saved config) answered. */
  testSettings: (ai: AIDraft) => post<TestResult>('/api/admin/settings/test', { ai }),
  models: (ai: AIDraft) => post<{ models: ModelInfo[] }>('/api/admin/settings/models', { ai }),
  /** The ChatGPT authorize URL; the API's /auth/callback brings the browser back to /settings?chatgpt=ok|error. */
  chatgptStart: () => post<{ url: string }>('/api/admin/settings/chatgpt/start'),
  chatgptSignOut: () => post<Settings>('/api/admin/settings/chatgpt/signout'),
}

/** Human message for any thrown value. 400 / 409 / 429 messages from the API are shown verbatim. */
export function errorMessage(err: unknown, fallback = 'Something went wrong. Please try again.') {
  return err instanceof ApiError && err.message ? err.message : fallback
}

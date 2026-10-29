import { createContext, use, useCallback, useEffect, useState, type ReactNode } from 'react'
import { ApiError, api, setAuthHandlers } from './api'
import type { User } from './types'

/**
 * loading: the first /api/me (or a Retry) is in flight · ok: checked; `user` is the admin, or null when signed out ·
 * restricted: a non-admin session was found (or an admin route answered 403) and has been signed out ·
 * error: the API could not be reached.
 */
export type AuthState = 'loading' | 'ok' | 'restricted' | 'error'

type Auth = {
  user: User | null
  state: AuthState
  /** Throws ApiError: the API's own (401 invalid_credentials, 429 …), or code `restricted` for a non-admin account. */
  login: (email: string, password: string) => Promise<void>
  logout: () => Promise<void>
  retry: () => void
  /** Leaves the "Access restricted" wall for the sign-in page. */
  dismissRestricted: () => void
}

export const RESTRICTED_MESSAGE = 'Access restricted — this console is for administrators only.'

const AuthContext = createContext<Auth | null>(null)

const signOut = () => api.logout().catch(() => {}) // forget the session even if the API is unreachable

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null)
  const [state, setState] = useState<AuthState>('loading')

  // The console only ever holds an admin session: anything else is signed out and walled off.
  const restrict = useCallback(async () => {
    setUser(null)
    await signOut()
    setState('restricted')
  }, [])

  const check = useCallback(async () => {
    setState('loading')
    try {
      const { user } = await api.me()
      if (user.role !== 'admin') return await restrict()
      setUser(user)
      setState('ok')
    } catch (err) {
      setUser(null)
      setState(err instanceof ApiError && err.status === 401 ? 'ok' : 'error')
    }
  }, [restrict])

  useEffect(() => {
    setAuthHandlers({ unauthorized: () => setUser(null), forbidden: () => void restrict() })
    void check()
  }, [check, restrict])

  const login = useCallback(async (email: string, password: string) => {
    const res = await api.login(email, password)
    if (res.user.role !== 'admin') {
      await signOut()
      throw new ApiError(403, 'restricted', RESTRICTED_MESSAGE)
    }
    setUser(res.user)
    setState('ok')
  }, [])

  const logout = useCallback(async () => {
    await signOut()
    setUser(null)
  }, [])

  return (
    <AuthContext
      value={{ user, state, login, logout, retry: () => void check(), dismissRestricted: () => setState('ok') }}
    >
      {children}
    </AuthContext>
  )
}

export function useAuth() {
  const ctx = use(AuthContext)
  if (!ctx) throw new Error('useAuth must be used inside <AuthProvider>')
  return ctx
}

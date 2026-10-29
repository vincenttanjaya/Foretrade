import { createContext, use, useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import { api, setUnauthorizedHandler } from './api'
import type { ConfirmMemoryRequest, Credits, Memory, MemoryProposal, Profile, UpdateProfileRequest, User } from './types'

type Receipt = MemoryProposal & { revision: number }

type Auth = {
  user: User | null
  credits: Credits | null
  loading: boolean // true until the first /api/me answers
  setCredits: (c: Credits) => void
  refresh: () => Promise<void>
  login: (email: string, password: string) => Promise<void>
  logout: () => Promise<void>
  refreshProfile: () => Promise<Profile | null>
  saveProfile: (request: UpdateProfileRequest) => Promise<Profile | null>
  confirmMemory: (request: ConfirmMemoryRequest) => Promise<Memory | null>
  deleteMemory: (id: string) => Promise<Profile | null>
  memoryReceipts: Record<string, Receipt>
  reconcileMemory: (proposalId: string, chatId: string) => Promise<MemoryProposal | null>
  observeConfirmedMemory: (owner: string, data: MemoryProposal) => void
}

const AuthContext = createContext<Auth | null>(null)

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null)
  const [credits, setCredits] = useState<Credits | null>(null)
  const [loading, setLoading] = useState(true)
  const current = useRef<User | null>(null)
  const generation = useRef(0)
  const [session, setSession] = useState(0)
  const ownerSignal = useRef(new AbortController())
  const [memoryReceipts, setMemoryReceipts] = useState<Record<string, Receipt>>({})
  const receiptRef = useRef<Record<string, Receipt>>({})
  const receipts = useCallback((next: Record<string, Receipt>) => { receiptRef.current = next; setMemoryReceipts(next) }, [])
  const signOutRef = useRef<() => void>(() => {})

  const advance = useCallback(() => {
    generation.current++
    const version = generation.current
    // New-session requests can complete before React's next effect. Bind their
    // 401 handler immediately; send() retains the old handler for older requests.
    setUnauthorizedHandler(() => { if (version === generation.current) signOutRef.current() })
    ownerSignal.current.abort()
    ownerSignal.current = new AbortController()
    receipts({})
    setSession(generation.current)
    return generation.current
  }, [receipts])

  const publish = useCallback((next: User | null) => {
    current.current = next
    setUser(next)
  }, [])

  const signedOut = useCallback(() => {
    advance()
    publish(null)
    setCredits(null)
  }, [advance, publish])
  signOutRef.current = signedOut

  const acceptProfile = useCallback((owner: string, version: number, profile: Profile): Profile | null => {
    const previous = current.current
    if (version !== generation.current || previous?.id !== owner) return null
    const latest = profile.revision < previous.profile.revision ? previous.profile : profile
    const next = { ...receiptRef.current }
    for (const [id, receipt] of Object.entries(next)) {
      if (receipt.status === 'confirmed' && latest.revision >= receipt.revision && !latest.memories.some((m) => m.id === receipt.memoryId)) next[id] = { ...receipt, status: 'deleted' }
    }
    receipts(next)
    publish({ ...previous, profile: latest })
    return latest
  }, [publish, receipts])

  // Re-reads the user and today's credits. A 401 signs out through the handler below.
  const refresh = useCallback(async () => {
    const version = generation.current
    const owner = current.current?.id
    const res = await api.me(ownerSignal.current.signal)
    if (version !== generation.current || current.current?.id !== owner) return
    const previous = current.current
    // A current ME can discover a session change in another tab. Advancing ownership
    // fences the other in-flight reads; an older ME can never reverse that transition.
    if (owner && res.user.id !== owner) advance()
    if (previous?.id === res.user.id) {
      publish({ ...res.user, profile: previous.profile })
      acceptProfile(res.user.id, version, res.user.profile)
    } else publish(res.user)
    setCredits(res.credits)
  }, [publish, acceptProfile, advance])

  useEffect(() => {
    const version = generation.current
    if (ownerSignal.current.signal.aborted) ownerSignal.current = new AbortController()
    setUnauthorizedHandler(() => { if (version === generation.current) signedOut() })
  }, [session, signedOut])

  useEffect(() => {
    const version = generation.current
    let disposed = false
    refresh()
      .catch(() => { if (!disposed && version === generation.current) signedOut() })
      .finally(() => { if (!disposed) setLoading(false) })
    return () => { disposed = true; ownerSignal.current.abort() }
  }, [refresh, signedOut])

  const login = useCallback(
    async (email: string, password: string) => {
      const version = advance()
      const res = await api.login(email, password)
      if (version !== generation.current) return
      publish(res.user)
      await refresh().catch(() => {}) // credits only; the session is already valid
    },
    [advance, publish, refresh],
  )

  const logout = useCallback(async () => {
    signedOut()
    await api.logout().catch(() => {}) // forget the session locally even if the API is unreachable
  }, [signedOut])

  const refreshProfile = useCallback(async () => {
    const owner = current.current?.id
    const version = generation.current
    if (!owner) return null
    const res = await api.profile(ownerSignal.current.signal)
    return acceptProfile(owner, version, res.profile)
  }, [acceptProfile])

  const saveProfile = useCallback(async (request: UpdateProfileRequest) => {
    const owner = current.current?.id
    const version = generation.current
    if (!owner) return null
    const res = await api.updateProfile(request, ownerSignal.current.signal)
    return acceptProfile(owner, version, res.profile)
  }, [acceptProfile])

  const confirmMemory = useCallback(async (request: ConfirmMemoryRequest) => {
    const owner = current.current?.id
    const version = generation.current
    if (!owner) return null
    const res = await api.confirmMemory(request, ownerSignal.current.signal)
    const latest = acceptProfile(owner, version, res.profile)
    if (!latest) return null
    receipts({ ...receiptRef.current, [request.proposalId]: { ...request, memoryId: res.memory.id, status: latest.memories.some((m) => m.id === res.memory.id) ? 'confirmed' : 'deleted', revision: res.profile.revision } })
    return res.memory
  }, [acceptProfile, receipts])

  const deleteMemory = useCallback(async (id: string) => {
    const owner = current.current?.id
    const version = generation.current
    if (!owner) return null
    const res = await api.deleteMemory(id, ownerSignal.current.signal)
    return acceptProfile(owner, version, res.profile)
  }, [acceptProfile])

  const reconcileMemory = useCallback(async (proposalId: string, chatId: string) => {
    const owner = current.current?.id
    const version = generation.current
    const signal = ownerSignal.current.signal
    if (!owner) return null
    const res = await api.chat(chatId, signal)
    const data = res.chat.messages.filter((m) => m.role === 'assistant').flatMap((m) => m.cards).find((c) => c.type === 'memory' && c.data.proposalId === proposalId)
    if (!data || data.type !== 'memory' || version !== generation.current || current.current?.id !== owner) return null
    // Read after receipt hydration so a pre-confirm profile cannot classify a new receipt as deleted.
    const latest = await api.profile(signal)
    const profile = acceptProfile(owner, version, latest.profile)
    if (!profile) return null
    const old = receiptRef.current[proposalId]
    const hydrated: Receipt = { ...data.data, revision: profile.revision }
    if (hydrated.status === 'confirmed' && hydrated.memoryId && !profile.memories.some((m) => m.id === hydrated.memoryId)) hydrated.status = 'deleted'
    if (old?.status === 'deleted' || (old?.status === 'confirmed' && hydrated.status === 'open')) return old
    receipts({ ...receiptRef.current, [proposalId]: hydrated })
    return hydrated
  }, [acceptProfile, receipts])

  const observeConfirmedMemory = useCallback((owner: string, data: MemoryProposal) => {
    const active = current.current
    if (active?.id !== owner || data.status !== 'confirmed' || !active.profile.memories.some((m) => m.id === data.memoryId) || receiptRef.current[data.proposalId]?.status === 'deleted' || receiptRef.current[data.proposalId]?.status === 'confirmed') return
    // Bind only an already confirmed card with an active memory in this owner's
    // authoritative profile. This records UI metadata, never writes a memory.
    receipts({ ...receiptRef.current, [data.proposalId]: { ...data, revision: active.profile.revision } })
  }, [receipts])

  return (
    <AuthContext value={{ user, credits, loading, setCredits, refresh, login, logout, refreshProfile, saveProfile, confirmMemory, deleteMemory, memoryReceipts, reconcileMemory, observeConfirmedMemory }}>{children}</AuthContext>
  )
}

export function useAuth() {
  const ctx = use(AuthContext)
  if (!ctx) throw new Error('useAuth must be used inside <AuthProvider>')
  return ctx
}

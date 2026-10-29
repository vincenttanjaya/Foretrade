import { useEffect, useId, useRef, useState } from 'react'
import { Button } from '../../components/ui/Button'
import { Card, CardHeader } from '../../components/ui/Card'
import { Input, Label, Select, Textarea } from '../../components/ui/Input'
import { ApiError } from '../../lib/api'
import { useAuth } from '../../lib/auth'
import { errorMessage } from '../../lib/errors'
import type { Profile, ProfilePreferences } from '../../lib/types'
import { SYMBOLS } from '../strategies/strategy'

export const preferences = (p: Profile): ProfilePreferences => ({ enabled: p.enabled, riskProfile: p.riskProfile, style: p.style, focusSymbols: [...p.focusSymbols], customPrompt: p.customPrompt })
const same = (a: ProfilePreferences, b: ProfilePreferences) => JSON.stringify(a) === JSON.stringify(b)

export function Personalization() {
  const { user } = useAuth()
  return user ? <ProfileEditor key={user.id} profile={user.profile} /> : null
}

function ProfileEditor({ profile }: { profile: Profile }) {
  const { saveProfile, refreshProfile, deleteMemory } = useAuth()
  const id = useId()
  const [draft, setDraft] = useState(() => preferences(profile))
  const baseline = useRef(preferences(profile))
  const [query, setQuery] = useState('')
  const [saving, setSaving] = useState(false)
  const [removing, setRemoving] = useState<string | null>(null)
  const busy = useRef(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  useEffect(() => {
    const next = preferences(profile)
    const previous = baseline.current
    setDraft((current) => same(current, previous) ? next : current)
    baseline.current = next
  }, [profile])
  const dirty = !same(draft, preferences(profile))
  const runes = [...draft.customPrompt].length
  const patch = (change: Partial<ProfilePreferences>) => { setDraft((p) => ({ ...p, ...change })); setNotice(''); setError('') }

  async function save() {
    if (busy.current || !dirty || runes > 600) return
    busy.current = true
    setSaving(true)
    setError('')
    setNotice('')
    const submitted = draft
    try {
      const saved = await saveProfile({ ...submitted, revision: profile.revision })
      if (saved) { setDraft((current) => same(current, submitted) ? preferences(saved) : current); setNotice('Preferences saved.') }
    } catch (err) {
      if (err instanceof ApiError && err.status === 409) {
        setError('Your profile changed. Your edits are kept. Review the latest saved profile and Save again.')
        await refreshProfile().catch(() => setError('Your profile changed. Your edits are kept. Could not reload the latest profile; retry Save when connected.'))
      } else setError(errorMessage(err, 'Could not save preferences. Your edits are kept.'))
    } finally { busy.current = false; setSaving(false) }
  }

  async function remove(memoryId: string) {
    if (busy.current) return
    busy.current = true
    setRemoving(memoryId)
    setError('')
    try {
      if (await deleteMemory(memoryId)) setNotice('Memory deleted.')
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) await refreshProfile().catch(() => {})
      setError(errorMessage(err, 'Could not delete this memory. Please try again.'))
    } finally { busy.current = false; setRemoving(null) }
  }

  return (
    <Card className="mt-6">
      <CardHeader label="Personalization" title="Your preferences" />
      <div className="space-y-5 px-5 pb-5">
        <label className="flex items-center gap-3 text-sm font-medium">
          <input type="checkbox" checked={draft.enabled} onChange={(e) => patch({ enabled: e.target.checked })} className="size-4 accent-accent" />
          Personalize AI responses
        </label>
        <p className="text-[13px] leading-relaxed text-muted">When off, your preferences and memories stay saved and editable. The AI uses its usual defaults.</p>
        <div className="grid gap-4 sm:grid-cols-2">
          <div><Label htmlFor={`${id}-risk`}>Risk profile</Label><Select id={`${id}-risk`} value={draft.riskProfile ?? ''} onChange={(e) => patch({ riskProfile: (e.target.value || null) as Profile['riskProfile'] })}>
            <option value="">No preference</option><option value="conservative">Conservative · 5%</option><option value="balanced">Balanced · 10%</option><option value="aggressive">Aggressive · 15%</option>
          </Select></div>
          <div><Label htmlFor={`${id}-style`}>Preferred trading style</Label><Select id={`${id}-style`} value={draft.style ?? ''} onChange={(e) => patch({ style: (e.target.value || null) as Profile['style'] })}>
            <option value="">No preference</option><option value="scalp">Scalp</option><option value="day">Day</option><option value="swing">Swing</option>
          </Select></div>
        </div>
        <p className="text-[13px] leading-relaxed text-muted">Risk limits apply when the AI chooses the size of an opening or added paper position: 5%, 10% or 15% of equity. The usual limit is 10%. Your explicit dollar or quantity requests and reducing or closing positions are exempt.</p>
        <fieldset>
          <legend className="label mb-1.5">Focus symbols · {draft.focusSymbols.length}/10</legend>
          <Label htmlFor={`${id}-symbols`} className="sr-only">Search focus symbols</Label>
          <Input id={`${id}-symbols`} placeholder="Search symbols or names" value={query} onChange={(e) => setQuery(e.target.value)} />
          {!!draft.focusSymbols.length && <p className="mt-2 text-[13px] text-muted [overflow-wrap:anywhere]">Selected: {draft.focusSymbols.join(', ')}</p>}
          <div className="mt-2 grid max-h-44 grid-cols-2 gap-1 overflow-y-auto rounded-lg border border-border p-2 sm:grid-cols-3">
            {SYMBOLS.filter(([symbol, name]) => `${symbol} ${name}`.toLowerCase().includes(query.toLowerCase())).map(([symbol, name]) => {
              const selected = draft.focusSymbols.includes(symbol)
              return <label key={symbol} className="flex min-w-0 items-center gap-2 rounded-md px-1 py-2 text-[13px]" title={name}>
                <input type="checkbox" aria-label={`Focus ${symbol}`} checked={selected} disabled={!selected && draft.focusSymbols.length >= 10} onChange={() => patch({ focusSymbols: selected ? draft.focusSymbols.filter((s) => s !== symbol) : [...draft.focusSymbols, symbol] })} className="size-4 shrink-0 accent-accent" />
                <span className="truncate">{symbol}</span>
              </label>
            })}
          </div>
        </fieldset>
        <div>
          <Label htmlFor={`${id}-prompt`}>Custom instructions</Label>
          <Textarea id={`${id}-prompt`} value={draft.customPrompt} onChange={(e) => patch({ customPrompt: e.target.value })} aria-invalid={runes > 600} aria-describedby={`${id}-count`} className="max-h-52" />
          <p id={`${id}-count`} className="mt-1 text-[12px] text-muted">{runes}/600 characters</p>
        </div>
        {error && <p role="alert" className="text-[13px] text-down">{error}</p>}
        {notice && <p role="status" className="text-[13px] text-muted">{notice}</p>}
        <div className="flex flex-wrap gap-2">
          <Button onClick={save} loading={saving} disabled={!dirty || runes > 600 || removing != null}>Save preferences</Button>
          {dirty && <Button variant="secondary" disabled={saving || removing != null} onClick={() => { setDraft(preferences(profile)); setError(''); setNotice('') }}>Discard changes</Button>}
        </div>
        <section aria-label="Saved memories" className="border-t border-border pt-5">
          <h3 className="font-display text-lg font-medium">Memories · {profile.memories.length}/20</h3>
          <p className="mt-1 text-[13px] text-muted">Only memories you confirm from a saved Agent suggestion appear here.</p>
          {!profile.memories.length ? <p className="mt-3 text-sm text-muted">No saved memories.</p> : <ul className="mt-3 space-y-2">
            {profile.memories.map((memory) => <li key={memory.id} className="flex items-start justify-between gap-3 rounded-lg border border-border p-3">
              <p className="min-w-0 text-sm [overflow-wrap:anywhere] whitespace-pre-wrap">{memory.text}</p>
              <Button variant="danger" size="sm" aria-label={`Delete memory: ${memory.text}`} loading={removing === memory.id} disabled={saving || removing != null} onClick={() => remove(memory.id)}>Delete</Button>
            </li>)}
          </ul>}
        </section>
      </div>
    </Card>
  )
}

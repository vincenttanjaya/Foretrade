import { useEffect, useRef, useState, type FormEvent, type ReactNode } from 'react'
import { useSearchParams } from 'react-router'
import { Page, PageHeader } from '../components/layout/AdminShell'
import { useToast } from '../components/toast'
import { Badge } from '../components/ui/Badge'
import { Button } from '../components/ui/Button'
import { Card, CardHeader } from '../components/ui/Card'
import { ErrorState } from '../components/ui/EmptyState'
import { IconAlert, IconCheck, IconExternal, IconEye, IconEyeOff, IconInfo, IconRefresh } from '../components/ui/icons'
import { Input, Label, Select } from '../components/ui/Input'
import { ConfirmDialog, FormError } from '../components/ui/Modal'
import { Skeleton } from '../components/ui/Spinner'
import { api, errorMessage } from '../lib/api'
import { cx } from '../lib/cx'
import { fmtNum, plural } from '../lib/format'
import type { AIDraft, AISettings, ModelInfo, ProviderInfo, SearchDraft, SearchSettings, Secret, Settings, Source, TestResult } from '../lib/types'
import { useLoad } from '../lib/useLoad'

const HELP = 'mt-1.5 text-[12px] leading-snug text-muted'
const LINK = 'text-accent-ink underline-offset-2 hover:underline'

export function SettingsPage() {
  const { data, setData, error, reload } = useLoad((signal) => api.settings(signal), [])
  useChatGPTReturn()

  return (
    <Page>
      <PageHeader
        label="Configuration"
        title="Settings."
        subtitle="The AI model behind chat, forecasts and research, and the keys for news and web search."
      />
      {data ? (
        <div className="max-w-3xl space-y-6">
          {error && <FormError>Couldn't refresh: {error}</FormError>}
          {!data.secretKeySet && <SecretKeyWarning />}
          <AICard settings={data} onSaved={setData} reload={reload} />
          <SearchCard search={data.search} onSaved={setData} />
        </div>
      ) : error ? (
        <ErrorState message={error} onRetry={reload} />
      ) : (
        <div role="status" aria-label="Loading settings" className="max-w-3xl space-y-6">
          <Skeleton className="h-96 rounded-[14px]" />
          <Skeleton className="h-64 rounded-[14px]" />
        </div>
      )}
    </Page>
  )
}

/** The API's /auth/callback lands on /settings?chatgpt=ok|error&message=…: toast it once, then drop the params. */
function useChatGPTReturn() {
  const [params, setParams] = useSearchParams()
  const { toast } = useToast()
  const result = params.get('chatgpt')
  const done = useRef(false) // StrictMode runs the effect twice
  useEffect(() => {
    if (!result || done.current) return
    done.current = true
    if (result === 'ok') toast('Signed in with ChatGPT.')
    else toast(params.get('message')?.slice(0, 200) || "ChatGPT sign-in didn't finish. Try again.", 'error')
    setParams({}, { replace: true })
  }, [result]) // not params/setParams/toast: only a new result matters
}

// ── AI ──────────────────────────────────────────────────────────────────────

type Group = 'Cloud APIs' | 'Local' | 'ChatGPT plan' | 'AWS'
const GROUPS: Group[] = ['Cloud APIs', 'Local', 'ChatGPT plan', 'AWS']
// ponytail: grouped from the preset's own fields; add a `group` to ProviderInfo if a preset ever lands in the wrong one.
const groupOf = (p: ProviderInfo): Group =>
  p.id === 'chatgpt' ? 'ChatGPT plan' : p.wire === 'converse' ? 'AWS' : p.needsKey ? 'Cloud APIs' : 'Local'
/** Under the provider select when the preset has no `note` of its own. */
const GROUP_HELP: Record<Group, string> = {
  'Cloud APIs': 'Pay-as-you-go with your own API key.',
  Local: 'A model server on your own machine: no key, no cost per call.',
  'ChatGPT plan': 'Uses your ChatGPT subscription instead of an API key.',
  AWS: "Amazon Bedrock, with the server's AWS credentials.",
}

type Form = Pick<AISettings, 'provider' | 'model' | 'baseUrl' | 'effort' | 'awsRegion' | 'knowledgeCutoff'>
const formOf = ({ provider, model, baseUrl, effort, awsRegion, knowledgeCutoff }: AISettings): Form => ({
  provider,
  model,
  baseUrl,
  effort,
  awsRegion,
  knowledgeCutoff,
})

/** A secret field's pending change: `edit` shows the input over a saved value, `clear` removes it on save. */
type SecretEdit = { edit: boolean; value: string; clear: boolean }
const KEEP: SecretEdit = { edit: false, value: '', clear: false }
const NONE: Secret = { set: false, hint: '' }
/** The value to send, null to clear, undefined to keep what is saved. */
const secretChange = (e: SecretEdit) => (e.clear ? null : e.value.trim() || undefined)
/** A base URL as the API compares it: trimmed, without trailing slashes. */
const trimUrl = (u: string) => u.trim().replace(/\/+$/, '')
/** Help for a key or token the API drops because the base URL changed. */
const moved = (secret: string) => `The ${secret} isn't sent to a new base URL: paste it again. `

/** What differs from the saved config, plus the key change: the body PUT, /test and /models take. */
function draftOf(saved: AISettings, f: Form, key: SecretEdit): AIDraft {
  const d: AIDraft = {}
  for (const k of Object.keys(f) as (keyof Form)[]) if (f[k] !== saved[k]) Object.assign(d, { [k]: f[k] })
  const v = secretChange(key)
  if (v === null) d.clearKey = true
  else if (v) d.apiKey = v
  return d
}

function AICard({ settings, onSaved, reload }: { settings: Settings; onSaved: (s: Settings) => void; reload: () => void }) {
  const { ai, providers } = settings
  const { toast } = useToast()
  const [form, setForm] = useState(() => formOf(ai))
  const [key, setKey] = useState(KEEP)
  const [models, setModels] = useState<ModelInfo[] | null>(null)
  const [modelsError, setModelsError] = useState<string | null>(null)
  const [busy, setBusy] = useState<'test' | 'models' | 'save' | 'chatgpt' | null>(null)
  const [result, setResult] = useState<TestResult | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [signInError, setSignInError] = useState<string | null>(null)
  const [waiting, setWaiting] = useState(false) // sign-in open in another tab
  const [signingOut, setSigningOut] = useState(false)

  /** Start over from the saved config. */
  const reset = (a: AISettings) => {
    setForm(formOf(a))
    setKey(KEEP)
    setResult(null)
    setError(null)
  }
  // A sign-in, sign-out or reload changed the saved config.
  const saved = JSON.stringify(ai)
  useEffect(() => reset(ai), [saved])

  // Back from the sign-in tab: the callback saved the credential there, so fetch it here.
  useEffect(() => {
    if (!waiting) return
    const back = () => {
      setWaiting(false)
      reload()
    }
    window.addEventListener('focus', back)
    return () => window.removeEventListener('focus', back)
  }, [waiting, reload])

  const p = providers.find((x) => x.id === form.provider)
  // The API keeps the saved key only while the provider and the base URL stay: it never follows a request to another host.
  const keyMoved = form.provider === ai.provider && trimUrl(form.baseUrl) !== trimUrl(ai.baseUrl)
  const draft = () => draftOf(ai, form, key)
  const stale = () => {
    setResult(null)
    setError(null)
  }
  const change = (patch: Partial<Form>) => {
    setForm((f) => ({ ...f, ...patch }))
    stale()
  }

  function pickProvider(id: string) {
    // Another preset's base URL, model and effort rarely fit; going back to the saved one restores them.
    setForm(id === ai.provider ? formOf(ai) : { ...form, provider: id, model: '', baseUrl: '', effort: '', awsRegion: '' })
    setKey(KEEP)
    setModels(null)
    setModelsError(null)
    setSignInError(null)
    stale()
  }

  async function test() {
    setBusy('test')
    setResult(null)
    try {
      setResult(await api.testSettings(draft()))
    } catch (err) {
      setResult({ ok: false, error: errorMessage(err) })
    } finally {
      setBusy(null)
    }
  }

  async function loadModels() {
    setBusy('models')
    setModelsError(null)
    try {
      setModels((await api.models(draft())).models)
    } catch (err) {
      setModels(null)
      setModelsError(errorMessage(err))
    } finally {
      setBusy(null)
    }
  }

  async function save(e: FormEvent) {
    e.preventDefault()
    const d = draft()
    if (!Object.keys(d).length) return toast('Nothing changed.', 'info')
    setBusy('save')
    setError(null)
    try {
      const res = await api.updateSettings({ ai: d })
      onSaved(res)
      reset(res.ai) // even when it reads the same as before (a new key with the same last four characters)
      const label = res.providers.find((x) => x.id === res.ai.provider)?.label ?? res.ai.provider
      toast(res.ai.provider ? `Saved. AI now uses ${[label, res.ai.model].filter(Boolean).join(' · ')}.` : 'Saved. AI is off.')
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setBusy(null)
    }
  }

  async function signIn() {
    const tab = window.open('', '_blank') // opened in the click itself, so popup blockers let it through
    setBusy('chatgpt')
    setSignInError(null)
    try {
      const { url } = await api.chatgptStart()
      if (tab) {
        tab.opener = null // the sign-in page gets no handle on this one
        tab.location.href = url
        setWaiting(true)
      } else window.location.assign(url) // blocked anyway: sign in here; the callback comes back to this page
    } catch (err) {
      tab?.close()
      setSignInError(errorMessage(err))
    } finally {
      setBusy(null)
    }
  }

  const ro = ai.readOnly

  return (
    <Card>
      <CardHeader
        label="Chat, forecasts, research"
        title="AI provider"
        actions={
          <>
            {ro && <SourceBadge source="env" />}
            <StatusBadge status={ai.status} />
          </>
        }
      />
      <form onSubmit={save} aria-label="AI provider" className="px-5 pb-5">
        {ro && (
          <Note className="mb-5">
            <span className="font-semibold">Set by environment.</span> The server's <Code>.env</Code> sets{' '}
            <Code>AI_PROVIDER</Code>, so this card is read-only. Edit <Code>.env</Code> and restart the app to change it, or
            remove <Code>AI_PROVIDER</Code> to manage AI here. Test connection still works.
          </Note>
        )}
        {ai.error && ai.error !== ai.chatgpt.error && ( // the ChatGPT box shows its own error
          <Note tone="error" className="mb-5">
            {ai.error}
          </Note>
        )}

        <fieldset disabled={ro} className="min-w-0">
          <Label htmlFor="ai-provider">Provider</Label>
          <Select id="ai-provider" value={form.provider} onChange={(e) => pickProvider(e.target.value)} aria-describedby="ai-provider-help">
            <option value="">Off</option>
            {GROUPS.map((g) => {
              const list = providers.filter((x) => groupOf(x) === g)
              return (
                list.length > 0 && (
                  <optgroup key={g} label={g}>
                    {list.map((x) => (
                      <option key={x.id} value={x.id}>
                        {x.label}
                      </option>
                    ))}
                  </optgroup>
                )
              )
            })}
          </Select>
          <p id="ai-provider-help" className={HELP}>
            {p
              ? (p.note ?? GROUP_HELP[groupOf(p)])
              : 'AI is off: chat, forecasts, research and the arena wait for a provider. Paper trading works without one.'}
          </p>
        </fieldset>

        {p?.id === 'chatgpt' && (
          <section aria-label="ChatGPT sign-in" className="mt-5 rounded-lg border border-border bg-surface-2/50 p-4">
            {ai.chatgpt.signedIn ? (
              <div className="flex flex-wrap items-center gap-3">
                <span className="grid size-6 shrink-0 place-items-center rounded-full bg-up/12 text-up">
                  <IconCheck size={14} strokeWidth={2.2} />
                </span>
                <p className="min-w-0 text-sm">
                  Signed in{ai.chatgpt.email && <> as <span className="font-medium break-all">{ai.chatgpt.email}</span></>}
                </p>
                <Button variant="secondary" size="sm" className="ml-auto" onClick={() => setSigningOut(true)}>
                  Sign out
                </Button>
              </div>
            ) : (
              <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
                <Button loading={busy === 'chatgpt'} disabled={!!busy} onClick={signIn}>
                  Sign in with ChatGPT <IconExternal size={15} />
                </Button>
                <p role="status" className="min-w-0 flex-1 basis-56 text-[12.5px] leading-snug text-muted">
                  {waiting
                    ? 'Finish signing in in the new tab. This page updates when you come back.'
                    : 'Opens OpenAI in a new tab. Open this console at localhost or 127.0.0.1 for it; on a server, use an SSH tunnel.'}
                </p>
              </div>
            )}
            {ai.chatgpt.error && <FormError>{ai.chatgpt.error}</FormError>}
            {signInError && <FormError>{signInError}</FormError>}
            <p className="mt-3 text-[12.5px] leading-snug text-muted">
              A ChatGPT plan belongs to the person who signs in, so Foretrade runs in owner-only mode with it: only admins can
              use AI features. For a shared instance, use an API key.
            </p>
          </section>
        )}

        {p && (
          <fieldset disabled={ro} className="mt-5 grid min-w-0 gap-5 sm:grid-cols-2">
            {p.id !== 'chatgpt' && p.wire !== 'converse' && (
              <>
                <SecretField
                  id="ai-key"
                  label="API key"
                  secret={form.provider === ai.provider && !keyMoved ? ai.apiKey : NONE}
                  edit={key}
                  onEdit={(e) => {
                    setKey(e)
                    stale()
                  }}
                  placeholder={p.needsKey ? 'Paste your key' : 'Optional'}
                  help={
                    <>
                      {keyMoved && ai.apiKey.set && moved('saved key')}
                      {p.keyUrl ? (
                        <>
                          Get one at{' '}
                          <a href={p.keyUrl} target="_blank" rel="noreferrer" className={LINK}>
                            {p.keyUrl.replace(/^https?:\/\//, '').replace(/\/$/, '')}
                          </a>
                          .{' '}
                        </>
                      ) : (
                        !p.needsKey && 'Only if your server asks for one. '
                      )}
                      Stored encrypted and never shown again.
                    </>
                  }
                />
                <div className="sm:col-span-2">
                  <Label htmlFor="ai-base">Base URL</Label>
                  <Input
                    id="ai-base"
                    type="url"
                    inputMode="url"
                    spellCheck={false}
                    required={!p.defaultBaseUrl}
                    placeholder={p.defaultBaseUrl || 'http://host.docker.internal:8000/v1'}
                    value={form.baseUrl}
                    onChange={(e) => change({ baseUrl: e.target.value })}
                    aria-describedby="ai-base-help"
                    className="font-mono"
                  />
                  <p id="ai-base-help" className={HELP}>
                    {!p.defaultBaseUrl
                      ? 'Required: the address of an OpenAI-compatible server, usually ending in /v1.'
                      : 'Where the API lives. Leave empty for the default shown.'}
                    {groupOf(p) === 'Local' &&
                      ' From Docker, host.docker.internal is the computer running Docker; on Linux, make the model server listen on 0.0.0.0 (Ollama: OLLAMA_HOST=0.0.0.0).'}
                  </p>
                </div>
              </>
            )}

            {p.wire === 'converse' && (
              <div className="sm:col-span-2">
                <Label htmlFor="ai-region">AWS region</Label>
                <Input
                  id="ai-region"
                  spellCheck={false}
                  placeholder="us-east-1"
                  value={form.awsRegion}
                  onChange={(e) => change({ awsRegion: e.target.value })}
                  aria-describedby="ai-region-help"
                  className="font-mono sm:max-w-60"
                />
                <p id="ai-region-help" className={HELP}>
                  Credentials come from the server's AWS environment: <Code>AWS_PROFILE</Code>, access keys or{' '}
                  <Code>AWS_BEARER_TOKEN_BEDROCK</Code>.
                </p>
              </div>
            )}

            <div className="sm:col-span-2">
              <Label htmlFor="ai-model">Model</Label>
              <div className="flex flex-col gap-2 sm:flex-row">
                <Input
                  id="ai-model"
                  list="ai-models"
                  autoComplete="off"
                  spellCheck={false}
                  required={!p.defaultModel}
                  placeholder={p.defaultModel || 'Load the list or type a model id'}
                  value={form.model}
                  onChange={(e) => change({ model: e.target.value })}
                  aria-describedby="ai-model-help"
                  className="font-mono sm:flex-1"
                />
                <Button variant="secondary" loading={busy === 'models'} disabled={!!busy} onClick={loadModels}>
                  {busy !== 'models' && <IconRefresh size={16} />} Load models
                </Button>
              </div>
              <datalist id="ai-models">
                {models?.map((m) => (
                  <option key={m.id} value={m.id}>
                    {m.label}
                  </option>
                ))}
              </datalist>
              {modelsError ? (
                <FormError id="ai-model-help">{modelsError}</FormError>
              ) : (
                <p id="ai-model-help" role="status" className={HELP}>
                  {models
                    ? `${plural(models.length, 'model')} loaded: pick one from the list or type any id.`
                    : p.defaultModel
                      ? 'Leave empty for the default shown, or load the list from the provider.'
                      : 'Load the list from the provider, or type a model id.'}
                </p>
              )}
            </div>

            <div>
              <span className="label mb-1.5 block">Reasoning</span>
              <label className="flex h-10 items-center gap-2 text-sm">
                <input
                  type="checkbox"
                  checked={form.effort === 'on' || (form.effort === '' && p.effortDefault)}
                  onChange={(e) => change({ effort: e.target.checked ? 'on' : 'off' })}
                  aria-describedby="ai-effort-help"
                  className="size-4 accent-[var(--accent-ink)]"
                />
                Send reasoning effort
              </label>
              <p id="ai-effort-help" className={HELP}>
                Lets the model think longer on hard tasks. Turn it off if the model rejects it.
              </p>
            </div>

            <div>
              <Label htmlFor="ai-cutoff">Knowledge cutoff</Label>
              <Input
                id="ai-cutoff"
                type="date"
                value={form.knowledgeCutoff}
                onChange={(e) => change({ knowledgeCutoff: e.target.value })}
                aria-describedby="ai-cutoff-help"
              />
              <p id="ai-cutoff-help" className={HELP}>
                The model's training-data cutoff. Arena backtests that start before it are flagged: the model may know how they end.
              </p>
            </div>
          </fieldset>
        )}

        {result &&
          (result.ok ? (
            <p role="status" className="mt-4 flex items-start gap-2 text-[13.5px] leading-snug text-up">
              <IconCheck size={16} className="mt-px shrink-0" />
              <span className="min-w-0 break-words">
                Connected: <span className="font-mono">{result.model || p?.defaultModel || 'the model'}</span> answered in {fmtNum(result.latencyMs)} ms.
              </span>
            </p>
          ) : (
            <FormError>Test failed: {result.error}</FormError>
          ))}
        {error && <FormError>{error}</FormError>}

        <div className="mt-6 flex flex-wrap items-center justify-end gap-2 border-t border-border pt-5">
          {!ro && form.provider && (
            <p className="mr-auto min-w-0 basis-64 text-[12px] leading-snug text-muted">
              Save tests the connection first. Nothing is saved if the test fails.
            </p>
          )}
          <Button variant="secondary" loading={busy === 'test'} disabled={!!busy || !form.provider} onClick={test}>
            Test connection
          </Button>
          {!ro && (
            <Button type="submit" loading={busy === 'save'} disabled={!!busy}>
              Save
            </Button>
          )}
        </div>
      </form>

      <ConfirmDialog
        open={signingOut}
        onClose={() => setSigningOut(false)}
        label="ChatGPT plan"
        title="Sign out of ChatGPT?"
        body="Foretrade forgets the sign-in and asks OpenAI to revoke it. AI stops until you sign in again or pick another provider."
        confirmLabel="Sign out"
        onConfirm={async () => {
          onSaved(await api.chatgptSignOut())
          toast('Signed out of ChatGPT.')
        }}
      />
    </Card>
  )
}

function StatusBadge({ status }: { status: AISettings['status'] }) {
  const [tone, text] = ({ off: ['neutral', 'Off'], ready: ['up', 'Ready'], error: ['down', 'Error'] } as const)[status]
  return (
    <Badge tone={tone}>
      <span className="sr-only">AI status: </span>
      {text}
    </Badge>
  )
}

// ── Search ──────────────────────────────────────────────────────────────────

function SearchCard({ search: s, onSaved }: { search: SearchSettings; onSaved: (s: Settings) => void }) {
  const { toast } = useToast()
  const [brave, setBrave] = useState(KEEP)
  const [token, setToken] = useState(KEEP)
  const [base, setBase] = useState(s.crawlBase)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const reset = (x: SearchSettings) => {
    setBrave(KEEP)
    setToken(KEEP)
    setBase(x.crawlBase)
    setError(null)
  }
  const saved = JSON.stringify(s)
  useEffect(() => reset(s), [saved])
  // Like the AI key: the API drops the token when the crawl base URL changes.
  const baseMoved = trimUrl(base) !== trimUrl(s.crawlBase)

  async function save(e: FormEvent) {
    e.preventDefault()
    const d: SearchDraft = {}
    const b = secretChange(brave)
    if (b === null) d.clearBraveApiKey = true
    else if (b) d.braveApiKey = b
    const t = secretChange(token)
    if (t === null) d.clearCrawlToken = true
    else if (t) d.crawlToken = t
    if (baseMoved) d.crawlBase = trimUrl(base)
    if (!Object.keys(d).length) return toast('Nothing changed.', 'info')
    setBusy(true)
    setError(null)
    try {
      const res = await api.updateSettings({ search: d })
      onSaved(res)
      reset(res.search)
      toast('Saved search settings.')
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card>
      <CardHeader label="News and research" title="Search" />
      <form onSubmit={save} aria-label="Search" className="px-5 pb-5">
        {s.error && (
          <Note tone="error" className="mb-5">
            {s.error}
          </Note>
        )}
        <div className="grid gap-5 sm:grid-cols-2">
          <SecretField
            id="brave-key"
            label="Brave Search API key"
            secret={s.braveApiKey}
            source={s.braveApiKey.source}
            edit={brave}
            onEdit={setBrave}
            placeholder="Paste your key"
            help={
              <>
                Live news and web search for chat, forecasts and research; without it, search is off. The free plan is enough
                to start:{' '}
                <a href="https://brave.com/search/api/" target="_blank" rel="noreferrer" className={LINK}>
                  brave.com/search/api
                </a>
                .
              </>
            }
          />
          <div className="sm:col-span-2">
            <div className="mb-1.5 flex items-center gap-2">
              <label htmlFor="crawl-base" className="label">
                crawl4ai base URL
              </label>
              <SourceBadge source={s.crawlBaseSource} />
            </div>
            <Input
              id="crawl-base"
              type="url"
              inputMode="url"
              spellCheck={false}
              placeholder="http://host.docker.internal:11235"
              value={base}
              onChange={(e) => setBase(e.target.value)}
              aria-describedby="crawl-base-help"
              className="font-mono"
            />
            <p id="crawl-base-help" className={HELP}>
              Optional. A crawl4ai server reads articles that need JavaScript or pass bot checks; without one, pages are fetched
              directly.{s.crawlBaseSource === 'env' && ' Set in .env; a value saved here takes precedence.'}
            </p>
          </div>
          <SecretField
            id="crawl-token"
            label="crawl4ai token"
            secret={baseMoved ? NONE : s.crawlToken}
            source={baseMoved ? '' : s.crawlToken.source}
            edit={token}
            onEdit={setToken}
            placeholder="Optional"
            help={
              <>
                {baseMoved && s.crawlToken.set && moved('token')}The crawl4ai server's bearer token. It is used only together with a
                base URL.
              </>
            }
          />
        </div>
        {error && <FormError>{error}</FormError>}
        <div className="mt-6 flex justify-end border-t border-border pt-5">
          <Button type="submit" loading={busy}>
            Save
          </Button>
        </div>
      </form>
    </Card>
  )
}

// ── Pieces ──────────────────────────────────────────────────────────────────

/**
 * A write-only secret: a saved one shows as "Set ••••a1b2" with Replace and Clear (Clear only for a value saved here:
 * one from .env can be overridden, not removed); otherwise a password input with show/hide.
 */
function SecretField({
  id,
  label,
  secret,
  source,
  edit,
  onEdit,
  placeholder,
  help,
}: {
  id: string
  label: string
  secret: Secret
  source?: Source
  edit: SecretEdit
  onEdit: (e: SecretEdit) => void
  placeholder: string
  help: ReactNode
}) {
  const [shown, setShown] = useState(false)
  const input = !secret.set || edit.edit
  useEffect(() => {
    if (!input) setShown(false) // a later Replace starts hidden again
  }, [input])
  return (
    <div className="sm:col-span-2">
      <div className="mb-1.5 flex items-center gap-2">
        <label id={`${id}-label`} htmlFor={input ? id : undefined} className="label">
          {label}
        </label>
        {source && <SourceBadge source={source} />}
      </div>
      {input ? (
        <div className="flex gap-2">
          <div className="relative min-w-0 flex-1">
            <Input
              id={id}
              type={shown ? 'text' : 'password'}
              autoComplete="new-password" // "off" is ignored on password fields: keep the admin login out of key fields
              data-1p-ignore
              data-lpignore="true"
              data-bwignore
              spellCheck={false}
              autoFocus={edit.edit} // Replace was clicked: it is gone, so focus moves here
              placeholder={placeholder}
              value={edit.value}
              onChange={(e) => onEdit({ edit: true, value: e.target.value, clear: false })} // typed text replaces, even after Clear
              aria-describedby={`${id}-help`}
              className="pr-10 font-mono"
            />
            <button
              type="button"
              onClick={() => setShown(!shown)}
              aria-label={`${shown ? 'Hide' : 'Show'} ${label}`}
              className="absolute top-1/2 right-1.5 grid size-7 -translate-y-1/2 place-items-center rounded-md text-muted transition-colors hover:bg-surface-2 hover:text-text"
            >
              {shown ? <IconEyeOff size={16} /> : <IconEye size={16} />}
            </button>
          </div>
          {secret.set && (
            <Button variant="ghost" aria-label={`Keep the saved ${label}`} onClick={() => onEdit(KEEP)}>
              Keep saved
            </Button>
          )}
        </div>
      ) : (
        <div
          role="group"
          aria-labelledby={`${id}-label`}
          aria-describedby={`${id}-help`}
          className="flex min-h-10 flex-wrap items-center gap-x-3 gap-y-1 rounded-lg border border-border bg-surface-2 py-1 pr-1 pl-3"
        >
          <span className="min-w-0 text-sm">
            {edit.clear ? (
              <span className="font-medium text-text">Removed when you save</span>
            ) : (
              <>
                Set <span className="font-mono text-muted">••••{secret.hint.replace(/^…/, '')}</span>
              </>
            )}
          </span>
          <span className="ml-auto flex gap-1">
            {!edit.clear && (
              <Button size="sm" variant="secondary" aria-label={`Replace ${label}`} onClick={() => onEdit({ ...KEEP, edit: true })}>
                Replace
              </Button>
            )}
            {source !== 'env' && (
              // One button that flips between Clear and Undo, so focus stays on it.
              <Button size="sm" variant="ghost" aria-label={`${edit.clear ? 'Undo clear' : 'Clear'} ${label}`} onClick={() => onEdit(edit.clear ? KEEP : { ...KEEP, clear: true })}>
                {edit.clear ? 'Undo' : 'Clear'}
              </Button>
            )}
          </span>
        </div>
      )}
      <p id={`${id}-help`} className={HELP}>
        {help}
      </p>
    </div>
  )
}

function SourceBadge({ source }: { source: Source }) {
  if (!source) return null
  return (
    <Badge tone={source === 'env' ? 'outline' : 'accent'} title={source === 'env' ? "From the server's .env" : 'Saved in this console'}>
      {source === 'env' ? 'env' : 'saved'}
    </Badge>
  )
}

function SecretKeyWarning() {
  return (
    <Note tone="warn">
      <p>
        <span className="font-semibold">SECRET_KEY isn't set.</span> The server encrypts saved keys and the ChatGPT sign-in
        with it, so it can't save them yet. Set <Code>SECRET_KEY</Code> in <Code>.env</Code> to 64 hex characters (
        <Code>openssl rand -hex 32</Code> makes one), then restart the app.
      </p>
      <p className="mt-1.5 text-muted">Keep it: if it changes, saved keys can't be decrypted and must be entered again.</p>
    </Note>
  )
}

const NOTE_TONES = {
  info: ['border-border bg-surface-2', 'text-muted'],
  warn: ['border-accent/40 bg-accent-soft', 'text-accent-ink'],
  error: ['border-down/25 bg-down/6', 'text-down'],
}

/** A boxed note at the top of a card: info (neutral), warn (gold) or error (red). */
function Note({ tone = 'info', className, children }: { tone?: keyof typeof NOTE_TONES; className?: string; children: ReactNode }) {
  const [box, icon] = NOTE_TONES[tone]
  return (
    <div
      role={tone === 'info' ? undefined : 'alert'}
      className={cx('flex items-start gap-2.5 rounded-lg border px-3 py-2.5 text-[13.5px] leading-snug text-text', box, className)}
    >
      {tone === 'info' ? <IconInfo size={16} className={cx('mt-px shrink-0', icon)} /> : <IconAlert size={16} className={cx('mt-px shrink-0', icon)} />}
      <div className="min-w-0 break-words">{children}</div>
    </div>
  )
}

const Code = ({ children }: { children: ReactNode }) => <code className="font-mono text-[12.5px] break-all">{children}</code>

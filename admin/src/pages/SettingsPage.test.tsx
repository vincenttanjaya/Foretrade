import { act, screen, waitFor, within } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { ApiError, api } from '../lib/api'
import type { Settings } from '../lib/types'
import * as fx from '../test/fixtures'
import { currentPath, renderPage, stubSession } from '../test/utils'
import { SettingsPage } from './SettingsPage'

const NO_KEY = { set: false, hint: '' }

function open(s: Settings = fx.settings(), route = '/settings') {
  stubSession()
  const load = vi.spyOn(api, 'settings').mockResolvedValue(s)
  return { ...renderPage(<SettingsPage />, { route, path: '/settings' }), load }
}
const ready = () => screen.findByRole('heading', { name: 'AI provider' })
const card = (title: string) => screen.getByRole('heading', { name: title }).closest('.card') as HTMLElement
const aiForm = () => screen.getByRole('form', { name: 'AI provider' })
const searchForm = () => screen.getByRole('form', { name: 'Search' })
const save = (form: HTMLElement) => within(form).getByRole('button', { name: 'Save' })

describe('SettingsPage: states', () => {
  it('shows a ready provider with its saved key, model and help, and the search keys with their sources', async () => {
    open()
    await ready()
    expect(screen.getByRole('heading', { level: 1, name: 'Settings.' })).toBeInTheDocument()
    expect(card('AI provider')).toHaveTextContent('AI status: Ready')
    expect(screen.getByLabelText('Provider')).toHaveValue('openrouter')
    expect(screen.getByRole('group', { name: 'API key' })).toHaveTextContent('Set ••••a1b2')
    expect(screen.getByRole('link', { name: 'openrouter.ai/keys' })).toHaveAttribute('href', 'https://openrouter.ai/keys')
    expect(screen.getByLabelText('Base URL')).toHaveAttribute('placeholder', 'https://openrouter.ai/api/v1')
    expect(screen.getByLabelText('Model')).toHaveValue('qwen/qwen4-72b')
    expect(screen.getByLabelText('Send reasoning effort')).toBeChecked() // the preset's default
    expect(screen.getByLabelText('Knowledge cutoff')).toHaveValue('2026-06-30')
    expect(screen.queryByText(/SECRET_KEY/)).not.toBeInTheDocument()

    // An env key can be overridden here but not cleared; a token saved here can be.
    const brave = screen.getByRole('group', { name: 'Brave Search API key' })
    expect(brave).toHaveTextContent('Set ••••9f3c')
    expect(within(brave).getByRole('button', { name: 'Replace Brave Search API key' })).toBeInTheDocument()
    expect(within(brave).queryByRole('button', { name: /Clear/ })).not.toBeInTheDocument()
    expect(within(card('Search')).getByText('env')).toHaveAttribute('title', "From the server's .env")
    expect(within(card('Search')).getByText('saved')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Clear crawl4ai token' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'brave.com/search/api' })).toHaveAttribute('href', 'https://brave.com/search/api/')
  })

  it('shows AI off: only the provider select, and nothing to test', async () => {
    open(fx.settings({ provider: '', model: '', status: 'off', apiKey: NO_KEY }))
    await ready()
    expect(card('AI provider')).toHaveTextContent('AI status: Off')
    expect(screen.getByLabelText('Provider')).toHaveValue('')
    expect(screen.getByText(/AI is off: chat, forecasts, research and the arena wait for a provider/)).toBeInTheDocument()
    expect(screen.queryByLabelText('Model')).not.toBeInTheDocument()
    expect(screen.queryByLabelText('API key')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Test connection' })).toBeDisabled()
  })

  it('shows the error status and the server message, with an empty key input', async () => {
    open(fx.settings({ status: 'error', error: "The stored key can't be decrypted. Enter it again.", apiKey: NO_KEY }))
    await ready()
    expect(card('AI provider')).toHaveTextContent('AI status: Error')
    expect(within(aiForm()).getByRole('alert')).toHaveTextContent("The stored key can't be decrypted. Enter it again.")
    expect(screen.getByLabelText('API key')).toHaveValue('')
    expect(screen.getByLabelText('API key')).toHaveAttribute('placeholder', 'Paste your key')
  })

  it('is read-only when the environment sets the provider, and can still test it', async () => {
    const { user } = open(fx.settings({ provider: 'anthropic', model: '', source: 'env', readOnly: true }))
    const test = vi.spyOn(api, 'testSettings').mockResolvedValue({ ok: true, provider: 'anthropic', model: 'claude-sonnet-5-5', latencyMs: 640 })
    await ready()
    expect(within(aiForm()).getByText('Set by environment.')).toBeInTheDocument()
    expect(within(card('AI provider')).getByText('env')).toBeInTheDocument()
    for (const label of ['Provider', 'Model', 'Base URL', 'Knowledge cutoff', 'Send reasoning effort']) expect(screen.getByLabelText(label)).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Replace API key' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Load models' })).toBeDisabled()
    expect(within(aiForm()).queryByRole('button', { name: 'Save' })).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Test connection' }))
    expect(test).toHaveBeenCalledWith({}) // the environment's config, as is
    expect(await screen.findByText(/answered in 640 ms/)).toHaveTextContent('Connected: claude-sonnet-5-5 answered in 640 ms.')
  })

  it('shows the signed-in ChatGPT account and the owner-only note, and signs out after confirming', async () => {
    const chatgpt = { provider: 'chatgpt', model: '', apiKey: NO_KEY }
    const { user } = open(fx.settings({ ...chatgpt, chatgpt: { signedIn: true, email: 'owner@example.com', error: '' } }))
    const signOut = vi.spyOn(api, 'chatgptSignOut').mockResolvedValue(fx.settings({ ...chatgpt, status: 'error' }))
    await ready()
    const box = screen.getByRole('region', { name: 'ChatGPT sign-in' })
    expect(box).toHaveTextContent('Signed in as owner@example.com')
    expect(box).toHaveTextContent('owner-only mode with it: only admins can use AI features')
    expect(screen.queryByLabelText('API key')).not.toBeInTheDocument()
    expect(screen.queryByLabelText('Base URL')).not.toBeInTheDocument()
    expect(screen.getByLabelText('Model')).toBeInTheDocument()

    await user.click(within(box).getByRole('button', { name: 'Sign out' }))
    const dialog = screen.getByRole('dialog', { name: 'Sign out of ChatGPT?' })
    expect(signOut).not.toHaveBeenCalled()
    await user.click(within(dialog).getByRole('button', { name: 'Sign out' }))
    expect(signOut).toHaveBeenCalledTimes(1)
    expect(await screen.findByText('Signed out of ChatGPT.')).toBeInTheDocument()
    expect(within(box).getByRole('button', { name: 'Sign in with ChatGPT' })).toBeInTheDocument()
    expect(card('AI provider')).toHaveTextContent('AI status: Error')
  })

  it('shows a ChatGPT credential problem', async () => {
    open(fx.settings({ provider: 'chatgpt', model: '', apiKey: NO_KEY, status: 'error', chatgpt: { signedIn: false, email: '', error: 'Sign in again: the session expired.' } }))
    await ready()
    expect(within(screen.getByRole('region', { name: 'ChatGPT sign-in' })).getByRole('alert')).toHaveTextContent('Sign in again: the session expired.')
  })

  it('shows an expired ChatGPT sign-in once, and a search key that cannot be decrypted', async () => {
    const msg = 'The ChatGPT sign-in has ended: sign in again.'
    const base = fx.settings({ provider: 'chatgpt', model: '', apiKey: NO_KEY, status: 'error', error: msg, chatgpt: { signedIn: false, email: '', error: msg } })
    open({ ...base, search: { ...base.search, error: "A stored search key can't be decrypted" } })
    await ready()
    expect(screen.getAllByRole('alert').filter((el) => el.textContent?.includes(msg))).toHaveLength(1)
    expect(within(searchForm()).getByText("A stored search key can't be decrypted")).toBeInTheDocument()
  })

  it('explains SECRET_KEY when the server has none', async () => {
    open(fx.settings({}, { secretKeySet: false }))
    await ready()
    const warning = screen.getAllByRole('alert').find((el) => el.textContent?.includes('SECRET_KEY'))!
    expect(warning).toHaveTextContent("SECRET_KEY isn't set.")
    expect(warning).toHaveTextContent('openssl rand -hex 32')
  })

  it('shows a load error with Retry', async () => {
    stubSession()
    const load = vi.spyOn(api, 'settings').mockRejectedValueOnce(new ApiError(500, 'internal', 'Database unavailable.'))
    load.mockResolvedValue(fx.settings())
    const { user } = renderPage(<SettingsPage />)
    expect(await screen.findByRole('alert')).toHaveTextContent('Database unavailable.')
    await user.click(screen.getByRole('button', { name: 'Retry' }))
    await ready()
  })
})

describe('SettingsPage: AI provider', () => {
  it('groups the presets, shows the fields each one needs, and restores the saved ones on the way back', async () => {
    const { user } = open()
    await ready()
    const provider = screen.getByLabelText('Provider')
    const groups = within(provider).getAllByRole('group')
    expect(groups.map((g) => [g.getAttribute('label'), within(g).getAllByRole('option').map((o) => o.textContent)])).toEqual([
      ['Cloud APIs', ['Anthropic', 'OpenRouter']],
      ['Local', ['Ollama', 'Custom server']],
      ['ChatGPT plan', ['ChatGPT plan']],
      ['AWS', ['Amazon Bedrock']],
    ])

    await user.selectOptions(provider, 'ollama')
    expect(screen.getByLabelText('API key')).toHaveAttribute('placeholder', 'Optional') // the saved key was OpenRouter's
    expect(screen.getByText(/Only if your server asks for one/)).toBeInTheDocument()
    expect(screen.getByLabelText('Base URL')).toHaveAttribute('placeholder', 'http://host.docker.internal:11434/v1')
    expect(screen.getByText(/OLLAMA_HOST=0\.0\.0\.0/)).toBeInTheDocument()
    expect(screen.getByLabelText('Model')).toHaveValue('')
    expect(screen.getByLabelText('Model')).toBeRequired()
    expect(screen.getByLabelText('Send reasoning effort')).not.toBeChecked()

    await user.selectOptions(provider, 'custom')
    expect(screen.getByLabelText('Base URL')).toBeRequired()
    expect(screen.getByText('Any OpenAI-compatible server: vLLM, llama.cpp, a gateway.')).toBeInTheDocument() // the preset's note

    await user.selectOptions(provider, 'bedrock')
    expect(screen.getByLabelText('AWS region')).toHaveAttribute('placeholder', 'us-east-1')
    expect(screen.queryByLabelText('API key')).not.toBeInTheDocument()
    expect(screen.queryByLabelText('Base URL')).not.toBeInTheDocument()
    expect(screen.getByLabelText('Model')).not.toBeRequired() // the preset has a default

    await user.selectOptions(provider, 'chatgpt')
    expect(screen.getByRole('button', { name: 'Sign in with ChatGPT' })).toBeInTheDocument()
    expect(screen.queryByLabelText('API key')).not.toBeInTheDocument()

    await user.selectOptions(provider, 'openrouter')
    expect(screen.getByLabelText('Model')).toHaveValue('qwen/qwen4-72b')
    expect(screen.getByRole('group', { name: 'API key' })).toHaveTextContent('Set ••••a1b2')
  })

  it('loads models into the combo box, which still takes free text, and shows a failure', async () => {
    const { user } = open()
    const models = vi.spyOn(api, 'models').mockRejectedValueOnce(new ApiError(400, 'bad_request', 'OpenRouter: invalid API key.'))
    models.mockResolvedValue({ models: [{ id: 'a/one', label: 'One' }, { id: 'b/two', label: 'Two' }] })
    await ready()
    await user.selectOptions(screen.getByLabelText('Provider'), 'ollama')
    await user.type(screen.getByLabelText('Base URL'), 'http://192.168.1.20:11434/v1')
    await user.click(screen.getByRole('button', { name: 'Load models' }))
    expect(models).toHaveBeenCalledWith({ provider: 'ollama', model: '', baseUrl: 'http://192.168.1.20:11434/v1' })
    expect(await within(aiForm()).findByRole('alert')).toHaveTextContent('OpenRouter: invalid API key.')

    await user.click(screen.getByRole('button', { name: 'Load models' }))
    expect(await screen.findByText('2 models loaded: pick one from the list or type any id.')).toBeInTheDocument()
    expect(within(aiForm()).queryByRole('alert')).not.toBeInTheDocument()
    const model = screen.getByLabelText('Model')
    expect(model).toHaveAttribute('list', 'ai-models')
    expect([...document.querySelectorAll('#ai-models option')].map((o) => [o.getAttribute('value'), o.textContent])).toEqual([
      ['a/one', 'One'],
      ['b/two', 'Two'],
    ])
    await user.type(model, 'my/own-model')
    expect(model).toHaveValue('my/own-model')
  })

  it('tests the draft with a replacement key, showing the latency or the provider error', async () => {
    const { user } = open()
    const test = vi.spyOn(api, 'testSettings').mockResolvedValueOnce({ ok: false, error: '401: invalid API key' })
    test.mockResolvedValueOnce({ ok: true, provider: 'openrouter', model: 'qwen/qwen4-72b', latencyMs: 1234 })
    await ready()
    await user.click(screen.getByRole('button', { name: 'Replace API key' }))
    const key = screen.getByLabelText('API key')
    expect(key).toHaveFocus()
    expect(key).toHaveAttribute('type', 'password')
    await user.type(key, 'sk-or-v1-new-key-0000')
    await user.click(screen.getByRole('button', { name: 'Show API key' }))
    expect(key).toHaveAttribute('type', 'text')
    await user.click(screen.getByRole('button', { name: 'Hide API key' }))
    expect(key).toHaveAttribute('type', 'password')

    await user.click(screen.getByRole('button', { name: 'Test connection' }))
    expect(test).toHaveBeenCalledWith({ apiKey: 'sk-or-v1-new-key-0000' })
    expect(await within(aiForm()).findByRole('alert')).toHaveTextContent('Test failed: 401: invalid API key')
    await user.click(screen.getByRole('button', { name: 'Test connection' }))
    expect(await screen.findByText(/answered in 1,234 ms/)).toHaveTextContent('Connected: qwen/qwen4-72b answered in 1,234 ms.')

    await user.type(screen.getByLabelText('Model'), 'x') // any edit makes the result stale
    expect(screen.queryByText(/answered in/)).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Keep the saved API key' }))
    expect(screen.getByRole('group', { name: 'API key' })).toHaveTextContent('Set ••••a1b2')
  })

  it('reports a test request that never reached the API', async () => {
    const { user } = open()
    vi.spyOn(api, 'testSettings').mockRejectedValue(new ApiError(0, 'network', "Can't reach the API."))
    await ready()
    await user.click(screen.getByRole('button', { name: 'Test connection' }))
    expect(await within(aiForm()).findByRole('alert')).toHaveTextContent("Test failed: Can't reach the API.")
  })

  it('saves only what changed, shows a failed save verbatim, then the saved provider', async () => {
    const { user } = open()
    const update = vi.spyOn(api, 'updateSettings').mockRejectedValueOnce(new ApiError(400, 'bad_request', 'openrouter: model "nope" not found'))
    update.mockResolvedValueOnce(fx.settings({ model: 'anthropic/claude-sonnet-5.5' }))
    await ready()
    await user.click(save(aiForm()))
    expect(await screen.findByText('Nothing changed.')).toBeInTheDocument()
    expect(update).not.toHaveBeenCalled()

    const model = screen.getByLabelText('Model')
    await user.clear(model)
    await user.type(model, 'nope')
    await user.click(save(aiForm()))
    expect(update).toHaveBeenCalledWith({ ai: { model: 'nope' } })
    expect(await within(aiForm()).findByRole('alert')).toHaveTextContent('openrouter: model "nope" not found')
    expect(model).toHaveValue('nope') // kept, to fix

    await user.clear(model)
    await user.type(model, 'anthropic/claude-sonnet-5.5')
    await user.click(screen.getByLabelText('Send reasoning effort'))
    await user.click(save(aiForm()))
    expect(update).toHaveBeenLastCalledWith({ ai: { model: 'anthropic/claude-sonnet-5.5', effort: 'off' } })
    expect(await screen.findByText('Saved. AI now uses OpenRouter · anthropic/claude-sonnet-5.5.')).toBeInTheDocument()
    expect(within(aiForm()).queryByRole('alert')).not.toBeInTheDocument()
    expect(screen.getByLabelText('Send reasoning effort')).toBeChecked() // the form starts over from the saved config
  })

  it('switches AI off', async () => {
    const { user } = open()
    const update = vi.spyOn(api, 'updateSettings').mockResolvedValue(fx.settings({ provider: '', model: '', status: 'off', apiKey: NO_KEY }))
    await ready()
    await user.selectOptions(screen.getByLabelText('Provider'), '')
    await user.click(save(aiForm()))
    expect(update).toHaveBeenCalledWith({ ai: { provider: '', model: '' } })
    expect(await screen.findByText('Saved. AI is off.')).toBeInTheDocument()
    expect(card('AI provider')).toHaveTextContent('AI status: Off')
  })

  it('clears the saved key on save; Undo keeps it', async () => {
    const { user } = open()
    const update = vi.spyOn(api, 'updateSettings').mockResolvedValue(fx.settings({ apiKey: NO_KEY, status: 'error' }))
    await ready()
    await user.click(screen.getByRole('button', { name: 'Clear API key' }))
    expect(screen.getByRole('group', { name: 'API key' })).toHaveTextContent('Removed when you save')
    expect(screen.queryByRole('button', { name: 'Replace API key' })).not.toBeInTheDocument()
    const undo = screen.getByRole('button', { name: 'Undo clear API key' })
    expect(undo).toHaveFocus() // the same button, relabelled
    await user.click(undo)
    expect(screen.getByRole('group', { name: 'API key' })).toHaveTextContent('Set ••••a1b2')

    await user.click(screen.getByRole('button', { name: 'Clear API key' }))
    await user.click(save(aiForm()))
    expect(update).toHaveBeenCalledWith({ ai: { clearKey: true } })
    expect(await screen.findByLabelText('API key')).toHaveValue('') // no saved key any more: the input
  })

  it('shows a saved key as gone once the base URL changes, as the API drops it', async () => {
    const { user } = open(fx.settings({ baseUrl: 'https://proxy.example.com/v1' }))
    const test = vi.spyOn(api, 'testSettings').mockResolvedValue({ ok: false, error: 'OpenRouter needs an API key.' })
    const update = vi.spyOn(api, 'updateSettings').mockResolvedValue(fx.settings())
    await ready()
    const base = screen.getByLabelText('Base URL')
    await user.type(base, '/') // the same URL to the API
    expect(screen.getByRole('group', { name: 'API key' })).toHaveTextContent('Set ••••a1b2')

    await user.clear(base)
    await user.type(base, 'https://other.example.com/v1')
    expect(screen.queryByRole('group', { name: 'API key' })).not.toBeInTheDocument()
    expect(screen.getByLabelText('API key')).toHaveValue('')
    expect(screen.getByText(/The saved key isn't sent to a new base URL: paste it again\./)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Test connection' }))
    expect(test).toHaveBeenCalledWith({ baseUrl: 'https://other.example.com/v1' }) // no key, as the field shows
    expect(await within(aiForm()).findByRole('alert')).toHaveTextContent('Test failed: OpenRouter needs an API key.')

    await user.type(screen.getByLabelText('API key'), 'sk-or-v1-proxy-key-0000')
    await user.click(save(aiForm()))
    expect(update).toHaveBeenCalledWith({ ai: { baseUrl: 'https://other.example.com/v1', apiKey: 'sk-or-v1-proxy-key-0000' } })
  })

  it('keeps a key typed for a new base URL when the URL goes back, even after Clear', async () => {
    const { user } = open()
    const update = vi.spyOn(api, 'updateSettings').mockResolvedValue(fx.settings())
    await ready()
    await user.click(screen.getByRole('button', { name: 'Clear API key' }))
    const base = screen.getByLabelText('Base URL')
    await user.type(base, 'https://proxy.example.com/v1')
    await user.type(screen.getByLabelText('API key'), 'sk-or-v1-typed-key-0000')
    await user.clear(base) // back to the saved (default) URL
    expect(screen.getByLabelText('API key')).toHaveValue('sk-or-v1-typed-key-0000')
    expect(screen.getByRole('button', { name: 'Keep the saved API key' })).toBeInTheDocument()
    await user.click(save(aiForm()))
    expect(update).toHaveBeenCalledWith({ ai: { apiKey: 'sk-or-v1-typed-key-0000' } })
  })

  it('opens the ChatGPT sign-in in a new tab and reloads when the admin comes back', async () => {
    const { user, load } = open(fx.settings({ provider: 'chatgpt', model: '', apiKey: NO_KEY, status: 'error' }))
    const url = 'https://auth.openai.com/api/accounts/authorize?client_id=dynamic_agent_client&state=s'
    const tab = { opener: window as unknown, location: { href: '' }, close: vi.fn() }
    const popup = vi.spyOn(window, 'open').mockReturnValue(tab as unknown as Window)
    vi.spyOn(api, 'chatgptStart').mockResolvedValue({ url })
    await ready()
    expect(screen.getByText(/Open this console at localhost or 127\.0\.0\.1/)).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Sign in with ChatGPT' }))
    expect(popup).toHaveBeenCalledWith('', '_blank') // opened in the click, before the request
    await waitFor(() => expect(tab.location.href).toBe(url))
    expect(tab.opener).toBeNull()
    expect(tab.close).not.toHaveBeenCalled()
    expect(screen.getByText('Finish signing in in the new tab. This page updates when you come back.')).toBeInTheDocument()

    load.mockResolvedValue(fx.settings({ provider: 'chatgpt', model: '', apiKey: NO_KEY, chatgpt: { signedIn: true, email: 'owner@example.com', error: '' } }))
    act(() => void window.dispatchEvent(new Event('focus')))
    expect(await screen.findByText(/Signed in as/)).toHaveTextContent('Signed in as owner@example.com')
    expect(load).toHaveBeenCalledTimes(2)
    expect(card('AI provider')).toHaveTextContent('AI status: Ready')
  })

  it('closes the new tab and shows why when the sign-in cannot start', async () => {
    const { user } = open(fx.settings({ provider: 'chatgpt', model: '', apiKey: NO_KEY }))
    const tab = { opener: null, location: { href: '' }, close: vi.fn() }
    vi.spyOn(window, 'open').mockReturnValue(tab as unknown as Window)
    vi.spyOn(api, 'chatgptStart').mockRejectedValue(new ApiError(400, 'bad_request', 'Open the admin via http://localhost:8081, e.g. ssh -L 8081:127.0.0.1:8081 host'))
    await ready()
    await user.click(screen.getByRole('button', { name: 'Sign in with ChatGPT' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Open the admin via http://localhost:8081')
    expect(tab.close).toHaveBeenCalled()
    expect(tab.location.href).toBe('')
  })

  it.each([
    ['?chatgpt=ok', 'Signed in with ChatGPT.'],
    ['?chatgpt=error&message=You+cancelled+the+sign-in.', 'You cancelled the sign-in.'],
    ['?chatgpt=error', "ChatGPT sign-in didn't finish. Try again."],
  ])('toasts the sign-in result from %s once, then drops it from the URL', async (search, text) => {
    open(fx.settings(), `/settings${search}`)
    expect(await screen.findByText(text)).toBeInTheDocument()
    await ready()
    expect(screen.getAllByText(text)).toHaveLength(1)
    expect(currentPath()).toBe('/settings')
  })
})

describe('SettingsPage: search', () => {
  it('overrides an env key, sets the crawler URL and clears the token, showing a failed save verbatim', async () => {
    const { user } = open()
    const update = vi.spyOn(api, 'updateSettings').mockRejectedValueOnce(new ApiError(400, 'bad_request', 'SECRET_KEY is not set'))
    update.mockResolvedValue(fx.settings())
    await ready()
    await user.click(save(searchForm()))
    expect(await screen.findByText('Nothing changed.')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Replace Brave Search API key' }))
    await user.type(screen.getByLabelText('Brave Search API key'), '  BSA-new-key-1234567 ')
    await user.click(screen.getByRole('button', { name: 'Clear crawl4ai token' }))
    await user.type(screen.getByLabelText('crawl4ai base URL'), 'http://host.docker.internal:11235')
    await user.click(save(searchForm()))
    const body = { search: { braveApiKey: 'BSA-new-key-1234567', clearCrawlToken: true, crawlBase: 'http://host.docker.internal:11235' } }
    expect(update).toHaveBeenCalledWith(body) // no `ai`: search saves never test the model
    expect(await within(searchForm()).findByRole('alert')).toHaveTextContent('SECRET_KEY is not set')

    await user.click(save(searchForm()))
    expect(update).toHaveBeenLastCalledWith(body)
    expect(await screen.findByText('Saved search settings.')).toBeInTheDocument()
    expect(screen.getByLabelText('crawl4ai base URL')).toHaveValue('') // the form starts over from the saved values
  })

  it('shows a saved token as gone once the crawler URL changes, as the API drops it', async () => {
    const search = { ...fx.settings().search, crawlBase: 'http://crawl:11235', crawlBaseSource: 'db' as const, crawlToken: { set: true, hint: '…beef', source: 'db' as const } }
    const { user } = open(fx.settings({}, { search }))
    const update = vi.spyOn(api, 'updateSettings').mockResolvedValue(fx.settings())
    await ready()
    const base = screen.getByLabelText('crawl4ai base URL')
    await user.type(base, '/') // the same URL to the API
    expect(screen.getByRole('group', { name: 'crawl4ai token' })).toHaveTextContent('Set ••••beef')
    await user.click(save(searchForm()))
    expect(await screen.findByText('Nothing changed.')).toBeInTheDocument()

    await user.clear(base)
    await user.type(base, 'http://other:11235')
    expect(screen.queryByRole('group', { name: 'crawl4ai token' })).not.toBeInTheDocument()
    expect(screen.getByLabelText('crawl4ai token')).toHaveValue('')
    expect(screen.getByText(/The token isn't sent to a new base URL: paste it again\./)).toBeInTheDocument()
    expect(within(card('Search')).getAllByText('saved')).toHaveLength(1) // the URL's badge, not the token's
    await user.click(save(searchForm()))
    expect(update).toHaveBeenCalledWith({ search: { crawlBase: 'http://other:11235' } })
  })

  it('notes that a saved crawler URL overrides the one from .env', async () => {
    open(fx.settings({}, { search: { ...fx.settings().search, crawlBase: 'http://crawl:11235', crawlBaseSource: 'env' } }))
    await ready()
    expect(screen.getByLabelText('crawl4ai base URL')).toHaveValue('http://crawl:11235')
    expect(screen.getByText(/Set in \.env; a value saved here takes precedence\./)).toBeInTheDocument()
  })
})

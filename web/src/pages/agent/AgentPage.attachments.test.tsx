import { act, fireEvent, screen, waitFor, within } from '@testing-library/react'
import { StrictMode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from '../../lib/api'
import { creditsUsed, currentPath, json, renderPage, sseFrame, sseStream, stubSession } from '../../test/utils'
import { PNG_DATA, imageBrowser, pngFile } from '../../test/image-browser'
import { JPEG_BYTES } from '../../test/image-fixtures'
import { AgentPage } from './AgentPage'
import * as fx from '../../test/fixtures'

let browser: ReturnType<typeof imageBrowser>
let stream: ReturnType<typeof sseStream>
const sent = () => vi.mocked(fetch).mock.calls.map(([, init]) => JSON.parse(init!.body as string))
const push = (event: string, data: unknown) => act(() => stream.push(sseFrame(event, data)))
const wire = { kind: 'image', mediaType: 'image/png', name: 'chart.png', data: PNG_DATA }
const metadata = { kind: 'image', mediaType: 'image/jpeg', name: 'chart.png', size: JPEG_BYTES.length }
const open = (route = '/', strict = false) => {
  const page = strict ? <StrictMode><AgentPage /></StrictMode> : <AgentPage />
  return renderPage(page, { route, path: '/', routes: { '/chat/:chatId': page } })
}
const attach = async (name = 'chart.png') => {
  fireEvent.change(screen.getByLabelText('Attach files or photos'), { target: { files: [pngFile(name)] } })
  await waitFor(() => expect(screen.getByRole('button', { name: 'Send message' })).toBeEnabled())
}

beforeEach(() => {
  stubSession(); browser = imageBrowser(JPEG_BYTES)
  vi.spyOn(api, 'chats').mockResolvedValue({ chats: [{ id: 'old', title: 'Old chat', updatedAt: '' }] })
  vi.spyOn(api, 'brief').mockResolvedValue({ brief: null })
  vi.spyOn(api, 'getPulse').mockResolvedValue({ fearGreed: null })
  vi.mocked(fetch).mockImplementation(async (_url, init) => { stream = sseStream(init?.signal); return stream.response })
})
afterEach(() => vi.useRealTimers())

describe('attachment chat transient state', () => {
  it('sends normalized attachments, renders metadata, keeps paced SSE and authoritative credits, and never stores bytes', async () => {
    const { user } = open()
    await attach()
    await user.click(screen.getByRole('button', { name: 'Send message' }))
    expect(sent()).toEqual([{ message: 'Read this chart', mode: 'quality', lens: 'auto', attachments: [wire] }])
    expect(browser.revoke.mock.calls).toEqual([['blob:chart-1']])
    const sentFiles = screen.getByRole('list', { name: 'Attachments' })
    expect(sentFiles).toHaveTextContent('chart.pngImage · 1 kB')
    // the live turn shows the image from the bytes it holds; nothing keeps a blob URL
    expect(sentFiles.querySelector('img')).toHaveAttribute('src', `data:image/png;base64,${PNG_DATA}`)
    await push('chat', { chatId: 'new', title: 'Read this chart' })
    await push('tool_call', { id: 't', name: 'get_asset_analysis', label: 'Analyzing BTC', input: {} })
    await push('tool_result', { id: 't', name: 'get_asset_analysis', ok: true, summary: 'Live price checked' })
    await push('delta', { text: 'Chart structure' })
    expect(await screen.findByText('Chart structure')).toBeVisible()
    expect(screen.getByRole('button', { name: 'Stop' })).toBeVisible()
    await push('delta', { text: ' differs from live prices.' })
    await push('done', { messageId: 'm', credits: { ...fx.credits, used: 21 } })
    expect(creditsUsed()).toBe('21')
    expect(Object.values(localStorage).join(' ')).not.toContain(wire.data)
    expect(Object.values(sessionStorage).join(' ')).not.toContain(wire.data)
    expect(document.querySelector('[src^="blob:"]')).toBeNull()
    await user.type(screen.getByLabelText('Message the agent'), 'Another question{Enter}')
    expect(sent()[1]).toEqual({ chatId: 'new', message: 'Another question', mode: 'quality', lens: 'auto' })
  })

  it.each(['http', 'partial'])('retains bytes once for an explicit retry after %s error, including its own first-chat redirect', async kind => {
    if (kind === 'http') vi.mocked(fetch).mockResolvedValueOnce(json({ error: { code: 'upstream', message: 'Try again' } }, 502))
    const { user } = open()
    await attach()
    await user.click(screen.getByRole('button', { name: 'Send message' }))
    if (kind === 'partial') {
      await push('chat', { chatId: 'new', title: 'Read this chart' })
      await waitFor(() => expect(currentPath()).toBe('/chat/new'))
      await push('tool_call', { id: 't', name: 'get_asset_analysis', label: 'Analyzing BTC', input: {} })
      await push('delta', { text: 'Partial read' })
      await push('error', { code: 'upstream', message: 'Try again' })
    }
    await user.click(await screen.findByRole('button', { name: 'Retry' }))
    expect(sent()).toEqual([
      { message: 'Read this chart', mode: 'quality', lens: 'auto', attachments: [wire] },
      { ...(kind === 'partial' ? { chatId: 'new' } : {}), message: 'Read this chart', mode: 'quality', lens: 'auto', attachments: [wire] },
    ])
    expect(within(screen.getByRole('list', { name: 'Conversation' })).getAllByText('Read this chart')).toHaveLength(1)
  })

  it.each(['same-route new', 'switch', 'delete'])('clears staged previews and delayed conversion on %s', async action => {
    vi.spyOn(api, 'chat').mockResolvedValue({ chat: { id: 'old', title: 'Old chat', messages: [{ id: 'u', role: 'user', attachments: [], content: 'Old question', cards: [], createdAt: '' }] } })
    vi.spyOn(api, 'deleteChat').mockResolvedValue(undefined)
    const { user } = open(action === 'delete' ? '/chat/old' : '/')
    if (action === 'delete') await screen.findByText('Old question')
    await attach()
    let resolve!: (image: unknown) => void
    browser.decode.mockImplementation(() => new Promise(r => { resolve = r }))
    fireEvent.change(screen.getByLabelText('Attach files or photos'), { target: { files: [pngFile('late.png')] } })
    await waitFor(() => expect(browser.decode).toHaveBeenCalledTimes(2))
    if (action === 'switch') await user.click(screen.getAllByRole('link', { name: /Old chat/ })[0])
    else if (action === 'delete') {
      await user.click(screen.getAllByRole('button', { name: 'Delete chat: Old chat' })[0])
      await user.click(screen.getByRole('button', { name: /^Delete$/ }))
    } else await user.click(screen.getAllByRole('button', { name: 'New chat' })[0])
    await act(async () => resolve({ width: 1, height: 1, close() {} }))
    await waitFor(() => expect(screen.queryByRole('img')).toBeNull())
    expect(browser.revoke.mock.calls).toEqual([['blob:chart-1']])
    expect(browser.create).toHaveBeenCalledTimes(1)
    expect(screen.getByRole('button', { name: 'Send message' })).toBeDisabled()
  })

  it('shows safe history metadata and requires reattachment when an unfinished historical turn with files is retried', async () => {
    vi.spyOn(api, 'chat').mockResolvedValue({ chat: { id: 'old', title: 'Old chat', pending: true, messages: [{ id: 'u', role: 'user', content: 'Read this chart\n\n[image: chart.png]', cards: [], attachments: [metadata, { kind: 'document', mediaType: 'application/pdf', name: 'report.pdf', size: 209_500, pages: 3 }], createdAt: '' }] } } as never)
    vi.useFakeTimers()
    open('/chat/old')
    await act(async () => {})
    await act(() => vi.advanceTimersByTimeAsync(150 * 2000))
    const files = screen.getByRole('list', { name: 'Attachments' })
    expect(files).toHaveTextContent(`chart.pngImage · 1 kB`)
    expect(files).toHaveTextContent('report.pdfPDF · 210 kB · 3 pages')
    expect(files.querySelector('img')).toBeNull() // history keeps names, never bytes
    expect(screen.getByRole('alert')).toHaveTextContent('Attach the files again')
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull()
    expect(fetch).not.toHaveBeenCalled()
    vi.useRealTimers()
  })

  it('unmounts StrictMode cleanly and keeps image Stop / Escape behavior', async () => {
    const view = open('/', true)
    await attach()
    await view.user.click(screen.getByLabelText('Message the agent'))
    await view.user.keyboard('{Enter}')
    await waitFor(() => expect(fetch).toHaveBeenCalledTimes(1))
    await push('delta', { text: 'Partial answer' })
    await view.user.click(screen.getByLabelText('Message the agent'))
    await view.user.keyboard('{Escape}')
    expect(await screen.findByText(/Stopped here/)).toBeInTheDocument()
    expect(browser.revoke.mock.calls).toEqual([['blob:chart-1']])
    view.unmount()
  })

  it('keeps the new retry stream owned after the old reader finally settles', async () => {
    let release!: () => void
    let first!: ReadableStreamDefaultController<Uint8Array>
    const cancellation = new Promise<void>(resolve => { release = resolve })
    vi.mocked(fetch).mockResolvedValueOnce(new Response(new ReadableStream<Uint8Array>({ start(controller) { first = controller }, cancel() { return cancellation } }), { headers: { 'Content-Type': 'text/event-stream' } }))
    const { user } = open()
    await attach(); await user.click(screen.getByRole('button', { name: 'Send message' }))
    await act(() => first.enqueue(new TextEncoder().encode(sseFrame('error', { code: 'upstream', message: 'Try again' }))))
    await user.click(await screen.findByRole('button', { name: 'Retry' }))
    expect(sent()).toHaveLength(2)
    await act(async () => release())
    const retrySignal = vi.mocked(fetch).mock.calls[1][1]!.signal as AbortSignal
    await user.click(screen.getByRole('button', { name: 'Stop' }))
    expect(retrySignal.aborted).toBe(true)
    await user.type(screen.getByLabelText('Message the agent'), 'Continue{Enter}')
    expect(sent()[2]).toEqual({ message: 'Continue', mode: 'quality', lens: 'auto' })
  })

  it.each(['done', 'error'])('accepts a new message after %s while terminal reader cancellation is still pending', async terminal => {
    let release!: () => void
    let first!: ReadableStreamDefaultController<Uint8Array>
    const cancellation = new Promise<void>(resolve => { release = resolve })
    vi.mocked(fetch).mockResolvedValueOnce(new Response(new ReadableStream<Uint8Array>({ start(controller) { first = controller }, cancel() { return cancellation } }), { headers: { 'Content-Type': 'text/event-stream' } }))
    const { user } = open()
    await attach(); await user.click(screen.getByRole('button', { name: 'Send message' }))
    await act(() => first.enqueue(new TextEncoder().encode(sseFrame(terminal, terminal === 'done' ? { messageId: 'm', credits: fx.credits } : { code: 'upstream', message: 'Try again' }))))
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Stop' })).toBeNull())
    try {
      await user.type(screen.getByLabelText('Message the agent'), 'New question')
      expect(screen.getByRole('button', { name: 'Send message' })).toBeEnabled()
      await user.keyboard('{Enter}')
      expect(screen.getByLabelText('Message the agent')).toHaveValue('')
      expect(sent()).toHaveLength(2)
      expect(sent()[1]).toEqual({ message: 'New question', mode: 'quality', lens: 'auto' })
    } finally {
      await act(async () => release())
    }
  })

  it.each(['resolve', 'reject'])('preserves a new image send and its retry when old cancellation later %ss', async settlement => {
    let resolve!: () => void, reject!: (error: Error) => void
    let first!: ReadableStreamDefaultController<Uint8Array>
    const cancellation = new Promise<void>((yes, no) => { resolve = yes; reject = no })
    vi.mocked(fetch).mockResolvedValueOnce(new Response(new ReadableStream<Uint8Array>({ start(controller) { first = controller }, cancel() { return cancellation } }), { headers: { 'Content-Type': 'text/event-stream' } }))
    const { user } = open()
    await attach(); await user.click(screen.getByRole('button', { name: 'Send message' }))
    await act(() => first.enqueue(new TextEncoder().encode(sseFrame('error', { code: 'upstream', message: 'Try again' }))))
    await screen.findByRole('button', { name: 'Retry' })
    await attach('new.png')
    await user.type(screen.getByLabelText('Message the agent'), 'New question{Enter}')
    const nextImage = { ...wire, name: 'new.png' }
    expect(sent()[1]).toEqual({ message: 'New question', mode: 'quality', lens: 'auto', attachments: [nextImage] })
    const balance = creditsUsed(), refreshes = vi.mocked(api.me).mock.calls.length
    await act(async () => settlement === 'resolve' ? resolve() : reject(new Error('Old reader cancellation failed')))
    expect(screen.getByRole('button', { name: 'Stop' })).toBeVisible()
    expect(creditsUsed()).toBe(balance)
    expect(api.me).toHaveBeenCalledTimes(refreshes)
    await push('delta', { text: 'New read' })
    expect(await screen.findByText('New read')).toBeVisible()
    await push('error', { code: 'upstream', message: 'Try again' })
    await user.click(await screen.findByRole('button', { name: 'Retry' }))
    expect(sent()[2]).toEqual({ message: 'New question', mode: 'quality', lens: 'auto', attachments: [nextImage] })
    const retrySignal = vi.mocked(fetch).mock.calls[2][1]!.signal as AbortSignal
    await user.click(screen.getByRole('button', { name: 'Stop' }))
    expect(retrySignal.aborted).toBe(true)
  })
})

import { act, fireEvent, screen, waitFor } from '@testing-library/react'
import { StrictMode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderPage, stubSession } from '../../test/utils'
import { CSV_TEXT, DOCX_BYTES, DOCX_TYPE, PDF_BYTES } from '../../test/documents'
import { PNG_DATA, imageBrowser, photoFile, pngFile } from '../../test/image-browser'
import { JPEG_BYTES } from '../../test/image-fixtures'
import { Composer } from './Composer'

const JPEG_DATA = btoa(String.fromCharCode(...JPEG_BYTES))
const base64 = (bytes: Uint8Array) => btoa(String.fromCharCode(...bytes))
const pdfFile = (name = 'report.pdf') => new File([new Uint8Array(PDF_BYTES)], name, { type: 'application/pdf' })
const docxFile = (name = 'memo.docx') => new File([new Uint8Array(DOCX_BYTES)], name, { type: DOCX_TYPE })

let browser: ReturnType<typeof imageBrowser>
beforeEach(() => { stubSession(); browser = imageBrowser(JPEG_BYTES) })
const open = (strict = false) => {
  const onSend = vi.fn()
  const composer = <Composer onSend={onSend} onStop={() => {}} streaming={false} mode="quality" onModeChange={() => {}} lens="auto" onLensChange={() => {}} />
  return { ...renderPage(strict ? <StrictMode>{composer}</StrictMode> : composer), onSend }
}
const picker = () => screen.getByLabelText('Attach files or photos')
const pick = (files: File[]) => fireEvent.change(picker(), { target: { files } })
const attach = async (files = [pngFile()]) => {
  pick(files)
  await screen.findByRole('img', { name: `Preview ${files[0].name}` })
}
const chips = () => screen.queryByRole('list', { name: 'Attached files' })?.querySelectorAll('li') ?? []

describe('attachment composer', () => {
  it('sends a small PNG as is, discloses 3 credits and the limits, and sends the image-only default once', async () => {
    const { user, onSend } = open()
    expect(picker()).toHaveAttribute('accept', expect.stringContaining('image/*'))
    expect(picker()).toHaveAttribute('accept', expect.stringContaining('.pdf'))
    expect(screen.getByText(/Up to 5 files/)).toHaveClass('sr-only')
    await attach()
    expect(screen.getByRole('img', { name: 'Preview chart.png' })).toHaveAttribute('src', 'blob:chart-1')
    expect(screen.getByText(/Up to 5 files/)).not.toHaveClass('sr-only')
    expect(screen.getByText('Image · 1 kB')).toBeVisible()
    expect(screen.getByText('3 cr / msg')).toHaveAttribute('title', 'This message spends 3 of your daily AI credits')
    await user.click(screen.getByRole('button', { name: 'Send message' }))
    expect(onSend).toHaveBeenCalledExactlyOnceWith('Read this chart', [{ kind: 'image', mediaType: 'image/png', name: 'chart.png', data: PNG_DATA }])
    expect(screen.queryByRole('img')).toBeNull()
    expect(browser.revoke.mock.calls).toEqual([['blob:chart-1']])
  })

  it('converts a phone photo to JPEG and reads documents with a type icon, size and the documents cost', async () => {
    const { user, onSend } = open()
    const csv = new File([CSV_TEXT], 'prices.csv', { type: 'application/vnd.ms-excel' })
    pick([photoFile('IMG_0042.HEIC'), pdfFile(), docxFile(), csv])
    await screen.findByRole('img', { name: 'Preview IMG_0042.HEIC' })
    await screen.findByText(`Word · ${Math.ceil(DOCX_BYTES.length / 1000)} kB`)
    expect(screen.getByText(`PDF · ${Math.ceil(PDF_BYTES.length / 1000)} kB`)).toBeVisible()
    expect(screen.getByText('CSV · 1 kB')).toBeVisible()
    expect(chips()).toHaveLength(4)
    expect(screen.getByText('5 cr / msg')).toHaveAttribute('title', 'This message spends 5 of your daily AI credits')
    await user.click(screen.getByRole('button', { name: 'Remove memo.docx' }))
    expect(screen.getByText('4 cr / msg')).toBeVisible()
    await user.click(screen.getByRole('button', { name: 'Send message' }))
    expect(onSend).toHaveBeenCalledExactlyOnceWith('Read the attached files', [
      { kind: 'image', mediaType: 'image/jpeg', name: 'IMG_0042.HEIC', data: JPEG_DATA },
      { kind: 'document', mediaType: 'application/pdf', name: 'report.pdf', data: base64(PDF_BYTES) },
      { kind: 'document', mediaType: 'text/csv', name: 'prices.csv', data: base64(new TextEncoder().encode(CSV_TEXT)) },
    ])
    expect(browser.revoke).toHaveBeenCalledOnce() // only the photo had a preview
  })

  it('handles paste and drop, caps the combined selection at five, and revokes only the removed preview', async () => {
    const { user } = open()
    const box = screen.getByLabelText('Message the agent')
    // cells copied from Excel carry text and a picture of the range: the text is pasted, nothing attached
    const cells = { files: [pngFile('image.png')], items: [], types: ['text/plain', 'text/html', 'Files'], getData: (type: string) => ({ 'text/plain': 'BTC\t64000', 'text/html': '<html xmlns:x="urn:schemas-microsoft-com:office:excel"><table><tr><td>BTC</td></tr></table>' })[type] ?? '' }
    expect(fireEvent.paste(box, { clipboardData: cells })).toBe(true)
    expect(chips()).toHaveLength(0)
    fireEvent.paste(box, { clipboardData: { files: [], items: [{ kind: 'file', getAsFile: () => pngFile('paste.png') }, { kind: 'string', getAsFile: () => null }], types: ['text/plain', 'Files'], getData: () => 'paste.png' } })
    await screen.findByRole('img', { name: 'Preview paste.png' })
    const form = box.closest('form')!
    fireEvent.dragOver(form, { dataTransfer: { types: ['Files'] } })
    expect(form).toHaveClass('border-accent')
    fireEvent.drop(box, { dataTransfer: { files: [pngFile('drop.png'), pdfFile()], types: ['Files'] } })
    await screen.findByRole('img', { name: 'Preview drop.png' })
    expect(form).not.toHaveClass('border-accent')
    fireEvent.paste(box, { clipboardData: { files: [], items: [] } }) // plain text paste: nothing attached
    pick([pdfFile('a.pdf'), pdfFile('b.pdf'), pdfFile('c.pdf')])
    expect(await screen.findByRole('alert')).toHaveTextContent('Too many files: attach at most 5 per message. Left out: c.pdf.')
    await waitFor(() => expect(chips()).toHaveLength(5))
    expect(screen.queryByText('c.pdf')).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Remove paste.png' }))
    expect(screen.queryByRole('alert')).toBeNull()
    expect(browser.revoke.mock.calls).toEqual([['blob:chart-1']])
    expect(screen.getByRole('img', { name: 'Preview drop.png' })).toHaveAttribute('src', 'blob:chart-2')
  })

  it('names the file in each specific error and leaves the selection usable', async () => {
    open()
    browser.decode.mockRejectedValueOnce(new Error('no decoder'))
    pick([
      new File(['PK'], 'archive.zip', { type: 'application/zip' }),
      new File([new Uint8Array(4_500_001)], 'huge.pdf', { type: 'application/pdf' }),
      new File(['hello'], 'fake.pdf', { type: 'application/pdf' }),
      new File(['no'], 'bad.heic', { type: 'image/heic' }),
    ])
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('archive.zip: Unsupported file type.')
    await waitFor(() => expect(alert).toHaveTextContent("bad.heic: Couldn't read this photo."))
    await waitFor(() => expect(alert).toHaveTextContent('huge.pdf: Too big (4.6 MB). Documents can be at most 4.5 MB.'))
    await waitFor(() => expect(alert).toHaveTextContent("fake.pdf: This doesn't look like a PDF."))
    expect(picker()).toHaveAttribute('aria-describedby', expect.stringContaining(alert.id))
    expect(chips()).toHaveLength(0)
    expect(screen.getByRole('button', { name: 'Send message' })).toBeDisabled()
    await attach()
    expect(screen.queryByRole('alert')).toBeNull()
    expect(screen.getByRole('button', { name: 'Send message' })).toBeEnabled()
  })

  it('blocks Send while decoding and reserves pending slots across concurrent picks', async () => {
    const resolvers: ((image: unknown) => void)[] = []
    browser.decode.mockImplementation(() => new Promise(resolve => resolvers.push(resolve)))
    const { user, onSend } = open()
    pick([pngFile('one.png')])
    await waitFor(() => expect(resolvers).toHaveLength(1))
    pick([pngFile('two.png'), pngFile('three.png'), pngFile('four.png'), pngFile('five.png')])
    await waitFor(() => expect(resolvers).toHaveLength(5))
    expect(screen.getAllByRole('status')).toHaveLength(5)
    pick([pngFile('six.png')])
    expect(await screen.findByRole('alert')).toHaveTextContent('at most 5')
    expect(screen.getByRole('button', { name: 'Send message' })).toBeDisabled()
    for (const i of [1, 0, 4, 3, 2]) await act(async () => resolvers[i]({ width: 1, height: 1, close() {} }))
    await waitFor(() => expect(screen.getAllByRole('img')).toHaveLength(5))
    expect(browser.decode).toHaveBeenCalledTimes(5)
    await user.click(screen.getByRole('button', { name: 'Send message' }))
    expect(onSend.mock.calls[0][1].map((a: { name: string }) => a.name)).toEqual(['one.png', 'two.png', 'three.png', 'four.png', 'five.png'])
    expect(browser.revoke).toHaveBeenCalledTimes(5)
  })

  it('drops late conversion after removal without creating a preview', async () => {
    let resolve!: (image: unknown) => void
    browser.decode.mockImplementation(() => new Promise(r => { resolve = r }))
    const { user } = open()
    pick([pngFile()])
    await waitFor(() => expect(browser.decode).toHaveBeenCalledTimes(1))
    await user.click(screen.getByRole('button', { name: 'Remove chart.png' }))
    await act(async () => resolve({ width: 1, height: 1, close() {} }))
    await act(async () => {})
    expect(screen.queryByRole('img')).toBeNull()
    expect(browser.create).not.toHaveBeenCalled()
  })

  it('reports a photo that cannot shrink below 1.5 MB and permits a new valid one', async () => {
    browser.encode.mockImplementation(callback => callback(new Blob([new Uint8Array(1_500_001)], { type: 'image/jpeg' })))
    open()
    pick([photoFile()])
    const error = await screen.findByRole('alert')
    expect(error).toHaveTextContent("photo.heic: Couldn't shrink this photo below 1.5 MB")
    expect(screen.queryByRole('img')).toBeNull()
    expect(browser.create).not.toHaveBeenCalled()
    await attach()
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('keeps a new selection when a removed conversion fails late, and keeps another file when one fails', async () => {
    let reject!: (error: Error) => void
    browser.decode.mockImplementationOnce(() => new Promise((_resolve, r) => { reject = r }))
    const { user } = open()
    pick([pngFile('old.png')])
    await waitFor(() => expect(browser.decode).toHaveBeenCalledOnce())
    await user.click(screen.getByRole('button', { name: 'Remove old.png' }))
    await attach([pngFile('new.png')])
    await act(async () => reject(new Error('late failure')))
    expect(screen.queryByRole('alert')).toBeNull()
    expect(screen.getByRole('img')).toHaveAccessibleName('Preview new.png')
    pick([new File(['x'], 'notes.txt', { type: 'text/plain' }), new File([new Uint8Array([0])], 'bad.txt', { type: 'text/plain' })])
    expect(await screen.findByRole('alert')).toHaveTextContent('bad.txt: Text files must be UTF-8')
    await waitFor(() => expect(chips()).toHaveLength(2))
    expect(screen.getByRole('img')).toHaveAccessibleName('Preview new.png')
    // notes.txt reads in two steps, so it can land after bad.txt has failed
    await waitFor(() => expect(screen.getByRole('button', { name: 'Send message' })).toBeEnabled())
  })

  it('keeps IME and Shift+Enter behavior and counts 4,000 Unicode runes with an image', async () => {
    const { user, onSend } = open()
    await attach()
    const box = screen.getByLabelText('Message the agent')
    fireEvent.change(box, { target: { value: '😀'.repeat(4000) } })
    expect(screen.getByText('4,000 / 4,000 · 3 cr')).toBeVisible()
    expect(screen.getByRole('button', { name: 'Send message' })).toBeEnabled()
    fireEvent.keyDown(box, { key: 'Enter', isComposing: true })
    expect(onSend).not.toHaveBeenCalled()
    await user.click(box); await user.keyboard('{Shift>}{Enter}{/Shift}')
    expect(onSend).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: 'Send message' })).toBeDisabled()
    fireEvent.change(box, { target: { value: '😀'.repeat(4000) } })
    await user.keyboard('{Enter}')
    expect(onSend).toHaveBeenCalledExactlyOnceWith('😀'.repeat(4000), [expect.objectContaining({ mediaType: 'image/png' })])
    expect(browser.revoke).toHaveBeenCalledOnce()
  })

  it('stops dictation on Escape and aborts late speech results on an attachment send', async () => {
    let rec!: Speech
    class Speech {
      continuous = false; interimResults = false; lang = ''
      onresult: ((event: { results: unknown[] }) => void) | null = null
      onerror: (() => void) | null = null; onend: (() => void) | null = null
      abort = vi.fn(); start() { rec = this }; stop() { this.onend?.() }
    }
    vi.stubGlobal('SpeechRecognition', Speech)
    const { user, onSend } = open()
    await attach()
    const box = screen.getByLabelText('Message the agent')
    await user.click(screen.getByRole('button', { name: 'Dictate' }))
    await act(() => rec.onresult?.({ results: [Object.assign([{ transcript: 'Read support' }], { isFinal: true })] }))
    await user.click(box); await user.keyboard('{Escape}')
    expect(box).toHaveValue('Read support')
    expect(onSend).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'Dictate' }))
    const late = rec
    await user.click(screen.getByRole('button', { name: 'Send message' }))
    expect(late.abort).toHaveBeenCalledOnce()
    expect(late.onresult).toBeNull()
    expect(onSend).toHaveBeenCalledExactlyOnceWith('Read support', [expect.objectContaining({ kind: 'image' })])
  })

  it.each([false, true])('cleans up every URL and rejects late decode after unmount (StrictMode=%s)', async strict => {
    const view = open(strict)
    await attach()
    let resolve!: (image: unknown) => void
    browser.decode.mockImplementation(() => new Promise(r => { resolve = r }))
    pick([pngFile('late.png')])
    await waitFor(() => expect(browser.decode).toHaveBeenCalledTimes(2))
    view.unmount()
    await act(async () => resolve({ width: 1, height: 1, close() {} }))
    expect(browser.create).toHaveBeenCalledTimes(1)
    expect(browser.revoke.mock.calls).toEqual([['blob:chart-1']])
  })
})

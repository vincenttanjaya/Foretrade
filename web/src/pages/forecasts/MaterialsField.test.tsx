import { act, fireEvent, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError } from '../../lib/api'
import type { ForecastDraft } from '../../lib/types'
import { CSV_TEXT, PDF_BYTES } from '../../test/documents'
import * as fx from '../../test/fixtures'
import { imageBrowser, photoFile, pngFile } from '../../test/image-browser'
import { JPEG_BYTES } from '../../test/image-fixtures'
import { creditsUsed, renderPage, stubSession } from '../../test/utils'
import { MATERIALS_HELP } from './MaterialsField'
import { NewForecastPage } from './NewForecastPage'

const draft: ForecastDraft = {
  shape: 'binary',
  question: 'Will BTC trade above $150k before July 2027?',
  outcomes: ['Yes', 'No'],
  resolutionCriteria: 'Resolves Yes if the Binance BTCUSDT price trades above $150,000 before Jul 1, 2027.',
  resolutionDate: '2027-07-01',
  category: 'crypto',
  priceRule: null,
  context: '',
}
const base64 = (bytes: Uint8Array) => btoa(String.fromCharCode(...bytes))
const pdfFile = (name = 'q3-note.pdf') => new File([new Uint8Array(PDF_BYTES)], name, { type: 'application/pdf' })
const csvFile = (name = 'closes.csv') => new File([CSV_TEXT], name, { type: 'text/csv' })

let browser: ReturnType<typeof imageBrowser>
beforeEach(() => {
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(new Date(2026, 9, 2, 12))
  stubSession()
  browser = imageBrowser(JPEG_BYTES)
})
afterEach(() => vi.useRealTimers())

async function review(state: Record<string, unknown> = {}) {
  const r = renderPage(<NewForecastPage />, {
    route: '/forecasts/new',
    path: '/forecasts/new',
    state: { draft, ...state },
    routes: { '/forecasts/:id': <p>detail page</p> },
  })
  await screen.findByRole('heading', { name: 'Review the question.' })
  return r
}
const picker = () => screen.getByLabelText('Add material')
const zone = () => screen.getByRole('group', { name: 'Materials: drop or paste files' })
const chips = () => within(screen.getByRole('list', { name: 'Attached files' })).getAllByRole('listitem')
const runButton = () => screen.getByRole('button', { name: /Run forecast/ })

describe('New forecast · materials', () => {
  it('attaches a PDF, a CSV and a photo, prices the run and sends them with the draft', async () => {
    const create = vi.spyOn(api, 'createForecast').mockResolvedValue({ forecast: fx.forecast({ id: 'f9', status: 'running' }), credits: fx.credits })
    const { user } = await review()
    expect(screen.getByText('Materials (optional)')).toBeInTheDocument()
    expect(screen.getByText('0/5')).toBeInTheDocument()
    expect(screen.getByText(MATERIALS_HELP)).toBeVisible()
    expect(picker()).toHaveAttribute('accept', expect.stringContaining('.pdf'))
    expect(runButton()).toHaveTextContent('Run forecast· 5 cr')
    fireEvent.change(picker(), { target: { files: [pdfFile(), csvFile(), photoFile('IMG_0042.HEIC')] } })
    expect(screen.getByText('3/5')).toBeInTheDocument()
    expect(screen.getByText('3 of 5 files')).toHaveClass('sr-only') // the counter reads as words
    expect(runButton()).toBeDisabled() // still converting
    expect(screen.getByText('Preparing files…')).toBeInTheDocument()
    await screen.findByRole('img', { name: 'Preview IMG_0042.HEIC' })
    await screen.findByText(`PDF · ${Math.ceil(PDF_BYTES.length / 1000)} kB`)
    expect(screen.getByText('CSV · 1 kB')).toBeInTheDocument()
    expect(runButton()).toBeEnabled()
    expect(runButton()).toHaveTextContent('· 8 cr')
    expect(screen.getByText('Materials', { selector: 'dt' }).nextSibling).toHaveTextContent('3')
    await user.click(runButton())
    expect(create).toHaveBeenCalledExactlyOnceWith(draft, {
      attachments: [
        { kind: 'document', mediaType: 'application/pdf', name: 'q3-note.pdf', data: base64(PDF_BYTES) },
        { kind: 'document', mediaType: 'text/csv', name: 'closes.csv', data: base64(new TextEncoder().encode(CSV_TEXT)) },
        { kind: 'image', mediaType: 'image/jpeg', name: 'IMG_0042.HEIC', data: base64(JPEG_BYTES) },
      ],
    })
    expect(await screen.findByText('detail page')).toBeInTheDocument()
    expect(browser.revoke).toHaveBeenCalledOnce() // leaving the page drops the photo's preview
  })

  it('takes drops and pastes on the zone, files dropped on the context box, and skips a spreadsheet paste', async () => {
    const { user } = await review()
    fireEvent.dragOver(zone(), { dataTransfer: { types: ['Files'] } })
    expect(zone()).toHaveClass('border-accent')
    fireEvent.drop(zone(), { dataTransfer: { files: [pngFile('drop.png')], types: ['Files'] } })
    expect(zone()).not.toHaveClass('border-accent')
    await screen.findByRole('img', { name: 'Preview drop.png' })
    fireEvent.paste(zone(), { clipboardData: { files: [], items: [{ kind: 'file', getAsFile: () => pngFile('paste.png') }], types: ['Files'], getData: () => '' } })
    await screen.findByRole('img', { name: 'Preview paste.png' })
    const cells = { files: [pngFile('cells.png')], items: [], types: ['text/plain', 'text/rtf', 'Files'], getData: (t: string) => (t === 'text/plain' ? 'BTC\t64000' : '') }
    expect(fireEvent.paste(zone(), { clipboardData: cells })).toBe(true) // not prevented: the text pastes, nothing attached
    expect(fireEvent.paste(zone(), { clipboardData: { files: [], items: [] } })).toBe(true)

    await user.click(screen.getByRole('button', { name: 'Add context for the desk (optional)' }))
    const context = screen.getByLabelText('Context')
    expect(fireEvent.dragOver(context, { dataTransfer: { types: ['Files'] } })).toBe(false)
    fireEvent.drop(context, { dataTransfer: { files: [csvFile('dropped.csv')], types: ['Files'] } })
    fireEvent.paste(context, { clipboardData: { files: [pdfFile('pasted.pdf')], items: [], types: ['Files'], getData: () => '' } })
    await waitFor(() => expect(chips()).toHaveLength(4))
    expect(context).toHaveValue('') // files never inline into S0 any more
    expect(screen.getByText('dropped.csv')).toBeInTheDocument()
    expect(screen.getByText('pasted.pdf')).toBeInTheDocument()
  })

  it('caps a forecast at five files, names each bad file, and frees a slot on remove', async () => {
    const { user } = await review()
    fireEvent.change(picker(), {
      target: {
        files: [
          new File(['PK'], 'archive.zip', { type: 'application/zip' }),
          new File([new Uint8Array(4_600_000)], 'big.pdf', { type: 'application/pdf' }),
          csvFile('a.csv'), csvFile('b.csv'), csvFile('c.csv'), csvFile('d.csv'), csvFile('e.csv'),
        ],
      },
    })
    const alert = screen.getByRole('alert')
    expect(alert).toHaveTextContent('archive.zip: Unsupported file type.')
    expect(alert).toHaveTextContent('Too many files: attach at most 5 per forecast. Left out: e.csv.')
    await waitFor(() => expect(alert).toHaveTextContent('big.pdf: Too big (4.6 MB). Documents can be at most 4.5 MB.'))
    expect(picker()).toHaveAttribute('aria-describedby', expect.stringContaining(alert.id))
    await waitFor(() => expect(chips()).toHaveLength(4))
    expect(screen.getByText('4/5')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Remove a.csv' }))
    expect(screen.queryByRole('alert')).toBeNull()
    fireEvent.change(picker(), { target: { files: [csvFile('f.csv'), csvFile('g.csv')] } })
    await waitFor(() => expect(chips()).toHaveLength(5))
    expect(picker()).toBeDisabled() // full
    expect(runButton()).toHaveTextContent('· 10 cr')
  })

  it('shows a server error that names a file, and keeps the files for a retry', async () => {
    vi.spyOn(api, 'createForecast').mockRejectedValue(new ApiError(400, 'bad_request', 'closes.csv: text files must be UTF-8 without NUL bytes'))
    const { user } = await review()
    fireEvent.change(picker(), { target: { files: [csvFile()] } })
    await screen.findByText('CSV · 1 kB')
    await user.click(runButton())
    expect(await screen.findByRole('alert')).toHaveTextContent('closes.csv: text files must be UTF-8 without NUL bytes')
    expect(chips()).toHaveLength(1)
  })

  it('says what the files cost when the credits left cover the run without them', async () => {
    vi.spyOn(api, 'me').mockResolvedValue({ user: fx.user, credits: { ...fx.credits, used: 494 } })
    vi.spyOn(api, 'createForecast').mockRejectedValue(new ApiError(429, 'ai_quota', "You have used today's AI credits. They reset at 00:00 UTC."))
    const { user } = await review()
    await waitFor(() => expect(creditsUsed()).toBe('494'))
    fireEvent.change(picker(), { target: { files: [csvFile('a.csv'), csvFile('b.csv'), csvFile('c.csv')] } })
    await waitFor(() => expect(runButton()).toBeEnabled())
    expect(runButton()).toHaveTextContent('· 8 cr')
    await user.click(runButton())
    expect(await screen.findByRole('alert')).toHaveTextContent('This run needs 8 credits and you have 6 left today. Remove 2 files, or run it after the 00:00 UTC reset.')
    expect(chips()).toHaveLength(3)
  })

  it('says earlier files are not carried over to an edited question', async () => {
    await review({ materials: 2 })
    expect(screen.getByText("Your earlier files aren't carried over to an edited question. Attach them again.")).toBeInTheDocument()
  })

  it('lists the files on the manual review step and keeps them across steps', async () => {
    const { user } = renderPage(<NewForecastPage />, { route: '/forecasts/new', path: '/forecasts/new' })
    fireEvent.change(screen.getByLabelText('Your rough idea'), { target: { value: 'Will BTC trade above $150k before July 2027?' } })
    await user.click(screen.getByRole('button', { name: 'Create manually' }))
    await user.click(screen.getByRole('button', { name: /Continue to Ask/ }))
    await user.click(screen.getByRole('button', { name: /Continue to Resolve/ }))
    fireEvent.change(screen.getByLabelText('Resolution criteria'), { target: { value: draft.resolutionCriteria } })
    fireEvent.change(screen.getByLabelText('Resolution date'), { target: { value: '2027-07-01' } })
    fireEvent.change(picker(), { target: { files: [csvFile()] } })
    await screen.findByText('CSV · 1 kB')
    await user.click(screen.getByRole('button', { name: /Continue to Review/ }))
    expect(screen.getByText('Materials · 1')).toBeInTheDocument()
    expect(screen.getByText('closes.csv')).toBeInTheDocument()
    expect(runButton()).toHaveTextContent('· 6 cr')
    await act(async () => {})
  })
})

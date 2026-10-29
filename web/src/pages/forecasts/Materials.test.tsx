import { act, fireEvent, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError } from '../../lib/api'
import { CSV_TEXT } from '../../test/documents'
import * as fx from '../../test/fixtures'
import { creditsUsed, currentPath, renderPage, stubSession } from '../../test/utils'
import { YourMaterials } from './Materials'

const open = (f = fx.materialsForecast()) =>
  renderPage(<YourMaterials f={f} />, { route: `/forecasts/${f.id}`, path: '/forecasts/:id', routes: { '/forecasts/:id/next': <p /> } })
const row = (id: string) => document.getElementById(`material-${id}`)!
const details = (id: string) => row(id).querySelector('details')!

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(new Date('2026-10-06T12:00:00Z'))
  stubSession()
})
afterEach(() => {
  vi.useRealTimers()
  window.location.hash = ''
})

describe('YourMaterials', () => {
  it('lists each material with its type, size, status and what the desk read, as plain text', () => {
    open()
    expect(screen.getByText('Your materials · 3')).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'What you gave the desk' })).toBeInTheDocument()
    expect(screen.getByText(/we keep these notes, not the files/)).toBeInTheDocument()
    expect(within(row('D1')).getByText('PDF · 1.2 MB · 12 pages')).toBeInTheDocument()
    expect(within(row('D1')).getByText('About this question')).toBeInTheDocument()
    expect(within(row('D2')).getByText('Background')).toBeInTheDocument()
    expect(within(row('D2')).getByText('Read on Oct 5, 2026')).toBeInTheDocument() // reused from an earlier run
    expect(within(row('D3')).getByText("Couldn't read this file")).toBeInTheDocument()
    expect(within(row('D3')).getByText('Image · 880 kB')).toBeInTheDocument()
    // three rows: collapsed until opened
    expect(details('D1')).not.toHaveAttribute('open')
    // a summary and an excerpt that carry markup render it literally
    expect(row('D1').querySelector('b')).toBeNull()
    expect(within(row('D1')).getByText(/Its <b>numbers<\/b> are/)).toBeInTheDocument()
    expect(within(row('D1')).getByText(/<b>Key risk<\/b>/)).toBeInTheDocument()
    const findings = within(within(row('D1')).getByRole('list', { name: 'Findings from Q3 broker note (final).pdf' })).getAllByRole('listitem')
    expect(findings.map((li) => li.textContent?.slice(0, 3))).toEqual(['F15', 'F16', 'F17'])
    expect(findings[0]).toHaveTextContent('moderate')
    expect(findings[0]).toHaveTextContent('p. 4 · as of Sep 30, 2026')
    expect(findings[0]).toHaveTextContent('Upward')
    expect(findings[2]).toHaveTextContent('Downward')
    expect(within(row('D1')).getByText('as of Sep 2026')).toBeInTheDocument()
  })

  it('opens the row a link points to, on load, on a hash change and on a repeat click', async () => {
    window.location.hash = '#material-D2'
    open()
    expect(details('D2')).toHaveAttribute('open')
    expect(document.activeElement).toBe(row('D2').querySelector('summary'))
    expect(details('D1')).not.toHaveAttribute('open')
    await act(async () => {
      window.location.hash = '#material-D1'
      window.dispatchEvent(new HashChangeEvent('hashchange'))
    })
    expect(details('D1')).toHaveAttribute('open')
    // closing it and clicking the same link again (no hash change) opens it again
    act(() => {
      details('D1').open = false
      details('D1').dispatchEvent(new Event('toggle'))
    })
    expect(details('D1')).not.toHaveAttribute('open')
    const link = document.createElement('a')
    link.href = '#material-D1'
    document.body.append(link)
    fireEvent.click(link)
    await waitFor(() => expect(details('D1')).toHaveAttribute('open'))
    link.remove()
  })

  it('opens every row when there are two or fewer, and shows a run still reading', () => {
    const [d1, d2] = fx.materials
    open(fx.materialsForecast({ status: 'running', result: null, materials: [{ ...d1, status: 'pending', summary: '', findings: [], excerpt: '', readAt: null }, d2] }))
    expect(details('D2')).toHaveAttribute('open')
    expect(within(row('D1')).getByText('Reading…')).toBeInTheDocument()
    expect(within(row('D2')).getAllByText(/six of (the )?nine/i)).toHaveLength(2) // summary and finding
    expect(within(row('D2')).getByText('·')).toBeInTheDocument() // no ledger yet: no F id
    expect(screen.queryByText('Add material to the next update')).toBeNull() // a running forecast cannot update
  })

  it('shows a file with nothing to expand as a plain row, not a disclosure, and a link to it still lands on it', () => {
    window.location.hash = '#material-D3'
    open()
    expect(row('D3').querySelector('details, summary')).toBeNull() // the failed photo
    expect(document.activeElement).toBe(row('D3').querySelector('[data-row-head]'))
    expect(within(row('D3')).getByText("Couldn't read this file")).toBeInTheDocument()
  })

  it('names why a file was not read', () => {
    const failed = (error: 'timeout' | 'interrupted', id: string) => ({ ...fx.materials[2], id, error })
    open(fx.materialsForecast({ materials: [failed('timeout', 'D1'), failed('interrupted', 'D2')] }))
    expect(screen.getByText('Ran out of time')).toBeInTheDocument()
    expect(screen.getByText('Run stopped before it was read')).toBeInTheDocument()
  })

  it('updates with new files and the kept ones, sending keepMaterials only after a removal', async () => {
    const update = vi.spyOn(api, 'updateForecast').mockResolvedValue({ forecast: fx.forecast({ id: 'next', status: 'running' }), credits: fx.credits })
    const { user } = open()
    await user.click(screen.getByText('Add material to the next update'))
    const kept = within(screen.getByRole('list', { name: 'Attached files' })).getAllByRole('listitem')
    expect(kept.map((li) => li.textContent)).toEqual(['PDFQ3 broker note (final).pdfRead earlier', 'CSVsgx-debuts-2026.csvRead earlier']) // the failed D3 is not kept
    expect(screen.getByText('2/5')).toBeInTheDocument()
    const go = screen.getByRole('button', { name: /Update with these materials/ })
    expect(go).toBeDisabled()
    expect(go).toHaveTextContent('· 5 cr')
    fireEvent.change(screen.getByLabelText('Add material'), { target: { files: [new File([CSV_TEXT], 'fresh.csv', { type: 'text/csv' })] } })
    await screen.findByText('CSV · 1 kB')
    expect(go).toHaveTextContent('· 6 cr')
    await user.click(go)
    expect(update).toHaveBeenLastCalledWith('m1234567-dddd', { attachments: [expect.objectContaining({ name: 'fresh.csv', mediaType: 'text/csv' })] })
    await waitFor(() => expect(currentPath()).toBe('/forecasts/next'))
  })

  it('drops a kept material on removal and reports a failed update', async () => {
    const update = vi.spyOn(api, 'updateForecast').mockRejectedValue(new Error('Server is busy'))
    const { user } = open()
    await user.click(screen.getByText('Add material to the next update'))
    await user.click(screen.getByRole('button', { name: 'Remove sgx-debuts-2026.csv' }))
    await user.click(screen.getByRole('button', { name: /Update with these materials/ }))
    expect(update).toHaveBeenCalledWith('m1234567-dddd', { attachments: [], keepMaterials: ['D1'] })
    expect(await screen.findByRole('alert')).toHaveTextContent(/./)
  })

  it('says what a new file costs when the credits left cover the update without it', async () => {
    vi.spyOn(api, 'me').mockResolvedValue({ user: fx.user, credits: { ...fx.credits, used: 495 } })
    vi.spyOn(api, 'updateForecast').mockRejectedValue(new ApiError(429, 'ai_quota', "You have used today's AI credits. They reset at 00:00 UTC."))
    const { user } = open()
    await waitFor(() => expect(creditsUsed()).toBe('495'))
    await user.click(screen.getByText('Add material to the next update'))
    fireEvent.change(screen.getByLabelText('Add material'), { target: { files: [new File([CSV_TEXT], 'fresh.csv', { type: 'text/csv' })] } })
    await screen.findByText('CSV · 1 kB')
    await user.click(screen.getByRole('button', { name: /Update with these materials/ }))
    expect(await screen.findByRole('alert')).toHaveTextContent('This run needs 6 credits and you have 5 left today. Remove a file, or run it after the 00:00 UTC reset.')
  })

  it('offers only "Add material" on an updatable forecast without materials, and nothing otherwise', async () => {
    const { unmount } = open(fx.deepForecast({ resolutionDate: '2026-10-28' }))
    expect(screen.getByRole('region', { name: 'Add material' })).toBeInTheDocument()
    expect(screen.queryByText(/Your materials/)).toBeNull()
    unmount()
    open(fx.deepForecast({ resolutionDate: '2026-10-01' }))
    expect(screen.queryByText('Add material')).toBeNull()
  })
})

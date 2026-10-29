import { act, renderHook, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it } from 'vitest'
import { CSV_TEXT, PDF_BYTES } from '../../test/documents'
import { PNG_DATA, imageBrowser, pngFile } from '../../test/image-browser'
import { JPEG_BYTES } from '../../test/image-fixtures'
import { useAttachmentQueue } from './useAttachmentQueue'

const pdf = (name = 'note.pdf') => new File([new Uint8Array(PDF_BYTES)], name, { type: 'application/pdf' })
const csv = (name = 'prices.csv') => new File([CSV_TEXT], name, { type: 'text/csv' })

let browser: ReturnType<typeof imageBrowser>
beforeEach(() => {
  browser = imageBrowser(JPEG_BYTES)
})

describe('useAttachmentQueue', () => {
  it('reserves slots, converts each file on its own and returns the inputs in selection order', async () => {
    const { result } = renderHook(() => useAttachmentQueue({ max: 5, per: 'per forecast' }))
    act(() => result.current.attach([pdf(), pngFile(), csv()]))
    expect(result.current.files.map((f) => [f.name, f.kind])).toEqual([['note.pdf', 'document'], ['chart.png', 'image'], ['prices.csv', 'document']])
    expect(result.current.processing).toBe(true)
    await waitFor(() => expect(result.current.processing).toBe(false))
    expect(result.current.inputs().map((i) => [i.name, i.mediaType])).toEqual([['note.pdf', 'application/pdf'], ['chart.png', 'image/png'], ['prices.csv', 'text/csv']])
    expect(result.current.inputs()[1].data).toBe(PNG_DATA)
    expect(result.current.files[1].url).toBe('blob:chart-1')
  })

  it('names its unit in the overflow error, takes the slot count at pick time, and clears errors on remove', async () => {
    let max = 2
    const { result, rerender } = renderHook(() => useAttachmentQueue({ max, per: 'per forecast' }))
    act(() => result.current.attach([csv('a.csv'), csv('b.csv'), csv('c.csv'), new File(['x'], 'x.zip', { type: 'application/zip' })]))
    expect(result.current.errors).toEqual([
      'x.zip: Unsupported file type. Attach photos, or PDF, Word, Excel, CSV, text, Markdown or HTML files.',
      'Too many files: attach at most 2 per forecast. Left out: c.csv.',
    ])
    await waitFor(() => expect(result.current.processing).toBe(false))
    act(() => result.current.remove(result.current.files[0].key))
    expect(result.current.errors).toEqual([])
    max = 1
    rerender()
    act(() => result.current.attach([csv('d.csv')]))
    expect(result.current.errors).toEqual(['Too many files: attach at most 1 per forecast. Left out: d.csv.'])
    expect(result.current.files.map((f) => f.name)).toEqual(['b.csv'])
  })

  it('adds each failed file to the errors, ignores picks while disabled, and revokes previews on clear and unmount', async () => {
    let disabled = false
    const { result, rerender, unmount } = renderHook(() => useAttachmentQueue({ max: 5, disabled, per: 'per message' }))
    act(() => result.current.attach([new File(['hello'], 'fake.pdf', { type: 'application/pdf' }), pngFile('one.png')]))
    await waitFor(() => expect(result.current.errors).toEqual(["fake.pdf: This doesn't look like a PDF. Export it as PDF again."]))
    await waitFor(() => expect(result.current.files.map((f) => f.name)).toEqual(['one.png']))
    disabled = true
    rerender()
    act(() => result.current.attach([pngFile('two.png')]))
    expect(result.current.files).toHaveLength(1)
    disabled = false
    rerender()
    act(() => result.current.clear())
    expect([result.current.files, result.current.errors]).toEqual([[], []])
    expect(browser.revoke.mock.calls).toEqual([['blob:chart-1']])
    act(() => result.current.attach([pngFile('three.png')]))
    await waitFor(() => expect(result.current.files[0]?.url).toBe('blob:chart-2'))
    unmount()
    expect(browser.revoke.mock.calls).toEqual([['blob:chart-1'], ['blob:chart-2']])
  })

  it('drops a conversion that lands after clear', async () => {
    let resolve!: (image: unknown) => void
    browser.decode.mockImplementation(() => new Promise((r) => (resolve = r)))
    const { result } = renderHook(() => useAttachmentQueue({ max: 5, per: 'per forecast' }))
    act(() => result.current.attach([pngFile()]))
    await waitFor(() => expect(browser.decode).toHaveBeenCalledOnce())
    act(() => result.current.clear())
    await act(async () => resolve({ width: 1, height: 1, close() {} }))
    expect(result.current.files).toEqual([])
    expect(browser.create).not.toHaveBeenCalled()
  })
})

import { beforeEach, describe, expect, it } from 'vitest'
import { CSV_TEXT, DOCX_BYTES, DOCX_TYPE, PDF_BYTES, XLSX_BYTES, XLSX_TYPE, zip } from '../../test/documents'
import { imageBrowser, photoFile, pngFile } from '../../test/image-browser'
import { JPEG_BYTES } from '../../test/image-fixtures'
import { ACCEPT, attachmentLabel, classify, formatBytes, inputMetadata, messageCost, pastesText, prepareAttachment, readDocument, safeAttachmentMetadata } from './attachments'

const file = (bytes: Uint8Array | string, name: string, type = '') => new File([typeof bytes === 'string' ? bytes : new Uint8Array(bytes)], name, { type })
const base64 = (bytes: Uint8Array) => btoa(String.fromCharCode(...bytes))

beforeEach(() => { imageBrowser(JPEG_BYTES) })
describe('attachment classification', () => {
  it.each([
    ['report.pdf', '', 'application/pdf'], ['memo.DOCX', 'application/octet-stream', DOCX_TYPE], ['sheet.xlsx', '', XLSX_TYPE],
    ['prices.csv', 'application/vnd.ms-excel', 'text/csv'], ['notes.md', '', 'text/markdown'], ['notes.markdown', '', 'text/markdown'],
    ['page.htm', '', 'text/html'], ['a.txt', 'text/plain', 'text/plain'], ['untitled', 'application/pdf', 'application/pdf'],
  ])('reads %s (%s) as a %s document by extension first, then type', (name, type, mediaType) => {
    expect(classify(file('x', name, type))).toEqual({ kind: 'document', mediaType })
  })
  it.each([['IMG_0001.HEIC', ''], ['photo.jpg', 'image/jpeg'], ['shot', 'image/png'], ['loop.gif', 'image/gif'], ['x.avif', '']])('treats %s as a photo', (name, type) => {
    expect(classify(file('x', name, type))).toEqual({ kind: 'image' })
  })
  it.each([['archive.zip', 'application/zip'], ['deck.pptx', ''], ['binary', 'application/octet-stream'], ['old.doc', 'application/msword']])('rejects %s', (name, type) => {
    expect(classify(file('x', name, type))).toBeNull()
  })
  it('offers camera/photos plus every document type in the picker', () => {
    expect(ACCEPT).not.toMatch(/hei[cf]/i) // iOS then converts HEIC photos to JPEG
    expect(ACCEPT.split(',')).toEqual(expect.arrayContaining(['image/*', 'application/pdf', '.pdf', '.docx', '.xlsx', '.csv', '.txt', '.md', '.html', DOCX_TYPE, XLSX_TYPE]))
  })
})

describe('pasting', () => {
  const clip = (data: Record<string, string>) => ({ types: [...Object.keys(data), 'Files'], getData: (type: string) => data[type] ?? '' }) as unknown as DataTransfer
  const picture = file('x', 'image.png', 'image/png')
  it.each([
    ['Excel cells', { 'text/plain': 'BTC\t1', 'text/html': '<html xmlns:o="urn:schemas-microsoft-com:office:office"><body>1</body></html>' }],
    ['a Google Sheets range', { 'text/plain': 'BTC\t1', 'text/html': '<google-sheets-html-origin><table>' }],
    ['Numbers or Word rich text', { 'text/plain': 'Q3 revenue', 'text/rtf': '{\\rtf1 Q3}' }],
  ])('pastes %s as text instead of attaching their picture', (_, data) => {
    expect(pastesText(clip(data), [picture])).toBe(true)
  })
  it.each([
    ['a screenshot', {}],
    ['a copied web image', { 'text/html': '<img src="https://example.com/chart.png">' }],
    ['a file copied in Finder', { 'text/plain': 'chart.png' }],
    ['blank text beside a table picture', { 'text/plain': ' ', 'text/html': '<table>' }],
  ])('attaches %s', (_, data) => {
    expect(pastesText(clip(data), [picture])).toBe(false)
  })
  it('attaches documents even beside rich text', () => {
    expect(pastesText(clip({ 'text/plain': 'x', 'text/rtf': 'x' }), [file('%PDF-', 'a.pdf', 'application/pdf')])).toBe(false)
  })
})

describe('document reading', () => {
  it.each([
    [PDF_BYTES, 'q3 report.pdf', 'application/pdf'], [DOCX_BYTES, 'memo.docx', DOCX_TYPE], [XLSX_BYTES, 'model.xlsx', XLSX_TYPE],
    [new TextEncoder().encode(CSV_TEXT), 'prices.csv', 'text/csv'], [new TextEncoder().encode('# Héllo 😀\n'), 'C:\\notes\\plan.md', 'text/markdown'],
  ] as const)('reads a valid synthetic %#: %s to base64 with its canonical type', async (bytes, name, mediaType) => {
    const result = await prepareAttachment(file(bytes, name))
    expect(result).toEqual({ input: { kind: 'document', mediaType, name: name.split('\\').at(-1), data: base64(bytes) }, size: bytes.length })
    expect(inputMetadata(result.input)).toEqual({ kind: 'document', mediaType, name: name.split('\\').at(-1), size: bytes.length })
  })
  it('rejects mismatched bytes with specific, actionable messages', async () => {
    await expect(readDocument(file('hello', 'fake.pdf'), 'application/pdf')).rejects.toThrow("doesn't look like a PDF")
    await expect(readDocument(file(XLSX_BYTES, 'swap.docx'), DOCX_TYPE)).rejects.toThrow('valid Word file')
    await expect(readDocument(file(zip({ 'a.txt': 'x' }), 'bare.xlsx'), XLSX_TYPE)).rejects.toThrow('valid Excel file')
    await expect(readDocument(file(PDF_BYTES, 'pdf.docx'), DOCX_TYPE)).rejects.toThrow('valid Word file')
    await expect(readDocument(file(new Uint8Array([104, 0, 105]), 'nul.txt'), 'text/plain')).rejects.toThrow('UTF-8')
    await expect(readDocument(file(new Uint8Array([0xc3, 0x28]), 'latin.csv'), 'text/csv')).rejects.toThrow('UTF-8')
    await expect(readDocument(file('', 'empty.txt'), 'text/plain')).rejects.toThrow('empty')
  })
  it('accepts exactly 4,500,000 bytes and rejects one more before reading', async () => {
    const at = new Uint8Array(4_500_000).fill(97)
    expect((await readDocument(file(at, 'big.txt'), 'text/plain')).size).toBe(4_500_000)
    await expect(readDocument(file(new Uint8Array(4_500_001).fill(97), 'huge.txt'), 'text/plain')).rejects.toThrow('Too big (4.6 MB). Documents can be at most 4.5 MB.')
    await expect(readDocument(file(new Uint8Array(4_549_999), 'big.pdf'), 'application/pdf')).rejects.toThrow('Too big (4.6 MB)') // never "4.5 MB"
    await expect(readDocument(file(new Uint8Array(12_340_000), 'scan.pdf'), 'application/pdf')).rejects.toThrow('Too big (12.4 MB)')
  })
  it('routes photos to conversion and unsupported files to a clear error', async () => {
    expect((await prepareAttachment(photoFile())).input).toMatchObject({ kind: 'image', mediaType: 'image/jpeg' })
    expect((await prepareAttachment(pngFile())).blob).toBeInstanceOf(Blob)
    await expect(prepareAttachment(file('x', 'a.zip', 'application/zip'))).rejects.toThrow('Unsupported file type')
  })
})

describe('attachment metadata and cost', () => {
  it('builds bounded metadata from an allowlist and drops all historical bytes and URLs', () => {
    const image = { kind: 'image', mediaType: 'image/png', name: '../\u0000chart.png', size: 100, data: 'secret', url: 'https://example.com' }
    const document = { kind: 'document', mediaType: 'application/pdf', name: 'report.pdf', size: 4_500_000, pages: 3, data: 'secret' }
    expect(safeAttachmentMetadata([image, { ...image, size: 1_500_001 }, document, { ...document, size: 4_500_001 }, { ...document, mediaType: 'application/zip' }, { ...document, pages: 0, name: '' }, { ...image, mediaType: 'image/gif' }])).toEqual([
      { kind: 'image', mediaType: 'image/png', name: 'chart.png', size: 100 },
      { kind: 'document', mediaType: 'application/pdf', name: 'report.pdf', size: 4_500_000, pages: 3 },
    ])
    expect(safeAttachmentMetadata([{ ...document, pages: 1.5 }])).toEqual([{ kind: 'document', mediaType: 'application/pdf', name: 'report.pdf', size: 4_500_000 }])
    expect(safeAttachmentMetadata(Array.from({ length: 7 }, () => image))).toHaveLength(5)
    expect(safeAttachmentMetadata(null)).toEqual([])
    expect(safeAttachmentMetadata([null, { ...image, size: '1' }])).toEqual([])
    expect(inputMetadata({ kind: 'image', mediaType: 'image/jpeg', name: 'chart', data: '/9j/AA==' }).size).toBe(4)
    expect(inputMetadata({ kind: 'document', mediaType: 'text/plain', name: '', data: 'YWI=' })).toEqual({ kind: 'document', mediaType: 'text/plain', name: 'document', size: 2 })
  })
  it('charges 2 for text, 3 with attachments and 1 more per document after the first', () => {
    expect(messageCost([])).toBe(2)
    expect(messageCost([{ kind: 'image' }, { kind: 'image' }])).toBe(3)
    expect(messageCost([{ kind: 'document' }])).toBe(3)
    expect(messageCost([{ kind: 'image' }, { kind: 'document' }, { kind: 'document' }])).toBe(4)
    expect(messageCost(Array.from({ length: 5 }, () => ({ kind: 'document' })))).toBe(7)
  })
  it('formats sizes and labels', () => {
    expect([formatBytes(1), formatBytes(999_999), formatBytes(1_000_000), formatBytes(4_499_999)]).toEqual(['1 kB', '1000 kB', '1.0 MB', '4.4 MB'])
    expect(attachmentLabel({ kind: 'document', mediaType: XLSX_TYPE })).toBe('Excel')
    expect(attachmentLabel({ kind: 'image', mediaType: 'image/png' })).toBe('Image')
    expect(attachmentLabel({ kind: 'document', mediaType: 'x/unknown' })).toBe('File')
  })
})

// Chat attachments: photos (images.ts converts them) and documents, read to base64 in the browser.
import type { ChatAttachmentInput, ChatAttachmentMetadata, ChatDocumentInput, ChatDocumentMediaType, ChatImageMetadata } from '../../lib/types'
import { MAX_IMAGE_BYTES, normalizeImage, readBase64, readBytes, safeImageName } from './images'

export const MAX_ATTACHMENTS = 5 // images and documents combined, per message
export const MAX_DOCUMENT_BYTES = 4_500_000 // the API's document bound

type DocumentType = { label: string; short: string; extensions: string[]; check: 'pdf' | 'zip' | 'text'; part?: string }
export const DOCUMENTS: Record<ChatDocumentMediaType, DocumentType> = {
  'application/pdf': { label: 'PDF', short: 'PDF', extensions: ['pdf'], check: 'pdf' },
  'application/vnd.openxmlformats-officedocument.wordprocessingml.document': { label: 'Word', short: 'DOCX', extensions: ['docx'], check: 'zip', part: 'word/' },
  'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet': { label: 'Excel', short: 'XLSX', extensions: ['xlsx'], check: 'zip', part: 'xl/' },
  'text/csv': { label: 'CSV', short: 'CSV', extensions: ['csv'], check: 'text' },
  'text/plain': { label: 'Text', short: 'TXT', extensions: ['txt'], check: 'text' },
  'text/markdown': { label: 'Markdown', short: 'MD', extensions: ['md', 'markdown'], check: 'text' },
  'text/html': { label: 'HTML', short: 'HTML', extensions: ['html', 'htm'], check: 'text' },
}
const IMAGE_MEDIA = new Set<string>(['image/png', 'image/jpeg', 'image/webp', 'image/gif'])
const IMAGE_EXTENSIONS = new Set(['png', 'jpg', 'jpeg', 'webp', 'gif', 'heic', 'heif', 'avif', 'bmp'])

/** The picker's accept list: every photo the device offers (camera and library on phones) plus the document types,
 * by MIME type and extension, since browsers often leave .md or .csv untyped. HEIC is left to image/*: naming it
 * makes iOS hand over the original HEIC instead of a JPEG, which only recent Safari can decode. */
export const ACCEPT = ['image/*', ...Object.entries(DOCUMENTS).flatMap(([type, d]) => [type, ...d.extensions.map(e => `.${e}`)])].join(',')
export const UNSUPPORTED = 'Unsupported file type. Attach photos, or PDF, Word, Excel, CSV, text, Markdown or HTML files.'

const extension = (name: string) => (name.includes('.') ? name.split('.').at(-1)!.toLowerCase() : '')

/** Images by MIME type or photo extension; documents by extension first (browsers type .csv as Excel on Windows). */
export function classify(file: File): { kind: 'image' } | { kind: 'document'; mediaType: ChatDocumentMediaType } | null {
  const ext = extension(file.name)
  const byExtension = (Object.keys(DOCUMENTS) as ChatDocumentMediaType[]).find(type => DOCUMENTS[type].extensions.includes(ext))
  if (byExtension) return { kind: 'document', mediaType: byExtension }
  if (file.type.startsWith('image/') || IMAGE_EXTENSIONS.has(ext)) return { kind: 'image' }
  if (file.type in DOCUMENTS) return { kind: 'document', mediaType: file.type as ChatDocumentMediaType }
  return null
}

const startsWith = (bytes: Uint8Array, prefix: string) => prefix.length <= bytes.length && Array.from(prefix).every((c, i) => bytes[i] === c.charCodeAt(0))
const contains = (bytes: Uint8Array, needle: string) => {
  const first = needle.charCodeAt(0)
  for (let i = bytes.indexOf(first); i !== -1 && i + needle.length <= bytes.length; i = bytes.indexOf(first, i + 1)) {
    if (startsWith(bytes.subarray(i), needle)) return true
  }
  return false
}

/** Cells or text copied from Excel, Word, Google Sheets or Numbers also carry a picture of the selection; that
 * paste is the text, not an image to attach (and pay for). */
export function pastesText(data: DataTransfer, files: readonly File[]): boolean {
  if (!files.every(file => file.type.startsWith('image/')) || !data.getData('text/plain').trim()) return false
  return data.types.includes('text/rtf') || /<table[\s>]|urn:schemas-microsoft-com:office/i.test(data.getData('text/html'))
}

/** Check a document's bytes match its type (the API checks again) and read it to base64. */
export async function readDocument(file: File, mediaType: ChatDocumentMediaType): Promise<{ input: ChatDocumentInput; size: number }> {
  const type = DOCUMENTS[mediaType]
  if (!file.size) throw new Error('This file is empty.')
  // rounded up, so a file just over the limit never reads as "4.5 MB"
  if (file.size > MAX_DOCUMENT_BYTES) throw new Error(`Too big (${(Math.ceil(file.size / 100_000) / 10).toFixed(1)} MB). Documents can be at most 4.5 MB.`)
  const bytes = await readBytes(file)
  if (type.check === 'pdf' && !startsWith(bytes, '%PDF-')) throw new Error("This doesn't look like a PDF. Export it as PDF again.")
  // Office files are ZIP packages; their [Content_Types].xml and main part names are stored uncompressed.
  if (type.check === 'zip' && !(startsWith(bytes, 'PK\x03\x04') && contains(bytes, '[Content_Types].xml') && contains(bytes, type.part!))) {
    throw new Error(`This isn't a valid ${type.label} file. Save it as .${type.extensions[0]} again.`)
  }
  if (type.check === 'text') {
    let ok = !bytes.includes(0)
    try { new TextDecoder('utf-8', { fatal: true }).decode(bytes) } catch { ok = false }
    if (!ok) throw new Error('Text files must be UTF-8 text. Save it as UTF-8 and attach it again.')
  }
  return { input: { kind: 'document', mediaType, name: safeImageName(file.name, 'document'), data: await readBase64(file) }, size: file.size }
}

/** One picked, pasted or dropped file, ready to send; `blob` is an image's preview. */
export async function prepareAttachment(file: File): Promise<{ input: ChatAttachmentInput; size: number; blob?: Blob }> {
  const kind = classify(file)
  if (!kind) throw new Error(UNSUPPORTED)
  return kind.kind === 'image' ? normalizeImage(file) : readDocument(file, kind.mediaType)
}

/** Construct a fresh allowlisted DTO: neither history nor a display turn ever copies wire data. */
export function safeAttachmentMetadata(value: unknown): ChatAttachmentMetadata[] {
  if (!Array.isArray(value)) return []
  return value.slice(0, MAX_ATTACHMENTS).flatMap((item): ChatAttachmentMetadata[] => {
    if (!item || typeof item.name !== 'string' || !Number.isInteger(item.size) || item.size < 1) return []
    if (item.kind === 'image' && IMAGE_MEDIA.has(item.mediaType) && item.size <= MAX_IMAGE_BYTES) {
      return [{ kind: 'image', mediaType: item.mediaType, name: safeImageName(item.name), size: item.size }]
    }
    if (item.kind === 'document' && Object.hasOwn(DOCUMENTS, item.mediaType) && item.size <= MAX_DOCUMENT_BYTES) {
      const pages = Number.isInteger(item.pages) && item.pages > 0 && item.pages <= 100_000 ? { pages: item.pages as number } : {}
      return [{ kind: 'document', mediaType: item.mediaType, name: safeImageName(item.name, 'document'), size: item.size, ...pages }]
    }
    return []
  })
}

export function inputMetadata(input: ChatAttachmentInput): ChatAttachmentMetadata {
  const padding = input.data.endsWith('==') ? 2 : input.data.endsWith('=') ? 1 : 0
  const size = input.data.length * 3 / 4 - padding
  return input.kind === 'image'
    ? { kind: 'image', mediaType: input.mediaType, name: safeImageName(input.name), size } satisfies ChatImageMetadata
    : { kind: 'document', mediaType: input.mediaType, name: safeImageName(input.name, 'document'), size }
}

/** Contract cost: 2 credits; 3 with any attachment, plus 1 for each document after the first. */
export function messageCost(attachments: readonly { kind: string }[]): number {
  if (!attachments.length) return 2
  return 3 + Math.max(0, attachments.filter(a => a.kind === 'document').length - 1)
}

export const formatBytes = (n: number) => (n < 1_000_000 ? `${Math.max(1, Math.ceil(n / 1000))} kB` : `${(Math.floor(n / 100_000) / 10).toFixed(1)} MB`)
export const attachmentLabel = (a: { kind: string; mediaType: string }) => (a.kind === 'image' ? 'Image' : (DOCUMENTS[a.mediaType as ChatDocumentMediaType]?.label ?? 'File'))
export const attachmentShort = (a: { kind: string; mediaType: string }) => (a.kind === 'image' ? 'IMG' : (DOCUMENTS[a.mediaType as ChatDocumentMediaType]?.short ?? 'FILE'))

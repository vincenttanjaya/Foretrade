import type { ChatImageInput } from '../../lib/types'

export const MAX_IMAGE_BYTES = 1_500_000
const LONG_SIDE = 2048 // converted photos are at most this many pixels on their long side
const QUALITIES = [0.9, 0.82, 0.74, 0.66, 0.58, 0.5] // JPEG re-encodes step down until the photo fits
const SHRINKS = 3 // then the canvas shrinks by 30%, at most this many times
const MAX_SIDE = 20_000
const MAX_PIXELS = 64_000_000 // phone photos reach 48 MP; panoramas are wider still

const PHOTO_ERROR = "Couldn't read this photo. Export it as JPEG or PNG and attach it again."

type ImageType = ChatImageInput['mediaType']

export const runeLength = (text: string) => Array.from(text).length
export function safeImageName(name: string, fallback = 'image'): string {
  const base = (name.replace(/\\/g, '/').split('/').at(-1) ?? '').replace(/\p{Cc}/gu, '').trim()
  return Array.from(!base || base === '.' || base === '..' ? fallback : base).slice(0, 120).join('')
}

function read(blob: Blob, asDataURL = false): Promise<ArrayBuffer | string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onerror = () => reject(new Error("Couldn't read this file. Try attaching it again."))
    reader.onload = () => resolve(reader.result as ArrayBuffer | string)
    if (asDataURL) reader.readAsDataURL(blob)
    else reader.readAsArrayBuffer(blob)
  })
}
export const readBytes = async (blob: Blob) => new Uint8Array(await read(blob) as ArrayBuffer)
/** Standard base64 of the blob's bytes, without the data URL prefix. */
export async function readBase64(blob: Blob): Promise<string> {
  const dataURL = await read(blob, true) as string
  return dataURL.slice(dataURL.indexOf(',') + 1)
}

function dimensions(width: number, height: number) {
  if (!Number.isInteger(width) || !Number.isInteger(height) || width < 1 || height < 1) throw new Error(PHOTO_ERROR)
  if (width > MAX_SIDE || height > MAX_SIDE || width * height > MAX_PIXELS) {
    throw new Error('This photo is too large to process: at most 20,000 pixels per side and 64 megapixels.')
  }
}

type Header = { mediaType?: ImageType; width: number; height: number; animated: boolean; intact: boolean }

/** Sniff the real format from a bounded header, never trusting the file's MIME type, and read its dimensions
 * before asking the browser to allocate decoded pixels. Formats it can't parse (HEIC, AVIF…) go to the decoder. */
async function sniff(file: File): Promise<Header> {
  const bytes = await readBytes(file.slice(0, 262_144))
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength)
  const text = (start: number, count: number) => String.fromCharCode(...bytes.slice(start, start + count))
  const header: Header = { width: 0, height: 0, animated: false, intact: true }
  if (bytes.length >= 24 && text(0, 8) === '\x89PNG\r\n\x1a\n' && text(12, 4) === 'IHDR') {
    Object.assign(header, { mediaType: 'image/png', width: view.getUint32(16), height: view.getUint32(20) })
    for (let p = 8; p + 8 <= bytes.length;) { // APNG declares acTL before its first IDAT
      const size = view.getUint32(p), kind = text(p + 4, 4)
      if (kind === 'acTL') header.animated = true
      if (kind === 'IDAT' || kind === 'acTL') break
      p += 12 + size
    }
  } else if (bytes[0] === 0xff && bytes[1] === 0xd8) {
    header.mediaType = 'image/jpeg'
    for (let p = 2; p + 4 <= bytes.length;) {
      if (bytes[p++] !== 0xff) break
      while (bytes[p] === 0xff) p++
      const marker = bytes[p++]
      if (marker === 0xd9 || marker === 0xda || p + 2 > bytes.length) break
      const size = view.getUint16(p)
      if (size < 2 || p + size > bytes.length) break
      if ([0xc0, 0xc1, 0xc2, 0xc3, 0xc5, 0xc6, 0xc7, 0xc9, 0xca, 0xcb, 0xcd, 0xce, 0xcf].includes(marker) && size >= 8) {
        header.height = view.getUint16(p + 3); header.width = view.getUint16(p + 5); break
      }
      p += size
    }
  } else if (bytes.length >= 12 && text(0, 4) === 'RIFF' && text(8, 4) === 'WEBP') {
    header.mediaType = 'image/webp'
    header.intact = view.getUint32(4, true) + 8 === file.size
    for (let p = 12; p + 8 <= bytes.length;) {
      const kind = text(p, 4), size = view.getUint32(p + 4, true)
      p += 8
      if (p + size > file.size) break
      if (kind === 'ANIM' || kind === 'ANMF' || (kind === 'VP8X' && p + 10 <= bytes.length && bytes[p] & 2)) header.animated = true
      if (kind === 'VP8X' && size === 10 && p + 10 <= bytes.length) {
        header.width = 1 + bytes[p + 4] + (bytes[p + 5] << 8) + (bytes[p + 6] << 16)
        header.height = 1 + bytes[p + 7] + (bytes[p + 8] << 8) + (bytes[p + 9] << 16)
      } else if (kind === 'VP8 ' && size >= 10 && p + 10 <= bytes.length && text(p + 3, 3) === '\x9d\x01\x2a') {
        header.width = view.getUint16(p + 6, true) & 0x3fff; header.height = view.getUint16(p + 8, true) & 0x3fff
      } else if (kind === 'VP8L' && size >= 5 && p + 5 <= bytes.length && bytes[p] === 0x2f) {
        const bits = view.getUint32(p + 1, true)
        header.width = (bits & 0x3fff) + 1; header.height = ((bits >>> 14) & 0x3fff) + 1
      }
      p += size + (size & 1)
    }
  } else if (bytes.length >= 10 && (text(0, 6) === 'GIF87a' || text(0, 6) === 'GIF89a')) {
    Object.assign(header, { mediaType: 'image/gif', width: view.getUint16(6, true), height: view.getUint16(8, true), animated: true }) // frames unknown: always converted
  }
  if (header.mediaType && (!header.width || !header.height)) header.intact = false
  return header
}

type Decoded = { source: CanvasImageSource; width: number; height: number; close: () => void }

function decodeElement(file: File): Promise<Decoded> {
  const url = URL.createObjectURL(file)
  const image = new Image()
  return new Promise<Decoded>((resolve, reject) => {
    image.onload = () => resolve({ source: image, width: image.naturalWidth, height: image.naturalHeight, close: () => URL.revokeObjectURL(url) })
    image.onerror = () => { URL.revokeObjectURL(url); reject(new Error('decode')) }
    image.src = url
  })
}

/** createImageBitmap applies EXIF orientation; an <img> is the fallback (and decodes HEIC on Safari versions whose
 * bitmap path can't). */
async function decode(file: File): Promise<Decoded> {
  if (typeof createImageBitmap === 'function') {
    try {
      const bitmap = await createImageBitmap(file)
      return { source: bitmap, width: bitmap.width, height: bitmap.height, close: () => bitmap.close() }
    } catch { /* try the element decoder */ }
  }
  return decodeElement(file)
}

/**
 * Any photo the browser can decode becomes an image the API accepts: a small static PNG or WebP is sent as is;
 * everything else (HEIC, large JPEG/PNG, GIF, animated WebP's first frame) is drawn to a canvas with its long side
 * at most 2048 px and re-encoded as JPEG at falling quality, then smaller, until it is at most 1.5 MB.
 */
export async function normalizeImage(file: File): Promise<{ input: ChatImageInput; size: number; blob: Blob }> {
  const name = safeImageName(file.name)
  if (!file.size) throw new Error('This file is empty.')
  const header = await sniff(file)
  // PNG and GIF declare their size up front; a JPEG's frame header can sit past the sniffed bytes
  if (header.mediaType === 'image/png' || header.mediaType === 'image/gif' || (header.width && header.height)) dimensions(header.width, header.height)
  let image: Decoded
  try { image = await decode(file) } catch { throw new Error(PHOTO_ERROR) }
  try {
    dimensions(image.width, image.height)
    const keep = (header.mediaType === 'image/png' || header.mediaType === 'image/webp') && header.intact && !header.animated &&
      file.size <= MAX_IMAGE_BYTES && Math.max(image.width, image.height) <= LONG_SIDE
    if (keep) return { input: { kind: 'image', mediaType: header.mediaType!, name, data: await readBase64(file) }, size: file.size, blob: file }
    const canvas = document.createElement('canvas')
    const ctx = canvas.getContext('2d')
    if (!ctx) throw new Error("Couldn't convert this photo in your browser.")
    let scale = Math.min(1, LONG_SIDE / Math.max(image.width, image.height))
    for (let shrink = 0; shrink <= SHRINKS; shrink++, scale *= 0.7) {
      canvas.width = Math.max(1, Math.round(image.width * scale))
      canvas.height = Math.max(1, Math.round(image.height * scale))
      ctx.fillStyle = '#fff' // transparent charts remain legible after conversion to JPEG
      ctx.fillRect(0, 0, canvas.width, canvas.height)
      ctx.imageSmoothingQuality = 'high'
      ctx.drawImage(image.source, 0, 0, canvas.width, canvas.height)
      for (const quality of QUALITIES) {
        const blob = await new Promise<Blob | null>(resolve => canvas.toBlob(resolve, 'image/jpeg', quality))
        if (!blob) throw new Error("Couldn't convert this photo. Try another one.")
        if (blob.type !== 'image/jpeg' || blob.size === 0) throw new Error("Couldn't convert this photo to JPEG.")
        if (blob.size > MAX_IMAGE_BYTES) continue
        const data = await readBase64(blob)
        if (!data.startsWith('/9j/')) throw new Error("Couldn't convert this photo to JPEG.")
        return { input: { kind: 'image', mediaType: 'image/jpeg', name, data }, size: blob.size, blob }
      }
    }
    throw new Error("Couldn't shrink this photo below 1.5 MB. Crop it and attach it again.")
  } finally { image.close() }
}

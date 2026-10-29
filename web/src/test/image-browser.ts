import { vi } from 'vitest'

// Valid one-pixel PNG; actual JPEG fixture below is generated once, never by the production encoder.
export const PNG_DATA = 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a1ioAAAAASUVORK5CYII='
export const pngFile = (name = 'chart.png') => new File([Uint8Array.from(atob(PNG_DATA), c => c.charCodeAt(0))], name, { type: 'image/png' })
/** A file no header sniffer knows (like HEIC): only the browser decoder can read it, so it always converts to JPEG. */
export const photoFile = (name = 'photo.heic', size = 64) => new File([new Uint8Array(size).fill(7)], name, { type: name.endsWith('.heic') ? 'image/heic' : '' })

/** jsdom has no native image decoder/canvas encoder. Only those browser boundaries are faked. */
export function imageBrowser(jpeg: Uint8Array, dimensions = { width: 1, height: 1 }) {
  const close = vi.fn()
  const decode = vi.fn().mockResolvedValue({ ...dimensions, close })
  vi.stubGlobal('createImageBitmap', decode)
  const draw = vi.fn()
  vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue({ drawImage: draw, fillRect() {}, fillStyle: '', imageSmoothingQuality: 'low' } as unknown as CanvasRenderingContext2D)
  const canvases: HTMLCanvasElement[] = []
  const encode = vi.spyOn(HTMLCanvasElement.prototype, 'toBlob').mockImplementation(function (this: HTMLCanvasElement, callback) {
    canvases.push(this)
    callback(new Blob([new Uint8Array(jpeg)], { type: 'image/jpeg' }))
  })
  // The <img> fallback decoder: fails like an undecodable file unless a test makes it load.
  const element = { load: false, width: 1, height: 1 }
  class NativeImage {
    naturalWidth = 0; naturalHeight = 0
    onload: (() => void) | null = null; onerror: (() => void) | null = null
    set src(_value: string) {
      queueMicrotask(() => {
        if (!element.load) return this.onerror?.()
        this.naturalWidth = element.width; this.naturalHeight = element.height
        this.onload?.()
      })
    }
  }
  vi.stubGlobal('Image', NativeImage)
  let sequence = 0
  const create = vi.fn(() => `blob:chart-${++sequence}`)
  const revoke = vi.fn()
  Object.defineProperties(URL, {
    createObjectURL: { value: create, configurable: true, writable: true },
    revokeObjectURL: { value: revoke, configurable: true, writable: true },
  })
  return { close, decode, draw, encode, create, revoke, canvases, element }
}

import { beforeEach, describe, expect, it, vi } from 'vitest'
import { PNG_DATA, imageBrowser, photoFile, pngFile } from '../../test/image-browser'
import { JPEG_BYTES, WEBP_BYTES } from '../../test/image-fixtures'
import { normalizeImage, safeImageName } from './images'

const JPEG_DATA = btoa(String.fromCharCode(...JPEG_BYTES))
const blobOf = (size: number) => { const bytes = new Uint8Array(size); bytes.set(JPEG_BYTES); return new Blob([bytes], { type: 'image/jpeg' }) }
const gif = (width: number, height: number) => {
  const bytes = new Uint8Array(32); bytes.set(new TextEncoder().encode('GIF89a'))
  new DataView(bytes.buffer).setUint16(6, width, true); new DataView(bytes.buffer).setUint16(8, height, true)
  return new File([bytes], 'loop.gif', { type: 'image/gif' })
}

let browser: ReturnType<typeof imageBrowser>
beforeEach(() => { browser = imageBrowser(JPEG_BYTES) })
describe('photo normalization', () => {
  it('sends a small static PNG as is after the browser decodes it, keeping a safe bounded Unicode filename', async () => {
    const result = await normalizeImage(pngFile('C:\\folder\\chart\u0000.png'))
    expect(result.input).toEqual({ kind: 'image', mediaType: 'image/png', name: 'chart.png', data: PNG_DATA })
    expect(result.blob.type).toBe('image/png')
    expect(browser.decode).toHaveBeenCalledOnce()
    expect(browser.encode).not.toHaveBeenCalled()
    expect(browser.close).toHaveBeenCalledOnce()
  })
  it('sends a small static WebP as is', async () => {
    const result = await normalizeImage(new File([new Uint8Array(WEBP_BYTES)], 'chart.webp', { type: 'image/webp' }))
    expect(result.input.mediaType).toBe('image/webp')
    expect(browser.encode).not.toHaveBeenCalled()
  })
  it('converts a photo no header parser knows (HEIC) to real JPEG bytes, sniffing content over the MIME type', async () => {
    const result = await normalizeImage(photoFile())
    expect(result.input).toEqual({ kind: 'image', mediaType: 'image/jpeg', name: 'photo.heic', data: JPEG_DATA })
    expect(result.size).toBe(JPEG_BYTES.length)
    expect(browser.encode).toHaveBeenCalledExactlyOnceWith(expect.any(Function), 'image/jpeg', 0.9)
    const lie = await normalizeImage(new File([new Uint8Array(JPEG_BYTES)], 'lie.png', { type: 'image/png' }))
    expect(lie.input.mediaType).toBe('image/jpeg') // the bytes are JPEG: re-encoded, never sent as "PNG"
  })
  it.each([[6000, 4000, 2048, 1365], [4000, 6000, 1365, 2048], [3136, 1568, 2048, 1024], [512, 256, 512, 256]])('fits %sx%s to a 2048 px long side without upscaling', async (width, height, expectedW, expectedH) => {
    browser.decode.mockResolvedValue({ width, height, close: browser.close })
    await normalizeImage(photoFile())
    expect([browser.canvases[0].width, browser.canvases[0].height]).toEqual([expectedW, expectedH])
  })
  it('converts large PNGs, GIFs and animated WebP instead of rejecting them', async () => {
    browser.decode.mockResolvedValue({ width: 2049, height: 10, close: browser.close })
    expect((await normalizeImage(pngFile())).input.mediaType).toBe('image/jpeg')
    browser.decode.mockResolvedValue({ width: 4, height: 4, close: browser.close })
    expect((await normalizeImage(gif(4, 4))).input.mediaType).toBe('image/jpeg')
    const bytes = new Uint8Array(30)
    bytes.set(new TextEncoder().encode('RIFF'), 0); bytes.set(new TextEncoder().encode('WEBPVP8X'), 8)
    const header = new DataView(bytes.buffer); header.setUint32(4, 22, true); header.setUint32(16, 10, true); bytes[20] = 2
    expect((await normalizeImage(new File([bytes], 'animated.webp', { type: 'image/webp' }))).input.mediaType).toBe('image/jpeg')
    const big = new Uint8Array(1_500_001); big.set(Uint8Array.from(atob(PNG_DATA), c => c.charCodeAt(0)))
    expect((await normalizeImage(new File([big], 'big.png', { type: 'image/png' }))).input.mediaType).toBe('image/jpeg')
  })
  it('steps quality down, then size, until the JPEG fits 1,500,000 bytes', async () => {
    const sizes = [2_000_000, 1_800_000, 1_500_000]
    browser.encode.mockImplementation(callback => callback(blobOf(sizes.shift() ?? 10)))
    browser.decode.mockResolvedValue({ width: 4000, height: 3000, close: browser.close })
    expect((await normalizeImage(photoFile())).size).toBe(1_500_000)
    expect(browser.encode.mock.calls.map(call => call[2])).toEqual([0.9, 0.82, 0.74])
    browser.encode.mockClear()
    let calls = 0
    browser.encode.mockImplementation(callback => callback(blobOf(++calls <= 6 ? 1_600_000 : 900)))
    expect((await normalizeImage(photoFile())).size).toBe(900)
    expect(browser.encode).toHaveBeenCalledTimes(7)
    expect((browser.encode.mock.contexts.at(-1) as HTMLCanvasElement).width).toBe(Math.round(2048 * 0.7))
  })
  it('gives up with a clear message when even the smallest JPEG is over 1.5 MB', async () => {
    browser.encode.mockImplementation(callback => callback(blobOf(1_500_001)))
    await expect(normalizeImage(photoFile())).rejects.toThrow('below 1.5 MB')
    expect(browser.encode).toHaveBeenCalledTimes(24)
  })
  it('rejects failed encoding and a browser returning a PNG instead of JPEG', async () => {
    browser.encode.mockImplementation(callback => callback(null))
    await expect(normalizeImage(photoFile())).rejects.toThrow('convert')
    browser.encode.mockImplementation(callback => callback(new Blob(['not JPEG'], { type: 'image/png' })))
    await expect(normalizeImage(photoFile())).rejects.toThrow('JPEG')
    browser.encode.mockImplementation(callback => callback(new Blob(['not JPEG'], { type: 'image/jpeg' })))
    await expect(normalizeImage(photoFile())).rejects.toThrow('JPEG')
  })
  it('accepts a 48 MP phone photo but rejects one beyond the pixel bound, closing its bitmap', async () => {
    browser.decode.mockResolvedValue({ width: 8064, height: 6048, close: browser.close })
    expect((await normalizeImage(photoFile())).input.mediaType).toBe('image/jpeg')
    browser.decode.mockResolvedValue({ width: 20_001, height: 1, close: browser.close })
    await expect(normalizeImage(photoFile())).rejects.toThrow('too large to process')
    expect(browser.close).toHaveBeenCalledTimes(2)
  })
  it.each([[20_001, 1], [10_000, 6401], [0, 1]])('bounds PNG header dimensions %s × %s before allocating a bitmap', async (width, height) => {
    const file = pngFile(); const bytes = Uint8Array.from(atob('iVBORw0KGgoAAAANSUhEUgAAAAEAAAAB'), c => c.charCodeAt(0))
    const header = new DataView(bytes.buffer); header.setUint32(16, width); header.setUint32(20, height)
    const oversized = new File([bytes], file.name, { type: file.type })
    const slice = vi.spyOn(oversized, 'slice')
    await expect(normalizeImage(oversized)).rejects.toThrow()
    expect(slice).toHaveBeenCalledExactlyOnceWith(0, 262_144)
    expect(browser.decode).not.toHaveBeenCalled()
  })
  it('bounds GIF header dimensions too, and rejects empty files', async () => {
    await expect(normalizeImage(gif(20_001, 1))).rejects.toThrow('too large to process')
    await expect(normalizeImage(new File([], 'empty.jpg', { type: 'image/jpeg' }))).rejects.toThrow('empty')
    expect(browser.decode).not.toHaveBeenCalled()
  })
  it('falls back to the <img> decoder when the bitmap path fails, and reports an unreadable photo', async () => {
    browser.decode.mockRejectedValue(new Error('no HEIC bitmap decoder'))
    await expect(normalizeImage(photoFile())).rejects.toThrow("Couldn't read this photo")
    expect(browser.revoke).toHaveBeenCalledOnce()
    browser.element.load = true; browser.element.width = 3000; browser.element.height = 4000
    expect((await normalizeImage(photoFile())).input.mediaType).toBe('image/jpeg')
    expect([browser.canvases[0].width, browser.canvases[0].height]).toEqual([1536, 2048])
    expect(browser.revoke).toHaveBeenCalledTimes(2)
  })
  it('reports a missing canvas context without leaking bitmap ownership', async () => {
    vi.mocked(HTMLCanvasElement.prototype.getContext).mockReturnValue(null)
    await expect(normalizeImage(photoFile())).rejects.toThrow('browser')
    expect(browser.close).toHaveBeenCalledOnce()
  })
  it('uses the element decoder alone where createImageBitmap is missing', async () => {
    vi.stubGlobal('createImageBitmap', undefined)
    browser.element.load = true
    expect((await normalizeImage(photoFile())).size).toBe(JPEG_BYTES.length)
    expect(browser.create).toHaveBeenCalledOnce()
    expect(browser.revoke.mock.calls).toEqual([['blob:chart-1']])
  })
  it('bounds names', () => {
    expect(safeImageName('😀'.repeat(121))).toBe('😀'.repeat(120))
    expect(safeImageName('..')).toBe('image')
    expect(safeImageName('', 'document')).toBe('document')
  })
})

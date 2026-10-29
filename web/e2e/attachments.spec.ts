import { expect, expectAccessible, expectNoHorizontalScroll, httpError, test, type Page } from './fixtures'
import { API_URL, SEED } from './mock-api'
import { CSV_TEXT, PDF_BYTES, XLSX_BYTES, XLSX_TYPE } from '../src/test/documents'
import type { ChatStreamRequest } from '../src/lib/types'

type Upload = { name: string; mimeType: string; buffer: Buffer }

/** A chart drawn by the browser itself, in any format it encodes. */
async function chart(page: Page, type = 'image/png', width = 3136, height = 1568): Promise<Upload> {
  const data = await page.evaluate(({ type, width, height }) => {
    const canvas = document.createElement('canvas'); canvas.width = width; canvas.height = height
    const ctx = canvas.getContext('2d')!
    ctx.fillStyle = '#fff'; ctx.fillRect(0, 0, width, height)
    ctx.strokeStyle = '#236dca'; ctx.lineWidth = 8; ctx.beginPath(); ctx.moveTo(0, height * .8); ctx.lineTo(width, height * .2); ctx.stroke()
    return canvas.toDataURL(type).split(',')[1]
  }, { type, width, height })
  return { name: `chart.${type.split('/')[1]}`, mimeType: type, buffer: Buffer.from(data, 'base64') }
}
/** A 12 MP phone-camera JPEG: a sky gradient with sensor noise, well over 1.5 MB. */
async function phonePhoto(page: Page, name = 'IMG_0042.jpg'): Promise<Upload> {
  const data = await page.evaluate(() => {
    const canvas = document.createElement('canvas'); canvas.width = 4032; canvas.height = 3024
    const ctx = canvas.getContext('2d')!
    const sky = ctx.createLinearGradient(0, 0, 0, 3024)
    sky.addColorStop(0, '#5d8fd6'); sky.addColorStop(0.62, '#f4c99a'); sky.addColorStop(0.63, '#2f4a3a'); sky.addColorStop(1, '#16241c')
    ctx.fillStyle = sky; ctx.fillRect(0, 0, 4032, 3024)
    const pixels = ctx.getImageData(0, 0, 4032, 3024)
    let seed = 7
    for (let i = 0; i < pixels.data.length; i += 4) {
      seed = (seed * 1103515245 + 12345) >>> 0
      const noise = ((seed >>> 24) - 128) / 4
      pixels.data[i] += noise; pixels.data[i + 1] += noise; pixels.data[i + 2] += noise
    }
    ctx.putImageData(pixels, 0, 0)
    return canvas.toDataURL('image/jpeg', 0.97).split(',')[1]
  })
  return { name, mimeType: 'image/jpeg', buffer: Buffer.from(data, 'base64') }
}
const pdf: Upload = { name: 'Q3 report.pdf', mimeType: 'application/pdf', buffer: Buffer.from(PDF_BYTES) }
const xlsx: Upload = { name: 'model.xlsx', mimeType: XLSX_TYPE, buffer: Buffer.from(XLSX_BYTES) }
const csv: Upload = { name: 'prices.csv', mimeType: 'text/csv', buffer: Buffer.from(CSV_TEXT) }

const picker = (page: Page) => page.getByLabel('Attach files or photos')
const chips = (page: Page) => page.getByRole('list', { name: 'Attached files' }).getByRole('listitem')
async function pick(page: Page, selected?: Upload) {
  const file = selected ?? await chart(page)
  await picker(page).setInputFiles(file)
  await expect(page.getByRole('img', { name: `Preview ${file.name}` })).toBeVisible()
}
/** Dispatches a synthetic paste or drop; true when the page left its default action alone. */
async function transfer(page: Page, type: 'paste' | 'drop', files: Upload[], strings: Record<string, string> = {}) {
  return page.getByLabel('Message the agent').evaluate((box, { type, files, strings }) => {
    const transfer = new DataTransfer()
    for (const [format, value] of Object.entries(strings)) transfer.setData(format, value)
    for (const f of files) transfer.items.add(new File([Uint8Array.from(atob(f.data), c => c.charCodeAt(0))], f.name, { type: f.mimeType }))
    return box.dispatchEvent(type === 'paste' ? new ClipboardEvent('paste', { bubbles: true, cancelable: true, clipboardData: transfer }) : new DragEvent('drop', { bubbles: true, cancelable: true, dataTransfer: transfer }))
  }, { type, strings, files: files.map(f => ({ name: f.name, mimeType: f.mimeType, data: f.buffer.toString('base64') })) })
}

for (const theme of ['light', 'dark'] as const) {
  test(`attachments ${theme}: a phone photo converts to JPEG, documents read as is, metadata-only reload @mobile`, async ({ page }, info) => {
    test.slow() // a 12 MP photo is drawn, encoded and converted in the page
    await page.addInitScript(theme => {
      localStorage.setItem('theme', theme)
      const urls = { created: [] as string[], revoked: [] as string[] }
      Object.assign(window, { previewURLs: urls })
      const create = URL.createObjectURL.bind(URL), revoke = URL.revokeObjectURL.bind(URL)
      URL.createObjectURL = blob => { const url = create(blob); urls.created.push(url); return url }
      URL.revokeObjectURL = url => { urls.revoked.push(url); revoke(url) }
    }, theme)
    const bodies: ChatStreamRequest[] = []
    page.on('request', request => { if (request.url() === `${API_URL}/api/chat/stream`) bodies.push(request.postDataJSON()) })
    await page.goto('/')
    const photo = await phonePhoto(page)
    expect(photo.buffer.byteLength).toBeGreaterThan(1_500_000) // the old 1.5 MB rule rejected this
    await pick(page, photo)
    await picker(page).setInputFiles([pdf, xlsx])
    await expect(chips(page)).toHaveCount(3)
    await expect(chips(page).filter({ hasText: 'Q3 report.pdf' })).toContainText(`PDF · ${Math.ceil(PDF_BYTES.length / 1000)} kB`)
    await expect(chips(page).filter({ hasText: 'model.xlsx' })).toContainText('Excel')
    await expect(chips(page).filter({ hasText: 'IMG_0042.jpg' })).toContainText(/Image · (\d+ kB|1\.[0-4] MB)/)
    await expect(page.getByText(/Up to 5 files: photos/)).toBeVisible()
    await expect(page.getByText('4 cr / msg')).toBeVisible()
    await expectAccessible(page); await expectNoHorizontalScroll(page)
    await page.locator('form').filter({ has: picker(page) }).screenshot({ path: info.outputPath(`composer-${theme}.png`) })
    await page.getByLabel('Message the agent').fill('What do these say about BTC?')
    await page.getByLabel('Message the agent').press('Enter')
    await expect(page).toHaveURL(/\/chat\/[\w-]+$/)
    await expect(page.getByRole('list', { name: 'Suggested follow-ups' })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Stop', exact: true })).toHaveCount(0)
    expect(bodies).toHaveLength(1)
    expect(bodies[0]).toMatchObject({ message: 'What do these say about BTC?', mode: 'quality' })
    const [image, ...documents] = bodies[0].attachments!
    expect(image).toMatchObject({ kind: 'image', mediaType: 'image/jpeg', name: 'IMG_0042.jpg' })
    const jpeg = Buffer.from(image.data, 'base64')
    expect(jpeg.subarray(0, 3)).toEqual(Buffer.from([255, 216, 255]))
    expect(jpeg.byteLength).toBeLessThanOrEqual(1_500_000)
    expect(documents).toEqual([
      { kind: 'document', mediaType: 'application/pdf', name: 'Q3 report.pdf', data: pdf.buffer.toString('base64') },
      { kind: 'document', mediaType: XLSX_TYPE, name: 'model.xlsx', data: xlsx.buffer.toString('base64') },
    ])
    expect(Buffer.byteLength(JSON.stringify(bodies[0]))).toBeLessThanOrEqual(33_554_432)
    const dimensions = await page.evaluate(async data => {
      const bitmap = await createImageBitmap(new Blob([Uint8Array.from(atob(data), c => c.charCodeAt(0))], { type: 'image/jpeg' }))
      const dimensions = [bitmap.width, bitmap.height]; bitmap.close(); return dimensions
    }, image.data)
    expect(dimensions).toEqual([2048, 1536])
    const urls = await page.evaluate(() => (window as unknown as { previewURLs: { created: string[]; revoked: string[] } }).previewURLs)
    expect(urls.created).toEqual([expect.stringMatching(/^blob:/)]) // documents have no preview
    expect(urls.revoked).toEqual(urls.created)
    // the sent turn shows the photo from the bytes still in memory, and the documents by name
    const sent = page.getByRole('list', { name: 'Conversation' }).getByRole('list', { name: 'Attachments' })
    await expect(sent.locator('img[src^="data:image/jpeg"]')).toHaveCount(1)
    await expect(sent).toContainText('Q3 report.pdf')
    await expect(sent).toContainText('model.xlsx')
    await expectNoHorizontalScroll(page)
    await page.evaluate(() => document.getElementById('main')?.scrollTo(0, 0))
    await sent.locator('xpath=ancestor::li[1]').screenshot({ path: info.outputPath(`sent-turn-${theme}.png`) })
    const me = await (await page.request.get(`${API_URL}/api/me`)).json()
    expect(me.credits.used).toBe(22) // 3 with attachments, +1 for the second document
    const saved = await (await page.request.get(`${API_URL}/api/chats/${page.url().split('/').at(-1)}`)).json()
    expect(saved.chat.messages[0].attachments).toEqual([
      { kind: 'image', mediaType: 'image/jpeg', name: 'IMG_0042.jpg', size: jpeg.byteLength },
      { kind: 'document', mediaType: 'application/pdf', name: 'Q3 report.pdf', size: PDF_BYTES.length, pages: 1 },
      { kind: 'document', mediaType: XLSX_TYPE, name: 'model.xlsx', size: XLSX_BYTES.length },
    ])
    for (const a of bodies[0].attachments!) expect(JSON.stringify(saved)).not.toContain(a.data)
    expect(await page.evaluate(() => JSON.stringify({ ...localStorage, ...sessionStorage }))).not.toContain(image.data)
    await page.reload()
    const history = page.getByRole('list', { name: 'Attachments' })
    await expect(history).toContainText('IMG_0042.jpg')
    await expect(history).toContainText('Q3 report.pdf')
    await expect(history).toContainText('1 page')
    await expect(page.locator('img[src^="blob:"], img[src^="data:"]')).toHaveCount(0)
    await expect(page.getByRole('list', { name: 'Suggested follow-ups' })).toBeVisible()
    await expectNoHorizontalScroll(page)
    await page.getByLabel('Message the agent').fill('Another question')
    await page.getByLabel('Message the agent').press('Enter')
    await expect.poll(() => bodies.length).toBe(2)
    expect(bodies[1]).not.toHaveProperty('attachments')
  })

  test(`attachments ${theme}: keyboard picker, paste/drop, five-file limit, specific errors, remove and New chat @mobile`, async ({ page }) => {
    await page.addInitScript(theme => localStorage.setItem('theme', theme), theme)
    await page.goto('/')
    await page.getByLabel('Message the agent').focus()
    await page.keyboard.press('Tab') // Attach is the next stop after the message box
    await expect(picker(page)).toBeFocused()
    await expect(picker(page)).toHaveAttribute('accept', /image\/\*.*\.pdf.*\.docx.*\.xlsx.*\.csv/)
    await pick(page, await chart(page, 'image/jpeg', 512, 256))
    await page.getByRole('button', { name: 'Remove chart.jpeg' }).click()
    await expect(chips(page)).toHaveCount(0)
    // spreadsheet cells come with a picture of the range: they paste as text, nothing is attached
    const cells = { 'text/plain': 'BTC\t64000', 'text/html': '<table><tr><td>BTC</td><td>64000</td></tr></table>' }
    expect(await transfer(page, 'paste', [{ ...await chart(page, 'image/png', 256, 128), name: 'image.png' }], cells)).toBe(true)
    await expect(page.getByRole('list', { name: 'Attached files' })).toHaveCount(0)
    await transfer(page, 'paste', [{ ...await chart(page, 'image/webp', 256, 128), name: 'paste.webp' }])
    await expect(page.getByRole('img', { name: 'Preview paste.webp' })).toBeVisible()
    await transfer(page, 'drop', [{ ...await chart(page, 'image/png', 256, 128), name: 'drop.png' }, csv])
    await expect(chips(page)).toHaveCount(3)
    await expect(chips(page).filter({ hasText: 'prices.csv' })).toContainText('CSV · 1 kB')
    await picker(page).setInputFiles([pdf, xlsx, { ...pdf, name: 'third.pdf' }])
    await expect(page.getByRole('alert')).toContainText('Too many files: attach at most 5 per message. Left out: third.pdf.')
    await expect(chips(page)).toHaveCount(5)
    await expect(page.getByText('5 cr / msg')).toBeVisible() // 3, +1 for each of the 2 documents after the first
    await page.getByRole('button', { name: 'Remove paste.webp' }).click()
    await page.getByRole('button', { name: 'Remove Q3 report.pdf' }).click()
    await page.getByRole('button', { name: 'Remove model.xlsx' }).click()
    await expect(chips(page)).toHaveCount(2)
    await picker(page).setInputFiles([
      { name: 'archive.zip', mimeType: 'application/zip', buffer: Buffer.from('PK\x03\x04') },
      { name: 'IMG_0001.HEIC', mimeType: 'image/heic', buffer: Buffer.from('not a photo') },
      { name: 'huge.pdf', mimeType: 'application/pdf', buffer: Buffer.concat([Buffer.from('%PDF-1.4\n'), Buffer.alloc(4_500_000)]) },
      { name: 'fake.docx', mimeType: 'application/vnd.openxmlformats-officedocument.wordprocessingml.document', buffer: Buffer.from('hello') },
    ])
    const alert = page.getByRole('alert')
    await expect(alert).toContainText('archive.zip: Unsupported file type.')
    await expect(alert).toContainText("IMG_0001.HEIC: Couldn't read this photo.")
    await expect(alert).toContainText('huge.pdf: Too big (4.6 MB). Documents can be at most 4.5 MB.')
    await expect(alert).toContainText("fake.docx: This isn't a valid Word file.")
    await expect(alert).not.toContainText('Too many')
    await expect(chips(page)).toHaveCount(2)
    await expect(page.getByRole('button', { name: 'Send message' })).toBeEnabled()
    await expectAccessible(page); await expectNoHorizontalScroll(page)
    await page.getByRole('button', { name: 'New chat', exact: true }).first().click()
    await expect(chips(page)).toHaveCount(0)
    await expect(page.getByRole('button', { name: 'Send message' })).toBeDisabled()
  })

  test.describe(() => {
    test.use({ allowedErrors: [httpError(502)] })
    test(`attachments ${theme}: transient HTTP/tool retries preserve bytes and Stop clears the next request @mobile`, async ({ page }) => {
      await page.addInitScript(theme => localStorage.setItem('theme', theme), theme)
      for (const kind of ['http', 'partial'] as const) {
        await page.goto('/')
        const bodies: ChatStreamRequest[] = []
        let first = true
        await page.route(`${API_URL}/api/chat/stream`, async route => {
          bodies.push(route.request().postDataJSON())
          if (!first) return route.continue()
          first = false
          if (kind === 'http') return route.fulfill({ status: 502, contentType: 'application/json', body: JSON.stringify({ error: { code: 'upstream', message: 'Try again' } }) })
          const frame = (event: string, data: unknown) => `event: ${event}\ndata: ${JSON.stringify(data)}\n\n`
          await route.fulfill({ contentType: 'text/event-stream', body: frame('chat', { chatId: SEED.chat, title: 'Chart' }) + frame('tool_call', { id: 't', name: 'get_asset_analysis', label: 'Checking live price', input: {} }) + frame('delta', { text: 'Partial chart read' }) + frame('error', { code: 'upstream', message: 'Try again' }) })
        })
        await pick(page)
        await picker(page).setInputFiles(pdf)
        await expect(chips(page)).toHaveCount(2)
        await expect(page.getByRole('button', { name: 'Send message' })).toBeEnabled()
        await page.getByRole('button', { name: 'Send message' }).click()
        await page.getByRole('button', { name: 'Retry', exact: true }).click()
        await expect(page.getByRole('list', { name: 'Suggested follow-ups' })).toBeVisible()
        await expect(page.getByRole('button', { name: 'Stop', exact: true })).toHaveCount(0)
        expect(bodies).toHaveLength(2)
        expect(bodies[0].message).toBe('Read the attached files')
        expect(bodies[0].attachments?.map(a => a.kind)).toEqual(['image', 'document'])
        expect(bodies[1].attachments).toEqual(bodies[0].attachments)
        await page.unroute(`${API_URL}/api/chat/stream`)
      }
      await page.goto('/')
      const bodies: ChatStreamRequest[] = []
      page.on('request', request => { if (request.url() === `${API_URL}/api/chat/stream`) bodies.push(request.postDataJSON()) })
      await pick(page)
      await page.getByRole('button', { name: 'Send message' }).click()
      await page.getByRole('button', { name: 'Stop', exact: true }).click()
      await expect(page.getByText(/Stopped here/)).toBeVisible()
      await expect(page.getByRole('img', { name: /^Preview/ })).toHaveCount(0)
      await page.getByLabel('Message the agent').fill('Continue')
      await page.getByLabel('Message the agent').press('Enter')
      await expect.poll(() => bodies.length).toBe(2)
      expect(bodies[1]).not.toHaveProperty('attachments')
      await expectNoHorizontalScroll(page)
    })
  })
}

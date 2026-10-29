// Forecast materials (API contract round 9): files attached on the New forecast page, read once by the desk, shown in
// "Your materials" on the report with evidence linking to them, and carried into an update.
import { expect, expectNoHorizontalScroll, test, type Page } from './fixtures'
import { API_URL, MATERIALS_FORECAST, RUNNING_MATERIALS_FORECAST } from './mock-api'
import { CSV_TEXT, PDF_BYTES } from '../src/test/documents'

type Upload = { name: string; mimeType: string; buffer: Buffer }
type Body = { attachments?: { kind: string; mediaType: string; name: string; data: string }[]; keepMaterials?: string[] }

const pdf: Upload = { name: 'Q3 broker note.pdf', mimeType: 'application/pdf', buffer: Buffer.from(PDF_BYTES) }
const csv: Upload = { name: 'eth-flows.csv', mimeType: 'text/csv', buffer: Buffer.from(CSV_TEXT) }

/** A chart photo drawn by the browser itself, as a JPEG the page converts again. */
async function photo(page: Page): Promise<Upload> {
  const data = await page.evaluate(() => {
    const canvas = document.createElement('canvas')
    canvas.width = 1200
    canvas.height = 800
    const ctx = canvas.getContext('2d')!
    ctx.fillStyle = '#fff'
    ctx.fillRect(0, 0, 1200, 800)
    ctx.strokeStyle = '#236dca'
    ctx.lineWidth = 8
    ctx.beginPath()
    ctx.moveTo(0, 640)
    ctx.lineTo(1200, 160)
    ctx.stroke()
    return canvas.toDataURL('image/jpeg', 0.9).split(',')[1]
  })
  return { name: 'flows-chart.jpg', mimeType: 'image/jpeg', buffer: Buffer.from(data, 'base64') }
}

const credits = (page: Page) => page.locator('[aria-describedby="credits-tip"]').textContent().then((t) => Number(t?.match(/\d+/)?.[0]))

for (const theme of ['light', 'dark'] as const) {
  test(`materials ${theme}: attach on the New forecast page, read in the report, carried into an update @mobile`, async ({ page }) => {
    await page.addInitScript((t) => localStorage.setItem('theme', t), theme)
    const bodies: { url: string; body: Body }[] = []
    page.on('request', (r) => {
      if (r.method() === 'POST' && r.url().startsWith(`${API_URL}/api/forecasts`)) bodies.push({ url: r.url(), body: r.postDataJSON() })
    })
    await page.goto('/forecasts/new')
    await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
    await page.getByLabel('Your rough idea').fill('Will ETH ETF inflows top $2bn in October 2026?')
    await page.getByRole('button', { name: /Draft with AI/ }).click()
    await expect(page.getByRole('heading', { level: 1, name: 'Review the question.' })).toBeVisible()
    await expect(page.getByLabel('Attach file')).toHaveCount(0) // the context box has no attach button of its own
    const field = page.getByRole('group', { name: 'Materials: drop or paste files' })
    await expect(field).toContainText('Files are read once; we keep the extracted notes, not the file. +1 credit per file.')
    await page.getByLabel('Add material').setInputFiles([pdf, csv, await photo(page)])
    const chips = page.getByRole('list', { name: 'Attached files' }).getByRole('listitem')
    await expect(chips).toHaveCount(3)
    await expect(page.getByRole('img', { name: 'Preview flows-chart.jpg' })).toBeVisible()
    await expect(chips.filter({ hasText: 'Q3 broker note.pdf' })).toContainText(`PDF · ${Math.ceil(PDF_BYTES.length / 1000)} kB`)
    await expect(chips.filter({ hasText: 'eth-flows.csv' })).toContainText('CSV · 1 kB')
    const run = page.getByRole('button', { name: /Run forecast/ })
    await expect(run).toContainText('· 8 cr')
    await expect(run).toBeEnabled()
    await expectNoHorizontalScroll(page)
    const before = await credits(page)
    await run.click()

    await expect(page).toHaveURL(/\/forecasts\/(?!new$)[\w-]+$/) // "/forecasts/new" matches [\w-]+ too
    // The request listener records the body asynchronously: wait for the request itself, as the update step does.
    await expect.poll(() => bodies.some((b) => b.url === `${API_URL}/api/forecasts`)).toBe(true)
    const create = bodies.find((b) => b.url === `${API_URL}/api/forecasts`)!.body
    expect(create.attachments?.map((a) => [a.kind, a.mediaType, a.name])).toEqual([
      ['document', 'application/pdf', 'Q3 broker note.pdf'],
      ['document', 'text/csv', 'eth-flows.csv'],
      ['image', 'image/jpeg', 'flows-chart.jpg'],
    ])
    expect(create.attachments![0].data).toBe(pdf.buffer.toString('base64'))
    await expect.poll(() => credits(page)).toBe(before - 8)
    // running: the card lists the files as being read
    await expect(page.getByText('Your materials · 3')).toBeVisible()
    await expect(page.getByText('Reading…').first()).toBeVisible()
    await expectNoHorizontalScroll(page)

    // the mock completes on its second poll: the report shows what the desk read
    await expect(page.getByText('Probability estimate · Yes')).toBeVisible({ timeout: 10_000 })
    await expect(page.getByText(/^Atomic findings from 3 of your materials/)).toBeVisible()
    const evidence = page.getByRole('link', { name: 'Your material · Q3 broker note.pdf' })
    await expect(evidence).toHaveAttribute('href', '#material-D1')
    await evidence.click()
    const d1 = page.locator('#material-D1 details').first()
    await expect(d1).toHaveAttribute('open', '')
    await expect(page.locator('#material-D1 summary').first()).toBeFocused()
    await expect(page.locator('#material-D1')).toBeInViewport()
    await page.locator('#material-D1').getByText('Quoted passages').click()
    await expect(page.locator('#material-D1').getByText(/ETH ETF inflows reached \$1\.4bn/).last()).toBeVisible()
    await expectNoHorizontalScroll(page)

    // update: drop the CSV, add a new file; the update keeps the other two at no cost
    await page.getByText('Add material to the next update').click()
    await page.getByRole('button', { name: 'Remove eth-flows.csv' }).click()
    await page.getByLabel('Add material').last().setInputFiles({ name: 'october-notes.md', mimeType: 'text/markdown', buffer: Buffer.from('# Notes\nInflows slowed in week 2.') })
    const update = page.getByRole('button', { name: /Update with these materials/ })
    await expect(update).toContainText('· 6 cr')
    await update.click()
    await expect(page.getByText('Your materials · 3')).toBeVisible()
    // The report already showed three files before the update, so wait for the request itself.
    await expect.poll(() => bodies.some((b) => b.url.endsWith('/update'))).toBe(true)
    const sent = bodies.find((b) => b.url.endsWith('/update'))!.body
    expect(sent.keepMaterials).toEqual(['D1', 'D3'])
    expect(sent.attachments?.map((a) => a.name)).toEqual(['october-notes.md'])
    await expect(page.locator('#material-D1')).toContainText('Q3 broker note.pdf')
    await expect(page.locator('#material-D3')).toContainText('october-notes.md')
  })
}

test('a report with materials: the card, evidence links to a row, findings marked as yours, a failed file @mobile', async ({ page }) => {
  await page.goto(`/forecasts/${MATERIALS_FORECAST}`)
  const card = page.getByRole('region', { name: 'What you gave the desk' })
  await expect(page.getByText('Your materials · 3')).toBeVisible()
  await expect(card.locator('#material-D1')).toContainText('PDF · 1.2 MB · 12 pages')
  await expect(card.locator('#material-D2')).toContainText('Read on Oct 5, 2026')
  await expect(card.locator('#material-D3')).toContainText("Couldn't read this file")
  await expect(page.getByText(/couldn't read your file order-book-photo\.jpg/)).toBeVisible()
  const finding = page.locator('li[data-finding]').filter({ hasText: 'The broker note projects first-day demand' })
  await expect(finding.getByText('Your material')).toBeVisible()
  await finding.getByRole('link', { name: 'D1 · Q3 broker note (final).pdf' }).click()
  await expect(page).toHaveURL(/#material-D1$/)
  await expect(card.locator('#material-D1 details').first()).toHaveAttribute('open', '')
  await expect(card.locator('#material-D1').getByText('<b>Key risk</b>', { exact: false })).toBeHidden() // inside the closed excerpt
  await card.locator('#material-D1').getByText('Quoted passages').click()
  await expect(card.locator('#material-D1').getByText(/<b>Key risk<\/b>/)).toBeVisible() // plain text, never markup
  await expectNoHorizontalScroll(page)
})

test('a run still reading its materials shows them in the trace and the card @mobile', async ({ page }) => {
  await page.goto(`/forecasts/${RUNNING_MATERIALS_FORECAST}`)
  const row = page.locator('[data-stage="materials"]')
  await expect(row).toContainText('Your materials')
  await expect(row).toContainText('Reading your materials… (1 of 3)')
  await expect(page.locator('li[data-stage]').first()).toHaveAttribute('data-stage', 'materials')
  await expect(page.getByText('Reading…')).toHaveCount(2)
  await expect(page.locator('#material-D2')).toContainText('Background')
  await expectNoHorizontalScroll(page)
})

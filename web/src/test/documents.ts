// Synthetic document fixtures, built in code: a one-page PDF, stored (uncompressed) DOCX / XLSX packages and CSV text.

const encode = (text: string) => new TextEncoder().encode(text)

function crc32(bytes: Uint8Array): number {
  let crc = ~0
  for (const byte of bytes) {
    crc ^= byte
    for (let k = 0; k < 8; k++) crc = (crc >>> 1) ^ (0xedb88320 & -(crc & 1))
  }
  return ~crc >>> 0
}

/** A valid ZIP of stored entries: local headers, central directory and end record. */
export function zip(entries: Record<string, string>): Uint8Array {
  const locals: Uint8Array[] = [], centrals: Uint8Array[] = []
  let offset = 0
  for (const [path, content] of Object.entries(entries)) {
    const name = encode(path), data = encode(content), crc = crc32(data)
    const local = new Uint8Array(30 + name.length + data.length), l = new DataView(local.buffer)
    l.setUint32(0, 0x04034b50, true); l.setUint16(4, 20, true); l.setUint32(14, crc, true)
    l.setUint32(18, data.length, true); l.setUint32(22, data.length, true); l.setUint16(26, name.length, true)
    local.set(name, 30); local.set(data, 30 + name.length)
    const central = new Uint8Array(46 + name.length), c = new DataView(central.buffer)
    c.setUint32(0, 0x02014b50, true); c.setUint16(4, 20, true); c.setUint16(6, 20, true); c.setUint32(16, crc, true)
    c.setUint32(20, data.length, true); c.setUint32(24, data.length, true); c.setUint16(28, name.length, true); c.setUint32(42, offset, true)
    central.set(name, 46)
    locals.push(local); centrals.push(central); offset += local.length
  }
  const size = centrals.reduce((n, c) => n + c.length, 0)
  const end = new Uint8Array(22), e = new DataView(end.buffer)
  e.setUint32(0, 0x06054b50, true); e.setUint16(8, centrals.length, true); e.setUint16(10, centrals.length, true)
  e.setUint32(12, size, true); e.setUint32(16, offset, true)
  const out = new Uint8Array(offset + size + 22)
  let p = 0
  for (const part of [...locals, ...centrals, end]) { out.set(part, p); p += part.length }
  return out
}

const types = (main: string, type: string) => `<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/${main}" ContentType="${type}"/></Types>`
const rels = (main: string) => `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="${main}"/></Relationships>`

export const DOCX_BYTES = zip({
  '[Content_Types].xml': types('word/document.xml', 'application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml'),
  '_rels/.rels': rels('word/document.xml'),
  'word/document.xml': '<?xml version="1.0" encoding="UTF-8"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Synthetic memo: BTC thesis.</w:t></w:r></w:p></w:body></w:document>',
})
export const XLSX_BYTES = zip({
  '[Content_Types].xml': types('xl/workbook.xml', 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml'),
  '_rels/.rels': rels('xl/workbook.xml'),
  'xl/workbook.xml': '<?xml version="1.0" encoding="UTF-8"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheets><sheet name="Sheet1" sheetId="1"/></sheets></workbook>',
})

function pdf(text: string): Uint8Array {
  const objects = [
    '<< /Type /Catalog /Pages 2 0 R >>',
    '<< /Type /Pages /Kids [3 0 R] /Count 1 >>',
    '<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 100] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>',
    `<< /Length ${`BT /F1 12 Tf 10 50 Td (${text}) Tj ET`.length} >>\nstream\nBT /F1 12 Tf 10 50 Td (${text}) Tj ET\nendstream`,
    '<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>',
  ]
  let body = '%PDF-1.4\n'
  const offsets = objects.map((object, i) => { const at = body.length; body += `${i + 1} 0 obj\n${object}\nendobj\n`; return at })
  const xref = body.length
  body += `xref\n0 ${objects.length + 1}\n0000000000 65535 f \n${offsets.map(o => `${String(o).padStart(10, '0')} 00000 n \n`).join('')}`
  body += `trailer\n<< /Size ${objects.length + 1} /Root 1 0 R >>\nstartxref\n${xref}\n%%EOF\n`
  return encode(body)
}
export const PDF_BYTES = pdf('Synthetic quarterly report')
export const CSV_TEXT = 'date,symbol,close\n2026-10-01,BTC,64000\n2026-10-02,BTC,65120\n'

export const DOCX_TYPE = 'application/vnd.openxmlformats-officedocument.wordprocessingml.document'
export const XLSX_TYPE = 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet'

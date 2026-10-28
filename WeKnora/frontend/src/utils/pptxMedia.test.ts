import assert from 'node:assert/strict'
import test from 'node:test'
import JSZip from 'jszip'
import { DOMParser, XMLSerializer } from '@xmldom/xmldom'
import { createPptxMediaRequestGate, listPptxVectorMediaForSlide, rasterizePptxSlidePreview, sha256Fallback, sha256Hex } from './pptxMedia'

Object.assign(globalThis, { DOMParser, XMLSerializer })

const P = 'http://schemas.openxmlformats.org/presentationml/2006/main'
const A = 'http://schemas.openxmlformats.org/drawingml/2006/main'
const R = 'http://schemas.openxmlformats.org/officeDocument/2006/relationships'
const REL = 'http://schemas.openxmlformats.org/package/2006/relationships'

function picture(name: string, rid: string, x: number) {
  return `<p:pic><p:nvPicPr><p:cNvPr id="1" name="${name}"/><p:cNvPicPr/><p:nvPr/></p:nvPicPr><p:blipFill><a:blip r:embed="${rid}"/></p:blipFill><p:spPr><a:xfrm><a:off x="${x}" y="200"/><a:ext cx="300" cy="400"/></a:xfrm></p:spPr></p:pic>`
}

async function makePptx() {
  const zip = new JSZip()
  zip.file('[Content_Types].xml', '<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/></Types>')
  zip.file('ppt/presentation.xml', `<p:presentation xmlns:p="${P}" xmlns:r="${R}"><p:sldIdLst><p:sldId id="1" r:id="displayFirst"/><p:sldId id="2" r:id="displaySecond"/></p:sldIdLst></p:presentation>`)
  zip.file('ppt/_rels/presentation.xml.rels', `<Relationships xmlns="${REL}"><Relationship Id="displayFirst" Target="slides/unusual-name.xml"/><Relationship Id="displaySecond" Target="slides/slide1.xml"/></Relationships>`)
  zip.file('ppt/slides/unusual-name.xml', `<p:sld xmlns:p="${P}" xmlns:a="${A}" xmlns:r="${R}"><p:cSld><p:spTree>${picture('Crop Chart', 'chart', 1000)}${picture('Second Placement', 'chart', 2000)}${picture('Logo', 'png', 3000)}${picture('External', 'external', 4000)}</p:spTree></p:cSld></p:sld>`)
  zip.file('ppt/slides/_rels/unusual-name.xml.rels', `<Relationships xmlns="${REL}"><Relationship Id="chart" Target="../media/chart.emf"/><Relationship Id="png" Target="../media/logo.png"/><Relationship Id="external" Target="https://example.test/external.wmf" TargetMode="External"/></Relationships>`)
  zip.file('ppt/slides/slide1.xml', `<p:sld xmlns:p="${P}" xmlns:a="${A}" xmlns:r="${R}"><p:cSld><p:spTree>${picture('Later Page', 'vec', 9000)}</p:spTree></p:cSld></p:sld>`)
  zip.file('ppt/slides/_rels/slide1.xml.rels', `<Relationships xmlns="${REL}"><Relationship Id="vec" Target="../media/later.wmf"/></Relationships>`)
  zip.file('ppt/media/chart.emf', 'fake media fixture')
  zip.file('ppt/media/logo.png', 'fake raster fixture')
  zip.file('ppt/media/later.wmf', 'fake media fixture')
  return zip.generateAsync({ type: 'arraybuffer' })
}

test('uses presentation display order and exact slide relationships for selected vector parts', async () => {
  const data = await makePptx()
  const first = await listPptxVectorMediaForSlide(data, 1)
  assert.deepEqual(first, [{
    part: 'ppt/media/chart.emf',
    shapeNames: ['Crop Chart', 'Second Placement'],
    shapeIndexes: [1, 2],
    shapeBoundsEmu: [{ x: 1000, y: 200, cx: 300, cy: 400 }, { x: 2000, y: 200, cx: 300, cy: 400 }],
  }])
  assert.deepEqual((await listPptxVectorMediaForSlide(data, 2)).map(item => item.part), ['ppt/media/later.wmf'])
})

test('rejects invalid page numbers and out-of-package relationships', async () => {
  const data = await makePptx()
  await assert.rejects(listPptxVectorMediaForSlide(data, 0), /Invalid physical slide/)
  await assert.rejects(listPptxVectorMediaForSlide(data, 3), /outside the presentation/)
  const zip = await JSZip.loadAsync(data)
  zip.file('ppt/_rels/presentation.xml.rels', `<Relationships xmlns="${REL}"><Relationship Id="displayFirst" Target="../../private.xml"/><Relationship Id="displaySecond" Target="slides/slide1.xml"/></Relationships>`)
  const invalid = await zip.generateAsync({ type: 'arraybuffer' })
  await assert.rejects(listPptxVectorMediaForSlide(invalid, 1), /relationship is invalid/)
})

test('aborts and discards a late response when the source page changes', async () => {
  const gate = createPptxMediaRequestGate()
  const oldLease = gate.begin('source-a|page-2|token-1')
  let finishOld!: (value: string) => void
  const oldResult = new Promise<string>((resolve) => { finishOld = resolve })
  const committed: string[] = []
  const oldTask = oldResult.then((value) => {
    if (gate.isCurrent(oldLease, 'source-a|page-2|token-1')) committed.push(value)
  })

  const newLease = gate.begin('source-b|page-2|token-2')
  assert.equal(oldLease.controller.signal.aborted, true)
  assert.equal(gate.isCurrent(oldLease, 'source-a|page-2|token-1'), false)
  assert.equal(gate.isCurrent(newLease, 'source-b|page-2|token-2'), true)
  assert.equal(gate.isCurrent(newLease, 'source-a|page-2|token-1'), false)

  finishOld('stale PNG')
  await oldTask
  assert.deepEqual(committed, [])
})

test('explicit cancellation invalidates an in-flight media response', async () => {
  const gate = createPptxMediaRequestGate()
  const lease = gate.begin('source-a|page-4|token-3')
  gate.cancel()
  assert.equal(lease.controller.signal.aborted, true)
  assert.equal(gate.isCurrent(lease, 'source-a|page-4|token-3'), false)
})

test('computes a SHA-256 fingerprint for the exact original byte buffer', async () => {
  const bytes = new TextEncoder().encode('source bytes')
  const buffer = bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength)
  assert.equal(await sha256Hex(buffer), '4d4823794cbed3c4ee0bbc684c8f66e1dfd5afa6f078d494ce254ec5a4671753')
})

test('matches WebCrypto SHA-256 without a secure context across padding boundaries', async () => {
  const { createHash } = await import('node:crypto')
  for (const size of [0, 1, 55, 56, 63, 64, 65, 119, 120, 1000, 1 << 20]) {
    const bytes = new Uint8Array(size + 3).map((_, index) => (index * 31 + 7) & 0xff).subarray(3)
    const expected = createHash('sha256').update(bytes).digest('hex')
    assert.equal(Buffer.from(sha256Fallback(bytes)).toString('hex'), expected, `size ${size}`)
  }
  const subtle = Object.getOwnPropertyDescriptor(globalThis.crypto, 'subtle')
  Object.defineProperty(globalThis.crypto, 'subtle', { value: undefined, configurable: true })
  try {
    const bytes = new TextEncoder().encode('source bytes')
    assert.equal(await sha256Hex(bytes.buffer), '4d4823794cbed3c4ee0bbc684c8f66e1dfd5afa6f078d494ce254ec5a4671753')
  } finally {
    if (subtle) Object.defineProperty(globalThis.crypto, 'subtle', subtle)
    else delete (globalThis.crypto as { subtle?: unknown }).subtle
  }
})

const png = new Uint8Array([137, 80, 78, 71, 13, 10, 26, 10, 0]).buffer

test('rasterizes only the selected physical slide and promotes an OLE snapshot in place', async () => {
  const original = await makePptx()
  const source = await JSZip.loadAsync(original)
  const name = 'ppt/slides/unusual-name.xml'
  const slide = await source.file(name)!.async('string')
  const ole = `<p:graphicFrame><a:graphic><a:graphicData uri="http://schemas.openxmlformats.org/presentationml/2006/ole"><p:oleObj r:id="book">${picture('OLE snapshot', 'chart', 6000)}</p:oleObj></a:graphicData></a:graphic></p:graphicFrame>`
  const unsupported = '<p:graphicFrame><a:graphic><a:graphicData><p:oleObj r:id="uncached"/></a:graphicData></a:graphic></p:graphicFrame>'
  source.file(name, slide.replace('</p:spTree>', `${ole}${unsupported}</p:spTree>`))
  source.file('ppt/embeddings/book.bin', 'Original embedded workbook')
  const data = await source.generateAsync({ type: 'arraybuffer' })
  const hash = await sha256Hex(data)
  const result = await rasterizePptxSlidePreview(data, 1, [{ part: 'ppt/media/chart.emf', png }])
  const changed = await JSZip.loadAsync(result)
  const xml = new DOMParser().parseFromString(await changed.file(name)!.async('string'), 'application/xml')
  const pics = Array.from(xml.getElementsByTagNameNS(P, 'pic'))
  const promoted = pics.find(pic => pic.getElementsByTagNameNS(P, 'cNvPr')[0]?.getAttribute('name') === 'OLE snapshot')!
  assert.equal(promoted.parentNode!.localName, 'spTree')
  assert.equal(promoted.getElementsByTagNameNS(A, 'off')[0].getAttribute('x'), '6000')
  assert.equal(xml.getElementsByTagNameNS(P, 'graphicFrame').length, 1, 'retain OLE objects without a verified cached image')
  assert.equal(await changed.file('ppt/embeddings/book.bin')!.async('string'), 'Original embedded workbook')
  assert.equal(await changed.file('ppt/slides/slide1.xml')!.async('string'), await source.file('ppt/slides/slide1.xml')!.async('string'))
  const rels = await changed.file('ppt/slides/_rels/unusual-name.xml.rels')!.async('string')
  assert.match(rels, /Target="\.\.\/media\/chart.emf.preview.png"/)
  assert.match(rels, /Target="https:\/\/example.test\/external.wmf" TargetMode="External"/)
  assert.match(await changed.file('[Content_Types].xml')!.async('string'), /ContentType="image\/png"/)
  assert.deepEqual(await changed.file('ppt/media/chart.emf.preview.png')!.async('uint8array'), new Uint8Array(png))
  assert.equal(await sha256Hex(data), hash, 'never mutate the downloaded source buffer')
  assert.match(await source.file(name)!.async('string'), /p:graphicFrame/)
})

test('refuses a raster from another page, a duplicate part or a non-PNG response', async () => {
  const data = await makePptx()
  await assert.rejects(rasterizePptxSlidePreview(data, 1, [{ part: 'ppt/media/later.wmf', png }]), /not a unique vector/)
  await assert.rejects(rasterizePptxSlidePreview(data, 1, [{ part: 'ppt/media/chart.emf', png }, { part: 'ppt/media/chart.emf', png }]), /not a unique vector/)
  await assert.rejects(rasterizePptxSlidePreview(data, 1, [{ part: 'ppt/media/chart.emf', png: new ArrayBuffer(1) }]), /Invalid PNG/)
})


import assert from 'node:assert/strict'
import test from 'node:test'
import JSZip from 'jszip'
import { DOMParser } from '@xmldom/xmldom'
import { createPptxMediaRequestGate, listPptxVectorMediaForSlide, sha256Hex } from './pptxMedia'

Object.assign(globalThis, { DOMParser })

const P = 'http://schemas.openxmlformats.org/presentationml/2006/main'
const A = 'http://schemas.openxmlformats.org/drawingml/2006/main'
const R = 'http://schemas.openxmlformats.org/officeDocument/2006/relationships'
const REL = 'http://schemas.openxmlformats.org/package/2006/relationships'

function picture(name: string, rid: string, x: number) {
  return `<p:pic><p:nvPicPr><p:cNvPr id="1" name="${name}"/><p:cNvPicPr/><p:nvPr/></p:nvPicPr><p:blipFill><a:blip r:embed="${rid}"/></p:blipFill><p:spPr><a:xfrm><a:off x="${x}" y="200"/><a:ext cx="300" cy="400"/></a:xfrm></p:spPr></p:pic>`
}

async function makePptx() {
  const zip = new JSZip()
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


import assert from 'node:assert/strict'
import test from 'node:test'
import JSZip from 'jszip'
import { DOMParser, XMLSerializer } from '@xmldom/xmldom'
import { preparePptxOlePreview, sha256Hex } from './pptxMedia'
import { createPptxViewportMedia } from './pptxViewportMedia'

Object.assign(globalThis, { DOMParser, XMLSerializer })
const P = 'http://schemas.openxmlformats.org/presentationml/2006/main'
const A = 'http://schemas.openxmlformats.org/drawingml/2006/main'
const R = 'http://schemas.openxmlformats.org/officeDocument/2006/relationships'
const REL = 'http://schemas.openxmlformats.org/package/2006/relationships'
const MC = 'http://schemas.openxmlformats.org/markup-compatibility/2006'
const png = new Blob([new Uint8Array([137, 80, 78, 71, 13, 10, 26, 10, 0])], { type: 'image/png' })
const flush = () => new Promise(resolve => setImmediate(resolve))
function picture(rid: string) {
  return `<p:pic><p:nvPicPr><p:cNvPr id="3" name="Synthetic chart"/></p:nvPicPr><p:blipFill><a:blip r:embed="${rid}"/></p:blipFill><p:spPr><a:xfrm><a:off x="10" y="20"/><a:ext cx="300" cy="400"/></a:xfrm><a:prstGeom prst="rect"/></p:spPr></p:pic>`
}
function ole(rid: string) {
  return `<p:graphicFrame><p:xfrm><a:off x="10" y="20"/><a:ext cx="300" cy="400"/></p:xfrm><a:graphic><a:graphicData><mc:AlternateContent><mc:Choice Requires="v"><p:oleObj r:id="externalBook"><p:link/></p:oleObj></mc:Choice><mc:Fallback><p:oleObj r:id="externalBook"><p:link/>${picture(rid)}</p:oleObj></mc:Fallback></mc:AlternateContent></a:graphicData></a:graphic></p:graphicFrame>`
}
async function fixture(thirdPageVector = false) {
  const zip = new JSZip()
  zip.file('[Content_Types].xml', '<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>')
  zip.file('ppt/presentation.xml', `<p:presentation xmlns:p="${P}" xmlns:r="${R}"><p:sldIdLst>${[1,2,3,4].map(i => `<p:sldId id="${i}" r:id="s${i}"/>`).join('')}</p:sldIdLst></p:presentation>`)
  zip.file('ppt/_rels/presentation.xml.rels', `<Relationships xmlns="${REL}">${[1,2,3,4].map(i => `<Relationship Id="s${i}" Target="slides/slide${i}.xml"/>`).join('')}</Relationships>`)
  for (const page of [1,2,3,4]) {
    zip.file(`ppt/slides/slide${page}.xml`, `<p:sld xmlns:p="${P}" xmlns:a="${A}" xmlns:r="${R}" xmlns:mc="${MC}"><p:cSld><p:spTree>${page === 1 ? picture('logo') : page === 2 ? ['a','b','c'].map(ole).join('') : page === 4 ? ole('a') : page === 3 && thirdPageVector ? ole('d') : ''}</p:spTree></p:cSld></p:sld>`)
    zip.file(`ppt/slides/_rels/slide${page}.xml.rels`, `<Relationships xmlns="${REL}"><Relationship Id="logo" Target="../media/image-logo.png"/>${['a','b','c','d'].map(id => `<Relationship Id="${id}" Target="../media/image-${id}.emf"/>`).join('')}<Relationship Id="externalBook" Target="https://example.invalid/book.xls" TargetMode="External"/></Relationships>`)
  }
  zip.file('ppt/media/image-logo.png', 'Original logo')
  for (const id of ['a','b','c','d']) zip.file(`ppt/media/image-${id}.emf`, `Original vector ${id}`)
  return zip.generateAsync({ type: 'arraybuffer' })
}

class Image extends EventTarget {
  dataset: Record<string, string> = {}
  constructor(private value: string) { super() }
  getAttribute(name: string) { return name === 'src' ? this.value : null }
  set src(value: string) { this.value = value; queueMicrotask(() => this.dispatchEvent(new Event('load'))) }
}
function dom() {
  const image = (id: string) => new Image('data:image/x-emf;base64,' + Buffer.from(`Original vector ${id}`).toString('base64'))
  const images = [[], [image('a'), image('b'), image('c'), image('unrelated')], [], [image('a')]]
  const slides = images.map(imgs => ({ dataset: {} as Record<string,string>, querySelectorAll: () => imgs, getBoundingClientRect: () => ({ top: 0, bottom: 100 }) }))
  const root = { querySelectorAll: () => slides }
  return { images, slides, root: root as unknown as HTMLElement }
}

test('promotes verified OLE snapshots on all pages while preserving original bytes and PNG-only pages', async () => {
  const data = await fixture()
  const originalHash = await sha256Hex(data)
  const before = await JSZip.loadAsync(data)
  const prepared = await JSZip.loadAsync(await preparePptxOlePreview(data))
  for (const page of [2,4]) {
    const slide = new DOMParser().parseFromString(await prepared.file(`ppt/slides/slide${page}.xml`)!.async('string'), 'application/xml')
    assert.equal(slide.getElementsByTagNameNS(P, 'graphicFrame').length, 0)
    const pictures = [...slide.getElementsByTagNameNS(P, 'pic')]
    assert.equal(pictures.length, page === 2 ? 3 : 1)
    assert.ok(pictures.every(pic => pic.parentNode!.localName === 'spTree'))
    assert.equal(pictures[0].getElementsByTagNameNS(A, 'off')[0].getAttribute('x'), '10')
  }
  for (const name of ['ppt/slides/slide1.xml','ppt/slides/slide3.xml','ppt/slides/_rels/slide2.xml.rels','ppt/media/image-a.emf']) {
    assert.equal(await prepared.file(name)!.async('string'), await before.file(name)!.async('string'))
  }
  assert.equal(await sha256Hex(data), originalHash)
})

test('a PNG-only cited page does not prevent later visible chart pages from loading; shared parts are cached', async t => {
  const data = await fixture(), doc = dom(), requested: string[] = []
  const snapshots = new Map<number, any[]>()
  const oldObserver = globalThis.IntersectionObserver
  let intersect: ((entries: any[]) => void) | undefined
  globalThis.IntersectionObserver = class {
    constructor(callback: any) { intersect = callback }
    observe() {} disconnect() {}
  } as unknown as typeof IntersectionObserver
  t.after(() => { globalThis.IntersectionObserver = oldObserver })
  const loader = createPptxViewportMedia({ data, sourceSha256: 'a'.repeat(64), generation: 1,
    loadRaster: async (page, part) => { requested.push(`${page}:${part}`); return png },
    onPage: (page, items) => snapshots.set(page, items), onPageError: () => assert.fail('unexpected metadata error'),
  })
  t.after(() => loader.stop())
  loader.observe(doc.root)
  await loader.loadPage(1)
  assert.equal(requested.length, 0)
  assert.equal(doc.slides[1].dataset.pptxSourcePage, '2')
  intersect!([{ target: doc.slides[1], isIntersecting: true }])
  await loader.loadPage(2)
  await flush()
  assert.equal(requested.length, 3)
  assert.ok(doc.images[1].slice(0,3).every(image => image.getAttribute('src')!.startsWith('blob:')))
  assert.ok(doc.images[1].slice(0,3).every(image => image.dataset.pptxMediaState === 'displayed'))
  assert.ok(doc.images[1][3].getAttribute('src')!.startsWith('data:'), 'unrelated image bytes cannot be painted')
  assert.ok(snapshots.get(2)!.every(item => item.state === 'displayed'))
  await loader.loadPage(3)
  await loader.loadPage(4)
  await flush()
  assert.equal(requested.length, 3, 'scrolling to a repeated original part reuses its authorized raster')
  assert.ok(doc.images[3][0].getAttribute('src')!.startsWith('blob:'))
})

test('visible-page requests are bounded to two, and a source change cancels queued and late work', async t => {
  const data = await fixture(), releases: Array<() => void> = []
  let active = 0, maximum = 0, calls = 0, snapshots = 0
  let started!: () => void
  const bothStarted = new Promise<void>(resolve => { started = resolve })
  const loader = createPptxViewportMedia({ data, sourceSha256: 'b'.repeat(64), generation: 2,
    loadRaster: async () => {
      calls++; active++; maximum = Math.max(maximum, active)
      if (calls === 2) started()
      await new Promise<void>(resolve => releases.push(resolve)); active--; return png
    }, onPage: () => snapshots++, onPageError: () => assert.fail('unexpected metadata error'),
  })
  t.after(() => { loader.stop(); releases.splice(0).forEach(release => release()) })
  const task = loader.loadPage(2)
  await new Promise<void>((resolve, reject) => {
    const timeout = setTimeout(() => reject(new Error('two requests did not start')), 3000)
    void bothStarted.then(() => { clearTimeout(timeout); resolve() })
  })
  assert.equal(calls, 2)
  assert.equal(maximum, 2)
  loader.stop()
  const atStop = snapshots
  releases.splice(0).forEach(release => release())
  await task
  assert.equal(calls, 2, 'the queued third request never starts after cancellation')
  assert.equal(snapshots, atStop, 'late responses cannot publish into the next source')
})

test('scrolling moves newly visible figures ahead of queued work from an old page', { timeout: 5000 }, async t => {
  const doc = dom(), calls: string[] = [], releases: Array<() => void> = []
  let visiblePage = 2, finishRemaining = false
  doc.slides.forEach((slide, index) => {
    slide.getBoundingClientRect = () => ({ top: (index + 1 - visiblePage) * 200, bottom: (index + 1 - visiblePage) * 200 + 100 })
  })
  Object.assign(doc.root, { getBoundingClientRect: () => ({ top: 0, bottom: 100 }), addEventListener: () => {} })
  let firstTwo!: () => void, thirdStarted!: () => void, pageThreeKnown!: () => void
  const twoStarted = new Promise<void>(resolve => { firstTwo = resolve })
  const nextStarted = new Promise<void>(resolve => { thirdStarted = resolve })
  const pageThreeReady = new Promise<void>(resolve => { pageThreeKnown = resolve })
  const loader = createPptxViewportMedia({ data: await fixture(true), sourceSha256: 'd'.repeat(64), generation: 4,
    loadRaster: async (_page, part) => {
      calls.push(part)
      if (calls.length === 2) firstTwo()
      if (calls.length === 3) thirdStarted()
      if (!finishRemaining) await new Promise<void>(resolve => releases.push(resolve))
      return png
    }, onPage: page => { if (page === 3) pageThreeKnown() }, onPageError: () => assert.fail('unexpected metadata error'),
  })
  t.after(() => { loader.stop(); releases.splice(0).forEach(release => release()) })
  loader.observe(doc.root)
  const oldPage = loader.loadPage(2)
  await twoStarted
  assert.deepEqual(new Set(calls), new Set(['ppt/media/image-a.emf', 'ppt/media/image-b.emf']), 'manifest order reserves the first two tasks before inflation')
  const newPage = loader.loadPage(3)
  await pageThreeReady
  await flush()
  visiblePage = 3
  releases.shift()!()
  await nextStarted
  assert.equal(calls[2], 'ppt/media/image-d.emf', 'current viewport takes priority over the old page third figure')
  finishRemaining = true
  releases.splice(0).forEach(release => release())
  await Promise.all([oldPage, newPage])
  assert.equal(calls[3], 'ppt/media/image-c.emf', 'old page work remains available after visible work')
})

test('a failed raster is explicit and retry does not refetch successful parts', async () => {
  const calls = new Map<string, number>(), snapshots = new Map<number, any[]>()
  const loader = createPptxViewportMedia({ data: await fixture(), sourceSha256: 'c'.repeat(64), generation: 3,
    loadRaster: async (_page, part) => {
      calls.set(part, (calls.get(part) || 0) + 1)
      return part.endsWith('-b.emf') && calls.get(part) === 1 ? new Blob(['not an image']) : png
    }, onPage: (page, items) => snapshots.set(page, items), onPageError: () => assert.fail('unexpected metadata error'),
  })
  await loader.loadPage(2)
  assert.equal(snapshots.get(2)!.find(item => item.part.endsWith('-b.emf')).state, 'failed')
  await loader.loadPage(2, true)
  assert.equal(calls.get('ppt/media/image-b.emf'), 2)
  assert.equal(calls.get('ppt/media/image-a.emf'), 1)
  assert.equal(calls.get('ppt/media/image-c.emf'), 1)
  loader.stop()
})

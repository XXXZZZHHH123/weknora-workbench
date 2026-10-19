import assert from 'node:assert/strict'
import test from 'node:test'
import JSZip from 'jszip'
import { DOMParser, XMLSerializer } from '@xmldom/xmldom'
import { preparePptxPreview, isCompletePptxRender, hasCompletePptxSlideDOM, watchCompletePptxSlideDOM, bindPptxRendererCallbacks, isPptxSourceKeyCurrent, resolvePptxSlideLocator, resolvePptxLocatePage } from './pptxPreview'
import { findInText } from './sourceLocator'

Object.assign(globalThis, { DOMParser, XMLSerializer })
const typesNs = 'http://schemas.openxmlformats.org/package/2006/content-types'
const relsNs = 'http://schemas.openxmlformats.org/package/2006/relationships'
const masterType = 'application/vnd.openxmlformats-officedocument.presentationml.slideMaster+xml'
const missingPart = '/ppt/slideMasters/slideMaster2.xml'

async function fixture({ dangling = true, reference = '', external = false, prefix = '' } = {}) {
  const zip = new JSZip()
  zip.file('ppt/presentation.xml', '<p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"><p:sldIdLst><p:sldId id="256"/><p:sldId id="257"/></p:sldIdLst></p:presentation>')
  zip.file('ppt/slides/slide1.xml', '<slide>First slide</slide>')
  zip.file('ppt/slides/slide2.xml', '<slide>Second slide</slide>')
  zip.file('ppt/slideMasters/slideMaster1.xml', '<master/>')
  const tag = prefix ? `${prefix}:` : ''
  zip.file('[Content_Types].xml', `<${tag}Types xmlns${prefix ? ':' + prefix : ''}="${typesNs}"><${tag}Override PartName="/ppt/slideMasters/slideMaster1.xml" ContentType="${masterType}"/>${dangling ? `<${tag}Override PartName="${missingPart}" ContentType="${masterType}"/>` : ''}</${tag}Types>`)
  zip.file('ppt/slideLayouts/_rels/slideLayout1.xml.rels', `<Relationships xmlns="${relsNs}"><Relationship Id="rId1" Target="${reference || '../slideMasters/slideMaster1.xml'}"${external ? ' TargetMode="External"' : ''}/></Relationships>`)
  return zip.generateAsync({ type: 'arraybuffer' })
}

test('removes unused missing masters from a preview copy and preserves every other part', async () => {
  const input = await fixture()
  const originalBytes = new Uint8Array(input).slice()
  const result = await preparePptxPreview(input)
  assert.equal(result.slideCount, 2)
  assert.deepEqual(new Uint8Array(input), originalBytes)
  const before = await JSZip.loadAsync(input), after = await JSZip.loadAsync(result.data)
  const xml = await after.file('[Content_Types].xml')!.async('string')
  assert.ok(!xml.includes(missingPart))
  assert.ok(xml.includes('/ppt/slideMasters/slideMaster1.xml'))
  assert.deepEqual(Object.keys(before.files), Object.keys(after.files))
  for (const part of Object.values(before.files)) {
    if (!part.dir && part.name !== '[Content_Types].xml') {
      assert.deepEqual(await part.async('uint8array'), await after.file(part.name)!.async('uint8array'))
    }
  }
})

test('does not repack a valid presentation', async () => {
  const input = await fixture({ dangling: false })
  assert.equal((await preparePptxPreview(input)).data, input)
})

test('never removes missing masters referenced by relative, absolute or encoded OPC paths', async () => {
  for (const reference of ['../slideMasters/slideMaster2.xml', '/ppt/slideMasters/slideMaster2.xml', '../slideMasters/slideMaster%32.xml']) {
    await assert.rejects(preparePptxPreview(await fixture({ reference })), /references a missing slide master/)
  }
})

test('supports namespace prefixes and ignores external relationships', async () => {
  const input = await fixture({ prefix: 'ct', reference: '../slideMasters/slideMaster2.xml', external: true })
  const result = await preparePptxPreview(input)
  const zip = await JSZip.loadAsync(result.data)
  assert.ok(!(await zip.file('[Content_Types].xml')!.async('string')).includes(missingPart))
})

test('zero and partial render success events must be treated as failures', () => {
  for (const result of [null, undefined, {}, { slides: [] }, { slides: [{}] }]) {
    assert.equal(isCompletePptxRender(result, 2), false)
  }
  assert.equal(isCompletePptxRender({ slides: [{}, {}] }, 2), true)
  assert.equal(isCompletePptxRender({ slides: [] }, 0), false)
})

test('an exact partial slide locator can locate a page without claiming unmatched text', () => {
  const locator = { type: 'slide', mapping: 'exact', partial: true, slide: 2, quote: 'A verified source sentence' }
  assert.deepEqual(resolvePptxSlideLocator(locator, 4, false), {
    page: 2, partial: true, precise: false, granularity: 'block',
  })

  const pageText = 'A verified source sentence appears once on this slide.'
  const uniqueQuote = findInText(pageText, locator.quote) !== null
  assert.deepEqual(resolvePptxSlideLocator(locator, 4, uniqueQuote), {
    page: 2, partial: true, precise: true, granularity: 'text',
  })

  const duplicateQuote = findInText(`${pageText} A verified source sentence appears again.`, locator.quote) !== null
  assert.deepEqual(resolvePptxSlideLocator(locator, 4, duplicateQuote), {
    page: 2, partial: true, precise: false, granularity: 'block',
  })
})

test('approximate, missing and out-of-range slide locators cannot select a physical page', () => {
  const candidates = [
    { type: 'slide', mapping: 'approximate', slide: 2, quote: 'A verified source sentence' },
    { type: 'slide', mapping: 'exact', quote: 'A verified source sentence' },
    { type: 'slide', mapping: 'exact', slide: 0, quote: 'A verified source sentence' },
    { type: 'slide', mapping: 'exact', slide: -1, quote: 'A verified source sentence' },
    { type: 'slide', mapping: 'exact', slide: 5, quote: 'A verified source sentence' },
    { type: 'text', mapping: 'exact', slide: 2, quote: 'A verified source sentence' },
  ]
  for (const locator of candidates) {
    assert.equal(resolvePptxSlideLocator(locator, 4, true), null, JSON.stringify(locator))
  }
  assert.equal(resolvePptxLocatePage(candidates, 4, true), 0)
  assert.equal(resolvePptxLocatePage([{ type: 'slide', mapping: 'exact', partial: true, slide: 2 }], 4, false), 0,
    'an unavailable source keeps its existing stale-evidence protection')
  assert.equal(resolvePptxLocatePage([{ type: 'slide', mapping: 'exact', partial: true, slide: 2 }], 4, true), 2)
})

test('renderer callbacks retain their source key and stale instances cannot affect a new source', () => {
  let currentSource = 'source-a'
  const delivered: string[] = []
  const callbacks = bindPptxRendererCallbacks('source-a', {
    onRendered: key => { if (isPptxSourceKeyCurrent(key, currentSource, true)) delivered.push(`rendered:${key}`) },
    onError: key => { if (isPptxSourceKeyCurrent(key, currentSource, true)) delivered.push(`error:${key}`) },
  })

  currentSource = 'source-b'
  callbacks.onRendered({ slides: [] })
  callbacks.onError(new Error('stale renderer'))
  assert.equal(delivered.length, 0)

  const currentCallbacks = bindPptxRendererCallbacks('source-b', {
    onRendered: key => { if (isPptxSourceKeyCurrent(key, currentSource, true)) delivered.push(`rendered:${key}`) },
    onError: key => { if (isPptxSourceKeyCurrent(key, currentSource, true)) delivered.push(`error:${key}`) },
  })
  currentCallbacks.onRendered({ slides: [] })
  assert.deepEqual(delivered, ['rendered:source-b'])
})

test('requires every physical slide wrapper to be connected and laid out before locating', () => {
  const slide = (connected: boolean, width: number, height: number) => ({
    isConnected: connected,
    getBoundingClientRect: () => ({ width, height }),
  }) as HTMLElement
  const root = (slides: HTMLElement[]) => ({
    querySelectorAll: (selector: string) => selector === '.pptx-preview-slide-wrapper' ? slides : [],
  }) as unknown as ParentNode

  assert.equal(hasCompletePptxSlideDOM(root([slide(true, 960, 540), slide(true, 960, 540)]), 2), true)
  assert.equal(hasCompletePptxSlideDOM(root([slide(true, 960, 540)]), 2), false)
  assert.equal(hasCompletePptxSlideDOM(root([slide(true, 960, 540), slide(false, 960, 540)]), 2), false)
  assert.equal(hasCompletePptxSlideDOM(root([slide(true, 960, 540), slide(true, 0, 0)]), 2), false)
  assert.equal(hasCompletePptxSlideDOM(root([slide(true, 960, 540)]), 0), false)
})

function slideRoot(slides: HTMLElement[]) {
  return {
    querySelectorAll: (selector: string) => selector === '.pptx-preview-slide-wrapper' ? slides : [],
  } as unknown as ParentNode
}

function makeSlide(connected = true, width = 960, height = 540) {
  return {
    isConnected: connected,
    getBoundingClientRect: () => ({ width, height }),
  } as HTMLElement
}

const delay = (ms: number) => new Promise(resolve => setTimeout(resolve, ms))

test('waits for partial slide DOM to become fully laid out before locating', async () => {
  const slides = [makeSlide()]
  const events: string[] = []
  watchCompletePptxSlideDOM(slideRoot(slides), 2, {
    onReady: () => events.push('ready'),
    onTimeout: () => events.push('timeout'),
  }, { timeoutMs: 100, pollIntervalMs: 5 })
  await delay(15)
  slides.push(makeSlide())
  await delay(15)
  assert.deepEqual(events, ['ready'])
})

test('reports timeout for partial or unlaid-out slides', async () => {
  const events: string[] = []
  watchCompletePptxSlideDOM(slideRoot([makeSlide(), makeSlide(true, 0, 0)]), 2, {
    onReady: () => events.push('ready'),
    onTimeout: () => events.push('timeout'),
  }, { timeoutMs: 20, pollIntervalMs: 5 })
  await delay(35)
  assert.deepEqual(events, ['timeout'])
})

test('cancels slide DOM watching without completing a stale locate', async () => {
  const slides = [makeSlide()]
  const events: string[] = []
  let sourceKey = 'source-a'
  const locate = (boundSourceKey: string) => {
    if (isPptxSourceKeyCurrent(boundSourceKey, sourceKey, true)) events.push(`located:${boundSourceKey}`)
  }
  const cancel = watchCompletePptxSlideDOM(slideRoot(slides), 2, {
    onReady: () => locate('source-a'),
    onTimeout: () => events.push('timeout'),
  }, { timeoutMs: 25, pollIntervalMs: 5 })
  sourceKey = 'source-b'
  cancel()
  slides.push(makeSlide())
  await delay(35)
  assert.deepEqual(events, [])
})

import JSZip from 'jszip'

const CONTENT_TYPES_NS = 'http://schemas.openxmlformats.org/package/2006/content-types'
const RELATIONSHIPS_NS = 'http://schemas.openxmlformats.org/package/2006/relationships'
const PRESENTATION_NS = 'http://schemas.openxmlformats.org/presentationml/2006/main'
const SLIDE_MASTER_TYPE = 'application/vnd.openxmlformats-officedocument.presentationml.slideMaster+xml'

function parseXml(source: string): Document {
  const document = new DOMParser().parseFromString(source, 'application/xml')
  if (document.getElementsByTagName('parsererror').length) throw new Error('Invalid PPTX metadata')
  return document
}

/** Resolve an OPC relationship relative to its owning part, not its _rels directory. */
function relationshipTarget(relsPath: string, target: string): string {
  const base = relsPath === '_rels/.rels' ? '' : relsPath.slice(0, relsPath.lastIndexOf('/_rels/') + 1)
  return decodeURIComponent(new URL(target, `https://pptx.invalid/${base}`).pathname.slice(1))
}

/**
 * PptxGenJS 4.0.1 declares a master per slide even when only one master is stored.
 * Office ignores these unused entries; vue-office aborts parsing and returns zero
 * slides. Repair only missing, unreferenced master declarations in a preview copy.
 * Never remove a referenced part or alter the downloaded original.
 */
export async function preparePptxPreview(data: ArrayBuffer): Promise<{ data: ArrayBuffer; slideCount: number }> {
  const zip = await JSZip.loadAsync(data)
  const typesPart = zip.file('[Content_Types].xml')
  const presentationPart = zip.file('ppt/presentation.xml')
  if (!typesPart || !presentationPart) throw new Error('Invalid PPTX package')
  const types = parseXml(await typesPart.async('string'))
  const presentation = parseXml(await presentationPart.async('string'))
  const slideCount = presentation.getElementsByTagNameNS(PRESENTATION_NS, 'sldId').length
  if (!slideCount) throw new Error('PPTX contains no slides')

  const missingMasters = Array.from(types.getElementsByTagNameNS(CONTENT_TYPES_NS, 'Override')).filter(entry => {
    const partName = entry.getAttribute('PartName') || ''
    return entry.getAttribute('ContentType') === SLIDE_MASTER_TYPE
      && partName.startsWith('/ppt/slideMasters/') && !zip.file(decodeURIComponent(partName.slice(1)))
  })
  if (!missingMasters.length) return { data, slideCount }

  const references = new Set<string>()
  for (const part of Object.values(zip.files)) {
    if (part.dir || !part.name.endsWith('.rels')) continue
    const relations = parseXml(await part.async('string'))
    for (const rel of Array.from(relations.getElementsByTagNameNS(RELATIONSHIPS_NS, 'Relationship'))) {
      const target = rel.getAttribute('Target')
      if (target && rel.getAttribute('TargetMode') !== 'External') references.add(relationshipTarget(part.name, target))
    }
  }
  for (const entry of missingMasters) {
    const partName = decodeURIComponent(entry.getAttribute('PartName')!.slice(1))
    if (references.has(partName)) throw new Error('PPTX references a missing slide master')
    entry.parentNode!.removeChild(entry)
  }
  zip.file('[Content_Types].xml', new XMLSerializer().serializeToString(types))
  return { data: await zip.generateAsync({ type: 'arraybuffer' }), slideCount }
}

/** The renderer can swallow parsing errors and report successful empty/partial output. */
export function isCompletePptxRender(result: unknown, expectedSlides: number): boolean {
  const slides = (result as { slides?: unknown[] } | null)?.slides
  return expectedSlides > 0 && Array.isArray(slides) && slides.length === expectedSlides
}

export type PptxSlideLocator = {
  type?: string
  mapping?: string
  partial?: boolean
  slide?: number
  quote?: string
}

export type PptxSlideLocation = {
  page: number
  partial: boolean
  precise: boolean
  granularity: 'text' | 'block'
}

/**
 * Resolve only a verified physical slide mapping. Partial means the source
 * chunk covers only part of that slide; it still identifies the page, but
 * must not be treated as a precise text match unless the page quote matched.
 */
export function resolvePptxSlideLocator(
  locator: PptxSlideLocator,
  slideCount: number,
  uniqueQuoteMatched: boolean,
): PptxSlideLocation | null {
  const page = locator.slide
  if (locator.type !== 'slide' || locator.mapping !== 'exact' ||
      !Number.isSafeInteger(page) || (page || 0) < 1 || page! > slideCount || slideCount < 1) return null
  const precise = !!locator.quote && uniqueQuoteMatched
  return {
    page: page!,
    partial: !!locator.partial,
    precise,
    granularity: precise ? 'text' : 'block',
  }
}

/** Stale or otherwise unavailable source evidence must never drive a preview. */
export function resolvePptxLocatePage(
  locators: PptxSlideLocator[],
  slideCount: number,
  sourceAvailable: boolean,
): number {
  if (!sourceAvailable) return 0
  for (const locator of locators) {
    const resolved = resolvePptxSlideLocator(locator, slideCount, false)
    if (resolved) return resolved.page
  }
  return 0
}

export type PptxRendererCallbacks = {
  onRendered: (result: unknown) => void
  onError: (reason: unknown) => void
}

export function isPptxSourceKeyCurrent(sourceKey: string, currentSourceKey: string, active: boolean): boolean {
  return active && !!sourceKey && sourceKey === currentSourceKey
}

/** Bind renderer events to the source key that created that renderer instance. */
export function bindPptxRendererCallbacks(
  sourceKey: string,
  handlers: {
    onRendered: (sourceKey: string, result: unknown) => void
    onError: (sourceKey: string, reason: unknown) => void
  },
): PptxRendererCallbacks {
  return {
    onRendered: result => handlers.onRendered(sourceKey, result),
    onError: reason => handlers.onError(sourceKey, reason),
  }
}

/**
 * A rendered PPTX DOM is ready for locating only when every physical slide has layout.
 * This validates citation geometry, not image decoding or pixel completeness.
 */
export function hasCompletePptxSlideDOM(root: ParentNode | null | undefined, expectedSlides: number): boolean {
  if (!root || expectedSlides <= 0) return false
  const slides = Array.from(root.querySelectorAll<HTMLElement>('.pptx-preview-slide-wrapper'))
  if (slides.length !== expectedSlides) return false
  return slides.every((slide) => {
    if (!slide.isConnected) return false
    const rect = slide.getBoundingClientRect()
    return rect.width > 0 && rect.height > 0
  })
}

/** Observe actual slide structure/layout when vue-office's completion event is delayed or absent. */
export function watchCompletePptxSlideDOM(
  root: ParentNode,
  expectedSlides: number,
  callbacks: { onReady: () => void; onTimeout: () => void },
  options: { timeoutMs?: number; pollIntervalMs?: number } = {},
): () => void {
  let settled = false
  let poll: ReturnType<typeof setTimeout> | null = null
  let timeout: ReturnType<typeof setTimeout> | null = null
  let observer: MutationObserver | null = null
  const clear = () => {
    observer?.disconnect()
    observer = null
    if (poll !== null) clearTimeout(poll)
    if (timeout !== null) clearTimeout(timeout)
    poll = null
    timeout = null
  }
  const settle = (ready: boolean) => {
    if (settled) return
    settled = true
    clear()
    if (ready) callbacks.onReady()
    else callbacks.onTimeout()
  }
  const check = () => {
    if (settled) return
    if (hasCompletePptxSlideDOM(root, expectedSlides)) return settle(true)
    if (poll !== null) return
    poll = setTimeout(() => {
      poll = null
      check()
    }, options.pollIntervalMs ?? 100)
  }

  if (typeof MutationObserver !== 'undefined') {
    observer = new MutationObserver(check)
    observer.observe(root as Node, { childList: true, subtree: true })
  }
  timeout = setTimeout(() => settle(hasCompletePptxSlideDOM(root, expectedSlides)), options.timeoutMs ?? 45_000)
  queueMicrotask(check)
  return () => {
    if (settled) return
    settled = true
    clear()
  }
}

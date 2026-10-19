import JSZip from 'jszip'

const PRESENTATION_NS = 'http://schemas.openxmlformats.org/presentationml/2006/main'
const DRAWING_NS = 'http://schemas.openxmlformats.org/drawingml/2006/main'
const OFFICE_REL_NS = 'http://schemas.openxmlformats.org/officeDocument/2006/relationships'
const PACKAGE_REL_NS = 'http://schemas.openxmlformats.org/package/2006/relationships'
const VECTOR_MEDIA = /\.(?:emf|wmf)$/i

export type PptxShapeBoundsEmu = { x: number; y: number; cx: number; cy: number }

export type PptxVectorMedia = {
  part: string
  shapeNames: string[]
  shapeIndexes: number[]
  /** Raw DrawingML bounds. Nested group transforms are not applied. */
  shapeBoundsEmu: PptxShapeBoundsEmu[]
}

export type PptxMediaRequestLease = {
  key: string
  generation: number
  controller: AbortController
}

/** A single-current-request gate for page media work across source/page changes. */
export function createPptxMediaRequestGate() {
  let generation = 0
  let current: PptxMediaRequestLease | null = null
  return {
    begin(key: string): PptxMediaRequestLease {
      current?.controller.abort()
      const lease = { key, generation: ++generation, controller: new AbortController() }
      current = lease
      return lease
    },
    cancel() {
      generation++
      current?.controller.abort()
      current = null
    },
    isCurrent(lease: PptxMediaRequestLease, key: string): boolean {
      return current === lease && generation === lease.generation && lease.key === key && !lease.controller.signal.aborted
    },
    get generation() { return generation },
  }
}

export async function sha256Hex(data: ArrayBuffer): Promise<string> {
  if (!globalThis.crypto?.subtle) throw new Error('WebCrypto SHA-256 is unavailable')
  const digest = await globalThis.crypto.subtle.digest('SHA-256', data)
  return Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, '0')).join('')
}

function parseXml(text: string): Document {
  if (typeof DOMParser === 'undefined') throw new Error('XML parser is unavailable')
  const xml = new DOMParser().parseFromString(text, 'application/xml')
  if (xml.getElementsByTagName('parsererror').length) throw new Error('Invalid PPTX relationship metadata')
  return xml
}

function normalizePackagePart(ownerPart: string, target: string): string | null {
  if (!target || target.includes('\\') || target.includes('?') || target.includes('#')) return null
  let decoded: string
  try { decoded = decodeURIComponent(target) } catch { return null }
  if (/^[a-z][a-z0-9+.-]*:/i.test(decoded)) return null
  const parts = decoded.startsWith('/') ? [] : ownerPart.split('/').slice(0, -1)
  for (const segment of decoded.replace(/^\/+/, '').split('/')) {
    if (!segment || segment === '.') continue
    if (segment === '..') {
      if (!parts.length) return null
      parts.pop()
      continue
    }
    parts.push(segment)
  }
  const result = parts.join('/')
  return result.startsWith('ppt/') ? result : null
}

function relationshipsPart(ownerPart: string): string {
  const slash = ownerPart.lastIndexOf('/')
  const directory = slash < 0 ? '' : ownerPart.slice(0, slash + 1)
  const name = slash < 0 ? ownerPart : ownerPart.slice(slash + 1)
  return `${directory}_rels/${name}.rels`
}

async function relationshipTargets(zip: JSZip, ownerPart: string): Promise<Map<string, string>> {
  const part = relationshipsPart(ownerPart)
  const file = zip.file(part)
  if (!file) return new Map()
  const xml = parseXml(await file.async('string'))
  const output = new Map<string, string>()
  for (const relation of Array.from(xml.getElementsByTagNameNS(PACKAGE_REL_NS, 'Relationship'))) {
    if (relation.getAttribute('TargetMode')?.toLowerCase() === 'external') continue
    const id = relation.getAttribute('Id') || ''
    const target = normalizePackagePart(ownerPart, relation.getAttribute('Target') || '')
    if (id && target && zip.file(target)) output.set(id, target)
  }
  return output
}

function relationId(element: Element, localName: string): string {
  return element.getAttributeNS(OFFICE_REL_NS, localName)
    || element.getAttribute(`r:${localName}`)
    || ''
}

function shapeBounds(pic: Element): PptxShapeBoundsEmu | null {
  const transform = pic.getElementsByTagNameNS(DRAWING_NS, 'xfrm')[0]
  if (!transform) return null
  const offset = transform.getElementsByTagNameNS(DRAWING_NS, 'off')[0]
  const extent = transform.getElementsByTagNameNS(DRAWING_NS, 'ext')[0]
  if (!offset || !extent) return null
  const values = {
    x: Number(offset.getAttribute('x')),
    y: Number(offset.getAttribute('y')),
    cx: Number(extent.getAttribute('cx')),
    cy: Number(extent.getAttribute('cy')),
  }
  return Object.values(values).every(Number.isFinite) ? values : null
}

/**
 * Resolve vector media on a physical slide using presentation display order,
 * slide relationships, and each picture's own embedded relationship. It does
 * not infer content from filenames or scan unrelated package members.
 */
export async function listPptxVectorMediaForSlide(data: ArrayBuffer, physicalPage: number): Promise<PptxVectorMedia[]> {
  if (!Number.isInteger(physicalPage) || physicalPage < 1) throw new Error('Invalid physical slide number')
  const zip = await JSZip.loadAsync(data)
  const presentationPart = zip.file('ppt/presentation.xml')
  if (!presentationPart) throw new Error('PPTX presentation part is missing')
  const presentation = parseXml(await presentationPart.async('string'))
  const slideIds = Array.from(presentation.getElementsByTagNameNS(PRESENTATION_NS, 'sldId'))
  if (physicalPage > slideIds.length) throw new Error('Physical slide is outside the presentation')

  const presentationRels = await relationshipTargets(zip, 'ppt/presentation.xml')
  const slidePart = presentationRels.get(relationId(slideIds[physicalPage - 1]!, 'id'))
  if (!slidePart || !slidePart.startsWith('ppt/slides/')) throw new Error('Physical slide relationship is invalid')
  const slideFile = zip.file(slidePart)
  if (!slideFile) throw new Error('Physical slide part is missing')
  const slide = parseXml(await slideFile.async('string'))
  const slideRels = await relationshipTargets(zip, slidePart)
  const pictures = Array.from(slide.getElementsByTagNameNS(PRESENTATION_NS, 'pic'))
  const byPart = new Map<string, PptxVectorMedia>()

  pictures.forEach((pic, index) => {
    const blip = pic.getElementsByTagNameNS(DRAWING_NS, 'blip')[0]
    if (!blip) return
    const part = slideRels.get(relationId(blip, 'embed'))
    if (!part || !VECTOR_MEDIA.test(part)) return
    let item = byPart.get(part)
    if (!item) {
      item = { part, shapeNames: [], shapeIndexes: [], shapeBoundsEmu: [] }
      byPart.set(part, item)
    }
    const properties = pic.getElementsByTagNameNS(PRESENTATION_NS, 'cNvPr')[0]
    const name = properties?.getAttribute('name')?.trim()
    if (name && !item.shapeNames.includes(name)) item.shapeNames.push(name)
    item.shapeIndexes.push(index + 1)
    const bounds = shapeBounds(pic)
    if (bounds) item.shapeBoundsEmu.push(bounds)
  })

  return [...byPart.values()]
}


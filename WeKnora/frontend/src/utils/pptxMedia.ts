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
  // WebCrypto exists only in secure contexts; plain-HTTP intranet deployments
  // still need the same fingerprint to request server-side vector rasters.
  const digest = globalThis.crypto?.subtle
    ? new Uint8Array(await globalThis.crypto.subtle.digest('SHA-256', data))
    : sha256Fallback(new Uint8Array(data))
  return Array.from(digest, (byte) => byte.toString(16).padStart(2, '0')).join('')
}

const SHA256_K = new Uint32Array([
  0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
  0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
  0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
  0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
  0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
  0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
  0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
  0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
])

/** FIPS 180-4 SHA-256 for contexts without WebCrypto; only the final partial block is copied for padding. */
export function sha256Fallback(bytes: Uint8Array): Uint8Array {
  const h = new Uint32Array([0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19])
  const w = new Uint32Array(64)
  const tail = new Uint8Array(128)
  const length = bytes.length
  const fullBlocks = Math.floor(length / 64)
  const rest = length - fullBlocks * 64
  tail.set(bytes.subarray(fullBlocks * 64))
  tail[rest] = 0x80
  const tailBlocks = rest < 56 ? 1 : 2
  const tailView = new DataView(tail.buffer)
  const bitLength = length * 8
  tailView.setUint32(tailBlocks * 64 - 8, Math.floor(bitLength / 0x100000000))
  tailView.setUint32(tailBlocks * 64 - 4, bitLength >>> 0)

  const compress = (view: DataView, offset: number) => {
    for (let i = 0; i < 16; i++) w[i] = view.getUint32(offset + i * 4)
    for (let i = 16; i < 64; i++) {
      const a = w[i - 15]!, b = w[i - 2]!
      const s0 = ((a >>> 7) | (a << 25)) ^ ((a >>> 18) | (a << 14)) ^ (a >>> 3)
      const s1 = ((b >>> 17) | (b << 15)) ^ ((b >>> 19) | (b << 13)) ^ (b >>> 10)
      w[i] = (w[i - 16]! + s0 + w[i - 7]! + s1) | 0
    }
    let a = h[0]!, b = h[1]!, c = h[2]!, d = h[3]!, e = h[4]!, f = h[5]!, g = h[6]!, k = h[7]!
    for (let i = 0; i < 64; i++) {
      const s1 = ((e >>> 6) | (e << 26)) ^ ((e >>> 11) | (e << 21)) ^ ((e >>> 25) | (e << 7))
      const t1 = (k + s1 + ((e & f) ^ (~e & g)) + SHA256_K[i]! + w[i]!) | 0
      const s0 = ((a >>> 2) | (a << 30)) ^ ((a >>> 13) | (a << 19)) ^ ((a >>> 22) | (a << 10))
      const t2 = (s0 + ((a & b) ^ (a & c) ^ (b & c))) | 0
      k = g; g = f; f = e; e = (d + t1) | 0; d = c; c = b; b = a; a = (t1 + t2) | 0
    }
    h[0] = h[0]! + a; h[1] = h[1]! + b; h[2] = h[2]! + c; h[3] = h[3]! + d
    h[4] = h[4]! + e; h[5] = h[5]! + f; h[6] = h[6]! + g; h[7] = h[7]! + k
  }

  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength)
  for (let block = 0; block < fullBlocks; block++) compress(view, block * 64)
  for (let block = 0; block < tailBlocks; block++) compress(tailView, block * 64)
  const out = new Uint8Array(32)
  const outView = new DataView(out.buffer)
  for (let i = 0; i < 8; i++) outView.setUint32(i * 4, h[i]!)
  return out
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
async function readPhysicalSlide(zip: JSZip, physicalPage: number) {
  if (!Number.isInteger(physicalPage) || physicalPage < 1) throw new Error('Invalid physical slide number')
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
  return { slide, slidePart }
}

export async function listPptxVectorMediaForSlide(data: ArrayBuffer, physicalPage: number): Promise<PptxVectorMedia[]> {
  const zip = await JSZip.loadAsync(data)
  const { slide, slidePart } = await readPhysicalSlide(zip, physicalPage)
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

/** Make cached vector pictures visible to the browser renderer on every slide.
 * The renderer ignores OLE frames; these are only their existing snapshots,
 * never the linked workbook or executable object. No original bytes change.
 */
export async function preparePptxOlePreview(data: ArrayBuffer): Promise<ArrayBuffer> {
  const zip = await JSZip.loadAsync(data)
  const presentation = zip.file('ppt/presentation.xml')
  if (!presentation) throw new Error('PPTX presentation part is missing')
  const count = parseXml(await presentation.async('string')).getElementsByTagNameNS(PRESENTATION_NS, 'sldId').length
  let changed = false
  for (let page = 1; page <= count; page++) {
    const { slide, slidePart } = await readPhysicalSlide(zip, page)
    const targets = await relationshipTargets(zip, slidePart)
    let slideChanged = false
    for (const frame of Array.from(slide.getElementsByTagNameNS(PRESENTATION_NS, 'graphicFrame'))) {
      if (!frame.getElementsByTagNameNS(PRESENTATION_NS, 'oleObj').length) continue
      const cached = Array.from(frame.getElementsByTagNameNS(PRESENTATION_NS, 'pic')).filter(pic => {
        const blip = pic.getElementsByTagNameNS(DRAWING_NS, 'blip')[0]
        const part = blip && targets.get(relationId(blip, 'embed'))
        return part && VECTOR_MEDIA.test(part)
      })
      if (cached.length !== 1 || !shapeBounds(cached[0]!)) continue
      frame.parentNode!.replaceChild(cached[0]!.cloneNode(true), frame)
      slideChanged = true
    }
    if (slideChanged) {
      zip.file(slidePart, new XMLSerializer().serializeToString(slide))
      changed = true
    }
  }
  return changed ? zip.generateAsync({ type: 'arraybuffer' }) : data
}

/** Replace only verified vector snapshots on one physical slide in a preview
 * copy. An OLE fallback picture becomes a normal picture in the same tree
 * position, preserving its own crop, transform and surrounding group. The
 * original bytes, embedded workbook and other slides are never changed.
 */
export async function rasterizePptxSlidePreview(
  data: ArrayBuffer,
  physicalPage: number,
  rasters: Array<{ part: string; png: ArrayBuffer }>,
): Promise<ArrayBuffer> {
  if (!rasters.length) return data
  const zip = await JSZip.loadAsync(data)
  const { slide, slidePart } = await readPhysicalSlide(zip, physicalPage)
  const targets = await relationshipTargets(zip, slidePart)
  const pictures = Array.from(slide.getElementsByTagNameNS(PRESENTATION_NS, 'pic'))
  const referenced = new Set(pictures.flatMap(pic => {
    const blip = pic.getElementsByTagNameNS(DRAWING_NS, 'blip')[0]
    const part = blip && targets.get(relationId(blip, 'embed'))
    return part && VECTOR_MEDIA.test(part) ? [part] : []
  }))
  const replacements = new Map<string, string>()
  for (const { part, png } of rasters) {
    if (!referenced.has(part) || replacements.has(part)) throw new Error('Raster is not a unique vector on this slide')
    const signature = new Uint8Array(png, 0, Math.min(8, png.byteLength))
    if (signature.length !== 8 || ![137, 80, 78, 71, 13, 10, 26, 10].every((b, i) => signature[i] === b)) throw new Error('Invalid PNG snapshot')
    let rasterPart = `${part}.preview.png`
    for (let suffix = 1; zip.file(rasterPart); suffix++) rasterPart = `${part}.preview-${suffix}.png`
    zip.file(rasterPart, png)
    replacements.set(part, rasterPart)
  }
  const relsPart = relationshipsPart(slidePart)
  const rels = parseXml(await zip.file(relsPart)!.async('string'))
  for (const rel of Array.from(rels.getElementsByTagNameNS(PACKAGE_REL_NS, 'Relationship'))) {
    const oldPart = targets.get(rel.getAttribute('Id') || '')
    const rasterPart = oldPart && replacements.get(oldPart)
    if (rasterPart) {
      const owner = slidePart.split('/').slice(0, -1)
      const target = rasterPart.split('/')
      while (owner.length && target.length && owner[0] === target[0]) { owner.shift(); target.shift() }
      rel.setAttribute('Target', '../'.repeat(owner.length) + target.join('/'))
    }
  }
  for (const frame of Array.from(slide.getElementsByTagNameNS(PRESENTATION_NS, 'graphicFrame'))) {
    if (!frame.getElementsByTagNameNS(PRESENTATION_NS, 'oleObj').length) continue
    const cached = Array.from(frame.getElementsByTagNameNS(PRESENTATION_NS, 'pic')).filter(pic => {
      const blip = pic.getElementsByTagNameNS(DRAWING_NS, 'blip')[0]
      const part = blip && targets.get(relationId(blip, 'embed'))
      return part && replacements.has(part)
    })
    // Ambiguous or unpositioned OLE previews remain untouched, with the
    // standalone source image still available to the reader.
    if (cached.length !== 1 || !shapeBounds(cached[0]!)) continue
    frame.parentNode!.replaceChild(cached[0]!.cloneNode(true), frame)
  }
  const typesFile = zip.file('[Content_Types].xml')
  if (!typesFile) throw new Error('PPTX content types are missing')
  const types = parseXml(await typesFile.async('string'))
  const typesNs = 'http://schemas.openxmlformats.org/package/2006/content-types'
  const pngType = Array.from(types.getElementsByTagNameNS(typesNs, 'Default')).find(e => e.getAttribute('Extension')?.toLowerCase() === 'png')
  if (pngType && pngType.getAttribute('ContentType') !== 'image/png') throw new Error('Invalid PNG content type')
  if (!pngType) {
    const entry = types.createElementNS(typesNs, 'Default')
    entry.setAttribute('Extension', 'png')
    entry.setAttribute('ContentType', 'image/png')
    types.documentElement.appendChild(entry)
  }
  const serializer = new XMLSerializer()
  zip.file('[Content_Types].xml', serializer.serializeToString(types))
  zip.file(relsPart, serializer.serializeToString(rels))
  zip.file(slidePart, serializer.serializeToString(slide))
  return zip.generateAsync({ type: 'arraybuffer' })
}


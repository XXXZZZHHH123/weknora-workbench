import JSZip from 'jszip'
import { listPptxVectorMediaForSlide, type PptxVectorMedia } from './pptxMedia'

export type PptxPageMediaView = PptxVectorMedia & {
  generation: number
  objectUrl?: string
  state: 'pending' | 'loading' | 'loaded' | 'displayed' | 'failed'
}

/** One source lifetime, two requests at most. Pages are admitted by the viewport,
 * not by the citation alone. PNGs replace byte-identical original image sources
 * in place, preserving the renderer's crop/group transform and scroll position.
 */
export function createPptxViewportMedia(options: {
  data: ArrayBuffer
  sourceSha256: string
  generation: number
  loadRaster: (page: number, part: string, signal: AbortSignal) => Promise<Blob>
  onPage: (page: number, items: PptxPageMediaView[]) => void
  onPageError: (page: number) => void
}) {
  const controller = new AbortController()
  const zip = JSZip.loadAsync(options.data)
  const pages = new Map<number, PptxPageMediaView[]>()
  const pageTasks = new Map<number, Promise<void>>()
  const parts = new Map<string, { source: string; url?: string; state: PptxPageMediaView['state']; task?: Promise<void> }>()
  const urls = new Set<string>()
  const queue: Array<() => Promise<void>> = []
  let running = 0
  let root: ParentNode | null = null
  let observer: IntersectionObserver | null = null
  let mutations: MutationObserver | null = null
  const observed = new Set<Element>()
  const active = () => !controller.signal.aborted
  const publish = (page: number) => {
    if (!active()) return
    const items = pages.get(page) || []
    const slide = root?.querySelectorAll<HTMLElement>('.pptx-preview-slide-wrapper')[page - 1]
    if (slide) slide.dataset.pptxMediaState = !items.length ? 'none'
      : items.some(item => item.state === 'failed') ? 'failed'
        : items.every(item => item.state === 'displayed') ? 'displayed' : 'loading'
    options.onPage(page, items.map(item => ({ ...item })))
  }
  const publishPart = (part: string) => {
    const value = parts.get(part)
    if (!value || !active()) return
    for (const [page, items] of pages) {
      for (const item of items) if (item.part === part) {
        item.objectUrl = value.url
        item.state = value.state
      }
      if (items.some(item => item.part === part)) publish(page)
    }
  }
  const mark = (part: string, state: 'displayed' | 'failed') => {
    const item = parts.get(part)
    if (!item || !active()) return
    item.state = state
    publishPart(part)
    paint()
  }
  const paint = () => {
    if (!root || !active()) return
    const slides = Array.from(root.querySelectorAll('.pptx-preview-slide-wrapper'))
    for (const [page, items] of pages) {
      const slide = slides[page - 1]
      if (!slide) continue
      for (const item of items) {
        const media = parts.get(item.part)
        if (!media) continue
        for (const image of Array.from(slide.querySelectorAll<HTMLImageElement>('img'))) {
          const original = image.getAttribute('src')
          const previouslyVerified = image.dataset.pptxVectorPart === item.part && image.dataset.pptxVectorSource === options.sourceSha256
          // The renderer may choose image/* for uppercase extensions. Identity
          // comes from the embedded bytes, not its guessed MIME label.
          const comma = original?.indexOf(',') ?? -1
          const identicalBytes = comma >= 0 && /^data:[^,]*;base64$/i.test(original!.slice(0, comma)) &&
            original!.slice(comma + 1) === media.source.slice(media.source.indexOf(',') + 1)
          if (!identicalBytes && !previouslyVerified) continue
          image.dataset.pptxVectorPart = item.part
          image.dataset.pptxVectorSource = options.sourceSha256
          image.dataset.pptxMediaState = media.state
          if (!media.url || original === media.url) continue
          image.addEventListener('load', () => { if (active() && image.getAttribute('src') === media.url) mark(item.part, 'displayed') }, { once: true, signal: controller.signal })
          image.addEventListener('error', () => { if (active() && image.getAttribute('src') === media.url) mark(item.part, 'failed') }, { once: true, signal: controller.signal })
          image.src = media.url
        }
      }
    }
  }
  const drain = () => {
    while (active() && running < 2 && queue.length) {
      const work = queue.shift()!
      running++
      void work().finally(() => { running--; drain() })
    }
  }
  async function request(page: number, part: string, retry: boolean) {
    let value = parts.get(part)
    if (value && !(retry && value.state === 'failed')) { publishPart(part); return value.task }
    const file = (await zip).file(part)
    if (!file || !active()) return
    const source = `data:image/x-${part.toLowerCase().endsWith('.emf') ? 'emf' : 'wmf'};base64,${await file.async('base64')}`
    if (!active()) return
    // Another visible page can reference the same original media part.
    value = parts.get(part)
    if (value && !(retry && value.state === 'failed')) { publishPart(part); return value.task }
    value = { source, state: 'pending' }
    const media = value
    parts.set(part, media)
    media.task = new Promise<void>(resolve => {
      queue.push(async () => {
        if (!active()) { resolve(); return }
        media.state = 'loading'; publishPart(part)
        try {
          const blob = await options.loadRaster(page, part, controller.signal)
          if (!active()) return
          if (blob.type && blob.type !== 'image/png') throw new Error('Invalid PNG content type')
          const signature = new Uint8Array(await blob.slice(0, 8).arrayBuffer())
          if (signature.length !== 8 || ![137, 80, 78, 71, 13, 10, 26, 10].every((b, i) => signature[i] === b)) throw new Error('Invalid PNG snapshot')
          if (!active()) return
          media.url = URL.createObjectURL(blob); urls.add(media.url)
          media.state = 'loaded'; publishPart(part); paint()
        } catch {
          if (active()) { media.state = 'failed'; publishPart(part) }
        } finally { resolve() }
      })
      drain()
    })
    return media.task
  }
  async function loadPage(page: number, retry = false): Promise<void> {
    if (!active() || !Number.isSafeInteger(page) || page < 1) return
    if (pageTasks.has(page) && !retry) return pageTasks.get(page)
    const task = (async () => {
      try {
        let items = pages.get(page)
        if (!items) {
          items = (await listPptxVectorMediaForSlide(options.data, page)).map(item => ({ ...item, generation: options.generation, state: 'pending' as const }))
          if (!active()) return
          pages.set(page, items)
        }
        publish(page)
        await Promise.all(items.map(item => request(page, item.part, retry)))
        paint()
      } catch { if (active()) options.onPageError(page) }
    })()
    pageTasks.set(page, task)
    return task
  }
  function scan() {
    if (!root || !active()) return
    const slides = Array.from(root.querySelectorAll('.pptx-preview-slide-wrapper'))
    slides.forEach((slide, index) => {
      (slide as HTMLElement).dataset.pptxSourcePage = String(index + 1)
      if (!observed.has(slide)) {
        observed.add(slide)
        observer?.observe(slide)
      }
      // Unsupported IntersectionObserver still loads only pages in the view.
      if (!observer) {
        const rect = slide.getBoundingClientRect()
        if (rect.bottom >= 0 && rect.top < globalThis.innerHeight) void loadPage(index + 1)
      }
    })
    paint()
  }
  return {
    loadPage,
    markDisplayed: (part: string) => mark(part, 'displayed'),
    markFailed: (part: string) => mark(part, 'failed'),
    observe(element: HTMLElement) {
      if (!active()) return
      root = element
      if (typeof IntersectionObserver !== 'undefined') {
        observer = new IntersectionObserver(entries => {
          const slides = Array.from(element.querySelectorAll('.pptx-preview-slide-wrapper'))
          for (const entry of entries) if (entry.isIntersecting) {
            const index = slides.indexOf(entry.target)
            if (index >= 0) void loadPage(index + 1)
          }
        }, { root: element, rootMargin: '200px 0px' })
      }
      if (typeof MutationObserver !== 'undefined') {
        mutations = new MutationObserver(scan)
        mutations.observe(element, { childList: true, subtree: true })
      }
      if (!observer) element.addEventListener('scroll', scan, { passive: true, signal: controller.signal })
      scan()
    },
    stop() {
      controller.abort()
      observer?.disconnect(); mutations?.disconnect()
      for (const url of urls) URL.revokeObjectURL(url)
      urls.clear()
      // Release queued promises without starting further network requests.
      for (const work of queue.splice(0)) void work()
    },
  }
}

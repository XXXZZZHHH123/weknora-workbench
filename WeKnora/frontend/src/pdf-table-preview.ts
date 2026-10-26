import { loadPdfJs, type PdfJs } from './components/source-preview/pdfjsLoader'
import './pdf-table-preview.css'

type Document = Awaited<ReturnType<PdfJs['getDocument']>['promise']>
const root = document.querySelector<HTMLDivElement>('#preview')!
root.innerHTML = '<div class="toolbar"><button id="previous" aria-label="上一页">‹</button><span id="page" aria-live="polite"></span><button id="next" aria-label="下一页">›</button><span class="spacer"></span><button id="minus" aria-label="缩小">−</button><button id="fit">适合宽度</button><button id="plus" aria-label="放大">＋</button></div><div id="status" role="status"></div><div id="scroll"><div id="paper"></div></div>'
const paper = document.querySelector<HTMLDivElement>('#paper')!
const scroller = document.querySelector<HTMLDivElement>('#scroll')!
const status = document.querySelector<HTMLDivElement>('#status')!
let pdfDocument: Document | null = null
let library: PdfJs | null = null
let page = 1
let zoom = 1
let version = 0
let openVersion = 0
let task: ReturnType<Awaited<ReturnType<Document['getPage']>>['render']> | null = null
let loadingTask: ReturnType<PdfJs['getDocument']> | null = null

function send(type: string, extra = {}) { window.parent.postMessage({ type, ...extra }, location.origin) }
async function render() {
  const saved = ++version
  task?.cancel()
  if (!pdfDocument || !library) return
  const doc = pdfDocument
  page = Math.max(1, Math.min(doc.numPages, page))
  const selected = page
  status.textContent = '正在显示原件…'
  paper.replaceChildren() // Never leave a previous page visible under a new page label.
  window.document.querySelector('#page')!.textContent = `第 ${selected} / ${doc.numPages} 页`
  ;(window.document.querySelector('#previous') as HTMLButtonElement).disabled = selected === 1
  ;(window.document.querySelector('#next') as HTMLButtonElement).disabled = selected === doc.numPages
  try {
    const pdfPage = await doc.getPage(selected)
    if (saved !== version || doc !== pdfDocument) return
    const base = pdfPage.getViewport({ scale: 1 })
    const viewport = pdfPage.getViewport({ scale: Math.max(180, scroller.clientWidth - 24) / base.width * zoom })
    const canvas = window.document.createElement('canvas')
    const ratio = Math.min(devicePixelRatio || 1, 2)
    canvas.width = Math.floor(viewport.width * ratio)
    canvas.height = Math.floor(viewport.height * ratio)
    canvas.style.width = `${viewport.width}px`
    canvas.style.height = `${viewport.height}px`
    const rendering = pdfPage.render({ canvas, viewport, transform: [ratio, 0, 0, ratio, 0, 0] })
    task = rendering
    await rendering.promise
    if (saved !== version || doc !== pdfDocument) return
    paper.append(canvas)
    paper.dataset.page = String(selected)
    scroller.scrollTop = 0
    status.textContent = ''
    send('pdf-source-rendered', { page: selected, pageCount: doc.numPages })
  } catch (error) {
    if (saved !== version) return
    status.textContent = '此页预览失败，可打开原件查看。'
    send('pdf-source-error')
  }
}

window.addEventListener('message', async event => {
  if (event.source !== window.parent || event.origin !== location.origin) return
  const message = event.data
  if (message?.type === 'pdf-source-page' && Number.isInteger(message.page) && message.page > 0) {
    page = message.page
    await render()
  }
  if (message?.type !== 'pdf-source-open' || !(message.data instanceof ArrayBuffer)) return
  const opened = ++openVersion
  version++
  task?.cancel()
  const previous = loadingTask
  loadingTask = null
  pdfDocument = null
  paper.replaceChildren()
  page = Number.isInteger(message.page) ? message.page : 1
  status.textContent = '正在读取原件…'
  try {
    await previous?.destroy()
    const lib = await loadPdfJs()
    if (opened !== openVersion) return
    library = lib
    const loading = lib.getDocument({ data: new Uint8Array(message.data) })
    loadingTask = loading
    const doc = await loading.promise
    if (opened !== openVersion) { await loading.destroy(); return }
    pdfDocument = doc
    zoom = 1
    for (const key of ['--font', '--text', '--background', '--border']) {
      const value = message.theme?.[key]
      if (typeof value === 'string') window.document.documentElement.style.setProperty(key, value)
    }
    await render()
  } catch {
    if (opened === openVersion) { status.textContent = '无法显示原件，请使用“打开原件”。'; send('pdf-source-error') }
  }
})
window.document.querySelector('#previous')!.addEventListener('click', () => { page--; void render() })
window.document.querySelector('#next')!.addEventListener('click', () => { page++; void render() })
for (const [id, factor] of [['minus', 1 / 1.25], ['plus', 1.25]] as const) {
  window.document.querySelector(`#${id}`)!.addEventListener('click', () => { zoom = Math.max(.5, Math.min(4, zoom * factor)); void render() })
}
window.document.querySelector('#fit')!.addEventListener('click', () => { zoom = 1; void render() })
let resizeTimer: ReturnType<typeof setTimeout>
new ResizeObserver(() => { clearTimeout(resizeTimer); resizeTimer = setTimeout(() => void render(), 100) }).observe(scroller)
window.addEventListener('pagehide', () => { openVersion++; version++; task?.cancel(); void loadingTask?.destroy() })
send('pdf-source-ready')

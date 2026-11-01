<template>
  <section v-if="evidence || sources?.length" class="analysis-evidence">
    <details v-if="evidence">
    <summary>查看分析取数与计算依据</summary>
    <p>来源：{{ evidence.source?.file }}。本次计算读取 {{ evidence.input_count }} 条记录。</p>
    <p v-for="(input,id) in evidence.inputs" :key="String(id)">
      {{ input.series.sheet }} · {{ input.series.metric }}
      <span>（{{ input.series.unit || '单位待核对' }}）</span><br />
      实际记录：{{ input.actual_first_date || '无日期' }} 至 {{ input.actual_last_date || '无日期' }}，有效 {{ input.valid_count }} 条，缺失 {{ Math.max(0, Number(input.input_count || 0) - Number(input.valid_count || 0)) }} 条。
      表头：{{ input.series.header_cells?.join('、') }}。
      <span v-if="input.example_cells?.length">示例来源单元格：{{ input.example_cells.join('、') }}（仅为输入示例，不代表完整 SQL 行级血缘）</span>
    </p>
    <div class="analysis-table"><table>
      <thead><tr><th v-for="(column,i) in evidence.columns" :key="i">{{ column }}</th></tr></thead>
      <tbody><tr v-for="(row,i) in evidence.rows" :key="i"><td v-for="(value,j) in row" :key="j">{{ value === null ? '无有效值' : value }}</td></tr></tbody>
    </table></div>
    <p v-if="evidence.output_truncated">仅展示部分结果行，计算使用了上述全部输入。</p>
    <p v-if="evidence.input_complete === false">输入覆盖不完整；不能将当前结果解释为全表统计。</p>
    <details><summary>查看计算表达式</summary><pre>{{ evidence.sql }}</pre></details>
    </details>
    <section v-if="sources?.length" class="source-locator">
      <h3>打开原始来源</h3>
      <article v-for="item in sources" :key="sourceKey(item)" class="source-card">
        <strong>{{ item.data.source?.file || item.data.source?.file_name || (item.kind === 'pdf' ? 'PDF 原始表格' : 'Excel 原始表格') }}</strong>
        <span v-if="item.kind === 'pdf'"> · PDF 逻辑表 {{ item.data.table_id || (item.data.tables || []).map((x:any) => x.table_id).join('、') }}</span>
        <template v-if="item.kind === 'excel'">
          <p v-if="!item.context" class="blocked">历史工具结果未保存知识库范围，无法安全定位原件。</p>
          <button v-else type="button" :disabled="busyKey === sourceKey(item)" @click="openExcel(item)">{{ busyKey === sourceKey(item) ? '正在核验原件…' : '查看原始表格单元格' }}</button>
          <div v-if="item.kind === 'excel' && activeSourceKey === sourceKey(item) && sourceCellsFor(item.data).length" class="source-cell-picker">
            <p>来源坐标 {{ sourceCellPageStart(item) + 1 }}–{{ Math.min(sourceCellPageStart(item) + sourceCellPageSize, sourceCellsFor(item.data).length) }} / {{ sourceCellsFor(item.data).length }}；点击单元格可查看其附近的原表窗口。</p>
            <button v-for="cell in visibleSourceCells(item)" :key="`${cell.sheet}:${cell.address}`" type="button" :aria-pressed="selectedCell?.sheet === cell.sheet && selectedCell?.address === cell.address" @click="openExcel(item, cell)">{{ cell.sheet }}!{{ cell.address }}</button>
            <button v-if="sourceCellPageStart(item) > 0" type="button" @click="changeSourceCellPage(item, -1)">上一组坐标</button>
            <button v-if="sourceCellPageStart(item) + sourceCellPageSize < sourceCellsFor(item.data).length" type="button" @click="changeSourceCellPage(item, 1)">下一组坐标</button>
          </div>
        </template>
        <template v-else>
          <p v-if="!item.context" class="blocked">历史工具结果未保存知识库范围，无法安全定位原件。</p>
          <template v-else>
            <p>逻辑表状态：{{ (item.data.review_status || item.data.status) === 'auto_accepted' ? '自动校验入库（非人工确认）' : (item.data.review_status || item.data.status) === 'reviewed' ? '人工确认映射' : '工具返回状态未标注' }}；抽取正确性和整份文档覆盖仍须以原页核对。</p>
            <p v-if="pdfPages(item.data).length">工具结果包含原 PDF 页：{{ pdfPages(item.data).join('、') }}</p>
            <p v-else>分析结果未携带原页坐标；打开时将按真实逻辑表读取单元格出处。</p>
            <button v-if="canOpenPdf(item)" type="button" :disabled="busyKey === sourceKey(item)" @click="openPdf(item)">{{ busyKey === sourceKey(item) ? '正在核验 PDF 原件…' : '查看 PDF 原页与表格单元格' }}</button>
            <p v-else-if="item.data.source?.knowledge_base_id && item.data.source.knowledge_base_id !== item.context?.knowledge_base_id" class="blocked">PDF来源范围与当前已验证知识库不一致，已拒绝定位。</p>
            <p v-else-if="!item.data.source?.knowledge_id" class="fine-print">PDF 结果没有经验证的原件文档 ID，无法安全打开原页。</p>
            <p v-else-if="!item.data.table_id || !item.data.source?.sha256" class="fine-print">PDF 结果缺少真实逻辑表 ID 或原件 hash，无法安全定位。</p>
            <p v-if="item.data.coverage?.notice">{{ item.data.coverage.notice }}</p>
            <details v-if="pdfReadResult?.table_id === item.data.table_id"><summary>查看已核验 PDF 表行与单元格来源</summary><pre>{{ JSON.stringify(pdfReadResult.rows, null, 2) }}</pre></details>
            <details v-else-if="pdfRows(item.data).length"><summary>查看工具返回的 PDF 表行与单元格来源</summary><pre>{{ JSON.stringify(pdfRows(item.data), null, 2) }}</pre></details>
            <p class="fine-print">聚合 SQL 的输入数量不是 SQL 输出行的逐格血缘；实际 PDF 来源按工具返回的页码和单元格映射核对。</p>
          </template>
        </template>
        <p v-if="loadError && activeSourceKey === sourceKey(item)" role="alert" class="blocked">{{ loadError }}</p>
      </article>
      <div v-for="view in sheetViews" :key="view.sheet_name" class="original-sheet" aria-label="原始工作表单元格">
        <p>{{ view.sheet_name }} · 当前窗口 {{ view.range.start_row }}–{{ view.range.end_row }} 行、{{ view.range.start_col }}–{{ view.range.end_col }} 列 · 已核验来源 hash · 目标 {{ selectedCell?.sheet }}!{{ selectedCell?.address }}</p>
        <div class="analysis-table"><table>
          <tbody><tr v-for="row in viewRows(view)" :key="row.number">
            <th>{{ row.number }}</th><td v-for="cell in row.cells" :key="cell.ref" :class="{ highlighted: selectedCell?.sheet === view.sheet_name && selectedCell?.address === cell.ref }" :data-source-cell="cell.ref">{{ cell.display ?? cell.value ?? '' }}</td>
          </tr></tbody>
        </table></div>
        <p v-for="warning in view.warnings || []" :key="warning" class="fine-print">{{ warning }}</p>
      </div>
    </section>
    <div v-if="pdfPreview" class="pdf-source-overlay" role="dialog" aria-modal="true" :aria-label="pdfPreview.fileName + ' 原始 PDF 来源'">
      <header><strong>{{ pdfPreview.fileName }} · 第 {{ pdfPreview.page }} 页原件</strong><button type="button" @click="closePdf">关闭</button></header>
      <p v-if="pdfPreviewError" role="alert" class="blocked">{{ pdfPreviewError }}</p>
      <DocumentPreview v-else class="pdf-source-viewer" :source-blob="pdfPreview.blob" file-type="pdf" :file-name="pdfPreview.fileName" :active="true" source-mode fill-height :locate="pdfPreview.locate" />
    </div>
    <p v-if="loadError && !sources?.length" role="alert" class="blocked">{{ loadError }}</p>
  </section>
</template>
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useAuthStore } from '@/stores/auth'
import { fetchWithAuthRetry } from '@/utils/authFetch'
import DocumentPreview from '@/components/document-preview.vue'
import type { SourceLocateRequest } from '@/utils/sourceLocator'
import { isStructuredImportId, parseExcelCell, type StructuredAnswerSource } from '@/utils/structuredAnswerSources'

type SheetView = { sheet_name: string; cells: Array<{row:number;column:number;ref:string;display?:string;value?:unknown}>; warnings?: string[]; range: Record<string,number> }
type PdfPreview = { blob: Blob; fileName: string; knowledgeId: string; page: number; locate: SourceLocateRequest }
const props = defineProps<{ evidence: any; sources?: StructuredAnswerSource[] }>()
const auth = useAuthStore()
const busyKey = ref(''), activeSourceKey = ref(''), loadError = ref(''), sheetViews = ref<SheetView[]>([])
const pdfPreview = ref<PdfPreview | null>(null), pdfPreviewError = ref('')
const pdfReadResult = ref<any | null>(null), selectedCell = ref<{sheet:string;address:string} | null>(null)
const sourceCellPage = ref(0), sourceCellPageSize = 12
let operationEpoch = 0
let pdfLocateToken = 0
function sourceCellsFor(data: any) {
  const cells: Array<{sheet:string;address:string}> = []
  const add = (sheet: unknown, address: unknown) => {
    const parsed = parseExcelCell(address)
    if (typeof sheet === 'string' && sheet && parsed && !cells.some(c => c.sheet === sheet && c.address === parsed.address)) cells.push({ sheet, address: parsed.address })
  }
  const defaultSheet = data?.source?.sheet
  for (const row of data?.rows || []) add(row.sheet || defaultSheet, row.source_cell)
  for (const input of Object.values(data?.inputs || {}) as any[]) {
    for (const cell of input?.example_cells || []) add(input.series?.sheet || defaultSheet, cell)
  }
  return cells
}
const activeSource = computed(() => (props.sources || []).find(item => sourceKey(item) === activeSourceKey.value))
const activeSourceStamp = ref('')
function viewRows(view: SheetView) {
  const cells = new Map((view.cells || []).map(cell => [cell.ref, cell]))
  const rows: Array<{number:number;cells:any[]}> = []
  const { start_row: startRow, end_row: endRow, start_col: startColumn, end_col: endColumn } = view.range
  for (let row = startRow; row <= endRow; row++) {
    const rowCells = []
    for (let column = startColumn; column <= endColumn; column++) {
      let n = column, letters = ''
      while (n > 0) { const rem = (n - 1) % 26; letters = String.fromCharCode(65 + rem) + letters; n = Math.floor((n - 1) / 26) }
      const ref = `${letters}${row}`
      rowCells.push(cells.get(ref) || { row, column, ref, display: '' })
    }
    rows.push({ number: row, cells: rowCells })
  }
  return rows
}
function sourceKey(item: StructuredAnswerSource) {
  return JSON.stringify([
    item.kind,
    item.data.dataset_id || item.data.table_id,
    item.operation,
    item.data.series?.series_id || item.data.table_id || '',
    item.context?.knowledge_base_id || '',
    item.context?.knowledge_base_tenant_id ?? '',
  ])
}
function sourceStamp(item: StructuredAnswerSource) { return [sourceKey(item), item.context?.knowledge_base_id || '', item.context?.knowledge_base_tenant_id || '', item.data.source?.sha256 || ''].join(':') }
function sourceCellPageStart(item: StructuredAnswerSource) { return activeSourceKey.value === sourceKey(item) ? sourceCellPage.value * sourceCellPageSize : 0 }
function visibleSourceCells(item: StructuredAnswerSource) { const cells = sourceCellsFor(item.data); const start = sourceCellPageStart(item); return cells.slice(start, start + sourceCellPageSize) }
function changeSourceCellPage(item: StructuredAnswerSource, offset: number) {
  if (activeSourceKey.value !== sourceKey(item)) { activeSourceKey.value = sourceKey(item); sourceCellPage.value = 0 }
  sourceCellPage.value = Math.max(0, sourceCellPage.value + offset)
}
function canOpenPdf(item: StructuredAnswerSource) {
  return item.kind === 'pdf' && !!item.context && typeof item.data.source?.knowledge_id === 'string' &&
    /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(item.data.source.knowledge_id) &&
    (!item.data.source?.knowledge_base_id || item.data.source.knowledge_base_id === item.context.knowledge_base_id) && !!item.data.table_id && !!item.data.source?.sha256
}
async function openPdf(item: StructuredAnswerSource) {
  if (!canOpenPdf(item)) return
  const key = sourceKey(item), knowledgeId = String(item.data.source.knowledge_id), sourceHash = String(item.data.source.sha256)
  const epoch = ++operationEpoch
  activeSourceKey.value = key; activeSourceStamp.value = sourceStamp(item); loadError.value = ''; pdfPreviewError.value = ''; pdfPreview.value = null; pdfReadResult.value = null; sheetViews.value = []; busyKey.value = key
  const user = auth.user?.id || '', tenant = String(auth.selectedTenantId ?? auth.tenant?.id ?? '')
  if (!auth.token || !user) { busyKey.value = ''; loadError.value = '登录状态已失效，请重新登录后重试。'; return }
  try {
    const scopedTenant = item.context!.knowledge_base_tenant_id
    const table = await getJSON(`/structured/api/pdf/tables/${encodeURIComponent(item.data.table_id)}?offset=0&limit=100`, item.context!.knowledge_base_id,
      { token: auth.token, user, tenant }, scopedTenant)
    if (!isCurrentOperation(epoch, key, item.context!.knowledge_base_id, user, tenant, sourceHash)) return
    if (table?.table_id !== item.data.table_id || String(table?.source?.sha256 || '').toLowerCase() !== String(item.data.source.sha256).toLowerCase()) {
      throw new Error('当前 PDF 表记录 hash 与回答中的证据不一致，已拒绝显示旧页。')
    }
    pdfReadResult.value = table
    const locators = pdfSourceLocators(table)
    const first: any = locators[0] || { type: 'pdf', mapping: 'exact', page: pdfPages(table)[0], granularity: 'page' }
    const response = await fetchWithAuthRetry(`/api/v1/knowledge/${encodeURIComponent(knowledgeId)}/preview`, { headers: requestHeaders(item.context!.knowledge_base_id, scopedTenant) }, {
      token: auth.token,
      isCurrent: () => (auth.user?.id || '') === user && String(auth.selectedTenantId ?? auth.tenant?.id ?? '') === tenant,
    })
    if (!isCurrentOperation(epoch, key, item.context!.knowledge_base_id, user, tenant, sourceHash)) return
    if (!response.ok) {
      if (response.status === 401 || response.status === 403) throw new Error('当前账号或知识库无权读取 PDF 原件。')
      if (response.status === 404 || response.status === 409) throw new Error('原 PDF 已不存在或来源版本已改变。')
      throw new Error(`PDF 原件读取失败（HTTP ${response.status}）。`)
    }
    const blob = await response.blob()
    const digest = await crypto.subtle.digest('SHA-256', await blob.arrayBuffer())
    if (!isCurrentOperation(epoch, key, item.context!.knowledge_base_id, user, tenant, sourceHash)) return
    const actual = Array.from(new Uint8Array(digest), byte => byte.toString(16).padStart(2, '0')).join('')
    if (actual.toLowerCase() !== String(item.data.source.sha256).toLowerCase()) throw new Error('当前 PDF 原件 hash 与分析证据不一致，已拒绝显示旧页。')
    const page = first.page || pdfPages(table)[0]
    if (!page) throw new Error('当前逻辑表没有可定位的真实 PDF 页码，已拒绝打开未定位的原件。')
    pdfPreview.value = { blob, fileName: table.source.file_name || table.source.file || item.data.source.file_name || 'PDF 原件', knowledgeId, page,
      locate: { locators: [first], quotes: first.quote ? [first.quote] : [], token: ++pdfLocateToken } }
  } catch (error: any) {
    if (!isCurrentOperation(epoch, key, item.context!.knowledge_base_id, user, tenant, sourceHash)) return
    const status = error?.response?.status
    loadError.value = status === 401 || status === 403 ? '当前账号或知识库无权读取 PDF 原件。' : error?.message || 'PDF 原件读取失败，请稍后重试。'
  } finally { if (operationEpoch === epoch && busyKey.value === key) busyKey.value = '' }
}
function closePdf() { pdfPreview.value = null; pdfPreviewError.value = '' }

function pdfRows(data: any) { return data.rows || data.row_provenance || [] }
function pdfSourceLocators(data: any): SourceLocateRequest['locators'] {
  const expectedHash = String(data?.source?.sha256 || '').toLowerCase(), found: SourceLocateRequest['locators'] = []
  const visited = new Set<any>()
  const visit = (value: any, depth = 0) => {
    if (depth > 10 || !value || typeof value !== 'object' || visited.has(value)) return
    visited.add(value)
    if (Number.isSafeInteger(value.page) && value.page > 0 &&
      (!value.source_sha256 || String(value.source_sha256).toLowerCase() === expectedHash)) {
      const bbox = Array.isArray(value.bbox) && value.bbox.length === 4 && value.bbox.every((n:any) => typeof n === 'number' && n >= 0 && n <= 1) ? value.bbox :
        Array.isArray(value.fragment_bbox) && value.fragment_bbox.length === 4 && value.fragment_bbox.every((n:any) => typeof n === 'number' && n >= 0 && n <= 1) ? value.fragment_bbox : undefined
      if (bbox && bbox[0] < bbox[2] && bbox[1] < bbox[3]) {
        const locator: any = { type: 'pdf', mapping: 'exact', page: value.page, bbox }
        if (typeof value.text === 'string' && value.text) locator.quote = value.text
        if (!found.some(item => item.page === locator.page && JSON.stringify(item.bbox) === JSON.stringify(bbox))) found.push(locator)
      }
    }
    for (const child of Object.values(value)) visit(child, depth + 1)
  }
  visit(data)
  return found
}
function pdfPages(data: any): number[] {
  const pages = new Set<number>()
  const visit = (value: any, depth = 0) => {
    if (depth > 8 || !value || typeof value !== 'object') return
    if (Number.isSafeInteger(value.page) && value.page > 0) pages.add(value.page)
    for (const child of Object.values(value)) if (child && typeof child === 'object') visit(child, depth + 1)
  }
  visit(data)
  return [...pages].sort((a,b)=>a-b)
}
function requestHeaders(kbId: string, scopedTenant?: number | string) {
  const headers = new Headers({ 'X-Knowledge-Base-ID': kbId })
  const tenant = scopedTenant ?? auth.selectedTenantId ?? auth.tenant?.id
  if (tenant != null) headers.set('X-Tenant-ID', String(tenant))
  return headers
}
async function getJSON(url: string, kbId: string, stamp: { token:string; user:string; tenant:string }, scopedTenant?: number | string) {
  const response = await fetchWithAuthRetry(url, { headers: requestHeaders(kbId, scopedTenant) }, {
    token: stamp.token,
    isCurrent: () => (auth.user?.id || '') === stamp.user && String(auth.selectedTenantId ?? auth.tenant?.id ?? '') === stamp.tenant,
  })
  if (!response.ok) {
    if (response.status === 401 || response.status === 403) throw new Error('当前账号或知识库无权读取原始来源。')
    if (response.status === 404) throw new Error('原始来源或对应数据集已不存在。')
    throw new Error(`原件读取失败（HTTP ${response.status}）。`)
  }
  return response.json()
}
function isCurrentOperation(epoch: number, key: string, kbId: string, user?: string, tenant?: string, sourceHash?: string) {
  return operationEpoch === epoch && activeSourceKey.value === key &&
    (user === undefined || (auth.user?.id || '') === user) &&
    (tenant === undefined || String(auth.selectedTenantId ?? auth.tenant?.id ?? '') === tenant) &&
    (props.sources || []).some(item => sourceKey(item) === key && item.context?.knowledge_base_id === kbId &&
      (sourceHash === undefined || String(item.data.source?.sha256 || '').toLowerCase() === sourceHash.toLowerCase()))
}
async function openExcel(item: StructuredAnswerSource, target?: {sheet:string;address:string}) {
  const key = sourceKey(item)
  const epoch = ++operationEpoch
  activeSourceKey.value = key; activeSourceStamp.value = sourceStamp(item); loadError.value = ''; sheetViews.value = []; pdfReadResult.value = null; pdfPreview.value = null; pdfPreviewError.value = ''
  selectedCell.value = target || null
  if (!target) sourceCellPage.value = 0
  if (!item.context) { loadError.value = '缺少知识库范围，无法安全定位。'; return }
  const kbId = item.context.knowledge_base_id
  const sourceHash = String(item.data.source?.sha256 || '')
  const stamp = { token: auth.token, user: auth.user?.id || '', tenant: String(auth.selectedTenantId ?? auth.tenant?.id ?? '') }
  if (!stamp.token || !stamp.user) { loadError.value = '登录状态已失效，请重新登录后重试。'; return }
  busyKey.value = key
  try {
    const datasetId = item.data.dataset_id
    const scopedTenant = item.context.knowledge_base_tenant_id
    const report = await getJSON(`/structured/api/datasets/${encodeURIComponent(datasetId)}/report`, kbId, stamp, scopedTenant)
    if (!isCurrentOperation(epoch, key, kbId, stamp.user, stamp.tenant, sourceHash)) return
    const manifest = report?.manifest
    if (manifest?.dataset_id !== datasetId || typeof manifest.sha256 !== 'string' || manifest.sha256.toLowerCase() !== String(item.data.source?.sha256).toLowerCase()) {
      throw new Error('原件版本 hash 与回答中的证据不一致，已阻止打开旧证据。')
    }
    if (!isStructuredImportId(manifest.import_id)) throw new Error('数据集未返回有效的原件导入标识，无法定位。')
    const locations = sourceCellsFor(item.data).filter(cell => cell.sheet)
    if (!locations.length) throw new Error('成功工具结果未返回可验证的来源单元格坐标。')
    const cell = target || locations[0]
    selectedCell.value = cell
    const parsed = parseExcelCell(cell.address)
    if (!parsed) throw new Error('工具返回的来源坐标无效，无法定位。')
    const startRow = Math.max(1, parsed.row - 2), startColumn = Math.max(1, parsed.column - 1)
    const query = new URLSearchParams({ name: cell.sheet, start_row: String(startRow), start_col: String(startColumn), row_count: '5', col_count: '3' })
    const view = await getJSON(`/structured/api/imports/${encodeURIComponent(manifest.import_id)}/sheet-view?${query}`, kbId, stamp, scopedTenant)
    if (!isCurrentOperation(epoch, key, kbId, stamp.user, stamp.tenant, sourceHash)) return
    const range = view?.range
    const boundedRange = range &&
      Number.isSafeInteger(range.start_row) && Number.isSafeInteger(range.end_row) &&
      Number.isSafeInteger(range.start_col) && Number.isSafeInteger(range.end_col) &&
      range.start_row >= startRow && range.start_col >= startColumn &&
      range.end_row >= range.start_row && range.end_col >= range.start_col &&
      range.end_row <= startRow + 4 && range.end_col <= startColumn + 2 &&
      range.end_row - range.start_row + 1 <= 5 && range.end_col - range.start_col + 1 <= 3 &&
      parsed.row >= range.start_row && parsed.row <= range.end_row &&
      parsed.column >= range.start_col && parsed.column <= range.end_col
    if (view?.sheet_name !== cell.sheet || !Array.isArray(view.cells) || !boundedRange) {
      throw new Error('原件窗口返回结构不完整，无法准确定位。')
    }
    sheetViews.value = [view]
  } catch (error) {
    if (!isCurrentOperation(epoch, key, kbId, stamp.user, stamp.tenant, sourceHash)) return
    loadError.value = (error as Error)?.message || '原件读取失败，请稍后重试。'
  } finally { if (operationEpoch === epoch && busyKey.value === key) busyKey.value = '' }
}
watch(() => [auth.user?.id, auth.selectedTenantId ?? auth.tenant?.id], () => {
  operationEpoch++
  activeSourceKey.value = ''; activeSourceStamp.value = ''; busyKey.value = ''; loadError.value = ''; sheetViews.value = []; pdfReadResult.value = null; pdfPreview.value = null
})
watch(() => (props.sources || []).map(sourceStamp).join('|'), () => {
  if (activeSourceKey.value && !(props.sources || []).some(item => sourceStamp(item) === activeSourceStamp.value)) {
    operationEpoch++; activeSourceKey.value = ''; activeSourceStamp.value = ''; busyKey.value = ''; loadError.value = ''; sheetViews.value = []; pdfReadResult.value = null; pdfPreview.value = null
  }
})
onBeforeUnmount(() => {
  operationEpoch++
  pdfPreview.value = null
})
</script>
<style scoped>
.analysis-evidence { margin: 12px 0; padding: 12px; border: 1px solid #dce5df; border-radius: 8px; line-height: 1.7; }
summary { cursor: pointer; } .analysis-table { overflow-x: auto; }
table { border-collapse: collapse; width: 100%; } th,td { padding: 6px 10px; border: 1px solid #dce5df; text-align: left; white-space: nowrap; }
pre { white-space: pre-wrap; overflow-wrap: anywhere; max-height: 24rem; overflow: auto; }
.source-locator { margin-top: 14px; border-top: 1px solid #dce5df; padding-top: 10px; }
.pdf-source-overlay { position: fixed; inset: 4vh 4vw; z-index: 3000; display: flex; flex-direction: column; padding: 12px; background: white; border: 1px solid #cbd5cf; border-radius: 10px; box-shadow: 0 16px 50px #0004; }
.pdf-source-overlay header { display:flex; justify-content:space-between; gap:12px; align-items:center; padding-bottom:8px; }
.pdf-source-viewer { flex:1; min-height:0; }
.source-card { border: 1px solid #dce5df; border-radius: 8px; padding: 10px; margin: 8px 0; }
.source-card button { margin: 6px 0; padding: 7px 10px; cursor: pointer; }
.blocked { color: #8a4b16; background: #fff8ed; padding: 8px; border-radius: 6px; }
.fine-print { color: #66766c; font-size: .9em; }
.highlighted { background: #ffe59b; outline: 2px solid #c77900; outline-offset: -2px; }
</style>

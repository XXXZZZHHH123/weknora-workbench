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
      <h3>原始来源</h3>
      <article v-for="group in mainSourceGroups" :key="group.key" class="source-row">
        <div class="source-heading">
          <span class="source-kind" aria-hidden="true">{{ group.kind === 'pdf' ? 'PDF' : 'XLS' }}</span>
          <strong class="source-name" :title="sourceName(group.primary)">{{ sourceName(group.primary) }}</strong>
          <span v-if="group.kind === 'pdf' && group.pages.length" class="source-pages">第 {{ group.pages.slice(0, 3).join('、') }} 页<span v-if="group.pages.length > 3">等 {{ group.pages.length }} 页</span></span>
          <button v-if="group.kind === 'pdf' && canOpenPdf(group.primary)" class="source-open" type="button" :disabled="busyKey === sourceKey(group.primary)" @click="openPdf(group.primary)">{{ busyKey === sourceKey(group.primary) ? '正在打开…' : '查看原件' }}</button>
          <button v-else-if="group.kind === 'excel' && group.primary.context" class="source-open" type="button" :disabled="busyKey === sourceKey(group.primary)" @click="openExcel(group.primary)">{{ busyKey === sourceKey(group.primary) ? '正在打开…' : '查看原始表格单元格' }}</button>
        </div>
        <template v-if="group.kind === 'excel'">
          <p v-if="!group.primary.context" class="blocked">来源信息不完整，暂时无法打开原件。</p>
          <div v-if="activeSourceKey === sourceKey(group.primary) && sourceCellsFor(group.primary.data).length" class="source-cell-picker">
            <button v-for="cell in visibleSourceCells(group.primary)" :key="`${cell.sheet}:${cell.address}`" type="button" :aria-pressed="selectedCell?.sheet === cell.sheet && selectedCell?.address === cell.address" @click="openExcel(group.primary, cell)">{{ cell.sheet }}!{{ cell.address }}</button>
            <button v-if="sourceCellPageStart(group.primary) > 0" type="button" @click="changeSourceCellPage(group.primary, -1)">上一组</button>
            <button v-if="sourceCellPageStart(group.primary) + sourceCellPageSize < sourceCellsFor(group.primary.data).length" type="button" @click="changeSourceCellPage(group.primary, 1)">下一组</button>
          </div>
        </template>
        <template v-else>
          <p v-if="!canOpenPdf(group.primary)" class="blocked">来源信息不完整，暂时无法打开原件。</p>
          <details class="pdf-locations">
            <summary>查看来源位置</summary>
            <div v-for="(table, index) in group.tables.filter(table => table.inspected)" :key="table.key" class="pdf-table-location">
              <div class="table-location-heading">
                <span>{{ pdfTableLabel(table, index) }}</span>
                <button v-for="page in table.pages" :key="page" type="button" :disabled="!canOpenPdf(table.primary)" @click="openPdf(table.primary, { page })">第 {{ page }} 页</button>
                <button v-if="!table.pages.length" type="button" :disabled="!canOpenPdf(table.primary)" @click="openPdf(table.primary)">查看位置</button>
              </div>
              <details v-if="pdfTableCells(table).length" class="pdf-cell-locations">
                <summary>单元格位置 · {{ pdfTableCells(table).length }} 处</summary>
                <div class="pdf-cell-list">
                  <button v-for="cell in visiblePdfCells(table)" :key="cell.key" type="button" :disabled="!canOpenPdf(table.primary)" @click="openPdf(table.primary, cell)">
                    <span>{{ pdfCellLabel(cell) }}</span><span class="cell-text">{{ cell.text }}</span>
                  </button>
                </div>
                <div v-if="pdfTableCells(table).length > sourceCellPageSize" class="cell-pagination">
                  <button type="button" :disabled="!pdfCellPages[sourceKey(table.primary)]" @click="changePdfCellPage(table, -1)">上一组</button>
                  <button type="button" :disabled="((pdfCellPages[sourceKey(table.primary)] || 0) + 1) * sourceCellPageSize >= pdfTableCells(table).length" @click="changePdfCellPage(table, 1)">下一组</button>
                </div>
              </details>
            </div>
          </details>
        </template>
        <p v-if="loadError && group.tables.some(table => table.observations.some(item => sourceKey(item) === activeSourceKey))" role="alert" class="blocked">{{ loadError }}</p>
      </article>
      <details v-if="catalogSourceGroups.length" class="catalog-sources">
        <summary>其他检索到的来源 · {{ catalogSourceGroups.length }} 个文件</summary>
        <div v-for="group in catalogSourceGroups" :key="group.key" class="catalog-source">
          <strong>{{ sourceName(group.primary) }}</strong>
          <div class="catalog-pages"><button v-for="(table, index) in group.tables" :key="table.key" type="button" :disabled="!canOpenPdf(table.primary)" @click="openPdf(table.primary)">{{ pdfTableLabel(table, index) }}</button></div>
          <p v-if="loadError && group.tables.some(table => table.observations.some(item => sourceKey(item) === activeSourceKey))" role="alert" class="blocked">{{ loadError }}</p>
        </div>
      </details>
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
    <Teleport to="body">
    <div v-if="pdfPreview" class="pdf-source-overlay" role="dialog" aria-modal="true" :aria-label="pdfPreview.fileName + ' 原始 PDF 来源'">
      <header><strong>{{ pdfPreview.fileName }} · 第 {{ pdfPreview.page }} 页原件</strong><button type="button" @click="closePdf">关闭</button></header>
      <p v-if="pdfPreviewError" role="alert" class="blocked">{{ pdfPreviewError }}</p>
      <DocumentPreview v-else class="pdf-source-viewer" :source-blob="pdfPreview.blob" file-type="pdf" :file-name="pdfPreview.fileName" :active="true" source-mode fill-height :locate="pdfPreview.locate" />
    </div>
    </Teleport>
    <p v-if="loadError && !sources?.length" role="alert" class="blocked">{{ loadError }}</p>
  </section>
</template>
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useAuthStore } from '@/stores/auth'
import { fetchWithAuthRetry } from '@/utils/authFetch'
import DocumentPreview from '@/components/document-preview.vue'
import type { SourceLocateRequest } from '@/utils/sourceLocator'
import { groupStructuredAnswerSources, structuredPdfPages, isStructuredImportId, parseExcelCell, type StructuredAnswerSource, type StructuredSourceTable } from '@/utils/structuredAnswerSources'

type SheetView = { sheet_name: string; cells: Array<{row:number;column:number;ref:string;display?:string;value?:unknown}>; warnings?: string[]; range: Record<string,number> }
type PdfPreview = { blob: Blob; fileName: string; knowledgeId: string; page: number; locate: SourceLocateRequest }
const props = defineProps<{ evidence: any; sources?: StructuredAnswerSource[] }>()
const auth = useAuthStore()
const busyKey = ref(''), activeSourceKey = ref(''), loadError = ref(''), sheetViews = ref<SheetView[]>([])
const pdfPreview = ref<PdfPreview | null>(null), pdfPreviewError = ref('')
const pdfReadResult = ref<any | null>(null), selectedCell = ref<{sheet:string;address:string} | null>(null)
const sourceCellPage = ref(0), sourceCellPageSize = 12
const sourceGroups = computed(() => groupStructuredAnswerSources(props.sources || []))
const mainSourceGroups = computed(() => sourceGroups.value.filter(group => !group.catalogOnly))
const catalogSourceGroups = computed(() => sourceGroups.value.map(group => ({ ...group, tables: group.tables.filter(table => !table.inspected) })).filter(group => group.tables.length))
const pdfCellPages = ref<Record<string, number>>({})
type PdfCellLocation = { key?: string; page: number; row?: number | null; column?: number | null; text?: string; bbox?: number[] }
function sourceName(item: StructuredAnswerSource) { return item.data.source?.file_name || item.data.source?.file || (item.kind === 'pdf' ? 'PDF 原件' : 'Excel 原件') }
function pdfTableLabel(table: StructuredSourceTable, index: number) {
  const observations = [...table.observations].reverse()
  const title = observations.map(item => item.data.title || item.data.fragments?.map((fragment: any) => fragment.caption).filter(Boolean).join(' · ')).find(Boolean)
  return title || (table.pages.length ? `第 ${table.pages.join('、')} 页` : `表格 ${index + 1}`)
}
function pdfTableCells(table: StructuredSourceTable): PdfCellLocation[] {
  const expectedHash = String(table.primary.data.source?.sha256 || '').toLowerCase()
  const data = table.observations.filter(item => item.data.kind === 'structured_source_inspection').map(item => item.data)
  const active = table.observations.some(item => sourceKey(item) === activeSourceKey.value)
  if (active && pdfReadResult.value?.table_id === table.key && String(pdfReadResult.value.source?.sha256 || '').toLowerCase() === expectedHash) data.push(pdfReadResult.value)
  const cells: PdfCellLocation[] = [], indexes = new Map<string, number>()
  for (const observation of data) {
    if (String(observation.source?.sha256 || '').toLowerCase() !== expectedHash) continue
    for (const row of observation.rows || []) for (const values of Object.values(row.source_cells || {})) {
      if (!Array.isArray(values)) continue
      for (const cell of values) {
        if (!Number.isSafeInteger(cell.page) || cell.page < 1 || (cell.source_sha256 && cell.source_sha256.toLowerCase() !== expectedHash)) continue
        const key = JSON.stringify([cell.fragment_id, cell.page, cell.row, cell.column, cell.manual_row_id || ''])
        const location = { ...cell, key, text: cell.correction?.text ?? cell.text ?? '' }
        const existing = indexes.get(key)
        if (existing === undefined) { indexes.set(key, cells.length); cells.push(location) } else cells[existing] = location
      }
    }
  }
  return cells
}
function visiblePdfCells(table: StructuredSourceTable) { const start = (pdfCellPages.value[sourceKey(table.primary)] || 0) * sourceCellPageSize; return pdfTableCells(table).slice(start, start + sourceCellPageSize) }
function changePdfCellPage(table: StructuredSourceTable, offset: number) { pdfCellPages.value[sourceKey(table.primary)] = Math.max(0, (pdfCellPages.value[sourceKey(table.primary)] || 0) + offset) }
function pdfCellLabel(cell: PdfCellLocation) { return `第 ${cell.page} 页` + (Number.isSafeInteger(cell.row) && Number(cell.row) >= 0 ? ` · 第 ${Number(cell.row) + 1} 行` : '') + (Number.isSafeInteger(cell.column) && Number(cell.column) >= 0 ? ` · 第 ${Number(cell.column) + 1} 列` : '') }

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
    String(item.data.source?.sha256 || '').toLowerCase(), item.data.source?.knowledge_id || '',
    item.data.source?.knowledge_base_id || item.context?.knowledge_base_id || '',
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
async function openPdf(item: StructuredAnswerSource, requested?: PdfCellLocation) {
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
    if (requested && !pdfPages(table).includes(requested.page)) throw new Error('所选位置不属于当前已核验的表格。')
    const verifiedCell = requested && pdfTableCells({ key: table.table_id, primary: item, observations: [item], pages: pdfPages(table), inspected: true }).find(cell => cell.page === requested.page && cell.row === requested.row && cell.column === requested.column)
    const first: any = requested ? (verifiedCell && locators.find(locator => locator.page === verifiedCell.page && JSON.stringify(locator.bbox) === JSON.stringify(verifiedCell.bbox))) ||
      { type: 'pdf', mapping: 'exact', page: requested.page, granularity: 'page', ...(verifiedCell?.text ? { quote: verifiedCell.text } : {}) } : locators[0] || { type: 'pdf', mapping: 'exact', page: pdfPages(table)[0], granularity: 'page' }
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
function pdfPages(data: any): number[] { return structuredPdfPages(data) }

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
  pdfCellPages.value = {}
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
.analysis-evidence { margin: 16px 0; line-height: 1.6; color: inherit; min-width: 0; }
summary { cursor: pointer; color: #66716b; font-size: 13px; }
.analysis-table { overflow-x: auto; }
table { border-collapse: collapse; width: 100%; }
th,td { padding: 6px 10px; border-bottom: 1px solid #e6e9e7; text-align: left; white-space: nowrap; }
pre { white-space: pre-wrap; overflow-wrap: anywhere; max-height: 24rem; overflow: auto; }
.source-locator { margin-top: 16px; padding-top: 12px; border-top: 1px solid #e6e9e7; }
.source-locator h3 { margin: 0 0 4px; font-size: 13px; font-weight: 500; color: #758078; }
.source-row { padding: 10px 0; min-width: 0; }
.source-row + .source-row { border-top: 1px solid #f0f1f0; }
.source-heading { display: flex; align-items: center; gap: 9px; min-width: 0; }
.source-kind { flex-shrink: 0; padding: 2px 5px; border-radius: 4px; background: #f2f3f2; color: #737c76; font-size: 10px; font-weight: 600; letter-spacing: .02em; }
.source-name { font-size: 13px; font-weight: 500; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; min-width: 0; }
.source-pages { color: #758078; font-size: 12px; white-space: nowrap; }
.source-locator button { font: inherit; color: #5b6860; border: 0; background: transparent; cursor: pointer; padding: 4px 6px; border-radius: 4px; }
.source-locator button:hover { background: #f2f4f2; color: #304c3c; }
.source-locator button:focus-visible, summary:focus-visible { outline: 2px solid #7b9c88; outline-offset: 2px; }
.source-locator button:disabled { opacity: .5; cursor: default; }
.source-locator .source-open { margin-left: auto; flex-shrink: 0; font-size: 12px; color: #365c46; }
.pdf-locations { margin: 5px 0 0 38px; }
.pdf-table-location { padding: 7px 0; }
.table-location-heading { display: flex; gap: 8px; align-items: baseline; flex-wrap: wrap; font-size: 13px; }
.table-location-heading > span { color: #4c5750; overflow-wrap: anywhere; }
.table-location-heading button { font-size: 12px; white-space: nowrap; }
.pdf-cell-locations { margin-top: 4px; }
.pdf-cell-list { margin-top: 6px; display: grid; gap: 3px; }
.pdf-cell-list button { display: flex; gap: 14px; text-align: left; font-size: 12px; min-width: 0; }
.pdf-cell-list button > span:first-child { flex-shrink: 0; color: #758078; }
.cell-text { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.cell-pagination, .source-cell-picker, .catalog-pages { display: flex; flex-wrap: wrap; gap: 4px; font-size: 12px; }
.catalog-sources { margin-top: 6px; }
.catalog-source { padding: 8px 0 4px 16px; font-size: 13px; }
.catalog-source strong { font-weight: 500; overflow-wrap: anywhere; }
.original-sheet { margin-top: 10px; }
.pdf-source-overlay { position: fixed; inset: 4vh 4vw; z-index: 3000; display: flex; flex-direction: column; padding: 12px; background: white; border-radius: 10px; box-shadow: 0 16px 50px #0004; }
.pdf-source-overlay header { display: flex; justify-content: space-between; gap: 12px; align-items: center; padding-bottom: 8px; }
.pdf-source-overlay header strong { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 14px; }
.pdf-source-overlay header button { flex-shrink: 0; }
.pdf-source-viewer { flex: 1; min-height: 0; }
.blocked { color: #8a4b16; font-size: 13px; margin: 6px 0; }
.fine-print { color: #66766c; font-size: .9em; }
.highlighted { background: #ffe59b; outline: 2px solid #c77900; outline-offset: -2px; }
@media (max-width: 600px) {
  .source-heading { flex-wrap: wrap; gap: 6px 8px; }
  .source-name { flex: 1; }
  .source-pages { order: 2; flex-basis: calc(100% - 36px); margin-left: 36px; }
  .source-locator .source-open { margin-left: 0; }
  .pdf-locations { margin-left: 0; }
  .pdf-cell-list button { display: block; }
  .pdf-cell-list button > span { display: block; }
  .pdf-source-overlay { inset: 8px; padding: 10px; }
}
</style>

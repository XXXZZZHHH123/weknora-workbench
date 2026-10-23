<script setup lang="ts">
import { computed, nextTick, onUnmounted, ref, shallowRef, watch } from 'vue';
import type { ExcelPreviewPage } from '@/utils/excelPreview';
import type { ExcelPreviewChart, ExcelPreviewChartList } from '@/utils/excelPreviewCharts';
import ExcelSourceChart from './ExcelSourceChart.vue';
import type { SourceLocateRequest } from '@/utils/sourceLocator';
import { sourceQuoteText } from '@/utils/sourceLocator';
import { findTextRange, highlightElements, highlightRanges, buildTextIndex, scrollRectIntoContainer } from '@/utils/sourceLocatorDom';

const props = defineProps<{ blob: Blob; extension: string; fileName: string; locate?: SourceLocateRequest | null }>();
const emit = defineEmits<{ located: [result: { found: boolean; precise: boolean; granularity?: string; reason?: string }] }>();
const sheetNames = shallowRef<string[]>([]);
const selectedSheet = ref('');
const page = shallowRef<ExcelPreviewPage | null>(null);
const viewport = ref<HTMLElement | null>(null);
const busy = ref(false);
const error = ref('');
const sheetFilter = ref('');
const jumpRow = ref<number | string>(1);
const mode = ref<'table' | 'charts'>('table');
const chartList = shallowRef<ExcelPreviewChartList>({ charts: [] });
const chart = shallowRef<ExcelPreviewChart | null>(null);
const chartId = ref(''), chartBusy = ref(false), chartListBusy = ref(false), chartError = ref('');
const matchingSheets = computed(() => {
  const text = sheetFilter.value.trim().toLocaleLowerCase();
  return text ? sheetNames.value.filter(name => name.toLocaleLowerCase().includes(text)) : sheetNames.value;
});
const selectedSheetIndex = computed(() => sheetNames.value.indexOf(selectedSheet.value));
const availableRows = computed(() => page.value ? Math.max(0, Math.min(page.value.previewRows, page.value.totalRows)) : 0);
let worker: Worker | null = null;
let generation = 0, requestId = 0, viewVersion = 0;
let chartVersion = 0, listVersion = 0;
let queuedLocate: number | null = null, locating = false;
let marks: Array<() => void> = [];
const pending = new Map<number, { resolve: (value: any) => void; reject: (error: Error) => void; timer: ReturnType<typeof setTimeout> }>();

function clearMarks() { marks.forEach(clear => clear()); marks = []; }
function dispose(reason = '预览已关闭。') {
  chartVersion++; listVersion++; chart.value = null;
  worker?.terminate(); worker = null;
  for (const item of pending.values()) { clearTimeout(item.timer); item.reject(new Error(reason)); }
  pending.clear(); clearMarks();
}
function request<T>(message: object, transfer: Transferable[] = []): Promise<T> {
  if (!worker) return Promise.reject(new Error('预览已关闭，请重新打开。'));
  const id = ++requestId;
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => { dispose('预览处理超时，已停止解析。请下载原件查看。'); }, 30_000);
    pending.set(id, { resolve, reject, timer });
    worker!.postMessage({ ...message, id }, transfer);
  });
}
async function showPage(sheet: string, row = 0, column = 0): Promise<boolean> {
  const version = ++viewVersion, own = generation;
  const changedSheet = sheet !== selectedSheet.value;
  showTable();
  if (changedSheet) { listVersion++; chartList.value = { charts: [] }; chartId.value = ''; chartError.value = ''; }
  clearMarks(); busy.value = true; error.value = '';
  try {
    const value = await request<ExcelPreviewPage>({ type: 'page', sheet, row, column });
    if (own !== generation || version !== viewVersion) return false;
    page.value = value; selectedSheet.value = sheet; jumpRow.value = value.startRow + 1;
    if (changedSheet) void loadChartList(sheet);
    await nextTick();
    if (viewport.value) { viewport.value.scrollTop = 0; viewport.value.scrollLeft = 0; }
    return true;
  } catch (failure) {
    if (own === generation && version === viewVersion) error.value = (failure as Error).message;
    return false;
  } finally {
    if (own === generation && version === viewVersion) { busy.value = false; void drainLocate(); }
  }
}
async function loadChartList(sheet: string) {
  const own = generation, version = ++listVersion;
  chartListBusy.value = true; chartError.value = '';
  try {
    const value = await request<ExcelPreviewChartList>({ type: 'chart-list', sheet });
    if (own !== generation || version !== listVersion || sheet !== selectedSheet.value) return;
    chartList.value = value; chartId.value = value.charts[0]?.id || '';
  } catch (failure) {
    if (own === generation && version === listVersion) chartError.value = (failure as Error).message;
  } finally { if (own === generation && version === listVersion) chartListBusy.value = false; }
}
function showTable() {
  if (mode.value === 'charts') worker?.postMessage({ type: 'cancel-charts' });
  mode.value = 'table'; chartVersion++; chart.value = null; chartBusy.value = false;
}
async function showChart(id = chartId.value) {
  mode.value = 'charts'; chart.value = null; chartError.value = ''; chartId.value = id;
  const own = generation, version = ++chartVersion, sheet = selectedSheet.value;
  if (!id) return;
  chartBusy.value = true;
  try {
    const value = await request<ExcelPreviewChart>({ type: 'chart', sheet, chartId: id });
    if (own !== generation || version !== chartVersion || sheet !== selectedSheet.value || mode.value !== 'charts') return;
    chart.value = value;
    chartList.value = { ...chartList.value, charts: chartList.value.charts.map(entry => entry.id === id ? { ...entry, title: value.title || entry.title } : entry) };
  } catch (failure) {
    if (own === generation && version === chartVersion) chartError.value = (failure as Error).message;
  } finally { if (own === generation && version === chartVersion) chartBusy.value = false; }
}
async function jumpToRow() {
  const row = Number(jumpRow.value);
  if (!page.value || busy.value || !Number.isSafeInteger(row) || row < 1 || row > availableRows.value) return;
  if (!await showPage(selectedSheet.value, row - 1, page.value.startColumn)) return;
  jumpRow.value = row;
  const target = viewport.value?.querySelector<HTMLElement>(`tr[data-row="${row}"]`);
  if (target && viewport.value) scrollRectIntoContainer(viewport.value, target.getBoundingClientRect());
}
async function drainLocate() {
  if (busy.value || locating || queuedLocate === null || !page.value) return;
  queuedLocate = null; locating = true;
  try { await locateSource(); }
  finally { locating = false; if (queuedLocate !== null) void drainLocate(); }
}
async function locateSource() {
  const current = props.locate, own = generation;
  if (!current || !page.value) return;
  showTable(); await nextTick(); clearMarks();
  if (current.unavailable) { emit('located', { found: false, precise: false, reason: 'unavailable' }); return; }
  const locator = current.locators.find(l => l.type === 'sheet' && l.mapping === 'exact' && !l.partial
    && Number.isSafeInteger(l.row_start) && (l.row_start || 0) > 0 && l.sheet && sheetNames.value.includes(l.sheet));
  if (locator) {
    if (!await showPage(locator.sheet!, (locator.row_start || 1) - 1)) return;
    if (own !== generation || current.token !== props.locate?.token || !page.value) return;
    // The worker owns the accessible range. A future fallback may retain a
    // bounded range, while full worksheets can be cited beyond any old cap.
    if ((locator.row_start || 0) > availableRows.value) {
      emit('located', { found: false, precise: false,
        reason: (locator.row_start || 0) > page.value.totalRows ? 'out_of_range' : 'preview_limit' });
      return;
    }
  }
  if (own !== generation || current.token !== props.locate?.token || !viewport.value) return;
  const box = viewport.value;
  const targets = locator ? [...box.querySelectorAll<HTMLElement>('tbody tr[data-row]')].filter(row => {
    const number = Number(row.dataset.row);
    return number >= (locator.row_start || 1) && number <= (locator.row_end || locator.row_start || 1);
  }) : [];
  if (targets.length) {
    marks.push(highlightElements(targets));
    scrollRectIntoContainer(box, targets[0].getBoundingClientRect());
    emit('located', { found: true, precise: false, granularity: 'block' }); return;
  }
  const index = buildTextIndex(box);
  for (const quote of current.quotes) {
    const range = findTextRange(box, sourceQuoteText(quote), index);
    if (range) { marks.push(highlightRanges([range])); scrollRectIntoContainer(box, range.getBoundingClientRect()); emit('located', { found: true, precise: true }); return; }
  }
  emit('located', { found: false, precise: false });
}
async function open() {
  const own = ++generation;
  dispose(); page.value = null; sheetNames.value = []; selectedSheet.value = ''; sheetFilter.value = '';
  mode.value = 'table'; chartList.value = { charts: [] }; chartId.value = ''; chartBusy.value = false; chartListBusy.value = false; chartError.value = '';
  jumpRow.value = 1; error.value = ''; busy.value = true;
  queuedLocate = props.locate?.token ?? null;
  try {
    if (props.blob.size > 100 * 1024 * 1024) throw new Error('文件超过 100 MB，请下载原件查看。');
    worker = new Worker(new URL('../../utils/excelPreview.worker.ts', import.meta.url), { type: 'module' });
    worker.onmessage = event => {
      const item = pending.get(event.data.id);
      if (!item) return;
      clearTimeout(item.timer); pending.delete(event.data.id);
      if (event.data.error) item.reject(new Error(event.data.error)); else item.resolve(event.data.value);
    };
    worker.onerror = () => { if (own === generation) { error.value = '预览线程启动失败，请重试或下载原件。'; busy.value = false; dispose(); } };
    const data = await props.blob.arrayBuffer();
    if (own !== generation) return;
    const names = await request<string[]>({ type: 'open', data, extension: props.extension }, [data]);
    if (own !== generation) return;
    sheetNames.value = names;
    await showPage(sheetNames.value[0] || '');
  } catch (failure) { if (own === generation) error.value = (failure as Error).message; }
  finally {
    if (own === generation) {
      // showPage may already have started a queued citation read. Only the
      // initial metadata/error path still owns the loading state here.
      if (!page.value) busy.value = false;
      void drainLocate();
    }
  }
}
watch(() => [props.blob, props.extension], open, { immediate: true });
watch(() => props.locate?.token, token => { queuedLocate = token ?? null; void drainLocate(); });
onUnmounted(() => { generation++; queuedLocate = null; dispose(); });
</script>

<template>
  <section class="excel-viewer" :aria-label="fileName" :aria-busy="busy">
    <div class="excel-controls">
      <template v-if="sheetNames.length">
        <div class="excel-sheet-picker">
          <select :value="selectedSheet" :disabled="busy" aria-label="选择工作表" @change="showPage(($event.target as HTMLSelectElement).value)">
            <option v-if="selectedSheet && !matchingSheets.includes(selectedSheet)" :value="selectedSheet">{{ selectedSheet }}</option>
            <option v-for="name in matchingSheets" :key="name" :value="name">{{ name }}</option>
          </select>
          <span class="excel-caption excel-sheet-count" aria-label="工作表数量">{{ selectedSheetIndex >= 0 ? `${selectedSheetIndex + 1} / ${sheetNames.length}` : `共 ${sheetNames.length}` }} 张</span>
        </div>
        <input v-if="sheetNames.length > 10" v-model="sheetFilter" type="search" class="excel-sheet-filter"
          aria-label="筛选工作表" placeholder="筛选工作表" :disabled="busy">
        <span v-if="sheetFilter.trim()" class="excel-caption" role="status">{{ matchingSheets.length }} 个匹配</span>
      </template>
      <span class="excel-caption" title="公式显示原文件保存的结果，预览不重新计算；未保存计算结果时可能为空">原件预览 · 只读</span>
      <span v-if="busy" class="excel-pending" role="status"><span class="excel-spinner" />正在读取…</span>
      <div v-if="page" class="excel-mode" role="group" aria-label="原件预览视图">
        <button :aria-pressed="mode === 'table'" @click="showTable">表格</button>
        <button :aria-pressed="mode === 'charts'" :disabled="busy || chartListBusy" @click="showChart()">图表{{ chartListBusy ? '…' : ` (${chartList.charts.length})` }}</button>
      </div>
      <template v-if="page && mode === 'table'">
        <div class="excel-pager excel-row-pager">
          <button aria-label="上一页" :disabled="busy || page.startRow === 0" @click="showPage(selectedSheet, page.startRow - 100, page.startColumn)">‹</button>
          <span>{{ page.startRow + 1 }}–{{ page.startRow + page.rows.length }} / {{ page.totalRows }} 行</span>
          <button aria-label="下一页" :disabled="busy || page.startRow + 100 >= availableRows" @click="showPage(selectedSheet, page.startRow + 100, page.startColumn)">›</button>
        </div>
        <form class="excel-row-jump" @submit.prevent="jumpToRow">
          <label>跳到 <input v-model="jumpRow" type="number" required min="1" :max="availableRows" step="1"
            aria-label="跳转到行" :disabled="busy || !availableRows"> 行</label>
          <button type="submit" :disabled="busy || !availableRows">跳转</button>
        </form>
        <div v-if="page.totalColumns > 30" class="excel-pager">
          <button aria-label="前一组列" :disabled="busy || page.startColumn === 0" @click="showPage(selectedSheet, page.startRow, page.startColumn - 30)">‹</button>
          <span>{{ page.columns[0] }}–{{ page.columns.at(-1) }}</span>
          <button aria-label="后一组列" :disabled="busy || page.startColumn + 30 >= page.totalColumns" @click="showPage(selectedSheet, page.startRow, page.startColumn + 30)">›</button>
        </div>
      </template>
    </div>
    <p v-if="error" class="excel-error" role="alert">{{ error }} <button @click="open">重试</button></p>
    <p v-if="page?.truncated" class="excel-note">当前可预览前 {{ page.previewRows.toLocaleString('zh-CN') }} / {{ page.totalRows.toLocaleString('zh-CN') }} 行；完整内容请下载原件。预览不计算公式，显示文件保存的值。</p>
    <template v-if="mode === 'charts' && !error">
      <div v-if="chartList.charts.length" class="excel-controls excel-chart-picker">
        <label>图表 <select :value="chartId" aria-label="选择原件图表" @change="showChart(($event.target as HTMLSelectElement).value)"><option v-for="(entry, index) in chartList.charts" :key="entry.id" :value="entry.id">{{ index + 1 }} · {{ entry.title }}</option></select></label>
        <span class="excel-caption">{{ chartList.charts.findIndex(item => item.id === chartId) + 1 }} / {{ chartList.charts.length }} 张</span>
        <span v-if="chartBusy" class="excel-pending" role="status"><span class="excel-spinner" />正在读取图表…</span>
      </div>
      <p v-if="chartError" class="excel-error" role="alert">{{ chartError }} <button @click="open">重新打开</button></p>
      <p v-if="!chartListBusy && !chartList.charts.length && !chartError" class="excel-note">当前工作表没有可读取的原生图表。</p>
      <p v-for="notice in chartList.notices || []" :key="notice" class="excel-note">{{ notice }}</p>
      <ExcelSourceChart v-if="chart" :key="chart.id" :chart="chart" />
    </template>
    <div v-if="page && !error && mode === 'table'" ref="viewport" class="excel-grid" tabindex="0" :aria-label="`${selectedSheet}工作表`">
      <table>
        <thead><tr><th aria-label="行号" /><th v-for="column in page.columns" :key="column" scope="col">{{ column }}</th></tr></thead>
        <tbody><tr v-for="row in page.rows" :key="row.number" :data-row="row.number">
          <th scope="row">{{ row.number }}</th>
          <template v-for="(cell, index) in row.cells" :key="index">
            <td v-if="!cell.hidden" :rowspan="cell.rowSpan" :colspan="cell.colSpan" :title="cell.text">{{ cell.text }}</td>
          </template>
        </tr></tbody>
      </table>
    </div>
  </section>
</template>

<style scoped>
.excel-viewer { display: flex; flex-direction: column; min-height: 0; height: 100%; font-family: inherit; color: var(--td-text-color-primary); }
.excel-controls { display: flex; align-items: center; flex-wrap: wrap; gap: 8px 12px; padding: 8px 12px; border-bottom: 1px solid var(--td-component-stroke); font-size: var(--app-text-sm, 12px); }
.excel-controls select { max-width: min(45vw, 280px); height: 30px; border: 1px solid var(--td-component-stroke); border-radius: 6px; padding: 0 6px; font: inherit; color: inherit; background: var(--td-bg-color-container); }
.excel-sheet-picker { display: inline-flex; align-items: center; gap: 6px; min-width: 0; }
.excel-sheet-count { white-space: nowrap; font-variant-numeric: tabular-nums; }
.excel-sheet-filter { width: 112px; min-width: 0; }
.excel-row-jump { display: inline-flex; align-items: center; gap: 5px; }
.excel-row-jump label { display: inline-flex; align-items: center; gap: 4px; white-space: nowrap; }
.excel-row-jump input { width: 76px; font-variant-numeric: tabular-nums; }
.excel-sheet-filter, .excel-row-jump input { height: 26px; border: 1px solid var(--td-component-stroke); border-radius: 5px; padding: 0 6px; font: inherit; color: inherit; background: var(--td-bg-color-container); }
.excel-sheet-filter:focus-visible, .excel-row-jump input:focus-visible { outline: 2px solid var(--td-brand-color); outline-offset: 1px; }
.excel-caption, .excel-note { color: var(--td-text-color-secondary); }
.excel-pager { display: flex; align-items: center; gap: 6px; }
.excel-row-pager { margin-left: auto; }
.excel-mode { display: inline-flex; gap: 3px; }
.excel-mode button[aria-pressed="true"] { color: var(--td-brand-color); border-color: var(--td-brand-color); }
.excel-chart-picker label { display: inline-flex; align-items: center; gap: 8px; }
.excel-chart-picker select { max-width: min(64vw, 520px); }
button { font: inherit; color: inherit; cursor: pointer; background: var(--td-bg-color-container); border: 1px solid var(--td-component-stroke); border-radius: 5px; min-width: 26px; height: 26px; }
button:disabled { opacity: .4; cursor: default; }
.excel-pending { display: inline-flex; align-items: center; gap: 5px; }
.excel-spinner { width: 10px; height: 10px; border: 2px solid var(--td-component-stroke); border-top-color: var(--td-brand-color); border-radius: 50%; animation: excel-spin .8s linear infinite; }
@keyframes excel-spin { to { transform: rotate(360deg); } }
@media (prefers-reduced-motion: reduce) { .excel-spinner { animation: none; } }
.excel-note, .excel-error { margin: 0; padding: 8px 12px; font-size: var(--app-text-sm, 12px); line-height: 1.5; }
.excel-error { color: var(--td-error-color); }
.excel-grid { overflow: auto; min-height: 0; max-height: 70vh; flex: 1; }
table { border-collapse: separate; border-spacing: 0; font-size: var(--app-text-sm, 12px); width: max-content; min-width: 100%; }
th, td { border-right: 1px solid var(--td-component-stroke); border-bottom: 1px solid var(--td-component-stroke); padding: 6px 10px; max-width: 320px; min-width: 72px; white-space: pre-wrap; overflow-wrap: anywhere; vertical-align: top; line-height: 1.5; }
td { background: var(--td-bg-color-container); }
th { background: var(--td-bg-color-secondarycontainer); font-weight: 500; position: sticky; }
thead th { top: 0; z-index: 2; }
tbody th { left: 0; z-index: 1; min-width: 40px; text-align: right; }
thead th:first-child { left: 0; z-index: 3; min-width: 40px; }
</style>

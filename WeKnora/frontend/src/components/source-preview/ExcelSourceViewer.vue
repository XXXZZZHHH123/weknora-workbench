<script setup lang="ts">
import { nextTick, onUnmounted, ref, shallowRef, watch } from 'vue';
import type { ExcelPreviewPage } from '@/utils/excelPreview';
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
let worker: Worker | null = null;
let generation = 0, requestId = 0, viewVersion = 0;
let queuedLocate: number | null = null, locating = false;
let marks: Array<() => void> = [];
const pending = new Map<number, { resolve: (value: any) => void; reject: (error: Error) => void; timer: ReturnType<typeof setTimeout> }>();

function clearMarks() { marks.forEach(clear => clear()); marks = []; }
function dispose(reason = '预览已关闭。') {
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
  clearMarks(); busy.value = true; error.value = '';
  try {
    const value = await request<ExcelPreviewPage>({ type: 'page', sheet, row, column });
    if (own !== generation || version !== viewVersion) return false;
    page.value = value; selectedSheet.value = sheet;
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
async function drainLocate() {
  if (busy.value || locating || queuedLocate === null || !page.value) return;
  queuedLocate = null; locating = true;
  try { await locateSource(); }
  finally { locating = false; if (queuedLocate !== null) void drainLocate(); }
}
async function locateSource() {
  const current = props.locate, own = generation;
  if (!current || !page.value) return;
  clearMarks();
  if (current.unavailable) { emit('located', { found: false, precise: false, reason: 'unavailable' }); return; }
  const locator = current.locators.find(l => l.type === 'sheet' && l.mapping === 'exact' && !l.partial && l.row_start && l.sheet && sheetNames.value.includes(l.sheet));
  if (locator) {
    // A bounded preview cannot claim to have located rows beyond its limit.
    if ((locator.row_start || 0) > 5_000) { emit('located', { found: false, precise: false, reason: 'preview_limit' }); return; }
    if (!await showPage(locator.sheet!, (locator.row_start || 1) - 1)) return;
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
  dispose(); page.value = null; sheetNames.value = []; error.value = ''; busy.value = true;
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
  finally { if (own === generation) { busy.value = false; void drainLocate(); } }
}
watch(() => [props.blob, props.extension], open, { immediate: true });
watch(() => props.locate?.token, token => { queuedLocate = token ?? null; void drainLocate(); });
onUnmounted(() => { generation++; queuedLocate = null; dispose(); });
</script>

<template>
  <section class="excel-viewer" :aria-label="fileName" :aria-busy="busy">
    <div class="excel-controls">
      <select v-if="sheetNames.length" :value="selectedSheet" :disabled="busy" aria-label="选择工作表" @change="showPage(($event.target as HTMLSelectElement).value)">
        <option v-for="name in sheetNames" :key="name" :value="name">{{ name }}</option>
      </select>
      <span class="excel-caption">原件预览 · 只读</span>
      <span v-if="busy" class="excel-pending" role="status"><span class="excel-spinner" />正在读取…</span>
      <template v-if="page">
        <div class="excel-pager">
          <button aria-label="上一页" :disabled="busy || page.startRow === 0" @click="showPage(selectedSheet, page.startRow - 100, page.startColumn)">‹</button>
          <span>{{ page.startRow + 1 }}–{{ page.startRow + page.rows.length }} / {{ page.totalRows }} 行</span>
          <button aria-label="下一页" :disabled="busy || page.startRow + 100 >= page.previewRows" @click="showPage(selectedSheet, page.startRow + 100, page.startColumn)">›</button>
        </div>
        <div v-if="page.totalColumns > 30" class="excel-pager">
          <button aria-label="前一组列" :disabled="busy || page.startColumn === 0" @click="showPage(selectedSheet, page.startRow, page.startColumn - 30)">‹</button>
          <span>{{ page.columns[0] }}–{{ page.columns.at(-1) }}</span>
          <button aria-label="后一组列" :disabled="busy || page.startColumn + 30 >= page.totalColumns" @click="showPage(selectedSheet, page.startRow, page.startColumn + 30)">›</button>
        </div>
      </template>
    </div>
    <p v-if="error" class="excel-error" role="alert">{{ error }} <button @click="open">重试</button></p>
    <p v-if="page?.truncated" class="excel-note">为保持流畅，仅预览当前工作表前 5,000 行；完整内容请下载原件。预览不计算公式，显示文件保存的值。</p>
    <div v-if="page && !error" ref="viewport" class="excel-grid" tabindex="0" :aria-label="`${selectedSheet}工作表`">
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
.excel-caption, .excel-note { color: var(--td-text-color-secondary); }
.excel-pager { display: flex; align-items: center; gap: 6px; }
.excel-pager:first-of-type { margin-left: auto; }
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

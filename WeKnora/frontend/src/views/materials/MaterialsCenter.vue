<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { MessagePlugin } from 'tdesign-vue-next'
import { FileAddIcon, FolderOpenIcon, FileExcelIcon, FileIcon, SearchIcon, DownloadIcon, RefreshIcon, LinkIcon, ChevronRightIcon } from 'tdesign-icons-vue-next'
import { useAuthStore } from '@/stores/auth'
import { useUploadConfirmStore } from '@/stores/uploadConfirm'
import { createKnowledgeFromURL, getKnowledgeBaseById } from '@/api/knowledge-base'
import { createMaterialClient, type LibraryAsset, type LibraryOperation, type MaterialScope } from './client'
import { fileKind, isTable, isStructuredFile, purposeNames, readableSize, stateName, takeIntake } from './intake'

const auth = useAuthStore(), route = useRoute(), router = useRouter(), confirmStore = useUploadConfirmStore()
const scope = (): MaterialScope => ({ token: auth.token, userId: auth.user?.id || '', tenantId: String(auth.effectiveTenantId || '') })
const identity = () => ({ userId: scope().userId, tenantId: scope().tenantId })
let client = createMaterialClient(scope(), scope), generation = 0, listingRequest = 0, legacyRequest = 0, detailRequest = 0, timer: ReturnType<typeof setTimeout> | undefined, searchTimer: ReturnType<typeof setTimeout> | undefined
let optionsOpen = false
const assets = ref<LibraryAsset[]>([]), total = ref(0), offset = ref(0), nextOffset = ref<number | null>(null)
const loading = ref(false), error = ref(''), search = ref(''), kindFilter = ref('all'), sourceFilter = ref('manual')
const libraries = ref<any[]>([]), legacyDatasets = ref<any[]>([]), legacyDocuments = ref<any[]>([]), indicators = ref<any[]>([])
const legacyErrors = ref<Record<string, string>>({})
const uploadOpen = ref(false), uploadMode = ref('file'), fileInput = ref<HTMLInputElement>(), folderInput = ref<HTMLInputElement>()
const pendingFiles = ref<File[]>([]), pickedKb = ref(''), folder = ref(''), tags = ref<string[]>([]), processConfig = ref<any>({}), url = ref('')
const includeStructured = ref(true), includeKnowledge = ref(false), includeResearch = ref(false), uploading = ref(false), batchStatus = ref('')
const selected = ref<LibraryAsset>(), operations = ref<LibraryOperation[]>([]), detailOpen = ref(false), actionBusy = ref(false)
const reviewOpen = ref(false), reviewFrame = ref<HTMLIFrameElement>(), reviewImport = ref('')
const mappingOperation = ref<LibraryOperation>(), mappings = ref<Record<string, {series_id: string; source_unit: string; factor: number}>>({})
const selectedRows = ref<string[]>([])
const mappingNote = ref('已核对原文件、指标和单位'), registerNew = ref(false), bridgeSeries = ref<any[]>([]), pickedSeries = ref('')
const bridgeTarget = ref(''), bridgeUnit = ref(''), bridgeFactor = ref(1), allowConflicts = ref(false), loadingSeries = ref(false)
const automaticRouting = ref(false)
const datasetOf = computed(() => operations.value.find(o => o.purpose === 'structured')?.remote_ids?.dataset_id || operations.value.find(o => o.purpose === 'structured')?.result?.dataset_id)
const visibleAssets = computed(() => assets.value.filter(a => (kindFilter.value === 'all' || (isTable(a.filename) ? 'table' : 'document') === kindFilter.value) && a.filename.toLowerCase().includes(search.value.toLowerCase())))
const canWriteFacts = computed(() => auth.currentTenantRole !== 'viewer')
const allTables = computed(() => pendingFiles.value.length > 0 && pendingFiles.value.every(f => isTable(f.name)))
const mixedFiles = computed(() => pendingFiles.value.some(f => isTable(f.name)) && pendingFiles.value.some(f => !isTable(f.name)))
const hasStructured = computed(() => pendingFiles.value.some(f => isStructuredFile(f.name)))
const pageReady = computed(() => visibleAssets.value.filter(a => a.operations?.some(o => o.status === 'completed')).length)
const pagePending = computed(() => visibleAssets.value.filter(a => a.operations?.some(o => ['needs_mapping', 'failed', 'interrupted'].includes(o.status))).length)
const preview = computed(() => mappingOperation.value?.result?.preview || mappingOperation.value?.result || {})
const operationActive = (o: LibraryOperation) => ['pending', 'running', 'processing'].includes(o.status)
const dateLabel = (value: string) => new Date(value).toLocaleDateString('zh-CN', { month: '2-digit', day: '2-digit' })
const statusClass = (status: string) => ['failed', 'interrupted'].includes(status) ? 'warning' : status === 'needs_mapping' ? 'pending' : status === 'completed' ? 'ready' : ''
function notify(e: unknown) { const message = e instanceof Error ? e.message : '操作未完成，请重试'; error.value = message; MessagePlugin.error(message) }
function stopPolling() { if (timer) clearTimeout(timer); timer = undefined }

async function refresh() {
  const ownGeneration = generation
  const ownRequest = ++listingRequest
  loading.value = true; error.value = ''
  try {
    const data = await client.get(`/api/library?source=${sourceFilter.value}&kind=${kindFilter.value}&search=${encodeURIComponent(search.value)}&offset=${offset.value}&limit=25`)
    if (ownGeneration !== generation || ownRequest !== listingRequest) return
    assets.value = data.assets; total.value = data.total; nextOffset.value = data.next_offset ?? null
    if (selected.value) await openDetail(selected.value, false)
  } catch (e) { if (ownGeneration === generation && ownRequest === listingRequest) notify(e) }
  finally { if (ownGeneration === generation && ownRequest === listingRequest) loading.value = false }
}
async function legacy(kbId = '') {
  const ownGeneration = generation, ownRequest = ++legacyRequest
  try {
    const data = await client.get('/api/library/legacy' + (kbId ? '?kb_id=' + encodeURIComponent(kbId) : ''))
    if (ownGeneration !== generation || ownRequest !== legacyRequest) return
    libraries.value = data.libraries || data.knowledge_bases || []
    legacyDatasets.value = data.datasets || []
    legacyDocuments.value = data.documents || []
    legacyErrors.value = data.errors || {}
    if (!pickedKb.value && data.default_kb_id) pickedKb.value = data.default_kb_id
  } catch (e) { if (ownGeneration === generation && ownRequest === legacyRequest) notify(e) }
}
async function openDetail(asset: LibraryAsset, reveal = true) {
  const ownGeneration = generation, ownRequest = ++detailRequest
  const data = await client.get('/api/library/' + encodeURIComponent(asset.id))
  if (ownGeneration !== generation || ownRequest !== detailRequest) return
  selected.value = data.asset; operations.value = data.operations || []
  assets.value = assets.value.map(a => a.id === data.asset.id ? data.asset : a)
  if (reveal) { detailOpen.value = true; mappingOperation.value = undefined; bridgeSeries.value = []; pickedSeries.value = '' }
  stopPolling()
  if (detailOpen.value && operations.value.some(operationActive)) timer = setTimeout(() => { if (generation === ownGeneration && selected.value) void openDetail(selected.value, false).catch(notify) }, 2500)
}
function chooseFiles(files: File[]) {
  if (files.some(f => f.size > 100 * 1024 * 1024)) { MessagePlugin.warning('单个文件不能超过 100 MB。'); return }
  pendingFiles.value = files; uploadOpen.value = true; uploadMode.value = 'file'; batchStatus.value = ''
  const table = files.length > 0 && files.every(f => isTable(f.name))
  includeStructured.value = table && files.every(f => isStructuredFile(f.name)) && canWriteFacts.value
  includeKnowledge.value = !table || !canWriteFacts.value || !!route.query.kb_id
  automaticRouting.value = mixedFiles.value
  includeResearch.value = table && canWriteFacts.value && (route.query.purpose === 'research' || !includeStructured.value && !includeKnowledge.value)
}
function inputFiles(event: Event) { const input = event.target as HTMLInputElement; chooseFiles(Array.from(input.files || [])); input.value = '' }
function dropFiles(event: DragEvent) { if (uploading.value) return; chooseFiles(Array.from(event.dataTransfer?.files || [])) }
async function addFiles() { uploadOpen.value = true; uploadMode.value = 'file'; batchStatus.value = ''; mappingOperation.value = undefined; await nextTick(); fileInput.value?.click() }
async function knowledgeOptions() {
  if (!pickedKb.value) { MessagePlugin.warning('请先选择目标知识库。'); return }
  const ownGeneration = generation, kbId = pickedKb.value
  let data: any
  try { data = await getKnowledgeBaseById(kbId) }
  catch (e) { if (ownGeneration === generation) notify(e); return }
  if (ownGeneration !== generation || pickedKb.value !== kbId) return
  try {
    if (ownGeneration !== generation) return
    optionsOpen = true
    const result = await confirmStore.open({ mode: uploadMode.value === 'url' ? 'url' : 'file', kbInfo: data.data || data,
      files: pendingFiles.value, urls: url.value ? [url.value] : [], tagIds: tags.value, targetFolder: folder.value })
    if (ownGeneration !== generation) return
    processConfig.value = result.processConfig; tags.value = result.tagIds || []; folder.value = result.targetFolder || ''
    if (result.files?.length) pendingFiles.value = result.files
    if (result.urls?.length) url.value = result.urls[0]
  } catch { /* Closing the existing options dialog leaves the intake unchanged. */ }
  finally { optionsOpen = false }
}
async function process(asset: LibraryAsset, body: Record<string, any>) {
  const result = await client.post('/api/library/' + encodeURIComponent(asset.id) + '/process', body)
  return result.operation as LibraryOperation
}
function purposesFor(file: File) {
  if (automaticRouting.value && mixedFiles.value) return !isTable(file.name) || !canWriteFacts.value ? ['knowledge'] : isStructuredFile(file.name) ? ['structured'] : ['research']
  return [includeStructured.value && isStructuredFile(file.name) && 'structured', includeKnowledge.value && 'knowledge', includeResearch.value && 'research'].filter(Boolean) as string[]
}
async function waitForOperation(operation: LibraryOperation, expectedGeneration: number) {
  for (let n = 0; n < 180 && operationActive(operation); n++) {
    await new Promise(resolve => setTimeout(resolve, 1500))
    if (expectedGeneration !== generation) throw new Error('空间已切换，后续处理已停止。')
    const data = await client.get('/api/library/operations/' + encodeURIComponent(operation.id)); operation = data.operation || data
  }
  if (operationActive(operation)) throw new Error('文件仍在处理中，请在资料详情查看结果后继续。原件已经保留。')
  return operation
}
async function importFiles() {
  if (uploading.value) return
  if (includeKnowledge.value && !pickedKb.value) { MessagePlugin.warning('请为正文选择目标知识库。'); return }
  if (!includeStructured.value && !includeKnowledge.value && !includeResearch.value) { MessagePlugin.warning('请至少选择一种用途。'); return }
  const ownGeneration = generation, files = [...pendingFiles.value]
  uploading.value = true; error.value = ''; batchStatus.value = ''
  try {
    for (let i = 0; i < files.length; i++) {
      if (generation !== ownGeneration) break
      batchStatus.value = `正在保存 ${i + 1}/${files.length}：${files[i].name}`
      const saved = await client.upload(files[i]), asset: LibraryAsset = saved.asset
      await openDetail(asset, false)
      const purposes = purposesFor(files[i])
      for (const purpose of purposes) {
        if (generation !== ownGeneration) break
        batchStatus.value = `${files[i].name} · ${purposeNames[purpose]}`
        const relativeFolder = files[i].webkitRelativePath?.split('/').slice(0, -1).join('/') || ''
        const destinationFolder = [folder.value, relativeFolder].filter(Boolean).join('/')
        const op = await process(asset, { purpose, ...(purpose === 'knowledge' ? { kb_id: pickedKb.value, folder: destinationFolder, tag_ids: tags.value, process_config: processConfig.value } : {}) })
        if (purpose === 'structured') await waitForOperation(op, ownGeneration)
      }
    }
    if (ownGeneration === generation) { batchStatus.value = '原件已保存，处理状态可在资料详情查看。'; pendingFiles.value = []; uploadOpen.value = false; detailOpen.value = true; await refresh() }
  } catch (e) { if (ownGeneration === generation) notify(e) }
  finally { if (ownGeneration === generation) uploading.value = false }
}
async function importUrl() {
  if (!pickedKb.value || !url.value.trim()) { MessagePlugin.warning('请选择知识库并填写网页地址。'); return }
  const ownGeneration = generation
  uploading.value = true
  try { await createKnowledgeFromURL(pickedKb.value, { url: url.value.trim(), tag_ids: tags.value, process_config: processConfig.value }); if (ownGeneration !== generation) return; MessagePlugin.success('网页已交给知识库采集，解析状态可在历史资料中查看。'); uploadOpen.value = false; await legacy(pickedKb.value) }
  catch (e) { if (ownGeneration === generation) notify(e) }
  finally { if (ownGeneration === generation) uploading.value = false }
}
async function download() {
  if (!selected.value) return
  try { const blob = await client.download(selected.value.id); const address = URL.createObjectURL(blob); const a = document.createElement('a'); a.href = address; a.download = selected.value.filename; a.click(); setTimeout(() => URL.revokeObjectURL(address), 1000) }
  catch (e) { notify(e) }
}
async function retry(op: LibraryOperation) {
  actionBusy.value = true
  try { await client.post('/api/library/operations/' + encodeURIComponent(op.id) + '/retry', {}); if (selected.value) await openDetail(selected.value, false) }
  catch (e) { notify(e) }
  finally { actionBusy.value = false }
}
async function appendPurpose(purpose: string) {
  if (!selected.value) return
  actionBusy.value = true
  try { await process(selected.value, { purpose, ...(purpose === 'knowledge' ? { kb_id: pickedKb.value, folder: folder.value, tag_ids: tags.value, process_config: processConfig.value } : {}) }); await openDetail(selected.value, false) }
  catch (e) { notify(e) }
  finally { actionBusy.value = false }
}
async function showMapping(op: LibraryOperation) {
  const originalPreview = op.result?.preview || op.result || {}
  const nextMappings: Record<string, {series_id: string; source_unit: string; factor: number}> = {}
  for (const col of originalPreview.columns || []) nextMappings[col.col_key] = { series_id: col.series_id || '', source_unit: col.source_unit || col.unit || '', factor: col.factor || 1 }
  mappings.value = nextMappings; registerNew.value = false
  selectedRows.value = (originalPreview.rows || []).filter((r: any) => ['ready', 'error'].includes(r.category)).map((r: any) => r.row_key)
  const known = originalPreview.known_mapping || {}
  bridgeTarget.value = known.research_series_id || ''; bridgeUnit.value = known.source_unit || originalPreview.series?.unit || ''; bridgeFactor.value = known.factor || 1; allowConflicts.value = false
  mappingOperation.value = op
  const ownGeneration = generation
  const data = await client.get('/api/library/indicators')
  if (ownGeneration !== generation || mappingOperation.value?.id !== op.id) return
  indicators.value = (data.indicators || data).filter((i: any) => i.fetch_mode === 'manual')
}
async function loadBridgeSeries() {
  if (!datasetOf.value) return
  const ownGeneration = generation, dataset = datasetOf.value, assetId = selected.value?.id
  loadingSeries.value = true; bridgeSeries.value = []
  try {
    let cursor = 0
    for (;;) {
      const data = await client.post('/api/library/catalog', { operation: 'list_series', arguments: { dataset_id: dataset, limit: 100, offset: cursor } })
      if (ownGeneration !== generation || selected.value?.id !== assetId || datasetOf.value !== dataset) return
      bridgeSeries.value.push(...(data.series || []))
      if (data.next_offset == null) { if (data.truncated) throw new Error('字段目录不完整，请重试。'); break }
      if (data.next_offset <= cursor) throw new Error('字段目录分页异常。')
      cursor = data.next_offset
    }
  } catch (e) { if (ownGeneration === generation && selected.value?.id === assetId) notify(e) }
  finally { if (ownGeneration === generation && selected.value?.id === assetId) loadingSeries.value = false }
}
async function previewBridge() {
  if (!selected.value || !datasetOf.value || !pickedSeries.value) return
  const ownGeneration = generation, asset = selected.value, dataset = datasetOf.value, field = pickedSeries.value
  actionBusy.value = true
  try {
    let op = await process(asset, { purpose: 'research', dataset_id: dataset, series_id: field })
    op = await waitForOperation(op, ownGeneration)
    if (ownGeneration !== generation || selected.value?.id !== asset.id) return
    await openDetail(asset, false)
    if (op.status === 'needs_mapping') await showMapping(op)
    else if (op.status === 'failed') throw new Error(op.error || '字段接入失败')
    else MessagePlugin.success('已按确认格式接入研究。')
  } catch (e) { notify(e) }
  finally { actionBusy.value = false }
}
async function commitMapping() {
  if (!selected.value || !mappingOperation.value) return
  actionBusy.value = true
  try {
    const p = preview.value
    await process(selected.value, { purpose: 'research', operation_id: mappingOperation.value.id, confirm: true, ...(p.mode === 'bridge' ? {
      dataset_id: mappingOperation.value.request?.dataset_id,
      series_id: mappingOperation.value.request?.series_id,
      mapping: { research_series_id: bridgeTarget.value, source_unit: bridgeUnit.value, target_unit: indicators.value.find(i => i.series_id === bridgeTarget.value)?.unit, factor: bridgeFactor.value, note: mappingNote.value },
      allow_conflicts: allowConflicts.value,
    } : p.mode === 'vendor' ? { register_new: registerNew.value } : {
      selected_row_keys: selectedRows.value, column_overrides: mappings.value,
    }), note: mappingNote.value })
    mappingOperation.value = undefined; await openDetail(selected.value, false); await refresh()
  } catch (e) { notify(e) }
  finally { actionBusy.value = false }
}
function openReview(op: LibraryOperation) { reviewImport.value = op.remote_ids?.import_id || op.result?.import_id || ''; reviewOpen.value = true }
function authReview() { reviewFrame.value?.contentWindow?.postMessage({ type: 'structured-auth', token: auth.token, tenant: auth.effectiveTenantId }, window.location.origin) }
async function closeReview() { reviewOpen.value = false; if (selected.value) await openDetail(selected.value, false); await refresh() }
async function page(next: boolean) { offset.value = next ? nextOffset.value || offset.value : Math.max(0, offset.value - 25); await refresh() }
async function reset() {
  if (optionsOpen) confirmStore.rejectConfirm()
  generation++; legacyRequest++; detailRequest++; stopPolling(); if (searchTimer) clearTimeout(searchTimer); searchTimer = undefined; client = createMaterialClient(scope(), scope)
  assets.value = []; libraries.value = []; legacyDatasets.value = []; legacyDocuments.value = []; selected.value = undefined; operations.value = []
  search.value = ''; kindFilter.value = 'all'; sourceFilter.value = 'manual'; error.value = ''; batchStatus.value = ''; legacyErrors.value = {};
  selectedRows.value = []; mappings.value = {}; indicators.value = []; bridgeSeries.value = []; pickedSeries.value = ''; bridgeTarget.value = ''; bridgeUnit.value = ''; bridgeFactor.value = 1; allowConflicts.value = false; registerNew.value = false; loadingSeries.value = false; actionBusy.value = false; automaticRouting.value = false; includeStructured.value = canWriteFacts.value; includeKnowledge.value = false;
  pendingFiles.value = []; detailOpen.value = false; reviewOpen.value = false; mappingOperation.value = undefined; uploading.value = false; offset.value = 0
  pickedKb.value = typeof route.query.kb_id === 'string' ? route.query.kb_id : ''; folder.value = typeof route.query.folder === 'string' ? route.query.folder : ''
  tags.value = []; processConfig.value = {}; url.value = ''; includeResearch.value = false; uploadOpen.value = !!route.query.kb_id || route.query.add === '1'; uploadMode.value = route.query.mode === 'url' ? 'url' : 'file'
  const transfer = takeIntake(identity())
  if (transfer) { pickedKb.value = transfer.context.kbId || pickedKb.value; folder.value = transfer.context.folder || folder.value; tags.value = transfer.context.tagIds || []; url.value = transfer.context.url || ''; if (transfer.files.length) chooseFiles(transfer.files); if (url.value) { uploadOpen.value = true; uploadMode.value = 'url' } }
  if (!auth.token || !auth.effectiveTenantId) return
  await Promise.all([refresh(), legacy(pickedKb.value)])
  if (typeof route.query.source === 'string' && /^[a-f0-9]{64}$/.test(route.query.source)) {
    try { await openDetail({ id: route.query.source } as LibraryAsset) } catch (e) { notify(e) }
  }
}
watch(() => [auth.token, auth.effectiveTenantId, auth.user?.id], reset, { immediate: true, flush: 'sync' })
watch(() => route.fullPath, reset)
watch(sourceFilter, () => { offset.value = 0; void refresh() })
watch(kindFilter, () => { offset.value = 0; void refresh() })
watch(search, () => { if (searchTimer) clearTimeout(searchTimer); searchTimer = setTimeout(() => { offset.value = 0; void refresh() }, 300) })
function receiveReview(event: MessageEvent) {
  if (event.origin !== window.location.origin || event.source !== reviewFrame.value?.contentWindow || !reviewOpen.value) return
  if (event.data?.type === 'structured-ready') authReview()
  if (event.data?.type === 'structured-import-updated' && String(event.data.tenant_id) === scope().tenantId && selected.value) void openDetail(selected.value, false).catch(notify)
  if (event.data?.type === 'structured-navigate' && event.data.view === 'model-settings') router.push({ path: '/platform/structured-data', query: { view: 'model-settings' } })
}
onMounted(() => window.addEventListener('message', receiveReview))
onBeforeUnmount(() => { generation++; stopPolling(); if (searchTimer) clearTimeout(searchTimer); if (optionsOpen) confirmStore.rejectConfirm(); window.removeEventListener('message', receiveReview) })
</script>

<template>
  <div class="materials-page">
    <header class="page-bar"><div><span class="section-label">研究资料</span><h1>资料中心</h1></div><span class="workspace-label">{{ auth.currentTenantName || '当前空间' }}</span></header>
    <main class="materials-main">
      <section class="intake-hero">
        <div class="hero-copy"><span class="eyebrow">中哲物产 · 研究工作台</span><h2>一份资料，连接你的研究</h2><p>统一上传原文件，按用途读取正文、分析表格或更新研究指标。每一个结果都能回到来源。</p><div class="purpose-line"><span>知识库正文</span><i></i><span>表格分析</span><i></i><span>商品研究</span></div></div>
        <div class="hero-action"><button class="primary large" @click="addFiles"><FileAddIcon />添加资料</button><span>文件 · 文件夹 · 网页</span></div>
      </section>
      <div v-if="error" class="error-banner" role="alert">{{ error }}<button class="text-button" @click="refresh">重新加载</button></div>
      <section v-if="uploadOpen" class="upload-panel">
        <div class="panel-heading"><div><h3>添加研究资料</h3><p>先保存原件，再选择如何使用。</p></div><button class="text-button" :disabled="uploading" @click="uploadOpen = false">收起</button></div>
        <div class="upload-mode"><button :class="{ active: uploadMode === 'file' }" @click="uploadMode = 'file'">文件上传</button><button :class="{ active: uploadMode === 'url' }" @click="uploadMode = 'url'">网页采集</button></div>
        <template v-if="uploadMode === 'file'">
          <div class="drop-area" :class="{ disabled: uploading }" @dragover.prevent @drop.prevent="dropFiles">
            <FileAddIcon class="drop-icon" /><strong>{{ pendingFiles.length ? `已选择 ${pendingFiles.length} 个文件` : '拖入资料，或选择文件' }}</strong><span>Excel / CSV / PDF / Word / 文本 / 图片 · 单个文件最大 100 MB</span>
            <div><button class="secondary" :disabled="uploading" @click="fileInput?.click()">选择文件</button><button class="text-button" :disabled="uploading" @click="folderInput?.click()"><FolderOpenIcon />选择文件夹</button></div>
            <input ref="fileInput" type="file" multiple class="hidden-input" @change="inputFiles"><input ref="folderInput" type="file" multiple webkitdirectory class="hidden-input" @change="inputFiles">
          </div>
          <ul v-if="pendingFiles.length" class="picked-files"><li v-for="file in pendingFiles" :key="file.name + file.size"><FileIcon /><span>{{ file.webkitRelativePath || file.name }}</span><small>{{ readableSize(file.size) }}</small></li></ul>
          <label v-if="mixedFiles" class="checkbox-row"><input v-model="automaticRouting" type="checkbox" :disabled="uploading">按文件类型自动分配用途：表格优先数值处理，文档进入正文检索。</label>
          <div class="purpose-cards">
            <label :class="{ chosen: includeStructured, unavailable: !hasStructured || !canWriteFacts }"><input v-model="includeStructured" type="checkbox" :disabled="!hasStructured || !canWriteFacts || uploading || automaticRouting && mixedFiles"><div><strong>表格分析</strong><p>读取完整数值和单元格，直接用于普通对话计算。</p><small>{{ hasStructured ? 'Excel XLSX · 保留原单元格' : '当前支持 XLSX；其他表格可接入研究指标' }}</small></div></label>
            <label :class="{ chosen: includeKnowledge }"><input v-model="includeKnowledge" type="checkbox" :disabled="uploading || automaticRouting && mixedFiles"><div><strong>知识库正文</strong><p>研报、文档和图片正文，可检索与引用。</p><small>沿用知识库权限；表格正文检索不用于精确计算</small></div></label>
            <label :class="{ chosen: includeResearch, unavailable: !pendingFiles.length || !canWriteFacts }"><input v-model="includeResearch" type="checkbox" :disabled="!pendingFiles.length || !canWriteFacts || uploading || automaticRouting && mixedFiles"><div><strong>商品研究</strong><p>{{ allTables ? '把表格字段绑定到锡研究指标，保留版本与证据。' : '明确共享为研究资料；文档数字不会自动写成指标。' }}</p><small>研究用途在空间内共享；表格首次确认指标和单位</small></div></label>
          </div>
        </template>
        <label v-else class="form-label">网页地址<input v-model="url" placeholder="https://…" type="url"></label>
        <div v-if="includeKnowledge || uploadMode === 'url'" class="knowledge-options"><label class="form-label">目标知识库<select v-model="pickedKb"><option value="">请选择知识库</option><option v-for="kb in libraries" :key="kb.id" :value="kb.id">{{ kb.name }}</option></select></label><label class="form-label">文件夹（可选）<input v-model="folder" placeholder="知识库根目录"></label><button class="secondary" @click="knowledgeOptions">解析选项与标签{{ tags.length ? ` · ${tags.length}` : '' }}</button></div>
        <div class="upload-footer"><span role="status">{{ batchStatus || (includeResearch ? '研究事实将共享到当前空间；个人判断保持独立。' : includeStructured ? '表格用于当前空间的数值分析，原单元格保持可追溯。' : '正文资料按目标知识库权限访问。') }}</span><button class="primary" :disabled="uploading || uploadMode === 'file' && !pendingFiles.length" @click="uploadMode === 'url' ? importUrl() : importFiles()">{{ uploading ? '正在处理…' : '保存并处理' }}</button></div>
      </section>
      <div class="library-layout">
        <section class="library-panel">
          <div class="panel-heading"><div><h3>空间资料 <span class="count">{{ total }}</span></h3><p>原件、处理进度与研究用途，集中管理。</p></div><button class="icon-button" :disabled="loading" aria-label="刷新资料" @click="refresh"><RefreshIcon /></button></div>
          <div class="library-filters"><div class="filter-tabs"><button v-for="tab in [{ id: 'all', name: '全部' }, { id: 'table', name: '表格' }, { id: 'document', name: '文档' }]" :key="tab.id" :class="{ active: kindFilter === tab.id }" @click="kindFilter = tab.id">{{ tab.name }}</button></div><label class="search-input"><SearchIcon /><input v-model="search" placeholder="查找资料名称" aria-label="查找资料"></label><select v-model="sourceFilter" aria-label="资料来源"><option value="manual">上传资料</option><option value="automatic">自动采集</option><option value="all">全部来源</option></select></div>
          <div v-if="loading && !assets.length" class="empty-state">正在读取当前空间资料…</div>
          <div v-else-if="!visibleAssets.length" class="empty-state"><FolderOpenIcon /><h4>{{ assets.length ? '没有匹配的资料' : '从第一份资料开始' }}</h4><p>上传后可以在这里查看原件、处理状态和每一种用途。</p><button class="secondary" @click="addFiles">添加资料</button></div>
          <div v-else class="asset-table-wrap"><table class="asset-table"><thead><tr><th>资料</th><th>用途与状态</th><th>加入时间</th><th></th></tr></thead><tbody><tr v-for="asset in visibleAssets" :key="asset.id"><td><button class="file-cell" @click="openDetail(asset).catch(notify)"><span class="file-symbol" :class="{ table: isTable(asset.filename) }"><FileExcelIcon v-if="isTable(asset.filename)" /><FileIcon v-else /></span><span><strong>{{ asset.filename }}</strong><small>{{ fileKind(asset.filename) }} · {{ readableSize(asset.size) }}</small></span></button></td><td><div class="badges"><span v-for="operation in asset.operations || []" :key="operation.id" class="badge" :class="statusClass(operation.status)">{{ purposeNames[operation.purpose] }} · {{ stateName(operation.status) }}</span><span v-if="!asset.operations?.length" class="badge">原件已保存</span></div></td><td class="muted">{{ dateLabel(asset.created_at) }}</td><td><button class="text-button" @click="openDetail(asset).catch(notify)">查看<ChevronRightIcon /></button></td></tr></tbody></table></div>
          <div class="pagination"><span>{{ offset + (assets.length ? 1 : 0) }}–{{ offset + assets.length }} / {{ total }} 份资料</span><div><button class="secondary" :disabled="!offset || loading" @click="page(false)">上一页</button><button class="secondary" :disabled="nextOffset === null || loading" @click="page(true)">下一页</button></div></div>
        </section>
        <aside class="library-aside"><section class="aside-card"><span class="eyebrow">本页处理概况</span><div class="overview-number"><strong>{{ pageReady }}</strong><span>份资料已就绪</span></div><div class="overview-line"><span>待确认或需恢复</span><b>{{ pagePending }}</b></div><p>处理状态按用途分别记录，原件保存和解析完成会明确区分。</p></section><section class="aside-card"><h4>上传后，去哪里使用？</h4><router-link to="/platform/creatChat">普通对话<ChevronRightIcon /></router-link><p>直接询问资料内容或表格数值，无需切换智能体。</p><router-link to="/platform/structured-data">表格分析<ChevronRightIcon /></router-link><p>查看数据集版本、字段和单元格证据。</p><router-link to="/platform/commodity-research">商品研究<ChevronRightIcon /></router-link><p>把已确认的指标用于看板与研究。</p></section></aside>
      </div>
      <section class="legacy-panel"><div class="panel-heading"><div><h3>既有资料</h3><p>原来上传的文件继续保留，可从这里进入查看。</p></div><button class="text-button" @click="legacy(pickedKb)">刷新</button></div><div v-if="Object.keys(legacyErrors).length" class="error-banner" role="status">{{ Object.values(legacyErrors).join("；") }}</div><div class="legacy-columns"><div><h4>知识库文档</h4><select v-model="pickedKb" aria-label="查看既有知识库" @change="legacy(pickedKb)"><option value="">选择知识库查看</option><option v-for="kb in libraries" :key="kb.id" :value="kb.id">{{ kb.name }}</option></select><div class="legacy-items"><router-link v-for="doc in legacyDocuments.slice(0, 5)" :key="doc.id" :to="{ path: '/platform/knowledge-bases/' + pickedKb, query: { knowledge_id: doc.id } }"><FileIcon /><span>{{ doc.title || doc.file_name || doc.id }}</span><small>{{ doc.parse_status === 'completed' ? '已解析' : doc.parse_status === 'failed' ? '解析失败' : '处理中' }}</small></router-link><router-link v-if="pickedKb" :to="'/platform/knowledge-bases/' + pickedKb" class="text-button">查看知识库全部文档<ChevronRightIcon /></router-link></div></div><div><h4>表格文件版本</h4><div class="legacy-items"><router-link v-for="dataset in legacyDatasets.slice(0, 5)" :key="dataset.dataset_id" :to="{ path: '/platform/structured-data', query: { dataset: dataset.dataset_id } }"><FileExcelIcon /><span>{{ dataset.source_file }}</span><small>已保存</small></router-link><router-link to="/platform/structured-data" class="text-button">查看全部表格版本<ChevronRightIcon /></router-link></div></div></div></section>
    </main>
    <t-drawer v-model:visible="detailOpen" :header="selected?.filename || '资料详情'" size="640px" :footer="false" @close="stopPolling">
      <div v-if="selected" class="asset-detail"><div class="original-card"><span class="eyebrow">原文件</span><h3>{{ selected.filename }}</h3><p>{{ fileKind(selected.filename) }} · {{ readableSize(selected.size) }} · {{ selected.shared_research ? '研究空间共享' : selected.shared_structured ? '当前空间表格共享' : '依知识库与上传者权限访问' }}</p><button class="secondary" @click="download"><DownloadIcon />下载原件</button><details><summary>查看文件指纹</summary><code>{{ selected.sha256 }}</code></details></div>
        <h3 class="detail-section-title">用途与处理状态</h3><div v-for="op in operations" :key="op.id" class="operation-card"><div><strong>{{ purposeNames[op.purpose] }}</strong><span class="badge" :class="statusClass(op.status)">{{ stateName(op.status) }}</span></div><p v-if="op.error" class="error-text">{{ op.error }}</p><p v-if="op.status_checked === false" class="error-text">最新处理结果暂无法核验：{{ op.status_refresh_error }}</p><p v-if="op.purpose === 'knowledge' && op.result?.parse_status">正文解析：{{ op.result.parse_status === 'completed' ? '已完成' : op.result.parse_status === 'failed' ? '失败，可重试' : '正在解析' }}</p><div class="operation-actions"><button v-if="op.status === 'needs_mapping' && op.purpose === 'research'" class="primary" @click="showMapping(op).catch(notify)">确认研究指标</button><button v-if="op.status === 'needs_mapping' && op.purpose === 'structured'" class="primary" @click="openReview(op)">确认表格范围</button><button v-if="['failed', 'interrupted'].includes(op.status)" class="secondary" :disabled="actionBusy" @click="retry(op)">恢复处理</button><router-link v-if="op.remote_ids?.dataset_id" :to="{ path: '/platform/structured-data', query: { dataset: op.remote_ids.dataset_id } }">查看表格</router-link><router-link v-if="op.remote_ids?.kb_id && op.remote_ids?.knowledge_id" :to="{ path: '/platform/knowledge-bases/' + op.remote_ids.kb_id, query: { knowledge_id: op.remote_ids.knowledge_id } }">查看正文</router-link></div></div>
        <section v-if="mappingOperation" class="mapping-panel"><h3>确认研究指标与单位</h3>
          <div v-if="preview.mode === 'bridge'" class="mapping-row"><strong>{{ preview.series?.sheet }} · {{ preview.series?.metric || preview.series?.description }}</strong><p>完整文件版本 {{ preview.total }} 条记录，原件指纹已核对。</p><label class="form-label">研究指标<select v-model="bridgeTarget"><option value="">请选择登记指标</option><option v-for="ind in indicators" :key="ind.series_id" :value="ind.series_id">{{ ind.name }} · {{ ind.unit }}</option></select></label><div class="two-fields"><label class="form-label">原单位<input v-model="bridgeUnit" placeholder="请核对原单位"></label><label class="form-label">换算系数<input v-model.number="bridgeFactor" type="number" min="0" step="any"></label></div><div v-if="preview.mode === 'bridge' && preview.sample_rows?.length" class="preview-table"><table><thead><tr><th>日期</th><th>原单元格</th><th>原值</th></tr></thead><tbody><tr v-for="(row,i) in preview.sample_rows" :key="i"><td>{{ row.date || row.period }}</td><td>{{ row.source_cell }}</td><td>{{ row.raw_value ?? row.value }}</td></tr></tbody></table><p>展示 {{ preview.sample_rows.length }} / {{ preview.total }} 条来源记录；研究接入按完整字段处理。</p></div><div v-if="preview.import_result?.conflicts?.length" class="preview-table"><h4>需要确认的来源冲突</h4><table><thead><tr><th>时点</th><th>原单元格</th><th>已有值</th><th>本次值</th></tr></thead><tbody><tr v-for="(conflict,i) in preview.import_result.conflicts" :key="i"><td>{{ conflict.as_of }}</td><td>{{ conflict.source_cell }}</td><td>{{ conflict.old_value ?? '—' }}</td><td>{{ conflict.new_value }}</td></tr></tbody></table></div><label v-if="preview.import_result?.conflicts?.length" class="checkbox-row"><input v-model="allowConflicts" type="checkbox">已核对来源冲突，允许保存为新的观测修订</label></div>
          <p>{{ preview.mode === 'vendor' ? '已识别供应商文件。确认新增指标前，请核对来源的定义与单位。' : '选择可人工导入的指标，确认原单位与换算系数。自动采集指标由更新任务维护，未识别内容不会补零。' }}</p><div v-for="column in preview.columns || []" :key="column.col_key" class="mapping-row"><strong>{{ column.header }}</strong><label class="form-label">研究指标<select v-model="mappings[column.col_key].series_id"><option value="">请选择登记指标</option><option v-for="ind in indicators" :key="ind.series_id" :value="ind.series_id">{{ ind.name }} · {{ ind.unit }}</option></select></label><div class="two-fields"><label class="form-label">原单位<input v-model="mappings[column.col_key].source_unit"></label><label class="form-label">换算系数<input v-model.number="mappings[column.col_key].factor" type="number" min="0" step="any"></label></div></div><label v-if="preview.mode === 'vendor'" class="checkbox-row"><input v-model="registerNew" type="checkbox">我已核对，允许登记本次新的供应商指标</label><details open v-if="preview.rows?.length"><summary>核对本次数据（{{ preview.rows.length }} 行）</summary><div class="preview-table"><table><thead><tr><th>写入</th><th>工作表 / 单元格</th><th>日期与指标</th><th>原值</th><th>已有值</th><th>状态</th></tr></thead><tbody><tr v-for="row in preview.rows" :key="row.row_key"><td><input v-model="selectedRows" type="checkbox" :value="row.row_key" :disabled="row.category === 'skip'" :aria-label="`写入 ${row.locator?.sheet || ''} ${row.locator?.value_cell || row.row_key}`"></td><td>{{ row.locator?.sheet }} · {{ row.locator?.value_cell }}</td><td>{{ row.as_of || row.locator?.raw_date }}<br>{{ row.series_id || '待绑定指标' }}</td><td>{{ row.locator?.raw_value ?? row.value }}</td><td>{{ row.old_value ?? '—' }}</td><td>{{ row.category === 'revision' ? '修订，需逐项勾选' : row.category === 'skip' ? '已有相同值' : row.category === 'error' ? '请确认映射' : '新增' }}<small v-if="row.reason">{{ row.reason }}</small></td></tr></tbody></table><p>修订行默认不写入；核对旧值和来源后逐项勾选。提交后仍会校验映射及并发变化。</p></div></details><label class="form-label">映射说明<input v-model="mappingNote"></label><button class="primary" :disabled="actionBusy || preview.mode === 'table' && !selectedRows.length" @click="commitMapping">确认并写入研究</button></section>
        <section v-if="datasetOf && isTable(selected.filename) && canWriteFacts" class="append-purpose"><h3>从已解析的表格接入研究</h3><p>选择已确认字段，直接映射到研究指标，无需再次上传。</p><button class="secondary" :disabled="loadingSeries" @click="loadBridgeSeries">{{ loadingSeries ? '读取完整字段目录…' : '读取表格字段' }}</button><div v-if="bridgeSeries.length" class="knowledge-options"><label class="form-label">已确认字段<select v-model="pickedSeries"><option value="">请选择字段</option><option v-for="field in bridgeSeries" :key="field.series_id" :value="field.series_id">{{ field.sheet }} · {{ field.metric || field.description || field.series_id }} · {{ field.unit || '单位待确认' }}</option></select></label><button class="primary" :disabled="!pickedSeries || actionBusy" @click="previewBridge">预览映射</button></div></section>
        <section class="append-purpose"><h3>追加用途</h3><p>复用此原文件，成功分支保持已有结果。</p><label class="form-label">正文目标知识库<select v-model="pickedKb"><option value="">请选择知识库</option><option v-for="kb in libraries" :key="kb.id" :value="kb.id">{{ kb.name }}</option></select></label><div class="operation-actions"><button v-if="selected.capabilities.knowledge" class="secondary" :disabled="actionBusy || !pickedKb" @click="appendPurpose('knowledge')">用于正文检索</button><button v-if="selected.capabilities.structured && canWriteFacts" class="secondary" :disabled="actionBusy" @click="appendPurpose('structured')">用于表格分析</button><button v-if="selected.capabilities.research && canWriteFacts" class="secondary" :disabled="actionBusy" @click="appendPurpose('research')">{{ isTable(selected.filename) ? '用于研究指标' : '用作研究资料' }}</button></div></section>
      </div>
    </t-drawer>
    <t-dialog v-model:visible="reviewOpen" header="确认表格范围与字段" width="90%" :footer="false" @close="closeReview"><iframe v-if="reviewOpen" ref="reviewFrame" class="review-frame" :src="'/structured/?view=review&import_id=' + encodeURIComponent(reviewImport)" title="资料表格范围确认" @load="authReview"></iframe><div class="review-footer"><span>确认结果会同步回这份资料的处理状态。</span><button class="primary" @click="closeReview">完成并查看状态</button></div></t-dialog>
  </div>
</template>

<style scoped>
.materials-page{height:100%;overflow:auto;background:var(--app-bg);color:var(--td-text-color-primary)}
.page-bar{display:flex;align-items:center;justify-content:space-between;padding:20px 32px;border-bottom:1px solid var(--app-border-color);background:var(--app-card-bg)}
.page-bar>div{display:flex;gap:16px;align-items:center}.section-label,.workspace-label{font-size:var(--app-text-md);color:var(--app-text-secondary)}h1{font-size:var(--app-text-3xl);margin:0;font-weight:650}
.materials-main{max-width:1400px;margin:0 auto;padding:32px}.intake-hero{display:flex;gap:32px;justify-content:space-between;align-items:center;padding:32px 36px;margin-bottom:24px;border:1px solid var(--app-border-color);border-radius:var(--app-radius-xl);background:var(--app-card-bg)}
.eyebrow{font-size:var(--app-text-sm);font-weight:600;color:var(--td-brand-color);letter-spacing:.06em}.hero-copy h2{font-size:var(--app-text-4xl);font-weight:650;letter-spacing:-.02em;margin:12px 0}.hero-copy p{margin:0;color:var(--app-text-secondary);line-height:1.8;font-size:var(--app-text-base)}.purpose-line{display:flex;gap:12px;align-items:center;margin-top:20px;font-size:var(--app-text-md);color:var(--td-text-color-secondary)}.purpose-line i{height:4px;width:4px;border-radius:var(--app-radius-pill);background:var(--td-brand-color)}.hero-action{display:flex;flex-direction:column;gap:12px;align-items:center;flex-shrink:0}.hero-action>span{font-size:var(--app-text-sm);color:var(--app-text-secondary)}
button,input,select{font:inherit}button{display:inline-flex;align-items:center;justify-content:center;gap:7px;cursor:pointer;border:0;font-size:var(--app-text-md)}button:disabled{opacity:.55;cursor:not-allowed}button svg{width:17px;height:17px}.primary{background:var(--td-brand-color);color:var(--td-text-color-anti);padding:10px 16px;border-radius:var(--app-radius-md);font-weight:550}.primary.large{padding:14px 24px;font-size:var(--app-text-base)}.secondary{background:var(--td-bg-color-secondarycontainer);color:var(--td-text-color-primary);padding:9px 14px;border:1px solid var(--app-border-color);border-radius:var(--app-radius-md)}.text-button{background:transparent;color:var(--td-brand-color);padding:6px 3px;text-decoration:none}.icon-button{width:34px;height:34px;background:var(--td-bg-color-secondarycontainer);border-radius:var(--app-radius-md);color:var(--td-text-color-secondary)}
.upload-panel,.library-panel,.aside-card,.legacy-panel{border:1px solid var(--app-border-color);border-radius:var(--app-radius-xl);background:var(--app-card-bg)}.upload-panel{padding:24px;margin-bottom:24px}.panel-heading{display:flex;align-items:center;justify-content:space-between;gap:16px;margin-bottom:20px}.panel-heading h3{font-size:var(--app-text-2xl);margin:0;font-weight:600}.panel-heading p{font-size:var(--app-text-md);margin:7px 0 0;color:var(--app-text-secondary);line-height:1.7}.upload-mode,.filter-tabs{display:flex;gap:4px}.upload-mode{margin-bottom:16px}.upload-mode button,.filter-tabs button{padding:7px 14px;border-radius:var(--app-radius-sm);background:transparent;color:var(--app-text-secondary)}.upload-mode button.active,.filter-tabs button.active{background:var(--td-brand-color-light);color:var(--td-brand-color);font-weight:600}
.drop-area{display:flex;flex-direction:column;align-items:center;gap:12px;padding:28px;border:1px dashed var(--td-component-border);border-radius:var(--app-radius-lg);background:var(--td-bg-color-secondarycontainer)}.drop-icon{width:30px;height:30px;color:var(--td-brand-color)}.drop-area strong{font-size:var(--app-text-xl)}.drop-area>span{font-size:var(--app-text-md);color:var(--app-text-secondary)}.drop-area>div{display:flex;gap:14px}.hidden-input{display:none}.picked-files{list-style:none;padding:0;margin:12px 0}.picked-files li{display:flex;gap:10px;align-items:center;padding:9px 0;font-size:var(--app-text-md);border-bottom:1px solid var(--app-border-color)}.picked-files li>span{flex:1;overflow-wrap:anywhere}.picked-files li svg{color:var(--td-brand-color);width:18px}.picked-files small{color:var(--app-text-secondary)}
.purpose-cards{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:12px;margin-top:20px}.purpose-cards label{display:flex;align-items:flex-start;gap:10px;padding:16px;border:1px solid var(--app-border-color);border-radius:var(--app-radius-md);cursor:pointer}.purpose-cards label.chosen{border-color:var(--td-brand-color);background:var(--td-brand-color-light)}.purpose-cards label.unavailable{opacity:.6}.purpose-cards input{margin-top:4px;accent-color:var(--td-brand-color)}.purpose-cards strong{font-size:var(--app-text-base);font-weight:600}.purpose-cards p{font-size:var(--app-text-md);line-height:1.7;margin:7px 0;color:var(--app-text-secondary)}.purpose-cards small{font-size:var(--app-text-sm);color:var(--app-text-secondary)}.knowledge-options{display:flex;gap:16px;align-items:end;margin-top:20px}.form-label{display:flex;flex-direction:column;gap:8px;font-size:var(--app-text-md);flex:1;min-width:0}.form-label input,.form-label select,.library-filters select,.legacy-columns select{width:100%;padding:9px 10px;border:1px solid var(--app-border-color);border-radius:var(--app-radius-md);background:var(--app-card-bg);color:var(--td-text-color-primary)}.upload-footer{display:flex;justify-content:space-between;align-items:center;gap:16px;padding-top:20px;margin-top:20px;border-top:1px solid var(--app-border-color)}.upload-footer>span{font-size:var(--app-text-md);color:var(--app-text-secondary)}
.library-layout{display:grid;grid-template-columns:minmax(0,1fr) 278px;gap:24px;align-items:start}.library-panel{padding:24px;min-width:0}.count{font-size:var(--app-text-sm);display:inline-block;margin-left:6px;padding:2px 8px;background:var(--td-bg-color-secondarycontainer);border-radius:var(--app-radius-pill);color:var(--app-text-secondary)}.library-filters{display:flex;gap:12px;align-items:center;margin-bottom:18px;flex-wrap:wrap}.search-input{display:flex;gap:7px;align-items:center;border:1px solid var(--app-border-color);border-radius:var(--app-radius-md);padding:7px 10px;flex:1;min-width:140px}.search-input svg{width:16px;color:var(--app-text-secondary)}.search-input input{border:0;outline:0;width:100%;font-size:var(--app-text-md);background:transparent;color:var(--td-text-color-primary)}.library-filters>select{width:auto;font-size:var(--app-text-md)}.asset-table-wrap{overflow:auto}.asset-table{width:100%;border-collapse:collapse;text-align:left;font-size:var(--app-text-md)}.asset-table th{color:var(--app-text-secondary);font-weight:500;padding:10px 8px;border-bottom:1px solid var(--app-border-color);white-space:nowrap}.asset-table td{padding:16px 8px;border-bottom:1px solid var(--app-border-color);vertical-align:middle}.file-cell{display:flex;gap:12px;align-items:center;justify-content:flex-start;text-align:left;background:transparent;color:inherit;max-width:380px;padding:0}.file-cell>span:last-child{min-width:0}.file-cell strong{display:block;font-size:var(--app-text-base);font-weight:500;word-break:break-word}.file-cell small{display:block;margin-top:5px;color:var(--app-text-secondary);font-size:var(--app-text-sm)}.file-symbol{display:flex;align-items:center;justify-content:center;flex-shrink:0;width:40px;height:44px;border-radius:var(--app-radius-md);background:var(--td-brand-color-light);color:var(--td-brand-color)}.file-symbol svg{width:22px;height:22px}.file-symbol.table{background:var(--td-success-color-light);color:var(--td-success-color)}.badges{display:flex;gap:5px;flex-wrap:wrap}.badge{display:inline-block;padding:3px 8px;border-radius:var(--app-radius-sm);font-size:var(--app-text-sm);background:var(--td-bg-color-secondarycontainer);color:var(--app-text-secondary);white-space:nowrap}.badge.ready{background:var(--td-success-color-light);color:var(--td-success-color)}.badge.pending,.badge.warning{background:var(--td-warning-color-light);color:var(--td-warning-color)}.muted{color:var(--app-text-secondary)}.pagination{display:flex;gap:12px;justify-content:space-between;align-items:center;margin-top:18px;color:var(--app-text-secondary);font-size:var(--app-text-sm)}.pagination>div{display:flex;gap:8px}.empty-state{display:flex;flex-direction:column;align-items:center;justify-content:center;padding:48px 20px;color:var(--app-text-secondary);text-align:center}.empty-state>svg{width:36px;height:36px;color:var(--td-brand-color);margin-bottom:12px}.empty-state h4{font-size:var(--app-text-xl);margin:4px 0;color:var(--td-text-color-primary)}.empty-state p{font-size:var(--app-text-md);margin:8px 0 18px;line-height:1.7}
.library-aside{display:flex;flex-direction:column;gap:20px}.aside-card{padding:22px}.overview-number{display:flex;gap:12px;align-items:baseline;margin:20px 0}.overview-number strong{font-size:var(--app-text-display);font-weight:600;line-height:1;color:var(--td-brand-color)}.overview-number span,.overview-line{font-size:var(--app-text-md);color:var(--app-text-secondary)}.overview-line{display:flex;justify-content:space-between}.aside-card p{font-size:var(--app-text-md);color:var(--app-text-secondary);line-height:1.8;margin:12px 0 0}.aside-card h4,.legacy-panel h4{font-size:var(--app-text-base);margin:0 0 16px;font-weight:600}.aside-card a{display:flex;justify-content:space-between;align-items:center;margin-top:18px;color:var(--td-brand-color);font-size:var(--app-text-base);text-decoration:none}.aside-card a svg{width:16px}.aside-card a+p{margin-top:7px}.legacy-panel{padding:24px;margin-top:24px}.legacy-columns{display:grid;grid-template-columns:1fr 1fr;gap:32px}.legacy-items{display:flex;flex-direction:column;gap:12px;margin-top:16px}.legacy-items a{display:flex;gap:8px;align-items:center;color:var(--td-text-color-primary);font-size:var(--app-text-md);text-decoration:none}.legacy-items svg{width:16px;color:var(--td-brand-color)}.legacy-items span{flex:1;word-break:break-word}.legacy-items small{color:var(--app-text-secondary)}.legacy-items .text-button{justify-content:flex-start;color:var(--td-brand-color)}.error-banner{background:var(--td-error-color-light);border-radius:var(--app-radius-md);color:var(--td-error-color);padding:12px 16px;font-size:var(--app-text-md);margin-bottom:20px}.error-banner button{margin-left:20px}
.asset-detail h3{font-size:var(--app-text-2xl);font-weight:600}.original-card{background:var(--td-bg-color-secondarycontainer);padding:20px;border-radius:var(--app-radius-lg)}.original-card h3{word-break:break-word;margin:12px 0}.original-card p,.append-purpose p,.mapping-panel p{font-size:var(--app-text-md);line-height:1.8;color:var(--app-text-secondary)}.original-card details{margin-top:18px;font-size:var(--app-text-sm);color:var(--app-text-secondary)}.original-card code{display:block;word-break:break-all;margin-top:12px}.detail-section-title{margin:28px 0 16px}.operation-card{border:1px solid var(--app-border-color);border-radius:var(--app-radius-md);padding:16px;margin-top:12px}.operation-card>div:first-child{display:flex;gap:12px;justify-content:space-between;align-items:center}.operation-card strong{font-size:var(--app-text-base)}.operation-card p{font-size:var(--app-text-md)}.operation-actions{display:flex;gap:10px;align-items:center;flex-wrap:wrap;margin-top:14px}.operation-actions a{font-size:var(--app-text-md);color:var(--td-brand-color);text-decoration:none}.error-text{color:var(--td-error-color)}.append-purpose,.mapping-panel{border-top:1px solid var(--app-border-color);margin-top:26px;padding-top:10px}.mapping-row{padding:16px 0;border-bottom:1px solid var(--app-border-color)}.mapping-row>strong{display:block;margin-bottom:14px;font-size:var(--app-text-base)}.two-fields{display:flex;gap:12px;margin-top:12px}.mapping-panel>.form-label{margin:16px 0}.mapping-panel details{margin-top:18px;font-size:var(--app-text-md)}.preview-table{overflow:auto;margin-top:12px}.preview-table table{border-collapse:collapse;width:100%;font-size:var(--app-text-sm)}.preview-table td,.preview-table th{padding:8px;border-bottom:1px solid var(--app-border-color);text-align:left}.checkbox-row{display:flex;gap:8px;font-size:var(--app-text-md);line-height:1.7;margin:16px 0}.review-frame{width:100%;height:70vh;border:0}.review-footer{display:flex;gap:16px;align-items:center;justify-content:space-between;margin-top:16px;font-size:var(--app-text-md);color:var(--app-text-secondary)}
@media(max-width:1100px){.library-layout{grid-template-columns:minmax(0,1fr)}.library-aside{display:grid;grid-template-columns:1fr 1fr}.materials-main{padding:24px}.intake-hero{padding:26px}.purpose-cards{grid-template-columns:1fr}.knowledge-options{flex-wrap:wrap}}
@media(max-width:700px){.page-bar{padding:16px}.section-label{display:none}.materials-main{padding:16px}.intake-hero{flex-direction:column;align-items:flex-start;gap:22px;padding:24px}.hero-action{align-items:flex-start}.library-aside,.legacy-columns{grid-template-columns:1fr}.library-panel,.upload-panel,.legacy-panel{padding:18px}.knowledge-options{flex-direction:column;align-items:stretch}.pagination{flex-wrap:wrap}.drop-area>span{line-height:1.7;text-align:center}.upload-footer{flex-direction:column;align-items:flex-start}}
</style>

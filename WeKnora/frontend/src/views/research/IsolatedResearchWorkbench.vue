<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import { useAuthStore } from '@/stores/auth'
import { useUploadTasksStore } from '@/stores/uploadTasks'
import { updateResearchWorkbenchOnboarding, type ResearchWorkbench } from '@/api/workspaces'
import { createIsolatedResearchClient, type IsolatedResearchIdentity, type WorkbenchSource, type WorkbenchDataset, type WorkbenchIndicator, type WorkbenchOverview } from '@/api/isolatedResearch'
import KbUploadSourceDropdown from '@/views/knowledge/components/KbUploadSourceDropdown.vue'
import { buildUploadFileName } from '@/views/knowledge/folderTree'
import { workspaceErrorMessage } from '@/views/workspaces/workspaceForm'
import { applyMetadataRows, parseHeaderRows } from './workbenchMapping'
import { observationChart, reusableTemplate, structureBody, structureDetails, structureTree, type ResearchArtifact } from './workbenchArtifacts'
import { isolatedWorkbenchScope, templateById, workbenchModuleNames } from './workbenchTemplates'

const props = defineProps<{ workbench: ResearchWorkbench }>()
const emit = defineEmits<{ 'open-chat': []; 'update-workbench': [workbench: ResearchWorkbench] }>()
const auth = useAuthStore(), uploads = useUploadTasksStore()
const uploadTypes = ['pdf','txt','md','docx','doc','pptx','ppt','epub','xmind','html','htm','mhtml','jpg','jpeg','png','csv','xlsx','xls','mp3','wav','m4a','flac','ogg','zip']
const saveVisualOriginals = ref(true)
const overview = ref<WorkbenchOverview | null>(null), sources = ref<WorkbenchSource[]>([]), datasets = ref<WorkbenchDataset[]>([]), indicators = ref<WorkbenchIndicator[]>([])
const indicatorNext = ref<number | null>(null), indicatorTotal = ref(0), indicatorFilter = ref('')
const visibleIndicators = computed(() => indicators.value.filter(indicator => `${indicator.name} ${indicator.unit || ''} ${indicator.frequency || ''}`.toLowerCase().includes(indicatorFilter.value.toLowerCase())))
const sourceTotal = ref(0), sourcePage = ref(1), recordTotal = ref(0), recordNextOffset = ref<number | null>(null)
const loading = ref(false), busy = ref(false), error = ref(''), drawerError = ref('')
const module = ref('overview'), drawer = ref<'sources' | 'review' | 'evidence' | 'upload' | 'edit' | null>(null)
const sourceContent = ref<{text?: string; sha256?: string; supported?: boolean; notice?: string; image_data_url?: string} | null>(null), sourceContentOpen = ref(false), sourceContentLoading = ref(false)
const selectedSource = ref<WorkbenchSource | null>(null), pendingFiles = ref<File[]>([])
const template = computed(() => templateById(props.workbench.template_id || 'blank'))
const guideVisible = computed(() => !props.workbench.onboarding || props.workbench.onboarding.status === 'active')
const extraModules = ref<string[]>([])
const allModules = computed(() => [...new Set<string>([...template.value.modules, ...extraModules.value])])
const moduleList = computed(() => allModules.value.filter(id => id !== 'assistant'))
const modulePicker = ref(false)
const structureNodes = computed(() => structureTree(records.value as ResearchArtifact[]))
const recordParent = ref(''), editingRecord = ref<Record<string, any> | null>(null)
const observationPanels = ref<Array<{indicator: WorkbenchIndicator; observations: Array<Record<string, any>>; total: number; next_offset: number | null; offset: number}>>([])
const readinessText = computed(() => ({ empty: '空白起步，先添加自己的资料', preparing: '资料准备中，确认后再进入研究', partially_ready: '部分资料可用，剩余待核项继续保留', ready: '已确认资料可用，研究判断仍由你确认' }[overview.value?.readiness.status || 'empty']))
const records = ref<Array<Record<string, any>>>([]), recordTitle = ref(''), recordBody = ref('')
const recordSources = ref<string[]>([]), recordConfirmed = ref(false)
const importList = ref<Array<Record<string, any>>>([]), activeImport = ref<Record<string, any> | null>(null), mapping = ref<Record<string, any> | null>(null), mappingPreview = ref<Record<string, any> | null>(null)
const sheetEvidence = ref<Record<string,any> | null>(null), columnPages = ref<Record<number,number>>({}), metadataChoices = ref<Record<number,{nameRow:string;unitRow:string;frequencyRow:string}>>({})
const futureConfirmed = ref(false)
const sourceRole = ref(''), scanStage = ref(''), datasetSeries = ref<Array<Record<string, any>>>([]), activeDataset = ref(''), researchPreview = ref<Record<string, any> | null>(null), timeType = ref(''), entryTime = ref(''), importNote = ref('')
const reviewRoleConfirmed = ref(false)
const selectedSeriesIds = ref<string[]>([]), batchErrors = ref<Record<string,string>>({}), batchProgress = ref(''), batchRuleConfirmed = ref(false), seriesNextOffset = ref<number | null>(null), seriesTotal = ref(0)
const editableEndpoint = computed(() => ({ structure: 'structure', judgment: 'judgments', notes: 'notes', brief: 'briefs', strategies: 'strategies' } as Record<string, string>)[module.value])
const taskNames = { sources: '导入资料', review: '核对来源', research: '开始研究' }
let generation = 0, scopeEpoch = 0, sourceSelection = 0, recordSelection = 0, chartModuleEpoch = 0, chartRequest = 0
const chartRequests = new Map<string,number>(), desiredCharts = ref<string[]>([])
let refreshTimer: ReturnType<typeof setTimeout> | undefined
function identity(): IsolatedResearchIdentity { return { token: auth.token || '', tenantId: String(auth.selectedTenantId ?? auth.tenant?.id ?? ''), userId: auth.user?.id || '', workbenchId: props.workbench.id, knowledgeBaseId: props.workbench.knowledge_base_id } }
const scopeKey = computed(() => Object.values(identity()).join(':'))
function client() { return createIsolatedResearchClient(identity(), identity) }
async function load() {
  const own = ++generation
  overview.value = null; sources.value = []; datasets.value = []; indicators.value = []; records.value = []
  loading.value = true; error.value = ''
  try {
    const api = client()
    const [summary, sourceList, indicatorList, scans] = await Promise.all([
      api.get<WorkbenchOverview>('/overview'),
      api.get<{ knowledge_base_id: string; sources: WorkbenchSource[]; datasets: WorkbenchDataset[]; total?:number }>('/sources?page=1&page_size=100'),
      api.get<{ indicators: WorkbenchIndicator[]; total?:number;next_offset?:number|null }>('/indicators'),
      api.get<{ imports: Array<Record<string, any>> }>('/source-imports'),
    ])
    if (own !== generation) return
    if (!isolatedWorkbenchScope(summary.workbench, props.workbench.id, props.workbench.knowledge_base_id) || sourceList.knowledge_base_id !== props.workbench.knowledge_base_id) throw new Error('工作台与资料范围不匹配，已停止显示。')
    overview.value = summary; sources.value = sourceList.sources || []; sourcePage.value = 1; sourceTotal.value = sourceList.total ?? sources.value.length; datasets.value = sourceList.datasets || []; indicators.value = indicatorList.indicators || []; indicatorNext.value = indicatorList.next_offset ?? null; indicatorTotal.value = indicatorList.total || indicators.value.length; importList.value = scans.imports || []
    if (editableEndpoint.value) await loadRecords()
  } catch (e) { if (own === generation) error.value = workspaceErrorMessage(e) }
  finally { if (own === generation) loading.value = false }
}
async function guide(status: 'active' | 'dismissed' | 'skipped' | 'completed', task?: keyof typeof taskNames) {
  if (busy.value) return
  const own = generation, ownerEpoch = scopeEpoch; busy.value = true; error.value = ''
  try {
    const result = await updateResearchWorkbenchOnboarding(props.workbench.id, { status, ...(task ? { tasks: { [task]: true } } : {}) })
    if (own === generation) emit('update-workbench', result.data)
  } catch (e) { if (own === generation) error.value = workspaceErrorMessage(e) }
  finally { if (ownerEpoch === scopeEpoch) busy.value = false }
}
function openSources() { sourceSelection++; drawer.value = 'sources'; drawerError.value = '' }
function openGuide() { void guide('active') }
function openReview() { sourceSelection++; drawer.value = 'review'; drawerError.value = '' }
async function showSource(source: WorkbenchSource) {
  selectedSource.value = source; drawer.value = 'evidence'; drawerError.value = ''; sourceContent.value = null; sourceContentOpen.value = false; sourceContentLoading.value = true
  const own = generation, selection = ++sourceSelection
  try {
    const result = await client().get<{text?: string; sha256?: string; supported?: boolean; notice?: string; image_data_url?: string}>(`/sources/${encodeURIComponent(source.id)}/content`)
    if (own === generation && selection === sourceSelection && selectedSource.value?.id === source.id) sourceContent.value = result
  } catch (e) { if (own === generation && selection === sourceSelection && selectedSource.value?.id === source.id) drawerError.value = workspaceErrorMessage(e) }
  finally { if (own === generation && selection === sourceSelection && selectedSource.value?.id === source.id) sourceContentLoading.value = false }
}
function selectModule(id: string) {
  if (id === 'assistant') { emit('open-chat'); return }
  chartModuleEpoch++; recordSelection++; desiredCharts.value = []; chartRequests.clear(); sourceSelection++; module.value = id; records.value = []; recordTotal.value = 0; recordNextOffset.value = null; drawer.value = null; drawerError.value = ''; observationPanels.value = []
  if (id !== 'overview' && !allModules.value.includes(id)) { extraModules.value.push(id); saveModuleLayout() }
  if (editableEndpoint.value) void loadRecords()
}
function selectFiles(files: File[]) { if (!files.length) return; pendingFiles.value = [...files]; drawer.value = 'upload'; drawerError.value = '' }
function enqueueFiles() {
  if (!pendingFiles.value.length || busy.value) return
  const own = identity()
  if (!own.token || !own.userId || !own.tenantId) { drawerError.value = '请先登录并选择空间。'; return }
  const rawOnly = saveVisualOriginals.value ? pendingFiles.value.filter(file => ['png','jpg','jpeg','zip'].includes(file.name.split('.').at(-1)?.toLowerCase() || '')) : []
  const normal = pendingFiles.value.filter(file => !rawOnly.includes(file))
  for (const [files,sourceOnly] of [[normal,false],[rawOnly,true]] as const) {
    if (files.length) uploads.enqueue({kbId:own.knowledgeBaseId,kbName:props.workbench.knowledge_base_name||'',targetFolder:'',...(sourceOnly?{processConfig:{source_only:true}}:{}),uploads:files.map(file=>({file,fileName:buildUploadFileName(file,'')}))})
  }
  pendingFiles.value = []; drawer.value = 'sources'; void guide('active', 'sources')
  MessagePlugin.info('原件上传到绑定知识库；Excel 等待结构化解析，其他文档按知识库配置解析。')
}
function filesChanged(event: Event) {
  if ((event as CustomEvent).detail?.kbId !== props.workbench.knowledge_base_id) return
  if (refreshTimer) clearTimeout(refreshTimer)
  refreshTimer = setTimeout(() => { void load() }, 300)
}
async function pollJob(result: Record<string, any>, own: number): Promise<Record<string, any>> {
  let current = result
  const deadline = Date.now() + 300_000
  while (current.status === 'processing') {
    if (own !== generation) throw new Error('工作台已切换，已停止等待。')
    if (Date.now() > deadline) throw new Error('仍在处理，请稍后刷新继续查看；无需重复上传。')
    scanStage.value = current.stage || '正在处理本知识库原件'
    await new Promise(resolve => setTimeout(resolve, 1000))
    if (own !== generation) throw new Error('工作台已切换，已停止等待。')
    current = await client().get(`/scan-jobs/${encodeURIComponent(current.id)}`)
  }
  if (current.status === 'failed') throw new Error(current.error || '当前项处理失败，可重试。')
  return current.result || current
}
async function saveSourceRole(source: WorkbenchSource, role: string) {
  if (!role || busy.value) return
  const own = generation, ownerEpoch = scopeEpoch; busy.value = true; drawerError.value = ''
  try {
    await client().put(`/sources/${encodeURIComponent(source.id)}/role`, { source_role: role })
    if (own !== generation) return
    source.source_role = role
    sourceRole.value = role
  } catch (e) { if (own === generation) drawerError.value = workspaceErrorMessage(e) }
  finally { if (ownerEpoch === scopeEpoch) busy.value = false }
}
async function scanSource(source: WorkbenchSource) {
  if (busy.value) return
  if (source.source_role !== 'observations') { drawerError.value = '请先将资料用途明确设为真实观测；取数需求和规范不能作为观测。'; return }
  const own = generation, ownerEpoch = scopeEpoch; busy.value = true; drawerError.value = ''; scanStage.value = '读取本知识库原件'
  try {
    const initial = await client().post<Record<string, any>>(`/sources/${encodeURIComponent(source.id)}/scan`, {})
    const result = await pollJob(initial, own)
    if (own !== generation) return
    await load()
    drawer.value = 'review'
    if (result.import_id) await openImport(result.import_id)
  } catch (e) { if (own === generation) drawerError.value = workspaceErrorMessage(e) }
  finally { if (ownerEpoch === scopeEpoch) { busy.value = false; scanStage.value = '' } }
}
async function openImport(id: string) {
  const own = generation, selection = ++sourceSelection; drawerError.value = ''; activeImport.value = null; sheetEvidence.value = null; mapping.value = null; mappingPreview.value = null; researchPreview.value = null; datasetSeries.value = []; sourceRole.value = ''; reviewRoleConfirmed.value = false
  try {
    const doc = await client().get<Record<string, any>>(`/source-imports/${encodeURIComponent(id)}`)
    if (own !== generation || selection !== sourceSelection) return
    activeImport.value = doc; drawer.value = 'review'
    const first = doc.sheets?.find((sheet: any) => sheet.status !== 'empty')
    if (first) await chooseSheet(first.name)
  } catch (e) { if (own === generation && selection === sourceSelection) drawerError.value = workspaceErrorMessage(e) }
}
async function chooseSheet(name: string) {
  if (!activeImport.value) return
  const own = generation, selection = ++sourceSelection, importId = activeImport.value.import_id; mapping.value = null; mappingPreview.value = null; drawerError.value = ''; sheetEvidence.value = null; columnPages.value = {}; metadataChoices.value = {}
  try {
    const result = await client().get<Record<string, any>>(`/source-imports/${encodeURIComponent(activeImport.value.import_id)}/sheet?name=${encodeURIComponent(name)}`)
    if (own === generation && selection === sourceSelection && activeImport.value?.import_id === importId) { sheetEvidence.value = result; mapping.value = result.mapping ? JSON.parse(JSON.stringify(result.mapping)) : null }
  } catch (e) { if (own === generation && selection === sourceSelection) drawerError.value = workspaceErrorMessage(e) }
}
function blockColumns(block: Record<string,any>, index: number) { const offset = (columnPages.value[index] || 0) * 12; return (block.columns || []).slice(offset,offset + 12) }
function metadataRows(index: number) { return metadataChoices.value[index] || (metadataChoices.value[index] = { nameRow:'',unitRow:'',frequencyRow:'' }) }
function setColumnPage(index: number, next: number) { columnPages.value[index] = Math.max(0,next) }
function normalizeEndRow(block: Record<string,any>) { if (!block.data_end_row) block.data_end_row = null }
function setHeaders(block: Record<string,any>, input: string) {
  mappingPreview.value = null
  try { block.header_rows = parseHeaderRows(input); drawerError.value = '' } catch (e) { drawerError.value = workspaceErrorMessage(e) }
}
function applyHeaders(index: number) {
  if (!mapping.value || !sheetEvidence.value) return
  mappingPreview.value = null
  try {
    const choice=metadataRows(index), block=mapping.value.blocks[index]
    const updated=applyMetadataRows(block,sheetEvidence.value.preview || [],choice.nameRow?Number(choice.nameRow):undefined,choice.unitRow?Number(choice.unitRow):undefined,choice.frequencyRow?Number(choice.frequencyRow):undefined)
    mapping.value.blocks[index]=updated;mappingPreview.value=null;drawerError.value=''
    MessagePlugin.info('已按选定原件行更新此区块，需重新检查全部数据。')
  } catch(e) { drawerError.value=workspaceErrorMessage(e) }
}
function headerPreview(block: Record<string,any>, index: number) {
  const columns = [block.date_column,...blockColumns(block,index).slice(0,4).map((column:any)=>column.column)].filter(Boolean)
  return (sheetEvidence.value?.preview || []).map((row:any[])=>row.filter(cell=>columns.includes(cell.cell.replace(/[0-9]+$/,''))))
}
async function checkMapping(confirm = false) {
  if (busy.value || !mapping.value || !activeImport.value) return
  if (sourceRole.value !== 'observations' || !reviewRoleConfirmed.value) { drawerError.value = '请先明确此表是实际观测，并核对用途；清单和规范不得导入为观测。'; return }
  const own = generation, ownerEpoch = scopeEpoch; busy.value = true; drawerError.value = ''
  try {
    const result = await client().post<Record<string, any>>(`/source-imports/${encodeURIComponent(activeImport.value.import_id)}/${confirm ? 'confirm' : 'preview'}`, { mapping: mapping.value, save_template: false, source_role: sourceRole.value })
    const finished = await pollJob(result, own)
    if (own !== generation) return
    mappingPreview.value = finished
    if (confirm) {
      await load()
      drawer.value = 'review'
      MessagePlugin.success('已保存本库工作表的结构化结果；下一步确认指标口径。')
    }
  } catch (e) { if (own === generation) drawerError.value = workspaceErrorMessage(e) }
  finally { if (ownerEpoch === scopeEpoch) { busy.value = false; scanStage.value = '' } }
}
async function openDataset(dataset: WorkbenchDataset) {
  const id = String(dataset.dataset_id || dataset.id || '')
  if (!id) return
  const own = generation, selection = ++sourceSelection; drawerError.value = ''; datasetSeries.value = []; researchPreview.value = null; activeImport.value = null; mapping.value = null; activeDataset.value = id
  try {
    const result = await client().get<{series: Array<Record<string, any>>;total:number;next_offset:number|null}>(`/datasets/${encodeURIComponent(id)}/series?offset=0&limit=100`)
    if (own !== generation || selection !== sourceSelection || activeDataset.value !== id) return
    datasetSeries.value = result.series || []; seriesNextOffset.value = result.next_offset ?? null; seriesTotal.value = result.total || 0; selectedSeriesIds.value = []; batchErrors.value = {}; drawer.value = 'review'
  } catch (e) { if (own === generation && selection === sourceSelection) drawerError.value = workspaceErrorMessage(e) }
}
async function moreDatasetSeries() {
  if (seriesNextOffset.value === null || busy.value) return
  const own = generation, ownerEpoch = scopeEpoch; busy.value = true
  try {
    const result = await client().get<{series: Array<Record<string,any>>;total:number;next_offset:number|null}>(`/datasets/${encodeURIComponent(activeDataset.value)}/series?offset=${seriesNextOffset.value}&limit=100`)
    if (own !== generation) return
    datasetSeries.value = [...new Map([...datasetSeries.value,...result.series].map(series => [series.series_id,series])).values()]
    seriesNextOffset.value = result.next_offset ?? null; seriesTotal.value = result.total
  } catch (e) { if (own === generation) drawerError.value = workspaceErrorMessage(e) }
  finally { if (ownerEpoch === scopeEpoch) busy.value = false }
}
function selectSeriesPage() { selectedSeriesIds.value = datasetSeries.value.map(series => series.series_id) }
function previewSeriesUnit(preview: Record<string,any>) { const item = preview.series?.[0] || preview.series || {}; return item.unit || preview.source_unit || '' }
async function confirmSelectedSeries() {
  if (busy.value || !batchRuleConfirmed.value || !timeType.value || !entryTime.value || !importNote.value.trim() || !selectedSeriesIds.value.length) return
  const own = generation, ownerEpoch = scopeEpoch; busy.value = true; batchErrors.value = {}; drawerError.value = ''
  const selected = datasetSeries.value.filter(series => selectedSeriesIds.value.includes(series.series_id))
  try {
    for (const [index,series] of selected.entries()) {
      if (own !== generation) return
      batchProgress.value = `逐项核验 ${index+1} / ${selected.length}`
      try {
        const preview = await client().post<Record<string,any>>('/imports/preview', {dataset_id:activeDataset.value,series_id:series.series_id,source_role:'observations'})
        const unit = previewSeriesUnit(preview)
        await client().post('/imports/confirm', {batch_id:preview.batch_id,source_unit:unit,target_unit:unit,factor:1,caliber:{time_type:timeType.value},default_entry_time:entryTime.value,source_role:'observations',confirmed:true,future_dates_confirmed:false,note:importNote.value.trim()})
        if (own !== generation) return
        selectedSeriesIds.value = selectedSeriesIds.value.filter(id => id !== series.series_id)
      } catch (e) { if (own !== generation) return; batchErrors.value = {...batchErrors.value,[series.series_id]:workspaceErrorMessage(e)} }
    }
    if (own !== generation) return
    await load(); drawer.value = 'review'
    MessagePlugin.info(Object.keys(batchErrors.value).length ? '成功项已保存；失败项保留选择，可只重试失败项。' : '已按确认规则逐项保存指标。')
  } finally { if (ownerEpoch === scopeEpoch) { busy.value = false; batchProgress.value = '' } }
}
async function previewSeries(series: Record<string, any>) {
  if (busy.value) return
  const own = generation, ownerEpoch = scopeEpoch; busy.value = true; drawerError.value = ''; timeType.value = ''; entryTime.value = ''; futureConfirmed.value = false
  try {
    const result = await client().post<Record<string, any>>('/imports/preview', { dataset_id: activeDataset.value, series_id: series.series_id })
    if (own === generation) researchPreview.value = result
  } catch (e) { if (own === generation) drawerError.value = workspaceErrorMessage(e) }
  finally { if (ownerEpoch === scopeEpoch) busy.value = false }
}
async function confirmSeries() {
  if (busy.value || !researchPreview.value || !timeType.value || !entryTime.value || !importNote.value.trim() || (researchPreview.value.future_numeric_count > 0 && !futureConfirmed.value)) return
  const own = generation, ownerEpoch = scopeEpoch; busy.value = true; drawerError.value = ''
  try {
    const preview = researchPreview.value
    const unit = previewSeriesUnit(preview)
    await client().post('/imports/confirm', { batch_id: preview.batch_id, source_unit: unit, target_unit: unit, factor: 1, caliber: { time_type: timeType.value }, confirmed: true, future_dates_confirmed: futureConfirmed.value, source_role: 'observations', default_entry_time: entryTime.value, note: importNote.value.trim() })
    if (own !== generation) return
    researchPreview.value = null
    await load(); drawer.value = 'review'
    MessagePlugin.success('已添加已确认指标；未核验的未来周期保留待核。')
  } catch (e) { if (own === generation) drawerError.value = workspaceErrorMessage(e) }
  finally { if (ownerEpoch === scopeEpoch) busy.value = false }
}
async function loadRecords(append=false) {
  const endpoint=editableEndpoint.value,own=generation,selected=module.value,request=++recordSelection,offset=append?records.value.length:0
  if(!endpoint)return
  try {
    const result=await client().get<{items?:Array<Record<string,any>>;records?:Array<Record<string,any>>;total?:number;truncated?:boolean;[key:string]:unknown}>(`/${endpoint}${append?'?offset='+offset+'&limit=100':''}`)
    if(own!==generation||selected!==module.value||request!==recordSelection)return
    const items=result.items||result.records||(Array.isArray(result[endpoint])?result[endpoint] as Array<Record<string,any>>:[])
    records.value=append?[...new Map([...records.value,...items].map(item=>[item.id,item])).values()]:items
    recordTotal.value=result.total??records.value.length;recordNextOffset.value=result.truncated?offset+items.length:null
  }catch(e){if(own===generation&&selected===module.value&&request===recordSelection)error.value=workspaceErrorMessage(e)}
}
async function moreSources(){
  if(busy.value||sources.value.length>=sourceTotal.value)return
  const own=generation,ownerEpoch=scopeEpoch;busy.value=true
  try{
    const result=await client().get<{knowledge_base_id:string;sources:WorkbenchSource[];total:number}>(`/sources?page=${sourcePage.value+1}&page_size=100`)
    if(own!==generation)return
    if(result.knowledge_base_id!==props.workbench.knowledge_base_id)throw new Error('资料范围不匹配，已停止显示。')
    sources.value=[...new Map([...sources.value,...result.sources].map(source=>[source.id,source])).values()];sourceTotal.value=result.total;sourcePage.value++
  }catch(e){if(own===generation)drawerError.value=workspaceErrorMessage(e)}finally{if(ownerEpoch===scopeEpoch)busy.value=false}
}
function editRecord(record?: Record<string, any>) { if (!record || typeof record.id !== 'string') record = undefined; editingRecord.value = record || null; recordTitle.value = record?.title || ''; recordBody.value = record ? (module.value === 'structure' ? structureDetails(record as ResearchArtifact).text : record.body || '') : ''; recordParent.value = record && module.value === 'structure' ? structureDetails(record as ResearchArtifact).parent_id : ''; recordSources.value = record?.source_ids ? [...record.source_ids] : []; recordConfirmed.value = record?.status === 'confirmed'; drawer.value = 'edit'; drawerError.value = '' }
async function saveRecord() {
  if (busy.value || !editableEndpoint.value) return
  const title = recordTitle.value.trim(), body = recordBody.value.trim()
  if (!title) { drawerError.value = '请填写简短标题。'; return }
  const own = generation, ownerEpoch = scopeEpoch; busy.value = true; drawerError.value = ''
  try {
    const payload = { title, body: module.value === 'structure' ? structureBody(body, recordParent.value) : body, status: recordConfirmed.value ? 'confirmed' : 'draft', confirmed: recordConfirmed.value, source_ids: [...recordSources.value] }
    if (editingRecord.value) await client().patch(`/${editableEndpoint.value}/${encodeURIComponent(editingRecord.value.id)}`, { ...payload, revision: editingRecord.value.revision })
    else await client().post(`/${editableEndpoint.value}`, payload)
    if (own !== generation) return
    drawer.value = null; await loadRecords()
    const updated = await updateResearchWorkbenchOnboarding(props.workbench.id, { tasks: { research: true } })
    if (own === generation) emit('update-workbench', updated.data)
  } catch (e) { if (own === generation) drawerError.value = workspaceErrorMessage(e) }
  finally { if (ownerEpoch === scopeEpoch) busy.value = false }
}
function moduleStorageKey() { const own = identity(); return `research-workbench-layout:${own.tenantId}:${own.userId}:${own.workbenchId}` }
function saveModuleLayout() { try { localStorage.setItem(moduleStorageKey(), JSON.stringify(extraModules.value)) } catch { /* Layout persistence is optional; research remains on the server. */ } }
function addModule(id: string) { if (!allModules.value.includes(id)) extraModules.value.push(id); saveModuleLayout(); modulePicker.value = false; selectModule(id) }
async function moreIndicators() {
  if (indicatorNext.value === null || busy.value) return
  const own = generation, ownerEpoch = scopeEpoch; busy.value = true
  try {
    const result = await client().get<{indicators:WorkbenchIndicator[];total:number;next_offset:number|null}>(`/indicators?offset=${indicatorNext.value}&limit=100`)
    if (own !== generation) return
    indicators.value = [...new Map([...indicators.value,...result.indicators].map(indicator => [indicator.series_id,indicator])).values()]
    indicatorNext.value = result.next_offset ?? null; indicatorTotal.value = result.total
  } catch (e) { if (own === generation) error.value = workspaceErrorMessage(e) }
  finally { if (ownerEpoch === scopeEpoch) busy.value = false }
}
async function showObservations(indicator: WorkbenchIndicator, offset = 0) {
  const own = generation, chartScope = chartModuleEpoch, selection = ++chartRequest, selectedModule = module.value
  const id=indicator.series_id; chartRequests.set(id,selection);error.value=''
  desiredCharts.value=selectedModule==='compare'?[...desiredCharts.value.filter(key=>key!==id),id].slice(-2):[id]
  observationPanels.value=observationPanels.value.filter(panel=>desiredCharts.value.includes(panel.indicator.series_id))
  try {
    const result=await client().get<{indicator:WorkbenchIndicator;observations:Array<Record<string,any>>;total:number;next_offset:number|null}>(`/indicators/${encodeURIComponent(id)}/observations?offset=${offset}&limit=100`)
    if(own!==generation||chartScope!==chartModuleEpoch||selectedModule!==module.value||chartRequests.get(id)!==selection||!desiredCharts.value.includes(id))return
    const byId=new Map([...observationPanels.value,{...result,offset}].map(panel=>[panel.indicator.series_id,panel]))
    observationPanels.value=desiredCharts.value.map(key=>byId.get(key)).filter((panel):panel is NonNullable<typeof panel>=>Boolean(panel))
  }catch(e){if(own===generation&&chartScope===chartModuleEpoch&&chartRequests.get(id)===selection)error.value=workspaceErrorMessage(e)}
}
function observationDate(observation:Record<string,any>,indicator:WorkbenchIndicator){return indicator.caliber?.time_type==='原表日期'?(observation.sources?.find((source:any)=>source.locator?.date)?.locator.date||observation.as_of):observation.as_of}
function downloadText(name: string, text: string, type: string) {
  const url = URL.createObjectURL(new Blob([text], { type })), link = document.createElement('a')
  link.href = url; link.download = name; link.click(); setTimeout(() => URL.revokeObjectURL(url), 1000)
}
async function allRecords<T>(api:ReturnType<typeof client>,endpoint:string,key:string,kind:'offset'|'page'='offset'):Promise<T[]> {
  const items:T[]=[];let offset=0,page=1
  for(let round=0;round<200;round++){
    const result=await api.get<Record<string,any>>(`${endpoint}?${kind==='page'?'page='+page+'&page_size=100':'offset='+offset+'&limit=200'}`)
    const rows=result[key]
    if(!Array.isArray(rows))throw new Error('导出列表格式不完整，未生成部分文件。')
    items.push(...rows)
    const hasMore=result.truncated===true||(typeof result.total==='number'&&items.length<result.total)||typeof result.next_offset==='number'
    if(!hasMore)return items
    if(!rows.length)throw new Error('资料分页未完整返回，请刷新后重试导出。')
    offset=typeof result.next_offset==='number'?result.next_offset:offset+rows.length;page++
  }
  throw new Error('资料数量较多，请分工作台导出；未生成不完整文件。')
}
async function exportResearch(asTemplate=false){
  if(busy.value)return
  const own=generation,ownerEpoch=scopeEpoch;busy.value=true;error.value=''
  try{
    const api=client(),endpoints=asTemplate?['structure']:['structure','judgments','notes','briefs','strategies']
    const groups=await Promise.all(endpoints.map(endpoint=>allRecords<ResearchArtifact>(api,`/${endpoint}`,'items')))
    if(own!==generation)return
    if(asTemplate){downloadText('研究工作台模板.json',JSON.stringify(reusableTemplate(props.workbench.template_id||'blank',allModules.value,groups[0]||[]),null,2),'application/json');return}
    const [allSources,allIndicators]=await Promise.all([allRecords<WorkbenchSource>(api,'/sources','sources','page'),allRecords<WorkbenchIndicator>(api,'/indicators','indicators')])
    if(own!==generation)return
    const lines=[`# ${props.workbench.name}`,'',`知识库：${props.workbench.knowledge_base_name||props.workbench.knowledge_base_id}`,`导出时间：${new Date().toISOString()}`,'','## 指标口径',...allIndicators.map(item=>`- ${item.name} · 单位：${item.unit||'待核'} · 频率：${item.frequency||'待核'}`),'','## 来源原件',...allSources.map(source=>`- ${sourceLabel(source)} · ${sourceStatus(source)} · 来源标识：${source.id} · 原件版本：${source.file_hash||source.sha256||'待核'}`)]
    groups.forEach((items,index)=>{lines.push('',`## ${workbenchModuleNames[['structure','judgment','notes','brief','strategies'][index]!]}`,'');items.forEach(item=>lines.push(`### ${item.title}`,`状态：${item.status||'draft'} · 版本：${item.revision||1}`,'',index===0?structureDetails(item).text:item.body||'','',`来源：${item.source_ids?.join('、')||'未引用；草稿待补证据'}`,''))})
    downloadText('研究工作台.md',lines.join('\n'),'text/markdown;charset=utf-8')
  }catch(e){if(own===generation)error.value=workspaceErrorMessage(e)}finally{if(ownerEpoch===scopeEpoch)busy.value=false}
}
function datasetName(item: WorkbenchDataset) { return item.file_name || item.filename || item.name || item.dataset_id || item.id || '结构化原件' }
function sourceLabel(source: WorkbenchSource) { return source.folder_path ? `${source.folder_path}/${source.file_name}` : source.file_name }
function isWorkbook(source: WorkbenchSource) { return ['xlsx','xls'].includes(source.file_name.split('.').at(-1)?.toLowerCase() || '') }
function sourceStatus(source: WorkbenchSource) { const value = source.parse_status || 'pending'; return ({ completed: '文档已解析', stored: isWorkbook(source) ? '待结构化解析' : '原件已保存', pending: '等待处理', processing: '处理中', failed: '处理失败' } as Record<string, string>)[value] || value }
watch(recordSources, ids => { if (!ids.length) recordConfirmed.value = false })
watch(scopeKey, () => { scopeEpoch++; sourceSelection++; recordSelection++; chartModuleEpoch++; chartRequests.clear(); desiredCharts.value = []; generation++; overview.value = null; sources.value = []; sourceTotal.value = 0; sourcePage.value = 1; indicators.value = []; indicatorNext.value = null; indicatorTotal.value = 0; indicatorFilter.value = ''; records.value = []; recordTotal.value = 0; recordNextOffset.value = null; drawer.value = null; selectedSource.value = null; sourceContent.value = null; sourceContentOpen.value = false; sourceContentLoading.value = false; pendingFiles.value = []; observationPanels.value = []; extraModules.value = []; try { const saved = JSON.parse(localStorage.getItem(moduleStorageKey()) || '[]'); if (Array.isArray(saved)) extraModules.value = saved.filter((id: unknown): id is string => typeof id === 'string' && Boolean(workbenchModuleNames[id])) } catch {} importList.value = []; activeImport.value = null; mapping.value = null; sheetEvidence.value = null; columnPages.value = {}; metadataChoices.value = {}; futureConfirmed.value = false; datasetSeries.value = []; researchPreview.value = null; selectedSeriesIds.value = []; batchErrors.value = {}; batchProgress.value = ''; batchRuleConfirmed.value = false; seriesNextOffset.value = null; seriesTotal.value = 0; scanStage.value = ''; busy.value = false; const own = generation; void nextTick(() => { if (own === generation) void load() }) }, { immediate: true, flush: 'sync' })
onMounted(() => window.addEventListener('knowledgeFileUploaded', filesChanged))
onBeforeUnmount(() => { generation++; if (refreshTimer) clearTimeout(refreshTimer); window.removeEventListener('knowledgeFileUploaded', filesChanged) })
defineExpose({ openSources, openGuide })
</script>

<template>
  <div class="isolated-research" data-testid="isolated-research-workbench">
    <aside class="isolated-nav" aria-label="工作台功能">
      <button class="isolated-nav__item" @click="emit('open-chat')">
<t-icon name="chat" />问答助手</button>
      <div class="isolated-nav__label">功能<span>{{ template.name }}</span>
</div>
      <button class="isolated-nav__item" :class="{ active: module === 'overview' }" @click="selectModule('overview')">
<t-icon name="dashboard" />总览</button>
      <button v-for="id in moduleList" :key="id" class="isolated-nav__item" :class="{ active: module === id }" @click="selectModule(id)">{{ workbenchModuleNames[id] || id }}</button>
      <button class="isolated-nav__item" @click="modulePicker = !modulePicker">＋ 添加功能</button>
<div v-if="modulePicker" class="isolated-module-picker">
<button v-for="(label,id) in workbenchModuleNames" :key="id" @click="addModule(String(id))">{{ label }}</button>
</div>
<div class="isolated-nav__scope">仅绑定知识库<br>{{ workbench.knowledge_base_name || '当前知识库' }}</div>
    </aside>
    <section class="isolated-content">
      <div class="isolated-status">
<span class="isolated-status__dot">
</span>
<span>{{ readinessText }}</span>
<t-button size="small" variant="text" @click="openReview">资料准备</t-button>
<t-button size="small" variant="text" @click="load" :loading="loading">刷新</t-button>
</div>
      <section v-if="guideVisible" class="isolated-guide" aria-label="首次研究引导">
        <div class="isolated-guide__header">
<strong>开始研究</strong>
<span>随时收起，进度保留</span>
<t-button size="small" variant="text" :disabled="busy" @click="guide('dismissed')">稍后继续</t-button>
<t-button size="small" variant="text" :disabled="busy" @click="guide('skipped')">跳过</t-button>
</div>
        <div class="isolated-guide__tasks">
<div v-for="(label,id,index) in taskNames" :key="id" class="isolated-guide__task">
<span class="isolated-guide__number">{{ overview?.readiness.tasks.find(task => task.id === id)?.status === 'ready' ? '✓' : index + 1 }}</span>
<div>
<strong>{{ label }}</strong>
<small>{{ id === 'sources' ? '保留原件与文件夹层级' : id === 'review' ? '确认用途、表头与口径' : '建立结构与自己的判断' }}</small>
<t-button size="small" variant="text" @click="id === 'sources' ? openSources() : id === 'review' ? openReview() : selectModule(template.modules.includes('structure' as never) ? 'structure' : 'notes')">{{ id === 'sources' ? '添加 / 查看资料' : id === 'review' ? '开始核对' : '开始整理' }}</t-button>
</div>
</div>
</div>
      </section>
      <t-alert v-if="error" theme="error" :message="error" class="isolated-error" />
      <div v-if="loading && !overview" class="isolated-loading">
<t-loading text="正在读取本工作台资料" />
</div>
      <template v-else-if="overview">
        <template v-if="module === 'overview'">
          <div class="isolated-counts">
<div>
<span>原件</span>
<strong>{{ overview.counts.sources }}<small> 份</small>
</strong>
</div>
<div>
<span>已解析文档</span>
<strong>{{ overview.counts.documents_ready }}<small> 份</small>
</strong>
</div>
<div>
<span>待结构化 Excel</span>
<strong>{{ overview.counts.excel_pending }}<small> 份</small>
</strong>
</div>
<div>
<span>已确认指标</span>
<strong>{{ overview.counts.indicators }}<small> 个</small>
</strong>
</div>
</div>
          <div class="isolated-grid">
<section class="isolated-panel">
<header>
<h2>指标与图表</h2>
<t-button size="small" variant="text" @click="selectModule('data')">查看</t-button>
</header>
<div v-if="!indicators.length" class="isolated-empty">
<strong>暂无已确认指标</strong>
<p>导入自己的表格，核对来源后再出图。</p>
<t-button size="small" variant="outline" @click="openReview">准备指标来源</t-button>
</div>
<div v-else class="isolated-list">
<button v-for="indicator in indicators.slice(0,5)" :key="indicator.series_id" @click="selectModule('data')">
<span>{{ indicator.name }}</span>
<small>{{ indicator.unit || '单位待核' }} · {{ indicator.frequency || '频率待核' }}</small>
</button>
</div>
<footer>保留原件、工作表与单元格引用。</footer>
</section>
          <section class="isolated-panel">
<header>
<h2>研究结构与判断</h2>
<span>{{ template.name }}</span>
</header>
<div class="isolated-empty">
<strong>从证据建立自己的研究</strong>
<p>方法文档提供候选结构，历史观点需要重新确认。</p>
<div class="isolated-actions">
<t-button size="small" variant="outline" @click="selectModule('structure')">整理研究结构</t-button>
<t-button size="small" variant="text" @click="selectModule('judgment')">写研究判断</t-button>
</div>
</div>
<footer>人工编辑与新资料版本分别保留。</footer>
</section>
          <section class="isolated-panel isolated-grid__full">
<header>
<h2>资料与来源</h2>
<div class="isolated-actions">
<KbUploadSourceDropdown unified :supported-file-types="uploadTypes" trigger-label="添加资料" @files="selectFiles" />
<t-button size="small" variant="text" @click="openSources">全部来源</t-button>
</div>
</header>
<div v-if="!sources.length" class="isolated-empty isolated-empty--small">
<p>知识库还没有原件。可以上传文件或整个文件夹，之后在此继续准备。</p>
</div>
<button v-for="source in sources.slice(0,4)" :key="source.id" class="isolated-source-row" @click="showSource(source)">
<t-icon name="file" />
<span>{{ sourceLabel(source) }}</span>
<small>{{ sourceStatus(source) }}</small>
<t-icon name="chevron-right" />
</button>
</section>
</div>
        </template>
        <section v-else-if="module === 'data' || module === 'trends' || module === 'compare'" class="isolated-panel">
<header>
<h2>{{ workbenchModuleNames[module] }}</h2>
<div class="isolated-actions">
<input v-model="indicatorFilter" class="isolated-indicator-filter" aria-label="筛选已加载指标" placeholder="筛选指标" />
<t-button size="small" variant="outline" @click="openReview">添加指标</t-button>
</div>
</header>
<div v-if="!indicators.length" class="isolated-empty">
<strong>先选择已确认来源</strong>
<p>保留单位、频率、日期和原件引用；未核验的口径不进入结论。</p>
<t-button size="small" @click="openReview">资料准备</t-button>
</div>
<div class="isolated-series-list" :class="{ 'isolated-series-list--with-chart': observationPanels.length }">
<div v-for="indicator in visibleIndicators" :key="indicator.series_id" class="isolated-data-row">
<div>
<strong>{{ indicator.name }}</strong>
<small>{{ indicator.unit || '单位待核' }} · {{ indicator.frequency || '频率待核' }}</small>
</div>
<div class="isolated-actions">
<t-button size="small" variant="text" @click="showObservations(indicator)">{{ module === 'compare' ? '加入对比' : '查看趋势' }}</t-button>
<t-button size="small" variant="text" @click="openReview">来源口径</t-button>
</div>
</div>
<div v-if="indicators.length" class="isolated-actions isolated-pagination">
<span>已加载 {{ indicators.length }} / {{ indicatorTotal }} 个指标</span>
<t-button v-if="indicatorNext !== null" size="small" variant="text" :loading="busy" @click="moreIndicators">加载更多</t-button>
</div>
</div>
<section v-for="panel in observationPanels" :key="panel.indicator.series_id" class="isolated-observation-panel">
<div class="isolated-actions">
<h3>{{ panel.indicator.name }}</h3>
<small>单位 {{ panel.indicator.unit || '待核' }} · 本页 {{ panel.observations.length }} / {{ panel.total }} 条</small>
<t-button v-if="panel.next_offset !== null" size="small" variant="text" @click="showObservations(panel.indicator, panel.next_offset)">较早记录</t-button>
</div>
<p class="isolated-hint">展示本页真实观测，不代表完整历史。对比按来源单位分别绘图，不跨单位混算。</p>
<svg v-if="panel.observations.length" class="isolated-chart" viewBox="0 0 580 150" role="img" :aria-label="`${panel.indicator.name}本页真实观测趋势`">
<line x1="10" y1="140" x2="570" y2="140" class="isolated-chart__axis" />
<polyline v-for="(points,index) in observationChart(panel.observations).segments" :key="index" :points="points" fill="none" class="isolated-chart__line" />
</svg>
<div class="isolated-actions isolated-hint">
<span>{{ panel.observations[0] ? observationDate(panel.observations[0],panel.indicator) : '暂无时点' }}</span>
<span>至 {{ panel.observations.length ? observationDate(panel.observations.at(-1)!,panel.indicator) : '—' }}</span>
</div>
<table class="isolated-observation-table">
<thead>
<tr>
<th>观测时点</th>
<th>值</th>
<th>原件 / 工作表 / 单元格</th>
</tr>
</thead>
<tbody>
<tr v-for="observation in panel.observations.slice(-6)" :key="observation.observation_id">
<td>{{ observationDate(observation,panel.indicator) }}<small v-if="panel.indicator.caliber?.time_type === '原表日期'">存储时点 {{ observation.as_of }}</small>
</td>
<td>{{ observation.value ?? '缺失' }}</td>
<td>
<div v-for="source in observation.sources" :key="source.artifact_id">{{ source.locator?.filename || '原件' }} · {{ source.locator?.sheet || '—' }} · {{ source.locator?.value_cell || '—' }}<small>{{ source.sha256?.slice(0,12) }} · {{ source.locator?.value_status }}</small>
</div>
</td>
</tr>
</tbody>
</table>
</section>
</section>
        <section v-else-if="module === 'sources' || module === 'evidence' || module === 'freshness' || module === 'monitoring'" class="isolated-panel">
<header>
<h2>{{ workbenchModuleNames[module] }}</h2>
<KbUploadSourceDropdown unified :supported-file-types="uploadTypes" trigger-label="补充资料" @files="selectFiles" />
</header>
<div v-if="!sources.length" class="isolated-empty">
<p>仅使用绑定知识库的资料。</p>
<t-button size="small" @click="openSources">添加资料</t-button>
</div>
<button v-for="source in sources" :key="source.id" class="isolated-source-row" @click="showSource(source)">
<t-icon name="file" />
<span>{{ sourceLabel(source) }}</span>
<small>{{ sourceStatus(source) }}</small>
</button>
</section>
        <section v-else-if="editableEndpoint" class="isolated-panel">
<header>
<h2>{{ workbenchModuleNames[module] }}</h2>
<t-button size="small" @click="editRecord()">新建{{ module === 'structure' ? '节点' : module === 'brief' ? '草稿' : '记录' }}</t-button>
</header>
<div v-if="!records.length" class="isolated-empty">
<strong>{{ module === 'strategies' ? '先明确研究判断与风险条件' : '从空白开始' }}</strong>
<p>{{ module === 'brief' ? '简报从自己的资料和判断形成，草稿可修改。' : module === 'judgment' ? '事实、观点和时点分别确认；历史文档观点不会自动成为当前结论。' : '可手工建立，也可先从自己的方法资料整理候选结构。' }}</p>
<div class="isolated-actions">
<t-button size="small" variant="outline" @click="openSources">查看证据</t-button>
<t-button size="small" variant="text" @click="editRecord()">开始整理</t-button>
</div>
</div>
<div v-if="module === 'structure' && records.length" class="isolated-structure-tree" aria-label="研究结构树">
<div v-for="node in structureNodes" :key="node.id" class="isolated-structure-node" :style="{ marginLeft: `${Math.min(node.depth,6) * 16}px` }">
<span>◇</span>
<div>
<strong>{{ node.title }}</strong>
<small>{{ node.text || '空节点，可继续编辑' }}</small>
</div>
<t-button size="small" variant="text" @click="editRecord(node)">编辑</t-button>
</div>
</div>
<article v-for="record in module === 'structure' ? [] : records" :key="record.id" class="isolated-record">
<div>
<strong>{{ record.title }}</strong>
<div class="isolated-actions">
<span>{{ record.status === 'draft' ? '草稿' : '已确认' }} · v{{ record.revision }}</span>
<t-button size="small" variant="text" @click="editRecord(record)">编辑</t-button>
</div>
</div>
<p>{{ record.body || record.content || '暂无正文' }}</p>
<small v-if="record.source_ids?.length">{{ record.source_ids.length }} 个来源引用</small>
</article>
<div v-if="recordTotal" class="isolated-actions isolated-pagination">
<span>已加载 {{ records.length }} / {{ recordTotal }} 条</span>
<t-button v-if="recordNextOffset !== null" size="small" variant="text" @click="loadRecords(true)">加载更多</t-button>
</div>
</section>
        <section v-else class="isolated-panel">
<header>
<h2>{{ workbenchModuleNames[module] || '研究配置' }}</h2>
</header>
<div class="isolated-empty">
<p>导出本工作台已确认的资料与人工草稿，未确认来源不作为结论输出。</p>
<div class="isolated-actions">
<t-button size="small" variant="outline" :loading="busy" @click="exportResearch(false)">导出研究 Markdown</t-button>
<t-button size="small" variant="outline" :loading="busy" @click="exportResearch(true)">导出模板结构 JSON</t-button>
</div>
</div>
</section>
      </template>
    </section>
    <aside v-if="drawer" class="isolated-drawer" aria-label="资料与研究侧栏">
<header>
<h2>{{ drawer === 'review' ? '资料准备与核对' : drawer === 'evidence' ? '来源证据' : drawer === 'upload' ? '确认添加资料' : drawer === 'edit' ? `新建${workbenchModuleNames[module]}` : '资料与来源' }}</h2>
<t-button size="small" variant="text" aria-label="关闭资料侧栏" @click="drawer = null">
<t-icon name="close" />
</t-button>
</header>
<div class="isolated-drawer__body">
<t-alert v-if="drawerError" theme="error" :message="drawerError" />
      <template v-if="drawer === 'sources' || drawer === 'review'">
<div class="isolated-actions">
<KbUploadSourceDropdown unified :supported-file-types="uploadTypes" trigger-label="添加资料" @files="selectFiles" />
<t-button size="small" variant="text" @click="load">刷新来源</t-button>
</div>
<p class="isolated-hint">原件归属绑定知识库；Excel 需确认结构，文档按知识库规则解析。</p>
<div v-if="!sources.length" class="isolated-note">尚无原件，可以先添加文件或文件夹。</div>
<div v-for="source in sources" :key="source.id" class="isolated-review-row">
<div>
<strong>{{ sourceLabel(source) }}</strong>
<small>{{ sourceStatus(source) }}</small>
</div>
<select class="isolated-role-select" :value="source.source_role || ''" aria-label="资料用途" :disabled="busy" @change="saveSourceRole(source, ($event.target as HTMLSelectElement).value)">
<option value="">资料用途</option>
<option value="observations">真实观测</option>
<option value="acquisition-plan">取数需求</option>
<option value="method">方法 / 逻辑</option>
<option value="specification">产品规范</option>
</select>
<t-button v-if="source.parse_status === 'stored' && isWorkbook(source)" size="small" variant="text" :loading="busy" :disabled="source.source_role !== 'observations'" @click="scanSource(source)">结构化准备</t-button>
<t-button size="small" variant="text" @click="showSource(source)">证据</t-button>
</div>
<template v-if="importList.length">
<h3 class="isolated-section-title">工作表识别与确认</h3>
<div v-for="scan in importList" :key="scan.import_id" class="isolated-review-row">
<div>
<strong>{{ scan.source_file || scan.filename || scan.file_name || scan.import_id }}</strong>
<small>{{ Array.isArray(scan.sheets) ? `${scan.sheets.length} 张工作表 · ` : '' }}保留原表元数据</small>
</div>
<t-button size="small" variant="text" @click="openImport(scan.import_id)">核对结构</t-button>
</div>
</template>
<p v-if="scanStage" class="isolated-hint" role="status">{{ scanStage }}</p>
      <section v-if="activeImport" class="isolated-mapping">
<div class="isolated-actions">
<h3>工作表核对</h3>
<select :value="mapping?.sheet || sheetEvidence?.name || ''" aria-label="选择工作表" @change="chooseSheet(($event.target as HTMLSelectElement).value)">
<option v-for="sheet in activeImport.sheets" :key="sheet.name" :value="sheet.name">{{ sheet.name }} · {{ sheet.status }}</option>
</select>
</div>
<label class="isolated-field">
<span>用途</span>
<select v-model="sourceRole" @change="reviewRoleConfirmed = false">
<option value="">请选择资料用途</option>
<option value="observations">真实观测表</option>
<option value="acquisition-plan">取数需求 / 指标清单</option>
<option value="method">研究方法 / 逻辑</option>
<option value="specification">产品规范</option>
</select>
</label>
<label class="isolated-confirm-line">
<input v-model="reviewRoleConfirmed" type="checkbox" />已核对用途，清单中的“已有”不代表本库已有观测</label>
<div v-for="(block,index) in mapping?.blocks || []" :key="index" class="isolated-mapping-block">
<div class="isolated-actions">
<strong>区块 {{ Number(index) + 1 }}</strong>
<label>观测开始行 <input v-model.number="block.data_start_row" @input="mappingPreview = null" type="number" min="1" aria-label="观测开始行" />
</label>
</div>
<div class="isolated-hint">日期列 {{ block.date_column || '未指定' }} · 日期口径 {{ block.date_mode || '待核' }} · {{ block.columns?.length || 0 }} 列；顶部行作为元数据保留</div>
<details class="isolated-header-editor">
<summary>校正表头与字段</summary>
<div class="isolated-raw-header">
<table>
<tbody>
<tr v-for="(row,rowIndex) in headerPreview(block,Number(index))" :key="rowIndex">
<td v-for="cell in row" :key="cell.cell" :title="String(cell.value ?? '')">
<small>{{ cell.cell }}</small>{{ cell.value ?? '空' }}</td>
</tr>
</tbody>
</table>
</div>
<p class="isolated-hint">原件顶部实际样本；当前页前 4 个指标列和日期列，可横向查看。</p>
<div class="isolated-header-controls">
<label>日期列<input v-model="block.date_column" maxlength="3" @input="mappingPreview = null" @change="block.date_column = String(block.date_column).trim().toUpperCase()" />
</label>
<label>表头行<input :value="block.header_rows?.join(',')" @change="setHeaders(block,($event.target as HTMLInputElement).value)" placeholder="行号，以逗号分隔" />
</label>
<label>结束行<input v-model.number="block.data_end_row" type="number" min="1" @input="mappingPreview = null" @change="normalizeEndRow(block)" />
</label>
</div>
<div class="isolated-header-controls">
<label>名称行<select v-model="metadataRows(Number(index)).nameRow">
<option value="">不批量修改</option>
<option v-for="row in block.header_rows" :key="row" :value="String(row)">第 {{ row }} 行</option>
</select>
</label>
<label>单位行<select v-model="metadataRows(Number(index)).unitRow">
<option value="">不批量修改</option>
<option v-for="row in block.header_rows" :key="row" :value="String(row)">第 {{ row }} 行</option>
</select>
</label>
<label>频率行<select v-model="metadataRows(Number(index)).frequencyRow">
<option value="">不批量修改</option>
<option v-for="row in block.header_rows" :key="row" :value="String(row)">第 {{ row }} 行</option>
</select>
</label>
<t-button size="small" variant="outline" @click="applyHeaders(Number(index))">应用到此区块</t-button>
</div>
<p class="isolated-hint">显式选择原件元数据行后应用。空单位、频率保留待核，不提供常识默认；来源单元格保留。</p>
</details>
<table class="isolated-column-editor">
<thead>
<tr>
<th>原列</th>
<th>字段名</th>
<th>单位</th>
<th>频率</th>
</tr>
</thead>
<tbody>
<tr v-for="column in blockColumns(block,Number(index))" :key="column.column">
<td :title="column.header_cells?.join('、')">{{ column.column }}</td>
<td>
<input v-model="column.metric" aria-label="真实字段名称" @input="mappingPreview = null" />
</td>
<td>
<input v-model="column.unit" aria-label="原件单位" placeholder="待核" @input="mappingPreview = null" @change="column.unit = column.unit?.trim() || null" />
</td>
<td :title="column.frequency_cells?.join('、')">
<input v-model="column.frequency" aria-label="原件频率" placeholder="待核" @input="mappingPreview = null" />
</td>
</tr>
</tbody>
</table>
<div class="isolated-actions isolated-column-pages">
<span>{{ (columnPages[Number(index)] || 0) * 12 + 1 }}–{{ Math.min(((columnPages[Number(index)] || 0) + 1) * 12,block.columns?.length || 0) }} / {{ block.columns?.length || 0 }} 列</span>
<t-button size="small" variant="text" :disabled="!(columnPages[Number(index)] || 0)" @click="setColumnPage(Number(index),(columnPages[Number(index)] || 0)-1)">上一页</t-button>
<t-button size="small" variant="text" :disabled="((columnPages[Number(index)] || 0)+1)*12 >= (block.columns?.length || 0)" @click="setColumnPage(Number(index),(columnPages[Number(index)] || 0)+1)">下一页</t-button>
</div>
</div>
<div class="isolated-actions">
<t-button size="small" variant="outline" :loading="busy" :disabled="sourceRole !== 'observations' || !reviewRoleConfirmed" @click="checkMapping(false)">检查全部数据</t-button>
<t-button size="small" :loading="busy" :disabled="!mappingPreview || sourceRole !== 'observations' || !reviewRoleConfirmed" @click="checkMapping(true)">确认此工作表</t-button>
</div>
<template v-if="mappingPreview">
<p class="isolated-hint">检查结果：{{ mappingPreview.series_count ?? '—' }} 个序列 / {{ mappingPreview.observation_count ?? '—' }} 条观测</p>
<table class="isolated-sample">
<thead>
<tr>
<th>来源格</th>
<th>日期 / 周期</th>
<th>值</th>
</tr>
</thead>
<tbody>
<tr v-for="(sample,index) in mappingPreview.sample?.slice(0,4)" :key="index">
<td>{{ sample.source_cell }}</td>
<td>{{ sample.date || sample.period || '待核' }}</td>
<td>{{ sample.value ?? '缺失' }}</td>
</tr>
</tbody>
</table>
</template>
</section>
      <section v-if="datasetSeries.length" class="isolated-mapping">
<h3>选择研究指标</h3>
<div class="isolated-actions">
<t-button size="small" variant="text" :disabled="busy" @click="selectSeriesPage">选择已加载指标</t-button>
<span class="isolated-hint">已加载 {{ datasetSeries.length }} / {{ seriesTotal }}</span>
<t-button v-if="seriesNextOffset !== null" size="small" variant="text" :loading="busy" @click="moreDatasetSeries">加载更多</t-button>
</div>
<p class="isolated-hint">先确认来源、单位和时点口径。清单不作为观测，重复来源不自动合并。</p>
<div v-for="series in datasetSeries" :key="series.series_id" class="isolated-review-row">
<input v-model="selectedSeriesIds" type="checkbox" :value="series.series_id" :disabled="busy" :aria-label="`选择${series.metric || series.name}`" />
<div>
<strong>{{ series.metric || series.name }}</strong>
<small>{{ series.unit || '单位待核' }} · {{ series.frequency || '频率待核' }}</small>
<small v-if="batchErrors[series.series_id]" class="isolated-error-text">{{ batchErrors[series.series_id] }}</small>
</div>
<t-button size="small" variant="text" :loading="busy" @click="previewSeries(series)">核对并添加</t-button>
</div>
<div v-if="selectedSeriesIds.length" class="isolated-batch-confirm">
<div class="isolated-actions">
<label>时间口径 <select v-model="timeType">
<option value="">请选择</option>
<option value="原表日期">原表日期</option>
<option value="交易时点">交易时点</option>
<option value="发布时点">发布时点</option>
<option value="抓取时点">抓取时点</option>
</select>
</label>
<label>登记时刻 <input v-model="entryTime" type="time" aria-label="批量指标登记时刻" />
</label>
</div>
<label class="isolated-field" style="margin-top:10px">
<span>说明</span>
<input v-model="importNote" maxlength="2000" placeholder="说明来源与适用时点" />
</label>
<label class="isolated-confirm-line">
<input v-model="batchRuleConfirmed" type="checkbox" />采用各自原单位，不转换；已核对以上 {{ selectedSeriesIds.length }} 项适用相同时间口径。未来周期保留待核。</label>
<p v-if="batchProgress" class="isolated-hint" role="status">{{ batchProgress }}</p>
<t-button size="small" :disabled="!batchRuleConfirmed || !timeType || !entryTime || !importNote.trim()" :loading="busy" @click="confirmSelectedSeries">确认选中 {{ selectedSeriesIds.length }} 项</t-button>
</div>
</section>
<section v-if="researchPreview" class="isolated-mapping">
<h3>确认指标口径</h3>
<p class="isolated-hint">{{ researchPreview.series?.[0]?.name || researchPreview.series?.[0]?.metric || researchPreview.series?.name || '本次选择的真实序列' }}</p>
<label class="isolated-field">
<span>时点</span>
<select v-model="timeType">
<option value="">请选择时间口径</option>
<option value="原表日期">原表日期</option>
<option value="交易时点">交易时点</option>
<option value="发布时点">发布时点</option>
<option value="抓取时点">抓取时点</option>
</select>
</label>
<label class="isolated-field">
<span>时刻</span>
<input v-model="entryTime" type="time" required aria-label="明确资料登记时刻" />
</label>
<p class="isolated-hint">原件仅注明日期时，登记时刻用于保存，不代表发布时点。</p>
<label class="isolated-field">
<span>说明</span>
<input v-model="importNote" maxlength="2000" placeholder="说明来源与适用时点" />
</label>
<div class="isolated-note">单位沿用原件，不默认转换。时间口径需要单独确认。</div>
<div v-if="researchPreview.future_numeric_count > 0" class="isolated-future-review">
<p class="isolated-hint">有 {{ researchPreview.future_numeric_count }} 个晚于当前日期的有效数值，确认前保留待核。</p>
<table v-if="researchPreview.future_rows?.length" class="isolated-sample">
<thead>
<tr>
<th>原格</th>
<th>日期 / 周期</th>
<th>原值</th>
</tr>
</thead>
<tbody>
<tr v-for="(row,index) in researchPreview.future_rows" :key="index">
<td>{{ row.source_cell }}</td>
<td>{{ row.date || row.period }}</td>
<td>{{ row.value }}</td>
</tr>
</tbody>
</table>
<label class="isolated-confirm-line">
<input v-model="futureConfirmed" type="checkbox" />已核对原件晚于当前日期的期间标签与时间口径</label>
</div>
<t-button size="small" :disabled="!timeType || !entryTime || !importNote.trim() || (researchPreview.future_numeric_count > 0 && !futureConfirmed)" :loading="busy" @click="confirmSeries">确认并添加到工作台</t-button>
</section>
<template v-if="datasets.length">
<h3 class="isolated-section-title">本库结构化资料</h3>
<div v-for="dataset in datasets" :key="String(dataset.dataset_id || dataset.id)" class="isolated-review-row">
<div>
<strong>{{ datasetName(dataset) }}</strong>
<small>本知识库的解析记录</small>
</div>
<t-button size="small" variant="text" @click="openDataset(dataset)">核对指标</t-button>
</div>
</template>
<div v-if="sourceTotal" class="isolated-actions isolated-pagination">
<span>已加载 {{ sources.length }} / {{ sourceTotal }} 份原件</span>
<t-button v-if="sources.length < sourceTotal" size="small" variant="text" :loading="busy" @click="moreSources">加载更多来源</t-button>
</div>
<div class="isolated-note">取数清单不等于观测，规范文档不等于市场事实。重复来源、#N/A、未来周期和不明单位分别保留待核。</div>
<div class="isolated-actions">
<t-button size="small" variant="outline" @click="drawer = null">先用已确认资料</t-button>
<t-button size="small" variant="text" :disabled="busy" @click="guide('active', 'review')">记录核对进度</t-button>
</div>
</template>
      <template v-else-if="drawer === 'evidence' && selectedSource">
<div class="isolated-evidence-name">{{ sourceLabel(selectedSource) }}</div>
<dl class="isolated-evidence">
<dt>归属</dt>
<dd>{{ workbench.knowledge_base_name || '绑定知识库' }}</dd>
<dt>文件类型</dt>
<dd>{{ selectedSource.file_type || '原件' }}</dd>
<dt>处理状态</dt>
<dd>{{ sourceStatus(selectedSource) }}</dd>
<dt>来源标识</dt>
<dd>{{ selectedSource.id }}</dd>
<dt>可信范围</dt>
<dd>解析状态不代表业务口径已确认；只引用本库原件。</dd>
</dl>
<div class="isolated-content-preview">
<t-button size="small" variant="outline" :loading="sourceContentLoading" :disabled="!sourceContent?.supported" @click="sourceContentOpen = !sourceContentOpen">{{ sourceContentOpen ? '收起原文' : '查看原文' }}</t-button>
<small v-if="sourceContent?.sha256">版本 {{ sourceContent.sha256.slice(0,12) }}</small>
<p v-if="sourceContent?.notice" class="isolated-hint">{{ sourceContent.notice }}</p>
<img v-if="sourceContentOpen && sourceContent?.image_data_url" :src="sourceContent.image_data_url" alt="来源原图，关系尚待人工确认" class="isolated-source-image" />
<pre v-else-if="sourceContentOpen && sourceContent?.supported">{{ sourceContent.text }}</pre>
</div>
<div class="isolated-note">原件内容仅作为研究资料，确认口径后采用。</div>
<div class="isolated-actions">
<t-button v-if="selectedSource.parse_status === 'stored' && isWorkbook(selectedSource)" size="small" :loading="busy" @click="scanSource(selectedSource)">结构化准备</t-button>
<t-button size="small" variant="outline" @click="openReview">返回核对</t-button>
</div>
</template>
      <template v-else-if="drawer === 'upload'">
<p class="isolated-hint">添加到 {{ workbench.knowledge_base_name || '绑定知识库' }}，保留文件夹层级。Excel 先存原件，其他文档按当前知识库配置解析。</p>
<div v-for="(file,index) in pendingFiles" :key="index" class="isolated-upload-row">
<t-icon name="file" />
<span>{{ file.webkitRelativePath || file.name }}</span>
<small>{{ Math.ceil(file.size / 1024) }} KB</small>
</div>
<label class="isolated-confirm-line">
<input v-model="saveVisualOriginals" type="checkbox" />图片和压缩包仅保存原件，不触发图像模型；ZIP 需选中此项才能保存</label>
<div class="isolated-note">文档解析使用知识库已配置的模型与索引规则；本步骤不自动生成研究结论。</div>
<div class="isolated-actions">
<t-button size="small" variant="outline" @click="pendingFiles = []; drawer = null">取消</t-button>
<t-button size="small" @click="enqueueFiles">确认添加 {{ pendingFiles.length }} 个文件</t-button>
</div>
</template>
      <template v-else-if="drawer === 'edit'">
<label class="isolated-field">
<span>标题</span>
<input v-model="recordTitle" maxlength="160" placeholder="简短标题" />
</label>
<label v-if="module === 'structure'" class="isolated-field">
<span>上级</span>
<select v-model="recordParent">
<option value="">根节点</option>
<option v-for="record in records.filter(item => item.id !== editingRecord?.id)" :key="record.id" :value="record.id">{{ record.title }}</option>
</select>
</label>
<label class="isolated-field">
<span>内容</span>
<textarea v-model="recordBody" rows="3" maxlength="12000" placeholder="记录自己的研究内容" />
</label>
<div class="isolated-source-picker">
<span>引用来源（可选）</span>
<label v-for="source in sources" :key="source.id">
<input v-model="recordSources" type="checkbox" :value="source.id" />{{ sourceLabel(source) }}</label>
</div>
<label class="isolated-confirm-line">
<input v-model="recordConfirmed" type="checkbox" :disabled="!recordSources.length" />已核验事实、时点与风险，保存为已确认记录</label>
<div class="isolated-note">保存为本人工作台草稿，不产生模型调用；发布前核验事实、时点与风险条件。</div>
<div class="isolated-actions">
<t-button size="small" variant="outline" @click="drawer = null">取消</t-button>
<t-button size="small" :loading="busy" @click="saveRecord">{{ recordConfirmed ? '确认并保存' : '保存草稿' }}</t-button>
</div>
</template>
    </div>
</aside>
  </div>
</template>

<style scoped>
.isolated-research,.isolated-research *{box-sizing:border-box}.isolated-research{position:relative;display:flex;flex:1;min-width:0;min-height:0;color:var(--app-text-primary);background:var(--app-bg)}.isolated-nav{width:150px;flex:none;padding:15px 9px;display:flex;flex-direction:column;background:var(--app-sidebar-bg,var(--app-card-bg));border-right:1px solid var(--app-border-color);overflow:auto}.isolated-nav__item{display:flex;align-items:center;gap:7px;width:100%;height:32px;padding:0 9px;border:0;border-radius:6px;background:transparent;color:var(--app-text-primary);font:inherit;font-size:12px;text-align:left;cursor:pointer;flex:none;margin:2px 0}.isolated-nav__item:hover,.isolated-nav__item.active{color:var(--td-brand-color);background:var(--td-brand-color-light)}.isolated-nav__label{font-size:11px;color:var(--app-text-secondary);padding:15px 9px 6px}.isolated-nav__label span{display:block;font-size:10px;margin-top:3px}.isolated-nav__scope{margin-top:auto;padding:16px 9px 0;font-size:10px;line-height:1.7;color:var(--app-text-secondary)}.isolated-content{min-width:0;flex:1;overflow:auto;padding:16px 18px 24px}.isolated-status{display:flex;align-items:center;gap:7px;font-size:12px;padding:5px 10px;min-height:36px;background:var(--app-card-bg);border:1px solid var(--app-border-color);border-radius:6px;margin-bottom:10px;flex-wrap:wrap}.isolated-status>span:nth-child(2){flex:1;min-width:100px}.isolated-status__dot{width:6px;height:6px;border-radius:50%;background:var(--td-brand-color);flex:none}.isolated-status .t-button{height:25px;font-size:11px}.isolated-guide{background:var(--app-card-bg);border:1px solid var(--app-border-color);border-radius:7px;margin-bottom:14px}.isolated-guide__header{display:flex;align-items:center;gap:8px;padding:7px 11px;font-size:12px}.isolated-guide__header>span{font-size:11px;color:var(--app-text-secondary);flex:1}.isolated-guide__header .t-button{height:24px;padding:0 5px;font-size:11px}.isolated-guide__tasks{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:10px;padding:0 11px 11px}.isolated-guide__task{display:flex;gap:7px;border-top:1px solid var(--app-border-color);padding-top:9px;min-width:0}.isolated-guide__task strong{font-size:12px;font-weight:500}.isolated-guide__task small{display:block;color:var(--app-text-secondary);font-size:10px}.isolated-guide__task .t-button{height:23px;padding:0;font-size:11px}.isolated-guide__number{width:19px;height:19px;display:grid;place-items:center;border:1px solid var(--app-border-color);border-radius:50%;font-size:10px;flex:none;color:var(--td-brand-color)}.isolated-error{margin-bottom:12px}.isolated-loading{padding:20px}.isolated-counts{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));border:1px solid var(--app-border-color);border-radius:7px;overflow:hidden;background:var(--app-card-bg);margin-bottom:14px}.isolated-counts>div{padding:10px 12px;border-right:1px solid var(--app-border-color)}.isolated-counts>div:last-child{border:0}.isolated-counts span{display:block;font-size:11px;color:var(--app-text-secondary)}.isolated-counts strong{font-size:18px;font-weight:600}.isolated-counts small{font-size:10px;font-weight:400;color:var(--app-text-secondary)}.isolated-grid{display:grid;grid-template-columns:1fr 1fr;gap:14px}.isolated-grid__full{grid-column:1/-1}.isolated-panel{min-width:0;background:var(--app-card-bg);border:1px solid var(--app-border-color);border-radius:7px;overflow:hidden}.isolated-panel>header{display:flex;align-items:center;justify-content:space-between;gap:8px;min-height:41px;padding:7px 12px;border-bottom:1px solid var(--app-border-color)}.isolated-panel h2{font-size:13px;font-weight:500;margin:0}.isolated-panel>header>span{font-size:10px;color:var(--app-text-secondary)}.isolated-panel footer{padding:9px 12px;background:var(--app-bg);font-size:10px;color:var(--app-text-secondary);border-top:1px solid var(--app-border-color)}.isolated-empty{padding:27px 14px;text-align:center}.isolated-empty strong{font-size:12px;font-weight:500}.isolated-empty p{font-size:11px;color:var(--app-text-secondary);line-height:1.7;margin:5px 0 12px}.isolated-empty .isolated-actions{justify-content:center}.isolated-empty--small{padding:16px}.isolated-empty--small p{margin:0}.isolated-actions{display:flex;gap:6px;align-items:center;flex-wrap:wrap}.isolated-actions .t-button{font-size:11px;height:28px}.isolated-source-row{display:flex;gap:8px;align-items:center;width:100%;padding:10px 12px;border:0;border-bottom:1px solid var(--app-border-color);background:transparent;color:var(--app-text-primary);font:inherit;font-size:12px;text-align:left;cursor:pointer}.isolated-source-row:last-child{border-bottom:0}.isolated-source-row:hover{background:var(--td-brand-color-light)}.isolated-source-row>span{flex:1;overflow-wrap:anywhere;min-width:0}.isolated-source-row>small{font-size:10px;color:var(--app-text-secondary);flex:none}.isolated-source-row .t-icon{flex:none;color:var(--app-text-secondary)}.isolated-list button{display:flex;flex-direction:column;gap:3px;width:100%;padding:10px 12px;border:0;border-bottom:1px solid var(--app-border-color);font:inherit;background:none;text-align:left;color:var(--app-text-primary);cursor:pointer}.isolated-list button span{font-size:12px}.isolated-list button small{font-size:10px;color:var(--app-text-secondary)}.isolated-data-row{display:flex;gap:8px;align-items:center;justify-content:space-between;padding:11px 12px;border-bottom:1px solid var(--app-border-color)}.isolated-data-row strong{font-size:12px;font-weight:500}.isolated-data-row small{display:block;color:var(--app-text-secondary);font-size:10px;margin-top:3px}.isolated-record{padding:12px;border-bottom:1px solid var(--app-border-color)}.isolated-record:last-child{border:0}.isolated-record>div{display:flex;justify-content:space-between;gap:10px}.isolated-record strong{font-size:13px;font-weight:500}.isolated-record span,.isolated-record small{font-size:10px;color:var(--app-text-secondary)}.isolated-record p{font-size:12px;line-height:1.7;white-space:pre-wrap;overflow-wrap:anywhere;margin:7px 0;color:var(--app-text-secondary)}.isolated-drawer{position:absolute;z-index:8;right:0;top:0;bottom:0;width:min(460px,calc(100% - 24px));display:flex;flex-direction:column;background:var(--app-card-bg);border-left:1px solid var(--app-border-color);box-shadow:-8px 0 24px #20344717}.isolated-drawer>header{display:flex;align-items:center;justify-content:space-between;gap:8px;padding:12px 15px;border-bottom:1px solid var(--app-border-color)}.isolated-drawer h2{font-size:14px;font-weight:500;margin:0}.isolated-drawer__body{overflow:auto;padding:14px 15px;flex:1;min-height:0}.isolated-hint{font-size:11px;color:var(--app-text-secondary);line-height:1.7;margin:10px 0}.isolated-note{font-size:11px;color:var(--app-text-secondary);padding:10px;line-height:1.7;border:1px solid var(--app-border-color);border-radius:6px;background:var(--app-bg);margin:12px 0}.isolated-review-row{display:flex;gap:4px;align-items:center;padding:10px 0;border-bottom:1px solid var(--app-border-color)}.isolated-review-row>div{flex:1;min-width:0;overflow-wrap:anywhere}.isolated-review-row strong{font-size:12px;font-weight:400}.isolated-review-row small{display:block;font-size:10px;color:var(--app-text-secondary)}.isolated-review-row .t-button{height:25px;font-size:11px;padding:0 5px;flex:none}.isolated-section-title{font-size:12px;font-weight:500;margin:16px 0 5px}.isolated-evidence-name{font-size:13px;font-weight:500;overflow-wrap:anywhere}.isolated-evidence{display:grid;grid-template-columns:64px 1fr;gap:10px;font-size:12px;margin-top:16px}.isolated-evidence dt{color:var(--app-text-secondary)}.isolated-evidence dd{margin:0;overflow-wrap:anywhere;min-width:0}.isolated-upload-row{display:flex;align-items:center;gap:8px;border-bottom:1px solid var(--app-border-color);padding:9px 0;font-size:11px}.isolated-upload-row span{flex:1;min-width:0;overflow-wrap:anywhere}.isolated-upload-row small{flex:none;color:var(--app-text-secondary)}.isolated-field{display:flex;gap:10px;align-items:flex-start;margin-bottom:12px;font-size:12px}.isolated-field>span{width:33px;flex:none;padding-top:6px}.isolated-field input,.isolated-field textarea{min-width:0;flex:1;border:1px solid var(--app-border-color);border-radius:6px;background:var(--app-card-bg);color:var(--app-text-primary);font:inherit;padding:7px 9px;line-height:1.6}.isolated-field input{height:32px}.isolated-field textarea{resize:vertical;max-height:160px}.isolated-field input:focus-visible,.isolated-field textarea:focus-visible,.isolated-nav__item:focus-visible,.isolated-source-row:focus-visible{outline:2px solid var(--td-brand-color);outline-offset:2px}
@media(max-width:1100px){.isolated-grid{grid-template-columns:1fr}.isolated-grid__full{grid-column:auto}.isolated-nav{width:130px}.isolated-content{padding:13px}.isolated-guide__tasks{grid-template-columns:1fr}.isolated-guide__task>div{display:flex;gap:8px;align-items:center;flex:1;justify-content:space-between}.isolated-guide__task small{display:none}.isolated-guide__task{padding-top:6px}.isolated-guide__header>span{display:none}.isolated-guide__header strong{flex:1}.isolated-counts{grid-template-columns:repeat(2,minmax(0,1fr))}.isolated-counts>div:nth-child(-n+2){border-bottom:1px solid var(--app-border-color)}}
@media(max-width:650px){.isolated-nav{width:108px;padding:12px 5px}.isolated-nav__item{font-size:11px;padding:0 6px}.isolated-nav__label{padding:12px 6px 5px}.isolated-content{padding:10px}.isolated-counts>div{padding:9px}.isolated-counts strong{font-size:16px}.isolated-status{padding:6px;gap:4px;font-size:11px}.isolated-guide__header{gap:3px;padding:6px 8px}.isolated-guide__tasks{padding:0 8px 9px}.isolated-guide__task strong{font-size:11px}.isolated-guide__task .t-button{font-size:10px}.isolated-panel>header{padding:7px 9px}.isolated-panel h2{font-size:12px}.isolated-source-row{padding:9px;font-size:11px;gap:5px;flex-wrap:wrap}.isolated-source-row small{font-size:9px}.isolated-drawer{width:calc(100% - 12px)}.isolated-drawer__body{padding:12px}.isolated-review-row{flex-wrap:wrap}.isolated-review-row>div{flex-basis:100%}.isolated-counts span{font-size:10px}}
.isolated-mapping{margin:14px 0;padding:11px;border:1px solid var(--app-border-color);border-radius:6px}.isolated-mapping h3{font-size:12px;font-weight:500;margin:0 0 8px}.isolated-mapping select,.isolated-field select{min-width:0;max-width:100%;height:29px;font:inherit;font-size:11px;color:var(--app-text-primary);background:var(--app-card-bg);border:1px solid var(--app-border-color);border-radius:5px;padding:0 6px}.isolated-field select{flex:1}.isolated-mapping-block{border-top:1px solid var(--app-border-color);padding-top:9px;margin-top:10px}.isolated-mapping-block .isolated-actions{font-size:11px;justify-content:space-between}.isolated-mapping-block input[type=number]{width:54px;height:27px;background:var(--app-card-bg);color:var(--app-text-primary);border:1px solid var(--app-border-color);border-radius:4px;padding:0 4px}.isolated-mapping table,.isolated-sample{width:100%;border-collapse:collapse;font-size:10px;margin:8px 0}.isolated-mapping td,.isolated-mapping th,.isolated-sample td,.isolated-sample th{padding:5px;border:1px solid var(--app-border-color);text-align:left;overflow-wrap:anywhere}.isolated-mapping th,.isolated-sample th{font-weight:400;color:var(--app-text-secondary)}.isolated-confirm-line{display:flex;align-items:flex-start;gap:6px;font-size:11px;color:var(--app-text-secondary);margin:10px 0;line-height:1.7}.isolated-confirm-line input{accent-color:var(--td-brand-color);margin-top:3px;flex:none}.isolated-source-picker{display:flex;flex-direction:column;gap:6px;margin-bottom:10px;font-size:11px;max-height:160px;overflow:auto}.isolated-source-picker>span{color:var(--app-text-secondary)}.isolated-source-picker label{display:flex;align-items:center;gap:6px;overflow-wrap:anywhere}.isolated-source-picker input{accent-color:var(--td-brand-color);flex:none}
.isolated-role-select{font:inherit;font-size:10px;height:26px;max-width:96px;color:var(--app-text-primary);background:var(--app-card-bg);border:1px solid var(--app-border-color);border-radius:5px;padding:0 4px}
.isolated-module-picker{display:flex;flex-direction:column;padding:3px;border:1px solid var(--app-border-color);border-radius:6px;background:var(--app-card-bg)}.isolated-module-picker button{height:26px;border:0;background:none;color:var(--app-text-secondary);font:inherit;font-size:10px;text-align:left;cursor:pointer}.isolated-module-picker button:hover{background:var(--td-brand-color-light);color:var(--td-brand-color)}.isolated-observation-panel{padding:12px;border-top:1px solid var(--app-border-color)}.isolated-observation-panel h3{font-size:12px;margin:0;font-weight:500}.isolated-observation-panel .isolated-actions>small{font-size:10px;color:var(--app-text-secondary)}.isolated-chart{width:100%;max-height:190px;background:var(--app-bg);border-radius:5px}.isolated-chart__axis{stroke:var(--app-border-color);stroke-width:1}.isolated-chart__line{stroke:var(--td-brand-color);stroke-width:2;stroke-linejoin:round;stroke-linecap:round;vector-effect:non-scaling-stroke}.isolated-observation-table{width:100%;border-collapse:collapse;font-size:10px;table-layout:fixed}.isolated-observation-table th,.isolated-observation-table td{border:1px solid var(--app-border-color);padding:6px;text-align:left;overflow-wrap:anywhere}.isolated-observation-table th{font-weight:400;color:var(--app-text-secondary)}.isolated-observation-table small{display:block;color:var(--app-text-secondary)}.isolated-structure-tree{padding:12px}.isolated-structure-node{display:flex;align-items:center;gap:8px;padding:9px 10px;margin-bottom:7px;border:1px solid var(--app-border-color);border-radius:5px;background:var(--app-bg)}.isolated-structure-node>span{color:var(--td-brand-color);flex:none}.isolated-structure-node>div{flex:1;min-width:0}.isolated-structure-node strong{font-size:12px;font-weight:500}.isolated-structure-node small{display:block;font-size:10px;color:var(--app-text-secondary);overflow-wrap:anywhere}.isolated-structure-node .t-button{font-size:10px;flex:none}
.isolated-content-preview{margin:13px 0}.isolated-content-preview>small{font-size:10px;color:var(--app-text-secondary);margin-left:8px}.isolated-content-preview pre{font-family:inherit;font-size:11px;line-height:1.8;white-space:pre-wrap;overflow-wrap:anywhere;max-height:270px;overflow:auto;padding:10px;background:var(--app-bg);border:1px solid var(--app-border-color);border-radius:5px}
.isolated-batch-confirm{padding:10px 0}.isolated-batch-confirm .isolated-actions{font-size:11px;align-items:center}.isolated-batch-confirm select,.isolated-batch-confirm input[type=time]{height:28px;border:1px solid var(--app-border-color);border-radius:5px;background:var(--app-card-bg);color:var(--app-text-primary);font:inherit;font-size:11px;max-width:120px;padding:0 5px}.isolated-error-text{color:var(--td-error-color)!important}
.isolated-source-image{display:block;max-width:100%;max-height:320px;object-fit:contain;border:1px solid var(--app-border-color);border-radius:5px;margin-top:10px;background:var(--app-bg)}
.isolated-indicator-filter{width:138px;height:28px;min-width:0;font:inherit;font-size:11px;border:1px solid var(--app-border-color);border-radius:5px;background:var(--app-card-bg);color:var(--app-text-primary);padding:0 7px}.isolated-pagination{padding:8px 12px;font-size:10px;color:var(--app-text-secondary);border-top:1px solid var(--app-border-color)}@media(max-width:650px){.isolated-indicator-filter{width:98px}}
.isolated-header-editor{margin-top:9px;font-size:11px}.isolated-header-editor summary{color:var(--td-brand-color);cursor:pointer}.isolated-raw-header{max-height:200px;overflow:auto;margin-top:8px}.isolated-raw-header td{max-width:135px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}.isolated-raw-header td small{display:block;font-size:9px;color:var(--app-text-secondary)}.isolated-header-controls{display:flex;gap:7px;align-items:center;flex-wrap:wrap;margin:9px 0}.isolated-header-controls label{display:flex;gap:5px;align-items:center}.isolated-header-controls input,.isolated-header-controls select{width:76px;height:27px;padding:0 5px;font:inherit;font-size:10px;color:var(--app-text-primary);background:var(--app-card-bg);border:1px solid var(--app-border-color);border-radius:4px}.isolated-header-controls label:first-child input{width:46px}.isolated-column-editor{table-layout:fixed}.isolated-column-editor th:first-child{width:36px}.isolated-column-editor th:nth-last-child(-n+2){width:58px}.isolated-column-editor td{padding:3px!important}.isolated-column-editor input{width:100%;min-width:0;height:26px;font:inherit;font-size:10px;padding:0 4px;border:1px solid var(--app-border-color);border-radius:4px;color:var(--app-text-primary);background:var(--app-card-bg)}.isolated-column-pages{justify-content:flex-end;font-size:10px;color:var(--app-text-secondary);margin:8px 0}.isolated-future-review{border:1px solid var(--app-border-color);border-radius:5px;padding:0 9px 5px;margin:10px 0}
.isolated-series-list--with-chart{max-height:280px;overflow:auto;overscroll-behavior:contain}
</style>

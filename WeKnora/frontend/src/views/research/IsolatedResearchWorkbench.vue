<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { MessagePlugin } from 'tdesign-vue-next'
import { useAuthStore } from '@/stores/auth'
import { useUploadTasksStore } from '@/stores/uploadTasks'
import { updateResearchWorkbenchOnboarding, type ResearchWorkbench } from '@/api/workspaces'
import { createIsolatedResearchClient, type IsolatedResearchIdentity, type WorkbenchSource, type WorkbenchOverview } from '@/api/isolatedResearch'
import KbUploadSourceDropdown from '@/views/knowledge/components/KbUploadSourceDropdown.vue'
import { buildUploadFileName } from '@/views/knowledge/folderTree'
import { workspaceErrorMessage } from '@/views/workspaces/workspaceForm'

const props = defineProps<{ workbench: ResearchWorkbench; guideSuppressed?: boolean }>()
const emit = defineEmits<{ 'open-chat': []; 'update-workbench': [workbench: ResearchWorkbench]; navigate: [view: string]; refresh: [] }>()
const auth = useAuthStore(), uploads = useUploadTasksStore(), router = useRouter()
const uploadTypes = ['pdf','txt','md','docx','doc','pptx','ppt','epub','xmind','html','htm','mhtml','jpg','jpeg','png','csv','xlsx','xls','mp3','wav','m4a','flac','ogg','zip']
const saveVisualOriginals = ref(true)
const overview = ref<WorkbenchOverview | null>(null), sources = ref<WorkbenchSource[]>([])
const sourceTotal = ref(0), sourcePage = ref(1)
const loading = ref(false), busy = ref(false), error = ref(''), drawerError = ref('')
const drawer = ref<'sources' | 'evidence' | 'upload' | null>(null)
const sourceContent = ref<{text?: string; sha256?: string; supported?: boolean; notice?: string; image_data_url?: string} | null>(null)
const sourceContentOpen = ref(false), sourceContentLoading = ref(false)
const selectedSource = ref<WorkbenchSource | null>(null), pendingFiles = ref<File[]>([])
const isIsolated = computed(() => props.workbench.workspace_mode === 'isolated')
const guideVisible = computed(() => !props.guideSuppressed && isIsolated.value && (!props.workbench.onboarding || props.workbench.onboarding.status === 'active'))
const taskNames = { sources: '添加资料', review: '核对来源', research: '开始研究' }
let generation = 0, scopeEpoch = 0, sourceSelection = 0
let refreshTimer: ReturnType<typeof setTimeout> | undefined
function identity(): IsolatedResearchIdentity { return { token: auth.token || '', tenantId: String(auth.selectedTenantId ?? auth.tenant?.id ?? ''), userId: auth.user?.id || '', workbenchId: props.workbench.id, knowledgeBaseId: props.workbench.knowledge_base_id } }
const scopeKey = computed(() => Object.values(identity()).join(':'))
function client() { return createIsolatedResearchClient(identity(), identity) }
function validBinding() {
  const own = identity()
  return Boolean(own.token && own.tenantId && own.userId && own.workbenchId && own.knowledgeBaseId && ['legacy', 'isolated'].includes(props.workbench.workspace_mode))
}
function clearPreview() { sourceSelection++; selectedSource.value = null; sourceContent.value = null; sourceContentOpen.value = false; sourceContentLoading.value = false }
async function load() {
  const own = ++generation
  overview.value = null; sources.value = []; sourceTotal.value = 0; sourcePage.value = 1
  loading.value = true; error.value = ''
  try {
    if (!validBinding()) throw new Error('请先登录并重新打开当前工作台。')
    const api = client()
    const summary = await api.get<WorkbenchOverview>('/overview')
    const sourceList = summary.source_inventory || await api.get<{ knowledge_base_id: string; sources: WorkbenchSource[]; total?: number }>('/sources?page=1&page_size=100')
    if (own !== generation) return
    const binding = summary.workbench
    const counts = summary.counts
    if (!binding || binding.id !== props.workbench.id || binding.knowledge_base_id !== props.workbench.knowledge_base_id
      || binding.workspace_mode !== props.workbench.workspace_mode || sourceList.knowledge_base_id !== props.workbench.knowledge_base_id
      || !counts || ![counts.sources, counts.indicators, counts.observations].every(value => Number.isSafeInteger(value) && value >= 0)
      || !Array.isArray(summary.readiness?.tasks) || !Array.isArray(sourceList.sources)
      || sourceList.sources.some(source => typeof source.id !== 'string' || !source.id || source.knowledge_base_id && source.knowledge_base_id !== props.workbench.knowledge_base_id)) {
      throw new Error('工作台与资料范围不匹配，已停止显示。')
    }
    overview.value = summary; sources.value = sourceList.sources; sourceTotal.value = sourceList.total ?? sources.value.length
    sourcePage.value = Math.max(1,Math.ceil(sources.value.length/100))
  } catch (e) { if (own === generation) error.value = workspaceErrorMessage(e) }
  finally { if (own === generation) loading.value = false }
}
async function guide(status: 'active' | 'dismissed' | 'skipped' | 'completed', task?: keyof typeof taskNames) {
  // Legacy workbenches share source preparation, but their metadata endpoint
  // does not support the isolated onboarding state machine.
  if (!isIsolated.value || busy.value || !validBinding()) return
  const own = generation, ownerEpoch = scopeEpoch; busy.value = true; error.value = ''
  try {
    const result = await updateResearchWorkbenchOnboarding(props.workbench.id, { status, ...(task ? { tasks: { [task]: true } } : {}) })
    if (own === generation) emit('update-workbench', result.data)
  } catch (e) { if (own === generation) error.value = workspaceErrorMessage(e) }
  finally { if (ownerEpoch === scopeEpoch) busy.value = false }
}
function openSources() { clearPreview(); drawer.value = 'sources'; drawerError.value = '' }
function openGuide() { if (isIsolated.value) void guide('active'); else openSources() }
function openKnowledgeBase() {
  if (validBinding()) void router.push(`/platform/knowledge-bases/${encodeURIComponent(props.workbench.knowledge_base_id)}`)
}
function openReview() {
  if (validBinding()) void router.push({ path: `/platform/knowledge-bases/${encodeURIComponent(props.workbench.knowledge_base_id)}/structured-data`, query: { mode: 'import' } })
}
function currentSource(source: WorkbenchSource) {
  if (!validBinding() || !source?.id) return undefined
  return sources.value.find(item => item.id === source.id && (!item.knowledge_base_id || item.knowledge_base_id === props.workbench.knowledge_base_id))
}
function openStructured(source: WorkbenchSource) {
  const real = currentSource(source)
  if (!real || !isWorkbook(real)) { drawerError.value = '请重新选择当前知识库中的 Excel 原件。'; return }
  void router.push({ path: `/platform/knowledge-bases/${encodeURIComponent(props.workbench.knowledge_base_id)}/structured-data`, query: { mode: 'import', source_knowledge_id: real.id } })
}
async function showSource(source: WorkbenchSource) {
  const real = currentSource(source)
  if (!real) { drawerError.value = '资料范围已变化，请刷新来源后重试。'; return }
  selectedSource.value = real; drawer.value = 'evidence'; drawerError.value = ''; sourceContent.value = null; sourceContentOpen.value = false; sourceContentLoading.value = true
  const own = generation, selection = ++sourceSelection
  try {
    const result = await client().get<NonNullable<typeof sourceContent.value>>(`/sources/${encodeURIComponent(real.id)}/content`)
    if (own === generation && selection === sourceSelection && selectedSource.value?.id === real.id) sourceContent.value = result
  } catch (e) { if (own === generation && selection === sourceSelection && selectedSource.value?.id === real.id) drawerError.value = workspaceErrorMessage(e) }
  finally { if (own === generation && selection === sourceSelection && selectedSource.value?.id === real.id) sourceContentLoading.value = false }
}
function selectFiles(files: File[]) { if (!files.length || !validBinding()) return; pendingFiles.value = [...files]; drawer.value = 'upload'; drawerError.value = '' }
function enqueueFiles() {
  if (!pendingFiles.value.length || busy.value) return
  const own = identity()
  if (!validBinding()) { drawerError.value = '请先登录并选择空间。'; return }
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
  const own = scopeEpoch
  refreshTimer = setTimeout(() => { if (own === scopeEpoch) { void load(); emit('refresh') } }, 300)
}
async function moreSources() {
  if (busy.value || loading.value || sources.value.length >= sourceTotal.value) return
  const own = generation, ownerEpoch = scopeEpoch; busy.value = true; drawerError.value = ''
  try {
    const result = await client().get<{knowledge_base_id: string; sources: WorkbenchSource[]; total: number}>(`/sources?page=${sourcePage.value+1}&page_size=100`)
    if (own !== generation) return
    if (result.knowledge_base_id !== props.workbench.knowledge_base_id || !Array.isArray(result.sources)
      || result.sources.some(source => typeof source.id !== 'string' || !source.id || source.knowledge_base_id && source.knowledge_base_id !== props.workbench.knowledge_base_id)) throw new Error('资料范围不匹配，已停止显示。')
    sources.value = [...new Map([...sources.value, ...result.sources].map(source => [source.id, source])).values()]
    sourceTotal.value = result.total; sourcePage.value++
  } catch (e) { if (own === generation) drawerError.value = workspaceErrorMessage(e) }
  finally { if (ownerEpoch === scopeEpoch) busy.value = false }
}
function sourceLabel(source: WorkbenchSource) { const name = source.file_name || source.title || source.id; return source.folder_path ? `${source.folder_path}/${name}` : name }
function isWorkbook(source: WorkbenchSource) { return ['xlsx','xls'].includes((source.file_name || '').split('.').at(-1)?.toLowerCase() || '') }
function sourceStatus(source: WorkbenchSource) { const value = source.parse_status || 'pending'; return ({ completed: isWorkbook(source) ? '原件已保存' : '文档已解析', stored: '原件已保存', pending: '等待处理', processing: '处理中', failed: '处理失败' } as Record<string, string>)[value] || value }
watch(scopeKey, () => {
  scopeEpoch++; generation++; clearPreview()
  if (refreshTimer) { clearTimeout(refreshTimer); refreshTimer = undefined }
  overview.value = null; sources.value = []; sourceTotal.value = 0; sourcePage.value = 1
  loading.value = false; busy.value = false; error.value = ''; drawerError.value = ''; drawer.value = null; pendingFiles.value = []
  const own = generation
  void nextTick(() => { if (own === generation) void load() })
}, { immediate: true, flush: 'sync' })
onMounted(() => window.addEventListener('knowledgeFileUploaded', filesChanged))
onBeforeUnmount(() => { generation++; scopeEpoch++; sourceSelection++; if (refreshTimer) clearTimeout(refreshTimer); window.removeEventListener('knowledgeFileUploaded', filesChanged) })
defineExpose({ openSources, openGuide })
</script>

<template>
  <div class="workbench-preparation" data-testid="workbench-preparation">
    <section v-if="guideVisible" class="isolated-guide" aria-label="首次研究引导">
      <div class="isolated-guide__header">
        <strong>开始研究</strong>
        <t-button size="small" variant="text" :disabled="busy" @click="guide('dismissed')">稍后继续</t-button>
        <t-button size="small" variant="text" :disabled="busy" @click="guide('skipped')">跳过</t-button>
      </div>
      <div class="isolated-guide__tasks">
        <div v-for="(label,id,index) in taskNames" :key="id" class="isolated-guide__task">
          <span class="isolated-guide__number">{{ overview?.readiness.tasks.find(task => task.id === id)?.status === 'ready' ? '✓' : index + 1 }}</span>
          <div>
            <strong>{{ label }}</strong>
            <small>{{ id === 'sources' ? '保留原件与文件夹层级' : id === 'review' ? '在知识库确认结构与口径' : '整理结构与自己的判断' }}</small>
            <t-button size="small" variant="text" @click="id === 'sources' ? openSources() : id === 'review' ? openReview() : emit('navigate', 'diagram')">{{ id === 'sources' ? '添加 / 查看' : id === 'review' ? '前往核对' : '开始整理' }}</t-button>
          </div>
        </div>
      </div>
    </section>
    <t-alert v-if="error && (guideVisible || drawer)" theme="error" :message="error" class="isolated-error" />
    <aside v-if="drawer" class="isolated-drawer" aria-label="资料与研究侧栏">
      <header>
        <h2>{{ drawer === 'evidence' ? '来源预览' : drawer === 'upload' ? '确认添加资料' : '资料与来源' }}</h2>
        <t-button size="small" variant="text" aria-label="关闭资料侧栏" @click="clearPreview(); drawer = null"><t-icon name="close" /></t-button>
      </header>
      <div class="isolated-drawer__body">
        <t-alert v-if="drawerError" theme="error" :message="drawerError" />
        <template v-if="drawer === 'sources'">
          <div class="isolated-actions">
            <KbUploadSourceDropdown unified :supported-file-types="uploadTypes" trigger-label="添加资料" @files="selectFiles" />
            <t-button size="small" variant="text" :loading="loading" @click="load">刷新来源</t-button>
            <t-button size="small" variant="text" @click="openKnowledgeBase">知识库管理</t-button>
          </div>
          <p class="isolated-hint">原件保存在绑定知识库。Excel 在知识库确认结构后，指标即可在工作台使用。</p>
          <p v-if="overview" class="isolated-hint">{{ overview.counts.sources }} 份原件 · {{ overview.counts.indicators }} 个指标 · {{ overview.counts.observations }} 条观测</p>
          <p v-if="loading" class="isolated-hint" role="status">正在读取来源…</p>
          <div v-else-if="!sources.length && !error" class="isolated-note">尚无原件，可以先添加文件或文件夹。</div>
          <div v-for="source in sources" :key="source.id" class="isolated-review-row">
            <div><strong>{{ sourceLabel(source) }}</strong><small>{{ sourceStatus(source) }}</small></div>
            <t-button v-if="isWorkbook(source)" size="small" variant="text" @click="openStructured(source)">结构化解析</t-button>
            <t-button size="small" variant="text" @click="showSource(source)">预览</t-button>
          </div>
          <div v-if="sourceTotal" class="isolated-actions isolated-pagination">
            <span>已加载 {{ sources.length }} / {{ sourceTotal }} 份原件</span>
            <t-button v-if="sources.length < sourceTotal" size="small" variant="text" :loading="busy" @click="moreSources">加载更多来源</t-button>
          </div>
        </template>
        <template v-else-if="drawer === 'evidence' && selectedSource">
          <div class="isolated-evidence-name">{{ sourceLabel(selectedSource) }}</div>
          <dl class="isolated-evidence">
            <dt>归属</dt><dd>{{ workbench.knowledge_base_name || '绑定知识库' }}</dd>
            <dt>文件类型</dt><dd>{{ selectedSource.file_type || '原件' }}</dd>
            <dt>处理状态</dt><dd>{{ sourceStatus(selectedSource) }}</dd>
            <dt>来源标识</dt><dd>{{ selectedSource.id }}</dd>
          </dl>
          <div class="isolated-content-preview">
            <t-button size="small" variant="outline" :loading="sourceContentLoading" :disabled="!sourceContent?.supported" @click="sourceContentOpen = !sourceContentOpen">{{ sourceContentOpen ? '收起原文' : '查看原文' }}</t-button>
            <small v-if="sourceContent?.sha256">版本 {{ sourceContent.sha256.slice(0,12) }}</small>
            <p v-if="sourceContent?.notice" class="isolated-hint">{{ sourceContent.notice }}</p>
            <img v-if="sourceContentOpen && sourceContent?.image_data_url" :src="sourceContent.image_data_url" alt="来源原图" class="isolated-source-image" />
            <pre v-else-if="sourceContentOpen && sourceContent?.supported">{{ sourceContent.text }}</pre>
          </div>
          <div class="isolated-actions">
            <t-button v-if="isWorkbook(selectedSource)" size="small" @click="openStructured(selectedSource)">结构化解析</t-button>
            <t-button size="small" variant="outline" @click="openSources">返回来源</t-button>
          </div>
        </template>
        <template v-else-if="drawer === 'upload'">
          <p class="isolated-hint">添加到 {{ workbench.knowledge_base_name || '绑定知识库' }}，保留文件夹层级。Excel 先存原件，其他文档按知识库配置解析。</p>
          <div v-for="(file,index) in pendingFiles" :key="index" class="isolated-upload-row"><t-icon name="file" /><span>{{ file.webkitRelativePath || file.name }}</span><small>{{ Math.ceil(file.size / 1024) }} KB</small></div>
          <label class="isolated-confirm-line"><input v-model="saveVisualOriginals" type="checkbox" />图片和压缩包仅保存原件，不触发图像模型；ZIP 需选中此项才能保存</label>
          <div class="isolated-actions">
            <t-button size="small" variant="outline" @click="pendingFiles = []; drawer = null">取消</t-button>
            <t-button size="small" @click="enqueueFiles">确认添加 {{ pendingFiles.length }} 个文件</t-button>
          </div>
        </template>
      </div>
    </aside>
  </div>
</template>
<style scoped>
.isolated-research,.isolated-research *{box-sizing:border-box}.isolated-research{position:relative;display:flex;flex:1;min-width:0;min-height:0;color:var(--app-text-primary);background:var(--app-bg)}.isolated-nav{width:150px;flex:none;padding:15px 9px;display:flex;flex-direction:column;background:var(--app-sidebar-bg,var(--app-card-bg));border-right:1px solid var(--app-border-color);overflow:auto}.isolated-nav__item{display:flex;align-items:center;gap:7px;width:100%;height:32px;padding:0 9px;border:0;border-radius:var(--app-radius-sm);background:transparent;color:var(--app-text-primary);font:inherit;font-size:var(--app-text-sm);text-align:left;cursor:pointer;flex:none;margin:2px 0}.isolated-nav__item:hover,.isolated-nav__item.active{color:var(--td-brand-color);background:var(--td-brand-color-light)}.isolated-nav__label{font-size:var(--app-text-xs);color:var(--app-text-secondary);padding:15px 9px 6px}.isolated-nav__label span{display:block;font-size:var(--app-text-2xs);margin-top:3px}.isolated-nav__scope{margin-top:auto;padding:16px 9px 0;font-size:var(--app-text-2xs);line-height:1.7;color:var(--app-text-secondary)}.isolated-content{min-width:0;flex:1;overflow:auto;padding:16px 18px 24px}.isolated-status{display:flex;align-items:center;gap:7px;font-size:var(--app-text-sm);padding:5px 10px;min-height:36px;background:var(--app-card-bg);border:1px solid var(--app-border-color);border-radius:var(--app-radius-sm);margin-bottom:10px;flex-wrap:wrap}.isolated-status>span:nth-child(2){flex:1;min-width:100px}.isolated-status__dot{width:6px;height:6px;border-radius:50%;background:var(--td-brand-color);flex:none}.isolated-status .t-button{height:25px;font-size:var(--app-text-xs)}.isolated-guide{background:var(--app-card-bg);border:1px solid var(--app-border-color);border-radius:var(--app-radius-md);margin-bottom:14px}.isolated-guide__header{display:flex;align-items:center;gap:8px;padding:7px 11px;font-size:var(--app-text-sm)}.isolated-guide__header>span{font-size:var(--app-text-xs);color:var(--app-text-secondary);flex:1}.isolated-guide__header .t-button{height:24px;padding:0 5px;font-size:var(--app-text-xs)}.isolated-guide__tasks{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:10px;padding:0 11px 11px}.isolated-guide__task{display:flex;gap:7px;border-top:1px solid var(--app-border-color);padding-top:9px;min-width:0}.isolated-guide__task strong{font-size:var(--app-text-sm);font-weight:500}.isolated-guide__task small{display:block;color:var(--app-text-secondary);font-size:var(--app-text-2xs)}.isolated-guide__task .t-button{height:23px;padding:0;font-size:var(--app-text-xs)}.isolated-guide__number{width:19px;height:19px;display:grid;place-items:center;border:1px solid var(--app-border-color);border-radius:50%;font-size:var(--app-text-2xs);flex:none;color:var(--td-brand-color)}.isolated-error{margin-bottom:12px}.isolated-loading{padding:20px}.isolated-counts{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));border:1px solid var(--app-border-color);border-radius:var(--app-radius-md);overflow:hidden;background:var(--app-card-bg);margin-bottom:14px}.isolated-counts>div{padding:10px 12px;border-right:1px solid var(--app-border-color)}.isolated-counts>div:last-child{border:0}.isolated-counts span{display:block;font-size:var(--app-text-xs);color:var(--app-text-secondary)}.isolated-counts strong{font-size:var(--app-text-2xl);font-weight:600}.isolated-counts small{font-size:var(--app-text-2xs);font-weight:400;color:var(--app-text-secondary)}.isolated-grid{display:grid;grid-template-columns:1fr 1fr;gap:14px}.isolated-grid__full{grid-column:1/-1}.isolated-panel{min-width:0;background:var(--app-card-bg);border:1px solid var(--app-border-color);border-radius:var(--app-radius-md);overflow:hidden}.isolated-panel>header{display:flex;align-items:center;justify-content:space-between;gap:8px;min-height:41px;padding:7px 12px;border-bottom:1px solid var(--app-border-color)}.isolated-panel h2{font-size:var(--app-text-md);font-weight:500;margin:0}.isolated-panel>header>span{font-size:var(--app-text-2xs);color:var(--app-text-secondary)}.isolated-panel footer{padding:9px 12px;background:var(--app-bg);font-size:var(--app-text-2xs);color:var(--app-text-secondary);border-top:1px solid var(--app-border-color)}.isolated-empty{padding:27px 14px;text-align:center}.isolated-empty strong{font-size:var(--app-text-sm);font-weight:500}.isolated-empty p{font-size:var(--app-text-xs);color:var(--app-text-secondary);line-height:1.7;margin:5px 0 12px}.isolated-empty .isolated-actions{justify-content:center}.isolated-empty--small{padding:16px}.isolated-empty--small p{margin:0}.isolated-actions{display:flex;gap:6px;align-items:center;flex-wrap:wrap}.isolated-actions .t-button{font-size:var(--app-text-xs);height:28px}.isolated-source-row{display:flex;gap:8px;align-items:center;width:100%;padding:10px 12px;border:0;border-bottom:1px solid var(--app-border-color);background:transparent;color:var(--app-text-primary);font:inherit;font-size:var(--app-text-sm);text-align:left;cursor:pointer}.isolated-source-row:last-child{border-bottom:0}.isolated-source-row:hover{background:var(--td-brand-color-light)}.isolated-source-row>span{flex:1;overflow-wrap:anywhere;min-width:0}.isolated-source-row>small{font-size:var(--app-text-2xs);color:var(--app-text-secondary);flex:none}.isolated-source-row .t-icon{flex:none;color:var(--app-text-secondary)}.isolated-list button{display:flex;flex-direction:column;gap:3px;width:100%;padding:10px 12px;border:0;border-bottom:1px solid var(--app-border-color);font:inherit;background:none;text-align:left;color:var(--app-text-primary);cursor:pointer}.isolated-list button span{font-size:var(--app-text-sm)}.isolated-list button small{font-size:var(--app-text-2xs);color:var(--app-text-secondary)}.isolated-data-row{display:flex;gap:8px;align-items:center;justify-content:space-between;padding:11px 12px;border-bottom:1px solid var(--app-border-color)}.isolated-data-row strong{font-size:var(--app-text-sm);font-weight:500}.isolated-data-row small{display:block;color:var(--app-text-secondary);font-size:var(--app-text-2xs);margin-top:3px}.isolated-record{padding:12px;border-bottom:1px solid var(--app-border-color)}.isolated-record:last-child{border:0}.isolated-record>div{display:flex;justify-content:space-between;gap:10px}.isolated-record strong{font-size:var(--app-text-md);font-weight:500}.isolated-record span,.isolated-record small{font-size:var(--app-text-2xs);color:var(--app-text-secondary)}.isolated-record p{font-size:var(--app-text-sm);line-height:1.7;white-space:pre-wrap;overflow-wrap:anywhere;margin:7px 0;color:var(--app-text-secondary)}.isolated-drawer{position:absolute;z-index:8;right:0;top:0;bottom:0;width:min(460px,calc(100% - 24px));display:flex;flex-direction:column;background:var(--app-card-bg);border-left:1px solid var(--app-border-color);box-shadow:-8px 0 24px #20344717}.isolated-drawer>header{display:flex;align-items:center;justify-content:space-between;gap:8px;padding:12px 15px;border-bottom:1px solid var(--app-border-color)}.isolated-drawer h2{font-size:var(--app-text-base);font-weight:500;margin:0}.isolated-drawer__body{overflow:auto;padding:14px 15px;flex:1;min-height:0}.isolated-hint{font-size:var(--app-text-xs);color:var(--app-text-secondary);line-height:1.7;margin:10px 0}.isolated-note{font-size:var(--app-text-xs);color:var(--app-text-secondary);padding:10px;line-height:1.7;border:1px solid var(--app-border-color);border-radius:var(--app-radius-sm);background:var(--app-bg);margin:12px 0}.isolated-review-row{display:flex;gap:4px;align-items:center;padding:10px 0;border-bottom:1px solid var(--app-border-color)}.isolated-review-row>div{flex:1;min-width:0;overflow-wrap:anywhere}.isolated-review-row strong{font-size:var(--app-text-sm);font-weight:400}.isolated-review-row small{display:block;font-size:var(--app-text-2xs);color:var(--app-text-secondary)}.isolated-review-row .t-button{height:25px;font-size:var(--app-text-xs);padding:0 5px;flex:none}.isolated-section-title{font-size:var(--app-text-sm);font-weight:500;margin:16px 0 5px}.isolated-evidence-name{font-size:var(--app-text-md);font-weight:500;overflow-wrap:anywhere}.isolated-evidence{display:grid;grid-template-columns:64px 1fr;gap:10px;font-size:var(--app-text-sm);margin-top:16px}.isolated-evidence dt{color:var(--app-text-secondary)}.isolated-evidence dd{margin:0;overflow-wrap:anywhere;min-width:0}.isolated-upload-row{display:flex;align-items:center;gap:8px;border-bottom:1px solid var(--app-border-color);padding:9px 0;font-size:var(--app-text-xs)}.isolated-upload-row span{flex:1;min-width:0;overflow-wrap:anywhere}.isolated-upload-row small{flex:none;color:var(--app-text-secondary)}.isolated-field{display:flex;gap:10px;align-items:flex-start;margin-bottom:12px;font-size:var(--app-text-sm)}.isolated-field>span{width:33px;flex:none;padding-top:6px}.isolated-field input,.isolated-field textarea{min-width:0;flex:1;border:1px solid var(--app-border-color);border-radius:var(--app-radius-sm);background:var(--app-card-bg);color:var(--app-text-primary);font:inherit;padding:7px 9px;line-height:1.6}.isolated-field input{height:32px}.isolated-field textarea{resize:vertical;max-height:160px}.isolated-field input:focus-visible,.isolated-field textarea:focus-visible,.isolated-nav__item:focus-visible,.isolated-source-row:focus-visible{outline:2px solid var(--td-brand-color);outline-offset:2px}
@media(max-width:1100px){.isolated-grid{grid-template-columns:1fr}.isolated-grid__full{grid-column:auto}.isolated-nav{width:130px}.isolated-content{padding:13px}.isolated-guide__tasks{grid-template-columns:1fr}.isolated-guide__task>div{display:flex;gap:8px;align-items:center;flex:1;justify-content:space-between}.isolated-guide__task small{display:none}.isolated-guide__task{padding-top:6px}.isolated-guide__header>span{display:none}.isolated-guide__header strong{flex:1}.isolated-counts{grid-template-columns:repeat(2,minmax(0,1fr))}.isolated-counts>div:nth-child(-n+2){border-bottom:1px solid var(--app-border-color)}}
@media(max-width:650px){.isolated-nav{width:108px;padding:12px 5px}.isolated-nav__item{font-size:var(--app-text-xs);padding:0 6px}.isolated-nav__label{padding:12px 6px 5px}.isolated-content{padding:10px}.isolated-counts>div{padding:9px}.isolated-counts strong{font-size:var(--app-text-xl)}.isolated-status{padding:6px;gap:4px;font-size:var(--app-text-xs)}.isolated-guide__header{gap:3px;padding:6px 8px}.isolated-guide__tasks{padding:0 8px 9px}.isolated-guide__task strong{font-size:var(--app-text-xs)}.isolated-guide__task .t-button{font-size:var(--app-text-2xs)}.isolated-panel>header{padding:7px 9px}.isolated-panel h2{font-size:var(--app-text-sm)}.isolated-source-row{padding:9px;font-size:var(--app-text-xs);gap:5px;flex-wrap:wrap}.isolated-source-row small{font-size:var(--app-text-2xs)}.isolated-drawer{width:calc(100% - 12px)}.isolated-drawer__body{padding:12px}.isolated-review-row{flex-wrap:wrap}.isolated-review-row>div{flex-basis:100%}.isolated-counts span{font-size:var(--app-text-2xs)}}
.isolated-mapping{margin:14px 0;padding:11px;border:1px solid var(--app-border-color);border-radius:var(--app-radius-sm)}.isolated-mapping h3{font-size:var(--app-text-sm);font-weight:500;margin:0 0 8px}.isolated-mapping select,.isolated-field select{min-width:0;max-width:100%;height:29px;font:inherit;font-size:var(--app-text-xs);color:var(--app-text-primary);background:var(--app-card-bg);border:1px solid var(--app-border-color);border-radius:var(--app-radius-sm);padding:0 6px}.isolated-field select{flex:1}.isolated-mapping-block{border-top:1px solid var(--app-border-color);padding-top:9px;margin-top:10px}.isolated-mapping-block .isolated-actions{font-size:var(--app-text-xs);justify-content:space-between}.isolated-mapping-block input[type=number]{width:54px;height:27px;background:var(--app-card-bg);color:var(--app-text-primary);border:1px solid var(--app-border-color);border-radius:var(--app-radius-xs);padding:0 4px}.isolated-mapping table,.isolated-sample{width:100%;border-collapse:collapse;font-size:var(--app-text-2xs);margin:8px 0}.isolated-mapping td,.isolated-mapping th,.isolated-sample td,.isolated-sample th{padding:5px;border:1px solid var(--app-border-color);text-align:left;overflow-wrap:anywhere}.isolated-mapping th,.isolated-sample th{font-weight:400;color:var(--app-text-secondary)}.isolated-confirm-line{display:flex;align-items:flex-start;gap:6px;font-size:var(--app-text-xs);color:var(--app-text-secondary);margin:10px 0;line-height:1.7}.isolated-confirm-line input{accent-color:var(--td-brand-color);margin-top:3px;flex:none}.isolated-source-picker{display:flex;flex-direction:column;gap:6px;margin-bottom:10px;font-size:var(--app-text-xs);max-height:160px;overflow:auto}.isolated-source-picker>span{color:var(--app-text-secondary)}.isolated-source-picker label{display:flex;align-items:center;gap:6px;overflow-wrap:anywhere}.isolated-source-picker input{accent-color:var(--td-brand-color);flex:none}
.isolated-role-select{font:inherit;font-size:var(--app-text-2xs);height:26px;max-width:96px;color:var(--app-text-primary);background:var(--app-card-bg);border:1px solid var(--app-border-color);border-radius:var(--app-radius-sm);padding:0 4px}
.isolated-module-picker{display:flex;flex-direction:column;padding:3px;border:1px solid var(--app-border-color);border-radius:var(--app-radius-sm);background:var(--app-card-bg)}.isolated-module-picker button{height:26px;border:0;background:none;color:var(--app-text-secondary);font:inherit;font-size:var(--app-text-2xs);text-align:left;cursor:pointer}.isolated-module-picker button:hover{background:var(--td-brand-color-light);color:var(--td-brand-color)}.isolated-observation-panel{padding:12px;border-top:1px solid var(--app-border-color)}.isolated-observation-panel h3{font-size:var(--app-text-sm);margin:0;font-weight:500}.isolated-observation-panel .isolated-actions>small{font-size:var(--app-text-2xs);color:var(--app-text-secondary)}.isolated-chart{width:100%;max-height:190px;background:var(--app-bg);border-radius:var(--app-radius-sm)}.isolated-chart__axis{stroke:var(--app-border-color);stroke-width:1}.isolated-chart__line{stroke:var(--td-brand-color);stroke-width:2;stroke-linejoin:round;stroke-linecap:round;vector-effect:non-scaling-stroke}.isolated-observation-table{width:100%;border-collapse:collapse;font-size:var(--app-text-2xs);table-layout:fixed}.isolated-observation-table th,.isolated-observation-table td{border:1px solid var(--app-border-color);padding:6px;text-align:left;overflow-wrap:anywhere}.isolated-observation-table th{font-weight:400;color:var(--app-text-secondary)}.isolated-observation-table small{display:block;color:var(--app-text-secondary)}.isolated-structure-tree{padding:12px}.isolated-structure-node{display:flex;align-items:center;gap:8px;padding:9px 10px;margin-bottom:7px;border:1px solid var(--app-border-color);border-radius:var(--app-radius-sm);background:var(--app-bg)}.isolated-structure-node>span{color:var(--td-brand-color);flex:none}.isolated-structure-node>div{flex:1;min-width:0}.isolated-structure-node strong{font-size:var(--app-text-sm);font-weight:500}.isolated-structure-node small{display:block;font-size:var(--app-text-2xs);color:var(--app-text-secondary);overflow-wrap:anywhere}.isolated-structure-node .t-button{font-size:var(--app-text-2xs);flex:none}
.isolated-content-preview{margin:13px 0}.isolated-content-preview>small{font-size:var(--app-text-2xs);color:var(--app-text-secondary);margin-left:8px}.isolated-content-preview pre{font-family:inherit;font-size:var(--app-text-xs);line-height:1.8;white-space:pre-wrap;overflow-wrap:anywhere;max-height:270px;overflow:auto;padding:10px;background:var(--app-bg);border:1px solid var(--app-border-color);border-radius:var(--app-radius-sm)}
.isolated-batch-confirm{padding:10px 0}.isolated-batch-confirm .isolated-actions{font-size:var(--app-text-xs);align-items:center}.isolated-batch-confirm select,.isolated-batch-confirm input[type=time]{height:28px;border:1px solid var(--app-border-color);border-radius:var(--app-radius-sm);background:var(--app-card-bg);color:var(--app-text-primary);font:inherit;font-size:var(--app-text-xs);max-width:120px;padding:0 5px}.isolated-error-text{color:var(--td-error-color)!important}
.isolated-source-image{display:block;max-width:100%;max-height:320px;object-fit:contain;border:1px solid var(--app-border-color);border-radius:var(--app-radius-sm);margin-top:10px;background:var(--app-bg)}
.isolated-indicator-filter{width:138px;height:28px;min-width:0;font:inherit;font-size:var(--app-text-xs);border:1px solid var(--app-border-color);border-radius:var(--app-radius-sm);background:var(--app-card-bg);color:var(--app-text-primary);padding:0 7px}.isolated-pagination{padding:8px 12px;font-size:var(--app-text-2xs);color:var(--app-text-secondary);border-top:1px solid var(--app-border-color)}@media(max-width:650px){.isolated-indicator-filter{width:98px}}
.isolated-header-editor{margin-top:9px;font-size:var(--app-text-xs)}.isolated-header-editor summary{color:var(--td-brand-color);cursor:pointer}.isolated-raw-header{max-height:200px;overflow:auto;margin-top:8px}.isolated-raw-header td{max-width:135px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}.isolated-raw-header td small{display:block;font-size:var(--app-text-2xs);color:var(--app-text-secondary)}.isolated-header-controls{display:flex;gap:7px;align-items:center;flex-wrap:wrap;margin:9px 0}.isolated-header-controls label{display:flex;gap:5px;align-items:center}.isolated-header-controls input,.isolated-header-controls select{width:76px;height:27px;padding:0 5px;font:inherit;font-size:var(--app-text-2xs);color:var(--app-text-primary);background:var(--app-card-bg);border:1px solid var(--app-border-color);border-radius:var(--app-radius-xs)}.isolated-header-controls label:first-child input{width:46px}.isolated-column-editor{table-layout:fixed}.isolated-column-editor th:first-child{width:36px}.isolated-column-editor th:nth-last-child(-n+2){width:58px}.isolated-column-editor td{padding:3px!important}.isolated-column-editor input{width:100%;min-width:0;height:26px;font:inherit;font-size:var(--app-text-2xs);padding:0 4px;border:1px solid var(--app-border-color);border-radius:var(--app-radius-xs);color:var(--app-text-primary);background:var(--app-card-bg)}.isolated-column-pages{justify-content:flex-end;font-size:var(--app-text-2xs);color:var(--app-text-secondary);margin:8px 0}.isolated-future-review{border:1px solid var(--app-border-color);border-radius:var(--app-radius-sm);padding:0 9px 5px;margin:10px 0}
.isolated-series-list--with-chart{max-height:280px;overflow:auto;overscroll-behavior:contain}

.workbench-preparation{position:absolute;inset:0;z-index:4;pointer-events:none}
.workbench-preparation .isolated-guide{position:absolute;right:16px;bottom:16px;width:310px;max-width:calc(100% - 32px);margin:0;background:var(--app-card-bg);border:1px solid var(--app-border-color);border-radius:var(--app-radius-md);box-shadow:0 5px 18px #20344712;pointer-events:auto}
.workbench-preparation .isolated-guide__header{padding:8px 10px;gap:4px}
.workbench-preparation .isolated-guide__header>span{display:none}
.workbench-preparation .isolated-guide__header>strong{flex:1;font-size:var(--app-text-sm)}
.workbench-preparation .isolated-guide__header .t-button{font-size:var(--app-text-xs);height:26px}
.workbench-preparation .isolated-guide__tasks{display:flex;flex-direction:column;gap:6px;padding:0 10px 10px}
.workbench-preparation .isolated-guide__task{display:flex;align-items:flex-start;gap:8px;padding-top:7px;border-top:1px solid var(--app-border-color)}
.workbench-preparation .isolated-guide__task>div{display:grid;grid-template-columns:1fr auto;flex:1;align-items:center;gap:1px 4px}
.workbench-preparation .isolated-guide__task strong{font-size:var(--app-text-sm);font-weight:500}
.workbench-preparation .isolated-guide__task small{display:block;grid-row:2;font-size:var(--app-text-2xs)}
.workbench-preparation .isolated-guide__task .t-button{grid-column:2;grid-row:1 / 3;font-size:var(--app-text-xs);height:27px}
.workbench-preparation .isolated-drawer{pointer-events:auto;z-index:2}
.workbench-preparation>.isolated-error{position:absolute;right:16px;bottom:230px;max-width:360px;pointer-events:auto}
</style>

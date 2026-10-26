<template>
  <div class="research-workspace" :class="{ 'research-workspace--isolated': Boolean(workbench) }">
    <div v-if="workbench" class="research-toolbar">
      <router-link v-if="workbenchId" class="research-back" to="/platform/research-workbenches" aria-label="返回研究工作台列表">
        <t-icon name="chevron-left" />研究工作台
      </router-link>
      <strong>{{ workbench.name }}</strong><span>{{ sourceLabel }}</span>
      <div class="research-toolbar-actions">
        <t-button v-if="canReadSource" class="research-toolbar-action resource-action resource-action--compact" data-guide="research-sources" theme="default" variant="outline" size="small" @click="isolatedView?.openSources()">资料与来源</t-button>
        <router-link v-if="workbench?.knowledge_base_id && canReadSource" class="research-toolbar-action resource-action resource-action--compact" data-guide="research-kb" :to="`/platform/knowledge-bases/${workbench.knowledge_base_id}`">转到知识库</router-link>
        <t-button v-if="canReadSource" class="research-toolbar-action resource-action resource-action--compact" theme="default" variant="outline" size="small" :disabled="!canShowResearch" :aria-expanded="chatVisible"
          aria-controls="research-chat-panel" data-guide="research-chat" @click="openChat">问答助手</t-button>
        <WorkbenchActions ref="actionsView" :workbench="workbench" @renamed="applyWorkbenchUpdate" @sources-changed="reloadSources" @deleted="router.replace('/platform/research-workbenches')" />
      </div>
    </div>
    <div class="research-body">
      <div class="research-canvas">
      <IsolatedResearchWorkbench v-if="canShowResearch && workbench && canReadSource" :key="scopeGeneration" ref="isolatedView" :workbench="workbench" @open-chat="openChat" @navigate="navigateResearch" @refresh="restartSession" @update-workbench="applyWorkbenchUpdate" />
      <iframe v-if="canShowResearch && frameReady" :key="generation" ref="frame" :src="frameUrl"
        title="商品研究工作台" @load="frameLoaded" />
      <div v-else-if="workbenchLoading" class="research-empty" role="status">正在打开研究工作台…</div>
      <div v-else-if="workbenchError" class="research-empty" role="alert">
        <p>{{ workbenchError }}</p><t-button variant="outline" @click="loadWorkbench">重试</t-button>
      </div>
      <div v-else class="research-empty">
        <p>{{ auth.token && tenantId ? '请先选择一个研究工作台，使用它绑定的知识库。' : '请先登录并选择空间。' }}</p>
        <router-link v-if="auth.token && tenantId" to="/platform/research-workbenches">选择研究工作台</router-link>
      </div>
      <ResearchFeatureGuides v-if="canShowResearch && workbench && canReadSource" :workbench="workbench" :frame="frame" :frame-version="frameVersion" :suppressed="chatVisible || !frameReady || Boolean(isolatedView?.drawerOpen)" />
      </div>
      <ResearchChatPanel v-if="chatMounted && canShowResearch && canReadSource" :key="scopeGeneration" :visible="chatVisible"
        :kb-id="chatSourceIds[0] || ''" :kb-ids="chatSourceIds" :kb-name="workbench?.knowledge_base_name || ''"
        :workbench-name="workbench?.name || '研究工作台'" :isolated="isIsolated" :workbench-id="workbenchId" @close="chatVisible = false" />
    </div>
  </div>
</template>
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useAuthStore } from '@/stores/auth'
import { useTheme } from '@/composables/useTheme'
import { useRoute, useRouter } from 'vue-router'
import { getResearchWorkbench, type ResearchWorkbench } from '@/api/workspaces'
import ResearchChatPanel from './ResearchChatPanel.vue'
import IsolatedResearchWorkbench from './IsolatedResearchWorkbench.vue'
import { authenticateResearchFrame, researchFramePath } from './researchFrameSession'
import WorkbenchActions from '@/views/workspaces/WorkbenchActions.vue'
import ResearchFeatureGuides from './ResearchFeatureGuides.vue'
import { directKnowledgeBaseIds, workbenchSourceLabel } from './workbenchDataSources'
const auth = useAuthStore()
const { effectiveTheme, setTheme } = useTheme()
const route = useRoute()
const router = useRouter()
const frame = ref<HTMLIFrameElement>()
const isolatedView = ref<InstanceType<typeof IsolatedResearchWorkbench>>()
const actionsView = ref<InstanceType<typeof WorkbenchActions>>()
const generation = ref(0)
const frameVersion = ref(0)
const scopeGeneration = ref(0)
const tenantId = computed(() => auth.selectedTenantId ?? auth.tenant?.id)
const workbenchId = computed(() => typeof route.params.workbenchId === 'string' ? route.params.workbenchId : '')
const workbench = ref<ResearchWorkbench | null>(null)
const workbenchLoading = ref(false)
const workbenchError = ref('')
const chatMounted = ref(false)
const chatVisible = ref(false)
const researchView = ref('home')
const nativeDrawerOpen = ref(false), frameIsHome = ref(true)
const frameReady = ref(false)
let requestGeneration = 0
let authGeneration = 0
const isIsolated = computed(() => workbench.value?.workspace_mode === 'isolated')
const canShowResearch = computed(() => Boolean(auth.token && tenantId.value && workbenchId.value && workbench.value))
const canReadSource = computed(() => workbench.value?.can_read_source !== false)
const sourceLabel = computed(() => workbench.value ? workbenchSourceLabel(workbench.value, []) : '')
const chatSourceIds = computed(() => workbench.value ? directKnowledgeBaseIds(workbench.value) : [])
const frameUrl = computed(() => {
  if (!canShowResearch.value) return ''
  return researchFramePath(workbenchId.value,researchView.value)
})
function restartSession() { generation.value += 1 }
async function prepareFrameSession() {
  const current = ++authGeneration
  frameReady.value = false
  const token = auth.token, tenant = tenantId.value
  if (!token || !tenant || !workbenchId.value) return false
  const ready = await authenticateResearchFrame(token, tenant, () => current === authGeneration)
  if (current === authGeneration) frameReady.value = ready
  return ready
}
async function reauthenticateFrame() {
  restartSession()
  try { await prepareFrameSession() }
  catch (error) { workbenchError.value = error instanceof Error ? error.message : '研究工作台登录核验失败。' }
}
function openChat() { if (canReadSource.value) { chatMounted.value = true; chatVisible.value = true } }
function navigateResearch(view: string) {
  if (!canShowResearch.value || !['home', 'diagram', 'indicators', 'judgment', 'strategies', 'reports'].includes(view)) return
  researchView.value = view
  nativeDrawerOpen.value = false
  frameIsHome.value = view === 'home'
  restartSession()
}
function applyWorkbenchUpdate(updated: ResearchWorkbench) {
  const current = workbench.value
  if (!current || updated?.id !== current.id || updated.id !== workbenchId.value
      || updated.knowledge_base_id !== current.knowledge_base_id || !['legacy', 'isolated'].includes(current.workspace_mode)
      || updated.workspace_mode !== current.workspace_mode) {
    requestGeneration++
    scopeGeneration.value++
    workbench.value = null
    workbenchLoading.value = false
    workbenchError.value = '工作台范围信息不完整或已改变，请重新打开。'
    chatMounted.value = false
    chatVisible.value = false
    return
  }
  workbench.value = updated
}
async function reloadSources() {
  scopeGeneration.value++
  chatMounted.value = false; chatVisible.value = false
  restartSession()
  await loadWorkbench()
}
async function loadWorkbench() {
  const current = ++requestGeneration
  workbench.value = null
  workbenchError.value = ''
  workbenchLoading.value = false
  if (!workbenchId.value || !auth.token || !tenantId.value) return
  workbenchLoading.value = true
  try {
    const [result, authenticated] = await Promise.all([getResearchWorkbench(workbenchId.value),prepareFrameSession()])
    if (current !== requestGeneration) return
    if (!authenticated) return
    const data = result.data
    const validSources = data?.scope_version === 2 && Number.isSafeInteger(data.sources_revision) && Number(data.sources_revision) > 0 && Array.isArray(data.sources) && data.sources.length <= 8
    if (!result.success || !data || data.id !== workbenchId.value || (!data.knowledge_base_id && !validSources)) throw new Error('工作台或数据来源不可用。')
    if (!['legacy', 'isolated'].includes(result.data.workspace_mode || '')) throw new Error('工作台范围信息不完整，请更新服务后重试。')
    workbench.value = result.data
  } catch (error) {
    if (current !== requestGeneration) return
    workbenchError.value = error instanceof Error ? error.message : '未能打开研究工作台，请重试。'
  } finally {
    if (current === requestGeneration) workbenchLoading.value = false
  }
}
function sendTheme() { frame.value?.contentWindow?.postMessage({ type: 'workspace-theme', mode: effectiveTheme.value }, window.location.origin) }
function frameLoaded() {
  frameVersion.value++
  nativeDrawerOpen.value = false
  try {
    const path = frame.value?.contentWindow?.location?.pathname
    if (path) frameIsHome.value = path === researchFramePath(workbenchId.value,'home')
  } catch { frameIsHome.value = false }
  sendTheme()
}
function receive(event: MessageEvent) {
  if (event.origin !== window.location.origin || event.source !== frame.value?.contentWindow) return
  if (canShowResearch.value && event.data?.type === 'research-open-sources') { if (isolatedView.value) isolatedView.value.openSources(); else if (workbench.value?.can_manage) actionsView.value?.openSources(); return }
  if (canShowResearch.value && event.data?.type === 'research-drawer-state' && typeof event.data.open === 'boolean') { nativeDrawerOpen.value = event.data.open; return }
  if (event.data?.type === 'workspace-theme-ready') { sendTheme(); return }
  if (event.data?.type === 'workspace-theme-request') {
    if (event.data.mode === 'light' || event.data.mode === 'dark') setTheme(event.data.mode)
    return
  }
  if (event.data?.type === 'research-auth-expired') void reauthenticateFrame()
}
watch(effectiveTheme, sendTheme)
// Replace both surfaces on identity changes; old workspace content must disappear.
watch(() => [auth.token, tenantId.value, auth.user?.id, workbenchId.value], (scope, previous) => {
  const identityChanged = !previous || scope.slice(1).some((value, index) => value !== previous[index + 1]) || Boolean(scope[0]) !== Boolean(previous[0])
  if (!identityChanged) { if (!workbenchLoading.value) void reauthenticateFrame(); return }
  restartSession()
  authGeneration++
  frameReady.value = false
  chatMounted.value = false
  chatVisible.value = false
  researchView.value = 'home'
  nativeDrawerOpen.value = false
  frameIsHome.value = true
  scopeGeneration.value++
  requestGeneration++
  workbench.value = null
  workbenchError.value = ''
  workbenchLoading.value = Boolean(workbenchId.value && auth.token && tenantId.value)
  const currentScope = scopeGeneration.value
  // The auth store persists the new tenant header after updating its reactive ID.
  void nextTick(() => { if (currentScope === scopeGeneration.value) void loadWorkbench() })
}, { flush: 'sync', immediate: true })
onMounted(() => window.addEventListener('message', receive))
onBeforeUnmount(() => { requestGeneration++; authGeneration++; scopeGeneration.value++; window.removeEventListener('message', receive) })
</script>
<style scoped>
.research-workspace{display:flex;flex-direction:column;width:100%;height:100%;min-height:0;background:var(--app-bg);font-family:var(--app-font-family)}
.research-toolbar{display:flex;align-items:center;flex-wrap:wrap;gap:10px;min-height:44px;padding:6px 24px;box-sizing:border-box;background:var(--app-bg)}
.research-toolbar strong{font-size:var(--app-text-base);font-weight:600;max-width:280px;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.research-toolbar>span{font-size:var(--app-text-sm);color:var(--app-text-secondary);max-width:180px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.research-toolbar .research-back{font-size:var(--app-text-md);color:var(--td-brand-color);text-decoration:none}
.research-toolbar-actions{display:flex;align-items:center;gap:8px;margin-left:auto}

.research-back{display:inline-flex;align-items:center;gap:4px}
.research-body{position:relative;display:flex;flex:1;min-height:0;min-width:0;overflow:hidden}
.research-canvas{position:relative;display:flex;flex:1;min-width:0;min-height:0}
.research-empty{padding:24px;flex:1}iframe{width:100%;flex:1;min-width:0;min-height:0;border:0}
@media(max-width:720px){.research-toolbar{gap:6px;padding:6px 16px}.research-toolbar>span{display:none}.research-toolbar strong{max-width:calc(100vw - 150px)}.research-toolbar-actions{margin-left:0;flex-wrap:wrap;gap:6px}}
</style>

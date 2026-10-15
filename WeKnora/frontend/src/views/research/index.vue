<template>
  <div class="research-workspace" :class="{ 'research-workspace--isolated': isIsolated }">
    <div v-if="!workbenchId || workbench" class="research-toolbar">
      <router-link v-if="workbenchId" class="research-back" to="/platform/research-workbenches" aria-label="返回研究工作台列表">
        <t-icon name="chevron-left" />研究工作台
      </router-link>
      <strong>{{ isIsolated ? workbench?.name : '商品研究' }}</strong><span>{{ isIsolated ? (workbench?.knowledge_base_name || '绑定知识库') : '当前空间的研究数据与个人判断' }}</span>
      <t-button v-if="isIsolated" class="research-materials" variant="text" size="small" @click="isolatedView?.openSources()">资料与来源</t-button>
      <t-button v-if="isIsolated" variant="text" size="small" @click="isolatedView?.openGuide()" aria-label="重新打开新手引导">新手引导</t-button>
      <router-link v-else class="research-materials" to="/platform/materials?purpose=research&add=1">添加研究资料</router-link>
      <router-link v-if="workbench?.knowledge_base_id" :to="`/platform/knowledge-bases/${workbench.knowledge_base_id}`">转到知识库</router-link>
      <t-button variant="outline" size="small" :disabled="!canShowResearch" :aria-expanded="chatVisible"
        aria-controls="research-chat-panel" @click="openChat">问答助手</t-button>
    </div>
    <div class="research-body">
      <IsolatedResearchWorkbench v-if="canShowResearch && isIsolated && workbench" :key="scopeGeneration" ref="isolatedView" :workbench="workbench" @open-chat="openChat" @update-workbench="workbench = $event" />
      <iframe v-else-if="canShowResearch" :key="generation" ref="frame" :src="bootstrapUrl"
        title="商品研究工作台" @load="sendAuth" />
      <div v-else-if="workbenchLoading" class="research-empty" role="status">正在打开研究工作台…</div>
      <div v-else-if="workbenchError" class="research-empty" role="alert">
        <p>{{ workbenchError }}</p><t-button variant="outline" @click="loadWorkbench">重试</t-button>
      </div>
      <div v-else class="research-empty">请先选择空间。</div>
      <ResearchChatPanel v-if="chatMounted && canShowResearch" :key="scopeGeneration" :visible="chatVisible"
        :kb-id="workbench?.knowledge_base_id || ''" :kb-name="workbench?.knowledge_base_name || ''"
        :workbench-name="workbench?.name || '研究工作台'" :isolated="isIsolated" :workbench-id="workbenchId" @close="chatVisible = false" />
    </div>
  </div>
</template>
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useAuthStore } from '@/stores/auth'
import { useTheme } from '@/composables/useTheme'
import { useRoute } from 'vue-router'
import { getResearchWorkbench, type ResearchWorkbench } from '@/api/workspaces'
import ResearchChatPanel from './ResearchChatPanel.vue'
import IsolatedResearchWorkbench from './IsolatedResearchWorkbench.vue'
const auth = useAuthStore()
const { effectiveTheme, setTheme } = useTheme()
const route = useRoute()
const frame = ref<HTMLIFrameElement>()
const isolatedView = ref<InstanceType<typeof IsolatedResearchWorkbench>>()
const generation = ref(0)
const scopeGeneration = ref(0)
const tenantId = computed(() => auth.selectedTenantId ?? auth.tenant?.id)
const workbenchId = computed(() => typeof route.params.workbenchId === 'string' ? route.params.workbenchId : '')
const workbench = ref<ResearchWorkbench | null>(null)
const workbenchLoading = ref(false)
const workbenchError = ref('')
const chatMounted = ref(false)
const chatVisible = ref(false)
let requestGeneration = 0
const isIsolated = computed(() => workbench.value?.workspace_mode === 'isolated')
const canShowResearch = computed(() => Boolean(auth.token && tenantId.value && (!workbenchId.value || workbench.value)))
const bootstrapUrl = computed(() => {
  const source=typeof route.query.source==='string' && /^[a-f0-9]{64}$/.test(route.query.source) ? route.query.source : ''
  const indicator = typeof route.query.indicator === 'string' ? route.query.indicator : ''
  return `/research/auth/bootstrap?session=${generation.value}${source ? '&source='+source : indicator ? '&indicator='+encodeURIComponent(indicator) : ''}`
})
function restartSession() { generation.value += 1 }
function openChat() { chatMounted.value = true; chatVisible.value = true }
async function loadWorkbench() {
  const current = ++requestGeneration
  workbench.value = null
  workbenchError.value = ''
  workbenchLoading.value = false
  if (!workbenchId.value || !auth.token || !tenantId.value) return
  workbenchLoading.value = true
  try {
    const result = await getResearchWorkbench(workbenchId.value)
    if (current !== requestGeneration) return
    if (!result.success || !result.data?.knowledge_base_id) throw new Error('工作台或绑定知识库不可用。')
    workbench.value = result.data
  } catch (error) {
    if (current !== requestGeneration) return
    workbenchError.value = error instanceof Error ? error.message : '未能打开研究工作台，请重试。'
  } finally {
    if (current === requestGeneration) workbenchLoading.value = false
  }
}
function sendAuth() {
  if (!auth.token || !tenantId.value || isIsolated.value) return
  frame.value?.contentWindow?.postMessage({
    type: 'research-auth', token: auth.token, tenantId: tenantId.value,
  }, window.location.origin)
}
function sendTheme() { frame.value?.contentWindow?.postMessage({ type: 'workspace-theme', mode: effectiveTheme.value }, window.location.origin) }
function receive(event: MessageEvent) {
  if (isIsolated.value) return
  if (event.origin !== window.location.origin || event.source !== frame.value?.contentWindow) return
  if (event.data?.type === 'workspace-theme-ready') { sendTheme(); return }
  if (event.data?.type === 'workspace-theme-request') {
    if (event.data.mode === 'light' || event.data.mode === 'dark') setTheme(event.data.mode)
    return
  }
  if (event.data?.type === 'research-ready') sendAuth()
  if (event.data?.type === 'research-auth-expired') restartSession()
}
watch(effectiveTheme, sendTheme)
// Replace both surfaces on identity changes; old workspace content must disappear.
watch(() => [auth.token, tenantId.value, auth.user?.id, workbenchId.value], (scope, previous) => {
  restartSession()
  const identityChanged = !previous || scope.slice(1).some((value, index) => value !== previous[index + 1]) || Boolean(scope[0]) !== Boolean(previous[0])
  if (!identityChanged) return
  chatMounted.value = false
  chatVisible.value = false
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
onBeforeUnmount(() => { requestGeneration++; scopeGeneration.value++; window.removeEventListener('message', receive) })
</script>
<style scoped>
.research-workspace{display:flex;flex-direction:column;width:100%;height:100%;min-height:0;background:var(--app-bg,#f4f7f5)}
.research-toolbar{display:flex;gap:24px;align-items:center;padding:16px 24px;background:var(--app-card-bg,white);border-bottom:1px solid var(--app-border-color,#dce5df);flex-wrap:wrap}
.research-toolbar span{font-size:var(--app-text-md);color:var(--app-text-secondary,#5b6b63)}
.research-toolbar a{font-size:var(--app-text-md);color:var(--td-brand-color);text-decoration:none}
.research-toolbar .research-materials{margin-left:auto}
.research-back{display:inline-flex;gap:4px;align-items:center}
.research-body{position:relative;display:flex;flex:1;min-height:0;min-width:0;overflow:hidden}
.research-empty{padding:24px;flex:1}iframe{width:100%;flex:1;min-width:0;min-height:0;border:0}
.research-workspace--isolated .research-toolbar{gap:12px;padding:10px 18px}.research-workspace--isolated .research-toolbar strong{font-size:14px;max-width:300px;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.research-workspace--isolated .research-toolbar>span{font-size:12px;max-width:200px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
@media(max-width:900px){.research-toolbar{gap:12px;padding:12px 16px}}
</style>

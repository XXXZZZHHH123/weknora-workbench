<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { useTheme } from '@/composables/useTheme'
import { getKnowledgeBaseById } from '@/api/knowledge-base'
import KnowledgeWorkbenchLink from './KnowledgeWorkbenchLink.vue'
import { workspaceErrorMessage, workspaceIdentity } from './workspaceForm'

const route = useRoute(), router = useRouter(), auth = useAuthStore()
const { effectiveTheme } = useTheme()
const kbId = computed(() => String(route.params.kbId || ''))
const tenant = computed(() => auth.selectedTenantId ?? auth.tenant?.id)
const scope = computed(() => `${workspaceIdentity(tenant.value, auth.user?.id)}:${kbId.value}`)
const mode = computed(() => route.query.mode === 'query' ? 'query' : 'import')
const sourceKnowledgeId = computed(() => route.query.view !== 'model-settings' && typeof route.query.source_knowledge_id === 'string' ? route.query.source_knowledge_id : '')
const library = ref<any>(null), frame = ref<HTMLIFrameElement>(), frameGeneration = ref(0), loading = ref(false), error = ref('')
const src = computed(() => `/structured/?session=${frameGeneration.value}${route.query.view === 'model-settings' ? '&view=model-settings' : ''}`)
let generation = 0
async function load() {
  const own = ++generation
  library.value = null; error.value = ''; loading.value = true; frameGeneration.value++
  try {
    const detail = await getKnowledgeBaseById(kbId.value)
    if (own !== generation) return
    library.value = detail.data || detail
  } catch (e) { if (own === generation) error.value = workspaceErrorMessage(e) }
  finally { if (own === generation) loading.value = false }
}
function sendView() { frame.value?.contentWindow?.postMessage({ type: 'structured-view', mode: mode.value }, window.location.origin) }
function sendTheme() { frame.value?.contentWindow?.postMessage({ type: 'workspace-theme', mode: effectiveTheme.value }, window.location.origin) }
function sendAuth() {
  if (!library.value || !auth.token || !tenant.value) return
  frame.value?.contentWindow?.postMessage({ type: 'structured-auth', token: auth.token, tenant: tenant.value, knowledgeBaseId: kbId.value, sourceKnowledgeId: sourceKnowledgeId.value }, window.location.origin)
  sendView()
}
function receive(event: MessageEvent) {
  if (event.origin !== window.location.origin || event.source !== frame.value?.contentWindow) return
  if (event.data?.type === 'workspace-theme-ready') { sendTheme(); return }
  if (event.data?.type === 'structured-ready') sendAuth()
  if (event.data?.type === 'structured-navigate') {
    if (event.data.view === 'conversation') router.push(`/platform/knowledge-bases/${kbId.value}/creatChat`)
    else if (event.data.view === 'materials') router.push({ path: route.path, query: { mode: 'import' } })
    else router.push({ path: route.path, query: { ...route.query, view: event.data.view === 'model-settings' ? 'model-settings' : undefined } })
  }
}
watch(scope, load, { immediate: true })
watch(() => auth.token, sendAuth)
watch(sourceKnowledgeId, sendAuth)
watch(mode, sendView)
watch(effectiveTheme, sendTheme)
watch(() => route.query.mode, (value) => {
  if (value === 'source') router.replace({ path: route.path, query: { ...route.query, mode: 'query' }, hash: route.hash })
}, { immediate: true })
onMounted(() => window.addEventListener('message', receive))
onBeforeUnmount(() => { generation++; window.removeEventListener('message', receive) })
</script>

<template>
  <div class="knowledge-structured-workspace">
    <header class="knowledge-structured-toolbar">
      <div class="knowledge-structured-heading">
        <h1>结构化数据库</h1>
        <div class="knowledge-structured-context">
          <span class="knowledge-structured-library" :title="library?.name || '知识库'">{{ library?.name || '知识库' }}</span>
          <span class="knowledge-structured-mode">{{ route.query.view === 'model-settings' ? '字段模型设置' : mode === 'query' ? '数据查询' : 'Excel 导入' }}</span>
        </div>
      </div>
      <nav class="knowledge-structured-actions" aria-label="结构化数据库操作">
        <router-link class="knowledge-structured-legacy" to="/platform/structured-data" title="旧 Excel 保留在原空间，尚未自动分配到本库">原空间表格</router-link>
        <router-link :to="`/platform/knowledge-bases/${kbId}`"><t-icon name="folder" aria-hidden="true" />目录</router-link>
        <router-link :to="{ path: route.path, query: { ...route.query, view: route.query.view ? undefined : 'model-settings' } }"><t-icon :name="route.query.view ? 'arrow-left' : 'setting'" aria-hidden="true" />{{ route.query.view ? '返回数据库' : '字段模型设置' }}</router-link>
        <KnowledgeWorkbenchLink :kb-id="kbId" />
      </nav>
    </header>
    <div v-if="error" class="knowledge-structured-status" role="alert"><p>{{ error }}</p><t-button variant="outline" @click="load">重试</t-button></div>
    <div v-else-if="loading" class="knowledge-structured-status"><t-loading text="正在打开当前知识库" /></div>
    <template v-else-if="library">

      <iframe :key="frameGeneration" ref="frame" :src="src" title="当前知识库结构化数据工作台" @load="sendAuth" />
    </template>
  </div>
</template>

<style scoped>
.knowledge-structured-workspace {
  --structured-accent: var(--td-brand-color);
  --structured-accent-soft: var(--td-brand-color-light);
  display: flex;
  flex-direction: column;
  width: 100%;
  height: 100%;
  min-width: 0;
  min-height: 0;
  background: var(--app-card-bg);
  color: var(--td-text-color-primary);
}
.knowledge-structured-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: var(--app-space-3) var(--app-space-6);
  padding: var(--app-space-3) var(--app-space-6);
  background: var(--app-card-bg);
  border-bottom: 1px solid var(--app-border-color);
  flex: none;
}
.knowledge-structured-heading { min-width: 0; }
.knowledge-structured-heading h1 {
  margin: 0;
  font-size: var(--app-text-xl);
  font-weight: 600;
  line-height: 1.5;
  letter-spacing: -.01em;
}
.knowledge-structured-context {
  display: flex;
  align-items: center;
  gap: var(--app-space-2);
  margin-top: 2px;
  font-size: var(--app-text-sm);
  line-height: 1.6;
}
.knowledge-structured-library {
  max-width: 260px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--app-text-secondary);
}
.knowledge-structured-mode {
  display: inline-flex;
  flex: none;
  align-items: center;
  gap: 6px;
  color: var(--structured-accent);
}
.knowledge-structured-mode::before {
  content: '';
  width: 3px;
  height: 3px;
  border-radius: var(--app-radius-pill);
  background: var(--app-border-color);
}
.knowledge-structured-actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: flex-end;
  gap: var(--app-space-1);
  min-width: 0;
}
.knowledge-structured-actions a {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  min-height: 32px;
  padding: 0 var(--app-space-2);
  border-radius: var(--app-radius-sm);
  color: var(--app-text-secondary);
  font-size: var(--app-text-sm);
  line-height: 1.5;
  text-decoration: none;
  white-space: nowrap;
  transition: color var(--app-motion-fast), background-color var(--app-motion-fast);
}
.knowledge-structured-actions a:hover {
  color: var(--structured-accent);
  background: var(--structured-accent-soft);
}
.knowledge-structured-actions a:focus-visible {
  outline: 2px solid var(--structured-accent);
  outline-offset: 2px;
}
.knowledge-structured-actions :deep(.t-button) {
  margin-left: var(--app-space-2);
  height: 32px;
  border-radius: var(--app-radius-sm);
}
.knowledge-structured-status { padding: var(--app-space-6); }
.knowledge-structured-workspace iframe {
  display: block;
  width: 100%;
  flex: 1;
  min-height: 360px;
  border: 0;
}
:global(:root[theme-mode="dark"]) .knowledge-structured-workspace { --structured-accent: var(--td-brand-color); }
@media (max-width: 900px) {
  .knowledge-structured-toolbar { gap: var(--app-space-2); padding: var(--app-space-3) var(--app-space-4); }
  .knowledge-structured-actions { justify-content: flex-start; }
  .knowledge-structured-actions :deep(.t-button) { margin-left: 0; }
}
@media (max-width: 600px) {
  .knowledge-structured-toolbar { align-items: flex-start; }
  .knowledge-structured-actions { width: 100%; }
  .knowledge-structured-library { max-width: 190px; }
  .knowledge-structured-actions a { padding: 0 6px; }
}
@media (prefers-reduced-motion: reduce) {
  .knowledge-structured-actions a { transition: none; }
}
</style>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { getKnowledgeBaseById } from '@/api/knowledge-base'
import { getDefaultKnowledgeBase } from '@/api/workspaces'
import { createMaterialClient } from '@/views/materials/client'
import KnowledgeWorkbenchLink from './KnowledgeWorkbenchLink.vue'
import { workspaceErrorMessage, workspaceIdentity } from './workspaceForm'

type MacroSource = { series_id: string; name: string; source: string; frequency: string; unit: string; status: string; registered: boolean }
const route = useRoute(), router = useRouter(), auth = useAuthStore()
const kbId = computed(() => String(route.params.kbId || ''))
const tenant = computed(() => auth.selectedTenantId ?? auth.tenant?.id)
const scope = computed(() => `${workspaceIdentity(tenant.value, auth.user?.id)}:${kbId.value}`)
const mode = computed(() => ['import', 'query', 'source'].includes(String(route.query.mode)) ? String(route.query.mode) : 'import')
const library = ref<any>(null), frame = ref<HTMLIFrameElement>(), frameGeneration = ref(0), loading = ref(false), error = ref('')
const macro = ref(false), macroSources = ref<MacroSource[]>([]), macroError = ref('')
const src = computed(() => `/structured/?session=${frameGeneration.value}${route.query.view === 'model-settings' ? '&view=model-settings' : ''}`)
let generation = 0
function materialScope() { return { token: auth.token, userId: String(auth.user?.id || ''), tenantId: String(tenant.value || '') } }
async function load() {
  const own = ++generation
  library.value = null; macro.value = false; macroSources.value = []; error.value = ''; macroError.value = ''; loading.value = true; frameGeneration.value++
  try {
    const [detail, binding] = await Promise.all([getKnowledgeBaseById(kbId.value), getDefaultKnowledgeBase()])
    if (own !== generation) return
    library.value = detail.data || detail
    macro.value = binding.data?.knowledge_base_id === kbId.value && binding.data.kind === 'macro'
    if (macro.value) {
      const captured = materialScope(), client = createMaterialClient(captured, materialScope)
      try {
        const response = await client.get('/api/sn/indicators/catalog?scope=macro')
        if (own === generation) macroSources.value = response.indicators || []
      } catch (e) { if (own === generation) macroError.value = workspaceErrorMessage(e) }
    }
  } catch (e) { if (own === generation) error.value = workspaceErrorMessage(e) }
  finally { if (own === generation) loading.value = false }
}
function sendView() { frame.value?.contentWindow?.postMessage({ type: 'structured-view', mode: mode.value }, window.location.origin) }
function sendAuth() {
  if (!library.value || !auth.token || !tenant.value) return
  frame.value?.contentWindow?.postMessage({ type: 'structured-auth', token: auth.token, tenant: tenant.value, knowledgeBaseId: kbId.value }, window.location.origin)
  sendView()
}
function receive(event: MessageEvent) {
  if (event.origin !== window.location.origin || event.source !== frame.value?.contentWindow) return
  if (event.data?.type === 'structured-ready') sendAuth()
  if (event.data?.type === 'structured-navigate') {
    if (event.data.view === 'conversation') router.push(`/platform/knowledge-bases/${kbId.value}/creatChat`)
    else if (event.data.view === 'materials') router.push({ path: route.path, query: { mode: 'import' } })
    else router.push({ path: route.path, query: { ...route.query, view: event.data.view === 'model-settings' ? 'model-settings' : undefined } })
  }
}
watch(scope, load, { immediate: true })
watch(() => auth.token, sendAuth)
watch(mode, sendView)
onMounted(() => window.addEventListener('message', receive))
onBeforeUnmount(() => { generation++; window.removeEventListener('message', receive) })
</script>

<template>
  <div class="knowledge-structured-workspace">
    <header class="knowledge-structured-toolbar"><div><strong>{{ library?.name || '知识库' }} · 结构化数据库</strong><span>{{ mode === 'query' ? '数据查询' : mode === 'source' ? '原表查看' : 'Excel 导入' }}</span></div><div class="knowledge-structured-actions"><KnowledgeWorkbenchLink :kb-id="kbId" /><router-link to="/platform/structured-data" title="旧 Excel 保留在原空间，尚未自动分配到本库">原空间表格</router-link><router-link :to="`/platform/knowledge-bases/${kbId}`">转到目录</router-link><router-link :to="{ path: route.path, query: { ...route.query, view: route.query.view ? undefined : 'model-settings' } }">{{ route.query.view ? '返回工作台' : '字段模型设置' }}</router-link></div></header>
    <div v-if="error" class="knowledge-structured-status" role="alert"><p>{{ error }}</p><t-button variant="outline" @click="load">重试</t-button></div>
    <div v-else-if="loading" class="knowledge-structured-status"><t-loading text="正在打开当前知识库" /></div>
    <template v-else-if="library">
      <section v-if="macro && mode !== 'import' && !route.query.view" class="knowledge-macro-sources"><h3>Default · 内置宏观来源</h3><p>来源目录显示登记信息；最新数值和更新状态在“查看指标与来源”中查看。自动序列不作为 Excel 原件显示。下方可另行导入本库 Excel。</p><t-alert v-if="macroError" theme="error" :message="macroError" /><table v-else><thead><tr><th>名称</th><th>来源</th><th>频率 / 单位</th><th>登记状态</th><th></th></tr></thead><tbody><tr v-for="source in macroSources" :key="source.series_id"><td>{{ source.name }}</td><td>{{ source.source }}</td><td>{{ source.frequency }} / {{ source.unit }}</td><td>{{ source.registered ? source.status : '尚未登记' }}</td><td><router-link v-if="source.registered" :to="{ path: '/platform/commodity-research', query: { indicator: source.series_id } }">查看指标与来源</router-link><span v-else class="knowledge-macro-pending">登记后可查看详情</span></td></tr></tbody></table></section>
      <iframe :key="frameGeneration" ref="frame" :src="src" title="当前知识库结构化数据工作台" @load="sendAuth" />
    </template>
  </div>
</template>

<style scoped>
.knowledge-structured-workspace{display:flex;flex-direction:column;width:100%;height:100%;min-height:0;background:var(--app-bg)}
.knowledge-structured-toolbar{display:flex;align-items:center;justify-content:space-between;gap:16px;padding:16px 24px;background:var(--app-card-bg);border-bottom:1px solid var(--app-border-color);flex-wrap:wrap}
.knowledge-structured-toolbar>div{display:flex;align-items:center;gap:20px}.knowledge-structured-toolbar span{font-size:var(--app-text-md);color:var(--app-text-secondary)}
.knowledge-structured-actions{flex-wrap:wrap}.knowledge-structured-actions a{color:var(--td-brand-color);font-size:var(--app-text-md);text-decoration:none}
.knowledge-structured-status{padding:24px}.knowledge-structured-workspace iframe{width:100%;flex:1;min-height:360px;border:0}
.knowledge-macro-sources{padding:20px 24px;background:var(--app-card-bg);border-bottom:1px solid var(--app-border-color);max-height:260px;overflow:auto;flex:none}
.knowledge-macro-sources h3{margin:0 0 8px;font-size:var(--app-text-lg)}.knowledge-macro-sources p{font-size:var(--app-text-md);color:var(--app-text-secondary);line-height:1.6}
.knowledge-macro-sources table{width:100%;border-collapse:collapse;font-size:var(--app-text-md)}.knowledge-macro-sources th,.knowledge-macro-sources td{text-align:left;border-bottom:1px solid var(--app-border-color);padding:8px}.knowledge-macro-sources a{color:var(--td-brand-color)}
.knowledge-macro-pending{color:var(--app-text-secondary)}
</style>

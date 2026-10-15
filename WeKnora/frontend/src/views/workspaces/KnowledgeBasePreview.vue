<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { getKnowledgeBaseById, listKnowledgeFolders, type KnowledgeFolderTree } from '@/api/knowledge-base'
import { listResearchWorkbenches, type ResearchWorkbench } from '@/api/workspaces'
import { workspaceErrorMessage, workspaceIdentity } from './workspaceForm'
import './workspaces.css'

const route = useRoute(), router = useRouter(), auth = useAuthStore()
const kbId = computed(() => String(route.params.kbId || ''))
const identity = computed(() => workspaceIdentity(auth.selectedTenantId ?? auth.tenant?.id, auth.user?.id))
const library = ref<{ id: string; name: string; description?: string; type?: string } | null>(null), tree = ref<KnowledgeFolderTree | null>(null), workbenches = ref<ResearchWorkbench[]>([])
const loading = ref(false), error = ref(''), choosing = ref(false), bindingKnown = ref(false)
let generation = 0
async function load() {
  const own = ++generation
  library.value = null; tree.value = null; workbenches.value = []; choosing.value = false; bindingKnown.value = false; error.value = ''; loading.value = true
  try {
    const detail = await getKnowledgeBaseById(kbId.value)
    if (own !== generation) return
    library.value = detail.data || detail
    const results = await Promise.allSettled([listResearchWorkbenches(), listKnowledgeFolders(kbId.value)])
    if (own !== generation) return
    if (results[0].status === 'fulfilled') {
      workbenches.value = results[0].value.data.filter(w => w.knowledge_base_id === kbId.value)
      bindingKnown.value = true
    } else error.value = workspaceErrorMessage(results[0].reason)
    if (results[1].status === 'fulfilled') tree.value = results[1].value.data
    else error.value = [error.value, workspaceErrorMessage(results[1].reason)].filter(Boolean).join('；')
  } catch (e) { if (own === generation) error.value = workspaceErrorMessage(e) }
  finally { if (own === generation) loading.value = false }
}
watch([identity, kbId], load, { immediate: true })
onBeforeUnmount(() => { generation++ })
function bridge() {
  if (!bindingKnown.value || loading.value) return
  if (!workbenches.value.length) router.push({ path: '/platform/research-workbenches/new', query: { kb_id: kbId.value } })
  else if (workbenches.value.length === 1) router.push(`/platform/research-workbenches/${workbenches.value[0]!.id}`)
  else choosing.value = true
}
</script>

<template>
  <div class="workspace-management"><main class="workspace-management__content"><header class="workspace-management__header"><div><h2>{{ library?.name || '知识库预览' }}</h2><p>{{ library?.description || '预览当前知识库，再进入原有文件管理页面。' }}</p></div><t-button variant="outline" @click="router.push('/platform/knowledge-bases')">全部知识库</t-button></header>
    <t-alert v-if="error" theme="error" :message="error" class="workspace-management__error" />
    <div v-if="loading" class="workspace-management__empty"><t-loading text="正在加载知识库" /></div>
    <div v-else-if="library" class="workspace-management__details"><section class="workspace-management__panel"><h3>一级预览</h3><p v-if="tree">{{ tree.total_document_count }} 个知识条目 · {{ tree.folders.length }} 个一级目录</p><div v-for="folder in tree?.folders || []" :key="folder.path" class="workspace-management__row"><strong>{{ folder.name }}</strong><small>{{ folder.total_count }} 个条目</small></div><p v-if="tree && !tree.total_document_count">还没有文件，可以进入知识库添加资料。</p><div class="workspace-management__actions"><t-button @click="router.push(`/platform/knowledge-bases/${kbId}`)">进入知识库</t-button></div></section><aside class="workspace-management__panel"><h3>研究工作台</h3><p>{{ !bindingKnown ? '绑定状态未能加载，请重试' : workbenches.length ? `已绑定 ${workbenches.length} 个工作台` : '尚未建立研究工作台' }}</p><p>研究工作台基于这个知识库创建，进入后保留现有研究模式。</p><t-button variant="outline" :disabled="!bindingKnown || loading" @click="bridge">{{ workbenches.length ? '转到工作台' : '创建工作台' }}</t-button></aside></div>
    <t-button v-if="error" variant="outline" @click="load">重新加载</t-button>
  </main><t-dialog v-model:visible="choosing" header="选择工作台" :footer="false"><div v-for="workbench in workbenches" :key="workbench.id" class="workspace-management__row"><strong>{{ workbench.name }}</strong><t-button @click="router.push(`/platform/research-workbenches/${workbench.id}`)">转到工作台</t-button></div></t-dialog></div>
</template>

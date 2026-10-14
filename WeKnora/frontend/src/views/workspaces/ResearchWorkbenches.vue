<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { useChatResourcesStore } from '@/stores/chatResources'
import { useOrganizationStore } from '@/stores/organization'
import KnowledgeBaseEditorModal from '@/views/knowledge/KnowledgeBaseEditorModal.vue'
import { createResearchWorkbench, getResearchWorkbench, listResearchWorkbenches, type ResearchWorkbench } from '@/api/workspaces'
import { validateWorkbenchSource, validateWorkspaceName, workspaceErrorMessage as message, workspaceIdentity } from './workspaceForm'
import { workspaceKnowledgeBases } from './workspaceKnowledgeBases'
import './workspaces.css'

const route = useRoute(), router = useRouter(), auth = useAuthStore(), resources = useChatResourcesStore(), organizations = useOrganizationStore()
const identity = computed(() => workspaceIdentity(auth.selectedTenantId ?? auth.tenant?.id, auth.user?.id))
const id = computed(() => String(route.params.workbenchId || ''))
const creating = computed(() => route.name === 'researchWorkbenchCreate')
const items = ref<ResearchWorkbench[]>([]), current = ref<ResearchWorkbench | null>(null), loading = ref(false), busy = ref(false), error = ref('')
const step = ref(1), kbId = ref(''), name = ref(''), description = ref(''), kbEditorOpen = ref(false)
const sharedKnowledgeBaseIdentity = ref('')
const libraries = computed(() => workspaceKnowledgeBases(resources.rawKnowledgeBases, organizations.sharedKnowledgeBases, identity.value, sharedKnowledgeBaseIdentity.value))
const selectedLibrary = computed(() => libraries.value.find(k => k.id === kbId.value))
const currentLibrary = computed(() => libraries.value.find(k => k.id === current.value?.knowledge_base_id))
const existing = computed(() => items.value.filter(w => w.knowledge_base_id === kbId.value))
let generation = 0, creationKey = crypto.randomUUID()
function changed() { window.dispatchEvent(new CustomEvent('weknora:workspaces-changed')) }
async function load() {
  const own = ++generation, capturedIdentity = identity.value
  items.value = []; current.value = null; loading.value = true; error.value = ''
  try {
    const [res, detail] = await Promise.all([listResearchWorkbenches(), id.value ? getResearchWorkbench(id.value) : Promise.resolve(null), resources.ensureKnowledgeBases()])
    if (own !== generation) return
    await organizations.fetchSharedKnowledgeBases({ force: sharedKnowledgeBaseIdentity.value !== capturedIdentity })
    if (own !== generation || capturedIdentity !== identity.value) return
    sharedKnowledgeBaseIdentity.value = capturedIdentity
    items.value = res.data; current.value = detail?.data || null
    if (creating.value && typeof route.query.kb_id === 'string' && libraries.value.some(k => k.id === route.query.kb_id)) kbId.value = route.query.kb_id
  } catch (e) { if (own === generation) error.value = message(e) }
  finally { if (own === generation) loading.value = false }
}
watch([identity, id, creating, () => route.query.kb_id], () => { if (sharedKnowledgeBaseIdentity.value !== identity.value) sharedKnowledgeBaseIdentity.value = ''; step.value = 1; kbId.value = ''; name.value = ''; description.value = ''; busy.value = false; kbEditorOpen.value = false; creationKey = crypto.randomUUID(); void load() }, { immediate: true })
watch([kbId, name, description], () => { if (!busy.value) creationKey = crypto.randomUUID() })
onBeforeUnmount(() => { generation++ })
function next() {
  error.value = validateWorkbenchSource(kbId.value, libraries.value.map(k => k.id))
  if (error.value) return
  if (step.value === 1) { if (!name.value) name.value = `${selectedLibrary.value?.name || ''}工作台`; step.value = 2; return }
  error.value = validateWorkspaceName(name.value)
  if (!error.value) step.value = 3
}
async function create() {
  if (busy.value) return
  error.value = validateWorkbenchSource(kbId.value, libraries.value.map(k => k.id)) || validateWorkspaceName(name.value)
  if (error.value) return
  const own = generation
  busy.value = true
  try {
    const res = await createResearchWorkbench({ name: name.value.trim(), description: description.value.trim(), knowledge_base_id: kbId.value, idempotency_key: creationKey })
    if (own !== generation) return
    changed(); await router.push(`/platform/research-workbenches/${res.data.id}`)
  } catch (e) { if (own === generation) error.value = message(e) }
  finally { if (own === generation) busy.value = false }
}
async function knowledgeCreated(newId: string) {
  const own = generation
  try {
    await resources.ensureKnowledgeBases(true)
    if (own !== generation) return
    kbId.value = newId; kbEditorOpen.value = false
    window.dispatchEvent(new CustomEvent('weknora:workspaces-changed'))
  } catch (e) { if (own === generation) error.value = message(e) }
}
</script>

<template>
  <div class="workspace-management"><main class="workspace-management__content">
    <header class="workspace-management__header"><div><h2>{{ creating ? '新建研究工作台' : current?.name || '研究工作台' }}</h2><p>{{ creating ? '选择主知识库，填写研究信息，核对后进入现有研究页面。' : current?.description || '先预览绑定的知识库，再进入研究工作台。' }}</p></div><div class="workspace-management__actions"><t-button v-if="creating || id" variant="outline" @click="router.push('/platform/research-workbenches')">全部工作台</t-button><t-button v-else @click="router.push('/platform/research-workbenches/new')">新建工作台</t-button></div></header>
    <t-alert v-if="error" theme="error" :message="error" class="workspace-management__error" />
    <div v-if="loading" class="workspace-management__empty"><t-loading text="正在加载工作台" /></div>
    <form v-else-if="creating" class="workspace-management__form" @submit.prevent="step === 3 ? create() : next()">
      <nav class="workspace-management__steps" aria-label="创建进度"><span :class="{ 'is-current': step === 1 }">1 选择知识库</span><span :class="{ 'is-current': step === 2 }">2 填写研究信息</span><span :class="{ 'is-current': step === 3 }">3 核对并创建</span></nav>
      <section class="workspace-management__panel">
        <template v-if="step === 1"><h3>选择主知识库</h3><p>每个工作台基于一个知识库；已绑定的库仍可以用于新的研究工作台。</p>
          <label v-for="kb in libraries" :key="kb.id" class="workspace-management__choice" :class="{ 'is-selected': kbId === kb.id }"><input v-model="kbId" type="radio" name="knowledge-base" :value="kb.id" /><span><strong>{{ kb.name }}</strong><small>{{ kb.description || '当前可访问的知识库' }}</small></span></label>
          <p v-if="!libraries.length">还没有可用知识库，请先创建。</p>
          <div v-if="existing.length" class="workspace-management__confirm"><h3>此库已有工作台</h3><div v-for="workbench in existing" :key="workbench.id" class="workspace-management__row"><strong>{{ workbench.name }}</strong><t-button variant="text" @click="router.push(`/platform/research-workbenches/${workbench.id}/preview`)">查看</t-button></div></div>
          <t-button v-if="auth.hasRole('contributor')" variant="text" @click="kbEditorOpen = true">先新建知识库</t-button>
        </template>
        <template v-else-if="step === 2"><h3>填写研究信息</h3><p>主知识库：{{ selectedLibrary?.name }}</p><label class="workspace-management__field"><span>工作台名称</span><input v-model="name" maxlength="80" required :disabled="busy" /></label><label class="workspace-management__field"><span>研究主题说明</span><textarea v-model="description" maxlength="1000" :disabled="busy" /></label></template>
        <template v-else><h3>核对并创建</h3><div class="workspace-management__confirm"><p>工作台：{{ name.trim() }}</p><p>主知识库：{{ selectedLibrary?.name }}</p><p>说明：{{ description.trim() || '未填写' }}</p></div><p>进入后沿用现有研究页面与导航。研究事实由当前空间共享，判断和策略按当前账号保存；主知识库用于资料导航与问答。</p><p>创建不会自动生成研究结论或调用模型。</p></template>
        <div class="workspace-management__actions"><t-button v-if="step > 1" variant="outline" :disabled="busy" @click="step--; error = ''">上一步</t-button><t-button variant="outline" :disabled="busy" @click="router.push('/platform/research-workbenches')">取消</t-button><t-button v-if="step < 3" type="submit" :disabled="!kbId || loading">下一步</t-button><t-button v-else type="submit" :loading="busy">创建并进入</t-button></div>
      </section>
    </form>
    <section v-else-if="current" class="workspace-management__panel"><h3>工作台一级预览</h3><div class="workspace-management__confirm"><p>依托知识库：{{ currentLibrary?.name || current.knowledge_base_id }}</p><p>研究事实由当前空间共享，研究成果按当前账号保存。问答助手位于右侧可收起的常驻侧栏，并使用绑定知识库。</p></div><div class="workspace-management__actions"><t-button @click="router.push(`/platform/research-workbenches/${current.id}`)">进入工作台</t-button><t-button variant="outline" @click="router.push(`/platform/knowledge-bases/${current.knowledge_base_id}`)">转到知识库</t-button></div></section>
    <div v-else-if="items.length" class="workspace-management__grid"><article v-for="workbench in items" :key="workbench.id" class="workspace-management__card"><h3>{{ workbench.name }}</h3><p>{{ workbench.description || '研究工作台' }}</p><p>主知识库：{{ libraries.find(k => k.id === workbench.knowledge_base_id)?.name || workbench.knowledge_base_id }}</p><div class="workspace-management__actions"><t-button variant="outline" @click="router.push(`/platform/research-workbenches/${workbench.id}/preview`)">一级预览</t-button><t-button @click="router.push(`/platform/research-workbenches/${workbench.id}`)">进入工作台</t-button></div></article></div>
    <div v-else-if="!error" class="workspace-management__empty"><p>还没有研究工作台。先选择知识库建立工作台。</p><t-button @click="router.push('/platform/research-workbenches/new')">新建工作台</t-button><t-button variant="text" @click="router.push('/platform/commodity-research')">打开现有研究页面</t-button></div>
    <t-button v-if="error" variant="outline" @click="load">重新加载</t-button>
  </main><KnowledgeBaseEditorModal v-model:visible="kbEditorOpen" mode="create" @success="knowledgeCreated" /></div>
</template>

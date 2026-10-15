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
import { createRandomUUID } from '@/utils/uuid'
import { workbenchTemplates, workbenchModuleNames, templateById, type WorkbenchTemplateId } from '@/views/research/workbenchTemplates'
import './workspaces.css'

const route = useRoute(), router = useRouter(), auth = useAuthStore(), resources = useChatResourcesStore(), organizations = useOrganizationStore()
const identity = computed(() => workspaceIdentity(auth.selectedTenantId ?? auth.tenant?.id, auth.user?.id))
const id = computed(() => String(route.params.workbenchId || ''))
const creating = computed(() => route.name === 'researchWorkbenchCreate')
const items = ref<ResearchWorkbench[]>([]), current = ref<ResearchWorkbench | null>(null), loading = ref(false), busy = ref(false), error = ref('')
const templateId = ref<WorkbenchTemplateId>('industrial')
const kbId = ref(''), name = ref(''), description = ref(''), kbEditorOpen = ref(false)
const sharedKnowledgeBaseIdentity = ref('')
const libraries = computed(() => workspaceKnowledgeBases(resources.rawKnowledgeBases, organizations.sharedKnowledgeBases, identity.value, sharedKnowledgeBaseIdentity.value))
const selectedLibrary = computed(() => libraries.value.find(k => k.id === kbId.value))
const currentLibrary = computed(() => libraries.value.find(k => k.id === current.value?.knowledge_base_id))
const existing = computed(() => items.value.filter(w => w.knowledge_base_id === kbId.value))
let generation = 0, creationKey = createRandomUUID()
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
watch([identity, id, creating, () => route.query.kb_id], () => { if (sharedKnowledgeBaseIdentity.value !== identity.value) sharedKnowledgeBaseIdentity.value = ''; templateId.value = 'industrial'; kbId.value = ''; name.value = ''; description.value = ''; busy.value = false; kbEditorOpen.value = false; creationKey = createRandomUUID(); void load() }, { immediate: true })
watch([kbId, name, description, templateId], () => { if (!busy.value) creationKey = createRandomUUID() })
onBeforeUnmount(() => { generation++ })
watch(kbId, (value, previous) => {
  const oldName = libraries.value.find(k => k.id === previous)?.name
  if (!name.value || name.value === `${oldName || ''}工作台`) name.value = `${libraries.value.find(k => k.id === value)?.name || ''}工作台`
})
async function create() {
  if (busy.value) return
  error.value = validateWorkbenchSource(kbId.value, libraries.value.map(k => k.id)) || validateWorkspaceName(name.value)
  if (error.value) return
  const own = generation
  busy.value = true
  try {
    const res = await createResearchWorkbench({ name: name.value.trim(), description: description.value.trim(), knowledge_base_id: kbId.value, template_id: templateId.value, template_version: 1, idempotency_key: creationKey })
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
  <div class="workspace-management research-management"><main class="workspace-management__content">
    <header class="workspace-management__header"><div><h2>{{ creating ? '新建研究工作台' : current?.name || '研究工作台' }}</h2><p>{{ creating ? '选择知识库和起步模板，稍后在工作台内准备资料。' : current?.description || '每个新工作台都有独立的资料范围和研究成果。' }}</p></div><div class="workspace-management__actions"><t-button v-if="creating || id" size="small" variant="outline" @click="router.push('/platform/research-workbenches')">全部工作台</t-button><t-button v-else size="small" @click="router.push('/platform/research-workbenches/new')">新建工作台</t-button></div></header>
    <t-alert v-if="error" theme="error" :message="error" class="workspace-management__error" />
    <div v-if="loading" class="workspace-management__empty"><t-loading text="正在加载工作台" /></div>
    <form v-else-if="creating" class="research-create" @submit.prevent="create">
      <div v-if="!items.length" class="research-create__first"><t-icon name="info-circle" />第一次创建？先选知识库，创建后按“导入资料 → 核对来源 → 开始研究”继续。</div>
      <section class="workspace-management__panel research-create__panel">
        <div class="research-create__field"><label for="workbench-source">绑定知识库</label><div class="research-create__control"><select id="workbench-source" v-model="kbId" :disabled="busy"><option value="" disabled>选择知识库</option><option v-for="kb in libraries" :key="kb.id" :value="kb.id">{{ kb.name }}</option></select><t-button v-if="auth.hasRole('contributor')" size="small" variant="text" :disabled="busy" @click="kbEditorOpen = true">新建</t-button></div></div>
        <div class="research-create__field"><label for="workbench-name">工作台名称</label><input id="workbench-name" v-model="name" maxlength="80" required :disabled="busy" placeholder="填写名称" /></div>
        <div v-if="existing.length" class="research-create__existing"><span>此库已有 {{ existing.length }} 个工作台</span><t-button v-for="workbench in existing" :key="workbench.id" size="small" variant="text" @click="router.push(`/platform/research-workbenches/${workbench.id}`)">{{ workbench.name }}</t-button></div>
        <div class="research-create__label"><span>起步模板</span><small>只建立功能与证据要求，不预置指标、观点或结果</small></div>
        <div role="radiogroup" aria-label="研究工作台模板"><label v-for="template in workbenchTemplates" :key="template.id" class="research-template" :class="{ 'is-selected': templateId === template.id }"><input v-model="templateId" type="radio" name="workbench-template" :value="template.id" :disabled="busy" /><span><strong>{{ template.name }}</strong><small>{{ template.description }}</small></span></label></div>
        <div class="research-create__modules" aria-label="所选模板功能"><span v-for="module in templateById(templateId).modules" :key="module">{{ workbenchModuleNames[module] || module }}</span></div>
        <label class="research-create__field"><span>说明<small>（选填）</small></span><input v-model="description" maxlength="1000" :disabled="busy" placeholder="简短说明研究范围" /></label>
        <p class="research-create__note">仅使用绑定知识库的资料。没有文件也能先创建，随后按引导准备。</p>
        <div class="workspace-management__actions research-create__footer"><t-button size="small" variant="outline" :disabled="busy" @click="router.push(typeof route.query.kb_id === 'string' ? `/platform/knowledge-bases/${route.query.kb_id}` : '/platform/research-workbenches')">取消</t-button><t-button size="small" type="submit" :loading="busy" :disabled="!kbId || loading">创建工作台</t-button></div>
      </section>
    </form>
    <section v-else-if="current" class="workspace-management__panel"><h3>工作台预览</h3><div class="workspace-management__confirm"><p>依托知识库：{{ currentLibrary?.name || current.knowledge_base_id }}</p><p v-if="current.workspace_mode === 'isolated'">{{ templateById(current.template_id || 'blank').name }} · 独立研究范围。进入后可继续资料准备和首次引导。</p><p v-else>原有研究工作台沿用原研究模式，主知识库用于资料导航与问答。</p></div><div class="workspace-management__actions"><t-button size="small" @click="router.push(`/platform/research-workbenches/${current.id}`)">进入工作台</t-button><t-button size="small" variant="outline" @click="router.push(`/platform/knowledge-bases/${current.knowledge_base_id}`)">转到知识库</t-button></div></section>
    <div v-else-if="items.length" class="workspace-management__grid"><article v-for="workbench in items" :key="workbench.id" class="workspace-management__card"><h3>{{ workbench.name }}</h3><p>{{ workbench.description || (workbench.workspace_mode === 'isolated' ? templateById(workbench.template_id || 'blank').name : '原有研究工作台') }}</p><p>知识库：{{ libraries.find(k => k.id === workbench.knowledge_base_id)?.name || workbench.knowledge_base_id }}</p><div class="workspace-management__actions"><t-button size="small" variant="outline" @click="router.push(`/platform/research-workbenches/${workbench.id}/preview`)">一级预览</t-button><t-button size="small" @click="router.push(`/platform/research-workbenches/${workbench.id}`)">进入工作台</t-button></div></article></div>
    <div v-else-if="!error" class="workspace-management__empty"><p>还没有研究工作台。选择知识库和起步模板，创建后有三步引导帮助准备资料。</p><t-button size="small" @click="router.push('/platform/research-workbenches/new')">新建工作台</t-button></div>
    <t-button v-if="error" size="small" variant="outline" @click="load">重新加载</t-button>
  </main><KnowledgeBaseEditorModal v-model:visible="kbEditorOpen" mode="create" @success="knowledgeCreated" /></div>
</template>

<style scoped>
.research-create,.research-create *{box-sizing:border-box}.research-management h2{font-size:18px;margin-bottom:4px}.research-management .workspace-management__header{margin-bottom:16px}.research-management .workspace-management__header p{font-size:12px}.research-create{max-width:560px;margin:0 auto}.research-create__panel{padding:18px}.research-create__first{display:flex;align-items:flex-start;gap:7px;font-size:12px;line-height:1.7;color:var(--app-text-secondary);padding:9px 0 12px}.research-create__first .t-icon{flex:none;margin-top:3px;color:var(--td-brand-color)}.research-create__field{display:flex;align-items:center;gap:10px;margin-bottom:12px;font-size:12px}.research-create__field>label,.research-create__field>span{width:76px;flex:none}.research-create__field small{font-size:11px;color:var(--app-text-secondary)}.research-create__field input,.research-create__field select{height:32px;min-width:0;flex:1;padding:0 9px;color:var(--app-text-primary);background:var(--app-card-bg);border:1px solid var(--app-border-color);border-radius:6px;font:inherit}.research-create__field input:focus-visible,.research-create__field select:focus-visible{outline:2px solid var(--td-brand-color);outline-offset:2px}.research-create__control{display:flex;flex:1;min-width:0;gap:5px}.research-create__existing{display:flex;align-items:center;gap:4px;flex-wrap:wrap;font-size:11px;color:var(--app-text-secondary);margin-bottom:12px}.research-create__existing .t-button{font-size:11px;height:25px}.research-create__label{display:flex;justify-content:space-between;align-items:center;gap:8px;font-size:12px;margin:16px 0 8px}.research-create__label small{font-size:10px;color:var(--app-text-secondary)}.research-template{display:flex;align-items:center;gap:10px;min-height:60px;padding:9px 11px;margin-bottom:7px;border:1px solid var(--app-border-color);border-radius:6px;cursor:pointer;transition:background .15s,border-color .15s}.research-template:hover{background:var(--app-bg)}.research-template.is-selected{background:var(--td-brand-color-light);border-color:var(--td-brand-color)}.research-template input{accent-color:var(--td-brand-color);width:14px;height:14px;margin:0;flex:none}.research-template strong{display:block;font-size:13px;font-weight:500}.research-template small{display:block;font-size:11px;color:var(--app-text-secondary);margin-top:2px}.research-create__modules{display:flex;gap:5px;flex-wrap:wrap;margin:10px 0 14px}.research-create__modules span{border:1px solid var(--app-border-color);border-radius:4px;padding:1px 6px;font-size:11px;color:var(--app-text-secondary)}.research-create .research-create__note{font-size:11px;line-height:1.65;margin:0;color:var(--app-text-secondary)}.research-create__footer{justify-content:flex-end;margin-top:14px;padding-top:12px;border-top:1px solid var(--app-border-color)}
@media(max-width:600px){.research-management{padding:16px 12px}.research-create__panel{padding:13px}.research-create__label{flex-wrap:wrap;gap:3px}.research-create__label small{font-size:10px}.research-create__field>label,.research-create__field>span{width:65px}.research-template{padding:8px}.research-template small{font-size:10px}}
@media(prefers-reduced-motion:reduce){.research-template{transition:none}}
</style>

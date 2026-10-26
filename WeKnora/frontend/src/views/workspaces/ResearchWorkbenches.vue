<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { useChatResourcesStore } from '@/stores/chatResources'
import { useOrganizationStore } from '@/stores/organization'
import KnowledgeBaseEditorModal from '@/views/knowledge/KnowledgeBaseEditorModal.vue'
import ResourceListToolbar from '@/components/ResourceListToolbar.vue'
import ResourceSortControl from '@/components/ResourceSortControl.vue'
import EmptyState from '@/components/EmptyState.vue'
import WorkbenchActions from './WorkbenchActions.vue'
import WorkbenchSourcePicker from './WorkbenchSourcePicker.vue'
import { sortResourcesWithinGroups, type ResourceSortValue } from '@/utils/resourceSorting'
import { createResearchWorkbench, listResearchWorkbenches, listCentralDatasets, type CentralDataset, type ResearchWorkbench, type WorkbenchSourceInput } from '@/api/workspaces'
import { validateWorkspaceName, workspaceErrorMessage as message, workspaceIdentity } from './workspaceForm'
import { sourceOptions, validateSourceSelection, workbenchSourceLabel } from '@/views/research/workbenchDataSources'
import { workspaceKnowledgeBases } from './workspaceKnowledgeBases'
import { createRandomUUID } from '@/utils/uuid'
import { workbenchTemplates, workbenchModuleNames, templateById, type WorkbenchTemplateId } from '@/views/research/workbenchTemplates'
import { registerResearchGuide } from '@/views/research/researchFeatureGuides'
import './workspaces.css'

const route = useRoute(), router = useRouter(), auth = useAuthStore(), resources = useChatResourcesStore(), organizations = useOrganizationStore()
const identity = computed(() => workspaceIdentity(auth.selectedTenantId ?? auth.tenant?.id, auth.user?.id))
const creating = computed(() => route.name === 'researchWorkbenchCreate')
const items = ref<ResearchWorkbench[]>([]), loading = ref(false), busy = ref(false), error = ref('')
const keyword = ref(''), selectedResourceSort = ref<ResourceSortValue>('created_desc')
const templateId = ref<WorkbenchTemplateId>('industrial')
const kbId = ref(''), name = ref(''), description = ref(''), kbEditorOpen = ref(false)
const central = ref<CentralDataset[]>([]), selectedSources = ref<WorkbenchSourceInput[]>([])
const availableSources = computed(() => sourceOptions(libraries.value, central.value))
const sharedKnowledgeBaseIdentity = ref('')
const libraries = computed(() => workspaceKnowledgeBases(resources.rawKnowledgeBases, organizations.sharedKnowledgeBases, identity.value, sharedKnowledgeBaseIdentity.value))
function sourceName(workbench: ResearchWorkbench) { return workbenchSourceLabel(workbench, libraries.value) }
const visibleItems = computed(() => {
  const query = keyword.value.trim().toLocaleLowerCase()
  const filtered = query ? items.value.filter(workbench => [workbench.name, workbench.description, sourceName(workbench)].some(value => value?.toLocaleLowerCase().includes(query))) : items.value
  return sortResourcesWithinGroups(filtered, selectedResourceSort.value, item => item.is_pinned ? 'pinned' : 'normal', ['pinned', 'normal'], { getName: item => item.name, getCreatedAt: item => item.created_at, getUpdatedAt: item => item.updated_at })
})
const existing = computed(() => items.value.filter(w => w.knowledge_base_id === kbId.value))
let generation = 0, creationKey = createRandomUUID()
function changed() { window.dispatchEvent(new CustomEvent('weknora:workspaces-changed')) }
async function load() {
  const own = ++generation, capturedIdentity = identity.value
  items.value = []; loading.value = true; error.value = ''
  try {
    const [res, datasets] = await Promise.all([listResearchWorkbenches(), creating.value ? listCentralDatasets() : Promise.resolve(null), resources.ensureKnowledgeBases()])
    if (own !== generation) return
    await organizations.fetchSharedKnowledgeBases({ force: sharedKnowledgeBaseIdentity.value !== capturedIdentity })
    if (own !== generation || capturedIdentity !== identity.value) return
    sharedKnowledgeBaseIdentity.value = capturedIdentity
    items.value = res.data
    if (creating.value) {
      if (!datasets?.success) throw new Error('无法读取中心数据来源，请重试。')
      central.value = datasets.data
      selectedSources.value = datasets.data.map(source => ({ source_type: 'central_dataset', source_id: source.id }))
      if (typeof route.query.kb_id === 'string' && libraries.value.some(k => k.id === route.query.kb_id)) {
        kbId.value = route.query.kb_id
        if (!central.value.some(source => source.knowledge_base_id === kbId.value)) selectedSources.value.push({ source_type: 'knowledge_base', source_id: kbId.value })
      }
    }
  } catch (e) { if (own === generation) error.value = message(e) }
  finally { if (own === generation) loading.value = false }
}
watch([identity, creating, () => route.query.kb_id], () => { if (sharedKnowledgeBaseIdentity.value !== identity.value) sharedKnowledgeBaseIdentity.value = ''; templateId.value = 'industrial'; kbId.value = ''; central.value = []; selectedSources.value = []; name.value = ''; description.value = ''; keyword.value = ''; busy.value = false; kbEditorOpen.value = false; creationKey = createRandomUUID(); void load() }, { immediate: true })
watch([selectedSources, name, description, templateId], () => { if (!busy.value) creationKey = createRandomUUID() }, { deep: true })
onBeforeUnmount(() => { generation++ })
watch(kbId, (value, previous) => {
  const oldName = libraries.value.find(k => k.id === previous)?.name
  if (!name.value || name.value === `${oldName || ''}工作台`) name.value = `${libraries.value.find(k => k.id === value)?.name || ''}工作台`
})
async function create() {
  if (busy.value) return
  error.value = validateSourceSelection(selectedSources.value, availableSources.value) || validateWorkspaceName(name.value)
  if (error.value) return
  const own = generation, ownerIdentity = identity.value
  busy.value = true
  try {
    const res = await createResearchWorkbench({ name: name.value.trim(), description: description.value.trim(), sources: selectedSources.value, template_id: templateId.value, template_version: 1, idempotency_key: creationKey })
    if (own !== generation || ownerIdentity !== identity.value) return
    try {
      registerResearchGuide(window.localStorage, { tenantId: String(auth.selectedTenantId ?? auth.tenant?.id ?? ''), userId: auth.user?.id || '', workbenchId: res.data.id, knowledgeBaseId: res.data.knowledge_base_id, scopeVersion: res.data.scope_version, sourcesRevision: res.data.sources_revision }, res.data.workspace_mode)
    } catch { /* Storage restrictions must not prevent opening a created workbench. */ }
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
    selectedSources.value = [...selectedSources.value, { source_type: 'knowledge_base', source_id: newId }]
    window.dispatchEvent(new CustomEvent('weknora:workspaces-changed'))
  } catch (e) { if (own === generation) error.value = message(e) }
}
</script>

<template>
  <div class="research-management" :class="creating ? 'workspace-management' : 'research-list-container'"><main :class="creating ? 'workspace-management__content' : 'research-list-content'">
    <header class="header" :class="{ 'workspace-management__header': creating }"><div class="header-title"><div class="title-row"><h2><t-icon v-if="!creating" name="chart-bar" size="24px" />{{ creating ? '新建研究工作台' : '研究工作台' }}</h2><div class="header-actions"><t-button v-if="creating" size="small" variant="outline" @click="router.push('/platform/research-workbenches')">全部工作台</t-button><template v-else><ResourceListToolbar class="research-list-search" model-value="all" v-model:query="keyword" hide-scopes /><ResourceSortControl v-model="selectedResourceSort" /><t-button size="small" theme="default" variant="outline" class="header-action-btn resource-action" @click="router.push('/platform/research-workbenches/new')"><template #icon><t-icon name="chart-add" size="16px" /></template>新建工作台</t-button></template></div></div><p class="header-subtitle">{{ creating ? '选择数据来源和起步模板，稍后在工作台内准备资料。' : '基于知识库组织资料与研究，点击工作台即可进入。' }}</p></div></header>
    <t-alert v-if="error" theme="error" :message="error" class="workspace-management__error" />
    <div v-if="loading" class="workspace-management__empty"><t-loading text="正在加载工作台" /></div>
    <form v-else-if="creating" class="research-create" @submit.prevent="create">
      <div v-if="!items.length" class="research-create__first"><t-icon name="info-circle" />选择起步模板和数据来源，创建后按页面引导继续。也可以先创建空白工作台。</div>
      <section class="workspace-management__panel research-create__panel">
        <div class="research-create__label"><span>数据来源<small>（选填）</small></span><t-button v-if="auth.hasRole('contributor')" size="small" variant="text" :disabled="busy" @click="kbEditorOpen = true">新建知识库</t-button></div>
        <WorkbenchSourcePicker v-model="selectedSources" :options="availableSources" :disabled="busy" />
        <div class="research-create__field"><label for="workbench-name">工作台名称</label><input id="workbench-name" v-model="name" maxlength="80" required :disabled="busy" placeholder="填写名称" /></div>
        <div v-if="existing.length" class="research-create__existing"><span>此库已有 {{ existing.length }} 个工作台</span><t-button v-for="workbench in existing" :key="workbench.id" size="small" variant="text" @click="router.push(`/platform/research-workbenches/${workbench.id}`)">{{ workbench.name }}</t-button></div>
        <div class="research-create__label"><span>起步模板</span><small>只建立功能与证据要求，不预置指标、观点或结果</small></div>
        <div role="radiogroup" aria-label="研究工作台模板"><label v-for="template in workbenchTemplates" :key="template.id" class="research-template" :class="{ 'is-selected': templateId === template.id }"><input v-model="templateId" type="radio" name="workbench-template" :value="template.id" :disabled="busy" /><span><strong>{{ template.name }}</strong><small>{{ template.description }}</small></span></label></div>
        <div class="research-create__modules" aria-label="所选模板功能"><span v-for="module in templateById(templateId).modules" :key="module">{{ workbenchModuleNames[module] || module }}</span></div>
        <label class="research-create__field"><span>说明<small>（选填）</small></span><input v-model="description" maxlength="1000" :disabled="busy" placeholder="简短说明研究范围" /></label>
        <p class="research-create__note">最多关联 8 个中心库或知识库来源。研究成果独立保存，资料和数值仍保留在来源中。</p>
        <div class="workspace-management__actions research-create__footer"><t-button size="small" variant="outline" :disabled="busy" @click="router.push(typeof route.query.kb_id === 'string' ? `/platform/knowledge-bases/${route.query.kb_id}` : '/platform/research-workbenches')">取消</t-button><t-button size="small" type="submit" :loading="busy" :disabled="loading">创建工作台</t-button></div>
      </section>
    </form>
    <div v-else-if="!creating" class="research-list-main">
      <div v-if="visibleItems.length" class="workbench-card-wrap"><article v-for="workbench in visibleItems" :key="workbench.id" class="workbench-card" role="link" tabindex="0" :aria-label="`进入${workbench.name}`" @click="router.push(`/platform/research-workbenches/${encodeURIComponent(workbench.id)}`)" @keydown.enter.self.prevent="router.push(`/platform/research-workbenches/${encodeURIComponent(workbench.id)}`)" @keydown.space.self.prevent="router.push(`/platform/research-workbenches/${encodeURIComponent(workbench.id)}`)">
        <div class="card-header"><span class="workbench-avatar"><t-icon name="chart-bar" size="18px" /></span><span class="card-title" :title="workbench.name">{{ workbench.name }}</span><t-icon v-if="workbench.is_pinned" name="pin-filled" size="14px" title="已置顶" /><WorkbenchActions :workbench="workbench" @sources-changed="load" @renamed="updated => { items = items.map(item => item.id === updated.id ? updated : item) }" @deleted="id => { items = items.filter(item => item.id !== id) }" /><t-icon class="workbench-open" name="chevron-right" size="16px" /></div>
        <div class="card-content"><div class="card-description" :class="{ 'is-empty': !workbench.description }">{{ workbench.description || '暂无说明' }}</div></div>
        <div class="card-bottom"><button type="button" class="workbench-source" :disabled="workbench.can_read_source === false || !workbench.knowledge_base_id" :title="sourceName(workbench)" @click.stop="workbench.can_read_source !== false && workbench.knowledge_base_id && router.push(`/platform/knowledge-bases/${encodeURIComponent(workbench.knowledge_base_id)}`)" @keydown.stop><t-icon name="folder" size="14px" /><span>{{ sourceName(workbench) }}</span></button><span class="workbench-template">{{ workbench.workspace_mode === 'isolated' ? templateById(workbench.template_id || 'blank').name : '研究工作台' }}</span></div>
      </article></div>
      <EmptyState v-else-if="!error && keyword.trim()" icon="search" title="没有匹配的工作台"><t-button size="small" variant="outline" @click="keyword = ''">清除搜索</t-button></EmptyState>
      <EmptyState v-else-if="!error" icon="chart-bar" title="还没有研究工作台" description="选择知识库和起步模板，创建后按引导准备资料。"><t-button size="small" theme="default" variant="outline" class="resource-action" @click="router.push('/platform/research-workbenches/new')"><template #icon><t-icon name="chart-add" size="16px" /></template>新建工作台</t-button></EmptyState>
    </div>
    <t-button v-if="error" size="small" variant="outline" @click="load">重新加载</t-button>
  </main><KnowledgeBaseEditorModal v-model:visible="kbEditorOpen" mode="create" @success="knowledgeCreated" /></div>
</template>

<style scoped>
.research-create,.research-create *{box-sizing:border-box}.research-management h2{font-size:var(--app-page-title-size);margin-bottom:4px}.research-management .workspace-management__header{margin-bottom:16px}.research-management .workspace-management__header p{font-size:var(--app-text-sm)}.research-create{max-width:560px;margin:0 auto}.research-create__panel{padding:18px}.research-create__first{display:flex;align-items:flex-start;gap:7px;font-size:var(--app-text-sm);line-height:1.7;color:var(--app-text-secondary);padding:9px 0 12px}.research-create__first .t-icon{flex:none;margin-top:3px;color:var(--td-brand-color)}.research-create__field{display:flex;align-items:center;gap:10px;margin-bottom:12px;font-size:var(--app-text-sm)}.research-create__field>label,.research-create__field>span{width:76px;flex:none}.research-create__field small{font-size:var(--app-text-xs);color:var(--app-text-secondary)}.research-create__field input,.research-create__field select{height:32px;min-width:0;flex:1;padding:0 9px;color:var(--app-text-primary);background:var(--app-card-bg);border:1px solid var(--app-border-color);border-radius:var(--app-radius-sm);font:inherit}.research-create__field input:focus-visible,.research-create__field select:focus-visible{outline:2px solid var(--td-brand-color);outline-offset:2px}.research-create__control{display:flex;flex:1;min-width:0;gap:5px}.research-create__existing{display:flex;align-items:center;gap:4px;flex-wrap:wrap;font-size:var(--app-text-xs);color:var(--app-text-secondary);margin-bottom:12px}.research-create__existing .t-button{font-size:var(--app-text-xs);height:25px}.research-create__label{display:flex;justify-content:space-between;align-items:center;gap:8px;font-size:var(--app-text-sm);margin:16px 0 8px}.research-create__label small{font-size:var(--app-text-2xs);color:var(--app-text-secondary)}.research-template{display:flex;align-items:center;gap:10px;min-height:60px;padding:9px 11px;margin-bottom:7px;border:1px solid var(--app-border-color);border-radius:var(--app-radius-sm);cursor:pointer;transition:background var(--app-motion-fast),border-color var(--app-motion-fast)}.research-template:hover{background:var(--app-bg)}.research-template.is-selected{background:var(--td-brand-color-light);border-color:var(--td-brand-color)}.research-template input{accent-color:var(--td-brand-color);width:14px;height:14px;margin:0;flex:none}.research-template strong{display:block;font-size:var(--app-text-md);font-weight:500}.research-template small{display:block;font-size:var(--app-text-xs);color:var(--app-text-secondary);margin-top:2px}.research-create__modules{display:flex;gap:5px;flex-wrap:wrap;margin:10px 0 14px}.research-create__modules span{border:1px solid var(--app-border-color);border-radius:var(--app-radius-xs);padding:1px 6px;font-size:var(--app-text-xs);color:var(--app-text-secondary)}.research-create .research-create__note{font-size:var(--app-text-xs);line-height:1.65;margin:0;color:var(--app-text-secondary)}.research-create__footer{justify-content:flex-end;margin-top:14px;padding-top:12px;border-top:1px solid var(--app-border-color)}
@media(max-width:600px){.workspace-management.research-management{padding:16px 12px}.research-create__panel{padding:13px}.research-create__label{flex-wrap:wrap;gap:3px}.research-create__label small{font-size:var(--app-text-2xs)}.research-create__field>label,.research-create__field>span{width:65px}.research-template{padding:8px}.research-template small{font-size:var(--app-text-2xs)}}
@media(prefers-reduced-motion:reduce){.research-template{transition:none}}
</style>

<style scoped lang="less">
@import (reference) '@/components/css/resource-card.less';

.research-list-container {
  flex: 1;
  min-width: 0;
  min-height: 0;
  height: 100%;
  display: flex;
  color: var(--td-text-color-primary);
  .resource-list-header();

  .header { display: flex; align-items: center; }
  .header-title { display: flex; flex-direction: column; }
  .header .title-row { display: grid; grid-template-columns: minmax(0, 1fr) auto; align-items: center; }
  .header h2 {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0 auto 0 0;
    font-size: var(--app-text-4xl);
    font-weight: 600;
    line-height: 32px;
  }
  .header-actions { display: flex; align-items: center; gap: 8px; min-width: 0; flex-wrap: nowrap; }
  .research-list-search { padding: 0; border: 0; width: 220px; flex: 0 1 220px; min-width: 140px; }
  .research-list-search :deep(.search-input) { width: 100%; }
  .header-subtitle { margin: 0; line-height: 20px; }
  .workspace-management__empty { border: 0; background: transparent; }
}
.research-list-content { .resource-list-content(); }
.research-list-main { .resource-list-main(); }
.workbench-card-wrap { .resource-card-grid(); }
.workbench-card {
  .resource-card();
  .card-header { padding-right: 0; }
}
.workbench-avatar {
  display: flex;
  align-items: center;
  justify-content: center;
  flex: none;
  width: 28px;
  height: 28px;
  border-radius: var(--app-radius-sm);
  color: var(--td-brand-color);
  background: var(--td-brand-color-light);
}
.workbench-open { flex: none; color: var(--td-text-color-placeholder); }
.workbench-source {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  min-width: 0;
  max-width: 65%;
  padding: 2px 0;
  border: 0;
  border-radius: var(--app-radius-xs);
  color: var(--td-text-color-secondary);
  background: transparent;
  font: inherit;
  font-size: var(--app-text-sm);
  cursor: pointer;
  .t-icon { flex: none; color: var(--td-brand-color); }
  span { overflow: hidden; white-space: nowrap; text-overflow: ellipsis; }
  &:hover { color: var(--td-brand-color); }
  &:focus-visible { outline: 2px solid var(--td-brand-color); outline-offset: 2px; }
}
.workbench-template { color: var(--td-text-color-placeholder); font-size: var(--app-text-xs); }
.workspace-management.research-management {
  .header-title { width: 100%; }
  .title-row { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
}
@media (max-width: 1000px) {
  .research-list-container .header .title-row { grid-template-columns: minmax(0, 1fr); }
  .research-list-container .header-actions { justify-content: flex-start; flex-wrap: wrap; }
}
@media (max-width: 600px) {
  .research-list-container .header-actions { width: 100%; gap: 8px; }
  .research-list-container .research-list-search { flex: 1 1 100%; }
}
</style>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { useMenuStore } from '@/stores/menu'
import { createSessions, getSessionsList } from '@/api/chat'
import { assignSessionToProject, createAssistantProject, getAssistantProject, listAssistantProjects, listProjectSessions, type AssistantProject } from '@/api/workspaces'
import { validateWorkspaceName, workspaceErrorMessage as message } from './workspaceForm'
import ProjectActions from './ProjectActions.vue'
import ResourceListToolbar from '@/components/ResourceListToolbar.vue'
import ResourceSortControl from '@/components/ResourceSortControl.vue'
import EmptyState from '@/components/EmptyState.vue'
import { DEFAULT_RESOURCE_SORT, sortResourceItems, type ResourceSortValue } from '@/utils/resourceSorting'
import './workspaces.css'

interface ProjectSession { id: string; title?: string; created_at?: string; updated_at?: string }
const auth = useAuthStore(), route = useRoute(), router = useRouter(), menu = useMenuStore()
const projectId = computed(() => String(route.params.projectId || ''))
const identity = computed(() => JSON.stringify([auth.selectedTenantId ?? auth.tenant?.id, auth.user?.id, auth.token]))
const canRead = computed(() => Boolean((auth.selectedTenantId ?? auth.tenant?.id) && auth.user?.id && auth.token) && auth.hasRole('viewer'))
const loadedIdentity = ref('')
const canManage = computed(() => canRead.value && loadedIdentity.value === identity.value)
const keyword = ref(''), selectedResourceSort = ref<ResourceSortValue>(DEFAULT_RESOURCE_SORT)
const visibleProjects = computed(() => {
  const query = keyword.value.trim().toLocaleLowerCase()
  const filtered = query ? projects.value.filter(project => [project.name, project.description].some(value => value?.toLocaleLowerCase().includes(query))) : projects.value
  return sortResourceItems(filtered, selectedResourceSort.value, { getName: item => item.name, getCreatedAt: item => item.created_at, getUpdatedAt: item => item.updated_at })
})
const projects = ref<AssistantProject[]>([]), current = ref<AssistantProject | null>(null), sessions = ref<ProjectSession[]>([])
const loading = ref(false), busy = ref(false), error = ref(''), createOpen = ref(false), name = ref(''), description = ref('')
const archiveOpen = ref(false), recentSessions = ref<ProjectSession[]>([]), selectedSession = ref(''), page = ref(1), total = ref(0)
const pendingSession = ref('')
let generation = 0
function changed() { window.dispatchEvent(new CustomEvent('weknora:workspaces-changed')) }
async function load(preserve = false) {
  const own = ++generation, id = projectId.value
  loadedIdentity.value = ''; error.value = ''
  if (!preserve) { projects.value = []; current.value = null; sessions.value = []; total.value = 0; loading.value = true }
  if (!canRead.value) { loading.value = false; return }
  try {
    if (id) {
      const [detail, rows] = await Promise.all([getAssistantProject(id), listProjectSessions(id, page.value, 20)])
      if (own !== generation) return
      if (detail?.success !== true || detail.data?.id !== id || rows?.success !== true || !Array.isArray(rows.data)) throw new Error('未能读取项目信息，请重试。')
      current.value = detail.data; sessions.value = rows.data; total.value = rows.total; loadedIdentity.value = identity.value
    } else {
      const res = await listAssistantProjects()
      if (own !== generation) return
      if (res?.success !== true || !Array.isArray(res.data)) throw new Error('未能读取项目列表，请重试。')
      projects.value = res.data; loadedIdentity.value = identity.value
    }
  } catch (e) { if (own === generation) error.value = message(e) }
  finally { if (own === generation) loading.value = false }
}
watch([identity, projectId, canRead], () => { keyword.value = ''; page.value = 1; createOpen.value = false; archiveOpen.value = false; name.value = ''; description.value = ''; recentSessions.value = []; selectedSession.value = ''; pendingSession.value = ''; busy.value = false; void load() }, { immediate: true, flush: 'sync' })
onBeforeUnmount(() => { generation++ })
function renamed(id: string, nextName: string) {
  if (!canManage.value) return
  projects.value = projects.value.map(project => project.id === id ? { ...project, name: nextName } : project)
  if (current.value?.id === id) current.value = { ...current.value, name: nextName }
  // Update returns no conversation count; reload the owner-scoped source of truth.
  void load(true)
}
function deleted(id: string) {
  if (!canManage.value) return
  projects.value = projects.value.filter(project => project.id !== id)
  if (current.value?.id === id) { current.value = null; sessions.value = []; void router.push('/platform/projects') }
  else void load(true)
}
async function create() {
  if (busy.value) return
  error.value = validateWorkspaceName(name.value)
  if (error.value) return
  const own = generation
  busy.value = true
  try {
    const res = await createAssistantProject({ name: name.value.trim(), description: description.value.trim() })
    if (own !== generation) return
    changed(); createOpen.value = false; name.value = ''; description.value = ''
    await router.push(`/platform/projects/${res.data.id}`)
  } catch (e) { if (own === generation) error.value = message(e) }
  finally { if (own === generation) busy.value = false }
}
async function newConversation() {
  if (busy.value || !current.value) return
  const own = generation, id = current.value.id
  busy.value = true; error.value = ''
  try {
    if (!pendingSession.value) {
      const session = await createSessions({ title: current.value.name })
      if (own !== generation) return
      pendingSession.value = session.data.id
    }
    const sessionId = pendingSession.value
    await assignSessionToProject(id, sessionId)
    if (own !== generation) return
    pendingSession.value = ''; changed(); menu.changeIsFirstSession(false)
    await router.push(`/platform/chat/${sessionId}`)
  } catch (e) { if (own === generation) error.value = pendingSession.value ? `对话已创建，归入项目失败：${message(e)} 再次点击将重试归档。` : message(e) }
  finally { if (own === generation) busy.value = false }
}
async function showArchive() {
  if (busy.value) return
  const own = generation; busy.value = true; error.value = ''; selectedSession.value = ''
  try {
    const res = await getSessionsList(1, 50, 'web')
    if (own !== generation) return
    recentSessions.value = res.data || []; archiveOpen.value = true
  } catch (e) { if (own === generation) error.value = message(e) }
  finally { if (own === generation) busy.value = false }
}
async function archive() {
  if (busy.value || !selectedSession.value || !projectId.value) return
  const own = generation; busy.value = true; error.value = ''
  try {
    await assignSessionToProject(projectId.value, selectedSession.value)
    if (own !== generation) return
    archiveOpen.value = false; changed(); busy.value = false; await load()
  } catch (e) { if (own === generation) error.value = message(e) }
  finally { if (own === generation) busy.value = false }
}
</script>

<template>
  <div :class="projectId ? 'workspace-management' : 'project-list-container'"><main :class="projectId ? 'workspace-management__content' : 'project-list-content'">
    <header v-if="projectId" class="workspace-management__header"><div><h2>{{ current?.name || '项目' }}</h2><p>{{ current?.description || '用项目组织相关对话，历史对话仍保留原会话。' }}</p></div><div class="workspace-management__actions">
      <t-button variant="outline" @click="router.push('/platform/projects')">全部项目</t-button><t-button variant="outline" :loading="busy" @click="showArchive">归档已有对话</t-button><t-button :loading="busy" :disabled="!current" @click="newConversation">新建项目对话</t-button><ProjectActions v-if="current" :project="current" :can-manage="canManage" @renamed="renamed" @deleted="deleted" />
    </div></header>
    <header v-else class="header"><div class="header-title"><div class="title-row"><h2><t-icon name="folder" size="24px" />项目</h2><div class="header-actions"><ResourceListToolbar class="project-list-search" model-value="all" v-model:query="keyword" hide-scopes /><ResourceSortControl v-model="selectedResourceSort" /><t-button size="small" theme="default" variant="text" class="header-action-btn" @click="createOpen = true; error = ''"><template #icon><t-icon name="folder-add" size="16px" /></template>新建项目</t-button></div></div><p class="header-subtitle">用项目组织相关对话，历史对话仍保留原会话。</p></div></header>
    <t-alert v-if="error" theme="error" :message="error" class="workspace-management__error" />
    <div v-if="loading" class="workspace-management__empty"><t-loading text="正在加载项目" /></div>
    <template v-else-if="projectId && current">
      <section class="workspace-management__panel"><h3>项目对话</h3><p>归档仅调整项目关联，不复制消息，不改变历史回答的来源。</p>
        <div v-for="session in sessions" :key="session.id" class="workspace-management__row"><div><strong>{{ session.title || '未命名对话' }}</strong><small>{{ session.updated_at || session.created_at }}</small></div><t-button variant="text" @click="router.push(`/platform/chat/${session.id}`)">打开对话</t-button></div>
        <p v-if="!sessions.length">还没有对话，可以新建或归档已有对话。</p>
        <t-pagination v-if="total > 20" :current="page" :page-size="20" :total="total" :show-page-size="false" @current-change="(value: number) => { page = value; load() }" />
      </section>
    </template>
    <div v-else-if="!projectId" class="project-list-main">
      <div v-if="visibleProjects.length" class="project-card-wrap"><article v-for="project in visibleProjects" :key="project.id" class="project-card" role="link" tabindex="0" :aria-label="`进入${project.name}`" @click="router.push(`/platform/projects/${encodeURIComponent(project.id)}`)" @keydown.enter.self.prevent="router.push(`/platform/projects/${encodeURIComponent(project.id)}`)" @keydown.space.self.prevent="router.push(`/platform/projects/${encodeURIComponent(project.id)}`)">
        <div class="card-header"><span class="project-avatar"><t-icon name="folder" size="18px" /></span><span class="card-title" :title="project.name">{{ project.name }}</span><ProjectActions :project="project" :can-manage="canManage" @renamed="renamed" @deleted="deleted" /><t-icon class="project-open" name="chevron-right" size="16px" /></div>
        <div class="card-content"><div class="card-description" :class="{ 'is-empty': !project.description }">{{ project.description || '暂无说明' }}</div></div>
        <div class="card-bottom"><span class="project-session-count"><t-icon name="chat" size="14px" /><span>{{ project.session_count || 0 }} 条对话</span></span></div>
      </article></div>
      <EmptyState v-else-if="!error && keyword.trim()" icon="search" title="没有匹配的项目"><t-button size="small" variant="outline" @click="keyword = ''">清除搜索</t-button></EmptyState>
      <EmptyState v-else-if="!error" icon="folder" title="还没有项目" description="新建项目，将相关对话归档在一起。"><t-button size="small" @click="createOpen = true">新建项目</t-button></EmptyState>
    </div>
    <t-button v-if="error" variant="outline" @click="load()">重新加载</t-button>
  </main>
  <t-dialog v-model:visible="createOpen" header="新建项目" :confirm-btn="{ content: '创建项目', loading: busy }" :close-on-overlay-click="!busy" @confirm="create"><form @submit.prevent="create"><label class="workspace-management__field"><span>项目名称</span><input v-model="name" maxlength="80" required /></label><label class="workspace-management__field"><span>说明</span><textarea v-model="description" maxlength="1000" /></label><t-alert v-if="error" theme="error" :message="error" /></form></t-dialog>
  <t-dialog v-model:visible="archiveOpen" header="归档已有对话" :confirm-btn="{ content: '保存归档', loading: busy, disabled: !selectedSession }" @confirm="archive"><p>从最近 50 条网页会话中选择。会话仍出现在历史列表中。</p><label class="workspace-management__field"><span>对话</span><select v-model="selectedSession"><option value="">请选择</option><option v-for="session in recentSessions" :key="session.id" :value="session.id">{{ session.title || '未命名对话' }}</option></select></label><t-alert v-if="error" theme="error" :message="error" /></t-dialog>
  </div>
</template>


<style scoped lang="less">
@import (reference) '@/components/css/resource-card.less';
.project-list-container {
  flex: 1; min-width: 0; min-height: 0; height: 100%; display: flex;
  color: var(--td-text-color-primary);
  .resource-list-header();
  .header { display: flex; align-items: center; }
  .header-title { display: flex; flex-direction: column; }
  .header .title-row { display: grid; grid-template-columns: minmax(0, 1fr) auto; align-items: center; }
  .header h2 { display: flex; align-items: center; gap: 8px; margin: 0 auto 0 0; font-size: var(--app-text-4xl); font-weight: 600; line-height: 32px; }
  .header-actions { display: flex; align-items: center; gap: 8px; min-width: 0; flex-wrap: nowrap; }
  .project-list-search { padding: 0; border: 0; width: 220px; flex: 0 1 220px; min-width: 140px; }
  .project-list-search :deep(.search-input) { width: 100%; }
  .header-subtitle { margin: 0; line-height: 20px; }
  .workspace-management__empty { border: 0; background: transparent; }
}
.project-list-content { .resource-list-content(); }
.project-list-main { .resource-list-main(); }
.project-card-wrap { .resource-card-grid(); }
.project-card { .resource-card(); .card-header { padding-right: 0; } }
.project-avatar { display: flex; align-items: center; justify-content: center; flex: none; width: 28px; height: 28px; border-radius: var(--app-radius-sm); color: var(--td-brand-color); background: var(--td-brand-color-light); }
.project-open { flex: none; color: var(--td-text-color-placeholder); }
.project-session-count { .resource-feature-badge(); }
@media (max-width: 1000px) {
  .project-list-container .header .title-row { grid-template-columns: minmax(0, 1fr); }
  .project-list-container .header-actions { justify-content: flex-start; flex-wrap: wrap; }
}
@media (max-width: 600px) {
  .project-list-container .header-actions { width: 100%; gap: 8px; }
  .project-list-container .project-list-search { flex: 1 1 100%; }
}
</style>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { useMenuStore } from '@/stores/menu'
import { createSessions, getSessionsList } from '@/api/chat'
import { assignSessionToProject, createAssistantProject, getAssistantProject, listAssistantProjects, listProjectSessions, type AssistantProject } from '@/api/workspaces'
import { validateWorkspaceName, workspaceErrorMessage as message, workspaceIdentity } from './workspaceForm'
import './workspaces.css'

interface ProjectSession { id: string; title?: string; created_at?: string; updated_at?: string }
const auth = useAuthStore(), route = useRoute(), router = useRouter(), menu = useMenuStore()
const projectId = computed(() => String(route.params.projectId || ''))
const identity = computed(() => workspaceIdentity(auth.selectedTenantId ?? auth.tenant?.id, auth.user?.id))
const projects = ref<AssistantProject[]>([]), current = ref<AssistantProject | null>(null), sessions = ref<ProjectSession[]>([])
const loading = ref(false), busy = ref(false), error = ref(''), createOpen = ref(false), name = ref(''), description = ref('')
const archiveOpen = ref(false), recentSessions = ref<ProjectSession[]>([]), selectedSession = ref(''), page = ref(1), total = ref(0)
const pendingSession = ref('')
let generation = 0
function changed() { window.dispatchEvent(new CustomEvent('weknora:workspaces-changed')) }
async function load() {
  const own = ++generation, id = projectId.value
  projects.value = []; current.value = null; sessions.value = []; total.value = 0; error.value = ''; loading.value = true
  try {
    if (id) {
      const [detail, rows] = await Promise.all([getAssistantProject(id), listProjectSessions(id, page.value, 20)])
      if (own !== generation) return
      current.value = detail.data; sessions.value = rows.data; total.value = rows.total
    } else {
      const res = await listAssistantProjects()
      if (own === generation) projects.value = res.data
    }
  } catch (e) { if (own === generation) error.value = message(e) }
  finally { if (own === generation) loading.value = false }
}
watch([identity, projectId], () => { page.value = 1; createOpen.value = false; archiveOpen.value = false; name.value = ''; description.value = ''; recentSessions.value = []; selectedSession.value = ''; pendingSession.value = ''; busy.value = false; void load() }, { immediate: true })
onBeforeUnmount(() => { generation++ })
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
  <div class="workspace-management"><main class="workspace-management__content">
    <header class="workspace-management__header"><div><h2>{{ current?.name || '项目' }}</h2><p>{{ current?.description || '用项目组织相关对话，历史对话仍保留原会话。' }}</p></div><div class="workspace-management__actions">
      <template v-if="projectId"><t-button variant="outline" @click="router.push('/platform/projects')">全部项目</t-button><t-button variant="outline" :loading="busy" @click="showArchive">归档已有对话</t-button><t-button :loading="busy" :disabled="!current" @click="newConversation">新建项目对话</t-button></template>
      <t-button v-else @click="createOpen = true; error = ''">新建项目</t-button>
    </div></header>
    <t-alert v-if="error" theme="error" :message="error" class="workspace-management__error" />
    <div v-if="loading" class="workspace-management__empty"><t-loading text="正在加载项目" /></div>
    <template v-else-if="projectId && current">
      <section class="workspace-management__panel"><h3>项目对话</h3><p>归档仅调整项目关联，不复制消息，不改变历史回答的来源。</p>
        <div v-for="session in sessions" :key="session.id" class="workspace-management__row"><div><strong>{{ session.title || '未命名对话' }}</strong><small>{{ session.updated_at || session.created_at }}</small></div><t-button variant="text" @click="router.push(`/platform/chat/${session.id}`)">打开对话</t-button></div>
        <p v-if="!sessions.length">还没有对话，可以新建或归档已有对话。</p>
        <t-pagination v-if="total > 20" :current="page" :page-size="20" :total="total" :show-page-size="false" @current-change="(value: number) => { page = value; load() }" />
      </section>
    </template>
    <div v-else-if="!projectId && projects.length" class="workspace-management__grid"><article v-for="project in projects" :key="project.id" class="workspace-management__card"><h3>{{ project.name }}</h3><p>{{ project.description || '相关会话的集合' }}</p><p>{{ project.session_count || 0 }} 条对话</p><div class="workspace-management__actions"><t-button variant="outline" @click="router.push(`/platform/projects/${project.id}`)">预览项目</t-button></div></article></div>
    <div v-else-if="!error" class="workspace-management__empty"><p>还没有项目。</p><t-button @click="createOpen = true">新建项目</t-button></div>
    <t-button v-if="error" variant="outline" @click="load">重新加载</t-button>
  </main>
  <t-dialog v-model:visible="createOpen" header="新建项目" :confirm-btn="{ content: '创建项目', loading: busy }" :close-on-overlay-click="!busy" @confirm="create"><form @submit.prevent="create"><label class="workspace-management__field"><span>项目名称</span><input v-model="name" maxlength="80" required /></label><label class="workspace-management__field"><span>说明</span><textarea v-model="description" maxlength="1000" /></label><t-alert v-if="error" theme="error" :message="error" /></form></t-dialog>
  <t-dialog v-model:visible="archiveOpen" header="归档已有对话" :confirm-btn="{ content: '保存归档', loading: busy, disabled: !selectedSession }" @confirm="archive"><p>从最近 50 条网页会话中选择。会话仍出现在历史列表中。</p><label class="workspace-management__field"><span>对话</span><select v-model="selectedSession"><option value="">请选择</option><option v-for="session in recentSessions" :key="session.id" :value="session.id">{{ session.title || '未命名对话' }}</option></select></label><t-alert v-if="error" theme="error" :message="error" /></t-dialog>
  </div>
</template>

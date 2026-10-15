<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { MessagePlugin } from 'tdesign-vue-next'
import { useAuthStore } from '@/stores/auth'
import { useUIStore } from '@/stores/ui'
import { useOrganizationStore } from '@/stores/organization'
import { getKnowledgeBaseById, listKnowledgeFiles, listKnowledgeFolders, createKnowledgeFolder, deleteKnowledgeFolder, renameKnowledgeFolder, type KnowledgeFolderTree } from '@/api/knowledge-base'
import { permissionCanEditKB } from '@/utils/kbPermission'
import BrandWordmark from '@/components/BrandWordmark.vue'
import UserMenu from '@/components/UserMenu.vue'
import WorkspaceNavigationNode from '@/components/WorkspaceNavigationNode.vue'
import PanelResizeHandle from '@/components/PanelResizeHandle.vue'
import { SIDEBAR_COLLAPSED_WIDTH, SIDEBAR_MAX_WIDTH } from '@/utils/sidebarWidth'
import KbFolderTree from '@/views/knowledge/components/KbFolderTree.vue'
import { canMoveFolderTo, folderPathExists } from '@/views/knowledge/folderTree'
import { KNOWLEDGE_DIRECTORY_EVENT, KNOWLEDGE_DIRECTORY_PAGE_SIZE, knowledgeDirectorySelection, newKnowledgeFolderPath, knowledgeDirectoryFileLocation, validateKnowledgeDirectoryPage, type KnowledgeDirectoryFile, type KnowledgeDirectoryFilePage } from './knowledgeDirectory'
import { workspaceErrorMessage, workspaceIdentity } from './workspaceForm'

const route = useRoute(), router = useRouter(), auth = useAuthStore(), ui = useUIStore(), org = useOrganizationStore()
const kbId = computed(() => String(route.params.kbId || ''))
const identity = computed(() => `${workspaceIdentity(auth.selectedTenantId ?? auth.tenant?.id, auth.user?.id)}:${kbId.value}`)
const library = ref<any>(null), tree = ref<KnowledgeFolderTree | null>(null), loading = ref(false), error = ref('')
const selected = computed(() => knowledgeDirectorySelection(route.query))
const directoryOpen = ref(true), structuredOpen = ref(true)
const isDirectory = computed(() => route.name === 'knowledgeBaseDetail')
const canEdit = computed(() => {
  if (!library.value || library.value.type === 'faq') return false
  const grant = org.getKBPermission(kbId.value) || library.value.my_permission
  if (grant) return permissionCanEditKB(grant)
  const ownTenant = String(library.value.tenant_id) === String(auth.selectedTenantId ?? auth.tenant?.id)
  return ownTenant && (library.value.creator_id === auth.user?.id || auth.hasRole('admin'))
})
const folderOpen = ref(false), folderName = ref(''), folderError = ref(''), folderBusy = ref(false), folderParent = ref('')
const deletePath = ref<string | null>(null)
const filePages = ref<Record<string, KnowledgeDirectoryFilePage>>({})
let filesGeneration = 0, filesActive = 0
let filesController = new AbortController(), loadedIdentity = ''
const filesQueue: Array<{ path: string; generation: number }> = []
let generation = 0, resizeStart = 0
async function load() {
  const own = ++generation
  const actor = identity.value, id = kbId.value
  filesController.abort(); filesController = new AbortController(); loadedIdentity = ''
  filesGeneration++; filesActive = 0; filesQueue.length = 0; filePages.value = {}
  tree.value = null; library.value = null; error.value = ''; loading.value = true
  try {
    const [detail, folders] = await Promise.all([getKnowledgeBaseById(id), listKnowledgeFolders(id)])
    if (own !== generation || actor !== identity.value) return
    library.value = detail.data || detail; tree.value = folders.data; loadedIdentity = actor
  } catch (e) { if (own === generation && actor === identity.value) error.value = workspaceErrorMessage(e) }
  finally { if (own === generation && actor === identity.value) loading.value = false }
}
function loadFiles(path: string, more = false) {
  const previous = Object.hasOwn(filePages.value, path) ? filePages.value[path] : undefined
  if (!tree.value || !library.value || loadedIdentity !== identity.value || !folderPathExists(tree.value.folders, path) || previous?.loading) return
  if (!more && previous?.page && !previous.error) return
  filePages.value = { ...filePages.value, [path]: { files: previous?.files || [], page: previous?.page || 0,
    total: previous?.total || 0, loading: true, error: '' } }
  filesQueue.push({ path, generation: filesGeneration })
  drainFiles()
}
function drainFiles() {
  while (filesActive < 2 && filesQueue.length) {
    const work = filesQueue.shift()!
    if (work.generation !== filesGeneration) continue
    filesActive++; void requestFiles(work)
  }
}
async function requestFiles(work: { path: string; generation: number }) {
  const own = identity.value, id = kbId.value, previous = filePages.value[work.path]!
  const page = previous.page + 1
  try {
    const response = await listKnowledgeFiles(id, { page, page_size: KNOWLEDGE_DIRECTORY_PAGE_SIZE,
      folder_path: work.path, sort_by: 'file_name', sort_order: 'asc' }, { signal: filesController.signal })
    if (own !== identity.value || work.generation !== filesGeneration) return
    const data = validateKnowledgeDirectoryPage(response, id, work.path)
    const files = new Map(previous.files.map(file => [file.id, file]))
    data.files.forEach(file => files.set(file.id, file))
    filePages.value = { ...filePages.value, [work.path]: { files: [...files.values()], page,
      total: data.total, loading: false, error: '' } }
  } catch (e) {
    if (own === identity.value && work.generation === filesGeneration) {
      filePages.value = { ...filePages.value, [work.path]: { ...previous, loading: false, error: workspaceErrorMessage(e) } }
    }
  } finally {
    if (work.generation === filesGeneration) { filesActive--; drainFiles() }
  }
}
function selectFile(file: KnowledgeDirectoryFile) {
  if (loadedIdentity !== identity.value || !filePages.value[file.folder_path]?.files.some(current => current.id === file.id)) return
  const target = knowledgeDirectoryFileLocation(kbId.value, file)
  if (target) void router.push(target)
}
watch(identity, () => { folderOpen.value = false; deletePath.value = null; folderBusy.value = false; void load() }, { immediate: true })
function selectFolder(path: string) {
  router.push({ path: `/platform/knowledge-bases/${kbId.value}`, query: path ? { folder: path } : {} })
}
function allFiles() { router.push({ path: `/platform/knowledge-bases/${kbId.value}`, query: { directory: 'all' } }) }
function emitDirectoryChange(id: string) { window.dispatchEvent(new CustomEvent(KNOWLEDGE_DIRECTORY_EVENT, { detail: { kbId: id } })) }
function refresh(event: Event) {
  if ((event as CustomEvent).detail?.kbId === kbId.value) void load()
}
function allFolderPaths() {
  const paths: string[] = []
  const walk = (nodes: KnowledgeFolderTree['folders']) => nodes.forEach(f => { paths.push(f.path); walk(f.children || []) })
  walk(tree.value?.folders || [])
  return paths
}
function openNewFolder() {
  if (!canEdit.value) return
  folderParent.value = selected.value.path; folderName.value = ''; folderError.value = ''; folderOpen.value = true
}
async function createFolder() {
  if (folderBusy.value || !canEdit.value) return
  const input = newKnowledgeFolderPath(folderParent.value, folderName.value, allFolderPaths())
  folderError.value = input.error
  if (input.error) return
  const own = identity.value, id = kbId.value
  folderBusy.value = true
  try {
    await createKnowledgeFolder(id, input.path)
    if (own !== identity.value) return
    folderOpen.value = false; emitDirectoryChange(id); MessagePlugin.success('文件夹已创建。')
  } catch (e) { if (own === identity.value) folderError.value = workspaceErrorMessage(e) }
  finally { if (own === identity.value) folderBusy.value = false }
}
async function renameFolder({ from, to }: { from: string; to: string }) {
  if (!canEdit.value || !canMoveFolderTo(from, to)) return
  const own = identity.value, id = kbId.value
  try {
    await renameKnowledgeFolder(id, from, to)
    if (own !== identity.value) return
    const current = selected.value.path
    if (current === from || current.startsWith(`${from}/`)) selectFolder(to + current.slice(from.length))
    emitDirectoryChange(id); MessagePlugin.success('文件夹名称已更新。')
  } catch (e) { if (own === identity.value) MessagePlugin.error(workspaceErrorMessage(e)) }
}
async function removeFolder() {
  if (folderBusy.value || !canEdit.value || deletePath.value === null) return
  const own = identity.value, id = kbId.value, path = deletePath.value
  folderBusy.value = true; folderError.value = ''
  try {
    await deleteKnowledgeFolder(id, path)
    if (own !== identity.value) return
    deletePath.value = null
    if (selected.value.path === path) selectFolder(path.split('/').slice(0, -1).join('/'))
    emitDirectoryChange(id); MessagePlugin.success('空文件夹已删除。')
  } catch (e) { if (own === identity.value) folderError.value = workspaceErrorMessage(e) }
  finally { if (own === identity.value) folderBusy.value = false }
}
onMounted(() => {
  window.addEventListener(KNOWLEDGE_DIRECTORY_EVENT, refresh)
  window.addEventListener('knowledgeFileUploaded', refresh)
})
onBeforeUnmount(() => {
  generation++
  filesGeneration++; filesQueue.length = 0
  filesController.abort(); loadedIdentity = ''
  window.removeEventListener(KNOWLEDGE_DIRECTORY_EVENT, refresh)
  window.removeEventListener('knowledgeFileUploaded', refresh)
})
</script>

<template>
  <aside class="knowledge-workspace-menu" :class="{ 'is-collapsed': ui.sidebarCollapsed }" aria-label="知识库工作区导航">
    <div class="knowledge-workspace-menu__brand"><button v-if="!ui.sidebarCollapsed" class="knowledge-workspace-menu__plain" aria-label="返回全局知识库" @click="router.push('/platform/knowledge-bases')"><BrandWordmark compact /></button><button class="knowledge-workspace-menu__icon" :aria-label="ui.sidebarCollapsed ? '展开侧栏' : '收起侧栏'" @click="ui.toggleSidebar"><t-icon :name="ui.sidebarCollapsed ? 'chevron-right-double' : 'chevron-left-double'" /></button></div>
    <div v-if="!ui.sidebarCollapsed" class="knowledge-workspace-menu__context"><button class="knowledge-workspace-menu__back" @click="router.push('/platform/knowledge-bases')">‹ 全部知识库</button><strong>{{ library?.name || '知识库' }}</strong></div>
    <button v-else class="knowledge-workspace-menu__icon" aria-label="返回全局知识库" @click="router.push('/platform/knowledge-bases')"><t-icon name="arrow-left" /></button>
    <nav class="knowledge-workspace-menu__navigation">
      <WorkspaceNavigationNode title="问答助手" icon="chat" :collapsed="ui.sidebarCollapsed" :active="route.name === 'kbCreatChat' || route.name === 'kbChat'" @navigate="router.push(`/platform/knowledge-bases/${kbId}/creatChat`)" />
      <WorkspaceNavigationNode title="结构化数据库" icon="table" :collapsed="ui.sidebarCollapsed" :active="route.name === 'knowledgeStructuredData'" expandable :expanded="structuredOpen" @toggle="structuredOpen = !structuredOpen" @navigate="router.push(`/platform/knowledge-bases/${kbId}/structured-data`)">
        <WorkspaceNavigationNode v-for="[mode, title] in [['import','Excel 导入'],['query','数据查询']]" :key="mode" :title="title!" :depth="1" :active="route.name === 'knowledgeStructuredData' && (route.query.mode === mode || mode === 'import' && !route.query.mode)" @navigate="router.push({ path: `/platform/knowledge-bases/${kbId}/structured-data`, query: { mode } })" />
      </WorkspaceNavigationNode>
      <WorkspaceNavigationNode title="目录" icon="folder" :collapsed="ui.sidebarCollapsed" :active="isDirectory" expandable :expanded="directoryOpen" @toggle="directoryOpen = !directoryOpen" @navigate="selectFolder('')">
        <template #actions><button v-if="canEdit && !ui.sidebarCollapsed" class="knowledge-workspace-menu__icon" aria-label="新建文件夹" @click="openNewFolder"><t-icon name="folder-add" size="16px" /></button></template>
        <WorkspaceNavigationNode title="全库预览" icon="view-list" :depth="1" :active="isDirectory && selected.all" @navigate="allFiles" />
        <p v-if="error" role="alert" class="knowledge-workspace-menu__error">{{ error }}<button class="knowledge-workspace-menu__back" @click="load">重试目录</button></p>
        <KbFolderTree v-else :key="identity" :tree="tree" :selected-path="selected.path" :selected-file-id="typeof route.query.knowledge_id === 'string' ? route.query.knowledge_id : ''" :loading="loading" :can-edit="canEdit" :file-pages="filePages" show-files root-label="库根目录" navigation allow-delete @select="selectFolder" @select-file="selectFile" @load-files="loadFiles" @rename="renameFolder" @delete="(path: string) => { folderError = ''; deletePath = path }" />
      </WorkspaceNavigationNode>
    </nav>
    <UserMenu />
    <PanelResizeHandle edge="right" label="调整侧栏宽度" :value="ui.sidebarDisplayWidth" :min="SIDEBAR_COLLAPSED_WIDTH" :max="SIDEBAR_MAX_WIDTH" @start="resizeStart = ui.sidebarDisplayWidth; ui.sidebarResizing = true" @resize="(delta: number) => ui.resizeSidebar(resizeStart + delta)" @end="ui.sidebarResizing = false" />
    <t-dialog v-model:visible="folderOpen" header="新建文件夹" :confirm-btn="{ content: '创建', loading: folderBusy }" :close-on-overlay-click="!folderBusy" @confirm="createFolder"><p>位置：{{ library?.name }} / {{ folderParent || '根目录' }}</p><t-input v-model="folderName" aria-label="文件夹名称" placeholder="文件夹名称" :disabled="folderBusy" @enter="createFolder" /><t-alert v-if="folderError" theme="error" :message="folderError" /></t-dialog>
    <t-dialog :visible="deletePath !== null" header="删除空文件夹" :confirm-btn="{ content: '删除空目录', theme: 'danger', loading: folderBusy }" @update:visible="(visible: boolean) => { if (!visible) deletePath = null }" @confirm="removeFolder"><p>仅删除空目录：{{ deletePath }}。含文件或子目录时会拒绝删除。</p><t-alert v-if="folderError" theme="error" :message="folderError" /></t-dialog>
  </aside>
</template>

<style scoped>
.knowledge-workspace-menu{position:relative;display:flex;flex-direction:column;flex:none;width:var(--sidebar-width,260px);height:100%;min-height:0;box-sizing:border-box;padding:var(--app-space-3) var(--app-space-2) var(--app-space-2);background:var(--td-bg-color-sidebar);border-right:1px solid var(--app-border-color)}
.knowledge-workspace-menu__brand{display:flex;align-items:center;justify-content:space-between;gap:8px;margin:0 6px 18px}
.knowledge-workspace-menu__plain{border:0;background:transparent;padding:0;cursor:pointer;min-width:0}
.knowledge-workspace-menu__icon{display:inline-flex;align-items:center;justify-content:center;flex:none;width:26px;height:28px;color:var(--td-text-color-secondary);background:transparent;border:0;border-radius:var(--app-radius-xs);cursor:pointer}
.knowledge-workspace-menu__icon:hover{background:var(--td-bg-color-container-hover)}
.knowledge-workspace-menu__context{display:flex;flex-direction:column;gap:10px;margin:0 14px 20px;overflow-wrap:anywhere;font-size:var(--app-text-base)}
.knowledge-workspace-menu__back{border:0;background:transparent;padding:0;text-align:left;color:var(--td-brand-color);font-size:var(--app-text-sm);cursor:pointer}
.knowledge-workspace-menu__navigation{flex:1;min-height:0;overflow:auto;padding-bottom:18px}
.knowledge-workspace-menu__error{padding:12px;font-size:var(--app-text-sm);line-height:1.6;color:var(--td-error-color);overflow-wrap:anywhere}
.knowledge-workspace-menu__error button{display:block;margin-top:8px}
.knowledge-workspace-menu.is-collapsed{width:var(--sidebar-width,60px);padding:8px 3px 6px}
.knowledge-workspace-menu.is-collapsed .knowledge-workspace-menu__brand{justify-content:center;margin:0 0 12px}
</style>

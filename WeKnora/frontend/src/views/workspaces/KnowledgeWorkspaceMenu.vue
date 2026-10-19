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
    <div class="knowledge-workspace-menu__brand"><button v-if="!ui.sidebarCollapsed" type="button" class="knowledge-workspace-menu__plain" aria-label="返回全局知识库" @click="router.push('/platform/knowledge-bases')"><BrandWordmark compact /></button><button type="button" class="knowledge-workspace-menu__icon" :aria-label="ui.sidebarCollapsed ? '展开侧栏' : '收起侧栏'" :aria-expanded="!ui.sidebarCollapsed" @click="ui.toggleSidebar"><svg viewBox="0 0 20 20" width="16" height="16" fill="none" aria-hidden="true"><rect x="1.5" y="1.5" width="17" height="17" rx="3" stroke="currentColor" stroke-width="1.5"/><line x1="7.5" y1="1.5" x2="7.5" y2="18.5" stroke="currentColor" stroke-width="1.5"/></svg></button></div>
    <div v-if="!ui.sidebarCollapsed" class="knowledge-workspace-menu__context"><button type="button" class="knowledge-workspace-menu__back" @click="router.push('/platform/knowledge-bases')">‹ 全部知识库</button><strong :title="library?.name || '知识库'">{{ library?.name || '知识库' }}</strong></div>
    <button v-else type="button" class="knowledge-workspace-menu__icon" aria-label="返回全局知识库" @click="router.push('/platform/knowledge-bases')"><t-icon name="arrow-left" /></button>
    <nav id="knowledge-workspace-navigation" class="knowledge-workspace-menu__navigation" aria-label="知识库功能">
      <WorkspaceNavigationNode title="问答助手" icon="chat" :collapsed="ui.sidebarCollapsed" :active="route.name === 'kbCreatChat' || route.name === 'kbChat'" @navigate="router.push(`/platform/knowledge-bases/${kbId}/creatChat`)" />
      <WorkspaceNavigationNode title="结构化数据库" icon="table" :collapsed="ui.sidebarCollapsed" :active="route.name === 'knowledgeStructuredData'" expandable :expanded="structuredOpen" @toggle="structuredOpen = !structuredOpen" @navigate="router.push(`/platform/knowledge-bases/${kbId}/structured-data`)">
        <WorkspaceNavigationNode v-for="[mode, title, icon] in [['import','Excel 导入','file-import'],['query','数据查询','search']]" :key="mode" :title="title!" :icon="icon" :depth="1" :active="route.name === 'knowledgeStructuredData' && (route.query.mode === mode || mode === 'import' && !route.query.mode)" @navigate="router.push({ path: `/platform/knowledge-bases/${kbId}/structured-data`, query: { mode } })" />
      </WorkspaceNavigationNode>
      <WorkspaceNavigationNode title="目录" icon="folder" :collapsed="ui.sidebarCollapsed" :active="isDirectory" expandable :expanded="directoryOpen" @toggle="directoryOpen = !directoryOpen" @navigate="selectFolder('')">
        <template #actions><button v-if="canEdit && !ui.sidebarCollapsed" type="button" class="knowledge-workspace-menu__icon" aria-label="新建文件夹" @click="openNewFolder"><t-icon name="folder-add" size="16px" /></button></template>
        <WorkspaceNavigationNode title="全库预览" icon="view-list" :depth="1" :active="isDirectory && selected.all" @navigate="allFiles" />
        <p v-if="error" role="alert" class="knowledge-workspace-menu__error">{{ error }}<button type="button" class="knowledge-workspace-menu__back" @click="load">重试目录</button></p>
        <KbFolderTree v-else :key="identity" :tree="tree" :selected-path="selected.path" :selected-file-id="typeof route.query.knowledge_id === 'string' ? route.query.knowledge_id : ''" :loading="loading" :can-edit="canEdit" :file-pages="filePages" show-files root-label="库根目录" navigation allow-delete @select="selectFolder" @select-file="selectFile" @load-files="loadFiles" @rename="renameFolder" @delete="(path: string) => { folderError = ''; deletePath = path }" />
      </WorkspaceNavigationNode>
    </nav>
    <UserMenu />
    <PanelResizeHandle edge="right" label="调整侧栏宽度" :value="ui.sidebarDisplayWidth" :min="SIDEBAR_COLLAPSED_WIDTH" :max="SIDEBAR_MAX_WIDTH" @start="resizeStart = ui.sidebarDisplayWidth; ui.sidebarResizing = true" @resize="(delta: number) => ui.resizeSidebar(resizeStart + delta)" @end="ui.sidebarResizing = false" />
    <t-dialog v-model:visible="folderOpen" header="新建文件夹" :confirm-btn="{ content: '创建', loading: folderBusy }" :close-on-overlay-click="!folderBusy" @confirm="createFolder"><p>位置：{{ library?.name }} / {{ folderParent || '根目录' }}</p><t-input v-model="folderName" aria-label="文件夹名称" placeholder="文件夹名称" :disabled="folderBusy" @enter="createFolder" /><t-alert v-if="folderError" theme="error" :message="folderError" /></t-dialog>
    <t-dialog :visible="deletePath !== null" header="删除空文件夹" :confirm-btn="{ content: '删除空目录', theme: 'danger', loading: folderBusy }" @update:visible="(visible: boolean) => { if (!visible) deletePath = null }" @confirm="removeFolder"><p>仅删除空目录：{{ deletePath }}。含文件或子目录时会拒绝删除。</p><t-alert v-if="folderError" theme="error" :message="folderError" /></t-dialog>
  </aside>
</template>

<style scoped lang="less">
.knowledge-workspace-menu {
  --nav-surface: var(--cds-surface-1, var(--td-bg-color-sidebar));
  --nav-hover: var(--td-bg-color-container-hover);
  --nav-active: var(--td-bg-color-container-hover);
  --nav-accent: var(--cds-clay, var(--td-brand-color));
  --nav-text: var(--cds-text-primary, var(--td-text-color-primary));
  --nav-text-secondary: var(--cds-text-secondary, var(--td-text-color-secondary));
  position: relative;
  display: flex;
  flex: none;
  flex-direction: column;
  width: var(--sidebar-width, 260px);
  height: 100%;
  min-height: 0;
  box-sizing: border-box;
  padding: 8px;
  border-right: 1px solid var(--cds-border, var(--app-border-color));
  background: var(--nav-surface);
  color: var(--nav-text);
}

.knowledge-workspace-menu__brand {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  min-height: var(--app-sidebar-brand-height);
  margin: 0 2px 0 10px;
}

.knowledge-workspace-menu__plain {
  display: inline-flex;
  align-items: center;
  min-width: 0;
  min-height: 32px;
  padding: 0;
  border: 0;
  border-radius: var(--cds-radius-control, var(--app-radius-sm));
  background: transparent;
  cursor: pointer;
}

.knowledge-workspace-menu__icon {
  display: inline-flex;
  flex: none;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  padding: 0;
  border: 0;
  border-radius: var(--cds-radius-control, var(--app-radius-sm));
  background: transparent;
  color: var(--nav-text-secondary);
  cursor: pointer;
  transition: background-color var(--cds-duration-fast, var(--app-motion-fast)) var(--cds-ease-out, ease-out),
    color var(--cds-duration-fast, var(--app-motion-fast)) var(--cds-ease-out, ease-out);
}

.knowledge-workspace-menu__icon:hover {
  background: var(--nav-hover);
  color: var(--nav-text);
}

.knowledge-workspace-menu__icon:focus-visible,
.knowledge-workspace-menu__plain:focus-visible,
.knowledge-workspace-menu__back:focus-visible {
  outline: 2px solid var(--nav-accent);
  outline-offset: 2px;
}

.knowledge-workspace-menu__context {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
  margin: 0 12px 10px;
  font-size: var(--app-text-base);
}

.knowledge-workspace-menu__context strong {
  display: block;
  min-width: 0;
  overflow: hidden;
  color: var(--nav-text);
  font-weight: 500;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.knowledge-workspace-menu__back {
  min-height: 32px;
  padding: 0;
  border: 0;
  border-radius: var(--cds-radius-control, var(--app-radius-sm));
  background: transparent;
  color: var(--nav-text-secondary);
  font-size: var(--app-text-sm);
  text-align: left;
  cursor: pointer;
  transition: color var(--cds-duration-fast, var(--app-motion-fast)) var(--cds-ease-out, ease-out);
}

.knowledge-workspace-menu__back:hover { color: var(--nav-text); }

.knowledge-workspace-menu__navigation {
  flex: 1;
  min-height: 0;
  overflow: auto;
  padding-bottom: 18px;
  scrollbar-width: thin;
}

// Match the global sidebar's compact, neutral navigation while keeping the
// knowledge-base-specific hierarchy and expandable rows intact.
.knowledge-workspace-menu__navigation :deep(.workspace-nav-node__row) {
  min-height: var(--app-sidebar-row-height);
  margin-bottom: 1px;
  padding-left: calc(10px + var(--nav-depth) * 14px);
  padding-right: 6px;
  border-radius: var(--cds-radius-control, var(--app-radius-sm));
  color: var(--nav-text-secondary);
  transition: background-color var(--cds-duration-fast, var(--app-motion-fast)) var(--cds-ease-out, ease-out),
    color var(--cds-duration-fast, var(--app-motion-fast)) var(--cds-ease-out, ease-out);
}

.knowledge-workspace-menu__navigation :deep(.workspace-nav-node__row:hover),
.knowledge-workspace-menu__navigation :deep(.workspace-nav-node__row:focus-within) {
  background: var(--nav-hover);
  color: var(--nav-text);
}

.knowledge-workspace-menu__navigation :deep(.workspace-nav-node__row.is-active),
.knowledge-workspace-menu__navigation :deep(.workspace-nav-node__row.is-active:hover),
.knowledge-workspace-menu__navigation :deep(.workspace-nav-node__row.is-active:focus-within) {
  background: var(--nav-active);
  color: var(--nav-text);
}

.knowledge-workspace-menu__navigation :deep(.workspace-nav-node__link) {
  gap: 10px;
  padding-top: var(--app-sidebar-link-padding-y);
  padding-bottom: var(--app-sidebar-link-padding-y);
}

.knowledge-workspace-menu__navigation :deep(.workspace-nav-node__title) {
  color: inherit;
  font-size: var(--app-text-base);
  font-weight: 400;
}

.knowledge-workspace-menu__navigation :deep(.workspace-nav-node__icon) {
  width: 16px;
  height: 16px;
  color: var(--nav-text-secondary);
}

.knowledge-workspace-menu__navigation :deep(.workspace-nav-node__row.is-active .workspace-nav-node__icon) {
  color: var(--nav-text);
}

.knowledge-workspace-menu__navigation :deep(.workspace-nav-node__toggle) {
  border-radius: var(--cds-radius-control, var(--app-radius-sm));
  color: var(--nav-text-secondary);
}

.knowledge-workspace-menu__navigation :deep(.workspace-nav-node__link:focus-visible),
.knowledge-workspace-menu__navigation :deep(.workspace-nav-node__toggle:focus-visible) {
  outline-color: var(--nav-accent);
}

.knowledge-workspace-menu__navigation :deep([data-nav-depth="1"] > .workspace-nav-node__row) {
  min-height: var(--app-sidebar-row-height);
  margin-bottom: 1px;
}

.knowledge-workspace-menu__navigation :deep([data-nav-depth="1"] .workspace-nav-node__title) {
  font-size: var(--app-text-base);
  font-weight: 400;
}

.knowledge-workspace-menu__navigation :deep(.kb-folder-tree.is-navigation) {
  --kb-folder-indent: 12px;
  --kb-folder-base-indent: 20px;
}

.knowledge-workspace-menu__navigation :deep(.kb-folder-tree__body) {
  padding-top: 4px;
}

.knowledge-workspace-menu__navigation :deep(.kb-folder-row) {
  min-height: var(--app-sidebar-row-height);
  height: auto;
  margin-bottom: 1px;
  padding-top: 0;
  padding-bottom: 0;
  border-radius: var(--cds-radius-control, var(--app-radius-sm));
  color: var(--nav-text-secondary);
  font-size: var(--app-text-base);
}

.knowledge-workspace-menu__navigation :deep(.kb-folder-row:hover),
.knowledge-workspace-menu__navigation :deep(.kb-folder-row:focus-visible) {
  background: var(--nav-hover);
  color: var(--nav-text);
}

.knowledge-workspace-menu__navigation :deep(.kb-folder-row.active),
.knowledge-workspace-menu__navigation :deep(.kb-folder-row.active:hover),
.knowledge-workspace-menu__navigation :deep(.kb-folder-row.active:focus-visible) {
  background: var(--nav-active);
  color: var(--nav-text);
}

.knowledge-workspace-menu__navigation :deep(.kb-folder-row.active .kb-folder-row__label),
.knowledge-workspace-menu__navigation :deep(.kb-folder-row.active .kb-folder-row__icon) {
  color: var(--nav-text);
}

.knowledge-workspace-menu__navigation :deep(.kb-folder-row__icon),
.knowledge-workspace-menu__navigation :deep(.kb-folder-row__toggle) {
  color: var(--nav-text-secondary);
}

.knowledge-workspace-menu__navigation :deep(.kb-folder-row__more) {
  width: var(--app-sidebar-row-height);
  height: var(--app-sidebar-row-height);
  border-radius: var(--cds-radius-control, var(--app-radius-sm));
}

.knowledge-workspace-menu__navigation :deep(.kb-folder-row__trailing) {
  height: var(--app-sidebar-row-height);
}

.knowledge-workspace-menu__error {
  padding: 12px;
  color: var(--td-error-color);
  font-size: var(--app-text-sm);
  line-height: 1.6;
  overflow-wrap: anywhere;
}

.knowledge-workspace-menu__error button {
  display: block;
  margin-top: 8px;
}

.knowledge-workspace-menu.is-collapsed {
  width: var(--sidebar-width, 48px);
  padding: 8px 3px 6px;
}

.knowledge-workspace-menu.is-collapsed .knowledge-workspace-menu__brand {
  justify-content: center;
  min-height: 32px;
  margin: 0 0 6px;
}

@media (hover: none) {
  .knowledge-workspace-menu__navigation :deep(.kb-folder-row.is-editable .kb-folder-row__more) {
    opacity: 1;
    pointer-events: auto;
  }
}

@media (max-width: 720px) {
  .knowledge-workspace-menu:not(.is-collapsed) {
    position: absolute;
    z-index: 100;
    top: 0;
    bottom: 0;
    left: 0;
    width: min(var(--sidebar-width, 260px), 82vw);
    min-width: 0;
    box-shadow: 12px 0 28px color-mix(in srgb, var(--nav-text) 12%, transparent);
  }
}

.knowledge-workspace-menu__navigation .knowledge-workspace-menu__icon {
  width: var(--app-sidebar-row-height);
  height: var(--app-sidebar-row-height);
}
</style>

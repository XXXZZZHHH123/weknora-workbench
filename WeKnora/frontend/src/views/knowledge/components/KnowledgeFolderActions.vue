<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import { deleteKnowledgeFolder, renameKnowledgeFolder } from '@/api/knowledge-base'
import { useAuthStore } from '@/stores/auth'
import { KNOWLEDGE_DIRECTORY_EVENT, newKnowledgeFolderPath } from '@/views/workspaces/knowledgeDirectory'
import { workspaceErrorMessage } from '@/views/workspaces/workspaceForm'

const props = withDefaults(defineProps<{ kbId: string; folder: { path: string; name: string; total_count?: number }; canEdit: boolean; allowDelete?: boolean }>(), { allowDelete: true })
const emit = defineEmits<{ renamed: [payload: { from: string; to: string }]; deleted: [path: string] }>()
const auth = useAuthStore()
const menuOpen = ref(false), action = ref<'rename' | 'delete' | null>(null), name = ref(''), error = ref(''), busy = ref(false)
const identity = computed(() => JSON.stringify([auth.selectedTenantId ?? auth.tenant?.id, auth.user?.id, auth.token, props.kbId, props.folder.path]))
const knownDocumentCount = computed(() => Number.isSafeInteger(props.folder.total_count) && props.folder.total_count! >= 0 ? props.folder.total_count : undefined)
let epoch = 0
function reset() { epoch++; menuOpen.value = false; action.value = null; busy.value = false; error.value = '' }
watch(() => [identity.value, props.canEdit, props.allowDelete], reset, { flush: 'sync' })
onBeforeUnmount(reset)
function open(kind: 'rename' | 'delete') {
  if (!props.canEdit || !props.kbId || !props.folder.path || kind === 'delete' && !props.allowDelete) return
  menuOpen.value = false; action.value = kind; name.value = props.folder.name; error.value = ''
}
function visibleChanged(visible: boolean) { if (!visible && !busy.value) action.value = null }
async function confirm() {
  const kind = action.value
  if (!kind || busy.value || !props.canEdit || !props.folder.path || kind === 'delete' && !props.allowDelete) return
  const from = props.folder.path, parent = from.split('/').slice(0, -1).join('/')
  const input = newKnowledgeFolderPath(parent, name.value, [])
  if (kind === 'rename' && input.error) { error.value = input.error; return }
  if (kind === 'rename' && input.path === from) { action.value = null; return }
  const own = identity.value, version = epoch, id = props.kbId
  const current = () => own === identity.value && version === epoch && props.canEdit
  busy.value = true; error.value = ''
  try {
    const result: any = kind === 'rename' ? await renameKnowledgeFolder(id, from, input.path) : await deleteKnowledgeFolder(id, from, { recursive: true })
    if (!current()) return
    if (result?.success !== true) throw new Error(result?.message || '未收到操作成功确认，请重试。')
    action.value = null
    if (kind === 'rename') emit('renamed', { from, to: input.path })
    else emit('deleted', from)
    window.dispatchEvent(new CustomEvent(KNOWLEDGE_DIRECTORY_EVENT, { detail: { kbId: id, ...(kind === 'delete' ? { deletedPath: from } : {}) } }))
    MessagePlugin.success(kind === 'rename' ? '文件夹名称已更新。' : '文件夹及其内容已删除。')
  } catch (e) {
    if (current()) {
      const reason = workspaceErrorMessage(e)
      error.value = kind === 'delete' ? `删除未完成，可重试。${reason}` : reason
      // File cleanup may have partly completed. Refresh real counts without
      // announcing a successful directory deletion or changing the route.
      if (kind === 'delete') window.dispatchEvent(new CustomEvent(KNOWLEDGE_DIRECTORY_EVENT, { detail: { kbId: id } }))
    }
  }
  finally { if (current()) busy.value = false }
}
</script>

<template>
  <span v-if="canEdit && kbId && folder.path" class="knowledge-folder-actions" @click.stop @keydown.stop>
    <t-popup v-model:visible="menuOpen" trigger="click" placement="bottom-right" destroy-on-close overlay-class-name="card-more-popup">
      <button type="button" class="knowledge-folder-actions__trigger" :aria-label="`${folder.name}，文件夹更多操作`" aria-haspopup="menu" :aria-expanded="menuOpen" @keydown.down.prevent="menuOpen = true" @keydown.esc="menuOpen = false"><t-icon name="more" size="16px" /></button>
      <template #content>
        <div class="popup-menu knowledge-folder-actions__menu" role="menu" @click.stop @keydown.esc.stop="menuOpen = false">
          <button type="button" role="menuitem" class="popup-menu-item" @click="open('rename')"><t-icon name="edit" class="menu-icon" /><span>重命名</span></button>
          <button v-if="allowDelete" type="button" role="menuitem" class="popup-menu-item delete" @click="open('delete')"><t-icon name="delete" class="menu-icon" /><span>删除</span></button>
        </div>
      </template>
    </t-popup>
    <t-dialog :visible="action !== null" :header="action === 'rename' ? '重命名文件夹' : '删除文件夹'" width="400px"
      :confirm-btn="{ content: action === 'rename' ? '保存' : '删除', theme: action === 'delete' ? 'danger' : 'primary', loading: busy }"
      :cancel-btn="{ content: '取消', disabled: busy }" :close-btn="!busy" :close-on-overlay-click="!busy" :close-on-esc-keydown="!busy"
      @update:visible="visibleChanged" @confirm="confirm">
      <template v-if="action === 'rename'"><p>位置：{{ folder.path.split('/').slice(0, -1).join('/') || '库根目录' }}。子目录与文件将一起保留在改名后的目录中。</p><t-input v-model="name" aria-label="文件夹名称" placeholder="文件夹名称" :disabled="busy" autofocus @enter="confirm" /></template>
      <div v-else><p>删除“{{ folder.path }}”及其所有子文件夹和文件。此操作无法撤销。</p><p v-if="knownDocumentCount !== undefined">当前目录统计包含 {{ knownDocumentCount }} 个文件（含子目录）。</p></div>
      <t-alert v-if="error" theme="error" :message="error" class="knowledge-folder-actions__error" />
    </t-dialog>
  </span>
</template>

<style scoped lang="less">
.knowledge-folder-actions { display: inline-flex; flex: none; }
.knowledge-folder-actions__trigger { display: inline-flex; align-items: center; justify-content: center; width: 22px; height: 22px; padding: 0; border: 0; border-radius: var(--app-radius-xs); background: transparent; color: var(--td-text-color-secondary); cursor: pointer; }
.knowledge-folder-actions__trigger:hover { background: var(--td-bg-color-container-hover); }
.knowledge-folder-actions__trigger:focus-visible { outline: 2px solid var(--td-brand-color); outline-offset: 1px; }
.knowledge-folder-actions__menu button { width: 100%; border: 0; background: transparent; text-align: left; }
.knowledge-folder-actions__menu button:hover { background: var(--td-bg-color-container-hover); }
.knowledge-folder-actions__menu button.delete:hover { background: var(--td-error-color-1); }
.knowledge-folder-actions__error { margin-top: 12px; }
</style>

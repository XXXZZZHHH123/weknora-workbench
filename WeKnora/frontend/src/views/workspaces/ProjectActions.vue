<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useAuthStore } from '@/stores/auth'
import { deleteAssistantProject, updateAssistantProject, type AssistantProject } from '@/api/workspaces'
import { validateWorkspaceName, workspaceErrorMessage } from './workspaceForm'

const props = defineProps<{ project: AssistantProject; canManage?: boolean }>()
const emit = defineEmits<{ renamed: [id: string, name: string]; deleted: [id: string] }>()
const auth = useAuthStore()
const tenantId = computed(() => auth.selectedTenantId ?? auth.tenant?.id)
const canManage = computed(() => props.canManage === true && Boolean(tenantId.value && auth.user?.id && auth.token && props.project.id) && auth.hasRole('viewer'))
const scope = computed(() => JSON.stringify([tenantId.value, auth.user?.id, auth.token, props.project.id]))
const menuOpen = ref(false), action = ref<'rename' | 'delete' | null>(null), name = ref(''), error = ref(''), busy = ref(false)
let epoch = 0
function reset() { epoch++; menuOpen.value = false; action.value = null; name.value = ''; error.value = ''; busy.value = false }
watch([scope, canManage], reset, { flush: 'sync' })
onBeforeUnmount(reset)
function close() { if (!busy.value) { action.value = null; error.value = '' } }
function open(kind: 'rename' | 'delete') {
  if (busy.value || !canManage.value) return
  menuOpen.value = false; action.value = kind; name.value = props.project.name; error.value = ''
}
async function confirm() {
  const kind = action.value
  if (!kind || busy.value || !canManage.value) return
  const nextName = name.value.trim()
  error.value = kind === 'rename' ? validateWorkspaceName(nextName) : ''
  if (error.value) return
  if (kind === 'rename' && nextName === props.project.name) { close(); return }
  const own = scope.value, version = epoch, id = props.project.id
  const current = () => own === scope.value && version === epoch && canManage.value
  busy.value = true
  try {
    if (kind === 'rename') {
      const response = await updateAssistantProject(id, { name: nextName })
      if (!current()) return
      if (response?.success !== true || response.data?.id !== id || response.data.name !== nextName) throw new Error('未收到重命名成功确认，请重试。')
    } else {
      const response = await deleteAssistantProject(id)
      if (!current()) return
      if (response?.success !== true) throw new Error('未收到删除成功确认，请重试。')
    }
    if (!current()) return
    action.value = null
    window.dispatchEvent(new CustomEvent('weknora:workspaces-changed'))
    if (kind === 'rename') emit('renamed', id, nextName)
    else emit('deleted', id)
  } catch (e) { if (current()) error.value = workspaceErrorMessage(e) }
  finally { if (current()) busy.value = false }
}
</script>

<template>
  <span v-if="canManage" class="project-actions" @click.stop @keydown.stop>
    <t-popup v-model:visible="menuOpen" trigger="click" placement="bottom-right" destroy-on-close overlay-class-name="card-more-popup">
      <button type="button" class="project-actions__trigger" :aria-label="`${project.name}的更多操作`" title="更多操作" aria-haspopup="menu" :aria-expanded="menuOpen" @keydown.down.prevent="menuOpen = true" @keydown.esc="menuOpen = false">
        <t-icon name="more" size="16px" />
      </button>
      <template #content>
        <div class="popup-menu project-actions__menu" role="menu" @click.stop @keydown.esc.stop="menuOpen = false">
          <button type="button" role="menuitem" class="popup-menu-item" @click="open('rename')"><t-icon name="edit" class="menu-icon" /><span>重命名</span></button>
          <button type="button" role="menuitem" class="popup-menu-item delete" @click="open('delete')"><t-icon name="delete" class="menu-icon" /><span>删除</span></button>
        </div>
      </template>
    </t-popup>
    <t-dialog :visible="action !== null" :header="action === 'rename' ? '重命名项目' : '删除项目'" width="400px"
      :confirm-btn="{ content: action === 'rename' ? '保存' : '删除项目', theme: action === 'delete' ? 'danger' : 'primary', loading: busy }"
      :cancel-btn="{ content: '取消', disabled: busy }" :close-btn="!busy" :close-on-overlay-click="!busy" :close-on-esc-keydown="!busy"
      @update:visible="(visible: boolean) => { if (!visible) close() }" @confirm="confirm" @cancel="close" @close="close">
      <t-input v-if="action === 'rename'" v-model="name" aria-label="项目名称" placeholder="项目名称" :maxlength="80" :disabled="busy" autofocus @enter="confirm" />
      <p v-else class="project-actions__note">确定删除“{{ project.name }}”？仅删除项目与对话的归档关联，对话及消息仍保留在历史对话中。</p>
      <t-alert v-if="error" theme="error" :message="error" class="project-actions__error" />
    </t-dialog>
  </span>
</template>

<style scoped lang="less">
.project-actions { display: inline-flex; flex: none; }
.project-actions__trigger { display: inline-flex; align-items: center; justify-content: center; width: 26px; height: 26px; padding: 0; border: 0; border-radius: var(--app-radius-sm); background: transparent; color: var(--td-text-color-secondary); font: inherit; cursor: pointer; }
.project-actions__trigger:hover { background: var(--td-bg-color-container-hover); }
.project-actions__trigger:focus-visible { outline: 2px solid var(--td-brand-color); outline-offset: 1px; }
.project-actions__menu :deep(button.popup-menu-item) { width: 100%; border: 0; background: transparent; text-align: left; }
.project-actions__menu :deep(button.popup-menu-item:hover) { background: var(--td-bg-color-container-hover); }
.project-actions__menu :deep(button.popup-menu-item.delete:hover) { background: var(--td-error-color-1); }
.project-actions__note { margin: 0; color: var(--td-text-color-secondary); line-height: 1.6; }
.project-actions__error { margin-top: 12px; }
</style>

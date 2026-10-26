<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import { deleteKnowledgeBase, getKnowledgeBaseById, togglePinKnowledgeBase, updateKnowledgeBase } from '@/api/knowledge-base'
import { useAuthStore } from '@/stores/auth'
import { useChatResourcesStore } from '@/stores/chatResources'
import { useOrganizationStore } from '@/stores/organization'
import { validateWorkspaceName, workspaceErrorMessage } from '@/views/workspaces/workspaceForm'
import ResourcePermissionDrawer from '@/components/ResourcePermissionDrawer.vue'

const props = defineProps<{ knowledgeBase: { id: string; name: string; tenant_id?: string | number; system_role?: string; can_manage?: boolean; can_manage_permissions?: boolean; is_pinned?: boolean }; canRename?: boolean; canDelete?: boolean }>()
const emit = defineEmits<{ changed: [payload: { id: string; deleted: boolean; name?: string }] }>()
const auth = useAuthStore(), resources = useChatResourcesStore(), organizations = useOrganizationStore()
const menuOpen = ref(false), action = ref<'rename' | 'delete' | null>(null), name = ref(''), error = ref(''), busy = ref(false)
const identity = computed(() => JSON.stringify([auth.selectedTenantId ?? auth.tenant?.id, auth.user?.id, auth.token, props.knowledgeBase.id]))
const canManageHere = computed(() => props.canDelete && props.knowledgeBase.tenant_id != null && String(props.knowledgeBase.tenant_id) === String(auth.selectedTenantId ?? auth.tenant?.id))
const canDeleteHere = computed(() => canManageHere.value && props.knowledgeBase.system_role !== 'central')
const permissionsOpen = ref(false), pinned = ref(props.knowledgeBase.is_pinned === true)
watch(() => props.knowledgeBase.is_pinned, value => { pinned.value = value === true })
const canManagePermissions = computed(() => (props.knowledgeBase.can_manage_permissions ?? props.knowledgeBase.can_manage) === true && canManageHere.value)
let epoch = 0
function reset() { pinned.value = props.knowledgeBase.is_pinned === true; epoch++; menuOpen.value = false; action.value = null; permissionsOpen.value = false; error.value = ''; busy.value = false }
watch(() => [identity.value, props.canRename, canDeleteHere.value, canManagePermissions.value], reset, { flush: 'sync' })
onBeforeUnmount(reset)
function open(kind: 'rename' | 'delete') {
  if (!props.knowledgeBase.id || !(kind === 'rename' ? props.canRename : canDeleteHere.value)) return
  menuOpen.value = false; action.value = kind; name.value = props.knowledgeBase.name; error.value = ''
}
function visibleChanged(visible: boolean) { if (!visible && !busy.value) action.value = null }
async function confirm() {
  const kind = action.value
  if (!kind || busy.value || !(kind === 'rename' ? props.canRename : canDeleteHere.value)) return
  const nextName = name.value.trim()
  error.value = kind === 'rename' ? validateWorkspaceName(nextName) : ''
  if (error.value) return
  if (kind === 'rename' && nextName === props.knowledgeBase.name) { action.value = null; return }
  const own = identity.value, version = epoch, id = props.knowledgeBase.id
  const current = () => own === identity.value && version === epoch && (kind === 'rename' ? props.canRename : canDeleteHere.value)
  busy.value = true
  try {
    if (kind === 'rename') {
      // This PUT replaces description too. Preserve a fresh authorized value,
      // rather than silently clearing it or replaying unrelated KB settings.
      const response: any = await getKnowledgeBaseById(id)
      if (!current()) return
      const latest = response?.data
      if (response?.success !== true || latest?.id !== id || typeof latest.description !== 'string' && latest.description != null) throw new Error('知识库信息已变化，请重新打开后重试。')
      const result: any = await updateKnowledgeBase(id, { name: nextName, description: latest.description ?? '' })
      if (result?.success !== true) throw new Error(result?.message || '未收到重命名成功确认，请重试。')
    } else {
      const result: any = await deleteKnowledgeBase(id)
      if (result?.success !== true) throw new Error(result?.message || '未收到删除成功确认，请重试。')
    }
    if (!current()) return
    action.value = null
    resources.invalidate('knowledgeBases')
    void resources.ensureKnowledgeBases(true).catch(() => {})
    void organizations.fetchSharedKnowledgeBases({ force: true }).catch(() => {})
    window.dispatchEvent(new CustomEvent('weknora:workspaces-changed'))
    window.dispatchEvent(new CustomEvent('weknora:knowledge-base-changed', { detail: { id, deleted: kind === 'delete', ...(kind === 'rename' ? { name: nextName } : {}) } }))
    emit('changed', { id, deleted: kind === 'delete', ...(kind === 'rename' ? { name: nextName } : {}) })
    MessagePlugin.success(kind === 'delete' ? '知识库已删除。' : '知识库名称已更新。')
  } catch (e) { if (current()) error.value = workspaceErrorMessage(e) }
  finally { if (current()) busy.value = false }
}
async function pin() {
  if (busy.value || !props.knowledgeBase.id) return
  const own = identity.value, version = epoch, id = props.knowledgeBase.id
  busy.value = true; error.value = ''
  try {
    const response: any = await togglePinKnowledgeBase(id)
    if (own !== identity.value || version !== epoch) return
    if (response?.success !== true || response.data?.id !== id || typeof response.data.is_pinned !== 'boolean') throw new Error('未能更新置顶状态，请重试。')
    pinned.value = response.data.is_pinned
    resources.invalidate('knowledgeBases')
    window.dispatchEvent(new CustomEvent('weknora:workspaces-changed'))
    emit('changed', { id, deleted: false })
    menuOpen.value = false
  } catch (e) { if (own === identity.value && version === epoch) error.value = workspaceErrorMessage(e) }
  finally { if (own === identity.value && version === epoch) busy.value = false }
}

</script>

<template>
  <span class="knowledge-base-actions" @click.stop @keydown.stop>
    <t-popup v-model:visible="menuOpen" trigger="click" placement="bottom-right" destroy-on-close overlay-class-name="card-more-popup">
      <button type="button" class="knowledge-base-actions__trigger" aria-label="知识库更多操作" aria-haspopup="menu" :aria-expanded="menuOpen" @keydown.down.prevent="menuOpen = true" @keydown.esc="menuOpen = false">
        <t-icon name="more" size="16px" />
      </button>
      <template #content>
        <div class="popup-menu knowledge-base-actions__menu" role="menu" @click.stop @keydown.esc.stop="menuOpen = false">
          <slot name="before" :close="() => menuOpen = false"><button type="button" role="menuitem" class="popup-menu-item" :disabled="busy" @click="pin"><t-icon class="menu-icon" :name="pinned ? 'pin-filled' : 'pin'" /><span>{{ pinned ? '取消置顶' : '置顶' }}</span></button></slot>
          <button v-if="canManagePermissions" type="button" role="menuitem" class="popup-menu-item" @click="menuOpen = false; permissionsOpen = true"><t-icon name="usergroup" class="menu-icon" /><span>成员与权限</span></button>
          <button v-if="canRename" type="button" role="menuitem" class="popup-menu-item" @click="open('rename')"><t-icon name="edit" class="menu-icon" /><span>重命名</span></button>
          <button v-if="canDeleteHere" type="button" role="menuitem" class="popup-menu-item delete" @click="open('delete')"><t-icon name="delete" class="menu-icon" /><span>删除</span></button>
        </div>
      </template>
    </t-popup>
    <span v-if="error && !action" role="alert" class="knowledge-base-actions__error">{{ error }}</span>
    <t-dialog :visible="action !== null" :header="action === 'rename' ? '重命名知识库' : '删除知识库'" width="400px"
      :confirm-btn="{ content: action === 'rename' ? '保存' : '删除知识库', theme: action === 'delete' ? 'danger' : 'primary', loading: busy }"
      :cancel-btn="{ content: '取消', disabled: busy }" :close-btn="!busy" :close-on-overlay-click="!busy" :close-on-esc-keydown="!busy"
      @update:visible="visibleChanged" @confirm="confirm">
      <t-input v-if="action === 'rename'" v-model="name" aria-label="知识库名称" placeholder="知识库名称" :disabled="busy" autofocus @enter="confirm" />
      <p v-else>删除“{{ knowledgeBase.name }}”及库内资料，并解除研究工作台的此项来源。研究工作台及其他来源会保留，相关指标将不可用。此操作无法撤销。</p>
      <t-alert v-if="error" theme="error" :message="error" class="knowledge-base-actions__error" />
    </t-dialog>
    <ResourcePermissionDrawer v-if="permissionsOpen" v-model:visible="permissionsOpen" kind="knowledge-base" :resource-id="knowledgeBase.id" :resource-name="knowledgeBase.name" :system-role="knowledgeBase.system_role" />
  </span>
</template>

<style scoped lang="less">
.knowledge-base-actions { display: inline-flex; flex: none; }
.knowledge-base-actions__trigger { display: inline-flex; align-items: center; justify-content: center; width: 26px; height: 26px; padding: 0; border: 0; border-radius: var(--app-radius-sm); background: transparent; color: var(--td-text-color-secondary); cursor: pointer; }
.knowledge-base-actions__trigger:hover { background: var(--td-bg-color-container-hover); }
.knowledge-base-actions__trigger:focus-visible { outline: 2px solid var(--td-brand-color); outline-offset: 1px; }
.knowledge-base-actions__menu :deep(button.popup-menu-item) { width: 100%; border: 0; background: transparent; text-align: left; }
.knowledge-base-actions__menu :deep(button.popup-menu-item:hover) { background: var(--td-bg-color-container-hover); }
.knowledge-base-actions__menu :deep(button.popup-menu-item.delete:hover) { background: var(--td-error-color-1); }
.knowledge-base-actions__error { margin-top: 12px; }
</style>

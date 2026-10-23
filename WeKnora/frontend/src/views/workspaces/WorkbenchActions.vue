<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useAuthStore } from '@/stores/auth'
import { deleteResearchWorkbench, setResearchWorkbenchPin, updateResearchWorkbench, type ResearchWorkbench } from '@/api/workspaces'
import { validateWorkspaceName, workspaceErrorMessage, workspaceIdentity } from './workspaceForm'
import ResourcePermissionDrawer from '@/components/ResourcePermissionDrawer.vue'

const props = defineProps<{ workbench: ResearchWorkbench }>()
const emit = defineEmits<{ renamed: [workbench: ResearchWorkbench]; deleted: [id: string] }>()
const auth = useAuthStore()
const canManage = computed(() => props.workbench.can_manage === true && auth.hasRole('contributor'))
const canManagePermissions = computed(() => (props.workbench.can_manage_permissions ?? props.workbench.can_manage) === true && auth.hasRole('contributor'))
const canPin = computed(() => Boolean(auth.user?.id && props.workbench.id))
const scope = computed(() => JSON.stringify([workspaceIdentity(auth.selectedTenantId ?? auth.tenant?.id, auth.user?.id), auth.token, props.workbench.id]))
const menuOpen = ref(false), action = ref<'rename' | 'delete' | null>(null), permissionsOpen = ref(false)
const name = ref(''), error = ref(''), busy = ref(false)
let generation = 0
function close() {
  if (busy.value) return
  action.value = null; error.value = ''
}
function select(kind: 'rename' | 'delete') {
  if (busy.value || !canManage.value) return
  menuOpen.value = false; name.value = props.workbench.name; error.value = ''; action.value = kind
}
watch([scope, canManage, canManagePermissions, canPin], () => {
  generation++; menuOpen.value = false; action.value = null; permissionsOpen.value = false; name.value = ''; error.value = ''; busy.value = false
}, { flush: 'sync' })
onBeforeUnmount(() => { generation++ })
async function confirm() {
  if (busy.value || !action.value || !canManage.value) return
  error.value = action.value === 'rename' ? validateWorkspaceName(name.value) : ''
  if (error.value) return
  const own = generation, id = props.workbench.id, selected = action.value
  busy.value = true
  try {
    if (selected === 'rename') {
      const response = await updateResearchWorkbench(id, { name: name.value.trim() })
      if (own !== generation) return
      if (response?.success !== true || response.data?.id !== id || response.data.knowledge_base_id !== props.workbench.knowledge_base_id) throw new Error('未能更新工作台，请重试。')
      emit('renamed', response.data)
    } else if (selected === 'delete') {
      const response = await deleteResearchWorkbench(id)
      if (own !== generation) return
      if (response?.success !== true) throw new Error('未能删除工作台，请重试。')
      emit('deleted', id)
    }
    window.dispatchEvent(new CustomEvent('weknora:workspaces-changed'))
    action.value = null
  } catch (e) { if (own === generation) error.value = workspaceErrorMessage(e) }
  finally { if (own === generation) busy.value = false }
}
async function pin() {
  if (!canPin.value || busy.value) return
  const own = generation, id = props.workbench.id, next = !props.workbench.is_pinned
  busy.value = true; error.value = ''
  try {
    const response = await setResearchWorkbenchPin(id, next)
    if (own !== generation) return
    if (response?.success !== true || response.data?.id !== id || response.data.is_pinned !== next) throw new Error('未能更新置顶状态，请重试。')
    emit('renamed', { ...props.workbench, ...response.data })
    window.dispatchEvent(new CustomEvent('weknora:workspaces-changed'))
    menuOpen.value = false
  } catch (e) { if (own === generation) error.value = workspaceErrorMessage(e) }
  finally { if (own === generation) busy.value = false }
}
</script>

<template>
  <div v-if="canManage || canManagePermissions || canPin" class="workbench-actions" @click.stop @keydown.stop>
    <t-popup v-model:visible="menuOpen" trigger="click" placement="bottom-right" destroy-on-close overlay-class-name="card-more-popup">
      <button type="button" class="workbench-actions__trigger" :aria-label="`${workbench.name}的更多操作`" title="更多操作" aria-haspopup="menu" :aria-expanded="menuOpen" @keydown.down.prevent="menuOpen = true" @keydown.esc="menuOpen = false">
        <t-icon name="more" size="16px" />
      </button>
      <template #content>
        <div class="popup-menu workbench-actions__menu" role="menu" @click.stop @keydown.esc.stop="menuOpen = false">
          <button v-if="canManagePermissions" type="button" role="menuitem" class="popup-menu-item" @click="menuOpen = false; permissionsOpen = true"><t-icon name="usergroup" class="menu-icon" /><span>成员与权限</span></button>
          <button v-if="canPin" type="button" role="menuitem" class="popup-menu-item" :disabled="busy" @click="pin"><t-icon :name="workbench.is_pinned ? 'pin-filled' : 'pin'" class="menu-icon" /><span>{{ workbench.is_pinned ? '取消置顶' : '置顶' }}</span></button>
          <button v-if="canManage" type="button" role="menuitem" class="popup-menu-item" @click="select('rename')"><t-icon name="edit" class="menu-icon" /><span>重命名</span></button>
          <button v-if="canManage" type="button" role="menuitem" class="popup-menu-item delete" @click="select('delete')"><t-icon name="delete" class="menu-icon" /><span>删除</span></button>
        </div>
      </template>
    </t-popup>
    <p v-if="error && !action" role="alert" class="workbench-actions__error">{{ error }}</p>
    <t-dialog :visible="Boolean(action)" :header="action === 'rename' ? '重命名工作台' : '删除工作台'" width="400px"
      :confirm-btn="{ content: action === 'delete' ? '删除' : '保存', theme: action === 'delete' ? 'danger' : 'primary', loading: busy }"
      :cancel-btn="{ content: '取消', disabled: busy }" :close-btn="!busy" :close-on-overlay-click="!busy" :close-on-esc-keydown="!busy"
      @update:visible="(value: boolean) => { if (!value) close() }" @confirm="confirm" @cancel="close" @close="close">
      <t-input v-if="action === 'rename'" v-model="name" autofocus :maxlength="80" :disabled="busy" aria-label="工作台名称" @enter="confirm" />
      <p v-else class="workbench-actions__note">确定删除“{{ workbench.name }}”？绑定的知识库及其中资料会保留。</p>
      <t-alert v-if="error" theme="error" :message="error" class="workbench-actions__error" />
    </t-dialog>
    <ResourcePermissionDrawer v-if="permissionsOpen" v-model:visible="permissionsOpen" kind="research-workbench" :resource-id="workbench.id" :resource-name="workbench.name" />
  </div>
</template>

<style scoped>
.workbench-actions{display:inline-flex;align-items:center;flex:none}
.workbench-actions__trigger{display:inline-flex;align-items:center;justify-content:center;width:28px;height:28px;padding:0;border:0;border-radius:var(--app-radius-sm);background:transparent;color:var(--td-text-color-secondary);font:inherit;cursor:pointer}
.workbench-actions__trigger:hover{background:var(--td-bg-color-container-hover);color:var(--td-text-color-primary)}
.workbench-actions__trigger:focus-visible{outline:2px solid var(--td-brand-color);outline-offset:2px}
.workbench-actions__note{margin:0;color:var(--td-text-color-secondary);line-height:1.6}
.workbench-actions__error{margin-top:var(--app-space-3)}
.workbench-actions__menu :deep(button.popup-menu-item){width:100%;border:0;background:transparent;text-align:left}
.workbench-actions__menu :deep(button.popup-menu-item:hover){background:var(--td-bg-color-container-hover)}
.workbench-actions__menu :deep(button.popup-menu-item.delete:hover){background:var(--td-error-color-1)}
</style>

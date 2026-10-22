<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useAuthStore } from '@/stores/auth'
import { deleteResearchWorkbench, getResearchWorkbench, updateResearchWorkbench, type ResearchWorkbench } from '@/api/workspaces'
import { validateWorkspaceName, workspaceErrorMessage, workspaceIdentity } from './workspaceForm'
import ResourcePermissionDrawer from '@/components/ResourcePermissionDrawer.vue'
import { updateWorkbenchSourceSharing } from '@/api/resourcePermissions'
import { SOURCE_SHARING_MODULES, sourceSharingModulesDraft, type SourceSharingModule } from '@/utils/sourceSharingModules'

const props = defineProps<{ workbench: ResearchWorkbench }>()
const emit = defineEmits<{ renamed: [workbench: ResearchWorkbench]; deleted: [id: string] }>()
const auth = useAuthStore()
const canManage = computed(() => props.workbench.can_manage === true && auth.hasRole('contributor'))
const canManagePermissions = computed(() => (props.workbench.can_manage_permissions ?? props.workbench.can_manage) === true && auth.hasRole('contributor'))
const canManageSource = computed(() => props.workbench.can_manage_source === true)
const scope = computed(() => JSON.stringify([workspaceIdentity(auth.selectedTenantId ?? auth.tenant?.id, auth.user?.id), auth.token, props.workbench.id]))
const menuOpen = ref(false), action = ref<'rename' | 'delete' | 'source' | null>(null), permissionsOpen = ref(false), sourceApproved = ref(false)
const sourceModules = ref<SourceSharingModule[]>([])
const name = ref(''), error = ref(''), busy = ref(false)
let generation = 0
function close() {
  if (busy.value) return
  action.value = null; error.value = ''
}
function select(kind: 'rename' | 'delete' | 'source') {
  if (busy.value || !(kind === 'source' ? canManageSource.value : canManage.value)) return
  menuOpen.value = false; name.value = props.workbench.name; error.value = ''; action.value = kind
  sourceApproved.value = props.workbench.source_sharing_approved === true
  sourceModules.value = sourceSharingModulesDraft(props.workbench.source_sharing_modules)
}
watch([scope, canManage, canManagePermissions, canManageSource], () => {
  generation++; menuOpen.value = false; action.value = null; permissionsOpen.value = false; sourceModules.value = []; name.value = ''; error.value = ''; busy.value = false
}, { flush: 'sync' })
onBeforeUnmount(() => { generation++ })
async function confirm() {
  if (busy.value || !action.value || !(action.value === 'source' ? canManageSource.value : canManage.value)) return
  error.value = action.value === 'rename' ? validateWorkspaceName(name.value) : action.value === 'source' && sourceApproved.value && sourceSharingModulesDraft(sourceModules.value).length === 0 ? '请至少选择一个可展示数据的页面。' : ''
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
    } else {
      const response = await updateWorkbenchSourceSharing(id, sourceApproved.value, sourceApproved.value ? sourceSharingModulesDraft(sourceModules.value) : [])
      if (own !== generation) return
      if (response?.success !== true) throw new Error('未能更新来源共享授权，请重试。')
      const refreshed = await getResearchWorkbench(id)
      if (own !== generation) return
      if (refreshed?.success !== true || refreshed.data?.id !== id) throw new Error('授权已保存，工作台状态刷新失败，请重新打开。')
      emit('renamed', refreshed.data)
    }
    window.dispatchEvent(new CustomEvent('weknora:workspaces-changed'))
    action.value = null
  } catch (e) { if (own === generation) error.value = workspaceErrorMessage(e) }
  finally { if (own === generation) busy.value = false }
}
</script>

<template>
  <div v-if="canManage || canManagePermissions || canManageSource" class="workbench-actions" @click.stop @keydown.stop>
    <t-popup v-model:visible="menuOpen" trigger="click" placement="bottom-right" destroy-on-close overlay-class-name="card-more-popup">
      <button type="button" class="workbench-actions__trigger" :aria-label="`${workbench.name}的更多操作`" title="更多操作" aria-haspopup="menu" :aria-expanded="menuOpen" @keydown.down.prevent="menuOpen = true" @keydown.esc="menuOpen = false">
        <t-icon name="more" size="16px" />
      </button>
      <template #content>
        <div class="popup-menu workbench-actions__menu" role="menu" @click.stop @keydown.esc.stop="menuOpen = false">
          <button v-if="canManagePermissions" type="button" role="menuitem" class="popup-menu-item" @click="menuOpen = false; permissionsOpen = true"><t-icon name="usergroup" class="menu-icon" /><span>成员与权限</span></button>
          <button v-if="canManageSource" type="button" role="menuitem" class="popup-menu-item" @click="select('source')"><t-icon name="secured" class="menu-icon" /><span>来源共享授权</span></button>
          <button v-if="canManage" type="button" role="menuitem" class="popup-menu-item" @click="select('rename')"><t-icon name="edit" class="menu-icon" /><span>重命名</span></button>
          <button v-if="canManage" type="button" role="menuitem" class="popup-menu-item delete" @click="select('delete')"><t-icon name="delete" class="menu-icon" /><span>删除</span></button>
        </div>
      </template>
    </t-popup>
    <t-dialog :visible="Boolean(action)" :header="action === 'rename' ? '重命名工作台' : action === 'source' ? '来源共享授权' : '删除工作台'" width="400px"
      :confirm-btn="{ content: action === 'delete' ? '删除' : '保存', theme: action === 'delete' ? 'danger' : 'primary', loading: busy }"
      :cancel-btn="{ content: '取消', disabled: busy }" :close-btn="!busy" :close-on-overlay-click="!busy" :close-on-esc-keydown="!busy"
      @update:visible="(value: boolean) => { if (!value) close() }" @confirm="confirm" @cancel="close" @close="close">
      <t-input v-if="action === 'rename'" v-model="name" autofocus :maxlength="80" :disabled="busy" aria-label="工作台名称" @enter="confirm" />
      <template v-else-if="action === 'source'">
        <p class="workbench-actions__note">勾选页面可展示必要指标数值，授权作用于此台所有获授权成员。不会开放全库指标、任意查询、下载原文件或全文问答。</p>
        <label class="workbench-actions__source"><input v-model="sourceApproved" type="checkbox" aria-label="允许共享研究数据" :disabled="busy" />允许共享研究数据</label>
        <fieldset v-if="sourceApproved" class="workbench-actions__modules" :disabled="busy">
          <legend>允许展示来源数据的页面</legend>
          <label v-for="module in SOURCE_SHARING_MODULES" :key="module.key"><input v-model="sourceModules" type="checkbox" name="source-sharing-module" :value="module.key" :aria-label="module.label" /><span>{{ module.label }}<small>{{ module.hint }}</small></span></label>
        </fieldset>
      </template>
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
.workbench-actions__source{display:flex;align-items:center;gap:7px;margin-top:12px;font-size:var(--app-text-sm)}
.workbench-actions__source input,.workbench-actions__modules input{accent-color:var(--td-brand-color)}
.workbench-actions__modules{display:flex;flex-direction:column;gap:9px;margin:12px 0 0;padding:0;border:0;font-size:var(--app-text-sm)}
.workbench-actions__modules legend{padding:0 0 10px;color:var(--td-text-color-secondary);font-size:var(--app-text-xs)}
.workbench-actions__modules label{display:flex;align-items:flex-start;gap:7px;cursor:pointer}
.workbench-actions__modules input{margin:3px 0 0}.workbench-actions__modules span{display:flex;flex-direction:column;gap:2px}
.workbench-actions__modules small{color:var(--td-text-color-secondary);font-size:var(--app-text-xs);line-height:1.5}
.workbench-actions__menu :deep(button.popup-menu-item){width:100%;border:0;background:transparent;text-align:left}
.workbench-actions__menu :deep(button.popup-menu-item:hover){background:var(--td-bg-color-container-hover)}
.workbench-actions__menu :deep(button.popup-menu-item.delete:hover){background:var(--td-error-color-1)}
</style>

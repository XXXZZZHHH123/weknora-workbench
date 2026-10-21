<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import SettingDrawer from '@/components/settings/SettingDrawer.vue'
import { useAuthStore } from '@/stores/auth'
import { listMembers, type TenantMember } from '@/api/tenant/members'
import { getResourcePermissions, requestWorkbenchSourceSharing, updateResourcePermissions, updateWorkbenchSourceSharing, type ResourceAccessPolicy, type ResourceKind, type ResourcePermissionsView } from '@/api/resourcePermissions'
import { resourcePolicyDraft, setResourceMemberPermission } from '@/utils/resourcePermissions'
import { workspaceErrorMessage } from '@/views/workspaces/workspaceForm'

const props = defineProps<{ visible: boolean; resourceId: string; resourceName: string; kind: ResourceKind }>()
const emit = defineEmits<{ 'update:visible': [value: boolean]; saved: [] }>()
const auth = useAuthStore()
const details = ref<ResourcePermissionsView | null>(null), policy = ref<ResourceAccessPolicy>({ space_permission: 'none', members: [] })
const members = ref<TenantMember[]>([]), selected = ref(''), query = ref(''), total = ref(0), page = ref(1), loadedMemberCount = ref(0)
const loading = ref(false), memberLoading = ref(false), busy = ref(false), error = ref(''), memberError = ref('')
const sourceBusy = ref(''), sourceError = ref('')
const knownMembers = ref<Record<string, TenantMember>>({})
const identity = computed(() => JSON.stringify([auth.selectedTenantId ?? auth.tenant?.id, auth.user?.id, auth.token, props.kind, props.resourceId]))
const editable = computed(() => details.value?.can_manage === true)
const ownerLabel = computed(() => { const owner = knownMembers.value[details.value?.owner_id || '']; return owner?.username || owner?.email || (details.value?.owner_id === auth.user?.id ? auth.user?.username || '我' : '资源主人') })
const available = computed(() => members.value.filter(member => member.status === 'active' && member.user_id !== details.value?.owner_id && !policy.value.members.some(grant => grant.user_id === member.user_id)))
let generation = 0, memberGeneration = 0
const current = (own: number) => own === generation && props.visible
async function loadMembers(nextPage = 1) {
  const tenant = Number(auth.selectedTenantId ?? auth.tenant?.id), own = generation, search = ++memberGeneration
  if (!Number.isSafeInteger(tenant) || !tenant || !current(own)) return
  memberLoading.value = true; memberError.value = ''
  try {
    const response = await listMembers(tenant, { q: query.value, page: nextPage, page_size: 50 })
    if (!current(own) || search !== memberGeneration) return
    if (!response.success || !response.data) throw new Error(response.message || '无法读取空间成员，请重试。')
    const batch = response.data.members.filter(member => member.status === 'active')
    members.value = nextPage === 1 ? batch : [...members.value, ...batch]
    loadedMemberCount.value = (nextPage - 1) * 50 + response.data.members.length
    for (const member of batch) knownMembers.value[member.user_id] = member
    page.value = nextPage; total.value = response.data.total
  } catch (e) { if (current(own) && search === memberGeneration) memberError.value = workspaceErrorMessage(e) }
  finally { if (current(own) && search === memberGeneration) memberLoading.value = false }
}
async function load() {
  const own = ++generation
  details.value = null; policy.value = { space_permission: 'none', members: [] }; members.value = []; knownMembers.value = {}; selected.value = ''; query.value = ''; total.value = 0; page.value = 1; loadedMemberCount.value = 0; error.value = ''; memberError.value = ''; sourceBusy.value = ''; sourceError.value = ''; busy.value = false
  if (!props.visible) { loading.value = false; return }
  loading.value = true
  try {
    const response = await getResourcePermissions(props.kind, props.resourceId)
    if (!current(own)) return
    if (!response.success || !response.data) throw new Error(response.message || '无法读取资源权限，请重试。')
    details.value = response.data; policy.value = resourcePolicyDraft(response.data.policy)
    await loadMembers()
  } catch (e) { if (current(own)) error.value = workspaceErrorMessage(e) }
  finally { if (current(own)) loading.value = false }
}
function grant(userId: string, permission: 'viewer' | 'editor' | 'none') {
  if (!editable.value || busy.value || !userId || userId === details.value?.owner_id) return
  policy.value = setResourceMemberPermission(policy.value, userId, permission, knownMembers.value[userId]?.role || '')
}
function add() { grant(selected.value, 'viewer'); selected.value = '' }
async function save() {
  if (!editable.value || busy.value || loading.value || sourceBusy.value) return
  const own = generation; busy.value = true; error.value = ''
  try {
    const response = await updateResourcePermissions(props.kind, props.resourceId, policy.value)
    if (!current(own)) return
    if (!response.success) throw new Error(response.message || '未收到保存确认，请重试。')
    MessagePlugin.success('成员权限已更新。')
    window.dispatchEvent(new CustomEvent('weknora:resource-permissions-changed', { detail: { kind: props.kind, id: props.resourceId } }))
    window.dispatchEvent(new CustomEvent('weknora:workspaces-changed'))
    emit('saved'); emit('update:visible', false)
  } catch (e) { if (current(own)) error.value = workspaceErrorMessage(e) }
  finally { if (current(own)) busy.value = false }
}
async function shareSource(workbenchId: string, approved?: boolean) {
  if (!editable.value || busy.value || sourceBusy.value) return
  const own = generation; sourceBusy.value = workbenchId; sourceError.value = ''
  try {
    const response = approved === undefined ? await requestWorkbenchSourceSharing(workbenchId) : await updateWorkbenchSourceSharing(workbenchId, approved)
    if (!current(own)) return
    if (!response.success) throw new Error(response.message || '未能更新来源授权，请重试。')
    if (approved === undefined) details.value!.source_sharing_requested = true
    else {
      const request = details.value?.pending_workbenches?.find(item => item.id === workbenchId)
      if (request) request.source_sharing_approved = approved
    }
    MessagePlugin.success(approved === undefined ? '已向知识库主人发出来源共享请求。' : approved ? '已允许此工作台共享研究数据。' : '已撤销来源共享授权。')
    window.dispatchEvent(new CustomEvent('weknora:workspaces-changed'))
  } catch (e) { if (current(own)) sourceError.value = workspaceErrorMessage(e) }
  finally { if (current(own)) sourceBusy.value = '' }
}
watch(() => [props.visible, identity.value], load, { immediate: true, flush: 'sync' })
onBeforeUnmount(() => { generation++; memberGeneration++ })
</script>

<template>
  <SettingDrawer :visible="visible" title="成员与权限" :description="resourceName" icon="usergroup" width="440px" :min-width="360" :resizable="false"
    :confirm-loading="busy" :confirm-disabled="loading || !details || !editable || Boolean(sourceBusy)" :cancel-disabled="busy || Boolean(sourceBusy)" :close-on-overlay-click="!busy && !sourceBusy" :close-on-esc-keydown="!busy && !sourceBusy"
    @update:visible="value => { if (!busy && !sourceBusy) emit('update:visible', value) }" @cancel="emit('update:visible', false)" @confirm="save">
    <div class="resource-permissions">
      <t-loading v-if="loading" text="正在读取权限" />
      <t-alert v-if="error" theme="error" :message="error" />
      <template v-if="details && !loading">
        <p v-if="details.legacy" class="resource-permissions__legacy">此资源保留原有空间可见范围。保存后将按下方配置独立授权。</p>
        <div class="resource-permissions__owner"><span>{{ ownerLabel }}</span><small>主人 · 始终可管理</small></div>
        <label class="resource-permissions__space"><span>空间默认权限</span><select v-model="policy.space_permission" :disabled="!editable || busy" aria-label="空间默认权限"><option value="none">仅自己和指定成员</option><option value="viewer">所有人可查看</option><option value="editor">所有人可编辑</option></select></label>
        <p class="resource-permissions__hint">各项授权取较高权限，可编辑包含可查看；空间只读成员仍仅可查看。只有主人可修改权限、重命名或删除整个资源。</p>
        <div class="resource-permissions__section"><strong>指定成员</strong><small>{{ policy.members.length }} 人</small></div>
        <form v-if="editable" class="resource-permissions__add" @submit.prevent="add">
          <select v-model="selected" :disabled="busy || memberLoading" aria-label="选择空间成员"><option value="" disabled>选择空间成员</option><option v-for="member in available" :key="member.user_id" :value="member.user_id">{{ member.username || member.email }}</option></select>
          <t-button type="submit" size="small" theme="default" variant="outline" :disabled="!selected || busy">添加</t-button>
        </form>
        <form v-if="editable" class="resource-permissions__search" @submit.prevent="loadMembers(1)"><input v-model="query" aria-label="搜索空间成员" placeholder="搜索姓名或邮箱" :disabled="busy" /><t-button type="submit" size="small" variant="text" :loading="memberLoading">查找</t-button></form>
        <p v-if="memberError" class="resource-permissions__hint" role="alert">{{ memberError }}</p>
        <t-button v-if="loadedMemberCount < total && editable" size="small" variant="text" :loading="memberLoading" @click="loadMembers(page + 1)">加载更多成员</t-button>
        <p v-if="!policy.members.length" class="resource-permissions__empty">尚未授权指定成员</p>
        <div v-for="member in policy.members" :key="member.user_id" class="resource-permissions__member">
          <span class="resource-permissions__member-name"><span>{{ knownMembers[member.user_id]?.username || knownMembers[member.user_id]?.email || member.user_id }}</span><small v-if="knownMembers[member.user_id]?.role === 'viewer'">空间只读成员</small></span>
          <select :value="member.permission" :disabled="!editable || busy" :aria-label="`${knownMembers[member.user_id]?.username || member.user_id}的权限`" @change="grant(member.user_id, ($event.target as HTMLSelectElement).value as 'viewer' | 'editor')"><option value="viewer">可查看</option><option value="editor" :disabled="knownMembers[member.user_id]?.role === 'viewer'">可编辑</option></select>
          <button v-if="editable" type="button" class="resource-permissions__remove" :disabled="busy" :aria-label="`移除${knownMembers[member.user_id]?.username || member.user_id}的授权`" @click="grant(member.user_id, 'none')"><t-icon name="close" size="14px" /></button>
        </div>
        <p class="resource-permissions__hint">{{ kind === 'knowledge-base' ? '文件和文件夹继承本知识库权限。研究工作台单独授权。' : '只共享工作台内容，个人对话、策略和研究草稿不会因此公开。原始资料仍按知识库权限访问。' }}</p>
        <section v-if="kind === 'research-workbench' && editable && !details.can_manage_source && !details.source_sharing_approved" class="resource-permissions__source">
          <strong>研究数据来源</strong><p class="resource-permissions__hint">共享指标和产业图需知识库主人同意。请求仅向其展示本工作台名称和主人，不授予工作台内容权限。</p>
          <t-button size="small" theme="default" variant="outline" :disabled="busy || details.source_sharing_requested" :loading="sourceBusy === resourceId" @click="shareSource(resourceId)">{{ details.source_sharing_requested ? '已请求，等待知识库主人授权' : '请求来源共享授权' }}</t-button>
        </section>
        <section v-if="kind === 'knowledge-base' && editable && details.pending_workbenches?.length" class="resource-permissions__source">
          <strong>研究台数据共享请求</strong><p class="resource-permissions__hint">允许后，该研究台的授权成员可使用本库指标和产业图，不可据此浏览原库、下载原件或检索全文。</p>
          <div v-for="request in details.pending_workbenches" :key="request.id" class="resource-permissions__request"><span><span>{{ request.name }}</span><small>主人：{{ knownMembers[request.owner_id]?.username || knownMembers[request.owner_id]?.email || request.owner_id }}</small></span><t-button size="small" theme="default" variant="outline" :disabled="busy || Boolean(sourceBusy) && sourceBusy !== request.id" :loading="sourceBusy === request.id" @click="shareSource(request.id, !request.source_sharing_approved)">{{ request.source_sharing_approved ? '撤销授权' : '允许共享' }}</t-button></div>
        </section>
        <p v-if="sourceError" class="resource-permissions__hint" role="alert">{{ sourceError }}</p>
      </template>
      <t-button v-if="error && !details" size="small" variant="text" @click="load">重新加载</t-button>
    </div>
  </SettingDrawer>
</template>

<style scoped>
.resource-permissions{display:flex;flex-direction:column;gap:14px;color:var(--td-text-color-primary);font-size:var(--app-text-sm)}
.resource-permissions__owner,.resource-permissions__space,.resource-permissions__section,.resource-permissions__member{display:flex;align-items:center;gap:10px;justify-content:space-between}.resource-permissions__owner{padding-bottom:12px;border-bottom:1px solid var(--app-border-color)}
.resource-permissions small,.resource-permissions__hint,.resource-permissions__empty,.resource-permissions__legacy{font-size:var(--app-text-xs);color:var(--td-text-color-secondary)}.resource-permissions p{margin:0;line-height:1.7}.resource-permissions__legacy{padding:8px 10px;background:var(--td-bg-color-secondarycontainer);border-radius:var(--app-radius-sm)}
.resource-permissions select,.resource-permissions input{height:30px;min-width:0;border:1px solid var(--app-border-color);border-radius:var(--app-radius-sm);background:var(--app-card-bg);color:inherit;font:inherit;padding:0 7px}.resource-permissions select:focus-visible,.resource-permissions input:focus-visible{outline:2px solid var(--td-brand-color);outline-offset:2px}.resource-permissions__space select{max-width:220px}.resource-permissions__add,.resource-permissions__search{display:flex;gap:6px}.resource-permissions__add select,.resource-permissions__search input{flex:1}.resource-permissions__search{margin-top:-6px}.resource-permissions__member{min-height:40px;border-bottom:1px solid var(--app-border-color);padding-bottom:10px}.resource-permissions__member-name{display:flex;flex-direction:column;gap:3px;flex:1;min-width:0}.resource-permissions__member-name>span{overflow-wrap:anywhere}.resource-permissions__remove{display:flex;align-items:center;justify-content:center;width:26px;height:26px;border:0;border-radius:var(--app-radius-sm);background:transparent;color:var(--td-text-color-secondary);cursor:pointer}.resource-permissions__remove:hover{background:var(--td-bg-color-container-hover)}.resource-permissions__empty{padding:12px 0}.resource-permissions strong{font-weight:500}
.resource-permissions__source{display:flex;flex-direction:column;gap:9px;padding-top:12px;border-top:1px solid var(--app-border-color)}.resource-permissions__source>.t-button{align-self:flex-start}.resource-permissions__request{display:flex;align-items:center;gap:10px;justify-content:space-between}.resource-permissions__request>span{display:flex;flex-direction:column;gap:3px;min-width:0;overflow-wrap:anywhere}
</style>

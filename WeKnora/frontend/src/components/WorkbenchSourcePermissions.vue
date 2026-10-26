<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useAuthStore } from '@/stores/auth'
import { getResearchWorkbenchSources, requestResearchWorkbenchSourceSharing, updateResearchWorkbenchSourceSharing } from '@/api/workspaces'
import { SOURCE_SHARING_MODULES, sourceSharingModulesDraft, type SourceSharingModule } from '@/utils/sourceSharingModules'
import { workspaceErrorMessage } from '@/views/workspaces/workspaceForm'

const props = defineProps<{ workbenchId: string; disabled?: boolean }>()
const emit = defineEmits<{ 'busy-change': [busy: boolean] }>()
const auth = useAuthStore()
const identity = computed(() => JSON.stringify([auth.selectedTenantId ?? auth.tenant?.id, auth.user?.id, Boolean(auth.token), props.workbenchId]))
const rows = ref<Array<{ id: string; name: string; canManage: boolean; requested: boolean; approved: boolean; modules: SourceSharingModule[] }>>([])
const loading = ref(false), busy = ref(''), error = ref(''), notice = ref('')
let generation = 0
async function load() {
  const own = ++generation; rows.value = []; error.value = ''; notice.value = ''; loading.value = true
  if (busy.value) { busy.value = ''; emit('busy-change', false) }
  try {
    const response = await getResearchWorkbenchSources(props.workbenchId)
    if (own !== generation) return
    if (!response.success || !Array.isArray(response.data?.sources)) throw new Error('无法读取逐来源授权，请重试。')
    rows.value = response.data.sources.filter(source => ['active', 'unavailable'].includes(source.status)).map(source => ({ id: source.id, name: source.name || '已关联来源', canManage: source.can_manage_source, requested: source.sharing_requested === true, approved: source.sharing_approved === true, modules: sourceSharingModulesDraft(source.sharing_modules) }))
  } catch (cause) { if (own === generation) error.value = workspaceErrorMessage(cause) }
  finally { if (own === generation) loading.value = false }
}
async function request(row: (typeof rows.value)[number]) {
  if (busy.value || props.disabled || row.canManage || row.requested) return
  const own = generation; busy.value = row.id; emit('busy-change', true); error.value = ''; notice.value = ''
  try {
    const response = await requestResearchWorkbenchSourceSharing(props.workbenchId, row.id)
    if (own !== generation) return
    if (!response.success || response.data?.requested !== true) throw new Error('未收到来源请求确认，请重试。')
    row.requested = true; notice.value = '已向来源主人发出请求；批准前不展示该来源的数值。'
  } catch (cause) { if (own === generation) error.value = workspaceErrorMessage(cause) }
  finally { if (own === generation) { busy.value = ''; emit('busy-change', false) } }
}
async function save(row: (typeof rows.value)[number]) {
  if (busy.value || props.disabled || !row.canManage) return
  if (row.approved && !row.modules.length) { error.value = '请为该来源选择至少一个允许展示数值的页面。'; return }
  const own = generation; busy.value = row.id; emit('busy-change', true); error.value = ''; notice.value = ''
  try {
    const response = await updateResearchWorkbenchSourceSharing(props.workbenchId, row.id, row.approved, row.approved ? row.modules : [])
    if (own !== generation) return
    if (!response.success || response.data.approved !== row.approved) throw new Error('未收到来源授权确认，请重试。')
    row.modules = sourceSharingModulesDraft(response.data.source_sharing_modules)
    notice.value = `${row.name}的来源授权已保存。`
    window.dispatchEvent(new CustomEvent('weknora:workspaces-changed'))
  } catch (cause) { if (own === generation) error.value = workspaceErrorMessage(cause) }
  finally { if (own === generation) { busy.value = ''; emit('busy-change', false) } }
}
watch(identity, load, { immediate: true, flush: 'sync' })
onBeforeUnmount(() => { generation++; emit('busy-change', false) })
</script>

<template>
  <section class="source-permissions">
    <strong>各来源的数值展示授权</strong>
    <p>成员可见页面还需获得对应来源的批准；每个来源分别授权，不能借用其他来源的权限。</p>
    <t-loading v-if="loading" text="正在读取来源授权" />
    <details v-for="row in rows" :key="row.id">
      <summary>{{ row.name }}<small>{{ row.approved ? '已批准展示' : '尚未批准' }}</small></summary>
      <fieldset :disabled="disabled || Boolean(busy) || !row.canManage">
        <label><input v-model="row.approved" type="checkbox" />允许在受限视角展示必要数值</label>
        <div v-if="row.approved" class="source-permissions__pages"><label v-for="module in SOURCE_SHARING_MODULES" :key="module.key"><input v-model="row.modules" type="checkbox" :value="module.key" />{{ module.label }}</label></div>
      </fieldset>
      <t-button v-if="row.canManage" size="small" theme="default" variant="outline" :loading="busy === row.id" :disabled="disabled || Boolean(busy)" @click="save(row)">保存此来源授权</t-button>
      <template v-else><p>由该来源的主人或中心库管理员配置。发出请求后，对方可在来源的成员与权限中批准。</p><t-button size="small" theme="default" variant="text" :loading="busy === row.id" :disabled="disabled || Boolean(busy) || row.requested" @click="request(row)">{{ row.requested ? '已请求授权' : '请求来源主人授权' }}</t-button></template>
    </details>
    <p v-if="notice" role="status">{{ notice }}</p><t-alert v-if="error" theme="error" :message="error" />
    <t-button v-if="error" size="small" variant="text" :disabled="Boolean(busy)" @click="load">重新读取</t-button>
  </section>
</template>

<style scoped>
.source-permissions{border-top:1px solid var(--app-border-color);padding-top:14px;margin-top:16px;font-size:var(--app-text-sm)}
.source-permissions p{font-size:var(--app-text-xs);line-height:1.65;color:var(--app-text-secondary);margin:6px 0 9px}
.source-permissions details{border-bottom:1px solid var(--app-border-color);padding:8px 0}
.source-permissions summary{cursor:pointer;display:flex;gap:8px;align-items:center}.source-permissions summary small{color:var(--app-text-secondary);margin-left:auto}
.source-permissions fieldset{border:0;padding:10px 0;margin:0;min-width:0}.source-permissions label{display:flex;align-items:center;gap:6px;line-height:1.8}.source-permissions input{accent-color:var(--td-brand-color)}
.source-permissions__pages{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:4px;margin-top:8px}
</style>

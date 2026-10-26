<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useAuthStore } from '@/stores/auth'
import { useChatResourcesStore } from '@/stores/chatResources'
import { useOrganizationStore } from '@/stores/organization'
import { getResearchWorkbenchSources, listCentralDatasets, updateResearchWorkbenchSources, type CentralDataset, type ResearchWorkbenchSource, type WorkbenchSourceInput } from '@/api/workspaces'
import { sourceKey, sourceOptions, validateSourceSelection } from '@/views/research/workbenchDataSources'
import { workspaceKnowledgeBases } from './workspaceKnowledgeBases'
import { workspaceErrorMessage, workspaceIdentity } from './workspaceForm'
import WorkbenchSourcePicker from './WorkbenchSourcePicker.vue'

const props = defineProps<{ visible: boolean; workbenchId: string }>()
const emit = defineEmits<{ 'update:visible': [visible: boolean]; saved: [] }>()
const auth = useAuthStore(), resources = useChatResourcesStore(), organizations = useOrganizationStore()
const identity = computed(() => workspaceIdentity(auth.selectedTenantId ?? auth.tenant?.id, auth.user?.id))
const selected = ref<WorkbenchSourceInput[]>([]), existing = ref<ResearchWorkbenchSource[]>([]), central = ref<CentralDataset[]>([])
const revision = ref(0), loadedIdentity = ref(''), loading = ref(false), busy = ref(false), error = ref('')
const libraries = computed(() => workspaceKnowledgeBases(resources.rawKnowledgeBases, organizations.sharedKnowledgeBases, identity.value, loadedIdentity.value))
const options = computed(() => sourceOptions(libraries.value, central.value, existing.value))
let generation = 0
async function load() {
  const own = ++generation, captured = identity.value
  selected.value = []; existing.value = []; central.value = []; revision.value = 0; error.value = ''; busy.value = false
  if (!props.visible) { loading.value = false; return }
  loading.value = true
  try {
    const [sources, datasets] = await Promise.all([getResearchWorkbenchSources(props.workbenchId), listCentralDatasets(), resources.ensureKnowledgeBases(), organizations.fetchSharedKnowledgeBases({ force: true })])
    if (own !== generation || captured !== identity.value) return
    if (!sources.success || !datasets.success || !Number.isSafeInteger(sources.data.sources_revision) || sources.data.sources_revision < 1) throw new Error('无法核对来源版本，请重试。')
    loadedIdentity.value = captured; revision.value = sources.data.sources_revision; existing.value = sources.data.sources; central.value = datasets.data
    selected.value = existing.value.filter(source => ['active', 'unavailable'].includes(source.status)).map(({ source_type, source_id }) => ({ source_type, source_id }))
  } catch (cause) { if (own === generation) error.value = workspaceErrorMessage(cause) }
  finally { if (own === generation) loading.value = false }
}
async function save() {
  if (busy.value || loading.value || !revision.value) return
  error.value = validateSourceSelection(selected.value, options.value)
  if (error.value) return
  const own = generation; busy.value = true
  try {
    const response = await updateResearchWorkbenchSources(props.workbenchId, revision.value, selected.value)
    if (own !== generation) return
    const returned = response.data?.sources?.filter(source => ['active', 'unavailable'].includes(source.status)).map(sourceKey).sort()
    if (!response.success || !Number.isSafeInteger(response.data?.sources_revision) || response.data.sources_revision < revision.value || JSON.stringify(returned) !== JSON.stringify(selected.value.map(sourceKey).sort())) throw new Error('未收到来源更新确认，请刷新后重试。')
    emit('saved'); emit('update:visible', false)
    window.dispatchEvent(new CustomEvent('weknora:workspaces-changed'))
  } catch (cause) { if (own === generation) error.value = workspaceErrorMessage(cause) }
  finally { if (own === generation) busy.value = false }
}
watch(() => [props.visible, props.workbenchId, identity.value, Boolean(auth.token)], load, { immediate: true, flush: 'sync' })
onBeforeUnmount(() => { generation++ })
</script>

<template>
  <t-dialog :visible="visible" header="数据来源" width="460px" :confirm-btn="{ content: '保存', loading: busy, disabled: loading || !revision }" :cancel-btn="{ content: '取消', disabled: busy }" :close-btn="!busy" :close-on-overlay-click="!busy" :close-on-esc-keydown="!busy" @confirm="save" @cancel="emit('update:visible', false)" @close="emit('update:visible', false)">
    <t-loading v-if="loading" text="正在读取来源" />
    <WorkbenchSourcePicker v-else v-model="selected" :options="options" :disabled="busy" />
    <p class="source-note">最多关联 8 个来源，也可以暂不关联。移除来源会保留研究成果，相关指标绑定将不可用；只加载当前页面需要的数值。</p>
    <t-alert v-if="error" theme="error" :message="error" />
    <t-button v-if="error" size="small" variant="text" :disabled="busy" @click="load">重新读取</t-button>
  </t-dialog>
</template>

<style scoped>
.source-note{font-size:var(--app-text-xs);line-height:1.65;color:var(--app-text-secondary);margin:12px 0}
</style>

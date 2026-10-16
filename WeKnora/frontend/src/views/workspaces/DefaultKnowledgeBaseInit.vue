<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useAuthStore } from '@/stores/auth'
import { useChatResourcesStore } from '@/stores/chatResources'
import { getDefaultKnowledgeBase, initializeDefaultKnowledgeBase } from '@/api/workspaces'
import { workspaceErrorMessage, workspaceIdentity } from './workspaceForm'

const emit = defineEmits<{ identified: [id: string] }>()
const auth = useAuthStore(), resources = useChatResourcesStore()
const identity = computed(() => workspaceIdentity(auth.selectedTenantId ?? auth.tenant?.id, auth.user?.id))
const known = ref(false), initialized = ref(false), busy = ref(false), confirming = ref(false), error = ref('')
let generation = 0
async function load() {
  const own = ++generation
  known.value = false; initialized.value = false; error.value = ''; confirming.value = false
  emit('identified', '')
  try {
    const res = await getDefaultKnowledgeBase()
    if (own !== generation) return
    initialized.value = !!res.data; known.value = true
    if (res.data) emit('identified', res.data.knowledge_base_id)
  } catch (e) { if (own === generation) error.value = workspaceErrorMessage(e) }
}
async function initialize() {
  if (busy.value || !auth.hasRole('contributor')) return
  const own = generation
  busy.value = true; error.value = ''
  try {
    const result = await initializeDefaultKnowledgeBase()
    if (own !== generation) return
    resources.invalidate('knowledgeBases'); initialized.value = true; confirming.value = false
    emit('identified', result.data.knowledge_base_id)
    window.dispatchEvent(new CustomEvent('weknora:workspaces-changed'))
  } catch (e) { if (own === generation) error.value = workspaceErrorMessage(e) }
  finally { if (own === generation) busy.value = false }
}
watch(identity, () => { busy.value = false; void load() }, { immediate: true })
onBeforeUnmount(() => { generation++ })
</script>

<template>
  <t-tooltip v-if="!initialized && auth.hasRole('contributor')" :content="error || '初始化 Default 宏观知识库'" placement="right"><t-button variant="text" theme="default" size="small" :disabled="!known && !error" aria-label="初始化 Default 宏观知识库" @click="error && !known ? load() : confirming = true"><template #icon><t-icon name="earth" size="16px" /></template></t-button></t-tooltip>
  <t-dialog v-model:visible="confirming" header="初始化宏观知识库" :confirm-btn="{ content: '创建 Default 知识库', loading: busy }" @confirm="initialize"><p>建立当前空间的宏观知识库，作为各研究工作台的默认共用来源。请在本库导入并确认数据；创建知识库不会自动采集数据或调用模型。</p><t-alert v-if="error" theme="error" :message="error" /></t-dialog>
</template>

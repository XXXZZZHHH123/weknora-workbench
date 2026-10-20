<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { listResearchWorkbenches, type ResearchWorkbench } from '@/api/workspaces'
import { workspaceErrorMessage, workspaceIdentity } from './workspaceForm'

const props = defineProps<{ kbId: string }>()
const router = useRouter(), auth = useAuthStore()
const scope = computed(() => `${workspaceIdentity(auth.selectedTenantId ?? auth.tenant?.id, auth.user?.id)}:${props.kbId}`)
const workbenches = ref<ResearchWorkbench[]>([]), loading = ref(false), loaded = ref(false), error = ref(''), choosing = ref(false)
let generation = 0
async function load() {
  const own = ++generation
  loaded.value = false; workbenches.value = []; choosing.value = false; error.value = ''; loading.value = true
  if (!props.kbId) { loading.value = false; return }
  try {
    const res = await listResearchWorkbenches({ knowledge_base_id: props.kbId })
    if (own !== generation) return
    if (!Array.isArray(res.data)) throw new Error('未能读取工作台绑定状态。')
    workbenches.value = res.data.filter(w => w.knowledge_base_id === props.kbId)
    loaded.value = true
  } catch (e) { if (own === generation) error.value = workspaceErrorMessage(e) }
  finally { if (own === generation) loading.value = false }
}
watch(scope, load, { immediate: true })
const workspacesChanged = () => { void load() }
onMounted(() => window.addEventListener('weknora:workspaces-changed', workspacesChanged))
onBeforeUnmount(() => { generation++; window.removeEventListener('weknora:workspaces-changed', workspacesChanged) })
function navigate() {
  if (!loaded.value) return
  if (!workbenches.value.length) router.push({ path: '/platform/research-workbenches/new', query: { kb_id: props.kbId } })
  else if (workbenches.value.length === 1) router.push(`/platform/research-workbenches/${workbenches.value[0]!.id}`)
  else choosing.value = true
}
</script>

<template>
  <t-button v-if="error" variant="outline" size="small" :title="error" @click="load">重试工作台绑定</t-button>
  <t-button v-else variant="outline" size="small" :loading="loading" :disabled="!loaded" data-testid="kb-workbench-link" @click="navigate">{{ workbenches.length ? '转到工作台' : '创建工作台' }}</t-button>
  <t-dialog v-model:visible="choosing" header="选择工作台" :footer="false"><div v-for="workbench in workbenches" :key="workbench.id" class="knowledge-workbench-choice"><div><strong>{{ workbench.name }}</strong><p>{{ workbench.description }}</p></div><t-button @click="router.push(`/platform/research-workbenches/${workbench.id}`)">转到工作台</t-button></div></t-dialog>
</template>

<style scoped>
.knowledge-workbench-choice{display:flex;align-items:center;justify-content:space-between;gap:16px;padding:12px 0;border-bottom:1px solid var(--app-border-color)}
.knowledge-workbench-choice p{font-size:var(--app-text-md);color:var(--app-text-secondary)}
</style>

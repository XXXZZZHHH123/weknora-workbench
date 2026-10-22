<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useAuthStore } from '@/stores/auth'
import { refreshStructuredFrame } from '@/utils/authFetch'
import { useTheme } from '@/composables/useTheme'
import { getKnowledgeBaseById } from '@/api/knowledge-base'
import { workspaceErrorMessage, workspaceIdentity } from '@/views/workspaces/workspaceForm'
import { isTrustedStructuredFrameMessage, structuredFrameAuthPayload } from '@/utils/structuredFrameHost'

const props = withDefaults(defineProps<{
  knowledgeBaseId: string
  sourceKnowledgeId?: string
  sourceFileType?: 'pdf' | 'xlsx'
  mode?: 'import' | 'query'
  view?: string
  title?: string
}>(), { sourceKnowledgeId: '', sourceFileType: 'xlsx', mode: 'import', view: '', title: '当前知识库结构化数据工作台' })
const emit = defineEmits<{ navigate: [view: string] }>()
const auth = useAuthStore()
const { effectiveTheme } = useTheme()
const tenant = computed(() => auth.selectedTenantId ?? auth.tenant?.id)
const scope = computed(() => `${workspaceIdentity(tenant.value, auth.user?.id)}:${props.knowledgeBaseId}`)
const library = ref<any>(null), frame = ref<HTMLIFrameElement>(), frameGeneration = ref(0), loading = ref(false), error = ref('')
const src = computed(() => `/structured/?session=${frameGeneration.value}${props.view === 'model-settings' ? '&view=model-settings' : ''}`)
let generation = 0

async function load() {
  const own = ++generation
  library.value = null; error.value = ''; loading.value = true; frameGeneration.value++
  try {
    const detail = await getKnowledgeBaseById(props.knowledgeBaseId)
    if (own !== generation) return
    library.value = detail.data || detail
  } catch (e) { if (own === generation) error.value = workspaceErrorMessage(e) }
  finally { if (own === generation) loading.value = false }
}
function sendView() { frame.value?.contentWindow?.postMessage({ type: 'structured-view', mode: props.mode }, window.location.origin) }
function sendTheme() { frame.value?.contentWindow?.postMessage({ type: 'workspace-theme', mode: effectiveTheme.value }, window.location.origin) }
function sendAuth() {
  if (!library.value || !auth.token || !tenant.value) return
  frame.value?.contentWindow?.postMessage(structuredFrameAuthPayload({ token: auth.token, tenant: tenant.value, knowledgeBaseId: props.knowledgeBaseId, sourceKnowledgeId: props.sourceKnowledgeId, sourceFileType: props.sourceFileType }), window.location.origin)
  sendView()
}
function receive(event: MessageEvent) {
  if (!isTrustedStructuredFrameMessage(event, frame.value, window.location.origin)) return
  if (event.data?.type === 'structured-refresh-auth') { void refreshStructuredFrame(event, () => frame.value?.contentWindow === event.source); return }
  if (event.data?.type === 'workspace-theme-ready') { sendTheme(); return }
  if (event.data?.type === 'structured-ready') sendAuth()
  if (event.data?.type === 'structured-navigate') emit('navigate', String(event.data.view || ''))
}
watch(scope, load, { immediate: true })
watch(() => auth.token, sendAuth)
watch(() => [props.sourceKnowledgeId, props.sourceFileType], sendAuth)
watch(() => props.mode, sendView)
watch(effectiveTheme, sendTheme)
onMounted(() => window.addEventListener('message', receive))
onBeforeUnmount(() => { generation++; window.removeEventListener('message', receive) })
</script>

<template>
  <div class="knowledge-structured-frame">
    <div v-if="error" class="knowledge-structured-frame__status" role="alert">
      <p>{{ error }}</p><t-button variant="outline" @click="load">重试</t-button>
    </div>
    <div v-else-if="loading" class="knowledge-structured-frame__status"><t-loading text="正在打开当前知识库" /></div>
    <iframe v-else-if="library" :key="frameGeneration" ref="frame" :src="src" :title="title" @load="sendAuth" />
  </div>
</template>

<style scoped>
.knowledge-structured-frame { display: flex; flex: 1; width: 100%; min-width: 0; min-height: 0; background: var(--app-card-bg); color: var(--td-text-color-primary); }
.knowledge-structured-frame iframe { display: block; flex: 1; width: 100%; height: 100%; min-width: 0; min-height: 0; border: 0; background: var(--app-card-bg); }
.knowledge-structured-frame__status { display: grid; place-content: center; gap: 12px; width: 100%; min-height: 220px; text-align: center; }
</style>

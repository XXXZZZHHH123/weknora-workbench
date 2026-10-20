<template>
  <SpotlightGuide v-if="page && steps.length" :key="`${scopeKey}:${page}:${frameVersion}`" v-model:active="active"
    :steps="steps" :target-frame="frame" labels-prefix="contextualGuide" @dismiss="acknowledge('skipped')" @finish="acknowledge('completed')" />
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import { useAuthStore } from '@/stores/auth'
import type { ResearchWorkbench } from '@/api/workspaces'
import SpotlightGuide from '@/components/SpotlightGuide.vue'
import { isGlobalUserGuideDone } from '@/config/contextualGuides'
import { finishResearchGuide, readResearchGuide, researchFeatureSteps, researchGuideFramePage, researchGuideKey, type ResearchGuidePage, type ResearchGuideScope } from './researchFeatureGuides'

const props = defineProps<{ workbench: ResearchWorkbench; frame?: HTMLIFrameElement; frameVersion: number; suppressed?: boolean }>()
const auth = useAuthStore()
const active = ref(false), page = ref<ResearchGuidePage | null>(null)
const scope = computed<ResearchGuideScope>(() => ({ tenantId: String(auth.selectedTenantId ?? auth.tenant?.id ?? ''), userId: auth.user?.id || '', workbenchId: props.workbench.id, knowledgeBaseId: props.workbench.knowledge_base_id }))
const scopeKey = computed(() => researchGuideKey(scope.value))
const steps = computed(() => page.value ? researchFeatureSteps(page.value, openPanel) : [])
let epoch = 0, concluded = false, stopped = false, openedLogic = false
let timer: ReturnType<typeof setTimeout> | undefined
let observedWindow: Window | null = null
function storage() { try { return window.localStorage } catch { return null } }
function currentFrame() {
  try {
    const frame = props.frame, native = frame?.contentWindow as (Window & { RESEARCH_CONTEXT?: unknown }) | null
    if (!auth.token || props.workbench.workspace_mode !== 'isolated' || !frame?.isConnected || !native || !frame.contentDocument
        || native.frameElement !== frame || frame.contentDocument.defaultView !== native) return null
    const currentPage = researchGuideFramePage(scope.value, { origin: native.location.origin, pathname: native.location.pathname, context: native.RESEARCH_CONTEXT }, window.location.origin)
    return currentPage ? { page: currentPage, document: frame.contentDocument, window: native } : null
  } catch { return null }
}
function clearTimer() { if (timer) { clearTimeout(timer); timer = undefined } }
function pause() { epoch++; clearTimer(); active.value = false; page.value = null; openedLogic = false }
function acknowledge(outcome: 'skipped' | 'completed') {
  if (concluded || !page.value) return
  const target = currentFrame(), currentPage = page.value
  concluded = true; active.value = false; page.value = null; clearTimer()
  const local = storage()
  if (target?.page !== currentPage || !local) return
  if (!finishResearchGuide(local, scope.value, currentPage, outcome)) MessagePlugin.warning('本次引导已关闭；浏览器未允许保存引导进度。')
}
async function openPanel(panel: 'market' | 'logic' | 'logic-analysis' | 'logic-close' | 'diagram' | 'diagram-close') {
  const owner = epoch, current = currentFrame()
  if (!current || current.page !== page.value) { pause(); return }
  const doc = current.document
  if (panel === 'market') {
    if (!doc.querySelector<HTMLDialogElement>('#market-core-dialog')?.open) doc.querySelector<HTMLButtonElement>('#market-core-open')?.click()
  } else if (panel === 'logic' || panel === 'logic-analysis') {
    if (!doc.querySelector<HTMLDialogElement>('#judgment-manager')?.open) { openedLogic = true; doc.querySelector<HTMLButtonElement>('[data-open-logic]')?.click() }
    if (panel === 'logic-analysis') doc.querySelector<HTMLButtonElement>('[data-logic-tab="analyze"]')?.click()
  } else if (panel === 'logic-close') {
    if (openedLogic) doc.querySelector<HTMLButtonElement>('[data-logic-close]')?.click()
    openedLogic = false
  } else if (panel === 'diagram-close') doc.querySelector<HTMLButtonElement>('#dg-md-drawer button[onclick*="dgCloseMarkdown"]')?.click()
  else if (doc.querySelector('#dg-md-drawer')?.classList.contains('translate-x-full')) doc.querySelector<HTMLButtonElement>('#dg-md-btn')?.click()
  // Opening Markdown serializes the current canvas without saving a layout;
  // wait for its normal drawer animation before measuring the real control.
  await nextTick()
  if (owner !== epoch) return
  if (currentFrame()?.document !== doc) pause()
}
function openWhenAvailable(owner: number) {
  if (stopped || owner !== epoch || props.suppressed) return
  const current = currentFrame(), local = storage()
  if (!current || !local || readResearchGuide(local, scope.value)?.pages[current.page] !== 'pending') return
  if (!isGlobalUserGuideDone() || document.querySelector('.guide[role="dialog"]')) {
    timer = setTimeout(() => openWhenAvailable(owner), 400); return
  }
  page.value = current.page; concluded = false
  void nextTick(() => { if (!stopped && owner === epoch && !props.suppressed && currentFrame()?.document === current.document) active.value = true })
}
function stopObserving() {
  try { observedWindow?.removeEventListener('pagehide', pause) } catch { /* A frame may have navigated away from this origin. */ }
  observedWindow = null
}
watch(() => [scopeKey.value, props.frame, props.frameVersion, props.suppressed, auth.token] as const, () => {
  pause(); stopObserving()
  observedWindow = currentFrame()?.window ?? null
  observedWindow?.addEventListener('pagehide', pause)
  if (!props.suppressed) timer = setTimeout(() => openWhenAvailable(epoch), 400)
}, { immediate: true })
onBeforeUnmount(() => { stopped = true; pause(); stopObserving() })
</script>

<template>
  <SpotlightGuide v-model:active="active" :steps="steps" labels-prefix="contextualGuide" :busy="saving" :close-on-finish="false"
    @dismiss="dismiss" @finish="finish">
    <template #notice>
      <p v-if="saveError" class="research-guide-notice is-error" role="alert">{{ saveError }} 请重试当前操作。</p>
      <p v-else-if="saving" class="research-guide-notice" role="status">正在保存引导进度…</p>
    </template>
  </SpotlightGuide>
</template>

<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import SpotlightGuide from '@/components/SpotlightGuide.vue'
import { isGlobalUserGuideDone } from '@/config/contextualGuides'
import type { SpotlightGuideStep } from '@/types/spotlightGuide'

const props = defineProps<{ steps: SpotlightGuideStep[]; saving?: boolean; saveError?: string }>()
const emit = defineEmits<{ finish: []; dismiss: [] }>()
const active = ref(false)
let dismissed = false, stopped = false
let timer: ReturnType<typeof setTimeout> | undefined

function finish() {
  const wasDismissed = dismissed
  dismissed = false
  if (props.saving) return
  if (wasDismissed) emit('dismiss'); else emit('finish')
}
function dismiss() { dismissed = !props.saving }
function openWhenAvailable() {
  if (stopped) return
  if (!isGlobalUserGuideDone() || document.querySelector('.guide[role="dialog"]')) {
    timer = setTimeout(openWhenAvailable, 400)
    return
  }
  void nextTick(() => { if (!stopped) active.value = true })
}
onMounted(openWhenAvailable)
onBeforeUnmount(() => { stopped = true; if (timer) clearTimeout(timer) })
</script>

<style scoped>
.research-guide-notice{margin:10px 0 0;font-size:var(--app-text-xs);line-height:1.6;color:var(--app-text-secondary)}
.research-guide-notice.is-error{color:var(--td-error-color)}
</style>

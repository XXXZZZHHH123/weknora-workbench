<template>
  <div v-if="active" class="answer-progress" role="status" aria-live="polite">
    <span class="answer-progress-dot" aria-hidden="true" />
    <span>{{ stageText }}</span>
    <span class="answer-progress-time" aria-live="off">{{ t('chat.answerProgress.elapsed', { seconds: elapsed }) }}</span>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { answerProgressStage } from '@/utils/answerProgress'

const props = defineProps<{ session?: {
  id?: string | number; request_id?: unknown; created_at?: unknown; is_completed?: boolean
  agentEventStream?: Array<Record<string, unknown>>
} }>()
const { t } = useI18n()
const now = ref(Date.now())
const fallbackStartedAt = ref(now.value)
let timer: ReturnType<typeof setInterval> | undefined
const active = computed(() => props.session?.is_completed === false)
const elapsed = computed(() => {
  const created = typeof props.session?.created_at === 'string' ? Date.parse(props.session.created_at) : NaN
  return Math.max(0, Math.floor((now.value - (Number.isFinite(created) ? created : fallbackStartedAt.value)) / 1000))
})
const stageText = computed(() => {
  switch (answerProgressStage(props.session?.agentEventStream)) {
    case 'sources': return t('chat.answerProgress.sources')
    case 'data': return t('chat.answerProgress.data')
    case 'generation': return t('chat.answerProgress.generation')
    case 'verification': return t('chat.answerProgress.verification')
    case 'processing': return t('chat.answerProgress.processing')
    default: return t('chat.answerProgress.waiting')
  }
})
watch(() => [props.session?.id, props.session?.request_id, active.value], () => {
  if (timer) clearInterval(timer)
  timer = undefined
  now.value = Date.now()
  fallbackStartedAt.value = now.value
  if (active.value) timer = setInterval(() => { now.value = Date.now() }, 1000)
}, { immediate: true })
onBeforeUnmount(() => { if (timer) clearInterval(timer) })
</script>

<style scoped>
.answer-progress { display: flex; align-items: center; gap: 8px; padding: 4px 0 10px; color: var(--td-text-color-secondary); font-size: 13px; line-height: 22px; }
.answer-progress-dot { width: 6px; height: 6px; flex: none; border-radius: 50%; background: var(--td-brand-color); animation: answer-progress-pulse 1.5s ease-in-out infinite; }
.answer-progress-time { margin-left: auto; color: var(--td-text-color-placeholder); white-space: nowrap; font-variant-numeric: tabular-nums; }
@keyframes answer-progress-pulse { 50% { opacity: .35; } }
@media (prefers-reduced-motion: reduce) { .answer-progress-dot { animation: none; } }
</style>

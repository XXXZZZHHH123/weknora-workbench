<template>
  <div class="shell-exec-result">
    <p v-if="failed" class="shell-exec-note" role="status">{{ $t('chat.executionFailed', 'This operation did not finish. Check your input and try again.') }}</p>
    <div v-if="durationLabel || view.truncated" class="shell-exec-meta">
      <span v-if="durationLabel">{{ durationLabel }}</span>
      <span v-if="view.truncated">{{ $t('agentStream.shellExec.truncated') }}</span>
    </div>

    <div v-if="view.stdoutBinary || view.stderrBinary" class="shell-exec-note">
      {{ $t('agentStream.shellExec.binarySuppressed') }}
    </div>

    <div v-if="view.stdout" class="shell-exec-stream">
      <div class="shell-exec-stream-label">{{ $t('agentStream.shellExec.stdout') }}</div>
      <pre class="shell-exec-stream-body">{{ view.stdout }}</pre>
    </div>

    <ErrorDiagnostics :detail="diagnostics" />

    <div v-if="!failed && !view.stdout && !view.stdoutBinary" class="shell-exec-empty">
      {{ $t('agentStream.shellExec.emptyOutput') }}
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import ErrorDiagnostics from '@/components/ErrorDiagnostics.vue'
import type { ShellJobPayload } from '@/api/chat/background-work'
import type { ShellExecData } from '@/types/tool-results'
import { buildShellExecView } from '@/utils/shellExecResult'

const props = defineProps<{
  data?: ShellExecData | ShellJobPayload | Record<string, unknown> | null
  output?: string
  arguments?: Record<string, unknown>
}>()

const source = computed(() => {
  const value = props.data
  if (value && typeof value === 'object') return value as unknown as Record<string, unknown>
  return null
})

const view = computed(() =>
  buildShellExecView(source.value, props.arguments, props.output),
)

const failed = computed(() =>
  view.value.killed ||
  (view.value.exitCode !== null && view.value.exitCode !== 0) ||
  ['failed', 'lost', 'cancelled'].includes(String(source.value?.state)),
)
const diagnostics = computed(() => [
  view.value.command,
  view.value.workDir,
  view.value.exitCode === null ? '' : `Exit code: ${view.value.exitCode}`,
  view.value.stderr,
  source.value?.note,
].filter(Boolean).join('\n'))

const durationLabel = computed(() => {
  const ms = view.value.durationMs
  if (ms == null) return ''
  if (ms < 1000) return `${Math.round(ms)}ms`
  const seconds = ms / 1000
  if (seconds < 10) return `${seconds.toFixed(1)}s`
  return `${Math.round(seconds)}s`
})
</script>

<style lang="less" scoped>
.shell-exec-result {
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-width: 0;
}

.shell-exec-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 12px;
  font-size: var(--app-text-sm);
  line-height: 1.4;
  color: var(--td-text-color-placeholder);
}

.shell-exec-note,
.shell-exec-empty {
  font-size: var(--app-text-sm);
  line-height: 1.4;
  color: var(--td-text-color-placeholder);
}

.shell-exec-stream {
  min-width: 0;
  border: 1px solid var(--td-component-stroke);
  border-radius: var(--app-radius-sm);
  overflow: hidden;
  background: var(--td-bg-color-container);
}

.shell-exec-stream-label {
  padding: 6px 12px;
  font-size: var(--app-text-sm);
  font-weight: 500;
  line-height: 1.4;
  color: var(--td-text-color-secondary);
  background: var(--td-bg-color-secondarycontainer);
  border-bottom: 1px solid var(--td-component-stroke);
}

.shell-exec-stream-body {
  margin: 0;
  padding: 10px 12px;
  max-height: 280px;
  overflow: auto;
  font-family: var(--app-font-family-mono);
  font-size: var(--app-text-sm);
  line-height: 1.55;
  color: var(--td-text-color-primary);
  white-space: pre-wrap;
  word-break: break-word;

  &::-webkit-scrollbar {
    width: 8px;
    height: 8px;
  }

  &::-webkit-scrollbar-thumb {
    background: var(--td-component-border);
    border-radius: var(--app-radius-xs);
  }
}
</style>

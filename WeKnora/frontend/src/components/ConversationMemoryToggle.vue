<script setup lang="ts">
defineProps<{ disabled: boolean; locked?: boolean }>()
const emit = defineEmits<{ 'update:disabled': [value: boolean] }>()
</script>

<template>
  <button type="button" class="conversation-memory-toggle" :class="{ active: disabled }"
    :aria-pressed="disabled" :aria-disabled="locked || undefined"
    :title="locked ? '此对话不读取或写入长期记忆，对话记录仍保留。该设置由此对话及其分支持续遵守。' : '为新对话关闭长期记忆。对话创建后设置固定；对话记录仍保留，可从历史中删除。'"
    @click="!locked && emit('update:disabled', !disabled)">
    <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" aria-hidden="true">
      <rect x="5" y="10" width="14" height="11" rx="2" />
      <path d="M8 10V7a4 4 0 0 1 8 0v3M12 14v3" />
    </svg>
    <span>{{ disabled ? '本次不记忆' : '长期记忆' }}</span>
  </button>
</template>

<style scoped>
.conversation-memory-toggle { display:inline-flex; align-items:center; gap:4px; border:1px solid var(--td-component-border); border-radius:var(--app-radius-md); padding:3px 7px; background:transparent; color:var(--td-text-color-secondary); font:inherit; font-size:var(--app-text-sm); cursor:pointer; white-space:nowrap; }
.conversation-memory-toggle.active { color:var(--td-brand-color); border-color:var(--td-brand-color-light); background:var(--td-brand-color-light); }
.conversation-memory-toggle[aria-disabled="true"] { cursor:default; }
.conversation-memory-toggle:focus-visible { outline:2px solid var(--td-brand-color); outline-offset:2px; }
</style>

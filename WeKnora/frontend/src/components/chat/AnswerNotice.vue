<template>
  <span class="answer-notice">
    <t-tooltip :content="notice" placement="top-right" :visible="hovered || focused"
      :overlay-inner-style="{ maxWidth: 'min(320px, calc(100vw - 32px))', whiteSpace: 'normal', lineHeight: '1.6' }"
      @visible-change="hovered = $event">
      <button type="button" class="answer-notice__trigger" :aria-label="notice"
        @focus="focused = true" @blur="focused = false" @keydown.esc="focused = hovered = false">
        <ChatActionIcon name="info-circle" />
      </button>
    </t-tooltip>
  </span>
</template>

<script setup lang="ts">
import { ref } from 'vue';
import ChatActionIcon from '@/components/ChatActionIcon.vue';

defineProps<{ notice: string }>();
const hovered = ref(false);
const focused = ref(false);
</script>

<style scoped>
.answer-notice { float: inline-end; margin: 0 0 2px 8px; line-height: 1; }
.answer-notice__trigger {
  display: inline-flex; align-items: center; justify-content: center;
  width: 24px; height: 24px; padding: 0; border: 0; border-radius: 50%;
  color: var(--td-text-color-secondary); background: transparent; cursor: help;
}
.answer-notice__trigger:hover, .answer-notice__trigger:focus-visible {
  color: var(--td-text-color-primary); background: var(--td-bg-color-container-hover);
}
.answer-notice__trigger:focus-visible { outline: 2px solid var(--td-brand-color); outline-offset: 2px; }
.answer-notice__trigger :deep(svg) { width: 15px; height: 15px; }
</style>

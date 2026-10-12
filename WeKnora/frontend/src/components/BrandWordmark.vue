<template>
  <span class="brand-wordmark" :class="{ 'brand-wordmark--compact': compact }" aria-label="中哲物产 · 研究工作台">
    <strong>中哲物产</strong>
    <span>研究工作台</span>
  </span>
</template>

<script setup lang="ts">
defineProps<{ compact?: boolean }>()
</script>

<style scoped>
.brand-wordmark { display: inline-flex; flex-direction: column; gap: var(--app-space-1); text-align: left; white-space: nowrap; }
.brand-wordmark strong { color: var(--td-text-color-primary); font-size: var(--app-text-3xl); font-weight: 650; letter-spacing: .12em; line-height: 1.15; }
.brand-wordmark > span { color: var(--td-text-color-secondary); font-size: var(--app-text-sm); letter-spacing: .18em; }
.brand-wordmark--compact strong { font-size: var(--app-text-xl); }
.brand-wordmark--compact > span { font-size: var(--app-text-2xs); letter-spacing: .14em; }
</style>
